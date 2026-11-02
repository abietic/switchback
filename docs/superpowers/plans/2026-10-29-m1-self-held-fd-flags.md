# M1 Self-held FD Flags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct the private maintenance entry's self-held descriptor flag representation and deliver a completely rebound, independently reviewed, disabled offline carrier using unchanged kit25/bundle0002.

**Architecture:** At the held-FD capture boundary map F_GETFD's FD_CLOEXEC to O_CLOEXEC while preserving every F_GETFL status bit. Validate stable descriptor identity and raw flags before emitting an audit; feed the original exact policy unchanged. Extend the prior binding with explicit private-helper revisions and immutable receipts, without rewriting historical evidence or rebuilding production inputs.

**Tech Stack:** Python 3.9, unittest with synthetic Linux OS/fcntl facades, existing LZMA/base85 carrier packaging, SHA256 manifests.

## Global Constraints

- Worktree `/path/to/switchback`; preserve HEAD/index and existing dirty files. No Git commits or staging: preexisting dirty work is not this slice.
- Phase `.scratch/runtime-m1-self-held-fd-flags-1029`; new future execution `.scratch/runtime-m1-self-held-fd-flags-execution-1029-01/carrier` remains absent.
- Accepted design `runtime-m1-default-bridge-store-execution-1028-01/analysis/next-offline-fix-design.md`, copied byte-exact into this phase's `approved-design.md`; user's acceptance is offline-only.
- Never read, stat, enumerate or follow `.scratch/runtime-m1-worker-execution-credential-1026-01` or `.scratch/runtime-m1-containerd-dump-imports-1025`; reference strings are not authorization.
- No guest entry, Engine/PG/VM/service activity, actual grant, credential reads or provider/model calls. Tests select fake native interfaces only.
- Original phase seal `b404ed730807d999dbabc476a1b0dd403d6d95271ee136190b3071f6848a774c` and failed-window seal `3c74adc8efb4904b2f2c8aa38813e415fea326a8a703140e1654e77d534d9f43` remain byte-exact. New ROOT-baseline.json protects 24675 existing file records.
- Keep Source, Go, qualification templates, kit25 manifest `e82905915d9493f68d349fb4bfedb6abbdc8dfcb0262b312559d9834baa5b146` and bundle0002 manifest `0784bafc2c672dbb164a7a371c94741c5f7ed83e623a9f6f61ddef763e78823b` unchanged.
- Preserve original policy, owner-native collector, maintenance state machine, TypedPreserver, old installed byte checks, full coverage, daemon identity, Stop order, failure/unknown stop rules and budget values: maintenance120, Engine120, qualification240, barrier300, total7200.
- Exact caller/start/FD tuple required. Do not mask flags globally, exempt the whole sampler PID, add descriptor FD_CLOEXEC directly into access-mode bits, or treat a synthetic replay as a full original sample.
- Literal Python `/Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B`; no -I, no native environment adapters in offline runs.

---

### Task 1: Stable self-held descriptor capture and call-site regression

**Files:**
- Modify: `.scratch/runtime-m1-self-held-fd-flags-1029/carrier/maintenance_entry.py` only as the behavioral implementation.
- Create: `.scratch/runtime-m1-self-held-fd-flags-1029/test_self_held_fd.py`, its focused evidence and report/freeze under `task-1/`.
- Reuse unchanged: root four core modules, carrier's unchanged dependencies, `test_owner.py` fixture, and old failed-window frames.

**Interfaces:**
- Consumes `Inventory(config, deadline, audit)`, `Inventory.bind()`, original `validate_owner_pair`, raw FD/fdinfo rows in the failed stream.
- Produces `Inventory._held_descriptor(fd, sampler) -> dict` with exactly fd/target/flags/device/inode/socket_inode and separately emitted raw-source audit. Original protected schema is unchanged.

- [ ] **Step 1: Establish a RED at Inventory.bind.** Build a fake scene for actual `Inventory.bind`, stubbing unrelated immutable tree/material reads but exercising its real descriptor loop, then feed its protected contract into the original policy fixture. Linux values are F_GETFL=180224, F_GETFD=1, FD_CLOEXEC=1, O_CLOEXEC=524288 and Source fdinfo flags=704512. The test must fail on the old entry with `foreign protected FD`; use a test-only label/report to preserve the original nonzero result. No expected/actual tuple copied identically as the only test. Importing test modules must not run actual entry points.

