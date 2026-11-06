# M1 B3-E Diagnostic Carrier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 离线交付能独立判读 B3-D 输出的只读取证 carrier 与唯一新 arm64 ELF 候选，准备可审阅的未来单次诊断清单。

**Architecture:** guest 只做固定 stage、单次 launch 和有界原始采集；host 先持久化 raw，再独立投影 output 与 manager。保留 B3-C 的 FD/transport/once 机制，以新文件定向复用，移除 guest DTO/authorizing manager/Stop。所有结果只有诊断格式意义，不形成执行关联或准入权威。

**Tech Stack:** Python 3 标准库/unittest；Go 1.26.4 cross-build linux/arm64；ELF 只读检查；fixture-only IO；冻结 JSON receipts/SHA256。

## Global Constraints

- Binding spec: `docs/superpowers/specs/2026-10-21-m1-deployment-diagnostic-carrier-b3e-design.md`, SHA256 `4038d530a361a2cb55e6f12ca43501f7c15dd19f836371698afd84b0cfdc77d2`.
- Work only in `/path/to/switchback`; HEAD `fbebfe754045bdbf1af40871a5018eed33743011`, branch/index unchanged. No stage/commit/push/reset. All 1783 baseline Git-visible files,977 old inputs,9 B3-D source files and646 prior bound artifacts are immutable.
- New phase `.scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021/` (P below); exactly eight software files: guest.py,host.py,output_mapper.py,manager_forensics.py,test_guest.py,test_host.py,test_output_mapper.py,verify.py. Other new files are evidence/build/config manifests only. Existing Go and old phases remain read-only.
- Offline only: never enter guest, run ELF/native CLI/carrier modes, grant capability, create native window/actual marker, touch VM/unit/Engine/PG/Backend/Worker/provider, or change deployment/Trust/quota. `capacity_ready=false`, `execution_enabled=false`, `paid_consumed=false`.
- New stage `/var/lib/switchback-deployment-diagnostic1021-b3e`; new unit `switchback-deployment-diagnostic1021-b3e.service`; cgroup `/system.slice/switchback-deployment-diagnostic1021-b3e.service`. Generate one UUID nonce offline and freeze it. New window `.scratch/runtime-m1-deployment-diagnostic-native-window-b3e-1021/` must remain absent.
- Modes only preflight,stage,actual,readback; request version3; guest schema `switchback.b3e.guest.v1`,host schema `switchback.b3e.host.v1`. Actual alone declares one fixed systemd-run; other modes zero unit mutation; never Stop/restart/reset-failed/kill/remove. This resolves the design test shorthand “四 mode 全路径证明无 unit 操作” consistently with its explicit single actual launch.
- All projections `kind=diagnostic_only`, `authority=none`, `execution_linkage=unproven`; `remote_termination=unknown`. Captured is raw evidence, never native success. No ownership/policy-equivalence/can_stop/can_start verdict.
- Limits incl LF: stdout32768,stderr4096,show/fragment65536 each,mountinfo1MiB,stage request12MiB; transport stdout256KiB/stderr64KiB. Guest source and actual encoded bootstrap <=65536 each. Total budgets30/45/45/30seconds; reserve local cleanup time. Kill/reap only own transport/client process groups.
- Preflight must verify fixed Running VM and no stage/unit; no VM start. Stage/root/ext4/ancestors/root ownership/no group-world-write/FD nofollow/same-device/exclusive identity/hash/16MiB/mountflags/path+FD X_OK remain enforced. Stage0700,guestELF0500,outputs/binding/marker0600. Safe readback must not require writable/executable mount but still verifies identity.
- Host actual once consumed at entry; guest actual once after valid stage binding before fresh launch admission/unit absence. Each mode exclusive attempt dir. Incomplete transport/capture persistence ends guest IO; no retry/reacquisition. Readback requires complete recorded actual,continuous prior,boot/source/candidate/stage/ELF/actual-marker equality.
- Unit declaration unchanged: root/root,CAP_SYS_ADMIN only,NoNewPrivileges yes,AF_UNIX,RestrictNamespaces yes,SystemCallFilter `~@mount @reboot @swap`,Restart no,UMask0077,CORE0,KillMode control-group,Startup10s/Stop5s,CPU20%/period100ms,Memory128MiB/Swap0/Tasks64,CPU2s/FSIZE32768,oneshot,RemainAfterExit yes,SuccessExitStatus2,stdin null,fixed dual output. No --wait/--pipe/--collect/--replace. Declarations are not proof of effective permissions.
- Reuse frozen B3-C source as reduced new files; no runtime source-text replacement or changing imported globals. Frozen manager_forensics.py copied byte-for-byte from B3-D, SHA256 `cf7e424d6393a7dc2217f6e6c39d880786160b18789262f0f30ca2d23490974f`.
- Build only `/usr/local/go/bin/go`, expected `go version go1.26.4 darwin/arm64`; GOOSlinux,GOARCHarm64,CGO_ENABLED0,GOMAXPROCS2,GOPROXYoff,GOSUMDBoff,GOTOOLCHAINlocal,private GOCACHE/GOTMPDIR. `go build -p=2 -trimpath -buildvcs=false -mod=readonly -ldflags=-buildid= -o <P>/artifact/switchback-deployment-diagnostic-linux-arm64 ./cmd/switchback-deployment-diagnostic`; no execution/download/install/dependency mutation. ELF64LE,machine183,<=8MiB,host0400. Receipt records argv,allowlisted env,tool hashes/version,Go source/module inputs before/after,modulecache verification,buildinfo/hash.

