# Runtime Execution Core Specification

## Purpose

Define the M1 Worker Runtime provisioning, isolation, ACP control, model routing, evidence, observability, residency, failure, and cleanup contracts.

## Requirements

### Requirement: Backend owns Runtime provisioning
The system SHALL provision every M1 Worker Runtime through the Backend-owned RuntimeSupervisor using server-derived image, network, resource, mount, identity and secret configuration.

#### Scenario: Runtime is created for an allocated Session
- **WHEN** Worker Runtime accepts an allocation for an authorized Tenant/User/Workspace WorkerSession
- **THEN** RuntimeSupervisor creates exactly one RuntimeInstance from server configuration and records its runtime id, container id and generation before dispatching Agent work

#### Scenario: Client supplies Runtime configuration
- **WHEN** a TaskSpec or client command contains an image, host path, Docker option, network target, provider credential or resource number
- **THEN** the system rejects or ignores the client-controlled field according to schema policy and never passes it to Docker Engine

### Requirement: Worker Runtime is isolated from Docker ownership
The system SHALL run Codex in a non-privileged Worker container without Docker Socket, host network, host port, arbitrary host mount or ambient infrastructure credential.

#### Scenario: Worker container is inspected
- **WHEN** acceptance inspects the created Worker container
- **THEN** it finds only server-derived Session storage and secret mounts, the approved control network, the fixed resource profile and no Docker administration capability

#### Scenario: Agent requests broader container access
- **WHEN** Agent output, Tool input or TaskSpec requests privileged mode, Docker Socket, host network or an arbitrary host path
- **THEN** RuntimeSupervisor refuses the request and records a policy-safe diagnostic without creating a broader Runtime

### Requirement: Codex is controlled through ACP
The system SHALL use the pinned ACP Go client and `codex-acp` adapter to initialize, authenticate, create or load an AgentSession and execute AgentTurns without parsing Codex TUI output.

#### Scenario: First AgentTurn starts
- **WHEN** a ready WorkerRun receives its first valid dispatch permit
- **THEN** the Shim completes typed ACP initialize, authenticate and session creation before sending the prompt and records the capability snapshot

#### Scenario: Follow-up AgentTurn uses the live Session
- **WHEN** an authorized follow-up targets the same live WorkerSession and existing AgentSession
- **THEN** the Shim sends the new prompt through the same ACP session and preserves the current WorkingCopy and Agent state

#### Scenario: ACP security semantics are unknown
- **WHEN** protocol version, required capability, authentication result or permission semantics cannot be determined
- **THEN** the Runtime fails closed before Tool execution or WorkingCopy mutation and preserves the raw diagnostic frame after redaction

### Requirement: WorkerControl is identity-fenced
The system SHALL bind every WorkerControl command and event to WorkerSession, WorkerRun, RuntimeInstance generation, AgentSession, AgentTurn, command/event id and monotonic stream sequence.

#### Scenario: Current Runtime reports an event
- **WHEN** the active generation emits a correctly sequenced authenticated event
- **THEN** Backend acknowledges it idempotently and applies it only to the matching Run and Turn

#### Scenario: Stale or duplicate Runtime event arrives
- **WHEN** an event has an old generation, wrong identity, reused sequence with different content or already acknowledged event id
- **THEN** Backend fences or deduplicates it without mutating current state and records an integrity diagnostic

### Requirement: One model profile is fixed per WorkerRun
The system SHALL resolve exactly one server-owned AgentProfile and model route before WorkerRun start, SHALL support the verified DeepSeek and Kimi profiles, and MUST NOT switch providers automatically inside an AgentTurn.

#### Scenario: WorkerRun starts with Kimi or DeepSeek
- **WHEN** policy selects an enabled profile for the Run
- **THEN** the Worker receives only that profile's model, base URL and scoped secret material and records the profile revision in the Run start snapshot and Trace metadata

#### Scenario: Upstream request fails
- **WHEN** the selected model provider rejects or fails a request
- **THEN** the AgentTurn reports failure or waiting-user state for that same profile and does not retry against the other provider automatically

### Requirement: Provider secrets are separated from Codex Tool identity
The system SHALL keep the upstream provider credential unreadable by Codex/Tool UID, exclude it from Agent child environments, and redact the LiteLLM proxy token from persisted ACP Trace while preserving transmitted ACP bytes.

#### Scenario: Runtime secret boundary is inspected
- **WHEN** acceptance checks the root secret, LiteLLM child and Codex/Tool child
- **THEN** the root file has server-owned restrictive mode, direct child reads fail, only LiteLLM holds the upstream key in its private environment, and Codex receives only the local proxy route/token

