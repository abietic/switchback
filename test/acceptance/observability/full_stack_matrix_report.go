package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

type fullStackMatrixResult struct {
	Schema          string                      `json:"schema_version"`
	Project         string                      `json:"project"`
	Scope           string                      `json:"scope"`
	Boundary        string                      `json:"boundary"`
	Status          string                      `json:"status"`
	EvidenceDigests map[string]string           `json:"input_digests"`
	PairCount       int                         `json:"pair_count"`
	Pairs           []fullStackMatrixPairResult `json:"pairs"`
	HealthyP95      [3]float64                  `json:"healthy_p95_ms"`
	OutageP95       [3]float64                  `json:"outage_p95_ms"`
}

type fullStackMatrixPairResult struct {
	HealthyID string `json:"healthy_measurement_id"`
	OutageID  string `json:"outage_measurement_id"`
}

func matrixCaptureContainer(c ProjectCapture, service string) (CapturedContainer, bool) {
	var result CapturedContainer
	found := false
	for _, item := range c.Containers {
		if item.Service == service {
			if found {
				return CapturedContainer{}, false
			}
			result, found = item, true
		}
	}
	return result, found
}

func verifyMatrixWorkerCapture(initial, final fullStackWorkerCapture, session, runtime string, generation uint64) error {
	a, b := initial.Container, final.Container
	for _, capture := range []fullStackWorkerCapture{initial, final} {
		if capture.Labels["com.switchback.worker-session-id"] != session || capture.Labels["com.switchback.runtime-instance-id"] != runtime || capture.Labels["com.switchback.runtime-generation"] != fmt.Sprint(generation) {
			return fmt.Errorf("resident Worker identity labels missing/changed")
		}
	}
	if initial.Session != session || final.Session != session || initial.Runtime != runtime || final.Runtime != runtime || initial.Generation != generation || final.Generation != generation || a.ID == "" || a.ID != b.ID || a.ImageID == "" || a.ImageID != b.ImageID || !a.StartedAt.Equal(b.StartedAt) || !a.Running || !b.Running || initial.ObservedAt.IsZero() || final.ObservedAt.Before(initial.ObservedAt) {
		return fmt.Errorf("resident Worker container identity/generation changed")
	}
	return nil
}

func verifyMatrixCapture(initial, final ProjectCapture, smoke RawEvidence, fixtureID string) error {
	if initial.Schema != "observability.project_capture.v1" || final.Schema != initial.Schema || initial.Project != final.Project || initial.Project != smoke.Project || initial.StartedAt.IsZero() || initial.CompletedAt.Before(initial.StartedAt) || final.CompletedAt.Before(final.StartedAt) || final.StartedAt.Before(initial.StartedAt) {
		return fmt.Errorf("matrix capture scope/order invalid")
	}
	for _, service := range []string{"switchback-backend", "postgres", "observation-fake-model"} {
		a, ok := matrixCaptureContainer(initial, service)
		b, ok2 := matrixCaptureContainer(final, service)
		if !ok || !ok2 || a.ID == "" || a.ID != b.ID || !a.StartedAt.Equal(b.StartedAt) || a.ImageID != b.ImageID || !a.Running || !b.Running || a.Project != initial.Project || b.Project != initial.Project {
			return fmt.Errorf("matrix core %s replaced/unhealthy", service)
		}
		if service == "postgres" && (a.Health == nil || b.Health == nil || *a.Health != "healthy" || *b.Health != "healthy") {
			return fmt.Errorf("matrix postgres health missing")
		}
		if service == "observation-fake-model" && a.ID != fixtureID {
			return fmt.Errorf("matrix fixture identity changed")
		}
	}
	for _, capture := range []ProjectCapture{initial, final} {
		if capture.BackendHealth == nil || capture.BackendHealth.Status != 200 || capture.BackendHealth.Error || capture.BackendHealth.StartedAt.Before(capture.StartedAt) || capture.BackendHealth.CompletedAt.After(capture.CompletedAt) {
			return fmt.Errorf("matrix Backend health missing")
		}
		seen := map[string]bool{}
		for _, c := range capture.Containers {
			if c.Project != capture.Project || c.ID == "" || seen[c.Service] {
				return fmt.Errorf("matrix capture duplicate/cross-project container")
			}
			seen[c.Service] = true
		}
		for _, service := range fullStackObservationServices {
			c, ok := matrixCaptureContainer(capture, service)
			if !ok || !c.Running {
				return fmt.Errorf("matrix final service %s not running", service)
			}
		}
	}
	return nil
}

