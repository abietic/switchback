## Context

Switchback / Switchback is being shaped from an internal platform idea into a concrete backend architecture. The core risk is not a missing framework; it is ambiguity in module responsibility. Assistant, Worker, Evolution, Delivery, EventCenter, LLMProxy, Skilling, and Gateway can all appear to "coordinate work" unless their boundaries are made explicit.

The current baseline is a Go monorepo using DDD-style boundaries, PostgreSQL transactional outbox and governed-context revisions, a local filesystem ObjectStore adapter, OpenViking as the required active-context retrieval projection, and LiteLLM as the P0 model gateway. RocketMQ and S3-compatible endpoints are P1 integration profiles; MongoDB, Jenkins, local GitLab, Kubernetes on developer machines, MinIO, and LLMGateway are not P0 dependencies.

Stakeholders include internal users, platform engineers, Coding Agent maintainers, Skill/MCP maintainers, security reviewers, infrastructure owners, and product owners responsible for Web/Feishu delivery experience.

## Goals / Non-Goals

**Goals:**

- Define reviewable capability specs for Assistant, Worker, Evolution, Delivery/Eventing, and model/capability governance.
- Establish TaskSpec, IntegrationEvent, TraceRecord, ArtifactRef, Truth, ContextPack, Skill/MCP Manifest, and LLMProxy metadata as first-class contracts.
- Make the Assistant/Worker split enforceable: Assistant reduces requirement uncertainty; Worker performs heavy execution.
- Make long-running tasks asynchronous, observable, idempotent, and recoverable.
- Require P0 Worker execution to use Codex as the first real Coding Agent in an isolated local Docker-compatible container, communicating with the backend over the network.
- Deliver report and patch artifacts first, then let users continue interacting with the same WorkerSession.
- Preserve evidence and permissions for long-term Truth generation.
- Add Mermaid diagrams as living architecture documentation.

**Non-Goals:**

- Implement services or production deployment in this change.
- Choose a final post-P0 Worker Agent set, production Kubernetes topology, CI provider, or LLMGateway implementation.
- Build a visual workflow engine.
- Treat OpenSpec documents as a replacement for source code, tests, or runtime evidence.

## Decisions

### Decision 1: Use TaskSpec as the Assistant/Delivery to Worker boundary

Assistant and Delivery will not call Worker with free-form prompts as the primary contract. They will submit TaskSpec with goal, context refs, constraints, acceptance criteria, execution policy, runtime config, agent config, capability refs, and approval policy.

Why: free-form prompts make execution hard to audit, retry, resume, or compare. TaskSpec creates a stable language that can be validated before allocating expensive runtime.

Alternatives considered:

- Direct prompt handoff to Worker: faster to prototype, but hides permissions and acceptance criteria in prose.
- A full workflow DSL: more powerful, but too heavy before the core task contract stabilizes.

### Decision 1a: Allow Delivery to create TaskSpec directly

Delivery may create TaskSpec directly for clear channel requests. It still must resolve identity, permissions, validation, and approval policy before Task Orchestration accepts the task.

Why: Feishu and other channels can contain explicit commands that should not pay an unnecessary Assistant clarification round trip.

### Decision 2: Keep EventCenter out of business ownership

EventCenter distributes IntegrationEvents, drives projections, retries consumers, and manages dead letters. It does not become the source of truth for Task, WorkerRun, Truth, or Artifact state.

Why: if EventCenter owns business facts, replay, correction, idempotency, and projection repair become coupled to event transport. Aggregates should own state; EventCenter should move facts.

Alternatives considered:

- Event sourcing for every aggregate: useful later for some domains, but too expensive as a P0 platform-wide rule.
- Service-local queues only: simpler initially, but weakens cross-module visibility and DLQ operations.

### Decision 3: Store execution evidence in object storage JSONL

Assistant Turns, Worker Trace, raw transcripts, logs, and artifacts are stored as append-friendly object storage records, with RDBMS holding current state and references.

Why: traces can be large and semi-structured. RDBMS is better for state machines and idempotency, while object storage is cheaper and better suited to audit payloads.

P0 Trace will preserve a fidelity similar to normal Coding Agent local session JSONL. Redaction is not a P0 blocker, although retention and access control are still required.

Alternatives considered:

- Store all trace in MongoDB: flexible, but can become expensive and less portable for raw transcript replay.
- Store only summaries: cheaper, but breaks evidence-based Evolution and debugging.

### Decision 4: Treat Truth as governed knowledge, not plain RAG

Evolution generates TruthCandidate from evidence, classifies scope/risk/confidence, and only then publishes active Truth. Assistant and Worker read Truth through Evolution and compile ContextPack themselves, rather than using unrestricted search.

Why: long-term memory can silently corrupt behavior if it lacks source, scope, expiry, and review. Governance is part of product correctness.

Evolution owns Truth and provides Truth read capability. Inspiration owns external resource indexes and summaries. Full Feishu document reading is performed by Assistant or Worker agents through lark-cli and the relevant Skill using links returned by Inspiration.

Alternatives considered:

- Direct vector upsert from transcripts: simple, but hard to delete, explain, or trust.
- Manual-only memory: safer, but loses the self-evolution value proposition.

### Decision 5: Separate capability declaration from runtime injection

Skilling owns Skill/MCP/AgentProfile manifests and authorization. Worker performs final runtime injection based on TaskSpec, CapabilityBundle, and policy.

Why: capability metadata belongs in a governance context, but actual injection depends on runtime lease, workspace, secrets, and network policy.

Alternatives considered:

- Let Worker discover all skills dynamically: flexible, but unsafe and hard to audit.
- Hard-code skills into Agent images: stable, but blocks per-user/project evolution.

### Decision 6: Use OpenSpec and Markdown/Mermaid as planning source

OpenSpec captures changes and capability requirements. Markdown/Mermaid documents capture diagrams, contracts, matrices, and context map.

Why: the current project is still architecture-heavy. A versioned spec process prevents future work from bypassing hard decisions while staying lightweight enough for a monorepo.

Alternatives considered:

- Single `README` design: too coarse for multi-module architecture.
- Heavy external design system: unnecessary before code and teams stabilize.

### Decision 7: P0 Worker uses Codex as the first real containerized Coding Agent

P0 will not stop at a mock Worker. Codex is the first AgentProfile and must run inside an isolated Docker-built container, avoid host-machine side effects, and communicate with the backend over the network. Worker controls Codex through ACP rather than bare-launching Codex and scraping terminal output. The Worker model still keeps AgentProfile and AgentAdapter pluggable so Cline, OpenHands, Claude Code, Aider, or internal ACP agents can be added later.

Why: the core product promise is cloud execution by actual Coding Agents. A mock Worker validates event plumbing but does not validate runtime isolation, Agent session continuity, model proxying, trace capture, or artifact production.

### Decision 8: Worker runtime follows CI/CD runner style local container execution

P0 RuntimeProvisioner follows the traditional CI/CD runner pattern: the host Worker service schedules a job/session into a Docker-compatible local container image, mounts that WorkerSession's isolated persistent WorkingCopy and cache, injects env/secrets/capabilities, streams logs and trace, collects artifacts, then reclaims the container.

Why: GitHub Actions job containers, GitLab Runner Docker executor, and Jenkins Docker Pipeline all converge on the same runner-orchestrates-container model. It is mature enough for local P0 validation and keeps a later move to K8s or internal sandboxing behind the RuntimeProvisioner boundary.

### Decision 9: Codex model traffic goes through a LiteLLM co-process

P0 WorkerSession images are built with Docker and include a Go Worker runtime shim, `agentclientprotocol/codex-acp`, Codex, and a co-located LiteLLM process. The shim uses the community `github.com/coder/acp-go-sdk` as an ACP client over local stdio; the shim itself communicates with Switchback backend APIs over the network. Authentication enters through environment variables or mounted config, and Codex model traffic uses LiteLLM on localhost.

Why: this keeps model routing, budget, audit, and fallback outside Codex-specific business logic while preserving the P0 one-to-one WorkerSession/container mapping. Switchback contracts avoid LiteLLM-specific fields; no second gateway is implemented now.

### Decision 10: WorkerSession is user-scoped and interactive

WorkerSession is a user-level interactive execution session with a 48-hour idle timeout and an absolute maximum lifetime of 14 days. P0 has a WorkerSession continuity limit of 10 per Tenant/User; exact counted states and capacity-wait behavior are refined by Decision 53. Valid interaction renews the idle deadline but never the absolute deadline. `waiting_user` pauses idle countdown but not absolute lifetime. Before either timeout, follow-up chat/command resumes the exact targeted WorkerSession.

Why: the product needs follow-up interaction with an ongoing coding session, not only one-shot task execution.

P0 fixes `worker_session_id` when the first container starts. It maps to at most one active RuntimeInstance/container at a time; crash recovery reuses the WorkerSession id, increments runtime generation, and records a new container id. A WorkerSession may also map to multiple historical RuntimeInstances and Codex/Agent session ids.

### Decision 11: Langfuse is an observability projection, not the authoritative trace store

Worker and LLMProxy trace data should be exported to Langfuse for session, LLM, tool, latency, token, and cost observability. Object storage JSONL remains the authoritative evidence store for Switchback Trace, Evolution input, replay, audit, and artifact linkage.

Why: Langfuse is useful for debugging and LLM observability, but Switchback still needs a durable product-owned trace format with explicit retention, access control, schema versioning, and evidence references.

### Decision 12: Separate Gateway commands from Frontier realtime output

Gateway owns synchronous commands and key metadata queries. EventCenter drives Frontier projections, Frontier exposes SSE and read models to Frontend, and Frontend renders intermediate and final results. Delivery is a Feishu channel adapter for triggering work, intermediate choices/dialogue, and result cards; it does not own Task or Worker state.

Why: execution, projection, Web transport, and Feishu rendering have different change rates and failure modes. Keeping them separate prevents Gateway or Delivery from becoming a second orchestrator.

### Decision 13: Use stream sequence for event order

IntegrationEvent carries `stream_id` and monotonically increasing `sequence`. P0 PostgreSQL outbox dispatch groups by stream; a P1 RocketMQ message group reuses the same stream id. Consumers deduplicate by event/idempotency key, and `occurred_at` is only audit/display metadata.

Why: timestamps from multiple processes cannot establish reliable distributed order, and both polling retries and RocketMQ provide at-least-once behavior.

### Decision 14: Approve external capability changes, not normal sandbox work

Worker performs ordinary file, command, test, report, and patch operations inside its sandbox without per-operation approval. New external-platform authentication, cross-sandbox or production writes, and untrusted Skill installation require approval. An explicit Assistant conversation requesting installation becomes an auditable CapabilityInstallGrant. Operation approval may be granted once or for the WorkerSession, but session scope remains constrained to a specific capability, target, limits, and expiry.

Why: sandbox isolation already contains ordinary coding side effects; asking on every write would make a real Coding Agent unusable while failing to focus review on external effects.

### Decision 15: Govern Truth publication and Inspiration separately

Evolution uses a model to recommend auto-publish, review, or rejection, then applies a deterministic policy gate that can only tighten the decision. Switchback RDBMS owns candidates, immutable revisions, reviews, audit, tombstones, and publication state; OpenViking is the required active-context retrieval projection and snapshot target. Automatic Memory publication follows the narrow allowlist and evidence rules in Decision 26. InstructionPolicy and SkillPackage generation or modification require human review. Inspiration is retained as capability-gated P2 experimental: P0/P1 ContextPack works without it, and implementation waits for a manual-manifest Workspace pilot to demonstrate reduced clarification, relevant candidate hits and actual source reads. If activated, it uses a protected versioned repository with YAML entries, Markdown context, a generated index and rebuildable PostgreSQL/OpenViking projections; semantic changes require PR/CODEOWNER review.

Why: model judgment is useful for fuzzy knowledge value, but must not bypass production, security, cross-user, conflict, or low-confidence review rules.

