## Why

Worker Runtime Spike 已经证明真实 Codex、ACP、Docker、LiteLLM、Trace/Artifact 和 Langfuse 的最小执行链可行，但 Switchback 仍缺少可持久、可幂等、可由用户直接继续交互的正式产品闭环。现在需要把 Spike 中验证过的机制提升为生产边界，并用一个单用户纵切验证 Task、WorkerSession、WorkerRun、实时展示和 report/patch 审查能够共同工作。

## What Changes

- 建立正式 Worker Runtime 执行核心，由 Backend 内 RuntimeSupervisor 通过端口创建 Docker Worker，Shim 通过 ACP 控制 Codex，并继续以 ObjectStore JSONL 为权威执行证据。
- 建立 PostgreSQL-backed lightweight Task、WorkerSession、WorkerRun、AgentTurn、RuntimeInstance、TaskTransition 和 transactional Outbox，提供幂等创建、串行执行和最小生命周期调度。
- 允许受信任的单用户调用者直接提交明确 TaskSpec，不要求先经过 Assistant 澄清；Gateway 为 exact ready TaskSpec 生成绑定 actor/digest/Workspace/policy 的 ExplicitTaskCreationIntent，TaskSpec validation、TaskReadinessAssessment、Workspace binding 和服务端 AgentProfile 仍然强制执行。
- 建立 Gateway command/query 与 Frontier projection/SSE 边界，提供 WorkerDetail 主交互面、实时进度、report/patch 展示以及对 exact live WorkerSession 的后续 chat、queue、interrupt 和 steer。
- 保持 Task 是轻量交付记录，WorkerSession 是主要工作面；阶段结果可 delivered，但 Session 在租期内继续可交互。
- 复用 Spike 已验证的 Codex、LiteLLM `1.93.0`、DeepSeek/Kimi profile、Trace/Artifact 和 Langfuse adapter 行为，但不直接复制 Spike 的 in-memory state、Harness API、测试 timeout 或 Compose credential ownership。
- 将 LiteLLM sidecar 对比保留为独立限时实验，不阻塞本纵切；本变更默认沿用已验证的 Worker 内 co-process。

## Capabilities

### New Capabilities

- `runtime-execution-core`: 正式 Backend RuntimeSupervisor、Docker RuntimeProvisioner、WorkerControl、ACP CodexAdapter、AgentProfile、Trace/Artifact upload、ObjectStore 与 Langfuse 投影的最小生产执行能力。
- `task-session-persistence`: PostgreSQL 中 Task、WorkerSession、WorkerRun、AgentTurn、RuntimeInstance、Outbox、幂等命令、Session 串行化、continuity quota 及最小 deadline/idle/absolute lifecycle。
- `worker-detail-interaction`: Gateway/Frontier 的 WorkerDetail snapshot 与 typed SSE、阶段进度、report/patch Artifact 审查，以及 exact WorkerSession 后续交互和 approval/waiting-user 命令。

### Modified Capabilities

- 无。`refine-switchback-platform-design` 仍是当前未归档的总体设计 change；本 change 新增可实施的 M1 capability slices，不在两个并行 change 之间建立隐式 MODIFIED delta。

## Impact

- 新增正式 Go backend/runtime/application/domain/infrastructure 代码与数据库 migration；具体包路径由现有 DDD 目录基线和 design artifact 固化。
- 新增 PostgreSQL、LocalFileObjectStore、Docker Engine、LiteLLM、Codex ACP 和 Langfuse 的本地单用户集成 profile。
- 新增 Gateway/Frontier API、SSE projection contract 和最小 WorkerDetail 前端；Frontend 不从 raw Trace 重建业务状态。
- 影响 TaskSpec、IntegrationEvent/Outbox、Trace JSONL、ArtifactRef、AgentProfile 和 WorkerControl 契约，但不改变已确认的 Assistant、Delivery、Evolution、Inspiration 所有权。
- M1 仍使用服务端固定 tenant/actor、loopback-only Web 和 Docker Desktop 开发环境；SSO/OIDC、动态 Membership/RBAC、多用户隔离和远端生产部署延后。
- Runtime recovery、HTTPS long-poll fallback、完整 Linux egress enforcement、PreviewProxy、managed SessionProcess、Skill Market、Delivery 和 Evolution 不属于首个纵切完成条件；它们必须使用后续 change 增量实现。
- 实施前需要把总体设计 change 与本 change 的 capability ownership 做一次显式关联；不得因总体 spec 尚未归档而放宽既有 ADR 和 AGENTS.md 约束。
