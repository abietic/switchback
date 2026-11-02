# Grilling 0001: Requirements And Technical Design

> 日期：2026-07-07。
>
> 输入文档：[REQUIREMTENTS.md](../../REQUIREMTENTS.md)、[TECHNICAL_DESIGN.md](../../TECHNICAL_DESIGN.md)。
>
> 目标：把当前设计中会影响模块边界、数据模型、安全策略和 P0 范围的模糊点逼出来，作为下一轮需求交互的问题清单。
>
> 2026-07-10 更新：用户已回答第三轮关键问题；本文把产品边界、P0 产物、事件顺序、知识/能力治理和基础设施基线写为已决项。

## Executive Read

当前文档的总体方向是对的：Assistant 做澄清和 TaskSpec，Worker 做隔离执行，EventCenter 做异步分发，Evolution/Inspiration 做长期上下文。但设计还没有到可以直接开代码骨架的程度，因为几个基础边界仍然没有被钉死：

- P0 到底交付“真实代码改动闭环”，还是先交付“mock Worker + Trace/Event/Artifact 闭环”？
- Delivery 是飞书入口 adapter，还是承担审批、确认、任务编排和用户交互状态？
- Frontier 是前端 BFF/实时网关，还是 Gateway 的读模型输出？
- Truth 与 Inspiration 是两个并列上下文源，还是 Inspiration 索引 Truth 并统一编译 ContextPack？
- Worker runtime 是每任务短生命周期，还是用户/项目常驻会话？
- Trace 是否保存完整模型原文、工具输出、文件片段？脱敏发生在 Worker、LLMProxy、Trace writer 还是统一管道？

## Resolved Decisions

| Question | Decision | Consequence |
| --- | --- | --- |
| P0 demo 是否需要真实 Agent | 必须是真实 Coding Agent，并且在容器中完成动作，不影响宿主机。 | P0 不能只做 mock Worker；需要容器化执行、后端网络通信、Agent session 记录和 Artifact/Trace 闭环。 |
| Worker runtime 生命周期 | 用户级别常驻一段时间，超出交互时间后回收；未超时前可像普通 Coding Agent 一样继续 interact。 | WorkerSession 改为用户级交互会话；WorkerRun 是会话内针对 TaskSpec 或后续交互的一次执行。 |
| Delivery 能否直接创建 TaskSpec | 可以，但创建面受 scope 约束。 | Group ordinary message 必须先进入 Assistant；结构化 `/task`/form、绑定 ready TaskDraft 的确认，以及策略允许的 P2P/private/API 明确请求可直达；readiness、intent、权限和 validator 独立兜底。 |
| Trace 保真度与脱敏 | Trace 要像普通 Coding Agent 本地 session JSONL 一样记录；目前先不考虑脱敏。 | P0 优先保真、审计和可复盘；脱敏作为后续治理能力，不阻塞核心闭环。 |
| Truth/Inspiration/ContextPack 所有权 | Truth 主要归 Evolution，读取也由 Evolution 提供；Inspiration 提供索引和简要内容；具体飞书文档读取由 lark-cli 和对应 Skill 根据链接让 Agent/Assistant 自主完成。 | Evolution 是 Truth owner 和 read provider；Inspiration 不是文档内容代理，也不直接读取完整飞书文档。 |
| P0 主 Agent | 先用 Codex，后续 Agent 应该可选。 | P0 建 `CodexAgentProfile` 和 `CodexAdapter`，但保留 AgentProfile/AgentAdapter 扩展点。 |
| 容器运行时方向 | 参考传统 CI/CD 的容器支持方式，在本地创建容器实现。 | RuntimeProvisioner 采用 runner 编排 Docker-compatible container 的模式；未来可替换为 K8s 或内部沙箱。 |
| WorkerSession 资源限制 | idle 48 小时、最大存活 2 week、Tenant/User continuity quota 10。 | waiting/idle/lost 仍计数，queued/unassigned Task 与 pending Continuation 不计数，closed/expired 终态释放；有效交互续期 idle deadline，但不能突破 14 天绝对上限。 |
| Trace 观测系统 | Trace 最好接入 Langfuse。 | 对象存储 JSONL 仍是权威事实源；Langfuse 是 session/LLM/tool/cost/latency 的观测投影。 |
| Codex 容器控制方式 | 镜像通过 Docker 构建；认证通过环境变量或挂载配置注入；Worker 通过 ACP 控制 Codex，不裸启动 Codex。 | Worker 控制逻辑拥有 session state、JSONL、Trace/Artifact 上传和 EventCenter 回传；需要 spike Codex ACP adapter 版本和事件覆盖。 |
| Worker 内模型代理 | Codex 使用同容器 LiteLLM co-process。 | Codex provider config 指向 localhost；P1/K8s 可拆 sidecar，P0 不实现 LLMGateway。 |
| WorkerSession 恢复语义 | 未超时前默认恢复旧 WorkerSession，并以 ACP chat/command 方式继续交互。 | 后续用户交互不是新建任务，而是在现存 session 内产生新的 WorkerRun/AgentSession。 |
| Session 映射 | WorkerSession 同时只绑定一个 active RuntimeInstance/container；历史 RuntimeInstance 与 AgentSession 都可一对多。 | 崩溃恢复复用 `worker_session_id`、递增 runtime generation 并记录新 container id。 |
| WorkerSession idle timeout | 固定为 48 小时。 | 有效交互续期 idle deadline，但不能突破 14 天 absolute deadline。 |
| P0 用户产物 | 先支持 report 和 patch。 | 用户围绕产物继续对话；branch、PR、CI 不属于默认 P0 闭环。 |
| Go ACP SDK | P0 采用 `github.com/coder/acp-go-sdk` 作为容器内 Worker runtime shim 的 ACP client。 | 该库是 ACP 官方 Community 列表中的社区 SDK，不是官方 SDK；仍需 pin 版本并做 capability/event spike。 |
| Gateway / Frontier / Frontend | Gateway 处理同步命令和关键元信息；Frontier 提供 projection/SSE；Frontend 负责渲染。 | EventCenter 驱动 Frontier projection；前端交互与执行逻辑解耦。 |
| Delivery 边界 | 只负责飞书触发、中间选择/对话和最终结果卡片。 | Task/Assistant/Worker 持有业务状态；Delivery 只持有通道投递状态。 |
| Worker 审批线 | 沙箱内普通实现和重操作直接执行；新增外部鉴权、跨沙箱、生产写和未授权能力安装才审批。 | 减少审批噪声，同时保留外部副作用边界。 |
| Task 清晰度判断 | Assistant 通过类似 brainstorming/clarify 的询问作语义判断。 | TaskSpec validator 只做 schema、权限和策略硬校验。 |
| Event 排序/去重 | 按 session/task stream 和序号排序，按事件 id/幂等键去重。 | 时间戳不能单独保证分布式顺序，需 `stream_id + sequence`。 |
| Trace 保留与隔离 | 默认全部落盘并尽量不自动删除，按 tenant/workspace namespace + Membership/ACL 隔离，由后端运营平台管理。 | 保留 archive/tombstone/受审计删除，避免“永久保留”阻断纠错和合规。 |
| Truth 发布 | 模型判断自动发布或人工审核，行为类似工具审批。 | 确定性风险策略可否决模型自动发布，高风险/冲突/低置信强制审核。 |
| OpenViking | 可以强依赖。 | 官方调研后收紧为 active Truth/Resource 的强依赖检索投影；候选、修订、审核、审计和看板由 Switchback RDBMS 治理。 |
| Inspiration 形态 | 独立代码仓库，保存飞书文档/Wiki/妙记的摘要、URL 和意图辅助上下文。 | 需要专门 brainstorming 仓库 schema、更新/审核/发布流程。 |
| 模型网关 | 当前只实现 LiteLLM。 | “迁移边界”只是不泄漏专有字段，不在 P0 再造 LLMGateway。 |
| Skill 安装审批 | 预装和 Evolution 生成 Skill 无需额外用户确认；其他 Skill 需用户批准，Assistant 明确安装对话视为批准。 | 仍需平台策略检查，Assistant/Worker 通过 Skilling 内部 API 管理。 |
| 基础设施方向 | P0 用 GitHub、Docker、PostgreSQL outbox、本地文件 ObjectStore、LiteLLM、OpenViking；P1 再验证 GitLab、RocketMQ、S3 endpoint 和 K8s。 | Jenkins、MongoDB、本地 GitLab/K8s、RocketMQ、MinIO 不进入默认 P0。 |
| CodePlatform 委托 | 接受受管 B2；保留方案 A 与混合 C 作为 fallback。 | RepositoryAccessGrant 后 Agent 可直接使用短期 Token 和原生 `git`/`gh`，不逐命令审批；root/refresh secret 留在 CredentialBroker，管理权限禁止，known token 必须剔除，外部写入按 provider 状态核查。 |

