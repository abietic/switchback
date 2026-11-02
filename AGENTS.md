# AGENTS.md

本文档给未来参与 Switchback / Switchback 仓库工作的 Coding Agent 使用。它描述当前项目理解、工作边界和工程约束。若本文与真实源码冲突，以源码、测试、运行配置和当前任务要求为准。

## 默认协作方式

- 默认用中文回复用户，除非用户明确使用英文或要求英文。
- 先给结论和可执行方案，再给必要解释、取舍和风险。
- 遇到代码、配置、文档、自动化或本地文件任务，优先主动查看上下文并直接完成，不要只给建议。
- 不要破坏或回滚用户已有未提交改动。
- 对需求不清但风险较低的地方，基于合理假设继续推进，并在结果中标注假设。
- 对会影响架构、数据兼容、安全权限或生产行为的关键不确定点，先澄清或给出推荐默认方案。

## CodeGraph 与 Agent 导航

- 仓库根目录存在 `.codegraph/` 时，理解或定位代码优先使用 CodeGraph，再按需使用 `rg` 或直接读取文件。
- 查询符号源码、调用路径、动态分派和 blast radius 时，先运行 `make codegraph-status`，再使用 MCP `codegraph_explore`；MCP 不可用时使用 `codegraph explore "<问题>"`。
- 索引缺失、损坏或明显落后于工作树时，运行 `make codegraph-init`，不要手工修改 `.codegraph/codegraph.db`。
- 精确文本、非代码文件、生成物或 CodeGraph 未覆盖的内容继续使用 `rg` 和直接读取。
- CodeGraph 结果只用于缩小阅读范围；架构、行为和完成度结论必须回到当前源码、测试、运行配置和相关 active OpenSpec change 核验。
- 新任务先阅读 [Agent 导航](docs/agent-navigation.md)，按问题类型选择源码入口和验证门禁。

## 项目定位

Switchback（Switchback）是公司内部云端端到端需求交付平台，核心目标是把中台能力、Agent、Skill、MCP、知识演进、飞书入口和交付流程整合起来。

当前已知核心模块：

- Gateway：后端入口，处理前端/内部 API 请求。
- Assistant：面向用户交互的轻量 Agent，负责澄清、规划、派发，不做重型开发执行。
- Worker：接收任务并装载实际 Coding Agent、Skill、MCP，在隔离环境中执行重型任务。
- Evolution：消费 Assistant/Worker 轨迹，在 PostgreSQL 中治理候选/修订/审核，把 active Truth 投影到 OpenViking，并提供读取和用户 Memory 看板。
- Inspiration：P2 experimental，以独立代码仓库维护外部知识源 URL、摘要和意图辅助上下文；未验证使用闭环前不进入 P0/P1 关键路径。
- Delivery：作为飞书通道适配器触发任务，承载中间选择/对话并渲染最终结果卡片，不拥有执行逻辑。
- EventCenter：统一处理 Switchback 相关事件，把异步执行状态、消息和统计数据传回 Gateway/Frontier/飞书。
- LLMProxy：统一代理模型请求，包括 Assistant 请求和 Worker 中 Coding Agent 的模型流量；P0 基于 LiteLLM co-process，未来可替换但不实现 LLMGateway。
- Frontier / Frontend：Frontier 提供前端读模型和实时 SSE；Frontend 渲染会话、中间过程和最终产物，二者与执行逻辑解耦。
- Skilling：连接 Skill Market，管理用户、项目和 Worker 可用 Skills/MCP。

## 第一性原则

- Assistant 的价值是降低需求不确定性，生成可执行任务规格；Worker 的价值是执行重任务。不要把两者职责混在一起。
- 异步是默认形态。用户入口应快速返回，长任务进展通过事件回传。
- 轨迹是事实来源。Assistant Turns、Worker Trace、模型调用、工具调用和产物引用应可审计、可复盘。
- 长期记忆必须可控。Evolution 生成的 Truth 需要来源、范围、置信度、版本和过期策略。
- Skill/MCP/模型/容器都是能力边界，必须显式权限、配置和审计。
- 文档是辅助，源码和运行证据优先。实现前先读相关代码、测试和配置。

## 工程技术栈假设

当前描述中的主要技术栈：

- Go / Golang 作为主后端语言。
- DDD 风格 monorepo。
- 关系数据库用于核心实体和状态。
- 消息队列用于异步命令、事件和重试。
- PostgreSQL 作为 P0 核心状态、Outbox、投影和灵活 JSONB 元数据存储；MongoDB 暂不进入 P0。
- 对象存储用于 JSONL 轨迹、日志片段和产物。
- P0 使用本地文件系统 ObjectStore adapter；P1 再对接公司对象存储或仍在维护的 S3-compatible endpoint。不要默认使用已归档的 MinIO。
- OpenViking 是已发布 Truth / Resource 的强依赖检索投影，但不是候选、修订、审批和审计的权威事实源；领域层仍通过端口访问。
- LiteLLM 是 P0 模型代理实现；LLMGateway 仅作为未来可替换基础设施，不进入 P0。
- P0 使用 PostgreSQL transactional outbox + polling dispatcher；P1 再增加 RocketMQ 5 integration profile，使用官方 Go 5.x client、FIFO message group、重试和 DLQ。
- ACP 协议用于 Worker 装载主流 Coding Agent。

如果仓库实际结构与上述不一致，优先跟随真实仓库。

## DDD 与模块边界建议

实现时优先保持清晰边界：

- `domain`：领域实体、值对象、领域服务、状态机、领域事件，不依赖基础设施。
- `application`：用例编排、命令处理、事务边界、权限检查、调用领域服务。
- `adapter` / `interfaces`：HTTP、RPC、飞书回调、MQ handler、ACP adapter、MCP adapter。
- `infrastructure`：数据库、对象存储、MQ、OpenViking、LiteLLM/LLMGateway、容器运行时。
- `pkg` / `shared`：只放稳定通用能力，避免把业务逻辑塞入公共包。

依赖方向应尽量保持：

```text
interfaces/adapter -> application -> domain
infrastructure -> application/domain ports
domain 不依赖 application、infrastructure、adapter
```

## 核心协议优先级

在代码和设计中优先稳定以下协议：

1. Task Spec：Assistant / Delivery 派发给 Worker 的结构化任务。
2. Lightweight Task：Task revision、TaskTransition、acceptance_mode、WorkerRun/result refs、同事务 Outbox 和 scheduled-command fencing。
3. Workspace/ChangeSet：WorkspaceRevision、WorkingCopy、ChangeSet、串行 Apply 和冲突语义。
4. WorkerControl/Recovery：RuntimeControlEnvelope、RuntimeHeartbeat、ChannelEpoch、RuntimeRecoveryIncident 和恢复幂等语义。
5. Event Schema：模块间异步通信和前端/飞书回传的统一事件。
6. Frontier View/Display Protocol：ProjectionSnapshot、ProjectionDelta、ProjectionControlEvent、DisplayCheckpoint、TransientDisplayDelta、ViewCursor 和 replay/resync 语义。
7. Trace JSONL / Manifest / Analytics：Assistant Turns 和 Worker 执行轨迹、segment 生命周期，以及排障/Evolution/效果看板的增量统计契约。
8. Governed Context Schema：Evolution 管理的 ContextCandidate、MemoryTruth、InstructionPolicy、ContentRevision、审核和 OpenVikingProjection。
9. Skill/MCP Manifest：可加载能力、权限、版本和依赖声明。
10. LLMProxy Request Metadata：模型路由、成本统计、审计、trace 关联。

## Assistant 开发约束

- Assistant 负责澄清、规划、总结、派发，不直接运行长时间开发任务。
- Conversation 可以在 Workspace 未解析时进行 tenant/user-scope 的脑暴和澄清；在加载 Workspace-scoped Truth/Inspiration/Repository/Trace/Artifact、提交 TaskSpec 或创建/复用 WorkerSession 前，必须取得显式且当前有效的 WorkspaceContextResolution。
- 首次 `unresolved -> Workspace` 可在原 Conversation 内完成；一旦 Conversation 已有 resolved Workspace 历史，`W1 -> W2`、`W1 -> cleared` 或 W1 Membership/Policy 撤销必须执行 Interaction-owned `ConversationRollover`，在同一飞书 topic 下创建 successor Conversation，并把 predecessor 标记为 `superseded` 只读。切回 W1 也创建新 generation，不得复活旧 Conversation。
- ConversationRollover 必须在一个事务中重验 binding/source revision 与幂等键，创建 target Conversation/WorkspaceContextResolution/ConversationHandoff，推进 ChannelThreadBinding、冻结 source 并写 Outbox；事务内禁止模型、对象存储、provider 或远程上下文调用。
- 默认 ConversationHandoff 只允许结构化 Workspace-neutral goal/constraint/assumption/blocker，不复制 raw Turns、旧 summary、Workspace Truth、Repository、Task/Worker/Trace/Artifact refs 或 Tool output。受保护跨 Workspace carry-over 是独立显式动作，逐 source 重验 W1 read、W2 target-context permission、classification 和 intent evidence；模型 summary 不授予携带权限。
- Assistant 负责用类似 brainstorming/clarify 的交互循环判断需求是否足够明确；TaskSpec validator 只做结构、权限和策略硬校验。
- Assistant 必须按版本化 TaskReadinessPolicy 生成 TaskReadinessAssessment，对 goal、deliverable/acceptance、scope/target、constraints、context/access、risk/approval 和 execution policy 标记 `known`、`assumed`、`blocking_unknown` 或 `not_applicable`。只有 validator 通过且不存在 `blocking_unknown` 时才能派发。
- 低风险、可逆细节可以使用策略允许且写入 TaskSpec 的显式 assumptions；权限、冲突目标、生产/外部/不可逆副作用、关键访问缺失和不可验证目标不能靠默认值绕过。Assistant 每次只问一个当前最高优先级阻塞问题；连续三次澄清问题后必须产生 ClarificationCheckpoint。用户选择继续后重置下一组三问预算，不形成整个会话硬上限。
- ClarificationCheckpoint 只有在无硬阻塞且 validator 通过时才允许向 TaskDraft initiator 展示绑定 exact revision 的“接受假设并创建”；该动作同时生成新 assessment 与 creation intent。否则只允许继续澄清、缩小范围或取消。用户要求“直接做”也不能豁免权限和硬阻塞。
- Prompt 构建需要有预算意识，不能无限加载历史、Truth、文档和 Skill。
- Assistant 输出给 Worker 的 Task Spec 必须包含目标、上下文、约束、验收标准、审批/副作用策略、可用能力和运行配置；不得携带用户可选的执行时长或预算字段。
- TaskSpec 还必须携带与 exact TaskDraft/spec digest/readiness/Workspace/actor 一致的 active `creation_intent_ref`。Ready assessment 不能单独授权创建；private/API 明确请求可按 TaskCreationTriggerPolicy 生成 intent，澄清导致实质变更后需确认最终 ready draft。
- 派发时 TaskSpec `workspace_id` 必填，并绑定 workspace-resolution 与 Membership evidence。Task Orchestration 不得为缺失 workspace 的 TaskSpec 静默推断或创建 Workspace；新 Workspace 必须先经独立、幂等、显式的 CreateWorkspace/选择动作。
- 对话 Turns 应在阶段性完成或窗口达到阈值后写入对象存储 JSONL；归档必须记录 Conversation generation、Workspace scope snapshot、resolution revision 和 rollover lineage。ContextCompiler、搜索/导出与 Evolution 不得仅因同一 provider topic/lineage 合并不同 Workspace generation 的受保护内容。
- Assistant 使用 Truth 时，应能追踪 Truth 来源，避免把不可靠记忆当事实。

