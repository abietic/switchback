# CodeGraph Agent Navigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Initialize CodeGraph for Switchback and give agents a stable, source-backed path from a development question to relevant code, call paths, impact analysis, and verification commands.

**Architecture:** CodeGraph remains a local development-time index under `.codegraph/`; it does not enter the Switchback runtime or CI. The repository exposes three Make targets as the command interface, while `AGENTS.md` and one concise navigation document define when to use the graph and how to verify its results against source and tests.

**Tech Stack:** CodeGraph CLI 1.4.1, GNU Make-compatible Makefile targets, Markdown, Git ignore rules.

## Global Constraints

- Do not upgrade or reinstall global CodeGraph.
- Do not modify Switchback business code or runtime behavior.
- Do not stage or commit implementation changes automatically.
- Preserve all pre-existing uncommitted changes; patch only the exact sections named below.
- Never commit `.codegraph/codegraph.db` or other local CodeGraph runtime files.
- Treat CodeGraph as a navigation aid; current source, tests, runtime configuration, and relevant active OpenSpec changes remain authoritative.
- Do not add CodeGraph to CI or `make verify`.

## File Structure

- Create: `.codegraph/.gitignore` — generated local-index ignore policy; all CodeGraph runtime files remain untracked.
- Modify: `Makefile` — stable `codegraph-init`, `codegraph-status`, and `codegraph-smoke` command interface.
- Modify: `AGENTS.md` — repository-specific CodeGraph-first tool-selection rules and the navigation-document link.
- Create: `docs/agent-navigation.md` — concise source map, source-of-truth order, graph query recipes, and verification matrix.
- Existing reference: `docs/superpowers/specs/2026-07-26-codegraph-agent-navigation-design.md` — approved scope and acceptance contract; do not modify during implementation.

---

### Task 1: Local Index and Makefile Command Interface

**Files:**
- Create: `.codegraph/.gitignore`
- Modify: `Makefile:1-4`
- Modify: `Makefile` after the `verify` target

**Interfaces:**
- Consumes: executable `codegraph` CLI 1.4.1 on `PATH`
- Produces: Make targets `codegraph-init`, `codegraph-status`, and `codegraph-smoke`
- Produces: local `.codegraph/codegraph.db`, ignored by `.codegraph/.gitignore`

- [x] **Step 1: Record the expected pre-implementation failure**

Run:

```bash
make codegraph-status
```

Expected: non-zero exit with `No rule to make target 'codegraph-status'`.

- [x] **Step 2: Add the Makefile interface**

Add the variable beside the existing tool variables:

```make
GO ?= go
OPENSPEC ?= npx --yes @fission-ai/openspec@1.6.0
CODEGRAPH ?= codegraph
```

Add `codegraph-init codegraph-status codegraph-smoke` to the existing `.PHONY` declaration.

Add these targets immediately after `verify`:

```make
codegraph-init:
	@if [ -d .codegraph ]; then \
		$(CODEGRAPH) sync .; \
	else \
		$(CODEGRAPH) init .; \
	fi

codegraph-status:
	$(CODEGRAPH) status .

codegraph-smoke: codegraph-status
	$(CODEGRAPH) explore "CreateTask Store.CreateTask internal/taskorchestration/application/create_task.go internal/taskorchestration/adapter/postgres/store.go StartWorkerRun internal/workerruntime/application/start_run.go internal/workerruntime/adapter/postgres/start_run.go"
```

- [x] **Step 3: Initialize the current working tree**

Run:

```bash
make codegraph-init
```

Expected: exit 0; on first use CodeGraph initializes the project, while later runs synchronize changed files. `.codegraph/codegraph.db` and `.codegraph/.gitignore` exist. The index may include uncommitted source because the current working tree is intentionally authoritative for agent work.

- [x] **Step 4: Verify the generated ignore policy**

The generated `.codegraph/.gitignore` must contain:

```gitignore
# CodeGraph data files — local to each machine, not for committing.
# Ignore everything in .codegraph/ except this file itself, so transient
# files (the database, daemon.pid, sockets, logs) never show up in git.
*
!.gitignore
```

Run:

```bash
git check-ignore -v .codegraph/codegraph.db
git status --short -- .codegraph
```

Expected: the first command identifies `.codegraph/.gitignore`; the second lists only `?? .codegraph/`, whose sole committable file is `.codegraph/.gitignore`.

- [x] **Step 5: Verify all three Make targets**

Run:

```bash
make codegraph-status
make codegraph-smoke
```

Expected: both exit 0; status does not say `Not initialized`; the smoke query returns source from `internal/taskorchestration` or `internal/workerruntime` plus relationship/blast-radius context.

- [x] **Step 6: Review Task 1 without staging**

Run:

```bash
git diff --check -- Makefile
git diff -- Makefile
git status --short -- Makefile .codegraph
```

Expected: no whitespace errors; only the planned Makefile additions and `.codegraph/.gitignore` are attributable to this task. Leave both files unstaged.

---

### Task 2: Repository Agent Rules and Navigation Document

**Files:**
- Modify: `AGENTS.md:5-14`
- Create: `docs/agent-navigation.md`

