# M1 FD Authority Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付quiet_partial_v2完整离线恢复候选，接纳无关FD正常变化并支持三项已保存、四项待移动。

**Architecture:** 先实现纯共享process视图，再把它接入两份完整样本均验证之后的initial/stopped比较点。第二任务同时完成基于封存回执的布局、版本化接线、程序/角色绑定和完整离线执行链。Root拥有上下文保护、审核及最终seal。

**Tech Stack:** Python3.9标准库、现有私有维护载体、原样kit25/bundle0002；不引入依赖或环境服务。

## Global Constraints

- Workdir固定 `/path/to/switchback`；新PHASE为其 `.scratch/runtime-m1-fd-authority-recovery-1029`。
- 用户call_61c22041df6e4608b852670d9436026d批准的proposal SHA `987384f0e3f30da760368e0d2ef06440e3bc764f23503fb1ba1747158ea879e9`、正式spec和PHASE/approved-design.md共同给出约束。
- 原离线parent `.scratch/runtime-m1-quiet-partial-recovery-1029` seal `a74413a79dc7b80ab85bd33771878d3882b004af4e89e7e7d30963b2738ab2cf`；失败parent `.scratch/runtime-m1-quiet-partial-recovery-execution-1029-01` seal `ab87df31b33c1df7b878f1570653bb924b7b982bd973060f5c5a9a2019d7b42b` 均不可写。
- Future `.scratch/runtime-m1-fd-authority-recovery-execution-1029-01/carrier`保持不存在。无guest/VM/Engine/PG/provider/credential/服务/Native实际操作，无Git/index或Source/Go/templates/kit/bundle修改，不重跑已消费窗口。
- 不读/stat/枚举 `.scratch/runtime-m1-worker-execution-credential-1026-01`、`.scratch/runtime-m1-containerd-dump-imports-1025`。public selection metadata只作为不解引用的离线输入。
- 保留Source1095/templates7、HEAD/index和116条tracked dirty状态。kit25 manifest `e82905915d9493f68d349fb4bfedb6abbdc8dfcb0262b312559d9834baa5b146`；bundle0002 `0784bafc2c672dbb164a7a371c94741c5f7ed83e623a9f6f61ddef763e78823b`。
- 限制固定：maintenance120、Engine120、qualification240、barrier300、driver7200秒；frame16384/event1048576/expanded2097152/argv65536/LZMA2mem134217728 bytes；decoded128MiB、wire/journal各192MiB、ordinary64MiB、failure1MiB。file→checked-dir fsync、单append/fsync journal、独立failure与错误身份保护保留。
- 原始样本全量校验先于视图；raw proof/FD/maps/hash不变。保护对象使用两个sample的并集，采样器FD全部保留，其他已有稳定字段与负例保留。新语义只用于quiet_partial_v2。
- 本地临时夹具只在新phase内、无服务/网络。阶段已冻结文件不并发修改。Root保留所有before/review失败版本。保护dirty范围，故本片不stage/commit/push。

---

### Task 1: Shared pure FD authority view and captured-data contract

**Files:** Create PHASE/carrier/fd_authority.py、PHASE/test_fd_authority.py、PHASE/fixtures/fd-authority-pair.json、PHASE/task-1/原始回归证据。不得修改其他carrier成员；两处真实接线归Task2。

**Interfaces:** Consumes失败parent的sealed `analysis/audit-events.jsonl`、`analysis/process-projection-diff.json`及同journal完整性事实。Produces `authority_process_pair(before, after, *, sampler, roots, objects)` → `(before_view, after_view)`，保留process字段，只投影FD列表；返回深拷贝，无IO/authority。

- [ ] Step1: 生成固定真实fixture。流式读取sealed audit的sequence40023/42063两份complete-process-fd-inventory，保留128个process的原字段与FD，绑定source文件SHA、sequence、journalSHA；不得把control去差异样本当原始捕获。

```python
assert [row['sequence'] for row in rows] == [40023, 42063]
assert [len(row['event']['value']) for row in rows] == [128, 128]
assert rows[0]['event']['value'] != rows[1]['event']['value']
```

- [ ] Step2: 写并运行RED：实际pair在当前整份FD比较下不同；新API未实现前契约测试失败，保留raw输出。测试调用接口如下，不用硬编码PID名字为准入。

```python
left, right = authority_process_pair(before, after,
    sampler={'main_pid': 99, 'start_tick': 1},
    roots=['/protected'], objects={(7, 8)})
assert left == right
assert before == saved_before and after == saved_after
```

- [ ] Step3: 实现下述完整规则并防御相关输入的格式/重复/不完整问题，错误抛ValueError。不接受validated布尔开关，不解析JSON/env选择行为。调用者在完整校验之后提供保护集合。

