# M1 B3-D Deployment Failure Observation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a bounded diagnostic failure API/CLI and a non-authorizing captured-manager mapper, with an entirely offline feedback loop.

**Architecture:** Decorate the existing private deploymentBackend and reuse observeDeployment without modifying existing files. A separate command preserves success v1 and emits a failure v1 packet. A pure Python mapper interprets saved manager bytes without entering any authority path.

**Tech Stack:** Go 1.26.4, existing syscall/x/sys dependencies, Python 3 standard library, captured B3-C fixtures.

## Global Constraints

- Binding approved spec: docs/superpowers/specs/2026-10-20-m1-deployment-failure-observation-b3d-design.md, SHA256 e5dddaa24e1283af2354df4e5c42c49fef6eedfdb348bba934fe2895cba62575.
- Work only in /path/to/switchback. No staging, commits, push, branch mutation or editing preexisting inputs.
- `capacity_ready=false`, `execution_enabled=false`, `paid_consumed=false` remain unchanged. No guest/VM/unit/Engine/PG/Worker/Backend/publisher/provider operation or actual capability grant.
- Do not execute native ObserveDeployment/ObserveDeploymentDiagnostic or either CLI. Go tests must use private fixture backends/injected observers; platform entry tests on Darwin may only exercise the unsupported implementation.
- Existing collector, success DeploymentObservation v1, old command, carrier, seals and 977 current inputs remain byte-identical. The two B3-C seals protect 443 and 69 bound artifacts.
- No new dependencies, paths/PID/environment selectors, mutable global hooks, retries, production ELF/candidate, native window or nonce.
- Backend adapters forward exactly once, do not add IO, and report last primitive context rather than proven root cause. close does not overwrite context.
- New Go signature: `ObserveDeploymentDiagnostic(context.Context) (DeploymentObservation, *DeploymentFailureObservation)`.
- Success output limit is 32768 bytes including LF; failure packet limit is 4096 bytes including LF; one Write per stream; exit 0/2/1. No second JSON appended to partially written stdout.
- Manager schema/kind/authority: `switchback.deployment.manager_forensics.v1` / `diagnostic_only` / `none`. Its result cannot enter Stop/Start/ownership logic.
- Use sequential implementers with disjoint file ownership, a spec+quality review per task, and one broad final review. Evidence packages use explicit new-file diffs against /dev/null because HEAD and index must remain unchanged.

---

### Task 1: Bounded failure DTO and traced collector adapter

**Files:** Create only the five approved internal/platform/capacityprobe/deployment_diagnostic{_types, ,_linux,_other,_test}.go files (the unqualified middle name is deployment_diagnostic.go). No existing source changes.

**Interfaces:** Consumes private deploymentBackend/observeDeployment and the existing same-package deploymentFixture. Produces the public API above; exported `DeploymentFailureObservation`, `DeploymentPrimitiveObservation`, `DeploymentFailureSchema`, and `Validate() error`; private `observeDeploymentDiagnostic(ctx context.Context, backend deploymentBackend)` with the same return signature.

- [x] Write fixture-backed red tests for success parity, late errno, parser rejection after a returned primitive, valid namespace denial followed by a different failure, and no-IO entry/cancelled handling. Establish compilation fails because new symbols are absent.

```go
func TestDeploymentDiagnosticSuccessParity(t *testing.T) {
    want, err := observeDeployment(context.Background(), newDeploymentFixture())
    if err != nil { t.Fatal(err) }
    got, failure := observeDeploymentDiagnostic(context.Background(), newDeploymentFixture())
    if failure != nil || !reflect.DeepEqual(got, want) { t.Fatalf("success mismatch: %#v", failure) }
}
```

- [x] Implement the fixed public shape below, exact JSON tags, closed stage/class/primitive validation, errno 1..4095 only for failed primitive, and absence of arbitrary strings beyond the enumerated fixed targets.

```go
const DeploymentFailureSchema = "switchback.capacity.deployment_failure.v1"
type DeploymentPrimitiveObservation struct {
    Operation string `json:"operation"`
    Target string `json:"target"`
    Outcome string `json:"outcome"`
    Errno *uint32 `json:"errno,omitempty"`
}
type DeploymentFailureObservation struct {
    Schema string `json:"schema"`
    Kind string `json:"kind"`
    Stage string `json:"stage"`
    ErrorClass string `json:"error_class"`
    LastPrimitive *DeploymentPrimitiveObservation `json:"last_primitive,omitempty"`
}
```

