# M1 Diagnostic Single Window Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 关闭B3-E I1，把磁盘receipt推进权改为一个live cursor窗口，完成离线修订、监督验证、复审与封存。

**Architecture:** 新阶段复制并冻结旧guest/mappers，host唯一window入口在受监督worker内持有RAM cursor；父监督器无推进权。全窗口guard先持久确认，任何失败/退出禁止从磁盘恢复。保持四内部step和原guest协议/资源/取证。

**Tech Stack:** Python3.9+ stdlib,fixture unittest,POSIX private fork/pipe/process ownership ports (只写源码不执行)，现有linuxarm64 ELF只读复用，SHA256 JSON evidence。

## Global Constraints

- Worktree `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`,branch/index不变，无stage/commit/push/reset。
- W=`.scratch/runtime-m1-deployment-diagnostic-window-b3e-1021`; P=`.scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021`。用户已接受P/persistence-fix-design-candidate.md路线A；W/approved-design.md固定有界监督/guard细节，实施全部要求以其为准。本次无需再询问执行方式。
- Offline only：不fork/spawn native进程、不运行NativeHost/NativeSupervisor/host CLI/guest/ELF/VM/unit/Engine/PG/Backend/Worker/provider；fixture-only行为；不创建真实nativewindow或actual marker。不更改resource/capability/Trust/quota/keys。capacity_ready/execution_enabled/paid_consumed=false。
- 旧P全部118文件、1784Git-visible基线、977旧inputs、9份B3-D、646旧seal entries全部只读；新Git-visible仅本plan。新增source只在W，仍8文件guest.py,host.py,output_mapper.py,manager_forensics.py,test_guest.py,test_host.py,test_output_mapper.py,verify.py；其余仅固定data/provenance/证据/报告/seal。不得改旧P任何文件或旧ledger/plan。
- guest.py/output_mapper.py/manager_forensics.py与P逐字节一致；guest44810/bootstrap47418且分别<=65536。test_guest只改artifact fixture引用；test_output_mapper可原样复制。复用P artifact5658871bytes SHA38b02a79d69b87317db97679db04a2df3a75c3ddcf6fe9bedf47b1e2601cb1c5，host0400，ELF64LE/machine183；不重建/执行。build-receipt SHAff631c6b4ba3bd96ea8b1a168a3b56cfb33e28255d236353edd871e7153e1127。
- 新candidate仍stage=/var/lib/switchback-deployment-diagnostic1021-b3e,unit=switchback-deployment-diagnostic1021-b3e.service,nonce=c3369c90-981a-4dc1-ac9a-4717779ccab6；native window=.scratch/runtime-m1-deployment-diagnostic-native-window-b3e-1021/必须absent。本次没有重新生成nonce或替换ELF。
- CLI仅 `window`；原4mode或任意其他参数在构造native/监督、fork/目录/IO之前拒绝。4step仅内部，live cursor绑定owner/io/current step，不可从receipt/ack/目录重建。后续mode必须经过cursor；raw evidence≠authority。
- 全窗口guard确认exclusive/no-follow/owner/mode0700/0600/file+directoryfsync/close成功前0transport/guestIO；存在/部分/损坏目录guard均拒绝，不删/重置/修复/迁移。建立任何持久状态前失败仅能保证0guestIO，不伪称记住了该调用。
- 每步完整返回、所需persist/close/cleanup成功且未cancel/deadline才能RAM推进；原6例late fsync/close必须下一步0新process及0launch。任何failure/unknown关闭cursor，进程退出不可续，无独立readback。正常actual诊断failurepacket不等于control failure，但incomplete控制采集/unknownlaunch/rejectedguest不可继续。
- 原guest v3/schema、FD/ext4/挂载/identity/X_OK/16MiB门禁、rawfirst、两个hostmapper独立、主客actualonce、一次launch、readback无Stop全保留。退出事实分开，kinddiagnostic_only/authoritynone/execution_linkageunproven/remote_terminationunknown。unit声明原封，不新增capabilities。
- 150秒全窗口monotonic协议截止，含最后1秒父cleanup；step上限30/45/45/30且原1秒stepcleanup仍包括在内。父监督器无文件持久化/guestIO/推进权；私有worker startup握手与进程归属确认前0effect；deadline/父失联后worker返回时也不再nativeIO/推进。
- 监督父只用非阻塞pipe/select/poll/WNOHANG及有界自有signal/close，最终对外summary<=32768bytes，IPC固定封闭有界。不得等阻塞worker fsync/close返回或无限wait；超时未reap/未证明group/登记间隙记录unknown，不声称local/remote全部终止。任何登记group目标只能来自私有自有worker，不得来自磁盘/用户。
- all8source bytes先hashcheck再加载guest/mappers。candidate/release必须绑定单次window协议/细化设计/旧P来源及新8source；不绑定可变plan字节，plan仅固定路径登记、最终seal绑定。verify默认固定只读，无任意selector，不调用carrier或新增nativeentry。

