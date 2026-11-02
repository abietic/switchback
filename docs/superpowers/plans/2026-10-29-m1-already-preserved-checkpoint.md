# M1 Already-Preserved Checkpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the approved A offline candidate: fresh certification of seven already-preserved guest objects and three host objects, no repeated preservation mutations, real host-caller timing coverage, and 100-second work plus 20-second local cleanup inside the existing120-second maintenance window.

**Architecture:** Pure layout compilation binds original content obligations to the completed captured moves and current fixed host root metadata. A read-only host certifier collects the existing complete facts; new-mode orchestration uses one fresh T5 certification because it performs no preservation mutation. Work completion and owned-child cleanup have separate deadlines; the original Engine/qualification chain remains unchanged behind one live continuation.

**Tech Stack:** Existing Python3.9 private deployment carrier, unchanged Go/Source and kit25/bundle0002, captured JSON/journals, unittest, original read-only filesystem collectors.

**Workspace:** `/path/to/switchback`.
**Phase P:** `.scratch/runtime-m1-all-preserved-checkpoint-1029`.
**Frozen seed:** P/before/carrier50, P/before/helpers14, captured three inputs and immutable old271/failed133 sealed parents.
**Approved spec:** `docs/superpowers/specs/2026-10-29-m1-already-preserved-checkpoint-design.md`, SHA256 `6e43c606142436e29f293ae3a7c50708e96cf7c7aa4b6708c2a05e5475aab2c2`, accepted by `call_02b248ae1f464d2491d6f83dae5ca540`.

## Global Constraints

- 新增独立版本 `quiet_all_preserved_v1`，旧v1/v2、active/owned_pair语义保留。
- guest七个非空source均须缺失；对应固定archive目标须存在，API仍双侧缺失。维护不Stop、不mkdir、不rename。
- 宿主三source须缺失，固定已有archive恰含两副本和stage；stage须逐项对9,735文件旧snapshot重新校验。
- 完整使用原1344-node/1187-file/196652178-byte guest内容义务，不用历史推断代替fresh完整证明。
- 动作100秒，本地收尾20秒，总预算仍为120秒，受原7200秒driver硬上限约束；不足完整120秒时不创建维护子进程。
- 原Engine120、资格240、barrier300、driver7200、权限、容量/PG/Trust/Start、quota、Worker资源、Source/Go/templates和kit25/bundle0002保持。
- T5保留原manifest、历史archive、stage、两份完整metadata/content采集、writer/FD/ancestor等检查；因新模式不修改namespace，不执行旧版保存前→移动→保存后链。
- 完整验收正例不得将host_inputs/snapshot/verify_pair/verify_prior_archive/complete_tree_metadata/strict validator替换成直接通过。外部OS、文件与时钟端口可隔离，真实算法必须执行。
- 完整端口隔离流程调用实际宿主检查路径、guest端口使用记录规模；100秒内完成且至少10秒实测余量才可offline-timing-ready。分别披露真实host wall-clock、guest replay wall-clock、fake clock；不能冒充真实Linux全链时延保证。
- 普通固定宿主目录只读测量允许；不进入guest、不启停服务、不移动真实对象、不读provider/observer key、不调用模型、不授予新capability、不生成真实grant。
- 原Source strict socket-FD liveness RED保留、披露，不修改该Source合同或声称消除了风险。
- 新future固定为 `/path/to/switchback/.scratch/runtime-m1-all-preserved-checkpoint-execution-1029-01/carrier`，本片保持该目录及父目录不存在。
- 不暂存/提交/修改Git index或旧tracked116项，不修改旧sealed phases、Source1095/templates7或已接受纯FD helper。
- 不读/stat/枚举 `.scratch/runtime-m1-worker-execution-credential-1026-01`、`.scratch/runtime-m1-containerd-dump-imports-1025`，不解引用公开metadata中的key路径。
- 使用当前phase独立progress ledger；每项Spec+Quality review通过后再推进下一项，最后最强可用模型整片评审。源码权限不扩大，所写原始收据与执行代码冻结后不得覆写。

---

### Task 1: Pure all-preserved policy and complete read-only host certifier

**Files:** Create `P/carrier/all_preserved_layout.py`, `P/carrier/preserved_host.py`, `P/test_all_preserved_layout.py`, `P/test_preserved_host.py`, `P/all_preserved_fixtures.py`, `P/task-1/` raw/report/freeze. Modify `P/carrier/partial_layout.py` only if needed to extract version-neutral complete-content validation; its existing public validators and semantics remain.

