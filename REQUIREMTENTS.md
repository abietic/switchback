# Switchback / Switchback Requirements Draft

> 状态：v0.3 需求细化草案。本文基于多轮需求澄清整理；已决项按当前基线执行，未决项继续通过 grilling 收敛。
>
> 文件名按当前请求保留为 `REQUIREMTENTS.md`；若需要标准拼写，建议后续重命名为 `REQUIREMENTS.md`。

## 1. 项目定位

Switchback（Switchback）是一个面向公司内部的云端端到端需求交付平台。它的目标不是只提供一个聊天机器人，而是把公司内中台能力、研发工具链、Coding Agent、Skill/MCP、知识演进、飞书入口和交付流程整合起来，让用户可以从自然语言需求出发，完成需求澄清、任务拆解、执行派发、异步交付、过程追踪、结果沉淀和后续个性化演进。

核心判断：

- Assistant 负责理解用户、澄清任务、生成可执行任务规格，并派发给 Worker；它不应承担重型开发执行。
- Worker 负责在隔离、可追踪、可配置的运行环境中装载实际 Coding Agent、Skill 和 MCP，完成重型任务。
- 系统主链路必须是异步事件驱动：前端/飞书入口不等待长任务完成，后续状态通过 EventCenter、Gateway、Frontier 等通道回传。
- 用户长期使用后产生的偏好、业务知识和有效规则，应优先通过 Evolution 沉淀并在后续 Assistant/Worker 执行时可控加载；外部资料发现由可选 P2 Inspiration 试验，不作为核心闭环前提。
- 全链路轨迹需要可审计、可复盘、可回放；JSONL 对象存储是核心事实来源之一。

## 2. 目标用户与典型场景

### 2.1 目标用户

- 内部研发、算法、测试、运维、产品、项目管理等需要把需求转化为交付结果的员工。
- 平台管理员、Skill 维护者、Agent 能力维护者、基础设施维护者。
- 可能通过 Web 前端、飞书、自动化入口或未来 API 入口使用平台的内部系统。

### 2.2 典型场景

- 用户在 Web/飞书中描述一个研发需求，Assistant 追问关键约束后派发 Worker 完成代码改动、测试和总结。
- 用户直接在飞书中触发 Delivery，由后端创建 Worker 任务并异步回传执行进度。
- Worker 执行过程中产生长轨迹、代码变更、日志、模型调用和统计指标，系统将其写入对象存储并通过事件流回传。
- Evolution 定期或按窗口消费 Assistant / Worker 轨迹，生成用户/业务/项目相关 Truth，用于后续对话和执行上下文。
- Inspiration 维护外部知识索引，例如飞书文档、妙记、业务文档、项目资料与用户知识空间之间的映射，并提供渐进式加载入口。

## 3. 成功标准

P0 成功标准：

- 用户可以通过 Gateway/前端向 Assistant 发消息，后端立即返回接收成功，后续通过事件异步更新。
- Assistant 可以基于对话历史生成任务规格，并派发给 Worker。
- Worker 可以创建或复用用户级交互会话，在 Docker 构建的容器镜像中通过 ACP 控制 Codex 作为 P0 真实 Coding Agent，通过网络请求与后端通信，并完成对应动作而不影响宿主机。
- WorkerSession 空闲 48 小时后回收，绝对最长存活 14 天；超时前用户可以继续对话并默认恢复旧会话。
- P0 对用户承诺的任务产物是 `report` 和 `patch`，用户可围绕这些产物继续交互；branch、PR 和 CI 触发不属于默认 P0 闭环。
- Assistant 与 Worker 的 Turns/Trace 能写入对象存储 JSONL，Trace 同时导出到 Langfuse，并能通过 session id 查询。
- Trace/Artifact 通过 TraceManifest 管理 hot/warm/cold 生命周期，默认不自动物理删除；故障、成本、产物和 Evolution 效果看板从结构化增量投影读取，不扫描原始 JSONL。
- EventCenter 能统一处理 Assistant、Worker、Delivery 等模块事件，并将关键状态转发到前端或飞书入口。
- Frontier 能向 Frontend 提供实时 SSE，Frontend 能渲染 Assistant 输出、Worker 中间过程、阶段结果和最终产物。

P1 成功标准：

- Evolution 能从轨迹中总结可复用 ContextCandidate，在 RDBMS 中治理修订/审核，并把批准的 active Truth 投影到强依赖的 OpenViking。
- Assistant 在新会话中能加载相关 Truth、Rules、Skills，并显著减少重复澄清。
- Delivery 能从飞书入口稳定创建 Worker 任务，并回传进度、产物和失败原因。
- LLMProxy 能统一代理 Assistant 与 Worker 的模型请求，支持模型路由、成本统计、审计和失败降级。

P2 成功标准：

- Inspiration 能维护和演进 `INSPIRATION_INDEX.md` 或等价索引，把外部知识源映射到可加载上下文。
- Worker 执行环境具备隔离、资源限制、可观测和生命周期管理能力。
- Truth / Skill / MCP / 用户偏好加载具备权限边界、版本管理和人工干预入口。
- 平台具备端到端可观测性：任务状态、模型调用、Token/成本、事件延迟、失败率、重试链路和用户可见消息均可追踪。

## 4. 核心领域模型

建议先把以下概念作为领域实体或值对象建模，具体归属可在 DDD 设计阶段调整。

| 概念 | 含义 | 关键字段示例 |
| --- | --- | --- |
| Tenant / Org | 公司、组织或业务域边界 | tenant_id, name, policy |
| User | 平台使用者，可通过 Membership 加入多个 Tenant | user_id, profile, status |
| TenantMembership | 用户进入 Tenant 的显式关系 | tenant_membership_id, tenant_id, user_id, status, revision |
| WorkspaceMembership | 用户在 Workspace 的显式角色集合 | workspace_membership_id, workspace_id, user_id, roles, status, revision |
| AuthorizationDecision | 针对主体、动作和资源的可审计裁决 | decision_id, actor_user_id, action, resource_ref, allow, reasons, policy_revisions |
| ExecutionAuthorizationLease | Worker 代表用户执行的短期授权 | lease_id, worker_session_id, actor_user_id, allowed_actions, expires_at |
| WorkspaceChannelShareGrant | Workspace owner 对一个 provider group audience 的显式披露授权 | grant_id, workspace_id, audience_scope, delivery_scope, disclosure_profile, audience_baseline_ref, revision, status, expires_at |
| ChannelDisclosureDecision | 每次共享 channel 投影发送/更新前的字段级披露裁决 | decision_id, grant_ref, channel_binding_ref, source_projection_refs, provider_snapshot_ref, allowed_fields, profile, reasons |
| ProviderChannelSecuritySnapshot | Delivery adapter 对 provider group audience/security 状态的版本化观测 | snapshot_id, provider, chat_id, visibility, externality, bot_status, audience_digest, observed_at, valid_until |
| Conversation | 单用户或 provider channel 的 Assistant 对话容器 | conversation_id, tenant_id, scope_type, scope_ref, created_by, status, revision |
| ConversationParticipant | channel-scoped Conversation 中的真实参与者和控制角色 | conversation_id, actor_user_id, role, source, provider_membership_ref, status, revision |
| Turn | 用户/Assistant/系统的一轮消息 | turn_id, role, content, metadata, trace_uri |
| AssistantSession | Assistant 一次会话运行 | assistant_session_id, conversation_id, model_config |
| TaskSpec | Assistant、Delivery 或 API 派发的结构化任务规格 | source, workspace_id, workspace_resolution_ref, title, goal, constraints, acceptance_criteria |
| TaskReadinessPolicy | 可执行维度、硬阻塞、允许默认值和澄清预算的版本化规则 | policy_id, revision, dimensions, blocking_rules, default_catalog, checkpoint_budget |
| TaskReadinessAssessment | TaskDraft 的结构化可执行判断快照 | assessment_id, dimension_states, blockers, assumptions, evidence_refs, status |
| ClarificationCheckpoint | 每连续三次澄清问题后的用户决策点 | checkpoint_id, task_draft_id, blockers, assumptions, choices, decision |
| WorkspaceContextResolution | Conversation 的显式默认 Workspace 选择，可由 ChannelThreadBinding 引用 | resolution_id, conversation_id, channel_binding_id?, default_workspace_id, revision, actor, evidence, status |
| ConversationRollover | Workspace boundary 变化时创建 successor Conversation 并冻结 predecessor 的原子操作 | rollover_id, source_conversation_id, target_conversation_id, trigger_type, binding_ref, handoff_ref, idempotency_key |
| ConversationHandoff | Rollover 的不可变结构化交接内容 | handoff_id, source_conversation_id, target_conversation_id, mode, typed_items, source_refs, policy_ref, authorization_refs, digest |
| Task | 轻量的可跟踪交付记录 | task_id, task_spec_snapshot, initiator_user_id, workspace_id, requested_worker_session_id?, worker_session_id?, worker_session_target_intent_ref?, session_context_policy, session_allocation_sequence?, allocation_wait_reason?, session_queue_sequence?, queued_at?, current_worker_run_id, acceptance_mode, status, result_refs, revision |
| TaskInitiator | accepted creation intent 对应的不可变任务发起人 | task_id, user_id, creation_intent_ref |
| TaskTransition | Task 状态/revision 的不可变推进事实 | transition_id, task_id, from_state, to_state, expected_revision, trigger, result |
| TaskSessionLineageEntry | Task 的历史 Session/Run 绑定事实 | task_id, worker_session_id, worker_run_id?, bound_at, released_at, release_reason, checkpoint_ref? |
| ScheduledCommand | retry/deadline/reminder/reconciliation 的持久到期命令 | scheduled_command_id, task_id?, worker_run_id?, kind, due_at, expected_revision, lease, fencing_token, status |
| Workspace | 持久逻辑工作上下文，可无仓或多仓 | workspace_id, owner_id, resource_refs, repository_checkouts |
| WorkspaceRevision | Workspace 的不可变逻辑版本 | revision_id, workspace_id, parent_revision_id, component_manifest |
| RepositoryImport | Repository 到 Workspace 首个 RepositoryCheckout/Revision 的异步导入记录 | import_id, workspace_id, repository_ref, requested_base, status, attempts, result_revision_id? |
| RepositorySelectionSnapshot | 服务端解析可移动 ref 后生成的不可变短期选择证据 | snapshot_id, repository_ref, requested_ref, resolved_commit_sha, observed_at, expires_at, digest |
| WorkingCopy | WorkerSession 的隔离可写工作副本 | working_copy_id, worker_session_id, base_revision_id, status |
| WorkerSessionTargetIntent | 用户显式要求复用 exact WorkerSession 的不可变证据 | intent_id, actor_user_id, worker_session_id, workspace_id, source_ref, source_revision, idempotency_key |
| WorkerRunStartSnapshot | WorkerRun 首次执行动作前的实际上下文基线 | snapshot_id, worker_run_id, worker_session_id, context_policy, agent_session_refs, working_copy_checkpoint_id, base_revision_id, manifest_digest, dirty_state |
| WorkspaceStorage | 独立于容器生命周期的持久工作区存储 | storage_id, workspace_id, root_ref, storage_class |
| StagingRevision | Apply 提交前的完整不可变候选版本 | staging_revision_id, apply_id, base_revision_id, manifest_digest, status |
| ActiveRevisionPointer | Workspace 当前权威版本选择 | workspace_id, active_revision_id, version |
| RepositoryCheckout | Workspace 内的仓库检出或挂载 | checkout_id, workspace_id, repository_ref, revision, path |
| ChangeSet | WorkingCopy 相对 base revision 的可审查变更 | change_set_id, base_revision_id, repository_changes, file_changes, preconditions |
| WorkspaceWriteLease | Apply 临界区的短时独占写租约 | lease_id, workspace_id, holder_apply_id, fencing_token, expires_at |
| WorkspaceApply | 串行应用 ChangeSet 的操作 | apply_id, change_set_id, expected_revision_id, status, result_revision_id |
| WorkerSession | 绑定一个 Workspace 的可交互执行会话和顶层执行串行化边界 | worker_session_id, user_id, workspace_id, bound_task_id?, active_worker_run_id?, session_revision, runtime_id, idle_expires_at |
| WorkerSessionContinuityQuota | Tenant/User 非终态 Session 连续性容量 | tenant_id, user_id, policy_ref, limit, reserved_count, revision |
| WorkerSessionContinuation | 从终态 Session 的精确 checkpoint 派生新 Session 的显式请求与来源链路 | continuation_id, actor_user_id, source_worker_session_id, source_session_revision, source_checkpoint_ref, derived_worker_session_id?, status, idempotency_key |
| WorkerSessionContinuationCheckpoint | 可供派生续作的不可变 WorkingCopy 快照 | checkpoint_id, worker_session_id, captured_at, manifest_digest, coverage, warning? |
| WorkerSessionExpiryOutcome | absolute expiry 的 Worker 收敛事实 | outcome_id, worker_session_id, session_revision, worker_run_id?, run_terminal_reason?, checkpoint_ref, artifact_refs, fencing_evidence, external_effect_state, bound_task_id? |
| SessionAllocationQueue | 配额满时等待新 Session 的 Task/Continuation 分配请求 | tenant_id, user_id, allocation_request_id, request_kind, owner_ref, session_allocation_sequence, allocation_wait_reason |
| WorkerSessionExecutionSlot | Worker Runtime 管理的单顶层执行槽 | worker_session_id, bound_task_id?, active_worker_run_id?, session_revision, lease/fencing evidence |
| RuntimeInstance | WorkerSession 的一代实际容器/Pod 实例 | runtime_instance_id, worker_session_id, generation, container_id, status |
| RuntimeRecoveryIncident | 一次连续 runtime/agent 故障及其单次自动恢复限制 | recovery_incident_id, failed_runtime_id, attempt_count, replacement_runtime_id, status |
| ToolInvocationControl | 单个 Tool 的副作用、取消证据、fencing 和核查状态 | tool_invocation_id, effect_class, cancellation_mode, idempotency_key, status, outcome |
| ToolCapabilityDeclaration | Tool/Skill/MCP 提供方的版本化能力与风险声明 | declaration_id, tool_ref, effect_class, cancellation_capability, idempotency_support |
| ResolvedToolPolicy | 平台最终生成并交给 Worker 执行的 Tool 策略快照 | resolution_id, declaration_ref, evidence_ref, policy_revision, resolved_risk, restrictions |
| AgentSession | WorkerSession 内部的具体 Agent 会话 | agent_session_id, worker_session_id, parent_agent_session_id, agent_profile_id |
| AgentTurn | 用户直接与 Worker Agent 的一轮交互 | agent_turn_id, worker_session_id, mode, content, status |
| SessionProcess | Worker 显式启动并管理、允许跨 AgentTurn 存活的会话级后台进程 | session_process_id, worker_session_id, origin_agent_turn_id, runtime_process_ref, kind, command_summary, cwd, declared_ports, status, log_ref, revision |
| SessionProcessPreviewAccess | Backend 为一个运行中 SessionProcess 声明端口签发的临时受管访问引用 | preview_access_id, worker_session_id, session_process_id, runtime_generation, protocols, declared_port, audience, expires_at |
| WorkerRun | WorkerSession 中一次有阶段性结果的执行窗口 | run_id, worker_session_id, task_id?, origin_task_id?, trigger_type, status, retry_of |
| WorkerControlChannel | Shim 与后端的传输无关逻辑控制通道 | runtime_instance_id, generation, channel_epoch, transport, cursors |
| RuntimeProvisioner | CI/CD runner 风格的容器运行时提供者 | provisioner_id, runtime_type, docker_host, capacity |
| RuntimeSupervisor | 协调 RuntimeInstance 期望状态、生命周期、健康、恢复与清理的 Worker Runtime 组件 | worker_session_id, desired_state, observed_runtime_id, generation, recovery_incident_id? |
| RuntimeNetworkPolicy | RuntimeInstance 的版本化已解析网络能力与隔离证据 | policy_id, revision, digest, outbound_mode, host_gateway_mode, enforcement_status |
| RuntimeResidencyPolicy | 无 active AgentTurn 时的 Runtime 驻留策略 | policy_id, resident_session_states, stop_triggers, revision |
| RuntimeResourceProfile | 平台拥有的版本化单 Runtime 资源上限 | profile_id, cpu_limit, memory_limit, pids_limit, writable_layer_limit, revision |
| RuntimeCapacityAdmission | AgentTurn dispatch 前的本机总容量裁决 | admission_id, worker_session_id, requested_profile_id, budget_revision, decision, wait_reason? |
| RuntimeLease | 隔离执行环境租约 | runtime_id, container_id, network_policy, residency_policy, resource_profile, expires_at |
| RepositoryAccessGrant | 指定 WorkerSession 对单个 Repository 的受限委托 | grant_id, repository_id, capabilities, scope, constraints, expires_at, revision |
| RepositoryCredentialLease | 由当前 Grant 派生的短期代码平台凭证租约 | lease_id, grant_id, provider, permission_profile, runtime_generation, expires_at |
| CredentialBroker | 保管根/刷新凭证并签发、刷新、撤销短期 Repository Token 的控制面能力 | provider, credential_subject, grant_revision, token_fingerprint, expires_at |
| CodePlatformOperation | Agent 直接执行代码平台操作的可追踪事实 | operation_id, actor_id, worker_session_id, repository_id, command_kind, target_refs, provider_result |
| Skill | 可加载能力 | skill_id, version, scope, permissions |
| MCPServer | MCP 服务配置 | mcp_id, endpoint, auth_policy |
| ContextCandidate | Evolution 从 Trace 提取的 Memory/Instruction/Skill 候选信封 | candidate_id, kind, source_refs, risk_level, status |
| MemoryPublicationPolicy | Memory 自动发布、审核或拒绝的版本化确定性规则 | policy_id, revision, allowlist, evidence_rules, risk_rules, ttl_rules |
| MemoryLibrary | active/disabled/expired Memory 的用户治理读模型 | memory_id, scope, revision, lifecycle, usage, projection, actions |
| ReviewInbox | Memory/Procedure/Instruction/Skill 待审内容的 reviewer 读模型 | review_task_id, lane, diff, evidence, risk, scope, age |
| ReviewBatch | 带逐项校验和结果的批量审核命令 | batch_id, action, actor, items, item_results, status |
| Truth | Evolution + RDBMS 治理的长期知识 | truth_id, current_revision, scope, confidence, status |
| ContentRevision | Truth/Instruction/Skill 的不可变内容修订 | revision_id, parent_revision_id, content_hash, diff, author |
| OpenVikingProjection | active revision 的检索投影 | projection_id, target_uri, revision_id, snapshot_id, status |
| InspirationIndex | 外部知识源索引 | index_id, source_type, source_uri, tags, owner |
| Event | 领域事件或集成事件 | event_id, type, aggregate_id, payload, created_at |
| EventStream | 单一 owner 维护的领域事件有序流 | stream_id, owner_context, next_sequence |
| TimelineEntry | 多 stream 用户可见事件的稳定展示项 | timeline_id, cursor, source_stream, source_sequence, late |
| Artifact | Worker/Assistant 产物 | artifact_id, task_id, uri, type, checksum |
| TraceManifest | TraceStream 分段、校验、生命周期与可用状态目录 | trace_stream_id, segments, checksums, tier, availability, cursors |
| DataLifecyclePolicy | Trace/Artifact 分层与删除策略 | policy_id, revision, hot_days, warm_days, auto_delete, projection_retention |
| TraceAnalyticsProjection | 故障、成本、产物和演进效果统计读模型 | projection_id, dimension, metric, window, source_cursor |
| DataLifecycleAction | 轻量归档、恢复、隔离或删除记录 | action_id, actor, target, action, reason, result |

