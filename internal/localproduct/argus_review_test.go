package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestArgusReportReviewSubprocessPinsInputsAndReturnsBoundReceipt(t *testing.T) {
	root := t.TempDir()
	report := []byte("Frozen report\n")
	job := taskapp.ArgusReviewJob{
		DeliveryReviewScope: taskapp.DeliveryReviewScope{TaskID: "task-review"},
		TaskRevision:        4, WorkerRunID: "run-review", ArtifactID: "artifact-review",
		ArtifactSHA256: reviewSHA256(report),
	}
	evidence := make(map[string]string)
	for _, name := range argusReviewEvidenceNames {
		b := []byte(name)
		if err := os.WriteFile(filepath.Join(root, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
		evidence[name] = reviewSHA256(b)
	}
	executable := filepath.Join(root, "approved-argus")
	script := `#!/bin/sh
set -eu
test "$1" = report-review
test "$2" = --store
test -d "$3"
test "$4" = --request
test "$6" = --input-dir
test ! "${SWITCHBACK_ARGUS_TEST_AMBIENT+x}"
test "$(cat "$7/report.md")" = "Frozen report"
test "$(cat "$7/facts.json")" = "facts.json"
out="$(dirname "$0")/fake-output"
cat "$out"
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	conf := argusReviewConfig{
		Executable: executable, ExecutableSHA256: reviewSHA256([]byte(script)),
		EvidenceDir: root, EvidenceSHA256: evidence,
	}
	configBytes := marshalArgusTestJSON(t, conf)
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := loadArgusReviewAdapter(configPath, root)
	if err != nil {
		t.Fatal(err)
	}
	request := argusReviewRequest{
		SchemaVersion: argusReviewRequestV1, TaskID: job.TaskID, TaskRevision: job.TaskRevision,
		WorkerRunID: string(job.WorkerRunID), ArtifactID: job.ArtifactID, ArtifactSHA256: job.ArtifactSHA256,
		Profile: argusReviewProfile, EvidenceSHA256: evidence,
	}
	request.IdempotencyKey = reviewSHA256(append([]byte(adapter.profileSHA256), marshalArgusTestJSON(t, request)...))
	result := argusReviewResult{
		SchemaVersion: argusReviewResultV1, RequestSHA256: reviewSHA256(marshalArgusTestJSON(t, request)),
		ReviewRunID: "review-1", TaskID: job.TaskID, TaskRevision: job.TaskRevision, WorkerRunID: string(job.WorkerRunID),
		ArtifactID: job.ArtifactID, ArtifactSHA256: job.ArtifactSHA256, Profile: request.Profile,
		Verdict: "rework_required", Findings: []argusFinding{{Code: "missing-execution-parameters", Summary: "Document the parameters.", Line: 2}},
	}
	raw := append(marshalArgusTestJSON(t, result), '\n')
	if err := os.WriteFile(filepath.Join(adapter.profileDir, "fake-output"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHBACK_ARGUS_TEST_AMBIENT", "not-for-child")
	receipt, err := adapter.Review(context.Background(), job, report)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ReviewRunID != result.ReviewRunID || receipt.ReviewResultSHA256 != reviewSHA256(raw) ||
		receipt.Verdict != result.Verdict || len(receipt.Findings) != 1 || receipt.Findings[0].Summary != result.Findings[0].Summary {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	repeated, err := adapter.Review(context.Background(), job, report)
	if err != nil || repeated.ReviewResultSHA256 != receipt.ReviewResultSHA256 {
		t.Fatalf("repeat = %+v, %v", repeated, err)
	}

	if err := os.WriteFile(filepath.Join(root, "facts.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadArgusReviewAdapter(configPath, root); err == nil {
		t.Fatal("mutated evidence was accepted")
	}
}

func TestArgusReportReviewRejectsResultForWrongDelivery(t *testing.T) {
	request := argusReviewRequest{
		TaskID: "task-review", TaskRevision: 2, WorkerRunID: "run-review", ArtifactID: "artifact-review",
		ArtifactSHA256: strings.Repeat("a", 64), Profile: argusReviewProfile,
	}
	requestSHA := strings.Repeat("b", 64)
	result := argusReviewResult{
		SchemaVersion: argusReviewResultV1, RequestSHA256: requestSHA,
		ReviewRunID: "review-1", TaskID: request.TaskID, TaskRevision: request.TaskRevision,
		WorkerRunID: request.WorkerRunID, ArtifactID: request.ArtifactID, ArtifactSHA256: request.ArtifactSHA256,
		Profile: request.Profile, Verdict: "passed", Findings: []argusFinding{},
	}
	valid := append(marshalArgusTestJSON(t, result), '\n')
	result.TaskRevision++
	wrongRevision := append(marshalArgusTestJSON(t, result), '\n')
	duplicate := []byte(fmt.Sprintf(`{"task_revision":99,%s`, valid[1:]))
	for _, item := range []struct {
		name string
		raw  []byte
	}{
		{"wrong revision", wrongRevision},
		{"missing LF", valid[:len(valid)-1]},
		{"duplicate revision", duplicate},
	} {
		t.Run(item.name, func(t *testing.T) {
			if _, err := validateArgusResult(item.raw, request, requestSHA); err == nil {
				t.Fatal("invalid result was accepted")
			}
		})
	}
}

func TestArgusReportReviewBoundsStdout(t *testing.T) {
	var stdout boundedArgusStdout
	raw := strings.NewReader(strings.Repeat("a", argusReviewMaxOutput+1))
	if _, err := io.Copy(&stdout, raw); !errors.Is(err, errArgusReviewIntegrity) || !stdout.overflow {
		t.Fatalf("oversized stdout: %v", err)
	}
	if stdout.buffer.Len() > argusReviewMaxOutput {
		t.Fatal("oversized stdout was buffered")
	}
}

func marshalArgusTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
