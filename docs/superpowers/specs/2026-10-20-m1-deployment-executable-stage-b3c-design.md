# M1 B3-C 可执行 stage 与启动前校验设计

_2026-10-20 · 待审阅；当前仅申请新候选的离线实施。本文不授权创建 guest stage、启动 unit 或重跑 probe。_

---

B3-B 的失败窗口已经消费并封存。当前只读核查确认：同一 boot、stage inode 与 exact ELF 所在的 `/run` 是 `noexec` tmpfs；root 对路径及已打开 ELF 的 `X_OK` 检查均为 EACCES。旧执行器只检查 tmpfs 类型与空间，遗漏执行策略。推荐把下一候选的完整私有 stage 放在固定 `/var/lib` 子目录，并把执行策略校验提前；保留原 ELF、unit 权限和单次执行语义。

## 📋 已核实事实与证据限制

| 事实 | 当前读回 |
| --- | --- |
| VM / boot | `switchback-core` Running；`751079a6-74a4-4570-bca6-18e460c7f33d` |
| 旧 stage | device27 / inode10412；root:root0700 |
| 旧 ELF | device27 / inode10413；root:root0500；5640057 bytes；SHA256 `a1309c87bc3868f275f05e4ec42c853f7b57c7c2c356ac616e94d0f769a62d27` |
| `/run` | `rw,nosuid,nodev,noexec,relatime`；`f_flag=4110`；`ST_NOEXEC=8` |
| 访问检查 | 路径与 `/proc/self/fd/<held ELF fd>` 的 `access(X_OK)` 均返回 -1 / errno13 |
| 只读检查进程 | real/effective/saved UID/GID均0；当前 security context为`unconfined` |

证据：只读 guest 原始回执 (private evidence retained)、带源码与传输的 receipt (private evidence retained)、失败窗口 (private evidence retained)。

原 actual 时没有采集 mount flags；因此“原拒绝与当前 noexec 阻塞一致”是有依据的推断，不是历史 syscall trace。当前检查进程的 capabilities/security context 也不是目标 SYS_ADMIN-only unit 的 credentials 证明。`/var/lib` 的存在、owner、mount与可写性尚未核查，后续固定 preflight 必须核验，不能用当前根挂载 ext4 的事实替代。

systemd v255.4 的 `run()` 在连接 manager、创建 transient unit 前调用 `find_executable()`；该检查会对已打开文件做 `X_OK`。这与失败窗口的启动错误、`LoadState=not-found` 和空 InvocationID 相符。上游 v255.4 用来解释机制，本轮没有取得 Ubuntu `255.4-1ubuntu8.18` 的完整已应用源码，不能声称完成发行包逐字节对应。[^1][^2][^3]

## 🧭 路线与范围

| 方案 | 影响与取舍 | 决定 |
| --- | --- | --- |
| 固定 `/var/lib` 私有 stage，先检查挂载策略与 X_OK | 需要明确接受 guest 根文件系统上的少量持久文件；所有载荷仍在一个 owned 目录 | 推荐 |
| 将 `/run` remount 为 exec | 改变共享挂载策略，扩大对既有服务的影响 | 不采用 |
| 通过 interpreter、loader或memfd绕过原路径 | 改变 exact executable/manager 关联方式，并绕过已配置的挂载执行限制 | 不采用 |

本次范围只覆盖独立诊断 carrier。publisher部署、Trust、PG/Start准入、Worker、Engine、quota/project/inheritance、已有unit/drop-in及生产协议均不变化。`capacity_ready=false`、`execution_enabled=false`、`paid_consumed=false` 保持。

## 🔐 固定新候选与持久写入边界

| 项目 | 候选固定值 |
| --- | --- |
| VM / 工具 / 环境 | 继承原固定 `switchback-core`、Lima2.2.1绝对路径与干净环境；每次重验Running，不自动启动 |
| stage | `/var/lib/switchback-deployment-probe1020-b3c` |
| unit | `switchback-deployment-probe1020-b3c.service` |
| nonce | `9c44cb69-ab9f-4910-9af3-7080d9b91f07` |
| ELF | 引用原准备封存中的相同5640057-byte ELF，不重建 |
| 离线实施 phase | `.scratch/runtime-m1-deployment-exec-stage-b3c-1020/` |
| 后续实际窗口 phase | `.scratch/runtime-m1-deployment-native-window-b3c-1020/`；离线阶段必须不存在 |

祖先 `/`、`/var`、`/var/lib` 必须已存在，以目录FD逐级no-follow打开；均为root:root、无group/world write，`/var/lib` 必须位于与已打开根目录相同的设备。held FD的fstatfs type须为`0xEF53`，并通过有界`/proc/self/mountinfo`中与相应路径和device对应的mount记录确认fstype为ext4；不能仅凭ext家族共享的magic证明ext4。mountinfo最大1MiB，解析失败、无法关联或观察不一致即拒绝；保留对应祖先/stage的原始记录。禁止创建缺失祖先、跟随symlink、选择备用目录或接受调用者路径。固定stage必须不存在；新建root:root0700，ELF0500，输出/marker0600，O_EXCL/NOFOLLOW与原子身份校验延续原设计。

未来实际执行会在guest系统盘留下ELF约5.64MB、每份不超过32KiB的两份输出与小型绑定/消费记录；文件在重启后仍可能存在。stage和证据保留，不自动清理，后续清理由独立owner动作决定。可用空间至少16MiB仅是这次复制的前置观察，不是持续容量、metadata增长或ENOSPC保证。此次设计审阅不产生上述guest写入。