## 5. 模块需求

### 5.1 Gateway

职责：

- 作为后端统一入口，接收 Web/前端/内部系统请求。
- 处理 Assistant message、关键元信息查询与操作、任务查询、会话查询、产物查询、用户权限校验等同步 API。
- 对长任务只返回接收结果和可追踪 id，不阻塞等待执行完成。
- 将用户消息转化为领域命令或事件，投递到消息队列或 EventCenter。

不应承担：

- 不在 Gateway 内直接运行 Agent。
- 不在 Gateway 内做重型模型调用或长时间任务编排。

### 5.2 Assistant

职责：

- 与用户进行需求澄清和任务定义。
- 通过类似 Superpowers brainstorming 或 OpenSpec clarify 的渐进式询问判断需求是否足够明确，而不是依赖一组僵硬表单字段替代语义判断。
- 加载对话历史、Truth、Rules、用户偏好、业务上下文和可用 Skills。
- 生成结构化 Task Spec，并派发给 Worker 或 Delivery 相关流程。
- 按版本化 TaskReadinessPolicy 生成 TaskReadinessAssessment；goal、交付物/验收、scope/target、constraints、context/access、risk/approval 和 execution policy 各维度必须标记为 `known`、`assumed`、`blocking_unknown` 或 `not_applicable` 并保留证据。
- 只在 TaskSpec validator 通过且不存在 `blocking_unknown` 时派发。低风险、可逆且命中 default catalog 的缺口可成为显式 assumption；权限、冲突目标、生产/外部/不可逆副作用、关键访问缺失和不可验证目标不得默认。
- Conversation 可在 Workspace 未解析时继续 tenant/user-scope 脑暴与澄清，但不得读取 Workspace-scoped Truth/Inspiration/Repository/Trace/Artifact，也不得派发 Task 或创建/复用 WorkerSession。
- 执行前必须由用户显式选择已有授权 Workspace，或先显式创建新 Workspace 再选择。用户全局默认、唯一候选、链接、最近活动和模型推断只能用于推荐，不能形成执行授权或 TaskSpec workspace。
- TaskSpec 派发时 `workspace_id` 必填，并带 WorkspaceContextResolution/Membership evidence；Task Orchestration 只重验和快照，不得静默推断、替换或创建 Workspace。
- Assistant 每条澄清消息只问一个当前最高优先级阻塞问题。连续提出三次澄清问题后必须展示 ClarificationCheckpoint 和当前 TaskDraft；用户可接受允许的 assumptions、继续澄清、缩小范围或取消。继续澄清重置下一组三问预算，不形成会话总轮次硬上限。
- 对阶段性结果进行用户可读总结，并决定是否需要继续追问、重试或追加任务。
- 将 Turns 按窗口或阶段写入对象存储 JSONL。

边界：

- Assistant 只做轻量推理、澄清、计划和派发，不执行重型代码修改、长时间构建、测试或容器内操作。
- Assistant 生成的任务规格必须足够明确，包括目标、约束、输入、期望产物、权限、超时、可用技能、上下文来源和验收方式。
- Assistant 负责语义上的“是否可执行”判断；TaskSpec validator 负责 schema、必填字段、权限和审批策略的硬校验。

### 5.3 Worker

职责：

- 接收 Assistant / Delivery 派发的 Task Spec。
- 根据任务配置创建、显式复用或回收用户级交互 WorkerSession；复用必须有 exact WorkerSessionTargetIntent，未指明目标的新 Task 默认创建新 Session。
- 装载主流 Coding Agent、基础 Skill、MCP、模型代理配置和上下文文件。
- 通过 ACP 协议适配不同 Agent 实现。
- 记录执行轨迹、模型请求、工具调用、状态变更和产物位置。
- 将轨迹写入对象存储 JSONL，并维护 session id 与当前 Worker 的映射。

关键能力：

