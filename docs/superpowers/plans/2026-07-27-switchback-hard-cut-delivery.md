# Switchback Hard-Cut Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Commit the current capability baseline atomically, replace every current product identity with Switchback without compatibility aliases, rebuild the local Compose environment from empty state, and capture phase-acceptance evidence.

**Architecture:** Preserve the functional diff before the identity migration by committing it in capability-owned slices. Apply the hard cut in three layers—code/protocol, deployment/runtime resources, and documentation—then run the full repository gate before deleting pre-cut Compose state. Rebuild from empty volumes so persisted identities cannot silently cross the compatibility boundary.

**Tech Stack:** Go, PostgreSQL migrations, Docker Compose, TypeScript/React/Vite, shell, Python/LiteLLM callbacks, JSON Schema, OpenSpec 1.6.0, CodeGraph.

## Global Constraints

- The only current product identity is `Switchback`.
- The canonical Go module is `github.com/abietic/switchback`.
- Runtime configuration uses only `SWITCHBACK_*`; no fallback lookup is allowed.
- Product HTTP headers use only `X-Switchback-*`.
- Product protocol and schema identifiers use only the `switchback.*` namespace.
- Compose application resources use only the `switchback-*` namespace.
- The local checkout directory and Git history are not renamed.
- Existing PostgreSQL, Langfuse, container, network, volume, and Runtime state are not migrated.
- User changes are never reset, stashed, cleaned, or overwritten.
- `.codegraph/codegraph.db` and other derived index files are never committed.
- `openspec/changes/single-runtime-recovery/tasks.md` items `2.3`, `6.4`, and `9.10` remain unchecked until their missing acceptance evidence exists.

---

## Target file structure

### Renamed paths

- `cmd/switchback-backend/`: canonical Backend command
- `images/worker/litellm/switchback_callback.py`: Worker LiteLLM callback
- `images/worker/litellm/test_switchback_callback.py`: callback tests
- `spikes/worker-runtime/worker/litellm/switchback_callback.py`: spike callback
- `spikes/worker-runtime/worker/litellm/test_switchback_callback.py`: spike callback tests
- `web-demo/src/types/switchback.ts`: demo contracts
- `openspec/changes/refine-switchback-platform-design/`: renamed active design change
- `docs/adr/0037-switchback-owned-display-part-schema.md`: renamed ADR
- `docs/adr/0059-embed-m1-runtime-supervisor-in-switchback-backend.md`: renamed ADR

### New execution record

- `docs/status/status-2026-07-27-switchback-phase-acceptance.md`: commit inventory, repository gates, Compose resources, health checks, migration state, effective configuration, and unresolved acceptance gaps

### Existing files with shared ownership

- `Makefile`: CodeGraph targets, verification gates, image names, and Compose entrypoints
- `deploy/local/compose.yaml`: Assistant, trusted-network, recovery, and renamed local topology
- `internal/localproduct/runtime_runner.go`: trusted-network, recovery, control-resume, and settlement assembly
- `internal/contracts/v1/workercontrol.go`: recovery and resumable-control contracts
- `internal/gateway/http/handler.go`: Workbench and Assistant command surfaces
- `web/src/app/App.tsx`: Workbench and Assistant routes plus product copy

Shared files are staged by exact hunk for capability commits. The hard-cut commits may stage whole files after the capability baseline has been preserved.

---

### Task 1: Freeze and verify the capability baseline

**Files:**
- Inspect: all modified and untracked files from `git status --short`
- Inspect: `openspec/changes/*/tasks.md`
- Inspect: `migrations/000010_assistant_interaction.sql`
- Inspect: `migrations/000011_worker_display_timeline.sql`
- Inspect: `migrations/000012_trusted_full_network_profile.sql`
- Inspect: `migrations/000013_single_runtime_recovery.sql`
- Inspect: `migrations/000014_runtime_control_resume.sql`
- Inspect: `migrations/000015_worker_turn_settlement.sql`
- Inspect: `migrations/000016_turn_settlement_source_union.sql`
- Inspect: `migrations/000017_runtime_control_fence_snapshot.sql`
- Inspect: `migrations/000018_runtime_first_heartbeat_watchdog.sql`

