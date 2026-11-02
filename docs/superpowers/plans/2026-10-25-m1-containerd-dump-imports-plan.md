# M1 Containerd Dump Imports Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute both tasks and reviews continuously after accepted A; no intermediate permission check.

**Goal:** Correct one fixed containerd2.4.1 dump representation mismatch and deliver a complete reviewed offline candidate.

**Architecture:** Retain empty imports in the actual configuration. Add a finite comparison at the current daemon_reload seam, bound to validated version and input bytes. Prove official loader behavior with private fixtures, then rebuild through the original artifact pipeline.

**Tech Stack:** Python3.9 for normal tests/build orchestration, Python3.14 stdlib tomllib for real captured TOML replay, pinned Go1.26.4 for candidate builds. Official containerd/v2 v2.4.1 requires Go1.26.6; its isolated loader experiment alone uses an explicitly materialized task-private Go1.26.6 toolchain. See phase `task-1-environment-resolution.md`; product toolchain/cache/module remain unchanged.

## Global Constraints

- Accepted design `docs/superpowers/specs/2026-10-25-m1-containerd-dump-imports-design.md`, grant `call_93ee8a19112c4acb9cd5baa834c44ccb`.
- Work only in `/path/to/switchback`; phase `.scratch/runtime-m1-containerd-dump-imports-1025`. All29886 prior sealed records, existing dirty paths and HEAD/index remain intact. No staging/commit/reset/clean/checkout/branch operations.
- No guest/Lima/lsof/Engine/PG/provider/VM invocation or live reads/writes under `~/.switchback`, `/opt/switchback` or current/archived runtime paths. Only WT saved receipts may supply environment facts. No built candidate executable may run.
- Only four Source leaves may change: `deploy/experience/engine/{guest.py,test_prepare.py,README.md}` and `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go`. The Go change only appends the new phase bundle-discovery glob. Config template/imports, builder/allowlists and root go.mod/go.sum stay unchanged.
- Actual imports stays `[]`. Keep original equal-output behavior. Only add compatibility for the exact single output list `['/etc/containerd/conf.d/*.toml']`, with empty validated input and exact containerd2.4.1 version. Any other imports, wrong type/version/nonempty input or compared-field drift rejects before daemon-reload.
- Preserve installed byte/package/binary pins, complete unit/daemon/listener/store checks, default masks, permissions/capabilities, network/resources, output256KiB only for two exact catalogues/otherwise16KiB, frame1MiB/audit64MiB,120/240/300second deadlines and PG/Trust/Start.
- The official-loader experiment uses unmodified v2.4.1 library code, task-owned private fixture/cache paths and relocated initial default imports. It runs no daemon/guest and reads no real /etc. An explicit-import positive control must demonstrate sentinel loading; empty input must demonstrate it is not loaded. A separate task module/dependency materialization cannot modify root module, builder inputs or artifact cache.
- Candidate compilation remains cached-pinned only: `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOMAXPROCS=4 CGO_ENABLED=0 GOENV=off GOWORK=off GOPATH=/Users/example/go GOMODCACHE=/Users/example/go/pkg/mod GOCACHE=/private/tmp/switchback-qualification-1022-go-cache PYTHONDONTWRITEBYTECODE=1 TMPDIR=/private/tmp/ HOME=/Users/example`.
- Python unittest TMPDIR must be the task-owned real0700 `task-1/test-tmp`, not /private/tmp. Preserve original raw stdout/stderr, argv/env/cwd/exit and hashes for every executed validation/build/tool probe; never replace failed receipts or reconstruct missing bytes.
- Root owns plan/status/ledger/authorization/freezes/final seal. Agents own only their task outputs and explicitly allowed Source. No tests repeatedly after unchanged-source pass unless new evidence requires it.

### Task 1: finite dump validation and official-loader proof (complete; reviewed)

**Files:** Modify four allowed Source leaves. Create task-1 outputs and `task-1-report.md` in this phase. Keep helper inside existing guest.py, tests inside existing test_prepare.py.

