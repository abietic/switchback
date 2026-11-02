package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeObservationBasisZeroModelsAndSecretFiltering(t *testing.T) {
	for _, scenario := range []string{"zero models", "input secret", "report secret", "missing original registry", "ambiguous initial", "later turn"} {
		t.Run(scenario, func(t *testing.T) {
			turn, _, scanner := observationCaptureFixture(t)
			report := []byte("# Result\nDone\n")
			initial := []string{turn.AgentTurnID}
			var scan observationScanner = scanner
			switch scenario {
			case "input secret":
				turn.Content = json.RawMessage(`{"text":"secret-token-only-for-capture-test"}`)
			case "report secret":
				report = []byte("secret-token-only-for-capture-test")
			case "missing original registry":
				scan = nil
			case "ambiguous initial":
				initial = append(initial, "other")
			case "later turn":
				initial = []string{"previous"}
			}
			capture := encodeRuntimeObservationBasis(context.Background(), turn, turn.StartedAt, initial, turn.StartedAt.Add(time.Second), report, "sha256:"+strings.Repeat("a", 64), scan)
			bundle, err := artifactapp.DecodeSealedObservationBundle(capture.Sealed)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if scenario == "later turn" {
				want = 1
			}
			if len(bundle.Observations) != want {
				t.Fatalf("got %d basis frames", len(bundle.Observations))
			}
			if scenario == "zero models" {
				old := encodeRuntimeObservationSource(context.Background(), turn, turn.StartedAt.Add(time.Second), 1, nil, []observationScanner{scanner}, true, report)
				marker, err := artifactapp.DecodeTurnObservationSource(old)
				if err != nil || marker.ErrorCode != "source_incomplete" || len(marker.ModelObservations) != 0 {
					t.Fatal("zero-model basis changed the frozen legacy marker contract")
				}
			}
			for i, p := range bundle.Observations {
				code := ""
				switch scenario {
				case "input secret", "report secret":
					code = "credential_detected"
				case "missing original registry":
					code = "basis_scan_unavailable"
				case "ambiguous initial":
					if i == 1 {
						code = "source_invalid"
					}
				}
				if p.ErrorCode != code {
					t.Fatalf("basis %d code=%s want=%s", i, p.ErrorCode, code)
				}
				if code != "" {
					if len(p.Frame) != 0 || len(p.Receipt) != 0 {
						t.Fatal("gap leaked content")
					}
					continue
				}
				f, err := artifactdomain.DecodeObservationFragment(p.Frame)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 && f.Kind != artifactdomain.FragmentTurnBasis {
					t.Fatal("first basis must be Turn")
				}
				r, err := artifactapp.DecodeObservationScanReceipt(p.Receipt, p.Frame)
				if err != nil || r.Provenance.ReportDigest != runtimeSettlementBytesDigest(report) {
					t.Fatal("receipt did not bind exact frozen report")
				}
			}
		})
	}
}

type basisResponseLossController struct {
	*recordingRuntimeSettlementController
	sealed       []byte
	loseResponse bool
}

func (c *basisResponseLossController) checkpoint(ctx context.Context, current workerapp.TurnSettlement, checkpoint runtimeTurnSettlementCheckpoint) (workerapp.TurnSettlement, error) {
	if current.Phase == workerapp.TurnSettlementAccepted && checkpoint.ObservationCapture != nil {
		c.sealed = append([]byte(nil), checkpoint.ObservationCapture.Sealed...)
	}
	result, err := c.recordingRuntimeSettlementController.checkpoint(ctx, current, checkpoint)
	if err == nil && c.loseResponse {
		c.loseResponse = false
		return workerapp.TurnSettlement{}, errInjectedSettlementResponseLoss
	}
	return result, err
}

