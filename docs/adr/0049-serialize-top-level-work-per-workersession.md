# ADR 0049: Serialize Top-Level Work Per WorkerSession In P0

- Status: Accepted
- Date: 2026-07-13
- Amended: 2026-07-14

## Context

WorkerSession is a long-lived interactive Coding Agent session, not a one-shot job. A user can send prompt, queue, interrupt and steer commands, while Assistant, Delivery or API may also create new tracked Tasks for the same Workspace and preferred WorkerSession. Allowing two top-level Tasks or WorkerRuns to execute concurrently in one WorkerSession would make AgentTurn routing, cancellation, fixed deadlines, approvals, Artifact ownership and WorkingCopy changes ambiguous.

Codex may still use internal subagents or child sessions during one top-level execution. Those are implementation detail and Trace lineage unless the Agent adapter exposes a stable independent session identity; they do not justify multiple top-level WorkerRuns in one WorkerSession.

## Decision

P0 serializes top-level work at the `WorkerSession` boundary.

- A WorkerSession has at most one `bound_task_id` and at most one `active_worker_run_id` at any instant.
- A Task may reference an explicitly requested or newly assigned `worker_session_id` while still `queued`, but it does not become that Session's bound Task until Worker Runtime accepts `StartWorkerRun` for it.
- A Task references an existing Session only after authenticated exact target intent; an untargeted Task is assigned a new Session before it enters a Session queue. Reuse and start-context semantics are defined by [ADR 0050](0050-require-explicit-workersession-reuse-and-snapshot-start-context.md).
- A busy WorkerSession is never implicitly interrupted by a new Task. The Task remains `queued` for that Session, or the user explicitly chooses a new WorkerSession in the same authorized Workspace when parallel progress is required.
- `prompt`, AgentTurn `queue`, `interrupt` and `steer` target only the single active WorkerRun. They are not Task scheduling operations.
- A Task normally remains the bound Task while it is `running`, `waiting_user` or `delivered`; `delivered` therefore preserves the exact Agent and WorkingCopy context for acceptance or rework. The only P0 non-terminal release is a committed WorkerSession absolute-expiry outcome under [ADR 0053](0053-preserve-task-identity-across-workersession-absolute-expiry.md), which moves the association into Task Session lineage and requires explicit continuation in a new Session.
- A standalone WorkerRun may start only when the WorkerSession has no bound Task and no active WorkerRun. It never jumps ahead of an already selected queued Task after that Task has acquired the Session slot.
- If the active WorkerRun has a `task_id`, it must equal `bound_task_id`. A terminal Task releases the binding only after its active Run has reached a terminal or safely cancelled state.
- Worker Runtime owns the Session slot and rejects a conflicting `StartWorkerRun` with `session_busy`. Task Orchestration owns Task queue state and retries or allocates another Session; EventCenter only carries the resulting events.
- Slot acquire, Run identity assignment and Session revision advance are fenced and idempotent. Response loss or duplicate commands must return the same accepted Run or the same busy result, never start a second Run.
- Internal Agent subagents and child execution spans may run concurrently inside the one active top-level WorkerRun, subject to its shared execution, permission, resource and Trace policies.

P0 also fixes the minimum Session Task scheduling policy:

- Each WorkerSession has an independent stable FIFO Task queue. Task Orchestration assigns a monotonic `session_queue_sequence` when an enqueue commits; duplicate enqueue requests return the original sequence.
- Only the lowest eligible sequence may receive a dispatch permit. A queued Task can be cancelled, but P0 provides no priority scheduling, drag reorder or preemption. Display-level Task priority does not affect this queue.
- A direct standalone prompt cannot overtake an earlier queued Task. Every new top-level Run requires a short-lived single-use dispatch permit: a Task permit is issued only to the queue head, while a standalone permit is issued only when the Session queue is empty. The permit and Session slot are both fenced and idempotent.
- Reassigning a still-queued Task to another authorized WorkerSession is an explicit initiator action. It removes the old queue position and appends the Task to the target Session's tail with a new sequence; it never carries priority or preempts work.
- Slot-release events trigger head dispatch, and reconciliation repairs lost notifications. Authorization, Task state and Session lifetime are revalidated before every dispatch.

## Consequences

- Direct chat and runtime controls always have one unambiguous target.
- Artifact, deadline, approval and WorkingCopy lineage stay attributable to one top-level execution.
- Parallel work consumes another WorkerSession and its isolated WorkingCopy, so the continuity quota defined by [ADR 0051](0051-count-continuity-holding-workersessions-against-user-quota.md) and WorkspaceApply conflict rules remain the capacity boundary.
- A manual Task in `delivered` blocks the same WorkerSession from starting unrelated tracked work until acceptance, cancellation or explicit use of another Session. This is a deliberate P0 simplicity trade-off.
- Task scheduling latency can increase for users who choose Session continuity over parallelism.
- FIFO is deterministic and explainable, but an old `delivered` or `waiting_user` Task can hold the Session while later Tasks remain queued; P0 exposes age and cancellation rather than hidden priority or automatic preemption.

## Alternatives Retained

- Permit multiple top-level WorkerRuns in one WorkerSession after introducing explicit per-Run Agent session, WorkingCopy, command routing, deadline and Artifact isolation.
- Release the bound Task at `delivered` and queue any later rework after the newer Task. This improves utilization but makes the Worker chat target and rework continuity less predictable.
- Add priority, reorder or preemption policy after real queue-wait, cancellation and new-Session usage data exists. Any future preemption must be explicit and auditable.
