# 自持目录 FD flags 采集边界的最小修订候选

状态：用户于 2026-10-29 接受 A，授权完成整片离线修订；没有真实执行授权。

## 目标与成功标准

修正私有维护 entry 将自身目录 FD 误判为 foreign protected FD 的表示差异。让自持 descriptor 的 expected flags 与 Source 的 Linux fdinfo flags 同义，同时保留原 policy 的完整 FD tuple、sampler PID/start tick、外部进程、writer FD、mapping、归属及阶段稳定性检查。

当前失败的 seal、grant、5315 个 frame 和所有 guest 对象原样保留；当前真实窗口不重试、不续跑、不读回。新一片只交付离线源码、回归、完整载体与评审。

## 方案比较

**A（推荐）：在自持 FD 采集边界合成 fdinfo 语义。** 同时读取 F_GETFL 与 F_GETFD；仅把 F_GETFD 的 FD_CLOEXEC 映射为 flags 中的 Linux O_CLOEXEC，完整保留 F_GETFL 的其余所有位。读取前后绑定 FD 的 device/inode、target、F_GETFL/F_GETFD，变化或读取失败即拒绝。只支持当前 Linux 维护载体，使用该平台的命名常量。保留 readonly 校验；不把 FD_CLOEXEC 数值本身混入 access mode，不全局 mask flags，不豁免整个 PID。

这不新增 proc 文件采集路径；只修改 `maintenance_entry.py` 的自持 FD 边界及所需私有测试/装配绑定，原 `owner_policy.py`、`owner_native.py`、维护状态机和 TypedPreserver 行为保持。持有的五个目录 FD 继续由原生命周期关闭。

**B：由已有有界 proc reader 读取 self/fdinfo，再解析相同 flags。** 表示直接一致，但增加 proc 读取、严格 framing、FD 身份绑定与失败分支。没有必须复用原字节作为 expected flags 的需求，A 的依赖和改动更少。

## 具体交付

1. 新建独立 phase，保留旧载体。先在实际 `Inventory.bind` 调用处建立红色回归：fake Linux F_GETFL 不含 CLOEXEC，而实际 Source inventory 语义含该位，再进入原 policy；不能把 expected 和 actual flags 手工设为同一个值作为唯一测试。
2. 实施 A 的有限 helper；前后 fstat/readlink 与两类 flags 必须稳定。F_GETFD 非法/未知位、异常、超时、路径/inode/FD 变化均拒绝。审计 raw F_GETFL、raw F_GETFD、映射后 flags、FD tuple、sampler PID/start tick；不记录秘密。所有新增采集/审计服从原 deadline、frame 和总量预算。
3. 覆盖 CLOEXEC 开/关、readonly、其他 status bits 原样保留、错误地混入 descriptor bit、同 PID 未绑定 FD、不同 PID/start、target/device/inode 变化、write flags、获取失败和不稳定值；继续回放本次两组原始 FD/fdinfo 字节，显式区分真实字段和合成 envelope。
4. 完整载体回归、旧失败/拒绝用例、完整私有副本与实际打包程序重绑，检查 argv/frame/原有 120/120/240/300 秒预算。逐字节核对 Source、Go、kit25、bundle0002 原有记录；不重建 kit 或修改正式准入。
5. 完成独立规格/代码评审、归属/数据流检查和封存，再形成具体的新执行路径、grant 输入和失败即停操作清单。只有该候选完成后才提交真实执行的最终确认。

## 不变量与完成口径

- 原保护根、对象 inode、FD 全字段和 sampler PID/start tick 仍由原 policy 精确比较；其他进程不能借此成为自持 FD。
- 不改变 Docker/bridge/权限/网络、PG、容量、quota、Trust、Start 或 Worker 配置。
- 不进入 guest、不读取凭据、不 Stop/rename/复制现场对象、不启动环境或调用模型；不消费或复用旧 grant。
- 离线通过仅意味着新维护候选可审阅。仍须新的真实窗口逐项证明归属、Stop/保留、Engine 部署和条件首启；Task→报告→页面闭环尚未执行，不能承诺下一次必然全通过。

本次已完成设计自审：没有未决占位；修改只在 expected 自持 FD 采集处；没有放宽 policy 的 flags 比较；历史记录与未来 candidate 分离；没有从修订设计推导真实执行授权。

接受消息：`call_6670c1b18fdd4a408a0b6ca4977f411d`，回答“接受 A，完成整片离线修订（推荐）”。原被接受候选逐字节保存在新 phase 的 `approved-design.md`，原失败 phase 保持封存。
