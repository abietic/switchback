//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStartWorkerRunAndCancelTaskLockOrder(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	permit, ids := prepareDispatchLockPermit(t, pool, now)
	cancelPool := lockOrderPool(t, pool, "dispatch-lock-cancel")
	startPool := lockOrderPool(t, pool, "dispatch-lock-start")
	release := holdDispatchTaskUpdate(t, ctx, pool, "dispatch-lock-cancel")
	cancelResult := make(chan error, 1)
	go func() {
		_, err := (taskapp.TaskActionService{Store: taskpostgres.NewStore(cancelPool),
			IDs: ids,
			Now: func() time.Time { return now.Add(4 * time.Minute) }}).Cancel(ctx,
			taskapp.CancelTaskCommand{
				Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: permit.TaskID, ExpectedTaskRevision: 2, IdempotencyKey: "lock-cancel",
			})
		cancelResult <- err
	}()
	waitDispatchLockEdge(t, ctx, pool, "dispatch-lock-cancel", "dispatch-lock-gate")
	startResult := make(chan error, 1)
	go func() {
		_, err := (workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(startPool),
			IDs: ids,
			Now: func() time.Time { return now.Add(4 * time.Minute) }}).Execute(ctx,
			residentStartCommand(permit))
		startResult <- err
	}()
	waitDispatchLockEdge(t, ctx, pool, "dispatch-lock-start", "dispatch-lock-cancel")
	release()
	cancelErr, startErr := <-cancelResult, <-startResult
	logDispatchError(t, "CancelTask", cancelErr)
	logDispatchError(t, "StartWorkerRun", startErr)
	if cancelErr != nil || !errors.Is(startErr, workerpostgres.ErrDispatchPermitInvalid) {
		t.Fatalf("cancel must commit and start must reject revoked permit: cancel=%v start=%v", cancelErr, startErr)
	}
	for _, table := range []string{"worker_runs", "agent_turns", "runtime_instances"} {
		assertAllocationCount(t, pool, table, 0)
	}
	var starts, deadlines int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='WorkerRunStarted.v1'`).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM scheduled_commands WHERE command_type='agent_turn_deadline'`).Scan(&deadlines); err != nil {
		t.Fatal(err)
	}
	if starts != 0 || deadlines != 0 {
		t.Fatalf("rejected start leaked events=%d deadlines=%d", starts, deadlines)
	}
	var status, permitStatus, queueStatus string
	var revision uint64
	if err := pool.QueryRow(ctx, `SELECT task.status,task.revision,permit.status,queue.status
FROM tasks task JOIN session_dispatch_permits permit ON permit.target_id=task.task_id
JOIN session_task_queue_entries queue ON queue.task_id=task.task_id
WHERE task.task_id=$1`, permit.TaskID).Scan(&status, &revision, &permitStatus, &queueStatus); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || revision != 3 || permitStatus != "revoked" || queueStatus != "cancelled" {
		t.Fatalf("cancel state=%s/%d permit=%s queue=%s", status, revision, permitStatus, queueStatus)
	}
}

