# G4 full-stack matrix implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Prove twenty full-stack healthy/outage pairs with original-source recovery, then retain actual logging loss/recovery evidence.

**Architecture:** Extend the existing controlled acceptance driver, not production execution. Keep the completed cold proof and legacy five-service report independent. Reuse authoritative timing/source loaders and the continuous Docker event collector.

**Tech Stack:** Go, PostgreSQL read-only evidence, Docker Compose, controlled ACP/model fixture, Prometheus/Loki.

## 📋 Global Constraints

- PostgreSQL and Trace JSONL/ObjectStore remain authoritative; derived telemetry must not gate execution.
- Exactly nine observation services are in scope: langfuse-web, langfuse-worker, langfuse-clickhouse, langfuse-redis, langfuse-minio, prometheus, alloy, loki, grafana.
- At least 20 controlled healthy/outage pairs; each settlement and next dispatch at most 5 seconds; outage p95 increase at most 1 second for each of the three timings.
- Preserve core PostgreSQL, Backend, resident Worker identity/generation, authoritative objects and failed evidence. Only root performs Docker operations; no paid provider, YHC or automatic recovery.
- Preserve legacy five-service defaults, cold-start-only evidence, and OpenSpec 3.2 unchecked until actual retention expiry and final gates also pass.
- Evidence must be claim-bound, bounded regular files, exclusive writes; no symlink/path escape, substituted timing, duplicate Turn or stitched subscription accepted.
- Existing approved test seams are the acceptance driver/evidence verifier, external Docker event/probe boundary, Gateway and derived query APIs. No new production test-only endpoints.

## 📦 File responsibilities

- `test/acceptance/observability/full_stack_matrix.go`: claim-bound runner and CLI orchestration.
- `test/acceptance/observability/full_stack_matrix_report.go`: pure evidence validation and standalone result.
- `test/acceptance/observability/full_stack_probes.go`: independent nine-service readiness probes.
- Corresponding `_test.go` files: external boundary/verifier behavior and negative cases.
- `service_intervals.go`: parameterized measure helper, retaining old wrapper semantics.
- `driver.go`: additive CLI dispatch only.
- `README.md`: bounded root-only invocation and claim limits.

### Task 1: Nine-service matrix runner and verifier

**Interfaces:** Reuse `matrixDriver.measureRaw`, `recoverSample`, `compose`, `supplementEvidenceSample`, `measurementFromRaw`, `fullStackObservationServices`, `startIntervalCollectionForServices`, `IntervalEvidence.validateWindowForServices`, `readFullStackInput`, and `correlationEnvironment`. Add `runFullStackMatrix(project, output, backend string) error` and `verifyFullStackMatrix(project, output string) error`; CLI modes `run-full-stack-matrix` and `verify-full-stack-matrix`.

- [x] Write a failing verifier regression at the approved evidence seam: missing ninth-service coverage and two individually incomplete subscriptions must be rejected, even if their union covers the window.
- [x] Run `GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./test/acceptance/observability -run FullStackMatrix -count=1`; retain expected RED evidence.
- [x] Parameterize the existing measurement helper without changing the old wrapper:

```go
func measureWithIntervals(ctx context.Context, project, state string, io intervalCollectorIO, measure func() (RawEvidence, error)) (RawEvidence, IntervalEvidence, error) {
    return measureWithIntervalsForServices(ctx, project, state, observationServices, io, measure)
}
```

The new helper owns one subscription per measurement, waits for handshake/readiness, collects through the final probe, joins the reader, and validates that individual subscription against all requested services. Save failed evidence too. Nine-service probes use exact project/container inspection; stopped/absent means unavailable, running alone is unknown, healthy requires successful independent bounded readiness. Reuse the existing five probes. Probe the four new services via the owned controlled fixture's Node HTTP client on its observability network (`prometheus:9090/-/ready`, `alloy:12346/-/ready`, `loki:3100/ready`, `grafana:3000/api/health`), rejecting redirects/non-200 and binding the target to its current exact inspected container. Root verifies image/runtime capabilities before physical use. Never classify network errors as absent.

