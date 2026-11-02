## ADDED Requirements

### Requirement: Tenant and Workspace authorization is explicit
The system SHALL treat Tenant as a hard security boundary, SHALL require active TenantMembership and explicit WorkspaceMembership for protected Workspace access, SHALL use composable viewer/editor/reviewer/owner roles, and SHALL NOT infer P0 access from Project or Team association.

#### Scenario: Same-tenant editor starts work
- **WHEN** an authenticated user has active TenantMembership and WorkspaceMembership with editor role for a Workspace in that Tenant
- **THEN** Identity & Access returns a revisioned allow decision and may issue a short Worker execution authorization lease scoped to that actor, Workspace, WorkerSession, actions, and expiry

#### Scenario: Cross-tenant resource id is presented
- **WHEN** the authenticated request Tenant differs from the Workspace or resource Tenant
- **THEN** authorization fails before role evaluation and no metadata, content, Trace, Artifact, Truth, or existence detail is disclosed

#### Scenario: Reviewer lacks editor role
- **WHEN** a user has reviewer but not editor or owner role
- **THEN** the user may perform eligible review actions but cannot create WorkerSession, send AgentTurn, modify WorkingCopy, or Apply ChangeSet

#### Scenario: Last owner is removed
- **WHEN** a membership mutation would leave the Workspace without an active owner
- **THEN** the mutation is rejected under expected-revision and idempotency rules and an audit result is recorded

### Requirement: Workspace roles do not grant external provider access
The system SHALL authorize external effects separately from Workspace roles using resolved capability policy, a matching RepositoryAccessGrant or other ApprovalGrant, current RepositoryCredentialLease or SecretLease, and provider-side authorization.

#### Scenario: Editor requests branch publication
- **WHEN** a Workspace editor requests a branch push without a matching RepositoryAccessGrant or usable provider credential
- **THEN** Switchback does not publish and returns waiting-user or structured denial without changing Workspace authorization

#### Scenario: WorkerSession repository Grant is active
- **WHEN** a matching WorkerSession-scoped RepositoryAccessGrant and short credential lease remain valid
- **THEN** Agent may directly execute allowed non-administrative native GitHub operations without per-command approval while Workspace and provider checks remain independent

#### Scenario: Provider rejects an otherwise authorized action
- **WHEN** Switchback policy allows an external action but the provider rejects the delegated or service credential
- **THEN** Switchback records the provider result as failure and does not report the action as successful

### Requirement: Running Workers honor authorization revocation
The system SHALL bind Worker execution to the initiating actor using short-lived authorization leases and SHALL fence protected operations when Membership or Policy is revoked.

#### Scenario: Editor is revoked during a WorkerSession
- **WHEN** Identity & Access publishes a newer revoked Membership or Policy revision
- **THEN** Gateway and Frontier deny new access, Worker stops new command/Apply/Artifact/secret operations, execution and secret leases are revoked, and the preserved session enters `waiting_user` with `access_revoked`

#### Scenario: Local work cannot be proven stopped after revocation
- **WHEN** a revoked runtime continues a local process and termination cannot be confirmed
- **THEN** the runtime is tainted and follows existing recovery fencing without granting a replacement broader authorization

### Requirement: Gateway accepts long-running work asynchronously
The system SHALL return an accepted response for valid user requests without waiting for Assistant or Worker completion.

#### Scenario: Web message accepted
- **WHEN** Gateway receives a valid Web Assistant message
- **THEN** it returns request id and conversation id, then publishes the corresponding command or event asynchronously

### Requirement: EventCenter distributes versioned integration events
The system SHALL require one StreamOwner per IntegrationEvent stream, allocate sequence with aggregate state and Outbox in the owner transaction, and let EventCenter validate, route, retry, detect duplicates/gaps/integrity conflicts, drive DLQ/replay, and project events without assigning or rewriting domain sequence.

#### Scenario: StreamOwner commits an event
- **WHEN** an owning bounded context changes aggregate state and emits one or more IntegrationEvents
- **THEN** it allocates contiguous stream sequence values and writes state plus Outbox in one transaction before relay

#### Scenario: Duplicate event delivery
- **WHEN** a consumer receives the same IntegrationEvent more than once
- **THEN** it uses event id or idempotency key to avoid duplicate side effects

