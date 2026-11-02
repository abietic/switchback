# M1 Worker Runtime Spike Promotion Matrix

> Status: reviewed baseline for `m1-worker-delivery-vertical-slice` task 1.1. This matrix decides ownership; it does not authorize production packages to import `switchback.local/spikes/worker-runtime`.

## Decision Rules

| Decision | Meaning |
|---|---|
| `promote` | Preserve the proved behavior and tests while moving it under a production owner. Package names, configuration, and imports still become production-native. |
| `adapt` | Preserve selected invariants, then change contracts, lifecycle, persistence, or ownership before production use. |
| `rewrite` | Use the Spike only as executable evidence. Build the production component from its accepted domain contract. |
| `discard` | Do not move the component into a production runtime. Historical evidence may remain under `spikes/`. |

Every promoted or adapted component must first have a production contract test. No production package may import a Spike package, generated Spike protocol, Harness DTO, or in-memory state type.

## Production Ownership Matrix

| Spike component | Source evidence | Decision | Production owner | Promotion boundary |
|---|---|---|---|---|
| Identity generation and validation | `internal/ids` | `adapt` | shared production contracts | Replace Spike-specific prefixes and errors with versioned Task/Session/Run/Turn/Runtime value types; retain non-empty, canonical identity tests. |
| Agent profile parsing | `internal/agentprofile` | `adapt` | Worker Runtime domain | Replace the generic environment-selected route with immutable server-owned profile revisions and digests. |
| Evidence DTOs | `internal/evidence` | `adapt` | Artifact & Trace domain | Split TraceManifest and ArtifactRef contracts, add Tenant/User/Workspace and exact Run/Turn/Runtime provenance. |
| Exact-token redactor | `internal/redact` | `promote` | Artifact & Trace application | Preserve byte-safe exact replacement and tests; expose only production diagnostic markers and registered secret classes. |
| WorkerControl protocol | `api/workercontrol/v1` | `adapt` | Worker Runtime interface | Change package/version ownership, add channel epoch and complete command/event identity, then regenerate; never import Spike generated code. |
| Generated WorkerControl Go | `gen/workercontrol/v1` | `discard` | generated production protocol | Regenerate from the production proto and verify a clean generated-code diff. |
| WorkerControl authentication | `backend/internal/control/auth.go` | `adapt` | Worker Runtime adapter | Replace process-local credentials with persisted Runtime ownership and scoped bootstrap authentication. |
| WorkerControl envelope sequencing | `backend/internal/control/envelope.go` | `adapt` | Worker Runtime application | Preserve message id, sequence, ACK, generation fencing, duplicate detection, and content-conflict behavior. Persist owner cursors. |
| Worker connection registry | `backend/internal/control/registry.go` | `rewrite` | Worker Runtime application | The in-memory socket registry may be an ephemeral transport index only; PostgreSQL RuntimeInstance and channel ownership remain authoritative. |
| WorkerControl gRPC server | `backend/internal/control/server.go` | `adapt` | Worker Runtime gRPC adapter | Rebind to production commands/events, authenticated channel epoch, durable ACK application, and sanitized errors. |
| Session path derivation | `backend/internal/runtime/pathmap.go` | `promote` | Worker Runtime infrastructure | Preserve clean absolute path, containment, identity-derived layout, and no client host-path input. |
| WorkingCopy/state materializer | `backend/internal/runtime/materializer.go` | `adapt` | Worker Runtime infrastructure | Replace fixed Fixture ownership with explicit WorkspaceRevision bootstrap while preserving no-symlink, no-overwrite, UID, and rollback tests. |
| Docker host-path attestation | `backend/internal/runtime/docker.go` host verifier | `promote` | Worker Runtime Docker adapter | Preserve server root containment and Docker-host verification; keep the documented TOCTOU limitation visible. |
| Docker Runtime provisioner | `backend/internal/runtime/docker.go` | `adapt` | Worker Runtime Docker adapter | Accept only production RuntimeSpec, persist container identity before dispatch, enforce fixed resources, no socket/privileged/host network/host port, and exact cleanup. |
| Runtime provisioner interface | `backend/internal/runtime/provisioner.go` and `backend/internal/service/runtime_provisioner.go` | `rewrite` | Worker Runtime application port | Define the port from production RuntimeInstance lifecycle; remove Spike scheduler handoff and process-local lifecycle maps. |
| Runtime secret materialization | `backend/internal/service/runtime_provisioner.go` supplemental resolver plus Docker secret volume | `adapt` | Worker Runtime secret infrastructure | Derive one Runtime resource from server profile, preserve root ownership and UID boundaries, persist exact cleanup identity, and remove environment-selected provider ownership. |
| Local ObjectStore | `backend/internal/objectstore/local.go` | `adapt` | Artifact & Trace infrastructure | Add Tenant/User/Workspace-derived keys, temporary/finalize lifecycle, checksum-on-read, path containment, and orphan reconciliation. |
| Internal evidence upload HTTP | `backend/internal/httpapi/internal_upload.go` | `adapt` | Artifact & Trace HTTP adapter | Replace Spike bearer/run lookup with Runtime-scoped upload authorization, resumable finalize, immutable identity, and stable sanitized errors. |
| Public Spike Harness HTTP API | `backend/internal/httpapi/handler.go` | `discard` | Gateway and acceptance tests | Do not expose create-run, summary, or cleanup Harness DTOs. Production commands use Task/Session exact identity, revision, and idempotency. |
| In-memory run state | `backend/internal/runstate` | `discard` | PostgreSQL repositories | Task, Session, Run, Turn, Runtime, queue, idempotency, Outbox, and scheduler state must be durable and transactionally owned. |
| Spike coordinator | `backend/internal/service/coordinator.go` | `rewrite` | Task Orchestration and Worker Runtime applications | Split Task admission/allocation from Runtime coordination. Do not retain process-local scheduler handoffs, maps, or synchronous Run ownership. |
| Langfuse HTTP client | `backend/internal/langfuse/client.go` | `adapt` | Artifact & Trace projection adapter | Use production configuration and sanitized retries; Langfuse remains derived and never changes execution outcome. |
| Langfuse exporter | `backend/internal/langfuse/exporter.go` and `service/langfuse_projection.go` | `adapt` | Artifact & Trace projection adapter | Export finalized production Trace/Run/Turn/model identities from durable work, with idempotent projection status. |
| Langfuse exact verifier | `backend/internal/langfuse/verifier.go` | `promote` | acceptance test support | Preserve exact cardinality and metadata assertions as a production acceptance verifier, not a request-path dependency. |
| Shim composition root | `worker/cmd/worker-shim` | `rewrite` | Worker Runtime executable | Compose production WorkerControl, ACP, trace, upload, process, and LiteLLM lifecycle from versioned bootstrap; remove fixed two-Turn assumptions. |
| Typed ACP client and adapter | `worker/internal/acp/client.go` and `adapter.go` | `adapt` | Worker Runtime Codex adapter | Preserve pinned SDK initialize/auth/session/prompt flow; add production capability policy, exact AgentSession ownership, cancellation/steer outcomes, and no TUI parsing. |
| ACP raw frame tee | `worker/internal/acp/rawtee.go` | `promote` | Artifact & Trace recorder adapter | Preserve pre-decode bounded JSONL capture and unknown optional update tolerance; apply production redaction only to persisted copies. |
| Shim WorkerControl client | `worker/internal/control` | `adapt` | Worker Runtime control adapter | Bind production channel epoch, persisted ACK cursor, Runtime generation, reconnect ownership, and stable command results. |
| Trace recorder | `worker/internal/trace` | `adapt` | Artifact & Trace application | Preserve append-only raw/normalized JSONL behavior; add segment lifecycle, immutable provenance, bounded writes, and finalize contract. |
| Evidence finalizer | `worker/internal/evidence` | `adapt` | Artifact & Trace application | Finalize only verified objects and manifest; remove Spike two-Turn cardinality assumptions and emit production ArtifactRefs. |
| Git patch snapshot | `worker/internal/gitpatch` | `promote` | Artifact & Trace patch adapter | Preserve baseline-relative patch generation and deterministic tests; bind output to WorkingCopy checkpoint and source Turn. |
| Evidence upload client | `worker/internal/upload` | `adapt` | Artifact & Trace upload adapter | Add resumable temporary upload/finalize and production auth while retaining checksum and non-disclosing error behavior. |
| Model observation receiver | `worker/internal/modelobs` | `adapt` | Worker Runtime model observation adapter | Bind observations to AgentProfile revision and exact Run/Turn; validate cardinality and treat export as derived. |
| Child process supervisor | `worker/internal/process` | `adapt` | Worker Runtime Shim | Preserve UID-separated lifecycle and termination semantics for ACP/LiteLLM children. Do not infer the deferred SessionProcess product from this helper. |
| LiteLLM callback normalization | `worker/litellm/switchback_callback.py` | `promote` | LLMProxy runtime adapter | Preserve Responses-history reasoning/tool pair normalization and fail-closed malformed tool ids with the Python regression suite. |
| LiteLLM configuration template | `worker/litellm/config.yaml` | `adapt` | LLMProxy runtime adapter | Render from one immutable server profile; no client route, credential, or mid-Turn fallback controls. |
| Worker image definition and lockfile | `images/worker` | `adapt` | Worker Runtime image build | Keep exact ACP/Codex/LiteLLM pins and UID layout; produce production image provenance and reproducibility checks. |
| Backend Spike image | `images/backend` | `discard` | production Backend image build | Build from the root Go module and production composition; do not copy the Spike binary or module. |
| Buf generation configuration | `buf.yaml`, `buf.gen.yaml` | `adapt` | production protocol tooling | Move to root tooling, pin generators, and gate generated output. |
| Image digest lock | `image-lock.json` and `scripts/lock-images` | `adapt` | deployment tooling | Produce a production lock for Backend, Worker, PostgreSQL, and Langfuse inputs with source provenance. |
| Preflight and deployment checks | `scripts/preflight`, `scripts/test-deployment` | `adapt` | local deployment tooling | Retain fail-before-create resource and topology checks; use production Compose names and fixed local profile. |
| Fixture baseline and tree digest | `fixtures/go-repository`, `scripts/tree-digest`, `scripts/check-fixture-baseline` | `promote` | acceptance test support | Preserve immutable source verification; represent the bootstrapped WorkspaceRevision explicitly in tests. |
| Fake ACP and fake model | `test/fakes` | `adapt` | integration test support | Rebind to production protocol and AgentAdapter seams; preserve deterministic success/failure behavior. |
| Fault injection helpers | checksum proxy, cleanup proxy, invalid credential and Langfuse variants in `test/e2e` and scripts | `promote` | acceptance test support | Preserve dual execution/acceptance assertions and exact cleanup fallback; remove Harness summary as a production API. |
| Spike unit and integration tests | package tests and `test/integration` | `adapt` | production package/integration suites | Port tests to production public seams and retain negative isolation, identity, checksum, redaction, and cleanup assertions. |
| Real two-Turn Harness | `scripts/run-e2e` and `test/e2e` | `adapt` | end-to-end acceptance tooling | Drive Gateway/Frontier and durable state, retain Kimi/DeepSeek two-Turn evidence checks, and remove the 45-minute Spike timeout from product semantics. |
| Spike Compose topology | `compose.yaml`, `compose.integration.yaml` | `discard` | production local Compose profile | Author a new profile with PostgreSQL, Backend, Langfuse, and sibling Workers; do not reuse Spike project names, credential generation, or lifecycle ownership. |
| Spike Makefile | `spikes/worker-runtime/Makefile` | `adapt` | root developer workflow | Keep useful gate intent while adding root production unit, race, vet, migration, integration, browser, protocol, and secret-scan targets. |
| Spike README and acceptance summary | `README.md` and ignored `acceptance/summary.json` | `discard` | production operations docs and evidence schema | Keep as historical Spike evidence. Production operation docs and machine-readable acceptance records use production identities and gates. |

## Review Checklist

- [x] Every tracked Spike code, protocol, image, deployment, script, fixture, and test area is represented by a row above.
- [x] Each reused behavior has exactly one production owner and an explicit destination boundary.
- [x] No row authorizes a production import from the Spike Go module or generated protocol.
- [x] In-memory state, Harness API, Spike Compose ownership, generated code, and Spike Backend image are explicitly discarded.
- [x] Langfuse remains a derived projection; ObjectStore evidence and PostgreSQL business state keep distinct ownership.
- [x] Client-controlled identity, Docker, host path, resource, credential, and provider fallback inputs remain outside every promotion boundary.
- [x] Recovery, long-poll fallback, full Linux egress, PreviewProxy, and managed SessionProcess remain deferred rather than being inferred from Spike helpers.

## Implementation Order

1. Define production contracts and rejection tests before moving adapter behavior.
2. Establish the root Go module and production composition without Spike imports.
3. Implement PostgreSQL owners and application ports before adapting Docker/ACP/ObjectStore/Langfuse.
4. Port acceptance fixtures only after the production seams exist.
5. Keep `spikes/worker-runtime` runnable as regression evidence until the production E2E satisfies the same invariants.