## Task Orchestration 开发约束

- P0 采用“轻量 Task + WorkerSession 主交互面”。Task 是交付跟踪记录，不是第二个重型工作流产品；其最小字段包括不可变 TaskSpec snapshot、TaskInitiator、Workspace、可选 requested/assigned WorkerSession、current WorkerRun、result refs、`acceptance_mode`、status 和 revision。
- Task 状态固定为 `queued`、`running`、`waiting_user`、`delivered`、`completed`、`failed`、`cancelled`。不要增加 drafted/validated/starting/succeeded/timed_out 等与 TaskDraft 或 WorkerRun 重叠的状态。
- `acceptance_mode=auto|manual`。report-only 且验收证据可确定时可自动完成；patch 默认 manual。manual Task 的阶段结果进入 `delivered`，initiator 可 accept 或通过 Worker 反馈要求 rework，后者创建新 WorkerRun 并回到 `running`。
- P0 不建立 TaskSaga、TaskAttempt、FollowUpTaskProposal 或 TaskActionGrant 公开聚合/协议。Task 状态是唯一业务权威；实现内部可以复用 state-machine helper，但不能形成第二套 revision/terminality。
- Task Orchestration 接受 TaskSpec 时必须重验显式 `workspace_id`、WorkspaceContextResolution revision、Tenant/WorkspaceMembership、creation intent 和策略，并写入不可变 snapshot；缺失、stale、revoked 或跨 Tenant 时不得创建 Task/Transition/Outbox。
- Task 创建、状态推进、TaskTransition 和 owner-stream Outbox 必须在一个 PostgreSQL 事务内按 expected revision/idempotency key 收敛。事务内禁止网络、容器、模型、文件复制和外部 provider 调用。
- retry、deadline、reminder、reconciliation 可使用通用 ScheduledCommand。Poller 使用 `FOR UPDATE SKIP LOCKED`、短 lease、单调 fencing token 和 expected Task/Run revision，只提交幂等应用命令；它不直接执行副作用，也不形成新的领域聚合。
- Task 非终态或 `delivered` 时，对该交付的直接 Worker 反馈保持同一 Task 并创建/加入新 WorkerRun。RuntimeInstance 崩溃恢复仍属于原 WorkerRun，只递增 generation，不能伪装成返工或重试。
- Task 终态后的直接 AgentTurn 形成 standalone WorkerRun，可带 `origin_task_id`，不得改写旧 Task。新的可跟踪交付只接受认证用户的明确创建指令或显式 TaskSpec；Agent 可以建议，但模型推断、生成文本和范围扩大不能作为授权。
- 复用既有 WorkerSession 必须有认证用户对 exact `worker_session_id` 的显式 WorkerSessionTargetIntent；Workspace 相同、最近活动、唯一 idle Session、related ref 或模型推断都不能触发复用。没有精确目标的新 Task 默认在同一授权 Workspace 分配新 WorkerSession；busy session 不被隐式 interrupt，expired session 不能被复活。Workspace 访问撤销时 fail closed。
- 显式复用默认 `inherit_current`：继承目标 Session 当前 WorkingCopy、持久 Agent state/context、AgentSession 和未提交变更，禁止静默 reset/clean/stash/checkout。要求 clean/exact base 时必须新建 Session 或阻塞等待显式动作，不得原地清理被复用 Session。
- `closed/expired` WorkerSession 是不可重开的终态 identity，只提供权限过滤后的只读 Timeline/Trace/Artifact/checkpoint。继续工作必须由真实用户显式 `CreateWorkerSessionContinuation`，绑定 source Session revision 与 exact durable checkpoint，创建新的 WorkerSession/WorkingCopy/AgentSession，并记录 `continued_from_worker_session_id` 与 `continued_from_checkpoint_ref`。
- P0 必须区分 LiveWorkerState 与 WorkerSessionContinuationCheckpoint：非终态 WorkingCopy、Agent state、Trace/control cursor 持续保存在 WorkspaceStorage，用于同 Session Runtime generation 恢复；不可变 continuation checkpoint 只在 AgentTurn terminal、进入 `waiting_user`、阶段结果和 graceful close 等语义边界 best-effort 生成。P0 不提供周期/字节阈值或数值 RPO；强制 host/storage failure 只展示 latest proven checkpoint 的 capture time、digest 和 coverage warning。
- WorkerSessionContinuation 必须重新校验 Tenant/WorkspaceMembership、policy、checkpoint readability 和 continuity quota。新 Session 不继承旧 Runtime generation、ChannelEpoch、dispatch permit、queue position、ApprovalGrant、RepositoryAccessGrant、SecretLease 或其他 Session-scoped authority；允许带入的 ContextPack/Artifact refs 必须显式选择并重新鉴权。
- 每个 WorkerRun 在首次 Agent prompt、Tool 调用或 WorkingCopy 写入前必须持久化 WorkerRunStartSnapshot，记录 resolved context policy、AgentSession refs、WorkingCopy checkpoint/manifest digest、base WorkspaceRevision、dirty state、前序 Task/Run refs 和服务端时间。
- P0 以 WorkerSession 串行化顶层执行：同一时刻最多一个 `bound_task_id` 和一个 `active_worker_run_id`。Task 可在 `queued` 时引用目标 Session，但只有 Worker Runtime 幂等接受 StartWorkerRun 后才取得 execution slot；冲突返回 `session_busy`，由 Task Orchestration 保持排队或显式分配另一 Session。
- `running/waiting_user/delivered` Task 通常保持 Session binding，直到 `completed/failed/cancelled` 才释放；唯一 P0 非终态例外是 WorkerSession 14 天 absolute expiry。此时旧 Run 终态、current binding 清空并写入 TaskSessionLineage；manual delivered 结果仍可接受，返工必须显式派生新 Session/Run。standalone WorkerRun 只能在无 bound Task 和无 active Run 时启动。
- WorkerSession absolute expiry 必须先 fencing 新命令/lease 并证明旧 Runtime 已失去 WorkingCopy 写权限；无法证明时 Session 保持 `lost` 且继续占 quota，禁止创建可写 continuation。确认后 active Run 以 `timed_out:worker_session_absolute_expired` 终止，Session `expired`，晚到结果只写 Trace。
- WorkerSessionExpiryOutcome 必须携带 exact Session/Run revisions、latest proven checkpoint、Artifact 完整性、cancel/fencing 证据、外部副作用不确定性和 bound Task ref。Task Orchestration 幂等消费：queued/running/waiting_user 进入或保持 `waiting_user:worker_session_expired`，delivered 保持 delivered，terminal Task no-op。
- Task 维护一个可空 current `worker_session_id` 与有序 `worker_session_lineage`。expiry 清空 current Session/Run 并追加 lineage；只有 TaskInitiator 的 `ContinueTaskInNewWorker` 可把 waiting_user/delivered Task 转回 queued、提交 Task-owned continuation allocation request，并在新 Session 正常 dispatch 后创建新 WorkerRun。Workspace owner 仍只有 emergency cancel。
- `ContinueTaskInNewWorker` 必须携带真实 TaskInitiator、exact Task revision、expiry outcome、durable checkpoint 和 idempotency key；P0 没有续作预算、Task 累计时长或时间授权。新 Session/Run 仍重新校验当前权限、配额、checkpoint 与 14 天 Session 生命周期。
- 外部 non-idempotent/unknown effect 未核查时不得 continuation/retry。旧 partial report/patch 保留 source Run/checkpoint provenance，不得单独触发 auto acceptance；delivered 前已提交的完整结果不因 Session expiry 降级。
- 只有已分配 WorkerSession 的 Task 才进入该 SessionTaskQueue 并获得 `session_queue_sequence`。未携带 exact Session target intent 的新 Task 保持 queued，并先分配新 Session；不得进入任意既有 Session 队列。
- P0 WorkerSessionContinuityQuota 按 `(tenant_id, user_id)` 计数，默认上限 10。`allocating/interactive/running/recovering/waiting_user/idle` 各占一个槽位；`lost` 在 reconciliation 完成前继续占用；只有 `closed/expired` 完成终态与 quota release 提交后不再计数。queued/unassigned Task 与 pending WorkerSessionContinuation 不占槽位。
- 配额满时，合法新 Session 请求按 Tenant/User 单调 `session_allocation_sequence` 进入 SessionAllocationQueue。Task 保持 `queued`、`worker_session_id=null`、`allocation_wait_reason=session_capacity`；WorkerSessionContinuation 保持 pending allocation。不得自动复用、reset、close 或 evict 既有 Session；用户可关闭可终止 Session、显式复用 exact Session 或取消请求。
- SessionAllocationQueue 与 SessionTaskQueue 是两个协议。前者在分配 Session 前按 Tenant/User FIFO 等容量，后者在分配后按 Session FIFO 等 execution slot。容量释放事件丢失必须由 reconciliation 恢复，且一个合法 allocation head 不得被后到请求越过。
- Worker Runtime 的 WorkerSessionAllocator 是 SessionAllocationQueue sequence/FIFO 与 continuity quota 的单 owner。Task Orchestration 只提交 Task-owned allocation request，Continuation application 只提交 source/checkpoint-bound request；二者不能各自分配序号或绕过统一队头。
- Worker Runtime 必须在同一事务中按 expected quota revision/idempotency key 预留连续性槽位并创建 `allocating` WorkerSession；并发分配不能超过上限，响应丢失返回原 Session identity。Session 终态与 quota release 同事务，容器/存储清理可异步。
- SessionTaskQueue 按 WorkerSession 独立使用服务端单调 `session_queue_sequence` 做稳定 FIFO。重复 enqueue 返回原序号；P0 只允许取消、显式把 queued Task 改派到另一 exact Session 尾部，或改派到新建 Session，不实现 priority、reorder 或 preemption，Task 展示 priority 不影响执行顺序。
- 所有新顶层 Run 都需要短期单次 SessionDispatchPermit。Task 只为当前 queue head 取得 permit；standalone prompt 仅在队列为空时取得 permit，因此不能抢占更早 queued Task。slot-release event 丢失必须由 reconciliation 恢复。
- Task 创建时固化 TaskInitiator。P0 只有 initiator 可继续交互、accept、rework、cancel 和 reassign queued session；current Workspace owner 只有 emergency cancel。可见性、Conversation controller、Workspace editor/reviewer、群披露和 Artifact review 都不产生这些权限。
- accept/rework/cancel/reassign-queued-session command 必须携带真实 actor、exact target、expected Task revision 和 idempotency key。ApprovalGrant 只授权受保护 Tool/外部操作；P1 若增加 TaskActionGrant，也不得将可见性误作权限。
- `waiting_user` 的 TaskTransition 同事务记录 reason 和允许动作。48 小时 WorkerSession idle clock 按既有规则暂停，但当前 AgentTurn 的固定 24 小时 hard deadline 与 WorkerSession 14 天 absolute lifetime 均不暂停。
- 外部副作用效果未知时安排 reconciliation 或进入 `waiting_user`，禁止 ScheduledCommand 盲重试。补偿是显式、可审计且幂等的业务动作，不是数据库回滚。
- ADR 0048 是 P0 Task 产品与领域基线，ADR 0049 定义单 WorkerSession 顶层执行串行化，ADR 0050 定义显式复用与启动基线，ADR 0051 定义连续性配额与容量等待，ADR 0052 定义终态不可复活与派生续作，ADR 0053 定义 absolute expiry 下 Task identity/Session lineage/新 Run 收敛，ADR 0055 定义固定 24 小时 AgentTurn hard deadline，ADR 0056 定义失败 Workspace bootstrap 的保留/重试边界，ADR 0057 定义 repository ref 到 exact SHA 的服务端选择快照，ADR 0058 定义 M1 本地单用户范围，ADR 0059 定义 M1 RuntimeSupervisor 与 Docker 部署拓扑，ADR 0060 保留旧公网 Web/私网拒绝策略的历史背景，ADR 0061 定义 M1 live WorkerSession 的 Runtime 驻留策略，ADR 0062 定义 M1 单 Runtime 资源上限与 aggregate admission，ADR 0063 定义跨 Turn 后台进程只能成为显式受管 SessionProcess，ADR 0064 定义 accepted SessionProcess 跨所有 AgentTurn terminal reason 存活，ADR 0065 定义通过 Backend PreviewProxy 访问声明端口且不发布 host port，ADR 0066 为 SingleUserLocalProfile 启用受信任完整公网/私网直通并取代 ADR 0060 的现行决策；ADR 0020/0054 已被取代，ADRs 0031/0032/0033/0034/0047 仅保留历史、底层可靠性机制或 P1 备选，不得覆盖本节。

