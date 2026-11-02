//go:build integration

package localproduct

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	platformconfig "github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestStartupDeadlineDrainClaimsDueAgentTurnBeforeRuntimeRestore(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	now := deadlineAt.Add(time.Second)
	runner := startupDeadlineTestRunner(pool, func() time.Time {
		return now
	})

	if err := drainStartupDeadlineCommands(
		ctx,
		runner.startupDeadlineScheduledCommandPoller(),
	); err != nil {
		t.Fatal(err)
	}
	assertStartupDeadlineClaimed(t, pool)
}

func TestStartupDeadlineDrainWaitsForForeignLeaseBeforeRuntimeRestore(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadlineAt := time.Now().UTC().Add(-time.Second)
	seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	if _, err := pool.Exec(ctx, `
UPDATE scheduled_commands
SET status='leased',lease_owner='foreign-deadline-owner',
    lease_expires_at=$2,fencing_token=1,attempt_count=1,updated_at=$2
WHERE scheduled_command_id=$1`,
		"deadline-command-claim",
		time.Now().UTC().Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	runner := startupDeadlineTestRunner(pool, time.Now)
	done := make(chan error, 1)
	go func() {
		done <- drainStartupDeadlineCommands(
			ctx,
			runner.startupDeadlineScheduledCommandPoller(),
		)
	}()

	select {
	case err := <-done:
		t.Fatalf(
			"startup deadline drain crossed a foreign live lease: %v",
			err,
		)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := pool.Exec(ctx, `
UPDATE scheduled_commands
SET lease_expires_at=$2
WHERE scheduled_command_id=$1
  AND status='leased'
  AND lease_owner='foreign-deadline-owner'`,
		"deadline-command-claim",
		time.Now().UTC().Add(-time.Second),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("startup deadline drain did not reclaim the expired lease")
	}
	assertStartupDeadlineClaimedWithDelivery(t, pool, 2, 2)
}

func TestStartupDeadlineDrainCancellationDoesNotReportSuccess(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithCancel(context.Background())
	deadlineAt := time.Now().UTC().Add(-time.Second)
	seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	if _, err := pool.Exec(ctx, `
UPDATE scheduled_commands
SET status='leased',lease_owner='foreign-deadline-owner',
    lease_expires_at=$2,fencing_token=1,attempt_count=1,updated_at=$2
WHERE scheduled_command_id=$1`,
		"deadline-command-claim",
		time.Now().UTC().Add(time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	runner := startupDeadlineTestRunner(pool, time.Now)
	done := make(chan error, 1)
	go func() {
		done <- drainStartupDeadlineCommands(
			ctx,
			runner.startupDeadlineScheduledCommandPoller(),
		)
	}()
	select {
	case err := <-done:
		t.Fatalf(
			"startup deadline drain crossed a foreign live lease: %v",
			err,
		)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled startup deadline drain error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled startup deadline drain did not stop")
	}
	var settlementCount int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*)
FROM worker_turn_settlements
WHERE settlement_id='turn-settlement/turn-deadline-claim'`,
	).Scan(&settlementCount); err != nil {
		t.Fatal(err)
	}
	if settlementCount != 0 {
		t.Fatalf(
			"cancelled startup deadline drain created %d settlements",
			settlementCount,
		)
	}
}

func TestStartupDeadlineWorkerClaimsDeadlineThatExpiresDuringRuntimeRestore(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 8, 30, 0, 0, time.UTC)
	seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	var nowNanos atomic.Int64
	nowNanos.Store(deadlineAt.Add(-time.Second).UnixNano())
	runner := startupDeadlineTestRunner(pool, func() time.Time {
		return time.Unix(0, nowNanos.Load()).UTC()
	})
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- runStartupDeadlineCommands(
			workerCtx,
			runner.startupDeadlineScheduledCommandPoller(),
		)
	}()

	nowNanos.Store(deadlineAt.Add(time.Second).UnixNano())
	expiry := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM worker_turn_settlements
WHERE settlement_id='turn-settlement/turn-deadline-claim'
  AND source_kind='backend_deadline'`).Scan(&count); err != nil {
			cancel()
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(expiry) {
			cancel()
			t.Fatal("startup deadline worker did not claim the expired Turn")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertStartupDeadlineClaimed(t, pool)
}

func TestBackendDeadlineClaimHandlerPersistsSourceWithoutLiveRuntimeAndLeavesDomainUnchanged(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	before := loadBackendDeadlineClaimDomainSnapshot(t, pool)
	handler := backendDeadlineClaimHandler{
		Pool: pool,
		Source: workerapp.TurnSettlementSourceService{
			Store: workerpostgres.NewTurnSettlementRepository(pool),
			Now:   func() time.Time { return deadlineAt.Add(time.Second) },
		},
	}

	if err := handler.HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}

	settlement, err := workerpostgres.NewTurnSettlementRepository(pool).
		LoadTurnSettlement(ctx, "turn-settlement/turn-deadline-claim")
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline ||
		settlement.Source.Ref != command.ID ||
		!settlement.Source.OccurredAt.Equal(deadlineAt) ||
		settlement.Revision != 1 {
		t.Fatalf("settlement=%+v", settlement)
	}
	after := loadBackendDeadlineClaimDomainSnapshot(t, pool)
	if after != before {
		t.Fatalf(
			"deadline source claim mutated domain: before=%+v after=%+v",
			before,
			after,
		)
	}
}

func startupDeadlineTestRunner(
	pool *pgxpool.Pool,
	now func() time.Time,
) *runtimeRunner {
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	runner := &runtimeRunner{
		configuration: Config{
			Profile: platformconfig.SingleUserLocalProfile{
				TenantID: "tenant-local",
			},
		},
		pool: pool,
		now:  now,
	}
	runner.scheduled = platformpostgres.ScheduledCommandPoller{
		Pool: pool,
		Handler: localScheduledCommandHandler{
			deadline: backendDeadlineClaimHandler{
				Pool: pool,
				Source: workerapp.TurnSettlementSourceService{
					Store: repository,
					Now:   now,
				},
			},
		},
		LeaseOwner:    "localproduct-scheduled",
		LeaseDuration: 30 * time.Second,
		BatchSize:     32,
		Now:           now,
	}
	return runner
}

func assertStartupDeadlineClaimed(
	t *testing.T,
	pool *pgxpool.Pool,
) {
	assertStartupDeadlineClaimedWithDelivery(t, pool, 1, 1)
}

func assertStartupDeadlineClaimedWithDelivery(
	t *testing.T,
	pool *pgxpool.Pool,
	wantFencingToken uint64,
	wantAttempts uint64,
) {
	t.Helper()
	ctx := context.Background()
	settlement, err := workerpostgres.NewTurnSettlementRepository(pool).
		LoadTurnSettlement(
			ctx,
			"turn-settlement/turn-deadline-claim",
		)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline ||
		settlement.Source.Ref != "deadline-command-claim" ||
		settlement.Phase != workerapp.TurnSettlementAccepted {
		t.Fatalf("startup deadline settlement=%+v", settlement)
	}
	var status string
	var fencingToken, attempts uint64
	if err := pool.QueryRow(ctx, `
SELECT status,fencing_token,attempt_count
FROM scheduled_commands
WHERE scheduled_command_id='deadline-command-claim'`).Scan(
		&status,
		&fencingToken,
		&attempts,
	); err != nil {
		t.Fatal(err)
	}
	if status != "applied" ||
		fencingToken != wantFencingToken ||
		attempts != wantAttempts {
		t.Fatalf(
			"startup deadline command status=%s token=%d attempts=%d, want applied/%d/%d",
			status,
			fencingToken,
			attempts,
			wantFencingToken,
			wantAttempts,
		)
	}
}

func TestBackendDeadlineClaimTransactionRejectsReplacedCurrentRuntime(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 2, 30, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	target, err := loadBackendDeadlineClaimTarget(ctx, pool, command)
	if err != nil {
		t.Fatal(err)
	}
	record, err := workerapp.NewBackendDeadlineClaimRecord(
		workerapp.ClaimBackendDeadlineCommand{
			TenantID:          target.TenantID,
			WorkerSessionID:   target.WorkerSessionID,
			WorkerRunID:       target.WorkerRunID,
			AgentSessionID:    target.AgentSessionID,
			AgentTurnID:       target.AgentTurnID,
			RuntimeInstanceID: target.RuntimeInstanceID,
			RuntimeGeneration: target.RuntimeGeneration,
			ChannelEpoch:      target.ChannelEpoch,
			DeadlineAt:        target.DeadlineAt,
			SourceRef:         command.ID,
		},
		deadlineAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id=NULL,
    runtime_generation=runtime_generation+1,
    session_revision=session_revision+1,
    updated_at=$2
WHERE worker_session_id=$1`,
		target.WorkerSessionID,
		deadlineAt.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}

	_, err = workerpostgres.NewTurnSettlementRepository(pool).
		ClaimBackendDeadlineTurnSettlement(ctx, record)
	if !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("replacement claim error=%v", err)
	}
	var settlementCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM worker_turn_settlements
WHERE settlement_id=$1`,
		record.SettlementID,
	).Scan(&settlementCount); err != nil {
		t.Fatal(err)
	}
	if settlementCount != 0 {
		t.Fatalf(
			"replacement claim persisted %d settlements",
			settlementCount,
		)
	}
}

func TestBackendDeadlineClaimHandlerReplaysExactCommandWithoutRevisionAdvance(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	now := deadlineAt.Add(time.Second)
	handler := newBackendDeadlineClaimHandler(pool, func() time.Time {
		return now
	})

	if err := handler.HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	first, err := repository.LoadTurnSettlement(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil {
		t.Fatal(err)
	}
	now = deadlineAt.Add(time.Minute)
	if err := handler.HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.LoadTurnSettlement(
		ctx,
		first.SettlementID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != first.Revision ||
		!replayed.AcceptedAt.Equal(first.AcceptedAt) ||
		replayed.Source.Digest != first.Source.Digest ||
		replayed.Source.StateDigest != first.Source.StateDigest ||
		string(replayed.Source.State) != string(first.Source.State) {
		t.Fatalf(
			"replay changed settlement: first=%+v replayed=%+v",
			first,
			replayed,
		)
	}
}

func TestBackendDeadlineClaimHandlerRetriesWhenCurrentRuntimeGraphIsMissing(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id=NULL
WHERE worker_session_id='session-deadline-claim'`); err != nil {
		t.Fatal(err)
	}
	handler := newBackendDeadlineClaimHandler(pool, func() time.Time {
		return deadlineAt.Add(time.Second)
	})

	err := handler.HandleScheduledCommand(ctx, command)
	if !errors.Is(err, errBackendDeadlineClaimRetryable) {
		t.Fatalf("error=%v", err)
	}
	var settlements int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM worker_turn_settlements
WHERE agent_turn_id='turn-deadline-claim'`).Scan(&settlements); err != nil {
		t.Fatal(err)
	}
	if settlements != 0 {
		t.Fatalf("missing Runtime graph created %d settlements", settlements)
	}
}

func TestBackendDeadlineClaimHandlerTreatsTerminalTurnAsSafeNoOp(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	if _, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='completed',revision=revision+1,terminal_at=$1,
    terminal_reason='completed'
WHERE agent_turn_id='turn-deadline-claim'`,
		deadlineAt.Add(-time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id=NULL
WHERE worker_session_id='session-deadline-claim'`); err != nil {
		t.Fatal(err)
	}
	before := loadBackendDeadlineClaimDomainSnapshotWithoutRuntime(t, pool)
	handler := newBackendDeadlineClaimHandler(pool, func() time.Time {
		return deadlineAt.Add(time.Second)
	})

	if err := handler.HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}

	after := loadBackendDeadlineClaimDomainSnapshotWithoutRuntime(t, pool)
	if after != before {
		t.Fatalf(
			"terminal no-op mutated domain: before=%+v after=%+v",
			before,
			after,
		)
	}
	var settlements int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM worker_turn_settlements
WHERE agent_turn_id='turn-deadline-claim'`).Scan(&settlements); err != nil {
		t.Fatal(err)
	}
	if settlements != 0 {
		t.Fatalf("terminal no-op created %d settlements", settlements)
	}
}

func newBackendDeadlineClaimHandler(
	pool *pgxpool.Pool,
	now func() time.Time,
) backendDeadlineClaimHandler {
	return backendDeadlineClaimHandler{
		Pool: pool,
		Source: workerapp.TurnSettlementSourceService{
			Store: workerpostgres.NewTurnSettlementRepository(pool),
			Now:   now,
		},
	}
}

type backendDeadlineClaimDomainSnapshot struct {
	SessionStatus   string
	RunStatus       string
	TurnStatus      string
	RuntimeStatus   string
	TaskStatus      string
	SessionRevision uint64
	RunRevision     uint64
	TurnRevision    uint64
	RuntimeRevision uint64
	TaskRevision    uint64
}

func loadBackendDeadlineClaimDomainSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
) backendDeadlineClaimDomainSnapshot {
	t.Helper()
	var snapshot backendDeadlineClaimDomainSnapshot
	if err := pool.QueryRow(context.Background(), `
SELECT session.status,run.status,turn.status,runtime.status,task.status,
       session.session_revision,run.revision,turn.revision,runtime.revision,
       task.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_run_id=session.active_worker_run_id
JOIN agent_turns turn
  ON turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id='turn-deadline-claim'
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=session.active_runtime_instance_id
 AND runtime.worker_session_id=session.worker_session_id
JOIN tasks task
  ON task.task_id=run.task_id
WHERE session.worker_session_id='session-deadline-claim'`).Scan(
		&snapshot.SessionStatus,
		&snapshot.RunStatus,
		&snapshot.TurnStatus,
		&snapshot.RuntimeStatus,
		&snapshot.TaskStatus,
		&snapshot.SessionRevision,
		&snapshot.RunRevision,
		&snapshot.TurnRevision,
		&snapshot.RuntimeRevision,
		&snapshot.TaskRevision,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func loadBackendDeadlineClaimDomainSnapshotWithoutRuntime(
	t *testing.T,
	pool *pgxpool.Pool,
) backendDeadlineClaimDomainSnapshot {
	t.Helper()
	var snapshot backendDeadlineClaimDomainSnapshot
	if err := pool.QueryRow(context.Background(), `
SELECT session.status,run.status,turn.status,
       (SELECT status
        FROM runtime_instances
        WHERE runtime_instance_id='runtime-deadline-claim'),
       task.status,session.session_revision,run.revision,turn.revision,
       (SELECT revision
        FROM runtime_instances
        WHERE runtime_instance_id='runtime-deadline-claim'),
       task.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_run_id=session.active_worker_run_id
JOIN agent_turns turn
  ON turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id='turn-deadline-claim'
JOIN tasks task
  ON task.task_id=run.task_id
WHERE session.worker_session_id='session-deadline-claim'`).Scan(
		&snapshot.SessionStatus,
		&snapshot.RunStatus,
		&snapshot.TurnStatus,
		&snapshot.RuntimeStatus,
		&snapshot.TaskStatus,
		&snapshot.SessionRevision,
		&snapshot.RunRevision,
		&snapshot.TurnRevision,
		&snapshot.RuntimeRevision,
		&snapshot.TaskRevision,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func seedBackendDeadlineClaimGraph(
	t *testing.T,
	pool *pgxpool.Pool,
	deadlineAt time.Time,
) platformpostgres.ScheduledCommand {
	t.Helper()
	ctx := context.Background()
	startedAt := deadlineAt.Add(-24 * time.Hour)
	createdAt := startedAt.Add(-time.Hour)
	statements := []string{
		`INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,
   source,policy_revision,dimensions,blocking_unknowns,assumptions,status,
   content_digest,evaluated_at)
VALUES
  ('assessment-deadline-claim','tenant-local','user-local','workspace-1',
   'workspace-revision-1','api','direct-ready-v1','{}'::jsonb,'[]'::jsonb,
   '[]'::jsonb,'ready_confirmed',
   'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$1)`,
		`INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,
   readiness_assessment_id,task_spec_digest,trigger_policy_revision,status,
   consumed_by_task_id,created_at,expires_at,consumed_at)
VALUES
  ('intent-deadline-claim','tenant-local','user-local','workspace-1',
   'workspace-revision-1','assessment-deadline-claim',
   'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
   'direct-create-v1','consumed','task-deadline-claim',$1,
   $1::timestamptz + interval '15 minutes',$1)`,
		`INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,
   task_spec_snapshot,task_spec_digest,creation_intent_id,worker_session_id,
   current_worker_run_id,status,acceptance_mode,result_refs,revision,created_at,
   updated_at)
VALUES
  ('task-deadline-claim','tenant-local','user-local','workspace-1',
   'workspace-revision-1','{}'::jsonb,
   'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
   'intent-deadline-claim','session-deadline-claim','run-deadline-claim',
   'running','manual','[]'::jsonb,3,$1,$1)`,
		`INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,
   working_copy_id,status,active_runtime_instance_id,bound_task_id,
   active_worker_run_id,session_revision,runtime_generation,
   quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,
   updated_at)
VALUES
  ('session-deadline-claim','tenant-local','user-local','workspace-1',
   'workspace-revision-1','working-copy-1','running','runtime-deadline-claim',
   'task-deadline-claim','run-deadline-claim',4,1,1,
   $1::timestamptz + interval '48 hours',
   $1::timestamptz + interval '14 days',$1,$1)`,
		`INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,
   agent_profile_id,agent_profile_revision,agent_profile_digest,status,revision,
   created_at,started_at)
VALUES
  ('run-deadline-claim','session-deadline-claim','task-deadline-claim',
   'task_initial','{}'::jsonb,'deepseek-codex-openai-v1','1',
   'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
   'running',2,$1,$1)`,
		`INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,
   status,revision,idempotency_key,created_at,started_at,deadline_at)
VALUES
  ('turn-deadline-claim','session-deadline-claim','run-deadline-claim',
   'agent-session-deadline-claim','prompt','{}'::jsonb,'consumed',2,
   'turn-deadline-claim',$1,$1,$2)`,
		`INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,
   container_id,status,image_ref,resource_profile_id,network_policy_id,
   control_channel_epoch,revision,created_at,started_at)
VALUES
  ('runtime-deadline-claim','session-deadline-claim','run-deadline-claim',1,
   'docker_container','container-deadline-claim','active','switchback-worker:test',
   'local_standard_v1','trusted_full_network_v1',7,5,$1,$1)`,
		`INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
VALUES
  ('deadline-command-claim','tenant-local','agent_turn_deadline','AgentTurn',
   'turn-deadline-claim',1,'{}'::jsonb,$2,'pending',
   'agent-turn-deadline/turn-deadline-claim',$1,$1)`,
	}
	for index, statement := range statements {
		arguments := []any{createdAt}
		switch index {
		case 4:
			arguments = []any{startedAt}
		case 5:
			arguments = []any{startedAt, deadlineAt}
		case 7:
			arguments = append(arguments, deadlineAt)
		}
		if _, err := pool.Exec(ctx, statement, arguments...); err != nil {
			t.Fatalf("seed Backend deadline claim graph: %v", err)
		}
	}
	return platformpostgres.ScheduledCommand{
		ID:               "deadline-command-claim",
		TenantID:         "tenant-local",
		CommandType:      workerpostgres.CommandAgentTurnDeadline,
		TargetType:       "AgentTurn",
		TargetID:         "turn-deadline-claim",
		ExpectedRevision: 1,
		Payload:          []byte(`{}`),
		DueAt:            deadlineAt,
		FencingToken:     1,
	}
}
