//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerfake "github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeFailureOutboxPausesTaskBeforeProjection(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedBackendDeadlineClaimGraph(t, pool, now.Add(time.Hour))
	store := workerpostgres.NewRuntimeRecoveryStore(pool)
	observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapp.RuntimeRecoveryService{
		Store: store, IDs: randomIDs{}, Now: func() time.Time { return now },
	}, false)
	state, recorded, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID: "session-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim",
		RuntimeGeneration: 1, ChannelEpoch: 7, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/fixture", EvidenceAt: now,
		Inspection: &workerapp.RuntimeInspection{Exists: true, ContainerID: "container-deadline-claim", Generation: 1, ObservedAt: now},
	})
	if err != nil || !recorded || state.EventID == "" {
		t.Fatalf("persist actual Worker failure: state=%+v recorded=%t err=%v", state, recorded, err)
	}
	projectionStore := projectionpostgres.NewStore(pool)
	views, err := projectionapp.NewViewContinuityReader(projectionStore, projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now }, ReplayLimit: 128})
	if err != nil {
		t.Fatal(err)
	}
	materializer := projectionpostgres.Materializer{Pool: pool, Projector: projectionapp.Projector{Store: projectionStore}, Views: views, UserID: "user-local", Now: func() time.Time { return now }}
	taskStore := taskpostgres.NewStore(pool)
	publisher := runtimeFailureEventPublisher{Consumer: taskapp.RuntimeFailureConsumer{Reader: taskStore, Pause: taskapp.TaskPauseService{Store: taskStore, IDs: randomIDs{}, Now: func() time.Time { return now }}}, Next: materializer}
	dispatcher := platformpostgres.OutboxDispatcher{Pool: pool, Publisher: publisher,
		LeaseOwner: "runtime-failure-owner-test", LeaseDuration: time.Minute, BatchSize: 16, Now: func() time.Time { return now }}
	if _, err := dispatcher.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var transitions, events int
	if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM task_transitions WHERE task_id=tasks.task_id AND to_state='waiting_user'),(SELECT count(*) FROM outbox_events WHERE aggregate_id=tasks.task_id AND event_type='TaskWaitingForUser.v1') FROM tasks WHERE task_id='task-deadline-claim'`).Scan(&status, &transitions, &events); err != nil {
		t.Fatal(err)
	}
	if status != "waiting_user" || transitions != 1 || events != 1 {
		t.Fatalf("Worker failure did not reach Task owner: status=%s transitions=%d owner_events=%d", status, transitions, events)
	}
	query := projectionapp.SnapshotQuery{Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, ViewType: projectionapp.ViewTaskSummary, ViewID: "task-deadline-claim"}
	snapshot, err := views.ReadSnapshot(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	if err := json.Unmarshal(snapshot.Data, &task); err != nil {
		t.Fatal(err)
	}
	if task["status"] != "waiting_user" {
		t.Fatalf("Task projection=%s", snapshot.Data)
	}
	query.ViewType, query.ViewID = projectionapp.ViewWorkerDetail, "session-deadline-claim"
	worker, err := views.ReadSnapshot(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Waiting struct {
			Message string   `json:"message"`
			Actions []string `json:"allowed_actions"`
		} `json:"waiting_user"`
	}
	if err := json.Unmarshal(worker.Data, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Waiting.Message != "自动恢复未启用，Runtime 已停止，需要处理" || len(detail.Waiting.Actions) != 0 {
		t.Fatalf("zero-attempt failure offered unsupported execution/message: %s", worker.Data)
	}
	for _, action := range worker.AvailableActions {
		if action.Action == "worker.queue" || action.Action == "worker.interrupt" || action.Action == "worker.follow_up" {
			t.Fatalf("dead Runtime action=%+v", action)
		}
	}
	reloaded, err := views.ReadSnapshot(ctx, query)
	if err != nil || !reflect.DeepEqual(worker.Data, reloaded.Data) || !reflect.DeepEqual(worker.RelatedRefs, reloaded.RelatedRefs) || worker.ViewCursor != reloaded.ViewCursor {
		t.Fatalf("reload changed failure identity: err=%v", err)
	}
}

type runtimeFailureFixture struct {
	pool       *pgxpool.Pool
	event      contractsv1.IntegrationEvent
	store      *taskpostgres.Store
	publisher  runtimeFailureEventPublisher
	dispatcher platformpostgres.OutboxDispatcher
}

func newRuntimeFailureFixture(t *testing.T, options ...string) runtimeFailureFixture {
	t.Helper()
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedBackendDeadlineClaimGraph(t, pool, now.Add(time.Hour))
	for _, option := range options {
		if option == "worker-only" {
			if _, err := pool.Exec(ctx, `UPDATE worker_runs SET task_id=NULL,origin_task_id='task-deadline-claim',trigger_type='direct_agent_turn' WHERE worker_run_id='run-deadline-claim'; UPDATE worker_sessions SET bound_task_id=NULL WHERE worker_session_id='session-deadline-claim'`); err != nil {
				t.Fatal(err)
			}
		}
		if option == "idle-worker" || option == "idle-delivered-task" {
			bindingStatement := `UPDATE worker_sessions SET status='idle',bound_task_id=NULL,active_worker_run_id=NULL,updated_at=$1 WHERE worker_session_id='session-deadline-claim'`
			if option == "idle-delivered-task" {
				bindingStatement = `UPDATE worker_sessions SET status='idle',active_worker_run_id=NULL,updated_at=$1 WHERE worker_session_id='session-deadline-claim'`
			}
			for _, statement := range []string{`UPDATE agent_turns SET status='completed',terminal_at=$1,terminal_reason='completed' WHERE agent_turn_id='turn-deadline-claim'`, `UPDATE worker_runs SET status='completed',terminal_at=$1 WHERE worker_run_id='run-deadline-claim'`, bindingStatement} {
				if _, err := pool.Exec(ctx, statement, now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			if option == "idle-delivered-task" {
				if _, err := pool.Exec(ctx, `UPDATE tasks SET status='delivered' WHERE task_id='task-deadline-claim'`); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Preceding Worker events demonstrate that a failure-only consumer accepts
	// sequence 2 without falsely requiring a failure receipt at sequence 1.
	if err := platformpostgres.InTransaction(ctx, pool, func(tx platformpostgres.Tx) error {
		stream := platformpostgres.OwnerStream{StreamID: "worker-session/session-deadline-claim", OwnerContext: "worker-runtime", AggregateType: "WorkerSession", AggregateID: "session-deadline-claim"}
		sequence, err := (platformpostgres.OwnerStreamRepository{}).Next(ctx, tx, stream)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO outbox_events (schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,visibility,occurred_at,payload) VALUES ($1,'preceding-worker-event','WorkerRunStarted.v1','worker-runtime',$2,$3,'tenant-local','WorkerSession','session-deadline-claim','fixture-trace','fixture-correlation','fixture-causation','preceding-worker-key','internal',$4,'{}')`, contractsv1.IntegrationEventSchemaVersion, stream.StreamID, sequence, now.Add(-time.Second))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	workerStore := workerpostgres.NewRuntimeRecoveryStore(pool)
	observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapp.RuntimeRecoveryService{Store: workerStore, IDs: randomIDs{}, Now: func() time.Time { return now }}, false)
	state, recorded, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{WorkerSessionID: "session-deadline-claim", RuntimeInstanceID: "runtime-deadline-claim", RuntimeGeneration: 1, ChannelEpoch: 7, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "runtime-inspection/fixture", EvidenceAt: now, Inspection: &workerapp.RuntimeInspection{Exists: true, ContainerID: "container-deadline-claim", Generation: 1, ObservedAt: now}})
	if err != nil || !recorded {
		t.Fatalf("persist Worker failure: state=%+v err=%v", state, err)
	}
	var event contractsv1.IntegrationEvent
	if err := pool.QueryRow(ctx, `SELECT schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,COALESCE(client_command_id,''),visibility,occurred_at,payload FROM outbox_events WHERE event_id=$1`, state.EventID).Scan(&event.SchemaVersion, &event.EventID, &event.EventType, &event.ProducerContext, &event.StreamID, &event.Sequence, &event.TenantID, &event.AggregateType, &event.AggregateID, &event.TraceID, &event.CorrelationID, &event.CausationID, &event.IdempotencyKey, &event.ClientCommandID, &event.Visibility, &event.OccurredAt, &event.Payload); err != nil {
		t.Fatal(err)
	}
	store := taskpostgres.NewStore(pool)
	projectionStore := projectionpostgres.NewStore(pool)
	views, err := projectionapp.NewViewContinuityReader(projectionStore, projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now }, ReplayLimit: 128})
	if err != nil {
		t.Fatal(err)
	}
	materializer := projectionpostgres.Materializer{Pool: pool, Projector: projectionapp.Projector{Store: projectionStore}, Views: views, UserID: "user-local", Now: func() time.Time { return now }}
	publisher := runtimeFailureEventPublisher{Consumer: taskapp.RuntimeFailureConsumer{Reader: store, Pause: taskapp.TaskPauseService{Store: store, IDs: randomIDs{}, Now: func() time.Time { return now }}}, Next: materializer}
	fixture := runtimeFailureFixture{pool: pool, event: event, store: store, publisher: publisher, dispatcher: platformpostgres.OutboxDispatcher{Pool: pool, Publisher: publisher, LeaseOwner: "failure-boundary-test", LeaseDuration: time.Minute, BatchSize: 1, Now: func() time.Time { return now }}}
	if published, err := fixture.dispatcher.RunOnce(ctx); err != nil || published != 1 {
		t.Fatalf("publish preceding Worker owner event: published=%d err=%v", published, err)
	}
	return fixture
}