---

### Task 1: Single live window, bounded supervision and fixture regression

**Files:** Create W八份source、candidate.json/release.json/source-manifest.json、identity/build/fixture provenance固定data和task1证据。Read P源/fixtures/build/旧review及原seal，不能修改。复制readonlyfixture data或直接绑定原P输入均可；不重复复制32MB工具若只读引用足够。

**Interfaces:**
- `run_window(io) -> dict`内部fixture入口与私有Window(cursor)；核心step必须显式接收当前live授权，单独step调用无授权时在IO前拒绝。保留typed request/guest parser/raw保存/mapper public API。
- Native main只接受window并调用有界supervisor；supervisor worker通过固定内部函数执行run_window(NativeHost(...))，没有公开worker CLI。
- summary封闭schema `switchback.b3e.window.v1`、固定diagnostic invariants、status/reason、每step精简状态/receipt facts、actual_consumed、deadline/cancel/localcleanup事实；不把所有raw复制到父输出。
- W source/candidate/release由完成源码计算，host绑定W8source，artifact/build引用P固定事实；verify适配W基线1784+plan=1785、P118filemanifest、旧977/9/646和新nativeAST。

- [x] **Step 1: Preserve/setup and RED.** 复制3份冻结source并验证hash；复制2份旧测试仅artifact引用必要变更。创建新的host/test_host/verify候选后先写late failure回归，复用原HostIO primitive和原六例作为behavioral RED；新测试必须证明到达失败final receipt而非缺token提前拒绝。

```python
# Fixture-only; run_window drives the real fixed controller/step state machine.
io = LateReceiptIO(failed_mode='stage', failure='fsync_after_full_bytes')
result = run_window(io)
self.assertEqual(result['status'], 'rejected')
self.assertEqual(io.guest.unit_mutations, [])
self.assertNotIn('actual', io.reached_modes)
```

Run `/usr/bin/python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-window-b3e-1021 -p test_host.py -k Window -v` before window implementation; keep exact expected failure/codehash. Existing red six-case proof is read-only prior evidence, not new green.

- [x] **Step 2: Implement cursor/guard and supervision.** 实施W/approved-design.md全部边界。对oldpermode API提供明确no-owner拒绝，测试harness可持有私有liveWindow验证内部step，但native入口无法恢复owner。guard步骤先于任何Lima进程；latepersist回复不改判；所有必需close/cleanup失败关闭RAM。实现带启动gate的固定worker fork和privatebounded监督ports，父断管/cancel/deadline在所有后续native调用前fence，输出/IPC/workerexit不一致不回退磁盘。父只清理proved-owned目标，不无限reap/写证据。

