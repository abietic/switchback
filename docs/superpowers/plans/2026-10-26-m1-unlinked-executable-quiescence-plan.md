# M1 Unlinked Executable Quiescence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task-by-task. Steps use checkbox syntax for tracking. User already authorized implementation, validation, sealing, and one conditional real window; do not ask for another execution-style choice.

**Goal:** Deliver the approved v2 private operator, then execute one real preservation/Engine/conditional first-boot window only after every offline and review gate passes.

**Architecture:** Reuse kit20 and all project Source unchanged. Add a metadata-only O_PATH identity branch to a new copy of the private process guard, enforce protected object/path rejection in its full scan, then wire the same guard into observer preservation and installed-object preservation. One original LocalOperations deadline/claim owns the complete new operation, including observer preservation.

**Tech Stack:** Host Python3.9.6, Linux Python3, existing frozen Python dirfd/rename/process primitives, Lima2.2.1, unchanged kit20/bundle1. No dependency installation.

## Global Constraints

- Workspace: `/path/to/switchback`.
- Approved design: `docs/superpowers/specs/2026-10-26-m1-unlinked-executable-quiescence-design.md`, snapshot SHA256 `c5bed0b5fe478c93c9d95b6dc01c17f85224683afe5f865f8fad6a76f2ad93fc`.
- Current human authorization: `call_6e430e2f5d6e477eaf35ab43aef767d9`, choice “接受设计，验证通过后单次实跑（推荐）”. It covers offline implementation/reviews/seal then one real window in the design; no additional confirmation within that scope.
- Agent writes only `.scratch/runtime-m1-unlinked-exe-guard-1026/`. Root alone updates this plan and `.superpowers/sdd/progress.md`, seals, creates a live grant and runs actual.
- New fixed actual phase: `.scratch/runtime-m1-unlinked-exe-guard-execution-next`; it must remain absent during agents' offline work.
- No edits to old sealed phases, project Source, kit20/bundle1, templates, dependencies, resource values, network/capabilities or existing environment. No git add/commit/reset/checkout/clean/branch mutation; preserve dirty files, HEAD and index.
- No agent guest/VM/Engine/PG/provider calls, real `/proc` scans or credential reads. Do not read `.scratch/runtime-m1-worker-execution-credential-1026-01/` or historical private full-env metadata. Synthetic phase-owned fixtures only. Always use `-B`; no cache files in sealed inputs.
- Stable absolute deleted regular exe only; O_PATH metadata, `nlink=0`, valid nonzero dev/inode, `0 < size <= 512 MiB`. Nonabsolute links remain rejected. Normal and kernel-thread legacy behavior stays.
- Bind PID/start/flags/cgroup/raw exe and full object metadata before/after. Retain raw deleted marker; no PID/name whitelist, no opening stripped named path, no reading exe contents.
- Reject protected path/inode, writer FD and writable maps; preserve full PID/FD/maps, manager/job, boot, deadline and owner-frozen checks. Both preservation callers use the new guard.
- Kit manifest `eb77d938dab0909a00c95529c6eab2397fa47f4003b60cbeca61af96093dd38a`; bundle manifest `4029c440783319ce82ebdae5f7edc255cc04b3d94846139c5248cfef86fc3e6b`; SourceRoot `sha256:d115d2758925bd8198a4f83b7ba76159b7febcd9ea23aa6705f84d6d0a588541`; templates root `sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`.
- Policy id `unlinked_exe_object_identity_v1`; private binding schema `switchback.kit20.preparation_binding.v2`; private grant schema `switchback.kit20.exact_execution_grant.v2`. Both carry exact `guard_policy`; reject old v1 and all code/scope drift.
- Root actual only after full validation/reviews/seal: existing VM must already Running; one Engine120s then conditional qualification240s/barrier300s; one shared7200s outer preparation deadline. No VM start, partition/storage migration, system-service restart, old database access, Task or provider call. Fail/unknown stop, no retry/resume/postfailure guest check/remote Stop; partial effects retained.

## File Structure and Boundaries

The new phase is seeded with the original29 declared inputs plus artifact binding, byte-exact. `ROOT-baseline.json` freezes1093 project Source/template files and3482 immutable evidence/candidate records; `ROOT-baseline-status.z` preserves initial git status. The old input remains `.scratch/runtime-m1-worker-execution-profile-1025/` and is read-only forever.

Task1 owns `ownership_guard.py`, `test_unlinked_guard.py`, its report/raw test evidence. Task2 consumes those files unchanged and owns carrier integration, copied observer preserver, tests, public snapshot/contract inputs, verifier, binding and operation documentation. Root owns baseline/scope, review packages/decisions, final consistency/seal and actual grant/run.

