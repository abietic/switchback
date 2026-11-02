# ADR 0034: Use Task And Worker As Dual Primary Frontend Views

- Status: Superseded by ADR 0048 for P0
- Date: 2026-07-12

## Context

Switchback has several valid user entry points: Assistant conversations, Feishu Delivery, direct Worker interaction, Workspace context, Artifact review, and Memory governance. No single aggregate can contain the others without distorting lifecycle boundaries. In particular, Task is a tracked delivery unit while WorkerSession is a longer-lived interactive execution session that can outlive a Task and span multiple Tasks or standalone runs.

## Decision

Frontend uses Task and Worker as dual primary work views.

- **Tasks** organize tracked delivery: goal, TaskSpec, acceptance, business status, TaskSaga timeline, WorkerRuns, waiting actions, results, and Artifacts.
- **Workers** organize direct execution and interaction: WorkerSession chat, active/stage Runs, projected activity, approvals, runtime health, Artifacts, and Task lineage. The product label “Worker” is keyed by `worker_session_id`; it does not identify the backend Worker service.
- **Workspaces** organize durable context and resources: repositories, files, active revision, members, related Tasks, WorkerSessions, and Artifacts. Workspace is a secondary context view, not the only navigation root.
- **Home/Inbox** aggregates actionable items across Tasks and Workers, including waiting-user requests, approvals, conflicts, failures, and recent completions. It is a projection, not a workflow owner.
- **Artifacts** use one canonical Artifact detail identity. Task, Worker, Workspace, and search surfaces embed summaries and deep-link to the same permission-aware detail/action view. A separate Artifact library is secondary in P0.
- **Memory** remains an independent governance area with MemoryLibrary and ReviewInbox. Memory is linked back to evidence but is not nested inside Task or Worker pages.
- **Conversations** remain Assistant interaction history and may link to created Tasks; they are not the hierarchy that owns Delivery-created Tasks or WorkerSessions.

Frontend renders page-optimized projections from Frontier and sends commands through Gateway. It does not reconstruct authoritative Task, Worker, Workspace, Artifact, or Memory state from raw events and does not own action outcomes.

## Consequences

- Users can find both business outcomes and live Coding Agent sessions without forcing one lifecycle under the other.
- Cross-links and canonical identities become mandatory because the same Task, WorkerSession, Workspace, and Artifact appear in several projections.
- Frontier must provide ActionInbox, Task, WorkerSession, Workspace, Artifact, and Memory read models with permission-aware available actions.
- P0 navigation has more than one work surface, but each page has a single clear purpose and source identity.

## Alternatives Rejected

- Workspace-primary navigation was rejected as the sole hierarchy because Feishu tasks, temporary/no-repository work, and global waiting actions may start before users think in Workspace terms.
- Conversation-primary navigation was rejected because Delivery-created Tasks and long-lived WorkerSessions do not belong to one Assistant thread.
- Task-only navigation was rejected because WorkerSession remains interactive after Task terminal state and may host standalone work.