### Decision 16: Adopt a minimal P0 infrastructure baseline

P0 uses GitHub, Docker Engine/Compose, PostgreSQL outbox, a local filesystem ObjectStore adapter, LiteLLM, OpenViking, and optional Langfuse. In M1, Docker Compose starts the long-lived `switchback-backend` and PostgreSQL containers; RuntimeSupervisor is a Go component inside `switchback-backend` and uses a Docker Socket mounted only into that trusted loopback-only Backend to create sibling WorkerSession containers. Worker containers have no Docker Socket or privileged mode, and all mount sources are derived from the server-configured Switchback data root. P1 adds dedicated GitLab, RocketMQ 5.x, S3-compatible ObjectStore, and Kubernetes integration profiles when their acceptance criteria become relevant. Jenkins, MongoDB, local GitLab, local Kubernetes, and MinIO are not P0 defaults.

Why: these choices validate the product's real risks without requiring every developer to operate a full internal platform stack. MinIO's community distribution is no longer a stable new-project default.

### Decision 17: Separate durable Workspace from runtime and repository identity

Every WorkerSession binds to exactly one Workspace, and a Workspace may host one or more WorkerSessions. Workspace is a durable logical working context that may contain zero or more RepositoryCheckouts, files, artifacts, design materials, and Agent state. RuntimeInstance is only a replaceable container generation. Research or design work may start without a repository, multi-repository work attaches multiple checkouts, and a repository may be created later from produced artifacts.

P0 uses isolated WorkingCopies: every WorkerSession starts from an immutable WorkspaceRevision, Git repositories use per-session worktrees/internal refs, and non-repository files use an isolated materialized tree and content manifest. Worker produces a ChangeSet. The physical storage, staging, fencing, and finalize transaction are fixed separately in Decision 29. Stale bases require rebase or conflict resolution; last-write-wins and partial authoritative updates are forbidden.

Why: equating Workspace with a repository excludes non-code work, while equating it with a container loses state across runtime recovery. Isolated WorkingCopies preserve parallel Worker execution without exposing a shared mutable filesystem, and serialized revision updates make conflict and recovery semantics auditable.

### Decision 18: Direct Worker detail chat uses AgentTurn

An authorized user may open a Worker detail view, inspect projected trace and stage results, and send prompt, queue, interrupt, or steer AgentTurns directly to WorkerSession without returning through Assistant. AgentTurns may affect an active WorkerRun or start a later WorkerRun. WorkerRun success creates a stage report and artifacts but does not close WorkerSession. Patch output remains the P0 artifact; preview, download, apply, and explicit branch publication are ArtifactActions, and external publication requires a matching RepositoryAccessGrant without repeating prompts inside an active session-scoped Grant.

Why: the product needs the continuity and control surface of a normal Coding Agent TUI while preserving Task, WorkerRun, Trace, Artifact, and permission boundaries.

### Decision 19: Prefer gRPC Worker control with HTTPS fallback

Worker Runtime Shim initiates one transport-independent WorkerControlChannel to the backend. P0 prefers an outbound gRPC bidirectional stream for commands, ACKs, heartbeats, and realtime events. When HTTP/2, proxy, connectivity, or transport timeout prevents gRPC, the shim re-bootstraps onto HTTPS long-poll command retrieval plus batched ACK/event POST. Trace and Artifact payload bytes always use separate resumable HTTPS upload.

Both control transports use the same RuntimeControlEnvelope, command/event sequence, received/accepted/completed ACK state, idempotency key, runtime generation, ChannelEpoch, and resume cursors. A new epoch fences the old transport. Authentication, authorization, policy, schema, and protocol-version errors fail closed and never trigger transport fallback. A fallback epoch stays on HTTPS until the next safe reconnect rather than probing back to gRPC during active work.

Why: gRPC gives the normal low-latency bidirectional control path, while standard HTTPS survives environments whose proxies or network policy cannot carry gRPC. Keeping business semantics above transport avoids two command systems and makes reconnect/fallback testable. WebSocket is not required in P0; Frontier SSE remains a separate browser projection channel.

### Decision 20: Allow one automatic RuntimeInstance recovery per incident

P0 Supervisor opens a RuntimeRecoveryIncident for confirmed process exit, container loss, heartbeat timeout, ACP connection close, or cancellation-grace expiry without a terminal prompt response, and may automatically create exactly one replacement RuntimeInstance. `active_quiet` and deadline expiry before cancellation are not recovery failures. The Supervisor preserves WorkerSession, Workspace, WorkingCopy, durable Agent state, Trace and control cursors, and still-valid scoped ApprovalGrants; it increments runtime generation, fences the old generation and ChannelEpoch, and issues fresh SecretLeases. The replacement must resume the prior ACP AgentSession, pass control, LiteLLM, workspace, and Agent health checks, and satisfy the recovery stability criterion before the incident closes.

If the replacement fails, ACP resume cannot be proven safe, or state consistency is uncertain, WorkerRun and WorkerSession enter `waiting_user`. The platform does not start another replacement or silently create a new AgentSession. A replacement closes the incident only after passing all health checks and satisfying a configured stability window or durable progress checkpoint; a later independent failure then creates a new incident with its own single attempt. Cancellation, idle or absolute expiry, authentication, authorization, policy, schema, and protocol failures are not auto-recovery triggers.

Why: one automatic replacement handles transient runtime failures without interrupting the user, while an incident-scoped attempt limit prevents crash loops and unbounded cost. Preserving logical state but replacing ephemeral credentials and runtime identity keeps continuity without weakening fencing or access control.

### Decision 21: Separate Runtime heartbeat from Agent progress

P0 Worker Runtime Shim emits RuntimeHeartbeat every 15 seconds independently of model and tool streams. The backend uses receive time and monotonic sequence. At 45 seconds without a valid heartbeat, RuntimeInstance becomes `suspect`: new command dispatch pauses and the control plane probes transport fallback, container existence, Shim process, and Agent process without automatically failing the active WorkerRun. At 90 seconds without recovery, RuntimeInstance becomes `unhealthy` and may open RuntimeRecoveryIncident. Confirmed process exit or container loss may bypass the timers and become unhealthy immediately.

The fixed ACP/Codex stack has no safe ping/no-op. Agent unresponsiveness is evaluated separately from runtime heartbeat and requires process exit, stdio EOF, ACP connection close, or failure to obtain a terminal prompt response within a bounded cancellation grace. No model tokens or user-visible text for a period is not sufficient evidence because valid model inference and tool execution may be quiet. Before replacement, the control plane must terminate or isolate the old container, revoke its control and secret leases, and acquire an exclusive WorkingCopy mount lease; if write exclusivity cannot be proven, it escalates to `waiting_user`.

An `active_quiet` operation emits a non-terminal warning and does not trigger an early silence cancellation. The current AgentTurn's fixed 24-hour wall-clock deadline continues unchanged. Cancellation grace starts as a 30-second Docker-spike candidate and is not frozen until measured against model, tool, and background-process behavior.

Why: a heartbeat measures the controllable runtime, while progress measures work inside an Agent operation. Conflating them causes false recovery during long inference or tools, and replacing an unreachable-but-still-running container without storage fencing creates split-brain writes.

### Decision 22: Use one fixed 24-hour AgentTurn hard deadline

Each actual top-level ACP prompt receives one fixed `86400s` wall-clock hard deadline. The backend Worker Runtime owner persists server-observed `started_at` and `deadline_at` when its transaction accepts `StartAgentTurn` for dispatch; later Shim/ACP invocation time is observational Trace only. TaskSpec, Assistant, Delivery, Agent output and user actions cannot select, pause or extend the duration.

Approval or other `waiting_user` time, model inference, Tool execution, `active_quiet`, transport disconnection, confirmed platform outage and Runtime recovery all consume the same window. `steer` keeps the current deadline, `interrupt` terminates the Turn, and an authenticated queued user message starts a later independent Turn only after current terminality. Agent output, automatic retry and recovery cannot mint another top-level Turn.

At deadline Worker requests ACP cancellation and stops the active Tool/process under existing effect, evidence and generation fencing. AgentTurn becomes terminal `timed_out`. Confirmed cancellation leaves the WorkerRun resumable and moves Task-bound work to `waiting_user:agent_turn_timed_out`; uncertain cancellation must taint/fence or reconcile before another Turn starts. Tools inherit the Turn's remaining deadline and have no separate business timeout profile. Infrastructure transport/shutdown timeouts remain internal guards.

Deadline enforcement is dual. Backend ScheduledCommand remains the sole state, audit and fencing owner. Runtime Shim arms one non-resettable local monotonic watchdog for the same AgentTurn revision and Runtime generation so control-channel loss cannot disable cancellation. Both paths use one idempotent cancellation identity; reconnect converges watchdog evidence to backend terminal state. Shim may stop conservatively but cannot extend or independently rewrite `deadline_at`, and ACP prompt execution does not start until both enforcement paths are armed.

P0 start uses backend-precommitted one-way dispatch rather than a Ready/Permit round trip. One backend transaction persists `started_at/deadline_at/ScheduledCommand` and `StartAgentTurn` Outbox. The command binds exact Turn revision, Runtime generation, ChannelEpoch, deadline and cancellation identity. Shim validates freshness, derives a conservative remaining duration from backend deadline plus control-channel server-time uncertainty, arms the monotonic watchdog and only then invokes ACP. Stale, expired or unverifiable time fails closed; redelivery cannot reset the watchdog or invoke ACP twice. Dispatch latency and uncertainty margin intentionally consume the 24-hour window.

WorkerRun, Task and continuation have no active-time budget, cumulative time account or time-extension grant. LLMProxy rate, token and cost guards remain independent because elapsed time is not a reliable cost proxy.

Why: P0 only needs to stop one interaction from occupying a container forever. A single non-pausing deadline provides that safety without turning time into a user-facing accounting and authorization product.

### Decision 23: Escalate unconfirmed Tool cancellation by side-effect risk

AgentTurn hard deadline or user cancellation first invokes the Tool's declared cancellation mode and records a ToolCancellationOutcome. Confirmed cancellation lets the Turn reach its requested terminal state. An unconfirmed local process-group/cgroup stop taints RuntimeInstance, pauses new command dispatch, and opens one `tool_cancel_unconfirmed_local` RuntimeRecoveryIncident. An unconfirmed remote read-only or verifiably idempotent operation may be fenced before later work continues. An unconfirmed non-idempotent or unknown external effect enters `waiting_user`, exposes only supported reconcile/confirm/compensate/retry actions, and cannot retry automatically.

Every invocation is fenced by `tool_invocation_id` and runtime generation. Once terminal, late progress, success, or error is retained as `late_tool_result` Trace and cannot mutate ToolInvocation, AgentTurn, or WorkerRun state.

Why: rebuilding a container cannot undo an external write, while treating a runaway local process as merely a late response risks concurrent WorkingCopy mutation. Side-effect-aware escalation applies isolation where it helps and human reconciliation where automation cannot establish truth.

### Decision 24: Resolve Tool metadata through declarations, evidence, and platform policy

A versioned ToolCapabilityDeclaration from a built-in Tool, Skill, or MCP manifest declares effect, cancellation, idempotency, and reconciliation intent but is not authoritative authorization. The Tool Adapter validates and normalizes technical evidence and may raise risk or revoke unverifiable capabilities, but cannot independently lower declared risk. Capability/Skilling owns versioned ToolGovernancePolicy and produces the authoritative immutable ResolvedToolPolicy. TaskSpec, tenant policy, ApprovalGrant, and runtime constraints may only narrow that result; Worker validates and enforces the snapshot, while Agent/model text cannot modify it.

Missing, untrusted, conflicting, stale, or unverifiable metadata fails closed to `effect_class=unknown`, unverifiable cancellation becomes `unsupported`, and automatic retry remains disabled without Adapter-backed idempotency and reconciliation evidence. Trusted runtime evidence may escalate a single invocation but never lower risk or expand permission. Platform-owned overrides must be versioned and auditable.

