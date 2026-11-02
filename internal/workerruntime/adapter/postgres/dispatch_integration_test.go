//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestSessionFIFOAndSingleUsePermitPreventConcurrentTopLevelRuns(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	seedAllocationRequests(t, pool, 3, now)
	runtimecapacity.Initialize(t, pool)
	ids := &concurrentAllocationIDs{}
	allocator := workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(time.Minute) },
	}
	allocated, err := allocator.Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2
WHERE worker_session_id=$1`, allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE session_allocation_requests
SET status='cancelled',updated_at=$1 WHERE allocation_request_id IN ('allocation-02','allocation-03')`, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	dispatch := taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(3 * time.Minute) }, PermitTTL: 5 * time.Minute,
	}
	first, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
		TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01",
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-task-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
		TenantID: "tenant-local", UserID: "user-local", TaskID: "task-02",
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-task-02",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionQueueSequence != 1 || second.SessionQueueSequence != 2 {
		t.Fatalf("queue order first=%+v second=%+v", first, second)
	}
	replayed, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
		TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01",
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-task-01",
	})
	if err != nil || replayed.SessionQueueSequence != first.SessionQueueSequence {
		t.Fatalf("enqueue replay=%+v error=%v", replayed, err)
	}

	permits := issuePermitConcurrently(t, dispatch, allocated.WorkerSessionID, 8)
	permitID := permits[0].PermitID
	for _, permit := range permits {
		if permit.PermitID != permitID || permit.TaskID != "task-01" || permit.SessionQueueSequence != 1 {
			t.Fatalf("permit retry diverged: %+v", permits)
		}
	}
	_, err = dispatch.IssueTaskPermit(ctx, taskapplication.IssueTaskPermitCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID,
		RuntimeGeneration: 1, IdempotencyKey: "different-permit-command",
	})
	if !errors.Is(err, taskpostgres.ErrDispatchPermitExists) {
		t.Fatalf("second active permit error=%v", err)
	}

	startRun := workerapplication.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(4 * time.Minute) },
	}
	runs := startRunConcurrently(t, startRun, permits[0], 12)
	runID := runs[0].WorkerRunID
	for _, run := range runs {
		if run.WorkerRunID != runID || run.TaskID != "task-01" ||
			run.RuntimeInstanceID != runs[0].RuntimeInstanceID ||
			run.AgentSessionID != runs[0].AgentSessionID || run.AgentTurnID != runs[0].AgentTurnID {
			t.Fatalf("permit consumption created divergent Runs: %+v", runs)
		}
	}
	assertAllocationCount(t, pool, "worker_runs", 1)
	assertAllocationCount(t, pool, "runtime_instances", 1)
	assertAllocationCount(t, pool, "agent_turns", 1)
	var boundTaskID, activeRunID, sessionStatus, permitStatus, queueStatus, taskStatus string
	if err := pool.QueryRow(ctx, `SELECT bound_task_id,active_worker_run_id,status
FROM worker_sessions WHERE worker_session_id=$1`, allocated.WorkerSessionID).Scan(
		&boundTaskID, &activeRunID, &sessionStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM session_dispatch_permits WHERE permit_id=$1`, permitID).Scan(&permitStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM session_task_queue_entries
WHERE worker_session_id=$1 AND task_id='task-01'`, allocated.WorkerSessionID).Scan(&queueStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE task_id='task-01'`).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if boundTaskID != "task-01" || activeRunID != runID || sessionStatus != "running" ||
		permitStatus != "consumed" || queueStatus != "dispatched" || taskStatus != "running" {
		t.Fatalf("slot task=%s run=%s session=%s permit=%s queue=%s task_status=%s",
			boundTaskID, activeRunID, sessionStatus, permitStatus, queueStatus, taskStatus)
	}
	_, err = dispatch.IssueTaskPermit(ctx, taskapplication.IssueTaskPermitCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID,
		RuntimeGeneration: 1, IdempotencyKey: "permit-task-02",
	})
	if !errors.Is(err, taskpostgres.ErrSessionNotDispatchable) {
		t.Fatalf("busy Session issued second Task permit: %v", err)
	}
}

func TestStartWorkerRunRejectsSessionWithCurrentRuntime(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	seedAllocationRequests(t, pool, 1, now)
	ids := &concurrentAllocationIDs{}
	allocated, err := (workerapplication.WorkerSessionAllocator{
		Store: workerpostgres.NewAllocatorStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(time.Minute) },
	}).Execute(ctx, workerapplication.AllocateWorkerSessionCommand{
		TenantID: "tenant-local", UserID: "user-local", AllocationRequestID: "allocation-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,generation,runtime_type,container_id,status,image_ref,
   resource_profile_id,network_policy_id,control_channel_epoch,revision,created_at,started_at)
VALUES ('runtime-existing',$1,1,'docker_container','container-existing','active',
  'switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  'local_standard_v1','m1_public_web_restricted_v1',1,1,$2,$2)`,
		allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='interactive',active_runtime_instance_id='runtime-existing',
    runtime_generation=1,session_revision=2,updated_at=$2
WHERE worker_session_id=$1`, allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	dispatch := taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(3 * time.Minute) }, PermitTTL: 5 * time.Minute,
	}
	if _, err := dispatch.EnqueueTask(ctx, taskapplication.EnqueueTaskCommand{
		TenantID: "tenant-local", UserID: "user-local", TaskID: "task-01",
		WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
		ExpectedSessionRevision: 2, IdempotencyKey: "enqueue-task-01",
	}); err != nil {
		t.Fatal(err)
	}
	permit, err := dispatch.IssueTaskPermit(ctx, taskapplication.IssueTaskPermitCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: allocated.WorkerSessionID,
		RuntimeGeneration: 1, IdempotencyKey: "permit-task-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	startRun := workerapplication.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(4 * time.Minute) },
	}
	if _, err := startRun.Execute(ctx, residentStartCommand(permit)); !errors.Is(
		err,
		workerpostgres.ErrSessionBusy,
	) {
		t.Fatalf("Session with current Runtime started another initial Runtime: %v", err)
	}
	assertAllocationCount(t, pool, "runtime_instances", 1)
}

func TestIssueTaskPermitWaitsForCancelledTaskTurnSettlementCompletion(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 6, 0, 0, 0, time.UTC)
	fixture := preparePendingDispatchBarrierFixture(t, pool, now)
	before := loadDispatchBarrierMutationCounts(t, pool)
	dispatch := taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool),
		IDs:   fixture.IDs,
		Now: func() time.Time {
			return fixture.Settlement.UpdatedAt.Add(time.Second)
		},
		PermitTTL: 5 * time.Minute,
	}
	command := taskapplication.IssueTaskPermitCommand{
		TenantID:          "tenant-local",
		UserID:            "user-local",
		WorkerSessionID:   fixture.Started.WorkerSessionID,
		RuntimeGeneration: fixture.RuntimeGeneration,
		IdempotencyKey:    "permit-after-cancelled-task",
	}
	if _, err := dispatch.IssueTaskPermit(
		ctx,
		command,
	); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlPendingTurnSettlement,
	) {
		t.Fatalf("IssueTaskPermit pending settlement error=%v", err)
	}
	after := loadDispatchBarrierMutationCounts(t, pool)
	if after != before {
		t.Fatalf(
			"blocked IssueTaskPermit mutated state: before=%+v after=%+v",
			before,
			after,
		)
	}
	var queueStatus string
	if err := pool.QueryRow(ctx, `
SELECT status
FROM session_task_queue_entries
WHERE worker_session_id=$1 AND task_id=$2`,
		fixture.Started.WorkerSessionID,
		fixture.NextTaskID,
	).Scan(&queueStatus); err != nil {
		t.Fatal(err)
	}
	if queueStatus != "pending" {
		t.Fatalf("blocked IssueTaskPermit queue status=%s", queueStatus)
	}
	completePendingDispatchBarrierSettlement(t, pool, fixture)
	_, runtimeGeneration := reactivateDispatchBarrierSession(
		t,
		pool,
		fixture,
		now.Add(4*time.Minute),
	)
	command.RuntimeGeneration = runtimeGeneration
	permit, err := dispatch.IssueTaskPermit(ctx, command)
	if err != nil {
		t.Fatalf("IssueTaskPermit after settlement completion: %v", err)
	}
	if permit.WorkerSessionID != fixture.Started.WorkerSessionID ||
		permit.TaskID != fixture.NextTaskID {
		t.Fatalf("permit after settlement completion=%+v", permit)
	}
}

func TestStartWorkerRunWaitsForCancelledTaskTurnSettlementCompletion(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 6, 10, 0, 0, time.UTC)
	fixture := preparePendingDispatchBarrierFixture(t, pool, now)
	permit := seedPendingDispatchBarrierPermit(t, pool, fixture, now.Add(2*time.Minute))
	before := loadDispatchBarrierMutationCounts(t, pool)
	start := workerapplication.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool),
		IDs:   fixture.IDs,
		Now: func() time.Time {
			return now.Add(3 * time.Minute)
		},
	}
	command := residentStartCommand(permit)
	if _, err := start.Execute(
		ctx,
		command,
	); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlPendingTurnSettlement,
	) {
		t.Fatalf("StartWorkerRun pending settlement error=%v", err)
	}
	after := loadDispatchBarrierMutationCounts(t, pool)
	if after != before {
		t.Fatalf(
			"blocked StartWorkerRun mutated state: before=%+v after=%+v",
			before,
			after,
		)
	}
	var permitStatus, queueStatus string
	if err := pool.QueryRow(ctx, `
SELECT permit.status,queue.status
FROM session_dispatch_permits permit
JOIN session_task_queue_entries queue
  ON queue.worker_session_id=permit.worker_session_id
 AND queue.task_id=permit.target_id
WHERE permit.permit_id=$1`,
		permit.PermitID,
	).Scan(&permitStatus, &queueStatus); err != nil {
		t.Fatal(err)
	}
	if permitStatus != "active" || queueStatus != "permit_issued" {
		t.Fatalf(
			"blocked StartWorkerRun permit=%s queue=%s",
			permitStatus,
			queueStatus,
		)
	}
	completePendingDispatchBarrierSettlement(t, pool, fixture)
	sessionRevision, runtimeGeneration := reactivateDispatchBarrierSession(
		t,
		pool,
		fixture,
		now.Add(4*time.Minute),
	)
	if _, err := pool.Exec(ctx, `
UPDATE session_dispatch_permits
SET status='revoked'
WHERE permit_id=$1 AND status='active';
`,
		permit.PermitID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE session_task_queue_entries
SET status='pending'
WHERE worker_session_id=$1 AND task_id=$2 AND status='permit_issued'`,
		fixture.Started.WorkerSessionID,
		fixture.NextTaskID,
	); err != nil {
		t.Fatal(err)
	}
	dispatch := taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool),
		IDs:   fixture.IDs,
		Now: func() time.Time {
			return now.Add(5 * time.Minute)
		},
		PermitTTL: 5 * time.Minute,
	}
	reissued, err := dispatch.IssueTaskPermit(
		ctx,
		taskapplication.IssueTaskPermitCommand{
			TenantID:          "tenant-local",
			UserID:            "user-local",
			WorkerSessionID:   fixture.Started.WorkerSessionID,
			RuntimeGeneration: runtimeGeneration,
			IdempotencyKey:    "permit-after-settlement-completed",
		},
	)
	if err != nil {
		t.Fatalf("IssueTaskPermit after settlement completion: %v", err)
	}
	start.Now = func() time.Time { return now.Add(6 * time.Minute) }
	retryCommand := residentStartCommand(reissued)
	retryCommand.ExpectedSessionRevision = sessionRevision
	started, err := start.Execute(ctx, retryCommand)
	if err != nil {
		t.Fatalf("StartWorkerRun after settlement completion: %v", err)
	}
	if started.WorkerSessionID != fixture.Started.WorkerSessionID ||
		started.TaskID != fixture.NextTaskID {
		t.Fatalf("started after settlement completion=%+v", started)
	}
}

