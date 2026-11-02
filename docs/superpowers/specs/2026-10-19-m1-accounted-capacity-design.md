# M1 可审计容量记账与真实硬限额设计

2026-10-19。**状态：方向已批准，正式设计待审阅；未实施。** 用户选择了“收敛 M1 容量保证，先完成设计”，接受专用 guest 数据池未来 metadata 增长未被全量上界覆盖、该池 ENOSPC 可能影响 Backend/PG 可用性。宿主 backing 安全、真实 byte quota、80% 记账、原 PG 原子预留与单次 Start 校验保留。本文不授权环境启动或 Git mutation。[^approval]

## 🎯 目标与成功标准

先完成 initial Task → 正式报告 → 页面接受 → 刷新保持 Task/Run/Turn/Artifact identity → 同 Task 观测。M1 的容量承诺是：在明确的部署身份与保证版本下，按新鲜实际采样和持久承诺做保守记账，以真实 quota/cgroup 限制每个受管执行；容量不足则在 permit 前等待，资源耗尽则按事实收敛。它不宣称专用 guest 数据池在未来 metadata、日志恢复、修复或维护下永不 ENOSPC。

成功标准分三层：实现与回归证明此契约；独立 native 验证证明实际 enforcement/身份/宿主隔离；最终真实产品闭环证明交付和接受。任一层缺失不写 ready，也不关闭 OpenSpec 6.5/8.4/9.3。当前所有执行门禁继续关闭。

## 📋 当前事实与方案选择

ADR 0062 定义固定 profile、80% aggregate admission、permit 前等待及资源耗尽收敛；后续准备流程增加了完整未来 metadata 与宿主物理证明。现有 PG 已实现容量预留与 source pin，Docker 已按 Create → 检查 → 单次 Start 编排；生产 running snapshot 仍没有经过新契约批准的真实发布链。[^adr][^source][^pg][^docker]

已核查的 QSTATV/bulkstat/handle/GETFSMAP 接口不能在当前约束内补齐 C03 完整 inventory、generation、BMBT root 与一致性。Q_GETINFO 只见证它自身那次 active-type get_state 的 ip 分支，不能给独立 QSTATV 零值消歧。新契约不为这些证明增加采集接口。[^research]

| 选择 | 处理 | 理由 |
| --- | --- | --- |
| 在原 Trust、manifest 和 PG owner 绑定显式保证版本 | 采用 | 保证范围可审计，复用已有签名与账本，旧 evidence 不静默升级 |
| 新建 growth-debt ledger，继续完整物理增长证明 | 本 M1 范围不实施 | 用户已移除专用 guest 内部未来 metadata 上界的启动前义务；保留研究材料供更强保证另案使用 |
| 直接允许旧 Trust 或删掉拒绝条件 | 拒绝 | 会丢失范围版本、旧 evidence 隔离和原授权链 |
| 增加 Q_GETINFO / quota inode fd / rmapbt 作为启动前置 | 本 M1 范围不实施 | 未补齐目标证明，也不是 byte quota 执限所需事实 |

## 🧭 保证边界

| 项目 | M1 保留的硬条件 | 不宣称的保证 |
| --- | --- | --- |
| 宿主与环境隔离 | 专用 VM/存储 identity，既有服务不改动；宿主 backing 独立物理安全边界与资源共存验证仍必须通过 | 瞬时 df/内存余量不是长期物理边界；本设计不以 guest ENOSPC 风险转移给旧宿主服务 |
| CPU/memory/PIDs | `local_standard_v1` 固定 2 vCPU、4 GiB、512 PIDs；core 与所有 resident 的 hard upper 计入80% | idle 不变成零成本，continuity quota10不等于10个重执行槽 |
| Worker layer | 实际 XFS project byte quota soft=hard=10 GiB，project inheritance、真实CID/FS/Engine及防篡改边界有效 | StorageOpt 回显不证明执限；10 GiB不是整个Session或inode数量上限 |
| 持久数据域 | core/product/Engine等受管物理域各有实际 byte hard limit，覆盖不重不漏，计入剩余承诺 | 不把每个coverage role重复扣费；不保证byte quota覆盖全部filesystem内部metadata |
| 当前事实 | exact identity、可信来源、新鲜时间窗、完整受管 inventory、无未知未受控数据writer | 不宣称多个系统调用构成原子filesystem快照 |
| 生命周期 | 原PG owner、reserve-before-permit、单次effect路径、重放/迟到fencing、unknown effect保留 | 不用删目录/404/Task终态证明全部物理历史增长已归还 |
| guest可用性 | 已证实的耗尽停止新执行，保留身份与可解释状态；DB不可写则不再发放执行权 | guest metadata/log/replay/repair耗尽可能同时影响Backend/PG；不承诺这种情况下仍能立即持久化waiting_user |

