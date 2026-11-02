# M1 B3-A 部署身份与 namespace 访问诊断设计

_2026-10-20 · 设计候选，待用户审阅 · 本文不授权 Linux 执行、启动环境或增加 capability_

## 🎯 目标与当前边界

先把 B2 留下的 namespace 权限疑点变成可观察、可区分的事实，再设计部署验证器。推荐本片只实现固定的只读诊断入口及离线回归。它没有私钥、签名、Engine/quota/PG 调用，也不替代宿主 backing、安全隔离和完整 writer inventory 验证；两个生产 deployment gate 继续固定 unavailable。

B2 当前源码与921项封存证据已在本阶段开始时重验：954个输入、1760个 Git-visible 文件，3728项完整回归与255项定向 race 的历史结果均有原始回执。它们证明 B2 的源码/离线范围，不证明目标 Linux 中的真实 namespace 权限。[^b2]

当前存在两道不同门禁：publisher 的 `verifyPublisherDeployment` 在 key/Store/Engine/native 构造前拒绝；Backend 的 `verifyCapacityDeployment` 在 Reader/observer/PG Trust 初始化前拒绝。`ReadAdmissionManifest` 的 digest 比对只绑定结构性字节。本片不把这两个拒绝改为允许。[^gates][^metadata]

## 🔍 已核实的权限条件与路线比较

Linux v6.8 的 proc namespace magic-link 跟随路径会调用 `PTRACE_MODE_READ_FSCREDS`。commoncap 使用 caller effective 与 target permitted 集合及 user namespace；`CAP_SYS_ADMIN` 不能直接替代该判定所需条件。PID1 的实际 credentials、dumpable、LSM 和最终 publisher principal 未探测，因此没有“目标已失败”的结论。[^proc][^commoncap][^research]

| 路线 | 收益 | 代价与边界 | 选择 |
| --- | --- | --- | --- |
| 保持权限，先做固定只读诊断 | 获得实际访问结果与 principal 字段，避免猜测；可先完成离线代码 | 诊断不签发部署证明；实际 Linux 执行另行授权 | 推荐 |
| 现在给 publisher 增加 SYS_PTRACE | 对某些已确认 commoncap 阻塞，代码改动可能较小 | 扩大进程检查/内存访问能力，仍受其他 LSM；当前没有 exact-target 证据与扩权批准 | 本片不采用 |
| 删除 PID1 对比或直接接受 launcher 声明 | 可减少 publisher 的直接 proc 访问 | 改变既有身份来源，必须重新设计 launcher 的权限、关联、失效和篡改边界 | 保留为独立方案 |

standalone probe 与最终 publisher 是不同 principal。probe 成功最多说明 probe 自己的这次读回；不得作为 publisher 已通过的替代 receipt。完整 verifier 以后必须在最终 publisher 自身执行必要观察，并将其与外部受信任部署边界绑定。[^research]

## 🧩 本片接口与事实模型

新增固定无参数数据源：

```go
func ObserveDeployment(ctx context.Context) (DeploymentObservation, error)
```

生产函数自行构造 concrete Linux backend，不接收 PID/path、expected 值、权限、文件内容、clock 或 verifier options。仅支持 Linux amd64/arm64；nil/cancelled context、unsupported 平台或必要读回失败返回固定错误与零 Observation。测试使用同包私有有限端口，不新增 exported factory 或 mutable global hook。

`DeploymentObservation` 是 unsigned diagnostic DTO。其 JSON schema 固定为 `switchback.capacity.deployment_diagnostic.v1`，`kind=diagnostic_only`；不含 Trust、Guarantee、manifest/receipt、ready、permit 或可消费的 deployment verification token。

| 内容 | 实际字段与规则 |
| --- | --- |
| 窗口 | 实际 Started/Ended；boot UUID before/after；kernel release。按去除monotonic部分后的输出wall clock比较，零值、相等/回退、boot漂移拒绝；不是原容量30秒窗口的新实现 |
| Caller | 当前调用线程的 PID/TGID/TID、stat field22 ticks、Uid/Gid 四元组、五类 Cap、NoNewPrivs、Seccomp、unified cgroup path；before/after 必须稳定 |
| PID1 | 当前 procfs 视图下 PID/TGID=1、start ticks、相同的 credentials/capability/cgroup 字段；before/after 必须稳定；不据此自证它是专用 guest 的真正 PID1 |
| Namespace | caller-thread 与 PID1 各自的 user/pid/mnt/cgroup，固定8项，每项保留前后两次结果；成功记录实际 nsfs device/inode，拒绝记录精确 EACCES 或 EPERM 类别 |
| 缺失与拒绝 | namespace 的上述两类拒绝可作为诊断值返回，Device/Inode 必须为空；成功时必须有实际值且 errno=0。ENOENT、ESRCH、其他IO、解析错误或前后权限/身份变化使整轮返回零 Observation |

