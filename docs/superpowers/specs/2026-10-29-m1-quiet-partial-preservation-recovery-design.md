# M1 静止状态下部分保存恢复与审计预算设计

_2026-10-29；候选设计，尚未批准实施；本次只读窗口不授予恢复、写入或首启权限。_

---

## 📋 目标与已验证事实

目标是把“部分对象已原样保存”和“审计记录填满”合成一个完整离线修订，复用 kit25/bundle0002，完成新载体、完整生命周期验证和独立复审，再申请一次新的真实恢复及条件首启。不再以重跑旧失败载体解决问题。

本次只读入口单次exit0，前后boot均为 `751079a6-74a4-4570-bca6-18e460c7f33d`；26位置、1344节点metadata完整，1175文件共93010510B哈希匹配。12个大文件仅metadata，`content_acceptance=false`。本次没有写guest、查询unit运行状态/EngineAPI、读密钥或调用模型。依据：accepted observation (private evidence retained)、actual summary (private evidence retained)。

| 原始对象 | 本次快照位置 | 恢复处理 |
| --- | --- | --- |
| `/var/lib/switchback/m1-engine` | source缺席；固定归档target存在，device64769/inode312116 | 新窗口完整重验后标为already_preserved；不反向移动、不再次rename |
| `/etc/switchback/m1-engine` | 原位存在，target缺席 | 新窗口满足全部门禁后原样保存 |
| `/run/switchback-engine/m1-resident` | 原位存在，target缺席 | 同上 |
| 两条固定Engine/containerd unit文件 | 均原位存在，targets缺席 | 同上，随后一次维护daemon-reload退出旧定义 |
| `/opt/switchback/qualification-kit`、`engine-bundle` | 均原位存在，targets缺席 | 同上 |
| `/run/switchback-engine/docker.sock` | source/target均缺席 | 新窗口仍须证明两处缺席；不制造socket角色 |

五个归档根均root:root/0700，其device/inode与失败窗口的创建事件一致。本次没有把归档容器的任意兄弟目录名作为完整清单发布，因此新窗口仍需逐个检查归档目录children精确闭合，不能由nlink2推断为空。最后quiet运行证明来自上一失败窗口，不能当作当前服务状态。

## 🧭 路线与取舍

| 路线 | 改动和收益 | 限制 |
| --- | --- | --- |
| A，推荐 | 新quiet-only部分恢复入口；原raw帧写单个append journal；分离审计预算与失败记录；全部fresh guard保留 | 私有维护、传输及持久记录模块需完整重绑和回归；不改正式Source/Go/kit |
| B | 部分恢复和预算分离保持，继续每帧一个文件 | 代码改动略少，但旧窗口44442次文件创建/持久化会继续产生大量IO与小文件，完整120秒窗口风险更高 |

选A。journal记录方式改变，原始wire帧和顺序/digest语义保留；不以缓存验证、抽样proc、删审计或放松权限来降低日志量。方案不承诺任何实际输入都必然落在硬上限内；超界仍拒绝，并明确保存可确定的部分效果和未知范围。

## 🔐 新窗口资格与权限

恢复只能从新的exact human grant、固定新phase、nonce和O_EXCL claim开始。旧grant、旧失败窗口、旧continuation和本次只读结果都没有恢复权限。VM必须已Running，不启动VM、不改分区/挂载布局，不删除、覆盖或回滚对象。

新入口固定为quiet-only：完整采集当前boot、manager、所有进程/FD/maps/listeners、store inventory v4及受保护树，证明无活跃Engine/containerd/Worker、无业务/恢复引用、无外来writer或冲突job。已加载的固定旧unit只允许已证明归属且inactive/无主进程；unexpected live或unknown即停。维护恢复不发Stop、不启动旧unit。两个单位的typed ExecStop/ExecStopPost与原properties、listener、默认bridge检查保持。

在任何mkdir/rename/reload或host checkpoint前，对全部公共原树重新做完整metadata与内容哈希：历史1187个regular文件总196652178B，包括此次未读的12文件；继续使用原每文件128MiB边界，不把此次128MiB aggregate只读抽样额度错误移作恢复合同。实际恢复读取总量须在新清单明确为完整集合，不能用expected hash冒充observed hash。

当前观察是初始候选，不是自动跟随的容错模式。新窗口中任一source/target同时存在、非预期缺席、inode/owner/mode/hash不匹配、symlink/mount变化、归档兄弟项不闭合、身份不明即拒绝。目录历史size/nlink/times不直接复用；先绑定新的完整基线，之后只接受本窗口已证明自身syscall造成的变化。

## 📦 部分保存模型与动作顺序

新增私有 `quiet_partial_v1` 布局模型，每个固定角色分别携带不可变origin、当前physical位置、original manifest/node digest和fresh observation。已经保存与本窗口新执行的move必须分开记录，不能把旧move塞入本窗口成功syscall列表。已保存树仍受每次fresh guard的writer/FD/maps、完整树及父目录验证保护。

