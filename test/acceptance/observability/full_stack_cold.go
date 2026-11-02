package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

var fullStackObservationServices = []string{"langfuse-web", "langfuse-worker", "langfuse-clickhouse", "langfuse-redis", "langfuse-minio", "prometheus", "alloy", "loki", "grafana"}

func collectFullStackCold(ctx context.Context, project, output string) error {
	io := dockerIntervalCollector(project)
	io.probe = probeFullStackColdServices
	return collectIntervalsWithServices(ctx, project, output, fullStackObservationServices, io, func() {
		fmt.Printf("collector_ready project=%s scope=full_stack_cold\n", project)
	}, saveIntervals)
}

// Exact inspection is sufficient for the cold state. Running is unknown, never unavailable.
func probeFullStackColdServices(ctx context.Context, project string) ([]ServiceProbe, error) {
	selected, err := inspectOwnedServiceContainers(ctx, project)
	if err != nil {
		return nil, err
	}
	probes := make([]ServiceProbe, 0, len(fullStackObservationServices))
	for _, service := range fullStackObservationServices {
		c := selected[service]
		p := ServiceProbe{Project: project, Service: service, ContainerID: c.ID, StartedAt: c.StartedAt, From: time.Now().UTC(), State: "unavailable", Kind: "exact_inspect"}
		if c.Running {
			p.State = "unknown"
		}
		p.To = time.Now().UTC()
		probes = append(probes, p)
	}
	return probes, nil
}

func readFullStackInput(output, name string) ([]byte, error) {
	if filepath.Base(name) != name {
		return nil, fmt.Errorf("noncanonical evidence name")
	}
	path := filepath.Join(output, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("evidence %s is not a bounded regular file", name)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20+1))
	if err != nil || len(data) > 16<<20 {
		return nil, fmt.Errorf("evidence %s exceeds read bound: %w", name, err)
	}
	return data, nil
}

func fullStackInputDigest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

func verifyFullStackCold(project, output string) error {
	return verifyFullStackColdWithWriter(project, output, writeEvidence)
}

