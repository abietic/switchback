# assistant-conversation-dispatch Specification

## Purpose

Define durable SingleUserLocalProfile Assistant conversations, readiness assessment, revisioned TaskDrafts, and exact-confirmation dispatch into the existing Task and Worker owners.

## Requirements
### Requirement: Conversation messages are accepted durably and asynchronously
The system SHALL persist each SingleUserLocalProfile message, its server-owned tenant/user identity, Conversation sequence, idempotency result, queued AssistantRun, and owner-stream Outbox before returning an accepted response.

#### Scenario: User sends a new natural-language message
- **WHEN** the local user submits a message with the current Conversation revision and a new idempotency key
- **THEN** Gateway returns `202` with stable command, Turn, AssistantRun, and projection identities without waiting for a model call

#### Scenario: Message acceptance response is lost
- **WHEN** the same actor repeats the same message and idempotency key after an unknown response
- **THEN** the system returns the original accepted identities and does not append another Turn or AssistantRun

#### Scenario: Client attempts to override identity
- **WHEN** the request supplies a different tenant, user, model, policy, runtime, Tool, or secret configuration
- **THEN** the system rejects or ignores the field according to server schema policy and never grants the requested authority

### Requirement: Assistant processing is serialized per Conversation
The system SHALL execute at most one active AssistantRun per Conversation and SHALL process later accepted user Turns in stable owner-sequence order.

#### Scenario: User sends another message while a run is active
- **WHEN** a Conversation already has one claimed AssistantRun
- **THEN** the later message remains durably queued and cannot concurrently mutate the active TaskDraft or readiness state

#### Scenario: Backend restarts with queued work
- **WHEN** accepted Conversation Turns and queued runs exist in PostgreSQL after restart
- **THEN** the dispatcher resumes from durable sequence and idempotency state without creating duplicate model work

### Requirement: Assistant compiles a bounded and scoped M2 ContextPack
The system SHALL compile each AssistantRun ContextPack from the current Conversation generation, a bounded recent-Turn window, latest draft/readiness/checkpoint state, versioned rules and TaskReadinessPolicy, and authorized Workspace metadata while recording selected and dropped items.

#### Scenario: ContextPack is built for a resolved Workspace
- **WHEN** the Conversation has an explicit current Workspace resolution
- **THEN** the ContextPack includes only authorized metadata for that exact Workspace/revision plus source references, token estimates, priorities, and dropped-item reasons

#### Scenario: Conversation has no resolved Workspace
- **WHEN** Assistant processes a message before explicit Workspace selection
- **THEN** it may clarify user intent but excludes repository, Workspace-scoped Truth, Inspiration, Worker Trace, Artifact bodies, and other Workspace-protected context

#### Scenario: Context exceeds the server budget
- **WHEN** eligible context is larger than the configured Assistant budget
- **THEN** ContextCompiler deterministically drops lower-priority items, records their references and reasons, and never silently crosses tenant/user/Workspace scope

### Requirement: Assistant model output is an untrusted typed proposal
The system SHALL route one server-owned model profile through LiteLLM per AssistantRun and SHALL validate a versioned typed AssistantDecision before it can change Conversation, readiness, TaskDraft, intent, Task, or Worker state.

#### Scenario: Model proposes a valid clarification
- **WHEN** the selected model returns a schema-valid clarification decision consistent with current policy and revisions
- **THEN** Interaction commits one user-visible Assistant Turn, updated readiness evidence, and Outbox events without granting Tool or execution authority

#### Scenario: Model output is malformed or conflicts with policy
- **WHEN** output has invalid JSON/schema, unknown security semantics, a forbidden assumption, stale Workspace evidence, or an attempted Tool/Task command
- **THEN** the AssistantRun fails closed with a sanitized retryable diagnostic and creates no intent, Task, WorkerRun, permission, or external side effect

#### Scenario: Selected provider fails
- **WHEN** the Assistant model request times out or returns an upstream failure
- **THEN** the Run records failure for the same model profile and does not automatically switch provider or infer a ready draft

### Requirement: Task readiness uses the versioned seven-dimension policy
The system SHALL create an immutable TaskReadinessAssessment for goal, deliverable/acceptance, scope/target, constraints, context/access, risk/approval, and execution policy, with every dimension marked `known`, `assumed`, `blocking_unknown`, or `not_applicable`.

#### Scenario: Request has an untestable outcome
- **WHEN** the user requests work without a target or verifiable acceptance outcome
- **THEN** Assistant records the affected dimensions as `blocking_unknown`, asks for clarification, and does not expose Task confirmation

#### Scenario: A reversible low-risk detail is absent
- **WHEN** the missing detail matches an active policy default and does not change permission, target, risk, external effect, or delivery boundary
- **THEN** Assistant records the dimension as `assumed` with rule/evidence references and includes the visible assumption in TaskDraft and TaskSpec

#### Scenario: User asks to bypass a hard blocker
- **WHEN** access, target ownership, conflicting constraints, production/external/irreversible effects, or a testable outcome remains unknown
- **THEN** Assistant keeps `blocking_unknown` and does not convert it to an assumption even when the user asks to proceed

