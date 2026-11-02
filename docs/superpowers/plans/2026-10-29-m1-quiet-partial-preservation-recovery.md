# Quiet partial preservation recovery implementation plan

> For agentic workers: use subagent-driven-development task-by-task, fresh Spec/Quality task review and most-capable whole review. Root owns final protection/seal and future approval request.

**Goal:** Deliver the complete accepted offline recovery carrier with capture-scale full lifecycle evidence and a reviewable future operation checklist.

**Architecture:** Keep the existing Source and pinned kit. Separate durable host evidence from maintenance semantics; add a quiet-only partial layout to private inventory/preservation; finally rebind every actual program and inherited dependency into one disabled carrier.

**Tech Stack:** Python3.9 stdlib, current managed integration worktree, explicit fake OS/process ports, sealed captured public frames.

## Global constraints

- Human call_1ffbcce18a4648828a1f88f64728a731 approved accepted A design SHA3add7357e12778e2f7787a0df9467e1d452c05c50b30242641332f62eaaa281c, OFFLINE ONLY. No guest/VM/Engine/PG/service/provider/key access or startup, no actual grant, no Git/index/Source/Go/template/kit mutation.
- Only new phase /path/to/switchback/.scratch/runtime-m1-quiet-partial-recovery-1029 may change, plus this new untracked implementation plan. Existing worktree/Source/old seals/captured bytes stay unchanged; no commits or staging.
- Never read/stat/enumerate/follow .scratch/runtime-m1-worker-execution-credential-1026-01 or .scratch/runtime-m1-containerd-dump-imports-1025. References in public metadata are not permission to follow.
- Budgets fixed: decoded128MiB; wire192MiB; host journal192MiB; ordinary host64MiB; failure1MiB; frame16KiB; event1MiB; maintenance120s/Engine120s/qualification240s/barrier300s/driver7200s.
- Journal is O_EXCL/0600, complete frame raw+LF append/fsync before protocol action; partial write/fsync failure poisons, never retry. Keep committed offset/count/digest and bounded current buffers, not a second full raw list. Failure writer is independent, primary identity survives all secondary failures.
- New mode is quiet_partial_v1 only: fresh full quiet owner/manager/process/FD/maps/listener/store-v4 and full public content proof before mutation. Existing5archive roots and1adopted var/lib tree,0mkdir/0Stop/6remaining fixed noReplace moves; adopted is separate from current successful moves.
- All1187 regular files/196652178B require fresh full verification in future real recovery; the12 metadata-only files from readonly are not accepted content. New whole-source/target/archive children closure and exact identities required.
- Reuse kit25 e82905915d9493f68d349fb4bfedb6abbdc8dfcb0262b312559d9834baa5b146 and bundle0002 0784bafc2c672dbb164a7a371c94741c5f7ed83e623a9f6f61ddef763e78823b. Source1095/templates7 roots unchanged; no ELF rebuild.
- Argv<65536, package expanded<2MiB, LZMA2 memory128MiB, canonical/EOF/exact module and revision-role completeness, disabled environment/provider grant with null human/time/worker fields, no retry.

## Task 1: Durable journal and independent audit budgets

**Files:** carrier/audit_policy.py, carrier/evidence_store.py (new); carrier/maintenance_wire.py, carrier/maintenance_transport.py (cloned private changes); test_evidence_store.py, test_audit_transport.py; task-1 report/raw evidence/freeze. Own no layout/owner/preserve/driver/builder files.

**Interfaces:** audit_policy exports fixed profile/bounds. EvidenceStore(phase, ports=None) performs no constructor IO; start_journal(), ordinary(name, raw), frame(raw), failure(name, payload), state(), close() are explicit native/test seams. frame returns committed offset/count/digest after fsync. Frames retains frame_count/decoded_total instead of full held raw list. supervise can use an independent failure recorder, only already-read output.

- [ ] RED shared ordinary+raw exhaustion and failed finalization at the original callback seam.
- [ ] Implement independent before-IO accounting, single raw journal, bounded failure snapshot/primary preservation and poison-after-uncertainty.
- [ ] Keep bootstrap self-contained before authenticated package: do not top-level import audit_policy in maintenance_wire because it runs standalone under guest -c; use explicit wire constants checked against profile by builder/verifier.
- [ ] Test exact/+1, growth, short writes, partial/fsync/close/recorder failures, record-before-action, clock/EOF, no retries or post-failure reads, and no full raw duplicate. Run a single bounded local journal fixture using captured frames in a new private file; it proves local IO only.
- [ ] Run focused then final whole owned suites once, freeze/report; independent task review and fixes.

```python
store = EvidenceStore(phase, ports=fake_ports)
store.start_journal()
receipt = store.frame(b'{"kind":"audit"}')
assert receipt['committed_bytes'] == len(b'{"kind":"audit"}\n')
assert events.index('fsync') < events.index('protocol_action')
```

## Task 2: Quiet-only partial layout and preservation

**Files:** carrier/partial_layout.py plus private owner_policy.py/owner_native.py/maintenance.py/preserve_tree.py/maintenance_entry.py and existing root mirrors as required; test_partial_layout.py/test_partial_maintenance.py/test_partial_native.py; task2 report/freeze.

