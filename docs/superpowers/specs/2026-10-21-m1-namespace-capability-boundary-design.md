# M1 namespace 观测权限修订与离线候选

日期：2026-10-21。状态：待用户确认权限边界；本轮仅完成分析和设计，没有修改运行配置或实际授予能力。

## 当前事实与判断

上一个真实窗口已经消费。新 ELF 成功生成 v1 packet，PID1 before/after ticks=0得到保留；四项PID1 namespace为EACCES、探针exit2。独立纯mapper修复已完成，原始3140-byte packet在新mapper下格式有效，仍保留这些拒绝事实。它不是完整namespace或部署资格证明。

原始packet SHA256：5a9b13ddc411556e76ab12cceecf14438b6154688802cf4ffabda3d604f91cb9。caller有效能力为0000000000200000，PID1 permitted为000001ffffffffff；两者UID/GID均为0。caller没有CAP_SYS_PTRACE，其能力也不覆盖PID1 permitted集合。计算记录见 .scratch/runtime-m1-namespace-access-analysis-1021/capability-analysis.json。

上游Linux6.8的proc namespace链接跟随和readlink均执行PTRACE_MODE_READ_FSCREDS检查；commoncap要求同user namespace下caller能力覆盖目标permitted，或者具备目标user namespace的CAP_SYS_PTRACE。因此当前能力集合存在足以解释拒绝的缺口，改用readlink也不能避开同一检查。[proc namespace源码](https://raw.githubusercontent.com/torvalds/linux/v6.8/fs/proc/namespaces.c)、[commoncap源码](https://raw.githubusercontent.com/torvalds/linux/v6.8/security/commoncap.c)。

这仍是结合真实回执与上游规则的判断：目标为Ubuntu6.8.0-142-generic，本轮没有采集其vendor补丁、dumpability或LSM/audit事实，也没有实测新增能力。不能宣称唯一拒绝分支已现场证明，或新增能力一定足以通过所有LSM。

当前源码确认：

- deployment_observe_linux.go用Openat(O_PATH)及Fstatat跟随proc namespace链接；pool_native_linux.go的publisher collector也Stat /proc/1/ns/mnt与cgroup。
- 未部署publisher模板仅有CapabilityBoundingSet=CAP_SYS_ADMIN。
- verifyPublisherDeployment和Backend的verifyCapacityDeployment均仍固定返回unavailable。任何诊断回执或能力模板都不能直接开启它们。

## 选择与代价

| 路线 | 收益 | 代价/限制 |
| --- | --- | --- |
| 专用publisher与一次性诊断进程使用CAP_SYS_ADMIN + CAP_SYS_PTRACE | 推荐。保持当前单进程collector，最小化实现和部署拓扑变化 | 新增能力作用于整个进程，可能影响guest内其他进程，需要明确接受权限边界 |
| 独立CAP_SYS_PTRACE namespace观察器，向publisher提供绑定证据 | 可拆开两个进程的能力集合 | 新增进程、IPC认证、boot/PID绑定、时序与失败语义；当前M1成本明显更高 |
| 保持当前权限，继续关闭准入 | 不扩权限 | 保留受限诊断，当前collector无法获得所需namespace事实，不推进生产启用 |

不采用忽略EACCES、把PID1 namespace当成caller、使用路径/boot字符串替代namespace身份、修改PID1/LSM/sysctl或取消全部capability限制的路径。

## 拟接受的权限边界

未来专用进程的固定能力集合为：

```ini
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_SYS_PTRACE
```

相应位图为0000000000280000（bits21与19）。维持root/Group=root、NoNewPrivileges=yes、AF_UNIX、RestrictNamespaces=yes、原SystemCallFilter和资源上限；ambient仍为0。不为Backend、Worker、其他unit或macOS宿主增加能力。

CAP_SYS_PTRACE不是“只读namespace权限”。它允许跨进程观察/控制相关检查，在guest内可涉及其他进程的内存和状态。固定代码路径、NoNewPrivileges和现有filter不能把整进程能力缩成一个只读接口。该边界仅适用于已选择的专用受信任单用户Linux guest。[ptrace权限与影响](https://www.man7.org/linux/man-pages/man2/ptrace.2.html)。

本次若接受，仅批准源码和离线候选；不安装/启用unit、不capset或设置file capability，不运行新的原生进程，不初始化key/Trust/journal，不调用Engine、PG、provider，也不改变三个生产门禁。

## 一次完成的离线实施范围

1. 修改未部署模板 deploy/experience/switchback-capacity-publisher.service 的固定能力行与风险注释，同步publish_test.go的TestPublisherProductionConstructionAndUnit严格期望。保留所有其他unit配置；TestPublisherDeploymentAlwaysClosed继续验证生产入口拒绝。两个生产部署验证函数原样保留。
2. 复用已经完成的v1单次window carrier与新纯mapper，在下表的新候选目录离线组装。只更新固定身份、双能力launch/property、源码/receipt绑定、批准文档与对应fixture期望。manager mapper、once、raw-first、父/worker监督、30/45/45/30秒和总150秒、失败停止、禁止retry/resume/独立readback/Stop均保留。
3. 复用现有ELF ae851af5d68277d80d4135d7b8e1f5a435554d991f971698fd22f4eacb4228e0（5659311 bytes），以及真实既有build receipt 7e7ceaf4000367aec3b2a3876bfb92d8ca54bce67695af2b13e1ec29c95a54f6。明确标注复用既有构建；重核参与输入和快照，不伪造新构建回执。只有测试/模板变化不要求重建Go ELF；构建输入漂移则停止报告，不自动替换产物。
4. 新mapper仍精确绑定新unit的完整绝对cgroup与caller_thread。旧pid1真实packet作为跨unit拒绝例，不能修改旧packet冒充新unit观测。新unit完整/受限packet仅使用标注synthetic的离线fixture；真实成功只能来自后续新授权窗口。
5. 两个明确修改的模板/测试路径记录exact old/new摘要与旧frozen副本；其余当前源码与旧候选、真实窗口、seal/metadata保持。旧977项历史记录不改写，旧frozen_ref仍核验；新候选的当前路径校验只允许这两个已批准修改及已有PID1修复的固定摘要，不添加通用fallback。
6. 完成针对unit配置、关闭入口、双能力/额外能力漂移、路径/namespace/zero语义、候选binding和原carrier故障边界的相关回归与独立审查，形成实际哈希和新操作清单。当前仅设计，不执行这些新增测试或生成候选程序。

本片不添加新的Go API、保证版本、raw格式、签名授权、PG预算、Start owner、IPC协议或env/CLI放行开关。

## 固定新身份

| 项目 | 值 |
| --- | --- |
| 候选 | .scratch/runtime-m1-namespace-capability-candidate-1021 |
| 真实窗口 | .scratch/runtime-m1-namespace-capability-window-1021 |
| guest stage | /var/lib/switchback-deployment-diagnostic1021-nscap |
| unit | switchback-deployment-diagnostic1021-nscap.service |
| cgroup | /system.slice/switchback-deployment-diagnostic1021-nscap.service |
| nonce | 76c393e4-32e8-47c0-9f05-c32619124827 |

宿主候选与窗口目前不存在。guest stage不存在性尚未检查，未来只能在新授权窗口内预检；不为设计另开guest调用。任何固定路径冲突即停止，不更换身份绕过once。

## 验收与剩余门禁

离线完成只表示候选和权限声明自洽、测试/审查通过。新的真实窗口必须在具体候选和操作清单完成后单独授权；只在现有switchback-core已Running时运行，不启动VM。若未来仍出现EACCES/EPERM或任意不确定，保留原文并停止，不自动增权或重试。

未来真实窗口须分别记录父/worker最终退出、一次launch、probe exit、八项namespace及before/after、caller精确能力、unit/resource属性与cleanup。probe exit2仍只表示受限诊断；即使exit0与完整namespace事实全部出现，也不能把diagnostic_only/authority=none转换成准入权。

本设计修复的是权限契约并准备完整观测。之后仍须实施独立生产部署验证器，验证其要求的host backing、inventory、目录/密钥隔离、原生资源约束和own-core绑定，才能讨论容量发布/消费；再验收Create→quota→Start与真实产品体验。两个unavailable门禁本片不打开。
