# M1 Resident Engine Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the complete accepted resident Engine prerequisite, its finite executor, qualification handoff, rebuilt qualification kit and independent offline reviews.

**Architecture:** Separate immutable configuration and pure policy from guest I/O and host orchestration. Bind repository SourceRoot → qualification kit → static Engine bundle → operational deployment receipt without a hash cycle. Qualification independently checks the receipt and current native identities before its first Engine connection; the receipt grants no capacity or business authority.

**Tech Stack:** Python 3 standard library, systemd, Docker 29.9.0, containerd 2.4.1, existing Go 1.26.4 qualification commands and Linux native readbacks.

## Global Constraints

- Accepted design: `docs/superpowers/specs/2026-10-22-m1-resident-engine-deployment-design.md`, SHA256 `3f86c9f11024f12d63bb610110339f1ac532f5a88c0c650e042ef59eedb83a48`; latest human approval `call_cc9c890bc2e34547be6efe4679d5632e` authorizes complete source/offline implementation only.
- Work exclusively in `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`, empty index. No staging, commits, resets, cleanup, branch changes or new worktree.
- Do not enter guest, execute a Linux/native helper, install/start services or Engine/VM, access Engine/PG/provider, create deployment secrets/journal/PG, build/pull Docker images, or change actual network/storage/partitions. Native/systemd compatibility remains unproven by offline tests.
- Preserve all historical dirty paths and old evidence, kits, masks, units, epoch claims, receipts and data. Never edit `deploy/experience/switchback-capacity-publisher.service` or `internal/workerruntime/adapter/capacitypublisher/publish_test.go`.
- Use existing storage and exact `m1_shared_volume_capacity_v1`; no external disk, main-disk repartition, migration, metadata-growth physical-upper-bound requirement or old stronger guarantee.
- New fixed units: `switchback-m1-containerd.service`, `switchback-m1-engine.service`. Private root0700 config directory `/etc/switchback/m1-engine`, regular0600 `containerd.toml` and `daemon.json`; only exclusive new targets, no overwrite.
- Unique Engine endpoint `unix:///run/switchback-engine/docker.sock`, parent root0700, socket root:root0660; no TCP listener/socket activation. Backend view `/var/run/docker.sock`; Worker gets no Docker socket.
- Existing `/run/switchback-engine` tmpfs128MiB/inodes4096/root0700 is verified, never replaced/remounted/cleaned. New runtime subtree `/run/switchback-engine/m1-resident`; Docker root `/mnt/lima-sbquota/docker`, containerd root `/mnt/lima-sbquota/docker/containerd`, overlay2 and containerd-snapshotter=false.
- Existing core `/switchback.slice/switchback-core.slice`: CPU `120000 100000`, memory3221225472, swap0, PIDs1024, counted once; fixed VM4vCPU/10GiB. No extra aggregate budget or Worker resource/capability changes.
- Both daemons `Type=notify`, `Restart=no`, `TimeoutStartSec=30`, `TimeoutStopSec=10`, `KillMode=control-group`, no enable-at-boot/RuntimeMaxSec/watchdog. Explicit containerd Start once then prove ready then Engine Start once; Engine Requisite+After, not auto-starting Requires.
- New Docker config uses standard bridge/NAT with iptables/ip6tables/ip-forward/ip-masq enabled and default core cgroup-parent; these are candidate future guest effects only.
- containerd version4, imports=[], explicit new grpc/ttrpc/shim/temp/state paths, empty grpc-tcp/debug/metrics listeners; disable CRI/NRI/dynamic opt/image-verifier restoration. Never fall back to default `/run/containerd`.
- Fixed networks: `switchback-core1011_core` internal=true; `switchback-m1-worker-control` internal=false. bridge/IPAM server-owned; reject owner/config mismatch, no delete/recreate or retry after unknown create.
- One Engine preparation deadline120seconds across all work, no reset/retry/resume/independent readback/unknown cleanup. Preserve partial outputs, exit codes and known/unknown effects. Successful daemons remain resident; no remote Stop in executor.
- Preflight rejects active/foreign writers/listeners, unsafe/existing targets and unexplained store restoration inputs; no deleting/resetting container, image, volume, BuildKit or containerd metadata. Any nonempty accepted store class needs explicit bounded read-only classification; unknown business/volume origins reject.
- Before any socket connection prove daemon PID/start/exe/cgroup/socket inode+FD owner, root permissions and complete Unix/TCP/TCP6 tables; then bounded read-only APIs. Recheck identity around collection to reject drift.
- Operational receipt schema `switchback.engine.deployment_receipt.v1`, root-private and unsigned, binds bundle/kit/source, boot/FS, both daemons, installed files, socket, EngineID, networks, ordered steps/times/effects. It cannot grant Trust/capacity/permit/Task/provider authority.
- Original qualification300second barrier and240second native window, seven check IDs, raw-v1/v2/signing/Trust/PG/reserve-before-permit/exact-CID quota/one Start remain unchanged. Production admission gates remain closed.
- Run each task's focused regressions and one appropriate full affected suite, then scoped Spec+Quality review. Fix all required findings in one wave; same reviewer incrementally rechecks. Finish one fresh whole-slice review. No user check-ins between tasks.

