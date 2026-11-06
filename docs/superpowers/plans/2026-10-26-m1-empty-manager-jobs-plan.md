# M1 Empty Manager Jobs and Remaining Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task-by-task. User has already chosen A and authorized offline validation/review/seal followed by one new real window. Do not ask another execution-style or same-scope authorization question. Steps use checkbox syntax.

**Goal:** Correct the captured successful empty jobs response, verify the already-preserved observer without moving it, and deliver then execute the approved new single remaining preparation/Engine/conditional-first-boot window.

**Architecture:** Reuse kit20, project Source and the reviewed private operator. Task1 adds the exact empty-byte manager case plus a read-only archived-observer verifier that reuses the existing bounded observer primitives. Task2 rebinds the complete operator and six finite guest actions to a new namespace. Root alone seals and runs the already authorized actual once.

**Tech Stack:** Python3.9.6 host, Linux Python3, existing pinned source/dirfd/O_PATH/rename/process primitives and Lima2.2.1; no new dependency or kit rebuild.

## Global Constraints

- Workspace: `/path/to/switchback`.
- Approved design: `docs/superpowers/specs/2026-10-26-m1-empty-manager-jobs-design.md`, SHA256 `2bec3947fcf21ba4daa11dee4267ab3fd5e10fa0698675ae3f4dd13758884377`.
- Current human authorization: `call_7c6f072dda7745df98d79b7902f30ec0`, choice “接受A，验证通过后单次实跑（推荐）”. It covers offline implementation/review/seal then one new remaining real window; Root does not need another same-scope confirmation.
- New offline phase: `.scratch/runtime-m1-empty-manager-jobs-1026`; new actual phase: `.scratch/runtime-m1-empty-manager-jobs-execution-next`. Actual phase/grant/claim stay absent during agents' work.
- Agent writes only the new offline phase. Root owns `ROOT-*`, this plan, `.superpowers/sdd/progress.md`, final seal and actual. No git add/commit/reset/checkout/clean/branch mutation; protect dirty files, HEAD and index.
- No project Source, kit20/bundle1, old phase/seal, template, dependency, resource, network or capability edits. Root baseline freezes1093 Source/template records and3816 immutable evidence/candidate records.
- No agent VM/guest/Engine/PG/provider calls, real `/proc` scans or credential reads. Do not read `.scratch/runtime-m1-worker-execution-credential-1026-01/` or historical private full-env metadata. Synthetic phase-owned fixtures only, always `-B`.
- Normalize only `type(jobs_raw) is bytes and jobs_raw == b''` after the original command adapter's successful exit/no-error/no-timeout/no-truncation check. No strip, exception fallback or acceptance of unknown nonempty bytes. Original strict nonempty JSON, owner jobs and loaded-unit rules remain.
- Unlinked executable policy id remains `unlinked_exe_object_identity_v1`; O_PATH, nine-field object identity, PID/start/flags/cgroup/raw exe, protected roots/inodes, full FD/maps/PID-set/boot and owner-frozen checks remain.
- Observer has already been moved. This operation only reads/verifies its fixed archives and original-path absence; it must not create directories, rename, rewrite configuration/key leaves, read their contents, or mutate services.
- Existing observer archive prefix remains `.preserve-observer-unlinked-exe-guard-kit20-1026` at `/opt/switchback` and `/etc/switchback`. Exact archive parent identities from the existing receipt are `opt=[64769,297213,0,0,448]`, `etc=[64769,309131,0,0,448]`. This strengthens the approved design's minimum check; the earlier missing-inode statement was a display-filter mistake, recorded in `ROOT-archive-identity-fact-correction.json`.
- New copy archive: `archive-empty-manager-jobs-1026-kit-0020-bundle-0001`; installed prefix: `.preserve-empty-manager-jobs-1026-kit-0020`; guest audit: `/var/lib/switchback/empty-manager-jobs-1026-audit`.
- Kit manifest `eb77d938dab0909a00c95529c6eab2397fa47f4003b60cbeca61af96093dd38a`; bundle manifest `4029c440783319ce82ebdae5f7edc255cc04b3d94846139c5248cfef86fc3e6b`; SourceRoot `sha256:d115d2758925bd8198a4f83b7ba76159b7febcd9ea23aa6705f84d6d0a588541`; TemplatesRoot `sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`.
- Keep schemas `switchback.kit20.preparation_binding.v2` / `switchback.kit20.exact_execution_grant.v2`, exact guard_policy, new paths/finite actions/byte binding and new human intent. Reject old grants/claims/phase or any drift. Offline template/binding authority remains false.
- Root actual only after final suite/verifier/reviews/seal: VM already Running; one7200s outer deadline, Engine120s then conditional qualification240s/barrier300s. Original Native CAP_SYS_ADMIN+CAP_SYS_PTRACE, newPG/secret/credential preparation only after Engine success. No VMstart/partition/storage migration/system-service maintenance or restart/oldDB/Task/provider. Fail or unknown stops; no retry/resume/postfailure guest check/remote Stop. Partial effects stay.