root/CAP_SYS_ADMIN-only、NNP、AF_UNIX、RestrictNamespaces、syscall deny groups、CPU20%/memory128MiB/swap0/Tasks64、CPU2秒/FSIZE32768/CORE0、Startup10秒/Stop5秒及oneshot/remain-after-exit/SuccessExitStatus2均沿用原接受值。仅ExecStart和两个输出路径跟随新stage；完整argv/property必须在新候选中展开、冻结和评审，不修改原candidate或封存。

## ▶️ 校验位置与单次语义

```mermaid
flowchart LR
    accTitle: Executable Stage Admission Flow
    accDescr: Filesystem execution policy is checked before staging, then exact ELF access is checked before reporting staged. A separately authorized actual still consumes one attempt, and ownership controls cleanup.
    parent_check["检查固定 parent 挂载"] --> create_stage["创建私有 stage 与 ELF"]
    create_stage --> access_check["检查 exact ELF X_OK"]
    access_check --> staged["返回 staged 回执"]
    staged --> actual["另行授权单次 actual"]
    actual --> owned_post["证明 ownership 后 post"]
```

1. preflight在创建任何guest文件前，验证父目录身份、ext4、与根目录设备一致、可用空间和`ST_NOEXEC`/`ST_RDONLY`。从held FD取得fstatfs/fstatvfs原始整数flags；缺失、类型错误、bool冒充整数、负值或字段不一致均拒绝。保留其他flags原值，不把未知的其他bit当作exec保证；只对已定义的NOEXEC/READONLY做拒绝裁决。
2. stage在held父目录下exclusive创建新目录/文件，验证相同设备与完整ELF哈希，读取stage/ELF文件系统flags，再对fixed path和held ELF fd的proc路径分别进行`access(X_OK)`，保存原始return/errno。两者成功且身份稳定才返回`staged`；拒绝时保留不完整stage，host不得调用actual。该访问检查与systemd v255.4的调用方式一致，但不授予执行权限、不替代真实启动，也不消除后续TOCTOU。
3. actual仍在host入口先消费host一次性标记；guest按原ownership与消费顺序处理。消费后的任何失败均不能重试，包括后续挂载flags或访问结果漂移。启动前再次验证固定身份、ELF、filesystem与X_OK；不能因为前两阶段通过而省略。unit创建/启动仍只发生一次，原始probe0/2与外层exit分开保存。
4. post只依据完整ownership链保存证据并停止新unit。不得把“当前挂载又变为noexec/readonly”自动当作拒绝已证明归属unit清理的理由：路径身份与事实读取、launch可用性裁决应分开。若所有权或证据不足仍保持unknown，不扩大Stop目标、不重新取得未知ownership。沿用原完整actual transport缺失时的post限制。

所有mode总预算仍为30/45/45/30秒，含有限取证与清理；不宣称能硬终止不可中断IO。host/guest schema与严格mapper一起演进为新候选内部版本；旧receipt保持原样，不能把v1缺失flags静默补成可执行。stage成功、actual诊断收集成功和权限有效仍为不同事实。

## 🧪 离线实施与验收

新phase中的五份source以原封存carrier为基线复制后定向修改：`guest.py`、`host.py`、`test_guest.py`、`test_host.py`、`verify.py`。新增绑定candidate，明确区分旧ELF的来源seal和新nonce/stage/unit/argv；禁止改原PREP或release以令旧校验误通过。生成新plan、source/frozen manifest、完整测试receipt、独立评审及seal。

| 必须覆盖的情形 | 预期 |
| --- | --- |
| 本次真实noexec事实映射到原始filesystem原语 | 在preflight拒绝；没有stage写入或actual消费 |
| 缺flags、负值、bool/string、RO、错误type/device、祖先symlink/owner/write位 | 拒绝；原始事实保留 |
| 合法ext4可写可执行parent及exact ELF | 可返回staged；无unit调用 |
| stage后flags变化、X_OK EACCES/EPERM/其他errno、path/fd分歧、inode/ELF漂移 | 失败关闭；已消费actual保持消费 |
| 每项新原语before/after故障/取消、短读/溢出、FD关闭失败 | 有界失败与完整/不完整证据如实标记 |
| post时NOEXEC/RO变化而完整ownership仍成立 | 不凭launch条件阻断owned Stop；失败仍保留unknown |
| 旧receipt/旧nonce/旧stage被当作新prior或authorizing evidence | 拒绝，不fallback |
| 原host raw/DTO/manager/GC/预算与group cleanup回归 | 保持既有拒绝、终止和证据语义 |

先建立针对真实bug模式的离线RED，再GREEN；fixture/故障注入不等于真实Linux mount或unit成功。本轮捕获回执的两次RED只能证明症状可稳定检出，不能作为修复验证。后续新候选完成源码与离线验证后，再申请绑定其seal和完整操作清单的独立真实窗口；当前不进入实施，不启动该新窗口。

## 🔗 决策与参考

待用户接受：采用固定`/var/lib`私有stage及上述执行策略校验，先完成新候选离线实现。接受设计只授权源码和离线验证；新的guest持久写入、root/CAP_SYS_ADMIN unit和actual仍需单独明确授权。原B3-B的43项窗口证据、613项carrier封存、当前970项输入和1771项Git-visible内容均保全。

[^1]: systemd-stable v255.4. `src/run/run.c`，特别是run的执行路径检查与随后manager连接。https://github.com/systemd/systemd-stable/blob/v255.4/src/run/run.c#L1744-L1818
[^2]: systemd-stable v255.4. `src/basic/path-util.c`，check_x_access与find_executable_impl。https://github.com/systemd/systemd-stable/blob/v255.4/src/basic/path-util.c#L563-L619
[^3]: systemd-stable v255.4. `src/basic/fs-util.c`，access_fd。https://github.com/systemd/systemd-stable/blob/v255.4/src/basic/fs-util.c#L614-L630
