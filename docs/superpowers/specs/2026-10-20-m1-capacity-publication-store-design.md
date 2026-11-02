# S4-B 发布持久性边界与 B1 离线详细设计

_2026-10-20 · 设计候选，待用户审阅 · 未实施、未部署_

## 📋 决定与交付边界

S4-A 已完成 typed 观测到未签名 Facts 的转换。本文件建议将 S4-B 拆成两片，先完成 **B1：持久序号与原子发布 Store**，再完成 **B2：concrete native 采样、私钥签名与受管命令**。部署与 host/native 证明仍是独立执行窗口，S4-C 仍负责资源故障收敛。

B1 只提供进程内 Go module：接收已经签名的 raw v1 envelope，再执行验签、Facts/时效校验和文件发布。它不持有或读取私钥、不签名、不采样、不增加 command/HTTP/IPC 入口，也不接 PG、permit、Start 或生产装配。这样可以先证明崩溃时不会复用可能发布过的序号，且发布不确定性不会被报告为成功。

本次建议只批准 B1 的源码与离线回归。生产调用者必须等 B2 固定 native 来源与独立部署验证成立；B1 的 API 能写出有效签名文件，不表示该签名来自真实内核采样。`verifyCapacityDeployment` 保持 unavailable，`capacity_ready=false`、`execution_enabled=false`、`paid_consumed=false`，OpenSpec 仍为38/41。

## 🔎 当前依据与路径比较

| 当前源码事实 | 本次设计的结果 |
| --- | --- |
| raw v1 已固定 Envelope、Ed25519 signature domain 和私有 Verified；现有 Verify 不调用 CheckFacts | Commit 必须分别调用原 Verify 与原 CheckFacts，不能把验签成功当完整容量证明 |
| Reader 每次重开 deployment directory 下的 capacity.json，拒绝 symlink 与读取期间替换 | 未来 Backend 只读挂载整个 public directory，保留原 Reader；不能只 bind 单个文件，否则 rename 后会读旧 inode |
| 原 PG 允许更大 sequence，拒绝回退/同序号异内容；同序号同字节不会刷新接收时间 | publisher 可以烧掉序号，但不能重复使用，不能重签旧 ObservedAt 或靠重复读取续期 |
| 原 PG 的 fresh 同时检查 observed/received，窗口30秒，未来容差2秒 | B1 发布前重验 ObservedAt；停止/删文件不能被描述为立即撤销已消费 permit |
| 现有 created-layer reader 的 HTTP allowlist 没有全量 list；parser 要求 created/PID0；physical sampler也专用其路径 | B2 另建 running pool collector，不放宽原 created 入口、不调用旧 Collection 签名 |
| Backend 已有三项文件 capability，且持 Docker Socket；observer 独立 CAP_SYS_ADMIN | B1 不增加 capability。私有 journal/key 不能靠0700声称隔绝可达它们的 Backend root；正常拓扑不向 Backend/Worker 挂载 private 目录 |

当前源码及哈希绑定记录在本阶段 `source-findings.md`、`source-bindings.json`；S4-A 的152项封存成员、922输入与1728个 Git-visible 文件在新增本文前已重新核验。

| 路径 | 收益 | 代价与取舍 | 选择 |
| --- | --- | --- | --- |
| B1 发布 Store → B2 native+signer → 独立部署 | crash/sequence 与 native 失败可分别验证；首片不碰新权限或真实环境 | 多一个明确模块边界，后续仍要验证端到端采样时效 | 推荐 |
| B 一次实现采样、签名、daemon、发布和部署 | 一次产出完整源码链路 | syscall、密钥、服务权限、磁盘失败和30秒衔接同时变化，难以定位验收缺口 | 暂不采用 |
| 扩大现有 exact-CID IPC observer | 少一个进程 | 扩大同步 IPC、密钥与权限职责，与先前边界冲突 | 不采用 |

## 🧱 模块接口与固定存储

新增 `internal/workerruntime/adapter/capacitypublication/`，避免将纯 mapper 扩成 IO/密钥所有者。接口固定为：

```go
type Store struct { /* private Trust copy, held FDs, mutex, pending sequence */ }
func Open(trust capacitysnapshot.Trust) (*Store, error)
func (s *Store) ReserveSequence(ctx context.Context) (int64, error)
func (s *Store) Commit(ctx context.Context, envelope []byte) error
func (s *Store) Close() error
```

