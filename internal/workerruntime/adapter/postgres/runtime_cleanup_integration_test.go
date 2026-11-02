//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestCleanupFailureIsReportedAndScheduledBeforeReconciliationConfirmsZeroResidue(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO worker_session_continuity_quotas
  (tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at)
VALUES ('tenant-local','user-local',10,1,1,'continuity-v1',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,
   active_runtime_instance_id,session_revision,runtime_generation,quota_reservation_revision,
   idle_expires_at,absolute_expires_at,created_at,updated_at)
VALUES ('session-cleanup','tenant-local','user-local','workspace-demo','workspace-revision-1',
  'working-copy-cleanup','lost','runtime-cleanup',5,1,1,$2,$3,$1,$1)`,
		now, now.Add(48*time.Hour), now.Add(14*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,generation,runtime_type,container_id,status,image_ref,
   resource_profile_id,network_policy_id,control_channel_epoch,cleanup_confirmed,revision,
   created_at,started_at,ended_at)
VALUES ('runtime-cleanup','session-cleanup',1,'docker_container','container-cleanup','lost',
  'switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  'local_standard_v1','m1_public_web_restricted_v1',1,false,3,$1,$1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	cleaner := &fake.RuntimeProvisioner{
		CleanupResult: workerapplication.RuntimeCleanupObservation{
			ContainerExists: false, SecretVolumeExists: true,
			ContainerRef:    "switchback-runtime-runtime-cleanup",
			SecretVolumeRef: "switchback-runtime-secrets-runtime-cleanup-g1", ObservedAt: now.Add(time.Minute),
		},
		Err: errors.New("delete failed with provider-key-secret"),
	}
	journal := workerapplication.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now }}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='running' WHERE worker_session_id='session-cleanup'; UPDATE runtime_instances SET status='active',ended_at=NULL WHERE runtime_instance_id='runtime-cleanup'`); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Bind(ctx, workerapplication.BindRuntimeControlChannelCommand{WorkerSessionID: "session-cleanup", RuntimeInstanceID: "runtime-cleanup", RuntimeGeneration: 1, ChannelEpoch: 1, Credential: "cleanup-fixture-control-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='lost' WHERE worker_session_id='session-cleanup'; UPDATE runtime_instances SET status='lost',ended_at=created_at WHERE runtime_instance_id='runtime-cleanup'`); err != nil {
		t.Fatal(err)
	}
	control := &cleanupJournalControl{journal: journal, fenceErr: errors.New("controlled fence failure with provider-key-secret")}
	service := workerapplication.RuntimeCleanupService{
		Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: cleaner,
		Control: control,
		IDs:     &concurrentAllocationIDs{}, Now: func() time.Time { return now.Add(time.Minute) },
		RetryDelay: time.Minute,
	}
	failed, err := service.Cleanup(ctx, workerapplication.BeginRuntimeCleanupCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-cleanup",
		RuntimeInstanceID: "runtime-cleanup", ExpectedContainerID: "container-cleanup",
		RuntimeGeneration: 1, ExpectedSessionRevision: 5, ExpectedRuntimeRevision: 3,
		IdempotencyKey: "cleanup/runtime-cleanup/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Confirmed || failed.ErrorCode != "runtime_cleanup_residue" ||
		failed.Reconciliation.ScheduledCommandID == "" {
		t.Fatalf("failed cleanup=%+v", failed)
	}
	var runtimeStatus, commandStatus string
	var cleanupConfirmed bool
	var diagnostic []byte
	var expectedRevision uint64
	if err := pool.QueryRow(ctx, `SELECT status,cleanup_confirmed,cleanup_diagnostic
FROM runtime_instances WHERE runtime_instance_id='runtime-cleanup'`).Scan(
		&runtimeStatus, &cleanupConfirmed, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,expected_revision FROM scheduled_commands
WHERE scheduled_command_id=$1`, failed.Reconciliation.ScheduledCommandID).Scan(
		&commandStatus, &expectedRevision); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != "draining" || cleanupConfirmed || commandStatus != "pending" ||
		expectedRevision != failed.Reconciliation.ExpectedRuntimeRevision ||
		strings.Contains(string(diagnostic), "provider-key-secret") ||
		!strings.Contains(string(diagnostic), "runtime_cleanup_residue") {
		t.Fatalf("unreported cleanup status=%s confirmed=%v command=%s expected=%d diagnostic=%s",
			runtimeStatus, cleanupConfirmed, commandStatus, expectedRevision, diagnostic)
	}
	cleaner.Err = nil
	// The durable store also rejects a caller that omits the safe fence error.
	guard, err := workerpostgres.NewRuntimeCleanupStore(pool).FinishRuntimeCleanup(ctx, workerapplication.FinishRuntimeCleanupRecord{
		Lease: failed.Reconciliation, FenceStatus: "unconfirmed",
		Observation: workerapplication.RuntimeCleanupObservation{Confirmed: true, ObservedAt: now.Add(2 * time.Minute)},
		Now:         now.Add(2 * time.Minute), RetryAt: now.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if guard.Confirmed || guard.Reconciliation.ScheduledCommandID == "" {
		t.Fatalf("store accepted missing fence proof: %+v", guard)
	}
	failed.Reconciliation = guard.Reconciliation
	cleaner.CleanupResult = workerapplication.RuntimeCleanupObservation{
		Confirmed: true, ContainerRef: "switchback-runtime-runtime-cleanup",
		SecretVolumeRef: "switchback-runtime-secrets-runtime-cleanup-g1", ObservedAt: now.Add(2 * time.Minute),
	}
	service.Now = func() time.Time { return now.Add(3 * time.Minute) }
	fenceFailed, err := service.Reconcile(ctx, failed.Reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if fenceFailed.Confirmed || fenceFailed.ErrorCode != "runtime_cleanup_fence_unconfirmed" || fenceFailed.Reconciliation.ScheduledCommandID == "" {
		t.Fatalf("zero residue without a fence completed cleanup: %+v", fenceFailed)
	}
	if err := pool.QueryRow(ctx, `SELECT status,cleanup_confirmed FROM runtime_instances WHERE runtime_instance_id='runtime-cleanup'`).Scan(&runtimeStatus, &cleanupConfirmed); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != "draining" || cleanupConfirmed {
		t.Fatalf("unfenced runtime=%s confirmed=%v", runtimeStatus, cleanupConfirmed)
	}
	control.fenceErr = nil
	service.Now = func() time.Time { return now.Add(5 * time.Minute) }
	applied, err := (platformpostgres.ScheduledCommandPoller{
		Pool: pool, Handler: workerpostgres.RuntimeCleanupCommandHandler{Service: service},
		LeaseOwner: "cleanup-test", LeaseDuration: time.Minute, BatchSize: 1,
		Now: func() time.Time { return now.Add(5 * time.Minute) },
	}).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("reconciled cleanup commands=%d", applied)
	}
	var activeRuntimeID *string
	if err := pool.QueryRow(ctx, `SELECT status,cleanup_confirmed,cleanup_diagnostic
FROM runtime_instances WHERE runtime_instance_id='runtime-cleanup'`).Scan(
		&runtimeStatus, &cleanupConfirmed, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT active_runtime_instance_id FROM worker_sessions
WHERE worker_session_id='session-cleanup'`).Scan(&activeRuntimeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM scheduled_commands
WHERE scheduled_command_id=$1`, failed.Reconciliation.ScheduledCommandID).Scan(&commandStatus); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != "terminated" || !cleanupConfirmed || activeRuntimeID != nil ||
		commandStatus != "applied" || len(cleaner.CleanupRequests) != 3 {
		t.Fatalf("reconciled status=%s confirmed=%v active_runtime=%v command=%s requests=%v diagnostic=%s",
			runtimeStatus, cleanupConfirmed, activeRuntimeID, commandStatus, cleaner.CleanupRequests, diagnostic)
	}
	resultContract := failurematrix.Result{
		Variant: "cleanup-failure", ExecutionOutcome: "completed", AcceptanceOutcome: "failed",
		Assertions: []string{"residue_reported", "reconciliation_scheduled", "eventual_cleanup_confirmed"},
	}
	failurematrix.AssertAndRecord(t, resultContract, resultContract)
}

type cleanupJournalControl struct {
	fake.WorkerControl
	journal  workerapplication.RuntimeControlJournalService
	fenceErr error
}

func (control *cleanupJournalControl) Fence(ctx context.Context, runtimeID string, generation uint64, _ string) error {
	if control.fenceErr != nil {
		return control.fenceErr
	}
	state, err := control.journal.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{WorkerSessionID: "session-cleanup", RuntimeInstanceID: runtimeID, RuntimeGeneration: generation, ChannelEpoch: 1})
	if err != nil {
		return err
	}
	if state.Status != "fenced" || state.FencedAt.IsZero() || state.FenceRevision == 0 || state.FenceOwnerFence == 0 {
		return errors.New("missing committed fixture fence")
	}
	return nil
}
