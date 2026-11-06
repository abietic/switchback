# M1 capacity publication Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax. This session uses one coherent Store task, followed by task and whole-slice independent reviews. No commits or global-ledger writes.

**Goal:** Implement the approved B1 durable sequence and atomic publication Store with executable RED, real temporary-directory interoperability and bounded fault/cancellation coverage.

**Architecture:** A new capacitypublication package owns a detached Trust, fixed safe directory FDs, stable advisory-lock FD, one mutex and one pending sequence. The journal is persisted before that sequence can be returned; Commit verifies an already-signed raw v1 envelope and publishes its exact bytes. No key, signer, native capacity sampler, daemon, PG or production composition is added.

**Tech Stack:** Go standard library, existing golang.org/x/sys/unix, existing config/capacitysnapshot. Linux amd64/arm64 production entry, Darwin offline tests through private seams. No new dependencies or network fetch.

## Global Constraints

- Sole checkout `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`, branch `codex/observability-master-integration-1009`. Parent checkout, pre-existing dirty changes, index, HEAD, branches and tracked `.superpowers/sdd/progress.md` are read-only.
- Approved spec `docs/superpowers/specs/2026-10-20-m1-capacity-publication-store-design.md`, SHA256 `c7d1334f787ce53937eac16379e1bba7099238b9d9c963e961e9dd72d124e178`. Direct user answer `接受 B1，进入离线实施（推荐）` to question `call_250fdbede99a43ac9ef433c6cf9043b0` authorizes this source/offline slice only.
- Public Interface: `Open(trust capacitysnapshot.Trust) (*Store,error)`, `(*Store).ReserveSequence(context.Context) (int64,error)`, `(*Store).Commit(context.Context,[]byte) error`, `(*Store).Close() error`. No public path/UID/backend/clock/factory/callback override. All configuration retained by Store is detached.
- Production paths fixed: `/mnt/lima-sbquota/core/capacity-publication-private` (root:root0700), `/mnt/lima-sbquota/core/capacity-publication-public` (root:root0750). Leaves: private `writer.lock`0600/0bytes, `sequence.bin`0400/96bytes, `sequence.next`0400/<=96; public `capacity.json`0440/1..1MiB, `capacity.next`0440/<=1MiB. Temporary creation0600. Regular leaves nlink1; nofollow/CLOEXEC, safe ancestors, XFS/Trust device/mount. Unknown entries reject.
- No mkdir, repair owner/mode, missing-journal initialization, key generation/read/signing, native capacity probe, PG, Start, integration-tag execution, VM/Engine/Worker/provider start, system fixed-path writes or network package fetch. Only owned synthetic test directories may be written during tests. In-memory SYNTHETIC keys/Trust/journal fixture bytes are allowed; no deployment provisioning.
- Exact nonnil `m1_accounted_capacity_v1@1` required. Original resource2vCPU/4GiB/512PIDs/10GiB,80% budget and original PG owner unchanged. B1 never manufactures native provenance or a grant.
- Every method serializes through the private Store mutex. At most one pending sequence. No same-number retry. Reserve Busy with existing pending has zero IO and does not invalidate the original pending. IO failure poisons that Store. Every Commit error poisons it; after rename starts, any error/cancellation is Uncertain. Close fences calls, withdraws only identified unchanged own capacity entry, fsyncs public dir, releases lock last, is idempotent and retains first close result.
- Error sentinels: ErrPublicationInvalid/Busy/StateUnavailable/IOFailed/Uncertain/Closed. Error text fixed `publication_invalid`, `publication_busy`, `publication_state_unavailable`, `publication_io_failed`, `publication_uncertain`, `publication_closed`; context cancellation/deadline retain errors.Is. No raw path/cause/body/secret text. Private bounded stages only; no logging API or resource-failure/Task translation.
- Protected923 inputs, Git-visible1729 files, dirty259 standard entries. Seven NEW Go files in phase allowed-paths.json are implementer scope. Root owns this new plan, phase artifacts/ledger and controller checks. One product and Go writer. Earlier seals stay immutable.
- All Go invocations use `.scratch/runtime-m1-publication-b1-1020/run-offline.py`, GOPROXY/GOSUMDB off, local toolchain, GOWORK off, empty GOFLAGS, GOMAXPROCS2 and existing private cache. No ambient PG/provider credentials. Linux compile-only binaries never execute.
- capacity_ready=false, execution_enabled=false, paid_consumed=false; OpenSpec38/41 unchanged. B2 native/signing/deployment and S4-C/realPG/Start/UI remain excluded/open.

