## ADDED Requirements

### Requirement: Task and Run identity survive eligible Runtime recovery
The system SHALL preserve Task, WorkerSession and WorkerRun identity while one eligible RuntimeRecoveryIncident replaces the failed Runtime generation.

#### Scenario: Bound Task Runtime begins recovery
- **WHEN** a running Task's current Runtime opens an eligible recovery incident
- **THEN** Task remains running, keeps the same WorkerSession and WorkerRun references, and exposes recovering-runtime state from Worker-owned projection events

#### Scenario: Runtime recovery succeeds
- **WHEN** the replacement generation proves control, ACP, WorkingCopy and cursor continuity
- **THEN** the same Task, WorkerSession and WorkerRun continue without a synthetic rework, retry Task or new top-level Run

#### Scenario: Runtime recovery is exhausted
- **WHEN** fencing, replacement, reattachment or stability verification consumes the incident's only attempt without safe recovery
- **THEN** the same WorkerRun and WorkerSession enter waiting_user and a nonterminal bound Task enters `waiting_user:runtime_recovery_failed`

#### Scenario: Task was already delivered or terminal
- **WHEN** Runtime recovery exhausts after the bound Task reached delivered, completed, failed or cancelled
- **THEN** the system does not regress or rewrite that Task state and retains recovery evidence under WorkerSession lineage

### Requirement: Recovery state survives restart and duplicate delivery
The system SHALL persist incident phase, attempt, replacement identity, aggregate revisions, fencing evidence and idempotency results so Backend restart or duplicate commands cannot create another replacement.

#### Scenario: Backend restarts after replacement intent commit
- **WHEN** generation `N+1` identity was committed but Docker provisioning response was not recorded
- **THEN** reconciliation inspects the exact replacement identity and resumes the same incident phase without reserving generation `N+2`

#### Scenario: Duplicate failure evidence arrives
- **WHEN** heartbeat timeout, authenticated ACP EOF and Docker exit independently report the same failed generation
- **THEN** all reports converge on one incident and one attempt without repeating TaskTransition or Outbox side effects

### Requirement: First authenticated heartbeat boundary survives reconnect and restart
The system SHALL preserve one immutable first-heartbeat liveness boundary for an exact Runtime generation and ChannelEpoch from its first authenticated `ResumeComplete` until a real authenticated RuntimeHeartbeat satisfies it or the boundary becomes terminal.

#### Scenario: Resume completes before a real heartbeat exists
- **WHEN** Backend commits `ResumeComplete` for an exact Runtime generation and ChannelEpoch that has no persisted heartbeat observation
- **THEN** that same PostgreSQL transaction records the first authenticated resume anchor and exact revision and creates the T+45 suspect command, or rolls back the complete operation

#### Scenario: A real heartbeat already precedes resume completion
- **WHEN** an authenticated heartbeat observation for the same Runtime generation and ChannelEpoch was persisted before `ResumeComplete`
- **THEN** Backend does not arm a first-heartbeat watchdog for that Runtime

#### Scenario: Transport reconnects before the first heartbeat
- **WHEN** WorkerControl disconnects and the same live Runtime completes another exact resume before its first real heartbeat
- **THEN** Backend creates no recovery incident and reuses the original watchdog anchor and T+45/T+90 deadlines without resetting them

#### Scenario: The first real heartbeat wins before the unhealthy boundary
- **WHEN** a strictly sequenced authenticated RuntimeHeartbeat with server receive time before T+90 is persisted while the watchdog is awaiting or suspect
- **THEN** the heartbeat observation and watchdog satisfaction commit atomically with the real heartbeat/control sequences, and pending watchdog commands are cancelled

#### Scenario: No real heartbeat reaches the boundary
- **WHEN** the immutable anchor reaches T+45 and then T+90 without a valid heartbeat observation
- **THEN** Backend marks the Runtime suspect at T+45 and marks an ordinary live Runtime unhealthy with one stable recovery intent at T+90; a replacement timeout exhausts its existing incident without opening a nested incident

#### Scenario: Backend restarts with an armed first-heartbeat watchdog
- **WHEN** Backend restarts before the watchdog is satisfied or terminal
- **THEN** startup drains due first-heartbeat commands before ordinary dispatch and resolves liveness from the latest heartbeat receive time, otherwise the original watchdog anchor, without restarting the window

#### Scenario: Backend restarts during a deadline-owned reconnect grace
- **WHEN** a live Runtime has an unfinished Backend-deadline settlement with a future frozen `force_after`
- **THEN** startup revalidates the exact environment, running container and credential identity, immediately defers that candidate without blocking global readiness, and leaves the durable deadline worker plus post-ready unowned-Runtime adoption to converge it

#### Scenario: A heartbeat is accepted before its ordered business observation
- **WHEN** durable control journal contains a strictly decodable RuntimeHeartbeat with matching identity and `created_at` before the immutable unhealthy boundary, but an earlier event or Backend crash prevents the ordinary pump from projecting it before T+45 or T+90 evaluation
- **THEN** the evaluator projects the heartbeat observation, watchdog satisfaction and normal liveness scheduling while holding the same Session, Runtime and watchdog locks, without marking the journal message applied or advancing the ordered event-applied cursor

