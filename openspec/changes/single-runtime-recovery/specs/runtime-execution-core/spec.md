## ADDED Requirements

### Requirement: Eligible Runtime failure opens one durable recovery incident
The system SHALL create at most one active RuntimeRecoveryIncident for a WorkerSession and failed Runtime generation and SHALL permit at most one automatic replacement attempt within that incident.

#### Scenario: Current Runtime container exits
- **WHEN** automatic recovery is enabled and Docker, Shim process exit or authenticated ACP EOF proves that the active nonterminal Runtime failed
- **THEN** Worker Runtime persists one incident, marks the Session recovering, revokes new dispatch and returns the same incident on duplicate evidence

#### Scenario: Hard container failure with automatic recovery disabled
- **WHEN** server-owned inspection proves the exact current Runtime container exited or is missing while automatic recovery is disabled and no terminal, deadline, expiry or cancellation authority has won
- **THEN** Worker Runtime atomically persists an exhausted incident with zero attempts and no replacement, fails the active Turn and pauses the same Run and Session, fences database control and dispatch authority, and emits the Worker failure event without directly changing Task state or manufacturing Runtime terminal or Artifact evidence
- **AND** external write fencing remains unproven until cleanup provides evidence; the terminal incident permits existing cleanup reconciliation after restart, and an exact Turn/Runtime/generation/epoch reader hands waiters off without sending an interrupt to the failed Runtime

#### Scenario: Disabled admission preserves earlier recovery ownership
- **WHEN** automatic recovery is disabled after an incident was already opened
- **THEN** that existing incident continues its previously authorized reconciliation to recovered or exhausted; the flag does not cancel it or create another attempt

#### Scenario: Inspection cannot establish hard failure
- **WHEN** inspection fails, returns a different container or generation, or proves a running container despite transport closure or quiet output
- **THEN** disabled recovery admission does not create a failure incident from that observation

#### Scenario: WorkerControl transport closes while the Runtime remains healthy
- **WHEN** the control stream closes but Docker still proves the exact Runtime container is running and the authenticated Shim has not crossed the unhealthy boundary
- **THEN** Worker Runtime freezes new dispatch, awaits exact-identity resume, and does not open a recovery incident from transport closure alone

#### Scenario: Heartbeats stop without hard exit evidence
- **WHEN** no valid monotonic RuntimeHeartbeat is received for 45 seconds and then 90 seconds by server receive time
- **THEN** the Runtime becomes suspect and then unhealthy before one incident is opened

#### Scenario: Agent is quiet while Runtime remains healthy
- **WHEN** model or Tool output is quiet but RuntimeHeartbeat, process and control evidence remain valid
- **THEN** the system records at most a nonterminal quiet warning and does not open a recovery incident

#### Scenario: Replacement fails inside the stability window
- **WHEN** the only replacement fails before durable progress or the configured stability window proves health
- **THEN** the incident becomes exhausted and the system does not create a second automatic replacement

### Requirement: Runtime replacement fences old authority
The system SHALL revoke the failed generation's dispatch, ChannelEpoch, control credential, generation-scoped secrets and WorkingCopy write authority before generation `N+1` can execute.

#### Scenario: Old generation is fenced successfully
- **WHEN** RuntimeSupervisor proves the old container stopped or lost write authority
- **THEN** the system transactionally reserves one replacement Runtime identity and higher generation/ChannelEpoch and may provision it with fresh generation-scoped secrets

#### Scenario: Old generation cannot be fenced
- **WHEN** container cleanup or exclusive WorkingCopy mount authority cannot be proven
- **THEN** recovery becomes exhausted, no replacement receives write authority and the Session enters an actionable waiting-user state

#### Scenario: Old generation reports late evidence
- **WHEN** a command result, event, Trace record or Artifact finalization arrives with the failed generation or ChannelEpoch
- **THEN** the system prevents aggregate, DisplayCheckpoint and ArtifactRef mutation and retains allowed bytes only as non-authoritative late Trace evidence

### Requirement: Healthy Runtime control resumes without replaying effects
The system SHALL persist WorkerControl authority, both directional message journals, cursor acknowledgements and command effect state so the same live Shim can resume after transport or Backend restart without changing generation or duplicating work.

#### Scenario: Command committed before transport send
- **WHEN** Backend restarts after assigning a command sequence but before the live Shim durably acknowledges it
- **THEN** resume replays the exact journaled envelope at its original sequence and conflicting content is rejected

#### Scenario: Concurrent senders submit logical commands
- **WHEN** more than one Backend caller attempts to send through the same claimed Runtime
- **THEN** PostgreSQL serially assigns sequence, event receipt ACK and server send time while Hub never predicts or reuses transport authority

#### Scenario: Event committed before Backend acknowledgement
- **WHEN** the live Shim resends an event after losing the stream before receiving its cumulative ACK
- **THEN** Backend returns the original durable receipt for the exact sequence and digest, while one ordered inbox application records the business outcome separately