func prepareDispatchLockPermit(t *testing.T, pool *pgxpool.Pool, now time.Time) (taskapp.IssuedTaskPermit, *concurrentAllocationIDs) {
	t.Helper()
	ctx := context.Background()
	seedAllocationRequests(t, pool, 1, now)
	runtimecapacity.Initialize(t, pool)
	ids := &concurrentAllocationIDs{}
	allocated, err := (workerapp.WorkerSessionAllocator{Store: workerpostgres.NewAllocatorStore(pool),
		IDs: ids,
		Now: func() time.Time { return now.Add(time.Minute) }}).Execute(ctx,
		workerapp.AllocateWorkerSessionCommand{TenantID: "tenant-local",
			UserID:              "user-local",
			AllocationRequestID: "allocation-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2 WHERE worker_session_id=$1`, allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	dispatch := taskapp.SessionDispatchService{Store: taskpostgres.NewStore(pool), IDs: ids, Now: func() time.Time { return now.Add(3 * time.Minute) }, PermitTTL: 5 * time.Minute}
	if _, err := dispatch.EnqueueTask(ctx,
		taskapp.EnqueueTaskCommand{TenantID: "tenant-local",
			UserID:                  "user-local",
			TaskID:                  "task-01",
			WorkerSessionID:         allocated.WorkerSessionID,
			ExpectedTaskRevision:    1,
			ExpectedSessionRevision: 2,
			IdempotencyKey:          "lock-enqueue"}); err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(ctx,
		taskapp.IssueTaskPermitCommand{TenantID: "tenant-local",
			UserID:            "user-local",
			WorkerSessionID:   allocated.WorkerSessionID,
			RuntimeGeneration: 1,
			IdempotencyKey:    "lock-permit"})
	if err != nil {
		t.Fatal(err)
	}
	return permit, ids
}

// The trigger is confined to postgrestest's schema and the exact command/Task.
// Reaching it proves the real action already acquired the Task row lock.
func holdDispatchTaskUpdate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, action string) func() {
	t.Helper()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION dispatch_task_gate() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.task_id='task-01' AND current_setting('application_name')='%s' THEN
 PERFORM pg_advisory_xact_lock(hashtextextended(current_schema() || ':dispatch-task-gate',0));
 END IF; RETURN NEW; END $$;
 CREATE TRIGGER dispatch_task_gate BEFORE UPDATE ON tasks FOR EACH ROW EXECUTE FUNCTION dispatch_task_gate()`, action)); err != nil {
		t.Fatal(err)
	}
	gatePool := lockOrderPool(t, pool, "dispatch-lock-gate")
	tx, err := gatePool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema() || ':dispatch-task-gate',0))`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func waitDispatchLockEdge(t *testing.T, ctx context.Context, pool *pgxpool.Pool, waiter, blocker string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiterPID, blockerPID int
		var query, event string
		err := pool.QueryRow(ctx, `SELECT w.pid,b.pid,w.query,w.wait_event
FROM pg_stat_activity w JOIN pg_stat_activity b ON b.pid=ANY(pg_blocking_pids(w.pid))
WHERE w.application_name=$1 AND b.application_name=$2 AND w.wait_event_type='Lock'`, waiter, blocker).Scan(&waiterPID, &blockerPID, &query, &event)
		if err == nil {
			t.Logf("catalog edge %s(pid=%d) -> %s(pid=%d) event=%s query=%s", waiter, waiterPID, blocker, blockerPID, event, query)
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("missing catalog edge %s -> %s: %v", waiter, blocker, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}

func logDispatchError(t *testing.T, command string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Logf("%s SQLSTATE=%s message=%s detail=%s where=%s", command, pgErr.Code, pgErr.Message, pgErr.Detail, pgErr.Where)
	} else {
		t.Logf("%s error=%v", command, err)
	}
}

func TestEnqueueTaskAndAcceptTaskLockOrder(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	permit, ids := prepareDispatchLockPermit(t, pool, now)
	start := workerapp.StartWorkerRunService{Store: workerpostgres.NewRunStore(pool), IDs: ids, Now: func() time.Time { return now.Add(4 * time.Minute) }}
	started, err := start.Execute(ctx, residentStartCommand(permit))
	if err != nil {
		t.Fatal(err)
	}
	residency := workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return now.Add(5 * time.Minute) }}
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "test-only-resident", 1)
	active, err := residency.Activate(ctx, workerapp.ActivateResidentRuntimeCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		ContainerID:             "test-only-resident",
		RuntimeGeneration:       1,
		ControlChannelEpoch:     1,
		ExpectedSessionRevision: started.SessionRevision,
		ExpectedRunRevision:     1,
		ExpectedTurnRevision:    1,
		ExpectedRuntimeRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := residency.CompleteStage(ctx, workerapp.CompleteResidentStageCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       1,
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision:     active.RunRevision,
		ExpectedTurnRevision:    active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		SessionTarget:           domain.SessionInteractive,
		RunTarget:               domain.RunCompleted,
		TurnTarget:              domain.TurnCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}
	delivery := taskapp.TaskDeliveryService{Store: taskpostgres.NewDeliveryStore(pool), IDs: ids, Now: func() time.Time { return now.Add(6 * time.Minute) }}
	delivered, err := delivery.RecordStageResult(ctx,
		taskapp.RecordStageResultCommand{Actor: actor,
			TaskID:               permit.TaskID,
			WorkerSessionID:      started.WorkerSessionID,
			WorkerRunID:          started.WorkerRunID,
			ExpectedTaskRevision: 3,
			IdempotencyKey:       "lock-delivery",
			ArtifactRefs: []contractsv1.ArtifactRef{{
				SchemaVersion:     contractsv1.ArtifactRefSchemaVersion,
				ArtifactID:        "artifact-lock-report",
				TenantID:          "tenant-local",
				UserID:            "user-local",
				WorkspaceID:       "workspace-demo",
				TaskID:            contractsv1.TaskID(permit.TaskID),
				WorkerSessionID:   contractsv1.WorkerSessionID(started.WorkerSessionID),
				WorkerRunID:       contractsv1.WorkerRunID(started.WorkerRunID),
				AgentTurnID:       contractsv1.AgentTurnID(started.AgentTurnID),
				RuntimeInstanceID: contractsv1.RuntimeInstanceID(started.RuntimeInstanceID),
				RuntimeGeneration: 1,
				Type:              "report",
				URI:               "object://artifact/lock-report",
				SHA256:            strings.Repeat("a", 64),
				Permission: contractsv1.ArtifactPermission{Visibility: "private",
					PolicyRevision: "policy-1"},
				RetentionPolicyRef: "retain-local",
				StorageTier:        "hot",
				Availability:       "online",
				Completeness:       "complete",
				SourceCheckpointRef: contractsv1.CheckpointRef{CheckpointID: "lock-checkpoint",
					Digest: "sha256:" + strings.Repeat("b", 64)},
				AvailableActions: []string{"download"},
				CreatedAt:        now,
			}}})
	if err != nil || delivered.Status != "delivered" {
		t.Fatalf("stage delivery=%+v err=%v", delivered, err)
	}
	acceptPool := lockOrderPool(t, pool, "dispatch-lock-accept")
	enqueuePool := lockOrderPool(t, pool, "dispatch-lock-enqueue")
	release := holdDispatchTaskUpdate(t, ctx, pool, "dispatch-lock-accept")
	acceptCommand := taskapp.AcceptTaskCommand{Actor: actor, TaskID: permit.TaskID, ExpectedTaskRevision: delivered.Revision, IdempotencyKey: "lock-accept"}
	delivery.Store = taskpostgres.NewDeliveryStore(acceptPool)
	acceptResult := make(chan error, 1)
	go func() { _, err := delivery.Accept(ctx, acceptCommand); acceptResult <- err }()
	waitDispatchLockEdge(t, ctx, pool, "dispatch-lock-accept", "dispatch-lock-gate")
	dispatch := taskapp.SessionDispatchService{Store: taskpostgres.NewStore(enqueuePool),
		IDs:       ids,
		Now:       func() time.Time { return now.Add(7 * time.Minute) },
		PermitTTL: 5 * time.Minute}
	enqueueResult := make(chan error, 1)
	go func() {
		_, err := dispatch.EnqueueTask(ctx,
			taskapp.EnqueueTaskCommand{TenantID: "tenant-local",
				UserID:                  "user-local",
				TaskID:                  permit.TaskID,
				WorkerSessionID:         permit.WorkerSessionID,
				ExpectedTaskRevision:    delivered.Revision,
				ExpectedSessionRevision: completed.SessionRevision,
				IdempotencyKey:          "invalid-already-bound-enqueue"})
		enqueueResult <- err
	}()
	waitDispatchLockEdge(t, ctx, pool, "dispatch-lock-enqueue", "dispatch-lock-accept")
	release()
	acceptErr, enqueueErr := <-acceptResult, <-enqueueResult
	logDispatchError(t, "AcceptTask", acceptErr)
	logDispatchError(t, "EnqueueTask", enqueueErr)
	if acceptErr != nil || (!errors.Is(enqueueErr, taskpostgres.ErrSessionNotDispatchable) && !errors.Is(enqueueErr, platformpostgres.ErrRevisionConflict)) {
		t.Fatalf("accept must commit and bound enqueue must reject: accept=%v enqueue=%v", acceptErr, enqueueErr)
	}
	var status, bound string
	var taskRevision, sessionRevision uint64
	if err := pool.QueryRow(ctx, `SELECT task.status,task.revision,session.session_revision,COALESCE(session.bound_task_id,'')
FROM tasks task JOIN worker_sessions session ON session.worker_session_id=task.worker_session_id
WHERE task.task_id=$1`, permit.TaskID).Scan(&status, &taskRevision, &sessionRevision, &bound); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || taskRevision != delivered.Revision+1 || sessionRevision != completed.SessionRevision+1 || bound != "" {
		t.Fatalf("accept state=%s/%d session=%d bound=%s", status, taskRevision, sessionRevision, bound)
	}
	for _, table := range []string{"session_task_queue_entries", "session_dispatch_permits", "worker_runs", "agent_turns", "runtime_instances"} {
		assertAllocationCount(t, pool, table, 1)
	}
	var acceptedEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='TaskAccepted.v1'`).Scan(&acceptedEvents); err != nil || acceptedEvents != 1 {
		t.Fatalf("accept events=%d err=%v", acceptedEvents, err)
	}
	// Response-loss replays remain valid after Task completion and Session revision
	// changes: no new Run, permit or queue position may be created.
	replay, err := start.Execute(ctx, residentStartCommand(permit))
	if err != nil || replay.WorkerRunID != started.WorkerRunID || replay.AgentTurnID != started.AgentTurnID {
		t.Fatalf("consumed old permit replay=%+v err=%v", replay, err)
	}
	enqueued, err := dispatch.EnqueueTask(ctx,
		taskapp.EnqueueTaskCommand{TenantID: "tenant-local",
			UserID:                  "user-local",
			TaskID:                  permit.TaskID,
			WorkerSessionID:         permit.WorkerSessionID,
			ExpectedTaskRevision:    1,
			ExpectedSessionRevision: 2,
			IdempotencyKey:          "lock-enqueue"})
	if err != nil || enqueued.SessionQueueSequence != permit.SessionQueueSequence || enqueued.QueueRevision != permit.QueueRevision {
		t.Fatalf("original enqueue replay=%+v err=%v", enqueued, err)
	}
	accepted, err := delivery.Accept(ctx, acceptCommand)
	if err != nil || accepted.Status != "completed" || accepted.Revision != taskRevision {
		t.Fatalf("accept replay=%+v err=%v", accepted, err)
	}
	for _, table := range []string{"session_task_queue_entries", "session_dispatch_permits", "worker_runs", "agent_turns"} {
		assertAllocationCount(t, pool, table, 1)
	}
}