```python
protected = inventory.bind()['protected']
observed = captured_fd_from_saved_inventory
sample['processes'][0]['fds'] = [observed]
sample['sampler'] = protected['sampler']
validate_owner_pair(sample, copy.deepcopy(sample), expected, 'quiet', protected)
```

- [ ] **Step 2: Implement the bounded capture method and replace the old loop.** Use original os/fcntl constants on Linux and injectable fake facades only in tests. Representative complete algorithm (integrate the audit field naming with existing conventions):

```python
def _held_descriptor(self, fd, sampler):
    def observe():
        self.check()
        value = os.fstat(fd)
        self.check()
        target = os.readlink('/proc/self/fd/%d' % fd)
        self.check()
        status = fcntl.fcntl(fd, fcntl.F_GETFL)
        self.check()
        descriptor = fcntl.fcntl(fd, fcntl.F_GETFD)
        require(type(status) is int and 0 <= status < (1 << 63), 'held status flags')
        require(type(descriptor) is int and descriptor >= 0 and
                descriptor & ~fcntl.FD_CLOEXEC == 0, 'held descriptor flags')
        require(status & 3 == 0, 'held descriptor must be read-only')
        require(type(target) is str and len(target.encode()) <= 4096, 'held target bound')
        return (value.st_dev, value.st_ino, target, status, descriptor)
    before = observe()
    after = observe()
    self.check()
    require(before == after, 'held descriptor identity/flags drift')
    device, inode, target, status, descriptor = before
    flags = status | (os.O_CLOEXEC if descriptor & fcntl.FD_CLOEXEC else 0)
    result = dict(fd=fd, target=target, flags=flags, device=device,
                  inode=inode, socket_inode=None)
    self.audit({'kind': 'held-descriptor-source-binding', 'authority': 'none',
                'sampler': dict(sampler), 'raw_f_getfl': status,
                'raw_f_getfd': descriptor, 'descriptor': result})
    self.check()
    return result
```

Actual integration should preserve readonly/directory identity, reject negative/invalid object fields, close failures through original lifetime handling, and use before/after stability with the same FD. The old loop becomes a comprehension calling this method with the exact parsed own PID/start keys. No Source or policy changes.

- [ ] **Step 3: GREEN plus negative coverage.** Exercise CLOEXEC both states, other status bits unchanged, raw audit values, FD_CLOEXEC numerical/access-mode confusion, foreign or unbound same-PID FD, wrong PID/start/target/device/inode, writes, unknown F_GETFD bit, exceptions from each getter/stat/readlink/audit, deadline failure, per-field before/after drift. Tests use actual call-site and helper seams without invoking guest/native FS APIs. Preserve captured-vs-synthetic provenance for two real FD/fdinfo rows, not a full original-sample claim.

```sh
python3 -B -m unittest discover -s .scratch/runtime-m1-self-held-fd-flags-1029 -p test_self_held_fd.py -v
```

- [ ] **Step 4: Self-review, report, exact freeze.** Record old/new entry SHA, added tests, RED/GREEN commands+exit/stdout/stderr, fake syscall counts and protected/no-runtime scope. Independently review this task's exact diff before Task 2; resolve all actionable findings. A fix stays in the same scoped report with original receipts preserved.

### Task 2: Complete carrier rebind and offline release verification

**Files:**
- Modify only new-phase `build_carrier.py`, `verify_carrier.py`, `test_carrier.py`, `test_lifecycle.py`, `task3-determinism.py`, necessary carrier selectors and new artifact/grant outputs.
- Create new-phase `private-revision-map.json`, role/provenance controls, packaging tests, task report/freeze and concrete `operation-window.md`/README.
- Root owns `ROOT-baseline.json`, `ROOT-integrity.py`, final review/verification/seal and delivery; coordinate rather than rewriting those inputs after binding.

**Interfaces:**
- Consumes Task 1 accepted/frozen new maintenance entry and old sealed default-bridge carrier.
- Produces complete current 45-file-style carrier with new immutable binding and disabled authorization; exact count may grow only for declared test/control leaves.

