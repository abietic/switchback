# Evolution / Inspiration Domain

## 1. 领域定位

Evolution 负责把 Assistant 和 Worker 的事实轨迹转化为可治理的长期上下文。候选、不可变修订、审核、审计和发布状态以 Switchback PostgreSQL 为权威；批准后的 active revision 投影到强依赖的 OpenViking，并由 Evolution 提供读取。Inspiration 是 P2 experimental 的可选外部知识发现能力，以独立代码仓库维护知识源 URL、摘要和意图辅助上下文；Assistant/Worker 不得依赖其存在。完整飞书文档仍由 Agent/Assistant 通过 lark-cli 和对应 Skill 自主读取。

核心原则：

- Trace/Evidence 是事实来源。
- Truth 是经过治理的长期断言。
- Instruction 和 Skill 是行为/可执行能力，不与简单 Memory 共用自动发布策略。
- OpenViking 是 active context serving projection，不是审批和修订事实源。
- Inspiration 是资源索引和上下文候选，不是无限 prompt 注入。
- 向量检索只是实现手段，不是领域模型。

## 2. 三层记忆模型

```text
Trace / Evidence
  - Assistant Turns
  - Worker Trace JSONL
  - Langfuse observation refs
  - raw transcript
  - artifacts
  - user feedback

Governed Context
  - MemoryTruth: preference / fact / entity / event / project_state
  - ProcedureTruth: reviewed procedure / constraint
  - InstructionPolicy: system prompt / behavior boundary / tool rule
  - SkillPackage: executable instruction / script / dependency / permission

Inspiration Resource Catalog
  - 飞书文档
  - 妙记
  - 需求文档
  - repo 文档
  - 历史交付记录
  - 业务知识库
```

## 3. 聚合与实体

| 类型 | 名称 | 说明 |
| --- | --- | --- |
| Aggregate Root | `TraceIngestionJob` | 一次轨迹消费任务。 |
| Aggregate Root | `ContextCandidate` | Memory、Instruction、Skill 候选的统一来源与风险信封。 |
| Aggregate Root | `MemoryTruth` | 简单长期记忆及其 active revision。 |
| Aggregate Root | `InstructionPolicy` | 会改变 Agent 行为、边界或系统提示的受控内容。 |
| Entity | `ContentRevision` | 不可变正文、parent、diff、hash、作者和来源。 |
| Entity | `ReviewTask` | 人工审核任务、reviewer、理由和决策。 |
| Entity | `OpenVikingProjection` | active revision 的 URI、snapshot、同步状态和错误。 |
| Value Object | `TruthPublicationDecision` | 模型建议与确定性策略共同产生的 auto publish/review/reject 决定。 |
| Aggregate Root | `KnowledgeSource` | 外部知识源，如飞书文档、妙记、repo。 |
| Aggregate Root | `InspirationRepository` | Inspiration 条目的版本化代码仓库。 |
| Entity | `InspirationEntry` | Git 中受审核的资源声明、摘要、scope、owner 和 intent。 |
| Projection | `ResourceRecord` | Git 声明与源系统观测编译出的运行时 freshness/ACL/index 记录。 |
| Value Object | `SourceRef` | trace/artifact/message/source hash 引用。 |
| Value Object | `ResourceCandidate` | Inspiration 返回的外部资料候选链接和简要内容。 |

## 4. Truth 生命周期

```text
candidate
-> active
-> deprecated
-> expired

candidate
-> needs_review
-> active | rejected

active
-> superseded
-> deprecated
```

简单 MemoryTruth 仅在 MemoryPublicationPolicy 白名单、低风险、非敏感、无冲突、scope 不扩大且 evidence 充分时允许自动发布。P0 白名单固定为：

