# Platform Domains: Gateway / EventCenter / LLMProxy / Delivery / Skilling

## 1. 领域定位

这些模块与 Identity & Access 是 Switchback 的平台骨架。它们不直接完成代码任务，但决定系统是否可靠、可控、可观测、可扩展。

模块边界：

- Gateway：同步命令、关键元信息查询和鉴权，不执行长任务。
- EventCenter：事件分发和投影，不做业务事实源。
- Frontier：面向 Frontend 的用户可见读模型和实时 SSE，不执行任务。
- Delivery：飞书等通道适配，负责触发、选择/对话和结果卡片，不拥有任务状态；结构化创建面或 exact ready-draft confirmation 可以直接提交 TaskSpec，但不绕过 readiness、intent、validator、权限和审批策略。
- LLMProxy：模型治理，不承载业务 prompt 编排。
- Skilling：能力注册和授权，不直接执行能力。
- Identity & Access：拥有 TenantMembership、WorkspaceMembership、AuthorizationDecision、ExecutionAuthorizationLease、WorkspaceChannelShareGrant 和 ChannelDisclosureDecision；其他模块不复制角色或群披露判断。

## 2. Identity & Access

P0 授权基线：

- Tenant 是硬边界，一个 Workspace 只属于一个 Tenant；跨 Tenant 请求在角色判断前拒绝。
- 访问同时要求 active TenantMembership 和显式 WorkspaceMembership。Project/Team 不隐式继承权限。
- WorkspaceRole 使用可组合 viewer/editor/reviewer/owner；reviewer 不隐式拥有 editor，Workspace 始终至少有一个 owner。
- AuthorizationDecision 记录 actor、action、resource、allow/deny reasons、Membership revision、Policy revision、constraints 和 expiry。
- Worker 以用户身份取得短期 ExecutionAuthorizationLease，不使用平台服务身份扩大 Workspace 权限。
- WorkspaceRole 与外部能力分离；外部动作继续检查 Capability/ResolvedToolPolicy、ApprovalGrant、SecretLease 和 provider authorization。
- Membership/Policy 撤销会触发 command/read/Apply/Artifact/secret fencing，运行中 WorkerSession 保留证据并进入 access-revoked waiting 状态。
- WorkspaceChannelShareGrant 是 Workspace owner 对一个精确 provider group audience 的独立披露授权，不等于 WorkspaceContextResolution、Membership、Web access 或 action grant。P0 profile 只允许 owner projection 标记为 channel-shareable 的 bounded summary。
- 每次共享 card send/update 必须基于当前 grant/policy/provider snapshot/source classification 生成 ChannelDisclosureDecision。audience 扩张、群外部化/公开化、snapshot stale/unknown、Grant revoke/expire 或 owner Membership 失效时 suspend rich disclosure 并降级；历史 provider 内容只 best-effort cleanup 并保留 residual-exposure audit。

完整角色矩阵、判定顺序、时序和撤权流程见 [Tenant And Workspace Authorization Guide](../architecture/tenant-workspace-authorization-explainer.md)，群聊披露见 [Workspace Channel Share And Disclosure](../architecture/workspace-channel-share-and-disclosure.md)。

## 3. Gateway

职责：

- HTTP/RPC 入口。
- 鉴权、租户识别、请求幂等。
- 接收 Assistant message、关键元信息查询/操作、任务查询、会话查询和产物查询。
- 写入 command/outbox 并快速返回。

不职责：

- 不运行 Agent。
- 不等待 Worker 长任务。
- 不直接访问对象存储拼装大 trace。

P0 API：

```text
POST /v1/conversations/{id}/messages
GET  /v1/conversations/{id}
GET  /v1/tasks/{id}
GET  /v1/tasks/{id}/timeline
GET  /v1/artifacts/{id}
POST /v1/tasks/{id}/cancel
```

## 4. EventCenter

职责：

- 接收 outbox relay 发布的 IntegrationEvent。
- 事件 schema 校验和版本路由。
- 订阅、重试、DLQ、replay。
- 维护面向 Frontier/Delivery 的 projection。

事件字段要求：

