# CreatedLayer 同步 IPC 观察器

2026-10-16。用户已选择 VM 内受管同步 IPC 路线，批准继续该路线的设计和实现。它替代本地 M1 生产路径对 CreatedLayer 签名文件的即时读取；完整容量 snapshot、部署 Trust 与 PostgreSQL consumed-authority 维持原职责。旧 unsigned 采集源码切片及全部证据保持冻结副本，本阶段另建 `.scratch/runtime-observer-ipc-1016/`。

## 目标与成功标准

Backend 在现有 Create→exact Inspect→checker→authorizer→exact Inspect→单次 Start 的 checker 位置同步取得同 CID 的有限真实事实。观察器只复用已批准的 `capacityprobe.CollectCreatedLayer` 与 Linux native adapter，不 Create/Start/Remove、不改 quota、不接模型/DB、不持有完整容量签名私钥。不因 IPC 成功把 collection 的 trust/status/capacity/execution 字段改为 ready。source、macOS 记录后端测试、Linux compile-only、真正部署和物理验收分别报告。

## 模块与 Interface

新增 `internal/platform/createdlayeripc`，仅依赖 Go stdlib、已有 x/sys/unix 与 capacityprobe，隐藏帧、认证、文件/peer 检查、deadline 和串行采样。调用方只学习 Binding、NewClient/Observe 与 NewServer/Serve。采用 `codebase-design` 的深模块与内部测试 seam；不提供通用 RPC、插件、任意地址/路径/命令或资源参数。

```go
type Binding struct {
    ManifestDigest string `json:"manifest_digest"`
    ContainerID string `json:"container_id"`
    RuntimeID string `json:"runtime_id"`
    SessionID string `json:"session_id"`
    RunID string `json:"run_id"`
    Generation uint64 `json:"generation"`
    ProfileID string `json:"profile_id"`
    ProfileRevision uint64 `json:"profile_revision"`
    ProfileDigest string `json:"profile_digest"`
}
func NewClient(key []byte, manifestDigest string) (*Client, error)
func (c *Client) Observe(context.Context, Binding) (capacityprobe.CreatedLayerCollection, error)
func NewServer(key []byte, manifestDigest string) (*Server, error)
func (s *Server) Serve(context.Context) error
func ReadClientKey() ([]byte, error)
func ReadServerKey() ([]byte, error)
```

Binding 的 digest 格式为 `sha256:` 加64小写 hex；CID64小写hex；其余 ID 为有界无空白/control 的 server ID，generation/revision 为1..MaxInt64。Server 与 Client 各自固定并复制部署 digest/key，拒绝不一致 Binding。请求不接受路径、project、Engine 地址、资源数字、权限或可信 bool。

## 固定通信与认证

唯一生产 socket `/run/switchback-created-layer/observer.sock`，仅 Linux amd64/arm64。外部入口无 TCP、HTTP proxy、环境 endpoint 或 fallback。父目录由部署方 root:root0700 创建；程序不创建任意父目录、不删除旧 socket、不自动修复 ownership。socket root:root0600；逐组件 nofollow 验证路径、目录权限、socket type/nlink/owner 和前后 inode/device。Server 只在不存在的固定叶子 bind；结束后只清理它实际拥有且未漂移的 socket。Worker 不挂载 socket、认证 key 或宿主 `/run`。

SO_PEERCRED 检查 UID/GID0；host-native Server 要求 peer PID>0。容器内 Client 允许不在本 PID namespace 中的 server PID 映射为0，不能以 PID0 或 UID0 独自认证。部署身份同时由私有只读 mount、固定 endpoint、下面的独立 IPC key、digest 与 challenge/request binding 确定。其他 root 容器必须没有这些挂载；VM root/部署操作者仍是受信任 authority，不能宣称抵抗其篡改或证明所有外部 writer 已排除。

认证使用专用32字节 HMAC-SHA256 key，不使用完整容量 Ed25519 key/Trust，不生成任何 capacity envelope。Server 固定读取 `/etc/switchback/created-layer-observer/key`；Backend 固定读取 `/run/switchback-observer-auth/key`，均要求 root-owned0400、单链接 regular file、逐祖先 nofollow/无 group-other write，有界读取恰好32字节且拒绝全零。key 由部署方另行产生/安装，本阶段不产生或读取真实 key；Server 在宿主原 mount namespace 以 O_RDONLY|O_NOFOLLOW 安全读取，Backend 使用只读 bind mount；0400不代表能抵抗受信任 VM root 改写，不额外添加会创建 mount namespace 的 BindReadOnlyPaths/ReadOnlyPaths。不进入 env、日志、Trace 或 stdout。两方复制并固定 key，轮换需要替换两进程，不能悄悄重载或试多把 key。

