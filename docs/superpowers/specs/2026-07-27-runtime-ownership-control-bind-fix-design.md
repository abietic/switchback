# Initial Runtime Ownership And Control Bind Fix

Date: 2026-07-27

## Problem

A newly dispatched Task commits a generation-1 `RuntimeInstance` with
`status=starting`, but the same transaction leaves
`worker_sessions.active_runtime_instance_id` empty. Before Docker creates the
container, the Runtime environment registers its WorkerControl credential.
The durable Runtime control journal correctly rejects that registration
because its identity fence requires the Session's current Runtime identity to
match the credential identity.

The resulting production path is:

1. `StartWorkerRun` commits the Task, WorkerSession, WorkerRun, AgentTurn and
   RuntimeInstance owners.
2. `RuntimeEnvironment.PrepareRuntime` registers the generation-scoped
   WorkerControl credential.
3. `RuntimeControlJournal.BindRuntimeControlChannel` sees an empty
   `active_runtime_instance_id` and returns
   `Runtime control identity conflict`.
4. Startup reconciliation records the Runtime as `lost`, fails the Run and
   Turn with `runtime_lost`, and moves the Task to `waiting_user`.
5. No Worker container is created.

This is an ownership-ordering defect. It is independent of the selected model,
provider credential, frontend rendering and the Runtime Recovery feature flag.

## Decision

The initial `StartWorkerRun` transaction SHALL bind the newly created
`RuntimeInstance` as the WorkerSession's current Runtime owner.

The transaction will atomically establish:

- `worker_sessions.bound_task_id`
- `worker_sessions.active_worker_run_id`
- `worker_sessions.active_runtime_instance_id`
- `worker_sessions.status=running`
- the generation-1 `runtime_instances` row with `status=starting`
- the initial WorkerRun, AgentTurn, deadline, queue and dispatch-permit facts

`RuntimeResidency.ActivateResidentRuntime` will no longer attach a previously
unowned Runtime. It will compare-and-swap the already owned Runtime from
`starting` to `active`, persist the Docker container identity, advance the
WorkerRun to `running`, and advance the Session revision while preserving the
same `active_runtime_instance_id`.

## Invariants

1. A WorkerSession has at most one current Runtime identity.
2. A generation-1 Runtime becomes the current owner before any filesystem,
   secret, credential, Docker or network side effect.
3. Durable WorkerControl credential binding is allowed only for the exact
   current Runtime, generation and ChannelEpoch.
4. Runtime activation cannot replace or steal the current Runtime pointer.
5. A Session that already has a current Runtime cannot start another initial
   Runtime through `StartWorkerRun`.
6. Startup failure preserves the failed Runtime identity and diagnostics for
   reconciliation; it does not rewrite or delete the historical Session,
   Run, Turn or Task facts.
7. Existing Runtime Recovery generation replacement remains owned by its
   recovery transaction and is not changed by this fix.

## Implementation Scope

### Start ownership transaction

Update the PostgreSQL `StartWorkerRun` store to:

- read the existing current Runtime pointer while locking the Session;
- reject initial Runtime creation when that pointer is already populated;
- set `active_runtime_instance_id` to the newly inserted Runtime identity in
  the same transaction that consumes the dispatch permit and occupies the
  WorkerSession execution slot;
- preserve current revision and idempotency behavior.

### Runtime activation

Update `ActivateResidentRuntime` to:

- require `active_runtime_instance_id` to equal the command's exact
  `RuntimeInstanceID`;
- transition the existing Runtime row to `active` and persist `container_id`;
- increment the Session revision without rebinding the Runtime pointer;
- fail with a revision conflict when another Runtime owns the Session.

### Failure handling

Do not mutate the existing startup-failure state machine. Its current
fail-closed behavior remains authoritative:

- Runtime becomes `lost`;
- WorkerRun and AgentTurn become `failed:runtime_lost`;
- Task becomes `waiting_user`;
- cleanup and fencing evidence remain attached to the failed Runtime.

The previously failed local Session remains an audit record. Acceptance will
create a new Task instead of rewriting historical rows.

## Rejected Alternatives

### Allow an unbound starting Runtime in the control journal

Rejected because it weakens the journal's central identity fence and would
require the Bind, Claim, replay and event-apply paths to maintain a second
temporary ownership rule.

### Bind the durable control channel only after Docker activation

Rejected because the Worker needs its credential and control stream to publish
`runtime.ready`, while activation currently waits for that authenticated ready
event. Deferring the bind would create a startup cycle.

### Reset the failed Session and retry in place

Rejected because it would rewrite already committed failure evidence and blur
the distinction between a new WorkerRun, Runtime recovery and manual Task
rework.

## Test Design

Add an integration regression test at the PostgreSQL/runtime-control seam:

1. Allocate a fresh WorkerSession and enqueue a Task.
2. Issue a SessionDispatchPermit.
3. Execute `StartWorkerRun`;
4. assert the Session current Runtime equals the returned Runtime identity;
5. bind the durable WorkerControl credential for the exact generation and
   ChannelEpoch;
6. activate the resident Runtime with a container identity;
7. assert the Session still points to the same Runtime and the Runtime is
   `active`.

The test must fail against the current implementation at step 4 or 5 before
the fix and pass after the fix.

Add or extend tests proving:

- a Session with an existing current Runtime rejects another initial Runtime;
- activation rejects a mismatched Runtime identity;
- startup failure still converges through the existing loss path.

## Verification

Run:

```bash
go test -tags=integration ./internal/workerruntime/adapter/postgres \
  -run 'InitialRuntimeOwnership|RuntimeResidency|SessionFIFO' -count=1
go test ./internal/workerruntime/application -count=1
make verify
```

Then rebuild the affected Compose services and execute a new hello-world Task.
Acceptance requires:

- WorkerSession does not become `lost`;
- WorkerRun and AgentTurn progress beyond their startup states;
- Runtime has a non-empty container identity and becomes `active`;
- durable Runtime control channel exists for the same Session, Runtime,
  generation and ChannelEpoch;
- the Worker detail page no longer shows `Not started` for the new Task.

## Non-Goals

- Enabling Single Runtime Recovery by default.
- Completing the remaining Runtime Recovery OpenSpec tasks.
- Adding an in-place retry protocol for the historical failed Task.
- Changing AgentProfile, provider credentials, LiteLLM or frontend status
  rendering.
