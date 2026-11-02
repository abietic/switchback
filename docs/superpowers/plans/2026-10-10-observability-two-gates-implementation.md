# Observability two gates implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保留全部测试和原断言，把 PostgreSQL 自动回归与原生物理验收变成两个不能相互替代的严格门禁。

**Architecture:** 测试只按需要的环境分组，不修改产品行为。普通集成入口保持全项目及严格 JSON 检查；原生入口必须证明六个指定测试实际执行。最终只读汇总需要原始证据及真实构建来源，资料不足时失败，不输出虚假的通过。

**Tech Stack:** Go build constraints / AST、Bash、jq、现有 Docker Compose 和物理验收 verifier；不增加依赖。

## Global Constraints

- 工作目录仅为 `.worktrees/observability-master-integration-1009`；固定 master 基线 `8fdbe6b556a0602b8392a2eed0e09b9e007542d1`，实现起点 `2ee4c24bfd43621dae7acc73445fece17e73f5ae`。
- 遵循 [已确认设计](../specs/2026-10-10-observability-two-gates-design.md)；不改产品 capture、ACP、Factory、profile、YHC、WorkerControl、settlement；不扩大 250ms/30s 原时限。
- `scripts/check-integration-json` 原样保留；不允许 test-level SKIP、删除测试、降低 SQL/publish/物理断言或把空包 PASS 当作验收。
- 六个原生测试使用 `observability_native`；Worker 与 published claim 保留 `integration && observability_native`；container helper 独用 `observability_fixture`。
- 纯测试、共享纯函数、PostgreSQL SQL prepare 保持原默认/集成覆盖；helper 不是 PASS 验收项。
- 架构会话已确认本轮申报文件和低并发 Go/PG 无交叉占用；root 独占本任务 fixture 所有权，不保证整机 CPU 独占。所有 Go 命令使用 `GOMAXPROCS=2` / `-p=1`。
- 物理 Docker/全栈窗口另行协调；未经窗口确认不得执行原生套件。不启动真实 Agent/provider；不复用宿主 PostgreSQL 或旧业务卷。
- 旧 dirty 工作树、失败证据及 G3/G4/retention 验收证据保留。旧 claim 可作 loader 回归，不能证明新候选验收。
- 不 push、Ready、合入或勾选 OpenSpec 3.2；`capture_cancelled` 不因通过或分组关闭。

---

## 📋 文件与接口

| 文件组 | 职责 |
| --- | --- |
| `internal/architecture/local_observability_*test.go` | 拆开纯测试、原生测试和容器 helper |
| `internal/localproduct/telemetry_worker_docker_integration_test.go` | 原生 Worker 双标签 |
| `test/acceptance/observability/matrix_integration_test.go` 与新 native 文件 | SQL prepare 留在 PG；published claim 移到 native |
| `test/acceptance/observabilitygroups/` 与 `scripts/check-observability-test-groups` | AST 测试身份/函数体及真实构建清单比较 |
| `scripts/check-observability-native-json` 与 `scripts/test-observability-native-json` | 严格六项执行证据及反例 |
| `scripts/test-observability-native` | fail-closed preflight、执行、保留原 JSONL |
| `scripts/check-observability-gates`、验收 verifier | 只读双门禁汇总；要求源码和实际镜像来源 |
| `.github/workflows/ci.yml` | 保留 PG 主命令，仅增加工具自测 |

## ✍️ 实施任务

### Task 1: 分组与覆盖守恒

**Files:** 修改上述 architecture 六个现有文件、Worker 文件、`matrix_integration_test.go`；新增 explore/monitoring/published-claim 原生分拆文件、fixture helper 文件、`test/acceptance/observabilitygroups/main.go` 和 `main_test.go`、`scripts/check-observability-test-groups`。

**Interfaces:** `scripts/check-observability-test-groups BASE_COMMIT`，只读检查 git 基线和当前树；exit0 仅表示测试身份和覆盖分组守恒，任何缺失/新增未登记/函数体变化/错误构建组 exit1，参数错误 exit2。使用 Go AST 枚举顶层 `Test*` 和规范化函数体，使用 `go list -json -test` 的 TestGoFiles/XTestGoFiles 核验实际 build constraints；不执行被枚举的测试。模块为 `github.com/abietic/switchback`。

- [x] 先写黑盒 characterization：原树未隔离返回非零；丢失默认纯测试、丢失 SQL prepare、原生 test 未入 native、helper 入普通组、修改原测试断言均返回非零；有效机械分组返回零。自测使用临时小仓库，覆盖实际 Go 构建选择，不比较格式化空白。
- [x] 保存 RED 输出；先记录起点三个包默认/integration 的真实构建测试清单，不运行原生或 DB 测试。
- [x] 原生函数和仅原生 helper 原样移动；纯共享函数保留默认。具体边界：ExploreNative 92 行之后、MonitoringNative 483 行之后、PromQL 文件的 `number` 若被默认测试引用则移回纯文件、matrix 的 published claim 26–127 行移走，SQL prepare 129 行后保持 integration。
- [x] 标签使用如下完整声明；helper server 的 opt-in 和 HTTP 行为不动，构建命令只增加 fixture tag。

