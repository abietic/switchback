# M1 B3-A Deployment Diagnostic Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fixed read-only, unsigned deployment diagnostic with portable offline tests; retain the two unavailable production gates.

**Architecture:** A value-only observation model and private finite backend separate proc parsing and stability checks from fixed Linux syscalls. One observation owns its thread pin and descriptors. A zero-argument command reports complete diagnostics, including stable namespace permission denials, without granting deployment authority.

**Tech Stack:** Existing Go toolchain, standard library, cached golang.org/x/sys/unix; Linux amd64/arm64 concrete backend, unsupported stub elsewhere. No dependency changes.

## Global Constraints

生产函数自行构造 concrete Linux backend，不接收 PID/path、expected 值、权限、文件内容、clock 或 verifier options。仅支持 Linux amd64/arm64；nil/cancelled context、unsupported 平台或必要读回失败返回固定错误与零 Observation。

`DeploymentObservation` 是 unsigned diagnostic DTO。其 JSON schema 固定为 `switchback.capacity.deployment_diagnostic.v1`，`kind=diagnostic_only`；不含 Trust、Guarantee、manifest/receipt、ready、permit 或可消费的 deployment verification token。

status 单次最多64KiB，stat/cgroup 各4KiB，boot64 bytes，cap_last_cap32 bytes；均检查第 limit+1 字节。固定8个 namespace 项、16次逻辑访问，每次成功包含上述有限stat/open/identity/type检查，无截断、重试循环或 detached syscall goroutine。固定两份 proc directory FD，同步临时 leaf或namespace FD最多一个；每个 owned FD 在错误和取消路径只关闭一次。命令使用5秒 cooperative context；不承诺内核调用硬5秒返回。

诊断不预先要求caller UID=0、NoNewPrivs=1或caller与PID1 namespace相同；这些均保留实际读回。它可以完整记录一个与预期unit不同的principal，仍没有“该部署符合要求”的含义；未来verifier负责受信任expected值及同principal匹配。

上述路径均相对唯一工作树 `/path/to/switchback`。本片只新增七文件；不修改 B2 publisher/Backend gate、service模板、Mapper、签名/raw、PG/Start、worker/observer、Compose 或任何既有数据。没有在生产 gate 接入 ObserveDeployment；接入是后续详细设计事项。

No VM/Engine/Worker/provider/real PG, Linux/native probe execution, service install/start, capability grants, key/journal provisioning, integration tags, dependency fetch or Git mutation. This direct approval authorizes source/offline work only. Existing 955 inputs and 1761 Git-visible files must remain byte-identical. The only additional documentation is this plan; evidence goes in `.scratch/runtime-m1-deployment-b3a-1020/`.

---

### Task 1: Fixed deployment observer and diagnostic command

One coherent deliverable: the unsigned observer's fact/stability contract and the command's status mapping have one test cycle and one task review. No independent deployment policy task is included.

**Files:**
- Create: `internal/platform/capacityprobe/deployment_types.go`: DTOs and fixed sentinel errors only.
- Create: `internal/platform/capacityprobe/deployment_observe.go`: private finite backend, required parsers, bounded observation/stability checks.
- Create: `internal/platform/capacityprobe/deployment_observe_linux.go`: fixed proc/uname and thread/FD ownership implementation, Linux amd64/arm64 build constraint.
- Create: `internal/platform/capacityprobe/deployment_observe_other.go`: complementary unsupported build constraint.
- Create: `internal/platform/capacityprobe/deployment_observe_test.go`: portable primitive fixtures, behavior/fault/privacy/concurrency tests.
- Create: `cmd/switchback-deployment-probe/main.go`: fixed main and private command runner.
- Create: `cmd/switchback-deployment-probe/main_test.go`: portable output and exit tests.

**Interfaces:**
- Produces `func ObserveDeployment(ctx context.Context) (DeploymentObservation, error)`; no other exported factory or mutable hook.
- Value DTOs: `DeploymentObservation` with schema/kind, Started/Ended, BootBefore/BootAfter, KernelRelease, CapLastCap, CallerBefore/CallerAfter, PID1Before/PID1After and `[8]DeploymentNamespaceObservation`.
- `DeploymentProcessObservation`: PID (TGID process identity), TGID, TID, StartTimeTicks, `[4]uint32` UIDs/GIDs, five named 16-character capability strings, NoNewPrivs, Seccomp, unified cgroup path. Caller status Pid is TID, Tgid is getpid; PID1 status Pid/Tgid are 1. Do not confuse caller TID with leader.
- Namespace entry has fixed owner (`caller_thread`/`pid1`), fixed kind (`user`/`pid`/`mnt`/`cgroup`), Before/After values. Each value has canonical decimal Device/Inode strings and errno/class; success is errno0/nonempty values; EACCES/EPERM denial has empty values. Exact numeric errno/class must agree.
- Sentinel errors use fixed text for unavailable/invalid/cancelled/unsupported; no path/raw source/error wrapping in public failures. No dumpable field.
- Private backend exposes only finite raw IO/clock primitives, per-call instance ownership, no observed success DTO factory. Keep parsing/identity/nsfs proof rules in portable orchestration where practical so fault tests cover decisions. Concrete backend has fixed leaf enums/targets and no arbitrary path input from production callers.
- CLI private observer injection is for tests only; actual main binds `capacityprobe.ObserveDeployment` directly. No mutable global.
- Reuses existing pure `poolProcess`/`poolUnified`/`createdLayerDecimal` rules without changing their files. Canonical cgroup output may retain existing `/sys/fs/cgroup` prefix consistently; do not duplicate a full parser.