C01 保留与实际 ABI/enforcement 有关的 loaded binary、Engine、FS、boot 和 mount 绑定；不再为未来 metadata 公式要求精确 SRU 数学对应。C02 保留受管资源/data-domain覆盖与副作用owner/fencing；不要求证明全部内核后台metadata写入的未来峰值。C03 保留 project byte quota、raw inode事实与实际目录继承；完整 quota inode物理集合/Q0/root不再是此保证的前提。C04/C05 的 guest未来metadata上界退出 M1 启动门禁。C06 的宿主backing安全部分继续开放，guest系统盘风险仍需被限制在任务专有环境内。C07 不新建数值增长债务，原 consumed owner、source pin和unknown-effect保留继续有效。N01/N02/P01/R01/U01的native/PG/真实体验义务不因本选择关闭。原closure matrix及历史seal不改写。

## 🔏 显式版本与兼容

### 唯一保证定义

新增服务端固定保证 `m1_accounted_capacity_v1`、revision=1，仅适用于 `single_user_local_v1`。其 digest 由固定字段顺序、无空白JSON的UTF-8原字节计算SHA256。定义内容固定如下；它不是容量观察或授权凭证：

```json
{"id":"m1_accounted_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":true,"authority":"original_pg_capacity_owner"}
```

只接受编译期注册表的exact id/revision/digest。TaskSpec、Frontend、Assistant、Worker、Agent和采集payload不能选择保证版本、修改数值或填ready。该定义不新增执行开关。

### Trust 与部署 manifest

复用现有 `capacitysnapshot.Trust`，新增 `admission_policy,omitempty` 可空字段，值为 `{id,revision,digest}`。缺省nil保持旧JSON字节形状和旧legacy验证语义；旧Trust仅可继续用于既有legacy/fixture/诊断入口，新的生产组装要求此字段存在且匹配服务端注册表。不能提供“missing means accounted”的fallback。

新增root-owned、只读的 admission manifest 文件，schema固定为 `switchback.capacity.admission_manifest.v1`，包含：GuaranteeRef、exact Identity、ResourceProfileRef、NetworkProfileRef、冻结源码manifest digest、部署输入/image/binary清单digest、宿主边界receipt digest及native执限验收receipt digest。不存在ready布尔值，也不存secret。以上digest必须指向实际存在且通过对应验证的证据；一串哈希或operator填表本身不构成验证。宿主boundary的具体实现方式仍按独立物理安全门禁核实，本设计不宣称现有环境已经满足。

manifest原字节digest作为新 `Trust.ManifestDigest`；running signed Facts与created-layer IPC Binding继续使用这个字段。Trust的identity/profile/key与manifest须逐项相等。新增服务端配置 `RuntimeCapacityAdmissionManifestFile`，对应 `SWITCHBACK_RUNTIME_CAPACITY_ADMISSION_MANIFEST_FILE`，仅是固定本地文件路径，不是远程endpoint或profile选择器。配置Trust时三项路径必须成组：Trust、snapshot目录、admission manifest；未配置时继续uninitialized，不临时生成任何一项。

文件读取复用现有安全祖先/nofollow/root owner/权限/长度限制做法，拒绝duplicate/unknown/missing/null字段；旧文件读取器的兼容规则不全局改变。manifest先校验，随后才能组装新的production source；nil/错误policy、无宿主或native receipt、候选source/image不匹配均不得落入allow。

