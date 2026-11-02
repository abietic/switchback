# Worker Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute with one implementation writer at a time. The user approved the existing observability design and delegated internal implementation/test decisions; do not request another technical-document approval.

**Goal:** Complete local-observability task 2.2: safely observe real Worker output and committed lifecycle facts, and provide authorized production diagnostic links.

**Architecture:** Backend alone discovers and verifies managed Docker Runtime identities, discards raw output, and emits bounded typed metadata through the existing optional sender. Trusted lifecycle observations come only from committed Backend facts. Diagnostic links use server-owned local browser bases and existing target authorization, without creating an execution or readiness dependency on telemetry.

**Tech Stack:** Existing Go/Moby/pgx telemetry modules and React production UI; reuse pinned dependencies and deployment profiles. No schema migration or additional durable log ledger.

## Global Constraints

- Use the existing `observability-supplements-v1` worktree. Start implementation only after settlement repair final gates pass. Preserve unrelated main-checkout changes.
- PostgreSQL and TraceManifest/ObjectStore remain authoritative. Loki, metrics and diagnostic links are derived, optional, and may not gate Backend readiness, settlement or dispatch.
- Only Backend receives Docker Socket access. No Socket/host-log-directory/provider credentials for Alloy, Grafana or Worker; no privileged or host-network test containers.
- Never export raw stdout/stderr, prompt, source, Tool payload, arbitrary error text, headers, environment or credentials. Unknown output is represented only by bounded source/byte metadata. Synthetic sentinel secrets must be absent from serialized events and native Loki results.
- Exact server-owned Session/Runtime identity, container ID, generation and immutable managed/profile labels must be verified. A resident container's creation-time Run label is not current Run authority. Do not attribute arbitrary cross-Turn process output to the current Run/Turn.
- IDs and generation remain JSON metadata, never Loki/Prometheus labels. Preserve four Loki labels: service, environment, component, level. Keep existing sender bounds: 1024 events, 4 KiB/event, 64/request, 1-second HTTP deadline, no spool/retry/backfill claim.
- Collector discovery uses the existing separate maximum-two-connection telemetry pool with a 2-second database deadline. Internal defaults: at most 10 readers, discovery every 5 seconds, authority freshness at most 7 seconds, 16 KiB discard buffers, 1 MiB maximum declared frame, at most 256 KiB/s consumed per reader, and 1-second numeric aggregation. Query at most 11 candidates to detect capacity excess without unbounded materialization. These are telemetry limits, not Task/Agent budgets. Replacement/terminality or unverifiable authority cancels the old reader; stale discovery must not indefinitely authorize collection. Cancellation interrupts rate waits and closes the actual stream; use a bounded 2-second collector shutdown and account for discarded observations.
- Committed Run/Turn semantic observations cannot be inferred by parsing Docker text. Absent duration/model data is unavailable, not zero; metric terminal counting remains derived from unique committed rows.
- Production links must revalidate exact Tenant/User/Workspace/Session and requested Run/Turn relationships through existing authorization. No client-provided destinations, arbitrary proxying, credential-bearing links, or reuse of an internal service URL as a browser base.
- No Task/lifecycle/queue/recovery policy changes, YHC enablement, real model/provider calls, data-volume deletion, pushes, PRs or merges. Preserve original 20 pairs, 5-second transition and 1-second p95-increase limits.
- This plan does not itself close G3/G4: full stack alerts, Langfuse correlation, all-services-absent cold start and actual retention expiry remain distinct acceptance work.

## Binding design and source context

Read `openspec/changes/local-observability-stack/{design.md,specs/local-observability-stack/spec.md}` and `.superpowers/sdd/worker-observability-source-notes.md`. These notes identify verified seams and the resident-container Run-label trap. Recheck actual source before edits. Current CodeGraph status reports up to date; use its symbols to narrow reads, not as completion evidence.

### Task 1: Backend-owned safe Worker observations

Completed and independently approved at `70f0db1`; all six steps below are covered in `.superpowers/sdd/worker-observability-task-1-report.md` and `/private/tmp/worker-observation-review-70f0db1.md`. Controller verified final success logs and unchanged sender/deployment/20-pair boundaries; G3/G4 remain separate.

