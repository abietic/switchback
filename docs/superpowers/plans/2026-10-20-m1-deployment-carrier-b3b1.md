# M1 B3-B1 Offline Deployment Carrier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the fixed four-mode carrier, its offline fault coverage, and read-only verifier without entering a guest or granting runtime authority.

**Architecture:** Standalone guest source implements finite filesystem and manager operations; host source binds only the prepared VM, source and ELF and captures bounded transport evidence. Tests drive private raw primitive ports. The verifier checks stored bytes and evidence without running operational modes. One coherent task owns the five tightly related files and one end-to-end offline contract.

**Tech Stack:** Existing Python3 stdlib, compatible with host Python3.9 and guest Python3.12; no dependencies. Existing prepared Linux arm64 Go executable is immutable data, never executed during this task.

## Global Constraints

仅在新实施phase新增五个操作代码文件：`guest.py`（固定四mode与有限IO/manager原语）、`host.py`（固定Lima/清洁环境/source绑定/有界传输与raw receipts）、`test_guest.py`、`test_host.py`、`verify.py`（只读复核器）。其余manifest/fixture/报告仍放同phase。无产品Go、部署模板或既有文件修改，不安装依赖，不运行任何真实Lima shell、systemd、Engine或PG。

私有fixture端口注入原始stat/read/process/transport响应；实际main只能构造固定native端口。host公开mode只有preflight/stage/actual/post；任何额外VM/path/command/env selector拒绝。预算、DTO schema and field rules、命令、unit/property和ELF只来自本准备phase中哈希绑定的固定清单，不能由调用者或fixture结果覆盖。

离线先独立验证合法原语fixture，再跑可编译/可解释的行为RED，随后GREEN。覆盖每个有限原语的before/after故障/取消、limit+1及短写、源/ELF/boot漂移、重名与symlink、raw exit2保留、manager状态/InvocationID/cgroup/Start身份不符、actual一次消费、transport timeout/outer与nested不一致、清理ownership不确定以及本地kill不等于远端结束。所有成功与拒绝路径必须保留可核对raw回执，不合成native success。

The direct human approval is B3-B1 offline implementation only. No real Lima invocation, guest command, systemd operation, VM lifecycle, Engine/PG/provider, privilege grant, key/Trust/journal provisioning, Go execution, Git mutation or native ELF execution. Only controller may later request a distinct actual window after concrete reviewed implementation.

Work only in `/path/to/switchback`. Protect all964 prior inputs and1770 Git-visible files, including `.superpowers/sdd/progress.md`. New formal documentation is this plan only. All five source files, tests and progress/evidence live under `.scratch/runtime-m1-deployment-carrier-b3b1-1020/`. No commits/staging. Use the phase ledger only.

Approved design: `docs/superpowers/specs/2026-10-20-m1-deployment-diagnostic-native-window-design.md`, SHA256 `4c97b052f224cf379588efbb1f8ca25cdb3f2d948ae12d3834c81f39210449ab`. Immutable preparation phase `.scratch/runtime-m1-deployment-native-prep-1020/`, seal44 artifacts SHA256 `05d2aeb15d6b45b97fc3fe72fbb43c238710ba1c4689e11a0125f971f88a1701`. It remains read-only.

---

### Task 1: Finite guest/host carrier, verifier and offline contract

**Files:** Create only `.scratch/runtime-m1-deployment-carrier-b3b1-1020/{guest.py,host.py,test_guest.py,test_host.py,verify.py}`. Do not add shared operational modules; keep helper responsibilities within these five. Fixtures/manifests/reports may be new files in this phase. Root owns plan, baseline, final evidence/seal; implementer owns five files and offline Python test window until STOPWRITING.

**Interfaces and fixed values:**

