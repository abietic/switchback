## ADDED Requirements

### Requirement: Assistant compiles bounded context
The system SHALL compile a bounded ContextPack for each Assistant run using recent Turns, conversation summaries, active Truth, Rules, available Skills, and candidates from enabled optional providers. P2 Inspiration candidates MAY participate when that capability is enabled, but their absence SHALL NOT block ContextPack compilation.

#### Scenario: Context pack records selected and dropped items
- **WHEN** Assistant starts processing a user message
- **THEN** the generated ContextPack includes selected items, source references, token estimates, priorities, and dropped-item reasons

#### Scenario: Context pack enforces scope
- **WHEN** candidate context belongs to another tenant, project, or unauthorized user scope
- **THEN** the system excludes that context and records a permission drop reason

#### Scenario: Conversation has no Workspace resolution
- **WHEN** Assistant processes a message before the user explicitly selects a Workspace
- **THEN** it may use authorized tenant/user-scoped context for brainstorming and clarification but excludes Workspace-scoped Truth, Inspiration, repositories, Trace and Artifacts

#### Scenario: Conversation is a successor after Workspace rollover
- **WHEN** ContextCompiler builds a prompt for the active successor Conversation
- **THEN** it loads current-generation Turns and authorized ConversationHandoff items but does not load predecessor Turns/summaries merely because the provider topic or lineage matches

### Requirement: Assistant clarifies ambiguous requests
The system SHALL use versioned TaskReadinessPolicy and TaskReadinessAssessment to make a semantic execution-readiness decision across goal, deliverable/acceptance, scope/target, constraints, context/access, risk/approval, and execution policy, and SHALL not dispatch while any dimension remains `blocking_unknown` or deterministic validation fails.

#### Scenario: Missing acceptance criteria
- **WHEN** a user asks Assistant to "fix this service" without target or expected outcome
- **THEN** Assistant emits a user-visible clarification request and does not create a WorkerRun

#### Scenario: Validator accepts structure but intent remains ambiguous
- **WHEN** TaskDraft has all required fields but Assistant cannot determine a testable intended outcome
- **THEN** Assistant continues clarification because validator success does not replace semantic readiness

#### Scenario: Low-risk detail uses a safe default
- **WHEN** a missing reversible detail matches the active TaskReadinessPolicy default catalog and does not change permissions, target, risk, or delivery boundary
- **THEN** Assistant records an `assumed` dimension with rule id and evidence, includes it in TaskSpec assumptions, and may continue toward dispatch without asking a mandatory form question

#### Scenario: User asks to bypass a hard blocker
- **WHEN** target ownership, required access, conflicting constraints, production/external/irreversible effects, or a testable outcome remains unknown and the user says to proceed anyway
- **THEN** Assistant keeps the dimension `blocking_unknown` and does not dispatch or convert it to an assumption

#### Scenario: Three clarification questions do not resolve readiness
- **WHEN** Assistant has emitted three consecutive one-question clarification messages since the prior checkpoint and blockers remain
- **THEN** Assistant emits ClarificationCheckpoint with TaskDraft, assumptions, blockers, evidence, and continue/narrow-scope/cancel choices, omitting initiator-bound accept-assumptions-and-create while hard blockers remain

#### Scenario: User continues clarification
- **WHEN** the user chooses continue or narrows scope at ClarificationCheckpoint
- **THEN** Assistant records the decision, creates a new assessment, resets the consecutive-question counter, and may ask the next highest-priority blocker

#### Scenario: Structured channel request is immediately ready
- **WHEN** Delivery or API supplies sufficient evidence for all dimensions and deterministic validation succeeds
- **THEN** the system records a zero-question ready assessment and may submit TaskSpec without an Assistant clarification round trip

#### Scenario: Ordinary group request is ready but not authorized for creation
- **WHEN** Assistant produces a ready assessment from a shared-channel ordinary message
- **THEN** it exposes an exact TaskDraft confirmation state and does not dispatch until a separate TaskCreationTriggerPolicy-approved authenticated action creates ExplicitTaskCreationIntent

### Requirement: TaskDraft confirmation preserves actor ownership
The system SHALL keep one immutable initiator per TaskDraft lineage, SHALL reject confirmation by another participant or Conversation controller, and SHALL implement cross-participant continuation as an explicit newly authorized TaskDraft fork.

#### Scenario: Other users clarify the initiator's draft
- **WHEN** other shared Conversation participants add accepted constraints or evidence to Alice's D1
- **THEN** Interaction advances D1 revision/readiness while preserving Alice as initiator and invalidating any confirmation bound to the prior revision

#### Scenario: Participant adopts a visible draft
- **WHEN** Bob explicitly adopts exact D1 under the current Conversation/Workspace generation
- **THEN** Interaction creates D2 with Bob as initiator, records derived-from lineage, filters context for Bob, recomputes readiness and does not copy Alice-specific authority

### Requirement: Assistant emits TaskSpec for executable and explicitly authorized requests
The system SHALL emit a versioned TaskSpec with readiness assessment reference, active creation-intent reference, explicit WorkspaceContextResolution and assumptions only when no blocking unknown remains, deterministic validation succeeds and TaskCreationTriggerPolicy accepts intent bound to the exact draft.

