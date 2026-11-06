# B3-C Executable Stage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. This task is one coupled host/guest protocol change, followed by task and whole-slice reviews.

**Goal:** Build the accepted offline B3-C diagnostic carrier with held-FD filesystem facts and exact-ELF X_OK checks, preserving single-attempt and owned-cleanup semantics.

**Architecture:** Copy the five sealed B3-B1 source files into a new isolated scratch phase. Keep the original ELF as an immutable input; bind it to a new candidate, nonce, stage, and internal wire version. Keep path ownership separate from launchability so post can stop its proven unit even after mount flags change.

**Tech Stack:** Python3 standard library, unittest primitive-only fixtures, Linux arm64 native ports that remain unexecuted, fixed Lima2.2.1/systemd255 selectors.

## Global Constraints

- Worktree `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`; existing branch and index remain unchanged. No commits/staging/push or existing-ledger writes in this dirty shared checkout.
- Accepted spec `docs/superpowers/specs/2026-10-20-m1-deployment-executable-stage-b3c-design.md`, SHA256 `5dd9f009b61f6ed77259e5f50823625ae374d44ce15d62171ebb37342f0ba941`.
- New code/evidence phase `.scratch/runtime-m1-deployment-exec-stage-b3c-1020/`. Root controls manifests, release, ledger, final report/seal; implementer controls exactly five code files, `execution-candidate.json`, and `impl/` evidence. Reviewers write only their assigned review files.
- Future `.scratch/runtime-m1-deployment-native-window-b3c-1020/` must remain absent. No native carrier mode, VM/guest entry, Engine/PG/provider/Worker/publisher/Backend, guest writes, unit commands, installations, keys/Trust/journal/quota/project/inheritance or permission changes.
- Old carrier613, actual-window43 and diagnosis18 sealed artifacts stay immutable. Original current970 inputs and Git-visible1772 paths/content are protected; this plan is the only expected new Git-visible file.
- Fixed guest stage `/var/lib/switchback-deployment-probe1020-b3c`; unit `switchback-deployment-probe1020-b3c.service`; nonce `9c44cb69-ab9f-4910-9af3-7080d9b91f07`.
- Original prepared ELF5640057 bytes, SHA256 `a1309c87bc3868f275f05e4ec42c853f7b57c7c2c356ac616e94d0f769a62d27`, still read from original prep phase. No rebuild/Go tests required by this source-only Python slice.
- All original unit capability/resource/syscall properties and mode budgets30/45/45/30 remain; only stage-dependent ExecStart/output paths change. Preserve original raw exit0/2 and incomplete-transport/ownership rules.
- `capacity_ready=false`, `execution_enabled=false`, `paid_consumed=false`; no production readiness or native execution claims.

---

## 📁 File ownership and verification boundaries

| File | Responsibility |
| --- | --- |
| `guest.py` | Fixed new candidate, bounded native raw ports, filesystem/mountinfo/access parsers and mode sequencing |
| `host.py` | New candidate and source/ELF bindings; independent strict reply/prior validation; fixed transport |
| `test_guest.py` | Independent primitive fixtures, real-bug RED, valid/failure/cancellation/effect/FD coverage |
| `test_host.py` | Independently authored complete raw wire fixtures, malformed replies/prior and integrity verifier tests |
| `verify.py` | Read-only new release verification, legacy seals and current/frozen hashes, tests/review evidence |

These five files are under the new phase only. No new general launcher, reusable product filesystem module, public path selector or external dependency.

### Task 1: Implement and validate the coupled B3-C carrier

**Files:** create the five files above, `execution-candidate.json` and `impl/task-1-report.md`; read the original five files under `.scratch/runtime-m1-deployment-carrier-b3b1-1020/` without modification.

**Interfaces:** retain `main(argv=None)`, guest `_dispatch` and raw `NativeGuest.call`/primitive fixture seams, host `_run` and `validate_reply`, and pure `verify_release(read)` unless a small internal change is required. New request version is2; guest/host schemas are `switchback.b3c.guest.v2` and `switchback.b3c.host.v2`; release/test-receipt schemas are `switchback.b3c.offline_release.v2` and `switchback.b3c.test_receipt.v2`. Original schemas/nonce/path cannot qualify a new prior. Diagnostic DTO schema remains unchanged.