- Private guest entry `_dispatch(mode, request, io)` returns a bounded JSON-compatible diagnostic envelope. `main` accepts exactly one mode from `preflight|stage|actual|post`; concrete raw backend construction is fixed and unsupported platform is rejected before native IO. Private test seams may inject raw metadata/command/clock responses, never a high-level success DTO or production expected-value options.
- Host private `_run(mode, io)` assembles only fixed argv/environment, validates prior raw evidence, records transport. Actual `main` accepts exactly one of the same four modes; no output/root/path/VM/command/expected/env switches or environment configuration. Do not execute it in tests.
- Python names for the private backend types/methods may follow local clarity, but retain the two named entrypoints for test and controller navigation. No mutable global test hooks.
- The source may safely share pure parsers from guest/verify via explicit fixed-path source loading AFTER hash checks. Do not rely on ambient module search: `python -I` omits the script directory. Guest source is standalone stdlib source suitable for `python3 -I -B -c <guarded source> MODE`.
- Prepared candidate values are normative: `.scratch/runtime-m1-deployment-native-prep-1020/design/execution-candidate.json`; copy exact property/argv data after checking its prep seal. Do not weaken values for the synthetic fixture.
- VM=`switchback-core`; launcher=`/Volumes/SwitchbackCore/toolchain/lima-2.2.1/bin/limactl`; LIMA_HOME=`/Volumes/SwitchbackCore/lima`; phase_nonce=`1fc831e9-6507-4c1e-8b5d-a770688a818b`.
- Guest stage=`/run/switchback-deployment-probe1020-b3b`; unit=`switchback-deployment-probe1020-b3b.service`; exact own cgroup=`/system.slice/switchback-deployment-probe1020-b3b.service` and filesystem path under `/sys/fs/cgroup` only. Reject manager-provided alternate paths.
- ELF immutable path=`.scratch/runtime-m1-deployment-native-prep-1020/artifact/switchback-deployment-probe-linux-arm64`, size5640057, sha256=`a1309c87bc3868f275f05e4ec42c853f7b57c7c2c356ac616e94d0f769a62d27`, ELF little-endian machine183. Limit8MiB; copying/reading/hashing is allowed, executing is forbidden in this task.
- Actual runtime evidence must go in future fixed `.scratch/runtime-m1-deployment-native-window-1020/`; this directory must NOT be created during B3-B1. Offline tests use explicit private fixture IO/new fixture directories in the implementation phase. Root release verifier is read-only.
- Mode budgets30/45/45/30s include owned child cleanup, use monotonic deadlines. Host stdout envelope<=256KiB, host stderr<=64KiB, each guest command response<=64KiB, probe stdout/stderr files<=32KiB, marker<=2KiB, request<=12MiB (base64 ELF plus bounded header), bootstrap source<=64KiB. Always read limit+1 and fail on overflow; do not classify partial captures as complete evidence. Service settings remain exact prepared values, including Startup10s/Stop5s, not a claimed hard deadline.
- Clean host transport env contains only fixed HOME, PATH, LANG=C and LIMA_HOME; no preserve-env/start/sync. Production argv: fixed limactl `--tty=false shell --workdir=/ switchback-core -- sudo -n /usr/bin/python3 -I -B -c <hash-bound guest source> MODE`. No shell interpolation. Source/binary/prep hashes are checked before dispatch; recorded host argv remains exact public bytes.
- Wire request is duplicate-key rejecting JSON with exact version/mode/phase_nonce fields, an expected boot binding (empty only for preflight), prior stage identity when needed and optional actual InvocationID from owned receipts; stage alone carries the base64 exact ELF. These are continuity comparisons against real reads, never replacements for observed success. No caller CLI supplies wire values. Reject unknown fields/types/oversize. Decide the remaining stable envelope key names in code and document them in the implementation report.
- Every reply is diagnostic only, not a permit/Trust/manifest or publisher verification receipt. Preserve original command exit/code and bounded raw command stdout/stderr separately from semantic status. Fixed public reason tags/errno, no raw syscall exception strings or synthesized success.

