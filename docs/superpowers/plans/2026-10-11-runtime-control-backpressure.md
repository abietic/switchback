# Runtime 输出背压实施计划

> 执行方式：在当前会话顺序委派实现与独立评审，测试先行。用户批准的范围不包含 commit/push/merge；技能模板中的提交步骤不适用。

**Goal:** ACK 暂时滞后时，真实 ACP→Shim→WorkerControl 输出链保持有界、可取消；不因普通背压退出、不丢消息、不重复 Prompt，终态证明仍可恢复。

**Architecture:** 保持固定 ACP SDK、Codex façade、RuntimeControl wire schema 和既有队列上限。Control 提供无丢唤醒的容量变化通知；Shim 仅保留一个待发布事件并继续处理取消/心跳。ACP 层把背压传播到 stdout，而不是在 128 条 update 或 SDK 1024 条 notification 缓冲满时丢失输出。原始 Trace 在 typed decode 前落盘，严格区分传输 ACK、应用提交和终态证明。

**Tech Stack:** Go、固定 acp-go-sdk v0.13.5、既有 gRPC fake stream、ACP 子进程 fixture；不增依赖，不使用 PG/Docker/模型。

## Scope and safety

- 已批准设计：`docs/superpowers/specs/2026-10-11-platform-experience-loop-design.md`。
- owned worktree：`.worktrees/observability-master-integration-1009`，HEAD `fbebfe754045bdbf1af40871a5018eed33743011`；主 checkout、旧失败 Runtime、旧证据不动。
- 架构会话已确认本项 acpclient/controlgrpc/shimruntime 文件无占用；不修改 AgentAdapter 六方法、codexacp.New(trace)、ACP capability/auth/profile、协议或 lifecycle schema。
- 新实现不得无限队列、丢更新、吞 systemError、在 adapter.mu 下等待 channel、阻塞唯一 Shim control select 或每条输出启动 goroutine。
- 所有 Git 调用先清除 GIT_*，禁用 global/system config 与 replace objects。
- 测试只用批准 public combined seam：真实 Shim runtime + controlgrpc.Client + acpclient.Adapter；fake 仅外部 gRPC peer 和 ACP stdio peer。现有 fakeRuntimeControl 无容量上限，不可替代本回归。

---

### Task 1: 完整输出链路 red→green

**Status:** 已实现并经 fresh r1/r2 规格/质量评审批准。四包最终 ordinary/race 通过；详细 RED/GREEN、两项测试补齐和证明边界保存在 `.scratch/control-backpressure-implementation-1011.md`、`-r2.md` 和对应 review reports。此状态不包含 Task 3 实机闭环。

**Files:**

- Modify: `internal/workerruntime/adapter/controlgrpc/client.go`（若通知封装需要独立文件，限定同 package、单责任；先说明）
- Modify: `internal/workerruntime/adapter/shimruntime/runtime.go`
- Modify: `internal/workerruntime/adapter/acpclient/adapter.go`, `client.go`, `process.go`, `rawtee.go`（实际 process 文件名先核实）
- Test: corresponding package tests；新增 `shimruntime/runtime_backpressure_test.go` 或同 package 单责任组合 fixture

**Inputs/outputs:** 真实 Runtime command、ACK/reconnect、SDK stdout SessionUpdate → 单调 RuntimeControlEnvelope 与 exact terminal；Cancel 可在 ACK 恢复前到达 ACP peer。

