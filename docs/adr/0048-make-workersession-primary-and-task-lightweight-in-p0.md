# ADR 0048: Make WorkerSession Primary And Task Lightweight In P0

- Status: Accepted
- Date: 2026-07-13

## Context

Earlier design iterations modeled Task, TaskSaga, TaskAttempt, FollowUpTaskProposal, TaskActionPolicy, TaskActionGrant, WorkerSession and WorkerRun as separate first-class concepts, then exposed both Task and Worker as primary frontend work surfaces. Those concepts can express advanced delegation, long-running workflow recovery and portfolio reporting, but they also duplicate lifecycle state and force users to choose between two pages for the same ongoing Coding Agent interaction.

The P0 product objective is narrower: a user can submit a sufficiently clear request, obtain a real containerized Coding Agent, watch it work, review report/patch artifacts and keep talking to the same live Agent. The minimum domain should optimize that loop.

## Decision

P0 uses **lightweight Task plus WorkerSession-primary interaction**.

- `WorkerSession` is the primary user work surface and durable interaction identity. Its detail page owns chat, queue/interrupt/steer controls, progress, approvals, runtime health, WorkerRuns and Artifacts.
- `Task` is a small tracked-delivery envelope. It snapshots TaskSpec and initiator, binds Workspace, conditionally references an explicitly requested or newly assigned WorkerSession, points to the current WorkerRun and result Artifacts, records `acceptance_mode`, status and revision, and provides list/reporting identity.
- `WorkerRun` represents each execution or rework stage. P0 does not add a separate `TaskAttempt`; runtime recovery remains inside the same WorkerRun and only changes RuntimeInstance generation.
- Task uses the state set `queued`, `running`, `waiting_user`, `delivered`, `completed`, `failed`, `cancelled`. A stage result moves a manual-acceptance Task to `delivered`; user feedback starts a new WorkerRun and returns it to `running`; explicit acceptance moves it to `completed`. Low-risk auto-acceptance may move directly from stage delivery to `completed`.
- P0 `acceptance_mode` is `auto` or `manual`. Report-only work with deterministic evidence may be auto-accepted; patch delivery defaults to manual acceptance. This is a Task field resolved from policy, not a separate acceptance aggregate.
- While a Task is `queued`, `running`, `waiting_user` or `delivered`, direct feedback about that delivery remains in the same Task and starts or joins another WorkerRun. After `completed`, `failed` or `cancelled`, further Worker chat is standalone unless the user explicitly creates a new tracked Task.
- Creating a new tracked Task still requires authenticated explicit user intent. The Agent may suggest it, but P0 does not require a first-class FollowUpTaskProposal lifecycle. “Create as my Task” may clone a readable draft/context into a new actor-owned TaskDraft and rerun readiness and authorization.
- P0 Task action rules are fixed: TaskInitiator may interact, accept, request rework, cancel and reassign a still-queued Task to another authorized Session tail; a current Workspace owner may emergency-cancel. Visibility, Conversation control and Workspace editor/reviewer roles grant no lifecycle authority. General TaskActionGrant delegation is deferred to P1.
- Task state and revision, an append-only Task transition/audit record and owner-stream Outbox are committed atomically in PostgreSQL. Timed retry/reminder/reconciliation may use a generic fenced scheduled-command table. `TaskSaga`, `SagaTransition` and `SagaWakeup` are not P0 public domain aggregates or external protocols; an implementation may use internal helpers with those names only if they do not create a second authority beside Task.
- Task List is a lightweight tracking index. Task Detail is a summary/deep-link view or drawer. Delivery result cards deep-link to Worker Detail for continued interaction and Artifact review. P0 does not require Task/Worker dual-primary feature parity or a rich global ActionInbox.
- Top-level execution inside one WorkerSession is serialized. A Session has at most one bound Task and one active WorkerRun; busy-session Tasks queue or use another WorkerSession. Exact slot and binding semantics are defined by [ADR 0049](0049-serialize-top-level-work-per-workersession.md).
- Existing WorkerSession reuse requires authenticated exact target intent and inherits its current WorkingCopy and Agent context; otherwise a new Session is allocated. Start-baseline semantics are defined by [ADR 0050](0050-require-explicit-workersession-reuse-and-snapshot-start-context.md).

The simplification does **not** remove container isolation, ACP control, persistent WorkingCopy and Agent state, WorkerSession resume, RuntimeHeartbeat/recovery, Trace/Artifact persistence, transactional Outbox, idempotent commands, external authorization, secret fencing, execution limits or the 48-hour idle/14-day absolute session lifecycle.

## Consequences

- The first usable product has one obvious place to work with the Coding Agent.
- Task implementation and frontend projection are smaller, while tracked delivery, acceptance and reporting remain available.
- Rework is natural conversation rather than a new Saga/Attempt workflow.
- Advanced delegation, reviewer acceptance, portfolio workflow and complex durable timers are deferred until real usage demonstrates the need.
- Existing ADRs 0031, 0032, 0033, 0034 and 0047 remain useful design history and P1 option material, but their P0 TaskSaga, proposal, grant and dual-primary requirements are superseded by this ADR.

## Alternatives Retained

- Reintroduce an explicit TaskSaga when a single Task needs materially more durable branches, compensations or long-lived timers than Task revision plus scheduled commands can express.
- Reintroduce TaskActionGrant when users need bounded delegation that cannot be represented by initiator and Workspace-owner rules.
- Promote Task Detail to a primary work surface when portfolio operators need a workflow view independent of Worker interaction.