## File responsibilities and fixed handoff

`deploy/experience/engine/` owns candidate configuration, pure contracts/policy, fixed bundle creation, guest I/O and finite host execution. Avoid a monolithic script: `contract.py` holds constants/static policy/strict bounded DTO validation; `bundle.py` validates and builds the acyclic bundle; `guest.py` performs only fixed guest operations; `prepare.py` supervises one host/guest preparation window. Tests remain colocated and use private seams, temporary files and synthetic process tables only.

The bundle source inputs are already part of qualification builder Source records. A bundle is built only after the new complete kit exists; its manifest contains the qualification manifest SHA256, SourceRoot, TemplatesRoot and exact source records for new Engine files and current pins. A fresh phase-local `bundle-NNNN` is created exclusively; no runtime identity or receipt is inserted upstream. Fixed future copies are host `~/.switchback/engine-bundle`, guest `/opt/switchback/engine-bundle`; existing qualification copies remain `~/.switchback/qualification-kit` and `/opt/switchback/qualification-kit`. Runtime receipt is fixed `/var/lib/switchback/m1-engine/deployment-receipt.json` under a new exclusive root-owned0700 stage. Build output and this operational stage are distinct.

The receipt contract is frozen by Task1 and consumed unchanged by Task2/Task3. It must provide schema/status, bundle and kit manifest SHA256, SourceRoot/TemplatesRoot, boot/FS identities, two exact unit/main-PID/start-tick/exe/cgroup records, installed source→target file records, socket identities, Engine identity, two network identities, ordered step results, monotonic elapsed timing and known/unknown effects. Successful status means only `engine_ready`; explicit fields `runtime_qualification`, `capacity_ready`, `execution_enabled`, `paid_consumed` are false. A checked-in synthetic JSON fixture documents exact JSON property names and must be read by both Python and Go contract tests; it is never an executable authorization or runtime receipt.

---

### Task 1: Fixed configuration, pure policy and deployment contract

**Files:**
- Create `deploy/experience/engine/{switchback-m1-containerd.service,switchback-m1-engine.service,containerd.toml,daemon.json,contract.py,bundle.py,test_contract.py,README.md}`.
- Create `deploy/experience/engine/testdata/receipt-v1.json` (synthetic fixture, no real identities/secrets).
- Evidence `.scratch/runtime-m1-resident-engine-delivery-1022/task-1/`.

**Interfaces:**
- Consumes current `deploy/experience/primary-pins.json` and accepted spec.
- Produces Python `validate_configuration(files: dict[str, bytes]) -> None`, `validate_receipt(value: dict) -> None`, `validate_preflight(facts: dict) -> None`, `validate_store_inventory(facts: dict) -> None`, `validate_listeners(facts: dict) -> None`, raising `ValueError` on incomplete, malformed, unknown or inconsistent facts.
- Produces `build_bundle(kit: pathlib.Path, destination: pathlib.Path) -> dict` and `verify_bundle(bundle: pathlib.Path, kit: pathlib.Path) -> dict`; library arguments are private/offline interfaces, not actual executor CLI overrides. Exact successful bundle/receipt JSON contract is recorded in README and synthetic fixture.
- Produces fixed `FILES`, `UNIT_NAMES`, `ENGINE_SOCKET`, `RECEIPT_PATH`, `BUDGET_SECONDS` constants consumed by Task2; use task constants instead of duplicated tables.

- [x] **Step1: Write focused negative and positive contract tests, then capture RED.** Required assertions include no network/guest calls during import, malformed/extra/duplicate JSON keys, unsafe paths/links/owners, wrong masks/core limits, stale boot, restored containers/plugins/volumes, inconsistent daemon ownership and hidden TCP/ttrpc listeners. Meaningful example:

