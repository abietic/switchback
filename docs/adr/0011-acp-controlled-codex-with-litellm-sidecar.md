# ACP-Controlled Codex With LiteLLM Co-Process

P0 Worker will control Codex through ACP inside a Docker-built WorkerSession container, instead of bare-launching Codex and scraping terminal output as the primary integration path. The container-side Go Worker runtime shim pins community-maintained `github.com/coder/acp-go-sdk@v0.13.5` as its ACP client and starts `@agentclientprotocol/codex-acp@v1.1.2` as a local stdio ACP server, with its bundled compatible Codex dependency. The image also carries workspace/session state mounts, Skill/MCP config, and a co-located LiteLLM process. Authentication is injected through environment variables or mounted configuration and should later be governed by SecretLease.

`worker_session_id` is fixed when the first container starts. At any moment it maps to at most one active RuntimeInstance/container, but crash recovery reuses the WorkerSession id with a new runtime generation and container id. A WorkerSession may map to multiple explicit Codex/Agent session ids when the user or adapter creates additional sessions. In fixed `codex-acp@1.1.2`, subagent work remains an ACP tool call with child-thread metadata and is recorded as AgentExecutionSpan/Trace unless a later adapter contract explicitly promotes it. Worker owns session state, JSONL/raw transcript normalization, object storage upload, ArtifactRef creation, and EventCenter progress reporting.

## Status

Accepted as draft baseline.

## Consequences

- Worker implementation focuses on ACP session supervision and event normalization, not terminal scraping.
- P0 keeps ACP local to the container: Worker runtime shim to `codex-acp` uses stdio, while the shim communicates with Switchback backend APIs over the network.
- LiteLLM is co-located in the P0 container to keep the runtime topology minimal. A later K8s runtime may split it into a Pod sidecar without changing model-governance contracts.
- LiteLLM remains the P0 model gateway implementation; TaskSpec and Trace expose Switchback model policy fields rather than LiteLLM-specific configuration.
- Crash recovery rebinds the existing `worker_session_id` to a new RuntimeInstance generation while preserving historical container mappings.
- P0 still needs a fixed-image Docker contract spike for exact dependency pinning, ACP capability negotiation, raw-frame compatibility, session resume/load, permission requests, event coverage, process failure, Dockerfile/process supervision, health checks, graceful drain, and background-process cleanup.
