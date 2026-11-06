# M1 Darwin Owned-group Convergence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the accepted Darwin EPERM compatibility gap, prove it with bounded local fixtures, and deliver a complete new reviewed kit/bundle without resuming guest deployment.

**Architecture:** Keep one private process supervisor in `prepare.py`. Only a retained terminal leader, Darwin EPERM, a complete exact group snapshot and repeated non-reaping identity checks may establish terminal convergence. Qualification uses the same verified-source helper; native validation and candidate building remain separate from guest execution.

**Tech Stack:** Python3.9 standard library, fixed Darwin64 libproc/waitid ABI, existing Go1.26.4 offline builder and pure qualification binding tests.

## Global Constraints

- Accepted spec: `docs/superpowers/specs/2026-10-23-m1-darwin-owned-group-convergence-design.md`, SHA256 `bd608f76cad24652b3e9ac938ede6536997dfb1d252331a351a145f6fa586dfa`; human `call_f3ca6ede418549e486c534c64745512e` accepted A, complete repair and bounded local validation.
- Fixed WT `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`, empty index SHA256 `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5`. No staging/commit/Git mutation, cleanup, branch or worktree changes.
- Evidence is only `.scratch/runtime-m1-darwin-group-convergence-1023`. Preserve every old phase/kit/bundle/receipt/mask/epoch/data and dirty path. Never edit `deploy/experience/switchback-capacity-publisher.service` or `internal/workerruntime/adapter/capacitypublisher/publish_test.go`.
- No guest/VM/Engine/service/PG/provider calls, deployment secrets/journal initialization, image build/pull, actual network/storage/partition operations, or retry/resume of the failed preparation. Existing storage and `m1_shared_volume_capacity_v1`, original300s barrier/240s qualification/120s Engine window, PG/Trust/reserve-before-permit/exact-CID one Start stay unchanged.
- Only Darwin plus original `killpg(owned_pid,SIGKILL)` EPERM plus a valid retained WNOWAIT terminal leader may attempt the new proof. Original errors and actual errno stay in evidence; never describe an unsuccessful signal as successful.
- Fixed `/usr/lib/libproc.dylib`, `proc_listpids(PROC_PGRP_ONLY=2, owned_pid, ...)`,8192-byte buffer/2048 32-bit PIDs; no public override. Clear ctypes errno before call. Only errno0, exactly4 returned bytes and the single PID equal to the still-retained leader are accepted. Reject zero, extra/duplicate/wrong/negative PIDs, full/truncated/unaligned/error results and unsupported ABI.
- Reobserve the same original child without reaping before and after the snapshot; preserve original deadline and terminal identity. ECHILD/reaped/drift or incomplete facts reject. Other platforms/errors/unproven terminal states remain fail closed. No extra signal after the original attempt, no untrusted old numeric PGID queries.
- No new public CLI/schema, extra bundle source input, privileged process or duplicate supervisor. Imports/self-check/fake tests load no actual native binding.
- Actual local fixtures only in Task2 after Task1 clean review and controller inspection of the harness: at most3 fixed kinds, each once, each≤5s, total≤30s, current non-root UID, `start_new_session=True`, no network/service, only the exact owned unreaped groups. Each child self-terminates within2s as an independent safety bound. Failure/unknown stops remaining fixtures; consumed kinds are never retried.
- Original fixed builder direct `python3 deploy/experience/qualification/build_candidate.py build`; no wrapper/phase/source/output override. It creates the next exclusive kit number under original `task-4/`. New bundle is exclusively in this phase `task-2/bundle-NNNN`, built through the existing library. All SourceRoot inputs finish before the final build.
- One scoped Spec+Quality review per task, required fixes in one owning-implementer wave and same-reviewer incremental recheck; one fresh final whole-slice review. No human check-ins between tasks. Never repeat an already-green broad suite/build without a source change, failure or specific unresolved concern.

## Task 1: Shared strict proof and offline caller regressions

**Files:** Modify `deploy/experience/engine/prepare.py`, `test_prepare.py`, `README.md`; modify `deploy/experience/qualification/README.md` and narrowly needed `test/acceptance/experienceprepare/test_qualification_window.py`. Narrowly extend `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go` final-kit test discovery as described below. No Go production changes.

**Interfaces:** Keep `_cleanup_process(process, selector, reader, terminal, deadline, monotonic, identity_lost=False)` unchanged. Add private `_darwin_group_reader()` returning a reader `pid -> {byte_count:int, errno:int, raw:bytes}` (raw is the full zero-initialized8192-byte buffer, including on errors) and `_prove_darwin_terminal_group(process, reader, terminal, deadline, monotonic, observe) -> dict`. Exact private names may be adjusted together for clarity; report names to Task2. All three existing callers continue sharing the original supervisor.

