# M1 Complete Daemon Listener Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a complete Python/Go live v2 six-role Unix listener contract, meaningful offline regressions, and a new fully bound kit/bundle without using the guest.

**Architecture:** Keep the v1 receipt/listener interpretation available for historical evidence. Live preparation, collection, publication and qualification consume v2 exclusively. A shared synthetic corpus exercises both languages; immutable kit21 evidence remains a negative replay, because it lacks filesystem identities for the three supplementary sockets.

**Tech Stack:** Python 3.9-compatible deployment code, current builder-pinned Go 1.26.4, existing bounded Linux collectors, original offline kit and bundle builders.

## Global Constraints

- Work only in `/path/to/switchback`; protect all prior dirty work. No Git mutations or commits.
- This slice is offline only. No guest/VM/Engine/PG/service/credential/provider actions, including post-failure readback or Stop. Never scan `.scratch/runtime-m1-worker-execution-credential-1026-01` or `.scratch/runtime-m1-containerd-dump-imports-1025`.
- Preserve source-before bytes and all 6326 immutable evidence records in `.scratch/runtime-m1-complete-listener-contract-1027/ROOT-baseline.json`. HEAD `fbebfe754045bdbf1af40871a5018eed33743011` and index SHA256 `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5` stay unchanged.
- The approved scope is `docs/superpowers/specs/2026-10-27-m1-complete-daemon-listener-contract-design.md`, approved bytes SHA256 `5d7aa9ba8b033aa3f3b28093f86828d5da20ae6aa161dcd4149e7e62bfede798`.
- Fixed live schemas are `switchback.engine.listeners.v2` and `switchback.engine.deployment_receipt.v2`. Historical v1 validation must retain its meaning; no live v1 fallback or mixed-schema acceptance.
- The six ordered socket roles are containerd API, containerd ttrpc, Docker API, extension callback, internal metrics, libnetwork external key. Main paths/modes remain unchanged. Additional static paths are `/run/switchback-engine/m1-resident/docker/extensions/callback.sock` and `/run/switchback-engine/m1-resident/docker/metrics.sock`, both mode `0777`. Exactly one libnetwork path matches `/run/switchback-engine/m1-resident/docker/libnetwork/[0-9a-f]{12}\.sock`, mode `0600`. No specific random basename is pinned.
- Socket identities retain every existing `SOCKET_KEYS` field, root:root, nlink 1, unique positive kernel/FS inode, same tmpfs device, exact daemon PID/start/unit and current boot. Unknown/foreign/missing/duplicate roles, symlink/wrong-type/ownership/mode/device, colliding identities, drift and daemon TCP/TCP6 remain rejected.
- Add mandatory `socket_parents` to v2 listener facts and v2 receipts. Each entry has exactly `path,device,inode,kind,uid,gid,mode`, with kind `directory`, root:root and mode `0700`. Ordered paths for engine_ready are `/run/switchback-engine`, `/run/switchback-engine/m1-resident`, `/run/switchback-engine/m1-resident/docker`, `/run/switchback-engine/m1-resident/docker/extensions`, `/run/switchback-engine/m1-resident/docker/libnetwork`; preflight has none, containerd_ready has the first two. Require unique directory identities on the runtime tmpfs and exact immediate-parent correspondence to socket fields. Recheck socket and parent identities before/after each full collection.
- In the future exclusive `install_files` step, create the newly owned runtime root and its `docker`, `docker/extensions`, `docker/libnetwork` directories at `0700` using the existing no-replace, no-follow `_mkdir` behavior. No adoption or chmod repair of existing directories. This prevents Moby's `MkdirAll(...,0600)` libnetwork default from denying traversal to the already approved Native capability set. Do not add capabilities.
- Preserve full bounded Unix/TCP/TCP6, process and FD inventories, front/back boot/PID/start/exe/hash/cgroup checks, foreign daemon/shim/writer rejection, existing watchdog behavior, resource/quota/PG/Trust/Start gates and `m1_shared_volume_capacity_v1` semantics.
- Preserve Engine 120 seconds, qualification 240 seconds, barrier 300 seconds, preparation 7200 seconds, guest argv `<65536` bytes and all other output/frame/deadline limits. Packing may change representation only if decoded bytes and exact module sets are verified and tests prove losslessness; never truncate inventories or raise limits.
- Positive six-role filesystem metadata is explicitly synthetic, never an actual runtime proof. Keep the original 139-row inventory unchanged. New kits/bundles do not authorize transitioning the already-started Engine; future live ownership/switching needs its own concrete operation.

---

### Task 1: Python live v2 contract and real collector

