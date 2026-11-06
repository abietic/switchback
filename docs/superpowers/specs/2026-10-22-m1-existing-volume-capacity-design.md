# M1 复用现有卷与虚拟磁盘的容量保证修订

_2026-10-22 · 用户已明确复用当前存储、无外置盘、不改主盘分区。本文是待审阅的保证修订，未改变源码或运行门禁。_

---

## 🎯 结论与纠偏

采用现有 `/Volumes/SwitchbackCore` APFS 卷和现有 Lima/VZ 的 file-backed 虚拟磁盘。M1 体验闭环不需要外置设备或新的物理分区。此前的外置盘建议源于把“独立宿主物理安全证明”保留成了启动前置；它超出了当前本机体验目标应承担的复杂度。本轮用户的直接约束取代此前外置设备设计方向。

当前实现确实把该要求写在保证常量中：`host_backing_safety_required=true`，并用 exact bytes/digest 回归固定。调整应使用显式新保证，而不是修改原定义或让旧证据自动获得更弱语义。[当前定义与测试](/path/to/switchback/internal/platform/config/runtime_capacity_guarantee.go:9)。

Lima VZ 支持把普通镜像文件作为可写 block device；现有配置与10月21日只读事实已对应到24 GiB system disk、64 GiB data disk和112 GiB卷限额。再套一层虚拟镜像仍使用同一个宿主文件系统，不能补出此前那条独立物理证明，也没有必要为此迁移数据。[^vz][^apfs]

方案比较已收敛：复用当前虚拟磁盘可保留已有 Linux/XFS/容器运行模型；新增嵌套镜像会增加一层迁移和容量管理，不能解决被移除的证明要求。因此选前者，不再设计外置盘或主盘分区变更。

## 📋 保留的行为与明确减少的承诺

| 项目 | 新 M1 保证 |
| --- | --- |
| 存储形态 | 复用当前APFS卷与24/64 GiB虚拟磁盘；不要求独立physical store |
| 宿主卷 | 资格验收确认实际卷UUID、container、quota/reserve、镜像和受管写路径归属；当前112 GiB配置只在实际重验匹配后使用 |
| 宿主物理承诺 | 不要求证明共享container全部未来metadata/COW/快照成本的独立上界；不承诺所有情况下宿主都不受空间耗尽影响 |
| guest与Worker | 原Linux VM隔离、XFS project byte quota、防属性修改边界、cgroup CPU/memory/PIDs/swap、固定Worker资源全部保留 |
| 容量与授权 | 保留80% aggregate、resident记账、30秒新鲜签名样本、原PG reserve/consume、exact-CID quota及单次Start |
| 失败 | EDQUOT/ENOSPC、PG不可写、未知effect仍沿原失败合同收敛；不自动退款、重建或重复Start |
| 生命周期 | 保留A：资格验收后冻结，维护前先停准入并由原owner收敛；无新增宿主常驻witness |

Apple 对APFS卷quota/reserve的公开语义可用于这种卷级限额合同；本方案明确接受共享container，不能称为已证明独立物理隔离。guest及宿主空间仍可能耗尽。这是本次需要审阅的实际保证变化，不是一个隐藏的allow开关。[^apfs]

## 🔏 新保证与旧证据隔离

新增服务端固定 `m1_shared_volume_capacity_v1`、revision=1。以下UTF-8原字节构成规范定义；不含换行，长度482 bytes，digest=`sha256:31b0caa11d3ff6cf95631815a0879771ffce565c735f6fd5f6ecab58638dcb01`：

```json
{"id":"m1_shared_volume_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":false,"host_storage_boundary":"apfs_volume_quota_and_reserve_v1","host_future_container_metadata_upper_bound":false,"authority":"original_pg_capacity_owner"}
```

`host_backing_safety_required=false` 取消旧定义的独立物理证明义务；`host_storage_boundary` 明确保留实际APFS卷限额与reserve检查。不能据false省略卷身份、配置、路径归属或native执限验证。

新增 `M1SharedVolumeCapacityGuarantee()` 和 `RuntimeCapacityGuaranteeDefinitionFor(ref RuntimeCapacityGuarantee) ([]byte, error)`；返回值复制静态定义，不公开可变注册入口。旧无参数definition getter保持原语义。

旧 `m1_accounted_capacity_v1` 的367-byte定义、digest、getter和原语义保持。catalog只识别两个exact ref，拒绝未知/错版本/错digest；这不表示两种资格证据互相兼容。旧强保证仍不能使用新卷级收据放行，旧nil-policy仍只保留legacy语义。

新manifest/Trust明确携带新ref，原签名与IPC继续经ManifestDigest绑定。PG仍按exact Trust不可热替换；已有pool不同则identity drift，不清旧账、不改epoch、不静默迁移。raw v1/v2格式、签名domain、PG schema和Worker资源数值不改。

## 🧩 资格与生产组装的最小调整

