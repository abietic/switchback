# M1 Owned Engine Transition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver one disabled, fully bound private carrier which proves the saved kit21 deployment, stops only its two units, preserves all objects, unloads the old definitions, and hands off to unchanged kit23 and conditional first boot.

**Architecture:** A separate guest maintenance program owns T1–T6. An independent observation contract has `authority=none`; a same-host-process, once-consumed continuation receipt connects maintenance to the existing preparation driver. Production Source and all existing sealed inputs stay byte-for-byte unchanged.

**Tech Stack:** Python 3.9 stdlib, immutable Engine Python Source, Linux procfs/systemd and no-replace filesystem primitives behind fixed test ports. Offline tests use byte replays and private temporary directories only.

## Global Constraints

- The accepted design is `docs/superpowers/specs/2026-10-27-m1-owned-engine-transition-design.md`, approved bytes SHA256 `411852446c10c3923f43d8bc0f9a55fc1cde0e512b4c5953ac1beb26233e5176`; a copy is `approved-design.md` in the new phase.
- Work only in `.scratch/runtime-m1-owned-engine-transition-1027` and this plan. No Git mutation. No Source/template/binary/kit changes; no edits to any older phase.
- This is offline implementation only. No live VM/guest/Engine/PG/systemd/provider calls, no actual Stop, no credential reads, no host-stage reads. Tests never select real command implementations.
- Do not read/stat/scan `.scratch/runtime-m1-worker-execution-credential-1026-01` or `.scratch/runtime-m1-containerd-dump-imports-1025`.
- Reuse kit23 manifest `fb5d88d49ae6252bf72558339d954f71927b35fe4a74dba55e77fe8404b693bf` and bundle manifest `fa8e504846b756fcde1e25a5e77038dae7369be90f1701f733c59abdb1127f98`.
- Source root `sha256:11dbae2d86a0bc4198888a2e8d0e853a71c4d50c3ef50c4dccac1e46fd1d4c21`; 1093 Source + 7 templates, HEAD/index and 9769 immutable records are pinned in `ROOT-baseline.json`.
- New maintenance T1–T6 is 120 seconds inside the existing total 7200 seconds. Existing Engine120, qualification240, barrier300 and all original output/frame/audit bounds remain unchanged.
- Every transported guest program actual code argument must be strictly less than65536 bytes. Maintenance is separate; Source bytes cannot be minified or changed.
- `OwnerObservation` is private and `authority=none`; no capacity/Trust/Start/readiness authority. No generic ignore-loaded or relaxed production gate.
- Fail or unknown ends the complete driver: no further guest action, Stop, retry, independent readback or rollback. Local held-output capture and close attempts preserve the original error.
- Only exact old boot/PID/start/exe/hash/cgroup is owned. Initial six roles, containerd-only two roles, then zero live daemon listeners; libnetwork parent0600/0700 is ownership-only and never converted into v2 readiness.
- No business containers, volumes, plugins, Worker/shim, restore references, foreign writer/FD/maps, conflicting jobs or unauthorized Stop propagation. Engine-absent still requires restoration scanning.
- Only already-listed loaded units can be shown/stopped; Stop uses `systemctl --job-mode=fail stop` once for Engine then once for containerd. Each successful Stop requires full predefined postconditions; exit0 is insufficient.
- Full nested runtime + separate API socket must be preserved with no-replace rename and identity/hash/type coverage. No deletion, unmount, network cleanup or data-root move. Fixed new archive names are `archive-owned-engine-1027-kit-0021-to-0023` and `.preserve-owned-engine-1027-kit-0021`.
- Same-window first-observed archive roots require complete known child/hash/manifest and stable parent/tree proofs; they cannot be guessed from historical records.
- After preservation exactly one daemon-reload must remove both definitions from full installed/loaded catalogs; invoke original strict quiescence/empty-target gates before deployment.
- New grant template is disabled/null. Future actual execution requires a separately authorized exact sealed carrier. Old receipts or a disk JSON file cannot resume a new driver.
- Use fresh task implementers and reviewers sequentially. Use immutable per-task input manifests/diffs, not dirty Git HEAD. Fix Important/Critical findings with a fresh fixer; final review uses the most capable model. No commits.

## File ownership and interfaces

