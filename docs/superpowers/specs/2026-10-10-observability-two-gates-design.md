# 可观测性双门禁设计

_2026-10-10 · 双门禁设计及第三阶段构建来源补充均已书面确认；物理门禁尚未验收_

---

## 📋 结论与边界

把“普通 PostgreSQL 回归”和“依赖真实观测服务的验收”明确分开，但二者都必须通过。CI 的 PostgreSQL job 通过，不再被解释为可观测性已经验收完成。

本轮沿用 master `8fdbe6b` 的本地组合工作树。上轮受测代码为 `3b1b8eb`，之后 `6fbdf18` 只追加交接文档。[上轮检查点](/path/to/switchback/docs/superpowers/plans/2026-10-09-observability-integration-checkpoint.md)保留 Go exit0、7 项 SKIP、严格检查 exit1 的原结果，不回写为新门禁通过。

三个已比较的路径：拆分两个必过门禁（用户选择，依赖和结果清楚）；默认 CI 部署所有物理 fixture（仍可行，但增加资源和维护成本）；先保持 Draft 不改分组（不能消除本次 CI 组合冲突）。本方案只改变测试组织与验收入口，不修改捕获、执行、模型或观测产品逻辑。

## 🎯 两个门禁分别证明什么

| 门禁 | 依赖 | 必须证明 |
| --- | --- | --- |
| PostgreSQL 自动回归 | 隔离 PostgreSQL | 全量适用的 Go/SQL 集成测试完成，无测试级 SKIP |
| 物理观测验收 | 自有 Docker fixture 与观测服务 | 六项原生测试、原有 G3/G4 场景和真实保留期验证通过 |

PG 门禁保留当前全仓 `go test -json -count=1 -tags=integration ./...` 与 `scripts/check-integration-json` 的严格语义。缺数据库、FAIL、SKIP、截断、身份不匹配仍失败，不加入 allowlist。Unit/race/static/Production Web/browser 等其它原有门禁也不减少。

物理门禁在本地受控机器上显式运行，不默认创建云资源或新 self-hosted runner。本阶段不更改 GitHub branch protection、不上传私有轨迹、不自动设置 PR Ready。PG CI 绿不能替代本地物理门禁；未来若需要服务端强制 required check，另行授权接入证据存储及校验。

## ⚙️ 测试如何分组，如何防止漏测

用显式 build tag `observability_native` 隔离原生验收。Worker 与 published-claim 子例同时要求 `integration && observability_native`。架构文件中的纯配置/契约测试仍默认编译，不能给混合文件整体加 tag 丢掉它们；`TestAcceptanceQueriesAgainstMigratedPostgres` 仍留在 PG 门禁。

| 原生验收身份 | 当前源文件 |
| --- | --- |
| `TestLocalObservabilityExploreNative` | `internal/architecture/local_observability_explore_test.go` |
| `TestLocalObservabilityMonitoringNative` | `internal/architecture/local_observability_monitoring_test.go` |
| `TestLocalObservabilityAlloyFreshVolumeNative` | `internal/architecture/local_observability_native_test.go` |
| `TestLocalObservabilityPromQL` | `internal/architecture/local_observability_promql_test.go` |
| `TestTelemetryWorkerDockerNativeCollectionAndReplacement` | `internal/localproduct/telemetry_worker_docker_integration_test.go` |
| `TestSupplementProofsReadPublishedClaimThroughProductionLoader` | `test/acceptance/observability/matrix_integration_test.go` |

第七个 SKIP 是 `TestMonitoringRegistryFixture`：它在容器内持续提供 `/metrics`，不是完成后返回 PASS 的验收。单独置于 `observability_fixture` tag，现有 cross-compile 只为 helper binary 显式加入该 tag，容器仍精确运行这个 helper。它不进入 PG/原生验收计数；父 `MonitoringNative` 的真实 scrape/query/告警断言继续验证它，没有将 helper 当作成功测试。

拟新增原生入口 `scripts/test-observability-native`：先检查已声明 fixture、所需 env、固定镜像和 exact ownership，再按六个 exact 身份运行三个相关包，输出原始 Go JSONL。保留原断言、生命周期与时间上限；没有配置时明确失败，不靠测试 SKIP 判通过。每份日志仍经过原严格 checker；再检查六项均实际启动并 PASS，避免错误的 `-run` 或空包绿灯。

