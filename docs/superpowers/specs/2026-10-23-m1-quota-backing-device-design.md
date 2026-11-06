# M1 quota backing-device 兼容修正

用户已接受 `.scratch/runtime-m1-host-recorder-execution-1023/store-device-fix-design.md` 的完整方案，授权 `call_b5ecdf245c5a4e5d8d5494ab667c5243`，回答“接受，完成整片离线修复（推荐）”。接受原文的哈希见新 phase `authorization.json`；原文与真实失败封存均不可修改。本规格将其收敛为内部 DTO 与调用接口，不扩大效果授权。

## 目标和边界

修正 `_scan_store` 无法表示固定 quota backing-device 的兼容缺口。固定路径是 `/mnt/lima-sbquota/docker/overlay2/backingFsBlockDev`；只接受与本次 XFS backing filesystem 一致的 Linux root:root 0600、nlink1、size0块设备，完整覆盖、当前 inode 和 st_rdev 均复验；其余特殊文件继续拒绝。仅通过 `O_PATH|O_NOFOLLOW|O_CLOEXEC` 获取元数据 fd，禁止普通 open/read/write 设备 payload、mknod、unlink、chmod/chown、quotactl 或任何环境效果。

现行 v1 语义和已有 fixture 保留；新增独立 inventory v2/rule v2。不以文件名跳过扫描，不把块设备伪装成 regular/directory，不将缺失 st_rdev 的真实失败 packet 转为成功事实。

## 接口与 wire 形状

`_scan_store(root=Path(DATA_ROOT), logical_root=DATA_ROOT, uid=0, gid=0, check=..., observe=..., *, expected_filesystem=None)` 保留现有调用兼容。无 expected_filesystem 时保留 v1 的普通文件/目录语义，设备拒绝；提供绑定时必须完整校验，不能失败后回落 v1。两个生产调用点都必须传入当前窗口 filesystem；第一次使用 `_filesystem` 已观测事实，claim 前先 `_boundary` 重验再使用 `self.before['filesystem']`。

v2 顶层沿用现有字段，新增 `backing_filesystem`，为 contract 现有 `_filesystem` validator 接受的完整固定 XFS事实。schema=`switchback.engine.store_inventory.v2`，条目 rule_revision=`m1-engine-store-v2`。v1 schema/rule 原样；版本混合拒绝。

普通/目录条目继续使用原字段和验证规则；仅固定块设备条目新增 `device_identity`，键严格为 `device,inode,rdev,major,minor`，并使用：

```python
{
    'path': DATA_ROOT + '/overlay2/backingFsBlockDev',
    'kind': 'block_device', 'uid': 0, 'gid': 0, 'mode': 0o600, 'nlink': 1,
    'bytes': 0, 'sha256': None,
    'classification': 'quota_backing_device',
    'format': 'docker-xfs-quota-backing-device-v1',
    'rule_revision': 'm1-engine-store-v2', 'scan_complete': True,
    'restore_refs': dict.fromkeys(RESTORE_CLASSES, 0),
    'device_identity': {'device': st_dev, 'inode': st_ino, 'rdev': st_rdev,
                        'major': observed_major, 'minor': observed_minor},
}
```

raw dev/rdev 必须非 bool 整数、相等且匹配当前 Linux filesystem major/minor；inode正整数。不要使用宿主 Darwin 的 dev_t 解码规则解释捕获的 Linux dev_t：生产在 Linux 采集 major/minor，纯 validator 若解码 raw 值需用明确 Linux 编码规则或等价无平台依赖检查。scanner 还需证明 root fd.st_dev 等于该 backing filesystem，块设备自身 st_dev、st_rdev 与 root相等。`validate_preflight` 对v2还必须将 inventory 的 backing_filesystem 与同一 facts['filesystem'] 完整绑定，不能用两个分别合法却不同的filesystem。

`_identity` 和原始 stat 观测纳入 st_rdev。固定父 dirfd、O_PATH fstat、pathname before/after、完整 walk 末尾都复验；根目录也有 fd pin，全部 traversal 仍有原 bounds。设备不存在不由本修复创建；是否接受无该节点的普通 store 继续按完整 v1/v2覆盖与其余原语义判断。

## 完整离线交付

1. 从真实失败三节点重放建立 scanner 调用路径回归，补 v2成功与拒绝矩阵；真实 packet仍缺rdev，保留其不完整含义。
2. 修改 scanner/validator/两生产调用与必要文档，保持120/240秒、300秒barrier、所有 capability、PG/Start/Trust 和其他业务行为。
3. 用原 direct builder 生成新的完整 kit、新 phase bundle；核验 full coverage/current-copy/build roots、格式、构建回执和反向绑定，再验证实际新 kit 的纯 Go binding 无 SKIP，完成任务评审及独立整片评审。
4. 离线制定固定四副本及本次已消费 host engine-preparation stage 的 no-replace 保留/切换清单；不执行。

真实运行缺失：当前 guest 节点 st_rdev 未证明，旧 store 在拒绝点之后未扫描。离线合成 fixture 的通过不能填补这些事实，也不能保证下一次真实窗口成功。

## 禁止效果

不进入 guest、不运行 Lima/Engine/API/PG/provider，不生成秘密/journal，不执行编译出的候选或 native fixture，不改真实 stage/副本/存储/分区，不重试任何旧实际窗口。保护所有历史kit/bundle/seal/raw和未提交改动；不提交、stage或改branch/index。`deploy/experience/switchback-capacity-publisher.service` 与 `internal/workerruntime/adapter/capacitypublisher/publish_test.go` 不动。