## Worker 开发约束

- Worker 负责执行重任务，必须考虑隔离、权限、资源、超时、取消、重试和清理。
- M1 tracer bullet 使用 SingleUserLocalProfile：服务端配置固定 tenant/actor、客户端不得覆盖、Frontend 无登录页且默认仅监听 loopback；SSO/OIDC、多用户 session、动态 Membership/RBAC 延后。M1 仅由宿主 Repository resolver/Importer 使用 env/mounted read-only GitHub credential，Worker 容器不接收 provider token、不执行远端 Git/`gh`；完整 RepositoryAccessGrant/CredentialBroker/受管 B2 保留为后续能力。见 ADR 0058。
- P0 Worker 必须在本地 Docker-compatible 容器中运行 Codex 作为第一个真实 Coding Agent，并通过网络请求与后端通信，不能影响宿主机；P0 固定 `github.com/coder/acp-go-sdk@v0.13.5`、`@agentclientprotocol/codex-acp@1.1.2` 和 lockfile 中的 `@openai/codex@0.144.1`。
- Worker 不应绑定单一 Coding Agent；P0 先实现 ACP-controlled CodexAdapter，后续通过 AgentProfile/AgentAdapter 接入 ACP Agent 或其他主流 Coding Agent。
- 容器运行时按传统 CI/CD runner 模式设计。M1 RuntimeSupervisor 是 Docker Compose 管理的 `switchback-backend` 容器内 Go 模块，通过只挂载给 Backend 的 Docker Socket/Engine API 编排 sibling WorkerSession 容器，挂载该 Session 的隔离 WorkingCopy/cache/agent state，注入 env/secret/capability，采集日志/产物/退出码并负责回收；它不是独立宿主进程。Worker 容器不得挂载 Docker Socket、不得 privileged，也不得接受客户端提供的任意宿主路径；所有 mount source 必须由 Backend 从服务端配置的 Switchback data root 派生。见 ADR 0059。
- M1 Worker 网络固定使用服务端拥有的 `trusted_full_network_v1`：允许公网、私网、VPN、LAN、其他可路由容器和 Linux 宿主开发服务的任意出站协议/端口，宿主入口通过 `host.docker.internal=host-gateway` 注入；不做 destination/CIDR 过滤、redirect 重验、payload inspection 或 per-target ApprovalGrant。它只适用于 loopback-only SingleUserLocalProfile，不是 DLP。Worker 仍不得挂载 Docker Socket、使用 privileged/host network、接受任意宿主路径、发布 host/public 入站端口或继承 ambient infrastructure credential；PreviewProxy 仍是唯一受管入站。TaskSpec、RuntimeInstance、WorkerRunStartSnapshot、Trace 和 WorkerDetail 必须携带 exact profile id/revision/digest/status；旧 profile 只读为 `legacy_unenforced`。见 ADR 0065/0066。
- M1 RuntimeResidencyPolicy 固定为 `m1_resident_until_session_terminal_v1`：首次分配后，`interactive/running/waiting_user/idle` WorkerSession 始终保持同一 RuntimeInstance、Shim、Codex ACP 和 LiteLLM 运行；阶段结果、Turn terminal、waiting_user 和普通 idle 不 stop/pause/hibernate，也不递增 generation。没有 active Turn/dispatch permit 时 Agent 不得自行执行。只有 explicit close、idle/absolute expiry、安全终止或故障 fencing 才停止或替换；M1 不增加 suspended/resuming 状态。见 ADR 0061。
- M1 RuntimeResourceProfile 固定为服务端 `local_standard_v1`：2 vCPU、4 GiB memory、512 PIDs、10 GiB container writable layer；Task/Agent 不能提供任意数值。RuntimeCapacityAdmission 默认使用 Docker Engine 可用 CPU/memory/storage 的 80% aggregate budget，容量不足时不签发 SessionDispatchPermit、不启动 AgentTurn/deadline，保持 resident Session 并按原 FIFO 等待 `runtime_capacity`。CPU throttle 非故障；明确 OOM/PID/storage limit 进入 `waiting_user:resource_exhausted`，不作为普通 runtime loss 自动重建。见 ADR 0062。
- 普通 shell/Tool 子进程属于当前 AgentTurn 的 process group/cgroup，Turn terminal 时必须停止；`&`、`nohup` 或 daemonize 不会改变归属。只有通过 Worker managed-process API 启动并登记的 preview server、dev server 或 watcher 才成为可跨 Turn 的 SessionProcess。Managed start acceptance 先于 Turn terminal 提交后，`completed/cancelled/timed_out/failed` 均不得自动停止它，interrupt 以 `cancelled:user_interrupt` 表达；Turn terminal 先提交则拒绝 start。它继承同一 Runtime 资源、网络、WorkingCopy 和权限，不得启动 ACP prompt 或自主 Agent 工作；WorkerDetail 展示状态/端口/日志和 stop/restart。Process output/health/status 不刷新 Session idle clock。Session terminal、安全终止、Runtime taint/replacement 或 resource hard limit 仍停止进程，replacement 后标记 stopped/lost 且不自动重启。见 ADR 0063/0064。
- SessionProcess live preview 只能通过 Gateway `OpenSessionProcessPreview` 与 Backend PreviewProxy。命令校验 actual actor、exact Session/process revision、runtime generation 和 declared port，RuntimeSupervisor 解析内部 endpoint；客户端不得指定任意 proxy target，Worker 不发布 host port。P0 只代理 HTTP/WebSocket，剥离 Switchback 凭证/内部 headers，bytes 不进入 EventCenter/SSE/Trace/ObjectStore。Process/Session terminal、Runtime replacement、撤权或 access expiry 必须关闭 tunnel；只有显式 OpenPreview 可刷新一次 idle，后续页面/HMR 流量不续期。M1 route/origin 是 loopback-only 实现细节，不建立 PreviewOriginPolicy；非本机或多用户 profile 前再评审。见 ADR 0065。
- P0 WorkerSession 镜像通过 Docker 构建，认证通过环境变量或挂载配置注入；同一容器内运行 Worker runtime shim、`codex-acp`、Codex 和 LiteLLM，Codex 模型请求指向本地 LiteLLM。
- 容器内 Go Worker runtime shim 使用 `github.com/coder/acp-go-sdk` 作为 ACP client，通过 stdio 控制 `agentclientprotocol/codex-acp`；shim 通过网络与 Switchback 后端通信。不要把 ACP 本地控制通道与后端网络通道混为一谈。
- `internal/workerruntime/adapter/acpclient` 集中 ACP 协议/会话、原始 Trace、受限文件回调与进程组机制；`codexacp` 只保留固定启动、gateway 认证与 Codex 专属 update 判定门面。Shim 仍调用 `codexacp.New(trace)`；构造策略仅由受信任 Go 代码提供，Factory/profile/YHC 接入仍须独立评审，不接受 Task 提供的 command/env/path。
- Shim 与后端的 WorkerControlChannel 必须保持传输无关：P0 优先使用由 Shim 主动发起的 gRPC 双向流，网络/代理或 HTTP/2 不兼容时回退 HTTPS long-poll command + batched event POST；Trace/Artifact 大文件始终走可恢复 HTTP 上传。
- gRPC 与 fallback 共用 command/event id、stream sequence、ACK、runtime generation、channel epoch 和幂等语义。同一 RuntimeInstance generation 同时只能有一个 active transport；旧 epoch 必须被 fencing。鉴权失败、协议版本不兼容和策略拒绝不能触发 transport fallback。
- Worker 通过 ACP 控制 Codex，不裸启动 Codex 并解析终端输出；Worker 控制逻辑负责 session state、JSONL、Trace/Artifact 上传和 EventCenter 回传。
- ACP 初始化必须记录协议版本和 capability snapshot，未声明的可选能力视为不支持。入站/出站 ACP JSONL 必须在 typed SDK 解码前 tee 到有界原始帧记录器；未知可选 update 保留为 Trace 并告警，不得因单个未知进度事件杀死健康 runtime。协议版本、权限或安全语义无法确定时必须 fail closed。
- WorkerSession 是用户级交互执行会话，idle timeout 固定为 48 小时，P0 绝对最大存活时间为 14 天，Tenant/User 级连续性配额默认上限为 10；超时前默认恢复旧 session。每个 WorkerSession 同一时刻最多有一个 active RuntimeInstance/container；崩溃重建复用 `worker_session_id`，递增 runtime generation 并产生新 container id；与 Codex/Agent session id 可一对多。
- 只有仍在有效生命周期内的 WorkerSession 可按 exact target 恢复或进行 Runtime generation 恢复。`closed/expired` 永不复活；从其 durable checkpoint 继续会创建新 identity、新 AgentSession、新生命周期和新授权链路。
- 每个 WorkerSession 同一时刻最多执行一个顶层 WorkerRun。AgentTurn 的 queue/interrupt/steer 只作用于该 Run；Agent 内部 subagent/child span 可以并发但共享父 AgentTurn deadline、权限、WorkingCopy 和 Artifact lineage，不得伪装成第二个顶层 Run。
- P0 Supervisor 对每个连续 RuntimeRecoveryIncident 最多自动重建一次 RuntimeInstance。恢复必须复用 WorkerSession、Workspace、WorkingCopy、已持久化 Agent state 和 Trace cursor，递增 runtime generation 并 fencing 旧 generation/epoch；新 runtime 通过完整健康检查且满足稳定窗口或完成持久进度 checkpoint 后才关闭 incident。自动恢复再次失败或 ACP session 无法安全 resume 时进入 `waiting_user`，不得 crash loop 或静默创建全新 AgentSession。
- 自动恢复只适用于进程退出、容器丢失、心跳超时或 Agent 无响应等 runtime failure。用户取消、48 小时 idle、14 天 absolute timeout、鉴权/授权、策略、schema/protocol 错误不得触发自动重建。有效的 WorkerSession ApprovalGrant 可继续匹配，但 SecretLease 必须为新 generation 重新签发。
- 14 天 absolute timeout 不是 runtime recovery：完成 fencing 后旧 WorkerRun 必须 timed_out、Session 必须 expired。Task 是否继续由 ADR 0053 的 initiator action 决定，不得静默重建 Runtime/AgentSession 或迁移旧 Run。
- P0 RuntimeHeartbeat 默认每 15 秒发送；后端连续 45 秒未收到有效心跳时标记 `suspect`、暂停向该 runtime 派发新命令并主动探测 transport/container/process，90 秒仍未恢复时标记 `unhealthy` 并进入恢复流程。明确 process exit/container missing 可立即判定 unhealthy，不必等待 90 秒。timeout 以服务端接收时间和 heartbeat sequence 为准，不能信任容器 wall clock。
- RuntimeHeartbeat 必须由 Shim 独立发送，长时间模型推理、tool call 或无 token 输出不能阻塞心跳。固定 ACP/Codex 栈没有安全 ping/no-op；Agent unresponsive 只能由 process exit、stdio EOF、ACP connection close 等硬证据，或 cancel 后超出 grace 仍无法得到 prompt 终态触发，不能仅凭“没有模型输出”。
- `active_quiet` 只产生非终止告警，不因无输出提前取消；当前 AgentTurn 仍受固定 24 小时 wall-clock hard deadline 约束。
- 每个顶层 AgentTurn 在后端 Worker Runtime owner 事务接受 `StartAgentTurn` dispatch 时记录服务端 `started_at/deadline_at`，固定 `deadline_at=started_at+86400s`。Shim/ACP 实际调用时间只作为 Trace 观测；审批等待、`active_quiet`、断线和 `recovering_runtime` 均不暂停，也不存在延期、Grant 或用户选择时长。
- AgentTurn deadline 使用双重 enforcement：后端 ScheduledCommand 是状态、审计和 fencing 的唯一权威；Shim 为同一 Turn revision/runtime generation 启动不可重置的本地 monotonic watchdog，在控制通道断开时仍能触发同一幂等取消。重连后以后端终态证据收敛，Shim 不得延长或独立改写 deadline；ACP prompt 在两侧 enforcement 均 armed 前不得开始。
- AgentTurn 启动采用后端预提交后单向 dispatch，不增加 Ready/Permit 往返：后端在一个事务中写入 `started_at/deadline_at/ScheduledCommand` 与 `StartAgentTurn` Outbox；命令携带 exact Turn revision、Runtime generation、ChannelEpoch、deadline 和 cancellation identity。Shim 校验 freshness 后，按后端 deadline 与控制通道 server-time uncertainty 计算保守剩余时长，先 arm monotonic watchdog 再调用 ACP；stale/expired/时间无法证明时 fail closed，重投不得重置 watchdog 或重复 prompt。运输延迟和 uncertainty margin 只缩短可用窗口。
- `steer` 只修改当前 Turn 且不重置 deadline；`interrupt` 终结当前 Turn；显式 queued user message 只能在当前 Turn 终态后启动新的独立 24 小时 Turn。Agent 输出、自动重试和 Runtime recovery 不得自行创建顶层 Turn 绕过限制。
- deadline 到期必须请求 ACP cancel 并取消当前 Tool/process，AgentTurn 不可逆进入 `timed_out`。取消确认后 Task-bound WorkerRun/Task 可进入 `waiting_user:agent_turn_timed_out` 并等待用户发起新 Turn；取消无法确认时沿用 tainted runtime、fencing 和外部副作用 reconciliation 路径。
- WorkerRun、Task 和 continuation 不维护时间预算、累计时长或时间授权。TaskSpec 不携带时长字段，Tool 默认继承当前 AgentTurn 剩余 deadline；网络、模型请求和进程退出可有基础设施超时，但不是领域预算或用户 Grant。
- 时间限制不能替代模型成本治理。LLMProxy 可独立执行 tenant/user/model 级 rate、token 和 cost 限制，但不得把它表达为 AgentTurn 延期。
- AgentTurn deadline 或用户取消触发 Tool 停止时必须按副作用和取消证据分级。本地 process-group/cgroup 无法证明停止时标记 TaintedRuntime，停止新命令并以 `tool_cancel_unconfirmed_local` 打开一次 RuntimeRecoveryIncident。远端 read-only 或有稳定幂等/核查证据的 idempotent Tool 可 fencing invocation 后继续；non-idempotent 或 `unknown` 外部副作用不确定时禁止自动重试并进入 `waiting_user`，提供核查、确认已成功、补偿或重试动作。
- Tool invocation 终态必须按 `tool_invocation_id` fencing；终态后的 success/error/progress 统一作为 `late_tool_result` Trace，不得改写 Tool、AgentTurn 或 WorkerRun 状态。
- SessionProcess 必须有稳定 identity、source Turn/Tool、runtime generation、process handle、kind、command summary、cwd、declared ports、status、bounded log ref 和 revision；禁止在 Turn terminal 后把未知 orphan process 追认为 SessionProcess。其资源占用必须持续进入 aggregate capacity accounting，不能因 Turn admission release 而消失。
- Tool/Skill/MCP manifest 只提供版本化 ToolCapabilityDeclaration；Adapter 负责验证和标准化技术证据，只能提高风险，不能自行降低声明风险。Capability/Skilling 的版本化 ToolGovernancePolicy 生成最终 ResolvedToolPolicy，TaskSpec/Approval 只能继续收紧，Worker 负责校验快照并执行，Agent/模型文本不得改变裁决。
- Tool 元数据缺失、声明冲突、Adapter 无法验证或 policy revision 不可用时必须 fail closed：effect class 解析为 `unknown`，不可验证 cancellation 解析为 `unsupported`，禁止自动重试；任何可信运行时证据仍可把单次 invocation 升级到更高风险。
- 自动创建 replacement runtime 前，Control Plane 必须终止或隔离旧容器、撤销旧 generation 的 control/secret lease，并取得新的独占 WorkingCopy mount lease；无法证明旧 runtime 已失去写权限时进入 `waiting_user`，禁止两个 generation 并发写同一 WorkingCopy。
- 每个 WorkerSession 绑定一个持久逻辑 Workspace，一个 Workspace 可承载多个 WorkerSession 和零到多个 RepositoryCheckout；Workspace 可以服务无仓调研/设计或多仓修改，并与可回收的 RuntimeInstance identity 分离。
- M1 repository-backed Workspace bootstrap 使用 `provisioning -> ready | setup_failed`。RepositoryImport 网络操作不进入 CreateWorkspace/Task 事务；失败保留 Workspace、脱敏错误和 append-only attempts，active revision 为空且不得创建 Task/WorkerSession，可在同一 bootstrap identity 下显式重试或归档。成功时原子创建首个 RepositoryCheckout/WorkspaceRevision、active pointer、`ready` 状态和 Outbox；见 ADR 0056。
- M1 branch/tag 选择必须由宿主 Repository adapter 在事务外解析为短期 RepositorySelectionSnapshot，绑定 actor/tenant/repository/requested ref/exact commit SHA/digest。CreateWorkspace 只接受有效 Snapshot 并固化 SHA；Importer、retry、Task submit/dispatch 不得重新解析远端 ref。Snapshot 不授予仓库权限；M1 重验本地 allowlist/read policy，后续 profile 重验 RepositoryAccessGrant。见 ADR 0057。
- 多 WorkerSession 共享 Workspace 时采用隔离 WorkingCopy：每个 WorkerSession 基于一个 WorkspaceRevision 工作，RuntimeInstance 只挂载该 WorkingCopy；Worker 产出带 base revision 的 ChangeSet，Workspace 在短时独占写租约下串行应用并生成新 WorkspaceRevision。禁止多个 WorkerSession 直接并发写同一权威目录。
- P0 WorkingCopy 与持久 Agent state 位于宿主管理的 WorkspaceStorage，不在 disposable container layer；容器只 bind mount 当前 WorkerSession 的副本。Git 使用 per-session worktree 和不可变 commit ref，非仓内容使用 materialized tree、manifest 与 checksum；reflink/copy-on-write 只能是优化，不能成为正确性前提。
- WorkspaceApply 的昂贵校验和多组件物化必须在 WorkspaceWriteLease 外完成，产出完整不可变、非权威的 StagingRevision。Finalize 才获取 PostgreSQL lease row 的单调 fencing token，并在同一事务中重验 expected revision/token/digest、创建 WorkspaceRevision、切换 active pointer、记录 Apply result 和写 Outbox。事务内不得复制大目录、运行测试或调用外部服务。
- 多组件 Apply 的原子性是“一个数据库 active pointer 整体选择一份完整 manifest”，不是多个目录同时 rename。提交前崩溃只留下可清理 staging；提交成功但响应丢失按 idempotency key 返回原 revision；过期 token 和 stale base 均不得推进 active revision。
- P0 Tenant 是硬安全边界：每个 Workspace 只属于一个 Tenant；访问必须同时具备 active TenantMembership 和显式 WorkspaceMembership。Project/Team 关联不隐式授予 Workspace 权限，跨 Tenant 共享同一 Workspace identity 必须拒绝。
- WorkspaceRole 是可组合的 viewer/editor/reviewer/owner。reviewer 不自动拥有 editor；owner 可管理成员但不能绕过 Tenant、外部 provider 和平台硬策略；任何成员变更必须 expected revision + audit，且不能移除最后一个 active owner。
- Worker 始终代表发起用户，通过短期 ExecutionAuthorizationLease 执行；lease 必须绑定 actor、tenant、workspace、WorkerSession、allowed actions、membership/policy revisions 和 expiry。Worker/runtime 不能替换 actor 或自行扩权。
- WorkspaceRole 只授权 Switchback 内动作。GitHub/GitLab、飞书、生产系统等外部动作还必须独立通过 Capability/ResolvedToolPolicy、对应的 RepositoryAccessGrant 或 ApprovalGrant、SecretLease 和 provider 权限；任何一层失败都 fail closed。
- Membership/Policy 撤销后必须 fencing 新 command、Apply、Artifact action、受保护读取、lease renewal 和 secret issuance；撤销 execution/secret lease，保留 WorkerSession/Trace/Artifact 并进入 `waiting_user:access_revoked`。已运行本地动作无法证明停止时按 TaintedRuntime 处理。
- Worker 详情页允许用户绕过 Assistant 直接提交 AgentTurn，支持 prompt、queue、interrupt 和 steer；WorkerRun 阶段完成会生成 report/patch，但不会关闭仍在有效期内的 WorkerSession。
- Worker 详情页必须区分“继续直接交互”和“创建后续 Task”。Agent 只能创建/更新 Proposal 或响应用户明确创建指令，不能依据意图推断自行调用 Task 创建接口。
- P0 用户交付产物只承诺 `report` 和 `patch`；Frontend 按类型渲染，patch 支持 diff 审查、预览、下载、应用到 Workspace，以及在有效 RepositoryAccessGrant 下显式发布分支。PR 和 CI 触发不属于默认 P0 闭环，即使受管 B2 允许 Agent 按任务需要使用 branch/PR/CI-read 能力。
- Worker 在沙箱内执行普通文件、命令、测试、patch 和 report 操作时不额外询问；需要新增外部平台鉴权、跨沙箱或生产写权限、或安装未授权能力时才进入审批。审批支持一次性或限定 WorkerSession 的 capability/target/constraint scope；`waiting_user` 暂停 idle timeout，但不暂停 14 天绝对上限。
- P0 CodePlatform 采用受管 B2：固定镜像预装版本锁定的 Git、GitHub CLI 和批准的 GitHub Skills；Agent 在 repository-scoped `once` 或 `worker_session` RepositoryAccessGrant 生效后，可直接执行短期 Token 允许的非管理类 `git`/`gh` 操作，不再逐命令审批。WorkspaceRole、TaskSpec 或 prompt 本身都不能替代该 Grant。
- 受管 B2 是显式受托模型：RepositoryCredentialLease 中的短期 access token 对 Agent 可用，不声称对容器内任意命令保密；GitHub App 私钥、OAuth refresh token、长期 PAT/根凭证只能保存在控制面 CredentialBroker。Token 生命周期独立于最长 14 天的 WorkerSession，只有 Membership、Grant、Session 和 policy 均有效时才可续签。
- Git 使用开启 `credential.useHttpPath=true` 的 `git-credential-switchback`，GitHub CLI 使用按 invocation 获取当前 lease 后 `exec` 原生 `gh` 的受管 wrapper；禁止持久化 `gh auth login`、明文 credential store、带 Token remote URL 或镜像内置凭证。
- P0 Repository capability 分离为 `repository.read`、`repository.write`、`pull_request.write` 和 `ci.read`。禁止 administration、secrets、variables、workflow/Actions write、ruleset management、repository delete 和组织管理；default/protected branch、force push 和 delete 由 Provider ruleset/branch protection 强制。
- 每次外部写入产生 CodePlatformOperation，记录 actor/session/repository、能力快照、命令类别、目标 ref、provider id 和结果，但不记录 secret。push/PR 响应丢失或认证/网络失败时必须先核对 remote ref/head 或 provider 对象后再决定重试，禁止盲重放外部写入。
- 平台已知 Token 必须在 Trace、Langfuse、Artifact、shell/CLI 输出持久化前做确定性剔除；这是 P0 高保真 Trace 暂不要求通用 DLP 的明确例外。
- 方案 A（所有远端操作由控制面执行）和方案 C（远端只读直连、写入由控制面执行）保留为 fallback profile。安全政策禁止 Token 进入 Agent 时切 A；Provider 写权限过粗或 ruleset 无法收敛写入、但仍需原生只读 Git 灵活性时切 C。三种 profile 复用 RepositoryAccessGrant、CodePlatformOperation 和 provider result 契约。
- 所有关键执行步骤都应产生结构化事件和 JSONL 轨迹；Trace 需要导出到 Langfuse 作为观测投影，但对象存储 JSONL 仍是权威事实源。
- TraceStream 必须通过 PostgreSQL TraceManifest 管理 immutable segments、sequence range、checksum、schema、storage tier、availability、Langfuse export 和 analytics cursor；排障/Evolution 按 manifest 读取，不能扫描 bucket 猜测内容。
- P0 DataLifecyclePolicy 默认 hot 30 天、warm 至 180 天、cold 长期且不自动物理删除。LocalFileObjectStore 可逻辑模拟 tier；具体 S3 storage class 不得进入领域契约。
- Dashboard 必须消费 IntegrationEvent/Trace ingest 的 normalized facts 增量构建 TraceAnalyticsProjection，不得在请求路径扫描原始 JSONL，也不得把 Langfuse 当唯一统计源。
- P0 生命周期审计保持轻量，只记录 actor/action/target/reason/before-after/time/result/correlation id；archive/restore/quarantine/delete 不要求默认工单或四眼审批。raw payload 读取仍需明确能力并记录 access action。
- 显式删除通过正常授权和一次确认进入幂等 job，删除 payload 与可识别 Langfuse projection 后保留最小 tombstone和非敏感聚合；Evolution evidence 必须标记 `source_deleted`。quarantine 内容不得继续普通下载、用户投影或新 Evolution 消费。
- 产物必须通过 Artifact 引用管理，不要只散落在日志里。
- 对 Agent 原始输出、用户可见进度、内部调试日志进行分层。

