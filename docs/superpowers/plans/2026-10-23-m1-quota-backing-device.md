# M1 quota backing-device compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 严格表示固定 quota backing-device，完成源码、回归、全新完整 kit/bundle、独立评审和离线后续操作清单。

**Architecture:** 在现行 scanner/validator 深模块边界内新增独立 inventory v2，保留 v1拒绝语义。metadata-only O_PATH证明精确设备身份，并将两生产扫描与同一窗口filesystem绑定；不借此改变任何准入或能力。

**Tech Stack:** Python3.9+、Linux metadata syscalls、现有 Go1.26.4交叉构建和纯离线binding。

## Global Constraints

- 固定 WT `/path/to/switchback`；phase `.scratch/runtime-m1-quota-backing-device-1023`；HEAD/index/所有既有dirty paths受保护，不stage、不提交、不切branch。
- 用户授权 `call_b5ecdf245c5a4e5d8d5494ab667c5243`；接受原文和细化DTO见 `docs/superpowers/specs/2026-10-23-m1-quota-backing-device-design.md`，均是本计划约束。
- 不进入 guest、不运行 Lima/Engine/API/PG/provider，不生成秘密/journal，不执行编译候选或nativefixture，不改真实stage/副本/存储/分区，不重试旧实际窗口。
- 固定路径只为 `DATA_ROOT/overlay2/backingFsBlockDev`；Linux block0600/root:root/nlink1/size0，st_dev、st_rdev、rootfd和现行XFS major/minor必须一致；只使用 `O_PATH|O_NOFOLLOW|O_CLOEXEC` 获取元数据，不设备payload I/O/mknod/unlink/chmod/chown/quotactl。
- v1 schema/rule/语义保留；新schema `switchback.engine.store_inventory.v2`、rule `m1-engine-store-v2`，固定device条目结构和backing_filesystem严格按spec；未知字段、版本混合、其他special和不明来源继续拒绝。
- two production scans都传同窗口filesystem；validate_preflight将v2 backing_filesystem与自身filesystem完整绑定。stat audit和稳定identity包含st_rdev，before/fd/pathname/scan末尾都复验，保留原bounds。
- 不改变120/240秒、300秒barrier、capabilities、PG/Start/Trust或三个生产门禁。
- `deploy/experience/switchback-capacity-publisher.service` 与 `internal/workerruntime/adapter/capacitypublisher/publish_test.go` 不动。旧candidates、seals、raw及本次真实失败phase逐字节保留。
- 完整报告落文件，回复简短。报告保留命令、exit、日志hash、RED/GREEN、自评和疑虑。无diff/证据不宣称完成。

---

### Task 1: Strict inventory v2 and real scanner integration

**Files:**
- Modify: `deploy/experience/engine/store.py`, `contract.py`, `guest.py`, `README.md`。
- Test/Modify: `deploy/experience/engine/test_store.py`, `test_contract.py`, `test_guest.py`。
- Optional test-only discovery: `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`，仅增加新phase bundle查找。
- Evidence: phase `task-1/`、`task-1-report.md`；baseline为phase `before/`。

**Interfaces:**
- Consumes: contract现有 `_filesystem(facts)`、`validate_store_inventory(facts)`、`validate_preflight(facts)`、固定DATA_ROOT和完整目录/常规payload扫描。
- Produces: `_scan_store(..., *, expected_filesystem=None)`；v1兼容结果或完整v2结果；stat/identity含st_rdev；真实caller两处明确绑定；root源码freeze供Task2使用。

- [x] **Step 1: Build the failing real-path regression and preserve RED.** 三节点原始metadata在 `.scratch/runtime-m1-host-recorder-execution-1023/{guest-frames-decoded.jsonl,replay_store_failure.py,store-failure-replay.json}`。只读它们，将fake目录/设备seam纳入真实scanner测试，补显式合成rdev成功fixture；不改真实失败packet。

```python
# 原真实失败保持“缺少设备身份”，合成正例与它分开。
assert captured_leaf['mode'] == 0o60600
assert 'rdev' not in captured_leaf
# 改代码前：测试中的完整固定设备正例应在真实_scan_store上RED。
inventory = _scan_store(root=fake_root, expected_filesystem=filesystem,
                        check=check, observe=record.raw)
assert inventory['schema'] == 'switchback.engine.store_inventory.v2'
```

从repo根用CLT Python3.9/-B/`PYTHONDONTWRITEBYTECODE=1`执行聚焦 unittest；stdout/stderr/exit独占写task-1。仅一次最终全Engine套件；调试期间跑相关新测试。

- [x] **Step 2: Implement v2 at the existing seam.** 按spec严格DTO处理独立v2，保留v1。Linux设备编码必须无平台依赖；例如校验raw设备号可用完整Linux64编码，输入各为非bool的uint32：

```python
def linux_device_number(major, minor):
    if type(major) is not int or type(minor) is not int or not (0 <= major < 1 << 32 and 0 <= minor < 1 << 32):
        raise ValueError('invalid Linux device identity')
    return ((major & 0xfff) << 8) | (minor & 0xff) | ((minor & ~0xff) << 12) | ((major & ~0xfff) << 32)
```

不要使用当前Darwin的os.major/os.makedev解释Linuxpacket。元数据fd打开前后路径在同一固定父dirfd下；O_PATH不可用即拒绝，不能fallback读取设备。

```python
# 两处生产调用的绑定关系必须是这个关系；复用原window/check/observe。
store = _scan_store(check=io_ops.check, observe=io_ops.record.raw,
                    expected_filesystem=filesystem)
# claim前已有_boundary重新验证filesystem，随后进行完整第二次扫描。
current = _scan_store(check=io_ops.check, observe=record.raw,
                      expected_filesystem=self.before['filesystem'])
_require(current == self.before['store'], 'store changed before exclusive claim')
```