每连接一次 RPC，Server 先发送固定8字节 magic `SBCLIPC1` 与 CSPRNG32字节新 challenge。Client 再生成 CSPRNG32字节 nonce，以 canonical JSON 请求传递 version、nonce、deadline、Binding。请求 MAC 为 `HMAC(key, request-domain || challenge || raw-request)`；响应 MAC 为 `HMAC(key, response-domain || challenge || SHA256(raw-request) || raw-response)`，两域固定不同、以NUL结尾。响应携带 version、nonce、request digest、exact Binding 与 collection，全部认证。一个 challenge 仅接受一帧，处理后关闭连接；跨连接或重启 replay 不能通过新 challenge，不建立 nonce cache 或第二个持久权威。

帧为4字节 big-endian body length、32字节 MAC、body，请求≤8KiB、响应≤128KiB；分配前检查 size。只处理一帧，尾随帧永不作为第二个请求。两端 strict decoder 禁未知字段，并要求解码后标准 `json.Marshal` 与 raw body 完全一致；这使 duplicate/case alias/缺失 mandatory wire或Binding字段/非规范数字/短或过长固定数组/尾随 JSON 全部拒绝；nested collection 原有 omitempty partial字段不改，缺 engine/physical仍作为partial由checker拒绝。此协议由本仓库同版本 Go 实现互通，不接受自由格式外部 JSON。

Server 先验 peer，再读认证帧，MAC 通过才解析/验证 Binding/deadline 并调用实际 collector。只有一条采样执行流：无并行 sampler、无持久队列、无 detached syscall goroutine。连接读取/握手有限 deadline，request 时限≤30秒，Client 取 caller deadline 与30秒的较小值；读写错误、超时、取消、partial、身份漂移均 fail closed。Client 验 MAC、nonce/request digest/Binding、请求采样窗口后才返回 collection；partial 保留给 checker 拒绝。30秒不能硬抢占同步内核调用；进程失去响应时 Backend 按超时拒绝，service 停止也不能伪造已完成 cleanup。

## 事实验证与 Backend 接线

新增 capacitysnapshot 的 `CreatedLayerVerifier`：从完整合法 Trust 复制并固定 Identity/Core/Storage/Coverage 与 production local_standard_v1 profile。`VerifyCreatedLayerFacts(ctx, facts, request, identity)` 复用现有 reader 的同一份 facts policy；旧 File reader 必须仍先验证原域签名，再调用相同 policy，不出现 unsigned 文件入口。可新增 `ValidateRequest` 供 IPC checker 在任何通信前校验 exact request/profile；拒绝零值 verifier，不将 ready assessment 当权限。

新增 `internal/workerruntime/adapter/capacityobserver.Checker`，只在这个受信任部署 adapter 把认证 collection 映射为旧 facts。必须拒绝不完整/缺字段/非原 unsigned状态、SamplesTruncated、任何 unavailable physical sample、漂移或错误 raw/recheck；检查 Engine.ID/DockerRoot/driver、真实 BootUUID、mount namespace、FSUUID、device/mount 与 pinned Trust。UUID 直接按原16字节 canonical format，mount namespace 按既有 readlink 契约编码为 `mnt:[<十进制 inode>]`；不可用 Trust 配置覆盖实际观察字段。root/parent/diff before/after 身份及 geometry/FSXAttr 必须一致，三目录 inode 非零且 parent/diff 不同；目标 project 来自 raw attrs并与采集值一致，保留 QSTATV/target/default0 raw。事实 freshness/quota enforcement/10GiB soft=hard/positive target inode hard/static-project 排除复用现有 policy，零/off 事实保留后拒绝，default0 不能补全未知 inode authority。

Checker 构造时只固定 Trust/validator；每次合法 Check 在 key/IPC 前先要求 Trust.Core 存在 exact aggregate path `/switchback.slice/switchback-core.slice`（非相似前缀、子域或其他per-container entry）。首次合法且覆盖完整的 Check 才从固定路径加载一次 key 并创建 Client，避免仅加载完整容量配置就读取真实 key 或连接 IPC。使用 sync.Once 固定首次结果，无 key fallback/重试轮换。内部 per-instance transport seam 只用于包内测试；没有全局 mutable hook 或 Task/context 隐藏 authority。

localproduct 的 `initialCapacitySource.createdLayer` 改成明确 `*capacityobserver.Checker`，production common constructor 只装配它；仍按原顺序构造 running snapshot reader/CapacityStore/InitializeTrust。没有 Trust 则 unavailable。旧 File CreatedLayerReader 不作为生产切换或失败 fallback，只保留独立验证与既有 fixture 用途。constructor 的 lazy key 行为维持现有 capacity-only 集成测试可运行；任何真实 preStart Check 仍必须通信成功和事实合规。两份原 fake 忽略 preStart 的覆盖缺口另列，本阶段不把它们的绿当 IPC 或真实 Start 证据。