#### Scenario: Out-of-order event delivery
- **WHEN** a projection receives events for the same stream out of sequence
- **THEN** it durably buffers future sequence, retains the last applied cursor, and requests retry or reconciliation until the gap can be applied in order instead of trusting timestamps

#### Scenario: Stream sequence is reused by another event
- **WHEN** a consumer observes the same stream id and sequence with a different event id
- **THEN** it raises an integrity incident and isolates the event for DLQ/operations instead of overwriting the prior event

### Requirement: User timeline has stable projection order
The system SHALL merge user-visible events from multiple domain streams into TimelineProjection using an append-only TimelineCursor, preserve source stream/sequence and causation, and SHALL NOT claim TimelineCursor or occurred time is a global business order.

#### Scenario: Cross-stream events are projected
- **WHEN** Task and Worker streams emit correlated user-visible events
- **THEN** TimelineProjection records each source stream/sequence and causation link, assigns the next timeline-local cursor, and exposes that cursor for SSE resume and pagination

#### Scenario: User-visible event arrives late
- **WHEN** an event with an earlier occurred time arrives after later Timeline entries have already been delivered
- **THEN** the projector appends a new cursor, keeps the original occurred time, sets `late=true`, and does not reorder or rewrite published cursors

#### Scenario: SSE reconnects
- **WHEN** a client reconnects with its last TimelineCursor
- **THEN** Frontier returns entries after the same timeline epoch/cursor without using occurred time to infer missing items

#### Scenario: Timeline is destructively rebuilt
- **WHEN** a full rebuild cannot preserve existing event-to-cursor mappings and increments timeline epoch
- **THEN** Frontier rejects old-epoch resume with `resync_required` and returns a new snapshot cursor instead of silently translating by timestamp

### Requirement: RocketMQ profile preserves event semantics
The system SHALL use stream id as RocketMQ FIFO message group in P1 while keeping payload sequence, event idempotency, gap repair, integrity incidents, DLQ/replay, causation, and TimelineCursor behavior identical to the P0 PostgreSQL profile.

#### Scenario: RocketMQ redelivers or replays a message
- **WHEN** an event is delivered again from retry or DLQ replay
- **THEN** consumers apply the same event-id and stream-sequence rules and do not rely solely on broker delivery position

### Requirement: User-visible events are separated from internal events
The system SHALL distinguish internal observability events from user-visible progress and result events.

#### Scenario: Internal retry
- **WHEN** Worker retries an internal command
- **THEN** EventCenter may record internal telemetry but does not expose noisy retry details to the user unless user action is required

### Requirement: Delivery maps Feishu context to internal identity
The system SHALL resolve Feishu messages, commands, and callbacks into tenant, user, project, permissions, and channel thread context.

#### Scenario: Unknown Feishu user
- **WHEN** Delivery receives a callback for an unmapped user
- **THEN** it rejects or routes to binding flow without creating a Task

### Requirement: Delivery can create TaskSpec directly
The system SHALL separate executable readiness from authenticated Task creation intent, SHALL route shared-channel ordinary messages to Assistant even when ready, and SHALL allow Delivery to submit TaskSpec directly only from a TaskCreationTriggerPolicy-approved surface that passes readiness, identity, Workspace, permission, validation, and approval checks.

#### Scenario: Explicit Feishu command
- **WHEN** a mapped Feishu user sends an explicit command with target, goal, constraints, and acceptance criteria
- **THEN** Delivery materializes an exact TaskDraft and zero-question ready assessment, records structured creation intent, and submits them to Task Orchestration without an Assistant clarification round trip

#### Scenario: Ready ordinary group message
- **WHEN** a mapped participant posts an ordinary group message whose content can produce a ready TaskDraft
- **THEN** Delivery appends one shared Assistant Turn and exposes an exact ready-draft confirmation action without creating Task

#### Scenario: Ready draft is explicitly confirmed
- **WHEN** an authenticated registered action targets the current TaskDraft/readiness/Workspace/render revisions
- **THEN** Gateway records ExplicitTaskCreationIntent and Task Orchestration revalidates actor, digest, policy and authorization before atomically creating one Task, initial TaskTransition and Outbox

#### Scenario: Confirmation is unbound or stale
- **WHEN** free text has no immutable TaskDraft target or any bound draft, readiness, Workspace, Conversation generation or policy revision changed
- **THEN** the interaction remains Assistant input or returns stale confirmation state and does not migrate intent or create Task

