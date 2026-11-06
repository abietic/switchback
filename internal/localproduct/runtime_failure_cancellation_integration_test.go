//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestHealthyLegacyCancellationSettlementFreezesReceipt(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Now().UTC()
	seedBackendDeadlineClaimGraph(t, pool, now.Add(time.Hour))
	store := taskpostgres.NewStore(pool)
	sessions := workerpostgres.SessionReader{Pool: pool}
	runtimes := workerpostgres.RuntimeReader{Pool: pool}
	service := taskapp.TaskActionService{Store: store, IDs: randomIDs{}, Now: func() time.Time { return now }}
	actor := taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}
	cancelled, err := service.Cancel(ctx, taskapp.CancelTaskCommand{
		Actor: actor, TaskID: "task-deadline-claim", ExpectedTaskRevision: 3,
		IdempotencyKey: "healthy-legacy-cancel",
	})
	if err != nil || cancelled.Status != "cancelled" || cancelled.Revision != 4 || cancelled.EventID == "" {
		t.Fatalf("healthy initiator cancellation: result=%+v err=%v", cancelled, err)
	}
	bound, err := sessions.ReadWorkerSession(ctx, "session-deadline-claim")
	if err != nil || bound.Status != "running" || bound.Revision != 4 ||
		bound.BoundTaskID != cancelled.TaskID || bound.ActiveWorkerRunID != "run-deadline-claim" {
		t.Fatalf("accepted cancellation released live binding: session=%+v err=%v", bound, err)
	}
	// No public command reader exists here; inspect the durable accepted interrupt
	// only as evidence of Cancel's output, never as a substitute for that operation.
	var interrupts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_user_commands