- 镜像构建：P0 WorkerSession 镜像通过 Docker 构建，并固定 `github.com/coder/acp-go-sdk@v0.13.5`、`@agentclientprotocol/codex-acp@1.1.2`、`@openai/codex@0.144.1`、Go Worker runtime shim、必要 CLI、Skill/MCP 基础依赖和 LiteLLM 进程；Node 依赖必须通过 lockfile 精确锁定。
- 认证注入：通用认证材料通过环境变量或挂载配置进入容器；长期由 SecretLease 管控。代码平台使用 CredentialBroker 按 RepositoryAccessGrant 签发短期 RepositoryCredentialLease，不能把模型 key、App 私钥、OAuth refresh token、长期 PAT 或内部 endpoint 写入镜像和 Trace。
- 模型代理：P0 在同一 WorkerSession 容器内侧载 LiteLLM，Codex 只访问本地 LiteLLM endpoint；LiteLLM 再按 Switchback ModelPolicy 路由上游 provider。
- ACP 控制：容器内 Go Worker runtime shim 使用社区 SDK `github.com/coder/acp-go-sdk@v0.13.5` 作为 ACP client，通过本地 stdio 控制 `@agentclientprotocol/codex-acp@1.1.2`；ACP SDK 只负责协议交互，不负责容器、Task、Trace、对象存储或后端通信。
- ACP 兼容：Shim 启动时持久化 protocol version 和 capability snapshot，只调用 Agent 已声明的可选方法。所有 ACP JSONL 帧在 typed SDK 解码前先写入有界 raw recorder；未知可选 update 保留原始负载并发出兼容性告警，涉及协议版本、权限或安全决策的未知语义则 fail closed。
- 运行环境隔离：Codex P0 Agent 必须在本地 Docker-compatible 容器中动作，不能影响宿主机；容器内 runtime shim 通过网络请求与 Switchback 后端通信，ACP 控制通道保留在容器内部。
- M1 运行时拓扑：`switchback-backend` 作为 Docker Compose 管理的常驻容器，内部包含 RuntimeSupervisor Go 模块；该模块通过只挂载给 Backend 的 Docker Socket/Engine API 创建、停止和恢复同一 Docker Engine 下的 sibling WorkerSession 容器。Worker 容器不挂载 Docker Socket、不使用 privileged 模式，也不能创建其他容器。
- 宿主路径约束：RuntimeSupervisor 只能从服务端配置的 Switchback data root 派生 WorkingCopy、agent state 和配置挂载；客户端、TaskSpec、Agent、Skill 与 Worker 都不能提交任意宿主路径。Docker Socket 赋予 Backend root-equivalent 管理能力，因此 M1 仅允许 loopback-only 受信任 Backend 持有该能力。
- M1 网络基线：SingleUserLocalProfile 固定由服务端解析为 `trusted_full_network_v1` revision 1 和 exact digest。Worker 通过命名 Docker bridge 直通公网、RFC1918/CGNAT/IPv6 私网、企业 VPN、LAN、其他可路由容器服务以及任意协议/端口；Linux 宿主开发服务通过 `host.docker.internal=host-gateway` 访问，实际可达性仍取决于宿主 Docker、路由、DNS 和 VPN 配置。
- 网络隔离边界：该 profile 不做 destination allowlist、CIDR deny、redirect 重验、payload inspection 或 per-target ApprovalGrant，也不记录目的地或 payload telemetry。它只适用于 loopback-only、受信任的单用户本地部署，不是 DLP 或多用户生产网络策略；切换到远程或多用户 profile 前必须重新设计隔离。
- 非网络隔离保持不变：Worker 不挂载 Docker Socket、不使用 privileged/host network、不接受任意宿主路径、不发布 host/public 入站端口，也不获得环境中的基础设施或代码平台根凭证。唯一受管入站仍是 Backend PreviewProxy 对当前 Runtime generation 中 exact declared SessionProcess HTTP/WebSocket port 的内部访问。TaskSpec 只保存服务端已解析的 profile id/revision/digest，不能自行选择或放宽 authority；旧 `m1_public_web_restricted_v1` 仅用于历史读取和已准入执行，运行证据标记为 `legacy_unenforced`。
- M1 驻留策略：固定使用 `m1_resident_until_session_terminal_v1`。WorkerSession 完成首次 Runtime 分配后，在 `interactive/running/waiting_user/idle` 期间始终保有一个运行中的 RuntimeInstance、Shim、Codex ACP 与 LiteLLM；AgentTurn/WorkerRun 阶段完成、进入 `waiting_user` 或普通 idle 不触发计划性 stop、pause、hibernate 或 generation 变化。
- 驻留不等于执行授权：没有 active AgentTurn 和有效 SessionDispatchPermit 时，Codex 不得自行开始 prompt、Tool 或外部动作；Runtime 只允许 heartbeat、控制通道、按 ADR 0063 显式登记的 SessionProcess 以及 Trace/Artifact drain。M1 不定义 `suspending/suspended/resuming` 状态，也不使用 `docker pause`。
- Runtime 只在显式关闭、48 小时 idle expiry、14 天 absolute expiry、授权/安全终止或故障恢复 fencing 时停止或替换。`waiting_user` 继续暂停 idle clock、但不暂停 absolute lifetime，因此一个等待中的驻留容器最长可持续到 Session 的 14 天边界；它始终占用 continuity quota 和实际宿主资源。
- M1 单实例资源固定为服务端 `local_standard_v1`：`2 vCPU`、`4 GiB` memory、`512` PIDs、`10 GiB` container writable layer。TaskSpec、Frontend、Assistant、Agent、Skill 和 MCP 都不能提交或覆盖任意资源数值；部署配置可以整体调整 profile revision，但一次 RuntimeLease 必须快照 exact profile。
- WorkingCopy、cache 和 Agent state 位于独立持久目录，不计入 container writable layer；其 quota/告警/可回收 cache 边界单独确认。CPU 超限使用 throttling；memory/PID/writable-layer 硬限制必须由 Docker/cgroup/storage adapter 执行或在不支持时 fail closed，不能静默无上限运行。
- M1 RuntimeCapacityAdmission 使用服务端配置的本机 CPU、memory 与 storage 总预算，默认不超过 Docker Engine 可用量的 `80%`。Continuity quota 10 只约束可续聊 Session 数，不代表允许 10 个 AgentTurn 同时重负载执行。SessionTaskQueue 队头或 standalone start 只有在容量裁决通过后才能取得 SessionDispatchPermit；容量不足时保持 Session resident、记录 `runtime_capacity` 并按原队列顺序等待，不关闭、降级或抢占已有 Session。
- 等待 `runtime_capacity` 不创建 active AgentTurn、不写 `started_at/deadline_at`，也不开始固定 24 小时 deadline。取得 admission 后，capacity evidence、SessionDispatchPermit 与 StartAgentTurn 的既有 revision/idempotency fencing 绑定；Turn terminal 后释放 capacity 并唤醒合法队头。CPU throttle 不是故障；明确 OOM/PID/storage hard-limit 形成 `resource_exhausted` 证据并进入 `waiting_user`，不得把已知资源不足当普通 container loss 自动重建一次后再次撞限。
- 后端控制通道：Shim 主动建立 WorkerControlChannel。P0 首选 gRPC 双向流；HTTP/2、代理或传输连通性不支持时回退 HTTPS long-poll 拉取命令和批量 POST 事件。两种传输共用 command/event id、sequence、ACK、runtime generation、channel epoch 和幂等处理。
- 数据上传：Trace JSONL segment、raw transcript 和 Artifact 大文件不走控制流，统一使用带 checksum、offset/part 和幂等键的可恢复 HTTP 上传。
- 生命周期管理：排队、启动、心跳、暂停、取消、超时、失败、重试、清理、48 小时 idle timeout、最大 14 天存活时间和交互续期。
- Runtime 心跳：P0 默认由 Shim 每 15 秒发送独立 RuntimeHeartbeat。服务端 45 秒未收到有效心跳时进入 `suspect`，暂停新命令并探测 control transport、容器和进程；90 秒仍未恢复时进入 `unhealthy` 并触发恢复。明确 process exit/container missing 可立即 unhealthy；判定基于服务端 receive time 与 heartbeat sequence，不依赖容器 wall clock。
- Agent 活性：模型或 tool 暂时无输出不代表 Agent 无响应。固定 ACP/Codex 栈没有安全 ping/no-op；Agent unresponsive 只能由 process exit、stdio EOF、ACP connection close 等硬证据，或 cancel 后超过 grace 仍无法得到 `session/prompt` 终态判断。runtime 心跳在长模型推理和长 tool call 期间仍必须持续。
- 静默处理：`active_quiet` 只产生非终止告警，不因无输出提前取消；当前 AgentTurn 仍继续消耗固定 24 小时 wall-clock hard deadline。
- Turn 固定超时：每个顶层 AgentTurn 在后端 Worker Runtime owner 事务接受 `StartAgentTurn` dispatch 时记录服务端 `started_at` 与 `deadline_at=started_at+86400s`；Shim/ACP 实际调用时间只作为 Trace 观测。审批等待、`active_quiet`、断线、平台故障和 Runtime recovery 均不暂停，也不允许延期或增加时间。
- 双重执行：后端 ScheduledCommand 是 AgentTurn deadline 的唯一状态、审计和 fencing owner；容器 Shim 为同一 Turn revision/runtime generation 启动不可重置的 monotonic watchdog。任一路径都提交同一幂等取消 identity，断线期间本地 watchdog 仍须停止执行，重连后以后端终态证据收敛。Shim 可以保守地提前停止，但不能延长或独立改写 deadline；ACP prompt 在两侧 enforcement 都 armed 前不得启动。
- 单向启动：后端在一个事务中预提交 AgentTurn `started_at/deadline_at`、ScheduledCommand 和 `StartAgentTurn` Outbox，随后单向 dispatch，不增加 RuntimeReady/StartPermit 往返。命令绑定 exact Turn revision、Runtime generation、ChannelEpoch、deadline 和 cancellation identity。Shim 校验 freshness，按后端 deadline 与控制通道 server-time uncertainty 计算保守剩余时长，先 arm monotonic watchdog 再启动 ACP；stale/expired/时间无法证明时拒绝，重投不能重置 watchdog 或重复 prompt。运输延迟和 uncertainty margin 计入 24 小时窗口。
- Turn 独立性：`steer` 不重置当前 Turn deadline，`interrupt` 终结当前 Turn；显式 queued user message 只能在当前 Turn 终态后启动新的独立 Turn。Agent 输出、自动重试和 Runtime recovery 不能自行创建顶层 Turn。
- 超时收敛：deadline 到期后 Worker 请求 ACP cancel 并停止当前 Tool/process，AgentTurn 终态固定为 `timed_out`。取消确认后 Task-bound WorkerRun/Task 进入 `waiting_user:agent_turn_timed_out`，用户可在同一 live Session/Run 发起新 Turn；取消不确定时沿用 tainted runtime、fencing 和副作用 reconciliation。
- 进程归属：普通 shell/Tool 子进程默认属于发起它的 AgentTurn process group/cgroup，Turn 终态时必须停止；`&`、`nohup`、daemonize、关闭 stdio 或脱离父进程都不能把它隐式提升为会话级进程。
- 受管后台进程：preview server、dev server、file watcher 等只有通过 Worker 的显式 managed-process 接口启动并成功登记后才成为 `SessionProcess`。登记至少记录 source Turn/Tool、runtime generation、process handle、kind、command 摘要、cwd、声明端口、启动时间、状态和有界日志引用；不允许在 Turn terminal 后追认未知 orphan process。
- 权限与执行边界：`SessionProcess` 继续受当前 RuntimeResourceProfile、RuntimeNetworkPolicy、WorkingCopy mount、ResolvedToolPolicy 和 ApprovalGrant 约束，不获得额外 Secret、网络或宿主能力，也不能提交 ACP prompt、创建顶层 AgentTurn/WorkerRun 或把任意 batch Agent 工作伪装成后台服务。其持续资源占用必须计入 Runtime aggregate capacity 观测/准入。
- 可见性与控制：WorkerDetail 必须列出 active/history SessionProcess、来源 Turn、端口、状态、资源/日志引用和允许的 stop/restart 动作。日志按有界分段/轮转上传 Trace，不能用无界 stdout 占满 writable layer 或 SSE。
- 预览访问：用户对运行中 `preview_server|dev_server` 提交 `OpenSessionProcessPreview` 时，Gateway 必须重验当前 actor、WorkerSession、exact SessionProcess revision/runtime generation 和已登记端口，并返回短期 `SessionProcessPreviewAccess`。该引用只能解析到 RuntimeSupervisor 提供的当前容器 endpoint 与声明端口，不能接受客户端 URL、IP、host、任意端口或 `CONNECT` 目标。
- 代理数据面：P0 只代理 HTTP/1.1 与 WebSocket。浏览器到 SessionProcess 的请求/响应字节由 Backend PreviewProxy 直接转发，不进入 EventCenter、SSE、Trace JSONL 或对象存储；Lifecycle/访问裁决/错误摘要仍产生事件与审计记录。代理必须移除 Switchback Cookie、Authorization、内部身份和 hop-by-hop headers，不能把控制面凭证传给 Worker 生成的应用。
- 端口边界：SessionProcess 只监听容器网络中的已声明端口，不创建 Docker host-port mapping。RuntimeSupervisor 向 PreviewProxy 解析 exact runtime generation 的内部 endpoint；其他客户端、Worker 或 Agent 看不到可复用的容器地址。Process stop/fail/lost、WorkerSession 终态、Runtime fencing/replacement、权限撤销或 access expiry 必须拒绝新请求并关闭活跃 WebSocket/长连接。
- 交互与空闲：显式 `OpenSessionProcessPreview` 可作为一次用户交互按既有规则刷新 idle；后续页面资源、轮询、health check、HMR/WebSocket 或进程自身流量均不刷新 48 小时 idle，避免预览页面无限延长 Session。
- Turn 终态独立：一旦 managed start 在 source AgentTurn 终态前按 exact Turn revision/runtime generation 被 Worker 接受，SessionProcess 就独立于该 Turn 的后续终态。`completed/cancelled/timed_out/failed` 均不得自动停止或回滚已经登记的 SessionProcess；`interrupt` 作为控制动作收敛为 `cancelled` + `terminal_reason=user_interrupt`。Turn cancel/deadline 仍停止 ACP prompt、当前 Tool 和所有普通 descendants。并发竞态按 owner 提交顺序收敛：start acceptance 先提交则 process 保留，Turn terminal 先提交则拒绝新 start。
- 清理与恢复：WorkerSession close/idle expiry/absolute expiry、授权/安全终止必须停止全部 SessionProcess，这些边界高于 Turn survival。SessionProcess 的 stdout、日志、端口流量、健康检查或状态更新不属于有效用户交互，不刷新 48 小时 idle clock。RuntimeInstance 被 fencing/replaced 或因 taint/resource hard limit 无法继续时，原 generation 中仍在运行的 SessionProcess 收敛为 `stopped` 或 `lost`，不得自动重启；用户或后续 AgentTurn 可显式重新启动并产生新的 process identity。
- 无预算模型：WorkerRun、Task、Tool 和 continuation 不维护 active budget、累计时长、时间 Grant 或可选 profile。TaskSpec 不携带用户可选时长；Tool 继承当前 AgentTurn 剩余 deadline。网络、模型请求和进程退出可以有基础设施超时，但不进入领域授权。
- 成本边界：时间限制不等于模型成本限制。LLMProxy 可独立执行 tenant/user/model 级 rate、token 和 cost guard，不把它表达为 AgentTurn 延期。
- Tool 取消升级：本地 Tool 在终止进程组/cgroup 后仍无法证明停止，则 runtime 进入 tainted、暂停新命令并以 `tool_cancel_unconfirmed_local` 进入一次 RuntimeRecoveryIncident。远端 read-only 或具有稳定 idempotency/reconciliation 证据的 Tool 可仅 fencing invocation；non-idempotent/unknown 外部副作用不确定时禁止自动重试，WorkerRun 进入 `waiting_user` 等待核查、确认、补偿或重试。
- 迟到结果：每个 Tool 必须有 `tool_invocation_id` 和终态 fencing。终态后到达的 progress/result 只写 `late_tool_result` Trace，不能改写任何业务状态。
- Tool 元数据权威：Tool/Skill/MCP manifest 提供版本化 ToolCapabilityDeclaration；Adapter 验证并标准化 cancellation、idempotency 和 reconciliation 技术证据，只能提高风险，不能自行降低声明风险；Capability/Skilling 的版本化 ToolGovernancePolicy 生成最终 ResolvedToolPolicy。TaskSpec、Approval 和租户策略只能继续收紧，Worker 只负责校验并执行快照，Agent/模型输出不是裁决来源。
- 保守降级：声明缺失、来源不可信、声明与 Adapter 证据冲突、证据不可验证或 policy revision 不可用时，`effect_class` 必须解析为 `unknown`，不可验证的 cancellation 必须解析为 `unsupported`，禁止自动重试并按最高风险路径审批或核查。可信运行时证据可以升级单次 invocation 风险，但不能降低已解析风险。
- 故障恢复：进程退出、容器丢失、心跳超时或 Agent 无响应时创建 RuntimeRecoveryIncident；P0 每个连续 incident 最多自动重建一次 RuntimeInstance。重建复用 WorkerSession、Workspace、WorkingCopy、已持久化 Agent state 和 Trace cursor，递增 generation 并 fencing 旧 runtime/channel；恢复再次失败或 ACP session 无法安全 resume 时进入 `waiting_user`。
- 写隔离恢复：创建 replacement 前必须终止或隔离旧 container、撤销旧 control/secret lease，并取得新的独占 WorkingCopy mount lease；无法证明旧 generation 已失去写权限时停止自动恢复并进入 `waiting_user`。
- 恢复权限：新 generation 重新签发 SecretLease；仍在有效期且 capability/target/constraint 匹配的 WorkerSession ApprovalGrant 可继续使用。用户取消、idle/absolute timeout、鉴权、授权、策略、schema 和 protocol 错误不触发自动恢复。
- 容量治理：P0 按 `(tenant_id, user_id)` 应用 WorkerSessionContinuityQuota，默认上限 10。`allocating/interactive/running/recovering/waiting_user/idle` 占用槽位，`lost` 在 reconciliation 前占用，`closed/expired` 终态释放；queued/unassigned Task 与 pending WorkerSessionContinuation 不计数。
- 满额行为：合法新 Session 请求记录 Tenant/User 单调 `session_allocation_sequence`。Task 保留 `queued` 且不分配 Session，WorkerSessionContinuation 保持 pending allocation。平台禁止因容量压力自动复用、reset、close 或 evict Session；用户可显式复用 exact live Session、关闭可终止 Session 或取消请求。
- 分配顺序：SessionAllocationQueue 与 SessionTaskQueue 分离。前者统一排序 Task/Continuation 的新 Session 请求；容量恢复后只处理最早合法 allocation sequence。Worker Runtime 原子预留 quota slot 并创建 `allocating` Session，终态释放与 quota revision 同事务，丢失事件由 reconciliation 修复。
- 终态语义：`closed/expired` WorkerSession 不可重开、不可重新绑定 Runtime 或接受 AgentTurn/WorkerRun。详情页保留权限过滤后的只读 Timeline、Trace、Artifact 和 durable checkpoint。
- 派生续作：用户必须显式选择 exact source Session revision 与 durable checkpoint，通过 `CreateWorkerSessionContinuation` 创建新 `worker_session_id`、WorkingCopy 和 AgentSession；新 Session 记录来源 lineage，并重新开始 48 小时 idle 与 14 天 absolute lifetime。
- 继承边界：派生续作不继承旧 Runtime generation、ChannelEpoch、queue/dispatch state、ApprovalGrant、RepositoryAccessGrant、SecretLease 或其他 Session-scoped authority。显式选择的 ContextPack/Artifact refs 也必须重新鉴权；默认只从 checkpoint 物化 WorkingCopy。
- checkpoint 完整性：graceful close 应在终态前准备 final durable checkpoint；若 expiry/failure 无法得到最终快照，只允许使用最新已证明的 durable checkpoint，并向用户展示 capture time、digest 和 coverage warning。续作只读 source、创建隔离副本，不原地修改终态 Session。
- checkpoint 承诺：P0 采用 best-effort semantic checkpoint，不承诺数值 RPO。非终态 WorkingCopy、Agent state 和 Trace/control cursor 作为 LiveWorkerState 持续保存在 WorkspaceStorage，普通 Runtime generation 恢复直接复用；不可变 continuation checkpoint 在 AgentTurn terminal、进入 `waiting_user`、阶段结果和 graceful close 时尝试生成。强制 host/storage failure 可能只保留 latest proven checkpoint，不能把未 checkpoint 的 live state 声称为已恢复。
- absolute expiry fencing：14 天到期先停止新 command/lease、请求 ACP/Tool cancel、撤销 control/secret lease 并证明旧 Runtime 失去 WorkingCopy 写权限。无法证明时 Session 保持 `lost` 且继续占 quota，禁止派生可写 continuation；确认 fencing 后才提交 `expired` 与 quota release。
- Run 收敛：到期时 active WorkerRun 进入不可逆 `timed_out:worker_session_absolute_expired`，不得迁移到新 Session。late Agent/Tool result 只进入 Trace；已持久化 report/patch 保留 source Run/checkpoint provenance，未达到 delivery boundary 的产物标记 partial，不能单独触发 auto acceptance。
- Task 收敛：Task 当前 `worker_session_id` 表示可执行 Session，另有有序 `worker_session_lineage`。expiry 清空 current Session/Run 并追加 lineage；queued/running/waiting_user Task 进入或保持 `waiting_user:worker_session_expired`，delivered Task 保持 delivered，terminal Task 不变。
- 同 Task 续作：只有 TaskInitiator 可用 exact Task revision、expiry outcome、checkpoint 与 idempotency key 执行 `ContinueTaskInNewWorker`。Task 原子转为 `queued` 并写 Outbox allocation request；新 Session 分配和正常 dispatch 成功后设置 current Session、创建新 WorkerRun 并进入 `running`。旧 Run、Session authority、Grant 和 Secret 不迁移。
- 副作用阻塞：若 expiry outcome 表示 non-idempotent/unknown 外部效果未核查，continuation/retry 必须先完成 reconciliation；不能因 Session 到期自动重放外部操作。Workspace owner 仍只有 emergency cancel。
- Agent 适配：P0 Worker 通过 ACP 控制 Codex，而不是裸启动 Codex 并解析终端输出；统一 ACP 输入输出，不把业务逻辑绑死在某个 Coding Agent。
- Session 映射：`worker_session_id` 在首次启动时固化；同一时刻最多绑定一个 active RuntimeInstance/container。崩溃恢复复用原 `worker_session_id`，递增 runtime generation 并记录新 container id；历史上 WorkerSession 与 RuntimeInstance、Codex/Agent session 都是一对多。
- Workspace 映射：每个 WorkerSession 绑定一个 Workspace，一个 Workspace 可承载一个或多个 WorkerSession；Workspace 可以没有代码仓库，也可以包含多个 RepositoryCheckout，并能在预研或产物生成后再创建或绑定仓库。Workspace 是持久逻辑上下文，不等同于可回收的 RuntimeInstance。
- Workspace 写隔离：每个 WorkerSession 基于 WorkspaceRevision 创建并长期持有宿主管理的持久 WorkingCopy；代码仓库使用 per-session Git worktree/内部临时 ref，非仓文件使用独立 materialized tree 和内容 manifest。RuntimeInstance 只 bind mount 自己的 WorkingCopy，容器删除或 generation 重建不能删除副本，也不能直接写权威 Workspace。
- ChangeSet 准备：WorkerRun 从 WorkingCopy 生成带 base revision、不可变 commit ref、文件 manifest、checksum、测试结果和前置条件的 ChangeSet。WorkspaceApply 在不持有 WorkspaceWriteLease 时构建完整、不可变、非权威的 StagingRevision；copy-on-write/reflink 可以优化，但不能成为正确性前提。
- ChangeSet 提交：Finalize 获取 PostgreSQL lease row 分配的短时独占租约和单调 fencing token，在同一数据库事务中重验 expected active revision、token 和 staging digest，创建 WorkspaceRevision、切换 ActiveRevisionPointer、记录 Apply 结果并写 Outbox。事务内不复制大目录、不运行测试、不调用外部服务；冲突、租约过期、任一组件失败或事务回滚时 active revision 保持不变。
- Apply 恢复：提交前崩溃只留下非权威 staging 和可过期租约，由重试/janitor 收敛；提交成功但响应丢失时按 idempotency key 返回原 result revision。多仓“原子”表示一个数据库指针整体选择完整 manifest，不表示多个物理目录同时 rename。
- 直接交互：用户打开 Worker 详情页后，可以绕过 Assistant 直接发送 AgentTurn，并选择普通 prompt、queue、interrupt 或 steer；等待审批或外部操作时也从该通道恢复。
- 阶段完成：WorkerRun 完成即产生阶段性 report/patch，但不会关闭 WorkerSession。Task `acceptance_mode=manual` 时进入 `delivered`；`auto` 且验收证据满足时直接进入 `completed`。
- 返工交互：Task 处于 `queued/running/waiting_user/delivered` 时，用户对该交付的反馈继续属于同一 Task，并创建或加入下一 WorkerRun；不引入独立 TaskAttempt。
- 运行时恢复：RuntimeInstance 恢复仍属于原 WorkerRun，只递增 runtime generation；它不是返工、重试或新 Task。
- 终态后交互：Task `completed/failed/cancelled` 后的直接 AgentTurn 默认启动不带 `task_id` 的 standalone WorkerRun，可记录 `origin_task_id`。只有用户明确要求建立新的可跟踪交付时才创建新 Task。
- 显式创建意图：用户使用结构化创建命令、明确说“创建为任务”，或 Assistant/Delivery/API 显式提交 TaskSpec 时才允许创建。Agent 可以建议，但模型推断、建议文本、对话变长或范围扩大不能作为授权；P0 不要求独立 FollowUpTaskProposal 生命周期。
- 创建收敛：Gateway/Delivery 记录认证 actor 与 intent evidence；Task Orchestration 重验 TaskReadiness、TaskSpec digest、WorkspaceMembership 和当前策略，并以 idempotency key 原子创建 Task、初始 TaskTransition 和 Outbox。重复点击/消息返回原 Task；校验失败不留下半创建 Task。
- Session 复用：只有认证用户通过 Worker 详情、注册卡片/命令或 exact API 字段显式指向 `worker_session_id` 时，新 Task 才可复用该 Session。Workspace 相同、最近活动、唯一 idle Session、related ref 和模型推断都不能触发复用；无 exact target 的新 Task 默认在同一授权 Workspace 分配新 Session。
- Context 继承：显式复用固定继承该 Session 当前 WorkingCopy、Agent state/context、AgentSession 和未提交变更，禁止静默 reset/clean/stash/checkout。要求 clean/exact base 的 Task 应使用新 Session；无法分配时保持 queued/blocked 并要求显式动作。
- 启动基线：每个 WorkerRun 在首次 Agent prompt、Tool 调用或 WorkingCopy 写入前记录不可变 WorkerRunStartSnapshot，包含 resolved context policy、AgentSession refs、WorkingCopy checkpoint/manifest digest、base WorkspaceRevision、dirty state 和前序 Task/Run refs。
- Session 串行化：P0 每个 WorkerSession 同一时刻最多一个 bound Task 和一个 active 顶层 WorkerRun。Task 可在 `queued` 时引用目标 WorkerSession，但未取得 execution slot；Worker Runtime 对冲突 StartWorkerRun 返回 `session_busy`，Task Orchestration 保持排队或根据显式用户选择新建 Session。
- Slot 生命周期：Task 在 `running`、`waiting_user` 或 `delivered` 时通常保持 WorkerSession binding；`completed/failed/cancelled` 释放。唯一非终态例外是 WorkerSession absolute expiry：旧 binding 进入 TaskSessionLineage，current binding 清空，同一 Task 需 initiator 显式续作。standalone WorkerRun 只能在无 bound Task/active Run 时开始。
- Queue 术语：SessionTaskQueue 表示等待 WorkerSession execution slot 的 Task；AgentTurn `queue` 表示给当前唯一 active WorkerRun 排队消息。两者必须使用不同 command、状态和前端文案。
- Queue 归属：只有已分配 WorkerSession 的 queued Task 才进入对应 SessionTaskQueue。未携带 exact Session target intent 的 Task 在新 Session 分配完成前不进入任意既有队列，也不取得 `session_queue_sequence`。
- Queue 顺序：每个 WorkerSession 的 SessionTaskQueue 使用服务端提交时分配的单调 `session_queue_sequence` 做稳定 FIFO。重复 enqueue 复用原位置；P0 不按 Task priority 改序，不支持 drag reorder 或 preemption。
- Queue 操作：TaskInitiator 可按现有权限取消 queued Task，或在 exact target/expected revision/idempotency key 下显式改派到另一授权 WorkerSession；改派后从目标队尾取得新 sequence。Session 已达并发/权限限制时原队列位置保持不变。
- Dispatch 公平性：所有新顶层 WorkerRun 都必须消费短期单次 SessionDispatchPermit。Task permit 只发给当前合法 queue head；standalone prompt 只在队列为空时取得 permit，不能抢占更早 queued Task。slot release 触发调度，事件丢失由定期 reconciliation 收敛。
- 并行执行：需要同时推进两个顶层目标时，创建另一个 WorkerSession，并使用隔离 WorkingCopy；Agent 内部 subagent 仍归属一个顶层 WorkerRun，共享其权限、24 小时 Turn deadline 和产物谱系。
- Task 动作权限：Task 创建时固化 TaskInitiator。P0 中 initiator 可继续交互、验收、要求返工、取消和改派 queued Task；current Workspace owner 只有 emergency cancel。Task 可见性、Conversation controller、Workspace editor/reviewer、群披露和 Artifact review 不产生上述权限。
- 动作收敛：accept/rework/cancel/reassign-queued-session 必须携带真实 actor、exact target、expected Task revision 和 idempotency key，并由 Task Orchestration 在同一事务中重验、写 TaskTransition/queue revision 与 Outbox。P1 再引入受限 TaskActionGrant，不让 P0 委托模型阻塞核心交互。
- Grant 分工：ApprovalGrant 只授权受保护 Tool/外部操作，不用于延长 AgentTurn。
- 产物边界：P0 默认生成 `report` 和 `patch` Artifact；前端按类型渲染，patch 提供 diff 审查、预览、下载、应用到 Workspace 和显式发布为分支等 ArtifactAction。发布分支不是默认自动输出，但已存在匹配的 RepositoryAccessGrant 时不再逐命令审批。
- 执行审批：Worker 在沙箱内执行普通文件、命令、测试、patch 和 report 操作不额外询问；新增外部平台鉴权、跨沙箱或生产写权限、安装未授权能力时进入 `waiting_user`。`waiting_user` 暂停 48 小时 idle 计时但不突破 14 天绝对上限；用户可选择一次性授权或限定 WorkerSession 的会话级授权。
- 受管 CodePlatform：P0 固定镜像预装版本锁定的 Git、GitHub CLI 与批准的 GitHub Skills。RepositoryAccessGrant 按 repository + capability + `once|worker_session` + expiry/constraints 授权；Grant 生效后，Agent 可直接使用原生 `git`/`gh` 执行短期 Token 允许的非管理类操作。
- 代码平台凭证：短期 access token 对 Agent 可用是受托执行模型的一部分；CredentialBroker 保管且绝不下发 App 私钥、OAuth refresh token 和长期 PAT/根凭证。Git 通过 `git-credential-switchback` 和 `credential.useHttpPath=true` 按仓库取 lease；`gh` 通过受管 wrapper 按 invocation 获取 lease，不执行持久化 `gh auth login`。
- 权限 Profile：P0 分离 `repository.read`、`repository.write`、`pull_request.write` 和 `ci.read`，不签发 administration、secrets、variables、workflow/Actions write、ruleset management、repository delete 或组织管理权限。Token 不能表达的 branch/ref 限制由 GitHub Ruleset/Branch Protection 承担。
- 外部结果收敛：push、PR 等写入必须生成 CodePlatformOperation；响应丢失或认证/网络失败后先查询 remote ref/head 或 provider 对象，再决定是否重试。禁止盲重放外部写入。
- Secret 例外：P0 暂不以通用 Trace 脱敏阻塞高保真记录，但平台已知 Token 必须在 Trace、Langfuse、Artifact、用户进度和 CLI 输出持久化前做确定性剔除。
- 回退档位：受管 B2 是默认；安全政策禁止 write Token 进入 Agent 时切换方案 A（全部远端操作由控制面执行）；Provider 写 scope 过粗或 ruleset 无法安全约束写入、但仍需原生 read Git 时切换方案 C（直接只读、受控写入）。A/B2/C 复用 RepositoryAccessGrant、CodePlatformOperation 与 provider result 契约。
- 可观测：任务状态、日志、事件、模型调用、成本、产物、失败原因。

