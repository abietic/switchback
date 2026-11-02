<!-- Structure adapted from the decision-record template in SuperiorByteWorks-LLC/agent-project; Apache-2.0. -->

# Codex-only ACP 深化与并行交接设计

| 字段 | 内容 |
| --- | --- |
| 状态 | Accepted：用户已批准按推荐方案继续，并要求兼顾方案 3；进入 Codex-only 实施，不自动扩大范围 |
| 日期 | 2026-10-05 |
| 本会话 | `01a021d1-3d8a-7300-bef7-d577a63414d7` |
| 协作会话 | `01a050db-aa9b-7732-869d-19a8d0c2aff0`，集成可观测性与 YHC Harness |
| 不可变开发基线 | `72a96845c44c993723c4261a647b351374d0bcbc` |
| 隔离分支 | `codex/acp-codex-only-deepening`，由本会话附属 managed worktree 承载 |

## 🎯 目标与约束

本轮只做 Codex 适配器的行为等价深化：让 ACP 协议、会话、原始帧记录与进程生命周期拥有一个可测试的模块，Codex 的启动与兼容策略留在薄适配器中。目的不是现在交付第二个 Agent，而是让后续 YHC 接入不必复制一套 ACP 状态机。

并行工作的成功标准是三件事同时成立：没有相互覆盖的文件写入，没有不一致的协议或生命周期改动，没有污染对方验收的共享资源。工作树隔离只解决第一件事的一部分。

本轮不改变 `application.AgentAdapter`、domain/contracts、WorkerControl、startup outcome、Turn settlement、模型观测、secret registry、镜像/Compose、依赖版本或数据库 schema；不实现 YHC profile，不引入客户端可选 executable/env/path，不修改共享进度文件，不自动 merge/rebase/push。

## 🔍 当前证据

首轮分工核查快照及本轮后续复核：

- 主工作树位于 `codex/production-view-architecture-deepening`、HEAD `2a42a591c288f73dee2e6db5fbcf8c4827f7184c`，有用户未提交的 UI、测试及进度改动；本轮不在该工作树写入。
- 初次核查时，对方开发工作树位于 `.worktrees/observability-supplements-v1`、HEAD 为上述 `72a96845`，未提交改动集中在终态遥测的 Backend 时钟修复及相关测试、设计和进度文档。
- 本轮后续复核时，该切片已提交为 `29098fd8f5dd0e4fdc86f20b41ea97665b30a2a7`，对方开发工作树 clean；从 `72a96845` 到该提交的七个文件仍全部位于观测代码、测试、设计和进度范围，没有触及 `codexacp`、拟新增 `acpclient` 或 `shimruntime`。本会话不可变起点仍为 `72a96845`，未自动 rebase 或合入该提交。
- 对方已通过跨会话消息明确确认：当前时钟修复及紧随其后的 `capture_cancelled` 排查不计划修改 `codexacp`、`shimruntime` 或 Factory；允许本会话在独立工作树推进 Codex-only 等价接缝。
- 从主工作树 HEAD 到开发基线，ACP 两个目录的提交差异仅包含 `shimruntime/runtime_test.go` 的测试增量；`codexacp` 生产代码没有提交差异。未提交时钟修复没有被复制到本会话基线。
- 当前消费者已经依赖 `application.AgentAdapter` 的六个方法；Shim 的 `Run` 仍通过 `codexacp.New(trace)` 构造实际 Agent。无需新增 application/domain 端口来实现本轮深化。
- 不可变基线的 `codexacp/adapter.go` 同时管理 Codex 策略与 ACP 机制；当前源码已由 `acpclient/{adapter,process,rawtee,client}.go` 管理协议与安全机制，`codexacp/{adapter,policy}.go` 保留兼容门面和固定 Codex 策略。

源码依据见文末。协作状态来自本轮实时消息及 Git 核查，不把历史进度勾选作为当前完成证据。

## ⚖️ 方案比较

| 方案 | 收益 | 代价与边界 | 判断 |
| --- | --- | --- | --- |
| 共用 ACP 核心 + 固定 Codex 薄适配器 | 协议和安全不变量集中；后续 Agent 可复用同一机制；消费者接口不变 | 需要少量仅供受信任 Go composition 使用的内部构造参数，必须保持 Codex 策略显式 | 推荐 |
| 仅在 `codexacp` 内拆文件 | 改动最小，便于先降低阅读负担 | 不能提供独立可复用的 ACP 接缝；YHC 仍可能复制协议状态机 | 适合纯局部清理，但未充分解决本次目标 |
| 同时引入 Factory/registry 并接入 YHC | 一次形成多 Agent 路由 | 扩大 auth/profile/startup/镜像范围，与对方长期接入目标相交；行为不再等价 | 延后，单独评审和交接 |

