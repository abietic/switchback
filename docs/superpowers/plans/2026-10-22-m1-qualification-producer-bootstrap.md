# M1 Qualification Producer and Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the complete source/offline toolchain for real qualification, safe installation and same-process startup, without performing environment operations.

**Architecture:** A strict private native-v2 codec and a shared bounded wait sit beside the existing qualification validator. Final program entrypoints wait before business side effects, finite helpers perform real test I/O, host/native producers collect actual facts, and a fixed installer commits the manifest last. Existing PG and Start remain the only runtime authorization chain.

**Tech Stack:** Go 1.26.4, existing x/sys/unix and Moby client, fixed JSON/plist codecs, Python stdlib for the offline candidate/build driver, existing Docker/systemd source templates.

## Global Constraints

- Approved spec: docs/superpowers/specs/2026-10-22-m1-qualification-producer-bootstrap-design.md; user accepted in call_89ea338ebf0d4ee290e1d31bc881252f. Authorization record: .scratch/runtime-m1-qualification-delivery-1022/authorization.json.
- Work only in /path/to/switchback. HEAD fbebfe754045bdbf1af40871a5018eed33743011 and index stay unchanged. No staging, commits, resets, cleanup or new worktrees. Preserve other dirty files and historical evidence.
- Source, synthetic offline tests and builds only. No VM/guest/Engine/PostgreSQL/provider/listener/key/journal/unit operations. No real ioctl enforcement test, private key generation, service install or Docker image build. Production adapters may be written but cannot be executed this round. Tests may use only isolated temporary files and private test adapters.
- Reuse existing storage. No external disk, partition changes, migration, fill-disk test or renewed independent physical-upper-bound requirement. Keep exact m1_shared_volume_capacity_v1 separate from old guarantee and evidence.
- Wait at most 300 seconds, fixed 100ms poll; only final manifest ENOENT under an already-safe fixed parent is pending. Existing invalid files, missing parents, symlinks, identity drift, cancellation and timeout fail. No exposed timeout/skip/allow parameter. Manifest last, fsync leaves then directory; post-commit failure is unknown, no resume/retry.
- Waiting has zero PG, business listener, Engine, provider, private-key, B1 Store/journal, sequence and capacity-collection calls. Backend wait precedes localproduct.New. Publisher wait precedes readPublisherConfig/key/Store/collect as needed. Observer serve-installed avoids pre-exec EnvironmentFile digest expansion; actual env remains verified after commit.
- Private new native schema is switchback.capacity.native_qualification_receipt.v2. The seven check IDs remain exact. Old v1 bytes/legacy pure parsing stay intact; new production qualification cannot fall back to v1. Capacity raw v1/v2, signing domain, IPC, PG and Start formats and resource values remain unchanged.
- Actual native/compat ELF caller identity, PID/TID/start/boot, raw status/cgroup, binary digest, ABI, target FD identity and actual request/buffer bytes must bind to frozen Worker parent/CID/image/profile and explicit helper artifacts. Real ELF32 execve ENOEXEC is unsupported, never setter-pass. Missing/wrong helper and other exec failures reject.
- Native producer only root CAP_SYS_ADMIN|CAP_SYS_PTRACE; Backend helper only its existing CHOWN/DAC_OVERRIDE/FOWNER; Worker UID10002 cap_drop ALL/NNP/seccomp/fixed resource/network profile. Negative helpers cannot gain/restore caps. Since dropping the bounding set needs SETPCAP, use Engine-created same-Backend-image helper containers with the reduced cap set; do not grant SETPCAP or try to restore capabilities in children.
- Native window at most 240 seconds. Actual instance facts are produced only by a later separately authorized operation. Offline kit must not fabricate actual CIDs, boot, image IDs, keys or successful receipts.
- Never edit protected deploy/experience/switchback-capacity-publisher.service or internal/workerruntime/adapter/capacitypublisher/publish_test.go, old source snapshots/raw packets/seals/consumed windows. New templates and tests use new paths.
- Every task records exact commands/results and changed paths in its report. Use focused failure regressions for material behavior, full relevant packages once after convergence, no unnecessary repeated suites. Independent Spec/Quality task review then final whole-slice review; no check-in between tasks.
- Common test prefix: env -u SWITCHBACK_TEST_DATABASE_URL TMPDIR=/private/tmp GOCACHE=/private/tmp/switchback-qualification-1022-go-cache GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4. Use go test -p 2. Linux cross-compilation is compile-only. Do not execute package tests that bind real listeners or external fixtures.

