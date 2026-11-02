# Immutable model observation supplements implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. 本环境没有 executing-plans 技能；若选择单 agent 执行，沿用同一任务和审核门禁，不调用不存在的技能。

**Goal:** 使合法早到/晚到模型在安全持久化后可独立补投，保持旧冻结事实、执行进展和凭据隔离。

**Architecture:** 单 owner 应用模型接收器在 journal Apply 同事务保存安全来源及证明；Run/Turn basis 在首次 evidence-input freeze 同事务保存。ArtifactTrace 后台发布不可变 fragments，再以独立 v3 作业编译、导出和逐对象验证。Runtime secret cleanup 只依赖 exact durable fence，不等待观测后台。

**Tech Stack:** 现有 Go、pgx/PostgreSQL、localfile ObjectStore、ACP/gRPC、锁定 Langfuse v3.217.0 HTTP adapter 与 Docker Compose；不升级依赖。

## Global Constraints

- 基线 `0e01791533f0d093a8dab78d30ac1a4ca0ea345e`；[设计](../specs/2026-09-11-immutable-model-observation-supplements-design.md)已于 2026-09-13 获用户确认。
- 工作目录固定 `/path/to/switchback/.worktrees/observability-supplements-v1`；分支 `codex/feat/observability-supplements-v1`。所有下列 repo-relative 文件名与命令以此为根，不到主工作区实施。
- 原 `turn.observation.source.v1`、TraceManifest、settlement checkpoint 和 v2 job 的冻结事实不变；不放宽 terminal cut、不改写 marker、不等待未来 callback。
- 新来源 `observation.fragment.v1`，角色 `run_basis | turn_basis | model`；新投影 `langfuse.projection.v3`。现有 `DurableProjectionSchemaVersion = "langfuse.projection.v2"` 不修改。
- 所有模型，无论早到或晚到，只有同一个补充来源发布入口。ID 不含到达时间、ChannelEpoch、terminal cut、manifest/publication checksum 或每次重试时间。
- Run/Turn basis 在零模型时仍可成立。Run input 必须来自与 Run 创建绑定的唯一 initial prompt/queue Turn。
- 凭据值不进入 receipt、job、错误、日志或 source 对象；原 scope 缺失不可用新 generation/current config 替代。
- 原 scope 扫描及纯 compiler 在事务外；模型接受/receipt 与 cursor 原子提交；basis receipt 与首次 freeze 原子提交。ObjectStore、HTTP、外部 provider 均不进事务。
- 不新增 Task/Run 状态、deadline、请求 inventory、水位完整性协议、公开 backfill API、真实 fallback transport、自动 Runtime 恢复、看板、Tool/MCP 或 YHC；不执行历史导出、不 push/merge/sync/archive。
- 本地 M1、固定 loopback 身份、既有资源/网络/权限/Session 生命周期不变；不把本片称为 production HA。
- Runtime idle/后续 Turn/replay 都必须能接收原 Turn 的模型事实，但不得因此启动新模型调用、改变执行状态或刷新 idle clock。
- 真实验收沿用 20 healthy/outage pairs、outage p95 增量不超过 1 秒、每次测量不超过 5 秒；fixture、API ACK、skip 或 `not_collected` 均不算真实门禁通过。

---

## 📍 任务地图与核验基线

