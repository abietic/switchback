# M1 Native Publisher B2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Implement the accepted B2 candidate chain offline, preserving old signed bytes/time through transient sampling failure and keeping production activation unavailable.

**Architecture:** Add one durable pending-number abandonment action to B1. A separate fixed-source collector feeds the existing Mapper; a private signer and loop use B1's exact byte API, with a disabled production Run and undeployed independent service template.

**Tech Stack:** existing Go toolchain, x/sys/unix, net/http, crypto/ed25519, encoding/json; no dependency changes.

## Global Constraints

本设计请求的是源码及离线验证，包括新增受管进程的权限模板审阅；不授权安装/启用 service、CAP 实际授予、key/Trust/journal 初始化、Linux native 调用、VM/Engine/PG/Worker/provider 启动或真实环境验收。`verifyCapacityDeployment` 原样 unavailable；新 publisher 的部署验证入口也必须 unavailable，直到独立 host/native verifier 的后续设计与实现完成。不存在 env/CLI “临时放行”。`capacity_ready=false`、`execution_enabled=false`、`paid_consumed=false`，OpenSpec38/41。

这是**新的待批准 API**。旧四个方法、journal96字节格式、路径、六个 sentinel、原始 raw v1 envelope、失败 Commit/不确定发布语义不变。

只新增一个 collector 和一个发布编排，不抽象成通用采样框架或签名服务。

使用独立 fixed Unix reader：`/run/switchback-engine/docker.sock`。仅允许四类 GET：`/v1.56/version`、`/v1.56/info`、`/v1.56/containers/json?all=1`、`/v1.56/containers/<64 lowercase hex>/json`；不支持过滤、limit、aliases、转义CID、其他query或写方法。无 env socket/TCP/DNS/proxy/redirect/旧socket fallback，不使用 Docker CLI。单请求5秒、dial2秒、response header3秒、header/body各<=1MiB；总轮次另受25秒合作式 deadline约束。

JSON 先做全层 duplicate-key/单根值/尾随内容与大小检查，再对白名单所需字段做 required/non-null/exact-type 校验；允许并丢弃其他 bounded 属性，不保留/log Env、mount secret、Engine error body。整数以 int64 范围校验再转 uint64，拒绝负数、零（要求正数时）、溢出、float/exponent 替代和 null；布尔必须显式，PidsLimit不可空。Labels最多64项，key/value各<=512 bytes。

只支持 Linux amd64/arm64 little-endian；其他平台在 native IO 前拒绝。同包复用既有 LP64 ABI与 geometry/FSGETXATTR/QUOTACTL_FD有限读取，不修改 created-only 入口、原诊断collector、observer、IPC或ABI常量。读取是同步的，所有FD与buffer由当前调用持有；ctx不能硬中断卡住的quota/fsync，返回后才检查并拒绝晚到值，不启动 detached goroutine或提前close仍在使用的FD。

key reader在确认regular inode前不能data-open FIFO/device；沿B1同类O_PATH pin→owned self-FD reopen，精确读取32并检测第33字节、canonical/held identity/mode/owner前后相等。先检查长度再调NewKeyFromSeed，派生public key必须等于Trust与manifest。只使用plain Ed25519和既有SignatureDomain+原Facts payload，不改用Ed25519ctx/ph。[^ed25519]

循环单线程串行，不排队重叠。配置/部署验证成功后才读key、构造Mapper并Open B1。首轮立即开始，后续每轮结束后等待5秒；整个采样attempt为25秒合作式context，保留原mapper/B1/PG的30秒窗口和未来2秒容差，不能声称系统调用硬25秒返回。

Abandon只避免**主动提前撤旧文件**；它不证明Create→Start必然在30秒内完成，也不刷新原PG received_at。若created持续超时，旧样本自然失效；仍保留原CID/owner并走原reconciliation，不为恢复采样而盲Start、重复Create、生成Removal或放宽时效。真实窗口闭合必须由后续exact-candidate验收证明，失败则单独设计显式状态协议。

