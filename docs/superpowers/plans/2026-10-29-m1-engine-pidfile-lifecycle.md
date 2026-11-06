# M1 Engine PID File Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind the fixed Engine PID file before Stop, permit only its verified post-Stop disappearance, and deliver an independently reviewed disabled carrier using unchanged kit25.

**Architecture:** One private maintenance-core role predicate uses existing validated filesystem nodes and live Engine identity. Original exact continuity remains for retained nodes and all other disappearances. Rebind and decode the accepted new core without changing Source or previous evidence.

**Tech Stack:** Python3.9, unittest/fake OS and manager ports, captured FS/hash fixtures, existing LZMA/base85 packers and SHA256 manifests.

## Global Constraints

- Workdir `/path/to/switchback`; preserve HEAD `fbebfe754045bdbf1af40871a5018eed33743011`, index SHA `2a66106ce8e0fe4c792efe9f4d0e8c04dc3a7ac7f989f3404b33564f33896cb5`, and 116 tracked dirty paths. No staging/commits or other checkout changes.
- Phase `.scratch/runtime-m1-engine-pidfile-lifecycle-1029`; future execution `.scratch/runtime-m1-engine-pidfile-lifecycle-execution-1029-01/carrier` stays absent.
- User accepted A offline-only in call_be76c670f48b4c9583d76d8428a15e9a. Accepted text `approved-design.md`; formal spec `docs/superpowers/specs/2026-10-29-m1-engine-pidfile-lifecycle-design.md`.
- No guest/VM/Engine/PG/service/provider/model/credential access or actual grant. The preceding real window is permanently failed/consumed, no restart/retry/readback/remote cleanup. Its last proven state was containerd_only, not current live authority.
- Never read/stat/enumerate/follow `.scratch/runtime-m1-worker-execution-credential-1026-01` or `.scratch/runtime-m1-containerd-dump-imports-1025`. Reference strings are not authority.
- Old seal `7260aa59c992e19194f389ecc6c399bc6f612975e3cb9642ddfca812d1807bca` and failed evidence seal `efb05de4826928138cbbf068774bf7b9a226f867808c42a30c4eccfe64581ccd` immutable. Root baseline protects 49588 old records and 10 exact trees.
- Source1095/templates7/Go, kit25 manifest `e82905915d9493f68d349fb4bfedb6abbdc8dfcb0262b312559d9834baa5b146`, bundle0002 manifest `0784bafc2c672dbb164a7a371c94741c5f7ed83e623a9f6f61ddef763e78823b` unchanged; no rebuild or unrelated Source242/Go2244 reruns.
- Only behavioral change: phase-root and carrier copies of `maintenance.py`. Other three core modules, owner policy, collector, preserver, and self-held FD entry SHA `fa1a3b41db6126dad62eec0e2cb38a6e0116301193ab697e5e7a97d36023c165` unchanged.
- All authority/schema/action ordering, owner/FD/maps/API/restoration proofs, no-retry, witness, failure handling and cleanup remain. Budgets maintenance120/Engine120/qualification240/barrier300/total7200, argv<65536, binding<8MiB and all wire/LZMA caps unchanged.
- Fixed PID path `/run/switchback-engine/m1-resident/docker.pid` comes from bound Engine ExecStart `--pidfile`, not external input or daemon.json. Node uid/gid0, mode0644, nlink1, kind file, same device as Runtime dir; its inode and all fields remain exact across pre-Stop observations. PID canonical decimal bytes use the initially verified live Engine main_pid; no historical-basis fallback.
- Capture fixture `fixtures/captured-engine-stop.json`: complete captured FS46→41/41 nodes, exact hashes and provenance; full owner samples were digest-only. Manager/proof envelopes and later quiet stages are synthetic. PID bytes were not emitted; size/hash matches known daemon PID. No full-original-proof or actual-Linux execution claim.
- Literal Python `/Library/Developer/CommandLineTools/Library/Frameworks/Python3.framework/Versions/3.9/bin/python3.9 -B`; no -I on host. Prior copied regressions use `task-1/captured-fds.json` and historical `ROOT-fd-approval.json`; keep their meanings separate from new PID-core approval.

---

### Task 1: PID role, continuity and complete lifecycle regression

**Files:**
- Modify phase `maintenance.py`; mirror exact bytes to `carrier/maintenance.py`.
- Create `test_engine_pidfile.py`, task-1/ receipts/diff/freeze and task-1-report.md.
- Existing `test_maintenance.py` is unchanged coverage unless a specifically justified fixture integration is needed; document any alteration.
- Do not modify the copied legacy task-1/captured-fds.json, other core/entry, Root controls or Source.

