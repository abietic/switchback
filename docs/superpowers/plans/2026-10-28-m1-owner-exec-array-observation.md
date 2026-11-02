# M1 Owner Exec Array Observation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task-by-task. Steps use checkbox syntax for tracking. The user already selected complete offline implementation; execute without another workflow-selection question.

**Goal:** Deliver a new disabled complete carrier that proves the two empty ExecStop arrays through fixed typed reads, while retaining the original50-field ownership and Stop contract.

**Architecture:** The manager sampler reads48 fields through its existing strict text query and always reads the two Stop arrays through fixed Properties.Get calls for already-loaded units. Only explicit variant/array/zero proofs become the existing empty string fields. Canonical owner code is reviewed first; a second task rebinds its exact mirror and the complete carrier into a new offline/execution identity.

**Tech Stack:** Python3.9 stdlib, unchanged Engine Python Source/kit23/bundle, a pinned upstream C formatter behind a purely synthetic message API for local fixture validation. No live system bus, VM or service.

## Global Constraints

- Accepted design: `docs/superpowers/specs/2026-10-28-m1-owner-exec-array-observation-design.md`; direct user answer to call_bd83dbc260134a6dbcbf194fe412dca6 is “接受 A，完成整片离线修复（推荐）”. Preserve accepted bytes and hash in new Root baseline.
- Work only in `.scratch/runtime-m1-owner-exec-array-1028` and this plan. No Git mutation, Source/template/Go/ELF/kit/bundle edits, old phase edits, or current failed execution edits.
- Offline only: no VM/guest/Engine/PG/systemd/provider/native bus/Stop/host-stage calls, credential reads/stat/scan or actual grants. Never access `.scratch/runtime-m1-worker-execution-credential-1026-01` or `.scratch/runtime-m1-containerd-dump-imports-1025`.
- Preserve old offline seal73bf1d6418581472f7cbd16fba36b15c008e653038df2b226a94352f17c09f6e and failed execution seal0c5b0171d1640db647678ed6b9c8ed13f2cf5311af5b58fbd7b9e3b3f075f2fb; all approved raw inputs remain byte/mode exact.
- New offline phase `.scratch/runtime-m1-owner-exec-array-1028`; future execution `.scratch/runtime-m1-owner-exec-array-execution-1028-01/carrier`, exclusive creation only. No reuse of execution-next, its old grant or claim.
- The other48 show fields remain complete, duplicate-free and unknown-free. Final manager DTO retains all50 fields and existing string values. OwnerObservation remains authority=none; all existing policy/Stop gates remain.
- ExecStop and ExecStopPost are always independently proven, in that order, for each already-listed loaded unit only. No show/Get for absent units; no GetUnit/LoadUnit/GetAll/Environment, arbitrary path/interface/property, bus selector, or remote operation.
- Fixed unit paths are `/org/freedesktop/systemd1/unit/switchback_2dm1_2dcontainerd_2eservice` and `/org/freedesktop/systemd1/unit/switchback_2dm1_2dengine_2eservice`.
- Fixed Get argv is `/usr/bin/busctl --system --no-pager --auto-start=no --allow-interactive-authorization=no --timeout=2s call org.freedesktop.systemd1 <fixed path> org.freedesktop.DBus.Properties Get ss org.freedesktop.systemd1.Service <fixed property>`.
- Accept only bytes `v a(sasbttttuii) 0` with zero or one final LF. No other whitespace, trailing frames, coercion, missing/unknown/nonempty values or fallback. Only both explicit proofs produce the existing two empty string fields.
- Keep original bounded command result validation. Single-command stdout/stderr≤16KiB, maintenance120, Engine120, qualification240, barrier300, total7200, frame16KiB/event1MiB/audit64MiB/decoder128MiB/expanded<2MiB, actual code argv<65536; no deadline extension.
- Fail/unknown ends the full driver, no next guest action/retry/readback/recovery. Original error and all held-close attempts retain the already-reviewed complete-carrier fixes.
- Root baseline pins HEAD fbebfe754045bdbf1af40871a5018eed33743011 and indexSHA2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5,1100 Source/templates and9769 older immutable records, plus both newer sealed phases. Canonical owner_policy/maintenance/preserve_tree and their mirrors stay unchanged.
- Fresh task implementers/reviewers sequentially; fresh fixer for complete C/I list, whole-slice final independent review. Most-capable Astra was unavailable due usage limit in this session; use the capable available independent fallback, recording it honestly. No commits.

## File map and preparation ownership

Root clones only the needed, sealed local inputs into the new phase before task dispatch: four canonical core files and their tests; complete carrier44 files; build/verify/determinism helpers; cloned regression fixtures; required Root input/provenance JSON and seed bytes. Clones are provenance-recorded, not fresh approvals. The copied grant stays disabled. Root creates an unchanged-input guard covering both old sealed phases and original baseline without scanning forbidden directories.