**Files:**
- Modify: `deploy/experience/engine/contract.py`, `guest.py`, `prepare.py`, `test_contract.py`, `test_guest.py`, `test_prepare.py`, `README.md`.
- Create: `deploy/experience/engine/test_listener_v2.py`.
- Create: `deploy/experience/engine/testdata/listener-v2-corpus.json`, `receipt-v2.json`, `listeners-kit21-v1.json`, `listener-v2-provenance.json`.
- Evidence/report: `.scratch/runtime-m1-complete-listener-contract-1027/task-1-*`.

**Interfaces:**
- Consumes: unchanged historical `validate_listeners(facts: dict) -> None`, `validate_receipt(value: dict) -> None`; existing exact closed DTO helpers.
- Produces: `validate_listeners_v2(facts: dict) -> None`, `validate_receipt_v2(value: dict) -> None`, `validate_preflight_v2(facts: dict) -> None` in `contract.py`; old public validators retain historical semantics. Live `guest.py` and `prepare.py` explicitly use these new functions. The outer preflight schema stays v1; the v2 preflight validator requires nested listeners.v2.
- v2 has the same top-level fields as v1 plus mandatory `socket_parents`; v2 receipt phases carry the latest fully validated parent subset, including an empty list before any valid listener sample. Final identity/publication includes and compares this field.
- Shared corpus is a closed object `{ "schema":"switchback.engine.listener_corpus.v2", "provenance":"synthetic; no runtime authority", "cases":[{"name":str,"accepted":bool,"listeners":dict,"receipt":dict}, ...] }`. Each case is accepted only if both strict v2 listener and strict ready-v2 receipt validation succeed. JSON bytes are decoded with existing strict duplicate-key/integer behavior.

- [x] **Step 1: Preserve and reproduce the failure before changing production code.** Copy only `value` from `.scratch/runtime-m1-kit21-carrier-execution-next/ROOT-listener-inventory-05.json` to the public fixture, record wrapper and extracted canonical SHA256 in provenance, and assert historical validation raises `hidden/foreign Unix listener`. Add v2 import/use tests to obtain a concrete failing RED. Do not remove the three actual rows or synthesize missing actual facts.

```python
with self.assertRaisesRegex(ValueError, 'hidden/foreign Unix listener'):
    validate_listeners(actual_v1)
self.assertEqual(len(actual_v1['unix']), 139)
with self.assertRaises(ValueError):
    validate_listeners_v2(actual_v1)
```

- [x] **Step 2: Implement v2 strict helpers with shared private policy helpers rather than duplicating the whole v1 validator.** Phase socket counts are `0,2,6`; parent counts are `0,2,5`. Ordered role/path/mode checks bind supplemental roles to dockerd. Full-table required-inode coverage and owner validation must still run. Reject multiple listening kernel inodes for one protected pathname. Preserve legitimately established nonlisteners with different kernel inodes on approved paths: the immutable actual inventory already has two such containerd.sock connections, and all exact owner/FD/start/exe/cgroup checks remain mandatory. Keep legitimate daemon nonlisteners constrained to the exact six-role path set or empty path and only approved daemon owners.

```python
def validate_listeners_v2(facts: dict) -> None:
    # Closed v2 shape, complete role and parent checks, then all table/FD rules.
    return _validate_listener_inventory(facts, version=2)

def validate_receipt_v2(value: dict) -> None:
    return _validate_deployment_receipt(value, version=2)
```

The private helper names can follow current file conventions; the three public v2 signatures and wire ABI are fixed. Historical wrappers select version 1 explicitly, never based on untrusted schema alone.

- [x] **Step 3: Wire all live paths and collect the entire contract.** Discover the sole libnetwork role only from the complete parsed Unix table, prove its filesystem object/parent chain, sample identities twice, retain all observations before validation, and compare parents in every API-before/API-after/final/publish gate. The Docker API connection must find the Docker API role by its exact path; it must not use `sockets[-1]` after adding roles. Exclusive installation creates the four fresh runtime directories stated in Global Constraints.

```python
api_socket = next(s for s in before['sockets']
                  if s['path'] == ENGINE_SOCKET.removeprefix('unix://'))
```

