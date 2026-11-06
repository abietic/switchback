# PID 1 Zero Starttime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Accept an observed zero starttime only for explicitly bound PID 1, and align a new offline success mapper without changing existing deployments.

**Architecture:** Keep the shared pure poolProcess parser and all identity checks. Narrow one zero-value guard; diagnostic checks already reuse this parser. Derive a new pure Python mapper candidate from the frozen B3-F baseline and change only the PID 1 starttime rule.

**Tech Stack:** Go 1.26.4, Python 3 standard library, existing procfs fixture backends.

## Global Constraints

- Approved spec: docs/superpowers/specs/2026-10-21-pid1-zero-starttime-fix-design.md, approved bytes SHA256 8f54d7f2a9db06d30d29383d4f92f0e46d8e43780b0380828794e0a890a05c81.
- Zero ticks are accepted only when expected PID=1 and actual PID=1 has passed the earlier binding check. Other PIDs and expected PID=0 retain positive ticks.
- No API/DTO/schema changes, no fabricated ticks, no budget/Trust/PG/Start/IPC/publisher/capability changes.
- No guest/VM/unit/stage/Engine/PG/provider access. Linux binaries are test compile-only; never execute them or build deployment ELF.
- Preserve all frozen scratch directories, snapshots, seals, once markers and old ELF bytes. New mapper/test/log files live only in /path/to/switchback/.scratch/experience-progress-2026-10-21/fix-candidate.
- Keep HEAD fbebfe754045bdbf1af40871a5018eed33743011, branch and empty index unchanged. No stage/commit/push/reset/clean.
- All Go verification uses GOPROXY=off, GOSUMDB=off, GOTOOLCHAIN=local, CGO_ENABLED=0 except race requires CGO_ENABLED=1. Cache/tmp must be fresh under /private/tmp and only owned temp directories may be removed.
- Python execution uses -I -B with explicit candidate sys.path; never create pycache in frozen directories.
- Record the previous raw replay RED evidence; final identical raw replay must become GREEN. Native success remains unproven.
- Write ownership: Task 1 owns only pool_native.go, deployment_parse_diagnostic_test.go and new pid1_zero_starttime_test.go; Task 2 owns only new candidate Python files. Controller owns plan/ledger/baseline/logs.

### Task 1: Shared parser and deployment regression

**Files:** Modify internal/platform/capacityprobe/pool_native.go and deployment_parse_diagnostic_test.go. Create internal/platform/capacityprobe/pid1_zero_starttime_test.go. No other existing source edits.

**Interfaces:** Consume poolProcess(rawStat, rawGroup []byte, pid uint64) (uint64,string,error), newDeploymentFixture(), observeDeployment(ctx,k), observeDeploymentParseDiagnostic(ctx,k). Produce the same interfaces with only the exact PID 1 zero-value acceptance.

- [x] Add a failing focused regression before changing the guard. A minimal positive example is:

```go
func TestPoolPID1ZeroStarttime(t *testing.T) {
    raw := bytes.Replace(deploymentFixtureStat(1), []byte("18 99 20\n"), []byte("18 0 20\n"), 1)
    ticks, group, err := poolProcess(raw, []byte("0::/init.scope\n"), 1)
    if err != nil || ticks != 0 || group != "/sys/fs/cgroup/init.scope" {
        t.Fatalf("ticks=%d group=%q err=%v", ticks, group, err)
    }
}
```

Also cover non-PID1 zero, expected PID=0 with actual PID1 zero, mismatched/zero actual PID, bad/negative/overflow/missing ticks and bad cgroup using the same real parser. Positive nonzero and exact maximum uint64 parsing keep their existing meaning.

- [x] Run focused Go tests, record actual RED with the existing guard. Use fresh cache/tmp and a process-only fixture. Existing captured raw replay RED is in ../replay-red.stdout.
- [x] Apply the minimal guard plus a short reason comment:

```go
// PID 1 may start in tick zero; retain it only for an explicit PID 1 binding.
if e != nil || (ticks == 0 && pid != 1) {
    return 0, "", ErrPoolInvalid
}
```