**Interfaces:**
- Consumes: the current dirty working tree and approved design commit `1971da7`
- Produces: one verified, unstaged capability baseline with an explicit file-to-commit ownership map

- [ ] **Step 1: Capture the current identity without modifying it**

```bash
git status --short --branch
git diff --check
git diff --cached --name-only
git log -12 --oneline --decorate
```

Expected: branch `master`; no staged files; whitespace check passes; the design commit is present in history.

- [ ] **Step 2: Verify the complete dirty baseline**

```bash
env GOCACHE=/tmp/switchback-go-cache \
  SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58778/switchback_test?sslmode=disable' \
  make verify
```

Expected: exit `0`. This is the final capability-baseline verification before the identity cut.

- [ ] **Step 3: Confirm OpenSpec truthfulness**

```bash
npx --yes @fission-ai/openspec@1.6.0 list --json
npx --yes @fission-ai/openspec@1.6.0 validate --all --strict
```

Expected: strict validation passes; completed changes report their current task counts; incomplete recovery tasks remain incomplete.

- [ ] **Step 4: Inventory and stop the pre-cut Compose project without deleting data**

```bash
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml ps -a
docker volume ls --filter label=com.docker.compose.project=switchback-m1
docker network ls --filter label=com.docker.compose.project=switchback-m1
docker image inspect switchback-web:local switchback-backend:local switchback-worker:local
make local-down
```

Expected: exact pre-cut resource names and image IDs are captured; containers and project networks stop cleanly; named data volumes remain until Task 11.

---

### Task 2: Commit CodeGraph navigation

**Files:**
- Add: `.codegraph/.gitignore`
- Modify by hunk: `AGENTS.md`
- Modify by hunk: `Makefile`
- Add: `docs/agent-navigation.md`
- Add: `docs/superpowers/plans/2026-07-27-codegraph-agent-navigation.md`

**Interfaces:**
- Consumes: current CodeGraph index and repository navigation design
- Produces: `codegraph-init`, `codegraph-status`, and `codegraph-smoke` repository entrypoints without committing derived database files

- [ ] **Step 1: Stage only navigation artifacts**

```bash
git add .codegraph/.gitignore \
  docs/agent-navigation.md \
  docs/superpowers/plans/2026-07-27-codegraph-agent-navigation.md
git add -p AGENTS.md Makefile
```

Accept only CodeGraph/navigation hunks.

- [ ] **Step 2: Verify staged scope and behavior**

```bash
git diff --cached --check
git diff --cached --name-only
make codegraph-status
make codegraph-smoke
```

Expected: only the five owned paths are staged; status and smoke pass; no `.codegraph/*.db*`, PID, socket, or log is staged.

- [ ] **Step 3: Commit**

```bash
git commit -m "chore(agent): add CodeGraph navigation workflow"
```

---

### Task 3: Commit Worker Workbench interaction

**Files:**
- Modify/Add: `internal/artifacttrace/`
- Modify/Add: `internal/eventingprojection/`
- Modify/Add: `internal/frontier/`
- Modify by hunk: `internal/gateway/`
- Modify/Add: `web/src/api/`, `web/src/components/`, `web/src/state/`, `web/src/views/`, `web/src/workbench/`
- Modify/Add: `web/tests/`
- Add: `migrations/000011_worker_display_timeline.sql`
- Move: `openspec/changes/worker-workbench-interaction-hardening/` to `openspec/changes/archive/2026-07-26-worker-workbench-interaction-hardening/`
- Modify: `openspec/specs/worker-detail-interaction/spec.md`
- Modify/Add: Workbench-related architecture and operation documents

