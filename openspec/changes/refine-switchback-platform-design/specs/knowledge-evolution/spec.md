## ADDED Requirements

### Requirement: Evolution generates evidence-backed TruthCandidate
The system SHALL generate TruthCandidate only from traceable Assistant or Worker evidence.

#### Scenario: Candidate has source references
- **WHEN** Evolution extracts a user preference from repeated Assistant Turns
- **THEN** the candidate includes source refs, scope, confidence, risk level, and lifecycle metadata

### Requirement: Switchback owns governed context revisions
The system SHALL persist ContextCandidate, immutable ContentRevision, ReviewTask, publication decision, tombstone, audit, and OpenVikingProjection state in Switchback RDBMS as the governance authority.

#### Scenario: OpenViking is unavailable during publication
- **WHEN** an approved revision cannot be written to OpenViking
- **THEN** Switchback retains the approved revision and Outbox state as `publish_pending`, retries projection, and does not report the revision as retrievable active context

### Requirement: Truth publication is governed by deterministic Memory policy
The system SHALL let a model create and recommend candidates without publication authority, and SHALL use versioned MemoryPublicationPolicy to allow automatic publication only for `explicit_preference`, `verified_descriptive_fact`, `entity_alias`, and `ephemeral_project_state` that satisfy evidence, sensitivity, conflict, scope/ACL, risk, and lifecycle gates.

#### Scenario: High-risk procedure candidate
- **WHEN** Evolution extracts a procedure that affects production, security, or cross-user behavior
- **THEN** the candidate remains `needs_review` and is not loaded into future ContextPacks as active Truth

#### Scenario: Model recommends unsafe automatic publication
- **WHEN** a model recommends auto-publish for a security, production, cross-user, conflicting, or low-confidence candidate
- **THEN** deterministic policy overrides the recommendation and records `needs_review` with model and policy versions

#### Scenario: Explicit low-risk preference
- **WHEN** a user explicitly states a low-risk choice preference that does not grant permission or prescribe production, external, irreversible, or mandatory behavior
- **THEN** deterministic policy may automatically approve `explicit_preference`, record the source Turn and policy revision, create an immutable revision, and publish it to the Memory dashboard and OpenViking projection

#### Scenario: Verifiable descriptive memory
- **WHEN** a `verified_descriptive_fact` or `entity_alias` is supported by an authoritative source or deterministic runtime verification, is non-sensitive and conflict-free, and preserves source scope and ACL
- **THEN** deterministic policy may automatically approve it with evidence and decision audit

#### Scenario: Ephemeral project state has lifecycle bounds
- **WHEN** an `ephemeral_project_state` candidate has authoritative evidence, `refresh_after`, and `expires_at`, and all other automatic-publication gates pass
- **THEN** deterministic policy may publish it and the system removes it from active retrieval when it expires

#### Scenario: Model-only inferred preference
- **WHEN** a model infers a preference from behavior, summaries, frequency, or confidence without a user explicit statement or independently verifiable source
- **THEN** the candidate cannot auto-publish and remains `needs_review` or is rejected with the policy reason

#### Scenario: Preference changes behavior or permission
- **WHEN** content labeled as `explicit_preference` grants permission, mandates a procedure, or changes production or external side effects
- **THEN** Evolution reclassifies it as behavioral memory, ProcedureTruth, or InstructionPolicy and requires human review

#### Scenario: Automatic publication expands scope
- **WHEN** the proposed Memory scope or ACL is broader than its evidence source, includes cross-user or organization scope, or contains sensitive or conflicting content
- **THEN** deterministic policy blocks automatic publication and records `needs_review` or `rejected`

#### Scenario: Instruction or Skill changes
- **WHEN** Evolution generates or modifies an InstructionPolicy, behavioral memory, or SkillPackage
- **THEN** the content remains pending human content/security review and cannot affect ContextPack or runtime capabilities before approval

### Requirement: Truth retrieval enforces scope and lifecycle
The system SHALL project approved active Truth into OpenViking and provide retrieval through Evolution only when user, workspace, tenant, project, repository, permission, RDBMS revision, projection, status, and lifecycle constraints allow it.

#### Scenario: Expired Truth
- **WHEN** a Truth has expired or is deprecated
- **THEN** ContextPack excludes it or marks it as dropped for lifecycle reasons

### Requirement: User can govern personal memory
The system SHALL provide MemoryLibrary for active/disabled/expired/deleted Memory and ReviewInbox for pending Memory/Procedure/Instruction/Skill, SHALL enforce reviewer scope, and SHALL perform editing, disabling, deleting, restoring, and review only through Switchback governance APIs with immutable revisions, tombstones, audit, and Outbox projection.

#### Scenario: User edits an active memory
- **WHEN** a user corrects a MemoryTruth in the dashboard
- **THEN** Switchback creates a new ContentRevision and audit event, updates the active pointer, and asynchronously replaces the OpenViking projection without deleting revision history