#### Scenario: Private or API request is explicit
- **WHEN** a user-scoped P2P/private request or authenticated API create call unambiguously requests execution and passes the active trigger policy
- **THEN** the system may record explicit intent and use the same zero-question readiness and Task creation gate

#### Scenario: Task creation is duplicated or its response is lost
- **WHEN** the same command or confirmation is delivered again or the original response is unknown
- **THEN** the stable intent key and TaskSpec digest return the original Task identity without creating a second Task/Transition/Outbox or changing Conversation/default Workspace

### Requirement: Shared TaskDraft confirmation is initiator-bound
The system SHALL bind each shared TaskDraft lineage to one immutable initiator, SHALL allow only that actor to confirm the draft, and SHALL require another participant to fork a newly authorized actor-owned draft before confirmation.

#### Scenario: Another participant confirms the source draft
- **WHEN** Bob invokes `create_task` for ready draft D1 whose initiator is Alice
- **THEN** Delivery and Interaction reject `actor_not_initiator`, create no intent or Task, and may expose an explicit adoption action

#### Scenario: Another participant adopts the draft
- **WHEN** Bob invokes AdoptTaskDraft for the exact current D1 revision and passes participation, source-visibility, Workspace and context checks
- **THEN** Interaction creates D2 with Bob as initiator and D1 as derived-from evidence, recomputes readiness/digest, leaves D1 unchanged, and requires Bob to confirm D2 separately

#### Scenario: Source actor capabilities are not inherited
- **WHEN** D2 is created from D1
- **THEN** D2 excludes Alice's creation intent, approvals, repository/secret grants, private evidence and actor-specific assumptions and fails readiness if Bob cannot replace required access

#### Scenario: Adoption competes or repeats
- **WHEN** Bob repeats adoption for the same D1 revision or Carol adopts the same content
- **THEN** Bob receives the original D2 identity while Carol receives a distinct lineage, and equal TaskSpec digest never merges actors or Tasks

#### Scenario: Source scope is no longer safe
- **WHEN** D1 is consumed/cancelled, belongs to a predecessor generation, has a Workspace mismatch, contains inaccessible context or cannot be interpreted under current schema/policy
- **THEN** adoption fails closed without copying protected content; stale readiness or Alice authorization alone may be recovered only by a fresh Bob-specific fork when exact payload and current scope remain valid

### Requirement: Delivery owns channel interaction only
The system SHALL limit Delivery to channel triggers, intermediate choices/dialogue, notification delivery, and final result-card rendering while Task, Assistant, and Worker own business and execution state.

#### Scenario: Worker requests user input
- **WHEN** Task enters `waiting_user`
- **THEN** Delivery renders the requested choice in Feishu and forwards the answer without maintaining a duplicate Task state machine

### Requirement: Frontier exposes task projections
The system SHALL expose user-facing projections and SSE for task status, progress, waiting actions, intermediate results, failures, and artifacts, while Gateway remains the synchronous command and key-query entry.

#### Scenario: Artifact ready
- **WHEN** Task emits a user-visible artifact-ready event
- **THEN** Frontier projection includes artifact type, display label, permission-aware link, and task status

#### Scenario: Worker detail opened
- **WHEN** an authorized user opens a WorkerSession detail view
- **THEN** Frontier exposes projected AgentTurns, active WorkerRun state, stage reports, Trace summaries, ApprovalRequests, and ArtifactActions while raw internal trace remains separately permissioned

### Requirement: Frontend uses WorkerSession as the primary work view
The system SHALL expose WorkerDetail as the primary Coding Agent interaction view, Tasks as a lightweight delivery index/summary, Workspace as a durable context view, Artifact as a canonical detail view, Memory as an independent governance area, and SHALL NOT require Frontend to reconstruct authoritative state from raw events or Trace.

#### Scenario: Task completes while WorkerSession remains active
- **WHEN** Task reaches `completed`, `failed` or `cancelled` but the associated WorkerSession remains within its lease
- **THEN** TaskSummary remains terminal, WorkerDetail remains reachable and interactive, and further chat creates a standalone WorkerRun unless a new Task is explicitly created

#### Scenario: Standalone WorkerRun is displayed
- **WHEN** WorkerSession starts a direct WorkerRun without `task_id`
- **THEN** WorkerDetail displays standalone lineage and Task views do not manufacture a placeholder Task