## File/Module Map

- capacitysnapshot/deployment_qualification_v2*.go: native v2 strict evidence and legacy isolation.
- capacitysnapshot/deployment_qualification_wait*.go: finite safe manifest wait using existing directory/leaf syscalls.
- internal/platform/qualificationio/: small raw-I/O/helper wire types and Linux implementations, buildable for native64 and compat32 without business runtimes.
- cmd/worker-shim and cmd/switchback-backend: explicit finite qualification branches; normal startup retains existing semantics after the new barrier.
- localproduct/runtime_capacity_bootstrap.go, capacitypublisher/bootstrap.go, observer/bootstrap.go: role-specific entry integration; no duplicate qualification codec.
- capacitysnapshot/qualification_candidate*.go and qualification_producer_{host,native}*.go: candidate preparation, actual OS adapters and shared receipt assembly/validation.
- capacitysnapshot/qualification_install*.go: fixed safe install and commit semantics.
- cmd/switchback-qualification-{host,native,install}/: fixed command bindings, no test observer injection in production.
- deploy/experience/qualification/: new undeployed templates and offline build/candidate driver; test/acceptance/experienceprepare/: bounded offline driver tests.

### Task 1: Private native-v2 evidence and safe bounded wait

**Files:** Modify capacitysnapshot/deployment_qualification.go, deployment_qualification_native.go, and current test-only fixture assembly in deployment_qualification_test.go where necessary; create deployment_qualification_v2.go, deployment_qualification_v2_test.go, deployment_qualification_wait.go, deployment_qualification_wait_test.go. Create minimal raw executor wire types in internal/platform/qualificationio/evidence.go if needed by later helpers. Do not modify the protected publisher test. Evidence under .scratch/runtime-m1-qualification-delivery-1022/task-1/.

**Interfaces:** Produce this exact public barrier, backed by private fixed-policy clock/files seams only:

```go
func AwaitDeploymentQualification(ctx context.Context, manifestPath, trustPath string) (*DeploymentQualification, error)
```

Read Trust only after manifest appears, enforce the actual frozen trust sibling, then call the real OpenDeploymentQualification exactly once; return no trusted handle on any error. Caller checks its own current role. Waiting parent identity is held/reopened and compared so a replacement during ENOENT polling rejects. Existing OpenDeploymentQualification is production-v2-only. Keep legacy v1 semantic parsing in a private, explicitly legacy path for historical bytes and pure regressions; no public bypass argument/constructor.

Native v2 evidence distinguishes parent Worker from actual executor. Required executor facts: host PID/TID/TGID and start ticks, namespace PID/TID mapping, guest boot, pre/post raw stat/status/cgroup, actual exe digest and ELF class/machine, raw container mountinfo and target FD statx/device/inode/project proof. Helpers report namespace-local facts; the trusted native producer must add corresponding host-/proc observations while helper is alive. Match NSpid/NStgid mappings instead of assuming namespace PID equals host PID. Unsupported compat exec has no fabricated child identity and must bind the actual attempted ELF and parent exec errno.

Native64 GETFLAGS actual buffer is 8 bytes, compat32 is 4, fsxattr is 28. Preserve actual request number and raw bytes, separately decode numeric value. Native and compat helpers are separate actual processes/artifacts named worker-native-helper and worker-compat-helper; neither uses the parent executable as a substitute. Both helpers and tested parent share one exact qualification Worker CID/image/profile and UID10002/cap0. This finite tested parent is not the normal root Worker shim startup. Keep mandatory old build/role coverage. Cross-namespace mount ID may differ: bind FD to the expected host object via actual mountinfo/device/inode/FSUUID and fixed test bind target, not numeric mount-ID equality alone. Existing quota/handoff/core/reachability checks stay strict.