Open 只接受 exact 新保证，复制保留的 Trust 可变数据。生产路径在模块内固定，不接受环境变量、CLI path、输入 filename、替代 syscall/backend、clock 或 callback。B1 没有生产调用点；B2 的命令以后只能用 Open 与固定 concrete sampler，不接受 stdin Facts、Collection、fixture 或签名请求。

| 固定路径/叶子 | 规则 |
| --- | --- |
| `/mnt/lima-sbquota/core/capacity-publication-private` | root:root0700，private，Backend/Worker 正常拓扑不可达；同一已计入 core storage 域 |
| private/`writer.lock` | 部署方预建 root:root0600、regular、nlink1、长度0；Open用独立FD取得非阻塞 exclusive flock，持有到Close末尾；从不unlink/rename它 |
| private/`sequence.bin` | 部署方预建 root:root0400、regular、nlink1，恰好96字节；正常更新以新inode替换，不原地覆写 |
| private/`sequence.next` | 唯一临时叶子；O_EXCL/NOFOLLOW，写入时0600、发布前0400；上限96字节 |
| `/mnt/lima-sbquota/core/capacity-publication-public` | root:root0750，只有publisher正常写入；Backend只读挂载整个目录，Worker不挂载 |
| public/`capacity.json` | root:root0440、regular、nlink1，原 raw v1 envelope，1..capacitysnapshot.MaxBytes |
| public/`capacity.next` | 唯一临时叶子；写入时0600、发布前0440；上限同capacity.json |

部署方必须先建立目录、lock和初始journal，程序不自动mkdir、修复owner/mode、在正式journal缺失时初始化它、生成key或初始化PG。正常Reserve替换已验证journal不属于初始化。首次 high-water=0 只允许用于另行验证的全新部署/pool绑定；文件不存在不构成“全新”的证明。B1不提供初始化、重置、key rotation或rebind命令。

两目录必须是 canonical、无symlink祖先、root-owned且祖先无group/world write，位于 pinned Trust 的 XFS mount/device；不能等于旧 blocked-status、互相包含或落入Backend/Worker的正常可写mount subtree。后一个跨拓扑条件需要后续独立部署验证，B1只能校验自身能观察到的路径/FD元数据，不能假装从Trust推导了全部mount inventory。持有目录FD，并在每个产生可见副作用的操作前后重走canonical路径比较inode/device/mount identity。所有FD使用CLOEXEC；打开叶子使用NOFOLLOW，先安全限定文件类型，避免FIFO/device打开阻塞；UID/GID与mode/nlink都校验。Close错误不对同一数值FD盲重试。

flock只是配合进程间的 advisory lock，与open-file-description关联；不能抵御绕过协议的root writer。[^3] private目录不挂载给Backend是正常写入边界，不承诺防御受信任VM root或持Docker控制权的Backend被攻破。真实writer覆盖仍由部署验证器负责。

### Journal 的精确格式

`sequence.bin` 为96字节little-endian记录，无JSON歧义：

| offset | 长度 | 值 |
| --- | ---: | --- |
| 0 | 8 | ASCII `SBCAPSEQ` |
| 8 | 4 | version=1 |
| 12 | 4 | reserved=0 |
| 16 | 32 | binding digest原始SHA256 bytes |
| 48 | 8 | high-water，范围0..MaxInt64 |
| 56 | 8 | reserved全零 |
| 64 | 32 | SHA256(bytes0..63)，完整性校验，不是认证或签名 |

binding=`SHA256("switchback/capacity/publication-store/v1\x00" || json.Marshal(copiedTrust))`，使用现有Go Trust struct的固定字段序列；不接受任意JSON对象重排或输入提供的binding。Trust变更、boot/mount identity更新、轮换、磁盘回滚/备份恢复都不能静默rebind或从0继续，需另行审核且保留既有高水位；B1不实现该运维动作。此journal只保证同一绑定、未被回滚的受信任本地volume在正常crash/restart下单调，不能证明对恶意篡改/整盘回滚的单调性。原PG仍独立拒绝回退或同号异内容。

### 打开和重启

Open依次验证Trust与两个目录，安全打开既有lock并取得`LOCK_EX|LOCK_NB`，核对lock canonical entry仍为held inode，再读取journal。校验file type/owner/mode/nlink/大小、完整记录与binding；缺失、损坏、未知版本、越界或不匹配均拒绝，绝不推算PG sequence。

