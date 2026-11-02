# Durable Observability Projection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地已批准观测设计的第一片：可从封存 Trace 重建的 Langfuse 持久化异步投影，执行不等待观测后端。

**Architecture:** artifacttrace 拥有封存观察源、发现/作业/投影实现；localproduct 只在原证据冻结点捕获源，并独立运行后台 worker。PostgreSQL 激活目录和作业元数据管理补投，ObjectStore 仍保存权威源。旧投影保持只读兼容，新投影使用 `langfuse.projection.v2` 命名空间。

**Tech Stack:** 现有 Go / pgx v5 / PostgreSQL / ObjectStore / 锁定 self-hosted Langfuse v3 HTTP ingestion；不新增 SDK、服务镜像升级或部署系统。

## Global Constraints

- 本变更只实现已批准设计的第一片；Prometheus / Loki / Grafana、Tool/MCP 深化和 YHC Harness 各在后续变更实现。
- PostgreSQL execution state and ObjectStore Trace JSONL are authoritative evidence; observability is derived only.
- HTTP and ObjectStore I/O MUST occur outside database transactions.
- Backend startup/readiness, Runtime terminal settlement and next-turn dispatch MUST NOT wait for observability.
- 全部身份必须绑定 exact Tenant/User/Workspace/WorkerSession/WorkerRun/AgentTurn/Runtime generation；缺失/冲突 fail closed，不生成虚假 I/O。
- 完整 Turn/model I/O 不做语义截断；已知凭据不得进入源快照、HTTP 或错误文本。阻断使用安全错误码，不改变 execution outcome。
- 新 schema version 建立自己的启用边界，不自动回灌历史。旧 Manifest、旧 checkpoint 与旧 Langfuse 数据保持可读，不改写历史源。
- Local lease fencing protects database completion, not remote HTTP; stable immutable payloads make late batches harmless.
- 不启用 Runtime 自动恢复，不修改 TaskSpec / IntegrationEvent / ArtifactRef authority，不改 Truth / ContextPack，不创建公开 replay API。
- 仅在 `/private/tmp/Switchback-observability.H4SQTg/worktree` 修改实现；原工作区未提交内容不动。不 push / PR / merge。

## Verification scope

基线 `env GOCACHE=/tmp/Switchback-go-cache go test -short ./internal/artifacttrace/... ./internal/localproduct` 已通过。每项先 RED 再 GREEN，测试通过相同 public seam；PG 用 `SWITCHBACK_TEST_DATABASE_URL` 的隔离 schema。没有 DATABASE_URL 导致的 Skip 不计作 PG 通过。根代理提供隔离临时 PostgreSQL，Docker / 真实 Langfuse / 真实模型未跑不得声称通过。实现接线测试必须用阻塞 exporter 验证 terminal/next-turn 无等待，而非字符串查找替代运行行为。

---

### Task 1: Seal and reload observation source evidence

**Files:**

- Create: `internal/artifacttrace/application/observation_source.go`
- Create: `internal/artifacttrace/application/observation_source_test.go`
- Modify: `internal/artifacttrace/application/finalizer.go`
- Modify: `internal/artifacttrace/domain/turn_evidence.go`
- Test: `internal/artifacttrace/application/finalizer_test.go`
- Test: `internal/artifacttrace/domain/turn_evidence_test.go`

**Interfaces:**

Consumes existing `artifactdomain.TurnIdentity`, `workerapp.ModelObservation`, `workerapp.ObjectStore` and `contractsv1.TraceManifest`. Produces:

```go
const ObservationSourceSchemaVersion = "turn.observation.source.v1"
const ObservationSourceSegmentKind = "turn-observation-source"
type TurnObservationSource struct {
    SchemaVersion string `json:"schema_version"`
    Identity artifactdomain.TurnIdentity `json:"identity"`
    StartedAt time.Time `json:"started_at"`
    EndedAt time.Time `json:"ended_at"`
    Input json.RawMessage `json:"input"`
    ModelObservations []workerapp.ModelObservation `json:"model_observations"`
    ErrorCode string `json:"error_code,omitempty"`
}
func EncodeTurnObservationSource(TurnObservationSource) ([]byte, error)
func DecodeTurnObservationSource([]byte) (TurnObservationSource, error)
func ReadTurnObservationSource(context.Context, workerapp.ObjectStore, contractsv1.TraceManifest) (TurnObservationSource, error)
```

Encoding is deterministic JSONL: one source record preserving complete input and sorted, unique ObservationID models. Zero model observations is an incomplete source, never a synthetic model. Safe `ErrorCode` markers contain no content; support `source_incomplete` and `credential_detected`. A marker may omit input/models but still carries exact identity/time. Normal sources require non-null input, valid model I/O and all exact scope/runtime identities. Known-token scanner is invoked by capture (Task 5) and outbound exporter (Task 3), not invented inside domain.

- [ ] **Step 1: Add behavioral failing tests.** Extend the real finalizer fixture with this exact source, finalize, reload through the ObjectStore reader, and compare original timestamps and complete nested I/O. Test tampering source bytes, mismatched manifest identity, duplicate/mismatched model ID, missing segment and legacy finalization without source. Test reordering models yields equal encoded bytes.

