## ADDED Requirements

### Requirement: Task lifecycle is state-machine driven
The system SHALL manage Task lifecycle through an explicit state machine with auditable transitions.

#### Scenario: Task reaches completion
- **WHEN** Worker evidence and any acceptance required by the Task's current completion rule establish that acceptance criteria are met
- **THEN** Task transitions to `completed` and emits a user-visible completion event

#### Scenario: Task waits for user
- **WHEN** execution requires missing input or approval
- **THEN** Task transitions to `waiting_user` and records the requested action

### Requirement: Lightweight Task durable execution is PostgreSQL authoritative
The system SHALL use Task as the only P0 delivery-state authority, SHALL persist Task state, immutable TaskTransition and owner-stream Outbox records in one PostgreSQL transaction, and SHALL NOT require TaskSaga or Temporal in P0.

#### Scenario: Task command advances state
- **WHEN** an idempotent command presents the current expected Task revision and satisfies transition and actor rules
- **THEN** one transaction updates Task, appends exactly one TaskTransition and appends Outbox before returning the result revision

#### Scenario: Duplicate command arrives after response loss
- **WHEN** the same idempotency key is submitted after the original transition committed but its response was lost
- **THEN** Task Orchestration returns the original Transition result without advancing revision or emitting another side effect

#### Scenario: Transaction would call an external system
- **WHEN** a transition needs Worker, model, ObjectStore or provider work
- **THEN** the transaction writes an Outbox command or event and commits without making the external call

### Requirement: ScheduledCommand uses leased fenced polling
The system SHALL persist retry, deadline, recovery, reminder and reconciliation commands, claim due rows with PostgreSQL `FOR UPDATE SKIP LOCKED`, short leases and monotonic fencing tokens, and submit only idempotent application commands outside the claim transaction.

#### Scenario: Poller crashes after claiming a command
- **WHEN** a poller holds a ScheduledCommand lease but crashes before command handling commits
- **THEN** another poller may reclaim it after lease expiry using a newer fencing token

#### Scenario: Old poller resumes
- **WHEN** a delayed poller presents an obsolete fencing token, stale expected Task/WorkerRun revision or cancelled/obsolete command
- **THEN** the command is an auditable no-op and cannot advance Task

#### Scenario: External write has unknown effect
- **WHEN** a ScheduledCommand follows an external operation whose result cannot be proven
- **THEN** the owner schedules reconciliation or enters `waiting_user` and does not blindly repeat the write

#### Scenario: Waiting user clocks are persisted
- **WHEN** Task enters `waiting_user`
- **THEN** the same transition persists the waiting reason and WorkerSession idle-clock effect while any active AgentTurn hard deadline and WorkerSession absolute lifetime continue unchanged

### Requirement: WorkerSession is primary and Task remains lightweight
The system SHALL keep Task lifecycle independent from WorkerSession lifecycle, SHALL represent each execution or rework stage as a WorkerRun without TaskAttempt, and SHALL NOT let post-terminal direct AgentTurns mutate a terminal Task.

#### Scenario: Delivered work is revised
- **WHEN** TaskInitiator requests rework while a manual Task is `delivered`
- **THEN** the same Task returns to `running`, creates a new WorkerRun linked to the previous Run and preserves prior Run history

#### Scenario: Runtime is replaced during a Task attempt
- **WHEN** an eligible RuntimeInstance failure triggers automatic recovery
- **THEN** the system retains the same Task and WorkerRun and only advances RuntimeInstance generation

#### Scenario: User continues after Task termination
- **WHEN** a user submits a direct AgentTurn in the still-valid WorkerSession after its Task is `completed`, `failed` or `cancelled`
- **THEN** Worker starts or joins a standalone WorkerRun without `task_id`, may record `origin_task_id`, and cannot mutate the terminal Task

#### Scenario: User creates a tracked follow-up
- **WHEN** an authorized explicit action creates a new tracked deliverable from prior Task or Worker context
- **THEN** the system creates a new lightweight Task and initial TaskTransition, reuses an existing WorkerSession only when exact target intent is present, otherwise allocates a new Session in the authorized Workspace, and does not retroactively assign earlier standalone WorkerRuns to the new Task

#### Scenario: Late terminal event arrives
- **WHEN** an event for a terminal Task arrives
- **THEN** the system records Trace/audit evidence or an integrity incident and does not advance the terminal Task revision

### Requirement: P0 Task actions use fixed initiator rules
The system SHALL snapshot immutable TaskInitiator from accepted creation intent, SHALL let that initiator continue, accept, request rework, cancel and reassign a still-queued Task to another authorized Session tail, SHALL let current Workspace owner emergency-cancel only, and SHALL NOT derive those actions from Task visibility, Conversation role, Workspace editor/reviewer role, disclosure or Artifact review.

#### Scenario: Task initiator operates an active Task
- **WHEN** TaskInitiator submits accept, rework, cancel or queued-Session reassignment
- **THEN** Task Orchestration revalidates actor, Membership, exact target, policy, expected Task revision and idempotency key before committing one transition

#### Scenario: Workspace owner stops risky work
- **WHEN** a current Workspace owner who is not TaskInitiator requests cancellation of an active Task
- **THEN** Task Orchestration permits emergency cancel but does not grant accept, rework, delegation, AgentTurn extension or any external operation

#### Scenario: Workspace collaborator attempts a Task action
- **WHEN** an editor, reviewer, Conversation controller, group participant, viewer or disclosure recipient requests accept, rework or another Task action
- **THEN** the command fails closed even though the actor may read or discuss the Task

#### Scenario: Expected revision is stale
- **WHEN** Membership changes, policy revision is unavailable, constraints are exceeded or expected Task revision is stale
- **THEN** the action is rejected without partial transition; a current Workspace owner retains only emergency cancel

#### Scenario: Rework targets a terminal Task
- **WHEN** any actor requests rework after Task is `completed`, `failed` or `cancelled`
- **THEN** Task Orchestration rejects mutation and requires explicit new Task creation for tracked work while allowing standalone Worker chat

#### Scenario: Approval is treated as deadline authority
- **WHEN** an actor presents ApprovalGrant or Task visibility as permission to extend an AgentTurn
- **THEN** the system rejects the substitution because the fixed 24-hour deadline is not extendable

### Requirement: M1 creates TaskSpec through a deterministic Worker-first form
The system SHALL let the server-configured local actor select Workspace, repository and immutable base commit, enter goal, acceptance criteria and optional constraints, preview a human-readable structured TaskSpec and explicitly submit it; the Web form and structured API SHALL reuse the same typed command and validator, and M1 SHALL expose neither a login flow, raw JSON editor nor model-based TaskSpec completion.

#### Scenario: M1 starts in single-user local mode
- **WHEN** the tracer-bullet server starts with SingleUserLocalProfile
- **THEN** it derives the configured tenant and actor server-side, bootstraps the local owner context, exposes no login flow, rejects any client identity override and listens on loopback unless a later authenticated profile is explicitly configured

#### Scenario: M1 resolves or imports the configured repository
- **WHEN** RepositorySelection or RepositoryImport needs GitHub read access
- **THEN** the trusted host adapter reads the configured env or mounted read-only credential, enforces the single-repository allowlist and never persists the secret in Task, Trace, Artifact or Workspace metadata

#### Scenario: M1 Worker starts
- **WHEN** RuntimeProvisioner materializes the imported repository for Codex
- **THEN** the Worker container receives only its isolated WorkingCopy and no GitHub token, credential helper, provider refresh secret or remote Git/`gh` capability

#### Scenario: First-time user bootstraps Workspace inline
- **WHEN** the user has no suitable Workspace and explicitly selects create-from-GitHub in the Worker-first form
- **THEN** the UI starts separate idempotent CreateWorkspace and RepositoryImport operations, renders their progress inline, creates no Task, and enables TaskSpec preview only after the first readable WorkspaceRevision is returned and explicitly selected

#### Scenario: User selects a movable repository ref
- **WHEN** the configured local actor selects a branch or tag before repository-backed Workspace creation
- **THEN** the trusted host repository adapter resolves it outside the database transaction and returns a short-lived server RepositorySelectionSnapshot bound to configured actor, tenant, stable repository identity, requested ref, exact commit SHA, observation time and canonical digest

#### Scenario: User confirms repository selection
- **WHEN** CreateWorkspace receives a valid unexpired matching RepositorySelectionSnapshot
- **THEN** it revalidates current repository read authority, persists the exact commit SHA in RepositoryImport and never performs provider network access inside the transaction

#### Scenario: Remote ref moves after confirmation
- **WHEN** the selected branch or tag points to another commit after RepositoryImport has accepted its Snapshot
- **THEN** import, retry, first WorkspaceRevision, Task submission and Worker dispatch continue using the persisted SHA and never silently re-resolve the ref

