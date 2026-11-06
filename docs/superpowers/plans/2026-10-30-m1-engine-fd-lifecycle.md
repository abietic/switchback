# M1 Engine FD Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复正式 Python/Go collector 的 FD 生命周期竞争，保留严格完整样本条件，交付完整离线 kit/bundle、回归、独立复审与封存。

**Architecture:** 在内部 FD 元数据采集边界产生有限 typed race；保持目录与 O_PATH 对象 handle、绑定原 fdinfo 的 mount/inode/flags，并在源条目前后核对对象。外层最多两次完整只读 collection，共用 deadline，受保护 anchor 不变；所有安全拒绝和记录/清理错误直接终止。公开 listener/receipt/store wire 不变。

**Tech Stack:** Python stdlib/unittest，Go 1.26.4，golang.org/x/sys v0.47.0，Linux O_PATH/procfs；Darwin 只执行纯离线 fixtures，Linux binaries 只编译。

## Global Constraints

- 每次 listener/native collection 固定最多两次 attempt，共用原来的 deadline 和全部 byte/frame/process/FD 上限，不 sleep、不增加预算。
- 一次 service start、Engine API 请求、NetworkCreate、daemon-reload、资格/安装/Start 窗口不因该机制重复。
- 权限错误、unsupported syscall、非法编码/类型、外来 daemon、data-root writer、隐藏或外来 listener、protected anchor 漂移、超时、资源/输出/记录/清理失败都直接终止，不归入可重采样类别。
- 已经成立的安全拒绝保持为本次 collection 的终止事实，不能被后续取样冲掉。
- partial 结果、第一轮 prefix、旧真实回执都不参与第二轮成功判定。
- 原 FD flags 保持来自原 fdinfo；`flags`、`mnt_id`、`ino` 均需唯一且严格解码；没有字段不降级。
- O_PATH handle 只做元数据观察并及时关闭；不 read/write/seek 原 FD，不复制其 open-file-description，不调用 SET 操作，不冻结/停止目标进程，不增加 capability。
- 保留现有 listeners v1/v2、deployment receipt v1/v2、store inventory v1—v4 的 wire 格式与合法成功事实语义。
- 本轮不进入 guest、不操作现行副本或 service、不读取 credential、不调用模型；不构建 Docker 镜像，不启动 PG、VM、Engine、Worker 或 provider。
- 当前工作树已有 116 项 tracked 未提交改动，按文件前像保护，不提交或改写 Git index。
- 新生产行为、worker-delivery 配置、quota/容量/Trust/PG/Start 资源数值与权限保持。
- 旧 142 项执行证据与 1,390 项离线文件保持 byte/mode 不变。

## Fixed inputs and coordination

Workdir: `/path/to/switchback`.

Spec: `docs/superpowers/specs/2026-10-30-m1-engine-fd-lifecycle-design.md`, accepted by `call_3b90bab795924d1db4d79b9abd80e8d0`.

New private phase: `.scratch/runtime-m1-fd-lifecycle-1030` (PHASE below). Baseline and complete Source/templates preimages are under `PHASE/ROOT-baseline.json` and `PHASE/before/source/`. The accepted spec bytes are `PHASE/approved-design.md`; its original draft status line does not override the later recorded human acceptance.

Captured failed execution: `.scratch/runtime-m1-all-preserved-checkpoint-execution-1029-01`, seal `723252d7f444e1f73fee0b5f3504536f7f181a2cf989a28c59d6b664a1541845`. Raw Engine outcome `carrier/engine-local-outcome.raw.json`: 37,533,587 bytes, SHA256 `2386ae22abdd7c25ab523a02a52cee864914e010dbc0b18b24d1b2f25c411ca7`. Its 45,194 observations have `_encode(list)` SHA256 `e3003aa1724666f300bfd27275d169a46a54fb1aaac8865c80d2fb73b52c416f`. The old Source guest SHA256 is `d83e419b947e657325b093d44f1ef436068afed0dc1bfbbe5a27638172b564cd`.

No agent reads, stats, enumerates or follows the canonical key directory `.scratch/runtime-m1-worker-execution-credential-1026-01` or the old selection's host_source. Public profile/reference JSON can be read without following credential references.