#### Scenario: Action requires user attention
- **WHEN** any authorized Task or Worker enters waiting_user, approval, conflict, or actionable failure state
- **THEN** Home projects a permission-filtered item with canonical target identity, reason, available actions, and Worker deep link while the owner context retains state authority

#### Scenario: Artifact is opened from different views
- **WHEN** the same artifact_id is opened from Task, Worker, Workspace, or search
- **THEN** every route resolves to one permission-aware ArtifactDetail projection and action state rather than copied per-page ownership

#### Scenario: Memory evidence references execution
- **WHEN** MemoryLibrary or ReviewInbox displays evidence from a Task or Worker trace
- **THEN** Memory governance remains in its own view and uses a permission-aware evidence link instead of embedding a second review state machine in Task or Worker

#### Scenario: Feishu creates a Task without Assistant Conversation
- **WHEN** Delivery submits an executable TaskSpec without an Assistant Conversation
- **THEN** the Task appears in Tasks and Home recent deliveries based on its canonical identity and does not require a synthetic Conversation parent

### Requirement: Frontier synchronizes views with snapshot and typed SSE delta
The system SHALL return a complete authorized ProjectionSnapshot for each page View, SHALL stream versioned semantic ProjectionDelta events over SSE after the snapshot cursor, SHALL maintain epoch/cursor/version continuity, and SHALL require deterministic resynchronization when replay cannot safely continue.

#### Scenario: Event commits between snapshot and subscription
- **WHEN** a projection delta commits after the snapshot cursor is read but before EventSource connects
- **THEN** the SSE replay after `Last-Event-ID=epoch:cursor` includes that delta exactly once logically and the client reaches the current projection version

#### Scenario: Client reconnects within replay window
- **WHEN** SSE disconnects and reconnects with the last applied event id while its cursor remains retained
- **THEN** Frontier replays later deltas in cursor order and duplicate delivery is idempotently ignored

#### Scenario: Cursor cannot be replayed
- **WHEN** client epoch is old, cursor is evicted, a replay gap exists, or base projection version is unreachable
- **THEN** Frontier returns `resync_required` and Frontend discards only the affected View cache and fetches a fresh snapshot without timestamp inference

#### Scenario: Client schema is incompatible
- **WHEN** no compatible snapshot/delta schema can be emitted for the Frontend version
- **THEN** Frontier returns `client_upgrade_required` rather than causing an infinite resync loop

#### Scenario: Command is accepted before projection changes
- **WHEN** Gateway accepts an idempotent command but its domain result has not reached the ViewProjection
- **THEN** Frontend may show pending by command id but does not mutate authoritative status until a causal delta or refreshed snapshot converges

#### Scenario: Access is revoked during stream
- **WHEN** actor loses Tenant/Workspace/resource access while SSE is open
- **THEN** Frontier stops protected deltas, clears the View through a non-disclosing control path, closes the stream, and does not leak related identities

#### Scenario: GraphQL is absent in P0
- **WHEN** Frontend queries TaskSummary, Worker, Workspace, Home or Artifact views in P0
- **THEN** it uses the versioned Frontier snapshot endpoints and typed SSE protocol without requiring GraphQL schema, resolvers, normalized cache, or subscription transport

### Requirement: Agent display separates durable checkpoints from transient chunks
The system SHALL commit complete recoverable DisplayCheckpoint state at policy and semantic boundaries, SHALL stream fine-grained TransientDisplayDelta without advancing ViewCursor, SHALL converge every display stream to a complete terminal checkpoint, and SHALL preserve high-fidelity source evidence independently in Trace JSONL.

#### Scenario: Text streams normally
- **WHEN** Agent emits text or user-visible reasoning-summary chunks between checkpoints
- **THEN** Frontier may stream/coalesce transient deltas against the current checkpoint without creating one projection version per chunk

#### Scenario: Semantic state changes
- **WHEN** Tool, Approval, waiting_user, Artifact, error, cancellation, timeout, message or Run crosses a semantic lifecycle boundary
- **THEN** the system commits a durable DisplayCheckpoint and publishes a replayable `view.stream_checkpointed` ProjectionDelta

#### Scenario: Browser misses transient chunks
- **WHEN** transient sequence has a gap or browser reconnects after best-effort chunks were lost
- **THEN** Frontend stops appending only the affected part and replaces it from the next/current checkpoint without changing Task, WorkerRun or Tool authority