func TestRuntimeObservationBasisFirstFreezeResponseLossSkipsRegistryAndPreparation(t *testing.T) {
	controller := &basisResponseLossController{recordingRuntimeSettlementController: &recordingRuntimeSettlementController{current: workerapp.TurnSettlement{SettlementID: "turn-settlement/turn-1", AgentTurnID: "turn-1", Phase: workerapp.TurnSettlementAccepted, PhaseState: mustRuntimeSettlementCheckpoint(t, runtimeTurnSettlementCheckpoint{}), Revision: 7, LeaseOwnerID: "settler-1", LeaseFence: 3}}, loseResponse: true}
	effects := (recordingRuntimeSettlementEffects{calls: &controller.calls}).steps()
	originalPrepare := effects.prepareEvidence
	turn, _, scanner := observationCaptureFixture(t)
	capture := encodeRuntimeObservationBasis(context.Background(), turn, turn.StartedAt, []string{turn.AgentTurnID}, turn.StartedAt.Add(time.Second), []byte("report"), "sha256:"+strings.Repeat("a", 64), scanner)
	registryRemoved := false
	effects.prepareEvidence = func(ctx context.Context, s workerapp.TurnSettlement, c runtimeTurnSettlementCheckpoint) (runtimeTurnSettlementCheckpoint, error) {
		if registryRemoved {
			t.Fatal("response-loss retry tried to prepare after registry cleanup")
		}
		result, err := originalPrepare(ctx, s, c)
		result.ObservationCapture = &capture
		return result, err
	}
	coordinator := runtimeTurnSettlementCoordinator{controller: controller, effects: effects}
	if _, err := coordinator.reconcileClaimed(context.Background(), controller.current); !errors.Is(err, errInjectedSettlementResponseLoss) {
		t.Fatalf("first freeze err=%v", err)
	}
	if !bytes.Equal(controller.sealed, capture.Sealed) || countRuntimeSettlementCall(controller.calls, "finalize_evidence") != 0 {
		t.Fatal("basis must commit before ObjectStore finalization")
	}
	if bytes.Contains(controller.current.PhaseState, []byte("observation.capture")) || bytes.Contains(controller.current.PhaseState, []byte("ObservationCapture")) {
		t.Fatal("sidecar entered checkpoint JSON")
	}
	registryRemoved = true
	got, err := coordinator.reconcileClaimed(context.Background(), controller.current)
	if err != nil || got.Phase != workerapp.TurnSettlementCompleted {
		t.Fatalf("retry phase=%s err=%v", got.Phase, err)
	}
	if countRuntimeSettlementCall(controller.calls, "prepare_evidence") != 1 || countRuntimeSettlementCall(controller.calls, "checkpoint:accepted") != 1 || countRuntimeSettlementCall(controller.calls, "finalize_evidence") != 1 {
		t.Fatalf("first-freeze replay calls=%v", controller.calls)
	}
}

func TestRuntimeObservationBasisCheckpointDigestExcludesSidecar(t *testing.T) {
	cp := completeRuntimeSettlementCheckpointThrough(workerapp.TurnSettlementAccepted)
	before, _ := json.Marshal(cp)
	cp.ObservationCapture = &workerapp.RuntimeObservationCapture{Sealed: []byte("private backend evidence")}
	after, _ := json.Marshal(cp)
	if !bytes.Equal(before, after) {
		t.Fatal("basis changes checkpoint bytes")
	}
	typed, err := applicationTurnSettlementCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"schema_version": typed.SchemaVersion, "frozen_artifacts": typed.FrozenArtifacts}
	for k, v := range typed.AdditionalFields {
		fields[k] = v
	}
	raw, _ := json.Marshal(fields)
	_, preparedDigest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(before)
	if err != nil {
		t.Fatal(err)
	}
	_, freezeDigest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(raw)
	if err != nil || freezeDigest != preparedDigest {
		t.Fatal("prepared receipt does not bind service canonical checkpoint")
	}
}

func TestRuntimeObservationBasisDatabaseFailureIsCoreError(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://unused:unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	turn, _, _ := observationCaptureFixture(t)
	got, err := (&runtimeRunner{pool: pool}).prepareRuntimeObservationBasis(context.Background(), turn, turn.StartedAt.Add(time.Second), []byte("report"), "sha256:"+strings.Repeat("a", 64))
	if err == nil || len(got.Sealed) != 0 || got.SafeCode != "" {
		t.Fatal("database failure was disguised as optional basis gap")
	}
}