- [x] **Step1: Write caller-level RED regressions.** Extend the real normal-EOF fake seam in `test_prepare.py` so both `_bounded_run` and `_Host.transport` reproduce captured exit0+EPERM. A positive fake must supply a full sole-leader snapshot and repeated unchanged WNOWAIT records; assert completion and retained errno with `signalled=false`. Example core assertion:

```python
facts = {"byte_count": 4, "errno": 0,
         "raw": process.pid.to_bytes(4, sys.byteorder, signed=True) + bytes(8188)}
with mock.patch("prepare._darwin_group_reader", return_value=lambda pid: facts):
    result = run_existing_normal_eof_fake_with_eperm()
self.assertEqual(result.exit_code, 0)
self.assertIn("already_terminal", str(result.cleanup))
```

Use the existing actual receipt `.scratch/runtime-m1-resident-engine-execution-1023/guest-target-inventory.json` as the captured origin. Its missing old snapshot cannot be fabricated into real proof. Retain the original RED replay as historical; the new positive test supplies explicitly synthetic additional facts. Negative tests cover live/extra/zombie/duplicate/wrong members, zero/full/unaligned/truncated/negative counts, stale errno, ABI/library/reader errors, ECHILD/reaped/drift, non-Darwin, other errno, timeout, nonzero exit, output truncation and original failure preservation. Existing fake EPERM cases must patch the new native seam too, so they never query real PIDs or load libproc.

- [x] **Step2: Implement the fixed lazy reader and strict private proof.** ctypes uses the exact current SDK integer ABI, fixed path/argtypes/restype/use_errno, zero-initialized8192-byte buffer and errno reset. Emit the actual bounded raw return before validation, including negative/error/oversized results; never read beyond the buffer. The acceptance predicate includes:

```python
if facts["errno"] != 0 or facts["byte_count"] != 4:
    raise ValueError("owned group is not proven sole terminal leader")
if len(facts["raw"]) != 8192 or int.from_bytes(facts["raw"][:4], sys.byteorder, signed=True) != process.pid:
    raise ValueError("owned group leader mismatch")
```

Also validate exact types, raw length, supported ABI, original deadline, retained returncode and identical terminal records before/after. Use existing ctypes/sys/builtins without a new production import dependency. Hook only the Darwin EPERM terminal path. Preserve the failed signal and proof as separate truthful events; `already_terminal` is a convergence result, not signal success. Reap and verify the original exit exactly as before. The current kernel/SDK primary source is fixed in `.scratch/runtime-m1-resident-engine-execution-1023/diagnosis/primary-provenance.json`; no further guest diagnostics.

- [x] **Step3: Verify shared-loader and complete-candidate test integration.** Exercise qualification's actual fixed verified-source loader with fake native readers; no duplicate implementation or extra imports escaping its whitelist. Narrowly extend the existing Go final-kit binding test's bundle glob list to include `.scratch/runtime-m1-darwin-group-convergence-1023/task-2/bundle-*/manifest.json` as well as its original phase. Keep exact current-source and kit-digest matching and no runtime I/O. Do not broaden to arbitrary scratch roots or introduce an env/CLI override. This test legitimately skips before a new source-matching kit exists, then must PASS/no SKIP in Task2.

- [x] **Step4: Finish source docs and GREEN.** Document exact proof bounds, errno evidence, unchanged deadlines and remaining real validation. Focus first with `env PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/experience/engine -p test_prepare.py -v`; then once final source is stable run the full Engine `test_*.py` suite, qualification window16+affected regressions and static `prepare.py self-check`. Use the account-private Darwin TMPDIR, not `/private/tmp`, for Python's secure temporary-directory tests. Run Go capacitysnapshot affected tests once and compile its test package for Linux arm64/amd64 if the Go test file changed; Go environment uses `/private/tmp`, local cached dependencies, GOTOOLCHAIN=local/GOPROXY=off and unset SWITCHBACK_TEST_DATABASE_URL. Preserve every RED/failure/GREEN argv/env/exit/output and do not execute the compiled ELFs or any real native fixture here.

- [x] **Step5: Freeze, self-review and task gate.** Full report `task-1/task-1-report.md`, source mode/bytes/SHA freeze, no commit. Root produces the fixed dirty-baseline diff; fresh scoped reviewer checks Spec and Quality. Resolve required findings through this same implementer and incremental review before Task2. Finish all source/README/discovery changes before the next build.

## Task 2: Finite native validation and complete candidate delivery

**Files:** Create only phase-local `task-2/native-fixtures.py`, its fake/self-check harness if needed, command receipts, bundle build/audit helpers, report and pending future operation checklist. No product source edits. Concrete Task1 defects go back to its owner before proceeding.

**Interfaces:** Consume Task1's frozen private helper and original `_bounded_run`/non-reaping reader. Produce a durable once-only native claim/outcome, at most3 actual fixture records, a complete new original-builder kit, a17-input bundle, actual full-kit Go pure binding evidence and `task-2/task-2-report.md`.

