# Engine identity 与 quota 类型只读核验

承接用户持续推进授权与已审 handoff 的下一最小步骤。本阶段不更改架构、容量权限或产品代码。唯一候选为 `.worktrees/observability-master-integration-1009`，HEAD `fbebfe754045bdbf1af40871a5018eed33743011`；862 产品输入和三轮封存已在本阶段 baseline 逐字节核验。

## 目标与取舍

把历史计划的 Engine binary/config 与本次固定读取绑定，并取得实际 quota 类型和固定初始化路径的 UID/GID/project/inherit。优先排除不适用的 quota owner 和具名 kernel 慢路径，缩小 Gmax/K_bootstrap 证明范围。三种路径中，选择静态 ELF + 固定 GET：比执行 daemon/version/plugin 命令副作用少；比只复述历史哈希多出当前字节证据。它不证明 plugin 实际 initialized，亦不替代完整容量证明。

## 全局约束

- Root 独占 VM/SSH/native/service；子 Agent 只在新 scratch 内离线实现/测试/审查。
- 新身份 `.scratch/runtime-engine-identity-1017`；禁止 Git 写入、旧 packet/marker/服务重放、产品代码修改、Engine/Worker/Create/Start/provider/付费动作。
- `capacity_ready=false`、`execution_enabled=false`、paid 未消费；固定 10GiB 与 80% 合同不变。无证据的 Gmax/K_bootstrap/Q0/backing 保持 unknown。
- 唯一 native 入口固定 argv、路径、命令、syscall，无客户端/argv/env 自选目标。只读文件/元数据、FSGETXATTR、Q_XGETQSTATV、固定 project 的 Q_XGETQUOTA；不 SET、不 sync、不枚举 quota IDs、不读取业务 DB 内容、不执行 Engine。
- 复用上轮 exact `policy.py`、`transport.py` 和 `guest.py` 的 `runtime_guard`，绑定字节后仅调用函数，绝不调用已消费 main。检查真实 core-slice reader cgroup，12 command guard 前后保留原始输出/退出。
- 持有 FD、不跟随 symlink、受信 root owner/非 group/world-writable ancestors；binary/config 读取保持 no-atime。目标 before/after 与 no-follow rewalk 身份稳定才可给 matched；missing/errno/不支持/不完整不能补零。
- native 20 秒 wall/4 秒 CPU/256MiB AS/128 FD/FSIZE0/core0，host 35 秒；structured 2MiB 与 transport 3MiB 捕获界沿用已审保留语义。界限是采样界，不是容量常数。
- actual 目录 exclusive create 一次，失败也消费；先保留 raw/原始 exit/termination，再解析 JSON。local child reaped 不代表 remote/group termination。

## 有限目标

binary 固定 `/usr/bin/dockerd`、`/usr/bin/containerd`；预期 bytes/hash/dev/inode 取历史 `prestart1014-candidate-r7.json`。流式 hash，最多128MiB/文件；仅 ELF64 little endian AArch64 的 `.go.buildinfo`，最多256KiB/section、4096 section headers、256KiB section-name table。支持 Go>=1.18 inline 编码；其他编码显式 unavailable，不猜。保留 section raw 与完整解码公共 build metadata，标注 source revision/build tags 与历史/source 的关系，不把 BuildInfo 自动等同 reproducible build 或 initialized plugin。无 Engine exec。

config 固定 `/etc/containerd/config.toml`、`/etc/docker/daemon.json`，最多128KiB/文件，exact raw/hash 与历史预期比较；保留 parsed public config。配置相同只证明读取字节，不证明运行时已加载。plugin 集合记录 disabled 配置；编译/import/initialized 不混淆。

metadata 固定：`/mnt/lima-sbquota`，其 `docker`、`docker/containerd`、`docker/overlay2`、`docker/volumes`、`docker/buildkit`；containerd 下 content.v1.content、metadata.v1.bolt、snapshotter.v1.overlayfs、snapshotter.v1.native 四个完整 `io.containerd.*` 目录；两个 snapshotter 下的 `snapshots` 和 `userxattr-check`；六 DB 路径同上轮（只 stat/FSGETXATTR）。present 目标记录 stat 与28-byte raw FSGETXATTR、UID/GID/project/inherit/realtime 标志；missing 明确记录首个 missing component，不遍历、不导出未知 entry 名字。

pool 固定 FD 读取160-byte `Q_XGETQSTATV(version1)` 前/后，保留所有三类 quota inode raw 字段、flags、errno。仅有实际 ACCOUNT/ENFORCE flags 才能说该类型启用；零/NULL inode须区分。按 exact Ubuntu 源码，qfs_nblks 不套用普通 quota 的512单位，也不把 incoredqs 当持久chunk。读取固定 project999/1000 的112-byte Q_XGETQUOTA，errno/ENOENT 不补零；statvfs 本轮跳过，不产生当前几何/余量样本。固定1000来自已审 Moby NewControl 的 base999+1 双 probe 条件；GET 不执行该 probe，不由 ENOENT 推断无 quota inode增长。

## 验证与交付

生成 ELF fixture 覆盖 valid/tags/version、非 ELF/wrong endian/arch/section bounds/varint/frame/duplicate、读取变化/符号链接、quota version/flags/layout/errno、metadata missing/type/replaced、host malformed/deep/duplicate JSON raw 保留和 exclusive attempt。仅为本次新增逻辑测试，不重跑字节相同旧 collectors 的 suite。新增代码先离线审查；C/I 由 fresh fixer 处理并定向复审。Root 单次实机后独立核 raw 与来源，再做 fresh cumulative review、handoff 和封存。OpenSpec 6.5/8.4/9.3 保持 open，除非独立真实体验验收完成。


Root只读范围收窄（native前）：exact kernel142 `fs/quota/quota.c:705–720,819–820` → `fs/xfs/xfs_quotaops.c:214–226` → `fs/xfs/xfs_qm_syscalls.c:448–477`，project0 GET 会先 `xfs_inodegc_push`。本轮固定采集因此**不调用project0 GET**，输出 `status=not_queried, reason=kernel_inodegc_side_effect`，无raw/errno/limits伪值；默认quota0字段保持unknown。仅查询999与1000；不改成NEXT枚举、动态ID、SET或其他绕过。QSTAT前后仍按原范围采集。这是将内部计划收窄到用户授权的只读事实范围，非产品quota/API/权限/容量决策。旧0/999/1000包从未native，作为历史保留，不再执行。

同一只读收窄补充：exact kernel142 `fs/xfs/xfs_super.c:818–836` 的 statfs 实现也先 xfs_inodegc_push；native collect 因此不调用geometry/os.fstatvfs，statvfs只输出 `{status:not_queried,reason:kernel_inodegc_side_effect}`，不带任何伪造几何/余量。当前geometry/Q0/backing保持unknown；以后在允许该内核维护效果的受控epoch再采集。不得藉此增加任何query或改动复用guard字节。
