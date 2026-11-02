# Switchback / Switchback Domain Context

本文是项目级领域词汇表，用于统一需求交互、OpenSpec、DDD 设计和后续代码命名。它只定义领域语言，不描述具体实现细节。

## Core Product Terms

| Term | Definition |
| --- | --- |
| Switchback | 公司内部云端端到端需求交付平台。 |
| Switchback | Switchback 的 Web 产品形态或前端体验名称，具体命名边界待确认。 |
| Requirement Delivery | 从自然语言需求到澄清、任务规格、执行、产物、反馈、知识沉淀的闭环。 |
| Tenant | 权限、数据、配置、审计和成本统计的组织边界。 |
| SingleUserLocalProfile | M1 本机单用户部署模式；服务端配置固定 tenant/actor，默认仅监听 loopback，不提供登录、SSO 或动态 Membership，但保留完整 tenant/user 字段和可替换授权端口。 |
| User | 平台使用者，可通过 Web、飞书或内部 API 触发任务。 |
| TenantMembership | User 进入某个 Tenant 的显式成员关系；跨 Tenant 身份不能由 Workspace、Project 或外部凭证隐式推导。 |
| WorkspaceMembership | User 在单个 Workspace 中持有的一组显式角色；它只授权 Switchback Workspace 内动作，不授予外部平台凭证。 |
| WorkspaceRole | WorkspaceMembership 中的可组合角色；P0 包含 viewer、editor、reviewer 和 owner。 |
| AuthorizationDecision | Identity & Access 针对主体、Tenant、Workspace、动作、资源和当前策略生成的可审计 allow/deny 裁决。 |
| ExecutionAuthorizationLease | 由 AuthorizationDecision 签发、绑定用户与 WorkerSession 的短期执行授权；撤权后不能续期，也不能扩展到未授权动作。 |
| Project | 一组代码仓库、文档、技能、权限和交付记录的业务边界。 |

## Interaction Terms

| Term | Definition |
| --- | --- |
| Gateway | 同步命令与关键元信息查询入口，负责鉴权、接收操作请求、查询读模型和返回可追踪 id。 |
| Frontier | 面向 Frontend 的实时输出与展示读模型层，负责 SSE 事件流、消息/过程结果/最终产物投影，不拥有执行逻辑。 |
| Frontend | Web 用户界面，负责消费 Frontier 投影并渲染会话、执行过程和最终结果，不持有业务事实。 |
| Conversation | 用户或 provider channel 与 Assistant 围绕一个目标或上下文展开的对话容器；scope 为 `user` 或 `channel`，Workspace boundary rollover 会产生 successor generation。 |
| ConversationScope | Conversation 的交互主体范围；P0 为单用户 `user` 或群话题共享 `channel`。 |
| ConversationParticipant | channel-scoped Conversation 中一个真实 User 的版本化参与关系；角色为 `participant` 或 `controller`。 |
| ConversationController | 可管理共享 Conversation Workspace context、rollover、生命周期和 controller 委派的参与者角色；它不授予 Workspace、Task、Worker、审批、Artifact 或群披露权限。 |
| ConversationParticipationPolicy | 决定 provider 成员参与资格、controller 创建/恢复和撤权 fencing 的版本化策略；provider membership 与 WorkspaceMembership 都不自动产生 controller。 |
| Turn | Conversation 中的一轮用户、Assistant、系统或工具消息。 |
| Assistant | 负责澄清需求、装配上下文、生成 TaskSpec、派发任务和总结结果的轻量 Agent；它可以自主使用授权 Skill 读取 TaskSpec 引用的外部资料。 |
| TurnWindow | Assistant 用于短期上下文和阶段性归档的一段连续 Turns。 |
| TaskDraft | Assistant、Delivery 或 API 在 Task 创建前维护的版本化草稿；每条 draft lineage 有一个不可变 initiator，其他用户不能原地接管或代为确认。 |
| TaskDraftInitiator | 创建某条 TaskDraft lineage 并对其承担 Task 创建确认责任的真实 User；协作补充内容和 Conversation controller 身份不会改变 initiator。 |
| TaskDraftAdoption | 另一个 User 显式把当前可见 TaskDraft fork 为自己发起的新 TaskDraft lineage；它不转移原草稿所有权，也不继承原用户的创建意图、权限、审批或私有证据。 |
| TaskReadinessPolicy | 平台或组织对任务可执行维度、硬阻塞类别、允许默认值和澄清检查点预算的版本化规则。 |
| TaskReadinessAssessment | Assistant、Delivery 或 API 对 TaskDraft 各可执行维度的结构化判断快照；记录已知、假设、硬阻塞和证据。 |
| TaskCreationTriggerPolicy | 按 Conversation/channel/API scope 定义哪些已认证交互面可以生成 ExplicitTaskCreationIntent 的版本化规则；它不判断 readiness，也不授予 Workspace 或执行权限。 |
| ClarificationCheckpoint | Assistant 连续提出三次澄清问题后必须展示的 TaskDraft 决策点；用户可接受允许的假设、继续澄清或取消。 |
| WorkspaceContextResolution | 用户为 Conversation 显式选择默认 Workspace 的版本化决定，可由 ChannelThreadBinding 引用；它可以为空或失效，不授予 Workspace 权限，也不迁移已有资源。 |
| ConversationRollover | 已解析 Workspace 边界发生切换、清除或撤权时，由 Interaction 原子创建 successor Conversation、冻结 predecessor 并推进 ChannelThreadBinding 的版本化操作。 |
| ConversationHandoff | ConversationRollover 产生的不可变、结构化、带 scope/来源/策略的上下文交接；默认只含 Workspace-neutral 内容，受保护的跨 Workspace 内容需要独立显式授权。 |
| WorkspaceChannelShareGrant | Identity & Access 拥有的 Workspace 到 provider 群 audience 的显式披露授权；只允许按 profile 发布 `channel_shareable` 投影，不授予 Membership、执行或卡片动作权限。 |
| ChannelDisclosureDecision | 每次群卡片发送/更新前，对 grant revision、provider audience snapshot、source classification 和 policy 作出的可审计字段级允许/降级裁决。 |
| ChannelDisclosureProfile | Workspace owner 在平台硬限制内选择的群披露档位；P0 为 `workspace_summary`，缺少有效 Grant 时降级为 `minimal_notice`。 |
| ProviderChannelSecuritySnapshot | Delivery adapter 对 provider group identity、tenant、公开/外部状态、bot 可见性和 audience baseline 的版本化观测；它是披露裁决证据，不是 Workspace 授权。 |