#### Scenario: Pinned commit cannot be fetched
- **WHEN** force-push, deletion or provider retention makes the persisted SHA unavailable
- **THEN** import fails to `setup_failed` with evidence and does not fall forward to the new ref; using latest requires a new explicit RepositorySelectionSnapshot

#### Scenario: Task is submitted while import is incomplete
- **WHEN** TaskSpec references a Workspace whose initial RepositoryImport has not produced a readable WorkspaceRevision
- **THEN** deterministic validation blocks submission without Task, TaskTransition, Outbox or WorkerSession creation and without retrying clone inside the Task transaction

#### Scenario: Repository import fails
- **WHEN** provider authorization, fetch, validation or materialization fails before the first readable WorkspaceRevision commits
- **THEN** RepositoryImport appends a sanitized failed attempt, Workspace becomes `setup_failed`, active revision remains absent, and no Task, WorkerSession, RepositoryCheckout or partial WorkspaceRevision is created

#### Scenario: User retries failed import
- **WHEN** an authorized user submits RetryRepositoryImport with the original import identity, expected revision and a new command idempotency key
- **THEN** the system revalidates current repository read authority, appends at most one new attempt, and never creates a second Workspace for response loss or duplicate delivery

#### Scenario: Repository import succeeds
- **WHEN** a validated repository materialization is ready to commit
- **THEN** one PostgreSQL transaction creates RepositoryCheckout and the first WorkspaceRevision, sets ActiveRevisionPointer, marks Workspace `ready`, records import result and writes Outbox

#### Scenario: User previews a valid form
- **WHEN** the user provides the required selections and intent fields
- **THEN** the server derives configured actor, local policy, AgentProfile, output and fixed-deadline fields and returns the exact human-readable preview and canonical digest that confirmation will authorize

#### Scenario: User confirms the preview
- **WHEN** the configured local actor submits the exact preview revision with an idempotency key
- **THEN** Gateway records non-model creation-intent evidence and the same typed command path used by API clients validates and creates at most one Task

#### Scenario: Deterministic form validation fails
- **WHEN** a required field, explicit Workspace, configured repository/base/read policy, revision or platform-policy check fails before confirmation
- **THEN** the system returns structured field or form blockers and creates no Task, TaskTransition, Outbox, WorkerSession or Workspace

#### Scenario: Worker discovers a semantic blocker after creation
- **WHEN** the real Agent finds a conflicting goal, unverifiable acceptance criterion or missing context that deterministic validation could not establish
- **THEN** the current AgentTurn terminates, Task enters `waiting_user`, WorkerDetail asks the user directly, and an authenticated reply starts a new independent fixed 24-hour AgentTurn without Assistant mediation

### Requirement: New Task creation from Worker context requires explicit authenticated intent
The system SHALL allow Worker Agent to suggest a new tracked Task, SHALL create it only from explicit authenticated user or trusted-channel intent, SHALL reject model-inferred creation, and SHALL atomically create the new Task, initial TaskTransition and owner-stream Outbox under one idempotency key without requiring a FollowUpTaskProposal aggregate.

#### Scenario: Worker recommends stronger tracking
- **WHEN** direct chat expands into work that would benefit from independent status, retry, or acceptance tracking
- **THEN** Worker may present a suggestion but does not create Task, WorkerRun or runtime reservation

#### Scenario: User explicitly requests a new tracked Task
- **WHEN** an authenticated user confirms an exact TaskSpec through UI, structured command, or unambiguous create instruction
- **THEN** Gateway records non-model intent evidence and Task Orchestration revalidates authorization, readiness, TaskSpec digest and policy before creating the new Task

#### Scenario: Model only infers that work is a Task
- **WHEN** Worker Agent classifies conversation scope as a new deliverable without explicit user intent
- **THEN** it may recommend a Task but cannot invoke Task creation or fabricate intent evidence

#### Scenario: Confirmation is delivered twice
- **WHEN** duplicate UI clicks or channel deliveries submit the same idempotency key after Task creation commits
- **THEN** Task Orchestration returns the original Task identity without creating another aggregate

#### Scenario: TaskSpec or authorization is stale
- **WHEN** TaskSpec digest, readiness policy, Workspace Membership, or actor authorization no longer matches at confirmation time
- **THEN** creation fails without a partial Task and returns blockers or a refreshed draft path

#### Scenario: Preferred WorkerSession is busy or expired
- **WHEN** the new Task references an active WorkerSession with an existing Run or a Session that has expired
- **THEN** a valid exact target may queue without interrupting the active Run, while an expired or invalid target is not revived and the Task allocates a new Session in the same authorized Workspace without rewriting source Run ownership

### Requirement: Existing WorkerSession reuse requires exact intent and preserves inherited state
The system SHALL reuse an existing WorkerSession only from authenticated evidence targeting its exact identity, SHALL allocate a new Session when a new Task has no exact target, SHALL preserve the reused Session WorkingCopy and Agent context without implicit reset, and SHALL persist an immutable WorkerRunStartSnapshot before execution mutates state.

#### Scenario: New Task has no Session target
- **WHEN** Assistant, Delivery or API creates an authorized Task without exact WorkerSessionTargetIntent
- **THEN** the Task remains queued while a new Session is allocated, has no existing Session queue position, and is never assigned from recent activity, Workspace equality, a unique idle candidate or model inference

#### Scenario: User explicitly reuses a Worker
- **WHEN** an authenticated Worker Detail action, registered card/command or exact API field targets a valid WorkerSession
- **THEN** the system validates actor, Workspace and target revision, resolves `inherit_current`, and preserves current WorkingCopy, Agent state/context, AgentSessions and uncommitted changes

#### Scenario: Reused Session is dirty
- **WHEN** the explicitly targeted Session contains uncommitted changes or prior Agent context
- **THEN** the new WorkerRun inherits and snapshots that actual state, and the platform does not reset, clean, stash, checkout or replace it silently

#### Scenario: Task requires a clean or exact baseline
- **WHEN** TaskSpec requires clean state or an exact WorkspaceRevision/checkpoint that does not match the targeted Session
- **THEN** the system allocates a new Session at that baseline when authorized and capacity permits, or keeps the Task queued/blocked for explicit action without mutating the original Session

#### Scenario: WorkerRun is about to execute
- **WHEN** a Session and dispatch permit have been accepted for a WorkerRun
- **THEN** Worker Runtime persists WorkerRunStartSnapshot with context policy, AgentSession refs, WorkingCopy checkpoint and manifest digest, base revision, dirty state and prior lineage before the first Agent prompt, Tool call or WorkingCopy write

#### Scenario: Queued Task is reassigned to an existing Session
- **WHEN** TaskInitiator explicitly targets another valid WorkerSession
- **THEN** a new exact target intent is required, the Task inherits the target Session current state and joins its queue tail; choosing fresh or exact-base instead allocates a new Session

### Requirement: WorkerSession continuity quota counts retained resumable Sessions
The system SHALL enforce a default WorkerSessionContinuityQuota of 10 per Tenant/User, SHALL count every non-terminal Session retaining continuity including unreconciled lost state, SHALL exclude queued unassigned Tasks and pending continuations, SHALL keep all capacity-blocked new-Session requests in one stable FIFO, and SHALL NOT automatically reuse, reset, close or evict a Session to create capacity.

#### Scenario: User reaches the continuity limit
- **WHEN** the Tenant/User already owns 10 Sessions in `allocating`, `interactive`, `running`, `recovering`, `waiting_user`, `idle` or unreconciled `lost`
- **THEN** a valid untargeted Task remains `queued` with no WorkerSession, records `allocation_wait_reason=session_capacity` and a stable `session_allocation_sequence`, and starts no execution or idle clock

#### Scenario: Terminal continuation waits at the limit
- **WHEN** a valid continuation request targets an exact terminal checkpoint while the Tenant/User has no continuity slot
- **THEN** it remains pending without a derived Worker identity, joins the same SessionAllocationQueue as Task requests, starts no Session clock and cannot bypass an earlier legal request

#### Scenario: Waiting and idle Sessions use little CPU
- **WHEN** a Session is `waiting_user` or `idle` but retains WorkingCopy and Agent continuity
- **THEN** it still consumes one continuity slot even though current container or CPU utilization may be low

#### Scenario: A Session is lost
- **WHEN** runtime ownership or cleanup cannot yet prove whether a `lost` Session has terminated
- **THEN** the Session continues to count until owner reconciliation commits recovery or a terminal `closed/expired` transition

#### Scenario: Capacity becomes available
- **WHEN** a Session commits terminal state and quota release while Task or continuation requests wait in SessionAllocationQueue
- **THEN** WorkerSessionAllocator revalidates the earliest legal allocation sequence and Worker Runtime atomically reserves one slot and creates exactly one `allocating` Session under quota revision and idempotency fencing

