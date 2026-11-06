//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeRecoveryIncidentOpenIsDurableAndIdempotent(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, now)
	if _, err := pool.Exec(ctx, `INSERT INTO session_dispatch_permits
  (permit_id,worker_session_id,target_kind,target_id,queue_revision,expected_session_revision,
   runtime_generation,capacity_admission_ref,status,idempotency_key,expires_at,created_at)
VALUES ('permit-recovery-late',$1,'standalone_run','late-run',1,$2,1,'capacity-late','active',
  'permit-recovery-late',$3,$4)`, started.WorkerSessionID, active.SessionRevision,
		now.Add(20*time.Minute), now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}

	ids := &concurrentAllocationIDs{}
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(7 * time.Minute) },
	}
	command := workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1,
		FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef:             "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:              now.Add(6 * time.Minute),
		ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision,
		ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" + started.WorkerSessionID + "/g1",
	}
	opened, err := service.Open(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Phase != domain.RecoveryOpen || opened.AttemptCount != 0 ||
		opened.PreviousSessionStatus != domain.SessionRunning ||
		opened.SessionRevision != active.SessionRevision+1 ||
		opened.FailedRuntimeRevision != active.RuntimeRevision+1 {
		t.Fatalf("opened recovery=%+v", opened)
	}

	restartedService := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(8 * time.Minute) },
	}
	replayed, err := restartedService.Open(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.IncidentID != opened.IncidentID || replayed.IncidentRevision != opened.IncidentRevision ||
		replayed.EventID != opened.EventID {
		t.Fatalf("replayed recovery=%+v opened=%+v", replayed, opened)
	}

	var sessionStatus, runStatus, turnStatus, runtimeStatus, taskStatus, permitStatus string
	if err := pool.QueryRow(ctx, `
SELECT session.status,run.status,turn.status,runtime.status,task.status,permit.status
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
JOIN tasks task ON task.task_id='task-01'
JOIN session_dispatch_permits permit ON permit.permit_id='permit-recovery-late'
WHERE session.worker_session_id=$1`, started.WorkerSessionID, started.WorkerRunID,
		started.AgentTurnID, started.RuntimeInstanceID).Scan(
		&sessionStatus, &runStatus, &turnStatus, &runtimeStatus, &taskStatus, &permitStatus,
	); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "recovering" || runStatus != "running" || turnStatus != "delivered" ||
		runtimeStatus != "unhealthy" || taskStatus != "running" || permitStatus != "revoked" {
		t.Fatalf("state session=%s run=%s turn=%s runtime=%s task=%s permit=%s",
			sessionStatus, runStatus, turnStatus, runtimeStatus, taskStatus, permitStatus)
	}
	assertAllocationCount(t, pool, "runtime_recovery_incidents", 1)
	var recoveryEvents int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM outbox_events WHERE event_type='RuntimeRecoveryStarted.v1'`).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if recoveryEvents != 1 {
		t.Fatalf("recovery events=%d", recoveryEvents)
	}

	conflict := command
	conflict.EvidenceRef = "runtime-inspection/conflicting"
	if _, err := restartedService.Open(ctx, conflict); !errors.Is(err, workerpostgres.ErrRuntimeRecoveryConflict) {
		t.Fatalf("conflicting idempotency error=%v", err)
	}
}

func TestRuntimeRecoveryOpenRollsBackWhenOutboxPublicationFails(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 3, 30, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, now)
	rejectRuntimeRecoveryOutbox(t, pool)

	_, err := (workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return now.Add(7 * time.Minute) },
	}).Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1,
		FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef:             "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:              now.Add(6 * time.Minute),
		ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision,
		ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err == nil {
		t.Fatal("Runtime recovery open committed without its Outbox event")
	}

	var incidents int
	var sessionStatus, runtimeStatus string
	var sessionRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_recovery_incidents`).Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT session.status,session.session_revision,runtime.status,runtime.revision
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$2
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID, started.RuntimeInstanceID,
	).Scan(&sessionStatus, &sessionRevision, &runtimeStatus, &runtimeRevision); err != nil {
		t.Fatal(err)
	}
	if incidents != 0 || sessionStatus != "running" || sessionRevision != active.SessionRevision ||
		runtimeStatus != "active" || runtimeRevision != active.RuntimeRevision {
		t.Fatalf(
			"rollback incidents=%d session=%s/r%d runtime=%s/r%d",
			incidents, sessionStatus, sessionRevision, runtimeStatus, runtimeRevision,
		)
	}
}