CAP_SYS_ADMIN在Linux v6.8 project Q_XGETQUOTA权限分支中需要检查，仍受LSM。它不是“只读quota capability”；固定GET/IO路径是受信任程序约束，不能防被攻破进程滥用其整进程能力或Docker socket权限。新publisher可以接触guest的固定Docker socket和root私有key/journal；本威胁边界只适用于专用受信任单用户guest，不防恶意guest root/持Docker控制权Backend。[^quota]

本次若批准，只写未部署模板，不执行systemctl、mount、capset、key provisioning或任何syscall采样。Backend三项文件能力与Worker seccomp过滤保持不变，observer不增加全量采样或签名能力。实际部署须独立授权并核验正常拓扑private目录不挂给Backend/Worker、public整个目录只读挂载；0700不能替代可达性证明。

实际Linux/XFS/LSM/服务能力、host backing/writer inventory、真实key隔离、kernel quota、断电及Create→same-CID Start30秒衔接，均保留给独立真实候选窗口。离线sample标签、录制backend、签名成功与cross-compile都不能关闭它们。


## Controller execution boundaries

- Only checkout `/path/to/switchback`, BASE=HEAD `fbebfe754045bdbf1af40871a5018eed33743011`; no Git mutations/commits. Existing other worktrees are read-only.
- Root owns this plan and phase `.scratch/runtime-m1-native-b2-1020`; frozen approved-design.md is the requirements source. Task1 owns exactly allowed-task1.json (2 existing +10 new), Task2 exactly allowed-task2.json (11 new). No reorganization or unrelated fixes. Only one product/Go writer.
- Old932 product/design inputs and1738 Git-visible files are baseline. Only existing store.go/store_test.go may change;930 other product inputs and1736 other Git-visible files must keep exact bytes. New source21 + Root plan1 produce954 inputs /1760 visible at final completion.
- Every Go test or compile uses phase run-offline.py <task1|task2|controller> <fresh-label> /usr/local/go/bin/go test ...; credentials not inherited, GOPROXY/GOSUMDB off, GOTOOLCHAIN local, GOWORK off, GOFLAGS empty, GOMAXPROCS2, existing private cache. No Go command while another writer owns window.
- Freeze source bytes and sha256.json before every RED/final invocation; retain every attempt, raw JSONL/stderr/receipt, no overwritten directories. Final sources must equal their run input hashes. Build-fail is never runtime RED. Validate complete fixture independently before initial RED; previous B1 historical mistake must not recur.
- No VM/Engine/Worker/provider/real-PG start, package fetch, actual native sampling, integration tags, actual key/Trust/journal provisioning, fixed system path writes, service installation or generated-binary execution. Local net.Pipe/httptest fixture and owned temp files allowed; use existing offline deps only.
- Cross-package private constructors must remain private. Do not add a production test factory, signing oracle or bypass to achieve a test. Test loop ordering/exact outgoing raw bytes with a finite capturing Store port; test those bytes' original Verify/CheckFacts contract and B1 real temporary-file/Reader mechanics in their owning package. Report this compositional evidence separately from a real production/native assembly, which remains a future Linux window. A single offline test cannot be claimed to execute two packages' private constructors.
- Implementer writes full report/evidence index under phase/taskN, returns DONE/DONE_WITH_CONCERNS/NEEDS_CONTEXT/BLOCKED and STOPWRITING. Release product/Go window explicitly. Root freezes scoped diff and dispatches independent Spec/Quality review; no reruns by reviewer except a named unresolved concern with agreed window. Final whole-B2 review is separate.

## Task 1: Abandon pending numbers and collect complete native pool observations

**Files:** Modify only `internal/workerruntime/adapter/capacitypublication/store.go`, `store_test.go`. Create only `internal/platform/capacityprobe/{pool_collect_types.go,pool_collect.go,pool_engine.go,pool_engine_decode.go,pool_native.go,pool_native_linux.go,pool_native_other.go,pool_collect_test.go,pool_engine_test.go,pool_native_test.go}`. Existing DTO, created-only/blocked collector/ABI/IPC, Mapper and all other source remain unchanged.