- [ ] **Step 1: Freeze a source baseline and construct independent noexec RED.** Copy the baseline with exclusive creation:

```python
from pathlib import Path
origin = Path('.scratch/runtime-m1-deployment-carrier-b3b1-1020')
target = Path('.scratch/runtime-m1-deployment-exec-stage-b3c-1020')
for name in ('guest.py','host.py','test_guest.py','test_host.py','verify.py'):
    with (target / name).open('xb') as out:
        out.write((origin / name).read_bytes())
```

Author the new test against raw filesystem/access ports before applying the missing guard. A mechanically retargeted baseline is allowed, but label it explicitly: it must still omit the missing flags policy and reach the ordinary behavior assertion. Qualification must show the fixture is otherwise valid. An import error, request/schema/path mismatch, or rejection caused only by changed fixture fields is not evidence for the noexec bug.

The test assertion must establish this relationship:

```python
result = dispatch_preflight_with_valid_raw_ext4_fixture(flags_noexec=True)
self.assertEqual(result['status'], 'rejected')
self.assertFalse(io.effects)  # no stage create/write or unit command
self.assertFalse(result['actual_consumed'])
```

The fixture helper is private test setup: independently author mountinfo, directory metadata and filesystem raw values; do not build successful raw evidence by echoing production expected dictionaries or by calling guest success and treating it as independent host qualification. Save RED stdout/stderr, events, exit code, and before/after source snapshots under `impl/`.

- [ ] **Step 2: Implement bounded filesystem and mount association facts.** Keep raw `statfs_flags` and `statvfs_flags` as distinct nonnegative integers, plus type and available space. Linux statfs can include ABI control bits not exposed by statvfs; compare the defined RO/NOEXEC semantic bits, preserve both full values, and never require incidental full-word equality or rewrite unknown bits.

```python
EXEC_POLICY_MASK = 1 | 8  # ST_RDONLY | ST_NOEXEC
assert type(statfs_flags) is int and statfs_flags >= 0
assert type(statvfs_flags) is int and statvfs_flags >= 0
assert (statfs_flags & EXEC_POLICY_MASK) == (statvfs_flags & EXEC_POLICY_MASK)
launchable = (statvfs_flags & EXEC_POLICY_MASK) == 0
```

Open existing `/`, `/var`, `/var/lib` via no-follow directory FDs. Validate root ownership and group/world write bits, root/parent device relation, held-FD fstatfs magic0xEF53, space>=16MiB, and bounded mountinfo association. Parse `/proc/self/mountinfo` at most1MiB with correct escaped fields, mount boundaries, separator and device correlation; choose the actual longest matching mountpoint, reject ambiguity/unparsable observations, require corresponding fstype ext4, and preserve corresponding raw records. No parent creation or alternate directory.

Raw transport envelope remains256KiB and per-command bounds remain unchanged. If complete mountinfo/selected evidence cannot fit the existing envelope, fail closed with a bounded honest rejection; do not silently truncate or present a partial record set as complete. The implementation must let host independently validate the evidence it uses for filesystem admission.

- [ ] **Step 3: Separate owned-stage opening from launch validation, add exact X_OK facts.** Keep stage identity/ELF/binding verification usable in post. `preflight` rejects unavailable launch filesystem before writes; `stage` verifies new stage/ELF filesystem and exact path plus held-FD proc path with `access(X_OK)`, preserving return/errno and before/after identities, before returning staged. `actual` rechecks launch facts before Start, retaining host consumption first and original guest ownership/consumption ordering. Post may observe RO/NOEXEC but must not apply those launch predicates to reject otherwise proven owned cleanup.

```python
owned = open_and_verify_owned_stage(request)  # identity + binding, not launch authority
if mode in ('stage', 'actual'):
    require_launch_filesystem(owned)
    require_exact_elf_access(owned)  # path and held-fd X_OK, raw rc/errno
# post uses the original full ownership/InvocationID/ExecStart proof and Stop chain
```

These helper names describe internal interfaces; define all used helpers in `guest.py`, and keep their host validation counterparts independent. Preserve errno distinctions, partial evidence, once semantics, own-process-group cleanup and GC-aware remote termination rules. No additional capability, syscalls that execute a probe, environment selector or callback execution authority.