#### Scenario: Terminal display completes
- **WHEN** message, AgentTurn or WorkerRun reaches terminal display state
- **THEN** a complete terminal checkpoint can render exact final content after all transient buffers are discarded

#### Scenario: Backpressure occurs
- **WHEN** a client or connection cannot consume transient output fast enough
- **THEN** Frontier bounds queues, coalesces same-part chunks, drops progress pulses first, and never drops durable checkpoint/control or semantic state

#### Scenario: Raw execution evidence is needed
- **WHEN** a transient browser delta was coalesced, dropped or never replayed
- **THEN** authorized debugging/Evolution still reads the corresponding high-fidelity ACP/Agent evidence from Trace according to TraceManifest and lifecycle policy

#### Scenario: Model emits hidden reasoning
- **WHEN** a provider exposes hidden chain-of-thought or internal reasoning not intended for users
- **THEN** Frontier does not project it as DisplayPart and may only expose an authorized user-visible reasoning summary under model/Trace policy

### Requirement: DisplayPart is a Switchback-owned stable materialized union
The system SHALL represent user-visible Agent output as versioned Switchback DisplayPart types `text`, `reasoning_summary`, `tool`, `activity`, `approval`, `artifact`, `source` and `error`, SHALL map external protocol identities and lifecycles through adapters, SHALL keep actions as revalidated Gateway command references, and SHALL degrade or fail closed according to criticality and security semantics.

#### Scenario: ACP text is materialized
- **WHEN** Codex/ACP emits assistant text chunks and a later complete message state
- **THEN** the adapter maps them to one stable Switchback `text` part, uses transient deltas for compatible chunks, and checkpoints complete content with monotonic part revision

#### Scenario: Source Tool identity is reused or scoped differently
- **WHEN** provider-local Tool ids collide across Agent sessions, subagents or runtime generations
- **THEN** the adapter uses a scoped identity map and canonical `tool_invocation_id` rather than merging by source id alone

#### Scenario: Optional unknown progress arrives
- **WHEN** an unknown source update is demonstrably optional progress with no permission, Tool lifecycle or security effect
- **THEN** the system preserves raw Trace, records telemetry, and may render a restricted non-critical `activity` fallback

#### Scenario: Unknown critical source semantics arrive
- **WHEN** an adapter cannot safely classify a Tool, approval, permission or security lifecycle update
- **THEN** it fails closed and enters protocol incompatibility or waiting_user handling without downgrading the update to harmless progress

#### Scenario: Browser does not know a part type
- **WHEN** RendererRegistry receives a compatible-schema unknown DisplayPart
- **THEN** it uses a sanitized no-action fallback for a non-critical part and returns `client_upgrade_required` for a critical part

#### Scenario: Display payload attempts executable content
- **WHEN** source content includes arbitrary HTML/JavaScript/components, credential-bearing raw payload or an ungoverned host path
- **THEN** adapter/rendering policy rejects or sanitizes it and preserves only separately authorized evidence in Trace

#### Scenario: User invokes a displayed action
- **WHEN** Frontend or Delivery selects a DisplayPart available action
- **THEN** it submits the referenced Gateway command and the owner context revalidates actor, target, expected revision, policy and idempotency before any state change

#### Scenario: Display schema changes incompatibly
- **WHEN** a change adds a required field, changes meaning, expands visibility or changes permission/security behavior
- **THEN** the system introduces a new schema version and does not treat it as an additive v1 extension

### Requirement: P0 Frontend uses a React/Vite SPA with explicit state ownership
The system SHALL build P0 Frontend as a React + TypeScript Vite SPA served same-origin with Go Gateway/Frontier, SHALL separate route identity, HTTP lifecycle, materialized View replica, transient overlay and PendingCommand ownership, SHALL keep optional chat frameworks behind adapters, and SHALL not require a Next.js/Node production application runtime.

#### Scenario: Snapshot loads an active View
- **WHEN** TanStack Query completes a validated ProjectionSnapshot request
- **THEN** it atomically replaces the affected ClientViewReplica in ViewProjectionStore without creating an independently evolving second authoritative copy in component/query state

#### Scenario: Durable and transient stream updates arrive
- **WHEN** Frontier emits ProjectionDelta and TransientDisplayDelta
- **THEN** the dedicated store validates/applies the durable reducer to epoch/cursor/version state and applies transient content only to a bounded overlay