namespace 标识在 JSON 中用规范十进制字符串保留 uint64 精度，Cap 使用精确16位小写 hex。cap_last_cap 必须实际读得且在0..63；更大值视为当前编码 unsupported，不能截断。未知 status 普通字段丢弃，所需字段缺失/重复/非法/溢出拒绝。NoNewPrivs/Seccomp 是观察值，不证明某份 unit/filter 已生效。

没有 PID1 dumpable 字段。不能把 caller 的 PR_GET_DUMPABLE、CoreDumping 或 LimitCORE 填成目标 dumpable；读取被拒绝也不按 errno 猜测唯一原因。不能使用 lstat 得到的 proc symlink inode 代替所指 namespace 身份。[^research]

## ⚙️ Concrete 读取、线程与资源边界

`ObserveDeployment` 在 native 观察期间使用 `runtime.LockOSThread`，全部资源释放后 Unlock；caller 的字段取自 `/proc/thread-self`，并与实际 getpid/gettid 对齐。这样观察的是发起 namespace syscall 的线程，而不是随便选进程 leader 的 credentials。Go 官方接口约定支持把调用 goroutine 绑定到其 OS thread。[^go]

只读范围固定为 `/proc/thread-self` 与 `/proc/1` 的 status/stat/cgroup/ns 四种目录项，以及 `/proc/sys/kernel/random/boot_id`、`/proc/sys/kernel/cap_last_cap` 和 uname。不会读取 environ、cmdline、maps、mem、fd 目标、LSM 策略、key/config/publication 目录或 Docker socket；不会调用 ptrace、process_vm_*、capset、setns、unshare、mount、quota 或启动子进程。

proc 目录使用固定 magic-link 入口、held FD 和 procfs 身份检查；不允许一般路径注入或把其他 filesystem 的伪造文件当成 proc。以同一 held proc directory 的 fstatat 跟随固定 ns 子路径；成功后用 O_PATH 跟随打开该 namespace，仅做 Fstat/Fstatfs，确认实际 nsfs type/device/inode与stat一致，随即关闭。stat成功而打开失败或身份不同不能改写成稳定denied。前后 process identity 与 independently reopened canonical entry 对齐。复用现有 stat field22/cgroup 的纯解析规则，不改旧 pool sampler 的 context、FSXAttr 或权限逻辑。

诊断不预先要求caller UID=0、NoNewPrivs=1或caller与PID1 namespace相同；这些均保留实际读回。它可以完整记录一个与预期unit不同的principal，仍没有“该部署符合要求”的含义；未来verifier负责受信任expected值及同principal匹配。

status 单次最多64KiB，stat/cgroup 各4KiB，boot64 bytes，cap_last_cap32 bytes；均检查第 limit+1 字节。固定8个 namespace 项、16次逻辑访问，每次成功包含上述有限stat/open/identity/type检查，无截断、重试循环或 detached syscall goroutine。固定两份 proc directory FD，同步临时 leaf或namespace FD最多一个；每个 owned FD 在错误和取消路径只关闭一次。命令使用5秒 cooperative context；不承诺内核调用硬5秒返回。

```mermaid
flowchart LR
    accTitle: Deployment diagnostic authority boundary
    accDescr: Fixed proc observations produce unsigned diagnostics. Production deployment gates remain closed until independent host and native verification is separately designed and accepted.
    fixed_proc["固定 proc 与 uname 观察"] --> diagnostic["Unsigned diagnostic_only"]
    diagnostic --> evidence_review["审阅实际字段与访问拒绝"]
    evidence_review --> future_verifier["后续同 principal 观察及独立部署验证"]
    closed_gate["当前 publisher / Backend gate unavailable"]
    future_verifier -.-> closed_gate
```

## 🖥️ 命令、文件与兼容

新增零业务参数命令 `switchback-deployment-probe`，实际 main 固定绑定 ObserveDeployment，不读取 stdin/env 配置，不提供 PID/path/enable 参数。先完整编码并检查<=32KiB，再向 stdout 写单个 JSON；无 banner/body/raw错误。全部必要读回完整且 namespace 均成功时 exit0；存在稳定 EACCES/EPERM 诊断时输出相同 schema 并 exit2；unsupported/必要IO或解析失败时 stdout 空、固定 stderr reason、exit1。任何 exit code 都没有 admission authority；短写/编码失败不能返回0或2。