```go
//go:build observability_native

// Worker / published claim 文件使用：
//go:build integration && observability_native

// container helper 文件使用：
//go:build observability_fixture

// newMonitoringFixture 中的 cross-build：
build := exec.CommandContext(ctx, "go", "test", "-c", "-tags=observability_fixture", "-o", binary, ".")
```

- [x] Go AST 原函数体比较只允许上述 helper cross-build 参数变化，不允许任意函数白名单绕过；普通分组仅排除六项原生与一项 helper；native 包清单要求六项全部存在，fixture 清单仅要求 helper，纯测试留默认。
- [x] 运行 `GOMAXPROCS=2 go test -p=1 ./test/acceptance/observabilitygroups ./internal/architecture -count=1`、`GOMAXPROCS=2 scripts/check-observability-test-groups 2ee4c24bfd43621dae7acc73445fece17e73f5ae`；三个相关包 `go test -p=1 -tags=integration,observability_native -run '^$'` 和 architecture fixture tag 均必须编译通过。预期无 DB/Docker 调用，原生不执行。
- [x] root 检查机械移动/清单/原断言，取得独立任务审查，再本地提交 `test: isolate native observability acceptance without coverage loss`。实际提交 `659027a`，恢复验证及最后独立复审 Approved。

### Task 2: 严格原生入口

**Files:** 新增 `scripts/check-observability-native-json`、`scripts/test-observability-native-json`、`scripts/test-observability-native`；按需新增入口 preflight 自测；修改 `.github/workflows/ci.yml` 的 static 自测步骤。不得修改现有 strict checker。

**Interfaces:** JSON checker 接受一个原始 Go JSONL 文件；native runner 接受一个尚不存在的绝对输出目录（exclusive create，私有权限），环境来自实际 claim/owned fixture；参数错 exit2，preflight/Go/checker 任一失败 exit1，只有六项真实 PASS 且严格 checker 通过才 exit0。preflight 不回显 token/完整 DB URL，不 pull 镜像、不创建外部 fixture、不启动真实 Agent。

六个完整测试 identity（package + Test）固定如下：

```text
github.com/abietic/switchback/internal/architecture TestLocalObservabilityExploreNative
github.com/abietic/switchback/internal/architecture TestLocalObservabilityMonitoringNative
github.com/abietic/switchback/internal/architecture TestLocalObservabilityAlloyFreshVolumeNative
github.com/abietic/switchback/internal/architecture TestLocalObservabilityPromQL
github.com/abietic/switchback/internal/localproduct TestTelemetryWorkerDockerNativeCollectionAndReplacement
github.com/abietic/switchback/test/acceptance/observability TestSupplementProofsReadPublishedClaimThroughProductionLoader
```

- [x] 写 characterization fixtures：六项真实 run/pass 成功；少一项、错误包、重复、SKIP、截断、包空 PASS、helper 冒充、只有自填 passed 字段、Go 失败全都失败。先运行缺失脚本的 RED，再最小实现。此处是合成事件的工具自测，不是物理正例。
- [x] Checker 先调用未修改的 `scripts/check-integration-json`，再用 jq 比较精确顶层 identity 集合；不容忍七项/空集合。保留合法子测试事件，不把 package event 当作测试 PASS。
- [x] Runner 必须要求所有现有 opt-in/env：LOG_CLIENT、MONITORING_NATIVE_TEST、TEST_ALLOY_NATIVE、OBSERVABILITY_NATIVE_TEST、TEST_WORKER_TELEMETRY_DOCKER、WORKER_ALLOY_URL、WORKER_LOKI_URL、TEST_DATABASE_URL、PROOF_TEST_OUTPUT、PROOF_TEST_DATABASE_URL；缺任一直接失败，不等待 t.Skip。
- [x] 额外要求 `SWITCHBACK_OBSERVABILITY_PROJECT` 对应 claim 的精确 project；通过 `docker inspect` 核对已运行 task-owned client/PG/Alloy/Loki 容器、Compose project、loopback ports/URL，以及 compose 中固定且本地缓存的实际 image identity。纯字符串看起来 loopback 不足以证明归属。需要更多 fixture 名称时用明确必填环境参数，不能扫描复用任意服务。
- [x] 保留 preflight 身份、HEAD/tree/dirty、实际镜像 ID 和原始 JSONL；不把这些记录声称为全栈产品镜像源码证明。执行命令如下，run expression 为六项精确名称的锚定并集；helper 不进入命令。

