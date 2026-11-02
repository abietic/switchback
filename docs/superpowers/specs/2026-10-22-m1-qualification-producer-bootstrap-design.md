# M1 真实资格采集、安装与首启设计

_2026-10-22 · 待用户审阅。本文只定义源码与离线交付范围，未授权或执行实际环境操作。_

## 🎯 目标与完成标准

完成一片可组装的真实资格采集与安装工程，解除首启循环依赖。交付包括真实 host/native producer、固定配置安装器、三个进程的有界启动等待、实际 ABI 执行者证据，以及一份构建完成后才生成的具体操作清单。不能以 DTO、合成成功收据或文档完成替代这个交付。

沿用已批准的 [共享卷保证](/path/to/switchback/docs/superpowers/specs/2026-10-22-m1-existing-volume-capacity-design.md)：复用现有 APFS 卷和虚拟磁盘；不改主盘分区、不要求外置盘；真实 quota、容器硬限额、80% 记账、原 PG reserve-before-permit 与 exact-CID 单次 Start 保留。共享 APFS 空间或 metadata 耗尽仍可能影响宿主可用性。

本片源码交付的完成标准是：实际命令与 OS/Engine adapter 齐全、候选可离线组装、失败路径经过回归、完整审阅通过、构建身份可核查。真实资格通过、签名容量发布、PG 消费及产品体验分别需要实际运行证据，不能在本片关闭。

这里的离线候选是完整工具、模板、构建材料和操作执行器；不会伪造尚不存在的实际 CID、boot、quota 结果或资格收据。最终实例身份与两份真实收据只能在后来获授权的真实操作中生成并冻结。

## 🔎 已确认的两个阻塞

1. **首启循环。** native 收据要求 Backend、publisher、observer 等角色的真实 loaded binary、status、cgroup 和挂载事实。Backend/publisher 配好 Trust 后缺资格文件立即退出；observer unit 又在 ELF 执行前要求包含最终 manifest digest 的 EnvironmentFile。最终 digest 必须等真实收据产生，因此现有时序无法得到保持同一进程身份的完整收据。Backend 的 `localproduct.New` 还会先连接 PG，等待必须放在它之前。
2. **执行者身份不准确。** 当前 setter 证据把每个 ABI 的 Worker 身份与单一 loaded Worker 全字段相等，包括 PID、start ticks 和 executable。实际 compat32 helper 是另一个 ELF/进程；64 位进程调用一个 32 位 ioctl 编号不能替代实际 compat 执行。当前 GETFLAGS 的规范化 4-byte 值也不能冒充完整 native syscall buffer。

另一个已修复的兼容错误独立交付：按冻结角色的声明 UID 校验 PostgreSQL UID999、Assistant UID10001；Backend/publisher/observer 及 guest host-process 仍为 UID0。此修复不包含本文新启动行为。

## 🧭 路线选择

| 路线 | 优点 | 代价与结论 |
| --- | --- | --- |
| **同一 ELF/PID 内有界等待，manifest 最后提交** | 直接观察最终程序；PID/CID/cgroup 不更换；共用现有严格验证 | 改变三个入口的启动行为，推荐 |
| 独立 launcher 完成观测后 exec | 业务入口改动较少 | 增加 loader 协议；exec 前的 executable 不是最终角色，仍要处理执行后观测与恢复，暂不采用 |
| ptrace/exec-stop 冻结最终 ELF | 可在业务逻辑前观察 executable | 新增调试控制、线程与信号边界，操作和故障恢复更复杂，暂不采用 |

只采用第一条。资格安装不是 ready 标志；每个进程仍自行执行完整资格与当前角色检查。不存在临时 Trust、省略资格、替换 CID、额外提权或按 `verified=true` 放行。

## ⏳ 有界启动等待

新增共享的固定文件提交等待模块，使用 monotonic clock，最长 **300 秒**、固定 **100ms** 轮询；不是 Task 参数或可调环境变量。首次读到 manifest 后只验证一次；取消、超时、已存在的无效文件、权限/身份漂移及其他错误立即退出。只允许安全父目录下的最终 manifest 叶子 `ENOENT` 表示等待；父目录缺失、软链接、越权和其他叶子缺失均不能被重试掩盖。