**Read first:** Approved design and prep candidate JSON. CodeGraph already current after7-file sync; source snippets from the B3-A observer are in `controller/codegraph-probe-source.txt`. B3-A success namespace Class is exactly `observed`, errno0, decimal device/inode; denied Class exactly EACCES/EPERM with errno13/1 and empty IDs. Do not guess `success` as a class.

- [ ] **Step 1: Create compiling/rejecting scaffold and qualify independent raw fixtures.**

Create all five source files with real parsers and private raw ports but rejecting orchestration. Use `ast.parse`/import checks to prove they interpret; SyntaxError/import failure is never RED. Fully qualify a raw fixture before dispatch behavior: Linux/aarch64, Python supported, systemd major255 version stdout, canonical nonzero boot UUID, actual-root metadata for fixed ancestors, `/run` tmpfs with>=16MiB available, missing stage/unit, active system.slice. Use raw systemctl property text, not validator output. Include complete independent B3-A JSON fixtures for both all-observed exit0 and stable-denied exit2, plus real prepared ELF bytes for valid stage binding.

Example test assertions (use actual parser names chosen consistently):

```python
class GuestFixtureTests(unittest.TestCase):
    def test_fixture_qualified(self):
        data = fixture_raw_diagnostic()
        self.assertEqual(data['schema'], 'switchback.capacity.deployment_diagnostic.v1')
        self.assertEqual(data['kind'], 'diagnostic_only')
        self.assertEqual(data['caller_before']['pid'], 301)
        self.assertEqual(data['caller_before']['tid'], 307)
        self.assertEqual(len(data['namespaces']), 8)
        self.assertEqual(len(prepared_elf()), 5640057)
        self.assertEqual(hashlib.sha256(prepared_elf()).hexdigest(), ELF_SHA256)
```

The fixture must explicitly validate ALL selected manager/proc/metadata/schema fields independently. Snapshot all five source files plus raw test receipt before moving on. Emit JSON test events through controller-compatible unittest result instrumentation; each method and subtest pass/failure identified separately. Stream to stdout so test stderr remains a real error channel. An inline `python -I -B -c` runner is allowed; store its exact source/argv in evidence, no sixth operational source file.

- [ ] **Step 2: Add and run executable behavior RED for both sides.**

Guest complete preflight/stage/actual0/actual2/post tests assert exact raw continuity and no authority fields; host test must assert exact fixed launcher/env, receipt source+wire hashes and outer/nested separation. Keep rejecting orchestration until RED is captured. For example:

```python
result = _dispatch('actual', valid_request(), fixture_io())
self.assertEqual(result['status'], 'collected')
self.assertEqual(result['exec_main_status'], 2)
self.assertEqual(result['diagnostic']['namespaces'][4]['before']['errno'], 13)
```

`status`/reply field naming above is illustrative and may be made consistent within the code; the assertions must check actual values and call ordering, not merely absence of error. Run selected guest and host tests with distinct evidence labels; expect normal unittest failure after interpretation succeeded. Preserve common RED source snapshots/hashes and failing assertions. No mode is dispatched to real IO.

- [ ] **Step 3: Implement finite guest state machine and concrete backend.**

Preflight: root Linux/aarch64; fixed boot raw read; fixed version commands; held no-follow `/`/`run` dirs with type/owner/mode checks, statfs TMPFS and statvfs available>=16MiB; exact stage and unit absence (manager load state + fixed possible unit file locations), already active system.slice. Failure only reports facts; no install/restart/repair. Unknown/malformed manager response is not `not-found`.

Stage: reread stable boot; exclusive no-follow private0700 stage and files; descriptor ownership registered before late cancellation checks. Write exact ELF completely in finite chunks, reject short/error writes, chmod0500, fsync/recheck same inode/content, create empty diagnostic files0600. No symlink, overwrite, recursive cleanup or alternate path. Preserve partial owned stage if a fault occurs and report incomplete state; never pretend rollback deleted it. Return raw stage identity and artifact binding.

