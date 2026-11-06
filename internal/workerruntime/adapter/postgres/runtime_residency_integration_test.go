//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestInitialRuntimeOwnershipAllowsControlBindBeforeActivation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	control := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(5 * time.Minute) },
	}
	bound, err := control.Bind(ctx, runtimeControlBind(started))
	if err != nil {
		t.Fatalf("bind initial Runtime control channel: %v", err)
	}
	if bound.WorkerSessionID != started.WorkerSessionID ||
		bound.RuntimeInstanceID != started.RuntimeInstanceID ||
		bound.RuntimeGeneration != started.RuntimeGeneration ||
		bound.ChannelEpoch != started.ControlChannelEpoch {
		t.Fatalf("bound control identity=%+v started=%+v", bound, started)
	}
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "container-initial", started.RuntimeGeneration)
	active, err := (workerapplication.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool),
		Now:   func() time.Time { return now.Add(6 * time.Minute) },
	}).Activate(ctx, workerapplication.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-initial",
		RuntimeGeneration:       started.RuntimeGeneration,
		ControlChannelEpoch:     started.ControlChannelEpoch,
		ExpectedSessionRevision: started.SessionRevision,
		ExpectedRunRevision:     1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if err != nil {
		t.Fatalf("activate initially owned Runtime: %v", err)
	}
	if active.RuntimeInstanceID != started.RuntimeInstanceID ||
		active.RuntimeStatus != "active" ||
		active.ContainerID != "container-initial" {
		t.Fatalf("active Runtime=%+v started=%+v", active, started)
	}
}

func TestRuntimeActivationRejectsMismatchedCurrentRuntime(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 10, 30, 0, 0, time.UTC)
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,generation,runtime_type,status,image_ref,
   resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at,ended_at)
