package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthySnapshotsPlacedInOutageSlotCannotPassPairCoverage(t *testing.T) {
	dir := t.TempDir()
	raw := `{"project":"healthy-only","controlled_acp_fixture":true,"fixture_checksum":"sha256:unchanged","verified_original_sources":[{"Source":{"TraceStreamID":"s1","ManifestChecksum":"c1"}},{"Source":{"TraceStreamID":"s2","ManifestChecksum":"c2"}}],"jobs":[{"trace_stream_id":"s1","manifest_checksum":"c1","status":"passed"},{"trace_stream_id":"s2","manifest_checksum":"c2","status":"passed"}],"turns":[{"agent_turn_id":"t1"},{"agent_turn_id":"t2"}],"runtimes":[{"runtime_instance_id":"r1","worker_session_id":"session","generation":1}],"measured_settlements":[{"terminal_receipt_to_settlement_ms":10},{"terminal_receipt_to_settlement_ms":11}],"measured_next_dispatch":{"terminal_receipt_to_next_dispatch_ms":12}}`
	snapshot := filepath.Join(dir, "healthy.json")
	if err := os.WriteFile(snapshot, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	recipe := `{"source_commit":"` + strings.Repeat("a", 40) + `","worker_image_digest":"sha256:` + strings.Repeat("b", 64) + `","backend_image_digest":"sha256:` + strings.Repeat("c", 64) + `","pairs":[{"healthy":{"original":"` + snapshot + `","recovered":"` + snapshot + `"},"outage":{"original":"` + snapshot + `","recovered":"` + snapshot + `"}}]}`
	recipePath := filepath.Join(dir, "recipe.json")
	if err := os.WriteFile(recipePath, []byte(recipe), 0600); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(dir, "result.json")
	if err := routeProduceReport(recipePath, result); err == nil {
		t.Fatal("healthy evidence classified as outage")
	}
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Coverage["paired_20"] != "not_collected" || report.Coverage["healthy_two_turn"] != "not_collected" {
		t.Fatalf("labels created physical service-state proof: %v", report.Coverage)
	}
}

func TestClaimReportRoutingPreservesNewOutputAndRejectsForeignEvidence(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := "switchback-obsv-a1234567"
	claim, _ := json.Marshal(map[string]string{"project": project, "output": dir})
	if err = os.WriteFile(filepath.Join(dir, "claim.json"), claim, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "matrix-recipe.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "report.json")
	if err = routeProduceReport(output, dir); err == nil {
		t.Fatal("incomplete report passed")
	}
	if _, err = os.Stat(output); err != nil {
		t.Fatal("new routing did not write failed report")
	}
	before, _ := os.ReadFile(output)
	if err = routeProduceReport(output, dir); err == nil {
		t.Fatal("existing report overwritten")
	}
	after, _ := os.ReadFile(output)
	if string(before) != string(after) {
		t.Fatal("existing evidence changed")
	}
	foreign := filepath.Join(t.TempDir(), "foreign.json")
	if err = os.WriteFile(foreign, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if validateRecipePaths(dir, ReportRecipe{Physical: foreign}) == nil {
		t.Fatal("foreign claim evidence accepted")
	}
}

func TestColdSmokeOutsideCaptureWindowRejected(t *testing.T) {
	started := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	after := ProjectCapture{StartedAt: started.Add(time.Minute), Containers: []CapturedContainer{{Service: "switchback-backend", StartedAt: started}}}
	for _, at := range []time.Time{started.Add(-time.Second), started.Add(2 * time.Minute)} {
		if err := verifyColdSmokeWindow(ProjectCapture{}, after, RawEvidence{CapturedAt: at}); err == nil {
			t.Fatal("out-of-window smoke accepted")
		}
	}
	smoke := RawEvidence{CapturedAt: started.Add(30 * time.Second), Turns: json.RawMessage(`[{"created_at":"2026-09-09T00:00:00Z","terminal_at":"2026-09-09T00:00:01Z"},{"created_at":"2026-09-10T00:00:02Z","terminal_at":"2026-09-10T00:00:03Z"}]`)}
	if err := verifyColdSmokeWindow(ProjectCapture{}, after, smoke); err == nil {
		t.Fatal("old Turn accepted in new snapshot")
	}
}
