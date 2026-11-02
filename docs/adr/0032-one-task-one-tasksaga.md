# ADR 0032: One Task Owns One TaskSaga

- Status: Superseded by ADR 0048 for P0
- Date: 2026-07-12

## Context

Switchback separates durable Task orchestration from the longer-lived interactive WorkerSession. A WorkerSession can remain open after a Task reaches a terminal state, and users can continue talking directly to its Coding Agent. Without a fixed aggregate boundary, a direct follow-up could accidentally reopen completed business state, or every AgentTurn could create noisy Task/Saga records.

## Decision

Each Task owns exactly one TaskSaga. The TaskSaga coordinates that Task from creation to a terminal state and is never reopened after the Task becomes terminal.

- A retry before Task termination stays in the same TaskSaga and creates a new TaskAttempt and WorkerRun.
- Runtime recovery stays in the same TaskSaga, TaskAttempt, and WorkerRun; it only replaces RuntimeInstance generation.
- WorkerSession lifecycle is independent from TaskSaga lifecycle. One WorkerSession may outlive a Task and may host WorkerRuns for multiple Tasks plus standalone direct interaction.
- A direct AgentTurn after Task termination starts or joins a session-scoped WorkerRun without `task_id`. It may carry `origin_task_id` for lineage, but it cannot mutate the completed Task or TaskSaga.
- A newly tracked follow-up deliverable creates a new Task and a new TaskSaga, while optionally reusing the existing WorkerSession and Workspace.
- Late events for a terminal Task are retained as Trace/audit evidence or raised as integrity incidents. They do not reopen the TaskSaga.

## Consequences

- Task history has a stable one-to-one orchestration identity and simple terminal invariants.
- Worker chat remains lightweight and does not manufacture Tasks for every conversational turn.
- Frontend and analytics must distinguish task-bound WorkerRuns from standalone WorkerRuns and show optional origin lineage.
- Promotion of standalone work into a tracked Task must be an explicit application action; copying context does not retroactively attach the original WorkerRun to the new Task.

## Alternatives Retained

- One Saga per WorkerSession was rejected because a session may span unrelated goals and multiple terminal Tasks.
- One Saga per WorkerRun was rejected because retries, waiting-user states, compensation, and final Task acceptance need a durable coordinator across runs.
