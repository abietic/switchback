# M1 FD candidate owned transition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver one fully assembled, verified and sealed offline carrier for the accepted FD lifecycle candidate, current kit25 ownership and a new preservation generation.

**Architecture:** Reuse the immutable final FD kit/bundle and the existing strict owned-maintenance sequence. Separate recreated current objects from retained archives, project Source FD facts through an exact private adapter, and bind every actual module/program/copy/continuation producer. The carrier remains disabled until a later exact authorization.

**Tech Stack:** Python stdlib, existing bounded framed transport, immutable Go/Linux-arm64 artifacts, repository-local captured evidence.

## Global Constraints

- Workdir `/path/to/switchback`; new implementation phase `.scratch/runtime-m1-fd-lifecycle-transition-1030` (P below).
- Accepted design: `docs/superpowers/specs/2026-10-30-m1-fd-lifecycle-owned-transition-design.md`, SHA256 `ec405729b792f9d6c1c82bfec5521fc0771f239592fc6d4c1833146e569fefff`; explicit acceptance call `call_080055a5f8fc4daeb62e8a17b81b6d43`.
- No Source/Go/template changes or binary rebuilds. Reuse `.scratch/runtime-m1-fd-lifecycle-1030/closing-owner-delivery/candidates/kit-0001` and `bundle-0001`. Kit manifest `a0efe562f7374bf104051b7cda706ed0bb621900a768a85ebfd46f4666566eb7`; bundle manifest `61bc3b24ff1a356d0fd7843af9982d7b68b9b467aa1dca7e653ac3b215154ed3`; Source `sha256:81fac0053a7cbf98a4288ed55b3af7daaddc9f87f710983d430aef6b99ac2bce`; templates `sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`.
- No guest, VM/Engine/PG/Worker/provider/service activity, live host deployment-tree reads or mutations, credential reads, actual authorization/claim, Git index/HEAD mutations. In particular never enumerate/read `.scratch/runtime-m1-worker-execution-credential-1026-01`, `/Volumes/SwitchbackCore`, or `~/.switchback` during this delivery.
- All writes stay inside P, this plan, or append-only `.superpowers/sdd/progress.md`. All old phases, candidate trees, design bytes and uncommitted changes are immutable. Preserve task preimages and review exact private diffs; no commit/stage despite generic skill defaults.
- New host/guest-opt archive `archive-owned-engine-1030-kit25-to-fd`; other guest parents use `.preserve-owned-engine-1030-kit25-to-fd`. Exact eight guest source roles from the design; current seven nonempty roles plus optional independent API socket. Targets must be absent. Old archives and all retained stages remain fully checked in place.
- Historical boot `751079a6-74a4-4570-bca6-18e460c7f33d`; containerd PID/start `78372/141400509`, Engine `78390/141400598`; historical identities constrain future fresh proofs, never current liveness. New instance/boot is refused. Naturally quiet/containerd-only state requires the original full proof.
- Input FD schema exactly `fd,target,flags,device,inode,mount_id,socket_inode`; old private schema exactly `fd,target,flags,device,inode,socket_inode`. No validator weakening, numeric coercion, defaulted mount ID or new maintenance retry. Mount ID is uint64 including zero, and raw seven-field facts plus mapper input/output/source/helper digests remain audited.
- Source load order includes `contract/prepare/store/binaries/fd_lifecycle/guest` and all actual private dependencies. Source bytes must match the sealed candidate. The formal Source two-sample retry is metadata-only; Stop/API/NetworkCreate stay single-attempt.
- Future real sequence: fresh full ownership proof; Stop proven Engine then containerd at most once each; quiet; exact preservation; reload once; unchanged strict-empty; four exclusive new copies; Engine once; only on success existing Native double-capability qualification, selected private DeepSeek reference, images/new secrets/new PG/migrations and conditional Core boot. No Task/model calls.
- Outer preparation 7200 seconds; maintenance work100 + local cleanup20 =120 seconds; Engine120/qualification240/barrier300. Each actual argv <65536 bytes; frame16384/event1048576; decoded128MiB/wire192MiB/journal192MiB/ordinary64MiB/failure1MiB. Existing NODE/file/package/decompression/Source caps remain.
- First failure/unknown stops permanently: no retry/resume/rollback/post-failure guest readback or Stop. Same-process one-use continuation witness is required; old disk receipts grant nothing.
- No invented current root/inode/static stage manifests. Missing historical identity is explicitly first-observed by complete bounded double samples in a future same window. Full original recorded stream and complete old archive obligations must be charged; synthetic future states must be labeled and cannot stub final success.
- Disabled authorization has unset human/id/time/worker and environment/provider=false; no actual claim. Final runtime authorization only after complete reviewed/sealed candidate and concrete operation checklist.