- [x] Write a complete positive v2 frozen fixture and a legacy v1 replay test, plus failures for parent/executor/ELF/ABI/FD/request/buffer/UID/caps/NNP/seccomp/boot/cgroup swaps, ENOEXEC versus general failure, duplicate/missing checks and attempted production v1 fallback. The main positive must reach the real full qualification validator.
- [x] Run focused tests before implementation and preserve actual expected RED. Introduce v2 strict parsing and source artifact bindings; update current synthetic fixture generation rather than editing historical snapshot inputs. Existing UID999/10001 compatibility remains covered.
- [x] Implement the barrier using the existing no-follow directory/leaf reader. Separate fixed-policy wait loop from OS operations privately, with an injected test clock only. Test ENOENT then valid commit, missing parent, malformed manifest, missing sibling, unsafe permissions/link, parent replacement, cancellation, 300-second deadline, and exactly one real validation attempt after commit.
- [x] Run the full capacitysnapshot package and qualificationio package (if created). Run focused current-role/assembly contracts only if fixture changes affect them. Compile snapshot tests for Linux arm64/amd64. Write task-1-report.md, including exact shared wire type signatures and JSON field names for Task 2/3, and self-review.

### Task 2: Final entrypoints and finite real Worker/Backend helpers

**Files:** Modify cmd/switchback-backend/main.go, cmd/worker-shim/main.go, cmd/switchback-created-layer-observer/main.go, capacitypublisher/publish.go only as needed for the barrier; create localproduct/runtime_capacity_bootstrap.go and tests, capacitypublisher/bootstrap.go and bootstrap_test.go, observer bootstrap files/tests, internal/platform/qualificationio/{config,worker,backend,process,ioctl}_*.go and tests, cmd/switchback-qualification-compat/main.go and tests. Create images/worker/qualification build integration and deploy/experience/qualification source templates. Evidence under task-2/.

**Interfaces:** Consume AwaitDeploymentQualification and Task 1's exact wire types. Produce fixed production helper functions, with private test seams:

```go
func RunWorker(ctx context.Context, output io.Writer) error
func RunBackend(ctx context.Context, output io.Writer) error
func RunCompat(ctx context.Context, output io.Writer) error
```

These functions live in qualificationio and read only fixed control files; arguments do not carry paths, commands, endpoints or resource numbers. `worker-shim qualify-worker` and `switchback-backend qualify-backend` branch before business config/secrets/runtime/PG. Compat command has no dynamic command arguments. Reject extra args. Existing build-info and normal worker behavior remain intact.

Worker control uses the fixed `/qualification/control.json` read-only mount and `/qualification-data` test bind. It contains the exact candidate/window/nonce, parent binding, fixed case set, expected helper artifact and target object. Securely open root-owned non-writable control with no symlink/hardlink and bounded bytes; check namespace UID10002/caps0/NNP/seccomp and actual executable/profile. Test root is a fresh owned nonce directory in the fixed data bind; use dirfds/no-follow and actual fstat/statx. Ioctl GET/SET acts on the held test directory FD; ordinary read/write acts on its held regular-child FD. Bind both objects and retain their exact identity: a directory cannot serve as the regular write target. No shell or arbitrary-path channel.

Resolve frozen Trust Coverage.Role==product through its unique Storage domain. The host test root is path.Join(productDomain.Path, ".qualification", window.ID), where window.ID is a safe single label. Target is host_test_root/target and child is host_test_root/target/io, mapped to /qualification-data/target and /qualification-data/target/io. Prove the actual guest mountpoint/root mapping rather than treating an absolute guest path as the filesystem-relative mountinfo root.

