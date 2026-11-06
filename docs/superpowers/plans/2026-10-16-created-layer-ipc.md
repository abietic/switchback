# CreatedLayer IPC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development, one source writer at a time and independent Spec+Quality review after each task. No Git mutation.

**Goal:** Add the user-selected managed synchronous finite observation path at the existing preStart checker, keeping full-capacity authority independent.

**Architecture:** A fixed Unix IPC deep module authenticates fresh one-connection observations with a dedicated role key/challenge; a shared facts verifier checks the same quota policy for IPC and signed files; Backend assembles only the IPC checker in production. Native service/compose assets are source templates, not deployed services.

**Tech Stack:** Go1.26/std crypto/net/JSON, existing x/sys/unix0.47, existing capacityprobe/native adapter, systemd template and Compose overlay.

## Global Constraints

- Candidate only `/path/to/switchback`; protect dirty work, no Git mutation. Old source-only1672-binding seal `83c2c8d0bd988a3026ccaa9b8fd7ee65b05260cdab478e45899111be31ecca89` is preserved; declared current-source changes receive new task-local before copies.
- Approved design: `docs/superpowers/specs/2026-10-16-created-layer-ipc-design.md`; user chose managed synchronous IPC after proposal. No further route approval is needed for this source implementation.
- No VM/guest/Engine/container/image/Task/ACP/model/DB connection/schema/privilege installation or deployment. No actual key/Trust generation, signing or publication. No consumed1013/1014 phase retry. Paid acceptance remains unconsumed.
- Endpoint only `/run/switchback-created-layer/observer.sock`; native only Linux amd64/arm64. Backend `cap_drop ALL` unchanged. Server key `/etc/switchback/created-layer-observer/key`; client key `/run/switchback-observer-auth/key`, root0400 regular nlink1 and32bytes, never env/log/output. Filesystem ancestor/socket/peer validation and HMAC are complementary.
- Only finite facts; collections remain original unsigned/preparation_blocked, trust none, capacity/execution false. No capacity ledger/token/DB authority changes. Single actual production path, no File reader fallback, no generic recovery allow.
- Per-task exact before/new source baseline, raw argv/original child exits/source snapshots/hash; RED/GREEN and one final covering affected suite plus gofmt/vet; both Linux architectures compile-only with true labels. Reviewers inspect logs/source, do not repeat the same suite. Fresh isolated implementer and reviewer, fix all Critical/Important before next task.

## Task 1: authenticated fixed IPC and managed native entry

**Files:** Create focused `.go`/tests under `internal/platform/createdlayeripc/` (`types.go`, `wire.go`, `client.go`, `server.go`, `native_linux.go`, `native_other.go` and focused tests); create `cmd/switchback-created-layer-observer/main.go` and tests. No existing production file changes. Raw capacityprobe sources stay unchanged.

**Produces:** Exact Binding and NewClient/Observe, NewServer/Serve, ReadClientKey/ReadServerKey interfaces in the design. Client/server private per-instance dial/peer/clock/entropy/filesystem/collector seams let package tests drive the actual protocol; public constructors install only fixed native implementations and fixed existing collector. Avoid a configurable public transport or generic RPC.

- [x] Capture original source baseline and public missing-interface RED. Add public constructor/Observe protocol regression tests before implementation, with net.Pipe/internal dial seam, never actual network/Engine/filesystem root mutations.
- [x] Implement bounded authenticated wire. Use exact domains `switchback/created-layer-observer/request/v1\x00` and `switchback/created-layer-observer/response/v1\x00`, magic `SBCLIPC1`, one fresh32-byte server challenge, one fresh32-byte client nonce. Request/response structures have all fields non-omitempty, version `switchback.created_layer.ipc.v1`; MAC request over domain+challenge+rawrequest, response over domain+challenge+SHA256(rawrequest)+rawresponse. Frame length4BE+MAC32+body; body≤8192/131072. No replay cache: challenge used once/connection, never process second frame.

```go
func canonicalDecode(raw []byte, out any) error {
    d := json.NewDecoder(bytes.NewReader(raw))
    d.DisallowUnknownFields()
    if err := d.Decode(out); err != nil { return errWire }
    if err := d.Decode(new(any)); err != io.EOF { return errWire }
    canonical, err := json.Marshal(out)
    if err != nil || !bytes.Equal(canonical, raw) { return errWire }
    return nil
}
```

