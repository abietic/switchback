//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestLifecycleScheduledCommandsUseServerDeadlinesAndFencing(t *testing.T) {
	t.Run("AgentTurn hard deadline", func(t *testing.T) {
		pool := postgrestest.Open(t)
		now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
		sessionID := allocateLifecycleSession(t, pool, now.Add(-48*time.Hour))
		seedDeadlineTurn(t, pool, sessionID, now)
		insertScheduledCommand(t, pool, "deadline-command", workerpostgres.CommandAgentTurnDeadline,
			"AgentTurn", "turn-deadline", 1, now, now.Add(-time.Hour))

		runLifecyclePoller(t, pool, now)
		var status, reason string
		var revision uint64
		if err := pool.QueryRow(context.Background(), `SELECT status,revision,terminal_reason
FROM agent_turns WHERE agent_turn_id='turn-deadline'`).Scan(&status, &revision, &reason); err != nil {
			t.Fatal(err)
		}
		if status != "timed_out" || revision != 2 || reason != "agent_turn_deadline" {
			t.Fatalf("Turn status=%s revision=%d reason=%s", status, revision, reason)
		}
		assertScheduledApplied(t, pool, "deadline-command", 1)
		resultContract := failurematrix.Result{
			Variant: "deadline", ExecutionOutcome: "timed_out", AcceptanceOutcome: "pending",
			Assertions: []string{"server_deadline_enforced", "turn_timed_out", "scheduled_command_fenced"},
		}
		failurematrix.AssertAndRecord(t, resultContract, resultContract)
	})

	t.Run("AgentTurn revision advance preserves fixed hard deadline", func(t *testing.T) {
		pool := postgrestest.Open(t)
		now := time.Date(2026, 7, 24, 12, 30, 0, 0, time.UTC)
		sessionID := allocateLifecycleSession(t, pool, now.Add(-48*time.Hour))
		seedDeadlineTurn(t, pool, sessionID, now)
		if _, err := pool.Exec(context.Background(), `
UPDATE agent_turns SET revision=2 WHERE agent_turn_id='turn-deadline'`); err != nil {
			t.Fatal(err)
		}
		insertScheduledCommand(t, pool, "advanced-deadline-command",
			workerpostgres.CommandAgentTurnDeadline,
			"AgentTurn", "turn-deadline", 1, now, now.Add(-time.Hour))

		runLifecyclePoller(t, pool, now)
		var status, reason string
		var revision uint64
		if err := pool.QueryRow(context.Background(), `SELECT status,revision,COALESCE(terminal_reason,'')
FROM agent_turns WHERE agent_turn_id='turn-deadline'`).Scan(
			&status, &revision, &reason,
		); err != nil {
			t.Fatal(err)
		}
		if status != "timed_out" || revision != 3 || reason != "agent_turn_deadline" {
			t.Fatalf("Turn status=%s revision=%d reason=%s", status, revision, reason)
		}
		assertScheduledApplied(t, pool, "advanced-deadline-command", 1)
	})

	t.Run("AgentTurn deadline command is fenced by immutable due time", func(t *testing.T) {
		pool := postgrestest.Open(t)
		now := time.Date(2026, 7, 24, 12, 45, 0, 0, time.UTC)
		sessionID := allocateLifecycleSession(t, pool, now.Add(-48*time.Hour))
		seedDeadlineTurn(t, pool, sessionID, now)
		insertScheduledCommand(t, pool, "stale-deadline-command",
			workerpostgres.CommandAgentTurnDeadline,
			"AgentTurn", "turn-deadline", 1, now.Add(-time.Second), now.Add(-time.Hour))

		runLifecyclePoller(t, pool, now)
		var status string
		var revision uint64
		if err := pool.QueryRow(context.Background(), `SELECT status,revision
FROM agent_turns WHERE agent_turn_id='turn-deadline'`).Scan(
			&status, &revision,
		); err != nil {
			t.Fatal(err)
		}
		if status != "consumed" || revision != 1 {
			t.Fatalf("stale command mutated Turn status=%s revision=%d", status, revision)
		}
		assertScheduledApplied(t, pool, "stale-deadline-command", 1)
	})

	for _, testCase := range []struct {
		name          string
		sessionStatus string
		wantStatus    string
		wantReserved  int
	}{
		{"idle closes Session", "idle", "closed", 0},
		{"waiting_user pauses idle expiry", "waiting_user", "waiting_user", 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			now := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)
			sessionID := allocateLifecycleSession(t, pool, now.Add(-49*time.Hour))
			if _, err := pool.Exec(context.Background(), `UPDATE worker_sessions
SET status=$2,idle_expires_at=$3,updated_at=$3 WHERE worker_session_id=$1`,
				sessionID, testCase.sessionStatus, now.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(context.Background(), `UPDATE scheduled_commands
SET due_at=$2 WHERE target_id=$1 AND command_type=$3`,
				sessionID, now.Add(-time.Second), workerpostgres.CommandWorkerSessionIdle); err != nil {
				t.Fatal(err)
			}
			runLifecyclePoller(t, pool, now)
			var status string
			if err := pool.QueryRow(context.Background(), `SELECT status FROM worker_sessions WHERE worker_session_id=$1`, sessionID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			var reserved int
			if err := pool.QueryRow(context.Background(), `SELECT reserved_count FROM worker_session_continuity_quotas
WHERE tenant_id='tenant-local' AND user_id='user-local'`).Scan(&reserved); err != nil {
				t.Fatal(err)
			}
			if status != testCase.wantStatus || reserved != testCase.wantReserved {
				t.Fatalf("Session status=%s reserved=%d", status, reserved)
			}
		})
	}

	for _, testCase := range []struct {
		taskStatus string
		wantStatus string
	}{
		{"running", "waiting_user"},
		{"delivered", "delivered"},
	} {
		t.Run("absolute expiry preserves "+testCase.taskStatus, func(t *testing.T) {
			pool := postgrestest.Open(t)
			now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
			sessionID := allocateLifecycleSession(t, pool, now.Add(-15*24*time.Hour))
			seedActiveLifecycleRun(t, pool, sessionID, testCase.taskStatus, now)
			if _, err := pool.Exec(context.Background(), `UPDATE worker_sessions
SET absolute_expires_at=$2 WHERE worker_session_id=$1`, sessionID, now.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(context.Background(), `UPDATE scheduled_commands
SET due_at=$2 WHERE target_id=$1 AND command_type=$3`,
				sessionID, now.Add(-time.Second), workerpostgres.CommandWorkerSessionAbsolute); err != nil {
				t.Fatal(err)
			}
			runLifecyclePoller(t, pool, now)
			var sessionStatus, runStatus, taskStatus string
			if err := pool.QueryRow(context.Background(), `SELECT status FROM worker_sessions WHERE worker_session_id=$1`, sessionID).Scan(&sessionStatus); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), `SELECT status FROM worker_runs WHERE worker_run_id='run-lifecycle'`).Scan(&runStatus); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(context.Background(), `SELECT status FROM tasks WHERE task_id='task-01'`).Scan(&taskStatus); err != nil {
				t.Fatal(err)
			}
			if sessionStatus != "expired" || runStatus != "timed_out" || taskStatus != testCase.wantStatus {
				t.Fatalf("Session=%s Run=%s Task=%s", sessionStatus, runStatus, taskStatus)
			}
			var reserved int
			if err := pool.QueryRow(context.Background(), `SELECT reserved_count FROM worker_session_continuity_quotas
WHERE tenant_id='tenant-local' AND user_id='user-local'`).Scan(&reserved); err != nil {
				t.Fatal(err)
			}
			if reserved != 0 {
				t.Fatalf("absolute expiry retained quota=%d", reserved)
			}
		})
	}
}

