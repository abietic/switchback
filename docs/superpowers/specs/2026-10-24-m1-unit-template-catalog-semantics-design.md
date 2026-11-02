# Engine unit 模板与运行实例的最小分类修正

状态：待用户接受；本文件只定义下一片源码与离线交付，不授权真实执行。kit15、本次失败回执和现行 Source 保持原样。

## 已取得的真实进展与新阻塞

用户 grant `call_5a0bafd3229744cc89e42ba4c16afe19` 的一次准备已完成：四份新kit15/bundle1的2340叶全部校验，五旧对象（含2448叶消费stage）no-replace保全，四个更早archive核对保持。随后唯一Engine120秒窗口实际1.234733792秒结束：host status/remote_effects unknown，guest preflight failed，未进入安装/启动step。没有镜像、新秘密、新PG/迁移、资格窗口、provider、VMStart、重试、续跑、失败后guest核查或远程Stop。

两条exact清单均exit0、完整EOF、无timeout/truncation：list-unit-files为19861 bytes/279行，list-units为21630 bytes/194行，去重311名称，含28个模板。上一片256KiB stdout修复已由这次真实结果覆盖；清单均超过旧16KiB，但完整返回。完成两份128进程FD inventory和listener观测不等于完整preflight验收。

新错误发生在第五个逐项properties请求：

```text
/usr/bin/systemctl show --no-pager --property=Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen apport-coredump-hook@.service
exit1, stdout0, stderr114:
Failed to get properties: Unit name apport-coredump-hook@.service is neither a valid invocation ID nor unit name.
```

当前 `_unit_catalog` 将安装文件和manager运行名称合并后无差别调用show。前4个普通unit完成，随后对未实例化模板发出运行属性查询而被systemd拒绝。原失败是有效fail-closed，不能忽略exit1让检查继续。

本次新host stage inode141571427、2425叶已保存完整快照。复制阶段新建的archive-unit-catalog-output-budget-1024-kit-0015-bundle-0001保留kit14四副本和2448叶stage；现在共五个既有archive。保存事实不替代下一窗口live重验。

## 一手依据与诊断边界

[systemctl官方手册](https://www.freedesktop.org/software/systemd/man/257/systemctl.html)明确区分：list-unit-files包含模板；list-units列manager中的unit，包括实例，未实例化模板不可运行且不会出现在其中。show提供unit运行属性；cat提供磁盘fragment/drop-ins，可能与manager当前理解不同。[systemd.unit官方说明](https://www.freedesktop.org/software/systemd/man/257/systemd.unit.html)定义`foo@.service`模板与`foo@bar.service`实例。

冻结packet与实际生产 `_unit_catalog` / `_Record.command` 的离线回放稳定得到相同nonzero错误；synthetic普通mask control通过。两份清单、前4项properties、模板exit1/stderr来自真实字节；未采集到的其余普通unit properties只能用明确标注的synthetic中性事实支持未来GREEN，不能称完整真实properties回放。没有修改生产函数或再次调用systemctl。

直接stderr和exit已定位命令类型错误；输出超限/timeout假设由本轮完整两清单和相应false标记排除，无需额外guest探测。

## 方案比较

A（推荐）：保留两份完整清单及来源，区分不可运行的模板定义与可查询的普通/已实例化unit。对模板保留显式原始分类观测，不调用show、不伪造运行字段；其他名称保持原完整properties/ExecStart检查。这个保证针对当前运行实例，不把模板文本解释为未来运行行为。

B：另用cat收集全部模板fragment/drop-ins并建立静态配置解释器。可额外检查模板定义，但需要处理多文件优先级、specifier和磁盘/manager差异，超出本次运行时清单错误的最小修复，不能冒充show等价事实。

C：为每个模板构造占位实例后show。会改变manager加载对象，并引入人为实例和新身份/清理问题，本次不采用。任何方案都不能吞掉全部show失败或按daemon名称过滤清单。

## 推荐A的精确执行契约

1. 两条完整清单命令及exact argv、256KiB stdout保持。分别保留`installed_names`与`loaded_names`，原始stdout先记录再解析；`all_names=installed_names|loaded_names`。原311样本仅为本次事实，上限仍512个去重名称；缺mask/坏行/溢出/非零/超时等仍拒绝。
2. 只将完整匹配`[^@]+@\.(service|socket)`的名称分类为未实例化模板；不按substring、basename或归一化参数分类。它必须出现在安装清单，且不得出现在完整manager清单；反例立即拒绝，不猜inactive。形如`foo@bar.service`的实例始终走原show路径。
3. 每个模板写一份独立`unit-catalog-template`原始观测，记录exact name与两份清单的来源归属。不给它构造MainPID、ActiveState、ExecStart或Listen，不把它追加为伪造inactive的other_units。模板名称仍在总集合、512上限和coverage观测中，不能静默丢弃。
4. 其余全部名称仍逐个读取原六项properties并记录`unit-catalog-item`，按现行Id/ExecStart/daemon name/mask/new unit规则校验。已实例化的改名dockerd/containerd/shim必须仍被识别和拒绝；所有非模板show的exit1、未知、timeout/truncated/cleanup失败保持原拒绝。
5. 默认mask与新专用unit都是普通unit，保留原精确coverage/独占判定；不创建占位实例、不调用cat/load/start/reload/reset/Stop，不增加外部探测命令或公共override。`_unit_catalog`对调用者仍返回原`(masks,others)`，不改receipt/schema/Go生产readback协议。
6. 这明确修正“每个安装文件都能取得运行properties”的旧假设。对每个可运行普通unit/实例的完整属性检查保留；模板只是定义，不能从缺失运行字段推导授权、当前无任何实例或未来不会启动。当前实例覆盖来自完整list-units与原进程/FD/listener闭环；全部原前提仍需通过后才准入。
7. 原stderr/其他stdout/step摘要16KiB、raw frame1MiB/累计64MiB、Engine120秒/资格240秒/barrier300秒、PID/start/flags/R/S、FD/listener、权限/资源以及PG/Trust/Start三门禁均保持。

## 完整源码与离线交付验收

- Source只修改既有guest.py、test_prepare.py、README.md及Go最终绑定测试的新bundle发现路径；不新增Engine源叶、不扩大bundle/readback白名单、不改生产权限或门禁。
- 保留本次311名称/28模板的完整实际清单和有效RED；源码测试输入自包含或明确fixture来源，运行测试不依赖live目标。回放限于已捕获字节；未捕获properties标注synthetic。
- 覆盖普通unit、service/socket模板、实例、模板与实例并存、同模板多个活跃/改名daemon实例、模板误入manager清单、来源缺失、非模板show失败、缺mask/坏行/513、两份raw与模板逐项保留。断言所有可查询名称properties调用完整，模板无任何运行字段伪造。
- 运行完整离线Engine/资格/共享helper/Go相关验证；不执行历史native fixtures或候选程序。旧失败、kit15和所有seal保持；不进入guest，不读取或修改live副本/stage/archive。
- 全部Source冻结后原builder一次生成下一个完整kit，canonical新bundle，核验1076Source/7模板、逐叶/双root/20格式/43构建收据/17逆输入/13null/4false与真实最终Go绑定PASS无SKIP；独立源码、交付和整片评审。
- 新具体切换清单绑定当前kit15四副本、2425叶unknown/failed stage及五历史archive，仅准备，不执行。真实窗口必须在新完整候选可审查后单独授权，不能重试或续跑本轮。

接受本设计只授权上述完整源码与离线交付；不授权任何guest/Engine/PG/provider执行或实际归档/复制/秘密初始化。当前Source与kit15并未应用此分类修正，也没有成功首启结论。