**Interfaces consumed:** existing Store.usable/poison/checkState, six publication sentinels and newFixture test helper; existing capacityprobe PoolObservation/Pool*Readback/CreatedLayerDirectory/Quota, private createdLayerLinux geometry/attrs/quota/stat ABI methods, parseQuota(raw,project), createdLayerMountTable/FDInfo. Check exact current definitions before reuse; do not treat created-layer full sampler as a running sampler.

**Interfaces produced (exact):**

```go
func (s *Store) AbandonSequence(ctx context.Context, expectedSequence int64) error

type PoolPlan struct {
    AnchorPath string
    Core []PoolCorePlan
    Storage []PoolStoragePlan
}
type PoolCorePlan struct { ID, CgroupPath string; ContainerIDs []string }
type PoolStoragePlan struct { ID, Path string }
var ErrPoolUnstable = errors.New("pool_unstable")
var ErrPoolUnavailable = errors.New("pool_unavailable")
var ErrPoolInvalid = errors.New("pool_invalid")
func CollectPool(ctx context.Context, plan PoolPlan) (PoolObservation, error)
```

The plan is detached/copied, Core/Storage nonempty<=32, unique IDs/paths/CIDs and no overlapping storage paths, total inventory<=32. Paths canonical under Root, cgroups under existing Switchback core/workers subtrees. Layout does not accept observed quota/PID/provenance/success fields. Production CollectPool performs platform/context/plan validation then constructs fixed HTTP/native sources. Private collectPool(ctx,plan,get,k,now) is the only injected seam; no exported constructor/global hook. New exported API reports unsigned observations, never readiness.

- [ ] **Step 1: Scaffold APIs and legal fixtures; run executable RED.** Use existing real B1 newFixture for Abandon's positive. Independently validate pool fixture raw version/info/list/inspect, proc/cgroup/mount text and all geometry/attrs/quota arrays before invoking a rejecting collectPool scaffold. `TestPoolFixtureComplete` must pass before RED; retain its output. Do not seed a placeholder codec or invalid fixture just because a scaffold rejects before reading it.

```go
func TestStoreAbandonExecutable(t *testing.T) {
    f := newFixture(t); s := f.open(t); defer s.Close()
    n, err := s.ReserveSequence(context.Background()); if err != nil { t.Fatal(err) }
    raw := f.signedFacts(t,n,time.Now()); if err=s.Commit(context.Background(),raw);err!=nil{t.Fatal(err)}
    n,err=s.ReserveSequence(context.Background());if err!=nil{t.Fatal(err)}
    if err=s.AbandonSequence(context.Background(),n);err!=nil{t.Fatal(err)}
    r,err:=capacitysnapshot.NewReader(f.publicDirectory);if err!=nil{t.Fatal(err)}
    got,err:=r.Read();if err!=nil||!bytes.Equal(raw,got){t.Fatalf("old bytes lost: %v",err)}
    next,err:=s.ReserveSequence(context.Background());if err!=nil||next!=n+1{t.Fatalf("number reused: %d %v",next,err)}
}
```

Pool fixture is zero Worker plus one core member, and a separate one-Worker case: actual Root `/mnt/lima-sbquota`, `/anchor`, `/core`, `/product`; mount73/device8:7/XFS geometry256 version5 nonzero UUID; project100/101 and worker200; hard blocks20971520 (10GiB), full112 raw target/default0 and QSTATV160 flags0x30, three actual-recorded sample names qstatv/target_quota/default0_quota. Engine API1.56/version29.9.0,8 CPUs/32GiB, one core full64hex CID and an optional different Worker CID; PID101/202, startticks123, explicit running bools/started timestamps; exact 2CPU/4GiB/512 worker limits and existing registry label digests. Use concrete finite per-op fake read data, not a fake returning a preassembled successful observation. Source labels are test values, not Linux evidence. `TestPoolCollectExecutable` asserts resulting inventory/proc/FD/quota values and before/after actual call order, then tests zero output on a mutated primitive.

```sh
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task1 fixture /usr/local/go/bin/go test ./internal/platform/capacityprobe -run '^TestPoolFixtureComplete$' -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task1 red /usr/local/go/bin/go test ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitypublication -run '^(TestPoolCollectExecutable|TestStoreAbandonExecutable)$' -json -p=2 -count=1
```

