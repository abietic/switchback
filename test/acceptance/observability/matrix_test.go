package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

type frozenMetadata struct {
	value artifactapp.ProjectionSourceMetadata
}

func (m frozenMetadata) LoadProjectionSourceMetadata(context.Context, artifactapp.ProjectionSourceRef) (artifactapp.ProjectionSourceMetadata, error) {
	return m.value, nil
}

type frozenIDs struct{ n int }

func (i *frozenIDs) NewID(string) (string, error) { i.n++; return fmt.Sprintf("frozen-%d", i.n), nil }

func TestFrozenLegacyMarkerRemainsValidImmutableEvidence(t *testing.T) {
	ctx := context.Background()
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	id := artifactdomain.TurnIdentity{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", TaskID: "task", WorkerSessionID: "session", WorkerRunID: "run", AgentSessionID: "agent", AgentTurnID: "turn", RuntimeInstanceID: "runtime", RuntimeGeneration: 1, TraceStreamID: "trace", LangfuseTraceID: "langfuse", RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{ID: contractsv1.RuntimeNetworkPolicyLegacyRestrictedV1, EnforcementStatus: "legacy_unenforced"}}
	marker, err := artifactapp.EncodeTurnObservationSource(artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: id, StartedAt: at, EndedAt: at.Add(time.Second), ErrorCode: "source_incomplete"})
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{Store: objects, IDs: &frozenIDs{}, Now: func() time.Time { return at }, Policy: artifactapp.FinalizationPolicy{ArtifactPermission: contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "policy"}, ArtifactRetentionPolicyRef: "retention", TraceLifecyclePolicyRef: "lifecycle"}}).Execute(ctx, artifactapp.FinalizeTurnEvidenceCommand{Identity: id, ServerRecordedAt: at, WorkerRecordedAt: at, RawACPJSONL: []byte("{\"type\":\"progress\"}\n"), ObservationSourceJSONL: marker, NoFilesChanged: true, SourceCheckpointRef: contractsv1.CheckpointRef{CheckpointID: "checkpoint", Digest: "sha256:" + strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	m := artifactapp.ProjectionSourceMetadata{Root: artifactapp.ProjectionRunRoot{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", StartedAt: at}, RootManifest: finalized.Manifest, Manifest: finalized.Manifest, ManifestObject: finalized.ManifestObject}
	for _, r := range finalized.ArtifactRefs {
		if r.Type == "report" {
			m.Report = r
		}
	}
	ref := artifactapp.ProjectionSourceRef{TraceStreamID: "trace", ManifestChecksum: finalized.ManifestObject.Checksum}
	before, err := readFrozenLegacy(ctx, frozenMetadata{m}, objects, ref)
	if err != nil {
		t.Fatalf("valid frozen marker rejected: %v", err)
	}
	after, err := readFrozenLegacy(ctx, frozenMetadata{m}, objects, ref)
	if err != nil || evidenceDigest(before) != evidenceDigest(after) || before.Turn.ErrorCode != "source_incomplete" {
		t.Fatalf("marker changed: %v", err)
	}
	encoded, _ := artifactapp.EncodeTurnObservationSource(after.Turn)
	if !bytes.Equal(marker, encoded) {
		t.Fatal("frozen marker bytes changed")
	}
	m.ManifestObject.Checksum = "sha256:" + strings.Repeat("b", 64)
	if _, err := readFrozenLegacy(ctx, frozenMetadata{m}, objects, ref); err == nil {
		t.Fatal("corrupt manifest accepted")
	}
}

func TestFaultRecoveryBindsOriginalRequestAndClass(t *testing.T) {
	at := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	f := FaultProof{Instance: "process", RequestID: "7", Kind: "query", ConnectedAt: at, CancelledAt: at.Add(time.Second)}
	raw := []byte(`[{"instance":"process","request_id":7,"kind":"query","connected_at":"2026-09-19T00:00:00Z","cancelled_at":"2026-09-19T00:00:01Z","recovered_at":"2026-09-19T00:00:02Z"}]`)
	if _, err := faultRecoveryTime(raw, f); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*FaultProof){func(f *FaultProof) { f.Instance = "restarted" }, func(f *FaultProof) { f.Kind = "ingestion" }, func(f *FaultProof) { f.RequestID = "8" }} {
		bad := f
		change(&bad)
		if _, err := faultRecoveryTime(raw, bad); err == nil {
			t.Fatal("unrelated recovery event accepted")
		}
	}
}

func TestCompletedReadersRejectLateFailureOrWrongIdentity(t *testing.T) {
	at := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	e := ExpectedModel{FixtureKey: "key", FixtureInstance: "process", ObservationID: "model", TurnID: "turn", ReleasedAt: at}
	r := fixtureRecord{Key: "key", Instance: "process", ExpectedID: "model", ReleasedAt: at, CompletedAt: at.Add(time.Millisecond), ReaderCompletedAt: at.Add(2 * time.Millisecond)}
	r.Scope.Turn = "turn"
	if ready, err := validateCompletedReaders([]fixtureRecord{r}, []ExpectedModel{e}); !ready || err != nil {
		t.Fatal(ready, err)
	}
	for _, change := range []func(*fixtureRecord){func(r *fixtureRecord) { r.Failure = "reader_failed" }, func(r *fixtureRecord) { r.Instance = "replaced" }, func(r *fixtureRecord) { r.ExpectedID = "different" }} {
		bad := r
		change(&bad)
		if _, err := validateCompletedReaders([]fixtureRecord{bad}, []ExpectedModel{e}); err == nil {
			t.Fatal("invalid completed reader accepted")
		}
	}
	r.ReaderCompletedAt = time.Time{}
	if ready, err := validateCompletedReaders([]fixtureRecord{r}, []ExpectedModel{e}); ready || err != nil {
		t.Fatal("unfinished reader accepted", ready, err)
	}
}

func TestMeasuredLatencyRequiresOriginalTimestampAndAck(t *testing.T) {
	var raw RawEvidence
	input := `{"project":"switchback-obsv-a1234567","measurement_id":"sample","runtimes":[{"runtime_instance_id":"runtime","worker_session_id":"session","generation":1}],"measured_settlements":[{"agent_turn_id":"a","terminal_message_id":"event-a","terminal_receipt_at":"2026-09-19T00:00:00Z","completed_at":"2026-09-19T00:00:00.01Z","terminal_receipt_to_settlement_ms":10},{"agent_turn_id":"b","terminal_message_id":"event-b","terminal_receipt_at":"2026-09-19T00:00:01Z","completed_at":"2026-09-19T00:00:01.02Z","terminal_receipt_to_settlement_ms":20}],"measured_next_dispatch":{"agent_turn_id":"b","command_message_id":"command-b","terminal_receipt_at":"2026-09-19T00:00:00Z","acknowledged_at":"2026-09-19T00:00:00.5Z","terminal_receipt_to_next_dispatch_ms":500}}`
	if err := json.Unmarshal([]byte(input), &raw); err != nil {
		t.Fatal(err)
	}
	m, err := measurementFromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.To.Sub(m.From) != 1020*time.Millisecond {
		t.Fatal("window not derived from exact journal timestamps")
	}
	if len(m.Settlements) != 2 || m.Settlements[0].MessageID != "event-a" || m.Settlements[1].Milliseconds != 20 || m.Dispatch.MessageID != "command-b" || !m.Dispatch.From.Equal(m.Settlements[0].From) || !m.Dispatch.To.Equal(raw.Dispatch.To) {
		t.Fatal("original terminal/settlement/ACK evidence discarded")
	}
	for _, scenario := range []string{"cold", "ingestion", "query", "warmup", "cleanup"} {
		for _, value := range []float64{5100, math.NaN(), math.Inf(1), -1} {
			t.Run(fmt.Sprintf("%s/%v", scenario, value), func(t *testing.T) {
				var candidate RawEvidence
				if err := json.Unmarshal([]byte(input), &candidate); err != nil {
					t.Fatal(err)
				}
				candidate.MeasurementID = scenario
				*candidate.Settlements[0].Milliseconds = value
				if value == 5100 {
					candidate.Settlements[0].To = candidate.Settlements[0].From.Add(5100 * time.Millisecond)
				}
				if _, err := measurementFromRaw(candidate); err == nil {
					t.Fatal("invalid measured latency accepted")
				}
			})
		}
	}
	*raw.Dispatch.Milliseconds = 499
	if _, err = measurementFromRaw(raw); err == nil {
		t.Fatal("arbitrary latency label accepted")
	}
}

func TestCompletedCoverageGateRejectsGapBeforeCleanup(t *testing.T) {
	r := evidenceReport(t)
	d := matrixDriver{project: r.Physical.Project, intervals: r.Physical.Intervals, measurements: []MeasuredWindow{{State: "healthy", Measurement: r.Pairs[0].Healthy.Measurement}, {State: "healthy", Measurement: r.Physical.Faults[0].Measurement}}}
	if err := d.validateCoverage(); err != nil {
		t.Fatal(err)
	}
	for i := range d.intervals.Subscriptions {
		if d.intervals.Subscriptions[i].ID == "ingestion" {
			d.intervals.Subscriptions[i].End = "unexpected_eof"
		}
	}
	if err := d.validateCoverage(); err == nil || !strings.Contains(err.Error(), "interval") {
		t.Fatalf("completed coverage missing before cleanup: %v", err)
	}
}
