# M1 Namespace Capability Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Correct the inactive publisher capability contract and produce a fully reviewed offline dual-capability diagnostic candidate without granting or executing it.

**Architecture:** First update only the inactive service template and its existing strict construction test. Then derive a new fixed v1 carrier from the PID1 candidate, consume the corrected pure mapper and reuse the actual existing ELF/receipt. Preserve historical evidence using exact before/frozen/after records for the two authorized source edits; production deployment gates remain closed.

**Tech Stack:** Go1.26.4 offline targeted tests; Python3 standard-library fake/read-only tests and binding verifier; systemd source template only.

## Global Constraints

- Workdir /path/to/switchback. New phase C=.scratch/runtime-m1-namespace-capability-candidate-1021. Apart from the two authorized current paths and this new plan, writes are confined to C and owned private temporary test cache/tmp.
- Accepted spec docs/superpowers/specs/2026-10-21-m1-namespace-capability-boundary-design.md SHA256809c13b87911640ba9cb0541bdce91361eaf07b8f8882101399b47351a1e55d0; human approval call_6499526fa62a48449d2599418c7bfd17, “接受双能力边界，离线实施（推荐）”.
- Fixed candidate capability declaration is CAP_SYS_ADMIN CAP_SYS_PTRACE, bitmap0000000000280000; ambient remains0. This is a whole-process guest authority boundary, not read-only namespace permission.
- 维持root/Group=root、NoNewPrivileges=yes、AF_UNIX、RestrictNamespaces=yes、原SystemCallFilter和资源上限；不为Backend、Worker、其他unit或macOS宿主增加能力。
- 不安装/启用unit、不capset或设置file capability，不运行新的原生进程，不初始化key/Trust/journal，不调用Engine、PG、provider，也不改变三个生产门禁。
- verifyPublisherDeployment and verifyCapacityDeployment stay unchanged/unavailable. No new Go API, raw format, guarantee version, PG/Start/IPC owner or env/CLI bypass.
- Only existing source edits allowed: deploy/experience/switchback-capacity-publisher.service (before SHA4ca507e7eb681469948c79a57ce47127913037b7602ad73d43c2836f82f193a9) and internal/workerruntime/adapter/capacitypublisher/publish_test.go (before SHA59760cde3a90bfccc33fe8e772b4055b2b4f1883b920ac5c0bac8bd2f9865105). Controller source-before copies preserve exact prior bytes. No commits/stage/reset/clean/push.
- Preserve HEAD fbebfe754045bdbf1af40871a5018eed33743011, branch codex/observability-master-integration-1009, empty index,1805 baseline visible files except those two exact updates,985 protected metadata entries and536 prior-carrier files. New plan adds only one visible path, union1806.
- Fixed window .scratch/runtime-m1-namespace-capability-window-1021 must remain absent. Fixed stage /var/lib/switchback-deployment-diagnostic1021-nscap; unit switchback-deployment-diagnostic1021-nscap.service; cgroup /system.slice/switchback-deployment-diagnostic1021-nscap.service; nonce76c393e4-32e8-47c0-9f05-c32619124827. Guest stage absence remains untested until a separately authorized future window.
- Reuse frozen N=.scratch/runtime-m1-deployment-pid1-native-candidate-1021 ELF5659311B SHAae851af5d68277d80d4135d7b8e1f5a435554d991f971698fd22f4eacb4228e0 and actual receipt SHA7e7ceaf4000367aec3b2a3876bfb92d8ca54bce67695af2b13e1ec29c95a54f6. Do not rebuild/execute ELF or fabricate new build results. Verify actual selected input/snapshot equality before reuse; drift stops work.
- Carrier source names remain guest.py/host.py/output_mapper.py/manager_forensics.py/test_guest.py/test_host.py/test_output_mapper.py/verify.py. manager_forensics.py remains SHA cf7e424d6393a7dc2217f6e6c39d880786160b18789262f0f30ca2d23490974f. Corrected mapper/test base is F=.scratch/runtime-m1-deployment-output-contract-fix-1021.
- 原manager mapper、once、raw-first、父/worker监督、30/45/45/30秒和总150秒、失败停止、禁止retry/resume/独立readback/Stop均保留。Old native authorization stays consumed.
- Python -I -B, explicit local imports only. All native constructors and real guest/VM/service paths are forbidden in this slice. Fake IO/local temporary FD fixtures only.

---

### Task 1: Inactive publisher capability contract

**Files:** Modify only the two current source paths listed above. Create C/task-1-* evidence/report/diffs; controller baseline/source-before/authorization files read-only.

**Interfaces:** Existing TestPublisherProductionConstructionAndUnit validates exact service properties and gate-before-secrets construction. TestPublisherDeploymentAlwaysClosed must remain passing. Produce task-1-source-hashes.json with exact before/after facts for both source paths; controller creates the fixed authorized-source-updates manifest for Task2.

- [x] Confirm before hashes; inspect current construction test/source (CodeGraph first). Change the existing expected capability value first:

```go
"CapabilityBoundingSet": "CAP_SYS_ADMIN CAP_SYS_PTRACE"
```

- [x] Run genuine RED against the still-old service template. Use /usr/local/go/bin/go, GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off, GOMAXPROCS=2, fresh owned /private/tmp cache/tmp; -mod=readonly and -p=1. Save complete env/argv/stdout/stderr/exit. Exact target:

```sh
/usr/local/go/bin/go test -mod=readonly -p=1 ./internal/workerruntime/adapter/capacitypublisher -run '^TestPublisherProductionConstructionAndUnit$' -count=1
```

Expected RED: unit policy mismatch CapabilityBoundingSet, not a build/environment failure.

