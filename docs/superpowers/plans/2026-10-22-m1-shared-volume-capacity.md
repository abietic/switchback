# M1 Shared Volume Capacity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver the approved shared-volume guarantee, complete deployment qualification, and both production assembly paths using the existing PG capacity owner.

**Architecture:** Keep qualification in capacitysnapshot beside its strict codec and safe readers. A private, non-forgeable handle binds the fixed files and their semantics; publisher and Backend perform distinct current role checks before accessing keys or initializing PG. Qualification never supplies capacity, issues permits, or changes the Start protocol.

**Tech Stack:** Existing Go 1.26.4, x/sys/unix, strict JSON, existing capacityprobe observations, PostgreSQL adapter unit tests. No new dependency.

## Global Constraints

- Accepted requirements: `docs/superpowers/specs/2026-10-22-m1-existing-volume-capacity-design.md`, SHA256 `16038a09189bca7000275681279448fb02fa2f45365de5594becb4ccf629b80d`; authorization `call_7fe50964ed564e10bc97e9bd341a274a`.
- Reuse the linked integration worktree, HEAD `fbebfe754045bdbf1af40871a5018eed33743011`; preserve all unrelated dirty bytes and the empty index. No commits, staging, resets, cleanup, or worktree creation; scope diffs use `.scratch/runtime-m1-shared-volume-implementation-1022/before` rather than HEAD.
- Complete source and offline validation continuously; no environment commands, Engine/PG calls, network/provider calls, VM/unit operations, partition changes, data migration, keys or journal initialization.
- Read the accepted October 22 spec first and the reused October 21 verifier architecture. October 22 replaces only the old independent host physical guarantee for the new exact ref.
- New guarantee: `m1_shared_volume_capacity_v1`, revision 1, canonical 482 bytes, digest `sha256:31b0caa11d3ff6cf95631815a0879771ffce565c735f6fd5f6ecab58638dcb01`.
- Old guarantee: `m1_accounted_capacity_v1`, 367-byte definition and digest `sha256:badb64addbd9fbeff2f74978887f4d3c592d587f0cd47d72efeb40dd48b5f5cd` stay exact. Legacy nil stays legacy. Unknown refs reject.
- No `allow`, environment toggle, fixture loader, public injected verifier, alternate capacity owner, fake sample or sequence, new PG schema, raw v1/v2/domain/IPC changes, or resource numerical change.
- Backend retains only CHOWN/DAC_OVERRIDE/FOWNER; publisher retains the already accepted dedicated SYS_ADMIN/SYS_PTRACE boundary. No capabilities are actually granted in this slice.
- `.scratch/runtime-m1-namespace-capability-candidate-1021`, its consumed window/execution, their seals and preserved raw receipts remain immutable. The implementation phase records any intentionally changed live source separately, never edits historical seals.
- Test commands use `GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 go test -p 2`; no listeners or integration database starts. PostgreSQL tests requiring a DB may be compiled and explicitly skipped, never described as executed.

## File Structure

Task 1 changes only config guarantee definitions and their tests. Task 2 owns new `capacitysnapshot/deployment_qualification*.go` files and tests (reader/semantic binding/host/native/candidate responsibilities), new current role reader files under `capacityprobe` if needed, and the publisher/localproduct qualification assembly and tests. Task 3 adds new-ref compatibility coverage to existing mapper/created-layer/PostgreSQL tests and writes the final scope/verification report. Existing legacy structural readers and raw observation formats remain compatible.

### Task 1: Exact guarantee catalog

**Files:** Modify `internal/platform/config/runtime_capacity_guarantee.go`; test `internal/platform/config/runtime_capacity_guarantee_test.go`.

**Interfaces:**
```go
func M1SharedVolumeCapacityGuarantee() RuntimeCapacityGuarantee
func RuntimeCapacityGuaranteeDefinitionFor(ref RuntimeCapacityGuarantee) ([]byte, error)
```
The existing no-argument definition getter remains the old definition. Validate accepts the two exact immutable values only; no registry is exported.