**Files and ownership:**
- Extend `internal/platform/telemetry/event.go` and its tests for typed Runtime identity/generation and finite output-source metadata; keep schema backward-compatible and payload-free.
- Add `internal/localproduct/telemetry_worker_collector.go`, `telemetry_worker_output.go` and corresponding `_test.go` files; keep raw-byte handling separate from discovery/reader lifecycle. Put real database tests in `telemetry_worker_integration_test.go` and controlled Docker opt-in tests in `telemetry_worker_docker_integration_test.go`.
- Integrate through `internal/localproduct/telemetry_runner.go`, reusing its optional lifecycle, private metrics and capped telemetry pool. Add bounded collector availability/loss measurements in `internal/platform/telemetry/metrics.go` only as needed.
- Wire committed lifecycle observations in `internal/localproduct/runtime_telemetry.go`, using current trusted persisted facts. Any execution-side hook must be a bounded nonblocking enqueue after commit; no database/network/collector readiness wait in execution. Keep event construction outside large coordinator functions.
- Add real PostgreSQL integration and controlled Docker opt-in tests beside the collector. Update `docs/runbooks/local-observability.md` with actual limits, scope attribution, loss and no-backfill boundaries.

**Interfaces:** Consume the existing `telemetry.Event`, `Scope`, `Sender.Enqueue`, private registry and optional `telemetryRunner.Run`. Consume pinned Moby `ContainerLogs(ctx, id, ContainerLogsOptions) (io.ReadCloser, error)` and inspect APIs. New helper names are implementation-private; report exact exported additions. Task 2 does not depend on their guessed names.

- [x] **Step 1: Typed metadata RED→GREEN.** Demonstrate accepted Runtime/source metadata, rejected arbitrary source/invalid identity, and no free-text field. Extend only the finite event contract. Existing bounded-cardinality and secret-scanner tests must remain green.
- [x] **Step 2: Bounded decoder RED→GREEN.** Cover fragmented multiplex headers, stdout/stderr, TTY combined bytes, invalid/truncated frames, huge advertised lengths, stream cancellation and a credential sentinel. Do not allocate from an unchecked frame/line size; count observed bytes with fixed buffers, aggregate bounded safe events, and never pass raw bytes to the sink. The algorithm is:

```text
read a bounded header (or a fixed chunk for TTY)
validate finite source and bounded frame policy
consume bytes through a fixed-size discard buffer
accumulate only numeric observed-byte totals
flush bounded typed metadata; reset totals
on invalid input/cancel: close reader and disclose the bounded failure/loss reason
```

- [x] **Step 3: Authority/lifecycle RED→GREEN.** Select only current configured actor/tenant candidates, cap candidates and concurrent readers, inspect exact server-verified container labels, then follow with `Tail: "0"`, `Follow: true`, stdout+stderr enabled and no Docker timestamp/details text export. Periodically revalidate authority under the query deadline. Test generation replacement, same-container later Run, Session terminality, label mismatch, failed/stale discovery, capacity excess, blocked reads and shutdown. Failures degrade telemetry only. Test that old-generation cancellation closes the real read boundary, not just removes an entry from a map.
- [x] **Step 4: Committed semantic events RED→GREEN.** Exercise actual committed Run/Turn terminal facts through production wiring. Assert exact scope and finite outcome, no event for rolled-back/uncommitted state, no raw content, and unchanged snapshot metric counting on replay. Keep any best-effort duplicate/restart/loss behavior explicit; do not add a second authority or telemetry ledger to promise exactly-once logs.
- [x] **Step 5: Real seams.** Run PostgreSQL authority/replacement tests and a controlled owned Docker output fixture using an existing verified pinned image. Native collection must pass through the production collector and sender. A synthetic secret emitted on stdout/stderr must never be serialized; a replacement must stop the old reader. Preserve retained volumes and core services; no model execution is required for this fixture.
- [x] **Step 6: Covering gates and local report/commit.** Run focused tests, telemetry/localproduct integration+races, and the unchanged original 20-pair test. Record exact finite limits, lifecycle-event source, failures/skips, native boundaries and commands in the named report. Commit only Task 1 files; leave parent ledger and Task 2 files alone.