**Interfaces:**
- Consumes: existing WorkerDetail, ArtifactRef, Frontier snapshot/delta, and Task action contracts
- Produces: reviewable artifact content, worker timeline, safe markdown display, and archived Workbench contract

- [ ] **Step 1: Stage Workbench-owned files and exact shared hunks**

```bash
git add internal/artifacttrace internal/eventingprojection internal/frontier \
  web/src/components web/src/state web/src/views web/src/workbench web/tests \
  migrations/000011_worker_display_timeline.sql \
  openspec/specs/worker-detail-interaction/spec.md \
  openspec/changes/archive/2026-07-26-worker-workbench-interaction-hardening
git add -u openspec/changes/worker-workbench-interaction-hardening
git add -p internal/gateway web/src/api web/src/app/App.tsx web/src/app/router.ts \
  web/package.json web/package-lock.json web/playwright.config.ts
```

Accept only Artifact review, timeline, Workbench rendering, and archive hunks.

- [ ] **Step 2: Verify Workbench tests**

```bash
env GOCACHE=/tmp/switchback-go-cache go test ./internal/artifacttrace/... ./internal/eventingprojection/... ./internal/frontier/...
npm --prefix web test -- --run
npx --yes @fission-ai/openspec@1.6.0 validate --all --strict
git diff --cached --check
```

Expected: all commands exit `0`.

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(workbench): add artifact review and worker timelines"
```

---

### Task 4: Commit M2 Assistant dispatch

**Files:**
- Add: `migrations/000010_assistant_interaction.sql`
- Add: `api/schema/v1/assistant-*.schema.json`
- Add/Modify: `internal/contracts/v1/assistant.go`, fixtures, and contract tests
- Add: `internal/interaction/`
- Add: `internal/platform/config/assistant.go` and tests
- Add: `internal/localproduct/assistant_runner.go`
- Modify by hunk: `internal/localproduct/app.go`, `internal/localproduct/config.go`, tests
- Modify by hunk: `internal/gateway/`
- Add/Modify: `web/src/api/assistant*`, `web/src/views/AssistantConversation*`, app routing, and Assistant browser tests
- Add: `images/assistant-litellm/`
- Add/Modify: `test/acceptance/assistantfailurematrix/`, `scripts/run-m2-real-provider-acceptance`
- Modify: `openspec/changes/m2-assistant-task-dispatch-vertical-slice/tasks.md`
- Add: M2 architecture, audit, operations, and acceptance documents

**Interfaces:**
- Consumes: TaskSpec creation, Workspace resolution, LiteLLM model profiles, EventCenter projections
- Produces: durable Assistant conversation/readiness/creation-intent flow and task dispatch vertical slice

- [ ] **Step 1: Stage Assistant-owned artifacts and hunks**

```bash
git add migrations/000010_assistant_interaction.sql \
  api/schema/v1/assistant-conversation-command.schema.json \
  api/schema/v1/assistant-decision.schema.json \
  api/schema/v1/assistant-projection-event.schema.json \
  internal/interaction internal/platform/config/assistant.go \
  internal/platform/config/assistant_test.go internal/localproduct/assistant_runner.go \
  images/assistant-litellm test/acceptance/assistantfailurematrix \
  scripts/run-m2-real-provider-acceptance \
  openspec/changes/m2-assistant-task-dispatch-vertical-slice/tasks.md \
  docs/architecture/m2-assistant-task-dispatch.md \
  docs/architecture/m2-assistant-task-dispatch-manual-acceptance.md \
  docs/architecture/m2-assistant-task-dispatch-requirement-audit.md
git add -p internal/contracts/v1 internal/gateway internal/localproduct \
  deploy/local/compose.yaml web/src web/tests Makefile scripts/check-generated-protocol
