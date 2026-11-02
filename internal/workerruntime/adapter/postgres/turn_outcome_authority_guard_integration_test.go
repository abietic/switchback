//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestTurnOutcomeAuthorityCreatedAfterStartupSnapshotBlocksOrdinaryMutation(
	t *testing.T,
) {
	tests := []struct {
		name string
		run  func(
			context.Context,
			*pgxpool.Pool,
			workerapplication.StartedWorkerRun,
			workerapplication.ResidentRuntimeState,
			time.Time,
		) error
	}{
		{
			name: "runtime recovery open",
			run: func(
				ctx context.Context,
				pool *pgxpool.Pool,
				started workerapplication.StartedWorkerRun,
				active workerapplication.ResidentRuntimeState,
				now time.Time,
			) error {
				_, err := (workerapplication.RuntimeRecoveryService{
					Store: workerpostgres.NewRuntimeRecoveryStore(pool),
					IDs:   &concurrentAllocationIDs{},
					Now:   func() time.Time { return now },
				}).Open(
					ctx,
					runtimeRecoveryOpenCommand(
						started,
						active,
						now.Add(-time.Second),
						domain.RecoveryTriggerContainerExit,
					),
				)
				return err
			},
		},
		{
			name: "runtime loss observe",
			run: func(
				ctx context.Context,
				pool *pgxpool.Pool,
				started workerapplication.StartedWorkerRun,
				active workerapplication.ResidentRuntimeState,
				now time.Time,
			) error {
				_, err := runtimeLossServiceForGuardTest(pool, now).Observe(
					ctx,
					runtimeLossCommandForGuardTest(
						started,
						active,
						"runtime-loss/after-startup-snapshot",
					),
				)
				return err
			},
		},
		{
			name: "unowned runtime reconcile",
			run: func(
				ctx context.Context,
				pool *pgxpool.Pool,
				started workerapplication.StartedWorkerRun,
				active workerapplication.ResidentRuntimeState,
				now time.Time,
			) error {
				_, err := (workerapplication.UnownedRuntimeReconciler{
					Store: workerpostgres.NewRuntimeLossStore(pool),
					Cleaner: &fake.RuntimeProvisioner{
						CleanupResult: workerapplication.RuntimeCleanupObservation{
							Confirmed:       true,
							ContainerRef:    "container-recovery-1",
							ContainerExists: false,
							ObservedAt:      now,
						},
					},
					Control: &fake.WorkerControl{},
					IDs:     &concurrentAllocationIDs{},
					Now:     func() time.Time { return now },
				}).Execute(
					ctx,
					workerapplication.ReconcileUnownedRuntimeCommand{
						TenantID:                "tenant-local",
						WorkerSessionID:         started.WorkerSessionID,
						WorkerRunID:             started.WorkerRunID,
						AgentTurnID:             started.AgentTurnID,
						RuntimeInstanceID:       started.RuntimeInstanceID,
						ExpectedContainerID:     "container-recovery-1",
						RuntimeGeneration:       started.RuntimeGeneration,
						RuntimeStatus:           "active",
						ExpectedSessionRevision: active.SessionRevision,
						ExpectedRunRevision:     active.RunRevision,
						ExpectedTurnRevision:    active.TurnRevision,
						ExpectedRuntimeRevision: active.RuntimeRevision,
						IdempotencyKey:          "unowned-runtime/after-startup-snapshot",
					},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
			started, active := startActiveRecoveryFixture(t, pool, base)

			assertNoTurnSettlementAtStartupSnapshot(t, pool, started.AgentTurnID)
			claimBackendDeadlineAfterStartupSnapshot(t, pool, started)
			before := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)

			err := test.run(
				ctx,
				pool,
				started,
				active,
				base.Add(8*time.Minute),
			)
			if !errors.Is(
				err,
				workerapplication.ErrTurnOutcomeAuthorityClaimed,
			) {
				t.Fatalf("ordinary mutation after startup snapshot error=%v", err)
			}

			after := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)
			if after != before {
				t.Fatalf(
					"rejected ordinary mutation changed database:\nbefore=%+v\nafter=%+v",
					before,
					after,
				)
			}
		})
	}
}