- [x] Add a behavioral regression that passes the new exact ref to the existing Validate method and currently fails; preserve the old golden test. Then add new canonical/digest, mutation isolation, wrong revision/digest and legacy compatibility tests.
```go
want := RuntimeCapacityGuarantee{ID: "m1_shared_volume_capacity_v1", Revision: 1, Digest: "sha256:31b0caa11d3ff6cf95631815a0879771ffce565c735f6fd5f6ecab58638dcb01"}
if err := want.Validate(); err != nil { t.Fatal(err) }
```
- [x] Run the focused regression and retain the actual RED output in the task report.
```sh
GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 go test -p 2 ./internal/platform/config -run 'TestRuntimeCapacityGuarantee' -count=1
```
- [x] Add this exact static definition and digest it using the existing sha256/hex pattern. Dispatch on exact struct equality, returning fresh byte slices; unsupported input returns the existing unsupported error wording. Keep the old getter and constant bytes intact.
```json
{"id":"m1_shared_volume_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":false,"host_storage_boundary":"apfs_volume_quota_and_reserve_v1","host_future_container_metadata_upper_bound":false,"authority":"original_pg_capacity_owner"}
```
- [x] Run the config package, gofmt, inspect only the two-file delta, and write task-1-report.md with files, RED/GREEN commands/output, both lengths/digests, and concerns. No Git mutation.

### Task 2: Complete qualification and production assembly

**Files:** Create focused `internal/workerruntime/adapter/capacitysnapshot/deployment_qualification*.go` and matching tests; create current role reader/validation files under `internal/platform/capacityprobe` only when existing APIs cannot safely serve Backend; modify `internal/workerruntime/adapter/capacitypublisher/publish.go` and focused new assembly tests; modify `internal/localproduct/runtime_capacity_source.go` and focused assembly tests. Update the old tests that intentionally asserted fixed unavailable to assert missing qualification rejects before side effects. Do not weaken existing publication or PG behavior.

**Interfaces:** Consume the exact new catalog from Task 1. Produce these concrete methods; fields stay private and a zero handle is invalid:
```go
func OpenDeploymentQualification(ctx context.Context, manifestPath string, trust Trust) (*DeploymentQualification, error)
func (q *DeploymentQualification) Metadata() AdmissionManifestMetadata
func (q *DeploymentQualification) PublisherBinding() DeploymentPublisherBinding
func (q *DeploymentQualification) Recheck(ctx context.Context) error
type DeploymentCoreBinding struct {
    ContainerID string `json:"container_id"`
    ImageDigest string `json:"image_digest"`
    CoreID string `json:"core_id"`
}
type DeploymentPublisherBinding struct {
    Core []DeploymentCoreBinding
    WorkerImageDigest string
}
```
Metadata/binding are deep copies and cannot manufacture a qualified handle. Role-specific expected values are derived from the validated inventory, Trust and bindings, never caller supplied flags. Private test seams cannot be passed through a production factory.

- [x] Establish finite strict schemas and regression fixtures for all accepted requirements below. Use package-private types/validators; full source contains the fixed schema and complete allowed checkset. Fixtures live only in `_test.go` and cannot become a production route. Preserve existing strictJSON rules (case/duplicate/null/unknown rejection). Add tests showing a previously structurally accepted placeholder and a weaker host receipt under the old guarantee are refused.
```go
if _, err := validateDeploymentQualification(fixtureTrust, placeholderFiles); err == nil { t.Fatal("placeholder qualified") }
if _, err := validateDeploymentQualification(oldGuaranteeTrust, volumeReceiptFiles); err == nil { t.Fatal("weaker receipt qualified old guarantee") }
```
`validateDeploymentQualification` is package-private pure assembly; production can call it only after the fixed safe file reader. Private helper names may follow package conventions.

