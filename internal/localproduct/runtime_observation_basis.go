package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// Preparation reads durable input and only the original Runtime's registry.
// Database failures are core failures; unavailable optional scanning is a gap.
func (runner *runtimeRunner) prepareRuntimeObservationBasis(ctx context.Context, turn pendingTurn, endedAt time.Time, frozenReport []byte, checkpointDigest string) (workerapp.RuntimeObservationCapture, error) {
	if runner == nil || runner.pool == nil {
		return workerapp.RuntimeObservationCapture{}, errors.New("load observation basis failed")
	}
	var createdAt time.Time
	var initialIDs []string
	if err := runner.loadRuntimeObservationTurn(ctx, &turn); err != nil {
		return workerapp.RuntimeObservationCapture{}, errors.New("load observation basis failed")
	}
	if err := runner.pool.QueryRow(ctx, `SELECT r.created_at,COALESCE((SELECT array_agg(t.agent_turn_id) FROM agent_turns t WHERE t.worker_run_id=r.worker_run_id AND t.worker_session_id=r.worker_session_id AND t.created_at=r.created_at AND t.mode IN ('prompt','queue')),ARRAY[]::text[]) FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) WHERE r.worker_run_id=$1 AND s.worker_session_id=$2 AND s.tenant_id=$3 AND s.user_id=$4 AND s.workspace_id=$5`, turn.WorkerRunID, turn.WorkerSessionID, turn.TenantID, turn.UserID, turn.WorkspaceID).Scan(&createdAt, &initialIDs); err != nil {
		return workerapp.RuntimeObservationCapture{}, errors.New("load observation basis failed")
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	scanner := runner.runtimeObservationBasisScanner(turn)
	return encodeRuntimeObservationBasis(ctx, turn, createdAt, initialIDs, endedAt, frozenReport, checkpointDigest, scanner), nil
}

func (runner *runtimeRunner) runtimeObservationBasisScanner(turn pendingTurn) observationScanner {
	if runner.environment == nil {
		return nil
	}
	storage, err := runner.environment.storage.OpenExisting(turn.WorkerSessionID)
	if err != nil {
		return nil
	}
	original, err := runner.environment.secrets.ObservationScanner(runtimesecret.ObservationRegistryRequest{WorkerSessionID: turn.WorkerSessionID, RuntimeInstanceID: turn.RuntimeInstanceID, Generation: turn.RuntimeGeneration, SessionStorage: storage})
	if err != nil {
		return nil
	}
	current, err := newRuntimeKnownTokenScanner(runner.configuration)
	if err != nil {
		return nil
	}
	return combinedObservationScanner{original, current}
}

func (runner *runtimeRunner) loadRuntimeObservationTurn(ctx context.Context, turn *pendingTurn) error {
	return runner.pool.QueryRow(ctx, `SELECT content,started_at FROM agent_turns WHERE agent_turn_id=$1 AND worker_run_id=$2 AND worker_session_id=$3 AND agent_session_id=$4`, turn.AgentTurnID, turn.WorkerRunID, turn.WorkerSessionID, turn.AgentSessionID).Scan(&turn.Content, &turn.StartedAt)
}

func encodeRuntimeObservationBasis(ctx context.Context, turn pendingTurn, runCreatedAt time.Time, initialIDs []string, endedAt time.Time, report []byte, checkpointDigest string, scanner observationScanner) workerapp.RuntimeObservationCapture {
	scope := artifactdomain.ObservationScope{TenantID: turn.TenantID, UserID: turn.UserID, WorkspaceID: turn.WorkspaceID, WorkerSessionID: turn.WorkerSessionID, WorkerRunID: turn.WorkerRunID}
	identity := runtimeObservationIdentity(turn)
	reportJSON, _ := json.Marshal(string(report))
	frames := []artifactdomain.ObservationFragment{{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentTurnBasis, Scope: scope, Turn: &identity, StartedAt: turn.StartedAt, EndedAt: &endedAt, InputJSON: string(turn.Content), OutputJSON: string(reportJSON)}}
	if len(initialIDs) != 1 {
		frames = append(frames, artifactdomain.ObservationFragment{Kind: artifactdomain.FragmentRunBasis})
	} else if initialIDs[0] == turn.AgentTurnID {
		frames = append(frames, artifactdomain.ObservationFragment{SchemaVersion: "observation.fragment.v1", Kind: artifactdomain.FragmentRunBasis, Scope: scope, InitialTurnID: turn.AgentTurnID, StartedAt: runCreatedAt, InputJSON: string(turn.Content)})
	}
	prepared := make([]artifactapp.PreparedObservation, 0, len(frames))
	capture := artifactapp.ObservationCapture{Compiler: artifactlangfuse.FragmentCompiler{}, Scanner: scanner, Scope: artifactapp.ObservationScanScope{WorkerSessionID: turn.WorkerSessionID, RuntimeInstanceID: turn.RuntimeInstanceID, RuntimeGeneration: turn.RuntimeGeneration}}
	provenance := artifactapp.ObservationProvenance{CheckpointDigest: checkpointDigest, InputDigest: runtimeSettlementBytesDigest(turn.Content), ReportDigest: runtimeSettlementBytesDigest(report)}
	for i, frame := range frames {
		p := artifactapp.PreparedObservation{ErrorCode: "basis_scan_unavailable"}
		if i == 1 && len(initialIDs) != 1 {
			p.ErrorCode = "source_invalid"
		} else if scanner != nil {
			raw, err := artifactdomain.EncodeObservationFragment(frame)
			if err != nil {
				p.ErrorCode = "source_invalid"
			} else {
				p = capture.Prepare(ctx, raw, struct {
					Input  json.RawMessage `json:"input"`
					Report string          `json:"report"`
				}{turn.Content, string(report)}, provenance)
			}
		}
		prepared = append(prepared, p)
	}
	sealed, err := artifactapp.EncodeSealedObservationBundle(artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: prepared})
	if err != nil {
		return workerapp.RuntimeObservationCapture{SafeCode: workerapp.ObservationCaptureInvalid}
	}
	return workerapp.RuntimeObservationCapture{Sealed: sealed}
}

func runtimeObservationReport(turn pendingTurn, recordedAt time.Time, raw, patch []byte) ([]byte, error) {
	bundle, err := artifactdomain.BuildTurnEvidence(artifactdomain.BuildTurnEvidenceInput{Identity: runtimeObservationIdentity(turn), ServerRecordedAt: recordedAt, WorkerRecordedAt: recordedAt, RawACPJSONL: raw, Patch: patch, NoFilesChanged: len(patch) == 0})
	if err != nil {
		return nil, err
	}
	var report []byte
	for _, object := range bundle.Objects {
		if object.Kind == artifactdomain.KindReport {
			if report != nil {
				return nil, errors.New("multiple Turn reports")
			}
			report = object.Payload
		}
	}
	if len(report) == 0 {
		return nil, errors.New("missing Turn report")
	}
	return report, nil
}