Helpers stay alive at bounded pre/post observation points using a finite fixed control protocol over attached stdin/stdout; native producer collects host-/proc facts at those points. Frames are bounded, strict and exact phase/case/window/token matched; unsupported/timeout/channel loss fails. The protocol is observation coordination only and never permits runtime/Agent work. Build a small ioctl-helper command for native64 and compat32; the compat command path above may be generalized to cmd/switchback-qualification-ioctl so the same syscall source produces two explicitly named artifacts. Compat ELF is built Go linux/arm (GOARM7) for arm64 and linux/386 for amd64; use real exec, retain ENOEXEC, no emulation/fake success.

Backend helper has fixed cases empty/nonempty/private/sticky and missing-CHOWN/DAC_OVERRIDE/FOWNER. Actual same-image helper containers have the exact positive or reduced cap mask from Engine creation, not ad hoc cap elevation. It performs only the named finite operation on an exact fresh test object, records errno/status/binary/object/entries and exposes bounded observation points. Cleanup acts only on established owned objects; unknown ownership fails.

For Backend ordinary startup add an explicit pre-New barrier function; preserve no-capacity configuration behavior for existing non-execution local tests. Configured-but-partial capacity paths fail. For publisher, wait before reading missing final config and retain its existing real qualification/role/config rechecks afterward. New observer `serve-installed` reads the fixed complete qualification and actual `/etc/switchback/created-layer-observer/deployment.env` after commit, checks observer role/current identity, then reads IPC key and serves. Existing `serve <digest>` compatibility remains, new candidate uses serve-installed.

- [x] Add failure regressions proving wait/invalid/cancel/timeout paths invoke no PG/New/listener/Engine/key/Store/sequence/collect/provider dependencies and successful commit continues only once. Use counters around real entrypoint order; no real listener or PG.
- [x] Implement the entrypoint integration and finite helpers with actual Linux syscalls, strict frame/control codecs and non-Linux rejection. Build and inspect actual native/compat ELF class/machine, never execute them this round.
- [x] Add new undeployed `Restart=no` publisher/observer templates without mandatory pre-exec final EnvironmentFile expansion, and a new Compose qualification overlay with fixed capacities/cap sets/ro config mounts. Never edit the protected old publisher unit; the new template is explicitly not installed.
- [x] Run relevant command/helper full unit packages and focused localproduct/publisher bootstrap contracts, preserving original protected tests. Compile affected tests for Linux arm64/amd64 and helpers for compat32. Write task-2-report.md with actual command/wire protocol/candidate template paths and test evidence.

### Task 3: Actual host/native producers and complete receipt assembly

**Files:** Create capacitysnapshot/qualification_candidate*.go, qualification_producer_host*.go, qualification_producer_native*.go, qualification_engine*.go and focused tests; create cmd/switchback-qualification-host/main.go and cmd/switchback-qualification-native/main.go with tests. Helpers/codecs from Tasks 1/2 may receive minimal integration fixes with covering tests. Evidence under task-3/.

**Interfaces:** Produce fixed commands and these source entrypoints (no production injection args):

```go
func CollectHostQualification(ctx context.Context) error
func CollectNativeQualification(ctx context.Context) error
```

Candidate and facts are private typed DTOs in capacitysnapshot; no exported success bundle constructor. Linux candidate root is `/var/lib/switchback/qualification-candidate`; host root is `filepath.Join(os.UserHomeDir(), ".switchback", "qualification-candidate")`. Fixed candidate leaves include frozen source/inventory/trust-prefix/bindings/window/artifact materials; host candidate uses owner=current UID and guest root=UID0, safe fixed leaves with strict bounds. These roots are deployment-owned, never Task/API parameters. Producer outputs are new exclusive files in the same candidate root and do not initialize final Trust/key/journal.