#### Scenario: User disables or deletes memory
- **WHEN** an authorized user disables or deletes a MemoryLibrary item
- **THEN** Switchback records the state transition or tombstone, emits an Outbox removal, preserves history, and excludes the memory from active retrieval

#### Scenario: User restores historical memory
- **WHEN** an authorized user restores a prior Memory revision
- **THEN** Switchback creates a new active ContentRevision referencing the selected history and projects that new revision without rewriting old revisions

#### Scenario: Reviewer opens a pending item
- **WHEN** an authorized reviewer opens a ReviewInbox item
- **THEN** the projection includes lane, content diff, source evidence, policy decision, risk, conflict, scope/ACL impact, reviewer requirement, history, and age without making the content active

### Requirement: Batch governance preserves high-risk review boundaries
The system SHALL allow batch reject and disable with per-item authorization, SHALL allow batch approve only for homogeneous low-risk Memory ReviewTasks, and SHALL return explicit per-item results under idempotent expected-revision checks.

#### Scenario: Homogeneous Memory batch is approved
- **WHEN** all selected ReviewTasks share scope, MemoryPublicationPolicy revision, low-risk Memory type, reviewer authority, and contain no sensitive, conflict, security, permission, or scope-expansion tags
- **THEN** ReviewBatchCommand may approve them and records a decision, revision, audit, and item result for each Memory

#### Scenario: Batch includes high-risk governed content
- **WHEN** a batch-approve selection contains Procedure, Instruction, Skill, cross-scope, security, permission, sensitive, or conflicting content
- **THEN** the system rejects batch approval for those items and requires individual review

#### Scenario: One batch item changed concurrently
- **WHEN** an item's current revision or status differs from ReviewBatchCommand expected values
- **THEN** that item returns `stale` without mutation, other independently valid items may complete, and the batch reports `completed_with_errors`

#### Scenario: Review remains pending
- **WHEN** a ReviewTask has no decision before any configured reminder threshold
- **THEN** P0 keeps it pending and inactive, displays age or sends a digest, and does not block Task/Worker execution or include it in ContextPack

### Requirement: Inspiration returns candidate context references
When the P2 Inspiration capability is explicitly enabled after a validated Workspace pilot, the system SHALL use a protected versioned Inspiration code repository with one YAML InspirationEntry per resource, Markdown context pages, and a deterministically generated `INSPIRATION_INDEX.md` as the governed declaration source for external URLs, summaries, and intent/workspace context rather than as unlimited context injection or a full document proxy. P0/P1 SHALL operate without this capability.

#### Scenario: Inspiration is unavailable or disabled
- **WHEN** ContextCompiler runs without the P2 capability or Inspiration returns no candidates
- **THEN** Assistant and Worker continue with Truth, Rules and Skills and do not treat absence as an execution error

#### Scenario: Human or automation changes semantic metadata
- **WHEN** a human, sync process, or model changes a resource summary, intent, scope, owner, source URI, or lifecycle
- **THEN** the change is submitted as a branch/PR, validated against schema and policy, and requires CODEOWNER review before protected main and runtime projections change

#### Scenario: External document candidate
- **WHEN** Assistant requests project context
- **THEN** Inspiration returns candidate resource references with permissions, freshness, owner, summary, and relevance score

#### Scenario: Full Feishu document read
- **WHEN** Assistant or Worker needs the full content of a Feishu document referenced by Inspiration
- **THEN** the agent uses lark-cli and the corresponding Skill to read the document from the returned link

#### Scenario: Source metadata changes
- **WHEN** a connector observes source version, ACL, freshness, or health changes
- **THEN** Inspiration updates its runtime ResourceRecord without creating Git commits, while semantic summary or intent changes are proposed through the versioned repository workflow

#### Scenario: Published repository revision is compiled
- **WHEN** a reviewed Inspiration commit reaches protected main
- **THEN** the compiler idempotently updates PostgreSQL ResourceRecord, regenerates the human index, and rebuilds the scoped OpenViking L0/L1 projection while external source content remains authoritative

#### Scenario: Repository webhook is lost
- **WHEN** protected main head differs from last projected SHA because a webhook was lost or processing failed
- **THEN** periodic reconciliation submits an idempotent exact-SHA sync request and converges runtime projections to the latest valid head without manufacturing semantic Git commits

### Requirement: ContextPack records budget decisions
The system SHALL let Assistant or Worker compile ContextPack from Evolution Truth query results, Inspiration resource candidates, Rules, and Skills while recording why each candidate context item was selected or dropped.

#### Scenario: Token budget exceeded
- **WHEN** relevant context exceeds the token budget
- **THEN** ContextPack keeps higher-priority items and records lower-priority items as dropped for budget reasons
