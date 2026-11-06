# M1 正式采集器 FD 生命周期修正设计

状态：待用户确认。范围为源码、离线回归和新候选；不进入 guest、不操作现行副本或 service、不读取 credential、不调用模型。当前工作树已有 116 项 tracked 未提交改动，按文件前像保护，不提交或改写 Git index。

## 事实与验收目标

本次真实窗口的维护与四副本验证通过，containerd/dockerd 已启动；Engine 在 `networks` 步骤读取 `/proc/1/fd/8` 得到 ENOENT，最终为 unknown。后续 Native、credential、新 PG、Core 未进入。失败证据 142 项封存 SHA256 为 `723252d7f444e1f73fee0b5f3504536f7f181a2cf989a28c59d6b664a1541845`。

45,194 条真实 observation 的流摘要已复核。FD 8 最后成功样本是 timerfd，但失败时身份不可证明；不得用历史类型来跳过当前未知条目。原函数的缺失 FD 回放为 RED，稳定反事实对照通过。

正式 Python `_IO.processes` 和 Go `qualificationEngineProcesses` 都先枚举号码，再分别读取 target、fdinfo 和 stat。这允许源 FD 在多次读取之间关闭或复用。现有 closure 又要求两套完整表和全进程/socket-FD coverage 相等。本设计保持成功样本的全部严格条件，修正其获取过程。

交付须证明：真实故障在旧函数上稳定复现；修正后的真实调用链在一次 FD 关闭后取得全新完整样本；FD 复用、权限错误、隐藏 listener、foreign daemon/writer、预算/记录失败及持续 churn 不会被当成成功；Python/Go 一致；完整新 kit/bundle 与 Source 成员集合闭合并独立复审。

## 路线比较

推荐 A：固定最多两次完整只读取样，配合 FD 对象身份校验。第一份因明确的 FD 生命周期竞争而未完成时，只保留为诊断；在原 deadline 内从头取得全新的完整样本。成功条件和所有外部副作用的单次约束保持。

B：把正式证明改为仅比较相关 FD/socket 的投影。可减少背景抖动拒绝，但会改变已接受的全表/全 socket-FD 完整性保证，范围更大，本片不采用。