Host uses exact `/usr/sbin/diskutil info -plist <frozen mount>` and `diskutil apfs list -plist` under a bounded context/output cap, plus actual boot/sysctl/stat/writer executable/config observations. It records both before/after raw plist, exact volume/container/store/quota/reserve, actual writer mapping/stat/device/inode/allocated-block units. Writer artifact provenance is checked against frozen source/build material; do not hash mutable 112-GiB disks or invent content digests for them. Disk path identity and executable/config artifact identity are separate facts. Capture common window without joining old outputs.

Native uses actual fixed Unix Engine adapter and guest /proc, cgroup, mountinfo, statx, filesystem/XFS ioctls and quota reads. Reuse current capacityprobe parsers/observations. Collect the seven exact checks from real OS operations: loaded identity, eight namespaces and declared roles, core CPU/memory/PIDs/swap, finite byte-quota denied/success on same object, actual Worker setters via Task 2 protocol/host identity, Backend handoff via exact same-image cap cases, mounts/writers/private directory reachability. Inspector data is raw evidence and compared to actual process/cgroup facts. Every syscall/Engine/body/exec output is bounded and cancellation-aware.

Use a fixed candidate project ID proved unused: nonzero quota record or existing matching inodes/ownership uncertainty rejects, do not choose another ID at execution time or reset existing project. Test writes within 16MiB/request and prechecked free bytes. Core role containers and helper CIDs are exact; no provider, ACP, public ports or unrelated mounts. Engine helpers must use NNP, seccomp, fixed resources and cap limits, and exact label/nonce/CID ownership. Create/start unknown outcome stops the entire window with evidence, no blind retry/cleanup. Clean only proven created objects; failed/unknown cleanup prevents install. No raw test bytes count as capacity.

- [x] Add parser/adapter/failure tests with captured synthetic raw inputs and isolated finite fake Engine/syscall seams. Demonstrate all seven real adapters are wired in production main; unsupported/missing facts cannot return a successful receipt.
- [x] Implement actual host/native command adapters and same-window assembly. Bind producer binary/build/input-root/source/inventory and actual identity. Host/native observations are sealed with a common real window only after both complete; fixed coordinator files cannot rewrite an earlier observation or import historical windows.
- [x] Run the same production semantic validator on fully assembled synthetic output; prove source/boot/role/image/window/errno/raw buffer mixups and cleanup-unknown reject. Preserve v1 legacy tests and all global constraints. Tests do not execute actual adapters or listeners.
- [x] Run full relevant pure producer/codec/command unit packages, compile host command darwin/arm64 and native command linux/arm64+amd64. Write task-3-report.md, actual fixed input/output leaf schema and interface details for installer/driver. Any substantive production adapter still absent is BLOCKED, never DONE.

### Task 4: Fixed install, single-window driver and complete offline kit

**Files:** Create capacitysnapshot/qualification_install*.go and tests, cmd/switchback-qualification-install/main.go and tests, deploy/experience/qualification/{build_candidate.py,window.py,README.md}, test/acceptance/experienceprepare/test_qualification_candidate.py and test_qualification_window.py. Update new qualification templates and current research progress report only. Evidence under task-4/.

**Interfaces:** Consume Tasks 1–3 fixed commands and codecs; produce:

```go
func InstallQualification(ctx context.Context) error
```

Installer loads only the fixed Linux candidate root, verifies full real production semantics and matching common window, proves cleanup complete, then installs fixed leaves at `/mnt/lima-sbquota/core/capacity-publisher-config` and the actual observer env path. Source directory and final directory owner/mode/link/path identities must remain stable. Reject existing final manifest/Trust/journal/pool identity, any extra leaf, concurrent installer or unrecognized file. No key/journal/PG initialization inside installer. Prepare leaves as exclusive temporary files, bounded write loops, chmod/fstat/fsync/readback, no-follow rename/no-replace semantics, observer env installed before manifest. Manifest final commit and directory fsync; post-rename failure produces explicit unknown and cannot be retried/resumed. Existing readers expect publisher config exactly seven leaves; store operational locks/receipts outside that directory, never add an eighth marker there.