```python
def authority_process_pair(before, after, *, sampler, roots, objects):
    import copy
    def need(ok, message):
        if not ok: raise ValueError(message)
    need(type(before) is list and type(after) is list, 'process lists required')
    need(type(sampler) is dict and set(sampler) == {'main_pid', 'start_tick'}, 'sampler framing')
    need(type(sampler['main_pid']) is int and sampler['main_pid'] > 0 and
         type(sampler['start_tick']) is int and sampler['start_tick'] >= 0, 'sampler identity')
    need(type(roots) in (list, tuple, set, frozenset) and roots and
         all(type(r) is str and r.startswith('/') and chr(0) not in r for r in roots) and
         len(set(roots)) == len(roots), 'complete roots')
    need(type(objects) in (list, tuple, set, frozenset) and objects, 'complete objects')
    for item in objects:
        need(type(item) in (tuple, list) and len(item) == 2 and
             all(type(v) is int and v > 0 for v in item), 'object identity')
    identities = {tuple(item) for item in objects}
    def project(rows):
        pids = set()
        for p in rows:
            need(type(p) is dict and type(p.get('main_pid')) is int and p['main_pid'] > 0 and
                 type(p.get('start_tick')) is int and p['start_tick'] >= 0 and
                 type(p.get('fds')) is list and p['main_pid'] not in pids, 'process framing')
            pids.add(p['main_pid'])
            descriptors = set()
            for fd in p['fds']:
                need(type(fd) is dict and set(fd) == {'fd','target','flags','device','inode','socket_inode'} and
                     type(fd.get('fd')) is int and fd['fd'] >= 0 and
                     fd['fd'] not in descriptors and type(fd.get('target')) is str and
                     bool(fd['target']) and chr(0) not in fd['target'] and
                     (fd['socket_inode'] is None or type(fd['socket_inode']) is int and fd['socket_inode'] > 0) and
                     all(type(fd.get(k)) is int and fd[k] >= 0 for k in ('flags', 'device', 'inode')),
                     'FD framing')
                descriptors.add(fd['fd'])
        result = copy.deepcopy(rows)
        for p in result:
            if (p['main_pid'], p['start_tick']) == (sampler['main_pid'], sampler['start_tick']):
                continue
            keep = []
            for fd in p['fds']:
                path = fd['target']
                related = (fd['device'], fd['inode']) in identities or any(
                    path == r or path.startswith(r.rstrip('/') + '/') or path == r + ' (deleted)'
                    for r in roots)
                if related:
                    keep.append(fd)
                else:
                    need(fd['device'] > 0 and fd['inode'] > 0, 'unclassifiable FD object')
            p['fds'] = keep
        return result
    return project(before), project(after)
```

此规则块固定接口与判定边界；实现可提炼验证小函数，但不得削弱格式检查或复制出另一套视图规则。

- [ ] Step4: GREEN测试覆盖真实波动、任意PID/名字同规则、相关路径、相同inode不同路径、socket/deleted alias、sampler全部FD保留、PID/start/exe/cgroup/进程闭包变化仍不同、非FD字段不变、格式/duplicate拒绝、输入未修改及构造无IO。不得将相关FD仅因只读而丢弃。
- [ ] Step5: 一次运行 `python3 -B -m unittest -v test_fd_authority`，保存命令、exit、raw输出及before/after实测。写task-1-report.md，精确owned/context冻结task-1/freeze.json。说明真实入口接线尚属Task2，不能宣称完整维护已修复。Root做Spec+Quality独立评审。

### Task 2: Two call sites, receipt-bound three-adopted layout and full carrier

**Files:** Modify PHASE/carrier/{owner_policy,maintenance,owner_native,partial_layout,preserve_tree,maintenance_entry,driver,copy_switch,preparation_runtime,launch_copy,maintenance_wire}.py及需要的现有私有入口；Create/Modify PHASE/{build_carrier,verify_carrier,capture_scale,task3-determinism}.py、完整集成/安全/兼容测试、README.md、operation-window.md、private-revision-map.json、生成程序和关闭grant。Task1 helper/fixture/测试/报告/freeze已接受后不可改；若发现接口缺陷，先向Root报告协调完整修复。

**Interfaces:** Consumes已接受Task1 API与Root-task-1-approval.json；旧sealed parent完整before49、固定candidate、captured basis/observation/summary和本次failed journal/raw effects。Produces完整quiet_partial_v2离线carrier、独立verifier、零写rebuild及task-2-report/freeze；未来真实入口仍关闭。