- [ ] **Step 4: Bind new candidate and strict host/verify protocols.** New `execution-candidate.json` is the only source of the new fixed launch argv/properties/nonce/stage/unit. It references original ELF/prep seal without changing them. New host `bindings` validates the immutable legacy provenance and the new candidate; it must not compare changed argv against the old candidate or mutate the old prep seal to obtain success. Each mode saves source snapshots/raw transport and strict new-version facts; host validates flags, mount association and X_OK raw facts, all prior receipts and source bindings without trusting a guest success string. The read-only verifier verifies new baseline/manifests/test/review/schema and old seal protection.

Root will produce `baseline.json`, `product-before-manifest.json`, `git-visible-before-manifest.json` before dispatch and final `release-manifest.json` after review. Baseline protected counts are972 inputs (old970 + accepted spec + new plan) and1773 current Git-visible paths; references use worktree-relative `frozen_ref`. Do not edit these Root-owned files. New current/frozen source release will add the five files. Reference Root's actual manifests rather than inventing success records.

- [ ] **Step 5: Add focused regression/fault coverage and run one full final suite.** Cover flags missing/negative/bool/string/mismatch/RO/NOEXEC/unknown-other-bits, wrong type/device/owner/mode/symlink, malformed/escaped/overlong/ambiguous/wrong-device/non-ext4 mountinfo, valid staged path and held-fd checks, EACCES/EPERM/other errno/disagreement, stage/actual drift, each new raw primitive's before/after failure and cancellation, overflow/short read/FD cleanup, post RO/NOEXEC changes with valid owned Stop, old receipts/prior/source mutations, and all unchanged raw/DTO/GC/transport/group-cleanup behavior. Ordinary assertions must reach the actual policy call paths; isolated helper echo tests are insufficient.

Use existing event-capture runners as read-only examples, adapting their paths/schema into new `impl/` evidence. Invoke only Python with clean explicit environment (`HOME=/Users/example`, `PATH=/usr/bin:/bin`, `LANG=C`, `TMPDIR=/private/tmp`) and `-I -B`, source imports by fixed paths. Never invoke real `host.py preflight/stage/actual/post`, `NativeGuest` on this host, real Lima/systemd or the ELF. Native primitive implementation can be exercised with patched library/OS ports only.

Exact full-suite shape (capture runner may select the same unittest modules programmatically):

```bash
/usr/bin/python3 -I -B .scratch/runtime-m1-deployment-exec-stage-b3c-1020/impl/runner.py
```

The runner must discover only the new `test_guest.py` and `test_host.py`, emit method/subtest events with unittest, return actual test success/failure, and preserve raw stdout/stderr, events, five-file hashes/frozen copies before+after and invocation/environment/exit receipts. Expected final full result: exit0, all methods/subtests pass, no skips or stderr warnings, source stable. Do not rerun a full suite after every focused edit. If a review changes source, rerun covering tests; final Root controller validation occurs only after source is stable.

- [ ] **Step 6: Self-review and report, then stop writing.** Save full implementation/TDD evidence with source hashes and concerns in `impl/task-1-report.md`. Preserve all failed attempts honestly. No commits. Return only status, one-line test summary and report path. After Root dispatches review, do not mutate source until a new fix assignment.

## ✅ Controller review and release

Root generates one diff package against the original five source files plus the new candidate, then dispatches one task reviewer for spec compliance and code quality. A separate broad reviewer evaluates the whole slice, the inherited reliability chain and final integrity evidence. No reviewer reruns the full suite solely to confirm reported results; concrete doubts can use narrowly scoped read-only tests. Fix all Critical/Important findings and track Minor findings with disposition; re-review amended code.

Root runs its own final captured suite once on stable final hashes, writes the final current/frozen manifests/release and a read-only verifier receipt, and seals the phase only after independent reviews and preservation checks. Store durable progress in the new phase `progress.md`; protect the historical `.superpowers/sdd/progress.md`. `git diff --check` and document checks apply without broad Go/PG/native windows. Final report separates offline success from an unexecuted new actual window and records persistent-stage implications.

Only after a concrete reviewed release and complete operation checklist exist may Root ask for the separate B3-C actual authorization. This implementation plan and acceptance of it do not grant it.