**Interfaces:** Consumes `_continuity(initial, current, stopped)`, `run_maintenance`, original FakeOps/proof seam, captured fixture; produces a pure `_engine_pidfile(initial)` role and updated `_continuity` with the same public signature. No IO or new public DTO.

- [ ] **Step 1: RED from actual captured FS.** Compose initial/after proof envelopes with captured node dictionaries, verified Engine main_pid68199 and explicit synthetic manager/remaining proof fields. Preserve original nonzero result `unexplained disappeared T1 node`; minimise to PID-only while retaining the full46→41 case. Also drive original `run_maintenance` fake boundary from owned_pair, verifying failure occurs after one Engine Stop and before containerd Stop/preserve.

```sh
python3 -B -m unittest discover -s .scratch/runtime-m1-engine-pidfile-lifecycle-1029 -p test_engine_pidfile.py -v
```

- [ ] **Step 2: Implement the smallest pure role predicate and continuity extension.** Representative complete predicate (retain repository error conventions):

```python
def _engine_pidfile(initial):
    nodes = {n['path']: n for n in initial['after']['filesystem']}
    node = nodes.get(RUNTIME + '/docker.pid')
    if node is None or initial['stage'] != 'owned_pair':
        return None
    live = [d for d in initial['after']['daemons'] if d['unit'] == UNITS[1]]
    require(len(live) == 1, 'Engine PID live identity missing')
    pid = live[0]['main_pid']
    require(type(pid) is int and 0 < pid < (1 << 63), 'Engine PID value')
    runtime = nodes.get(RUNTIME)
    _node(node)
    _node(runtime)
    require(runtime['kind'] == 'directory' and node['kind'] == 'file' and
            node['mode'] == 0o644 and node['nlink'] == 1 and
            node['device'] == runtime['device'], 'Engine PID file role')
    raw = str(pid).encode('ascii')
    require(node['size'] == len(raw) and
            node['sha256'] == 'sha256:' + hashlib.sha256(raw).hexdigest(),
            'Engine PID file bytes binding')
    return dict(node)
```

Import hashlib and existing owner_policy `_node`; its validation enforces full NODE schema, positive bounded identity fields, root owner and hash types. Compute this predicate on initial before any disappearance comparison on every `_continuity` call; original sequencer calls continuity before Stop, so bad role must fail before the command. Preserve retained-node all-field comparison, runtime-directory nlink/size exception and manager/boot gates. Only extend vanished-node condition:

```python
engine_pid_gone = (role is not None and node == role and UNITS[1] in stopped and
                  current['stage'] in ('containerd_only', 'quiet') and
                  not any(d['unit'] == UNITS[1] for d in current['after']['daemons']))
require(engine_pid_gone or (node['kind'] == 'socket' and
        ((path in PATHS[:2] and UNITS[0] in stopped) or
         (path not in PATHS[:2] and UNITS[1] in stopped))),
        'unexplained disappeared T1 node')
```

No file filtering, type relabeling, arbitrary regular-file exemption or Stop-exit0 shortcut. An initial non-live Engine cannot grant this role; stable existing files retain original comparison/preservation. No live identity is fabricated for an initial containerd_only/quiet proof.

- [ ] **Step 3: GREEN plus necessary refusals.** Test exact full captured metadata/hash and both after sets; wrong path/PID/hash/size/uid/gid/mode/nlink/device/inode, missing or invalid live identity, bool/invalid PID, no/wrong Stop, owned_pair or Engine-live after proof, altered retained PID node, other regular file/dir disappearance, already-absent PID and retained PID cases. Assert bad initial role and pre-Stop inode replacement reject with zero Stop commands. Keep existing socket cases unchanged.

- [ ] **Step 4: Full real sequencer fake-port lifecycle.** Model captured owned_pair46→containerd_only41, then explicitly synthetic quiet by removing only proven containerd socket roles. Exercise preserve/reload/strict and local cleanup/error paths. Also start from captured containerd_only with only containerd loaded and from quiet with no units loaded; assert no Engine show/Get/Stop when unloaded, no old continuation and original call ordering. Record complete fake call logs.

```sh
python3 -B -m unittest discover -s .scratch/runtime-m1-engine-pidfile-lifecycle-1029 -p test_engine_pidfile.py -v
python3 -B -m unittest discover -s .scratch/runtime-m1-engine-pidfile-lifecycle-1029 -p test_maintenance.py -v
```