The earlier actual PID match must remain unchanged.

- [x] Add full observer fixtures: all PID1 stat reads with zero must succeed and preserve the value; second read 0→1 and canonical third read 0→1 must fail and close owned descriptors. Use f.mutate on "read:leaf:1:1" with its occurrence n; caller/other process facts remain baseline. Use the existing fixture cleanup and parity assertions. Confirm the B3-G check still agrees with the collector: a later identity drift may have accepted/accepted checks; caller zero remains rejected.
- [x] Replace the previous B3-G PID1 "zero-start" rejection fixture with malformed ticks (for example "-1") so it remains a process-rejected case, then add actual zero success coverage. Do not remove the rejected-case assertion or change the parser checks API.
- [x] Run focused new tests and existing related deployment/pool pure tests; record GREEN and gofmt touched Go files. Full relevant selector excludes TestPoolEngineTransport and all external/integration profiles.
- [x] Write Task 1 report, source hashes and logs in candidate directory. Self-review no semantic changes outside the guard. Do not commit.
- [x] Controller obtains read-only task review for both Spec and Quality; resolve findings before Task 2.

### Task 2: New offline mapper candidate

**Files:** Create /path/to/switchback/.scratch/experience-progress-2026-10-21/fix-candidate/output_mapper.py and /path/to/switchback/.scratch/experience-progress-2026-10-21/fix-candidate/test_output_mapper.py. Baseline is .scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021/output_mapper.py and its test_output_mapper.py. Do not edit the baseline.

**Interfaces:** Consume and retain assess_output(stdout: bytes, stderr: bytes, expected_boot: str) -> dict. Preserve fixed cgroup/unit baseline strings for pure fixtures only. Produce a diagnostic-only, authority=none assessment with exact original packet semantics except PID1 zero acceptance.

- [x] Copy both baseline files to the candidate directory, preserving baseline hashes in report:

```python
shutil.copyfile(baseline / "output_mapper.py", candidate / "output_mapper.py")
shutil.copyfile(baseline / "test_output_mapper.py", candidate / "test_output_mapper.py")
```

- [x] Add tests first for equal before/after PID1 ticks=0 success; caller zero rejection; zero combined with wrong PID/TGID/TID; booleans, negatives, overflow and 0→1 drift rejection. Confirm RED on copied original mapper.
- [x] Change only process-field validation:

```python
for key in ('pid', 'tgid', 'tid'):
    uint(before[key], positive=True)
uint(before['start_time_ticks'], positive=(owner != 'pid1'))
```

Earlier exact typed before/after comparison and subsequent exact PID/TGID/TID checks remain mandatory. Update only baseline tests that treat PID1 zero alone as invalid; keep caller zero and all other invalid packets rejected.

- [x] Run the complete candidate pure mapper test file and the frozen original mapper suite with -I -B. Assert no old bytes or pycache changes. New candidate keeps the old fixed identity for fixtures and is not an executable native candidate.
- [x] Write Task 2 report and exact baseline/candidate diff in candidate directory. Do not commit.
- [x] Controller obtains read-only task review (Spec and Quality).

## Controller completion

- [x] Run the unchanged captured raw snapshot replay; all three controls must now pass, with the first RED log retained.
- [x] Run relevant deployment/pool parser + both CLI pure tests and the candidate/old mapper suites; race the related Go scope. Compile API/new CLI tests for Linux amd64/arm64 without executing.
- [x] Final whole-change review reads the current increment diff and test evidence. Root resolves all actionable findings.
- [x] Compare original Git-visible baseline: only two existing Go files may change; new plan/new Go test are expected additions. Compare frozen historical artifacts; changed current source bindings in old G seal are recorded as intentional differences, never rewrite a seal.
- [x] Update current progress with fixed/offline-verified status and remaining native/Create/quota/Start/product milestones. Remove owned temp caches, retain small test evidence. No new approval question is required within this accepted scope.