Source changes invalidate old live eligibility by design; they do not authorize rewriting old seals or receipts. Legacy RED runs load the immutable Source preimage, not the changing live checkout. New continuation inputs are explicitly synthetic because no successful post-failure Linux sample exists.

Per-task gates use a full diff against the task's exact byte preimage, not Git HEAD (which contains older unrelated WIP). No Git commits are produced. A fresh task reviewer returns both spec and quality verdicts; a final most-capable reviewer examines all three tasks.

### Task 1: Python FD sampling and whole-collection restart

**Files:**
- Create: `deploy/experience/engine/fd_lifecycle.py`.
- Create: `deploy/experience/engine/test_fd_lifecycle.py`.
- Create: `deploy/experience/engine/testdata/fd-lifecycle-corpus.json`.
- Modify: `deploy/experience/engine/guest.py`, `prepare.py`, `bundle.py`; `contract.py` only for factoring the existing hard-negative listener checks for reuse on valid partial observations, with public full validation unchanged.
- Modify as needed for exact integration: `deploy/experience/engine/test_guest.py`, `test_listener_v2.py`, `test_prepare.py`, `test_contract.py`.
- Evidence/report: `PHASE/task-1/` and `PHASE/task-1-report.md` only.

**Interfaces:**
- `fd_lifecycle.py` is a small stdlib-only deep module: strict fdinfo decoder, pinned filesystem-object sampling, typed FD sampling race, bounded two-attempt collection support. No import-time I/O, runtime CLI/env adapter selection, Source dependencies or service operations.
- Keep `_IO.processes()` and `_collect_listeners(io_ops, phase, binary_hashes)` caller signatures. The latter is the retry boundary; `_collect_listeners_once` does one fresh full attempt. Do not retry individual metadata reads to stitch a sample.
- A race carries exact PID, FD number, primitive, original error and first valid protected anchor. Recorder/cleanup errors are never a race. Anchor fields are boot, daemon identity/hash/cgroup and protected sockets/parent metadata/kernel inode; a missing anchor forbids attempt 2.
- Shared corpus schema `switchback.engine.fd_lifecycle.corpus.v1`; cases expose `id`, raw fdinfo/pin/source facts or primitive errors, and expected `stable|sampling_race|hard_failure`. Document exact final case interface in the task report for Go. Production paths cannot load this fixture.
- Emitted private FD facts include the original FD flags and mount identity. Update synthetic test inventories explicitly where the stronger internal facts require it; do not rewrite original real packets or public wire fixtures to pretend missing data was observed.
- A typed race may carry already observed valid process/FD facts. Before retry, latch hard daemon/writer/listener negatives from these facts. Reuse one policy implementation for complete and partial negative checks; a partial scan may prove a violation, never prove successful coverage. Keep the `_IO.processes()` signature by carrying its partial observations on the typed error rather than adding a public policy selector.

- [x] **1. Save focused REDs and extracted fixture.** Verify every frame's bytes/hash and original stream digest before extracting PID1 stat/cgroup/cmdline/fdinfo. Load old `guest.py` with its old dependency modules from `PHASE/before/source/deploy/experience/engine`. Keep missing FD RED and stable counterfactual control separate. Capture the command, exit and stdout/stderr in task-1.

```python
raw = base64.b64decode(row['raw']['base64'], validate=True)
assert len(raw) == row['raw']['bytes']
assert 'sha256:' + hashlib.sha256(raw).hexdigest() == row['raw']['sha256']
# After decoding all observation frames in original order:
assert hashlib.sha256(json.dumps(observations, ensure_ascii=False,
    separators=(',', ':'), allow_nan=False).encode()).hexdigest() == EXPECTED_STREAM
```

- [x] **2. Add tests at the real helper/caller seam before implementation.** Force FD removal at pin/open, source readlink, source fdinfo and source stat; test object reuse across inode/mount/socket, own pin errors, source CLOEXEC distinct from pin, self-enumeration and every close boundary. Drive the real `_IO.processes` and the real `_collect_listeners` through injected metadata primitives, not a stub that returns a completed inventory. Focused RED must name the missing behavior, not only fail imports.