func (fixture runtimeFailureFixture) assertOwner(t *testing.T, wantStatus string, wantCount int) {
	t.Helper()
	var status string
	var transitions, events, incidents int
	if err := fixture.pool.QueryRow(context.Background(), `SELECT status,(SELECT count(*) FROM task_transitions),(SELECT count(*) FROM outbox_events WHERE event_type='TaskWaitingForUser.v1'),(SELECT count(*) FROM runtime_recovery_incidents) FROM tasks WHERE task_id='task-deadline-claim'`).Scan(&status, &transitions, &events, &incidents); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || transitions != wantCount || events != wantCount || incidents != 1 {
		t.Fatalf("durable owners: status=%s transitions=%d events=%d incidents=%d", status, transitions, events, incidents)
	}
}

func TestRuntimeFailureOwnerRetriesOrdinaryErrorsAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"ordinary-read-error", "ordinary-update-error", "owner-outbox-error"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			install, remove := "", ""
			switch scenario {
			case "ordinary-read-error":
				install = `ALTER TABLE tasks RENAME TO unavailable_tasks`
				remove = `ALTER TABLE unavailable_tasks RENAME TO tasks`
			case "ordinary-update-error":
				install = `CREATE FUNCTION reject_owner_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled Task update failure'; END $$; CREATE TRIGGER reject_owner_write BEFORE UPDATE ON tasks FOR EACH ROW EXECUTE FUNCTION reject_owner_write()`
				remove = `DROP TRIGGER reject_owner_write ON tasks`
			case "owner-outbox-error":
				install = `CREATE FUNCTION reject_owner_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled owner Outbox failure'; END $$; CREATE TRIGGER reject_owner_write BEFORE INSERT ON outbox_events FOR EACH ROW WHEN (NEW.producer_context='task-orchestration') EXECUTE FUNCTION reject_owner_write()`
				remove = `DROP TRIGGER reject_owner_write ON outbox_events`
			}
			if _, err := fixture.pool.Exec(ctx, install); err != nil {
				t.Fatal(err)
			}
			_, err := fixture.dispatcher.RunOnce(ctx)
			if err == nil || errors.Is(err, platformpostgres.ErrNotFound) || errors.Is(err, platformpostgres.ErrRevisionConflict) {
				t.Fatalf("ordinary error became no-op/conflict: %v", err)
			}
			var released bool
			if err := fixture.pool.QueryRow(ctx, `SELECT lease_owner IS NULL AND published_at IS NULL AND last_error_code IS NOT NULL FROM outbox_events WHERE event_id=$1`, fixture.event.EventID).Scan(&released); err != nil || !released {
				t.Fatalf("failed publish not released: %t %v", released, err)
			}
			if _, err := fixture.pool.Exec(ctx, remove); err != nil {
				t.Fatal(err)
			}
			fixture.assertOwner(t, "running", 0)
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			fixture.assertOwner(t, "waiting_user", 1)
		})
	}
}

