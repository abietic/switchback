# M1 Engine Watchdog Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct the inactive/active watchdog contract, preserve its real regression, and deliver a complete rebuilt offline candidate plus an evidence-based preservation checklist.

**Architecture:** Keep the fixed systemctl adapter and both existing lifecycle seams. Fresh inactive units require `infinity`; active ready daemons require `0` before any Engine connection. Build with the unchanged qualification builder and bundle library, then verify the complete candidate and describe existing installed objects from saved receipts without claiming fresh guest state.

**Tech Stack:** Python3.9 host, synthetic Linux fixtures, unittest, existing pinned Go1.26.4 offline builder, both Linux architectures and Darwin arm64 artifacts.

## Global Constraints

- Accepted design: `docs/superpowers/specs/2026-10-26-m1-engine-watchdog-lifecycle-design.md`, SHA256 `991a318ac0716eb0e4b1316791e05b06c6e0c8d4151795e377bc456a654facd9`; human intent `call_0b38ab4b76984a75a49d5056c4a8774a` authorizes full offline delivery only.
- Workdir `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`; index SHA256 `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5`. No git mutation/commit/cleanup; preserve dirty state.
- New private phase `.scratch/runtime-m1-engine-watchdog-lifecycle-1026` (called PHASE below). All earlier phases, seals, kit20/bundle1 and 4053 immutable baseline records stay unchanged.
- Only product Source edits are `deploy/experience/engine/guest.py` and `deploy/experience/engine/test_guest.py`. No template, Go, schema, PG/Trust/quota/Start, Native authority, storage or resource changes.
- No guest/Lima/VM/Docker/Engine/service/PG/provider calls, network downloads, key reads, current live stage reads/writes or install. Only explicitly minimal-env offline builder subprocesses and synthetic tests are allowed.
- Never read `.scratch/runtime-m1-worker-execution-credential-*`, any real private key/config, ambient credential environment, or historical `.scratch/runtime-m1-containerd-dump-imports-1025` raw metadata. Root supplied public saved inputs are sufficient.
- No actual phase, live grant or runnable new real launcher. Runtime/credential/provider authority stays false; 13 unresolved kit runtime fields stay null. A later exact real-window proposal is a separate deliverable/approval.
- Root baseline has 1093 Source/template records and 4053 immutable records. Source count is expected unchanged: extend existing test file, not bundle's closed Engine source-leaf set.
- Preserve ordinary 16KiB output / two exact catalogue 256KiB exceptions, program `<65536` bytes, original 120/240/300-second budgets and 7200-second outer contract. No increase of limits or new acceptance policy for loaded/active/foreign/unknown objects.
- Minimal child environment is established by exec/env replacement before importing the builder or recording env; preserve complete actual argv/cwd/env, exit, raw stdout/stderr lengths/hashes and elapsed. Do not copy old full-env receipts.
- Fresh serial implementers and independent task/whole reviews; no unresolved Critical/Important before handoff/seal. Reports and freezes are finite and explicit. Output warnings remain in raw evidence.

## Task 1: Lifecycle Source and real-adapter regression

**Files:**
- Modify `deploy/experience/engine/guest.py` (`_GuestOperations._effective`, `_IO.daemon`).
- Modify `deploy/experience/engine/test_guest.py` (focused lifecycle class and fixtures).
- Create PHASE `task-1-report.md`, `task-1-freeze.json`, focused RED/GREEN/full-suite raw receipts.
- Read-only inputs: PHASE `captured-effective-command.json`, `captured-effective-command.frame.raw`, `captured-effective.stdout.raw`, `source-before/guest.py`, `source-before/test_guest.py`, `ROOT-approved-design.md`, `ROOT-baseline.json`.

**Interfaces:**
- Consumes actual `_IO.show(unit, properties) -> dict` and unchanged `_properties`; exact saved successful command stdout is 753 bytes, SHA256 `0aa3f160c01c1400f6716212b3d21139a6ad4b0c60644a2e5aee2de2efbe5424`.
- Produces unchanged `_GuestOperations._effective(io_ops) -> configuration` and `_IO.daemon(index, boot, binary_hash) -> existing daemon DTO`. Only acceptance of the specified property at its lifecycle point changes.
- Keep one actual captured containerd wire fixture embedded in the existing test module with its provenance/hash; fixed derived Engine fixture is explicitly synthetic. Tests remain independent of `.scratch` at normal execution time.

- [x] **Step 1: Add meaningful RED at the actual parser/call seam.** Feed exact captured stdout through `_IO.show` via bounded command stub; map fixed four configuration reads, then invoke `_effective`. Derive second unit fields from its actual fixed unit config; label those values synthetic. The core success assertion is:

```python
record = RawRecord()
io_ops = _IO(time.monotonic() + 30, record)
io_ops.read = lambda target, maximum, mode: (by_target[target], None)
io_ops.command = lambda argv: wires[argv[-1]]
ops = _GuestOperations({}, {}, 0, 120, files, {})
self.assertEqual(ops._effective(io_ops), files)
```

