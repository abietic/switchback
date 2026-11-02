package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"time"
)

// ReportRecipe contains evidence paths, never caller-supplied latency or pass flags.
type ReportRecipe struct {
	Physical  string `json:"physical_evidence,omitempty"`
	ColdStart *struct {
		Before string `json:"before"`
		After  string `json:"after"`
		Smoke  string `json:"smoke"`
	} `json:"cold_start,omitempty"`
	SourceCommit string `json:"source_commit"`
	WorkerImage  string `json:"worker_image_digest"`
	BackendImage string `json:"backend_image_digest"`
	Pairs        []struct {
		Healthy EvidencePaths `json:"healthy"`
		Outage  EvidencePaths `json:"outage"`
	} `json:"pairs"`
}
type EvidencePaths struct {
	Original  string `json:"original"`
	Recovered string `json:"recovered"`
}
type RawEvidence struct {
	MeasurementID   string          `json:"measurement_id"`
	Expected        []ExpectedModel `json:"expected_models"`
	FragmentProofs  []FragmentProof `json:"fragment_proofs"`
	LegacyDigest    string          `json:"v2_bytes_digest"`
	CapturedAt      time.Time       `json:"captured_at"`
	FixtureChecksum string          `json:"fixture_checksum"`
	Project         string          `json:"project"`
	Controlled      bool            `json:"controlled_acp_fixture"`
	Settlements     []struct {
		TurnID       string    `json:"agent_turn_id"`
		MessageID    string    `json:"terminal_message_id"`
		From         time.Time `json:"terminal_receipt_at"`
		To           time.Time `json:"completed_at"`
		Milliseconds *float64  `json:"terminal_receipt_to_settlement_ms"`
	} `json:"measured_settlements"`
	Dispatch *struct {
		TurnID       string    `json:"agent_turn_id"`
		MessageID    string    `json:"command_message_id"`
		From         time.Time `json:"terminal_receipt_at"`
		To           time.Time `json:"acknowledged_at"`
		Milliseconds *float64  `json:"terminal_receipt_to_next_dispatch_ms"`
	} `json:"measured_next_dispatch"`
	Sources []json.RawMessage `json:"verified_original_sources"`
	Jobs    []struct {
		Stream   string `json:"trace_stream_id"`
		Checksum string `json:"manifest_checksum"`
		Status   string `json:"status"`
	} `json:"jobs"`
	Turns    json.RawMessage `json:"turns"`
	Runtimes json.RawMessage `json:"runtimes"`
}

