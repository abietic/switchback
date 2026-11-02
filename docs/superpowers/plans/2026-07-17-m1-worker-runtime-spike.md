# M1 Worker Runtime Spike Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 `spikes/worker-runtime/` 中完成第一个真实 Switchback Worker Runtime tracer bullet：Backend 通过 Docker Engine API 动态创建隔离 Worker，Shim 通过 ACP 控制真实 Codex，在同一 Session 和 WorkingCopy 中连续执行两个 AgentTurn，生成 report/patch/权威 JSONL，并由 Backend 将派生观测写入并反查自托管 Langfuse。

**Architecture:** Spike 是独立 Go module。Compose 只管理 Backend 与 Langfuse 六服务栈；Backend 内 RuntimeSupervisor 使用 Docker Socket 创建 sibling Worker。Worker 内可信 root Shim 以不同 Unix identity 启动 LiteLLM 与 Codex/Tool 子进程，通过 gRPC 双向流接收命令、通过 HTTP 上传大对象。LocalFileObjectStore JSONL 是事实源，Langfuse 是可重建投影。

**Tech Stack:** Go 1.26.4、gRPC/Protobuf、`github.com/coder/acp-go-sdk@v0.13.5`、`@agentclientprotocol/codex-acp@1.1.2`、`@openai/codex@0.144.1`、LiteLLM `v1.93.0`、Docker Engine API 1.55、Docker Compose、Langfuse `v3.217.0`。

## Global Constraints

- 实现必须留在 `spikes/worker-runtime/`；不得提前创建生产 DDD module，也不得把 Spike 类型宣传成稳定公共协议。
- 只实现 gRPC WorkerControl 主路径与 HTTP evidence upload；不实现 long-poll fallback、runtime recovery、ChannelEpoch failover、48h/14d timer、24h production deadline、配额、PreviewProxy 或完整网络 egress policy。
- 不实现 Assistant、Delivery、EventCenter、Frontier/Frontend、Evolution、Inspiration、OpenViking、GitHub import、remote Git、Skill/MCP marketplace 或 PostgreSQL Task orchestration。
- 真实验收必须使用 ACP 控制 Codex，禁止裸启动 Codex CLI 并解析 TUI/stdout。
- Backend 是 Docker Socket、Worker bootstrap secret、上游模型 secret 和 Langfuse secret 的可信控制面；Codex/Tool execution identity 不得读取上游模型 secret、Docker Socket 或 Langfuse secret。
- Worker root Shim 属于当前 co-located LiteLLM 方案的显式可信边界。独立 LiteLLM sidecar 是紧随其后的备选，不在本计划中实现。
- 客户端只能提交 goal、acceptance criteria、constraints 和第二轮 message；image、network、mount、identity、model route 与 credential path 全部由服务端配置产生。
- `execution_outcome` 与 `spike_acceptance` 分开：Langfuse 失败不能改写已完成的 Agent 执行结果，但必须让 Spike 验收失败。
- 所有任务按 TDD 顺序执行：先写失败测试并确认失败，再写最小实现并确认通过，最后提交。不得把多个任务积攒成一个大提交。
- 每个提交只包含当前任务列出的文件。发现设计外需求时先更新本计划或设计文档，不顺手扩张范围。
- 当前开发机状态：Go/Node/npm 可用；`buf`、`protoc` 未安装；Docker CLI 可用但 daemon 未启动。Task 1 可直接执行；Task 2 的协议生成开始使用固定 Buf 容器，因此继续前需要启动 Docker daemon，但不要求宿主安装 Buf/Protoc。

---

## Source Of Truth

- [M1 Worker Runtime Spike design](../../architecture/m1-worker-runtime-spike.md)
- [M1 Worker Runtime Spike execution architecture](../../architecture/m1-worker-runtime-spike-execution.md)
- [M1 Worker Runtime Spike follow-up roadmap](../../architecture/m1-worker-runtime-post-spike-roadmap.md)
- [Langfuse self-hosted research](../../research/langfuse-self-hosted-runtime-spike.md)
- [ACP/Codex/LiteLLM runtime notes](../../research/codex-acp-litellm-runtime-notes.md)
- [ACP liveness and recovery research](../../research/acp-codex-runtime-liveness-and-session-recovery.md)
- [ADR 0059: Backend-owned RuntimeSupervisor](../../adr/0059-embed-m1-runtime-supervisor-in-switchback-backend.md)
- [ADR 0060: M1 Worker network boundary](../../adr/0060-allow-public-web-egress-and-deny-private-networks-in-m1.md)
- [ADR 0061: resident Worker runtime](../../adr/0061-keep-m1-runtime-resident-until-session-terminal.md)

Current external baselines verified before writing this plan:

