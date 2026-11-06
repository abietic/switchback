# M1 双固定 quota backing-device inventory v3

状态：用户已接受 A，授权 `call_98cfe5c2eaec4b1cbf336ead89b69d78`。本文件将已接受候选细化为可实施契约，不增加真实环境权限。

接受原文：`.scratch/runtime-m1-quota-backing-device-entry-execution-1023/volume-device-next-design.md`，SHA256 `5c2fe50fac59c502ce46c3f03594aeedfdd51fd276fb5142cd6b3ee49dee856e`。

## 目标与事实

修正现行 scanner 只接纳 overlay2 quota helper 的兼容缺口。最新真实 Engine 窗口完成 overlay2 设备元数据分支后，在 `volumes/backingFsBlockDev` 停止；全 store 没有扫描完成，不能据此声称 preflight、Engine 或资格首启成功。

真实 packet 是 `.scratch/runtime-m1-quota-backing-device-entry-execution-1023/engine-host-outcome.json`，SHA256 `fe34d055c294b4935787f3d4e7c19b790aeb1b67e6f22cfba9d0581952ceab21`。它的 255 帧 guest raw 数据和本次五节点 RED 回放保持原样。overlay2 和 volumes 两设备的实际 st_dev/st_rdev 均为 64785；第二设备 inode 为 72192。旧失败、unknown/remote_effects unknown 和未观测部分不可转写为成功。

采用独立 v3，使双路径接纳能力与旧证据语义明确分离。普通文件、数据库解码、恢复引用和容量/Start 门禁没有新例外。

## 全局约束

- 固定 WT `/path/to/switchback`；phase `.scratch/runtime-m1-volume-quota-backing-device-1023`；HEAD/index/所有既有dirty paths受保护，不stage、不提交、不切branch。
- 用户授权 `call_98cfe5c2eaec4b1cbf336ead89b69d78`；接受原文和本设计均为实现约束；仅完成源码、离线回归、完整新kit/bundle、独立task和whole-slice复审。
- 不进入guest，不运行Lima/lsof/Engine/API/PG/provider，不生成秘密/journal，不执行编译候选或nativefixture，不读取或操作真实固定副本/stage，不改存储/权限/分区，不重试旧实际窗口。
- 新schema `switchback.engine.store_inventory.v3`、rule `m1-engine-store-v3`；v1与v2的schema/rule/验收语义保留，v2仍只接纳overlay2设备；未知schema/字段、版本混合、其他special和不明来源继续拒绝。
- v3仅支持 `DATA_ROOT/overlay2/backingFsBlockDev` 与 `DATA_ROOT/volumes/backingFsBlockDev`；允许0/1/2节点，每路径至多一次，不按basename、glob或任意子目录放行。
- 每设备独立要求Linux block0600/root:root/nlink1/size0、正inode、非bool精确整数；st_dev/st_rdev/rootfd和同窗口完整XFS major/minor一致；只用固定父dirfd下 `O_PATH|O_NOFOLLOW|O_CLOEXEC` 获取元数据，不设备payload I/O/mknod/unlink/chmod/chown/quotactl。
- two production scans均传同窗口完整filesystem；validate_preflight将v2/v3 backing_filesystem与自身filesystem完整绑定。保留before/fd/pathname/scan末尾复验、原bounds、普通payload完整read/hash/decoder和restore_refs拒绝策略。
- 不改变120/240秒窗口、300秒barrier、CAP_SYS_ADMIN+CAP_SYS_PTRACE、PG/Start/Trust、三个生产门禁或现行共享卷保证。
- `deploy/experience/switchback-capacity-publisher.service` 与 `internal/workerruntime/adapter/capacitypublisher/publish_test.go` 不动。所有旧candidates、seals、raw、真实失败phase和现存归档逐字节保留。
- 每个验证尝试使用独占的新stdout/stderr/receipt路径，保留失败和RED；不得覆盖旧尝试。报告给出实际命令、exit、日志hash、自评和疑虑。

## 接口与验收语义

现有 `_scan_store(..., *, expected_filesystem=None)` 签名不变。无绑定时继续产生旧 v1；有完整 filesystem 绑定时产生 v3，条目统一使用 v3 rule。生产不引入可选版本开关，也不提供旧数据升级器。

`validate_store_inventory(facts)` 接受三个独立版本：

| 版本 | 顶层 backing_filesystem | 合法设备路径 | 条目 rule |
| --- | --- | --- | --- |
| v1 | 不允许该字段 | 无 | m1-engine-store-v1 |
| v2 | 必填，完整闭合 filesystem DTO | overlay2/backingFsBlockDev | m1-engine-store-v2 |
| v3 | 必填，完整闭合 filesystem DTO | overlay2/backingFsBlockDev、volumes/backingFsBlockDev | m1-engine-store-v3 |