Why: manifests know producer intent, Adapters know technical behavior, and platform policy owns organizational risk. Keeping these roles distinct prevents third-party metadata or model output from silently granting retry and external-write authority while preserving deterministic replay from a policy snapshot.

### Decision 25: Gate dispatch with structured readiness and three-question checkpoints

Assistant, Delivery, and API produce a versioned TaskReadinessAssessment across goal, deliverable/acceptance, scope/target, constraints, context/access, risk/approval, and execution policy. Each dimension is `known`, `assumed`, `blocking_unknown`, or `not_applicable` with evidence. Task Orchestration accepts TaskSpec only when no blocking unknown remains, the readiness snapshot matches TaskSpec assumptions/content, and deterministic validation succeeds.

TaskReadinessPolicy owns hard-blocker rules, a low-risk reversible default catalog, and a three-question checkpoint budget. Assistant asks one highest-priority blocker per clarification message. After three consecutive questions, it emits ClarificationCheckpoint with TaskDraft, assumptions, blockers, and evidence. Continuing or narrowing scope resets the next three-question budget; there is no conversation-wide hard maximum. Accepting assumptions may dispatch only when all hard blockers are resolved. Delivery/API may produce a ready assessment with zero clarification turns but cannot bypass the same policy and validator.

Why: confidence thresholds are unstable and mandatory forms create friction without proving semantic readiness. Structured evidence and periodic checkpoints make the decision auditable while preserving reasonable defaults for reversible details.

### Decision 26: Auto-publish only deterministic evidence-backed Memory allowlist

MemoryPublicationPolicy permits automatic publication only for `explicit_preference`, `verified_descriptive_fact`, `entity_alias`, and `ephemeral_project_state`. Candidates must be low risk, non-sensitive, conflict-free, scope/ACL preserving, and supported by a user explicit statement, authoritative source, or deterministic runtime verification. Ephemeral project state requires refresh and expiry; mutable facts use policy lifecycle rules.

Model inference, summaries, confidence, and repeated behavior may produce candidates and recommendations but cannot authorize publication. A preference that grants permissions, prescribes mandatory behavior, or changes production/external side effects is reclassified as behavioral memory, ProcedureTruth, or InstructionPolicy. Inferred preferences, behavioral/Skill/Instruction content, security and permission claims, sensitive data, production/external operations, cross-user or organization scope, failure-derived rules, subjective judgments, conflicts, low confidence, and scope expansion require human review or rejection.

Why: Evolution needs a low-friction path to become useful, but model confidence is not a stable authorization boundary. A narrow deterministic allowlist preserves self-evolution while keeping every automatic decision replayable, expirable, reversible, and visible to the user.

### Decision 27: Separate Memory Library from Review Inbox and restrict batch approval

MemoryLibrary is the user-facing projection for active, disabled, expired, and deleted MemoryTruth grouped by user, workspace, and project scope. ReviewInbox is the reviewer-facing projection for pending Memory, Procedure, Instruction, and Skill lanes. Library entries expose source, publication mode, freshness, usage, revision, and OpenViking projection state; Inbox entries expose diff, evidence, model recommendation, deterministic policy result, risks, conflicts, scope/ACL impact, reviewer requirement, and age.

Users govern user-scope Memory, while workspace/project content requires matching owner or reviewer authority. Batch reject/disable validates each item independently. Batch approve is limited to the same scope, MemoryPublicationPolicy revision, low-risk Memory type, reviewer authority, and items without sensitive, conflict, security, permission, or scope-expansion tags. Procedure, Instruction, Skill, cross-scope, security, and conflicting content is approved individually. Batch execution is idempotent, uses expected revisions, and returns per-item outcomes; partial success is explicit.

Edit and restore create new immutable ContentRevisions, disable removes the active projection, and delete writes a tombstone. All changes use Switchback governance APIs and Outbox projection rather than direct OpenViking CRUD. P0 has no blocking review SLA: age and digest reminders are projections, and pending content never enters ContextPack or blocks execution.

Why: active memories and pending high-risk candidates answer different user questions. Separating them prevents accidental activation, while narrowly constrained batching improves throughput without turning review into an unsafe checkbox exercise.

### Decision 28: Keep domain stream order separate from user timeline order

Each IntegrationEvent stream has one StreamOwner. The owning bounded context allocates StreamSequence in the same transaction as aggregate state and Outbox. EventCenter never allocates or rewrites domain sequence; it validates schema, deduplicates identical delivery, durably buffers gaps, raises integrity incidents for one stream/sequence reused by a different event id, routes retries/DLQ/replay, and drives projections. Other contexts send commands to the owner instead of writing its stream.

Switchback defines no global business sequence across streams. `correlation_id` and `causation_id` preserve cross-stream relationships. TimelineProjection independently allocates timeline epoch and append-only TimelineCursor when committing user-visible entries, using `epoch:cursor` for SSE resume and pagination. Normal replay reuses existing cursor mappings; a destructive rebuild that cannot preserve them increments epoch and requires client resync. `occurred_at` remains display/audit time. A late event receives a new tail cursor and `late=true`; previously published cursors are never inserted, moved, or rewritten.

P1 RocketMQ uses `stream_id` as FIFO message group, while IntegrationEvent payload sequence remains authoritative. PostgreSQL and RocketMQ profiles share duplicate, gap, conflict, DLQ, replay, causation, and TimelineCursor contract tests. Detailed diagrams, examples, transaction pseudocode, and consumer logic are maintained in `docs/architecture/event-ordering-explainer.md`.

Why: EventCenter cannot atomically assign a global number with every domain transaction, and timestamps cannot order distributed producers. Separating domain order from read-model order preserves correctness while giving the frontend a stable append-only cursor.

### Decision 29: Persist WorkingCopies on the host and finalize by fenced revision pointer

P0 stores each WorkerSession WorkingCopy and durable Agent state in host-managed WorkspaceStorage outside the disposable container layer. RuntimeInstance bind mounts only its own session WorkingCopy. Git uses per-session worktrees and immutable commit refs protected from GC; non-repository content uses immutable manifests and checksums. Copy-on-write and reflink are performance optimizations, not correctness assumptions.

WorkspaceApply prepares and validates a complete immutable StagingRevision outside WorkspaceWriteLease. Finalize acquires a short PostgreSQL lease row with a monotonic fencing token and uses one transaction to lock/revalidate expected active revision, token and staging digest, create WorkspaceRevision, advance the active revision pointer, record the Apply result, and write Outbox. The transaction does not copy directories, run tests, or call external services. Atomic multi-component Apply means one database pointer selects one complete manifest, not that unrelated filesystem directories rename simultaneously. Orphan staging is non-authoritative and cleanup is retryable; a committed-but-unacknowledged Apply is recovered by idempotency key.

Why: container layers cannot preserve interactive sessions across replacement, long staging under a global writer lease destroys concurrency, and filesystem operations cannot atomically commit multiple repositories. Durable isolated copies plus immutable staging and a fenced metadata pointer give recoverability and all-or-nothing authority without serializing expensive work.

### Decision 30: Use explicit Workspace membership and separate external authorization

Tenant is the P0 hard security boundary and every Workspace belongs to exactly one Tenant. Access requires an active TenantMembership and explicit WorkspaceMembership. Workspace roles are composable viewer, editor, reviewer, and owner; reviewer does not imply editor, at least one owner remains active, and Project/Team association does not silently grant access. Cross-Tenant Workspace identity sharing is forbidden.

Workspace roles authorize only Switchback actions. Private repository access and branch/PR operations separately require Capability/ResolvedToolPolicy, RepositoryAccessGrant, RepositoryCredentialLease and provider authorization; Feishu mutation, production access and other external effects use their corresponding ApprovalGrant/SecretLease. Worker acts on behalf of the initiating user through a short-lived ExecutionAuthorizationLease bound to tenant, workspace, WorkerSession, actions, membership revisions, and policy revisions; it never becomes a platform superuser.

Identity & Access owns one shared authorization API and revisioned decisions. Membership or policy revocation fences new commands, protected reads, Apply, Artifact actions, lease renewal, and secret issuance; the control plane revokes execution/secret leases and preserves WorkerSession evidence in an access-revoked waiting state. Tenant administrators do not implicitly read Workspace content; audited break-glass access remains a separate future design.

Why: Workspace collaboration and external credentials answer different security questions. Explicit membership keeps access explainable, while short delegated leases prevent long-lived Worker containers from retaining permissions after revocation.

### Decision 31: Tier authoritative Trace and build dashboards from incremental facts

ObjectStore JSONL and Artifact payloads remain authoritative evidence and PostgreSQL TraceManifest indexes immutable segments, checksums, sequence ranges, lifecycle state and consumer cursors. P0 DataLifecyclePolicy defaults to hot for 30 days, warm through 180 days, then cold indefinitely with no time-based physical deletion. Local storage may model tiers logically; S3-compatible adapters may later map them to real storage classes. Langfuse is independently retained and rebuildable.

Trace ingest and domain events emit normalized idempotent facts into TraceAnalyticsProjection for troubleshooting, Task/Worker/Tool/model/cost/Artifact metrics, and Evolution-effect dashboards. Dashboard requests never scan raw JSONL. Evolution resolves evidence through TraceManifest and may restore cold segments for replay.

P0 lifecycle auditing is intentionally lightweight because compliance workflow is not a core product concern. Archive, restore, quarantine, release and delete record actor, target, reason, before/after state, time, result and correlation id without mandatory tickets or four-eyes approval. Explicit deletion uses normal authorization plus one confirmation, idempotently removes payload/projection data, retains a minimal tombstone and non-sensitive aggregates, and marks affected Evolution evidence `source_deleted`.

Why: durable execution evidence is valuable mainly for debugging, self-evolution and measuring outcomes. Tiering controls cost, while incremental analytics prevents dashboards from becoming expensive ObjectStore queries; lightweight lifecycle records preserve operability without overbuilding an audit product.

### Decision 32: Use managed native CodePlatform CLI with A/C fallbacks

P0 uses managed B2 for GitHub. The Worker image includes pinned Git, GitHub CLI and approved GitHub Skills. A repository-scoped once or WorkerSession RepositoryAccessGrant delegates `repository.read`, `repository.write`, `pull_request.write` and/or `ci.read`; while the Grant is active, Agent may directly run any non-administrative native `git`/`gh` operation allowed by the short-lived provider Token without per-command approval.

This is an explicit trusted-delegate model: the short-lived access token is available to Agent and is not treated as secret from the sandbox. CredentialBroker retains GitHub App private keys, OAuth refresh tokens and long-lived PAT/root credentials, revalidates Membership, Grant revision, Session, runtime generation and policy, and issues or revokes RepositoryCredentialLease independently of the up-to-14-day WorkerSession. Git uses path-aware `git-credential-switchback`; `gh` uses a managed invocation wrapper without persistent `gh auth login`.

P0 profiles exclude administration, secrets, variables, workflow/Actions write, ruleset management, repository deletion and organization management. GitHub Ruleset/Branch Protection enforces protected/default branch, force-push and delete restrictions that cannot be expressed by a repository Token. Platform-known Tokens are deterministically removed before Trace, Langfuse, Artifact and user projection persistence even though generic content DLP is not a P0 blocker.

Every external operation produces CodePlatformOperation metadata. Push and PR response loss is reconciled against remote refs/heads or provider objects before any retry; unknown effects enter waiting_user instead of blind replay. GitLab P1 adds managed `glab` and provider-specific permission profiles under the same Grant/Broker/Operation contracts.

Option A, full control-plane execution, remains the fallback when policy forbids repository Token use in the Agent sandbox or operation-level compliance is mandatory. Option C, direct read plus controlled write, remains the fallback when native read workflows are needed but provider write permissions/rules cannot safely contain Agent writes. Switching profiles does not change the published authorization and result contracts.