## Task 1: Coherent publication Store and its crash protocol

**Files:** Create only `internal/workerruntime/adapter/capacitypublication/{store.go,journal.go,files_unix.go,files_other.go,store_test.go,journal_test.go,files_test.go}`. This is one task because the codec, ownership tracker and two-directory update protocol form one state machine; the deliverable is the complete Store, not a standalone generic journal library.

| File | Responsibility |
| --- | --- |
| store.go | six sentinels/private error wrapper, immutable Trust, mutex/lifecycle, public Interface and envelope checks |
| journal.go | exact96-byte codec and binding; no IO or public codec API |
| files_unix.go | finite concrete FD operations for Linux and Darwin test mechanics, safe path/entry identity, lock/temporary/rename/fsync/cleanup |
| files_other.go | unsupported platform backend; no fallback or platform-independent unsafe IO |
| store_test.go | complete synthetic fixture and public Reserve/Commit/Close behavior, faults, cancellation/concurrency |
| journal_test.go | independent literal journal vectors and mutation matrix |
| files_test.go | syscall recorder and owned temporary-directory tests; safe entry/lock/FD ownership |

- [ ] **Step 1: Scaffold the exact Interface and complete fixture.** The initial Reserve/Commit reject, so two positive behavior tests compile and fail at runtime. Define all seven source files/build constraints before RED. Open's production guard must reject non-Linux or unsupported architecture before any fixed-path IO. Private `openStore`/finite filesystem seam and private test clock may construct test Stores; production Open always passes constants/native filesystem/time.Now and cannot accept test options.

```go
var (
    ErrPublicationInvalid = errors.New("publication_invalid")
    ErrPublicationBusy = errors.New("publication_busy")
    ErrPublicationStateUnavailable = errors.New("publication_state_unavailable")
    ErrPublicationIOFailed = errors.New("publication_io_failed")
    ErrPublicationUncertain = errors.New("publication_uncertain")
    ErrPublicationClosed = errors.New("publication_closed")
)
// The private wrapper's Error returns reason.Error(), not its nested IO text.
// Unwrap exposes only the fixed sentinel and, when present, ctx cancellation.
type publicationFailure struct { reason error; contextCause error; stage string }
func (e *publicationFailure) Error() string { return e.reason.Error() }
func (e *publicationFailure) Unwrap() []error {
    if e.contextCause == nil { return []error{e.reason} }
    return []error{e.reason,e.contextCause}
}
```

The finite private IO seam covers open directory/existing leaf/exclusive temporary, FD/entry metadata, bounded read/write, chmod, flock, sync, renameat, unlinkat, bounded directory listing, close and canonical-directory recheck. Model only these operations, not a reusable filesystem framework. Production owns the concrete constructor; no global mutable hook. Stable metadata includes device/inode/mount/type/mode/uid/gid/nlink/size; directory identity excludes own-write mtime/ctime. Linux mount ID comes from bounded actual `/proc/self/fdinfo/<owned-fd>`; statfs.Fsid is not a UUID. Only XFS type/device/mount required here; B2/deployment still owns actual FSUUID/source/mount-writer proof.