## Hard Questions

### Q1. P0 的第一个可演示闭环是什么？

文档里 P0 同时出现“接入至少一种主流 Coding Agent，通过 ACP 执行任务”和“先稳定协议与 mock Worker”的倾向。两条路的成本完全不同。

已决策：P0 必须接入真实 Coding Agent，并且执行发生在容器中，通过网络请求与后端通信，不能影响宿主机。P0 主 Agent 先用 Codex，后续通过 AgentProfile/AgentAdapter 支持可选 Agent。

已决策：

- 镜像通过 Docker 构建。
- 认证通过环境变量或挂载配置进入容器，长期应由 SecretLease 管控。
- Codex 不作为裸进程被 Worker 启动和解析输出；Worker 通过 ACP 控制 Codex。
- session state 和 JSONL 由 Worker 控制逻辑管理，并通过后端请求存入对象存储；事件回传 EventCenter。
- Codex 模型请求通过同容器、仅监听 loopback 的 LiteLLM co-process。

已决策：第一个 demo 必须由真实 Codex 在容器中完成任务并产出 `report` 和 `patch`，用户随后可以在同一 WorkerSession 继续交互。测试可作为 report 的证据，但 branch、PR、CI 不是 P0 默认产物。

仍需回答：`acp-go-sdk`、`codex-acp` 和 Codex 的精确 pin、capability/event 覆盖、supervisor、health check 与 graceful drain 如何固定。

### Q2. Delivery 是入口，还是交互编排器？

`REQUIREMTENTS.md` 说 Delivery “直接创建 Worker 任务”，也说模糊任务回到 Assistant 澄清；`TECHNICAL_DESIGN.md` 把 Delivery 放在 Entry Layer。这里必须避免 Delivery 悄悄长成第二个 Assistant。

已决策：Delivery 可以直接创建 TaskSpec，但只负责飞书触发、中间选择/对话和最终结果卡片渲染。Task/Interaction/Worker 持有业务状态；Delivery 只持有通道投递和卡片状态，并继续通过 validator、身份、权限和审批策略兜底。

### Q3. Gateway、Frontier、Frontend 的边界是什么？

文档里 Gateway 接收请求，Frontier 做实时投影，但有些流程描述成 EventCenter 回传到 Gateway/Frontier。这会影响 WebSocket/SSE、读模型和权限检查位置。

已决策：Gateway 负责同步命令、关键元信息查询和操作；Frontier 独立维护面向 Frontend 的 projection，并提供实时 SSE；Frontend 只渲染。EventCenter 驱动 Frontier projection，不直接向浏览器推流，Gateway 不承担实时输出。

### Q4. Truth 和 Inspiration 的关系是什么？

当前文档把 Truth 写入 OpenViking，同时 Inspiration 维护外部知识索引并返回 ContextPack。`docs/architecture/context-map.md` 里又出现 Knowledge Evolution 把 Truth 给 Inspiration 的倾向。这里需要钉死：谁是长期事实源，谁是索引源，谁负责编译上下文。

已决策：Truth 所有权主要归 Evolution，读取也由 Evolution 提供；Inspiration 提供外部资料索引和简要内容，不负责读取完整飞书文档。需要具体读取飞书文档时，由 Agent 或 Assistant 依据 Inspiration 返回的链接，调用 lark-cli 和对应 Skill 自主读取。

进一步决策：OpenViking 是 active context 强依赖检索投影，但不是治理事实源。Inspiration 是独立代码仓库，保存外部资料 URL、摘要和意图辅助上下文；采用 YAML manifest + Markdown context + generated index + 可重建运行时投影，人工、同步器和模型的语义变更统一走 PR/CODEOWNER 审核。ContextPack 仍由 Assistant/Worker 自行编译。

### Q5. WorkerSession 是短生命周期还是常驻？

需求文档写“创建或复用执行环境”，技术设计又倾向短生命周期隔离环境。这个决定会影响安全、成本、缓存、恢复、session 映射和清理策略。

已决策：WorkerSession 是用户级别常驻一段交互时间的会话。idle timeout 固定 48 小时；未超时前通过 exact target 恢复旧 session，并以 ACP chat/command 方式继续 interact。P0 绝对最大存活时间为 14 天，Tenant/User WorkerSessionContinuityQuota 默认 10，计数与满额行为见 ADR 0051。

必须回答：

- workspace id 与 WorkerSession 的映射是否也按一对一处理，还是允许同一 WorkerSession 多 workspace？

已决策的映射：

- `worker_session_id` 与当前 active RuntimeInstance/container 一对一。
- `worker_session_id` 在首次启动时固化；崩溃恢复复用该 id、递增 runtime generation 并产生新 container id。
- `worker_session_id` 与 Codex/Agent session id 可以一对多，以支持 new session 和 subagent。