type failOncePublisher struct {
	next         platformpostgres.EventPublisher
	afterPublish bool
	failed       bool
}

func (publisher *failOncePublisher) SupportedIntegrationEventSchemaVersions() []string {
	return publisher.next.SupportedIntegrationEventSchemaVersions()
}
func (publisher *failOncePublisher) Publish(ctx context.Context, event contractsv1.IntegrationEvent) error {
	if !publisher.failed {
		publisher.failed = true
		if publisher.afterPublish {
			if err := publisher.next.Publish(ctx, event); err != nil {
				return err
			}
		}
		return errors.New("controlled publish response loss after owner commit")
	}
	return publisher.next.Publish(ctx, event)
}

func TestRuntimeFailureOwnerResponseLossConvergesOriginalEvent(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "after-task-commit", true: "after-projection-commit"}[after], func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			publisher := fixture.publisher
			publisher.Next = &failOncePublisher{next: publisher.Next, afterPublish: after}
			fixture.dispatcher.Publisher = publisher
			if _, err := fixture.dispatcher.RunOnce(ctx); err == nil {
				t.Fatal("response loss not injected")
			}
			fixture.assertOwner(t, "waiting_user", 1)
			command, found, err := fixture.store.ReadRuntimeFailureTask(ctx, fixture.event)
			if err != nil || !found {
				t.Fatalf("duplicate owner evidence: %t %v", found, err)
			}
			first, err := fixture.publisher.Consumer.Pause.Execute(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			second, err := fixture.publisher.Consumer.Pause.Execute(ctx, command)
			if err != nil || first != second || first.EventID == "" {
				t.Fatalf("duplicate result differs: first=%+v second=%+v err=%v", first, second, err)
			}
			var eventID, transitionID string
			if err := fixture.pool.QueryRow(ctx, `SELECT (SELECT event_id FROM outbox_events WHERE event_type='TaskWaitingForUser.v1'),(SELECT transition_id FROM task_transitions LIMIT 1)`).Scan(&eventID, &transitionID); err != nil {
				t.Fatal(err)
			}
			if first.EventID != eventID || first.EventID == transitionID {
				t.Fatalf("duplicate restored incorrect event identity: %+v", first)
			}
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			fixture.assertOwner(t, "waiting_user", 1)
		})
	}
}