### Task 1: Scoped identity for absolute unlinked executables

**Files:**
- Modify only new phase `ownership_guard.py`.
- Create new phase `test_unlinked_guard.py`, `task-1-report.md`, raw focused test receipts and `task-1-freeze.json`.
- Read original `test_preparation_operator.py`/`ProcFixture` and observed four rows in `.scratch/runtime-m1-process-identity-readonly-1026/guest-audit.raw.json`; do not mutate them.

**Interfaces:**
- Consumes existing `ProcInventory(root='/proc', monotonic=...)`, `process(pid,deadline)`, `scan(roots,nodes,deadline)` and `OwnershipGuard`.
- Produces same API and legacy normal-process dict. Deleted branch additionally returns `flags:int` and `exe_binding:dict` with keys `device,inode,mode,uid,gid,nlink,size,mtime_ns,ctime_ns`. Export `POLICY_ID='unlinked_exe_object_identity_v1'`.
- `scan` must reject a deleted binding's device/inode in `nodes` or original raw exe within `roots` before FD/maps enumeration, then still perform all existing enumeration/stability checks.

- [x] **Step 1: Reproduce and preserve RED.** Build a synthetic proc magic-link fixture around an actually unlinked, still-held public fixture inode. Inject only proc-exe open/stat/readlink seams; real fixture stat/cgroup/fd/maps files retain the original guard path. O_PATH may be emulated by duplicating the held descriptor on Darwin; actual execution must require Linux O_PATH. Spy reads to reject reading that exe descriptor. The four observed system-process rows supply raw link, PID/start/flags/cgroup and metadata expectations; they are not an authority whitelist. Run the stable-unlinked positive against the seeded guard and save exact failure `process executable unknown/deleted`.

Run:
```sh
/usr/bin/python3 -B .scratch/runtime-m1-unlinked-exe-guard-1026/test_unlinked_guard.py
```

- [x] **Step 2: Add a bounded metadata capture seam and the deleted-only branch.** This is the required algorithm; preserve equivalent behavior if factoring it into a small helper:

```python
POLICY_ID = 'unlinked_exe_object_identity_v1'

def executable_binding(s):
    return dict(device=s.st_dev, inode=s.st_ino, mode=s.st_mode,
                uid=s.st_uid, gid=s.st_gid, nlink=s.st_nlink,
                size=s.st_size, mtime_ns=s.st_mtime_ns, ctime_ns=s.st_ctime_ns)

# In process, keep initial stat parsing, kernel-thread branch and normal output.
# Replace the blanket deleted rejection with this branch, inside a try/finally
# whose fd remains held through the second stat/cgroup/raw-link observation.
require(exe.startswith('/'), 'process executable unknown')
if exe.endswith(' (deleted)'):
    require(hasattr(os, 'O_PATH'), 'O_PATH required for unlinked executable')
    fd = os.open(base/'exe', os.O_PATH | os.O_CLOEXEC)
    first = os.fstat(fd)
    require(stat.S_ISREG(first.st_mode) and first.st_nlink == 0 and
            first.st_dev > 0 and first.st_ino > 0 and
            0 < first.st_size <= 512 << 20, 'unlinked executable object')
    binding = executable_binding(first)
    require(exe == os.readlink(base/'exe') and
            binding == executable_binding(os.stat(base/'exe')),
            'unlinked executable target drift')
```

Re-read the process stat with the original bounded reader; parse the same PID/start/flags fields, require equality to the initial fields, re-read the same cgroup and raw exe link, and compare both named proc target and held fd to `binding`. Do not open the path obtained by removing deleted. Close the held fd in `finally` on every return/exception. For legacy nondeleted/kernel paths, keep the original return keys and semantics; only the new branch adds binding/flags.

- [x] **Step 3: Enforce the protected-object check inside scan.** Immediately after the existing `before=self.process(...)`, before descriptor enumeration:

```python
binding = before.get('exe_binding')
if binding is not None:
    require((binding['device'], binding['inode']) not in objects and
            not referenced(before['exe']),
            'protected unlinked executable is running')
```

The existing `before == self.process(pid,deadline)` then compares all new identity fields as well. Keep every FD/maps and PID-set check.

- [x] **Step 4: Run meaningful positive and rejection cases.** Cover stable unlinked metadata-only success; inode and path alias match; nlink!=0; zero/oversize/special target; missing O_PATH/permission/target; raw-link/PID/start/flags/cgroup/held-vs-named metadata drift; nonabsolute rejection; normal/kernel legacy behavior; deleted-process writer FD and writable map rejection; full PID-set churn; fd closure on each error; no read from the held exe descriptor. Use actual isolated files/FDs where supported, fake kernel magic-link metadata only where Darwin cannot provide it. Save full raw stdout/stderr and exit codes. Run original focused ownership/proc classes on this guard as compatibility evidence, without selecting environment adapters.