For Linux existing leaves, validate type without opening a FIFO/device for data: an O_PATH|NOFOLLOW held handle can establish regular inode, followed by reopening that pinned `/proc/self/fd/<owned-fd>` with CLOEXEC/NONBLOCK and comparing identities. This internal self-FD path is generated only from a live held FD, not an accepted path or filename. Safe nofollow directory walks and fixed actual entry rechecks remain required. Darwin actual-temp tests exercise ordinary file mechanics via private seam and do not prove this Linux behavior. Close each numeric FD once even when close reports error.

Fixture helper `newFixture(t)` owns an isolated test directory pair and seeded96-byte journal, copied Trust and an in-memory deterministic SYNTHETIC Ed25519 seed (32 bytes0x11). It returns the paths, public-key-only Trust, private test signing helper, filesystem seam and `open()` function. The production Store never receives the seed/private key. A temp filesystem wrapper may synthesize only root UID/GID/XFS/mount metadata to exercise state mechanics on macOS; actual inode/type/mode/nlink/bytes/write/rename/lock operations still execute. Production Open has no such override. A separate recorder can inject individual syscall failures/cancellation and assert exact call order.

Trust fixture: key_id=`publication-synthetic`; full fixed guarantee from config registry; identity Engine/FS/boot canonical synthetic UUIDs, DockerRoot=`/mnt/lima-sbquota/docker`, mountID73/device8:7, namespace17; anchor=`/mnt/lima-sbquota/anchor`. Core aggregate=`/sys/fs/cgroup/switchback.slice/switchback-core.slice`, CPU120000/100000, memory3GiB, PIDs1024. Two nonoverlapping storage domains `/mnt/lima-sbquota/core` project100 and `/mnt/lima-sbquota/product`101, hard10GiB each, quota fields linux_xfs_quotactl_v1/version1/flags2/state1/flags0x30/blocks20971520/used0/xflags0x200. Coverage core/images/logs/build→core, product/working_copy/cache→product. Facts use actual fixture literals: CPU8000/memory32GiB, anchoravailable16GiB, matching core/storage, empty nonnil containers/removals, ObservedAt taken after Reserve. Fixture may sign malformed variants only in `_test.go`.

```go
func TestStoreExecutableRestart(t *testing.T) {
    f:=newFixture(t); s:=f.open(t)
    n,err:=s.ReserveSequence(context.Background())
    if err!=nil || n!=1 { t.Fatalf("reserve=%d err=%v",n,err) }
    if err=s.Close();err!=nil { t.Fatal(err) }
    s=f.open(t); defer s.Close()
    n,err=s.ReserveSequence(context.Background())
    if err!=nil || n!=2 { t.Fatalf("restart=%d err=%v",n,err) }
}
func TestStoreExecutableCommit(t *testing.T) {
    f:=newFixture(t); s:=f.open(t); defer s.Close()
    n,err:=s.ReserveSequence(context.Background());if err!=nil { t.Fatal(err) }
    raw:=f.signedFacts(t,n,time.Now())
    if err=s.Commit(context.Background(),raw);err!=nil { t.Fatal(err) }
    reader,err:=capacitysnapshot.NewReader(f.publicDirectory);if err!=nil { t.Fatal(err) }
    got,err:=reader.Read();if err!=nil || !bytes.Equal(raw,got) { t.Fatalf("read mismatch: %v",err) }
    v,err:=capacitysnapshot.Verify(got,f.trust);if err!=nil { t.Fatal(err) }
    if err=capacitysnapshot.CheckFacts(v.Facts(),f.trust);err!=nil { t.Fatal(err) }
    if v.Facts().Sequence!=1 { t.Fatal("wrong sequence") }
}
```

- [ ] **Step 2: Freeze sources and run actual RED.** Capture `impl/red-sources/<repo-path>` and sha256.json for all seven files, then run the exact runner below. Expected exit1 with both positive leaf tests executed and failed, no build-fail, empty stderr. Do not count compiler errors/setup failures as RED; preserve every attempt.

