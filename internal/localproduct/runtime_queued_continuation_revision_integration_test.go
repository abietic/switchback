//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"testing"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func newQueuedContinuationRevisionFixture(t *testing.T) (*supplementFixture, observationFixtureTurn, string, string, workerapp.ContinueQueuedTurnCommand) {
	t.Helper()
	frozen := make(chan workerapp.TurnSettlement, 1)
	release := make(chan struct{})
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.runtime.heartbeatStart = make(chan struct{})
		f.settlements.afterFreeze = func(ctx context.Context, s workerapp.TurnSettlement) error {
			var cp runtimeTurnSettlementCheckpoint
			if err := json.Unmarshal(s.PhaseState, &cp); err != nil {
				return err
			}
			if s.Phase == workerapp.TurnSettlementAccepted && cp.EvidenceInput != nil {
				frozen <- s
				select {
				case <-release:
					return context.Canceled
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}
	})
	taskID := createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "continuation-revision", "Deliver report then continue")
	turn, callback := f.next()
	f.send(callback)
	queueID := f.queue(turn)
	f.finish(turn, callback)
	supplementReceive(t, f.ctx, frozen)
	f.app.runtime.startupReady.Store(false)
	close(release)
	live := f.app.runtime.runtimes[string(turn.start.Envelope.RuntimeInstanceID)]
	live.mu.Lock()
	live.mu.Unlock()
	f.settlements.afterFreeze = nil
	f.startPolicy.deny.Store(true)
	_, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
	if !errors.Is(err, errRuntimeStartCapacityUnavailable) {
		t.Fatalf("expected capacity boundary: %v", err)
	}
	s, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
	supplementMust(t, err)
	var cp runtimeTurnSettlementCheckpoint
	supplementMust(t, json.Unmarshal(s.PhaseState, &cp))
	p, i := cp.ContinuationPlan, cp.EvidenceInput
	if p.ExpectedSessionRevision == p.ExpectedRuntimeRevision {
		t.Fatal("fixture must exercise different Session and Runtime revisions")
	}
	command := workerapp.ContinueQueuedTurnCommand{TenantID: i.TenantID, UserID: i.UserID, WorkerSessionID: i.WorkerSessionID, PreviousWorkerRunID: i.WorkerRunID, PreviousAgentTurnID: i.AgentTurnID, RuntimeInstanceID: i.RuntimeInstanceID, RuntimeGeneration: i.RuntimeGeneration, QueueCommandID: p.QueueCommandID, ExpectedSessionRevision: p.ExpectedSessionRevision, ExpectedRunRevision: p.ExpectedRunRevision, ExpectedTurnRevision: p.ExpectedTurnRevision, ExpectedRuntimeRevision: p.ExpectedRuntimeRevision}
	return f, turn, queueID, taskID, command
}