- [x] 先新增 `TestReconnectableRuntimeRealACPControlBackpressure`。80/256 条小型 update，SendFrame 持续排空但累计 ACK 暂扣，观察当前 `spool AgentTurn update: WorkerControl queue is full` RED。ordinal 必须通过真实 ACP peer/adapter，不直接灌 fakeUpdates。所有 producer、Send/Recv、等待均可取消，禁止用小型 fake sent channel 先阻塞制造错误根因。
- [x] 对同一 fixture 增加：256 burst、2048 burst、饱和期间 interrupt（ACK 恢复前 cancel_seen）、Close/ctx cancellation、terminal 未 ACK 时断链再 resume。要求每个已接受 ordinal 顺序到达一次、Prompt 一次、一个 exact terminal digest，重放原 MessageID/sequence，不追加第二份 terminal。
- [x] Control 保持现有非阻塞 Publish 契约；获取失败和容量变化 channel 的 snapshot 必须有无丢唤醒保证（同锁取得，或在 Publish 前取得并解释）。只等待/重试 ErrBackpressure，不重试 stale/conflict/logical-history/auth/protocol。终态及 active effect 预留继续有效；不把 ACK 当业务提交。
- [x] Shim 单 pending 事件；重试不重新生成 identity/ordinal。pending 时暂停普通更新/model observation 消费，select 仍响应 command/interrupt、heartbeat、critical ready、proxy/control failure、ctx。终态前 model observation drain 也必须是有界、可取消的状态路径，不能把背压当 fatal 或同步阻塞取消。不得为 terminal 抢过在收到terminal时已接受的有界buffer前缀；之后晚到的模型事实按原scope普通supplemental路径继续发布，不能等待无限静默来证明closure。
- [x] ACP callback 保留 SDK inbound ctx 与 exact Prompt context；释放 adapter.mu 后等消费。Turn-local delivery/closing gate 必须防 send-close 竞态与旧 callback 污染新 Turn；正常 response 的 watermark 不覆盖 ctx 取消路径。systemError 仍 fail closed。
- [x] 在等待原始帧 credit 前快照 exact Turn delivery，取得 credit 后再次核验，不把旧 waiting frame 绑定到新 Turn。无正常 SDK response/watermark proof 的 Prompt ctx abort 必须将本 Adapter fail closed、拒绝新 Prompt；不新建 Session/自动恢复。真实 cancelled response 可以续 Turn，Cancel 已发送、EOF 或 RPC error 不得被合成 cancelled 成功终态。
- [x] typed SDK 前的原始帧 credit gate：权威 raw tee 完成之后，已知且合法 session/update 才取得有界 permit；在所有 callback 返回路径释放。unknown optional 保留 raw/warning 不占 permit；invalid known validation fail closed、target write failure 释放。Close/failClosed/lifecycle cancellation 唤醒等待，无 adapter/recorder/writer 锁等待。不得绑定短暂 Initialize ctx。
- [x] 明示 FIFO stdout 边界：credit 耗尽时不能越过任意长 notification prefix 读取后面的 permission/RPC/EOF；Cancel outbound 与 Close 必须独立可达，测试 peer stdin/output 分离。现有 fs/permission/security 与 unknown optional 回归必须通过。
- [x] RED/GREEN 结果写独立 `.scratch/control-backpressure-implementation-1011.md`，记录命令、真正失败断言、修复后结果和任何残余边界；不得覆盖旧证据。

Focused command（第一轮 RED 可只跑256场景）：

```bash
go test -p 1 -parallel 1 ./internal/workerruntime/adapter/shimruntime -run '^TestReconnectableRuntimeRealACPControlBackpressure$' -count=1 -timeout 30s
```

最终本项验证：

```bash
go test -p 1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/codexacp ./internal/workerruntime/adapter/controlgrpc ./internal/workerruntime/adapter/shimruntime -count=1 -timeout 120s
go test -race -p 1 ./internal/workerruntime/adapter/acpclient ./internal/workerruntime/adapter/controlgrpc ./internal/workerruntime/adapter/shimruntime -count=1 -timeout 180s
```

本任务不改 Runtime death settlement、Task authority、观测导航、OpenSpec 的其他工作项；若完整背压方案需要扩大这些边界，停下报告而非自行修改。

### Task 2: 独立规格与正确性评审

- [x] 新 reviewer 阅读本任务、当前 dirty diff、实现者报告，核对所有 accepted updates 的 bounded ordering、Cancel/Close/replay/terminal fencing、raw tee security 和 test seam；不得以旧测试代替新回归。
- [x] 修复评审问题并补跑相应 tests；随后单次执行上述全部 package tests/race，保存新日志。无 commit。

### Task 3: 接入最终候选体验

- [ ] 本项单测只是输出链证据。待退出/报告/观测切片一并完成，以新源码摘要、构建日志与实际镜像 ID 启动隔离候选，旧现场不替换。
- [ ] 仅使用已授权的一个小型新任务，验证 production 页面→Assistant→Worker→正式 report→刷新→同身份观测；没有该证据不把本计划勾为全链完成。
