# M1 C06 宿主 backing 物理机制核实

日期：2026-10-21。范围：Apple 官方契约、Lima `v2.2.1` 官方源码及主代理新增的本机只读事实。原独立研究版本保存在本阶段 scratch。本文不证明现有部署已合格，不授权任何磁盘、服务或 VM 操作。

---

## 🎯 结论

**现有“共享 APFS container 内，专用卷 reserve=quota”路线仍未证明 C06。** Apple 明确给出卷可分配空间的 reserve/quota 语义，但本次核实没有找到将该卷诱发的全部 container 元数据、快照保留和 COW 事务开销完整归入该 quota 的官方契约。不能将“未找到保证”写成“quota 已知可绕过”，也不能反向推为全覆盖成立。[^apfs_volume][^apfs_reference]

最可能收口、且保留 `host_backing_safety_required=true` 的候选是：**真实固定分区上的专用 APFS container，或独立物理设备上的专用文件系统**，继续放置普通 raw 镜像。它用外层地址空间边界包住文件、APFS 元数据与快照增长，避免依赖卷级 quota 对内部成本的归属算法。这是基于固定分区语义的工程推断，成立条件是实际 backing 非稀疏镜像、分区/physical-store 区间独立、全部相关增长路径被覆盖且冻结期间不扩容。[^partition][^apfs_reference]

单独“把 raw 全量预分配”不足以替代该边界：预分配只说明某时刻已写入/分配数据，不能据此证明共享 APFS 上未来 COW、快照保留及文件系统元数据不再消耗额外空间。`m1_accounted_capacity_v1` 接受 guest 数据池自身 ENOSPC；这不允许将增长溢出到受保护的其他宿主存储空间。[^qemu][^apfs_reference]

## 📚 Apple 契约与证明边界

| 事项 | 官方可确认的事实 | 对 C06 的判断 |
| --- | --- | --- |
| APFS 卷空间 | 同一 container 的多个卷共享空闲空间；reserve 保留该卷可用空间，quota 限制该卷可分配空间。[^apfs_volume] | `reserve=quota` 不是独立 partition/container 的同义词。 |
| 块级字段 | APFS Reference 的 `apfs_fs_reserve_block_count`、`apfs_fs_quota_block_count`、`apfs_fs_alloc_count` 分别记录卷保留块数、最大可分配块数及当前文件系统分配块数。[^apfs_reference] | 可以验证声明和当前计数，不能自行补出各类成本的完整计费规则。 |
| container 级结构 | 同一 Reference 定义 container 的 `nx_block_count`、space manager、object map、checkpoint 区域；磁盘对象修改写入新位置。[^apfs_reference] | 存在独立 container 结构；文档未给出这些增长对特定卷 quota 的完整归属及上界。 |
| snapshot | Reference 定义独立 snapshot 元数据/extent-reference 结构。[^apfs_reference] | “验收时 snapshot 列表为空”不能证明后续自动或手工 snapshot 不出现。 |
| 固定 partition | Apple 将物理磁盘分区描述为划分成独立区段/container，并明确分区有 size、调整需要显式磁盘操作。[^partition] | 若 backing 几何范围独立且冻结，内部增长可以耗尽自身，不能靠同 container 空间共享扩展到相邻分区；此为机制推断，需真实几何证据。 |

对共享 container 的旧路线，只有补齐“全部相关物理消耗均受一个已验证边界约束”的权威机制或独立外层边界，才可接受。`df` 当前余量、逻辑镜像大小相加、当前没有 snapshot、一次 quota/ENOSPC 压测，均不能独自证明所有未来行为。

## 🔍 Lima v2.2.1 的写入面

