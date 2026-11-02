# Switchback 运行与发布清单

本次采用分层交付：主分支保存已完成的已提交功能与进度；新增容量切片保存在独立功能 PR，完整 PG 验证仍有未集成执行入口及旧夹具失败。这里的运行进度描述该切片，不表示它已全部进入主分支或通过验收。

最新实际 CI 发现另两项问题：RUN-052 为 Go/Python Engine 文件合同不一致（代码修复已通过定向测试，旧候选需更新）；RUN-053 为驻留后续 Turn、返工、续作、恢复等生产 owner 尚未接通。保留原有拒绝，不删除或改弱测试来制造全绿。

_截至 2026-11-02；单入口维护 G0–G7、当前 RUN-ID 和独立仓库发布结果。_

---

当前目标是任务完成后生成正式报告，在页面由 initiator 接受，并把同一 Task/Run/Artifact 的观测关联起来。现阶段先取得当前候选真实 owner/Engine/network 收据，再按原执行范围进入后续步骤。公开仓库发布和 CI 不等于产品现场验收。

## 📋 当前运行门禁

| 门禁 | 验收所需事实 | 当前状态 | 更新关联 |
| --- | --- | --- | --- |
| G0 控制器 | 与新候选 exact seal/binding/成员集绑定的 reader/main 组合 | 旧绑定 v2 离线 9 项通过、独立 C0/I0/M0；新候选组装后须重绑并核对组合回归，未执行真实 sealed CLI | RUN-051 |
| G1 当前候选验封 | 当前 exact seal/binding 的完整验封，回执在执行目录之外 | 旧候选 EXIT0 / 154.816 秒；含 RUN-052 修复的新候选尚待组装和 fresh verify | RUN-049、051 |
| G2 当前部署归属 | 当前 owner/store/API/FD/maps/namespace 与新 IPAM/creator2 合同一致 | pending | RUN-048 |
| G3 受控切换保存 | 清单内 Stop、no-replace 保存、post-identity 与完整 checkpoint | pending；历史其他候选成功不升级 | RUN-039–044、048 |
| G4 当前 Engine/网络 | install/effective config、containerd/engine ready 与固定网络完成收据 | pending；历史 ready 有事实 | RUN-044、048 |
| G5 资格与 Core | 当前卷级/native receipts、原签名容量/Trust/journal/PG 绑定、安装与同进程首启 | pending | RUN-001、007–017、048 |
| G6 Worker/ACP | 原 PG 原子 reserve/permit、exact-CID quota、单次 Start、ACP 实际连通 | pending | RUN-001、031、050 |
| G7 Task/页面接受 | 同 Task/Run/Artifact 正式报告、initiator 接受、刷新保持与观测关联 | pending | RUN-050 |

G0–G7 是证据分类，不是新增授权制度。不得用一个格子的通过跳过原有动作范围、权限或安全前提。

## ▶️ 下一次单次执行

<a id="下一次单次执行"></a>

当前 active issue 为 RUN-052：代码合同已修、候选待更新；RUN-051 reader 逻辑已离线修复，但 v2 仍绑定旧候选，须在新候选组装后重绑。RUN-053是未完执行入口，保持独立PR；首次闭环只使用明确支持的initial路径。上次失败即停的窗口已结束，新动作不得续用旧 grant/claim 或旧授权记录。以下是已有操作清单的公开摘要，省略私有路径、秘密与授权原文，不是可直接替代私有封存包的运行脚本。