区分“manifest结构与digest绑定有效”和“当前部署已通过宿主/native验证”。S2只完成前者；不能因receipt文件存在或digest匹配就构造可执行的部署。后者由S4的实际native数据源与独立宿主边界验证器产出私有字段的部署验证结果；任一验证器未实现、unsupported、未运行或已失效都返回unavailable。测试替身不进入production factory，也没有bool参数可跳过这层。当前宿主backing验证仍开放，本文没有替它选择或批准新磁盘/volume机制。

### PG 与 receipt

保证归属原 `runtime_capacity_pools.trust_bytes/trust_digest`，仍由 `InitializeTrust` 一次固定。不同policy/manifest不能就地替换已初始化pool；旧DB保持可结算，进入新保证需另行批准的全新私有环境/DB或专门迁移设计，本阶段不做自动epoch reset。[^pg]

不新增pool、quota owner、Task状态或growth-debt表。新增内部 `CapacityGuaranteeRef` 值对象，并在 `CapacityReservation` 的服务端生成字段 `Guarantee` 中记录它，随既有 `runtime_capacity` StartSnapshot保存；target/idempotency key不由调用者新增policy参数。加载reservation时从已固定pool Trust解析其Guarantee；生产consumption、source pin、pre-Start需重验StartSnapshot中的ref一致。nil保留legacy语义但不能经过新的production composition。

保留signed raw v1、raw v2、created-layer raw v1与IPC wire/domain。新manifest绑定和Trust policy属于部署解释范围，不把旧raw v1说成含inode字段，也不把 `VerifiedEvidenceV2` 转换为原 `Verified`。不复制或重新签名旧consumed evidence来获得新保证。

## 🧮 准入算式和采样

沿用当前原PG事务下的算式及整型溢出检查，不增加猜测的margin或inode cap：[^budget]

```text
CPU_commitment = sum(core CPU hard upper) + sum(nonremoved resident CPU hard upper)
MEM_commitment = sum(core memory hard upper) + sum(nonremoved resident memory hard upper)
BYTE_remaining = sum_distinct_static_domain max(hard-used, 0)
               + sum_nonremoved_resident max(layer_hard-observed_used, 0)
               + new_resident * 10 GiB

CPU_commitment(+new) <= floor(Engine_CPU * 80 / 100)
MEM_commitment(+new) <= floor(Engine_memory * 80 / 100)
BYTE_remaining <= floor(anchor_available_bytes * 80 / 100)
```

无有效used读回的既有resident按全额layer hard保留。已用字节已经降低anchor available，不再全额重复扣；多个coverage role映射同一个物理domain只计一次。CPU/memory按resident hard upper，不能从瞬时idle值借额度。core包括observer/publisher等全部受管基础服务的总cgroup hard upper。

30秒采样窗口、最多未来2秒、单调sequence、pool epoch、Engine/boot/mountns/FSUUID/device/mountID身份和scope不放宽。snapshot中的任何不明受管container、项目冲突、无限制持久数据域或观察与账本identity不一致仍拒绝。通过80%比较仅代表该次记账条件成立，余下20%不标为metadata增长已获数学覆盖。

周期publisher是必要但尚未实现的后续组件：部署方私有signing key，限定本地native数据源，在exact core cgroup内运行；它只能发布真实事实，不能发permit。不得把unsigned `Collection.Status=preparation_blocked` 改成ready作为签名依据；需独立、typed、白名单字段验证和受管部署receipt校验。旧collector诊断及其未知项原样保留；旧all-time metadata诊断不再是新保证的否决项，其他必需事实缺失依然否决。publisher与独立HMAC observer不合并密钥或权限职责；实际安装/运行另行授权，资源纳入同一core上界。

## 🧱 inode事实与byte quota的分离

当前 `CreatedLayerVerifier.checkQuota` 强制target inode hard>0；这是现行保守子集，不能静默全局删除。[^inode]