- [ ] **Step 1: Build explicit revision and selector maps.** `before` is original kit25 carrier entry; `after` is Task 1 reviewed entry. All four core modules, Source, templates, candidate trees and old installs stay equal. Change only fixed OFFLINE/EXECUTION paths and textual identity selectors required to identify this new phase; ACTIONS remain the same kit25 actions and POLICY_ID remains owned_engine_transition_v1. All helper differences require a before path/hash and after path/hash; Source revision map must still truthfully describe prior v4 revisions.

```python
assert current_entry_record == accepted_task1_freeze['entry']
for name in ('owner_policy.py', 'owner_native.py', 'maintenance.py', 'preserve_tree.py'):
    assert (new_phase/name).read_bytes() == (old_phase/name).read_bytes()
    assert (new_phase/'carrier'/name).read_bytes() == (old_phase/'carrier'/name).read_bytes()
new_source_files = dict(old_binding['source_files'])
new_source_files.update(current_declared_helper_and_receipt_records)
assert all(new_source_files[k] == v for k, v in old_binding['source_files'].items())
```

Use old canonical binding as historical ancestry, preserve its entire Source/history map, and add all current builder/verifier/helper/test/design/accepted review inputs. Binding coverage must be independently enumerated in verifier, not trusted from builder output. If old test-specific role lists need current selectors, update those checks without deleting refusal coverage.

- [ ] **Step 2: Regenerate every actual program with existing packers.** Maintenance package includes the reviewed entry. Decode all module rows and every real program to check exact Source/private bytes and actual argv limit <65536. Verify expanded/compressed frame/total caps. Reuse original kit25 and bundle0002 manifests. Disabled grant human IDs/timestamps/worker selection null, environment=false/provider=false/no_retry=true. Execution directory remains absent.

```python
assert grant['execution_phase'] == str(runtime.EXECUTION)
assert grant['artifact_binding_sha256'] == sha(encode(binding))
assert grant['environment_execution_authorized'] is False
assert grant['provider_calls_authorized'] is False
assert all(grant[k] is None for k in ('authorization_id', 'human_intent_ref',
                                     'issued_unix_ns', 'expires_unix_ns', 'worker_delivery'))
```

- [ ] **Step 3: Meaningful complete offline regressions.** Run current private owner/maintenance/preservation/transport/binding/lifecycle/finalization suites, new call-site tests, copied carrier compatibility tests and new private revision/tamper/budget checks. Test decoded maintenance package uses the corrected real capture path with synthetic Linux fcntl values. Test old consumed grant/old execution path and altered entry/revision hash are rejected before native adapter selection. Preserve original failures and all final exact command outputs.

```sh
python3 -B -m unittest discover -s .scratch/runtime-m1-self-held-fd-flags-1029 -p 'test_*.py' -v
python3 -B -m unittest discover -s .scratch/runtime-m1-self-held-fd-flags-1029/carrier-tests -p 'test_*.py' -v
python3 -B .scratch/runtime-m1-self-held-fd-flags-1029/verify_carrier.py
python3 -B .scratch/runtime-m1-self-held-fd-flags-1029/task3-determinism.py
```

Do not rerun unrelated unchanged Python242/Go2244 or rebuild binaries; root verifies full unchanged Source/candidate coverage instead. Synthetic Linux tests do not claim actual kernel execution.

- [ ] **Step 4: Task review, whole review and final seal.** Generate exact scoped diff against old copies, accepted Task 1 evidence and builder/verifier source context; a fresh reviewer checks standards and approved spec. Final whole review covers real data flow from capture→protected→decoded package→original policy, all inherited finite actions, credential non-use, readonly evidence, preservation and disabled grant. Resolve findings, run fresh final verifier/protection only after changes, finalize concrete operation checklist, then Root seals all new-phase files plus exact external current/history/candidate records. No writes to phase after seal.

## Completion checklist

- [x] Approved design copied; immutable baseline established; plan self-review clean.
- [ ] Task 1 accepted: exact call-site RED/GREEN and raw provenance.
- [ ] Task 2 accepted: complete carrier, compatibility, decoder and deterministic output.
- [ ] Independent whole review accepted; Root protection/verifier current; complete seal verified.
- [ ] User receives offline result, remaining actual Engine/Core/product loop status, exact candidate/checklist/seal. New real execution is a separate final confirmation and has not occurred.
