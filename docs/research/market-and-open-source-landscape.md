# Market And Open Source Landscape

> 调研日期：2026-07-07。
>
> 目标：从市面产品、开源 Coding Agent、协议、RAG/Memory 平台中提炼 Switchback / Switchback 可借鉴的架构能力，而不是做功能堆砌。

## 1. 一句话结论

市面上的端到端软件工程 Agent 正在收敛到同一个结构：

```text
需求入口 / Chat / Issue
-> 任务规格化
-> 云端隔离执行环境
-> Agent 工具循环
-> 可见轨迹和产物
-> PR / diff / report
-> 组织知识和自定义规则沉淀
```

Switchback 应该借鉴这个结构，但不要照搬单一产品。公司内部平台的核心差异在于：多入口、多租户权限、内部中台能力、飞书生态、Skill/MCP、模型网关、对象存储轨迹、Evolution/Truth。

## 2. 产品与平台

| 产品/项目 | 官方来源 | 核心能力 | 对 Switchback 的启发 | 不应照搬 |
| --- | --- | --- | --- | --- |
| OpenAI Codex Cloud | [docs](https://developers.openai.com/codex/cloud) | 云端后台/并行 Coding Agent，独立云环境，GitHub/PR 集成。 | Assistant/Worker 分离；同一个 Worker runtime 可由 Web、飞书、CLI、自动化入口调用。 | 不要把所有上下文塞 prompt；必须有 ContextPack、Trace、Truth 来源。 |
| GitHub Copilot cloud agent | [docs](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-cloud-agent) | GitHub Actions 环境，研究 repo、计划、改代码、分支、PR、usage metrics、custom agents、MCP。 | TaskSpec 对标高质量 issue；执行透明性来自 branch/commit/log/review。 | 不绑定 GitHub Actions；公司内部可能是 GitLab/内部 Git/自研 CI。 |
| Google Jules | [site](https://jules.google/) | GitHub repo/branch 选择、异步任务、并发任务、Gemini 模型。 | WorkerSession 是短生命周期隔离任务；产物应是 patch/branch/report。 | 不绑定 GitHub；不要跳过企业权限和审计。 |
| Devin | [site](https://devin.ai/) | 云端 AI software engineer，面向复杂多 repo，知识沉淀、自动化、进度界面。 | Worker UI 应暴露 shell/editor/browser/progress/可接管能力；Skill 应可版本化。 | 不把“全自动工程师”当核心卖点，先保证任务边界和验证。 |
| Replit Agent | [docs](https://docs.replit.com/references/agent/overview) | 自然语言构建应用、Design Canvas、部署、连接服务、checkpoint。 | 前端可借鉴任务看板、checkpoint、预览、回滚。 | Replit 偏原型生成，Switchback 面向企业研发交付，安全和代码审查更重要。 |
| Dify | [GitHub](https://github.com/langgenius/dify) | LLM app 平台、Workflow、RAG pipeline、Agent、模型管理、观测。 | Skilling/Inspiration 可借鉴工具市场、知识库 pipeline、模型 provider、观测集成。 | 可视化 workflow 不应替代 Coding Agent runtime。 |
| Flowise | [site](https://flowiseai.com/) | LLM workflow、Agentflow、队列、HITL、tracing。 | Delivery/EventCenter 可借鉴 flow state、HITL 和流式状态。 | 节点图不适合承载复杂代码探索和编辑。 |
| n8n | [docs](https://docs.n8n.io/) | 工作流自动化、触发器、AI Agent 节点、大量 SaaS 集成。 | Delivery 可以借鉴触发器和连接器，把入口统一转 TaskSpec。 | 集成层不能直接拥有生产写权限。 |
| Factory Droid | [site](https://www.factory.ai/) | Agent-native 开发平台、多入口、Missions、多步任务。 | 未来可有 Mission/Project 层做跨 Worker 任务拆分和汇总。 | 多 Agent 并行需要先解决冲突、隔离和责任归属。 |

## 3. 开源 Coding Agent 与 Worker Runtime

| 项目 | 官方来源 | 可借鉴能力 | Switchback 映射 |
| --- | --- | --- | --- |
| OpenHands | [repo](https://github.com/OpenHands/openhands), [SDK](https://github.com/OpenHands/software-agent-sdk) | 本地/ephemeral workspace，Agent Server，REST API，Docker/K8s 运行，事件式 runtime。 | Worker Runtime 和 Agent Server 设计参考。 |
| Cline | [repo](https://github.com/cline/cline) | CLI/SDK/IDE，Plan/Act，MCP，checkpoint，NDJSON/headless，ACP。 | Codex 之后的可选 Agent adapter 参考。 |
| Aider | [docs](https://aider.chat/docs/) | 终端 pair programming，git diff/commit，repo map，lint/test 自动修复。 | 轻量 CLIAdapter，适合 patch/report 任务。 |
| SWE-agent / mini-SWE-agent | [docs](https://swe-agent.com/latest/) | trajectory、ACI、issue -> fix、benchmark/eval。 | Trace JSONL 和 eval 参考，不建议直接作为 P0 核心依赖。 |
| Continue | [docs](https://docs.continue.dev/) | config.yaml、context provider、rules/prompts/docs/mcpServers、Plan Mode。 | Skill/MCP/Rules 配置形态参考。 |
| Roo Code | [docs](https://roocodeinc.github.io/Roo-Code/) | mode -> tool group -> permission、MCP、checkpoint。 | Worker 权限策略参考。 |

Worker 设计判断：

- 不绑定单一 Agent，先抽象 `Runtime + Adapter + Capability Injector`。
- 原生 transcript 保留为 raw artifact，Switchback 再转换成统一 Trace JSONL。
- Git diff、checkpoint、test log、PR link 都是 Artifact，不要埋在日志里。
- P0 已决定先接 Codex；Cline、OpenHands 和 Aider 作为后续 AgentProfile/AgentAdapter 扩展目标；SWE-agent 用来做 eval/trajectory 对照。

## 4. 协议与能力注入

| 协议/能力 | 官方来源 | 结论 |
| --- | --- | --- |
| ACP | [GitHub](https://github.com/agentclientprotocol/agent-client-protocol) | ACP 标准化 editor/client 与 Coding Agent 通信。Switchback 可把 Worker 作为 ACP client/session supervisor。 |
| MCP | [spec](https://modelcontextprotocol.io/specification/2025-06-18) | MCP 提供 Resources、Prompts、Tools。它是工具/资源接入协议，不是任务编排协议。 |
| AGENTS.md | [site](https://agents.md/) | 项目级 agent 指令的通用 Markdown 入口。Switchback 可生成/注入类似 rules，但不要让它替代权限策略。 |
| LiteLLM | [repo](https://github.com/BerriAI/litellm) | 统一 OpenAI 格式、多 provider、AI Gateway、成本/预算/日志。适合 P0 LLMProxy 基座。 |

设计取舍：

- TaskSpec 是 Switchback 内部的任务协议。
- ACP/CLI 是 Agent 会话协议。
- MCP 是能力接入协议。
- LLMProxy metadata 是模型治理协议。

这四者不能混成一个“大 JSON”。

## 5. Memory / Knowledge / RAG

| 项目 | 官方来源 | 可借鉴点 |
| --- | --- | --- |
| OpenViking | [repo](https://github.com/volcengine/OpenViking) | Context Database，文件系统范式统一 memory/resource/skill，适合 Truth/Inspiration。 |
| Letta | [memory docs](https://docs.letta.com/letta-agent/memory) | git-backed memory filesystem/context repository，可读、可版本化、可人工编辑。 |
| LangGraph memory | [docs](https://docs.langchain.com/oss/python/langchain/long-term-memory) | 短期 thread memory 和长期 namespace/key store 分离。 |
| Dify Knowledge | [docs](https://docs.dify.ai/en/cloud/use-dify/knowledge/readme) | 知识库 pipeline、外部知识库、metadata、检索测试、发布。 |
| OpenAI Agents tracing | [docs](https://github.com/openai/openai-agents-python/blob/main/docs/tracing.md) | agent run 中 LLM、tool、handoff、guardrail、自定义事件需要完整 tracing。 |

Switchback 结论：

- 长期记忆不是向量库，而是 `Trace/Evidence -> Truth -> ContextPack`。
- Inspiration 不是一个手工维护的大 Markdown 文件，而是可治理 Resource Catalog；`INSPIRATION_INDEX.md` 可以由 machine-readable manifests 确定性生成人工可读视图。
- Truth 需要 status、source refs、confidence、risk、TTL、permission、review。
- Context Compiler 负责预算和权限过滤，Assistant/Worker 不直接全库检索。

## 6. 架构风险

- 只看竞品 UI，不看底层隔离、轨迹和权限，会做成不可控 demo。
- 只接入一个 Coding Agent，会被其上下文、日志、工具语义绑定。
- 只做向量库记忆，会产生不可审计、不可过期、不可解释的长期幻觉。
- 只做飞书机器人，会丢失 Web/Worker/Knowledge 的平台化内核。
- 只做事件流，不做状态机和 Outbox，会在失败重试时丢状态或重复执行。