C：用 `pidfd_getfd` 复制其他进程的 FD。它绑定实际 open-file-description，但共享 offset/status flags、可操作底层对象，返回的 FD_CLOEXEC 也不能代表原 FD；会把原来的元数据观察变成实际 FD 复制。本片不采用。依据：[Linux pidfd_getfd(2)](https://man7.org/linux/man-pages/man2/pidfd_getfd.2.html)。

## A 的固定合同

### 1. 有界、纯只读的采集重启

每次 listener/native collection 固定最多两次 attempt，共用原来的 deadline 和全部 byte/frame/process/FD 上限，不 sleep、不增加预算。第二次仍不完整即失败。attempt 是 collector 内部未完成的元数据样本；只有整个 collector 返回失败才结束其所在的真实窗口。一次 service start、Engine API 请求、NetworkCreate、daemon-reload、资格/安装/Start 窗口不因该机制重复。

只允许以下 typed sampling-race 触发第二次：

- 在已枚举的严格数字 FD 路径上，元数据原语返回 ENOENT，并且 fresh PID/start/flags、exe、cgroup 与该进程的采集前像保持；进程身份未知或改变不能转换为 race。
- FD 的 target/stat/fdinfo 显示对象被替换，字段自身编码均合法，但无法组成同一对象的样本。
- 两份完整、分别通过安全策略的表/FD 样本出现 coverage 变化，同时固定受保护 anchor 没有漂移。

权限错误、unsupported syscall、非法编码/类型、外来 daemon、data-root writer、隐藏或外来 listener、protected anchor 漂移、超时、资源/输出/记录/清理失败都直接终止，不归入可重采样类别。只在元数据原语的边界分类错误；不能把 recorder 抛出的 ENOENT 或其他任意异常误认成源 FD 关闭。

已经成立的安全拒绝保持为本次 collection 的终止事实，不能被后续取样冲掉。partial 结果、第一轮 prefix、旧真实回执都不参与第二轮成功判定。

### 2. 新样本与固定 anchor

第一次完整进入 FD 采集前固定 boot、daemon PID/start/exe/hash/cgroup、全部受保护 socket 与父目录的路径/dev/inode/mode/owner/kernel inode。Go 同时固定原 filesystem/runtime-parent 身份。若这些初始事实没有取得完整证明，则不能重采样。

第二次从原始 listener tables、全进程/FD 采集及全部 closing 开始重新证明，要求与第一次固定 anchor 一致。两份完整样本及最终 filesystem/daemon/boot 检查沿用原严格条件。禁止在第二轮换绑新的 daemon 或 socket 后把它当成原 endpoint。

每轮完整枚举所有 FD 名称后才逐项采集，保持目录 handle 存活，避免采集器自己的临时 FD 被迭代器再次枚举。

### 3. FD 元数据属于同一对象

使用只读 O_PATH handle 固定源 FD 指向的文件系统对象；由 handle 取得 stat、target 与 mount/inode 身份。原源 FD 的 fdinfo 保留完整 flags，使用其 `ino`/`mnt_id` 与已固定对象匹配，并在其前后核对源条目的 target/对象身份，防止拼接不同文件系统对象的元数据。读取自身 handle 的元数据不读取其指向文件的内容。

这里的对象键是 mount、device、inode、type、target，不声称取得 open-file-description identity 或冻结了原 FD。对共享 inode 的匿名对象不能凭 inode 相同证明同一生命周期；同类匿名对象的不可区分性不得用来放宽 socket/writer 证明。保持原完整 flags 与安全分类；无法组成本合同样本时只可重采样或失败。

`fdinfo` 的 `flags`、`mnt_id`、`ino` 均需唯一且严格解码；没有字段不降级。`ino` 字段在 Linux 5.14 起提供，本候选须把该能力作为资格前提；本次真实帧已实际包含它，但不把这些帧当作后续内核执行的证据。

源条目消失、合法对象身份不一致只生成 typed race，不生成成功 FD 行。禁止填补 flags、使用旧 timerfd 类型、`continue` 跳过未知条目，或把 source FD_CLOEXEC 从本地 O_PATH handle 推导。原 FD flags 保持来自原 fdinfo。

O_PATH handle 只做元数据观察并及时关闭；不 read/write/seek 原 FD，不复制其 open-file-description，不调用 SET 操作，不冻结/停止目标进程，不增加 capability。close 失败必须阻止成功并保留主错误与收尾错误。依据：[Linux open(2)](https://man7.org/linux/man-pages/man2/open.2.html)、[proc_pid_fdinfo(5)](https://man7.org/linux/man-pages/man5/proc_pid_fdinfo.5.html)。

### 4. 诊断与版本边界

Python 通过原 raw recorder 保留 attempt 编号、原因与完整原始帧；失败 prefix 有诊断意义、无准入权。Go 通过固定容量 2 的 internal attempt 记录保留原因和结果，终止错误也携带它；该记录不新增对外 JSON/PG schema，不能给 partial 样本授权。

保留现有 listeners v1/v2、deployment receipt v1/v2、store inventory v1—v4 的 wire 格式与合法成功事实语义。新 Source root 和新候选绑定新采集实现；旧样本、旧候选及封存文件不修改，不宣称兼容证明可替代当前采集。

## 实施与回归边界

实施分三项独立交付：Python 正式 collector 与回归；Go 正式 collector/portable seams 与回归；新候选装配、完整闭包、规模回放与终审。每项保留精确前像与变更清单，完成 task review，最后整片复审。

必须覆盖：

1. 使用本轮真实 stat/cgroup/fdinfo/表原始字节，在真实 `_IO.processes`、`_collect_listeners` 和 Go collector 的实际原语调用处注入关闭；旧实现 RED、新实现只在第二轮全新完整样本成功后 GREEN。
2. readlink/fdinfo/stat 各边界关闭，FD 号码复用至不同 inode、mount 或 socket，关闭后返回、错误来源为 recorder、原 FD CLOEXEC 与 O_PATH 不同、self-FD 迭代器及所有 handle/close 故障。
3. 初始或第二轮 protected socket/daemon/boot 漂移、hidden TCP/Unix、foreign data-root writer、权限、malformed、持续两轮 race、deadline 到期、byte/frame/process/FD 上限、prefix 混用；全部拒绝且无额外副作用。
4. 保留原 strict socket/table coverage 的成功与拒绝回归；对可重采样的 coverage 变化，只有第二轮独立完整通过才可接受。
5. 以本次完整捕获规模验证记录预算和原 deadline 检查；不能用直接返回通过替代真实调用。Darwin 上只执行纯离线 fixtures；Linux/arm64 至少完成静态编译，未运行 Linux 内核原语须明确标为未实证。
6. 原 Source/templates 与 Git/index 状态的允许差异必须完全对应本片变更；旧 142 项执行证据与 1,390 项离线文件保持 byte/mode 不变。旧 verifier 对当前 Source 失效不等于历史证据被改写；新回放使用保留的旧源码前像。

## 新候选与停止边界

在新的私有 phase 内产出完整新 qualification kit 和 Engine bundle，不覆盖 kit25/bundle2、现行四副本、host/guest stage 或任何历史目录。候选装配只构建源码/静态 ELF，不构建 Docker 镜像，不启动 PG、VM、Engine、Worker 或 provider，不打开/读取 key。

候选必须通过完整 Source 成员、manifest/构建收据、原始回放、预算、权限/门禁及独立复审的绑定校验并封存。新生产行为、worker-delivery 配置、quota/容量/Trust/PG/Start 资源数值与权限保持。

本轮不创建可消费的实际 grant/claim，不沿用已经消费的恢复入口。当前现场存在新四副本、已安装并启动过的 Engine 及未完全证明的网络阶段；新候选交付必须把这个边界写清楚，不能伪造 quiet/all-preserved 状态或声称可直接复用旧入口。后续实际切换仍须候选完成后的具体单次操作授权。