func TestRuntimeFailureOwnerRejectsChangedEventAndActorDuplicates(t *testing.T) {
	for _, scenario := range []string{"header-before", "payload-before", "header-duplicate", "payload-duplicate", "changed-durable-digest", "wrong-actor-duplicate", "wrong-tenant-duplicate", "changed-reason-duplicate", "switched-binding-duplicate", "changed-incident-run"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			duplicate := scenario != "header-before" && scenario != "payload-before" && scenario != "changed-incident-run"
			if duplicate {
				if err := fixture.publisher.Consumer.Consume(ctx, fixture.event); err != nil {
					t.Fatal(err)
				}
			}
			command, found, err := fixture.store.ReadRuntimeFailureTask(ctx, fixture.event)
			if err != nil || !found {
				t.Fatalf("exact command: %t %v", found, err)
			}
			event := fixture.event
			switch scenario {
			case "header-before", "header-duplicate":
				event.TraceID = "altered-trace"
			case "payload-before", "payload-duplicate", "changed-durable-digest":
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				payload["actor_user_id"] = "forged-actor"
				event.Payload, _ = json.Marshal(payload)
				if scenario == "changed-durable-digest" {
					if _, err := fixture.pool.Exec(ctx, `UPDATE outbox_events SET payload=$2 WHERE event_id=$1`, event.EventID, event.Payload); err != nil {
						t.Fatal(err)
					}
				}
			case "wrong-actor-duplicate":
				command.Actor.UserID = "wrong-user"
			case "wrong-tenant-duplicate":
				command.Actor.TenantID = "wrong-tenant"
			case "changed-reason-duplicate":
				command.Reason = "other-reason"
			case "switched-binding-duplicate":
				if _, err := fixture.pool.Exec(ctx, `UPDATE tasks SET current_worker_run_id=NULL,revision=revision+1 WHERE task_id='task-deadline-claim'`); err != nil {
					t.Fatal(err)
				}
			case "changed-incident-run":
				if _, err := fixture.pool.Exec(ctx, `UPDATE runtime_recovery_incidents SET worker_run_id=NULL,agent_turn_id=NULL WHERE incident_id=$1`, event.CorrelationID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "wrong-actor-duplicate" || scenario == "wrong-tenant-duplicate" || scenario == "changed-reason-duplicate" || scenario == "switched-binding-duplicate" {
				_, err = fixture.publisher.Consumer.Pause.Execute(ctx, command)
			} else {
				err = fixture.publisher.Consumer.Consume(ctx, event)
			}
			if err == nil {
				t.Fatal("changed event/actor/binding accepted")
			}
			wantStatus, wantCount := "running", 0
			if duplicate {
				wantStatus, wantCount = "waiting_user", 1
			}
			fixture.assertOwner(t, wantStatus, wantCount)
		})
	}
}