Why: native CLI gives a real Coding Agent the Git/Provider flexibility users expect, while the Grant, short credential lifecycle, provider rules and result reconciliation constrain authority. Recording A/C triggers preserves stronger containment paths if B2 proves unacceptable in a provider or Tenant.

### Decision 33: Persist TaskSaga and durable wakeups in PostgreSQL (partially superseded by Decision 50)

P0 uses an explicit PostgreSQL TaskSaga state machine rather than Temporal. Each valid command transaction locks and revalidates the expected Saga revision and idempotency key, updates Task/TaskSaga, appends immutable SagaTransition, creates/cancels/obsoletes SagaWakeup rows, and appends owner-stream Outbox records. Network, container, model, ObjectStore and external-provider calls never run inside this transaction.

SagaWakeup represents a durable request to re-evaluate retry, deadline, recovery, reminder or reconciliation after `due_at`; it does not represent the side effect. Pollers claim due rows with `FOR UPDATE SKIP LOCKED`, a short lease and monotonic fencing token, then submit an idempotent application command outside the claim transaction. Command handling rechecks wakeup status, fencing token and expected Saga revision. Expired tokens, stale revisions and cancelled, obsolete or already delivered wakeups are auditable no-ops.

This design converges database commit/response loss, Outbox relay crashes, poller lease expiry and Worker ACK loss without a second Transition or WorkerRun. Unknown external effects use owner reconciliation or `waiting_user`; retry wakeups never blindly replay them. Entering `waiting_user` persists the reason and WorkerSession idle-clock effect atomically, while any active AgentTurn hard deadline and WorkerSession absolute lifetime continue unchanged.

Temporal remains a future re-evaluation when a managed company platform exists, Saga/timer complexity or operational load outgrows the PostgreSQL implementation, or wakeup contention/SLO cannot be met economically. P0 does not introduce a generic DurableWorkflowEngine interface because replay-based Workflows and database state machines have materially different ownership and migration semantics; stable business commands/events and narrow persistence seams provide the current change boundary.

Why: Switchback already requires PostgreSQL transactional state, Outbox, idempotency and owner-side reconciliation. Keeping orchestration in the same authority minimizes P0 infrastructure and split-brain state while retaining crash recovery. The cost is that Switchback must implement and test wakeup leasing, fencing and operational backlog itself.

### Decision 34: Use one terminal TaskSaga per Task (superseded by Decision 50 for P0)

Each Task owns exactly one TaskSaga. The Saga coordinates the Task until a terminal state and is never reopened. A business retry before Task termination remains in that Saga and creates a new TaskAttempt and WorkerRun; runtime recovery remains in the same TaskAttempt and WorkerRun and only replaces RuntimeInstance generation.

WorkerSession lifecycle is independent and may outlive a Task, host WorkerRuns for multiple Tasks, and host standalone direct interactions. A direct AgentTurn after Task termination starts or joins a WorkerRun without `task_id`; it may record `origin_task_id` for lineage but cannot mutate the terminal Task/Saga. A newly tracked follow-up deliverable creates a new Task and TaskSaga and may reuse the same WorkerSession and Workspace. Existing standalone runs are not retroactively attached to that Task.

Late events after Task termination are retained as Trace/audit evidence or integrity incidents and cannot reopen the Saga.

Why: Task is the stable business unit for acceptance, retry, cancellation and compensation, while WorkerSession is the stable user interaction container. One Saga per Session merges unrelated goals; one Saga per Run cannot coordinate retries and waiting states across runs.

### Decision 35: Require explicit creation of follow-up Tasks (partially superseded by Decision 50)

Worker Agent may suggest a tracked follow-up and create or revise a non-authoritative FollowUpTaskProposal, but it cannot create a Task from inferred intent, generated text, conversation growth, or an unconfirmed proposal. The canonical operation creates a new follow-up Task; it never promotes or retroactively reassigns a standalone WorkerRun.

Explicit intent is an authenticated user confirmation, structured create command, unambiguous natural-language create instruction, or an Assistant/Delivery/API submission already governed by TaskSpec readiness. Gateway or the channel adapter records intent evidence independently from model output. Task Orchestration revalidates actor and Workspace authorization, proposal revision/hash, readiness and current policy, then atomically creates Task, its unique TaskSaga and Outbox under one idempotency key.

A valid preferred WorkerSession may be reused without interrupting an active Run. An expired Session may be replaced within the same authorized Workspace; revoked Workspace access fails closed. The source standalone WorkerRun remains unchanged and is linked only by lineage references.

Why: this preserves direct Coding Agent flexibility while making workflow creation deterministic and auditable. Model confidence is useful for recommendations but is not user authorization.

### Decision 36: Use Task and Worker as dual primary frontend views (superseded by Decision 50 for P0)

Frontend uses Tasks for tracked delivery and Workers for direct interactive execution. A product Worker view is keyed by `worker_session_id` and represents WorkerSession, not a backend Worker service instance. TaskDetail shows TaskSpec, acceptance, business timeline, Attempts/Runs, actions and Artifacts; WorkerDetail shows chat, projected activity, approvals, runtime health, Artifacts and Task/standalone lineage. Their lifecycles remain independent and cross-linked.

Home/ActionInbox is a cross-resource actionable projection. Workspace is a durable context/resource view rather than the only root. Artifact uses one canonical detail identity shared by Task, Worker and Workspace summaries. Memory remains an independent governance area with MemoryLibrary and ReviewInbox; Conversation remains Assistant interaction history and cannot own all Delivery-created Tasks or WorkerSessions.

Frontier exposes page-optimized ActionInbox, Task, WorkerSession, Workspace, Artifact and Memory views. Frontend renders those projections and submits commands through Gateway; it does not reconstruct authoritative state from raw events/Trace or duplicate owner state machines.

Why: Task and WorkerSession have different identities, lifetimes and user jobs. A dual-primary structure preserves both business delivery scanning and Coding Agent continuity while canonical cross-links prevent duplicated state.

### Decision 37: Synchronize Frontier views with snapshot and typed SSE delta

P0 separates browser communication into Gateway HTTP commands, Frontier ProjectionSnapshot queries, typed SSE ProjectionDelta/control events, and resumable Artifact transfer. Each page ViewProjection has independent epoch, monotonic cursor and projection version. Snapshot state and cursor are committed consistently; Frontend connects with `Last-Event-ID=epoch:cursor` and replays only later deltas, avoiding the initial query/subscription race.

Each delta has a versioned semantic type plus exact base/result projection versions. Frontend applies only compatible, contiguous deltas and never consumes raw domain events, Trace, broker payloads, CDC or generic JSON Patch. Frontier retains a policy-bounded replay window. Old epoch, evicted cursor, gap or unreachable base returns `resync_required`; schema incompatibility returns `client_upgrade_required`; authorization revocation clears and closes the protected view.

Gateway command response is only accepted plus command id. Frontend pending state is ephemeral and converges when a causally related delta or refreshed snapshot arrives. SSE uses same-origin secure credentials or equivalent BFF and never puts bearer tokens in URLs.

GraphQL is not a P0 dependency. It may later expose the same read projections if client query-shape diversity justifies schema/resolver/cache governance, but cannot replace command idempotency, SSE replay/resync or owner context authority. Fine-grained Agent chunks use the separately defined durable/transient display lanes.

Why: HTTP/SSE matches the accepted one-way realtime flow and already-defined timeline cursor semantics with less P0 infrastructure. A full snapshot provides a deterministic recovery point while typed deltas keep browser logic semantic and reviewable.

### Decision 38: Separate durable display checkpoints from transient Agent chunks

P0 uses two Frontend display lanes. DisplayCheckpoint is a complete materialized user-visible state committed into ViewProjection and replayed through `view.stream_checkpointed` ProjectionDelta, advancing projection version and ViewCursor. TransientDisplayDelta streams low-latency text, user-visible reasoning summary, partial Tool arguments and progress between checkpoints without advancing durable cursor/version or promising exact replay.

Transient browser durability is independent from Trace durability. Worker continues to record high-fidelity ACP/Agent source frames in authoritative Trace JSONL. Each display stream has a durable start checkpoint, policy-bounded periodic checkpoints, semantic checkpoints for Tool/Approval/waiting_user/Artifact/error/cancel/timeout boundaries, and a complete terminal checkpoint. Time/byte thresholds are versioned DisplayDurabilityPolicy values calibrated by a Codex/ACP load spike.

Transient delta binds stream, part, base checkpoint version and stream-local sequence. Duplicate sequence is ignored; a gap stops only that part's concatenation until a newer checkpoint arrives. Transient SSE events do not set a durable event id, so EventSource `Last-Event-ID` remains the last ViewCursor. Backpressure may coalesce adjacent chunks or drop progress pulses but cannot drop durable semantic state.

Only user-visible reasoning summaries enter Display; hidden chain-of-thought is not a Frontend contract.

Why: this preserves natural Coding Agent streaming while making reconnect correctness explicit at checkpoints and avoiding one PostgreSQL projection write per token.

### Decision 39: Own a stable DisplayPart union behind protocol adapters

Switchback owns `frontier.display_part.v1` as a materialized union with `text`, `reasoning_summary`, `tool`, `activity`, `approval`, `artifact`, `source` and `error`. Every part has stable Switchback identity, monotonic revision, stable ordinal, lifecycle, criticality, permission-filtered visibility, provenance and canonical Gateway command references. Display payload is read state: it cannot contain arbitrary executable components, ungoverned paths, credentials or authority grants.

ACP/Codex, future AG-UI and Vercel AI SDK inputs pass through protocol adapters. Adapters map provider-local identity/lifecycle to canonical Tool/Approval/Artifact and part identities while preserving source evidence in Trace/provenance. Unknown optional progress may become a restricted non-critical activity; unknown Tool/approval/permission/security semantics fail closed. Browser RendererRegistry uses schema/type keys: unknown non-critical parts get a sanitized no-action fallback, while unknown critical parts require client upgrade.

Additive optional fields remain compatible in v1. New required fields, changed meaning, visibility expansion or new permission/security behavior require a new schema version. `available_actions` only names Gateway commands and targets; Gateway and the owner context always revalidate actor, expected revision, policy and idempotency.

Why: Switchback needs one stable user-visible language across Worker/Task pages and Delivery without making a frontend SDK or Agent protocol the owner of platform authority semantics.

### Decision 40: Use a React/Vite SPA with explicit browser state owners

P0 Frontend is a React + TypeScript SPA built with Vite and served from the same browser origin as Go Gateway/Frontier. Node.js is a development/build dependency, not a required production application runtime. React Router owns route/resource/filter identity. TanStack Query owns HTTP request, cache and refetch lifecycle. A dedicated ViewProjectionStore owns active ClientViewReplica epoch/cursor/version, typed SSE reducers, transient overlays and PendingCommand convergence.

Snapshot success atomically replaces one affected replica. ProjectionDelta advances it only after schema/epoch/base/result checks; TransientDisplayDelta never advances durable state. Gateway `accepted + command_id` creates a non-authoritative PendingCommand that only projection, command-status or replacement-snapshot evidence resolves. Components neither consume raw SSE independently nor interpret timeout/disconnect as business terminal state.

assistant-ui may render Worker Chat through ExternalStore conversion and supported callbacks, but Switchback retains WorkerSession/message/part identities, persistence, Tool/Approval authority, queue/interrupt/steer commands and replay. Same-origin Secure/HttpOnly session credentials, Gateway CSRF/idempotency checks, API route exclusion from SPA fallback, no client-side secrets and protocol-compatible static rollout remain mandatory.

Why: Switchback is an internal Go-backed work application without a P0 SSR/SEO need. A small owned projection store isolates exact recovery semantics while React/Vite and optional chat primitives remain replaceable presentation tools.

### Decision 41: Use a channel-specific DeliveryCardProjection

