# 隔离 Linux 体验准备包（S3a）

本包始终是 `preparation_blocked`。纯渲染和本地 fixture/cross-build 不能证明 guest enforcement、host backing、实际镜像、provider、浏览器或 retained 接线。它不创建 key、Trust、签名 `capacity.json`，也没有 ready 分支。环境授权以后仍需独立物理证据，不会自动变成可运行验收。

## 现在可以复现的纯操作

在仓库根目录使用已有 Python3；不安装任何依赖：

```sh
python3 scripts/experience-prepare.py plan --source "$PWD"
python3 scripts/experience-prepare.py render --source "$PWD" --output /private/tmp/switchback-core1011-review-new
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s test/acceptance/experienceprepare -v
env GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOCACHE=/private/tmp/switchback-control-backpressure-1011.39rHe8 go test -p 1 -parallel 1 -count=1 ./internal/platform/capacityprobe ./cmd/switchback-capacity-probe
env GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOCACHE=/private/tmp/switchback-control-backpressure-1011.39rHe8 GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -p 1 -buildvcs=false -o /private/tmp/switchback-capacity-probe-review ./cmd/switchback-capacity-probe
```

render 输出路径必须是 canonical absolute、新建目录，拒绝 symlink ancestor、已有目录及 source overlap。使用 plan 的 `source_sha256` 加 `--expected-source-sha256` 可拒绝输入漂移；manifest 的完整闭包可独立比较。四角色 COPY closure 与额外 deploy/probe/scripts/migrations/tests 分开 raw-byte SHA。pins 必须保持 `80b79a98b6c71f7cf90c8be802885945c36b40b87a61165bb6eb012b1c04bacc`（3438 bytes），它仅记录 2026-10-12 官方 metadata，不是已下载或安装的收据。不要刷新版本、临时放宽 profile 或插入虚构 image ID。

示例 actual image IDs 全为 null。Compose 使用外部必填 `FROZEN_{WEB,BACKEND,ASSISTANT}_IMAGE_ID`；未来必须逐项核对四角色 dirty-build receipts 的真实 image ID，Worker ID也须单独冻结。`pull_policy: never` 不替代校验 ID。不能用 local/latest、示例 SHA 或 `.env approved=true` 跳过门禁。provider key 不进入源码、render bundle 或 build context。

## 新输出素材的权限合同

顶层output固定0700；manifest、collector-manifest、pins、Compose与其他诊断/配置素材默认0600。`manifest.json.render_permissions`另列不含secret的消费素材：

| 新输出路径 | mode | 未来消费者与读取方式 |
| --- | --- | --- |
| `init-postgres.sh` | 0644 | PostgreSQL容器UID999显式读取init脚本，不要求executable位 |
| `migrate-postgres.sh` | 0644 | migrate容器UID999通过`/bin/sh`读取，不要求executable位 |
| `migrations/`及其子目录 | 0755 | UID999在**单独bind挂载**的SQL目录内遍历/读取 |
| `migrations/*.sql`（含子目录SQL） | 0644 | migrate的awk/psql读取；逐文件清单在manifest中 |

这些mode只设置于本轮新渲染目录内的素材，没有chown、切UID、sudo、install或改已有目录。非owner测试以fixture实际owner非999及other r/x位计算预期访问，不执行容器或切UID。

当前Compose把两个脚本各自的exact leaf和migrations子目录分别bind进去；未来Engine root负责在host/guest侧解析这些source，消费者看到的是独立mount后的leaf/subtree，不需要跨越output外层0700。**不能改成把整个私有output目录挂给999并假称可读。**未来将bundle安置到guest core域时仍须校验Engine source路径、owner、mount权限与数据目录UID；本测试不是实际Docker bind或guest权限验收。所有provider/secret及其内容保持在bundle外。

## 未来对象、目录与边界（全部待另行授权）