```text
event_id
schema_version
event_type
stream_id
sequence
tenant_id
user_id
aggregate_type
aggregate_id
trace_id
correlation_id
causation_id
idempotency_key
visibility: internal|user
occurred_at
payload
```

顺序与去重：

- 每个 EventStream 只有一个 StreamOwner。Owner 在业务状态与 Outbox 同一事务中分配单调 sequence；EventCenter 不重编号。其他 context 通过 command 请求 owner 产生事件。
- Projection 按 `stream_id + sequence` 应用：相同 event id 重投幂等忽略；未来 sequence 进入持久 gap buffer；同一 stream/sequence 对应不同 event id 进入 integrity incident/DLQ。
- 跨 stream 没有全局业务顺序。`correlation_id + causation_id` 表达因果，`occurred_at` 只用于审计和显示。
- Timeline projector 为每个用户可见 timeline 分配 `timeline_epoch + TimelineCursor`，供 SSE resume、分页和稳定追加；迟到事件追加新 cursor 并标记 `late=true`。普通 replay 复用原 cursor，无法保留 cursor mapping 的全量重建递增 epoch 并要求客户端 resync。
- P0 PostgreSQL outbox dispatcher 以 `stream_id` 分组投递；P1 RocketMQ message group 复用 `stream_id`。两者都以 payload sequence 为权威，并共享 duplicate/gap/DLQ/replay contract tests。

完整图示、事件示例、事务伪代码和消费算法见 [Event Ordering And Timeline Guide](../architecture/event-ordering-explainer.md)。

用户可见事件示例：

```text
AssistantMessageDelta
ClarificationRequested
TaskQueued
WorkerRunStarted
WorkerProgressUpdated
TaskWaitingUser
ArtifactReady
TaskSucceeded
TaskFailed
```

内部事件示例：

```text
OutboxPublished
ProjectionUpdated
DeadLetterCreated
WorkerHeartbeatMissed
ModelBudgetExceeded
TruthCandidateGenerated
```

## 5. Frontier

职责：