预先创建安全配置目录并挂载目录本身；安装期间不替换目录 inode。等待与验证沿用当前固定叶子、无软链接、owner/mode/nlink/大小限制和读取前后身份核查。安装器先写其余叶子并 fsync，再原子提交最终 manifest 并 fsync 目录。manifest 一旦存在即作为提交边界，不能补写缺失字段或就地修复；半安装失败不提交 manifest。

| 入口 | 等待位置及通过后行为 |
| --- | --- |
| Backend | 在 `cmd/switchback-backend` 调用 `localproduct.New` 之前。配置缺失/非法立即拒绝；配置完整但 manifest 尚未提交才等待。通过完整资格和当前 Backend 角色检查后进入原组装，原 factory 再检查一次，真实首样本前容量仍为 stale |
| publisher | 在读私钥、打开 B1 Store、分配序号或采集之前。通过完整资格、publisher 角色与绑定检查后进入原发布链，保留循环及签名前 Recheck |
| observer | 新增固定 `serve-installed` 入口，等待同一 manifest，再安全读取实际安装的固定 observer env、资格与当前 observer 角色，之后才读 IPC key/创建监听。新 unit 不依赖 systemd 启动前展开最终 digest；旧 `serve <digest>` CLI 保留兼容，新候选固定使用新入口 |

等待期间没有 PG 连接、业务 HTTP/gRPC/IPC listener、Engine 调用、provider、私钥读取、Store/journal 操作、序号分配或容量采样。只读取固定非秘密配置与本进程/资格所需身份；使用有界脱敏状态输出。启动等待不增加 capability，也不允许开启 execution/Start。

资格采集窗口固定最多 **240 秒**，包含所需前后事实；超时不提交。超时后的进程退出，不自动续跑。新的部署模板使用 `Restart=no` 或等价单次语义，不由 Compose 健康重试把失败窗口变成隐式新窗口。资格通过后的常规生命周期仍遵循已有规则。

## 🧩 组件与接口边界

| 组件 | 负责内容 | 输入与输出 |
| --- | --- | --- |
| host producer | macOS APFS 卷、Lima/VZ 受管路径及实际 writer 事实 | 固定部署候选与单次窗口；输出真实 raw host receipt，不安装配置 |
| native producer | guest loaded identity、namespace/roles、core limits、byte quota、Worker setters、Backend handoff、mounts/writers 七项事实 | 同一候选、窗口和冻结程序；输出真实 raw native receipt，不签容量、不申请 Start |
| 固定安装器 | 校验同一候选全部收据，生成最终 manifest/Trust/bindings/observer env，安全安装并提交 | 只接收部署 owner 的封闭候选目录；不接 Task/API 任意路径，不创造事实、不替换已固定 Trust |
| 单次操作执行器 | 顺序协调前置核查、角色启动、两端采集、清理核查、安装与结果保留 | 使用完成实现后的 exact 构建/路径/对象清单；实际运行需单独具体授权 |

host/native 共用 qualification ID、candidate fingerprint、冻结输入与构建收据、真实开始/结束窗口。需要先缓冲观测再填共同结束时间的部分，只能封装本窗口的实际事实，不能拼接旧窗口。producer 失败、raw 超限、事实缺失或 cleanup 不确定均不安装。没有真实 OS adapter 的测试替身不得传入生产命令。

采集实现复用已有纯 codec、procstatus、namespace 采集与严格验证；host/native producer 不复制一套 `passed` 判断。安装前运行同一个生产 validator，进程启动后再次验证。生产验证不依赖测试 fixture loader 或导出的任意注入函数。

## 🧪 真实采集的权限、身份与有限 I/O

### Host 与角色事实

host producer 读取前后实际 `diskutil info`、`diskutil apfs list`、stat 和已识别的受管 writer。卷 UUID、container UUID、physical-store 关联、quota/reserve、挂载路径和文件身份必须一致；当前候选 quota/reserve 均为 `120259084288` bytes。读取实际分配/单位，不把 reserve 对应的用量当卷已满，不由目录名推断卷身份。没有分区、迁移、扩容或填盘动作。

每个 core CID/image/CoreID、角色 UID/caps、binary digest、PID/start、boot、cgroup、mountinfo 由实际观测绑定。Docker 配置/inspect 与 proc/mount 观测互相核对；不只信 image tag 或 source label。guest host-process 与 macOS writer 分别保留身份语义，不把两者 PID/UID 混用。

### Native 执限与文件交接