```sh
python3 .scratch/runtime-m1-publication-b1-1020/run-offline.py impl red /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublication -run '^TestStoreExecutable' -json -p=2 -count=1
```

- [ ] **Step 3: Implement the exact private journal codec and detached binding.** Binding is SHA256(domain bytes including one actual NUL + json.Marshal(cloned Trust)). Copy key/Core/Storage/Coverage and policy pointer, reject legacy/nil policy using ValidateTrust plus config.RuntimeCapacityGuarantee.Validate; Core/Storage<=32. Use signed high-water only after checking raw uint64<=MaxInt64. No journal repair or migration.

```go
const journalSize=96
func encodeJournal(binding [32]byte, high int64) ([journalSize]byte,error) {
    var b [journalSize]byte
    if high<0 { return b,ErrPublicationStateUnavailable }
    copy(b[:8],"SBCAPSEQ")
    binary.LittleEndian.PutUint32(b[8:12],1)
    copy(b[16:48],binding[:])
    binary.LittleEndian.PutUint64(b[48:56],uint64(high))
    sum:=sha256.Sum256(b[:64]);copy(b[64:],sum[:])
    return b,nil
}
func decodeJournal(raw []byte,binding [32]byte) (int64,error) {
    if len(raw)!=journalSize || string(raw[:8])!="SBCAPSEQ" || binary.LittleEndian.Uint32(raw[8:12])!=1 || !bytes.Equal(raw[16:48],binding[:]) { return 0,ErrPublicationStateUnavailable }
    for _,v:=range append(bytes.Clone(raw[12:16]),raw[56:64]...) { if v!=0 { return 0,ErrPublicationStateUnavailable } }
    sum:=sha256.Sum256(raw[:64]);if !bytes.Equal(sum[:],raw[64:96]) { return 0,ErrPublicationStateUnavailable }
    n:=binary.LittleEndian.Uint64(raw[48:56]);if n>math.MaxInt64 { return 0,ErrPublicationStateUnavailable }
    return int64(n),nil
}
```

Independent literal vector: binding32bytes0x11, high-water7 yields hex `53424341505345510100000000000000111111111111111111111111111111111111111111111111111111111111111107000000000000000000000000000000262852e7d7a91adba609219472df41551461952c82ebb377706b91c5bc0ba27d`. Verify encode equals literal and decode literal yields7. Mutate magic/version/reserved/binding/hash/length/negative encode/MaxInt64+1 and assert fixed failure; do not derive expected checksum by invoking the tested encoder.

- [ ] **Step 4: Implement Open and ownership tracking.** Private helpers must perform this order: guard platform/exact copied Trust; safe directory walk and stable canonical identities; bound directory listing to at most8 entries; open existing exact0byte lock with safe ownership/mode/nlink and nonblocking flock; recheck lock entry; bounded read+validate96byte state and binding. Only after valid state clean safe stale next leaves, unlink+dir-sync without promotion. If an old capacity entry exists, Verify+CheckFacts, Containers<=32, Removals empty, sequence<=high-water; freshness/reservedAt deliberately not used for restart withdrawal. Remember exact inode/content, unlink only if unchanged, sync public. Unknown file, wrong metadata/binding/sequence, symlink or missing state reject without deleting that entry. All failure paths close owned descriptors and release lock last; no lock unlink.

Track expected journal bytes and inode, and the exact owned capacity inode/content fingerprint. Revalidate full journal bytes plus inode before each state-changing operation, not only the in-memory high-water. An unexpected capacity entry cannot be overwritten or withdrawn. Directory FDs stay open for all relative operations, and canonical identities/lock are rechecked before and after side effects. No current-capacity file or journal is read through an arbitrary caller leaf. Filesystems other than pinned XFS/device/mount reject in production; the private test seam is explicit synthetic evidence.