- 维护用户可见 timeline。
- 为各 TimelineProjection 分配 append-only timeline epoch/cursor，使用 `epoch:cursor` 作为 SSE `Last-Event-ID` 和分页边界；保留 source stream/sequence、occurred_at、causation 和 late 标记。
- 向 Frontend 提供 SSE 和必要的 projection 查询接口。
- 屏蔽内部重试噪声。
- 展示产物、进度、等待输入、失败原因。
- 与 Frontend 共同维护可版本化的展示事件/schema，但不接收 Worker 执行命令。
- 为 Worker 详情页投影 AgentTurn、active WorkerRun、阶段性 report/patch、ApprovalRequest 和 ArtifactAction；执行命令仍经 Gateway 进入 WorkerSession application service。
- 维护 WorkerSession 主交互面和轻量 Task 索引：产品 Worker 面向 `worker_session_id`，TaskSummary 不复制 chat/runtime control；不把 Worker 页面映射成后端服务实例。
- Home、Task、Worker、Workspace、Artifact 与 Memory View 使用 canonical identity 和 permission-aware related refs；Frontend 不从 raw event/Trace 自行拼装权威状态。
- Frontier 为每个 page View 返回带 epoch/cursor/version 的完整 ProjectionSnapshot，并通过 typed SSE ProjectionDelta 增量推进；Snapshot cursor 与数据同版本提交，消除 query/subscribe race。
- SSE 以 `view_epoch:view_cursor` 作为 event id，按 ProjectionReplayWindow 续传。旧 epoch、cursor 淘汰、gap 或 base version 不匹配时返回 `resync_required`；schema 不兼容返回 `client_upgrade_required`。
- Gateway command 返回 accepted + command_id；Frontend pending 只作临时显示，最终由 ProjectionDelta/Snapshot 收敛。GraphQL 不进入 P0，未来只能作为同一 ViewProjection 的 read adapter。
- Agent display 采用 durable DisplayCheckpoint + transient DisplayDelta 双 lane。Checkpoint 经 ViewProjection/SSE cursor 重放；transient chunk 不推进 ViewCursor/version，只按 stream/part/base checkpoint/sequence 做实时渲染。
- Frontier 对 transient queue 有界并可 coalesce/drop progress pulse；Tool/Approval/Artifact/error/terminal 与完整 message checkpoint 不得丢。gap 只恢复该 display part/checkpoint，不能改写 owner state。
- Canonical DisplayPart 由 Switchback 定义为 `text/reasoning_summary/tool/activity/approval/artifact/source/error`。Frontier 接受 adapter 产生的 canonical DisplayMutation，不暴露 ACP/AG-UI/AI SDK native payload；RendererRegistry 按 schema/type 渲染并对 unknown critical type 要求升级。
- DisplayPart actions 只投影 permission-filtered Gateway command refs。Delivery 可以降级为卡片摘要或 Web deep link，但不能根据 part payload 自行批准、执行或改写 owner state。
- P0 Frontend 是 React/TypeScript/Vite SPA；生产不要求 Node server。React Router 管 route identity，TanStack Query 管 HTTP lifecycle，独立 ViewProjectionStore 管 Snapshot/SSE materialization、transient overlay 和 PendingCommand convergence。
- assistant-ui 只能作为 Worker Chat ExternalStore adapter，不能拥有 WorkerSession/message/Tool/Approval identity 或执行权限。Frontend state-changing request 始终通过 Gateway，浏览器同源 session/CSRF/idempotency 规则保持不变。
- Delivery 使用独立 DeliveryCardProjection/CardInstance：projector 消费 owner events、authorized public facts 与 durable Display summary，渲染 task_progress/waiting_user/approval/result；禁止直接映射 Web renderer 或 raw Trace。
- inline card action 仅允许当前 owner action 暴露的 create-task、adopt-task-draft、choice/text、once/session approval、cancel/accept/rework；create-task 只用于 initiator 的 `waiting_user` + exact `task_draft`，adopt-task-draft 只创建新 actor-owned draft。Artifact review、patch apply/publish/PR/production/Skill/Tool/unrestricted Worker chat deep-link Web。
- callback adapter 验证 provider signature、actor/binding/membership、opaque token、render revision、expiry 和 duplicate identity，再调用 Gateway。DeliveryProviderCapability 管 provider 时限/版本/限流；accepted callback 不改业务状态，owner evidence 驱动 card convergence。
- 每个 active ChannelThreadBinding 绑定一个 primary Assistant Conversation。无显式 target 的普通消息只形成 Assistant Turn；Task、WorkerSession、Approval 和 Artifact 操作必须携带验证后的 InteractionTargetRef，且不改变默认路由。
- P0 飞书群 topic 的 primary Assistant Conversation 是 channel-scoped shared Conversation。Delivery 把真实 actor 和 participant/controller evidence 传给 Interaction；不得为不同 sender 建立隐藏 per-user Conversation。
- provider-mapped participant 可发 ordinary Turn；只有 current controller 可选择/清除 Workspace、触发 rollover、关闭 channel Conversation 或管理 controller。所有 control/action command 仍按真实 actor 和 target authority 重验，Conversation role 不产生 Workspace/Task/Worker/Approval/Artifact 权限。
- Binding creator 经 provider/Tenant/channel policy 校验后成为首个 controller；delegation/revoke 使用 expected revision 且不能移除最后 controller。撤权导致无有效 controller 时进入 `control_orphaned`，只允许策略定义的安全对话，直到 authorized channel administrator 显式恢复。
- Delivery 不维护 current focus，不从 latest activity/card/Task/Worker、provider parent_id 或模型推断 target。related refs 只供导航，provider topic fallback 只改变 binding identity 形态。
- InboundInteractionEnvelope 记录 provider message/action identity、actor、binding revision、target evidence 和幂等键；同一话题中每个 actor 独立鉴权，binding revoke 只 fencing 通道交互。
- Delivery 提供 Workspace selection adapter，但 WorkspaceContextResolution 归 Interaction。未解析 Conversation 可继续非 Workspace 对话；Workspace-scoped context/Task/Worker 前必须有显式选择，global default/model/link 仅可推荐。
- ChannelThreadBinding 引用 resolution revision。首次 unresolved->Workspace 保持 current Conversation；离开已有 resolved Workspace 时，Delivery 提交带 expected binding revision 的 ConversationRollover command，由 Interaction 创建 successor 并返回 resulting binding revision。Delivery 不复制 predecessor Turns 或自行生成 cross-Workspace handoff。
- Rollover/ordinary-message acceptance 以 binding revision 串行化；stale envelope 不得静默改挂。rebind 不迁移资源，显式 target 使用资源自身 Workspace；selection 不替代 Membership/Policy 重验，也不自动授权群聊 audience 查看 Workspace 内容。
- 群聊 Workspace-derived output 只接受 Identity & Access 的 ChannelDisclosureDecision。Delivery adapter 提供 ProviderChannelSecuritySnapshot/变化事件，projector 只渲染字段 allowlist；缺少 active WorkspaceChannelShareGrant 时共享卡片只能是非资源标识的 minimal notice 和 canonical Web link。
- workspace_summary 不包含 raw Trace、私有 prompt、hidden reasoning、Secret、Tool stdout、内部错误、完整 report 或 patch/diff。actor-private choice/approval/detail 优先 per-user ephemeral card，provider 不支持时 deep-link Web。
- shared card action descriptor 不授予权限；每个 callback 仍按点击者、binding、Workspace、owner action 和 expected revision 独立鉴权。Grant revoke/suspend 后 fencing 新输出并 best-effort 更新/撤回旧消息，不承诺删除已读/引用/导出内容。