func evidenceSample(paths EvidencePaths) (Sample, error) {
	read := func(path string) (RawEvidence, error) {
		var r RawEvidence
		b, e := os.ReadFile(path)
		if e == nil {
			e = json.Unmarshal(b, &r)
		}
		return r, e
	}
	before, err := read(paths.Original)
	if err != nil {
		return Sample{}, err
	}
	after, err := read(paths.Recovered)
	if err != nil {
		return Sample{}, err
	}
	if len(before.FragmentProofs) > 0 || len(after.FragmentProofs) > 0 {
		return supplementEvidenceSample(before, after)
	}
	if !before.Controlled || !after.Controlled || before.Project == "" || before.Project != after.Project || len(before.Sources) != 2 || len(after.Sources) != 2 || len(after.Jobs) != 2 || !reflect.DeepEqual(before.Sources, after.Sources) {
		return Sample{}, fmt.Errorf("original source identity/bytes changed or missing")
	}
	if before.FixtureChecksum == "" || before.FixtureChecksum != after.FixtureChecksum {
		return Sample{}, fmt.Errorf("fixture checksum changed or missing")
	}
	var objects []struct {
		Source struct {
			TraceStreamID    string
			ManifestChecksum string
		}
	}
	for _, raw := range after.Sources {
		var obj struct {
			Source struct {
				TraceStreamID    string
				ManifestChecksum string
			}
		}
		if err = json.Unmarshal(raw, &obj); err != nil {
			return Sample{}, err
		}
		objects = append(objects, obj)
	}
	for _, source := range objects {
		found := false
		for _, job := range after.Jobs {
			if job.Stream == source.Source.TraceStreamID && job.Checksum == source.Source.ManifestChecksum && job.Status == "passed" {
				found = true
			}
		}
		if !found {
			return Sample{}, fmt.Errorf("original exact source job not passed")
		}
	}
	// Runtime rows include mutable heartbeat/lease timestamps; compare stable identities only.
	identity := func(raw json.RawMessage) ([]map[string]any, error) {
		var rows []map[string]any
		if e := json.Unmarshal(raw, &rows); e != nil {
			return nil, e
		}
		result := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			result = append(result, map[string]any{"runtime_instance_id": row["runtime_instance_id"], "worker_session_id": row["worker_session_id"], "generation": row["generation"]})
		}
		return result, nil
	}
	bIDs, err := identity(before.Runtimes)
	if err != nil {
		return Sample{}, err
	}
	aIDs, err := identity(after.Runtimes)
	if err != nil {
		return Sample{}, err
	}
	if len(bIDs) != 1 || !reflect.DeepEqual(bIDs, aIDs) || !reflect.DeepEqual(before.Turns, after.Turns) {
		return Sample{}, fmt.Errorf("duplicate execution or identity changed during recovery")
	}
	if before.Dispatch == nil || before.Dispatch.Milliseconds == nil || len(before.Settlements) != 2 {
		return Sample{}, fmt.Errorf("exact measured latency evidence missing")
	}
	sample := Sample{ID: before.Project, NextDispatchMS: *before.Dispatch.Milliseconds, SourceVerified: true, IdentityStable: true}
	for _, s := range before.Settlements {
		if s.Milliseconds == nil {
			return Sample{}, fmt.Errorf("settlement latency missing")
		}
		sample.SettlementsMS = append(sample.SettlementsMS, *s.Milliseconds)
	}
	return sample, nil
}
func produceReport(recipePath, output string) error {
	data, err := os.ReadFile(recipePath)
	if err != nil {
		return err
	}
	var recipe ReportRecipe
	if err = json.Unmarshal(data, &recipe); err != nil {
		return err
	}
	report := Report{Mode: "full", ControlledACP: true, SourceCommit: recipe.SourceCommit, WorkerImage: recipe.WorkerImage, BackendImage: recipe.BackendImage, Coverage: map[string]string{}}
	if recipe.Physical != "" {
		raw, e := os.ReadFile(recipe.Physical)
		if e != nil {
			return e
		}
		var physical PhysicalEvidence
		if e = json.Unmarshal(raw, &physical); e != nil {
			return e
		}
		report.Physical = &physical
	}
	if recipe.ColdStart != nil {
		var before, after ProjectCapture
		var smoke RawEvidence
		for _, item := range []struct {
			path  string
			value any
		}{{recipe.ColdStart.Before, &before}, {recipe.ColdStart.After, &after}, {recipe.ColdStart.Smoke, &smoke}} {
			b, e := os.ReadFile(item.path)
			if e != nil {
				return e
			}
			if e = json.Unmarshal(b, item.value); e != nil {
				return e
			}
		}
		if err = verifyColdStart(before, after); err != nil {
			return err
		}
		if smoke.Project != before.Project || !smoke.Controlled || len(smoke.Sources) != 2 || len(smoke.Settlements) != 2 {
			return fmt.Errorf("cold smoke source/settlement proof missing")
		}
		if err = verifyColdSmokeWindow(before, after, smoke); err != nil {
			return err
		}
		// Captures are point-in-time evidence, not proof LF stayed down between them.
		// Typed physical interval evidence is independently required below.
	}
	for _, input := range recipe.Pairs {
		healthy, e := evidenceSample(input.Healthy)
		if e != nil {
			return e
		}
		outage, e := evidenceSample(input.Outage)
		if e != nil {
			return e
		}
		report.Pairs = append(report.Pairs, Pair{Healthy: healthy, Outage: outage})
	}
	if len(report.Pairs) > 0 {
		report.Coverage["original_source_recovery"] = "passed"
		report.Coverage["fixture_unchanged"] = "passed"
	}
	// Recipe positions cannot prove healthy/outage service state during execution.
	// Preserve raw samples, but never classify paired_20 or healthy_two_turn from labels.
	// Lifecycle/fault/residue evidence needs its own physical producer; omission is explicit failure.
	for _, name := range requiredScenarios {
		if report.Coverage[name] == "" {
			report.Coverage[name] = "not_collected"
		}
	}
	// Coverage is derived only after all typed relationships have been checked.
	// A failed/partial report keeps not_collected and is still written below.
	if report.Physical != nil {
		candidate := report
		candidate.Coverage = map[string]string{}
		for _, name := range requiredScenarios {
			candidate.Coverage[name] = "passed"
		}
		if candidate.Validate() == nil {
			report.Coverage = candidate.Coverage
		}
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(encoded)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println(output)
	return report.Validate()
}

func verifyColdSmokeWindow(before, after ProjectCapture, smoke RawEvidence) error {
	var started time.Time
	for _, container := range after.Containers {
		if container.Service == "switchback-backend" {
			started = container.StartedAt
		}
	}
	if started.IsZero() || smoke.CapturedAt.Before(started) || smoke.CapturedAt.After(after.StartedAt) {
		return fmt.Errorf("cold smoke outside exact Backend/outage capture window")
	}
	var turns []struct {
		CreatedAt  time.Time `json:"created_at"`
		TerminalAt time.Time `json:"terminal_at"`
	}
	if err := json.Unmarshal(smoke.Turns, &turns); err != nil {
		return err
	}
	if len(turns) != 2 {
		return fmt.Errorf("cold two-Turn timing proof missing")
	}
	for _, turn := range turns {
		if turn.CreatedAt.Before(started) || turn.TerminalAt.Before(turn.CreatedAt) || turn.TerminalAt.After(smoke.CapturedAt) {
			return fmt.Errorf("cold Turn outside exact Backend/outage window")
		}
	}
	return nil
}
