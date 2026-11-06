# 独立 Linux 体验环境执行计划

> For agentic workers: 使用 subagent-driven-development 逐项实施和独立评审；禁止 Git mutation。环境步骤仅在获得对应操作授权后执行。

_2026-10-11 · 已确认设计的实施拆分；不是已部署的环境或可直接执行的安装脚本_

---

**Goal:** 新任务能在真实页面完成、展示正式报告、刷新保持一致，并跳转到同身份观测；不影响旧环境。

**Architecture:** Lima + VZ 原生 arm64 guest；新 Engine、独立 XFS quota 盘及产品 DB；Backend 编排 sibling Worker；现有观测服务通过逐服务受控通道接入。容量不足只等待，不启动下一 Turn，也不阻止已完成工作的报告结算。

**Tech Stack:** 已核查的 Lima v2.2.1、classic overlay2、XFS project quota、现有 Go/PostgreSQL/Compose/前端及验收工具。安装时的版本和镜像必须锁定，不自动跟随 latest。

## 统一会话与当前主线

2026-10-13：用户将三条讨论汇到 [Switchback 统一推进与精简](codex://threads/01a1307d-1161-7860-9cb2-a3788c0e8298)，并选择“先完成体验闭环，再定向精简”。原始历史保留，当前会话作为继续实现和决策的入口。

| 来源会话 | 保留的工作上下文 | 当前处理 |
| --- | --- | --- |
| [审查代码库架构机会](codex://threads/01a021d1-3d8a-7300-bef7-d577a63414d7) | 模块边界、源码偏差和可维护性机会 | 只在主闭环触及的接口/事务边界上定向收敛，后续再做整体精简 |
| [集成可观测性与 YHC Harness](codex://threads/01a050db-aa9b-7732-869d-19a8d0c2aff0) | 正式报告、生产 Web、观测关联、隔离 Linux 体验及容量/结算阻塞 | 继续当前唯一 implementation candidate；新 Harness 正向接入延后 |
| [重新分析项目架构与实现偏差](codex://threads/01a0080c-aafb-7c62-8cc5-eb86810299b1) | 需求权威、历史 D2-r3 文档收敛与独立 D3 操作边界 | 作为需求依据；不把历史提交/计划或合并会话当成 D3/Git 操作授权 |

当前唯一源码候选为 `.worktrees/observability-master-integration-1009`，HEAD `fbebfe754045bdbf1af40871a5018eed33743011`；根工作树和其他候选的未提交内容不合并、不覆盖。精确切片进度和原始证据见 `.superpowers/sdd/isolated-linux-experience-progress.md`。

首个正向范围是 **initial Task → 正式报告 → 预览/下载 → 页面接受 → 刷新身份一致 → 同任务观测**。先接完整执行/终态链，其余新 Run 入口明确拒绝；真实 Repository/Workspace、更多 Harness、续作/recovery 正向和整体模块精简后续再推进。Task2 的所有路径与真实 Linux/UI/provider 验收仍各自保持未完成，不能由局部 PG 绿替代。

## 📌 全局约束

- 当前只允许文档、源码、隔离测试；安装、下载、VM/服务启动、磁盘创建/分区/格式化/挂载都等待另行授权。
- 不修改 Desktop 配置、默认 context、旧项目/DB/端口/任务/证据；不停止旧服务换取容量。
- Worker 固定 2 vCPU、4 GiB、512 PID、10 GiB writable layer；这是单容器限制，不是 WorkingCopy 总额度。
- aggregate admission 不超过经证明可用容量的 80%。未知、过期、身份不匹配或余量不足拒绝，不能靠改 VM 或 synthetic test allow 绕过。
- 队列保持原身份、FIFO 和幂等；容量等待不创建 permit、active Turn、started_at 或 deadline。报告、terminal、cancel、expiry、cleanup 仍能收敛。
- 未经用户批准，不提交、推送、合入或清理工作树。付费验收只用尚未消费的那 1 个只读任务，不把失败当重试授权。

## 📋 候选资源与明确边界

这些是供后续操作授权核对的候选值，尚未分配，也不代表当前宿主准入通过。启动前重新测内存压力、CPU/空间和旧服务负载；不足就停止，不自动扩大资源。

| 项目 | 候选配置 |
| --- | --- |
| VM / Compose project | `switchback-core` / `switchback-core1011` |
| VM 资源 | 4 vCPU、10 GiB RAM、24 GiB 系统盘 |
| 新数据盘 | `sbquota`，64 GiB 上限，独占、新建 |
| guest Engine root | `/mnt/lima-sbquota/docker` |
| guest 产品 data root | `/mnt/lima-sbquota/product` |
| 首轮 Worker | 同时最多 1 个；仍须真实 admission |
| 宿主页面 / Backend | 候选 `127.0.0.1:44175` / `127.0.0.1:48081`，执行前查重；既有 `44174` preview 保留 |
| DB / gRPC / metrics | guest 内部；不发布宿主公共端口 |
| 浏览器观测入口 | 保留旧 Grafana/Langfuse 地址；执行前重验 |

核心部署只准备 Web、Backend、Assistant LiteLLM、PostgreSQL、一次性 migrate，以及按需 Worker；不复制 Langfuse/ClickHouse/Redis/对象存储整套栈。`deploy/local/compose.yaml` 当前仍依赖旧整套栈，不能直接 `up` 充当瘦身方案。后续独立 profile 需为核心依赖设置资源上限、guest 路径、日志轮转和显式观测 transport，先校验渲染结果再启动。

## ✍️ 现在执行：代码与证据准备

### Task 1：解除跨 Session 的结算阻塞和启动误判

Files: `internal/localproduct/runtime_settlement_recovery.go`、`runtime_start_guard_integration_test.go`；确定性复现启动误判后，另经架构确认加入 `runtime_runner.go` 的窄修复。

- [x] 两个真实 pending settlement 回归：旧项等待 queued continuation 的容量，另一 Session 的报告仍须完成发布。
- [x] 包含“忙锁 → 容量等待 → 独立报告”的混合组合；保留被拒绝项、同 Session FIFO、lease fence 和错误传播，有界扫描，不造完成事实。
- [x] 真实 Provision 屏障回归：初始化尚未完成时不得误判新 Runtime 丢失；本进程初始化保护在成功/失败/取消后释放，无本进程初始化 owner 的遗留 `starting` 和正式运行后的真实丢失仍正常收敛。成功/取消释放经过真实检测回归；普通错误释放经共享 defer 和独立源码审查核实，不等于实际 Backend 重启验收。
- [x] 原失败场景 GREEN、覆盖组和定点 race；独立任务评审，保留 RED/失败日志。

具体 brief：`.superpowers/sdd/settlement-capacity-fairness-1011-brief.md` 及独立评审后新增的 `settlement-capacity-fairness-fix-1011-brief.md`。这是原生产拒绝门禁和结算的补全，不是正向容量许可。

2026-10-12 本地收口：修复方最终 focused / race 各 12 个顶层测试＋8 个子项 PASS，控制器另跑当前候选 PG 13＋8 PASS，均无跳过；r2 独立评审 Approved。完整证据索引为 `.scratch/settlement-capacity-fairness-parent-closeout-1012.md`。旧失败未删除，测试暂停造成的 restore ERROR 噪声保留为 Minor；不宣称所有 busy 组合的全局公平性或整个平台回归通过。

### Task 2：容量预留接线设计与分阶段实现

2026-10-13 更新：PG pool snapshot、resident commitment、execution reservation 及初始 Task 的 reserve/permit/consume 已实现并完成各自独立评审；部署配置的私有 Trust/Reader 仅接入首次 Task 路径，未配置时继续拒绝。C1 等待诊断、C2a 同 owner 激活和 C2b 正式报告/公共接受/原身份/HTTP 观测关联已通过定向本地验证及评审。成功容量证据来自显式 synthetic fixture，真实 Linux 物理证明与完整生命周期仍未完成；standalone、续作、recovery 等未接入的新执行路径继续硬拒绝。当前具体证据见 `.superpowers/sdd/isolated-linux-experience-progress.md` 与 `.scratch/experience-loop-current-1013.md`。

以下是原执行顺序与完整 Task2 的验收范围，未勾选项保持整个范围未完成。旧计划 Task 2A 的“不加 migration”仅适用于历史拒绝切片；不能把文本 `capacity_admission_ref` 当作持久账本，也不能把现有 synthetic 账本验证当作实际 quota enforcement。

实施顺序：先确定性复现并修复正向 queued continuation 的 `aggregate revision conflict`，再按可复现等待环收敛既有事务锁顺序，随后实现 PG 预留账本及 token 消费，最后接可信 pool probe 与公开等待状态。

- [x] 普通 queued continuation 的 Task lifecycle 冲突已确定性 RED→GREEN 并独立评审 Approved：正式报告先将 Task 推进 `delivered`，queued store 却只接受 `running`。窄修复在同事务将 exact bound Task 推进 `running`，创建新 Run/Turn、TaskTransition 和 Task owner Outbox，保留旧 report/result refs、四 aggregate revision guard 和幂等。
- [x] Task-first 锁序首切片：`StartWorkerRun ↔ CancelTask`、`EnqueueTask ↔ AcceptTask` 两条真实 PostgreSQL `40P01` 已 RED→GREEN，独立 spec/quality review Approved。首条来自 Run 的 Task FK/permit 竞争，第二条来自无效 enqueue 在拒绝前的 Session/Task 逆序；保留 consumed permit 与旧 enqueue 序号重放、原权限/CAS/事务边界。
- [x] recovery replacement 的 Session-before-Runtime 窄锁契约已 RED→GREEN、独立 spec/quality review Approved：旧路径在等待 Session FK 前已持有 failed Runtime，NOWAIT probe 为 55P03；现显式先锁/重验 Session，再写 fence/Runtime/replacement，保留原 Run/Turn/deadline、CAS和幂等。没有复现 cleanup 死锁，不把 JOIN 的表列表当行锁顺序证明。
- [x] terminal journal Apply/domain replay 的 original authority 锁边：公开 ApplyEvent 与 DomainCommit replay 实际40P01 RED→GREEN，独立 spec/quality Approved；original outcome→existing settlement→graph/channel→message，后续完整identity/owner/lease/phase/CAS校验保留。缺失settlement拒绝、不创建，重放不变。

2026-10-12 continuation 本地证据：`.scratch/continuation-revision-1012-7tc9el5d/task-2a-report.md` 与 `task-2a-review.md`；完整定点 race 17 顶层＋15 子用例通过，最终新增 PG waiter 屏障后新组 focused/race 2＋2 通过，均无 SKIP。首次 focused 的既有 busy-owner 失败仍为 UNKNOWN，保留原日志并另作 closeout；后续 GREEN 不作为该失败已修复的证据。这些结果不关闭 Task 2 ledger/probe 或真实 Linux/页面验收。

Task2b 本地证据：`.scratch/capacity-lock-order-1012-lse4ujhy/task-2b-report.md`、`task-2b-review.md`，before→current delta 单独封存；最终 focused/race 各 2 顶层，邻接 8 顶层＋5 子例，均 0 FAIL/SKIP。最初 sandbox TCP 拒绝不算业务 RED；两条后续 RED 各有实际 blocker graph 和 40P01。下一范围为 `.scratch/recovery-lock-order-1012-e45c27x9/task-2c-scope.md`；terminal Apply 切片另见 `.scratch/terminal-apply-lock-1012-euxkigcy/task-2d-scope.md`。

Task2c 本地证据：`.scratch/recovery-lock-order-1012-e45c27x9/task-2c-report.md`、`task-2c-review.md`；最终 focused 7 顶层＋6 子例、race 1 顶层＋6 子例，0 FAIL/SKIP。四种并发 Session revision/status/binding/generation 变化和 Outbox fault 证明零部分 replacement 写入；普通成功/重放证明原身份。removal bool 的可信度未被提升，ledger接线不得据此直接释放容量。

Task2d 本地证据：`.scratch/terminal-apply-lock-1012-euxkigcy/task-2d-report.md`、`task-2d-review.md`；最终 focused15顶层＋15子例、race4顶层＋10子例，0FAIL/SKIP，root核验source/raw receipts。全仓锁图仍未被证明。下一切片为Task2e基础账本；随后S2先接初始Task完整启动/报告/接受/失败收敛，其余未接入新执行路径保持硬拒绝。用户已选择先体验闭环、再定向精简；完整Task2的全部路径检查项保持未完成。

- [ ] 在 Worker Runtime owner 下确定 pool snapshot、resident commitment、Turn execution reservation 的唯一账本和 lock 顺序；Task 不另造 scheduler。
- [ ] 明确采样身份、过期、单位、非 Worker 开销、同池 WorkingCopy/image/log/build 占用；使用 guest 同一 XFS 根采样，而非 Backend 容器 `statfs` 或宿主 APFS。
- [ ] 原子 reserve-before-permit；两个并发请求竞争最后一个额度只能成功一个；响应丢失/重放返回同一 reservation。
- [ ] Turn terminal 只释放执行预留，resident Runtime/SessionProcess 与仍占用的 storage 不清零；停止/删除的证明齐全后才释放对应 commitment。重复释放、迟到事件和崩溃用 reconciliation 收敛，不能 TTL 到期就认定容器消失。
- [ ] Task / standalone / accepted queue / settlement continuation / recovery 均绑定 exact Session、generation、profile、pool 与 reservation；模拟账本不进入生产 allow 分支。
- [ ] 真正等待/释放/唤醒形成公开 `runtime_capacity` 状态后，以页面和并发 PG 回归验收；普通路径旧 Runtime revision 冲突也须先复现并修复。

没有实际 resident 基线测量时，先保守计入整个 resident 上限；不把 idle 当零消耗，也不声称“Turn terminal 会释放整个 4 GiB”。执行预留与驻留承诺须去重，不能同时全额重复扣减或同时遗漏。具体单位/去重算法在接线前锁定并测试；这条保守边界不修改 resident-until-terminal 生命周期。

首次正向接线采用以下保守约束，不先做预测性超卖：

| 事实/命令 | 唯一责任与事务语义 |
| --- | --- |
| Pool snapshot | 可信 guest probe 采集；Backend 重验身份、时间、原始单位与单调 sample sequence；事务内不调用 Docker/guest |
| Resident commitment | 按 exact Runtime generation 唯一；包括 provisioning 中的承诺，容器被证明停止/移除前不因 lease/TTL 或 Turn 终态释放 |
| Execution reservation | exact Session/target/revision/profile/pool epoch；同 Runtime 复用 resident commitment，不重复扣一份全额 profile |
| Reserve | 首先锁 pool，校验 snapshot 和预算，再建立幂等 reservation；reservation 未落盘不得发 permit |
| Bind/consume | 在原业务事务内校验 reservation 为当前未消费 token、目标合法且未过期；绑定 permit/Run/Turn，失败回滚不启动 deadline |
| Terminal/reconcile | 幂等释放执行 token；尚未 provision 的承诺仅在证明没有容器/未消费 permit 后撤销；响应丢失先核查，不直接重试副作用 |

CPU/RAM 初版以 resident 的整个固定上限和其他工作负载的受控上限保守计数，不能从瞬时 idle 使用量推算可借出的预算。storage 用同池真实 available bytes 与尚未使用的额度承诺比较：已落盘用量已体现在 available 中，不能再全额扣一次；remaining commitment 取硬额度减同身份已用量的非负值。采样不能识别的容器、无限制 core 数据增长、WorkingCopy/cache/日志/build 增长承诺都按 unknown 拒绝；必须有独立受控上限/保留预算证据才能进入 allow。10 GiB 的 layer quota 不能代替这项证明。

拟议文件 seam：`internal/workerruntime/application/runtime_capacity.go`（小型命令 interface）、`internal/workerruntime/adapter/postgres/runtime_capacity.go`（pool/预留/reconcile）、同 adapter 下事务校验 helper、独立 migration（锁定编号后添加）。Task permit 的 `session_dispatch.go` 和 Worker `start_run.go` / `follow_up.go` / `queued_turn.go` 消费同一 token，不分别算容量。具体锁顺序必须结合已有 Task-first/Session-first 事务核对；统一顺序的迁移未验证前，不混合旧新锁法启用生产。

首组真实 PG 用例固定为：最后一份额度的两请求并发（恰一成功）；响应丢失的同幂等重放；不同目标复用 token 拒绝；sample 过期/Engine 或 UUID/epoch 变化拒绝；terminal 重复释放不增加余额；resident 未停止时余额不回升；中断在 reserve→permit→provision 三个缝隙后的恢复；同 Session 的较早 Task 队头不被后来的 standalone 越过。跨 Session 不另建全局优先级队列。最终以 `App.Handler()`/公开 Task/Worker 读模型检验无 permit/Turn/deadline 和可解释等待，不以私有 calculator 测试代替接线。

### Task 3 源码准备切片 S3a：默认阻塞的环境包

在现有源码授权内，C2b 独立通过后交付纯 plan/render 工具、core-only Compose/Lima/daemon/systemd 模板、精确记录的版本清单与只读 Linux collector。绑定范围见 `.scratch/core-preparation-1013/task-s3a-scope-draft.md`；冻结后使用其正式 scope。生成物只写入新的私有目录，不执行安装、下载、VM/服务启动或磁盘初始化。

准备 manifest 固定为 `preparation_blocked`，实际镜像、Engine、boot/mount、quota enforcement、宿主 backing 及 all-time Mmax/Delta_inode/exact SRU 未知。只读采集与配置候选不生成生产签名、Trust 或 ready `capacity.json`，也不能证明 DockerProvisioner 的 cgroup/no-swap/create-before-start 接线。物理证明和接线另列 S3b，本切片不关闭 Task2 的完整 probe/allow 或 Tasks3–5 环境验收。

采集器仅可发布自己的诊断目录。成功隔离旧签名文件时验证 Reader 不再读取；目录不安全或不可写则硬失败并报告旧权限未撤销，不能把退出码当作后端 admission 已停止。新的准备目录和禁用执行是默认起点；任何既有签名目录的接管都先需要另行授权的外部 admission shutdown。

完成源码、纯 fixture 验证与独立评审后，以具体生成包及未知清单请求新环境操作授权。授权不替代物理证据，也不消费真实模型验收次数。

## 🔐 另行授权后：环境构建

### Task 3：锁定与创建清单

- [ ] 写入独立 manifest：确切 Lima binary version/SHA、arm64 guest URL/SHA、Engine package version/build、kernel/xfsprogs、Compose 版本、候选端口及宿主 backing file 路径。来源/摘要不全即不下载或启动。
- [ ] 列出授权操作和精确对象：安装位置、新 VM identity、新虚拟盘 identity/最大容量、首次初始化一次、新服务与 probe、停止但保留数据的对象。用户确认后才执行对应阶段。
- [ ] guest 初始不启动 Docker；不挂宿主 HOME/旧数据或可写源码；只输入经摘要核对的最小源码包。Lima VZ 支持原生 guest，但不是 Docker/XFS 成功证据。[^1]

### Task 4：一次初始化与重启证明

- [ ] 证明 `sbquota` 未占用、新建、独占、空白，登记设备与 backing file；任何不一致停止，不更换目标继续格式化。
- [ ] 唯一获批的首次阶段才允许 `format:true`、`fsType:xfs`；初始化后记录 partition、XFS UUID、label、ftype。停止 VM，将稳态改成 `format:false`，禁止自动修盘。[^2]
- [ ] 在 Engine 尚未启动时收敛 quota mount 顺序：v2.2.1 自动挂载不带 `pquota`，不能把默认 mount 成功当作配额生效；按登记 UUID 核查 accounting/enforcement 和实际挂载目录。[^2][^3]
- [ ] 后续 guest/daemon 重启后身份、挂载与 quota 再证明；失败即留存证据并停止，绝不再次初始化。

新 guest 的 daemon 目标为 `storage-driver=overlay2`、`features.containerd-snapshotter=false`、`data-root=/mnt/lima-sbquota/docker`。Docker 新安装可能默认 containerd 后端；只在新 Engine 首次运行前选择 classic，不能切换旧 Desktop 来迁就测试。[^4]

### Task 5：quota / capacity / transport 无模型验收

- [ ] 一小一大两个独占 quota probe：同一实际写量，小额度真实越界失败、大额度成功，失败时池未耗尽；记录 project ID/limit/usage/errno 和镜像摘要，不用 `inspect` 回显代替 enforcement。
- [ ] guest-side snapshot 绑定 Engine ID/build/endpoint、guest boot、root、XFS UUID/mount namespace、时间和原始字段。宿主 backing-space 单独证明；未知即不启动 Worker。
- [ ] 逐服务验证 guest Backend → retained Langfuse/日志接收端，以及 retained Prometheus → 新 metrics。首选受控 SSH 通道，保持端点认证、loopback 和明确 listener；不假定 Desktop 与 VM 同网，不新增无认证 Docker TCP。
- [ ] 浏览器端入口与服务端 ingestion/scrape 分开验证：页面能打开 Grafana 不等于新任务已被采集。通道不通即停止体验，不写成观测通过。

## ✅ 最后一关：真实页面验收

前置：代码回归/独立评审、quota/容量/transport 都通过；精确 dirty source manifest、构建日志、四个产品镜像实际 ID 相互核对。旧证据保留，新结果另起目录。

- [ ] 新页面 → Assistant → Worker，只读示例文件，报告不超过 300 字；只消耗已获批的 1 个真实任务。
- [ ] Task 正式 delivered，Report 有真实 Artifact identity、能预览/下载；无产物显示空态而非无限 Loading。
- [ ] 页面 accept 后正式 completed；刷新后 Task/Run/Turn/Artifact identity 与结果一致。
- [ ] 相同任务 identity 可在 Langfuse 与 Grafana/日志/指标入口定位；留原始 query/状态、截图与导出记录，不仅入口链接。
- [ ] 关闭/停止仅作用于 manifest 登记的新对象；保留 DB、磁盘、报告和日志，不运行 prune/down-v/强制清理旧对象。

停止条件：身份不匹配、未证明 capacity、quota/mount/transport 失败、源码/镜像不一致、真实任务失败。停止不是完成；付费失败后不自动再跑。

## 🔗 官方核查依据

本计划未实测安装/启动流程；后续各步骤的原始证据是门禁，不以文档勾选代替。

[^1]: Lima. VZ. https://lima-vm.io/docs/config/vmtype/vz/
[^2]: Lima v2.2.1. `05-lima-disks.sh`，首次初始化与默认 mount。https://raw.githubusercontent.com/lima-vm/lima/v2.2.1/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/05-lima-disks.sh
[^3]: Docker. Storage driver options per container，overlay2 size 的 XFS/pquota 条件。https://docs.docker.com/reference/cli/docker/container/run/#set-storage-driver-options-per-container---storage-opt
[^4]: Docker. Containerd image store with Docker Engine. https://docs.docker.com/engine/storage/containerd/
