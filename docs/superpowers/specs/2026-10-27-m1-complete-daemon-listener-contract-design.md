# M1 完整 daemon Unix 监听合同修订候选

状态：用户已明确接受完整方案，授权 Python/Go 同步修正、回归和新候选，仅离线实施。本文不授权进入 guest、停止服务或重跑失败窗口。

## 已证明的根因与当前进度

2026-10-27，kit21/bundle-0001 的一次真实准备运行106.803991秒后按失败即停结束。完整前检、旧对象保留、四新副本校验均成功；Engine执行到第9步 `engine_ready`。`effective_configuration`、`containerd_start`、`containerd_ready`、`engine_start` 都成功。原watchdog修正已在真实窗口通过对应检查。

失败为 `hidden/foreign Unix listener`。在保存的完整 inventory 中，有三条附加监听，均由同一个已核验的 dockerd PID68199/start117500083/exe/cgroup拥有：

| 角色 | 本次路径 |
|---|---|
| extension callback | `/run/switchback-engine/m1-resident/docker/extensions/callback.sock` |
| internal metrics | `/run/switchback-engine/m1-resident/docker/metrics.sock` |
| libnetwork external key | `/run/switchback-engine/m1-resident/docker/libnetwork/0ab5b456481f.sock` |

现行 Python `contract.validate_listeners` 仅接受三条主socket（containerd API、ttrpc、Docker API）；Go `qualificationEngineCurrent` 同样要求恰好三条，因而不能只修Python。仓库旧的独立诊断器 `deploy/experience/engine_listener_check.py` 已有这三类附加socket的固定profile，但该规则未接入当前resident Engine部署合同。

原始139条Unix记录完整重放可稳定复现同一错误。仅在诊断内存副本删除上述三行后，原validator通过；这个实验定位缺口，不能作为准入或修复，原始记录没有删除或改写。三个owners全量字段均匹配，故当前证据支持“部署监听profile不完整”；没有把未知owner或非监听项误标作为既成根因。

原始host回执、完整观察帧、六份分阶段inventory、RED重放和摘要保存在 `.scratch/runtime-m1-kit21-carrier-execution-next`。回执记录两个daemon曾成功Start；失败后未再访问guest、未Stop或复查，因此不声明它们当前仍运行或已停止。新PG、Native身份/秘密、provider凭据读取、镜像构建和条件首启都未进入。

## 路线比较

1. **推荐：完整固定profile、独立v2合同。** Python collector/validator与Go native/readback同步接入六类必需角色，完整覆盖后再准入；旧v1证据维持历史语义且不授予新准入。工作量明确，避免下一阶段被Go再次拒绝。
2. 在v1里临时跳过上述三条：短，但修改旧证据解释，且容易只修Python、遗漏Go、路径元数据和碰撞检查。不采用。
3. 修改daemon版本/构建或尝试禁用内部socket：改变已固定的运行机制与二进制验收基线，范围更大，现有证据也不足以证明可关闭全部三类。不采用。

## 推荐边界

- 新live profile固定6个角色：现有3条主socket加callback、metrics及恰好1条libnetwork。前两条附加路径精确固定；libnetwork仅允许固定私有目录下12位小写hex basename加`.sock`，每次新建identity重新绑定，不能硬编码本次随机ID。
- 每个角色均需要完整的kernel inode、filesystem inode/dev/type/uid/gid/mode/nlink、父目录身份、同一PID/start/exe SHA/cgroup/boot，以及前后稳定的FD/进程归属。路径字符串或“由dockerd打开”本身不足以放行；当前回执缺少三条附加socket的FS身份，不能把合成字段冒充实际取证。
- 两个静态附加socket的固定权限和libnetwork权限，实施前核对当前固定Moby版本及其固定extensions依赖源码；旧诊断器的0777/0777/0600为待交叉核对的既有profile。仍须证明所有相应父目录私有，拒绝symlink、错owner、跨FS、同名多inode、重复/缺失角色、共享foreign owner和前后漂移。
- 完整Unix/TCP/TCP6与FD/进程扫描保留；未知daemon监听、隐藏/非受管同名socket、daemon TCP/TCP6监听仍拒绝。既有IPC、quota/容量、资源、PG/Trust/Start门禁及Native权限保持。
- 新`listeners.v2`/`deployment_receipt.v2`及Go消费链一次接线；v1旧validator/回执保留历史解释，live不静默回落v1。上层publisher/qualification若绑定receipt schema，一并验证并拒绝混版本。
- 失败归档直接携带新建archive根身份，避免已知根inode再次只能从追加只读窗口取得；Engine非零回执在同一次本地调用结束后即归档，保留“失败即停、不再SSH补查”。这两项只增加已持有事实的保存，不授予准入或触发额外现场动作。

## 离线交付与验收

接受后一次完成上述Source/跨语言合同、同一真实回执的不可变回放、严格负例、完整新kit/bundle及独立评审；不改旧seal或当前安装物，不读取凭据，不执行新ELF，不进入guest或启停服务。

必须覆盖完整六角色正例，缺/多/错路径、随机ID格式、owner/PID/start/cgroup/hash/boot漂移、socket文件/父目录元数据不符、重复kernel或FS inode、foreign FD、TCP监听及新旧版本混用。正例里新增FS字段必须标为离线构造fixture，不能升级为实证。Python和Go对同一组语义用例给出一致判定。

候选构建仍受原Source/templates/binary绑定与旧证据隔离约束；原120秒Engine、240秒qualification、300秒barrier及7200秒外部准备预算不变。guest argv仍严格小于65536；当前最大65425，余量有限，若完整新候选不能满足原边界，必须报告具体超限并在真实执行前解决，不擅自扩限或宣称可运行。

当前已Start的daemon及新安装物不在本次离线修改范围内。离线候选完成后，须另行完成“如何证明归属并切换当前活跃Engine”的具体操作清单；不假定旧的unloaded-only门禁能直接重跑，也不把新设计当作Stop/恢复授权。

## 来源

- 当前源码：`deploy/experience/engine/contract.py:602`、`internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_readback.go:199`、`deploy/experience/engine_listener_check.py:150`。
- [Moby docker-v29.9.0 daemon.go](https://raw.githubusercontent.com/moby/moby/docker-v29.9.0/daemon/daemon.go)：创建extension host，并注册ExecRoot下的metrics.sock。
- [Moby docker-v29.9.0 libnetwork external-key listener](https://raw.githubusercontent.com/moby/moby/docker-v29.9.0/daemon/libnetwork/sandbox_externalkey_unix.go)：创建ExecRoot/libnetwork/<short-ID>.sock并设置0600。该来源不替代当前socket实际FS元数据。

本候选先明确完整修订范围；实际源码、ABI与依赖的精确清单在设计获准后由writing-plans制定。未经新真实授权，不停止当前daemon、不恢复旧失败窗口、不调用模型。

## 离线实施澄清：监听与已建立连接

保存的完整139行现场表中，containerd.sock 有一条监听inode491363及两条已建立非监听inode493189/493723，三者均由已绑定containerd拥有。本文“同名多inode”约束精确指同名多个监听对象；已建立非监听连接继续遵守原有有限路径、完整owner/FD/PID/start/exe/cgroup校验，不能按路径字符串删除或统一拒绝。实施需以保留全部139行、仅补明确标注合成FS字段的正向回放验证这一点；原始缺FS证据仍拒绝。这是保留既有连接语义的实现澄清，不增加角色、权限、环境动作或未知owner接受条件。
