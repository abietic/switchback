# Worker Detail Interaction Specification

## Purpose

Define the M1 WorkerDetail projection, synchronization, artifact review, revisioned commands, approvals, local identity, and safe diagnostics contracts.

## Requirements

### Requirement: WorkerDetail is the primary execution view
The system SHALL expose a Tenant/User/Workspace-authorized WorkerDetail view containing Session identity/status, current and historical Runs/Turns, stage reports, patch Artifacts, approval/waiting-user state and available commands without requiring raw Trace access.

#### Scenario: User opens a live WorkerSession
- **WHEN** the fixed local actor opens an authorized WorkerSession detail route
- **THEN** Frontier returns one complete WorkerDetail ProjectionSnapshot and the UI renders current state, history, artifacts and chat controls from that projection

#### Scenario: Task is terminal but Session remains live
- **WHEN** the bound Task completes, fails or is cancelled before WorkerSession expiry
- **THEN** WorkerDetail remains reachable and interactive while TaskSummary stays terminal

### Requirement: Frontier provides snapshot and typed SSE synchronization
The system SHALL expose a complete versioned ProjectionSnapshot and typed SSE ProjectionDelta stream with epoch, cursor and projection-version continuity.

#### Scenario: Client connects after snapshot
- **WHEN** UI opens SSE with the snapshot cursor
- **THEN** Frontier replays all later retained deltas in cursor order and then streams live deltas without a snapshot/subscribe gap

#### Scenario: Client reconnects with retained cursor
- **WHEN** SSE reconnects with the last applied `epoch:cursor`
- **THEN** Frontier replays only later logical deltas and ClientViewReplica ignores exact duplicates idempotently

#### Scenario: Cursor cannot be replayed
- **WHEN** epoch changed, cursor was evicted, a gap exists or base projection version cannot be reached
- **THEN** Frontier emits `resync_required` and UI atomically replaces only that View replica from a new snapshot

### Requirement: Frontend does not reconstruct authority from raw events
The system SHALL materialize TaskSummaryView and WorkerDetailView in Eventing & Projection and MUST NOT require Frontend to join raw IntegrationEvents, database rows, ACP frames or Trace JSONL into business state.

#### Scenario: Worker emits progress and Artifact events
- **WHEN** multiple owner streams emit causally related events
- **THEN** projector applies them idempotently into one page-optimized view while preserving source stream/sequence references for diagnostics

#### Scenario: Projection is behind an accepted command
- **WHEN** Gateway returns `202` before the owner event reaches WorkerDetail
- **THEN** UI shows a command-scoped pending state and does not invent a new Task/Run status until a causal delta or refreshed snapshot arrives

### Requirement: Durable display checkpoints recover transient output
The system SHALL separate replayable DisplayCheckpoint state from best-effort TransientDisplayDelta chunks and SHALL converge every terminal Agent message/Turn to a complete checkpoint.

#### Scenario: Agent streams text or activity
- **WHEN** Codex emits nonterminal user-visible chunks
- **THEN** Frontier streams bounded transient deltas without advancing durable View cursor or exposing hidden chain-of-thought

#### Scenario: Tool, approval, Artifact or terminal state changes
- **WHEN** execution crosses a semantic lifecycle boundary
- **THEN** projector commits a complete DisplayCheckpoint and publishes a replayable checkpoint delta

#### Scenario: Browser misses transient chunks
- **WHEN** transient sequence has a gap or the browser reconnects
- **THEN** UI discards the affected overlay and replaces it from the current or next durable checkpoint

### Requirement: Report and patch have canonical Artifact detail
The system SHALL expose Tenant/User/Workspace-scoped report and patch ArtifactRefs through a permission-aware canonical Artifact detail view with checksum, source Run/Turn and completeness provenance.

#### Scenario: Report Artifact is opened
- **WHEN** a completed stage provides a report ArtifactRef
- **THEN** UI renders or downloads the exact verified report and shows its source WorkerRun/AgentTurn

#### Scenario: Patch Artifact is opened
- **WHEN** a completed stage provides a patch ArtifactRef
- **THEN** UI renders a diff review with changed files and source provenance and does not imply the patch was applied or published

#### Scenario: Artifact content is missing or checksum fails
- **WHEN** canonical Artifact detail cannot verify its ObjectStore content
- **THEN** UI shows an integrity failure, disables accept/apply-like actions and preserves the metadata for diagnosis

### Requirement: Worker commands target exact revisioned identities
The system SHALL require exact WorkerSession/Task/Run target, expected revision and idempotency key for follow-up, accept, rework, cancel, queue, interrupt, steer and approval commands.