func TestRuntimeLossAndBackendDeadlineClaimShareAgentTurnOutcomeLock(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 10, 15, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	before := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	lockKey := "worker-runtime:agent-turn-outcome:" + started.AgentTurnID
	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_lock(hashtextextended($1, 0))`,
		lockKey,
	); err != nil {
		t.Fatal(err)
	}
	lockHeld := true
	unlock := func() {
		if !lockHeld {
			return
		}
		var unlocked bool
		if err := blocker.QueryRow(
			ctx,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`,
			lockKey,
		).Scan(&unlocked); err != nil {
			t.Errorf("unlock AgentTurn outcome guard test barrier: %v", err)
		} else if !unlocked {
			t.Error("AgentTurn outcome guard test barrier was not held")
		}
		lockHeld = false
	}
	defer unlock()

	claimResult := make(chan error, 1)
	go func() {
		_, err := (workerapplication.TurnSettlementSourceService{
			Store: workerpostgres.NewTurnSettlementRepository(pool),
			Now:   func() time.Time { return deadlineAt },
		}).ClaimBackendDeadline(
			ctx,
			backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
		)
		claimResult <- err
	}()
	claimWaiters, err := waitForAgentTurnOutcomeWaiters(
		ctx,
		pool,
		lockKey,
		1,
		3*time.Second,
	)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if claimWaiters != 1 {
		unlock()
		t.Fatalf("Backend deadline claim lock waiters=%d", claimWaiters)
	}

	lossResult := make(chan error, 1)
	go func() {
		_, err := runtimeLossServiceForGuardTest(
			pool,
			base.Add(8*time.Minute),
		).Observe(
			ctx,
			runtimeLossCommandForGuardTest(
				started,
				active,
				"runtime-loss/races-backend-deadline",
			),
		)
		lossResult <- err
	}()
	waiters, err := waitForAgentTurnOutcomeWaiters(
		ctx,
		pool,
		lockKey,
		2,
		3*time.Second,
	)
	unlock()
	if err != nil {
		t.Fatal(err)
	}

	var claimErr, lossErr error
	select {
	case claimErr = <-claimResult:
	case <-time.After(5 * time.Second):
		t.Fatal("Backend deadline claim did not finish after outcome lock release")
	}
	select {
	case lossErr = <-lossResult:
	case <-time.After(5 * time.Second):
		t.Fatal("Runtime loss did not finish after outcome lock release")
	}
	if waiters != 2 {
		t.Fatalf(
			"Backend deadline claim and Runtime loss did not share lock: waiters=%d claim=%v loss=%v",
			waiters,
			claimErr,
			lossErr,
		)
	}
	if claimErr != nil {
		t.Fatalf("Backend deadline claim lost ordered authority: %v", claimErr)
	}
	if !errors.Is(
		lossErr,
		workerapplication.ErrTurnOutcomeAuthorityClaimed,
	) {
		t.Fatalf("Runtime loss after ordered Backend deadline claim error=%v", lossErr)
	}
	after := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)
	if after != before {
		t.Fatalf(
			"ordered Runtime loss rejection changed database:\nbefore=%+v\nafter=%+v",
			before,
			after,
		)
	}
}

