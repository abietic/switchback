//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestBackendRestartRecoversTaskAllocationOutboxIdempotencyAndDueCommands(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 16, 0, 0, 0, time.UTC)
	pool := postgrestest.Open(t)
	ids := &concurrentAllocationIDs{}
	admission := admitReuseTask(t, ids, "Restart durable task", now)
	taskStore := taskpostgres.NewStore(pool)
	if err := taskStore.RecordDirectAdmission(ctx, admission); err != nil {
		t.Fatal(err)
	}
	command := taskapplication.CreateTaskCommand{
		Actor:    taskapplication.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: admission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "restart-create-task",
	}
	created, err := (taskapplication.CreateTaskService{
		Store: taskStore, IDs: ids, Now: func() time.Time { return now.Add(time.Second) },
	}).Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}

	pool = postgrestest.Restart(t, pool)
	taskStore = taskpostgres.NewStore(pool)
	taskSnapshot, err := taskStore.ReadTask(ctx, created.TaskID)
	if err != nil || taskSnapshot.Status != "queued" || taskSnapshot.Revision != 1 {
		t.Fatalf("Task after restart=%+v error=%v", taskSnapshot, err)
	}
	replayed, err := (taskapplication.CreateTaskService{
		Store: taskStore, IDs: ids, Now: func() time.Time { return now.Add(2 * time.Second) },
	}).Execute(ctx, command)
	if err != nil || replayed.TaskID != created.TaskID || replayed.AllocationRequestID != created.AllocationRequestID {
		t.Fatalf("idempotent CreateTask after restart=%+v error=%v", replayed, err)
	}
	allocated, err := (workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(3 * time.Second) },
	}).Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: created.AllocationRequestID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionReader := workerpostgres.SessionReader{Pool: pool}
	sessionSnapshot, err := sessionReader.ReadWorkerSession(ctx, allocated.WorkerSessionID)
	if err != nil || sessionSnapshot.Status != "allocating" || sessionSnapshot.Revision != 1 {
		t.Fatalf("allocated Session=%+v error=%v", sessionSnapshot, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status='idle',idle_expires_at=$2,updated_at=$3 WHERE worker_session_id=$1`,
		allocated.WorkerSessionID, now.Add(-time.Second), now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scheduled_commands
SET due_at=$2 WHERE target_id=$1 AND command_type=$3`, allocated.WorkerSessionID,
		now.Add(-time.Second), workerpostgres.CommandWorkerSessionIdle); err != nil {
		t.Fatal(err)
	}

	pool = postgrestest.Restart(t, pool)
	taskStore = taskpostgres.NewStore(pool)
	if _, err := taskStore.ReadTask(ctx, created.TaskID); err != nil {
		t.Fatalf("read Task after second restart: %v", err)
	}
	sessionReader = workerpostgres.SessionReader{Pool: pool}
	if snapshot, err := sessionReader.ReadWorkerSession(ctx, allocated.WorkerSessionID); err != nil || snapshot.Status != "idle" {
		t.Fatalf("read active Session after second restart=%+v error=%v", snapshot, err)
	}
	publisher := &restartRecordingPublisher{}
	clock := advancingTestClock(now)
	published, err := (platformpostgres.OutboxDispatcher{
		Pool: pool, Publisher: publisher, LeaseOwner: "restart-dispatcher",
		LeaseDuration: time.Minute, BatchSize: 20, Now: clock,
	}).RunOnce(ctx)
	if err != nil || published != 2 {
		t.Fatalf("resumed Outbox published=%d error=%v", published, err)
	}
	runLifecyclePoller(t, pool, now.Add(5*time.Second))
	closed, err := sessionReader.ReadWorkerSession(ctx, allocated.WorkerSessionID)
	if err != nil || closed.Status != "closed" {
		t.Fatalf("due idle command after restart=%+v error=%v", closed, err)
	}
	var idempotencyResults, pendingOutbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_results
WHERE operation='create_task' AND idempotency_key='restart-create-task'`).Scan(&idempotencyResults); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&pendingOutbox); err != nil {
		t.Fatal(err)
	}
	if idempotencyResults != 1 || pendingOutbox != 0 || publisher.Count() != 2 {
		t.Fatalf("idempotency=%d pending_outbox=%d published=%d", idempotencyResults, pendingOutbox, publisher.Count())
	}
}

type restartRecordingPublisher struct {
	mu     sync.Mutex
	events []contractsv1.IntegrationEvent
}

func (publisher *restartRecordingPublisher) Publish(_ context.Context, event contractsv1.IntegrationEvent) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.events = append(publisher.events, event)
	return nil
}

func (*restartRecordingPublisher) SupportedIntegrationEventSchemaVersions() []string {
	return []string{
		contractsv1.IntegrationEventLegacySchemaVersion,
		contractsv1.IntegrationEventSchemaVersion,
	}
}

func (publisher *restartRecordingPublisher) Count() int {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	return len(publisher.events)
}