func reactivateDispatchBarrierSession(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture pendingDispatchBarrierFixture,
	now time.Time,
) (uint64, uint64) {
	t.Helper()
	var sessionRevision, runtimeGeneration uint64
	err := pool.QueryRow(context.Background(), `
UPDATE worker_sessions
SET status='idle',
    runtime_generation=runtime_generation+1,
    session_revision=session_revision+1,
    idle_expires_at=$2::timestamptz + interval '48 hours',
    updated_at=$2
WHERE worker_session_id=$1
  AND status='waiting_user'
  AND active_runtime_instance_id IS NULL
  AND active_worker_run_id IS NULL
  AND bound_task_id IS NULL
RETURNING session_revision,runtime_generation`,
		fixture.Started.WorkerSessionID,
		now,
	).Scan(&sessionRevision, &runtimeGeneration)
	if err != nil {
		t.Fatalf("reactivate dispatch barrier Session: %v", err)
	}
	return sessionRevision, runtimeGeneration
}

type pendingDispatchBarrierFixture struct {
	Started           workerapplication.StartedWorkerRun
	Settlement        workerapplication.TurnSettlement
	NextTaskID        string
	SessionRevision   uint64
	RuntimeGeneration uint64
	IDs               *concurrentAllocationIDs
}

func preparePendingDispatchBarrierFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
) pendingDispatchBarrierFixture {
	t.Helper()
	runtimecapacity.Initialize(t, pool)
	started, cleaned, deadlineAt :=
		prepareBackendDeadlineSourceForApply(
			t,
			pool,
			now,
			true,
		)
	settlement := seedBackendDeadlineCancelledTaskSettled(
		t,
		pool,
		started,
		cleaned,
		deadlineAt,
	)
	var (
		sessionStatus, boundTaskID, activeRunID string
		sessionRevision, runtimeGeneration      uint64
	)
	if err := pool.QueryRow(context.Background(), `
SELECT status,session_revision,runtime_generation,
       COALESCE(bound_task_id,''),COALESCE(active_worker_run_id,'')
FROM worker_sessions
WHERE worker_session_id=$1`,
		started.WorkerSessionID,
	).Scan(
		&sessionStatus,
		&sessionRevision,
		&runtimeGeneration,
		&boundTaskID,
		&activeRunID,
	); err != nil {
		t.Fatal(err)
	}
	if settlement.Phase !=
		workerapplication.TurnSettlementTaskSettled ||
		sessionStatus != "interactive" ||
		boundTaskID != "" ||
		activeRunID != "" {
		t.Fatalf(
			"dispatch barrier predecessor settlement=%+v session=%s/%s/%s",
			settlement,
			sessionStatus,
			boundTaskID,
			activeRunID,
		)
	}
	nextTaskID := "task-dispatch-after-cancelled"
	ids := &concurrentAllocationIDs{}
	ids.sequence.Store(100_000)
	seedQueuedDispatchBarrierTask(
		t,
		pool,
		nextTaskID,
		now.Add(time.Minute),
	)
	if _, err := (taskapplication.SessionDispatchService{
		Store: taskpostgres.NewStore(pool),
		IDs:   ids,
		Now: func() time.Time {
			return settlement.UpdatedAt.Add(time.Second)
		},
		PermitTTL: 5 * time.Minute,
	}).EnqueueTask(
		context.Background(),
		taskapplication.EnqueueTaskCommand{
			TenantID:                "tenant-local",
			UserID:                  "user-local",
			TaskID:                  nextTaskID,
			WorkerSessionID:         started.WorkerSessionID,
			ExpectedTaskRevision:    1,
			ExpectedSessionRevision: sessionRevision,
			IdempotencyKey:          "enqueue-" + nextTaskID,
		},
	); err != nil {
		t.Fatal(err)
	}
	return pendingDispatchBarrierFixture{
		Started:           started,
		Settlement:        settlement,
		NextTaskID:        nextTaskID,
		SessionRevision:   sessionRevision,
		RuntimeGeneration: runtimeGeneration,
		IDs:               ids,
	}
}