### Q6. Trace 的保真度和脱敏边界是什么？

文档强调全链路审计，但没有定义哪些内容能进入 Trace。Coding Agent 的原始输出可能含密钥、内部代码、用户隐私、模型思维链或供应商敏感字段。

已决策：Trace 要做到接近普通 Coding Agent 记录在本地的 session JSONL；目前先不考虑脱敏。Trace 需要导出到 Langfuse，但 Langfuse 是观测投影，不替代对象存储 JSONL 权威轨迹。

进一步决策：P0 全部落盘、尽量不自动物理删除，按 tenant/workspace namespace + Membership/ACL 隔离，并提供后端运营平台管理 Trace/Artifact/Langfuse 引用。仍需明确后续脱敏位置、冷归档和受控删除策略。

### Q7. “足够明确的任务”由谁判断？

Assistant 需要决定追问还是派发，但文档只说“足够明确”，没有判定规则。

已决策：Assistant 通过类似 Superpowers brainstorming 或 OpenSpec clarify 的渐进式询问做语义判断，并按 TaskReadinessPolicy 生成七维 TaskReadinessAssessment。无 blocking unknown 且 TaskSpec validator 通过才派发；低风险缺口进入 assumptions；每次只问一个问题，连续三问后产生 ClarificationCheckpoint。用户继续后重置下一组三问预算，不设置会话总追问硬上限。

### Q8. EventCenter 是否允许业务补偿？

技术设计明确 EventCenter 不是事实源，但需求文档写它“统一处理所有事件”。“处理”这个词容易引发误解。

已决策：每个 stream 由唯一 StreamOwner 在领域状态与 Outbox 同一事务中分配 `sequence`；EventCenter 不重编号，只做 delivery、duplicate/gap/integrity incident、retry、DLQ、replay 和 projection。跨 stream 不定义全局业务顺序，以 correlation/causation 表达因果；Timeline projector 分配 append-only TimelineCursor，迟到事件追加并标记 late。P1 RocketMQ message group 复用 stream_id，但 payload sequence 仍是权威。详细图示和示例见 [Event Ordering And Timeline Guide](../architecture/event-ordering-explainer.md)。

### Q9. 自动开 PR 的权限线在哪里？

技术设计把产物列为 diff/branch/PR/report，但没有定义哪些任务可以自动写远端仓库。

已决策：P0 产品默认只交付 report/patch。CodePlatform 采用受管 B2：WorkspaceRole 本身不授权 GitHub；RepositoryAccessGrant 生效后，Agent 可直接运行短期 Token 允许的非管理类 `git`/`gh` 操作，不再为每个 push/PR 命令询问。branch/PR/CI-read 可以作为任务中的外部操作和引用，但不因此成为 P0 renderer/交付承诺。

信任边界已明确：短期 access token 对 Agent 可用；GitHub App private key、OAuth refresh token 和长期 PAT/root credential 只留在 CredentialBroker。P0 禁止 administration、secret/variable、workflow/Actions write、ruleset、delete 和组织权限；protected branch 由 provider ruleset 防守。平台已知 Token 必须在 Trace/Langfuse/Artifact 前确定性剔除，push/PR 结果不确定时先按 remote/provider 状态核查。

备选已保留：安全政策禁止 Token 进入 Agent 时切方案 A（控制面执行全部远端操作）；Provider 写 scope 过粗或 ruleset 不足但仍需原生远端只读时切方案 C（Agent 只读直连、控制面写入）。详细图示与示例见 [Managed B2 CodePlatform Access Guide](../architecture/codeplatform-managed-b2-explainer.md)。

### Q10. LLMProxy 是平台服务还是 sidecar 模式？

文档同时提到 LiteLLM、LLMGateway 和 sidecar 劫持。这里会影响 Agent 适配、审计一致性和模型密钥下发。

已决策：P0 只实现 LiteLLM。容器内 Codex 指向同容器 localhost LiteLLM；LiteLLM 访问上游 provider。所谓“迁移边界”只要求 TaskSpec、ModelPolicy、ModelInvocation 和 Trace 不出现 LiteLLM 专有字段，不要求现在实现中心 LLMGateway。

## Terminology Conflicts

| Term | Current Problem | Proposed Canonical Direction |
| --- | --- | --- |
| `Task` | `REQUIREMTENTS.md` 里把 Task 写成“派发的任务规格”，但 `CONTEXT.md` 已把 TaskSpec 和 Task 拆开。 | `TaskSpec` 是规格；`Task` 是平台承诺执行和跟踪的任务。 |
| `AgentRuntime` | 需求表里把实际 Coding Agent 与运行环境合在一起。 | 拆为 `AgentProfile`、`Agent Adapter`、`RuntimeLease`、`Workspace`。 |
| `Event` | 需求表中“领域事件或集成事件”合并。 | `DomainEvent` 属于聚合内部事实；`IntegrationEvent` 是跨模块信封。 |
| `session id` | Assistant session、WorkerSession、WorkerRun、外部 Agent session 都可能叫 session。 | 用户可见查询优先用 `task_id`/`conversation_id`；内部执行用 `worker_session_id`/`worker_run_id`。 |
| `Truth` | 有时像长期事实，有时像 RAG 文档。 | Truth 是有证据、范围、风险、生命周期的长期断言。 |
| `Inspiration` | 有时像服务，有时像独立仓库，有时像 ContextPack compiler。 | 已决策为外部资源索引和简要内容提供者；ContextPack 由 Assistant/Worker 自行编译。 |
| `Frontier` | 有时像 BFF，有时像实时投影，有时和 Frontend 混用。 | Frontier 是实时输出/读模型层，Frontend 是 UI。 |

## Candidate ADRs

这些不是现在就该写死的 ADR；它们满足“难回滚、有取舍、未来读者会困惑”的条件，一旦你给出答案就应该记录。

1. P0 直接接真实 Agent，并要求容器隔离和网络通信。
2. P0 主 Agent 选 Codex，但保留 AgentProfile/AgentAdapter 可选 Agent 边界。
3. Worker runtime 按 CI/CD runner 风格在本地创建 Docker-compatible 容器。
4. Worker runtime 生命周期采用用户级交互会话、空闲回收、14 天绝对超时和用户级 10 并发。
5. Trace P0 采用类似本地 session JSONL 的保真记录，暂不做脱敏，并导出 Langfuse 观测投影。
6. Truth 归 Evolution 并由 Evolution 提供读取；Inspiration 提供索引和摘要，不代理完整飞书文档读取。
7. EventCenter 不作为业务事实源，只做事件分发和投影驱动。
8. LiteLLM 作为 P0 co-process、P1 Pod sidecar，领域契约不暴露其专有字段。
9. WorkerControlChannel 采用 gRPC 双向流主通道与 HTTPS long-poll/event POST fallback；大文件独立走 resumable HTTPS。
10. 每个连续 RuntimeRecoveryIncident 最多自动重建一次 RuntimeInstance；达到稳定健康或完成持久进度 checkpoint 后未来新 incident 才重新获得一次机会，重复失败或 ACP resume 不安全时进入 waiting_user。
11. Runtime heartbeat 默认 15 秒，45 秒 suspect 暂停新命令并探测，90 秒 unhealthy；固定 ACP/Codex 栈无安全 ping，Agent 无响应只由硬进程/连接证据或 cancel grace 耗尽判断。
12. CodePlatform 使用受管原生 Git/GitHub CLI 与 repository-scoped 短期委托；方案 A/C 保留为策略 fallback。

