# Core Protocol Contracts

> 状态：v0.2 草案。
>
> 这些契约是模块间边界的第一批稳定语言。字段名可以随实现调整，但语义、来源、权限和版本策略必须保留。

## Shared Rules

- 每个跨模块 payload MUST 带 `schema_version`。
- 每个异步命令或事件 MUST 带 `idempotency_key` 或可推导的去重键。
- 每个用户或任务相关记录 MUST 带 `tenant_id`，并尽量带 `user_id`、`workspace_id`、`project_id`、`task_id`、`trace_id`、`correlation_id`；P0 ACL 以 user/workspace 为主要隔离范围。
- 对象存储 URI MUST 配套 checksum、retention 和权限快照。
- 内部调试字段 MUST 与用户可见字段分离。

## TaskReadinessAssessment v1

TaskReadinessAssessment 是 TaskDraft 在某一时刻的不可变可执行判断。Assistant 负责语义归类；Delivery/API 可以从明确结构化输入直接生成；Task Orchestration 负责校验 assessment、TaskSpec、权限和策略的一致性。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `task.readiness.v1`。 |
| `assessment_id` | string | yes | 不可变 assessment id。 |
| `task_draft_id` | string | conditional | Assistant/Delivery 草稿 id；纯 API 请求可使用 request id。 |
| `source` | enum | yes | `assistant`、`delivery` 或 `api`。 |
| `policy_snapshot_ref` | object | yes | TaskReadinessPolicy id/revision/scope/hash。 |
| `dimensions` | object | yes | 各维度状态、摘要、evidence refs 和 assumption ref。 |
| `blocking_unknowns` | array | yes | 仍阻止派发的未知项和 blocking rule id。 |
| `assumptions` | array | yes | 命中 default catalog 的显式假设、来源、风险和可撤销性。 |
| `clarification_questions_since_checkpoint` | integer | yes | 自上次 checkpoint 后 Assistant 连续提出的问题数。 |
| `checkpoint_index` | integer | yes | 已产生 ClarificationCheckpoint 次数。 |
| `evidence_refs` | array | yes | 用户 Turns、文档、资源解析和策略证据。 |
| `status` | enum | yes | `clarifying`、`ready_confirmed`、`ready_with_assumptions` 或 `cancelled`。 |
| `evaluated_at` | timestamp | yes | Assessment 生成时间。 |

`dimensions` 固定包含：

- `goal`
- `deliverable_acceptance`
- `scope_target`
- `constraints`
- `context_access`
- `risk_approval`
- `execution_policy`

每个维度状态为 `known`、`assumed`、`blocking_unknown` 或 `not_applicable`。TaskReadinessPolicy 至少定义以下硬阻塞：不可验证的目标/交付结果；会影响错误资源的 scope/target 歧义；冲突约束；生产、外部或不可逆副作用范围不明；缺失必须的身份、权限、凭证或源上下文。硬阻塞不能被用户一句“直接做”或模型置信度绕过。

TaskReadinessPolicy 可以允许低风险、可逆默认值，例如 P0 report/patch 输出、从已显式选择 Workspace 的仓库约定推导测试命令、平台默认 Agent，以及不改变交付边界的实现细节。Workspace 选择、创建和跨 Workspace target 不能成为默认 assumption。每个允许默认值必须有 catalog rule id，并进入 TaskSpec `assumptions`。固定 24 小时 AgentTurn hard deadline 是平台不变量，不是 Task assumption。

不变量：

- Task Orchestration MUST 只接受 `ready_confirmed` 或 `ready_with_assumptions`、`blocking_unknowns` 为空且 TaskSpec validator 通过的快照。
- Assistant MUST 每条澄清消息只提出一个问题，并优先选择最高风险、阻塞最多后续维度的问题。
- Assistant 连续提出 3 个澄清问题后 MUST 生成 ClarificationCheckpoint；用户选择继续后计数重置，可开启下一组三问，不设置会话总追问硬上限。
- ClarificationCheckpoint MUST 展示当前 TaskDraft、已知项、assumptions 和 blockers。仅在无 blocker 且 validator 通过时可向 initiator 提供绑定 exact draft 的 `accept_assumptions_and_create`；该动作同时生成新 assessment 与 ExplicitTaskCreationIntent。否则只提供 `continue_clarification`、`narrow_scope` 和 `cancel`。
- 用户接受 assumptions MUST 生成新的 assessment 和 evidence ref，不能改写旧快照；用户不能通过 checkpoint 豁免权限、审批或其他硬阻塞。
- Delivery/API 的明确请求 MAY 以 0 次澄清生成 ready assessment，但 MUST 使用相同 policy、dimension 和 validator 语义。

## TaskDraft And Adoption v1

TaskDraft 是 Task 创建前的版本化规格容器。P0 对 shared Conversation 使用 initiator-bound ownership：协作补充消息可以推进同一 draft revision，但不会把确认责任转给发言者或 controller。

| TaskDraft Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `task.draft.v1`。 |
| `task_draft_id` | string | yes | 稳定 draft lineage identity。 |
| `tenant_id` | string | yes | Tenant 硬边界。 |
| `conversation_ref` | object | conditional | Conversation id/generation/scope；API-only draft 可使用 authenticated request scope。 |
| `initiator_user_id` | string | yes | 该 draft lineage 的不可变发起人。 |
| `origin_evidence_ref` | object | yes | 发起该 draft 的 Turn、structured command、form 或 API request。 |
| `derived_from_task_draft_ref` | object | optional | AdoptTaskDraft 时记录 source draft id/revision/digest；只表示 lineage。 |
| `workspace_resolution_ref` | object | yes | Exact WorkspaceContextResolution id/revision。 |
| `revision` | integer | yes | 内容、context、assumption 或 readiness 变化时单调递增。 |
| `task_spec_digest` | string | yes | 当前 canonical TaskSpec creation payload digest。 |
| `readiness_assessment_ref` | object | yes | 当前 assessment id/revision/hash。 |
| `status` | enum | yes | `clarifying`、`ready`、`consumed`、`cancelled` 或 `stale`。 |
| `created_at`, `updated_at` | timestamp | yes | 服务端审计时间。 |

`AdoptTaskDraft v1` 是显式 fork command，不是 ownership transfer：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `task.adopt_draft.v1`。 |
| `actor_user_id` | string | yes | 采用该草稿并成为新 initiator 的真实 actor。 |
| `source_task_draft_ref` | object | yes | Exact source id/revision/spec digest/readiness ref。 |
| `conversation_ref` | object | yes | Current shared Conversation id/generation。 |
| `channel_binding_ref` | object | channel yes | Exact binding id/revision。 |
| `workspace_resolution_ref` | object | yes | Current exact Workspace resolution。 |
| `source_evidence_ref` | object | yes | `/task adopt`、Web 或 registered bot action 的 authenticated evidence。 |
| `idempotency_key` | string | yes | Actor + source draft revision + interaction identity 派生。 |

不变量：

- 只有 `TaskDraft.initiator_user_id` 可以对该 exact draft 生成 `ready_draft_confirmation` intent。Controller、其他 participant、WorkspaceRole 或 source Turn contributor 都不能代为确认。
- 非 initiator MUST 先显式执行 AdoptTaskDraft。成功后创建新的 TaskDraft id，`initiator_user_id=actor_user_id` 并保留 `derived_from_task_draft_ref`；原 draft 不变、不失效且不被转移。
- Adoption MUST 重验 actor 的 Conversation participation、source visibility、Tenant/Workspace access、current generation/binding 和每个 context ref。原 initiator 的 ExplicitTaskCreationIntent、ApprovalGrant、SecretLease、RepositoryAccessGrant、私有 Turn/evidence 和 actor-specific assumptions MUST NOT 继承。
- 新 draft MUST 重新生成 actor-specific readiness assessment 和 canonical digest。即使复制后内容不变，adoption 本身也不创建 Task；新 initiator 仍须确认自己的 ready draft。
- Source draft consumed/cancelled、Conversation rollover、Workspace resolution mismatch、不可读取 context ref 或无法安全解释 schema/policy MUST fail closed；不得静默复制受保护内容或迁移到 successor。仅 readiness 或原 initiator authorization stale 时，若 exact payload 仍可读且 generation/scope 有效，MAY fork，但新 draft MUST 从头重验 readiness 与 actor authorization。
- 同一 actor 对同一 source revision 的重复 adoption 返回原 new draft identity。不同 actor 的 adoption 创建不同 lineage；相同 TaskSpec digest 不构成跨 actor 幂等键，也不得自动合并两个 Task。

## ExplicitTaskCreationIntent v1

`TaskReadinessAssessment` 回答“这份 TaskSpec 是否可执行”，`ExplicitTaskCreationIntent` 回答“这个 actor 是否明确要求现在创建一项可跟踪 Task”。两者是独立门槛：ready 不产生创建授权，显式创建动作也不能绕过 blocker、validator、Workspace 或权限校验。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `task.creation_intent.v1`。 |
| `intent_id` | string | yes | 不可变 intent identity。 |
| `tenant_id` | string | yes | Tenant 硬边界。 |
| `actor_user_id` | string | yes | 作出创建决定的已认证 Switchback actor。 |
| `task_draft_initiator_user_id` | string | yes | Intent 接受时必须与 `actor_user_id` 及 TaskDraft initiator 一致。 |
| `interaction_scope` | enum | yes | `channel`、`user` 或 `api`。 |
| `source_type` | enum | yes | `structured_task_command`、`task_create_form`、`ready_draft_confirmation` 或 `explicit_private_request`。 |
| `source_evidence_ref` | object | yes | Provider command/action、Web form、private message 或 API caller 的不可伪造证据。 |
| `channel_binding_ref` | object | channel conditional | Exact ChannelThreadBinding id/revision 和 Conversation generation。 |
| `task_draft_ref` | object | yes | Exact TaskDraft id/revision。结构化直达请求也必须先物化不可变 draft snapshot。 |
| `task_spec_digest` | string | yes | 用户授权创建的 canonical TaskSpec creation payload 摘要；计算时排除 `creation_intent_ref`、待分配 Task identity、传输 metadata 和服务端时间，避免循环引用。 |
| `readiness_assessment_ref` | object | yes | Exact ready assessment id/revision/hash。 |
| `workspace_resolution_ref` | object | yes | Exact WorkspaceContextResolution id/revision。 |
| `trigger_policy_ref` | object | yes | TaskCreationTriggerPolicy id/revision/scope/hash。 |
| `idempotency_key` | string | yes | 从 authenticated source identity + draft revision/digest 派生。 |
| `status` | enum | yes | `active`、`consumed`、`expired` 或 `stale`。 |
| `created_at`, `expires_at` | timestamp | yes | 创建时间与短时有效窗口。 |

P0 `TaskCreationTriggerPolicy`：

- `channel` scope 的 `ordinary_message` MUST 先进入当前 shared Assistant Conversation，即使内容足以得到 ready assessment，也 MUST NOT 直接生成创建意图。
- `channel` scope 只接受显式 `/task`/结构化任务命令、create-task form/button，或对 exact ready TaskDraft 的注册确认动作。自由文本中的“确认”“直接做”若没有不可变 draft target，仍是普通 Assistant Turn。
- 完整结构化 `/task` 请求 MAY 以零次澄清生成 ready assessment 并直接创建；不完整命令只保留“希望创建 Task”的来源证据，待草稿补全或实质变化后仍 MUST 对最终 ready draft 重新确认。
- `user`/P2P/private scope 与 authenticated API MAY 把语义明确的 create/execute instruction 解析为 `explicit_private_request`，但普通咨询、脑暴或模糊请求仍进入 Assistant。
- Controller role MUST NOT 生成或扩大 Task 创建权限。每个创建动作按真实 actor、Workspace、policy 和 TaskSpec 独立重验。
- Shared ready TaskDraft confirmation MUST require `actor_user_id == task_draft_initiator_user_id`。Non-initiator 只能先 AdoptTaskDraft 并确认新 draft。

不变量：

- Task Orchestration MUST 同时重验 active intent、ready assessment、TaskSpec digest、Workspace resolution、actor authorization 和 current policy，才可在一个事务中创建 Task/TaskTransition/Outbox。
- `task_spec_digest` MUST 使用版本化 canonicalization 规则；同一规则同时供 ready-draft render、intent 记录和 Task Orchestration 校验，且 MUST NOT 包含 `creation_intent_ref` 本身。
- TaskDraft/TaskSpec/readiness/Workspace resolution/Conversation generation 任一 revision 或 digest 改变，旧 intent MUST 变 stale；不得静默把旧确认迁移到新草稿或 rollover successor。
- 重复 command/callback/response loss MUST 按同一 idempotency key 返回原 Task identity，不能创建第二个 Task/Transition/Outbox。
- Task 创建 MUST NOT 改变 ChannelThreadBinding primary Conversation、default Workspace 或 current focus。
- Intent actor、TaskDraft initiator 和 TaskSpec `user_id` MUST 相同。Adopted draft 创建的 Task actor 是 adopter，不是 source initiator。

## TaskSpec v1

TaskSpec 是 Assistant/Delivery/API 到 Task Orchestration 的 published language。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `task.spec.v1`。 |
| `source` | enum | yes | `assistant`、`delivery`、`api`。 |
| `tenant_id` | string | yes | 租户边界。 |
| `user_id` | string | yes | 发起用户。 |
| `project_id` | string | recommended | 项目或知识边界。 |
| `workspace_id` | string | yes | 显式选择且当前授权的目标 Workspace；Task Orchestration 不得对缺失值静默创建、替换或推断。 |
| `workspace_resolution_ref` | object | yes | WorkspaceContextResolution id/revision、selection evidence 和 observed Membership revision。 |
| `title` | string | yes | 用户可读短标题。 |
| `goal` | string | yes | 明确期望结果。 |
| `readiness_assessment_ref` | object | yes | 被接受的 TaskReadinessAssessment id、policy revision 和内容 hash。 |
| `creation_intent_ref` | object | yes | Active ExplicitTaskCreationIntent id、trigger policy revision 和 TaskSpec digest。 |
| `assumptions` | array | yes | 已接受的低风险默认假设；没有时为空数组。 |
| `context_refs` | array | yes | 对话、文档、仓库、Truth、Artifact 等引用。 |
| `target` | object | conditional | 仓库、分支、工作区或 report-only 目标。 |
| `constraints` | array | yes | 不允许做什么、必须遵守什么。 |
| `acceptance_criteria` | array | yes | 可验证验收标准。 |
| `runtime_config` | object | yes | 网络、资源、隔离和容器配置；不允许覆盖固定 AgentTurn hard deadline。 |
| `agent_config` | object | yes | AgentProfile、adapter、模型策略。 |
| `capability_refs` | object | yes | Skill/MCP/工具引用。 |
| `approval_policy` | object | yes | 哪些行为需要用户或管理员确认。 |
| `output_policy` | object | yes | 允许的用户交付产物和远端写入边界；P0 默认为 report/patch。 |

### Invariants

- `goal` 和 `acceptance_criteria` 不能为空。
- `workspace_id` MUST 在派发时存在并与 `workspace_resolution_ref`、TaskReadinessAssessment scope/target 和当前 Tenant/WorkspaceMembership 一致；new Workspace MUST 先通过独立幂等 CreateWorkspace + selection command 创建。
- Missing、stale、revoked、cross-Tenant 或 binding revision mismatch MUST fail before Task/TaskTransition/Outbox creation；Task Orchestration MUST NOT fallback to user default、related ref、URL、recent activity 或 model inference。
- `readiness_assessment_ref` MUST 指向 `ready_confirmed` 或 `ready_with_assumptions`、无 blocking unknown 且与当前 TaskSpec 内容 hash/assumptions 一致的快照。
- `creation_intent_ref` MUST 指向同一 actor、TaskDraft revision、TaskSpec digest、readiness assessment、Workspace resolution 和 interaction scope 的 active intent；ready assessment alone MUST NOT authorize creation。
- TaskSpec `creation_intent_ref.task_spec_digest` MUST equal the canonical digest recomputed from the accepted TaskSpec creation payload under the intent's trigger-policy/canonicalization revision。
- `context_refs` 只能引用当前用户/项目有权限访问的资源。
- SingleUserLocalProfile 的 `runtime_config.network_policy` MUST 由服务端解析为 `trusted_full_network_v1`，并与 `network_policy_revision=1`、exact `network_policy_digest` 一起固化。该 profile 允许公网、私网、VPN、LAN、可路由容器和宿主服务的任意出站协议/端口，不做 destination/CIDR 过滤、redirect 重验、payload inspection 或 per-target ApprovalGrant；Worker 仍 MUST NOT 获得 Docker Socket、privileged/host network、任意 host path、published port 或 ambient infrastructure credential。旧 `m1_public_web_restricted_v1` 只允许历史读取和已准入执行，且 MUST 标记 `legacy_unenforced`。
- M1 `RuntimeLease.residency_policy` 由服务端固定解析为 `m1_resident_until_session_terminal_v1`，TaskSpec 不携带且客户端不能覆盖。interactive/running/waiting_user/idle Session 保持 RuntimeInstance 运行；AgentTurn/WorkerRun terminal 不触发 planned stop 或 generation 变化，只有 close、idle/absolute expiry、安全终止或 recovery fencing 才停止/替换。驻留不构成 AgentTurn/Tool dispatch authority。
- M1 `RuntimeLease.resource_profile` 由服务端固定解析为 exact `local_standard_v1` revision：2 vCPU、4 GiB memory、512 PIDs、10 GiB container writable layer。TaskSpec/Frontend/Agent 不接受任意资源数字；WorkingCopy/cache/Agent state 使用独立持久存储 quota。
- SessionDispatchPermit MUST 绑定一次通过的 RuntimeCapacityAdmission evidence。默认 aggregate budget 不超过 Docker Engine 可用 CPU/memory/storage 的 80%；容量不足 MUST 保持原 FIFO 和 resident Session，记录 `runtime_capacity`，且 MUST NOT 创建 active AgentTurn、`started_at` 或 24h deadline。Turn terminal releases capacity idempotently。已确认 OOM/PID/storage hard-limit MUST 收敛为 `resource_exhausted` 而非普通 runtime-loss 自动恢复。
- TaskSpec MUST NOT 携带用户可选的 WorkerRun、AgentTurn 或 Tool 时长；Worker Runtime 对每个顶层 AgentTurn 统一应用 `86400s` wall-clock hard deadline。
- `approval_policy` MUST 允许沙箱内普通文件/命令/测试/report/patch 自动执行，并覆盖新增外部鉴权、跨沙箱、生产写、能力安装和未来远端 branch/PR/CI 动作。
- P0 `output_policy.user_visible_types` MUST 限制为 `report` 和 `patch`；branch、PR、CI trigger 默认禁用。

## Lightweight Task v1

Task 是 P0 的轻量交付跟踪协议。WorkerSession/WorkerRun 拥有交互和执行细节，Task 不复制 runtime 状态。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `task.v1`。 |
| `task_id` | string | yes | 可跟踪交付 identity。 |
| `tenant_id`, `workspace_id` | string | yes | 安全与工作上下文边界。 |
| `task_spec_snapshot` | object | yes | 创建时接受的不可变 TaskSpec。 |
| `task_initiator_user_id` | string | yes | ExplicitTaskCreationIntent 的真实 actor。 |
| `requested_worker_session_id` | string | optional | 用户显式请求复用的 exact WorkerSession；必须由 target intent 支撑。 |
| `worker_session_target_intent_ref` | object | conditional | `requested_worker_session_id` 存在时必填，指向认证 actor 的 exact target evidence。 |
| `worker_session_id` | string | conditional | 当前可执行的主要交互 WorkerSession；queued/allocating 阶段或 ADR 0053 absolute-expiry 收敛后可为空。 |
| `worker_session_lineage` | array | yes | 按服务端顺序追加的 `TaskSessionLineageEntry`；记录历史 Session/Run、绑定原因、释放原因和 checkpoint，不因 current ref 清空而丢失。 |
| `session_context_policy` | enum | yes | 已解析为 `inherit_current`、`fresh_session` 或 `exact_base`。 |
| `session_allocation_sequence` | integer | conditional | 尚未分配 Session 且等待 Tenant/User 容量时的稳定 FIFO 位置。 |
| `allocation_wait_reason` | enum | conditional | P0 容量等待固定为 `session_capacity`；不是 `waiting_user` Task 状态。 |
| `allocation_queued_at` | timestamp | conditional | 服务端提交 SessionAllocationQueue 位置的时间。 |
| `session_queue_sequence` | integer | conditional | Task 在目标 Session 等待 slot 时的稳定 FIFO 位置；离队后保留为审计值。 |
| `queued_at` | timestamp | conditional | 服务端接受当前 Session enqueue 的时间。 |
| `current_worker_run_id` | string | conditional | `running` 时的当前 WorkerRun；Run 终态或 absolute-expiry 收敛后为空。 |
| `status` | enum | yes | `queued`、`running`、`waiting_user`、`delivered`、`completed`、`failed` 或 `cancelled`。 |
| `acceptance_mode` | enum | yes | `auto` 或 `manual`；patch 默认 manual。 |
| `result_refs` | array | yes | 当前 report/patch Artifact refs；无结果时为空。 |
| `waiting_reason` | object | optional | waiting_user 原因和允许动作。 |
| `revision` | integer | yes | 每次有效 TaskTransition 单调递增。 |
| `created_at`, `updated_at` | timestamp | yes | 服务端时间。 |

Task action 最小矩阵：

| Action | P0 actor | Constraint |
| --- | --- | --- |
| `continue` / `rework` | TaskInitiator | Task 必须处于 `queued/running/waiting_user/delivered` 且当前 live Session 可执行；返工创建新 WorkerRun。expired current Session 使用下一动作。 |
| `continue_task_in_new_worker` | TaskInitiator | 当前 Session 已成功 absolute-expiry 收敛；绑定 exact expiry outcome/checkpoint，先把 Task 推进或保持为 `queued`，再走新 Session allocation。 |
| `accept` | TaskInitiator | 仅 `delivered` manual Task。 |
| `cancel` | TaskInitiator；current Workspace owner | owner 只有 emergency cancel，不获得其他动作。 |
| `reassign_queued_session` | TaskInitiator | 仅 queued Task；重验目标 Session/Workspace 后追加到目标队尾。 |

Workspace editor/reviewer、Conversation participant/controller、WorkspaceChannelShareGrant、Task read visibility 或 Artifact review MUST NOT 隐式授予上述动作。P0 不发布 TaskActionGrant 协议；有限委托在 P1 重新设计。

