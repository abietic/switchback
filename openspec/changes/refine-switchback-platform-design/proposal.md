## Why

Switchback / Switchback 目前只有口述级项目理解和初版设计草稿，关键能力边界、协议契约、状态机和验收标准还不够稳定，后续容易在 Assistant、Worker、Evolution、Delivery、EventCenter、LLMProxy 等模块之间发生职责漂移。

本变更把现有描述沉淀为可评审的需求矩阵、DDD 领域词汇、OpenSpec 能力规格、协议契约和 Mermaid 架构图，作为后续需求交互、模块设计和代码落地的共同基线。

## What Changes

- 引入项目级 OpenSpec 工作流，所有关键能力变更先经过 proposal、design、specs、tasks。
- 补齐 Switchback 平台 P0/P1/P2 需求矩阵，并把需求映射到 bounded context、验收标准和未决问题。
- 建立 DDD `CONTEXT.md`，统一项目术语，减少模块讨论中的命名歧义。
- 新增架构图文档，覆盖系统上下文、容器视图、领域上下文图、Web/飞书链路、直接 Worker 交互、隔离 Workspace ChangeSet Apply、Evolution 闭环、状态机和概念数据模型。
- 新增协议契约文档，细化 TaskSpec、WorkspaceRevision/WorkingCopy/ChangeSet/WorkspaceApply、ConversationScope/ConversationParticipant、WorkspaceContextResolution/ConversationRollover/ConversationHandoff、WorkspaceChannelShareGrant/ChannelDisclosureDecision、DeliveryCardProjection、WorkerControlBootstrap/RuntimeControlEnvelope、IntegrationEvent、Trace JSONL、ArtifactRef、Governed Context、Skill/MCP Manifest、LLMProxy metadata 的字段、约束和版本策略。
- 新增项目级技能使用说明，记录 OpenSpec、Superpowers、Matt Pocock skills 和 Mermaid 文档技能的安装位置与使用边界。

## Capabilities

### New Capabilities

- `interaction-assistant`: user/channel scoped Assistant 对话、ConversationParticipant participant/controller、WorkspaceContextResolution/ConversationRollover/ConversationHandoff、上下文装配、TaskReadinessAssessment、三问 ClarificationCheckpoint、TaskSpec 生成、结果总结和 generation-scoped Turns 归档。
- `task-worker-runtime`: PostgreSQL lightweight Task/TaskTransition/Outbox 与 fenced ScheduledCommand、WorkerSession 主交互面、WorkerRun 返工阶段、WorkspaceRevision、隔离 WorkingCopy、ChangeSet/WorkspaceApply、ToolInvocationControl 风险分级取消、ResolvedToolPolicy 强制执行、RuntimeInstance generation、15s/45s/90s RuntimeHeartbeat、RuntimeRecoveryIncident 单次自动恢复、gRPC primary/HTTPS fallback WorkerControlChannel、Docker 隔离运行时、ACP-controlled Codex、Trace、Langfuse 和 report/patch Artifact。
- `knowledge-evolution`: Evolution RDBMS 治理、四类确定性 Memory 自动发布白名单、MemoryLibrary/ReviewInbox 双视图与受限批量审核、OpenViking active projection、Instruction/Skill 人工审核；Inspiration Git manifests/Markdown/generated index 保留为 capability-gated P2 experimental，不进入 P0/P1 关键路径。
- `platform-eventing-delivery`: Gateway、Delivery、EventCenter、Frontier、StreamOwner/StreamSequence、causation、TimelineCursor、gap/DLQ/replay、WorkspaceChannelShareGrant/ChannelDisclosureDecision、异步投影和用户可见回传。
- `model-and-capability-governance`: LLMProxy、模型路由、成本审计、Langfuse 观测关联、Skill/MCP/AgentProfile 版本与权限治理，以及 ToolCapabilityDeclaration、AdapterEvidence、ToolGovernancePolicy 和 ResolvedToolPolicy。

### Modified Capabilities

- 无。当前仓库还没有已归档 OpenSpec capability，本次是初始能力建模。

## Impact

- 影响文档与规范：`AGENTS.md`、`REQUIREMTENTS.md`、`TECHNICAL_DESIGN.md`、`CONTEXT.md`、`docs/**`、`openspec/**`、`.agents/skills/**`、`.codex/skills/**`、`skills-lock.json`。
- 不引入运行时代码、不修改生产配置、不要求远端 Git。
- 后续代码实现应以这些 capability specs、核心协议和 DDD context map 为验收基线。