```

Accept only Assistant contracts, configuration, assembly, routes, projections, UI, generated-protocol inventory, and Compose service hunks.

- [ ] **Step 2: Verify Assistant slice**

```bash
env GOCACHE=/tmp/switchback-go-cache go test ./internal/interaction/... ./internal/contracts/v1 ./internal/platform/config ./internal/localproduct ./internal/gateway/...
npm --prefix web test -- --run
./scripts/check-generated-protocol
npx --yes @fission-ai/openspec@1.6.0 validate m2-assistant-task-dispatch-vertical-slice --strict
git diff --cached --check
```

Expected: all commands exit `0`.

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(assistant): deliver M2 task dispatch vertical slice"
```

---

### Task 5: Commit trusted full-network profile

**Files:**
- Add: `migrations/000012_trusted_full_network_profile.sql`
- Add: `docs/adr/0066-trust-full-worker-network-in-single-user-local-profile.md`
- Add: `internal/platform/config/runtime_network_profiles.go` and tests
- Modify: `api/schema/v1/task-spec.schema.json`
- Modify/Add: TaskSpec network contracts and producer tests
- Modify: Docker provisioner and tests
- Modify by hunk: local product Runtime environment/runner/configuration
- Add: `scripts/run-network-profile-acceptance`
- Add: `test/acceptance/networkprofile/`
- Add: network operations and acceptance documents
- Modify: `openspec/changes/trusted-full-network-profile/tasks.md`

**Interfaces:**
- Consumes: server-owned Runtime profile registry and TaskSpec admission
- Produces: `trusted_full_network_v1` evidence from TaskSpec through RuntimeInstance, Docker configuration, Trace, and projection

- [ ] **Step 1: Stage network-profile artifacts and exact shared hunks**

```bash
git add migrations/000012_trusted_full_network_profile.sql \
  docs/adr/0066-trust-full-worker-network-in-single-user-local-profile.md \
  internal/platform/config/runtime_network_profiles.go \
  internal/platform/config/runtime_network_profiles_test.go \
  api/schema/v1/task-spec.schema.json scripts/run-network-profile-acceptance \
  test/acceptance/networkprofile \
  docs/architecture/m1-worker-network-operations.md \
  docs/architecture/m1-worker-network-manual-acceptance.md \
  openspec/changes/trusted-full-network-profile/tasks.md
git add -p internal/contracts/v1 internal/taskorchestration internal/workerruntime \
  internal/localproduct internal/architecture deploy/local/compose.yaml \
  REQUIREMTENTS.md TECHNICAL_DESIGN.md docs
```

Accept only network-profile contract, persistence, provisioning, projection, acceptance, and normative-document hunks.

- [ ] **Step 2: Verify network profile**

```bash
env GOCACHE=/tmp/switchback-go-cache go test ./internal/platform/config ./internal/contracts/v1 ./internal/taskorchestration/... ./internal/workerruntime/adapter/docker ./internal/localproduct ./internal/architecture
npx --yes @fission-ai/openspec@1.6.0 validate trusted-full-network-profile --strict
git diff --cached --check
```