- legacy Trust仍按原规则，0仍拒绝，既有raw/测试语义保持。
- 只有exact新policy选择单独的accounted检查：target block soft=hard=10GiB、used<=hard、project/ABI/flags/设备/执限全部仍必须通过。
- inode hard与used从同一112-byte target raw按uint64读取并保留。hard>0且used>hard时拒绝；hard=0记为 `inode_bound_unproven`，不把它解释成已证明的有效无限制，也不把default0值补进target。
- hard=0下byte quota可以合规；这不表示inode、quota inode metadata或其它metadata受未来数量上界保护。default0及quota类型状态继续原样保留，无自动policy修改。
- 非零hard的等号边界不承诺下一次创建inode必然成功；实际EDQUOT仍可能发生并按下节收敛。全部字段采用无符号比较，不为尚未使用的inode预算转成int64或相乘。

不新增Q_GETINFO、rmapbt、open-by-handle、raw磁盘读取或inode cap配置。这些接口不能为此byte accounting保证增加必要权威。

## 🔄 副作用顺序与一次性语义

保留当前原owner调用顺序；新增policy校验嵌入原短事务。事务内不做FS、Engine、签名文件或网络IO。native创建检查依然是认证有限事实，随后必须回到原PG authorizer。[^coord][^docker]

```mermaid
sequenceDiagram
    accTitle: M1 Accounted Capacity Start
    accDescr: The original PG owner reserves before permits, authorizes each side-effect boundary, and retains unknown effects. Native quota facts do not grant execution.
    participant backend as Backend
    participant pg as Original PG owner
    participant fs as Source and storage
    participant engine as Docker Engine
    participant observer as Managed observer

    backend->>pg: Fresh sample and policy; reserve before permit
    pg-->>backend: Original reservation and GuaranteeRef
    backend->>pg: Consume and persist Run StartSnapshot
    backend->>fs: Read-only source Plan
    backend->>pg: Bind source plan; authorize preparation
    backend->>fs: Prepare and handoff
    backend->>pg: Recheck same owner and policy
    backend->>engine: Stage secrets and Create once
    engine-->>backend: Exact CID
    backend->>observer: Bound CID/profile/manifest request
    observer-->>backend: Authenticated finite quota facts
    backend->>pg: Recheck original owner and policy
    backend->>engine: Reinspect created state; Start once
    Note over backend,pg: Unknown outcome retains owner and resident; no blind retry
```

ownership.Created=false、既有容器、Start响应丢失、Prepare部分失败和未知cleanup继续进入原reconciliation路径，不生成新token/CID尝试。policy变更或旧epoch不得通过重新配置规避旧owner。排队continuation、rework、standalone及recovery新effect路径仍保留 `capacity_path_not_integrated`，首轮只开放已完整接线的task_initial；不因本文顺带解锁其它Run入口。

## 🚦 耗尽、不可用和恢复

| 事实 | 行为 | 保留 |
| --- | --- | --- |
| reserve前记账不足、freshness/identity/policy失败 | 不发permit、不创建active Turn/deadline，保留FIFO与可解释capacity等待 | 原Session身份、排队顺序 |
| Prepare/Engine effect前可证失败且没有effect | 原协议允许的拒绝/结算；不伪造容器 | 原source plan和失败原因 |
| Prepare/staging/Create/Start效果未知，含响应丢失 | 拒绝重试新effect，保留original owner/resident，交显式reconciliation | CID若已知、阶段、source digest、exact policy、错误/时间 |
| 已归属Worker的可信EDQUOT/ENOSPC、OOM/PID hard-limit事实 | 按原resource_exhausted进入waiting_user并停止受影响执行，不当普通runtime loss自动重建 | Session/Run及结果/Trace；resident不凭Turn终态释放 |
| Backend/PG所在guest池耗尽或PG不可达 | 停止发放新授权；不能把DB写失败当业务waiting_user已提交 | 允许当前确定性stop/fence，记录本地有界诊断；PG恢复后按原owner reconciliation |
| 文本报错或Engine generic500无法归属资源 | 报resource/IO状态不明，不从文本自动判ENOSPC或推导清理成功 | unknown-effect与原owner；无盲重建 |