- [ ] **Step 5: Implement ReserveSequence.** Under mutex: validate ctx/healthy/active; Busy on already pending with no IO or invalidation; reject exhausted high-water. Prepare exact n+1 journal, exclusive temporary0600, complete writes (positive short writes loop, zero-progress fails), chmod0400, file fsync, close once, renameat to sequence.bin, private-dir fsync, recheck actual canonical state/lock/record; then retain pending=n and reservedAt=time.Now, return n. Any IO failure poisons, no usable number; context before return also returns zero/no usable number, even if new state persisted. No same-number rollback/retry, no wrap. Successful Reserve must never happen after a visibility-changing operation in public dir for that number.

- [ ] **Step 6: Implement Commit and Close.** Commit under mutex copies raw<=MaxBytes before validation; Validate raw v1 via existing Verify AND CheckFacts, identity/manifest/policy, exact pending, Containers<=32, Removals empty, ObservedAt>=reservedAt and age<=30s/future<=2s. Do not mutate or remarshal envelope. Exclusive capacity.next0600 → all bytes → chmod0440 → fsync → close once → context/freshness/dir/lock/current-owned-entry rechecks → mark visibility boundary before rename → renameat capacity.json → public-dir fsync → canonical/lock/new-file identity/content/context/freshness recheck → clear pending and return nil. Any Commit error poisons. Errors at/after rename call are Uncertain, including cancellation, even if rename returns failure; context identity is retained. Temp bytes may be readable to trusted Backend but original Reader only reads capacity.json. No rollback/retry claim.

Close is mutex-protected and idempotent, fences methods, tries only bounded safe next cleanup and unchanged owned capacity withdrawal+dir-sync, closes owned FDs once and releases lock last. Cleanup errors do not leak descriptors or erase original operation errors. Preserve first Close result; return fixed Uncertain for unconfirmed public visibility. nil/zero-value objects reject safely without IO. Failed Store may Close; it cannot Reserve/Commit again. No directory/journal/lock deletion, arbitrary recursive cleanup or goroutine performing detached IO.

- [ ] **Step 7: Expand behavioral tests with exact fault positions.** Implement these matrices through the same Store methods. Helpers/fixtures can be private; assertions are literal outcomes, bytes, order and ownership, not a mirror of Store algorithm.

| Matrix | Required identities and literal checks |
| --- | --- |
| lifecycle | 0→1→2 and burned gap after Close/restart; pending Busy leaves original Commit usable; failed/closed/nil/unconfigured behavior; no journal recreation; MaxInt64 never wraps |
| journal/config | all exact byte fields, hash/size/versions/binding; key/Core/Storage/Coverage/policy alias mutation; wrong guarantee; journal bytes/inode drift before Reserve/Commit/Close |
| envelope | wrong sequence/key/domain/v2/identity/manifest/CheckFacts/33containers/nonemptyremovals/null fields; raw input unchanged; Reader exact bytes; stale/future/reservedAt boundary cases |
| fault order | before/after each create/write/chmod/file-sync/close/rename/dir-sync/recheck for state and public; literal journal-durable precedes capacity-visible; positive short writes and zero progress; IO cause/path never exposed |
| cancellation | pre-IO, after durable state, before/after public rename/fsync and late return; errors.Is(ctxErr), error.Is(Uncertain) after boundary, pending is never reused; no background effect after method returns |
| crash/restart | frozen disk-image cuts at each meaningful boundary reopened as a fresh Store; only formal journal used, next discarded never promoted, publishedseq<=highwater, same-number envelope never retried |
| ownership | unknown entries, missing state, lock symlink/hardlink/replacement, mode/owner/group/device/mount drift, public wrong-binding or seq>highwater; no deletion/overwrite of unrecognized entry; each FD closed once |
| concurrency | `TestStoreConcurrentReserve` one success and Busy peers; `TestStoreConcurrentClose` fences queued calls while synchronous operation holds mutex; independent real open-file-description lock contention and subsequent reacquisition |
| temporary directory | private seam normal-user actual write/rename/flock/reader tests and stale cleanup, not just recorder; demonstrate old file never torn by new rename and Close withdrawal, no fixed production paths |
| production surface | public Open on current Darwin rejects before fixed IO; no public overrides/signing/key/command/native collector/PG/Start imports or construction; sentinel text exact and no arbitrary cause |