Validate `sha256(CAPTURED_PRESTART_WIRE)` against the exact saved hash. Add active readiness tests through actual `_IO.daemon`, using synthetic `/proc`, ELF bytes and FD identities; existing Kernel can supply listener tables, but its `daemon` stub cannot substitute for the new active check.

- [x] **Step 2: Capture RED before production edits.** Run the focused class(es) using `/usr/bin/python3 -B -m unittest` from `deploy/experience/engine`, with `PYTHONDONTWRITEBYTECODE=1`, recording argv/env/raw exit/elapsed. Expected: captured inactive success fails on current lifecycle policy, and unsafe active watchdog accepted by old daemon check is caught by an assertRaises test. Existing broader tests need not rerun during each edit.

- [x] **Step 3: Apply only these production changes.** In `_effective` keep the entire service dictionary except:

```python
'RuntimeMaxUSec': 'infinity', 'WatchdogUSec': 'infinity', 'DropInPaths': '',
```

In `_IO.daemon`, keep all process/cgroup/ELF and identity checks, changing its fixed query and exact dictionary to:

```python
value = self.show(unit, ('Id', 'ActiveState', 'SubState', 'MainPID', 'Result', 'WatchdogUSec'))
_require(value == {'Id': unit, 'ActiveState': 'active', 'SubState': 'running',
    'MainPID': value.get('MainPID'), 'Result': 'success', 'WatchdogUSec': '0'}
    and value['MainPID'].isdigit(), 'daemon not notify-ready/running')
```

No generic duration normalizer, `0|infinity` whitelist, new action or schema. Existing non-Watchdog invalid values keep rejecting.

- [x] **Step 4: Cover lifecycle and compatibility boundaries.** Use compact table-driven tests for inactive `0`/finite/missing/empty/whitespace, active infinity/finite/missing/empty/whitespace, and wrong type at direct validator seam. For wire, invalid nontext values must remain invalid parser inputs rather than being quietly stringified into valid values. Cover both units, fragment/drop-in/ExecStart/dependency/state/PID mutations, duplicate scalar/ExecStart and failed/timeout/truncated/error command handling through actual bounded adapter where relevant. Exercise `_collect_listeners` and actual `_request` connection ordering with `_IO.daemon` bound to the actual method: invalid watchdog must stop before socket construction/API/network request. Do not rely on a fake daemon returning the expected DTO.

- [x] **Step 5: Final Task1 verification and handoff.** Focused tests GREEN; full existing Engine suite via `/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py' -q` from ROOT once after final code. Preserve raw evidence and public fixture provenance. Report actual method/subtest counts, files/hashes, RED/GREEN commands, known warnings and zero live calls. Freeze only final Task1-owned files/report/raw and readonly fixture hashes. Root makes a filesystem diff against `source-before`, dispatches fresh task review, and resolves findings before Task2.

## Task 2: Complete fresh candidate and saved-state preservation checklist

**Files:**
- Read-only current product Source and Task1-approved two-file freeze; no Source edits.
- Execute unchanged `deploy/experience/qualification/build_candidate.py` via controlled offline recorder; output its next fresh slot under `.scratch/runtime-m1-qualification-delivery-1022/task-4` (expected kit-0021; if the next slot differs, report before building).
- Create PHASE `build_offline.py`, `verify_delivery.py`, `test_delivery.py`, `bundle-0001/`, `delivery.json`, `saved-retention-inventory.json`, `next-operation-checklist.md`, `task-2-report.md`, `task-2-freeze.json`, and raw build/test/verify evidence.
- Read-only saved inputs: `.scratch/runtime-m1-empty-manager-jobs-execution-next/{ROOT-execution-summary.json,ROOT-local-engine-stage-inventory.json,engine-host-outcome.raw.json,four-copy-verification.json,switch-guest-summary.json,preparation-layout.json,preservation-inputs.json,installed-contract.json}` and prior protected archive metadata referenced there. No current `/Users/example/.switchback` or `/Volumes` access.

**Interfaces:**
- Consumes approved exact Source freeze, unchanged builder `build()/inputs()/verify_kit`, primary bundle `build_bundle(kit:Path,destination:Path)->dict` and `verify_bundle(bundle:Path,kit:Path)->dict`.
- Produces `delivery.json` with exact new kit/bundle paths/manifests/roots, complete file and binary counts, task1 Source hashes, raw receipt indices and all runtime/credential/provider/environment authority false. `saved-retention-inventory.json` contains saved provenance/identity/coverage and explicit missing facts, not fresh guest assertions.
- `verify_delivery.py` is a fixed offline verifier with no configurable remote/action target; imports only offline libraries. It returns nonzero on candidate/input/Source drift or invented runtime facts. No actual CLI/launcher/grant is created.

