# M1 常驻 Engine 部署补齐设计

状态：待用户接受；只有设计与取证，尚未实施配置、启动工具或新运行候选。已有 kit-0005、旧 Engine epoch、mask、数据及其授权/回执保持原样。

## 问题与目标

kit-0005 的源码/离线终审已通过，但真实准备在第一个固定 Engine `/info` 连接处因 ENOENT 停止。新只读核对证明默认 docker.service、docker.socket、containerd.service 均 masked/inactive，switchback-engine.service/socket 未安装；私有与默认 Unix socket 均不存在。它没有查询所有历史命名 unit，因此不能声称其他旧 unit 也不存在。

随后对switchback unit catalog的独立只读核对进一步确认：现存1013/1014的docker/containerd units均masked/inactive，未发现可用常驻Engine部署；package metadata与仓库pins一致。实际旧daemon配置的bridge=none、iptables/ip6tables/ip-forward/ip-masq=false，因此单纯解mask也不能完成当前构建/网络准备。catalog本身不证明所有进程全局为空，实际部署前仍需完整writer/process检查。

旧 `.scratch/experience-launch-1013/engine-bootstrap-candidate-r4.json` 是45秒 Engine-only 诊断，禁止 pull/build/create，旧窗口已消费。它不是可重复启动的生产 Engine 部署。此前清单要求现有 Engine 却禁止启动 Engine，遗漏了这一前置。

本片一次补齐服务配置、固定准备/启动执行器、失败验证、完整候选重建与独立终审。采用已接受 ADR0059/0062/0066 的后端编排 sibling Worker、固定资源与完整出站网络，不创建新 RuntimeSupervisor 服务或新产品协议。当前阶段只做源码/离线；真实安装、网络变更、启动及资格实跑在具体候选完成后另行授权。

## 路线选择

| 路线 | 效果与代价 |
| --- | --- |
| **A：新独立常驻 Engine units（推荐）** | 新固定名称与私有配置；默认服务继续 masked，旧诊断内容不变。新增少量部署文件，但运行对象与旧 epoch 分开。 |
| B：解 mask 默认 Docker/containerd 并改全局配置 | 文件较少，但改变既有服务边界和 socket activation 状态，容易混入旧配置/自动恢复；需要更广的环境修改。 |

选择A。不得通过重放旧45秒 epoch、延长旧 RuntimeMaxSec、清除旧 marker 或删数据来实现常驻。

## 固定部署边界

- 现有 Running 的 switchback-core，既有4vCPU/10GiB及XFS `/mnt/lima-sbquota`。不启动/重建VM，不分区、不换盘、不迁移或填盘；保持现有 APFS quota/reserve120259084288。
- 新 units：`switchback-m1-containerd.service`、`switchback-m1-engine.service`。新配置位于 root0700 `/etc/switchback/m1-engine`，固定 regular0600叶子 `containerd.toml`、`daemon.json`。只创建新目标，已有/未知对象拒绝，不覆盖。
- 使用已安装 `/usr/bin/containerd`、`/usr/bin/dockerd`；启动前核对实际版本、包身份、文件owner/mode/hash及架构，与当前 `deploy/experience/primary-pins.json` 中 containerd.io2.4.1、Docker29.9.0等对应pin一致。缺少或不符即停止，不安装/升级/替换二进制。
- Docker data-root 保持 `/mnt/lima-sbquota/docker`、overlay2、`containerd-snapshotter=false`。新containerd沿用该域内受管持久root。没有创建新Engine数据根或恢复旧容量预算的隐式迁移。
- dockerd使用唯一 `-H unix:///run/switchback-engine/docker.sock --group=root`，JSON不再声明hosts；显式external-containerd地址与新pidfile/exec-root，不使用fd://或派生另一个containerd。新daemon默认cgroup-parent为switchback-core.slice；Worker现有显式parent仍覆盖它。配置校验可用不启动daemon的 `dockerd --validate`，参见[官方dockerd文档](https://docs.docker.com/reference/cli/dockerd/)。
- 唯一Engine入口保持 `unix:///run/switchback-engine/docker.sock`，root-private目录0700、socket root:root0660；不创建TCP listener或socket-activation unit。Backend仍仅挂载容器内 `/var/run/docker.sock`；Worker无socket。
- 已有 `/run/switchback-engine` tmpfs的身份/大小128MiB/inodes4096/mode0700须核对；新exec/state/temp只使用其全新 `m1-resident` 子树。不得清空、重挂、替换该父目录或使用旧诊断state子树。
- 两daemon均在现有 `switchback-core.slice`，只按一个aggregate120%CPU/3GiB/1024PIDs/swap0记账。新unit不更改这些值、不另加“免费”子额度；普通Worker原cgroup/resources/capability不变。

