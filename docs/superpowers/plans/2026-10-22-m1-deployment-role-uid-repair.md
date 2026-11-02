# M1 frozen-role UID compatibility repair plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this one-task repair. Steps use checkbox syntax.

**Goal:** Accept the existing PostgreSQL UID999/container roles through full frozen qualification without weakening Backend/publisher/observer root identity or missing-cap handoff checks.

**Architecture:** Compare actual namespace-role UIDs with that exact role's frozen UID. Keep the existing root-only `roleMatches` wrapper for Backend handoff. Root service roles remain explicitly root. No startup or receipt-format change.

**Tech Stack:** Existing Go/procstatus, private full qualification fixtures, offline tests.

## Global constraints

- Existing approved shared-volume design and deployment profiles remain authoritative; core.compose.yaml explicitly configures postgres user 999:999, assistant user 10001:10001, and Backend 0:0.
- Work only in /path/to/switchback; preserve all other dirty files, HEAD/index and historical seals. No commits or staging.
- No VM/Engine/PG/provider/listener/key/journal/unit execution. Source and synthetic offline qualification only.
- Backend/publisher/observer remain UID0. Existing exact cap sets, ambient/inheritable0, NNP1, seccomp2, boot/binary/cgroup and role/core coverage remain unchanged.
- No schema, guarantee ref, signature, raw v1/v2, PG owner or Start changes. A per-role declared UID is already present; do not infer it from current process or silently rewrite it.
- Test-only fixture assembly must not become a production injection API. Protected publish_test.go and historical source snapshots remain untouched.

### Task 1: Regression and minimal role comparison repair

**Files:** Modify internal/workerruntime/adapter/capacitysnapshot/deployment_qualification.go and deployment_qualification_native.go; create deployment_qualification_role_uid_test.go. All evidence under .scratch/runtime-m1-role-uid-bootstrap-1022.

**Interfaces:** Retain roleMatches(p procstatus.Status, cap uint64) bool as root-only. Add a package-private UID-aware predicate (for example roleMatchesUID(p procstatus.Status, uid uint64, cap uint64) bool) and use only for declared role observations. No exported interface change.

- [x] Build a full positive frozen fixture including postgres UID999 with exact core CID/image/build/input/observation bindings, and show it rejects on current production validation. Preserve actual RED command/output. Use existing fixture helpers rather than copying the full schema. Explicitly distinguish fixture replay from real PG/native execution.
- [x] Compare declared UID against uint32 range before conversion; require all four observed UID values to equal it. Source validation permits declared non-root container roles but retains UID0 for backend/publisher/observer and existing host-process assumptions. Native role validation uses its matching declared UID. Keep handoff calling the root-only wrapper.

```go
func roleMatches(p procstatus.Status, caps uint64) bool {
    return roleMatchesUID(p, 0, caps)
}
func roleMatchesUID(p procstatus.Status, uid, caps uint64) bool {
    if uid > uint64(^uint32(0)) { return false }
    want := uint32(uid)
    mask := fmt.Sprintf("%016x", caps)
    return p.UIDs == [4]uint32{want,want,want,want} &&
        p.CapPrm == mask && p.CapEff == mask && p.CapBnd == mask &&
        p.CapAmb == "0000000000000000" && p.CapInh == "0000000000000000" &&
        p.NoNewPrivs == 1 && p.Seccomp == 2
}
```

- [x] Cover UID999 and another declared non-root container, actual-vs-declared mismatch (including one changed real/effective/saved/fs UID), UID overflow, wrong caps/NNP/seccomp, and attempted non-root mandatory/host roles. Confirm exact core coverage remains required and root-only handoff cannot become non-root. The test must reach real full qualification for the main compatibility failure, not only the helper.
- [x] Run focused RED/GREEN and full capacitysnapshot package, then publisher/probe/observer and focused localproduct only where cross-module contract could change. Use env -u SWITCHBACK_TEST_DATABASE_URL TMPDIR=/private/tmp GOCACHE=/private/tmp/switchback-shared-volume-1022-go-cache GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 go test -p 2. Gofmt changes. Cross-compile affected snapshot/publisher/localproduct packages Linux arm64/amd64 without execution; compiler changes justify this run.
- [x] Write task-1-report.md with source cause, actual commands/output, exact changed path list and limits; no success claims about real PG/native. Controller creates scoped before-snapshot diff for independent Spec/Quality review and verifies preservation. Bootstrap startup behavior is outside this repair.