### 5.4 Evolution

职责：

- 消费 Assistant 和 Worker 的 JSONL 轨迹。
- 按滑动窗口、阶段完成、任务完成或定时策略生成 Truth。
- 在 PostgreSQL 中管理 ContextCandidate、不可变 ContentRevision、ReviewTask、PublicationDecision、tombstone 和审计；将批准后的 active revision 投影到 OpenViking。OpenViking 是当前强依赖检索服务，但不是治理事实源。
- 为后续 Assistant/Worker 提供 Truth 读取能力，包括用户偏好、项目事实、业务知识、规则、反模式和可复用经验。

关键要求：

- Truth 必须带来源引用、置信度、适用范围、过期策略和版本。
- MemoryPublicationPolicy 使用确定性白名单决定自动发布。P0 仅允许 `explicit_preference`、`verified_descriptive_fact`、`entity_alias` 和带强制 TTL 的 `ephemeral_project_state`；还必须低风险、非敏感、无冲突、scope 不扩张，并有用户明确原话、权威来源或确定性运行验证。
- 模型推断、摘要、置信度或重复行为本身不能授权自动发布。`explicit_preference` 不能授予权限，也不能表达生产/外部副作用、强制流程或 Agent 行为边界；这些内容必须改归行为型 Memory、ProcedureTruth 或 InstructionPolicy 并人工审核。
- 推断偏好、行为/流程/Instruction、Skill、权限安全、生产/外部操作、跨用户或 org scope、敏感内容、失败案例归纳、主观评价、冲突或低置信候选必须人工审核或拒绝。动态事实必须有 `refresh_after`，`ephemeral_project_state` 必须有 `expires_at`，过期后自动退出 active projection。
- 自动发布仍必须生成不可变 ContentRevision、TruthPublicationDecision、policy revision、证据和审计事件，并进入用户 Memory 看板；用户可修改、停用、删除或恢复。
- Memory 看板分为 MemoryLibrary 与 ReviewInbox：Library 按“我的 / Workspace / Project”scope 管理 active、disabled、expired Memory；Inbox 将 Memory、Procedure、Instruction、Skill 分 lane，展示内容 diff、来源证据、风险/冲突、scope/ACL 影响、策略判定、审核历史和等待时长。
- user-scope Memory 由用户本人治理，workspace/project 内容由对应 owner/reviewer 治理。批量 reject/disable 允许逐条鉴权执行；批量 approve 仅允许同 scope、同 policy revision、同低风险 Memory 类型且无敏感、冲突或 scope 扩张的项目。Procedure、Instruction、Skill、权限安全、跨 scope 和冲突项目禁止批量批准。
- 编辑必须创建新 ContentRevision；disable 使 active projection 移除，delete 写 tombstone，restore 产生新的 active revision。批量命令必须有 expected revision、幂等键和逐项结果，不得静默部分成功。
- P0 不设置阻塞式审核 SLA，只提供 age、筛选和摘要提醒；pending 内容不进入 ContextPack，不阻塞当前 Task/Worker 执行。未来可按 tenant 配置 reviewer SLA。
- InstructionPolicy、行为型 Memory，以及 SkillPackage 的生成或修改必须人工审核；模型不能因为候选来源是 Evolution 而自动激活行为或可执行能力。
- Evolution 可以使用模型判断候选应自动发布、请求人工审核或拒绝，但安全、生产、跨用户/工作区、低置信、敏感和冲突候选必须由确定性策略强制审核。
- Memory 看板和审核操作必须先写 Switchback RDBMS，再通过 Outbox 幂等同步 OpenViking；禁止前端直接调用 OpenViking CRUD。
- 避免把一次性错误、临时状态、失败推测沉淀为长期规则。