## Next Interview Order

第四轮进一步确认：

- 每个 WorkerSession 绑定一个 Workspace；一个 Workspace 可承载多个 WorkerSession，并包含零到多个 RepositoryCheckout。Workspace 是持久逻辑上下文，不等同 RuntimeInstance。
- Worker 详情页直接交互形成 AgentTurn，支持 prompt、queue、interrupt、steer，不经过 Assistant；Assistant 侧新消息再按意图创建 Task 或 WorkerRun。
- `waiting_user` 暂停 idle timeout；ApprovalGrant 支持 once 或受 capability/target/constraint 限制的 WorkerSession scope。
- WorkerRun 完成即阶段完成并展示 report/patch，但 WorkerSession 仍可继续交互。
- patch 需要 diff 审查、预览、下载、应用和显式发布分支；发布分支需要匹配的 RepositoryAccessGrant，session Grant 生效后不逐命令重复审批。
- OpenViking 官方实现支持 CRUD、分层检索、账户/用户隔离和 snapshot/restore，但没有候选审批、完整修订治理或 Memory 看板；Switchback PostgreSQL/Evolution 是治理权威，OpenViking 是 active serving projection。
- 简单低风险 Memory 可自动发布并进入用户看板；Instruction 和 Skill 的生成/修改必须人工审核。
- Inspiration 已确认采用 Markdown context + YAML manifest + generated index + PostgreSQL/OpenViking 可重建投影；人工、同步器和模型的语义变更一律走 PR/CODEOWNER 审核。
- Workspace 并发已确认采用隔离 WorkingCopy：WorkerSession 并行产出 ChangeSet，WorkspaceApply 在短时写租约下串行生成 WorkspaceRevision，禁止共享目录并发写和 last-write-wins。
- Worker Runtime Shim 后端控制采用传输无关 WorkerControlChannel：gRPC 双向流优先，transport/HTTP2/proxy 不可用时回退 HTTPS long-poll + event POST；两者共享 sequence、ACK、cursor 和幂等语义，旧 ChannelEpoch 必须被 fencing。
- Supervisor 自动恢复已确认：只对 runtime/agent failure 自动重建一次，保留 WorkerSession/WorkingCopy/持久 Agent state/Trace cursor，递增 generation 并重新签发 SecretLease；取消、超时和安全/协议错误不自动恢复。
- Runtime liveness 已确认：15s/45s/90s 默认阈值，服务端 receive-time/sequence 判定；replacement 前必须证明旧 generation 已失去 WorkingCopy 写权限，避免 split-brain。
- Agent quiet 与执行时间已由 [ADR 0055](../adr/0055-use-a-fixed-agentturn-hard-deadline.md) 重新冻结：`active_quiet` 只告警；每个实际 ACP prompt 独立使用固定 24 小时 wall-clock hard deadline，approval/waiting/断线/平台故障/recovery 均不暂停，steer 不重置且不能延期。
- 原 ADR 0020 的 WorkerRun/AgentTurn/Tool 三层预算、active clock、ExecutionBudgetGrant 和 profile 数值已被取代；P0 只保留 Turn deadline、取消/fencing、Session 48h idle/14d absolute lifetime 与独立 LLM rate/token/cost guard。
- Tool 取消不确定已确认风险分级：本地不可证明停止则 taint/recover runtime；远端 read-only 或有幂等+核查证据时 fencing 后继续；non-idempotent/unknown 副作用进入 waiting_user；迟到结果只进 Trace。
- Tool 元数据权威已确认分层：Manifest 版本化声明，Adapter 验证/标准化且只能提高风险，Capability/Skilling ToolGovernancePolicy 最终生成 ResolvedToolPolicy，TaskSpec/Approval 只能收紧，Agent 无权改变；缺失、冲突或不可验证一律按最高风险 fail closed。
- Assistant readiness 已确认结构化 gate：七维 assessment、无 blocker + validator 通过才进入 ready、低风险 assumptions、单问题追问，以及每三问一次可继续重置的 ClarificationCheckpoint；实际派发还要求 initiator-bound active creation intent。
- MemoryTruth 自动发布已确认确定性白名单：仅 explicit preference、verified descriptive fact、entity alias、强制 TTL 的 ephemeral project state；必须有用户明确/权威/确定性验证证据，模型推断无发布权；行为型、敏感、冲突、跨 scope、失败归纳和主观评价进入审核或拒绝。
- Memory 看板已确认双视图：MemoryLibrary 管 active/disabled/expired，ReviewInbox 分 lane 管待审内容；批量 approve 仅限同 scope/policy/type 的低风险 Memory，其他高风险内容逐条批准；用户纠错通过 revision/disable/tombstone/restore 和 Outbox，P0 无硬 SLA。
- Event 顺序已确认分层：领域 StreamSequence 由唯一 owner 事务化分配，跨 stream 用 causation，TimelineCursor 只负责 SSE/分页和稳定展示；重复、缺口、同序号冲突、迟到与 RocketMQ 映射均保留同一契约。
- WorkingCopy 物理方案已确认采用 A：宿主持久目录承载 session 副本和 Agent state，容器只 bind mount 自己的副本；Git 使用 per-session worktree/不可变 commit ref，非仓内容使用 manifest/checksum。
- WorkspaceApply 已确认拆成租约外 Prepare 与短租约 Finalize：先构建完整不可变 StagingRevision，再用 PostgreSQL lease row + 单调 fencing token，在一个事务中提交 WorkspaceRevision、active pointer、Apply result 和 Outbox。多仓原子性是指针选择完整 manifest，提交前崩溃只留下可清理 staging。
- WorkingCopy 方案的目录图、单仓/多仓示例、竞争 Apply、过期 token 和崩溃恢复见 [workspace-storage-and-apply-explainer.md](../architecture/workspace-storage-and-apply-explainer.md)。
- Tenant/Workspace 授权已确认采用 A：Tenant 硬隔离，active TenantMembership + 显式 WorkspaceMembership，可组合 viewer/editor/reviewer/owner，Project/Team 不隐式授权，禁止跨 Tenant 共享同一 Workspace identity。
- Worker 委托授权已确认：Worker 代表 actor user 使用短期 ExecutionAuthorizationLease；成员撤权会 fencing command/read/Apply/Artifact/secret 并保留 session/evidence。WorkspaceRole 与外部 Capability/Approval/Secret/provider 权限相互独立。
- 授权判定图、角色矩阵、Worker/branch publish 时序和撤权流程见 [tenant-workspace-authorization-explainer.md](../architecture/tenant-workspace-authorization-explainer.md)。
- Trace/Artifact 生命周期已确认采用 A：TraceManifest 索引 ObjectStore 权威 segments，默认 hot 30 天、warm 至 180 天、cold 长期且不自动物理删除；Langfuse 可短保留，cold 可 restore 供排障和 Evolution replay。
- 运营与审计已确认从简：核心用途是排查问题、促进 Evolution 和构建效果看板。Dashboard 走 normalized facts 的 TraceAnalyticsProjection；archive/restore/quarantine/delete 只记录 actor/target/reason/state/result，不默认要求工单或四眼审批。
- 显式删除已确认保留最小 tombstone 和非敏感聚合，并把 Evolution evidence 标记 `source_deleted`。完整图示见 [trace-artifact-retention-and-analytics-explainer.md](../architecture/trace-artifact-retention-and-analytics-explainer.md)。
- CodePlatform 已确认受管 B2：镜像预装锁定版 Git/`gh`/Skills；RepositoryAccessGrant 后 Agent 在单仓、能力和期限内直接操作；CredentialBroker 负责短 Token 续期/撤权，管理权限不下发，ruleset 防守受保护分支，known token 强制剔除，provider 写入通过 CodePlatformOperation 核查收敛。
- 方案 A 与 C 不是被否决后遗忘：安全政策不允许 Token 进 Agent 时回退 A；写权限过粗但需要原生 read Git 时回退 C。三种 profile 复用 Grant、Operation 和 provider result 契约。完整流程见 [codeplatform-managed-b2-explainer.md](../architecture/codeplatform-managed-b2-explainer.md)。
- TaskSaga durable execution 已确认采用 PostgreSQL 方案 A：Task/TaskSaga/SagaTransition/SagaWakeup/Outbox 同事务，due wakeup 使用 `SKIP LOCKED`、短 lease、fencing 和 expected revision 投递幂等命令；事务和 poller 都不直接执行外部副作用。
- Temporal 在 P0 延后，不永久排除。公司已有受管平台、Saga/timer 复杂度或 poller 运维/SLO 明显超过当前实现时重新提出 OpenSpec/ADR；不实现假设可无缝替换两类执行模型的通用 workflow engine。完整图示见 [postgresql-tasksaga-explainer.md](../architecture/postgresql-tasksaga-explainer.md)。
- TaskSaga 聚合粒度已确认方案 A：一 Task 一 TaskSaga，Task 终态后不重开。Task 终态前的业务重试在原 Saga 创建新 TaskAttempt/WorkerRun；runtime recovery 保持原 Attempt/Run。WorkerSession 可跨 Task 存活，直接后续交互形成 standalone WorkerRun 并可保留 origin task 谱系；显式后续目标创建新 Task/Saga。
- 后续 Task 创建已确认方案 A：规范动作是“创建后续 Task”而非转换旧 Run。Worker 可建议并生成 FollowUpTaskProposal，但只有用户确认、结构化命令、明确自然语言创建指令或 Assistant/Delivery/API 显式提交才形成创建意图；模型推断不能自动创建。Gateway/通道记录 intent evidence，Task Orchestration 重验 revision/hash/readiness/权限并幂等创建 Task/Saga/Outbox。
- 前端信息架构已确认方案 A：Tasks 表达可跟踪交付，Workers 以 worker_session_id 表达独立可交互 WorkerSession；Home/ActionInbox 聚合待办，Workspace 聚合上下文，Artifact 使用 canonical detail，MemoryLibrary/ReviewInbox 独立治理，Conversation 不作为所有 Task/Worker 的父树。Frontend 只渲染 Frontier projections 并通过 Gateway 发命令。
- Frontier 同步协议已确认方案 A：HTTP ProjectionSnapshot + typed SSE ProjectionDelta，共享 ViewEpoch/ViewCursor/ProjectionVersion；Last-Event-ID 在有限 replay window 内续传，gap/old epoch/replay miss 触发 resync，schema 不兼容要求客户端升级。GraphQL 不进入 P0，只保留未来 read adapter 可能。
- Agent display 持久性已确认方案 A：DisplayCheckpoint 完整物化并推进 ViewCursor，TransientDisplayDelta 只做 chunk 动画且不推进 durable cursor。Tool/Approval/Artifact/error/terminal 必须 checkpoint；gap 到下一 checkpoint 收敛，Trace JSONL 继续高保真记录 source frames。
- DisplayPart 已确认方案 A：Switchback 自有 `text/reasoning_summary/tool/activity/approval/artifact/source/error` 物化 union；ACP/Codex、AG-UI、AI SDK 只经 adapter 映射。稳定 id/revision/ordinal 和 owner refs 跨 checkpoint 保持，unknown optional progress 安全降级，unknown security/permission/Tool lifecycle fail closed；critical unknown 要求客户端升级。
- Frontend runtime 已确认方案 A：React + TypeScript + Vite SPA，同源接 Go Gateway/Frontier；Node 只用于 build/dev。Router、TanStack Query、ViewProjectionStore、transient overlay、PendingCommand 各有单一 owner；assistant-ui 只作为 Worker Chat ExternalStore adapter。
- Delivery card 已确认方案 A：独立 DeliveryCardProjection + CardInstance，四类 card、有限 inline action 和 Web deep-link fallback；callback 只快速接受并转 Gateway，owner evidence 推进 projection。Feishu 时限属于 DeliveryProviderCapability，Delivery 不拥有业务状态。
- 群聊 Workspace disclosure 已确认方案 A：Identity & Access 拥有显式 WorkspaceChannelShareGrant 和每次发送/更新的 ChannelDisclosureDecision。Grant 只授权精确 provider group audience 查看 owner 标记为 channel-shareable 且 `workspace_summary` profile 允许的字段，不授予 Membership/执行/action；无 Grant、audience 扩张/外部化、snapshot stale/unknown 或 revoke/expire 时降级 minimal notice/Web。raw Trace、私有 prompt、hidden reasoning、Secret、Tool stdout、完整 report/patch 永不进入 P0 共享卡片；历史消息只 best-effort cleanup 并记录 residual exposure。
- Workspace rebind Conversation 已确认方案 A：首次 unresolved->W1 保持原 Conversation；W1->W2、clear 或 access revoke 在同一飞书 topic 创建 successor，source 变 superseded 只读，切回 W1 也创建新 generation。ConversationRollover 原子推进 source/target/resolution/handoff/binding/Outbox；默认 handoff 只含 Workspace-neutral typed facts，protected carry-over 独立重验 source read、target permission 和 intent。message、archive、ContextCompiler、search/export 与 Evolution 按 generation/scope 隔离，Task/Worker/Trace/Artifact 不迁移。
- ChannelThreadBinding 路由已确认方案 A：一个 active provider 话题默认绑定一个 primary Assistant Conversation，普通消息始终形成 Assistant Turn；TaskDraft/Task/WorkerSession/Approval/Artifact 必须通过当前 card action、结构化 command 或不可变明确交互上下文携带显式 target。不存在自动 current focus，related refs 和 provider reply ancestry 不参与路由；topic 不支持时仅切换 binding identity fallback。
- ChannelThreadBinding Workspace scope 已确认方案 A：Conversation 可无 Workspace 进行 tenant/user-scope 澄清；Workspace-scoped context、Task/Worker 前必须由用户显式选择已有 Workspace 或先显式 CreateWorkspace。global default/唯一候选/URL/model 仅可推荐；TaskSpec workspace/resolution evidence 必填，rebind 不迁移已有资源，显式 target 使用自身 Workspace，撤权后不 fallback。
- 群聊 Conversation ownership 已确认方案 A：一个飞书 group topic 共享一个 channel-scoped primary Conversation，每个 Turn/command 保留真实 actor。provider-mapped participant 可普通对话；只有 controller 可管理 Workspace context、rollover、channel Conversation lifecycle 和 controller delegation。Binding creator 经 policy 校验后成为首个 controller，不能显式移除最后 controller；撤权导致无 controller 时进入 `control_orphaned` 并由 authorized channel administrator 显式恢复。Controller 不授予任何 Workspace/Task/Worker/Approval/Artifact/Disclosure 权限。P0 不做 per-user private sidecar，actor-private detail 使用 ephemeral/Web。完整总览见 [delivery-interaction-design.md](../architecture/delivery-interaction-design.md)，决策见 [ADR 0044](../adr/0044-channel-scoped-shared-conversation-with-explicit-controllers.md)。
- 群聊 Task 创建意图已确认方案 A：TaskReadinessAssessment 与 ExplicitTaskCreationIntent 是独立门槛。group ordinary message 即使 ready 也先进入 shared Assistant Conversation；只有结构化 `/task`/form 或绑定 exact ready TaskDraft 的注册确认动作可创建 Task。P2P/private/API 可按 TaskCreationTriggerPolicy 接受明确 create request。Intent 绑定 actor、draft/spec digest、readiness、Workspace resolution、Conversation generation 和 policy；变化后 stale，重复确认幂等。Ready draft 确认可复用 `waiting_user`，controller 不获得创建权限。完整图示见 [delivery-interaction-design.md](../architecture/delivery-interaction-design.md)，决策见 [ADR 0045](../adr/0045-require-explicit-task-creation-surface-in-shared-channels.md)。
- Shared ready TaskDraft 确认权已确认方案 A：每条 draft lineage 固定 initiator，只有 initiator 可确认；其他 participant/controller 必须显式 AdoptTaskDraft fork 新 actor-owned draft，重新过滤 context、重验 Workspace/权限/readiness 并自行确认。Source intent/grants/secret/private evidence 不继承，source 不被改写；同 actor adoption 幂等，不同 actor 相同 digest 不自动合并。完整时序见 [delivery-interaction-design.md](../architecture/delivery-interaction-design.md)，决策见 [ADR 0046](../adr/0046-bind-taskdraft-confirmation-to-initiator-and-fork-on-adoption.md)。
- Task lifecycle/cost authority 的早期方案记录在 ADR 0047；P0 已由 ADR 0048/0055 简化为 initiator 可 continue/accept/rework/cancel，Workspace owner 仅 emergency cancel，ApprovalGrant 只授权受保护操作，不存在时间预算或延期 action。
- P0 Task 复杂度复审已确认简化：上述 TaskSaga/TaskAttempt/FollowUpTaskProposal/TaskActionGrant 和 Task/Worker 双主视角作为历史方案/P1 备选保留，但被 [ADR 0048](../adr/0048-make-workersession-primary-and-task-lightweight-in-p0.md) 的 P0 基线替代。WorkerSession 是主交互面；Task 只保存 TaskSpec snapshot、initiator、Workspace/WorkerSession/current Run、result refs、`acceptance_mode`、status 和 revision；WorkerRun 表示执行/返工阶段，runtime recovery 只换 generation。
- 单 WorkerSession 顶层并发已确认采用方案 A：同一时刻最多一个 bound Task 和一个 active WorkerRun；busy Task 排队且不 interrupt，需要并行则创建另一 WorkerSession。Task 在 running/waiting_user/delivered 通常保持 binding，终态释放；ADR 0053 absolute expiry 是唯一非终态释放例外。AgentTurn queue 与 SessionTaskQueue 必须分开。见 [ADR 0049](../adr/0049-serialize-top-level-work-per-workersession.md)。
- SessionTaskQueue 最小策略已确认：每个 WorkerSession 使用服务端单调 sequence 做稳定 FIFO；重复 enqueue 保持原位置；P0 允许取消或显式改派到另一 Session 队尾，不实现 priority/reorder/preemption。所有顶层 Run 需要单次 dispatch permit，已有 queued Task 时后到 standalone prompt 不能抢占；slot-release 事件丢失由 reconciliation 恢复。
- WorkerSession 复用与继承已确认方案 A：只有认证用户通过 Worker Detail、注册 card/command 或 exact API 字段指向 `worker_session_id` 才复用；无 target 的新 Task 默认分配新 Session。复用固定继承当前 WorkingCopy、持久 Agent context/AgentSession 和未提交变更，禁止静默 reset/clean/stash/checkout；clean/exact-base 使用新 Session或保持 blocked。每个 WorkerRun 首次 Agent/Tool/write 前记录 WorkerRunStartSnapshot。见 [ADR 0050](../adr/0050-require-explicit-workersession-reuse-and-snapshot-start-context.md)。
- WorkerSession continuity quota 已确认方案 A：按 `(tenant_id, user_id)` 默认 limit 10；`allocating/interactive/running/recovering/waiting_user/idle/lost` 计数，queued/unassigned Task 与 pending Continuation 不计数，`closed/expired` 终态释放。满额 Task/Continuation 进入统一 SessionAllocationQueue FIFO，不自动复用/reset/close/evict；Worker Runtime 原子 reserve+create 与 terminal+release，丢失事件 reconciliation。见 [ADR 0051](../adr/0051-count-continuity-holding-workersessions-against-user-quota.md)。
- WorkerSession 终态续作已确认方案 A：`closed/expired` identity 永不复活，只读保留 Timeline/Trace/Artifact/durable checkpoint；用户从 exact checkpoint 显式派生新 WorkerSession/WorkingCopy/AgentSession 和新 clocks/授权，不继承旧 Session-scoped Grant/Secret/queue/runtime。Task 与 Continuation 新 Session 请求共享 FIFO，checkpoint coverage 对用户可见。见 [ADR 0052](../adr/0052-make-terminal-workersessions-immutable-and-continue-by-derivation.md)。
- Task-bound absolute expiry 已确认方案 A：先 fencing 旧 runtime/control/secret/WorkingCopy 写权，无法证明则保持 lost/quota 且禁止续作；成功后 active Run 以 `timed_out:worker_session_absolute_expired` 终止，queued/running/waiting_user Task 进入或保持 waiting_user，delivered 保持 delivered。partial/complete Artifact 与 TaskSessionLineage 保留；只有 initiator 绑定 exact outcome/checkpoint 才让同一 Task queued 并经 quota/FIFO 创建新 Session/Run，unknown external effect 先核查，不引入 TaskSaga。见 [ADR 0053](../adr/0053-preserve-task-identity-across-workersession-absolute-expiry.md)。
- continuation budget 的 ADR 0054 已被 ADR 0055 取代。`ContinueTaskInNewWorker` 只绑定 initiator、exact Task revision、expiry outcome、checkpoint 和 idempotency；新 Session 中每个实际 AgentTurn 自动获得独立固定 24 小时 deadline。
- P0 Task 状态固定为 `queued/running/waiting_user/delivered/completed/failed/cancelled`。report-only 且证据确定可 `auto` 验收，patch 默认 `manual`；manual 结果进入 delivered，initiator accept 后 completed，反馈返工在同 Task 创建新 WorkerRun。终态后的 Worker chat 为 standalone，显式意图才创建新 Task。
- P0 Task action 使用固定规则：initiator 可 continue/accept/rework/cancel，current Workspace owner 只有 emergency cancel，其他角色不因可见性、editor/reviewer/controller 获权。Task/TaskTransition/Outbox 同事务；通用 ScheduledCommand 保留 PostgreSQL lease/fencing/poller 可靠性，不创建第二套 TaskSaga 权威状态。