- [x] **Step 4: Include the new explicit files in bundle `_ENGINE_LEAVES` and require `testdata/receipt-v2.json` for live self-check; preserve v1 fixture membership and keep unrelated leaves rejected. Add meaningful fixtures and regressions.** Include valid six roles with at least two distinct valid random IDs, containerd/preflight subsets, current raw negative replay, v1-as-live, missing/extra/duplicate roles, invalid basename/escape paths, owner/PID/start/exe/cgroup/hash/boot drift, wrong socket/parent metadata, inconsistent parent chain, duplicate kernel/FS inode, foreign FD and TCP/TCP6. Mutate fields that cause the claimed validation to run; no vacuous positive tests. Real `_collect_listeners` synthetic kernel tests verify discovery, first/last lstat and FD/process drift, not only DTO helpers. Preserve v1 historical tests; update live fake operations to v2 explicitly.

- [x] **Step 5: Run focused RED/GREEN then the complete Engine suite once.** Use the pinned host Python39 for compatibility and a clean evidence directory; imports have no side effects. Commands:

```sh
cd deploy/experience/engine
/Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3 -I -B -c 'import sys,unittest;sys.path.insert(0,".");unittest.main(module=None,argv=["tests","discover","-s",".","-p","test_*.py"])'
```

Expected exit 0, no skips/unexpected warning/traceback. Save actual argv/exit/complete stdout+stderr and SHA256, update README with ABI/role/provenance semantics, and write task-1-report.md with exact changed paths and compatibility evidence. Do not commit. A fresh task review gates completion.

### Task 2: Go qualification v2 reader and cross-language parity

**Files:**
- Modify: `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment.go`, `qualification_engine_deployment_readback.go`, `qualification_engine_deployment_linux.go`, `qualification_engine_deployment_test.go`, `qualification_execution_profile_linux.go` (only two direct live v2 decoder call sites).
- Create: `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_v2.go`, `qualification_engine_deployment_v2_test.go`.
- Evidence/report: `.scratch/runtime-m1-complete-listener-contract-1027/task-2-*`.

**Interfaces:**
- Consumes: Task 1's exact v2 wire shape, ordered role table, parent-chain DTO and shared `deploy/experience/engine/testdata/listener-v2-corpus.json`.
- Produces: a strict ready-v2 receipt validator and native current-readback validator used exclusively by production qualification readers; preserve an explicit historical v1 decode/validation route for old fixtures. v1 remains unable to authorize current qualification. Private Go type names may follow repository conventions; no exported protocol outside the accepted Engine receipt ABI changes.

- [x] **Step 1: Add RED tests using the exact shared JSON corpus and current six-role positive receipt.** A live reader must reject v1, even if historical parsing succeeds. Go and Python must agree on each `cases[].accepted`; report case names/count/hash. Never regenerate a separate Go corpus with different meanings.

```go
for _, tc := range corpus.Cases {
    t.Run(tc.Name, func(t *testing.T) {
        err := validateBothV2(tc.Listeners, tc.Receipt)
        if (err == nil) != tc.Accepted { t.Fatalf("acceptance=%v error=%v", tc.Accepted, err) }
    })
}
```

`validateBothV2` is the test adapter to actual strict production validators, not an independent test implementation. Preserve strict duplicate/null/unknown/number-token decoding; tests must pass JSON into production decode.

- [x] **Step 2: Implement v2 strict receipt and current identity policy with bounded reusable helpers.** Bind receipt to current candidate Source/templates/binaries, filesystem/boot/runtime parent, six socket identities, exact full private parent chain, daemon identities and complete listener ownership. Check random libnetwork basename syntax and role uniqueness before use. No sanitizing/drop-extra transformation through the v1 validator.

- [x] **Step 3: Extend real Linux collection.** Read complete tables and owner processes as before; discover and require exactly one libnetwork listener. Open every fixed parent chain without symlink traversal, validate type/owner/device/mode, collect each socket via directory FD/no-follow, and recheck filesystem/parent/FD/process/boot identities after collection. Current-readback must compare all six socket and five parent identities against v2 receipt before/after Engine API requests.

- [x] **Step 4: Cover decode/receipt/current-readback/collector boundaries.** Include schema mismatch, parent races, foreign shared owner, same pathname on multiple LISTENING kernel inodes, legitimate same-path exact-owner established nonlisteners, unrelated listeners retained, and daemon TCP/TCP6. Keep the original v1 historical tests running through their historical route and test that the production reader rejects them. Add synthetic real collector tests only through existing safe seams; no live `/proc` daemon observations count as fixture proof.

- [x] **Step 5: Run exact Go package and cross-build checks offline.** Use `GOPROXY=off GOSUMDB=off GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot` with the existing approved test env/temporary cache. Cross-compile the affected Linux arm64 path with the original builder at Task3; do not execute it. Save argv/exit/raw output/hash, no PG or provider test env, then write task-2-report.md and obtain fresh task review before Task3.

### Task 3: New complete candidate, evidence retention, and delivery verification

