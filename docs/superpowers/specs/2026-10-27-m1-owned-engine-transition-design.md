# M1 已有 Engine 的归属证明、受控停止与 kit23 切换

状态：待用户审阅。本文提出完整的私有操作器设计；当前只写设计和核对本地证据，不授权进入 guest、Stop、归档现场对象或启动环境。

## 目标与已确认事实

目标是在保留现有存储和全部失败证据的前提下，使当前部署回到原 Engine executor 接受的干净安装边界，再运行已经交付的 kit23。成功标准仍为真实资格与 Core 首启，随后另做 Task→报告→页面接受→同任务观测关联；本文不将 Engine ready 当成产品闭环完成。

kit23/bundle 已重新核验：封存 SHA256 为 `733cacec519a657c61a437dfa8a74f16b6a8c63412acc08ee34c32f0d9111c95`。kit manifest 为 `fb5d88d49ae6252bf72558339d954f71927b35fe4a74dba55e77fe8404b693bf`，bundle 为 `fa8e504846b756fcde1e25a5e77038dae7369be90f1701f733c59abdb1127f98`。四份 Engine 安装文件在 kit21 与 kit23 中逐字节相同；需要改变的是切换载体与运行时观测，当前没有重建 Go/Engine 候选的理由。

保存的 kit21 实跑表明两条 Start 成功，随后 engine_ready 失败；失败后没有再次进入 guest。以下是历史绑定，必须在未来同一操作内重验，不能声称当前仍成立：

| unit | PID / start tick | executable | cgroup |
| --- | --- | --- | --- |
| switchback-m1-containerd.service | 68181 /117500050 | /usr/bin/containerd | /switchback.slice/switchback-core.slice/switchback-m1-containerd.service |
| switchback-m1-engine.service | 68199 /117500083 | /usr/bin/dockerd | /switchback.slice/switchback-core.slice/switchback-m1-engine.service |

历史 guest boot 为 `751079a6-74a4-4570-bca6-18e460c7f33d`。exe SHA、四文件 SHA、已有三 socket 身份和输入回执哈希保存在 `.scratch/runtime-m1-active-engine-transition-design-1027/saved-basis.json`。三个附加 socket 的实际 FS 元数据仍缺失，不能补造。

现行 `ownership_guard.Quiescence.manager` 即使 unit 为 inactive/dead，也拒绝任何同名 loaded unit。Source `_unit_catalog` 只要在 installed/loaded 全量清单中见到这两个名字即拒绝。旧 `installed_preserve` 还绑定旧的九节点/空 runtime 目录，不能原样用于当前活动 runtime 子树。因此简单 Stop 或更换 kit 路径均不足以继续。

## 路线比较与推荐

| 路线 | 具体做法 | 取舍 |
| --- | --- | --- |
| **A，推荐：受控停止并原样保留** | 证明 exact 旧部署无业务占用，停止两条已证明归属的 unit，保存安装物/运行目录/副本与旧 stage，卸载旧定义，再交回未修改的 v2 Engine executor | 需要新私有切换载体与明确的 Stop 设计；复用现有 v2 正式准入、kit23 与存储 |
| B：接管当前运行实例 | 对旧运行目录作权限修正，定义新的 adopt/requalification 回执与 Go 消费语义，再绑定新 Source | 当前没有成功 v2 receipt，libnetwork 父目录权限也未实际取证；会扩大生产合同和证据解释范围 |

采用 A。仅新增私有维护/准备载体及回归，保留 Source 的完整 v2 合同、原 unloaded-only 门禁和现有 PG/Trust/quota/Start 规则。不存在通用的“忽略 loaded unit”开关。

## 归属证明与可接受状态

未来操作开始时先重验 kit23、Source/templates、旧证据、HEAD/index、宿主目录身份和 VM 已 Running。VM 非 Running、boot 变化或候选漂移立即终止，不启动 VM。

新 OwnerObservation 只供操作器决定能否操作旧部署，始终 `authority=none`，不作为容量、Start 或 v2 readiness 证据。必须收齐初末两个观察：