## Structure and task ownership

The phase is seeded byte-exact from the previous37 declared inputs plus binding; Root rebinds its approved design and approval records. Task1 owns `ownership_guard.py`, a shared bounded guest emitter in `guest_observer_preserve.py`, new `guest_observer_check.py`, `test_manager_jobs.py`, `test_observer_check.py`, its report/raw receipts/freeze. It reads Root's captured manager commands and preserved-state snapshot without changing them. Task2 owns carrier integration, operative data/test rebinding and full delivery. Root-owned helpers are local-only controls and are reviewed in the final whole-slice gate.

### Task 1: Exact empty jobs and read-only archived-observer verification

**Files:**
- Modify new phase `ownership_guard.py` and `guest_observer_preserve.py`.
- Create `guest_observer_check.py`, `test_manager_jobs.py`, `test_observer_check.py`, `task-1-report.md`, new raw receipts and `task-1-freeze.json`.
- Read `captured-manager-commands.json`, `observer-preserved-snapshot.json`, old151 tests and Root baseline/scope. Do not edit inherited tests or Root data in this task.

**Interfaces:**
- Existing `OwnershipGuard.manager(deadline)` still returns `{'listed':list,'jobs':list,'loaded_details':dict}`.
- Add `guest_observer_preserve.run_checked_guest(expected, actor, success_status, receipt_schema)`; existing `run_guest(expected)` delegates with `Preserver` and its original status/schema.
- New `guest_observer_check.ArchiveChecker` subclasses the existing `Preserver` for read/verify/guard primitives and exposes `run() -> dict`; new `run_guest(expected)` uses the shared bounded emitter and returns schema `switchback.observer_archive_check.receipt.v1`, status `observer_archive_verified`.
- Root input `observer-preserved-snapshot.json` contains all old snapshot fields plus `preserved.schema='switchback.observer_preserved_state.v1'`, `archive_parent_identities`, exact post-rename `binary_metadata`, exact `configuration_metadata`, and original receipt/audit hashes.

- [x] **Step 1: Capture the exact RED and fix only the observed parser boundary.** Replay the two saved wire commands through the real `manager` method with a dummy clock and no proc adapter. Validate original exit0/error=None/timeout=False/truncated=False/byte hashes before returning each saved stdout. Expect the current JSONDecodeError. Then apply:

```python
rows = json.loads(call(list(LIST_UNITS)))
jobs_raw = call(list(LIST_JOBS))
jobs = [] if type(jobs_raw) is bytes and jobs_raw == b'' else json.loads(jobs_raw)
```

Keep everything after these lines unchanged. Tests include exact raw empty bytes, original JSON arrays, owner/unrelated jobs, malformed/non-list/missing-unit, whitespace, bytearray/str empty, callback errors and before/after manager drift. Exercise the original command adapter rejection seam for nonzero/error/timeout/truncated empty responses.