## Evolution 与 Inspiration 约束

- Evolution 只能从可追踪来源生成 Truth，每条 Truth 需要 source_trace、scope、confidence、version。
- Evolution 拥有 Truth 生命周期和读取能力。
- Evolution + PostgreSQL 拥有 ContextCandidate、不可变 ContentRevision、审核、tombstone、审计和用户 Memory 看板；只有批准后的 active revision 才投影到 OpenViking，并记录 projection/snapshot 状态。
- Evolution 可用模型对 TruthCandidate 作出自动发布或人工审核建议，但模型没有发布权。MemoryPublicationPolicy 只允许 `explicit_preference`、`verified_descriptive_fact`、`entity_alias` 和带强制 TTL 的 `ephemeral_project_state` 进入自动发布候选；还必须满足低风险、非敏感、无冲突、scope 不扩张和证据门槛。
- 自动发布证据必须是用户明确原话、权威来源或确定性运行验证之一；模型推断、摘要、置信度或重复行为本身不能单独授权自动发布。`explicit_preference` 只能表达低风险选择倾向，不能授予权限或编码生产/外部副作用、强制流程和行为边界。
- 推断偏好、行为/流程/Instruction、Skill、权限安全、生产/外部操作、跨用户或 org scope、敏感信息、失败案例归纳、主观评价、冲突或低置信内容必须进入人工审核或拒绝。动态事实按 policy 设置 `refresh_after`，`ephemeral_project_state` 必须设置 `expires_at`；过期内容不得继续进入 ContextPack。
- Memory 治理 UI 分为 MemoryLibrary 和 ReviewInbox。Library 按 user/workspace/project scope 展示 active/disabled/expired Memory；Inbox 将 Memory、Procedure、Instruction、Skill 分 lane，展示 diff、证据、风险、冲突、scope/ACL 变化、策略判定和审核历史。
- user-scope Memory 由本人治理；workspace/project 内容需要对应 owner/reviewer 权限。批量 reject/disable 可在逐条鉴权与 expected revision 校验后执行；批量 approve 仅允许同 scope、同 policy revision、同低风险 Memory 类型且无敏感/冲突/scope 扩张的 ReviewTask。Procedure、Instruction、Skill、权限安全、跨 scope 和冲突内容禁止批量批准。
- 看板编辑生成新 ContentRevision；disable 移除 active projection，delete 写 tombstone，restore 生成新的 active revision。批量命令按 item 返回成功/失败，禁止静默部分成功。P0 不设置阻塞式审核 SLA，只显示 age/摘要提醒；待审内容永不进入 ContextPack。
- InstructionPolicy 和 SkillPackage 的生成或修改必须人工审核。Evolution 生成的 Skill 完成人工发布审核后，可在批准 scope 内免去第二次安装提示，但不能把“Evolution 生成”当作自动发布许可。
- 不要把一次失败、临时状态或未经验证推测沉淀为长期规则。
- Truth 应支持过期、覆盖、删除、人工审核或回滚；用户看板操作先修改 Switchback RDBMS，再通过 Outbox 同步 OpenViking，不能让 UI 直接修改 OpenViking。
- Inspiration 是 P2 experimental 且必须 capability-gated；Assistant/Worker/ContextPack 在其缺失、停用或无候选时正常工作。保留设计采用独立 Git repository、YAML InspirationEntry、Markdown context、确定性 `INSPIRATION_INDEX.md` 与可重建 ResourceRecord/OpenViking 投影；语义变更走 PR/CODEOWNER，完整文档由 lark-cli/Skill 读取。main merge 使用 webhook fast path，周期 reconciliation 按 head/last projected SHA 修复漏事件。没有真实 Workspace 使用证据前不得建设完整 connector/自动摘要链路。
- 对外部文档、飞书资料、妙记等来源要保留权限和来源信息。

