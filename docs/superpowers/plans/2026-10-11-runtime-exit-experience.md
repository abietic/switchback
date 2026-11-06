# 明确退出状态收敛实施计划

> 用户已单独确认修正旧disabled-gate语义：仍记录明确退出并进入待处理，但不自动重建/重跑。Task1/2/3 已完成本地实现、独立评审和 root PG race 复验；最终固定镜像与页面验收仍待完成。不提交/推送/合入。

**Goal:** 自动恢复关闭时，服务端确认 exact Runtime 已退出/缺失，仍必须收敛状态并显示真实可用动作；不能无限 Running，也不能隐式启动 replacement。

**Architecture:** Worker Runtime 记录零尝试、终态 exhausted incident 与 fencing/清理意图；Task Orchestration 消费持久结果，经自己的服务提交 Task、Transition、Task-owner Outbox。沿用现有状态、权限、fixed deadline 与 outcome advisory lock。不把 Docker 故障伪装成 ACP terminal，不扩展 WorkerTurnSettlement source union。

**Tech Stack:** Go、PostgreSQL、既有 OutboxDispatcher、Materializer；只 fake Docker inspect/cleanup side effects。测试使用全新 `switchback-experience-loop1011-postgres`，127.0.0.1:55439、新卷；每个测试 helper 独立 schema，单 Go 进程 -p1。

## Constraints

- 已批准设计 `docs/superpowers/specs/2026-10-11-platform-experience-loop-design.md`。本计划落实其退出收敛项，不启用恢复、不迁移/修复原失败 Runtime/Task。
- owned WT `.worktrees/observability-master-integration-1009`；保护主树与当前 Workbench/Control dirty。没有 commit/push/merge/真实模型/新 Docker 构建授权扩张。
- 沿用 `single-runtime-recovery` active change；更新“disabled”的新故障语义，不自动关闭其它固定镜像/恢复/expiry/安全终止门禁。
- 明确区分：hard exit/missing 是故障证据；Inspector error、transport close、quiet、缺模型输出不是。任何 fence/cleanup 未证明，均不得输出 confirmed 或授予执行权。
- 关闭 feature flag 只禁新自动 replacement；已经打开的 incident 仍按既有迁移/回滚规则 reconcile。不得全局关 coordinator，也不得先提交 open 再另事务 exhaust（中间可 Provision）。
- 此处 NoProvision 断言只针对本次新 disabled 故障；历史已经授权的 active incident 是明确例外，不能宣称 flag=0 全局禁止历史 provisioning。全局抢占政策不在本次范围。
- Task 状态/actor 权限/Worker identity不改；所有执行许可、旧 generation/epoch late event、Task-owner Outbox 维持 fencing 与幂等。已 delivered/terminal Task 不回退，不生成 ArtifactRef。
- 有风险的字段/方法名称可按现有代码调整；若需要新 wire/schema/lifecycle/第三种 terminal source，先报告具体变更，不能越界实现。

---

### Task 1: Worker-owner 零尝试故障落库

**Files:**

- Modify: `internal/workerruntime/application/runtime_recovery.go`（独立窄 RecordFailureWithoutReplacement 服务/Store端口，不强制所有既有recovery调用/fake接受mode；自动模式接口兼容）
- Modify: `internal/workerruntime/adapter/postgres/runtime_failure_observer.go`, `recovery.go`；必要的新 `recovery_disabled_failure.go` 只承担这一事务
- Modify: `internal/localproduct/runtime_runner.go`，hard-inspection route 不再整体被 disabled gate 跳过；waiter精确读取terminal failure而非只查active incident
- Test: 现有 `internal/workerruntime/adapter/postgres/recovery_integration_test.go`（旧disabled gate用例在823行）及必要focused新文件；localproduct新增 `runtime_disabled_failure_integration_test.go` 或复用实际存在的runner/cleanup测试入口，不使用不存在的 `runtime_recovery_integration_test.go`

**Input/output:** authenticated/server-owned exact Session/Runtime/container/generation/epoch、monotonic evidence time与current revisions → 一个持久 exhausted incident（attempt=0、无 replacement）与 Worker failure event。active Session/Run waiting_user，Turn failed 作为后台结论；没有伪 Runtime terminal envelope 或 Artifact。