- [x] **Step 2: Share the existing bounded observer emitter.** Move the existing `run_guest` body to `run_checked_guest`; its sole actor invocation is `actor(expected['snapshot'],command,record,deadline).run()`, its success value is `success_status`, and receipt schema is `receipt_schema`. All original count/8MiB audit/16KiB stdout/deadline/root and bounded command checks stay byte-equivalent in behavior. The existing wrapper is:

```python
def run_guest(expected):
    return run_checked_guest(expected, Preserver, 'observer_preserved',
                             'switchback.observer_preservation.receipt.v1')
```

No duplicated emitter or substitute process guard. The inherited rename operation stays available only to existing synthetic tests, not the new finite actual action.

- [x] **Step 3: Add the read-only archived checker using the saved post-rename state.** Implement this complete core, preserving equivalent factoring:

```python
import os
from pathlib import Path
from copy_common import missing
import guest_observer_preserve as base

class ArchiveChecker(base.Preserver):
    def __init__(self, snapshot, command, record, deadline, uid=0):
        super().__init__(snapshot, command, record, deadline, uid=uid)
        saved = snapshot['preserved']
        base.require(saved['schema'] == 'switchback.observer_preserved_state.v1',
                     'fixed preserved observer state')
        self.saved = saved
        self.binary_metadata = saved['binary_metadata']
        self.expected_config = saved['configuration_metadata']
        self.stage = 2

    def run(self):
        try:
            self.open('opt', str(Path(base.BIN).parent))
            self.open('bin', base.BIN)
            self.open('etc', str(Path(base.CONFIG).parent))
            base.require(self.ids['opt'] == self.snapshot['parent_identities']['opt'] and
                         self.ids['etc'] == self.snapshot['parent_identities']['etc'],
                         'fixed observer parent identity drift')
            self.open('opt_archive', str(Path(base.BIN).parent / base.PREFIX))
            self.open('etc_archive', str(Path(base.CONFIG).parent / base.PREFIX))
            base.require(self.ids['opt_archive'] == self.saved['archive_parent_identities']['opt'] and
                         self.ids['etc_archive'] == self.saved['archive_parent_identities']['etc'],
                         'preserved observer archive parent identity drift')
            missing(base.CONFIG)
            config = str(Path(self.paths['etc_archive']) / 'created-layer-observer')
            self.open('config', config)
            base.require(self.ids['config'] == self.expected_config[0]['metadata']['identity'],
                         'preserved observer configuration identity drift')
            old_bin = self.expected_bin[0]['metadata']
            current_bin = base.metadata(os.fstat(self.fds['bin']))
            base.require(not os.listdir(self.fds['bin']) and
                         base.equal_except(current_bin, old_bin,
                             ('mtime_ns','ctime_ns','nlink','bytes')),
                         'preserved observer original bin changed')
            self.expected_bin = [{'path':'','metadata':current_bin}]
            self.guard()
            return {'status':'observer_archive_verified', 'stage':2,
                    'boot':self.snapshot['boot'],
                    'binary_archive':self.paths['opt_archive']+'/'+base.NAME,
                    'binary_sha256':self.snapshot['binary']['sha256'],
                    'config_archive':self.paths['config'],
                    'configuration_metadata':self.expected_config,
                    'archive_parent_identities':self.saved['archive_parent_identities'],
                    'bin_empty':True,'config_content_reads':0,
                    'key_content_reads':0,'service_mutations':0,'file_mutations':0}
        finally:
            self.close()

def run_guest(expected):
    return base.run_checked_guest(expected, ArchiveChecker,
                                 'observer_archive_verified',
                                 'switchback.observer_archive_check.receipt.v1')
```