| 对象 | 固定候选 |
| --- | --- |
| VM / Lima | `switchback-core`；Lima 2.2.1 / macOS VZ / arm64 |
| CPU / RAM / system disk | 4 vCPU / 10 GiB / 24 GiB |
| 额外 quota disk | 新 `sbquota` 64 GiB；不是任何既有盘 |
| guest mount | `/mnt/lima-sbquota`；XFS ftype=1、reflink=0、rmapbt=0 候选 |
| Compose project | `switchback-core1011` |
| 新 host loopback | Web 44175，Backend 48081；启动前重查占用 |
| retained 旧端口 | 44174/44173/48080/43000/43001/49091/49090 全部保留 |
| core aggregate | `switchback-core.slice`，CPUQuota=120%，MemoryMax=3 GiB，swap=0 |
| Worker aggregate / 单 Runtime | sibling `switchback-workers.slice`，2 CPU / 4 GiB / 512 PIDs / 10 GiB layer |

只包含 Web、Backend、Assistant LiteLLM（默认 disabled profile）、PostgreSQL 和一次性 migrate。保留 nginx 依赖的 `switchback-backend` service 名。Worker 由未来 Supervisor 创建，无 steady Worker service。Backend 默认 Worker delivery=0、Assistant=0；capacity source/Trust不配置。无模型 preflight不能调用 Assistant 或付费任务。

Lima 源码配置仅将 Web 44175、Backend 48081 的 TCP 显式转发到同号 host loopback 端口；其后分别忽略 TCP、UDP 的全端口范围。两条 ignore 均使用 `guestIP: 0.0.0.0`、`guestIPMustBeZero: false`，覆盖全部 guest interface。本轮仅修改配置并验证纯渲染，不启动或修改 VM；源码规则不能证明当前 VM 已采用这些规则。

Backend `/var/lib/switchback` 绑定 guest `/mnt/lima-sbquota/product`，EngineDataRoot也为该 guest路径，不能换成宿主路径。只给 Backend 固定 `/var/run/docker.sock`；它不是 read-only API，挂载本身将赋予较大权限，故当前仅渲染且 execution disabled。不存在 Worker socket、host network、privileged、Worker host ports。source固定只读 `/mnt/lima-sbquota/core/source`；blocked-status目录固定只读挂载，未成为 capacity source。Compose渲染目录中的SQL/init/migrate均只读挂载，未来须位于 core 域内；不能直接运行源模板。

| writable 分类 | quota 候选与覆盖 |
| --- | --- |
| `/mnt/lima-sbquota/core` | project100；PG data/WAL、只读源码/部署包、blocked-status归档（控制平面也有写预算） |
| `/mnt/lima-sbquota/product` | project101；WorkingCopy、Agent state、ObjectStore/Trace/cache及当前RuntimeSecret的 `runtimes/<id>-g<generation>/secret-input` |
| `/mnt/lima-sbquota/docker` | project999；Engine images/layers residual、日志、build、provider文件及daemon temp |
| `/tmp`、PG socket、LiteLLM临时目录、shm | 每服务有界tmpfs，计入memory；不是磁盘容量证据 |
| system disk 的 `/run/containerd`、daemon journals、Lima agent/transport/probe | 实际写者与硬上界 UNKNOWN；当前drop-ins不足以把全部写者纳入999，不得据此运行 |

当前 `localstorage.Materializer` 的 BackendDataRoot/EngineDataRoot均由product root派生；`runtimesecret.Materialize` 再在SessionStorage下创建secret-input。因此实际RuntimeSecret属于101，不能声称被此模板搬到999；999中的外部provider候选与Docker自身secret/temp是不同对象。旧candidate将所有secrets归999的假设未实现，保持blocked，不修改生产root或预算。

没有匿名 volume，PG主数据路径与WAL均显式bind；固定 PG UID/GID999仅候选，需要 exact image 检查。所有服务只读 root、有界tmpfs、固定轮转10m×3；各容器限制不等于core aggregate保证，daemon/containerd/shim/probe/transport实际cgroup membership仍待读回。额外disk不自动格式化。Lima 2.2.1配置不使用2.3引入的block-device helper。