```python
def test_closing_fd_requires_a_second_complete_collection(self):
    io_ops = self.real_collector_fixture(close_once_at='source-fdinfo')
    result = guest._collect_listeners(io_ops, 'engine_ready', self.binary_hashes)
    self.assertEqual(io_ops.collection_attempts, [1, 2])
    self.assertEqual(result['complete'], dict.fromkeys(
        ('unix', 'tcp', 'tcp6', 'fd_owners', 'processes'), True))
    self.assertEqual(io_ops.side_effect_calls, [])
```

- [x] **3. Implement coherent FD acquisition.** Enumerate all numeric names under a still-open FD directory before opening per-FD pins. For each source slot use O_PATH|O_CLOEXEC, fstat and read its pin target/mount metadata; read original fdinfo preserving complete flags; bracket with source target/object checks and match inode/mount. Require strict unique flags/mnt_id/ino. Do not infer source flags from the pin. A source ENOENT is eligible only at an identified primitive and after fresh process identity (PID/start/flags/exe/cgroup, existing R/S rule) succeeds. Own pin, parser, observer and finalization failures are hard errors. Always attempt close; preserve primary plus all close failures.

- [x] **4. Implement the two-attempt boundary and anchor.** Split the existing single full collector body without weakening its successful predicate. Check deadline before each attempt. Persist start/error/success using the original recorder; record failures remain terminal. Before classifying a whole-coverage change as a race, validate both complete samples' daemon/writer/listener policies and identical protected anchor. A primary policy failure remains latched; later races cannot erase it.

```python
anchor = None
attempts = []
for number in (1, 2):
    io_ops.check()
    record_attempt_start(number)
    try:
        result = collect_once(number, anchor)
    except FDSamplingRace as race:
        anchor = require_same_complete_anchor(anchor, race.anchor)
        record_attempt_failure(number, race)
        attempts.append(race)
        if number == 2:
            raise SamplingExhausted(attempts) from race
    else:
        record_attempt_success(number)
        return result
```

The helper names in this code are implemented as private task-local functions; exact exported/private interfaces used by guest and fixtures must be listed in the report. The real implementation must reject cleanup-tainted races before reaching this loop's retry branch.

- [x] **5. Wire all required sources.** Add `fd_lifecycle.py` to `bundle._ENGINE_LEAVES` and `_REQUIRED`, test/corpus to the permitted full tree, and the `prepare._self_check` code compilation list. Confirm installed `guest.py` imports the helper from the verified bundle directory. Capture tests rejecting a bundle missing the helper or with a same-byte/path/member mismatch.
- [x] **6. Run focused GREEN then one whole engine suite.** Commands: `python3 -B -m unittest discover -s deploy/experience/engine -p test_fd_lifecycle.py -v`, then `python3 -B -m unittest discover -s deploy/experience/engine`. Require output pristine, no new skips; retain current shared listener/store/legacy behavior. Write report with exact final interfaces, files, RED/GREEN and full-suite commands/results, no commits.

### Task 2: Go native collector parity and portable primitive seams

**Files:**
- Create: `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_fd_lifecycle.go` (portable policy/seams).
- Create: `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_fd_lifecycle_linux.go` (fixed Linux primitives).
- Create: `internal/workerruntime/adapter/capacitysnapshot/qualification_engine_fd_lifecycle_test.go`.
- Modify: `qualification_engine_deployment_linux.go`, `qualification_engine_deployment_v2.go`, `qualification_engine_deployment_readback.go` in that package.
- Modify tests only where needed: `qualification_engine_deployment_v2_test.go`, `qualification_engine_deployment_test.go`.
- Evidence/report: `PHASE/task-2/` and `PHASE/task-2-report.md`.

**Interfaces:**
- Consume Task1's exact shared corpus and accepted helper/caller observations; do not edit Task1-owned code/corpus without coordination.
- Keep the production entry `qualificationEngineCollectNative(ctx, receipt)`. Split one attempt into `qualificationEngineCollectNativeOnce` with a private metadata operations seam; only fixed Linux ops are selected in production. No public constructor/env/CLI switches.
- Portable interfaces cover enumerate, source/pin readlink, fdinfo, fstat, open O_PATH and close, process identity and deadline. Return the existing process/native facts (private mount/attempt diagnostics may be added; no public wire change).
- Native attempt anchor includes Boot, Filesystem, RuntimeParent, Daemons, Sockets, SocketParents. Store at most 2 internal attempts with `json:"-"` (or equivalent explicit exclusion), and attach attempts to terminal errors. Partial Native never has Complete=true and never authorizes requests.