func TestRuntimeRecoveryPhaseRollsBackWhenOutboxPublicationFails(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 3, 45, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, now)
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return now.Add(7 * time.Minute) },
	}
	opened, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID, FailedGeneration: 1,
		FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef:             "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:              now.Add(6 * time.Minute),
		ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision,
		ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	rejectRuntimeRecoveryOutbox(t, pool)
	service.Now = func() time.Time { return now.Add(8 * time.Minute) }
	if _, err := service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: opened.IncidentID, TargetPhase: domain.RecoveryFencing,
		ExpectedIncidentRevision: opened.IncidentRevision,
		IdempotencyKey:           "recovery/" + opened.IncidentID + "/fencing",
	}); err == nil {
		t.Fatal("Runtime recovery phase committed without its Outbox event")
	}

	var phase string
	var revision uint64
	var phaseResults, recoveryEvents int
	if err := pool.QueryRow(ctx, `
SELECT phase,revision FROM runtime_recovery_incidents WHERE incident_id=$1`,
		opened.IncidentID,
	).Scan(&phase, &revision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM runtime_recovery_idempotency_results
WHERE incident_id=$1 AND command_kind='begin_fencing'`,
		opened.IncidentID,
	).Scan(&phaseResults); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM outbox_events WHERE correlation_id=$1`,
		opened.IncidentID,
	).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if phase != "open" || revision != opened.IncidentRevision ||
		phaseResults != 0 || recoveryEvents != 1 {
		t.Fatalf(
			"rollback phase=%s/r%d results=%d events=%d",
			phase, revision, phaseResults, recoveryEvents,
		)
	}
}

func TestIdleRuntimeRecoveryOpensWithoutInventingRunOrTurn(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 3, 30, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, now)
	settledAt := now.Add(6 * time.Minute)
	if _, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='completed',revision=revision+1,terminal_at=$2
WHERE agent_turn_id=$1`,
		started.AgentTurnID, settledAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_runs
SET status='completed',revision=revision+1,terminal_at=$2
WHERE worker_run_id=$1`,
		started.WorkerRunID, settledAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='idle',active_worker_run_id=NULL,session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1`,
		started.WorkerSessionID, settledAt,
	); err != nil {
		t.Fatal(err)
	}
	var sessionRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session_revision FROM worker_sessions WHERE worker_session_id=$1`,
		started.WorkerSessionID,
	).Scan(&sessionRevision); err != nil {
		t.Fatal(err)
	}
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return settledAt.Add(time.Minute) },
	}
	opened, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerContainerMissing,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:  settledAt, ExpectedSessionRevision: sessionRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.WorkerRunID != "" || opened.AgentTurnID != "" ||
		opened.RunRevision != 0 || opened.TurnRevision != 0 ||
		opened.PreviousSessionStatus != domain.SessionIdle ||
		opened.SessionRevision != sessionRevision+1 {
		t.Fatalf("idle recovery=%+v", opened)
	}
}

