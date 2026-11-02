# Settlement 跨阶段 Runtime 进度修复设计

_2026-09-29 · Switchback · Accepted for implementation：用户已授权按推荐范围直接推进，内部实现与测试细节交由 agent 处理；实现状态另见计划与验证记录。_

---

## 🎯 目标与范围

在合法 Runtime 控制进度与 Turn settlement 并发时，Artifact 发布和 queued continuation 必须能够收敛，同时保留执行身份、权限、租约和幂等保护。成功标准不是重试次数增加，而是两个精确暂停点插入真实认证 heartbeat 后均能完成，且非法变化仍被拒绝。

本次只修这两个已暴露的执行路径，不新增 Task 状态、不改变 WorkerSession/WorkerRun 权威、不改变 Task-bound queue 产品语义、不改模型或 Langfuse frozen frame。完成后再恢复 `local-observability-stack` 的 Worker collection 与物理验收任务。

## 🔍 当前证据与诊断边界

基线为 `e440a3b`。当前实现中，domain commit 冻结四个 revision，后续 publication 和 continuation 将其作为精确比较条件；Runtime journal 接受事件时会推进 Runtime revision。ADR 0061 明确允许 Turn terminal 后驻留 Runtime 继续 heartbeat/control，因此停止心跳不符合生命周期语义。

| 失败阶段 | 冻结 / 当前 Runtime revision | 其他状态 | 证据强度 |
| --- | --- | --- | --- |
| Artifact publication 前 | 76 / 77 | `domain_committed`，无 continuation plan；Session/Run/Turn 相同 | 前轮日志与 publisher 拒绝条件吻合；未捕获直接错误/数据库 publication 行 |
| queued continuation | 538 / 540 | `terminal_event_applied`；Session/Run/Turn 相同 | 前轮隔离 race 重放 |
| queued continuation | 264 / 266 | `terminal_event_applied`；Session=62、Run=2、Turn=3 均相同 | 本轮同一精确测试重放失败，20.85 秒 |

现有端到端反馈命令：

```bash
SWITCHBACK_TEST_DATABASE_URL="$SETTLEMENT_TEST_DATABASE_URL" \
GOCACHE=/private/tmp/Switchback-supplements-go-build \
go test -race -tags=integration ./internal/localproduct \
  -run '^TestBlockedDurableProjectionDoesNotDelayTerminalSettlementOrNextTurn$' \
  -count=1 -json
```

本轮输出为 `next Turn was not dispatched within 5s`，exit 1；私有日志 `settlement-repair-baseline-current-port.jsonl` 的 SHA256 为 `c19ea8731b2d0ac6c484c14c726ec1844af3d44814c61ded7946b37aa48aac06`。首次重放误用了重启前的临时端口，得到 connection refused，仅是环境失败，不计入行为 RED；日志单独保留。当前已核实端口为 49411，之后每次启动仍须重新读取映射。

原反馈循环仍受时序影响。实施第一步必须将其缩为两个确定性的真实 PostgreSQL 暂停点测试；不能把当前日志中的两个 revision 增量直接断言为两个 heartbeat。

待区分的假设依优先级为：冻结 revision 被合法控制进度推进；Session/Run/Turn 或执行身份发生真实冲突；单纯调度慢导致 5 秒超时。确定性 heartbeat 正例、权限/身份负例以及原始时限回归分别区分三者。

## 📊 方案比较

| 方案 | 主要收益 | 代价与风险 | 选择 |
| --- | --- | --- | --- |
| settlement 专用事务入口 | 将跨阶段证据和锁内当前权限集中校验；通用接口保持严格 | 两处入口及事务协作需新增回归覆盖 | 推荐 |
| 拆分 Runtime 业务 revision 与进度 revision | 从数据模型消除无关写冲突 | 涉及 schema、所有写入方、旧 checkpoint 和恢复兼容，不宜混入当前修复 | 暂不采用 |

不采用事务外读取最新 revision 再重试：读取与写入之间仍有窗口，也无法证明旧命令可合法继承新权限。不采用全局 `>=`、跳过 revision、停止 heartbeat、延长 5 秒门槛或重写冻结 domain proof。