WHERE tenant_id='tenant-local' AND user_id='user-local'
  AND worker_session_id='session-deadline-claim' AND worker_run_id='run-deadline-claim'
  AND agent_turn_id='turn-deadline-claim' AND command_type='interrupt' AND status='accepted'
  AND expected_session_revision=4 AND expected_run_revision=2 AND expected_turn_revision=2
  AND idempotency_key='task-cancel-control/healthy-legacy-cancel'
  AND outcome->>'source'='task_cancel'`).Scan(&interrupts); err != nil || interrupts != 1 {
		t.Fatalf("initiator cancellation did not accept exact interrupt: count=%d err=%v", interrupts, err)
	}
	// This lower Worker-owner fixture confirms the accepted interrupt. It does
	// not exercise physical ACP cancellation or durable Turn settlement leases.
	stage, err := (workerapp.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now },
	}).CompleteStage(ctx, workerapp.CompleteResidentStageCommand{
		TenantID: actor.TenantID, WorkerSessionID: bound.WorkerSessionID,
		WorkerRunID: "run-deadline-claim", AgentTurnID: "turn-deadline-claim",
		RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1,
		ExpectedSessionRevision: 4, ExpectedRunRevision: 2, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 5,
		SessionTarget: workerdomain.SessionInteractive, RunTarget: workerdomain.RunCancelled,
		TurnTarget: workerdomain.TurnCancelled, TerminalReason: "cancelled:task_cancel",
	})
	if err != nil || stage.SessionRevision != 5 || stage.RunRevision != 3 || stage.TurnRevision != 3 ||
		stage.RuntimeRevision != 5 || stage.SessionStatus != workerdomain.SessionInteractive ||
		stage.RunStatus != workerdomain.RunCancelled || stage.TurnStatus != workerdomain.TurnCancelled ||
		stage.RuntimeStatus != "active" {
		t.Fatalf("confirm accepted interrupt through resident Worker owner: stage=%+v err=%v", stage, err)
	}
	resident, err := sessions.ReadWorkerSession(ctx, bound.WorkerSessionID)
	if err != nil || resident.Revision != 5 || resident.BoundTaskID != cancelled.TaskID || resident.ActiveWorkerRunID != "" {
		t.Fatalf("terminal Run lost resident Task binding: session=%+v err=%v", resident, err)
	}
	command := taskapp.SettleCancelledTaskCommand{
		Actor: actor, TaskID: cancelled.TaskID, WorkerSessionID: bound.WorkerSessionID,
		WorkerRunID: "run-deadline-claim", ExpectedTaskRevision: 4, ExpectedSessionRevision: 5,
		IdempotencyKey: "healthy-legacy-settle",
	}
	first, err := service.SettleCancelled(ctx, command)
	if err != nil || first.TaskID != command.TaskID || first.WorkerSessionID != command.WorkerSessionID ||
		first.SessionRevision != 6 || first.EventID == "" {
		t.Fatalf("legacy cancellation settlement: result=%+v err=%v", first, err)
	}
	var originalPayload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM outbox_events
WHERE event_id=$1 AND event_type='TaskCancellationSettled.v1'
  AND producer_context='task-orchestration' AND aggregate_id=$2
  AND idempotency_key='TaskCancellationSettled.v1/healthy-legacy-settle'`, first.EventID, command.TaskID).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal(originalPayload, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt) != 3 || string(receipt["task_id"]) != `"task-deadline-claim"` ||
		string(receipt["task_revision"]) != "4" || string(receipt["session_revision"]) != "6" {
		t.Fatalf("healthy settlement did not emit the legacy receipt: %s", originalPayload)
	}
	if _, present := receipt["request_digest"]; present {
		t.Fatalf("legacy receipt contains request_digest: %s", originalPayload)
	}
	if _, present := receipt["expected_session_revision"]; present {
		t.Fatalf("legacy receipt contains expected_session_revision: %s", originalPayload)
	}
	released, err := sessions.ReadWorkerSession(ctx, command.WorkerSessionID)
	if err != nil || released.Status != "interactive" || released.Revision != 6 ||
		released.BoundTaskID != "" || released.ActiveWorkerRunID != "" {
		t.Fatalf("legacy settlement did not release binding: session=%+v err=%v", released, err)
	}
	// Trusted detector evidence advances the resident Session through an existing
	// recovery operation; no direct revision or active-pointer update is needed.
	opened, err := (workerapp.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: randomIDs{},
		Now: func() time.Time { return now },
	}).Open(ctx, workerapp.OpenRuntimeRecoveryCommand{
		TenantID: actor.TenantID, WorkerSessionID: command.WorkerSessionID,
		FailedRuntimeInstanceID: "runtime-deadline-claim", FailedGeneration: 1, FailedChannelEpoch: 7,
		Trigger: workerdomain.RecoveryTriggerContainerMissing, EvidenceRef: "runtime-inspection/runtime-deadline-claim",
		EvidenceAt: now, ExpectedSessionRevision: 6, ExpectedRuntimeRevision: 5,
		IdempotencyKey: "healthy-legacy-idle-recovery",
	})
	if err != nil || opened.SessionRevision != 7 || opened.PreviousSessionStatus != workerdomain.SessionInteractive ||
		opened.WorkerRunID != "" || opened.AgentTurnID != "" || opened.RunRevision != 0 || opened.TurnRevision != 0 {
		t.Fatalf("advance unbound resident Session legally: recovery=%+v err=%v", opened, err)
	}
	beforeSession, err := sessions.ReadWorkerSession(ctx, command.WorkerSessionID)
	if err != nil || beforeSession.Status != "recovering" || beforeSession.Revision != 7 ||
		beforeSession.BoundTaskID != "" || beforeSession.ActiveWorkerRunID != "" {
		t.Fatalf("recovery did not advance live Session: session=%+v err=%v", beforeSession, err)
	}
	beforeTask, err := store.ReadTask(ctx, command.TaskID)
	if err != nil || beforeTask.Status != "cancelled" || beforeTask.Revision != 4 {
		t.Fatalf("idle recovery changed cancelled Task: task=%+v err=%v", beforeTask, err)
	}
	beforeRuntime, err := runtimes.ReadRuntimeInstance(ctx, "runtime-deadline-claim")
	if err != nil {
		t.Fatal(err)
	}
	// Recovery contributes its own event before the replay baseline is captured.
	var beforeTransitions, beforeEvents int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_transitions),
  (SELECT count(*) FROM outbox_events)`).Scan(&beforeTransitions, &beforeEvents); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.SettleCancelled(ctx, command)
	if err != nil || replayed != first {
		t.Fatalf("live revision replaced frozen legacy receipt: first=%+v replay=%+v err=%v", first, replayed, err)
	}
	changedSession := command
	changedSession.ExpectedSessionRevision = 6
	if _, err := service.SettleCancelled(ctx, changedSession); !errors.Is(err, taskapp.ErrIdempotencyConflict) {
		t.Fatalf("changed expected Session revision must conflict with receipt: %v", err)
	}
	changedTask := command
	changedTask.ExpectedTaskRevision = 5
	if _, err := service.SettleCancelled(ctx, changedTask); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("changed expected Task revision must conflict: %v", err)
	}
	afterSession, err := sessions.ReadWorkerSession(ctx, command.WorkerSessionID)
	if err != nil || !reflect.DeepEqual(afterSession, beforeSession) {
		t.Fatalf("replay changed live Session: before=%+v after=%+v err=%v", beforeSession, afterSession, err)
	}
	afterTask, err := store.ReadTask(ctx, command.TaskID)
	if err != nil || !reflect.DeepEqual(afterTask, beforeTask) {
		t.Fatalf("replay changed cancelled Task: before=%+v after=%+v err=%v", beforeTask, afterTask, err)
	}
	afterRuntime, err := runtimes.ReadRuntimeInstance(ctx, "runtime-deadline-claim")
	if err != nil || !reflect.DeepEqual(afterRuntime, beforeRuntime) {
		t.Fatalf("replay changed resident Runtime: before=%+v after=%+v err=%v", beforeRuntime, afterRuntime, err)
	}
	var afterTransitions, afterEvents, settlements int
	var retainedPayload []byte
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_transitions),
  (SELECT count(*) FROM outbox_events),
  (SELECT count(*) FROM outbox_events WHERE event_type='TaskCancellationSettled.v1'),
  (SELECT payload FROM outbox_events WHERE event_id=$1)`, first.EventID).Scan(
		&afterTransitions, &afterEvents, &settlements, &retainedPayload,
	); err != nil || afterTransitions != beforeTransitions || afterEvents != beforeEvents ||
		settlements != 1 || !reflect.DeepEqual(retainedPayload, originalPayload) {
		t.Fatalf("replay changed durable evidence: transitions=%d/%d events=%d/%d settlements=%d payload=%s err=%v",
			afterTransitions, beforeTransitions, afterEvents, beforeEvents, settlements, retainedPayload, err)
	}
}