Expected fixture exit0; RED exit1 with both positives compiled/executed/failed, no build-fail and empty stderr. Freeze exact pre-RED sources; do not proceed to GREEN without qualifying runtime failures.

- [ ] **Step 2: Implement Abandon using the existing state machine.** This code is the required control flow; use existing private helpers without rewriting old methods.

```go
func (s *Store) AbandonSequence(ctx context.Context, expectedSequence int64) error {
    if s == nil { return ErrPublicationInvalid }
    s.mu.Lock(); defer s.mu.Unlock()
    if err:=s.usable();err!=nil{return err}
    if ctx==nil || expectedSequence<=0 || s.pending==0 || expectedSequence!=s.pending{return ErrPublicationInvalid}
    if err:=ctx.Err();err!=nil{return s.poison(err,ctx,"abandon_context",false)}
    if err:=s.checkState();err!=nil{return s.poison(err,ctx,"abandon_state",false)}
    if err:=ctx.Err();err!=nil{return s.poison(err,ctx,"abandon_return",false)}
    s.pending=0
    s.reservedAt=time.Time{}
    return nil
}
```

Record/compare no create/write/chmod/sync/rename/unlink, exact old file bytes/ObservedAt, lock still held, journal unchanged high-water. Wrong/repeat/nil/zero/closed/failed preserve state; after Commit error no Abandon; concurrent Commit/Close/Reserve exact outcomes and race. Every state-read fault/cancel fences with fixed error/context identity. Crash before/after clear never reuses expected; restart reads existing journal.

- [ ] **Step 3: Implement independent Engine reader and parser.** pool_engine.go owns fixed Unix transport and four request forms in approved design, including all=1 exact. pool_engine_decode.go owns bounded recursive duplicate scanning and exact-case required JSON key/type extraction. Unknown ordinary properties are discarded, not copied into DTO/log. No last-write-wins/case-insensitive alias decoding for required fields. Decode int64 before unsigned conversion, reject null/missing/bad numbers/bools, canonical CID/image and bounded labels. Before/after census sorts by CID, compares all security projection fields. Do not filter transient/non-running containers out of a successful result. HTTP path validation precedes dial; private net.Pipe/fixture tests exercise same transport, method, redirect, bounds and sanitized errors as production.

- [ ] **Step 4: Implement finite native backend plus collecting sequence.** Use a small private per-op backend, not a general filesystem or a success Observation source. Methods cover boot, self/PID1 mnt/cgroup namespace, mountinfo, safe directory open/stat/reopen/close, same-FD geometry/attrs/quota/statfs, fixed cgroup leaf reads, and bounded process stat/cgroup reads. Linux concrete code may call existing private ABI methods for geometry/attrs/quota only. Its own directory walk validates every ancestor ownership/permissions, not only final inode. Other platforms reject before native IO.

Sequence: Started→global boot/ns/mount before→version/info/all+inspect before→hold Root/anchor/storage/worker parent/diff and cgroup FDs→before identities/geometry/attrs/statfs/cgroup/process→quota qstatv/target/default0 with real per-call timestamps→after process/cgroup/statfs/geometry/attrs/held and canonical identities→after full census/global identity/mount→Ended. Reject nil/zero/late/equal clock and return zero Observation on every failure. Do not synthesize Ended or provenance bools to fill missing data. Hold only bounded FDs, close each once even on errors. Actual directory comparisons ignore only mutable ctime/size/nlink in running state, retain stable ownership/type/dev/inode/mount and project/xflags. Parent/diff worker project must agree/nonzero/inherit and never use configured target ID in place of actual FSXAttr.

Process parser handles stat comm spaces/parentheses, validates PID and field22 ticks, unified cgroup exact0:: canonical host-relative path, before/after PID+ticks+group. No environ/cmdline. cgroup v2 files read from held actual directories, <=128 each, before/after exact contents. Namespace comparisons use actual guest host view; no private mount/cgroup namespace or config-derived live cgroup. Fstatfs signed fields checked before casts; actual mount/super options produce ordered union. Finite errors expose only three fixed sentinels/context identity, no body/path/raw nested cause. `pool_unstable` only recognized inventory change/non-running/process disappearance; malformed/config/permission/quota/identity/other IO are fatal classes.