#### Scenario: Ordered replay reaches a repaired heartbeat
- **WHEN** the ordinary event pump later reaches a heartbeat already projected by the watchdog evaluator
- **THEN** it applies the journal event in sequence and duplicate-safe observation handling preserves the existing observation revision, first-heartbeat proof and single liveness command

#### Scenario: Multiple accepted heartbeats await ordered projection
- **WHEN** more than one strictly valid pre-boundary heartbeat is durable behind an unapplied event
- **THEN** the evaluator freezes the first valid heartbeat as watchdog proof, advances the ordinary observation through every candidate to the latest monotonic heartbeat and receive time, and leaves exactly one pending liveness command anchored to that freshest evidence

#### Scenario: Journal heartbeat is invalid or on the unhealthy boundary
- **WHEN** a candidate heartbeat payload fails strict RuntimeHeartbeat decoding or its durable `created_at` is equal to or later than the immutable unhealthy due time
- **THEN** it does not satisfy the watchdog or create a heartbeat observation, and the original T+90 unhealthy decision remains authoritative

#### Scenario: Terminal authority enters before liveness mutation
- **WHEN** a Runtime terminal or Backend deadline claim enters the exact AgentTurn outcome critical section before heartbeat recording, first-heartbeat evaluation or ordinary liveness evaluation, including before its settlement row commits
- **THEN** liveness waits behind the same advisory lock, revalidates exact Run/Turn identity after locking the mutable graph, then cancels the competing watchdog decision as `turn_outcome_owned` or `backend_deadline_owned` without marking the Runtime unhealthy or opening recovery

#### Scenario: Active Turn identity changes during liveness lock admission
- **WHEN** active Run/Turn identity changes after heartbeat or liveness reads its lock target but before it obtains the mutable Session/Runtime graph
- **THEN** the store rolls back that snapshot and performs a bounded cross-transaction retry against the new identity rather than closing the control stream, losing the command or failing startup readiness on the transient conflict

#### Scenario: Heartbeat races with an owned terminal settlement
- **WHEN** a valid authenticated heartbeat arrives after the exact AgentTurn has an unfinished terminal settlement, including at or after the immutable first-heartbeat unhealthy boundary before its watchdog evaluator runs
- **THEN** Backend preserves the heartbeat observation and any strictly pre-boundary first-heartbeat proof without rolling back or closing the control stream, and does not restore Runtime status, increment Runtime revision or cancel/recreate ordinary liveness commands

#### Scenario: Graceful terminal arrives after Runtime becomes suspect
- **WHEN** a Backend-deadline settlement attaches an exact authenticated terminal witness before force-stop cleanup, while the same Runtime is already suspect
- **THEN** domain settlement advances Session, Run and Turn to the deadline-owned result while preserving Runtime status, revision and resident binding

Implementation evidence note: database integration coverage proves watchdog/heartbeat transaction races, immutable deadlines, accepted-journal backlog projection, ordered duplicate-safe replay, exact T+90 rejection and startup ordering. Production/fixed-image acceptance remains separate from these implemented database convergence semantics.

## MODIFIED Requirements

### Requirement: Business state survives Backend restart
The system SHALL persist aggregate state, queue sequence, idempotency result, deadline, Outbox, projection cursor and RuntimeRecoveryIncident phase so a Backend restart does not lose committed work or create duplicate execution.

#### Scenario: Backend restarts after Task commit
- **WHEN** Task creation committed but its allocation event had not yet been processed
- **THEN** the Outbox dispatcher resumes delivery and allocator processes the original request once logically

#### Scenario: Backend restarts during a healthy live Run
- **WHEN** PostgreSQL contains a current active Runtime and that Runtime reconnects with the exact generation, ChannelEpoch and control identity
- **THEN** Backend reconstructs control ownership and both message cursors from the durable journal, replays only unacknowledged messages, and does not create another Runtime or Run

#### Scenario: Backend restarts after journaling an AgentTurn start
- **WHEN** Backend committed `agent_turn.start` before restart and the same live Shim resumes with the exact command cursor and active-Turn effect identity
- **THEN** Backend reconciles delivered or consumed state from the authenticated effect acknowledgement and never invokes or sends that prompt as a new effect

#### Scenario: A stale Backend control owner continues after resume
- **WHEN** a newer Backend owner fence or Shim stream attempt claimed the exact Runtime control channel
- **THEN** every later append, ACK or business-state mutation from the stale owner or attempt is rejected without advancing a cursor

#### Scenario: Backend restarts during Runtime recovery
- **WHEN** PostgreSQL contains an open incident in fencing, provisioning, reattaching or verifying state
- **THEN** Backend resumes that exact phase, reconciles server-owned external resource identities and never exceeds one replacement attempt
