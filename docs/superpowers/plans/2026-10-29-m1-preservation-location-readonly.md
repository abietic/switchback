# Preservation location readonly implementation plan

> For agentic workers: use subagent-driven-development for Task1 offline implementation and review. Root performs Task2 exactly once after all offline gates pass.

**Goal:** Execute the already approved single readonly inventory, preserve a complete receipt, and use it only for a new offline recovery design.

**Architecture:** Fixed guest fd-pinned metadata/hash sampler plus host interactive clock/transport wrapper. Reuse unchanged Source process ownership/cleanup helpers. Keep bounded observation/failure evidence separate, with no service or file mutations in guest.

**Tech Stack:** Python3.9 stdlib, existing pinned Lima and Linux Python, current managed worktree.

## Global constraints

- Human authority: call_576af4d37b6542768c51aa297ad78ddd only; whole60s, guest40s; oneVMlist and oneguestcall; no retry or post-failure guest calls.
- scope.json exact26 positions; Root hash-policy.json selects1175 files/93010510B and leaves12 public files metadata-only with observed hash null; no complete-content acceptance; max16384 nodes, aggregate regular128MiB, stdout8MiB, stderr16KiB, config2MiB, code argv<65536.
- Guest read-only: no file writes/renames/deletes, systemctl, EngineAPI, PG/provider, credentials, VM starts, partition changes or automatic boot.
- Source/Go/templates/kit/bundle/old sealed evidence and HEAD/index/tracked dirty paths remain unchanged; no commits or staging.
- No read/stat/enumerate/follow of the two forbidden credential/dump directories documented in the task brief.

## Task 1: Offline sampler, protocol and test delivery

**Files:** Create /path/to/switchback/.scratch/runtime-m1-preservation-location-readonly-1029-01/collector.py, reader.py, test_collector.py, test_reader.py, offline-report.md, offline-test.*, freeze.json. Requirements and precise test cases: /path/to/switchback/.scratch/runtime-m1-preservation-location-readonly-1029-01/task-1-brief.md.

**Consumes:** scope.json and basis.json (Root-owned); fixed Source helper modules, unchanged. **Produces:** guest program/config binding, read-only host actual entry gated on explicit authorization/claim, deterministic offline evidence.

- [ ] Implement the bounded target-only sampler and clock protocol at the real call sites described in the brief.
- [ ] Test original captured1344-node layout using Python-only fake ports; test fault and boundary cases without invoking native environment commands.
- [ ] Verify complete whole profile, independent failure evidence and no key/Source/service side effects.
- [ ] Run final suite and freeze all owned inputs; produce report with exact command/exit/raw output hashes.
- [ ] Fresh task review: Spec and Quality; fix actionable findings within approved scope and rerun affected tests.
- [ ] Most-capable whole-slice review of code and Root protocol/binding integration; no repeated broad tests unless a new failure requires them.

## Task 2: One actual window and evidence closeout (Root only)

- [ ] Verify old failure seal bff41907448ffd096c4432b1ded88e197f78eec6ef0090f15dc3e9dbc36b2fbb and current source/context/bindings.
- [ ] Issue an exact short-lived grant for the accepted single readonly question, create actual claim once, run reader.py actual once.
- [ ] On failure/unknown stop environment work permanently; inspect held local bytes only.
- [ ] On success, classify all8 source/target locations and5 archive roots, and write a complete offline recovery design for later approval; do not perform recovery.
- [ ] Seal current evidence and verify current Source/history/HEAD/index preservation; report actual outcome, limits and next design.

## Verification commands

From the fixed worktree, literal Framework Python3.9 -B:

```sh
python3.9 -B -m unittest discover -s /path/to/switchback/.scratch/runtime-m1-preservation-location-readonly-1029-01 -p 'test_*.py' -v
python3.9 -B /path/to/switchback/.scratch/runtime-m1-preservation-location-readonly-1029-01/reader.py actual
```

The first command uses fake ports only. The second is Root-only, once, after static hashes/reviews and exact authorization. Do not execute either through a host -I wrapper.