- `explicit_preference`：用户明确表达的低风险选择倾向，仅限 user scope；不得授予权限、要求生产/外部操作或形成强制行为规则。
- `verified_descriptive_fact`：由权威来源或确定性运行验证支持的描述性事实。
- `entity_alias`：由用户原话或权威来源确认的实体别名、术语或名称映射。
- `ephemeral_project_state`：由权威来源支持的短期项目状态，必须设置 `refresh_after` 和 `expires_at`。

模型推断、摘要、置信度和重复行为只能生成候选或审核建议，不能单独满足自动发布 evidence。允许的证据类别是 `user_explicit_statement`、`authoritative_source` 和 `deterministic_runtime_verification`。必须进入 `needs_review` 或拒绝的情况：

- 组织级规则。
- 跨用户共享记忆。
- 权限、安全、合规相关 Truth。
- 会改变 Worker 行为的 procedural memory。
- 推断偏好、失败案例归纳和对用户/团队的主观评价。
- 生产、外部平台或不可逆副作用相关内容。
- 敏感信息，或 source ACL 与目标 scope 不一致。
- 任意 InstructionPolicy 新增或修改。
- 任意 SkillPackage 生成或修改。
- 低置信或与已有 Truth 冲突。
- 从失败案例中提炼的红线规则。

发布判断采用“两层门控”：

1. 模型根据 evidence、confidence、scope、conflict 和 risk 输出 `auto_publish`、`needs_review` 或 `reject` 建议及理由。
2. 确定性策略检查安全、生产、跨用户/工作区、凭证、低置信和冲突规则；策略只能收紧，不能被模型绕过。

## 5. Truth Schema 建议

```text
id
schema_version
kind: memory_truth|procedure_truth|instruction_policy|skill_candidate
tenant_id
scope_type: user|workspace|project|repo|org
scope_id
truth_type: explicit_preference|verified_descriptive_fact|entity_alias|ephemeral_project_state|inferred_preference|procedure|constraint|skill_hint
subject
content
structured_value_json
status: candidate|active|needs_review|rejected|deprecated|expired
confidence
risk_level
extractor_version
source_refs: trace_id/span_id/artifact_uri/message_id/source_hash
evidence_class: user_explicit_statement|authoritative_source|deterministic_runtime_verification|model_inference
permissions: visibility/acl_snapshot/pii_classification/policy_tags
lifecycle: created_at/valid_from/refresh_after/expires_at/last_verified_at
usage: last_used_at/use_count/last_retrieved_by
review: required/reviewer/decision/notes
conflict_group
supersedes
superseded_by
current_revision_id
projection: openviking_uri/target_revision/snapshot_id/status/error
```

## 6. Evolution Pipeline

```text
TraceReadyForEvolution
-> LoadTraceAndArtifacts
-> SegmentEvidence
-> ExtractContextCandidates
-> NormalizeAndClassify
-> ConflictDetection
-> ModelPublicationRecommendation
-> DeterministicPolicyGate
-> AutoApproveSimpleMemoryOrRequestReview
-> PersistApprovedRevisionAndOutbox
-> ProjectActiveRevisionToOpenViking
-> CreateProjectionSnapshot
-> EmitTruthPublished
```

设计要求：

- 抽取器输出必须包含 source refs。
- 自动发布必须记录 MemoryPublicationPolicy revision、命中的 allowlist rule、evidence class、scope/ACL 检查、conflict 检查和 TTL 结果。
- 模型建议不能把 `model_inference` 升格为自动发布证据，也不能把行为规则伪装成 `explicit_preference`。
- 同一 evidence 不能无限重复生成候选。
- 低置信候选不能自动影响 prompt。
- 失败任务可生成“风险候选”，但默认需要人工审核。
- 用户显式“记住/忘掉”可以走 hot path，但仍需要权限和范围判断。
- 每次发布决定都要保留模型版本、policy version、理由和人工 override 记录。
- 用户 Memory 看板的修改、停用、删除和恢复先产生新 ContentRevision/tombstone，再由 Outbox 更新 OpenViking；UI 不直接调用 OpenViking CRUD。
- OpenViking 发布失败时保持 `publish_pending` 并重试，不把未完成 projection 误报为 active。

