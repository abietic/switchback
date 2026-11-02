# Deterministic Memory Auto-Publication

Evolution may automatically publish only a narrow allowlist of MemoryTruth types: `explicit_preference`, `verified_descriptive_fact`, `entity_alias`, and `ephemeral_project_state`. Every candidate must also be low risk, non-sensitive, conflict-free, scope- and ACL-preserving, and supported by `user_explicit_statement`, `authoritative_source`, or `deterministic_runtime_verification` evidence. Ephemeral project state requires refresh and expiry; other mutable facts follow the active MemoryPublicationPolicy lifecycle rules.

Model output may create, classify, summarize, or recommend a candidate, but model inference, confidence, summaries, and repeated behavior do not grant automatic publication authority. `explicit_preference` means a low-risk choice tendency. Content that grants permission, prescribes mandatory behavior, changes production or external effects, or establishes a procedure is reclassified and reviewed as behavioral memory, ProcedureTruth, or InstructionPolicy.

Inferred preferences, behavioral or executable content, permissions, security, production/external operations, cross-user or organization scope, sensitive data, failure-derived rules, subjective judgments, conflicts, low confidence, or scope expansion require human review or rejection. Automatic publication still creates an immutable revision, deterministic decision with policy revision, audit event, Outbox projection, and dashboard entry that the user can correct, disable, delete, or restore.

## Status

Accepted as design baseline.

## Consequences

- Evolution remains useful without giving model judgments direct long-term authority.
- Auto-published memories are deterministic, replayable, scoped, expirable, and reversible.
- Some legitimate inferred preferences require review, trading recall speed for predictable governance.
- Dashboard and batch-review interaction remains a separate product decision.
