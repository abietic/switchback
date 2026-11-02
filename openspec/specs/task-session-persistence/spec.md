# Task Session Persistence Specification

## Purpose

Define the M1 Task admission, durable lifecycle, WorkerSession allocation, execution serialization, reuse, scheduling, follow-up, and event-ordering contracts.
## Requirements
### Requirement: Direct TaskSpec admission is explicit and server-scoped
The system SHALL allow the SingleUserLocalProfile caller to create a Task from either an explicit direct TaskSpec or an Assistant-produced exact ready TaskDraft while requiring server-injected tenant/user identity, an exact authorized Workspace, a ready TaskReadinessAssessment, an ExplicitTaskCreationIntent bound to the exact TaskSpec digest and an idempotency key.

#### Scenario: Valid direct TaskSpec is submitted
- **WHEN** the local caller submits goal, deliverables, acceptance criteria, constraints, `workspace_id`, execution policy and an enabled AgentProfile
- **THEN** Gateway injects the fixed actor, rejects client identity overrides, produces a zero-question readiness assessment with no blocking unknown and records an intent bound to actor, TaskSpec digest, Workspace and trigger-policy revision before submitting CreateTask

#### Scenario: Valid Assistant TaskDraft is confirmed
- **WHEN** the fixed local actor confirms the exact current ready TaskDraft/readiness/Workspace/digest/policy revisions through the registered confirmation action
- **THEN** Task Orchestration revalidates the immutable creation intent and atomically creates Task, initial TaskTransition, SessionAllocationRequest, and owner-stream Outbox through the same admission path as direct TaskSpec

#### Scenario: Assistant confirmation or delivery is duplicated
- **WHEN** the same creation intent, TaskSpec digest, actor, Workspace, and idempotency key are consumed again after duplicate delivery or response loss
- **THEN** Task Orchestration returns the original Task and command identities without creating another Task, transition, allocation request, or Outbox event

#### Scenario: Workspace or TaskSpec evidence is missing
- **WHEN** `workspace_id`, readable WorkspaceRevision, required acceptance criteria, readiness, creation intent, exact Assistant draft binding, or server policy evidence is missing or stale
- **THEN** Task Orchestration rejects the command without creating Task, TaskTransition, allocation request or Outbox record

### Requirement: Task creation is transactionally idempotent
The system SHALL consume the active creation intent and create Task, initial TaskTransition, SessionAllocationRequest and owner-stream Outbox in one PostgreSQL transaction using expected revision, TaskSpec digest and idempotency key.

#### Scenario: CreateTask response is lost and retried
- **WHEN** the same actor repeats the same idempotency key and TaskSpec digest
- **THEN** the system returns the original Task and command identities without creating duplicate state or events

#### Scenario: Idempotency key is reused with different content
- **WHEN** the same actor repeats an idempotency key with a different TaskSpec digest or Workspace
- **THEN** the system returns an idempotency conflict and leaves the original Task unchanged

### Requirement: Task remains a lightweight delivery record
The system SHALL restrict Task status to `queued`, `running`, `waiting_user`, `delivered`, `completed`, `failed` and `cancelled` and SHALL keep Worker execution detail in WorkerSession, WorkerRun and AgentTurn records.

#### Scenario: Runtime starts work
- **WHEN** Worker Runtime accepts the Task queue head and creates its WorkerRun
- **THEN** Task advances from `queued` to `running`, points to the current Session/Run and appends one TaskTransition without copying Tool or ACP state

#### Scenario: Invalid duplicate lifecycle is proposed
- **WHEN** an implementation attempts to add drafted, validated, starting, succeeded or timed_out as Task status
- **THEN** contract validation fails and requires the detail to remain in TaskDraft, WorkerRun, AgentTurn or reason metadata

### Requirement: Acceptance mode controls delivery completion
The system SHALL snapshot `acceptance_mode=auto|manual`; report-only results with deterministic acceptance evidence can complete automatically, while patch delivery defaults to manual acceptance.

#### Scenario: Patch result is ready
- **WHEN** a WorkerRun finalizes a valid patch Artifact for a manual Task
- **THEN** Task enters `delivered`, retains the WorkerSession binding and exposes accept and rework actions to the initiator

#### Scenario: User accepts delivered result
- **WHEN** the initiator sends an idempotent accept command for the exact Task revision
- **THEN** Task enters `completed`, appends TaskTransition and Outbox in one transaction and releases its active Task binding according to Session policy

#### Scenario: User requests rework
- **WHEN** the initiator sends feedback against the exact delivered Task and live WorkerSession
- **THEN** the same Task returns to `running` through a new WorkerRun without mutating prior Run evidence

### Requirement: WorkerSession allocation enforces continuity quota and FIFO
The system SHALL count continuity-holding WorkerSessions per Tenant/User with default limit 10 and SHALL allocate legal requests in stable server sequence order.