- [x] **Step 3: Complete GREEN/rejection and caller evidence.** 必测固定合法节点；错误路径、char/FIFO/socket/symlink、错uid/gid/mode/nlink/size、缺/错误rdev、节点st_dev/rootfd/mount不一致、替换竞态、额外special、完整coverage、preflight与store合法但不同filesystem、v1原样与拒绝block、混版本/未知字段。断言设备只以O_PATH取得fd，没有read/payload open；普通文件仍完整读取校验。macOS测试使用fake stat/syscall seams，不创建真实设备。

```sh
env PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
env PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s test/acceptance/experienceprepare -p 'test_qualification_window.py' -v
env PYTHONDONTWRITEBYTECODE=1 python3 -B deploy/experience/engine/prepare.py self-check
```

命令在固定WT运行；使用实际CLT Python绝对路径记录工具身份。若调整Go discovery，跑原相关包的聚焦binding（Task2新kit前的expected SKIP保持事实），不启动PG或环境。

- [x] **Step 4: Freeze and review.** 只对baseline前后允许文件生成上下文完整diff，报告RED/GREEN和所有变更/限制；保存源码SHA及phase证据清单。Task1 spec+quality独立review通过后才能Task2。没有commits，HEAD保持fbebfe7；Review包装用scoped uncommitted baseline，不把整棵旧dirty tree当本任务diff。

### Task 2: Complete new kit/bundle and concrete next-operation specification

**Files:**
- Read-only source: Task1 frozen files、原builder `deploy/experience/qualification/build_candidate.py`。
- Create: 原builder下一个独占kit目录（预期 `task-4/kit-0011`，以真实输出为准），本phase `task-2/bundle-NNNN`、audits、reports、`operation-checklist.md`。
- No production source edits；必要修复回Task1 gate，不能直接patch候选。

**Interfaces:**
- Consumes: Task1 source freeze、完整当前1076source+7template输入、原builder和canonical bundle验证、已通过Task1review。
- Produces: 新完整kit/bundle、真实manifest/SourcesRoot/TemplatesRoot/receipt identities、实际新kit的纯Go binding PASS无SKIP、具体未来操作清单、完整delivery freeze供root与whole reviewer。

- [x] **Step 1: Build exactly once through the original direct entry.** 先记录工具身份/current source root并确认保护文件、旧seal、HEAD/index一致。直接运行原文件，不import-main/wrapper、不复制改写builder、不覆盖原默认输出或用旧kit补文件：

```sh
env PYTHONDONTWRITEBYTECODE=1 python3 -B deploy/experience/qualification/build_candidate.py build
```

原始43内部构建回执全部保留，交叉构建只生成文件不执行。builder失败保留原尝试并报告具体原因，root处理后再决定；不得静默删除或复用失败输出。

- [x] **Step 2: Canonical bundle plus independent complete audit.** 以新kit创建本phase新bundle，通过可信current源码的canonical `verify_bundle(bundle, kit)`。核对1151covered+manifest（若本scope新增文件则按真实count解释差异）、1076source+7templates、20格式（含compat32）、43exit0、17反向输入、13null及4false；重算所有leaf/path/type/mode/bytes/hash，SourceRoot/TemplatesRoot与current/copied/build before-after一致。

```python
from bundle import build_bundle, verify_bundle
manifest = build_bundle(new_kit, new_bundle)
assert manifest == verify_bundle(new_bundle, new_kit)
assert manifest['runtime_qualification'] is False
```

采集实际值，不能预填新SHA。按原纯Go测试使用真实新kit和新bundle，在唯一最终binding前验证发现目标正确；最终要求PASS/noSKIP/runtime reads zero。未生成runtime identities，不执行编译程序。

- [x] **Step 3: Deliver the complete future operation checklist.** 复用已接受完整Engine/资格effects重新绑定新manifest；本次仅编写，不inventory真实目标。明确四个现行kit10/bundle1副本按旧manifest保留与新副本独占复制；本次host stage在 `.scratch/runtime-m1-host-recorder-execution-1023/host-stage-preservation.json` 有244leaf完整身份，未来先核对exact grant/claim/boot/binding、现行coverage/owner/hash/inode，再以新的固定archive目标和no-replace rename完整保留，不删除、续用或续跑。guest stage未来必须证明空闲；失败/unknown停止，不retry/resume/remoteStop；Engine120秒和资格240秒/300秒barrier原样。不得假定当前VM或node合法、不得把缺rdevpacket复用为成功，也不增加本轮真实效果。

- [x] **Step 4: Freeze and review.** 交付full report、audits、命令收据和完整freeze；报告必须区分合成离线成功与真实失败/未观测部分。独立task review通过后交root，fresh whole-slice review通过后才发布completion和seal。更新current进度doc但不改历史sealed报告；本轮不请求/执行新的真实窗口直到具体新完整候选可审阅。

## Root integration gate

- [x] Task1 scoped review closed；Task2 scoped review closed；fresh whole-slice review closed。
- [x] 完整baseline/prefix/source/protected/oldcandidate/seal/HEAD/index核对，通过后更新statusdoc与ledger。
- [x] root独立核对actual manifest、source/template coverage、format、receipt及final实际binding；completion+phase seal最后独占发布。

**Plan self-review:** 所有已接受要求对应Task1或Task2；无架构冲突。输出目录以原builder真实单调结果为准，测试fixture不能创建native设备，no Git mutation覆盖skill通用commit步骤。任务间自动继续，不再次问执行方式。
