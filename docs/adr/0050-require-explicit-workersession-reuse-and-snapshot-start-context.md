# ADR 0050: Require Explicit WorkerSession Reuse And Snapshot Start Context

- Status: Accepted
- Date: 2026-07-14

## Context

A WorkerSession is not an empty execution slot. It owns a persistent WorkingCopy, Agent sessions and resumable Agent context, and it may contain uncommitted changes from earlier WorkerRuns. Automatically selecting a recent or idle Session for a new Task can therefore mix unrelated work. Silently cleaning or resetting a reused Session would solve the isolation problem by destroying the continuity that users selected the Session to preserve.

The system needs one rule that works for Assistant, Delivery, API and direct Worker interaction, including busy Sessions, expired Sessions and Tasks that require a clean or exact WorkspaceRevision baseline.

## Decision

P0 requires authenticated explicit intent to reuse an exact WorkerSession.

- Reuse requires a `WorkerSessionTargetIntent` bound to the actor, exact `worker_session_id`, Workspace, interaction source/revision and idempotency key. A Worker Detail action, a registered card/command with an immutable target or an exact API field may produce this evidence. Recent activity, the only idle Session, Workspace equality, related refs and model inference do not.
- Direct Worker Detail interaction and rework of a non-terminal bound Task already carry an exact Session target and continue in that Session.
- A new Task without valid exact Session target intent is created as `queued` and assigned a newly allocated WorkerSession in the authorized Workspace. It is not appended to an arbitrary existing Session queue.
- Explicit reuse resolves `WorkerSessionContextPolicy=inherit_current`. The new Run inherits the Session's current WorkingCopy, persisted Agent state/context, Agent sessions and uncommitted changes. The platform must not silently reset, clean, stash, checkout or replace that state.
- Fresh allocation resolves `WorkerSessionContextPolicy=fresh_session` and materializes a new WorkingCopy from the authorized Workspace active revision or another exact revision selected by policy.
- A Task requiring `exact_base` must name the required WorkspaceRevision/checkpoint. If an explicitly targeted Session does not already match it, the system does not mutate that Session in place. It allocates a new Session when authorized and capacity permits, or keeps the Task queued/blocked with an explicit user action.
- Only a Task assigned to a WorkerSession enters that Session's `SessionTaskQueue` and receives `session_queue_sequence`. An unassigned queued Task may expose an allocation reason but does not consume another Session's queue position.
- Reassigning a queued Task to an existing Session requires new exact target intent and inherits that Session's current state. Reassigning to a fresh/exact baseline allocates a new Session.
- Before the first Agent prompt, Tool invocation or WorkingCopy mutation of every WorkerRun, Worker Runtime persists an immutable `WorkerRunStartSnapshot`. It records the WorkerSession, resolved context policy, AgentSession refs, WorkingCopy checkpoint/manifest digest, base WorkspaceRevision, dirty state, prior Task/Run refs and server capture time. The Run references this snapshot for Trace, Artifact and ChangeSet lineage.
- ContextPack or TaskSpec summaries may reference prior work, but they do not substitute for actual WorkingCopy and Agent-state inheritance or for the start snapshot.

## Consequences

- Default Task isolation is predictable: no exact Session target means a fresh Session.
- Selecting an existing Session has a clear cost and benefit: full continuity, including dirty state, is preserved.
- Task may remain `queued` before `worker_session_id` is assigned; Task status does not gain a separate allocation state.
- Fresh Sessions consume more continuity capacity. Counting and capacity-wait behavior are defined by [ADR 0051](0051-count-continuity-holding-workersessions-against-user-quota.md); pressure must be handled by an explicit queue or Session close/reuse choice, not hidden reuse or eviction.
- Start snapshots make inherited state diagnosable and let Artifact, ChangeSet and Evolution consumers distinguish Task input from pre-existing Session state.

## Alternatives Retained

- Post-terminal continuation is now fixed by [ADR 0052](0052-make-terminal-workersessions-immutable-and-continue-by-derivation.md): derive a new Session and WorkingCopy from an exact checkpoint rather than reset or reopen the source Session.
- Reconsider automatic same-Workspace reuse only if product evidence shows the extra Session cost is unacceptable and a user-visible, deterministic isolation model replaces it.