func seedPendingDispatchBarrierPermit(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture pendingDispatchBarrierFixture,
	now time.Time,
) taskapplication.IssuedTaskPermit {
	t.Helper()
	var queueSequence, queueRevision uint64
	if err := pool.QueryRow(context.Background(), `
SELECT session_queue_sequence,queue_revision
FROM session_task_queue_entries
WHERE worker_session_id=$1 AND task_id=$2 AND status='pending'`,
		fixture.Started.WorkerSessionID,
		fixture.NextTaskID,
	).Scan(&queueSequence, &queueRevision); err != nil {
		t.Fatal(err)
	}
	permitID := "session-dispatch-permit-pending-settlement"
	if _, err := pool.Exec(context.Background(), `
INSERT INTO session_dispatch_permits
  (permit_id,worker_session_id,target_kind,target_id,
   session_queue_sequence,queue_revision,expected_session_revision,
   runtime_generation,capacity_admission_ref,status,idempotency_key,
   expires_at,created_at)
VALUES ($1,$2,'task',$3,$4,$5,$6,$7,
  'capacity-after-cancelled-task','active',
  'permit-after-cancelled-task',$8,$9)`,
		permitID,
		fixture.Started.WorkerSessionID,
		fixture.NextTaskID,
		queueSequence,
		queueRevision,
		fixture.SessionRevision,
		fixture.RuntimeGeneration,
		now.Add(5*time.Minute),
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `
UPDATE session_task_queue_entries
SET status='permit_issued'
WHERE worker_session_id=$1 AND task_id=$2 AND status='pending'`,
		fixture.Started.WorkerSessionID,
		fixture.NextTaskID,
	); err != nil {
		t.Fatal(err)
	}
	return taskapplication.IssuedTaskPermit{
		PermitID:                permitID,
		WorkerSessionID:         fixture.Started.WorkerSessionID,
		TaskID:                  fixture.NextTaskID,
		SessionQueueSequence:    queueSequence,
		QueueRevision:           queueRevision,
		ExpectedSessionRevision: fixture.SessionRevision,
		RuntimeGeneration:       fixture.RuntimeGeneration,
	}
}