Expected: all commands exit `0`.

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(runtime): enforce trusted full network profile"
```

---

### Task 6: Commit durable Runtime recovery, control, and settlement

**Files:**
- Add: `migrations/000013_single_runtime_recovery.sql`
- Add: `migrations/000014_runtime_control_resume.sql`
- Add: `migrations/000015_worker_turn_settlement.sql`
- Add: `migrations/000016_turn_settlement_source_union.sql`
- Add: `migrations/000017_runtime_control_fence_snapshot.sql`
- Add: `migrations/000018_runtime_first_heartbeat_watchdog.sql`
- Add/Modify: Runtime recovery/control/settlement application, domain, Postgres, gRPC, shim, and local-product files
- Add/Modify: scheduled-command and Outbox persistence
- Add/Modify: Runtime recovery acceptance scripts and tests
- Add/Modify: recovery contracts, operations, manual acceptance, OpenSpec design/spec/tasks, and implementation plans

**Interfaces:**
- Consumes: WorkerSession/WorkerRun/AgentTurn identities, RuntimeControl authority, ScheduledCommand, Outbox, Docker provisioner, ACP session resume
- Produces: one-attempt Runtime recovery, resumable control transport, exactly-once terminal settlement authority, startup adoption, and durable first-heartbeat watchdog

- [ ] **Step 1: Stage the remaining recovery-owned implementation**

```bash
git add migrations/000013_single_runtime_recovery.sql \
  migrations/000014_runtime_control_resume.sql \
  migrations/000015_worker_turn_settlement.sql \
  migrations/000016_turn_settlement_source_union.sql \
  migrations/000017_runtime_control_fence_snapshot.sql \
  migrations/000018_runtime_first_heartbeat_watchdog.sql \
  internal/workerruntime internal/localproduct internal/platform/postgres \
  internal/contracts/v1/workercontrol.go \
  internal/contracts/v1/workercontrol_resume_test.go \
  api/schema/v1/runtime-control-frame.schema.json \
  scripts/run-runtime-recovery-acceptance scripts/run-failure-acceptance \
  scripts/run-real-provider-acceptance test/acceptance/failurematrix \
  test/acceptance/realprovider \
  docs/architecture/m1-worker-runtime-recovery-operations.md \
  docs/architecture/m1-worker-runtime-recovery-manual-acceptance.md \
  openspec/changes/single-runtime-recovery \
  docs/superpowers/plans/2026-07-26-backend-deadline-source-union.md \
  docs/superpowers/plans/2026-07-26-release-guardrails.md \
  docs/superpowers/plans/2026-07-26-runtime-control-resume.md \
  docs/superpowers/plans/2026-07-26-single-runtime-recovery.md \
  docs/superpowers/plans/2026-07-27-fenced-terminal-evidence.md \
  docs/superpowers/plans/2026-07-27-first-heartbeat-watchdog.md \
  docs/superpowers/plans/2026-07-27-startup-deadline-lease-barrier.md
git add -p internal/architecture internal/eventingprojection internal/taskorchestration \
  deploy/local/compose.yaml Makefile REQUIREMTENTS.md TECHNICAL_DESIGN.md \
  docs/contracts/core-protocols.md docs/domains/task-worker-runtime.md
```

Accept only recovery, control-resume, settlement, deadline-source, watchdog, release-gate, and projection hunks.

- [ ] **Step 2: Check for unowned capability changes**

```bash
git status --short
git diff --check
git diff --cached --check
```

Expected: the staged diff is recovery-owned. Any remaining dirty files are classified before commit; no unknown file is silently absorbed.

- [ ] **Step 3: Verify Runtime recovery**

```bash
env GOCACHE=/tmp/switchback-go-cache \
  SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58778/switchback_test?sslmode=disable' \
  go test -tags=integration ./internal/workerruntime/... ./internal/localproduct ./internal/platform/postgres
env GOCACHE=/tmp/switchback-go-cache go test -race ./internal/workerruntime/... ./internal/localproduct
npx --yes @fission-ai/openspec@1.6.0 validate single-runtime-recovery --strict
git diff --cached --check
```

Expected: all commands exit `0`.

- [ ] **Step 4: Commit**

```bash
git commit -m "feat(runtime): make recovery and turn settlement durable"
```

This is one integrated commit because migrations `000013`–`000018` share Runtime authority, startup adoption, deadline, settlement, and watchdog invariants. Splitting the accumulated shared-file hunks would create unbuildable intermediate states.

---

### Task 7: Rename code and protocol identities

**Files:**
- Move: paths listed under “Renamed paths”
- Modify: `go.mod`, `go.sum`, nested `go.mod` files, all Go imports
- Modify: `api/schema/v1/`, `internal/contracts/v1/`, generated protocol files
- Modify: Python callbacks/configuration/tests
- Modify: TypeScript package names, imports, types, tests, and UI copy
- Modify: Runtime labels, environment names, header names, schema IDs, log messages, and fixtures

**Interfaces:**
- Consumes: the committed capability baseline
- Produces: code and protocol surfaces that recognize only Switchback identities

- [ ] **Step 1: Record the pre-change failure**

```bash
rg -n --hidden \
  -g '!.git/**' -g '!.codegraph/**' -g '!**/node_modules/**' \
  -g '!runtime-data/**' '(?i)h[e]lix|h[a]ilix|flux[ _-]?web'