## Task And Worker Terms

| Term | Definition |
| --- | --- |
| TaskSpec | Assistant、Delivery 或 API 提交给任务编排层的结构化任务规格。 |
| Task | 轻量的可跟踪交付记录；保存不可变 TaskSpec snapshot、发起人、Workspace/目标 WorkerSession、当前 WorkerRun、结果 Artifact、验收模式、状态和 revision，不是另一个重型工作流引擎。处于 `queued` 不表示已经取得 WorkerSession 执行槽。 |
| TaskInitiator | 其显式创建意图产生该 Task 的真实 User；P0 中可直接交互、验收、要求返工和取消，但不等于唯一可见者、Conversation controller 或 Workspace owner。 |
| TaskAcceptanceMode | Task 的 `auto` 或 `manual` 验收方式；report-only 且证据可确定时可自动验收，patch 默认人工验收。 |
| TaskTransition | Task 状态与 revision 推进的不可变审计事实；它与 Task/Outbox 同事务写入，但不构成第二套权威状态机。 |
| TaskSessionBinding | Task 当前可执行 WorkerSession 的唯一关联；可以为空，Session absolute expiry 会释放它而不终结 Task。 |
| TaskSessionLineageEntry | Task 与一个历史 WorkerSession/WorkerRun 的不可变关联记录；说明绑定、释放、checkpoint 和 continuation 来源，不授予旧 Session 执行能力。 |
| WorkerSessionExpiryOutcome | WorkerSession 达到绝对寿命后产生的不可变收敛事实；描述终态 Run、latest proven checkpoint、Artifact 完整性、fencing 与外部副作用不确定性。 |
| ScheduledCommand | 对 retry、deadline、reminder 或 reconciliation 的持久到期请求；到期只提交幂等应用命令，不直接代表或执行副作用。 |
| ExplicitTaskCreationIntent | TaskDraftInitiator 通过 TaskCreationTriggerPolicy 允许的交互面明确要求创建可跟踪 Task 的不可变证据；它绑定 exact actor/TaskDraft/TaskSpec/readiness/Workspace revision，模型推断、他人确认、建议文本、readiness 和对话范围扩大都不构成该意图。 |
| Worker | 接收 TaskSpec 并在受控运行时中装载真实 Coding Agent、Skill、MCP 的执行模块。 |
| WorkerSession | 用户可单独打开并像 Coding Agent TUI 一样持续交互的执行会话；每个 WorkerSession 绑定一个 Workspace，一个 Workspace 可承载一个或多个 WorkerSession。P0 同一时刻最多绑定一个 Task 和一个顶层 WorkerRun。 |
| WorkerSessionTargetIntent | 用户明确要求把新工作放入某个精确 WorkerSession 的不可变证据；Workspace 相同、最近活动、唯一候选和模型推断都不构成该意图。 |
| WorkerSessionContextPolicy | WorkerRun 启动上下文的已解析语义；`inherit_current` 继承目标 Session 当前状态，`fresh_session` 使用新 Session，`exact_base` 要求精确 WorkspaceRevision 或 checkpoint。 |
| WorkerSessionContinuityQuota | Tenant 内某个 User 可同时保留的非终态 WorkerSession 连续性槽位上限；它衡量可恢复 WorkingCopy/Agent 交互承诺，不等同于正在运行的容器数。 |
| LiveWorkerState | 非终态 WorkerSession 持续保存在 WorkspaceStorage 中的可变 WorkingCopy、Agent state 和 Trace/control cursor；用于同一 Session 的 Runtime generation 恢复，不是终态续作使用的不可变 checkpoint。 |
| WorkerSessionContinuation | 用户从终态 WorkerSession 的精确 durable checkpoint 显式创建新 WorkerSession 的派生操作；它保留来源链路但不复活旧 identity，也不继承旧 Session 权限或 AgentSession。 |
| WorkerSessionContinuationCheckpoint | 终态 WorkerSession 可供派生续作的不可变 WorkingCopy checkpoint；P0 在语义边界 best-effort 生成，必须带 capture time、digest 和 coverage，不能被续作过程原地修改，也不承诺数值 RPO。 |
| SessionAllocationQueue | 某个 Tenant/User 在连续性配额满时等待新 WorkerSession 的有序分配请求集合；请求可来自 Task 或 WorkerSessionContinuation，它发生在 Session 分配前，与 SessionTaskQueue 不同。 |
| WorkerSessionExecutionSlot | WorkerSession 对顶层执行的唯一串行化槽位；记录可选 `bound_task_id`、`active_worker_run_id` 和 session revision，由 Worker Runtime fencing，不能由 Task 队列或 EventCenter 直接改写。 |
| SessionTaskQueue | 同一 WorkerSession 上按接受顺序等待 execution slot 的有序 Task 队列；P0 中较早排队的 Task 不会被较晚 Task 或 standalone prompt 越过。它不等于 AgentTurn 的 `queue` 消息模式。 |
| AgentTurn | 用户直接在 Worker 详情页与具体 Agent 发生的一轮交互；投递模式可以是 `prompt`、`queue`、`interrupt` 或 `steer`，不经过 Assistant 重新澄清。其中 `queue` 只表示向当前唯一 active WorkerRun 排队消息。 |
| SessionProcess | WorkerSession 中由 Worker 显式管理、生命周期独立于来源 AgentTurn 终态的后台进程；它是从属于 Session/Runtime 边界的会话资源，不是新的 AgentTurn、WorkerRun 或独立权限主体。 |
| SessionProcessPreviewAccess | Gateway 为 exact running SessionProcess declared port 签发的临时访问引用；Backend PreviewProxy 据此转发 HTTP/WebSocket，但不发布 Worker host port，也不接受任意代理目标。 |
| WorkerRun | WorkerSession 中一次有开始和阶段性结果的执行窗口，可由 TaskSpec、直接 AgentTurn 或 Task 返工触发，并可包含多个 AgentTurn；Task 每次重新执行直接创建新 WorkerRun，运行时恢复只更换 RuntimeInstance generation，不创建新 WorkerRun。 |
| WorkerRunStartSnapshot | WorkerRun 在首次 Agent/Tool/WorkingCopy 动作前记录的不可变实际启动基线；描述 WorkerSession、AgentSession、WorkingCopy checkpoint、base WorkspaceRevision、dirty state 和继承策略。 |
| StandaloneWorkerRun | 不属于任何 Task 的直接交互 WorkerRun；可以引用来源 Task/Run 解释上下文，但该引用不会改变历史归属。 |
| ExecutionPolicy | TaskSpec 对执行自主性、审批、可用能力和副作用边界的结构化约定；不携带可选时长，所有 AgentTurn 统一受平台固定硬超时。 |
| AgentTurnHardDeadline | 每个顶层 AgentTurn 从后端 Worker Runtime owner 事务接受 `StartAgentTurn` dispatch 起独立计算的固定 24 小时 wall-clock 终止点；Shim/ACP 实际收到命令可能稍晚，审批等待、`active_quiet`、断线和 Runtime recovery 均不暂停，也不能延期。 |
| ToolEffectClass | Tool invocation 的副作用类别；至少区分 `read_only`、`idempotent_write`、`non_idempotent_write` 和 `unknown`，用于决定取消不确定时能否继续或重试。 |
| ToolCapabilityDeclaration | Tool 提供方对副作用、取消、幂等和核查能力的版本化声明；它是裁决输入而不是最终授权事实。 |
| ResolvedToolPolicy | 平台根据声明、Adapter 证据、组织策略和任务限制生成的不可变 Tool 执行裁决；Worker 只能执行该裁决允许的范围。 |
| ToolCancellationOutcome | Tool 取消的终态证据；可为 `cancelled_confirmed`、`cancel_unconfirmed_local`、`cancel_unconfirmed_remote_safe` 或 `cancel_unconfirmed_external`。 |
| TaintedRuntime | 因本地 Tool/子进程无法证明停止而不再允许派发新命令的 RuntimeInstance；它必须被 fencing 并进入 RuntimeRecoveryIncident。 |
| LateToolResult | Tool invocation 进入终态后才到达的结果；只作为 Trace 证据保留，不能改写 Tool/AgentTurn/WorkerRun 状态。 |
| AgentSession | 某个具体 Coding Agent 在 WorkerSession 内部维护的显式会话；一个 WorkerSession 可包含多个 AgentSession。子 Agent 只有在 adapter 明确暴露并持久化独立 session identity 时才提升为 AgentSession，否则是 AgentExecutionSpan/Trace。 |
| AgentActivityState | Agent 操作的活动状态，与 RuntimeHealthState 分离；`active_quiet` 表示 prompt 未结束、runtime/process 健康但近期无结构化进度，不表示 Agent 故障。 |
| RuntimeInstance | WorkerSession 在某一代实际运行的容器或 Pod 实例；同一时刻最多一个 active instance，崩溃重建会产生新 generation。 |
| RuntimeLease | 对隔离执行环境、工作区、密钥、网络和资源配额的临时占用声明。 |
| RuntimeProvisioner | 按 CI/CD runner 模式创建、续期和回收 WorkerSession 容器的运行时提供者。 |
| RuntimeSupervisor | Worker Runtime 中拥有 RuntimeInstance 期望状态、生命周期、健康检查、恢复和清理协调职责的逻辑组件；该术语不表示它必须是独立进程、服务或容器。 |
| RuntimeNetworkPolicy | RuntimeInstance 的服务端已解析网络能力证据；当前 SingleUserLocalProfile 使用 `trusted_full_network_v1` 允许公网、私网、VPN、LAN 与宿主路由直通，并以 id/revision/digest/enforcement status 绑定 TaskSpec、Runtime、StartSnapshot 和 Trace。 |
| RuntimeResidencyPolicy | WorkerSession 在没有 active AgentTurn 时是否继续保有运行中 RuntimeInstance 的生命周期策略；它不授予 Agent 自主启动新 Turn 或绕过 dispatch permit 的权限。 |
| RuntimeResourceProfile | 平台拥有的版本化 Runtime 单实例资源上限；约束 CPU、内存、PID 和容器 writable layer，Task 与 Agent 不能提交任意数值。 |
| RuntimeCapacityAdmission | 在签发 SessionDispatchPermit 前，根据平台总资源预算决定一个 AgentTurn 是否可以开始的容量裁决；等待容量不创建或启动 AgentTurn。 |
| Workspace | 可持续存在的逻辑工作上下文，保存当前工作内容、文件、Artifact、设计材料、Agent 状态和零到多个 RepositoryCheckout；它不是 RuntimeInstance，且可以在没有代码仓库时先存在。 |
| RepositoryImport | 把一个已授权外部 Repository 物化为 Workspace 中 RepositoryCheckout 和首个可读 WorkspaceRevision 的异步操作；失败 attempt 可审计、可在同一 bootstrap identity 下重试，但不得创建 Task。 |
| WorkspaceRevision | Workspace 某一时刻的不可变逻辑版本，记录 RepositoryCheckout revision、文件/Artifact manifest 和父版本。 |
| WorkingCopy | 某个 WorkerSession 基于 WorkspaceRevision 创建的隔离可写工作副本；RuntimeInstance 只挂载该副本，不直接写共享 Workspace。 |
| WorkspaceStorage | 独立于 RuntimeInstance 生命周期、承载 WorkingCopy、非权威 staging 和 WorkspaceRevision 内容引用的持久存储边界。 |
| StagingRevision | WorkspaceApply 在提交前构建的完整、不可变候选版本；只有被 active revision pointer 选中后才成为权威 WorkspaceRevision。 |
| ActiveRevisionPointer | Workspace 当前权威 WorkspaceRevision 的唯一选择；一次成功 Apply 只能整体切换该选择，不能逐组件推进。 |
| Repository | GitHub、GitLab 或内部代码平台上的外部代码仓库身份。 |
| RepositorySelectionSnapshot | Repository adapter 在事务外把用户选择的 branch/tag/commit 解析为 exact commit SHA 后签发的短期不可变选择证据；它保证基线一致性，但不代替当前 repository read authority。 |
| RepositoryCheckout | Repository 在某个 Workspace 中的受控检出或挂载；一个 Workspace 可有零到多个 RepositoryCheckout，并可在预研后新增。 |
| ChangeSet | WorkerSession 相对 base WorkspaceRevision 产生的可审查变更集合，包含多仓 patch/commit、非仓文件 manifest、ArtifactRef、校验结果和前置条件。 |
| WorkspaceWriteLease | WorkspaceApply 最终提交临界区使用的短时独占写租约；它不覆盖 WorkerRun、StagingRevision 构建，也不阻止其他 WorkerSession 并行工作。 |
| WorkspaceApply | 把完整 StagingRevision 串行提交为新 WorkspaceRevision 并切换 ActiveRevisionPointer 的操作；冲突或租约失效时不改变权威版本。 |
| Worker Runtime Shim | WorkerSession 容器内的控制进程，接收后端命令、作为 ACP Client 驱动 Coding Agent，并回传 Trace、事件和产物。 |
| WorkerControlChannel | Worker Runtime Shim 与后端之间的逻辑双向控制通道，承载命令、ACK、心跳和实时事件；语义独立于 gRPC 或 HTTPS long-poll 传输。 |
| ControlTransport | WorkerControlChannel 的网络适配器；P0 优先 `grpc_bidi`，网络/代理不兼容时回退 `https_long_poll`。 |
| ChannelEpoch | 一次有效 ControlTransport 连接世代；切换或重连时递增，用于拒绝旧通道继续提交 ACK 或事件。 |
| RuntimeRecoveryIncident | 一次连续的 Runtime/Agent 不健康故障及其单次自动恢复限制；P0 每个 incident 最多自动重建一次 RuntimeInstance，恢复稳定健康或完成持久进度 checkpoint 后关闭。 |
| RuntimeHeartbeat | Runtime Shim 独立于模型输出发送的存活与运行诊断信号；后端按接收时间和单调 sequence 判断 freshness，不用它表示 Agent 语义进度。 |
| RuntimeHealthState | Runtime 的 `healthy`、`suspect`、`unhealthy` 状态；P0 默认 15 秒心跳、45 秒 suspect、90 秒 unhealthy，恢复过程由 RuntimeRecoveryIncident 表达。 |
| Agent Adapter | 把 Switchback TaskSpec 转换为具体 Coding Agent 协议或 CLI 调用的适配层。 |
| ACP Control Channel | Worker 控制 Coding Agent 的协议通道；P0 通过 ACP 控制 Codex，而不是裸启动 Codex 并解析终端输出。 |