func TestRuntimeRecoveryAdvancesOneReplacementAndRestoresOriginalIdentity(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 7, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	ids := &concurrentAllocationIDs{}
	now := base.Add(6 * time.Minute)
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: ids,
		Now: func() time.Time { return now },
	}
	incident, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:  now, ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	advance := func(command workerapplication.AdvanceRuntimeRecoveryCommand) workerapplication.RuntimeRecoveryState {
		t.Helper()
		now = now.Add(time.Second)
		state, err := service.Advance(ctx, command)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	fencing := advance(workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryFencing,
		ExpectedIncidentRevision: incident.IncidentRevision,
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/fencing",
	})
	provisioningCommand := workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryProvisioning,
		ExpectedIncidentRevision: fencing.IncidentRevision,
		FenceStatus:              domain.RecoveryFenceConfirmed, MountFenceToken: 2,
		IdempotencyKey: "recovery/" + incident.IncidentID + "/replacement",
	}
	provisioning := advance(provisioningCommand)
	if provisioning.AttemptCount != 1 || provisioning.ReplacementGeneration != 2 ||
		provisioning.ReplacementChannelEpoch != 3 ||
		provisioning.ReplacementRuntimeInstanceID == "" {
		t.Fatalf("provisioning=%+v", provisioning)
	}
	replayed, err := service.Advance(ctx, provisioningCommand)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ReplacementRuntimeInstanceID != provisioning.ReplacementRuntimeInstanceID ||
		replayed.IncidentRevision != provisioning.IncidentRevision {
		t.Fatalf("replacement replay=%+v original=%+v", replayed, provisioning)
	}
	conflict := provisioningCommand
	conflict.MountFenceToken = 4
	if _, err := service.Advance(ctx, conflict); !errors.Is(err, workerpostgres.ErrRuntimeRecoveryConflict) {
		t.Fatalf("replacement idempotency conflict=%v", err)
	}
	reattaching := advance(workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryReattaching,
		ExpectedIncidentRevision: provisioning.IncidentRevision,
		ContainerID:              "container-replacement-2",
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/provisioned",
	})
	verifying := advance(workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryVerifying,
		ExpectedIncidentRevision: reattaching.IncidentRevision,
		ContinuityStatus:         domain.RecoveryContinuityProven,
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/verifying",
	})
	if _, err := (workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids,
	}).Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID:   started.WorkerSessionID,
		RuntimeInstanceID: verifying.ReplacementRuntimeInstanceID,
		RuntimeGeneration: verifying.ReplacementGeneration,
		ChannelEpoch:      verifying.ReplacementChannelEpoch,
		HeartbeatSequence: 1, ControlSequence: 1, ReceivedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	recovered := advance(workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryRecovered,
		ExpectedIncidentRevision: verifying.IncidentRevision,
		ContinuityStatus:         domain.RecoveryContinuityProven,
		OutcomeReason:            "authenticated heartbeat and durable attachment proven",
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/recovered",
	})
	if recovered.Phase != domain.RecoveryRecovered ||
		recovered.WorkerRunID != started.WorkerRunID ||
		recovered.AgentTurnID != started.AgentTurnID {
		t.Fatalf("recovered=%+v", recovered)
	}
	var sessionStatus, activeRuntimeID, runStatus, turnStatus, taskStatus, oldRuntimeStatus string
	var turnStartedAt, turnDeadlineAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT session.status,session.active_runtime_instance_id,run.status,turn.status,task.status,
       failed.status,turn.started_at,turn.deadline_at
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN tasks task ON task.task_id='task-01'
JOIN runtime_instances failed ON failed.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(&sessionStatus, &activeRuntimeID, &runStatus, &turnStatus, &taskStatus,
		&oldRuntimeStatus, &turnStartedAt, &turnDeadlineAt); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "running" || activeRuntimeID != recovered.ReplacementRuntimeInstanceID ||
		runStatus != "running" || turnStatus != "delivered" || taskStatus != "running" ||
		oldRuntimeStatus != "lost" ||
		!turnStartedAt.Equal(base.Add(4*time.Minute)) ||
		!turnDeadlineAt.Equal(base.Add(4*time.Minute+24*time.Hour)) {
		t.Fatalf("session=%s runtime=%s run=%s turn=%s task=%s old=%s started=%s deadline=%s",
			sessionStatus, activeRuntimeID, runStatus, turnStatus, taskStatus, oldRuntimeStatus,
			turnStartedAt, turnDeadlineAt)
	}
	var events int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM outbox_events WHERE correlation_id=$1`,
		incident.IncidentID,
	).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 6 {
		t.Fatalf("recovery phase events=%d want=6", events)
	}
}

func TestRuntimeRecoveryExhaustionPausesRunSessionAndNonterminalTask(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	ids := &concurrentAllocationIDs{}
	now := base.Add(6 * time.Minute)
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: ids,
		Now: func() time.Time { return now },
	}
	incident, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerContainerMissing,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:  now, ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	fencing, err := service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryFencing,
		ExpectedIncidentRevision: incident.IncidentRevision,
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/fencing",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	exhausted, err := service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted,
		ExpectedIncidentRevision: fencing.IncidentRevision,
		OutcomeReason:            "exclusive mount authority could not be proven",
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/exhausted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if exhausted.Phase != domain.RecoveryExhausted {
		t.Fatalf("exhausted=%+v", exhausted)
	}
	var sessionStatus, runStatus, turnStatus, taskStatus, waitingCode string
	if err := pool.QueryRow(ctx, `
