# Langfuse Full Input And Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development or execute this plan inline task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make new Switchback Langfuse traces show the user Turn input, stage report output, and complete model request/response content.

**Architecture:** LiteLLM exports the complete model request body and serializable provider response through the authenticated loopback model-observation sink. The Worker control path preserves these JSON values unchanged. The Langfuse projector maps Turn content/report to trace and span input/output, and model request/response to generation input/output. Transport credentials, HTTP headers, environment variables, and callback internals are not model content and remain outside the payload.

**Tech Stack:** Python LiteLLM callback, Go Worker Runtime, Langfuse legacy ingestion API, PostgreSQL-backed local product, Docker Compose.

## Global Constraints

- Existing authoritative Trace JSONL and ObjectStore evidence remain unchanged.
- Complete content means no semantic redaction, reasoning removal, or application-level truncation.
- HTTP headers, provider credentials, proxy credentials, environment variables, and callback internals must never be copied into Langfuse input/output.
- The loopback observation endpoint keeps a 32 MiB hard body limit to protect the runtime from malformed payloads.
- Existing model generations cannot be backfilled because the prior implementation discarded their request/response bodies.
- Execution success remains independent from Langfuse projection success.

---

### Task 1: Capture Complete LiteLLM Model Content

**Files:**
- Modify: `images/worker/litellm/switchback_callback.py`
- Test: `images/worker/litellm/test_switchback_callback.py`

**Interfaces:**
- Consumes: LiteLLM callback `proxy_server_request.body`, nested `litellm_params.proxy_server_request.body`, exact `input`/`messages` fallback, and `response_obj`.
- Produces: observation JSON fields `input` and `output`.

- [x] Add a failing callback test that expects the complete request body and serializable response while asserting headers are absent.
- [x] Run the callback test and confirm the missing fields fail.
- [x] Add defensive JSON conversion for mappings, model objects, sequences, primitives, and unsupported values.
- [x] Cover LiteLLM 1.93's nested request-body location and exact-input fallback.
- [x] Run the callback tests and confirm they pass.

### Task 2: Preserve Content Through Worker Control

**Files:**
- Modify: `internal/workerruntime/adapter/modelobs/server.go`
- Modify: `internal/workerruntime/application/ports.go`
- Modify: `internal/workerruntime/adapter/shimruntime/runtime.go`
- Test: `internal/workerruntime/adapter/modelobs/server_test.go`
- Test: `internal/workerruntime/adapter/shimruntime/runtime_test.go`
- Test: `internal/localproduct/runtime_runner_test.go`

**Interfaces:**
- Consumes: callback observation `input` and `output`.
- Produces: `application.ModelObservation.Input` and `.Output` as valid `json.RawMessage`.

- [x] Add failing receiver and shim round-trip assertions for exact JSON content.
- [x] Run the targeted Go tests and confirm the missing fields fail.
- [x] Add typed raw JSON fields and raise the authenticated loopback endpoint limit to 32 MiB.
- [x] Validate both fields are present and valid JSON before accepting an observation.
- [x] Run the targeted Go tests and confirm they pass.

### Task 3: Project Trace, Span, And Generation Content

**Files:**
- Modify: `internal/artifacttrace/application/observability.go`
- Modify: `internal/artifacttrace/adapter/langfuse/client.go`
- Modify: `internal/artifacttrace/adapter/langfuse/projector.go`
- Modify: `internal/localproduct/runtime_runner.go`
- Test: `internal/artifacttrace/adapter/langfuse/projector_test.go`
- Test: `internal/artifacttrace/application/observability_test.go`

**Interfaces:**
- Consumes: pending Turn content, finalized report object, and complete model observations.
- Produces: Langfuse trace/span `input` and `output`, generation `input` and `output`.

- [x] Add failing projector tests for exact input/output bodies and pre-ingestion known-token scanning.
- [x] Run the projector tests and confirm the missing fields fail.
- [x] Read the finalized report object and construct the completed Turn projection.
- [x] Add input/output fields to every Langfuse ingestion body and query verification type.
- [x] Scan the complete ingestion batch with the existing known-token scanner before HTTP export.
- [x] Run the artifact trace and local product tests and confirm they pass.

### Task 4: Document And Verify The Behavior

**Files:**
- Modify: `openspec/changes/m1-worker-delivery-vertical-slice/specs/runtime-execution-core/spec.md`
- Modify: `docs/adr/0010-langfuse-trace-export.md`
- Modify: `docs/architecture/m1-worker-delivery-operations.md`

**Interfaces:**
- Consumes: implemented behavior and local Compose profile.
- Produces: reviewable requirements and operator acceptance steps.

- [x] Document the complete-content contract, credential exclusion, and historical-data limit.
- [x] Run Python callback tests, targeted Go tests, and `make verify`.
- [x] Rebuild the Backend and Worker images with `make local-up`.
- [x] Submit one real Worker task and query the Langfuse Public API.
- [x] Verify the new trace, span, and generation all have non-null input/output without printing their contents or any credentials.