Actual: host attempt is consumed before any actual-mode failure can be retried. Guest revalidates boot, held/canonical stage, ELF digest/type/owner/mode and exact prior identity; exclusive guest actual.once is consumed before systemd Start. Both markers bind nonce/boot/ELF/unit. A late error that returns an owned FD still requires one close. Execute exact prepared launch argv once; failure/refusal/timeout/unknown delivery is consumed, no retry/reset or new path. Read selected exact manager properties and output files with bounds and metadata rechecks regardless of launch result where safe; record failures explicitly. Validate command strings against the fixed argv/property policy using typed normalization, not trusting arbitrary manager strings to select effects.

Validate raw B3-A DTO independently: exact fields/schema/kind; strict JSON with duplicates/nonfinite numbers rejected; timezone-aware RFC3339 window strictly advances, nonzero canonical boot matches actual window, before/after process equal, uint bounds/cap16lowerhex/NNP/Seccomp/cgroup grammar, exact8 owner-kind pairs, stable namespace values, canonical decimal uint64 identities and `observed`0 or EACCES13/EPERM1 only. Exit0 requires no denial; exit2 at least one stable denial; required error/partial JSON is not a valid complete observation. Require caller PID/TGID agree with manager ExecMainPID and cgroup equals the own fixed unit. TID307 need not equal leader PID301. Do not compare caller-thread stat ticks to the leader's stat ticks. Do not infer publisher compliance from any result.

Post: always preserve available raw result/manager evidence before effects. Stop only the new unit after nonce/stage/ELF/ExecStart/InvocationID bindings prove ownership. A missing InvocationID/unknown ownership cannot authorize stop. Recheck MainPID0 and fixed own cgroup empty; if a recorded caller thread remains, read its bounded stat via validated numeric PID/TID and compare the correct thread start ticks. A reused PID with different ticks is distinct; not-found alone is not proof of completion. Unknown termination stays unknown. Never stop/reset/mask/restart any old unit or remove old/new evidence. Native operations and command budget checks are cooperative and finite; no detached syscall workers or error retries. Actual source access to Python libc statfs is fixed to Linux64 ABI (supported aarch64) and unsupported platforms fail before it is called.

- [ ] **Step 4: Implement fixed host transport, evidence ownership and verifier.**

Host mode validates previous mode receipts from the future fixed execution phase; import and tests create none of it. Preflight rechecks frozen VM config/host metadata and tool/ELF/source hashes; real Lima list/shell are only native code paths, never called during this task. Preserve fixed source inputs and prior evidence bytes per dispatch. For stage, wire contains only the exact prepared ELF, not caller-selected bytes.

Concrete transport uses Popen argv list, clean env, new owned process group, bounded concurrent stdin write/stdout+stderr draining with selectors/nonblocking pipes and one monotonic budget. Handle overflow, cancellation, Popen failure, partial input/output, late exits, EOF and exit-code mismatch. Attempt termination of only the owned local process group within remaining budget, record whether local reaping was proven; never set remote termination from local kill. No shell=True, subprocess.run(capture_output=True) unbounded transport, global env inheritance, blind communicate retry or unbounded wait.

Every mode writes fresh per-attempt receipt/raw/source hashes and exact argv/times, consumes actual locally with O_EXCL even on later source/transport refusal, and never overwrites an attempt. Receipt distinguishes outer launcher exit, guest envelope result and nested systemd/probe raw exit. Validate guest wire/nonce/boot/mode rather than trusting a generic `ok` field. Store complete vs overflow/partial evidence honestly. Source/native facts are never accepted from CLI config.

`verify.py` reads fixed phase baseline/prep bindings and `release-manifest.json`; no args, native IO, operational imports with side effects, Go/Lima or service execution. It validates before/current/frozen sources, saved test receipts/logs/snapshots, final source hashes, review artifact bindings and phase seal (if present). Read-only Git metadata commands with GIT_OPTIONAL_LOCKS=0 are allowed. Missing release data before controller finalization must be a fixed verification failure, not an invented ready result.

