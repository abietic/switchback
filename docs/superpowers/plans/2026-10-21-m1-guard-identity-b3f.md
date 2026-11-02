# M1 B3-F Guard Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将已证明的目录nlink兼容修复集成至全新、未执行的单次诊断候选，并以真实隔离本地文件系统及完整fixture套件验证。

**Architecture:** 继承W的八源与单次窗口，只修复guard稳定身份比较和fixture值语义；更新全新固定身份与封存来源。三份pure mapper/test逐字节继承，ELF/build/data均引用已封存输入。

**Tech Stack:** Python3 unittest、POSIX本地文件端口、JSON/SHA256；无VM/Docker/PG/provider执行。

## Global Constraints

- Worktree=/path/to/switchback；HEAD=fbebfe754045bdbf1af40871a5018eed33743011、branch=codex/observability-master-integration-1009、index不变，不stage/commit/push/reset。
- F=.scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021；W=.scratch/runtime-m1-deployment-diagnostic-window-b3e-1021；E=.scratch/runtime-m1-deployment-diagnostic-execution-b3e-1021；P=.scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021。F以外旧文件只读；prior-carrier-manifest.json固定286项；Git-visible基线1785，仅新增本spec和plan两条登记路径，最终1787。
- 本轮仅离线实现/验证/评审/封存。禁止NativeHost/NativeSupervisor/host CLI/fork/transport/guest/ELF/VM/unit/Engine/PG/Backend/Worker/provider执行；允许memory fixtures和新独立临时目录中的本地guard文件系统测试，不得指向真实window。capability/resource/Trust/quota/keys与三个gates不变且false。
- F runtime/test source仍只有guest.py,host.py,output_mapper.py,manager_forensics.py,test_guest.py,test_host.py,test_output_mapper.py,verify.py八份。output_mapper.py/manager_forensics.py/test_output_mapper.py逐字节复制W；guest只允许四个固定身份literal变化，所有guest业务/资源/协议逻辑不变。ELF仍5658871bytes、host0400、SHA38b02a79d69b87317db97679db04a2df3a75c3ddcf6fe9bedf47b1e2601cb1c5，build-receipt SHAff631c6b4ba3bd96ea8b1a168a3b56cfb33e28255d236353edd871e7153e1127，不build/执行。guest/bootstrap分别<=65536。
- 新身份只用于未执行候选：nonce=36884bd8-2a04-411b-be72-8c732eeec1c0；stage=/var/lib/switchback-deployment-diagnostic1021-b3f；unit=switchback-deployment-diagnostic1021-b3f.service；cgroup=/system.slice/switchback-deployment-diagnostic1021-b3f.service；window=.scratch/runtime-m1-deployment-diagnostic-native-window-b3f-1021必须absent。旧B3-E窗口已消耗，必须保留原样且只含window.once，SHA6f573a55ea9b2fd526a4504142de9fa35ee87700f166f9ac6f074abe4d58ab0c；不得删除/reset/rename或重用旧授权。
- 单次window、RAM cursor、私有worker握手/父fencing、150秒总限含最后1秒cleanup、30/45/45/30步骤上限、32768摘要、pre-fork writable nonblocking pipe/socket校验、noresume/noindependentreadback/noStop/unknown termination全部保留。schema和guest v3不升级。
- marker nlink=1及目录device/inode/uid/mode身份检查保留。own marker写入前后不要求directory nlink恒定；最终held-FD和named metadata必须完整一致，再比较初始稳定身份。required写入/fsync/close失败、取消或超时均关闭窗口，永不从receipt恢复权限。
- 新baseline/来源必须绑定286旧文件、977旧inputs、9份B3-D、814历史seal entries（646+154+14），五份seal的SHA与count严格固定。新verify只读固定输入无selector；不调用旧verifier/native入口。注册两个docs路径；plan进度字节不参与candidate selfhash，最终seal绑定快照。

---

### Task 1: Integrate guard fix, fresh identity, complete regression and binding

**Files:** F八source、source-manifest.json/candidate.json/release.json、两份fixture provenance、RED/GREEN/full-suite/verify回执、task-1-report.md。F预置baseline/identity/auth/design/constraints/prior manifest不可改。root owns docs/ledger/review/seal。

**Interfaces:** 消费spec与F/identity.json、W八源、E/guard_replay.py及guard-stable-identity-candidate.patch；产生相同run_window(io)、durable_guard(io,deadline)、supervise(io)、唯一window CLI和固定verify接口。schema维持现有switchback.b3e.*及guest v3，身份只按新literal更新。

- [x] **Step 1: Candidate setup and red regression.** 将W八源复制至F。替换源中的PHASE/WINDOW/四guest身份到F/identity；host.PRIOR仍指向P的ELF/build，test_guest artifact引用保持P，pure mapper三份必须相等。HISTORY扩展两个exact seal154/14。candidate approved_spec指新spec、approved_window固定F四份文件，prior_design继续绑定既有single-window路线；baseline/register docs按新1785+2校验。source/metadataSHA256绑定链逐一重算。读取E/guard_replay.py的完整五case作为新增test_host回归，移除in-memory替换而直接调用新h.durable_guard；metadata两端dict copy、marker后directory link变化。未应用guard修复时运行：