```go
before, err := EncodeTurnObservationSource(source)
if err != nil { t.Fatal(err) }
slices.Reverse(source.ModelObservations)
after, err := EncodeTurnObservationSource(source)
if err != nil { t.Fatal(err) }
if !bytes.Equal(before, after) { t.Fatal("source encoding changed with model order") }
decoded, err := DecodeTurnObservationSource(before)
if err != nil { t.Fatal(err) }
if !decoded.StartedAt.Equal(source.StartedAt) { t.Fatal("original start time lost") }
```

- [ ] **Step 2: Run RED.** `env GOCACHE=/tmp/Switchback-go-cache go test ./internal/artifacttrace/application ./internal/artifacttrace/domain` fails because new source functions do not exist.
- [ ] **Step 3: Implement source/finalizer extension.** Add optional `ObservationSourceJSONL []byte` to `FinalizeTurnEvidenceCommand` and `BuildTurnEvidenceInput`. Application decodes and validates same identity before any writes; domain preserves opaque JSONL as an additional `EvidenceObject` without importing worker application. `buildManifest` includes that object as a third Trace segment, with its own correct count/range/checksum; existing raw/normalized segment bytes and counts stay unchanged. Empty optional input keeps historical output behavior. Reader requires one exact segment, checksum/size/count validation, then validates source against manifest. Object read is size-limited by manifest size plus one, without arbitrary content truncation.

```go
if len(command.ObservationSourceJSONL) != 0 {
    source, err := DecodeTurnObservationSource(command.ObservationSourceJSONL)
    if err != nil { return FinalizedTurnEvidence{}, err }
    if !reflect.DeepEqual(source.Identity, command.Identity) {
        return FinalizedTurnEvidence{}, errors.New("observation source identity mismatch")
    }
}
```

- [ ] **Step 4: Run GREEN.** Same command passes; `go test -short ./internal/artifacttrace/...` passes, including old finalizer tests. Record actual assertions, command and output in report.
- [ ] **Step 5: Commit only Task 1 files.** `git add internal/artifacttrace && git commit -m "feat(observability): seal reconstructable turn observation sources"`.

### Task 2: Persist activation, discovery and fenced source jobs

**Files:**

- Create: `migrations/000023_durable_observability_projection.sql`
- Create: `internal/artifacttrace/application/projection_jobs.go`
- Create: `internal/artifacttrace/adapter/postgres/projection_jobs.go`
- Create: `internal/artifacttrace/adapter/postgres/projection_sources.go`
- Modify: `internal/artifacttrace/adapter/postgres/publisher.go`
- Test: `internal/artifacttrace/adapter/postgres/publisher_integration_test.go`
- Create: `internal/artifacttrace/adapter/postgres/projection_jobs_integration_test.go`
- Create: `internal/artifacttrace/application/projection_source_loader.go`
- Create: `internal/artifacttrace/application/projection_source_loader_test.go`
- Create: `internal/artifacttrace/application/projection_json.go`
- Test: `internal/artifacttrace/application/projection_json_test.go`

**Interfaces:**

Consumes Task 1 `ReadTurnObservationSource`. Produces the following application contracts (add explicit exported fields needed to persist job timing/attempts; no HTTP payload in jobs):

```go
const DurableProjectionSchemaVersion = "langfuse.projection.v2"
const MaxProjectionLease = 5 * time.Minute
var ErrProjectionLeaseLost = errors.New("projection lease lost")
type ProjectionSourceRef struct { TraceStreamID, ManifestChecksum string }
type ProjectionJob struct {
    Source ProjectionSourceRef
    SchemaVersion string
    LeaseToken int64
    Attempts int
}
type ProjectionCompletion struct {
    Status string // passed, retryable, blocked
    ErrorCode string
    RetryAt time.Time
}
type ProjectionJobStore interface {
    Activate(context.Context, string) error
    Discover(context.Context, string, int) (int, error)
    Claim(context.Context, string, time.Duration) (ProjectionJob, bool, error)
    Finish(context.Context, ProjectionJob, ProjectionCompletion) error
    RequestSource(context.Context, string, ProjectionSourceRef) error
}
type ProjectionRunRoot struct {
    TenantID, UserID, WorkspaceID, WorkerSessionID, WorkerRunID string
    StartedAt time.Time
    Input json.RawMessage
}
type ProjectionSourceMetadata struct {
    Root ProjectionRunRoot
    RootManifest contractsv1.TraceManifest
    Manifest contractsv1.TraceManifest
    ManifestObject workerapp.StoredObject
    Report contractsv1.ArtifactRef
}
type ProjectionSourceMetadataStore interface {
    LoadProjectionSourceMetadata(context.Context, ProjectionSourceRef) (ProjectionSourceMetadata, error)
}
type SourceProjection struct {
    SchemaVersion string
    Source ProjectionSourceRef
    Root ProjectionRunRoot
    Turn TurnObservationSource
    Output json.RawMessage
}
type ProjectionSourceLoader struct {
    Metadata ProjectionSourceMetadataStore
    Objects workerapp.ObjectStore
}
func (ProjectionSourceLoader) Load(context.Context, ProjectionJob) (SourceProjection, error)
func ProjectionJSONEqual(json.RawMessage, json.RawMessage) bool
```