## 一次性初始化与稳态

当前无可执行安装/格式化 runner。未来授权前先确认精确 VM/project/disk/device/UUID，拒绝重名、非空、被占用目标；不得自动改名扩容或转用旧盘。host dedicated APFS reserve/quota/backing仍为独立候选，实际UUID、reserve、cache/base/aux/temp/snapshot/COW峰值未证明。

初始化必须先于第一次 Engine启动：确认新空盘→独立审查geometry→format/mount/project defaults/byte+inode quota→逐项identity读回→Engine/core配置。PG只创建 `switchback` DB，不创建Langfuse DB；migrate完成与兼容性证据必须在未来开放执行前取得。source/credentials只读bind的实际uid/gid也要先验证。安装和任何上述箭头都需要另行授权，不包含在本轮源码操作中。

稳态/restart不得format、repair、递归改project、重置epoch或把旧ledger计空。固定project候选0/100/101/999及动态allocator生命周期都未被证明；静态目录互不重叠不等于内部Engine动态层互不重叠。

生产 DockerProvisioner 已接线固定 Worker `CgroupParent=switchback-workers.slice`、2 vCPU、4 GiB memory、512 PIDs、10 GiB writable layer，且 `MemorySwap=Memory`（总内存加 swap 上限等于 memory）。复用 running/stopped 旧容器前比较 cgroup parent 与 swap；不匹配则拒绝，不 Start/Create/Remove 或原地修复。秘密复制容器固定 `switchback-core.slice`、0.1 vCPU、64 MiB Memory/MemorySwap、32 PIDs；保留只读 root、无网络、无 Docker socket、无 provider secret argv/env。

上述仅证明送入 Engine 的参数与复用配置校验。ContainerCreate 后仍直接 ContainerStart；create-before-start 的物理 quota 验证、start 后首次 Prompt 前实际 cgroup membership 门禁，以及全时段容量证明仍缺失。Cleanup/Inspect 维持原职责，Docker inspect 声明值不证明资源执行。本轮环境授权已另行发生，但本任务没有 VM/Engine/core/Worker、provider/model、capacity Trust/签名/ready source 运行操作；原 R3 805 文件及物理证据保留为历史身份，新证据必须绑定当前源码 bytes。

## Linux只读采样与发布责任

未来 deployment owner把固定manifest放到 `/etc/switchback/capacity-probe/collector-manifest.json`（root所有、不可group/other写），独占新私有0700目录 `/mnt/lima-sbquota/core/blocked-status`。CLI仅接受 `collect`，不接受任务指定path、endpoint或命令。**本轮不执行 collect**；未来运行也只输出 untrusted collection，不签名。

