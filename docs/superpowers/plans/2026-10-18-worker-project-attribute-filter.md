# Worker Project Attribute Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Before any Worker process starts, require an immutable container seccomp profile that prevents project-attribute setters while preserving the pinned Moby default restrictions and all other ioctl commands.

**Architecture:** Embed the exact complete Moby profiles/seccomp v0.2.4 baseline. Replace its sole unconditional ioctl ALLOW with a disjoint low32 prefix complement of the three setter commands. Bind the resulting JSON in every Worker create and exact inspect comparison; use actual Engine Info for supported architecture/seccomp prerequisites before staging.

**Tech Stack:** Go standard library, existing Moby client, embedded JSON, pure fake-Engine tests; no dependency additions.

## Global Constraints

- Only edit `/path/to/switchback`; preserve pre-existing dirty changes. No Git writes, commits, staging, reset, checkout, stash, clean, merge, or push.
- Source implementation only. Do not start VM/Engine/service/collector/provider/DB/native-kernel experiments or paid tasks; do not install or upgrade dependencies.
- `capacity_ready=false`, `execution_enabled=false`, `paid_consumed=false`; OpenSpec 6.5/8.4/9.3 remain open. Fixed 2 vCPU / 4 GiB / 512 PIDs / 10 GiB and 80% aggregate are unchanged.
- Filter all Worker processes through container creation, including shim/ACP/tools/SessionProcess/exec. No user-, Task-, or Agent-controlled policy input. Backend, observer and secret populator retain their own existing policies.
- Reject FS_IOC_FSSETXATTR `0x401c5820`, FS_IOC_SETFLAGS long64 `0x40086602`, and compat32/x32/ARM `0x40046602` by the command's low32 bits. Upper32 aliases must not bypass. Other flags/hints modified through those setters are also rejected, as explicitly accepted by the user.
- Preserve the entire pinned default restrictions, archMap, includes/excludes, errno and other rules. No default ALLOW, no unconfined/default fallback, no flattening by Backend GOARCH, no overlapping ALLOW/deny workaround, no repeated argument comparisons in one rule.
- Actual Engine architecture must be one of `amd64`, `x86_64`, `arm64`, `aarch64`, and Engine Info must declare seccomp support. Missing/unknown evidence fails before inspect/staging/create/start. This is a prerequisite, not proof of installed BPF.
- Exact policy equality is required for historical and newly created containers, including after the pre-Start check. Do not migrate, restart or eagerly clean up a mismatch. Create failure must not retry without the filter.
- Old source seals remain immutable. Authorized changed current paths are mapped to this phase's verified `source-before/` copies, with explicit before/after digests; old frozen receipts remain checked at their original paths. Do not silently skip historical drift.
- Tests/builds use `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local`, isolated task cache, and no shared database/container window. Source/fake tests and cross-compilation do not prove native enforcement.

---

### Task 1: Bind the Worker filter to the production create/inspect path

**Files:**
- Create: `internal/workerruntime/adapter/docker/worker_seccomp.go` (embedded baseline, private builder and Engine prerequisite helper).
- Create: `internal/workerruntime/adapter/docker/worker_seccomp_test.go` (internal transformation tests).
- Create: `internal/workerruntime/adapter/docker/project_attribute_filter_test.go` (external production-path tests using existing fixtures).
- Create: `internal/workerruntime/adapter/docker/seccomp/moby-default-v0.2.4.json`, `seccomp/LICENSE`, `seccomp/README.md` (exact source, license, provenance and limits).
- Modify: `internal/workerruntime/adapter/docker/provisioner.go` (private immutable policy string, constructor error propagation, create security option).
- Modify: `internal/workerruntime/adapter/docker/writable_layer.go` (actual Info prerequisite, using the existing Info call).
- Modify: `internal/workerruntime/adapter/docker/writable_layer_test.go` (supported synthetic Info fixture).
- Read: `.scratch/runtime-worker-project-filter-1018/primary-audit.md`, `primary-closure.md`, primary receipts and approved design.

**Interfaces:**
- Consume existing `NewProvisioner(Engine, Options) (*Provisioner, error)`, `Provision`, `createOptions`, `verifyExisting`, and `CheckWritableLayer`. Their public signatures remain unchanged.
- Produce private `buildWorkerSeccomp(baseline []byte) (string, error)` and immutable `Provisioner.workerSeccomp string`. Keep algorithm internals private and standard-library-only.
- A single Engine Info result serves storage compatibility and filter prerequisites; no additional external calls.

- [x] **Step 1: Add a regression test against actual captured Worker ContainerCreate options.**

Use `classicOverlay2Engine`, `recordingEngine`, `validRequest` and `allowSyntheticPreStart` from existing external tests. Assert the Worker (not populator) security options contain one complete `seccomp=` JSON with default ERRNO/errno 1 and no unconditional ioctl ALLOW. Independently interpret ioctl masks to assert setters rejected and getters/TTY commands retained. The central behavior check is:

