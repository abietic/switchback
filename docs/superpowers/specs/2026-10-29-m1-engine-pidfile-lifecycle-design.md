# Engine PID 文件生命周期最小修订候选

状态：用户于2026-10-29接受A，授权整片离线修订。当前真实窗口已经失败，不授权重跑、guest读回、服务恢复或真实执行。

## 目标、事实与边界

让私有维护载体精确识别目标 Engine 在 Stop 时移除其固定 PID 文件的生命周期，同时保留其他文件、完整身份、阶段稳定性与全部准入门禁。当前最近已验证状态为 containerd_only，后续新候选必须从新的现场证明出发，不沿用旧 proof 或 continuation。

本次自持 FD 修订已通过真实验证，归属/API 空业务已通过，Engine Stop一次且得到两份稳定 containerd_only 样本；仅常规文件 docker.pid 的消失触发原 continuity 的 socket-only 条件。文件 hash/size 匹配已证明 daemon PID，路径来自 exact bound unit 的 --pidfile，上游相同 commit 明确 defer remove。详见本目录 README/replay-result/upstream note。

## 两种实现路线

**A（推荐）：复用现有 Node hash/size 与已验证 daemon identity，增加精确 PID 文件角色。** 在第一次 Stop 之前的连续性检查中，若初始 owned_pair 含该固定文件，先验证它是普通单链接 root:root 0644、与 Runtime根同设备的确切节点，size/SHA必须匹配初始已验证 Engine PID 的规范十进制字节；不接受另一个路径、inode替换、非规范内容或未知类型。初始与fresh样本仍按原NODE全字段精确比较。只有本窗口已Stop目标Engine、且当前已通过原containerd_only/quiet owner proof时，才允许这一已绑定PID文件消失。仍存在的节点继续完整比较；其他常规文件或目录消失仍拒绝，已有socket规则保持。

这一角色只改变私有维护 continuity 及直接回归/装配证明，不修改 Source/Go/DTO/collector、生产配置或权限。缺少 Engine live identity 时不能从历史basis补出消失授权；初始containerd_only/quiet按其实际节点集合处理，未出现的PID文件不制造事实，存在且未消失的内容仍原样保留。任何缺失/不一致的所需证据都拒绝。

**B：新增独立的PID文件原字节DTO和collector接线。** 直接记录有界原始内容并生成角色证据，可增加排障细节，但会扩展观测协议、所有样本mapper和封装链。当前既有hash/size足以绑定已知PID的唯一规范字节，A的改动和兼容成本更低。

## 一次完成的离线交付

1. 新独立phase，保留当前候选、grant和真实失败全部证据；正式记录被批准的private core差异，不能继续声称四core全部不变。
2. 先用本次保存的完整46→41节点集合和两个post样本，在原 `_continuity` 建立RED；manager等未保存为全量结构的字段明确合成。最小PID-only反例也保留。
3. 实施A，覆盖精确路径/PID/hash/size/owner/mode/nlink/device/inode、错误unit/阶段、未Stop、同路径换对象、其他文件/目录消失、PID文件保留或缺席等正反例；错误PID角色必须在Stop之前拒绝。
4. 在真实维护sequencer的fake边界覆盖 owned_pair→Engine Stop→containerd_only→containerd Stop→quiet→preserve/reload，以及本次之后可能的初始containerd_only和quiet；禁止对unloaded Engine发show/Get/Stop。基于当前已保存FS事实构造完整节点fixture，不再用只有socket的理想化fixture代替生命周期。
5. 复用原kit25/bundle0002；不改Source/Go/模板/权限/PG/quota/Trust/Start，不重建候选二进制。完成所有实际程序重绑/解码、私有与兼容回归、独立规格/代码及整片评审、全部旧输入保护和新封存。
6. 完成具体新执行路径、exact grant与操作清单后，真实执行另行最终确认。原120/120/240/300/7200秒、argv/frame/total限制、失败即停保持。

## 不变量与自审

- 不允许任意常规文件消失，不删除/过滤T1基线，不伪造socket类型，不从Stop exit0单独授予后续动作。
- 其他PID/start/FD/maps、空业务、恢复引用、全树/manager与阶段稳定性保持；未知状态拒绝。
- 本片仅源码/离线测试/装配/评审/封存，不进入guest、不读凭据、不Stop/Start/rename现场对象、不重试原window。
- 容器级或生产行为、现有Source保证版本和物理存储边界不变。
- 自审已核对范围、证据来源、前置PID身份检查与后置消失例外、初始containerd_only分支及未来授权分离；没有未决占位或隐含环境动作。实跑Core与产品闭环仍未通过，不能保证下一次无其他现场问题。

接受消息：call_be76c670f48b4c9583d76d8428a15e9a，回答“接受A，完成整片离线修订（推荐）”。被接受候选原文保存在本phase approved-design.md。