SELECT session.status,run.status,turn.status,task.status,task.waiting_reason->>'code'
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN tasks task ON task.task_id='task-01'
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID,
	).Scan(&sessionStatus, &runStatus, &turnStatus, &taskStatus, &waitingCode); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "waiting_user" || runStatus != "waiting_user" ||
		turnStatus != "failed" || taskStatus != "waiting_user" ||
		waitingCode != "runtime_recovery_failed" {
		t.Fatalf("session=%s run=%s turn=%s task=%s reason=%s",
			sessionStatus, runStatus, turnStatus, taskStatus, waitingCode)
	}
	var sessionRevision, runtimeRevision uint64
	var containerID string
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,runtime.revision,COALESCE(runtime.container_id,'')
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=session.active_runtime_instance_id
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
	).Scan(&sessionRevision, &runtimeRevision, &containerID); err != nil {
		t.Fatal(err)
	}
	cleaner := &fake.RuntimeProvisioner{
		CleanupResult: workerapplication.RuntimeCleanupObservation{
			ContainerExists: true, SecretVolumeExists: true,
			ObservedAt: now.Add(time.Second),
		},
		Err: errors.New("temporary cleanup failure"),
	}
	cleanupResult, err := (workerapplication.RuntimeCleanupService{
		Store: workerpostgres.NewRuntimeCleanupStore(pool), Cleaner: cleaner,
		Control: &fake.WorkerControl{}, IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return now.Add(time.Second) }, RetryDelay: time.Minute,
	}).Cleanup(ctx, workerapplication.BeginRuntimeCleanupCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		RuntimeInstanceID: started.RuntimeInstanceID, ExpectedContainerID: containerID,
		RuntimeGeneration: 1, ExpectedSessionRevision: sessionRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		IdempotencyKey:          "recovery-cleanup/" + exhausted.IncidentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleanupResult.Confirmed || cleanupResult.Reconciliation.ScheduledCommandID == "" {
		t.Fatalf("recovery cleanup=%+v", cleanupResult)
	}
}

func TestRuntimeRecoveryDeadlinePreemptionRequiresSettlementSource(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	now := base.Add(6 * time.Minute)
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return now },
	}
	incident, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:  now, ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := loadRecoveryBusinessSnapshot(t, pool, started)
	now = base.Add(7 * time.Minute)
	_, err = service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted,
		ExpectedIncidentRevision: incident.IncidentRevision,
		OutcomeReason:            "agent_turn_deadline",
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/forged-early-deadline",
	})
	if !errors.Is(err, workerapplication.ErrTurnOutcomeAuthorityClaimed) {
		t.Fatalf("early forged recovery deadline Advance error=%v", err)
	}
	now = deadlineAt
	_, err = service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted,
		ExpectedIncidentRevision: incident.IncidentRevision,
		OutcomeReason:            "agent_turn_deadline",
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/deadline",
	})
	if !errors.Is(err, workerapplication.ErrTurnOutcomeAuthorityClaimed) {
		t.Fatalf("legacy recovery deadline Advance error=%v", err)
	}
	if afterRejectedAdvance := loadRecoveryBusinessSnapshot(
		t,
		pool,
		started,
	); afterRejectedAdvance != before {
		t.Fatalf(
			"legacy deadline Advance mutated domain: before=%+v after=%+v",
			before,
			afterRejectedAdvance,
		)
	}
	settlement, err := (workerapplication.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Source.Kind !=
		workerapplication.TurnSettlementSourceBackendDeadline {
		t.Fatalf("deadline settlement=%+v", settlement)
	}
	if afterClaim := loadRecoveryBusinessSnapshot(
		t,
		pool,
		started,
	); afterClaim != before {
		t.Fatalf(
			"deadline source claim settled domain: before=%+v after=%+v",
			before,
			afterClaim,
		)
	}
}

