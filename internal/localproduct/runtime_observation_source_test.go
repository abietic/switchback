package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func observationCaptureFixture(t *testing.T) (pendingTurn, []contractsv1.RuntimeControlEnvelope, *secretcheck.Scanner) {
	t.Helper()
	start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	turn := pendingTurn{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", AgentSessionID: "agent", AgentTurnID: "turn", RuntimeInstanceID: "runtime", RuntimeGeneration: 1, StartedAt: start, Content: json.RawMessage(`{"text":"goal"}`), NetworkPolicyID: "m1_public_web_restricted_v1", NetworkEnforcementStatus: "legacy_unenforced"}
	model := workerapp.ModelObservation{ObservationID: "model-1", WorkerSessionID: "session", WorkerRunID: "run", AgentTurnID: "turn", RuntimeID: "runtime", Generation: 1, Model: "model", Route: "route", StartedAt: start, EndedAt: start.Add(time.Second), Input: json.RawMessage(`{"prompt":"safe"}`), Output: json.RawMessage(`"answer"`), Status: "ok"}
	payload, _ := json.Marshal(model)
	event := contractsv1.RuntimeControlEnvelope{WorkerSessionID: "session", WorkerRunID: "run", AgentSessionID: "agent", AgentTurnID: "turn", RuntimeInstanceID: "runtime", RuntimeGeneration: 1, Sequence: 2, MessageType: "model.observation", Payload: payload}
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "test", Value: "secret-token-only-for-capture-test"}})
	if err != nil {
		t.Fatal(err)
	}
	return turn, []contractsv1.RuntimeControlEnvelope{event}, scanner
}

