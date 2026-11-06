# M1 Host recorder 接口最小修复设计

2026-10-23。待用户接受；本轮只完成取证与离线复现，未修改生产源码、重建候选或再次执行真实窗口。

## 问题、范围与成功标准

已按授权将 `/Volumes/SwitchbackCore` 卷根从0775改为0755，exact device/inode/owner验证通过；不递归、不改分区。之后只调用一次新的Engine窗口，Lima `list switchback-core --json` 退出0并报告Running4CPU/10GiB，随后宿主执行器以 `'_Host' object has no attribute 'raw'` 停止。没有进入guest、创建Engine stage、启动新服务、生成秘密或初始化PG。原回执status=unknown、remote_effects=none_attempted，完整保留。

实际拒绝原件SHA256：`78b7f262aeb85db26a5b11c56a0eb555565bc97e897dfe971402c7d2d2909f95`。取证目录：本轮停止与诊断 (private evidence retained)。当前源码 `prepare.py` SHA256为 `99265259872cf6f5478056ea638c4628076f4e3a606529e5eec9f8dd748a77e4`；当前源码、kit-0009与宿主复制件一致，不是导入了其他同名模块。这个接口缺口在Darwin修复前的封存baseline中也存在。

成功标准是：**真实 `_Host` 作为真实 `_bounded_run` 的recorder时，正常终态能保留raw观察及命令结果并返回；记录失败、超时、截断、非零退出和身份丢失仍按原规则拒绝。** 离线通过只交付新候选，不声称Engine、容量、PG或Start已验收。

## 根因与测试缺口

`prepare.py::_bounded_run` 在 `_wait_leader` 的回调中调用 `record.raw('owned-leader-before-reap', value)`。guest使用 `_Record`，有raw；host preflight传入 `_Host`，该类只有command/emit，缺少raw。因此正常Lima命令也在记录终态时失败。finally保留了exit0、原错误、Darwin组证明与reap；不能把exit0单独解释为准备成功。

旧ProcessBoundaryTests中 `host_transport=False` 使用带raw的SimpleNamespace，`host_transport=True`调用 `_Host.transport`，均没有覆盖“真实 `_Host` → `_bounded_run`”这一宿主preflight组合。新的回归必须在这个组合上证明修复，不能给测试对象临时增加raw或替换真实Host.emit来掩盖接口错误。

## 方案比较

| 方案 | 收益与代价 |
| --- | --- |
| **A：补齐 `_Host.raw`（推荐）** | 私有适配方法将kind与 `_wire(value)` 交给现有emit，沿用原帧/总量上限、内存观察及stage持久化；生产代码只改这一接口缺口。 |
| B：为 `_bounded_run` 增加单独raw回调参数 | 可以显式传入宿主/guest回调，但需修改共享签名及多个caller，增加两套recording接线；本次收益不足。 |

选择A。不增加新recorder框架、第二套监督器或公开协议；不使用hasattr后跳过记录、no-op回调、吞异常或扩大期限。

## A 的具体行为与边界

仅在 `deploy/experience/engine/prepare.py::_Host` 新增私有 `raw(kind, value)`，把 `{'kind': kind, 'value': _wire(value)}` 传给现有 `emit`。原kind与完整有界事实保留，不增加step字段或新schema。emit的帧上限、总量上限、超限留prefix/hash后拒绝，以及stage_owned前后持久化路径均原样复用。

不改变 `_bounded_run`、`_wait_leader`、Darwin ABI、fencing/signal/reap、首错优先级或120秒/240秒预算。raw记录失败仍作为原调用失败传播，finally继续保留命令结果和cleanup；不得改写为成功。原资格loader、Trust、raw-v1/v2、quota、PG预留、单次Start、资源/网络、capability与三个生产门禁均不变。ADR0059的Backend/Worker职责保持现状。

## 源码与离线验证

生产修改限定上述方法；回归位于现有 `deploy/experience/engine/test_prepare.py`。先加入真实caller组合回归，证明当前源码因同一个missing-raw错误失败，再实施A。测试仅替换OS/process/native边界，不能替换待验证的Host.raw/emit/command或 `_bounded_run`。至少覆盖：

1. **真实Host + 真实共享监督器**：固定成功命令输出及合法终态，raw观察与host-command-result均被保留，返回原stdout，无额外命令、deadline不延长。
2. **真实Host.preflight组合**：使用临时私有tiny kit/config与确定性假I/O，走原preflight到原 `_bounded_run` 再到VM几何/配置复查和新stage声明；不能用stubbed `_bounded_run` 让缺口再次漏过。
3. **记录边界**：binary事实经原_wire可序列化；无stage时只写内存观察，有owned stage时沿原exclusive0600文件路径留证；单帧/总量越界或持久化失败仍拒绝。
4. **失败组合**：原超时、输出截断、非零退出、未知exit、recording失败及cleanup错误不被新方法覆盖；原错误、partial输出和退出码分别保留。
5. **既有共享接线**：Engine现有全量离线回归及原qualification driver受影响回归通过；loader仍加载同一reviewed helper。Linux双架构只做受影响测试编译。本次不再运行三个已消费的本机native fixture，也不启动新隔离子进程场景。

当前离线诊断v2使用固定原始packet、真实Host与真实 `_bounded_run`，仅替换进程/selector/reader/cleanup边界；已返回RED_TARGET_BUG、exit1、exact错误、raw回调尝试1次、真实子进程0。最初诊断harness缺fileno而失败，已单独标为HARNESS_INVALID；原脚本和输出保留，不能当作目标回归证据。最终生产回归使用完整边界假I/O并替换掉诊断harness的简化callback。

## 完整交付与未来实跑

接受后一次完成源码、RED→GREEN与上述受影响离线检查，通过原builder生成新的独占完整kit及反向绑定bundle；核对全部源码/模板、可执行格式、原43份构建回执及manifest coverage，执行最终实际候选绑定回归、独立复审并封存。原kit-0009/bundle-0001、失败packet、claims、seals、两个publisher保护路径与未提交改动保留，无staging/commit。

现有host/guest固定副本仍绑定旧SourceRoot，不能原地补丁、覆盖或伪装为新候选。交付新候选时同时离线准备一份具体副本切换清单：仅在现有VM已Running且新固定归档目标为空、旧副本完整匹配其manifest时，先将旧两个固定副本分别改名保留到明确的新归档目录，再独占复制完整新候选；操作、路径和ownership在真实执行前单独核对并取得授权。本阶段不实施任何改名、复制、guest读取或启动。

本轮真实窗口已经失败，不能续跑。未来真实执行须使用封存后的新候选、清晰的副本切换动作和新的单次窗口授权；资格240秒窗口仍未消费。无需再次chmod卷根，当前0755保留。不得重跑旧fixture、启动VM、改分区、调用provider或付费模型；任何未来失败/unknown仍按约定停止。

## 设计自审与批准状态

上下文、精确复现、两个方案、最小生产改动、失败语义、真实caller回归及旧副本碰撞已明确。没有待定的资源、权限或兼容语义；没有新增运行权限。当前只完成诊断和此设计；等待用户接受后进入实现计划与整片离线交付，真实操作继续关闭。