现有模块调用时序见[已批准设计](../specs/2026-09-11-immutable-model-observation-supplements-design.md#-时序与持久化)，不另造第二份架构图。下表是 review 单位；每步 RED → GREEN → race/集成 → review → scoped commit。不得并行改共享事务/装配文件；可并行只读审核、独立 fixture 准备。

| Task | 可独立审核的交付 | 依赖 | OpenSpec |
| --- | --- | --- | --- |
| 1 | Fragment、纯 compiler、安全 receipt | 无 | 6.1 |
| 2 | PostgreSQL 来源/工作账本 | 1 | 6.2 |
| 3 | 单 owner 模型接受与原子 Apply | 1、2 | 6.3 |
| 4 | Basis 首次 freeze 原子封存 | 1–3 | 6.4 |
| 5 | Exact fence 与 secret cleanup | 3、4 | 6.5 |
| 6 | 来源发布、v3 导出与 readback | 1、2、4 | 6.6 |
| 7 | v3 装配、历史选择与 coverage | 3–6 | 6.7 |
| 8 | 确定性执行独立回归 | 7 | 6.8 |
| 9 | 物理采证与最终门禁 | 8 | 6.9、5.1、5.2 |

2026-09-13 源码核验：Hub 只有 gRPC pending pump；无已实现 fallback handler。`localfile.Finalize` 已内容寻址并以 `os.Link` 无覆盖发布；不新增 CAS adapter。现有 orphan reconciler 只认 `evidence_object_refs`，且 claim 与 publication 未共享对象锁；新 fragment 明确保留，不进入通用孤儿删除，旧类型 GC 竞态不在本片扩修。现有物理 producer 缺完整连续状态证据，不能直接运行后宣称全部通过。当前 task checkboxes 中 1–4 是 v2 历史 groundwork，不是本计划完成度。

### 执行前命令

```bash
cd /path/to/switchback/.worktrees/observability-supplements-v1
git status --short
git log -1 --oneline
make codegraph-status
node /Users/example/.npm/_npx/20f2b75ddc8bce88/node_modules/@fission-ai/openspec/bin/openspec.js instructions apply --change durable-observability-projection --json
```

预期：只有明确属于本任务的改动；索引属于此 worktree；OpenSpec contextFiles 都从此树加载。缓存 CLI 路径不存在时先定位当前已安装 CLI，不静默下载或换版本。

## Task 1: 不可变内容、纯编译器与原 scope receipt

**Files:**

- Create: `internal/artifacttrace/domain/observation_fragment.go`、`observation_fragment_test.go`：只含领域值和规范编码，不依赖 Worker application。
- Create: `internal/artifacttrace/application/observation_capture.go`、`observation_capture_test.go`：bounded scan、receipt、Backend-only prepared result。
- Create: `internal/artifacttrace/adapter/langfuse/fragment_compiler.go`、`fragment_compiler_test.go`：固定 v3 wire/expected/digest。
- Modify/test: `internal/artifacttrace/adapter/langfuse/durable_projector_test.go`：增加 v2 golden 保持旧 ID/bytes。
- Modify/test: `internal/platform/secretcheck/scanner.go`、`scanner_test.go`（或职责单一的 sibling 文件）：修复现有 JSON 表示转换导致的已知 token 漏扫，保持公开接口不变。

2026-09-14 实现核验补充：真实 scanner 的合成反例连续两次证明，凭据自身含引号、反斜杠、控制字符或 `<>&` 时，先 marshal 再 exact-byte scan 会漏检；capture 先 marshal 原证据也会把 `[]byte` 转为 base64。修复属于本任务已批准的原始内容安全契约，不缩窄 token 字符集、不改变 receipt/authority。先扫描完整原值，再扫描序列化原 JSON、frame 和 wire；语义扫描覆盖解码后的 key/value 与嵌套 JSON 文本，保留 raw 扫描和数字原文。proof 输入限于有界的真实 envelope DTO、显式 input/report、普通数据容器、文本/bytes、RawMessage 和 time.Time；未知自定义 marshaler、循环或不支持的形状在调用用户代码前 fail closed。不得为 `any` 扩建通用对象/DLP 框架，或以 detached goroutine 假装实现硬抢占。新增真实 scanner 与 capture 回归，并重跑既有调用方、v2 golden 和 race 门禁。

**Interfaces — 本任务定义，下游不得自行改名：**

```go
// artifacttrace/domain; Identity/TurnIdentity 是同包现有或以下明确值。
type FragmentKind string
const (
    FragmentRunBasis FragmentKind = "run_basis"
    FragmentTurnBasis FragmentKind = "turn_basis"
    FragmentModel FragmentKind = "model"
)
type ObservationScope struct {
    TenantID string `json:"tenant_id"`
    UserID string `json:"user_id"`
    WorkspaceID string `json:"workspace_id"`
    WorkerSessionID string `json:"worker_session_id"`
    WorkerRunID string `json:"worker_run_id"`
}
type ObservationFragment struct {
    SchemaVersion string `json:"schema_version"`
    Kind FragmentKind `json:"kind"`
    Scope ObservationScope `json:"scope"`
    InitialTurnID string `json:"initial_turn_id,omitempty"`
    Turn *TurnIdentity `json:"turn,omitempty"`
    ObservationID string `json:"observation_id,omitempty"`
    StartedAt time.Time `json:"started_at"`
    EndedAt *time.Time `json:"ended_at,omitempty"`
    InputJSON string `json:"input_json"`
    OutputJSON string `json:"output_json,omitempty"`
    Model string `json:"model,omitempty"`
    Route string `json:"route,omitempty"`
    Status string `json:"status,omitempty"`
    InputTokens int64 `json:"input_tokens,omitempty"`
    OutputTokens int64 `json:"output_tokens,omitempty"`
}
func EncodeObservationFragment(ObservationFragment) ([]byte, error)
func DecodeObservationFragment([]byte) (ObservationFragment, error)
func ObservationFragmentID(ObservationFragment) (string, error)
// artifacttrace/application
type ObservationWireCompiler interface {
    CompileObservationFragment(context.Context, []byte) ([]byte, error)
}
type ObservationScanner interface {
    ScanJSON(context.Context, string, any) error
}
type ObservationScanScope struct {
    WorkerSessionID string
    RuntimeInstanceID string
    RuntimeGeneration uint64
}
type ObservationProvenance struct {
    EnvelopeDigest string
    CheckpointDigest string
    InputDigest string
    ReportDigest string
}
type PreparedObservation struct {
    Frame []byte
    Receipt []byte
    ErrorCode string
}
type ObservationCapture struct {
    Compiler ObservationWireCompiler
    Scanner ObservationScanner
    Scope ObservationScanScope
    Timeout time.Duration
}
func (ObservationCapture) Prepare(context.Context, []byte, any, ObservationProvenance) PreparedObservation
// langfuse adapter; 无 API/HTTP/client 依赖。
type FragmentCompiler struct{}
func (FragmentCompiler) CompileObservationFragment(context.Context, []byte) ([]byte, error)
```

`Frame` 使用类型化 struct 的稳定字段顺序；`InputJSON/OutputJSON` 保留原文，包括空白和大整数。`TurnIdentity` 沿用现有身份，但 Run frame 不能带 Turn/Runtime。receipt 定义固定 `schema_version=observation.scan.receipt.v1`、`projection_version=langfuse.projection.v3`、`compiler_revision=langfuse.fragment.compiler.v1`、`policy_revision=runtime_known_tokens.v1`，以及 scope、frame_sha256、wire_sha256、provenance。receipt 不进入 wire，也不被 frame 引用。

- [x] **1.1 写 RED 测试：身份不依赖 payload；I/O 完整保留。** 新建以下实际测试，固定时间，不依赖 sleep。

```go
func TestObservationFragmentIdentityExcludesPayload(t *testing.T) {
    frame := ObservationFragment{
        SchemaVersion: "observation.fragment.v1", Kind: FragmentRunBasis,
        Scope: ObservationScope{"tenant-1", "user-1", "workspace-1", "session-1", "run-1"},
        InitialTurnID: "turn-1", StartedAt: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC),
        InputJSON: "{ \"n\":9007199254740993 }",
    }
    original, err := ObservationFragmentID(frame)
    if err != nil { t.Fatal(err) }
    frame.InputJSON = "{\"n\":9007199254740994}"
    changed, err := ObservationFragmentID(frame)
    if err != nil { t.Fatal(err) }
    if changed != original { t.Fatal("payload must not allocate a second logical source") }
    encoded, err := EncodeObservationFragment(frame)
    if err != nil { t.Fatal(err) }
    decoded, err := DecodeObservationFragment(encoded)
    if err != nil { t.Fatal(err) }
    if decoded.InputJSON != frame.InputJSON { t.Fatal("I/O text changed") }
}
```

另加表驱动案例：零模型 basis 正常；run frame 含 Turn/结束输出拒绝；model 缺 identity/model/route/input/time 拒绝；成功 output=null 拒绝，失败 callback 的合法 JSON null 保留；非法 UTF-8、重复 JSON 字段/尾部 record、usage 超安全范围拒绝。scope/token 命中、原 envelope 命中但规范 frame 不含该字段、JSON 转义 token、scanner cancellation、wire 变更逐一测试。每个失败断言 safe code 且没有 content-bearing result。

```bash
go test ./internal/artifacttrace/domain ./internal/artifacttrace/application ./internal/artifacttrace/adapter/langfuse -run 'Test(ObservationFragment|ObservationCapture|FragmentCompiler)' -count=1
```

预期 RED：新符号尚不存在；实现后必须以行为断言继续防回归。

- [x] **1.2 实现规范 frame 与固定 ID。** 使用下列完整摘要 helper；`ObservationFragmentID` 对 Run 使用 version/kind/scope，Turn 再加 AgentTurnID、AgentSessionID、RuntimeInstanceID、RuntimeGeneration，model 再加 ObservationID。不得直接序列化整个既有 `TurnIdentity` 作为 ID seed：其中包含旧 LangfuseTraceID、TraceStreamID 和 network metadata，均不是 v3 逻辑 ID 成员。不要加内容 checksum；冲突由 Task 2 处理。encoder/decoder 执行本步列明的逐 kind 字段白名单与校验，拒绝多 record/未知字段，不使用 `map[string]any` 解析 I/O。

```go
func observationSHA256(data []byte) string {
    sum := sha256.Sum256(data)
    return "sha256:" + hex.EncodeToString(sum[:])
}
func observationIdentityDigest(parts []string) string {
    data, err := json.Marshal(parts)
    if err != nil { panic("string identity encoding failed") }
    return observationSHA256(data)
}
```

- [x] **1.3 实现纯 v3 compiler 与 capture。** 从现有 `durable_projector.go` 复用 `encodeJSONText`、`encodeMetadataForTransport`、`transportTime`、`safeUsage`，不改 v2 算法；新增 kind 分支生成恰好一个 root/span/generation `IngestionEvent`。模型 parent ID 仅由其 Turn identity 推出，不要求父 publication 已存在。event timestamp 固定为本 frame 的 StartedAt；root/turn/model metadata 只含设计指定身份、来源时间、固定 version/compiler 和 frame digest。root不带output；modelusage input/output/total只算一次。

capture 必须按此顺序：验证 scope → 校验原 envelope/input/report → 扫描原数据 → encode frame → scan frame → compile → scan wire → 写 frame/wire digest receipt → scan 完整 receipt 后返回。摘要格式合法不代表不含已知 token，receipt 的 provenance、版本、固定摘要等输出字段也须满足全局无凭据要求。scope scanner 和本次 Backend scanner 使用组合 scanner；任何扫描失败返回仅安全 code。`Timeout` 默认 250 ms，构造只允许正值且不超过 1 秒；超时为 no-content gap，不让队头等后台重试。该限制属于本地准备，不是用户 Task deadline。

```go
func observationWireEnvelope(raw string) json.RawMessage {
    value, err := json.Marshal(struct {
        Encoding string `json:"encoding"`
        JSON string `json:"json"`
    }{Encoding: "json-text.v1", JSON: raw})
    if err != nil { panic("string envelope encoding failed") }
    return value
}
```

- [x] **1.4 GREEN/race，并固定旧 golden。** 检查大数原始文本、UTC ms 与 source ns、字符串 `"1"/"true"` 的 metadata 类型，重复编译 frame/wire SHA 完全相同。不得以整条 trace observation 总数作断言。

```bash
go test ./internal/artifacttrace/... -short -count=1
go test -race ./internal/artifacttrace/domain ./internal/artifacttrace/application ./internal/artifacttrace/adapter/langfuse -count=1
git diff --check
```

- [x] **1.5 独立 review 后提交。** 范围仅本 Task 文件；commit `feat(observability): add immutable fragments and scan receipts`。附 RED/GREEN 命令与结果，确认没有生产 runner 切换。

2026-09-14 Task 1 门禁：`8176dd8` + `4bcef85`，独立 spec/quality 复审通过；原 IsZero P1 已关闭、无剩余 findings。scanner 与 capture 额外拦截 JSON 转义、原始 bytes/base64、最终 receipt 与 `omitzero` 执行回调漏项。ArtifactTrace short、相关调用方、scanner/domain/application/Langfuse race 和四组原始反例通过；v2 golden 不变。此为纯类型/编译/扫描基础，不代表 provenance 权威绑定、PG duplicate/conflict、生产装配或真实服务验收已完成。

## Task 2: Durable ledger、Run activation 与来源身份唯一性

**Files:**

- Create: `migrations/000024_observation_fragments.sql`；执行前验证 000024 未被其他本分支工作占用，若占用按 migration inventory 顺延并更新本计划。
- Create: `internal/artifacttrace/application/fragment_jobs.go`、`fragment_publication.go`：下列端口和值。
- Create: `internal/artifacttrace/adapter/postgres/observation_ledger.go`、`fragment_jobs.go`、`fragment_activation.go`、`observation_ledger_integration_test.go`、`fragment_jobs_integration_test.go`。
- Modify/test: `internal/architecture/migration_schema_test.go`；只新增 inventory，不修改历史 migration。

**Interfaces:** consumes Task 1 frame/receipt；produces 以下签名。`ProjectionCompletion` 与 `StoredObject` 分别复用现有 ArtifactTrace application/Worker application 类型。

```go
type FragmentRef struct { Kind string; SourceID string; FrameDigest string }
const ObservationFragmentObjectKind = "observation-fragment"
type FragmentJob struct {
    Source FragmentRef
    Version string
    LeaseToken int64
    Attempts int
    LeaseUntil time.Time
}
type FragmentCandidate struct {
    Source FragmentRef
    Frame []byte
    Receipt []byte
    LeaseToken int64
    LeaseUntil time.Time
}
type FragmentJobStore interface {
    Activate(context.Context, string) error
    Discover(context.Context, string, int) (int, error)
    Claim(context.Context, string, time.Duration) (FragmentJob, bool, error)
    Finish(context.Context, FragmentJob, ProjectionCompletion) error
}
type FragmentPublicationStore interface {
    ClaimCandidate(context.Context, time.Duration) (FragmentCandidate, bool, error)
    Publish(context.Context, FragmentCandidate, workerapp.StoredObject) (FragmentRef, error)
    DeferCandidate(context.Context, FragmentCandidate, string, bool) error
}
```

- [x] **2.1 写真实 PG RED。** 用 `postgrestest.OpenThroughMigration(t,23)` 保存旧 v2 pending/blocked/passed、source/manifest/checkpoint bytes；应用新增 migration 后逐字比对。`postgrestest.Open` 没有 URL 会 skip，所以先执行预检。用 `insertProjectionManifestWithExecutor` 的 pool/tx 共享 fixture 方式构造两个事务，不以睡眠猜 commit 顺序。

```bash
test -n "${SWITCHBACK_TEST_DATABASE_URL:-}"
go test -tags=integration ./internal/artifacttrace/adapter/postgres -run 'Test(FragmentActivation|FragmentLedger|FragmentJobs)' -count=1 -v
```

必须有命名案例：`TestFragmentActivationExcludesRunningRunWithoutManifest`、`TestFragmentActivationIncludesLateCommittedRun`、`TestFragmentLedgerRejectsConflictingDigest`、`TestFragmentJobsRejectExpiredCompletionWithoutNewClaim`。每个案例 assert 原行数、exact identity/digest/status，检查实际执行而非 skip。

- [x] **2.2 建 migration。** 表职责固定如下，全部成功内容列 immutable；工作状态列独立。scope 采用显式 Tenant/User/Workspace/Session/Run，唯一键不得只用 digest。

| 表 | 主键/唯一性 | 必需内容 |
| --- | --- | --- |
| observation_candidates | kind/source_id 唯一 | safe frame bytes、receipt bytes、固定摘要、scope、provenance、publisher lease/state |
| observation_publications | kind/source_id 唯一 | candidate 固定摘要、object key/size/checksum、core refs |
| observation_fragment_jobs | version/kind/source_id 唯一 | 固定 frame摘要、状态、attempt、lease token/until、retry time、safe code |
| observation_fragment_activations | version 主键 | 首次 activation time |
| observation_fragment_run_exclusions | version/worker_run_id 主键 | Tenant/User/Workspace/Session scope、精确 Run |
| observation_fragment_requests | actor/idempotency 主键 | exact request bytes/digest、version、source set |
| observation_fragment_conflicts | source_id/conflicting_digest 唯一 | 无 payload 的冲突摘要 |

bytes 用 `bytea`，provenance 用版本化 JSON，不把 raw I/O 变成 jsonb 浮点对象。CHECK：SHA256 格式、非空 identity、status枚举、nonnegative size/attempt/fence、running 与 lease_until 一致、成功候选须有 frame/receipt；blocked no-content 候选不得保存 unsafe payload。publication/job 对 candidate 精确 kind/id/digest 外键；每 job 唯一身份独立于checksum。新增 migration down 使用以下 SQL 明确拒绝 destructive downgrade，不删历史：

```sql
-- migrate:down
DO $$ BEGIN
  RAISE EXCEPTION 'observation fragment evidence is retained; application rollback must disable projection';
END $$;
```

- [x] **2.3 实现同一 statement 的 Run activation。** 下列 SQL 是精确模板，避免多 statement 的 Read Committed 快照漂移；并发 first activation 只让真正插入方写 baseline。

```sql
WITH activated AS (
  INSERT INTO observation_fragment_activations(version)
  VALUES ($1) ON CONFLICT DO NOTHING RETURNING version
)
INSERT INTO observation_fragment_run_exclusions
  (version,worker_run_id,tenant_id,user_id,workspace_id,worker_session_id)
SELECT activated.version,run.worker_run_id,session.tenant_id,session.user_id,
       session.workspace_id,session.worker_session_id
FROM activated CROSS JOIN worker_runs run
JOIN worker_sessions session ON session.worker_session_id=run.worker_session_id;
```

exclusion 的 scope 由 Run→Session 权威关联取得，不用客户端参数。后台 eligibility 为 activation 存在且 Run 不在排除集合，或 Task 7 的 exact request 显式选择了该 source，不能因选一个历史 event 就解除整个 Run 排除。

- [x] **2.4 实现 candidate/publication/job claim/finish。** 复用 v2 `SKIP LOCKED` 模式，发布和投影各自独立 lease；默认 30秒，上限现有 `MaxProjectionLease=5*time.Minute`。finish 同验 running、exact source/digest、token、`lease_until > clock_timestamp()`；成功不能因无新 claimant 就允许过期提交。重复 logical source 先比 payload digest；冲突安全落账，不采用 `ON CONFLICT DO UPDATE frame=...`。

```sql
WITH next AS (
  SELECT version,kind,source_id FROM observation_fragment_jobs
  WHERE version=$1 AND
    ((status IN ('pending','retryable') AND next_attempt_at<=clock_timestamp())
      OR (status='running' AND lease_until<=clock_timestamp()))
  ORDER BY next_attempt_at,source_id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE observation_fragment_jobs job
SET status='running',attempts=job.attempts+1,lease_token=job.lease_token+1,
    lease_until=clock_timestamp()+$2::interval
FROM next WHERE (job.version,job.kind,job.source_id)=(next.version,next.kind,next.source_id)
RETURNING job.*;
```

- [x] **2.5 GREEN、migration check、review、commit。** 包含 concurrent activation、late commit、restart、page size变化、duplicate job、same-key different-content、rollback evidence preservation。commit `feat(observability): persist fragment publication and jobs`。

2026-09-14 核验：实现 `ad2e697`、锁等待期间租约过期修复 `d11f3a4` 已独立复审通过。真实 PG focused 与 race 各 16 个顶层测试及 3 个新增子例通过，零 skip；原始公开 adapter 并发反例修复后连续两轮通过。迁移保留真实 v2 source/manifest 对象和数据库原行，拒绝 destructive down。Task 3/4 权威 receipt 接线、Task 6 对象实读/孤儿保护、Task 7 请求授权和 Task 9 物理门禁仍分别待验。

```bash
make migration-check
go test -tags=integration ./internal/artifacttrace/adapter/postgres -run 'Test(Fragment|ObservationLedger)' -count=1 -v
go test -tags=integration -race ./internal/artifacttrace/adapter/postgres -run 'Test(Fragment|ObservationLedger)' -count=1
```

## Task 3: 单 owner 接收与 Apply 原子 sidecar

**Files:**

- Create: `internal/workerruntime/application/runtime_observation.go`、`model_observation_acceptor.go`、`model_observation_acceptor_test.go`。
- Modify: `internal/workerruntime/application/runtime_control_journal.go`、`ports.go`：Backend-only sidecar 参数；不加入 RuntimeControlEnvelope 或公开协议。
- Create: `internal/workerruntime/adapter/postgres/observation_participant.go`；Modify: `runtime_control_journal.go`。
- Create: `internal/artifacttrace/adapter/postgres/observation_participant.go`：同事务验证/持久化实现。
- Modify/test: `internal/workerruntime/adapter/controlgrpc/hub.go`、`resume.go`、`resume_test.go`；新增 `model_observation_test.go`。
- Modify: `internal/localproduct/runtime_runner.go`、`runtime_settlement_recovery.go`；Create: `runtime_model_observation.go`。
- Test: `internal/workerruntime/adapter/postgres/model_observation_integration_test.go`。

**Interfaces:** consumes Task 1 `PreparedObservation` 与 Task 2 ledger。同事务参数放 Worker application 的 Backend-only 值类型，Worker application 不 import ArtifactTrace（反向依赖已存在）。

```go
// Worker application: only local trusted callers can populate this sidecar.
type RuntimeObservationCapture struct {
    Sealed []byte `json:"-"`
    SafeCode RuntimeObservationCaptureCode `json:"-"`
}
type RuntimeObservationCaptureCode string
type ModelObservationDelivery interface {
    WorkerControlEventDelivery
    ApplyModelObservation(context.Context, json.RawMessage, RuntimeObservationCapture) error
}
type ModelObservationPreparer interface {
    PrepareModelObservation(context.Context, WorkerControlEvent) RuntimeObservationCapture
}
type ModelObservationAcceptor struct { Preparer ModelObservationPreparer }
func (ModelObservationAcceptor) Accept(context.Context, WorkerControlEvent) error
// Worker PG adapter interface; only adapter layer knows pgx transaction.
type ObservationCommitParticipant interface {
    CommitModelObservation(context.Context, platformpostgres.Tx,
        application.RuntimeControlMessage, // locked actual event
        application.RuntimeControlMessage, // locked verified historical start
        application.ApplyRuntimeControlEventRecord) error
    CommitTurnBasis(context.Context, platformpostgres.Tx,
        application.TurnSettlement, // locked pre-freeze settlement
        application.FreezeTurnSettlementCheckpointRecord) error
}
func NewRuntimeControlJournalWithObservation(*pgxpool.Pool, ObservationCommitParticipant) (*RuntimeControlJournal, error)
func NewTurnSettlementRepositoryWithObservation(*pgxpool.Pool, ObservationCommitParticipant) (*TurnSettlementRepository, error)
```

保留两个旧构造器不变；新增构造器拒绝 nil participant，v3 装配只用新构造器。不能传任意 SQL callback。ArtifactTrace PG `ObservationCaptureStore` 结构化实现上面两个方法，不 import Worker PG adapter；重验锁定事件、历史 start、record outcome、ledger/source 绑定，不信任 Worker payload 中任何 receipt 字段。

ArtifactTrace application 在 `observation_capture.go` 定义本地密封包：`type SealedObservationBundle struct { SchemaVersion string; Observations []PreparedObservation }`；schema 固定 `observation.capture.bundle.v1`，model 必须恰好一个 model frame，basis 恰好一个 turn frame，initial Turn 另有一个 run frame。`EncodeSealedObservationBundle(SealedObservationBundle) ([]byte,error)` 与 `DecodeSealedObservationBundle([]byte) (SealedObservationBundle,error)` 使用严格 JSON 编解码及 Task 1 的 frame/receipt 校验。bundle 只承载已扫描 bytes 或 no-content code，不能包含 token/scanner，不能进入公开 wire。

成功时 `Sealed` 非空且 `SafeCode` 为空；整体准备失败时相反。`RuntimeObservationCaptureCode` 只允许 `observation_capture_unavailable`、`observation_capture_timeout`、`observation_capture_invalid`；更细粒度原因由 ArtifactTrace 包内的固定 safe-code 白名单表达。跨 API 复制 bytes，避免调用方后续变异。v3 缺依赖只能产生无内容 gap，不能退回旧接受路径伪造 sealed。

- [x] **3.1 写接收 RED。** 基于 `scriptedControlJournal` / `openResumedTestStream`，发 model→heartbeat 而不创建任何 Turn waiter，要求两者按序 applied；再测试旧A model→B terminal/replay。参考现有 `TestPendingTerminalBlocksFollowingHeartbeatUntilApplied`：terminal仍按原机制阻塞，只有model由新owner消费。

```go
func (acceptor ModelObservationAcceptor) Accept(ctx context.Context, event WorkerControlEvent) error {
    if event.Envelope.MessageType != "model.observation" {
        return errors.New("model_observation_type_invalid")
    }
    delivery, ok := event.Delivery.(ModelObservationDelivery)
    if !ok || acceptor.Preparer == nil {
        return errors.New("model_observation_receiver_unavailable")
    }
    prepared := acceptor.Preparer.PrepareModelObservation(ctx, event)
    outcome := json.RawMessage(`{"status":"model_observation_recorded"}`)
    return delivery.ApplyModelObservation(ctx, outcome, prepared)
}
```

此完整应用编排不负责 auth/PG；准备失败返回 no-content sidecar，永久 scan问题仍交 Apply 推 cursor。准备器以 `SealedObservationBundle` 编码 Task 1 结果，再按字节复制到 Worker sidecar。PG 最终关联核验失败时必须改用明确 ignored/rejected outcome，而不是采用调用方建议的 recorded。

```bash
go test ./internal/workerruntime/adapter/controlgrpc -run 'TestModelObservation|TestPendingTerminalBlocksFollowingHeartbeatUntilApplied' -count=1
```

- [x] **3.2 实现 pump 注入和唯一 owner。** 新增 `Hub.SetModelObservationAcceptor(application.ModelObservationAcceptor) error`，仿 `SetEventObserver` 的连接前注入约束。在 `pendingDelivery` 完成后、`delivery.publish` 前按 message type 分流；成功continue，不再发到live.events。失败以现有 delivery retry/context取消机制收敛，不另开reader。移除两个waiter中 `model_observation_recorded` 的空业务接受分支。startup `waitWorkerEvent` 不再收到已消费model。

- [x] **3.3 扩展专用 Apply。** `ApplyRuntimeControlEventCommand` 新增 `ObservationCapture *RuntimeObservationCapture`，带 `json:"-"`（非wire字段）。`journalEventDelivery.ApplyModelObservation` 调既有持mutex/rebind authority的apply路径并附sidecar；不能在外面先写candidate再普通Apply。模型的合法Outcome固定 `{ "status": "model_observation_recorded" }`，安全blocked/duplicate/conflict在sidecar ledger表示；非法关联使用明确ignored/rejected outcome而不是伪造recorded。普通 Apply 不得对 v3 适用模型在没有 capture/participant 时产生新的 recorded；nil sidecar 只允许服务端 durable activation 判定的 disabled/legacy/non-applicable 路径，不由调用方选择绕过。

事务锁顺序沿用 Session/Runtime→channel→message→historical start→sidecar。复用 `readRuntimeControlStartCommandForTurn`，要求唯一且active/terminal历史start，envelope与payload全部身份一致；不检查observed Turn等于session当前active Turn。不得沿用backend-deadline relaxed authority替旧generation授权。duplicate 分支只验证原 outcome 与既有 receipt，不补插缺失证明，原 ignored 保持 ignored。可选扫描失败、same-source payload 冲突安全落账后返回 nil，让合法 cursor 前进；数据库/持久完整性错误才回滚 message、receipt 与 cursor，不把永久观测失败变成队头无限重试。

```sql
SELECT message_id,envelope,effect_status
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='command'
  AND envelope->>'message_type'='agent_turn.start'
  AND envelope->>'agent_turn_id'=$2
FOR UPDATE;
```

- [x] **3.4 GREEN/real PG/race/review/commit。** 新测试 `TestModelObservationApplyReceiptRollback`、`TestModelObservationHistoricalStartRequired`、`TestModelObservationIgnoredDuplicateStaysIgnored`、`TestModelObservationIdleDoesNotBlockHeartbeat`、`TestModelObservationReconnectSingleOwner`，复用 `runtimeControlActiveTurn`、`runtimeControlBind/Claim`、`authorityFrom`，设置 barriers 不sleep。commit `feat(worker): atomically accept late model observations`。

```bash
go test ./internal/workerruntime/application ./internal/workerruntime/adapter/controlgrpc ./internal/localproduct -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestModelObservation|TestRuntimeControlJournal' -count=1 -v
go test -race ./internal/workerruntime/adapter/controlgrpc ./internal/localproduct -count=1
```

## Task 4: Run/Turn basis 与首次 checkpoint freeze 同事务

**Files:**

- Create: `internal/localproduct/runtime_observation_basis.go`、`runtime_observation_basis_test.go`。
- Modify: `internal/localproduct/runtime_settlement.go`、`internal/workerruntime/application/turn_settlement.go`、`internal/workerruntime/adapter/postgres/turn_settlement.go`。
- Modify: `internal/artifacttrace/adapter/postgres/observation_participant.go` 中 `ObservationCaptureStore.CommitTurnBasis`；Create test: `internal/workerruntime/adapter/postgres/observation_basis_integration_test.go`。

**Interfaces:** consumes Task 1 capture、Task 3 `SealedObservationBundle`、Worker sidecar/participant。`FreezeTurnSettlementCheckpointCommand` 新增 `ObservationCapture *RuntimeObservationCapture`，带 `json:"-"`；`runtimeTurnSettlementCheckpoint` 的 JSON 结构不变。使用 Task 3 的 `NewTurnSettlementRepositoryWithObservation`；现有构造器不改签名。LocalProduct 定义 `func (runner *runtimeRunner) prepareRuntimeObservationBasis(ctx context.Context, turn pendingTurn, endedAt time.Time, frozenReport []byte, checkpointDigest string) (workerapp.RuntimeObservationCapture, error)`；使用 durable input/time、已冻结 report 与原 registry，不从当前活动 Turn 取值。非 initial Turn 只准备 Turn frame，initial 另准备 Run frame；数据库错误是 error，可选 capture 失败在返回值中以 no-content code 表达。

- [x] **4.1 写 RED：零模型、首marker与首次freeze。** 使用 `recordingRuntimeSettlementController`/`Effects` 验证prepare→first freeze(sidecar)→ObjectStore→EvidencePrepared；在 first freeze commit 后模拟响应丢失与registry清理，重试只读既有bytes。PG fixture复用 `prepareTerminalSettlementApplyFixture`。精确断言phase_state digest保持原值且旁表与首次freeze同事务。

```bash
go test ./internal/localproduct -run 'TestRuntimeObservationBasis|TestRuntimeTurnSettlementCoordinator' -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestObservationBasis|TestTurnSettlementCheckpointFreeze' -count=1 -v
```

- [x] **4.2 接线冻结 sidecar。** 提取现有 `captureRuntimeObservationSource` 已有durable input加载和纯 `BuildTurnEvidence` report选择逻辑，旧source函数仍按原cut与marker规则工作。Run初始输入只用唯一 `created_at=run.created_at AND mode IN ('prompt','queue')` 的关联，非唯一阻断。以下是首次事务写入位置，不把它移到 `EvidencePrepared`：

```go
// Inside the existing FreezeTurnSettlementCheckpoint transaction, after
// exact lease/checkpoint validation and before updating phase_state.
if repository.observations != nil {
    if err := repository.observations.CommitTurnBasis(ctx, tx, current, record); err != nil {
        return err
    }
}
```

`current` 保留锁定的旧行，`record` 提供本次已验证的 canonical checkpoint/digest/capture；participant 不猜测也不改写 `current`。首次写入与 exact replay 分开：duplicate 返回前校验原 sidecar 存在且绑定相同；旧 checkpoint 无 sidecar 只报告 legacy gap，不在普通 replay 补签。scanner 缺失返回 no-content `basis_scan_unavailable`，PG 故障返回核心错误；reportSHA 来自真正冻结 report，不调用模型。v3 适用的首次 freeze 缺 capture 必须记录 gap，不能由 nil 参数绕过证明要求。

- [x] **4.3 GREEN/race/review/commit。** 回归 `TestRuntimeTurnSettlementCoordinatorResumesFromEveryDurablePhase`、`TestTurnSettlementDomainCommitRollsBackResidentStageWhenPhaseWriteFails`；新增input/report分别含secret、scope错配、首Turn歧义、forced-stop原registry已失、freeze写receipt失败rollback。commit `feat(observability): seal basis with initial evidence checkpoint`。

```bash
go test -race ./internal/localproduct -run 'TestRuntimeObservationBasis|TestRuntimeTurnSettlementCoordinator' -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestObservationBasis|TestTurnSettlement' -count=1 -v
```

## Task 5: Exact durable fence 与 registry 清理

**Files:**

- Create: `internal/localproduct/runtime_observation_cleanup.go`、`runtime_observation_cleanup_test.go`。
- Modify/test: `internal/localproduct/runtime_environment.go`、`runtime_runner.go`、`runtime_environment_test.go`；`internal/workerruntime/application/runtime_cleanup.go`、`internal/workerruntime/adapter/postgres/runtime_cleanup.go`及各自cleanup测试。
- Modify/test: `internal/workerruntime/adapter/controlgrpc/hub.go`、`internal/workerruntime/adapter/runtimesecret/observation_registry_test.go`。

**Interfaces:** uses existing `RuntimeControlJournalService.Fence(ctx,FenceRuntimeControlChannelCommand) (RuntimeControlChannelState,error)`、`Materializer.ObservationScanner`；新增 LocalProduct `cleanupFencedObservationRegistry(ctx, identity controlgrpc.RuntimeIdentity, state workerapp.RuntimeControlChannelState) error`，校验exactidentity及persisted fence再派生路径，不接受任意宿主path。

- [x] **5.1 写 RED：unconfirmed不能删、terminal不泄漏。** 复用 `newDeadlineCleanupTestEnvironment`、`createDeadlineCleanupSecretRoot`，故意让PG fence失败：registry仍可验证，cleanup结果不可宣称confirmed；成功fence后即使publication/HTTP永不返回，registry仍删除。把 `Hub.Revoke` 无返回与 `RuntimeCleanupService` 当前confirmed不足的失败用例固定下来。

```bash
go test ./internal/localproduct -run 'TestObservationRegistryCleanup|TestRuntimeEnvironment' -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestRuntimeCleanup' -count=1 -v
```

- [x] **5.2 落实exact fence结果。** `RuntimeCleanupService.reconcile`与PG完成条件同时要求已证实FenceStatus和原有residue/container证据；统一ordinary close/expiry、failed prepare、discard、recovery/deadline对registry的删除入口。Backend重启使用 `OpenExisting`和持久identity，不因Hub无内存credential返回ErrNotConnected就丢失清理能力。非终态resident Session不清理。

```go
// FinishRuntimeCleanup: preserve every existing residue/error check.
confirmed := record.FenceStatus == "confirmed" &&
    record.Observation.Confirmed && record.ErrorCode == "" &&
    !record.Observation.ContainerExists && !record.Observation.SecretVolumeExists
```

`FenceStatus` 是现有 `FinishRuntimeCleanupRecord` 的顶层字段，使用已有 `confirmed/unconfirmed` 值。应用 `reconcile` 在 fence 失败时设置安全错误并保留重试诊断；state proof 校验失败返回固定 `observation_cleanup_fence_unproven`，不删 registry、不返回虚假的 cleaned。禁止用本地手工构造的 state 充当持久 fence，必须校验 journal 返回的 exact identity/fence 状态。

- [x] **5.3 验证竞态与missing-proof语义。** scanner取得前/后删除两种barrier；scan后Apply前fence；已receipt提交后重启；ordinary terminal与active resident区别；旧generation保留receipt但无basis proof时明确blocked。原token仅本次有界调用持有，不能进retry队列。

```bash
go test -race ./internal/localproduct ./internal/workerruntime/adapter/runtimesecret ./internal/workerruntime/adapter/controlgrpc -count=1
go test -tags=integration ./internal/workerruntime/adapter/postgres -run 'TestRuntimeCleanup|TestModelObservation' -count=1 -v
git diff --check
```

- [x] **5.4 Review/commit** `fix(worker): require proven fence before registry cleanup`。报告删除范围仅任务运行时测试fixtures/业务exact Runtime，不运行repo、volume或宿主广域删除。

## Task 6: 后台来源发布与 v3 exact readback

**Files:**

- Create: `internal/artifacttrace/application/fragment_publisher.go`、`fragment_loader.go`、`fragment_worker.go`及对应 `_test.go`。
- Create: `internal/artifacttrace/adapter/langfuse/fragment_projector.go`、`fragment_projector_test.go`。
- Modify: `internal/artifacttrace/adapter/postgres/observation_ledger.go`、`publisher.go`；新增 `fragment_publication_integration_test.go`。
- Modify test: `internal/artifacttrace/adapter/postgres/publisher_integration_test.go`，增加新来源引用保护回归。
- Modify/test: `internal/artifacttrace/adapter/localfile/object_store.go`、`internal/artifacttrace/adapter/localfile/object_store_test.go`，仅为新 kind 添加保留边界，不改旧 kind 的 GC 协议。

**Interfaces:**

```go
type LoadedFragmentProjection struct {
    Source FragmentRef
    Version string
    Frames [][]byte
    Receipts [][]byte
}
type FragmentProjectionLoader interface {
    Load(context.Context, FragmentJob) (LoadedFragmentProjection, error)
}
type FragmentProjector interface {
    ExportAndVerify(context.Context, LoadedFragmentProjection) error
}
type FragmentPublisher struct {
    Store FragmentPublicationStore
    Objects workerapp.ObjectStore
}
func (*FragmentPublisher) Tick(context.Context) error
func (*FragmentPublisher) Run(context.Context)
type FragmentWorker struct {
    Jobs FragmentJobStore
    Loader FragmentProjectionLoader
    Projector FragmentProjector
    Report func(string)
}
func (*FragmentWorker) Tick(context.Context) error
func (*FragmentWorker) Run(context.Context)
```

Loader按job/source/frame/receipt与核心provenance加载精确父basis；不再调用旧 `ReadTurnObservationSource` 作为父资格。Run frame对象路径使用initialTurn作用域，model/Turn使用自身作用域；ObjectIdentity.Kind固定`observation-fragment`，底层内容寻址仍唯一。

- [x] **6.1 写 RED：发布崩溃、父等待、对象引用。** fixture 分别在 Finalize 完成后、publication commit 后、job Discover 前中断；重启时 source/job 均单个。新 kind 的 temporary/finalized 对象均不出现在 `ListCandidates`；伪造或过期库存直接调用 `DeleteCandidate` 也必须拒绝，不能只依赖 listing 过滤。model 先 sealed、basis 未 published 为 retryable；basis 已 blocked 为 `parent_basis_unavailable`。2026-09-19 证据限制：原实施 RED 输出未保存，不能补证；现有回归已新鲜验证，I1 完整性修复另有真实 RED/GREEN，详见 Task6 report/rereview。

```bash
go test ./internal/artifacttrace/application ./internal/artifacttrace/adapter/langfuse -run 'TestFragment(Publisher|Loader|Worker|Projector)' -count=1
go test ./internal/artifacttrace/adapter/localfile -run 'TestObservationFragmentRetention|TestObjectStore' -count=1
go test -tags=integration ./internal/artifacttrace/adapter/postgres -run 'TestFragmentPublication|TestFragmentOrphan' -count=1 -v
```

- [x] **6.2 实现对象写入与引用提交。** 使用已有 `PutTemporary→Finalize→Verify`，打开文件和 HTTP 均在 transaction 外。`Publish` 同事务验证 candidate lease、固定 digest、core manifest/report 已提交，然后登记 publication 与 `evidence_object_refs`，校验 existing ref 的 exact scope/kind/checksum/size 并拒绝冲突；不把新片段塞进旧 `publicationObjectRefs` 的固定 5/6 对象计数，不修改旧 manifest JSON。后台独立发现 job，不依赖一次性 enqueue。

新 kind 是首次引入的专用目录，不与旧对象 key 重合。在 `ListCandidates` 完成对应 key validator 后过滤，在 `DeleteCandidate` 完成 validator 后、触碰文件前拒绝以下已验证路径。新旧 worker 不并行运行；切换/回滚先停旧进程及 orphan 执行，不能让旧 binary 继续删除新类型。失败发布的残留暂保留，这是显式成本；未来回收需另行评审 reservation/incarnation，不在本片增加通用对象锁或 GC 状态机。

```go
func isRetainedObservationKey(segments []string) bool {
    // Call only after validateObjectKey or validateTemporaryKey succeeds.
    return len(segments) >= 14 && segments[13] == artifactapp.ObservationFragmentObjectKind
}
```

删除分支返回固定 `observation_fragment_retained`，不能报告 Deleted/AlreadyAbsent。已发布对象引用用于追溯与长期保留；安全性不建立在现有 claim 普通 SELECT 上。给 `Finalize→Publish` 加 barrier，同时执行 orphan inventory/reconcile，断言新对象一直可读、publication 精确一次。

```go
temporary, err := publisher.Objects.PutTemporary(ctx, identity, bytes.NewReader(candidate.Frame))
if err != nil { return err }
object, err := publisher.Objects.Finalize(ctx, identity, temporary)
if err != nil { return err }
if err := publisher.Objects.Verify(ctx, object.Key, object.Checksum); err != nil { return err }
_, err = publisher.Store.Publish(ctx, candidate, object)
return err
```

`identity`在本步由已验证frame scope生成，绝不来自CLI/user path；最终objectchecksum与候选frame摘要再验。扫描证明不用在此重建；不存在proof时不能靠当前scanner直接通过。

- [x] **6.3 实现纯编译证明与HTTP。** 逐frame验证receipt原scope和frame/wire SHA；按Task1compiler生成对应wire，既有parent可重放相同片段。将验证过的 `IngestionEvent` 组合为 `IngestionBatch`，扫描current Backend/Langfuse tokens，调用既有 `DurableAPI`。trace root和每个SPAN/GENERATION逐字段readback；大整数保留原始JSON字符串，meta精度不退化，callback失败的合法JSON null按固定schema如实表达，不造成功output。

- [x] **6.4 有界workers。** 复用v2参数而不是v2类：discover50、claim1、30s lease、15s job timeout、1s poll、5s指数退避cap5min；parentpending分类retryable，checksum/proof/conflict明确blocked。取消直接结束，不shutdown flush，不向App fatal channel传播可选观测失败。ErrorCode whitelist新增安全code，不存外部error文本。

- [x] **6.5 GREEN/race/realPG/review/commit** `feat(observability): publish and verify immutable v3 fragments`。

```bash
go test -short ./internal/artifacttrace/... -count=1
go test -race ./internal/artifacttrace/application ./internal/artifacttrace/adapter/langfuse -count=1
go test -tags=integration ./internal/artifacttrace/adapter/postgres -run 'TestFragment|TestOrphan' -count=1 -v
```

## Task 7: v3-only装配、exact历史选择与coverage

**Files:**

- Modify: `internal/localproduct/observability_runner.go`、`app.go`、`runtime_runner.go`；Create test: `internal/localproduct/observability_runner_test.go`。
- Create: `internal/artifacttrace/application/fragment_history.go`、`fragment_coverage.go`及对应test；`internal/artifacttrace/adapter/postgres/fragment_history.go`、`fragment_coverage.go`及integration test。
- Modify: `docs/operations/durable-observability-projection.md`；新README式运维说明并入此文件，不新增控制台/API。

**Interfaces:**

```go
type HistoricalModelRef struct {
    MessageID string
    Sequence uint64
    EnvelopeDigest string
}
type HistoricalTurnRef struct {
    AgentTurnID string
    ManifestChecksum string
}
type RequestHistoricalFragments struct {
    ActorID string
    Scope artifactdomain.ObservationScope
    Version string
    Initial HistoricalTurnRef
    Target HistoricalTurnRef
    Models []HistoricalModelRef
    IdempotencyKey string
}
type HistoricalFragmentSelection struct { RequestID string; RequestDigest string }
type FragmentHistory interface {
    Request(context.Context, RequestHistoricalFragments) (HistoricalFragmentSelection, error)
}
type FragmentCoverage struct {
    Version string
    Sources []FragmentRef
    Accepted int
    Sealed int
    Published int
    Verified int
    GapCodes []string
}
```

历史Request只是exact授权选择；若原registry仍在可由同一可信capture模块补录证明，已清理且无proof拒绝。Request不能直接提供`scan_passed`、自由SQL、任意sourcebytes，不能修改原ignored outcome。历史选择只解锁显式initial/target basis与model集合，不解除整个Run exclusion。

- [x] **7.1 写 RED。** 完整LF配置只构造v3worker；三项空或部分配置非fatal；v2jobs仍原状态且完全无Claim/HTTP调用。相同历史request返回原digest；同key换模型集合/actor/scope/version拒绝；旧Run新事件未被选择仍排除。coverage有两代数据时只计算requestedversion，不暴露未选scope，不创建all_models_complete字段。

```bash
go test ./internal/localproduct -run 'TestObservability|TestFragmentCutover' -count=1
go test -tags=integration ./internal/artifacttrace/adapter/postgres -run 'TestFragmentHistory|TestFragmentCoverage' -count=1 -v
```

- [x] **7.2 装配新owner。** 在`newObservabilityRunner`一次选择v3，复用App现有context取消/等待退出。new runtime runner构造时注入唯一modelacceptor以及PGparticipant，不通过live runtime map传receipt。v3enabled而capture或participant依赖缺失只能safe gap，不可偷偷fallback v2。v2类/常量保持，以支持旧golden只读校验。

```go
type fragmentBackgroundRunners struct {
    Publisher *artifactapp.FragmentPublisher
    Projector *artifactapp.FragmentWorker
}
func (r fragmentBackgroundRunners) Run(ctx context.Context) {
    var workers sync.WaitGroup
    workers.Add(2)
    go func() { defer workers.Done(); r.Publisher.Run(ctx) }()
    go func() { defer workers.Done(); r.Projector.Run(ctx) }()
    workers.Wait()
}
```

激活只在完整观测配置下发生，故无配置时积累的旧Run不自动导出；LF服务不可用但完整配置时可以激活并保持后台重试，Backend仍ready。对象publisher不需要LF健康。

- [x] **7.3 实现history/coverage与文档。** request按真实M1actor、exactscope、core refs、accepted outcome、proof条件验证；使用顺序稳定的model ref集合生成requestdigest，保留idempotency。coverage在一个一致数据库读快照中获取selected来源及状态，不边分页边加总不同时间点；包括no-content gaps，不能只统计有publication的成员。文档明确“已接受N/验证M”不代表callback闭包、v2历史双计数风险及rollback先disable。

- [x] **7.4 GREEN/review/commit** `feat(observability): activate v3 with exact coverage and history selection`。不实际选择或导出用户历史；不启动服务替用户迁移。

```bash
go test ./internal/localproduct ./internal/artifacttrace/application -count=1
go test -tags=integration ./internal/artifacttrace/adapter/postgres -run 'TestFragment(History|Coverage|Activation)' -count=1 -v
git diff --check
```

## Task 8: 无sleep的执行独立与旧bytes不变性集成

**Files:**

- Create: `internal/localproduct/observation_supplements_integration_test.go`（场景与不可变性）、`observation_supplements_fixture_integration_test.go`（共享真实依赖与装饰器）、`observation_supplements_recovery_integration_test.go`（故障矩阵），均位于 `internal/localproduct/`；复用 `observability_settlement_integration_test.go`、`runtime_settlement_test.go` 的 realPG/control fixture。2026-09-19 父代理批准仅测试布局拆分，避免将场景、fixture 与六窗矩阵堆入单文件；不改变生产范围或验收要求。
- Modify/test: `internal/localproduct/observability_settlement_integration_test.go` 的测试 runtime 可增加可选 callback scheduler；nil 保持旧直接发送行为，新场景保留原 Start/model identity。须覆盖原测试，不改变生产 Shim 或把 fixture 收到 Start 当成服务端 ACK。
- Create/test: `internal/artifacttrace/adapter/postgres/fragment_marker_integration_test.go`；最小复用既有 `fragmentFixture` 与明确标注的 historical later-Turn seed，补设计验收3的同 Run 旧 marker / 后续完整 source 与真实新 root 父来源验证。不得把 localproduct 的跨 standalone Run 连续性当成同 Run 证明，也不得重置 existing Run/Session 或修复 Task-bound queue 来制造该场景。2026-09-19 父代理核验既有 Task6/7 same-Run 测试没有设置旧 `ObservationSourceJSONL`，据此批准此专用补测。
- Modify/test: `internal/workerruntime/adapter/shimruntime/runtime_test.go`、`internal/workerruntime/adapter/modelobs/server_test.go`：只测试生产callback绑定/terminal后的事实接收，不加fake sleep。

**Interfaces:** consumes Tasks1–7真实应用seams；使用现有 `RequestBinder` 与可控 observation channel，不改生产Shim时序来迎合测试。新增test helper `supplementBarrier` 是以下完整同步实现。

```go
type supplementBarrier struct { reached chan struct{}; release chan struct{} }
func newSupplementBarrier() supplementBarrier {
    return supplementBarrier{make(chan struct{}), make(chan struct{})}
}
func (b supplementBarrier) block(ctx context.Context) error {
    close(b.reached)
    select {
    case <-b.release: return nil
    case <-ctx.Done(): return ctx.Err()
    }
}
```

- [x] **8.1 写端到端RED。** 在callback发送前block，等真实terminal commit与下一Turn可派发证据，检查零未来观测等待，再release；记录旧source/checkpoint/manifest/v2job bytes前后完全相同。不是只比较Task最终status。必须分别有idle、下一Turnactive、settlement replay、同Turn早A晚B、旧source passed、首Turnmarker后续Turn正常等命名subtests。2026-09-19 证据边界：本项为 Tasks1–7 之后的回归与已执行故障敏感性检验，未建立历史生产 behavioral RED；编译/fixture 失败不算生产 RED，详见 Task8 report/rereview。

```bash
test -n "${SWITCHBACK_TEST_DATABASE_URL:-}"
go test -tags=integration ./internal/localproduct -run 'TestImmutableModelSupplement' -count=1 -v
```

- [x] **8.2 增加失败窗口矩阵。** 应用已有fixture hook分别卡scan→Apply、Applycommit→response、firstfreeze→finalize、Finalize→publication、publication→job、HTTP→Finish；重启pool/worker，删除exact已fenced测试registry，保证safe已sealed来源可恢复。旧ignored/stale不因replay升级，永久scan failure不挡heartbeat，PG故障仍rollback。

- [x] **8.3 用非外部HTTP的阻塞projector证明执行独立。** 阻塞一个已连接但不返回的API fixture，要求core terminal和下一Start ACK在释放前发生；拒绝通过新Task/新Runtime generation或手工SQL状态reset伪造通过。Task-bound continuation现有独立问题不得混入：沿用原fixture明确accept后standalone的真实流程并在报告标注。

```bash
go test -tags=integration -race ./internal/localproduct -run 'TestImmutableModelSupplement' -count=3 -v
go test ./internal/workerruntime/adapter/shimruntime ./internal/workerruntime/adapter/modelobs -count=1
```

- [x] **8.4 Review/commit** `test(observability): prove late supplement execution independence`。这是native PG/fixture层证据，不是Docker、真实Coding Agent或provider验收。

## Task 9: 真实物理采证、smoke与完成门禁

**Files:**

- Modify: `test/acceptance/observability/driver.go`、`scenario.go`、`producer.go`、`report.go`、`fault.go`、`capture.go`、`environment.go`及对应tests。
- Create: `test/acceptance/observability/service_intervals.go`、`service_intervals_test.go`、`supplements.go`、`supplements_test.go`。
- Modify: `test/acceptance/observability/compose.override.yaml`、`test/acceptance/observability/Dockerfile.worker`；受控 fixture 为 `spikes/worker-runtime/test/fakes/cmd/fake-acp/main.go`、`spikes/worker-runtime/test/fakes/cmd/fake-openai/main.go`，测试 `spikes/worker-runtime/test/fakes/cmd/fake-acp/main_test.go`。Dockerfile 已构建这两个 fixture，不在生产 callback 加延迟。
- Modify: `docs/operations/durable-observability-projection.md`、本计划、active change `tasks.md`。

**Interfaces:** 既有driver `-mode claim|capture|smoke|snapshot|fault-proxy|produce-report|validate` 继续有效；新增 `collect-intervals` 和 `run-matrix` 两个显式模式，复用claim的project/output ownership验证。v3验证输入为受控fixture已知模型集合与exact source refs，不再要求恰好2个v2来源/job。`ServiceInterval{Project,Service,ContainerID,From,To,State,EvidenceDigest}` 只由真实事件/探针生成，recipe标签不能构造。

- [x] **9.1 RED：采证缺失严格失败。** 保留现有`not_collected`案例，新增active interval不覆盖完整测量窗口、容器换代但ID未更新、event采集断流、只能证服务存在而非healthy、fault-proxy未证明请求已连接/未回答、report聚合混版本、residue不属于claim等输入，`Report.Validate`必须失败。成功fixture必须给各区间与exactknownsource完整证据。

```bash
go test ./test/acceptance/observability -count=1
```

- [x] **9.2 实现连续服务/fault/restart/residue采证。** `collect-intervals`在measurement前启动Docker events订阅，保存initial exact inspect和独立health probes；有时间间隙一律unknown，不从recipe推断。故障代理记录已连接请求的header-safe事件ID、ingestion/query分类与取消/恢复时间，不保存body/credential；Backend重启记录前后container ID及durable作业identity；清理只用claim所属容器/网络，保留必要数据证据。

```go
type ServiceInterval struct {
    Project string `json:"project"`
    Service string `json:"service"`
    ContainerID string `json:"container_id"`
    From time.Time `json:"from"`
    To time.Time `json:"to"`
    State string `json:"state"`
    EvidenceDigest string `json:"evidence_digest"`
}
func intervalCovers(interval ServiceInterval, from, to time.Time) bool {
    return !interval.From.After(from) && !interval.To.Before(to) && !to.Before(from)
}
```

实际validator还要校验exactproject/service/container、状态证据类型和区间连续性；以上helper只判断时间包含，不能单独充当health证明。`run-matrix`只调已有受控业务流程，不执行任意provider请求或业务SQL修复。

- [x] **9.3 改受控late barrier与v3检查。** 保持生产Shim/LiteLLM callback/Backend，只在已存在的test-only ACP/model fixture增加明确release信号；证明terminal及nextTurn/idle之后释放callback。`scenario.go`记录accepted→sealed→published→verified exactrefs与v2旧bytes摘要；producer移除“2个v2jobs=完整”的假设，使用测试预先知道的expected ObservationIDs。root/turn/model层缺内容必须失败。

2026-09-19：以上仅为 instrumentation 原始范围 `7d7c300..3fb9074` 独立 Spec/Quality 审批及原生回归通过；真实 Docker/LiteLLM/晚到 callback 的物理证明仍属于下方9.4/9.5，不能由这些勾选推断完成。

- [x] **9.4 真实环境预检与首轮smoke。** 先读Docker context、现存projects/ports和claim目录，无权限则报告不冒称执行。生成全新唯一project、exact output和数据卷，生产base Worker镜像必须digest pin；不复用旧临时资源或真实provider key。以下两个环境变量必须由claim预检填为已核验的新exact值，不得照搬旧smoke项目：

```bash
test -n "${OBSV_PROJECT:?use a freshly claimed isolated project}"
test -n "${OBSV_OUTPUT:?use its exact private evidence directory}"
test -n "${OBSV_BACKEND:?use its verified loopback Backend URL}"
go run ./test/acceptance/observability -mode smoke -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT" -backend "$OBSV_BACKEND"
```

先Langfuse全部五项服务不可用时cold start Backend + 同Session/Runtime两Turn；确保Task终结后才释放late callback，恢复LF后新增source自然通过。旧marker/job保持原状态；失败立即保留证据并停止批量20-pair，不能调大等待、改cut或手动reset。

- [x] **9.5 smoke通过后完整矩阵。** 执行新`run-matrix`：20 healthy/outage pairs、ingestion connected-unanswered、query connected-unanswered、Backend restart、原registry清理后补投、owned residue。LF数据库服务恢复≠准确readback通过，必须跑source verifier。持续采证coverage须包含每次测量整个窗口。始终区别controlled ACP/model与真实Coding Agent/provider、以及本片与完整日志指标G4。

```bash
go run ./test/acceptance/observability -mode run-matrix -project "$OBSV_PROJECT" -output "$OBSV_OUTPUT" -backend "$OBSV_BACKEND"
go run ./test/acceptance/observability -mode produce-report -output "$OBSV_OUTPUT" -report "$OBSV_OUTPUT/report.json"
go run ./test/acceptance/observability -mode validate -report "$OBSV_OUTPUT/report.json"
```

2026-09-26：全新 G claim `switchback-obsv-suppv3g0926` 从 `6bc6980` 构建并固定三张实际镜像 digest，逐 blob 验证生产 Worker base 的 OCI layout；专属 PG 迁移000026。Langfuse 五服务缺席时，外部 Docker events/health collector 在 Backend 前 ready，双 Turn cold smoke 和 collector join 通过；恢复五服务后六个 v3 source 自然重试并精确 readback 通过。唯一 G `run-matrix`、`produce-report`、严格 `validate` 均 exit0：20 对、两类 connected→cancelled→recovered 且无响应故障、Backend restart、六个恢复来源、原 registry/Worker/secret 清理，八个 coverage label 全 passed。20 对样本最大 transition832.775ms，三个 outage p95 增量分别 -155.191/-110.941/-249.250ms，未调整原5s/1s阈值。E 的失败矩阵和 F 的操作预检失败仍独立保留，未拼接进 G 报告；G 五数据卷与合成凭据/私有证据仍保留并明确非零残留。这仅是 controlled ACP/model + 真实 Docker/Langfuse 子集，不是完整 G4/G5/真实 Codex/provider。精确证据见 `.superpowers/sdd/supplements-task-9-physical-run.md`。

- [x] **9.6 最终非物理门禁。** 预检PG真实URL，保存JSON测试结果并检查没有skip。当前源码若出现与本片无关的固定时钟Assistant测试失败，报告原始失败并检查主工作区既有改动，不复制/回滚用户修改；整仓short suite失败不得宣称全绿。

```bash
test -n "${SWITCHBACK_TEST_DATABASE_URL:-}"
go test -short ./... -count=1
go test -race ./internal/artifacttrace/... ./internal/workerruntime/adapter/controlgrpc ./internal/workerruntime/adapter/runtimesecret ./internal/localproduct -count=1
go test -tags=integration ./internal/artifacttrace/adapter/postgres ./internal/workerruntime/adapter/postgres ./internal/localproduct -count=1 -v
make migration-check
make generated-protocol-check
node /Users/example/.npm/_npx/20f2b75ddc8bce88/node_modules/@fission-ai/openspec/bin/openspec.js validate durable-observability-projection --strict
git diff --check
```

2026-09-26：在实现/物理验收源码 HEAD `6bc6980` 上，race575 pass/0 skip、真实 PG594 pass/0 skip、migration、strict OpenSpec、diff、隔离 GOCACHE 的 generated protocol 均通过；整仓 short 两轮均1519 pass/1 个既有且本分支未修改的 Assistant `policy_revision` fail/0测试 skip，因此9.6与OpenSpec5.1仍未勾选，不宣称全绿。最终双轴 review：Standards Approved；Spec 实现及 G 物理范围合格，但整体完成审批因5.1失败保留，见 `.superpowers/sdd/supplements-final-review-6bc6980.md`。a2测试 PG 容器及其匿名卷已精确清理，证据与 review 已提交于 `7eb2f23`；9.7 因所需全量门禁未通过仍保持未完成。

- [x] **9.7 最终独立review、证据提交与handoff。** 标准/spec review覆盖相对本计划base的整个实现；逐条证伪review假设，只修真实问题。所需门禁全通过才勾选5.1/5.2；环境缺失保留未完成与exact blocker。commit `test(observability): verify v3 physical outage and recovery gates`，不push/merge/archive。记录测试资源删除的exact列表及保留的证据/数据卷；禁止全局prune或改其他任务Docker资源。

2026-09-28 闭合：用户要求继续下一步后，在隔离 worktree 独立复现 Assistant 固定日期导致真实 HTTP deadline 过期，先通过 model.err 断言确认 timeout，再于 `eb5880e` 仅修测试时钟及断言。fresh short1520/race643/真实PG594均0失败0测试skip，迁移/协议/strict OpenSpec通过；G原报告重新严格校验通过且生产/driver/部署未变化。原全分支review加本轮两轴增量review均合格，Important完成门禁已解除。仅本轮临时PG精确清理；原G数据卷/镜像/私有证据保留。测试结果、双轴review、资源清理及交接证据随本次提交落盘于 `.superpowers/sdd/supplements-closeout-2026-09-28.md`，关闭9.6/9.7与OpenSpec5.1/5.2；保留此前失败记录。此完成仅覆盖获批的durable Langfuse补充来源片，不扩大到完整G4/G5或YHC。

## 🔍 计划自检与交接

| 已确认设计要求 | 对应任务 |
| --- | --- |
| 三种fragment、无模型basis、稳定ID、lossless I/O | 1、4、6 |
| accepted/receipt原子、历史start、idle/next/replay | 2、3、8 |
| 首次checkpoint、无旧JSON改写 | 4、8 |
| 原scope证明、清理/fence、forced-stop缺proof | 1、3–5、8 |
| publication/lease/崩溃、孤儿引用保护 | 2、6、8 |
| Run activation、v2只读、exact history、coverage/rollback | 2、7、9 |
| 真实PG、固定LF、20pairs/故障/清理证据 | 8、9 |

- [x] 设计逐节映射到任务，未把fallback/真实provider/全平台看板扩入本片。
- [x] 新接口在所属任务给出，Worker application 不引入 ArtifactTrace循环依赖；无自由SQL/外部receipt授权入口。
- [x] 正式实现前的路径、占位、类型与命令自检完成；真实运行结果仅在执行任务后填写。
- [x] 用户于 2026-09-14 选择推荐的 subagent-driven-development：逐任务实现，规格与质量双重审核。

计划制定于 2026-09-13，2026-09-14 开始实现。各任务只在实现、测试与审核完成后勾选；制定计划与选择执行方式不代表补投缺陷已修复。