**Consumes:** phase before snapshots/authorization and captured `runtime-m1-directory-prerequisite-execution-1025/{engine-guest-frames.json,replay_config.py,config-replay.json}`; original4762-byte stdout hash918541f7a970f4ac23c2084e2ef49601f34e5f7a32e42c28f5dd406984d72b64.

**Produces:** `_validate_containerd_dump(effective:dict, expected_raw:bytes, binary_version:str)->None` or an equivalently finite existing-style function, called at the original daemon_reload seam using the already validated preflight containerd binary record; raw RED/GREEN, official loader proof, full validation receipts and source report.

- [x] Read exact current caller/contract/tests via CodeGraph and current files. Verify all protected inputs before editing.
- [x] Add regression before production change at original `_GuestOperations.call('daemon_reload', deadline, record)` using saved raw dump and a strict IO recorder: exact dockerd validate + containerd dump argv; assert no systemctl call on rejection. Use stdlib tomllib for the real replay. Python3.9 tests may use a typed captured fixture whose provenance is verified against the real parser by the Python3.14 replay; synthetic mutations must be labeled separately.
- [x] Record RED reproducing the exact existing mismatch while equal-output controls pass.
- [x] Implement finite comparison. Reference intent:

```python
def _validate_containerd_dump(effective, expected_raw, binary_version):
    expected = _fixed_toml(expected_raw)
    _require(binary_version == '2.4.1' and expected.get('imports') == [], 'fixed containerd imports/version')
    imports = effective.get('imports')
    _require(type(imports) is list and imports in ([], ['/etc/containerd/conf.d/*.toml']), 'containerd dump imports')
    _require(all(effective.get(key) == wanted for key, wanted in expected.items()
                 if key not in ('plugins', 'imports')) and
        all(all(effective.get('plugins', {}).get(plugin, {}).get(field) == wanted
                for field, wanted in values.items()) for plugin, values in expected['plugins'].items()),
        'installed containerd config parser effective values differ')
```

Keep strict input validation and installed-byte/version provenance; adapt the reference if a concrete test/risk reveals insufficient type or caller validation. Never introduce broad imports skipping.
- [x] Regression negatives: wrong version/nonempty or malformed input imports; extra/duplicate/different/non-list output imports; root/state/temp; grpc/ttrpc/shim socket; nonempty TCP/debug/metrics address; disabled/required lists; shim env. Test original caller stops before systemctl on each invalid observation and reaches the existing subsequent call for valid output. Do not weaken unrelated parser or return types.
- [x] Compile/run an isolated official v2.4.1 `LoadConfigWithPlugins` harness from a task-owned Go module. Set initial outConfig Imports to a private fixture glob. Input imports=[] must retain the displayed initial list without reading sentinel; explicit imports=[fixtureGlob] must apply sentinel root or another decisive value. Record module version/sums and dependency provenance, real raw command receipts, exact fixture reads/paths. Source/dependency fetching, if needed, is confined to task cache and public pinned modules; no installed containerd execution or service config access. Report blockers to Root rather than substitute copied or modeled loader behavior.
- [x] Document representation semantics and source URLs in existing README; append only `.scratch/runtime-m1-containerd-dump-imports-1025/task-2/bundle-*/manifest.json` to Go discovery.
- [x] Run focused GREEN, full Engine once, qualification window and selected Go gate, selfcheck:

```text
/usr/bin/python3 -B -m unittest discover -s deploy/experience/engine -p 'test_*.py'
/usr/bin/python3 -B -m unittest discover -s test/acceptance/experienceprepare -p test_qualification_window.py -v
/usr/bin/python3 -B deploy/experience/engine/prepare.py self-check
/usr/local/go/bin/go test -p 2 ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualification.*(Engine|Process|Window)' -count=1 -v
```

- [x] Full report includes actual/synthetic boundaries, red/green, loader positive/negative control results, source-after copies and before-relative diff. Stop edits after handoff; no commit. Root generates review package; reviewer gives spec and quality verdicts. Resolve Critical/Important and every cross-task warning before Task2.