#### Scenario: Gateway accepts a command
- **WHEN** a command returns accepted with command_id
- **THEN** Frontend records a non-authoritative PendingCommand and resolves it only from causal ProjectionDelta, command-status result or replacement Snapshot evidence

#### Scenario: HTTP or SSE connectivity is lost
- **WHEN** command response, projection result or stream connectivity is delayed or lost
- **THEN** Frontend preserves an unknown/pending outcome and follows idempotent status/resync behavior instead of inventing success/failure or issuing a new logical command

#### Scenario: Worker Chat uses assistant-ui
- **WHEN** assistant-ui components are enabled for WorkerDetail
- **THEN** ExternalStore conversion preserves Switchback canonical ids and only exposes implemented Gateway callbacks while Switchback retains persistence, replay and Tool/Approval authority

#### Scenario: SPA is deployed to production
- **WHEN** the Vite bundle is published
- **THEN** static assets and index caching, API/SSE/Artifact route exclusion, same-origin cookie authentication, CSRF/idempotency, no client secrets and client-upgrade compatibility are verified without running a Node application server

### Requirement: Delivery renders a channel-specific card projection
The system SHALL derive versioned DeliveryCardProjection state from authorized owner/public facts, SHALL keep provider CardInstance state separate from business authority, SHALL expose only a conservative current action subset, SHALL route callbacks through authenticated idempotent Gateway commands, and SHALL degrade unsupported or expired interactions to safe Web links.

#### Scenario: Semantic Task progress changes
- **WHEN** owner events or durable Display summaries cross a card policy/semantic boundary
- **THEN** the channel projector advances projection/render revision and Delivery updates the existing CardInstance without streaming every Agent token

#### Scenario: User invokes an inline card action
- **WHEN** current projection exposes exact TaskDraft creation/adoption, choice/text/approval/cancel/accept/rework and Feishu sends a callback
- **THEN** Delivery verifies provider, actor, binding, membership, action token, render revision, expiry and duplicate identity before submitting one idempotent Gateway command

#### Scenario: Shared Task action is clicked
- **WHEN** a shared Task card exposes a cancel, accept or rework candidate and any group actor clicks it
- **THEN** Delivery treats the descriptor as a command shape rather than authority, forwards the actual actor and expected Task revision, and lets Task Orchestration revalidate TaskInitiator or Workspace-owner emergency-cancel rules

#### Scenario: AgentTurn deadline is displayed
- **WHEN** a Delivery card references an active or timed-out AgentTurn
- **THEN** Delivery may show its fixed start/deadline/terminal state but exposes no time-extension or continuation-budget action

#### Scenario: Callback is accepted
- **WHEN** Gateway accepts the command and Delivery responds to the provider within its deadline
- **THEN** the card may show pending/accepted feedback but does not claim business success until owner evidence advances the projection

#### Scenario: Rich or high-risk action is requested
- **WHEN** the user needs full Artifact review, patch apply, branch/PR/production write, Skill/Tool operation or unrestricted Worker interaction
- **THEN** Delivery omits the unsafe inline action and provides an authorized canonical Web deep link

#### Scenario: Card action is stale or expired
- **WHEN** render revision, owner action, membership or provider interaction window is no longer valid
- **THEN** Delivery fails closed, returns a non-misleading stale/expired response and renders current read-only summary or Web link without mutating owner state

#### Scenario: Provider callback or update is duplicated or its response is lost
- **WHEN** the same provider interaction/update is delivered again or outcome is unknown
- **THEN** stable callback/render identity converges idempotently and Delivery reconciles provider state when possible without issuing a second business command

### Requirement: Channel threads default to the Assistant Conversation
The system SHALL bind one active provider channel scope to one primary Assistant Conversation, SHALL route ordinary messages without a validated explicit target to that Conversation, SHALL require explicit target evidence for TaskDraft/Task/WorkerSession/Approval/Artifact operations, and SHALL not maintain or infer a mutable current execution focus.

#### Scenario: Ordinary reply arrives while multiple Workers are active
- **WHEN** an authorized user sends an ordinary message without an explicit target in a bound topic
- **THEN** Delivery creates one Assistant Turn in the bound Conversation regardless of latest Worker, Task, card or activity