### 5.5 Inspiration

优先级：P2 experimental。当前不知道它能否形成稳定使用闭环，因此不进入 P0/P1 关键路径；Assistant、Worker 和 ContextPack 必须在 Inspiration 未部署、停用或无结果时正常工作。P2 实现前先用少量人工 manifest 在真实 Workspace 试点，验证是否降低上下文追问、提升相关资料命中和实际打开/读取率。

职责：

- 以独立代码仓库作为声明权威，固定使用单资源 YAML manifest、工作区/主题 Markdown 和确定性生成的 `INSPIRATION_INDEX.md`。
- 映射飞书文档、Wiki、妙记、需求文档、项目资料、业务知识库和历史交付记录，保存具体 URL、摘要以及帮助 Agent 理解意图和发现用户/工作区上下文的辅助内容。
- 支持人工调整，也支持同步器或模型生成分支/PR 提案；高频 source version、ACL、freshness、错误和索引状态写入 PostgreSQL ResourceRecord，不制造 Git 提交噪音。
- 为 Assistant / Worker 提供候选上下文来源，而不是无边界地加载所有资料；具体读取飞书文档时，由 Assistant 或 Worker 中的 Agent 根据链接调用 lark-cli 和对应 Skill 完成。

保留设计：

- 激活后采用“Markdown context + YAML manifest + generated index + PostgreSQL/OpenViking 可重建投影”。
- 模型生成或修改摘要、intent、scope、owner、source URI 时只创建 PR，由 CODEOWNER 审核后进入 main；外部源健康检查只更新 ResourceRecord。
- 完整文档继续由 lark-cli/Skill 实时读取；OpenViking 仅保存已发布 L0/L1 检索投影。
- main merge webhook 只校验并提交幂等 sync request；后台按 exact commit SHA 编译。周期 reconciliation 比较 repository head 与 last projected SHA，修复 webhook 丢失、停机和失败重试。连续提交可收敛到最新 head，中间版本由 Git 历史保留。

### 5.6 Delivery

职责：

- 作为飞书等外部入口触发 Worker/Assistant 流程。
- 处理飞书消息、命令、按钮、中间选择与对话、任务状态回传和最终结果卡片渲染。
- 与 Assistant 形成互补：共享群聊普通消息始终先进入 Assistant；完整结构化 `/task`/create-task form、绑定 exact ready TaskDraft 的确认，以及 P2P/private/API 中策略接受的明确创建请求可走直达 TaskSpec 快路径。
- Task readiness 与 Task creation intent 是独立门槛。群聊 ordinary message 即使 readiness 已满足也不得直接创建 Task；显式创建面仍必须经过身份映射、TaskCreationTriggerPolicy、Workspace/权限、TaskSpec validator 和审批策略。
- ready TaskDraft 确认可复用 `waiting_user` card kind，但必须以 `task_draft` subject/target 绑定 exact draft revision、TaskSpec digest、readiness、Workspace resolution、Conversation generation 和 render revision；自由文本“确认”无此 target 时仍进入 Assistant。
- Shared TaskDraft 每条 lineage 固定一个 initiator。只有 initiator 可执行 `create_task`；协作补充 Turn、Conversation controller 或 WorkspaceRole 不改变 initiator，也不能代为确认。
- 其他 participant 必须显式 `AdoptTaskDraft`，fork 出以自己为 initiator 的新 draft，并重新校验 context refs、Workspace/权限、readiness 和 digest 后再次确认。Adoption 不继承原用户 intent、Approval/Repository grant、Secret 或私有 evidence，不改写 source draft。
- 同一 actor/source revision 的 adoption 幂等；不同 actor 的相同 digest 是不同 lineage，不自动合并。Rollover、Workspace mismatch、source consumed/cancelled 或 context 不可读时 fail closed；仅原 initiator/readiness stale 时可在 payload 仍可读且 scope/generation 有效的前提下 fork 并完全重验。
- Delivery 只持有通道消息/卡片投递状态，不持有 Task、Assistant 或 Worker 的业务执行状态，也不实现第二套任务编排逻辑。
- P0 Delivery 使用独立 DeliveryCardProjection，从 owner events、授权 public projection facts 和 durable Display summary 派生 `task_progress`、`waiting_user`、`approval`、`result`；CardInstance 只保存 provider identity、render revision 和投递/更新/过期/对账状态。
- inline action 仅限 owner 当前显式允许的 create-task、adopt-task-draft、choice/text、once/session approval、cancel 和 retry；`create_task` 只用于 initiator 的 `waiting_user` + exact `task_draft`，`adopt_task_draft` 只创建新 actor-owned draft。完整 Artifact review、patch apply、发布、PR、生产操作、Skill/Tool 与继续 Worker chat 通过 Web deep link 完成。
- 共享卡片中的 cancel/accept/rework 只表示 Task 当前存在可提交的动作候选，不授予所有群成员权限；callback 必须重新验证点击 actor 是否为 TaskInitiator，emergency cancel 还可接受 current Workspace owner。固定 AgentTurn deadline 只读展示，不提供延期动作。
- 卡片 callback 快速确认后转 authenticated idempotent Gateway command；业务结果只能由 owner event -> DeliveryCardProjection -> card update 收敛。重复/stale/expired callback fail closed 或降级，不得在 Delivery 内直接审批/取消/重试。
- 飞书 callback/update/interaction 时限、卡片版本和 rate limit 由 DeliveryProviderCapability 管理，不进入 Task/Worker/Approval 领域常量。P0 只做语义/策略边界更新，不逐 token 更新卡片。
- P0 每个 active ChannelThreadBinding 在任一 revision 固定绑定一个 current primary Assistant Conversation。无有效显式目标的普通飞书回复始终形成该 Conversation 的 Assistant Turn；新建 Task、WorkerRun、Approval 或结果卡片不改变默认路由，只有接受的 ConversationRollover 可切换到 successor。
- P0 飞书群 topic 使用所有参与者共享的 channel-scoped Conversation，而不是按 sender 建立隐藏 per-user Conversation；每个 Turn/command 仍记录真实 actor。
- 共享 Conversation 的角色分为 `participant` 和 `controller`。provider-mapped participant 可提交普通 Turn；只有 controller 可选择/清除 Workspace、触发 rollover、关闭 channel Conversation 和委派 controller。Binding creator 通过 provider/Tenant/channel policy 后成为首个 controller。
- Controller 只是共享对话协调权，不授予 Workspace、Task、WorkerSession、Approval、Artifact、外部 provider 或群披露权限；所有操作仍按真实 actor 和目标资源重验。显式角色变更不能移除最后 controller；撤权导致 controller 全部失效时进入 `control_orphaned`，由策略授权的 channel administrator 显式恢复。
- Rollover 只把 source generation 中当前仍合格的 participant/controller 按 policy 投影到 successor，保留 role revision/evidence，不新增或提升 controller；无合格 controller 时 successor 直接进入 `control_orphaned`。
- TaskDraft/Task/WorkerSession/Approval/Artifact 直达操作必须携带经验证的 InteractionTargetRef，来源限当前 card action、结构化命令或注册了不可变路由元数据的明确交互上下文。Delivery 不维护 current focus，也不根据最近 Worker、最近 Task、最近卡片、provider parent_id 或模型猜测自动路由。
- related refs 只用于导航和投影。同一话题中的每个发送者独立鉴权；provider 不支持话题时可使用 chat/root-message fallback binding，但保持 Assistant-default 语义。
- 入站普通消息按 provider message identity 和可用 content revision 幂等，记录 binding revision 与 target evidence。Provider/system 导致的关闭/revoke ChannelThreadBinding 只终止通道路由，不静默终止 Conversation、Task 或 WorkerSession；显式 controller close 可以按 revision 同时关闭 channel Conversation/binding，但不终止执行资源。
- ChannelThreadBinding 可保持 Workspace 未解析；Workspace-scoped context/Task/Worker 前必须通过 Web selector、当前 card/结构化 command 或显式 CreateWorkspace 结果生成 WorkspaceContextResolution。default/global/唯一候选/model inference 都只可预选。
- SetWorkspaceContext 使用 expected binding revision 和幂等 evidence，成功递增 binding/resolution revision；每次受保护操作仍重验 Membership。更改 default 只影响未来 context/TaskDraft，不迁移既有资源；显式 target 使用资源自身 Workspace 且不修改 default。
- Membership 撤销使 resolution 对受保护操作变 stale，不得 fallback 到其他 Workspace。Workspace selection 不代表群聊 audience 已获授权。
- 首次 `unresolved -> W1` 在原 Conversation 记录 resolution；已有 W1 历史后，`W1 -> W2`、`W1 -> cleared` 或 W1 access revoked 必须创建 successor Conversation、将 source 标记 `superseded` 只读并在同一事务推进 ChannelThreadBinding/Outbox。切回 W1 也创建新 generation，不复活旧 Conversation。
- ConversationRollover 与普通 message acceptance 通过 binding/source revision 串行化。事务提交在 rollover 前的消息属于 predecessor，观察到新 binding revision 的消息属于 successor；duplicate/stale/response-loss 不得创建第二 successor 或静默搬运 Turn。
- 默认 ConversationHandoff 只含 typed Workspace-neutral goal/constraint/assumption/blocker。raw Turns、旧 summary、Workspace Truth、Repository/Task/Worker/Trace/Artifact refs 和 Tool output 不自动携带；显式跨 Workspace carry-over 逐 source 重验 source read、target context permission、classification 和 intent evidence。
- TurnWindow、ContextPack、搜索/导出与 Evolution 必须保留 Conversation generation、Workspace scope snapshot、resolution revision 和 lineage，不能按 provider topic 或 lineage 自动合并跨 Workspace 证据。
- P0 群聊 Workspace-derived 内容要求显式 `WorkspaceChannelShareGrant`。Identity & Access 校验当前 Workspace owner、精确 Workspace/provider group、audience/security baseline、profile、policy 和 revision；Grant 只授权披露，不授予 Membership、执行、Web 访问或卡片动作。
- 每次共享卡片发送/更新生成 `ChannelDisclosureDecision`。P0 `workspace_summary` 仅允许 owner projection 标记为 `channel_shareable` 且 profile allowlist 接受的标题、粗粒度状态、安全摘要和 Artifact 类型/数量；raw Trace、私有 prompt、hidden reasoning、Secret、Tool stdout、内部错误、完整 report、patch/diff 和完整 Artifact 永远 Web-only。
- 无有效 Grant、audience 扩张、群外部化/公开化、bot 无法读取当前群状态、provider snapshot stale/unknown、Grant revoke/expire 或 owner 权限失效时降级为不标识受保护资源的 `minimal_notice`。canonical Web link 不携带 bearer authority，每个打开者独立鉴权；actor-private 信息优先走 per-user ephemeral card。
- Grant revoke/suspend 必须 fencing 后续发送并 best-effort 更新/撤回旧卡片，但不能承诺收回已读通知、引用、导出或截图；provider cleanup outcome 和 residual exposure 必须审计。

关键要求：

- 飞书入口消息必须能映射到用户、组织、权限、任务和 WorkerSession。
- 异步执行期间需要持续回传状态，而不是只在结束时通知。
- 失败时必须给出用户可执行的下一步，例如补权限、补参数、重试、转人工。

### 5.7 EventCenter

职责：

- 统一处理 Switchback 相关事件，包括 Assistant、Worker、Delivery、LLMProxy、Skilling、Gateway 等模块事件。
- 支持事件发布、订阅、路由、重试、死信、幂等和审计。
- 将异步任务状态、消息片段、统计数据和最终产物传递给 Gateway / Frontier / 飞书入口。

关键要求：

- 事件 schema 需要版本化。
- 关键事件必须可追踪，包括 correlation_id、causation_id、trace_id、tenant_id、user_id。
- 同一会话/任务事件流使用 `stream_id` 和单调递增 `sequence` 排序，按 `event_id` 或 `idempotency_key` 去重；`occurred_at` 只用于审计和展示，不能独立保证顺序。P0 先用 PostgreSQL outbox dispatcher，P1 再验证 RocketMQ message group。
- 对用户可见消息和内部观测事件做清晰区分。