#### Scenario: Valid follow-up chat is submitted
- **WHEN** the fixed actor sends a message to the exact live WorkerSession revision
- **THEN** Gateway accepts one command, returns `command_id`, and Runtime queues or dispatches it according to the persisted Session execution state

#### Scenario: Command target is stale
- **WHEN** expected revision, active Run, Session state or Workspace binding changed
- **THEN** Gateway returns a stale-target result with a refreshed view reference and performs no action against a newer target

#### Scenario: Command is retried
- **WHEN** UI repeats the same idempotency key after timeout or reconnect
- **THEN** Gateway returns the original command result and does not enqueue or apply the action twice

### Requirement: Queue, interrupt and steer have distinct behavior
The system SHALL expose queue, interrupt and steer as separate commands against one active top-level AgentTurn and SHALL preserve an auditable command outcome.

#### Scenario: User queues a message
- **WHEN** an AgentTurn is active and user selects queue
- **THEN** the message receives stable Session queue sequence and is not delivered until earlier work reaches its dispatch boundary

#### Scenario: User interrupts active work
- **WHEN** user targets the exact active AgentTurn with interrupt
- **THEN** Runtime requests ACP cancellation, fences later output after terminal cancellation and records `cancelled:user_interrupt` without closing the WorkerSession

#### Scenario: User steers active work
- **WHEN** adapter capability and active Turn state support steer
- **THEN** Runtime sends the steering input to that exact Turn and records accepted or rejected capability outcome without creating concurrent top-level work

#### Scenario: Adapter does not support steer
- **WHEN** the active Agent capability snapshot lacks safe steer semantics
- **THEN** Gateway rejects steer as unsupported and offers queue or interrupt without emulating steer through a new prompt

### Requirement: Approval focuses on protected effects
The system SHALL allow ordinary sandbox file, command, test, report and patch operations without per-operation approval and SHALL require explicit approval for external authentication, external/production writes or unapproved capability installation.

M1 does not expose external credentials, production-write Tools or capability installation to Worker. It persists and renders exact ApprovalRequest/ApprovalGrant contracts, while unsolicited ACP permission requests fail closed. Enabling any protected-effect adapter requires a later same-operation pause/resume bridge; a stored grant alone never authorizes or implies an external side effect.

#### Scenario: Ordinary sandbox command runs
- **WHEN** Codex executes an allowed command inside the isolated WorkingCopy under resolved policy
- **THEN** Worker executes it directly and records Tool evidence without pausing for user approval

#### Scenario: Protected effect is requested
- **WHEN** a Tool requests an external credential, production write or unapproved capability
- **THEN** an enabled protected-effect adapter pauses the affected action, publishes ApprovalRequest with exact capability/target/effect and exposes once/session choices in WorkerDetail; M1 rejects the request before execution because no such adapter is enabled

#### Scenario: Session-scoped approval is granted
- **WHEN** user grants a Session-scoped ApprovalGrant
- **THEN** the grant remains usable only by an enabled adapter for the same Session, capability, target, effect, limits and expiry; unrelated operations and M1's disabled protected effects remain unauthorized

### Requirement: Waiting-user state exposes actionable commands
The system SHALL project waiting reason, allowed actions and canonical target identity without giving Frontend ownership of Task or Worker state.

#### Scenario: Worker needs user input
- **WHEN** Task or Run enters `waiting_user`
- **THEN** WorkerDetail displays the reason and only currently valid reply/approval/cancel actions derived from owner state

#### Scenario: Waiting action becomes obsolete
- **WHEN** owner revision advances before the user acts
- **THEN** the old action fails stale validation and UI refreshes from projection instead of applying it to the new state

### Requirement: Single-user Web remains local and non-overridable
The system SHALL bind M1 Gateway/Frontier to loopback, inject the configured SingleUserLocalProfile identity server-side and reject client attempts to select another tenant/user.

#### Scenario: Local UI submits a command
- **WHEN** request arrives through the loopback Web origin
- **THEN** Gateway applies the configured tenant/user and records that actor on command and resulting events

#### Scenario: Client sends identity override
- **WHEN** request includes a different tenant, user or authority claim
- **THEN** Gateway rejects the request and does not disclose whether the referenced resources exist

### Requirement: User-visible diagnostics do not expose secrets or raw internal payloads
The system SHALL render structured, sanitized diagnostics and keep raw ACP frames, provider payloads, credentials and internal IntegrationEvent bodies behind separate debug authorization.

#### Scenario: Model or Runtime fails
- **WHEN** execution reports an upstream, ACP, Artifact or cleanup error
- **THEN** WorkerDetail shows stable error code, affected stage and safe next action without provider key, proxy token, internal header or raw payload