- [x] Implement the whole pure semantic boundary, with these deterministic rules:
  * Candidate schema `switchback.capacity.deployment_candidate.v1`; canonical fixed-order JSON includes exact guarantee/resource/network, Trust prefields (key/public key/Identity/AnchorPath/Core/Storage/Coverage, excluding ManifestDigest), source/inventory digests, sorted exact core CID/image/CoreID and Worker image digest. Normalize each identity list and reject duplicates/empty ambiguity. Both receipts must match the independently recomputed fingerprint and same qualification ID/window/boot identities.
  * Source manifest schema is fixed and strict: sorted relative regular-file entries with path/type/mode/bytes/digest and per-role build receipt indexes; no absolute/parent/symlink, duplicate, secret/state/qualification output roots. Build facts bind toolchain/input root/output binary/image to inventory; loaded artifact identity is a separate native observation.
  * Deployment inputs are typed normalized actual rendered inputs; only Trust.ManifestDigest, publisher-bindings.ManifestDigest and observer MANIFEST_DIGEST can use `final_manifest_digest` references. Reject arbitrary exclusions, unresolved `${...}`, unknown fields and any other derived placeholder. Compare all three final installed references to the final Trust.ManifestDigest. Keep their final raw readback digest as installation audit, outside the candidate hash. Embed bounded input/raw facts in the fixed leaves; never follow an evidence-supplied path/URL.
  * New host schema `switchback.capacity.host_volume_receipt.v1`, kind `apfs_volume_quota_and_reserve_v1`, lifecycle `qualification_then_frozen_deployment_v1`. Require bounded raw `diskutil info`/`apfs list` before/after volume UUID/container UUID/mount/physical-store association, stable and exact candidate identity, quota=reserve=120259084288. Reject 0/missing/drift. Parse those actual raw observations rather than trusting summary booleans; preserve FreeSpace/CapacityInUse and their units without treating reserve as exhaustion or synthesizing availability.
  * Host mapping covers system/data image, Lima state/log, identified VZ helper, cache and controlled temp/conversion/qualification writes. Each finite writer/path is mapped to the identified volume or the maintenance action is explicitly forbidden by the fixed A policy. Normal runtime paths cannot simply be marked forbidden. No demand for a shared-container future metadata bound, external disk or partition, and no host fill test.
  * Host/native receipts bind exact producer artifact/build/input digests, finite source IDs and actual raw outputs/exit/errno, exact required check IDs with no duplicate/unknown/missing/skipped/partial/merged window. Do not accept `passed=true`, `source=native`, an exit code alone or an operator approval as a proof.
  * Complete native semantics cover loaded Engine/kernel/FS/role identity; stable caller-thread/PID1 namespace and actual dual caps/NNP/seccomp; core aggregate hard limits and complete role inventory; finite small byte quota denial plus sufficient-limit success with actual state/project/inheritance; Worker setter rejection for supported ABI with GET/read/write success and unchanged project/flags; real Backend three-cap UID10002 empty/nonempty/sticky handoff plus missing-cap negative; actual mount/writer inventory excluding Backend access to private key/B1 private directories despite DAC_OVERRIDE. Known unsupported ABI records actual cause, never a synthetic pass. Reuse existing raw parsers/observation validators, don't introduce alternate quota arithmetic or capacity facts. Keep receipt checks bounded and typed so every accepted observation has an actual semantic predicate.
  * Only the new guarantee can take the shared-volume receipt success path. Old guarantee remains recognizable metadata with its stronger unsupported physical proof rejected. No first snapshot or signed sample is used to decide qualification.

- [x] Implement safe production reading and frozen Recheck. The manifest basename is fixed and evidence leaves remain source-manifest.json, deployment-inventory.json, host-boundary-receipt.json, native-enforcement-receipt.json; include fixed Trust/binding config where final-reference checks require them. Canonical absolute ancestors are root owned, no group/world write, and NOFOLLOW at every hop. Leaves are root owned, read-only regular files, nlink=1, positive <=1MiB. Retain dev/inode/type/uid/gid/mode/nlink/size/mtime/ctime and bytes, compare before/after reads and path reopen, and compare directory identities; close all descriptors. Cancellation rejects before further I/O. Recheck is read-only, zero/nil handle rejects and same-byte rewrite/replacement rejects.
```go
if err := q.Recheck(cancelled); err == nil { t.Fatal("cancelled qualification accepted") }
copy := q.PublisherBinding(); copy.Core[0].CoreID = "mutated"
if q.PublisherBinding().Core[0].CoreID == "mutated" { t.Fatal("binding aliases handle") }
```