- [x] **Step 3: Focused GREEN, full related host suite once, verifier.** 覆盖6latecase及before/partial/full/late/close/dirsync at每guard与必需persist点，0nextIO/launch；健康4step+validmarker reachedreadback；错序/owner/io/reentry/closed cursor和重启存在guard；父spawn前后/cancel/EOF/malformed/oversize/duplicate/exit mismatch/stalled fsync/timeouts/late ownedpid/unknowncleanup/输出错误。监督行为测试仅FakeSupervisor ports，禁止真fork/process/NativeSupervisor。AST证明native调用归属和旧CLI拒绝。保持guest22和mapper15不扩写镜像测试。Run `/usr/bin/python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-window-b3e-1021 -p test_host.py -v` once final;必要覆盖变更再focused。Run `/usr/bin/python3 -B .scratch/runtime-m1-deployment-diagnostic-window-b3e-1021/verify.py`仅固定offline验证。保存argv/env/source beforeafter/output/exit/count及全source/candidate hashes。

- [x] **Step 4: Independent review gate.** root生成完整source和原P比较diff+newdatafacts；fresh reviewer双轴审查且明确I1六例关闭/监督scope。C/I一批fix、相关tests/重绑定/re-review。W/task-1-report.md与ledger登记清楚，不commit。跨最终整套/保护/seal事项交Task2。

### Task 2: Final current source evidence, whole review and seal

**Files:** W/final-verification.json,source-final八源/manifest,newoperation-window.md,report.md,outcome.json,review packages/seal;新ledger `.superpowers/sdd/m1-diagnostic-single-window-1021-progress.md`及本plan进度。只能更新W新证据，P和旧docs/ledger不动。

**Interfaces:** Task1冻结8sources+candidate/release+相同guest/bootstrap/旧ELF/build。Final metadata要绑定exact当前bytes，原P 61及sixred只作历史。root独立Git/inputs保护门禁和最强whole review不以此前task批准代替。

- [x] **Step 1: Final complete current coverage without redundant reruns.** 复用Task1完整host套件回执（implementation before=after=current；若仅测试fixture修正，保存原test源并用完整diff/AST作用域证明未变方法及其依赖，重跑全部受影响测试类，以最终fixture回执组合覆盖，不称旧test字节为当前），guest22与mapper15已由root运行，使用W/final-guest.*、final-mapper.*保存的exact命令/输出；按其实际未变module byte/AST依赖复用，不称整体八源与最终哈希相同。最新fixedverify使用I2修复冻结后的回执，源码/相关元数据再变化或新疑点时才重跑。若host源码变化则仅按变化重跑覆盖集，不能把历史哈希回执冒充当前。最终按三模块方法数汇总完整覆盖，不伪称一次combined命令。前后source/candidate/ELF事实完全相等；newbaseline1784+registeredplan，P118全部原字节，977/9/646、HEAD/branch/index及windowabsent独立核验。复核不会执行fork/NativeHost/NativeSupervisor/guest/ELF，所有行为fixture IO。

- [x] **Step 2: Assemble reviewable final candidate.** 冻结8source、candidate/release与来源/测试/准入；operation-window只列唯一window native命令及内部4step、150秒监督、30/45/45/30、全窗口once/不可续/无独立readback/noStop/unknowntermination。明确native仍未授权，旧CLI为历史不再候选，三个gatesfalse/rootcauseunknown。

- [x] **Step 3: Whole review and final fixes.** 最强fresh reviewer以完整package/设计/I1原review+新测试源绑定核查整体source/evidence；C/I统一freshfixer、覆盖tests/re-review。root复核protectedfiles和unknown边界，source漂移则不seal。Final task review和whole review共用一次完整双轴review，但分别给出metadata与全code结论。

- [x] **Step 4: Seal and report.** 仅修改anchoredcheckbox行，保留headerinline示例；ledger/plan最后快照；outcome明确offline_complete，I1closed-by-newprotocol，nativefalse。seal绑定全部W files(不含自身)+最终plan/ledger snapshots，读回全部facts和目录集合；metadata不写自身hash循环。封存后不改bytes，不启动任何真实环境。报告结果/测试/评审/局限与下一次独立native授权边界。