#### Scenario: Evidence is finalized
- **WHEN** raw ACP frames, normalized Trace, logs and Langfuse observations are persisted
- **THEN** known generated credentials and provider keys are absent and an explicit proxy-token redaction marker replaces the exact authentication value in the authoritative session Trace

### Requirement: Trace and Artifact evidence is authoritative and verifiable
The system SHALL persist Tenant/User/Workspace-scoped Trace JSONL, TraceManifest, report and patch Artifacts in ObjectStore with checksums and identity provenance before publishing their ArtifactRefs.

#### Scenario: AgentTurn completes
- **WHEN** Codex reaches a terminal result
- **THEN** Worker uploads raw and normalized Trace segments, report and patch, Backend verifies checksum and identity, and a finalized manifest references the exact WorkerSession/Run/Turn/Runtime generation

#### Scenario: Evidence checksum is invalid
- **WHEN** uploaded bytes do not match the declared checksum or Artifact identity
- **THEN** Backend rejects finalization, does not publish a valid ArtifactRef and prevents the Run from being reported as evidence-complete

### Requirement: Langfuse is a derived observability projection
The system SHALL export exact WorkerSession, WorkerRun, AgentTurn, Runtime and model-call identities to Langfuse while keeping ObjectStore Trace JSONL as the execution evidence source of truth. In the M1 single-user local profile, each Trace and AgentTurn span SHALL include the complete Turn input and finalized stage-report output, and each model generation SHALL include the complete model request body and serializable provider response without semantic redaction or application-level truncation.

#### Scenario: Langfuse projection succeeds
- **WHEN** finalized Trace and model observations are exported
- **THEN** the verifier can query one exact Trace with the expected Turn spans, generations, cardinality, runtime metadata and semantically equal non-missing input/output values

#### Scenario: Model content is exported
- **WHEN** LiteLLM completes a model call inside an active AgentTurn
- **THEN** the authenticated loopback callback sends the complete request body and serializable response through WorkerControl, while transport headers, environment variables, provider credentials, proxy credentials and callback internals remain outside the observation payload

#### Scenario: Known credential appears in projected content
- **WHEN** the complete Langfuse ingestion batch matches a server-known generated credential or provider key
- **THEN** Backend rejects the projection before issuing the Langfuse HTTP request, records observability degradation and does not echo the credential in the error

#### Scenario: Historical projection predates content capture
- **WHEN** an operator opens a model generation created before complete input/output capture was enabled
- **THEN** its missing content is reported as non-backfillable because the earlier callback discarded those bytes, while authoritative Trace and Artifact evidence remains available independently

#### Scenario: Langfuse is unavailable
- **WHEN** Codex and Artifact finalization complete but Langfuse export or exact verification fails
- **THEN** the system preserves `execution_outcome=completed`, records observability degradation and does not claim the observability acceptance gate passed

### Requirement: M1 Runtime remains resident for a live WorkerSession
The system SHALL keep one generation 1 RuntimeInstance, Shim, ACP session, Agent state and WorkingCopy resident while the WorkerSession is interactive, running, waiting_user or idle.

#### Scenario: Stage result is delivered
- **WHEN** an AgentTurn or Task stage reaches a terminal result but the WorkerSession lease remains valid
- **THEN** the Runtime remains alive without pause, hibernation or generation change and accepts no autonomous work until the next valid dispatch

#### Scenario: Session becomes terminal
- **WHEN** WorkerSession is explicitly closed, idles out, reaches absolute expiry or is security-terminated
- **THEN** RuntimeSupervisor fences new commands, stops and removes the Runtime, releases its secret resources and records cleanup outcome

### Requirement: Runtime loss fails closed in the first slice
The system SHALL NOT silently replace a lost Runtime in this change and SHALL preserve enough state to diagnose the loss and continue only through a later explicitly designed recovery path.

#### Scenario: Generation 1 container exits unexpectedly
- **WHEN** Docker reports the active Runtime container missing or exited before Session terminal
- **THEN** Backend fences dispatch, terminates the active Run with a runtime-loss reason, preserves proven Trace/Artifact refs and exposes an actionable waiting_user or failed state without creating generation 2

### Requirement: Runtime cleanup is exact and auditable
The system SHALL reconcile every created Runtime container and secret resource by exact server-recorded identity and SHALL treat unproven cleanup as a visible failure.

#### Scenario: Normal Session shutdown completes
- **WHEN** Backend closes a WorkerSession
- **THEN** RuntimeSupervisor removes the exact Worker container and runtime-secret resources, records `cleanup_confirmed=true` and leaves no managed Runtime

#### Scenario: Docker deletion fails
- **WHEN** the exact Runtime delete operation fails or post-delete enumeration still finds the resource
- **THEN** the system records sanitized resource identity, keeps cleanup unconfirmed, schedules reconciliation and does not report clean shutdown