## EventCenter 约束

- 所有跨模块事件必须带 schema_version。
- 关键事件必须带 trace_id、correlation_id、causation_id、tenant_id、user_id 或可解释的空值原因。
- Handler 必须考虑幂等、乱序、重复投递、重试和死信。
- 同一事件流按 `stream_id + sequence` 排序，按 `event_id`/`idempotency_key` 去重；时间戳只用于审计和展示，不能单独作为可靠顺序依据。
- 每个 EventStream 必须有唯一 StreamOwner。Owner 在业务状态和 Outbox 的同一事务中分配 sequence；EventCenter 不生成或重写领域 sequence。不同 bounded context 不得直接向同一个 stream 并发写入，需要产生该 stream 事件时必须调用 owner command。
- 消费者遇到同 event id 重投时幂等忽略；遇到未来 sequence 时暂存并记录 gap，补齐后按序应用；同一 `(stream_id, sequence)` 出现不同 event id 是 integrity incident，不能 last-write-wins。
- 跨 stream 不存在全局业务顺序。`correlation_id + causation_id` 表达因果；TimelineProjection 另行分配 `timeline_epoch + TimelineCursor` 供 SSE resume、分页和稳定追加。普通 replay 必须复用 cursor；无法保留映射的全量重建递增 epoch 并要求客户端 resync。`occurred_at` 仅展示时间。
- 迟到用户事件必须追加新 TimelineCursor 并标记 `late=true`，不能插入或重写已发送 cursor。P1 RocketMQ 以 `stream_id` 作为 FIFO message group，但 payload sequence 仍是权威，broker 顺序不能替代消费幂等和 gap 检测。
- 区分用户可见事件与内部观测事件。
- 不要在事件 handler 里做不可控长阻塞操作；长任务应进入 Worker 或后台任务。

## Frontier / Frontend 约束