---

### Task 1: Pure output assessment and frozen manager mapper

**Files:** Create P/output_mapper.py,P/test_output_mapper.py,P/manager_forensics.py; evidence P/task-1-*. No other source edits.

**Interfaces:**
- Consumes complete stdout/stderr bytes plus canonical expected_boot; fixed new cgroup above. Read old guest.validate_diagnostic for strict success v1 and Go deployment_diagnostic_types.go/Validate for exact failure enum shape.
- Produces `assess_output(stdout: bytes, stderr: bytes, expected_boot: str) -> dict`. Fixed keys schema/kind/authority/execution_linkage/classification/expected_probe_exit_from_packet/sources, optional packet only valid recognized output. Source facts bytes+sha256. Schema `switchback.deployment.output_assessment.v1`.
- Produces unchanged `map_manager_forensics(data: bytes) -> dict`, `ManagerForensicsError`; host must catch bounded mapper invalid/unavailable separately.

- [x] **Step 1: RED matrix before implementation.** Write unittest fixture constructors for the closed DTO schemas, then first test:

```python
failure = b'{"schema":"switchback.capacity.deployment_failure.v1","kind":"diagnostic_only","stage":"write","error_class":"write_failed"}\n'
result = assess_output(b'opaque', failure, '11111111-1111-4111-8111-111111111111')
self.assertEqual(result['classification'], 'output_write_failure')
self.assertEqual(result['expected_probe_exit_from_packet'], 1)
self.assertEqual(result['authority'], 'none')
```
Run `python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021 -p test_output_mapper.py -v` before mapper exists; record expected import failure.

- [x] **Step 2: Implement closed validation and classification.** Return stderr exact write/write_failed first regardless stdout; success stdout+empty stderr => success_packet/0or2; failure stdout+empty stderr =>failure_packet/1;both empty=>missing_output/null;elseinvalid_output/null. Strict one object plus one LF;duplicate/nonfinite/unknown/missing/type/bool integers/null optionals/bounds reject. Optional last_primitive observe-only,errno failed-only1..4095. Preserve full original recognized packet without fabricating boot/PID. Success uses internal callerPID/namespace-derived exit for format consistency only; expected_boot from argument,new fixedcgroup. Copy manager bytes exactly.

- [x] **Step 3: Complete focused contract regressions and GREEN.** Cover full success with denied namespace=>2; all stage/class/operation/target/outcome combinations; sourceSHA;failure omission/null;stdout32768/32769,stderr4096/4097;LF,duplicate,nan,bool,unknown,missing,key mutation;stdout full+stderrwriteerror priority;missing/invalid. Whitespace may pad inside the one object to exact bounds. Read-only compare Go valid/reject contract. Run command from Step1 once for full Task1; save output+inputsource hashes, exact manager hash, and report.

- [x] **Step 4: Review gate.** Root creates complete new-file diff and independent spec+quality review; address Critical/Important with focused tests and re-review. No commits. Register task complete only when scoped review approved; cross-task items assigned to Task3/4.

### Task 2: New offline ELF provenance and reduced standalone guest