func TestRuntimeLossTurnOutcomeAuthorityCompletionSemantics(t *testing.T) {
	t.Run("no settlement permits ordinary loss", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		base := time.Date(2026, 7, 27, 10, 30, 0, 0, time.UTC)
		started, active := startActiveRecoveryFixture(t, pool, base)

		result, err := runtimeLossServiceForGuardTest(
			pool,
			base.Add(8*time.Minute),
		).Observe(
			ctx,
			runtimeLossCommandForGuardTest(
				started,
				active,
				"runtime-loss/no-settlement",
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Lost ||
			result.State.RuntimeStatus != "lost" ||
			result.State.TurnStatus != domain.TurnFailed {
			t.Fatalf("ordinary loss without settlement=%+v", result)
		}
	})

	t.Run("completed runtime event permits ordinary loss", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		base := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
		started, active := startActiveRecoveryFixture(t, pool, base)
		active = completeIgnoredRuntimeEventForGuardTest(
			t,
			pool,
			started,
			active,
			base.Add(6*time.Minute),
		)

		result, err := runtimeLossServiceForGuardTest(
			pool,
			base.Add(8*time.Minute),
		).Observe(
			ctx,
			runtimeLossCommandForGuardTest(
				started,
				active,
				"runtime-loss/completed-runtime-event",
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Lost ||
			result.State.RuntimeStatus != "lost" ||
			result.State.TurnStatus != domain.TurnFailed {
			t.Fatalf("ordinary loss after completed Runtime event=%+v", result)
		}
	})

	t.Run("completed backend deadline still blocks ordinary loss", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		base := time.Date(2026, 7, 27, 11, 30, 0, 0, time.UTC)
		started, active := startActiveRecoveryFixture(t, pool, base)
		claimBackendDeadlineAfterStartupSnapshot(t, pool, started)
		completeBackendDeadlineAuthorityForGuardTest(
			t,
			pool,
			started.AgentTurnID,
			base.Add(7*time.Minute),
		)
		before := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)

		_, err := runtimeLossServiceForGuardTest(
			pool,
			base.Add(8*time.Minute),
		).Observe(
			ctx,
			runtimeLossCommandForGuardTest(
				started,
				active,
				"runtime-loss/completed-backend-deadline",
			),
		)
		if !errors.Is(
			err,
			workerapplication.ErrTurnOutcomeAuthorityClaimed,
		) {
			t.Fatalf("ordinary loss after completed Backend deadline error=%v", err)
		}
		after := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)
		if after != before {
			t.Fatalf(
				"completed Backend deadline rejection changed database:\nbefore=%+v\nafter=%+v",
				before,
				after,
			)
		}
	})
}

func TestRuntimeRecoveryStillRejectsCompletedBackendDeadlineAuthority(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	claimBackendDeadlineAfterStartupSnapshot(t, pool, started)
	completeBackendDeadlineAuthorityForGuardTest(
		t,
		pool,
		started.AgentTurnID,
		base.Add(7*time.Minute),
	)
	before := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)

	_, err := (workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(8 * time.Minute) },
	}).Open(
		ctx,
		runtimeRecoveryOpenCommand(
			started,
			active,
			base.Add(7*time.Minute),
			domain.RecoveryTriggerContainerExit,
		),
	)
	if !errors.Is(err, workerapplication.ErrTurnOutcomeAuthorityClaimed) {
		t.Fatalf("Runtime recovery after completed Backend deadline error=%v", err)
	}
	after := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)
	if after != before {
		t.Fatalf(
			"completed Backend deadline recovery rejection changed database:\nbefore=%+v\nafter=%+v",
			before,
			after,
		)
	}
}

