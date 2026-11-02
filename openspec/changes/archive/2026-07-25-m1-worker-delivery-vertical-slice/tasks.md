## 1. Contract Baseline And Production Scaffold

- [x] 1.1 Write and review a Spike promotion matrix that marks every reused component `promote`, `adapt`, `rewrite` or `discard` and names its production owner.
- [x] 1.2 Create the production Go module, `cmd/switchback-backend` composition root, DDD package boundaries and repository Make targets without importing Spike packages.
- [x] 1.3 Define versioned TaskSpec, Task/Session/Run/Turn identities, WorkerControl, IntegrationEvent, TraceManifest and ArtifactRef production contracts with schema/round-trip tests.
- [x] 1.4 Add contract tests that reject client tenant/user, Docker, host-path, resource-number, credential and provider-fallback overrides.
- [x] 1.5 Add the SingleUserLocalProfile configuration with fixed tenant/actor, loopback listeners and a test-only explicit WorkspaceRevision bootstrap fixture.
- [x] 1.6 Add migration, unit, integration, race, vet, formatting, generated-protocol and secret-scan gates to the root developer workflow.

## 2. PostgreSQL Task And Session Persistence

- [x] 2.1 Add migrations for Task, TaskTransition, WorkerSession, WorkerRun, AgentTurn, RuntimeInstance, allocation/session queues, quota, ScheduledCommand, idempotency result and Outbox tables.
- [x] 2.2 Implement domain state machines and exhaustive legal/illegal transition tests for lightweight Task, WorkerSession, WorkerRun and AgentTurn.
- [x] 2.3 Implement PostgreSQL repositories with expected-revision compare-and-swap and transaction-scoped owner stream sequence allocation.
- [x] 2.4 Implement direct-create readiness and ExplicitTaskCreationIntent generation bound to actor, TaskSpec digest, Workspace and trigger-policy revision without invoking Assistant.
- [x] 2.5 Implement CreateTask admission so active intent consumption, Task, initial transition, allocation request, idempotency result and Outbox commit atomically.
- [x] 2.6 Add duplicate-response-loss, stale-intent and idempotency-content-conflict integration tests for CreateTask.
- [x] 2.7 Implement the polling Outbox dispatcher and idempotent consumer receipt handling with duplicate, gap and stream-integrity tests.
- [x] 2.8 Implement WorkerSession continuity quota 10 and stable SessionAllocationQueue FIFO with concurrent allocation tests that prove the limit cannot be exceeded.
- [x] 2.9 Implement per-Session FIFO, single-use dispatch permits and one-active-top-level-Run enforcement with concurrent dispatch tests.
- [x] 2.10 Implement exact Session reuse validation that preserves WorkingCopy/Agent state and rejects inferred, cross-Workspace, busy-policy, closed and expired reuse.
- [x] 2.11 Implement `auto|manual` acceptance, delivered/accept/rework semantics and standalone post-terminal follow-up Run behavior with state-machine tests.
- [x] 2.12 Implement fenced ScheduledCommand polling for fixed 24-hour AgentTurn deadline, 48-hour Session idle and 14-day absolute expiry using server time.
- [x] 2.13 Add Backend-restart integration tests for pending Outbox, allocation, active Task/Session reads, idempotency results and due ScheduledCommands.

## 3. Production Worker Runtime Core

- [x] 3.1 Define RuntimeProvisioner, WorkerControl, AgentAdapter, ObjectStore, ModelObservation and clock ports with fake adapters for application tests.
- [x] 3.2 Add server-owned `local_standard_v1`, `deepseek-codex-openai-v1` and `kimi-codex-anthropic-v1` profiles with immutable revision/digest validation.
- [x] 3.3 Implement server-derived Session storage materialization for WorkingCopy, agent state and trace spool under the configured Switchback data root.
- [x] 3.4 Adapt the Spike Docker RuntimeProvisioner into production ownership and prove no socket, privileged mode, host network, host port or arbitrary mount reaches Worker.
- [x] 3.5 Implement root-owned per-Runtime bootstrap/model-secret resources and same-UID boundary tests for LiteLLM UID `10001` and Codex/Tool UID `10002`.
- [x] 3.6 Build the production Worker image and Shim with exact ACP/Codex/LiteLLM pins and reproducible image provenance checks.
- [x] 3.7 Adapt the typed ACP initialize/auth/session/prompt path, raw-frame tee, proxy-token redaction and capability snapshot into the production CodexAdapter.
- [x] 3.8 Implement the authenticated gRPC WorkerControl stream with command/event id, sequence, ACK, Runtime generation and stale-event fencing tests.
- [x] 3.9 Implement RuntimeCoordinator start flow so the database records Runtime/Run/Turn ownership before any first prompt, Tool call or WorkingCopy mutation.
- [x] 3.10 Implement resident generation 1 lifecycle across interactive/running/waiting_user/idle states and prove stage completion does not pause or replace Runtime.
- [x] 3.11 Implement fail-closed generation 1 Runtime-loss handling that preserves evidence, fences dispatch and never creates an implicit replacement Runtime.
- [x] 3.12 Implement exact Runtime/secret cleanup and reconciliation with injected Docker delete failure and zero-unreported-residue tests.