func TestRuntimeObservationBasisForcedStopMissingOriginalRegistryCannotBorrowGeneration(t *testing.T) {
	environment, _, storage, _ := newDeadlineCleanupTestEnvironment(t)
	environment.secrets = runtimesecret.Materializer{Ownership: runtimesecret.OwnershipFunc(func(string, int, int) error { return nil }), OwnerUID: func(string) (uint32, error) { return 0, nil }, OpenFileOwnerUID: func(*os.File) (uint32, error) { return 0, nil }}
	turn, _, _ := observationCaptureFixture(t)
	turn.WorkerSessionID = "session-1"
	for _, generation := range []uint64{1, 2} {
		_, err := environment.secrets.Materialize(runtimesecret.Request{WorkerSessionID: turn.WorkerSessionID, WorkerRunID: turn.WorkerRunID, RuntimeInstanceID: turn.RuntimeInstanceID, Generation: generation, ChannelEpoch: 1, WorkerControlToken: fmt.Sprintf("fixture-control-generation-%d", generation), SessionStorage: storage, AgentProfile: workerapp.ProfileRef{ID: "kimi-codex-anthropic-v1", Revision: 1, Digest: "sha256:2ba4c6870eb74c52515b54de6ef89dece1955893a4c7b0efb3d632c977e6eaf2"}, ProviderCredential: runtimesecret.ProviderCredential([]byte("fixture-original-provider-token"))})
		if err != nil {
			t.Fatal(err)
		}
	}
	runner := &runtimeRunner{environment: environment, configuration: Config{AssistantLiteLLMAPIKey: "fixture-current-backend-token"}}
	if runner.runtimeObservationBasisScanner(turn) == nil {
		t.Fatal("original registry fixture not usable")
	}
	historyFrame, err := artifactdomain.EncodeObservationFragment(artifactdomain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentRunBasis, Scope: artifactdomain.ObservationScope{TenantID: turn.TenantID, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID, WorkerSessionID: turn.WorkerSessionID, WorkerRunID: turn.WorkerRunID}, InitialTurnID: turn.AgentTurnID, StartedAt: turn.StartedAt, InputJSON: string(turn.Content)})
	if err != nil {
		t.Fatal(err)
	}
	history := artifactapp.HistoricalObservationCapture{Frame: historyFrame, Evidence: map[string]any{"input": json.RawMessage(turn.Content), "report": "safe report"}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: turn.WorkerSessionID, RuntimeInstanceID: turn.RuntimeInstanceID, RuntimeGeneration: turn.RuntimeGeneration}, Provenance: artifactapp.ObservationProvenance{CheckpointDigest: artifactdomain.Checksum([]byte("checkpoint")), InputDigest: artifactdomain.Checksum(turn.Content), ReportDigest: artifactdomain.Checksum([]byte("safe report"))}}
	if got := runner.PrepareHistoricalObservation(context.Background(), history); got.ErrorCode != "" || len(got.Receipt) == 0 {
		t.Fatalf("original registry history capture failed: %s", got.ErrorCode)
	}
	history.Evidence = map[string]string{"report": "fixture-original-provider-token"}
	if got := runner.PrepareHistoricalObservation(context.Background(), history); got.ErrorCode != "credential_detected" || len(got.Receipt) != 0 {
		t.Fatal("historical evidence skipped original registry scan")
	}
	history.Evidence = map[string]string{"report": "safe report"}
	if err := os.Remove(filepath.Join(storage.BackendRoot, "runtimes", "runtime-g1", "control-plane", "observation-tokens.json")); err != nil {
		t.Fatal(err)
	}
	later := turn
	later.RuntimeGeneration = 2
	if runner.runtimeObservationBasisScanner(later) == nil {
		t.Fatal("replacement registry fixture not usable")
	}
	if got := runner.PrepareHistoricalObservation(context.Background(), history); got.ErrorCode != "scanner_unavailable" || len(got.Receipt) != 0 {
		t.Fatal("historical recapture borrowed current generation registry")
	}
	scanner := runner.runtimeObservationBasisScanner(turn)
	if scanner != nil {
		t.Fatal("forced-stop old Turn borrowed replacement registry")
	}
	capture := encodeRuntimeObservationBasis(context.Background(), turn, turn.StartedAt, []string{turn.AgentTurnID}, turn.StartedAt.Add(time.Second), []byte("report"), "sha256:"+strings.Repeat("a", 64), scanner)
	bundle, err := artifactapp.DecodeSealedObservationBundle(capture.Sealed)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range bundle.Observations {
		if p.ErrorCode != "basis_scan_unavailable" || len(p.Frame) > 0 || len(p.Receipt) > 0 {
			t.Fatal("missing original registry did not produce precise no-content gap")
		}
	}
}