// The public cancellation seam starts after real Worker failure and Task-owner
// consumption; only external cleanup/control will be faked in later slices.
func TestRuntimeFailureCancellationCommitsBeforeCleanup(t *testing.T) {
	fixture := newRuntimeFailureFixture(t)
	ctx := context.Background()
	if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	command := taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-deadline-claim", ExpectedTaskRevision: 4, IdempotencyKey: "failure-cancel"}
	service := taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}
	result, err := service.Cancel(ctx, command)
	if err != nil || result.Status != "cancelled" {
		t.Fatalf("initiator cancellation after failed Turn: result=%+v err=%v", result, err)
	}
	var retained bool
	if err := fixture.pool.QueryRow(ctx, `SELECT status='waiting_user' AND bound_task_id='task-deadline-claim' AND active_worker_run_id='run-deadline-claim' AND NOT EXISTS(SELECT 1 FROM worker_user_commands WHERE command_type='interrupt') FROM worker_sessions WHERE worker_session_id='session-deadline-claim'`).Scan(&retained); err != nil || !retained {
		t.Fatalf("unproven cleanup released authority: retained=%t err=%v", retained, err)
	}
}

func TestRuntimeFailureCancellationDispatcherPreservesPendingCleanupAndResumes(t *testing.T) {
	for _, scenario := range []string{"cleanup-before-restart", "settle-response-loss"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			service := taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}
			fixture.dispatcher.Publisher = runtimeFailureCancellationPublisher{Pool: fixture.pool, Actions: service, Next: fixture.publisher}
			cancelled, err := service.Cancel(ctx, taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-deadline-claim", ExpectedTaskRevision: 4, IdempotencyKey: "failure-cancel"})
			if err != nil {
				t.Fatal(err)
			}
			// First delivers the existing TaskWaitingForUser event, then cancellation.
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.dispatcher.RunOnce(ctx); err == nil {
				t.Fatal("pending cleanup source was ACKed without a durable settlement")
			}
			var pending bool
			if err := fixture.pool.QueryRow(ctx, `SELECT published_at IS NULL AND lease_owner IS NULL FROM outbox_events WHERE event_id=$1`, cancelled.EventID).Scan(&pending); err != nil || !pending {
				t.Fatalf("source retry lost: %t %v", pending, err)
			}
			materializer := fixture.publisher.Next.(projectionpostgres.Materializer)
			snapshot, err := materializer.Views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, ViewType: projectionapp.ViewTaskSummary, ViewID: cancelled.TaskID})
			if err != nil {
				t.Fatal(err)
			}
			var detail map[string]any
			if err := json.Unmarshal(snapshot.Data, &detail); err != nil {
				t.Fatal(err)
			}
			if detail["status"] != "cancelled" {
				t.Fatalf("pending cleanup hid Task cancellation: %s", snapshot.Data)
			}
			assertRuntimeFailureCancellationView(t, materializer, false)
			for i := 0; i < 3; i++ {
				if _, err := fixture.dispatcher.RunOnce(ctx); err == nil {
					t.Fatal("pending duplicate was acknowledged")
				}
			}
			var attempts, receipts int
			if err := fixture.pool.QueryRow(ctx, `SELECT attempt_count,(SELECT count(*) FROM outbox_events WHERE event_type='TaskCancellationSettled.v1') FROM outbox_events WHERE event_id=$1`, cancelled.EventID).Scan(&attempts, &receipts); err != nil || attempts != 4 || receipts != 0 {
				t.Fatalf("pending duplicate loop was not bounded: attempts=%d receipts=%d err=%v", attempts, receipts, err)
			}
			cleanupRuntimeFailureFixture(t, fixture, nil)
			if scenario == "settle-response-loss" {
				// Commit through the real owner service, then lose its response before
				// the source ACK. The restarted composition must recover this receipt.
				if _, err := service.SettleCancelled(ctx, taskapp.SettleCancelledTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: cancelled.TaskID, WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", ExpectedTaskRevision: cancelled.Revision, ExpectedSessionRevision: 6, IdempotencyKey: "runtime-failure-cancel/" + cancelled.EventID}); err != nil {
					t.Fatal(err)
				}
			}
			fixture.pool = postgrestest.Restart(t, fixture.pool)
			service.Store = taskpostgres.NewStore(fixture.pool)
			fixture.dispatcher.Pool = fixture.pool
			projectionStore := projectionpostgres.NewStore(fixture.pool)
			materializer.Pool = fixture.pool
			materializer.Projector = projectionapp.Projector{Store: projectionStore}
			materializer.Views, err = projectionapp.NewViewContinuityReader(projectionStore, projectionapp.ViewContinuityReaderOptions{Now: time.Now, ReplayLimit: 128})
			if err != nil {
				t.Fatal(err)
			}
			fixture.publisher.Next = materializer
			fixture.dispatcher.Publisher = runtimeFailureCancellationPublisher{Pool: fixture.pool, Actions: service, Next: fixture.publisher}
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatalf("restart did not reconcile original cancellation: %v", err)
			}
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatalf("publish Task-owned settlement: %v", err)
			}
			assertRuntimeFailureCancellationView(t, materializer, true)
			var released bool
			if err := fixture.pool.QueryRow(ctx, `SELECT bound_task_id IS NULL AND active_worker_run_id IS NULL AND status='waiting_user' FROM worker_sessions WHERE worker_session_id='session-deadline-claim'`).Scan(&released); err != nil || !released {
				t.Fatalf("cleanup proven cancellation not released: %t %v", released, err)
			}
			if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='TaskCancellationSettled.v1'`).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatalf("same source generated extra receipts: %d %v", receipts, err)
			}
		})
	}
}

func assertRuntimeFailureCancellationView(t *testing.T, materializer projectionpostgres.Materializer, released bool) {
	t.Helper()
	ctx := context.Background()
	query := projectionapp.SnapshotQuery{Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-deadline-claim"}
	view, err := materializer.Views.ReadSnapshot(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Status      string `json:"status"`
		BoundTaskID string `json:"bound_task_id"`
		ActiveRunID string `json:"active_worker_run_id"`
		RunStatus   string `json:"run_status"`
		TurnStatus  string `json:"turn_status"`
	}
	if err := json.Unmarshal(view.Data, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Status != "waiting_user" || detail.TurnStatus != "failed" {
		t.Fatalf("Worker failure view changed identity: %s", view.Data)
	}
	if released {
		if detail.BoundTaskID != "" || detail.ActiveRunID != "" || detail.RunStatus != "cancelled" {
			t.Fatalf("released Worker view retained binding: %s", view.Data)
		}
	} else if detail.BoundTaskID != "task-deadline-claim" || detail.ActiveRunID != "run-deadline-claim" || detail.RunStatus != "waiting_user" {
		t.Fatalf("pending cleanup view released binding: %s", view.Data)
	}
	for _, action := range view.AvailableActions {
		if action.Action == "worker.follow_up" || action.Action == "worker.queue" || action.Action == "worker.interrupt" {
			t.Fatalf("dead Runtime offers execution: %+v", action)
		}
	}
	reload, err := materializer.Views.ReadSnapshot(ctx, query)
	if err != nil || !reflect.DeepEqual(view.Data, reload.Data) || !reflect.DeepEqual(view.RelatedRefs, reload.RelatedRefs) || view.ViewCursor != reload.ViewCursor {
		t.Fatalf("reload changed exact Worker view: %v", err)
	}
}

func TestRuntimeFailureCancellationSettlementRejectsUnsafeRequests(t *testing.T) {
	for _, scenario := range []string{"actor", "tenant", "task", "session", "run", "task-revision", "session-revision", "cleanup-container", "generation", "outbox-rollback", "ordinary-db-error", "concurrent-replay", "frozen-replay"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			service := taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}
			actor := taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}
			cancelled, err := service.Cancel(ctx, taskapp.CancelTaskCommand{Actor: actor, TaskID: "task-deadline-claim", ExpectedTaskRevision: 4, IdempotencyKey: "failure-cancel"})
			if err != nil {
				t.Fatal(err)
			}
			cleanupRuntimeFailureFixture(t, fixture, nil)
			command := taskapp.SettleCancelledTaskCommand{Actor: actor, TaskID: cancelled.TaskID, WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", ExpectedTaskRevision: 5, ExpectedSessionRevision: 6, IdempotencyKey: "failure-settle"}
			sql := ""
			switch scenario {
			case "actor":
				command.Actor.UserID = "other"
			case "tenant":
				command.Actor.TenantID = "other"
			case "task":
				command.TaskID = "other"
			case "session":
				command.WorkerSessionID = "other"
			case "run":
				command.WorkerRunID = "other"
			case "task-revision":
				command.ExpectedTaskRevision++
			case "session-revision":
				command.ExpectedSessionRevision++
			case "cleanup-container":
				sql = `UPDATE scheduled_commands SET payload=jsonb_set(payload,'{expected_container_id}','"wrong-container"') WHERE command_type='runtime_cleanup_reconcile'`
			case "generation":
				sql = `UPDATE worker_sessions SET runtime_generation=2 WHERE worker_session_id='session-deadline-claim'`
			case "ordinary-db-error":
				sql = `ALTER TABLE tasks RENAME TO unavailable_tasks`
			case "outbox-rollback":
				sql = `CREATE FUNCTION reject_cancel_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='TaskCancellationSettled.v1' THEN RAISE EXCEPTION 'controlled settlement outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cancel_settlement BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_cancel_settlement()`
			}
			if sql != "" {
				if _, err := fixture.pool.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "concurrent-replay" {
				var wg sync.WaitGroup
				results := make(chan taskapp.SettledCancelledTask, 2)
				errs := make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); r, e := service.SettleCancelled(ctx, command); results <- r; errs <- e }()
				}
				wg.Wait()
				first, second := <-results, <-results
				if e1, e2 := <-errs, <-errs; e1 != nil || e2 != nil || first != second {
					t.Fatalf("concurrent receipt diverged: %+v %+v %v %v", first, second, e1, e2)
				}
				return
			}
			first, err := service.SettleCancelled(ctx, command)
			if scenario == "frozen-replay" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.pool.Exec(ctx, `UPDATE worker_sessions SET session_revision=session_revision+1 WHERE worker_session_id='session-deadline-claim'`); err != nil {
					t.Fatal(err)
				}
				second, err := service.SettleCancelled(ctx, command)
				if err != nil || first != second {
					t.Fatalf("mutable Session replaced frozen result: %+v %+v %v", first, second, err)
				}
				for _, field := range []string{"actor", "tenant", "task", "session", "run"} {
					changed := command
					switch field {
					case "actor":
						changed.Actor.UserID = "other"
					case "tenant":
						changed.Actor.TenantID = "other"
					case "task":
						changed.TaskID = "other"
					case "session":
						changed.WorkerSessionID = "other"
					case "run":
						changed.WorkerRunID = "other"
					}
					if _, err := service.SettleCancelled(ctx, changed); err == nil {
						t.Fatalf("changed %s replay accepted", field)
					}
				}
				command.ExpectedSessionRevision++
				if _, err := service.SettleCancelled(ctx, command); err == nil {
					t.Fatal("changed replay request accepted")
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe settlement accepted")
			}
			if scenario == "ordinary-db-error" {
				if errors.Is(err, platformpostgres.ErrNotFound) || errors.Is(err, platformpostgres.ErrRevisionConflict) {
					t.Fatalf("database error misclassified: %v", err)
				}
				return
			}
			var retained bool
			if err := fixture.pool.QueryRow(ctx, `SELECT bound_task_id='task-deadline-claim' AND active_worker_run_id='run-deadline-claim' AND (SELECT status='waiting_user' FROM worker_runs WHERE worker_run_id='run-deadline-claim') AND NOT EXISTS(SELECT 1 FROM outbox_events WHERE event_type='TaskCancellationSettled.v1') FROM worker_sessions WHERE worker_session_id='session-deadline-claim'`).Scan(&retained); err != nil || !retained {
				t.Fatalf("rejected settlement partially wrote: %t %v", retained, err)
			}
		})
	}
}