```python
def test_receipt_does_not_grant_start(self):
    record = synthetic_receipt()
    record["execution_enabled"] = True
    with self.assertRaises(ValueError):
        validate_receipt(record)
```

Run `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/experience/engine -p test_contract.py -v`; before implementation it must fail at missing module/contract, save exact exit/output in task evidence.

- [x] **Step2: Write the two units and configs with explicit fixed settings.** Engine ExecStart has exactly one `-H unix:///run/switchback-engine/docker.sock --group=root`, explicit `--containerd=...`, `--pidfile=...`, `--exec-root=...` and `--config-file=/etc/switchback/m1-engine/daemon.json`; JSON has no hosts. Containerd ExecStart uses `/usr/bin/containerd --config /etc/switchback/m1-engine/containerd.toml`. Both use exact core Slice. Include explicit grpc/ttrpc/shim isolation and disabled plugin IDs supported by the pinned version; consult current primary config source if uncertain. Static tests parse the actual JSON/TOML/INI and compare effective paths/resource/lifecycle/network policies rather than substring-only assertions.

- [x] **Step3: Implement pure strict contracts and bounded store/listener classification.** Full accepted spec, not the example above, is binding. Preserve incomplete/unknown facts as rejection. Listener acceptance binds actual PID/start/exe/cgroup and full owner FD sets to AF_UNIX listeners; no API call can satisfy the preconnection gate. Accept only explicitly classified trusted caches, reject unclassified nonempty storage; no repair/delete methods. Add schema and fixture checked cross-field identities, exact file paths and counts, finite bounds, no authority fields set true.

- [x] **Step4: Implement exclusive acyclic bundle builder.** Verify kit manifest complete coverage, source record bytes/hash/mode and pinned inputs before copying bundle leaves; output must reference exact kit manifest hash without modifying it. Use O_EXCL/private directories, reject symlinks/hardlinks and changed inputs; fsync files/parent and read back. Offline fixed build entry, if needed, accepts no arbitrary source/command/timeout override. Add synthetic tiny-kit tests including hash-cycle rejection, drift, duplicate inputs and occupied destination.

- [x] **Step5: GREEN, self-review and contract handoff.** Run the focused suite once fully green; save output/exit codes. Document exact schema, private APIs, mode expectations, accepted store classification and future actual effects, including supported-version uncertainty not tested natively. Report the changed files and test counts in `task-1-report.md`; no commits. Independent reviewer checks Spec and Quality against a fresh task diff before Task2.

### Task 2: Complete finite Engine preparation executor

**Files:**
- Create `deploy/experience/engine/{guest.py,prepare.py,store.py,test_guest.py,test_prepare.py,test_store.py}`.
- Modify Task1 files only if integration demonstrates a concrete contract defect; keep one canonical schema and fixture.
- Evidence `.scratch/runtime-m1-resident-engine-delivery-1022/task-2/`.

**Interfaces:**
- Consumes all Task1 interfaces and its frozen receipt fixture.
- Produces `prepare.py self-check` and `prepare.py actual` as the only executor CLI actions. No user path/command/timeout/allow/skip/resume arguments. `actual` uses fixed host/guest candidate paths and existing exact Lima executable/config location from the original qualification driver.
- Produces private `_run_window(ops, monotonic)` with one deadline and private bounded command/file/process operations. Actual guest entry is private fixed code invoked only by the host actual action; no public independent readback/retry.
- Produces `store.py`'s private bounded read-only bbolt-format subset decoder/classifier for actual pinned containerd/BuildKit empty indexes and explicitly nonexecuting caches. It emits Task1 facts only after checksum, root/page/inline bucket traversal, complete enumeration and format/schema checks; unknown schemas/origins reject. No bbolt/containerd dependency download, new ELF or fabricated scan-success boolean.
- Produces a successful root-private `RECEIPT_PATH` matching Task1 contract, and host partial outcome/logs on every error. Successful process identities persist; failed/unknown remote effects are not cleaned up or retried.

- [x] **Step1: RED regressions on the real control flow with private fake I/O.** Assert exactly zero command mutations/API connections before read-only gates; a first Start timeout prevents Engine Start/API/network; socket ownership uncertainty prevents all connects; unknown NetworkCreate prevents any retry/second create. Test every phase with exception/nonzero exit/timeout/truncated output and require partial step evidence. Example:

