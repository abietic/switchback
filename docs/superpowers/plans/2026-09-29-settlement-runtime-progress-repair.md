# Settlement Runtime Progress Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox syntax for tracking. The user authorized internal implementation/test choices and explicitly waived repeated technical-document approval; execute continuously.

**Goal:** Publish completed Turn evidence and dispatch exactly one queued successor despite legitimate Runtime journal progress, without weakening authority or replay checks.

**Architecture:** Two settlement-owned entry points revalidate durable settlement authority and current graph under the same PostgreSQL transaction as the effect. Generic publication/queue commands remain strict. Object integrity I/O stays outside transactions; original evidence and frozen plans remain immutable.

**Tech Stack:** Existing Go, pgx, PostgreSQL test fixture, local ObjectStore and existing runtime coordinator. No new dependency, schema or provider is needed by default.

## Global Constraints

- Work only in `/path/to/switchback/.worktrees/observability-supplements-v1`; preserve the unrelated main checkout. Baseline is `2f73089`.
- Keep generic publisher and queued-command exact revision semantics; no `ignore_revision`, global `>=`, heartbeat suppression, or caller-side latest-revision retry.
- Validate persisted phase/revision/digest, lease owner/fence/expiry, frozen evidence/queue intent, exact Tenant/User/Workspace/Session/Run/Turn/AgentSession and Runtime identity/generation/channel epoch under lock.
- Session/Run/Turn revisions stay exact; only a matching, non-fenced current Runtime graph with monotonic revision may supply the transaction-local CAS revision.
- Preserve BackendDeadline forced-stop publication through its existing strict frozen-revision path, selected from durable settlement source and existing forced-stop witness. Its stopped/fenced Runtime does not acquire progress tolerance; cover successful strict publication and stale Runtime rejection. Active/non-fenced checks apply to the new progress-tolerant path, not removal of existing forced-stop behavior.
- No ObjectStore, file, network, provider or container I/O in a database transaction. No rewrite of frozen domain proof, evidence or queue intent.
- Lock order must preserve outcome/settlement before graph and Task before Session when Task participates; never reverse Session to settlement.
- Publication and continuation replay must return the original effect, not duplicate metadata/outbox/Run/Turn. Invalid authority must produce no partial writes.
- No changes to Task-bound queue product semantics, public wire schemas, durable Langfuse frames, YHC, real provider calls, volume deletion, pushes, PRs or merges.
- Preserve original 20 healthy/outage pairs, 5-second transition deadline and 1-second outage p95 increase; do not convert timing failures to skips.

## Context and test seams

Read the accepted `docs/superpowers/specs/2026-09-29-settlement-runtime-progress-design.md`, relevant ADR 0049/0061 and `.superpowers/sdd/local-stack-continuation-race-diagnosis.md`. Already reproduced baseline: Runtime 264/266 with Session62/Run2/Turn3 unchanged and no successor within 5s. Do not spend another full run rediscovering this before building deterministic tests.

The authorized seams are production settlement coordinator → real PostgreSQL publication/queued store; authenticated Runtime journal acceptance; generic publisher/queued public commands as compatibility negatives. Test fixtures may control external Runtime/Agent I/O, time and phase scheduling, not replace the internal stores with mocks. Prefer public result/read methods; DB queries are supplementary durable atomicity/uniqueness checks required by this design.

Progress is tracked in `.superpowers/sdd/settlement-progress-repair.md`, separate from the old web-product ledger. Missing using-git-worktrees/requesting-code-review/finishing skills are handled by reusing this verified worktree, the installed task-reviewer rubric, and local-only handoff.

### Task 1: Fenced publication and queued continuation

This is one coupled concurrency repair: both stages share settlement authority, checkpoint and graph invariants. One implementer owns production/tests throughout; do not split simultaneous writers.

**Files:**
- Modify `internal/localproduct/runtime_settlement.go` and its focused coordinator tests to carry the current leased settlement into the two effects.
- Modify `internal/artifacttrace/application/publisher.go` and `internal/artifacttrace/adapter/postgres/publisher.go`; separate settlement-specific code into `publisher_settlement.go` beside each if needed for clarity.
- Modify `internal/workerruntime/application/queued_turn.go` and `internal/workerruntime/adapter/postgres/queued_turn.go`; isolate settlement-specific entry points in `queued_turn_settlement.go`.
- Create a focused shared PostgreSQL guard under `internal/workerruntime/adapter/postgres/settlementeffect/` if sharing is necessary to avoid duplicate validation and package cycles; it must not own artifact or queue SQL.
- Add real-DB regression tests in `internal/localproduct/runtime_settlement_progress_integration_test.go` and focused publication/queued integration files, reusing existing fixtures instead of copying a complete runtime harness.

**Interfaces:**
- Consumes existing `workerapp.TurnSettlement`, `artifactapp.PublishTurnEvidenceCommand/Record/PublishedTurnEvidence`, and `workerapp.ContinueQueuedTurnCommand/Record/ContinuedQueuedTurn`.
- Produce separate settlement-owned application/store entry points receiving the current leased `TurnSettlement` (or a smaller typed reference containing its identity/revision/digest/lease fields) plus the existing effect command. The regular `Execute`, `PublishTurnEvidence`, and `ContinueQueuedTurn` must remain strict.
- Keep a shared inner transaction implementation for actual metadata/queue writes; do not duplicate the existing SQL block to implement the new entry point. Prefer typed references to unstructured maps or authorization callbacks. Exact internal names may be selected by the implementer and recorded in the report; no subsequent task consumes an invented name.

