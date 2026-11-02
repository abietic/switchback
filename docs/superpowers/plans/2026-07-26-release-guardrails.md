# Runtime Recovery Release Guardrails Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `tdd` to implement this plan task-by-task. Because this checkout already contains a large user-owned dirty worktree, do not create commits, stage files, or open another worktree unless the user explicitly requests it.

**Goal:** 在 Runtime recovery 的 Backend 重连与真实故障矩阵尚未闭环前，默认关闭自动恢复，并保证所有新 Task admission 只能绑定当前服务端 `trusted_full_network_v1`；历史 legacy TaskSpec 仍保持可读。

**Architecture:** 通用 `TaskSpec.Validate` 继续承担历史快照的结构解码，`CreateTaskService.Execute` 作为“创建新 Task”的应用层最终 admission seam，额外校验当前部署 profile 的 exact id/revision/digest。Compose 默认值作为发布安全闸门保持关闭，显式环境变量仍可用于受控验收。

**Tech Stack:** Go 1.x、PostgreSQL-backed application ports、Docker Compose、OpenSpec 1.6.0。

## Global Constraints

- `SingleUserLocalProfile` 当前服务端网络 profile 固定为 `trusted_full_network_v1` revision 1，并校验服务端 digest。
- `m1_public_web_restricted_v1` 仅用于不可变历史 TaskSpec 读取和已入场任务兼容执行，新 admission 必须拒绝。
- `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED` 在 fixed-image active-turn、Backend restart、failed fencing/replacement、stale-event 矩阵通过前默认 `0`。
- 不改写历史 TaskSpec JSON，不删除 recovery 数据，不回滚工作区已有改动。
- 测试只落在已确认 seam：`CreateTaskService.Execute`、Compose 架构契约，以及后续 Backend composed runtime ownership/reconnect。

---

### Task 1: Default-Off Recovery Release Gate

**Files:**

- Modify: `internal/architecture/local_compose_test.go`
- Modify: `deploy/local/compose.yaml`

**Interfaces:**

- Consumes: Compose 环境变量展开语义。
- Produces: `SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED` 未显式配置时为 `0`；显式配置 `1` 时仍可进行受控验收。

- [x] **Step 1: Write the failing architecture expectation**

```go
"SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED: ${SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED:-0}",
```

- [x] **Step 2: Run the focused test and confirm RED**

Run:

```bash
go test ./internal/architecture -run TestLocalComposeKeepsM1TopologyAndDockerOwnership -count=1
```

Expected: FAIL，指出 Compose 缺少默认 `-0` 的 invariant。

- [x] **Step 3: Change only the Compose default**

```yaml
SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED: ${SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED:-0}
```

- [x] **Step 4: Run the focused test and confirm GREEN**

Run:

```bash
go test ./internal/architecture -run TestLocalComposeKeepsM1TopologyAndDockerOwnership -count=1
```

Expected: PASS。

### Task 2: Final New-Task Network Admission

**Files:**

- Create: `internal/taskorchestration/application/create_task_network_test.go`
- Modify: `internal/taskorchestration/application/create_task.go`

**Interfaces:**

- Consumes: `config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)`.
- Produces: `CreateTaskService.Execute(ctx, command)` 在调用 `CreateTaskStore.CreateTask` 前拒绝 legacy、stale revision 和 stale digest。

- [x] **Step 1: Write one failing behavior test through `CreateTaskService.Execute`**

测试先由现有 `DirectAdmission` 生成一份合法 API TaskSpec，再把 `RuntimeConfig` 改成 legacy 值并重算 `CreationIntentRef.TaskSpecDigest`。使用 recording store 断言：

```go
_, err := service.Execute(context.Background(), command)
if err == nil {
    t.Fatal("non-current runtime network profile was accepted")
}
if store.called {
    t.Fatal("legacy TaskSpec reached CreateTaskStore")
}
```

同一测试表覆盖：

```go
contractsv1.RuntimeConfig{NetworkPolicy: config.LegacyPublicWebRestrictedV1}
contractsv1.RuntimeConfig{
    NetworkPolicy: current.ID,
    NetworkPolicyRevision: current.Revision + 1,
    NetworkPolicyDigest: current.Digest,
}
contractsv1.RuntimeConfig{
    NetworkPolicy: current.ID,
    NetworkPolicyRevision: current.Revision,
    NetworkPolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
}
```