```python
def test_unknown_first_network_create_is_not_retried(self):
    ops = prepared_synthetic_ops(fail="create-core-network:unknown")
    result = _run_window(ops, ops.monotonic)
    self.assertNotEqual(result["status"], "engine_ready")
    self.assertEqual(ops.calls.count("create-core-network"), 1)
    self.assertNotIn("create-worker-network", ops.calls)
```

Run both new test modules and capture RED before executor implementation.

- [x] **Step2: Implement bounded host orchestration and actual read-only guest collector.** Verify already Running exact VM before entering; use one120second remaining budget across host and guest, no deadline reset. Bound process groups, output and file reads, no shell interpolation/arbitrary env overrides. Collect actual boot/XFS/hard quota/core/tmpfs/masks/processes/listeners/binary package+version+hash+ELF/target/store evidence; call Task1 pure validation on actual data, not prefilled positive dictionaries. Decode fixed store formats read-only: current containerd2.4.1 metadata schema is `v1/<namespace>/<object>`, version4; reject nonempty containers/sandbox/runtime/plugin/volume restores and unknown records. Accept a nonzero-length initialized but empty database only after complete traversal proves that subset. Test meta checksum/tx selection, overflow/bounds/cycles, inline buckets, duplicate/hidden restore entries, malformed/unknown schema, and unchanged file/hash identity. Root can consult `https://raw.githubusercontent.com/containerd/containerd/v2.4.1/core/metadata/buckets.go` and pinned bbolt format source; no strings scan or caller attestation as proof. Boot and filesystem identities must be stable before/after readbacks. Inspect all daemon/shim processes and relevant service/listener inventories, not a generic service name alone. No actual guest calls in this phase's tests or self-check.

- [x] **Step3: Implement exclusive stage/install and single starts.** Record claim plus exact bundle/kit/boot/FS/executable/store/config binding. Open fixed safe parents, create only new target config/units/stage with required ownership/modes and fsync/readback; reject preexisting unknown targets and retain partial artifacts. Run fixed daemon config validation and daemon-reload within the same deadline; inspect effective unit state and paths. Start containerd once and prove ready ownership before Engine Start once. Preserve default/old masks and do not issue enable/restart/stop/reset-failed/delete operations.

- [x] **Step4: Implement listener-before-connect, bounded APIs, networks and receipt.** Inventory complete Unix/TCP/TCP6 tables and all relevant owner FDs; verify filesystem socket inode/root perms independently of proc socket inode and link correct daemon identity. No socket connect before all fixed endpoint checks. Then limited Engine info/inventory and fixed network inspect/create; owner/config equality and unknown outcomes fail closed. Recheck current identities before exclusive receipt publication. Record every command's bounded outputs/exit/signal/elapsed and attempted/known/unknown effects, even if an earlier phase fails. Entire work shares original120second budget; receipt alone never authorizes a workload.

- [x] **Step5: GREEN and self-review.** Run `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/experience/engine -p 'test_*.py' -v` once after focused fixes, plus `python3 deploy/experience/engine/prepare.py self-check`. Save complete logs. Tests use synthetic facts/temp files and private operations; no native listener, daemon or subprocess fixture that touches actual service state. Write `task-2-report.md` and then get independent scoped review before Task3.

### Task 3: Qualification handoff, complete new kit and reviewed execution candidate

**Files:**
- Create `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment.go`, `qualification_engine_deployment_linux.go` and focused tests, following existing platform guards; add a non-Linux fail-closed stub only where compilation needs it.
- Modify `internal/workerruntime/adapter/capacitysnapshot/qualification_material_preflight_linux.go` and narrowly needed qualification identity/source helpers.
- Modify `deploy/experience/qualification/{build_candidate.py,window.py,README.md}` only where needed for verified handoff/new source coverage; do not add a wrapper or phase/source/output override.
- Modify `deploy/experience/engine/README.md`; create a fixed offline bundle build entry if the Task1 library needs one.
- Controller updates `docs/research/2026-10-22-first-real-run-critical-path.md`, plan checks and append-only SDD ledger after clean review.
- Evidence `.scratch/runtime-m1-resident-engine-delivery-1022/task-3/`; new kit through the original fixed builder uses the next exclusive kit number, old kits unchanged.