| Owner | Files within new phase |
| --- | --- |
| Task1 | `owner_native.py`, `test_owner.py`, `fixtures/`, `test_exec_formatter.py`, `task1-report.md` and exact test evidence |
| Task2 | `carrier/` mirror/identity rebinding, `build_carrier.py`, `verify_carrier.py`, `test_carrier.py`, `README.md`, `operation-window.md`, `task2-report.md` and generated proofs |
| Root | `ROOT-*`, accepted-design copy, plan/brief/review packages, ledger, final seal |

### Task 1: Strict typed reads and independent serialized fixtures

**Consumes:** New phase canonical OwnerSampler and exact raw1175B captured response at `fixtures/captured-containerd-show.raw.txt`; provenance is failed audit event92, SHAe69586b0d3201b42aa48b629461ecdf94f66cabc16248a04c825f770b8d97272. Root supplies pinned upstream busctl source (public official source only) under fixtures/upstream with URL/hash receipt. Current schema constants in owner_policy.py remain unchanged.

**Produces:** `_stop_property_argv(unit, property_name) -> list[str]`, `parse_empty_stop_array(raw: bytes) -> str`, and the same `OwnerSampler._manager(io) -> dict`. The helper returns only `""` or raises; it never grants authority or selects IO implementations.

- [x] Step1: Add fake-only regressions at the actual `_manager` command seam before changing canonical code. Construct a fixture IO that rejects any unrecorded command and records attempted calls. Feed the captured48-line response unchanged as a negative case, and explicitly labeled synthetic projected48 text plus independently generated typed responses as positive cases. For the new query selector, the text fixture contains exactly the48 requested fields. Never label synthetic typed data as current guest evidence.

```python
def test_only_explicit_typed_zero_becomes_empty(self):
    self.assertEqual(parse_empty_stop_array(b'v a(sasbttttuii) 0\n'), '')
    for bad in (b'', b'v a(sasbttttuii) 1', b'v as 0', b's "0"',
                b'v a(sasbttttuii) 0\n\n', b'v  a(sasbttttuii) 0'):
        with self.subTest(raw=bad), self.assertRaises(ValueError):
            parse_empty_stop_array(bad)
```

- [x] Step2: Run the red tests with literal Python3.9 -B; save command/cwd/exit/output bytes+hash. The red must identify the absent typed query/proof or original coverage failure, not fixture import/OS-port errors. Preserve raw red output.
- [x] Step3: Add the closed constants/helpers and wire the48+2 sampler path. Exact helper logic is:

```python
STOP_PROPERTIES = ('ExecStop', 'ExecStopPost')
STOP_OBJECTS = {
    'switchback-m1-containerd.service': '/org/freedesktop/systemd1/unit/switchback_2dm1_2dcontainerd_2eservice',
    'switchback-m1-engine.service': '/org/freedesktop/systemd1/unit/switchback_2dm1_2dengine_2eservice',
}
def _stop_property_argv(unit, property_name):
    require(unit in STOP_OBJECTS and property_name in STOP_PROPERTIES, 'fixed Stop property target')
    return ['/usr/bin/busctl', '--system', '--no-pager', '--auto-start=no',
            '--allow-interactive-authorization=no', '--timeout=2s', 'call',
            'org.freedesktop.systemd1', STOP_OBJECTS[unit],
            'org.freedesktop.DBus.Properties', 'Get', 'ss',
            'org.freedesktop.systemd1.Service', property_name]
def parse_empty_stop_array(raw):
    require(type(raw) is bytes and raw in
            (b'v a(sasbttttuii) 0', b'v a(sasbttttuii) 0\n'),
            'typed Stop array must be explicitly empty')
    return ''
```

The manager text selector is `[key for key in DYNAMIC+EFFECTIVE if key not in STOP_PROPERTIES]`; require its exact key set before any typed Get. Keep loaded-only outer loop and existing ExecStart normalization/set sorting. For each fixed property: `io.check()` → `io.command(_stop_property_argv(unit, key))` through the existing audited bounded command result path → bytes/cap validation → explicit parse → raw bytes/hash/type/count audit → assign existing DTO value. Typed output never enters ExecStart or other property normalization. A failed first Get cannot issue the second. All normal policy tests still consume a complete50-field dict.

- [x] Step4: Build local fixture evidence using the pinned upstream `format_cmdline` routine extracted byte-exactly from busctl-v255.c, compiled only into a temporary host test executable with synthetic message APIs. No original busctl main/acquire_bus, real bus, native service, network, credential or deployment ELF. Capture extraction hash and fixture compiler/run commands; include empty variant-array, nonempty array, wrong signature and string-zero controls. The fixture generates the serialized bytes rather than comparing a handwritten production literal to itself; unsupported synthetic APIs fail closed. Keep source license/provenance. Host fixture compile/run bounds≤60sec total and≤5sec each executable run.
- [x] Step5: Cover both units, pair/partial/all-unloaded, strict48 duplicate/missing/unknown fields, both mandatory properties, extra ExecStop text rejection, variant/signature/count/framing corruption, binary/DBus unavailable, nonzero/timeout/truncation/command-result errors, original absolute deadline and bounded output/audit errors, and no later command/Stop after failure. Use real `_Record` result validation where result flags matter. Retain the other core/Stop-policy negative tests.
- [x] Step6: Run focused `test_owner.py` and `test_exec_formatter.py` once on final code. Write `task1-report.md` with interfaces, exact changed files/test evidence, raw-vs-synthetic provenance and all limits. Do not refresh carrier mirror or rebuild yet. Independent scoped review/fix gate precedes Task2.