## 🧩 推荐模块边界

| 模块 | 拥有的复杂性 | 不拥有的责任 |
| --- | --- | --- |
| 新增 `adapter/acpclient` | typed ACP 连接与 capability snapshot；会话状态机；Prompt/Cancel/Close；有界原始帧；记录后发送；受限文件回调；子进程/进程组生命周期 | Agent 选择、业务状态、deadline、WorkerControl、模型观测与 runtime recovery |
| 保留 `adapter/codexacp` | 固定可执行文件、UID、环境和路径；现有 gateway capability/认证请求策略；Codex system-error update 识别；兼容门面 | 另一套协议状态机、帧泵或 SDK 会话客户端 |
| 保留 Shim 消费者 | 继续调用 `codexacp.New(trace)` 并使用现有 `AgentAdapter` | 直接了解 ACP 核心内部实现或增加新 Factory 路由 |

保留 `codexacp.Adapter` 及 `New(trace)` 的现有公开方法/签名。门面组合 ACP 核心，保持当前所有消费者无需修改；第一轮不编辑 `shimruntime` 的源码或测试。

ACP 核心的构造输入仅由受信任 adapter/composition 代码提供，包含进程构造、固定 Workspace 根、Trace 目标和现有 Codex 必需的认证/更新判定策略。使用一个小的内部构造对象，不把这些参数扩散成 application/domain ports，不建立插件 DSL、全局 registry、任意 shell command API 或运行时配置语言。

认证策略必须显式且构造时验证完整，不能以缺失 hook 表达“无需认证”。本轮只配置现有 gateway 策略；YHC 的认证能力及启动方式仍需其接入阶段单独验证。协议版本和公共安全校验不能被 Agent 策略回调豁免。

### 方案 3 的后续衔接

未来 Factory 只在受信任 composition 中把服务端拥有的 exact profile id/revision/digest 解析为现有 `AgentAdapter`。未知、缺失或不兼容 profile 必须拒绝，不能静默回退 Codex，也不能把 TaskSpec 的任意 command/env/path 转成核心构造参数；本轮不新增 Factory、profile 字段、注册表或配置解析器。

本轮保留三处已存在的变化点：进程 launcher、认证 capability/请求策略、Agent 专属 update 判定。核心只负责验证认证策略完整、协商声明的认证方法、校验 token 并在认证帧记录前脱敏；Codex gateway metadata 与 `systemError` 规则仍归 Codex facade。没有认证回调绝不表示无需认证；没有专属错误判定只表示使用公共 ACP 协议/背压失败规则。

接入 YHC 前须另行固定实际启动环境及凭据范围，验证其 ACP 版本、认证扩展、resume/load、取消与终态，并通过同一核心接缝的契约测试。startup outcome、观测 watermark、WorkerControl 和 settlement 如需变化，由对应 owner 独立设计、同步 exact paths 后再实施。这里只记录交接条件，不宣称 YHC 当前能力已获验证。

## 🔒 等价与安全不变量

| 边界 | 必须保持的可观察行为 |
| --- | --- |
| 构造与进程 | 固定 binary/env/UID/Cwd；不继承上游模型凭证；验证进程组；保留现有 Close 的 TERM/KILL 顺序与超时结果 |
| Initialize/Authenticate | exact protocol、identity/generation 校验；记录 capability snapshot；仍要求 gateway auth；本地代理 token 在认证帧持久化前确定性脱敏 |
| OpenSession | 一个 Adapter 一个 Session；exact Workspace/Session identity；已声明 resume 优先，否则仅在声明 load 时 fallback；未知结果仍 fail closed |
| Prompt/Cancel | 一个 active Turn；保留有界 updates 和背压失败语义；取消只命中 exact active Turn；已入队终态后的同 Turn cancel 保持幂等；不关闭仍可用 Session |
| Trace | 保留 JSONL event shape、direction、marker 和 frame 字节；typed 解码前记录；Trace 失败不得先交付未记录的出站帧；未知可选 update 留证并告警，不投喂不兼容 typed SDK |
| 文件与可选能力 | 保留逐组件 `openat`/`O_NOFOLLOW`、普通文件检查与 Workspace 范围；permission 和 terminal 仍拒绝，不能靠新适配层扩大能力 |
| 输出与错误 | 保留现有 normalized update/terminal/error payload、返回错误的可观察含义和兼容文案；Codex system-error 判定不能变成所有 ACP Agent 的默认规则 |
| 外部流程 | Shim 的模型观测 drain、deadline/watchdog、control reconnect、事件发布及 settlement 顺序保持原状；不得将已知问题顺手修入本轮 |