**Interfaces:**
- `all_preserved_layout.MODE == 'quiet_all_preserved_v1'`, `ADOPTED` contains the seven nonempty fixed SOURCES, `PENDING == ()`.
- `compile_layout(basis, observation, summary, history, completed)` returns a closed versioned layout; history is the old two-move checked layout history, completed is the current failed journal-analysis four-move/eight-parent checked history. Expose `validate_layout`, `validate_history_fresh`, `validate_fresh`, `physical`, `tree_roots` with the existing partial-layout callable shapes for Task2.
- `preserved_host.compile_host_policy(layout, saved, captures, host_positions)` returns a closed fixed-path policy, not authority. Inputs are the immutable seed layout/saved state, captured original complete host-tree metadata/snapshots and this phase's current host root metadata reference.
- `preserved_host.verify_preserved_host(ops, policy)` returns complete facts with stable comparison shape; it may collect/persist evidence and remember the first complete baseline on ops, but cannot mutate host namespace, call guest/Engine or set execution/preservation authority flags. Task2 alone promotes proof after live claim gates.
- Ops interface is existing `phase`, `layout`, `now()`, `check_deadline()`, `save(name,value)`, `snapshot(path,record)` plus pinned host/archive handles. All I/O remains fixed to server-owned constants; no JSON/env/CLI adapter selector.

- [ ] Step 1: Read the approved spec, P/global-constraints.md and P/ROOT-input-map.md. Read only the named private seed functions `partial_layout`, `driver.host_inputs`, `copy_common.verify_pair/verify_prior_archive/complete_tree_metadata`, `copy_switch.snapshot` and exact captured inputs referenced by ROOT-input-map. Confirm parent hashes and forbid key-path dereference.
- [ ] Step 2: Add failing unittest cases first. The smallest policy checks are executable as:

```python
import unittest
import all_preserved_layout as policy
from preserve_tree import SOURCES

class PolicyShape(unittest.TestCase):
    def test_fixed_closed_recovery(self):
        self.assertEqual(policy.MODE, 'quiet_all_preserved_v1')
        self.assertEqual(policy.ADOPTED, frozenset(s for s in SOURCES if s != SOURCES[5]))
        self.assertEqual(policy.PENDING, ())
```

Run in P: `python3 -B -m unittest test_all_preserved_layout test_preserved_host -v`. Record RED stdout/stderr+exit before implementation.
- [ ] Step 3: Compile guest policy from exact captured content and move records. Match all original node chunks; require old two plus new four successful rename identities, select the last exact post-mutation identity/child hash for each affected parent, preserve var history, require API absent and all five archive identities. No guessed ns field. Add a version-neutral internal validator only where it avoids literal duplication, leaving old public old-mode validation exact. New pure mapping includes:

```python
MODE = 'quiet_all_preserved_v1'
ADOPTED = frozenset(s for s in SOURCES if s != SOURCES[5])
PENDING = ()

def physical(source):
    return target(source) if source in ADOPTED else source
```

Add negative cases: reordered/missing successful moves, wrong original hashes/nodes, wrong post-inode/ns/children, source reappears, target absent, API present, unexpected archive child, mount/alias, cross-version layout.
- [ ] Step 4: Compile host policy without I/O. Bind the exact eight host metadata positions: three original paths absent, archive identity and exact three children, original two manifests/identity and9735-stage snapshot. Update only the two moved copy-root ctime fields using the captured current root metadata; all descendants retain the captured original fullmetadata obligation. Stage's known root ns fields come from the current metadata; unknown historical leaf ns fields are not invented. Validate original per-leaf identity/size/hash and capture same-window stable full metadata during fresh verification.
- [ ] Step 5: Implement certifier by fixed-path reads: verify pair at existing archive, all nine prior archives, all archive-stage snapshots/retained stage/current archived stage, original fullmetadata two samples, root and ancestor pinning. No mkdir/rename/fsync of realhost data, no flag such as old `host_copy_renames_complete=True`. Record full first proof and compare later fresh proofs, normalize only fields explicitly bound in compiled policy. Retain unknown/error and original primary exceptions.
- [ ] Step 6: Real algorithm unit tests cover wrong archive/source occupancy, wrong stage count/hash/inode/ns, unexpected child, writable alias, symlink/special/hardlink, metadata drift between samples, failure without mutation, zero authority promotion and legacy semantics. Small fixture OS ports are allowed; no real host mutation. Run the focused tests and covering old `test_partial_layout` once after modifications. Save exact command/outputs, source ownership/context hashes and report to P/task-1-report.md; freeze current owned/context lists. Stop writing before task review.
- [ ] Step 7: Root builds full-context diff against before (new files as additions), dispatches independent Spec+Quality reviewer. Resolve all findings in one fix wave with covering checks and re-review. Record accepted proof and Task1 gate before Task2.