- [x] **Step 2: Run the focused test and confirm RED**

Run:

```bash
go test ./internal/taskorchestration/application -run TestCreateTaskRejectsNonCurrentRuntimeNetworkProfile -count=1
```

Expected: 三种非当前配置均错误到达 store。

- [x] **Step 3: Add the final admission invariant**

在 `TaskSpec.Validate` 之后、生成任何 IDs 或调用 store 之前，使用当前 server registry 的 stored-evidence resolver 执行：

```go
runtimeConfig := command.TaskSpec.RuntimeConfig
if _, err := config.ProductionRuntimeNetworkProfiles().ResolveStored(
    config.RuntimeNetworkEvidence{
        ID: runtimeConfig.NetworkPolicy,
        Revision: runtimeConfig.NetworkPolicyRevision,
        Digest: runtimeConfig.NetworkPolicyDigest,
        EnforcementStatus: config.NetworkEnforcementActive,
    },
); err != nil {
    return CreatedTask{}, fmt.Errorf("admit Task runtime network policy: %w", err)
}
```

- [x] **Step 4: Run focused GREEN tests**

Run:

```bash
go test ./internal/taskorchestration/application -count=1
go test ./internal/contracts/v1 -run 'TestTaskSpec(AcceptsCurrent|ReadsLegacy)' -count=1
```

Expected: application tests PASS，且历史 legacy decode 仍 PASS。

### Task 3: Truthful Change Tracking And Verification

**Files:**

- Modify: `openspec/changes/trusted-full-network-profile/tasks.md`
- Modify: `openspec/changes/single-runtime-recovery/tasks.md`
- Modify: `docs/architecture/m1-post-iteration-manual-capability-acceptance.md`

**Interfaces:**

- Consumes: Task 1/2 的真实测试结果和后续 fixed-image evidence。
- Produces: OpenSpec/运维文档不把未通过的恢复矩阵、VPN reachability 或默认 enablement 表述为已完成。

- [x] **Step 1: Reopen any overclaimed checkbox before implementation**

`trusted-full-network-profile` 的 2.4 在最终 admission 测试为 RED 时暂时改为未完成；修复并验证后重新勾选。`single-runtime-recovery` 的 8.7 在默认开关仍为 `1` 或完整 conformance gate 未通过时保持未完成并附剩余证据说明。

- [x] **Step 2: Update manual acceptance gate wording**

明确区分：

```text
默认部署：SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=0
受控 recovery 验收：操作者显式设置 SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED=1
```

不得把 skipped `vpn_tcp` 或未执行的 active-turn/Backend restart/failed fencing/replacement/stale-event case 计为通过。

- [x] **Step 3: Run repository verification**

Run:

```bash
gofmt -w internal/taskorchestration/application/create_task.go \
  internal/taskorchestration/application/create_task_network_test.go
go test ./internal/taskorchestration/application ./internal/contracts/v1 ./internal/architecture -count=1
npx --yes @fission-ai/openspec@1.6.0 validate trusted-full-network-profile --strict
npx --yes @fission-ai/openspec@1.6.0 validate single-runtime-recovery --strict
make verify
```

Expected: focused tests、两项 strict validation 和 `make verify` 全部 PASS；若真实 Docker/fixed-image gate 未跑，则 recovery 保持默认关闭且 OpenSpec 剩余项不勾选。

### Task 4: Truthful Runtime-Recovery Acceptance Evidence

**Files:**

- Create: `internal/architecture/runtime_recovery_acceptance_test.go`
- Modify: `scripts/run-runtime-recovery-acceptance`
- Modify: `test/acceptance/realprovider/main.go`
- Modify: `test/acceptance/realprovider/main_test.go`

**Interfaces:**

- Consumes: 实际通过的 PostgreSQL integration test 名称，以及 fixed-image harness 真正执行的 recovery scenario。
- Produces: summary 明确区分“本次 evidence collection 成功”和“完整 enablement matrix 已满足”；当前手工 generation replacement 只能声明 `idle_between_turns_manual_replacement`，不能声明 production coordinator、Backend restart 或 active-turn continuity。

- [x] **Step 1: Add failing truthfulness tests**

测试要求：