#### Scenario: Backend restarts with an unapplied event inbox
- **WHEN** an event is committed and acknowledged to Shim but Backend stops before its business handler records the applied outcome
- **THEN** startup redelivers that exact inbox entry in order and does not require Shim to retain or resend it

#### Scenario: Active prompt crosses Backend restart
- **WHEN** the same live Shim process has already invoked the exact authenticated `agent_turn.start` and reconnects after Backend restart
- **THEN** it reports the active effect state, Backend marks the original Turn consumed if needed, and neither side invokes or sends that prompt again

#### Scenario: A terminal Turn is followed by another Turn
- **WHEN** the latest exact start effect is terminal and a higher start-command sequence targets the next delivered AgentTurn
- **THEN** the journal installs the new effect identity and does not let the terminal proof permanently block the resident Runtime

#### Scenario: ResumeReady arrives before replay convergence
- **WHEN** Shim reports Ready before all declared replay envelopes are durably committed or with a mismatched final cursor
- **THEN** the journal rejects the barrier without enabling ordinary traffic or clearing the unresolved replay checkpoint

#### Scenario: Resume identity or cursor conflicts
- **WHEN** credential digest, Runtime generation, ChannelEpoch, process nonce, stream attempt, cursor digest or active-Turn identity conflicts with durable authority
- **THEN** resume fails closed, no journal cursor advances and no prompt, Tool or external effect is retried

### Requirement: Recovery preserves durable execution continuity
The system SHALL preserve WorkerSession, WorkerRun, AgentSession, WorkingCopy, durable Agent state, Trace cursor and original AgentTurn deadline while attaching the replacement Runtime.

#### Scenario: ACP resume succeeds
- **WHEN** generation `N+1` authenticates, advertises the required capability and resumes the exact AgentSession with matching WorkingCopy and cursors
- **THEN** the system keeps the existing Session, Run and AgentSession identities and closes the incident only after stable health or durable progress evidence

#### Scenario: Resume falls back to load
- **WHEN** resume is unavailable or fails before new work and capability-declared load safely attaches the same AgentSession
- **THEN** the system may continue the same attempt, tags replayed updates with the replacement generation and deduplicates them against durable evidence

#### Scenario: Active AgentTurn continuity is uncertain
- **WHEN** the replacement cannot prove the original underlying turn and Tool/external-effect state
- **THEN** the system does not resend the prompt, terminates that AgentTurn with `recovery_continuity_unproven` and leaves the same WorkerRun and WorkerSession waiting for explicit user action

#### Scenario: Recovery crosses the AgentTurn deadline
- **WHEN** server time reaches the original persisted `deadline_at` during recovery
- **THEN** deadline handling remains authoritative, the AgentTurn times out and recovery cannot reset or extend it

### Requirement: Ineligible failures do not trigger automatic replacement
The system SHALL NOT automatically recover user cancellation, Session idle or absolute expiry, authorization, authentication, policy, schema, protocol or known resource-exhaustion outcomes.

#### Scenario: Session reaches absolute expiry during recovery
- **WHEN** the immutable WorkerSession absolute deadline is reached before the incident closes
- **THEN** expiry preempts recovery, fences the Runtime and converges Task and Session state through the existing expiry contract

#### Scenario: External effect is unknown
- **WHEN** a failed Tool or remote operation has non-idempotent or unknown effect state
- **THEN** the system enters waiting-user reconciliation and does not use Runtime replacement as permission to retry the operation

## MODIFIED Requirements

### Requirement: M1 Runtime remains resident for a live WorkerSession
The system SHALL keep exactly one current RuntimeInstance, Shim, ACP session, Agent state and WorkingCopy resident while the WorkerSession is interactive, running, recovering, waiting_user or idle; ordinary interaction uses the current generation and eligible failure may replace it only through one RuntimeRecoveryIncident.

#### Scenario: Stage result is delivered
- **WHEN** an AgentTurn or Task stage reaches a terminal result but the WorkerSession lease remains valid
- **THEN** the current Runtime remains alive without pause, hibernation or generation change and accepts no autonomous work until the next valid dispatch

#### Scenario: Eligible Runtime fails
- **WHEN** the current Runtime fails before WorkerSession terminality and the incident is eligible
- **THEN** RuntimeSupervisor may replace it with one higher generation while preserving WorkerSession continuity and preventing concurrent writable generations

#### Scenario: Session becomes terminal
- **WHEN** WorkerSession is explicitly closed, idles out, reaches absolute expiry or is security-terminated
- **THEN** RuntimeSupervisor fences new commands, stops and removes every current or recovering Runtime resource, releases its secret resources and records cleanup outcome

## REMOVED Requirements

### Requirement: Runtime loss fails closed in the first slice
**Reason**: The first-slice generation-1 fail-closed behavior is replaced by the approved single automatic recovery contract.

**Migration**: Existing generation-1 loss records remain readable; new eligible failures create RuntimeRecoveryIncident state, while ineligible or exhausted failures continue to converge to waiting-user or terminal outcomes.