- [ ] **Step 5: Complete behavior/fault/transport tests and run focused then final GREEN.** Cover malformed JSON/null/overflow/alias/dupkey/oversize/canary, list completeness/unowned/32+1, every captured native op before/after fault and cancellation, FD close, PID reuse/namespace drift/directory replacement, normal ctime/nlink changes, project drift, actual raw quota samples/default0 and snapshot clock. Tests use real net.Pipe/temporary-file mechanics where portable, synthetic native metadata otherwise; record those limits, never call real quota.

```sh
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task1 green /usr/local/go/bin/go test ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitypublication -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task1 final-race /usr/local/go/bin/go test ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitypublication -run 'TestStore.*Abandon|TestPoolConcurrent|TestPoolCancellation' -race -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task1 final-capacity /usr/local/go/bin/go test ./internal/platform/config ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitysnapshot ./internal/workerruntime/adapter/capacityobserver ./internal/workerruntime/adapter/capacitypublisher ./internal/workerruntime/adapter/capacitypublication -json -p=2 -count=1
```

All expected exit0/no fail/skip/build-fail/empty stderr; count top/child/leaf unique identities separately; focused race not summed into full totals. Run gofmt only owned Go files. No native/integration-tag or binary execution.

- [ ] **Step 6: Self-review and handoff.** Write task1/task-1-report.md, source snapshots/hashes, preservation check (unchanged baseline except two explicitly owned files/new10), full raw receipts, evidence-sha256.json (paths relative to task1, excludes itself). Include commands/results and stage-specific exact input sets, all attempts, actual syscall reuse/remaining proof limits. Freeze report then STOPWRITING. Root task review requires approved functional scope and quality; fix findings with covering tests and re-review before Task2.

## Task 2: Private signer and loop with disabled production entry

**Files:** Create only `internal/workerruntime/adapter/capacitypublisher/{publish.go,publish_config.go,publish_key.go,publish_key_linux.go,publish_key_other.go,publish_test.go,publish_config_test.go,publish_key_test.go}`, `cmd/switchback-capacity-publisher/{main.go,main_test.go}`, `deploy/experience/switchback-capacity-publisher.service`. Task1 files are read-only after review. Original Mapper, PG/Start, raw protocol, localproduct, Compose and all old units remain unchanged.

**Consumes:** `CollectPool(ctx, PoolPlan) (PoolObservation,error)`, `PoolCorePlan{ID,CgroupPath,ContainerIDs}`, `PoolStoragePlan{ID,Path}`, ErrPoolUnstable/Unavailable/Invalid; `Store.Open`, ReserveSequence, AbandonSequence, Commit, Close; original NewMapper/Map/Result, capacitysnapshot Trust/ReadTrust/ReadAdmissionManifest/Envelope/Verify/CheckFacts/SignatureDomain.

**Produces:**

```go
func Run(ctx context.Context) error // fixed production construction only
// Private finite orchestration dependency; *capacitypublication.Store implements it.
type publicationStore interface {
    ReserveSequence(context.Context) (int64,error)
    AbandonSequence(context.Context,int64) error
    Commit(context.Context,[]byte) error
    Close() error
}
// private publisherBinding uses schema, manifest_digest, core, worker_image_digest only.
const publisherBindingsVersion = "switchback.capacity.publisher_bindings.v1"
```

No public signer/config/test factory. Linux-only key backend is private; non-Linux/unsupported architecture rejects at production Run before fixed path IO. Private tests may construct a finite pipeline and key file source without adding production options.

- [ ] **Step 1: Write complete private-pipeline/key/config fixtures and executable RED.** Validate existing Mapper fixture with actual NewMapper/Map before RED; use only synthetic seed0x11 with its real derived public key and a complete exact bound config/admission evidence. Adapt test-only fixture paths to approved Root/core cgroup layout; original fixture source stays immutable. No production key/Trust/journal generated. Separate fixture sanity test succeeds before RunCycle positive fails against rejecting stub; key/config readers return actual valid synthetic test metadata/bytes, not placeholder parse results. Positive zero-Worker and one-Worker cases assert original signature domain, exact unchanged ObservedAt, actual Verify+CheckFacts, commit bytes and sequence.

