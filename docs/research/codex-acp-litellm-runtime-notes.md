# Codex ACP And LiteLLM Runtime Notes

> 状态：v0.1。
>
> 日期：2026-07-09。
>
> 目的：记录 Switchback P0 Worker 通过 ACP 控制 Codex、并在容器内侧载 LiteLLM 的一手资料和设计影响。

## Sources

- [LiteLLM Proxy Docker quick start](https://docs.litellm.ai/docs/proxy/docker_quick_start)
- [LiteLLM Docker / Helm deployment](https://docs.litellm.ai/docs/proxy/deploy)
- [LiteLLM proxy config](https://docs.litellm.ai/docs/proxy/configs)
- [OpenAI Codex authentication](https://developers.openai.com/codex/auth)
- [OpenAI Codex custom model providers](https://developers.openai.com/codex/config-advanced)
- [OpenAI Codex configuration reference](https://developers.openai.com/codex/config-reference)
- [OpenAI Codex non-interactive mode](https://developers.openai.com/codex/noninteractive)
- [Agent Client Protocol introduction](https://agentclientprotocol.com/get-started/introduction)
- [Agent Client Protocol agents registry](https://agentclientprotocol.com/get-started/agents)
- [ACP adapter for Codex CLI](https://github.com/agentclientprotocol/codex-acp)

## Findings

LiteLLM Proxy supports a Docker-based deployment with a mounted `config.yaml`, provider credentials from environment variables, and an OpenAI-compatible `/chat/completions` interface. The config model uses public model aliases such as `gpt-4o` and routes them to concrete provider/model settings.

Codex supports custom model providers through `config.toml`. A provider can set `base_url`, `env_key`, static/env headers, and related model transport settings, which makes a local or sidecar LiteLLM endpoint a viable P0 model path.

Codex authentication has two distinct concerns:

- Codex product authentication: ChatGPT/API key/access-token login for the CLI or IDE.
- Model provider authentication: custom provider keys read from environment variables configured by `env_key`.

OpenAI's non-interactive guidance warns against exposing API keys as broad job-level environment variables when repository-controlled code can run in the same job. Switchback should prefer short-lived scoped secrets and inject them only into the controlled Codex/LiteLLM process boundary.

ACP is designed to decouple clients/editors from agents and supports local stdio as well as remote HTTP/WebSocket scenarios. The ACP registry lists Codex CLI via an adapter, and the `agentclientprotocol/codex-acp` repository describes a stdio ACP server that starts Codex App Server, translates ACP requests into Codex operations, and maps Codex events back to the client.

## Switchback Design Implications

- P0 Worker should not bare-launch `codex` and scrape terminal output as the primary control path.
- P0 Worker should act as ACP client/session supervisor and control Codex through a Codex ACP server inside the WorkerSession container.
- The Worker control logic owns `worker_session_id`, container lease, workspace, agent session state, Trace normalization, artifact upload, and EventCenter reporting.
- The WorkerSession container image should be built through Docker and include:
  - Worker-side runtime shim or agent supervisor
  - Codex / Codex ACP server
  - LiteLLM sidecar or co-located proxy process
  - mounted or generated Codex config pointing model provider traffic at LiteLLM
  - mounted credentials or environment variables from SecretLease
- `worker_session_id` should be fixed at first container creation and reused during crash recovery.
- WorkerSession maps to at most one active RuntimeInstance/container at a time; recovery increments runtime generation and records a new container id, so historical mapping is one-to-many.
- A WorkerSession may map to multiple explicit Codex/agent session ids when the user or adapter creates additional sessions. In fixed `codex-acp@1.1.2`, Codex collaborative/subagent work is exposed as ACP tool calls with child thread metadata, so Switchback should retain it as child execution spans/Trace rather than automatically inventing another ACP AgentSession.
- Worker uploads session state, JSONL trace, raw transcript, and artifacts to object storage through backend APIs rather than relying on local container files as durable state.

## Open Questions Kept

- Exact Dockerfile and process supervisor layout.
- P0 decision: LiteLLM runs as a co-process in the same container and listens on loopback; P1 may split it into a Kubernetes Pod sidecar.
- Pin and contract-test `github.com/coder/acp-go-sdk@v0.13.5`, `agentclientprotocol/codex-acp`, and bundled Codex versions; validate capability negotiation and Switchback TraceRecord event coverage.
- Secret injection and revocation mechanism for long-lived WorkerSession containers.
