# M1：已 unlink 的系统可执行文件与归档占用校验

状态：待用户确认设计与执行范围，尚未修改 guard。当前仅完成一次已授权只读定位，不产生迁移或 Start 权限。

## 目标与已验证事实

修正准备操作器对无关系统进程的过度拒绝，继续同一 kit20 的 observer 原样保留、Engine 部署和资格首启。产品源码、Go ELF、kit20、bundle1、资源与容量/PG/Trust/quota/Start 门禁均保持现状。

2026-10-26 的单次只读定位在0.514秒内完成：同一 boot、同一128 PID集合，124项身份检查通过，4项被原 guard 拒绝。前后 start tick、flags、原始 exe 链接、cgroup 摘要及目标 metadata 稳定。

| PID | 原始 exe 链接 | dev/inode | nlink | 命中待保留对象 inode |
| --- | --- | --- | --- | --- |
| 1298 | `/usr/sbin/sshd (deleted)` | 64769/23623 | 0 | 否 |
| 1348 | `/usr/sbin/sshd (deleted)` | 64769/23623 | 0 | 否 |
| 715 | `/usr/lib/systemd/systemd-logind (deleted)` | 64769/4805 | 0 | 否 |
| 802 | `/usr/lib/systemd/systemd-executor (deleted)` | 64769/4795 | 0 | 否 |

回执位于 `.scratch/runtime-m1-process-identity-readonly-1026/`。结果始终为 `authority=none`、`guard_passed=false`；未扫描完原归档 FD/maps 门禁，不能据此宣称对象没有占用。