- [x] Implement actual current role checks and wire both production factories. Linux arm64/amd64 only; other platforms fail explicitly. Read finite `/proc/self/exe`, boot/status/cgroup and namespace facts safely; bound binary hashing and before/after identity. No injected role facts in exported production APIs. Backend checks its inventory binary, UID0, exactly cap sets 0xb (CHOWN/DAC_OVERRIDE/FOWNER), ambient/inheritable0, NNP1, seccomp2 and guest boot; no PID1 namespace or Engine/collector access, hostname/self-CID guessing or host PID numeric comparison. Publisher additionally uses existing ObserveDeployment for stable full PID1/thread namespaces, exact dual cap sets 0x280000, guest boot, its core aggregate/cgroup and private/config path identities. Binding returned by qualification must exactly equal its own already-read config (sorted, no duplicates). Publisher's current full census remains the original CollectPool per publication cycle, not a qualification fixture.
```text
publisher: platform/ctx -> fixed config -> qualification -> current publisher role -> key -> Store -> original loop
Backend: paths/Trust -> qualification -> current Backend role -> Reader/observer -> CapacityStore.InitializeTrust(stale)
preflight: qualification.Recheck/current Backend role -> original ReconcileCapacity
```
Retain the original publisher cycle order and actual reserved sequence. First rejection happens before key/Store/Reserve. Backend does not Read capacity.json or ObservePool during construction; keep the validated handle in initialCapacitySource. Each preflight rechecks before advisory reconciliation. No test-only factory option or alternate InitializeTrust.

- [x] Run offline package tests and package-private syscall/reader fault tests: full positive pure qualification, every proof group negative, host wrong UUID/quota/path, strong/weak swap, candidate ordering and hash-cycle checks, replacement/ctime/hardlink/cancel/oversize, deep-copy/zero-handle, role caps/boot/binary mismatches, publisher side-effect ordering, Backend stale bootstrap and preflight ordering. Use Linux cross-compilation (arm64 and amd64) without execution. Record real RED/GREEN output and limitations in task-2-report.md. Do not describe native fixture replay as actual deployment qualification.

### Task 3: Original owner compatibility and closeout

**Files:** Add focused tests in `internal/workerruntime/adapter/capacitypublisher`, `capacitysnapshot`, `postgres`, and `internal/localproduct` for new-ref propagation; avoid production changes to the PG owner. Create `docs/research/2026-10-22-m1-shared-volume-implementation.md`; all execution evidence goes under the new phase.

**Interfaces:** Consume Task 1 exact refs and Task 2 handle/assembly. Verify original existing public APIs, persisted Trust, mapper, created-layer verifier and coordinator. No new authorization interface.

- [x] Add meaningful new-guarantee regressions: signed samples bind the new ref via manifest; raw v1/v2/old refs do not silently upgrade; mapper uses actual sequence, keeps 80% accounting; inode hard0 maps only to no proven inode bound for known policies; unknown/legacy rules unchanged; wrong exact CID/manifest/resource/source pin refuse before Start. Persisted Trust equality stays exact and original reserve/consume/unknown-effect tests remain intact.
```go
g := config.M1SharedVolumeCapacityGuarantee()
trust.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
if err := capacitysnapshot.ValidateTrust(trust); err != nil { t.Fatal(err) }
```
Use existing fixtures to invoke real mapper/verifier/owner pure helpers; a test that merely checks the same constant twice is insufficient. DB integration cases are compiled but not executed without a separately approved DB window; call this out explicitly.

- [x] Run the targeted complete packages once, then race where new mutable guards/qualification concurrency warrant it, and compile Linux role packages for both architectures. Record each actual command, exit and log path; no aggregate invented test count.
```sh
env -u SWITCHBACK_TEST_DATABASE_URL GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 go test -p 2 ./internal/platform/config ./internal/platform/capacityprobe ./internal/workerruntime/adapter/capacitysnapshot ./internal/workerruntime/adapter/capacitypublisher ./internal/workerruntime/adapter/capacitypublication ./internal/workerruntime/adapter/capacityobserver ./internal/workerruntime/adapter/postgres -count=1
env -u SWITCHBACK_TEST_DATABASE_URL GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 go test -p 2 ./internal/localproduct -run "Test(RuntimeCapacity|InitialCreatedLayer|SharedVolume|DeploymentQualification)" -count=1
```
- [x] Write the implementation report and scope artifact: exact source changes, behavior validated, skipped DB/native checks, new volume qualification still pending, old C06 independent physical proof not applicable only to the new guarantee. Preserve OpenSpec 6.5/8.4/9.3 unchecked. State next real milestone is bounded actual volume/native qualification, then signed publication/original PG/Create-quota-Start, then task/report/acceptance/observability.
- [x] Controller verifies all 1810 preexisting Git-visible files: only approved live source/test/docs allowlist changes; HEAD/index/unrelated bytes and historical raw evidence/seals intact. Review the full slice from before snapshots, resolve findings, record final ledger and verification. End with a concise source-delivery report, never production-ready or completed-experience claims.