**Interfaces:**
- Consumes: Make targets from Task 1
- Produces: a discoverable CodeGraph-first rule in `AGENTS.md`
- Produces: the repository-relative navigation entrypoint `docs/agent-navigation.md`

- [x] **Step 1: Add the repository CodeGraph rules**

Insert this section between `默认协作方式` and `项目定位`:

```markdown
## CodeGraph 与 Agent 导航

- 仓库根目录存在 `.codegraph/` 时，理解或定位代码优先使用 CodeGraph，再按需使用 `rg` 或直接读取文件。
- 查询符号源码、调用路径、动态分派和 blast radius 时，先运行 `make codegraph-status`，再使用 MCP `codegraph_explore`；MCP 不可用时使用 `codegraph explore "<问题>"`。
- 索引缺失、损坏或明显落后于工作树时，运行 `make codegraph-init`，不要手工修改 `.codegraph/codegraph.db`。
- 精确文本、非代码文件、生成物或 CodeGraph 未覆盖的内容继续使用 `rg` 和直接读取。
- CodeGraph 结果只用于缩小阅读范围；架构、行为和完成度结论必须回到当前源码、测试、运行配置和相关 active OpenSpec change 核验。
- 新任务先阅读 [Agent 导航](docs/agent-navigation.md)，按问题类型选择源码入口和验证门禁。
```

- [x] **Step 2: Create the concise navigation document**

Create `docs/agent-navigation.md` with this content:

```markdown
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
```

- [x] **Step 3: Verify discoverability and repo-relative links**

Run:

```bash
rg -n "CodeGraph 与 Agent 导航|docs/agent-navigation.md|make codegraph-status|codegraph_explore" AGENTS.md docs/agent-navigation.md
test -f docs/agent-navigation.md
```

Expected: both commands exit 0; `AGENTS.md` contains the repo-relative navigation link and both files expose the stable Make targets.

- [x] **Step 4: Review Task 2 without staging**

Run:

```bash
git diff --check -- AGENTS.md docs/agent-navigation.md
git diff -- AGENTS.md
git diff --no-index -- /dev/null docs/agent-navigation.md
```

Expected: no whitespace errors; only the planned AGENTS section and the new navigation document are attributable to this task. `git diff --no-index` returns 1 because the new file differs from `/dev/null`; that return value is expected. Leave both files unstaged.

---

### Task 3: End-to-End CodeGraph Acceptance

**Files:**
- Verify only: `.codegraph/.gitignore`
- Verify only: `Makefile`
- Verify only: `AGENTS.md`
- Verify only: `docs/agent-navigation.md`

**Interfaces:**
- Consumes: local index and repository guidance from Tasks 1–2
- Produces: evidence that CodeGraph can answer three representative Switchback navigation questions

- [x] **Step 1: Rebuild and check the final index**

Run:

```bash
make codegraph-init
make codegraph-status
```

Expected: both exit 0; status reports an initialized, up-to-date project and non-zero file/node/edge counts.

- [x] **Step 2: Verify Task creation and WorkerRun navigation**

Run:

```bash
codegraph explore "CreateTask Store.CreateTask internal/taskorchestration/application/create_task.go internal/taskorchestration/adapter/postgres/store.go StartWorkerRun internal/workerruntime/application/start_run.go internal/workerruntime/adapter/postgres/start_run.go"
```

Expected: exit 0 and output containing current source from `internal/taskorchestration` or `internal/workerruntime`, with at least one relationship or blast-radius section.

- [x] **Step 3: Verify Runtime recovery navigation**

Run:

```bash
codegraph explore "Trace RuntimeHeartbeat through liveness detection, RuntimeRecoveryIncident creation, and runtime replacement. Include internal/workerruntime/application/runtime_recovery.go and relevant Postgres adapters."
```

Expected: exit 0 and output containing current `internal/workerruntime` source related to heartbeat, liveness, loss, incident, or recovery. If one broad query is incomplete, rerun it with the exact symbol named in the first result; do not change repository files.

- [x] **Step 4: Verify Frontier projection navigation**

Run:

```bash
codegraph explore "internal/eventingprojection/application/projector.go internal/eventingprojection/adapter/postgres/materializer.go internal/frontier/http/handler.go internal/frontier/sse/handler.go Projector Materializer Snapshot Handler ServeHTTP"
```

Expected: exit 0 and output containing current source from `internal/eventingprojection` or `internal/frontier`, with call-path or impact context.

- [x] **Step 5: Run scoped repository checks**

Run:

```bash
git check-ignore -v .codegraph/codegraph.db
git diff --check
make codegraph-smoke
```

Expected: the database is ignored by `.codegraph/.gitignore`; the diff has no whitespace errors; smoke exits 0. Full `make verify` is intentionally not required because no Go, TypeScript, schema, migration, image, or runtime behavior changes.

- [x] **Step 6: Confirm final scope and leave implementation uncommitted**

Run:

```bash
git status --short -- Makefile AGENTS.md .codegraph docs/agent-navigation.md
git diff -- Makefile AGENTS.md
```

Expected: the only task-scoped tracked changes are `Makefile`, `AGENTS.md`, `.codegraph/.gitignore`, and `docs/agent-navigation.md`; `.codegraph/codegraph.db` is absent from status. Do not stage or commit these implementation files.
