# Interaction / Assistant Domain

## 1. 领域定位

Interaction 领域负责用户对话、需求澄清、上下文装配和 TaskSpec 草稿生成。它的核心产物不是代码，而是“足够明确、可执行、可审计的任务规格”。

Assistant 不直接执行重型开发任务，不直接拥有 Worker runtime、shell、repo 写权限或生产系统权限。

## 2. 聚合与实体

| 类型 | 名称 | 说明 |
| --- | --- | --- |
| Aggregate Root | `Conversation` | 单用户或 provider channel 与 Assistant 的长期对话容器；群话题 P0 共享一个 channel-scoped generation。 |
| Aggregate Root | `ConversationRollover` | 已解析 Workspace 边界变化时，原子创建 successor、冻结 source 并推进 channel binding 的操作记录。 |
| Aggregate Root | `AssistantSession` | 一次 Assistant 运行，会加载上下文并生成回复/TaskDraft。 |
| Entity | `Turn` | 用户、Assistant、系统、工具消息的一轮记录。 |
| Entity | `TurnWindow` | 可写入对象存储的滑动窗口。 |
| Entity | `TaskDraft` | Assistant 生成但尚未被 Task Orchestration 接收的任务草稿。 |
| Entity | `TaskDraftAdoption` | 另一个 participant 显式 fork source TaskDraft 为自己发起的新 lineage 的不可变记录；不是 ownership transfer。 |
| Entity | `ConversationHandoff` | predecessor 到 successor 的不可变、结构化、带 scope/policy/evidence 的交接内容。 |
| Entity | `ConversationParticipant` | channel Conversation 中的真实参与用户及 participant/controller 角色。 |
| Value Object | `ConversationScope` | `user` 或 `channel` 交互主体范围。 |
| Value Object | `ContextPackRef` | 本次运行加载的 Truth/Resource/Rule/Skill 上下文引用。 |
| Value Object | `ClarificationQuestion` | 面向用户的澄清问题。 |
| Value Object | `IntentRouteDecision` | Assistant 判断用户意图应创建新 Task、继续已有 WorkerRun，还是仅回答说明。 |
| Value Object | `TaskCreationTriggerDecision` | 按 interaction scope 判断当前 authenticated surface 是否可生成绑定 exact TaskDraft 的创建意图。 |

## 3. 状态机

`AssistantSession`：

```text
created
-> loading_context
-> reasoning
-> waiting_user
-> task_drafted
-> dispatched
-> summarized
-> failed
```

不变量：

- `task_drafted` 之前必须有当前用户输入、上下文摘要、权限范围。
- `dispatched` 必须关联一个 `TaskSpec` 和 `correlation_id`。
- `dispatched` 还必须关联 active ExplicitTaskCreationIntent；ready TaskDraft 本身不是创建授权。
- Shared TaskDraft confirmation 的 actor 必须等于 immutable initiator；non-initiator contribution/controller role 不改变 initiator。
- `waiting_user` 必须说明缺失信息和推荐默认值。
- 超预算上下文必须在 `ContextPack.dropped` 中记录原因。
- `Conversation.status=superseded` 后不得接收新 Turn；历史读取仍按原 Workspace scope 鉴权。
- channel-scoped Conversation 的每个 Turn 必须保留真实 actor；binding creator/controller 不能替代发言人身份。
- participant 可提交普通 Turn；controller 才可管理默认 Workspace、rollover、Conversation lifecycle 和 controller 委派，但每个动作仍需独立目标授权。
- Provider membership、WorkspaceMembership 和 WorkspaceRole 都不能自动授予 controller；显式角色变更不得移除最后一个 active controller。
- 最后一个 controller 因撤权失效时 Conversation 进入 `control_orphaned`，fencing context/lifecycle/control mutation，直到策略授权的 channel administrator 显式恢复控制权。
- 一个 active ChannelThreadBinding revision 只指向一个 current primary Conversation；离开已解析 Workspace 时必须通过 ConversationRollover 改变该 identity。

## 4. Context Compiler

Assistant Prompt 不应直接拼接所有数据源。推荐单独建 `ContextCompiler` 应用服务：

输入：

- conversation id / generation / user id / tenant id / project id
- active WorkspaceContextResolution 和 optional authorized ConversationHandoff
- 当前 user message
- token budget
- allowed scopes
- task intent

候选来源：

- 当前 Conversation generation 的最近 Turns
- 当前 generation 的安全 handoff items
- 对话/任务阶段摘要
- Truth
- Rules
- Skill manifest
- Inspiration Resource Catalog
- 项目文档和飞书/妙记索引

输出：

- `ContextPack`
- 被丢弃上下文及原因
- source refs
- token estimate