| 路径/功能 | 核实结果 | 资格验收必须覆盖的含义 |
| --- | --- | --- |
| `LIMA_HOME` | `LimaDir()` 使用该变量或 `~/.lima`，并解析已有符号链接；instance 在其下；`LimaDisksDir()` 是 `_disks`。官方 internals 页当前标题写 `_disk`，应以固定版本源码为准。[^dirnames][^internals] | 不能仅检查字符串前缀；要绑定实际文件系统、祖先、链接目标和设备身份。 |
| instance 状态 | 官方清单包括 `disk`、`iso`、legacy `basedisk/diffdisk`、`cidata.iso`、VZ EFI/identity 状态、hostagent 日志和 socket；哪些存在依 guest/版本而异。[^internals] | 不能只圈定数据盘；实际系统盘、状态、日志与旧链路同样要归属。 |
| VZ Linux 存储 | `attachDisks()` 将 instance `disk` 和 additional `datadisk` 以可写 file-backed VZ attachment 挂入；ISO/cidata 为只读。额外盘启动时可原地转 raw。[^vz_vm] | raw 是文件格式，不是物理预留机制；启动转换仍是宿主写入。 |
| raw 创建/扩容/复制 | `CreateDisk`/`ResizeDisk` 使用 truncate；`makeSparse` 为 seek+truncate；raw→raw 的 `CopyFile` 路径注明尝试 clonefile；非同路径转换先在目标目录创建临时文件。[^nativeimgutil] | 不能断言默认全量预分配、无 clone 或没有转换峰值。 |
| 下载与转换缓存 | `WithCache()` 使用 `os.UserCacheDir()/lima`，不从 `LIMA_HOME` 派生；缓存保留原始 `data`，还可能生成 `imgconv/raw`、`raw.tmp` 与 digest 临时文件。[^downloader] | 迁移 `LIMA_HOME` 不会自动圈住缓存；不可假设设置 `TMPDIR` 已覆盖它。 |
| VZ snapshot API | 固定 `v2.2.1` 的 create/apply/delete/list 均返回 `errUnimplemented`。[^vz_driver] | 仅能排除该版本这组 Lima API 的成功执行；不能排除宿主 APFS snapshot、文件 clone、备份或复制。 |
| transport/host 输出 | VZ guestagent 使用 vsock；instance 清单列出 hostagent stdout/stderr 与 SSH/socket 状态。[^vz_driver][^internals] | transport 不等于持久文件边界；本次未完成全部 SSH、复制、崩溃报告、系统日志与临时文件调用点审计，不能签发“全部宿主 writer 已覆盖”。 |

`LIMA_HOME`、下载缓存及宿主临时目录是不同选择逻辑。冻结配置应明确禁止运行中的 image download/convert/clone/resize/另存操作，或将其实际目标也纳入物理边界。禁止声明只能作为 A 模式下部署 owner 的操作约束；它不能伪装成自动检测或硬隔离。

## ⚖️ 候选机制取舍

以下为研究建议，不代表已批准迁移。

| 候选 | 结论 | 代价与剩余条件 |
| --- | --- | --- |
| 共享 container + 卷 reserve=quota | **尚未证明** | 保留为当前事实；不能凭更多相同计数或有限压测完成全物理论证。 |
| 同共享 APFS 上预分配 raw | **单独不可接受** | QEMU 的 `full` 通过写入预分配；其 `nocow` 仅适用 btrfs。两者没有提供 APFS 后续 COW/metadata 独立上界。[^qemu] |
| 独立固定 partition + 专用 APFS container + raw 文件 | **首选可验证候选** | 保持现有 VZ file-backed 形态；必须证明真实独立 backing 几何，并覆盖 cache/log/temp 等旁路；分区及迁移有数据操作风险，尚未授权。[^partition][^vz_vm] |
| 独立物理设备 + 全部部署写入 | **边界更清楚的候选** | 无需改变宿主系统盘布局，硬件/容量/断连成本更高；本机 OS 的一般系统写入仍需明确范围。 |
| 直接 host block-device 给 guest | **当前 v2.2.1 不作为最小方案** | 官方功能要求 Lima ≥2.3、macOS ≥14、VZ 和 root helper/device grant；授权绑定可复用的设备槽位，非永久物理 identity。锁也不协调其他用户、其他 `LIMA_HOME` 或 host mount。新增权限与设备误绑定风险，不优于在专用文件系统内保留 raw。[^disks] |

即使选独立分区，`LIMA_HOME` 的宿主日志、未圈住的下载缓存或显式 `copy-to-host` 仍可能在外写入。独立分区证明的是被绑定部署存储的物理容量边界；若要求覆盖 macOS swap、系统日志等任意进程间接写入，单一 Lima 数据分区并不充分，须在验收契约中精确定义被保护属性，不能默认为已经覆盖。

## ✅ 收口 C06 的最小证据

在 A“资格验收 + 冻结部署”下，下一份具体方案应直接收集/验证以下事实；不需要增加常驻 witness，也不应继续为尚未证明的共享卷 quota 编造未来 metadata 上界：