func TestRuntimeFailureOwnerPreservesDeliveredTerminalAndOriginalBinding(t *testing.T) {
	for _, scenario := range []string{"delivered", "completed", "failed", "cancelled", "switched-task-run", "switched-session-run", "worker-only", "idle-worker", "already-waiting"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t, scenario)
			ctx := context.Background()
			statement := ""
			wantStatus := "running"
			switch scenario {
			case "delivered", "completed", "failed", "cancelled":
				statement = `UPDATE tasks SET status=$1,revision=revision+1 WHERE task_id='task-deadline-claim'`
				wantStatus = scenario
			case "switched-task-run":
				statement = `UPDATE tasks SET current_worker_run_id=NULL,revision=revision+1 WHERE task_id='task-deadline-claim'`
			case "switched-session-run":
				statement = `UPDATE worker_sessions SET active_worker_run_id=NULL,session_revision=session_revision+1 WHERE worker_session_id='session-deadline-claim'`
			case "worker-only":
				statement = `SELECT 1`
			case "idle-worker":
				statement = `SELECT 1`
			case "already-waiting":
				statement = `UPDATE tasks SET status='waiting_user',revision=revision+1,waiting_reason='{"code":"other-pause"}' WHERE task_id='task-deadline-claim'`
				wantStatus = "waiting_user"
			}
			var args []any
			if scenario == "delivered" || scenario == "completed" || scenario == "failed" || scenario == "cancelled" {
				args = []any{scenario}
			}
			if _, err := fixture.pool.Exec(ctx, statement, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			fixture.assertOwner(t, wantStatus, 0)
			if scenario == "idle-worker" {
				materializer := fixture.publisher.Next.(projectionpostgres.Materializer)
				snapshot, err := materializer.Views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-deadline-claim"})
				if err != nil {
					t.Fatal(err)
				}
				for _, action := range snapshot.AvailableActions {
					if action.Action == "worker.follow_up" || action.Action == "worker.queue" || action.Action == "worker.interrupt" {
						t.Fatalf("idle failed Runtime offered unsupported execution: %+v", action)
					}
				}
			}
		})
	}
}

type racingRuntimeFailureReader struct {
	store        *taskpostgres.Store
	pool         *pgxpool.Pool
	reads, races int
}

func (reader *racingRuntimeFailureReader) ReadRuntimeFailureTask(ctx context.Context, event contractsv1.IntegrationEvent) (taskapp.PauseTaskCommand, bool, error) {
	command, found, err := reader.store.ReadRuntimeFailureTask(ctx, event)
	reader.reads++
	if err == nil && found && reader.reads <= reader.races {
		_, err = reader.pool.Exec(ctx, `UPDATE tasks SET revision=revision+1 WHERE task_id=$1`, command.TaskID)
	}
	return command, found, err
}