### Requirement: Clarification asks one blocker and checkpoints every three questions
The system SHALL ask only the current highest-priority blocking question and SHALL create a ClarificationCheckpoint after three consecutive Assistant clarification questions since the prior checkpoint.

#### Scenario: One blocker remains
- **WHEN** the latest readiness assessment has one or more blocking dimensions and the question budget is not exhausted
- **THEN** Assistant emits one question for the highest-priority blocker and does not render a multi-field mandatory form

#### Scenario: Three questions have not reached readiness
- **WHEN** the third consecutive clarification answer still leaves a hard blocker
- **THEN** Interaction commits a checkpoint containing current draft, assumptions, blockers, evidence, and continue, narrow-scope, or cancel actions

#### Scenario: User continues from a checkpoint
- **WHEN** the user chooses continue or submits a narrower scope against the exact checkpoint revision
- **THEN** the system records the decision, creates a new assessment, resets the next three-question budget, and preserves prior evidence

### Requirement: TaskDraft revisions and confirmation are distinct
The system SHALL maintain an immutable initiator and revisioned TaskDraft lineage and SHALL expose confirmation only for the exact current ready draft with no blocking unknown and a deterministically valid TaskSpec.

#### Scenario: Assistant reaches ready state
- **WHEN** all readiness dimensions are known, safely assumed, or not applicable and TaskSpec validation succeeds
- **THEN** Interaction publishes a human-readable draft preview with exact draft/readiness/Workspace/digest/policy identities and a confirm action

#### Scenario: User edits intent after a draft is ready
- **WHEN** a later message changes goal, acceptance, scope, constraints, Workspace, assumptions, or policy evidence
- **THEN** Interaction creates a new TaskDraft/readiness revision and invalidates confirmation bound to the previous revision

#### Scenario: User agrees through unbound free text
- **WHEN** a message says to proceed but does not invoke the registered exact-draft confirmation action
- **THEN** the message remains ordinary Assistant input and does not create an ExplicitTaskCreationIntent or Task

### Requirement: Task dispatch requires exact Workspace and creation intent
The system SHALL emit TaskSpec and ExplicitTaskCreationIntent only when actor, current Conversation, exact TaskDraft/readiness revisions, explicit Workspace resolution, TaskSpec digest, trigger-policy revision, and idempotency key agree.

#### Scenario: User confirms the current ready draft
- **WHEN** the fixed local actor invokes confirm with all current exact identities and authorization
- **THEN** Interaction records one immutable creation intent and emits one idempotent Task creation request for the existing Task Orchestration path

#### Scenario: Confirmation target is stale
- **WHEN** Conversation, draft, readiness, Workspace, Membership, TaskSpec digest, or policy revision changed before confirmation
- **THEN** the command returns a stale-target result with a refreshed projection reference and creates no intent, Task, allocation request, or WorkerRun

#### Scenario: Workspace is inferred rather than selected
- **WHEN** Workspace comes only from recent activity, a single candidate, repository URL, global default, or model inference
- **THEN** context/access remains blocking and Assistant cannot emit dispatch intent until the user explicitly selects an authorized Workspace/revision

### Requirement: Assistant delegates all heavy execution to Task and Worker owners
The system SHALL stop Assistant execution ownership after producing the valid creation request and SHALL rely on Task Orchestration and Worker Runtime for allocation, WorkerRun, AgentTurn, Tool, WorkingCopy, Trace, and Artifact state.

#### Scenario: Task is admitted
- **WHEN** Task Orchestration accepts the Assistant creation request
- **THEN** Interaction records the resulting Task and Worker projection references without starting Docker, ACP, Tool, or WorkingCopy operations itself

#### Scenario: Task admission rejects current evidence
- **WHEN** Task Orchestration detects stale intent, authorization, Workspace, readiness, digest, or policy evidence
- **THEN** Assistant projection shows a safe actionable rejection and no partial Task or Worker execution is created

### Requirement: Assistant phase evidence is archived as scoped JSONL
The system SHALL asynchronously persist checksum-addressed Conversation evidence after clarification checkpoint, ready draft, Task creation, cancellation, or AssistantRun failure while PostgreSQL remains authoritative for business state.

#### Scenario: Ready draft is confirmed
- **WHEN** the creation intent and resulting Task reference are committed
- **THEN** the evidence segment records tenant/user/Workspace scope, Conversation generation, visible Turns, structured readiness/draft decisions, model/policy revisions, correlation ids, and Task reference

#### Scenario: Evidence contains hidden or secret material
- **WHEN** a candidate JSONL record contains provider credentials, proxy credentials, environment values, or hidden chain-of-thought
- **THEN** the system excludes or rejects that material before finalizing the evidence segment and records a sanitized degradation outcome

#### Scenario: ObjectStore or Langfuse is unavailable
- **WHEN** Conversation or Task state commits but evidence upload or derived observability export fails
- **THEN** the committed business outcome remains unchanged and the system exposes retryable evidence/observability degradation
