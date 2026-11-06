# M1 B3-B Linux 诊断窗口与 B3-B1 离线执行器设计

_2026-10-20 · 待设计审阅；本次只申请B3-B1离线实施，不申请实际guest/root/CAP_SYS_ADMIN执行。_

## 🎯 目标与已完成准备

在现有专用 VM 中运行一次已审阅的 B3-A 诊断，获得 root + SYS_ADMIN-only service 的实际 credentials 与 caller/PID1 namespace 访问结果。它仍是 unsigned diagnostic，不证明 publisher、宿主 backing、完整 writer inventory、容量准入或 Start 可用。

B3-A 的134项seal、963 current/frozen inputs、4720条最终回归、659条定向race及四项compile-only证据已重新核验。当前正式命令从这些源码离线构建成功，未执行；构建时963输入保持不变。此前测试证据继续有效，没有因产物准备而重复整套测试。

| 当前只读宿主事实 | 读回 |
| --- | --- |
| Lima / VM | `/Volumes/SwitchbackCore/toolchain/lima-2.2.1/bin/limactl` v2.2.1；`LIMA_HOME=/Volumes/SwitchbackCore/lima`；`switchback-core` Running |
| VM 资源 | VZ/aarch64，4 vCPU，10 GiB RAM，24 GiB system disk；额外sbquota配置不变 |
| 宿主快照 | 48 GiB RAM，memory_pressure free percentage约38%；swap used约5 GiB，可能随时间变化，不据此证明容量充足 |
| 正式 probe ELF | arm64 machine183，5640057 bytes；SHA256 `a1309c87bc3868f275f05e4ec42c853f7b57c7c2c356ac616e94d0f769a62d27` |
| 既有环境 | 宿主Docker Desktop进程存在；本轮未调用其Engine API，也未启动/停止任何VM或服务 |

以上VM身份来自host Lima元数据，不替代guest boot/kernel/credentials证明。执行前必须重新读取；VM若已停止则拒绝，不加 `--start`、不自动换目标。

产物及可核对argv：execution-candidate.json (private evidence retained)、unit审阅素材 (private evidence retained)、build receipt (private evidence retained)、host快照 (private evidence retained)。unit素材没有安装、enable或执行。

## 🧭 路线选择

| 方案 | 能回答的问题 | 取舍 |
| --- | --- | --- |
| 推荐：现有VM中单次受限root/SYS_ADMIN-only service | 观察候选capability envelope下的真实PID1访问及credentials | 需要显式授权实际root/CAP_SYS_ADMIN进程和少量新运行期文件；不修改publisher |
| 普通SSH用户直接运行 | 仅说明普通用户principal的访问情况 | 不直接解决候选root/SYS_ADMIN-only权限疑点，额外一次真实执行收益较低 |
| 现在增加SYS_PTRACE或改publisher gate | 改变部署权限或准入行为 | 缺少exact-target事实，不在本窗口范围 |

选第一条。SYS_ADMIN并非只读能力；本方案依赖固定已评审ELF、哈希绑定和独立进程资源/系统调用限制。没有SYS_PTRACE扩权，也没有调用ptrace/process_vm等接口。

## 🔐 固定目标、权限与差异

只使用上述已运行VM及新unit `switchback-deployment-probe1020-b3b.service`，新stage `/run/switchback-deployment-probe1020-b3b`。unit通过 `systemd-run --system` 创建为transient service，不安装持久unit文件，不daemon-reload，不写 `/etc`，不改任何既有unit/drop-in/slice。

