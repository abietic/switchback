//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestWorkerSessionAllocatorEnforcesQuotaAndStableFIFOWithConcurrentRetries(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 7, 0, 0, 0, time.UTC)
	seedAllocationRequests(t, pool, 12, now)
	allocator := workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: &concurrentAllocationIDs{},
		Now: func() time.Time { return now.Add(time.Minute) },
	}

	_, err := allocator.Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-02",
	})
	if !errors.Is(err, workerpostgres.ErrAllocationNotHead) {
		t.Fatalf("out-of-order allocation error=%v", err)
	}

	allocatedSessions := make(map[string]struct{})
	for sequence := 1; sequence <= 10; sequence++ {
		requestID := fmt.Sprintf("allocation-%02d", sequence)
		results := allocateConcurrently(t, allocator, requestID, 8)
		firstSession := results[0].WorkerSessionID
		if firstSession == "" || results[0].CapacityFull {
			t.Fatalf("request %s result=%+v", requestID, results[0])
		}
		for _, result := range results[1:] {
			if result.WorkerSessionID != firstSession || result.CapacityFull {
				t.Fatalf("request %s response-loss retry diverged: %+v", requestID, results)
			}
		}
		if _, duplicate := allocatedSessions[firstSession]; duplicate {
			t.Fatalf("Session %s allocated to more than one request", firstSession)
		}
		allocatedSessions[firstSession] = struct{}{}
	}

	capacityResults := allocateConcurrently(t, allocator, "allocation-11", 8)
	for _, result := range capacityResults {
		if !result.CapacityFull || result.WorkerSessionID != "" || result.SessionAllocationSequence != 11 {
			t.Fatalf("capacity result=%+v", result)
		}
	}
	_, err = allocator.Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-12",
	})
	if !errors.Is(err, workerpostgres.ErrAllocationNotHead) {
		t.Fatalf("request 12 bypassed capacity head: %v", err)
	}
	replayedFirst, err := allocator.Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
	})
	if err != nil || replayedFirst.QuotaRevision != 2 {
		t.Fatalf("old allocation replay=%+v error=%v", replayedFirst, err)
	}

	var limit, reserved int
	var revision uint64
	if err := pool.QueryRow(ctx, `SELECT quota_limit,reserved_count,revision
FROM worker_session_continuity_quotas WHERE tenant_id='tenant-local' AND user_id='user-local'`).Scan(
		&limit, &reserved, &revision); err != nil {
		t.Fatal(err)
	}
	if limit != 10 || reserved != 10 || revision != 11 {
		t.Fatalf("quota limit=%d reserved=%d revision=%d", limit, reserved, revision)
	}
	assertAllocationCount(t, pool, "worker_sessions", 10)
	var allocatedRequests, allocationEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_allocation_requests WHERE status='allocated'`).Scan(&allocatedRequests); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE event_type='WorkerSessionAllocated.v1'`).Scan(&allocationEvents); err != nil {
		t.Fatal(err)
	}
	if allocatedRequests != 10 || allocationEvents != 10 {
		t.Fatalf("allocated requests=%d events=%d", allocatedRequests, allocationEvents)
	}
	var requestStatus, waitReason, taskStatus string
	var workerSessionID *string
	if err := pool.QueryRow(ctx, `
SELECT request.status,request.allocation_wait_reason,task.status,task.worker_session_id
FROM session_allocation_requests request
JOIN tasks task ON task.task_id=request.owner_id
WHERE request.allocation_request_id='allocation-11'`).Scan(
		&requestStatus, &waitReason, &taskStatus, &workerSessionID); err != nil {
		t.Fatal(err)
	}
	if requestStatus != "pending" || waitReason != "session_capacity" || taskStatus != "queued" || workerSessionID != nil {
		t.Fatalf("capacity head request=%s wait=%s task=%s session=%v", requestStatus, waitReason, taskStatus, workerSessionID)
	}
	resultContract := failurematrix.Result{
		Variant: "quota-full", ExecutionOutcome: "not_started", AcceptanceOutcome: "pending",
		Assertions: []string{"quota_not_exceeded", "task_remains_queued", "fifo_head_preserved"},
	}
	failurematrix.AssertAndRecord(t, resultContract, resultContract)
}