func TestProductionQueuedContinuationUsesIndependentAggregateRevisions(t *testing.T) {
	f, turn, queueID, taskID, command := newQueuedContinuationRevisionFixture(t)
	staleSession := command
	staleSession.ExpectedSessionRevision--
	if _, err := f.app.runtime.queued.Execute(f.ctx, staleSession); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("stale Session revision accepted: %v", err)
	}
	staleRuntime := command
	staleRuntime.ExpectedRuntimeRevision--
	if _, err := f.app.runtime.queued.Execute(f.ctx, staleRuntime); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("stale Runtime revision accepted: %v", err)
	}
	f.startPolicy.deny.Store(false)
	if _, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx); err != nil {
		s, loadErr := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
		supplementMust(t, loadErr)
		var failed runtimeTurnSettlementCheckpoint
		supplementMust(t, json.Unmarshal(s.PhaseState, &failed))
		t.Logf("failed phase=%s domain=%+v continuation=%+v", s.Phase, failed.Domain, failed.ContinuationPlan)
		t.Logf("actual graph=%s", f.jsonRow(`SELECT jsonb_build_object('session_revision',s.session_revision,'session_status',s.status,'active_run',s.active_worker_run_id,'run_revision',r.revision,'run_status',r.status,'turn_revision',a.revision,'runtime_revision',i.revision,'task_status',task.status) FROM worker_sessions s JOIN worker_runs r USING(worker_session_id) JOIN agent_turns a USING(worker_run_id) JOIN runtime_instances i ON i.runtime_instance_id=s.active_runtime_instance_id JOIN tasks task ON task.task_id=r.task_id WHERE a.agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)))
		t.Fatalf("accepted queued prompt could not continue after terminal settlement: %v", err)
	}
	continued, err := f.app.runtime.queued.Execute(f.ctx, command)
	supplementMust(t, err)
	replayed, err := f.app.runtime.queued.Execute(f.ctx, command)
	supplementMust(t, err)
	if continued != replayed {
		t.Fatal("queued prompt replay changed continuation identity")
	}
	// The continuation has already committed Task revision 5; actions that
	// observed its delivered predecessor at revision 4 must lose that race.
	for _, action := range []string{"accept", "cancel"} {
		response := guardRequest(f.app, "/api/v1/tasks/"+taskID+"/"+action, map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "stale-after-queue-" + action, "expected_task_revision": 4, "message": "Stale Task action"})
		if response.Code != http.StatusConflict {
			t.Fatalf("stale %s changed committed continuation: %d %s", action, response.Code, response.Body.String())
		}
	}
	supplementMust(t, f.app.runtime.executeNextDeliveredTurn(f.ctx))
	next, _ := f.next()
	f.assertContinuation(turn, next, queueID)
	supplementMust(t, f.app.runtime.executeNextDeliveredTurn(f.ctx))
	for n := 0; n < 3; n++ {
		_, err := f.app.outbox.RunOnce(f.ctx)
		supplementMust(t, err)
	}
	if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"running"`)) {
		t.Fatalf("public Task did not follow continuation: %s", view)
	}
	if view := guardView(t, f.app, "/api/v1/views/worker_detail/"+command.WorkerSessionID); !bytes.Contains(view, []byte(`"report"`)) || !bytes.Contains(view, []byte(continued.WorkerRunID)) {
		t.Fatalf("previous report missing from public Worker: %s", view)
	}
	if n := f.count(`SELECT count(*) FROM runtime_control_messages WHERE direction='command' AND message_id=$1`, "start-"+continued.AgentTurnID); n != 1 {
		t.Fatalf("queued prompt starts=%d", n)
	}
	if n := f.count(`SELECT count(*) FROM scheduled_commands WHERE target_id=$1 AND command_type='agent_turn_deadline' AND due_at=(SELECT started_at+interval '24 hours' FROM agent_turns WHERE agent_turn_id=$1)`, continued.AgentTurnID); n != 1 {
		t.Fatalf("queued prompt fixed deadlines=%d", n)
	}
	if n := f.count(`SELECT count(*) FROM task_transitions WHERE task_id=$1 AND idempotency_key=$2 AND from_state='delivered' AND to_state='running'`, taskID, "queued-turn/"+queueID); n != 1 {
		t.Fatalf("queued Task transitions=%d", n)
	}
	if n := f.count(`SELECT count(*) FROM outbox_events WHERE stream_id=$1 AND event_type='TaskContinuationStarted.v1' AND payload->>'queue_command_id'=$2`, "task/"+taskID, queueID); n != 1 {
		t.Fatalf("queued Task owner events=%d", n)
	}
	var deliveredSequence, continuedSequence uint64
	supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT (SELECT sequence FROM outbox_events WHERE stream_id=$1 AND event_type='TaskStageDelivered.v1'),sequence FROM outbox_events WHERE stream_id=$1 AND event_type='TaskContinuationStarted.v1'`, "task/"+taskID).Scan(&deliveredSequence, &continuedSequence))
	if continuedSequence != deliveredSequence+1 {
		t.Fatalf("Task owner sequence delivered=%d continuation=%d", deliveredSequence, continuedSequence)
	}
	t.Logf("Task owner sequence delivered=%d continuation=%d; one queued identity/start/deadline/transition/event", deliveredSequence, continuedSequence)
}

func TestProductionQueuedContinuationCompetesWithExactTaskAction(t *testing.T) {
	for _, action := range []string{"accept", "cancel"} {
		t.Run(action, func(t *testing.T) {
			f, turn, queueID, taskID, _ := newQueuedContinuationRevisionFixture(t)
			// Read-only Task row lock holds both real commands behind a shared
			// release barrier; no state is written by fixture orchestration.
			lock, err := f.pool.Begin(f.ctx)
			supplementMust(t, err)
			defer lock.Rollback(f.ctx)
			var revision uint64
			supplementMust(t, lock.QueryRow(f.ctx, `SELECT revision FROM tasks WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&revision))
			begin := make(chan struct{})
			continuation := make(chan error, 1)
			actionResult := make(chan *httptest.ResponseRecorder, 1)
			f.startPolicy.deny.Store(false)
			go func() {
				<-begin
				_, _, err := f.app.runtime.reconcileOnePendingTurnSettlement(f.ctx)
				continuation <- err
			}()
			go func() {
				<-begin
				actionResult <- guardRequest(f.app, "/api/v1/tasks/"+taskID+"/"+action, map[string]any{"schema_version": "gateway.task_command.v1", "idempotency_key": "race-" + action, "expected_task_revision": revision, "message": "Explicit Task action"})
			}()
			close(begin)
			// Prove both commands reached the real Task lock before releasing
			// it, including PostgreSQL's waiter-to-waiter lock dependency.
			for {
				var waiters int
				supplementMust(t, f.pool.QueryRow(f.ctx, `WITH RECURSIVE waiters AS (
 SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
 UNION SELECT activity.pid FROM pg_stat_activity activity
 JOIN waiters ON waiters.pid=ANY(pg_blocking_pids(activity.pid))
) SELECT count(*) FROM waiters`, lock.Conn().PgConn().PID()).Scan(&waiters))
				if waiters == 2 {
					break
				}
				goruntime.Gosched()
			}
			supplementMust(t, lock.Commit(f.ctx))
			continueErr := supplementReceive(t, f.ctx, continuation)
			response := supplementReceive(t, f.ctx, actionResult)
			if continueErr == nil {
				if response.Code != http.StatusConflict {
					t.Fatalf("continuation won but stale %s succeeded: %d %s", action, response.Code, response.Body.String())
				}
				if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != 1 {
					t.Fatalf("winning continuation Runs=%d", n)
				}
			} else {
				if !errors.Is(continueErr, platformpostgres.ErrRevisionConflict) {
					t.Fatalf("unexpected losing continuation: %v", continueErr)
				}
				if response.Code != http.StatusAccepted {
					t.Fatalf("%s won but rejected: %d %s", action, response.Code, response.Body.String())
				}
				if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != 0 {
					t.Fatalf("terminal action created %d continuation Runs", n)
				}
				if n := f.count(`SELECT count(*) FROM worker_user_commands WHERE command_id=$1 AND status='accepted'`, queueID); n != 1 {
					t.Fatal("rejected continuation consumed queue")
				}
			}
			for n := 0; n < 3; n++ {
				_, err := f.app.outbox.RunOnce(f.ctx)
				supplementMust(t, err)
			}
			wantStatus := "running"
			if continueErr != nil {
				wantStatus = "completed"
				if action == "cancel" {
					wantStatus = "cancelled"
				}
			}
			if view := guardView(t, f.app, "/api/v1/views/task_summary/"+taskID); !bytes.Contains(view, []byte(`"status":"`+wantStatus+`"`)) {
				t.Fatalf("public winning Task status: %s", view)
			}
			t.Logf("%s race continuation_won=%t action_http=%d public_status=%s", action, continueErr == nil, response.Code, wantStatus)
		})
	}
}