First-start state bridge: after assembly exposes final Trust and before manifest commit, a separate fixed candidate-owned bootstrap exclusively creates the initial publication journal/writer.lock, with original deadline, empty-directory preflight and immutable ownership/readback receipt. Installer only verifies that exact new state. Existing/foreign/unknown state is never overwritten, reused or reset. Use one independent pure codec shared with existing publication private wrappers; unchanged96-byte format/errors/Store behavior. Detailed controller resolution is in task-4/brief.md. No real initialization this round.

PG prerequisite: prepare/migrate a newly candidate-owned exact PG CID in a bounded single-attempt preparation phase; prove ready and the exact unconfigured migrated capacity-pool baseline, then keep that same CID/PID through freeze/qualification/commit. Backend/publisher/observer are the three manifest-wait roles. Start their single240s qualification origin immediately before the first of those waiting roles starts, covering all observations/cleanup/assembly/state bootstrap/install. PG preparation is an explicit later-authorized effect, not qualification or a capacity-pool initialization. Earlier “first final role Start” shorthand refers to this waiting-role boundary.

Single-window driver is fixed and requires later actual operation authorization. For this round only its offline self-check/tests/build subcommand may run. Actual mode: preflight current VM already Running without starting it; prove exact candidate/tool/material identity; prepare safe fixed dirs/public identity through separately authorized steps; Create-but-not-Start core CIDs; freeze candidate; start waiting roles once; host/native collect under shared 240s deadline; cleanup/prove object ownership; install; preserve outcome. Failure or unknown stops, no auto retry/resume/independent readback or unrelated Stop. Do not rerun old windows. Exact execution list and authorization include actual PG/core/Engine/key/Store effects that will occur after the barrier; it must not be described as a diagnostic-only window.

Build driver uses pinned local Go, CGO_ENABLED=0, offline module settings, exact command/tool/build info/source input root before/after, Linux native and compat artifacts, host command, source templates and source-context image build inputs. It can write under the current .scratch phase only and must not call Docker/VM/PG/provider. No real image ID/CID/boot/Trust/receipt is filled with sample values. Output manifest marks those runtime facts unresolved and is not accepted as qualification. Runtime phase later fills them from actual observations; kit validity is not capacity readiness.

- [x] Write failure regressions covering short write, fsync/rename/no-replace errors, symlink/hardlink/permissions/parent drift, extra/missing leaf, wrong common window, existing Trust/manifest, concurrent installer, commit-unknown and attempted resume. Assert final manifest absent on every proven pre-commit failure.
- [x] Implement installer and fixed production binding, run synthetic full installation through the same reader/validator with a private safe filesystem seam; no real /etc or /mnt writes. Prove observer reads actual installed env and all three barriers resume once only after complete commit.
- [x] Implement/test offline driver: ordered stages, timeout at every blocking boundary, exact ownership bookkeeping, failed/unknown operation no retries/no unrelated cleanup, bounded output, preserved evidence. Use private fake subprocess operations in tests; never invoke actual mode.
- [x] Build complete offline kit and all commands native/compat, inspect ELF headers/build receipts and artifact hashes; source inputs stable before/after. Check templates and current owner admission regressions. Write a concrete operation checklist with unresolved actual runtime facts honestly marked, not authorized or executable automatically.
- [x] Write task-4-report.md and current progress report. Controller performs full-slice independent review, repairs findings through one fix task, verifies source baseline/protected metadata/HEAD/index, and records completion only when every required adapter/tool is present and all gates pass.

## Plan self-review

The four tasks are sequential dependencies; each has a distinct rejection gate. Interfaces shared across tasks are fixed public functions with private internal DTO details recorded in each report, avoiding copied codecs. No task repeats an environment authorization request; all execution stays offline. Original Start/PG contracts are regression requirements, not duplicated implementations. Existing SDD ledger contains historical tasks and must not be reset; append a phase-qualified record after each clean task review. Current phase progress.md is the detailed recovery map.