- P0 以 WorkerDetailView 为唯一主要执行交互面，其主键是 `worker_session_id`；不要把 Worker 页面误解为后端 Worker service instance。TaskList 是轻量交付索引，TaskDetail 只做摘要/抽屉和 Worker deep link。
- Home P0 聚合最近 Worker、waiting_user/approval 和近期交付即可，不要求完整 ActionInbox 产品。Workspaces 是持久上下文入口；P1 Memory 使用独立治理入口。
- WorkerDetailView 展示 chat/Activity/Approvals/Runtime/Artifacts/当前 Task 与历史 lineage。Task 终态不能关闭仍有效的 Worker 页面，standalone Run 不能伪造 Task。
- Artifact 使用 canonical ArtifactDetailView；Task/Worker/Workspace 只嵌入摘要和 deep link，不复制权限、生命周期或 action 状态。MemoryLibrary/ReviewInbox 独立存在，只通过 evidence refs 跳转 Task/Worker。
- Frontend 只渲染 Frontier 的 page-optimized projections，并通过 Gateway 发命令；不得从 raw event/Trace 自行重建权威状态，也不得本地假定 command 已成功。
- 所有 View 必须携带 canonical identity、权限过滤后的 available actions 和 related refs。raw Trace 与用户可见 Activity summary 分离授权。
- P0 Frontier 同步固定为 HTTP ProjectionSnapshot + typed SSE ProjectionDelta。每个 ViewProjection 有独立 `view_epoch:view_cursor` 和 projection version；Snapshot cursor 与数据同版本提交，SSE 用 `Last-Event-ID` 从其后续传。
- ProjectionDelta 使用版本化语义 event type 和精确 base/result version，不暴露 raw IntegrationEvent、Trace、broker payload、数据库 CDC 或通用 JSON Patch。客户端只按兼容 reducer 应用连续 delta。
- cursor 超出 ProjectionReplayWindow、epoch 变化、gap 或 base version 不匹配时返回 `resync_required`，Frontend 丢弃该 View 缓存并重拉 Snapshot。协议版本不兼容使用 `client_upgrade_required`，不能靠无限 resync 掩盖。
- Gateway command 只快速返回 accepted + command_id；Frontend pending 是临时 UI 状态，必须由带 command/causation 的 ProjectionDelta 或后续 Snapshot 收敛。超时不等于失败，不得换新幂等键盲重放。
- Native EventSource 优先使用 same-origin Secure/HttpOnly cookie 或等价 BFF，不把 bearer token 放入 URL。Snapshot/stream 同时鉴权，撤权后 fencing delta 并清除不可访问 View。
- GraphQL 不进入 P0。未来只能作为相同 page projections 的可选 read adapter，不能替代 Gateway command、SSE cursor/resync 或 owner context。
- Agent display 固定采用 durable/transient 双 lane。DisplayCheckpoint 是完整物化的用户可见 Parts，进入 ViewProjection、推进 projection version/ViewCursor 并可重放；TransientDisplayDelta 只做实时渲染，不推进 durable cursor/version。
- Transient delta 必须绑定 display_stream_id、part_id、base checkpoint version 和 stream-local sequence。重复可忽略；gap 时停止该 part 拼接并等待/获取新 Checkpoint，不得猜测缺失文本或改变 Task/Worker 状态。
- 每个 display stream 必须有 start checkpoint、策略限制的周期 checkpoint 和完整 terminal checkpoint；Tool/Approval/waiting_user/Artifact/error/cancel/timeout 等语义边界必须 durable。具体时间/字节阈值由版本化 DisplayDurabilityPolicy 经 Codex/ACP load spike 校准。
- Transient SSE event 不设置新的 durable SSE id，`Last-Event-ID` 继续指向最后 ViewCursor。允许合并相邻 chunk、丢弃非语义 progress pulse，但绝不丢 durable checkpoint/control。
- “Frontend transient”不等于证据丢失：Worker 仍把高保真 ACP/Agent frames 写入权威 Trace JSONL。Display 只暴露用户可见 reasoning summary，不把隐藏 chain-of-thought 作为前端协议。
- DisplayPart 固定为 Switchback-owned `frontier.display_part.v1` 物化联合类型：`text`、`reasoning_summary`、`tool`、`activity`、`approval`、`artifact`、`source`、`error`。每个 part 必须有稳定 `part_id`、单调 `part_revision`、稳定 `ordinal`、lifecycle、criticality、permission-filtered visibility、provenance 和 Gateway command action refs。
- ACP/Codex、未来 AG-UI 和 AI SDK 只能通过 DisplayProtocolAdapter 映射 source identity/lifecycle；source tool/message id 不能单独作为 canonical identity，provider-native payload/type 不能泄漏进领域契约。未知可选 progress 可降级为受限 activity；未知 Tool/Approval/权限/安全语义必须 fail closed。
- Display payload 禁止任意 HTML/JS/React component、未经治理的本地路径和凭证。Frontend RendererRegistry 按 schema/type 渲染；未知 non-critical part 使用无特权动作的安全 fallback，未知 critical part 返回 `client_upgrade_required`。`available_actions` 只是 Gateway command refs，不授予权限，也不允许前端直接改写 owner state。
- P0 Frontend 固定采用 React + TypeScript + Vite SPA。Node.js 只用于开发/构建，不是生产应用运行时；生产静态 bundle 与 Go Gateway/Frontier 通过同源 ingress/静态服务发布，不引入 Next.js/Node BFF。
- React Router 拥有 route/resource/filter identity；TanStack Query 只拥有 HTTP request/cache/refetch lifecycle；独立 `ViewProjectionStore` 拥有 active ClientViewReplica 的 epoch/cursor/version、typed reducers、transient overlay 和 PendingCommand convergence。组件不得各自消费 raw SSE 或同时维护第二份权威 View。
- Snapshot 成功后原子 replace 对应 ClientViewReplica；ProjectionDelta 通过 schema/epoch/base/result 校验后推进；TransientDisplayDelta 只更新有界 overlay。accepted command 只注册 PendingCommand，必须由 causal delta、command status 或 replacement Snapshot 收敛。
- assistant-ui 只能通过 ExternalStore adapter 用于 Worker Chat 的可选 components/runtime；Switchback 拥有 message/thread/session identity、持久化、SSE recovery、Tool/Approval authority 和页面外壳。未定义 Switchback command 的 edit/branch/regenerate 等能力不得由 UI 本地模拟。
- 同源浏览器鉴权使用 Secure/HttpOnly/SameSite session cookie；状态变更命令遵守 Gateway CSRF 策略和 idempotency。Vite client env 不得承载 secret；SPA fallback 不得吞掉 API/SSE/Artifact route。

## LLMProxy 约束

- 模型请求必须可审计、可限流、可统计成本。
- Assistant 模型调用和 Worker Agent 模型调用都应关联 trace_id / task_id / session_id。
- P0 直接使用 LiteLLM；只保留 Switchback 的 ModelPolicy/ModelInvocation 契约边界，不额外实现第二套通用网关，也不向领域契约泄漏 LiteLLM 专有字段。
- 对敏感字段、密钥、用户数据和内部文档内容要考虑脱敏与日志降噪。

## Delivery / 飞书入口约束