Task 创建时 MAY 尚未分配 `worker_session_id`。只有 Task 分配到 Session 后，Task Orchestration 才能设置 `session_queue_sequence` 并进入该 Session 的队列；分配过程不增加新的 Task status。容量满时使用 `session_allocation_sequence`，它与 `session_queue_sequence` MUST NOT 同时有效。`requested_worker_session_id` 不能由最近活动、唯一候选、Workspace 相同、related ref 或模型推断填充。

`TaskSessionLineageEntry v1` 至少包含 `lineage_entry_id`、`task_id`、`worker_session_id`、可选 `worker_run_id`、`binding_kind=initial|rework|absolute_expiry_continuation`、`bound_at`、可选 `released_at`、`release_reason`、`source_expiry_outcome_ref` 和 `source_checkpoint_ref`。同一 Task 的 lineage MUST append-only；当前引用变化不得改写旧 entry。

每个 Task action command MUST 携带 `actor_user_id`、`task_id`、精确 target、`expected_task_revision` 和 `idempotency_key`。Task Orchestration MUST 在事务开始后重验 Tenant/Workspace membership、actor rule 和当前 Task 状态，再原子更新 Task、追加 TaskTransition 并写 Outbox；重复 command 返回原结果。

`TaskTransition v1` 至少包含 `transition_id`、`task_id`、`idempotency_key`、`expected_revision`、`result_revision`、`from_state`、`to_state`、`trigger`、`result` 和 `occurred_at`。它是审计事实，不拥有独立状态机。

## WorkerSession Execution Slot v1

WorkerSession 是 P0 顶层执行串行化边界。Worker Runtime owns slot；Task Orchestration 只通过幂等 command 请求取得或释放，不得直接更新。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `worker_session_id` | string | yes | execution slot identity。 |
| `bound_task_id` | string | conditional | 当前绑定的非终态 Task；queued 但未取得 slot 的 Task 不在此字段。 |
| `active_worker_run_id` | string | conditional | 当前唯一 active 顶层 WorkerRun。 |
| `session_revision` | integer | yes | slot 每次有效变化时单调递增。 |
| `holder_generation` | integer | conditional | 当前 RuntimeInstance generation fencing evidence。 |
| `updated_at` | timestamp | yes | 服务端时间。 |

Invariants:

- `bound_task_id` 和 `active_worker_run_id` 各自 cardinality MUST 为 `0..1`。
- `running`、`waiting_user` 或 `delivered` Task 通常 MUST 保持 `bound_task_id`。唯一 P0 非终态例外是 ADR 0053 absolute expiry：Worker Runtime 已证明旧 Runtime/WorkingCopy 写权限被 fencing、终态化 Session 并发布 exact expiry outcome 后，旧 slot MAY 释放，Task 把关联移入 lineage；不能证明 fencing 时 Session MUST 保持 `lost` 且不得释放 slot/quota。
- standalone WorkerRun MUST 只在两个 holder 字段均为空时启动。
- `active_worker_run_id` 对应的 Run 若包含 `task_id`，该值 MUST 等于 `bound_task_id`。Task 进入终态并释放 binding 前，其 active Run MUST 已终态或完成可证明的安全取消收敛。
- `StartWorkerRun` MUST 携带 task/run identity、有效 SessionDispatchPermit、expected session revision、runtime generation 与 idempotency key。重复请求返回原接受结果；冲突请求返回 `session_busy`，MUST NOT interrupt 当前 Run。
- AgentTurn `queue` MUST 只排队到当前 `active_worker_run_id`；Task scheduling queue MUST 使用独立 command/read-model 字段。
- Agent 内部 subagent MAY 并发，但 MUST 归属同一顶层 WorkerRun 并共享其 policy snapshot。

## SessionTaskQueue And Dispatch Permit v1

P0 的 SessionTaskQueue 是 Task 上的稳定调度顺序，不引入第二个 Task 状态机。Task Orchestration owns per-session queue revision 与 dispatch gate；Worker Runtime 仍 owns execution slot。

`SessionDispatchPermit v1`：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `permit_id` | string | yes | 单次调度凭证 identity。 |
| `worker_session_id` | string | yes | 目标 Session。 |
| `target_kind` | enum | yes | `task` 或 `standalone_run`。 |
| `target_id` | string | yes | Task 或 standalone AgentTurn/Run request identity。 |
| `session_queue_sequence` | integer | conditional | Task permit 必须等于当前 queue head sequence。 |
| `queue_revision` | integer | yes | 签发时的 per-session queue revision。 |
| `expected_session_revision` | integer | yes | Worker Runtime slot revision。 |
| `idempotency_key` | string | yes | 重复签发/消费收敛键。 |
| `expires_at` | timestamp | yes | 策略定义的短期有效期。 |
| `status` | enum | yes | `active`、`consumed`、`expired` 或 `revoked`。 |

Invariants:

- Enqueue MUST 在 per-session queue gate 下分配单调 `session_queue_sequence` 并推进 queue revision；同一 enqueue idempotency key MUST 返回原 sequence。回滚或取消产生的 sequence gap 合法，MUST NOT 被复用。
- Dispatcher MUST 只为最小合法 sequence 签发 Task permit，MUST NOT 用 `SKIP LOCKED` 跳过一个仍合法的队头。Task priority/source metadata MUST NOT 改变 P0 顺序。
- P0 MUST NOT 提供 priority scheduling、drag reorder 或 preemption。取消 queued Task 使其终态并推进 queue revision；显式改派 MUST 从原队列移除并在目标 Session 队尾获得新 sequence。
- Standalone permit MUST 只在目标 Session 没有 queued Task、没有 active permit 且权限有效时签发。一个先成功签发的 standalone permit 构成线性化点；之后到达的 Task 排在该已保留执行之后。
- Worker Runtime MUST 单次消费 permit，并同时重验 permit target、expiry、queue/session revision、runtime generation 和 slot。过期、stale、错误 target 或重复用于另一 Run MUST fail closed。
- Slot release SHOULD 触发即时 head dispatch；丢失事件、permit 过期或未知响应 MUST 由周期 reconciliation 重评，不能生成第二个 Run。

## WorkerSession Assignment And Start Snapshot v1

`WorkerSessionTargetIntent v1`：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `intent_id` | string | yes | 不可变复用意图 identity。 |
| `actor_user_id` | string | yes | 提交 exact target 的认证用户。 |
| `workspace_id` | string | yes | Session 和 Task 必须共同属于的 Workspace。 |
| `worker_session_id` | string | yes | 被精确指向的 Session。 |
| `source_ref`, `source_revision` | object/string | yes | Worker Detail、注册卡片/命令或 API request 的不可变来源。 |
| `idempotency_key` | string | yes | 重复 target action 收敛键。 |
| `created_at`, `expires_at` | timestamp | yes | 证据有效窗口。 |

`WorkerRunStartSnapshot v1`：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `snapshot_id`, `worker_run_id` | string | yes | snapshot 与 Run identity。 |
| `worker_session_id` | string | yes | 实际执行 Session。 |
| `context_policy` | enum | yes | `inherit_current`、`fresh_session` 或 `exact_base`。 |
| `agent_session_refs` | array | yes | 启动时存在或选中的 AgentSession refs。 |
| `working_copy_checkpoint_id` | string | yes | 首次执行动作前的持久 checkpoint。 |
| `base_workspace_revision_id` | string | yes | WorkingCopy 当前 base revision。 |
| `working_copy_manifest_digest` | string | yes | 实际文件/仓库 manifest digest。 |
| `dirty_state` | enum | yes | `clean`、`dirty` 或无法安全判定时的 `unknown`。 |
| `previous_task_refs`, `previous_worker_run_refs` | array | yes | 启动时可见的连续性谱系；可为空。 |
| `captured_at` | timestamp | yes | 服务端确认的 snapshot 时间。 |

Invariants:

- Reusing an existing Session MUST require a valid intent whose actor, Workspace, Session, source revision and policy are current. Same Workspace, recent/only/idle Session and model inference MUST NOT substitute for intent.
- Valid reuse MUST resolve to `inherit_current` and preserve current WorkingCopy, Agent state/context, AgentSessions and uncommitted changes. The platform MUST NOT silently reset, clean, stash, checkout or replace that state.
- A Task without exact target intent MUST resolve to `fresh_session`, remain queued while a new Session is allocated, and MUST NOT enter any existing SessionTaskQueue.
- `exact_base` MUST identify an exact WorkspaceRevision/checkpoint. A mismatch with an explicitly targeted Session MUST allocate a new Session or remain queued/blocked; it MUST NOT mutate the target Session in place.
- Worker Runtime MUST persist the immutable start snapshot after permit/slot acceptance and before the first Agent prompt, Tool invocation or WorkingCopy mutation. Trace, Artifact and ChangeSet lineage MUST reference the snapshot.
- Reassignment to an existing Session MUST require new exact target intent and inherit its current state. Reassignment to fresh/exact baseline MUST allocate a new Session.

## WorkerSession Continuity Quota And Allocation Queue v1

`WorkerSessionContinuityQuota v1`：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `worker-session-continuity-quota.v1`。 |
| `tenant_id`, `user_id` | string | yes | P0 配额作用域；不跨 Tenant 合并身份。 |
| `policy_snapshot_ref` | object | yes | 当前 limit 与计数规则来源。 |
| `limit` | integer | yes | P0 默认 `10`。 |
| `reserved_count` | integer | yes | 已提交的 continuity slots 数量。 |
| `revision` | integer | yes | 每次 reserve/release 单调递增。 |
| `updated_at` | timestamp | yes | 服务端时间。 |

`WorkerSessionAllocationRequest v1` 至少包含 `allocation_request_id`、`tenant_id`、`user_id`、`request_kind=task|session_continuation`、exact owner ref/revision、`session_allocation_sequence`、Workspace/context policy refs、status 与 `idempotency_key`。

`AllocateWorkerSession v1` command 至少包含 allocation request、expected owner/quota revisions 和 allocation claim evidence。Worker Runtime 的接受结果必须返回同一个 `worker_session_id`、result quota revision 与 owner binding evidence。

Invariants:

- `allocating`、`interactive`、`running`、`recovering`、`waiting_user`、`idle` MUST 各占一个 continuity slot。`lost` MUST 继续计数，直到 reconciliation 恢复或提交 `closed/expired` 终态。只有 `closed/expired` 与 quota release 已提交后才不计数。
- queued/unassigned Task 与 pending Session continuation MUST NOT 占 slot，也 MUST NOT 启动 AgentTurn deadline、WorkerSession idle clock 或 SessionTaskQueue position。
- 配额满时 Task MAY 保持 `queued` 并设置 `allocation_wait_reason=session_capacity`；Continuation MAY 保持 pending allocation。两者使用 Tenant/User 单调 `session_allocation_sequence` 和 `allocation_queued_at`，该位置不构成 WorkerSession identity 或 execution-slot reservation。
- Capacity pressure MUST NOT 自动选择、reset、clean、close、expire 或 evict 既有 Session。用户显式 exact reuse 不增加 slot，但继续遵守目标 Session 的上下文继承与 SessionTaskQueue。
- Worker Runtime `WorkerSessionAllocator` MUST own sequence allocation and只向最低合法 allocation sequence 发起新 Session 分配。Task Orchestration and Continuation application only submit owner-bound requests. Cancelled、terminal、stale-authority、unreadable-checkpoint 或不可满足的队头必须先幂等收敛/移出；仍合法的队头 MUST NOT 被后到请求越过。
- Worker Runtime MUST 在同一 PostgreSQL 事务中锁定 quota revision、验证 `reserved_count < limit`、创建唯一 `allocating` WorkerSession、递增 count/revision 并写 Outbox。并发分配不能超过 limit；重复 idempotency key 或响应丢失返回原 Session identity。
- Task assignment 或 Continuation binding 可在 owner event 后幂等完成。若 owner request 在 allocation 与 claim 之间失效，unclaimed Session MUST 被 fenced 并通过幂等 terminal release/reconciliation 释放，不能成为可交互 orphan。
- `closed/expired` Session 终态与 quota release MUST 在同一 owner transaction 中提交；Runtime/container/mount/storage cleanup MAY 异步执行。release event 丢失、stale reservation 和 unclaimed allocation MUST 由 reconciliation 修复。
- Quota release SHOULD 唤醒 SessionAllocationQueue dispatcher。分配与 release 都必须使用服务端 revision/idempotency，不依赖容器 wall clock 或投影计数。
- WorkerSessionContinuityQuota 不限制 queued Task 数量；Task create/rate/cost limits 属于独立 policy。

## WorkerSession Terminal Continuation v1

`CreateWorkerSessionContinuation v1`：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `worker-session-continuation-command.v1`。 |
| `continuation_id` | string | yes | 幂等续作 identity。 |
| `actor_user_id`, `tenant_id`, `workspace_id` | string | yes | 真实 actor 与当前授权边界。 |
| `source_worker_session_id` | string | yes | 必须为 closed/expired 的只读 source。 |
| `expected_source_session_revision` | integer | yes | 防止终态/checkpoint 选择竞态。 |
| `source_checkpoint_ref` | object | yes | exact checkpoint id、digest、captured_at 和 coverage。 |
| `explicit_context_refs` | array | yes | 用户主动选择且需重新鉴权的 ContextPack/Artifact refs；默认空。 |
| `idempotency_key` | string | yes | 重复 command/响应丢失收敛。 |

成功结果至少包含新 `worker_session_id`、`continued_from_worker_session_id`、`continued_from_checkpoint_ref`、allocation claim、new WorkingCopy/AgentSession refs 与新 idle/absolute deadlines。

Invariants:

- `closed/expired` WorkerSession MUST NOT 转回任何非终态，也 MUST NOT 接受 AgentTurn、WorkerRun、Runtime/Secret lease、dispatch permit 或 Session-scoped Grant。
- Continuation MUST revalidate actor、Tenant/WorkspaceMembership、policy、source revision 和 checkpoint readability。Cross-Tenant、stale source、mutable/unproven checkpoint 或 revoked access MUST fail closed without disclosing protected source content。
- Derived Session MUST use a new identity, isolated WorkingCopy and new AgentSession, and MUST start new 48-hour idle and 14-day absolute clocks from its own creation time。
- Derived Session MUST NOT inherit source Runtime generation、ChannelEpoch、queue/dispatch position、ApprovalGrant、RepositoryAccessGrant、SecretLease 或其他 Session-scoped authority。Explicit context refs MUST be permission-filtered again。
- Source checkpoint MUST remain immutable。Graceful close SHOULD prepare a final durable checkpoint；otherwise only latest proven durable checkpoint MAY be offered with capture time、digest and coverage warning。
- LiveWorkerState and continuation checkpoint MUST remain distinct。Non-terminal WorkingCopy、Agent state and Trace/control cursors persist in WorkspaceStorage for same-Session Runtime generation recovery without requiring an immutable continuation checkpoint。
- P0 continuation checkpoints are best-effort semantic snapshots at AgentTurn terminal、entry to `waiting_user`、stage-result publication and graceful close。P0 publishes no periodic interval、byte threshold or numeric RPO。Forced host/storage failure MUST expose only latest proven checkpoint age/digest/coverage and MUST NOT claim uncheckpointed state survived。
- Continuation MUST use WorkerSessionAllocationRequest and the same Tenant/User FIFO/quota protocol as Task allocation。It MUST NOT bypass an earlier legal request。
- If a terminal source is preserved in a non-terminal TaskSessionLineage and the user intends to continue that delivery, the command MUST be `ContinueTaskInNewWorker` against the exact Task/outcome/checkpoint. Generic Session continuation MAY create an independent Worker context but MUST NOT silently bind or mutate that Task。
- Duplicate continuation command or response loss MUST return the same derived Session。Cancelled/stale request or materialization failure MUST be fenced and reconciled without an interactive orphan。

## WorkerSession Absolute Expiry And Task Continuation v1

