# M1 systemctl 多值属性最小修正设计

状态：待用户接受；本片只完成失败诊断和设计，没有修改生产源码或重建候选。

## 真实结果与目标

用户明确授权剩余Engine部署与条件首启后，新单次入口已完成只读核验：VM已Running，四份kit16副本与六处archive完整匹配。原120秒Engine窗口实际1.600580125秒exit1，停在preflight，host status/remote_effects为unknown，guest failed。没有安装/服务启动step，也没有进入后续镜像、秘密、PG或资格首启。

两份完整unit清单仍为311个union名称；此次完成38项普通unit/实例properties，记录4个已遍历模板定义，随后第39次show查询`dm-event.socket`。该命令exit0，stdout130字节、stderr0、无timeout/truncation/error，原始哈希和owned cleanup均通过；其输出包含两个不同的Listen行和各一个Id/ActiveState/UnitFileState。当前`_properties`无条件拒绝重复key，因而抛出`duplicate/malformed systemd property`。此前模板修正已越过原报错，但不能据此前缀宣称完整preflight通过。

目标是正确保存本次完整多值观测，保持重复单值属性拒绝、完整unit/进程/listener检查和原权限/资源/PG/Trust/Start门禁。不能以跳过`dm-event.socket`、吞非零、截断输出或覆盖前值来通过。

## 选项与推荐

A（推荐）：在现有文本边界明确区分本次catalogue query的两个数组属性`Listen`与`ExecStart`，完整保留所有有序项，既有消费者仍取得字符串；单值属性和其他查询继续严格唯一。改动局部，无新依赖、协议或权限。

B：改用systemd D-Bus的typed数组读取，获得结构化值，但会新增依赖、调用与部署验证路径，扩大本次首启前置范围。后续确有更多systemd结构化消费需求时再评估。

只处理Listen会遗漏同一根因的ExecStart数组。上游systemd v255的show实现明确对Listen和Exec属性分别逐项输出同名key；因此A同时覆盖这两个已请求的数组属性。该上游版本用于契约核验，不声称已经验证本机guest版本，也没有在失败后进入guest读取版本。

## A的具体契约

- 保持`_properties`默认严格唯一。仅在`_IO.show`执行现有exact catalogue property tuple `Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen`时启用固定的`Listen`/`ExecStart`多值归并；其他show查询和直接默认parser调用不放宽。上下文选择必须来自服务端固定调用，不能来自输入字段。
- 多项按原顺序用单个换行分隔成字符串；不丢项、不去重、不取首项或末项。零项/单项的既有空值与缺席语义保留；重复项中任一项为空则拒绝，重复标量即使值相同也拒绝。所有非空原始value字节的UTF-8文本保持，命令raw帧仍是权威证据。
- `_unit_catalog`继续取得字符串形式的完整ExecStart并扫描所有命令项。位于第二项或后续项的dockerd/containerd/containerd-shim路径必须仍被识别，不能隐藏在第一项的普通命令之后。保留每个普通unit/实例完整show检查、28个模板的定义分类以及512名称上限。
- 安装后的专用Engine unit有效配置查询不属于catalogue tuple，其ExecStart仍须唯一，并继续原exact path/argv与生命周期/权限校验。新单次unit不能借多值归并扩大执行内容。
- 所有其他重复属性、缺`=`、畸形行和非零/超时/截断继续拒绝；不引入last-wins字典。两条清单stdout256KiB、其他stdout/stderr及摘要16KiB、frame1MiB/累计64MiB、120/240/300秒预算原样。

## 验证与整片离线交付

正确回归缝在真实`_IO.show`调用点。当前保存的`replay_show.py`以原始130字节输入走该调用：单Listen两个control和重复标量拒绝control通过，双Listen目标为RED。原默认parser回放也保留，修正后它继续严格拒绝重复key是预期；不得把它误报为应转GREEN的catalogue调用。

实施时完成：真实双Listen RED/GREEN；标量重复、上下文不匹配、空重复与畸形行拒绝；ExecStart多项保序与第二/末项危险daemon识别；自有unit多ExecStart继续拒绝；一份完整保存清单回放和原边界回归。现有数据包含39条真实show（38成功解析与1条目标输出），其余244个普通unit/实例properties未采集，若使用synthetic必须显式标记，不能称全量真实properties重放。

完成相关Engine/资格窗口/self-check/Go候选绑定验证后，通过原builder构建全新完整kit/bundle、逐叶封存、独立Source/交付/整片复审；保留kit16、原始回执、当前四副本、六处archive与新消费2514叶host stage。本片不进入guest、不操作现行副本或stage、不执行候选程序、不重跑actual；未来真实窗口需要具体新清单与授权。

## 原始证据与一手资料

- [本次原始回执与诊断](../../../.scratch/runtime-m1-unit-template-engine-window-execution-1024/README.md)
- [正确show调用点回放](../../../.scratch/runtime-m1-unit-template-engine-window-execution-1024/replay_show.py)
- [systemd v255 show源码：Listen逐项输出与Exec逐项输出](https://github.com/systemd/systemd/blob/v255/src/systemctl/systemctl-show.c#L1202-L1309)
- [systemd D-Bus Socket文档：Listen为数组](https://freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html#Socket%20Unit%20Objects)

## 自审与批准点

设计只收口已证明的数组属性边界及同类ExecStart序列化，不跳过unit或削弱daemon/listener检查，不修改权限、资源、PG/Trust/Start。本次失败即停授权已经消费，不能继续现场动作；下一步是接受A后整片源码与离线交付。设计不提交Git，保护既有巨大未提交工作树和冻结HEAD/index。