func verifyMatrixTurns(original, recovered RawEvidence, m Measurement) error {
	var a, b []fullStackTurn
	if json.Unmarshal(original.Turns, &a) != nil || json.Unmarshal(recovered.Turns, &b) != nil || len(a) != 2 || len(b) != 2 || !reflect.DeepEqual(a, b) {
		return fmt.Errorf("matrix completed Turn rows missing/changed")
	}
	seen := map[string]bool{}
	for _, turn := range a {
		if turn.ID == "" || seen[turn.ID] || turn.SessionID != m.SessionID || turn.Status != "completed" || turn.CreatedAt.IsZero() || turn.TerminalAt.Before(turn.CreatedAt) || turn.TerminalAt.After(original.CapturedAt) {
			return fmt.Errorf("matrix completed Turn identity/time invalid")
		}
		seen[turn.ID] = true
	}
	for i, id := range m.TurnIDs {
		if !seen[id] || a[i].ID != id || a[i].CreatedAt.After(m.Settlements[i].From) || a[i].TerminalAt.After(m.Settlements[i].To) {
			return fmt.Errorf("matrix Turn/journal timestamp mismatch")
		}
	}
	if m.Dispatch.To.Before(a[1].CreatedAt) {
		return fmt.Errorf("matrix next dispatch ACK precedes second Turn creation")
	}
	return nil
}

func verifyMatrixSubscription(project, state string, m Measurement, e IntervalEvidence, worker fullStackWorkerCapture) error {
	if len(e.Subscriptions) != 1 || e.Subscriptions[0].End != "requested_stop" {
		return fmt.Errorf("matrix measurement requires one completed subscription")
	}
	stream := e.Subscriptions[0]
	if stream.Scope != "project_and_exact_worker" || stream.WorkerContainerID == "" || stream.WorkerContainerID != worker.Container.ID {
		return fmt.Errorf("matrix subscription lacks exact resident Worker coverage")
	}
	if stream.From.After(m.From) || stream.To.Before(m.To) {
		return fmt.Errorf("matrix subscription does not span measurement")
	}
	for _, event := range stream.Events {
		if event.Service == "resident-worker" || event.ContainerID == worker.Container.ID {
			if event.Service != "resident-worker" || event.ContainerID != worker.Container.ID || event.Project != project || event.WorkerSessionID != worker.Session || event.RuntimeID != worker.Runtime || event.Generation != worker.Generation {
				return fmt.Errorf("matrix subscription Worker event identity invalid")
			}
		}
		if event.Service == "switchback-backend" || event.Service == "postgres" || strings.Contains(event.Service, "worker") && !strings.HasPrefix(event.Service, "langfuse-") || event.Service == "observation-fake-model" {
			if event.Action != "exec_create" && event.Action != "exec_start" && event.Action != "exec_die" && event.Action != "health_status: healthy" {
				return fmt.Errorf("matrix core lifecycle changed in subscription")
			}
		}
	}
	return e.validateWindowForServices(project, state, m.From, m.To, fullStackObservationServices)
}

func verifyFullStackMatrix(project, output string) error {
	return verifyFullStackMatrixWithWriter(project, output, writeEvidence)
}