**Read first:** Approved `docs/superpowers/specs/2026-10-20-m1-deployment-diagnostic-b3a-design.md` is normative, SHA256 `902ab7df964399204768562bfc8c812adeb2ce969192accc2099fccba79ce0f5`. Existing source relevant to pure parsing is `pool_native.go` (`poolProcess`, `poolUnified`) and Linux primitive conventions are `pool_native_linux.go`. Use current CodeGraph before source navigation. Prior phase is immutable.

- [ ] **Step 1: Qualify primitive fixtures independently before executable RED.**

Create all seven files with compiling types and rejecting observer/command scaffold. Implement pure required-field parser and independent finite primitive fixture data before behavior. Fixture uses actual textual proc grammar, comm containing spaces and parentheses, caller PID/TGID/TID such as 301/301/307, PID1 1, nonzero ticks, all UID/GID tuples and all five capabilities; known boot UUID and strictly advancing wall clock; independent fixed namespace facts on procfs and nsfs. Its fixture test must pass while the observer still rejects. Validate individual status fields and reuse stat/cgroup parsers. Preserve a source snapshot and raw receipt for this exact stage. Example test shape (use the actual private names chosen in the source):

```go
func TestDeploymentFixtureQualified(t *testing.T) {
    raw := deploymentFixtureStatus(307, 301)
    got, err := parseDeploymentStatus(raw)
    if err != nil || got.TID != 307 || got.TGID != 301 {
        t.Fatalf("fixture identity: %#v %v", got, err)
    }
    ticks, group, err := poolProcess(deploymentFixtureStat(307), []byte("0::/fixture\n"), 307)
    if err != nil || ticks != 99 || group != "/sys/fs/cgroup/fixture" {
        t.Fatalf("fixture stat/cgroup: %d %q %v", ticks, group, err)
    }
}
```

Use hermetic runner, existing cache only:

```sh
python3 .scratch/runtime-m1-deployment-b3a-1020/run-offline.py task1 fixture /usr/local/go/bin/go test -json -p=2 -count=1 ./internal/platform/capacityprobe -run '^TestDeploymentFixtureQualified$'
```

Expected exit0 and fixture pass; compile failure is never RED evidence.

- [ ] **Step 2: Run compiled behavior RED against the rejecting scaffold.**

Add complete observer assertion and CLI full-observation status0 assertion (plus stable denied status2). Assert exact schema, actual values, fixed 8/16 namespace observations and retained nonroot/NNP0/mismatched namespace facts, not just non-error. A representative hard assertion is:

```go
if err != nil || got.Schema != "switchback.capacity.deployment_diagnostic.v1" || got.Kind != "diagnostic_only" {
    t.Fatalf("complete diagnostic: %#v %v", got, err)
}
```

Run new tests using distinct runner labels `red-observer` and `red-command`; expect behavior failures with build succeeded. Freeze seven source files at RED and the exact failing assertion; do not overwrite attempts.

- [ ] **Step 3: Implement bounded pure observation and parsers.**

Follow this finite sequence: context check → actual start wall time (Round(0)) → boot/cap-last/kernel/caller IDs → open and validate caller-thread/PID1 held proc dirs → read/parse both before statuses/stat/cgroup and namespaces → read/parse both after sets → independently reopen each fixed canonical entry and compare held identity, never concurrently hold more than three FDs → boot-after → actual end wall time (Round(0)) → nonzero strictly advancing wall time/identical boot/all exact before-after fields → copy value DTO return. Any error returns `DeploymentObservation{}` and a fixed sentinel. Cleanup owns returned FDs even if context cancels immediately after open.