func TestRuntimeLossExactReplayPrecedesLaterOutcomeAuthorityGuard(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	command := runtimeLossCommandForGuardTest(
		started,
		active,
		"runtime-loss/exact-replay-before-guard",
	)
	service := runtimeLossServiceForGuardTest(pool, base.Add(8*time.Minute))
	first, err := service.Observe(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	insertCompletedBackendDeadlineAuthorityForGuardTest(
		t,
		pool,
		started,
		base.Add(9*time.Minute),
	)
	beforeReplay := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)

	replayed, err := service.Observe(ctx, command)
	if err != nil {
		t.Fatalf("exact Runtime loss replay after later authority error=%v", err)
	}
	if !replayed.Lost ||
		replayed.State.RuntimeInstanceID != first.State.RuntimeInstanceID ||
		replayed.State.RuntimeRevision != first.State.RuntimeRevision ||
		replayed.State.SessionRevision != first.State.SessionRevision ||
		replayed.State.RunRevision != first.State.RunRevision ||
		replayed.State.TurnRevision != first.State.TurnRevision {
		t.Fatalf("exact Runtime loss replay=%+v first=%+v", replayed, first)
	}
	afterReplay := loadTurnOutcomeAuthorityMutationSnapshot(t, pool, started)
	if afterReplay != beforeReplay {
		t.Fatalf(
			"exact Runtime loss replay changed database:\nbefore=%+v\nafter=%+v",
			beforeReplay,
			afterReplay,
		)
	}
}

func assertNoTurnSettlementAtStartupSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	agentTurnID string,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM worker_turn_settlements WHERE agent_turn_id=$1`,
		agentTurnID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("startup snapshot settlement count=%d", count)
	}
}

func claimBackendDeadlineAfterStartupSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapplication.StartedWorkerRun,
) {
	t.Helper()
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	if _, err := (workerapplication.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		context.Background(),
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	); err != nil {
		t.Fatal(err)
	}
}

func completeBackendDeadlineAuthorityForGuardTest(
	t *testing.T,
	pool *pgxpool.Pool,
	agentTurnID string,
	completedAt time.Time,
) {
	t.Helper()
	const digest = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	tag, err := pool.Exec(
		context.Background(),
		`
UPDATE worker_turn_settlements
SET phase='completed',
    terminal_event_disposition='ignore',
    terminal_apply_outcome_digest=$2,
    terminal_event_applied_at=GREATEST($3, accepted_at),
    completed_at=GREATEST($3, accepted_at),
    updated_at=GREATEST($3, accepted_at),
    revision=revision+1
WHERE agent_turn_id=$1 AND source_kind='backend_deadline'`,
		agentTurnID,
		digest,
		completedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf(
			"complete Backend deadline authority rows=%d",
			tag.RowsAffected(),
		)
	}
}

func insertCompletedBackendDeadlineAuthorityForGuardTest(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapplication.StartedWorkerRun,
	completedAt time.Time,
) {
	t.Helper()
	const digest = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	tag, err := pool.Exec(
		context.Background(),
		`
INSERT INTO worker_turn_settlements
  (settlement_id,tenant_id,worker_session_id,worker_run_id,
   agent_session_id,agent_turn_id,
   source_kind,source_reason,source_occurred_at,source_ref,source_digest,
   source_state,source_state_digest,
   terminal_event_disposition,terminal_apply_outcome_digest,
   phase,phase_state,phase_state_digest,revision,
   accepted_at,updated_at,terminal_event_applied_at,completed_at)