- [x] **Step 1: Record builder through an actually empty parent environment.** Launch `build_offline.py` using `/usr/bin/env -i` with exact PATH/HOME/LANG/PYTHONDONTWRITEBYTECODE and permitted macOS encoding metadata. Read only these current clean env keys after replacement. Its `subprocess.run` recording wrapper forwards original argv/cwd/env/timeout unchanged, saves raw stdout/stderr and full process metadata even on failure, and then returns the original CompletedProcess. Invoke original `build()` exactly once; preserve any failure instead of editing an existing kit. Fixed build env/toolchain policy remains builder-owned.

```python
original_run = subprocess.run
def recorded_run(argv, **kwargs):
    started = time.monotonic_ns()
    result = original_run(argv, **kwargs)
    save_raw_process(argv, kwargs, result, started, time.monotonic_ns())
    return result
subprocess.run = recorded_run
try:
    builder.build()
finally:
    subprocess.run = original_run
```

`save_raw_process` uses fresh exclusive names, actual bytes/len/hash/exit and full effective child env; secrets never enter the clean parent. Verify all 43 actual Go/version/module/build/build-info calls and 20 new artifacts. Do not substitute old binaries/receipts or fabricate a fresh build.

- [x] **Step 2: Build and validate fresh downstream bundle.** Call `build_bundle(new_kit, PHASE/'bundle-0001')` once, then `verify_bundle`. No template/code override or loose source leaf policy. Validate exact Source/before/after/copied bytes/modes/digests, both architecture input manifests, ELF/MachO class/machine/build-info, complete no-extra-leaf manifests, fixed runtime nulls and authority false. Compare new Source membership to Root baseline: exactly the approved two files may differ; all other 1091 Source/template records and all earlier kit/bundle/evidence records stay unchanged.

- [x] **Step 3: Build saved preservation inventory and real operation checklist.** Derive current four kit20/bundle1 copies, new Engine four installed files, claimed guest Engine stage, local 5002-leaf host stage and all old observer/copy/installed archives from saved artifacts, with provenance SHA and coverage. Do not copy key data, derive unknown inode identities, assume old live kit17, assume new Engine paths absent, or equate once-observed inactive with current quiescence. Mark missing guest stage/per-unit live identity clearly. The checklist starts with newly authorized read-only complete ownership/manager/jobs/boot/proc/FD/maps/path checks, describes no-replace preservation and fresh candidate copy, and holds writes if any proof is absent or current guards reject. No new loaded-unit acceptance, daemon maintenance/Stop or retry is implicitly permitted. It must distinguish candidate delivery readiness from execution readiness and name every outstanding decision.

- [x] **Step 4: Offline verifier and meaningful delivery regressions.** Bind fixed delivery metadata to candidate manifests and Task1 hashes. Verify primary bundle library, raw build receipts, exact previous-state inputs, Source delta, inert authority and complete inventories. Test candidate drift, manifest substitution, omitted state or made-up proven status, unsafe/extra leaves and authority/runtime identities rejected. Filesystem fixtures may copy small public manifests; no expensive complete kit rebuild in tests. For finite program budget claims, evaluate actual unchanged encoders against final Source/current max public inputs; if old private carrier is not rebound to the new candidate, clearly report that limit rather than claiming it is deployment-ready. Never run a stale bound carrier or give it an actual grant.

- [x] **Step 5: Final delivery validation and freeze.** Run focused delivery tests then verifier on final artifacts. Run relevant qualification Python tests/self-checks and the concrete Go final-kit binding test; record actual selected paths. The existing Go test has a closed list of old bundle patterns, so use a phase-private `go test -overlay` fixture that changes only the two kit/bundle selection string literals to this exact new pair. Keep all assertions and the current-source file read unchanged, save the exact overlay diff and original/replacement hashes, label this as the original validation logic with an isolated fixture selector. This does not edit any repository Go file or bypass assertions. Require one PASS/no SKIP plus logged exact new paths; do not silently select kit20. The combined final verification is owned once here/Root after all code changes; independent reviewers consume evidence and do not rerun whole suites. Freeze final Task2 inputs/code/report/raw plus full new candidate manifests, report exact counts/digests and any remaining operational proof gaps. Independent task review then broad most-capable accepted-slice review follows.

## Root verification and completion

- [x] Reconcile task freezes, Source two-file delta, old4053/HEAD/index, exact current candidate and raw receipt hashes.
- [x] Whole accepted-slice review with a complete filesystem diff; resolve all Critical/Important and triage Minors.
- [x] Run final relevant checks once if Task2 did not already cover the final Source state; seal exact finite new phase/candidate with explicit self-exclusions and readonly coverage recheck.
- [x] Deliver concise outcome and clickable artifacts. Report offline completion separately from real Engine/Native/PG/Core/Task acceptance; no environment grant exists.

## Plan self-review

Accepted spec coverage maps lifecycle behavior and real parser/callsite regression to Task1; complete fresh candidate, strict binding and saved-state preservation to Task2; independent review/Root integrity/seal to completion. No conflicting command or authority. The existing dirty worktree overrides skill commit boilerplate. New saved-state uncertainty is a documented execution prerequisite, never permission to widen the guard. No new product interface or schema is introduced.