读模型：

```text
HomeView
ConversationView
TaskListView
TaskSummaryView
WorkerListView
WorkerDetailView
WorkspaceOverviewView
ArtifactDetailView
MemoryLibrary
ReviewInbox
UsageDashboardView
```

## 6. Delivery

职责：

- 飞书消息、命令、按钮、中间选择/对话和授权回调。
- ChannelThreadBinding 与 provider topic/chat/root-message fallback 映射。
- channel-scoped Conversation participant/controller 映射、角色证据转发和 control-orphaned 渠道呈现；角色事实仍归 Interaction。
- 普通消息 Assistant-default 路由和显式 InteractionTargetRef 验证。
- TaskCreationTriggerPolicy adapter：group ordinary message 只进入 Assistant；结构化 `/task`/form、exact ready TaskDraft confirmation 以及策略允许的 private/API request 才生成创建意图。
- TaskDraft actor adapter：`create_task` 只转发 initiator confirmation；non-initiator 使用 authenticated `AdoptTaskDraft` fork 新 draft，不在 Delivery 内改写 ownership/readiness。
- WorkspaceContextResolution 的 Web/card/command adapter、expected revision 和 selection evidence 转换。
- ConversationRollover 的 expected binding/source revision、intent evidence、idempotency 和 provider notification adapter；backend rollover 成功不依赖飞书通知成功。
- ProviderChannelSecuritySnapshot 获取/订阅/reconciliation，以及 WorkspaceChannelShareGrant 状态变化后的 card 降级和 cleanup result 记录。
- 通道身份映射到内部 User/Tenant。
- 完整结构化任务物化 ready TaskDraft/assessment 后直达 TaskSpec；普通群消息和不完整输入进入 Assistant。
- 对 exact ready TaskDraft 的确认生成绑定 actor/draft/spec/readiness/Workspace/generation 的 ExplicitTaskCreationIntent；stale 或无 target 的自由文本不创建 Task。
- Non-initiator confirm 被拒绝并可获得 AdoptTaskDraft 入口；adoption 重验 actor/context/Workspace 并返回新 draft，不继承 source intent/grants/private evidence。
- Task action adapter：共享卡片只投影 owner 提供的 cancel/accept/rework candidate；callback 传递真实 actor、exact target、expected Task revision 和幂等 evidence，由 Task Orchestration 按 initiator/owner emergency-cancel 规则判定。Delivery 不把 controller/WorkspaceRole/群可见性解释为权限。
- P0 没有时间预算或延期动作；shared card 可显示当前 AgentTurn deadline，但不能改变它。TaskActionGrant 留到 P1。
- 订阅用户可见事件并回传飞书。
- 渲染最终 report/patch 结果卡片和继续交互入口。

关键状态：

```text
channel_message_received
user_mapped
task_draft_ready
task_creation_confirmed
task_created
waiting_user
notification_delivered
delivery_failed
```

规则：