### Task 2: New-mode integration and 100/20-second supervision

**Files:** Modify private `P/carrier/driver.py`, `copy_switch.py`, `maintenance.py`, `maintenance_entry.py`, `maintenance_transport.py`, `maintenance_wire.py`, `owner_policy.py`, `owner_native.py`, `preserve_tree.py`, `preparation_runtime.py`, `launch_copy.py` only as integration requires. Create `P/test_all_preserved_integration.py`, `P/test_maintenance_deadlines.py`, P/task-2 raw/report/freeze. Do not edit Task1-owned accepted modules or Root controls without root coordination.

**Interfaces:** Consume accepted Task1 modules/policies. Existing run_driver/LocalOperations stage names and consume-once token remain. New mode accepts zero-mutation adoption proof; `host_inputs` delegates to the real certifier, `preserve_host_checkpoint(deadline)` returns `complete=True` only after fresh complete evidence under this live claim. Introduce private literal `WORK_SECONDS=100`, `CLEANUP_SECONDS=20`, `TOTAL_MAINTENANCE_SECONDS=120`; do not change original Engine Source constants.

- [ ] Step 1: Read Task1 accepted interface/report and new brief. Add RED tests at real callers: initial two full samples before FD projection, stopped full checks before projection, full original union; seven adopted/zero pending; actual T5 certifier invocation and no write/rename/Stop. Direct tests of pure guards cannot stand in for integration.
- [ ] Step 2: Route only the exact new policy/version. Keep all legacy mode contracts, proof fields/raw hashes and helper identity. New guest preserver freshly validates all seven existing trees/parents/archive closure, performs zero mutation, and emits one host checkpoint. Driver pins existing archive instead of demanding it absent/creating it, builds full initial host baseline, and the live T5 obtains one fresh unchanged complete proof. Do not inherit an old mutable boolean or treat a policy dict as a continuation.
- [ ] Step 3: Separate deadlines in the actual supervisor:

```python
start = now()
work_deadline = start + 100
cleanup_deadline = start + 120
require(cleanup_deadline <= driver.deadline, 'insufficient complete maintenance window')
```

Pass only work_deadline to Frames, clock envelope, guest input/output, host checkpoint, selector and leader wait. Use unchanged Source clock mapper since remaining <=100 <=120. After fully observed outcome/EOF/exit0 record work completion before100. Pass cleanup_deadline only to original local owned-child cleanup; record cleanup completion separately before120. Frames finalization validates both timestamps and unchanged durable journal/claim/audit bindings rather than rejecting a valid99s work/103s cleanup because current time crossed work_deadline.
- [ ] Step 4: Failure always wins. At100 or invalid/late outcome no new remote action; local cleanup only owns the new-session child already proved by original Source. Preserve EPERM and subsequent singleton proof separately; never mark success on identity loss, unknown group, failed cleanup or expired total window. No new retry/readback/Stop or continuation after primary error. Prevent startup when parent remaining<120.
- [ ] Step 5: Add real caller time matrix for99/100/120, full T5 callback cost, late audit/outcome/EOF, work-end99 cleanup103 accepted, deadline120 rejection, only local cleanup during reserve, same primary+secondary finalization errors, no remote calls after failure, no duplicate ACK/EOF/token. Full initial/stopped protected path/inode/writer/maps/sampler/FD/manager/listener/store/mount negatives and v1/v2 remain. Run exact covering test files once after final changes; retain raw and freeze/report. No full candidate generation until Task2 review accepts.
- [ ] Step 6: Root generates complete private diff and independent task review; fix one complete findings wave, targeted checks/re-review. Mark Task2 accepted only after ⚠️ items resolved.

