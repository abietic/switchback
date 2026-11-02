# Created-layer observer：未部署源码模板

本目录新增 unit 与 Compose overlay 只表示部署意图。当前阶段仅有源码、离线测试、Compose 客户端配置解析和 Linux 双架构编译证据；没有启动服务、安装权限、连接 Engine/DB、生成真实 key/Trust、运行 Worker/ACP/模型或消耗付费验收。不得据此开启 execution。旧 1013/1014 consumed 阶段与 1672 绑定的 source-only seal 保留，OpenSpec 的物理闭环仍待独立新阶段验收。

## 组装与固定边界

生产 `newInitialCapacitySource` 依次读取部署 Trust、创建原 running-snapshot Reader、创建 `capacityobserver.New(trust)`、创建 CapacityStore、InitializeTrust。Checker 的 key/client IO 延后到首个合法且具备 exact aggregate Core coverage 的请求；缺 Trust/零 source/错误请求保持拒绝。没有环境选择器或 FileReader fallback。独立 legacy FileReader 保留，不参与生产 created-layer 路径。

IPC endpoint 只能是 `/run/switchback-created-layer/observer.sock`。native observer 只支持 Linux amd64/arm64，命令只能是 `serve <sha256:manifestDigest>`；缺失、非单一参数或错误 digest 在读 key 前拒绝。EnvironmentFile `/etc/switchback/created-layer-observer/deployment.env` 由 operator 外部管理，其中 `SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST` 必须绑定当前冻结 deployment manifest 的 exact digest，并与 Backend Trust 一致。此文件不存 key；unit 没有 shell、ExecStartPre、自动生成、自动启用或自动重启。

Observer 与 Docker Engine 的生命周期独立管理，unit 不依赖或启动 `docker.service`。固定私有 Engine socket 缺失时，observer 仍只返回已认证的 partial/unavailable facts，Backend 因证据不足拒绝 capacity admission；不会激活 stock Docker unit 或 consumed Engine units，也不会给它们授予 observer authority。

实际部署须由外部流程安装同一专用 32-byte 非全零 key：Server 固定只读打开 host namespace 的 `/etc/switchback/created-layer-observer/key`，Backend 唯一使用 read-only file bind 到 `/run/switchback-observer-auth/key`。两端均要求 root owner、0400、regular file、nlink=1，并校验安全 ancestors。不得把 key 放进 env、日志、命令参数或报告。Filesystem ancestor/socket 检查、root peer credentials 与 HMAC 互补；它们均不能替代另两者。任何失败保持 unavailable，不能以放宽权限、绕过 peer 或复制 unsigned JSON 恢复。

overlay 只修改 `switchback-backend` 的三个 bind mounts，全部 `create_host_path=false`：

| source | Backend target | read_only |
| --- | --- | --- |
| `/run/switchback-engine/docker.sock` | `/var/run/docker.sock` | false |
| `/run/switchback-created-layer` | `/run/switchback-created-layer` | true |
| `/etc/switchback/created-layer-observer/key` | `/run/switchback-observer-auth/key` | true |

Engine socket 保留 Backend 既有 lifecycle 权限，同时与 observer 固定私有 Engine source 相同；read-only IPC/key binds 不授予 Engine 代理权限。`core.compose.yaml` 保持原封；Compose 按 target 合并替换旧 Engine source。Backend 仍为 `0:0`、`cap_drop: [ALL]`、no-new-privileges；无新增 capability、privileged、host network 或 public port。Worker 没有 key/socket mounts；M1/M2 execution 开关保持 `0`。overlay 不安装或启动 native unit，也不创建任何 host path。

## namespace、资源和物理前置条件