**Interfaces:**
- Consumes Task1's exact receipt fixture and Task2's operational records; no schema forks.
- Produces private `qualificationVerifyEngineDeployment(ctx context.Context, kit qualificationKit) error` or the exact current kit DTO name established by CodeGraph; called by `qualificationRuntimeReadback` before `qualificationFixedEngineClient`/first Info. Runtime-preflight and verify-runtime-preflight both use it. Actual read-only implementation is Linux-only; pure contract tests use private readers, no real sockets.
- Produces a complete qualification kit from original builder with20 binaries/43 build receipts, recomputed SourceRoot/TemplatesRoot and unresolved runtime fields; then one acyclic Engine bundle referencing the exact final kit. Rebuild only if source changes invalidate prior kit, preserving every attempted output.

- [x] **Step1: RED receipt/current-runtime handoff tests.** Read the Task1 fixture and prove strict schema/current source binding, receipt boot/PID/start/exe/cgroup/socket/networks drift rejection, source/install bytes mismatch rejection, symlink/hardlink/owner/mode rejection and preconnection ordering. Capture a missing/invalid receipt test where the private Engine API recorder receives zero calls. Preserve existing300/240second timing and other serialized qualification formats byte-for-byte.

- [x] **Step2: Implement readonly native handoff.** Use existing bounded proc/status/cgroup/stat/FD/mount parsing where compatible and private fixed-path reads; verify installed configuration/unit hashes against the kit's existing source records and actual current bytes, not receipt assertions alone. Verify current owner PID/start/exe/cgroup/socket/listener and two network identities under a bounded context. A network API read occurs only after native socket ownership proof. Recheck boot/process identity after collection. Do not modify `qualificationFixedEngineClient` globally into a new trust/authority mechanism; gate these two deployment-preflight callers explicitly. No PG/Trust/raw-v1/v2/Start schema changes.

- [x] **Step3: GREEN and affected offline checks.** Run `env -u SWITCHBACK_TEST_DATABASE_URL TMPDIR=/private/tmp GOCACHE=/private/tmp/switchback-qualification-1022-go-cache GOTOOLCHAIN=local GOPROXY=off GOMAXPROCS=4 go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot ./internal/platform/createdlayeripc` and relevant qualification Python regressions once, saving complete logs. Cross-compile affected Linux tests for amd64/arm64 without executing them. Do not start PG or any local runtime service. Refresh CodeGraph only via `make codegraph-init` when source index is stale.

- [x] **Step4: Rebuild complete kit and Engine bundle, verify origins.** Run original `python3 deploy/experience/qualification/build_candidate.py build` with fixed offline env. Verify every manifest-covered file, exact source+template before/after equality, Go toolchain hash, all20 executable formats and43 exit0 build receipts. Build Engine bundle exclusively using Task1 interface from that kit; verify reverse lookup of every bundled input into kit Source records and no upstream receipt/cycle. Record runtime fields null and actual windows0; synthetic fixtures are explicitly separate from future root-private receipt.

- [x] **Step5: Draft concrete operation checklist and report, then task review.** Checklist lists resident daemons, new config/unit/stage paths, normal Engine metadata/GC and guest bridge/NAT/firewall effects, both fixed networks, original image/key/PG/unit/bind preparation and the unused qualification240second window. Include remaining environment/version/store ambiguity as actual preflight rejection, no blind cleanup/retry/resume. State whole effects and actual limits; no claim of real Linux/systemd/network/Task/provider success. Write `task-3-report.md` with exact kit/bundle hashes and test/build receipts, then independent Spec+Quality review.

- [x] **Step6: Whole-slice independent review and final preservation.** Controller builds one combined diff from the fresh phase baseline, asks a fresh reviewer for Spec and Quality with all three task reports/accepted design. Resolve required findings through the owning implementer and incremental review. Source changes require affected checks and a fresh complete kit/bundle; document-only fixes need only relevant validation. Check old closed evidence/dirty paths/HEAD/index against fresh phase baseline, update progress and seal a delivery inventory. Only then request one concrete real deployment authorization; no actual action this slice.

Final source/offline delivery: kit-0008 / bundle-0003; same whole-slice reviewer incremental Spec compliant / Quality Approved C0/I0/M0 in `.scratch/runtime-m1-resident-engine-delivery-1022/whole-slice/review-fix1.md`. Final Engine97/97, affected Go, Linux arm64/amd64 test compilation, actual full-kit pure binding and independent complete artifact/preservation audits passed. Exact hashes, command receipts and the concrete pending actual checklist are indexed by the phase `completion.md`; the exclusive `phase-seal.json` covers the phase evidence. No native/guest/Engine/PG/provider deployment action has been executed or authorized by this completion.