```go
for _, command := range []uint64{0x401c5820, 0x40086602, 0x40046602} {
    for _, high := range []uint64{0, 1 << 32, 0xffffffff00000000} {
        if ioctlAllowed(profile, high|command) { t.Fatalf("setter %#x allowed", high|command) }
    }
}
for _, command := range []uint64{0x801c581f, 0x80086601, 0x80046601, 0x5401, 0x5413} {
    if !ioctlAllowed(profile, command) { t.Fatalf("getter/TTY %#x blocked", command) }
}
```

Run `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOCACHE=/private/tmp/switchback-worker-project-filter-1018 go test ./internal/workerruntime/adapter/docker -run ProjectAttribute -count=1`; save the expected missing-policy FAIL output before product edits.

- [x] **Step 2: Embed and transform the complete pinned profile.**

Copy `primary/profiles-default.json` byte-for-byte, SHA256 `785b2429264afba4d594320337cb17f144f3c7d51585f9805eef72e28f4f9334`, and `primary/profiles-LICENSE` unchanged. Record Moby commit `a5b58c94d334674eae3a3207d4f05417643a90d0`, profiles commit `245180c51918481c0525424b3ee025d2b435d46c` and source URLs in README. Compile with `//go:embed`; verify input digest before transformation.

Use `map[string]json.RawMessage` for top-level and original syscall rules so no Docker extension or unknown field disappears. Validate defaultAction `SCMP_ACT_ERRNO`, defaultErrnoRet 1 and exactly one argument-free unconditional ioctl ALLOW in `names`; fail on unexpected legacy `name` ioctl or another ioctl rule. Remove only ioctl from that names slice. Preserve all other fields/rules/peers and relative order. Append rules using this complete complement walk:

```go
var forbidden = [...]uint32{0x401c5820, 0x40086602, 0x40046602}
var walk func(prefix uint32, depth uint)
walk = func(prefix uint32, depth uint) {
    var mask uint32
    if depth != 0 { mask = ^uint32(0) << (32-depth) }
    blocked := false
    for _, command := range forbidden {
        if command&mask == prefix { blocked = true; break }
    }
    if !blocked {
        // Append names:["ioctl"], action:"SCMP_ACT_ALLOW", args:[{
        // index:1, value:uint64(mask), valueTwo:uint64(prefix), op:"SCMP_CMP_MASKED_EQ"}].
        appendPrefixRule(mask, prefix)
        return
    }
    if depth == 32 { return }
    walk(prefix, depth+1)
    walk(prefix|uint32(1)<<(31-depth), depth+1)
}
walk(0, 0)
```

Serialize deterministically to compact JSON. `appendPrefixRule` is a private local closure constructing that exact map/typed appended rule; there is exactly one argument comparator per emitted rule. Hash/parse/shape errors return an error, never unrestricted JSON. No runtime architecture preprocessing.

- [x] **Step 3: Wire the policy and prerequisites into existing boundaries.**

After existing constructor validation, call `buildWorkerSeccomp(embeddedBaseline)` and return its error. Store the returned string in private `workerSeccomp`. In `createOptions` use:

```go
SecurityOpt: []string{"no-new-privileges", "seccomp=" + p.workerSeccomp},
```

The existing `verifyExisting` exact `reflect.DeepEqual(gotHost.SecurityOpt, wantHost.SecurityOpt)` then covers old and both new-container inspect checks. Add Engine prerequisite validation to `CheckWritableLayer` before its successful return using its existing `result.Info`: allow exactly the four architecture aliases; require a SecurityOptions entry equal to `name=seccomp` or beginning `name=seccomp,`. Return a descriptive unavailable error otherwise. Update `classicOverlay2Engine` to supply `Architecture: "aarch64"` and `SecurityOptions: []string{"name=seccomp,profile=builtin"}`. Do not add fallback, new Task fields or new capacity authority.

- [x] **Step 4: Add meaningful invariant and failure-path coverage, then run GREEN.**

Internal tests decode the emitted JSON, compare the entire remaining baseline semantically (including default, archMap and all extension fields), and independently convert each prefix mask to a uint64 interval. Sorted intervals must be disjoint and cover `[0, 1<<32)` with exactly the three forbidden singleton gaps. Require nonzero contiguous high-prefix low32 masks and prefixes within the mask. Test bad input/hash rejection and deterministic output. Do not copy the recursive production algorithm into the verifier.

Production-path tests use the existing `prestartEngine` mutation harness for missing/unconfined/tampered policy at first inspect and after pre-Start validation; assert zero WorkerStart, retained exact partial CID and no eager cleanup. Exercise historical mismatch rejection. Unsupported/missing architecture or seccomp support must use `assertNoProvisionSideEffects`. Supported architecture aliases pass. Custom-profile Create rejection propagates without unfiltered retry.