#### Scenario: User directly continues a Worker
- **WHEN** a current card action, structured command or Web interaction supplies an authorized WorkerSession InteractionTargetRef
- **THEN** Gateway submits the direct Worker command to that exact WorkerSession without changing the ChannelThreadBinding default

#### Scenario: User operates a Task, approval or Artifact
- **WHEN** the interaction carries a current canonical target, expected revision and authenticated intent evidence
- **THEN** Delivery revalidates actor, binding, Tenant/Workspace permission and owner action before submitting one idempotent Gateway command

#### Scenario: Old card receives a plain topic reply
- **WHEN** a user posts ordinary text near or under a historical result card without invoking its registered action
- **THEN** provider ancestry or visual proximity does not select the old subject and the message routes to Assistant

#### Scenario: Provider retries a receive event
- **WHEN** the same provider message identity and content revision is delivered more than once
- **THEN** Delivery records one InboundInteractionEnvelope and creates at most one Assistant Turn or explicit command

#### Scenario: Binding or explicit target is invalid
- **WHEN** binding revision is stale/revoked, actor access is missing, or the target is forged, stale or cross-Tenant
- **THEN** Delivery fails closed without disclosing protected target existence or falling back to a guessed current focus

#### Scenario: Provider cannot use topic replies
- **WHEN** DeliveryProviderCapability requires a chat/root-message or P2P fallback binding
- **THEN** the fallback preserves Assistant-default routing, explicit targets, actor authorization and idempotency semantics

### Requirement: Group topics share one Conversation with explicit controllers
The system SHALL use one channel-scoped shared Assistant Conversation per active group topic binding revision, SHALL attribute every Turn and command to the actual actor, SHALL distinguish participant dialogue from controller context/lifecycle authority, and SHALL reauthorize every resource action independently of Conversation role.

#### Scenario: Multiple mapped group users speak
- **WHEN** eligible users send ordinary messages in the same active group topic
- **THEN** Delivery and Interaction append each message exactly once to the same current Conversation generation with the correct actor and do not create hidden per-user Conversations

#### Scenario: Binding is first created
- **WHEN** an authenticated mapped user creates a group binding and passes Tenant/channel policy
- **THEN** Interaction records that actor as the initial controller and does not grant controller to every provider member

#### Scenario: Participant attempts a control action
- **WHEN** an active participant who is not controller selects/clears Workspace, requests rollover, closes the channel Conversation or manages controllers
- **THEN** the control command is denied without removing ordinary dialogue eligibility

#### Scenario: Controller operates a protected target
- **WHEN** a controller requests Workspace rebind, Task/Worker/Approval/Artifact action or group disclosure
- **THEN** Gateway reauthorizes the actual actor and exact target because controller role grants none of those permissions

#### Scenario: Controllers are delegated or revoked
- **WHEN** a controller changes another participant role with current expected revisions
- **THEN** Interaction records one audited idempotent change and rejects any explicit operation that would remove the last active controller

#### Scenario: Last controller loses access
- **WHEN** provider/Tenant access revocation invalidates every current controller
- **THEN** Conversation enters `control_orphaned`, control mutations are fenced, no participant is auto-promoted, and only policy-authorized explicit recovery can install a new eligible controller

#### Scenario: Private details are needed in P0
- **WHEN** one actor needs protected approval detail, report/patch content or unrestricted Worker chat
- **THEN** Delivery uses actor-bound ephemeral interaction when supported or an individually authorized Web route rather than a private sidecar Conversation in the group topic

#### Scenario: Shared Conversation rolls to a successor
- **WHEN** Workspace rollover replaces one channel Conversation generation with another
- **THEN** Interaction projects only currently eligible source participant/controller roles with source revision evidence, never adds or promotes a controller, and marks the successor `control_orphaned` when none remains eligible

### Requirement: Workspace is explicitly resolved before protected context or execution
The system SHALL allow Workspace-unresolved Assistant conversation, SHALL require an explicit authorized WorkspaceContextResolution before Workspace-scoped reads or Task/Worker execution, SHALL require TaskSpec to snapshot exact Workspace evidence, and SHALL never treat defaults, discovery or inference as authority.

#### Scenario: User brainstorms before choosing a Workspace
- **WHEN** the Conversation has no resolved default Workspace
- **THEN** Assistant may clarify using authorized tenant/user context but Workspace-scoped context loading, TaskSpec dispatch and WorkerSession create/reuse remain blocked

