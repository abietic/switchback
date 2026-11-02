//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSettlementQueueAndRuntimeLossLockOrder(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "generic_queue", true: "old_settlement_successor_loss"}[replay], func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			base := time.Now().UTC().Add(-10 * time.Minute)
			var started workerapp.StartedWorkerRun
			var state workerapp.ResidentRuntimeState
			var settlement workerapp.TurnSettlement
			if replay {
				fixture := prepareTerminalSettlementApplyFixture(t, pool, base, "lock-order")
				started = fixture.Started
				fixture.Apply.TurnSettlement = turnSettlementApplyAuthority(fixture.Settlement)
				if _, err := (workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return fixture.Settlement.UpdatedAt.Add(time.Second) }}).ApplyEvent(ctx, fixture.Apply); err != nil {
					t.Fatal(err)
				}
				service := workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}
				var err error
				settlement, err = service.Load(ctx, fixture.Settlement.SettlementID)
				if err != nil {
					t.Fatal(err)
				}
				settlement, err = service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: settlement.SettlementID, ExpectedRevision: settlement.Revision, OwnerID: "lock-order-holder"})
				if err != nil {
					t.Fatal(err)
				}
				var checkpoint struct {
					Domain workerapp.TurnSettlementDomainProof `json:"domain"`
				}
				if err := json.Unmarshal(settlement.PhaseState, &checkpoint); err != nil {
					t.Fatal(err)
				}
				state = workerapp.ResidentRuntimeState{SessionRevision: checkpoint.Domain.SessionRevision, RunRevision: checkpoint.Domain.RunRevision, TurnRevision: checkpoint.Domain.TurnRevision, RuntimeRevision: checkpoint.Domain.RuntimeRevision}
				// Existing domain fixture uses workspace-local in its frozen proof.
				if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET workspace_id='workspace-local' WHERE worker_session_id=$1`, started.WorkerSessionID); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(ctx, `UPDATE tasks SET workspace_id='workspace-local' WHERE task_id='task-01'`); err != nil {
					t.Fatal(err)
				}
			} else {
				var active workerapp.ResidentRuntimeState
				started, active = startActiveRecoveryFixture(t, pool, base)
				var err error
				state, err = (workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: time.Now}).CompleteStage(ctx, workerapp.CompleteResidentStageCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, SessionTarget: domain.SessionInteractive, RunTarget: domain.RunCompleted, TurnTarget: domain.TurnCompleted, TerminalReason: "end_turn"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `INSERT INTO worker_user_commands(command_id,tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,command_type,content,expected_session_revision,expected_run_revision,expected_turn_revision,queue_sequence,status,idempotency_key,created_at,updated_at) VALUES('lock-queue','tenant-local','user-local',$1,$2,$3,'queue','Continue',1,1,1,1,'accepted','lock-queue',$4,$4)`, started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID, time.Now()); err != nil {
				t.Fatal(err)
			}
			record := workerapp.ContinueQueuedTurnRecord{Command: workerapp.ContinueQueuedTurnCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: started.WorkerSessionID, PreviousWorkerRunID: started.WorkerRunID, PreviousAgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, QueueCommandID: "lock-queue", ExpectedSessionRevision: state.SessionRevision, ExpectedRunRevision: state.RunRevision, ExpectedTurnRevision: state.TurnRevision, ExpectedRuntimeRevision: state.RuntimeRevision}, WorkerRunID: "lock-successor", AgentTurnID: "lock-successor-turn", ScheduledCommandID: "lock-successor-deadline", EventID: "lock-successor-event", Now: time.Now()}
			queuePool := lockOrderPool(t, pool, "settlement-queue-lock-test")
			queueStore := workerpostgres.NewQueuedTurnStore(queuePool)
			queue := func() error { _, err := queueStore.ContinueQueuedTurn(ctx, record); return err }
			lossRun, lossTurn := started.WorkerRunID, started.AgentTurnID
			if replay {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(settlement.PhaseState, &fields); err != nil {
					t.Fatal(err)
				}
				fields["continuation_plan"], _ = json.Marshal(map[string]any{"action": "create_queued_turn", "queue_command_id": "lock-queue", "expected_session_revision": state.SessionRevision, "expected_run_revision": state.RunRevision, "expected_turn_revision": state.TurnRevision, "expected_runtime_revision": state.RuntimeRevision})
				encoded, _ := json.Marshal(fields)
				var err error
				settlement, err = (workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: time.Now}).FreezeCheckpoint(ctx, workerapp.FreezeTurnSettlementCheckpointCommand{SettlementID: settlement.SettlementID, ExpectedRevision: settlement.Revision, LeaseOwnerID: settlement.LeaseOwnerID, LeaseFence: settlement.LeaseFence, Checkpoint: typedTurnSettlementCheckpoint(t, encoded)})
				if err != nil {
					t.Fatal(err)
				}
				queue = func() error { _, err := queueStore.ContinueSettlementQueuedTurn(ctx, settlement, record); return err }
				if err := queue(); err != nil {
					t.Fatalf("create successor before response loss: %v", err)
				}
				lossRun, lossTurn = record.WorkerRunID, record.AgentTurnID
			}
			loss := workerapp.RuntimeLossRecord{Command: workerapp.ObserveRuntimeLossCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: lossRun, AgentTurnID: lossTurn, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, IdempotencyKey: "lock-runtime-loss"}, EventID: "lock-loss-event", TaskTransitionID: "lock-loss-transition", FenceStatus: "confirmed", Now: time.Now()}
			if err := pool.QueryRow(ctx, `SELECT s.session_revision,r.revision,t.revision,ri.revision,ri.container_id FROM worker_sessions s JOIN worker_runs r ON r.worker_run_id=$2 JOIN agent_turns t ON t.agent_turn_id=$3 JOIN runtime_instances ri ON ri.runtime_instance_id=$4 WHERE s.worker_session_id=$1`, started.WorkerSessionID, lossRun, lossTurn, started.RuntimeInstanceID).Scan(&loss.Command.ExpectedSessionRevision, &loss.Command.ExpectedRunRevision, &loss.Command.ExpectedTurnRevision, &loss.Command.ExpectedRuntimeRevision, &loss.Command.ExpectedContainerID); err != nil {
				t.Fatal(err)
			}
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(ctx, `SELECT command_id FROM worker_user_commands WHERE command_id='lock-queue' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			queueResult := make(chan error, 1)
			go func() { queueResult <- queue() }()
			waitLockOrderQuery(t, pool, "settlement-queue-lock-test", "worker_user_commands")
			lossPool := lockOrderPool(t, pool, "settlement-loss-lock-test")
			lossResult := make(chan error, 1)
			go func() {
				_, err := workerpostgres.NewRuntimeLossStore(lossPool).RecordRuntimeLoss(ctx, loss)
				lossResult <- err
			}()
			waitLockOrderQuery(t, pool, "settlement-loss-lock-test", "tasks")
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			queueErr, lossErr := <-queueResult, <-lossResult
			for _, err := range []error{queueErr, lossErr} {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
					t.Fatalf("queue/RuntimeLoss deadlock: queue=%v loss=%v", queueErr, lossErr)
				}
			}
			if queueErr != nil {
				t.Fatalf("queue or frozen replay failed: %v; loss=%v", queueErr, lossErr)
			}
			if !replay && !errors.Is(lossErr, platformpostgres.ErrNotFound) && !errors.Is(lossErr, platformpostgres.ErrRevisionConflict) {
				t.Fatalf("stale predecessor loss=%v", lossErr)
			}
			if replay && lossErr != nil {
				t.Fatalf("successor loss failed: %v", lossErr)
			}
		})
	}
}

func lockOrderPool(t *testing.T, pool *pgxpool.Pool, name string) *pgxpool.Pool {
	t.Helper()
	cfg := pool.Config()
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
func waitLockOrderQuery(t *testing.T, pool *pgxpool.Pool, name, queryPart string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		var query string
		err := pool.QueryRow(context.Background(), `SELECT query FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' LIMIT 1`, name).Scan(&query)
		if err == nil && strings.Contains(query, queryPart) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not wait on %s: query=%s err=%v", name, queryPart, query, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