**Files:**
- Create only under `.scratch/runtime-m1-complete-listener-contract-1027/task-3/`, plus the original builder's next exclusive output `.scratch/runtime-m1-qualification-delivery-1022/task-4/kit-0022` (if absent; never overwrite an allocated kit).
- Clone private evidence modules from `.scratch/runtime-m1-kit21-carrier-rebind-1026` into Task3 before any edits: `copy_switch.py`, `guest_copy.py`, `launch_engine.py` and their exact dependencies/tests. Do not alter the sealed original.
- New bundle: `.scratch/runtime-m1-complete-listener-contract-1027/task-3/bundle-0001`.

**Interfaces:**
- Consumes: reviewed final Source and templates, the unmodified original kit/bundle builders and existing bounded private carrier modules.
- Produces: complete kit22+bundle manifests, Source/templates/binary build-info audit, packed guest programs below 65536, evidence-retention module changes and tests, and an offline-only handoff. No actual grant or ready-to-enter-guest claim.

- [x] **Step 1: Preserve and add the two evidence improvements.** Retain already-held new host/guest copy archive root identities in existing preparation readback/summary so subsequent evidence can use them without another guest window. Record IDs exactly as observed/held, never infer inode values. In `launch_engine`, archive local outcome bytes and hashes after the single bounded Engine subprocess returns nonzero as well as zero; then propagate failure and stop without SSH/readback/retry. Validate paths/bounds before use, keep raw exit status and unknown semantics, cover missing/malformed local outcomes without treating them as successful effects.

```python
# The outcome is read from already-created host evidence only; no guest call.
result = run_once()
archive_local_outcome_if_present(result)
require_success(result)
```

The implementation must use the existing command recorder's real nonzero path rather than a test-only abstraction that bypasses its exception behavior.

- [x] **Step 2: Run focused evidence RED/GREEN regressions and freeze final Source before building.** Tests prove no post-failure command is dispatched, archive-root IDs survive the existing final readback, and unknown/missing output stays a failure. Exact original budgets and authority fields stay closed. Audit only approved Source changes versus source-before, original immutable evidence, HEAD and index; include source membership additions from Task1/2.

- [x] **Step 3: Build a full original kit and full Engine bundle.** Invoke `deploy/experience/qualification/build_candidate.py` unmodified in the previously verified clean Homebrew Python parent environment; do not use `/usr/bin/python3` which injects SDKROOT/CPATH. Original builder makes 43 actual child calls/20 binaries, each must return 0 with raw receipts. Bundle binds the new kit manifest and exact Source/templates. Never reuse the old ELF/manifest while claiming new behavior. Audit all leaf hashes/modes and all 20 binary Go module/version/arch/build settings against their raw build commands, using the prior delivery audit's semantics.

- [x] **Step 4: Verify lossless program transport within the original argv limit.** Compose all six actual guest programs with the final Source bytes. If representation needs compression, use one explicitly bounded decoder, require SHA256 and exact decoded byte lengths/module set before executing; reject trailing/corrupt/expanded/duplicate data and keep all original runtime/output/deadline gates. Test exactly 65535/65536 boundaries and the actual final programs. Do not add a new runtime privilege or transfer mechanism.

- [x] **Step 5: Complete delivery tests, independent task review and most-capable whole-slice review.** Give reviewers exact source-before diffs, task briefs/reports and frozen checksums; resolve all Critical/Important issues with covering tests. Do not rerun unchanged suites without cause. Seal final phase with an auditable self-excluding manifest, exact file/mode/hash coverage, Source and prior-evidence immutability and no secrets/environment authority.

- [x] **Step 6: Write a concrete offline handoff.** Include the new kit/bundle manifests, six-role parity and test results, raw replay limits and remaining live requirements. Explicitly record that the last real Engine window failed at engine_ready after both Start calls; no current running-state assertion. A future active-Engine ownership/switching operation and fresh runtime authorization remain necessary; do not generate an executable grant or ask for it during this offline slice.

## Final offline closure

Whole review I1 closing ordinary data-root writer gap was fixed by applying the unchanged process policy to both samples. Final Source 225 /private212 /Go package /51case parity /43newraw20binary semantic /newexactpair noSKIP PASS; whole rereview C0/I0/M0. Final candidate is kit23 +whole-fix-delivery/bundle-0001; kit22 and oldtask3 are superseded and preserved. SourceRoot11dbae2d86a0bc4198888a2e8d0e853a71c4d50c3ef50c4dccac1e46fd1d4c21; no runtime/credential/provider authority or live-syscall success claim.