- [x] Implement the 11-method decorator using the existing backend signatures. Each method calls the underlying method once, records operation/target and error after it returns, then returns the original result/error unchanged. `errors.As` is used only for the current error's syscall.Errno; no raw text is retained. Track fd→target for nonnegative acquired fd even on late errors; delete on close without overwriting context. `namespaceStat` derives the ns target from its parent fd and enum.
- [x] Implement the private wrapper with entry validation, cancelled-before-IO handling, zero success value on error, old sentinel mapping and a copy of the final context. On success return the original value and nil failure. Linux wrapper locks the OS thread around the private wrapper and fixed deploymentLinux; other-platform wrapper observes nil/cancel before unsupported. Validation returns nil or exactly ErrDeploymentInvalid.

```go
func ObserveDeploymentDiagnostic(ctx context.Context) (DeploymentObservation, *DeploymentFailureObservation) {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()
    return observeDeploymentDiagnostic(ctx, deploymentLinux{})
}
```

- [x] Cover every primitive fault, cleanup/late fd/cancellation, unknown-target fallback, fd reuse, all enums/limits and secret canaries using the original fixture. Test direct decorator calls only with fixture backends. Do not invoke Linux native entry in tests.
- [x] Run `/usr/local/go/bin/go test ./internal/platform/capacityprobe -run 'TestDeployment' -count=1`; save exact red/green commands and outputs under the new B3-D phase. Report files and source hashes; no commit.
- [x] Review task 1 from its complete new-file diff and report; fix Critical/Important findings and re-review before task 2.

### Task 2: Independent diagnostic command

**Files:** Create cmd/switchback-deployment-diagnostic/main.go and main_test.go only.

**Interfaces:** Consumes Task 1 public API and types; produces `main()` and private `run(args []string, stdout, stderr io.Writer, observe func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation)) int`. Observer injection is private; production main binds the literal new public API.

- [x] Write red tests with a complete typed success fixture and injected observer. Verify exit 0 and exact original success JSON, namespace denial exit 2, and observe failure exit 1 packet. Do not call production main or native observer.

```go
func main() {
    os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeploymentDiagnostic))
}
```

- [x] Implement the fixed command: reject args/nil observer with entry/invalid; call observer once with a 5-second timeout; reject nonzero success plus failure, invalid failure DTO, or wrong success schema/kind with observe/internal_invalid_result. Preserve original success value. Marshal fully and append LF before size check or Write.
- [x] Implement output policy with one bounded packet per stream. Before stdout write, failures produce the structured failure on stdout. A short/error stdout Write sends one constant write/write_failed packet to stderr; no recursive handling and no output retry. Test `n==len` plus error as a failure and nil/short/error writers. The fixed failure packet is not grown with arbitrary strings.

```go
func writePacket(w io.Writer, packet []byte) bool {
    if w == nil { return false }
    n, err := w.Write(packet)
    return err == nil && n == len(packet)
}
```

- [x] Cover 32768/32769 success bytes including LF, time.Time encoding failure, longest valid failure DTO below 4096, invalid enums and contradicting combinations, argument no-call behavior, context deadline, secret canaries and at-most-one-write per stream including stderr failures. An AST test verifies the literal production observer binding and rejects runtime configuration surface.
- [x] Run `/usr/local/go/bin/go test ./cmd/switchback-deployment-diagnostic ./cmd/switchback-deployment-probe -count=1`; save red/green receipts and hashes, no commit.
- [x] Review task 2 for spec and quality from complete new-file diff; fix and re-review blocking findings before task 3.

### Task 3: Pure captured-manager forensics

**Files:** Create only .scratch/runtime-m1-deployment-failure-observation-b3d-1020/manager_forensics.py and test_manager_forensics.py; test fixtures and report artifacts may be added under that same new phase.

**Interfaces:** `map_manager_forensics(raw: bytes) -> dict`; `ManagerForensicsError` with constant text invalid_manager_capture. Exact output keys, bounds, regexes and classification are the approved spec's manager section. No reader, subprocess, CLI, expected unit/nonce/prior receipt or authorization callback.