```

Expected: exit `0` with matches, proving the hard-cut acceptance gate is RED.

- [ ] **Step 2: Rename code-bearing paths**

Move each code-bearing path listed under “Renamed paths” exactly once with
`git mv`, then verify imports and references resolve to the destination.

- [ ] **Step 3: Apply the canonical token mapping**

Use one NUL-safe tracked-file list and apply the canonical identity contract
from the approved design in longest-token-first order. Do not retain aliases.

Then run `gofmt` over changed Go files and regenerate artifacts through the repository-owned generation entrypoints.

- [ ] **Step 4: Verify code and protocol surfaces**

```bash
go list ./...
make fmt-check
make generated-protocol-check
make migration-check
npm --prefix web test -- --run
python3 -m unittest discover -s images/worker/litellm -p 'test_*.py'
git diff --check
```

Expected: all commands exit `0`.

- [ ] **Step 5: Commit**

```bash
git add cmd internal api web web-demo images spikes go.mod go.sum migrations test
git diff --cached --check
git commit -m "refactor!: rename runtime identifiers to Switchback"
```

---

### Task 8: Rename deployment and operational resources

**Files:**
- Modify: `deploy/local/compose.yaml`
- Modify: `deploy/local/*.sh`
- Modify: `images/*/Dockerfile`
- Modify: `Makefile`
- Modify: `.gitignore`
- Modify: `scripts/`
- Modify: acceptance harnesses and architecture tests

**Interfaces:**
- Consumes: Switchback-only code and protocol identities
- Produces: Switchback-only Compose, image, database, path, credential-mount, label, and acceptance configuration

- [ ] **Step 1: Update deployment identities**

Apply these exact classes of replacements:

```text
switchback-backend                 -> switchback-backend
switchback-worker                  -> switchback-worker
switchback-web                     -> switchback-web
switchback-m1-worker-control       -> switchback-m1-worker-control
switchback-data                    -> switchback-data
switchback                         -> switchback
/opt/switchback                    -> /opt/switchback
/var/lib/switchback                -> /var/lib/switchback
/run/switchback-*                  -> /run/switchback-*
SWITCHBACK_*                       -> SWITCHBACK_*
```

Do not rename third-party services such as PostgreSQL, Langfuse, ClickHouse, Redis, or MinIO unless their application-owned database/user/resource value contains the previous product identity.

- [ ] **Step 2: Verify Compose and image contracts**

```bash
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml config >/dev/null
env GOCACHE=/tmp/switchback-go-cache go test ./internal/architecture ./internal/workerruntime/adapter/docker
./scripts/check-secrets
git diff --check
```

Expected: all commands exit `0`; rendered Compose contains only Switchback application resources.

- [ ] **Step 3: Commit**

```bash
git add .gitignore Makefile deploy images scripts internal/architecture \
  internal/workerruntime/adapter/docker test