Delivery renders Feishu through `DeliveryCardProjection v1`, derived from owner events, authorized public projection facts and durable Display summaries rather than Web View JSON, React output or raw Trace. P0 card kinds are task progress, waiting user, approval and result. CardInstance owns only provider card/message mapping, monotonic render revision, send/update/reconciliation/expiry state.

Inline action is a conservative subset of the current owner-authorized commands: exact ready-TaskDraft creation/adoption, bounded choice/text input, once/session approval, cancel and safe retry. `create_task` is limited to initiator + `waiting_user` + exact `task_draft` and creates intent rather than claiming Task success; `adopt_task_draft` only forks a new actor-owned draft. Full Artifact review, patch apply, branch/PR/production writes, Skill/Tool operations and unrestricted Worker chat deep-link to Web. Callback verifies provider signature, actor/binding/membership, opaque action, render revision, expiry and duplicate identity, then submits one idempotent Gateway command. Fast callback acceptance never means business success; owner evidence advances the card projection.

Provider response deadline, update/interaction windows, card versions, limits and reconciliation support live in a versioned DeliveryProviderCapability profile. Stale, expired, unsupported or complex actions fail closed or degrade to read-only summary plus authorized Web link. P0 coalesces semantic progress and does not mirror every Agent token into a card.

Why: a collaboration card should be useful inside the channel but cannot safely or ergonomically reproduce the full Coding Agent/Web runtime. A separate read model preserves both channel usability and owner-context authority.

### Decision 42: Default a channel thread to one Assistant Conversation

An active `ChannelThreadBinding v1` maps provider/tenant/chat/thread scope to one primary Assistant Conversation. An ordinary inbound message without a validated explicit target always becomes an Assistant Turn in that Conversation. Creating or completing Tasks, WorkerRuns, approvals or cards does not change the default route.

Task, WorkerSession, ApprovalRequest and Artifact actions require `InteractionTargetRef v1` from a current card action, validated structured command or registered immutable interaction context. Delivery records actor, provider message/action identity, binding revision, optional target evidence and idempotency in `InboundInteractionEnvelope v1`. It does not infer a mutable current focus from recent activity, latest card, provider reply ancestry, related refs or model output.

Every channel actor is mapped and authorized independently. Provider topic limitations may replace thread identity with a versioned chat/root-message fallback, but cannot change Assistant-default semantics. Binding revocation fences channel use without closing related Conversation, Task or WorkerSession lifecycles.

Why: one topic may contain parallel Tasks and long-lived WorkerSessions, while Feishu topic reply ancestry does not encode a precise execution target. A stable conversational default plus explicit mutations is recoverable, auditable and provider-independent.

### Decision 43: Resolve Workspace explicitly before protected context or execution

An Assistant Conversation may remain Workspace-unresolved for tenant/user-scoped brainstorming and clarification. Before loading Workspace-scoped Truth/Inspiration/Repository/Trace/Artifact, dispatching TaskSpec, or creating/reusing WorkerSession, the user must explicitly confirm an authorized Workspace through Web selection, a current card/structured command, or a separately completed idempotent CreateWorkspace flow.

Interaction owns `WorkspaceContextResolution v1`; ChannelThreadBinding references its optional default Workspace and revision. User-global default, a single candidate, URLs, related refs, recent activity and model inference may only rank or preselect candidates. They do not create execution intent. Selection rechecks TenantMembership and WorkspaceMembership but is not a grant; each protected operation reauthorizes current policy.

Accepted TaskSpec requires exact `workspace_id` plus resolution and observed Membership evidence. Task Orchestration validates and snapshots those values atomically before Task/Saga/Outbox creation and never creates or infers a missing Workspace. Rebinding affects future untargeted Assistant context and Task drafts only. Existing resources retain their own Workspace, and explicit targets use that Workspace without changing the default. Revocation makes the resolution stale and cannot trigger fallback.

Why: a conversational default is useful for discovery, but an execution target is an authorization and ownership decision. Keeping them separate prevents a convenient hint or model guess from selecting where code, context and artifacts live.

### Decision 44: Require an explicit WorkspaceChannelShareGrant for group disclosure

Workspace execution authority and provider-group disclosure authority are independent. Identity & Access owns `WorkspaceChannelShareGrant v1`: in P0, a current Workspace owner explicitly binds one Workspace to one verified provider group audience, optional channel-binding delivery scope and the policy-bounded `workspace_summary` profile. The grant does not create Membership, Web access, execution authority, approval authority or card-action authority.

Each Workspace-derived shared card send/update requires `ChannelDisclosureDecision v1` over the exact grant/policy revision, source projection classifications and a fresh `ProviderChannelSecuritySnapshot`. A field is visible only when the source owner classified it `channel_shareable` and the profile/platform allowlist accepts it. P0 may expose bounded title, coarse status/stage, safe summary and Artifact type/count. Raw Trace, private prompts, hidden reasoning, secrets, Tool stdout, internal error details, full reports, patch/diff and unrestricted Artifact content remain Web-only.

Without valid rich authority, Delivery renders a non-resource-identifying `minimal_notice` and canonical Web link; Web authorizes each opener and the link contains no bearer authority. Actor-private interaction uses a provider per-user ephemeral card when available or degrades to Web. Shared-card callbacks always reauthorize the clicked actor.

Known audience expansion, group external/public transition, bot inspection loss, stale/unknown provider state, owner access loss, grant revocation/expiry or stale policy suspends rich disclosure. Provider events trigger invalidation, while reconciliation and render-time freshness checks cover event loss. Delivery best-effort updates/recalls old bot messages but records cleanup outcome and residual exposure because already delivered content cannot be transactionally erased.

Why: sender-only authorization leaks private Workspace facts to a group, while dynamically checking every current member is race-prone and does not address later members or history. Explicit owner consent plus bounded source/profile classification makes the audience decision recoverable and auditable without pretending that the channel itself is a Workspace role.

### Decision 45: Rollover Conversation identity when leaving a resolved Workspace

The first explicit unresolved-to-W1 selection remains in the current Assistant Conversation. After a Conversation has resolved Workspace history, W1-to-W2 rebind, W1-to-cleared and W1 access revocation create a successor Conversation under the same provider topic. Interaction marks the source `superseded` and read-only, creates target WorkspaceContextResolution and immutable ConversationHandoff, advances ChannelThreadBinding to the successor and writes Outbox in one transaction. Switching back to W1 creates a later generation rather than reopening the old W1 Conversation.

Default handoff contains only typed Workspace-neutral goal, constraint, assumption and blocker facts. It excludes raw Turns, prior summaries, Workspace Truth, repository/file details, Task/Worker/Trace/Artifact references and Tool/model output. Protected cross-Workspace carry-over is a separate explicit action with current source-read permission, target-context permission, classification, exact evidence and authenticated intent; model summary text is not authority.

Ordinary-message acceptance and rollover serialize on binding/source revision. A message committed before rollover belongs to the predecessor; one observing the new revision belongs to the successor. Stale envelopes are not silently moved. Idempotent retries return the original successor, and competing rebinds cannot create extra generations.

Task, WorkerSession, Trace, Artifact and WorkspaceRevision ownership never migrates. ContextCompiler, Turn archives, search/export and Evolution preserve Conversation generation, Workspace scope snapshot, resolution revision and lineage. Same provider topic or lineage never authorizes a cross-Workspace context/evidence merge. WorkspaceChannelShareGrant remains exact to the subject Workspace and is not inherited by a successor in another Workspace.

Why: the security boundary exists in historical content, not just a current Workspace id. A successor identity concentrates the invariant in one transition instead of requiring every future prompt, archive, search and Evolution consumer to implement perfect historical filtering, while preserving one user-facing Feishu topic.

### Decision 46: Share one channel Conversation with explicit controllers

P0 Feishu group topics use one `scope_type=channel` shared Assistant Conversation for all mapped participants. Every Turn and command retains the actual actor. The system does not create a hidden per-user Conversation for each sender inside the same public topic.

Interaction owns revisioned `ConversationParticipant` roles. A `participant` may submit ordinary Assistant Turns. A `controller` may additionally select or clear the shared Workspace context, trigger explicit rollover, close the channel Conversation and delegate controller status. The binding creator becomes the first controller only after provider mapping, Tenant and channel-policy checks. Provider membership, WorkspaceMembership and WorkspaceRole do not automatically grant controller.

Controller is coordination authority only. Every Workspace selection/rollover, Task, WorkerSession, Approval, Artifact, external-provider action and group disclosure is reauthorized for the actual actor and exact target. Controller delegation/revocation uses expected revision and cannot explicitly remove the last active controller. If access revocation invalidates every controller, the Conversation becomes `control_orphaned`, fences control mutations and requires an explicit policy-authorized channel-administrator recovery; ordinary participants are not auto-promoted.

Conversation rollover projects only source participants/controllers that remain eligible under current policy into the successor, preserving source role revision/evidence and never adding or promoting a controller. A successor without an eligible controller starts `control_orphaned`.

P0 keeps actor-private approvals/details and unrestricted Worker interaction in provider ephemeral UI or individually authorized Web routes. Per-user private sidecar Conversations remain a later extension.

Why: a single visible group history makes shared intent and Workspace context understandable, while an explicit narrow controller role avoids turning volatile provider membership into broad data or execution authority.

### Decision 47: Separate Task readiness from explicit creation intent in shared channels

`TaskReadinessAssessment` determines whether an exact TaskSpec is executable. `ExplicitTaskCreationIntent` proves that an authenticated actor requests creation of that tracked Task now. P0 requires both gates plus current authorization; neither gate implies the other.

In a channel-scoped shared Conversation, every ordinary message first becomes an Assistant Turn even when it contains enough information for a ready TaskDraft. Direct creation is limited to a validated `/task` or equivalent structured command, a create-task form/button, or a registered confirmation action bound to one exact ready TaskDraft. Free text such as an unbound "confirm" remains an ordinary Assistant message. A complete structured command may produce a zero-question ready assessment and proceed directly; an incomplete command enters clarification and requires confirmation of the final materially changed ready draft.

In user-scoped P2P/private interaction and authenticated APIs, versioned `TaskCreationTriggerPolicy` may accept an unambiguous create/execute request as explicit intent. Generic consultation and ambiguous requests still route to Assistant.

Intent records actual actor, scope/source evidence, channel binding and Conversation generation when applicable, exact TaskDraft revision, TaskSpec digest, readiness assessment, Workspace resolution, trigger-policy revision, expiry and idempotency key. Any bound revision/digest change makes the intent stale; rollover never carries confirmation into a successor. Task Orchestration revalidates all evidence before atomically creating Task, its unique TaskSaga and Outbox. Duplicate command/callback or response loss returns the original Task identity and does not change the primary Conversation, default Workspace or current focus.

A pre-Task ready-draft confirmation reuses the existing `waiting_user` card kind with `subject_ref=task_draft`; it does not add a fifth lifecycle card. Controller status grants no Task creation authority. Decision 48 binds confirmation to the draft initiator and requires explicit fork-based adoption by another participant.

Why: ready text can be hypothetical, quoted or exploratory, especially in a shared group. A precise authenticated creation surface prevents accidental cost-bearing execution while retaining a zero-question fast path for structured commands and private/API requests.

### Decision 48: Bind shared TaskDraft confirmation to its initiator and fork on adoption

Each TaskDraft lineage has an immutable `initiator_user_id` derived from the authenticated Turn, structured command, form or API request that created it. Shared Conversation contributions may advance its revision but do not transfer confirmation authority. Only the initiator may confirm that exact ready draft; intent actor, draft initiator and TaskSpec user must match. Conversation controller and Workspace roles do not change this rule.

