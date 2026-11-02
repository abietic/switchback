## 1. Contracts And Durable Model

- [x] 1.1 Add versioned contracts for Conversation commands, AssistantDecision, seven-dimension readiness, TaskDraft, checkpoint, creation intent, and Assistant projection events; update generated-protocol checks.
- [x] 1.2 Add additive PostgreSQL migrations for Conversation, ordered Turns, AssistantRun queue/lease, ContextPack metadata, TaskDraft revisions, readiness, checkpoints, creation intents, idempotency results, evidence refs, and Assistant projections.
- [x] 1.3 Implement `internal/interaction/domain` entities, value objects, revision rules, terminal outcomes, and unit tests without infrastructure dependencies.
- [x] 1.4 Add server-owned SingleUserLocalProfile Assistant model, readiness-policy, context-budget, timeout, and feature-flag configuration with client override rejection tests.

## 2. Message Acceptance And Processing Queue

- [x] 2.1 Define Interaction repositories, transaction, clock, outbox, model, ObjectStore, Task admission, and projection ports with deterministic fakes.
- [x] 2.2 Implement PostgreSQL adapters for Conversation/Turn/Run state, stable owner sequence, expected revision, idempotency, and Outbox writes.
- [x] 2.3 Implement `SendConversationMessage` as one model-free transaction returning stable accepted identities and preserving server-owned actor/scope.
- [x] 2.4 Implement a leased outbox-driven AssistantRun dispatcher with one active run per Conversation, stable queued order, restart recovery, and duplicate-claim/concurrency tests.

## 3. Context, Model, And Readiness

- [x] 3.1 Implement bounded M2 ContextPack compilation from current Conversation, latest draft/readiness/checkpoint, rules/policy, and explicit authorized Workspace metadata with selected/dropped evidence tests.
- [x] 3.2 Implement the typed Assistant model port and scripted fake adapter for clarification, ready draft, informational, malformed, timeout, and policy-conflict outcomes.
- [x] 3.3 Implement the LiteLLM Assistant adapter using one server-owned profile per AssistantRun, sanitized diagnostics, no provider fallback, and model-observation correlation metadata.
- [x] 3.4 Implement AssistantDecision schema/policy validation and immutable seven-dimension TaskReadinessAssessment including allowed defaults and hard-blocker tests.
- [x] 3.5 Implement one-question clarification ordering, three-question ClarificationCheckpoint, continue/narrow/cancel actions, question-budget reset, and stale-checkpoint tests.

## 4. TaskDraft And Task Handoff

- [x] 4.1 Implement revisioned initiator-owned TaskDraft creation/update, visible assumptions, deterministic TaskSpec digest, exact Workspace binding, and invalidation tests.
- [x] 4.2 Implement exact ready-draft confirmation and immutable ExplicitTaskCreationIntent with actor/draft/readiness/Workspace/policy/digest/idempotency fencing.
- [x] 4.3 Extend Task Orchestration admission to consume Assistant intent through the existing Task/Transition/SessionAllocationRequest/Outbox transaction while preserving direct TaskSpec behavior.
- [x] 4.4 Add integration tests for successful handoff, stale or unauthorized evidence, duplicate confirmation, response loss, reused intent, rollback, and absence of partial Task/Worker state.

## 5. Evidence, Events, And Observability

- [x] 5.1 Implement checksum-addressed Assistant JSONL evidence segments and manifests for checkpoint, ready draft, Task creation, cancellation, and failed-run boundaries.
- [x] 5.2 Exclude credentials, environment values, hidden chain-of-thought, and unapproved raw provider payloads from Assistant evidence; add known-secret and scope-isolation tests.
- [x] 5.3 Define and publish owner-sequenced Assistant IntegrationEvents, then implement idempotent PostgreSQL projection handlers for Conversation and Task/Worker handoff state.
- [x] 5.4 Add optional Langfuse Assistant trace/span/generation export and prove ObjectStore or observability failure degrades independently from committed Conversation and Task outcomes.

## 6. Gateway And Frontier

- [x] 6.1 Add Gateway routes and application wiring for create/open Conversation, exact Workspace selection, send message, checkpoint action, confirm draft, cancel queued work, and retry failed run.
- [x] 6.2 Add `assistant_conversation` ProjectionSnapshot and typed SSE deltas/control events using existing epoch/cursor/version, replay, resync, and command-causation contracts.
- [x] 6.3 Add HTTP/SSE tests for identity override, stale revision, duplicate command, snapshot/subscribe race, reconnect replay, cursor gap, access failure, and pending-command convergence.
- [x] 6.4 Wire Interaction dispatcher, LiteLLM profile, PostgreSQL repositories, ObjectStore, Task admission, projection handlers, and feature flag into `internal/localproduct`, Backend startup, and local Compose.

## 7. Assistant Web

- [x] 7.1 Add typed/runtime-validated Assistant API contracts, command client, route identity, query ownership, projection reducer, pending-command state, and SSE/resync controller.
- [x] 7.2 Build the focused Assistant route with ordered visible Turns, queued/running/failed state, message composer, Workspace selection, and sanitized diagnostics.
- [x] 7.3 Render one-question clarification, checkpoint choices, visible assumptions, human-readable TaskDraft preview, exact confirmation, and disabled stale/pending actions without raw JSON editing.
- [x] 7.4 Add canonical Task/Worker handoff links while keeping execution/review controls in existing views; cover desktop/mobile, reconnect, duplicate response, stale confirmation, and direct-Task fallback in Vitest/Playwright.

## 8. Acceptance And Documentation

- [x] 8.1 Add a deterministic end-to-end acceptance path from ambiguous natural-language message through clarification, ready draft, explicit confirmation, Task admission, and existing fake Worker dispatch.
- [x] 8.2 Extend the failure matrix for Backend restart, concurrent messages, model timeout/malformed output, stale Workspace/draft, duplicate outbox delivery, Task rollback, ObjectStore failure, and projection resync.
- [x] 8.3 Add a real-provider local Compose acceptance that verifies one Assistant clarification/ready decision and dispatches one real Codex Worker without printing user/model content or credentials.
- [x] 8.4 Update architecture, operations, manual acceptance, API examples, data locations, rollback, and known-boundary documentation for the Assistant slice.
- [x] 8.5 Run Python/model adapter tests, `make verify`, Web unit/build/Playwright, OpenSpec strict validation, Compose health checks, and the documented real-provider acceptance; record the scenario audit and closeout evidence.