## 受管入口与部署

新增 `cmd/switchback-created-layer-observer`，参数只能为 `serve <sha256:manifestDigest>`；拒绝平台/参数后才读固定 Server key，信号驱动 Serve 退出。无 key/manifest 自动生成、shell、Engine lifecycle 或 diagnostic ready。新 systemd template 运行于专用 VM 的 root mount namespace，User/Group=root，CapabilityBoundingSet=CAP_SYS_ADMIN，NoNewPrivileges=yes，RestrictAddressFamilies=AF_UNIX，RestrictNamespaces=yes，SystemCallFilter 禁 mount/reboot/swap，Restart=no，Slice=switchback-core.slice，UMask=0077、CPUQuota20%、CPUQuotaPeriodSec100ms、MemoryMax128M、MemorySwapMax0、TasksMax64、KillMode=control-group、TimeoutStopSec10s。它不使用会改变 mount namespace 的 ProtectSystem/PrivateTmp/PrivateDevices，以免默默改变 pinned mount identity；源配置不能冒充目标 systemd/LSM/seccomp 的真实验证。

Server 在 listen 前必须从实际 unified `/proc/self/cgroup` 确认 exact `/switchback.slice/switchback-core.slice/switchback-created-layer-observer.service`，安全有界读取真实 cgroup v2 的 `cpu.max=20000 100000`、`memory.max=134217728`、`memory.swap.max=0`、`pids.max=64`；错误、缺失、unlimited或错误路径均零listen/collector。不改quota或自动修复cgroup。固定 CoreCgroupPath 与 ObserverCgroupPath 常量在 IPC 模块定义，供 checker/asset验证复用，不能由请求覆盖。

unit 和独立 compose overlay 只作为未部署模板，默认不把 service 启用或 execution 开关置1。overlay 仅给 Backend 挂载固定 socket directory 和专用 key（read_only、create_host_path=false），保持 cap_drop ALL；不修改旧 sealed core.compose.yaml，也不向 Worker 增加挂载。通过固定 slice、实际 self-cgroup/上限读回以及 checker 的 exact aggregate Core entry 三者把 observer 纳入既有 core aggregate hard upper；不增加或改写原core.slice的CPU120%/Memory3G/Tasks1024预算。128M不是免费额度，现有 full-capacity UNKNOWN 因此仍拒绝。部署前要实际验证 native namespace/capability/LSM/peer/key owner/mounts、服务硬限制和独占 writer；无此证据不启动 Worker。

本阶段只执行源码与本地测试/交叉编译，不安装 unit、不写 VM、不启动新 Engine/服务、不生成真实 key/Trust、不改 DB schema、不运行 Task/ACP/模型。1013/1014 consumed 阶段和1672绑定的旧 source-only seal 保留；只对声明的新源码增量建新封存。完整真实验证需要独立新阶段且不能绕过已有 full-capacity 约束。RuntimeDirectory默认随stop删除；同key的observer重启也可能更新目录inode，Backend旧目录bind mount会失效。runbook要求在受控静默窗口重建/重挂Backend IPC bind mount，不能只重启observer就假定可达；key原子替换同样需重挂其file bind。不得自动unlink旧socket或绕过owner/reconciliation。

## 验证

公共 Client→实际协议 handler 的记录连接覆盖认证成功、challenge/nonce/manifest/CID/owner/profile错配、反射/跨连接 replay、坏 MAC/缺key/错误peer、noncanonical/duplicate/未知字段/超长或截断帧、过去/超长 deadline、取消/慢连接、partial与secret canary清洗；不接实际 Engine或网络。记录测试须调用生产 collector 控制流，不能另写一份采样算法。真实 native peer/key/socket检查用窄私有 syscall/filesystem seam测试，Linux actual代码双架构编译；Mac不可冒充实测。

事实适配测试覆盖真实来源字段与 pinned Trust 的全部漂移、before/after raw变化、realtime两位、缺 sample/错误ABI/ENOENT、quota off/zero/溢出/10GiB不符/default fallback和上下文。至少一条公共 coordinator→真实 Checker 的认证/事实拒绝测试证明零 Start、原 identity 保留及不越过后续 authorizer；成功 finite检查仍需原 authorizer。各任务RED→GREEN、最终 affected suite/gofmt/vet、Linux amd64/arm64 compile-only；独立复审 Spec+Quality，Critical/Important修完再前进。

上游依据：[systemd.exec source](https://github.com/systemd/systemd/blob/v255/man/systemd.exec.xml)、[Linux unix(7)](https://man7.org/linux/man-pages/man7/unix.7.html)；quota ABI/权限的精确 selected142 源码证据在旧 `runtime-producer-1016/linux-abi-primary-sources.md`。上游资料说明设计依据，不能替代本 VM 的实际 capability/LSM/namespace 验证。