Required status fields: Pid/Tgid, Uid/Gid four uint32 values, CapInh/CapPrm/CapEff/CapBnd/CapAmb, NoNewPrivs 0..1, Seccomp 0..2. Reject selected duplicate/missing/invalid/range overflow; caps exactly16 hex digits and output lowercase. Last-cap raw decimal0..63; above63 is unsupported. Drop unknown ordinary status text, never expose it or error-wrap it. Read limits apply in both private orchestration validation and concrete IO; consume limit+1 and reject overflow.

Namespaces must follow the actual referent. On each fixed namespace, fstatat-follow failure EACCES/EPERM records denial; other errors fail. On success, open O_PATH, Fstat+Fstatfs NSFS, match actual device/inode, close immediately. A subsequent open failure is fatal, including EACCES/EPERM. Stable denied class must match before/after. Zero identities, noncanonical encodings, type mismatch, permission drift or identity drift fail. Never demand equality between caller and PID1.

- [ ] **Step 4: Implement concrete Linux resource ownership and command.**

Pin with runtime.LockOSThread and defer Unlock only after all cleanup. Caller uses `/proc/thread-self` and getpid/gettid, PID1 fixed `/proc/1`; held dirs and leaf files must prove procfs type and expected dir/regular type. Only fixed global paths, uname and selected leaves/namespaces may be read. Canonical reopens verify same held identity and parsed process identity; no `/proc/self/fd` reads, no subprocess, retries or detached syscall goroutines. Each native operation checks context before and after. Do not reuse old pool leaf reader which holds two temporary FDs.

CLI actual main zero business args, fixed5s context, no env/stdin, fixed ObserveDeployment. Private run returns1 with fixed stderr for arguments/observer/encoding/size/write error. Encode entire single JSON with newline before stdout; <=32768 bytes. Return0 iff no denied namespace; return2 iff stable EACCES/EPERM. After a partial write return1; cannot retract bytes. Do not claim empty stdout after a write has begun. No raw errors.

- [ ] **Step 5: Exercise behavior, all finite fault/cancellation positions, and cleanup.**

Table cases cover every selected field missing/duplicate/type/overflow; cap width/lastcap; boot/kernel malformed; zero/equal/backwards wall time including differing monotonic/wall components where constructible; comm spaces/parentheses; caller TID vs leader; each stable denial class; per-owner/per-kind namespace drift; stat-success/open-failure; wrong NSFS/dev/inode; held/canonical process replacement. The finite backend records ordered operations and ownership: replay fail-before/fail-after and cancel-before/cancel-after at every operation and assert zero result/error, no subsequent forbidden operation and exactly-once close of every returned owned FD. Check no more than two held+one transient FD. Nil/cancelled context avoids IO. Parallel observations use independent backend instances; race focused on this test.

Secret-canary raw unknown status and raw syscall error must not appear in DTO JSON, error or CLI stderr. Nonroot, NNP0, different namespaces retain facts. CLI tests check args cause no observer call, deadline about5s, complete/denied/error, oversized/encode-failing DTO, short/error writer, exact one JSON and fixed stderr. Unsupported wrapper is tested only on unsupported host, with no Linux proc execution.

Run focused new tests until green, then one task-final package pass:

```sh
python3 .scratch/runtime-m1-deployment-b3a-1020/run-offline.py task1 green /usr/local/go/bin/go test -json -p=2 -count=1 ./internal/platform/capacityprobe ./cmd/switchback-deployment-probe
```

Expected exit0, no skipped/failed tests, source hashes stable through run. Record actual counts from raw JSON, not invented totals.

- [ ] **Step 6: Self-review, stop writing and hand off for independent review.**

Do not stage or commit in this dirty shared checkout. Write `.scratch/runtime-m1-deployment-b3a-1020/task1/task-1-report.md` with implementation, exact receipt/source snapshot paths for fixture/RED/GREEN, self-review and remaining limits. Return DONE (or concerns), no commits, one-line tests, report path and explicit STOPWRITING for both source and Go execution. Root captures a scoped /dev/null diff of seven added files against frozen baseline, runs task spec+quality review, resolves findings through tested fix wave and rereview, then broad final review.

Controller final verification after write handoff: capacityprobe + new command + existing config/capacitysnapshot/capacityobserver/capacitypublisher/capacitypublication/old publisher command and focused localproduct gate tests. Distinct directed new concurrency/cancellation race run; four Linux amd64/arm64 probe+command test artifacts compile-only (`go test -c`, CGO_ENABLED=0), never execute them. Verify baseline955/1761 unchanged, index/HEAD unchanged, only7 source+plan added, prior28/921 seals intact, both gates still unavailable, OpenSpec38/41, all three readiness/execution/paid flags false. Save raw receipts, final report, read-only verifier and phase seal. Existing historical B1 TDD limitations stay historical; this task proves only its own lifecycle.