## 🔐 推荐事务契约

两个专用入口只由 Backend settlement coordinator 使用，不暴露为客户端可选开关，不给通用命令增加 `ignore_revision` 一类逃生参数。

共同规则：

1. 从当前 durable settlement 获取 exact identity、phase、revision、phase-state digest、lease owner/fence；锁内验证租约仍有效。调用方缓存不能替代持久记录。等待 graph/owner-stream/outbox 锁也可能耗尽租约，因此业务写入成功后、事务提交前必须再次按数据库当前时钟检查；过期则整体回滚。
2. 校验冻结 checkpoint/proof 及本次 effect 的完整身份与内容对应关系：Tenant/User/Workspace/Session/Run/Turn/AgentSession，以及 Runtime identity/generation/channel epoch。
3. Session/Run/Turn revision 和业务状态保持精确约束；Runtime revision 不可倒退，active binding、generation、epoch 和相关生命周期必须与 settlement 语义一致。仅数值前进本身不是授权证据。
4. Runtime 当前 graph 与冻结来源一致、无 replacement/fencing/ownership/lifecycle 变化时，才可将同事务锁定的 Runtime revision 用于内部 CAS。不能在调用方覆盖旧 proof，也不能将不明冲突自动解释为 heartbeat。
5. 校验与业务 effect 使用同一 PostgreSQL 事务和同一组锁；所有 ObjectStore 读取/校验、文件、网络、模型、容器动作均在事务外。
6. 锁顺序兼容现有 outcome/settlement 协议：先 outcome/settlement authority，再需参与的 Task/queue，最后 Session/Run/Turn/Runtime/channel graph；不得新增持有 Session 后反向等待 settlement 的路径。涉及 Task 时必须在 Session graph 之前取得 Task 锁。

Artifact 模块继续拥有 metadata/refs/outbox 写入；Worker 模块继续拥有 settlement/queue/Run 权威。事务协作放在 PostgreSQL adapter 层，使用小型 typed Interface，共用实际业务写入实现，不把 artifact SQL 复制到 coordinator，不让 domain 依赖数据库。

### Artifact publication

对象完整性验证沿用当前 publisher，在事务前完成。专用路径在事务内验证 `domain_committed` 的冻结 evidence receipts/digests 和 domain proof，再执行 metadata/refs/outbox 写入。通用 `PublishTurnEvidence` 的精确 stale-revision 拒绝语义保持不变。

兼容性边界：BackendDeadline forced-stop settlement 可能对应已停止或 fenced 的 Runtime，保留其原有严格 publication 路径和冻结 exact revision，不授予 Runtime 进度容忍。分流必须依据 durable settlement source 与已有 forced-stop witness，不能由客户端参数或无证据开关选择。上述 active/non-fenced 要求约束新增的进度容忍路径，不删除既有强制终止后的合法证据发布能力；以 forced-stop 发布正例及 stale Runtime revision 拒绝负例验证。

phase 更新继续遵守已有 checkpoint 扩展协议。若 publication 成功后、phase advance 前发生响应丢失，新持有者必须经当前租约与冻结证据校验返回同一 publication，不能重复写入、换用新内容或刷新旧 domain proof。

### Queued continuation

专用路径验证 `terminal_event_applied` 下已冻结的 exact queue command、previous Run/Turn、原始 intent 与 scope。只可消费该命令，不重选其他队列项。保持已有 Task binding、用户命令授权、Session 串行化、deadline、outbox 与 FIFO 约束，不把“同 Workspace”当作复用授权。

在同事务创建 successor Run/Turn、更新 Runtime/Session binding 和 queue outcome；重试必须返回同一 successor。通用 `ContinueQueuedTurn` 保持严格 revision。`action=none` 仍然不创建 Run；Task delivered/conflict 不能借此变成允许续派发。

## ⚙️ 错误、恢复与兼容

