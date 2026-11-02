## ADDED Requirements

### Requirement: Gateway exposes revisioned Assistant commands
The system SHALL expose server-identity-scoped commands to create/open a Conversation, select an exact Workspace, send a message, act on a ClarificationCheckpoint, confirm a ready TaskDraft, cancel queued Assistant work, and retry a retryable failed run.

#### Scenario: Valid message command is submitted
- **WHEN** the local user supplies exact Conversation revision, message content, and idempotency key
- **THEN** Gateway returns `202` with command and owner identities plus the canonical Assistant projection reference

#### Scenario: Command target is stale
- **WHEN** Conversation, active run, checkpoint, draft, readiness, or Workspace revision no longer matches the command
- **THEN** Gateway performs no action against newer state and returns a stable stale-target result with a refreshed view reference

#### Scenario: Command is retried
- **WHEN** the browser repeats the same idempotency key after timeout or reconnect
- **THEN** Gateway returns the original command result without repeating the message, checkpoint decision, intent, or Task creation

### Requirement: Frontier provides an Assistant Conversation projection
The system SHALL expose a complete `assistant_conversation` ProjectionSnapshot and typed SSE ProjectionDelta/control stream using the existing epoch, cursor, projection-version, replay, and resync contracts.

#### Scenario: Browser opens an Assistant Conversation
- **WHEN** the authorized local user opens the canonical Conversation route
- **THEN** Frontier returns ordered user-visible Turns, AssistantRun state, readiness blockers, assumptions, checkpoint, current draft, allowed actions, pending outcomes, and Task/Worker references

#### Scenario: Browser connects after snapshot
- **WHEN** the browser subscribes with the snapshot epoch/cursor
- **THEN** Frontier replays every later retained delta in order and then streams live updates without a snapshot/subscribe gap

#### Scenario: Cursor cannot be replayed
- **WHEN** epoch changed, cursor was evicted, a gap exists, or base projection version is unavailable
- **THEN** Frontier emits `resync_required` and the browser atomically replaces only the Assistant Conversation replica

### Requirement: Assistant Web renders one focused interaction surface
The system SHALL provide a local React/Vite Assistant route that renders Conversation history, queued/running/failed state, one clarification question, visible assumptions, checkpoint choices, TaskDraft preview, confirmation, and resulting Task/Worker navigation from the Frontier projection.

#### Scenario: Assistant needs one clarification
- **WHEN** the projection contains a blocking question
- **THEN** the page renders that question with a reply control and does not expose Task confirmation

#### Scenario: Clarification checkpoint is active
- **WHEN** the exact current checkpoint permits continue, narrow-scope, or cancel
- **THEN** the page renders only those projection-authorized actions and binds each command to the checkpoint revision

#### Scenario: TaskDraft is ready
- **WHEN** the projection contains a current confirmable draft
- **THEN** the page shows goal, deliverables, acceptance, target, constraints, assumptions, Workspace, model/execution profile summary, and an explicit confirm command without exposing raw JSON editing

### Requirement: Browser pending state does not invent business authority
The system SHALL keep HTTP lifecycle, pending command state, durable projection state, and any transient display overlay separate and SHALL not infer a Conversation, readiness, Task, or Worker transition from a `202` response alone.

#### Scenario: Confirmation is accepted before Task projection arrives
- **WHEN** Gateway returns `202` but the Task owner event has not reached Frontier
- **THEN** the page shows command-scoped pending state and keeps the prior authoritative draft/task state until a causal delta or refreshed snapshot arrives

#### Scenario: Command response is lost
- **WHEN** the browser cannot determine whether a message or confirmation was accepted
- **THEN** it retries with the same idempotency key and converges on the owner result rather than appending optimistic duplicate business state

### Requirement: Assistant handoff links to the existing Task and Worker views
The system SHALL project canonical Task and Worker references after admission and SHALL reuse existing TaskSummary, WorkerDetail, ArtifactDetail, and Worker follow-up routes instead of duplicating execution state in the Assistant page.

#### Scenario: Assistant-created Task starts
- **WHEN** Task Orchestration and Worker Runtime publish the resulting identities
- **THEN** the Assistant page links to the canonical Task and Worker views and displays only a compact handoff status

#### Scenario: Worker later delivers report or patch
- **WHEN** the existing M1 projections publish execution progress or artifacts
- **THEN** review, accept, rework, queue, interrupt, steer, and follow-up remain owned and rendered by the existing Task/Worker surfaces

### Requirement: Assistant UI hides internal reasoning and credentials
The system SHALL render structured user-visible content and sanitized diagnostics while keeping raw model payloads, hidden chain-of-thought, credentials, environment values, internal Outbox events, and JSONL evidence outside the normal Assistant projection.

#### Scenario: Model or evidence processing fails
- **WHEN** an AssistantRun reports provider, schema, policy, ObjectStore, or projection failure
- **THEN** the UI displays a stable error code, affected stage, and currently valid retry/cancel action without raw provider payload or secret material

#### Scenario: Model emits reasoning fields
- **WHEN** the provider response includes hidden reasoning or internal analysis
- **THEN** the normal Conversation projection excludes it and persists only policy-approved user-visible Assistant content and structured decision evidence

### Requirement: Direct TaskSpec remains available during rollout
The system SHALL preserve the existing direct TaskSpec form/API and Worker-first product path while the Assistant route is introduced and SHALL route both creation paths through the same Task admission and Worker contracts.

#### Scenario: Assistant route is disabled or degraded
- **WHEN** local configuration disables Assistant or the Assistant model is unavailable
- **THEN** the user can still create a valid direct TaskSpec and use the M1 Task/Worker experience

#### Scenario: Direct and Assistant paths submit equivalent TaskSpec
- **WHEN** both paths provide the same actor, Workspace, TaskSpec digest, readiness, creation intent, and policy evidence
- **THEN** Task Orchestration applies the same validation, idempotency, allocation, acceptance, Trace, and Artifact behavior