[Linux proc_pid_exe 接口说明](https://man7.org/linux/man-pages/man5/proc_pid_exe.5.html)定义：原可执行文件路径被 unlink 后，exe 链接会附加 deleted 标记。这是内核接口的已定义状态。是否由软件包更新引起属于推断，本轮没有读取包管理日志，也不依赖这一推断。

根因位于原封存操作器 `ownership_guard.py` 的 `ProcInventory.process`：它在读取实际 exe 对象前，对所有进程统一拒绝 deleted 后缀。原 Engine 的完整进程采集和 Go Engine 资格采集并无这一全局拒绝；特定受管角色的 ELF 哈希与身份规则仍然严格，不在本修订中改变。

## 路线取舍

推荐 A：只修准备操作器的对象身份判定。使用内核提供的实际 exe 对象身份，继续完整进程、FD、maps、manager 与 boot 校验；不维护 PID/程序名白名单，也不重启系统服务。

B：保持原 guard，另行维护并重启相关系统服务。需要先确定实际 service 归属和重启顺序，可能影响 SSH 与登录管理；这是系统维护范围，当前事实不足以直接操作。此设计选择 A，不执行 B。

## A 的固定接受条件

1. 正常绝对 exe、kernel-thread 证明和非绝对 exe 的现行处理保持。非绝对链接仍拒绝。
2. 仅在原始 exe 是绝对路径且以 ` (deleted)` 结尾时，新增读取 `/proc/PID/exe` 实际对象的分支。使用 Linux `O_PATH` 固定 fd，只取 metadata，不读取或执行该文件内容。
3. 目标须为 regular、nlink=0、有效 dev/inode、`0 < size <= 512 MiB`；捕获完整 metadata。固定 fd 与当前 proc 链接目标必须一致，原始链接前后相同；PID/start/flags/cgroup 与 exe 对象身份纳入前后 fingerprint。PID1 零 start tick 的既有语义保留，R/S 不作为 identity 漂移；其他未知身份、I/O、权限、变化或超限继续拒绝。
4. 删除标记原样保留，不通过去掉后缀把路径重新当作真实文件打开。不得根据本次4个PID、sshd/logind名称或系统目录做豁免。
5. `scan(roots,nodes,deadline)` 必须将新分支产生的 exe 实际 dev/inode 与保护集合比较，并检查原始 exe 是否落在保护路径/归档路径范围。命中任何保护对象即拒绝。这个检查适用于 observer 保留和原 installed-object 保留的全部调用点，新旧 observer archive 也纳入保护路径；正常非deleted进程的既有处理保持。
6. 原全部 PID集合、start/exe/cgroup 稳定性、writer FD、writable maps、完整覆盖、manager/job、boot、deadline 和 owner 冻结规则保留。不能跳过 deleted 进程的 FD/maps，不得把只读定位结果当作当前写入窗口的占用证明。

## 实现与证据边界

只在新的私有操作器 phase 实施，原封存目录、旧 grant、失败回执和旧语义原样保留。新 guard 使用独立 policy 标识和 v2 binding/grant，禁止旧 v1 授权执行新接受规则。实际执行使用独立的新固定路径 `.scratch/runtime-m1-unlinked-exe-guard-execution-next`，旧48项失败 execution phase 留在原处只读保留，不再为复用路径增加一次宿主改名。kit20/bundle1 manifest、Source/模板根和所有二进制保持原值；不修改项目 Go/Python Engine Source、镜像、依赖、资源、网络或服务能力。

新操作器同时接入 observer 两对象保留和原完整准备阶段的 guard；不只修前一个调用点。重建并校验完整 phase 输入绑定、当前项目 Source/模板绑定与 candidate reverse binding。只更新操作器，不重建 kit。

guest argv 的65536字节门禁保留。允许使用更强的无损 LZMA 压缩；若仍需空间，仅允许对私有 carrier 模块移除注释并机器验证 AST 等价，原 contract/prepare Source 原字节保持。原始模块 hash、打包模块 hash 与原始 Source 绑定均需保留；不增加 stdin framing、临时 guest 程序文件、远程重试或新传输协议。所有 action 的最大合法公开输入都必须通过尺寸校验。

## 必须完成的离线验证

- 使用本次4项完整拒绝观测及原 proc fixture 离线复现原拒绝，验证新判定识别实际对象身份。128总数仅用于现场覆盖证据，不把124项摘要当作完整raw回放。
- 稳定且不命中保护集合的 absolute/unlinked regular 对象可继续扫描；相同 dev/inode、保护路径、伪 deleted（nlink非0）、非绝对链接、权限失败、缺失目标、PID/exe/flags/cgroup/metadata 漂移均拒绝。
- deleted 进程的 writer FD 和 writable mapping 仍拒绝，未知/不完整/PID集合变化仍拒绝。证明没有读取可执行文件内容、配置或 key，没有按 PID/名称跳过。
- 保留原102项操作器回归、6项 observer 文件系统边界预演；新增真正覆盖此次对象身份与调用组合的回归。新旧授权隔离、一次 claim、阶段故障不继续、敏感凭据日志例外和全部 guest argv 尺寸均复核。
- 当前 Source、kit20/bundle1 和原证据完整性必须不变。离线检查和复核全部通过后封存新操作器；失败则在本设计范围修正并重验，不进入现场。需要扩大保证、权限、传输或 Source 范围时，先提出新的具体变更。

## 用户可选择的授权范围

范围一：接受 A，仅完成离线实现、回归、复核与新操作器封存；不进入 guest、不启动环境。

范围二：接受 A，并明确授权在上述离线条件全部通过、具体新操作器和 scope 完成封存后，直接执行一次新真实窗口。此授权只覆盖本设计的新操作器与原已审操作序列：重新证明 VM 已 Running 和完整前提，检查占用后原样保留 observer ELF 与整个含旧key的配置目录，保留旧失败证据、复制新私有操作器及不变 kit20，执行一次原120秒 Engine；成功后才继续原权限下的新秘密、新PG/迁移、root/CAP_SYS_ADMIN+CAP_SYS_PTRACE有限Native采集、已准备DeepSeek凭据的单次读取/SSH stdin准备及一次原240秒资格→安装→同进程首启。原300秒barrier与7200秒准备上限保留。为避免重复确认同一范围，选择范围二后不再要求对该设计内相同动作另行批准。

两种范围均不启动 VM、不改分区、不迁移存储、不维护或重启上述系统服务、不访问旧数据库、不创建 Task、不调用模型/provider。范围二失败或 unknown 即停，不重试、续跑、失败后guest核查或远程Stop；部分效果保留，不自动回滚。它不是一次实跑成功承诺，剩余门禁仍由现场证据判定。

需要明确确认：本次授权明示“不改 guard”；A 会改变操作器的接受条件。此文仅设计，不修改现行 guard 或环境。
