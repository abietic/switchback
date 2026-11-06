# M1 Darwin owned-group 收敛最小修复设计

_2026-10-23；候选设计，待用户接受。当前不修改生产源码、不重试真实准备。_

## 📍 问题与当前证据

kit-0008 / bundle-0003 的真实准备已按用户“继续推进吧”进入第一条只读 guest 检查。命令在约0.11秒内退出0，七个新目标均不存在；随后宿主 `killpg` 返回EPERM，监督器按当前规则将结果标为unknown并停止。复制、常驻Engine窗口与原资格窗口均未开始。原回执在 `.scratch/runtime-m1-resident-engine-execution-1023/guest-target-inventory.json`；其stdout原字节、退出码、未reap leader及cleanup结果均保留。不能用现在可能复用的旧PID/PGID回查当时组成员。

离线 `diagnosis/replay-cleanup.py` 从这份实际回执重放真实 `_cleanup_process`，仅替换系统调用，稳定复现exit0仍被EPERM阻塞；`red-replay.json`记录exit1，不执行native信号或guest动作。

宿主为Darwin25.5.0 / XNU12377.121.6。匹配版本的 [killpg1](https://github.com/apple-oss-distributions/xnu/blob/xnu-12377.121.6/bsd/kern/kern_sig.c#L1581-L1625) 跳过zombie，找不到可发送成员时POSIX分支返回EPERM。这证实已有实现缺少对应兼容路径，与本次故障吻合；当时组成员未采集，因此不能断言本次EPERM只有这一种原因，也不能直接忽略权限错误。

## ⚖️ 两种路径与选择

| 路径 | 收益 | 代价与风险 |
| --- | --- | --- |
| **A：完整组成员证明（推荐）** | 保留未reap leader与原kill尝试；只为Darwin EPERM增加有限只读证明，不增加进程、权限或公开协议 | 需要严格处理libproc错误、容量、PID绑定与前后waitid一致性 |
| B：保活sentinel/wrapper | 让组内保留可发送信号的活成员，避免正常EOF落入zombie-only情况 | 新增宿主进程与握手/退出协议，需改变原执行器和原资格driver监督流程，范围更大 |

不采用无条件忽略EPERM、提高宿主权限或reap后再次按数字PGID发送信号。

## 🔒 A 的准入条件

仅在Darwin、原 `killpg(owned_pid, SIGKILL)` 已返回EPERM、且原leader已有合法WNOWAIT终态时尝试补充证明；其他平台、错误码或未证实终态保持当前拒绝规则。

1. 仍持有该未reap子进程；`process.returncode is None`。重新用原reader确认exact PID及相同终态，ECHILD/已reap/不一致立即拒绝，不能查询或发送其他目标。
2. 使用固定 `/usr/lib/libproc.dylib` 的 `proc_listpids(PROC_PGRP_ONLY=2, owned_pid, ...)`。仅使用固定8192-byte缓冲区（2048个32-bit PID），不接受CLI/path/capacity override。实际调用前重置ctypes errno；返回值、errno与原始有界字节均留证。
3. 返回errno必须为0；byte count必须非负、对齐、严格小于缓冲上限。**只接受恰好4 bytes，唯一PID等于仍被持有的leader**。零、其他成员（即便也是zombie）、重复、负PID、截断、满缓冲、错误或不支持ABI一律拒绝。该条件不依赖逐PID权限过滤或可消失的进程状态查询。
4. 再用原非reaping reader确认exact leader及相同终态，仍未reap。前后条件成立才记录“group已由证据证明仅含终态leader”；保留原EPERM，不伪称发送信号成功。随后执行原有有界reap及退出码交叉核对。
5. 保留原deadline。查询前后均检查剩余预算；失败、超时和已有首个错误不被新证明覆盖。补充证明不能把原timeout/nonzero exit/输出截断改成成功，也不允许retry/resume或额外远程操作。

对应 [proc_listpids内核实现](https://github.com/apple-oss-distributions/xnu/blob/xnu-12377.121.6/bsd/kern/proc_info.c#L337-L458) 在同一proc-list锁下遍历allproc与zombproc并按PGID过滤，先执行全局权限检查，无逐成员权限省略。必须同时处理 [libproc wrapper](https://github.com/apple-oss-distributions/xnu/blob/xnu-12377.121.6/libsyscall/wrappers/libproc/libproc.c#L40-L54) 失败返回0的语义；因此不把空返回当作空组。对应版本源码、当前SDK头文件及SHA位于 `diagnosis/primary-provenance.json`。

证明范围仍是既有owned process group；不扩大为通用进程扫描/清理器，不授予其他PID的信号权限，不承诺已逃离该组的进程控制。

## 🧩 实施范围

只在 `deploy/experience/engine/prepare.py` 增加私有Darwin读者与终态组证明，并集中接到 `_cleanup_process`，让既有Engine `_bounded_run`、host transport和qualification driver共享同一实现；不新增公开CLI、分叉schema、额外bundle输入或第二套监督器。相关回归在现有 `test_prepare.py`，原资格共享loader回归按需要补充。Engine/qualification README同步准确语义。

不改Go权限、raw-v1/v2、Trust、PG、Start、两个正式窗口或资源/网络策略。保留两个publisher保护路径及全部历史证据。无commit/staging。SourceRoot受影响后通过原builder生成新的独占完整kit和下游bundle，不修改kit-0008/bundle-0003或其封存清单。

## 🧪 验证与交付

先用实际回执与同caller层面的假I/O回归捕获RED：正常EOF+EPERM+sole terminal leader应可完成；EPERM+live/额外成员、空/满/畸形返回、sticky errno、ECHILD、已reap、退出身份漂移、timeout与首错保留全部拒绝。测试真实共享loader能获得同一helper，导入和self-check不加载native库。

接受本设计后，除这些离线回归外，建议一次性允许最多三种本机隔离fixture：仅终态leader、leader退出而同组子进程仍活、运行中进程超时。每种一次、每个最多5秒、总30秒；固定当前UID Python子进程，`start_new_session=True`，无网络、无服务、无guest/Engine/PG/provider，只控制本次明确归属且未reap的进程组。其目的在新完整构建前验证实际Darwin ABI/errno与收敛语义，避免再次只凭fake判断平台兼容。

之后运行一套受影响全量验证、完整新kit/bundle构建与独立Spec/Quality审阅，核对全部来源/格式/43构建收据及历史保护，再封存具体候选。新的guest真实准备必须按新候选另行审阅；本次失败不重试、不续跑，Engine120秒和原240秒资格窗口当前均为0次。

## ✅ 设计自审

范围仅覆盖本次阻塞及共享监督器；没有权限提升或无条件忽略错误。完整枚举含zombie、wrapper零返回、errno复位、保留leader、读取前后身份、有限缓冲与原deadline均已明确；新native证明的真实性待隔离fixture验证。现阶段仅完成取证、RED回放与本设计，未实施上述行为。