过期租约、错误 fence/phase/digest、跨 scope、业务 revision 变化、Runtime replacement、generation/epoch 变化或不合法 lifecycle 均 fail closed，返回既有分类错误，不触发自动改身份、重放副作用或创建新授权。

冻结 domain proof、Evidence 和 continuation intent 保持不可变。优先复用现有 settlement identity/lease/checkpoint 字段，不新增 revision 体系。旧 pending checkpoint 只有现有证据足以满足上述契约时才允许继续；缺失证据不得现场编造、批量修写或静默放宽。若实现证明必须迁移协议或持久 schema，应重新评审该扩展，不纳入当前默认授权。

## ✅ 验收要求

- 两个确定性正例：分别在 domain commit 后、continuation plan 落盘后暂停，经真实认证 journal 提交 heartbeat，再恢复实际 coordinator/PG 写入；修复前精确 RED、修复后 GREEN。
- publication 正例直接断言原始 stale 错误/零新增 metadata，修复后同一 TraceManifest、Artifact refs 与单份 outbox；不能只判断“没有 panic”。
- continuation 正例断言恰好一个 successor、原 queue outcome、相同 Runtime generation 和保留的冻结 proof；响应丢失/重复调用不再创建 Run。
- 两组入口覆盖 stale lease/fence、错误 phase/digest、跨 scope、Session/Run/Turn revision 变化、generation/epoch 变化、失效 Runtime；拒绝时无部分写入。
- 通用 publisher/queued store 的旧版本拒绝测试继续通过；旧 checkpoint 的受支持 replay 和缺证据拒绝均有测试。
- 使用真实事务竞争测试证明两名 settlement 持有者不能双写、lease reclaim 后旧持有者不能提交；覆盖与 journal/Task/recovery 的相关锁顺序。
- 两个入口均以真实锁等待跨越租约有效期，证明即使入口检查通过，提交前过期仍会回滚而不是发布/续派发。
- 重跑原 20 对 healthy/outage 测试，不改变单次 5 秒与 outage p95 增量 1 秒门槛；再跑受影响包 integration/race、整仓普通 integration、标准 race 和静态门禁。
- 区分本地测试、物理 Worker/provider 验收、远端 CI 和发布；本设计不自动完成 OpenSpec 的 Worker/G3/G4 任务。

## 📍 推进记录

- [x] 核对隔离 worktree、当前源码、相关 ADR 和前轮证据
- [x] 用户确认先修执行阻塞、再继续 Worker 观测的顺序
- [x] 本轮重放原失败；保留环境失败与行为失败的独立记录
- [x] 比较方案，形成专用事务入口的设计草案并自检
- [x] 用户授权按推荐方案直接实施，免去逐份内部文档审批
- [x] 修复、独立复审、完整门禁（source `f181a10`；最终证据见 `.superpowers/sdd/settlement-progress-final-verification.md`）
- [x] 恢复 Worker 采集与物理验收建设（已启动下一独立增量；不表示 G3/G4 已通过）

本次问题无需 UI/浏览器视觉选择，因此未启用 visual companion。用户在通俗现状说明后明确要求按建议直接推进；不再重复内部设计或测试 seam 审批。本文件不是实现完成或门禁通过的声明。

## 🔗 源码与决策依据

- [前轮诊断](../../../.superpowers/sdd/local-stack-continuation-race-diagnosis.md)
- [Settlement coordinator](../../../internal/localproduct/runtime_settlement.go)
- [Artifact publisher](../../../internal/artifacttrace/adapter/postgres/publisher.go)
- [Queued continuation](../../../internal/workerruntime/adapter/postgres/queued_turn.go)
- [Domain settlement 锁内校验先例](../../../internal/workerruntime/adapter/postgres/turn_settlement_domain.go)
- [Runtime journal](../../../internal/workerruntime/adapter/postgres/runtime_control_journal.go)
- [ADR 0049：Session 串行化](../../adr/0049-serialize-top-level-work-per-workersession.md)
- [ADR 0061：Runtime 驻留](../../adr/0061-keep-m1-runtime-resident-until-session-terminal.md)
