# Switchback Agent 导航

本文档只回答三个问题：从哪里开始读、如何用 CodeGraph 缩小范围、最后运行什么验证。它不是第二份架构设计；文档与实现冲突时，以当前源码、测试和运行配置为准。

## 证据优先级

1. 当前用户任务、验收标准和明确约束。
2. 当前工作树中的源码、测试、schema、migration 和运行配置。
3. 与任务直接相关的 active OpenSpec change。
4. `REQUIREMTENTS.md`、`TECHNICAL_DESIGN.md`、`CONTEXT.md`、ADR 和 `docs/architecture/`。
5. archived OpenSpec、spike 和背景 explainer；这些只能解释历史，不能证明当前实现完成。

不要用文档中的未来时设计覆盖当前源码，也不要把 migration inventory 或任务勾选状态当作运行时证据。

## 问题到源码入口

| 问题 | 首要源码入口 | 常用补充证据 |
| --- | --- | --- |
| 本地产品如何组装和启动 | `cmd/switchback-backend/`, `internal/localproduct/`, `internal/platform/config/` | `deploy/local/compose.yaml`, `internal/architecture/` |
| Assistant 对话、readiness 与派发 | `internal/interaction/`, `internal/contracts/v1/assistant.go` | `api/schema/v1/assistant-*.schema.json`, `openspec/changes/m2-assistant-task-dispatch-vertical-slice/` |
| Task 创建、状态与用户动作 | `internal/taskorchestration/` | `internal/contracts/v1/taskspec.go`, `migrations/000001_task_session_persistence.sql` |
| WorkerSession、WorkerRun、恢复与 ACP | `internal/workerruntime/`, `cmd/worker-shim/` | `images/worker/`, `openspec/changes/single-runtime-recovery/` |
| Trace、Artifact 与评审 | `internal/artifacttrace/` | `api/schema/v1/trace-manifest.schema.json`, `api/schema/v1/artifact-ref.schema.json` |
| Event 投影、Frontier 与 SSE | `internal/eventingprojection/`, `internal/frontier/` | `web/src/state/`, `web/src/views/` |
| Gateway 命令入口 | `internal/gateway/http/`, `internal/gateway/adapter/postgres/` | `internal/localproduct/app.go` |
| 协议、schema 与 migration 一致性 | `internal/contracts/v1/`, `api/schema/v1/`, `migrations/` | `scripts/check-generated-protocol`, `scripts/check-migrations` |

## CodeGraph 工作流

先确认索引：

```bash
make codegraph-status
```

索引缺失或落后时刷新：

```bash
make codegraph-init
```

优先用 MCP `codegraph_explore`；不可用时使用 shell：

```bash
codegraph explore "CreateTask Store.CreateTask internal/taskorchestration/application/create_task.go internal/taskorchestration/adapter/postgres/store.go StartWorkerRun internal/workerruntime/application/start_run.go internal/workerruntime/adapter/postgres/start_run.go"

codegraph explore "Trace RuntimeHeartbeat through liveness detection, RuntimeRecoveryIncident creation, and runtime replacement. Include internal/workerruntime/application/runtime_recovery.go and relevant Postgres adapters."

codegraph explore "internal/eventingprojection/application/projector.go internal/eventingprojection/adapter/postgres/materializer.go internal/frontier/http/handler.go internal/frontier/sse/handler.go Projector Materializer Snapshot Handler ServeHTTP"
```

查询结果列出候选符号后，读取其当前源码和测试。精确字符串、Markdown、YAML、JSON、SQL、生成物或索引未覆盖内容使用 `rg`。

## 验证入口

| 改动范围 | 最小验证 |
| --- | --- |
| CodeGraph/Agent 文档 | `make codegraph-status && make codegraph-smoke && git diff --check` |
| Go 格式和短测试 | `make fmt-check && make unit` |
| 协议或 migration | `make generated-protocol-check && make migration-check` |
| OpenSpec | `make openspec-check` |
| 完整本地门禁 | `make verify` |

完整门禁可能依赖 Docker、PostgreSQL 或本地凭据。失败时区分代码失败、环境缺失和外部服务失败，不得把后两者伪报为实现缺陷或通过。