#### Scenario: Allocation response is lost
- **WHEN** Worker Runtime commits quota reservation and WorkerSession creation but the response or owner event is retried
- **THEN** the same idempotency key returns the original WorkerSession and does not increment quota or create another Session

#### Scenario: Task is cancelled before it claims an allocated Session
- **WHEN** Task becomes invalid between Session allocation and assignment
- **THEN** the unclaimed Session is fenced and terminally released through idempotent reconciliation without becoming an interactive orphan

#### Scenario: User explicitly reuses an existing Session while full
- **WHEN** exact WorkerSessionTargetIntent points to a valid already-counted Session
- **THEN** reuse consumes no new continuity slot, preserves that Session state, and follows its SessionTaskQueue without evicting any Session

#### Scenario: Capacity release notification is lost
- **WHEN** terminal Session and quota release commit but an allocation owner misses the immediate event
- **THEN** reconciliation observes authoritative quota revision and waiting allocation head and issues at most one effective allocation command

### Requirement: Terminal WorkerSession continues only through a newly derived identity
The system SHALL keep `closed` and `expired` WorkerSessions immutable and read-only, SHALL require explicit actor intent targeting an exact durable checkpoint to continue work, SHALL create a new WorkerSession, WorkingCopy, AgentSession, clocks and authority chain, and SHALL preserve auditable source lineage without inheriting Session-scoped authority.

#### Scenario: User inspects a terminal Worker
- **WHEN** a viewer opens a `closed` or `expired` WorkerSession they are authorized to read
- **THEN** Timeline, Trace, Artifact, Task/Run lineage and checkpoint metadata remain visible, while AgentTurn, WorkerRun, runtime, approval and credential actions are disabled

#### Scenario: User continues from an exact checkpoint
- **WHEN** an authenticated user submits CreateWorkerSessionContinuation for exact terminal Session revision and readable durable checkpoint
- **THEN** the system revalidates Tenant/WorkspaceMembership and policy, creates a new WorkerSession identity, isolated WorkingCopy and new AgentSession, records continued-from refs and starts new idle/absolute clocks

#### Scenario: Continuation would inherit old authority
- **WHEN** the source has Runtime generations, queue positions, ApprovalGrants, RepositoryAccessGrants, SecretLeases or persisted AgentSession identity
- **THEN** none transfers to the derived Session; required authority is freshly evaluated and explicit ContextPack/Artifact refs are permission-filtered again

#### Scenario: Final checkpoint could not be proven
- **WHEN** forced expiry or failure prevents a final durable checkpoint
- **THEN** the system offers only the latest proven checkpoint with capture time, digest and coverage warning and never claims that uncheckpointed state was preserved

#### Scenario: Live Session runtime is replaced
- **WHEN** an eligible RuntimeInstance failure is recovered inside the same non-terminal WorkerSession
- **THEN** the replacement reuses mutable WorkingCopy, Agent state and Trace/control cursors from WorkspaceStorage without requiring a new immutable continuation checkpoint

#### Scenario: Semantic continuation boundary occurs
- **WHEN** AgentTurn becomes terminal, WorkerSession enters `waiting_user`, a stage result is published or graceful close begins
- **THEN** Worker best-effort captures an immutable continuation checkpoint with capture time, digest and coverage, while P0 promises no periodic interval, byte threshold or numeric RPO

#### Scenario: Continuation command is repeated
- **WHEN** the command or allocation response is retried with the same idempotency key
- **THEN** the same derived WorkerSession identity is returned without another quota reservation, WorkingCopy or AgentSession

#### Scenario: Source or checkpoint is stale or unreadable
- **WHEN** source revision changed, checkpoint evidence is mutable/unproven, Workspace access is revoked or the request crosses Tenant boundary
- **THEN** continuation fails closed without disclosing protected content, selecting another checkpoint automatically or reopening the source identity

#### Scenario: Derived materialization fails after allocation
- **WHEN** the new Session slot was reserved but WorkingCopy materialization or owner binding cannot complete
- **THEN** the allocation is fenced and reconciled to a non-interactive terminal release without leaving an orphan Session

### Requirement: WorkerSession absolute expiry preserves Task identity through explicit new-Run continuation
The system SHALL enforce WorkerSession absolute expiry as a terminal execution boundary, SHALL prove the old Runtime lost WorkingCopy write authority before releasing Session/quota, SHALL terminate any active WorkerRun without migration, SHALL preserve Task identity and partial/complete evidence, and SHALL require TaskInitiator to explicitly allocate a new Session and WorkerRun before continuing the same Task.

#### Scenario: Active Task Session expires safely
- **WHEN** a WorkerSession reaches its absolute deadline and Worker Runtime fences new commands and leases, completes bounded cancellation/drain, revokes old runtime write authority and proves WorkingCopy exclusivity
- **THEN** the active WorkerRun becomes `timed_out` with reason `worker_session_absolute_expired`, the Session becomes `expired`, quota is released, and one idempotent WorkerSessionExpiryOutcome records exact revisions, checkpoint, Artifact completeness, fencing evidence, external-effect state and bound Task

#### Scenario: Old runtime cannot be fenced
- **WHEN** absolute expiry cannot prove the old container or runtime lost WorkingCopy write authority
- **THEN** the Session becomes or remains `lost`, continues consuming continuity quota, publishes no successful terminal expiry outcome and cannot be used as a writable continuation source

#### Scenario: Non-terminal Task receives an expiry outcome
- **WHEN** an exact current expiry outcome references a Task in `queued`, `running` or `waiting_user`
- **THEN** Task Orchestration atomically moves or keeps the Task at `waiting_user` with reason `worker_session_expired`, clears current Session/Run refs only after appending TaskSessionLineage, and writes one Outbox event

#### Scenario: Delivered Task Session expires
- **WHEN** an exact expiry outcome references a Task in `delivered`
- **THEN** the Task remains `delivered`, complete result refs remain acceptable, the current Session binding moves to lineage, and rework requires explicit new-Worker continuation

#### Scenario: Expiry outcome reaches a terminal Task
- **WHEN** Task is already `completed`, `failed` or `cancelled`
- **THEN** the outcome is an idempotent Task-state no-op while Worker and Trace evidence remain queryable

#### Scenario: Expiry drain preserves an incomplete artifact
- **WHEN** report or patch content is durable but acceptance work was incomplete at expiry
- **THEN** ArtifactRef remains visible with `completeness=partial`, source Run/checkpoint and expiry reason, and cannot independently satisfy automatic acceptance

#### Scenario: External side effect remains unknown
- **WHEN** the expired Run may have performed a non-idempotent external write whose result cannot be proven
- **THEN** Task remains `waiting_user` with reconcile, confirm, compensate or cancel actions and continuation cannot authorize blind repetition

#### Scenario: Initiator continues the same Task
- **WHEN** TaskInitiator submits ContinueTaskInNewWorker with exact Task revision, expiry outcome, readable checkpoint and idempotency key after all external-effect blockers are resolved
- **THEN** one Task transaction moves `waiting_user` or `delivered` to `queued`, records intent and lineage and writes Outbox; WorkerSessionAllocator then applies normal continuity quota/FIFO, and normal dispatch creates one new `task_session_continuation` WorkerRun whose start snapshot references the source Session/Run/outcome/checkpoint

#### Scenario: Continuation command or expiry outcome is redelivered
- **WHEN** the same outcome or continuation idempotency key arrives after its owner transaction committed
- **THEN** the system returns the original Task/allocation result and does not append duplicate lineage, reserve another Session or create another WorkerRun

#### Scenario: Old execution authority reaches the new Run
- **WHEN** the source Run or Session had ApprovalGrant, RepositoryAccessGrant, SecretLease, queue position, runtime generation or AgentSession identity
- **THEN** none transfers to the new Session/Run; fresh authority is resolved under current policy and each later AgentTurn receives the fixed 24-hour deadline

### Requirement: WorkerSession serializes top-level Task and WorkerRun
The system SHALL allow at most one bound Task and one active top-level WorkerRun per WorkerSession, SHALL keep conflicting Tasks queued or place parallel work in another WorkerSession, SHALL distinguish Task scheduling queue from AgentTurn queue, and SHALL fence slot acquisition by session revision, runtime generation and idempotency key.

#### Scenario: A new Task targets a busy WorkerSession
- **WHEN** Worker Runtime receives StartWorkerRun for Task B while Task A or Run A owns the Session slot
- **THEN** it returns stable `session_busy`, does not interrupt A or create Run B, and Task Orchestration leaves B queued or offers an explicit new WorkerSession

#### Scenario: Duplicate start command is delivered
- **WHEN** the same StartWorkerRun idempotency key is received after its Run was accepted but the response was lost
- **THEN** Worker Runtime returns the original WorkerRun identity and does not create another top-level Run