覆盖对照必须证明：拆分前后所有可执行的普通测试仍在原默认/PG 集合；六项原生测试在物理集合；helper 仅在 fixture binary。新增负例覆盖原生项遗漏、重复/截断、SKIP、错误 commit/hash/claim、未绑定 fixture、只有空包 PASS。不能仅检查“没有 SKIP”，也不能用新合成 PASS 代替真实物理证据。

2026-10-10 用户另行确认最小守恒适配：旧 `2ee4c24bfd43621dae7acc73445fece17e73f5ae` 基线不变，旧测试身份、规范化函数体和分组仍受保护；仅为本轮只读cold/matrix新增13个测试/辅助函数显式登记固定身份、token SHA256和九个顶层测试的默认/integration分组。未登记新增、旧测试删除/变化、登记内容变化或错误分组仍失败；没有通用allowlist、运行时输入manifest或从当前HEAD自动生成期望值。守恒工具适配和测试作为原第三任务的必要修订，具体见[只读子计划](../plans/2026-10-10-observability-readonly-verifiers.md)。此批准不扩大真实资源窗口或Ready范围。

## 🔐 物理证据与 Ready 边界

物理门禁继续执行当前验收工具和原 G3/G4 标准：真实 scrape/query/dashboard/alert 与 scoped Loki/Langfuse 关联；cold-start；至少 20 个 healthy/outage 对；loss/recovery；七天 retention 的实际存储过期。原 settlement/dispatch 上限与 p95 断言不变，不以 smoke、配置文本或六项原生测试代替整套验收。

沿用现有 controlled ACP/model/Tool fixture，不新增真实 Agent/provider 流量，也不把受控 fixture 当成真实 Codex 全能力验收。宿主 PostgreSQL、非本 claim 的容器、旧证据与数据不作为物理测试的写入或故障注入目标。

拟新增只读汇总入口 `scripts/check-observability-gates`，绑定同一受测代码 commit、原始日志 hash、测试集合、claim 和实际 fixture/image 身份；复用现有物理 verifier，不把手填 `passed` 当权威。声明 commit 与实际构建/镜像之间也必须有可核验的来源记录，不能只校验 SHA 字符串格式。任一证据缺失、过期于代码变更、不完整或 verifier 非零，汇总就失败。

已有验收和失败证据保留为历史事实。旧 published claim 可以继续用于 loader 兼容性回归，但不能单独算当前候选的物理门禁通过；当前候选须有新 claim 和新证据。一次失败后保留原目录/日志/hash，需要重跑就新建 claim；不覆盖旧数据或复用宿主常驻 PostgreSQL。

两个门禁通过只是 Ready 的必要条件，还需要 `capture_cancelled` 的根因/处置与独立审查闭合，以及原有完整回归。没有复现不等于修复。当前仍不勾选 OpenSpec 3.2，不 push、Ready 或合入。

## 📦 第三阶段补充：构建来源记录

用户已允许仅在验收工具中新增“受测 commit、源码清单摘要、构建日志、实际镜像 ID”的绑定。本节落实这项授权，不改产品逻辑、任何 Dockerfile、依赖锁或旧证据。先交付可独立验证的来源记录，再接入原计划的只读汇总；二者的工具自测都不算物理验收。

仍有两种实现路径：推荐在验收工具中包住真实构建，记录输入与结果；只给现有报告补 SHA 字段无法证明构建来源，拒绝采用。修改产品 Dockerfile 或向产品注入版本字段超出本轮边界，不采用。

### 从 exact commit 构建，不从可变工作目录构建

新增构建入口只接受当前干净 HEAD 的完整 commit，核验它包含指定 master 基线。读取该 Git commit 的完整 tracked 源码快照，放入本 claim 下独占创建的 `build/`，而非直接使用宿主当前工作目录作为 Docker context。不读取 ignored/untracked 文件、部署 env、provider 凭据或旧 claim。拒绝符号链接、submodule、非普通文件和非法路径；当前候选不存在 tracked symlink/submodule。

保存 commit、tree，以及按相对路径排序的源码清单：Git mode、字节数、文件内容 SHA256。原 Dockerfile、锁文件与 ignore 文件也属于清单。构建前后都核对快照的完整内容与清单；验证时从 Git commit 重算清单，不能靠调用者填写的摘要或镜像 label 判通过。快照和日志保留为私有证据；不在可变源码树里生成文件。

### 三次真实构建形成一条来源链

| 角色 | 原有 Dockerfile | 必须绑定 |
| --- | --- | --- |
| Backend | `images/backend/Dockerfile` | 本次源码快照与实际 Backend image ID |
| Worker 基础镜像 | `images/worker/Dockerfile` | 同一源码快照与实际 base image ID |
| controlled Worker | `test/acceptance/observability/Dockerfile.worker` | 同一快照、本次 Worker base 与实际 controlled image ID |