func TestRuntimeFailureCancellationReplayRevalidatesInitiatorAndRequest(t *testing.T) {
	for _, scenario := range []string{"restart-replay", "actor", "tenant", "revision", "operation-key", "ordinary-read-error"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			command := taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-deadline-claim", ExpectedTaskRevision: 4, IdempotencyKey: "failure-cancel"}
			service := taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}
			first, err := service.Cancel(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "restart-replay":
				service.Store = taskpostgres.NewStore(postgrestest.Restart(t, fixture.pool))
			case "actor":
				command.Actor.UserID = "other-user"
			case "tenant":
				command.Actor.TenantID = "other-tenant"
			case "revision":
				command.ExpectedTaskRevision++
			case "operation-key":
				command.IdempotencyKey = "runtime-failure/" + fixture.event.EventID
			case "ordinary-read-error":
				if _, err := fixture.pool.Exec(ctx, `ALTER TABLE tasks RENAME TO unavailable_tasks`); err != nil {
					t.Fatal(err)
				}
			}
			second, err := service.Cancel(ctx, command)
			if scenario == "restart-replay" {
				if err != nil || first != second {
					t.Fatalf("lost response replay changed result: first=%+v second=%+v err=%v", first, second, err)
				}
				return
			}
			if err == nil {
				t.Fatal("changed actor or request was accepted")
			}
			if scenario == "ordinary-read-error" && (errors.Is(err, platformpostgres.ErrNotFound) || errors.Is(err, platformpostgres.ErrRevisionConflict)) {
				t.Fatalf("ordinary database error misclassified: %v", err)
			}
		})
	}
}