- [x] Change only the template capability line and add concise whole-process CAP_SYS_PTRACE risk comments:

```ini
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_SYS_PTRACE
```

- [x] Reuse the same owned cache for the final command, then clean only that owned temporary root:

```sh
/usr/local/go/bin/go test -mod=readonly -p=1 ./internal/workerruntime/adapter/capacitypublisher -run '^(TestPublisherProductionConstructionAndUnit|TestPublisherDeploymentAlwaysClosed)$' -count=1
```

Expected exit0, both tests PASS. Check gofmt diff on changed test, go.mod/go.sum and unchanged gate source hashes. Do not run native/PG/Engine/provider or unrelated suites.

- [x] Save exact source-before→current unified diff and source hashes/report. Controller task review assesses Spec+Quality and verifies no other unit/source change.

### Task 2: Fixed dual-capability diagnostic candidate

**Files:** Create C's eight carrier sources, metadata-only assembler, source-manifest/candidate/release/identity, source-final snapshots, fixture provenance, reuse receipt/proof, task-2-* diffs/logs/report. Controller baselines, authorization, authorized-source-updates, root checks/operation checklist/reviews/seal are read-only.

**Interfaces:** Consume reviewed Task1 before/after facts and C/authorized-source-updates.json. Keep F-compatible bindings(io), verify_release(read), v1 packet/receipt/window schemas. Host build receipt path remains C/build-receipt.json, copied byte-identically from actual N receipt; artifact path in that receipt still points to the immutable N ELF. Record this as reuse, never a new build.

- [x] Verify N receipt hash, selected dependency/GOROOT/input membership hashes,29 current worktree/module inputs and build-source snapshots, and ELF hash/header/mode. Copy receipt bytes only; create build-reuse.json with original source provenance and current verification. No Go build or ELF invocation.
- [x] Copy N carrier; replace mapper/tests from F as the base, rebind fixed identities and exact absolute caller cgroup. Guest changes are only nonce/stage/unit plus:

```python
'CapabilityBoundingSet': 'CAP_SYS_ADMIN CAP_SYS_PTRACE'
```

ELF SHA/size stay unchanged. Keep HISTORY five seal pins/814 artifacts and manager unchanged. Preserve historical PRIOR fixture paths; avoid blanket replacement of all prior references.
- [x] Adapt test imports explicitly: F/test_output_mapper.py imports replay.load_capture, which is not a ninth runtime source. Load the immutable F/replay.py helper by fixed file path using importlib in tests, with the F file covered by536-file prior manifest; do not add ambient imports or a new native helper.
- [x] Old pid1 real packets must be rejected by the new-unit mapper as cross-unit inputs. Rename/adapt only the corresponding old real-packet tests to assert this rejection; do not rewrite old packet fields. Keep full/denied/zero positive cases as explicitly synthetic fixtures with new unit identity, absolute path and caller_thread. Failure primitive caller.* and process caller_before keys stay unchanged.
- [x] Fixed candidate/launch/property mutations to single capability, wrong pair or any extra capability must reject before fake guest IO. This is a source/candidate binding rule; pure packet mapper continues to preserve raw capability facts and grants no authority. No new eligibility API is introduced.
- [x] Rebind verifier spec/plan/phase/window/counts to this phase:1805 baseline,1806 union,536 prior files,985 metadata entries,814 history. Preserve all977 historical frozen_ref checks. Existing parser remains fixed SHA9b1c10cbae79bf81c6c1cbfff6e6451ced342a58792d8e4f85260d0ddb1ca4f6. For exactly the two authorized source paths, require exact old record/frozen bytes and exact current after hash from controller manifest. Other974 current historical paths remain old bytes. Baseline current-file checks likewise allow only those exact two before→after updates. No generic fallback or caller-selected map.
- [x] Reuse native build command checks from actual N receipt: six successful commands and original pid1 build-cache prefix, same tool/version and artifact path. source-final/manifest binds eight snapshots, reused build/ELF and new source/candidate/release facts.
- [x] Use existing memory/fake-IO harness for cap/identity/artifact/history/update-map mutations, cross-unit old real rejection and existing guard/supervisor/output/persistence/cancellation/once matrices. Run focused regressions during edits, then complete test_guest/test_host/test_output_mapper once, plus fixed read-only verify.py. Save exact env/argv/stdout/stderr/exit and method/subtest counts. Never call host.py window.
- [x] Freeze final eight sources, exact N→new and F-mapper→new diffs, metadata/source hashes and report. Controller task review must validate actual diff and the above boundaries.

## Controller closure

- [x] Execute approved tasks continuously, no human checkpoint between them. Fresh task reviews Spec+Quality, then independent whole-change review.
- [x] Verify final candidate with focused independent relevant suite/read-only verifier on reviewed hashes; avoid repeating unchanged large matrices once full implementer evidence passes unless a failure or later source change justifies it. Independently validate source snapshots, reused build chain and exact authorized current updates.
- [x] Final preservation: all985 historical metadata entries/536 prior files unchanged;1805 baseline paths differ only at the two reviewed after hashes, visible union1806, HEAD/branch/index unchanged, fixed real window absent. Current gate sources must match baseline hashes.
- [x] Write concrete operation-window.md with exact argv/env, fresh identity, actual source/build/ELF hashes, CAP_SYS_ADMIN+CAP_SYS_PTRACE effect and unchanged150s protocol/output/cleanup boundaries. Clearly distinguish offline readiness and future native/namespace/production qualification.
- [x] Seal/read back final candidate, reports, proof and plan. Only after this concrete closure may a new single real window be submitted for separate authorization. No runtime execution occurs under current approval.
