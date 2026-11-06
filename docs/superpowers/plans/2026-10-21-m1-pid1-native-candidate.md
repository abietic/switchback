# PID1 Native Candidate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task by task. Steps use checkboxes.

**Goal:** Assemble a new offline native candidate containing the approved PID1 zero-starttime fix, without entering guest or executing the deployment ELF.

**Architecture:** Rebuild the existing v1 CLI, then derive exactly eight carrier sources from F plus the already verified zero-aware mapper. Keep the four-step/once/supervisor state machine and rebind fixed identity and build/source evidence.

**Tech Stack:** Go 1.26.4, Linux arm64 ELF, Python 3 stdlib, existing F fixtures and readonly verifier.

## Global Constraints

- Workdir /path/to/switchback; candidate .scratch/runtime-m1-deployment-pid1-native-candidate-1021; only the new candidate plus this new plan may be written. No product Go edits.
- Approved design docs/superpowers/specs/2026-10-21-m1-pid1-native-candidate-design.md SHA256 43953da06e002359699ae6cd6f4834f33f0646f95662e40fb3aead9bf6153748.
- Fixed window .scratch/runtime-m1-deployment-pid1-native-window-1021 must remain absent.
- Fixed stage /var/lib/switchback-deployment-diagnostic1021-pid1; unit switchback-deployment-diagnostic1021-pid1.service; cgroup /system.slice/switchback-deployment-diagnostic1021-pid1.service; nonce a37bf58d-b4df-461d-8055-440a80bd5338.
- No guest/VM/unit/Engine/PG/Backend/Worker/publisher/provider calls or native entry. No capability/key/journal/quota/gate changes. No deployment ELF execution.
- Preserve HEAD fbebfe754045bdbf1af40871a5018eed33743011, branch codex/observability-master-integration-1009, empty index, all existing source/artifacts/once records. No stage/commit/push/reset/clean.
- Existing fixed Go source SHA for pool_native.go is 9b1c10cbae79bf81c6c1cbfff6e6451ced342a58792d8e4f85260d0ddb1ca4f6.
- Build only /usr/local/go/bin/go go1.26.4, GOTOOLCHAIN=local, GOPROXY/GOSUMDB=off, GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOMAXPROCS=2; fresh /private/tmp owned cache/tmp only.
- Python -I -B, explicit local sys.path and fake/read-only IO. No pycache additions in frozen parents. All fixture subprocess/file tests are host-local and may not enter guest.
- F source root .scratch/runtime-m1-deployment-diagnostic-guard-b3f-1021; old build only read from .scratch/runtime-m1-deployment-diagnostic-carrier-b3e-1021/build-receipt.json. Mapper source is .scratch/experience-progress-2026-10-21/fix-candidate.
- Eight native source filenames remain guest.py, host.py, output_mapper.py, manager_forensics.py, test_guest.py, test_host.py, test_output_mapper.py, verify.py. manager_forensics.py stays SHA256 cf7e424d6393a7dc2217f6e6c39d880786160b18789262f0f30ca2d23490974f.
- Wire schemas, 30/45/45/30 step budgets and total 150s, raw-first, cleanup/remote unknown semantics and F unit resource/capability properties are unchanged except fixed unit/stage identity.
- Task 1 owns build receipt/logs/artifact/build-source only. Task 2 owns eight carrier sources, their new offline assembly helper/metadata/snapshots/tests/logs. Controller owns global constraints/approval/baseline/progress/reviews/operation-window/seal.
- Do not repeat unrelated Go/race suites when current source hashes match the already verified fix. New candidate regression/readonly verifier and source/binary provenance must run.

### Task 1: Build and bind new deployment ELF

**Files:** Create .scratch/runtime-m1-deployment-pid1-native-candidate-1021/artifact/switchback-deployment-diagnostic-linux-arm64, build-receipt.json, build-source/ and task-1-* evidence/report. Do not write Python carrier files or any Go source.

**Interfaces:** Preserve the old build receipt required fields: environment, tool, commands, participating_inputs_before/after, module_versions, artifact, inputs_unchanged, module_files_before/after, participating_inputs_unchanged. Artifact fields path/bytes/sha256/elf_class/endianness/machine/mode/executed must remain accepted by F binding. Extra complete dependency/source evidence may be separate fields/files.

- [x] Read prior receipt command/field structure without copying prior results. Record new tool version/hash, module files, selected package input set and hashes.
- [x] Execute fresh offline version, list -deps -json, mod verify, build and version -m commands. Actual build argv is:

```python
[go, "build", "-p=2", "-trimpath", "-buildvcs=false", "-mod=readonly",
 "-ldflags=-buildid=", "-o", absolute_new_artifact,
 "./cmd/switchback-deployment-diagnostic"]
```

Use the fixed environment above, matching old flags. Capture full stdout/stderr/exit evidence; never execute the built ELF.

- [x] Discover inputs from actual Go list data. Keep compatibility worktree participating_inputs maps; also record/check full selected input and module membership including dependency/GOROOT evidence separately so no claimed input set is copied from old receipt. Before/after sets and hashes must match; include go.mod/go.sum, required fixed parser hash, and actual module version evidence.
- [x] Save exact current worktree build input snapshots under build-source and a manifest. Enforce ELF64 LE, machine183, <=8MiB and mode0400. Bind new artifact hash/size in receipt only after success; record executed=false.
- [x] Cleanup only owned /private/tmp cache/tmp. Check no Go/current index changes and fixed window absent. Write task-1-report.md with commands, inputs, hash/size, results and limitations.
- [x] Controller task review Spec/Quality validates actual build provenance, not native success.

### Task 2: Rebind F carrier and complete offline candidate

**Files:** Create the eight carrier sources under .scratch/runtime-m1-deployment-pid1-native-candidate-1021, assembly helper if useful, candidate/source-manifest/release, source-final snapshots, new fixture-provenance and preservation metadata, and task-2-* evidence/report. Controller baselines and Task1 artifacts are read-only inputs.

**Interfaces:** Consume new build-receipt.json artifact/inputs and controller baseline/git-visible-before/global-constraints/authorization/approved-design. Produce F-compatible bindings(io), verify and candidate schemas with new fixed paths/values; no new native API.

- [x] Copy F sources, replace mapper/tests with zero-aware fix-candidate sources. Keep a precise F→new diff, with mapper compared also against zero-fix source.
- [x] Rebind PHASE/WINDOW/stage/unit/cgroup/nonce, new ELF hash/size, build receipt path/hash and design/plan/document pins. Change build dependency to this phase; retain unchanged HISTORY verification and manager source. Only identity/build/fixture expectation changes are allowed.
- [x] Preserve the guard/state-machine/permission checks. An example necessary source change is:

```python
release["build_receipt"]["path"] == PHASE + "/build-receipt.json"
```

No old build fallback or runtime selector is added.

- [x] Generate source-manifest from eight final bytes; candidate from new guest LAUNCH/PROPERTIES, source manifest, approved docs, baseline and new artifact; release from exact candidate/source/build/fixture facts. Record native/guest/capability and three gates=false. New schemas remain existing v1 names.
- [x] Use the original frozen fixture provenance as historical inputs, update candidate references truthfully. Do not claim historical host/manager fixture data is a new observation.
- [x] Add regression tests before any relevant expectation change as applicable: old ELF/build/mapper/hash drift rejected before native IO; PID1 zero complete packet accepted, caller zero and 0→1 drift rejected; new identity and unknown path/nonce drift rejected. Use existing harness, no new framework.
- [x] Run complete new test_guest/test_host/test_output_mapper suites via -I -B, including existing all-mode fault/cancellation/output/once/local-FD matrices. Run readonly verify.py; resolve failures without weakening an admission check. Record intermediate failures accurately, then final GREEN.
- [x] Snapshot eight final sources/source hashes and exact binding values. Keep temporary-window tests under /private/tmp and real fixed window absent. Write task-2-report.md with precise changed logic/categories, test output and source hashes.
- [x] Controller task review Spec/Quality; no additional native authorization is implied.

## Controller closure

- [x] Independently run final full Python suite and readonly verifier on reviewed bytes; check existing Go fix source hashes and ELF header/mode without executing ELF.
- [x] Whole-change review assesses F→new diff, source/build/candidate/release closure, zero mapper and unchanged privilege/state-machine boundaries.
- [x] Verify all pre-existing Git-visible files and protected historical artifacts/metadata unchanged, HEAD/branch/index unchanged and fixed window absent. Remove owned temporary runtime/cache only.
- [x] Write operation-window.md with exact argv/output capture requirements, new fixed identities, actual hashes, total150s, no retry/resume/readback/Stop and honest cleanup/probe outcome interpretation.
- [x] Seal candidate/control artifacts and verify all bound bytes. Finish offline report. Only then request the concrete single real window if appropriate.