- [x] 新 public seam 回归：disabled + exact exited/missing → 不再 running；第一次观察现有 gate 导致零 incident/状态不变的 RED。不以修改旧测试期望取代新失败断言。
- [x] Worker transaction 共用 per-Turn advisory-before-graph lock；重验 terminal/deadline/expiry authority 与当前身份。新 incident 在一个事务内直接达到 exhausted，任何 observer/coordinator 都看不到新 open/provisioning 窗口。duplicate same-generation evidence 返回原结果、旧 generation no-op/conflict。
- [x] 事务内撤销 dispatch/control authority，记录真实 fence state 与 cleanup intent；外部 Docker/FS/Secret revoke 在事务外根据持久意图执行，response loss/Backend restart 可重试。未证实 write authority 不能称 confirmed；绝不分配新 generation、Provision 或重放 Prompt。
- [x] 复用已有cleanupTerminalRuntime的exhausted/zero-replacement rediscovery与RuntimeCleanupStore.Begin durable schedule，不新增清理表。atomic commit刚发生时仅DB fence事实，非容器/secret清理confirmed。
- [x] exact terminal failure可被waitTurnTerminal/restart查询，读取严格绑定tenant/session/run/turn/runtime/gen/epoch；dispatch interrupt前重读，返回既有errRuntimeRecoverySettled handoff，不走普通report finalizer。不得因FindActiveRuntimeRecovery不含exhausted而继续等待/向死Runtime发命令。
- [x] 区分已有 active incident：关闭 flag 不擅自取消既有恢复；current generation正常检测和 lifecycle preemption保持既有顺序。
- [x] 精确 Worker event 携带足够安全的 binding/revision/outcome refs，供 Task owner 处理，不能在此新分支直接 UPDATE tasks/插 TaskTransition。不得恢复 generation-1 RuntimeLossStore 旁路。
- [x] 通过 focused PG scenarios：duplicate、missing、alive+transport-close、Inspect error、terminal/deadline先赢、old epoch晚到、零 Provision、清理未知、restart后只处理原意图。Run/Session/Task refs与proven artifacts不被伪造或换身份。

Task1 evidence: `.scratch/runtime-exit-worker-1011-report.md`、独立 r1/r2 评审、root `.scratch/runtime-exit-worker-parent-race-1011.log`（exit0，Worker30.163s/local25.874s）。评审跨任务项由 Task2/3 和最终固定镜像/页面 gate 承担；真实 Docker/FS/Secret cleanup、完整故障矩阵没有在此勾选或声明通过。

### Task 2: Task-owner 持久结果消费与投影

**Files:**

- Add: `internal/taskorchestration/application/runtime_failure.go`, `adapter/postgres/runtime_failure.go` small durable outcome consumer与Task-owned exact binding/initiator reader；`internal/localproduct/runtime_failure_events.go` composition，不复制 Worker SQL 到 projection
- Modify: `internal/localproduct/app.go` 的 Outbox publisher composition（consumer成功后materializer；失败release/retry）
- Modify: Task pause store/service 仅按实际需要覆盖新 owner outcome，保留 exact initiator、expected revision、stable idempotency 和 Task-owner event
- Modify: `internal/eventingprojection/adapter/postgres/materializer.go` waiting message/actions，按实际 attempt/fence truth
- Test: combined PG owner/Outbox/Materializer public flow、新 failure任务状态projection以及重复/response loss

- [x] 先新增 RED：Worker failure持久后，通过真实 OutboxDispatcher.RunOnce→Task consumer→TaskPauseService→Materializer.Publish，Task必须waiting_user并存在同事务 TaskWaitingForUser.v1 owner event，而不是只投影Worker文案。
- [x] consumer从服务端durable outcome/currentTask读取真实initiator，重验exact Session/Run/binding与revision；delivered/terminal或已切换Run不回退。response loss/重复delivery收敛原Transition；无Task则Worker-only。
- [x] duplicate同样重验服务端Tenant/initiator/binding，eventID+digest绑定幂等；不相信payload actor、吞RevisionConflict或重试时改暂停另一Run。现有exhausted实际event type为`RuntimeRecoveryFailed.v1`，不是RuntimeRecoveryExhausted.v1。载荷不含Task/Run/Turn/revisions/epoch；consumer必须通过incidentID绑定Worker-owned不可变failed Runtime/Run/Turn/epoch与版本事实，再读Task-owned当前binding/initiator，不从Session当前Run猜故障目标。字段不足时先报告具体additive字段，不擅自新增wire/migration。
- [x] 保留正常 recovery exhaustion 既有兼容性；若本项不改其 legacy direct Task writer，明确记录其独立遗留，不能把新分支声明为所有 recovery owner 重构完成。
- [x] 零尝试文案写“自动恢复未启用，Runtime 已停止，需要处理”，不写“已尝试一次”。投影不提供后端无法执行的 reply/retry/interrupt，不从UI清空/伪造终态。
- [x] read/reload同目标状态与relatedrefs稳定；Task owner Outbox failure回滚Task/Transition，Worker outcome保留以供retry；不得在材料化时推进领域状态。

