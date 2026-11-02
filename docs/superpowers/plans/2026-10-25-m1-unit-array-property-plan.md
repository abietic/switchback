# M1 Unit Array Property Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Preserve every Listen/ExecStart item only in the existing catalogue show query, and deliver a fully bound new offline kit/bundle.

**Architecture:** Keep the default parser and all other show contexts strict. The fixed catalogue property tuple enables an explicit private mode with exactly two repeated-property names; values remain strings, joined in observed order with one newline, so current daemon scanning sees every command. Source and complete artifact delivery are two serial independently reviewed tasks.

**Tech Stack:** Python3.9-compatible standard library, existing unittest suites, existing offline Go builder and canonical bundle builder.

## Global Constraints

- Accepted design: `docs/superpowers/specs/2026-10-24-m1-unit-array-property-design.md`; user authorization `call_3abe728888b74f798a0a581e7742c4c6`.
- Work only in `/path/to/switchback`. Phase `.scratch/runtime-m1-unit-array-property-1025`; prior20124 sealed records and1995 pre-plan baseline are immutable.
- Protect all existing dirty changes. No staging/commit/reset/clean/checkout/branch operations; HEAD/index fixed. This user boundary overrides skill commit/finish defaults.
- No guest/Lima/lsof/Engine/PG/provider/native fixture or compiled candidate execution; no live reads or writes of `~/.switchback`, `/opt/switchback`, current four copies, six archives, or2514leaf stage. Existing WT saved evidence may be read.
- Only four Source paths may change: `deploy/experience/engine/{guest.py,test_prepare.py,README.md}` and `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`. Go change is one new bundle discovery glob. Do not add an Engine Source leaf or alter builder allowlists.
- Default `_properties` and every other show query stay strictly unique. Only exact `('Id','ActiveState','MainPID','UnitFileState','ExecStart','Listen')` catalogue tuple enables fixed `Listen`/`ExecStart` repetition. Input content cannot enable this mode.
- Keep every item, order and multiplicity; concatenate with a single newline. Preserve zero/one-item empty/missing semantics; reject repetition with any empty item. Duplicate scalars, including identical values, and malformed lines still reject.
- Preserve complete installed/loaded catalogues,512-name bound, template provenance, all ordinary/instance properties, all ExecStart daemon scanning, and strict own-unit effective ExecStart checks. Retain output256KiB only for two exact list argv, otherwise16KiB; frame1MiB/audit64MiB; deadlines120/240/300; permissions/resources/PG/Trust/Start unchanged.
- Saved current sample:311 union names,28 definitions,283 ordinary;39 captured show outputs (38 parsed and1 target),244 later ordinary properties unobserved.4 template observations were traversed, not all28. Derive facts from raw frames rather than manually copying counts; synthetic facts must be explicitly separate.
- Artifact builder may compile only from locally cached pinned dependencies. Use `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOMAXPROCS=4 CGO_ENABLED=0 GOENV=off GOWORK=off GOPATH=/Users/example/go GOMODCACHE=/Users/example/go/pkg/mod GOCACHE=/private/tmp/switchback-qualification-1022-go-cache PYTHONDONTWRITEBYTECODE=1 TMPDIR=/private/tmp/ HOME=/Users/example`; no package downloads.
- For Python unittest, override TMPDIR to the absolute task-owned mode0700 `.scratch/runtime-m1-unit-array-property-1025/task-1/test-tmp`; strict directory-owner fixtures require safe ancestors. Preserve initial `/private/tmp/` fixture failures as evidence. This test environment correction does not modify production Source.
- Evidence: retain exact argv/cwd/exit/raw stdout/stderr/hash for each real invocation, preserve failed helper attempts, never synthesize passing streams. Task reports and review packages are files. Root owns ledger/status/plan and final phase seal.

---

### Task 1: Source contract, real replay, and regression gate

**Files:** Modify only the four allowed Source paths. Create own outputs under `.scratch/runtime-m1-unit-array-property-1025/task-1/`; full report `.scratch/runtime-m1-unit-array-property-1025/task-1-report.md`.

**Interfaces:** Consume accepted design, phase authorization/baseline/before snapshots, saved packet `.scratch/runtime-m1-unit-template-engine-window-execution-1024/{engine-guest-frames.json,target-show.stdout.bin,parser-diagnosis.json,replay_show.py}`. Produce strict-default parser/context-specific show behavior, source-after copies, raw command receipts, a complete source report and full saved catalogue replay.