func TestRuntimeFailureCancellationReleasesOnlyAfterConfirmedCleanup(t *testing.T) {
	fixture := newRuntimeFailureFixture(t)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO worker_session_continuity_quotas(tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at) VALUES('tenant-local','user-local',10,1,1,'continuity-v1',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	service := taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}
	actor := taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}
	cancelled, err := service.Cancel(ctx, taskapp.CancelTaskCommand{Actor: actor, TaskID: "task-deadline-claim", ExpectedTaskRevision: 4, IdempotencyKey: "failure-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	command := taskapp.SettleCancelledTaskCommand{Actor: actor, TaskID: cancelled.TaskID, WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", ExpectedTaskRevision: cancelled.Revision, ExpectedSessionRevision: 5, IdempotencyKey: "failure-settle"}
	if _, err := service.SettleCancelled(ctx, command); err == nil {
		t.Fatal("unproven cleanup released failed Run")
	}
	cleanupRuntimeFailureFixture(t, fixture, nil)
	command.ExpectedSessionRevision = 6
	settled, err := service.SettleCancelled(ctx, command)
	if err != nil {
		t.Fatalf("confirmed cleanup did not release cancelled Task binding: %v", err)
	}
	var released bool
	if err := fixture.pool.QueryRow(ctx, `SELECT session.status='waiting_user' AND session.bound_task_id IS NULL AND session.active_worker_run_id IS NULL AND run.status='cancelled' AND turn.status='failed' AND turn.revision=3 AND turn.terminal_reason='runtime_recovery_failed' FROM worker_sessions session JOIN worker_runs run ON run.worker_session_id=session.worker_session_id JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id WHERE session.worker_session_id=$1`, command.WorkerSessionID).Scan(&released); err != nil || !released {
		t.Fatalf("unsafe release: %t %v", released, err)
	}
	var reserved int
	if err := fixture.pool.QueryRow(ctx, `SELECT reserved_count FROM worker_session_continuity_quotas WHERE tenant_id='tenant-local' AND user_id='user-local'`).Scan(&reserved); err != nil || reserved != 1 {
		t.Fatalf("Task cancellation released continuity quota: %d %v", reserved, err)
	}
	_, err = (taskapp.SessionDispatchService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now, PermitTTL: time.Minute}).IssueTaskPermit(ctx, taskapp.IssueTaskPermitCommand{TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: command.WorkerSessionID, RuntimeGeneration: 1, IdempotencyKey: "no-permit-after-failure"})
	if err == nil {
		t.Fatal("failed Session issued a new Task permit")
	}
	service.Store = taskpostgres.NewStore(postgrestest.Restart(t, fixture.pool))
	replay, err := service.SettleCancelled(ctx, command)
	if err != nil || settled != replay {
		t.Fatalf("settlement response loss: first=%+v replay=%+v err=%v", settled, replay, err)
	}
}

