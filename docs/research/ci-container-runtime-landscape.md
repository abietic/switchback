# CI/CD Container Runtime Notes

> 状态：v0.1。
>
> 日期：2026-07-08。
>
> 目的：为 Switchback P0 本地容器 Worker Runtime 选型提供参考。本文只抽取会影响设计的 CI/CD container runner 模式。

## Sources

- [GitHub Actions: Running jobs in a container](https://docs.github.com/actions/using-jobs/running-jobs-in-a-container)
- [GitHub Actions: Customizing containers used by jobs](https://docs.github.com/actions/hosting-your-own-runners/customizing-the-containers-used-by-jobs)
- [GitLab Runner Docker executor](https://docs.gitlab.com/runner/executors/docker/)
- [GitLab CI: Run CI/CD jobs in Docker containers](https://docs.gitlab.com/ci/docker/using_docker_images/)
- [Jenkins Pipeline: Using Docker with Pipeline](https://www.jenkins.io/doc/book/pipeline/docker/)
- [Codex ACP and LiteLLM runtime notes](codex-acp-litellm-runtime-notes.md)

## Common Pattern

主流 CI/CD 的容器执行模型基本一致：

1. Runner/agent 运行在宿主机或受控节点上。
2. 每个 job 或 stage 指定执行镜像。
3. Runner 通过 Docker 或兼容 runtime 创建容器。
4. 工作区、缓存、凭证、环境变量和服务依赖由 Runner 注入。
5. job 完成后容器被清理，日志、产物、退出码回传给 CI 系统。

对 Switchback 的直接启发：

- Worker service 应像 CI runner，而不是把 Coding Agent 直接跑在宿主机。
- WorkerSession container 应从 AgentProfile 指定镜像启动。
- Workspace、Agent session state、skills、MCP config、LLMProxy env 通过挂载或初始化步骤注入。
- Trace JSONL、raw transcript、artifact refs 和 exit status 从容器侧回传给后端。
- P0 本地实现可以先用 Docker Engine 或兼容 Docker API；后续再抽象到 K8s/internal sandbox。

## P0 Runtime Shape

```text
Switchback Worker Service
  -> RuntimeProvisioner(Docker-compatible)
  -> User-scoped WorkerSession container
  -> Worker runtime shim
  -> Codex ACP server
  -> LiteLLM co-process on loopback
  -> CodexAgentAdapter(ACP client)
  -> Trace/Langfuse exporter
  -> Artifact uploader
```

P0 建议：

- `RuntimeProvisioner` 抽象接口先按 Docker executor 模型设计。
- 本地开发实现用 Docker-compatible runtime 创建容器。
- 不依赖宿主机工作目录直接执行 Codex；所有 Agent 动作发生在容器内。
- Worker 通过 ACP 控制 Codex，不裸启动 Codex 并解析终端输出。
- Codex 模型请求指向同容器 LiteLLM co-process；P1/K8s 再拆 Pod sidecar。
- 每个 WorkerSession 有独立持久 WorkingCopy volume、agent state volume 和 network namespace；共享 Workspace 只通过 ChangeSet/WorkspaceApply 推进 revision。
- WorkerSession 同时只绑定一个 active RuntimeInstance/container；恢复递增 generation，历史 RuntimeInstance 与 Codex/Agent session id 一对多。
- 并发上限由 Worker scheduler 控制为 10。
- WorkerSession idle timeout 为 48 小时，最大存活时间为 14 天。

## Runtime Responsibilities

| Concern | P0 Decision |
| --- | --- |
| Image | Docker-built WorkerSession image from AgentProfile. |
| Workspace | Per WorkerSession isolated WorkingCopy volume derived from a WorkspaceRevision. |
| Agent state | Worker-managed per WorkerSession state volume for ACP/Codex continued interaction. |
| Network | Container talks to backend over network API. |
| Secrets | Short-lived env/file mounts, later hardened by SecretLease. |
| Model proxy | P0 LiteLLM co-process on loopback; P1 Pod sidecar. |
| Trace | Full-fidelity local session JSONL plus Langfuse export. |
| Cleanup | Idle timeout, absolute 14-day timeout, explicit cancel, failure cleanup. |
| Concurrency | Max 10 active WorkerSessions per user unless policy overrides. |

## Local Observation

On 2026-07-08 in this workspace:

- `codex` exists locally as `codex-cli 0.142.5`.
- No usable `docker` command was found from the current shell.

This means the design can target Docker-compatible local runtime, but actual local execution still needs Docker/Podman/Colima/Rancher Desktop or an internal equivalent installed and configured.