All paths below are relative to `.scratch/runtime-m1-owned-engine-transition-1027`.

| Files | Responsibility |
| --- | --- |
| `owner_policy.py`, `owner_native.py`, `test_owner.py` | Independent owner DTO, policy, fixed read-only sampler and offline replays |
| `maintenance.py`, `preserve_tree.py`, `test_maintenance.py`, `test_preserve_tree.py` | Fixed stop/preserve/reload sequence, typed FD-pinned nested trees and postconditions |
| `carrier/` | New cloned/rebound private preparation composition, disabled entry/grant and separate maintenance transport |
| `build_carrier.py`, `verify_carrier.py`, `test_carrier.py` | Deterministic offline binding, full transport decoding/length proof and failure composition regressions |
| `ROOT-integrity.py`, `ROOT-*.json`, review packages and ledger | Root-owned evidence and final seal |

### Task1: Owner observation and fixed read-only native sampler

**Files:** Create `owner_policy.py`, `owner_native.py`, `test_owner.py`, `task1-report.md`. Do not edit Root files or other task files.

**Interfaces:**

```python
# owner_policy.py: JSON-safe observation in, validated JSON-safe proof out.
def validate_owner_pair(before: dict, after: dict, basis: dict,
                        stage: str, protected: dict) -> dict: ...
# stages: owned_pair, containerd_only, quiet. Proof always authority=none.
# owner_native.py: constructor/import has no native IO.
class OwnerSampler:
    def __init__(self, source_modules, audit, expected): ...
    def observe(self, stage: str, deadline: float) -> dict: ...
# observe collects both complete samples, calls validate_owner_pair and
# returns a proof containing its bound samples/digests and exact selected units.
```

- [x] Step1: Build synthetic fixtures with explicit synthetic provenance and replay saved actual raw process/listener data. Expected missing historical extra FS facts must fail until supplied by explicitly synthetic sample values; never label this live evidence.
- [x] Step2: Implement closed-schema policy for stable boot, old daemon identity, manager effective configuration/Stop propagation closure, exact six/two/zero owner roles, socket FS/parent identities, complete stable process/FD/maps and current sampler exact held-FD exemptions. Reject ordinary identity drift, unknown special paths and foreign writers in either sample. Preserve legitimate established same-owner sockets.
- [x] Step3: Implement actual read-only sampling behind injectable IO: bound Source parsers/helpers, exact loaded-only manager queries, full process/net/mount/filesystem data, original restoration scanner, and fixed read-only Unix API only after owner proof. No network/API when absent and no writes/service actions in sampler.
- [x] Step4: Demonstrate failures for same-name/newPID, boot/start/exe/cgroup/effective property/trigger/reverse dependency/job drift, hidden daemon/shim, foreign writable FD/maps, unknown socket, busy API/restore state, incomplete/truncated sample and output/deadline errors.
- [x] Step5: Run `PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s .scratch/runtime-m1-owned-engine-transition-1027 -p test_owner.py -v`; save exact command/output and interface/schema notes in `task1-report.md`. Review this task before Task2.

### Task2: Controlled maintenance and full typed preservation

**Files:** Create `maintenance.py`, `preserve_tree.py`, `test_maintenance.py`, `test_preserve_tree.py`, `task2-report.md`. Consume Task1; any interface correction must be documented and reviewed.

**Interfaces:**

```python
def run_maintenance(ops, binding: dict, now=None) -> dict: ...
# Fixed ports: observe(stage, deadline), stop(unit, deadline), preserve(proof,
# deadline), reload(deadline), strict_empty(deadline), audit(event), close().
# Observe returns validated OwnerSampler proof. No direct retry path.
class PreservedContinuation:
    # Internal host object, not deserializable authority. Consume once in its
    # creating host PID/driver nonce; binds old/new grant/binding + raw proof.
    def consume(self, driver, binding: dict) -> dict: ...
```