func TestRuntimeFailureOwnerBoundsSameBindingRevisionReread(t *testing.T) {
	for _, races := range []int{1, 2} {
		t.Run(map[int]string{1: "one-race-converges", 2: "two-races-return-to-dispatcher"}[races], func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			reader := &racingRuntimeFailureReader{store: fixture.store, pool: fixture.pool, races: races}
			publisher := fixture.publisher
			publisher.Consumer.Reader = reader
			fixture.dispatcher.Publisher = publisher
			_, err := fixture.dispatcher.RunOnce(ctx)
			if reader.reads != 2 {
				t.Fatalf("unbounded/missing reread: reads=%d err=%v", reader.reads, err)
			}
			if races == 1 {
				if err != nil {
					t.Fatal(err)
				}
				fixture.assertOwner(t, "waiting_user", 1)
			} else {
				if !errors.Is(err, taskapp.ErrRuntimeFailureRevisionConflict) {
					t.Fatalf("contention was swallowed: %v", err)
				}
				fixture.assertOwner(t, "running", 0)
				if _, err := fixture.dispatcher.RunOnce(ctx); err != nil {
					t.Fatal(err)
				}
				fixture.assertOwner(t, "waiting_user", 1)
			}
		})
	}
}

func TestRuntimeFailurePauseCompatibilityRevalidatesDuplicate(t *testing.T) {
	for _, scenario := range []string{"stable-event-id", "wrong-actor", "wrong-tenant", "changed-reason", "changed-revision", "changed-binding", "ordinary-read-error"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			command := taskapp.PauseTaskCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-deadline-claim", WorkerSessionID: "session-deadline-claim", WorkerRunID: "run-deadline-claim", ExpectedTaskRevision: 3, Reason: "worker_waiting_user", IdempotencyKey: "ordinary-pause"}
			first, err := fixture.publisher.Consumer.Pause.Execute(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong-actor":
				command.Actor.UserID = "wrong-user"
			case "wrong-tenant":
				command.Actor.TenantID = "wrong-tenant"
			case "changed-reason":
				command.Reason = "other-reason"
			case "changed-revision":
				command.ExpectedTaskRevision = 99
			case "changed-binding":
				if _, err := fixture.pool.Exec(ctx, `UPDATE tasks SET current_worker_run_id=NULL,revision=revision+1 WHERE task_id=$1`, command.TaskID); err != nil {
					t.Fatal(err)
				}
			case "ordinary-read-error":
				if _, err := fixture.pool.Exec(ctx, `ALTER TABLE tasks RENAME TO unavailable_tasks`); err != nil {
					t.Fatal(err)
				}
			}
			second, err := fixture.publisher.Consumer.Pause.Execute(ctx, command)
			if scenario == "stable-event-id" {
				if err != nil || first != second {
					t.Fatalf("legacy pause response identity: first=%+v second=%+v err=%v", first, second, err)
				}
			} else if err == nil {
				t.Fatal("unsafe duplicate accepted")
			}
			if scenario == "ordinary-read-error" {
				if errors.Is(err, platformpostgres.ErrNotFound) || errors.Is(err, platformpostgres.ErrRevisionConflict) {
					t.Fatalf("ordinary pause read error misclassified: %v", err)
				}
				if _, err := fixture.pool.Exec(ctx, `ALTER TABLE unavailable_tasks RENAME TO tasks`); err != nil {
					t.Fatal(err)
				}
			}
			fixture.assertOwner(t, "waiting_user", 1)
		})
	}
}