1. 保留失败证据与旧控制目录。先生成包含 RUN-052 两个 exact leaf 修复的新 kit/bundle、新 exact seal/binding；这些新产物尚待组装。将 RUN-051 已评审 reader 逻辑及 main 绑定重新对准该候选与精确成员集，核对重绑组合回归、控制器/test/review 身份，确认固定 execution 尚不存在。旧 `8a19eaf9…` / `4714f193…` 仅识别历史未执行 sealed main 的候选，不能作为下一命令的目标。
2. 在新的明确单次授权内，新建独占外部控制目录；只复制已重绑新候选并核对过的精确控制器，绑定当次真实用户意图。原失败窗口的授权记录不复用。
3. fresh 完整验封步骤 1 新生成的 exact seal/binding，并核对控制器绑定一致，回执写外部控制目录。失败不创建 execution。
4. 验封通过后，以固定、无优化的 Python `-I -S -B` 入口运行重绑后的精确控制器一次；禁止 `-O` 或 `PYTHONOPTIMIZE` 移除守卫。独占创建 execution/carrier，复制该新候选声明的精确成员集，构造本次单次授权并仅调用一次 sealed `launch_copy.py actual`。
5. 沿原范围推进：仅已有 VM 为 Running 时，fresh owner 证明后 Engine/containerd 各 Stop 至多一次；原样保存旧部署、四份独占复制、一次 Engine 准备。此前成功才允许原清单既定的私有配置读取、镜像、新秘密、新 PG/迁移、双能力有限采集和条件 Core 首启。此处仅描述原范围，不发布配置内容。
6. 以实际收据更新 G2–G5、RUN-048/051/052 和最后成功步骤；G6/G7 只在相应实际动作已获授权且真实验收后更新。

既定上限保持：maintenance 3600 秒 + 独立 cleanup 20 秒，outer 7200 秒，Engine 120 秒，qualification 240 秒，barrier 300 秒，以及原字节/节点/参数限额。时间上限不表示当前 binding 已完成 fullscale 测量。

## ⚠️ 停止与范围

- 任意失败或效果 unknown 立即结束当次窗口；不 retry、不续跑、不做失败后 guest 读回或 Stop。
- VM 非 Running 则停止，不启动 VM；不改分区；模型/provider 调用须另有明确授权，当前未授权。
- 候选、绑定、owner、执行目录前提或授权范围不匹配则停止；不得用旧 PID/旧 receipt 代替 fresh identity。
- 保留 primary error、最后成功 effect、未完成动作与 unknown effects。未捕获的旧现场内容不补造。
- 本清单的公开整理没有运行 VM、SSH、Docker、PG、provider，也没有读取凭据或执行历史脚本/测试。

## 🔗 仓库同步与 CI

<a id="仓库同步与-ci"></a>

本节由发布执行方在真实完成后填写。勾选必须附可公开核验的 commit、CI run 或 settings 结果；计划不能勾作完成。原私有历史保留，与独立公开快照分开。

- [x] 私有 checkpoint 已隔离保存，原工作区 HEAD/index/未提交内容保持。
- [ ] 独立公开快照已完成脱敏；不含私有 ledger、原始审计、凭据、个人路径、私有历史引用。
- [x] 本目录四文件已进入公开快照；53 个稳定 RUN-ID 完整，主入口可达。
- [ ] 现有 Static、Go/race、PG/no-skip、Web、Browser、Required CI 已保留，并补 publication/Python runtime 检查。
- [ ] 依赖私有历史的 CI 基线已替换为可移植 exact baseline。
- [ ] publication/local checks 与独立 review 已通过，阻塞失败已修正。
- [ ] 独立远端 master 的完整 CI 已通过；已记录 exact commit 与 run。
- [ ] 远端公开可见性及 master 严格 Required、PR-only、无 bypass/force/delete 已读回；secret scanning/push protection 状态已核实。
- [ ] 原工作区和冻结运行证据未改；发布结果回执已记录。
- [ ] 发布完成后回到上述下一次真实执行；不把发布授权解释为续跑已失效窗口。

| 发布记录 | 当前值 |
| --- | --- |
| 公开 exact commit | 待发布执行方填写 |
| 完整 CI run / 结果 | 待发布执行方填写 |
| 可见性 / 保护设置读回 | 待发布执行方填写 |
| 原本地状态保持核验 | 待发布执行方填写 |

## 📚 更新规则与证据

[问题表](runtime-issues.tsv)保持 RUN-001–RUN-053，后续同根因更新原 ID。每次更新同时修改[主入口](README.md)中的最后成功步骤、当前阻塞和下一次具体实跑；仅新独立问题增加新 ID。不要把离线 fixed 改成真实 passed。

本目录转述既有记录，不是本次重新验证。完整候选及控制器 SHA256 见[复盘](2026-11-02-runtime-progress.md)。私有本地证据保留：**private evidence retained, not publicly reproducible**。公开版本不发布秘密、原始审计或本地证据路径，也不用不存在的仓库链接充当证据。