func TestRuntimeRecoveryAbsoluteExpiryPreemptionUsesSessionExpiryContract(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 11, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	expiresAt := base.Add(8 * time.Minute)
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions SET absolute_expires_at=$2 WHERE worker_session_id=$1`,
		started.WorkerSessionID, expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	now := base.Add(6 * time.Minute)
	service := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return now },
	}
	incident, err := service.Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerContainerMissing,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:  now, ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = expiresAt
	if _, err := service.Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: incident.IncidentID, TargetPhase: domain.RecoveryExhausted,
		ExpectedIncidentRevision: incident.IncidentRevision,
		OutcomeReason:            "worker_session_absolute_expired",
		IdempotencyKey:           "recovery/" + incident.IncidentID + "/absolute-expiry",
	}); err != nil {
		t.Fatal(err)
	}
	var sessionStatus, runStatus, runReason, turnStatus, turnReason, taskStatus, waitingCode string
	var taskSessionID, taskRunID *string
	if err := pool.QueryRow(ctx, `
SELECT session.status,run.status,run.terminal_reason,turn.status,turn.terminal_reason,
       task.status,task.waiting_reason->>'code',task.worker_session_id,task.current_worker_run_id
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN tasks task ON task.task_id='task-01'
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID,
	).Scan(
		&sessionStatus, &runStatus, &runReason, &turnStatus, &turnReason,
		&taskStatus, &waitingCode, &taskSessionID, &taskRunID,
	); err != nil {
		t.Fatal(err)
	}
	var reserved int
	if err := pool.QueryRow(ctx, `
SELECT reserved_count FROM worker_session_continuity_quotas
WHERE tenant_id='tenant-local' AND user_id='user-local'`).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "expired" || runStatus != "timed_out" ||
		runReason != "worker_session_absolute_expired" || turnStatus != "timed_out" ||
		turnReason != "worker_session_absolute_expired" || taskStatus != "waiting_user" ||
		waitingCode != "worker_session_expired" || taskSessionID != nil || taskRunID != nil ||
		reserved != 0 {
		t.Fatalf(
			"session=%s run=%s/%s turn=%s/%s task=%s/%s refs=%v/%v quota=%d",
			sessionStatus, runStatus, runReason, turnStatus, turnReason,
			taskStatus, waitingCode, taskSessionID, taskRunID, reserved,
		)
	}
}

func TestHardFailureSignalsConvergeOnOneIncidentAndFenceStaleGeneration(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	ids := &concurrentAllocationIDs{}
	observer := workerpostgres.NewRuntimeFailureObserver(
		pool,
		workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: ids,
			Now: func() time.Time { return base.Add(7 * time.Minute) },
		},
	)
	first, opened, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID + "/exit",
		EvidenceAt:  base.Add(6 * time.Minute),
	})
	if err != nil || !opened {
		t.Fatalf("open hard failure state=%+v opened=%t err=%v", first, opened, err)
	}
	second, opened, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1,
		Trigger:     domain.RecoveryTriggerHeartbeatUnhealthy,
		EvidenceRef: "runtime-heartbeat/" + started.RuntimeInstanceID + "/1",
		EvidenceAt:  base.Add(6*time.Minute + time.Second),
	})
	if err != nil || opened || second.IncidentID != "" {
		t.Fatalf("duplicate signal state=%+v opened=%t err=%v", second, opened, err)
	}
	_, opened, err = observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 2, ChannelEpoch: 2,
		Trigger:     domain.RecoveryTriggerContainerMissing,
		EvidenceRef: "runtime-inspection/stale", EvidenceAt: base.Add(6 * time.Minute),
	})
	if err != nil || opened {
		t.Fatalf("stale signal opened=%t err=%v", opened, err)
	}
	assertAllocationCount(t, pool, "runtime_recovery_incidents", 1)
}