### Task 2: Complete disabled new carrier and integration

**Consumes:** Reviewed canonical owner_native hash and Task1 fixtures/tests. The old complete carrier is sealed at `.scratch/runtime-m1-owned-engine-transition-1027`; new phase is Root-cloned from its current final bytes, not the old pre-whole-fix snapshot. Original kit23/bundle/manifests unchanged.

**Produces:** New complete carrier with disabled null grant, current exact code/Source/module/length binding, complete regression evidence and future operation checklist. Existing API entry remains `launch_copy.py actual`, permanently rejected from the offline phase.

- [x] Step1: Copy approved canonical owner_native bytes to its carrier mirror. Update builder approval resolution to consume a new Root Task1 revision approval, while verifying unchanged owner_policy/maintenance/preserve_tree against old approvals. Old approval records are historical, not rewritten as new approvals. Keep all other canonical core/mirror bytes exact.
- [x] Step2: Rebind only old offline/execution identity strings in the two entry/runtime files and their current tests/docs:

```python
OFFLINE = ROOT/'.scratch/runtime-m1-owner-exec-array-1028/carrier'
EXECUTION = ROOT/'.scratch/runtime-m1-owner-exec-array-execution-1028-01/carrier'
```

`carrier/launch_copy.py` has its own fixed EXECUTION to update consistently; `preparation_runtime.py` defines OFFLINE/EXECUTION. SourceROOT and fixed host/guest deployment/archive targets remain unchanged because the failed T1 performed no Stop/move/deploy. Rebind provenance/core-revision metadata truthfully, preserving old input/source hashes as history. No semantic changes to driver, T1–T8 order, grants/claims/witnesses, strict unloaded gates or closed50-field owner policy.
- [x] Step3: Add integration regressions that decode the actual maintenance package and exercise its actual manager sampler on the captured text plus explicitly synthetic generated typed replies. Keep the real command/audit adapter path where possible, replacing only the bounded process/OS boundary; verify the raw typed-proof bytes survive the actual wire emitter/decoder rather than testing only a list-append audit fake. Recheck first Get failure leaves no second command/Stop, no show/Get for unloaded target, old phase/grant rejection and current offline entry rejection before adapters. Retain the complete raw-output, original-error and all-held-close regression fixes from the old final carrier.
- [x] Step4: Extend builder/verifier binding input sets to include approved design/Task1 evidence/official fixture/provenance and new core revision approval, while retaining all original input records and forbidden-path guards. Root extra sealed-record verification can remain separate from carrier Source/source_files if no runtime reader needs those raw failure records; every consumed diagnostic/fixture input must be bound. Update exact count assertions from derived binding roles, never weaken manifest/tree/coverage checks to make a count pass.
- [x] Step5: Build deterministically and independently verify all7 guest programs, original Source bytes, module/hash/order/dedup, LZMA2 EOF/memory/expanded bounds, every actual code argv<65536, kit23/bundle full tree and disabled grant. Run in-memory output recomputation with rewritten_files=0. Keep runtime call-time cap for dynamic fields and label synthetic dynamic values.
- [x] Step6: Run final combined new suites (root `test_*.py`) plus applicable cloned regressions with intended imports, saving exact raw outputs. Tests select no actual IO. Report final counts, changed-file/delta list, canonical-vs-raw binding, full program length proof, original immutable baselines and residual real-world uncertainty in `task2-report.md`/evidence/freeze.
- [x] Step7: Write concise Chinese README/operation-window with new exact future identity, fixed two-property Gets, original finite complete actions and no-retry/no-postfailure-readback rules. No actual grant, human IDs/time/selection remain null, environment/provider false. Do not ask for actual permission before the complete reviewed carrier exists.

## Root completion gates

- [x] Task1 scoped review accepted; C/I fixed by fresh fixer and re-reviewed; Minor adjudicated.
- [x] Task2 scoped review accepted; all command/output/freeze records independently checked.
- [x] Independent whole-slice review accepted and cannot-verify integration items resolved within offline scope.
- [x] Fresh Source/old phase/failed execution/HEAD/index integrity and exact current binding/program checks pass; no live/credential/provider calls occurred.
- [x] Complete delivery receipt, deterministic phase seal and read-only seal verification; distinguish offline completion from pending new real window.