故障本地诊断不成为新授权/退款owner，也不无限写满磁盘；不得附带全盘修复、prune或删除旧数据。停止进程不承诺撤销已发生外部副作用。PG恢复前无法提交的Task状态不由UI或内存假补；恢复后只消费exact旧identity和幂等键。发生真实耗尽时是否能即时渲染waiting_user属于可用性限制，不影响“没有新有效PG授权就不能启动”的边界。

## 🛠️ 实现拆分与文件职责

本节是正式设计的交付拆分，尚不是执行计划。审阅后再按writing-plans产出逐项代码/命令计划；不在本轮写产品源码。

| 切片 | 文件/责任 | 可独立验收的结果 |
| --- | --- | --- |
| S1 保证版本与兼容 | 新增 `internal/platform/config/runtime_capacity_guarantee.go`；修改 `application/runtime_capacity.go`、`capacitysnapshot/snapshot.go`；新增policy验证文件及各自tests | exact ref、legacy nil字节兼容、未知版本拒绝、无执行权；不触碰publisher或环境 |
| S2 部署与原PG绑定 | 新增 `capacitysnapshot/admission_manifest.go` 与tests；修改 `localproduct/config.go`、`runtime_capacity_source.go`、PG `runtime_capacity.go/tx.go/execution.go`、StartSnapshot构造及测试 | production拒绝legacy；manifest/receipt/source/profile结构绑定；native验证未完成仍unavailable；原reservation与source pin带同一Guarantee，真实PG并发/重放约束仍成立 |
| S3 新policy的inode语义 | 修改 `created_layer_verifier.go`，新增 `created_layer_accounted_test.go`；更新少量transport/production composition回归 | 仅新policy允许target raw0作为inode_bound_unproven；legacy仍拒绝；block/identity/quota失败仍零Start |
| S4 typed native事实发布与故障收敛 | 新增独立capacity publisher命令/mapper，扩展已有有限native采样需要的identity字段；localproduct/resource故障与bounded日志测试 | 无合成事实publish；无DB授权publish；host/native缺receipt拒绝；ENOSPC/PG不可写不造成重复effect或伪waiting提交。该组件先形成自己的详细设计/执行计划 |
| S5 native与体验验收 | 新阶段exact candidate manifest、N01/N02、真实quota失败/成功对照、原PG/current-source回归、Create/quota/Start、正式UI流程 | 有实际运行/报告/接受/观测证据后才关闭对应门禁 |

路径在表内均相对于唯一工作树 `/path/to/switchback`。S1–S3不安装或启动环境；S4离线实现也不能替代实际publisher/native验收。S5必须在新具体对象、资源与命令授权后执行；已消费采集器和付费任务不重放，不自动追加第二次provider尝试。

不新增migration：Trust已有bytea、原pool不可变、reservation可由原pool提取Guarantee并固化进StartSnapshot。若实现发现需要可变policy或多policy共存，停止本方案扩张，另行设计epoch迁移；不能临时给executions补一个不受原owner约束的policy表。

## ✅ 必需回归与验收矩阵

以下均为待实施验证要求，本轮没有运行这些测试：