Run focused regression, full adapter package once, gofmt checks, `go vet ./internal/workerruntime/adapter/docker`, then `GOOS=linux GOARCH=amd64 go test -c ...` and the arm64 equivalent to phase-local outputs (compile only). Save stdout/stderr, exact commands and exit status under phase evidence. Do not run broad DB/runtime tests.

- [x] **Step 5: Self-review and report without Git mutation.**

Write `.scratch/runtime-worker-project-filter-1018/task-1-report.md` with changed paths, before/after behavior, actual RED/GREEN commands and outputs, cross-build results, limitations and unresolved concerns. Return status only with that report path. Root will create a snapshot-based diff, dispatch a fresh Spec/Quality review, resolve findings, then dispatch final scoped review. Preserve prior seals via explicit source-before mappings, write handoff and a new phase seal.

## Plan self-review

One cohesive production boundary with one test/review cycle; approved design criteria map to Steps 1–4. No public API/type drift, no new dependencies or privilege. Native per-ABI loading, whole-process enforcement, installed BPF and XFS-before-callee rejection remain a separate unperformed runtime acceptance. User authorization overrides generic skill commit/execution-choice defaults.

### Task 2: Close I1 on historical restore and durable control reconnect

The final integrated review found one Important issue in unchanged production seams: generic Inspect does not validate policy, startup prepare restores credentials before inspection, and gRPC serves/Claims/replays before startup restore. This task is required to satisfy the already-approved historical exact-policy requirement. Complete requirements and concrete recommended seams are frozen in `.scratch/runtime-worker-project-filter-1018/fix-brief.md`; the finding is preserved in `final-review.md`.

**Files:** extend the narrow task scope to `internal/localproduct/runtime_worker_policy.go`, `runtime_worker_policy_test.go`, `runtime_runner.go`, `runtime_startup.go`; amend the Docker policy helper/tests/provenance. Original pre-task dirty snapshots already include the existing runner/startup files. Additional paths require root verification before edits.

**Interfaces:** share a server-owned physical verifier for the exact CID/Session/runtime/generation and fixed policy. A production `workerapp.RuntimeControlJournal` Store decorator overrides Claim before delegating, forwarding all other methods unchanged. Startup prepares through the same verifier before opening credentials; ordinary observation/cleanup remains available. Trusted saved/resident CID pin is read-only input; it never grants execution and the original journal/capacity checks still run after a match.

- [x] Reproduce I1 with pure regression(s) for actual production wiring and pre-Claim/pre-credential ordering; save RED without altering previous evidence.
- [x] Implement the physical verifier and narrow production journal/startup wiring. Missing/conflicting owner or policy evidence fails closed without cleanup, restart, persistent mutation, quota release or alternate authority. Existing durable CID is used when available; normal first Claim needs the same-process verified Create handoff in Task3, never a guessed container name.
- [x] Prove missing/unconfined/tampered/unknown policy rejection, exact match delegation and original authority errors, startup-first/control-first ordering, saved/initial/legacy idle pin resolution and mismatch failure using pure fakes; run focused/full adapter and focused pure localproduct checks, vet, format and Linux cross-compiles with the established offline constraints.
- [x] Freeze the cumulative diff from original dirty before snapshots, refresh exact historical mappings for every additional authorized existing path, and re-review the complete I1 fix. Preserve the first Needs fixes report and resolve findings before the new phase seal. Source approval remains separate from native/capacity/experience acceptance.

### Task 3: Fix I2 first-Claim identity ordering

`final-review-r2.md` confirms that before initial Start both saved/resident CIDs must remain empty for the existing PG authorizer. Claim precedes runtime.ready, which precedes activation/reconcile that pins the resident. The Task2 interpretation that a pin was already present before Start was wrong. Implement `.scratch/runtime-worker-project-filter-1018/i2-fix-brief.md` without changing the original PG protocol.

**Files:** narrow amendments to existing Provisioner / owner admission / runner / tests / provenance, plus `internal/workerruntime/adapter/docker/worker_policy_handoff.go` and `worker_policy_handoff_test.go`.

- [x] Reproduce normal double-empty initial owner failing before Claim despite an exact verified Create handoff; keep fixture errors separate from regression evidence.
- [x] Publish immutable server-owned CreatedRuntimeIdentity after second verifyCreated and before Start, using an independent mutex. Resolve empty initial CID only from that exact process-local handoff; still freshly inspect the policy and delegate unchanged journal authority.
- [x] Test synchronous Start/Claim without deadlock, absent/conflicting handoff refusal, inspect/authority failure publication boundaries, journal-denied retry retention, unknown Start retention, confirmed-cleanup removal and restart-without-evidence owner hold. Update wrong pin timing descriptions and preserve old reports with an erratum.
- [x] Run focused/full adapter, relevant pure localproduct, formatting/vet and Linux dual-architecture compile-only checks. Freeze cumulative review-r4, preserve all historical bytes through explicit mappings and re-review before source approval/seal. Native and full experience gates remain open.