```sh
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task2 fixture /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublisher -run '^TestPublisherFixtureComplete$' -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task2 red /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublisher -run '^TestPublisherExecutable' -json -p=2 -count=1
```

Expected fixture exit0; RED compiled positive leaves actually run and fail with empty stderr/no build-fail. Preserve exact snapshots and complete fixtures.

- [ ] **Step 2: Implement fixed config/key readers and actual private signer.** Production paths exactly config=/mnt/lima-sbquota/core/capacity-publisher-config, keys=/mnt/lima-sbquota/core/capacity-publisher-keys/ed25519.seed; original B1 dirs untouched. Config seven finite leaves: trust.json, admission-manifest.json, publisher-bindings.json, source-manifest.json, deployment-inventory.json, host-boundary-receipt.json, native-enforcement-receipt.json. Root directory0700/root:root; leaves0400/root:root regular/nlink1/bounded1MiB, canonical safe ancestor/entry/held comparisons. ReadTrust/ReadAdmissionManifest are structural helpers only. Bind exact manifest digest, core references and inventory worker image; copy aliases; no unknown/duplicate config fields or arbitrary paths.

Key data-open only after regular O_PATH pin on supported Linux, owned self-FD reopen, exact32/overflow33 read, root/mode0400/nlink1 and stable canonical inode/bytes. Length validation before NewKeyFromSeed; derived public equals Trust/manifest. Recheck fixed key/config byte+inode identity each cycle and before signing, no silent rotation. Seed/private buffers owned and best-effort cleared; no secure-erasure claims or secret hashes/logging. No GenerateKey in production and no HMAC reuse.

```go
// private signing body: f is from original Mapper, never supplied by an external request.
payload,err:=json.Marshal(f);if err!=nil{return nil,errPublisherInvalid}
message:=append([]byte(capacitysnapshot.SignatureDomain),payload...)
signature:=ed25519.Sign(privateKey,message)
raw,err:=json.Marshal(capacitysnapshot.Envelope{Version:capacitysnapshot.Version,KeyID:trust.KeyID,Payload:payload,Signature:signature})
if err!=nil||len(raw)==0||len(raw)>capacitysnapshot.MaxBytes{return nil,errPublisherInvalid}
v,err:=capacitysnapshot.Verify(raw,trust);if err!=nil||capacitysnapshot.CheckFacts(v.Facts(),trust)!=nil{return nil,errPublisherInvalid}
return raw,nil
```

Use private fixed sentinel wrappers instead of raw error causes; preserve context identity. No alternative signature variant or remarshal in Store.

- [ ] **Step 3: Implement loop and deployment closure.** Production Run validates ctx/platform and fixed structural configuration, then calls private verifyPublisherDeployment, which unconditionally returns publisher_deployment_unavailable (or ctx error) before key/Store/Engine/native calls. Private test construction can inject finite fixture sources but no exported/cmd/env bypass exists. Tests directly check this gate and inspect production construction path; Darwin public Run is unsupported, not a claimed Linux activation test.

Private loop startup validates deployment before opening key, Mapper and Store. Every cycle: recheck config/deployment/self scope; Reserve; create25-second attempt context; CollectPool after Reserve; Map with same number; recheck deployment/key/current time; sign; Commit exact raw. Cancel attempt context only after synchronous operations return, never detach. After success or confirmed ErrPoolUnstable+Abandon wait5 seconds using context-aware timer; no overlapping attempts/catch-up. Unstable burns number using still-valid parent context; old bytes/time untouched. Any Abandon/Reserve/Commit or fatal source/mapper/key/signing/deployment failure closes once and exits; Commit error never calls Abandon/retries number. Close error retains visibility uncertainty; no deletion=instant permit revoke claim.