1. **兼容与防降级**：legacy Trust marshal字节形状不变；未知/缺失/null/错误policy和manifest拒绝新production composition；raw v1/v2签名domain及现有格式不变；old manifested evidence不能适配新manifest。
2. **保证与副作用绑定**：Guarantee digest、source/host/native receipt、profile/identity被逐一篡改均拒绝；StartSnapshot被改、source pin后policy不符、过期sample，必须在Prepare或Create/Start对应effect前拒绝。
3. **原PG并发**：最后额度两请求仅一个reserve+permit；相同请求响应丢失回放返回原token/Guarantee；不同target拒绝；source pin事务rollback不留半绑定；consumed permit TTL不制造新effect；旧pool不自动迁到新policy。定点检查现有锁顺序，事务内无文件IO。
4. **记账边界**：storage80%等号通过、超1拒绝；溢出拒绝；同物理domain多role不重复；未知resident按全额、idle不释放；删除/404/Turn终态不凭空提高可用预算。
5. **inode与真实byte quota**：accounted targethard0+block合规通过有限检查，仍标inode_bound_unproven；legacy同raw拒绝；hard>0且used>hard拒绝；target/default互不补值；112-byte缺失/错误project/flags/byte hard/soft/overflow仍拒绝；full-uint64 inode值保持。
6. **跨边界顺序**：fake Engine记录Create→authenticated quota→PG重验→Start一次；各拒绝和取消点证明零Start；已有CID及Start响应丢失不第二次Create/Start；这组只证明离线编排。
7. **耗尽注入**：typedEDQUOT/ENOSPC、未知IO、DB写失败、partial Prepare、secret staging失败、Start unknown分别注入；不把所有generic errors映射为resource_exhausted，不确认未提交waiting_user，不自动退款/重建。
8. **native独立验收**：actual image/binary/kernel/FS/namespace/能力/cgroup/host backing；Backend CHOWN/DAC_OVERRIDE/FOWNER三项及缺cap负例；Worker ioctl含compat、GET/普通IO对照；真实小quota失败/大quota成功且池未耗尽；exactCID从created到Start一次。
9. **最终产品**：当前候选重建及复核，报告Artifact完整性、真实initiator accept、刷新身份一致、同Task观测查询均成功。组件绿、PG绿、HTTP200、签名成功不代替这一项。

## 📦 生效、退出与旧证据

本文仅定义目标。正式设计审阅通过后，按S1开始离线实施；每片独立可核验，后续新组件先完成自己的具体设计。之后另行批准native环境窗口。新保证通过最终验收后，才在当前进度overlay将C04/C05记为“在该保证下不适用”，不能写成物理证明完成；C06/N01/N02/R01/U01各自凭新证据收口。

若后续回到完整物理保证，发布新的guarantee版本和manifest，在原owner允许的显式环境转换中重新验证；不修改本版本含义。故障停用只关闭新授权并保留现有pool/resident/Trace/DB；不做down -v、prune、自动清数据或把旧会话恢复成新identity。

## 🔗 来源与当前状态

[^approval]: 直接用户范围批准与证据绑定 (private evidence retained)；已批准路线比较 (private evidence retained)。
[^adr]: [ADR0062](/path/to/switchback/docs/adr/0062-use-one-fixed-m1-runtime-resource-profile.md:11)；[现行执行计划](/path/to/switchback/docs/superpowers/plans/2026-10-11-isolated-linux-experience-execution.md:110)。
[^source]: [当前production composition](/path/to/switchback/internal/localproduct/runtime_capacity_source.go:29)；[Trust/Facts](/path/to/switchback/internal/workerruntime/adapter/capacitysnapshot/snapshot.go:29)。
[^pg]: [InitializeTrust/ObservePool](/path/to/switchback/internal/workerruntime/adapter/postgres/runtime_capacity.go:36)；[原reserve/consume事务](/path/to/switchback/internal/workerruntime/adapter/postgres/runtime_capacity_tx.go:89)；[source-plan授权](/path/to/switchback/internal/workerruntime/adapter/postgres/runtime_execution.go:23)；[migration27](/path/to/switchback/migrations/000027_runtime_capacity_ledger.sql:1)。
[^budget]: [现行预算算法](/path/to/switchback/internal/workerruntime/adapter/postgres/runtime_capacity_budget.go:85)。
[^inode]: [当前created-layer byte/inode检查](/path/to/switchback/internal/workerruntime/adapter/capacitysnapshot/created_layer_verifier.go:129)。
[^coord]: [副作用边界编排](/path/to/switchback/internal/workerruntime/application/runtime_coordinator.go:96)。
[^docker]: [实际Create/preStart/Start顺序](/path/to/switchback/internal/workerruntime/adapter/docker/provisioner.go:117)。
[^research]: 接口可行性与源层限制 (private evidence retained)。

本轮只新增正式设计及独立scratch证据；原897输入保持。源码/测试/运行配置未修改，Go/native/VM/Engine/PG/Worker/provider未执行。方向已批准不等于本设计或实现已批准；`capacity_ready=false`、`execution_enabled=false`、`paid_consumed=false`，OpenSpec保持38/41。