- [ ] Step1: 初始RED用本次真实pair触发两处真实入口原相等拒绝，保持所有非目标sample条件合法，保留旧调用点原始失败回放。不要只测helper或把整个OwnerSampler/NativeMaintenance替换为true。
- [ ] Step2: 先两次完整验证再投影。initial在两个 `_sample` 都成功之后、stopped在两轮现有负例都成功之后，以两轮完整protected/store/restore/current/archive对象并集调用helper。legacy v1和active owned_pair/containerd_only保持原比较语义。保持raw before/after及hash，视图只用于稳定性比较；日志不得将view digest冒充raw digest。

```python
# New v2 quiet path only, AFTER both sample validations:
before_process_view, after_process_view = authority_process_pair(
    before_processes, after_processes,
    sampler=sampler_identity, roots=complete_roots,
    objects=all_sample_objects[0] | all_sample_objects[1])
# Compare remaining whole-snapshot fields unchanged, using those process views.
# Retain original before/after evidence objects and compute their original hashes.
```

- [ ] Step3: 构造固定v2布局而非任意subset。旧var项由旧sealed layout绑定；etc/run两项由失败journal的typed-root-moved与post-rename identity、父目录回执绑定，stream重算全部frame/event hash后才采信。固定表如下：

```python
ADOPTED = frozenset(SOURCES[:3])
PENDING = (SOURCES[3], SOURCES[4], SOURCES[6], SOURCES[7])
ABSENT = SOURCES[5]
assert len(ADOPTED) == 3 and len(PENDING) == 4
assert len(SOURCES) == 8
```

布局保持1344 nodes/1187 files/196652178原始字节内容义务、5个既有archives、boot751079a6-74a4-4570-bca6-18e460c7f33d。三项都是fresh full验证后才adopted，不记为本窗口syscall；只有4个本窗口成功noReplace能解释对应parent/root ctime变化。更新全部physical rebasing、source/target/children closure、guard alias及_success的mode/count固定值。缺证据列字段，禁止推测。

- [ ] Step4: 完整接线NativeMaintenance、TypedPreserver、Inventory.bind/factory、initial proof、每次guard、host checkpoint/ACK/EOF/reload/strict和continuation。保留DurableEvidenceStore、完整journal、failure budget、同primary及no-postfailure-readback行为。维护0Stop/0mkdir；4 guest moves、host两copy+9735stage的3 moves、四新copy、原Engine120→Native/选定凭据stdin/images/newPG/资格240+barrier300顺序。
- [ ] Step5: 新固定phase/path/policy/action表，关闭grant。Builder/verifier独立列出完整16-module维护包与所有carrier/helper/Root/fixture/捕获角色，前置candidate身份与完整树mode/closure校验；保留全部parent source_files，不导入builder冒充独立验证。Source记录1095/templates7、kit25/bundle0002、before49和既有 Source bytes/AST、动态argv<65536等全部核对。raw失败112、原offline574及原始basis三件作为不可变输入；不follow私有key路径。
- [ ] Step6: 正例跑本次真实波动穿过两处真实保护入口；负例分别验证protected path/inode alias、writable FD/maps、sampler held flags/fstat/readlink/coverage、PID/start/exe/cgroup、进程闭包、manager/jobs/mask、store/mount/listener漂移都拒绝；两份保护对象并集漏第二份对象的攻击也必须拒绝。legacy回归仍拒绝原全FD漂移。
- [ ] Step7: 布局正例完成三adopted、四rename、5existing archives、0Stop/0guest mkdir与同driver live continuation；每个rename/checkpoint/reload/strict/EOF/deadline/journal故障验证精确副作用前缀和主异常。真实文件字节前像缺失时使用明确标记synthetic real-hash IO与原manifest义务分别验证，不冒充196MB原内容验收。
- [ ] Step8: 一次包含真实FD变动的完整fake T0—T8，实际driver/LocalOperations/supervise/Frames/package/Inventory/NativeMaintenance/TypedPreserver走完整链；只替换OS/命令/manager/外部Engine/Native/PG响应端口。记录decoded/wire/journal/ordinary/failure实际预算、events/frames、每次fsync、动态argv/package。有限覆盖suite通过后仅针对新改动重测，避免重复未变旧44442失败prefix或每个负例重读整个captured流。
- [ ] Step9: 在最后源码上运行 `python3 -B verify_carrier.py` 与 `python3 -B task3-determinism.py`，确认独立候选验核、全部模块/角色、关闭grant、13输出零写确定性重建；运行Root完整保护验证。写完整报告、raw证据、精确owned/context freeze。Root做Task2 Spec+Quality，所有finding一波修复后复审，再最高能力whole review。
- [ ] Step10: Root在当前最终版本核验全部冻结/旧输入/Source/HEAD/index，形成最终approval、验证汇总、操作清单；完整phase seal create→只读verify，封存后不再写本phase。仅此后才向用户展示下一次具体实跑清单，不自动取得执行权。