- 专用 native producer 仅用已接受的 root + `CAP_SYS_ADMIN|CAP_SYS_PTRACE`，不附加 CHOWN/DAC_OVERRIDE/FOWNER。文件交接和私有测试目录清理由**同一冻结 Backend image/ELF 的有限资格子命令**执行，使用其现有三项文件能力；缺能力负例只丢弃相应能力，不能恢复或新增能力。
- Worker 资格子命令放在实际 `cmd/worker-shim` ELF 的受限分支，先于 `shimruntime.Run`；不启动 ACP/Codex/LiteLLM、不读取 provider 凭证、不连接 Backend。使用最终冻结 Worker image、UID10002、cap_drop ALL、NNP、seccomp、既定 cgroup/资源与网络 profile。资格命令只处理安装时固定的 case/对象集合，无 shell、任意命令或任意路径通道。
- 测试区域是现有 product data 下的新私有资格目录，只允许当前 nonce 的新对象；不为 Backend 增加可以看见 publisher 私有 key/journal 的大范围挂载。project ID 先证明未使用；已有 quota/对象、冲突或无法确定都拒绝，不能重置它。候选生成后固定 exact project ID，执行时不得另找一个替代。
- 在同一已绑定父/子对象上先做小 hard limit 下的 EDQUOT，再提高该测试项目 hard limit 做同量写入成功。每次 requested write 不超过现有 16 MiB 合同，预先核对可用空间；不做耗尽全卷实验。这些测试字节不进入容量 sample 或 PG 预算。
- 记录实际 target FD 对应 inode/device/FSUUID、原始 fsxattr/quota/QSTATV、写入/fsync errno 与前后对象身份。host 与 container 的 mount ID 可以不同；用实际 bind/mountinfo、device/inode/FSUUID 证明同一对象，不要求跨 namespace 的 mount ID 数值相同。
- 结束只清理已经证明属于本窗口的对象/CID，保留原始事实与清理结果；无法确认清理时不提交。既有服务、旧窗口/unit、旧证据与未知归属对象不能被停止或删除。

### 实际 ABI 执行者证据

新增独立私有格式 **`switchback.capacity.native_qualification_receipt.v2`**。七项 check ID 不扩张；复用其余严格事实契约，在 setter payload 中明确区分冻结 Worker parent 与实际 ioctl executor。新生产候选必须使用 v2；v1 原始字节和离线历史解析保留，不能作为新候选的兼容回退。Capacity raw v1/v2、签名 domain、PG/Start/IPC 格式与资源数值不改。

每个 setter case 绑定实际 executor PID/TID、start ticks、boot、raw status/cgroup、executable digest、ELF class/machine/ABI、所属 exact Worker CID/image/profile、parent 身份，以及目标 FD 的实际对象。native 与 compat helper 均是冻结 Worker image 的独立构建材料，分别记录输入与 artifact digest，不能拿父进程 executable digest 代替 helper。

compat32 通过需要实际 ELF32 执行并发出 ioctl。只有被冻结且结构正确的 compat ELF 实际 `execve` 返回 ENOEXEC 才可记为 `unsupported`；helper 缺失、错误 ELF、一般启动失败均拒绝，unsupported 也不能计为 setter 已执行。父进程记录实际 exec errno；已执行 case 记录对应 helper 的前后身份。

保存 syscall 的真实 request number、buffer 长度及原始 bytes，规范化 GETFLAGS 整数是单独解码结果。native long 与 compat long 的尺寸分别验证，不用 4-byte 整数摘要冒充 native 8-byte buffer。原 setter 的 EPERM、project/inherit 不变、普通 GET/读写可用语义保留；不扩大允许的 ioctl。

## 🔗 无环绑定与安装顺序

候选 fingerprint 延续现有无环算法，排除最终 manifest digest。实际操作先准备安全目录与公钥身份、创建尚未 Start 的 exact core containers，再冻结 source/build/inventory、exact core CIDs、Worker/helper artifacts、规范化部署输入和 Trust 前置字段，随后启动同一组等待进程并采集。helper 构建材料纳入 source/inventory 的显式辅助 artifact 集合，不能放宽原有必需 build/role 的 exact coverage。最终 host/native raw digest 进入 manifest；manifest digest 最后进入 Trust、publisher bindings 和实际 observer env。不得反向补进 receipt 的 candidate fingerprint。