func TestLegacyWorkerControlClosedIncidentRemainsReadableAndAdvanceable(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='recovering',session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1`,
		started.WorkerSessionID,
		base.Add(7*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances
SET status='unhealthy',revision=revision+1
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_recovery_incidents
  (incident_id,tenant_id,worker_session_id,worker_run_id,agent_turn_id,
   failed_runtime_instance_id,failed_generation,failed_channel_epoch,trigger,phase,
   previous_session_status,attempt_count,fence_status,continuity_status,evidence_ref,evidence_at,
   revision,opened_at,updated_at)
VALUES
  ('legacy-worker-control-closed','tenant-local',$1,$4,$5,$3,1,1,
   'worker_control_closed','open','running',0,'pending','pending',
   'worker-control/legacy-runtime',
   $2::timestamptz - interval '1 minute',1,$2::timestamptz,$2::timestamptz)`,
		started.WorkerSessionID,
		base.Add(7*time.Minute),
		started.RuntimeInstanceID,
		started.WorkerRunID,
		started.AgentTurnID,
	); err != nil {
		t.Fatal(err)
	}
	store := workerpostgres.NewRuntimeRecoveryStore(pool)
	legacy, found, err := store.FindActiveRuntimeRecovery(ctx, "tenant-local")
	if err != nil {
		t.Fatal(err)
	}
	if !found || legacy.IncidentID != "legacy-worker-control-closed" ||
		legacy.Trigger != domain.RecoveryTriggerWorkerControlClosed ||
		legacy.Phase != domain.RecoveryOpen {
		t.Fatalf("legacy recovery=%+v found=%t", legacy, found)
	}
	advanced, err := (workerapplication.RuntimeRecoveryService{
		Store: store,
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(8 * time.Minute) },
	}).Advance(ctx, workerapplication.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		IncidentID: "legacy-worker-control-closed", TargetPhase: domain.RecoveryFencing,
		ExpectedIncidentRevision: legacy.IncidentRevision,
		IdempotencyKey:           "legacy-worker-control-closed/begin-fencing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Trigger != domain.RecoveryTriggerWorkerControlClosed ||
		advanced.Phase != domain.RecoveryFencing {
		t.Fatalf("advanced legacy recovery=%+v", advanced)
	}
}

func TestDisabledRuntimeRecoveryGateRequiresHardInspection(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	observer := workerpostgres.NewGatedRuntimeFailureObserver(
		pool,
		workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   &concurrentAllocationIDs{},
			Now:   func() time.Time { return base.Add(7 * time.Minute) },
		},
		false,
	)
	state, opened, err := observer.OpenDetectedRuntimeFailure(
		ctx,
		workerpostgres.DetectedRuntimeFailure{
			WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: 1, ChannelEpoch: 1,
			Trigger:     domain.RecoveryTriggerContainerExit,
			EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID,
			EvidenceAt:  base.Add(6 * time.Minute),
		},
	)
	if err != nil || opened || state.IncidentID != "" {
		t.Fatalf("state=%+v opened=%t err=%v", state, opened, err)
	}
	var incidents int
	var sessionStatus string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_recovery_incidents`).Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT status FROM worker_sessions WHERE worker_session_id=$1`,
		started.WorkerSessionID,
	).Scan(&sessionStatus); err != nil {
		t.Fatal(err)
	}
	if incidents != 0 || sessionStatus != "running" {
		t.Fatalf("incidents=%d session=%s", incidents, sessionStatus)
	}
}

func startActiveRecoveryFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
) (workerapplication.StartedWorkerRun, workerapplication.ResidentRuntimeState) {
	t.Helper()
	ctx := context.Background()
	seedAllocationRequests(t, pool, 1, now)
	runtimecapacity.Initialize(t, pool)
	ids := &concurrentAllocationIDs{}
	allocated, err := (workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(time.Minute) },
	}).Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2
WHERE worker_session_id=$1`, allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	dispatch := taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(3 * time.Minute) }, PermitTTL: 5 * time.Minute,
	}
	if _, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
		TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01",
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-task-01",
	}); err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(ctx, taskapplication.IssueTaskPermitCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID,
		RuntimeGeneration: 1, IdempotencyKey: "permit-task-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := (workerapplication.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(4 * time.Minute) },
	}).Execute(ctx, residentStartCommand(permit))
	if err != nil {
		t.Fatal(err)
	}
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "container-recovery-1", started.RuntimeGeneration)
	active, err := (workerapplication.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool),
		Now:   func() time.Time { return now.Add(5 * time.Minute) },
	}).Activate(ctx, workerapplication.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-recovery-1",
		RuntimeGeneration: 1, ControlChannelEpoch: 1,
		ExpectedSessionRevision: started.SessionRevision,
		ExpectedRunRevision:     1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return started, active
}

func rejectRuntimeRecoveryOutbox(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION switchback_test_reject_runtime_recovery_outbox()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'injected Runtime recovery Outbox failure';
END
$$;
CREATE TRIGGER switchback_test_reject_runtime_recovery_outbox
BEFORE INSERT ON outbox_events
FOR EACH ROW
EXECUTE FUNCTION switchback_test_reject_runtime_recovery_outbox()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS switchback_test_reject_runtime_recovery_outbox ON outbox_events;
DROP FUNCTION IF EXISTS switchback_test_reject_runtime_recovery_outbox()`)
	})
}