#### Scenario: Manual delivery awaits acceptance
- **WHEN** a bound Task enters `delivered`
- **THEN** it keeps the WorkerSession Task binding while no WorkerRun is active, so acceptance or rework remains attached to the same Agent and WorkingCopy; only completed, failed, cancelled, or a successfully fenced absolute-expiry outcome releases the current binding, and expiry preserves it in TaskSessionLineage

#### Scenario: Direct standalone prompt races queued work
- **WHEN** a Task has already acquired the Session slot or another Task remains bound
- **THEN** a direct prompt cannot start a standalone WorkerRun or bypass the bound Task

#### Scenario: Agent uses a subagent
- **WHEN** the active Coding Agent starts child work inside one WorkerRun
- **THEN** the child activity may execute concurrently but shares the top-level Run policy and is recorded as AgentSession/AgentExecutionSpan lineage rather than a second top-level WorkerRun

### Requirement: SessionTaskQueue uses stable FIFO without preemption
The system SHALL assign a stable monotonic queue sequence per WorkerSession, SHALL dispatch only the legal queue head through a short-lived single-use permit, SHALL let queued Tasks be cancelled or explicitly reassigned to another Session tail, and SHALL NOT support P0 priority scheduling, reorder, preemption or standalone-prompt overtaking.

#### Scenario: Two Tasks enqueue for one busy Session
- **WHEN** Task B enqueue commits before Task C for the same WorkerSession
- **THEN** B receives the lower `session_queue_sequence`, and C cannot dispatch before B while B remains a legal queued head

#### Scenario: Enqueue response is lost
- **WHEN** the same enqueue idempotency key is retried after its transaction committed
- **THEN** Task Orchestration returns the original queue sequence and does not append a duplicate position

#### Scenario: Queue head is cancelled
- **WHEN** an authorized actor cancels the queued head before its dispatch permit is consumed
- **THEN** cancellation and permit invalidation converge idempotently, the Task becomes cancelled without a WorkerRun, and the next sequence becomes eligible

#### Scenario: Queued Task is moved for parallel work
- **WHEN** TaskInitiator explicitly reassigns a still-queued Task to another valid WorkerSession
- **THEN** Task Orchestration removes its old queue position, appends it to the target Session tail with a new sequence, and does not carry priority or interrupt either Session

#### Scenario: Standalone prompt races slot release
- **WHEN** the Session slot becomes free while a Task remains queued and a later direct prompt requests a standalone WorkerRun
- **THEN** only the queue head can receive the dispatch permit, and the standalone request is rejected with a queue-pending action path or directed to another WorkerSession

#### Scenario: Task priority differs from enqueue order
- **WHEN** a later Task has higher display or business priority than the queue head
- **THEN** P0 preserves FIFO and does not reorder or preempt; the user may cancel or explicitly reassign work instead

#### Scenario: Slot-release notification is lost
- **WHEN** Worker Runtime releases a slot but the immediate scheduling event or response is lost
- **THEN** reconciliation detects the free Session and queue head and issues at most one valid permit without creating a duplicate Run

### Requirement: WorkerSession uses user-scoped interactive runtime lease
The system SHALL create or explicitly reuse a user-scoped WorkerSession bound to exactly one durable Workspace with RuntimeLease before executing a WorkerRun, enforce a 48-hour idle timeout, a 14-day absolute lifetime, and P0 continuity quota of 10 per Tenant/User, and reclaim the runtime after lease termination without conflating Workspace and RuntimeInstance identity. M1 SHALL resolve RuntimeResidencyPolicy to `m1_resident_until_session_terminal_v1` and keep one RuntimeInstance running throughout `interactive`, `running`, `waiting_user` and `idle` Session states.

#### Scenario: AgentTurn or WorkerRun reaches a stage terminal state
- **WHEN** an AgentTurn ends, a WorkerRun delivers a stage result, or Task enters `waiting_user` while WorkerSession remains live
- **THEN** RuntimeInstance, Shim, Codex ACP and LiteLLM remain running with the same runtime generation, and no planned stop, pause, hibernation or resume transition occurs

#### Scenario: Resident Session has no active AgentTurn
- **WHEN** WorkerSession is idle or waiting and has no active AgentTurn or SessionDispatchPermit
- **THEN** Runtime remains healthy for heartbeat, control and Trace/Artifact drain but cannot autonomously start an ACP prompt, Tool or external action

#### Scenario: Resident Runtime reaches a stop boundary
- **WHEN** WorkerSession is explicitly closed, reaches idle or absolute expiry, is terminated for authorization/security, or enters recovery fencing
- **THEN** RuntimeSupervisor stops or replaces RuntimeInstance under the corresponding terminal or recovery protocol and does not create a suspended Session state

### Requirement: M1 uses one fixed Runtime resource profile and aggregate capacity admission
The system SHALL resolve every M1 RuntimeLease to server-owned `local_standard_v1` with 2 vCPU, 4 GiB memory, 512 PIDs and a 10 GiB container writable layer, SHALL keep persistent WorkingCopy/cache/Agent state outside that layer, SHALL reject arbitrary client or Agent resource values, and SHALL admit AgentTurn dispatch against aggregate Docker Engine budgets that default to at most 80% availability.

#### Scenario: RuntimeInstance is provisioned
- **WHEN** RuntimeSupervisor creates an M1 Worker container
- **THEN** it applies the exact `local_standard_v1` revision through Docker/cgroup/storage enforcement and records the profile in RuntimeLease

#### Scenario: Queue head has insufficient aggregate capacity
- **WHEN** SessionTaskQueue head or standalone start cannot fit the current RuntimeCapacityAdmission budget
- **THEN** WorkerSession remains resident with its FIFO position and `wait_reason=runtime_capacity`, no SessionDispatchPermit or active AgentTurn is created, and no `started_at` or 24-hour deadline begins

#### Scenario: Capacity becomes available
- **WHEN** an earlier AgentTurn terminates and releases capacity or observed/configured capacity otherwise becomes available
- **THEN** the earliest legal queue head is re-evaluated, one idempotent admission evidence is bound to SessionDispatchPermit, and later work cannot bypass it

#### Scenario: Runtime exceeds CPU quota
- **WHEN** Worker workload consumes more than its CPU share
- **THEN** Docker/cgroup throttles it without treating throttling alone as runtime failure

#### Scenario: Runtime reaches a hard resource limit
- **WHEN** Docker or the runtime adapter confirms OOM, PID exhaustion or writable-layer exhaustion
- **THEN** Worker records `resource_exhausted`, fences affected Tool/Turn work and enters an explainable waiting state without opening ordinary automatic recovery that would repeat under the same limit

#### Scenario: Workspace has no repository
- **WHEN** a WorkerSession performs research, experimental design, document work, or other non-code activity
- **THEN** its Workspace remains valid with zero RepositoryCheckouts and can attach or create a repository later

#### Scenario: Workspace contains multiple repositories
- **WHEN** a delivery requires coordinated changes across repositories
- **THEN** one Workspace records multiple RepositoryCheckouts with independent repository identity, revision, path, and access mode

#### Scenario: Multiple WorkerSessions share a Workspace
- **WHEN** another WorkerSession is opened for the same Workspace
- **THEN** both sessions reference the same workspace id while owning separate WorkingCopies based on a WorkspaceRevision and keeping separate WorkerSession, AgentSession, WorkerRun, RuntimeInstance, and trace identities

### Requirement: Workspace writes use isolated WorkingCopies and serialized ChangeSets
The system SHALL prevent WorkerSessions from directly sharing a writable authoritative Workspace tree, SHALL persist each WorkingCopy and durable Agent state in host-managed storage outside the disposable container layer, SHALL mount only the owning session WorkingCopy into a RuntimeInstance, and SHALL advance Workspace active revision only through a serialized fenced WorkspaceApply of a validated complete StagingRevision.

#### Scenario: Parallel sessions produce changes
- **WHEN** two WorkerSessions start from the same WorkspaceRevision
- **THEN** each modifies its own WorkingCopy and produces an independently reviewable ChangeSet without observing uncommitted filesystem writes from the other session

#### Scenario: Runtime replacement reuses persistent WorkingCopy
- **WHEN** a failed RuntimeInstance is replaced after the old generation loses write access
- **THEN** the new container bind mounts the same host-managed session WorkingCopy and durable Agent state without mounting any shared authoritative Workspace path

#### Scenario: Complete candidate is prepared outside the write lease
- **WHEN** WorkspaceApply validates Git commits, non-repository manifests, checksums, tests, and all components of a ChangeSet
- **THEN** it creates one immutable non-authoritative StagingRevision without holding WorkspaceWriteLease and does not change the active WorkspaceRevision

#### Scenario: First ChangeSet finalizes successfully
- **WHEN** WorkspaceApply acquires the short PostgreSQL lease and current fencing token, revalidates the expected active revision and staging digest, and commits Finalize
- **THEN** one transaction creates WorkspaceRevision, advances the active pointer, records the Apply result and Outbox event, and marks the ChangeSet applied without performing large filesystem work in the transaction