func TestRuntimeFailureCancellationIdleFailureRetainsBinding(t *testing.T) {
	fixture := newRuntimeFailureFixture(t, "idle-delivered-task")
	ctx := context.Background()
	service := taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}
	cancelled, err := service.Cancel(ctx, taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-deadline-claim", ExpectedTaskRevision: 3, IdempotencyKey: "idle-failure-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	var retained bool
	if err := fixture.pool.QueryRow(ctx, `SELECT status='waiting_user' AND bound_task_id='task-deadline-claim' AND active_worker_run_id IS NULL FROM worker_sessions WHERE worker_session_id='session-deadline-claim'`).Scan(&retained); err != nil || !retained {
		t.Fatalf("idle failure cancellation made a dead Runtime interactive: retained=%t err=%v", retained, err)
	}
	cleanupRuntimeFailureFixture(t, fixture, nil)
	_, err = service.SettleCancelled(ctx, taskapp.SettleCancelledTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: cancelled.TaskID, WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", ExpectedTaskRevision: cancelled.Revision, ExpectedSessionRevision: 6, IdempotencyKey: "idle-failure-settle"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT session.status='waiting_user' AND session.bound_task_id IS NULL AND run.status='completed' AND run.revision=2 AND turn.status='completed' AND turn.revision=2 AND incident.worker_run_id IS NULL AND incident.agent_turn_id IS NULL FROM worker_sessions session JOIN worker_runs run ON run.worker_session_id=session.worker_session_id JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id JOIN runtime_recovery_incidents incident ON incident.worker_session_id=session.worker_session_id WHERE session.worker_session_id='session-deadline-claim'`).Scan(&retained); err != nil || !retained {
		t.Fatalf("idle cleanup invented Run/Turn outcome: %t %v", retained, err)
	}
}