Postgres adapter: `NewProjectionJobStore(*pgxpool.Pool)` implements both stores. Preserve legacy `MetadataStore.RecordObservabilityProjection`; do not route v2 through its whole-Run mutable-shape constraint. Current `publicationObjectRefs` hardcodes five objects: replace this with an exact required-kind set (one manifest, raw ACP, normalized, report, patch), plus at most one known observation source; reject unknown/duplicate/missing kinds. Verify legacy five refs, source six refs and idempotent publication in real PostgreSQL. This is part of Task 2's real publication-to-discovery integration, not an arbitrary loosening of publication checks.

- [ ] **Step 1: Add failing isolated PG tests.** Cover first activation excludes visible history, repeat activation does not exclude a later source, source publication with older `visible_at` but commit after activation is discovered, duplicate discovery, publication without enqueue, exact-source request checksum mismatch, schema-v3 independent boundary, two concurrent claimers, expired lease reclaim and stale Finish rejected. Seed real Trace metadata via existing fixture helpers.

```go
if err := store.Activate(ctx, version); err != nil { t.Fatal(err) }
if n, err := store.Discover(ctx, version, 50); err != nil || n != 0 {
    t.Fatalf("historical discovery: n=%d err=%v", n, err)
}
// The fixture commits another real manifest after this activation.
if n, err := store.Discover(ctx, version, 50); err != nil || n != 1 {
    t.Fatalf("new source discovery: n=%d err=%v", n, err)
}
```

- [ ] **Step 2: Run RED.** `env GOCACHE=/tmp/Switchback-go-cache go test -tags=integration ./internal/artifacttrace/adapter/postgres -run Projection -count=1` with actual `SWITCHBACK_TEST_DATABASE_URL`, failing for missing adapter/contracts.
- [ ] **Step 3: Implement migration and store.** Atomic first-activation transaction inserts activation and exact visible source/checksum exclusion rows once; `INSERT ... ON CONFLICT` concurrent activations cannot reset baseline. Discover is bounded repeated anti-join, not a moving timestamp cursor. Job key `(schema_version, trace_stream_id, manifest_checksum)`; source identity changes must be rejected, not create silent replacement history. Claim database-clock lease via `FOR UPDATE SKIP LOCKED`, monotonically increment token, accepting only `0 < lease <= MaxProjectionLease`. Finish requires same key/token, running status and `lease_until > clock_timestamp()`; any stale completion returns `ErrProjectionLeaseLost`.

```sql
UPDATE observability_projection_jobs
SET status=$4,error_code=$5,next_attempt_at=$6,lease_until=NULL
WHERE schema_version=$1 AND trace_stream_id=$2 AND lease_token=$3
  AND status='running' AND lease_until > clock_timestamp();
```

Load metadata validates exact source checksum and scope joins, immutable initial Turn input and Run started_at; root excludes latest outcome/runtime/Turn list. Current schema has no Turn sequence: all three Run creation paths create their initial prompt/queue Turn with the same `created_at` as the Run, so anchor to that exact timestamp and require exactly one matching prompt/queue Turn. Missing/ambiguous anchors are blocked, not guessed from a mutable newest/oldest list. `RootManifest` references that initial Turn's published source; load it with Task 1 reader and require complete/non-marker source and semantically equal initial input before accepting Root.Input. This prevents a later healthy Turn from re-exporting an earlier credential-blocked initial input after token rotation. A legacy initial source is non-backfillable rather than silently trusted. Read and checksum-verify current manifest object, source segment and report object; verify all cross-object identities, then encode report text as JSON string (same legacy output convention). No object reads inside PG transactions. Missing/invalid source returns a typed safe permanent projection error; reuse a small application `ProjectionError{Code string; Permanent bool}` type for Tasks 3/5.

Temporary PG/ObjectStore I/O and cancellation must remain retryable safe typed errors, never permanent source corruption. Missing/invalid schema, source markers and checksum/identity mismatches are permanent. `RequestSource` must return any unclassified existing-job query failure, never claim idempotency success without proof. `ProjectionJSONEqual` compares JSON without float64 precision loss, retaining semantic equality of 1 and 1.0 and rejecting distinct integers above 2^53; its number algorithm must not expand unbounded exponents. Shared helper serves both root-source and Task 3 readback comparisons.

- [ ] **Step 4: Run GREEN.** Focused PG tests include actual open publication transaction across activation and concurrent first activation, not only old timestamps; loader tests cover transient fault retry and lossless numeric equality. Migration up/down tests if repo has them, and `go test -short ./internal/artifacttrace/...` pass. Explicitly report if integration test skipped (not success).
- [ ] **Step 5: Commit Task 2 only.** `git add migrations/000023_durable_observability_projection.sql internal/artifacttrace && git commit -m "feat(observability): persist activation discovery and fenced projection jobs"`.