## File Structure and Evidence

P contains `carrier/` copied from immutable seed `.scratch/runtime-m1-all-preserved-checkpoint-1029/carrier`, `before/carrier/` byte preimages, scope/baseline and a progress ledger. Do not dump multi-megabyte artifact-binding or host-policy JSONs: parse selected keys.

Immutable design input: `.scratch/runtime-m1-fd-lifecycle-transition-design-1030/evidence-basis.json` (SHA256 `937f0d798c655521ea3783ec4fdb4ce5af884ce4dd1b6350afb3ae48d9480937`). Last original packet `.scratch/runtime-m1-all-preserved-checkpoint-execution-1029-01/carrier/engine-local-outcome.raw.json` has 37533587 bytes, SHA256 `2386ae22abdd7c25ab523a02a52cee864914e010dbc0b18b24d1b2f25c411ca7`, 45196 frames/45194 observations; stream digest `e3003aa1724666f300bfd27275d169a46a54fb1aaac8865c80d2fb73b52c416f`. Last pre-validation inventory index43951 is not an accepted success proof.

Task boundaries: Task1 produces independently tested closed adapter/layout contracts; Task2 consumes them to build a complete executable disabled carrier; Task3 validates the actual full artifact and captured scale, then closes review/seal. ROOT protects baseline/Source/index at each gate. Implementers write exact reports; task and final reviewers read task diffs without repeating passed tests.

### Task 1: Exact FD projection and fixed historical preservation contract

**Files:**
- Create `P/carrier/fd_projection.py`, `P/carrier/transition_layout.py`, `P/carrier/transition-basis.json`.
- Create `P/transition_fixtures.py`, `P/test_fd_projection.py`, `P/test_transition_layout.py`, `P/task-1-report.md`.

**Interfaces:**
- `fd_projection.project_fd(row, source_hash, helper_hash) -> (private_row, audit_event)` validates exact Source schema, returns unchanged six values and canonical raw/result digest evidence with source/helper hashes. `project_process_fds(rows, source_hash, helper_hash) -> (rows, events)` handles complete process rows without mutating input; strict rejection propagates.
- `transition_layout.validate_basis(value) -> dict` validates a fixed closed history contract; `compile_layout(evidence_basis, retained_layout) -> dict` separates current sources/new targets/old archive obligations and identifies first-observed fields; `validate_layout(layout) -> dict` rejects dynamic paths, missing obligations, stale mode/new instance adoption. Constants `MODE='owned_fd_lifecycle_transition_v1'`, `ARCHIVE`, `HIDDEN`, `SOURCES` name only the accepted fixed roles.
- Task2 may add narrowly scoped integration functions in these files with covering tests, retaining these interfaces. Layout compilation must derive data from bound original inputs, not ad hoc guessed values.

- [x] Step1: Write focused rejection and success tests before implementing. Minimum mapper vectors:

```python
source = dict(fd=8, target='socket:[585236]', flags=0o2000002,
              device=10, inode=585236, mount_id=0, socket_inode=585236)
private, event = project_fd(source, SOURCE_HASH, HELPER_HASH)
assert private == {k: source[k] for k in ('fd','target','flags','device','inode','socket_inode')}
assert event['raw'] == source
assert source['mount_id'] == 0
for bad in (True, -1, 1 << 64):
    changed = dict(source, mount_id=bad)
    with self.assertRaises(ValueError): project_fd(changed, SOURCE_HASH, HELPER_HASH)
```

Test uint64 maximum, missing/extra fields, bool/integer/string/target/socket constraints matching current Source, unchanged flags, digest tampering, ordering and input nonmutation. Layout tests require exact new names, old archive/current coexistence, no stale quiet-only mode, optional socket, occupied target/new PID/boot/missing obligation rejection, explicit first-observed fields instead of fabricated identities.