1. 重新证明上述资格及全部树；固定识别5个已创建archive roots和1个已保存var/lib树，绑定当前完整identity与children。零新建归档根。
2. 原样保留已保存var/lib树。仅对其余6个非空source按固定顺序执行no-replace rename；每次之前保留完整stopped guard，每次之后验证完整节点和父目录变化。API两处仍须缺席。
3. 已保存1项加新完成6项构成完整保存结果。原样保留所有既有archive/copies/stage，不重命名回原位置，不做清理。
4. 在同一child内申请一次有界host checkpoint。宿主仍按原完整前提核验、no-replace保存两旧副本和9735 stage；本次只读未检验这些宿主对象，不能跳过fresh preflight。
5. 同一窗口ACK后重验全部保存树、进程与manager，只做一次维护daemon-reload并证明旧名称从完整installed/loaded catalogues退出。
6. 同driver消费一次内存continuation，继续原四新副本核验、未修改Source Engine120。仅canonical Engine成功后，才沿原顺序进行Native准备、所选凭据、镜像/新PG/迁移、资格→安装→Core条件首启。具体真实清单另行授权，不调用模型。

## 🧾 审计和失败记录

旧窗口的完整保存前缀为42561953B decoded event、59481731B wire；宿主基线7664854B与frame共用64MiB，总67102143B后只剩6721B。下一帧触发上限，最终失败记录也被同一计数拒绝。这些是该前缀的测量，不是完整生命周期上界。

| 独立预算 | 候选固定值 | 计数对象 |
| --- | ---: | --- |
| native decoded audit | 128MiB | 完整原event JSON bytes；单event仍1MiB |
| transport raw stdout | 192MiB | 实际接收wire，包括LF和非audit帧；单frame仍16KiB |
| host raw journal | 192MiB | 原始raw帧加LF，独立于常规宿主记录 |
| host ordinary evidence | 64MiB | 基线、checkpoint、命令和摘要；不再计入journal |
| host failure evidence | 1MiB | primary/secondary、stderr、未解析prefix、digest/count与已知部分归档/moves |

这五个上限独立，不能用raw小于decoded预算推断wire一定可容纳。候选值必须经完整T0—T8规模回归验证；不会因测试超界而自动增加。保持原120秒maintenance、120秒Engine、240秒资格、300秒barrier、7200秒driver及所有生产门禁。

journal以新私有host路径O_EXCL/0600创建，保留原始字节、顺序、rolling digest和已提交offset/count；每个完整frame在协议动作前append完整并fsync，不降低原先record-before-action的持久性要求。部分write/fsync不确定即停，记录最后确认offset及attempt，不能把未知尾部补成成功。不要在内存保存第二份全部raw帧，只保留有界parser状态、offset/count与当前event。

失败记录走独立有界路径；bulk或journal上限不能阻止保存已持有的pending/未解析prefix、错误和部分effects。保持原异常对象，secondary记录/close错误单独附加；所有记录介质同时失败时只报告无法持久化，不承诺物理空间保底。共享APFS可用性风险与现行容量保证保持。失败后不再读取child/guest来补证，不Stop/retry/rollback/cleanup。

## 🧪 完整离线交付与成功标准

- 捕获布局与反例：重放当前26位置/1344节点、source/target混合分布、五archive identities；完整内容接口必须覆盖此次metadata-only的12文件。篡改/多余children/同时存在/丢失/别名/mount/writer等均在首个副作用前拒绝。
- 必须执行真正的维护sequencer、preserver与传输接线的T0—T8 fake端口全流程：0 Stop、0 mkdir、1项already-preserved、6次固定rename、1次host ACK、1次维护reload、完整Source Engine与条件首启顺序。不能只测试孤立validator或靠抛错通过负例。
- 使用已捕获完整proc/FD/maps事件规模构造每个保留guard，不以几个空数组代表原样本；测量并记录完整生命周期的decoded/wire/journal/bulk/failure最大值与余量。明确fake输入与真实内核/性能边界。
- 每个预算exact-cap/+1、短读写、对象增长、EOF、时钟/截断、journal/普通/备用记录器失败及mutation后异常都必须有精确错误和副作用前缀断言，保存原始RED/GREEN证据。
- 一次受限宿主journal夹具只写新私有测试文件、使用已保存公开raw帧，验证append/fsync/offset和异常保留；不进入guest、不跑VM/Engine/PG/provider，不读密钥。不得由该夹具宣称实际Linux120秒必过。
- 完整新carrier必须重绑全部私有revision/程序/argv/config、原kit25/bundle0002的exact manifests、当前Scope/布局事实和Source/helper hashes；严格保留argv<65536、包expanded<2MiB/LZMA2 memory128MiB、原角色/修订全集与防混用回归。
- 完成独立Spec/Quality及whole复审、Source/history/HEAD/index保护、新操作清单和验封后，才申请一次真实恢复窗口。离线实施不创建guest文件、秘密、服务或actual grant。

## 🚦 不变项与尚未完成

正式Source/Go/模板/kit、既有PG/Trust/容量/quota/Create→Start门禁、默认bridge分类、Worker资源/网络/权限和CAP_SYS_ADMIN+CAP_SYS_PTRACE后续有限Native边界均不改变。M1继续使用现有存储和m1_shared_volume_capacity_v1，不引入外置盘或独立物理隔离保证。

本设计只收敛恢复与审计记录，尚未完成真实Core首启，也不表示Task→正式报告→页面接受→同任务观测已通过。新真实窗口成功后仍需单独准备有界模型任务的执行与验收；本设计没有付费模型调用权限。体验闭环通过后再做定向精简。

状态：设计自检已对照本次实际位置、完整内容覆盖限制、独立预算、角色和权限边界；无未填项目。等待用户接受A并授权整片离线实施，当前不实施任何行为变更。

