# ADR 0054: Require Explicit Budget For Task Continuation

- Status: Superseded by ADR 0055
- Date: 2026-07-14

> Retained as design history. P0 no longer has continuation budgets or Task cumulative active-time accounting; see [ADR 0055](0055-use-a-fixed-agentturn-hard-deadline.md).

## Context

ADR 0053 keeps a Task alive after its WorkerSession reaches absolute expiry, but the continuation creates a new WorkerSession and WorkerRun. Reusing the old Run's remaining active budget or ExecutionBudgetGrant would move authority across a terminal Session boundary. Automatically resetting the original TaskSpec budget would make every continuation a hidden cost renewal and allow one Task to grow without a cumulative bound.

## Decision

Every `ContinueTaskInNewWorker` command must carry an initiator-approved `ContinuationBudgetSpec` for the future WorkerRun.

- The spec binds the actor, Task revision, expiry outcome/checkpoint, requested WorkerRun active budget, ExecutionBudgetPolicy revision and idempotency key. It is immutable input to the continuation command, not an `ExecutionBudgetGrant` against a WorkerRun that does not yet exist.
- The continuation inherits neither source Run remaining active time nor any source ExecutionBudgetGrant. It also does not reset the accepted TaskSpec's original WorkerRun budget.
- Every Task has a `TaskCumulativeActiveBudget` governed by ExecutionBudgetPolicy. Initial, rework and expiry-continuation WorkerRuns with that `task_id` charge the same account. Standalone Runs with only `origin_task_id` do not.
- Cumulative usage counts actual persisted ActiveExecutionClock consumption, not requested or granted capacity. A Run that completes early returns no artificial charge; its unused Run budget remains available under the Task cumulative ceiling.
- Before accepting continuation, Task Orchestration requires `requested_run_active <= min(current WorkerRun ceiling, Task cumulative remaining)`. WorkerSession absolute lifetime and WorkerRun wall cap remain independent hard limits and are checked again before dispatch.
- The accepted continuation budget is persisted while the Task waits for Session allocation. Normal dispatch resolves a fresh WorkerRun budget snapshot from it; the new Run still starts only after SessionDispatchPermit and execution-slot acceptance.
- Any later ExecutionBudgetGrant for a Task-bound Run must satisfy both the Run ceiling and Task cumulative remaining. Worker or Agent may request more budget but cannot change either limit.
- Task cumulative consumption and applied Run transitions are updated from server-owned, idempotent ActiveExecutionClock state. Runtime/container clocks and projection summaries are not authority.
- If policy, Task revision or available cumulative budget changes before dispatch, the platform must not silently clamp the requested value. It moves or keeps the Task at `waiting_user:continuation_budget_stale`, shows the current calculation and requires a new initiator confirmation.
- Frontend and actor-private Delivery surfaces show Task cumulative consumed/ceiling, requested continuation budget and effective per-Run maximum. Shared cards may show only a coarse budget-action notice and Web deep link.

The concrete P0 Task cumulative default and hard ceiling remain an explicit follow-up decision. Their values must be introduced through an ExecutionBudgetPolicy revision, not hidden in application code.

## Consequences

- A terminal Session cannot leak old cost authority into a new execution identity.
- Repeated continuation cannot silently mint another full TaskSpec budget.
- Task remains lightweight: cumulative budget is an accounting constraint with no independent lifecycle or state machine.
- A pending continuation can wait for capacity without losing the exact budget the initiator approved.
- Policy tightening is visible to the user instead of causing an unexplained shorter Run.

## Alternatives Retained

- Inherit source Run remaining budget: rejected because a tiny remainder often makes continuation unusable and crosses a terminal authority boundary.
- Reset the original TaskSpec budget automatically: rejected because repeated continuation amplifies cost without a new user decision.
- Require a new Task for any additional budget: retained for genuinely expanded scope or when the Task cumulative hard ceiling is exhausted, but not required solely because the WorkerSession expired.