- Delivery 不直接操作 repo、生产环境或对象存储。
- 所有执行请求必须进入 Task Orchestration。
- Delivery 只持有 channel delivery 状态；`waiting_user`、Task 和 WorkerSession 状态由对应领域拥有。
- 普通消息没有显式 target 时只能进入绑定的 Assistant Conversation；创建/更新 Task、Worker 或卡片不能修改默认路由。
- 群 topic 内普通消息共享一个 Conversation history 并记录真实 actor；Delivery 不按用户拆分隐藏 Conversation，也不把 provider membership 当 controller。
- Workspace context、rollover、Conversation close 和 controller 管理命令必须验证 active controller 与 expected revision；controller 不能替代后续 Workspace/resource/action authorization。
- 直达 Worker/Task/Approval/Artifact 需要当前 actor 可验证的目标与意图证据；不得推断 current focus。
- Delivery 可以直接创建 TaskSpec，但必须先完成用户映射、权限校验、TaskSpec validator 和审批策略检查。
- 飞书线程/话题要与 `conversation_id` 或 `task_id` 绑定。
- 用户补充信息要以 `UserInputReceived` 回到 Saga。
- Worker 审批卡片必须携带结构化 ApprovalRequest，并允许用户选择拒绝、once 或受约束的 WorkerSession grant；Delivery 只转发决定，不解释授权范围。
- Task cancel/retry 同样只转发 authenticated intent。TaskInitiator、Workspace owner emergency cancel、未来 explicit grantee 判定与 Task terminal fencing 均归 Task Orchestration。

## 7. LLMProxy / Model Governance

职责：

- 统一模型 API。
- Assistant 和 Worker agent 请求都要经由代理。
- 模型路由、预算、限流、fallback、日志、审计；脱敏是后续治理能力，P0 Trace 暂以保真 session JSONL 为优先，并接入 Langfuse 作为观测投影。
- P0 Codex Worker 容器内运行 co-located LiteLLM，Codex 通过 localhost 访问；上游 provider 由 LiteLLM config 和 Switchback ModelPolicy 路由。
- P0 不实现 LLMGateway。保留的只是 Switchback `ModelPolicy`/`ModelInvocation` 契约：领域层不能依赖 LiteLLM config shape、virtual key API 或专有错误字段。

领域实体：

```text
ModelInvocation
RoutePolicy
UsageAccount
Budget
ProviderCredential
RedactionPolicy
```

请求 metadata：

```json
{
  "tenant_id": "tenant_xxx",
  "user_id": "user_xxx",
  "conversation_id": "conv_xxx",
  "task_id": "task_xxx",
  "worker_run_id": "run_xxx",
  "trace_id": "trace_xxx",
  "purpose": "assistant_turn|worker_agent|evolution_extract|context_rerank",
  "model_policy_id": "engineering-balanced",
  "sensitivity": "normal|sensitive|secret"
}
```

P0 要求：

- 记录 token、耗时、provider、model、错误。
- 支持按 tenant/user/project 限额。
- 支持 fallback。
- 支持敏感字段不落日志。
- 输出 `ModelRequestRouted`, `ModelRequestCompleted`, `ModelRequestFailed`, `BudgetExceeded`。
- 输出 Langfuse trace/span/generation 关联 id。

## 8. Skilling / Capability

职责：

- 连接 Skill Market。
- 管理 SkillPackage、MCPServerConfig、AgentProfile。
- 为 Assistant 提供可用能力列表。
- 为 Worker materialize 能力配置。

实体：

```text
SkillPackage
SkillVersion
MCPServerConfig
AgentProfile
CapabilityGrant
CapabilityInstallGrant
CapabilityBundle
```

P0 AgentProfile：

```text
CodexAgentProfile
  adapter: acp-codex
  acp_client_sdk: github.com/coder/acp-go-sdk@v0.13.5
  acp_agent_server: "@agentclientprotocol/codex-acp@1.1.2"
  codex: "@openai/codex@0.144.1"
  runtime: local-docker-compatible
  image_build_type: docker_build
  model_proxy: litellm_colocated
  auth_injection: env_or_mounted_config
  session_mapping: one active runtime instance per worker session; runtime/agent history = 1:N
  session_idle_timeout: 48h
  session_max_lifetime: 14d
  max_concurrency_per_user: 10
```

