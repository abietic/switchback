package localproduct

import (
	"context"
	"time"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type combinedObservationScanner []observationScanner

// Historical capture shares the original-registry scanner and pure compiler.
// The history owner, not this preparer, authorizes and atomically seals it.
func (runner *runtimeRunner) PrepareHistoricalObservation(ctx context.Context, in artifactapp.HistoricalObservationCapture) artifactapp.PreparedObservation {
	gap := artifactapp.PreparedObservation{ErrorCode: "scanner_unavailable"}
	if runner == nil || runner.environment == nil {
		return gap
	}
	scanner := runner.runtimeObservationBasisScanner(pendingTurn{WorkerSessionID: in.Scope.WorkerSessionID, RuntimeInstanceID: in.Scope.RuntimeInstanceID, RuntimeGeneration: in.Scope.RuntimeGeneration})
	if scanner == nil {
		return gap
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return (artifactapp.ObservationCapture{Compiler: artifactlangfuse.FragmentCompiler{}, Scanner: scanner, Scope: in.Scope}).Prepare(ctx, in.Frame, in.Evidence, in.Provenance)
}

func (scanners combinedObservationScanner) ScanJSON(ctx context.Context, source string, value any) error {
	for _, scanner := range scanners {
		if err := scanner.ScanJSON(ctx, source, value); err != nil {
			return err
		}
	}
	return nil
}

// PrepareModelObservation uses the exact original registry even when the
// Session is idle or a later Turn is active. All I/O and compilation precede Apply.
func (runner *runtimeRunner) PrepareModelObservation(ctx context.Context, event workerapp.WorkerControlEvent) workerapp.RuntimeObservationCapture {
	fail := func(code workerapp.RuntimeObservationCaptureCode) workerapp.RuntimeObservationCapture {
		return workerapp.RuntimeObservationCapture{SafeCode: code}
	}
	if runner == nil || runner.pool == nil || runner.environment == nil {
		return fail(workerapp.ObservationCaptureUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	e := event.Envelope
	var scope artifactdomain.ObservationScope
	err := runner.pool.QueryRow(ctx, `SELECT s.tenant_id,s.user_id,s.workspace_id,s.worker_session_id,r.worker_run_id FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) WHERE r.worker_run_id=$1 AND s.worker_session_id=$2`, string(e.WorkerRunID), string(e.WorkerSessionID)).Scan(&scope.TenantID, &scope.UserID, &scope.WorkspaceID, &scope.WorkerSessionID, &scope.WorkerRunID)
	if err != nil {
		if ctx.Err() != nil {
			return fail(workerapp.ObservationCaptureTimeout)
		}
		return fail(workerapp.ObservationCaptureUnavailable)
	}
	storage, err := runner.environment.storage.OpenExisting(scope.WorkerSessionID)
	if err != nil {
		return fail(workerapp.ObservationCaptureUnavailable)
	}
	original, err := runner.environment.secrets.ObservationScanner(runtimesecret.ObservationRegistryRequest{WorkerSessionID: scope.WorkerSessionID, RuntimeInstanceID: string(e.RuntimeInstanceID), Generation: uint64(e.RuntimeGeneration), SessionStorage: storage})
	if err != nil {
		return fail(workerapp.ObservationCaptureUnavailable)
	}
	current, err := newRuntimeKnownTokenScanner(runner.configuration)
	if err != nil {
		return fail(workerapp.ObservationCaptureUnavailable)
	}
	f, err := artifactapp.ModelObservationFragment(scope, e)
	if err != nil {
		return fail(workerapp.ObservationCaptureInvalid)
	}
	raw, err := artifactdomain.EncodeObservationFragment(f)
	if err != nil {
		return fail(workerapp.ObservationCaptureInvalid)
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(e)
	if err != nil {
		return fail(workerapp.ObservationCaptureInvalid)
	}
	capture := artifactapp.ObservationCapture{Compiler: artifactlangfuse.FragmentCompiler{}, Scanner: combinedObservationScanner{original, current}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: scope.WorkerSessionID, RuntimeInstanceID: string(e.RuntimeInstanceID), RuntimeGeneration: uint64(e.RuntimeGeneration)}}
	prepared := capture.Prepare(ctx, raw, e, artifactapp.ObservationProvenance{EnvelopeDigest: digest})
	sealed, err := artifactapp.EncodeSealedObservationBundle(artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: []artifactapp.PreparedObservation{prepared}})
	if err != nil {
		return fail(workerapp.ObservationCaptureInvalid)
	}
	return workerapp.RuntimeObservationCapture{Sealed: append([]byte(nil), sealed...)}
}
