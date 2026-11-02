# Initial Runtime Ownership Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a newly started WorkerRun own its generation-1 RuntimeInstance before durable WorkerControl credential registration, while preserving exact Runtime ownership during activation.

**Architecture:** `StartWorkerRun` remains the transaction owner for the initial WorkerSession execution slot and now binds the new Runtime identity in that same transaction. The durable Runtime control journal remains unchanged and continues to reject every identity that is not the Session's exact current Runtime. Runtime activation changes from attaching an unowned Runtime to compare-and-swapping the already owned Runtime into `active`.

**Tech Stack:** Go 1.x, PostgreSQL, pgx v5, Docker Compose, Go integration tests.

## Global Constraints

- The initial generation-1 Runtime must become the WorkerSession's current Runtime before filesystem, credential, Docker, or network side effects.
- Durable WorkerControl identity checks remain fail closed for exact WorkerSession, RuntimeInstance, generation, and ChannelEpoch.
- A WorkerSession with an existing current Runtime cannot start another initial Runtime.
- Runtime activation cannot replace or steal the current Runtime pointer.
- Startup failure preserves the failed Runtime, WorkerRun, AgentTurn, Task, diagnostics, and fencing evidence.
- Runtime Recovery generation replacement is outside this change.
- Preserve unrelated uncommitted `web-demo` changes.

---

### Task 1: Reproduce and establish initial Runtime ownership

**Files:**
- Modify: `internal/workerruntime/adapter/postgres/runtime_residency_integration_test.go`
- Modify: `internal/workerruntime/adapter/postgres/start_run.go`

**Interfaces:**
- Consumes: `application.StartWorkerRunService.Execute`, `application.RuntimeControlJournalService.Bind`, and `runtimeControlBind`.
- Produces: `RunStore.StartWorkerRun` atomically returns a `StartedWorkerRun` whose Runtime is already the WorkerSession's exact current Runtime.

- [ ] **Step 1: Add the failing control-bind regression test**

Add this integration test before `TestStageCompletionKeepsGenerationOneRuntimeResident`:

```go
func TestInitialRuntimeOwnershipAllowsControlBindBeforeActivation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
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
	control := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(5 * time.Minute) },
	}
	bound, err := control.Bind(ctx, runtimeControlBind(started))
	if err != nil {
		t.Fatalf("bind initial Runtime control channel: %v", err)
	}
	if bound.WorkerSessionID != started.WorkerSessionID ||
		bound.RuntimeInstanceID != started.RuntimeInstanceID ||
		bound.RuntimeGeneration != started.RuntimeGeneration ||
		bound.ChannelEpoch != started.ControlChannelEpoch {
		t.Fatalf("bound control identity=%+v started=%+v", bound, started)
	}
}
```

- [ ] **Step 2: Run the regression test and confirm the current failure**

Run:

```bash
go test -tags=integration ./internal/workerruntime/adapter/postgres \
  -run '^TestInitialRuntimeOwnershipAllowsControlBindBeforeActivation$' -count=1
```

Expected: FAIL with `Runtime control identity conflict`.

- [ ] **Step 3: Make the initial ownership transaction atomic**

In `runSession`, add the current Runtime identity:

```go
type runSession struct {
	TenantID          string
	UserID            string
	Status            string
	Revision          uint64
	RuntimeGeneration uint64
	BoundTaskID       string
	ActiveRunID       string
	ActiveRuntimeID   string
}
```

Lock and decode it with the existing Session facts:

```go
var boundTaskID, activeRunID, activeRuntimeID *string
err := tx.QueryRow(ctx, `
SELECT tenant_id,user_id,status,session_revision,runtime_generation,bound_task_id,
       active_worker_run_id,active_runtime_instance_id
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, sessionID).Scan(
	&session.TenantID, &session.UserID, &session.Status, &session.Revision,
	&session.RuntimeGeneration, &boundTaskID, &activeRunID, &activeRuntimeID)
```

Decode the optional value and include it in the execution-slot guard:

```go
if activeRuntimeID != nil {
	session.ActiveRuntimeID = *activeRuntimeID
}
```

```go
if session.ActiveRunID != "" || session.BoundTaskID != "" ||
	session.ActiveRuntimeID != "" {
	return ErrSessionBusy
}
```

Replace the WorkerSession update with:

```go
err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET bound_task_id=$3,active_worker_run_id=$4,active_runtime_instance_id=$5,
    status='running',session_revision=session_revision+1,updated_at=$6