## 7. Memory Governance Surfaces

Memory 治理对用户呈现两个独立读模型：

- `MemoryLibrary`：按 user/workspace/project scope 展示 active、disabled、expired MemoryTruth，包含类型、来源摘要、自动/人工发布、freshness/expiry、last used、current revision 和 OpenViking projection 状态。
- `ReviewInbox`：按 Memory、Procedure、Instruction、Skill lane 展示 pending ReviewTask，包含 candidate diff、完整 evidence refs、模型建议、确定性策略结果、冲突、risk tags、scope/ACL 变化、reviewer requirement 和 age。

写操作规则：

- user-scope Memory 由用户本人治理；workspace/project scope 需要对应 owner/reviewer authority。
- `edit` 创建新 ContentRevision；`disable` 移除 active projection；`delete` 写 tombstone；`restore` 从选定历史内容创建新的 active revision，均保留审计。
- batch reject/disable 允许，但每项都要独立鉴权、校验 expected revision 并记录结果。
- batch approve 只允许同 scope、同 policy revision、同低风险 Memory type 且无敏感、冲突或 scope 扩张的 ReviewTask。Procedure、Instruction、Skill、权限安全、跨 scope 和冲突内容只能逐条批准。
- ReviewBatchCommand 可以返回 `completed_with_errors`，必须列出每项 succeeded/failed/stale/forbidden，禁止静默部分成功或把失败项当已批准。
- P0 不设置阻塞式审核 SLA；ReviewInbox 显示 age、筛选和摘要提醒。pending 内容不进入 ContextPack，也不阻塞当前 Task/Worker。

## 8. Inspiration Resource Catalog

Inspiration 的声明权威来源是一个可由人工和模型共同演进的独立代码仓库。该能力降为 P2 experimental；激活时使用单资源 YAML `InspirationEntry`、`contexts/**/*.md` 和确定性生成的 `INSPIRATION_INDEX.md`。PostgreSQL `ResourceRecord` 保存高频 source version、ACL、freshness、同步错误和 index revision，不能反向成为第二个人工事实源。未部署、停用或空结果不得阻塞 ContextPack。

ResourceRecord 字段建议：

```text
resource_id
entry_revision
tenant_id
source_type: feishu_doc|meeting_note|repo_doc|requirement|artifact|wiki
source_uri
connector
owner
acl_snapshot
hash
version
title
summary_l0
overview_l1
content_refs_l2
intent_hints
workspace_context
tags
project_refs
chunk_policy
embedding_model
index_version
sync_state
freshness
citation_policy
created_at
updated_at
```

索引检索流程：

1. 按 tenant/user/project/repo scope 做权限过滤。
2. 按任务意图、标签、时间、资源类型做候选召回。
3. hybrid search + rerank。
4. 返回 ResourceCandidate，包括链接、摘要、source refs 和 dropped reasons。
5. 如果需要完整飞书文档，由 Assistant/Worker 中的 Agent 使用 lark-cli 和对应 Skill 读取。

同步与发布建议：

- 人工、同步器或模型只通过分支/PR 修改 InspirationEntry 或 context Markdown；受保护 main 和 CODEOWNERS 控制语义发布。
- source version、ACL、freshness 和短暂错误只更新 ResourceRecord，不制造 Git commit。
- 模型生成摘要、intent、scope、owner 或 source URI 的变化必须进入 PR 和 CODEOWNER 人工审核，P0 不做模型语义变更自动合并。
- Git merge 后 compiler 幂等生成 ResourceRecord 和 OpenViking L0/L1 projection；完整原文始终留在源系统。
- Git webhook 是低延迟 fast path，只验证来源、去重并提交 exact commit SHA sync request；编译在后台 owner 中执行。周期 reconciliation 比较 protected main head 与 last projected SHA，修复漏 webhook、停机和失败重试；连续 commit 可以收敛到最新 head。
- P2 正式实现前先以人工 manifest 试点，验证上下文追问减少、候选命中和用户实际打开/读取率；没有使用证据时保持 disabled，不建设完整 connector 或自动摘要流水线。