git diff --cached --check
git commit -m "build!: rename local deployment resources to Switchback"
```

---

### Task 9: Rewrite documentation and enforce zero legacy identity

**Files:**
- Modify: `AGENTS.md`, `CONTEXT.md`, `REQUIREMTENTS.md`, `TECHNICAL_DESIGN.md`
- Modify: `docs/**/*.md`
- Modify: `openspec/**/*.md`, `.openspec.yaml`, and specs
- Modify: examples, research notes, plans, and archived changes
- Modify: the approved design and this plan so current tracked text contains only Switchback

**Interfaces:**
- Consumes: final code/deployment names
- Produces: one coherent Switchback architecture and operational vocabulary

- [ ] **Step 1: Rewrite all tracked documentation**

Use the same canonical mapping as Task 7. Preserve domain terms and technical behavior; change product identity only. Update commands, paths, variables, service names, examples, and links so every instruction is executable against the renamed repository.

- [ ] **Step 2: Run the zero-name gate**

```bash
git grep -InE '(h[e]lix|h[a]ilix|flux[ _-]?web)' -- \
  ':!.git/**' ':!.codegraph/**' ':!runtime-data/**'
```

Expected: exit `1` with no output.

- [ ] **Step 3: Verify documentation contracts**

```bash
npx --yes @fission-ai/openspec@1.6.0 validate --all --strict
make codegraph-status
make codegraph-smoke
git diff --check
```

Expected: all commands exit `0`.

- [ ] **Step 4: Commit**

```bash
git add AGENTS.md CONTEXT.md REQUIREMTENTS.md TECHNICAL_DESIGN.md docs openspec
git diff --cached --check
git commit -m "docs: adopt Switchback as the sole product identity"
```

---

### Task 10: Run full verification

**Files:**
- Verify: entire repository

**Interfaces:**
- Consumes: all hard-cut commits
- Produces: fresh repository-gate evidence before any destructive Compose action

- [ ] **Step 1: Start an isolated Switchback test database**

```bash
docker run -d --rm --name switchback-test-postgres \
  -e POSTGRES_USER=switchback \
  -e POSTGRES_PASSWORD=switchback-test \
  -e POSTGRES_DB=switchback_test \
  -p 127.0.0.1:58779:5432 \
  postgres:16-alpine
until docker exec switchback-test-postgres \
  pg_isready -U switchback -d switchback_test >/dev/null 2>&1; do
  sleep 1
done
```

Expected: the isolated database accepts connections without using any previous product identity.

- [ ] **Step 2: Run the full gate**

```bash
env GOCACHE=/tmp/switchback-go-cache \
  SWITCHBACK_TEST_DATABASE_URL='postgres://switchback:switchback-test@127.0.0.1:58779/switchback_test?sslmode=disable' \
  make verify
```

Expected: exit `0`.

- [ ] **Step 3: Stop the isolated test database**

```bash
docker stop switchback-test-postgres
```

Expected: the `--rm` container is removed.

- [ ] **Step 4: Verify Git and identity state**

```bash
git status --short --branch
git log --oneline --decorate -15
git grep -InE '(h[e]lix|h[a]ilix|flux[ _-]?web)' -- \
  ':!.git/**' ':!.codegraph/**' ':!runtime-data/**'
```

Expected: only intentionally uncommitted local/derived files remain; commits are capability-scoped; legacy-name scan has no output and exits `1`.

---

### Task 11: Rebuild Compose from empty application state

**Files:**
- Modify locally but do not commit: `deploy/local/.env`
- Create locally: Switchback Docker images, containers, networks, volumes, PostgreSQL schema

**Interfaces:**
- Consumes: verified Switchback source and existing provider credential source files
- Produces: a fresh Switchback local deployment

- [ ] **Step 1: Reconfirm the old data volumes before deletion**

```bash
docker volume inspect \
  switchback-m1_postgres-data \
  switchback-m1_langfuse-clickhouse-data \
  switchback-m1_langfuse-clickhouse-logs \
  switchback-m1_langfuse-minio-data \
  switchback-m1_langfuse-redis-data
```

Expected: only the five named volumes recorded in Task 1 resolve. Inspect metadata only; do not read volume contents.

- [ ] **Step 2: Remove the exact old data volumes and images**

```bash
docker volume rm \
  switchback-m1_postgres-data \
  switchback-m1_langfuse-clickhouse-data \
  switchback-m1_langfuse-clickhouse-logs \
  switchback-m1_langfuse-minio-data \
  switchback-m1_langfuse-redis-data