`WorkerSessionExpiryOutcome v1` 是 Worker Runtime 对 14 天 absolute deadline 的权威收敛结果：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version`, `outcome_id` | string | yes | 固定为 `worker-session-expiry-outcome.v1` 与幂等 outcome identity。 |
| `tenant_id`, `workspace_id`, `worker_session_id` | string | yes | 安全边界与终态 source。 |
| `expected_session_revision`, `result_session_revision` | integer | yes | 终态转换的 revision fencing。 |
| `worker_run_id`, `worker_run_result_revision` | string/integer | conditional | 到期时存在 active Run 才必填。 |
| `worker_run_terminal_status` | enum | conditional | active Run 固定为 `timed_out`。 |
| `terminal_reason` | string | yes | 固定为 `worker_session_absolute_expired`。 |
| `bound_task_id` | string | optional | 到期时绑定的 Task。 |
| `terminal_checkpoint_ref` | object | optional | latest proven durable checkpoint、digest、capture time 与 coverage。 |
| `artifact_results` | array | yes | 已保留 Artifact refs 及 `partial|complete` completeness。 |
| `cancellation_evidence`, `fencing_evidence` | object | yes | cancel/drain 结果与旧 generation/container/mount lease 失权证据。 |
| `external_effect_state` | enum | yes | `none`、`known_succeeded`、`known_failed`、`unknown`。 |
| `occurred_at` | timestamp | yes | 服务端提交终态的时间。 |

`ContinueTaskInNewWorker v1` 至少包含 `actor_user_id`、`task_id`、`expected_task_revision`、exact `expiry_outcome_ref`、exact `source_checkpoint_ref`、可选显式 context/artifact refs 与 `idempotency_key`。

Invariants:

- Absolute expiry MUST first fence new command/lease issuance, request bounded Agent/Tool cancellation and drain, revoke old control/secret leases, and prove the old Runtime no longer holds WorkingCopy write authority. If exclusivity cannot be proven, Session MUST become/remain `lost`, continue consuming quota and MUST NOT publish a successful terminal outcome or allow writable continuation.
- A successfully fenced active WorkerRun MUST terminate as `timed_out` with reason `worker_session_absolute_expired`; it MUST NOT resume or migrate into another Session. Late runtime/Tool results are Trace evidence only.
- Task Orchestration MUST apply an exact, current outcome idempotently. A terminal Task is a no-op; `queued`、`running` or `waiting_user` becomes/remains `waiting_user:worker_session_expired`; `delivered` remains `delivered`. In every non-terminal case the old current Session/Run refs clear only after their refs are appended to lineage.
- A complete delivered result remains acceptable after Session expiry. Partial Artifact MUST stay visible with source Run/checkpoint provenance but MUST NOT satisfy automatic acceptance.
- Only TaskInitiator MAY submit `ContinueTaskInNewWorker`. Unknown external non-idempotent effect MUST first reconcile or receive an explicit user resolution; the system MUST NOT interpret continuation as permission to repeat an uncertain side effect.
- Accepting continuation MUST atomically move `waiting_user` or `delivered` Task to `queued`, append intent/lineage state and write Outbox. It MUST NOT allocate a Session or materialize files inside the Task transaction.
- WorkerSessionAllocator MUST apply the normal continuity quota and SessionAllocationQueue FIFO. After a new Session is bound, normal SessionTaskQueue/dispatch creates a new WorkerRun with `trigger_type=task_session_continuation`; the Task returns to `running` only when that Run starts.
- The new Run MUST reference prior Task/Run/Session/outcome/checkpoint in WorkerRunStartSnapshot and MUST use new AgentSession、runtime generation、deadline、leases and authority. Old queue position、ApprovalGrant、RepositoryAccessGrant and SecretLease MUST NOT transfer.
- Worker Runtime and Task Orchestration converge through owner events、expected revisions、idempotency and reconciliation. This protocol MUST NOT require a distributed transaction or TaskSaga.

需要到期执行时使用 `ScheduledCommand v1`，至少包含 command id、可选 Task/WorkerRun/AgentTurn target、kind、due time、expected revision、status、lease、fencing token 和 idempotency key。Poller MUST 只提交幂等应用命令；过期 token、stale revision、cancelled/obsolete command 是 no-op，外部 unknown-effect MUST reconciliation 或 `waiting_user`，不得盲重试。

### AgentTurnHardDeadline v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `agent_turn_id` | string | yes | 被限制的顶层 AgentTurn。 |
| `started_at` | timestamp | yes | 后端 Worker Runtime owner 事务接受 `StartAgentTurn` dispatch 的服务端时间；不是 Shim/ACP 实际调用时间。 |
| `deadline_at` | timestamp | yes | 固定等于 `started_at + 86400s`。 |
| `scheduled_command_ref` | object | yes | 幂等 deadline command、expected Turn revision 与 fencing token。 |
| `runtime_watchdog_ref` | object | yes before prompt start | Runtime generation、Turn revision、ChannelEpoch、不可重置的 monotonic arm evidence、保守 remaining duration、server-time uncertainty 与共享 cancellation identity。 |
| `terminal_at` | timestamp | conditional | Turn 终态提交时间。 |
| `terminal_reason` | enum | conditional | 超时固定为 `agent_turn_hard_deadline`。 |

Deadline invariants:

- `deadline_at` MUST NOT be overridden by TaskSpec、Assistant、Delivery、Agent output or user action and MUST NOT be extended。
- `waiting_user`、approval、`active_quiet`、transport disconnection、platform outage and `recovering_runtime` MUST NOT pause or shift the deadline。
- `steer` MUST preserve the current deadline；`interrupt` terminates the current Turn；only an authenticated user prompt or an already authenticated queued user message MAY start a later independent Turn after terminality。
- Agent output、automatic retry and Runtime recovery MUST NOT create a new top-level AgentTurn to evade the deadline。
- At deadline Worker MUST request ACP cancel and cancel the current Tool/process. AgentTurn MUST become terminal `timed_out` under revision/generation fencing；late progress/results are Trace-only。
- Backend ScheduledCommand MUST remain the sole state/audit/fencing authority. Runtime Shim MUST arm one non-resettable local monotonic watchdog for the same AgentTurn revision and Runtime generation, and both paths MUST use the same idempotent cancellation identity。
- ACP prompt execution MUST NOT start until backend deadline enforcement and matching `runtime_watchdog_ref` are both armed。A disconnect MUST NOT disable the watchdog；after reconnect, watchdog evidence MUST converge through backend revision/generation fencing and MUST NOT independently rewrite or extend `deadline_at`。
- Start uses backend-precommitted one-way dispatch。One backend transaction MUST persist AgentTurn `started_at/deadline_at`、ScheduledCommand and `StartAgentTurn` Outbox before delivery；there is no per-Turn RuntimeReady/StartPermit round trip。
- `StartAgentTurn` MUST bind exact Turn revision、Runtime generation、ChannelEpoch、`deadline_at` and cancellation identity。Shim MUST fail closed on stale/expired command or unverifiable server-time freshness, arm a monotonic watchdog for no more than the conservative remaining duration, then invoke ACP。Dispatch latency and uncertainty margin consume the fixed window；redelivery MUST NOT reset the watchdog or invoke the prompt twice。
- Confirmed cancellation MAY leave the WorkerRun resumable and move a Task-bound interaction to `waiting_user:agent_turn_timed_out`。Unconfirmed cancellation MUST follow TaintedRuntime or external-effect reconciliation before another Turn starts。
- Tool invocations inherit the current Turn's remaining deadline and have no separate business timeout profile。Infrastructure transport/shutdown timeouts MAY remain internal implementation guards。
- P0 publishes no WorkerRun/Task time budget、cumulative time account、time Grant or continuation budget schema。

P0 should measure AgentTurn elapsed-time distributions, deadline expirations, cancel confirmation latency and post-timeout resume outcomes. These metrics do not create a user-facing budget policy.

## Deferred TaskSaga Reference

`TaskSaga v1`、`SagaTransition v1` 和 `SagaWakeup v1` 不再是 P0 published language。ADR 0048 以 `Lightweight Task v1`、`TaskTransition v1` 和通用 `ScheduledCommand v1` 取代它们，避免 Task 与 Saga 拥有两套 revision 和终态。

ADR 0031 的 PostgreSQL 同事务、Outbox、leased polling、fencing 和 unknown-effect reconciliation 机制仍然有效。只有在实际任务出现复杂分支、补偿或长期 timer，并且轻量 Task 无法经济表达时，才通过新的 ADR/OpenSpec 引入 TaskSaga 协议和在途迁移方案。

## Tool Policy Resolution v1

### ToolCapabilityDeclaration

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | Declaration schema version。 |
| `declaration_id` | string | yes | 稳定声明 id。 |
| `declaration_version` | string | yes | 不可变版本。 |
| `content_hash` | string | yes | 声明内容完整性。 |
| `tool_ref` | object | yes | Built-in Tool、Skill 或 MCP Tool identity/version。 |
| `source_ref` | object | yes | Manifest/package/registry 来源、owner 和 review status。 |
| `declared_effect_class` | enum | yes | 提供方声明的 ToolEffectClass。 |
| `cancellation_capabilities` | array | yes | 声明支持的取消方式；空数组表示未声明。 |
| `idempotency_support` | object | yes | 是否支持、key 传递位置和 provider guarantee 声明。 |
| `reconciliation_actions` | array | yes | 声明的 query/confirm/compensate/retry 能力引用。 |

### AdapterEvidence

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `evidence_id` | string | yes | 不可变验证证据 id。 |
| `declaration_ref` | object | yes | 被验证 declaration id/version/hash。 |
| `adapter_ref` | object | yes | Adapter identity、version 和构建 hash。 |
| `trust_level` | enum | yes | 平台 trust store 解析出的信任级别。 |
| `validated_effect_class` | enum | yes | Adapter 归一化后的风险；不得低于声明风险。 |
| `validated_cancellation_modes` | array | yes | Adapter 可实际执行并验证结果的取消方式。 |
| `idempotency_evidence` | object | conditional | key 传播、provider contract 或测试证据。 |
| `reconciliation_evidence` | array | yes | Adapter 可调用且能映射结果的核查/补偿动作。 |
| `issues` | array | yes | 缺失、冲突、撤销或验证失败原因。 |
| `verified_at` | timestamp | yes | 验证时间。 |
| `expires_at` | timestamp | conditional | 动态证据需要重新验证的时间。 |

### ResolvedToolPolicy

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `resolution_id` | string | yes | 不可变策略快照 id。 |
| `tool_ref` | object | yes | 适用 Tool identity/version。 |
| `declaration_ref` | object | yes | Declaration id/version/hash。 |
| `adapter_evidence_ref` | object | yes | Evidence id/adapter version/hash。 |
| `policy_revision` | string | yes | Capability/Skilling ToolGovernancePolicy revision。 |
| `scope` | object | yes | tenant/user/workspace/task/capability 适用范围。 |
| `resolved_effect_class` | enum | yes | 最终 ToolEffectClass。 |
| `allowed_cancellation_modes` | array | yes | 最终允许的取消方式。 |
| `idempotency_policy` | object | yes | 是否允许 keyed retry 及其前置条件。 |
| `allowed_reconciliation_actions` | array | yes | 最终允许暴露的核查、确认或补偿动作。 |
| `auto_retry_allowed` | boolean | yes | 自动重试是否被明确允许。 |
| `restrictions` | array | yes | TaskSpec、tenant、Approval 和 runtime 限制。 |
| `resolution_reasons` | array | yes | 风险升级、override 或 fail-closed 原因。 |
| `content_hash` | string | yes | Worker 校验的完整性 hash。 |

不变量：

- 同一 declaration/evidence/policy revision/scope 输入 MUST 产生确定性的 ResolvedToolPolicy 内容；新判断通过新 resolution snapshot 表达，不能改写历史快照。
- AdapterEvidence 的信任级别 MUST 来自平台 trust store，不能由 Tool manifest 自报。
- `auto_retry_allowed=true` MUST 同时具有非 `unknown` effect class、可验证 idempotency key 传播和可用 reconciliation evidence。
- Worker MUST 在每次 invocation 前应用最新的 TaskSpec/Approval/runtime restriction；restriction 可以拒绝旧快照允许的动作，但不能反向扩大快照。

## ToolInvocationControl v1

Tool 策略解析链固定为：版本化 ToolCapabilityDeclaration -> AdapterEvidence -> Capability/Skilling ToolGovernancePolicy -> ResolvedToolPolicy -> Worker enforcement。Manifest 声明是输入而不是授权；Adapter 可验证、标准化或提高风险，但不得自行降低声明风险。TaskSpec、ApprovalGrant 和租户约束只能进一步收紧 ResolvedToolPolicy。Agent/模型输出不能修改任何解析字段。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `tool_invocation_id` | string | yes | Tool 调用与迟到结果 fencing id。 |
| `worker_session_id` | string | yes | 所属 WorkerSession。 |
| `worker_run_id` | string | yes | 所属 WorkerRun。 |
| `agent_turn_id` | string | yes | 所属 AgentTurn。 |
| `runtime_instance_id` | string | yes | 执行 Tool 的 RuntimeInstance。 |
| `runtime_generation` | integer | yes | 本地执行和迟到结果 fencing。 |
| `tool_ref` | object | yes | Tool/Skill/MCP/command identity 和版本。 |
| `declaration_ref` | object | yes | ToolCapabilityDeclaration id、版本、来源和内容 hash。 |
| `adapter_evidence_ref` | object | yes | Adapter 验证器版本、证据 hash、验证时间和信任级别。 |
| `resolved_policy_ref` | object | yes | ResolvedToolPolicy id、ToolGovernancePolicy revision、适用 scope 和内容 hash。 |
| `effect_class` | enum | yes | `read_only`、`idempotent_write`、`non_idempotent_write` 或 `unknown`。 |
| `cancellation_mode` | enum | yes | `process_group`、`cgroup`、`remote_cancel`、`cooperative` 或 `unsupported`。 |
| `idempotency_key` | string | conditional | idempotent write/retry 必填。 |
| `reconciliation_ref` | object | conditional | 查询结果或补偿能力引用。 |
| `status` | enum | yes | `running`、`cancel_requested`、`timed_out`、`succeeded`、`failed`、`waiting_user`。 |
| `cancellation_outcome` | enum | conditional | `cancelled_confirmed`、`cancel_unconfirmed_local`、`cancel_unconfirmed_remote_safe` 或 `cancel_unconfirmed_external`。 |
| `terminal_at` | timestamp | conditional | 终态时间；设置后不可逆转。 |

不变量：

- Capability/Skilling MUST 生成带 declaration/evidence/policy provenance 的不可变 ResolvedToolPolicy；Worker MUST 在调用前校验其版本、scope、hash 和 TaskSpec/Approval 限制。
- Adapter MAY 提高 effect/cancellation 风险或撤销不再成立的 idempotency/reconciliation 证据，MUST NOT 自行降低 Manifest 声明风险；只有受版本控制的 ToolGovernancePolicy override 才能改变最终裁决，并保留审计原因。
- 缺失、不可信、冲突、不可验证或过期元数据 MUST 解析为 `effect_class=unknown`、不可验证 cancellation 为 `unsupported`，并禁止自动重试。Agent/模型文本 MUST NOT 成为解析证据。
- 可信运行时证据 MAY 升级单次 ToolInvocationControl 风险并触发更严格路径，MUST NOT 降低 ResolvedToolPolicy 风险或扩展权限。
- AgentTurn hard deadline 或用户取消触发停止时，Worker MUST 先尝试声明的 cancellation mode，再根据可验证结果设置 ToolCancellationOutcome。
- 本地 process group/cgroup 终止后仍无法证明已停止时，RuntimeInstance MUST 进入 tainted、暂停新命令并以 `tool_cancel_unconfirmed_local` 打开 RuntimeRecoveryIncident。
- 远端 `read_only` 或具有稳定 idempotency key 和 reconciliation 证据的 `idempotent_write` MAY 在 invocation fencing 后继续 AgentTurn。
- `non_idempotent_write`、`unknown` 或缺少可验证 idempotency/reconciliation 证据时，Worker MUST 禁止自动重试并进入 `waiting_user`，暴露 reconcile、confirm-success、compensate 和 retry 中实际可用的动作。
- `terminal_at` 设置后所有 progress/success/error MUST 记录为 `late_tool_result` Trace，MUST NOT 改写 ToolInvocation、AgentTurn 或 WorkerRun 终态。
- 迟到结果和自动重试 MUST 同时校验 `tool_invocation_id`、runtime generation、effect class 和 idempotency key。

## SessionProcess v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `session_process_id` | string | yes | 会话级受管进程 identity；终态后 restart 创建新 identity。 |
| `worker_session_id` | string | yes | 唯一所属 WorkerSession。 |
| `origin_agent_turn_id` | string | yes | 发起 managed start 的 AgentTurn。 |
| `origin_tool_invocation_id` | string | conditional | 由 Tool 请求启动时的来源 invocation。 |
| `runtime_instance_id` | string | yes | 实际承载进程的 RuntimeInstance。 |
| `runtime_generation` | integer | yes | process handle、状态回传和控制命令的 fencing generation。 |
| `runtime_process_ref` | object | running conditional | Shim 管理的 PID/process-group/cgroup handle；不得作为跨 generation identity。 |
| `kind` | enum | yes | P0 为 `preview_server`、`dev_server` 或 `file_watcher`。 |
| `command_summary` | string | yes | 脱敏、可展示的启动命令摘要，不是重新执行授权。 |
| `cwd` | string | yes | WorkerSession WorkingCopy 内的规范化目录。 |
| `declared_ports` | array | no | 进程声明的 `{container_port, application_protocol=http, websocket_upgrade_allowed, purpose}`；不自动发布 host 入站端口。 |
| `status` | enum | yes | `starting`、`running`、`stopping`、`stopped`、`failed` 或 `lost`。 |
| `log_ref` | object | no | 有界/轮转 process log 的 Trace/ObjectStore 引用。 |
| `revision` | integer | yes | stop/status/restart command 的 expected-revision fencing。 |
| `created_at` | timestamp | yes | managed start 被 Worker 接受的服务端时间。 |
| `terminal_at` | timestamp | terminal conditional | stopped/failed/lost 提交时间。 |

Invariants:

- Every ordinary shell/Tool process and descendant MUST remain owned by the current AgentTurn process group/cgroup and MUST be drained or terminated at Turn terminality. `&`、`nohup`、daemonization、stdio detachment and parent exit MUST NOT promote it.
- Only a Worker managed-process start accepted while the source AgentTurn is active MAY create SessionProcess. Worker MUST NOT adopt an unknown orphan after Turn terminality.
- A SessionProcess MUST NOT invoke ACP prompts, create AgentTurn/WorkerRun or gain additional network, mount, Tool, ApprovalGrant, SecretLease or host authority. It inherits the exact Runtime policy and remains visible to aggregate capacity accounting.
- Start/stop/restart commands MUST carry actual actor or authorized active-Turn evidence, exact WorkerSession/process identity, expected revision/runtime generation and idempotency key. Restart of a terminal process MUST create a new identity and retain `restarted_from_session_process_id` lineage.
- Managed start acceptance and AgentTurn terminality MUST serialize on exact Turn revision、Runtime generation and Worker-owner commit order. If acceptance commits first, the SessionProcess MUST survive any later `completed`、`cancelled`、`timed_out` or `failed` Turn result; `interrupt` MUST converge to `cancelled` with a terminal reason. If terminality commits first, Worker MUST reject start and MUST NOT adopt a later orphan.
- WorkerDetail MUST expose permission-filtered active/history entries, source Turn, kind, ports, state, bounded logs and allowed stop/restart actions. Process logs MUST use bounded segments/rotation and MUST NOT become unbounded durable SSE state.
- WorkerSession close、idle expiry、absolute expiry and authorization/security termination MUST stop every SessionProcess. Process output、logs、port traffic、health checks and status updates MUST NOT renew the 48-hour idle clock. Runtime generation fencing/replacement MUST converge old processes to `stopped` or `lost` and MUST NOT auto-restart them.
- AgentTurn terminal survival MUST NOT override WorkerSession terminality、authorization/security termination、Runtime taint/fencing/replacement or resource hard-limit enforcement. Turn cancellation still stops ACP prompt、active Tool and ordinary descendants.

## SessionProcessPreviewAccess v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `preview_access_id` | string | yes | Backend 签发的临时访问 identity；不是 SessionProcess 或权限聚合根。 |
| `worker_session_id` | string | yes | 访问所属 WorkerSession。 |
| `session_process_id` | string | yes | 唯一目标 SessionProcess。 |
| `process_revision_at_issue` | integer | yes | `OpenSessionProcessPreview` 接受时校验的 process revision。 |
| `runtime_generation` | integer | yes | 目标容器 endpoint 的 generation fence。 |
| `protocols` | array | yes | P0 为 `http` 加可选 `websocket` upgrade；不支持 arbitrary TCP/UDP/CONNECT。 |
| `declared_port` | integer | yes | SessionProcess declaration 中的容器端口。 |
| `audience_ref` | object | yes | M1 为服务端固定 actor；后续 profile 绑定 authenticated tenant/user/session。 |
| `route_ref` | string | yes | Frontend 可打开的不透明临时路由引用；不是可自行修改的 target URL。 |
| `issued_at` | timestamp | yes | Gateway 接受显式 OpenPreview 的服务端时间。 |
| `expires_at` | timestamp | yes | 服务端 PreviewAccessPolicy 计算的最晚访问时间，不得超过 Process/Session 生命周期。 |
| `status` | enum | yes | `active`、`revoked` 或 `expired`。 |

Invariants:

- `OpenSessionProcessPreview` MUST carry actual actor, exact WorkerSession/SessionProcess identity, expected process revision, declared port and idempotency key. Gateway MUST reject non-running、stale-generation、undeclared-port or unauthorized requests.
- RuntimeSupervisor MUST resolve the target only from the current RuntimeInstance and SessionProcess record. Client、Agent、TaskSpec and preview content MUST NOT choose URL、IP、host、port mapping or proxy destination.
- Worker containers MUST publish no host/public port. PreviewProxy is the only P0 inbound caller and MAY reach only a declared HTTP/WebSocket port over the internal Docker network.
- Every HTTP request and WebSocket upgrade MUST revalidate access expiry、audience、Session/process status and runtime generation. Process terminality、Session terminality、Runtime fencing/replacement、authorization revocation or access expiry MUST reject new traffic and close indexed active tunnels.
- Preview bytes and WebSocket frames MUST bypass EventCenter、SSE、Trace JSONL and ObjectStore. The platform MAY record bounded issuance/revocation、decision、status、duration and byte-count metadata, but MUST NOT treat the proxy as an execution Trace recorder.
- PreviewProxy MUST strip Switchback Cookie、Authorization、internal identity、Forwarded and hop-by-hop headers before forwarding. It MUST NOT expose Backend control credentials or container endpoint metadata to preview content.
- Explicit OpenPreview MAY count as one user interaction. Subsequent resources、polling、health checks、HMR、WebSocket frames and SessionProcess traffic MUST NOT renew WorkerSession idle.
- `route_ref` format and browser origin are implementation-defined under M1 SingleUserLocalProfile and MUST NOT become a stable domain contract. Non-loopback or authenticated multi-user profiles MUST perform a separate isolation review before reuse.

## IntegrationEvent v1

IntegrationEvent 是跨模块异步传播的统一事件信封。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `event.v1`。 |
| `event_id` | string | yes | 全局唯一事件 id。 |
| `event_type` | string | yes | 事件类型，例如 `WorkerRunStarted`。 |
| `producer_context` | string | yes | 拥有该 stream 的 bounded context。 |
| `stream_id` | string | yes | 顺序范围，建议 `{context}/{aggregate_type}/{aggregate_id}`。 |
| `sequence` | integer | yes | StreamOwner 在业务事务中分配的同 stream 单调递增序号。 |
| `tenant_id` | string | yes | 租户边界。 |
| `aggregate_type` | string | yes | 事件来源聚合类型。 |
| `aggregate_id` | string | yes | 事件来源聚合 id。 |
| `trace_id` | string | recommended | 全链路追踪。 |
| `correlation_id` | string | yes | 同一用户请求或任务链路。 |
| `causation_id` | string | optional | 触发当前事件的上游事件。 |
| `idempotency_key` | string | yes | 消费者幂等处理键。 |
| `visibility` | enum | yes | `internal` 或 `user`。 |
| `occurred_at` | timestamp | yes | 事件发生时间。 |
| `payload` | object | yes | 事件负载，按 event_type 版本化。 |

### Invariants

- EventCenter 不根据 payload 推导业务事实，只负责分发、投影和重试。
- 用户可见事件不能包含密钥、完整模型原文、内部异常栈或未经脱敏日志。
- 每个 `stream_id` MUST 有且只有一个 StreamOwner；Owner MUST 在业务状态和 Outbox 的同一事务中分配 sequence。EventCenter MUST NOT 分配、重写或压缩领域 sequence。
- 同一 event id/idempotency key 重投 MUST 幂等忽略。消费者看到 `sequence = expected + 1` 时应用，看到未来 sequence 时持久化 gap buffer 并请求重投/补偿，看到已应用 sequence 时仅接受同 event id 的重复。
- 同一 `(stream_id, sequence)` 出现不同 event id MUST 作为 integrity incident 进入隔离/DLQ，MUST NOT last-write-wins。
- `occurred_at` 只用于审计和显示，不能替代 StreamSequence、TimelineCursor 或 causation。
- 跨 stream 因果关系 MUST 使用 `correlation_id` 和 `causation_id`；系统不提供全局业务 sequence。

## TimelineEntry v1

TimelineEntry 是 Frontier/Delivery 用户可见 TimelineProjection 的稳定追加项，不是领域事实源。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `timeline_id` | string | yes | Conversation/Task/Worker 详情等目标 timeline。 |
| `timeline_epoch` | integer | yes | Timeline 全量重建代次；普通 replay 不改变。 |
| `timeline_cursor` | integer | yes | Timeline projector 在提交读模型时分配的单调游标。 |
| `event_id` | string | yes | 来源 IntegrationEvent。 |
| `source_stream_id` | string | yes | 来源领域 stream。 |
| `source_sequence` | integer | yes | 来源 StreamSequence。 |
| `event_type` | string | yes | 用户可见事件类型。 |
| `correlation_id` | string | yes | 链路分组。 |
| `causation_id` | string | optional | 跨 stream 因果来源。 |
| `occurred_at` | timestamp | yes | 用户显示的发生时间，不用于 cursor 排序。 |
| `projected_at` | timestamp | yes | Timeline projector 提交时间。 |
| `late` | boolean | yes | 事件发生时间或因果前驱早于已发布项但本次追加到末尾。 |
| `causation_state` | enum | yes | `resolved`、`unresolved` 或 `not_applicable`。 |
| `display_payload` | object | yes | 权限过滤后的版本化展示负载。 |

不变量：

- `(timeline_epoch, timeline_cursor)` MUST 只在 TimelineProjection 写入事务中分配，并在同一 epoch/timeline 内单调递增；它只用于 SSE resume、分页和稳定追加。
- 同一 event id 在同一 timeline MUST 只产生一个 TimelineEntry。
- 迟到事件 MUST 取得新的末尾 cursor 并标记 `late=true`，MUST NOT 插入、移动或重写已发布 cursor。
- TimelineProjection MAY 使用 occurred_at 展示人类时间，并使用 causation link 展示“由此触发”，但 MUST NOT 声称不同 stream 具有全局业务顺序。
- SSE client 使用 `Last-Event-ID = timeline_epoch:timeline_cursor` 恢复；cursor 缺口由 TimelineProjection 数据恢复，不回退到 occurred_at 查询。
- 普通 event replay MUST 通过 `(timeline_id, event_id)` 复用已有 entry/cursor。只有无法保留 cursor mapping 的破坏性全量重建 MAY 递增 timeline_epoch；旧 epoch 请求 MUST 返回 `resync_required` 和新 snapshot cursor，不能静默映射。

## Frontend Read Model Set v1

Frontend 使用 page-optimized projection，不要求从 raw IntegrationEvent、TraceRecord 或多个领域 API 自行拼接权威状态。P0 以 Worker 为主交互面，Task 为轻量索引；MemoryLibrary/ReviewInbox 随 P1 Evolution governance 启用。

| View | Primary Identity | Required Composition |
| --- | --- | --- |
| `HomeView` | tenant_id + user_id | recent Workers、continuity quota used/limit、Task/Continuation SessionAllocationQueue wait count、waiting_user/approval、recent deliveries、target identity、reason、available actions、deep link。 |
| `TaskListView` | tenant/user filter | Task identity、title、status、source/priority、workspace ref、current stage、allocation/session queue reason and position、latest result。 |
| `TaskSummaryView` | task_id | TaskSpec/acceptance snapshot、current Session assignment/context policy、append-only Session lineage、expiry outcome、allocation wait/position、current/history Runs、AgentTurn deadline state、Artifacts/completeness、conditional WorkerSession deep link、available actions；可作为抽屉或摘要页。 |
| `WorkerListView` | tenant/user filter | WorkerSession identity/status、continuity-slot counted flag、workspace、bound Task、active Run、queued Task count/oldest age、idle/absolute deadline、waiting/approval、latest activity。 |
| `WorkerDetailView` | worker_session_id | live AgentTurns/chat or terminal read-only state、bound Task、ordered SessionTaskQueue、active/history Runs、active/history SessionProcesses with ports/logs/open-preview/stop/restart actions、Activity、approvals、runtime health、Artifacts、Task/standalone/Continuation lineage、terminal checkpoint capture/digest/coverage、available actions。 |
| `WorkspaceOverviewView` | workspace_id | resources、repositories、active revision、members、Tasks、WorkerSessions、Artifacts。 |
| `ArtifactDetailView` | artifact_id | type、provenance、availability、permission decision、preview/download/apply/publish actions。 |
| `MemoryLibrary` | scope + viewer | active/disabled/expired Memory、revision、source、usage、projection state、actions。 |
| `ReviewInbox` | reviewer + scope | lane、diff、evidence、risk/conflict、scope/ACL、review actions。 |

Invariants:

- 产品中的 Worker view MUST 以 `worker_session_id` 为 identity，MUST NOT 使用后端 Worker service instance identity。
- Task 与 WorkerSession MUST 互相引用但保持独立终态；Task terminal MUST NOT 隐藏或关闭有效 WorkerDetailView，standalone WorkerRun MUST NOT 生成伪 Task projection。TaskSummary MUST NOT 复制 Worker chat/runtime control。
- WorkerDetailView MUST distinguish `queued_tasks` from queued AgentTurn messages and MUST present only the one `active_worker_run_id` as the target of interrupt/steer controls。
- WorkerDetailView MUST distinguish AgentTurn-scoped Tool processes from SessionProcess resources, show source Turn/runtime generation/ports/status/bounded-log refs, and submit stop/restart only through revisioned Gateway commands. A terminal Session or lost Runtime generation MUST NOT expose an implicit resume action.
- WorkerDetailView MUST expose `open_preview` only for a current running preview/dev SessionProcess declared port. The action submits exact identity/revision to Gateway and returns an opaque PreviewAccessRef; projection MUST NOT contain container IP, host mapping or reusable control credential.
- WorkerDetailView MUST order queued Tasks by `session_queue_sequence`, MUST NOT expose priority/reorder/preempt controls in P0, and MUST prevent standalone start while an earlier Task owns dispatch order。
- SessionAllocationQueue MUST use `session_allocation_sequence` without Worker identity；TaskSummary/List/Home and pending Continuation MUST label it as capacity wait and MUST NOT present it as SessionTaskQueue。Quota used/limit MUST come from Worker Runtime projection, not browser counting。
- A terminal WorkerDetailView MUST disable all mutable Session actions and expose only permission-filtered evidence plus `continue_in_new_worker` when an exact durable checkpoint is readable。The action MUST show checkpoint capture/digest/coverage and MUST navigate to a new identity only after allocation/binding commits。
- TaskSummary MUST allow current WorkerSession to be empty after successful absolute-expiry convergence while retaining historical Worker deep links, exact expiry reason, partial/complete Artifact labels and initiator-only `continue_task_in_new_worker` when fencing and external-effect preconditions are satisfied。
- WorkerDetail/TaskSummary MUST show the active AgentTurn `started_at/deadline_at`, terminal reason and whether cancellation is still reconciling。There is no time-extension or continuation-budget form。
- 同一 `artifact_id` 在 Task/Worker/Workspace 中 MUST 解析到同一 ArtifactDetailView 权限、生命周期和 action 状态；嵌入摘要不得复制权威状态。
- Home item MUST 指向 canonical target identity；Home action 只提交 Gateway command，不直接改变 owner projection。
- MemoryLibrary/ReviewInbox MUST 保持独立 governance action；Task/Worker View MAY 展示 evidence link，但 MUST NOT 内嵌第二套审核状态机。
- 所有 View MUST 在投影或读取时应用 Tenant/Workspace/resource authorization，不可访问 related ref MUST 不泄露存在性。
- P0 View transport 使用 ProjectionSnapshot + typed SSE ProjectionDelta；GraphQL 不属于该契约的必要实现。

## ProjectionSnapshot v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `frontier.projection_snapshot.v1`。 |
| `view_type` | string | yes | TaskSummary、WorkerDetail、Home 等版本化类型。 |
| `view_id` | string | yes | canonical page projection identity。 |
| `tenant_id` | string | yes | Tenant 边界。 |
| `view_epoch` | integer | yes | 破坏性重建或 cursor 映射失效时递增。 |
| `view_cursor` | integer | yes | Snapshot 已包含的最后 replay cursor。 |
| `projection_version` | integer | yes | 当前 View 状态版本。 |
| `query_fingerprint` | string | yes | canonical scope/filter/sort/page shape hash。 |
| `generated_at` | timestamp | yes | Snapshot 生成时间。 |
| `data` | object | yes | 完整 page-optimized View。 |
| `available_actions` | array | yes | 权限过滤后的可提交命令。 |
| `related_refs` | array | yes | 权限过滤后的 canonical deep links。 |

Invariants:

- Snapshot data/actions/refs、epoch、cursor 和 version MUST 来自同一已提交 ViewProjection 状态。
- Snapshot response SHOULD 使用由 view identity、epoch 和 version 派生的 ETag。
- Snapshot MUST 不包含未授权 related resource 的存在性提示。

## ProjectionDelta v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `frontier.projection_delta.v1`。 |
| `delta_id` | string | yes | 幂等 delta identity。 |
| `view_type` | string | yes | 目标 View 类型。 |
| `view_id` | string | yes | 目标 View identity。 |
| `view_epoch` | integer | yes | 必须匹配客户端 Snapshot epoch。 |
| `view_cursor` | integer | yes | 当前 epoch 内单调递增。 |
| `base_projection_version` | integer | yes | Reducer 应用前精确版本。 |
| `result_projection_version` | integer | yes | Reducer 成功后的精确版本。 |
| `event_type` | string | yes | 版本化语义类型，不是 raw domain event 名。 |
| `critical` | boolean | yes | 无法识别时是否必须停止并升级客户端。 |
| `command_id` | string | optional | 与 Gateway accepted command 收敛。 |
| `source_refs` | array | yes | 内部审计/因果引用的受控摘要。 |
| `payload` | object | yes | 按 event_type 版本化的展示负载。 |

P0 durable event types 至少包含 `view.entity_upserted.v1`、`view.entity_removed.v1`、`view.timeline_appended.v1`、`view.available_actions_changed.v1`、`view.attention_changed.v1`、`view.artifact_ready.v1` 和 `view.stream_checkpointed.v1`。

Invariants:

- SSE id MUST 为 `view_epoch:view_cursor`；客户端用 `Last-Event-ID` 请求 cursor 之后的 replay。
- Client MUST 仅在 epoch 相等且 local version 等于 base version 时应用 delta；成功后才推进本地 version/cursor。
- 重复 `delta_id` 或已应用 cursor MUST 幂等忽略；gap/base mismatch MUST 停止 reducer 并请求 resync。
- ProjectionDelta MUST NOT 直接暴露 IntegrationEvent、TraceRecord、MQ payload、CDC row 或通用 JSON Patch path。
- Heartbeat MUST NOT 推进 view cursor/version；Command 与 Artifact bytes MUST NOT 通过 SSE 传输。

## ProjectionControlEvent v1

| Event | Trigger | Required Client Behavior |
| --- | --- | --- |
| `resync_required` | epoch 不同、cursor 超出 replay window、gap、base version 不可达 | 丢弃受影响 View cache 并获取新 Snapshot。 |
| `client_upgrade_required` | Snapshot/Delta schema 无兼容版本 | 停止 reducer 并刷新/升级客户端，不循环 resync。 |
| `access_revoked` | 当前 actor 不再可读 | 清理 View 并进入不泄露资源存在性的访问状态。 |
| `stream_draining` | Frontier 实例下线/维护 | 使用最后已应用 SSE id 重连。 |

ProjectionReplayWindow MUST 由版本化运维策略限制时长和最大 delta/bytes；cursor 不可重放时必须 resync，不能回退到时间戳查询。Native EventSource 应使用 same-origin Secure/HttpOnly credential 或等价 BFF，不得把 bearer token 放入 URL。

### Frontend Client State Invariants

- P0 React/Vite client MUST 维护一个按 View identity 隔离的 ClientViewReplica owner；Snapshot 原子替换该副本，ProjectionDelta 仅在 schema/epoch/base/result 校验后推进。
- HTTP query/cache layer MAY 负责 Snapshot/list/command-status 请求、retry 和 refetch，MUST NOT 与 live projection reducer 同时维护可独立变化的第二份权威 View state。
- TransientDisplayDelta MUST 进入有界 overlay，不能修改 durable replica version/cursor；DisplayCheckpoint 或 replacement Snapshot MUST supersede 对应 overlay。
- Gateway accepted response MUST 创建按 `command_id` 标识的 PendingCommand。只有 causal ProjectionDelta、command-status result 或 replacement Snapshot MAY 将其收敛为 completed/failed；HTTP timeout、SSE disconnect 或 UI unmount MUST NOT 推断业务终态。
- Chat/component runtime MAY 通过 adapter 消费 canonical message/DisplayPart 并调用已支持的 Gateway commands，MUST NOT 替换 WorkerSession/message/Tool/Approval identity、持久化、权限或 replay owner。
- Client schema decode/reducer failure MUST 隔离受影响 View 并触发 `resync_required` 或 `client_upgrade_required`；组件不得跳过 critical delta 后继续。

## DisplayDurabilityPolicy v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `policy_id` | string | yes | 版本化策略 identity。 |
| `revision` | integer | yes | 不可变 revision。 |
| `semantic_triggers` | array | yes | Tool/approval/waiting/artifact/error/terminal 等强制 checkpoint 边界。 |
| `max_checkpoint_interval_ms` | integer | yes | streaming 期间两次 checkpoint 的最大时间；具体值由 P0 load spike 冻结。 |
| `max_uncheckpointed_bytes` | integer | yes | 两次 checkpoint 间最大累计展示字节；具体值由 P0 load spike 冻结。 |
| `transient_queue_limits` | object | yes | connection/stream 最大 items/bytes 与 coalescing policy。 |
| `terminal_checkpoint_required` | boolean | yes | P0 必须为 true。 |

## DisplayPart v1

DisplayPart 是 DisplayStream 的完整物化用户可见状态，不是 raw Agent event、Tool authority record 或 frontend component。Canonical union 由 Switchback 拥有；ACP/Codex、AG-UI、AI SDK 等通过 adapter 映射。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `frontier.display_part.v1`。 |
| `part_id` | string | yes | Switchback 分配且跨 checkpoint 稳定的 part identity。 |
| `part_type` | enum | yes | `text`、`reasoning_summary`、`tool`、`activity`、`approval`、`artifact`、`source` 或 `error`。 |
| `part_revision` | integer | yes | 物化用户可见状态变化时单调递增。 |
| `ordinal` | integer | yes | DisplayStream 中稳定展示顺序；checkpoint array + ordinal 为权威顺序。 |
| `lifecycle_status` | enum | yes | `pending`、`streaming`、`waiting_user`、`completed`、`failed`、`cancelled` 或 `expired`。 |
| `critical` | boolean | yes | 未知该 part 是否必须触发 `client_upgrade_required`。 |
| `visibility` | object | yes | 权限过滤后的 audience/scope，不得泄露未授权 identity。 |
| `provenance` | object | yes | source adapter/schema、Trace refs 和非 secret source ids。 |
| `available_actions` | array | yes | Gateway command refs；不是本地 callback、权限或执行结果。 |
| `payload` | object | yes | 由 part_type 和 schema version 定义的 typed payload。 |
| `created_at` | timestamp | yes | 首次物化时间。 |
| `updated_at` | timestamp | yes | 最近物化时间。 |

P0 payload minimums:

| Part type | Minimum payload |
| --- | --- |
| `text` | `format: plain|markdown|code`、`content`、可选 `language`。 |
| `reasoning_summary` | `format`、`content`；只能包含显式用户可见 summary。 |
| `tool` | canonical `tool_invocation_id`、tool name、过滤后的 normalized args/result summary、effect/risk summary、timing。 |
| `activity` | canonical `activity_id`、kind、label、status、可选 progress/detail summary。 |
| `approval` | canonical `approval_request_id`、reason、risk/scope、options、expiry、decision summary。 |
| `artifact` | canonical `artifact_ref`、type/label、availability、permission-aware actions；不内嵌 bytes。 |
| `source` | permission-aware evidence/resource ref、label、可选 authorized URI。 |
| `error` | stable code、user-safe message、recoverability、available action refs。 |

Invariants:

- DisplayProtocolAdapter MUST 使用作用域化 identity map 把 source message/tool/update id 映射为 canonical part id；provider id MUST NOT 单独作为 Switchback identity。
- `part_revision` MUST 单调递增且不能因重复 source frame 改变；同一 checkpoint 中 part ids MUST 唯一。
- `available_actions` MUST 只引用 Gateway command/target/expected revision。Gateway/owner context MUST 重新校验 actor、权限、revision、policy 和 idempotency，DisplayPart 自身不授权副作用。
- payload MUST NOT 包含任意 HTML/JS/React component、未清洗 active content、secret、未经治理的 host path 或 provider-native executable callback。
- 未知可选 source progress MAY 保留 Trace 后降级为 non-critical restricted `activity`；未知 Tool/Approval/permission/security lifecycle MUST fail closed，不能伪装成普通 progress。
- RendererRegistry MUST 按 `(schema_version, part_type)` 选择 renderer。未知 non-critical type MUST 使用无特权动作的安全 fallback；未知 critical type MUST 触发 `client_upgrade_required`。
- v1 可增加可选字段；改变语义、新增 required field、扩大 visibility 或改变 permission/security behavior MUST 使用新 schema version。
- Delivery MAY 生成减少字段的 channel rendering，但 MUST 保留 part identity/revision/lifecycle/action target；不支持 critical interaction 时 MUST deep-link 到可完成操作的 Web View。

## DisplayCheckpoint v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `frontier.display_checkpoint.v1`。 |
| `display_stream_id` | string | yes | 一次持续显示输出 identity。 |
| `worker_session_id` | string | conditional | Worker display 必填。 |
| `worker_run_id` | string | conditional | WorkerRun 相关 display 必填。 |
| `agent_turn_id` | string | conditional | 直接交互 display 必填。 |
| `message_id` | string | optional | Message identity。 |
| `checkpoint_version` | integer | yes | 同一 display stream 单调递增。 |
| `status` | enum | yes | `streaming`、`waiting_user`、`completed`、`failed`、`cancelled` 或 `timed_out`。 |
| `parts` | array | yes | 完整物化的用户可见 DisplayParts。 |
| `last_observed_transient_sequence` | integer | yes | Checkpoint 已覆盖的 transient sequence。 |
| `trace_cursor_ref` | object | yes | 对应高保真 Trace evidence cursor/reference。 |
| `durability_policy_ref` | object | yes | 创建该 checkpoint 的 policy revision。 |
| `checkpointed_at` | timestamp | yes | 提交时间。 |

Invariants:

- Checkpoint MUST 是完整 materialized display state，不是相对上一 checkpoint 的 patch。
- start、所有 semantic trigger 和 terminal MUST 产生 durable checkpoint；terminal checkpoint MUST 能在清空 transient cache 后独立渲染最终状态。
- DisplayCheckpoint MUST 通过 `view.stream_checkpointed.v1` ProjectionDelta 推进 ViewProjection version/cursor，并遵守 Snapshot/SSE replay 语义。
- 只有用户可见 reasoning summary MAY 进入 parts；hidden chain-of-thought MUST NOT 成为 Display contract。

## TransientDisplayDelta v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `frontier.transient_display_delta.v1`。 |
| `display_stream_id` | string | yes | 目标 display identity。 |
| `message_id` | string | optional | Message identity。 |
| `part_id` | string | yes | 目标 DisplayPart。 |
| `base_checkpoint_version` | integer | yes | 必须匹配当前 materialized checkpoint。 |
| `transient_sequence` | integer | yes | 同一 checkpoint base 内单调递增。 |
| `delta_type` | enum | yes | `text_delta`、`reasoning_summary_delta`、`tool_args_delta`、`progress_pulse` 或版本化扩展。 |
| `payload` | object | yes | 临时展示增量。 |
| `emitted_at` | timestamp | yes | 发出时间，仅用于显示/观测。 |

Invariants:

- Transient SSE event MUST NOT 设置或推进 durable SSE id、ViewCursor 或 projection version。
- Client MUST 仅对匹配 display/part/base checkpoint 的连续 sequence 应用 delta；duplicate 忽略，gap 时停止该 part 拼接并等待/查询 checkpoint。
- Frontier MAY coalesce 相邻同类 delta，并 MAY 在 backpressure 下丢弃 progress pulse；MUST NOT 把 transient delivery 当作 Tool、Approval、Artifact 或 Run 业务事实。
- Durable checkpoint supersedes transient buffers up to `last_observed_transient_sequence`。
- Worker MUST 继续按 Trace policy 保存对应 ACP/Agent source frames；Transient 不降低 Trace evidence durability。

## WorkspaceChannelShareGrant v1

`WorkspaceChannelShareGrant` 是 Identity & Access 拥有的 Workspace 到 provider group audience 的显式披露授权。它不是 WorkspaceMembership、WorkspaceContextResolution、AuthorizationDecision、ExecutionAuthorizationLease 或 card action authority。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `identity.workspace_channel_share_grant.v1`。 |
| `grant_id` | string | yes | Stable grant identity。 |
| `tenant_id` | string | yes | Tenant hard boundary。 |
| `workspace_id` | string | yes | 被授权披露 bounded projections 的 Workspace。 |
| `audience_scope` | object | yes | provider、provider tenant 和 stable group/chat identity；不是单个发起人。 |
| `delivery_scope` | object | yes | 允许投递的 ChannelThreadBinding/topic allowlist。 |
| `disclosure_profile` | enum | yes | P0 固定支持 `workspace_summary`。 |
| `audience_baseline_ref` | object | yes | ProviderChannelSecuritySnapshot id/revision/digest/freshness。 |
| `grantor_user_id` | string | yes | P0 当前 active Workspace owner。 |
| `workspace_membership_revision` | string/integer | yes | Grant 时 owner Membership revision。 |
| `policy_ref` | object | yes | Channel disclosure policy id/revision。 |
| `grant_revision` | integer | yes | Update/suspend/revoke 单调 revision。 |
| `status` | enum | yes | `active`、`suspended`、`revoked` 或 `expired`。 |
| `valid_from`, `expires_at` | timestamp | yes | Grant validity；具体 TTL 由 policy profile 决定。 |
| `reason_codes` | array | yes | Structured suspension/revocation/expiry reasons。 |

Invariants:

- `tenant_id`、`workspace_id` 和 `audience_scope` MUST remain immutable for one grant identity；changing any MUST create a new grant。
- P0 create/update/revoke MUST require authenticated Workspace owner intent、current Tenant/WorkspaceMembership、expected grant/membership revision and stable idempotency key。
- Grant MUST authorize disclosure only；it MUST NOT grant Workspace access、Web access、execution、approval or any card action。
- Audience expansion、group external/public transition、bot inspection loss、stale/unknown provider snapshot、owner access loss、policy staleness、revoke or expiry MUST prevent rich disclosure。
- New grant MUST apply prospectively and MUST NOT retroactively authorize an earlier message。

### ProviderChannelSecuritySnapshot v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `delivery.provider_channel_security_snapshot.v1`。 |
| `snapshot_id` | string | yes | Stable observation identity。 |
| `provider`, `provider_tenant_key`, `chat_id` | string | yes | Exact provider audience identity。 |
| `channel_type`, `visibility`, `externality` | enum/string | yes | Provider-normalized group/security state。 |
| `bot_membership_status` | enum | yes | Bot 是否仍可见、读取和投递。 |
| `audience_count`, `audience_digest` | integer/string | policy conditional | Audience baseline；用于 expansion/change detection，不证明成员 WorkspaceMembership。 |
| `provider_revision` | string/integer | optional | Provider exposes 时记录。 |
| `observed_at`, `valid_until` | timestamp | yes | Observation freshness boundary。 |
| `evidence_refs` | array | yes | API/event/reconciliation evidence；不包含不必要成员 PII。 |

Snapshot is disclosure evidence, not Workspace authority. Provider event loss is possible, so policy MUST combine event-triggered invalidation with reconciliation and render-time freshness checks.

### ChannelDisclosureDecision v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `identity.channel_disclosure_decision.v1`。 |
| `decision_id` | string | yes | Auditable decision identity。 |
| `tenant_id`, `workspace_id` | string | yes | Exact content/security boundary。 |
| `channel_binding_ref` | object | yes | ChannelThreadBinding id/revision。 |
| `share_grant_ref` | object | conditional | Rich disclosure 时为 active grant id/revision；fallback 可为空。 |
| `source_projection_refs` | array | yes | Exact owner projection ids/revisions/classifications。 |
| `requested_classes` | array | yes | Projector requested normalized disclosure classes。 |
| `provider_snapshot_ref` | object | yes | Exact ProviderChannelSecuritySnapshot evidence。 |
| `policy_ref` | object | yes | Applied disclosure policy id/revision。 |
| `resolved_profile` | enum | yes | `minimal_notice` 或 `workspace_summary`。 |
| `allowed_fields` | array/object | yes | Explicit normalized field allowlist；MUST NOT be an arbitrary source payload。 |
| `allow`, `reason_codes` | boolean/array | yes | Deterministic allow/degrade result。 |
| `decided_at`, `valid_until` | timestamp | yes | Audit and short freshness window。 |

Invariants:

- Rich field MUST be both source-classified `channel_shareable` and allowed by current profile/platform policy；Delivery MUST only reduce, never upgrade classification。
- Raw Trace、private prompt、hidden reasoning、Secret、Tool stdout、internal error detail、full report、patch/diff and unrestricted Artifact content MUST NOT enter P0 shared card even with an active grant。
- Missing/invalid rich authority MUST resolve to `minimal_notice`, which MUST NOT name the protected resource and MAY provide only a canonical non-bearer Web route or authorized per-user ephemeral card。
- Each shared send/update MUST reference a current decision. Renderer MUST consume `allowed_fields` and MUST NOT fetch omitted richer state。
- Grant MUST NOT authorize callback actors. Callback and Web MUST independently map and authorize each actor。
- Revoke/suspend MUST fence future rich output. Provider update/recall is best-effort and MUST record cleanup result/residual exposure without asserting complete erasure。

## DeliveryCardProjection v1

DeliveryCardProjection 是 channel-specific materialized read model，不是 Web DisplayPart mirror、provider card JSON 或业务 aggregate。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `delivery.card_projection.v1`。 |
| `delivery_card_projection_id` | string | yes | 稳定逻辑 projection identity。 |
| `tenant_id` | string | yes | Tenant 安全边界。 |
| `channel_binding_id` | string | yes | 已授权 channel/chat binding。 |
| `card_kind` | enum | yes | `task_progress`、`waiting_user`、`approval` 或 `result`。 |
| `subject_ref` | object | yes | canonical TaskDraft/Task/WorkerSession/ApprovalRequest/Artifact ref；TaskDraft 只允许用于创建前 `waiting_user` 确认。 |
| `source_projection_refs` | array | yes | 构建卡片所依据的 owner/public projection ids + revisions。 |
| `workspace_id` | string | workspace-derived conditional | Subject/visible field 所属 Workspace。 |
| `share_grant_ref` | object | optional | WorkspaceChannelShareGrant id/revision/status；minimal fallback 可为空。 |
| `disclosure_decision_ref` | object | yes | Exact ChannelDisclosureDecision id/policy/provider snapshot revision。 |
| `disclosure_profile` | enum | yes | `minimal_notice` 或 `workspace_summary`。 |
| `projection_revision` | integer | yes | logical card projection 单调 revision。 |
| `render_revision` | integer | yes | 可见内容/action 集合变化时单调递增，用于 callback/update fencing。 |
| `status` | enum | yes | `active`、`waiting_user`、`completed`、`failed`、`cancelled`、`expired` 或 `degraded`。 |
| `title` | string | yes | bounded channel-safe title。 |
| `summary_blocks` | array | yes | typed、sanitized、size-bounded provider-neutral summaries。 |
| `attention` | object | optional | waiting reason/severity/deadline summary。 |
| `available_actions` | array | yes | DeliveryActionDescriptors。 |
| `web_deep_link` | string | yes | Canonical Web route；MUST NOT carry bearer authority，每个 opener 独立鉴权。 |
| `provider_capability_ref` | object | yes | DeliveryProviderCapability id/revision。 |
| `interaction_expires_at` | timestamp | optional | provider/profile-derived inline interaction cutoff。 |
| `update_expires_at` | timestamp | optional | provider/profile-derived card update cutoff。 |
| `generated_at` | timestamp | yes | projection generation time。 |

### DeliveryActionDescriptor v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `action_id` | string | yes | 当前 render revision 内稳定 action id。 |
| `action_type` | enum | yes | Inline：`create_task`、`adopt_task_draft`、`submit_choice`、`submit_text`、`approve_once`、`approve_session`、`cancel`、`retry`；Link：`open_web`、`review_artifact`、`continue_worker`。 |
| `target_ref` | object | yes | canonical command/link target。 |
| `expected_revision` | integer/string | conditional | Owner mutation action 必填。 |
| `input_schema` | object | conditional | choice/text action 的 bounded schema；不能包含 secret。 |
| `expires_at` | timestamp | optional | action-specific expiry。 |
| `opaque_action_token_ref` | string | inline yes | Callback 只携带 opaque/signed reference，不携带 reusable authority。 |

Invariants:

- Projector MUST 只消费 owner events、authorized public facts 和 durable Display summary，MUST NOT 扫描 Web JSON/React output/raw Trace。
- Projector MUST treat ChannelDisclosureDecision `allowed_fields` as maximum input；MUST NOT fetch or infer omitted rich fields。
- Shared Workspace-derived projection without active rich decision MUST use `minimal_notice`; actor-private detail SHOULD use per-user ephemeral provider capability or Web。
- Delivery/CardInstance MUST 只拥有 provider identity、render/delivery/reconciliation/expiry state，MUST NOT 改写 Task/Worker/Approval/Artifact state。
- Inline action MUST 同时存在于最新 owner available actions 和当前 DeliveryCardProjection；Delivery MUST NOT 从 status/error text 推断 retry/cancel/approval。
- `create_task` MUST 只出现在 `waiting_user` + `task_draft` subject 上，绑定 exact draft/readiness/Workspace/render revisions，并生成 ExplicitTaskCreationIntent；它 MUST NOT 表示 Task 已创建成功。
- `create_task` callback MUST verify clicking actor is the TaskDraft initiator。`adopt_task_draft` MAY be exposed to another authorized participant, but it only creates a new actor-owned draft and MUST NOT create Task or consume the source draft。
- Patch apply、branch/PR、production write、Skill install、arbitrary Tool 和 unrestricted Worker chat MUST NOT 成为 P0 inline card action。
- Callback MUST 验证 provider signature、actor mapping、ChannelBinding、Tenant/Workspace access、action token、render revision、expiry 和 duplicate identity，再以 stable idempotency key 提交 Gateway command。
- Provider callback accepted/toast MUST NOT 表示业务成功。只有 owner evidence 推进新 projection/render revision 后才能更新结果。
- Provider callback/update/interaction limits MUST 来自 versioned DeliveryProviderCapability，MUST NOT 改变 Task/Worker/Approval lifecycle。
- Stale、expired、unsupported 或不可完整解释的 action MUST fail closed/degrade to read-only summary + authorized Web link。
- P0 card updates MUST 按 semantic/policy boundary coalesce；MUST NOT 逐 Agent token 更新 provider card。
- Share grant suspension/revocation/expiry or provider audience uncertainty MUST advance projection/render revision to a degraded card and fence richer updates；cleanup outcome MUST be audited separately from owner business state。

### CardInstance v1

CardInstance MUST contain provider/channel/card/message identity, projection id, last applied render revision, template/card schema version, send/update attempt, provider result, last successful render checksum, interaction/update availability, reconciliation state and terminal delivery status. Duplicate/lower render revision MUST no-op/reject; provider response loss MUST reconcile before repeated update when supported and MUST NOT replay the business command.

## Conversation Scope And Participation v1

`Conversation` 可以是单用户 `user` scope，也可以是 provider channel 的 `channel` scope。P0 飞书群 topic 使用一个共享 `channel` Conversation；每个 Turn 仍记录真实 actor，Conversation role 不替代任何资源或动作授权。

| Conversation Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `conversation_id` | string | yes | 稳定 Conversation identity；rollover 产生新的 successor identity。 |
| `tenant_id` | string | yes | Tenant 硬安全边界。 |
| `scope_type` | enum | yes | `user` 或 `channel`。 |
| `scope_ref` | object | yes | user id，或 provider/channel binding scope 的不可变引用。 |
| `created_by` | actor ref | yes | 创建 Conversation 的真实 actor。 |
| `status` | enum | yes | `active`、`control_orphaned`、`superseded` 或 `closed`。 |
| `revision` | integer | yes | Conversation 乐观并发 revision。 |
| `created_at`, `updated_at` | timestamp | yes | 审计时间，不作为消息排序权威。 |

### ConversationParticipant v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `conversation_id` | string | yes | 仅适用于 `channel` scope Conversation。 |
| `actor_user_id` | string | yes | 真实 Switchback User；不得使用群或 binding creator 代替。 |
| `role` | enum | yes | `participant` 或 `controller`。 |
| `source` | enum | yes | `provider_policy`、`explicit_delegation`、`binding_creator`、`rollover_projection` 或 `control_recovery`。 |
| `status` | enum | yes | `active` 或 `revoked`。 |
| `provider_membership_ref` | object | yes | 当前 provider participant eligibility 证据及 revision/freshness。 |
| `revision` | integer | yes | 角色变更并发 fence。 |
| `created_by`, `updated_by` | actor/system ref | yes | 角色来源和最近变更主体。 |
| `created_at`, `updated_at` | timestamp | yes | 审计时间。 |

### ConversationParticipationPolicy v1

版本化策略至少定义：哪些已映射 provider 成员可在首次有效消息时成为 `participant`、controller 创建/委派/恢复所需条件、provider/Tenant access 撤销后的 fencing 行为，以及 `control_orphaned` 时是否允许继续 Workspace-neutral ordinary Turns。策略不能把 provider membership 或 WorkspaceMembership 直接等同于 controller。

Invariants:

- P0 每个 active group `ChannelThreadBinding` revision MUST 指向一个 `scope_type=channel` 的 current primary Conversation；同一 topic 的普通消息进入这份共享 history，而不是按 sender 建立隐藏的 per-user Conversation。
- 每个 accepted Turn/command MUST 保留真实 actor identity；Conversation creator、controller 或 channel identity MUST NOT 代替 actor。
- Binding creator 只有在通过 provider mapping、Tenant 和 channel-binding policy 后才成为初始 controller。Provider group membership、WorkspaceMembership 或 WorkspaceRole MUST NOT 自动授予 controller。
- `participant` MAY 提交 ordinary Assistant Turn；只有 active `controller` MAY 提交 Workspace context select/clear、explicit rollover、Conversation close 或 controller-management command。
- Controller command MUST 继续按真实 actor、target Workspace/resource、current Membership/Policy、expected revision 和 intent evidence 重验。Conversation role MUST NOT 授予 Task、WorkerSession、Approval、Artifact、external provider 或 channel disclosure authority。
- Active channel Conversation SHOULD 保持至少一个 active controller。显式角色变更 MUST reject 移除最后一个 controller；若 access revocation 使所有 controller 失效，Conversation MUST 进入 `control_orphaned` 并 fencing context/lifecycle/control mutations。
- `RecoverConversationControl` MUST 要求 Identity & Access policy-authorized channel administrator、current provider participant eligibility、expected Conversation revision 和审计 evidence；普通 participant MUST NOT 被自动提升。
- ConversationRollover MUST 按当前 policy 把 source generation 中仍有效的 participant/controller 投影到 successor，记录 source participant revision/evidence，且 MUST NOT 在投影中新增或提升 controller。若 successor 没有有效 controller，target Conversation MUST 以 `control_orphaned` 开始。
- Provider/Tenant eligibility 撤销 MUST 阻止未来 Turn/control command，但 MUST NOT 删除或改写历史 actor/Turn。关闭或撤销 channel binding MUST NOT 关闭 Task、WorkerSession 或 Artifact。
- P0 MUST NOT 在一个 group topic 内自动创建 per-user private sidecar Conversation；actor-private detail MUST 使用 provider ephemeral interaction 或逐用户鉴权 Web route。

## ChannelThreadBinding And Inbound Routing v1

`ChannelThreadBinding` 是 provider channel scope 到 current primary Assistant Conversation 的版本化关系，不是可变 Task/Worker focus、权限 grant 或业务 aggregate owner。同一 provider topic 可在 Workspace boundary 上 rollover Conversation identity，但每个 binding revision 只指向一个 current Conversation。

| ChannelThreadBinding Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `delivery.channel_thread_binding.v1`。 |
| `binding_id` | string | yes | 稳定 Switchback binding identity。 |
| `provider` | string | yes | Provider/profile，例如 `feishu`。 |
| `provider_tenant_key` | string | yes | Provider tenant scope。 |
| `tenant_id` | string | yes | Switchback Tenant 硬边界。 |
| `channel_scope` | object | yes | chat id + thread id；provider 无 topic 能力时为版本化 fallback scope。 |
| `conversation_id` | string | yes | Primary Assistant Conversation 和普通消息默认 target。 |
| `conversation_scope_ref` | object | yes | Current `channel` Conversation scope/type reference；防止误绑 user-scoped Conversation。 |
| `conversation_rollover_ref` | object | optional | Latest ConversationRollover id/resulting binding revision。 |
| `default_workspace_id` | string | optional | 显式选择的未来 Assistant context；为空时 Conversation 可继续非 Workspace 澄清。 |
| `workspace_resolution_ref` | object | optional | Current WorkspaceContextResolution id/revision/status。 |
| `binding_revision` | integer | yes | create/rebind/revoke/restore 单调 revision。 |
| `status` | enum | yes | `active`、`revoked` 或 `closed`。 |
| `related_refs` | array | yes | Permission-filtered Task/WorkerSession/Artifact refs，仅供导航/投影。 |
| `provider_capability_ref` | object | yes | Topic/fallback capability snapshot。 |
| `created_at`, `updated_at` | timestamp | yes | 审计时间，不用于消息排序。 |

### InboundInteractionEnvelope v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `delivery.inbound_interaction.v1`。 |
| `provider_event_id` | string | yes | Provider event delivery identity，仅作审计/辅助去重。 |
| `provider_message_id` | string | conditional | Ordinary message primary idempotency identity。 |
| `provider_message_revision` | string/integer | optional | Provider content/edit revision。 |
| `actor_ref` | object | yes | Provider identity + mapped Switchback actor evidence。 |
| `binding_id` | string | yes | Applied ChannelThreadBinding。 |
| `binding_revision` | integer | yes | Routing decision 使用的 exact revision。 |
| `interaction_type` | enum | yes | `ordinary_message`、`structured_command` 或 `card_callback`。 |
| `explicit_target_ref` | object | optional | Validated InteractionTargetRef；缺失时使用 binding Conversation。 |
| `content_ref` | object | yes | Sanitized inline content 或 protected payload ref。 |
| `idempotency_key` | string | yes | Provider message/action identity + revision 派生。 |
| `received_at` | timestamp | yes | 服务端接收时间，仅作审计/时效判断。 |

### InteractionTargetRef v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `target_type` | enum | yes | `assistant_conversation`、`task_draft`、`task`、`worker_session`、`approval` 或 `artifact_action`。 |
| `target_id` | string | yes | Canonical target identity。 |
| `expected_revision` | string/integer | mutation conditional | Owner mutation 的 optimistic concurrency fence。 |
| `target_source` | enum | yes | `default_binding`、`card_action`、`structured_command` 或 `immutable_bot_message_context`。 |
| `intent_evidence_ref` | object | explicit target yes | Authenticated provider/card/command evidence。 |

Invariants:

- `ordinary_message` without validated explicit target MUST route to `conversation_id`; Task/Worker/Card creation MUST NOT change this default。
- Only accepted ConversationRollover MAY replace `conversation_id` under a new binding revision；ordinary Task/Worker/Card lifecycle MUST NOT。
- Direct TaskDraft/Task/WorkerSession/Approval/Artifact action MUST carry a valid InteractionTargetRef and be re-authorized for the current actor/tenant/workspace/owner revision。
- Delivery MUST NOT infer a target from latest activity, latest card, most recent Task/Worker, related refs, provider `parent_id` alone or model output；`current_focus` is not part of the contract。
- A registered immutable bot message MAY provide explicit target evidence only when its interaction semantics and routing metadata are unambiguous；provider reply ancestry alone MUST NOT。
- Provider message duplicate handling MUST use message identity plus content revision when available；provider event id alone MUST NOT be assumed unique enough for ordinary-message idempotency。
- Topic capability fallback MAY change `channel_scope` shape but MUST preserve Assistant-default and explicit-target semantics。
- Revoking/provider-losing a binding MUST fence new channel interaction and MUST NOT silently close or rewrite related Conversation/Task/WorkerSession/Artifact lifecycle。An explicit controller `CloseChannelConversation` command MAY atomically close the Conversation and binding under expected revisions, but still MUST NOT close Task/WorkerSession/Artifact。
- Each channel actor MUST be mapped and authorized independently，并满足 current ConversationParticipant/provider participation policy；Channel membership MUST NOT imply controller、Switchback Workspace authorization 或 action authority。
- Ordinary message acceptance and rollover MUST serialize on binding/source revision；a committed Turn MUST remain in exactly one Conversation generation and stale envelopes MUST NOT be silently moved。
- `task_draft` target MUST come from a current registered ready-draft confirmation action or structured task command, bind exact draft/readiness/Workspace revisions, and produce `ExplicitTaskCreationIntent` before Task creation；plain reply ancestry or unbound confirmation text MUST NOT qualify。

## WorkspaceContextResolution v1

`WorkspaceContextResolution` 是 Conversation/ChannelThreadBinding 的显式默认 Workspace 选择，不是 WorkspaceMembership、AuthorizationDecision、ExecutionAuthorizationLease 或资源迁移命令。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `interaction.workspace_context_resolution.v1`。 |
| `resolution_id` | string | yes | Stable resolution identity。 |
| `tenant_id` | string | yes | Tenant hard boundary。 |
| `conversation_id` | string | yes | Interaction Conversation whose default context changes。 |
| `channel_binding_id` | string | optional | Channel source/projection reference；Web-only selection may omit。 |
| `expected_conversation_revision` | integer | yes | Interaction context optimistic concurrency fence。 |
| `expected_binding_revision` | integer | channel conditional | Channel projection fence。 |
| `resolution_revision` | integer | yes | Workspace context monotonic revision。 |
| `default_workspace_id` | string | conditional | `resolved` 时必填；`cleared` 时为空。 |
| `actor_user_id` | string | yes | Authenticated selecting actor。 |
| `selection_source` | enum | yes | `web_selector`、`card_action`、`structured_command` 或 `create_workspace_result`。 |
| `intent_evidence_ref` | object | yes | Immutable user-selection/CreateWorkspace evidence。 |
| `membership_revision` | integer/string | resolved yes | Observed WorkspaceMembership revision；不等于持续授权。 |
| `status` | enum | yes | `resolved`、`cleared`、`stale` 或 `rejected`。 |
| `reason_codes` | array | yes | Structured clear/stale/reject reasons。 |
| `created_at` | timestamp | yes | Audit time。 |

Invariants:

- Conversation MAY remain unresolved for tenant/user-scoped brainstorming and clarification；Workspace-scoped Truth/Inspiration/Repository/Trace/Artifact access and Task/Worker execution MUST wait for a resolved selection。
- User-global default、single candidate、model inference、URL、recent activity and related refs MAY propose/preselect but MUST NOT create a resolved decision without authenticated positive user intent。
- First unresolved->Workspace selection MUST require expected Conversation revision, binding revision when applicable, and stable idempotency key；success MAY resolve the current Conversation without rollover。
- Selection MUST revalidate TenantMembership and explicit WorkspaceMembership but MUST NOT become a durable grant；every protected read/command/lease MUST reauthorize current policy and Membership。
- TaskSpec MUST snapshot exact workspace id、resolution revision/evidence and Membership revision；Task Orchestration MUST reject stale/missing resolution atomically before creating Task/TaskTransition/Outbox。
- New Workspace creation MUST be separate from Task creation. For the M1 repository-backed flow, an inline UI may orchestrate idempotent `CreateWorkspace` and `RepositoryImport` operations, but TaskSpec submission remains blocked until import produces the first readable WorkspaceRevision and the user explicitly selects the exact workspace/revision result。
- Leaving a previously resolved Workspace by rebind、clear or access revocation MUST use ConversationRollover；it MUST affect only future untargeted context/TaskDrafts and MUST NOT migrate or rewrite Task、WorkerSession、Trace、Artifact or WorkspaceRevision ownership。
- Explicit Task/Worker/Artifact target MUST use the resource's own Workspace and MUST NOT mutate binding default。
- Revoked Membership MUST mark protected use stale and MUST NOT trigger fallback to another candidate。
- WorkspaceContextResolution MUST NOT by itself authorize channel-wide disclosure of Workspace-derived content；rich group output requires a current WorkspaceChannelShareGrant and ChannelDisclosureDecision。

## ConversationRollover v1

`ConversationRollover` 是 Interaction-owned Workspace boundary transition。它在同一 provider topic 下创建 successor Conversation，不是 history merge、Workspace migration 或 provider topic creation。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `interaction.conversation_rollover.v1`。 |
| `rollover_id` | string | yes | Stable rollover identity。 |
| `tenant_id` | string | yes | Tenant hard boundary。 |
| `channel_binding_ref` | object | channel conditional | Binding id + expected revision。Web-only Conversation rollover may omit when no channel binding exists。 |
| `source_conversation_ref` | object | yes | Source Conversation id + expected revision。 |
| `target_conversation_id` | string | yes | Newly created successor identity。 |
| `trigger_type` | enum | yes | `explicit_workspace_rebind`、`explicit_workspace_clear` 或 `access_revoked`。 |
| `source_workspace_resolution_ref` | object | yes | Source Workspace id/resolution revision/status。 |
| `target_workspace_resolution_ref` | object | yes | Target W2 resolution or cleared/unresolved result。 |
| `handoff_ref` | object | yes | ConversationHandoff id/revision/digest。 |
| `actor_ref` | object | explicit conditional | Explicit rebind/clear actor；access revoke uses system/policy actor evidence。 |
| `intent_evidence_ref` | object | explicit conditional | Authenticated selection/clear evidence。 |
| `policy_evidence_ref` | object | revoke conditional | Membership/policy revocation evidence。 |
| `resulting_binding_revision` | integer | channel conditional | Binding revision that points to target Conversation。 |
| `idempotency_key` | string | yes | Stable operation identity。 |
| `completed_at` | timestamp | yes | Commit audit time。 |

### ConversationHandoff v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定为 `interaction.conversation_handoff.v1`。 |
| `handoff_id`, `revision` | string/integer | yes | Immutable handoff identity/revision。 |
| `source_conversation_id`, `target_conversation_id` | string | yes | Exact lineage endpoints。 |
| `handoff_mode` | enum | yes | `workspace_neutral` 或 `explicit_cross_workspace`。 |
| `items` | array | yes | Typed goal/constraint/assumption/blocker items with source scope/classification。 |
| `source_refs` | array | yes | Exact structured source refs；empty is valid for minimal handoff。 |
| `policy_ref` | object | yes | Classification/carry-over policy id/revision。 |
| `authorization_refs` | array | conditional | Cross-Workspace item source-read and target-context decisions。 |
| `content_digest` | string | yes | Immutable checksum。 |
| `created_by`, `created_at` | object/timestamp | yes | Human/system/model-assisted origin and audit；model origin is not authority。 |

Invariants:

- First unresolved->W1 MUST NOT require rollover；W1->W2、W1->cleared and access revoke MUST create a successor and mark source Conversation `superseded`。
- Switching back to a prior Workspace MUST create another successor and MUST NOT reactivate a superseded Conversation。
- Rollover finalize MUST atomically validate idempotency and expected binding/source revisions, create target Conversation/target resolution/handoff/rollover、policy-projected participant/controller relations, mark source superseded, update binding and write Outbox。The transaction MUST NOT perform model、ObjectStore、provider or remote context calls。
- Duplicate/response-loss retry MUST return the original target identity；stale or competing rebind MUST NOT create an extra successor。
- Default `workspace_neutral` handoff MUST NOT contain raw Turns、predecessor summary、Workspace Truth、Repository、Task/Worker/Trace/Artifact refs、Tool/model output、Secret、hidden reasoning or internal errors。
- `explicit_cross_workspace` item MUST have authenticated intent、current source Workspace read、target Workspace context permission、classification/policy and exact source evidence。Model output alone MUST NOT authorize it。
- Existing Task、WorkerSession、Trace、Artifact and WorkspaceRevision identity/ownership MUST remain unchanged；explicit actions continue against resource Workspace。
- ContextCompiler、Turn archive、search/export and Evolution MUST preserve Conversation id/generation、Workspace scope snapshot、resolution revision and rollover lineage；provider topic/lineage MUST NOT authorize cross-scope merge。
- W1 WorkspaceChannelShareGrant MUST NOT authorize W2 output or Workspace names in a rollover notice；current ChannelDisclosureDecision remains required。

## TraceRecord v1

TraceRecord 是 Assistant/Worker/Agent/Tool 的事实轨迹 JSONL 行。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `trace.v1`。 |
| `record_id` | string | yes | 轨迹行 id。 |
| `sequence` | integer | recommended | 同一 TraceStream 内递增序号。 |
| `timestamp` | timestamp | yes | 记录时间。 |
| `tenant_id` | string | yes | 租户边界。 |
| `task_id` | string | conditional | 任务相关轨迹必填。 |
| `conversation_id` | string | conditional | Assistant Turns 相关轨迹必填。 |
| `workspace_scope_snapshot` | object | Assistant/Workspace conditional | Evidence creation 时 exact Workspace/unresolved scope，不由后续 rollover 改写。 |
| `workspace_resolution_ref` | object | Assistant/Workspace conditional | Resolution id/revision/status snapshot。 |
| `conversation_rollover_ref` | object | optional | Predecessor/successor lineage evidence，不授予跨 scope 读取。 |
| `worker_session_id` | string | conditional | Worker 轨迹必填。 |
| `worker_run_id` | string | conditional | WorkerRun 轨迹必填。 |
| `source` | enum | yes | `assistant`、`worker`、`agent`、`tool`、`llm_proxy`。 |
| `type` | enum | yes | `model_call`、`tool_call`、`tool_result`、`late_tool_result`、`command_output`、`artifact`、`error` 等。 |
| `payload` | object | yes | 结构化内容；P0 先保持接近 Coding Agent session JSONL 的保真度，暂不以脱敏作为阻塞项。 |
| `raw_ref` | object | optional | 原始 transcript 的对象存储偏移引用。 |
| `correlation_id` | string | yes | 链路关联 id。 |
| `langfuse_trace_id` | string | optional | 对应 Langfuse trace id。 |
| `langfuse_observation_id` | string | optional | 对应 Langfuse observation/span/generation id。 |

### Invariants

- Trace 是事实轨迹，不是用户展示文案。
- P0 先保留接近普通 Coding Agent 本地 session JSONL 的原始记录能力；Secret 和不可授权数据治理作为后续安全增强补齐。
- 原始 transcript 可保存到受限对象存储，但必须有 retention 和访问策略。
- Langfuse export 是观测投影，不是 Switchback 权威 TraceStore。

## TraceManifest And Data Lifecycle v1

| TraceManifest Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `trace_stream_id` | string | yes | Assistant/Worker TraceStream id。 |
| `tenant_id` | string | yes | 硬 Tenant 边界。 |
| `workspace_id` | string | conditional | Workspace 相关 Trace 必填。 |
| `conversation_id` | string | conditional | Assistant Trace 关联。 |
| `worker_session_id` | string | conditional | Worker Trace 关联。 |
| `worker_run_id` | string | conditional | WorkerRun Trace 关联。 |
| `segments` | array | yes | immutable URI、sequence range、checksum、bytes、record count。 |
| `first_recorded_at` | timestamp | yes | 最早记录时间。 |
| `last_recorded_at` | timestamp | yes | 最晚记录时间。 |
| `schema_versions` | array | yes | segment 中出现的 Trace schema。 |
| `lifecycle_policy_ref` | object | yes | DataLifecyclePolicy id/revision/scope。 |
| `storage_tier` | enum | yes | `hot`、`warm`、`cold`。 |
| `availability` | enum | yes | `online`、`restoring`、`quarantined`、`deletion_pending`、`deleted`、`error`。 |
| `analytics_cursor` | string | optional | TraceAnalyticsProjection 已消费位置。 |
| `langfuse_export` | object | optional | projection cursor/status/error，不作为权威内容。 |

`DataLifecyclePolicy` MUST define policy id/revision/scope, per-data-kind tier thresholds, automatic-delete behavior, quarantine rules and Langfuse projection retention. P0 default is hot 30 days, warm through 180 days, cold indefinitely and no automatic physical deletion.

`DataLifecycleAction` MUST contain `action_id`, actor/role, tenant and target refs, action, reason, before/after state, requested/completed time, result/error and correlation id. P0 does not require a ticket id, four-eyes approval or a separate audit store.

不变量：

- Tier migration MUST NOT rewrite immutable Trace payload or checksum。
- Dashboard queries MUST use normalized idempotent facts/TraceAnalyticsProjection，not scan raw JSONL on request。
- Evolution MUST resolve evidence through TraceManifest；cold source MAY restore asynchronously，deleted source MUST be marked `source_deleted`。
- Quarantined data MUST NOT enter normal download、new Evolution consumption or user projection。
- Explicit delete MUST be idempotent，leave a metadata tombstone，verify payload removal and preserve non-sensitive aggregate metrics。
- Langfuse retention/failure MUST NOT alter authoritative Trace availability。

## RuntimePolicy v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `runtime_type` | enum | yes | P0 为 `local_docker_compatible`。 |
| `agent_profile_id` | string | yes | P0 默认为 CodexAgentProfile。 |
| `image_build_type` | enum | yes | P0 为 `docker_build`。 |
| `agent_control_protocol` | enum | yes | P0 为 `acp`。 |
| `model_proxy_mode` | enum | yes | P0 为 `litellm_colocated`。 |
| `session_max_lifetime_seconds` | integer | yes | P0 为 14 天。 |
| `max_concurrency_per_user` | integer | yes | P0 为 10。 |
| `idle_timeout_seconds` | integer | yes | P0 固定为 `172800`（48 小时）。 |
| `idle_pause_states` | array | yes | P0 至少包含 `waiting_user`；暂停 idle 不暂停 absolute lifetime。 |
| `network_policy` | string | yes | 服务端已解析 profile id；新准入固定为 `trusted_full_network_v1`。 |
| `network_policy_revision` | integer | current yes | 当前 profile 固定为 `1`；历史旧 profile 可省略并按 `0` 读取。 |
| `network_policy_digest` | string | current yes | 当前 profile 的 exact `sha256:` digest；历史旧 profile 可省略为空。 |
| `workspace_mount_policy` | string | yes | P0 固定为 `isolated_working_copy`；RuntimeInstance 禁止直接挂载共享权威 Workspace 为可写。 |
| `secret_injection_policy` | string | yes | 环境变量或挂载配置，长期由 SecretLease 管控。 |
| `session_mapping_policy` | object | yes | WorkerSession 同时只绑定一个 active RuntimeInstance；恢复递增 generation，历史 RuntimeInstance/AgentSession 一对多。 |
| `trace_exporters` | array | yes | 至少包含 `object_storage_jsonl` 和 `langfuse`。 |
| `control_transports` | array | yes | P0 按优先级为 `grpc_bidi`、`https_long_poll`。 |
| `control_fallback_policy` | object | yes | 允许 fallback 的 transport error classes、backoff 和 epoch 切换规则。 |
| `bulk_upload_transport` | enum | yes | P0 为 `resumable_https`。 |
| `heartbeat_interval_seconds` | integer | yes | P0 默认 `15`。 |
| `heartbeat_suspect_after_seconds` | integer | yes | P0 默认 `45`；暂停新命令并探测。 |
| `heartbeat_unhealthy_after_seconds` | integer | yes | P0 默认 `90`；仍未恢复时进入 RuntimeRecoveryIncident。 |
| `max_auto_recovery_attempts_per_incident` | integer | yes | P0 固定为 `1`。 |
| `auto_recovery_failure_classes` | array | yes | P0 只包含 `process_exit`、`container_lost`、`heartbeat_timeout`、`acp_connection_closed`、`cancel_grace_expired`、`tool_cancel_unconfirmed_local` 等有硬证据的 runtime/Agent failure。 |
| `recovery_health_policy` | object | yes | 新 generation 达到健康的 control/LiteLLM/ACP/workspace 条件，以及关闭 incident 前的稳定窗口或持久进度 checkpoint；具体阈值由配置和 spike 冻结。 |

不变量：

- `active_quiet` MUST NOT 出现在 `auto_recovery_failure_classes`，也 MUST NOT 依据全局静默时长自动取消。
- 当前 AgentTurn hard deadline 到期时 MUST 幂等发送 ACP cancel，并取消当前 Tool/process；交互式、无人值守或 `waiting_user` 状态都不能改变这一行为。
- `cancel_grace_expired` 只能在 cancel 已发送且宽限内未取得 terminal prompt response 时成立；宽限候选值由固定镜像 Docker spike 测量。

## Tenant And Workspace Authorization v1

`TenantMembership` MUST contain `tenant_membership_id`, `tenant_id`, `user_id`, `status`, `revision`, `created_at` and `updated_at`.

| WorkspaceMembership Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `workspace_membership_id` | string | yes | 显式成员关系 id。 |
| `tenant_id` | string | yes | 必须与 Workspace tenant 一致。 |
| `workspace_id` | string | yes | 授权目标 Workspace。 |
| `user_id` | string | yes | 成员用户。 |
| `roles` | array | yes | 可组合 `viewer`、`editor`、`reviewer`、`owner`。 |
| `status` | enum | yes | `active`、`suspended`、`revoked`。 |
| `revision` | integer | yes | 乐观并发和撤权判定版本。 |
| `granted_by` | string | yes | 授权主体。 |
| `created_at` | timestamp | yes | 创建时间。 |
| `updated_at` | timestamp | yes | 最近变更时间。 |

`AuthorizationDecision` MUST contain `decision_id`, authenticated `actor_user_id`, `tenant_id`, optional `workspace_id`, `action`, `resource_ref`, `effect`, `membership_revisions`, `policy_revisions`, `allow`, structured `reason_codes`, `constraints`, `decided_at` and `expires_at`.

`ExecutionAuthorizationLease` MUST bind `lease_id`, `decision_id`, `actor_user_id`, `tenant_id`, `workspace_id`, `worker_session_id`, allowed actions, resource constraints, membership/policy revisions, issued time and short expiry. It MUST NOT be accepted after membership/policy revocation or for another runtime/session/actor.

不变量：

- Workspace MUST belong to exactly one Tenant；Tenant mismatch MUST fail before role evaluation。
- Access MUST require active TenantMembership and active explicit WorkspaceMembership；Project/Team association MUST NOT silently grant P0 access。
- Workspace MUST retain at least one active owner；last-owner revoke/downgrade MUST fail。
- WorkspaceRole only authorizes Switchback actions；external action MUST separately validate capability/policy, ApprovalGrant when required, SecretLease and provider authorization。
- Worker MUST act as the initiating actor through a scoped short-lived lease，not as a platform superuser。
- Membership/policy revocation MUST fence new commands, protected reads, Apply, Artifact actions, lease renewal and secret issuance。

## Workspace v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `workspace_id` | string | yes | 持久逻辑工作上下文 id。 |
| `tenant_id` | string | yes | 租户边界。 |
| `created_by_user_id` | string | yes | 创建者审计字段；持续授权由 WorkspaceMembership 决定。 |
| `resource_refs` | array | yes | Artifact、设计文件、外部资料和其他工作内容引用。 |
| `repository_checkouts` | array | yes | 可以为空，也可以包含多个 RepositoryCheckout。 |
| `active_revision_id` | string | conditional | `ready` Workspace 的当前权威 WorkspaceRevision；`provisioning/setup_failed` 时为空。 |
| `write_policy` | object | yes | P0 固定为隔离 WorkingCopy + 串行 WorkspaceApply + revision check。 |
| `status` | enum | yes | `provisioning`、`ready`、`setup_failed`、`archived`、`deleted`。 |

`RepositoryCheckout` 至少包含 `checkout_id`、`repository_ref`、`provider`、`revision`、`workspace_path`、`access_mode` 和 `created_at`。

不变量：

- 每个 WorkerSession MUST 绑定一个 Workspace，一个 Workspace MAY 绑定多个 WorkerSession。
- Workspace MAY 没有 RepositoryCheckout；研究、实验、设计和文档任务不需要伪造 repository。
- Workspace 与 RuntimeInstance identity 分离，runtime 重建不能改变 workspace id。
- Workspace access MUST use Identity & Access AuthorizationDecision；知道 id 或持有外部 token 不能绕过 Membership。
- WorkerSession MUST 使用隔离 WorkingCopy；任何 RuntimeInstance MUST NOT 直接写共享权威 Workspace tree。
- `provisioning/setup_failed` Workspace MUST NOT 创建 Task/WorkerSession；只有成功 RepositoryImport 可创建首个 active revision，后续 active revision 只能由成功 WorkspaceApply 推进。

## RepositorySelectionSnapshot v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `repository_selection_snapshot_id` | string | yes | 服务端签发的不可变选择 identity。 |
| `tenant_id` | string | yes | 租户边界。 |
| `actor_user_id` | string | yes | 发起解析与确认的 Switchback actor。 |
| `repository_ref` | object | yes | provider、installation/namespace 和稳定 repository identity。 |
| `requested_ref` | string | yes | 用户选择的 branch、tag 或 commit 表达。 |
| `resolved_commit_sha` | string | yes | provider 解析得到并由后续导入固定使用的 commit SHA。 |
| `provider_observed_at` | timestamp | yes | 解析远端 ref 的服务端观测时间。 |
| `expires_at` | timestamp | yes | 可用于新 bootstrap 确认的短期窗口；不改变已接受 import 的 SHA。 |
| `repository_authority_evidence_ref` | object | yes | 解析时观测到的本地 allowlist/read policy 或后续 RepositoryAccessGrant evidence，仅作证据，确认和 fetch 仍重验。 |
| `canonical_digest` | string | yes | 覆盖上述字段的版本化 canonical digest/服务端完整性证据。 |

不变量：

- ResolveRepositorySelection MUST 在数据库事务外通过受管 Code Platform adapter 读取 ref；客户端提供的 SHA、URL 或 branch name 本身不得成为受信 pinning evidence。
- Snapshot MUST 绑定 actor、tenant、稳定 repository identity、requested ref 和 exact SHA；过期、digest mismatch、actor/tenant/repository mismatch 的 snapshot 不得用于新 bootstrap。
- Snapshot MUST NOT 代替 repository read authority；M1 CreateWorkspace/fetch MUST 重验本地 allowlist/read policy，后续 authenticated profile MUST 重验 RepositoryAccessGrant、WorkspaceMembership 和 provider authorization。
- 一旦 RepositoryImport 接受有效 Snapshot，远端 branch/tag 移动或 Snapshot 后续过期都不得改写已持久化 SHA。

## RepositoryImport v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `repository_import_id` | string | yes | 同一次 Workspace bootstrap 的稳定 identity。 |
| `workspace_id` | string | yes | 被初始化的逻辑 Workspace。 |
| `repository_ref` | object | yes | provider 与稳定 repository identity，不使用未校验 URL 作为授权。 |
| `selection_snapshot_ref` | object | yes | 被确认的 RepositorySelectionSnapshot id/digest/requested ref/exact commit SHA。 |
| `status` | enum | yes | `queued`、`running`、`succeeded`、`failed`、`cancelled`。 |
| `attempts` | array | yes | append-only attempt id、generation、开始/结束时间、结果分类和脱敏 error ref。 |
| `result_checkout_id` | string | conditional | 成功后创建的 RepositoryCheckout。 |
| `result_revision_id` | string | conditional | 成功后创建的首个 WorkspaceRevision。 |
| `created_by_user_id` | string | yes | 真实 bootstrap actor。 |
| `idempotency_key` | string | yes | Create/bootstrap 响应丢失时返回原 identity。 |
| `revision` | integer | yes | 命令 expected-revision fencing。 |

不变量：

- CreateWorkspace 事务 MAY 创建 `provisioning` Workspace、初始 owner Membership、RepositoryImport intent 和 Outbox，但 MUST NOT 在事务内 clone/fetch 或调用 provider。
- CreateWorkspace MUST 接受仍有效且匹配 actor/tenant/repository 的 RepositorySelectionSnapshot，并把 exact commit SHA 固化进 RepositoryImport；Importer、retry、Task submit 和 Worker dispatch MUST NOT 重新解析 requested ref。
- Import 失败 MUST 把 Workspace 收敛为 `setup_failed`、追加 attempt 证据并保持 `active_revision_id=null`；不得创建 Task、WorkerSession、RepositoryCheckout 或部分 WorkspaceRevision。
- `RetryRepositoryImport` MUST 绑定原 `repository_import_id`、expected revision、当前 actor/grant 和新的命令幂等键；它追加 attempt 而不是创建另一个 Workspace。
- Import 成功 MUST 在一个 PostgreSQL 事务中创建 RepositoryCheckout 和首个 WorkspaceRevision、设置 ActiveRevisionPointer、把 Workspace 置为 `ready`、更新 import result 并写 Outbox。
- `ArchiveWorkspace` MAY 显式归档没有 active revision 的 `setup_failed` Workspace；自动补偿删除不得成为失败收敛路径。

## WorkspaceRevision v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `workspace_revision_id` | string | yes | 不可变 Workspace 版本 id。 |
| `workspace_id` | string | yes | 所属 Workspace。 |
| `parent_revision_id` | string | optional | 上一 active revision。 |
| `repository_manifest` | array | yes | 每个 RepositoryCheckout 的 repository/ref/commit/path。 |
| `file_manifest_ref` | object | yes | 非仓文件、设计材料和 Artifact refs 的 content-addressed manifest。 |
| `created_by_apply_id` | string | optional | 产生该 revision 的 WorkspaceApply。 |
| `created_at` | timestamp | yes | 创建时间。 |

`repository_manifest` 中的 revision MUST 是不可变 commit SHA，不得只保存可移动 branch。`file_manifest_ref` MUST 指向不可变、可校验的内容 manifest。

## WorkingCopy v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `working_copy_id` | string | yes | 隔离副本 id。 |
| `workspace_id` | string | yes | 来源 Workspace。 |
| `worker_session_id` | string | yes | 独占该副本的 WorkerSession。 |
| `base_workspace_revision_id` | string | yes | 创建/最近 rebase 的基线。 |
| `storage_ref` | object | yes | 持久工作副本位置；不位于临时容器层。 |
| `repository_worktrees` | array | yes | per-session worktree/branch 元数据。 |
| `mount_ref` | object | yes | 由 RuntimeProvisioner 解析的 session 专属 mount，不暴露共享权威目录。 |
| `status` | enum | yes | `materializing`、`ready`、`dirty`、`rebasing`、`conflicted`、`archived`。 |

不变量：

- 一个 WorkingCopy 只允许一个 WorkerSession 写；同一 Session 的新 RuntimeInstance generation 复用它。
- WorkingCopy 可并行存在，但不能自行改变 Workspace active revision。
- WorkingCopy 和持久 Agent state MUST 独立于 disposable container layer；删除 RuntimeInstance 不得删除它们。

## ChangeSet v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `change_set_id` | string | yes | 可审查变更集合 id。 |
| `workspace_id` | string | yes | 目标 Workspace。 |
| `working_copy_id` | string | yes | 变更来源。 |
| `base_workspace_revision_id` | string | yes | 生成时基线。 |
| `repository_changes` | array | yes | 每个 checkout 的 base commit、patch/commit、target path 和 assertions。 |
| `file_changes` | array | yes | 非仓文件 create/update/delete、checksum 和 ArtifactRef。 |
| `validation_results` | array | yes | 测试、格式、schema 和其他验收证据。 |
| `preconditions` | array | yes | Apply 前必须成立的条件。 |
| `status` | enum | yes | `draft`、`ready`、`applying`、`applied`、`needs_rebase`、`conflicted`、`rejected`。 |
| `idempotency_key` | string | yes | 防止重复 Apply。 |

## StagingRevision v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `staging_revision_id` | string | yes | 完整候选版本 id。 |
| `workspace_apply_id` | string | yes | 所属 Apply。 |
| `workspace_id` | string | yes | 目标 Workspace。 |
| `base_workspace_revision_id` | string | yes | Prepare 时使用的基线。 |
| `repository_manifest` | array | yes | 已物化并校验的不可变 commit refs。 |
| `file_manifest_ref` | object | yes | 已物化并校验的非仓内容 manifest。 |
| `manifest_digest` | string | yes | Finalize 快速重验完整性。 |
| `storage_ref` | object | yes | 非权威 staging 位置。 |
| `status` | enum | yes | `preparing`、`prepared`、`invalid`、`committed`、`cleanup_pending`、`cleaned`。 |
| `prepared_at` | timestamp | conditional | 完整候选准备时间。 |

不变量：

- StagingRevision MUST 是完整、不可变且非权威的候选；它 MAY 在 WorkspaceWriteLease 外构建。
- 只有成功 WorkspaceApply 引用的 StagingRevision 才能生成 WorkspaceRevision 并被 active pointer 选中。
- 部分物化或校验失败的 staging MUST NOT 暴露为 WorkspaceRevision。

## WorkspaceApply v1

`WorkspaceWriteLease` 至少包含 `lease_id`、`workspace_id`、`holder_apply_id`、单调 `fencing_token`、`acquired_at` 和 `expires_at`。它只覆盖 Apply 临界区，不能被 WorkerRun 长期持有。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `workspace_apply_id` | string | yes | Apply 操作 id。 |
| `change_set_id` | string | yes | 待应用 ChangeSet。 |
| `expected_active_revision_id` | string | yes | 获取租约后必须匹配的 active revision。 |
| `staging_revision_id` | string | conditional | Prepare 完成后的不可变候选。 |
| `write_lease_id` | string | conditional | Finalize 阶段的短时独占 Workspace 写租约。 |
| `fencing_token` | integer | conditional | 防止过期 Apply 在恢复后提交旧结果。 |
| `status` | enum | yes | `preparing`、`prepared`、`finalizing`、`succeeded`、`needs_rebase`、`conflicted`、`fenced`、`failed`。 |
| `result_revision_id` | string | conditional | 成功后新 WorkspaceRevision。 |
| `conflict_ref` | object | optional | revision/file/repository 冲突报告。 |

不变量：

- WorkspaceApply MUST 先在租约外构建并验证完整 StagingRevision，再获取 WorkspaceWriteLease 进入短 Finalize 临界区。
- Finalize MUST 在同一 PostgreSQL 事务中锁定并重验 expected active revision、lease expiry、最新 fencing token 和 staging manifest digest，然后创建 WorkspaceRevision、切换 active revision pointer、记录 Apply 结果并写 Outbox。
- Finalize 事务 MUST NOT 复制大目录、运行测试或调用外部服务。
- expected revision 不匹配时默认 `needs_rebase`，不得 last-write-wins。
- 任一组件失败、租约失效或事务回滚时 active revision MUST 保持不变；残留 staging 可异步清理。
- 数据库提交成功但响应丢失时，重复 idempotency key MUST 返回原 `result_revision_id`，不得重复创建 revision。

## WorkerSession v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `worker_session_id` | string | yes | 用户可打开的 Worker 会话 id。 |
| `workspace_id` | string | yes | 唯一绑定的 Workspace。 |
| `working_copy_id` | string | yes | 该 Session 独占的 WorkingCopy。 |
| `user_id` | string | yes | 会话用户。 |
| `status` | enum | yes | `allocating`、`interactive`、`running`、`recovering`、`waiting_user`、`idle`、`expired`、`closed`、`lost`。 |
| `active_runtime_instance_id` | string | optional | 当前 active runtime generation。 |
| `bound_task_id` | string | optional | 当前唯一绑定的 Task；queued 但尚未取得 slot 的 Task 不在此字段。 |
| `active_worker_run_id` | string | optional | 当前执行窗口。 |
| `session_revision` | integer | yes | execution slot 和 Session 状态变化的单调 revision。 |
| `active_recovery_incident_id` | string | optional | 当前连续故障及一次自动恢复限制。 |
| `continuity_quota_ref` | object | yes | Tenant/User quota identity、reserve revision 与 allocation claim。 |
| `continued_from_worker_session_id` | string | optional | 通过终态续作派生时的 source Session；普通创建为空。 |
| `continued_from_checkpoint_ref` | object | optional | exact source checkpoint id/digest/captured_at/coverage。 |
| `terminal_checkpoint_ref` | object | optional | closed/expired 时可供只读续作的 latest proven durable checkpoint。 |
| `expiry_outcome_ref` | object | optional | absolute expiry 成功 fencing/终态化后的权威 `WorkerSessionExpiryOutcome`。 |
| `idle_expires_at` | timestamp | yes | 当前 idle deadline；waiting_user 时冻结。 |
| `absolute_expires_at` | timestamp | yes | 创建后 14 天，不因暂停或交互延长。 |

状态计数不变量：`allocating/interactive/running/recovering/waiting_user/idle/lost` 计入 continuity quota；`closed/expired` 不计入且不可重开。`lost` 不能仅凭 projection 或 container lookup 释放，必须经 owner reconciliation 提交恢复或终态。Runtime generation recovery 只适用于非终态 Session。

## AgentTurn v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `agent_turn_id` | string | yes | 直接 Worker 交互 id。 |
| `worker_session_id` | string | yes | 目标 WorkerSession。 |
| `agent_session_id` | string | recommended | 目标 ACP/Agent session。 |
| `worker_run_id` | string | optional | 所属或受影响的 active/new WorkerRun。 |
| `mode` | enum | yes | `prompt`、`queue`、`interrupt`、`steer`。 |
| `content` | object | yes | 用户文本、命令或结构化控制信息。 |
| `status` | enum | yes | `accepted`、`queued`、`delivered`、`consumed`、`completed`、`cancelled`、`timed_out` 或 `failed`。 |
| `created_at` | timestamp | yes | 接收时间。 |
| `started_at` | timestamp | active prompt conditional | 后端 Worker Runtime owner 事务接受 `StartAgentTurn` dispatch 的服务端时间；queue 未消费、interrupt/steer 控制消息为空，Shim/ACP invocation time 仅进入 Trace。 |
| `deadline_at` | timestamp | active prompt conditional | 固定为 `started_at + 86400s`，任何状态不暂停、不可延期。 |
| `terminal_at` | timestamp | terminal conditional | Turn 终态提交时间。 |
| `terminal_reason` | string | terminal conditional | 超时固定为 `agent_turn_hard_deadline`。 |

AgentTurn 的 `status=queued` 表示消息等待当前 active WorkerRun 消费，不表示 Task 正在 SessionTaskQueue 等待 execution slot。

不变量：

- Worker 详情页提交 AgentTurn 时不经过 Assistant clarify loop。
- AgentTurn 可以加入 active WorkerRun，也可以在 interactive/idle session 中触发下一次 WorkerRun。只有实际开始 ACP prompt 的 Turn 才建立固定 deadline。
- queue/interrupt/steer MUST 记录目标 AgentSession/WorkerRun/active AgentTurn 和实际消费结果，不能只记录用户输入；它们自身不启动或重置 deadline。queued message 被消费为新 prompt 时沿用该消息 identity 建立一次新的独立 24 小时 Turn。
- 当前 Turn 超时后必须先完成 cancel/fencing 收敛，才能消费 queued message 或接受下一 prompt；Agent、自动重试和 Runtime recovery 不得自行创建新 Turn。
- WorkerSession MAY 承载多个 Task 的 WorkerRun 和不属于 Task 的 standalone WorkerRun；WorkerSession 终态与 Task 终态互不替代。

## WorkerRun Identity v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `worker_run_id` | string | yes | WorkerRun identity。 |
| `worker_session_id` | string | yes | 承载该 Run 的 WorkerSession。 |
| `task_id` | string | conditional | Task 执行/返工触发的 Run 必填；standalone direct Run 必须为空。 |
| `origin_task_id` | string | optional | 直接后续交互的只读来源谱系；不授予修改原 Task 的权利。 |
| `trigger_type` | enum | yes | `task_initial`、`task_rework`、`task_session_continuation`、`direct_agent_turn` 或其他版本化值。 |
| `previous_worker_run_id` | string | optional | Task 返工/到期续作来源；runtime recovery 不设置该字段。 |
| `continuation_source_ref` | object | conditional | `task_session_continuation` 必填，引用 source Session/Run/expiry outcome/checkpoint。 |
| `start_snapshot_ref` | object | yes before execution | WorkerRun 首次 Agent/Tool/WorkingCopy 动作前的实际上下文基线。 |
| `status` | enum | yes | WorkerRun 状态。 |
| `terminal_reason` | string | conditional | `timed_out/failed/cancelled` 的稳定原因；absolute expiry 固定为 `worker_session_absolute_expired`。 |

Invariants:

- `task_initial`、`task_rework` 和 `task_session_continuation` MUST 设置 `task_id`；每次返工/到期续作直接创建新 WorkerRun，不增加 TaskAttempt。
- `direct_agent_turn` 在没有显式新 Task 时 MUST 创建或加入 standalone WorkerRun，不得把范围扩张解释为自动创建 Task。
- 显式创建新 Task MUST 产生新的 Task identity；它只有在 exact WorkerSessionTargetIntent 有效时 MAY 复用 WorkerSession，否则 MUST 在 Workspace 中分配新 Session；它 MUST NOT 追溯修改此前 standalone WorkerRun 的 `task_id`。
- RuntimeInstance replacement MUST NOT 创建新的 WorkerRun；Task 返工 MUST 创建新 WorkerRun 并保留 `previous_worker_run_id`。

## Explicit New Task From Worker Context v1

P0 不发布 FollowUpTaskProposal 聚合。Worker MAY 向用户建议“创建新的可跟踪 Task”，但创建命令仍复用 `ExplicitTaskCreationIntent v1` + `TaskSpec v1`：

- intent evidence MUST 由认证 Gateway/Delivery/API adapter 从用户动作生成；模型输出、建议文本或 Worker 自报字段不是授权。
- Task Orchestration MUST 重验 actor、Tenant/Workspace Membership、TaskReadinessAssessment、TaskSpec digest/validator 和当前 policy revision。
- 一个 idempotency key MUST 最多创建一个 Task、一个初始 TaskTransition 和对应 Outbox；响应丢失时返回原 identity。
- exact preferred WorkerSession 只有在 WorkerSessionTargetIntent 有效时 MAY 被复用，并继承当前 Session 状态；active Run 不得被隐式 interrupt。无 target 或 Session 失效时在同一授权 Workspace 分配新 Session，不能复活或静默清理原 Session。
- 新 Task MAY 引用来源 Task/Run 作为 lineage，但 MUST NOT 追溯修改 standalone WorkerRun 的 `task_id`。
- 若未来需要可审核、可过期、多人协作的 proposal inbox，再在 P1 引入独立协议。

## WorkerControlBootstrap v1

Shim 主动调用 bootstrap，输入至少包含：

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `worker_session_id` | string | yes | 目标 WorkerSession。 |
| `runtime_instance_id` | string | yes | 当前 RuntimeInstance。 |
| `runtime_generation` | integer | yes | Runtime generation fencing。 |
| `supported_transports` | array | yes | 按客户端优先级声明，P0 为 gRPC bidi 与 HTTPS long-poll。 |
| `last_acked_command_sequence` | integer | yes | Shim 已持久接受/处理的命令 cursor。 |
| `last_confirmed_event_sequence` | integer | yes | 后端已确认的事件 cursor。 |
| `protocol_versions` | array | yes | 支持的 RuntimeControlEnvelope 版本。 |

响应至少包含 `selected_transport`、`channel_epoch`、`channel_token_ref`、transport endpoint、heartbeat interval/suspect/unhealthy thresholds、long-poll timeout、batch limit 和 server cursors。

## RuntimeControlEnvelope v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | `runtime.control.v1`。 |
| `message_id` | string | yes | command/event/ack/heartbeat 唯一 id。 |
| `kind` | enum | yes | `command`、`event`、`ack`、`heartbeat`。 |
| `worker_session_id` | string | yes | 会话。 |
| `runtime_instance_id` | string | yes | runtime。 |
| `runtime_generation` | integer | yes | runtime fencing generation。 |
| `channel_epoch` | integer | yes | transport connection epoch。 |
| `sequence` | integer | yes | 对应方向 stream 内单调序号。 |
| `ack_sequence` | integer | optional | 已连续确认 cursor。 |
| `idempotency_key` | string | yes | 跨重连/transport 去重。 |
| `message_type` | string | yes | 具体 command/event 类型。 |
| `payload` | object | yes | 版本化负载。 |
| `sent_at` | timestamp | yes | 发送时间，仅用于审计。 |

不变量：

- 同一 `(runtime_instance_id, runtime_generation)` 同时只允许一个 active `channel_epoch`；后端 MUST 拒绝旧 epoch 的 command ACK、heartbeat 和 event。
- gRPC 与 HTTPS fallback MUST 使用完全相同的 Envelope、sequence、ACK 和幂等状态；transport 不是业务状态来源。
- command ACK MUST 区分 `received`、`accepted`、`completed`，未 completed 的命令可安全重投。
- fallback MUST 只响应 transport/connectivity/HTTP2/proxy 错误；认证、授权、策略、schema 和 protocol version 错误 MUST fail closed。
- Trace/Artifact 内容 MUST NOT 内嵌到控制 Envelope；仅传 upload intent/ref/checksum，文件经 resumable HTTPS 上传。

## RuntimeInstance v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `runtime_instance_id` | string | yes | 一代实际 runtime id。 |
| `worker_session_id` | string | yes | 所属 WorkerSession。 |
| `generation` | integer | yes | 从 1 开始递增的恢复代次。 |
| `runtime_type` | enum | yes | `docker_container` 或未来 `kubernetes_pod`。 |
| `container_id` | string | conditional | Docker/POD runtime 标识。 |
| `status` | enum | yes | `starting`、`active`、`suspect`、`unhealthy`、`draining`、`terminated`、`lost`。 |
| `recovery_incident_id` | string | optional | 若该 generation 由自动恢复创建，记录来源 incident。 |
| `recovery_attempt` | integer | optional | 在该 incident 内的自动恢复序号；P0 只能为 `1`。 |
| `started_at` | timestamp | yes | 启动时间。 |
| `ended_at` | timestamp | optional | 结束时间。 |

不变量：

- 同一 WorkerSession 同一时刻最多有一个 `active` RuntimeInstance。
- 崩溃恢复 MUST 复用 `worker_session_id`、创建更大的 generation，并保留历史 instance/container mapping。
- RuntimeInstance identity 与 ACP/AgentSession identity 分离。

## RuntimeHeartbeat v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | `runtime.heartbeat.v1`。 |
| `heartbeat_id` | string | yes | 心跳唯一 id。 |
| `runtime_instance_id` | string | yes | 发送心跳的 runtime。 |
| `runtime_generation` | integer | yes | runtime fencing generation。 |
| `channel_epoch` | integer | yes | 当前 active control epoch。 |
| `sequence` | integer | yes | 同一 generation 单调递增。 |
| `shim_uptime_ms` | integer | yes | Shim monotonic uptime，仅用于诊断，不用于服务端 timeout 计时。 |
| `shim_process_state` | enum | yes | `healthy`、`degraded`、`stopping`。 |
| `agent_process_state` | enum | yes | `not_started`、`healthy`、`exited`、`unresponsive`、`unknown`。 |
| `agent_operation_state` | enum | yes | `idle`、`running_model`、`running_tool`、`waiting_user`、`draining`、`unknown`。 |
| `active_command_id` | string | optional | 当前处理命令。 |
| `last_acp_event_sequence` | integer | optional | 最近已观察 ACP event cursor。 |
| `last_progress_at` | timestamp | optional | 最近结构化 Agent/tool progress，仅作 Agent liveness 证据。 |
| `sent_at` | timestamp | yes | 容器 wall clock 时间，仅用于审计。 |

不变量：

- Shim MUST 独立于模型 stream 和 tool process 每 15 秒发送 heartbeat；长模型推理、长 tool call 或没有 token 输出不得阻塞 heartbeat。
- 后端 timeout MUST 使用 heartbeat 的服务端 receive time 和单调 sequence；不得使用容器 `sent_at` 作为唯一 freshness 依据。
- 若 exact `(runtime_instance_id, runtime_generation, channel_epoch)` 尚无已持久化 heartbeat observation，提交 `ResumeComplete` 的同一 PostgreSQL 事务 MUST 以首次 `resume_completed_at` 和 exact resume revision 创建 durable first-heartbeat watchdog，并写入 T+45 suspect ScheduledCommand；任一写入失败 MUST 回滚整个 resume completion。
- 同一 Runtime 的 duplicate `ResumeComplete` 或 transport reconnect MUST 复用既有 watchdog、首次 anchor 和 T+45/T+90 due time，不得重置健康窗口或生成第二套定时命令。WorkerControl transport close 本身只形成 disconnect/reconnect 证据，不得创建 recovery incident。
- T+90 前到达的第一条真实、认证且 sequence 合法的 heartbeat MUST 在 heartbeat observation 的同一事务中把 `awaiting|suspect` watchdog 置为 `satisfied`，记录 heartbeat/control sequence 与服务端 receive time，并取消仍为 pending 的 watchdog command；不得用 sequence `0` 或 `ResumeComplete` 伪造心跳。
- 为覆盖 durable control journal 已接受 heartbeat、但普通 ordered event pump 尚未完成 business observation 的 Backend crash/backlog 窗口，T+45/T+90 evaluator MUST 在持有同一 Session、Runtime 与 watchdog 锁时，按 control sequence 查找全部 `effect_status=received|applied` 且 `created_at < unhealthy_due_at` 的候选 event。只有通过 canonical Envelope 与 RuntimeHeartbeat payload 严格解码、且 identity 与 watchdog 完全一致的候选 MAY 原子补写 heartbeat observation、`satisfied` proof 与后续普通 liveness command；第一条有效 heartbeat 冻结 first-heartbeat proof，后续有效 heartbeat MUST 把普通 liveness observation 推进到最新单调 sequence/receive time。无效 payload、identity 冲突或 exact T+90 及之后的 event MUST NOT 满足 watchdog。
- evaluator 的补投影 MUST NOT 把 journal message 标记为 applied，也 MUST NOT 推进 `event_applied_sequence` 或越过更早的 ordered event。原 ordered pump 后续仍按 sequence 应用该 heartbeat；observer/store 对相同 heartbeat proof MUST duplicate-safe，不得重复递增 observation revision、重置 first-heartbeat proof 或生成第二个 liveness command。
- heartbeat recorder、first-heartbeat evaluator 与普通 liveness evaluator 在锁可变 Session/Runtime 图之前 MUST 解析 exact active AgentTurn、取得同一 AgentTurn outcome advisory lock，并在图锁后重验 Run/Turn identity。这样已先进入临界区但尚未提交的 terminal/deadline claim 也先于 liveness mutation；若 active Run/Turn 恰在预读与图锁之间切换，store MUST 通过有界跨事务 retry 重新进入新 identity 的锁域，不得关闭 control stream 或让 startup drain 因一次瞬态冲突失败。已存在 owner 时 watchdog 分别收敛为 `turn_outcome_owned` 或 `backend_deadline_owned`，不得再把同一 Runtime 标记为 unhealthy 或打开 recovery incident。
- settlement owner 已建立后到达的有效 heartbeat 仍 MUST 保存 observation，并 MAY 在严格早于 T+90 时完成 first-heartbeat proof；即使 `received_at >= unhealthy_due_at` 且 watchdog 尚未被 evaluator 取消，也不得回滚 outcome-owned heartbeat transaction 或关闭 control stream。heartbeat recorder MUST NOT 把 `suspect` Runtime 恢复为 `active`、递增 Runtime revision，或取消/重建普通 liveness command。业务终态 owner 完成后，后续新 heartbeat 才可按新 revision 恢复普通 liveness。
- 45 秒未收到有效 heartbeat 时 RuntimeInstance 进入 `suspect`：暂停新命令，主动探测 transport/container/process，并允许 control transport fallback；active WorkerRun 不因 suspect 自动失败。
- 90 秒仍未恢复时普通 live Runtime 进入 `unhealthy`，持久化稳定的 `heartbeat_unhealthy` recovery intent 并可创建一个 RuntimeRecoveryIncident；若该 Runtime 已是现有 incident 的 replacement，则记录 `replacement_first_heartbeat_timeout` 并耗尽该 incident，不得递归创建 nested incident。边界时刻及之后到达的 heartbeat 不得复活该 unhealthy generation。
- Backend startup MUST 在开放普通 dispatch 前 drain 已到期的 AgentTurn deadline 与 first-heartbeat watchdog command。恢复候选的 server-side liveness anchor 依次使用最新 heartbeat receive time、durable first-authenticated-resume anchor、Runtime `started_at`、Runtime `created_at`，因此 restart 不得把首次心跳窗口重新计时。若 exact Runtime 已由未到 `force_after` 的 Backend-deadline settlement 拥有，startup 在重验环境、容器和 credential identity 后 MUST 立即标记该候选 deferred，不得让单个 reconnect grace 阻塞全局 readiness；后续由 durable deadline worker 与 ready 后 unowned-Runtime adoption 收敛。
- 固定 ACP/Codex 栈没有安全 ping/no-op。Agent unresponsive MUST 由 process exit、stdio EOF、ACP connection close 等硬证据，或 cancel 后 grace 内无法取得 terminal prompt response 判定；仅缺少模型 token、文本或 UI progress MUST NOT 触发。

当前实现证据包括 heartbeat observation/watchdog satisfy 事务竞态、到期 evaluator、durable journal backlog 补投影、ordered pump duplicate-safe replay 和 startup drain 的数据库级覆盖；production/fixed-image 验收仍应记录这些 proof，但该验收缺口不改变上述已实现的数据库收敛语义。

## RuntimeRecoveryIncident v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `recovery_incident_id` | string | yes | 一次连续故障事件 id。 |
| `worker_session_id` | string | yes | 保持不变的逻辑 WorkerSession。 |
| `worker_run_id` | string | optional | 故障发生时的 active WorkerRun。 |
| `failed_runtime_instance_id` | string | yes | 被 fencing 的故障 runtime。 |
| `trigger` | enum | yes | M1 新 incident eligible 值固定为 `container_exit`、`container_missing`、`acp_eof`、`heartbeat_unhealthy`。`worker_control_closed` 仅为读取、推进历史已持久化 incident 保留，MUST NOT 用于新 incident。 |
| `failed_generation` | integer | yes | 被 fencing 的 Runtime generation。 |
| `failed_channel_epoch` | integer | yes | 被 fencing 的 WorkerControl epoch。 |
| `phase` | enum | yes | `open`、`fencing`、`provisioning`、`reattaching`、`verifying`、`recovered`、`exhausted`。 |
| `attempt_count` | integer | yes | 已启动的自动恢复次数。 |
| `max_auto_attempts` | integer | derived | P0 server policy 固定为 `1`，不由 TaskSpec 或调用者存储/覆盖。 |
| `replacement_runtime_instance_id` | string | optional | 自动重建的新 generation。 |
| `replacement_generation` | integer | optional | 必须等于 `failed_generation + 1`。 |
| `replacement_channel_epoch` | integer | optional | 必须大于 failed epoch。 |
| `fence_status` | enum | yes | `pending`、`confirmed` 或 `unproven`。 |
| `mount_fence_token` | integer | optional | 证明新写入 authority 的单调 fencing token。 |
| `continuity_status` | enum | yes | `pending`、`proven` 或 `unproven`。 |
| `evidence_ref` | string | yes | heartbeat、exit、Trace cursor、health check 或恢复错误的安全引用；原始 secret/provider payload 不进入 incident。 |
| `evidence_at` | timestamp | yes | 触发证据时间。 |
| `outcome` | enum | optional | terminal phase 下为 `recovered` 或 `exhausted`。 |
| `outcome_reason` | string | optional | 稳定恢复、fencing/continuity/health 失败或 lifecycle preemption 的稳定原因。 |
| `revision` | integer | yes | incident owner 单调 revision。 |
| `opened_at` | timestamp | yes | incident 创建时间。 |
| `updated_at` | timestamp | yes | 最近 phase 提交时间。 |
| `closed_at` | timestamp | optional | recovered/exhausted 时的关闭时间。 |

不变量：

- 一个 WorkerSession 同时最多有一个 open/recovering RuntimeRecoveryIncident。
- 自动重建前 MUST fencing 旧 runtime generation 和 ChannelEpoch；新 generation MUST 复用 WorkerSession、Workspace、WorkingCopy、持久化 Agent state 和已确认 cursors。
- replacement 挂载 WorkingCopy 前 MUST 终止或隔离旧 container、撤销旧 generation control/secret lease，并获得新的独占 mount lease；无法证明写独占时 MUST 进入 `waiting_user`。
- 新 generation MUST 重新签发 SecretLease；仍有效的 WorkerSession ApprovalGrant 只在 capability/target/constraint/expiry 继续匹配时生效。
- 同一 incident 的 `attempt_count` 达到 `max_auto_attempts` 后，任何再次失败 MUST 进入 `waiting_user`，不得再次自动创建 RuntimeInstance。
- 只有 authenticated control、独立 heartbeat、workspace mount 和 exact ACP AgentSession 均通过健康与恢复检查，并满足服务端 30 秒稳定窗口或完成持久进度 checkpoint，incident 才能进入 `recovered`；ACP resume/load 失败不得静默创建全新 AgentSession。
- 用户取消、idle/absolute timeout、鉴权/授权、策略、schema/protocol 错误 MUST NOT 创建自动恢复 incident。
- AgentTurn deadline 或 WorkerSession absolute expiry 在恢复期间继续流逝并可抢占 incident；抢占必须在同一 owner transaction 中收敛原 lifecycle outcome 和 Outbox。
- `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=0` MUST 只禁止创建新 incident；已持久化的非终态 incident仍须 reconciliation 到 recovered/exhausted。

## ArtifactRef v1

ArtifactRef 描述任务产物，而不是直接承载产物内容。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `artifact.v1`。 |
| `artifact_id` | string | yes | 产物 id。 |
| `task_id` | string | yes | 关联任务。 |
| `worker_run_id` | string | optional | 关联执行尝试。 |
| `type` | enum | yes | `patch`、`change_set`、`branch`、`pull_request`、`report`、`test_log`、`screenshot`、`checkpoint`、`raw_transcript`；P0 用户交付只启用 report/patch。 |
| `uri` | string | yes | 对象存储或外部系统引用。 |
| `checksum` | string | yes | 内容校验。 |
| `permission` | object | yes | 租户、可见性、策略标签。 |
| `retention_policy` | string | yes | DataLifecyclePolicy id/revision；P0 默认 hot 30d、warm 180d、cold 长期且不自动删除。 |
| `storage_tier` | enum | yes | `hot`、`warm`、`cold`。 |
| `availability` | enum | yes | `online`、`restoring`、`quarantined`、`deletion_pending`、`deleted`、`error`。 |
| `completeness` | enum | yes | `complete` 或 `partial`；异常 drain/absolute expiry 产生的未完成产物必须标记 partial。 |
| `completeness_reason` | string | conditional | partial 时的稳定原因，例如 `worker_session_absolute_expired`。 |
| `source_checkpoint_ref` | object | optional | 产物对应的 durable checkpoint/provenance。 |
| `available_actions` | array | yes | renderer 可提供的 `preview`、`download`、`apply_to_workspace`、`publish_branch` 等动作。 |
| `created_at` | timestamp | yes | 创建时间。 |

### ArtifactAction invariants

- `preview` 和 `download` 不改变 Workspace。
- `apply_to_workspace` MUST 指定 target workspace/checkouts、base revision 和冲突策略。
- `apply_to_workspace` MUST 消费一个 ready ChangeSet，经 WorkspaceApply 返回 result revision 或 conflict；不得直接把 patch 写进共享目录。
- `publish_branch` 是显式外部写入，MUST 经过 CodePlatform capability、认证和匹配的 RepositoryAccessGrant；它不是 P0 默认自动产物。`worker_session` Grant 已生效时 MUST NOT 再逐命令请求同范围审批。
- `partial` Artifact MAY preview/download/review when authorized, but MUST NOT independently satisfy auto acceptance、publish/apply safety checks or imply acceptance criteria completion。

## MemoryPublicationPolicy v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `policy_id` | string | yes | 稳定 policy id。 |
| `revision` | string | yes | 不可变版本。 |
| `scope` | object | yes | 适用 tenant/user/workspace/project/repo。 |
| `auto_publish_types` | array | yes | P0 固定为 `explicit_preference`、`verified_descriptive_fact`、`entity_alias`、`ephemeral_project_state`。 |
| `allowed_evidence_classes` | array | yes | `user_explicit_statement`、`authoritative_source`、`deterministic_runtime_verification`。 |
| `forbidden_risk_tags` | array | yes | 敏感、冲突、权限、安全、生产、外部副作用、跨用户/org、行为规则、失败归纳、主观评价等。 |
| `ttl_rules` | object | yes | 各 memory type 的 refresh/expiry 规则；`ephemeral_project_state` 必须有 expires_at。 |
| `content_hash` | string | yes | 决策重放与完整性校验。 |

不变量：

- `model_inference`、模型置信度、摘要或行为频率 MUST NOT 单独满足自动发布证据。
- `explicit_preference` MUST 只表示低风险选择倾向；涉及权限、生产/外部操作、强制流程或行为边界时 MUST 重新分类并人工审核。
- 自动发布 MUST 同时通过 type allowlist、evidence、risk、sensitivity、conflict、scope/ACL 和 lifecycle gate；任一失败进入 `needs_review` 或 `rejected`。
- Policy 更新 MUST 创建新 revision；历史 TruthPublicationDecision 继续引用原 policy snapshot。

## Truth v1

Truth 是长期上下文，不是普通 RAG 文档。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `truth.v1`。 |
| `truth_id` | string | yes | Truth id。 |
| `tenant_id` | string | yes | 租户边界。 |
| `scope_type` | enum | yes | `user`、`workspace`、`project`、`repo`、`org`。 |
| `scope_id` | string | yes | 作用域 id。 |
| `truth_type` | enum | yes | `explicit_preference`、`verified_descriptive_fact`、`entity_alias`、`ephemeral_project_state`、`inferred_preference`、`procedure`、`constraint` 或 `skill_hint`。 |
| `governance_kind` | enum | yes | `simple_memory`、`behavioral_memory`、`instruction_policy`；Skill 正文不使用 Truth schema。 |
| `content` | string | yes | 可直接被上下文编译器使用的内容。 |
| `status` | enum | yes | `candidate`、`active`、`needs_review`、`rejected`、`deprecated`、`expired`。 |
| `confidence` | number | yes | 置信度。 |
| `risk_level` | enum | yes | `low`、`medium`、`high`。 |
| `source_refs` | array | yes | Trace/Artifact 证据。 |
| `evidence_class` | enum | yes | `user_explicit_statement`、`authoritative_source`、`deterministic_runtime_verification` 或 `model_inference`。 |
| `permissions` | object | yes | 可见性和策略标签。 |
| `lifecycle` | object | yes | 创建、刷新、过期时间。 |
| `review` | object | yes | 是否需要审核和审核结论。 |
| `publication_decision` | object | yes | 模型建议、确定性 policy gate、理由、模型/policy 版本和人工 override。 |
| `current_revision_id` | string | yes | 当前不可变 ContentRevision。 |
| `openviking_projection` | object | conditional | active revision 的 URI、target revision、snapshot、状态和错误。 |

### Invariants

- 没有 source refs 的内容不能成为 Truth。
- 高风险 Truth 默认 `needs_review`。
- 模型可以建议自动发布，但安全、生产、跨用户/工作区、冲突和低置信候选 MUST 由确定性策略强制进入审核。
- 只有 `explicit_preference`、`verified_descriptive_fact`、`entity_alias` 和 `ephemeral_project_state` 可进入自动发布门控；还必须低风险、非敏感、无冲突、scope/ACL 不扩张，并具有用户明确原话、权威来源或确定性运行验证。
- `ephemeral_project_state` MUST 设置 `refresh_after` 和 `expires_at`；其他可变事实按 MemoryPublicationPolicy 设置 refresh/expiry。过期 revision MUST 从 active retrieval 移除。
- 模型推断、摘要、置信度或重复行为本身 MUST NOT 授权自动发布。推断偏好、失败归纳、主观评价、生产/外部副作用、权限安全、跨用户/org 和敏感候选 MUST 进入审核或拒绝。
- `behavioral_memory` 和 `instruction_policy` 新增或修改 MUST 人工审核；SkillPackage 由 Skilling 使用独立内容模型治理。
- Truth 检索必须强制 scope 和 permission 过滤。
- OpenViking 只包含批准后的 active revision；candidate、review、history、tombstone 和 audit MUST 留在 Switchback RDBMS。

## ContentRevision v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `revision_id` | string | yes | 不可变修订 id。 |
| `content_id` | string | yes | MemoryTruth、InstructionPolicy 或 SkillPackage id。 |
| `content_kind` | enum | yes | `memory_truth`、`instruction_policy`、`skill_package`。 |
| `parent_revision_id` | string | optional | 上一修订。 |
| `content` | object | yes | 版本化内容与 metadata。 |
| `content_hash` | string | yes | 内容校验。 |
| `diff_ref` | object | optional | 可审查 diff。 |
| `source_refs` | array | yes | Trace/Artifact/用户编辑证据。 |
| `created_by` | string | yes | 模型、用户或服务主体。 |
| `created_at` | timestamp | yes | 创建时间。 |

## MemoryLibraryItem v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `memory_id` | string | yes | MemoryTruth id。 |
| `current_revision_id` | string | yes | 当前展示修订。 |
| `truth_type` | enum | yes | Memory 类型。 |
| `scope` | object | yes | user/workspace/project scope 与 ACL 摘要。 |
| `status` | enum | yes | `active`、`disabled`、`expired` 或 `deleted`。 |
| `content_summary` | string | yes | 用户可读内容摘要。 |
| `source_summary` | object | yes | evidence class、来源标题和可访问 source refs。 |
| `publication_mode` | enum | yes | `auto_approved` 或 `human_approved`。 |
| `risk_and_policy` | object | yes | risk tags、policy revision 和 decision reason。 |
| `lifecycle` | object | yes | refresh、expiry 和 freshness 状态。 |
| `usage` | object | yes | last_used_at、use_count、last_retrieved_by。 |
| `projection` | object | yes | OpenVikingProjection status 与最新错误摘要。 |
| `available_actions` | array | yes | 当前 actor 可执行的 edit/disable/delete/restore。 |

## ReviewTask v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `review_task_id` | string | yes | 审核任务 id。 |
| `content_id` | string | yes | 待审 governed content id。 |
| `candidate_revision_id` | string | yes | expected candidate revision。 |
| `lane` | enum | yes | `memory`、`procedure`、`instruction` 或 `skill`。 |
| `scope` | object | yes | 目标 scope、ACL 与 owner。 |
| `content_diff_ref` | object | yes | 候选相对当前 revision 的可审查 diff。 |
| `evidence_refs` | array | yes | 原始证据引用。 |
| `risk_tags` | array | yes | 敏感、冲突、权限、安全、跨 scope 等标签。 |
| `conflict_refs` | array | yes | 冲突 Truth/Revision 引用。 |
| `publication_decision_ref` | object | yes | 模型建议、确定性 policy 结果及版本。 |
| `reviewer_requirement` | object | yes | 所需 role/scope/owner，以及是否禁止 batch approve。 |
| `status` | enum | yes | `pending`、`approved`、`rejected`、`superseded` 或 `cancelled`。 |
| `created_at` | timestamp | yes | 创建时间，用于 age 展示。 |

## ReviewBatchCommand v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `batch_id` | string | yes | 幂等批量操作 id。 |
| `action` | enum | yes | `approve`、`reject` 或 `disable`。 |
| `actor` | object | yes | user/reviewer identity 与 authority snapshot。 |
| `items` | array | yes | review/content id、expected revision 和 expected status。 |
| `reason` | string | conditional | reject/disable 或策略要求时必填。 |
| `idempotency_key` | string | yes | 重试去重。 |
| `status` | enum | yes | `accepted`、`running`、`completed` 或 `completed_with_errors`。 |
| `item_results` | array | yes | 每项 `succeeded`、`stale`、`forbidden`、`invalid` 或 `failed` 及原因。 |

不变量：

- Batch approve MUST 只包含同 scope、同 MemoryPublicationPolicy revision、同低风险 Memory type、同 reviewer authority 且没有敏感/冲突/scope expansion tag 的 Memory ReviewTask。
- Procedure、Instruction、Skill、权限安全、跨 scope 和冲突 ReviewTask MUST 设置 `batch_approve_forbidden=true` 并逐条批准。
- Batch reject/disable MAY 混合项目，但 MUST 对每项独立鉴权并校验 expected revision/status；失败项不得改变状态。
- 批量命令 MUST 返回逐项结果；部分成功只能报告 `completed_with_errors`，不得静默吞掉失败。
- user-scope Memory 可由本人 edit/disable/delete/restore；workspace/project scope MUST 匹配 owner/reviewer policy。
- edit/restore MUST 创建新 ContentRevision；disable/delete MUST 触发 Outbox 移除 active projection，delete 还 MUST 保留 tombstone。UI MUST NOT 直接调用 OpenViking CRUD。
- P0 pending ReviewTask 没有硬 SLA，MUST NOT 进入 ContextPack 或阻塞当前 Task/Worker；age 与提醒只属于 projection/notification。

## OpenVikingProjection v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `projection_id` | string | yes | 投影记录 id。 |
| `content_id` | string | yes | 对应 Switchback governed content。 |
| `target_revision_id` | string | yes | 应投影的批准修订。 |
| `viking_uri` | string | yes | active context URI。 |
| `status` | enum | yes | `publish_pending`、`active`、`failed`、`removing`、`removed`。 |
| `snapshot_id` | string | optional | 发布后显式 snapshot。 |
| `last_error` | object | optional | 结构化同步错误。 |
| `updated_at` | timestamp | yes | 最近同步时间。 |

不变量：

- RDBMS approval 与 Outbox MUST 在同一事务提交；OpenViking projection 异步幂等执行。
- projection 未成功前不得把 revision 暴露为可检索 active Truth。
- 用户看板修改、删除或恢复先产生 ContentRevision/tombstone，再更新 projection；UI 不直接写 OpenViking。

## ContextPack v1

ContextPack 是一次上下文编译结果，由 Assistant 或 Worker 结合 Evolution Truth 读取结果、Inspiration 资源候选、Rules 和 Skills 自行编译。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `schema_version` | string | yes | 固定形如 `context.pack.v1`。 |
| `context_pack_id` | string | yes | 上下文包 id。 |
| `request` | object | yes | tenant/user/project/task/budget。 |
| `items` | array | yes | 被选中的 Truth query result、resource candidate、rule、skill manifest、summary。 |
| `dropped` | array | yes | 因预算、权限、过期、低相关性被丢弃的候选。 |

### Invariants

- Assistant 和 Worker 通过 Evolution 读取 Truth，通过 Inspiration 获取资源索引和简要内容；需要完整飞书文档时，使用 lark-cli 和对应 Skill 按链接读取。
- 每个 item MUST 有 source ref、priority 和 token estimate。

## Skill/MCP Manifest v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `manifest_id` | string | yes | manifest id。 |
| `kind` | enum | yes | `skill`、`mcp_server`、`agent_profile`。 |
| `source_type` | enum | yes | `preinstalled`、`evolution_generated`、`marketplace`、`user_repository`。 |
| `name` | string | yes | 名称。 |
| `version` | string | yes | 版本。 |
| `scope` | object | yes | user/project/org/tenant 可用范围。 |
| `permissions` | array | yes | 文件、网络、密钥、工具、模型权限声明。 |
| `entrypoint` | object | conditional | Skill 或 MCP 的加载入口。 |
| `dependencies` | array | optional | 依赖能力。 |
| `audit` | object | yes | owner、created_at、updated_at、content/security review status 和 revision ref。 |
| `installation_policy` | object | yes | 是否需要 CapabilityInstallGrant、平台检查和允许作用域。 |

## CapabilityInstallGrant v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `grant_id` | string | yes | 安装授权 id。 |
| `user_id` | string | yes | 授权用户。 |
| `workspace_id` | string | conditional | 工作区授权范围。 |
| `skill_id` | string | yes | 待安装 Skill。 |
| `version_constraint` | string | yes | 允许安装的版本范围。 |
| `granted_via` | enum | yes | `ui_action`、`assistant_turn`、`admin_policy`。 |
| `evidence_ref` | object | yes | 按钮操作、Assistant Turn 或管理策略引用。 |
| `permissions_snapshot` | object | yes | 用户确认时的权限声明。 |
| `expires_at` | timestamp | optional | 授权过期时间。 |

不变量：

- `marketplace` 和 `user_repository` Skill 安装前 MUST 有有效 `CapabilityInstallGrant`。
- `preinstalled` 可不要求用户 grant。`evolution_generated` MUST 先通过人工内容/安全发布审核；审核后的 scope 内可不要求第二次安装 grant，但仍需平台来源、策略和权限检查。
- Assistant/Worker 只能调用 Skilling control plane 消费 grant，不能自行伪造或仅在容器内持久安装。

## ApprovalGrant v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `approval_grant_id` | string | yes | 操作授权 id。 |
| `worker_session_id` | string | yes | 授权所属 WorkerSession。 |
| `request_id` | string | yes | 原始结构化审批请求。 |
| `scope` | enum | yes | `once` 或 `worker_session`。 |
| `capability` | string | yes | 被授权能力或操作类型。 |
| `target` | object | yes | 仓库、外部平台、资源或生产目标。 |
| `constraints` | object | yes | 参数、路径、分支、网络、金额或其他限制。 |
| `granted_by` | string | yes | 授权用户或管理员。 |
| `evidence_ref` | object | yes | UI/飞书审批证据。 |
| `expires_at` | timestamp | yes | 有效期，不得晚于 WorkerSession absolute expiry。 |
| `consumed_at` | timestamp | conditional | once grant 消费时间。 |

不变量：

- `once` grant 成功匹配一次操作后 MUST 失效。
- `worker_session` grant 只对 capability、target 和 constraints 全部匹配的后续操作有效。
- 进入 `waiting_user` 时暂停 idle timeout；拒绝或过期后必须恢复、降级或终止原 WorkerRun，不能静默继续。

## RepositoryAccessGrant v1

RepositoryAccessGrant 是仓库级受限委托，不是单条 CLI 命令或长期平台身份。

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `grant_id` | string | yes | 稳定 Grant id。 |
| `tenant_id` | string | yes | Tenant 硬边界。 |
| `workspace_id` | string | yes | 发起工作的 Workspace。 |
| `worker_session_id` | string | yes | 被委托的 WorkerSession。 |
| `repository_ref` | object | yes | provider、owner/namespace、repository id 与 RepositoryCheckout 引用。 |
| `capabilities` | array | yes | `repository.read`、`repository.write`、`pull_request.write`、`ci.read` 的子集。 |
| `scope` | enum | yes | `once` 或 `worker_session`。 |
| `constraints` | object | yes | provider、network、可选 ref/operation 约束；不能扩大 provider 实际权限。 |
| `policy_revision` | string | yes | 授权时的组织/平台策略版本。 |
| `grant_revision` | integer | yes | 单调修订，用于撤权和 Token cache fencing。 |
| `issued_by` | object | yes | 用户、组织策略及证据引用。 |
| `status` | enum | yes | `active`、`consumed`、`expired`、`revoked`。 |
| `expires_at` | timestamp | yes | 不得晚于 WorkerSession absolute expiry。 |
| `revoked_at` | timestamp | optional | 撤销时间。 |

不变量：

- WorkspaceRole、TaskSpec、Skill 或 Agent prompt MUST NOT 代替 RepositoryAccessGrant。
- `once` Grant 在第一个匹配的外部操作进入已接受状态后 MUST 被原子消费；`worker_session` Grant 在 repository/capability/constraints/expiry 全部匹配时可连续使用，不逐命令审批。
- P0 capability MUST NOT 隐含 administration、secrets、variables、workflow/Actions write、ruleset management、repository delete 或组织管理。
- Grant 撤销、Membership/Session 失效或 policy revision 不再兼容时，CredentialBroker MUST 停止续签并 fencing 新操作。
- A、受管 B2 与混合 C profile MUST 复用该 Grant；profile 只改变操作与凭证在哪一侧执行。

## RepositoryCredentialLease v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `lease_id` | string | yes | 短期凭证租约 id。 |
| `grant_id` | string | yes | 当前 RepositoryAccessGrant。 |
| `provider` | string | yes | `github` 或后续 provider。 |
| `credential_subject` | object | yes | installation/user/service subject，不包含根凭证。 |
| `repository_ref` | object | yes | 单仓范围。 |
| `permission_profile` | string | yes | 已解析 provider permission profile。 |
| `runtime_generation` | integer | yes | 只对当前 RuntimeInstance generation 有效。 |
| `provider_token_id` | string | optional | Provider 返回的 token/authorization id。 |
| `token_fingerprint` | string | yes | 用于检测、撤销和确定性剔除，不是 Token。 |
| `expires_at` | timestamp | yes | 短期 Token 到期时间。 |
| `secret_delivery_ref` | object | yes | 进程级交付引用；领域事件和 Trace 不携带 Token 内容。 |

不变量：

- 短期 access token MAY 对 Agent 可用；该契约不声称在 sandbox 内对 Agent 保密。
- GitHub App 私钥、OAuth refresh token、长期 PAT 或根凭证 MUST NOT 进入 Worker、Trace、Artifact 或 Agent state。
- Git credential lookup MUST 包含 repository path；managed `gh` MUST NOT 依赖持久化 `gh auth login`。
- runtime generation 变化时 MUST 重新签发 lease；旧 generation 的 lease MUST 被 fencing。
- 平台已知 Token MUST 在 Trace、Langfuse、Artifact、用户进度和持久 CLI 输出前确定性剔除。

## CodePlatformOperation v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `operation_id` | string | yes | 外部操作事实 id。 |
| `tenant_id` | string | yes | Tenant。 |
| `actor_user_id` | string | yes | Worker 代表的用户。 |
| `worker_session_id` | string | yes | 执行会话。 |
| `repository_ref` | object | yes | 目标 Repository。 |
| `grant_snapshot` | object | yes | grant id/revision、capability、permission profile 和约束摘要。 |
| `command_family` | enum | yes | `git`、`gh`、`glab` 或 `control_plane_adapter`。 |
| `operation_kind` | string | yes | `fetch`、`push`、`pr_create`、`ci_read` 等标准化类别。 |
| `target_refs` | object | optional | branch/tag/head/base/commit/remote 等目标。 |
| `status` | enum | yes | `started`、`succeeded`、`failed`、`unknown_effect`、`reconciled`。 |
| `provider_result` | object | optional | provider request/object id、URL、remote head 和标准化结果，不含 secret。 |
| `reconciliation` | object | optional | 查询证据、expected/observed state 和最终结论。 |
| `redaction_status` | enum | yes | `clean`、`secret_redacted`、`quarantined`。 |
| `correlation_id` | string | yes | 端到端关联。 |
| `started_at` | timestamp | yes | 开始时间。 |
| `completed_at` | timestamp | optional | 收敛时间。 |

不变量：

- Provider 成功是外部事实；Switchback 投影或响应失败 MUST NOT 导致同一写操作被盲重放。
- push 失败或响应丢失后 MUST 查询 remote ref/head；PR create 不确定时 MUST 按 head/base/provider object 查询后再决定补偿或重试。
- 无法分类或核查的外部写入 MUST 进入 `unknown_effect`/`waiting_user`，不得自动重试。
- Operation/Trace MUST NOT 保存 access token、refresh token、App private key 或带凭证 remote URL。

## LLMProxy Metadata v1

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `tenant_id` | string | yes | 租户。 |
| `user_id` | string | conditional | 用户。 |
| `task_id` | string | conditional | Worker/Task 调用关联。 |
| `conversation_id` | string | conditional | Assistant 调用关联。 |
| `worker_run_id` | string | conditional | WorkerRun 关联。 |
| `trace_id` | string | yes | 链路追踪。 |
| `route_policy_id` | string | yes | 模型路由策略。 |
| `purpose` | enum | yes | `clarify`、`plan`、`code`、`review`、`summarize`、`classify`、`evolve`。 |
| `safety_tags` | array | yes | 脱敏和审计标签。 |
| `budget` | object | yes | 模型请求的 token、cost 与 rate 限制；不得包含或改变 AgentTurn 执行时长。 |