Engine identity使用独立的canonical小写UUID合同；container ID仍是64位小写hex。valid Engine保留exact UUID及inspect inventory；Engine格式失败时Engine=null，inventory采样操作仍记录但不保存未绑定有效Engine的container facts。来源为 [Moby docker-v29.1.3 daemon/id.go](https://raw.githubusercontent.com/moby/moby/docker-v29.1.3/daemon/id.go) 的 `uuid.New().String()`；exact candidate29.9.0 tag本次未取得，旧格式/实际Engine仍需未来核验，不能视作已安装身份。

Engine只有固定Unix socket的v1.55 GET info、all=1 inventory与最多32个exact hex ID inspect；每响应最多1MiB、有超时、不跟redirect。仅保留有限identity/resource/固定mount字段，不保存Env、command、任意label、secret-volume内容或错误response。created/exited也进入inventory；unknown ownership/mount/cgroup/resource都保持blocked。API失败错误不回显原body。Engine声明的StorageOpts、labels不能证明enforcement。

Linux采样仅固定 `/proc` identity/mountinfo、`/sys/fs/cgroup`白名单、四个固定目录的statfs、FSGETXATTR和Q_XGETQUOTA。保留采样序号/窗口/errno、boot/mountns/mountID、statfs block+fragment单位、quota原512B单位与112-byte payload。仅支持Linux little-endian arm64/amd64；quota API/权限/ABI/identity不支持即null/unavailable。读取是非原子诊断，不是capacity proof。UUID/geometry、all-time Mmax、Delta_inode、host backing、writer清单和exact Ubuntu SRU仍缺失。有限cgroup PID表不证明每个daemon/shim成员归属。固定proc/cgroup读取及目录打开失败仅保留可errors.As提取的syscall errno，collection中是数字errno和固定operation/outcome；PathError路径、任意cause/body/canary不进入错误字符串或诊断。超长/格式错误是无errno的受控语义失败。

成功发布必须先把旧 `capacity.json` 原bytes无覆盖归档到唯一 `quarantine-capacity-*`，移走原文件并fsync目录，之后才发布 `probe-status.json`。现有Reader此后无法再读原路径；后续现有Reconcile负责数据库durable拒绝。fixture仅证明文件/Reader组合；已有C2a missing-source/旧good-cache拒绝记录另行绑定，不重复PG。本工具不控制Backend、不撤销Trust、不证明即时停止历史执行。

不安全/不可写/被替换目录必须硬失败，不声称blocked发布成功或shutdown已发生；旧bytes保留。**接管可能服务过签名证据的目录之前，必须先有外部明确授权的admission shutdown，确认停止后才交给单一publisher。**文件错误不授权删除别处文件或停止旧服务。默认只用新输出目录、execution disabled、capacity source unconfigured。

## 物理阶段的剩余门禁

实际write+fsync、byte/inode限额失败、restart/dynamic-layer/default-inode/nonoverlap、全时刻metadata残差Mmax/Delta_inode、host backing/峰值、swap/core/Worker sibling、保留观测双向transport、实际镜像/迁移都属于后续S3b。retained ingestion、browser URL、scrape target维持unconfigured，guest loopback不会自动映射到Backend bridge；不得开启无认证Docker TCP。环境批准不等于物理证明，原一次付费只读体验未消费。

## ABI与事实来源

本次读取的上游依据（不是已安装Ubuntu SRU证明）：

- [Linux v6.8 dqblk_xfs.h](https://raw.githubusercontent.com/torvalds/linux/v6.8/include/uapi/linux/dqblk_xfs.h)：XFS quota version1、112-byte结构、512B Basic Blocks、project flag/id。
- [Linux v6.8 quota.h](https://raw.githubusercontent.com/torvalds/linux/v6.8/include/uapi/linux/quota.h)：QCMD与project type；不能把普通quota的1024B单位用于XFS API。
- [Linux v6.8 fs.h](https://raw.githubusercontent.com/torvalds/linux/v6.8/include/uapi/linux/fs.h)：28-byte fsxattr、FSGETXATTR、PROJINHERIT。
- [Linux v6.8 quota.c](https://raw.githubusercontent.com/torvalds/linux/v6.8/fs/quota/quota.c)：quotactl_fd参数与Q_XGETQUOTA只读分派；没有调用SET/ON/OFF/SYNC。

2026-10-13尝试读取Launchpad `Ubuntu-6.8.0-142.142`对应头文件未成功；不以upstream tag替代exact SRU核验。Docker字段合同另对照已锁定 `github.com/moby/moby/api v1.55.0`源码；未据API编译或fixture推断真实Engine版本。固定软件候选的官方metadata链接保留在primary-pins.json。

Lima模板字段还核对了 [v2.2.1 lima_yaml.go](https://raw.githubusercontent.com/lima-vm/lima/v2.2.1/pkg/limatype/lima_yaml.go) 与 [v2.2.1 disk文档](https://raw.githubusercontent.com/lima-vm/lima/v2.2.1/website/content/en/docs/config/disk.md)，format=false没有授权首次格式化。实际系统agent/SSH/transport端点和写路径仍须独立核验。