func TestRuntimeFailureCancellationQueuedTaskDoesNotOwnFailedSlot(t *testing.T) {
	fixture := newRuntimeFailureFixture(t)
	ctx := context.Background()
	if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Seed only a second admitted Task, then use the real queue/cancel APIs.
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO task_creation_intents SELECT (jsonb_populate_record(NULL::task_creation_intents,to_jsonb(intent)||'{"intent_id":"queued-intent","consumed_by_task_id":"queued-task"}'::jsonb)).* FROM task_creation_intents intent WHERE intent_id='intent-deadline-claim'; INSERT INTO tasks SELECT (jsonb_populate_record(NULL::tasks,to_jsonb(task)||'{"task_id":"queued-task","creation_intent_id":"queued-intent","status":"queued","worker_session_id":null,"current_worker_run_id":null,"revision":1,"waiting_reason":null}'::jsonb)).* FROM tasks task WHERE task_id='task-deadline-claim'`); err != nil {
		t.Fatal(err)
	}
	queued, err := (taskapp.SessionDispatchService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now, PermitTTL: time.Minute}).EnqueueTask(ctx, taskapp.EnqueueTaskCommand{TenantID: "tenant-local", UserID: "user-local", TaskID: "queued-task", WorkerSessionID: "session-deadline-claim", ExpectedTaskRevision: 1, ExpectedSessionRevision: 5, IdempotencyKey: "queue-second-task"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (taskapp.TaskActionService{Store: fixture.store, IDs: randomIDs{}, Now: time.Now}).Cancel(ctx, taskapp.CancelTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "queued-task", ExpectedTaskRevision: queued.TaskRevision, IdempotencyKey: "cancel-queued-task"})
	if err != nil || result.Status != "cancelled" {
		t.Fatalf("queued Task cancellation borrowed another Task slot: %+v %v", result, err)
	}
	var untouched bool
	if err := fixture.pool.QueryRow(ctx, `SELECT bound_task_id='task-deadline-claim' AND active_worker_run_id='run-deadline-claim' AND status='waiting_user' AND NOT EXISTS(SELECT 1 FROM worker_user_commands WHERE command_type='interrupt') FROM worker_sessions WHERE worker_session_id='session-deadline-claim'`).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("queued cancellation touched running owner: %t %v", untouched, err)
	}
}