- [x] Read design and phase authorization, relevant source via CodeGraph, and existing tests. Only the four Source paths and task1 outputs/report may be edited; no plan/status/ledger or other phase writes.
- [x] Add failing tests at actual `_IO.show` and `_unit_catalog` seams before production changes. One real captured-input example is:

```python
raw = (b'Listen=/run/dmeventd-server (FIFO)\n'
       b'Listen=/run/dmeventd-client (FIFO)\nId=dm-event.socket\n'
       b'ActiveState=active\nUnitFileState=enabled\n')
class IO:
    def command(self, argv):
        assert argv == ['/usr/bin/systemctl', 'show', '--no-pager',
                        '--property=Id,ActiveState,MainPID,UnitFileState,ExecStart,Listen',
                        'dm-event.socket']
        return raw
result = guest._IO.show(IO(), 'dm-event.socket',
                       ('Id','ActiveState','MainPID','UnitFileState','ExecStart','Listen'))
assert result['Listen'] == '/run/dmeventd-server (FIFO)\n/run/dmeventd-client (FIFO)'
```

- [x] Capture RED with original production parser: duplicate/malformed property at the real show call, while controls pass. Preserve output and receipt before changing production code.
- [x] Implement a small explicit fixed context. Complete reference behavior (adapt names to existing conventions without expanding scope):

```python
_CATALOG_PROPERTIES = ('Id', 'ActiveState', 'MainPID', 'UnitFileState', 'ExecStart', 'Listen')
def _properties(raw, *, catalogue=False):
    result = {}
    for line in raw.decode().splitlines():
        key, sep, value = line.partition('=')
        _require(sep and (key not in result or
                 (catalogue and key in ('Listen', 'ExecStart'))),
                 'duplicate/malformed systemd property')
        if key in result:
            _require(result[key] and value, 'empty repeated systemd property')
            result[key] += '\n' + value
        else:
            result[key] = value
    return result
# _IO.show: get the unchanged exact argv output, then call
# _properties(raw, catalogue=properties == _CATALOG_PROPERTIES).
```

- [x] Meaningful boundary tests: repeated Listen/ExecStart retain order and identical multiplicity; any repeated empty item rejects; duplicate scalars/unknown keys reject; absent/single empty fields preserve old behavior; malformed lines reject; direct default parser and reordered/subset/other queries reject repeats; installed own-unit effective query rejects multiple ExecStart. Preserve original nonzero/timeout/truncation rejection at command seam.
- [x] Test actual catalogue daemon recognition when dockerd/containerd/shim appears in second/last ExecStart; verify a dangerous running unit still reaches existing rejection, not skipped as unrelated. Avoid tests merely asserting a chosen helper exists.
- [x] Full saved catalogue replay uses exact captured list and39 show bytes through actual `_IO.show`/`_unit_catalog`;244 remaining facts explicitly synthetic. Reuse existing fixture constants only after proving byte equality; otherwise embed exact data in existing test leaf, without adding Source leaves. Derive and record set/count/provenance facts.
- [x] Append only `.scratch/runtime-m1-unit-array-property-1025/task-2/bundle-*/manifest.json` to existing Go candidate discovery. Briefly document context/multivalue behavior and real/synthetic evidence boundary in existing Engine README.
- [x] Run focused GREEN, full Engine unittest once, qualification window16, Engine self-check, and related Go gate. Commands:

```text
/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v
```

- [x] Explicitly distinguish any prebuild final-binding SKIP from Task2 required actual final binding. Preserve final source-after copies, before-relative four-file diff, focused/full raw receipts and report. Self-review spec and boundary effects. No commits. Report DONE/concerns and brief test counts only; detail lives in report. Stop writing Source/task1 after handoff unless Root requests a fix.

### Task 2: Complete new offline kit/bundle and preservation handoff

**Files:** Create `.scratch/runtime-m1-unit-array-property-1025/task-2/` outputs plus `.scratch/runtime-m1-unit-array-property-1025/task-2-report.md`. Create exactly one new kit through original builder under `.scratch/runtime-m1-qualification-delivery-1022/task-4/`; no Source edits, no writes in old phases/kits, no deletion/rewrite of failed outputs.