func cleanupRuntimeFailureFixture(t *testing.T, fixture runtimeFailureFixture, fenceErr error) workerapp.RuntimeCleanupResult {
	t.Helper()
	ctx := context.Background()
	var command workerapp.BeginRuntimeCleanupCommand
	if err := fixture.pool.QueryRow(ctx, `SELECT session.tenant_id,session.worker_session_id,runtime.runtime_instance_id,runtime.container_id,runtime.generation,session.session_revision,runtime.revision FROM worker_sessions session JOIN runtime_instances runtime ON runtime.runtime_instance_id=session.active_runtime_instance_id WHERE session.worker_session_id='session-deadline-claim'`).Scan(&command.TenantID, &command.WorkerSessionID, &command.RuntimeInstanceID, &command.ExpectedContainerID, &command.RuntimeGeneration, &command.ExpectedSessionRevision, &command.ExpectedRuntimeRevision); err != nil {
		t.Fatal(err)
	}
	command.IdempotencyKey = "review-r1-cleanup"
	observed := time.Now().UTC().Truncate(time.Microsecond)
	service := workerapp.RuntimeCleanupService{Store: workerpostgres.NewRuntimeCleanupStore(fixture.pool), Cleaner: &workerfake.RuntimeProvisioner{CleanupResult: workerapp.RuntimeCleanupObservation{Confirmed: true, ContainerRef: "switchback-runtime-runtime-deadline-claim", SecretVolumeRef: "switchback-runtime-secrets-runtime-deadline-claim-g1", ObservedAt: observed}}, Control: &workerfake.WorkerControl{Err: fenceErr}, IDs: randomIDs{}, Now: func() time.Time { return observed }, RetryDelay: time.Second}
	result, err := service.Cleanup(ctx, command)
	if err != nil || result.Confirmed != (fenceErr == nil) {
		t.Fatalf("actual Runtime cleanup: result=%+v err=%v", result, err)
	}
	return result
}

type cleanupAfterRuntimeFailureRead struct {
	reader taskapp.RuntimeFailureTaskReader
	after  func()
	done   bool
}

func (reader *cleanupAfterRuntimeFailureRead) ReadRuntimeFailureTask(ctx context.Context, event contractsv1.IntegrationEvent) (taskapp.PauseTaskCommand, bool, error) {
	command, found, err := reader.reader.ReadRuntimeFailureTask(ctx, event)
	if err == nil && found && !reader.done {
		reader.done = true
		reader.after()
	}
	return command, found, err
}

func TestRuntimeFailureOwnerConsumesAfterConfirmedCleanup(t *testing.T) {
	for _, scenario := range []string{"cleanup-before-first-consume", "cleanup-between-reader-and-pause"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			if scenario == "cleanup-before-first-consume" {
				cleanupRuntimeFailureFixture(t, fixture, nil)
			} else {
				publisher := fixture.publisher
				publisher.Consumer.Reader = &cleanupAfterRuntimeFailureRead{reader: fixture.store, after: func() { cleanupRuntimeFailureFixture(t, fixture, nil) }}
				fixture.dispatcher.Publisher = publisher
			}
			if published, err := fixture.dispatcher.RunOnce(ctx); err != nil || published != 1 {
				t.Fatalf("publish failure after actual cleanup: published=%d err=%v", published, err)
			}
			fixture.assertOwner(t, "waiting_user", 1)
			var released, sourceAck bool
			if err := fixture.pool.QueryRow(ctx, `SELECT active_runtime_instance_id IS NULL AND bound_task_id='task-deadline-claim' AND active_worker_run_id='run-deadline-claim',(SELECT published_at IS NOT NULL FROM outbox_events WHERE event_id=$1) FROM worker_sessions WHERE worker_session_id='session-deadline-claim'`, fixture.event.EventID).Scan(&released, &sourceAck); err != nil || !released || !sourceAck {
				t.Fatalf("same failed Task/Run lost: released=%t ack=%t err=%v", released, sourceAck, err)
			}
			materializer := fixture.publisher.Next.(projectionpostgres.Materializer)
			query := projectionapp.SnapshotQuery{Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, ViewType: projectionapp.ViewTaskSummary, ViewID: "task-deadline-claim"}
			snapshot, err := materializer.Views.ReadSnapshot(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			var task struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(snapshot.Data, &task); err != nil || task.Status != "waiting_user" {
				t.Fatalf("cleanup projection lost owner state: %s err=%v", snapshot.Data, err)
			}
			// A retry after cleanup remains the original pause, not a fresh event.
			if err := fixture.publisher.Consumer.Consume(ctx, fixture.event); err != nil {
				t.Fatal(err)
			}
			fixture.assertOwner(t, "waiting_user", 1)
		})
	}
}