Exact-clock tests use private time seam only; production Open uses time.Now. Resource guard/source/ABA/realpowerloss assertions remain unsupported, not inferred from recorder or Darwin temporary files. Do not expand into broad repository tests or real PostgreSQL.

- [ ] **Step 8: GREEN, focused race and final exact-source suites.** gofmt seven paths. Freeze every stage to impl/<label>-sources/<repo-path>, with sha256.json repo-path→digest; preserve every raw attempt. For final commands use fresh labels below and ensure exact seven source hashes identical. The race command is justified by the new shared-state lifecycle and lock/Close concurrency; do not run race for unrelated packages.

```sh
python3 .scratch/runtime-m1-publication-b1-1020/run-offline.py impl green /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublication -json -p=2 -count=1
python3 .scratch/runtime-m1-publication-b1-1020/run-offline.py impl final-race /usr/local/go/bin/go test ./internal/workerruntime/adapter/capacitypublication -run 'TestStoreConcurrent|TestStoreClose' -race -json -p=2 -count=1
python3 .scratch/runtime-m1-publication-b1-1020/run-offline.py impl final-capacity /usr/local/go/bin/go test ./internal/platform/config ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitysnapshot ./internal/workerruntime/adapter/capacityobserver ./internal/workerruntime/adapter/capacitypublisher ./internal/workerruntime/adapter/capacitypublication -json -p=2 -count=1
python3 .scratch/runtime-m1-publication-b1-1020/run-offline.py impl final-localproduct /usr/local/go/bin/go test ./internal/localproduct -run 'TestRuntimeCapacity|TestLoadRuntimeCapacity|TestInitialCreatedLayer' -json -p=2 -count=1
```

Expected final exit0, raw run/pass identity equality and uniqueness, no fail/skip/build-fail, empty stderr. Race overlaps are reported separately from final suite totals, never double-counted. Existing probe tests use synthetic/recording backends; no Linux quota native test or integration tag runs on this Darwin host.

- [ ] **Step 9: Self-review and handoff.** Verify protected923 and Git-visible1729 original bytes unchanged; exact seven additions plus root plan; branch/HEAD/index unchanged; no Git mutation. Write impl/task-1-report.md with implementation, API/semantics, exact sources/hash, RED/GREEN/race/final commands and unique test counts, raw receipts/stage copies, original failed attempts, concerns and native/production limitations. Write impl/task-1-evidence-sha256.json mapping every other impl evidence relative path→SHA256 (not itself). Return DONE/DONE_WITH_CONCERNS/BLOCKED/NEEDS_CONTEXT, report path and STOPWRITING/release product+Go window. Root owns later Linux compiles and reviews.

## Controller closeout

- [ ] Freeze seven-file full uncommitted diff against protected923, separately bind root plan (current inputs931). Preserve all old sources/seals and raw implementation evidence.
- [ ] Independent task Spec/Quality review from task brief, exact global constraints, approved spec, implementation report and full frozen diff. Resolve findings with one tested fix wave and re-review; no test reruns merely to confirm prior identical results.
- [ ] Recompute raw JSONL/log/source evidence, distinct final7package identity totals and separate focused race; run Linux amd64/arm64 publication test-binary compile-only, never execute. Capture gofmt/diff and original production-unavailable preservation.
- [ ] Most-capable independent whole-B1 final review. Resolve C/I and cannot-verify items; keep out-of-scope native/PG/UI obligations open.
- [ ] Final report, fresh read-only verifier and all-artifact seal. No edits to approved spec or earlier seals; no OpenSpec completion changes and no environment startup.