- fixed-image recovery summary 暴露 exact scenario 和 `production_recovery_path=false`；
- 顶层 acceptance summary 包含实际通过的 test 名称、required/missing scenario 集合与 `enablement_ready=false`；
- 脚本不再输出 `idle_and_active_loss`、`backend_restart_persistence` 等未实际执行的硬编码 assertion。

- [x] **Step 2: Run focused RED tests**

Run:

```bash
go test ./internal/architecture ./test/acceptance/realprovider -run 'Test(RuntimeRecoveryAcceptance|RecoveryEvidence)' -count=1
```

Expected: FAIL，指出当前脚本和 summary 仍过度声明。

- [x] **Step 3: Emit exact executed evidence**

将 integration suite 改为 `go test -json` 并从成功事件提取实际 test 名称；在 fixed-image profile 中写入 exact recovery scenario。顶层 summary 保留 `outcome=passed` 表示本次已执行范围通过，同时计算：

```json
{
  "required_enablement_scenarios": [],
  "missing_enablement_scenarios": [],
  "enablement_ready": false
}
```

required 集合固定来自 OpenSpec 8.6；只有所有场景都有 production/fixed-image evidence 时才允许 `enablement_ready=true`。

- [x] **Step 4: Run focused GREEN tests**

Run:

```bash
go test ./internal/architecture ./test/acceptance/realprovider -run 'Test(RuntimeRecoveryAcceptance|RecoveryEvidence)' -count=1
```

Expected: PASS；无需 provider credential 或 Docker。

### Task 5: Fence A Provisioned Replacement When Lifecycle Preempts

**Files:**

- Modify: `internal/workerruntime/application/runtime_recovery_coordinator.go`
- Modify: `internal/workerruntime/application/runtime_recovery_coordinator_test.go`
- Modify: `internal/localproduct/runtime_recovery.go`

**Interfaces:**

- Consumes: `ProvisionedRuntime` exact runtime/generation/container identity and server-owned deadline/absolute-expiry clock.
- Produces: 当 provision 外部调用返回时 deadline/absolute expiry 已到，coordinator 即使无法再提交 exhausted revision，也必须把该 exact late replacement 交给 `FenceReplacementRuntime`；不得留下孤儿容器。

- [x] **Step 1: Add failing race tests**

覆盖：

- provision 调用期间 clock 越过 deadline，persist exhausted 后 cleanup 收到 exact container id；
- lifecycle owner 已抢先推进 revision，`Advance(exhausted)` 失败时仍执行 exact fencing，并返回聚合错误。

- [x] **Step 2: Run focused RED**

Run:

```bash
go test ./internal/workerruntime/application \
  -run '^TestRuntimeRecoveryCoordinatorFencesLateProvision' -count=1
```

Expected: FAIL；当前 `exhaust` 在 advance 失败时不 cleanup，且成功路径丢失 provisioned container id。

- [x] **Step 3: Add provision-aware exhaustion**

只在已明确发生 deadline/absolute-expiry preemption 的 post-provision 分支使用专用 helper：

- 将 provisioned exact identity 写入内存 state；
- 尝试持久化 exhausted；
- 无论 revision advance 成功或已被 lifecycle owner 抢先，均 fence exact replacement；
- 保留并聚合原 effect、advance 与 cleanup error。

普通 revision conflict 仍不得盲目 cleanup，以免杀死另一个合法 coordinator 已接管的 replacement。

- [x] **Step 4: Run focused GREEN**

Run:

```bash
go test ./internal/workerruntime/application \
  -run '^TestRuntimeRecoveryCoordinator(FencesLateProvision|PersistsEveryIntent)' -count=1
```

Expected: PASS。

## Self-Review

- Spec coverage: 本计划覆盖当前发现的两个 release guardrail，并修正 acceptance evidence 的过度声明；Backend 重连、security termination 和完整 fault matrix 继续由 `2026-07-26-single-runtime-recovery.md` 的 Task 8、9、11 承担。
- Placeholder scan: 本计划不包含未决占位符或未定义接口。
- Type consistency: 使用现有 `RuntimeConfig.NetworkPolicy/Revision/Digest`、`RuntimeNetworkProfile` 和 `CreateTaskService.Execute` 名称。
- Commit boundary: 本轮不 stage、不 commit；由用户决定提交边界。