func TestRuntimeFailureOwnerDoesNotAcknowledgeUnprovenCleanupOrRuntime(t *testing.T) {
	for _, scenario := range []string{"unproven-null", "unconfirmed-fence-null", "wrong-cleanup-runtime", "wrong-cleanup-generation", "wrong-cleanup-container", "wrong-cleanup-session", "wrong-cleanup-tenant", "wrong-failed-generation", "wrong-failed-epoch", "wrong-session-generation", "unknown-runtime", "unknown-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRuntimeFailureFixture(t)
			ctx := context.Background()
			statement := ""
			switch scenario {
			case "unproven-null":
				statement = `UPDATE worker_sessions SET active_runtime_instance_id=NULL WHERE worker_session_id='session-deadline-claim'`
			case "unconfirmed-fence-null":
				cleanupRuntimeFailureFixture(t, fixture, errors.New("controlled external fence unconfirmed"))
				statement = `UPDATE worker_sessions SET active_runtime_instance_id=NULL WHERE worker_session_id='session-deadline-claim'`
			case "unknown-runtime":
				statement = `UPDATE worker_sessions SET active_runtime_instance_id='unknown-runtime' WHERE worker_session_id='session-deadline-claim'`
			case "unknown-replacement":
				statement = `UPDATE worker_sessions SET active_runtime_instance_id='unknown-runtime',runtime_generation=2 WHERE worker_session_id='session-deadline-claim'`
			default:
				// Corrupt only the isolated fixture after the real owner committed
				// confirmed cleanup. These are rejection cases, never happy-path
				// fabrication of cleanup_confirmed or NULL Runtime state.
				cleanupRuntimeFailureFixture(t, fixture, nil)
				switch scenario {
				case "wrong-cleanup-runtime":
					statement = `UPDATE scheduled_commands SET payload=jsonb_set(payload,'{runtime_instance_id}','"other-runtime"') WHERE command_type='runtime_cleanup_reconcile'`
				case "wrong-cleanup-generation":
					statement = `UPDATE scheduled_commands SET payload=jsonb_set(payload,'{runtime_generation}','2') WHERE command_type='runtime_cleanup_reconcile'`
				case "wrong-cleanup-container":
					statement = `UPDATE scheduled_commands SET payload=jsonb_set(payload,'{expected_container_id}','"other-container"') WHERE command_type='runtime_cleanup_reconcile'`
				case "wrong-cleanup-session":
					statement = `UPDATE scheduled_commands SET payload=jsonb_set(payload,'{worker_session_id}','"other-session"') WHERE command_type='runtime_cleanup_reconcile'`
				case "wrong-cleanup-tenant":
					statement = `UPDATE scheduled_commands SET payload=jsonb_set(payload,'{tenant_id}','"other-tenant"') WHERE command_type='runtime_cleanup_reconcile'`
				case "wrong-failed-generation":
					statement = `UPDATE runtime_instances SET generation=2 WHERE runtime_instance_id='runtime-deadline-claim'`
				case "wrong-failed-epoch":
					statement = `UPDATE runtime_instances SET control_channel_epoch=8 WHERE runtime_instance_id='runtime-deadline-claim'`
				case "wrong-session-generation":
					statement = `UPDATE worker_sessions SET runtime_generation=2 WHERE worker_session_id='session-deadline-claim'`
				}
			}
			if _, err := fixture.pool.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
			if published, err := fixture.dispatcher.RunOnce(ctx); err == nil || published != 0 {
				t.Fatalf("unproven failure was ACKed: published=%d err=%v", published, err)
			}
			fixture.assertOwner(t, "running", 0)
			var retained bool
			if err := fixture.pool.QueryRow(ctx, `SELECT published_at IS NULL AND lease_owner IS NULL AND last_error_code IS NOT NULL FROM outbox_events WHERE event_id=$1`, fixture.event.EventID).Scan(&retained); err != nil || !retained {
				t.Fatalf("unknown cleanup lost durable retry: retained=%t err=%v", retained, err)
			}
		})
	}
}