Root will write `release-manifest.json` after clean reviews. Agree on this exact core contract: `schema='switchback.b3b1.offline_release.v1'`; `source_files` array of path/kind/bytes/sha256/frozen_ref for all five files; `test_runs` array of receipt path, role fixture/red/green/controller and expected_exit; `reviews` array of artifact facts; `unresolved` counts critical/important/minor; `native_executed=false`, `guest_entered=false`, `capabilities_granted=false`. Each test receipt contains argv, env, exit_code, before/after five-source SHA256, logs facts and structured unittest events. Verifier derives counts from raw events and does not treat test count or review JSON as deployment authority. Additional non-authority metadata may be added consistently; report it to Root.

- [ ] **Step 5: GREEN, fault/concurrency coverage and self-review.**

Test guest required manager fields missing/duplicate/type/range/normalization, exact properties including transient/oneshot/Remain/limits, raw ExecStart identity, malformed/denied DTO exit mismatch, PID/TID/cgroup/boot mismatch, proc-thread stat parsing with parentheses, all preflight drift, stage short writes/replacement/wrong owner/symlink/partial state, marker repetition and every point before/after actual Start, failure evidence saved before owned-stop, unknown/reused ownership, absent vs empty cgroup, reused PID and unknown remote termination.

Replay every finite IO and command failure/cancellation before/after on a complete primitive trace, including a returned owned FD alongside late error. Verify zero unauthorized subsequent effects, exactly-once FD close and maximum finite limits. Host covers exact argv/env/no selectors, source drift, consumed-on-failure attempts, no overwrite, stdout/stderr limit+1, partial stdin/write/read, cancellation/timeout/termination, outer0+nested-failure, outerfailure+nested-success, malformed/replayed guest envelopes and local-kill-does-not-prove-remote. Use isolated fake processes/pipes/small temporary files only; no real service process/ELF/Lima.

Test verifier parsing/integrity refusal through isolated fixtures and pure helpers inside test_host.py. Production binding/absence of shell or mutable hooks should be checked by AST and explicit argv assertions, not string-only proof. Assert concurrent private instances do not share counters/FDs/files; no new process group or filesystem write occurs on import or invalid CLI args. Private canary exception text cannot enter public fixed errors or semantic success bodies; bounded command stdout/stderr is retained separately as raw evidence.

Use only existing Python and stdlib. Example final command (controller inline JSON runner may replace plain runner for receipts):

```sh
/usr/bin/python3 -I -B -m unittest discover -s .scratch/runtime-m1-deployment-carrier-b3b1-1020 -p 'test_*.py' -v
```

Run focused tests while iterating, one full task final after self-review. Store each attempt under a new impl label, preserve raw events+stdout/stderr, exact five-source snapshot and receipt; never overwrite RED or fixture. Do not rerun existing Go suites because no Go source changes. No new package fetch, real Linux native validation or actual markers in the future execution phase.

- [ ] **Step 6: Hand off for two independent reviews and controller verification.**

Write `impl/task-1-report.md` with five-file responsibilities, interfaces/protocol decisions, exact fixture/RED/GREEN receipt/source snapshots, actual counts (methods and subtest records separately), self-review and operational limits. Return DONE/concerns, no commits, short test summary and source+Python STOPWRITING. Root creates a full five-file /dev/null diff against captured baseline, runs task spec+quality review and a broad final source/operational review. Findings get one tested fix wave and rereview; no real operational mode can run.

Controller final: one clean complete Python suite against final hashes, distinct focused repeated/concurrent isolation check only if that resolves the concrete host/guest shared-state risk, AST/compile-source parse check, readonly verifier and before/current/seal checks. No mandatory Go rebuild/test repeat. Freeze source, raw receipts, reviews, operation-readiness list and phase seal. Only after source/offline delivery can a later actual-window authorization be requested; do not perform or claim it here.