#### Scenario: Second ChangeSet has stale base
- **WHEN** a ChangeSet expects an older WorkspaceRevision after another ChangeSet has advanced the Workspace
- **THEN** WorkspaceApply returns `needs_rebase` or `conflicted`, leaves the active revision unchanged, and never uses last-write-wins

#### Scenario: Multi-component apply fails
- **WHEN** any repository or non-repository component fails validation or staging
- **THEN** no new active WorkspaceRevision is exposed, partial staging is isolated for cleanup, and the ChangeSet remains unapplied

#### Scenario: Apply lease expires before commit
- **WHEN** WorkspaceApply loses its lease or presents an obsolete fencing token before advancing the active revision
- **THEN** the commit is rejected, the active revision remains unchanged, and staged output is not exposed as authoritative

#### Scenario: Apply crashes before database commit
- **WHEN** the process crashes after preparing staging but before the Finalize transaction commits
- **THEN** the active revision remains unchanged, the lease can expire, and a janitor can clean orphan staging after idempotent recovery checks

#### Scenario: Apply response is lost after database commit
- **WHEN** the Finalize transaction commits but the caller does not receive the response and retries the same idempotency key
- **THEN** the system returns the original result revision and does not create another WorkspaceRevision

#### Scenario: User continues before idle timeout
- **WHEN** a user sends a follow-up interaction before WorkerSession idle timeout
- **THEN** the system resumes the existing WorkerSession by default and starts a new WorkerRun in that session using ACP chat or command interaction

#### Scenario: User activity renews idle deadline
- **WHEN** a valid user interaction occurs before the idle deadline
- **THEN** the system renews the idle deadline by 48 hours without extending the 14-day absolute deadline

#### Scenario: Session waits for approval
- **WHEN** WorkerSession enters `waiting_user`
- **THEN** the system pauses its idle timeout until the approval decision while continuing the 14-day absolute lifetime

#### Scenario: WorkerSession idle timeout
- **WHEN** WorkerSession exceeds its idle timeout
- **THEN** the system reclaims the runtime, emits an event, drains available trace, and releases the runtime lease

#### Scenario: WorkerSession absolute timeout
- **WHEN** WorkerSession reaches 14 days since creation
- **THEN** the system rejects further interactions for that session, drains trace and artifacts, emits a timeout event, and reclaims the runtime lease

#### Scenario: WorkerSession concurrency limit
- **WHEN** a user already has 10 active WorkerSessions
- **THEN** the system rejects or queues new WorkerSession allocation according to policy and records the limit decision

#### Scenario: WorkerSession mapping recovered
- **WHEN** Worker service recovers after a crash and finds an existing runtime lease
- **THEN** the system reuses the original worker session id, increments runtime generation, binds one new active RuntimeInstance/container, and preserves historical RuntimeInstance and AgentSession mappings

### Requirement: Worker runs Codex first through an extensible agent adapter model
The system SHALL use Codex as the P0 real Coding Agent inside a container, use a Go runtime shim with `github.com/coder/acp-go-sdk` to control `agentclientprotocol/codex-acp` through local stdio ACP, and keep AgentProfile and AgentAdapter selection extensible for later agents.

#### Scenario: Codex agent execution
- **WHEN** TaskSpec selects the P0 CodexAgentProfile
- **THEN** the container-side runtime shim starts or connects to Codex ACP server through CodexAdapter, communicates with Switchback backend over the network, and maps ACP events into TraceRecord and IntegrationEvent outputs

#### Scenario: Multiple explicit agent sessions inside one WorkerSession
- **WHEN** the user or an adapter creates another explicit ACP/Codex session
- **THEN** Worker records each concrete agent session id under the same worker session id without creating another container

#### Scenario: Codex creates a subagent
- **WHEN** codex-acp maps a Codex collaborative Agent item into an ACP tool call with child thread metadata
- **THEN** Worker records an AgentExecutionSpan and raw child thread references without inventing another ACP AgentSession unless an adapter-specific contract explicitly promotes it

#### Scenario: Future agent profile selection
- **WHEN** TaskSpec selects a later ACP-capable AgentProfile
- **THEN** Worker starts the agent through the matching AgentAdapter without changing TaskSpec lifecycle semantics

### Requirement: ACP integration is capability-gated and raw-frame preserving
The system SHALL persist the ACP initialize protocol/capability snapshot, invoke optional methods only when advertised, capture bounded inbound and outbound ACP JSONL before typed decoding, preserve unknown optional updates for audit, and fail closed when version, permission, or security semantics cannot be determined.

#### Scenario: Optional capability is absent
- **WHEN** the active AgentProfile does not advertise resume, load, list, close, or another optional ACP capability
- **THEN** CodexAdapter does not invoke that method and selects only an explicitly supported path or reports a structured unsupported-capability result

#### Scenario: Agent emits an unknown optional update
- **WHEN** typed ACP decoding encounters an unrecognized non-security session update discriminator
- **THEN** the raw frame remains in Trace, an internal compatibility warning is emitted, and the healthy runtime is not terminated solely because of that update

#### Scenario: ACP semantics affect security
- **WHEN** protocol negotiation fails or an unknown method/update affects permission, authentication, or another security decision
- **THEN** the adapter rejects the operation, records raw evidence, and does not infer a permissive fallback

### Requirement: Worker uses CI/CD runner style local container runtime
The system SHALL provision Worker runtime through a RuntimeProvisioner that follows CI/CD runner style local Docker-compatible container execution built from Docker images. In M1, RuntimeSupervisor SHALL run as a Go component inside the Docker Compose-managed `switchback-backend` container and SHALL use a Docker Socket mounted only into that trusted Backend to create sibling WorkerSession containers under the same Docker Engine.

#### Scenario: Backend creates a sibling Worker container
- **WHEN** RuntimeSupervisor provisions a RuntimeInstance for a ready WorkerSession
- **THEN** Docker Engine creates a container sibling to `switchback-backend`, the Worker container receives no Docker Socket and no privileged mode, and it communicates with Switchback Backend only through the approved Docker network and WorkerControlChannel

#### Scenario: Runtime mount sources are resolved
- **WHEN** RuntimeSupervisor prepares WorkingCopy, agent state or generated configuration mounts
- **THEN** every source path is derived and allowlisted under the server-configured Switchback data root, and no client, TaskSpec, Agent, Skill or Worker-provided arbitrary host path is accepted

#### Scenario: Runtime provisioned
- **WHEN** WorkerRun is ready to execute
- **THEN** RuntimeProvisioner creates or reuses a local Docker-compatible container, mounts only that WorkerSession's persistent isolated WorkingCopy and agent state, injects environment, skills, MCP config, co-located LiteLLM config, and short-lived secrets, and exposes only backend-approved network paths

#### Scenario: LiteLLM model proxy configured
- **WHEN** Codex starts inside the WorkerSession container
- **THEN** Codex model provider configuration points model traffic to co-located LiteLLM on localhost using environment variables or mounted config for authentication

#### Scenario: Host isolation
- **WHEN** the Coding Agent performs file or command actions
- **THEN** those actions occur inside the containerized runtime and do not mutate the host machine

### Requirement: M1 Worker runtime permits public Web egress and denies host or private targets
The system SHALL resolve M1 Worker networking to versioned `m1_public_web_restricted_v1`, SHALL allow ordinary public Web dependency and documentation access, SHALL deny Worker ingress and access to Docker, host, metadata, private and control-plane networks, and SHALL NOT allow TaskSpec, Agent, Skill or MCP input to widen that boundary.

#### Scenario: Worker accesses a public package or documentation endpoint
- **WHEN** Worker resolves a public destination and connects over TCP 80 or 443
- **THEN** RuntimeNetworkPolicy permits the connection after checking the resolved address and rechecks every redirect while recording destination and decision metadata

#### Scenario: Worker targets a sensitive network
- **WHEN** Worker targets Docker API, host gateway, `host.docker.internal`, metadata/link-local, RFC1918, CGNAT, IPv6 private/link-local, another Worker or a control-plane infrastructure service
- **THEN** the connection is denied before dispatch except for exact configured Backend control/upload and DNS resolver endpoint-port pairs, and IPv6 is disabled when equivalent filtering cannot be proven

#### Scenario: Worker requires a non-Web or private endpoint
- **WHEN** Worker requests SSH, another non-Web protocol, a private endpoint or an additional destination
- **THEN** execution remains blocked until an ApprovalGrant matches exact destination, protocol, port, once/session scope and expiry, and the platform policy may still deny the exception

#### Scenario: Runtime receives inbound traffic
- **WHEN** RuntimeInstance is provisioned
- **THEN** it publishes no host/public port and initiates WorkerControlChannel to Backend itself, while only Backend PreviewProxy may later reach an exact running SessionProcess declared HTTP/WebSocket port over the internal network

