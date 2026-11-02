//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapplication "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestExactSessionReuseRequiresIntentAndPreservesBusySessionState(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
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
UPDATE worker_sessions
SET status='running',runtime_generation=1,session_revision=2,bound_task_id='task-01',
    active_worker_run_id='run-existing',updated_at=$2
WHERE worker_session_id=$1`, allocated.WorkerSessionID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var originalWorkingCopy string
	if err := pool.QueryRow(ctx, `SELECT working_copy_id FROM worker_sessions WHERE worker_session_id=$1`,
		allocated.WorkerSessionID).Scan(&originalWorkingCopy); err != nil {
		t.Fatal(err)
	}
	taskStore := taskpostgres.NewStore(pool)

	freshAdmission := admitReuseTask(t, ids, "Fresh task", now.Add(3*time.Minute))
	if err := taskStore.RecordDirectAdmission(ctx, freshAdmission); err != nil {
		t.Fatal(err)
	}
	fresh, err := (taskapplication.CreateTaskService{
		Store: taskStore, IDs: ids, Now: func() time.Time { return now.Add(4 * time.Minute) },
	}).Execute(ctx, taskapplication.CreateTaskCommand{
		Actor:    taskapplication.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: freshAdmission.TaskSpec, AcceptanceMode: "manual", IdempotencyKey: "create-fresh-with-existing-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.WorkerSessionID != "" || fresh.AllocationRequestID == "" || fresh.SessionAllocationSequence == 0 {
		t.Fatalf("Task without target inferred existing Session: %+v", fresh)
	}

	targetedAdmission := admitReuseTask(t, ids, "Targeted task", now.Add(5*time.Minute))
	if err := taskStore.RecordDirectAdmission(ctx, targetedAdmission); err != nil {
		t.Fatal(err)
	}
	targetService := taskapplication.SessionTargetIntentService{
		Store: taskStore, IDs: ids, Now: func() time.Time { return now.Add(6 * time.Minute) },
	}
	baseTarget := taskapplication.CreateSessionTargetIntentCommand{
		Actor:       taskapplication.Actor{TenantID: "tenant-local", UserID: "user-local"},
		WorkspaceID: "workspace-demo", WorkerSessionID: allocated.WorkerSessionID,
		TaskSpecDigest: targetedAdmission.Intent.TaskSpecDigest,
		SourceRef:      "worker-detail/" + allocated.WorkerSessionID, SourceRevision: 1,
		BusyPolicy: "queue", IdempotencyKey: "target-busy-session",
	}
	invalidInferred := baseTarget
	invalidInferred.WorkerSessionID = ""
	if _, err := targetService.Create(ctx, invalidInferred); !errors.Is(err, taskapplication.ErrSessionReuseInvalid) {
		t.Fatalf("inferred target error=%v", err)
	}
	crossWorkspace := baseTarget
	crossWorkspace.WorkspaceID = "workspace-other"
	crossWorkspace.IdempotencyKey = "target-cross-workspace"
	if _, err := targetService.Create(ctx, crossWorkspace); !errors.Is(err, taskapplication.ErrSessionReuseInvalid) {
		t.Fatalf("cross-Workspace target error=%v", err)
	}
	interruptPolicy := baseTarget
	interruptPolicy.BusyPolicy = "interrupt"
	interruptPolicy.IdempotencyKey = "target-interrupt"
	if _, err := targetService.Create(ctx, interruptPolicy); !errors.Is(err, taskapplication.ErrSessionReuseInvalid) {
		t.Fatalf("busy interrupt policy error=%v", err)
	}

	targetIntent, err := targetService.Create(ctx, baseTarget)
	if err != nil {
		t.Fatal(err)
	}
	replayedIntent, err := targetService.Create(ctx, baseTarget)
	if err != nil || replayedIntent.IntentID != targetIntent.IntentID {
		t.Fatalf("target intent replay=%+v error=%v", replayedIntent, err)
	}
	targeted, err := (taskapplication.CreateTaskService{
		Store: taskStore, IDs: ids, Now: func() time.Time { return now.Add(7 * time.Minute) },
	}).Execute(ctx, taskapplication.CreateTaskCommand{
		Actor:    taskapplication.Actor{TenantID: "tenant-local", UserID: "user-local"},
		TaskSpec: targetedAdmission.TaskSpec, AcceptanceMode: "manual",
		WorkerSessionTargetIntentID: targetIntent.IntentID, IdempotencyKey: "create-targeted-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if targeted.WorkerSessionID != allocated.WorkerSessionID || targeted.SessionQueueSequence == 0 || targeted.AllocationRequestID != "" {
		t.Fatalf("targeted CreateTask result=%+v", targeted)
	}
	var requestedSession, currentSession, contextPolicy, targetStatus, workingCopy string
	var allocationSequence *uint64
	if err := pool.QueryRow(ctx, `
