# Default bridge IPAM offline repair implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement task-by-task. Preserve the dirty worktree; do not commit or change the index.

**Goal:** Correct the private default-bridge parser using captured record bytes, retain strict rejection boundaries, and deliver a reviewed, disabled replacement candidate.

**Architecture:** The existing shared `_network_metadata_v2` remains the single parser for formal preflight and private maintenance. Validate the allocated pool and gateway before comparing the requested IPAM configuration to either the legacy empty request or the exactly matching explicit request. No new public protocol or Go contract is needed.

**Tech Stack:** Python standard library/unittest; existing offline Go candidate builder; existing private carrier and bounded audit protocol.

## Global Constraints

- Offline only: no guest entry, VM or service operations, credential reads, provider/model calls, partition changes, or Git/index writes.
- Preserve all original captures, seals, candidates, unrelated dirty files, HEAD and index.
- Keep inventory v1/v2/v3 semantics, inventory v4 schema and default-bridge v2 classification; bind the corrected parser by its actual Source root.
- Keep seven-record completeness, exact IDs/counts/options/driver fields, RFC1918 canonical pool/gateway checks, duplicate-safe nested JSON, record/byte/deadline checks and independent ownership/API checks.
- Maintain maintenance 3600 seconds plus cleanup 20 seconds, Engine 120, qualification 240, manifest barrier 300 and outer 7200 seconds, together with every existing wire/audit/argv bound.
- Actual captured `BridgeIfaceCreator` is integer 2. The user explicitly accepted A (call_85f4e55b2ffe4610996d6c6e30eabfad): allow integer 1/2 only within the already complete default `bridge`/`docker0` record set; reject 0, unknown values, booleans and custom bridges. Do not treat 2 as ownership proof. Task 1 remains a reviewed IPAM-only historical milestone; Task 2 completes the accepted extension.
- Keep runtime qualification false, grants disabled and human authorization fields unset. Use a fresh unused execution directory; the old real grant is consumed.
- Independent task review and final whole review must have no open Critical or Important findings; create an exclusive seal and separately verify its externally recorded digest.

### Task 1: Strict IPAM request/allocation consistency

**Files:** Modify `deploy/experience/engine/store.py` and the exact `_ENGINE_LEAVES` allowlist in `deploy/experience/engine/bundle.py` for the two new test leaves; create `deploy/experience/engine/test_bridge_ipam.py` and `deploy/experience/engine/testdata/default-bridge-ipam-records.json`. Evidence/report under `.scratch/runtime-m1-bridge-ipam-1101/task-1/`.

**Interfaces:** Consume the exact captured wire tree/provenance under this phase's `evidence/`. Reuse `_network_metadata_v2`, `_classify_v4`, `synthetic_bolt`, `scan_synthetic_default_store`, and preflight validators. Produce a minimal parser change and actual command receipts with code/input fingerprints.

- [x] Wait for persisted parent prechange verification EXIT0 before writing Source files.
- [x] Add a real-byte fixture, verify all seven value hashes and provenance. The raw original bbolt was only a prefix: filesystem/bbolt test envelopes must be explicitly synthetic.
- [x] Run a failing regression that starts with captured bytes and changes only creator to 1, explicitly labeling that transformation. Assert the current parser rejects the explicit matching IPAM request. Retain the unchanged full capture and current creator rejection as separate evidence.
- [x] Keep exact config schema and move its value comparison after allocation validation, equivalent to:

```python
config = nested(bridge['ipamV4Config'])
_require(type(config) is list and len(config) == 1 and type(config[0]) is dict
         and set(config[0]) == {'PreferredPool', 'SubPool', 'Gateway', 'AuxAddresses'},
         'unsupported default bridge IPAM config')
# Existing info/data/canonical pool/gateway validation remains unchanged.
empty_request = {'PreferredPool': '', 'SubPool': '', 'Gateway': '', 'AuxAddresses': {}}
explicit_request = {'PreferredPool': str(pool), 'SubPool': '',
                    'Gateway': str(gateway.ip), 'AuxAddresses': {}}
_require(config[0] == empty_request or config[0] == explicit_request,
         'unsupported default bridge IPAM config')
```

- [x] Cover legacy compatibility and explicit requests across 10/8, 172.16/12 and 192.168/16. Reject half-empty pairs, mismatches, noncanonical CIDR/IP, public/IPv6 pools, extra/missing/wrong-type fields, auxiliary/subpool changes, duplicate JSON, nonzero counts, hidden endpoints, malformed driver and check/deadline interrupts.
- [x] Exercise actual decoder/classifier/scanner and both preflight validators with exact records in synthetic envelopes; verify old inventory downgrade rejection remains.
- [x] Add only `test_bridge_ipam.py` and `testdata/default-bridge-ipam-records.json` to the existing closed bundle leaf allowlist; preserve unknown-leaf rejection and every path/type/identity/content bound. The existing bundle gate must pass with the new real test fixture.
- [x] Run `python3 -B -m unittest -v test_bridge_ipam test_default_bridge_store test_store test_contract` from `deploy/experience/engine` with `PYTHONDONTWRITEBYTECODE=1`; persist stdout/stderr/exit/time and input SHA256. Broaden to other directly affected engine tests once these pass.
- [x] Deliver a task-scoped diff and independent Spec/Quality review; fix Critical/Important findings and rerun affected tests. No commits.

### Task 2: Chosen creator boundary, complete candidate and closure

**Files:** The same minimal parser/test files if choice A; private assembly, tests, controls and candidate under `.scratch/runtime-m1-bridge-ipam-1101/`; final operation checklist in that phase.

**Interfaces:** Consume Task 1's reviewed Source plus the explicit user A/B choice. Use `qualification/build_candidate.py` through a wrapper with a new private output path, then `engine/bundle.py`. Rebind an exact copy of the previous reviewed CLI carrier to the new Source/kit/bundle and fresh execution identity, preserving historical installation evidence separately.

- [x] Record the explicit user choice. For A, replace the single creator fixed literal with an independently type-checked integer in `(1, 2)` and retain every other fixed field. Add unchanged-full-capture acceptance and creator negatives. For B, preserve creator=1 and record the unchanged-full-capture rejection without a false success claim.
- [x] Build a complete new kit and bundle; retain all original build commands, binaries and Source/manifest fingerprints. Do not invoke Docker or the real execution entry.
- [x] Generate the private package/programs from the new exact Source, rebind all candidate descriptors/manifests/programs and disable authorization. Verify canonical/mirror modules, actual CLI entry, Source membership and all byte/time bounds.
- [x] Replay the captured tree through the packaged scanner and modeled lifecycle ports; include malformed/mismatched IPAM and creator/owner/API negatives. Do not replace the scanner with an unconditional success stub. Inherited historical full-scale timing must remain labeled with its original binding, not a new measurement.
- [x] Verify unrelated tracked files, HEAD/index and every old physical evidence seal unchanged. Historical current-Source proofs cannot be rerun as though a legitimate approved Source change did not occur; verify immutable physical inventories plus the explicit Source delta instead.
- [x] Produce independent task review and final whole review; resolve findings with fresh evidence. Reviewers may read, but not execute environment actions or rewrite original receipts.
- [ ] Create final finite controls/snapshot, exclusive new seal, externally record its digest, and perform a separate fresh read-only verify. Freeze the phase after sealing; append only the external progress ledger afterward.

## Plan review

The creator choice is now explicitly accepted as A and recorded in creator-acceptance.json. Task 1 remains complete; proceed through Task 2 without further routine confirmation. No accepted runtime or security budget is altered. No environment authority is inferred from offline review or a new seal.