```bash
GOCACHE=/private/tmp/Switchback-supplements-go-build go test -race ./internal/platform/telemetry/... -count=1
GOCACHE=/private/tmp/Switchback-supplements-go-build go test -race -tags=integration ./internal/localproduct -run 'Telemetry|Observability|BlockedDurableProjection' -count=1
git diff --check
```

Use the parent-provided task-owned PostgreSQL URL. Native Docker tests must have an explicit opt-in and report skipped runs as skipped, not physical proof. Review this task before proceeding to links.

### Task 2: Authorized production diagnostic links

**Files and ownership:**
- Extend server-owned diagnostic configuration in `internal/localproduct/config.go` and `deploy/local/compose.yaml` using actual existing published local ports; update any referenced environment template only after locating its actual path.
- Add a focused diagnostic read route beside `internal/frontier/http/handler.go`, wired from `internal/localproduct/app.go`. Reuse the current viewer and target read authorization; keep network calls to Grafana/Langfuse out of this route.
- Add a small diagnostic client/component used by `web/src/features/worker/WorkerDetailPage.tsx` and its tests. Use `web/src/api/contracts.ts` for the actual response contract; do not modify `web-demo` or the durable projection protocol merely to add navigation.
- Update the runbook for configured/disabled/unavailable links and local operator access boundaries.

**Interfaces:** Existing Frontier `Viewer` and the server-configured M1 Workspace define the current local authorization boundary. Verify current Session/Run/Turn rows, not only the stale-capable projection/timeline. Current source has no dynamic Membership/RBAC service (ADR 0058); do not invent one for navigation. Deny missing or now out-of-scope targets and test changed server scope; do not claim dynamic revocation coverage. Authorized historical closed/expired Sessions remain readable. The route produces fixed-kind links (`grafana`, `logs`, optional `langfuse`) for an exact authorized target, or an empty configured-availability result; user input never chooses the destination base. Exact response/helper names are internal implementation choices and must be reported before the UI consumes them in this same task.

- [x] **Step 1: Authorization/configuration RED→GREEN.** Test allowed exact target, cross Tenant/User/Workspace, Run/Turn from another Session, changed current server scope or unavailable target despite a stale projection, disabled config, malformed/credential-bearing/nonlocal destinations and escaping. Reuse the existing M1 fixed-profile authorization boundary rather than trusting possession of an ID or URL; dynamic Membership revocation remains deferred by ADR 0058.
- [x] **Step 2: Deterministic URL construction.** Build only from validated server-local browser bases and verified scope/time values. Use URL/JSON encoders, preserve the proven explicit JSON-path LogQL alias convention, and omit unavailable Langfuse links rather than inventing project/trace identity. No remote request is needed to build a link.

```text
authorize actual viewer + exact Worker target
verify requested Run/Turn belong to that authorized Session
load server-owned local diagnostic bases
encode fixed destination + verified scope/time parameters
return finite link kinds; never proxy or accept a target URL
```

- [x] **Step 3: Production UI.** Add compact links in existing Worker diagnostics/metadata UI, independently of mutation controls and readiness. Test configured links, disabled/unavailable state, request failure, no stale link after target change, and safe external navigation. Do not redesign the page.
- [x] **Step 4: Gates/review.** Run focused backend authorization tests, production frontend tests/build and diff checks. Independent review must cover target authorization and URL injection. Record actual commands/results and commit the task locally.

### Task 3: Controller closeout for task 2.2

- [x] Inspect both reports and whole-increment diff; resolve every real Important/Critical review finding with one writer and covering tests.
- [x] Run fresh whole integration, standard race, relevant frontend gates, static repository checks and strict OpenSpec on final source. Preserve exact skip and physical-evidence boundaries.
- [x] Mark only OpenSpec task 2.2 complete when its implementation, production wiring, scoped tests and independent review are complete. Record remaining G3/G4 work explicitly; do not infer physical stack/retention completion from unit tests or configuration.