### Requirement: Every actual AgentTurn uses one fixed 24-hour hard deadline
The system SHALL assign every actual top-level ACP prompt an independent `86400s` wall-clock hard deadline, SHALL never pause or extend that deadline, SHALL fence timeout cancellation before later work starts, and SHALL NOT expose WorkerRun, Task, Tool or continuation time-budget protocols.

#### Scenario: Worker starts an ACP prompt
- **WHEN** backend dispatches an actual top-level ACP prompt
- **THEN** one transaction persists server-observed `started_at` and `deadline_at=started_at+86400s`, schedules one idempotent deadline command and appends `StartAgentTurn` Outbox; Shim validates exact Turn revision, Runtime generation, ChannelEpoch and freshness, arms a matching non-resettable monotonic watchdog for the conservative remaining duration, and only then invokes ACP without another Ready/Permit round trip

#### Scenario: Start command is delayed or redelivered
- **WHEN** `StartAgentTurn` is stale, expired, has unverifiable server-time freshness or is redelivered after its watchdog/prompt identity was already accepted
- **THEN** Shim fails closed or returns the original acceptance, never resets the watchdog and never invokes ACP twice; dispatch latency and uncertainty margin only reduce the usable fixed window

#### Scenario: Control channel disconnects during a Turn
- **WHEN** backend deadline delivery is unavailable after an AgentTurn has started
- **THEN** the Runtime watchdog still requests the same idempotent ACP/Tool cancellation and uploads its evidence after reconnect, while backend revision/generation fencing remains the only authority that commits terminal state

#### Scenario: Turn waits or runtime recovers
- **WHEN** the AgentTurn is in model inference, Tool execution, active_quiet, approval/waiting_user, transport disconnection, confirmed platform outage or Runtime recovery
- **THEN** its wall-clock deadline continues unchanged and no pause segment or replacement duration is created

#### Scenario: User steers or interrupts
- **WHEN** the user sends steer against the active AgentTurn
- **THEN** Worker records the control action without changing `started_at` or `deadline_at`; interrupt terminates the current Turn instead

#### Scenario: Queued user message follows a terminal Turn
- **WHEN** an authenticated queued user message is ready after the current Turn reaches terminal state and cancellation/fencing has converged
- **THEN** Worker starts one new independent AgentTurn with its own 24-hour deadline

#### Scenario: Agent attempts to evade the deadline
- **WHEN** Agent output, automatic retry or Runtime recovery attempts to create another top-level AgentTurn without authenticated user input
- **THEN** Worker rejects the start and preserves the current Turn deadline/terminal state

#### Scenario: AgentTurn deadline expires
- **WHEN** the exact backend deadline command or matching Runtime watchdog fires for a non-terminal Turn
- **THEN** Worker requests ACP cancel, cancels the active Tool/process, fences late results and converges AgentTurn to `timed_out:agent_turn_hard_deadline`

#### Scenario: Timed-out Task interaction is safely cancelled
- **WHEN** Agent and Tool cancellation are confirmed for a Task-bound timed-out Turn
- **THEN** WorkerRun remains resumable, Task moves or remains at `waiting_user:agent_turn_timed_out`, and the user may start a new independent Turn in the same live Session/Run

#### Scenario: Timed-out operation cannot be proven stopped
- **WHEN** local cancellation or an external non-idempotent effect remains uncertain after Turn timeout
- **THEN** Worker blocks another Turn and follows TaintedRuntime fencing or external-effect reconciliation before resuming

#### Scenario: TaskSpec tries to configure time
- **WHEN** Assistant, Delivery, API, Agent output or a user submits WorkerRun, AgentTurn, Tool or continuation duration/extension fields
- **THEN** deterministic validation rejects or ignores the unsupported fields according to schema compatibility and never changes the fixed hard deadline

#### Scenario: Tool runs within a Turn
- **WHEN** a shell, MCP, Skill or other Tool invocation starts
- **THEN** it inherits the current AgentTurn remaining deadline and has no separate business-level timeout profile, while internal transport/shutdown guards remain implementation details

#### Scenario: Continuation starts later work
- **WHEN** ContinueTaskInNewWorker creates a fresh Session/Run after exact expiry outcome and checkpoint validation
- **THEN** no time-budget confirmation is required and each later actual ACP prompt receives the same independent fixed AgentTurn deadline

### Requirement: Unconfirmed Tool cancellation escalates by side-effect risk
The system SHALL assign each Tool invocation a stable invocation id, runtime generation, resolved effect class and cancellation mode, SHALL fence all terminal invocations, and SHALL distinguish local runtime contamination from uncertain external effects.

#### Scenario: Worker receives an invalid Tool policy snapshot
- **WHEN** ResolvedToolPolicy provenance, scope, content hash, or policy revision is missing, stale, or does not cover the requested Tool invocation
- **THEN** Worker rejects invocation without asking the Agent to reinterpret metadata and records a policy validation failure

#### Scenario: Local Tool cannot be proven stopped
- **WHEN** process-group or cgroup cancellation completes or expires without evidence that the local Tool and descendants stopped
- **THEN** Worker marks RuntimeInstance tainted, pauses new command dispatch, fences the invocation and old runtime generation, and opens one `tool_cancel_unconfirmed_local` RuntimeRecoveryIncident

#### Scenario: Safe remote cancellation is unconfirmed
- **WHEN** a remote Tool is read-only or has a stable idempotency key plus reconciliation evidence but cancellation acknowledgement is unavailable
- **THEN** Worker fences the invocation, records `cancel_unconfirmed_remote_safe`, returns a structured timeout to the Agent, and may continue AgentTurn without rebuilding the local runtime

#### Scenario: External side effect is uncertain
- **WHEN** a remote Tool is non-idempotent, unknown, or lacks verifiable idempotency/reconciliation evidence after timeout
- **THEN** Worker prohibits automatic retry, moves WorkerRun to `waiting_user`, and exposes only actually supported reconcile, confirm-success, compensate, or retry actions

#### Scenario: Tool result arrives after terminal state
- **WHEN** progress, success, or error arrives after ToolInvocation has a terminal timestamp
- **THEN** Worker appends a `late_tool_result` TraceRecord and does not mutate ToolInvocation, AgentTurn, WorkerRun, Artifact, or Workspace state

### Requirement: Cross-Turn background work uses explicit SessionProcess
The system SHALL keep ordinary shell and Tool descendants owned by their AgentTurn, SHALL permit only Worker-managed preview servers, development servers and file watchers to become cross-Turn SessionProcess resources, and SHALL expose their lifecycle without granting additional execution authority.

#### Scenario: Ordinary command attempts to detach
- **WHEN** a shell or Tool process uses `&`, `nohup`, daemonization, stdio detachment or parent exit
- **THEN** it remains in the originating AgentTurn process group/cgroup and Worker drains or terminates it when the Turn becomes terminal rather than adopting it as SessionProcess

#### Scenario: Agent starts an intentional preview service
- **WHEN** an active AgentTurn uses the Worker managed-process interface with an allowed kind, WorkingCopy-relative cwd and declared ports
- **THEN** Worker creates one revisioned SessionProcess bound to the WorkerSession, source Turn/Tool and Runtime generation/process handle, reports `starting -> running|failed`, and captures bounded log references

#### Scenario: Unknown orphan is discovered
- **WHEN** Worker finds an unregistered process after its source AgentTurn is terminal
- **THEN** it does not retroactively promote the process and follows Turn cleanup or TaintedRuntime handling if termination cannot be proven

#### Scenario: Accepted SessionProcess crosses any Turn terminal reason
- **WHEN** managed start acceptance commits before the originating AgentTurn becomes `completed`, `cancelled`, `timed_out` or `failed`, including interrupt represented as cancelled with a terminal reason
- **THEN** the SessionProcess continues under the same Runtime resource, network, WorkingCopy, Tool, Grant and Secret boundaries while Turn cancellation still stops ACP, active Tool and ordinary descendants

#### Scenario: Managed start races with Turn terminality
- **WHEN** managed start and AgentTurn terminal commands race
- **THEN** Worker serializes them by exact Turn revision, Runtime generation and owner commit order; start acceptance first creates a surviving SessionProcess, while terminal first rejects start and prohibits orphan adoption

#### Scenario: Accepted SessionProcess is still starting when Turn terminates
- **WHEN** managed start has committed but the process has not yet reached `running` as the source AgentTurn terminates
- **THEN** SessionProcess continues its own `starting -> running|failed` lifecycle and is not cancelled solely by the Turn result

#### Scenario: Worker detail controls a SessionProcess
- **WHEN** an authorized user opens WorkerDetail or submits stop/restart
- **THEN** the view shows source Turn, Runtime generation, kind, ports, status and bounded logs, and Gateway sends an idempotent command with exact process revision/generation; restart creates a new process identity and lineage