- [x] **Step 5: Freeze and report.** Write `task-1-report.md` with RED/GREEN commands, exact counts, failures and corrected fixture attempts, changed-file hashes and remaining Task2-only integration requirements. Freeze only final guard/test/report/raw inputs; do not mark carrier or live readiness complete. No commits. Root generates full old/new diff and dispatches a fresh task reviewer.

### Task 2: Complete v2 carrier and both preservation call sites

**Files:**
- Modify copied new phase `preparation_runtime.py`, `copy_switch.py`, `guest_copy.py`, `copy_common.py`, `worker_delivery.py`, `launch_copy.py`, `operator_offline_check.py`, authorization template, installed contract/layout and copied tests/docs as needed for exact v2 rebinding.
- Rebind `installed_preserve.py`'s literal only where the new contract's namespace/archive paths require it; the installed object identities, content and preservation primitives remain.
- Create new phase `guest_observer_preserve.py`, `test_observer_preserve.py`, `test_v2_composition.py`, `observer-snapshot.json`, `expected-observer-snapshot.json`, `task-2-report.md` and full verifier/test/freeze evidence.
- Read original observer prototype `.scratch/runtime-m1-existing-observer-preservation-1026/guest_preserve.py` and its six filesystem tests; readonly snapshot `.scratch/runtime-m1-existing-observer-readonly-1026/observer-snapshot.json`; original installed/copy inputs and old snapshots. No old phase writes.

**Interfaces:**
- Consumes approved Task1 guard unchanged, including `POLICY_ID` and typed `exe_binding`.
- Produces sole actual entry `launch_copy.py actual` in new fixed execution phase, complete v2 `artifact-binding.json`, inert v2 grant template and `operator_offline_check.py` offline `check()`.
- Root can populate the exact v2 grant from current conditional user authorization after task/final reviews and immutable seal. Agent must not create `authorization.json`, live claim or actual execution directory.

- [x] **Step 1: Isolate and rebind namespace.** Use this fixed map throughout generated inputs and direct literals, with byte-level search/self-check to catch inconsistent old live namespace references:

```python
POLICY_ID = 'unlinked_exe_object_identity_v1'
OFFLINE = ROOT / '.scratch/runtime-m1-unlinked-exe-guard-1026'
EXECUTION = ROOT / '.scratch/runtime-m1-unlinked-exe-guard-execution-next'
BINDING_SCHEMA = 'switchback.kit20.preparation_binding.v2'
GRANT_SCHEMA = 'switchback.kit20.exact_execution_grant.v2'
COPY_ARCHIVE = 'archive-unlinked-exe-guard-1026-kit-0020-bundle-0001'
INSTALLED_ARCHIVE = '.preserve-unlinked-exe-guard-1026-kit-0020'
OBSERVER_ARCHIVE = '.preserve-observer-unlinked-exe-guard-kit20-1026'
GUEST_AUDIT = '/var/lib/switchback/unlinked-exe-guard-1026-audit'
```

Kit/bundle source paths, their manifests and Source/Template roots remain exactly Global Constraints. Include `guard_policy` in binding/grant and strict authorization shape/value checks. Add observer preflight/preserve actions to the complete fixed ACTIONS list; reject v1, missing policy, wrong policy, old phase, old claims and any byte/scope drift.

- [x] **Step 2: Integrate observer preservation before the original absent-target guest preflight.** Reuse the existing observer preserver and six meaningful filesystem tests; inject the new guard module into its guest program. The policy-specific process-guard call is shared with the original installed-object path; no duplicated substitute predicate. Both old and new observer archive prefixes are protected roots.

The single actual sequence must be:

```python
stages = (
    'require_exact_authorization',    # Includes original single key metadata fd lease.
    'verify_local_inputs',
    'verify_host_and_running_vm',     # Hold original host owner lock; full host facts.
    'preserve_fixed_observer_objects',
    'verify_guest_frozen_ownership',   # Original full guest preflight, now targets absent.
    'preserve_old_objects_and_installation',
    'copy_and_verify_new_kit',
)
```

Split the existing `verify_running_vm_and_frozen_ownership` at its first guest preflight call: its existing host checks/owner lock go before observer preservation; its original guest proof goes after. Recheck held host identity/deadline and prior host facts across this interval. Keep original Engine and conditional boot calls unchanged after those stages. One LocalOperations 7200s deadline owns everything, one authorization/entry claim prevents bypass/replay; no nested extra full preparation budget or alternate actual entry.