1. **真实物理边界**：disk/partition/container/physical-store UUID、起始偏移、长度、block size、非重叠关系与宿主受保护存储的位置；拒绝“独立 container 实际仍套在共享稀疏磁盘镜像里”的替代证明。多 physical-store、跨设备或未知映射应拒绝。
2. **完整实际写入映射**：固定 Lima binary/build/version、有效配置、真实 instance/system/data image 与可写状态；逐项保存 canonical path、文件/设备/文件系统身份与链接情况；明确 cache、转换峰值、日志、复制及临时输出的处理，不接受仅 `LIMA_HOME` 声明。
3. **冻结约束**：禁止自动/手工 resize、迁移 backing、创建新的写入出口或改变相关 snapshot/clone/backup 行为；任何变更先停准入并通过既有 owner/fencing 收敛，再重新验收。A 不承诺发现绕过该流程的宿主手工操作。
4. **容量与失败语义**：在该独立物理边界内证明已接受的实际 hard quota/80% 记账/PG 原子预留/单次 Start；对池 ENOSPC 保持失败关闭。有限故障测试用于核实失败行为，不用于证明所有未来 APFS 分配路径。

当前仍缺两类证据：共享 APFS 卷 quota 的全物理成本契约，以及整个有效部署的宿主 writer 覆盖。前者不宜继续靠重复读取同一 quota 计数解决；应优先评估是否已有可独立使用的真实固定分区/设备，再决定具体迁移方案。未取得这些证据前，C06 和生产门禁继续关闭。

## 🖥️ 本机当前事实与具体缺口

以下来自本轮同用户只读调用，不是旧历史收据重放。`diskutil` 在解除沙箱限制后取得结果，没有使用 sudo 或修改卷。原始 bytes、配置和有限 FD 列表位于 本轮取证目录 (private evidence retained)。没有进入 guest，也没有调用 Engine/PG/provider。

| 本轮事实 | 对 C06 的含义 |
| --- | --- |
| 卷 UUID `D07C62D3-60F4-4D6F-8A8D-36E2B62BA647`；container UUID `3D5FC9F6-3983-4EA3-A74E-1E3C89AA4A45` | 是原目标卷，不能以同名路径替代身份。 |
| 当前 reserve=quota=`120259084288` bytes（112 GiB）；physical store 为 `disk0s2` | 卷限额仍存在，但没有变成独立物理分区。 |
| 同 container 还包含 macOS System/Data/VM 等卷 | 与被保护宿主存储共享 container，仍存在本报告识别的机制证明缺口。 |
| 物理磁盘清单只列出 `disk0`；其分区为 ISC、主 APFS、Recovery | 本次清单未见可直接用于该方案的独立数据分区或外置盘；没有据此选择/改动任何分区。 |
| 当前 `lima.yaml` 声明 VZ、raw、4 CPU、10 GiB、24 GiB system disk、sbquota additional disk、`mounts:null` | 配置事实与固定版本源码可对应；不将 YAML 声明当作所有有效配置或执限证明。 |
| system `disk` logical size=24 GiB，`datadisk`=64 GiB；各自 `st_blocks` 为6011760、261408（512-byte 计数） | 当前未全量分配；这些块计数不证明独占物理块，也不构成未来空间保证。 |
| `ha.pid`/`vz.pid` 都指向88438；proc_pidpath 指向专用卷的 Lima binary，当前文件 hash=`6d32fb0f880c0ea750434f2ac855a3f2129126752a301ce4d88adbd2fa599e1e`；build info 为 v2.2.1、revision `27bc4c4b101e27994e01e36566a0f2091bbca507` | 确定本次应核对的版本/文件；不是对 loaded memory 的完整远程证明。 |
| exact 两个磁盘文件的持有者为 VZ helper PID88460；不是 hostagent 的普通磁盘 FD | writer 清单必须包含 helper，不能只读 ha.pid 就称完整。 |
| hostagent 与该 helper 的 `lsof` 中，带 w/u access 的 REG 目标均在专用卷；EFI 映射也在卷内 | 当前这些已标注 FD 没有卷外直接可写文件。其他 mapping 的权限、未来 open、缓存、临时操作和 OS 间接写入仍未因此获证。 |
| 当前 APFS snapshots 列表为空；`tmutil isexcluded` 返回 `[Included]` | 不能宣称已有备份排除策略；也不能由 Included 推断备份或快照已经运行。 |

`diskutil info` 报 `FreeSpace=0`、`CapacityInUse=112 GiB`，而同时的 `statvfs` 有非零可用块：`f_frsize=4096`、`f_blocks=29360128`、`f_bavail=28060637`。保留两种原始结果，**本轮不据 info 的这两个值报告卷已耗尽**，也不将任一当前 free 值当作长期物理证明。

当前样本是非原子的只读调查，没有给 helper 做完整 PID/start-time/boot 前后 fencing，也没有审计全部 host 调用点，因此不是可签发的 qualification receipt。沙箱内一次 `/bin/ps` 启动返回 EPERM，失败已记录；后续使用 proc_pidpath 和 exact 文件的 lsof 归属，未把失败补成成功。