docker image rm switchback-web:local switchback-backend:local switchback-worker:local
```

Expected: the exact old PostgreSQL/Langfuse volumes and application images are removed. This is destructive and intentionally discards their data.

- [ ] **Step 3: Preserve ignored host state outside the new runtime root**

```bash
test ! -e /private/tmp/switchback-local-env-pre-hard-cut
test ! -e .scratch/pre-switchback-runtime-20260727
mv deploy/local/.env /private/tmp/switchback-local-env-pre-hard-cut
if test -d .switchback; then
  mkdir -p .scratch
  mv .switchback .scratch/pre-switchback-runtime-20260727
fi
```

Expected: the old ignored environment file and host Runtime state are no longer active but remain locally recoverable.

- [ ] **Step 4: Regenerate local configuration**

```bash
make local-env
```

Expected: `deploy/local/.env` contains required `SWITCHBACK_*` keys without exposing their values in output.

- [ ] **Step 5: Build and start**

```bash
make local-up
```

Expected: images build successfully; migration job exits `0`; long-running services become healthy.

---

### Task 12: Execute phase acceptance and summarize progress

**Files:**
- Create: `docs/status/status-2026-07-27-switchback-phase-acceptance.md`

**Interfaces:**
- Consumes: running Compose environment and repository commit history
- Produces: auditable stage summary with evidence and explicit gaps

- [ ] **Step 1: Verify service health**

```bash
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml ps
curl --fail --silent http://127.0.0.1:8080/healthz
curl --fail --silent http://127.0.0.1:4173/ >/dev/null
```

Expected: Backend and frontend checks succeed; required services are healthy; one-shot migrations completed successfully.

- [ ] **Step 2: Verify database and effective Runtime configuration**

Use Compose service names from the rendered configuration:

```bash
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml \
  exec -T postgres \
  psql -U switchback -d switchback -Atc \
  'select version from switchback_schema_migrations order by version'
docker compose --env-file deploy/local/.env -f deploy/local/compose.yaml \
  exec -T switchback-backend env |
  awk -F= '/^SWITCHBACK_/{print $1}' |
  sort
```

Expected: every migration is present; only configuration key names—not values—are printed.

- [ ] **Step 3: Verify old resources are absent**

```bash
docker ps -a --format '{{.Names}}' | rg -i 'h[e]lix|h[a]ilix'
docker network ls --format '{{.Name}}' | rg -i 'h[e]lix|h[a]ilix'
docker volume ls --format '{{.Name}}' | rg -i 'h[e]lix|h[a]ilix'
```

Expected: each command exits `1` with no output.

- [ ] **Step 4: Write and verify the stage report**

The report records:

- Commit hash, subject, file count, and purpose for every new commit
- Implemented capability status for Workbench, M2 Assistant, network profile, Runtime recovery/control/settlement/watchdog
- `make verify` result and exact local timestamp
- Compose image IDs, service states, health endpoints, and migration range
- Acceptance actions executed versus intentionally not executed
- Remaining OpenSpec gaps, especially fixed-image recovery, preemption, fault acceptance, and terminal-settlement completeness

```bash
git diff --check -- docs/status/status-2026-07-27-switchback-phase-acceptance.md
git add docs/status/status-2026-07-27-switchback-phase-acceptance.md
git commit -m "docs: record Switchback phase acceptance"
```

---

## Plan self-review

- Every approved design requirement maps to Tasks 7–12.
- Existing dirty capabilities are preserved before mechanical rename in Tasks 2–6.
- Destructive Compose removal is ordered after the full repository gate.
- Configuration inspection prints key names only and does not expose credential values.
- Recovery task evidence remains truthful; incomplete OpenSpec tasks are not marked complete.
- The plan contains no unresolved implementation choice or compatibility fallback.
- Shared-file staging is explicit and must be reviewed before each commit.