WHERE worker_session_id=$1 AND session_revision=$2
  AND active_worker_run_id IS NULL AND bound_task_id IS NULL
  AND active_runtime_instance_id IS NULL
RETURNING session_revision`, record.Command.WorkerSessionID, session.Revision,
	record.Command.TaskID, record.WorkerRunID, record.RuntimeInstanceID,
	record.Now).Scan(&sessionRevision)
```

- [ ] **Step 4: Run the focused control-bind test**

Run:

```bash
go test -tags=integration ./internal/workerruntime/adapter/postgres \
  -run '^TestInitialRuntimeOwnershipAllowsControlBindBeforeActivation$' -count=1
```

Expected: PASS.

### Task 2: Preserve exact ownership during Runtime activation

**Files:**
- Modify: `internal/workerruntime/adapter/postgres/runtime_residency.go`
- Test: `internal/workerruntime/adapter/postgres/runtime_residency_integration_test.go`

**Interfaces:**
- Consumes: `application.RuntimeResidencyService.Activate`.
- Produces: activation changes only the already current Runtime's lifecycle and advances the Session revision without rebinding ownership.

- [ ] **Step 1: Run the existing residency test against the new start ownership**

Run:

```bash
go test -tags=integration ./internal/workerruntime/adapter/postgres \
  -run '^TestStageCompletionKeepsGenerationOneRuntimeResident$' -count=1
```

Expected: FAIL while the old activation SQL still requires `active_runtime_instance_id IS NULL`.

- [ ] **Step 2: Change activation from attach to exact-owner CAS**

Replace the WorkerSession activation update with:

```go
var sessionRevision uint64
err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND session_revision=$4 AND status='running'
  AND active_worker_run_id=$5 AND active_runtime_instance_id=$2
RETURNING session_revision`, record.Command.WorkerSessionID, record.Command.RuntimeInstanceID,
	record.Now, record.Command.ExpectedSessionRevision, record.Command.WorkerRunID).Scan(&sessionRevision)
if errors.Is(err, pgx.ErrNoRows) {
	return platformpostgres.ErrRevisionConflict
}
if err != nil {
	return fmt.Errorf("advance resident Runtime ownership: %w", err)
}
```

- [ ] **Step 3: Run the residency test**

Run:

```bash
go test -tags=integration ./internal/workerruntime/adapter/postgres \
  -run '^TestStageCompletionKeepsGenerationOneRuntimeResident$' -count=1
```

Expected: PASS for `interactive`, `waiting user`, and `idle`.

### Task 3: Lock negative ownership behavior and verify the product path

**Files:**
- Modify: `internal/workerruntime/adapter/postgres/dispatch_integration_test.go`
- Verify: `internal/workerruntime/adapter/postgres/runtime_control_journal_integration_test.go`
- Verify: `internal/workerruntime/adapter/postgres/recovery_integration_test.go`

**Interfaces:**
- Consumes: the public dispatch/start service and the existing PostgreSQL integration fixture.
- Produces: regression coverage that a current Runtime blocks a second initial Runtime even when the Task and Run slot fields are empty.

- [ ] **Step 1: Add the existing-current-Runtime rejection test**

Add a fixture with no historical Run or pending Turn settlement, but with an existing current Runtime, then assert `StartWorkerRun` returns `ErrSessionBusy` before it inserts a second Runtime:

```go
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
	RuntimeGeneration: 1, CapacityAdmissionRef: "capacity-local",
	IdempotencyKey: "permit-task-01",
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
```

- [ ] **Step 2: Run the focused PostgreSQL package tests**

Run:

```bash
go test -tags=integration ./internal/workerruntime/adapter/postgres \
  -run 'InitialRuntimeOwnership|SessionFIFOAndSingleUsePermit|RuntimeResidency|RuntimeControlJournal|RuntimeRecovery' \
  -count=1
```

Expected: PASS.

- [ ] **Step 3: Run application and repository gates**

Run:

```bash
go test ./internal/workerruntime/application -count=1
make verify
```

Expected: both commands exit 0.

- [ ] **Step 4: Commit the atomic behavior change**

```bash
git add -- \
  internal/workerruntime/adapter/postgres/start_run.go \
  internal/workerruntime/adapter/postgres/runtime_residency.go \
  internal/workerruntime/adapter/postgres/runtime_residency_integration_test.go \
  internal/workerruntime/adapter/postgres/dispatch_integration_test.go