- [x] **1. Add corpus and real-call REDs before implementation.** Exercise the portable sampler used by the real Linux adapter, not a second copy of the algorithm. Cover every Task1 corpus case and equivalent errors/outcomes. Use the captured raw fdinfo/stat rows from Task1; preserve original CLOEXEC. Add collector tests showing fresh full attempt 2, immutable anchor, second race exhaustion and no request/effect replay.

```go
func TestQualificationEngineFDTwoAttempts(t *testing.T) {
    f := newEngineFDCollectionFixture(t)
    f.CloseOnceAt("source-fdinfo")
    got, err := f.Collect(context.Background())
    if err != nil || !got.Complete { t.Fatalf("complete fresh sample: %v", err) }
    if !reflect.DeepEqual(f.Attempts(), []int{1, 2}) { t.Fatal("attempt bound") }
    if len(f.SideEffects()) != 0 { t.Fatal("collection repeated an effect") }
}
```

- [x] **2. Implement one portable sampling algorithm and strict decoder.** Keep existing flags-only legacy helper behavior where independently used; add a strict fdinfo tuple decoder requiring unique flags/mnt_id/ino and matching pin. Use a typed race only for the same identified source primitives and stable PID identity as Python. Error wrapping preserves syscall cause. Close failures poison retry eligibility; never mask primary failure.
- [x] **3. Bind Linux operations.** Use unix.Openat/O_PATH|O_CLOEXEC against the held FD directory and fixed `/proc/self` metadata only. Do not use PidfdGetfd, read target content, Setflags, ptrace, signals, service lifecycle or any new capability. Preserve original directory handle through name enumeration and sampling. Linux own-pins are closed before moving to the next FD; all metadata and close calls are guarded by the original context.
- [x] **4. Wire complete native collection.** Retain the existing full raw table/process coverage and both snapshot policies. Reclassify only proven acquisition/coverage races after hard policy checks; protect the original complete anchor across attempts. Keep all receipt/listener v1/v2/store v1—v4 decoders and supplied public corpus unchanged. Maintain finite internal attempts for both success-after-race and terminal failure.
- [x] **5. Run focused GREEN, one package suite and compile the Linux path.** Use `GOCACHE=/private/tmp/sb-fd-lifecycle-gocache-1030`, unset `SWITCHBACK_TEST_DATABASE_URL`, `GOPROXY=off`, `GOSUMDB=off`, `GOTOOLCHAIN=local`. Commands: focused `go test ./internal/workerruntime/adapter/capacitysnapshot -run 'TestQualificationEngineFD' -count=1`; whole same package once; `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -o PHASE/task-2/capacitysnapshot-linux-arm64.test ./internal/workerruntime/adapter/capacitysnapshot`. Never execute the ELF. Record shared case count/results, actual production binding, full command outputs and limitations; no commits.

### Task 3: Full-scale replay, fresh complete candidate and binding

**Files:**
- Create only under `PHASE/task-3/` plus `PHASE/task-3-report.md`.
- Source changes require a concrete integration defect, full covering tests and a preserved preimage; coordinate before modifying Task1/2 accepted files. Do not change templates or public policy to make the build pass.

**Interfaces:**
- Consume accepted Task1/2 Source and immutable reports/reviews. Use the original `deploy/experience/qualification/build_candidate.py` module with only its private `PHASE` output destination bound to a fresh private pool, keep ROOT/inputs/build commands/ENV semantics.
- Full new qualification kit under `PHASE/task-3/candidates/kit-0001`; new `bundle-0001` under the same new phase. IDs are namespaced by this phase; they do not overwrite or claim identity of any historic kit/bundle.
- Bundle from original `build_bundle(kit, destination)`. Exactly current Source/templates, original 20 build artifacts/receipts, unresolved_runtime all null. Carry unchanged worker_delivery public profile separately without opening or following a credential source.
- Evidence differentiates exact 45,194 observed frames from synthetic continuation needed after the real failure. Current native Linux behavior remains compile-only on Darwin. No actual execution carrier/grant is created.