### 5.8 LLMProxy

职责：

- 统一代理 Assistant 的模型请求。
- 对 Worker 中主流 Coding Agent 的模型请求进行 sidecar 劫持或代理。
- P0 固定基于 LiteLLM；LLMGateway 不进入当前实现范围。
- 支持模型路由、鉴权、限流、审计、成本统计、重试、降级、脱敏和日志关联。

边界：

- “保留迁移边界”只表示 TaskSpec、ModelPolicy、ModelInvocation 和 Trace 不暴露 LiteLLM 专有字段，不代表 P0 再实现一套 Model Gateway。
- P0 只验证 Codex 所需的 OpenAI-compatible Responses/Chat Completions 路径、流式响应、用量和错误映射；其他 provider 协议按实际 Agent 接入扩展。

### 5.9 Frontier / Frontend

职责：

- Frontier 消费 EventCenter 驱动的用户可见 projection，向 Frontend 提供任务 timeline、Assistant 消息、Worker 进度、中间结果和最终产物。
- Frontier 提供实时 SSE 和必要的读模型查询；Frontend 负责渲染、交互和错误展示，不持有执行状态。
- Frontier 与 Frontend 需要紧密协作定义展示事件和渲染 schema，但通过稳定 projection contract 与执行逻辑解耦。
- Frontend 提供可单独打开的 Worker 详情页，展示历史和实时 Trace、阶段性 report/patch、ArtifactAction，以及直连 WorkerSession 的 chat box。
- Worker chat box 支持普通 prompt、排队消息、interrupt 和 steer；这些操作进入 AgentTurn，不重新经过 Assistant clarify loop。
- P0 以 WorkerDetailView 为主交互面，identity 是 `worker_session_id`；产品中的 Worker 不表示后端 Worker 服务实例。TaskList 只做轻量交付跟踪，TaskDetail 是摘要/抽屉并深链到对应 Worker。
- Home P0 只需展示最近 Worker、waiting_user/approval 和近期交付，不要求完整跨资源 ActionInbox。Workspaces 聚合持久资源上下文；P1 Memory 独立提供 MemoryLibrary/ReviewInbox。
- WorkerDetailView 展示 Task 摘要和稳定 deep link。Artifact 在 Task/Worker/Workspace 中嵌入摘要，统一跳转 canonical ArtifactDetailView；Memory 只通过 evidence refs 关联执行视图。
- Agent display 使用 durable/transient 双 lane：DisplayCheckpoint 完整物化用户可见 parts、进入 ViewProjection 并推进 ViewCursor；TransientDisplayDelta 只负责 checkpoint 间低延迟文本/摘要/参数/进度，不推进 durable cursor/version。
- Display stream 必须在 start、Tool/Approval/waiting_user/Artifact/error/cancel/timeout 等语义边界和 terminal 创建 durable checkpoint，并受版本化 DisplayDurabilityPolicy 的时间/字节上限约束；terminal checkpoint 必须能在清空 transient buffer 后完整渲染。
- Transient delta 绑定 stream/part/base checkpoint/sequence；duplicate 忽略，gap 时停止该 part 拼接并等待/查询 checkpoint。允许合并相邻 chunk 和丢弃 progress pulse，但不能丢 durable checkpoint/control。
- 用户可见 Display 只包含 reasoning summary，不输出隐藏 chain-of-thought；完整 ACP/Agent frames 仍按 TracePolicy 写入权威 Trace JSONL，因此 transient browser delivery 不降低证据保真度。
- DisplayPart 采用 Switchback-owned v1 联合类型：`text`、`reasoning_summary`、`tool`、`activity`、`approval`、`artifact`、`source` 和 `error`。每个 part 使用稳定 identity/revision/ordinal、lifecycle、criticality、permission-filtered visibility、provenance 与 Gateway action refs。
- ACP/Codex、AG-UI、AI SDK 等 source frame 只能通过 adapter 映射到 DisplayPart；未知可选 progress 可安全降级，未知 Tool/Approval/权限/安全语义 fail closed。Frontend 按 schema/type renderer registry 渲染，未知关键类型要求升级。
- Display payload 不得包含任意可执行前端组件、未清洗 HTML、secret 或未经治理的本地路径；`available_actions` 只是命令引用，真正写操作仍由 Gateway/owner context 重验权限、revision、policy 和 idempotency。
- P0 Frontend 使用 React + TypeScript + Vite SPA；Node.js 只参与开发和构建，生产 bundle 通过与 Go Gateway/Frontier 相同的 origin 发布，不运行 Next.js/Node BFF。
- React Router 管 route/resource/filter identity；TanStack Query 管 HTTP 查询、缓存和 refetch；独立 ViewProjectionStore 管 active View 的 epoch/cursor/version、typed reducer、transient overlay 和 PendingCommand。组件不得直接拼 raw SSE 或维护并行权威副本。
- assistant-ui 仅作为 Worker Chat 的可选 ExternalStore 渲染层。Switchback 保留 WorkerSession/message/part identity、持久化、Tool/Approval authority、queue/interrupt/steer command 和 SSE recovery。
- 前端同源使用 Secure/HttpOnly/SameSite session credential；Gateway command 继续校验 CSRF 和 idempotency。Vite client env 不得包含 secret，SPA fallback 不得覆盖 API/SSE/Artifact route。

边界：

- Gateway 触发同步命令和关键元信息查询；Frontier 独立承载面向 Frontend 的 SSE 与展示读模型。
- EventCenter 不直接向浏览器输出；它驱动 Frontier projection，Frontier 再向 Frontend 推送。
- Frontend 不扫描 raw domain event/Trace 重建状态，不复制 Task/Worker/Artifact/Memory 权威状态机；所有写操作经 Gateway，结果以 Frontier projection 收敛。

### 5.10 Skilling

职责：

- 连接 Skill Market。
- 管理用户级、组织级、项目级和 Worker 级 Skills。
- 为 Assistant / Worker 提供可用 Skill 清单、版本、权限、依赖和加载方式。
- 支持 Skill 的启用、禁用、升级、回滚和审计。
- 提供内部 API 给 Assistant 和 Worker 搜索、查看、请求安装、安装、启停、升级和回滚用户/工作区 Skill 配置。

关键要求：

- Skill 不应默认拥有过宽权限。
- Skill 与 MCP 的权限、密钥、网络访问和数据范围需要显式声明。
- 预装 Skill 不要求单独用户安装确认。Evolution 生成或修改的 Skill 必须先完成人工内容/安全审核；审核发布后在批准 scope 内不要求重复安装确认，但仍要通过平台策略、权限和来源检查。
- 其他 Skill 必须获得用户手动批准；用户在与 Assistant 的明确对话中要求安装，视为可审计的 `CapabilityInstallGrant`。

## 6. 主流程

### 6.1 Web Assistant 流程

1. 用户通过 Frontend 向 Gateway 发送 message 或关键操作。
2. Gateway 鉴权、写入基础消息记录，返回 accepted 状态和 conversation_id / request_id。
3. Gateway 发布 AssistantCommand 或 MessageReceived 事件。
4. Assistant 加载最近 Turns、相关 Truth、Rules、Skills；仅当 P2 Inspiration capability 已启用且存在候选时，才把其候选作为可选上下文加入预算编译。
5. Assistant 生成 TaskReadinessAssessment；存在 blocking unknown 时每次只追问一个问题，连续三问后展示 ClarificationCheckpoint。
6. 无 blocker 且 validator 通过时，Assistant 生成带 readiness ref、creation intent ref 和 assumptions 的 Task Spec；已有 policy-accepted exact intent 时派发，否则先展示 exact ready TaskDraft 确认。用户继续澄清会重置下一组三问预算。
7. Worker 创建 WorkerSession，准备运行环境，装载 Agent/Skill/MCP。
8. Worker 执行任务，持续产生事件、轨迹和产物。
9. EventCenter 驱动 Frontier 更新用户可见 projection；Frontier 通过 SSE 向 Frontend 推送。
10. Worker 完成后写入最终 JSONL 轨迹与产物引用。
11. Assistant 可对结果做总结、追问是否继续、或生成下一步任务。
12. Evolution 根据窗口或完成事件生成 ContextCandidate，在 RDBMS 中治理修订/审核，并把批准的 active revision 投影到 OpenViking。

### 6.2 飞书 Delivery 流程

1. 用户在飞书触发命令、按钮或消息。
2. Delivery 校验用户身份、channel binding、ConversationParticipant、上下文、权限和任务参数；群 topic 的普通消息进入共享 Conversation，并保留真实 actor。
3. 群聊普通消息始终形成共享 Assistant Turn。Assistant 生成 TaskReadinessAssessment；ready 时展示绑定 exact TaskDraft revision 的确认入口，不直接创建 Task。
4. 完整结构化 `/task`/create-task form 可零问生成 ready assessment；不完整输入进入 Assistant 补全，并在最终草稿发生实质变化后重新确认。P2P/private/API 的明确创建请求可按 TaskCreationTriggerPolicy 走相同快路径。
5. Delivery/Gateway 只在允许的创建面生成 ExplicitTaskCreationIntent；Task Orchestration 重验 intent、readiness、TaskSpec digest、Workspace resolution、actor authorization 和 policy 后，同事务创建 Task、初始 TaskTransition 和 Outbox。重复确认返回原 Task identity。
6. Shared draft 的 `create_task` 仅接受 initiator。其他 participant 先通过 `/task adopt`、Web 或 registered bot action fork 新 draft，重新完成 actor-specific readiness/authorization 后确认；原 draft 保持不变。
7. Worker 执行期间，EventCenter 的 authorized public facts 驱动 DeliveryCardProjection，在语义/策略边界更新同一 CardInstance。
8. 任务完成后，Delivery 渲染 result card，展示 report/patch 摘要、失败原因和 Web 审阅/继续 Worker 入口；完整 patch 操作不在卡片内执行。

## 7. 数据与存储

P0/P1 基线：

| 能力 | P0/P1 选择 | 边界 |
| --- | --- | --- |
| 核心数据库 | PostgreSQL | Task、Conversation、WorkerSession、Outbox、projection、审计和 JSONB 扩展元数据；MongoDB 暂缓。 |
| 事件可靠性 | P0 PostgreSQL transactional outbox + polling dispatcher；P1 RocketMQ profile | P0 先验证 schema、幂等、重试和 SSE projection；需要跨服务订阅、吞吐和回放后再引入 RocketMQ。 |
| Task durable execution | PostgreSQL Task state + transition + scheduled-command poller | Task/TaskTransition/Outbox 同事务；due command 通过 `SKIP LOCKED`、短 lease、fencing 和幂等命令恢复，不引入 Temporal 或第二套 TaskSaga 权威状态。 |
| 对象存储 | P0 `LocalFileObjectStore` + Docker volume；P1 S3-compatible adapter | 保存 Assistant Turns、Worker Trace、日志和 Artifact；TraceManifest 位于 PostgreSQL。P0 可逻辑模拟 tier，生产 adapter 再映射真实 storage class。MinIO 已归档，不作为新项目默认依赖。 |
| Truth governance | PostgreSQL | Evolution 拥有 candidate、revision、review、audit、tombstone 和 projection 状态。 |
| Active context retrieval | OpenViking | 强依赖检索投影，保存 approved active Truth/Resource，并支持分层检索与 snapshot。 |
| Inspiration declaration | 独立 Git repository | YAML InspirationEntry、Markdown context、generated index 和 PR/CODEOWNER 审核权威。 |
| Inspiration runtime state | PostgreSQL ResourceRecord | 保存 source version、ACL/freshness、同步错误和 OpenViking index revision，可从 Git 与源系统重建。 |
| 模型代理 | LiteLLM | P0 唯一模型网关实现；Switchback 契约不暴露 LiteLLM 专有配置。 |
| 代码平台 | P0 GitHub 受管原生 CLI；P1 GitLab 受管 `glab` | GitHub 使用 CredentialBroker、`git-credential-switchback`、managed `gh` 和 RepositoryAccessGrant；GitLab 复用领域契约但可有 provider-specific wrapper/profile，不强求最低共同命令 API。 |
| CI | GitHub Actions 用于 Switchback 自身验证；Worker 可按 `ci.read` 查看任务相关状态 | P0 不把外部 CI 触发作为交付承诺；Worker 在沙箱内运行测试，GitLab CI/Jenkins 后续通过 provider capability 接入。 |
| 容器 | P0 Compose-managed Backend + Docker Engine sibling Workers；P1/生产 Kubernetes profile | M1 RuntimeSupervisor 是 `switchback-backend` 内的 Go 模块并独占 Docker Socket，动态 WorkerSession containers 是同级容器且无 Docker 管理权限；规模化调度再迁移 Kubernetes adapter。 |
| 观测 | Langfuse | Trace/LLM/tool 观测投影，不作为权威事实源。 |

数据保留与隔离：

- P0 DataLifecyclePolicy 默认 `hot 30 天 -> warm 至 180 天 -> cold 长期`，不按时间自动物理删除；Tenant 可通过新 policy revision 调整阈值。Langfuse 是可重建投影，可使用独立且更短的保留期。
- TraceManifest 管理不可变 JSONL segments 的 sequence range、checksum、bytes、schema、tier、availability、Langfuse export 和 analytics cursor。tier 变化不重写 payload。
- 后端运营平台优先服务排障、Evolution 重放和效果看板：按 tenant/workspace/task/session 查询 metadata，执行 restore/archive/quarantine/delete job，展示 ingest/projection/error 状态和任务、成本、Tool、Artifact、Memory 指标。
- P0 审计保持轻量，只记录 actor、action、target、reason、before/after、time、result 和 correlation id；不强制工单、四眼审批或独立审计仓库。raw Trace 读取需显式能力并记录访问，但不默认增加二次审批。
- 显式删除使用正常授权和一次确认，进入 deletion_pending，幂等删除 ObjectStore/Langfuse payload 并校验后保留最小 tombstone。非敏感聚合统计可继续保留；Evolution evidence 必须标记 `source_deleted`，不能假装来源仍可核验。
- Dashboard 消费 IntegrationEvent/Trace ingest 的 normalized facts 增量更新 TraceAnalyticsProjection，禁止在请求路径扫描原始 JSONL。cold 数据可异步 restore 后供排障或 Evolution replay。
- P0 Tenant 已作为硬安全边界：每个 Workspace 只属于一个 Tenant，所有数据访问先校验 resource tenant。用户还必须同时具有 active TenantMembership 和显式 WorkspaceMembership；禁止跨 Tenant 共享同一 Workspace identity。
- WorkspaceRole 为可组合的 viewer/editor/reviewer/owner。viewer 读用户可见内容；editor 可创建 WorkerSession、AgentTurn、ChangeSet 和普通 WorkspaceApply；reviewer 审核策略指定内容但不隐式拥有 editor；owner 可管理成员并包含前三者能力。Workspace 必须至少保留一个 active owner。
- Project/Team 不隐式授予 Workspace 权限。未来群组接入必须保留可解释的授权来源和 effective membership，不能用不可审计继承替代显式裁决。
- WorkspaceRole 只授权 Switchback 内部动作。外部 clone/push/PR、飞书写入或生产操作仍独立校验 Capability/ResolvedToolPolicy、必要 ApprovalGrant、SecretLease 和外部 provider 权限。
- Worker 始终代表发起用户，通过绑定 tenant/workspace/session/action 的短期 ExecutionAuthorizationLease 执行，不获得平台超级用户身份。Membership/Policy 撤销后停止新命令、Apply、受保护读取和 secret 续租，撤销相关 lease，并保留 session/trace 进入 `waiting_user:access_revoked`。

