# ADR 0033: Require Explicit Follow-up Task Creation

- Status: Partially Superseded by ADR 0048
- Date: 2026-07-12

## Context

A WorkerSession remains interactive after a Task completes. Direct chat can expand from a small explanation into meaningful new delivery work, but model-inferred Task creation would surprise users, pollute Task lists, and make authorization/audit evidence ambiguous. Retrofitting an existing standalone WorkerRun into a Task would also violate the accepted one-Task/one-TaskSaga boundary.

## Decision

Switchback creates a tracked follow-up Task only from explicit authenticated intent. The canonical action is **create a follow-up Task**, not promote or convert a standalone WorkerRun.

Explicit intent may be captured by:

- a user confirming a reviewed FollowUpTaskProposal in Worker detail;
- a structured user command such as `create follow-up task`;
- an unambiguous direct user instruction to create the work as a tracked Task; or
- an authenticated Assistant, Delivery, or API submission that already follows TaskSpec readiness rules.

Worker Agent may suggest creating a Task and may prepare or revise a FollowUpTaskProposal. A proposal is not a Task, does not own a TaskSaga, reserves no runtime capacity, and cannot execute by itself. Model inference, generated UI copy, conversation scope growth, or an unconfirmed proposal is not explicit creation intent.

On confirmation, Gateway or the channel adapter records authenticated intent evidence and submits an idempotent CreateFollowUpTask command. Task Orchestration revalidates authorization, Workspace access, proposal revision/hash, TaskSpec readiness, and current policy before atomically creating one new Task, one new TaskSaga, and Outbox records. Duplicate confirmation returns the original identities.

The new Task may reuse a valid WorkerSession and Workspace. If the preferred WorkerSession is busy, execution follows normal scheduling without interrupting the active run. If it is expired, the Task may allocate a new WorkerSession for the same authorized Workspace. Revoked Workspace access fails closed before Task creation.

The source standalone WorkerRun remains standalone. `origin_task_id`, `source_worker_run_id`, and proposal references provide lineage without retroactively changing ownership.

## Consequences

- Users keep fluid Coding Agent chat without receiving hidden workflow objects.
- Task creation has deterministic, auditable actor intent and idempotency evidence.
- Frontend and Delivery need a proposal/confirmation surface, but they do not own Task state.
- Worker can recommend stronger tracking while Task Orchestration remains the only creator of Task/TaskSaga authority.

## Alternatives Rejected

- Model-inferred automatic creation was rejected because confidence is not authorization and would create unstable product behavior.
- Creating a Task for every direct WorkerRun was rejected because conversational follow-ups are not always independent deliverables.
- Retrofitting a standalone WorkerRun into a Task was rejected because it rewrites history and breaks terminal aggregate boundaries.