#### Scenario: User selects an existing Workspace
- **WHEN** an authenticated Web/card/structured command names a Workspace with expected binding revision
- **THEN** Gateway validates TenantMembership and WorkspaceMembership and Interaction records one idempotent WorkspaceContextResolution with actor, evidence and observed revisions

#### Scenario: Work needs a new Workspace
- **WHEN** the user explicitly chooses create-new
- **THEN** a separate idempotent CreateWorkspace command completes and its exact result is explicitly selected before TaskSpec submission

#### Scenario: Candidate appears obvious
- **WHEN** a user-global default, one authorized candidate, repository URL, related ref, recent activity or model output suggests a Workspace
- **THEN** Delivery/Assistant may preselect or propose it but does not resolve, load protected context or execute until positive user confirmation

#### Scenario: TaskSpec is submitted
- **WHEN** readiness succeeds with a resolved Workspace
- **THEN** Task Orchestration revalidates exact workspace, resolution/binding/Membership revisions and atomically snapshots them before creating Task/TaskTransition/Outbox

#### Scenario: Default Workspace is changed
- **WHEN** an authorized explicit rebind advances the resolution revision
- **THEN** unresolved-first selection stays in place, but leaving resolved W1 atomically advances ChannelThreadBinding to a successor Conversation for future untargeted context/Task drafts while existing Task, WorkerSession, Trace and Artifact ownership remains unchanged

#### Scenario: Explicit resource belongs to another Workspace
- **WHEN** the user invokes an authorized action on an existing Task/Worker/Artifact
- **THEN** the command uses that resource's own Workspace and does not mutate the channel default

#### Scenario: Selection becomes stale or revoked
- **WHEN** binding/resolution revision or WorkspaceMembership is no longer valid
- **THEN** protected use fails closed or enters waiting_user without falling back to another default/candidate and without partially creating a Task

### Requirement: Workspace group disclosure uses an explicit independent grant
The system SHALL require an active WorkspaceChannelShareGrant and current ChannelDisclosureDecision before rendering Workspace-derived content to a provider group, SHALL intersect source classification with a policy-bounded disclosure profile, SHALL keep action/Web authorization per actor, and SHALL degrade or suspend disclosure when audience authority is missing or uncertain.

#### Scenario: Workspace owner grants a verified group audience
- **WHEN** a current Workspace owner explicitly selects one provider group, permitted channel bindings and the P0 workspace_summary profile with expected revisions
- **THEN** Identity & Access verifies Tenant/Workspace ownership and a fresh provider audience/security snapshot and records one idempotent revisioned WorkspaceChannelShareGrant

#### Scenario: Rich shared card is rendered
- **WHEN** an active grant, current policy and fresh provider snapshot match the exact Workspace/channel and owner projections request channel-shareable fields
- **THEN** Identity & Access emits a ChannelDisclosureDecision containing only the source/profile intersection and Delivery renders only that field allowlist

#### Scenario: Grant is absent or unusable
- **WHEN** the grant is missing, wrong-scope, stale, suspended, revoked or expired, owner access is invalid, or provider state is stale/unknown
- **THEN** Delivery emits only a non-resource-identifying minimal notice plus a canonical non-bearer Web link or authorized per-user ephemeral card

#### Scenario: Restricted source content is requested
- **WHEN** a card source contains raw Trace, private prompt, hidden reasoning, secret, Tool stdout, internal error detail, full report, patch/diff or unrestricted Artifact content
- **THEN** platform hard policy excludes the content even with an active workspace_summary grant and the renderer cannot fetch or infer it

#### Scenario: Provider audience expands or security changes
- **WHEN** membership/audience expansion, internal-to-external, private-to-public, bot removal or inspection loss is observed, or reconciliation cannot prove snapshot freshness
- **THEN** rich disclosure is suspended idempotently before the next render and existing cards are degraded or cleaned up best-effort

#### Scenario: A group member clicks a card action or Web link
- **WHEN** any channel actor invokes the shared card or opens its canonical route
- **THEN** Gateway/Web maps and authorizes that actor independently because WorkspaceChannelShareGrant grants neither WorkspaceMembership nor action authority

#### Scenario: Share grant is revoked after content was sent
- **WHEN** an owner revokes the grant or policy revokes its authority
- **THEN** Switchback fences future rich sends, records provider update/recall outcome and residual exposure, and does not claim that already read, copied, quoted or exported content was erased