#### Scenario: Capacity is available
- **WHEN** the allocation queue head has current authorization and a free continuity slot
- **THEN** allocator atomically reserves one quota slot, creates one `allocating` WorkerSession and returns the same identity on retry

#### Scenario: Capacity is full
- **WHEN** ten counted Sessions already hold continuity
- **THEN** Task remains `queued` with `worker_session_id=null`, records `allocation_wait_reason=session_capacity` and is not assigned to or used to evict an existing Session

### Requirement: Session execution is serialized
The system SHALL allow at most one active top-level WorkerRun and AgentTurn per WorkerSession and SHALL dispatch assigned Task work in stable Session FIFO order.

#### Scenario: Queue head receives execution slot
- **WHEN** no Run is active and the first queued Task has a valid single-use dispatch permit
- **THEN** Worker Runtime starts exactly one WorkerRun and consumes the permit idempotently

#### Scenario: Another top-level command arrives while busy
- **WHEN** a second Task or standalone prompt targets a busy Session
- **THEN** the system queues it behind earlier Session work or returns `session_busy` according to command type and never starts concurrent top-level Runs

### Requirement: Existing Session reuse is exact and non-destructive
The system SHALL reuse a WorkerSession only when the authenticated user targets its exact id and SHALL preserve its WorkingCopy, AgentSession, Agent state and uncommitted changes.

#### Scenario: New Task omits Session target
- **WHEN** CreateTask has no exact WorkerSession target intent
- **THEN** Task requests a new Session instead of selecting the recent, idle or only existing Session

#### Scenario: Exact live Session is targeted
- **WHEN** the fixed actor targets an authorized nonterminal Session in the same Workspace
- **THEN** the new work joins that Session queue without reset, clean, stash, checkout or implicit WorkingCopy replacement

#### Scenario: Closed or expired Session is targeted
- **WHEN** a command targets a terminal WorkerSession
- **THEN** the system rejects live reuse and exposes only permission-filtered read-only history in this slice

### Requirement: Business state survives Backend restart
The system SHALL persist aggregate state, queue sequence, idempotency result, deadline, Outbox and projection cursor so a Backend restart does not lose committed work or create duplicate execution.

#### Scenario: Backend restarts after Task commit
- **WHEN** Task creation committed but its allocation event had not yet been processed
- **THEN** the Outbox dispatcher resumes delivery and allocator processes the original request once logically

#### Scenario: Backend restarts during a live Run
- **WHEN** PostgreSQL contains an active generation 1 Runtime and the Runtime remains reachable
- **THEN** Backend reconstructs control ownership from durable identities and does not create another Runtime or Run; unsupported transport reattachment fails closed and surfaces runtime loss

### Requirement: Scheduled commands use durable fencing
The system SHALL implement AgentTurn deadline, Session idle and Session absolute expiry through durable ScheduledCommand records with lease, fencing token, expected revision and idempotent application command.

#### Scenario: AgentTurn reaches 24-hour deadline
- **WHEN** server time reaches `started_at + 86400s`
- **THEN** the system fences further Turn work and records a timed-out AgentTurn regardless of approval wait, disconnection or quiet model output

#### Scenario: Session is idle for 48 hours
- **WHEN** no policy-defined valid user interaction refreshes idle state for 48 hours and the idle clock is not paused by waiting_user
- **THEN** the system closes the Session, fences Runtime writes and schedules exact cleanup

#### Scenario: Session reaches 14-day absolute lifetime
- **WHEN** server time reaches the immutable absolute expiry
- **THEN** the system expires the Session even if waiting_user or active, terminates the active Run with the defined expiry reason, moves or keeps a nonterminal bound Task in `waiting_user:worker_session_expired`, preserves a delivered Task and never silently extends or revives the Session

### Requirement: Direct follow-up preserves Task semantics
The system SHALL classify an exact WorkerSession follow-up according to the bound Task state instead of always creating a new Task.

#### Scenario: Follow-up requests rework before Task terminal
- **WHEN** the initiator sends feedback to a live Session whose Task is running, waiting_user or delivered
- **THEN** the system retains the Task identity and creates or joins a new WorkerRun under that Task according to queue state

#### Scenario: Follow-up arrives after Task terminal
- **WHEN** the user chats with the same live Session after its Task completed, failed or cancelled
- **THEN** the system creates a standalone WorkerRun with no Task mutation and records optional `origin_task_id` lineage

### Requirement: Aggregate changes emit owner-sequenced events
The system SHALL allocate contiguous IntegrationEvent sequence with aggregate state and Outbox in the owning PostgreSQL transaction and SHALL NOT use timestamps as ordering authority.

#### Scenario: Task status changes
- **WHEN** Task Orchestration commits a legal transition
- **THEN** it appends TaskTransition and one or more owner-stream Outbox events with event id, stream id, sequence, causation and idempotency metadata

#### Scenario: Duplicate event is delivered
- **WHEN** projector or Runtime consumer receives the same event again
- **THEN** it deduplicates by event identity and stream sequence without repeating state changes or external side effects