加载顺序建议：

1. 系统规则和安全策略。
2. 当前对话最近 Turns。
3. 与用户/项目强相关的 active Truth。
4. 与任务强相关的 Resource summary。
5. Skill/MCP 可用能力列表。
6. 低优先级历史摘要。

ContextCompiler 默认只读取当前 Conversation generation。Predecessor Turns/summary 不因相同 provider topic 或 lineage 自动成为候选；只有已授权的 handoff item 可进入 successor ContextPack。

## 5. 应用服务

| 服务 | 命令 | 输出事件 |
| --- | --- | --- |
| `ConversationAppService` | `AcceptMessage`, `CloseChannelConversation` | `MessageAccepted`, `ChannelConversationClosed` |
| `ConversationParticipationService` | `MaterializeParticipant`, `DelegateController`, `RevokeController`, `RecoverConversationControl` | `ConversationParticipantChanged`, `ConversationControlOrphaned`, `ConversationControlRecovered` |
| `ConversationRolloverService` | `RolloverConversation`, `CarryContextAcrossWorkspaces` | `ConversationRolledOver`, `ConversationSuperseded`, `ConversationHandoffCreated` |
| `AssistantRunService` | `RunAssistantTurn` | `AssistantTurnStarted`, `ClarificationRequested`, `TaskSpecDrafted`, `AssistantTurnFailed` |
| `TurnArchivalService` | `ArchiveTurnWindow` | `TurnWindowArchived`, `TraceReadyForEvolution` |
| `ContextCompilerService` | `CompileContextPack` | `ContextPackCompiled` |

## 6. 领域事件

```text
MessageAccepted
ConversationParticipantChanged
ConversationControlOrphaned
ConversationControlRecovered
ChannelConversationClosed
ConversationRolledOver
ConversationSuperseded
ConversationHandoffCreated
AssistantSessionStarted
ContextPackCompiled
AssistantTurnRecorded
ClarificationRequested
TaskSpecDrafted
TaskDispatchRequested
TurnWindowArchived
AssistantSessionFailed
```

事件要求：

- `MessageAccepted` 是用户可见链路起点。
- `TaskSpecDrafted` 必须带 TaskSpec snapshot，不依赖后续重建 prompt。
- `TurnWindowArchived` 必须带对象存储 URI 和 checksum。
- `ConversationRolledOver` 必须带 source/target identities、Workspace resolution refs、handoff ref、resulting binding revision 和 idempotency evidence。

## 7. 端口

| Port | 说明 |
| --- | --- |
| `ConversationRepository` | 存取 Conversation、Turn metadata、AssistantSession。 |
| `ConversationParticipantRepository` | 存取 participant/controller revision，执行 last-controller 和 orphan recovery 并发约束。 |
| `ConversationRolloverRepository` | 存取 rollover/handoff、按 idempotency 返回原 successor，并与 binding/source revision 事务一致。 |
| `TraceStore` | 写 Assistant Turns / archived JSONL。 |
| `ContextPackProvider` | 从 Truth/Skill 及 capability-gated 的可选 Inspiration 获取预算内上下文；Inspiration absent/disabled/empty 时仍正常返回。 |
| `ModelPort` | 通过 LLMProxy 调用模型。 |
| `TaskSpecPublisher` | 向 Task Orchestration 提交 TaskSpec。 |
| `EventPublisher` | 写 outbox / publish domain events。 |

## 8. TaskSpec 生成规则

Assistant 只有在以下条件满足时才派发 Worker：

- 目标可验证。
- 输入上下文足够明确。
- 目标 Workspace 已由显式 WorkspaceContextResolution 确认；非代码任务不要求 Repository，代码任务的一个或多个 RepositoryCheckout 可在执行前解析。缺失 Workspace 时只能继续澄清或先执行独立 CreateWorkspace/选择动作，不能由默认策略在派发时静默创建。
- 权限范围明确。
- 验收标准至少有一条。
- 已明确审批/副作用策略、可用能力与 Runtime 配置；TaskSpec 不携带时间字段，实际 AgentTurn 统一使用固定 24 小时 hard deadline。
- 任务类型适合 Worker，而不是只需要 Assistant 直接回答。
- 当前交互已按 TaskCreationTriggerPolicy 生成绑定 exact TaskDraft/spec/readiness/Workspace 的 ExplicitTaskCreationIntent；否则只可展示 ready TaskDraft 确认状态。

如果不满足，应返回 `ClarificationRequested`。

判定分两层：