#### Scenario: WorkerSession terminates
- **WHEN** WorkerSession closes, reaches idle or absolute expiry, or is terminated for authorization/security reasons
- **THEN** Worker stops every SessionProcess and fences late process status/log events

#### Scenario: SessionProcess emits ongoing activity
- **WHEN** a process emits logs, serves port traffic, passes health checks or reports status without authenticated user interaction
- **THEN** Worker records bounded observations without renewing the WorkerSession 48-hour idle clock

#### Scenario: Runtime is replaced
- **WHEN** Runtime generation is fenced or replaced while SessionProcesses are running
- **THEN** Worker marks each old-generation process `stopped` when termination is proven or `lost` otherwise, never auto-restarts it, and requires an explicit later action to create a new identity

#### Scenario: Higher-priority boundary occurs after Turn survival
- **WHEN** WorkerSession closes/expires, authorization or security terminates execution, Runtime becomes tainted/fenced, or a resource hard limit requires termination
- **THEN** Worker stops or fences the SessionProcess regardless of its source Turn outcome and records stopped/lost evidence

#### Scenario: Process outlives Turn admission
- **WHEN** AgentTurn terminal releases its compute admission while a SessionProcess continues
- **THEN** the process remains inside the Runtime hard limits and its live resource use remains part of aggregate capacity accounting for later admissions

### Requirement: Backend proxies SessionProcess preview without publishing Worker ports
The system SHALL expose a running preview/development SessionProcess only through a Backend-managed HTTP/WebSocket PreviewProxy, SHALL resolve an exact declared port from trusted current-generation runtime state, and SHALL revoke access at Process、Session、Runtime、authorization or access-lifetime boundaries without accepting arbitrary proxy targets.

#### Scenario: User opens a running preview
- **WHEN** the configured actor submits `OpenSessionProcessPreview` with exact WorkerSession、SessionProcess、expected process revision、declared port and idempotency key
- **THEN** Gateway validates the running process and current Runtime generation and returns one temporary opaque SessionProcessPreviewAccess without exposing the container endpoint

#### Scenario: Preview target is not an exact declared process port
- **WHEN** client、Agent、TaskSpec or preview content supplies a URL、IP、host、undeclared port、Docker mapping、arbitrary TCP/UDP protocol or CONNECT target
- **THEN** Gateway rejects the request and never uses it as a proxy destination

#### Scenario: Browser accesses preview traffic
- **WHEN** a valid PreviewAccess sends an HTTP request or WebSocket upgrade
- **THEN** PreviewProxy revalidates audience/expiry、SessionProcess running status and Runtime generation, strips Switchback cookies/authorization/internal identity/hop-by-hop headers, and forwards only to the resolved internal declared port while Worker publishes no host/public port

#### Scenario: Preview emits bytes and frames
- **WHEN** HTTP bodies or WebSocket frames cross PreviewProxy
- **THEN** they bypass EventCenter、SSE、Trace JSONL and ObjectStore while the platform records only bounded access decision/status/duration/byte-count metadata

#### Scenario: Preview lifecycle boundary occurs
- **WHEN** SessionProcess stops/fails/is lost, WorkerSession becomes terminal, Runtime is fenced/replaced, authorization is revoked or PreviewAccess expires
- **THEN** Backend rejects new requests and actively closes indexed WebSocket/long-lived tunnels, with request-time state revalidation covering a missed lifecycle event

#### Scenario: Preview traffic remains active
- **WHEN** the user explicitly opens a PreviewAccess and its page later loads resources、polls、uses HMR/WebSocket or receives health checks
- **THEN** the explicit OpenPreview may refresh idle once but all subsequent proxy/process traffic does not renew the WorkerSession 48-hour idle clock

#### Scenario: Frontend renders a preview route
- **WHEN** Gateway returns an opaque route reference
- **THEN** Frontend treats its route/origin form as an M1 loopback implementation detail, exposes no container endpoint or control credential, and does not add a stable PreviewOriginPolicy contract

### Requirement: Worker control is transport-independent with fallback
The system SHALL expose one logical WorkerControlChannel initiated outbound by the runtime shim, prefer gRPC bidirectional streaming, fall back to HTTPS long-poll command retrieval plus batched event upload only for transport failures, and use resumable HTTPS for Trace and Artifact payload bytes independently of the active control transport.

#### Scenario: gRPC control is available
- **WHEN** bootstrap negotiation and the outbound gRPC bidirectional stream succeed
- **THEN** commands, ACKs, heartbeats, and realtime events use RuntimeControlEnvelope on one active ChannelEpoch

#### Scenario: gRPC transport is unavailable
- **WHEN** HTTP/2, proxy, connectivity, or transport timeout prevents the gRPC control stream
- **THEN** the shim re-bootstraps, fences the prior epoch, resumes from shared command and event cursors, and uses HTTPS long-poll plus batched event POST without changing command semantics

#### Scenario: Security or protocol validation fails
- **WHEN** authentication, authorization, policy, schema, or protocol-version validation fails
- **THEN** the channel fails closed and does not treat the error as permission to select fallback transport

#### Scenario: A command is redelivered after reconnect
- **WHEN** an uncompleted command is delivered again over a new connection or fallback transport
- **THEN** Worker uses message id, idempotency key, sequence, and received/accepted/completed ACK state to resume or return the prior result without duplicating execution

#### Scenario: An obsolete channel sends data
- **WHEN** a prior ChannelEpoch sends an ACK, heartbeat, or event after a new epoch is active for the same RuntimeInstance generation
- **THEN** the backend rejects the obsolete envelope and does not advance cursors or runtime state

#### Scenario: Trace or Artifact payload is uploaded
- **WHEN** the runtime produces JSONL, report, patch, or other managed bytes
- **THEN** the control envelope carries only upload intent, reference, and checksum while the payload uses a resumable HTTPS upload

### Requirement: Worker Supervisor performs one safe automatic recovery per incident
The system SHALL create a RuntimeRecoveryIncident for eligible runtime or Agent failures, automatically replace RuntimeInstance at most once per continuous incident, preserve durable session state, and escalate to `waiting_user` when that recovery does not safely restore the prior AgentSession.

#### Scenario: First eligible runtime failure is recovered
- **WHEN** a process exits, container is lost, heartbeat expires, the ACP connection closes, or cancellation grace expires without a terminal prompt response while WorkerSession remains within its lease and absolute lifetime
- **THEN** Supervisor fences the old runtime generation and ChannelEpoch, creates exactly one replacement generation, mounts the same WorkingCopy and durable Agent state, restores confirmed control and Trace cursors, issues fresh SecretLeases, and attempts to resume the prior ACP AgentSession

#### Scenario: Replacement reaches stable healthy state
- **WHEN** the replacement control channel, LiteLLM, workspace mount, ACP Agent, and resumed AgentSession pass health checks and satisfy a configured stability window or durable progress checkpoint
- **THEN** the RuntimeRecoveryIncident closes as recovered, WorkerRun continues without becoming a new Task or WorkerRun, and a later independent failure may open a new incident

#### Scenario: Automatic recovery fails again
- **WHEN** the only automatic replacement fails, ACP session resume fails, or state consistency cannot be proven
- **THEN** Supervisor does not create another replacement or silently create a new AgentSession, and WorkerRun and WorkerSession enter `waiting_user` with recovery evidence and explicit user actions

#### Scenario: Failure is not eligible for automatic recovery
- **WHEN** execution is cancelled, idle or absolute lifetime expires, or authentication, authorization, policy, schema, or protocol validation fails
- **THEN** Supervisor does not open an automatic RuntimeRecoveryIncident and follows the corresponding terminal or fail-closed path

#### Scenario: Approval and secret scope cross runtime generation
- **WHEN** one WorkerSession is rebound to a replacement RuntimeInstance
- **THEN** only unexpired ApprovalGrants matching capability, target, and constraints remain applicable, while every SecretLease is reissued for the new generation

### Requirement: Runtime heartbeat is independent from Agent progress
The system SHALL emit RuntimeHeartbeat every 15 seconds, mark the runtime suspect after 45 seconds without a valid heartbeat, mark it unhealthy after 90 seconds without recovery, and SHALL evaluate Agent unresponsiveness separately from model-output silence.

#### Scenario: Heartbeat becomes suspect
- **WHEN** the backend has not received a valid in-sequence RuntimeHeartbeat for 45 seconds
- **THEN** it marks RuntimeInstance suspect, pauses new command dispatch, probes transport fallback, container, Shim, and Agent processes, and leaves the active WorkerRun non-terminal

#### Scenario: Suspect runtime recovers
- **WHEN** transport fallback or process probing restores a valid heartbeat before 90 seconds
- **THEN** RuntimeInstance returns to healthy and command dispatch resumes without opening RuntimeRecoveryIncident