journal有效后，只允许上述固定叶子。上次crash留下的两个next文件可以在持锁后作为stale temporary删除：先校验它们是对应目录的root-owned、nlink1、regular、无symlink、安全mode且不超该叶子上限；不得将其提升为正式文件，不扫描/删除任意文件。删除后fsync对应目录。陌生叶子、错类型/权限/超界临时文件拒绝并交operator核查。

若有旧capacity.json，先以相同Trust执行Verify、CheckFacts，采用与Commit相同的结构规则（Containers<=32、Removals为空），并要求sequence<=journal high-water；此时不应用freshness或reservedAt规则，旧样本允许已过期，因为这里只识别可安全撤下的本绑定产物。invalid/wrong-binding/sequence超前不得覆盖或删除。可识别的旧产物在Open返回前撤下并fsync public目录；重启后必须重新采样，不能直接重发旧文件或修改ObservedAt。任一步失败都不取得可工作的Store，释放本次FD/lock，必要时返回“文件可见性未确认”。

## 🔄 序号、提交和关闭协议

所有方法由Store私有mutex串行化；同一Store最多一个pending sequence。nil context、nil/未配置/closed Store均拒绝；Close可重复调用并保留第一次关闭结果。文件IO同步执行，检查ctx但不启动脱离调用的goroutine；fsync等阻塞syscall不被描述为可由context保证硬中断。

```mermaid
flowchart LR
    accTitle: Capacity Publication Persistence Order
    accDescr: 先持久化新的高水位，再允许用这个序号生成并提交新样本；任何不确定发布都会结束Store，不能复用该序号。
    held_lock["固定目录与独占lock"] --> durable_sequence["journal高水位持久化"]
    durable_sequence --> native_future["B2新采样与签名"]
    native_future --> checked_envelope["验签与Facts时效校验"]
    checked_envelope --> synced_temporary["temporary文件fsync"]
    synced_temporary --> visible_file["rename capacity.json"]
    visible_file --> synced_directory["public目录fsync后成功"]
```

### ReserveSequence

检查context、Store健康且没有pending、high-water<MaxInt64，再取n=high-water+1。已有pending的并发Reserve返回Busy，零IO，不释放或作废原调用的pending。写入完整sequence.next（O_EXCL、全写循环、短写0失败），fchmod0400、fsync(file)、close成功、renameat替换sequence.bin、fsync(private directory)，最后核对canonical directory/lock/journal identity。只有全部成功才返回n并置pending=n。目录重验比较inode/device/mount及权限等稳定属性，不把自身写入造成的目录mtime/ctime变化误判为替换。

返回后n即被烧掉，记录私有reservedAt（journal持久化后捕获的server time），采样失败、签名失败、取消、进程退出或Commit拒绝都不能复用它。文件持久化失败不签名、不发布；Store进入failed，不能在同一对象里继续Reserve。ctx在journal更新完成后取消时也不返回可用序号，更新可能已经持久化，后续重开读取实际高水位。sequence gap合法，MaxInt64耗尽拒绝而不wrap。每次状态变更前将正式journal的完整bytes及inode与本对象上次确认值比较，不能只相信内存高水位；检测到漂移则停止。

### Commit

1. 锁内检查context和pending；clone有界envelope，拒绝空/oversized。调用原Verify和原CheckFacts；要求raw v1、sequence==pending、exact Trust binding、Containers<=32且Removals为空。ObservedAt必须>=reservedAt，当前server now下年龄<=30秒、未来<=2秒；不改ObservedAt或payload，不接受v2或partial Facts。该时钟关系不自行证明采样来源，B2仍须实际先Reserve再读取。
2. 在固定public目录用O_EXCL写capacity.next，检查全写、fchmod0440、fsync(file)、close；在rename前再次检查context、freshness、目录/lock及当前capacity entry的归属。
3. 同目录renameat替换capacity.json；记录已跨越“可能被Reader看到”的边界。fsync(public directory)，重新验证canonical目录/lock和新capacity inode；再次检查context和freshness。全部成功才清pending并返回nil。
4. 任一错误将Store置failed，不允许修正payload后重试同号、自动换目录、续签旧样本或继续下一次Reserve。rename开始后发生错误/取消，一律返回`publication_uncertain`并标记可见性未确认，即使检查时文件已存在，也不能报告“未发布”或“回滚完成”。n一直烧掉。

同目录rename提供可见替换的原子性；持久化还需要文件和目录fsync，二者不是同一个保证。[^1][^2] 本协议的“可见”专指原Reader的固定capacity.json入口，不承诺受信任Backend无法读取同一只读目录中的temporary文件。IO fault、设备行为和power-loss条件仍需要目标XFS与真实候选验收；离线call-order/fault测试不证明磁盘持久性。