### Task 3: Deterministic immutable Langfuse projection

**Files:**

- Create: `internal/artifacttrace/adapter/langfuse/durable_projector.go`
- Create: `internal/artifacttrace/adapter/langfuse/durable_projector_test.go`
- Modify: `internal/artifacttrace/adapter/langfuse/client.go`
- Test: `internal/artifacttrace/adapter/langfuse/client_test.go`
- Modify: `internal/artifacttrace/application/projection_jobs.go`

**Interfaces:**

Consumes `SourceProjection`, `ProjectionError`, `ProjectionJSONEqual` and `KnownTokenScanner`. Use the shared lossless JSON comparator for readback content instead of float64 JSON decoding. Preserve legacy `API`; add a source-scoped durable read seam to the same HTTP client:

```go
type SourceProjector interface {
    ExportAndVerify(context.Context, SourceProjection) error
}
type DurableAPI interface {
    Ingest(context.Context, IngestionBatch) error
    GetTraceRoot(context.Context, string) (Trace, error)
    GetObservation(context.Context, string) (Observation, error)
}
func NewDurableProjector(DurableAPI, KnownTokenScanner) (*DurableProjector, error)
func (*DurableProjector) ExportAndVerify(context.Context, artifactapp.SourceProjection) error
```

This adapter performs one bounded ingest plus source-scoped readback attempt; worker handles persistent scheduling. Missing/indexing-not-ready observation -> retryable; content/identity collision -> permanent safe code. Exact HTTP timeout comes from worker context and existing client. Preserve legacy projector behavior/tests; optional response fields needed for new comparisons may be added compatibly. `GetTraceRoot` explicitly requests `fields=core,io`; `GetObservation` uses the exact `/api/public/observations/{id}` legacy endpoint. This avoids fetching all historical model I/O on every Turn. Official v3.217.0 response uses `model`, `usageDetails` and legacy `usage.input/output`, not internal `inputUsage/outputUsage`; verify using its public fields. Upstream `z.coerce.date()` / JS Date round-trip has millisecond precision: transport timestamps use UTC milliseconds, while metadata retains original RFC3339Nano source timestamps. Test this distinction with nonzero nanoseconds. Sources: [trace endpoint](https://github.com/langfuse/langfuse/blob/v3.217.0/web/src/pages/api/public/traces/%5BtraceId%5D.ts), [observation types](https://github.com/langfuse/langfuse/blob/v3.217.0/web/src/features/public-api/types/observations.ts).

Physical v3.217.0 readback on 2026-09-10 proved native JSON numeric `9007199254740993` becomes `9007199254740992`. The pending, unreleased v2 adapter must encode every root/Turn/model I/O field uniformly as `{"encoding":"json-text.v1","json":string(originalRawJSON)}`. Preserve original source bytes, validate JSON and UTF-8 before wrapping, compare the full explicit envelope without float64 coercion, and retain root output absence. Scan the original source before extra escaping and scan the final batch before HTTP with the same safe failure mapping. Usage remains numeric but input/output/sum must fit `2^53-1`, checked without addition overflow. This retains complete inspectable I/O at the cost of Langfuse's native structured/chat rendering. Legacy projector and source schemas are unchanged; this pre-release correction must not be applied over deployed immutable v2 data. Test through a float64-decoding HTTP fixture plus fresh real-service IDs, including high-precision/exponent JSON, invalid UTF-8, encoding/content collisions and escaping-sensitive credentials.

The real-service follow-up also proved flat metadata strings are parsed as JSON literals: string `"1"` reads back as number `1`. Pinned upstream writes existing string KV values unchanged and parses each KV once on read. Therefore encode each flat metadata value for transport as `string(json.Marshal(originalValue))`, preserving expected/readback metadata in its native original form. This protects numeric/boolean/null/object-looking identity strings without nesting fields or changing the Client's public shape. Runtime generation remains native `uint32` (its actual domain type is already exactly representable), not a decimal string; test `MaxUint32`. The earlier string-generation suggestion was disproved by physical evidence. Both fake API and HTTP fixture must emulate the pinned per-KV write/read transform; add identity-literal and exact inner `9007199254740993` to `9007199254740992` collision regressions. Keep original-source and final-batch scanning around the extra metadata escaping.

- [ ] **Step 1: Write failing tests at API seam.** Capture two same-source batches and assert byte-equivalent IDs/timestamps/body; export two Turns then late Turn one and assert identical root plus Turn-two preserved; readback accepts extra newer observations; rejects a matching-ID wrong scope/I/O/model/time/token/status; known-token scanner failure sends zero HTTP; metadata error strings contain no input.

```go
digest := sha256.Sum256([]byte("langfuse.projection.v2\x00run\x00" + scopeKey))
traceID := hex.EncodeToString(digest[:16])
// All Turn/model/event IDs use role-separated canonical seeds as well.
```

- [ ] **Step 2: Run RED.** `env GOCACHE=/tmp/Switchback-go-cache go test ./internal/artifacttrace/adapter/langfuse -run Durable -count=1` fails for absent durable adapter.
- [ ] **Step 3: Implement batch and verification.** Seed IDs with canonical encoded arrays, not ambiguous concatenation. Run seed includes version and tenant/user/workspace/session/run only. Root input/start from `ProjectionRunRoot`; root event timestamp identical across all Turns, no root output/end/current runtime fields. Turn/model events include manifest checksum/source identity and role/observation identity and use original times. Sort models; retry recreates identical JSON. Turn span contains full input/output, true runtime/AgentSession provenance; generation includes full model I/O, model/route/usage/status/times. Scan exact full batch before HTTP. Verify only exact current source observations plus immutable root and compare meaningful fields, allowing unrelated later Turns. Source validation happens before scanner and HTTP. Do not rely on local lease to prevent remote overwrite.

```go
if err := projector.scanner.ScanJSON(ctx, "langfuse.ingestion.batch", batch); err != nil {
    if ctx.Err() != nil { return &artifactapp.ProjectionError{Code: "projection_cancelled"} }
    if secretcheck.IsExposure(err) {
        return &artifactapp.ProjectionError{Code: "credential_detected", Permanent: true}
    }
    return &artifactapp.ProjectionError{Code: "scanner_unavailable"}
}
if err := projector.api.Ingest(ctx, batch); err != nil {
    return &artifactapp.ProjectionError{Code: "ingestion_unavailable"}
}
```

- [ ] **Step 4: Run GREEN.** Focused durable tests plus all legacy Langfuse adapter tests pass; race tests cover fake exporter state and repeated/concurrent source builds.
- [ ] **Step 5: Commit Task 3 only.** `git add internal/artifacttrace && git commit -m "feat(langfuse): export immutable source-scoped deterministic projections"`.

### Task 4: Retain a Backend-only runtime token scanner across restart

**Files:**

- Create: `internal/workerruntime/adapter/runtimesecret/observation_registry.go`
- Create: `internal/workerruntime/adapter/runtimesecret/observation_registry_test.go`
- Modify: `internal/workerruntime/adapter/runtimesecret/materializer.go`
- Test: `internal/workerruntime/adapter/runtimesecret/materializer_test.go`
- Modify: `internal/platform/secretcheck/scanner.go`
- Test: `internal/platform/secretcheck/scanner_test.go`

**Interfaces:**

Current Materializer owns generated proxy/model-sink/control/provider tokens, but bootstrap/model inputs are deleted after Docker injection and live-credential escrow keeps only control. New registry is a private immutable `control-plane/observation-tokens.json` beside existing live credentials, never a Worker mount or public contract. Produce a scanner-only reader (no token slice getter):

```go
type ObservationRegistryRequest struct {
    WorkerSessionID string
    RuntimeInstanceID string
    Generation uint64
    SessionStorage application.SessionStorage
}
func (Materializer) ObservationScanner(ObservationRegistryRequest) (*secretcheck.Scanner, error)
```

Reader identity deliberately excludes Run/AgentSession/ChannelEpoch because a resident Runtime serves later Runs without regenerating its tokens. Session + exact Runtime generation is the token authority. Current Langfuse/config keys remain separately scanned in Task 5. Existing exact Runtime/Session secret-root cleanup deletes registry; no new broad deletion or credential export surface. Add redacted `String` / `GoString` to `secretcheck.Scanner` as well: returning only a scanner must not expose its private token slice through ordinary diagnostic formatting.

- [ ] **Step 1: Write RED tests.** Materialize using synthetic tokens, delete only transient bootstrap/model inputs as Docker does, reconstruct a fresh Materializer and obtain scanner. Each of all four credentials is detected in nested JSON; clean content passes. Verify 0400 file permissions, symlink/non-root-owned file rejection through injected OwnerUID, identity/generation mismatch, missing/legacy registry, unknown schema and trailing content. Registry write failure must not fail core Materialize; scanner then reports unavailable.

```go
scanner, err := materializer.ObservationScanner(request)
if err != nil { t.Fatal(err) }
if err := scanner.ScanJSON(ctx, "source", map[string]string{"text": proxyToken}); !secretcheck.IsExposure(err) {
    t.Fatalf("generated proxy credential was not detected: %v", err)
}
```

- [ ] **Step 2: Run RED.** `env GOCACHE=/tmp/Switchback-go-cache go test ./internal/workerruntime/adapter/runtimesecret -run 'Observation|Materialize' -count=1` fails for absent scanner method.
- [ ] **Step 3: Implement the registry behind Materializer.** During Materialize, privately encode deduplicated provider/control/proxy/model-sink tokens with Session/Runtime/generation and `switchback.runtime-observation-tokens.v1`; use existing root-only exclusive file writer and validated server-derived paths. Registry writing is observability-only: on failure emit a fixed sanitized warning (never raw error/path/value), leave normal bootstrap/model/live-credential provisioning usable, and let source capture produce `source_incomplete`. Reader checks the server-owned directory chain from `SessionStorage.BackendRoot` through `runtimes/<runtime>-g<generation>/control-plane` consists of real private root-owned directories, then the exact regular root-owned 0400 file, bounded 64 KiB size, single strict JSON object and exact identity. Ancestors above the supplied server-owned Session root remain the existing storage resolver's responsibility; do not require global system directories such as `/` or `/tmp` to be private. Return only `*secretcheck.Scanner`, sanitize all failure errors, and give private registry struct redacted String/GoString to prevent accidental diagnostic leakage. No changes to existing live-credential schema or Bootstrap payload/mounts.

```go
if err := m.sealObservationRegistry(request, bootstrap); err != nil {
    slog.Warn("Runtime observation registry unavailable", "code", "observation_registry_unavailable")
}
```

- [ ] **Step 4: Run GREEN.** Focused and full runtimesecret tests pass; existing Docker secret-volume tests still prove only transient bootstrap/model mounts. Run `go test -short ./internal/workerruntime/adapter/runtimesecret ./internal/workerruntime/adapter/docker` and record outcomes.
- [ ] **Step 5: Commit Task 4 only.** `git add internal/workerruntime/adapter/runtimesecret internal/platform/secretcheck && git commit -m "feat(observability): retain scoped runtime token scanner across restart"`.

### Task 5: Wire independent worker, freeze runtime sources and remove startup coupling

**Files:**

- Create: `internal/artifacttrace/application/projection_worker.go`
- Create: `internal/artifacttrace/application/projection_worker_test.go`
- Create: `internal/localproduct/observability_runner.go`
- Create: `internal/localproduct/runtime_observation_source.go`
- Create: `internal/localproduct/runtime_observation_source_test.go`
- Modify: `internal/localproduct/runtime_runner.go`
- Modify: `internal/localproduct/runtime_settlement.go`
- Modify: `internal/localproduct/runtime_settlement_recovery.go`
- Modify: `internal/workerruntime/application/turn_settlement_checkpoint.go`
- Test: `internal/workerruntime/application/turn_settlement_observation_checkpoint_test.go`
- Modify: `internal/localproduct/app.go`
- Modify: `internal/localproduct/config.go`
- Modify: `internal/localproduct/config_test.go`
- Modify: `internal/localproduct/assistant_runner.go`
- Test: `internal/localproduct/assistant_runner_test.go`
- Modify: `deploy/local/compose.yaml`
- Modify: `deploy/local/init-postgres.sh`
- Test: `internal/localproduct/runtime_settlement_test.go`
- Create: `internal/localproduct/observability_settlement_integration_test.go`
- Test: `internal/architecture/local_compose_test.go`
- Create: `docs/operations/durable-observability-projection.md`

**Interfaces:**

Consumes Tasks 1–4 contracts. Background worker has independently testable `Tick(context.Context) error` and context-cancellable `Run(context.Context)`; it never becomes a dependency of Runtime runner or its fatal channel. Defaults: discover batch 50; one claim at a time; 30s lease; 15s job context; 1s poll; retry exponential from 5s capped at 5m. Typed permanent errors -> blocked, transient -> retryable; no source content in diagnostics.

Implementation seam fixed before parallel work: freeze the optional source in the existing digest-named immutable local snapshot pattern, not embedded base64 bytes in cumulative PG checkpoints. Evidence input fields are `ObservationSourceSnapshotFile string` / `observation_source_snapshot_file,omitempty`, `ObservationSourceSize int64` / `observation_source_size,omitempty`, and `ObservationSourceDigest string` / `observation_source_digest,omitempty`. All absent means legacy; otherwise all three must be valid (safe basename, size>0, sha256 digest). Local finalization verifies actual file bytes; the shared pure Worker checkpoint proof binds the optional finalized source object/segment to the frozen scalar size/digest. Generalize the existing private snapshot helper without changing old patch filenames, fsync/hardlink or immutability behavior. The independent proof slice owns only `turn_settlement_checkpoint.go` and its new focused test file; the main Task5 implementer owns all localproduct/worker-loop/config changes. Root serializes Git staging/commits and performs combined review/verification after the non-overlapping implementations join.

- [ ] **Step 1: Write failing behavior tests.** Background worker discovers/claims/loads/projects/completes via injected stores; blocked exporter does not block runtime settlement/next-turn under a separate goroutine. Config: all empty disables; partial/malformed config disables with safe diagnostic instead of failing core; full valid preserves enabled. Settlement test freezes journal observations before terminal cut, restarts with empty live map, and asserts identical sealed source digest. Include old checkpoint replay without source, credential-marker, model duplicate/conflict and source-event sequence cutoff.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go worker.Run(ctx)
select {
case <-settlementCompleted:
case <-time.After(5 * time.Second):
    t.Fatal("execution waited for observability")
}
```

- [ ] **Step 2: Run RED.** Focused `go test -short ./internal/localproduct ./internal/artifacttrace/application` fails on missing independent runner/capture behavior; integration settlement fixture uses real PG.
- [ ] **Step 3: Implement freeze and runner.** In `prepareRuntimeSettlementEvidence`, read already applied `model.observation` journal rows scoped to current Turn/runtime/generation and at/before the terminal source sequence boundary. Read immutable Turn input/start and terminal evidence time. Sort/dedup identically by ObservationID, rejecting conflicts; scan before persisting snapshot. Freeze `ObservationSourceJSONL` bytes (or immutable local snapshot ref/digest with existing snapshot helpers) in evidence checkpoint before `EvidencePrepared` CAS. Later finalization passes exact frozen bytes to Task 1; failed/cancelled legacy finalizer path gets the same capture helper where safe, otherwise explicit incomplete marker. Old checkpoint without source retains old bytes; do not retrofit during replay. Remove all synchronous `projectTurnObservability` call sites and live-map-as-projection-authority usage. The new worker discovers by publication, without Runtime enqueue dependency.

```go
for ctx.Err() == nil {
    if err := worker.Tick(ctx); err != nil && ctx.Err() == nil {
        worker.Report("observability_worker_unavailable")
    }
    select {
    case <-ctx.Done(): return
    case <-ticker.C:
    }
}
```

Source capture uses Task 4 runtime scanner plus deduplicated non-empty current config tokens; missing runtime registry or source cut produces content-free `source_incomplete`. Before freezing, scan both full source JSON and the exact prospective stage report built deterministically from the already-frozen raw ACP range/no-files-changed input (reuse pure `artifactdomain.BuildTurnEvidence` and its report object; do not duplicate report parsing). A credential found only in the report must still create `credential_detected`, preventing later export after Runtime registry cleanup. Existing core Artifact availability/bytes are unaffected by this observation marker. It must not fabricate a no-op scanner when configuration fields are empty. Source marker lookup is distinct from actual PostgreSQL failure (core failure). Build optional exporter outside Runtime construction; invalid configuration logs only fixed safe codes and leaves observation worker disabled. Assistant optional exporter construction must degrade similarly, without changing its required evidence/model dependencies or expanding to an Assistant durable migration. App starts/stops worker independently; shared core PG schema validation does not perform remote observability probes. Remove backend Compose `depends_on: langfuse-web`; update topology regression test to assert core independence. Keep Langfuse image pins. Provide SQL status/retry diagnostics against new jobs; do not claim old Run-level UI projection reads v2 unless actually wired and tested.

Content-free markers still contain identity metadata: check any available scanner against the final marker before persistence. If known credential content is present even in required identity fields, omit the optional observation segment with a fixed diagnostic instead of persisting an unsafe marker or changing the execution result. Missing scanner availability is distinct from confirmed exposure. Document feature disablement as stopping the worker on the new binary, not a promise that a pre-source binary can read new checkpoint fields; do not downgrade migrations or remove source evidence for rollback.

The shared Worker application checkpoint proof decoder is strict and currently hardcodes four finalized objects/two Trace segments. Extend its scalar/opaque evidence-input proof compatibly for the frozen optional source (without importing artifacttrace application and creating a cycle). Retain exactly the original four required objects/two segments; permit exactly one additional `turn-observation-source` object/segment only when its size/checksum match the frozen source bytes or validated immutable snapshot proof. Missing/extra/duplicate/mismatched source proof must fail. Old source-free checkpoints still validate unchanged. Add focused public `ValidateTurnSettlementCheckpointProof` tests for legacy and source-enabled proof, including tampering and unknown kinds; do not merely loosen total counts.

The real-PG outage fixture is a second independent test-only slice owning only the new `observability_settlement_integration_test.go`; it consumes the existing settlement and background-worker boundaries and cannot add production injection overrides. Production changes remain with the main implementer; root joins all three slices before the same Task5 gate.

Fresh Docker initialization exposed an existing local bootstrap bug: `init-postgres.sh` creates databases as `POSTGRES_USER=switchback` but invokes the migration script without `PGUSER`, so libpq defaults to the OS account `postgres` and initialization exits with `role "postgres" does not exist`. Preserve the configured database authority by passing `PGUSER="$POSTGRES_USER"` to that migration invocation. Do not create a second role or modify old migrations. Lock down the startup contract and verify with fresh-volume startup evidence; existing initialized volumes are not proof that the first-init path works.

- [ ] **Step 4: Validate and document.** Run focused + race tests, actual PG restart/discovery/settlement tests, architecture tests, `go test -short ./...` and OpenSpec strict validation. Add operation docs with enable/disable, durable activation, safe exact-source request boundary, retries/blocked codes, no implicit historical backfill, version compatibility and honest pending Docker acceptance. For fixture outage acceptance run 20 healthy/outage two-Turn pairs where feasible and record actual latency; otherwise leave G4 incomplete rather than substituting a count-free assertion.

For the fixture, preserve the real PostgreSQL settlement coordinator and queued continuation application path; fake only Runtime/Agent I/O and the remote SourceProjector. Establish exporter blocking through a channel before the measured terminal/next-turn transition (a separately identified warm-up source is allowed). Do not treat the old immediate-failure `run_observability_projections` test or a direct journal Apply-only test as proof of the new localproduct path. Keep the new fixture in its focused integration file rather than expanding the already large app integration file. Report raw paired durations and p95, with fixture results distinct from real Docker/model G4.
- [ ] **Step 5: Commit Task 5 files.** `git add internal/artifacttrace internal/localproduct internal/architecture/local_compose_test.go deploy/local/compose.yaml docs/operations/durable-observability-projection.md && git commit -m "feat(observability): decouple runtime execution from background projection"`.

### Task 6: Verify the Langfuse slice with controlled physical execution

**Files:**

- Create: `test/acceptance/observability/Dockerfile.worker`
- Create: `test/acceptance/observability/litellm.yaml`
- Create: `test/acceptance/observability/compose.override.yaml`
- Create: `scripts/run-observability-isolation-acceptance`
- Create: `test/acceptance/observability/driver.go` if a typed HTTP/SQL/report driver is needed rather than embedding substantial logic in shell.
- Test: focused acceptance-driver/architecture tests beside the new driver or existing script-contract tests.
- Update: `docs/operations/durable-observability-projection.md`

**Boundary:** This closes the physical Langfuse portion of the approved first slice, not the future Loki/Prometheus/Grafana or G5 Harness gates. G4 latency uses controlled immediate model/tool fixtures, not paid-provider calls. Reuse existing spike fake ACP/OpenAI sources via their own build module and a clearly labelled test-only derived Worker image; preserve production shim, control transport, Backend, PostgreSQL, ObjectStore and settlement. The nominal Codex profile in a fixture is not proof of real Codex execution. No production executable/profile/config escape, no extra privileges and no real provider credentials. Read `/private/tmp/Switchback-observability.H4SQTg/g4-acceptance-path.md` for verified reuse points; the Task 5 source is the current integration authority, not that earlier snapshot.

- [ ] **Step 1: Add safe fixture orchestration and tests.** Require a uniquely claimed exact project, data root, image tags/digests and loopback ports. Derive a test image from the current Worker, installing the controlled ACP binary at its fixed path and a test-only LiteLLM Responses route retaining the production observation callback. Use fresh synthetic key files, never existing provider secrets or fallback endpoints. Drive actual Gateway Task/Session APIs and query actual settlement/jobs; do not build another control hub or seed terminal states. Test unsafe/default project rejection, report coverage/count/threshold checks and deterministic output claims before physical execution.
- [ ] **Step 2: Prove one healthy two-Turn smoke.** Build isolated test images and run production Backend/Worker/shim with the controlled model and Tool fixture. Verify two exact Turns in one Session, original fixture unchanged, real model observation callback/source publication and real Langfuse verification. Fix only a proven missing fake protocol field/endpoint when possible; stop and report if this requires a new production authority boundary. Record that the ACP/model/Tool are test doubles.
- [ ] **Step 3: Run physical cold start, outage and restore.** Stop this test project's observation-only services before creating the Backend process; leave shared core PostgreSQL available, observability configuration enabled and Runtime recovery gate off. Verify core health plus two Turns. Run at least 20 paired healthy/outage two-Turn scenarios; each uses a fresh WorkingCopy/Session. Current Gateway has no Session close command (`sessionCommand` supports follow-up/queue/interrupt/steer/approval only), so use a fresh isolated core database/data-root and Backend process per scenario, tearing down only the exact owned Backend/Worker containers after source recovery is verified. Retain each database and source evidence. This is deployment teardown, not a product Session-close or quota-release claim; do not add a production close/test-override API or mutate quota/Session terminal rows for the fixture. The Langfuse stack may remain shared between these isolated core scenarios. Measure from journal terminal receipt to durable settlement and exact next-Turn dispatch, not frontend polling; preserve raw samples and warmup/order. Every sample must be <=5s and outage p95 increase <=1s, per approved design. Separately exercise connected-but-unanswered ingestion and readback using a local fault proxy, not connection-refused. The Task 5 causally blocked double remains independent evidence.
- [ ] **Step 4: Verify recovery and residue.** Restore the same Langfuse services and require original exact source/checksum jobs to verify via background retry, with no new Task/Run/Turn, duplicate execution or generation change. Machine-readable report records source commit/image digests, executed/passed/failed/skipped, controlled fixture identity, raw latency, source checksums, readback assertions and exact owned resources. Missing scenarios or threshold/residue failures cannot produce slice-ready or exit0. Keep full all-observability G4 unclaimed because later metrics/log services are outside this slice. Document actual results and retained data. Root alone mutates Docker during this session, using only the freshly owned project; remove its containers/networks at finish and retain volumes unless separately authorized to delete them.
- [ ] **Step 5: Commit and review test-only Task 6.** Run focused tests and review the driver before broad execution. Commit only acceptance files/documentation; no push, PR, merge, spec sync or archive.

## Final gate

Independent whole-branch review from base `2a42a59`, fix proven important findings with tests, then report commits/worktree and verified versus unexecuted gates. No design re-approval loop, no archive/sync/push/merge. Subsequent feature slices remain outside this change.