func allocateConcurrently(
	t *testing.T,
	allocator workerapplication.WorkerSessionAllocator,
	requestID string,
	concurrency int,
) []workerapplication.AllocationResult {
	t.Helper()
	results := make(chan workerapplication.AllocationResult, concurrency)
	errorsFound := make(chan error, concurrency)
	var wait sync.WaitGroup
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := allocator.Execute(context.Background(), workerapplication.AllocateWorkerSessionCommand{
				TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: requestID,
			})
			results <- result
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("allocate %s concurrently: %v", requestID, err)
		}
	}
	collected := make([]workerapplication.AllocationResult, 0, concurrency)
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func seedAllocationRequests(t *testing.T, pool *pgxpool.Pool, count int, now time.Time) {
	t.Helper()
	ctx := context.Background()
	for sequence := 1; sequence <= count; sequence++ {
		assessmentID := fmt.Sprintf("assessment-%02d", sequence)
		intentID := fmt.Sprintf("intent-%02d", sequence)
		taskID := fmt.Sprintf("task-%02d", sequence)
		requestID := fmt.Sprintf("allocation-%02d", sequence)
		digest := fmt.Sprintf("sha256:%064x", sequence)
		if err := postgres.InTransaction(ctx, pool, func(tx postgres.Tx) error {
			if _, err := tx.Exec(ctx, `
INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
   dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ($1,'tenant-local','user-local','workspace-demo','workspace-revision-1','api','direct-ready-v1',
  '{}'::jsonb,'[]'::jsonb,'[]'::jsonb,'ready_confirmed',$2,$3)`, assessmentID, digest, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
   task_spec_digest,trigger_policy_revision,status,created_at,expires_at)
VALUES ($1,'tenant-local','user-local','workspace-demo','workspace-revision-1',$2,$3,'direct-create-v1',
  'active',$4,$4::timestamptz + interval '15 minutes')`, intentID, assessmentID, digest, now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,
   task_spec_digest,creation_intent_id,status,acceptance_mode,result_refs,session_allocation_sequence,
   revision,created_at,updated_at)
VALUES ($1,'tenant-local','user-local','workspace-demo','workspace-revision-1','{}'::jsonb,$2,$3,
  'queued','manual','[]'::jsonb,$4,1,$5,$5)`, taskID, digest, intentID, sequence, now); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
INSERT INTO session_allocation_requests
  (allocation_request_id,tenant_id,user_id,workspace_id,workspace_revision_id,request_kind,owner_id,
   owner_revision,context_policy,session_allocation_sequence,status,idempotency_key,created_at,updated_at)
VALUES ($1,'tenant-local','user-local','workspace-demo','workspace-revision-1','task',$2,1,
  'fresh_session',$3,'pending',$4,$5,$5)`, requestID, taskID, sequence, "allocate-"+requestID, now)
			return err
		}); err != nil {
			t.Fatalf("seed allocation request %d: %v", sequence, err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO session_allocation_queue_positions (tenant_id,user_id,next_sequence,revision,updated_at)
VALUES ('tenant-local','user-local',$1,1,$2)`, count+1, now); err != nil {
		t.Fatalf("seed allocation queue position: %v", err)
	}
}

type concurrentAllocationIDs struct {
	sequence atomic.Uint64
}

func (ids *concurrentAllocationIDs) NewID(kind string) (string, error) {
	return fmt.Sprintf("%s-%06d", kind, ids.sequence.Add(1)), nil
}

func assertAllocationCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want=%d", table, got, want)
	}
}