Task2 evidence: `.scratch/runtime-exit-task-owner-1011-report.md`、r2 Approved review、root `.scratch/runtime-exit-task-owner-parent-race-1011-r2.log`（exit0/noSKIP/race，16.120s）。r1 cleanup-before-consume 遗漏已通过 actual CleanupService 两种顺序 RED/GREEN 修复。较新 Runtime 正向 superseded no-op 的 Minor 测试建议留给最终离线回归；不阻塞 Task3，不能丢弃。真实 external cleanup、enabled legacy owner 重构和页面门禁仍未关闭。

### Task 3: 允许动作收敛与回归

**Files:** `internal/taskorchestration/application/actions.go`、`adapter/postgres/actions.go`；必要的新 `internal/workerruntime/application/runtime_failure_cancellation.go`、`adapter/postgres/runtime_failure_cancellation.go`；必要 `internal/localproduct/runtime_failure_cancellation.go` 和既有组合入口；对应 focused application/PG/localproduct tests，以及 projection action tests。新 Worker-owned 文件/内部 port 写入前须向架构会话申报 exact paths/用途；限定已有 cancel 动作，不新增自主 retry/provision command。

Read-only preflight: `.scratch/runtime-failure-cancel-preflight-1011.md`。核验假设后再实现，静态报告不算 RED。CancelTask 当前 failed Turn 查询缺口与无条件 waiting_user→interactive 必须分开修复，保持健康 resident 和 backend-deadline cancellation 原有语义。

- [x] 先测试真正 initiator cancel 已停止/fenced 的 waiting Task。必须拒绝其它actor/stale revision；不向dead Runtime派 interrupt，不因failed Turn查询不到delivered/consumed而回滚。
- [x] Worker-owner收敛Run/binding依据exact失败/fencing事实，不由Task/Frontend直接编造ACP Turn结果。不能仅凭status=lost宣称write fencing；未证明安全释放则保留待清理/不授执行权，投影提示准确。
- [x] 取消业务事实可先提交；外部 write fencing/cleanup 未证明时保留 active Run、Task binding、waiting Session 和 continuity quota，不签新 permit。确认 exact cleanup 后才由 Worker-owner outcome-lock-before-graph 事务收敛原 Run，failed Turn 不变；WorkerSession 的 bound_task_id/active_worker_run_id/dispatch slot 只能由 Worker-owned port 写入。Task owner 编排/消费该结果，仅写 Task 自身记录与 owner event，不以“释放绑定”直接改 WorkerSession；Session 不自动 interactive、不重建或重跑。
- [x] Cancel/Settle response-loss replay 重验实际 tenant/initiator、exact operation/target/idempotency；返回原 EventID/revision，不把 TransitionID 或当前 mutable Session revision 充作首次结果。普通 DB error 不等于已取消/无任务/冲突。
- [x] 独立 reviewer 检查 outcome lock、atomic新disabled路径、already-open兼容、Task owner/Outbox、取消与真实动作。修复后单进程unit/race/PG及严格OpenSpec validation。
- [ ] 真页面验收仍在最终新候选闭环中进行；旧失败Task只保留只读证据。正式report、同身份观测与完整E2E未完成时本计划不标全完成。

Task3 evidence: `.scratch/runtime-failure-cancellation-1011-report.md`、r1 Approved review（无 Critical/Important）、root `.scratch/runtime-failure-cancellation-parent-race-1011-r1.log`（exit0/noSKIP/race，18.033s）。当前故障图的真实 active/bound IDs 已收敛；健康路径历史字段兼作 command basis 的技术债保留。Minor 的 legacy 无 digest receipt 正向重放覆盖与 Task2 较新 Runtime 正向覆盖进入最终离线门禁；既有未归因 fixture 超时保留，不抹除为成功。外部真实 cleanup 和最终页面验收仍未关闭。

## Verification commands

PG test URL仅为独立fixture测试凭据，不使用任何旧env：

```bash
export SWITCHBACK_TEST_DATABASE_URL=postgres://switchback_test:isolated-loop1011-test-only@127.0.0.1:55439/switchback_test?sslmode=disable
go test -tags=integration -p 1 -parallel 1 ./internal/workerruntime/adapter/postgres ./internal/taskorchestration/adapter/postgres ./internal/eventingprojection/adapter/postgres ./internal/localproduct -run 'TestDisabledRuntimeHardFailure|TestRuntimeFailureTaskOwner|TestRuntimeFailureCancellation' -count=1 -timeout 180s
```

最终focused regex必须对应实际已实现的新测试，SKIP不算通过；资源需确认由本会话占用，避免并发migration/fixture。实现者报告保存新的`.scratch/runtime-exit-implementation-1011.md`，记录RED/GREEN/owner事件/NoProvision及残余门禁。