A different participant must issue authenticated `AdoptTaskDraft` through a registered bot action, structured command or Web action. Adoption creates a new TaskDraft identity with the adopter as initiator and an immutable `derived_from_task_draft_ref`; it never mutates, consumes or transfers the source. The new draft revalidates current Conversation/binding generation, source visibility, Workspace access and every context reference, then generates a new actor-specific readiness assessment and canonical digest. Source creation intent, approvals, secret/repository grants, private evidence and actor-specific assumptions do not carry over. Adoption itself never creates Task, so the adopter must confirm the new ready revision.

Same-actor adoption of the same source revision is idempotent. Different actors receive different lineages even when canonical TaskSpec digests match; equality may produce a duplicate warning but never automatic merge. Consumed/cancelled source, rollover, Workspace mismatch, inaccessible context or uninterpretable schema/policy fails closed. A draft whose readiness or original initiator authorization is stale may be forked only when its exact payload remains readable in the current generation/scope, after which all adopter checks run again.

Why: initiator-only confirmation keeps execution cost and audit ownership explicit, while immutable fork-based adoption preserves continuity without impersonation or evidence transfer.

### Decision 49: Govern Task lifecycle commands per action (superseded for P0 by Decision 50)

This decision originally explored a generic TaskActionPolicy and delegated TaskActionGrant. Decision 50 replaces that P0 surface with fixed initiator rules and defers delegation. Decision 22 also removes time-budget actions entirely. The retained principle is that visibility, Workspace collaboration and shared-card buttons are not command authority; callbacks always reauthorize the real actor, and ApprovalGrant only authorizes a protected Tool or external operation.

### Decision 50: Make WorkerSession primary and Task lightweight in P0

P0 optimizes the shortest useful product loop: explicit request -> real containerized Coding Agent -> visible progress -> report/patch review -> continued interaction with the same Agent. `WorkerSession` is therefore the primary work surface and durable interaction identity. `Task` becomes a lightweight tracked-delivery envelope containing immutable TaskSpec snapshot, initiator, Workspace/WorkerSession/current WorkerRun refs, result refs, `acceptance_mode`, status and revision.

Task states are `queued`, `running`, `waiting_user`, `delivered`, `completed`, `failed` and `cancelled`. `acceptance_mode=auto|manual`; report-only work with deterministic evidence may auto-complete, while patch defaults to manual. Manual delivery enters `delivered`; initiator acceptance completes it, while feedback creates a new WorkerRun and returns the same Task to `running`. P0 does not add TaskAttempt. Runtime recovery remains in the same WorkerRun and only changes RuntimeInstance generation.

After a terminal Task, direct Worker chat creates or joins a standalone WorkerRun and may retain origin lineage. Creating a new tracked Task still requires explicit authenticated intent, but no first-class FollowUpTaskProposal aggregate is required. A suggestion is UI text until an authorized create surface generates ExplicitTaskCreationIntent.

P0 action rules are static: TaskInitiator may continue, accept, rework, cancel and reassign a still-queued Task to another authorized Session tail; current Workspace owner has emergency cancel only. TaskActionGrant and reviewer delegation are P1. Task state, TaskTransition and Outbox commit in one PostgreSQL transaction. A generic ScheduledCommand table retains leased polling, fencing, idempotency and unknown-effect reconciliation without making TaskSaga a second public authority.

Frontend uses WorkerDetail as the primary chat/execution/review surface. TaskList is a compact tracking index and TaskSummary is a drawer or summary page that deep-links to Worker. Home only needs recent Workers, waiting_user/approval and recent deliveries in P0; rich ActionInbox and Task/Worker feature parity are deferred.

This decision supersedes the P0 product/domain portions of Decisions 33-36 and 49 while preserving their explicit-intent, PostgreSQL reliability and role-separation reasoning as implementation guidance or P1 options. It does not remove container isolation, ACP, persistent WorkingCopy/Agent state, Runtime recovery, Trace/Artifact, Outbox, idempotency, authorization, secret fencing or execution limits.

Why: the earlier model created parallel lifecycle and UI concepts before usage demonstrated a need for them. Keeping one primary interaction object and one small delivery envelope reduces code, state synchronization and user navigation cost while preserving the difficult safety and runtime foundations.

### Decision 51: Serialize top-level Task and WorkerRun per WorkerSession

P0 treats WorkerSession as one top-level execution slot. It may reference at most one bound Task and one active WorkerRun at a time. A Task may target the Session while still `queued`, but only an idempotent Worker Runtime acceptance binds it and starts the Run. If another Task/Run holds the slot, Worker Runtime returns stable `session_busy`; Task Orchestration keeps the Task queued or the user explicitly chooses another WorkerSession for parallel work. New work never implicitly interrupts the current Run.

The Task normally remains bound while `running`, `waiting_user` or `delivered`, preserving direct acceptance/rework against the same Agent and WorkingCopy. Terminal `completed`, `failed` or `cancelled` releases the binding. The only P0 non-terminal exception is Decision 55 absolute expiry: after Worker Runtime proves the old runtime lost write authority and terminalizes the Session, Task Orchestration clears the current binding and preserves it in append-only lineage. A standalone WorkerRun may start only with no bound Task and no active Run. AgentTurn `queue` is a message operation against the one active Run and is distinct from the Session Task scheduling queue. Adapter-visible subagents may execute concurrently inside one active top-level Run but share the parent Turn deadline, permission, WorkingCopy, Trace and Artifact lineage.

Worker Runtime owns the Session slot and session revision; Task Orchestration owns Task queue status. Slot acquire/release commands use expected revision, runtime generation, fencing and idempotency. EventCenter carries outcomes but does not assign work.

P0 uses one stable FIFO per WorkerSession. Task Orchestration assigns a monotonic `session_queue_sequence` when enqueue commits, and duplicate enqueue returns the original sequence. Only the lowest legal sequence receives a short-lived single-use SessionDispatchPermit. A standalone top-level prompt receives a permit only when the Session queue is empty, so it cannot overtake an earlier Task. The queue allows cancellation and explicit initiator re-assignment to another authorized Session's tail, but no priority scheduling, drag reorder or preemption; display priority never changes dispatch order. Slot release triggers dispatch and periodic reconciliation recovers lost notifications, expired permits or unknown responses.

Why: allowing concurrent top-level work in one persistent Agent session would make prompt control, interrupt, deadline, approval, Artifact and WorkingCopy ownership ambiguous. Explicit additional WorkerSessions already provide isolated parallelism with a clear capacity cost.

### Decision 52: Require exact intent for WorkerSession reuse and snapshot inherited start state

An existing WorkerSession is reused only when authenticated interaction evidence targets an exact `worker_session_id`. Worker Detail actions, registered card/commands and exact API fields may create `WorkerSessionTargetIntent`; same Workspace, recent/only/idle Session, related refs and model inference may not. A new Task without that intent remains queued while the platform allocates a new Session, and it enters no existing SessionTaskQueue.

Reuse resolves to `inherit_current` and preserves the Session's current WorkingCopy, persisted Agent state/context, AgentSessions and uncommitted changes. The platform never silently resets, cleans, stashes or checks out that Session. A clean or exact-base requirement uses a new Session materialized from the exact WorkspaceRevision/checkpoint, or remains queued/blocked when allocation is unavailable. Before any Agent prompt, Tool call or WorkingCopy mutation, Worker Runtime persists an immutable WorkerRunStartSnapshot and links it to Run, Trace, Artifact and ChangeSet lineage.

Why: automatic reuse leaks state between logically separate Tasks, while in-place cleanup destroys the continuity users expect when they explicitly choose a Worker. Exact intent makes continuity a deliberate choice and the start snapshot makes inherited state diagnosable.

### Decision 53: Count continuity-holding Sessions and queue new-Session requests at capacity

P0 defines WorkerSessionContinuityQuota per `(tenant_id, user_id)`, default 10. `allocating`, `interactive`, `running`, `recovering`, `waiting_user`, `idle`, and unreconciled `lost` Sessions consume one slot. A queued Task or pending continuation without a Session consumes none. Only a committed `closed` or `expired` terminal transition releases the slot; runtime and storage cleanup may finish asynchronously.

At capacity, each valid `WorkerSessionAllocationRequest` receives a monotonic Tenant/User `session_allocation_sequence` and enters SessionAllocationQueue. A Task stays `queued` with no `worker_session_id`; a post-terminal continuation stays pending allocation. Neither enters a SessionTaskQueue. Capacity pressure never selects, resets, closes or evicts a Session. The user may cancel, close an eligible Session, or explicitly reuse an exact live Session under Decision 52.

Worker Runtime's WorkerSessionAllocator owns allocation sequence/FIFO and revalidates the earliest legal request. Task Orchestration submits Task-owned requests; the continuation application submits source/checkpoint-bound requests. Worker Runtime owns quota revision and atomically reserves one slot while creating an `allocating` WorkerSession. Terminal Session plus quota release also commit atomically. Duplicate commands, response loss, stale allocations and lost release events converge through idempotency and reconciliation.

Why: the scarce P0 resource is the promise of resumable Agent/WorkingCopy continuity, not only current CPU. Counting waiting and idle Sessions prevents quota bypass; not counting unassigned Tasks lets users submit work without fabricating a Worker identity.

### Decision 54: Keep terminal Session identities immutable and continue by derivation

`closed` and `expired` WorkerSessions never reopen. They remain permission-filtered read-only records for Timeline, Trace, Artifact, Task/Run lineage and latest proven durable WorkingCopy checkpoint. Runtime generation replacement remains a recovery mechanism inside a live Session and cannot cross a terminal boundary.

An authenticated `CreateWorkerSessionContinuation` command binds exact source Session/revision and exact durable checkpoint/digest. It revalidates Tenant, WorkspaceMembership, policy and checkpoint readability, then submits a `request_kind=session_continuation` allocation request under Decision 53. Success creates a new WorkerSession identity, isolated WorkingCopy and new AgentSession, records source lineage, and starts new 48-hour idle and 14-day absolute clocks.

The derived Session does not inherit old Runtime/Channel generation, queue or dispatch position, ApprovalGrant, RepositoryAccessGrant, SecretLease or any Session-scoped authority. Explicit ContextPack/Artifact refs are permission-filtered again. Mutable LiveWorkerState in WorkspaceStorage supports same-Session Runtime generation recovery and is not a continuation checkpoint. Immutable continuation checkpoints are attempted best-effort at AgentTurn terminal, entry to `waiting_user`, stage-result publication and graceful close. P0 promises no periodic interval, byte threshold or numeric RPO; forced host/storage failure offers only the latest proven checkpoint with capture time, digest and coverage warning. Duplicate/response-loss requests return the same derived identity, while cancellation or materialization failure is fenced without an interactive orphan.

Why: reopening would make absolute lifetime renewable and attach fresh authority to a terminal audit identity. Derivation preserves useful work and provenance while making capacity, authorization, Agent context and lifecycle boundaries explicit.

### Decision 55: Preserve Task identity across WorkerSession absolute expiry

Worker Runtime owns the absolute-expiry boundary. Before publishing a terminal result it fences new commands and leases, performs bounded Agent/Tool cancellation and drain, revokes old control and secret leases, and proves the old runtime no longer holds WorkingCopy write authority. If that proof fails, the Session remains `lost`, continues consuming continuity quota, and cannot be used as a writable continuation source.

After successful fencing, an active WorkerRun terminates as `timed_out:worker_session_absolute_expired`; it is never resumed or migrated. Worker Runtime publishes one idempotent `WorkerSessionExpiryOutcome` containing exact Session/Run revisions, terminal checkpoint, partial/complete Artifact refs, cancellation and fencing evidence, external-effect state and any bound Task. Late runtime or Tool results remain Trace evidence only.

Task Orchestration applies that outcome under expected revision. Terminal Tasks are unchanged. `queued`, `running` and `waiting_user` Tasks enter or remain `waiting_user:worker_session_expired`; `delivered` Tasks remain delivered and complete results remain acceptable. The current Session/Run refs clear only after an append-only `TaskSessionLineageEntry` preserves the old binding. Partial results remain reviewable but cannot satisfy automatic acceptance.