### Task 2: complete new kit/bundle and installed-state preservation plan

**Files:** Task-2 outputs and `task-2-report.md` only; one fresh kit under original builder output `.scratch/runtime-m1-qualification-delivery-1022/task-4/`; no Source writes.

**Consumes:** accepted Task1 source freeze/review; unchanged `build_candidate.py build` and `build_bundle(kit:Path,destination:Path)`/`verify_bundle(bundle,kit)`; saved actual/preparation/copy receipts.

**Produces:** complete new kit, canonical bundle, reverse/source/template binding audits and final Go PASS/noSKIP; full private-preservation/checklist artifacts explicitly requiring later live authorization.

- [x] Preflight Source/template inputs, expected artifact layout, script syntax, saved-state schemas and all generation before invoking original builder once. Original complete Task2 pipeline under `.scratch/runtime-m1-unit-array-property-1025/task-2/` is a read-only reference; inspect actual filenames and interfaces, do not guess stale paths.
- [x] Invoke original builder exactly once with fixed cached build profile. Do not delete or retry failed output without Root adjudication.

```text
/usr/bin/python3 -B deploy/experience/qualification/build_candidate.py build
```

- [x] Canonical complete bundle, no hand edits:

```python
from bundle import build_bundle, verify_bundle
result = build_bundle(actual_new_kit, phase_task2 / 'bundle-0001')
verified = verify_bundle(phase_task2 / 'bundle-0001', actual_new_kit)
```

- [x] Independently verify all exact path/type/mode/bytes/hash coverage, source/template record ordering and current-before-after roots,20 binary formats/build-info, every raw receipt,17 reverse inputs,13runtime null/4authority false. Derive counts; prior shape1076Source/7Templates/1152kit leaves/18bundle leaves/43exit0 receipts is a check, not fabricated output. Run `TestQualificationEngineDeploymentFinalKitBinding` on actual new candidate; PASS/noSKIP/runtime reads zero required. No binary executes.
- [x] Preservation plan from WT saved evidence: current four kit17 identities from `runtime-m1-unit-array-property-execution-1025/copy-switch-verification.json`; seven archives in `runtime-m1-directory-prerequisite-execution-1025/saved-layout.json` and its common frozen map; separately retained3372leaf stage and current4858leaf stage from new actual receipts. Bind original boot/claim/grants/outcomes without presenting them as live state.
- [x] Include all newly installed guest directories and four files from receipt known_effects/partial_installed_observations; derive saved directory identities only where recorded, explicitly mark unobserved identities. Future recovery must first read-only prove identity/ownership/no running writers/boot, retain exact installed objects with no-replace, preserve all archives/stages and establish fresh exclusive paths. No direct replay over existing targets, speculative deletion/reset, hidden chmod/chown or remoteStop. Plan candidate is not execution authorization.
- [x] Future full operation specifies exact new manifests/roots, unchanged120s Engine and conditional240s qualification/300s barrier, unchanged capability/resource/network/gates, VM alreadyRunning and fail/unknown stop without retry/resume/postfailure guest check/provider/partition/VMstart. Concrete live ownership gaps block future execution until a fully reviewed scope resolves them.
- [x] Freeze delivery files/directories plus external kit, self-review and complete report. Root reviews spec/quality, resolves findings, performs whole-slice most-capable review and final complete consistency/seal. No Source/commit/live actions.

## Root closeout

- [x] Protect task Source/delivery handoffs and all prior sealed inputs; generate task-scoped before-relative diffs because no commits are authorized.
- [x] Whole-slice review is limited to this accepted slice; historic dirty work is context, not a new review target. Reviewer receives current before-relative package, reports, freezes and explicit scope.
- [x] Update critical path and progress from actual verified outputs, retaining runtime pending status. Seal new phase and verify exact coverage/hash without editing any sealed artifact. Report code, tests, artifacts and remaining true-runtime boundary concisely.