func completePendingDispatchBarrierSettlement(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture pendingDispatchBarrierFixture,
) {
	t.Helper()
	appliedAt := fixture.Settlement.UpdatedAt.Add(time.Second)
	applied, err := (workerapplication.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return appliedAt },
	}).ApplyBackendDeadline(
		context.Background(),
		workerapplication.ApplyBackendDeadlineSourceCommand{
			SettlementID:              fixture.Settlement.SettlementID,
			ExpectedRevision:          fixture.Settlement.Revision,
			ExpectedSourceDigest:      fixture.Settlement.Source.Digest,
			ExpectedSourceStateDigest: fixture.Settlement.Source.StateDigest,
			LeaseOwnerID:              fixture.Settlement.LeaseOwnerID,
			LeaseFence:                fixture.Settlement.LeaseFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase != workerapplication.TurnSettlementTerminalEventApplied {
		t.Fatalf("source applied settlement=%+v", applied)
	}
	completed := completeAppliedTerminalSettlement(
		t,
		pool,
		fixture.Started,
		appliedAt.Add(time.Second),
	)
	if completed.Phase != workerapplication.TurnSettlementCompleted {
		t.Fatalf("completed dispatch barrier settlement=%+v", completed)
	}
}

func seedQueuedDispatchBarrierTask(
	t *testing.T,
	pool *pgxpool.Pool,
	taskID string,
	now time.Time,
) {
	t.Helper()
	assessmentID := "assessment-" + taskID
	intentID := "intent-" + taskID
	digest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := pool.Exec(context.Background(), `
INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,
   workspace_revision_id,source,policy_revision,dimensions,
   blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ($1,'tenant-local','user-local','workspace-demo',
  'workspace-revision-1','api','direct-ready-v1','{}'::jsonb,
  '[]'::jsonb,'[]'::jsonb,'ready_confirmed',$2,$3);
`,
		assessmentID,
		digest,
		now,
	); err != nil {
		t.Fatalf("seed queued dispatch barrier assessment: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,
   workspace_revision_id,readiness_assessment_id,task_spec_digest,
   trigger_policy_revision,status,created_at,expires_at)
VALUES ($1,'tenant-local','user-local','workspace-demo',
  'workspace-revision-1',$2,$3,'direct-create-v1','active',$4,
  $4::timestamptz + interval '15 minutes')`,
		intentID,
		assessmentID,
		digest,
		now,
	); err != nil {
		t.Fatalf("seed queued dispatch barrier intent: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,
   workspace_revision_id,task_spec_snapshot,task_spec_digest,
   creation_intent_id,status,acceptance_mode,result_refs,
   revision,created_at,updated_at)
VALUES ($1,'tenant-local','user-local','workspace-demo',
  'workspace-revision-1','{}'::jsonb,$2,$3,'queued','manual',
  '[]'::jsonb,1,$4,$4)`,
		taskID,
		digest,
		intentID,
		now,
	); err != nil {
		t.Fatalf("seed queued dispatch barrier Task: %v", err)
	}
}

type dispatchBarrierMutationCounts struct {
	Permits  int
	Runs     int
	Runtimes int
	Outbox   int
}

func loadDispatchBarrierMutationCounts(
	t *testing.T,
	pool *pgxpool.Pool,
) dispatchBarrierMutationCounts {
	t.Helper()
	var counts dispatchBarrierMutationCounts
	for table, target := range map[string]*int{
		"session_dispatch_permits": &counts.Permits,
		"worker_runs":              &counts.Runs,
		"runtime_instances":        &counts.Runtimes,
		"outbox_events":            &counts.Outbox,
	} {
		if err := pool.QueryRow(
			context.Background(),
			fmt.Sprintf("SELECT count(*) FROM %s", table),
		).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func issuePermitConcurrently(
	t *testing.T,
	service taskapplication.SessionDispatchService,
	sessionID string,
	concurrency int,
) []taskapplication.IssuedTaskPermit {
	t.Helper()
	results := make(chan taskapplication.IssuedTaskPermit, concurrency)
	errorsFound := make(chan error, concurrency)
	var wait sync.WaitGroup
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.IssueTaskPermit(context.Background(), taskapplication.IssueTaskPermitCommand{
				TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: sessionID,
				RuntimeGeneration: 1, IdempotencyKey: "permit-task-01",
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
			t.Fatalf("issue permit concurrently: %v", err)
		}
	}
	collected := make([]taskapplication.IssuedTaskPermit, 0, concurrency)
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func startRunConcurrently(
	t *testing.T,
	service workerapplication.StartWorkerRunService,
	permit taskapplication.IssuedTaskPermit,
	concurrency int,
) []workerapplication.StartedWorkerRun {
	t.Helper()
	results := make(chan workerapplication.StartedWorkerRun, concurrency)
	errorsFound := make(chan error, concurrency)
	var wait sync.WaitGroup
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.Execute(context.Background(), workerapplication.StartWorkerRunCommand{
				TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: permit.WorkerSessionID,
				TaskID: permit.TaskID, PermitID: permit.PermitID,
				ExpectedSessionRevision: permit.ExpectedSessionRevision,
				RuntimeGeneration:       permit.RuntimeGeneration, TriggerType: "task_initial",
				StartSnapshot:  []byte(`{"working_copy":"checkpoint-1"}`),
				AgentProfileID: "deepseek-codex-openai-v1", AgentProfileRevision: "1",
				AgentProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				AgentTurnContent:   []byte(`{"text":"execute task"}`),
				TurnIdempotencyKey: "turn/" + permit.PermitID,
				RuntimeImageRef:    "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ResourceProfileID:  "local_standard_v1", NetworkPolicyID: "m1_public_web_restricted_v1",
				NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1,
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
			t.Fatalf("start Run concurrently: %v", err)
		}
	}
	collected := make([]workerapplication.StartedWorkerRun, 0, concurrency)
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}