- 四份安装文件的 bytes/hash/owner/mode/nlink 与 kit21、kit23 一致；已加载对象的 FragmentPath、DropInPaths、ExecStart、ExecStop/ExecStopPost、Restart、KillMode、停止超时与有效配置也一致。禁止凭同名 unit 或 PID 单独认领。
- 只查询全量 manager 清单中已经 loaded 的目标；记录 jobs、ControlPID、InvocationID、MainPID、cgroup、触发关系及停止传播/反向依赖。没有未授权目标可能因 Stop 被连带停止，且目标没有冲突 job，才允许继续。默认 Docker/socket mask 维持原状。
- active/running 的目标必须匹配表中 boot/PID/start/exe/hash/cgroup；只接受已证明的同一次实例。已自然停止的目标需明确 MainPID/ControlPID=0、无 job、cgroup 空；存在 failed 等状态时仍须证明没有未识别进程。重启后的新 PID、状态过渡、未知属性或缺证均不自动接管。
- 完整 process/FD/maps、Unix/TCP/TCP6、六角色 socket/父目录观察，以及全部旧副本、安装物、stage、归档父目录身份。当前操作器自己的 PID/start 和精确 held FD 单独绑定；没有泛化“当前 UID/root 全部豁免”。
- 新 OwnerObservation 可记录历史 libnetwork 父目录的 root:root 私有 `0600` 或 `0700`，只为归属/保留；正式 v2 仍必须严格 `0700`。未知 socket FS 身份只能来自这次实际双样本，不从历史路径或合成正例推断。
- 需要调用旧 Engine 时，仅在完整 owner/socket 证明后使用固定 Unix API 做 `/info`、`/version`、容器/volume/plugin/network 只读盘点，证明没有业务容器、volume、plugin、Worker/shim 或其他使用者。无当前活动实例时不进行 API 连接。不会用 HTTP 成功替代内核/文件身份。

观测按停止阶段区分：两 daemon 活动时要求完整六角色；Engine 成功停止后只允许原 containerd 的两角色；两者停止后要求零 daemon 监听。已退出 daemon 的旧 socket 文件可以残留，但不能再有监听/FD引用；不能把初始六角色要求套到停止后的状态。每步重验剩余实例身份，允许由这次成功 Stop 解释的 FD/manager 变化，其他新使用者或实例漂移继续拒绝。

T1 同时运行原有 data-root 恢复状态分类和完整进程/挂载检查，要求业务/恢复引用为零；Engine 活动时再与 API 空业务盘点交叉核验。Engine 不活动不构成跳过恢复检查的理由。归属窗口只允许读取既有状态，不连接/启动 PG、不执行 containerd 业务命令。

已保存的身份继续精确匹配。少数此前未保存的新 archive 根 inode，可在同窗口通过固定私有父目录、完整已知子树/manifest/hash与初末稳定观察建立首次实际绑定；明确标注 first-observed，不伪称历史已观察。缺少子树覆盖仍拒绝，不能凭内容相似猜 inode。

## 同一次执行中的固定顺序

| 阶段 | 动作与完成条件 |
| --- | --- |
| T0 本地验证 | 独占新的 execution 目录与一次 grant；核对全部输入，记录无凭据内容的审计；只检查 VM Running |
| T1 旧实例归属 | 完成上述双样本、manager 停止传播边界、业务空闲与路径证明；失败时尚未 Stop/移动旧对象 |
| T2 停 Engine | 立即重验归属后，最多一次 `systemctl --job-mode=fail stop switchback-m1-engine.service`；只有原来已 loaded 的已证明对象可被选择。已不存在且完全静止的对象跳过，不通过 show 加载它 |
| T3 停 containerd | T2 成功且后置证明通过后，重验 containerd 归属，最多一次同类 Stop。停止按既有 unit 的 KillMode/timeout 执行，可能终止该 cgroup 进程；不直接 kill/killall、不使用 restart |
| T4 静止证明 | 每个成功 Stop 后，在同窗口核验 MainPID/ControlPID/job、cgroup、完整 process/FD/maps、监听；相关 unit 无运行进程、无外来引用。exit0 本身不作为静止证明 |
| T5 原样保留 | 独占新 archive 父目录并 no-replace rename 两 unit 文件、配置目录、guest Engine stage 和完整 runtime 子树；保留宿主已消费 stage、四份 kit21 副本及所有既有 archive。持久记录每个 held 根身份和每次成功移动 |
| T6 退出旧定义 | 仅一次 `systemctl daemon-reload`，随后以全量 installed/loaded 清单验证两个旧定义已退出，默认 masks 未变、jobs 无目标任务；最终仍调用原未修改的空目标/静止检查。若 manager 仍持有它们则停止，不豁免 |
| T7 新部署 | 独占复制四份 kit23/bundle，精确全树校验，再执行原一次120秒 Engine 窗口；由 Source 原样预建0700运行父目录并生成真实六角色 v2 receipt |
| T8 条件首启 | 只有 Engine 全部通过，才沿既有方案进行镜像、新秘密/新PG迁移、原 Native 双能力有限采集及一次240秒资格→安装→首启；300秒 barrier 和既有门禁保留，不调用模型 |