git diff --cached --check
git commit -m "fix: bind initial Runtime ownership before startup"
```

- [ ] **Step 5: Rebuild Compose and execute a new Worker Task**

Use the repository Compose entrypoint to rebuild and restart the affected backend/frontend services. Create a new hello-world Task through the local API or UI; do not rewrite the historical failed Task.

Acceptance evidence:

```text
WorkerSession status != lost
WorkerRun status != failed
AgentTurn status != failed
Runtime container_id is non-empty
Runtime status = active
runtime_control_channels has the exact Session/Runtime/generation/ChannelEpoch row
Worker detail no longer displays "Not started"
```

### Task 4: Hand off durable `runtime.ready` revisions to AgentSession binding

**Files:**
- Modify: `internal/localproduct/runtime_runner.go`
- Add: `internal/localproduct/runtime_runner_startup_revision_integration_test.go`

**Interfaces:**
- Consumes: the durable WorkerControl journal after it accepts `runtime.ready`.
- Produces: an AgentSession binding command with the current exact Session, Run, Turn, and Runtime revisions.

- [ ] **Step 1: Reproduce the post-ready revision conflict**

Seed the startup aggregates after durable `runtime.ready` acceptance, including
`runtime_instances.revision=2`, while the other aggregate revisions remain at
their post-start values.

Run:

```bash
go test -v -tags=integration ./internal/localproduct \
  -run '^TestBindInitialAgentSessionUsesPostRuntimeReadyRevisions$' -count=1
```

Expected before the fix: FAIL with `aggregate revision conflict`.

- [ ] **Step 2: Load the exact current startup revisions**

After receiving the already durable `runtime.ready` event, read the four
aggregate revisions only when all identities, current ownership pointers,
generation, and startup statuses still match. Keep the binding store's exact
compare-and-swap unchanged.

- [ ] **Step 3: Bound transient revision races**

If the binding compare-and-swap loses a transient revision race, reload the
same exact startup state and retry at most three times. Do not retry missing,
mismatched, or non-startup aggregate state.

- [ ] **Step 4: Verify the regression and product path**

Run:

```bash
go test -v -tags=integration ./internal/localproduct \
  -run '^TestBindInitialAgentSessionUsesPostRuntimeReadyRevisions$' -count=1
make verify
```

Then rebuild `switchback-backend` and create a new UI Task. The historical
failed Tasks remain immutable evidence.

### Task 5: Reconcile terminal settlement with Runtime-control revisions

**Files:**
- Modify: `internal/localproduct/runtime_settlement.go`
- Add: `internal/localproduct/runtime_settlement_domain_revision_integration_test.go`
- Modify: `internal/workerruntime/application/turn_settlement_checkpoint.go`
- Modify: `internal/workerruntime/application/turn_settlement_test.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement_domain.go`
- Modify: `internal/workerruntime/adapter/postgres/turn_settlement_integration_test.go`

**Interfaces:**
- Consumes: a durable terminal settlement plus the current exact resident
  Session/Run/Turn/Runtime graph.
- Produces: a frozen domain plan based on post-terminal graph revisions and an
  atomic proof that records benign Runtime-control cursor revision advances.

- [ ] **Step 1: Reproduce the stale Turn graph**

Run:

```bash
go test -v -tags=integration ./internal/localproduct \
  -run '^TestRuntimeSettlementDomainPlanUsesPostTerminalGraphRevisions$' \
  -count=1
```

Expected before the fix: FAIL because the plan retains the pre-dispatch Turn
revision and the pre-execution Runtime revision.

- [ ] **Step 2: Reproduce Runtime cursor drift during settlement**

Run:

```bash
go test -v -tags=integration ./internal/workerruntime/adapter/postgres \
  -run '^TestTurnSettlementDomainCommitAllowsRuntimeControlCursorRevisionAdvance$' \
  -count=1