Only TaskInitiator may issue `ContinueTaskInNewWorker` against the exact Task revision, expiry outcome and checkpoint. Unknown non-idempotent external effect must be reconciled or explicitly resolved first. One Task transaction moves `waiting_user` or `delivered` back to `queued`, records continuation intent/lineage and writes Outbox; it performs no allocation, file materialization or network work. WorkerSessionAllocator then applies the normal continuity quota and SessionAllocationQueue FIFO. A new WorkerRun with `trigger_type=task_session_continuation` is created only through normal Session dispatch, and its start snapshot references the source Task/Run/Session/outcome/checkpoint.

The new Session and Run use fresh AgentSession, runtime generation, clocks, leases and authority. Old queue position, ApprovalGrant, RepositoryAccessGrant and SecretLease do not transfer. Worker Runtime and Task Orchestration converge through owner events, revisions, idempotency and reconciliation; this decision does not introduce a distributed transaction or TaskSaga.

Why: the 14-day limit must remain a real execution boundary, but an infrastructure lifecycle event should not discard delivery identity or force a user to create a duplicate Task. Explicit continuation preserves intent and evidence while making repeat work, cost and uncertain external effects deliberate.

### Decision 56: Remove continuation time budgets from P0

The earlier explicit ContinuationBudgetSpec and Task cumulative active-time design is superseded by Decision 22 and ADR 0055. `ContinueTaskInNewWorker` binds actor, exact Task revision, expiry outcome, checkpoint and idempotency evidence only. The new Session and Run do not inherit source authority, and every later actual ACP prompt receives the same independent fixed 24-hour AgentTurn deadline.

Why: continuation does not need a second time-authorization workflow when P0 no longer treats time as a user-managed budget.

### Decision 57: Allow public Web egress while denying host and private networks in M1

M1 resolves every Worker RuntimeNetworkPolicy to `m1_public_web_restricted_v1` unless a stricter platform policy is selected. The policy allows the configured DNS resolver, public TCP 80/443 destinations, the co-located LiteLLM localhost endpoint and exact Backend WorkerControl/Trace/Artifact endpoints. It publishes no Worker host/public inbound port and denies Docker API, host gateway/`host.docker.internal`, metadata/link-local, RFC1918, CGNAT, IPv6 private/link-local, other Worker containers and control-plane infrastructure. Backend and DNS are narrow endpoint/port exceptions; Decision 62 adds only Backend PreviewProxy access to an exact running SessionProcess declared HTTP/WebSocket port. Resolved IPs and every redirect are re-evaluated; IPv6 is disabled when equivalent filtering cannot be proven.

TaskSpec, Agent, Skill and MCP input may only tighten the policy. SSH, non-Web protocols, private endpoints or additional destinations require an existing ApprovalGrant bound to exact destination, protocol, port, once/session scope and expiry, and platform policy may still deny them. Network decisions record policy revision, requested and resolved destination, decision/reason, Runtime generation and grant reference without default TLS interception.

Why: a real Coding Agent must reach package registries, CDNs and public documentation, while access to the host, Docker control plane and private networks is unnecessary and dangerous. This baseline is not DLP because arbitrary public Web destinations remain reachable; it is acceptable only for the loopback-only SingleUserLocalProfile with minimal Worker credentials. A later multi-user profile can add an egress proxy, domain policy and content controls behind the same RuntimeNetworkPolicy port.

### Decision 58: Keep M1 RuntimeInstance resident until WorkerSession terminates

M1 fixes RuntimeResidencyPolicy to `m1_resident_until_session_terminal_v1`. After initial allocation, a WorkerSession in `interactive`, `running`, `waiting_user` or `idle` retains one running RuntimeInstance with Shim, Codex ACP and LiteLLM. AgentTurn or WorkerRun terminality, stage delivery, `waiting_user` and ordinary idle do not stop, pause, hibernate or replace the container and do not increment runtime generation. M1 defines no `suspending`, `suspended` or `resuming` runtime state and does not use `docker pause`.

Residency is not execution authority. Without an active AgentTurn and SessionDispatchPermit, Shim cannot start a new ACP prompt or Tool invocation; it may only maintain heartbeat/control, drain Trace/Artifact and host explicit SessionProcesses governed by Decision 60. Runtime stops or is replaced only for explicit close, 48-hour idle expiry, 14-day absolute expiry, authorization/security termination or RuntimeRecoveryIncident fencing. Because `waiting_user` pauses only the idle clock, a resident runtime may remain until the absolute boundary and continues consuming continuity quota and host resources.

Why: this gives the first slice the same immediate follow-up behavior as a local Coding Agent and avoids introducing a second suspend/resume state machine before ACP resume and normal crash recovery are proven. Durable WorkingCopy and Agent state remain outside the container, so a later hibernation profile can be added without changing WorkerSession identity or ownership.

### Decision 59: Use one fixed M1 Runtime resource profile with aggregate admission

M1 has one server-owned `RuntimeResourceProfile=local_standard_v1`: 2 vCPU, 4 GiB memory, 512 PIDs and a 10 GiB container writable layer. TaskSpec, Frontend, Assistant, Agent, Skill and MCP cannot submit arbitrary resource values. RuntimeLease snapshots the exact profile revision; WorkingCopy, cache and Agent state remain in separately governed persistent storage rather than the writable layer.

RuntimeCapacityAdmission uses configured aggregate CPU, memory and storage budgets that default to at most 80% of Docker Engine availability. WorkerSessionContinuityQuota remains 10 and measures resumable Session identities; it does not promise ten simultaneous heavy AgentTurns. Before issuing SessionDispatchPermit, Backend admits the legal queue head or standalone start. If capacity is unavailable, the resident Session and existing FIFO position remain unchanged with `wait_reason=runtime_capacity`; no active AgentTurn, `started_at` or 24-hour deadline is created. Terminal Turn releases capacity idempotently and wakes the next legal head without reordering or preemption.

CPU excess is throttled. Confirmed OOM, PID exhaustion or writable-layer hard limit produces `resource_exhausted` evidence and a user-visible waiting state rather than consuming the one automatic recovery attempt as if the same constrained replacement could succeed. Docker Desktop/Linux enforcement parity, aggregate sampling/reservation and persistent-storage quotas remain implementation spikes.

Why: unbounded Docker defaults let one build or ten resident Sessions exhaust the local machine, while multiple user-selectable profiles introduce resource authorization and scheduling complexity before measured demand. One server profile plus aggregate admission keeps the M1 product surface small and leaves profile expansion backward-compatible.

### Decision 60: Allow only explicit managed SessionProcesses to survive an AgentTurn

Runtime residency does not change ordinary process ownership. Every shell/Tool process and descendant remains in the originating AgentTurn process group/cgroup and is drained or terminated at Turn terminality. `&`, `nohup`, daemonization, stdio detachment and parent exit do not create a session resource or bypass the fixed deadline.

Preview servers, development servers and file watchers may cross a Turn boundary only when launched through the Worker managed-process interface. A successful launch creates SessionProcess with stable identity, WorkerSession and source Turn/Tool, Runtime generation/process handle, kind, command summary, WorkingCopy-relative cwd, declared ports, lifecycle state, bounded log reference and revision. WorkerDetail exposes active/history processes and authorized stop/restart actions. Worker cannot adopt an unknown orphan after the source Turn is terminal, and restart creates a new identity with lineage.

SessionProcess is not an AgentTurn, WorkerRun or independent authority. It cannot invoke ACP or autonomous Agent work. It inherits the exact Runtime resource/network/mount/Tool/Grant/Secret boundaries, remains visible to aggregate capacity accounting after Turn admission releases, and emits bounded/rotated logs into Trace rather than unbounded SSE or writable-layer output.

WorkerSession close, idle/absolute expiry and authorization/security termination stop all SessionProcesses. Process output, logs, port traffic, health checks and status updates do not renew the 48-hour idle clock. Runtime fencing/replacement marks old-generation processes stopped when termination is proven or lost otherwise, and never automatically restarts them; a user or later AgentTurn must explicitly create a new process identity. Decision 61 governs survival across individual AgentTurn terminal reasons.

Why: stopping everything breaks iterative preview workflows, while allowing arbitrary detached children turns container residency into a deadline and cancellation bypass. An explicit managed resource preserves the useful interactive behavior with observable ownership and deterministic cleanup.

### Decision 61: Preserve accepted SessionProcess across every AgentTurn terminal reason

Once a managed start acceptance commits before its source AgentTurn becomes terminal, the SessionProcess survives `completed`, `cancelled`, `timed_out` and `failed` outcomes. `interrupt` is a control action that converges to `cancelled` with `terminal_reason=user_interrupt`; any raw Codex `interrupted` status remains adapter Trace provenance. The Turn terminal path still cancels ACP, the active Tool and every ordinary descendant, but it neither stops nor rolls back the accepted SessionProcess. Intermediate preview servers, watchers and services can therefore remain useful for later user interaction even when the surrounding Agent step ends abnormally.

Managed start and Turn terminal serialize on exact AgentTurn revision, Runtime generation and Worker-owner commit order. If start acceptance commits first, SessionProcess remains; if terminal commits first, Worker rejects the start and cannot adopt the later process as an orphan. A process still in `starting` after accepted start follows the same lifecycle rather than being cancelled solely by the Turn result.

This survival rule is subordinate to WorkerSession close, idle/absolute expiry, authorization/security termination, Runtime taint/fencing/replacement and resource hard limits. Those boundaries may stop the process, and replacement never auto-restarts it. Process activity still does not renew idle.

Why: explicit managed registration already separates the process from Turn-owned execution and makes it visible, bounded and controllable. Preserving only normal completions would discard useful intermediate services based on the Agent outcome rather than the process's own value or health.

### Decision 62: Proxy SessionProcess preview through Backend without publishing Worker ports

M1 exposes a running `preview_server` or `dev_server` only through a Backend PreviewProxy. `OpenSessionProcessPreview` validates the server-configured actor, exact WorkerSession, SessionProcess revision, Runtime generation and declared port, then returns a temporary opaque SessionProcessPreviewAccess. RuntimeSupervisor resolves the target from trusted current-generation container state; client, Agent, TaskSpec and preview content cannot provide URL, IP, host, undeclared port, Docker mapping or `CONNECT` destination.

Worker publishes no host/public port. RuntimeNetworkPolicy permits only PreviewProxy to reach the selected declared HTTP/WebSocket port over the internal Docker network. HTTP bodies and WebSocket frames use a direct proxy data plane rather than EventCenter, SSE, Trace JSONL or ObjectStore; only bounded issuance/revocation, decision, status, duration and byte-count metadata is recorded. PreviewProxy strips Switchback cookies, authorization, internal identity and hop-by-hop headers before forwarding.

Every request and WebSocket upgrade revalidates access audience/expiry, process status and runtime generation. Process stop/fail/lost, WorkerSession terminality, Runtime fencing/replacement, authorization revocation or access expiry rejects new traffic and closes indexed active tunnels. Explicit OpenPreview may refresh idle once; resource loads, polling, HMR, WebSocket frames, health checks and process traffic do not. M1 keeps route/origin topology as a loopback-only implementation detail rather than a domain protocol; isolation is reviewed before non-local or authenticated multi-user deployment.

Why: live preview is a main benefit of SessionProcess continuity, but random host-port mapping bypasses Backend control and Artifact-only screenshots remove interactivity. A narrow Backend proxy preserves HTTP/WebSocket workflows while keeping container endpoints private and lifecycle-revocable.

## Risks / Trade-offs