The observer guest action has maximum180s clamped to that same deadline, uses fresh root/pinned parent locks, exact snapshot+ELF hash, complete process/FD/maps proof, then two renameat2(NOREPLACE) moves to the new fixed observer archive parents. It never opens configuration/key leaves. Preserve root inode/mode/nlink/size/mtime and exact children, allow only moved-root ctime and known parent entry-derived metadata to change. Failure after one move retains partial state; no rollback or automatic next action. Keep observer service masked/never loaded or restarted.

- [x] **Step 3: Preserve complete transport/binding/privacy.** Add a finite observer action/module set to `guest_program`, carrying both raw module hash and packed module hash. Try lossless LZMA extreme compression first. If needed, comment-only packing applies solely to private modules and requires `ast.dump(parse(raw),include_attributes=False)==ast.dump(parse(packed),include_attributes=False)` before use; original contract/prepare Source bytes stay raw. No new framing, stdin use, guest stage or protocol. Test all six action programs with maximum legal public fields; encoded argv strictly<65536. Preserve ordinary16KiB streams, only original two catalogue stdout256KiB exception, audit limits and deadlines. Keep sensitive credential stdin/raw suppression and cross-stage exact original manifest check intact.

- [x] **Step 4: Verify full operator and immutable Source.** Inherited original102 regression methods remain represented, plus Task1, observer6 and new combined ordering/auth/guard tests. Test failure/unknown at every new composition stage calls nothing later. Test original installed-object caller also invokes the v2 guard with no whitelist. Verify complete kit20/bundle/header/reverse/raw Source1093 rows, old object snapshots/seven archives/latest4858-leaf host stage/retained3372 stage, Source roots, publication flags and all v2 phase inputs. No Go build, image build, PG or live process/guest calls.

Run focused regressions while editing; one final complete suite and verifier after final edits:

```sh
/usr/bin/python3 -B -m unittest discover -s .scratch/runtime-m1-unlinked-exe-guard-1026 -p 'test_*.py' -v
/usr/bin/python3 -B .scratch/runtime-m1-unlinked-exe-guard-1026/operator_offline_check.py
```

Save full commands, minimal actual test env, raw stdout/stderr and exit codes in the new phase. Verify old seals and project files only against Root baseline; do not read unrelated historical full-env raw metadata or real credential files.

- [x] **Step 5: Deliver and freeze.** Final `operation-window.md` explicitly records conditional human scope, fixed paths, policy/schema, both guards, one7200s budget, original120/240/300s and all failure/secret/transport boundaries. Artifact binding/template authority flags remain false; actual grant/phase/claim absent. Write `task-2-report.md`, changed-code review package and final mutable handoff freeze with explicit small report/self-closure exclusions. Root resolves cross-task warnings and sends task/final reviews without rerunning unchanged suites.

## Root Final Gate and Already Authorized Actual

- [x] Review Task1, then Task2; Critical/Important findings require a fresh fixer with targeted tests and a fresh re-review. Do not dispatch simultaneous implementers on the shared phase.
- [x] Most-capable final reviewer reads the entire new operator scope and real permission/credential boundaries. Source and old evidence are unchanged; scope any whole review to this accepted slice, not unrelated historical dirty changes.
- [x] Verify1093 source rows,3482 immutable records, HEAD/index, final tests/verifier/raw/audit/review hashes; seal the complete offline phase and read-only rehash exact coverage. Record the current human conditional grant and final binding without modifying old sealed evidence.
- [x] Root alone creates the new fixed0700 execution phase, copies complete declared inputs byte-exact, and writes the exact v2 grant bound to final code/scope/hash/expiry/actions/policy and DeepSeek private file path. Do not expose key content/hash; no agent reads it. Run one `launch_copy.py actual` with minimal env, no additional permission question for this approved scope.
- [x] Save actual outcome and material partial effects. Success requires actual Engine receipt plus original first-boot installed result, never exit0 alone. On first failed/unknown, stop all new guest operations and preserve evidence. No Task or provider invocation even if first boot succeeds; subsequent paid Task/report/page acceptance requires its own scope.

Actual result: the authorized single window was executed and stopped after observer preservation. Guest manager preflight failed on successful zero-byte jobs stdout parsed as JSON. Engine and conditional first boot were not reached; real acceptance remains unmet. No retry, continuation, postfailure guest read or remote Stop. Saved-byte replay and a separate unapproved next design do not revive this grant.