### 7.1 JSONL 轨迹基本要求

每条 JSONL 记录应至少包含：

```json
{
  "schema_version": "v1",
  "event_id": "evt_xxx",
  "timestamp": "2026-07-07T00:00:00Z",
  "tenant_id": "tenant_xxx",
  "user_id": "user_xxx",
  "conversation_id": "conv_xxx",
  "task_id": "task_xxx",
  "stream_id": "worker-session:ws_xxx",
  "sequence": 42,
  "session_id": "session_xxx",
  "source": "assistant|worker|delivery|llm_proxy",
  "type": "turn|tool_call|model_call|status|artifact|error",
  "payload": {},
  "correlation_id": "corr_xxx"
}
```

### 7.2 Task Spec 基本要求

TaskSpec 不携带用户可选时长；所有执行统一应用平台固定的 24 小时 AgentTurn hard deadline。

```json
{
  "task_id": "task_xxx",
  "source": "assistant|delivery",
  "title": "short task title",
  "goal": "clear desired outcome",
  "context_refs": [],
  "constraints": [],
  "acceptance_criteria": [],
  "agent_config": {
    "agent_type": "codex|claude-code|other",
    "acp_version": "v1",
    "model_policy": {}
  },
  "runtime_config": {
    "workspace_ref": "",
    "container_policy": "",
    "resource_limits": {}
  },
  "skill_refs": [],
  "mcp_refs": [],
  "created_by": "user_xxx"
}
```

## 8. 非功能需求

### 8.1 可靠性

- 所有异步命令与事件必须支持幂等处理。
- 同一事件流必须按 `stream_id + sequence` 构建顺序，按 `event_id`/`idempotency_key` 去重；不能仅依赖不同进程产生的时间戳。
- EventStream 必须由唯一 StreamOwner 在业务状态与 Outbox 同一事务中分配 sequence；EventCenter 只校验、路由、重试和投影，不分配或改写领域 sequence。不同领域需要产生某 stream 的事件时，通过 owner command 完成。
- 消费端对同 event id 重投幂等忽略；未来 sequence 进入 gap buffer 并触发补偿读取/重试；同一 `(stream_id, sequence)` 对应不同 event id 必须进入 integrity incident/DLQ，禁止覆盖。
- 跨 stream 不定义全局业务顺序。TimelineProjection 独立分配 TimelineCursor 供 SSE resume、分页和稳定展示，使用 `correlation_id + causation_id` 保留因果。迟到事件追加新 cursor 并标记 `late=true`，不能重排已输出 cursor；`occurred_at` 只用于显示。
- P1 RocketMQ 使用 `stream_id` 作为 FIFO message group 并保留相同 payload sequence、幂等、gap、DLQ 和 TimelineCursor 语义，切换 broker 不改变领域契约。
- Task 状态固定为 `queued/running/waiting_user/delivered/completed/failed/cancelled`；WorkerRun 保留更细的 runtime 执行状态。
- 同一 WorkerSession 最多有一个 bound Task 和一个 active 顶层 WorkerRun。重复或并发 StartWorkerRun 必须按 Session revision/idempotency key/fencing 收敛到原 Run 或 `session_busy`，不能创建第二个 Run。
- P0 不依赖进程内 timer。每次 Task 状态推进必须原子更新 Task revision、追加 TaskTransition 并写 Outbox；事务内不调用 Worker、模型、对象存储或外部系统。
- ScheduledCommand poller 通过短租约、`SKIP LOCKED`、单调 fencing token、expected Task/Run revision 和 idempotency key 投递内部命令。poller crash、lease expiry、重复领取、commit 后响应丢失和 Outbox 重投必须收敛到同一个结果。
- retry、deadline、reminder 和 reconciliation 使用不同 command kind；提醒不得改变 Task 终态，外部副作用 unknown-effect 不得盲重试。
- 失败必须保留可诊断上下文，包括错误类型、失败阶段、可重试性和用户可见说明。
- 关键链路需要死信队列或等价补偿机制。
- 同一 RuntimeInstance generation 同时最多有一个 active ControlTransport；切换传输必须递增 channel epoch，后端使用 generation/epoch fencing 拒绝旧通道。
- fallback 只允许由 gRPC/HTTP2/代理/传输超时等 transport failure 触发；认证、授权、策略和协议版本错误必须 fail closed。
- 控制命令按至少一次投递设计，区分 received/accepted/completed ACK；重连和传输切换后按 command id/idempotency key 安全重投。
- 每个 RuntimeRecoveryIncident 的 P0 自动恢复上限固定为 1 次；新 generation 恢复控制/ACP 会话、通过完整健康检查并满足稳定窗口或完成持久进度 checkpoint 后才关闭 incident，未来独立故障创建新 incident。再次失败时必须进入 `waiting_user`，不能无限重启。
- 自动恢复前必须 fencing 旧 runtime generation 和 channel epoch；恢复后从已确认 command/event/trace cursor 继续，不能把未确认操作当作已完成。
- `suspect` 只暂停新命令并触发探测，不自动终止 active WorkerRun；transport fallback 或有效 heartbeat 恢复后返回 healthy。`unhealthy` 才可创建 RuntimeRecoveryIncident。
- RuntimeRecoveryIncident phase 固定为 `open/fencing/provisioning/reattaching/verifying/recovered/exhausted`。每个事务先提交 phase intent、exact replacement identity、fencing/continuity evidence 与 Outbox，再执行 Docker、文件、secret 或 ACP 副作用；Backend restart 只 reconcile 已持久化 identity。
- M1 当前稳定窗口为服务端固定 30 秒。原 AgentTurn deadline 和 WorkerSession absolute expiry 在所有 recovery phase 中继续流逝并可抢占恢复；不得以 replacement 重置、暂停或延长。
- SingleUserLocalProfile 的 Compose 在恢复门禁通过后默认开启 `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=1`；裸 Backend 默认关闭。设置为 `0` 只禁止新 incident，已持久化 incident 仍收敛到 recovered/exhausted，且不删除证据。

### 8.2 安全与权限

- P0 以 Tenant 为硬边界并用显式 WorkspaceMembership 授权；Skill、MCP、Artifact、Trace、Truth 均需执行 resource tenant、Membership、角色、资源分类和策略检查。
- Worker 容器默认最小权限，网络、文件系统、密钥和外部 API 访问均需策略控制。
- 模型请求和轨迹写入前最终需要考虑敏感信息脱敏；P0 为了保真审计，Trace 暂按普通 Coding Agent session JSONL 记录，暂不阻塞通用内容 DLP，但平台签发或已知的 Token 必须确定性剔除，不能进入 Trace、Langfuse 或 Artifact。
- Truth 不能跨用户或跨组织泄漏。

### 8.3 可观测性

- 每个请求需要 trace_id / correlation_id 串联 Gateway、Assistant、Worker、LLMProxy、EventCenter。
- 需要指标：任务成功率、平均耗时、排队时间、模型成本、Token、工具调用次数、失败分类、事件延迟。
- 用户可见进度与内部调试日志需要分层。
- 后端运营平台需要能按 tenant/workspace/session/task 查询 TraceManifest、Artifact、Langfuse 引用、投影 cursor/异常、storage tier 和 availability。

### 8.4 成本与性能

- Assistant 上下文加载需要预算控制，避免无限加载历史和 Truth。
- Evolution 总结需要滑动窗口、去重和增量更新，避免重复消费大轨迹。
- Worker 容器使用服务端固定 `local_standard_v1`（2 vCPU、4 GiB memory、512 PIDs、10 GiB writable layer）和本机默认 80% aggregate admission；连续性配额仍独立为 Tenant/User 10。持久 WorkingCopy/cache/Agent state 存储 quota 与 cleanup 另行确认。
- LLMProxy 需要模型路由策略，区分高价值推理、常规摘要、低成本分类等请求。

### 8.5 可演进性

- ACP Agent 适配层必须解耦具体 Agent。
- ModelPolicy/ModelInvocation 契约不得泄漏 LiteLLM 专有字段，以便未来替换基础设施；P0 不实现 LLMGateway。
- Event schema、Truth schema、Task Spec 需要版本化。
- P2 Inspiration 激活后，其索引应允许人工编辑与模型演进共存；P0/P1 不依赖该能力。

## 9. 初步里程碑

### M0：需求与架构对齐

- 明确模块边界、术语、领域模型和主链路。
- 明确当前 monorepo 目录结构和 DDD 分层约定。
- 评审 P0 最小基础设施与 P1 integration profiles：PostgreSQL outbox、LocalFileObjectStore、OpenViking、LiteLLM、Docker，以及后续 RocketMQ/S3/K8s。

### M1：Worker Tracer Bullet

- M1 使用 `SingleUserLocalProfile`：服务端配置固定 `tenant_id=local` 与 `user_id=local-user`（允许通过本地配置改名），前端无登录页，客户端请求不得自报或覆盖 actor；启动时为该用户创建本地 owner 上下文。默认只监听 loopback，非 loopback 暴露必须拒绝启动或显式启用后续认证 profile。SSO/OIDC、多用户登录、动态 Membership/RBAC 和用户会话管理后移。
- 首次没有 Workspace 时，Worker-first 表单内联提供“从 GitHub 仓库创建 Workspace”：UI 保持同一流程，但后台先执行独立、幂等的 Workspace bootstrap（CreateWorkspace + RepositoryImport）并展示进度。只有导入完成、首个 WorkspaceRevision 可用且用户明确选中返回的 `workspace_id/revision` 后，才允许预览和提交 TaskSpec；bootstrap 不创建 Task。
- Workspace bootstrap 状态固定为 `provisioning -> ready | setup_failed`。RepositoryImport 失败时保留 Workspace、导入输入、错误与 attempt 证据，但不产生 active WorkspaceRevision 或 Task；用户可在同一 bootstrap identity 下显式重试，或归档空 Workspace。成功时在一个 PostgreSQL 事务中创建首个 RepositoryCheckout/WorkspaceRevision、设置 active pointer、把 Workspace 置为 `ready` 并写 Outbox。
- branch/tag 只用于选择：Repository adapter 在事务外解析并签发短期 RepositorySelectionSnapshot，绑定稳定 repository identity、requested ref、exact commit SHA、actor/tenant、观测时间与 digest。用户确认后 CreateWorkspace/RepositoryImport 持久化 exact SHA；Importer、重试、首个 WorkspaceRevision 和后续 Task 不得重新解析远端 ref。Snapshot 不是授权，M1 确认和 fetch 重验本地 repository allowlist/read policy，后续 profile 重验 RepositoryAccessGrant；要跟随最新 ref 必须显式刷新并形成新选择/Revision。
- 认证用户通过 Worker-first Web 表单创建任务：选择 Workspace/repository/base commit，填写目标、验收标准和可选约束，预览人类可读的结构化 TaskSpec 后显式提交；对应结构化 API 复用同一 command/validator。用户不编辑 raw JSON，M1 不调用模型补全。
- 提交前只做确定性硬校验：必填字段、显式 Workspace、本地 repository/base/read policy、revision 和平台策略不满足时返回字段级 blocker，不创建 Task。通过后 Task Orchestration 为显式选中的 Workspace 创建 Task、分配 WorkerSession 并派发真实 WorkerRun，不创建或推断 Workspace。
- Worker 开始执行后若发现新的目标冲突、不可验证验收或上下文缺口，结束当前 AgentTurn，使 Task 进入 `waiting_user` 并在 WorkerDetail 直接追问；用户回复后开启新的独立 24 小时 AgentTurn，不回退到 Assistant。
- 用户选择一个已授权 GitHub repository 和 immutable base commit；平台为 WorkerSession 创建隔离 WorkingCopy，M1 不支持多仓或无仓任务。
- Docker Compose 启动 `switchback-backend` 与 PostgreSQL 等常驻容器；Backend 内 RuntimeSupervisor 通过 Docker Engine API 动态创建同级 WorkerSession 容器。Docker Socket 只挂给 Backend，Worker 不挂载该 Socket、不 privileged，所有宿主目录挂载由服务端从 Switchback data root 派生。
- Worker 使用服务端拥有的 `trusted_full_network_v1`：通过命名 Docker bridge 直通公网、私网、VPN、LAN 和宿主路由，对出站目的地、协议、端口不做过滤或逐目标审批；Linux 宿主使用 `host.docker.internal=host-gateway`。Worker 仍无 Docker Socket、privileged/host network、任意 host path、published port 或 ambient infrastructure credential，PreviewProxy 仍是唯一受管入站。运行时、StartSnapshot、Trace 和 WorkerDetail 持久化 exact profile id/revision/digest/status；历史旧 profile 只读为 `legacy_unenforced`，不回写。
- Worker 使用 `m1_resident_until_session_terminal_v1`：分配后在 live Session 的 interactive/running/waiting_user/idle 状态保持同一 RuntimeInstance 运行，不做计划性 suspend/resume；只有 close、idle/absolute expiry、安全终止或故障 fencing 才停止/替换。
- Worker 使用服务端 `local_standard_v1` 与默认 80% aggregate RuntimeCapacityAdmission；容量不足时保持 Session resident、按原队列等待 `runtime_capacity`，不启动 AgentTurn deadline。TaskSpec 不暴露任意资源数字。
- 普通 AgentTurn/Tool 子进程在 Turn terminal 时清理；只有 Worker managed-process API 启动的 preview server、dev server 或 watcher 可登记为 SessionProcess 并跨 Turn 存活。登记先于 Turn terminal 提交后，无论 Turn completed/cancelled/timed_out/failed（其中 interrupt 收敛为 cancelled reason）都保留；Session terminal、安全终止、Runtime taint/replacement 或 resource hard limit 仍可停止。WorkerDetail 展示来源、端口、状态、日志和 stop/restart；Runtime replacement 后不自动重启。
- Live preview 通过 Backend PreviewProxy 访问当前 Runtime generation 的 exact declared HTTP/WebSocket port，不映射 Worker host port、不接受任意 proxy target。OpenPreview 返回短期 access ref；代理剥离 Switchback credential，bytes 不经过 EventCenter/Trace，Process/Session/Runtime/撤权/expiry 会关闭活跃 tunnel。只有显式 OpenPreview 可刷新一次 idle，后续页面/HMR 流量不续期。M1 的 URL/origin 形式是 loopback-only 实现细节，不建立领域协议；非本机或多用户 profile 上线前再做隔离评审。
- Worker 在本地 Docker-compatible 容器中通过 ACP 驱动 Codex、经 LiteLLM 请求模型并完成一次真实修改与测试。
- Worker 生成 report 和 patch，并支持在 48 小时空闲窗口内继续交互。
- M1 只配置一个允许的 GitHub repository 和宿主侧 read-only credential（环境变量或挂载 secret）。Repository resolver/Importer 在宿主服务中使用该凭证，Worker 容器只获得已物化 WorkingCopy，不获得 GitHub token、credential helper 或远端 fetch/push 能力。动态 RepositoryAccessGrant、GitHub App/CredentialBroker 和受管 B2 延后；`apply` 只生成新的 Switchback WorkspaceRevision，不写 GitHub remote。
- Worker 详情页支持 Trace、AgentTurn、queue/interrupt/steer、阶段性结果和 patch diff 审查。
- EventCenter 驱动 Frontier projection，Frontier 通过 SSE 回传状态。
- JSONL 轨迹入对象存储并导出 Langfuse。
- Assistant clarify、Delivery、Evolution/Inspiration 和 Skill Market 不在该首个切片的关键路径；这只是实现顺序，不改变其后续产品范围。

