//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSettlementTerminalApplyAndContinuationLockOrder(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	f := prepareTerminalSettlementApplyFixture(t, pool, time.Now().Add(-10*time.Minute), "apply-lock")
	f.Apply.TurnSettlement = turnSettlementApplyAuthority(f.Settlement)
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return f.Settlement.UpdatedAt.Add(time.Second) }}
	if _, err := journal.ApplyEvent(ctx, f.Apply); err != nil {
		t.Fatal(err)
	}
	service := workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}
	settlement, err := service.Load(ctx, f.Settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err = service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: settlement.SettlementID, ExpectedRevision: settlement.Revision, OwnerID: "apply-lock-holder"})
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Phase != workerapp.TurnSettlementTerminalEventApplied {
		t.Fatalf("illegal fixture phase %s", settlement.Phase)
	}
	var checkpoint struct {
		Domain workerapp.TurnSettlementDomainProof `json:"domain"`
	}
	if err := json.Unmarshal(settlement.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	i := f.Started
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET workspace_id='workspace-local' WHERE worker_session_id=$1`, i.WorkerSessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tasks SET workspace_id='workspace-local' WHERE task_id='task-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO worker_user_commands(command_id,tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,command_type,content,expected_session_revision,expected_run_revision,expected_turn_revision,queue_sequence,status,idempotency_key,created_at,updated_at) VALUES('apply-lock-queue','tenant-local','user-local',$1,$2,$3,'queue','Continue',1,1,1,1,'accepted','apply-lock-queue',now(),now())`, i.WorkerSessionID, i.WorkerRunID, i.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	d := checkpoint.Domain
	record := workerapp.ContinueQueuedTurnRecord{Command: workerapp.ContinueQueuedTurnCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: i.WorkerSessionID, PreviousWorkerRunID: i.WorkerRunID, PreviousAgentTurnID: i.AgentTurnID, RuntimeInstanceID: i.RuntimeInstanceID, RuntimeGeneration: 1, QueueCommandID: "apply-lock-queue", ExpectedSessionRevision: d.SessionRevision, ExpectedRunRevision: d.RunRevision, ExpectedTurnRevision: d.TurnRevision, ExpectedRuntimeRevision: d.RuntimeRevision}, WorkerRunID: "apply-lock-successor", AgentTurnID: "apply-lock-successor-turn", ScheduledCommandID: "apply-lock-deadline", EventID: "apply-lock-event", Now: time.Now()}
	fields := typedTurnSettlementCheckpoint(t, settlement.PhaseState)
	fields.AdditionalFields["continuation_plan"], _ = json.Marshal(map[string]any{"action": "create_queued_turn", "queue_command_id": record.Command.QueueCommandID, "expected_session_revision": d.SessionRevision, "expected_run_revision": d.RunRevision, "expected_turn_revision": d.TurnRevision, "expected_runtime_revision": d.RuntimeRevision})
	settlement, err = service.FreezeCheckpoint(ctx, workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: settlement.SettlementID, ExpectedRevision: settlement.Revision, LeaseOwnerID: settlement.LeaseOwnerID, LeaseFence: settlement.LeaseFence, Checkpoint: fields})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT settlement_id FROM worker_turn_settlements WHERE settlement_id=$1 FOR UPDATE`, settlement.SettlementID); err != nil {
		t.Fatal(err)
	}
	queuePool := lockOrderPool(t, pool, "apply-lock-continuation")
	queueStore := workerpostgres.NewQueuedTurnStore(queuePool)
	queueResult := make(chan error, 1)
	go func() { _, err := queueStore.ContinueSettlementQueuedTurn(ctx, settlement, record); queueResult <- err }()
	waitLockOrderQuery(t, pool, "apply-lock-continuation", "SELECT settlement.settlement_id")
	var continuationBarrier bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity c JOIN pg_locks l ON l.pid=c.pid
 WHERE c.application_name='apply-lock-continuation' AND l.locktype='advisory' AND l.granted
 AND $1=ANY(pg_blocking_pids(c.pid)))`, blocker.Conn().PgConn().PID()).Scan(&continuationBarrier); err != nil {
		t.Fatal(err)
	}
	if !continuationBarrier {
		t.Fatal("continuation did not hold outcome while waiting on owned settlement barrier")
	}
	applyPool := lockOrderPool(t, pool, "apply-lock-replay")
	journal.Store = workerpostgres.NewRuntimeControlJournal(applyPool)
	applyResult := make(chan error, 1)
	go func() { _, err := journal.ApplyEvent(ctx, f.Apply); applyResult <- err }()
	waitLockOrderQuery(t, pool, "apply-lock-replay", "")
	var originalOutcomeWait bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_stat_activity c ON c.pid=ANY(pg_blocking_pids(a.pid)) WHERE a.application_name='apply-lock-replay' AND c.application_name='apply-lock-continuation' AND a.query LIKE '%pg_advisory_xact_lock%')`).Scan(&originalOutcomeWait); err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog: duplicate Apply waits on original continuation outcome=%t", originalOutcomeWait)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	queueErr, applyErr := <-queueResult, <-applyResult
	for _, err := range []error{queueErr, applyErr} {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("terminal Apply/continuation deadlock: continuation=%v apply=%v", queueErr, applyErr)
		}
	}
	if queueErr != nil || applyErr != nil {
		t.Fatalf("valid continuation/replay failed: continuation=%v apply=%v", queueErr, applyErr)
	}
	if !originalOutcomeWait {
		t.Fatal("duplicate terminal Apply did not wait before graph on original Turn outcome")
	}
	// Simulate lost responses after successor creation; both public retries must
	// keep the original settlement identity and return their committed result.
	if _, err := queueStore.ContinueSettlementQueuedTurn(ctx, settlement, record); err != nil {
		t.Fatalf("response-loss continuation replay: %v", err)
	}
	successorBlocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer successorBlocker.Rollback(context.Background())
	if _, err := successorBlocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "worker-runtime:agent-turn-outcome:"+record.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	replayCtx, replayCancel := context.WithTimeout(ctx, time.Second)
	defer replayCancel()
	if result, err := journal.ApplyEvent(replayCtx, f.Apply); err != nil || !result.Message.Duplicate {
		t.Fatalf("old terminal replay after successor: duplicate=%t err=%v", result.Message.Duplicate, err)
	}
	var successors int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_turns WHERE idempotency_key=$1`, "queued-turn/"+record.Command.QueueCommandID).Scan(&successors); err != nil || successors != 1 {
		t.Fatalf("response-loss replay successor identities=%d err=%v", successors, err)
	}
}