#### Scenario: Executable development request
- **WHEN** a user provides goal, repository target, constraints, and acceptance criteria
- **THEN** Assistant submits TaskSpec with exact workspace id/resolution evidence, context refs, three-layer execution policy, runtime config, agent config, capability refs, and approval policy

#### Scenario: Workspace is only suggested or stale
- **WHEN** Workspace comes only from a global default, single candidate, URL, recent activity or model inference, or the selected Membership/revision is stale
- **THEN** Assistant keeps scope/context access blocking and does not submit TaskSpec until the user explicitly resolves an authorized Workspace

### Requirement: Workspace boundary changes roll over Conversation identity
The system SHALL keep the first unresolved-to-Workspace resolution in the current Conversation, SHALL create a successor Conversation when leaving a previously resolved Workspace, SHALL atomically advance the channel binding and lineage, and SHALL permit protected cross-Workspace handoff only through an explicit separately authorized carry-over.

#### Scenario: First Workspace is selected
- **WHEN** an unresolved Conversation explicitly resolves W1 with current authorization
- **THEN** Interaction records WorkspaceContextResolution in the same Conversation without creating a successor

#### Scenario: Workspace changes from W1 to W2
- **WHEN** an authenticated rebind command with current binding/source revisions selects authorized W2
- **THEN** one transaction creates C2/target resolution/workspace-neutral handoff/rollover, marks C1 superseded, advances ChannelThreadBinding and writes Outbox without migrating existing resources

#### Scenario: Workspace is cleared or access is revoked
- **WHEN** a resolved W1 Conversation is explicitly cleared or current Membership/policy revokes access
- **THEN** Interaction fences W1 context and creates/requires a Workspace-unresolved successor with no protected W1 handoff

#### Scenario: User later switches back to W1
- **WHEN** the current successor is resolved to another Workspace and the user explicitly selects W1 again
- **THEN** Interaction creates another successor and does not reactivate the superseded earlier W1 Conversation

#### Scenario: Rollover races ordinary messages or another rebind
- **WHEN** message acceptance, duplicate command or competing rebind uses the same binding/source boundary
- **THEN** serialized revisions and idempotency assign each Turn to exactly one generation, return the original successor for duplicates and reject stale competitors without silent rerouting

#### Scenario: Default handoff is generated
- **WHEN** rollover prepares continuity without explicit protected carry-over
- **THEN** ConversationHandoff contains only typed Workspace-neutral goals/constraints/assumptions/blockers and excludes raw Turns, summaries, Workspace Truth, repositories, resource refs and Tool/model output

#### Scenario: Protected content is carried from W1 to W2
- **WHEN** the user explicitly selects cross-Workspace context to transfer
- **THEN** each item records authenticated intent, exact source evidence, current W1 read authorization, W2 target-context authorization, classification/policy and immutable handoff digest before it can enter C2

### Requirement: Interaction owns channel Conversation participation and control
The system SHALL maintain revisioned ConversationParticipant roles for channel-scoped Conversations, SHALL preserve the actual actor on every Turn and command, SHALL limit shared-context/lifecycle mutations to controllers, and SHALL keep Conversation role separate from protected-resource authorization.

#### Scenario: Eligible provider member sends the first message
- **WHEN** a mapped provider member satisfies current ConversationParticipationPolicy and sends an ordinary message
- **THEN** Interaction materializes or refreshes an active participant relation and records the Turn under that exact actor

#### Scenario: Controller changes shared context
- **WHEN** a current controller submits a Workspace selection, clear or rollover command
- **THEN** Interaction accepts the control role as only one prerequisite and still requires current target authorization, expected revisions and intent evidence

#### Scenario: Controller role is delegated
- **WHEN** a controller delegates or revokes controller with current participant revision
- **THEN** Interaction records one auditable role revision and rejects removal of the final active controller

#### Scenario: All controllers become invalid through revocation
- **WHEN** provider or Switchback access change revokes every controller without an explicit role mutation
- **THEN** Interaction enters `control_orphaned`, fences context/lifecycle/control changes and waits for a policy-authorized explicit RecoverConversationControl command

#### Scenario: Conversation rolls to a successor
- **WHEN** a channel Conversation rolls from C1 to C2
- **THEN** Interaction creates a new generation under the same channel scope and carries current eligible participant/controller relations by explicit policy projection without widening any Workspace/resource authorization

### Requirement: Assistant archives Turns as evidence
The system SHALL persist Assistant Turns and summaries to object storage as traceable records after configured windows or phase completion.

#### Scenario: Phase completion archive
- **WHEN** Assistant completes a clarification phase or dispatches a TaskSpec
- **THEN** the system writes the relevant Turns or summary reference with conversation id/generation, tenant id, user id, Workspace scope snapshot, resolution/rollover refs and correlation id

### Requirement: Assistant summarizes Worker results
The system SHALL produce a user-readable result summary from Worker events and artifacts without exposing internal trace noise.

#### Scenario: Worker succeeds
- **WHEN** WorkerRun succeeds and ArtifactRef is available
- **THEN** Assistant emits a summary containing outcome, artifact links, validation result, and suggested next action