- 飞书消息必须能映射到内部用户、租户、权限和任务上下文。
- P0 飞书群 topic 使用一个 `scope_type=channel` 的共享 primary Assistant Conversation；不得按 sender 建立隐藏 per-user Conversation。每个 Turn/command 必须保留真实 actor，不能用 binding creator、controller 或 channel identity 代替。
- channel Conversation 使用 Interaction-owned `ConversationParticipant`，P0 role 为 `participant` 和 `controller`。provider-mapped participant 可提交普通 Turn；只有 active controller 可选择/清除默认 Workspace、触发 rollover、关闭 channel Conversation 或管理 controller。
- Binding creator 只有通过 provider mapping、Tenant 和 channel policy 后才成为首个 controller。Provider membership、WorkspaceMembership/Role 都不自动授予 controller；delegation/revoke 要 expected revision + audit，不能显式移除最后 controller。
- Controller 只是 Conversation 协调权限，不授予 Workspace/Task/WorkerSession/Approval/Artifact/外部 provider/群披露权限。每个 control/action command 仍按真实 actor、target authority、policy、intent evidence 和 revision 独立重验。
- 撤权导致最后 controller 失效时 Conversation 进入 `control_orphaned`，fencing context/lifecycle/controller mutation；普通 participant 不自动提升，恢复需 Identity & Access policy-authorized channel administrator 且当前 provider participant eligibility 有效。
- ConversationRollover 创建 successor 时，只按当前 policy 投影 source generation 中仍合格的 participant/controller，保留 source role revision/evidence，不得新增或提升 controller；若无合格 controller，successor 以 `control_orphaned` 开始。
- P0 一个 active `ChannelThreadBinding` 在任一 revision 固定绑定一个 current primary Assistant Conversation。无有效显式 target 的普通消息始终形成该 Conversation 的 Assistant Turn；创建 Task、WorkerRun 或卡片不得自动改变默认路由。只有接受的 ConversationRollover 可在新 binding revision 下把 primary Conversation 切换到 successor。
- TaskDraft、Task、WorkerSession、Approval 和 Artifact 操作必须携带经验证的 `InteractionTargetRef`，来源只允许当前 card action、结构化 command 或注册了不可变路由元数据的明确交互上下文。Delivery 不得根据 latest activity/latest card/provider parent_id/模型推断维护或恢复 `current_focus`。
- `ChannelThreadBinding.related_refs` 只用于投影和导航，不参与路由。话题不支持时可按 provider capability 降级为 chat/root-message binding，但保持 Assistant-default 语义；同一话题内每个 actor 都必须独立映射和重新授权。
- 普通消息按 provider message identity + content revision 幂等；binding revision、actor、显式 target evidence 和服务端接收时间写入 `InboundInteractionEnvelope`。Provider/system 导致的关闭/revoke binding 只 fencing 通道路由，不得静默关闭关联 Conversation、Task 或 WorkerSession；只有显式 controller `CloseChannelConversation` 可按 expected revisions 同时关闭 Conversation/binding，且仍不关闭 Task/WorkerSession/Artifact。
- ChannelThreadBinding 可在 `default_workspace_id` 为空时继续 Assistant 对话。任何 Workspace-scoped 读取或执行前，用户必须通过 Web selector、当前 card/结构化 command，或显式 CreateWorkspace 结果确认 Workspace；用户全局默认、唯一候选、URL、related ref、最近活动和模型推断只能用于推荐。
- SetWorkspaceContext 必须带 expected binding revision、actor/intent evidence 和稳定幂等键，成功后递增 binding/workspace-resolution revision。选择不是授权 grant，后续读取、Task/Worker command 和 lease issuance 都要重验 Membership/Policy。
- 更换/清除已有 resolved default Workspace 通过 successor Conversation 只影响未来 Assistant context 和未定目标 TaskDraft，不迁移或覆盖既有 Task、WorkerSession、Trace、Artifact。显式 resource target 永远使用资源自身 Workspace，也不回写 binding default；撤权后旧 resolution 变 stale、rollover 到 unresolved successor 且禁止 fallback。
- Workspace rebind/clear 不在原 Conversation 上原地替换 default：已有 resolved Workspace 历史时必须 rollover。Inbound message 与 rollover 通过 binding/source row lock 和 revision 串行化；提交在 rollover 之前的消息属于 predecessor，观察到新 revision 的消息属于 successor，stale envelope 不得静默改挂或重复。
- Workspace 选择本身不代表群内所有成员可查看 Workspace 内容。P0 群聊披露采用 Identity & Access 拥有的显式 `WorkspaceChannelShareGrant`：当前 Workspace owner 将一个 Workspace 绑定到已验证 provider group audience 和 `workspace_summary` profile 后，Delivery 才可发布 owner projection 明确标记为 `channel_shareable` 的字段。
- `WorkspaceChannelShareGrant` 只授权披露，不授予 Tenant/WorkspaceMembership、执行、Web 访问或卡片动作权限。每次共享卡片发送/更新必须取得当前 `ChannelDisclosureDecision`，重验 grant/policy revision、provider audience snapshot 和 source classification；renderer 只能消费 decision 的字段 allowlist。
- 无有效 Grant、audience 扩张、群外部化/公开化、bot 失去群状态读取能力、provider snapshot stale/unknown、Grant 撤销/过期或 owner Membership 失效时，群卡片降级为不标识受保护资源的 `minimal_notice` 和 canonical Web link；Web 对每个打开者独立鉴权，链接不得携带个人 bearer authority。
- raw Trace、私有 prompt、hidden reasoning、Secret、Tool stdout、内部错误、完整 report、patch/diff 和 unrestricted Artifact content 即使有 Grant 也不得进入 P0 共享卡片。actor-private 交互优先使用 provider per-user ephemeral card，否则转 Web。
- Grant 撤销只保证 fencing 后续披露并 best-effort 更新/撤回 bot 消息；provider 时限、已读通知、引用、导出或截图无法事务性收回，必须记录 cleanup result 和 residual-exposure audit，不得宣称历史内容已彻底删除。
- Task readiness 与 Task creation intent 必须独立判断。共享群聊 `ordinary_message` 即使 readiness 已满足也始终先形成 Assistant Turn；只有结构化 `/task`/create-task form 或绑定 exact ready TaskDraft 的注册确认动作可生成 `ExplicitTaskCreationIntent`。P2P/private/API 可由版本化 TaskCreationTriggerPolicy 接受明确 create request。
- 创建意图必须绑定真实 actor、interaction scope/source evidence、exact TaskDraft revision、TaskSpec digest、readiness assessment、Workspace resolution、Conversation generation、policy revision、expiry 和 idempotency key。任一绑定事实变化时旧 intent stale，不能迁移到新 draft 或 rollover successor。
- Ready TaskDraft 确认可复用 `waiting_user` card kind 和 `task_draft` target，不新增第五种 card；无 immutable target 的自由文本“确认/直接做”仍是普通 Assistant Turn。完整结构化命令可零问创建，不完整命令补全后需对最终 ready draft 重新确认。
- Shared TaskDraft 必须记录 immutable `initiator_user_id`；只有 initiator 可生成该 draft 的 ready confirmation intent，且 intent actor、draft initiator、TaskSpec `user_id` 必须一致。Contribution、controller 或 WorkspaceRole 不能改变 initiator。
- Non-initiator 必须显式 `AdoptTaskDraft` fork 新 draft，记录 `derived_from_task_draft_ref`，重新过滤 context refs、重验 actor/Workspace/readiness/policy 并自行确认。不得继承 source intent、Approval/Repository grant、Secret/private evidence 或 actor-specific assumption，也不得修改/消费 source draft。
- Adoption 按 actor + source revision 幂等；不同 actor 相同 digest 不自动合并。Rollover、Workspace mismatch、source consumed/cancelled/context unreadable fail closed；仅 readiness/source-actor authorization stale 时可在 payload/current generation 仍有效的条件下 fork 并完全重验。
- Task Orchestration 必须同时重验 intent、readiness、TaskSpec digest、Workspace/actor/policy 后原子创建 Task、初始 TaskTransition 和 Outbox；重复 callback/command/response loss 返回原 Task identity。创建 Task 不改变 primary Conversation、default Workspace 或 current focus，controller role 不扩大创建权限。
- 长任务需要持续回传进度和最终结果。
- 用户需要补充信息、授权或确认风险操作时，应进入明确的 waiting_user 状态。
- Delivery 只持有通道投递状态；Task/Assistant/Worker 持有业务和执行状态。
- P0 使用独立 `DeliveryCardProjection`，从 owner events、授权 public facts 和 durable DisplayCheckpoint summary 派生 `task_progress`、`waiting_user`、`approval`、`result` 卡片；禁止扫描 Web View JSON、React output 或 raw Trace 构建卡片。
- Delivery 只拥有 CardInstance/provider message mapping、render revision、send/update/reconciliation/expiry 状态。Projection 必须带 canonical subject/source refs、projection/render revision、permission-filtered summary/actions、Web deep link 和 DeliveryProviderCapability ref。
- P0 inline action 仅允许当前 owner projection 显式暴露的 `create_task`、`adopt_task_draft`、`submit_choice`、`submit_text`、`approve_once`、`approve_session`、`cancel`、`accept`、`rework`。`create_task` 只用于 initiator 的 `waiting_user` + exact `task_draft`；`adopt_task_draft` 只 fork 新 actor-owned draft；共享 Task action 只是候选，callback 必须按真实 actor 重验 initiator 或 owner emergency-cancel 规则。完整 Artifact review、继续 Worker chat、patch apply、branch/PR/production write、Skill install和任意 Tool 必须转 actor-private/Web；AgentTurn deadline 只读展示且不可操作。
- 卡片 callback 必须验证 provider signature、actor mapping、ChannelBinding、Tenant/Workspace access、opaque action token、render revision、expiry 和 duplicate identity，再以稳定 idempotency key 向 Gateway 提交 command。callback accepted/toast 不是业务成功；只能由 owner evidence 更新 projection 后收敛。
- Feishu 3 秒回调、14 天更新和 30 天交互等限制属于版本化 DeliveryProviderCapability/profile，不能硬编码进 Task/Worker/Approval 生命周期。过期、组件不支持或复杂交互降级为只读摘要和授权 Web deep link。
- P0 卡片只在语义/策略边界更新，不逐 token 镜像 Agent output。重复 callback/update 幂等，provider response 丢失先 reconciliation，禁止盲重放业务命令。

## Skill 安装与管理约束

- 预装 Skill 不需要额外用户安装确认。Evolution 生成或修改的 Skill 必须先通过人工内容/安全审核；审核发布后，在批准 scope 内不再要求重复安装确认，但仍必须通过平台策略、权限和来源检查。
- 其他 Skill 默认需要用户手动批准；用户在与 Assistant 的明确交互中要求安装，视为可审计的 `CapabilityInstallGrant`。
- Assistant 和 Worker 只能通过 Skilling 内部 API 搜索、请求安装、启停、升级和回滚 Skill，不得绕过控制面直接修改用户能力配置。

## 测试与验证

代码改动应根据风险选择验证方式：

- 领域逻辑：单元测试覆盖状态机、权限、任务规格校验、Truth 生成规则。
- Workspace：覆盖宿主持久 WorkingCopy、session 专属 bind mount、单仓/多仓/非仓 manifest、租约外 staging、stale base、过期 fencing token、重复 Apply、提交前后崩溃、多组件失败、rebase/conflict、runtime 恢复和 active revision/Outbox 原子推进。
- 授权：覆盖 Tenant mismatch、无 TenantMembership、无 WorkspaceMembership、角色组合、reviewer/editor 分离、last-owner 保护、expected revision、跨 Tenant 引用、Worker actor 绑定、外部权限独立校验、撤权竞态和运行中 lease/secret fencing。
- CodePlatform：覆盖无 Grant fail closed、once/session Grant、repository/path credential 隔离、短 Token 续签与撤销、管理类 permission 拒绝、protected branch/ruleset、已知 Token 剔除、push/PR remote reconciliation、旧 runtime lease fencing，以及 A/C fallback profile 契约一致性。
- Lightweight Task：覆盖固定状态集、manual delivered/accept/rework、auto completion、终态不可改写、返工新建 WorkerRun、runtime recovery 保持原 Run、standalone Run 不改写旧 Task，以及 Task/revision/Transition/Outbox 原子推进、重复 command、commit 后响应丢失、ScheduledCommand 多 poller 竞争/fencing、waiting_user 时钟和外部 unknown-effect 禁止盲重试。
- Runtime 恢复：覆盖首次自动重建成功、旧 generation/epoch fencing、重复故障进入 waiting_user、不可恢复错误 fail closed、Agent session resume 失败和 SecretLease 重新签发。
- WorkerSession 终态续作：覆盖 closed/expired 不可复活与只读投影、exact source/checkpoint、derived identity/WorkingCopy/AgentSession/clocks、lineage、旧 Session-scoped authority 不继承、latest-proven coverage warning、Task/Continuation allocation FIFO、公平性、满额、响应丢失、撤权和 materialization 失败无 orphan。
- Tool 取消：覆盖 confirmed local kill、子进程残留导致 runtime taint、remote-safe fencing、non-idempotent/unknown waiting_user、重复 retry 拒绝和 late result 无法改写终态。
- SessionProcess：覆盖普通后台子进程无法逃逸 Turn cleanup、显式 managed start、start-vs-terminal revision 竞态、所有 AgentTurn terminal reason 下存活、WorkerDetail stop/restart、同一 Runtime 权限/资源继承、日志轮转、Session terminal/安全/资源清理，以及 Runtime replacement 后 no-auto-restart。
- PreviewProxy：覆盖 exact actor/process revision/generation/declared-port 校验、无 host-port mapping、任意 target/CONNECT 拒绝、HTTP/WebSocket 转发、控制凭证剥离、bytes 不进入 EventCenter/Trace、每次 request/upgrade 状态重验、终态/撤权/expiry tunnel 关闭，以及 preview traffic 不续期 idle。
- Memory 发布：覆盖四类 allowlist、用户明确/权威/确定性验证证据、模型推断拒绝、scope/ACL 扩张、敏感/冲突/行为型重分类、动态 TTL 到期、policy revision 重放和自动发布后的用户撤销。
- Memory 治理：覆盖 user/workspace/project reviewer 权限、Library/Inbox projection、批量 approve 同质性门禁、禁止高风险批量批准、expected revision 冲突、逐项部分失败、edit/disable/tombstone/restore、Outbox projection 和 pending 不进入 ContextPack。
- Runtime 健康：覆盖 15 秒 heartbeat、45 秒 suspect、90 秒 unhealthy、恢复前 transport/process probe、明确进程退出快速失败、服务端 receive-time 判定、长模型/tool 操作不误判，以及 replacement 的 WorkingCopy 独占挂载。
- Frontier display：覆盖 start/周期/语义/terminal checkpoint、transient duplicate/gap/base mismatch、断线与 Frontier 重启、terminal 完整渲染、Tool/Approval/Artifact durable、背压 coalesce/drop、transient 不推进 ViewCursor、Trace 保真和隐藏 reasoning 不投影。
- DisplayPart/adapter：覆盖 8 种 part、稳定 id/revision/ordinal、ACP/Codex identity/lifecycle mapping、未知可选 progress fallback、未知安全语义 fail closed、critical/non-critical renderer 兼容、Markdown/code sanitization、action ref 非授权性和 v1 additive/v2 breaking 规则。
- Frontend runtime：覆盖纯 reducer、Snapshot seed/replace、TanStack Query 与 ViewProjectionStore 单 owner 边界、route identity、transient overlay、PendingCommand causal convergence、assistant-ui canonical-id mapping、SPA/API fallback、cache header、cookie/CSRF、protocol upgrade 和浏览器 reconnect E2E。
- Delivery card：覆盖四类 projection、source/render revision、permission filtering、deterministic render、provider signature/actor/binding/action fencing、inline allowlist、Web-only actions、3 秒 callback、重复/响应丢失幂等、provider expiry/限流降级和 callback 无法直接改 owner state。
- Channel disclosure：覆盖 Grant 缺失/错误 scope/stale/revoke/expire、source class 与 profile 双重 allowlist、audience expansion/外部化/unknown snapshot 自动暂停、actor callback 独立鉴权、ephemeral fallback、Web link 无 bearer authority、受限字段永久禁止和历史消息 cleanup residual audit。
- Conversation rollover：覆盖 unresolved 首次 resolution 不 rollover、W1->W2/clear/revoke、W1->W2->W1 generation chain、原子 binding/source fencing、重复/响应丢失/并发 rebind、并发 message 精确归属、workspace-neutral handoff、显式 cross-Workspace carry-over、旧资源不迁移和 Evolution scope 不扩张。
- 应用编排：测试命令处理、事务边界、幂等和错误分支。
- 适配层：测试 HTTP/RPC/MQ/飞书/ACP adapter 的 schema 兼容。
- 异步链路：覆盖重复事件、乱序事件、失败重试、死信和取消。
- 存储层：覆盖对象存储 JSONL、关系库状态、OpenViking 写入与检索。
- Trace 生命周期：覆盖重复 segment ingest、checksum mismatch quarantine、hot/warm/cold 迁移、cold restore、Langfuse/analytics 投影重试、显式/部分删除、tombstone、source_deleted 和原 payload 删除后聚合保留。