- [x] Step2: Run `python3 -B -m unittest discover -s P -p 'test_fd_projection.py' -v` and `... -p 'test_transition_layout.py' -v`; preserve expected RED output in task1 evidence.
- [x] Step3: Implement pure closed projection/layout modules and derive transition-basis.json from the fixed design input plus independently hashed original packet/candidate/retained policy. Canonical digests use sorted compact JSON with allow_nan=False. An audit event retains both raw and private values and their hashes. Never read credential or live deployment paths.
- [x] Step4: Re-run focused tests to GREEN, verify four installation files are byte-identical between new and kit25, original packet/frame/stream hashes and historical PID/socket facts; save bounded report with exact commands/results. No runtime acceptance claim.
- [x] Step5: ROOT creates a private diff package against before/seed and reviews Task1 spec + quality with a fresh reviewer; fix Critical/Important findings via a fresh implementer and covering tests; append ledger only when approved. No Git commit.

### Task 2: Complete owned transition carrier integration

**Files:**
- Modify copied `P/carrier/{copy_common,copy_switch,preserved_host,preserve_tree,owner_native,maintenance,maintenance_entry,maintenance_transport,maintenance_wire,preparation_runtime,driver,launch_copy,launch_engine,audit_policy}.py` only as needed for the accepted integration; retain unchanged files byte-for-byte.
- Create `P/carrier/transition_host.py` for current host stage/archives first-observed proof and preservation, `P/build_carrier.py`, `P/verify_carrier.py`.
- Generate package/spec/bootstrap, six program files, installed contract, current/retained state, revision/provenance maps, preparation layout, program-binding-proof, complete artifact-binding and disabled authorization under `P/carrier/`.
- Create `P/test_transition_integration.py`, `P/test_transition_failures.py`, `P/test_transition_candidate.py`, `P/task-2-report.md`.

**Interfaces:**
- Consume Task1 exact project_fd/project_process_fds/compile_layout/validate_layout contracts and current sealed Source bytes.
- `build_carrier.build() -> dict` deterministically constructs all outputs; never launches adapters. `verify_carrier.verify() -> dict` independently verifies actual modules, Source/candidate/member closure, actual program payload imports/lengths, all revision maps and disabled authority without mutations.
- Runtime keeps existing `run_maintenance`, `NativeMaintenance`, `CompletionWitness` and driver live-continuation semantics. A fixed explicit mode selects this accepted new generation only; no externally selectable arbitrary path/mode.

- [x] Step1: Add RED tests through actual OwnerSampler boundary and complete maintenance transport/driver path. Old direct deepcopy seven-field FD input must fail; new mapping must emit raw/input/result/source/helper facts before old closed validator. Test fd_lifecycle load before guest with isolated imports of the actual package/program payload.
- [x] Step2: Wire OwnerSampler projection and raw audit, historical ownership basis, new layouts, current source/retained archive separation and native full preservation. Keep 50 manager props/typed Stop arrays/jobs/FD/maps/storev4 checks and strict-empty unchanged. Reuse generic owned Stop sequence, not the quiet-only old entry. Current parent children/nlink/time bind fresh twice after checking historical stable identity and exact allowed namespace. Every unknown stage field is first-observed/full semantic-content checked in the future actual window; retain old historical complete snapshots as separate obligations.
- [x] Step3: Wire complete host preservation before new exclusive copies. A host-stage verifier must consume claim/outcome/raw-frame semantics from the bound previous packet and validate every leaf/unknown leaf, complete metadata, no writer/maps/FD, held parent, no-replace rename, postproof and journal. No live host deployment reads in offline tests; use repository-local temporary fixtures with explicitly synthetic identity wrappers.
- [x] Step4: Build package using exact Source order `contract,prepare,store,binaries,fd_lifecycle,guest` followed by actual private dependencies. Derive maps/programs/specs from real current code, bound Source roots/candidate manifests and retained evidence; test transitive actual imports. Include projection/layout/helper in real bootstrap, six actual guest programs and every producer closure. Preserve argv/package/decompression caps; no program may silently omit a dependency to fit.
- [x] Step5: Exercise active pair/containerd-only/quiet and full T0–T8 through real policy/mapper/parser/recorder/guard paths, using explicit future-state syscall/command ports only. Inject unknown/Stop/rename/reload/ACK/journal/cleanup/target occupied/new instance/extra business/listener/maps/writer/store drift and assert exact effects stop. Check one-use live witness and old receipt rejection. Do not replace the final proof with a literal true/stub or change Source to pass.
- [x] Step6: Run focused suites while iterating, final Task2 suites once on final bytes, write complete report. ROOT generates exact private task diff + scope report, fresh reviewer gates spec/quality, fixes with covering tests, then records approved freeze.