### Task 3: Complete candidate, real host caller timing and independent delivery

**Files:** Modify `P/build_carrier.py`, `verify_carrier.py`, `task3-determinism.py`, `test_candidate_verification.py`, `test_carrier_integration.py`, only relevant evidence/transport tests and generated carrier/data. Create P/task-3 raw/report/freeze, `P/README.md`, `P/operation-window.md`, `P/ROOT-runtime-correspondence.json`. Root owns new context/integrity/seal controls and review packets; agree stable Root input roles before binding.

**Interfaces:** Accepted Task1/2 source. Guest module set includes all_preserved_layout (host certifier must stay host-only), original five Source guest modules unchanged, existing pureFD helper unchanged. Builder and independent verifier separately specify exact module/input roles, all old binding source members and old271/failed133 sealed closure, diagnostic7 frozen records, accepted gates and exact kit25/bundle0002 fulltree/modes. New disabled authorization has new policy, fixed absent future, nullhuman/id/time/worker, environment/provider=false, no_retry=true.

- [ ] Step 1: Fix complete role/source sets, compile exact all-preserved policies from bound originals, and regenerate seven programs/maintenance bundle under unchanged frame/event/argv/expanded/LZMA/budget caps. New code is not execution authority; old seed binding/grant is superseded but retained in before. Preserve all inherited input member records, not only code-role names.
- [ ] Step 2: Change fullchain fixture to exercise actual host caller and certifier/snapshot/manifest/metadata algorithms. Remove old `host_inputs=lambda...` bypass from acceptance path. External OS/filesystem/Engine/native/provider-transport/PG ports may be isolated; synthetic metadata/preimages are labeled. Assert guest0moves/0mkdir/0Stop, host0preservation mutations, seven+three accepted only after fresh proof, one ACK/EOF/reload/strict, four newcopy then unchanged canonical Engine→Native→credential/images/newPG→qualification.
- [ ] Step 3: Add ordinary fixed-host READONLY runner for actual full T5 caller including all standalone stage obligations and writer/ancestor checks, with evidence writes only in newphase. It cannot create a live LocalOperations claim, mutate realhost namespace or construct CredentialSource. Record real per-stage wall-clock and run complete endpoint-isolated flow with the actual host caller. At least10s measured headroom inside100 required to declareoffline-timing-ready. Keep original collector16.7s receipt only as component baseline. If insufficient, collect bounded cost breakdown; no deadline changes or real window.
- [ ] Step 4: Preserve original Source strict actualdrift RED/stablecontrol and explicit scheduling of stable strict control in fake. This residual is not treated as fixed. Include raw proof/data provenance, actual versus synthetic parts and runtime byte-correspondence when tests-only rebind. Candidate not real-executed, and timing-ready not Linux latency guarantee.
- [ ] Step 5: Run current independent exact-tree verifier,13-output or actual-new-output-set zero-write deterministic rebuild, source/template/index/old-parent protection and required covering suites. Do not repeat unchanged full suites on every artifact-only closure. Save all raw stdout/stderr/exit, exact final binding, peak budgets/argv/expanded, artifact freeze/report. Correct tests-only rebind must have explicit runtime byte-correspondence before accepting older fullchain receipt.
- [ ] Step 6: Independent Task3 Spec+Quality review, fixes and re-review. Root resolves all spanning-task items, then dispatches most-capable available whole review of full task diffs and Root controls. One fix wave for complete findings, rerun only affected checks and exact binding gates. No live grant.
- [ ] Step 7: Root final approval binds allclean reviews, fixed candidate, current raw validation receipts and operation checklist. Final full phase seal create then readonlyverify; no writes afterward. User sees actual outcome, known strict limitation and concrete next real scope. Actual recovery requires separate human approval after this complete result, never old consumedgrant.

## Plan self-review and execution

All approved spec requirements map to Task1(policy/host facts), Task2(real callers/deadlines/failure), Task3(timing/data validity/candidate/reviews/seal). No fallback to600 or omitted full checks. Oldtracked/Source/oldsealed protected; no commits. Follow skill's recommended SDD execution without another execution-choice prompt: the user already approved full independent delivery. Task briefs carry the complete global constraints and exact interfaces; implementers use only their ownership and report files. Context/Root closing helpers may be prepared in parallel with each task but not modify accepted frozen inputs.