SELECT
  'turn-settlement/' || turn.agent_turn_id,
  session.tenant_id,
  session.worker_session_id,
  run.worker_run_id,
  turn.agent_session_id,
  turn.agent_turn_id,
  'backend_deadline',
  'agent_turn_deadline',
  $5,
  'scheduled-command/replay-proof',
  $6,
  '{}'::jsonb,
  $6,
  'ignore',
  $6,
  'completed',
  '{}'::jsonb,
  $6,
  1,
  $5,
  $5,
  $5,
  $5
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id=$3
WHERE session.worker_session_id=$1
  AND turn.agent_session_id=$4`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.AgentSessionID,
		completedAt,
		digest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf(
			"insert completed Backend deadline authority rows=%d",
			tag.RowsAffected(),
		)
	}
}

func completeIgnoredRuntimeEventForGuardTest(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapplication.StartedWorkerRun,
	active workerapplication.ResidentRuntimeState,
	acceptedAt time.Time,
) workerapplication.ResidentRuntimeState {
	t.Helper()
	ctx := context.Background()
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		acceptedAt,
	)
	const messageID = "completed-runtime-event-before-loss"
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      messageID,
			IdempotencyKey: messageID,
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				messageID,
				messageID,
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ApplyEvent(
		ctx,
		workerapplication.ApplyRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      messageID,
			IdempotencyKey: messageID + "/apply",
			Outcome: json.RawMessage(
				`{"status":"ignored_runtime_recovery_signal","effect_disposition":"ignore"}`,
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot := loadRecoveryBusinessSnapshot(t, pool, started)
	active.SessionRevision = snapshot.SessionRevision
	active.RunRevision = snapshot.RunRevision
	active.TurnRevision = snapshot.TurnRevision
	active.RuntimeRevision = snapshot.RuntimeRevision
	return active
}

func runtimeLossServiceForGuardTest(
	pool *pgxpool.Pool,
	now time.Time,
) workerapplication.RuntimeLossService {
	return workerapplication.RuntimeLossService{
		Store: workerpostgres.NewRuntimeLossStore(pool),
		Provisioner: &fake.RuntimeProvisioner{
			Inspection: workerapplication.RuntimeInspection{
				Exists:      false,
				Running:     false,
				ContainerID: "container-recovery-1",
				Generation:  1,
				ObservedAt:  now,
			},
		},
		Control: &fake.WorkerControl{},
		IDs:     &concurrentAllocationIDs{},
		Now:     func() time.Time { return now },
	}
}

func runtimeLossCommandForGuardTest(
	started workerapplication.StartedWorkerRun,
	active workerapplication.ResidentRuntimeState,
	idempotencyKey string,
) workerapplication.ObserveRuntimeLossCommand {
	return workerapplication.ObserveRuntimeLossCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		ExpectedContainerID:     "container-recovery-1",
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision:     active.RunRevision,
		ExpectedTurnRevision:    active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          idempotencyKey,
	}
}

type turnOutcomeAuthorityMutationSnapshot struct {
	Business                recoveryBusinessSnapshot
	OutboxEvents            int
	TaskTransitions         int
	RecoveryIncidents       int
	PendingDeadlineCommands int
	ActiveDispatchPermits   int
}

func loadTurnOutcomeAuthorityMutationSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapplication.StartedWorkerRun,
) turnOutcomeAuthorityMutationSnapshot {
	t.Helper()
	snapshot := turnOutcomeAuthorityMutationSnapshot{
		Business: loadRecoveryBusinessSnapshot(t, pool, started),
	}
	if err := pool.QueryRow(
		context.Background(),
		`
SELECT
  (SELECT count(*) FROM outbox_events),
  (SELECT count(*) FROM task_transitions),
  (SELECT count(*) FROM runtime_recovery_incidents),
  (SELECT count(*)
   FROM scheduled_commands
   WHERE target_type='AgentTurn' AND target_id=$1 AND status='pending'),
  (SELECT count(*)
   FROM session_dispatch_permits
   WHERE worker_session_id=$2 AND status='active')`,
		started.AgentTurnID,
		started.WorkerSessionID,
	).Scan(
		&snapshot.OutboxEvents,
		&snapshot.TaskTransitions,
		&snapshot.RecoveryIncidents,
		&snapshot.PendingDeadlineCommands,
		&snapshot.ActiveDispatchPermits,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func waitForAgentTurnOutcomeWaiters(
	ctx context.Context,
	pool *pgxpool.Pool,
	lockKey string,
	want int,
	timeout time.Duration,
) (int, error) {
	deadline := time.Now().Add(timeout)
	var waiters int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_locks
WHERE locktype='advisory'
  AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
  AND classid=((hashtextextended($1,0) >> 32) & 4294967295)::oid
  AND objid=(hashtextextended($1,0) & 4294967295)::oid
  AND objsubid=1
  AND NOT granted`,
			lockKey,
		).Scan(&waiters); err != nil {
			return 0, err
		}
		if waiters == want {
			return waiters, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return waiters, nil
}