- [x] Add red tests which extract manager show stdout from existing sealed actual/post envelope (verify envelope/command completion, exit status, base64, bytes and SHA). Bind both extracted raw inputs to 1290 bytes / SHA256 5140e67e07a2856fc2cc917a8149a463e2364c3a1840bbce036e6a3190915246. Assert the absent mapper import/function fails.

```python
def test_manager_capture_facts(self):
    result = map_manager_forensics(self.actual_manager_raw)
    self.assertEqual(result["authority"], "none")
    self.assertEqual(result["unit"]["active_state"], "failed")
    self.assertEqual(result["cgroup"], {"raw": "", "class": "empty"})
    self.assertEqual(result["process"]["exec_main_pid"], 38960)
    self.assertEqual(result["syscall_filter"]["class"], "syscall_tokens")
```

- [x] Implement a pure parser: enforce bytes/ASCII/LF/line/key/value bounds, duplicate and missing-key checks, canonical uint32; select the 12 required attributes without echoing unknown values. Build only the fixed schema/kind/authority/source/unit/process/invocation/cgroup/syscall_filter dict. Classify raw strings lexically, preserve raw strings byte-for-byte and SHA the complete input. Ensure encoded output plus LF is within 65536.
- [x] Add boundary and mutation tests: 0/65536/65537 input, 256/257 lines, duplicates/missing fields, CR/NUL/non-ASCII, key/value bounds, overflow/noncanonical numbers, empty/hex32/unclassified InvocationID, empty/present cgroup, all filter classes, source digest changes and no forbidden authority keys. A valid-looking forged invocation stays authority none.
- [x] Use existing offline carrier fixtures for source/nonce drift, incomplete transport, actual single consumption and refusal to Stop. Prove the new mapper does not alter these fixtures or the old manager rejection; do not invoke real carrier modes. Record exact selected tests and their output.
- [x] Run `/usr/bin/python3 -I -B .scratch/runtime-m1-deployment-failure-observation-b3d-1020/test_manager_forensics.py`; save red/green output and report, no commit.
- [x] Review task 3 from its new-file diff, fixture provenance and reports; fix and re-review blocking findings.

### Task 4: Final offline verification and evidence closeout

**Files:** Only new B3-D phase evidence, this new plan's checkboxes and the new B3-D progress ledger. Do not alter the approved spec or historical ledgers.

**Interfaces:** Consumes all completed task reports/diffs and baseline protection manifests; produces a sealed report describing offline results and remaining native unknowns.

- [x] Run final relevant Go suite once with JSON receipts: `/usr/local/go/bin/go test -json ./internal/platform/capacityprobe ./cmd/switchback-deployment-diagnostic ./cmd/switchback-deployment-probe -count=1`; require exit0 and zero fail events. Run race once over those packages. If fixes follow, re-run only covering checks until the final complete relevant validation.
- [x] Compile test binaries only for Linux amd64 and arm64: `GOOS=linux GOARCH=<arch> /usr/local/go/bin/go test -c -o <new-phase>/compile/<package>-<arch>.test <package>` for capacityprobe and both commands. Do not execute these binaries or build a production ELF.
- [x] Run Python mapper suite and the existing B3-C read-only offline verifier. Record exact test counts, failures/skips and distinguish previous evidence from new receipts.
- [x] Dispatch one broad final spec+quality review with all new-file diffs, approved spec and task reports. Resolve findings in one fix wave, collect covering verification, then re-review.
- [x] Verify every old protected byte/hash, both B3-C seals, HEAD/branch/index, and Git-visible allowed additions. Run git diff --check. Freeze new sources and final receipts, write report/outcome/phase seal and verify bound artifacts.
- [x] Report that this delivers offline diagnostics only; original ELF exit1 root cause and native deployment remain unproven. Do not request another design approval for already-approved work.

## Plan self-review

All spec sections map to tasks 1–4. Public names and failure DTO fields are fixed in Task 1 and consumed unchanged by Task 2; Task 3 is independent from execution authority. No plan step edits old files or calls a native deployment collector. Existing dirty worktree protection takes precedence over the skill's generic commit/review-package examples, so diffs are assembled from the task-owned new files and no commit is created.