## 9. OpenViking 映射

OpenViking 的 context database / 文件系统范式是当前产品路径的强依赖 serving projection。官方实现支持 CRUD、分层检索、账户/用户隔离和显式 snapshot/restore，但没有发现满足 Switchback 的候选审批、逐条修订治理、用户 Memory 看板和完整内容审计，因此不能成为治理事实源。

- memory：批准后的 active MemoryTruth/ProcedureTruth projection。
- resource：已发布 Inspiration ResourceRecord 的 L0/L1 projection。
- skill：人工审核发布后的 Skill projection；SkillPackage 权威内容仍归 Skilling/Git/Artifact。
- hierarchical context：L0/L1/L2 summary/content。

Switchback 侧仍保留端口以隔离领域层并支持测试：

```text
GovernedContextRepository
ContextProjectionPort
ResourceIndexPort
TruthQueryPort
```

这些端口用于测试替身、schema 演进和故障隔离，不代表 P0/P1 同时支持其他 serving store。治理 authority 始终是 Evolution/Skilling 的 RDBMS 与版本化内容源。

## 10. 权限与遗忘

权限必须在检索服务层强制执行：

- Agent 不直接传 tenant filter。
- Evolution Truth 读取和 Inspiration 索引检索必须注入 tenant/project/user scope。
- 高敏资源可用独立 collection/store。
- resource ACL snapshot 需要随源系统同步。

遗忘策略：

- 用户偏好：长 TTL，但允许用户纠正/删除。
- 项目状态：短 TTL，需要 refresh_after。
- 外部文档：随 source hash/version 失效。
- 低置信推断：短 TTL，不能跨 scope 传播。
- 删除必须写 tombstone，并触发索引删除/重建。

保留策略：

- P0 Trace、ContentRevision、ReviewTask、发布决策和 OpenVikingProjection 默认长期保留，不配置自动物理删除；P2 Inspiration 激活后其 revision 使用同一保留原则。
- archived/cold 状态用于降低在线查询和存储成本；后端运营平台负责查询、归档、导出和受审计删除。
- “尽量不删”不能取消用户纠错、权限撤销和未来合规删除能力。
- P0 以 `tenant_id` 为硬边界，再按 WorkspaceMembership、user/project scope 和内容 ACL 收窄；跨 Tenant 候选、投影和检索必须 fail closed。

## 11. P0 验收

- 能消费一段 Trace JSONL。
- 能关联 Langfuse trace/session id 作为观测引用。
- 能生成带 source_refs 的 ContextCandidate 和不可变 ContentRevision。
- 能仅对四类白名单 MemoryTruth 在明确/权威/确定性证据、低风险、非敏感、无冲突、scope/ACL 不扩张和 TTL 规则全部通过时自动发布；模型推断无发布权，InstructionPolicy、SkillPackage、行为型/敏感/冲突内容强制人工审核。
- 能提供 Memory 看板查看、修改、停用、删除和恢复，并保留不可变审计。
- 能分别提供 MemoryLibrary 与 ReviewInbox；批量批准只接受同质低风险 Memory，所有批量操作返回逐项鉴权、revision 和结果状态。
- 能以 Outbox 幂等投影 active revision 到 OpenViking、创建 snapshot，并处理 publish_pending/retry。
- Assistant 能通过 Evolution Truth 读取能力加载 active Truth，并自行编译 ContextPack。
- Inspiration 能返回带权限、摘要和链接的 ResourceCandidate。
- 能按 tenant/user/project 做权限过滤。
- 能把 active Truth 投影到 OpenViking，并在 OpenViking 不可用时显式保持 publish_pending，而不是丢失 RDBMS 治理状态或误报已发布。
- 能记录 Truth 使用次数和 last_used_at。