- [ACP Go SDK v0.13.5 example client](https://github.com/coder/acp-go-sdk/tree/v0.13.5/example/client)
- [codex-acp v1.1.2 gateway authentication](https://github.com/agentclientprotocol/codex-acp/tree/v1.1.2)
- [Moby client v0.5.0](https://pkg.go.dev/github.com/moby/moby/client)
- [LiteLLM v1.93.0](https://github.com/BerriAI/litellm/releases/tag/v1.93.0)
- [Langfuse v3.217.0](https://github.com/langfuse/langfuse/releases/tag/v3.217.0)
- [Langfuse ingestion endpoint at v3.217.0](https://github.com/langfuse/langfuse/blob/v3.217.0/web/src/pages/api/public/ingestion.ts)
- [Buf v1.71.0](https://github.com/bufbuild/buf/releases/tag/v1.71.0)

## Target File Structure

```text
spikes/worker-runtime/
  Makefile
  README.md
  go.mod
  go.sum
  buf.yaml
  buf.gen.yaml
  compose.yaml
  image-lock.json
  api/workercontrol/v1/worker_control.proto
  gen/workercontrol/v1/worker_control.pb.go
  gen/workercontrol/v1/worker_control_grpc.pb.go
  internal/evidence/types.go
  internal/ids/ids.go
  internal/redact/redactor.go
  backend/cmd/spike-backend/main.go
  backend/internal/config/config.go
  backend/internal/control/auth.go
  backend/internal/control/registry.go
  backend/internal/control/server.go
  backend/internal/httpapi/handler.go
  backend/internal/httpapi/internal_upload.go
  backend/internal/langfuse/client.go
  backend/internal/langfuse/exporter.go
  backend/internal/langfuse/verifier.go
  backend/internal/objectstore/local.go
  backend/internal/runstate/model.go
  backend/internal/runstate/store.go
  backend/internal/runtime/docker.go
  backend/internal/runtime/materializer.go
  backend/internal/runtime/pathmap.go
  backend/internal/runtime/provisioner.go
  backend/internal/service/coordinator.go
  worker/cmd/worker-shim/main.go
  worker/internal/acp/adapter.go
  worker/internal/acp/client.go
  worker/internal/acp/rawtee.go
  worker/internal/evidence/finalizer.go
  worker/internal/gitpatch/snapshot.go
  worker/internal/modelobs/server.go
  worker/internal/process/supervisor.go
  worker/internal/trace/recorder.go
  worker/internal/upload/client.go
  worker/litellm/config.yaml
  worker/litellm/switchback_callback.py
  worker/litellm/test_switchback_callback.py
  images/backend/Dockerfile
  images/worker/Dockerfile
  images/worker/package.json
  images/worker/package-lock.json
  fixtures/go-repository/go.mod
  fixtures/go-repository/counter/counter.go
  fixtures/go-repository/counter/counter_test.go
  scripts/check-fixture-baseline
  scripts/generate-env
  scripts/lock-images
  scripts/preflight
  scripts/run-e2e
  scripts/tree-digest
  scripts/verify-langfuse
  test/fakes/cmd/fake-acp/main.go
  test/fakes/cmd/fake-openai/main.go
  test/integration/runtime_test.go
  test/e2e/runtime_test.go
```

All runtime files live below ignored `spikes/worker-runtime/.spike-data/`. The root `.gitignore` must ignore that exact tree while continuing to track `.env.example` files elsewhere.

## Core Interfaces

These interfaces are intentionally narrow. Implementations may add private helpers but must not add another lifecycle owner.

```go
// backend/internal/runtime/provisioner.go
type ProvisionRequest struct {
	WorkerSessionID string
	WorkerRunID     string
	RuntimeID       string
	RuntimeGeneration uint32
	WorkerToken     string
	WorkingCopy     PathPair
	AgentState      PathPair
	TraceSpool      PathPair
	Bootstrap       PathPair
	ModelSecret     PathPair
}

type RuntimeInstance struct {
	RuntimeID   string
	ContainerID string
	Generation  uint32
}

type Provisioner interface {
	Start(context.Context, ProvisionRequest) (RuntimeInstance, error)
	Inspect(context.Context, RuntimeInstance) (RuntimeInspection, error)
	StopAndRemove(context.Context, RuntimeInstance) error
}
```

```go
// backend/internal/control/registry.go
type CommandBus interface {
	WaitReady(context.Context, string) error
	InitializeSession(context.Context, Identity) error
	StartAgentTurn(context.Context, Identity, string) error
	CancelAgentTurn(context.Context, Identity, string) error
	ShutdownRuntime(context.Context, Identity) error
}
```

```go
// worker/internal/acp/adapter.go
type Adapter interface {
	Start(context.Context, StartConfig) (CapabilitySnapshot, error)
	NewSession(context.Context, string) (string, error)
	Prompt(context.Context, string, string) (PromptResult, error)
	Close(context.Context) error
}
```

```go
// backend/internal/langfuse/client.go
type IngestionClient interface {
	Ingest(context.Context, []IngestionEvent) error
	GetTrace(context.Context, string) (TraceView, error)
}
```

## Task Dependency Graph

```text
1 foundation
  -> 2 protobuf/envelope
  -> 3 lifecycle state
  -> 4 paths/fixture/object store/redaction
  -> 5 HTTP API/coordinator shell
  -> 6 gRPC control
  -> 7 Docker provisioner
  -> 8 LiteLLM/process boundary
  -> 9 ACP/Codex adapter
  -> 10 evidence/artifacts
  -> 11 Langfuse export/verify
  -> 12 images/Compose/preflight
  -> 13 fake full-path integration
  -> 14 real two-turn E2E and closeout
```

## Task 1: Scaffold The Isolated Module And Broken Fixture

**Files:**

- Modify: `.gitignore`
- Create: `spikes/worker-runtime/go.mod`
- Create: `spikes/worker-runtime/Makefile`
- Create: `spikes/worker-runtime/README.md`
- Create: `spikes/worker-runtime/internal/ids/ids.go`
- Create: `spikes/worker-runtime/internal/ids/ids_test.go`
- Create: `spikes/worker-runtime/fixtures/go-repository/go.mod`
- Create: `spikes/worker-runtime/fixtures/go-repository/counter/counter.go`
- Create: `spikes/worker-runtime/fixtures/go-repository/counter/counter_test.go`
- Create: `spikes/worker-runtime/scripts/check-fixture-baseline`

- [ ] **Step 1: Write failing ID tests and the intentionally failing Fixture**

`ids_test.go` must require UUID-shaped non-empty, unique values. The Fixture must contain a real defect, not a skipped test:

```go
package counter

func Add(current, delta int) int {
	return current - delta // Intentional Spike fixture defect.
}
```

`counter_test.go` must expect `Add(3, 2) == 5`. `scripts/check-fixture-baseline` succeeds only when `go test ./...` in the Fixture fails and output contains `want 5`.

- [ ] **Step 2: Run the tests and confirm the intended failures**

Run:

```bash
cd spikes/worker-runtime
go test ./internal/ids
./scripts/check-fixture-baseline
```

Expected: ID tests fail because implementation is absent; the baseline script passes because the Fixture fails for the known defect.

- [ ] **Step 3: Add the minimal module and ID implementation**

Use this dependency baseline:

```go
module switchback.local/spikes/worker-runtime

go 1.26.0

require github.com/google/uuid v1.6.0
```

`ids.New()` wraps `uuid.NewString()`. Root `go test ./...` must not enter the nested Fixture module.

The root `.gitignore` addition is exactly:

```gitignore
spikes/worker-runtime/.spike-data/
```

- [ ] **Step 4: Verify foundation commands**

Run:

```bash
cd spikes/worker-runtime
go mod tidy
go test ./...
./scripts/check-fixture-baseline
git check-ignore .spike-data/example
```

Expected: all root-module tests pass; Fixture baseline check passes; `.spike-data/example` is ignored.

- [ ] **Step 5: Commit the foundation**

```bash
git add .gitignore spikes/worker-runtime
git commit -m "spike: scaffold worker runtime fixture"
```

## Task 2: Define And Generate WorkerControl Envelopes

**Files:**

- Modify: `docs/superpowers/plans/2026-07-17-m1-worker-runtime-spike.md` (human-approved Decision A amendment)
- Modify: `spikes/worker-runtime/go.mod`
- Create: `spikes/worker-runtime/buf.yaml`
- Create: `spikes/worker-runtime/buf.gen.yaml`
- Create: `spikes/worker-runtime/api/workercontrol/v1/worker_control.proto`
- Create: `spikes/worker-runtime/gen/workercontrol/v1/worker_control.pb.go`
- Create: `spikes/worker-runtime/gen/workercontrol/v1/worker_control_grpc.pb.go`
- Create: `spikes/worker-runtime/backend/internal/control/envelope.go`
- Create: `spikes/worker-runtime/backend/internal/control/envelope_test.go`
- Modify: `spikes/worker-runtime/Makefile`

- [ ] **Step 1: Write failing envelope validation tests**

Cover missing identity, generation zero, turn command without `agent_turn_id`, duplicate/lower `stream_sequence`, ACK beyond last peer sequence, and a valid RuntimeReady envelope.

- [ ] **Step 2: Run the focused test and confirm failure**

```bash
cd spikes/worker-runtime
go test ./backend/internal/control -run TestValidateEnvelope -v
```

Expected: compile failure because generated messages and validator do not exist.

- [ ] **Step 3: Add the versioned protobuf contract**

The service and envelope roots must be:

```proto
syntax = "proto3";

package switchback.spike.workercontrol.v1;

option go_package = "switchback.local/spikes/worker-runtime/gen/workercontrol/v1;workercontrolv1";

import "google/protobuf/timestamp.proto";

service WorkerControl {
  rpc Connect(stream WorkerEnvelope) returns (stream BackendEnvelope);
}

message Identity {
  string worker_session_id = 1;
  string worker_run_id = 2;
  string runtime_instance_id = 3;
  uint32 runtime_generation = 4;
  string agent_turn_id = 5;
}

message EnvelopeMeta {
  string message_id = 1;
  uint64 stream_sequence = 2;
  uint64 ack_sequence = 3;
  Identity identity = 4;
  google.protobuf.Timestamp sent_at = 5;
}

message BackendEnvelope {
  EnvelopeMeta meta = 1;
  oneof payload {
    InitializeSession initialize_session = 10;
    StartAgentTurn start_agent_turn = 11;
    CancelAgentTurn cancel_agent_turn = 12;
    ShutdownRuntime shutdown_runtime = 13;
  }
}

message WorkerEnvelope {
  EnvelopeMeta meta = 1;
  oneof payload {
    RuntimeReady runtime_ready = 10;
    AgentSessionReady agent_session_ready = 11;
    TurnStarted turn_started = 12;
    ACPUpdate acp_update = 13;
    ModelObservation model_observation = 14;
    TurnCompleted turn_completed = 15;
    TurnFailed turn_failed = 16;
    RuntimeDiagnostic runtime_diagnostic = 17;
  }
}

message InitializeSession {
  string working_copy = 1;
  string agent_state = 2;
  string trace_spool = 3;
  string agent_profile = 4;
}

message StartAgentTurn { string prompt = 1; }
message CancelAgentTurn { string reason = 1; }
message ShutdownRuntime { string reason = 1; }

message RuntimeReady { string shim_version = 1; }

message AgentSessionReady {
  string agent_session_id = 1;
  string protocol_version = 2;
  bytes capability_snapshot_json = 3;
}

message TurnStarted {
  google.protobuf.Timestamp started_at = 1;
}

message ACPUpdate {
  string kind = 1;
  bytes normalized_json = 2;
}

message ModelObservation {
  string model_observation_id = 1;
  string model = 2;
  string route = 3;
  google.protobuf.Timestamp started_at = 4;
  google.protobuf.Timestamp ended_at = 5;
  int64 latency_ms = 6;
  int64 input_tokens = 7;
  int64 output_tokens = 8;
  int64 total_tokens = 9;
  double cost_usd = 10;
  string status = 11;
  string error_kind = 12;
  bytes metadata_json = 13;
}

message EvidencePointer {
  string kind = 1;
  string object_path = 2;
  string sha256 = 3;
  int64 size_bytes = 4;
}

message TurnCompleted {
  string agent_session_id = 1;
  repeated EvidencePointer evidence = 2;
}

message TurnFailed {
  string code = 1;
  string detail = 2;
  repeated EvidencePointer partial_evidence = 3;
}

message RuntimeDiagnostic {
  string level = 1;
  string code = 2;
  string detail = 3;
}
```

Identity remains exclusively in `EnvelopeMeta`; do not duplicate Session/Run/Runtime/Turn IDs inside each payload. `agent_session_id` is an ACP result rather than a transport routing identity: it is carried by `AgentSessionReady` and `TurnCompleted`; `TurnFailed` carries only its failure fields and partial evidence. Byte fields contain UTF-8 JSON that is schema-validated before use; they are bounded to 256 KiB per gRPC envelope.

- [ ] **Step 4: Generate with pinned tools and implement validation**

`buf.gen.yaml` uses remote plugins `buf.build/protocolbuffers/go:v1.36.11` and `buf.build/grpc/go:v1.6.2`. `make proto` runs `bufbuild/buf:1.71.0` in Docker and writes generated files below `gen/workercontrol/v1/`. Add:

```yaml
# buf.yaml
version: v2
modules:
  - path: api
lint:
  use: [STANDARD]
  except:
    - PACKAGE_DIRECTORY_MATCH
    - SERVICE_SUFFIX
    - RPC_REQUEST_STANDARD_NAME
    - RPC_RESPONSE_STANDARD_NAME
breaking:
  use: [FILE]
```

These four exceptions are narrow and intentional: the accepted binding fixes
the current `api/workercontrol/v1` source path, the `WorkerControl` service
name, and the `WorkerEnvelope`/`BackendEnvelope` stream message names. They
preserve the protocol/API contract while retaining Buf `STANDARD` for all
other lint rules and `FILE` breaking-change detection. Do not rename or move
the protocol merely to satisfy these stylistic rules.

```yaml
# buf.gen.yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go:v1.36.11
    out: .
    opt: [module=switchback.local/spikes/worker-runtime]
  - remote: buf.build/grpc/go:v1.6.2
    out: .
    opt: [module=switchback.local/spikes/worker-runtime]
```

Pin generated-code runtimes in `go.mod`:

```go
require (
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)
```

```go
func ValidateInbound(meta *workercontrolv1.EnvelopeMeta, payloadKind PayloadKind, cursor Cursor) error
```

The validator is pure: it validates shape and monotonicity but does not mutate the cursor.

- [ ] **Step 5: Verify generation determinism and tests**

```bash
cd spikes/worker-runtime
make proto
make proto-lint
go test ./backend/internal/control -v
make proto-check
```

Expected: validator tests pass; a second generation produces no Git diff.

- [ ] **Step 6: Commit protocol only**

```bash
git add spikes/worker-runtime/api spikes/worker-runtime/gen spikes/worker-runtime/buf.yaml spikes/worker-runtime/buf.gen.yaml spikes/worker-runtime/backend/internal/control spikes/worker-runtime/go.mod spikes/worker-runtime/go.sum spikes/worker-runtime/Makefile
git commit -m "spike: define worker control protocol"
```

## Task 3: Implement The In-Memory Spike Lifecycle

**Files:**

- Create: `spikes/worker-runtime/backend/internal/runstate/model.go`
- Create: `spikes/worker-runtime/backend/internal/runstate/model_test.go`
- Create: `spikes/worker-runtime/backend/internal/runstate/store.go`
- Create: `spikes/worker-runtime/backend/internal/runstate/store_test.go`
- Create: `spikes/worker-runtime/internal/evidence/types.go`

- [ ] **Step 1: Write table-driven transition and concurrency tests**

The lifecycle is exactly:

```text
provisioning -> connecting -> ready -> turn_running -> idle
idle -> turn_running -> completed
any nonterminal -> failed -> diagnostic_hold -> shutting_down
completed -> shutting_down
```

Tests must prove: one active turn maximum, Turn 2 cannot start before Turn 1 completes, runtime generation is fixed to `1`, same `agent_session_id` is retained, and execution/acceptance outcomes are independently writable.

- [ ] **Step 2: Confirm tests fail**

```bash
cd spikes/worker-runtime
go test ./backend/internal/runstate -race -v
```

- [ ] **Step 3: Implement model and synchronized store**

Use these public shapes inside the package:

```go
type ExecutionOutcome string
const (
	ExecutionPending ExecutionOutcome = "pending"
	ExecutionCompleted ExecutionOutcome = "completed"
	ExecutionFailed ExecutionOutcome = "failed"
)

type SpikeAcceptance string
const (
	AcceptancePending SpikeAcceptance = "pending"
	AcceptancePassed SpikeAcceptance = "passed"
	AcceptanceFailed SpikeAcceptance = "failed"
)

type Run struct {
	WorkerSessionID string
	WorkerRunID string
	RuntimeInstanceID string
	RuntimeGeneration uint32
	AgentSessionID string
	TraceID string
	State State
	ActiveTurnID string
	Turns []Turn
	ExecutionOutcome ExecutionOutcome
	SpikeAcceptance SpikeAcceptance
	Revision uint64
}
```

Every mutation accepts `expectedRevision` and returns a cloned snapshot. The store owns locking; callers never receive an internal pointer.

- [ ] **Step 4: Verify race safety and forbidden transitions**

```bash
cd spikes/worker-runtime
go test ./backend/internal/runstate -race -count=20
```

Expected: pass with no race reports.

- [ ] **Step 5: Commit lifecycle state**

```bash
git add spikes/worker-runtime/backend/internal/runstate spikes/worker-runtime/internal/evidence
git commit -m "spike: add worker run lifecycle"
```

## Task 4: Add Server-Owned Paths, Fixture Materialization, Object Store, And Redaction

**Files:**

- Create: `spikes/worker-runtime/backend/internal/runtime/pathmap.go`
- Create: `spikes/worker-runtime/backend/internal/runtime/pathmap_test.go`
- Create: `spikes/worker-runtime/backend/internal/runtime/materializer.go`
- Create: `spikes/worker-runtime/backend/internal/runtime/materializer_test.go`
- Create: `spikes/worker-runtime/backend/internal/objectstore/local.go`
- Create: `spikes/worker-runtime/backend/internal/objectstore/local_test.go`
- Create: `spikes/worker-runtime/internal/redact/redactor.go`
- Create: `spikes/worker-runtime/internal/redact/redactor_test.go`
- Create: `spikes/worker-runtime/scripts/tree-digest`

- [ ] **Step 1: Write path traversal, immutability, atomic write, and token tests**

Reject empty IDs, separators, `..`, symlinks escaping the data root, client-supplied absolute paths, and a host/container root mismatch. ObjectStore tests must prove temp-file-plus-rename behavior and SHA-256 manifest output. Redactor tests include upstream key, Worker token, LiteLLM proxy token, model sink token, Langfuse public key and Langfuse secret key.

- [ ] **Step 2: Confirm focused tests fail**

```bash
cd spikes/worker-runtime
go test ./backend/internal/runtime ./backend/internal/objectstore ./internal/redact -race -v
```

- [ ] **Step 3: Implement one relative namespace mapped to two roots**

```go
type PathPair struct {
	ContainerPath string
	HostPath      string
}

type PathMapper struct {
	ContainerDataRoot string
	HostDataRoot      string
}

func (m PathMapper) SessionPath(sessionID string, elems ...string) (PathPair, error)
```

The same sanitized relative path is joined to both roots. Runtime code writes through `ContainerPath`; Docker bind mounts use `HostPath`. No API DTO contains `PathPair`.

- [ ] **Step 4: Materialize a clean Git WorkingCopy**

`Materialize` copies the built-in Fixture into `sessions/<id>/working-copy`, runs:

```bash
git init -b main
git config user.name Switchback Spike
git config user.email spike@switchback.local
git add -A
git commit -m "fixture: baseline"
```

It then creates `agent-state`, `trace-spool`, and `runtime-logs`. On any failure it removes the partial Session tree. The source Fixture digest before and after materialization must match.

- [ ] **Step 5: Implement atomic LocalFileObjectStore and deterministic redaction**

Object names are derived from identities and evidence kind only. Writes go to a same-directory temporary file, call `Sync`, close, rename, and return `{path, sha256, bytes}`. Redaction replaces exact known tokens with `[REDACTED:<kind>]` before persistence; it does not claim general DLP.

- [ ] **Step 6: Verify all storage invariants**

```bash
cd spikes/worker-runtime
go test ./backend/internal/runtime ./backend/internal/objectstore ./internal/redact -race -count=10
./scripts/tree-digest fixtures/go-repository
```

- [ ] **Step 7: Commit the storage boundary**

```bash
git add spikes/worker-runtime/backend/internal/runtime spikes/worker-runtime/backend/internal/objectstore spikes/worker-runtime/internal/redact spikes/worker-runtime/scripts/tree-digest
git commit -m "spike: isolate runtime storage paths"
```

## Task 5: Build The Async HTTP Surface And Coordinator Shell

**Files:**

- Create: `spikes/worker-runtime/backend/internal/config/config.go`
- Create: `spikes/worker-runtime/backend/internal/config/config_test.go`
- Create: `spikes/worker-runtime/backend/internal/httpapi/handler.go`
- Create: `spikes/worker-runtime/backend/internal/httpapi/handler_test.go`
- Create: `spikes/worker-runtime/backend/internal/httpapi/internal_upload.go`
- Create: `spikes/worker-runtime/backend/internal/httpapi/internal_upload_test.go`
- Create: `spikes/worker-runtime/backend/internal/service/coordinator.go`
- Create: `spikes/worker-runtime/backend/internal/service/coordinator_test.go`
- Create: `spikes/worker-runtime/backend/cmd/spike-backend/main.go`

- [ ] **Step 1: Write HTTP contract tests with fake runtime and command bus**

Cover:

- `POST /spike/runs` returns `202` plus generated Session/Run/Turn IDs and never accepts runtime fields.
- `POST /spike/sessions/{id}/turns` returns `409` before Turn 1 completion or while a turn is active.
- `GET /spike/runs/{id}` exposes immutable snapshots and separate execution/acceptance outcomes.
- `POST /spike/sessions/{id}/cleanup` is idempotent.
- Internal upload rejects missing/incorrect bearer token, identity/path mismatch, unsupported evidence kind, checksum mismatch and payload over 32 MiB.
- Public request bodies over 1 MiB and unknown JSON fields return `400`/`413`.

- [ ] **Step 2: Confirm the handler tests fail**

```bash
cd spikes/worker-runtime
go test ./backend/internal/config ./backend/internal/httpapi ./backend/internal/service -race -v
```

- [ ] **Step 3: Implement strict DTOs and asynchronous ownership**

Public request shapes are fixed:

```go
type CreateRunRequest struct {
	Goal               string   `json:"goal"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Constraints        []string `json:"constraints,omitempty"`
}

type CreateTurnRequest struct {
	Message string `json:"message"`
}
```

`CreateRun` performs synchronous validation, ID allocation, Session directory materialization and state creation, then schedules provisioning/Turn 1 on the Backend root context. Request cancellation after `202` must not cancel the accepted run. No database persistence is added.

- [ ] **Step 4: Add Worker-only evidence upload authentication**

The internal route is:

```text
PUT /internal/v1/sessions/{session}/runs/{run}/turns/{turn}/evidence/{kind}
```

Allowed `kind`: `raw-acp`, `trace`, `report`, `patch`. Required headers: bearer Worker token, `Content-Type`, `X-Content-SHA256`. The token maps to one exact Session/Run/Runtime generation; a valid token cannot upload another run's evidence.

- [ ] **Step 5: Verify API behavior and process startup**

```bash
cd spikes/worker-runtime
go test ./backend/internal/config ./backend/internal/httpapi ./backend/internal/service -race -count=10
mkdir -p .spike-data/bin
go build -o .spike-data/bin/spike-backend ./backend/cmd/spike-backend
```

- [ ] **Step 6: Commit the HTTP slice**

```bash
git add spikes/worker-runtime/backend/cmd spikes/worker-runtime/backend/internal/config spikes/worker-runtime/backend/internal/httpapi spikes/worker-runtime/backend/internal/service
git commit -m "spike: add asynchronous runtime API"
```

## Task 6: Implement Authenticated gRPC WorkerControl

**Files:**

- Create: `spikes/worker-runtime/backend/internal/control/auth.go`
- Create: `spikes/worker-runtime/backend/internal/control/auth_test.go`
- Create: `spikes/worker-runtime/backend/internal/control/registry.go`
- Create: `spikes/worker-runtime/backend/internal/control/registry_test.go`
- Create: `spikes/worker-runtime/backend/internal/control/server.go`
- Create: `spikes/worker-runtime/backend/internal/control/server_test.go`
- Modify: `spikes/worker-runtime/backend/cmd/spike-backend/main.go`
- Modify: `spikes/worker-runtime/backend/internal/service/coordinator.go`
- Modify: `spikes/worker-runtime/go.mod`

- [ ] **Step 1: Write bufconn stream tests**

Tests must prove:

- Missing or wrong `authorization: Bearer <worker-token>` is rejected before registration.
- Runtime identity must match the token record exactly.
- A second stream for the same runtime is rejected rather than silently replacing the first.
- Command sequence and event sequence are independently monotonic.
- `WaitReady` unblocks only on a valid `RuntimeReady`.
- Late events after cleanup are recorded as diagnostics but cannot mutate a terminal run.
- Backpressure closes a runtime whose bounded outbound queue remains full; there is no unbounded buffering.

- [ ] **Step 2: Confirm stream tests fail**

```bash
cd spikes/worker-runtime
go test ./backend/internal/control -run 'Test(Auth|Registry|Connect)' -race -v
```

- [ ] **Step 3: Implement registry and CommandBus**

Each live connection owns:

```go
type Connection struct {
	Identity     Identity
	SendSequence uint64
	RecvSequence uint64
	PeerAck      uint64
	Commands     chan *workercontrolv1.BackendEnvelope // capacity 32
	Ready        chan struct{}
}
```

The stream receive loop validates and persists an event before advancing its receive cursor. The send loop assigns message ID, sequence, current ACK and server timestamp. RuntimeReady closes `Ready` exactly once.

The Spike gRPC server listens on `:9090` in the Compose control network. Worker dials `spike-backend:9090` with gRPC insecure transport credentials plus the per-Session bearer token from its root-only bootstrap file. This h2c choice is limited to the local Docker bridge; it is not a production TLS decision. Evidence upload similarly uses `http://spike-backend:8080` plus the same scoped token.

- [ ] **Step 4: Wire terminal events into run state**

`TurnCompleted` only advances the exact active turn after all four evidence uploads have valid object refs. `TurnFailed` preserves partial evidence and sets `execution_outcome=failed`. ModelObservation and ACPUpdate append bounded in-memory diagnostics but authoritative content remains in uploaded JSONL.

- [ ] **Step 5: Verify transport behavior**

```bash
cd spikes/worker-runtime
go test ./backend/internal/control ./backend/internal/service -race -count=20
go test ./... -race
```

Expected: no goroutine leaks under `-count=20`; invalid identities never enter the registry.

- [ ] **Step 6: Commit control transport**

```bash
git add spikes/worker-runtime/backend/internal/control spikes/worker-runtime/backend/internal/service spikes/worker-runtime/backend/cmd/spike-backend spikes/worker-runtime/go.mod spikes/worker-runtime/go.sum
git commit -m "spike: add worker control stream"
```

## Task 7: Add The Moby RuntimeProvisioner

**Files:**

- Create: `spikes/worker-runtime/backend/internal/runtime/provisioner.go`
- Create: `spikes/worker-runtime/backend/internal/runtime/docker.go`
- Create: `spikes/worker-runtime/backend/internal/runtime/docker_test.go`
- Create: `spikes/worker-runtime/backend/internal/runtime/docker_live_test.go`
- Modify: `spikes/worker-runtime/backend/internal/config/config.go`
- Modify: `spikes/worker-runtime/backend/internal/config/config_test.go`
- Create: `spikes/worker-runtime/backend/internal/service/runtime_provisioner.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_scheduler_test.go`
- Modify: `spikes/worker-runtime/go.mod`
- Modify: `spikes/worker-runtime/go.sum`

- [ ] **Step 1: Write Engine-client contract tests against a recording fake**

Assert the exact create request:

- Server-configured image and network only.
- Labels contain managed marker plus Session/Run/Runtime/generation.
- Bind mounts are exactly WorkingCopy, Agent state and Trace spool. Bootstrap
  JSON and model secret share one server-derived, read-only Docker-managed
  volume; no host credential file is mounted into the Worker.
- No Docker Socket, host root, Fixture source or arbitrary client path is mounted.
- `Privileged=false`, no host network, no port bindings, `ReadonlyRootfs=false` only because Coding Agent tools need the writable layer.
- Resource limits: 2 CPUs, 4 GiB memory, 512 PIDs.
- The adapter does not send an unsupported `StorageOpt` blindly. If the daemon cannot enforce the M1 10 GiB writable-layer limit, the acceptance report records `writable_layer_limit=not_verified` and the Spike does not claim full ADR 0062 conformance.
- Capabilities are dropped to `ALL` then only `KILL`, `SETUID` and `SETGID`
  are added for trusted init child process-group management and credential
  switching; `no-new-privileges` is set. Non-root LiteLLM and Agent children
  retain zero permitted/effective capabilities.
- Repeated Start with the same idempotency labels returns the existing exact container; conflicting identity fails.
- Cleanup removes only a container carrying all expected Switchback labels.

- [ ] **Step 2: Confirm tests fail**

```bash
cd spikes/worker-runtime
go test ./backend/internal/runtime -run TestDocker -race -v
```

- [ ] **Step 3: Pin the current Moby modules and implement the adapter**

Use:

```go
require (
	github.com/moby/moby/api v1.55.0
	github.com/moby/moby/client v0.5.0
)
```

Construct the client with:

```go
client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
```

Use the v0.5.0 option-object API: `ContainerCreate(ctx, client.ContainerCreateOptions{...})`, `ContainerStart`, `ContainerInspect`, `ContainerWait`, and `ContainerRemove`. Do not copy pre-v0.5 positional signatures from old Docker examples.

- [ ] **Step 4: Add live Docker negative tests behind a build tag**

Create `docker_live_test.go` with `//go:build docker_integration`. The test creates a minimal disposable container and inspects it through the real daemon. It skips only when `SWITCHBACK_SPIKE_DOCKER_TEST=1` is absent; with the variable present, daemon unavailability is a failure. Verify no socket mount, no host port, labels and limits, then always remove the container.

- [ ] **Step 5: Start Docker and verify both fake and live tests**

```bash
cd spikes/worker-runtime
go test ./backend/internal/runtime -race
SWITCHBACK_SPIKE_DOCKER_TEST=1 go test -tags=docker_integration ./backend/internal/runtime -run TestDockerLive -v
```

Expected: both pass. If Docker daemon is still stopped, start Docker Desktop before proceeding; do not weaken the live test.

- [ ] **Step 6: Commit the provisioner**

```bash
git add spikes/worker-runtime/backend/internal/runtime spikes/worker-runtime/backend/internal/config spikes/worker-runtime/backend/internal/service spikes/worker-runtime/go.mod spikes/worker-runtime/go.sum
git commit -m "spike: provision isolated worker containers"
```

## Task 8: Supervise LiteLLM Under A Separate Unix Identity

**Files:**

- Create: `spikes/worker-runtime/worker/internal/process/supervisor.go`
- Create: `spikes/worker-runtime/worker/internal/process/supervisor_test.go`
- Create: `spikes/worker-runtime/worker/internal/modelobs/server.go`
- Create: `spikes/worker-runtime/worker/internal/modelobs/server_test.go`
- Create: `spikes/worker-runtime/worker/litellm/config.yaml`
- Create: `spikes/worker-runtime/worker/litellm/switchback_callback.py`
- Create: `spikes/worker-runtime/worker/litellm/test_switchback_callback.py`
- Create: `spikes/worker-runtime/worker/cmd/worker-shim/main.go`
- Modify: `spikes/worker-runtime/go.mod`

- [ ] **Step 1: Write process environment and sink tests**

Tests must prove:

- LiteLLM child runs as UID/GID `10001`, receives upstream key, proxy key and sink key only in its child environment.
- Codex child configuration runs as UID/GID `10002` and receives none of the upstream, Worker-control, Langfuse or sink secrets.
- Trusted root Shim may read the root-only bootstrap/model files, but never writes secret values to argv or logs.
- Model sink binds only to `127.0.0.1:7777`, requires `X-Switchback-Model-Sink-Token`, rejects payloads over 1 MiB and emits a typed observation.
- LiteLLM readiness timeout prevents ACP startup and returns structured diagnostics.

- [ ] **Step 2: Write Python callback tests and confirm all fail**

```bash
cd spikes/worker-runtime
go test ./worker/internal/process ./worker/internal/modelobs -race -v
python3 -m unittest worker/litellm/test_switchback_callback.py -v
```

- [ ] **Step 3: Implement the fixed LiteLLM route**

`config.yaml` contains one server-owned alias:

```yaml
model_list:
  - model_name: switchback-codex
    litellm_params:
      model: os.environ/SWITCHBACK_SPIKE_UPSTREAM_MODEL
      api_base: os.environ/SWITCHBACK_SPIKE_UPSTREAM_API_BASE
      api_key: os.environ/SWITCHBACK_SPIKE_UPSTREAM_API_KEY

general_settings:
  master_key: os.environ/SWITCHBACK_SPIKE_PROXY_TOKEN

litellm_settings:
  drop_params: true
  callbacks: switchback_callback.switchback_callback
```

The callback subclasses `litellm.integrations.custom_logger.CustomLogger`, implements `async_log_success_event` and `async_log_failure_event`, extracts model/route/start/end/latency/usage/cost/error plus request correlation headers, and POSTs JSON to the fixed localhost sink with `urllib.request`. It never calls Langfuse.

- [ ] **Step 4: Implement trusted init process supervision**

Startup order is strict:

1. Parse root-only bootstrap JSON.
2. Read upstream model key into memory.
3. Start authenticated localhost ModelObservationSink.
4. Start LiteLLM as UID/GID 10001 with only its filtered environment.
5. Poll `http://127.0.0.1:4000/health/liveliness` for at most 30 seconds.
6. Only after readiness, make ACP startup available.

Shutdown sends SIGTERM, waits five seconds, then SIGKILLs the child process group. Child stdout/stderr goes through known-token redaction before bounded diagnostic logs.

- [ ] **Step 5: Verify callback mapping and secret separation**

```bash
cd spikes/worker-runtime
go test ./worker/internal/process ./worker/internal/modelobs -race -count=20
python3 -m unittest worker/litellm/test_switchback_callback.py -v
mkdir -p .spike-data/bin
go build -o .spike-data/bin/worker-shim ./worker/cmd/worker-shim
```

- [ ] **Step 6: Commit model proxy supervision**

```bash
git add spikes/worker-runtime/worker/cmd spikes/worker-runtime/worker/internal/process spikes/worker-runtime/worker/internal/modelobs spikes/worker-runtime/worker/litellm
git commit -m "spike: supervise worker model proxy"
```

## Task 9: Drive codex-acp Through The Typed Go ACP Client

**Files:**

- Create: `spikes/worker-runtime/worker/internal/acp/adapter.go`
- Create: `spikes/worker-runtime/worker/internal/acp/adapter_test.go`
- Create: `spikes/worker-runtime/worker/internal/acp/client.go`
- Create: `spikes/worker-runtime/worker/internal/acp/client_test.go`
- Create: `spikes/worker-runtime/worker/internal/acp/rawtee.go`
- Create: `spikes/worker-runtime/worker/internal/acp/rawtee_test.go`
- Create: `spikes/worker-runtime/worker/internal/trace/recorder.go`
- Create: `spikes/worker-runtime/worker/internal/trace/recorder_test.go`
- Modify: `spikes/worker-runtime/worker/cmd/worker-shim/main.go`
- Modify: `spikes/worker-runtime/worker/internal/process/supervisor.go`
- Modify: `spikes/worker-runtime/worker/internal/process/supervisor_test.go`
- Modify: `spikes/worker-runtime/go.mod`
- Modify: `spikes/worker-runtime/go.sum`

Task 9 review amendment: the trusted Shim reads one immutable
`RuntimeSecrets` snapshot and passes that same snapshot to the model-proxy
Supervisor and ACP gateway authentication. Do not reread the mounted bootstrap
between those startup phases. The ACP adapter must also preserve Task 8's
single-owner process lifecycle: concurrent start/close cannot orphan a child,
session creation and AgentTurn terminality fail closed after ambiguous results,
stdout/raw-trace drain completes before `Wait` and trace close, and filesystem
callbacks open beneath the trusted Workspace directory without following
symlinks.

- [ ] **Step 1: Write fake-agent tests for startup order and continuity**

Use a test ACP process, not a mocked `Adapter`, to assert:

- Raw inbound/outbound JSONL is recorded before SDK decode.
- `Initialize` advertises filesystem capability and `auth._meta.gateway=true`.
- Initialization response must advertise `gateway`; absence fails closed.
- `Authenticate(methodId="gateway")` occurs before `NewSession`.
- Gateway metadata uses base URL `http://127.0.0.1:4000/v1`, provider name `Switchback LiteLLM`, and only the short proxy Authorization header.
- Exactly one `NewSession` occurs, while two calls to `Prompt` reuse the returned session ID.
- Unknown optional SessionUpdate is retained as `acp.unknown_update` and does not kill the healthy session.
- Permission requests fail closed; the Spike never auto-selects an approval.
- File reads/writes are rejected unless the cleaned absolute path stays below `/workspace`.

- [ ] **Step 2: Confirm ACP tests fail**

```bash
cd spikes/worker-runtime
go test ./worker/internal/acp ./worker/internal/trace -race -v
```

- [ ] **Step 3: Pin ACP SDK and implement the process adapter**

Add:

```go
require github.com/coder/acp-go-sdk v0.13.5
```

The real startup sequence is:

```go
cmd := exec.CommandContext(ctx, "/opt/switchback/node_modules/.bin/codex-acp")
stdin, _ := cmd.StdinPipe()
stdout, _ := cmd.StdoutPipe()

recordedInput := NewRecordingWriter(stdin, rawRecorder, "client_to_agent")
recordedOutput := io.TeeReader(stdout, NewFrameWriter(rawRecorder, "agent_to_client"))
conn := acp.NewClientSideConnection(client, recordedInput, recordedOutput)
```

The adapter sets a filtered child environment including:

```text
CODEX_PATH=/opt/switchback/node_modules/.bin/codex
NO_BROWSER=1
CODEX_HOME=/agent-state/codex
APP_SERVER_LOGS=/trace-spool/app-server
CODEX_CONFIG={"model":"switchback-codex","approval_policy":"never","sandbox_mode":"danger-full-access","web_search":"disabled"}
```

`danger-full-access` applies only inside the already isolated Worker container; it is not a host permission.

- [ ] **Step 4: Implement gateway authentication and one-session reuse**

```go
initResp, err := conn.Initialize(ctx, acp.InitializeRequest{
	ProtocolVersion: acp.ProtocolVersionNumber,
	ClientCapabilities: acp.ClientCapabilities{
		Fs: acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
		Auth: acp.AuthCapabilities{Meta: map[string]any{"gateway": true}},
	},
})
```

After confirming the advertised method:

```go
_, err = conn.Authenticate(ctx, acp.AuthenticateRequest{
	MethodId: "gateway",
	Meta: map[string]any{"gateway": map[string]any{
		"baseUrl": "http://127.0.0.1:4000/v1",
		"providerName": "Switchback LiteLLM",
		"headers": map[string]string{"Authorization": "Bearer " + proxyToken},
	}},
})
```

Then call `NewSession` once with `Cwd: "/workspace"` and `McpServers: []acp.McpServer{}`. Both AgentTurns use `Prompt` with that same `SessionId` and `[]acp.ContentBlock{acp.TextBlock(message)}`.

- [ ] **Step 5: Verify typed behavior and raw recording**

```bash
cd spikes/worker-runtime
go test ./worker/internal/acp ./worker/internal/trace -race -count=20
go test ./... -race
```

- [ ] **Step 6: Commit the ACP adapter**

```bash
git add spikes/worker-runtime/worker/internal/acp spikes/worker-runtime/worker/internal/trace spikes/worker-runtime/worker/cmd/worker-shim spikes/worker-runtime/go.mod spikes/worker-runtime/go.sum
git commit -m "spike: control codex through ACP"
```

## Task 10: Finalize Per-Turn JSONL, Report, Patch, And Manifest

**Files:**

- Create: `spikes/worker-runtime/worker/internal/gitpatch/snapshot.go`
- Create: `spikes/worker-runtime/worker/internal/gitpatch/snapshot_test.go`
- Create: `spikes/worker-runtime/worker/internal/evidence/finalizer.go`
- Create: `spikes/worker-runtime/worker/internal/evidence/finalizer_test.go`
- Create: `spikes/worker-runtime/worker/internal/upload/client.go`
- Create: `spikes/worker-runtime/worker/internal/upload/client_test.go`
- Create: `spikes/worker-runtime/worker/internal/control/client.go`
- Create: `spikes/worker-runtime/worker/internal/control/client_test.go`
- Modify: `spikes/worker-runtime/internal/evidence/types.go`
- Modify: `spikes/worker-runtime/worker/cmd/worker-shim/main.go`
- Create: `spikes/worker-runtime/worker/cmd/worker-shim/main_test.go`
- Modify: `spikes/worker-runtime/backend/internal/httpapi/internal_upload.go`
- Modify: `spikes/worker-runtime/backend/internal/service/coordinator.go`
- Modify: `spikes/worker-runtime/api/workercontrol/v1/worker_control.proto`
- Modify: `spikes/worker-runtime/gen/workercontrol/v1/worker_control.pb.go`

Task 10 integration amendment: helper-only evidence wiring is not complete. The
real Shim entrypoint must actively connect to WorkerControl, receive the
server-issued Trace identity, initialize one ACP AgentSession, execute
`StartAgentTurn` commands, and invoke the evidence finalizer before sending a
terminal event. Task 13 is a full-path proof and does not own this production
command loop.

- Add `trace_id` to `InitializeSession`; the Backend sends the exact Run TraceID.
- The Worker reads its server-issued runtime identity, Worker token, Backend
  gRPC target and Backend HTTP base URL from trusted runtime configuration.
  Missing or malformed values fail startup; client/API payloads cannot choose
  them. Task 12 wires these values into the server-owned Docker create request.
- The WorkerControl client authenticates with the Worker token, sends
  `RuntimeReady` first, maintains monotonic stream sequence/ACK state, rejects
  stale/out-of-order Backend commands, and implements the evidence
  `TerminalSink`.
- `InitializeSession` creates exactly one ACP session and emits
  `AgentSessionReady`. Each `StartAgentTurn` captures the exact before-tree and
  raw-trace offset before `TurnStarted`/ACP Prompt, reuses that AgentSession,
  finalizes and uploads evidence, then emits exactly one `TurnCompleted` or
  `TurnFailed`.
- Command receive remains live while a Turn runs so `CancelAgentTurn` and
  `ShutdownRuntime` can cancel the active Prompt. A second concurrent
  `StartAgentTurn` fails closed rather than starting another top-level Turn.
- Model observations are associated with the active Turn and forwarded through
  WorkerControl. Stream failure, ambiguous terminal send, or identity mismatch
  fails closed; it cannot report a successful Turn locally.
- Cover the real command loop with an in-process gRPC server and fake ACP seam:
  initialization, two sequential Turns on one AgentSession, evidence
  finalization, partial upload failure, cancel, sequence/ACK rejection and
  graceful shutdown. Tests must prove the production `run` composition invokes
  the loop rather than leaving the new helpers unreachable.

- [ ] **Step 1: Write tests for incremental patches and evidence completeness**

Create a temporary dirty Git repository and prove:

- Turn 1 patch contains only Turn 1 changes.
- Turn 2 patch is diffed from the exact post-Turn-1 tree and does not repeat Turn 1.
- Snapshotting does not alter the real index, HEAD or WorkingCopy.
- Empty patch is valid only when the terminal report explicitly says no files changed.
- Report, raw ACP JSONL, normalized JSONL and patch each receive a checksum and byte count.
- `TurnCompleted` is sent only after all uploads return matching object refs.
- A partial upload failure emits `TurnFailed` and never reports completed evidence.

- [ ] **Step 2: Confirm tests fail**

```bash
cd spikes/worker-runtime
go test ./worker/internal/gitpatch ./worker/internal/evidence ./worker/internal/upload ./worker/internal/control -race -v
```

- [ ] **Step 3: Implement Git tree snapshots without synthetic commits**

Use a temporary index per snapshot:

```bash
GIT_INDEX_FILE=<temp> git read-tree HEAD
GIT_INDEX_FILE=<temp> git add -A
GIT_INDEX_FILE=<temp> git write-tree
```

At Turn start record `before_tree`; at terminal record `after_tree`; generate `git diff --binary --no-ext-diff <before_tree> <after_tree>`. Remove the temporary index in all paths. Do not stage, commit, stash, reset or clean the user's WorkingCopy.

- [ ] **Step 4: Define the authoritative manifest**

```go
type ObjectRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type TurnManifest struct {
	SchemaVersion     string      `json:"schema_version"`
	WorkerSessionID   string      `json:"worker_session_id"`
	WorkerRunID       string      `json:"worker_run_id"`
	RuntimeInstanceID string      `json:"runtime_instance_id"`
	RuntimeGeneration uint32      `json:"runtime_generation"`
	AgentSessionID    string      `json:"agent_session_id"`
	AgentTurnID       string      `json:"agent_turn_id"`
	TraceID           string      `json:"trace_id"`
	BeforeTree        string      `json:"before_tree"`
	AfterTree         string      `json:"after_tree"`
	Objects           []ObjectRef `json:"objects"`
}
```

Schema version is `switchback.spike.turn_manifest.v1`. Backend writes the manifest only after verifying the four uploaded objects.

- [ ] **Step 5: Build normalized trace and report**

Normalized JSONL records include schema, stable sequence, server/worker timestamps, all identities, kind and typed payload. The report is the final accumulated Agent message followed by a generated verification appendix listing commands/tests observed in ACP updates. Known tokens are redacted before upload; Backend redacts again before ObjectStore write.

- [ ] **Step 6: Verify evidence behavior**

```bash
cd spikes/worker-runtime
go test ./worker/internal/gitpatch ./worker/internal/evidence ./worker/internal/upload ./worker/internal/control ./worker/cmd/worker-shim ./backend/internal/httpapi ./backend/internal/service -race -count=20
```

- [ ] **Step 7: Commit evidence finalization**

```bash
git add spikes/worker-runtime/worker/internal/gitpatch spikes/worker-runtime/worker/internal/evidence spikes/worker-runtime/worker/internal/upload spikes/worker-runtime/worker/internal/control spikes/worker-runtime/worker/cmd/worker-shim spikes/worker-runtime/internal/evidence spikes/worker-runtime/backend/internal/httpapi spikes/worker-runtime/backend/internal/service spikes/worker-runtime/api/workercontrol/v1/worker_control.proto spikes/worker-runtime/gen/workercontrol/v1/worker_control.pb.go
git commit -m "spike: persist turn evidence and patches"
```

## Task 11: Export And Verify The Langfuse Projection

**Files:**

- Create: `spikes/worker-runtime/backend/internal/langfuse/client.go`
- Create: `spikes/worker-runtime/backend/internal/langfuse/client_test.go`
- Create: `spikes/worker-runtime/backend/internal/langfuse/exporter.go`
- Create: `spikes/worker-runtime/backend/internal/langfuse/exporter_test.go`
- Create: `spikes/worker-runtime/backend/internal/langfuse/verifier.go`
- Create: `spikes/worker-runtime/backend/internal/langfuse/verifier_test.go`
- Modify: `spikes/worker-runtime/backend/internal/service/coordinator.go`
- Modify: `spikes/worker-runtime/backend/internal/service/coordinator_test.go`
- Create: `spikes/worker-runtime/backend/internal/service/langfuse_projection.go`
- Create: `spikes/worker-runtime/backend/internal/service/langfuse_projection_test.go`
- Create: `spikes/worker-runtime/scripts/verify-langfuse`

- [ ] **Step 1: Write HTTP tests for ingestion and eventual queryability**

Using `httptest.Server`, cover:

- Basic Auth is `public_key:secret_key` and keys never appear in URL or logs.
- Ingestion sends one batch to `/api/public/ingestion`.
- HTTP `207` with any non-empty `errors` array is failure even if some events succeeded.
- `GetTrace` calls `/api/public/traces/{trace_id}?fields=core,observations`.
- Verifier tolerates initial `404`/missing observations, polls once per second, and succeeds only when exact two AgentTurn spans and expected model generations are visible.
- A 90-second bounded deadline returns acceptance failure with last observed response summary.
- Ingestion/query errors do not change `execution_outcome=completed`.

- [ ] **Step 2: Confirm Langfuse tests fail**

```bash
cd spikes/worker-runtime
go test ./backend/internal/langfuse ./backend/internal/service -run 'Test(Langfuse|Projection|Acceptance)' -race -v
```

- [ ] **Step 3: Implement the exact v3.217.0 legacy ingestion schema**

The deployment must set `LANGFUSE_MIGRATION_V4_WRITE_MODE=legacy`; `events_only` rejects trace/span/generation events at this endpoint.

```go
type IngestionEvent struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	Body      map[string]any `json:"body"`
}

type IngestionBatch struct {
	Batch []IngestionEvent `json:"batch"`
}
```

Export mapping:

| Switchback fact | Langfuse event | Langfuse identity |
| --- | --- | --- |
| WorkerRun | `trace-create` | `body.id = trace_id` |
| AgentTurn | `span-create` | `body.id = agent_turn_id`, `traceId = trace_id`, name `agent.turn` |
| LiteLLM observation | `generation-create` | `body.id = model_observation_id`, parent `agent_turn_id` |

Every body metadata map includes WorkerSession, WorkerRun, Runtime, generation, AgentSession and AgentTurn IDs. Generation includes model, start/end, usageDetails, costDetails and status; known tokens and raw Authorization headers are excluded.

- [ ] **Step 4: Keep Backend as the only Langfuse writer**

Worker sends ModelObservation over WorkerControl and never receives Langfuse host or keys. Backend batches a trace-create, completed turn spans and generations after authoritative evidence is stored. Export retries are bounded to the coordinator operation; no durable retry subsystem is added to the Spike.

- [ ] **Step 5: Verify exporter and asynchronous verifier**

```bash
cd spikes/worker-runtime
go test ./backend/internal/langfuse ./backend/internal/service -race -count=20
```

- [ ] **Step 6: Commit the observability projection**

```bash
git add spikes/worker-runtime/backend/internal/langfuse spikes/worker-runtime/backend/internal/service spikes/worker-runtime/scripts/verify-langfuse
git commit -m "spike: export traces to langfuse"
```

## Task 12: Build Pinned Images, Compose Topology, And Preflight

**Files:**

- Create: `spikes/worker-runtime/images/backend/Dockerfile`
- Create: `spikes/worker-runtime/images/worker/Dockerfile`
- Create: `spikes/worker-runtime/images/worker/package.json`
- Create: `spikes/worker-runtime/images/worker/package-lock.json`
- Create: `spikes/worker-runtime/compose.yaml`
- Create: `spikes/worker-runtime/image-lock.json`
- Create: `spikes/worker-runtime/scripts/generate-env`
- Create: `spikes/worker-runtime/scripts/lock-images`
- Create: `spikes/worker-runtime/scripts/preflight`
- Create: `spikes/worker-runtime/scripts/test-deployment`
- Modify: `spikes/worker-runtime/Makefile`
- Modify: `spikes/worker-runtime/README.md`
- Modify: `spikes/worker-runtime/backend/cmd/spike-backend/main.go`
- Create: `spikes/worker-runtime/backend/cmd/spike-backend/main_test.go`
- Modify: `spikes/worker-runtime/backend/internal/config/config.go`
- Modify: `spikes/worker-runtime/backend/internal/config/config_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker_live_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/materializer.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/materializer_test.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_provisioner.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_scheduler_test.go`

Task 12 integration amendment: the current local Backend main still uses
`UnavailableRuntimeProvisioner`, its HTTP default is process-local loopback,
and the Task 7 Docker request does not yet inject WorkerControl/upload
endpoints. The Compose task must close those deployment-only wiring gaps before
Task 13:

- Backend binds its container HTTP listener on the Compose network while the
  published host port remains loopback-only.
- Backend main constructs the real Moby provisioner and trusted supplemental
  bootstrap/model-secret resolver; it must not retain
  `UnavailableRuntimeProvisioner` in the Compose profile.
- The server-owned Docker create request injects the exact Backend gRPC target
  and HTTP base URL consumed by the Task 10 Worker command loop. These values
  come only from validated Backend configuration, never client JSON.
- The D-o-D HostPath verifier must work when Backend container paths and Docker
  host paths differ; the local-filesystem verifier is insufficient for that
  profile.
- Backend validates Langfuse origin/public-key/secret-key configuration and
  constructs the Task 11 Client/Exporter/Verifier/Projector. Langfuse host and
  credentials are injected only into Backend; Compose and RuntimeSupervisor
  must not pass them to the Worker container.

- [ ] **Step 1: Write shell contract tests for preflight and secret generation**

Use a temporary data root and stubbed `docker` executable to prove:

- Missing daemon, Compose v2, required model/key configuration, writable data root, required ports or the Spike minimum of 6 Docker CPUs, 12 GiB Docker memory and 25 GiB free Docker storage fails before any container is created.
- Secret generation writes owner-only files, is idempotent, and never echoes values.
- Host data root is absolute and maps to Backend container root `/var/lib/switchback-spike`.
- `image-lock.json` rejects tag-only runtime refs, duplicate names and mismatched platform.
- Cleanup never runs `docker compose down -v` during normal operation.

- [ ] **Step 2: Confirm preflight tests fail**

```bash
cd spikes/worker-runtime
make test-scripts
```

- [ ] **Step 3: Pin Node packages and build the Worker image**

`package.json` dependencies are exact:

```json
{
  "private": true,
  "dependencies": {
    "@agentclientprotocol/codex-acp": "1.1.2",
    "@openai/codex": "0.144.1"
  }
}
```

Generate and commit `package-lock.json` with npm 11.13.0. Worker Dockerfile uses reviewed digest refs for Go 1.26.4, Node 24.17.0 and Python 3.13 Bookworm stages, installs `litellm[proxy]==1.93.0`, `git`, CA certificates and `tini`, and creates:

```text
litellm uid/gid 10001
switchback-agent uid/gid 10002
```

Copy Go toolchain for Fixture tests, Node package tree, Worker Shim and LiteLLM callback/config. `codex-acp`, Codex, Go, Git and LiteLLM version checks run during image build.

- [ ] **Step 4: Build the Backend image and D-o-D path mapping**

Backend image contains only the Backend binary, Git, CA certificates and a read-only copy of the Fixture. Compose bind mounts:

```text
${SWITCHBACK_SPIKE_HOST_DATA_ROOT}:/var/lib/switchback-spike
${SWITCHBACK_SPIKE_DOCKER_SOCKET:-/var/run/docker.sock}:/var/run/docker.sock
```

The Docker Socket mount exists only on Backend. `scripts/generate-env` resolves the active Docker context with `docker context inspect`, accepts only a local `unix://` endpoint, strips that prefix into `SWITCHBACK_SPIKE_DOCKER_SOCKET`, and does not assume Docker Desktop uses `/var/run/docker.sock`. RuntimeSupervisor uses the host root for sibling bind sources and the container root for its own file operations.

`scripts/generate-env` writes the upstream key to an owner-only file below
`.spike-data/env/`. The trusted Backend reads that value for the known-token
redactor and per-Runtime secret staging; it never bind mounts that host file
into the Worker or injects the key into the Worker container's top-level
environment. The Backend creates one labeled Docker-managed secret volume per
Runtime, uses a short-lived no-network/no-Socket root populator to copy only
the server-derived bootstrap and model files as `root:root 0400`, removes the
populator before Worker start, and mounts the volume read-only into Worker.

- [ ] **Step 5: Lock all external images by manifest digest**

`scripts/lock-images` resolves and records the current multi-arch manifest digest plus selected platform digest for:

```text
bufbuild/buf:1.71.0
golang:1.26.4-bookworm
node:24.17.0-bookworm-slim
python:3.13-slim-bookworm
langfuse/langfuse:3.217.0
langfuse/langfuse-worker:3.217.0
postgres:17
redis:7
clickhouse/clickhouse-server (official v3.217.0 Compose reference)
cgr.dev/chainguard/minio (official v3.217.0 Compose reference)
```

Compose consumes only digest refs emitted from `image-lock.json`; floating source tags are provenance inputs, never runtime image refs.

- [ ] **Step 6: Implement the three-network Compose topology**

Networks:

- `spike-control`: Backend plus dynamic Worker; non-internal because LiteLLM needs upstream model egress.
- `spike-observability`: Backend plus Langfuse Web/Worker.
- `spike-storage`: internal-only Langfuse Web/Worker/PostgreSQL/ClickHouse/Redis/MinIO.

Compose assigns explicit Engine names `switchback-spike-control`, `switchback-spike-observability` and `switchback-spike-storage` instead of project-prefixed defaults. RuntimeSupervisor receives only `switchback-spike-control`; it never discovers a client-provided network name.

Only loopback host ports are published: Backend HTTP `18080`, Backend gRPC `19090`, Langfuse Web `13000`, Langfuse Worker health `13030`. Dynamic Worker publishes no port and joins only `spike-control`.

Use the full Langfuse v3 six-service topology. Set identical Web/Worker version, headless `LANGFUSE_INIT_*`, `LANGFUSE_MIGRATION_V4_WRITE_MODE=legacy`, telemetry off for the Spike, and generated secrets from `.spike-data/env/`.

- [ ] **Step 7: Implement stable Make targets**

Required targets:

```text
fmt
fmt-check
proto
proto-check
test
test-scripts
build
lock-images
preflight
compose-up
compose-down
spike-integration
spike-e2e
```

`compose-down` retains volumes by default. A separate explicit `purge` target may remove Spike volumes and must print what will be deleted before acting.

- [ ] **Step 8: Verify images and topology**

```bash
cd spikes/worker-runtime
make lock-images
make preflight
make build
docker compose --env-file .spike-data/env/compose.env config --quiet
make test-scripts
```

Expected: image lock contains only digests; Compose config resolves; Backend image has the socket mount; Worker image does not.

- [ ] **Step 9: Commit deployment assets separately**

```bash
git add \
  spikes/worker-runtime/images \
  spikes/worker-runtime/compose.yaml \
  spikes/worker-runtime/image-lock.json \
  spikes/worker-runtime/scripts/generate-env \
  spikes/worker-runtime/scripts/lock-images \
  spikes/worker-runtime/scripts/preflight \
  spikes/worker-runtime/scripts/test-deployment \
  spikes/worker-runtime/Makefile \
  spikes/worker-runtime/README.md \
  spikes/worker-runtime/backend/cmd/spike-backend \
  spikes/worker-runtime/backend/internal/config \
  spikes/worker-runtime/backend/internal/runtime/docker.go \
  spikes/worker-runtime/backend/internal/runtime/docker_test.go \
  spikes/worker-runtime/backend/internal/runtime/docker_live_test.go \
  spikes/worker-runtime/backend/internal/runtime/materializer.go \
  spikes/worker-runtime/backend/internal/runtime/materializer_test.go \
  spikes/worker-runtime/backend/internal/service/runtime_provisioner.go \
  spikes/worker-runtime/backend/internal/service/runtime_scheduler_test.go
git commit -m "spike: add pinned runtime topology"
```

## Task 13: Prove The Full Path With Fake ACP And Fake Upstream

**Files:**

- Create: `spikes/worker-runtime/compose.integration.yaml`
- Create: `spikes/worker-runtime/internal/agentprofile/profile.go`
- Create: `spikes/worker-runtime/internal/agentprofile/profile_test.go`
- Create: `spikes/worker-runtime/test/fakes/cmd/fake-acp/main.go`
- Create: `spikes/worker-runtime/test/fakes/cmd/fake-openai/main.go`
- Create: `spikes/worker-runtime/test/integration/runtime_test.go`
- Modify: `spikes/worker-runtime/backend/cmd/spike-backend/main.go`
- Modify: `spikes/worker-runtime/backend/cmd/spike-backend/main_test.go`
- Modify: `spikes/worker-runtime/backend/internal/config/config.go`
- Modify: `spikes/worker-runtime/backend/internal/config/config_test.go`
- Modify: `spikes/worker-runtime/backend/internal/control/server_test.go`
- Modify: `spikes/worker-runtime/backend/internal/httpapi/handler_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker_live_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/materializer.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/materializer_test.go`
- Modify: `spikes/worker-runtime/backend/internal/service/coordinator.go`
- Modify: `spikes/worker-runtime/backend/internal/service/coordinator_test.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_provisioner.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_scheduler_test.go`
- Modify: `spikes/worker-runtime/images/worker/Dockerfile`
- Modify: `spikes/worker-runtime/compose.yaml`
- Modify: `spikes/worker-runtime/Makefile`
- Modify: `spikes/worker-runtime/worker/cmd/worker-shim/main.go`
- Modify: `spikes/worker-runtime/worker/cmd/worker-shim/main_test.go`
- Modify: `spikes/worker-runtime/worker/internal/acp/adapter.go`
- Modify: `spikes/worker-runtime/worker/internal/acp/adapter_test.go`
- Modify: `spikes/worker-runtime/worker/internal/control/client.go`
- Modify: `spikes/worker-runtime/worker/internal/control/client_lifecycle_test.go`
- Modify: `spikes/worker-runtime/worker/internal/control/client_test.go`
- Modify: `spikes/worker-runtime/worker/internal/gitpatch/snapshot.go`
- Modify: `spikes/worker-runtime/worker/internal/process/supervisor.go`
- Modify: `spikes/worker-runtime/worker/internal/process/supervisor_test.go`

- [ ] **Step 1: Write failing tests for the server-owned Agent profile contract**

Add `internal/agentprofile` constants for exactly `codex` and
`integration-fake`. Write tests proving:

- Backend config defaults to `codex`.
- `SWITCHBACK_SPIKE_AGENT_PROFILE=integration-fake` is rejected unless the
  server-owned `SWITCHBACK_SPIKE_INTEGRATION=1` gate is also present.
- Unknown, whitespace-padded and empty explicit profiles are rejected.
- The trusted bootstrap contains the selected profile, while its String and
  GoString forms still redact every token.
- RuntimeScheduler sends the same selected profile in `InitializeSession`.
- Worker bootstrap parsing rejects unknown profiles.
- Worker maps profiles only to `/opt/switchback/node_modules/.bin/codex-acp` and
  `/opt/switchback/bin/fake-acp`; no environment value or client field can supply an
  executable path.
- WorkerControl rejects an `InitializeSession.agent_profile` that differs from
  the profile used to start its ACP child.

Run the focused tests and preserve the expected RED failures before changing
production code:

```bash
cd spikes/worker-runtime
go test ./internal/agentprofile ./backend/internal/config ./backend/internal/service ./backend/cmd/spike-backend ./worker/internal/process ./worker/internal/control ./worker/cmd/worker-shim -run 'AgentProfile|Bootstrap|InitializeSession' -count=1
```

- [ ] **Step 2: Implement one trusted profile value from Backend to Worker**

Use `Config.AgentProfile` as the single source selected from
`SWITCHBACK_SPIKE_AGENT_PROFILE`; omission means `codex`. The integration profile is
valid only when `SWITCHBACK_SPIKE_INTEGRATION=1`. These variables are Backend
deployment inputs and must not appear in public HTTP request DTOs.

Pass the exact profile to both trusted paths:

1. `TrustedSupplementalResolverOptions.AgentProfile` writes
   `agent_profile` into the root-owned per-Session bootstrap.
2. `NewRuntimeScheduler(controlPlane, provisioner, agentProfile)` validates the
   profile at construction and stores it for `InitializeSession.agent_profile`.

Change `NewRuntimeScheduler` to return `(Scheduler, error)` so an invalid
server-owned profile cannot create a partially configured scheduler. Update all
call sites and tests.

The Worker must read the profile before starting ACP, apply the fixed executable
mapping to the otherwise unchanged `process.AgentChildSpec`, and retain the
profile in `startedRuntime` and `workercontrol.Options`. The WorkerControl client
then requires the initialization profile to equal that retained value.
Production/default behavior remains Codex. Do not add a generic binary path,
command, argv or upstream selector to Worker environment or client JSON.

The fixed mapping must remain an invariant at the final ACP process launch
boundary. `ProcessAdapter` must reject an arbitrary executable, changed
`argv[0]` or additional arguments even if an internal caller constructs a
`ChildSpec` directly.

- [ ] **Step 3: Write a failing gated integration test**

The test starts Backend and fake upstream, asks Backend to create a real dynamic Worker, and asserts:

- Worker connects over gRPC and receives commands.
- Fake ACP receives initialize/auth/new-session/prompt in order.
- Fake ACP performs two deterministic WorkingCopy edits and reuses one session ID.
- Fake ACP makes one OpenAI-compatible `/v1/responses` call per turn through LiteLLM.
- LiteLLM callback reaches ModelObservationSink and Backend receives two generations.
- Four evidence objects per turn plus manifest are stored.
- Worker inspect shows no Docker Socket, no port binding, no privileged mode and only server-derived mounts.
- Fixture source digest is unchanged.
- Cleanup removes the dynamic Worker.

- [ ] **Step 4: Confirm the gated integration test fails**

```bash
cd spikes/worker-runtime
SWITCHBACK_SPIKE_INTEGRATION=1 go test ./test/integration -run TestRuntimeFullPath -v -count=1
```

- [ ] **Step 5: Implement protocol-correct fakes and isolated integration topology**

The fake ACP is a real ACP agent process implemented with `acp-go-sdk`; it is not stdout scripting. On Prompt 1 it fixes `counter.go`; on Prompt 2 it adds a zero-delta boundary test. It emits Agent message and Tool updates and performs an HTTP model request through the configured LiteLLM gateway before returning terminal status.

The fake upstream implements only the OpenAI Responses endpoint fields observed from Codex/LiteLLM, returns deterministic usage, and records whether Authorization was present without logging the value.

Build both fake binaries in the Worker image, but do not change its default
entrypoint. Add `compose.integration.yaml` as an explicit override that:

- sets Backend `SWITCHBACK_SPIKE_INTEGRATION=1`,
  `SWITCHBACK_SPIKE_AGENT_PROFILE=integration-fake` and the exact internal fake
  upstream URL;
- starts fake-openai from the pinned Worker image with an explicit fake-openai
  entrypoint;
- attaches fake-openai only to `spike-control`, publishes no host port, mounts
  no Docker Socket or Switchback data directory, and logs no credential value; and
- leaves default `compose.yaml` on Codex with no fake service.

The first real Docker run may expose prerequisites hidden by in-process tests.
Implement only these narrowly scoped runtime prerequisites, with focused RED
tests before each change:

- Materialized WorkingCopy files and directories preserve fixture content and
  executable bits but add only the owner write/traverse bits required by the
  Worker Agent identity. The source Fixture digest and modes remain unchanged.
- Git commands use per-command `-c safe.directory=<exact WorkingCopy>` because
  Backend materialization and the Agent use different UIDs. Do not modify
  global/system Git configuration or trust another path.
- Trusted Worker-owned ACP `_meta` carries request, trace, Session, Run and Turn
  correlation needed by the LiteLLM callback. Client JSON cannot provide or
  override those values.
- The root Shim may receive only `KILL`, `SETUID` and `SETGID` in addition to
  `CapDrop=ALL`, because it must create and terminate the UID 10001 LiteLLM
  process group and UID 10002 Agent process group. The integration gate must
  inspect the live children and prove both non-root child identities retain no
  effective or permitted Linux capabilities.

`make spike-integration` must use both Compose files, start only the Backend and
fake upstream needed for this proof, and always tear down static services while
preserving evidence long enough for assertions. Dynamic Worker creation still
uses the real Backend Docker provisioner.

- [ ] **Step 6: Verify repeatability and cleanup**

Before cleanup, wait for the intentionally unavailable Langfuse projection in
this isolated topology to settle. Assert `execution_outcome=completed`,
`spike_acceptance=failed`, and a bounded Backend diagnostic identifying
`langfuse_projection`; projection failure must not rewrite the completed Agent
execution.

Decode `manifest.json` and the per-Turn JSONL/patch objects rather than relying
on nonempty files or substring matching. Assert exact Session/Run/Turn
provenance, object names and cardinality, and prove Turn 1 and Turn 2 patches
contain only their respective deterministic changes.

```bash
cd spikes/worker-runtime
make spike-integration
make spike-integration
docker ps -a --filter label=switchback.spike.managed=true --format '{{.ID}}'
```

Expected: both runs pass; final container query is empty.

- [ ] **Step 7: Run focused and repository-local regression checks**

```bash
cd spikes/worker-runtime
make fmt-check
make proto-check
go test ./... -race -count=1
go vet ./...
git diff --check
```

- [ ] **Step 8: Commit fake full-path proof**

```bash
git add \
  spikes/worker-runtime/backend \
  spikes/worker-runtime/worker \
  spikes/worker-runtime/internal/agentprofile \
  spikes/worker-runtime/test/fakes \
  spikes/worker-runtime/test/integration \
  spikes/worker-runtime/images/worker/Dockerfile \
  spikes/worker-runtime/compose.yaml \
  spikes/worker-runtime/compose.integration.yaml \
  spikes/worker-runtime/Makefile
git commit -m "test: prove worker runtime integration"
```

## Task 14: Run The Real Two-Turn Codex And Langfuse Acceptance

**Files:**

- Modify: `docs/architecture/m1-worker-runtime-spike.md`
- Create: `spikes/worker-runtime/test/e2e/runtime_test.go`
- Create: `spikes/worker-runtime/scripts/run-e2e`
- Modify: `spikes/worker-runtime/backend/cmd/spike-backend/main_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker_test.go`
- Modify: `spikes/worker-runtime/backend/internal/runtime/docker_live_test.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_provisioner.go`
- Modify: `spikes/worker-runtime/backend/internal/service/runtime_scheduler_test.go`
- Modify: `spikes/worker-runtime/images/worker/Dockerfile`
- Modify: `spikes/worker-runtime/test/integration/runtime_test.go`
- Modify: `spikes/worker-runtime/worker/cmd/worker-shim/main.go`
- Modify: `spikes/worker-runtime/worker/cmd/worker-shim/main_test.go`
- Modify: `spikes/worker-runtime/scripts/verify-langfuse`
- Modify: `spikes/worker-runtime/Makefile`
- Modify: `spikes/worker-runtime/README.md`
- Modify: `spikes/worker-runtime/image-lock.json` only if the verified platform lock changes

- [x] **Step 0: Repair the production secret mount boundary discovered by E2E**

Docker Desktop projects host-bind ownership in a way that can let UID 10001
and UID 10002 read a host file even when container metadata reports
`root:root 0400`. A test-only source rewrite is not acceptance evidence.
Replace the production Worker secret host binds with one Backend-managed
Docker volume per Runtime:

- Keep only WorkingCopy, Agent state and Trace spool as host bind mounts.
- Derive the volume name and labels from the exact server-owned Runtime
  identity. Reuse is allowed only when all labels match; a conflicting volume
  fails closed.
- Before Worker create, launch a short-lived root populator from the pinned
  Worker image with `NetworkMode=none`, `CapDrop=ALL`,
  `no-new-privileges`, read-only rootfs, no Docker Socket, no published port
  and no secret in environment or argv. It may read only the two verified
  Backend host source files and write only the secret volume.
- The populator writes `bootstrap.json` and `model` as `root:root 0400`,
  verifies their type/owner/mode, exits zero, and is forcibly removed before
  the Worker starts. Populator failure removes the partial volume and creates
  no Worker.
- Worker mounts the volume read-only at one fixed directory. Root Shim reads
  both files from there; UID 10001 receives the exact model key only through
  the existing LiteLLM child environment, while UID 10002 receives neither
  file access nor upstream/Langfuse secret environment.
- After successful staging, remove the transient per-Session host bootstrap
  and model-secret copies. Failure to remove them fails provisioning and
  rolls back the Worker and volume.
- Cleanup verifies exact volume labels, removes the Worker first, then removes
  the secret volume. Missing resources are idempotent; mismatched labels,
  populator/volume removal failure or residue are surfaced with sanitized
  identities and non-zero cleanup.

Write recording-engine RED tests for volume/populator/Worker requests,
idempotency, conflict, every rollback point and cleanup. Then run a real
Docker production-path test on Docker Desktop and, where available, native
Linux proving:

- neither Worker UID can read either volume-backed file;
- UID 10001/10002 retain zero effective/permitted capabilities;
- Worker has no Docker Socket, host credential bind, privileged mode, host
  port or access to the populator;
- populator and source host files are gone before Agent prompt;
- Session cleanup leaves no labeled Worker, populator or secret volume.

Delete the Task 14 test-only Engine source-substitution path after the same
production path proves the boundary.

- [x] **Step 1: Write the E2E test before running the real stack**

The exact prompts are:

Turn 1:

```text
Inspect this Go repository, identify why the existing tests fail, fix the production code, and run go test ./.... Do not add or change tests in this turn. Finish with a concise report of the change and verification.
```

Turn 2:

```text
Continue from the current session and working tree. Add a focused boundary test proving that adding a zero delta leaves the value unchanged. Adjust production code only if the new test exposes a defect, run go test ./..., and report the result. Do not restate or redo the first-turn fix.
```

The test must fail unless Turn 2 uses the same `worker_session_id`, `worker_run_id`, `runtime_instance_id`, runtime generation `1`, `agent_session_id`, container ID and WorkingCopy while using a new `agent_turn_id`.

- [x] **Step 2: Implement the one-command harness with cleanup trap**

`scripts/run-e2e` performs, in order:

1. `make preflight` and record Fixture digest.
2. Validate image lock and build Backend/Worker.
3. Start Langfuse storage, Web/Worker and Backend.
4. Poll PostgreSQL/ClickHouse/Redis/MinIO health, Web `/api/public/health?failIfDatabaseUnavailable=true`, Web `/api/public/ready`, and Worker `:3030/api/health`.
5. Create Turn 1 and poll the exact Run until `idle` or failure.
6. Run `go test ./...` in the Session WorkingCopy and verify Turn 1 patch touches production code but no test.
7. Create Turn 2 and poll until execution terminal.
8. Verify Turn 2 patch touches the boundary test and does not repeat Turn 1 patch.
9. Verify raw/normalized JSONL, reports, patches, manifests and checksums.
10. Scan persisted evidence, logs and Langfuse response for all known exact tokens.
11. Poll exact Langfuse Trace until two `agent.turn` spans and expected generations are visible.
12. Write `acceptance/summary.json`, call cleanup, confirm dynamic Worker removal, and compare Fixture digest.

An EXIT trap always attempts explicit Session cleanup and reports remaining labeled containers. It does not delete evidence or Langfuse volumes.

- [x] **Step 3: Start Docker, configure a real model route, and run E2E**

Required local inputs are supplied to `scripts/generate-env` without committing them:

```text
SWITCHBACK_SPIKE_UPSTREAM_MODEL
SWITCHBACK_SPIKE_UPSTREAM_API_BASE
SWITCHBACK_SPIKE_UPSTREAM_API_KEY
```

Run:

```bash
cd spikes/worker-runtime
make spike-e2e
```

Expected: exit `0`, `execution_outcome=completed`, `spike_acceptance=passed`, and an acceptance summary path printed without any secret value.

- [x] **Step 4: Run the failure-path acceptance matrix**

Run focused E2E variants for:

- Invalid upstream credential: LiteLLM/model failure, no false completion.
- Langfuse unavailable after successful Turn 2: execution completed, acceptance failed.
- Evidence checksum mismatch: execution cannot complete.
- Worker cleanup failure simulated by fake Engine response: non-zero with leaked identity reported.
- Second run after cleanup: independent identities and same functional result.

These variants may use the fake profile except the Langfuse-unavailable case, which must use the real Langfuse stack.

Verified on 2026-07-22: invalid credentials and evidence checksum mismatch
produced failed execution without false completion; a real Kimi run with
Langfuse unavailable completed both turns but failed Spike acceptance; an
injected cleanup failure completed both turns, reported the leaked Worker and
secret-volume identities, failed acceptance, exited non-zero, and was removed
by the harness's real-daemon fallback cleanup. `SWITCHBACK_SPIKE_E2E_REPEAT=2`
completed twice on the same Kimi route with independent WorkerSession,
WorkerRun, RuntimeInstance, AgentSession and AgentTurn identities.

- [x] **Step 5: Run the complete repository-local verification**

```bash
cd spikes/worker-runtime
make fmt-check
make proto-check
make test
make spike-integration
make spike-e2e
go vet ./...
git diff --check
```

Expected: every command passes. Also inspect:

```bash
docker ps -a --filter label=switchback.spike.managed=true
git status --short
```

Expected: no managed Worker remains; only intended implementation changes are present.

- [x] **Step 6: Update README with verified facts only**

Document prerequisites, exact commands, runtime data locations, expected acceptance output, cleanup/purge difference, trusted root-init boundary, Docker Desktop/Linux network-policy gap, and sidecar follow-up. Do not claim recovery, full egress enforcement or production readiness.

- [x] **Step 7: Commit E2E acceptance as the final implementation commit**

```bash
git add spikes/worker-runtime/test/e2e spikes/worker-runtime/scripts/run-e2e spikes/worker-runtime/scripts/verify-langfuse spikes/worker-runtime/Makefile spikes/worker-runtime/README.md spikes/worker-runtime/image-lock.json
git commit -m "test: validate real worker runtime spike"
```

The closeout was split by reviewable behavior: `edef367` contains the
LiteLLM/Codex compatibility implementation, `0aca70b` records the Kimi and
DeepSeek compatibility evidence, and `112c379` contains the acceptance and
security hardening. Runtime evidence and credentials remain ignored.

## Final Review Checklist

Before declaring the Spike complete, verify each design acceptance item against evidence rather than code presence:

- [x] One command starts Backend, real Worker and the full Langfuse topology without UI setup.
- [x] Backend dynamically creates one sibling Worker from server-owned options.
- [x] Worker inspect proves no socket, privileged mode, host network, host port or arbitrary mount.
- [x] ACP raw frames prove typed initialize/auth/new-session/two-prompt flow.
- [x] Codex/Tool identity cannot read the upstream credential; trusted Backend/root-init boundary is documented.
- [x] Same ACP session, container and WorkingCopy span both turns.
- [x] Per-turn patch boundaries are incremental and host Fixture is unchanged.
- [x] JSONL/object manifests and checksums are authoritative and complete.
- [x] Known tokens are absent from persisted evidence and Langfuse projection.
- [x] Langfuse exact Trace query returns both AgentTurn spans and model generations.
- [x] Langfuse failure leaves completed execution intact but fails Spike acceptance.
- [x] Cleanup reports and removes every dynamically created Worker.
- [x] No production-only feature listed under Global Constraints was accidentally added.

## Commit Review

The original implementation plan expected fourteen ordered task commits. Review follow-ups and real-provider compatibility fixes were kept as additional focused commits instead of being squashed into their originating tasks. Generated protobuf and npm lockfile changes stay with their owning protocol/image commits; runtime evidence, `.spike-data`, local env files, model credentials and container logs are never staged. Before each commit run `git diff --cached --stat` and `git diff --cached --check`; if a task exceeds one reviewable behavior slice, split it without changing dependency order or squashing unrelated work together.
