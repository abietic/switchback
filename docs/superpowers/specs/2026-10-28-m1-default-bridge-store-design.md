# M1 默认 bridge 存储合同修订

状态：待用户审阅。本轮真实窗口已在 T1 失败并停止。本文定义下一片完整离线交付；不授权进入 guest、重跑窗口、Stop、移动现场对象或启动环境。

## 目标、证据与成功标准

目标是消除“正式配置启用默认 bridge，但恢复分类仅允许 host/none”的确定性矛盾。保留当前网络配置，由验证器识别有限的、无 endpoint 的默认桥记录，不通过清理网络数据库规避验证。

本次四个 ExecStop/ExecStopPost typed Get 均真实通过。随后 Source `_network_metadata` 拒绝 `docker/network/v1.0/bridge/<id>/`。原始完整 tree 有7条记录：host/none/bridge 三个 network、三个0计数以及一个 bridge driver。只去掉 driver 键仍因 bridge network 的 IPAM schema 失败；去掉全部桥记录后原 host/none 校验通过。此诊断已在本机使用保留字节复现。API 空业务核验尚未开始；Count=0 与名字本身不授予维护权限。

Source 文件、原候选和本次失败回执保持原样。原始 bbolt 文件仅保存前4096B及完整解码 tree，不能称作完整 raw DB 回放。新的端到端离线夹具必须明确区分真实字段字节与合成 bbolt envelope、合成 syscall/生命周期状态。

离线完成标准：新版规则处理完整7条真实 tree，旧规则仍拒绝该 tree；新增 bbolt/classifier/scanner/preflight/owner 的完整路径测试与拒绝矩阵；停止前、停止后、重新部署预检使用同一套新规则；重建完整候选与载体并经独立复审封存。真实成功仍须后续新窗口证明，不能由测试数量或候选生成推断。

## 路线与推荐

| 路线 | 做法 | 取舍 |
| --- | --- | --- |
| **A，推荐：版本化默认桥分类** | 新增 network format v2 与 store inventory v4；严格解析默认桥 driver/network/IPAM，并保持所有空业务、归属和 Start 门禁 | 修订集中在现有扫描边界；须更新正式 Source 与完整候选，避免维护通过后再次被正式预检拒绝 |
| B：在维护时原样搬走网络数据库 | 证明无业务后停止 Engine，把数据库移到 data-root 外再从空库启动 | 新增现场数据迁移动作和恢复问题；掩盖正常默认桥与扫描规则不一致，当前不采用 |
| C：关闭默认 bridge | 修改 daemon 网络配置，重新验证既有网络、NAT和部署合同 | 改动网络行为超出当前根因；也不能自动消除当前持久化记录，当前不采用 |

A 不增加 capability、常驻进程、网络过滤、磁盘分区或存储迁移，也不改变 Worker trusted_full_network_v1、PG/Trust/quota/Start 或容量保证。现有 engine/preflight v2 回执协议保持；修订仅改变其内部 store inventory 所允许的新明确版本。

## 版本与模块边界

保留 `_network_metadata`、`_classify` 及 inventory v1/v2/v3 的旧格式验证语义。新增独立 `_network_metadata_v2`，只由当前 bound scanner 的新分类分支使用；其他路径仍调用既有分类器。不增加可由客户端传入的宽松模式。

新 bound scanner 发出 `switchback.engine.store_inventory.v4`，所有 entry 使用 `m1-engine-store-v4`。纯 host/none 子集沿用 classification `docker_network_metadata` 和 network format v1；完整默认桥集合使用新 classification `docker_default_bridge_metadata`、format `docker-29.9.0-network-bbolt-v2`，路径仍仅为固定local-kv.db。v1/v2/v3 不接纳新 classification 或新 format。

`validate_store_inventory` 明确新增 v4 分支。v4 继承 v3 的两固定 backing device、两个空 plugin 子目录、完整父目录/同FS/身份/竞态、扫描总量和零 restoration refs 约束，不增加第三类设备或新目录豁免。旧 DTO 兼容回归保留；带新 classification 的默认桥记录不能仅改 schema/rule/format 三个字符串降级到旧版。DTO校验不单独证明其引用的原始字节，事实来源和Source绑定仍由完整实际scanner及原封装合同负责。