## Capability Terms

| Term | Definition |
| --- | --- |
| Skill | 可由 Assistant 或 Worker 加载的能力包，通常包含说明、脚本、模板或外部工具规则。 |
| MCP Server | 通过 Model Context Protocol 暴露工具、资源或 Prompt 的服务。 |
| AgentProfile | 某类 Coding Agent 的版本、模型、权限、运行参数和适配配置。 |
| CodexAgentProfile | P0 默认 AgentProfile，代表容器内运行的 Codex CLI Agent；后续可被其他 AgentProfile 替换。 |
| CapabilityBundle | 某个 Task 可以加载的一组 Skills、MCP servers、AgentProfile 和策略。 |
| CapabilityInstallGrant | 用户通过显式操作或与 Assistant 的明确对话授予的 Skill 安装许可。 |
| ApprovalGrant | 用户对受保护操作作出的可审计授权，作用域为 `once` 或 `worker_session`，并绑定具体能力、目标和约束，不能解释为会话内无限制放权。 |
| RepositoryAccessGrant | 用户或组织授予 WorkerSession 对指定 Repository 的 read/write/PR/CI-read 能力；受管 B2 在有效期内允许 Agent 自主使用 Token 范围内的非管理操作。 |
| CredentialBroker | 保管 Provider 根凭证和 refresh secret、按 RepositoryAccessGrant 签发或刷新短期 access token 的控制面能力。 |
| RepositoryCredentialLease | CredentialBroker 发给某个 WorkerSession/Repository 的短期 SecretLease；Token 对 Agent 可用，但不能包含 App 私钥、refresh token 或长期根凭证。 |
| ManagedCodePlatformCLI | 容器内由 Switchback 包装的原生 `git`/`gh`/未来 `glab` 入口，通过 CredentialBroker 获取短期 Token 并保留原生 Agent 操作体验。 |
| CodePlatformAccessProfile | RepositoryAccessGrant 的执行信任档位；P0 默认受管 B2，方案 A 表示全部远端操作由控制面执行，方案 C 表示 Agent 直连只读而写入由控制面执行。 |
| CodePlatformOperation | Agent 对外部代码平台执行的一次可追踪操作及其目标、权限快照和 Provider 结果；不包含 Token 内容。 |
| Skilling | 管理 Skill/MCP/AgentProfile 的版本、权限、启停和审计的上下文。 |