工具执行固定的三次 `docker build`，不能接受调用者指定旧 base 或任意 Dockerfile/build args。三个目标 claim tag 已存在时拒绝覆盖。controlled 构建的 `WORKER_BASE` 只指向本次基础镜像的独占 claim tag，构建前后 inspect 都须匹配本次 base ID。使用原有缓存，`--pull=false`，基础镜像缺失时先失败，不自动拉取。下载依赖的构建网络只在 root 申报的构建窗口内使用；不启动 Agent/provider，不挂载 Docker Socket/宿主数据给构建容器，不传环境密钥。

每次保存原始 argv、开始/结束时间、退出码、原始 stdout/stderr 构建日志及其 SHA256、`--iidfile` 原文、选定字段的实际 image inspect 结果。记录实际 Docker context/Engine identity 和平台。只有退出码为零、日志完整、iidfile 与 inspect ID 一致、源码快照未变，才能生成成功来源记录；不把日志中的一个 PASS 字符串当证明。允许合法缓存命中，但不能跳过真实构建命令后沿用旧记录。

### 只读核对与失败边界

来源验证入口只读 claim 中的原始材料及 Git commit，重新核对源码清单、构建命令、日志 hash、三项 IID 和 base 关系；返回验证后的镜像身份供汇总使用。汇总还须把它们与原始 Backend、fake-model 和实际 Worker captures 里的 image ID 对齐，再独立重算 PG/native/G3/G4/retention 校验。单独通过来源验证不表示 fixture 已运行或物理门禁通过。

新来源记录缺失、错 commit/tree/hash、快照或日志被替换、构建非零、旧 base/image、跨 claim、输入 symlink/路径逃逸、不完整记录均非零。已有 `build/` 或目标证据文件时拒绝覆盖；失败目录和原日志保留，重跑使用新 claim。本记录是受信任本地验收操作者的可核验执行记录，不是硬件签名，不能抵抗能同时篡改工具和全部原始材料的宿主攻击者。

负例自测使用真实临时 Git 仓库和仅替代外部 Docker 的命令边界，验证源码改动、构建失败、IID/base 替换、日志截断、重复输出及跨 claim 均被拒绝；明确标为工具自测。root 的真实构建、六项 native 和全栈/retention 正例必须另取新证据。现有 verifier 中会写 PASS 文件的 CLI 不直接在旧目录重跑：汇总复用其只读验证逻辑，保留既有写入模式与断言。

## ✍️ 实施范围与验收

书面确认后已生成[实施计划](../plans/2026-10-10-observability-two-gates-implementation.md)，当前实施与验证见[检查点](../plans/2026-10-10-observability-gates-checkpoint.md)。范围：上述七项入口及必要的新拆分测试文件、`.github/workflows/ci.yml`、`scripts` 门禁/覆盖对照/负例测试、对应使用说明。复用 `scripts/run-observability-isolation-acceptance` 与现有 verifier；如验证缺少 commit/资源来源证据，先报告缺口，再按明确的验收工具范围补足，不能静默扩大到产品逻辑。

不改 ACP、Factory/profile/YHC、WorkerControl/settlement、250ms 捕获预算、30s 原补充夹具预算或 SQL/发布断言。不动主 checkout/旧观测树 dirty、旧 claim、数据卷和日志；新物理资源仍须 root 核实 exact ownership 后管理，证据相关卷保留。现有原生测试自建的临时无业务 fixture 按其原有 cleanup 规则处理，不拿它们的规则清理历史数据。

完成标准：PG 严格门禁无 SKIP；原生六项和 G3/G4/retention 均有真实当前候选证据；覆盖对照及失败负例通过；资源与旧证据保全核验；独立审查明确本次范围及未解决项。拆分本身完成不自动关闭历史取消问题或整个 OpenSpec change。

源码依据：`.github/workflows/ci.yml:45-81`、`scripts/check-integration-json`、表中测试及 `internal/architecture/local_observability_monitoring_fixture_test.go:21-61,102-138`；现行验收标准在 [OpenSpec tasks](/path/to/switchback/openspec/changes/local-observability-stack/tasks.md) 和 [观测契约](/path/to/switchback/openspec/changes/local-observability-stack/specs/local-observability-stack/spec.md)。架构会话已确认本轮设计路径和候选实现范围无占用；该协调确认不替代设计审批。