```bash
GOMAXPROCS=2 go test -p=1 -json -count=1 -timeout=25m \
  -tags=integration,observability_native \
  -run '^(TestLocalObservabilityExploreNative|TestLocalObservabilityMonitoringNative|TestLocalObservabilityAlloyFreshVolumeNative|TestLocalObservabilityPromQL|TestTelemetryWorkerDockerNativeCollectionAndReplacement|TestSupplementProofsReadPublishedClaimThroughProductionLoader)$' \
  ./internal/architecture ./internal/localproduct ./test/acceptance/observability
```

- [x] `scripts/test-observability-native-json`、preflight negatives、现有 `scripts/test-integration-json` 必须通过；runner 无环境时必须明确非零且无 Docker/DB 测试执行。CI 原 `go test -json -count=1 -tags=integration ./...` 和 strict checker 原样保留，仅 static 增加上述自测/分组检查（基线固定并确保 checkout 可取到）。
- [x] root 核验、独立审查、本地提交 `test: add fail-closed native observability gate`。实际提交 `39b4c3a`，独立审查 Approved；六项物理正例仍待 root 取得新 claim 和 Docker 窗口后执行，不虚构 PASS。

### Task 3: 源码绑定与双门禁汇总

**Files:** `scripts/check-observability-gates`、`test/acceptance/observability` 的证据检查和特征测试、当前 checkpoint；不修改产品路径。

**Interfaces:** 汇总必须只读，消费 exact candidate、PG raw JSONL/hash、native raw JSONL/hash/六项清单、独占 claim 与真实 fixture/build provenance，以及已有 cold/matrix/report/retention verifier 的完整输入；输出两个独立门禁状态，任何缺失/stale/不匹配/校验错误 exit1。它不是 Ready/merge 授权。

- [x] 先核对 `report.go` 的 SourceCommit 和 `matrix.go` recipe：当前 SHA 格式和运行时 HEAD 不是镜像构建来源。沿当前实际 capture/build 记录追踪 source tree → backend/worker image ID → fixture → claim → verifier 输入。已证实来源链不完整；用户已授权最小验收工具适配并确认[设计补充](../specs/2026-10-10-observability-two-gates-design.md#-第三阶段补充构建来源记录)。
- [ ] 若完整链已可验证，写错 commit/hash/claim/旧镜像/缺 G3、cold、20 pairs、loss/recovery、实际 retention 的 RED tests；汇总调用原 strict/native checkers 和现有物理 verifier，不接受用户填 `passed` 字符串替代原始证据。
- [x] 最小适配与补充规格均获用户确认；按[来源记录子计划](2026-10-10-observability-build-provenance.md)完成独立 build/check 工具，代码提交 `739e30a`，fresh 任务复审 Approved，root 精确候选纯工具验证 exit0。未修改产品或 Dockerfile；外部 fake 不代表实际来源链已采集。
- [x] 按[只读验证子计划](2026-10-10-observability-readonly-verifiers.md)完成 cold/matrix 只读入口，代码提交 `0208668`，独立审查 Approved，root 同候选 race/default/守恒/vet 等检查 exit0。保留旧 writer/断言/JSON；固定 `2ee4c24` 基线不变，仅显式登记获准的13项新增测试/辅助函数。此子交付不代表双门禁汇总或物理验收完成。
- [x] 按[查询留档子计划](2026-10-11-observability-query-evidence.md)完成受控 GET 私有原始响应与 HTTP 离线回放，代码提交 `74132ab`，独立复审 Approved，root 同候选两种临时目录环境的工具验证 exit0。原 G3 断言/JSON不变，旧测试/登记未改；完整 source facts 重建及离线 G3 验证仍未交付，不以 manifest 冒充通过。
- [ ] 接入只读汇总；实际新候选来源链仍待真实构建与 fixture capture。保留已完成的 Task 1/2，禁止编一个只查 SHA 格式的绿色汇总；此为已确认设计的 fail-closed 边界，不是删除第三任务。
- [ ] root 协调物理窗口、新 claim/镜像、独立 loopback PG 后执行完整 PG strict gate、六项 native 和所有物理项目；不重复消费旧绿色证据。不满足全部标准时 OpenSpec 3.2 保持未勾选。
- [ ] 每个可验证实现单独审查和本地提交；最终全分支审查仅在新候选全部回归及 capture_cancelled disposition 有证据后进行。

## 🔍 自审与交付

本计划覆盖已确认设计的分组、两个严格入口、覆盖守恒、原始证据、源码归属和失败反例。Task 3 特意保留真实构建来源检查：当前只知道既有报告记录 SHA，不将其假设成构建证明。不存在修改执行语义或扩大资源窗口的任务。

每阶段给用户的简报只说明：实际改了什么、实际通过哪些检查、哪些验收还没执行或失败；详细日志留 root 私有目录。不得使用“测试全绿”概括尚未执行的物理门禁。