## Knowledge Terms

| Term | Definition |
| --- | --- |
| Trace | Assistant 或 Worker 的事实轨迹，以接近普通 Coding Agent 本地 session JSONL 的保真度记录模型、工具、状态、产物和错误。 |
| LangfuseExport | Trace 和模型观测数据向 Langfuse 的可观测性投影，不是 Switchback 的权威事实源。 |
| TurnObservationBasis | 一次 AgentTurn 已冻结的身份、输入、时间与结果依据；它不以模型观测成员是否齐全为成立条件，也不替代执行结果的权威记录。 |
| ModelObservationSupplement | 绑定一次已接受模型观测的不可变补充来源；早到和晚到成员使用同一规则，不修改原 Turn 的冻结事实。 |
| ObservationCoverage | 对一个明确来源集合的已接收、可安全发布与已验证投影情况的说明；它不是 AgentTurn 完成状态，也不证明未来不会再有模型回调。 |
| TraceManifest | 一个 TraceStream 已落盘分段、校验、时间范围、生命周期和可用状态的权威目录；消费者通过它定位 JSONL，而不是扫描对象存储。 |
| DataLifecyclePolicy | Trace/Artifact 按类型和 scope 定义 hot、warm、cold、quarantined 与删除行为的版本化策略。 |
| StorageTier | 数据当前的可访问层级；hot、warm、cold 表达访问成本和恢复方式，不改变内容事实。 |
| TraceAnalyticsProjection | 从事件和 Trace 增量构建的故障、成本、延迟、产物与演进效果统计读模型，不保存完整原始 payload。 |
| DataLifecycleAction | 对归档、恢复、隔离或删除操作的轻量记录，说明操作者、目标、理由和结果。 |
| Artifact | Worker 或 Assistant 产生的可引用产物，例如 patch、branch、PR、报告、测试日志、截图或原始 transcript。 |
| ArtifactAction | 用户针对 Artifact 发起的后续动作，例如预览、下载、应用到 Workspace 或发布为远端分支；代码平台写入受 RepositoryAccessGrant 和 Provider 权限约束，其他外部写入使用对应审批。 |
| Evolution | 从可审计轨迹中抽取长期知识候选、管理 Truth 生命周期，并提供 Truth 读取能力的模块。 |
| ContextCandidate | Evolution 从 Trace/Evidence 提取的统一候选信封，可进一步分类为 MemoryTruth、InstructionCandidate 或 SkillCandidate。 |
| TruthCandidate | ContextCandidate 中尚未发布或尚未审核的长期事实、偏好或知识候选。 |
| Truth | 由 Evolution + RDBMS 治理、带来源、范围、置信度、权限、不可变修订和生命周期的长期知识；active revision 会投影到 OpenViking。 |
| MemoryTruth | 低风险记忆聚合，例如稳定偏好、用户资料、已验证实体/事件或项目状态；满足确定性策略时可自动发布。 |
| MemoryPublicationPolicy | Evolution 用于判定 MemoryTruth 自动发布、人工审核或拒绝的版本化确定性规则；定义类型白名单、证据、scope、敏感性、冲突与 TTL 门槛。 |
| MemoryLibrary | 面向用户的已治理 Memory 读模型，按 scope 展示 active、disabled、expired 内容及其来源、使用、生命周期和投影状态。 |
| ReviewInbox | 面向有权 reviewer 的待审读模型，分 Memory、Procedure、Instruction 和 Skill 展示候选 diff、证据、风险、冲突与 scope 影响。 |
| ReviewBatch | 对一组 ReviewTask 发起的可审计批量决定；批量批准仅适用于同质低风险 Memory，其他高风险内容只能逐条批准。 |
| InstructionPolicy | 会改变 Agent 系统提示、行为边界或执行规则的受控内容；生成和修改必须人工审核，不作为普通 Memory 自动发布。 |
| ContentRevision | Truth、Instruction 或 Skill 内容的一次不可变修订，记录 parent、diff、hash、作者和来源。 |
| OpenVikingProjection | 已批准 active revision 在 OpenViking 中的 URI、revision、snapshot 和同步状态；是检索投影，不是治理事实源。 |
| TruthPublicationDecision | Evolution 对 TruthCandidate 作出的自动发布、请求人工审核或拒绝决定；模型可建议，确定性风险策略可否决自动发布。 |
| Inspiration | P2 experimental 的外部知识发现能力；以独立代码仓库维护知识源索引、简要内容和意图辅助上下文。它不是 P0/P1 ContextPack 或任务执行的必需依赖。 |
| InspirationEntry | Inspiration 仓库中一条可版本化记录，保存外部资料 URL、摘要、作用域、来源和意图提示。 |
| ResourceRecord | 从已合并 InspirationEntry 编译出的运行时投影，保存 source version、ACL/freshness 观测和索引状态，不是第二个声明事实源。 |
| ExternalResource | 被 Inspiration 索引的外部资料，例如飞书文档、妙记、需求文档或业务知识库条目。 |
| ContextPack | Assistant 或 Worker 在权限和 token 预算约束下，结合 Evolution Truth、Rules、Skills 及可选的 Inspiration candidates 自行编译出的上下文包；缺少 Inspiration 不影响生成。 |