- [x] **Step1: Build and inspect the native harness without running fixtures.** `self-check` only parses/validates fixed fixture code and deterministic fake budget/claim/error flows; it must load no native library or actual child. `actual` accepts no overrides, writes its exclusive claim before its first child, fixes total origin once and every per-fixture deadline to `min(total_deadline, fixture_origin+5)`, and records each kind before attempting it. No resume/retry or error-tolerant continuation. Report the ready harness and self-check proof to the controller; wait for its source/ownership/budget inspection before consuming any native kind.

- [x] **Step2: Execute each of the three allowed fixture kinds once.** Current-UID Python only, no networking/files beyond private test evidence, each child independently exits within2s. Kind1: a normally exited retained leader alone; expect native Darwin EPERM, exact `[leader]` snapshot, proof, exit0 and successful convergence. Kind2: a terminal leader with one same-group live child and closed ordinary pipes; expect group SIGKILL to reach the live member before reaping. Use a private inherited pipe with a fixed natural-exit marker to distinguish signal EOF from natural2s exit, avoiding queries/signals by old PGID after leader reap. Kind3: running process crosses the original short command deadline; verify original timeout stays failure and cleanup remains bounded. Use `_bounded_run` for caller coverage where suitable; a direct `_cleanup_process` seam is allowed for the inherited-pipe fixture. All native errors stop remaining kinds; retain partial output and consumed markers. This step is real local validation already authorized by the user, not a guest retry.

- [x] **Step3: Assemble and audit the complete new candidate.** Only after source freeze and native results pass, call original fixed builder direct with its offline cached environment. Use the next exclusive kit (expected0009; preserved failed attempts may consume numbers). Build `task-2/bundle-NNNN` through `build_bundle(kit,dest)` and verify it with `verify_bundle`. Full SourceRoot/TemplatesRoot current/copy/before/after equality, complete manifest coverage, Go tool hash,20 executable formats,43 exit0 build receipts,17 reverse-bound bundle inputs,13 runtime nulls and all authority flags false. Do not modify old sealed phases or insert downstream hashes upstream.

- [x] **Step4: Bind actual final artifacts and write full handoff.** Run only `go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run '^TestQualificationEngineDeploymentFinalKitBinding$' -count=1 -v` against the new actual kit/bundle; require PASS/no SKIP/runtime reads zero. No unaffected full-suite repeat after stable source. Produce the concrete next preparation checklist using the original complete effect scope, rebound to new manifests; old failed copies/preparation are not resumed. Record current native validation separately from still-unrun guest/systemd/Engine/PG/Task/provider work.

- [x] **Step5: Scoped review, fresh whole-slice review and final seal.** Root verifies artifact inventory and historical preservation, creates the Task2 package (phase-local harness/build/audit changes), then fresh scoped Spec+Quality review. Finish a fresh whole-slice review of the current source versus phase baseline plus both task reports and actual native evidence. Fix required findings in one owning wave; consumed native fixture kinds cannot be rerun without new authorization. Update progress/plan/append-only ledger, independently verify final source/artifacts/old seals/HEAD/index and seal the delivery. No guest environment action this slice.

## Execution note: stopped oracle check and remaining authorized kinds

The first native entry stopped after the sole-leader kind because its extra `waitid.si_uid == current_uid` oracle was wrong. The shared caller itself returned exit0 with exact sole-terminal proof and successful reap. Matching original XNU source `kern_exit.c:3370` to `:3410` zeroes siginfo and does not populate si_uid in this exit branch. Preserve the failed suite and its25-record native-window seal; never retry kind1 or resume its entry/claim.

The human-approved spec line46 authorizes three fixed kinds each once, each at most5s, total30s. Its text does not revoke unused-kind authorization after an offline harness correction. The remaining two kinds will use a separate fixed exclusive namespace `task-2/remaining-execution`, after controller inspection; this remaining batch has total10s and per-kind5s. Charge the consumed first kind its full5s, giving conservative cumulative15s below the authorized30s. No old PID/PGID is queried or signalled. Guest preparation still requires separate review under spec line48. This records the controller's implementation clarification and removes an unnecessary fresh-human-confirmation assumption; it does not change permissions or retry consumed work.

## Execution note: bounded negative timeout adjudication

All three fixed local kinds have now been consumed exactly once; no further native execution is permitted in this slice. Kind2 passed. Kind3 preserved the original timeout and returned within its fixture bound; its evidence-only1ms wait reported TimeoutExpired and exit remained null. The outer fixture incorrectly required proven reap. Scoped native-gate review found this requirement exceeds Task2 Step2, identified no product defect, and requested pure offline factual adjudication plus focused existing-raw replay. Preserve both original FAILED suites and the25/33-record seals; unknown reap stays unknown. Same-reviewer confirmation of that correction precedes the original complete candidate build. The final Task2 report is a new `task-2/final-delivery-report.md`; both earlier stop reports remain immutable. This clarification changes no production semantics, authorization, native budget or SourceRoot.