### Close与撤下

Close先fence新方法，在持锁状态下仅撤下自己识别且inode/内容归属未漂移的capacity.json，fsync public目录，然后关闭其他FD，最后释放lock。临时叶子只按上述有限规则清理，不删除lock/journal。cleanup失败仍释放FD/锁并保留固定错误；文件可能可见，不伪造cleanup已成功。下一次Open仍按journal和实际目录重验。Close不能和另一个未结束的同步操作并发释放其FD。

Open未成功、lock忙或检测到未知writer/目录/entry替换时，不删除对方产物。store已failed时Close仍可尝试撤下自己已识别的entry；无法确认归属则保留并报告未知。断电/kill不能保证执行Close，靠正常journal协议和原PG时效边界处理，不能宣称systemd的stop timeout消除了未完成syscall。

### 崩溃与失败矩阵

| 中断位置 | 重启读取与处理 | 禁止的推论 |
| --- | --- | --- |
| journal temporary写入/同步前后，尚未完成journal目录fsync | 未发布任何新envelope；读实际合法正式journal，有限清理next | 不从temporary恢复序号，不把失败Reserve当成功 |
| journal持久化后，采样/签名前 | high-water已升；n跳过，新尝试从更大序号开始 | 不复用n补发旧观察 |
| capacity temporary阶段 | journal保留n；next只删除，不提升 | 不将完整temporary当已发布 |
| capacity rename后、目录fsync前/失败 | 文件可能已经被Backend读取；返回不确定，停止该Store；重启撤下可识别产物并取更大序号 | 不能报告零副作用或已回滚 |
| 目录fsync后、函数返回前退出/取消 | journal仍保留n；文件可能已持久化/被消费；不确定结果不重试 | 响应丢失不产生复用n的权利 |
| Close unlink或directory fsync失败 | 保留未知可见性；原PG可继续持有先前样本至时效/重验 | 不宣称删除同步撤销已消费permit |
| journal丢失/损坏/绑定变化/检测到snapshot sequence>high-water | 拒绝打开，无自动初始化/rebind/PG查询 | capacity文件有效也不能修复journal |

错误文本仅固定六类：`publication_invalid`、`publication_busy`、`publication_state_unavailable`、`publication_io_failed`、`publication_uncertain`、`publication_closed`，分别提供可用`errors.Is`识别的`ErrPublicationInvalid/Busy/StateUnavailable/IOFailed/Uncertain/Closed`六个sentinel。可用私有wrapper与有界stage enum标记失败阶段，其Error只返回固定reason，不输出path、原始body或密钥。context取消/超时继续支持`errors.Is`：可见副作用前可直接返回标准context错误；跨rename的不确定性由private wrapper同时unwrap固定Uncertain sentinel和context错误。任何错误都不被翻译成Task状态、容量退款或重建授权。

## 🧪 B1 实施与离线验收范围

拟新增 `capacitypublication/{store.go,journal.go,files_unix.go,files_other.go,store_test.go,journal_test.go,files_test.go}`，不修改任何既有产品文件。真实生产入口仅Linux amd64/arm64；其他平台Open在读固定目录前返回publication_invalid（有界stage=unsupported_platform）。unit tests可用包私有的有限syscall seam/测试目录构造Store后测试同一Reserve/Commit/Close接口；不能增加生产UID/path/backend/clock override、fixture command或公开factory。测试中的Ed25519 key只在内存生成并明确为SYNTHETIC，不生成/读取真实部署key、Trust、manifest或seed；字节fixture和测试目录中的合成journal不属于部署初始化。