### Requirement: Projected Worker actions match command authority
The system SHALL derive WorkerDetail available actions from the exact authorized Task, WorkerSession, WorkerRun and AgentTurn state and MUST NOT advertise a command that the owner would structurally reject at those same revisions.

#### Scenario: User continues a delivered Task from chat
- **WHEN** the Task initiator sends free-form feedback to an exact live WorkerSession whose bound Task is `delivered` and whose latest Run and Turn are terminal
- **THEN** Gateway continues the same Task through a new WorkerRun and AgentTurn, returns the Task to `running` and preserves previous Run, Trace and Artifact evidence

#### Scenario: User requests changes from Artifact review
- **WHEN** the Task initiator submits review feedback for the same exact delivered Task and live WorkerSession
- **THEN** the system uses the same Task continuation lifecycle with interaction kind `rework` rather than creating another Task or standalone Run

#### Scenario: User chats after accepting delivery
- **WHEN** a Task is `completed`, is rendered as `Accepted`, has released its active Session binding and the WorkerSession remains live
- **THEN** WorkerDetail remains interactive and a new message creates a standalone WorkerRun without moving the accepted Task back to `running`

#### Scenario: Delivered Task is still bound
- **WHEN** the latest Run and Turn are terminal but their Task remains `delivered` and bound to the Session
- **THEN** WorkerDetail exposes Task continuation and acceptance actions and does not expose standalone `worker.follow_up`

### Requirement: Worker history materializes user-visible execution
The system SHALL materialize authorized Agent messages, Tool activity, approvals, errors, stage changes and Artifact references into versioned DisplayParts and durable DisplayCheckpoints without requiring the browser to read raw ACP frames or Trace JSONL.

#### Scenario: Agent streams and completes a response
- **WHEN** ACP emits user-visible response chunks followed by a terminal Agent message
- **THEN** Frontier may stream bounded transient text and MUST converge the Turn to one complete replayable text part in a durable checkpoint

#### Scenario: Tool lifecycle changes
- **WHEN** a Tool starts, updates, waits for approval or reaches terminal status
- **THEN** the durable display records stable Tool identity, safe arguments, status, duration, bounded output and authorized evidence references at semantic lifecycle boundaries

#### Scenario: Browser reconnects after missing transient output
- **WHEN** a WorkerDetail client reconnects after a transient sequence gap
- **THEN** it discards the incomplete overlay and restores complete Agent and Tool history from the latest durable checkpoint

#### Scenario: Raw internal content exists
- **WHEN** Trace contains provider payloads, credentials, hidden reasoning or unbounded Tool output
- **THEN** the user-visible display excludes that raw content and exposes only sanitized summaries and separately authorized evidence references

#### Scenario: Worker history is long
- **WHEN** a WorkerSession contains more Run and Turn history than the initial WorkerDetail page
- **THEN** Frontier returns a bounded recent page and allows older authorized history to load through a stable timeline cursor

### Requirement: Artifact Workbench renders canonical delivery results
The system SHALL render checksum-verified report and patch ArtifactRefs in a resizable Worker Workbench while preserving canonical source Run/Turn provenance and safe fallback behavior.

#### Scenario: Report is selected
- **WHEN** an authorized user selects an online report Artifact with a valid checksum
- **THEN** the Workbench renders safe Markdown with source Run/Turn, generation time, checksum and completeness while keeping Runtime and provider diagnostics outside the report body

#### Scenario: Multi-file patch is selected
- **WHEN** an authorized user selects an online parseable patch Artifact with a valid checksum
- **THEN** the Workbench shows changed-file status and statistics, file selection, unified or split review, wrapping and hunk navigation without implying that the patch was applied or published

#### Scenario: Patch contains no changes
- **WHEN** the canonical patch Artifact contains zero patch bytes
- **THEN** the Workbench displays a no-code-changes state and does not render an empty file tree or misleading diff review

#### Scenario: Artifact cannot be safely rendered
- **WHEN** Artifact content is missing, fails checksum, is binary, exceeds render policy or cannot be parsed
- **THEN** the Workbench preserves metadata and safe diagnostics, disables actions that require verified content and offers download only when integrity and authorization permit it

### Requirement: Stale command recovery preserves user intent
The system SHALL preserve an unsent or rejected Worker message across authoritative view refresh and MUST NOT automatically replay it after command target state changes.

#### Scenario: Exact target becomes stale
- **WHEN** Gateway rejects a Worker command because Task, Session, Run or Turn revision changed
- **THEN** the UI preserves the draft, replaces the affected WorkerDetail replica from a current snapshot and recalculates available send modes

#### Scenario: Refreshed state supports another command
- **WHEN** the refreshed snapshot exposes a different valid action for the preserved draft
- **THEN** the UI requires a new user send action against the new exact target and does not silently resubmit the prior command