- 固定 AgentTurn deadline 已确认采用双重 enforcement：后端 ScheduledCommand 是唯一状态/审计/fencing owner，Shim 为同一 Turn revision/runtime generation 使用不可重置的 local monotonic watchdog；断线期间仍可停止，任一路径使用同一幂等取消 identity，重连后以后端终态证据收敛。ACP prompt 在两侧 enforcement 都 armed 前不得启动。
- AgentTurn 启动握手已选择简化方案 B：后端事务预提交 `started_at/deadline_at/ScheduledCommand` 与 `StartAgentTurn` Outbox 后单向 dispatch；Shim 按 exact revision/generation/epoch 校验 freshness，用 server-time uncertainty 计算保守 remaining，先 arm monotonic watchdog 再调用 ACP。无 RuntimeReady/StartPermit 往返；运输延迟和 uncertainty margin 消耗窗口，stale/expired/无法证明时 fail closed，重投不重置或重复 prompt。
- 终态 WorkerSession checkpoint 已确认方案 A：LiveWorkerState 持续保存在 WorkspaceStorage，供同 Session Runtime generation 恢复直接复用；不可变 continuation checkpoint 只在 AgentTurn terminal、进入 `waiting_user`、阶段结果和 graceful close 等语义边界 best-effort 生成。P0 不承诺周期/字节阈值或数值 RPO，forced host/storage failure 只展示 latest proven checkpoint age/digest/coverage warning。
- Inspiration 自动同步已确认方案 A：main merge webhook 提交幂等 exact-SHA sync request，后台编译 ResourceRecord/OpenViking projection，周期 reconciliation 按 repository head/last projected SHA 修复漏事件。由于使用闭环尚不明确，整个模块降为 P2 experimental；P0/P1 ContextPack 不依赖它，先用人工 manifest 试点验证追问减少、候选命中和实际读取率。
- 第一条可用产品切片已确认方案 A：先做 Worker tracer bullet。认证用户直接提交最小 TaskSpec，创建一个 Workspace/WorkerSession，在 Docker 中通过 ACP Codex + LiteLLM 完成真实修改/测试，WorkerDetail 经 SSE 展示 Trace 并交付 report/patch。Assistant clarify、Delivery、Evolution/Inspiration 和 Skill Market 后接；这是实现顺序，不是删除产品范围。
- Worker tracer bullet 的仓库与交付边界已确认方案 A：导入一个已授权 GitHub repository 的 immutable base commit，为 WorkerSession 创建隔离 WorkingCopy，只交付 report/patch/ChangeSet。用户 `apply` 只生成新的 Switchback WorkspaceRevision；首切片不执行 push、不创建远端 branch/PR，写能力留给后续受管 B2 阶段。
- Worker tracer bullet 的 TaskSpec Web 入口已确认方案 A：使用 Worker-first 表单选择 Workspace/repository/base commit，填写目标、验收标准和可选约束，展示人类可读预览后显式提交；结构化 API 复用同一 command/validator。用户不编辑 raw JSON，M1 不调用模型生成或补全 TaskSpec。
- Worker-first 表单的 readiness 已确认方案 A：创建前只校验必填字段、Workspace/Membership、repository/base/read grant、revision 和平台策略等确定性硬条件，失败返回字段 blocker 且不创建 Task。通过后正常创建；Worker 新发现语义冲突、不可验证验收或上下文缺口时结束当前 AgentTurn，使 Task 进入 `waiting_user` 并在 WorkerDetail 直接追问，回复后开启新的固定 24 小时 AgentTurn，不经过 Assistant。
- 首次 Workspace 创建交互已确认方案 A：Worker-first 表单内联“选择既有 Workspace / 从 GitHub 仓库创建”步骤；后者后台执行独立幂等的 CreateWorkspace + RepositoryImport，UI 原地显示进度。只有 import 完成、首个 WorkspaceRevision 可用且用户明确选择 exact workspace/revision 后，才预览并提交 TaskSpec；Task 创建事务不包含 Workspace 创建、网络 clone 或 import。
- RepositoryImport 失败收敛已确认方案 A：Workspace 状态使用 `provisioning -> ready | setup_failed`；失败保留逻辑 Workspace、脱敏错误和 append-only attempt，不产生 active WorkspaceRevision/Task/WorkerSession。用户可在同一 bootstrap identity 下显式重试或归档空 Workspace；成功时在一个事务中创建 RepositoryCheckout/首个 WorkspaceRevision、切换 active pointer、置 `ready` 并写 Outbox。见 [ADR 0056](../adr/0056-preserve-failed-workspace-bootstrap-for-retry.md)。
- Repository ref pinning 已确认方案 A：宿主 Repository adapter 在事务外把 branch/tag/commit 解析成服务端 RepositorySelectionSnapshot，绑定 actor/tenant/stable repository/requested ref/exact SHA/observed time/expiry/digest。用户确认后 CreateWorkspace 固化 SHA，Importer/retry/Task/dispatch 不再解析远端 ref；Snapshot 不授予权限，M1 重验本地 allowlist/read policy，后续 profile 重验 RepositoryAccessGrant。要最新版本必须显式刷新。见 [ADR 0057](../adr/0057-pin-repository-import-to-server-selection-snapshot.md)。
- M1 身份与 GitHub 凭证范围已按用户要求简化：采用 loopback-only SingleUserLocalProfile，服务端配置唯一 tenant/actor，客户端不可覆盖且无登录页；SSO/OIDC、多用户 session、动态 Membership/RBAC 后置。M1 只配置一个 repository，宿主 resolver/importer 从 env/mounted secret 使用 read-only credential，Worker 只拿 WorkingCopy、不拿 provider token、不执行远端 Git/`gh`。GitHub App、CredentialBroker、动态 RepositoryAccessGrant 和受管 B2 留到 tracer bullet 之后。见 [ADR 0058](../adr/0058-use-single-user-local-profile-for-m1.md)。
- M1 RuntimeSupervisor 拓扑已确认：Docker Compose 启动常驻 `switchback-backend` 与 PostgreSQL；RuntimeSupervisor 是 Backend 内 Go 模块，通过只挂给 Backend 的 Docker Socket/Engine API 创建 sibling WorkerSession 容器。Worker 容器无 Docker Socket、非 privileged，所有 mount source 由 Backend 从 Switchback data root 派生。该方案是 Docker-outside-of-Docker，不是嵌套 DinD；未来可保留 RuntimeProvisioner port 并拆为独立 Runner/Kubernetes adapter。见 [ADR 0059](../adr/0059-embed-m1-runtime-supervisor-in-switchback-backend.md)。
- M1 Worker 网络出口已确认方案 A：固定 `m1_public_web_restricted_v1`，允许 exact DNS/Backend、容器内 LiteLLM 与公网 TCP 80/443；拒绝 Worker 入站、host/Docker/metadata/private/link-local/CGNAT、其他 Worker 和基础设施。resolved IP 与 redirect 重验，IPv6 不能等价过滤时禁用；非 Web/private endpoint 使用 exact once/session ApprovalGrant。该策略不是 DLP，只适用于当前 SingleUserLocalProfile，生产版保留 egress proxy/domain/content control adapter。见 [ADR 0060](../adr/0060-allow-public-web-egress-and-deny-private-networks-in-m1.md)。
- M1 live-idle Runtime 已确认方案 A：固定 `m1_resident_until_session_terminal_v1`，WorkerSession 在 interactive/running/waiting_user/idle 期间保持同一 RuntimeInstance、Shim、Codex ACP 与 LiteLLM 运行；AgentTurn/WorkerRun 阶段完成不 stop/pause/hibernate、不增加 generation。驻留不授予执行权；只有 close、idle/absolute expiry、安全终止或 recovery fencing 才停止/替换。waiting_user 最长可驻留到 14 天 absolute boundary，持续占 continuity quota 与宿主资源；M1 不增加 suspended/resuming。见 [ADR 0061](../adr/0061-keep-m1-runtime-resident-until-session-terminal.md)。
- M1 Runtime 资源已确认方案 A：仅服务端 `local_standard_v1`，固定 2 vCPU、4 GiB memory、512 PIDs、10 GiB container writable layer；Task/Agent 不提交任意数值。WorkingCopy/cache/Agent state 使用独立持久目录。RuntimeCapacityAdmission 默认使用 Docker Engine 80% aggregate budget，continuity quota 10 不等于 10 个重负载 Turn；容量不足保持 resident Session/FIFO 并等待 `runtime_capacity`，不创建 Turn/deadline。CPU throttle 非 failure，明确 OOM/PID/storage limit 进入 `waiting_user:resource_exhausted`，不原样自动恢复。见 [ADR 0062](../adr/0062-use-one-fixed-m1-runtime-resource-profile.md)。
- 跨 Turn 后台进程已确认方案 A：普通 shell/Tool descendants 属于 AgentTurn process group/cgroup 并在 Turn terminal 清理，`&`/`nohup`/daemonize 不提升。只有 Worker managed-process API 启动的 preview server、dev server、file watcher 可成为 SessionProcess；它有 source/generation/handle/kind/cwd/ports/status/bounded logs/revision，在 WorkerDetail 可 stop/restart，继承同一 Runtime 权限与资源并持续进入 capacity accounting。Process output/health/status 不刷新 idle clock；Session terminal 全部停止，Runtime replacement 收敛为 stopped/lost 且不自动重启。见 [ADR 0063](../adr/0063-manage-cross-turn-background-processes-explicitly.md)。
- SessionProcess 的 AgentTurn terminal survival 已确认方案 B：managed start acceptance 先于 Turn terminal 提交后，`completed/cancelled/timed_out/failed` 均不得自动停止它，interrupt 作为控制动作收敛为 `cancelled:user_interrupt`；用户认为中间形成的 preview/watcher/service 仍可能可用。Start 与 terminal 通过 exact Turn revision、Runtime generation 和 owner commit order 收敛，terminal 先提交则拒绝 start。Turn cancel/deadline 仍停止 ACP、active Tool 与普通 descendants；Session terminal、安全终止、Runtime taint/replacement、resource hard limit 优先，replacement 不自动重启。见 [ADR 0064](../adr/0064-preserve-sessionprocess-across-agentturn-terminal-reasons.md)。
- SessionProcess live preview 已确认方案 A：用户显式 OpenPreview 后，Gateway 校验 actual actor、exact Session/process revision、runtime generation 和 declared port，签发短期 opaque access ref；Backend PreviewProxy 通过内部 Docker 网络直连 RuntimeSupervisor 解析出的当前容器端口，不发布 host/public port，也不接受任意 URL/IP/host/port/CONNECT。P0 只代理 HTTP/WebSocket，剥离 Switchback credentials/internal headers，bytes 不进入 EventCenter/SSE/Trace/ObjectStore；Process/Session/Runtime/撤权/expiry 关闭 tunnel。仅显式 OpenPreview 可刷新一次 idle，后续页面/HMR traffic 不续期。见 [ADR 0065](../adr/0065-proxy-sessionprocess-preview-through-backend.md)。

下一轮按“一次一个高杠杆问题”继续：

1. Preview route/origin 细节已按用户要求降级：M1 不建立 PreviewOriginPolicy，也不继续确认 browser/proxy 细节。ADR 0065 只保留 Backend proxy/no-host-port 方向；route 兼容性和 live preview 是否进入 tracer-bullet 验收均后移，不阻塞 report/patch 与 Worker 后续交互主链路。