| 测试组 | 必须证明的行为 |
| --- | --- |
| 有效RED | 两个正例实际编译运行失败：持久Reserve后新对象不复用；Commit输出原字节可由原Reader/Verify/CheckFacts接受。不是build-fail |
| journal | 独立literal96-byte fixture；magic/version/reserved/hash/binding/length/MaxInt64边界；0→1，烧号/skip，丢失不重建；输入和Trust别名隔离 |
| 单writer | 独立open-file-description竞争，第二Store立即busy且零目录修改；lock替换、双Close、closed/nil/并发调用与FD释放顺序 |
| 签名与Facts | exact raw v1、错误key/domain、v2、同号异内容、wrong sequence/identity/manifest、CheckFacts失败、stale/future等号与越界、envelope输入不变 |
| 故障顺序 | 对create/write/short-write/chmod/file-fsync/close/rename/dir-fsync/recheck每个边界注入失败；journal必须先于capacity可见；失败后无同号重试；rename后不确定分类 |
| restart/cleanup | 正常、各crash cut的state重开；temporary只丢弃、无自动promotion；unknown/symlink/hardlink/mode/owner/超限entry不删除；snapshot超journal拒绝 |
| 取消 | before/after journal、temporary、rename、directory fsync、返回前；保留errors.Is，不能假报无副作用或让新方法越过Close |
| 真实临时目录 | 私有test seam下的普通用户临时目录验证write/rename/reader互操作和竞争；不访问固定部署目录，不把它当Linux/XFS/root-owner或断电证明 |
| 兼容 | 原mapper、raw v1/v2、Reader、PG/Start、IPC、blocked collector和production-unavailable源码不改；对相关既有离线包定向回归 |

审批后再由writing-plans给出精确任务/命令/源码归属。所有Go验证走新的offline runner；最终适当Linux amd64/arm64 compile-only，不执行生成二进制。无需新PG窗口、native quota调用、VM/Engine/Worker/provider、系统目录写入或服务启动。真实掉电、Linux flock/XFS、root ownership与部署隔离留在实际候选验收，不能由录制backend通过关闭。

## 📍 B2 与部署必须带走的义务

B2另行详细设计固定Unix Engine GET allowlist（list all+逐CID前后inspect）、有界严格JSON、同FD XFS读回、实际进程start-time/cgroup路径与节点、guest self-cgroup与全writer/mount覆盖；完整采样成功后才调A，使用Reserve返回的正序号。created/exited/paused或任一读失败使整个新样本失败，不丢CID、不制造Removal、不放宽30秒时效。同步quota调用无法硬中断的问题不能用游离goroutine掩盖。

私钥未来仅由B2的固定安全reader读取，独立于IPC HMAC；签名domain沿用raw v1。B2的production command没有stdin/任意Facts/fixture签名入口。新独立受管publisher若需要CAP_SYS_ADMIN，必须在具体权限设计中单独评审；不能加到Backend或直接复用/扩大observer。Linux v6.8 project Q_XGETQUOTA的权限依据是quota.c的实际check分支；这不构成目标loaded kernel/LSM或只读capability证明。[^4] native进程及observer都须计入现有core aggregate hard upper，不能增数值或借闲置usage。cgroup namespace会影响路径视图，native service必须与pinned身份一致，不能静默加会改变namespace的hardening选项。[^5]

Backend未来只读public目录，签名key与private journal不挂载给它或Worker；必须验证它们与正常可写data-root不重叠。磁盘空间不足可能使journal、snapshot、Backend或PG不可写，此为已批准保证接受的可用性风险，B1不会创造控制面保留量、容量退款或副作用重试。S4-C仍按原owner处理这些失败。

原PG在实际读取缺失/无效文件后可以block；未触发重验或publisher突然失联时，先前事实仍受原observed/received≤30秒约束。30秒是旧证据的时效上界，不是调度器一定完成撤销的SLA，更不撤销已消费操作。真实Create→created quota观察→same-CID Start期间旧样本的可用时窗必须在后续候选验证，不能靠重盖时间戳闭合。

## 🔗 来源与审阅点

本文件只要求审阅 **B1 固定路径Store、96-byte高水位journal、先序号后可见、失败停止与不确定发布语义、无key/native/command/生产接线的离线范围**。没有新增真实运行或文件权限授权。旧设计中的CAP_SYS_ADMIN、host/native部署验证器和S4-C仍各自独立。

[^1]: Linux man-pages, [fsync(2)](https://man7.org/linux/man-pages/man2/fsync.2.html)，file和directory持久化分开。
[^2]: Linux man-pages, [rename(2)](https://man7.org/linux/man-pages/man2/rename.2.html)，同目录替换的可见性与持久性不同。
[^3]: Linux man-pages, [flock(2)](https://man7.org/linux/man-pages/man2/flock.2.html)，advisory lock与open-file-description生命周期。
[^4]: Linux v6.8, [fs/quota/quota.c:24-47](https://github.com/torvalds/linux/blob/v6.8/fs/quota/quota.c#L24-L47)，Q_XGETQUOTA project分支和CAP_SYS_ADMIN。
[^5]: Linux v6.8, [Control Group v2](https://www.kernel.org/doc/html/v6.8/admin-guide/cgroup-v2.html)，资源限制与namespace视图；本设计不声称已验证目标host。