- [Risk] Too many documents drift from code -> Mitigation: `AGENTS.md` states source code and tests win; OpenSpec changes must be archived only when implemented and validated.
- [Risk] TaskSpec becomes too generic -> Mitigation: keep P0 fields minimal and require acceptance criteria, approval policy, and context refs.
- [Risk] Real containerized Agent makes P0 heavier -> Mitigation: start with Codex, keep AgentProfile/Adapter boundaries explicit, and defer broad compatibility.
- [Risk] Public Web egress can exfiltrate WorkingCopy content even when private networks are blocked -> Mitigation: M1 is loopback-only single-user, injects no host/Docker/code-platform root credential, records destination decisions and does not claim DLP; multi-user profiles require a stricter egress adapter.
- [Risk] Up to ten idle or waiting Worker runtimes retain memory and processes for hours or days -> Mitigation: count every resident Session against continuity quota, apply explicit Runtime resource profiles, expose manual close and retain a future hibernation adapter without adding M1 suspend states.
- [Risk] A fixed 4 GiB profile is too small for some builds or aggregate metrics race under pressure -> Mitigation: hard per-container limits, admission before Turn deadline, explicit resource-exhausted evidence, configurable deployment revision and Docker Desktop/Linux spike before adding user-selectable profiles.
- [Risk] A detached child or registered process keeps consuming resources or performing work after its Turn -> Mitigation: Turn-owned cgroup cleanup, no implicit promotion, narrow SessionProcess kinds, stable source/generation/revision, bounded logs, WorkerDetail controls, continuous capacity accounting, Session-terminal cleanup and no automatic restart after Runtime replacement.
- [Risk] A cancelled or timed-out Turn leaves an unwanted managed service running -> Mitigation: only committed managed starts survive, process types remain narrow and visible, WorkerDetail exposes stop, process activity does not renew idle, and Session/security/Runtime/resource boundaries retain stop authority.
- [Risk] Worker-generated preview content reaches Switchback APIs or turns PreviewProxy into SSRF -> Mitigation: M1 remains loopback-only, accepts no arbitrary target, resolves exact process/generation/declared port, publishes no host port, strips forwarded control credentials and requires a new isolation review before any non-local or multi-user profile.
- [Risk] Two trace sinks drift -> Mitigation: object storage JSONL is the source of truth; Langfuse export is derived and may be replayed.
- [Risk] Truth creates unsafe personalized behavior -> Mitigation: require source refs, scope, confidence, risk, lifecycle, and review.
- [Risk] P0 full-fidelity Trace stores sensitive data -> Mitigation: enforce object storage permissions and retention now; add redaction as a follow-up governance task.
- [Risk] EventCenter becomes a hidden monolith -> Mitigation: event schemas are published languages; aggregates stay in owning contexts.
- [Risk] LiteLLM details leak into domains -> Mitigation: domains only emit ModelInvocationMetadata and route policy ids.
- [Risk] Long-lived full-fidelity data grows without bound -> Mitigation: hot/warm/cold lifecycle, metadata/size dashboards, quarantine, explicit idempotent deletion and per-Tenant policy revisions.
- [Risk] Primary and fallback transports drift or execute one command twice -> Mitigation: one envelope/handler/ACK state machine, shared cursors, ChannelEpoch fencing, transport conformance tests, and idempotent command execution.
- [Risk] Automatic recovery loops, repeats side effects, or silently loses Agent context -> Mitigation: one attempt per RuntimeRecoveryIncident, generation/epoch fencing, persisted cursors, ACP resume verification, fresh SecretLease, and waiting_user escalation.
- [Risk] Quiet model/tool work is mistaken for runtime death, or a partitioned old container writes beside its replacement -> Mitigation: independent 15s RuntimeHeartbeat, hard process/connection evidence, bounded cancellation grace, and exclusive WorkingCopy mount fencing before replacement.
- [Risk] Managed B2 exposes a repository access Token to arbitrary Agent commands -> Mitigation: single-repository short leases, separate minimal permission profiles, provider rulesets, network fencing, deterministic known-secret removal, revocation and A/C fallback profiles.
- [Risk] PostgreSQL scheduled-command pollers duplicate work, lose timers, or create a second transition after crashes -> Mitigation: same-transaction Task/Transition/Outbox, stable idempotency keys, short `SKIP LOCKED` leases, monotonic fencing, expected revisions and owner reconciliation.
- [Risk] Direct Worker chat silently mutates a completed Task or creates noisy Tasks -> Mitigation: terminal Task revision fencing, standalone WorkerRun identity, optional origin lineage and explicit new Task creation.
- [Risk] Worker forges or infers Task-creation consent -> Mitigation: adapter-originated intent evidence, TaskSpec digest binding, readiness and authorization revalidation, and idempotent Task/Transition creation.
- [Risk] A lightweight Task loses advanced portfolio workflow features -> Mitigation: preserve Task transition evidence and reintroduce delegation, rich Task views or TaskSaga only after observed P1 demand.
- [Risk] Browser misses or misorders projection updates and silently diverges -> Mitigation: consistent snapshot cursor, exact base/result versions, bounded cursor replay, explicit resync and client-upgrade control events.
- [Risk] Persisting every Agent chunk overwhelms projection storage, or dropping chunks corrupts final UI -> Mitigation: bounded transient lane plus semantic/periodic/full terminal checkpoints and independent high-fidelity Trace.
- [Risk] Provider-native display types leak authority or unsafe executable content into Frontend -> Mitigation: Switchback-owned DisplayPart, protocol adapters, typed renderer registry, sanitized payloads and fail-closed unknown security semantics.
- [Risk] React query cache, SSE handlers and chat runtime each become a competing state owner -> Mitigation: explicit Router/Query/ViewProjectionStore/overlay/PendingCommand boundaries and adapter-only assistant-ui integration.
- [Risk] Feishu card callbacks or provider state become a second Task/Approval workflow -> Mitigation: DeliveryCardProjection/CardInstance separation, limited action refs, Gateway revalidation, callback idempotency and owner-evidence convergence.
- [Risk] An ordinary Feishu reply is routed to the wrong parallel Worker or stale card -> Mitigation: bind the topic to Assistant Conversation, require explicit InteractionTargetRef for execution objects, omit current focus and fence actor/binding revisions.
- [Risk] A global default or model guess sends protected context/execution to the wrong Workspace -> Mitigation: allow unresolved chat, require explicit WorkspaceContextResolution, make TaskSpec workspace mandatory and reject stale resolution before Task creation.
- [Risk] An authorized sender causes Workspace data to be posted to an unapproved group audience -> Mitigation: independent WorkspaceChannelShareGrant, fresh provider audience snapshot, source/profile field intersection, minimal fallback and per-actor callback/Web authorization.
- [Risk] Revoking a group grant is mistaken for erasing already delivered information -> Mitigation: fence future output, best-effort provider update/recall, explicit cleanup outcome and residual-exposure audit without a complete-erasure claim.
- [Risk] Reusing one Conversation across Workspace changes leaks W1 Turns or Evolution evidence into W2 -> Mitigation: successor Conversation identity, scope-filtered handoff, atomic binding rollover, per-generation archives and explicit protected carry-over.
- [Risk] Concurrent messages or duplicate rebind commands create ambiguous generations -> Mitigation: binding/source revision serialization, stable idempotency, one-transaction successor creation and stale-envelope rejection.
- [Risk] Shared Conversation controller becomes an accidental Workspace or execution super-role -> Mitigation: treat controller as Interaction-only coordination authority and reauthorize every target action/disclosure for the real actor.
- [Risk] Provider membership churn leaves a shared Conversation uncontrolled or auto-promotes an unsafe actor -> Mitigation: last-controller protection, `control_orphaned` fencing and explicit policy-authorized recovery.
- [Risk] A ready group message or model interpretation accidentally creates cost-bearing work -> Mitigation: separate readiness from explicit intent, require a policy-approved creation surface and bind confirmation to exact draft/readiness/Workspace revisions.
- [Risk] Another group participant silently assumes ownership by confirming someone else's ready draft -> Mitigation: immutable draft initiator, actor-equality checks and explicit fork-based adoption with fresh authorization/readiness.
- [Risk] A Task viewer, Workspace editor or Conversation controller interrupts work or increases cost -> Mitigation: immutable TaskInitiator, fixed P0 actor rules, expected-Task-revision fencing and owner-only emergency cancel.

## Migration Plan

This is a documentation and planning change, so deployment migration is not applicable.

Recommended adoption path:

1. Use the new OpenSpec change as the review vehicle for the initial architecture baseline.
2. Discuss open questions in `docs/requirements/requirements-matrix.md`.
3. Promote stable protocol fields into `pkg/contracts` when implementation begins.
4. Add state-machine tests before implementing TaskSaga and WorkerRun transitions.
5. Spike pinned `coder/acp-go-sdk@v0.13.5` -> `codex-acp@1.1.2` -> `@openai/codex@0.144.1` inside a Docker runtime with co-located LiteLLM and report/patch output.
6. Archive this OpenSpec change only after the agreed docs and initial code skeleton exist.

Rollback strategy: revert this documentation change or create a follow-up OpenSpec change that supersedes specific capability specs.

## Open Questions

The first usable product slice is fixed as a Worker tracer bullet under a loopback-only SingleUserLocalProfile. The server derives one configured tenant/actor, clients cannot override identity, and Frontend has no login; SSO/OIDC, multi-user sessions and dynamic Membership/RBAC are later work. One configured GitHub repository is resolved/imported by a trusted host adapter using an env or mounted read-only credential. Worker containers receive only materialized WorkingCopy and no provider token, credential helper or remote Git/`gh` capability; GitHub App, CredentialBroker, dynamic RepositoryAccessGrant and managed B2 follow after the tracer bullet. The user uses a Worker-first Web form to select Workspace/repository/base commit, enter goal, acceptance criteria and optional constraints, preview a human-readable structured TaskSpec and explicitly submit it; a structured API reuses the same command and validator. A first-time user may stay in that form and explicitly start a separate idempotent Workspace bootstrap, but `CreateWorkspace`/`RepositoryImport` and network clone never join the Task transaction; TaskSpec preview and submission remain blocked until import produces the first readable WorkspaceRevision and the user selects its exact workspace/revision. Repository-backed Workspace lifecycle is `provisioning -> ready | setup_failed`: failure preserves the logical Workspace, sanitized error and append-only attempts, creates no active revision/Task/WorkerSession, and permits explicit retry under the same bootstrap identity or archival; success atomically creates RepositoryCheckout, first WorkspaceRevision, active pointer, ready state and Outbox. Before confirmation, Code Platform Access resolves branch/tag/commit outside the database transaction and signs a short-lived RepositorySelectionSnapshot bound to actor, tenant, stable repository, requested ref, exact SHA, observation time and digest. CreateWorkspace persists that SHA; Importer/retry/Task/dispatch never re-resolve the remote ref. Snapshot proves baseline consistency, not authority, so the configured local repository policy is revalidated at confirmation and fetch. Users do not edit raw JSON and M1 does not invoke a model to generate or complete the TaskSpec. Before creation, deterministic validation checks required fields, Workspace/repository/base, revisions and local policy; failures return field blockers and create no Task. After creation, a newly discovered semantic conflict or context gap terminates the current AgentTurn, moves Task to `waiting_user`, asks through WorkerDetail, and starts a new fixed 24-hour AgentTurn after the user replies without routing through Assistant. The user then obtains one WorkerSession bound to the explicitly selected Workspace, runs real ACP Codex through LiteLLM inside Docker, streams Trace to Web WorkerDetail and receives report/patch. Assistant clarify, Delivery, Evolution/Inspiration and Skill Market are later milestones rather than dependencies of this slice. Its repository boundary is also fixed: import one configured GitHub repository at an immutable base commit into an isolated WorkingCopy, deliver report/patch/ChangeSet, and let `apply` create a new Switchback WorkspaceRevision without pushing or creating a remote branch or pull request.

1. No further PreviewProxy decision blocks M1 requirements. Route/origin compatibility and whether live preview enters the tracer-bullet acceptance gate are deferred behind the mandatory report/patch and Worker follow-up path; ADR 0065 only preserves the Backend-proxy/no-host-port direction for later implementation.