## Event And Model Terms

| Term | Definition |
| --- | --- |
| IntegrationEvent | 跨模块传播的版本化事件，携带 trace、correlation、causation 和 visibility 信息。 |
| DomainEvent | 聚合内部产生的领域事实，通常通过 outbox 发布为 IntegrationEvent。 |
| Outbox | 与业务状态同事务写入的待发布事件记录。 |
| EventStream | 由 `stream_id` 标识的一组有序 IntegrationEvent；同一 stream 内用单调递增序号排序。 |
| StreamOwner | 唯一有权为某个 EventStream 分配下一 sequence 的领域上下文或聚合写入方；其他模块必须通过命令请求它产生事件。 |
| StreamSequence | StreamOwner 在业务事务中分配的流内权威顺序；只比较同一 stream，不能比较不同 stream 的业务先后。 |
| EventCausation | 使用 correlation/causation id 表达不同 EventStream 之间“由谁触发谁”的关系，不要求全局序号。 |
| TimelineProjection | 把多个 EventStream 的用户可见事件合并成前端读模型；它提供稳定展示游标，但不是业务事实源。 |
| TimelineCursor | TimelineProjection 写入时分配的 epoch + 单调位置，仅用于 SSE resume、分页和稳定追加；全量重建可换 epoch，不能替代 StreamSequence。 |
| LateTimelineEntry | 在 Timeline 已输出后才到达的旧发生时间事件；追加新 cursor 并标记 late，而不重写已发布游标。 |
| Projection | 为前端、飞书或运营台查询构建的读模型。 |
| ViewProjection | 为一个具体页面身份持续维护的完整可授权读模型；它有独立版本与浏览器恢复游标，但不是领域事实源。 |
| ProjectionSnapshot | ViewProjection 在某个已提交版本和游标上的完整自洽副本，用于页面首次加载和重新同步。 |
| ProjectionDelta | 把一个 ViewProjection 从精确 base version 推进到 result version 的可重放语义增量；它不是 raw domain event 或 JSON Patch。 |
| ViewCursor | ViewProjection 在当前 epoch 内分配的单调浏览器恢复位置；只用于 delta replay，不表示跨聚合业务顺序。 |
| ProjectionReplayWindow | Frontier 能按 ViewCursor 重放 ProjectionDelta 的有限保留范围；超出范围时客户端必须重新获取 Snapshot。 |
| ResyncRequired | Frontier 无法证明客户端可从当前 cursor/version 连续恢复时发出的控制结果；客户端丢弃受影响 View 缓存并重新查询 Snapshot。 |
| ClientViewReplica | 浏览器中某个 ViewProjection 的非权威物化副本；由 Snapshot 原子替换、由连续 typed Delta 推进，并始终携带对应 epoch/cursor/version。 |
| PendingCommand | Gateway 已接受但业务结果尚未由 Projection、命令状态或新 Snapshot 证实的客户端临时状态；超时或断线不能把它推断成成功或失败。 |
| DisplayStream | 一个 AgentTurn/Message/Activity 面向用户持续生成内容的稳定显示身份；它与 TraceStream、WorkerRun 和 SSE 连接不是同一生命周期。 |
| DisplayPart | Switchback 自有的版本化物化展示单元；以稳定 part identity/revision/ordinal 表达 `text`、`reasoning_summary`、`tool`、`activity`、`approval`、`artifact`、`source` 或 `error`，外部 Agent/UI 协议只能经 adapter 映射。 |
| DisplayMutation | Protocol adapter 产生的内部展示变更语言，用于创建、更新、终结 DisplayPart 或发出 transient overlay；它不是浏览器协议，也不能直接改变 Tool/Approval/Task 权威状态。 |
| DisplayProtocolAdapter | 把 ACP/Codex、AG-UI、AI SDK 等 source frame 的 identity/lifecycle 映射为 canonical DisplayMutation/DisplayPart，并把原始证据保留在 Trace 的反腐层。 |
| RendererRegistry | Frontend 按 `(schema_version, part_type)` 选择安全 renderer 的注册表；未知非关键类型降级为无特权动作 fallback，未知关键类型要求客户端升级。 |
| DisplayCheckpoint | DisplayStream 在某一 checkpoint version 上完整物化的用户可见 Parts；它进入 ViewProjection 并可按 ViewCursor 重放。 |
| TransientDisplayDelta | 基于指定 DisplayCheckpoint、用于低延迟渲染且不推进 ViewCursor 的短期增量；丢失或重复不能改变业务状态，后续 Checkpoint 会恢复精确内容。 |
| DisplayDurabilityPolicy | 定义语义边界、时间/字节上限、背压和 terminal checkpoint 要求的版本化策略。 |
| HomeView | 按用户和 Tenant 聚合最近 Worker、waiting_user/approval 与近期交付的轻量读模型；它只提供跳转和允许动作，不拥有这些事项的状态机。 |
| TaskSummaryView | 以 Task 为主键展示目标、轻量状态、验收、WorkerRun/产物引用和允许动作的摘要读模型；不复制 Worker chat/runtime control。 |
| WorkerDetailView | 以 WorkerSession 为主键展示直接对话、运行、活动、审批、Runtime 状态、产物和 Task 谱系的交互读模型；产品中的“Worker 页面”不表示后端 Worker 服务实例。 |
| WorkspaceOverviewView | 以 Workspace 为主键聚合资源、版本、成员、Tasks、WorkerSessions 和 Artifacts 的上下文读模型。 |
| ArtifactDetailView | 以 Artifact 为主键统一展示来源、可用性、权限和后续动作的产物读模型；Task/Worker/Workspace 复用同一 identity。 |
| EventCenter | 负责事件路由、订阅、重试、死信和投影驱动的模块。 |
| Delivery | 飞书等协作入口的通道适配上下文，负责触发任务、承载中间选择/对话和渲染结果卡片；任务状态与执行逻辑归内部上下文。 |
| DeliveryCardProjection | 从 owner events、授权后的公开 projection facts 与 durable Display 摘要派生的通道专用读模型；表达卡片摘要、注意事项、有限动作和 Web deep link，不拥有 Task/Worker/Approval 状态。 |
| CardInstance | Delivery 拥有的一次 provider 卡片实例及其 message/card identity、已应用 render revision、发送/更新/过期/对账状态；不等同于 DeliveryCardProjection 或业务实体。 |
| DeliveryActionDescriptor | DeliveryCardProjection 中对当前 actor/channel 可见的有限动作引用；回调必须重验 actor、binding、revision、权限和 owner action，descriptor 本身不授予权限。 |
| DeliveryProviderCapability | 描述某通道当前卡片版本、组件、回调时限、更新/交互窗口、限流和对账能力的版本化快照；provider 限制不作为 Switchback 业务生命周期常量。 |
| ChannelThreadBinding | 将一个 provider 会话/话题上下文关联到一个 primary Assistant Conversation 的版本化关系；普通消息默认进入该 Conversation，关联的 Task/Worker/Artifact 不构成可变执行焦点。 |
| InboundInteractionEnvelope | Delivery 对一条入站普通消息、结构化命令或卡片回调的规范化记录，保留 actor、binding revision、provider identity、幂等身份和可选显式目标。 |
| InteractionTargetRef | 一次通道交互明确指向 Assistant Conversation、Task、WorkerSession、Approval 或 Artifact action 的目标证据；related ref、最近活动和模型推断都不能替代它。 |
| LLMProxy | 统一代理 Assistant 和 Worker Agent 模型请求的模型治理层。 |
| ModelInvocation | 一次模型调用的审计记录，包含路由、成本、trace、task、用户和安全元数据。 |