### M2：Assistant 闭环与 Worker Runtime 强化

- Gateway 接收自然语言 message，并在无模型网络调用的短事务中持久化 Turn、queued AssistantRun 和 Outbox；leased dispatcher 后续异步完成 clarify/readiness 和 TaskDraft。
- 模型输出只是 untrusted typed proposal。服务端按七维 TaskReadinessPolicy 重建 readiness、visible assumptions、TaskSpec digest 和 creation intent；权限、外部/不可逆副作用、冲突目标、关键上下文缺失和不可验证验收不能被模型或“直接做”绕过。
- 用户确认 exact Conversation/TaskDraft/readiness/Workspace revision 和 digest 后，Assistant 才复用现有 Task admission 原子边界派发 Worker；stale/duplicate evidence 不得留下 partial Task/Worker state。
- 每个 AssistantRun 固化一个服务端模型 profile，不做 in-run provider fallback。Local Compose 提供 Kimi 与 DeepSeek 两个显式 profile；切换需要启动配置变更，失败 run 只能由用户显式 retry。
- Frontier `assistant_conversation` projection 和 SSE 展示 Turns、clarification、checkpoint、ready draft、pending command 与 Task/Worker handoff；Frontend 不持有业务权威，不展示 raw provider payload、credential 或 hidden reasoning。
- Assistant 证据按 Tenant/User/Workspace/Conversation generation 写 checksum-addressed JSONL 与不可变 manifest；ObjectStore/Langfuse 故障独立降级，不回滚已提交 Conversation 或 Task。
- Direct TaskSpec 在 M2 rollout 期间保留为 Assistant disabled/degraded fallback。
- 使用 `github.com/coder/acp-go-sdk` 驱动 `agentclientprotocol/codex-acp`，接入 Codex 真实 Coding Agent。
- 验证 ACP capability negotiation、session new/resume/load、prompt、cancel、permission、tool/terminal/file/update 事件、未知 update 原始帧保留和恢复语义。
- 支持容器生命周期、工作区挂载、Skill/MCP 注入。
- 支持任务取消、超时、失败重试和产物管理。

### M3：Evolution 与 Truth 检索

- Evolution 消费 Assistant/Worker 轨迹。
- 生成带来源的 Truth。
- 在 PostgreSQL 中治理 candidate/revision/review，并将 active revision 投影到 OpenViking。
- Assistant 能加载相关 Truth 并在 Prompt 中可解释使用。

### M4：Delivery 与飞书闭环

- 飞书入口创建任务。
- 飞书内回传进度和结果。
- 支持确认、补充参数、取消、重试等用户交互。

### M5：企业级治理

- 权限、审计、成本、限流、租户隔离、配置中心、监控告警、数据保留策略。

## 10. 待澄清问题

固定 24 小时 AgentTurn hard deadline、后端/Runtime 双重 enforcement 与后端预提交单向 dispatch 已由 ADR 0055 冻结。跨 Turn 后台进程仅允许显式受管 SessionProcess 的边界已由 ADR 0063 冻结；ADR 0064 进一步规定 accepted SessionProcess 跨所有 AgentTurn terminal reason 存活，只有 Session/Runtime/安全/资源边界可以停止它。ADR 0065 冻结 Backend PreviewProxy、无 host-port mapping、exact target 和 lifecycle revocation；M1 URL/origin 形式降级为非阻塞实现 spike。

## 11. 下一步设计建议

P0 已冻结为“轻量 Task + WorkerSession 主交互面”：Task 只保留交付跟踪、验收和结果引用，WorkerRun 表示执行/返工阶段，TaskSaga/TaskAttempt/FollowUpTaskProposal/TaskActionGrant 和 Task/Worker 双主界面不进入 P0。底层仍保留 PostgreSQL 事务、Outbox、fenced scheduled commands、容器/ACP、恢复、权限与 Trace。单 WorkerSession 顶层执行固定串行化；复用 live Session 必须有 exact target intent，复用即继承；SessionTaskQueue 在 Session 内稳定 FIFO，SessionAllocationQueue 在 Tenant/User 配额满时统一排序 Task/Continuation 新 Session 请求。默认连续性配额 10，waiting/idle/lost 仍计数，只有 fenced closed/expired 终态释放。closed/expired identity 永不复活。absolute expiry 终止旧 Run 但保留 Task identity，通过 current binding + TaskSessionLineage 区分当前与历史执行面；initiator 可从 checkpoint 显式派生新 Session/Run。每个 AgentTurn 独立使用固定 24 小时、任何状态不暂停的 wall-clock hard deadline；P0 不建立 WorkerRun/Task/Tool 时间预算、累计时长、Grant 或续作预算。普通进程随 Turn 清理，只有显式 SessionProcess 可跨 Turn；已接受的 SessionProcess 不随来源 Turn 的任一终态自动停止。Live preview 通过 Backend PreviewProxy 访问 exact declared port，不发布 Worker host port。见 [ADR 0048](docs/adr/0048-make-workersession-primary-and-task-lightweight-in-p0.md)、[ADR 0049](docs/adr/0049-serialize-top-level-work-per-workersession.md)、[ADR 0050](docs/adr/0050-require-explicit-workersession-reuse-and-snapshot-start-context.md)、[ADR 0051](docs/adr/0051-count-continuity-holding-workersessions-against-user-quota.md)、[ADR 0052](docs/adr/0052-make-terminal-workersessions-immutable-and-continue-by-derivation.md)、[ADR 0053](docs/adr/0053-preserve-task-identity-across-workersession-absolute-expiry.md)、[ADR 0055](docs/adr/0055-use-a-fixed-agentturn-hard-deadline.md)、[ADR 0063](docs/adr/0063-manage-cross-turn-background-processes-explicitly.md)、[ADR 0064](docs/adr/0064-preserve-sessionprocess-across-agentturn-terminal-reasons.md) 与 [ADR 0065](docs/adr/0065-proxy-sessionprocess-preview-through-backend.md)。

## 12. 细化文档索引

本文件保留项目定位和需求总览；更细的需求、领域、协议和图建议优先维护在以下文档：

- [0001-requirements-technical-design.md](docs/grilling/0001-requirements-technical-design.md)：针对本文件和 `TECHNICAL_DESIGN.md` 的第一轮 grilling 问题清单。
- [requirements-matrix.md](docs/requirements/requirements-matrix.md)：P0/P1/P2 需求、验收标准、owner context 和未决问题。
- [CONTEXT.md](CONTEXT.md)：领域词汇表。
- [context-map.md](docs/architecture/context-map.md)：DDD bounded context、published language 和依赖规则。
- [frontend-information-architecture.md](docs/architecture/frontend-information-architecture.md)：WorkerSession 主交互面、轻量 Task 索引、Workspace/Artifact/Memory 页面边界与读模型 identity。
- [frontier-projection-sync-protocol.md](docs/architecture/frontier-projection-sync-protocol.md)：ProjectionSnapshot、typed SSE delta、ViewCursor、replay/resync、command convergence 与浏览器鉴权。
- [agent-display-stream-durability.md](docs/architecture/agent-display-stream-durability.md)：DisplayCheckpoint/TransientDisplayDelta 双 lane、checkpoint triggers、客户端 reducer、背压和故障恢复。
- [display-part-schema-and-adapters.md](docs/architecture/display-part-schema-and-adapters.md)：Switchback-owned DisplayPart v1、8 种 payload、外部协议 adapter、未知事件与 renderer/versioning 规则。
- [frontend-runtime-and-state-ownership.md](docs/architecture/frontend-runtime-and-state-ownership.md)：React/Vite SPA、同源部署、Router/Query/View store 分工、assistant-ui adapter、安全和兼容发布。
- [delivery-card-projection-and-callbacks.md](docs/architecture/delivery-card-projection-and-callbacks.md)：DeliveryCardProjection、CardInstance、卡片类型/动作、callback fencing、provider capability 与过期降级。
- [delivery-interaction-design.md](docs/architecture/delivery-interaction-design.md)：Delivery 端到端交互总设计，覆盖共享 Conversation 角色、入站路由、Workspace/Task/Worker/卡片流程、状态所有权、幂等和失败恢复。
- [channel-thread-binding-and-routing.md](docs/architecture/channel-thread-binding-and-routing.md)：ChannelThreadBinding、Assistant-default 路由、显式 InteractionTargetRef、幂等、授权与 topic fallback。
- [channel-workspace-resolution.md](docs/architecture/channel-workspace-resolution.md)：未解析 Conversation、显式 Workspace 选择/CreateWorkspace、TaskSpec snapshot、rebind、撤权与不迁移规则。
- [conversation-rollover-on-workspace-rebind.md](docs/architecture/conversation-rollover-on-workspace-rebind.md)：Workspace boundary 变化时的 Conversation successor、handoff、并发消息边界、scope 隔离和 lineage。
- [workspace-channel-share-and-disclosure.md](docs/architecture/workspace-channel-share-and-disclosure.md)：WorkspaceChannelShareGrant、ChannelDisclosureDecision、共享字段 allowlist、audience reconciliation、ephemeral/Web 降级与历史披露残余风险。
- [m2-assistant-task-dispatch.md](docs/architecture/m2-assistant-task-dispatch.md)：M2 Assistant 架构、所有权、时序、API、配置、数据位置、故障恢复、回滚和已知边界。
- [m2-assistant-task-dispatch-manual-acceptance.md](docs/architecture/m2-assistant-task-dispatch-manual-acceptance.md)：真实 Assistant provider + 真实 Codex Worker 的本地验收、浏览器检查和降级操作手册。
- [m2-assistant-task-dispatch-requirement-audit.md](docs/architecture/m2-assistant-task-dispatch-requirement-audit.md)：49 个 M2 delta scenario 到 unit/integration/Web/failure/real evidence 的审计矩阵。
- [graphql-sse-client-server-basics.md](docs/research/graphql-sse-client-server-basics.md)：GraphQL、SSE、Agent C/S 四通道和 P0 选型解释。
- [agent-frontend-technology-landscape.md](docs/research/agent-frontend-technology-landscape.md)：Vercel AI SDK、assistant-ui、AG-UI/CopilotKit 等 Agent 前端技术的一手资料对比与 Switchback 映射。
- [workspace-storage-and-apply-explainer.md](docs/architecture/workspace-storage-and-apply-explainer.md)：WorkingCopy 持久布局、StagingRevision、fencing 和原子 Apply 示例。
- [tenant-workspace-authorization-explainer.md](docs/architecture/tenant-workspace-authorization-explainer.md)：Tenant 硬边界、Workspace 角色、Worker 委托授权和外部权限分层。
- [trace-artifact-retention-and-analytics-explainer.md](docs/architecture/trace-artifact-retention-and-analytics-explainer.md)：TraceManifest、冷热分层、轻量运营、删除与效果看板。
- [codeplatform-managed-b2-explainer.md](docs/architecture/codeplatform-managed-b2-explainer.md)：受管 B2 信任边界、权限 Profile、Token 续期、provider 结果核查和 A/C fallback。
- [postgresql-tasksaga-explainer.md](docs/architecture/postgresql-tasksaga-explainer.md)：历史 TaskSaga 方案和仍可复用的 PostgreSQL 同事务、持久调度、poller fencing 机制；P0 领域基线以 ADR 0048 为准。
- [diagrams.md](docs/architecture/diagrams.md)：系统上下文、容器图、时序图、状态机和概念数据模型。
- [core-protocols.md](docs/contracts/core-protocols.md)：核心协议字段、约束和版本策略。
- [acp-codex-runtime-liveness-and-session-recovery.md](docs/research/acp-codex-runtime-liveness-and-session-recovery.md)：固定版 ACP/Codex 能力、活性证据、会话恢复、未知事件兼容和 Docker spike 验收。
- [execution-budget-defaults.md](docs/research/execution-budget-defaults.md)：已被 ADR 0055 取代的分层预算调研历史，仅用于解释早期取舍。
- [codeplatform-worker-git-auth-tradeoffs.md](docs/research/codeplatform-worker-git-auth-tradeoffs.md)：Git/GitHub/GitLab 鉴权续期事实和 A/B2/C 灵活性比较。
- [refine-switchback-platform-design](openspec/changes/refine-switchback-platform-design/proposal.md)：当前 OpenSpec change。