- [x] **1. Verify accepted Source freeze.** Build `builder.inputs()` before/after tests/build, compare exact membership and each byte/mode. Every new helper/test/corpus member must appear where expected; engine whitelist must cover the actual copied producer set. Verify old sealed file bytes/modes using the copied seal manifests, not an old full verifier that requires current Source or future absence.
- [x] **2. Run true scale replay and budgets.** Validate all original raw frame lengths/hashes and `_encode(observations)` digest, route an actual captured PID1 FD close through the new real collector seam and a fresh complete synthetic second attempt. Execute actual per-FD parser/pin/call/loop checks and recorder accounting; do not stub the final success predicate. Save attempt anchors, discarded prefix, final accepted sample, counts/bytes/frame sizes, elapsed times and deadline checks. Include persistent churn/recorder limit failure as negative controls. Keep original 120-second Engine/240-second qualification/300-second barrier and existing byte limits.
- [x] **3. Build the full fresh kit.** Import the verified builder without invoking its CLI, set `builder.PHASE` to `PHASE/task-3/candidates` (created 0700), invoke `builder.build()` once; it chooses the first empty `kit-0001`. Keep partial output on failure. Original Go 1.26.4, no network, CGO=0, Linux arm64/amd64 plus 32-bit ioctl compat and Darwin producer artifacts. Parse all 20 raw Go build-info receipts against actual ELF/Mach-O headers and their exact copied Source/module/flags/arch; a command exit 0 alone is insufficient.

```python
spec = importlib.util.spec_from_file_location('verified_fd_builder', ROOT / 'deploy/experience/qualification/build_candidate.py')
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)
builder.PHASE = PHASE / 'task-3/candidates'
builder.PHASE.mkdir(mode=0o700, parents=True, exist_ok=False)
builder.build()  # fixed original commands and environment; no Docker/provider
```

- [x] **4. Build and independently verify the bundle.** Load original contract/bundle via fixed dependency injection or a Source-only PYTHONPATH; call `build_bundle(new_kit,new_bundle)` and `verify_bundle(new_bundle,new_kit)`. Verify full physical file/directory closure, no links/specials/extra leaves, manifests and modes, new Source and template roots. Verify the kit's actual helper/guest imports and every new data member, not only a whitelist string.
- [x] **5. Run final integration and write review-ready artifacts.** Use current actual Source and exact candidate paths for Python/Go public contract parity and source-root/bundle binding. Save raw commands/exits/stdout/stderr and a machine-readable candidate record with immutable hashes, Source membership, all 20 binaries, offline-only flags, disabled defaults and no fabricated runtime identity. Include current live-state boundary: kit25 copies/Engine were installed, network step unknown; old all-preserved grant is consumed. Write delivery notes, full report, freeze and diff package. Do not assert real acceptance or construct a run-ready maintenance claim.

## Root gates and completion

- [x] Capture accepted spec, full Source/templates preimages, HEAD/index/tracked baseline and both old seal files before source mutation.
- [x] Generate unique task briefs and fixed before/after diff files from byte preimages; dispatch a fresh implementer then a fresh task reviewer for each Task. Implementers own only listed paths and their task evidence; no parallel implementers.
- [x] Resolve every Critical/Important finding with a fresh fixer and covering tests, then re-review. Record Minor items and all concrete ⚠️ checks for the whole review.
- [x] Dispatch one most-capable available final reviewer with the full changed-file diff, all task reports/gates, accepted spec, actual source/binary/candidate records and raw verification evidence. No pre-rated or suppressed findings, no unnecessary suite repetition.
- [x] Verify allowed Source delta only, unchanged templates/HEAD/index, preserved unrelated files and old sealed byte/mode closure. Verify candidate from its bytes once after final Source freeze.
- [x] Seal the new full offline phase including candidate and reports; read-only verify. No writes after seal. Final reply gives actual changes, bounded offline evidence, limitations and artifact links. No new real execution or credential/model permission is inferred.
