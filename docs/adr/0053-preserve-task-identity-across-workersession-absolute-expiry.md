# ADR 0053: Preserve Task Identity Across WorkerSession Absolute Expiry

- Status: Accepted
- Date: 2026-07-14

## Context

Task is the lightweight identity of a tracked delivery, while WorkerSession is a time-bounded interactive execution context. ADR 0049 normally keeps a non-terminal Task bound to one live Session so acceptance and rework target an unambiguous Agent and WorkingCopy. ADR 0052, however, makes a Session that reaches its 14-day absolute lifetime permanently terminal and requires any continuation to use a new identity.

Automatically failing the Task would confuse an execution-environment limit with delivery failure. Reopening or migrating the old WorkerRun would bypass the terminal Session and authorization boundaries. Creating a new Task would split one delivery solely because its execution context aged out.

## Decision

WorkerSession absolute expiry terminates the old execution context and active WorkerRun, but does not automatically terminate the bound Task.

- Absolute expiry is applied by an idempotent, revision-fenced command based on server time. Worker Runtime first fences new commands and lease issuance, requests ACP/Tool cancellation, revokes control/secret leases and proves the old Runtime no longer holds the WorkingCopy write lease.
- If the old Runtime cannot be isolated or its write lease cannot be revoked, the Session becomes or remains `lost`, continues consuming continuity quota and cannot be used as a continuation source. Only after fencing is proven may Worker Runtime commit `expired`, clear the active binding, release quota and publish the expiry outcome.
- An active WorkerRun becomes terminal `timed_out` with reason `worker_session_absolute_expired`. It is never resumed or moved into the derived Session. Late Tool/Agent results remain Trace evidence and cannot change terminal state.
- Report/patch Artifacts already persisted remain attached to their source Run. Output that had not reached a valid delivery boundary is marked partial with capture/checkpoint provenance and cannot satisfy automatic acceptance by itself. A Task already in `delivered` keeps its previously delivered result unchanged.
- Worker Runtime publishes an immutable `WorkerSessionExpiryOutcome` containing source Session/Run revisions, latest proven checkpoint, Artifact refs/completeness, cancellation/fencing evidence, external-effect uncertainty and the formerly bound Task ref.
- Task Orchestration consumes that outcome idempotently against the exact current binding. A terminal Task is unchanged. A `queued`, `running` or `waiting_user` Task enters or remains `waiting_user` with reason `worker_session_expired`; a `delivered` Task remains `delivered` so the initiator may accept the existing result without allocating another Worker.
- Expiry clears the Task's current executable `worker_session_id` and `current_worker_run_id`, then appends an immutable `TaskSessionLineageEntry` for the expired Session, terminal Run, checkpoint and release reason. The Task keeps one optional current Session plus ordered historical entries rather than pretending the terminal Session remains bound.
- Available actions are permission-filtered. TaskInitiator may cancel, accept a delivered result, or explicitly `ContinueTaskInNewWorker`. Current Workspace owner retains emergency cancel only. Continuation/retry is blocked while an unknown non-idempotent external effect requires reconciliation.
- `ContinueTaskInNewWorker` binds actor, exact Task revision, expiry outcome, checkpoint and idempotency key. In the Task transaction it changes `waiting_user` or `delivered` to `queued`, records the intent/lineage and writes an Outbox request for a Task-owned WorkerSessionContinuation. It does not allocate or materialize a Session inside the transaction.
- WorkerSessionAllocator applies the existing Tenant/User FIFO and quota. After a new Session is allocated and owner binding is accepted, Task Orchestration sets it as the current `worker_session_id`, enqueues the Task in that Session and creates a new WorkerRun only when the normal dispatch permit is accepted. The Task becomes `running` at that point.
- The new WorkerRun records the prior Task/Run/Session and continuation checkpoint in WorkerRunStartSnapshot. It is a new execution stage, not runtime recovery. Old Session-scoped grants, secrets, AgentSession and queue state do not transfer. Each later AgentTurn receives the same fixed 24-hour hard deadline under ADR 0055.
- Worker Runtime and Task Orchestration converge through owner events, expected revisions, idempotency and reconciliation. No distributed transaction or public TaskSaga is introduced. A temporary projection lag may show expiry convergence, but commands always consult owner state.

## Consequences

- The 14-day limit remains a hard execution boundary without destroying the business identity of unfinished work.
- Task gains explicit current-binding and historical-lineage fields, but not a second workflow state machine.
- Delivered results remain reviewable and acceptable even after their Worker expires; only rework needs a new Worker.
- Continuation can wait for capacity without fabricating a Worker identity or reviving the source Run.
- Operations must detect expiry event gaps, stale Task bindings, unconfirmed runtime fencing and partial Artifact classification.

## Alternatives Retained

- Automatically fail the Task at Session expiry: simpler, but reports infrastructure lifetime as delivery failure and discards an otherwise valid continuation path.
- Automatically migrate the active WorkerRun: rejected because Run identity, clocks, permissions and side-effect evidence belong to the terminal Session.
- Require a new Task: retained only for a genuinely new deliverable; Session lifetime alone is not sufficient reason to split delivery identity.