CapabilityBundle 输出：

```json
{
  "schema_version": "capability.bundle.v1",
  "task_id": "task_xxx",
  "agent_profile": {
    "id": "cline-default",
    "adapter": "cli",
    "command": "cline"
  },
  "skills": [],
  "mcp_servers": [
    {
      "id": "github-readonly",
      "transport": "stdio|sse|streamable_http",
      "allowed_tools": ["get_issue", "list_prs"],
      "secret_refs": [],
      "network_policy": "trusted_full_network_v1"
    }
  ],
  "rules": [],
  "model_policy_id": "engineering-balanced"
}
```

规则：

- Skill/MCP 默认最小权限。
- secret 通过 lease 注入，不写入 TaskSpec/Trace。
- Worker 是最后一道权限闸门，不能完全信任 Skill Market。
- Capability 版本必须可追溯，便于复盘同一个 WorkerRun。
- 预装 Skill 不需要单独用户安装确认。Evolution 生成或修改的 SkillPackage 必须先通过人工内容/安全审核；发布后在批准 scope 内免去重复安装确认，但仍需来源验证、静态/策略检查和作用域限制。
- 其他 Skill 必须有 `CapabilityInstallGrant`；用户明确让 Assistant 安装时，该对话 Turn 就是授权证据。
- Assistant/Worker 通过 Skilling 内部 API 执行 `SearchSkills`、`PlanInstall`、`RequestInstallGrant`、`InstallSkill`、`EnableSkill`、`DisableSkill`、`UpgradeSkill`、`RollbackSkill`。
- Worker 不得只在本地 workspace 私自安装并把它当成用户能力配置；持久变更必须回到 Skilling control plane。

### 8.1 CodePlatform Access

P0 采用受管 B2，而不是把每条 GitHub 命令翻译成 Switchback Adapter 命令：

- Capability/Identity & Access 生成 repository-scoped RepositoryAccessGrant，区分 `repository.read`、`repository.write`、`pull_request.write` 和 `ci.read`。
- CredentialBroker 持有 GitHub App 私钥、OAuth refresh token 或其他根凭证，按 Grant、Membership、WorkerSession、runtime generation 和 policy 签发短期 RepositoryCredentialLease。
- Worker 镜像中的 `git-credential-switchback` 与 managed `gh` wrapper 只负责取当前 lease 并调用原生 CLI。短期 Token 对 Agent 可用；root/refresh secret 不进入 Worker。
- Grant 为 `worker_session` scope 时，Agent 在该 repository/capability/constraint 内连续操作不逐命令审批；撤权或过期后停止续签并 fencing 新操作。
- P0 禁止 administration、secret/variable、workflow/Actions write、ruleset、repository delete 和组织管理权限。Provider ruleset 保护 default/protected branch、force push 和 delete。
- CodePlatformOperation 记录标准化操作、target refs、能力快照、provider id/result 和 reconciliation。响应不确定时先查 remote/provider 状态，禁止盲重试外部写入。
- Trace/Langfuse/Artifact ingest 必须剔除平台已知 Token；这不依赖后续通用 DLP。
- GitHub 是 P0 provider。P1 GitLab 使用 managed `glab` 和 provider-specific permission profile，复用 Grant/Broker/Operation 契约，不强制最低共同命令 API。
- 安全政策禁止 Token 进入 Agent 时可切方案 A；写 scope 过粗但仍需原生远端只读时可切方案 C。

## 9. P0 基础设施基线