#### Scenario: Heartbeat becomes unhealthy
- **WHEN** 90 seconds pass without a valid heartbeat and probes do not restore control
- **THEN** RuntimeInstance becomes unhealthy and Supervisor may open one RuntimeRecoveryIncident

#### Scenario: Process failure is explicit
- **WHEN** RuntimeProvisioner confirms process exit or container absence
- **THEN** RuntimeInstance may become unhealthy immediately without waiting for heartbeat thresholds

#### Scenario: Model or tool execution is quiet
- **WHEN** RuntimeHeartbeat continues while an ACP operation reports running_model or running_tool but emits no model token or user-visible text
- **THEN** the system does not classify the Agent or RuntimeInstance as unresponsive solely because output is quiet

#### Scenario: Active AgentTurn reaches its hard deadline
- **WHEN** any interactive, unattended or approval-waiting AgentTurn reaches its fixed 24-hour hard deadline
- **THEN** the system idempotently sends ACP cancel, cancels the current Tool/process, fences late results and does not wait for a user extension decision

#### Scenario: Deadline cancellation does not converge
- **WHEN** the cancellation grace expires without a terminal prompt response or Tool stop evidence
- **THEN** the system records hard failure evidence, blocks the next AgentTurn and enters TaintedRuntime or external-effect reconciliation as applicable

#### Scenario: Replacement cannot obtain exclusive WorkingCopy access
- **WHEN** the control plane cannot terminate or isolate the old container, revoke its leases, and prove an exclusive WorkingCopy mount lease for the replacement
- **THEN** it does not mount or start the replacement against that WorkingCopy and enters `waiting_user` with fencing evidence

### Requirement: Worker persists full-fidelity trace and artifacts
The system SHALL persist Worker execution trace as JSONL with fidelity comparable to a normal Coding Agent local session JSONL, export trace and model observations to Langfuse, and persist all deliverables as ArtifactRef.

#### Scenario: Worker uploads managed session trace
- **WHEN** Codex ACP server emits events, session state, JSONL, raw transcript, or artifacts
- **THEN** Worker control logic normalizes them, uploads durable files through backend APIs to object storage, and reports progress events to EventCenter

#### Scenario: Patch artifact created
- **WHEN** Coding Agent creates a report or patch
- **THEN** Worker persists an ArtifactRef with task id, worker run id, type, URI, checksum, permissions, and retention policy

### Requirement: Trace and Artifact lifecycle supports troubleshooting evolution and analytics
The system SHALL index authoritative immutable JSONL segments and Artifact payloads through TraceManifest, SHALL tier them under versioned DataLifecyclePolicy without default time-based physical deletion, and SHALL build dashboards from normalized incremental facts rather than raw JSONL scans.

#### Scenario: Trace segment is ingested
- **WHEN** Worker uploads a valid immutable JSONL segment
- **THEN** the system records sequence range, checksum, bytes, schema, tenant/workspace scope, lifecycle policy, tier and consumer cursors in TraceManifest and emits idempotent analytics facts

#### Scenario: Trace ages through default tiers
- **WHEN** data reaches 30 and 180 days under `trace-default-v1`
- **THEN** it transitions from hot to warm and then cold without rewriting payload/checksum or physically deleting content

#### Scenario: Dashboard is queried
- **WHEN** a user opens Task, Worker, Tool, cost, Artifact or Evolution-effect statistics
- **THEN** the query reads TraceAnalyticsProjection and does not scan ObjectStore JSONL or require Langfuse availability

#### Scenario: Evolution needs cold evidence
- **WHEN** an eligible candidate references a cold Trace segment
- **THEN** Evolution requests asynchronous restore through TraceManifest and does not treat unavailable content as empty evidence

#### Scenario: Suspect content is quarantined
- **WHEN** an authorized operator marks a segment sensitive or its checksum fails
- **THEN** normal download, user projection and new Evolution consumption stop while metadata remains available for resolution

#### Scenario: Authorized user deletes trace payload
- **WHEN** an authorized actor confirms explicit deletion with a reason
- **THEN** an idempotent job deletes ObjectStore and identifiable Langfuse payloads, preserves a minimal tombstone and non-sensitive aggregates, marks evidence `source_deleted`, and records only the lightweight lifecycle action fields

#### Scenario: Delete job partially fails
- **WHEN** some payload or projection deletions fail
- **THEN** lifecycle remains `deletion_pending`, successful deletions are not repeated unsafely, remaining targets retry, and the result is visible without reporting deletion complete

#### Scenario: Patch is rendered and acted on
- **WHEN** Frontend detects a patch ArtifactRef
- **THEN** it renders a diff review and exposes allowed preview, download, apply-to-workspace, or publish-branch actions, with external publication subject to CodePlatform capability, a matching RepositoryAccessGrant and provider authorization

#### Scenario: First tracer-bullet patch is applied
- **WHEN** a Worker tracer-bullet run imports one authorized GitHub repository at an immutable base commit and the user applies its ChangeSet
- **THEN** the system serially creates a new Switchback WorkspaceRevision and exposes no push, remote-branch, or pull-request action for that slice

### Requirement: Worker uses managed native CodePlatform operations
The system SHALL provide pinned native Git and GitHub CLI through managed credential entrypoints, SHALL let Agent perform allowed non-administrative operations directly after RepositoryAccessGrant, and SHALL record CodePlatformOperation without proxying every command through a control-plane operation API.

#### Scenario: Native Git requests repository credentials
- **WHEN** Agent runs Git against an authorized Repository
- **THEN** `git-credential-switchback` uses host plus repository path to obtain a current RepositoryCredentialLease and does not persist a Token in remote URL or plaintext credential storage

#### Scenario: GitHub CLI is invoked
- **WHEN** Agent runs an allowed `gh` command
- **THEN** a managed wrapper obtains a current short lease for that invocation, executes native GitHub CLI and does not create persistent `gh auth login` state

#### Scenario: Push response is lost
- **WHEN** a push may have reached GitHub but its response is lost or authentication/network failure is ambiguous
- **THEN** Worker queries the target remote ref/head, records reconciliation evidence and does not blindly replay the write

#### Scenario: Pull request creation result is ambiguous
- **WHEN** PR creation returns an unknown result
- **THEN** Worker queries provider state by repository, head and base before retrying or entering `waiting_user`

#### Scenario: CodePlatform profile falls back
- **WHEN** Tenant policy forbids any repository Token in Agent or Provider write scope cannot be safely constrained
- **THEN** Worker selects full control-plane option A or direct-read/controlled-write option C while preserving RepositoryAccessGrant, CodePlatformOperation and provider-result contracts

### Requirement: Worker detail supports direct agent interaction
The system SHALL allow an authorized user to open an individual WorkerSession, inspect its projected trace and stage results, and submit AgentTurns directly without routing them through Assistant.

#### Scenario: User steers an active run
- **WHEN** a user submits an AgentTurn in `queue`, `interrupt`, or `steer` mode while a WorkerRun is active
- **THEN** Worker targets the active AgentSession/WorkerRun, records delivery and consumption, and streams resulting updates through Frontier

#### Scenario: User continues after stage completion
- **WHEN** a WorkerRun has succeeded and the WorkerSession remains within its lease
- **THEN** the stage report and artifacts remain visible and a new direct AgentTurn may start the next standalone WorkerRun without reopening Assistant clarification or mutating a terminal Task

### Requirement: Worker approval policy targets external effects
The system SHALL execute ordinary file, command, test, report, and patch actions inside the sandbox without per-action approval, and SHALL request approval for new external authentication, cross-sandbox or production writes, and unapproved capability installation.

#### Scenario: Sandbox patch operation
- **WHEN** Codex edits files and generates a patch inside the assigned workspace using already granted capabilities
- **THEN** Worker executes the action and records Trace without moving Task to `waiting_user`

#### Scenario: External platform credential required
- **WHEN** a follow-up action needs a new GitHub, GitLab, Feishu, MCP, or production credential
- **THEN** Worker emits a structured approval request and Task enters `waiting_user`

#### Scenario: User chooses approval scope
- **WHEN** the user approves a structured operation request
- **THEN** the system records either a one-use grant or a WorkerSession grant constrained to the same capability, target, limits, and expiry

#### Scenario: Langfuse observation exported
- **WHEN** Worker records an agent step, tool call, or LLMProxy model invocation
- **THEN** the system exports the corresponding trace/span/generation identifiers to Langfuse while retaining object storage JSONL as the authoritative trace

### Requirement: Worker separates user-visible progress from internal trace
The system SHALL classify Worker output into internal trace records and user-visible progress events.

#### Scenario: Internal command output
- **WHEN** an agent emits verbose build logs
- **THEN** Worker stores them in TraceRecord or raw transcript and only emits summarized user-visible progress if appropriate