func TestRuntimeObservationSnapshotReplaysExactBytesWithoutLiveState(t *testing.T) {
	turn, events, scanner := observationCaptureFixture(t)
	ctx := context.Background()
	encoded := encodeRuntimeObservationSource(ctx, turn, turn.StartedAt.Add(2*time.Second), 3, events, []observationScanner{scanner}, true, []byte("report"))
	for _, withSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "frozen source"}[withSource], func(t *testing.T) {
			directory := t.TempDir()
			raw := []byte(`{"type":"acp.raw_frame","direction":"agent_to_client","frame":"{\"jsonrpc\":\"2.0\"}"}` + "\n")
			if err := os.WriteFile(filepath.Join(directory, "acp.raw.jsonl"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			patchName, err := freezeRuntimeSettlementPatchSnapshot(directory, turn.AgentTurnID, nil)
			if err != nil {
				t.Fatal(err)
			}
			input := &runtimeSettlementEvidenceInput{TenantID: turn.TenantID, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID, WorkerSessionID: turn.WorkerSessionID, WorkerRunID: turn.WorkerRunID, AgentSessionID: turn.AgentSessionID, AgentTurnID: turn.AgentTurnID, RuntimeInstanceID: turn.RuntimeInstanceID, RuntimeGeneration: turn.RuntimeGeneration, NetworkPolicyID: turn.NetworkPolicyID, NetworkEnforcementStatus: turn.NetworkEnforcementStatus, TraceEndOffset: int64(len(raw)), TraceDigest: runtimeSettlementBytesDigest(raw), PatchSnapshotFile: patchName, PatchDigest: runtimeSettlementBytesDigest(nil), NoFilesChanged: true, ServerRecordedAt: turn.StartedAt, WorkerRecordedAt: turn.StartedAt, ReportArtifactID: "report-frozen", PatchArtifactID: "patch-frozen", ArtifactCreatedAt: turn.StartedAt, SourceCheckpointID: "checkpoint", SourceCheckpointDigest: "sha256:" + strings.Repeat("a", 64)}
			if withSource {
				input.ObservationSourceSnapshotFile, err = freezeRuntimeSettlementSnapshot(directory, turn.AgentTurnID, "observation", "jsonl", encoded)
				if err != nil {
					t.Fatal(err)
				}
				input.ObservationSourceSize = int64(len(encoded))
				input.ObservationSourceDigest = runtimeSettlementBytesDigest(encoded)
			}
			objects, err := artifactlocal.NewObjectStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			finalizer := artifactapp.TurnEvidenceFinalizer{Store: objects, IDs: randomIDs{}, Now: func() time.Time { return turn.StartedAt }, Policy: artifactapp.FinalizationPolicy{ArtifactPermission: contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "local"}, ArtifactRetentionPolicyRef: "retention", TraceLifecyclePolicyRef: "trace"}}
			runner := &runtimeRunner{finalizer: finalizer}
			live := &liveRuntime{}
			live.storage.TraceSpool.BackendPath = directory
			checkpoint := runtimeTurnSettlementCheckpoint{EvidenceInput: input}
			first, err := runner.finalizeRuntimeSettlementEvidence(ctx, live, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			// A new process has no journal or live observation cache. Replay must only read its frozen input.
			restarted := &runtimeRunner{finalizer: finalizer}
			second, err := restarted.finalizeRuntimeSettlementEvidence(ctx, live, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if first.Evidence.ManifestDigest != second.Evidence.ManifestDigest {
				t.Fatal("restart changed sealed manifest")
			}
			if withSource {
				source, err := artifactapp.ReadTurnObservationSource(ctx, objects, second.Evidence.Finalized.Manifest)
				if err != nil {
					t.Fatal(err)
				}
				got, err := artifactapp.EncodeTurnObservationSource(source)
				if err != nil || !bytes.Equal(got, encoded) {
					t.Fatal("replay changed frozen source")
				}
				if err := os.WriteFile(filepath.Join(directory, input.ObservationSourceSnapshotFile), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := restarted.finalizeRuntimeSettlementEvidence(ctx, live, checkpoint); err == nil {
					t.Fatal("tampered source snapshot accepted")
				}
			} else if len(second.Evidence.Finalized.Manifest.Segments) != 2 || len(second.Evidence.Finalized.Objects) != 4 {
				t.Fatal("legacy evidence was retrofitted")
			}
		})
	}
}

func TestRuntimeScannerRetainsAvailableConfigurationTokens(t *testing.T) {
	configuration := Config{LangfusePublicKey: "available-public-key", LangfuseSecretKey: "available-public-key", AssistantLiteLLMAPIKey: "available-assistant-key", KimiCredentialFile: filepath.Join(t.TempDir(), "missing"), DeepSeekCredentialFile: filepath.Join(t.TempDir(), "missing")}
	scanner, err := newRuntimeKnownTokenScanner(configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{configuration.LangfusePublicKey, configuration.AssistantLiteLLMAPIKey} {
		if !secretcheck.IsExposure(scanner.ScanJSON(context.Background(), "source", token)) {
			t.Fatal("available configuration credential missed")
		}
	}
	if _, err := newRuntimeKnownTokenScanner(Config{}); err == nil {
		t.Fatal("empty registry fabricated a scanner")
	}
}

func TestRuntimeObservationCaptureScansPureProspectiveReport(t *testing.T) {
	turn, _, _ := observationCaptureFixture(t)
	const credential = "report-only-configuration-credential"
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": turn.AgentSessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": credential}}}})
	raw, _ := json.Marshal(map[string]any{"type": "acp.raw_frame", "direction": "agent_to_client", "frame": string(frame)})
	runner := &runtimeRunner{configuration: Config{LangfuseSecretKey: credential}}
	encoded, err := runner.captureRuntimeObservationSource(context.Background(), &liveRuntime{}, turn, turn.StartedAt.Add(time.Second), turn.StartedAt, 0, append(raw, '\n'), nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := artifactapp.DecodeTurnObservationSource(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if source.ErrorCode != "credential_detected" {
		t.Fatalf("report-only credential not frozen in marker: %q", source.ErrorCode)
	}
	if bytes.Contains(encoded, []byte(credential)) {
		t.Fatal("credential persisted in marker")
	}
}

func TestRuntimeObservationCaptureDoesNotHideDatabaseFailure(t *testing.T) {
	turn, _, _ := observationCaptureFixture(t)
	pool, err := pgxpool.New(context.Background(), "postgres://localhost/switchback")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	runner := &runtimeRunner{pool: pool}
	if _, err := runner.captureRuntimeObservationSource(context.Background(), &liveRuntime{}, turn, turn.StartedAt.Add(time.Second), turn.StartedAt, 3, nil, nil); err == nil {
		t.Fatal("closed PostgreSQL pool was disguised as optional observation failure")
	}
}

func TestRuntimeObservationSourceDeduplicatesAndCutsJournal(t *testing.T) {
	turn, events, scanner := observationCaptureFixture(t)
	events = append(events, events[0])
	late := events[0]
	late.Sequence = 4
	late.Payload = json.RawMessage(`{"invalid":"late"}`)
	events = append(events, late)
	bytes := encodeRuntimeObservationSource(context.Background(), turn, turn.StartedAt.Add(2*time.Second), 3, events, []observationScanner{scanner}, true, []byte("report"))
	source, err := artifactapp.DecodeTurnObservationSource(bytes)
	if err != nil {
		t.Fatal(err)
	}
	if source.ErrorCode != "" || len(source.ModelObservations) != 1 {
		t.Fatalf("unexpected source marker/count: %q %d", source.ErrorCode, len(source.ModelObservations))
	}
}

func TestRuntimeObservationSourceScansReportAndFinalMarker(t *testing.T) {
	turn, events, scanner := observationCaptureFixture(t)
	for _, tt := range []struct {
		name, report, identity, code string
		available, omit              bool
	}{
		{name: "report credential", report: "secret-token-only-for-capture-test", available: true, code: "credential_detected"},
		{name: "missing runtime registry", report: "safe", code: "source_incomplete"},
		{name: "identity credential", report: "safe", identity: "secret-token-only-for-capture-test", available: true, omit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			current := turn
			if tt.identity != "" {
				current.TenantID = tt.identity
			}
			bytes := encodeRuntimeObservationSource(context.Background(), current, turn.StartedAt.Add(2*time.Second), 3, events, []observationScanner{scanner}, tt.available, []byte(tt.report))
			if tt.omit {
				if len(bytes) != 0 {
					t.Fatal("unsafe identity must omit optional source")
				}
				return
			}
			source, err := artifactapp.DecodeTurnObservationSource(bytes)
			if err != nil {
				t.Fatal(err)
			}
			if source.ErrorCode != tt.code || len(source.ModelObservations) != 0 || string(source.Input) != "null" {
				t.Fatal("expected content-free marker")
			}
		})
	}
}