func TestAbsoluteExpiryDefersToActiveRuntimeRecoveryOwner(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	expiresAt := base.Add(8 * time.Minute)
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions SET absolute_expires_at=$2 WHERE worker_session_id=$1`,
		started.WorkerSessionID, expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := (workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}).Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:                 domain.RecoveryTriggerContainerExit,
		EvidenceRef:             "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:              base.Add(6 * time.Minute),
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision:     active.RunRevision,
		ExpectedTurnRevision:    active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	}); err != nil {
		t.Fatal(err)
	}
	var sessionRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session_revision FROM worker_sessions WHERE worker_session_id=$1`,
		started.WorkerSessionID,
	).Scan(&sessionRevision); err != nil {
		t.Fatal(err)
	}
	if err := (workerpostgres.LifecycleCommandHandler{
		Pool: pool, Now: func() time.Time { return expiresAt },
	}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{
		TenantID: "tenant-local", CommandType: workerpostgres.CommandWorkerSessionAbsolute,
		TargetType: "WorkerSession", TargetID: started.WorkerSessionID,
		ExpectedRevision: sessionRevision, DueAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	var status, phase string
	if err := pool.QueryRow(ctx, `
SELECT session.status,incident.phase
FROM worker_sessions session
JOIN runtime_recovery_incidents incident
  ON incident.worker_session_id=session.worker_session_id
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
	).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "recovering" || phase != "open" {
		t.Fatalf("session=%s incident=%s", status, phase)
	}
}

func TestAgentTurnDeadlineDefersToActiveRuntimeRecoveryOwner(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 13, 30, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt := base.Add(8 * time.Minute)
	if _, err := pool.Exec(ctx, `
UPDATE agent_turns
SET started_at=$2::timestamptz - interval '24 hours',deadline_at=$2
WHERE agent_turn_id=$1`,
		started.AgentTurnID, deadlineAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := (workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}).Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1, FailedChannelEpoch: 1,
		Trigger:                 domain.RecoveryTriggerContainerExit,
		EvidenceRef:             "runtime-inspection/" + started.RuntimeInstanceID,
		EvidenceAt:              base.Add(6 * time.Minute),
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision:     active.RunRevision,
		ExpectedTurnRevision:    active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-recovery/" + started.WorkerSessionID + "/g1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := (workerpostgres.LifecycleCommandHandler{
		Pool: pool, Now: func() time.Time { return deadlineAt },
	}).HandleScheduledCommand(ctx, platformpostgres.ScheduledCommand{
		TenantID: "tenant-local", CommandType: workerpostgres.CommandAgentTurnDeadline,
		TargetType: "AgentTurn", TargetID: started.AgentTurnID,
		ExpectedRevision: active.TurnRevision, DueAt: deadlineAt,
	}); err != nil {
		t.Fatal(err)
	}
	var sessionStatus, turnStatus, phase string
	var turnRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.status,turn.status,turn.revision,incident.phase
FROM worker_sessions session
JOIN agent_turns turn ON turn.worker_session_id=session.worker_session_id
JOIN runtime_recovery_incidents incident
  ON incident.worker_session_id=session.worker_session_id
WHERE session.worker_session_id=$1 AND turn.agent_turn_id=$2`,
		started.WorkerSessionID, started.AgentTurnID,
	).Scan(&sessionStatus, &turnStatus, &turnRevision, &phase); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "recovering" || turnStatus != "delivered" ||
		turnRevision != active.TurnRevision || phase != "open" {
		t.Fatalf(
			"session=%s turn=%s/r%d incident=%s",
			sessionStatus, turnStatus, turnRevision, phase,
		)
	}
}

func allocateLifecycleSession(t *testing.T, pool *pgxpool.Pool, createdAt time.Time) string {
	t.Helper()
	seedAllocationRequests(t, pool, 1, createdAt)
	result, err := (workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return createdAt },
	}).Execute(context.Background(), workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.WorkerSessionID
}

