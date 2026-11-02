//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestGenerationOneRuntimeLossFencesWithoutImplicitReplacement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 13, 0, 0, 0, time.UTC)
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
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2
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
		RuntimeGeneration: 1, CapacityAdmissionRef: "capacity-local", IdempotencyKey: "permit-task-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := (workerapplication.StartWorkerRunService{
		Store: workerpostgres.NewRunStore(pool), IDs: ids,
		Now: func() time.Time { return now.Add(4 * time.Minute) },
	}).Execute(ctx, residentStartCommand(permit))
	if err != nil {
		t.Fatal(err)
	}
	active, err := (workerapplication.RuntimeResidencyService{
		Store: workerpostgres.NewRuntimeResidencyStore(pool),
		Now:   func() time.Time { return now.Add(5 * time.Minute) },
	}).Activate(ctx, workerapplication.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "container-resident-1",
		RuntimeGeneration: 1, ControlChannelEpoch: 1,
		ExpectedSessionRevision: started.SessionRevision,
		ExpectedRunRevision:     1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalResultRefs := json.RawMessage(`[{"artifact_ref":"artifact/report-existing"}]`)
	if _, err := pool.Exec(ctx, `UPDATE tasks SET result_refs=$1 WHERE task_id='task-01'`,
		originalResultRefs); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_dispatch_permits
  (permit_id,worker_session_id,target_kind,target_id,queue_revision,expected_session_revision,
   runtime_generation,capacity_admission_ref,status,idempotency_key,expires_at,created_at)
VALUES ('permit-late',$1,'standalone_run','late-run',1,$2,1,'capacity-late','active',
  'permit-late',$3,$4)`, started.WorkerSessionID, active.SessionRevision,
		now.Add(10*time.Minute), now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	provisioner := &fake.RuntimeProvisioner{Inspection: workerapplication.RuntimeInspection{
		Exists: false, Running: false, ContainerID: "container-resident-1", Generation: 1,
		ExitCode: intPointer(137), ObservedAt: now.Add(7 * time.Minute),
	}}
	control := &fake.WorkerControl{}
	loss := workerapplication.RuntimeLossService{
		Store: workerpostgres.NewRuntimeLossStore(pool), Provisioner: provisioner, Control: control,
		IDs: ids, Now: func() time.Time { return now.Add(7 * time.Minute) },
	}
	command := workerapplication.ObserveRuntimeLossCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID, ExpectedContainerID: "container-resident-1",
		RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey:          "runtime-loss/" + started.RuntimeInstanceID + "/g1",
		ProvenTraceRefs:         []string{"trace/segment-1"},
		ProvenArtifactRefs:      []string{"artifact/report-existing"},
	}
	result, err := loss.Observe(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Lost || result.State.SessionStatus != "lost" || result.State.RuntimeStatus != "lost" ||
		result.State.RunStatus != "failed" || result.State.TurnStatus != "failed" ||
		result.State.TaskStatus != "waiting_user" {
		t.Fatalf("runtime loss result=%+v", result)
	}
	replayed, err := loss.Observe(ctx, command)
	if err != nil || !replayed.Lost || replayed.State.RuntimeInstanceID != result.State.RuntimeInstanceID {
		t.Fatalf("runtime loss replay=%+v error=%v", replayed, err)
	}
	assertAllocationCount(t, pool, "runtime_instances", 1)
	var generation uint64
	var runtimeStatus, sessionStatus, runStatus, turnStatus, taskStatus, permitStatus string
	var activeRuntimeID, activeRunID string
	var persistedResultRefs, diagnostic json.RawMessage
	if err := pool.QueryRow(ctx, `SELECT generation,status,loss_diagnostic FROM runtime_instances
WHERE runtime_instance_id=$1`, started.RuntimeInstanceID).Scan(&generation, &runtimeStatus, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,active_runtime_instance_id,active_worker_run_id
FROM worker_sessions WHERE worker_session_id=$1`, started.WorkerSessionID).Scan(
		&sessionStatus, &activeRuntimeID, &activeRunID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM worker_runs WHERE worker_run_id=$1`,
		started.WorkerRunID).Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM agent_turns WHERE agent_turn_id=$1`,
		started.AgentTurnID).Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,result_refs FROM tasks WHERE task_id='task-01'`).Scan(
		&taskStatus, &persistedResultRefs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM session_dispatch_permits WHERE permit_id='permit-late'`).Scan(
		&permitStatus); err != nil {
		t.Fatal(err)
	}
	if generation != 1 || runtimeStatus != "lost" || sessionStatus != "lost" ||
		activeRuntimeID != started.RuntimeInstanceID || activeRunID != started.WorkerRunID ||
		runStatus != "failed" || turnStatus != "failed" || taskStatus != "waiting_user" ||
		permitStatus != "revoked" || !sameJSON(persistedResultRefs, originalResultRefs) ||
		!json.Valid(diagnostic) {
		t.Fatalf("loss convergence generation=%d runtime=%s session=%s active_runtime=%s active_run=%s run=%s turn=%s task=%s permit=%s refs=%s diagnostic=%s",
			generation, runtimeStatus, sessionStatus, activeRuntimeID, activeRunID, runStatus,
			turnStatus, taskStatus, permitStatus, persistedResultRefs, diagnostic)
	}
	if len(provisioner.ProvisionRequests) != 0 || len(provisioner.CleanupRequests) != 0 || len(control.Fences) != 2 {
		t.Fatalf("runtime loss side effects provision=%d cleanup=%v fences=%v",
			len(provisioner.ProvisionRequests), provisioner.CleanupRequests, control.Fences)
	}
	resultContract := failurematrix.Result{
		Variant: "runtime-loss", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
		Assertions: []string{"generation_fenced", "implicit_replacement_absent", "task_waiting_user"},
	}
	failurematrix.AssertAndRecord(t, resultContract, resultContract)
}

func intPointer(value int) *int { return &value }

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}