**Consumes:** accepted Task1 budget/wire interfaces; Root-copied captured-basis/observation/summary. **Produces:** fresh quiet-only owner proof and TypedPreserver plan/state with already_preserved separated from current moves; full six-move/hostACK/reload/strict sequencer.

- [ ] Pin exact captured layout (1target-only,6source-only,APIabsentboth,5archive identities) as a candidate, not authority. Add pure closed schema and stale/conflict/missing/children/mount rejection before side effects.
- [ ] Native inventory must full-rehash all1187 files, preserve historical nine archives/copy/stage checks and cooperating FD normalization, observe directory current baseline rather than historical nlink/times.
- [ ] Current owner policy accepts only fixed physical tree roots in quiet partial mode; preserve legacy branch semantics, reject all live daemon stages. Never issue Stop even when manager has loaded inactive details. Keep old full manager/typed arrays/jobs/masks/FD/maps/store-v4 gates.
- [ ] Capture existing archives without mkdir, protect the adopted var/lib tree each guard; perform exactly6 remaining fixed noReplace moves, no reverse/duplicate rename. All Root timestamps are fresh; only current proven syscalls authorize parent/root changes.
- [ ] Preserve initial current/adopted nodes independently in state; 0Stop/0mkdir/1adopted/6moves/1hostACK/1reload/strict gates. Explicitly cover original NativeMaintenance.stopped_recheck rebasing and held FD coverage without historical-action spoofing.
- [ ] RED original missing-var/archive-occupied blockers, negative exact-side-effect prefixes and full fixture path; final owned and pertinent legacy suites, freeze/report, independent review.

```python
result = run_quiet_partial_maintenance(fake_ports, binding)
assert fake_ports.stops == [] and fake_os.mkdirs == []
assert len(result['preserved']['already_preserved']) == 1
assert len(result['preserved']['moves']) == 6
assert fake_ports.reload_count == fake_ports.host_ack_count == 1
```

## Task 3: Full carrier, bindings and capture-scale T0—T8

**Files:** new-phase driver/runtime/copy adapters and builder/verifier/revision maps/actual program artifacts; full lifecycle/package/budget tests; task3 report/freeze. Task1/2 accepted files are inputs, not rewritten absent a concrete reviewed defect.

**Consumes:** exact Task1+Task2 accepted freezes/approvals and old kit25/source lineage. **Produces:** deterministic disabled new carrier, exact bindings/programs, full measured lifecycle and operation-window.md.

- [ ] Clone the old45 carrier/adapters and exact lineage; only fixed future phase selectors and approved helper/semantic revisions differ. Bind new quiet policy/actions (noStop/noMkdir), new config/profile/complete layout and fresh execution phase. Preserve historical maps, and explicitly approve exact private before/after/addition roles; reject aliases/omissions/equal-digest wrong paths.
- [ ] Wire LocalOperations to EvidenceStore and journal; bypass no authorization/claim check. All lifecycle failure/outcome/transport diagnostics use independent bounded failure budget and keep primary error. Do not change Source loader, Engine/qualification/PG/credential timing or provider=false.
- [ ] Build and independently decode maintenance plus six actual programs. Runtime code argv check remains mandatory. Validate module order/exact_source_bytes and compressed canonical/EOF/memory bounds; full candidate tree verification and deterministic in-memory rebuild with zero writes.
- [ ] Execute real driver→transport→decoded guest sequencer/preserver and unchanged subsequent Source call ordering through fake ports; not a toy ops-only pass. Use captured proc/FD/maps event-size shapes at each fresh guard. Record maxima/headroom for all five counters across COMPLETE T0—T8.
- [ ] Test every failure prefix/close/primary and independent recording; corrupted config/candidate/module/map/approval/deadline/continuation is refused pre-native. Preserve original nonzero evidence. New offline Root controls bind all current input/review sets and disabled grant; future execution path must remain absent.
- [ ] Final appropriate suites, independent task review/fixes, most-capable whole review, Root exact Source/history/HEAD/index checks, final approval/full seal create+readonlyverify. No writes after seal.

```python
evidence = run_driver(integrated_fake_ports, binding)
assert evidence['status'] == 'installed'
assert integrated_fake_ports.guest_stops == integrated_fake_ports.archive_mkdirs == []
assert integrated_fake_ports.engine_calls == integrated_fake_ports.qualification_calls == 1
assert all(metrics[name] <= limits[name] for name in limits)
```

## Root delivery controls

Use fixed Framework Python3.9 -B for commands. Verify parent readonly seal8f2574f559722300aeb2d39da6a146e4396e79588bdd502d21dae823437c2e5f and exact Source1095/templates7 roots, HEAD/index/tracked dirty state. Older active/failed phases stay immutable. Record reports/reviews/static context without recursive binding to mutable closing outputs. Whole review can identify Source/lifecycle integration concerns; resolve them before final sealing.

Task acceptance is a testable offline deliverable; no request to rerun guest or obtain another grant mid-slice. Routine fixes within approved A proceed autonomously. If a real design/permission contradiction appears, finish unaffected work and surface one concrete decision. Final user review is for a sealed future real operation only.