**Files:** Create P/guest.py,P/test_guest.py; P/artifact/*,build-receipt.json,identity.json and Task2 evidence. Read Task1 interfaces, frozen B3-C guest/test sources. Existing files immutable.

**Interfaces:**
- `identity.json`: new nonce,stage,unit,cgroup,artifact path/bytes/sha256,build_receipt path/sha256. Constants frozen into guest/source candidate; no runtime selectors or imported-global mutation.
- Guest exports fixed `request(raw: bytes, mode: str) -> dict`, `State(mode: str, io)` with preflight/stage/actual/readback, `_dispatch(mode: str, raw: bytes, io) -> dict`, and standalone `main` guarded from import. Reuse old IO contract where practical; document exact signatures exported to host tests in Task2 report.
- Reply fixed guest schema,mode,status,binding,raw facts/errors plus diagnostic-only invariants; no DTO/authorizing manager/termination projection. Raw items independently carry complete/bytes/hash/metadata and bounded errors.
- Host Task3 embeds the entire guest source as bootstrap and uses native fixed IO; tests only call fixture IO.

- [x] **Step 1: Verify tool/dependencies and build once offline.** Use Python subprocess argument arrays with global build constraints, fresh tempfile.mkdtemp under /private/tmp for caches. Before/after snapshot participating repo Go sources,go.mod/go.sum,module versions and tool identity. Read-only `go list -deps -json`/`go mod verify` with networkdisabled and privatecache; missing cache stops. Build argv fixedabove; read-only `go version -m <artifact>`; validate header/size,chmod0400,record complete receipt. Generate UUID once and identity. Never execute ELF.

- [x] **Step 2: RED guest mutation-boundary fixture.** Reduce old in-memory IO fixture, add actual marker+binding prerequisites then test:

```python
reply = run_readback_with_valid_actual_marker(io)
self.assertEqual(reply['remote_termination'], 'unknown')
self.assertEqual(io.unit_mutations, [])
self.assertIn('readback_capture', io.reached)
```
Fixture helper must traverse real dispatch/readback and assert branch reached; report import/missing implementation failure before guest implementation.

- [x] **Step 3: Implement reduced standalone guest.** Preserve fixedfd/path/bootstrap/budget/pump security from B3-C. Remove guest DTO parser,authorizing manager,oldpost and all Stop paths; collect independent raw stdout/stderr/show/fragment even manager malformed. Stage binds newELF/nonce/source/candidate/boot; actualonce before fresh launch checks,onefixedlaunch. Readback requires validactualmarker and safe read identity thencapture only. Raw incompleteness stays visible and cannot become complete. Newschema/requestversion; CLI rejects post and allselectors. No runtime text patching. Testfixture time/process IO uses no native boundary.

- [x] **Step 4: Full guest GREEN and build/source review gate.** Cover all modes,actualone-launch/othermodeszero,noStop evenhealthymanager;truebranchreadback;onceorder,doubleonce;source/candidate/boot/nonce/ELF/stage drift,unitcollision;mountflags/FDXOK/ROreadback;shortread/overflow/identitydrift;show/fragmentfaultindependence;cancel/lateerror/budget;ownprocessgroupcleanup. Run `python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021 -p test_guest.py -v`, save output/hashes,AST native-boundary proof,bootstrap exact size and build metadata. Independent Task2 spec+quality review; focused fixes/re-review; no commits.

### Task 3: Host persistence, continuous binding and offline verifier

**Files:** Create P/host.py,P/test_host.py,P/verify.py; candidate.json,release.json,source-manifest.json,fixture provenance,Task3 evidence. Do not edit accepted Task1/2 sources except evidence-backed fix with explicit root ownership transfer.

**Interfaces:**
- Consume Task2 fixed identity/guest IO/reply; frozen eight sourcefacts; source-manifest `{files:[{path,bytes,sha256}]}`; candidate binds oldseals/newELF/buildreceipt/8sources/nonce/stage/unit/argv/properties/budgets. release binds candidate/source/build.
- Host `validate_reply(...)`, `prior(...)`, `_run(mode, io)` (exact practical IOsignature documented inreport) retains frozen B3-C boundedtransport/clientprocess semantics. Native CLI only onefixedmode; fixture-run functions do not launch native.
- Host receipt schema `switchback.b3e.host.v1`, always diagnostic-only/non-authorizing; save source/candidate/prior/rawtransport+envelope before output/manager projections. `output_assessment=null` for incomplete dual capture; catch manager errors separately; keep launch rc,transport rc,manager rawexit,packetexpectedexit separate.
- `verify.py` default fixed read-only offline checks fixtures/AST/manifests/header/hash/buildinfo; no carrier entry and no nativewindow creation. Evidence destination optionally supplied only by fixed verification harness,never arbitrary executable selector.

- [x] **Step 1: RED persistence/independent-projection tests.** Fixture correct actual/prior/marker chain,managerinvalid+validfailurepacket,then assert:

```python
receipt = fixture_actual_with_saved_raw_and_invalid_manager()
self.assertEqual(receipt['output_assessment']['classification'], 'failure_packet')
self.assertIsNone(receipt['manager_forensics'])
self.assertEqual(receipt['remote_termination'], 'unknown')
self.assertLess(receipt['raw_saved_order'], receipt['projection_order'])
```
Use real host dispatcher+persistence IO fixture,not prebuilt receipt. Run `python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021 -p test_host.py -v` before hostimplementation; preserve RED evidence.

- [x] **Step 2: Implement host/candidate/release.** New provenance must not assume oldartifact equality. Bind new buildreceipt/ELF+sourcefacts and oldseals;hostonce before bindings;exclusiveallmodeattempt;priorchainchecks requestversion3/newschemas/continuousboot/identity/actualmarker. Persist rawfirst; output andmanager independent. Transport/persistenceincomplete=>no furtherguestIO orreadback;permodefailureunknown=>norepeat. Strictfixednativeargv/environment; noStop/oldpost; noauthorizingverdict.

- [x] **Step 3: Implement fixed verifier and GREEN.** Regression covers validactualmarker reachedreadback,healthymanagerzeromutation,managerfailedemptycgroup/expandedfilter fixture replay with provenance,oldschema/postreject,drift,mismatchprior/nonce/source/candidate/artifact/boot/marker,hostdoubleonce/guestdoubleonce,beforefreshadmission,transport/persistencefailures,stderrpriority,incompletecaptures,overflow,budgets/processgroups/descriptorcleanup. Run Step1fullhost command once; mapper/guest rerun only if changed. Read-only verify must check completeeightfiles+encodedbootstrap,old646boundartifacts/977inputs/9sources/1783baseline and registerednewplan,ELFheaders/0400/receipt,absenceofnativewindow. Save source-bound outputs/report.

- [x] **Step 4: Review gate and fixes.** Independent Task3 spec+quality reviewer sees fullnewfilediff,brief,report,concreteinterfaces; resolve cross-task items and Critical/Important; focusedfix tests/re-review. No commits.

### Task 4: Final evidence, independent whole review and seal

**Files:** P/final-verification.json,source-final/*,final-source-manifest.json,operation-window.md,report.md,outcome.json,phase-seal.json,review packages/receipts; this plan checkboxes and new `.superpowers/sdd/m1-deployment-diagnostic-carrier-b3e-1021-progress.md` ledger only.

**Interfaces:** Frozen P eight sources,candidate/release/ELF/buildreceipt. Final sourcefacts and receipt hashes bind exact tested bytes; seal binds everyphase artifact except itself,plus plan/ledger finalsnapshot. All baseline/oldphase verification must be explicit,not inferred from test pass.

- [x] **Step 1: Final related full offline suite once.** Run `python3 -B -m unittest discover -s .scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021 -p 'test_*.py' -v` and fixed verify.py only after sourcecandidate freeze. Preserve exit/stdout/stderr/testcount/sourcehashes. Run no unchangedGo native tests; build is provenance/compile evidence only. Fixes rerun coveringtests; record final exact hash coverage.

- [x] **Step 2: Prepare concrete future operation-window/report.** Freeze all8sources,ELFbuildprovenance/candidate; describe exactfourmodeargv,source/candidate/ELF/nonce/stage/unit/budgets,exclusiveonce,readbacknoStop,retainedunit/stage,terminationunknown and stopconditions. Mark futureauthorizationsnotgranted/nativewindowabsent;neverwriteactualmarker. Report native rootcauseunknown and threegatesfalse.

- [ ] **Step 3: Whole review and preservation gate.** Mostcapable independent reviewer reads wholechangepackage+spec+Taskreviews+exacttestreceipts; C/I fixes asonebatchwithfocusedtests and re-review. Root separately verifies1783baseline+newplan,977oldinputs,9B3Dsources,646sealentries,HEAD/index,managerhash,newELF0400/bootstrap<=65536andwindowabsent. No furthernativeactions.

- [ ] **Step 4: Finalize ledger/plan and seal.** Mark only checked taskstep lines with anchored checkboxregex; inlineheaderexample remains. Snapshot finalplan/ledger; writeoutcome andcompletephase-seal with hashes/bytes of allotherphasefiles. Read backverify everysealentry and no extra artifacts. Report concise deliverable,tests/review,unchangedgates,remainingseparatelyauthorizednativewindow. No mutationafterseal.