准备公钥身份若需要生成私钥，只能在后来明确授权的新私有目录中进行；本轮不生成私钥或初始化 journal。共享文件大小与 raw aggregate 上限继续由现行安全 reader 限制，采集器必须在编码前按总量核对，超限拒绝，不截断成成功事实。

固定安装目录和只读 bind 在启动前准备。安装顺序为：

1. 在新候选目录验证所有来源、receipt、build、角色与前后身份，拒绝重复、未知、跳过与不确定事实。
2. 核实本窗口测试对象清理与已观测进程/CID仍一致；失败停止。
3. 以固定安全叶子写入 source、inventory、host/native receipts、Trust、publisher bindings 和 observer env，完成文件持久化。文件不可由 Worker 或非授权 core 写入。
4. 最后原子安装 admission manifest，持久化目录；任何 commit 后错误按结果不确定处理，不重新提交或续跑。
5. 各等待进程自行完整验证及检查当前角色后继续原启动链。Backend 仍 stale，publisher 后续才能读 key/Store 并发布，observer 后续才能读 IPC key/监听。最终启用准入仍需要原 PG 和 fresh sample。

已存在的 manifest/Trust/journal/PG pool 不覆盖、不重置。需要升级或重新验收时，按已有冻结部署 owner 流程先停止准入并核实在途权限，另建匹配的具体候选；不能由这个安装器自动热换 Trust/epoch。

真实窗口将涉及启动角色、PG、Engine 测试容器、有限文件写入及可能的 key/journal 初始化，必须在完整构建后逐项列入操作清单并取得具体执行授权。当前设计接受只允许写源码、离线测试和构建，不允许执行这些动作。

## ✅ 验收与交付顺序

实现计划将这些作为同一交付片的依赖任务连续完成，不把每个 DTO 再单独交给用户审批：

1. 实际 executor v2 codec/严格验证与 bounded wait；旧字节/原门禁兼容拒绝回归。
2. 三个真实入口及新候选模板；新增 Backend/Worker 有限资格分支和真实 host/native producer。
3. 固定安装器与单次执行器；共同窗口、文件提交顺序、cleanup/unknown-effect 行为。
4. 全链离线故障回归、Linux arm64/amd64 编译、host 工具构建和完整候选装配；独立审阅、保留 dirty worktree/旧封存证据，产出 exact 执行清单。

| 必须证明的行为 | 离线验证 |
| --- | --- |
| 等待没有业务副作用 | 用计数依赖证明等待/超时/取消/非法提交路径零 PG、listener、key、Store、sequence、collect/provider 调用；Backend 检查必须先于 `New` |
| 同一进程通过后恢复原链 | 固定时钟和安全临时目录下提交完整候选；校验失败不调用业务启动；成功只继续一次，身份变化拒绝 |
| 安全安装 | 缺父目录/软链接/硬链接/权限错误、leaf 替换、短写/fsync/rename/commit 后失败、并发安装均拒绝或明确 unknown，不补写成功 |
| 真实 ABI 语义 | parent/executor/ELF/FD 混换拒绝；两种 long size；ENOEXEC 与一般失败区分；旧 v1 不回退；cap/NNP/seccomp 漂移拒绝 |
| 采集链完整 | OS/Engine adapter 真实绑定，fake 只在私有测试 seam；七项 exact check 与同窗口绑定；缺事实/超限/超时/cleanup unknown 不提交 |
| 原容量授权 | 新保证/Trust 保持 exact；首样本前 stale；原 PG reserve-before-permit、exact CID、单次 Start 与旧证据隔离回归 |

Linux 编译与合成夹具通过只证明源码和失败合同；不证明 kernel ioctl、真实 quota 或 deployment 已验收。第一份实际资格通过后，继续真实 signed sample → 原 PG → Create/quota/Start，再完成同 Task 的报告、页面接受与观测关联；第一条完整体验闭环通过后进入定向精简。

## 📌 本次审阅点

接受本文即允许一次完成上述源码、离线实现、候选装配与审阅。需要确认的行为变化是：三个最终程序的固定有界等待、observer 新安装后入口、Backend/Worker 有限资格子命令，以及独立 native qualification v2 对真实 ABI 执行者的绑定。无需重复批准已接受的共享卷保证或 UID 兼容修复。

不扩展当前实际执行授权，不修改旧 protected publisher unit、旧 raw packet、seal 或已消费窗口。后续真实操作以完成构建的具体候选清单为准。