- [x] Add runner: strictly validate claimed env, Backend URL and DB credentials/port/name/fallbacks (same checks as correlation); load verified `smoke-passed.json` and bind project/session/runtime/fixture. Write exclusive `full-stack-matrix-started.json`; reject rerun in a started claim. Record initial/final exact container captures. Run twenty healthy/outage pairs, each state with a five-minute bound: start/stop only the nine services, wait independently, measure two real Gateway Turns with delayed model sources, restore all services for outage, verify original sources. Save per-window interval filename and original/recovered filenames, then exclusive `full-stack-matrix-recipe.json`. Report progress once per pair. Do not invoke legacy fault/cleanup routines or restart Backend.
- [x] Add offline verifier: read only canonical claim-owned files, recompute all hashes, source comparison and timings; require twenty unique pairs with eighty unique Turns; require same original Session/Runtime/generation and stable Backend/PG/fixture identity across captures; prove no core lifecycle replacement in subscriptions. Each measurement must have one uninterrupted requested-stop subscription covering its timing window for all nine exact services. Validate completed Turn identities and timestamps against raw rows as well as journal timing; reject NaN/Inf, changed source digests, cross-claim input, failed/incomplete subscription or latency violation. Emit exclusive `full-stack-matrix-passed.json` with schema `observability.full_stack_matrix.v1`, scope `paired_outage_only`, controlled boundary, input digests, pairs and three healthy/outage p95 values. Do not call this full G4 or retention proof.
- [x] Add one regression at a time for cross-claim/duplicate Turn/source mismatch/stale core identity/latency limits, complete passing matrix, and unchanged old helper defaults. Use external HTTP/event fixtures and pure verifier input; no Docker in tests.
- [x] Run focused tests, `go test -race ./test/acceptance/observability -count=1`, `git diff --check`; document commands and results, commit only task files.

### Task 1a: Preserve failed subscription handshake evidence

**Files:** `test/acceptance/observability/service_intervals.go`, `service_intervals_test.go`.

**Interfaces:** The approved external event/probe boundary and `measureWithIntervalsForServices` returned evidence. No production changes and no relaxed timestamp, identity or subscription validation.

- [x] Write one regression through the measurement boundary: external handshake command succeeds, its complete PostgreSQL event pair lies outside the command window, and context cancellation occurs before readiness. Require retained attempted container/time bounds and an explicit handshake-events failure; measurement must never run and failed evidence must remain invalid as coverage. Run the focused test RED before implementation.
- [x] Retain an additive `handshake_attempt` in `EventSubscription` when the command returns, even when command execution fails. Keep `subscription_handshake` reserved for an accepted recorded pair. Retain a finite failure-stage marker for handshake command failure versus event matching failure. Wrap handshake errors with their stage while preserving `errors.Is` cancellation/deadline semantics. Do not widen `recordedHandshake` bounds or retry the command automatically.
- [x] Add command-failure and successful-handshake cases one at a time; verify JSON retains the failed attempt, secrets/output are absent, and existing successful evidence remains accepted. Run `GOCACHE=/private/tmp/Switchback-supplements-go-build go test ./test/acceptance/observability -count=1` and scoped race; commit only these task code files after GREEN and self-review.
- [x] Independent spec/quality review before fresh physical use. This fixes lost failure diagnostics, not the unproven historical timeout cause.

### Task 2: Root physical execution and closeout

- [x] Build current driver, verify retained immutable images match unchanged production/fixture source; create a fresh exclusive synthetic-only claim. Start owned PG/migrator/fixture/Backend; seed a real two-Turn smoke and verify original sources after starting all nine observation services. Existing cold acceptance is not repeated as a missing requirement.
- [x] Run `run-full-stack-matrix`, then `verify-full-stack-matrix`; retain all raw evidence and summarize actual timing p95/max values.
- [x] Query actual Prometheus loss counters/first/last timestamps before and after outages and exact Loki gap summaries after recovery; retain bounded raw responses, verify real increase and recovery event. Do not fabricate events or claim dropped logs were backfilled. If the current metrics cannot support exact attribution, preserve evidence and state the narrower result.
- [x] Run fresh scoped race/static/vet/OpenSpec gates, independent task and integration review; stop/remove only this claim's containers/networks and synthetic secret volume after resolving exact identity, preserve data volumes/evidence.
- [x] Record result and remaining actual retention acceptance in `.superpowers/sdd/g4-progress.md`; update stage PR only with verified same-tree publication. OpenSpec remains 5/6.