沿用[已审阅的共享验证架构](/path/to/switchback/docs/superpowers/specs/2026-10-21-m1-production-deployment-verifier-design.md)：安全固定叶子读取、候选无环绑定、配置Recheck、publisher/Backend角色分离和Backend先以capacity stale组装的bootstrap保留。只替换其中对新保证的宿主证明义务。

新保证的host receipt记录卷级资格，schema固定为 `switchback.capacity.host_volume_receipt.v1`、kind=`apfs_volume_quota_and_reserve_v1`，仍使用原 `host-boundary-receipt.json` 叶子并绑定candidate fingerprint；共同窗口/来源/raw事实约束沿用既有共享资格设计。纯判定必须验证：

1. 原始 `diskutil info/apfs list` 观测前后卷UUID、container UUID、挂载点及physical-store关联一致，quota/reserve精确匹配本候选。当前M1候选要求两者均为 `120259084288` bytes，0、缺失或漂移均拒绝，不自动设置或修复。原始输出有界且身份不能由目录名猜测。
2. system/data images、Lima状态、hostagent日志、已识别的VZ helper、缓存与受管临时/转换路径属于资格清单；source/binary/config与收据绑定。允许A冻结合同下明确禁止的维护操作，不要求证明所有未来内核或OS间接写入成本。
3. 不把 `diskutil info.FreeSpace=0` 或 reserve对应的CapacityInUse当作卷已满；不将当前free值当永久容量。保留实际stat/分配计数单位和raw输出，不用合成available补值。
4. 卷级收据只在新GuaranteeRef下适用。缺失、不一致、unknown、读取漂移或当前角色不匹配均拒绝；旧namespace packet、占位JSON或operator填的ready都不能代替。

host资格仍由一次性受信任部署采集产生，不增加宿主常驻进程。上述机制记录配置与受管路径范围，不执行112 GiB填盘实验，也不宣称APFS共享元数据已通过完整物理论证。

native资格、当前CollectPool/mapper和原created-layer检查保留。Backend仍只有CHOWN/DAC_OVERRIDE/FOWNER，不接host PID namespace；publisher保留已批准的专用双能力。资格失败不能被设置环境变量、CLI或测试fixture旁路。

当前只读调查是机制线索，不自动成为新资格收据。真实候选要重新绑定当时的身份和配置；旧一次性窗口不重用。具备新ref本身不产生capacity ready或执行授权。

## 🛠️ 整片源码交付范围

审阅后按一个完整实现计划交付合同、资格校验和组装回归，不再把外置设备或主盘分区作为前置：

| 位置 | 具体职责 |
| --- | --- |
| config保证定义及测试 | 新增getter/按ref取定义，Validate识别两个exact ref；旧定义/golden字节保持 |
| capacitysnapshot资格module及测试 | 新卷级receipt判定、候选/文件绑定；强保证与卷级证据不得串用；两种明确已知保证均保留accounted inode语义 |
| capacitypublisher与localproduct组装及测试 | 使用共享资格结果和各自role检查；真实样本前保持stale；拒绝不完整资格且不读key/reserve/Start |
| 原PG/created-layer/mapper回归 | 新ref贯穿原Trust、reservation和StartSnapshot；错ref/错manifest/过期/并发/unknown effect仍由原owner拒绝 |
| 进度与验收说明 | 旧C06独立物理证明记为新保证下不适用，不能记为物理证明通过；新增卷级资格待实际验收 |

不存在 `skip_physical_verification` 或“开发环境直接ready”。原两个unavailable入口只有在完整新资格和当前角色验证实现后才能对合法新候选成功，不能靠新增catalog常量提前返回nil。

## ✅ 验证与后续顺序

离线验证须覆盖：新定义独立digest、旧367-byte兼容；两种ref与receipt串换拒绝；缺失/错卷/错quota/错path及读取漂移拒绝；无首样本也可组装stale且零授权；native失败、旧签名/manifest、PG原子预留和exact-CID单次Start拒绝路径；相关平台编译。不能只用测试数量代替行为证明。

之后依次完成具体候选的卷级/native验收、真实签名容量发布与原PG消费、Create→quota→Start，最终回到“任务→正式报告→页面接受→同Task观测”。真实PG、VM/服务启停、key/Trust/journal初始化及provider调用仍遵守现有具体操作授权；本轮没有新增此类执行。

本轮产物为本修订及变更依据；没有写实现、改主盘、准备外置盘、格式化、迁移或启动环境。OpenSpec 6.5/8.4/9.3保持未完成。下一步审阅只针对上面明确的共享卷保证范围，然后连续推进整片源码与离线验证。

[^vz]: Lima v2.2.1，[VZ attachDisks](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/driver/vz/vm_darwin.go#L518-L593)，system和additional disk采用file-backed attachment。
[^apfs]: Apple，[APFS卷与空间选项](https://support.apple.com/guide/disk-utility/add-delete-or-erase-apfs-volumes-dskua9e6a110/mac)，同container空间共享，quota/reserve为卷级配置。