```sh
/usr/bin/python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021 -p test_host.py -v -k GuardDirectoryIdentityTests
```

必须捕捉guard_directory_changed，且不是binding/identity/missinghelper失败。保存RED所有source hashes和日志。新的LocalGuardIO在本地临时目录直接调用guard，其before-fix复现也可纳入RED，禁止调用NativeHost。

- [x] **Step 2: Implement precise fix and faithful fixtures.** 保留durable_guard其余原体，初始及最终metadata显式copy，按下列替换final旧require：

```python
meta = dict(call('metadata_fd', window))
current = dict(call('metadata_fd', window))
require(current == call('metadata_at', parent, leaf) and
        all(current[k] == meta[k] for k in ('device', 'inode', 'uid', 'mode')),
        'guard_directory_changed')
```

metadata_fd/at实现值语义：

```python
return dict(self.dir_metadata.get(path, self._meta(path, False)))
```

open_file_at的fixture在创建window.once后按所选目录模型更新directory nlink，file nlink=1。新增case验证2→3和不变、snapshot独立、四稳定字段每个变动、named path swap、marker多链接仍拒绝。LocalGuardIO端口仅标准os file操作，namespace位于独立临时parent；finally清理自己的目录，记录原语事实与native window始终absent。

- [x] **Step 3: Freeze and complete test once.** 修正verify的baseline1785+2登记docs、prior286、HISTORY814、guest仅身份差异、pure copies、旧marker保持、新window absent。新增AST/fixture回归能拒绝只忽略全部目录身份、放松marker nlink、删held-FD/name比较等旁路。重算8sources/candidate/release，before=after。固定env为HOME=/Users/example PATH=/usr/bin:/bin:/usr/sbin:/sbin LANG=C TMPDIR=/private/tmp PYTHONDONTWRITEBYTECODE=1；运行以下两命令，各一次最终全量；开发期只跑受影响类：

```sh
/usr/bin/python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021 -p 'test_*.py' -v
/usr/bin/python3 -B .scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021/verify.py
```

用exactargv/env/当前11项source+binding与ELF beforeafter、stdout/stderr/hash、exit/method count登记。必须全部pass、无native、无warning；若修复再变更，仅按影响重跑相关覆盖并准确陈述映射，不能冒称历史哈希current。guest/bootstrap/ELF事实重算；不运行Go/race/PG无关套件。

- [x] **Step 4: Independent task gate.** report列出全部变更、redgreen、完整测试、scope限制与后续接口；root生成对W完整diff+sourcefacts，一位fresh reviewer双轴spec/quality。C/I由fresh fixer一次整批处理并重跑覆盖测试、重绑定再复审；无commit。允许继续Task2条件为task review Approved且无C/I。

### Task 2: Final evidence, whole review, root seal

**Files:** F/source-final八snapshot+manifest、final-verification.json、operation-window.md、report.md、outcome.json、task-2-report.md；root owns finalplan/ledger、whole-review包与seal。

**Interfaces:** 消费已通过Task1冻结8source/bindings/current full-suite+verify receipts，产生可读最终证据、仅offline的操作候选。不得修改源码或绑定；rootreview元数据与整体代码双轴门禁后派生最终状态。

- [x] **Step 1: Assemble current evidence without reruns.** 复用Task1最终完整suite与verify，独立hash/log/方法枚举读回；source-final snapshot等于当前八源，明确本地FS只是隔离tempfile，不是carrier/guest验证。source/candidate/ELF不变；引用root独立baseline/prior/marker保护检查，不冒充再次运行。无重复套件。
- [x] **Step 2: Prepare operation candidate.** 唯一argv为 `/usr/bin/python3 -I -B .scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021/host.py window`；固定新身份、全窗口once/不可续/无独立readback、stdout pipe/socket要求、完整summary+最终exit联合判读、150秒与30/45/45/30/noStop/unknown边界及三个gatesfalse齐全。当前native未授权，原B3-E窗口失败保留，不清理或重用。report/outcome状态为pending_whole_review，不自封完成。
- [x] **Step 3: Final whole review.** root对W→F完整delta、新metadata制作包交fresh最强reviewer，同时给metadata和wholecode/evidence双轴verdict。无suite复跑，具体疑点才focused。C/I一批freshfixer+覆盖验证和独立复审。root最终按prior286+基线1785+2、old977/B3D9/814、HEAD/branch/index、old marker、新window absent核验。
- [x] **Step 4: Root closure and seal.** root仅更新anchoredcheckbox、最终状态与证据引用，保存plan/ledger快照；outcome=offline_complete、native=false、gatesfalse。seal包括全部F文件（除seal自身）和最终plan/ledger快照，逐项读回并核对文件集合。封存后不修改。最终说明新候选尚未实际执行，不再次请求离线实施批准。