### Task 3: Actual artifact closure, full-scale budget proof and offline delivery

**Files:**
- Create `P/capture_scale.py`, `P/test_transition_budget.py`, `P/ROOT-protection.py`, `P/ROOT-seal.py`, `P/task-3-report.md`, `P/operation-window.md`, `P/README.md`.
- Update deterministic builder/verifier only for concrete integration defects; any changed covered path requires covering tests and regeneration/review of its actual consumers.
- Generate bounded captured correspondence/budget/length/replay/final integrity reports, exact task/whole review packages, final acceptance records and `P/phase-seal.json`.

**Interfaces:**
- `capture_scale.run() -> dict` consumes the actual 45196-frame packet, validates every raw byte/hash, replays production parse/record/mapper/guard and reports decoded/wire/journal/ordinary/failure charges plus every actual argv. Add clearly labeled synthetic future full transition stages, never present them as Linux/runtime acceptance.
- `ROOT-seal.py seal` freezes all final private files with path/type/owner/mode/size/hash and Source/candidate/input bindings. `ROOT-seal.py verify` is a separate read-only full verifier (no writes/actual entry). Exclude only the seal itself from its member list; explicit physical closure rejects additions/missing/symlinks. A seal cannot attest to its own later report: final delivery must be written before sealing.

- [ ] Step1: Run independent candidate verifier on the actual final package/programs. Verify Source1101/templates7/kit1176/bundle27 physical closures, exact hashes, imports incl actual fd_lifecycle, all binary identity reuse, per-argv <65536 and package/node caps. Tamper dependency/member/hash/authority/program payload and assert rejection.
- [ ] Step2: Validate complete original frame stream then replay the full captured scale through actual production code. New host-stage size derives from actual captured frame leaves, not old9735. Include old archive and retained-stage obligations and filesystem double reads/hashing in the recorded cost. No final success stubs. Report wall clock, caps, work100/cleanup20 headroom honestly; offline measurements cannot establish Linux latency.
- [ ] Step3: Resolve all test/review warnings and concrete gaps; protect all baseline tracked WIP, old phases and sealed inputs. Run `python3 -B .scratch/runtime-m1-fd-lifecycle-1030/ROOT-seal.py verify` and the new independent verifier on final bytes. Do not re-run prior Go builds/tests or unchanged Source suites just to duplicate known evidence.
- [ ] Step4: Write final concrete operation-window.md with exact paths/new generation/kit&bundle/seal references, history first-observed checks, T0–T8, budget/effects/conditional branches, live claim scope and stop contract. This is a reviewable future operation only, not authority or a new actual claim. README states offline result and remaining Engine→Native/Core→Task/report/page/observations work.
- [ ] Step5: ROOT fresh task review, then most-capable independent whole review of exact private diff, new physical closure, scope, all findings and tests. If findings, one fresh fixer gets all Critical/Important items and runs their covering tests; obtain fresh re-review before final approval. No unchanged-suite repetition by reviewers.
- [ ] Step6: ROOT write final delivery/approval and append ledger; seal once then separate read-only verify; record SHA256 outside sealed phase and in user response. Final result must distinguish sealed offline carrier from unexecuted real environment and product-loop acceptance. Real authorization may be requested only against this concrete complete checklist.

## Plan self-review

Every accepted design section maps to Task1 (history/mapping/layout), Task2 (full native/host/loader/continuation integration), or Task3 (real-scale caps/closure/review/seal/checklist). Task2 may repair private integration discovered by tests without a new user approval; no Source/permission/budget/policy relaxation is allowed. Current user selected complete offline A; execute all tasks continuously using fresh task agents and exact private preimages, without another execution-method question.