credentials和主要限制复制自B2候选：User/Group=root、CapabilityBoundingSet=CAP_SYS_ADMIN、NoNewPrivileges=yes、RestrictAddressFamilies=AF_UNIX、RestrictNamespaces=yes、SystemCallFilter=~@mount @reboot @swap。实际cap五集合/NNP/Seccomp仍以probe读回为准，不用声明值覆盖观察。capability配置的含义按[systemd v255 exec文档](https://raw.githubusercontent.com/systemd/systemd/v255/man/systemd.exec.xml)核对。

诊断刻意使用已经存在的 `system.slice`，避免激活或改变产品的 `switchback-core.slice`。它是独立oneshot service，生命周期、cgroup、InvocationID与最终publisher不同；所以即使读回成功也不能生成publisher receipt。后续publisher必须在自身进程观察并接受独立部署验证。

资源固定CPUQuota20%/period100ms、MemoryMax128M、swap0、TasksMax64、LimitCPU2秒、LimitFSIZE32768、LimitCORE0。probe内部5秒cooperative context；oneshot显式TimeoutStartSec10秒、TimeoutStopSec5秒、KillMode=control-group、Restart=no。`TimeoutStartSec`需显式设置，因为oneshot默认没有启动超时；内核不可中断IO仍可能延后消失，不能声称硬15秒终止。[systemd v255 service文档](https://raw.githubusercontent.com/systemd/systemd/v255/man/systemd.service.xml)

`Type=oneshot`、`RemainAfterExit=yes`，把exit2列为SuccessExitStatus，仅用于保留诊断拒绝结果，原始ExecMainStatus必须仍为2。无 `--wait`/`--pipe`/`--collect`，防止把退出等待、unit保留和stdio混用；输出仅写stage下两个新0600文件。[systemd v255 run文档](https://raw.githubusercontent.com/systemd/systemd/v255/man/systemd-run.xml)

## ▶️ 固定状态流与门禁

```mermaid
flowchart LR
    accTitle: One-shot Linux deployment diagnostic authorization boundary
    accDescr: Prepared bytes require explicit execution approval. A read-only preflight precedes exclusive staging, one consumed execution, evidence readback and owned-unit stop. Every drift or uncertainty blocks progression.
    frozen["已冻结ELF/源码/argv"] --> approval["显式执行授权"]
    approval --> pre["只读preflight"]
    pre --> stage["新私有stage / O_EXCL"]
    stage --> once["先消费actual.once，再启动一次"]
    once --> observe["保存DTO + manager原始状态"]
    observe --> post["仅停止自己的unit并读回"]
    post --> review["事实审阅；生产门禁仍关闭"]
```

设计接受后先在新实施phase `.scratch/runtime-m1-deployment-carrier-b3b1-1020/` 完成有限carrier与离线故障检查，再做独立窄操作评审。完整实现与评审可核对之后，再申请下列一次性实际窗口的授权；本次不允许进入guest或实际授予capability。carrier只接受preflight/stage/actual/post固定mode，无任意VM/path/command入口；旧actual/consumed modes一律不重放。

1. **只读preflight。** 重新检查host元数据/ELF/source hashes；用固定Lima shell（`--tty=false`、不preserve-env、不使用 `--start`/sync）进入固定VM。仅查询Linux/aarch64、实际boot UUID、systemd-run/systemctl版本（候选major255）、`/run`类型/owner/mode与至少16MiB free、新unit及stage不存在、system.slice已经active。VM/config/版本不同、路径别名、工具/权限缺失均拒绝，保存真实失败，不安装或修复。
2. **新stage。** 固定 `/run/switchback-deployment-probe1020-b3b` root:root0700；所有leaf以O_EXCL/NOFOLLOW新建，拒绝任何重名或symlink。复制exact ELF并验证长度/sha，文件0500；创建空diagnostic.json与diagnostic.stderr0600。创建范围只限这个新目录，不写 `/mnt/lima-sbquota`、key/config/publication或任何数据库。ELF≤8MiB，两个输出各≤32KiB，marker≤2KiB；不把这些payload上限当成SSH/journal等全系统写者的物理上界。
3. **单次actual。** 再核对boot/source/ELF/stage identity；host与guest分别先exclusive创建本phase的actual消费标记，包含phase_nonce/ELF/boot/unit绑定，再提交完整固定argv。任何失败、拒绝、timeout或不确定响应都消耗本次actual，不自动重试、改权限、换路径、启动既有服务或运行旧模式。
4. **保存结果。** systemd-run外层exit只作为transport/启动证据。独立读取manager的Type/Transient/ExecStart/User/Group/CapabilityBoundingSet/NNP/Seccomp相关声明、InvocationID、ControlGroup、ExecMainPID/Code/Status、Result/MainPID/ActiveState/SubState及资源限制。必须吻合stage中的exact ELF/argv和新unit。保持原始0/2；对DTO做独立schema/窗口/稳定性核对，exit2只表示稳定访问拒绝，不按errno猜唯一原因。exit1、缺输出、超长/部分JSON、unknown manager状态全部如实保留为失败/不确定，不合成成功DTO。
5. **post与停止。** 保存输出及状态之后，只在phase nonce、ExecStart、InvocationID/stage identity能证明ownership时stop这一新unit。验证MainPID0、owned cgroup无人、记录的PID/start ticks已结束或已变为不同identity。unit回收/不存在只能与完整pre/post证据合用，不能单凭not-found宣称成功。无法证明远端终止就保留unknown，不能拿本地limactl被kill替代远端清理。新stage与证据保留；不清理任何旧对象，不reset/mask/stop现有服务。

每个host/guest carrier窗口有限：preflight30秒、stage45秒、actual45秒、post30秒；不在工具里进行>60秒阻塞等待。actual unit的PID1超时独立于host SSH连接。每条命令和输出都保留起止时间、outer/nested exit、截断/timeout/termination字段及SHA256；每份输出读取limit+1，溢出即失败而非截断继续。子进程与传输的有限清理纳入单mode预算，不能把有界意图写成已经证明的硬deadline。

有限carrier与fixture只写新实施phase目录；本准备phase封存后保持只读，继续引用这里的exact ELF/argv/property与设计绑定。先冻结源码/ELF/property清单，再做编译/静态检查和独立操作复核。若实际反馈要求更改unit、carrier或pins，先保存失败事实，再走新候选评审；不能现场修改后重试本次actual。

## 🧩 B3-B1 本次离线实施范围

仅在新实施phase新增五个操作代码文件：`guest.py`（固定四mode与有限IO/manager原语）、`host.py`（固定Lima/清洁环境/source绑定/有界传输与raw receipts）、`test_guest.py`、`test_host.py`、`verify.py`（只读复核器）。其余manifest/fixture/报告仍放同phase。无产品Go、部署模板或既有文件修改，不安装依赖，不运行任何真实Lima shell、systemd、Engine或PG。

私有fixture端口注入原始stat/read/process/transport响应；实际main只能构造固定native端口。host公开mode只有preflight/stage/actual/post；任何额外VM/path/command/env selector拒绝。预算、DTO schema and field rules、命令、unit/property和ELF只来自本准备phase中哈希绑定的固定清单，不能由调用者或fixture结果覆盖。

离线先独立验证合法原语fixture，再跑可编译/可解释的行为RED，随后GREEN。覆盖每个有限原语的before/after故障/取消、limit+1及短写、源/ELF/boot漂移、重名与symlink、raw exit2保留、manager状态/InvocationID/cgroup/Start身份不符、actual一次消费、transport timeout/outer与nested不一致、清理ownership不确定以及本地kill不等于远端结束。所有成功与拒绝路径必须保留可核对raw回执，不合成native success。

独立评审通过、保护范围与准备seal重新核对后，交付可审阅的执行器及操作清单。B3-B1的完成标准不包含真正的Linux观察，不消费actual。实际执行将使用后续单独的新执行phase计数/证据，保留离线实施封存不变。

## ✅ 判定、范围与退出

本窗口成功的定义是取得exact ELF的一次真实、完整、可关联诊断，并证明本次进程结束；合法exit2同样可完成“诊断收集”，但不完成“权限可用”。若namespace拒绝被确认，再讨论保留权限、单独扩权或launcher方案，不预先批准SYS_PTRACE。

不会调用Engine/PG/provider、Create/Start Worker、修改quota/project/inheritance、初始化key/Trust/journal、启动publisher或Backend、修改生产gate；没有VM启动/停止或安装依赖的授权。若VM已不再Running，或任何前置条件不符，本窗口停止并交回事实。

所有963既有输入、1769 Git-visible文件和B3-A seal均须保全。本准备片只新增本文和本阶段准备证据，不改 `.superpowers/sdd/progress.md`；后续实施只在上述新实施phase内。当前与执行后均保持capacity_ready=false、execution_enabled=false、paid_consumed=false，OpenSpec38/41。

审阅请求是：接受这份窗口设计，先实施B3-B1有限执行器与离线故障回归，完成独立规格/质量及操作评审。实际guest preflight/stage/actual/post、root/CAP_SYS_ADMIN进程和unit停止读回都不在本次申请中；待具体实现、回执和评审可核对后再单独申请。此前B3-A批准明确只限离线，这份文件与build产物本身也没有授予执行权限。