## 4. Trace, Artifact And Langfuse

- [x] 4.1 Implement LocalFileObjectStore with Tenant/User/Workspace-derived keys, atomic writes, checksum verification and path-containment tests.
- [x] 4.2 Adapt Turn raw/normalized JSONL, report, patch and TraceManifest finalization with immutable WorkerSession/Run/Turn/Runtime provenance.
- [x] 4.3 Implement Artifact metadata persistence so only checksum-verified ObjectStore content can produce a visible ArtifactRef.
- [x] 4.4 Implement temporary upload/finalize and orphan reconciliation across PostgreSQL/ObjectStore failure boundaries.
- [x] 4.5 Adapt Langfuse export and exact Trace verification while preserving execution outcome when observability projection fails.
- [x] 4.6 Add known-token scans covering ObjectStore, application logs, sanitized diagnostics and Langfuse query results.
- [x] 4.7 Add checksum mismatch, missing object, duplicate finalize, wrong identity and Langfuse-unavailable integration tests.

## 5. Gateway, Projection And Frontier

- [x] 5.1 Implement loopback Gateway CreateTask, Task accept/rework/cancel and exact WorkerSession follow-up/queue/interrupt/steer/approval command endpoints.
- [x] 5.2 Require command idempotency key, exact target and expected revision and add stale-target/non-disclosing error tests.
- [x] 5.3 Add PostgreSQL projection tables and an idempotent projector for TaskSummaryView, WorkerDetailView, canonical ArtifactDetailView and command status.
- [x] 5.4 Implement complete ProjectionSnapshot endpoints with schema version, epoch, cursor and projection version.
- [x] 5.5 Implement typed SSE ProjectionDelta replay using `Last-Event-ID`, bounded retention and explicit `resync_required`/`client_upgrade_required` control events.
- [x] 5.6 Implement durable DisplayCheckpoint and bounded TransientDisplayDelta lanes without exposing hidden reasoning or advancing durable cursor for chunks.
- [x] 5.7 Add snapshot/subscribe race, reconnect, duplicate delta, cursor eviction, stale epoch, projection lag and access-clear integration tests.
- [x] 5.8 Add structured sanitized error mapping so provider, ACP, Artifact and cleanup failures expose stable codes and safe next actions only.

## 6. WorkerDetail Frontend

- [x] 6.1 Create the minimal React/Vite Worker UI with Task index, WorkerDetail route and canonical ArtifactDetail route using existing visual conventions.
- [x] 6.2 Implement one ClientViewReplica per View identity with atomic snapshot replace, checked delta apply, transient overlay and command-pending state.
- [x] 6.3 Build WorkerDetail status, Run/Turn timeline, live activity, stage report and waiting-user/approval sections from projection data.
- [x] 6.4 Build report rendering and patch diff review with source Run/Turn, checksum/completeness state and download action.
- [x] 6.5 Build exact-Session chat controls for follow-up, queue, interrupt and steer with unsupported/stale command feedback.
- [x] 6.6 Build once/session approval controls constrained to the projected capability/target/expiry and remove actions immediately when projection revision advances.
- [x] 6.7 Add component and browser tests for live update, reconnect/resync, Task terminal with Session live, Artifact integrity failure and command retry.
- [x] 6.8 Verify desktop and mobile Playwright screenshots for text fit, stable controls, no overlap and usable patch/chat layouts.

## 7. End-To-End Acceptance And Closeout

- [x] 7.1 Add a local Compose profile for PostgreSQL, Backend, Langfuse and dynamically provisioned sibling Workers without adding RocketMQ or Kubernetes.
- [x] 7.2 Add a fake-Agent vertical E2E that covers direct TaskSpec, persistence, allocation, projection, report/patch, manual accept and exact-Session follow-up.
- [x] 7.3 Add a Backend restart E2E proving committed Task/Session/Run/Artifact and SSE projection state remain readable without duplicate execution.
- [x] 7.4 Run the production path against real Codex for two Turns with one selected Kimi profile and one selected DeepSeek profile, with no mid-Turn fallback.
- [x] 7.5 Add executable invalid-credential, checksum-mismatch, Langfuse-unavailable, quota-full, deadline, Runtime-loss and cleanup-failure variants with dual execution/acceptance assertions.
- [x] 7.6 Prove known-token absence, Fixture/source immutability, exact identity cardinality and zero managed container/secret residue after success and failure.
- [x] 7.7 Document production architecture, local operation, data locations, migration/rollback, failure diagnosis and the explicit list of deferred hardening changes.
- [x] 7.8 Run full format, schema, unit, race, vet, integration, browser, real-provider and OpenSpec validation gates and record fresh machine-readable acceptance evidence.
- [x] 7.9 Review every requirement scenario against tests, resolve all P0/P1 findings and keep Spike/runtime credentials/evidence ignored before final commits.