SELECT requested_worker_session_id,worker_session_id,session_context_policy,session_allocation_sequence
FROM tasks WHERE task_id=$1`, targeted.TaskID).Scan(
		&requestedSession, &currentSession, &contextPolicy, &allocationSequence); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM worker_session_target_intents WHERE intent_id=$1`,
		targetIntent.IntentID).Scan(&targetStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT working_copy_id FROM worker_sessions WHERE worker_session_id=$1`,
		allocated.WorkerSessionID).Scan(&workingCopy); err != nil {
		t.Fatal(err)
	}
	if requestedSession != allocated.WorkerSessionID || currentSession != allocated.WorkerSessionID ||
		contextPolicy != "inherit_current" || allocationSequence != nil || targetStatus != "consumed" ||
		workingCopy != originalWorkingCopy {
		t.Fatalf("reuse requested=%s current=%s policy=%s allocation=%v target=%s working_copy=%s",
			requestedSession, currentSession, contextPolicy, allocationSequence, targetStatus, workingCopy)
	}

	for _, terminalStatus := range []string{"closed", "expired"} {
		if _, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status=$2,session_revision=session_revision+1,terminal_at=$3,terminal_reason='test',updated_at=$3
WHERE worker_session_id=$1`, allocated.WorkerSessionID, terminalStatus, now.Add(8*time.Minute)); err != nil {
			t.Fatal(err)
		}
		command := baseTarget
		command.IdempotencyKey = "target-terminal-" + terminalStatus
		if _, err := targetService.Create(ctx, command); !errors.Is(err, taskapplication.ErrSessionReuseInvalid) {
			t.Fatalf("terminal %s target error=%v", terminalStatus, err)
		}
	}
}

func admitReuseTask(
	t *testing.T,
	ids taskapplication.IDGenerator,
	title string,
	now time.Time,
) taskapplication.DirectAdmissionResult {
	t.Helper()
	networkProfile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (taskapplication.DirectAdmission{
		IDs: ids, Now: func() time.Time { return now },
		Policy: taskapplication.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1", TriggerPolicyRevision: "direct-create-v1",
			NetworkProfile: networkProfile,
		},
	}).Admit(taskapplication.DirectAdmissionCommand{
		Actor: taskapplication.Actor{TenantID: "tenant-local", UserID: "user-local"},
		Workspace: taskapplication.WorkspaceRevisionEvidence{
			TenantID: "tenant-local", WorkspaceID: "workspace-demo", WorkspaceRevisionID: "workspace-revision-1",
			ResolutionID: "resolution-1", ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
		AgentProfile: taskapplication.AgentProfileEvidence{
			ProfileID: "deepseek-codex-openai-v1", Revision: "1", Enabled: true,
		},
		Request: taskapplication.DirectTaskRequest{
			ProjectID: "project-switchback", Title: title, Goal: title,
			TargetKind: "workspace", TargetRef: "workspace-revision-1", Constraints: []string{},
			AcceptanceCriteria: []string{"Report and patch are reviewable"}, AcceptanceMode: "manual",
			UserVisibleTypes: []string{"report", "patch"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