```

Expected before the fix: FAIL with `aggregate revision conflict`.

- [ ] **Step 3: Refresh the post-terminal graph before freezing**

Resolve the current graph only when Tenant, Session, Run, Turn, AgentSession,
current Runtime pointer, generation, and current Run pointer still match the
durable settlement identity. Freeze those current revisions instead of the
pre-dispatch `pendingTurn` revisions.

- [ ] **Step 4: Admit only benign Runtime-control revision drift**

During the atomic domain commit, lock the exact graph and require the same
Runtime identity, generation, ChannelEpoch, active lifecycle state, and exact
Session/Run/Turn revisions. A Runtime revision greater than the frozen value
is allowed only for a Runtime-event settlement; the resulting proof records
the locked current Runtime revision. Backend-deadline settlement keeps its
existing exact revision semantics.

- [ ] **Step 5: Verify full delivery**

Run the focused tests, affected package suites, and `make verify`. Rebuild the
backend and create another new Task. Acceptance requires the Worker to execute,
produce report and patch artifacts, and converge the Run/Turn/Task settlement
without a repeating revision-conflict loop.

### Task 6: Restore a terminal settlement after later heartbeat commits

**Files:**
- Modify: `internal/workerruntime/adapter/postgres/durable_terminal_event.go`
- Modify: `internal/workerruntime/adapter/postgres/durable_terminal_event_loader_integration_test.go`

**Interfaces:**
- Consumes: the immutable terminal message and a Runtime-control channel whose
  committed event cursor may have advanced beyond that terminal sequence.
- Produces: the exact durable terminal delivery while independently proving
  both the terminal cursor and the channel's current committed cursor.

- [ ] **Step 1: Reproduce startup restore after a later heartbeat**

Run:

```bash
go test -v -tags=integration ./internal/workerruntime/adapter/postgres \
  -run '^TestDurableTerminalEventLoaderAllowsCommittedHeartbeatAfterTerminal$' \
  -count=1
```

Expected before the fix: FAIL with `Runtime control message conflict`.

- [ ] **Step 2: Validate both cursor checkpoints**

Require the channel committed sequence to be at least the terminal sequence.
Resolve and verify the stored cursor digest at the terminal sequence against
the terminal message, then separately resolve and verify the digest at the
channel's current committed sequence. Do not compare a later committed digest
directly with the earlier terminal digest.

- [ ] **Step 3: Verify restart recovery**

Run all durable terminal loader tests and repository gates, rebuild the
backend, and confirm the pending settlement crosses the startup barrier before
the newly queued Task executes.

### Task 7: Restore live Runtime control authority after transient secret cleanup

**Files:**
- Modify: `internal/workerruntime/adapter/runtimesecret/materializer.go`
- Modify: `internal/workerruntime/adapter/runtimesecret/materializer_test.go`
- Modify: `internal/localproduct/runtime_startup.go`
- Modify: `internal/localproduct/runtime_startup_restore_test.go`

**Interfaces:**
- Consumes: a live Runtime whose Docker secret volume survived Backend restart,
  while its host-side provisioning inputs were intentionally deleted.
- Produces: the exact original WorkerControl credential from a Backend-only
  escrow, or one bounded Runtime recovery for pre-escrow generations.

- [x] **Step 1: Reproduce restoration after transient input deletion**

Delete `secret-input` after materialization and prove `OpenLiveExisting` and
`RestoreLiveRuntime` fail before the fix.

- [x] **Step 2: Persist only Backend control authority**

Store a root-only, non-mounted control credential under the Runtime resource
root. Do not retain the provider credential or make the deleted Docker staging
inputs durable.

- [x] **Step 3: Recover pre-escrow Runtime generations**

Classify a missing live credential separately from malformed or stale
credentials. Inspect the exact surviving container and open the existing
single-attempt recovery path with `heartbeat_unhealthy`; do not crash the
Backend startup loop or silently rotate authority for the old container.

- [x] **Step 4: Verify product recovery**

Run affected package tests and `make verify`, rebuild Compose, prove the
historical Runtime is replaced or settled without a restart loop, and confirm a
new Task reaches a real Worker report/patch terminal result.

Evidence: `make verify` passed; Compose Backend stayed healthy with restart
count zero; `task-74aa4bca91b926db505a531cd07734bb` reached `delivered`, its
Run/Turn and settlement reached `completed`, and the Worker detail rendered one
complete report plus a one-file `+21/-0` patch. The pre-escrow Runtime and the
post-delivery disconnected Session converged to `lost` without downgrading the
already-delivered Task.
