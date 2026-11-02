## ADDED Requirements

### Requirement: Sealed observation sources are independently reconstructable
The system SHALL retain immutable Tenant/User/Workspace-scoped Run and Turn basis fragments and one supplemental fragment per accepted logical model observation. Zero model members MUST NOT invalidate proven basis input, original timestamps and complete report output. Published fragments MUST bind verified core provenance and MUST NOT depend on Runtime memory or rewrite legacy source, TraceManifest, checkpoint or v2 job content.

#### Scenario: Backend restarts after publication
- **WHEN** Runtime memory and its original credential registry no longer exist but a valid fragment and scan receipt were committed
- **THEN** the same complete source bytes and deterministic projection are reconstructed from verified durable evidence

#### Scenario: First Turn has a legacy zero-model marker
- **WHEN** actual immutable input/report and original-scope basis proof are valid
- **THEN** v3 publishes the Run/Turn basis without requiring the legacy model-bearing source to be complete
- **AND** the old marker and its job remain unchanged

#### Scenario: Core provenance is invalid
- **WHEN** the initial Turn is ambiguous, a required report is corrupt, or scope/runtime identity does not match
- **THEN** publication is blocked with a safe reason and no fabricated parent input is exported

### Requirement: Models are accepted by a single transport-independent owner
The system SHALL accept model facts through one application owner before dispatching remaining business events to Turn waiters. Acceptance MUST revalidate exact journal identity, current channel/runtime authority and the historical start relationship among Tenant/User/Workspace, Session, Run, AgentSession and Turn; it MUST NOT require the observed Turn to be currently active or confer new execution permission.

#### Scenario: Callback arrives after terminal
- **WHEN** an authorized callback for Turn A arrives while Runtime is idle, Turn B is active, or Turn B settlement is replaying
- **THEN** original correlation is preserved, the event is consumed without a competing reader, and subsequent heartbeats progress
- **AND** no Task/Run/Turn status or Session idle clock changes because of the model fact

#### Scenario: Applied event was ignored
- **WHEN** a historical model journal row is applied with an ignored or rejected outcome
- **THEN** discovery does not treat it as accepted and does not rewrite its outcome

### Requirement: Original-scope safety evidence is atomic and durable
The system MUST scan original envelope/input/report, canonical fragment and deterministic wire fragment against exact originating Runtime generation credentials before storing content-bearing candidates. A Backend-only receipt SHALL bind frame/wire/core-provenance digests and scanner/compiler/policy revisions without retaining token values. Model Apply and receipt/candidate persistence MUST share one transaction; basis persistence MUST share the first evidence-input checkpoint freeze transaction before ObjectStore finalization.

#### Scenario: Crash or fencing races model Apply
- **WHEN** receipt persistence fails or authority becomes stale before Apply commits
- **THEN** no accepted cursor advancement or independently publishable candidate escapes the failed transaction

#### Scenario: Original registry is unavailable
- **WHEN** no valid original-scope receipt exists and the original registry was removed, including forced-stop or recovery before basis capture
- **THEN** the affected content is an explicit safety coverage gap and is not certified using replacement or current-config credentials

#### Scenario: Optional scan is rejected
- **WHEN** an otherwise legitimate model event's bounded scan detects credentials or cannot complete
- **THEN** only safe no-content diagnostics are persisted, and optional scan retries do not indefinitely stall the ordered cursor
- **AND** unsafe content is not written to supplemental objects or sent to Langfuse

#### Scenario: Encoder or receipt changes
- **WHEN** reconstructed frame/wire digests or compiler/scope evidence differ from the receipt
- **THEN** export is blocked before HTTP without reinterpreting old IDs under a new encoder

### Requirement: Discovery respects a durable Run activation boundary
The system SHALL establish immutable v3 activation and the exact exclusion set of Runs visible at activation, then independently discover eligible candidates and publications. It MUST NOT infer commit order from timestamps, depend on enqueue notifications, or admit later events of excluded Runs. Historical requests MUST bind an authenticated actor, exact Run and initial/target source/event digests, version and idempotency key; missing historical safety evidence MUST NOT be bypassed.

#### Scenario: New Run commits after activation snapshot
- **WHEN** its transaction began earlier but was not visible in the activation snapshot
- **THEN** its eligible fragments are eventually discovered regardless of older application timestamps

#### Scenario: Existing Run receives new observations
- **WHEN** the Run was visible at activation and no exact historical request selects its sources
- **THEN** restart, new Turns or callbacks do not implicitly re-export that history

#### Scenario: Publication commits without enqueue
- **WHEN** Backend crashes after publication commits but before its job exists
- **THEN** bounded discovery creates one source/version job on restart