VALUES ('runtime-mismatched',$1,2,'docker_container','lost',
  'switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  'local_standard_v1','m1_public_web_restricted_v1',2,1,$2,$2)`,
		started.WorkerSessionID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var sessionRevision uint64
	if err := pool.QueryRow(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id='runtime-mismatched',runtime_generation=2,
    session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1
RETURNING session_revision`, started.WorkerSessionID, now.Add(5*time.Minute)).Scan(
		&sessionRevision,
	); err != nil {
		t.Fatal(err)
	}
	_, err := (workerapplication.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool),
		Now:   func() time.Time { return now.Add(6 * time.Minute) },
	}).Activate(ctx, workerapplication.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-stale",
		RuntimeGeneration: 1, ControlChannelEpoch: 1,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("mismatched current Runtime activation error=%v", err)
	}
}

func startInitialRuntimeBeforeActivation(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
) workerapplication.StartedWorkerRun {
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
	return started
}

func TestStageCompletionKeepsGenerationOneRuntimeResident(t *testing.T) {
	tests := []struct {
		name          string
		sessionTarget domain.WorkerSessionStatus
		runTarget     domain.WorkerRunStatus
		wantActiveRun bool
	}{
		{name: "interactive", sessionTarget: domain.SessionInteractive, runTarget: domain.RunCompleted},
		{name: "waiting user", sessionTarget: domain.SessionWaitingUser, runTarget: domain.RunWaitingUser, wantActiveRun: true},
		{name: "idle", sessionTarget: domain.SessionIdle, runTarget: domain.RunCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC)
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
			residency := workerapplication.RuntimeResidencyService{
				Store: workerpostgres.NewRuntimeResidencyStore(pool),
				Now:   func() time.Time { return now.Add(5 * time.Minute) },
			}
			runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "container-resident-1", started.RuntimeGeneration)
			active, err := residency.Activate(ctx, workerapplication.ActivateResidentRuntimeCommand{
				TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
				WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
				RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-resident-1",
				RuntimeGeneration: 1, ControlChannelEpoch: 1,
				ExpectedSessionRevision: started.SessionRevision,
				ExpectedRunRevision:     1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if active.SessionStatus != domain.SessionRunning || active.RuntimeStatus != "active" {
				t.Fatalf("active state=%+v", active)
			}
			var turnStatus string
			var startedAt, deadlineAt time.Time
			if err := pool.QueryRow(ctx, `SELECT status,started_at,deadline_at FROM agent_turns
WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&turnStatus, &startedAt, &deadlineAt); err != nil {
				t.Fatal(err)
			}
			if turnStatus != "delivered" || !startedAt.Equal(now.Add(4*time.Minute)) ||
				!deadlineAt.Equal(startedAt.Add(24*time.Hour)) {
				t.Fatalf("Turn dispatch status=%s started=%s deadline=%s", turnStatus, startedAt, deadlineAt)
			}
			completed, err := residency.CompleteStage(ctx, workerapplication.CompleteResidentStageCommand{
				TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
				WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
				RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1,
				ExpectedSessionRevision: active.SessionRevision,
				ExpectedRunRevision:     active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
				ExpectedRuntimeRevision: active.RuntimeRevision,
				SessionTarget:           test.sessionTarget, RunTarget: test.runTarget,
				TurnTarget: domain.TurnCompleted,
			})
			if err != nil {
				t.Fatal(err)
			}
			if completed.RuntimeInstanceID != started.RuntimeInstanceID ||
				completed.ContainerID != "container-resident-1" || completed.RuntimeGeneration != 1 ||
				completed.RuntimeRevision != active.RuntimeRevision || completed.RuntimeStatus != "active" {
				t.Fatalf("stage completion replaced or changed Runtime: active=%+v completed=%+v", active, completed)
			}
			assertAllocationCount(t, pool, "runtime_instances", 1)
			var activeRunID *string
			if err := pool.QueryRow(ctx, `SELECT active_worker_run_id FROM worker_sessions
WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(&activeRunID); err != nil {
				t.Fatal(err)
			}
			if (activeRunID != nil) != test.wantActiveRun {
				t.Fatalf("active Run=%v want present=%v", activeRunID, test.wantActiveRun)
			}
			var deadlineStatus string
			if err := pool.QueryRow(ctx, `SELECT status FROM scheduled_commands
WHERE target_type='AgentTurn' AND target_id=$1`, started.AgentTurnID).Scan(&deadlineStatus); err != nil {
				t.Fatal(err)
			}
			if deadlineStatus != "cancelled" {
				t.Fatalf("terminal Turn deadline status=%s", deadlineStatus)
			}
		})
	}
}

func TestAgentSessionBindingReplacesReservationBeforeRuntimeActivation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	seedAllocationRequests(t, pool, 1, now)
	runtimecapacity.Initialize(t, pool)
	ids := &concurrentAllocationIDs{}
	allocated, err := (workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids, Now: func() time.Time { return now.Add(time.Minute) },
	}).Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now.Add(2 * time.Minute) },
	}
	if _, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
		TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01",
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 1, IdempotencyKey: "bind-enqueue",
	}); err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(ctx, taskapplication.IssueTaskPermitCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID,
		RuntimeGeneration: 1, IdempotencyKey: "bind-permit",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := (workerapplication.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: func() time.Time { return now.Add(3 * time.Minute) },
	}).Execute(ctx, residentStartCommand(permit))
	if err != nil {
		t.Fatal(err)
	}
	binder := workerapplication.AgentSessionBindingService{
		Store: workerpostgres.NewAgentSessionBindingStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(4 * time.Minute) },
	}
	command := workerapplication.BindAgentSessionCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID:      started.RuntimeInstanceID,
		ReservedAgentSessionID: started.AgentSessionID, ActualAgentSessionID: "codex-session-real-1",
		RuntimeGeneration: 1, ExpectedSessionRevision: started.SessionRevision,
		ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
		IdempotencyKey: "bind-agent-session/" + started.RuntimeInstanceID,
	}
	bound, err := binder.Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if bound.AgentSessionID != command.ActualAgentSessionID || bound.TurnRevision != 2 {
		t.Fatalf("bound=%+v", bound)
	}
	retry, err := binder.Execute(ctx, command)
	if err != nil || retry != bound {
		t.Fatalf("idempotent bind=%+v error=%v", retry, err)
	}
	command.ActualAgentSessionID = "codex-session-conflict"
	command.IdempotencyKey = "bind-agent-session/conflict"
	if _, err := binder.Execute(ctx, command); err == nil {
		t.Fatal("conflicting AgentSession binding was accepted")
	}
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "container-bound-1", started.RuntimeGeneration)
	active, err := (workerapplication.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now.Add(5 * time.Minute) },
	}).Activate(ctx, workerapplication.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-bound-1",
		RuntimeGeneration: 1, ControlChannelEpoch: 1,
		ExpectedSessionRevision: bound.SessionRevision, ExpectedRunRevision: bound.RunRevision,
		ExpectedTurnRevision: bound.TurnRevision, ExpectedRuntimeRevision: bound.RuntimeRevision,
	})
	if err != nil || active.RuntimeStatus != "active" {
		t.Fatalf("activate bound Runtime=%+v error=%v", active, err)
	}
	commands := workerapplication.WorkerUserCommandService{
		Store: workerpostgres.NewWorkerUserCommandStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(6 * time.Minute) },
	}
	base := workerapplication.SubmitWorkerUserCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision,
		ExpectedTurnRevision: active.TurnRevision,
	}
	queuedCommand := base
	queuedCommand.CommandType = "queue"
	queuedCommand.Content = "Run one more verification after this Turn."
	queuedCommand.IdempotencyKey = "queue-after-active-turn"
	queued, err := commands.Execute(ctx, queuedCommand)
	if err != nil || queued.QueueSequence == nil || *queued.QueueSequence != 1 {
		t.Fatalf("queued command=%+v error=%v", queued, err)
	}
	var queuedClientCommandID, queuedEventIdempotencyKey, queuedServerCommandID string
	if err := pool.QueryRow(ctx, `
SELECT client_command_id,idempotency_key,correlation_id
FROM outbox_events WHERE idempotency_key=$1`,
		"worker-user-command/"+queuedCommand.IdempotencyKey,
	).Scan(&queuedClientCommandID, &queuedEventIdempotencyKey, &queuedServerCommandID); err != nil {
		t.Fatal(err)
	}
	if queuedClientCommandID != queuedCommand.IdempotencyKey ||
		queuedEventIdempotencyKey != "worker-user-command/"+queuedCommand.IdempotencyKey ||
		queuedServerCommandID != queued.CommandID {
		t.Fatalf("Worker command outbox identities client=%q idempotency=%q command=%q",
			queuedClientCommandID, queuedEventIdempotencyKey, queuedServerCommandID)
	}
	queuedRetry, err := commands.Execute(ctx, queuedCommand)
	if err != nil || queuedRetry.CommandID != queued.CommandID || *queuedRetry.QueueSequence != 1 {
		t.Fatalf("queued retry=%+v error=%v", queuedRetry, err)
	}
	conflictingQueue := queuedCommand
	conflictingQueue.Content = "Different queued content."
	if _, err := commands.Execute(ctx, conflictingQueue); !errors.Is(err, workerapplication.ErrWorkerUserCommandIdempotencyConflict) {
		t.Fatalf("queued content conflict error=%v", err)
	}
	interruptCommand := base
	interruptCommand.CommandType = "interrupt"
	interruptCommand.IdempotencyKey = "interrupt-active-turn"
	interrupted, err := commands.Execute(ctx, interruptCommand)
	if err != nil || interrupted.QueueSequence != nil || interrupted.Status != "accepted" {
		t.Fatalf("interrupt command=%+v error=%v", interrupted, err)
	}
	recoveredRuntimeID := "runtime-recovered-generation-2"
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances
SET status='lost',revision=revision+1,ended_at=$2
WHERE runtime_instance_id=$1`, started.RuntimeInstanceID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,
   image_ref,resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,
   network_enforcement_status,control_channel_epoch,revision,created_at,started_at)
SELECT $2,worker_session_id,worker_run_id,2,runtime_type,'container-recovered-2','active',
       image_ref,resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,
       network_enforcement_status,2,1,$3,$3
FROM runtime_instances
WHERE runtime_instance_id=$1`, started.RuntimeInstanceID, recoveredRuntimeID,
		now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var recoveredSessionRevision uint64
	if err := pool.QueryRow(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id=$2,runtime_generation=2,
    session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND active_runtime_instance_id=$4
RETURNING session_revision`, started.WorkerSessionID, recoveredRuntimeID,
		now.Add(7*time.Minute), started.RuntimeInstanceID).Scan(&recoveredSessionRevision); err != nil {
		t.Fatal(err)
	}
	completed, err := (workerapplication.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now.Add(7 * time.Minute) },
	}).CompleteStage(ctx, workerapplication.CompleteResidentStageCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: recoveredRuntimeID, RuntimeGeneration: 2,
		ExpectedSessionRevision: recoveredSessionRevision, ExpectedRunRevision: active.RunRevision,
		ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: 1,
		SessionTarget: domain.SessionInteractive, RunTarget: domain.RunCancelled,
		TurnTarget: domain.TurnCancelled, TerminalReason: "user_interrupt",
	})
	if err != nil {
		t.Fatal(err)
	}
	queuedTurns := workerapplication.QueuedTurnService{
		Store: workerpostgres.NewQueuedTurnStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(8 * time.Minute) },
	}
	continueCommand := workerapplication.ContinueQueuedTurnCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: started.WorkerSessionID,
		PreviousWorkerRunID: started.WorkerRunID, PreviousAgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: recoveredRuntimeID, RuntimeGeneration: 2, QueueCommandID: queued.CommandID,
		ExpectedSessionRevision: completed.SessionRevision, ExpectedRunRevision: completed.RunRevision,
		ExpectedTurnRevision: completed.TurnRevision, ExpectedRuntimeRevision: completed.RuntimeRevision,
	}
	staleGeneration := continueCommand
	staleGeneration.RuntimeGeneration = 1
	if _, err := queuedTurns.Execute(ctx, staleGeneration); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("stale Runtime generation error=%v", err)
	}
	continued, err := queuedTurns.Execute(ctx, continueCommand)
	if err != nil || continued.WorkerRunID == started.WorkerRunID ||
		continued.RuntimeInstanceID != recoveredRuntimeID || continued.RuntimeGeneration != 2 {
		t.Fatalf("continued queued Turn=%+v error=%v", continued, err)
	}
	retried, err := queuedTurns.Execute(ctx, continueCommand)
	if err != nil || retried != continued {
		t.Fatalf("retried queued Turn=%+v first=%+v error=%v", retried, continued, err)
	}
	var commandStatus, newTurnMode, newAgentSessionID string
	if err := pool.QueryRow(ctx, `SELECT status FROM worker_user_commands WHERE command_id=$1`, queued.CommandID).Scan(&commandStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT mode,agent_session_id FROM agent_turns WHERE agent_turn_id=$1`,
		continued.AgentTurnID).Scan(&newTurnMode, &newAgentSessionID); err != nil {
		t.Fatal(err)
	}
	if commandStatus != "applied" || newTurnMode != "queue" || newAgentSessionID != bound.AgentSessionID {
		t.Fatalf("command=%s mode=%s AgentSession=%s", commandStatus, newTurnMode, newAgentSessionID)
	}
}

func residentStartCommand(permit taskapplication.IssuedTaskPermit) workerapplication.StartWorkerRunCommand {
	network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	return workerapplication.StartWorkerRunCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: permit.WorkerSessionID,
		TaskID: permit.TaskID, PermitID: permit.PermitID,
		ExpectedSessionRevision: permit.ExpectedSessionRevision,
		RuntimeGeneration:       permit.RuntimeGeneration, TriggerType: "task_initial",
		StartSnapshot:  []byte(`{"working_copy":"checkpoint-1","working_copy_checkpoint_digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}`),
		AgentProfileID: "deepseek-codex-openai-v1", AgentProfileRevision: "1",
		AgentProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AgentTurnContent:   []byte(`{"text":"execute task"}`),
		TurnIdempotencyKey: "turn/" + permit.PermitID,
		RuntimeImageRef:    "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ResourceProfileID:  "local_standard_v1", NetworkPolicyID: network.ID, NetworkPolicyRevision: network.Revision, NetworkPolicyDigest: network.Digest,
		NetworkEnforcementStatus: network.Evidence().EnforcementStatus, ControlChannelEpoch: 1,
	}
}