如果实现发现必须改变这些行为，停止该切片，单独提出行为变更并重新与对方确认范围，而不是扩大“等价重构”的定义。

## 🧪 验收设计

先在不可变基线上建立外部行为的 characterization coverage，再移动机制；测试观察 RPC 调用顺序、peer bytes、Trace、update/terminal 和拒绝结果，不依赖重构后的内部字段布局。

复用现有 Go helper process 与临时 Workspace，不运行宿主 Codex、Docker、数据库或真实模型。应覆盖：

- typed lifecycle、多 Turn 复用、exact cancel 与 terminal 后幂等 cancel；
- resume/load capability 路由及 protocol/identity mismatch；
- 原始帧拆分/合并、未知可选 update、Trace 写入失败先于 peer delivery、token 脱敏；
- Workspace 文件和 symlink/非普通文件拒绝、permission/terminal 拒绝；
- 固定 launcher/env/UID 策略、进程关闭及未确认退出结果；
- Codex 门面与核心通过现有 `AgentAdapter` 观察到的行为一致。

测试随其责任迁移到 ACP 核心与 Codex 门面。Task 3 在 `2aca90b` 后以 public `New` + 真实 stdio peer 建立四个 focused test files，替代内部 client/rawtee 测试；保留现有门面 lifecycle/cancel/resume/load characterization。三个任务独立 Spec/Quality 审查及 `72a96845..0d09bf2` 全分支审查均 Approved，无 findings。主会话在源码 HEAD `0d09bf22b000ff3b38b01bb720dc52440f676aad` 新跑双包 ordinary/race、低并发 workerruntime/architecture short、scoped vet、两入口 compile、fmt/diff 均通过；具体命令、actual paths 和限制见[同主题计划的本地交接](../plans/2026-10-05-codex-only-acp-deepening.md#-本地切片交接2026-10-05)。

```sh
GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/codexacp
GOMAXPROCS=2 go test -p 1 -count=1 ./internal/workerruntime/adapter/acpclient
```

完整 localproduct/integration、真实 Codex/YHC、容器与远端 CI 验收尚未运行；本地短测试不证明这些门禁完成。对方的观测回归不作为 ACP 等价性证据，仍不借用其容器、卷、测试端口或冻结证据。

本轮未对历史机制重新完成全面安全审计，也没有动态补证既有 Close “无法确认退出”错误分支。非 root UID、确定性 OS 分片边界和 queued-terminal 竞态仍有计划所列证据限制；保持原算法及兼容语义不等于这些分支均已动态覆盖。

## 🤝 文件归属与同步规则

| 区域 | 本轮 owner / 规则 |
| --- | --- |
| `adapter/codexacp/**`、拟新增 `adapter/acpclient/**`、本设计及后续同主题独立计划 | 本会话；仅在附属隔离工作树写入 |
| 终态 Backend 时钟、`capture_cancelled`、当前观测设计/测试/进度、观测发布分支与 Draft PR #2 | 协作会话；本会话保持只读 |
| `shimruntime/**` | 本轮双方近期无修改计划；本会话主动排除第一轮编辑，后续如需修改再交接 |
| `AGENTS.md` | 本会话仅增加一条 ACP 模块导航；观测 owner 已确认本轮及下一轮不改该段；不改业务、安全或生命周期规则 |
| AgentAdapter/domain/contracts、modelobs、runtime secret、startup/lifecycle/settlement、schema/migration、images/Compose、YHC | 非本轮重构范围；任何一方新增改动前先同步 exact 文件与语义 |
| 主工作树、共享进度、远端发布、数据库/容器/真实模型与验收证据 | 不共同写入、不复用、不自动合入；变更 owner 与资源窗口单独确认 |

有意义的同步点：本设计提交、用户书面评审后首次源码写入之前、改动范围或关键错误假设变化、对方提交/公开 HEAD 更新、扩大测试资源、提交待集成切片与 YHC 交接。

每次同步携带自己的 baseline/HEAD、changed paths、已完成/未完成的门禁及下一步。首次源码写入与集成评审前重新核查对方 HEAD 和 dirty paths；发现交集后先停止相交切片并重排，不修改对方工作树、不自动 stash/reset/rebase。

```mermaid
flowchart LR
    accTitle: ACP Deepening Handoff Gates
    accDescr: The Codex-only slice starts after written design approval. Integration and the later YHC handoff require renewed baseline and resource coordination with the observability owner.

    design_review["评审 Codex-only 设计"] --> characterize["固定现有行为证据"]
    characterize --> deepen_core["深化 ACP 核心"]
    deepen_core --> local_checks["验证局部等价行为"]
    local_checks --> resync_owners["同步双方 HEAD 与范围"]
    resync_owners --> integration_review["协调组合回归与集成评审"]
    integration_review --> yhc_handoff["另行交接 YHC 接入"]
```

集成顺序不是立即 rebase 对方未提交改动。先提交独立切片、交换 exact HEAD；在观测修复具有已提交基线和可用回归窗口后，评审组合差异和兼容结果。YHC 注册、startup outcome 或后续 profile 行为另起已批准切片，不从本设计自动获得授权。

## 📋 风险与评审门禁

| 风险 | 应对 |
| --- | --- |
| 只有一个 Agent 时过度泛化 | 保留现有消费者接口；仅抽已存在的协议机制与已知 Codex 策略；不预设 YHC 能力、不建全局 registry |
| 抽取改变锁顺序、帧顺序或取消终态 | 先建立外部行为测试；逐机制迁移；局部/race 验证分别记录，不因可编译而宣称等价 |
| 后续 `capture_cancelled` 涉及相同运行时路径 | 当前分工不是永久锁；任何范围扩大先发 exact 文件与语义、等待重新确认 |
| 独立工作树仍共享宿主资源和 Git refs | 避免重型验收重叠；不改对方分支/资源；提交与发布由各 owner 独立负责 |
| 观测基线前移造成组合行为漂移 | 记录 `72a96845` 为本轮起点；集成前与对方新已提交 HEAD 重新比对并验证 |

设计自检要求：无占位符；Codex facade、认证与安全责任一致；没有把后续 YHC/观测行为带入本轮；实现路径不依赖共享工作树或共享测试资源。

用户已批准 Codex-only 核心实施与方案 3 的未来交接规划；该批准不授权未实现的 Factory/profile/YHC 自动实施或合入。本地实施、三个任务审查、源码全分支审查及上述本地门禁已完成；远端 CI、组合验收和合入仍未完成。后续文档状态闭合不改变已审查源码 HEAD，最终 Git HEAD 与观测 owner 的交接单独记录。

## 🔗 源码依据

- [现有 AgentAdapter 及请求/结果类型](../../../internal/workerruntime/application/ports.go)
- [Codex 兼容门面](../../../internal/workerruntime/adapter/codexacp/adapter.go)
- [固定 Codex command/env/UID、认证和 classifier](../../../internal/workerruntime/adapter/codexacp/policy.go)
- [公开 ACP 核心构造](../../../internal/workerruntime/adapter/acpclient/options.go)
- [ACP 会话、Prompt/Cancel](../../../internal/workerruntime/adapter/acpclient/adapter.go)
- [进程关闭](../../../internal/workerruntime/adapter/acpclient/process.go)
- [原始帧记录、先记录后发送、脱敏与未知 update](../../../internal/workerruntime/adapter/acpclient/rawtee.go)
- [受限 Workspace 文件回调与拒绝能力](../../../internal/workerruntime/adapter/acpclient/client.go)
- [现有 Codex 生命周期与帧行为测试](../../../internal/workerruntime/adapter/codexacp/adapter_test.go)
- [真实 peer 核心生命周期与认证测试](../../../internal/workerruntime/adapter/acpclient/adapter_test.go)
- [真实 peer Trace 契约](../../../internal/workerruntime/adapter/acpclient/frames_test.go)
- [真实 peer 文件与权限边界测试](../../../internal/workerruntime/adapter/acpclient/callbacks_test.go)
- [stdio peer fixture](../../../internal/workerruntime/adapter/acpclient/peer_test.go)
- [未改动的 Shim 消费者与执行循环](../../../internal/workerruntime/adapter/shimruntime/runtime.go)

以上链接反映当前实施路径；不可变开发基线仍为 `72a96845`，历史基线结构与当前实现不能混作同一快照。