#### Scenario: Historical selection is repeated
- **WHEN** the same exact request and idempotency key are resubmitted
- **THEN** the original selection is returned without widening its event set or resetting old jobs

### Requirement: Publication and projection are independently fenced
The system SHALL persist source identity/checksum/version jobs and bounded database-clock leases. ObjectStore and HTTP I/O MUST remain outside transactions. Logical source identity MUST be unique independently of checksum; identical duplicates converge and conflicting content is rejected. Publication and projection completion MUST reject stale or expired lease generations. Published objects MUST be protected from existing orphan reconciliation as referenced evidence.

#### Scenario: Object write succeeds but publication commit is lost
- **WHEN** the publisher retries after restart
- **THEN** it verifies and reuses the exact content-addressed object, commits one publication, and protects that reference from orphan cleanup

#### Scenario: Generic orphan reconciliation encounters a fragment
- **WHEN** a temporary or finalized observation-fragment object exists, including before publication commits
- **THEN** inventory excludes it and direct candidate deletion rejects it even from stale inventory
- **AND** failed-publication residue is retained until a separately reviewed retention policy exists; old reconcilers are stopped before version cutover or rollback

#### Scenario: Worker loses its lease
- **WHEN** an older publish/export attempt completes after expiration or a newer claim
- **THEN** it cannot replace the publication binding or current job outcome

#### Scenario: Parent is not yet published
- **WHEN** a valid model source's basis is still being published
- **THEN** the model waits with a retryable parent dependency
- **AND** proven parent corruption or missing safety proof is reported distinctly

### Requirement: Repeated projection cannot regress or double-count a model
The system SHALL use langfuse.projection.v3 IDs and one fixed compiler for all fragment paths. Root/Turn payloads MUST NOT depend on model collections, current Runtime or latest result. Logical model identity MUST exclude arrival time, channel epoch and publication/manifest checksum; all early and late models MUST use one supplemental path. Known credentials SHALL be checked before additional encoding and before HTTP, with exact original-scope wire proof verification.

#### Scenario: Early and late models share one Turn
- **WHEN** model A arrives before terminal and B afterward, including after the legacy source passed
- **THEN** A and B each have one v3 generation and immutable parents while legacy source/job remain unchanged

#### Scenario: Same model identity carries conflicting content
- **WHEN** another accepted message claims the same logical model with a different canonical payload
- **THEN** the conflict is recorded safely without a second counted generation or overwrite

#### Scenario: Old batch arrives after a newer Turn
- **WHEN** an earlier immutable batch is replayed after subsequent basis/model batches
- **THEN** existing content remains unchanged and exact readback allows unrelated later observations

#### Scenario: JSON numbers exceed JavaScript precision
- **WHEN** valid source I/O contains large integers or high-precision decimals
- **THEN** original JSON text is preserved in json-text.v1 and compared exactly without float64 coercion
- **AND** usage outside the supported safe-integer range is blocked rather than truncated

### Requirement: Version cutover and coverage are explicit
The system MUST stop v2 automatic discovery/claim/export when v3 becomes the runtime path, preserve v2 evidence read-only, and use independent activation/jobs without changing the v2 encoder constant. Aggregation MUST select one projection version. Coverage SHALL describe only an exact accepted-source snapshot and MUST NOT assert that no future callbacks exist.

#### Scenario: Two versions retain historical remote data
- **WHEN** an explicitly selected old Run is rebuilt under v3
- **THEN** namespaces remain separate and metrics do not sum their duplicate model facts

#### Scenario: Rollback to a v2 binary
- **WHEN** application code is rolled back
- **THEN** observation workers remain disabled until separately reviewed and migrations do not delete evidence

#### Scenario: All currently accepted sources have passed
- **WHEN** exact readback verifies the current source set
- **THEN** coverage reports that set and its counts without claiming total request completion or changing Task outcome

### Requirement: Observability failure is not execution failure
The system SHALL keep publication, HTTP and readback independent of Backend readiness, Runtime settlement, next-turn dispatch and secret cleanup. Local capture MUST be bounded and MUST NOT wait for future callbacks; core PostgreSQL failures MUST remain distinguishable from optional failures.

#### Scenario: Langfuse remains offline
- **WHEN** two Turns finalize valid core evidence while the exporter stalls or fails
- **THEN** both settle and the next dispatch proceeds without remote completion while observability retains its actual status

#### Scenario: Runtime becomes terminal with observation backlog
- **WHEN** durable fencing is proven for the exact Runtime identity
- **THEN** its registry is cleaned without waiting for publication/HTTP and committed receipts remain usable
- **AND** an unconfirmed fence is not treated as authority for secret cleanup