| 新文件 | 责任 |
| --- | --- |
| `internal/platform/capacityprobe/deployment_types.go` | DTO、固定 enum/schema 与错误；无IO或授权 |
| `internal/platform/capacityprobe/deployment_observe.go` | 纯解析与有界编排、窗口/前后稳定性、零失败结果 |
| `internal/platform/capacityprobe/deployment_observe_linux.go` | 固定 proc/uname backend、线程绑定、held/reopen/nsfs/cleanup |
| `internal/platform/capacityprobe/deployment_observe_other.go` | unsupported 平台；无固定路径IO |
| `internal/platform/capacityprobe/deployment_observe_test.go` | 完整/拒绝/故障/取消/隐私/并发回归，synthetic native facts 明示 |
| `cmd/switchback-deployment-probe/main.go` | 固定 ObserveDeployment、5秒context、单JSON输出与固定exit语义 |
| `cmd/switchback-deployment-probe/main_test.go` | 参数、输出范围/短写、退出和生产绑定 |

上述路径均相对唯一工作树 `/path/to/switchback`。本片只新增七文件；不修改 B2 publisher/Backend gate、service模板、Mapper、签名/raw、PG/Start、worker/observer、Compose 或任何既有数据。没有在生产 gate 接入 ObserveDeployment；接入是后续详细设计事项。

## ✅ 离线验收与后续接线

先验证完整 fixtures 的独立 parser/字段一致性，再对 rejecting scaffold 跑可编译行为 RED；GREEN 必须有完整合法观察、稳定 namespace 拒绝、零失败结果和 CLI 退出断言。覆盖所有所需字段的 missing/duplicate/type/overflow、能力宽度、comm空格/括号、TID与leader不一致、namespace成功/拒绝/漂移、stat/open类型与身份不一致、process重开替换、Boot/clock漂移、每次有限IO前后故障/取消、FD一次关闭、并发隔离及 raw status/canary不外泄。加入非root、NoNewPrivs=0及caller/PID1 namespace不同的完整读回，确认诊断保留事实而不暗中发放符合性判断。EACCES/EPERM 的诊断值不加入“平台允许”结论。

最终先执行新 probe/command 包和现有 capacityprobe/publisher/publication/localproduct 门禁相关回归；无integration tags或真实数据库。定向 race 单列；Linux amd64/arm64 test artifacts 仅编译，不执行。真实 kernel/syscall/credentials、unit/drop-in/LSM、procfs view、时间与 source/binary 对应均留给另行授权的 exact-candidate 窗口，离线 fakes 不替代它们。

推进顺序为：B3-A 无权威诊断源码与离线回归 → 具体目标/命令/资源边界审阅后做 Linux 前置观测 → 根据结果决定保留权限、单独批准扩权或重新设计 launcher 证明 → 独立 host/native verifier 接线 → PG/Start 与真实交付验收。host backing 物理安全边界、完整source/mount/writer覆盖、key目录可达性及 aggregate 仍是完整 verifier 必需条件，不能用本片诊断代替。[^accounted]

当前 `capacity_ready=false`、`execution_enabled=false`、`paid_consumed=false`；OpenSpec38/41，6.5/8.4/9.3 保持开放。请审阅的是 **B3-A 七文件源码与离线实施范围**；本设计接受不等于安装/运行 probe、启动 VM/unit、授予 SYS_ADMIN/SYS_PTRACE 或 provision key/journal。

## 🔗 来源

[^b2]: B2交付与封存 (private evidence retained)，本阶段重新执行只读 verify，921项seal匹配。
[^gates]: [publisher gate](/path/to/switchback/internal/workerruntime/adapter/capacitypublisher/publish.go:43)；[Backend gate](/path/to/switchback/internal/localproduct/runtime_capacity_source.go:29)。
[^metadata]: [结构性manifest读取与digest绑定](/path/to/switchback/internal/workerruntime/adapter/capacitysnapshot/admission_manifest.go:207)。
[^proc]: Linux v6.8 [proc namespace get_link](https://github.com/torvalds/linux/blob/v6.8/fs/proc/namespaces.c#L44-L67)，本轮亦读取[raw source](https://raw.githubusercontent.com/torvalds/linux/v6.8/fs/proc/namespaces.c)。
[^commoncap]: Linux v6.8 [cap_ptrace_access_check](https://github.com/torvalds/linux/blob/v6.8/security/commoncap.c#L127-L148)，本轮亦读取[raw source](https://raw.githubusercontent.com/torvalds/linux/v6.8/security/commoncap.c)。
[^research]: 完整权限链、替代方案与尚未验证的目标事实 (private evidence retained)，含ptrace/userns/dumpable/LSM及官方manual来源。
[^go]: Go 官方 [runtime.LockOSThread](https://pkg.go.dev/runtime#LockOSThread)。
[^accounted]: [M1已批准的保证边界与独立部署义务](/path/to/switchback/docs/superpowers/specs/2026-10-19-m1-accounted-capacity-design.md:23)；[B2对后续独立verifier的约束](/path/to/switchback/docs/superpowers/specs/2026-10-20-m1-native-publisher-b2-design.md:93)。