observer 必须在专用 VM 的 root mount namespace，与 pinned mount identity 一致。unit 不使用 ProtectSystem、PrivateTmp、PrivateDevices、RootDirectory、BindPaths 等会创建/改变 mount namespace 的设置；`RestrictNamespaces=yes` 禁止新 namespace，并不提供一个替代 namespace。capability bounding 仅 CAP_SYS_ADMIN，NoNewPrivileges、AF_UNIX、`SystemCallFilter=~@mount @reboot @swap` 限制所需行为。不能增加 blanket `@privileged` deny：systemd v255 的 quotactl/quotactl_fd 属于该组，见 [官方 seccomp-util.c](https://raw.githubusercontent.com/systemd/systemd/v255/src/shared/seccomp-util.c)。这是 v255 源码依据，目标 systemd/libseccomp/LSM 是否允许实际 quota reads 仍未验证。

unit 使用 `Slice=switchback-core.slice`，预期真实 unified self-cgroup 必须 exact 为 `/switchback.slice/switchback-core.slice/switchback-created-layer-observer.service`。Server 在 listen/collector 之前从真实 cgroup v2 安全有界读回：

| unit 设置 | 真实 cgroup 文件值 |
| --- | --- |
| CPUQuota=20%, CPUQuotaPeriodSec=100ms | `cpu.max=20000 100000` |
| MemoryMax=128M | `memory.max=134217728` |
| MemorySwapMax=0 | `memory.swap.max=0` |
| TasksMax=64 | `pids.max=64` |

错误路径、缺项、unlimited、值不符必须零 listen/collector；程序不设置 quota 或自动修复 cgroup。`KillMode=control-group`、TimeoutStopSec=10s、Restart=no。Checker 还要求 Trust 包含 exact `/switchback.slice/switchback-core.slice` aggregate Core entry。原 core slice 的 CPU120%/Memory3G/Tasks1024 hard upper 不变，observer 的128M并非免费额度；full-capacity evidence 目前仍 UNKNOWN，因此继续拒绝 Worker。成功 IPC 仅认证有限事实，原 collection 仍 unsigned/preparation_blocked、trust none、capacity/execution false，不发布容量 ledger/token、DB 或执行授权。

独立 fresh phase 的真实门禁须核验 native 进程架构、root mount namespace、Engine identity/私有 socket、独占 writer、key/ancestor/socket/peer/mount owner、exact cgroup ancestry/limits 和 aggregate usage、CAP_SYS_ADMIN 与 NoNewPrivileges、LSM/seccomp/systemd 兼容、quotactl raw observations 和目标物理映射。当前 macOS 没有 systemd-analyze，静态解析不能证明 unit 可启动；交叉编译也不证明 native runtime 行为。未具备真实 gate evidence 前，本模板不是可执行部署验收，不启动 Worker。

## 受控重启、轮换与清理

Backend/observer 都缓存 key，轮换必须覆盖两进程。使用受控静默窗口，先停止新派发、核对 in-flight effects 与 owner/reconciliation，再按授权停止相关进程。Server 只清理自己拥有且 identity 仍匹配的 socket；不得自动 unlink 陌生/残留 socket 或绕过 owner 检查，残留交由明确 reconciliation。

RuntimeDirectory 默认随 stop 删除；即使 key 不变，observer stop/start 也可能改变 socket directory inode，Backend 旧 directory bind mount 会继续指向旧 inode。key 原子替换也会使旧 file bind 留在旧 inode。因此 restart/rotation 必须受控重建 Backend container、重新挂载 IPC directory 与 key file，并重启 observer 使两端加载同一 key；仅重启 observer 或 Backend 进程不足以保证重挂。新 runtime directory 存在且身份正确后才能重新建 Backend bind。验证新 inode/mount/peer/key binding、错误 key 拒绝和取消传播后再评估恢复；禁止自动 fallback/重连到不同路径/恢复执行。不要打印 key、生成真实 key 或把此流程当作当前阶段已执行。

## 可复制的离线检查

```sh
python3 deploy/experience/created_layer_observer_assets_test.py -v
GOCACHE=/private/tmp/createdlayeripc-go-cache GOPROXY=off GOTOOLCHAIN=local go test ./internal/localproduct -run '^(TestInitialCreatedLayer|TestRuntimeCapacityConfig|TestLoadRuntimeCapacity)' -count=1
```

真实 Compose merge 检查必须使用新建0700 private 临时目录内的 nonexistent Unix socket、显式空 `--env-file`、`config --no-interpolate`；使用 subprocess argv，检查 socket 不存在，保留原 child exit/raw output，禁止 ambient Engine endpoint 和 `.env`。本任务 raw receipts 位于 `.scratch/runtime-observer-ipc-1016/task-3-evidence/`。集成 fixtures 只编译，不接 DSN 或执行 PG 测试；原 fake preStart fidelity 与 heartbeat conflict 是另案后续，不为恢复旧 green 添加 bypass。