`_validate_preflight` 对 v4 同样强制 store.backing_filesystem 与 filesystem 相等，覆盖 version dispatch 全部使用点。正式 receipt/Go 消费者如只持有 Source/receipt digest，继续原合同；不得为适配新候选放宽任何 Go 就绪或 Start 判断。

## 新网络分类的封闭支持范围

保持 bbolt 的最高 committed transaction、双 meta/checksum、完整 key 遍历、重复/隐藏/不可达处理、文件/record/depth/字节限制及 no-write 约束。新 helper 接受的结构仅有：原严格 host/none 子集，或下面完整默认桥集合。

默认桥集合必须恰有7条：host/none/bridge 各一个 network、各一个 endpoint_count，以及与 bridge 同 ID 的一个 bridge driver。三个 ID 为不同的64位小写hex；ID、名称、driver、key及count覆盖一一匹配。未知 bucket/key、endpoint、sandbox、第四个网络、自定义桥或任何非零 count 均拒绝。每个 value 仍为正的8字节LE revision加完整JSON；JSON拒绝重复字段和尾随内容。

host/none 沿用原字段、类型、local/persist、空选项/labels和禁用行为校验。bridge network 的字段集合精确为现场 host/none 通用集合加 `ipamV4Config`、`ipamV4Info`：name/networkType=bridge、scope=local、addrSpace=LocalDefault、ipamType=default；persist和IPv4为true，IPv6/internal/attachable/inDelete/ingress/configOnly/skipGwAlloc均false；configFrom/loadBalancerIP为空，loadBalancerMode=NAT，labels/ipamOptions为null。created只验证既有严格时间格式，不把现场时间硬编码为权限。

generic 精确包含两个布尔IPv4/IPv6键和一个driver选项字典。选项仅为现场固定配置：default_bridge、enable_icc、enable_ip_masquerade均为字符串true，host_binding_ipv4=0.0.0.0、bridge.name=docker0、driver.mtu=1500；拒绝新增、遗漏、类型混淆或其他值。

bridge driver 字段集合精确匹配本次已保存的21项（见失败回放JSON），仅 ID 与 AddressIPv4 是受验证的动态值，其余值按下列策略固定：BridgeName=docker0、BridgeIfaceCreator为整数1、Mtu为整数1500；DefaultBridge/EnableICC/EnableIPMasquerade/EnableIPv4为true；EnableIPv6/InhibitIPv4/Internal为false；DefaultBindingIP=0.0.0.0；ContainerIfacePrefix/GwModeIPv4/GwModeIPv6/TrustedHostInterfaces为空；DefaultGatewayIPv4/DefaultGatewayIPv6/HostIP/HostIPv6为字符串`<nil>`。bool不能代替整数。

嵌套JSON逐层有界解析，不接受任意深度或任意字段：

- ipamV4Config 必须是JSON字符串，解码为一个元素的数组；该元素精确为 PreferredPool/SubPool/Gateway三个空字符串和空 AuxAddresses。
- ipamV4Info 必须是JSON字符串，解码为一个元素的数组；该元素仅有 IPAMData字符串与PoolID字符串。IPAMData再解码为精确 AddressSpace/AuxAddresses/Gateway/Pool 四字段，AddressSpace=LocalDefault、AuxAddresses为空。
- 每个嵌套字符串至多8192B。Pool为canonical IPv4网络且全部位于RFC1918范围，Gateway为同prefix、同网络且非network/broadcast的IPv4 interface。PoolID必须精确等于LocalDefault加斜线及canonical Pool；driver.AddressIPv4必须与Gateway完全一致。无IPv6、aux地址、preferred pool、subpool或自定义gateway例外。

记录中的真实ID、revision、创建时间与合法IPv4分配可以变化；它们必须满足上述关联，不能凭路径名或Count=0单独通过。该支持范围是M1保守子集，不宣称支持Docker全部默认桥变体或所有持久化ABI。

## 三条扫描路径与维护安全

新私有 owner carrier 使用新 Source scanner，对初始 owned_pair、containerd_only、quiet 阶段均执行完整 store 扫描；停止后的持久化默认桥记录不会被解释成需要启动Engine才能验证的业务对象。无Engine时仍要求完整零恢复分类、process/FD/maps、挂载与监听检查。