The inherited `verify` enforces archive exact children, post-rename ELF metadata/20-byte header/full SHA, exact config leaf metadata and original config absence. The inherited `guard` enforces full manager/boot/proc/FD/maps with the same ProcInventory and protected roots/inodes. It must not call inherited `run` or `rename`. All original/new archive root protections remain.

- [x] **Step 4: Meaningful filesystem and failure tests, then focused verification.** Create archived fixture state using the old public-header synthetic fixture before the check; derive matching saved post-state, close old handles, then run a fresh checker. During check, spy/deny mkdir/rename/unlink/chown/chmod/utime/fsync and write-open; deny open of `key`/`deployment.env`. Assert success, zero mutation and exact identities. Reject original paths reappearing, missing/wrong archive or child inode/metadata/header/SHA, extra children/symlink, parent/boot/manager/proc drift before later operations. Keep guard logic tests separate from filesystem-only seam; use synthetic proc or injected bounded fake, never real `/proc`.

Run once after final edits:

```sh
/usr/bin/python3 -B -m unittest -v test_manager_jobs test_observer_check test_unlinked_guard test_observer_preserve test_preparation_operator.GuardTests
```

Working directory is the new phase. Save command/minimal env/raw stdout/stderr/exit/time, including any confstr warning. Preserve RED and corrected fixture attempts; do not rerun unchanged tests without a named reason.

- [x] **Step 5: Report and freeze Task1 only.** `task-1-report.md` lists actual commands/counts/code hashes and Task2 integration/live limits. `task-1-freeze.json` includes final Task1 files/report/raw and exact readonly fixture inputs. No carrier/full-verifier or live readiness claim. Root creates a filesystem review diff and dispatches fresh spec/quality review.

### Task 2: Complete fresh namespace, finite action and remaining-window carrier

**Files:**
- Modify new phase `preparation_runtime.py`, `copy_switch.py`, `guest_copy.py`, `copy_common.py`, `worker_delivery.py`, `launch_copy.py`, `operator_offline_check.py`, operative JSON/docs and inherited tests for exact rebinding.
- Rebind only installed archive literals in `installed_preserve.py`/contract; original primitives and old object identities stay.
- Create `test_remaining_window.py`, `task-2-report.md`, full raw validation and `task-2-freeze.json`.
- Consume Task1 files unchanged after Root approval. Root separately owns the actual-launch/consistency helpers and reviews them before real selection.

**Interfaces:**
- New sole actual entry stays `launch_copy.py actual` in the new actual phase.
- Six actions: `('observer-check','preflight','switch','boot','credential','boot-images')`. Replace the obsolete preserve action with the new read-only action; no seventh or legacy actual action.
- Shared finite ACTIONS starts `fixed-preserved-observer-check`; remove the previous observer-preserve actions, retain the original complete remaining operation scope.
- `LocalOperations.verify_preserved_observer_objects(binding)` is the new stage; sets the existing internal proof boolean only after status/schema/error and exact saved archive result are verified.

- [x] **Step 1: Rebind exact paths and grants.** Use the Global Constraints' fixed namespace, unchanged schemas/policy and inert authority. Authorization id prefix is `human-kit20-empty-manager-jobs-`; old id/phase/grant/claim rejected. Update new `artifact-binding.json` to cover all executable inputs, captured manager input and preserved snapshot, Root design/Task1 approval and Source1093. Keep the selected DeepSeek profile and exact cross-stage manifest checks.

- [x] **Step 2: Replace the first guest action without changing transport.** In `guest_payload`, observer-check loads only the existing original `contract`/`prepare` bytes plus `copy_common`, `ownership_guard`, `guest_observer_preserve`, `guest_observer_check`; `program_from_payload` dispatches only the latter `run_guest` for observer-check. All other action module sets remain. Input is `{'snapshot':json_file(phase/'observer-preserved-snapshot.json'),'budget_seconds':min(180,remaining)}`. LZMA extreme/comment-only AST-equivalent packing/raw+packed hashes, expanded bound, argv bound, stdin/audit/stream behavior stay.