func verifyFullStackColdWithWriter(project, output string, save fullStackEvidenceWriter) error {
	if _, err := readClaim(project, output); err != nil {
		return err
	}
	if _, err := readFullStackInput(output, "claim.json"); err != nil {
		return err
	}
	digests := map[string]string{}
	read := func(name string, dst any) error {
		data, err := readFullStackInput(output, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err = json.Unmarshal(data, dst); err != nil {
			return fmt.Errorf("decode %s: %w", name, err)
		}
		digests[name] = fullStackInputDigest(data)
		return nil
	}
	var before, after ProjectCapture
	var pending, passed RawEvidence
	for _, item := range []struct {
		name string
		dst  any
	}{{"cold-before.json", &before}, {"cold-after.json", &after}, {"smoke-pending.json", &pending}, {"smoke-passed.json", &passed}} {
		if err := read(item.name, item.dst); err != nil {
			return err
		}
	}
	if before.Project != project || after.Project != project || pending.Project != project || passed.Project != project {
		return fmt.Errorf("full-stack cold claim mismatch")
	}
	if !passed.CapturedAt.After(after.CompletedAt) || passed.CapturedAt.Before(pending.CapturedAt) {
		return fmt.Errorf("recovered snapshot was not captured after cold window")
	}
	if err := verifyColdStartForServices(before, after, fullStackObservationServices); err != nil {
		return err
	}
	if err := verifyColdSmokeWindow(before, after, pending); err != nil {
		return err
	}
	if err := verifyFullStackTurns(before, after, pending, passed); err != nil {
		return err
	}
	sample, err := supplementEvidenceSample(pending, passed)
	if err != nil {
		return err
	}
	if sample.Measurement.Project != project {
		return fmt.Errorf("cold measurement claim mismatch")
	}
	files, err := filepath.Glob(filepath.Join(output, "intervals-*.json"))
	if err != nil || len(files) == 0 {
		return fmt.Errorf("full-stack interval file missing: %w", err)
	}
	physical := PhysicalEvidence{Project: project, ColdBefore: before, ColdAfter: after, Cold: sample.Measurement}
	continuousColdSubscription := false
	for _, path := range files {
		var part IntervalEvidence
		if err := read(filepath.Base(path), &part); err != nil {
			return err
		}
		if len(part.Subscriptions) != 1 || part.Subscriptions[0].End != "requested_stop" {
			return fmt.Errorf("interval file subscription count invalid")
		}
		derived, err := part.derive(project)
		if err != nil || len(derived) == 0 || !reflect.DeepEqual(derived, part.Intervals) {
			return fmt.Errorf("interval file source mismatch: %w", err)
		}
		if part.validateWindowForServices(project, "unavailable", before.StartedAt, after.CompletedAt, fullStackObservationServices) == nil {
			continuousColdSubscription = true
		}
		physical.Intervals.Subscriptions = append(physical.Intervals.Subscriptions, part.Subscriptions...)
	}
	if !continuousColdSubscription {
		return fmt.Errorf("no uninterrupted nine-service subscription covers full cold window")
	}
	physical.Intervals.Intervals, err = physical.Intervals.derive(project)
	if err != nil {
		return err
	}
	if err := physical.validateCold(); err != nil {
		return err
	}
	var backendImage, fixtureImage, backendID string
	for _, c := range after.Containers {
		switch c.Service {
		case "switchback-backend":
			backendImage, backendID = c.ImageID, c.ID
		case "observation-fake-model":
			fixtureImage = c.ImageID
			for _, expected := range pending.Expected {
				if expected.FixtureContainerID != c.ID {
					return fmt.Errorf("fixture identity changed")
				}
			}
		}
	}
	if !digestPattern.MatchString(backendImage) || !digestPattern.MatchString(fixtureImage) || backendID == "" {
		return fmt.Errorf("Backend/fixture image identity missing")
	}
	result := struct {
		Schema             string            `json:"schema_version"`
		Project            string            `json:"project"`
		Scope              string            `json:"scope"`
		Status             string            `json:"status"`
		BackendContainerID string            `json:"backend_container_id"`
		BackendImageDigest string            `json:"backend_image_digest"`
		FixtureImageDigest string            `json:"controlled_fixture_image_digest"`
		Measurement        Measurement       `json:"original_cold_measurement_and_verified_sources"`
		EvidenceDigests    map[string]string `json:"evidence_digests"`
	}{"observability.full_stack_cold.v1", project, "cold_start_only", "passed", backendID, backendImage, fixtureImage, sample.Measurement, digests}
	return save(output, "full-stack-cold-passed.json", result)
}

type fullStackTurn struct {
	ID         string    `json:"agent_turn_id"`
	SessionID  string    `json:"worker_session_id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	TerminalAt time.Time `json:"terminal_at"`
}

func verifyFullStackTurns(before, after ProjectCapture, pending, passed RawEvidence) error {
	var original, recovered []fullStackTurn
	if json.Unmarshal(pending.Turns, &original) != nil || json.Unmarshal(passed.Turns, &recovered) != nil || len(original) != 2 || len(recovered) != 2 {
		return fmt.Errorf("cold original/recovered two-Turn identity missing")
	}
	measurement, err := measurementFromRaw(pending)
	if err != nil {
		return err
	}
	backendStart := time.Time{}
	for _, c := range after.Containers {
		if c.Service == "switchback-backend" {
			backendStart = c.StartedAt
		}
	}
	sort.Slice(original, func(i, j int) bool { return original[i].ID < original[j].ID })
	sort.Slice(recovered, func(i, j int) bool { return recovered[i].ID < recovered[j].ID })
	if !reflect.DeepEqual(original, recovered) || original[0].ID == original[1].ID {
		return fmt.Errorf("cold original Turn identity/timing changed after recovery")
	}
	seen := map[string]bool{}
	for _, turn := range original {
		if turn.ID == "" || turn.SessionID != measurement.SessionID || turn.Status != "completed" || turn.CreatedAt.Before(backendStart) || turn.CreatedAt.Before(before.CompletedAt) || turn.TerminalAt.Before(turn.CreatedAt) || turn.TerminalAt.After(pending.CapturedAt) {
			return fmt.Errorf("cold original Turn lifecycle outside startup window")
		}
		seen[turn.ID] = true
	}
	for _, id := range measurement.TurnIDs {
		if !seen[id] {
			return fmt.Errorf("cold measurement Turn differs from original lifecycle")
		}
	}
	return nil
}