缓存、维护和 qualification 产物的完整 writer 映射仍需补齐。当前默认 `~/Library/Caches/lima` 不存在只能说明该路径此时不存在；不能推出其他 cache 设置已关闭。研究文件仍写在开发工作树，不能把它们声称为已受112 GiB卷约束的生产产物。

## 🧭 下一步的可执行选择

本轮把 C06 从“缺少泛化 verifier”定位到了**现有共享 container 无完整物理保证依据，且完整 writer 规则仍待落地**。没有发现或声称 APFS quota 被绕过；也没有证据表明当前 VM 已造成宿主空间耗尽。

在保留已批准保证时，建议优先以可独占的外置设备建立固定分区/container，再保留现有 VZ file-backed 形态；这样不需要改宿主系统盘分区。当前没有连接此类设备的事实，先确认用户可用目标，不能自动选盘或要求立即购买。

若只使用当前主盘，则下一阶段仅设计独立固定分区的可行性、备份/空间检查、停用/一致复制、新身份资格和恢复路径；本轮不运行 shrink/partition/migrate/stop。独立 container 只证明其绑定 backing 的边界，仍不等于宿主 swap、unified log 等任意间接 OS 活动都被它覆盖。

若不接受新增物理边界，另一个需用户明确评估的方向是修订保证，接受“卷级实际 quota/reserve 与受管路径覆盖，仍共享 container 的物理成本风险”。这会改变已批准保证，**当前没有采用，也不能沿用旧 GuaranteeRef 静默放行**。需要独立版本与兼容设计。本轮不把它作为默认实现。

下一次有用的产出应是选定物理目标后的具体迁移/资格设计，或用户明确要求的保证版本比较；不会用另一套占位收据或重复 namespace 诊断来关闭 C06。

## 🔗 来源

本次只采用 primary sources；所有源码引用固定 `v2.2.1`，官方当前功能页仅用于比较替代方案，不代表本机已支持。外部内容均为转述，未长段引用。

[^apfs_volume]: Apple，[Add, delete, or erase APFS volumes](https://support.apple.com/guide/disk-utility/add-delete-or-erase-apfs-volumes-dskua9e6a110/mac)，空间共享及 Reserve/Quota Size。
[^apfs_reference]: Apple，[Apple File System Reference](https://developer.apple.com/support/downloads/Apple-File-System-Reference.pdf)，文档页 10（对象 COW）、27–32（container）、54（reserve/quota/allocated 字段）、117–120（snapshot metadata）。该文档标注 2020-06-22，描述格式，不是当前系统所有分配实现的完备安全证明。
[^partition]: Apple，[Partition a physical disk](https://support.apple.com/guide/disk-utility/partition-a-physical-disk-dskutl14027/mac)，独立区段、分区大小及扩容。
[^dirnames]: Lima v2.2.1，[dirnames.go](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limatype/dirnames/dirnames.go#L18-L86)，`LimaDir`、`LimaDisksDir`、`InstanceDir`。
[^internals]: Lima，[Internal data structure](https://lima-vm.io/docs/dev/internals/)，instance、cache、磁盘和 transport 清单；与固定版本源码交叉核实。
[^nativeimgutil]: Lima v2.2.1，[nativeimgutil.go](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/imgutil/nativeimgutil/nativeimgutil.go#L88-L244)，目标目录临时文件、clone 尝试、sparse/truncate。
[^downloader]: Lima v2.2.1，[downloader.go](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/downloader/downloader.go#L78-L95) 及 [`getCached`/`ensureRawInCache`](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/downloader/downloader.go#L255-L494)，缓存根选择及并存转换产物。
[^vz_driver]: Lima v2.2.1，[vz_driver_darwin.go](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/driver/vz/vz_driver_darwin.go#L506-L577)，vsock、支持格式及未实现 snapshot API。
[^vz_vm]: Lima v2.2.1，[vm_darwin.go](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/driver/vz/vm_darwin.go#L518-L593)，实际系统盘、additional disk 和只读 media attachment；目录共享见同文件 `directorySharingDevicesGeneric`。
[^qemu]: QEMU，[qemu-img Notes](https://www.qemu.org/docs/master/tools/qemu-img.html#notes)，raw `preallocation` 及 `nocow` 适用范围；当前工具文档不是本机已执行预分配的证据。
[^disks]: Lima，[Disks: Attach host block devices](https://lima-vm.io/docs/config/disk/#attach-host-block-devices)，版本要求、helper/授权、设备槽位与锁的边界。