停止后的 `/run/switchback-engine/docker.sock` 位于 `m1-resident` 外，必须单独处理：已消失则记录 absent；若残留，只有 exact 旧 socket inode/type/owner/mode、无监听且无 FD 引用的对象可以 no-replace 原样移入该父目录下的新 archive，不能删除或把任意 socket 当旧对象。runtime 子树允许记录并保留已证明的 socket 节点，普通文件按哈希、目录按身份覆盖；未知 special file、symlink、嵌套挂载或无法稳定覆盖即停止。原空目录 `_snapshot` 不会用于此非空目录。

保留根路径固定在 `/Users/example/.switchback`、`/opt/switchback`、`/var/lib/switchback`、`/etc/switchback`、`/etc/systemd/system` 和 `/run/switchback-engine` 内；使用新一轮明确的 `archive-owned-engine-1027-kit-0021-to-0023` /`.preserve-owned-engine-1027-kit-0021` 名称。父目录必须先持有并核验同 FS/no-follow 身份，目标必须不存在。`/mnt/lima-sbquota/docker` 数据池及已有 archive 留在原地，不做递归删除、分区、迁盘、unmount 或网络手工清理。

原私有九节点 preserver 由新的 maintenance-preserved 回执分支替代，只有这个 same-process、已消费、绑定 exact old/new deployment 与完整移动/静止证据的回执才能进入后续准备。该回执不授予 SessionDispatchPermit/Start，不能单独拿去重跑。原 Source 预检不接收维护豁免。

## 预算、失败与授权边界

新维护切片固定120秒，覆盖 T1–T6；原 Engine120秒、资格240秒、barrier300秒及整个准备7200秒保持原值，维护时间包含在原总预算内。维护程序单独传输，不把它塞进仅余387B的旧模板；每个实际 argv 仍严格小于65536，原 frame/output/audit 上限保持。实现与最终封存须给出全部程序的真实长度和解码 Source/hash/module 绑定。

部署 owner 沿用已选定的冻结部署模型，操作器持有协作锁并在每个不可逆动作前复核对象。systemctl 的按名称 Stop 不是 PID 原子 fencing；明确不承诺阻止绕过 owner 冻结流程的 root/其他 manager 客户端并发替换实例。

任何命令非零、timeout、截断、未知状态或后置证明失败，都终止整个驱动。只有成功命令后的预先定义后置读取属于本窗口；失败后不新增 guest 调用、Stop、重试、独立 readback 或自动恢复。若第二次 Stop/后续部署失败，已经停止或移动的对象保留审计，不自动重启旧 Engine。保留本地既有 stdout/outcome 和原始错误，保存失败也尝试关闭全部已持有本地句柄。

本阶段接受设计后**仅离线实施**：完成 mapper、维护执行器、载体重绑、故障回归、真实历史回放、完整候选/长度绑定与独立复审。kit23 Source/Go binary 与正式准入不改；若发现必须改变正式合同，先报告具体原因。没有 root guest 执行、Stop、key/secret读取或环境授权。实际操作需等具体载体及封存清单可审阅后，再单独确认一次完整窗口，而非反复拆出事后只读窗口。

## 离线验收

必须覆盖：active pair/部分已停止/已 unloaded；boot/PID/start/exe/hash/cgroup/文件或 loaded effective 配置漂移；隐藏 daemon/shim、foreign writer/FD/maps、未知 socket、业务占用、反向依赖/trigger/job 冲突；Stop exit0但 PID/cgroup/监听仍活、Stop未知后无任何新动作；非空 runtime 和独立 Docker API socket 的全树原样保留、symlink/mount/跨FS/目标占用/rename失败；reload后仍loaded拒绝；输出满、损坏、审计失败与close失败保持原工作错误。必须证明同一 grant 不可重放、旧维护回执不授权 Start、旧 production guard 未放宽，以及新输入和全部程序满足原字节/时间边界。

## 依据与自检

- 当前 Source `deploy/experience/engine/guest.py:_unit_catalog`、`_collect_listeners`；当前私有 `ownership_guard.py:manager` 与 `installed_preserve.py:_snapshot`。其严格 unloaded/空目录语义是 A 必须先完成维护的依据。
- [systemd v255 systemctl 官方手册源码](https://raw.githubusercontent.com/systemd/systemd/v255/man/systemctl.xml)：Stop、manager reload 与 loaded/installed 清单分别处理不同状态；reload 不等于停止服务，也不保证所有 unit 立即退出 manager。本文以实际清单/进程后置证明为准，不以手册替代现场事实。
- kit23与旧实跑保存输入的 bytes/hash及四安装文件等价证明见新 design phase 的 `saved-basis.json`；当前 VM/daemon/systemd 实际版本与状态仍未读取。

自检：路线、固定操作顺序、所有危险动作及失败停止边界均明确；没有未定义的恢复/绕过、凭空 live 身份或凭据内容。本文的120秒维护预算及 owned Stop/归档/manager reload 是新私有行为设计，需要先获设计接受；未来实际权限仍独立绑定最终载体。