## 生命周期与网络

两unit为常驻基础设施：`Restart=no`，不enable-at-boot，无45秒RuntimeMaxSec或旧watchdog。各自Type=notify，Start超时30秒、Stop超时10秒、KillMode=control-group。固定执行器先Start containerd一次、证明其身份/ready，再Start Engine一次；Engine用Requisite/After而非自动拉起依赖，避免失败后隐式重试containerd。Docker明确连接这一个external containerd，不自动派生另一个daemon。

Requisite不会拉起尚未启动的依赖，需要同时指定After，见[systemd官方说明](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html#Requisite=)。containerd使用version4配置，root固定为`/mnt/lima-sbquota/docker/containerd`，state/temp置于新m1-resident子树，imports为空。grpc、ttrpc、shim socket目录分别显式固定在新私有子树，grpc-tcp/debug/metrics的address显式为空；不得遗漏独立ttrpc配置而落到默认`/run/containerd`。关闭CRI/NRI/动态opt/image-verifier插件，启动前拒绝外部插件恢复输入；version4 server字段依据[containerd官方配置说明](https://github.com/containerd/containerd/blob/main/docs/man/containerd-config.toml.5.md)，实际安装版本兼容性在后续具体候选中校验。

本片不主动停止未知/旧进程；未知结果记录并停止执行器。systemd对新unit自身启动超时的终止属于其固定管理语义，须如实留存，不冒充零效果。成功的两daemon在执行器结束后继续运行，后续停止是单独的owner动作。

旧诊断的bridge/iptables/ip-forward关闭设置不适用于真实构建。新私有配置使用Docker标准bridge/NAT出站：docker0、iptables/ip6tables/ip-forward/ip-masq开启；配置不注入任何provider/仓库凭证。它会产生guest内的网络接口/转发/防火墙效果，必须列入未来真实授权。不会修改macOS网络配置或发布Engine/Worker host端口。

执行器在身份及listener校验通过后，创建/核对两个现有协议要求的网络：`switchback-core1011_core`（internal=true）、`switchback-m1-worker-control`（internal=false）。使用Engine的bridge/IPAM，任务/CLI不能选择任意网络/子网。已有同名且配置/owner不匹配时拒绝，不删除重建。未知NetworkCreate结果不重试。网络连通性仍需后续实际运行证明；不宣称所有VPN/LAN路径已验证。

## 固定一次执行器

源码路径采用新 `deploy/experience/engine/`，包含两unit、两配置、有限Python准备执行器及说明。执行器只有固定离线自检和实际入口，无任意path、command、timeout、skip、allow、resume参数；测试注入只在私有函数中。

1. **只读preflight**：实际VM/boot、XFS与hard quota、固定core limits、二进制、默认mask、所有dockerd/containerd/shim进程及相关unit/Unix监听、既有data-root恢复输入、目标安全目录逐项核对。活动/陌生writer、旧socket、未知归属、unsafe path、已有目标或不能排除自动恢复副作用时拒绝。
2. **阻断隐式业务恢复**：持久store中已有container记录、活跃runtime任务或启用的插件/外部恢复入口时拒绝。不得为满足条件手工删除metadata、容器、镜像、volume、BuildKit或containerd历史。受信任但非空的缓存/元数据只允许按明确只读分类规则接受；启动后的正常metadata写入/GC也属于实际效果，不能承诺Engine管理存储逐字节不变。业务数据/volume或恢复来源不明时拒绝；不声称完整metadata增长物理上界。
3. **独占stage**：在新受管目录绑定候选manifest、实际boot/FS/二进制/数据根/配置identity，独占claim；只写新root-owned配置/unit并fsync、readback、核对。daemon-reload后再次核对effective配置。任何失败保留已创建对象，不续跑或伪造回滚成功。
4. **单次Start及验证**：各unit一次Start；以actual PID/start、cgroup、exe、socket inode/FD owner、完整Unix/TCP/TCP6 listener表和文件权限确认私有监听。验证前不连接任何socket；避免socket activation。随后才在同deadline内对已证明daemon进行有限只读API核对EngineID/root/driver/resources/库存。
5. **网络及结果**：按上述固定网络策略准备，保存每步命令/退出码/真实身份、已知写入和未知效果。总准备窗口120秒，不重置deadline，不自动重试/续跑/独立readback。记录的ready只代表Engine部署步骤完成，不生成capacity-ready/Trust/permit，不启动Task或provider。

所有错误路径保存已有部分结果。失败/unknown停止后续动作；只根据精确归属另行制定恢复。即使Start失败，配置、unit、网络或某个daemon可能已存在，必须按真实效果报告。

## 与资格候选的衔接

Engine部署与原资格240秒窗口分开：前者是必要环境准备，不是资格证据。原300秒barrier、240秒共同窗口、PG reserve-before-permit、exact-CID quota/单次Start不变。

完成本片源码后，用原固定builder生成新的完整qualification kit，纳入新部署source/templates并重新绑定source/context/ELF身份；不修改已封存kit-0005及本次已停止准备的授权或结果。新增Engine候选manifest与新qualification kit的关系无环、可核验。实际imageID/CID/key/boot/host-native receipt等仍由后续真实操作填充，离线不预填。

具体绑定为：先有repository SourceRoot与qualification kit，再有只引用其固定source/config/unit的Engine bundle，最后实际执行生成私有 `switchback.engine.deployment_receipt.v1`。receipt记录bundle/kit digest、boot/FS、两个daemon的PID/start/exe/cgroup、安装文件hash、socket身份、EngineID、网络身份及步骤/效果，不签名、不授予capacity或业务权限。

资格 `prepare-identity` 的runtime-preflight及实际driver的verify-runtime-preflight，要只读核对该receipt与当前daemon/socket/network、安装配置/unit的实际bytes一致；安装文件必须精确匹配kit现有source records。只增加部署前置连接与回归，不修改现有qualification raw-v1/v2、七check ID、签名域、Trust、PG或Start格式。receipt不进入其上游kit的输入，避免哈希环；它也不能替代后续原资格验证。

后续具体授权必须整体列出：新Engine/containerd常驻、guest网络/NAT变化、两个网络，以及原镜像/key/PG/unit/bind准备和一次未开始过的资格/安装/同进程首启。先核对实际Engine前置，再开始镜像与秘密准备；不能再次使用“只检查、不启动Engine”的不完整清单。

## 离线交付与成功标准

- unit/config、固定执行器及当前资格衔接作为一片源码完成，不再拆成多个诊断片。
- 合成回归覆盖默认mask不改、socket activation不触发、旧/未知对象不覆盖、自动恢复输入拒绝、部分stage/Start/NetworkCreate未知、deadline、真实退出码与部分结果保留；无实际daemon、socket连接、PG或provider。
- 新配置静态校验、命令/资源/网络/模板一致性检查、受影响纯回归、完整kit/ELF构建及来源核对。编译或合成结果不冒充systemd/native验收。
- 独立Spec/Quality审阅，所有required finding修复后复审；旧dirty tree、mask/回执/数据及HEAD/index保护核查通过。
- 当前阶段不进入guest、不安装unit、不启动Engine、不改网络、不生成部署密钥、不构建Docker镜像。生成完整、冻结且可审阅的真实操作候选后，再请求一次具体执行授权。

## 取证入口

- 本次授权与停止结果：`.scratch/runtime-m1-qualification-execution-1022/{authorization,outcome}.json`。
- 固定socket原始ENOENT：`guest-inventory-receipt.json`；首次探针丢失的中间事实不作为证据。
- 独立只读unit/proc核对：`engine-deployment-readonly{,-receipt}.json`，socket连接数0。
- 实际unit catalog、旧network flags及package versions：`engine-catalog-readonly{,-receipt}.json`；所有动作只读、socket连接数0。
- 宿主卷quota/reserve/usage：`host-inventory.json`、`apfs-facts.json`。quota用满是观察值；共享APFS container尚有空间，不由此推断本次ENOENT原因，不要求新磁盘或改变分区。