**Interfaces:** Consume accepted Task1 source-after records and Root source freeze/task review. Use unchanged original `deploy/experience/qualification/build_candidate.py build` and canonical `deploy/experience/engine/bundle.py` APIs `build_bundle(kit:Path,destination:Path)->dict`, `verify_bundle(bundle:Path,kit:Path,*,check=lambda:None)->dict`. Produce complete current kit, bound bundle, independent artifact audits/raw receipts, preservation identity plan, future exact operation checklist and immutable delivery freeze.

- [x] Read Task1 report/review and verify source freeze; record build-before Source/Template inputs via original builder. Use prior complete offline task2 pipeline `.scratch/runtime-m1-unit-template-catalog-semantics-1024/task-2/` only as a read-only reference. Its correction subtree/report count fixes are effective for old delivery; do not propagate old numeric assertions.
- [x] Perform all pure input/schema/template/receipt/path preflight before original build. All generation and tests remain offline. Capture all command results through an exclusive standard runner with time bound and exact streams.
- [x] Invoke original builder exactly once. Concrete command:

```text
/usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build
```

Use its configured delivery output and fresh next kit directory; inspect its actual stdout/manifest rather than inventing a kit path. If source builder CLI requires flags, inspect its help and use exact documented output-directory argument before launching; do not probe `build` repeatedly. Build failure is evidence; do not delete consumed output or externally retry it without Root adjudication.
- [x] Canonical new bundle program:

```python
import sys
from pathlib import Path
sys.path.insert(0, str(Path('deploy/experience/engine').resolve()))
from bundle import build_bundle, verify_bundle
kit = Path(actual_kit_path).resolve()
bundle = Path('.scratch/runtime-m1-unit-array-property-1025/task-2/bundle-0001').resolve()
result = build_bundle(kit, bundle)
verified = verify_bundle(bundle, kit)
assert result and verified
```

- [x] Independently check every covered leaf and exact coverage/type/mode/bytes/hash; original record-order SourceRoot/TemplatesRoot, current Source and templates match before/after; build logs and actual binary headers/build-info. Prior expected shape1076 Source/7 templates,1152 kit/18bundle leaves,20 formats,43 exit0 receipts,17 reverse inputs,13 runtime null/4 authority false. Derive actual counts; any mismatch requires investigation, not silently changing expectations. Execute no built binary.
- [x] Run `TestQualificationEngineDeploymentFinalKitBinding` against actual new kit/bundle; must PASS/noSKIP with runtime reads zero. Preserve stdout/receipt with paths and exact candidate identities.
- [x] Derive preservation data from saved receipts only: kit16 four current copy identities from `.scratch/runtime-m1-unit-template-catalog-semantics-execution-1024/copy-switch-verification.json`; six archives = prior five in that phase plus its new archive roots from host-effect-02/create_archive.frames; newest consumed stage from `.scratch/runtime-m1-unit-template-engine-window-execution-1024/host-stage-preservation.json` (2514 leaves,root inode143593011). Bind original host unknown/guest failed claim/grant/boot; do not promote saved facts to live verification.
- [x] Prepare a concrete future no-replace five-old-object/four-new-copy switch and single Engine120s then conditional preparation/qualification240s checklist, retaining all six archives and2514leaf stage. Specify fresh exclusive archive/stage/claims, VM must already be Running, child-only SSH no-multiplex if needed, original300s barrier and permissions/resources/PG/Trust/Start; fail/unknown stop, no retry/resume/postfailure guest readback/remoteStop/VMstart/partition/provider. No actual authorization is granted by offline acceptance.
- [x] Generate catalogue sample explanation from actual saved list/show frames:311/28/283/39/244; distinguish38 parsed from39 raw captures and4 traversed template observations. Validate generated report/checklist facts against independent derivation, including negative mutated counts or real/synthetic overlap. Do not use stale string-only checks such as merely searching one expected number.
- [x] Freeze own delivery subtree and external kit records with exact coverage/hash, including raw receipts and effective checklist. Leave review/Root outputs outside this task freeze. Verify original Task1 freeze and all old inputs remain unchanged. Write self-review/report with actual identities, validation counts, limitations and pending next actual scope. No Source edits/commits or environment actions; handoff file paths and a concise DONE result only.
