# M1 owner 的 ExecStop 数组只读证明修订

状态：具体修订设计，待用户审阅。本轮真实窗口已在T1失败并停止；本文只提出下一片离线实施范围，不授权重新进入guest、修改现场对象或重跑已消费窗口。

## 当前证据与目标

封存73bf1d6418581472f7cbd16fba36b15c008e653038df2b226a94352f17c09f6e的载体已执行一次。授权、副本、宿主完整输入、VM Running与observer只读核验通过。T1首次OwnerSampler._manager读取containerd时失败；宿主表现为maintenance stdout incomplete EOF，guest原始错误为ValueError: show property coverage。

实际show命令请求50个属性，exit0、无timeout/truncation、stderr为空，却只返回48行，缺少ExecStop和ExecStopPost。stdout原始1175B，SHA256为e69586b0d3201b42aa48b629461ecdf94f66cabc16248a04c825f770b8d97272。1061个保存帧还原出完整10482683B stdout，其摘要与transport原记录一致；94个audit event全部完整。最后一个事件明确记录maintenance-failed/show property coverage。

本次尚未发出任何Stop、保留移动或reload，未进行新Engine部署、PG/资格首启、凭据读取或provider调用。此结论来自执行顺序、完成阶段与已保存完整命令/事件；不声称失败后重新观察过现场。

本地replay_saved_manager.py通过原OwnerSampler._manager和原contract parser重放保存响应，1项测试在约0.77秒内稳定报相同ValueError。只使用已保存命令响应，未选择native IO。原始缺字段响应保持原样，不补造空值。

目标是取得原合同已经要求的两项空数组事实，保留完整50属性和所有Stop安全判断。缺失属性、未知类型与已证明空数组必须保持不同语义。

## 路线比较

| 路线 | 内容 | 取舍 |
| --- | --- | --- |
| A，推荐 | 其余48项沿用现有systemctl文本查询；ExecStop/ExecStopPost改为固定D-Bus Properties.Get，严格验证variant中的数组类型和元素数为0 | 改动集中在两项读取，保持OwnerObservation/manager DTO与原安全策略；新增有界只读命令依赖 |
| B | 把现有50项全部改为按固定interface/property读取的typed D-Bus采集与mapper | 更统一，但扩大ABI、类型映射和回归范围，也增加命令与时间预算压力；当前不采用 |

不采用“缺字段补空值”。仅加--all同样不足：systemd v255的Exec结构数组printer只在逐元素循环中输出，空数组没有独立输出分支。该源码解释与本次缺两行的现象一致，但不能证明当前guest这两项确实存在且为空；新typed读取正是补这份事实。

## A的固定读取合同

只处理当次完整loaded catalogue已经列出的两个固定unit。不得通过GetUnit/LoadUnit、show或新查询加载absent对象。unit到object path为封闭映射：

- switchback-m1-containerd.service → /org/freedesktop/systemd1/unit/switchback_2dm1_2dcontainerd_2eservice
- switchback-m1-engine.service → /org/freedesktop/systemd1/unit/switchback_2dm1_2dengine_2eservice

现有systemctl show属性选择器精确减去ExecStop/ExecStopPost；其余48项仍要求完整、无重复、无未知字段。随后在同一个观测样本内，对每个loaded unit依次读取ExecStop和ExecStopPost。每个读取使用固定argv形状：

```text
/usr/bin/busctl --system --no-pager --auto-start=no --allow-interactive-authorization=no --timeout=2s call org.freedesktop.systemd1 <固定unit object path> org.freedesktop.DBus.Properties Get ss org.freedesktop.systemd1.Service <ExecStop或ExecStopPost>
```

使用call路径明确关闭自动激活与交互授权，不使用GetAll、Environment、任意目标/interface/property或远程bus选择器。只读Get不修改manager配置。两个参数位置仅从上述封闭映射和两个固定property枚举产生。

采用busctl非JSON的typed命令输出。成功空数组的唯一可接受token序列为：

```text
v a(sasbttttuii) 0
```

严格检查UTF-8、完整单条输出、variant类型、精确数组signature和0个元素；只允许末尾一个LF及token间ASCII单空格，不把字符串"0"、空输出、缺失属性、其他类型或非零元素解释为空。命令非零、stderr/输出约束失败、timeout、truncation、未知格式一律终止。原Source的命令结果捕获与失败规则保持；单命令stdout/stderr仍不超过16KiB。

只有两项typed Get均给出上述事实，才在现有manager DTO中写入ExecStop=""、ExecStopPost=""。DTO、50项最终closed schema、manager_effective匹配、Stop传播/反向依赖、MainPID/boot/start/exe/cgroup、完整process/FD/maps、restoration、六→二→零角色及所有T2–T8门禁均不改。即使文本show意外出现这两个字段，也不替代typed证明。

每个Get保留固定argv、原始stdout/bytes/hash和类型/计数结论，使用原bounded audit流。每次调用前检查原maintenance绝对deadline；外层bounded runner仍受剩余deadline约束，内部DBus超时最多2秒，不续期。原maintenance120、Engine120、qualification240、barrier300、总7200、frame/event/audit/argv上限均保留。真实完整窗口性能与audit容量仍须现场检验，不从本次初始片段推导成功保证。

## 离线实施与验证范围

在新私有phase `.scratch/runtime-m1-owner-exec-array-1028` 实施owner_native窄修订和canonical/mirror重绑；原失败执行副本、原始帧、旧seal、Source/templates/Go/ELF、kit23与bundle原样保留。原消费者和策略不增加豁免。新候选固定execution路径为 `.scratch/runtime-m1-owner-exec-array-execution-1028-01/carrier`，只允许独占创建；不覆盖runtime-m1-owned-engine-transition-execution-next，也不复用旧grant/claim。

必需回归：原48行响应重放仍不能单独授权；真实_manager调用接线在48项文本加两份明确synthetic typed空数组时完成原50字段；任意一项typed缺失/未知/非空/变型/多帧/超时/非零立即拒绝，无Stop或下一步动作。覆盖两unit与部分/全部unloaded，确保absent无show/Get；保留所有完整manager/Stop策略负例。

通过固定官方v255实现的受控格式fixture或同等可复现的纯离线fixture验证variant/array序列化，不把手写正例冒充guest实际输出。除上述两项外不修订getter与mapper合同。重新生成所有module/Source/hash/argv绑定，独立decoder与确定性核验、覆盖回归、独立复审后封存。新的实际窗口须在完整候选可审阅后另行授权。

## 一手依据

- [systemctl v255手册](https://raw.githubusercontent.com/systemd/systemd/v255/man/systemctl.xml)，show段说明默认空属性输出规则。
- [systemctl-show.c v255](https://raw.githubusercontent.com/systemd/systemd/v255/src/systemctl/systemctl-show.c)，1257–1309行的Exec结构数组循环没有空数组输出分支；因此不能仅凭--all作修复承诺。
- [busctl.c v255](https://raw.githubusercontent.com/systemd/systemd/v255/src/busctl/busctl.c)，format_cmdline输出variant signature及数组计数，call显式应用auto-start/interactive选项；不采用忽略这些选项的get-property便捷路径。
- [busctl v255手册](https://raw.githubusercontent.com/systemd/systemd/v255/man/busctl.xml)，call、参数格式、timeout与自动激活/交互授权选项。

自检：新目标/属性/方法/格式/失败行为和预算均封闭，没有TODO或隐含恢复；当前guest实际数组内容与busctl可用性仍未知，未来仅由新窗口中的typed Get建立，不靠历史缺行推断。本设计批准前不进入修订实施，也不更改任何当前封存输入。