Engine活动时，现有完整归属双样本和固定 Unix API `/info`、`/version`、容器/volume/plugin/network 空业务盘点保持。新网络分类只消除有限记录格式拒绝，不跳过API，不产生Stop authority。全部原始文件/内核/manager/socket/停止传播与依赖检查，以及六→二→零角色策略原样保留。

正式新部署预检使用相同v4 helper和validator，不另写私有豁免，也不在data-root内搬走或删除数据库。旧安装unit/config内容未改时，继续使用既有kit21归属依据；新的Source/candidate identity独立绑定，不把新source根冒充旧安装来源。

原维护120秒、Engine120秒、资格240秒、barrier300秒、总7200秒以及argv/frame/event/audit/内存边界均保持。新增解析必须在原 check/deadline 内有界执行。

## 完整离线交付与回归

一次交付完成 Source v4、真实tree回放、独立bbolt封装夹具、完整carrier及新kit/bundle。kit23、旧carrier、两个已消费失败窗口及所有旧seal原样保留；新candidate使用新编号和独占路径，不原位改旧候选。

必须验证以下四层，不能只新增helper单元测试：

1. **语义与版本**：真实7条tree通过v2，旧helper仍拒绝；旧host/none与空集合兼容；每一个关联/字段/类型、缺失driver/count、孤立driver、重复name/ID、额外网络/endpoint/sandbox、非零计数、嵌套JSON与IPAM不一致全部拒绝。v1/v2/v3 DTO与v4降级拒绝保留。
2. **实际分类与scanner**：独立生成合法bbolt envelope装入真实record字节，走实际decoder→新分类器→完整scanner→v4 DTO校验；同FS/device/两plugin空目录和本次之前已遇到的状态形态一起纳入完整合成夹具。该组合明确标注为合成，不冒充同次现场完整快照。隐藏活跃记录、损坏最高tx、stat/内容/目录竞态继续拒绝。
3. **完整生命周期**：同一个合法默认桥store分别经过owner初始证明、API空业务门禁、Engine停止后、两daemon停止后及Source正式新部署预检。任一步拒绝时没有下一步Stop/移动；API有业务时不能因为tree合法而通过。保留typed数组四次读取与全部既有Stop/持有FD/原样保留故障回归。
4. **封装与发布**：完整新的Source根/模板/manifest/ELF消费者绑定、carrier canonical/mirror、实际argv长度和decode字节、确定性、全树完整性、独立复审、最终seal。检查新的正式预检确实使用v4，不能只在维护脚本内替换函数。

离线流程不读取凭据，不使用guest、Engine、PG、provider或真实D-Bus。不增加环境隔离夹具。封存后给出一个具体的新完整真实操作清单，再由用户决定是否授权；旧失败grant不可续跑，失败后不做guest读回。

## 依据、风险与自检

具体记录依据：本次 execution analysis 的 `network-replay-result.json`（audit2036，7个原始value的逐项哈希），以及当前 `deploy/experience/engine/store.py`、`contract.py`、`test_store.py`、`test_contract.py`。失败报告为 `.scratch/runtime-m1-owner-exec-array-execution-1028-01/analysis/failure-report.md`。

[Docker官方bridge文档](https://docs.docker.com/engine/network/drivers/bridge/)支持daemon自动创建默认bridge及docker0配置这一通用行为；不用于证明固定29.9.0私有字段ABI。此次尝试获取精确tag源文件未成功，因此不引用未取得的源码解释具体字段。新支持边界以本次完整已提交tree与明确M1策略为准。

风险：当前完整扫描在network处终止，后续现场状态尚未被本次完整证明；新回归能消除这个确定性矛盾，不能承诺下一次所有环境门禁一次通过。未取得的API空业务事实仍必须在未来真实窗口中建立。最终目标仍为Core真实首启和Task→正式报告→页面接受→同任务观测关联，完成这些前不宣称实跑成功或开始大范围精简。

自检：路线、版本隔离、字段与关联、三条实际扫描路径、拒绝边界、预算和全候选交付均明确。当前只提交设计，无新的生产行为、权限或环境动作。
