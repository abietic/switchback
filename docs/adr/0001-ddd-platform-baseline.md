# ADR 0001: DDD Platform Baseline

## Status

Accepted as draft baseline.

## Context

Switchback / Switchback 需要把公司内部中台能力、Coding Agent、Skill/MCP、飞书入口、模型代理、知识演进和异步交付统一到一个端到端需求交付平台中。

如果直接按技术组件拆服务，容易出现以下问题：

- Assistant、Worker、Gateway 职责混杂。
- EventCenter 被误用成业务事实源。
- Worker 与某个 Coding Agent 强绑定。
- Evolution 把未验证模型总结写成长期记忆。
- Delivery 绕过权限和 TaskSpec 直接触发执行。

## Decision

采用 DDD bounded context + 异步事件 + Task Saga 的基线设计。

核心主轴：

```text
Interaction -> Task Orchestration -> Worker Runtime -> Artifact/Trace -> Evolution/Knowledge
```

平台支撑：

```text
Gateway / Delivery / EventCenter / Frontier / LLMProxy / Skilling / Identity
```

P0 稳定四个协议：

```text
TaskSpec
IntegrationEvent
Trace JSONL
ArtifactRef
```

Worker 采用三层模型：

```text
Worker Runtime
ACP/CLI Adapter
Capability Injector
```

长期记忆采用三层模型：

```text
Trace/Evidence
Truth
Inspiration Resource Catalog
```

## Consequences

正向影响：

- Assistant 和 Worker 边界清晰。
- 可以先接一个 Agent，再扩展多个 Agent。
- 任务状态、事件、轨迹、产物可分别治理。
- Evolution 具备来源、审核、过期和权限边界。
- 飞书/Web/API 入口可以统一到 TaskSpec。

代价：

- 前期需要定义协议和状态机，不能只快速拼一个聊天机器人。
- Trace/Event/Artifact 会有重复字段，需要保持 schema 治理。
- Task Saga、Outbox、DLQ 会增加基础设施复杂度。
- ContextPack/Truth 需要单独设计，不能只接向量库。

## Follow-up

- 细化 `TaskSpec v1` JSON Schema。
- 细化 `IntegrationEvent v1` JSON Schema。
- 细化 `Trace JSONL v1` JSON Schema。
- 选择 P0 Agent adapter。
- 选择 P0 runtime provisioner。
- 为 OpenViking 强依赖和 LiteLLM P0 实现补充故障、升级与数据治理方案。