| 能力 | 基线 | 设计理由 |
| --- | --- | --- |
| Code platform | P0 GitHub 受管原生 Git/`gh`；P1 GitLab managed `glab` | GitHub 先验证 B2 Grant/Broker/Token/reconciliation；GitLab 集成环境复用契约，但不是每位开发者必跑依赖。 |
| CI | P0 不部署 Jenkins；只开放受限 CI read | report/patch 闭环只需 Worker 内测试；外部 CI trigger 不属于 P0 产品承诺，GitHub Actions/GitLab CI/Jenkins 后续按 provider capability 接入。 |
| Database | PostgreSQL | 聚合、Outbox、projection、审计和 JSONB 扩展元数据足够覆盖 P0；MongoDB 暂缓。 |
| Task execution | PostgreSQL lightweight Task + ScheduledCommand poller | Task/Transition/Outbox 同事务，`SKIP LOCKED` + lease/fencing 恢复；P0 不部署 Temporal 或公开 TaskSaga。 |
| Event reliability | P0 PostgreSQL outbox/polling；P1 RocketMQ profile | 先验证事件契约、幂等、重试和 SSE；跨服务订阅与吞吐需求成立后再引入 broker。 |
| Object store | P0 local filesystem adapter；P1 S3-compatible adapter | 生产优先公司云对象存储；MinIO 已归档，不应成为新项目长期默认。 |
| Runtime | Docker local，Kubernetes later | Docker 先验证单容器 WorkerSession；K8s 后续提供调度、配额、Pod sidecar 和故障恢复。 |
| Model/knowledge | LiteLLM + OpenViking | LiteLLM 是 P0 模型治理实现；OpenViking 是 active Truth/Resource 强依赖检索投影，治理权威在 Switchback RDBMS/Git。 |

本地 P0 不要求运行 GitLab、Jenkins、RocketMQ、MinIO 或 Kubernetes。它们会把 runtime spike 变成基础设施安装项目，无法更快验证真实 Agent、Trace、SSE 和 report/patch 闭环。

Trace/Artifact 生命周期基线：

- ObjectStore 保存权威 JSONL/Artifact payload，PostgreSQL TraceManifest 保存 segment、checksum、tier、availability 和消费 cursor。
- DataLifecyclePolicy 默认 hot 30 天、warm 至 180 天、cold 长期，不自动物理删除；本地文件系统可先只维护逻辑 tier。
- 生命周期操作审计保持轻量，不建设独立审批系统；记录 actor/action/target/reason/state/result 即可。
- 后端运营视图以排障、Evolution 重放、投影状态和效果看板为主；Dashboard 消费 normalized facts，不扫描原始 JSONL。
- Langfuse 是可重建观测投影，可独立设置更短 retention。

完整流程见 [Trace Artifact Retention And Analytics Guide](../architecture/trace-artifact-retention-and-analytics-explainer.md)。

## 10. 平台可观测性

指标：

- Task success/failure/cancel/timeout rate。
- Queue latency、runtime allocation latency、worker run duration。
- Event publish lag、projection lag、DLQ count。
- Token、cost、model error、fallback rate。
- Truth candidate count、approval rate、retrieval hit rate。
- Artifact size、trace size、object store write failures。
- Repository grant/lease issuance、renew/revoke、provider error、reconciliation 和 secret-redaction incident。
- Trace hot/warm/cold bytes、restore latency、quarantine/delete backlog、analytics/Evolution cursor lag。

日志：

- 用户可见消息。
- 内部领域事件。
- Adapter 原始日志。
- 模型请求元数据。
- 轻量 DataLifecycleAction 和 raw-access 记录。

Tracing：

- 每个外部请求生成 `trace_id`。
- 通过 `correlation_id` 串起 Gateway -> Assistant -> Task -> Worker -> LLMProxy -> EventCenter。

## 11. P0 验收

- Gateway 能接 message 并返回 accepted。
- EventCenter 能接收事件并更新 TaskTimelineView。
- EventCenter 能按 stream sequence 排序、按 event id 去重，并在 P0 outbox 重投后保持 projection 幂等。
- Lightweight Task 能在数据库/relay/poller/Worker ACK 故障下保持单一 Transition，并通过持久 ScheduledCommand 恢复 retry/deadline/reconciliation。
- Delivery 能从飞书创建或映射一个 Task。
- LLMProxy 能记录模型调用元数据和成本。
- Skilling 能返回静态 CapabilityBundle。
- CodePlatform 无 Grant 时 fail closed；有效 Grant 下原生 Git/`gh` 可续签短 Token，管理权限不可申请，已知 Token 不落 Trace，push/PR 不确定结果可核查收敛。
- Frontier 能通过 SSE 展示 Task queued/running/waiting_user/delivered/completed/failed/cancelled、WorkerRun 进度、report 和 patch。
