# Runtime preStart fixture fidelity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. No Git writes override commit steps.

**Goal:** Successful test provisioners execute the same per-call preStart boundary as Docker, allowing real startup race tests to exercise actual authorization.

**Architecture:** Reuse RuntimePreStartCheck and existing synthetic inventory. Separate configured early errors from a created partial result, and invoke callbacks outside locks before success/client startup. Preserve real authorization and journal behavior.

**Tech Stack:** Go 1.26, existing testing package, PostgreSQL 17 disposable local cluster.

## Global Constraints

- Only /path/to/switchback; HEAD fbebfe754045bdbf1af40871a5018eed33743011; no Git writes.
- Preserve existing dirty work and all older sealed files; write baseline snapshots/diffs only in .scratch/runtime-bootstrap-continuation-1017.
- No VM/SSH/Engine/actual metadata DB/key/provider/native actions by agents. Root alone owns disposable local test PostgreSQL.
- Success invokes per-call preStart exactly once, outside mutexes, with exact created identity and original context; rejection/cancellation cannot report successful StartedAt or open a control stream.
- Configured early failures retain their original result/error and do not call preStart. Do not add allow/bypass options or weaken production authorizers.
- Capacity UNKNOWN/default-deny; OpenSpec 6.5/8.4/9.3 remain open; synthetic tests do not establish native or UI acceptance.

## Task 1: Shared fake contract

Files: internal/workerruntime/adapter/fake/fakes.go and fakes_test.go. Interface consumes existing request/result/RuntimePreStartCheck; no exported API additions. Exact task brief defines implementation and tests. Snapshot original bytes before writing.

- [x] Add focused tests proving success callback/identity/context, rejection with zero StartedAt and preserved CID, nil check, cancellation before/inside callback, configured-error precedence and callback outside mutex.
- [x] Run RED against original fake: `go test -mod=readonly -count=1 ./internal/workerruntime/adapter/fake -run TestRuntimeProvisionerPreStart -v`, preserving raw/exit/source snapshots.
- [x] Minimal implementation: snapshot configured result/error and append request under mutex; unlock; return configured error unchanged; create partial with zero StartedAt; reject canceled context/nil check; invoke check with result CID/runtime/generation and request session; propagate failure; recheck context; return configured successful result. Update the old generic port smoke test to use a real successful per-call callback rather than RejectRuntimePreStart.
- [x] Run focused fake and application tests, fmt/diff checks, self-review; hand off exact baseline-to-final diff and original execution receipts.
- [x] Independent task Spec/Quality PASS C0/I0/M0. Fake GREEN8; application110pass/4existingfail, before-overlay same4fail, no skips; retained OPEN. Task2 may proceed.

## Task 2: Positive localproduct fixtures

Files: internal/localproduct/observation_supplements_fixture_integration_test.go, observability_settlement_integration_test.go and focused fixture tests. Only successful simulated starts call preStart before their owned goroutine/control stream. Existing published synthetic inventory precedes check; errors return created partial identity without StartedAt. Intentionally failing pendingInitialProvisioner remains unchanged. Implementer brief will pin exact paths and named tests after Task1 review; no production CAS edits.

- [x] Snapshot exact affected files; add callback rejection/order regression at the real fixture Provision seam before implementation.
- [x] Run failing regression using disposable PostgreSQL test URL supplied by Root; record any skip/environment error separately.
- [x] Invoke required check before simulated client startup; preserve client lifetime/stop latch and cancellation, never hold mutex across callbacks.
- [x] Run focused public initial experience, rejection and lifecycle tests using real PG/Hub with synthetic Engine facts; record failures honestly and diagnose blocking failures with deterministic barriers.
- [x] Independent Task2 Spec PASS / Quality PASS WITH MINOR C0/I0/M1; report wording narrowed by additive task-2-review-erratum.md. 7PASS/2baselineFAIL/0SKIP remain explicit. Fresh cumulative review follows Task3; no physical/UI acceptance claim.

## Task 3: Deterministic actual activation diagnosis

Files: only .scratch/runtime-bootstrap-continuation-1017/task-3 overlay clock seam/new diagnostic test and task-3-report.md. Product runtime code remains unchanged. Consume reviewed faithful fixtures and the private local PG; produce exact before/no-HB/reverse-order snapshots and an observed or falsified cause.

- [x] Freeze effective overlay sources; inject only a manual heartbeat tick channel into the existing simulated client, retaining its real gRPC/Hub/Journal/heartbeat observer.
- [x] At actual Activate store entry hold a barrier after Bind; send exactly one heartbeat, wait for the Journal acceptance commit, capture revisions and still-unapplied observation state, then release real Activate. The real pump waits for runtime.ready Apply, so observer commit cannot precede the held Activate; wait for it only after successful activation in the reverse-order control. Compare no-HB and reverse-order controls.
- [x] Run exact diagnostic command once with command-time source/raw/original exit; preserve failures, no production repair or capacity/Start claim.
- [x] Independent Task3 Spec/Quality PASS C0/I0/M0. Actual HB Accept+ACK Reserve causes lifecycle revision+2 and exact Activate conflict; no-HB/reverse controls pass. Named transport-exemption design risk reviewed with no blocking premise; implementation/negative gates remain. All product/UI/native gates stay open.

## Task 4: Separate demonstrated transport bookkeeping from lifecycle revision

Files and finite classifier are pinned in task-4-brief.md and docs/superpowers/specs/2026-10-17-runtime-control-transport-revision-design.md. Only journal's two mirror cursor increment sites and new focused tests/fixture clock seam; Activate and capacity authorizers remain unchanged.

- [x] Snapshot actual candidate; permanent real PG/Hub heartbeat-before-Activate success regression goes RED on old production. red3 records actual Activate CAS conflict; green1 records repaired three-order behavior without overlay.
- [x] Exact heartbeat/runtime.heartbeat and ack/runtime.control.ack pairs keep channel/cursor accounting but lifecycle delta0; all other pairs delta1. No protocol/schema/CAS changes.
- [x] GREEN real PG/Hub activation/replay/negative identity controls and journal duplicate/fencing/business-event controls; finalLocal38 + finalJournal49 leaf PASS, 28 top-level PASS, zero FAIL/SKIP. ExistingJournal29leafPASS/3FAIL; exact original-SQL overlay reproduces same3 capacity_binding_mismatch failures. Earlier4application/2legacyfixture failures retained. All raw/original exits and 664 command-time source inputs preserved; current hashes match.
- [x] Independent concurrency Spec/Quality review C0/I0/M1; fresh cumulative Astra SpecPASS/QualityApprovedWithMinors C0/I0/M2, final-review.md SHA c963480e21897d0ba9765473646139b7da6a0d08c1d7072ebc0a218598e002e9. Root cumulative862/current664 bindings match, no unrelated baseline drift; local test PG stopped with cleanup_verified=true. Minors are stop-test negative proof strength and expected negative-test log noise, neither requires this continuation's code change. Native/UI/fullcapacity/paid and all9baseline top-level failures remain open; this bounded implementation plan is complete, overall product is not.