- Assistant 使用类似 brainstorming/clarify 的渐进式询问做语义判断，按 TaskReadinessPolicy 对 goal、deliverable/acceptance、scope/target、constraints、context/access、risk/approval、execution policy 标记 `known`、`assumed`、`blocking_unknown` 或 `not_applicable`；它不能因为 JSON 字段非空就假定任务清晰。
- TaskSpec validator 只负责 assessment/TaskSpec schema、必填字段、ExecutionPolicy 可解析性、引用权限、能力授权和 approval policy 等硬校验，不替代 Assistant 的语义判断。
- 只有 assessment 为 ready、无 blocking unknown、validator 通过且存在匹配 exact draft/spec 的 active creation intent 才派发。允许的低风险默认值必须来自版本化 default catalog，并作为 assumptions 写入 TaskSpec；权限、冲突目标、生产/外部/不可逆副作用、关键访问缺失和不可验证目标不能默认。
- Group ordinary message 即使 ready 也只形成 Assistant Turn 和 ready TaskDraft；结构化 `/task`/form 或注册 ready-draft confirmation 才生成 intent。P2P/private/API 明确 create request 可按 scope policy 直达。草稿、readiness、Workspace 或 Conversation generation 实质变化后旧 intent stale。
- Non-initiator 对 shared draft 必须显式 AdoptTaskDraft：创建新 id/initiator/derived-from lineage，重新校验可复制 context、Workspace/actor/readiness/policy，并由 adopter 确认。Source intent、grant、secret、private evidence 与 actor-specific assumptions 不继承；source draft 不改变。
- Conversation 可以在 Workspace 未解析时进行 tenant/user-scope 脑暴与澄清；Workspace-scoped Truth/Inspiration/Repository/Trace/Artifact、TaskSpec 和 WorkerSession 必须等待显式 WorkspaceContextResolution。
- WorkspaceContextResolution 由用户通过 Web selector、当前 card/结构化 command 或显式 CreateWorkspace 结果确认。用户全局默认、唯一候选、URL、最近活动和模型判断只用于候选排序，不形成 resolution。
- TaskSpec 派发时 `workspace_id` 和 resolution/Membership evidence 必填；Task Orchestration 不得静默推断或创建 Workspace。离开 resolved Workspace 的 rebind 通过 successor 只影响未来 context/TaskDraft，不迁移已有资源。
- 首次 unresolved->Workspace 保持原 Conversation；已有 resolved Workspace 历史后，rebind/clear/access revoke 必须 rollover 到 successor。source 进入 superseded，切回旧 Workspace 也新建 generation。
- 默认 handoff 只由结构化 Workspace-neutral goal/constraint/assumption/blocker 组成。受保护 carry-over 需要独立 user intent、source read、target context、classification 和 exact source evidence；模型只能建议，不能授权。
- Assistant 每条澄清消息只问一个当前最高优先级阻塞问题。连续三问后产生 ClarificationCheckpoint，展示 TaskDraft、assumptions、blockers 和证据。无 blocker 时可接受 assumptions 并形成 ready draft；只有同时具备 exact active intent 才派发，否则请求创建确认。存在 blocker 时只能继续、缩小范围或取消。用户选择继续后重置下一组三问预算，不设置会话总轮次硬上限。

Assistant 侧后续消息路由：

- 新目标、不同验收标准或不同交付边界：创建新 TaskSpec/Task。
- 明确延续现有目标并指向已有 WorkerSession：请求在原 Task/Session 中启动后续 WorkerRun。
- 仅询问解释、状态或元信息：Assistant 直接回答或查询 projection，不创建执行任务。
- 用户已经进入 Worker 详情页：该消息属于 AgentTurn，由 WorkerSession 路径处理，不由 Assistant 二次分类。

## 9. 失败策略

- 模型失败：可重试有限次数；仍失败则给用户可读错误。
- 上下文加载失败：降级加载最小上下文，并记录 `ContextPackPartial`。
- TaskSpec 校验失败：Assistant 重新澄清或修复 TaskDraft。
- 对象存储失败：不应丢 Turns；先写数据库状态 `archive_pending`，后台补偿。
- Rollover transaction 失败：source Conversation 与 binding 保持不变；commit 后响应丢失按 idempotency key 返回原 successor，禁止重建第二 generation。

## 10. P0 实现建议

- 先实现单 Assistant service，不拆多 Assistant 类型。
- Prompt 产出必须是结构化 TaskSpec JSON + 用户可读解释。
- 最近 Turns 可先存在关系库，长窗口归档对象存储。
- ContextPack 先用 stub/静态规则，接口保持可替换。
- 所有 Assistant 模型请求经 LLMProxy，记录 trace id、task id、conversation id。
- 用户明确要求 Assistant 安装某个 Skill 时，该 Turn 可作为 `CapabilityInstallGrant` 证据；实际安装仍调用 Skilling control plane。