- [x] Implement fixed native Unix/no TCP fallback; root-owned secure key/socket/ancestor checks, SO_PEERCRED and before/after inode/device. Before listen verify actual unified self cgroup is exact `/switchback.slice/switchback-core.slice/switchback-created-layer-observer.service` and actual cgroup2 cpu.max20000/100000, memory.max134217728, swap.max0, pids.max64. Export fixed `CoreCgroupPath` and `ObserverCgroupPath` constants only; no request/config override. Wrong path or any hard limit must produce zero listen/collector. Require host Server PID>0; Client may see host peer PID0 from a container, authenticated by role key/digest/challenge plus root UID/GID and fixed private mount, not PID alone. Do not unlink an existing socket; cleanup only actual owned unchanged socket. Unsupported platform rejects before IO. All key entropy/filesystem errors return constant typed errors without sensitive strings. Deep-copy key and Binding; nil/cancelled context before effects.
- [x] Server handles connections serially with finite handshake/read/write deadlines; use collector context≤30seconds and no sampler goroutine. Client ties cancel to connection closure only, not sampler FD reuse. Validate deadline not past/not more30seconds; response time must be within this request (allow2seconds clock skew) and exact Binding/nonce/request digest. Copy authenticated partial facts then deny in later checker. No host path selection via Binding.
- [x] CLI only `serve <sha256:digest>` on supported platform, then fixed key read and signal-aware Serve; no generation/installation/start command. Write tests for invalid/unsupported args with zero operational effects.
- [x] RED/GREEN protocol cases: success actual client+server+collector control flow, wrongMAC/key/challenge/direction/reflection/crossconnectionreplay/nonce/manifest/CID/runtime/profile, duplicate/missing/casealias/noncanonicalJSON/shortfixedarrays/unknown fields/oversize/truncation, stale/deadline/cancel/slowpeer, zero secret canary in errors/output. Native recorder covers symlink/writableancestor/wrongowner/link/socketdrift/peerdeny/FDcleanup.
- [x] One final package/CLI suite, gofmt/vet, Linux amd64+arm64 package tests/CLI tests/executable compile-only. Freeze `.scratch/runtime-observer-ipc-1016/task-1-{report,manifest,review.diff}` and task evidence, stop product writes; independent review.

## Task 2: shared fact policy and IPC checker

**Files:** Modify `internal/workerruntime/adapter/capacitysnapshot/created_layer_reader.go` narrowly; add focused verifier file/tests there; create `internal/workerruntime/adapter/capacityobserver/{checker,observation}.go` and tests; add `internal/platform/capacityprobe/created_layer_validation.go` and tests if needed to reuse its private finite-result helper, without changing the13sealed collector/native/CLI files. Do not change existing quota values/signature domain/Trust schema/runtime coordinator.

**Consumes:** Task1 `createdlayeripc.Binding`, NewClient/Observe and ReadClientKey. **Produces:**

```go
// package capacitysnapshot
func NewCreatedLayerVerifier(Trust) (*CreatedLayerVerifier, error)
func (*CreatedLayerVerifier) ValidateRequest(context.Context, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) error
func (*CreatedLayerVerifier) VerifyCreatedLayerFacts(context.Context, CreatedLayerFacts, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) error
// package capacityobserver
func New(trust capacitysnapshot.Trust) (*Checker, error)
func (*Checker) CheckCreatedLayer(context.Context, workerapp.RuntimeProvisionRequest, workerapp.CreatedRuntimeIdentity) error
// Optional focused pure helper, if needed to reuse existing private sampler checks:
// package capacityprobe
func ValidateCreatedLayerObservation(containerID string, c CreatedLayerCollection) error
```