- [x] **Step 1: Publication deterministic RED.** Use the existing real coordinator/finalizer/publisher fixture; stop immediately after persisted domain commit, submit one authenticated heartbeat through the real journal, then resume publication. The real public operation must fail before the fix with `ErrEvidenceTargetStale`; no visible new publication may appear. A compiler error, SQL fixture error, or connection refusal is not behavioral RED.
- [x] **Step 2: Publication minimal GREEN.** Carry current leased settlement into publisher. Verify immutable object receipts before entering TX; inside TX lock/revalidate settlement, match the frozen publication command and graph, use only the locked Runtime revision for generic inner CAS, and preserve idempotent same-evidence lookup. Do not mutate checkpoint.Domain. Implement the following decision contract in the new transaction entry point:

```text
lock outcome/settlement; verify exact phase, revision, digest and live lease/fence
verify command evidence matches frozen checkpoint and exact scope/identity
lock necessary authority graph in compatible order
if any business revision, identity, generation, epoch or lifecycle differs: reject
if current runtime revision is below frozen runtime revision: reject
use current locked runtime revision only for this effect's CAS
publish through the existing metadata-writing implementation
commit; return exact stored publication for compatible replay
```

- [x] **Step 3: Continuation deterministic RED then GREEN.** Pause after continuation plan is durably frozen; accept an authenticated heartbeat; resume the real queued operation. Before fix expect revision conflict and no successor; after fix expect exactly one successor bound to the frozen queue command. Apply the same durable settlement/graph checks in a separate queued entry point, retaining Task/Session checks and same-transaction queue outcome/outbox. Replay after successful effect but lost phase advance must resolve the stored successor even though Runtime/Session now point at that successor; it must still prove the original frozen intent and current settlement authority.
- [x] **Step 4: Negative/replay vertical slices.** Add one failing behavioral case at a time and minimal implementation: stale lease/fence; wrong phase/state digest; mismatched evidence or queue command; cross scope; Session/Run/Turn revision mismatch; runtime identity/generation/epoch mismatch; non-active/fenced lifecycle; incomplete legacy checkpoint. Each rejects without partial effects. Add lost-response replay and two-holder/reclaim concurrency tests; legacy valid checkpoint stays supported without edits to frozen bytes.
- [x] **Step 5: Compatibility.** Run the generic stale-revision and late-generation publication/queued tests. Ensure action=none and Task delivered/conflict semantics are unchanged. Check no newly introduced reverse lock path against existing journal, settlement, Task and recovery callers; use focused real transaction competition where needed.
- [x] **Step 6: Covering gates and self-review.** Use the parent-provided isolated test URL and cache. Run focused deterministic tests repeatedly, then package integration/race and the exact original paired test. Do not run whole repo for every edit. Capture commands, exit codes and key output in the report; no raw credentials or private log dump.

```bash
GOCACHE=/private/tmp/Switchback-supplements-go-build \
go test -tags=integration ./internal/localproduct \
  -run 'Test.*Settlement.*Progress' -count=3 -v
GOCACHE=/private/tmp/Switchback-supplements-go-build \
go test -race -tags=integration ./internal/localproduct \
  -run '^TestBlockedDurableProjectionDoesNotDelayTerminalSettlementOrNextTurn$' -count=1 -v
GOCACHE=/private/tmp/Switchback-supplements-go-build \
go test -race -tags=integration ./internal/artifacttrace/... ./internal/workerruntime/... ./internal/localproduct/... -count=1
git diff --check
```

Expected: all executed tests pass; enumerate opt-in skips, never present them as acceptance. If a gate fails, diagnose the exact failure and keep the evidence; do not weaken assertions.

- [x] **Step 7: Local commit/report.** Stage only this repair's files and its named report. Report actual entry-point names, lock order, immutable/replay behavior, exact RED/GREEN commands, covering results and concerns. Do not stage parent ledger/spec/plan edits. Commit locally; no remote actions.

### Task 2: Controller verification and independent final review

**Files:** Current increment diff from recorded implementation base; `.superpowers/sdd/settlement-progress-repair.md`; accepted design/this plan only for status updates.

- [x] Generate one whole-increment review package. Independently review spec + quality, emphasizing lock order, lease fencing during external object verification, publication replay and successor replay. Resolve every real Important/Critical finding with one fix writer and rerun covering tests.
- [x] Run fresh `go test -tags=integration ./... -count=1 -json` and `go test -race ./... -count=1 -json`, using isolated PG and explicit cache. Capture distinct logs, exits, counts and SHA256. Native telemetry tests remain separate opt-ins; document skips accurately.
- [x] Run `make fmt-check migration-check generated-protocol-check secret-scan openspec-check`, `go vet ./...`, and `git diff --check`.
- [x] Only mark the repair done when original failure and new positive/negative/replay gates pass and review is clean. Otherwise fix the in-scope issue or state the real blocker. Do not mark Worker/G3/G4 complete for repairing execution.
- [x] With execution gates green, continue the already-authorized local-observability Task 2.2; keep unsafe raw log export forbidden and plan that next bounded increment from current source. Do not require another internal-document approval.
