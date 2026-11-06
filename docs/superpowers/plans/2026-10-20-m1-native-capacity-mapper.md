# M1 S4-A Native Capacity Mapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development. Execute the single integration task, retain runtime RED/GREEN/final receipts, then perform task review and whole-slice review. Steps use checkbox syntax.

**Goal:** Convert complete, typed finite observations into unsigned capacity Facts and independent inode diagnostics, with no new native/signing/PG/Start authority.

**Architecture:** One immutable concrete Mapper validates the complete census/window and actual raw readbacks, then calls the existing CheckFacts. DTOs remain in capacityprobe; conversion and private validation live in capacitypublisher. Existing raw schemas, diagnostic collectors and production factories remain unchanged.

**Tech Stack:** Existing Go, capacityprobe DTOs, capacitysnapshot Facts/config registry, capacityobserver pure quota mappers. No dependency, wire, migration or deployment change.

## Global Constraints

- Sole worktree `/path/to/switchback`; branch `codex/observability-master-integration-1009`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`. Parent checkout and all pre-existing dirty work are read-only.
- Approved spec `docs/superpowers/specs/2026-10-20-m1-native-capacity-publisher-design.md`, SHA256 `b9832a373d37e34c5e9ec8758774ed6d3cf8b91a7ccbe01e94c7a19ab6ceee9a`; user approved S4-A offline implementation via question `call_058419cd506745ae83fea5798fdf982a`.
- Only exact `m1_accounted_capacity_v1` revision1 is accepted by this NEW mapper. Existing nil/legacy/raw v1/v2/created-layer/IPC/PG/composition semantics remain unchanged.
- Fixed local_standard_v1: 2vCPU, 4GiB, 512PIDs, 10GiB; original 80% budget and original PG owner remain unchanged. No publisher, signing, key, file publication, PG call, permit or Start API is added.
- Result is unsigned parsing output, never Verified/native provenance/admission. Every error returns zero Result; cancellation retains errors.Is semantics. Full uint64 inode values remain independent diagnostics; default0 never fills target.
- Complete census is core bindings plus fully observed running workers. No non-running omission, invented cgroup, host-config substitute for actual cgroup, or generated Removal. Source/receipt/mount-writer and actual host/native enforcement remain separately unproven.
- Limits: inventory/core/storage/members<=32; each container labels<=32 with key/value<=512; mountinfo<=capacityprobe.MaxResponse; quota StateSamples<=64; scalar cgroup text<=128. Use current bounded label/path checks; no unbounded input/error text.
- No VM/Engine/PG/Worker/provider startup, installation/network fetch, native/integration-tag execution, paid call or Git index/branch/commit mutation. Go commands use phase run-offline.py only. Cross-compiled binaries are never executed.
- Protected913, Git-visible1719, dirty256. Eight NEW Go files in phase allowed-paths.json are the implementer's entire product write scope. Root owns this plan, phase metadata/progress and controller artifacts. One product and Go writer at a time. Earlier seals and tracked global progress are read-only.
- capacity_ready=false, execution_enabled=false, paid_consumed=false; OpenSpec38/41 and real PG/native/Start/UI obligations stay open. S4-B/C are excluded.

## Task 1: Complete typed observation and unsigned Mapper

**Files:** Create `internal/platform/capacityprobe/pool_types.go`; create `internal/workerruntime/adapter/capacitypublisher/{mapper.go,pool_identity.go,pool_quota.go,pool_cgroup.go,mapper_test.go,pool_quota_test.go,pool_cgroup_test.go}`. No existing source file changes.

**Interface:** The approved public NewMapper/Map/Result/CoreContainerBinding signatures are retained. DTO Go field spellings below complete the approved table. Add GraphDriver/UpperDir to the selected container inspect DTO so layer readback is compared with actual Inspect output; these are observed values, not new caller-selected paths. The current finite Engine parser expects API1.56, which this module pins; the historical generic diagnostic's API1.55 remains unchanged.

- [ ] **Step 1: Add the internal DTO and a reject-only scaffold.** In pool_types.go use package capacityprobe, time, and existing CreatedLayerDirectory/Quota/CreatedLayerSample. No JSON tags, marshalling, IO or convenience constructors:

```go
type PoolObservation struct {
    Started, Ended time.Time
    BootBefore, BootAfter [16]byte
    NamespaceBefore, NamespaceAfter uint64
    MountInfoBefore, MountInfoAfter []byte
    EngineBefore, EngineAfter PoolEngineReadback
    Anchor PoolAnchorReadback
    Core []PoolCoreReadback
    Storage []PoolStorageReadback
    Workers []PoolWorkerReadback
}
type PoolEngineReadback struct {
    ID, APIVersion, Version, DockerRoot, Driver string
    CPUCount, MemoryBytes uint64
    Containers []PoolContainerInspect
}
type PoolContainerInspect struct {
    ID, ImageDigest, Status, GraphDriver, UpperDir string
    PID uint64
    Running, Paused, Restarting, Dead bool
    StartedAt time.Time
    Labels map[string]string
    CgroupParent string
    NanoCPUs, MemoryBytes, MemorySwapBytes, PIDsMax uint64
    Privileged bool
    NetworkMode string
}
type PoolStatFSReadback struct {
    Type, BlockSize, FragmentSize, Blocks, Free, Available uint64
}
type PoolAnchorReadback struct {
    Directory CreatedLayerDirectory
    Before, After PoolStatFSReadback
    MountOptions []string
}
type PoolQuotaReadback struct {
    QStatV [160]byte
    StateSamples []CreatedLayerSample
    StateSamplesTruncated bool
    Target Quota
    Default0 [112]byte
}
type PoolStorageReadback struct {
    DomainID string
    Directory CreatedLayerDirectory
    Quota PoolQuotaReadback
}
type PoolCgroupIdentity struct { FSType, Device, Inode uint64 }
type PoolCgroupFiles struct { CPU, Memory, Swap, PIDs, Type string }
type PoolCgroupReadback struct {
    Path string
    Before, After PoolCgroupIdentity
    FilesBefore, FilesAfter PoolCgroupFiles
}
type PoolProcessReadback struct {
    ContainerID string
    PIDBefore, PIDAfter, StartTimeTicksBefore, StartTimeTicksAfter uint64
    CgroupPathBefore, CgroupPathAfter string
}
type PoolCoreReadback struct {
    CoreID string
    Cgroup PoolCgroupReadback
    Members []PoolProcessReadback
}
type PoolWorkerReadback struct {
    ContainerID string
    Process PoolProcessReadback
    Cgroup PoolCgroupReadback
    Layer CreatedLayerDirectory
    Quota PoolQuotaReadback
}
```

In mapper.go initially define the public types/signatures and return a fixed error from Map, so behavior tests compile. Do not count a build failure as RED. The final constructor has private copied Trust/core bindings/workerImageDigest/profile/configured fields, not public seams or options.

- [ ] **Step 2: Build a complete SYNTHETIC fixture and executable RED.** Use literal bytes below as building blocks and construct a zero-worker fixture plus an optional complete worker. No real filesystem/Engine/native/signing/PG use. All runtime tests cross NewMapper/Map; private mapAt is reserved for exact-clock tests.

```go
// Synthetic geometry, not a kernel receipt.
var g [256]byte
binary.LittleEndian.PutUint32(g[0:4],4096)
binary.LittleEndian.PutUint32(g[12:16],4)
binary.LittleEndian.PutUint64(g[32:40],1<<20)
copy(g[64:80], []byte{1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16})
binary.LittleEndian.PutUint32(g[88:92],5)
// A physical quota record, not an admission token.
var raw [112]byte
raw[0],raw[1]=1,2
binary.LittleEndian.PutUint32(raw[4:8],100)
binary.LittleEndian.PutUint64(raw[8:16],20971520)
binary.LittleEndian.PutUint64(raw[16:24],20971520)
binary.LittleEndian.PutUint64(raw[24:32],0)
binary.LittleEndian.PutUint64(raw[40:48],8)
binary.LittleEndian.PutUint64(raw[48:56],math.MaxUint64)
// QStatV: all padding zero; project ACCT+ENFD on.
var qs [160]byte
qs[0]=1
binary.LittleEndian.PutUint16(qs[2:4],0x30)
```

Freeze fixture values: canonical UUID strings corresponding to actual16-byte fixture arrays; namespace17, mountID73, device8:7; Engine API1.56/Version29.9.0/overlay2, CPUCount8, MemoryBytes32GiB. Anchor `/pool/anchor`, statfs4096/4096, blocks1<<24, free1<<23, available1<<22; after available one block lower. Trust DockerRoot `/pool/docker`; static distinct paths `/pool/core-data` project100 and `/pool/product` project101, hard10GiB each. Coverage core/images/logs/build→core-data and product/working_copy/cache→product, exactly one DTO per physical domain. Anchor project0/flags0, storage/layer flags0x200; all directories stable, true finite walk/recheck records, distinct positive inodes, geometry UUID as above. Mountinfo includes a valid root record, XFS mount73 at `/pool` with root `/` and device8:7; no overmounts.

Trust.Core one actual aggregate `/sys/fs/cgroup/switchback.slice/switchback-core.slice`, CPU120000/100000, memory3GiB, PIDs1024, swap0. One core CID of64 `a` with fixed image sha256 of64 `b`, matching CoreID, running PID101; member path is an actual child `core.slice/core.scope`, consistent positive PID/start ticks. cgroup2 type0x63677270, distinct positive device/inodes, domain type and exact stable scalar files.

Worker fixture CID of64 `c`, image sha256 of64 `d`, labels exactly current Docker runtimeLabels grammar, session-one/run-one/runtime-one/generation1, exact registry resource and active network refs. Config2e9 NanoCPUs/4GiB memory/memswap/512PIDs. Actual cgroup `/sys/fs/cgroup/switchback.slice/switchback-workers.slice/worker.scope`, quota200000/100000; actual process PID202/start ticks; UpperDir `/pool/docker/overlay2/worker-layer/diff`, GraphDriver overlay2, layer project200 and hard/soft10GiB. Domain ID `layer-runtime-one`, derived from actual runtime label. State StartedAt precedes sample Started.

Start with at least two positive behavior assertions that fail against the reject-only Map: zero-worker exact full Facts success and full-uint64 hard0 diagnostic for a complete running worker. Assert literal expected bytes/counters, source buffers unchanged and Facts.Removals empty. Use named child identities if a table is clearer. Freeze all existing allowed files for every stage, then run:

```bash
python3 .scratch/runtime-m1-native-s4-a-1020/run-offline.py impl red /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublisher -run '^TestMapperExecutable' -json -p=2 -count=1
```

Expected: runtime exit1, two executed failed leaf identities, no build-fail. Preserve all attempts.

- [ ] **Step 3: Pin configuration and define the common pipeline.** Validate Trust and exact nonnil guarantee using the existing registry, validate resource/network registry digests and fixed resource values. Validate counts, core CID/image/CoreID bindings, duplicate bindings and worker image digest. Clone all retained configuration storage; zero-value/nil Mapper must reject. Select only fixed reason strings from the approved six-code set; do not return arbitrary nested errors. Code shape:

```go
type CoreContainerBinding struct { ContainerID, ImageDigest, CoreID string }
type Result struct {
    Facts capacitysnapshot.Facts
    StorageInodes map[string]capacitysnapshot.CreatedLayerInodeObservation
    LayerInodes map[string]capacitysnapshot.CreatedLayerInodeObservation
}
func (m *Mapper) Map(ctx context.Context, o capacityprobe.PoolObservation, seq int64) (Result,error) {
    return m.mapAt(ctx,o,seq,time.Now())
}
// mapAt checks context before/after, calls the validation stages below once,
// and discards all partial values on every error. Only mapAt owns Result assembly.
```

Private functions and return contracts:

| Private function | Exact output and required algorithm |
| --- | --- |
| `window(o PoolObservation, seq int64, now time.Time) error` | seq>0, positive <=30s interval, earliest Started age<=30s, Started/Ended<=now+2s; nonzero/equal boot/ns/mountinfo, valid byte limits; fixed observation_invalid/stale/identity_drift only |
| `census(o PoolObservation) (map[string]PoolContainerInspect,error)` | validate Engine fields/API1.56/UUID/root/overlay2/units; each inventory<=32; copy/sort or map by exact CID, detect duplicates before comparing all selected fields and labels by value; no input mutation; return canonical census |
| `mounts(raw []byte) ([]mountRecord,error)` | strict mountinfo grammar below; no IO |
| `directory(d CreatedLayerDirectory, expectedPath string, records []mountRecord) (directoryFacts,error)` | finite flags and actual identity/geometry/fsxattr/project rules below; unsigned facts only |
| `cgroup(c PoolCgroupReadback) (capacitysnapshot.CoreDomain,error)` | parse actual scalars and identity; caller fills ID only after the returned actual path/limits match the expected core or worker |
| `quota(q PoolQuotaReadback, project uint64, worker bool, started,ended time.Time) (capacitysnapshot.StorageQuota,CreatedLayerInodeObservation,error)` | original pure evidence helpers + full sample window + byte/fulluint inode checks below |
| `process(p PoolProcessReadback, i PoolContainerInspect, parent string) error` | CID/PID/start ticks/path consistency, actual path inside the expected parent subtree; no HostConfig substitution |

The package-local shortened type names in this table refer to capacityprobe or capacitysnapshot imports, not new public wrapper types. Local `mountRecord` contains id,device,root,point,fstype,options; local `directoryFacts` contains actual path/FSUUID/device/mount/project/xflags/inode. No private function can grant authority or construct Verified.

- [ ] **Step 4: Implement identity, raw quota and cgroup validation.** Follow these complete decision rules, keeping each implementation in its responsibility file:

| Area | Required decisions and value construction |
| --- | --- |
| strings/numbers | canonical absolute clean non-root paths; bounded labels, no NUL/CR/LF/tab; decimal tokens use digits-only strconv.ParseUint with explicit bit/range checks, no sign/hex/exponent; CID/digest regex exact64hex/sha256 |
| Engine | before/after exact equality after canonical census, API1.56, 3-part numeric version<=64, UUID ID, root equals Trust; CPUCount>0 and <=MaxInt64/1000, multiply1000; memory>0<=MaxInt64; actual values populate output |
| mountinfo | len1..MaxResponse, trailing newline/no NUL; exactly one ` - ` separator, at least6pre and3post fields, no empty tokens, positive unique IDs and decimal device components; decode only kernel octal escapes040/011/012/134 and reject unsafe resulting paths. For each observed directory choose deepest containing mountpoint; ambiguous deepest record rejects; must match Trust MountID/device, root `/`, XFS and prjquota/pquota. An overmount on a relevant directory cannot be hidden by a different recorded mountID |
| directory | Before==After identity, nonzero inode, directory mode, canonical expected path, Trust mount/device; both geometry version5, UUID nonzero and equalBeforeAfter/identity/Trust, blocksize power-of-two512..65536, nonzero AG/data geometry and zero reserved tail120..255. Before/after FSXAttr flags/project equal, raw padding20..27 zero. Required finite walk/reopen/identity/same-FD records true, but never treated as native proof. Reject shared physical directory inode across anchor/static/layer roles; raw buffers are not mutated |
| anchor | project0, no projinherit/realtime, actual statfs XFS, bsize/frsize positive/equal, before/after geometry sizes stable, bavail<=bfree<=blocks. Multiply with overflow guard and <=MaxInt64, choose min(before,after) available. Output mount options cloned from validated actual list; zero availability preserved |
| core | IDs and scalar readback exactly cover Trust.Core; before/after cgroup2 node equality, positive device/inode, domain type. Actual member list exactly covers fixed core bindings for that ID, actual process paths within aggregate, running PID/state/image checked; Worker identity keys forbidden in core classification. Reject duplicate physical cgroup nodes across distinct core/worker domains |
| cgroup text | every raw field1..128bytes, FilesBefore==FilesAfter. CPU exactly two decimal fields separated by whitespace, positive finite quota/period; quota<=MaxInt64/1000. memory/pids positive<=MaxInt64, swap decimal0, type trimmed domain. Core compares all values to Trust; worker actual CPU ratio must exactly2 (division+remainder, no overflow), memory4GiB/PIDs512; actual HostConfig must independently match profile2e9/4GiB/4GiB/512 |
| quota sample | <=64, not truncated; all sequences i+1, source linux_syscall, outcome observed, nil errno, positive intervals inside overall window, no overlap/backwards; exactly one qstatv. Call MapQuotaStateEvidence on an explicit parsing projection of QStatV/Samples only; require project flags0x30, no unknown flags. This projection is not a Verified/native object |
| target | call MapQuotaInodeEvidence(Target,expected positive project). Validate Default0 rawlen112/version1/type2/project0 without using its limits. Checked block multiplication512 and <=MaxInt64, hard>0 and used<=hard. Worker additionally raw soft @16 equals raw hard and hard bytes10GiB; static hard must match pinned Trust. Build StorageQuota from actual raw/state fields, not Trust constants |
| inode | classify with existing AssessInodeEvidence(StorageQuotaV2{HardInodes,UsedInodes}); positivehard+used>hard rejects, hard0 keeps original uint64 and InodeBoundUnproven. Return original counters and assessment; no narrowing or multiplication |
| workers | exact running status/PID, false paused/restarting/dead/privileged, positive StartedAt<=Started, no host network. Labels require managed=worker-runtime, exact current resource/network tuple and canonical positive generation<=MaxInt64; session/run/runtime bounded. Actual image equals constructor pin. Worker DTO/Inspect/Process IDs agree. Actual layer path equals Inspect.UpperDir, overlay2 and valid `<DockerRoot>/overlay2/<segment>/diff` layout; no l/dot traversal. Actual project not shared with any other static/layer domain. DomainID=`layer-`+runtime ID; no constructed native numbers |
| final Facts | identity values must first be constructed from actual Engine/boot/ns/directory geometry and equal Trust; manifest digest comes only from copied server Trust. Core/storage/containers are freshly built in stable ID order. Storage expected hard/IDs/path checked against actual readbacks; do not copy Trust.Storage and fillused. Removals is empty. Call CheckFacts and return zero on rejection; never weaken its existing no-overlap rule |

The managed label literal is `worker-runtime`, from current docker.managedWorkerRuntime. Confirm against current source before implementing. If a fixture disagrees with existing profile digests, derive the digest from the fixed registry rather than inventing it. This is not caller-selected policy.

- [ ] **Step 5: Extend behavior regressions.** All tests must be literal expected results/rejections through the public Interface. Use one immutable baseline fixture builder and deep-clone mutations; do not compare implementation output against another copy of the implementation. Include these named cases:

```go
// The quota matrix uses literal results, not computed expected classifications.
cases := []struct{ name string; hard,used uint64; ok bool; assessment capacitysnapshot.InodeEvidenceAssessment }{
    {"zero_empty",0,0,true,capacitysnapshot.InodeBoundUnproven},
    {"zero_used",0,2,true,capacitysnapshot.InodeBoundUnproven},
    {"zero_max",0,math.MaxUint64,true,capacitysnapshot.InodeBoundUnproven},
    {"positive_below",2,1,true,capacitysnapshot.InodesObserved},
    {"positive_equal",2,2,true,capacitysnapshot.InodesObserved},
    {"positive_over",1,2,false,""},
    {"signed_boundary",math.MaxInt64,math.MaxInt64,true,capacitysnapshot.InodesObserved},
    {"unsigned_boundary",uint64(math.MaxInt64)+1,uint64(math.MaxInt64)+1,true,capacitysnapshot.InodesObserved},
    {"unsigned_max",math.MaxUint64,math.MaxUint64,true,capacitysnapshot.InodesObserved},
}
```

Cross zero/positive/different/max default0 values with representative target cases. Map must preserve raw input and output counters, with zero entire Result on failure. Other matrices:

- configuration: nil/legacy/wrong policy fields, duplicate bindings, unknown CoreID, malformed images/counts, caller mutates all retained Trust/core fields after construction, returned maps/slices do not affect another Map or source input;
- census: shuffled equivalent order passes without mutating source; unknown/duplicate/missing/extra/core-worker overlap/labels-as-core, image/label/state/resource drift; 0/1/2workers; 33items; oversize labels;
- actual identity: boot/ns/mount table/device/UUID/geometry/version/reserved flags/raw project/reopen/inode corruption, same inode/cgroup alias and overmount, anchorunits/free/available/overflow including legal0;
- cgroup: max/negative/hex/exponent/extra tokens/empty/oversize/type threaded/swapnonzero, scalarbeforeafter differences, wrongparent/PID/startticks, CPUperiodoverflow/remainder, memory/PIDs mismatch;
- raw: target/default0 version/type/project/RawHex/counter mismatch, soft/hard/used limits, state missing/duplicated qstatv, truncated, overlapping or late failed sample after validprefix, unknown flags/padding;
- clock: exact30s/2s boundaries and one-nanosecond excess with private mapAt, public nil/cancelled context and unconfigured mapper, ObservedAt==Started even when Ended later; no partial Facts or diagnostics on any error;
- authority/compatibility: only new files, no calls to signing/PG/native/Start or Verified construction; existing source and gates exercised in final suites.

- [ ] **Step 6: GREEN and final exact-source verification.** gofmt all eight files. Freeze every existing allowed file and its SHA256 before/after each stage; all attempts remain. Run only via runner with GOPROXY/GOSUMDB off, GOTOOLCHAIN local, GOWORK off, GOFLAGS empty, GOMAXPROCS2 and existing private cache. No ambient PG/provider credentials.

```bash
python3 .scratch/runtime-m1-native-s4-a-1020/run-offline.py impl green /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublisher -json -p=2 -count=1
python3 .scratch/runtime-m1-native-s4-a-1020/run-offline.py impl final-capacity /usr/local/go/bin/go test ./internal/platform/config ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitysnapshot ./internal/workerruntime/adapter/capacityobserver ./internal/workerruntime/adapter/capacitypublisher -json -p=2 -count=1
python3 .scratch/runtime-m1-native-s4-a-1020/run-offline.py impl final-localproduct /usr/local/go/bin/go test ./internal/localproduct -run 'TestRuntimeCapacity|TestLoadRuntimeCapacity|TestInitialCreatedLayer' -json -p=2 -count=1
```

Expected final exits0, run/pass identities equal and unique, stderr empty, no fail/skip/buildfail. Existing capacityprobe native-state-machine tests use recording backends; the only direct native entry receives invalid paths and the current Darwin stub returns unsupported, not a real Linux probe. No integration tags are executed. Root later owns two Linux publisher test-binary compile-only commands for amd64/arm64, never execution.

- [ ] **Step 7: Self-review and handoff.** Check all obligations and `git diff --check`, preserve protected913 and rootplan, no nonallowlist mutation. Write `.scratch/runtime-m1-native-s4-a-1020/impl/task-1-report.md`: final API/semantics, exact changed paths/hash, RED/GREEN/final commands and unique package/test counts, original raw receipts and stage sources, unresolved concerns, remaining native/PG/Start limits. Return DONE/DONE_WITH_CONCERNS/BLOCKED/NEEDS_CONTEXT and STOPWRITING. No commits. Release the product/Go window explicitly.

## Controller closeout

- [ ] Freeze whole-S4-A delta against protected913/new8 baseline; rootplan remains separately owned and hashed.
- [ ] Independent task Spec/Quality review from approved brief, report, full frozen diff and raw receipts; resolve findings with tested fixes.
- [ ] Independently check source/run/hash identities, run two Linux publisher compile-only targets and format/diff checks.
- [ ] Broad whole-S4-A final review with most capable model; resolve concrete findings and cannot-verify items without runtime overclaims.
- [ ] Report/current progress/read-only verifier and seal; keep all production/native/realPG/Start/UI flags open.