func verifyFullStackMatrixWithWriter(project, output string, save fullStackEvidenceWriter) error {
	if _, err := readClaim(project, output); err != nil {
		return err
	}
	digests := map[string]string{}
	read := func(name string, dst any) error {
		data, err := readFullStackInput(output, name)
		if err != nil {
			return fmt.Errorf("matrix read %s: %w", name, err)
		}
		if err = json.Unmarshal(data, dst); err != nil {
			return fmt.Errorf("matrix decode %s: %w", name, err)
		}
		digests[name] = fullStackInputDigest(data)
		return nil
	}
	var recipe fullStackMatrixRecipe
	if err := read("full-stack-matrix-recipe.json", &recipe); err != nil {
		return err
	}
	if recipe.Schema != "observability.full_stack_matrix_recipe.v1" || recipe.Project != project || recipe.Smoke != "smoke-passed.json" || recipe.Started != "full-stack-matrix-started.json" || recipe.Initial != "full-stack-matrix-initial.json" || recipe.Final != "full-stack-matrix-final.json" || recipe.WorkerInitial != "full-stack-matrix-worker-initial.json" || recipe.WorkerFinal != "full-stack-matrix-worker-final.json" || len(recipe.Pairs) != 20 {
		return fmt.Errorf("matrix recipe scope/count invalid")
	}
	var started struct {
		Project, Session, Runtime, Fixture string
		Generation                         uint64
		At                                 time.Time
	}
	var smoke RawEvidence
	var initial, final ProjectCapture
	var workerInitial, workerFinal fullStackWorkerCapture
	for _, item := range []struct {
		name string
		dst  any
	}{{recipe.Started, &started}, {recipe.Smoke, &smoke}, {recipe.Initial, &initial}, {recipe.Final, &final}, {recipe.WorkerInitial, &workerInitial}, {recipe.WorkerFinal, &workerFinal}} {
		if err := read(item.name, item.dst); err != nil {
			return err
		}
	}
	if started.Project != project || started.Session == "" || started.Runtime == "" || started.Generation == 0 || started.Fixture == "" || started.At.Before(initial.StartedAt) || initial.CompletedAt.After(started.At) || final.StartedAt.Before(started.At) {
		return fmt.Errorf("matrix started claim binding invalid")
	}
	smokeSample, err := supplementEvidenceSample(smoke, smoke)
	if err != nil || smoke.Project != project || smokeSample.Measurement.SessionID != started.Session || smokeSample.Measurement.RuntimeID != started.Runtime || smokeSample.Measurement.Generation != started.Generation {
		return fmt.Errorf("matrix smoke identity/source invalid: %w", err)
	}
	if err = verifyMatrixCapture(initial, final, smoke, started.Fixture); err != nil {
		return err
	}
	if err = verifyMatrixWorkerCapture(workerInitial, workerFinal, started.Session, started.Runtime, started.Generation); err != nil {
		return err
	}
	var healthy, outage [3][]float64
	seenFiles, seenIDs, seenTurns := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, pair := range recipe.Pairs {
		for j, window := range []fullStackMatrixWindow{pair.Healthy, pair.Outage} {
			state := "healthy"
			if j == 1 {
				state = "unavailable"
			}
			nonce := fmt.Sprintf("%sf%02d%s", strings.TrimPrefix(project, "switchback-obsv-"), i, map[string]string{"healthy": "h", "unavailable": "o"}[state])
			if window.Original != "sample-"+nonce+"-original.json" || window.Recovered != "sample-"+nonce+"-recovered.json" || !strings.HasPrefix(window.Intervals, "intervals-") || !strings.HasSuffix(window.Intervals, ".json") {
				return fmt.Errorf("matrix canonical window path invalid")
			}
			for _, name := range []string{window.Original, window.Recovered, window.Intervals} {
				if seenFiles[name] {
					return fmt.Errorf("matrix duplicate evidence path")
				}
				seenFiles[name] = true
			}
			var original, recovered RawEvidence
			var intervals IntervalEvidence
			for _, item := range []struct {
				name string
				dst  any
			}{{window.Original, &original}, {window.Recovered, &recovered}, {window.Intervals, &intervals}} {
				if err = read(item.name, item.dst); err != nil {
					return err
				}
			}
			if len(intervals.Subscriptions) != 1 || window.Intervals != "intervals-"+intervals.Subscriptions[0].ID+".json" {
				return fmt.Errorf("matrix interval filename/subscription mismatch")
			}
			if original.MeasurementID != nonce || recovered.MeasurementID != nonce || original.Project != project || recovered.Project != project || seenIDs[nonce] {
				return fmt.Errorf("matrix duplicate/cross-claim measurement")
			}
			seenIDs[nonce] = true
			sample, e := supplementEvidenceSample(original, recovered)
			if e != nil {
				return e
			}
			m := sample.Measurement
			if m.SessionID != started.Session || m.RuntimeID != started.Runtime || m.Generation != started.Generation || original.CapturedAt.Before(started.At) || recovered.CapturedAt.Before(original.CapturedAt) || recovered.CapturedAt.After(final.StartedAt) {
				return fmt.Errorf("matrix resident scope or timing changed")
			}
			if err = verifyMatrixTurns(original, recovered, m); err != nil {
				return err
			}
			if err = verifyMatrixSubscription(project, state, m, intervals, workerInitial); err != nil {
				return err
			}
			for _, id := range m.TurnIDs {
				if seenTurns[id] {
					return fmt.Errorf("matrix repeats Turn")
				}
				seenTurns[id] = true
			}
			for _, source := range m.Expected {
				if source.FixtureContainerID != started.Fixture {
					return fmt.Errorf("matrix source fixture changed")
				}
			}
			for k, v := range []float64{m.Settlements[0].Milliseconds, m.Settlements[1].Milliseconds, m.Dispatch.Milliseconds} {
				if j == 0 {
					healthy[k] = append(healthy[k], v)
				} else {
					outage[k] = append(outage[k], v)
				}
			}
		}
	}
	if len(seenTurns) != 80 {
		return fmt.Errorf("matrix requires eighty unique completed Turns")
	}
	result := fullStackMatrixResult{Schema: "observability.full_stack_matrix.v1", Project: project, Scope: "paired_outage_only", Boundary: "controlled ACP/model/Tool; not real Codex, not G5, not full logging/metrics G4", Status: "passed", EvidenceDigests: digests, PairCount: 20}
	for _, pair := range recipe.Pairs {
		result.Pairs = append(result.Pairs, fullStackMatrixPairResult{HealthyID: strings.TrimSuffix(strings.TrimPrefix(pair.Healthy.Original, "sample-"), "-original.json"), OutageID: strings.TrimSuffix(strings.TrimPrefix(pair.Outage.Original, "sample-"), "-original.json")})
	}
	for k := 0; k < 3; k++ {
		sort.Float64s(healthy[k])
		sort.Float64s(outage[k])
		index := int(math.Ceil(.95*float64(len(healthy[k])))) - 1
		result.HealthyP95[k], result.OutageP95[k] = healthy[k][index], outage[k][index]
		if result.OutageP95[k]-result.HealthyP95[k] > 1000 {
			return fmt.Errorf("matrix metric %d outage p95 increase exceeds 1000ms", k)
		}
	}
	return save(output, "full-stack-matrix-passed.json", result)
}