如果当前仓库还没有标准命令，先查看 `Makefile`、`go.mod`、CI 配置、README；不要臆造测试命令。

## 配置与密钥

- 不要把密钥、Token、内部 endpoint、数据库密码或模型 key 写入仓库。
- 配置应区分本地、测试、预发、生产。
- 与 Skill/MCP/LLMProxy/容器运行时相关的权限配置需要显式声明默认值。

## 文档维护

- `REQUIREMTENTS.md` 是需求和架构澄清草稿，后续应随设计更新。
- `TECHNICAL_DESIGN.md` 是当前 DDD 总体技术设计基线。
- `CONTEXT.md` 是项目级领域词汇表；新增核心概念时先更新它，再扩散到代码和设计文档。
- `docs/requirements/requirements-matrix.md` 是可评审需求矩阵，包含优先级、验收标准、owner context 和未决问题。
- `docs/architecture/context-map.md` 是 DDD bounded context 与 published language 基线。
- `docs/architecture/diagrams.md` 是 Mermaid 架构图、时序图、状态图和概念数据模型。
- `docs/architecture/workspace-storage-and-apply-explainer.md` 记录 P0 WorkingCopy 宿主持久布局、StagingRevision、短时 fenced Finalize、单仓/多仓示例和崩溃恢复。
- `docs/architecture/tenant-workspace-authorization-explainer.md` 记录 Tenant 硬边界、WorkspaceMembership 角色矩阵、统一授权判定、Worker 委托 lease、撤权和外部 provider 分层。
- `docs/architecture/trace-artifact-retention-and-analytics-explainer.md` 记录 TraceManifest、冷热生命周期、轻量运营、幂等删除、Evolution 证据和效果看板投影。
- `docs/architecture/codeplatform-managed-b2-explainer.md` 记录受管 B2 的信任模型、权限 profile、Token 续期、外部结果核查和 A/C fallback 条件。
- `docs/architecture/postgresql-tasksaga-explainer.md` 记录历史 TaskSaga 方案和仍可复用的 PostgreSQL 同事务、ScheduledCommand lease/fencing poller 与崩溃恢复；P0 领域语义以 ADR 0048 为准。
- `docs/architecture/frontend-information-architecture.md` 记录 WorkerSession 主交互面、轻量 Task/Home、Workspace/Artifact/Memory 页面边界、读模型和跨页面 identity 规则。
- `docs/architecture/frontier-projection-sync-protocol.md` 记录 ProjectionSnapshot、typed SSE ProjectionDelta、ViewCursor、replay/resync、command convergence 和浏览器鉴权。
- `docs/architecture/agent-display-stream-durability.md` 记录 DisplayCheckpoint/TransientDisplayDelta 双 lane、checkpoint triggers、客户端 reducer、背压和故障恢复。
- `docs/architecture/display-part-schema-and-adapters.md` 记录 Switchback-owned DisplayPart v1、8 种 payload、ACP/AG-UI/AI SDK adapter、未知事件处理、RendererRegistry 和版本兼容。
- `docs/architecture/frontend-runtime-and-state-ownership.md` 记录 React/Vite SPA 部署、React Router/TanStack Query/ViewProjectionStore 单 owner 分工、assistant-ui ExternalStore 边界、安全和兼容发布。
- `docs/architecture/delivery-card-projection-and-callbacks.md` 记录 DeliveryCardProjection/CardInstance、四类卡片、有限动作、飞书回调、provider capability、过期降级和幂等对账。
- `docs/architecture/delivery-interaction-design.md` 是 Delivery 端到端交互总设计，串联 shared Conversation participant/controller、入站路由、Workspace resolution/rollover、Task/Worker、卡片、披露、幂等与失败恢复。
- `docs/architecture/channel-thread-binding-and-routing.md` 记录 ChannelThreadBinding、Assistant-default 普通消息路由、显式 InteractionTargetRef、provider topic fallback、幂等和授权边界。
- `docs/architecture/channel-workspace-resolution.md` 记录 Conversation 未解析 Workspace、显式选择/CreateWorkspace、TaskSpec workspace snapshot、rebind、撤权和资源不迁移规则。
- `docs/architecture/conversation-rollover-on-workspace-rebind.md` 记录 Workspace boundary change 时的 successor Conversation、ConversationHandoff、原子 binding 切换、并发消息归属、scope/Evolution 隔离和资源不迁移。
- `docs/architecture/workspace-channel-share-and-disclosure.md` 记录 WorkspaceChannelShareGrant、ChannelDisclosureDecision、`workspace_summary`/`minimal_notice`、provider audience reconciliation、actor-private ephemeral card 和撤权残余暴露。
- `docs/architecture/event-ordering-explainer.md` 用图示、事件表和伪代码解释 StreamOwner/StreamSequence、causation、TimelineCursor、重复/缺口/迟到和 P1 RocketMQ 映射。
- `docs/contracts/core-protocols.md` 是 TaskSpec、Lightweight Task/TaskTransition/ScheduledCommand、WorkerSession/WorkerRun、ProjectionSnapshot/ProjectionDelta、DisplayPart/DisplayCheckpoint/TransientDisplayDelta、WorkspaceChannelShareGrant/ProviderChannelSecuritySnapshot/ChannelDisclosureDecision、DeliveryCardProjection/DeliveryActionDescriptor/CardInstance、ConversationScope/ConversationParticipant、ChannelThreadBinding/InboundInteractionEnvelope/InteractionTargetRef/WorkspaceContextResolution/ConversationRollover/ConversationHandoff、WorkspaceRevision/WorkingCopy/ChangeSet/WorkspaceApply、IntegrationEvent、TraceRecord/TraceManifest/DataLifecyclePolicy、ArtifactRef、Governed Context、ContextPack 等核心契约草案。
- `docs/tooling/project-skills.md` 记录项目级 OpenSpec、Superpowers、Matt Pocock skills 和 Mermaid 文档技能。
- `openspec/changes/refine-switchback-platform-design/` 是当前需求/设计细化的 OpenSpec change。
- `docs/research/market-and-open-source-landscape.md` 记录竞品、开源项目和协议调研结论。
- `docs/research/ci-container-runtime-landscape.md` 记录 CI/CD runner 容器运行模式、Langfuse/Codex 参考和本地运行时前置条件。
- `docs/research/codex-acp-litellm-runtime-notes.md` 记录 Codex ACP adapter、Codex provider config，以及 LiteLLM P0 同容器协同进程/P1 Pod sidecar 的一手资料与设计影响。
- `docs/research/go-acp-and-infrastructure-selection.md` 记录 Go ACP SDK 与 Git/MQ/对象存储/容器基础设施选型。
- `docs/research/openviking-memory-governance.md` 记录 OpenViking CRUD、检索、snapshot、权限能力，以及 Switchback 必须自建的修订/审核/看板边界。
- `docs/research/codeplatform-worker-git-auth-tradeoffs.md` 记录 Git credential helper、GitHub/GitLab Token 生命周期，以及 A/B2/C 在灵活性和续期上的比较。
- `docs/research/graphql-sse-client-server-basics.md` 解释 GraphQL、SSE、Agent C/S 四通道、P0 Snapshot + typed SSE 选择和未来 GraphQL 重新评估条件。
- `docs/research/agent-frontend-technology-landscape.md` 对比 Vercel AI SDK、assistant-ui、AG-UI/CopilotKit 与 GraphQL，记录可借鉴的 typed parts、ExternalStore、Run/Tool/Activity/interrupt 语义及其 Switchback 边界。
- `docs/research/inspiration-context-repository-design.md` 记录 Inspiration Git manifest、运行时投影、同步、审核和渐进读取推荐方案。
- `docs/research/acp-codex-runtime-liveness-and-session-recovery.md` 记录固定 ACP/Go SDK/codex-acp/Codex 版本的 capability、progress、cancel、load/resume、未知字段、活性和 Docker spike 证据。
- `docs/research/execution-budget-defaults.md` 仅保留已被 ADR 0055 取代的分层预算调研历史，不是 P0 实现基线。
- `docs/domains/` 下按领域维护 Assistant、Worker、Evolution、Platform 等细节。
- `docs/adr/` 下记录关键架构决策。
- 新增核心协议时，优先补充 schema、字段含义、版本策略和兼容性说明。
- 修改模块边界时，更新本文档，避免未来 Agent 延续旧假设。
- 不要让文档替代源码验证；文档描述必须能被测试、配置或运行行为支撑。

## OpenSpec 与项目级 Skills

- 非平凡能力变更优先走 OpenSpec：`proposal -> design -> specs -> tasks -> apply/archive`。
- 当前项目级 OpenSpec 技能位于 `.codex/skills/openspec-*`。
- 当前项目级通用技能位于 `.agents/skills/`，包括 Superpowers、Matt Pocock skills、Mermaid 文档技能。
- 使用 `domain-modeling` 思路维护 `CONTEXT.md`，使用 `grill-with-docs` 思路梳理未决问题，使用 `codebase-design` 思路设计模块骨架。
- Mermaid 图应优先写入 Markdown 文档并保持可版本化；复杂图拆分，不要在一个图里表达所有系统细节。
- 不要盲从 skills 文档。技能只是工作方法，真实源码、测试、运行状态和用户最新要求优先。

## 禁止事项

- 不要把 Assistant 写成重型执行器。
- 不要让 Worker 绕过权限直接访问用户或组织数据。
- 不要把模型输出未经校验地写成长期 Truth。
- 不要在 Gateway 中执行长任务。
- 不要让事件缺少版本、追踪字段和幂等策略。
- 不要把 Agent、LLMProxy、Skill Market、OpenViking 等具体实现硬编码进领域层。
- 不要回滚用户未提交改动，除非用户明确要求。