Tests use a finite Store capture for ordering/returned envelope and real original Mapper/crypto; B1 owning tests exercise real Store/Reader byte and lifecycle compatibility. Explicitly list real full production assembly/native invocation as unexecuted. No public hooks to bridge private test packages.

- [ ] **Step 4: Add zero-parameter command and undeployed unit.** main accepts no arguments (except executable name), never reads stdin/env configuration, uses signal.NotifyContext, prints fixed reason only, returns nonzero on rejection. Private testable run function accepts only test context/args and a finite Run callback; actual main always binds capacitypublisher.Run. Callback is not a CLI/env switch.

Service exact settings: ExecStart=/opt/switchback/bin/switchback-capacity-publisher (no args), Type=simple, User=root, Group=root, Slice=switchback-core.slice, CapabilityBoundingSet=CAP_SYS_ADMIN, NoNewPrivileges=yes, RestrictAddressFamilies=AF_UNIX, RestrictNamespaces=yes, SystemCallFilter=~@mount @reboot @swap, Restart=no, UMask0077, LimitCORE0, KillMode=control-group, TimeoutStopSec10s, CPUQuota20%/CPUQuotaPeriodSec100ms/MemoryMax128M/MemorySwapMax0/TasksMax64. No [Install] enablement, no EnvironmentFile/ExecStartPre/key generation or namespace-changing hardening. Source template comment must state inactive/non-readonly capability and aggregate/target-verification limits. No systemctl or host mutation.

- [ ] **Step 5: Complete privacy/failure/timing/closure tests and final exact-source runs.** Cover config/key 31/32/33/64/zero/null/wrongpub/replace/FIFO/symlink/hardlink/owner/mode, finite key FD release, all startup/cycle failures/no-op before gate, zero/one Worker, transient created→Abandon→new-number running, original time unchanged and old file compatibility, 30sec/2sec clock boundaries, parent vs expired attempt cancellation, late synchronous read, no goroutine overlap, signature/commit failures no retry, closure once/error uncertainty, hostile enable env cannot bypass. Native source/control-plane assertions must have behavior, not merely contain-string reflection of implementation.

```sh
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task2 green /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublisher ./cmd/switchback-capacity-publisher -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task2 final-race /usr/local/go/bin/go test ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitypublication ./internal/workerruntime/adapter/capacitypublisher -run 'TestStore.*Abandon|TestPoolConcurrent|TestPoolCancellation|TestPublisherConcurrent|TestPublisherCancellation' -race -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task2 final-capacity /usr/local/go/bin/go test ./internal/platform/config ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitysnapshot ./internal/workerruntime/adapter/capacityobserver ./internal/workerruntime/adapter/capacitypublisher ./internal/workerruntime/adapter/capacitypublication ./cmd/switchback-capacity-publisher -json -p=2 -count=1
python3 .scratch/runtime-m1-native-b2-1020/run-offline.py task2 final-localproduct /usr/local/go/bin/go test ./internal/localproduct -run 'TestRuntimeCapacity|TestLoadRuntimeCapacity|TestInitialCreatedLayer' -json -p=2 -count=1
```

Expected all exit0/no fail/skip/build-fail/empty stderr. Format only Task2 Go sources, not unit through gofmt. Freeze report/index/source hashes and STOPWRITING after self-review.

- [ ] **Step 6: Controller final verification, broad review and seal.** After Task2 independent Spec/Quality approval, Root recounts all raw identities/unique top/child/leaf, checks exact-source inputs and all logs/stage/evidence hashes; final full totals are seven capacity+command packages plus localproduct=8 packages, race separate. Compare complete product delta to baseline932 (930 unchanged,2 modified,21 new), Root plan separately, all old1738 visible except two authorized sources, status/HEAD/branch/index. Capture gofmt -l and git diff --check. Through offline runner, compile publication+probe+publisher+command test binaries for Linuxamd64/arm64 with CGO_ENABLED0; never execute. Independent broad review on most capable model sees entire uncommitted B2 diff, not only Task2, and all evidence/cannot-verify limits. Resolve findings with tested bounded fixes and re-review. Final report/verifier/seal truthfully keep production gates closed and original experience loop open. No commit or runtime activation.