- [x] Freeze existing reader/source baseline. Public validator/checker RED; existing signed-reader suite remains the compatibility baseline.
- [x] Extract exactly one immutable facts policy. File CheckCreatedLayer still validates request before IO, reads bounded signed envelope, verifies existing exact domain/key/payload bytes, then calls same facts validation. New verifier validates full Trust once/deep-copies slices and profile. Keep old request, freshness, identity, node, static-project and quota conditions; do not introduce unsigned file acceptance or accept default0 instead of target inode hard.
- [x] Add Checker with per-instance lazy sync.Once key/client creation and no IO on New; every legal Check requires exact Trust.Core aggregate `/switchback.slice/switchback-core.slice` before key/IPC (wrong/missing/child/sibling refuses with zero communication); zero or invalid verifier/request denies before key/IPC, canceled context remains context.Canceled. Public production construction installs only fixed Task1 Client; package tests may inject per-instance observe function, never global hook.
- [x] Validate exact finite collection and observed-vs-pinned identities before translating. Identity is derived from actual Engine, raw root/parent/diff UUID/boot/mount/device; format mount namespace `mnt:[<decimal>]`, not bare decimal. Freshness, complete status, raw before/after equality, all required samples observed, geometry/raw ABI, dynamic target and errno are checked; no observed field copied from Trust to manufacture agreement. Owner/profile/manifest metadata bind the authenticated request, not a fabricated on-disk ownership observation. Feed existing policy with unchanged raw target/default0 bytes and actual QSTATV version/flags.
- [x] Test drift of every pinned identity, incomplete/missing/error samples, inode/path/UUID/attrs/raw changes, REALTIME/RTINHERIT, zero/off quota, wrongABI/ENOENT/inodehard0/overflow/not10GiB, time/context, post-constructor Trust mutation, unsigned/badsignature oldfile refusal. Public coordinator→real Checker rejection must retain exact created identity and make zero Start/second effects; finite checker success must still reach original authorizer, never become authority itself.
- [x] Cover affected capacitysnapshot/capacityobserver/capacityprobe suites, gofmt/vet, both Linux package compile-only. Freeze task2 diff/manifest/raw/source and independent review. No PG or real IPC path reads in tests.

## Task 3: production composition and source deployment assets

**Files:** Modify only `internal/localproduct/runtime_capacity_source.go` and `runtime_created_layer_test.go` as necessary; add focused production composition tests. Create `deploy/experience/switchback-created-layer-observer.service`, `created-layer-observer.compose.yaml`, focused deployment asset tests and runbook. Do not modify old `core.compose.yaml`, local compose caps, sealed engine units or migrations; do not change integration fakes to silently bypass checker.

**Consumes:** `capacityobserver.New` and concrete Checker. `initialCapacitySource.createdLayer` becomes `*capacityobserver.Checker`; source.CheckCreatedLayer remains workerapp contract.

- [x] New composition RED proves production uses IPC Checker and no Trust/zero source stays unavailable; canceled context preserved. Existing capacity-only constructor tests remain valid because key/client IO is lazy; compile integration fixtures without claiming executed PG tests.
- [x] Replace File reader constructor with `capacityobserver.New(trust)` and keep running snapshot Reader/Store/InitializeTrust unchanged. No environment switch or fallback reader route. Adjust only concrete-reader unit expectations to IPC/unconfigured errors. Existing fake fidelity and heartbeat conflict remain documented followups.
- [x] Add source-only unit with exact bounded profile in design. `ExecStart=/opt/switchback/bin/switchback-created-layer-observer serve ${SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST}`; operator-owned EnvironmentFile `/etc/switchback/created-layer-observer/deployment.env`, required digest argument. RuntimeDirectory=switchback-created-layer,0700; Slice=switchback-core.slice; CPUQuotaPeriodSec=100ms; same host mount namespace; SysAdmin bounding only; no namespace-creating filesystem hardening. Service start/LSM/seccomp/systemd compatibility remains physically unverified. No auto-restart or auto-key generation.
- [x] New Compose overlay only Backend: private Engine source `/run/switchback-engine/docker.sock`→existing `/var/run/docker.sock` target (same Engine as observer), read-only socket directory `/run/switchback-created-layer`, key file `/etc/switchback/created-layer-observer/key`→`/run/switchback-observer-auth/key`; `create_host_path=false`, IPC mounts read_only. Preserve cap_dropALL/user0/no privilege/host network/public port changes; no Worker key/socket mount.
- [x] Deployment tests parse real unit/overlay and compare required fields, fixed mounts, no caps/implicit enablement/keygeneration, resource profile and namespace assumptions. Runbook details externally installed key/manifest binding, native Server opens key read-only in host namespace while Backend alone has read-only key bind; rotation requires both processes, observer stop/start directory inode changes and atomic key replacement require controlled Backend container recreation/remount, owned socket cleanup, cgroup aggregate budget and missing full-capacity evidence, fresh-phase native/peer/physical validation. Don't claim an executable deployment until real gate evidence exists.
- [x] Final affected localproduct unit+adapter regressions, integration compile-only (no PG DSN), native doublearch CLI build; exact source/raw freeze and review. Parent verifies cumulative declared changes/history, broad final incremental Spec+Quality review, seal and handoff. Old OpenSpec physical closeout tasks remain open.