- [x] Step1: Write command-order tests with fake ports asserting the exact Engine→postproof→containerd→postproof→preserve→reload→strict sequence, absent/quiet cases and zero subsequent calls after every failure injection. Maintenance120 is nested inside parent7200.
- [x] Step2: Implement fixed selected-unit Stop calls, immediate identity recheck, post-success full closure, one reload and unchanged strict final gates. Preserve work error even when local audit/close fails, attempt all held closes.
- [x] Step3: Implement no-follow, same-FS FD-pinned typed snapshots of nested directories, regular files, proven sockets and fixed API residual. Cover all leaves and parents, stable initial/closing samples, content hashing, exact mode/owner/nlink, no symlink/device/mount, no-replace target and held archive root identity. Preserve socket original type/identity without opening it for IO. Use private temp fixtures only.
- [x] Step4: Cover target occupied, raced path, cross-FS, changed regular bytes, unknown socket/special file, nested mount, rename failure/partial outcome, API absent/present, reload stillloaded, Stop0 but alive and same-process receipt replay/new-process refusal. A receipt cannot itself pass Start gates.
- [x] Step5: Run the owner+maintenance+tree suites; save exact output and interfaces in `task2-report.md`. Independent review/fixes before Task3.

### Task3: Full disabled carrier, saved-state binding and kit23 handoff

**Files:** Create `carrier/`, `build_carrier.py`, `verify_carrier.py`, `test_carrier.py`, `operation-window.md`, `README.md`, `task3-report.md`. May modify new Task1/2 modules only for a demonstrated integration defect, then run their covering tests.

**Consumes:** Task1 OwnerSampler and Task2 run_maintenance/PreservedContinuation. Immutable private seed `.scratch/runtime-m1-complete-listener-contract-1027/whole-fix-delivery`, saved basis and exact kit21 failed execution inputs in the approved design.

- [x] Step1: Clone only required private module/data inputs into `carrier/`; pin seed raw hashes in provenance. Rebind old=current kit21 identities, stage9735 leaves and existing archives from saved raw receipts. Correctly distinguish historical IDs from same-window first-observed archive roots. Do not inspect live host paths or any credential directory.
- [x] Step2: Wire one host lifetime: exact null/disabled grant guard→local input/VMcheck/ownerfreeze→maintenance T1–T6→new four kit23 copies→original Engine120 v2→conditional qualification240. Maintenance T5 includes guest tree preservation and a single predefined host-preservation checkpoint, before T6 reload: the same guest process waits on a bounded fixed ACK while this host driver preserves its old copies/stage. This keeps all T5 actions before T6, without a resumable second guest call. Verify observer archives via existing read-only check, all old archives preserved. Original production strict preflight executes only after successful maintenance; no bypass flag or old nine-node preserver on the new nested runtime. Consume in-memory receipt once.
- [x] Step3: Preserve original transport bounds and complete Source/raw/module decoder integrity. Maintenance gets its own separate program. Rebind exact new candidate manifests, Source/templates, new action list and all carrier code. Disabled authorization has null human IDs and environment/provider false; real entry must reject old grant, old phase, payload drift, reused claim and disk-replayed receipt.
- [x] Step4: Test complete host/guest composition using fake command boundaries, not only isolated functions: stage failures, nonzero recorder, local-only held output retention, archive identity on partial failure, no remote action after failure, unchanged strict guard, conditional Engine-success prerequisite, no model calls or credential read during offline verification.
- [x] Step5: Deterministically build and independently decode each program; compare exact Source and private module SHA/bytes/order/dedup, canonical base85/LZMA2/EOF/memory/expanded limits and every actual code argv length<65536. Save complete length/binding proof. No Go/ELF rebuilding or executing.
- [x] Step6: Run combined new suites and cloned applicable suites once after final changes; verify kit23/bundle and baseline integrity. Save exact full outputs. Deliver operation-window for a future single reviewed grant covering complete sequence and failure-stop, but do not select actual/ask early.
- [x] Step7: Fresh most-capable whole review of all new code/bindings/closed design; fix full list via fresh fixer, rerun covering checks, re-review. Root seals only after all required checks pass and current Source/old bytes/HEAD/index unchanged.

## Root completion checklist

- [x] Task1 implementation/review accepted.
- [x] Task2 implementation/review accepted.
- [x] Task3 implementation/review accepted.
- [x] Final whole-slice review has no unresolved Critical/Important findings; Minor findings adjudicated in ledger.
- [x] Fresh integrity and exact program/candidate binding checks pass; no environment calls/credential reads/provider calls occurred.
- [x] Write deterministic phase seal, verify it read-only once, and report offline delivery vs pending real run distinctly.