func TestRuntimeObservationSourceConflictsAndStaleIdentityBecomeMarkers(t *testing.T) {
	turn, events, scanner := observationCaptureFixture(t)
	for _, kind := range []string{"conflict", "stale", "conflicting credential"} {
		t.Run(kind, func(t *testing.T) {
			copyEvent := events[0]
			if kind == "stale" {
				copyEvent.RuntimeGeneration = 2
			} else {
				var model workerapp.ModelObservation
				_ = json.Unmarshal(copyEvent.Payload, &model)
				model.Output = json.RawMessage(`"changed answer"`)
				if kind == "conflicting credential" {
					model.Output = json.RawMessage(`"secret-token-only-for-capture-test"`)
				}
				copyEvent.Payload, _ = json.Marshal(model)
			}
			selected := []contractsv1.RuntimeControlEnvelope{copyEvent, events[0]}
			encoded := encodeRuntimeObservationSource(context.Background(), turn, turn.StartedAt.Add(2*time.Second), 3, selected, []observationScanner{scanner}, true, []byte("report"))
			source, err := artifactapp.DecodeTurnObservationSource(encoded)
			if err != nil {
				t.Fatal(err)
			}
			want := "source_incomplete"
			if kind == "conflicting credential" {
				want = "credential_detected"
			}
			if source.ErrorCode != want {
				t.Fatalf("marker %q want %q", source.ErrorCode, want)
			}
		})
	}
}
