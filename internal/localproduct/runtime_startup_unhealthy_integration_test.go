//go:build integration

package localproduct

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestStartupRejectionReconcilesRuntimeMarkedUnhealthyByWatchdog(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	resumeAt := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	now := resumeAt.Add(workerapp.RuntimeUnhealthyAfter + time.Second)
	seedBackendDeadlineClaimGraph(t, pool, resumeAt.Add(24*time.Hour))
	const runtimeID, sessionID = "runtime-deadline-claim", "session-deadline-claim"
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_control_channels
 (runtime_instance_id,worker_session_id,runtime_generation,channel_epoch,
  credential_digest,process_nonce,stream_attempt,owner_id,owner_fence,
  resume_claim_revision,resume_ready_digest,resume_completed_revision,
  resume_completed_at,resume_completed_state,status,revision,bound_at,claimed_at,updated_at)
VALUES ($1,$2,1,7,'sha256:'||repeat('d',64),'startup-watchdog-process',1,'prior-backend',1,
 1,'sha256:'||repeat('e',64),2,$3,'{}','connected',3,$3,$3,$3)`, runtimeID, sessionID, resumeAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_first_heartbeat_watchdogs
 (runtime_instance_id,tenant_id,worker_session_id,runtime_generation,
  channel_epoch,anchor_kind,anchor_revision,first_authenticated_resume_at,
  suspect_due_at,unhealthy_due_at,status,suspect_at,revision,created_at,updated_at)
VALUES ($1,'tenant-local',$2,1,7,'resume_complete_v1',2,$3,$4,$5,'suspect',$4,2,$3,$4)`,
		runtimeID, sessionID, resumeAt, resumeAt.Add(workerapp.RuntimeSuspectAfter), resumeAt.Add(workerapp.RuntimeUnhealthyAfter),
	); err != nil {
		t.Fatal(err)
	}
	watchdog := workerapp.RuntimeFirstHeartbeatService{
		Store: workerpostgres.NewRuntimeFirstHeartbeatStore(pool), IDs: randomIDs{}, Now: func() time.Time { return now },
	}
	evaluation, err := watchdog.Evaluate(ctx, workerapp.EvaluateRuntimeFirstHeartbeatCommand{
		TenantID: "tenant-local", RuntimeInstanceID: runtimeID,
		RuntimeGeneration: 1, ChannelEpoch: 7, ExpectedWatchdogRevision: 2,
		AnchorKind: workerapp.RuntimeFirstHeartbeatAnchorResumeComplete, AnchorRevision: 2,
		Stage: workerapp.RuntimeFirstHeartbeatStageUnhealthy,
	})
	if err != nil || evaluation.OpenRecovery == nil {
		t.Fatalf("first-heartbeat watchdog did not mark Runtime unhealthy: %+v, %v", evaluation, err)
	}

	// This deployment leaves replacement recovery disabled. Startup can still
	// reach its reconnect deadline after the watchdog committed unhealthy.
	runner := startupDeadlineTestRunner(pool, func() time.Time { return now })
	runner.reconciler = workerapp.UnownedRuntimeReconciler{
		Store: workerpostgres.NewRuntimeLossStore(pool),
		Cleaner: &fake.RuntimeProvisioner{CleanupResult: workerapp.RuntimeCleanupObservation{
			Confirmed: true, ObservedAt: now,
		}},
		Control: &fake.WorkerControl{}, IDs: randomIDs{}, Now: func() time.Time { return now },
	}
	candidate := startupRuntimeCandidate{Restore: liveRuntimeRestoreRequest{
		RuntimeInstanceID: runtimeID, WorkerSessionID: sessionID, RuntimeGeneration: 1, ControlChannelEpoch: 7,
	}, RuntimeStatus: "suspect", LivenessAnchor: resumeAt}
	if err := runner.rejectStartupRuntime(ctx, candidate); err != nil {
		t.Fatalf("startup cleanup of a watchdog-expired Runtime: %v", err)
	}
	var runtimeStatus, sessionStatus string
	if err := pool.QueryRow(ctx, `
SELECT runtime.status,session.status FROM runtime_instances runtime
JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id
WHERE runtime.runtime_instance_id=$1`, runtimeID).Scan(&runtimeStatus, &sessionStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != "lost" || sessionStatus != "lost" {
		t.Fatalf("Runtime and Session were not reconciled: %s, %s", runtimeStatus, sessionStatus)
	}
}