v2/v3 共同绑定 `_filesystem` 和现有 Linux64 major/minor 编码；设备节点 st_dev、st_rdev 必须等于 rootfd device 和上述编码。禁止用 Darwin 的 `os.major/os.makedev` 解释 Linux packet。

设备条目沿用 v2 的闭合 DTO，没有新的 optional 字段：

```python
{
    'path': DATA_ROOT + '/volumes/backingFsBlockDev',
    'kind': 'block_device', 'uid': 0, 'gid': 0, 'mode': 0o600,
    'nlink': 1, 'bytes': 0, 'sha256': None,
    'classification': 'quota_backing_device',
    'format': 'docker-xfs-quota-backing-device-v1',
    'rule_revision': 'm1-engine-store-v3', 'scan_complete': True,
    'restore_refs': dict.fromkeys(RESTORE_CLASSES, 0),
    'device_identity': {'device': 64785, 'inode': 72192, 'rdev': 64785,
                        'major': 253, 'minor': 17},
}
```

上例 major/minor 为与设备号相符的离线完整 filesystem fixture；实际生产值始终来自同窗口采集。path、inode、所有设备身份独立验证；普通目录/regular 不能携带 device_identity。重复路径、额外设备、遗漏父目录、错误 directory classification、非零 restore_refs 均拒绝。接纳 volumes helper 不等于接纳有恢复对象的 volume/metadata.db。

scanner 对两个固定角色复用同一 metadata-only 分支；保留每节点 before/fstat/pathname 与全遍历末尾稳定性检查。不新增设备创建或修改代码。两处 guest 调用已有 expected_filesystem 绑定；若源码无须变化，以真实调用测试证明新 v3 已走通两个 seam。

## 可追溯回归

从原 packet 机械选择 root、overlay2、overlay2/backingFsBlockDev、volumes、volumes/backingFsBlockDev 的原始 stat frame 字节；记录 packet SHA、原 observation/frame 索引和逐帧 SHA。把这些机器生成的 raw frame 字节内嵌现有 `test_store.py`，测试从它们解析实际 metadata，不能手写一份看似真实的 stat 字典。

这样不引入 `.scratch` 运行依赖、不增加 bundle 的17份覆盖输入。完整 filesystem 包装、模拟 syscall 和缺席/畸形/竞态变体明确标记为合成 fixture；测试中实际 metadata 与合成字段来源分开。新增 phase 的 extraction receipt 机械证明内嵌 bytes 与原 packet 相等，旧 packet 不修改。

先在旧 scanner 下执行实际五节点 v3 正例得到 RED，再实施最小改动。覆盖0/1/2设备、逐路径缺席、第三设备与错误路径、symlink/char/FIFO/socket、owner/mode/nlink/size、missing/bad rdev、major/minor/跨FS/rootfd、inode/rdev/parent/scan末尾竞态、未知schema/字段/rule和版本混合。验证 v1 拒绝两设备、v2 只接受overlay2且拒绝volumes、v3 两设备均严格接纳；普通payload和两轮生产绑定保持有效。

## 完整交付与后续操作

仅允许 scanner/contract/相关tests/README 及必要 Go test-only bundle discovery 变更。源码 task review 通过后，使用原 `build_candidate.py build` direct entry 一次生成完整新 kit（单调目录以实际输出为准），不得 patch/reuse 旧候选。canonical bundle/audit 验证 current/copied/build before-after roots、1076source+7templates、1151covered+manifest、20formats、43内部exit0、17反向输入、13runtime null和4false。新实际 kit/bundle 的纯 Go final binding 必须 PASS/noSKIP，runtime reads为0。

离线操作清单绑定新候选的真实manifest。当前四份固定副本是 kit0011/bundle0001；最新已消费host stage有276文件。未来先验证原 grant/claim/boot/binding、root/per-leaf owner/mode/inode/hash和manifest完整覆盖，再在新的候选专属固定archive中no-replace rename原样保留五对象，独占复制四新对象。已存在 `archive-quota-backing-device-1023-kit-0011-bundle-0001` 与其五旧对象全部保留；不得重用它、任何旧stage或失败窗口。本片只写清单，不接触这些真实路径。

保留已通过的严格 host FD contract、no-replace协议和原120/240/300秒行为。未来真实窗口需新候选完整封存后单独授权；失败或unknown即停，不重试、不续跑、不remote Stop。当前设计不保证下一次完整store没有其他障碍。

## 完成标准

源码/兼容/真实frame回归、完整新候选与canonical audit、task1/task2独立复审和fresh whole-slice终审、root保护核验均完成；completion与phase seal最后独占发布。当前进度文档和append-only ledger明确区分离线完成与真实环境尚未通过。