func seedDeadlineTurn(t *testing.T, pool *pgxpool.Pool, sessionID string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at)
VALUES ('run-deadline',$1,'task-01','task_initial','{}'::jsonb,'deepseek-codex-openai-v1','1',
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','running',1,$2,$2)`,
		sessionID, now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
   idempotency_key,created_at,started_at,deadline_at)
VALUES ('turn-deadline',$1,'run-deadline','agent-session-1','prompt','{}'::jsonb,'consumed',1,
  'turn-deadline-key',$2,$3,$3::timestamptz + interval '24 hours')`,
		sessionID, now.Add(-25*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status='running',bound_task_id='task-01',active_worker_run_id='run-deadline',updated_at=$2
WHERE worker_session_id=$1`, sessionID, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func seedActiveLifecycleRun(t *testing.T, pool *pgxpool.Pool, sessionID, taskStatus string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE tasks
SET status=$1,worker_session_id=$2,session_allocation_sequence=NULL,current_worker_run_id='run-lifecycle',
    updated_at=$3 WHERE task_id='task-01'`, taskStatus, sessionID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at)
VALUES ('run-lifecycle',$1,'task-01','task_initial','{}'::jsonb,'deepseek-codex-openai-v1','1',
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','running',1,$2,$2)`,
		sessionID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status='running',bound_task_id='task-01',active_worker_run_id='run-lifecycle',updated_at=$2
WHERE worker_session_id=$1`, sessionID, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func insertScheduledCommand(
	t *testing.T,
	pool *pgxpool.Pool,
	id, commandType, targetType, targetID string,
	expectedRevision uint64,
	dueAt, createdAt time.Time,
) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,fencing_token,attempt_count,idempotency_key,created_at,updated_at)
VALUES ($1,'tenant-local',$2,$3,$4,$5,'{}'::jsonb,$6,'pending',0,0,$7,$8,$8)`,
		id, commandType, targetType, targetID, expectedRevision, dueAt, commandType+"/"+targetID, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

func runLifecyclePoller(t *testing.T, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	clock := advancingTestClock(now)
	poller := platformpostgres.ScheduledCommandPoller{
		Pool: pool, Handler: workerpostgres.LifecycleCommandHandler{Pool: pool, Now: clock},
		LeaseOwner: "lifecycle-poller", LeaseDuration: time.Minute, BatchSize: 20, Now: clock,
	}
	if _, err := poller.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func advancingTestClock(now time.Time) func() time.Time {
	return func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}
}

func assertScheduledApplied(t *testing.T, pool *pgxpool.Pool, commandID string, fencingToken uint64) {
	t.Helper()
	var status string
	var token uint64
	if err := pool.QueryRow(context.Background(), `SELECT status,fencing_token FROM scheduled_commands
WHERE scheduled_command_id=$1`, commandID).Scan(&status, &token); err != nil {
		t.Fatal(err)
	}
	if status != "applied" || token != fencingToken {
		t.Fatalf("command %s status=%s token=%d", commandID, status, token)
	}
}