- [x] **Step 3: Use this complete production stage order.**

```python
for name in ('require_exact_authorization','verify_local_inputs',
             'verify_host_and_running_vm','verify_preserved_observer_objects',
             'verify_guest_frozen_ownership','preserve_old_objects_and_installation',
             'copy_and_verify_new_kit'):
    ops.check_deadline()
    value = getattr(ops,name)(binding)
    require(value is True, name+': failed/unknown terminal stop')
```

The new method retains the old held-host/full-host-facts checks around its one guest call, requires `observer_archive_verified` plus schema/error/exact paths/hash/metadata/zero mutation counters, then sets the existing internal observer proof flag. It does no observer rename. The original Engine call and conditional boot follow unchanged with their exact ready/installed results. One LocalOperations/claim/deadline; no second actual launcher or extra full preparation budget.

- [x] **Step 4: Integrate tests, full verifier and input lineage.** Retain all original151 method names/coverage and add callback/output/readonly action/strict response/failure ordering tests. Update old preserved test namespaces only where new operative paths require it; filesystem preservation tests remain offline fixtures. Verify both shared ProcInventory call paths. The verifier checks actual captured manager bytes, post-state derivation from the two sealed receipt files, both exact archive parent identities, all six maximum-size programs and all old kit/bundle/header/reverse/source/publication checks. Historical3816 full rehash remains Root duty, reported accurately as pending rather than reading forbidden full-env data.

Run one final full suite and verifier after final edits/binding:

```sh
/usr/bin/python3 -B -m unittest discover -s .scratch/runtime-m1-empty-manager-jobs-1026 -p 'test_*.py' -v
/usr/bin/python3 -B .scratch/runtime-m1-empty-manager-jobs-1026/operator_offline_check.py
```

Record full raw streams, actual minimal env, exit/time and six program sizes. No Go/image rebuild, credential read or live adapter.

- [x] **Step 5: Complete delivery.** `operation-window.md` binds the new human intent, scopes, fixed paths and exact shared budget. It records the earlier observer effects and the new read-only stage, all original Native/PG/secret/key-transfer order and failstop. Report the code/binding hashes and remaining live proof, freeze finite Task2-owned inputs/report/raw with only explicit self-closure exclusions. Actual phase/grant stay absent; Root dispatches task and final whole-slice reviews.

## Root closing gate and already authorized actual

- [x] Check Task1 then Task2 with fresh spec/quality reviewers; fresh fixer and re-review for Critical/Important. Record Minor warnings for final triage.
- [x] Most-capable final whole-slice review includes Task1, full carrier and Root actual creator/launcher. Scope is this accepted slice, not the historical dirty repo.
- [x] Root rehashes Source1093, immutable3816, HEAD/index, all raw/freeze/lineage/code/review bindings and complete offline coverage; writes ready record, seals, then performs readonly exact coverage/hash recheck. Template/binding remain inert.
- [x] Root creates a new exact v2 grant/clone only after all gates, using the prepared private DeepSeek file path. No agent key access. Run one reviewed actual entry in a minimal environment; no additional in-scope question.
- [x] Save real Engine and qualification outcomes, exact partial effects and zero-provider status. Fail/unknown ends all guest activity. Read only saved local receipts afterward; no retry/resume/postfailure guest check/remote Stop. Success requires both actual `engine_ready` and `installed`, not exit0 alone.

Actual closure: preparation and four-copy verification succeeded. The sole Engine window failed at effective_configuration after four-file installation and daemon-reload; expected WatchdogUSec=0, observed infinity. No start step was recorded. Native/PG/credential content/qualification/provider were not reached; qualification outcome does not exist. This is completed execution/evidence handling, not Engine or product acceptance. See `.scratch/runtime-m1-empty-manager-jobs-execution-next/ROOT-execution-summary.json`; old grant is consumed and all guest activity ended. Local captured replay and next unapproved Source design are separate artifacts.