- [ ] **Step 5: Self-review, exact receipts and independent task gate.** Save RED/GREEN commands/exit/stdout/stderr, before/after diff, fixture and source context hashes, and file freeze. Owned report task-1-report.md. No commits; Root generates scoped snapshot review package. Fresh independent reviewer resolves all actionable findings before packaging.

### Task 2: Rebound decoded carrier and release

**Files:** New phase build_carrier.py/verify_carrier.py/private-revision-map.json, necessary selector/approval assertions in test_carrier.py/test_self_held_package.py, new test_engine_pidfile_package.py, task-2 evidence/report/freeze, README.md/operation-window.md. Preserve entry and runtime helper bodies except exact current OFFLINE/EXECUTION paths. Root owns baseline/integrity/seal/approval/final notes.

**Interfaces:** Consume accepted PID core and freeze/review, Root-private-approval.json, unchanged prior FD approval ROOT-fd-approval.json; produce disabled complete carrier. Root/carrier maintenance.py must match accepted core; other three root/carrier cores and entry remain exact previous bytes. New candidate reuses ROOT-candidate.json and Source provenance unchanged.

- [ ] **Step 1: Explicit current-core and historical approval maps.** Inherit every prior binding source_files record unchanged; add current private code/tests/controls/accepted Task1. Historical Source revision map remains byte-identical, and prior FD approval stays historical. Replace only the maintenance-equality exception with exact current approval; a generic exemption for all cores is forbidden. Independently enumerate required roles/revisions in verifier, including every current helper and meaningful refusal test. Validate all accepted freeze files/context records.

```python
assert root_maintenance_bytes == carrier_maintenance_bytes == approved_pid_core_bytes
for name in ('owner_policy.py', 'owner_native.py', 'preserve_tree.py'):
    assert current_root[name] == current_carrier[name] == previous_root[name]
assert current_entry == previous_entry
assert all(binding['source_files'][name] == old for name, old in prior_binding['source_files'].items())
```

- [ ] **Step 2: Complete packer/independent decoder.** Regenerate all seven actual programs and decoded module rows; new maintenance byte/hash must match accepted core. Decode and run actual packaged continuity and sequencer on captured/synthetic lifecycle fixtures, not a stand-alone copied predicate. Keep old FD decoded regression using its original approval; adapt only the necessary scope assertion. Cover unreviewed core/map/context tamper, old consumed grant/path, caps and pre-native disabled entry.

```python
assert grant['environment_execution_authorized'] is False
assert grant['provider_calls_authorized'] is False and grant['no_retry'] is True
assert all(grant[k] is None for k in ('authorization_id', 'human_intent_ref',
                                    'issued_unix_ns', 'expires_unix_ns', 'worker_delivery'))
assert new_execution_path_exists is False
```

- [ ] **Step 3: Final relevant full regressions and determinism.** Private suite previously205 plus new cases; cloned compatibility45 unchanged. Record exact commands, env, raw nonzero/success receipts and final hashes. Caps remain binding<8MiB/codeargv<65536/original budgets. Recompute generated outputs in memory, no rewrites. No unchanged Source/Go/binary test reruns.

```sh
python3 -B -m unittest discover -s .scratch/runtime-m1-engine-pidfile-lifecycle-1029 -p 'test_*.py' -v
PYTHONPATH=.scratch/runtime-m1-engine-pidfile-lifecycle-1029/carrier:deploy/experience/engine python3 -B -m unittest discover -s .scratch/runtime-m1-engine-pidfile-lifecycle-1029/carrier-tests -p 'test_*.py' -v
python3 -B .scratch/runtime-m1-engine-pidfile-lifecycle-1029/verify_carrier.py
python3 -B .scratch/runtime-m1-engine-pidfile-lifecycle-1029/task3-determinism.py
```

- [ ] **Step 4: Task review, whole review, Root final seal.** Fresh independent task Spec/Quality review, then final whole review of before-Stop role→after-Stop proof→preserve/strict, actual packaged core, inherited input/authority/cap/budget protections and Root release tools. Resolve findings without permission churn inside scope. Final Root guard/verifier, complete approval and seal cover all current files and all49588 prior/failed records; no writes after seal. Release notes state only offline completion and last observed containerd_only; real environment still requires new final explicit grant.

## Completion ledger

Progress is tracked in phase progress-ledger.md; plan becomes an immutable requirements input once Task1 freezes it. User already authorized executing both tasks; no intermediate choice is pending.
