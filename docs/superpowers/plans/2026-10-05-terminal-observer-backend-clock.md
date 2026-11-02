# Terminal observer Backend clock implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` to implement this task, followed by an independent task-scoped review.

**Goal:** Fix mixed-clock terminal diagnostics within the user-approved M1 best-effort contract, without a database migration.

**Architecture:** Run/Turn terminal facts remain authoritative PostgreSQL rows written by existing Backend services. The optional observer uses one Backend clock for startup, lookback and the query upper bound. PostgreSQL transaction visibility still excludes uncommitted/rolled-back rows; timestamps are not a strict commit-order cursor.

**Tech Stack:** Go, pgx, the existing optional telemetry runner/sender and isolated PostgreSQL integration schemas.

## 📋 Global constraints

- User explicitly approved option A: no database migration; align the Backend clock and test real terminal writes. Do not implement the previously rejected DB-clock-only proposal or immutable transaction markers.
- Keep the 60-second lookback, 1,024-entry dedup bound, 1,025-row query limit, 5-second discovery interval, 2-second sample deadline and existing 8-second wired-test deadline unchanged.
- Keep the separate maximum-two-connection pool, read-only observation queries, exact tenant/user scope filtering, optional/non-gating startup and typed-safe logging.
- No execution hooks, durable telemetry cursor/ledger, timestamp-author changes, client-selected clock, schema changes, paid model calls, YHC implementation or unrelated refactoring.
- No promises of cross-Backend clock consistency, time-jump immunity, complete late-commit recovery, strict post-start commit ordering or exactly-once logs. Startup/no-history exclusion is by Backend-authored event time, not commit time.
- Preserve all prior failure evidence, retention files and volumes. The new exclusive claim is `.superpowers/private-observability-clock1004a/claim.json`.
- Work only in `/path/to/switchback/.worktrees/observability-supplements-v1`. Root owns Docker, staging, commits and publication; do not mutate the main checkout.
- Keep OpenSpec task 3.2 unchecked and PR #2 Draft while model-capture disposition and whole-branch readiness remain outstanding.

## 📍 Diagnosis and accepted test seams

The prior real-runner failure used a direct SQL `terminal_at=now()` fixture; its DB time was 85.184 ms before the Backend startup cutoff. This proves that fixture/filter mechanism, not a general production commit-visibility defect. Current M1 terminal writers obtain `Now` from Backend services. The observer additionally compares those rows to `transaction_timestamp()`, which can exclude a valid Backend-authored timestamp when PostgreSQL is behind.

Accepted seams: `RuntimeResidencyService.CompleteStage` with the real PostgreSQL store, then the production `telemetryRunner.Run` loop and the real asynchronous sender's HTTP Loki push protocol. Existing direct observer tests remain for rollback, late-commit window, overflow and replay; their SQL fixtures must use explicit Backend timestamps rather than a different clock. Clock injection is a time-boundary test seam, not a public configuration feature.

### Task 1: Align terminal collection and add a real-write regression

**Files:**

- Modify: `internal/localproduct/runtime_telemetry.go`
- Modify: `internal/localproduct/telemetry_runner.go`
- Modify: `internal/localproduct/telemetry_worker_integration_test.go`
- Create: `internal/localproduct/telemetry_worker_clock_integration_test.go`
- Modify: `openspec/changes/local-observability-stack/design.md` (document the approved narrow clock boundary only)

**Interfaces:**

- Existing `newTelemetryRunner(Config) *telemetryRunner` remains unchanged for callers and initializes an internal `now func() time.Time` to `time.Now`.
- Internal constructor becomes `newWorkerTerminalObserver(pool *pgxpool.Pool, tenant, user string, metrics *telemetry.Metrics, emit func(telemetry.Event), now func() time.Time) *workerTerminalObserver`; every call supplies the same clock used by its terminal writer.
- `workerTerminalObserver.read(ctx context.Context, cutoff, through time.Time) ([]telemetry.Event, error)` takes the upper bound explicitly. Clock injection is set before `Run`, never mutated concurrently.
- Real writer: `workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: backendNow}.CompleteStage(ctx, command)`.

- [x] Step 1: Add internal clock wiring and a deterministic regression while leaving the old SQL upper bound unchanged. Preserve the wiring-only diff and RED output privately so failure is attributable to the old query, not compile errors or widened deadlines.

```go
// Internal time seam; production defaults to time.Now.
now func() time.Time

// Constructor initialization and sample-loop assembly:
now: time.Now
newWorkerTerminalObserver(pool, tenant, user, metrics, emit, runner.now)

// Observer construction and sample:
started: now(), now: now
through := o.now()
cutoff := through.Add(-workerTerminalWindow)
// Keep max(cutoff, started), pruning and bounded loss accounting unchanged.
events, err := o.read(ctx, cutoff, through)
```

The new test uses isolated schemas, an `httptest` Loki receiver and the existing sender. Exercise Backend ahead and behind PostgreSQL by two minutes using a calibrated, immutable clock offset; real polling/cancellation timers remain on real time. For the RED cycle run the ahead subtest only: its terminal timestamp must be later than the database transaction upper bound and must be committed by the real service, yet no matching terminal arrives before the unchanged 8-second deadline.

Seed nonterminal ownership with the existing fixture, then prepare a legal production stage before starting the observer:

```go
_, err := pool.Exec(ctx, `UPDATE worker_sessions
SET status='running',active_worker_run_id=$2
WHERE worker_session_id=$1`, id, id+"-run")
if err != nil { t.Fatal(err) }
_, err = pool.Exec(ctx, `UPDATE agent_turns
SET status='delivered',started_at=$2,deadline_at=$2+interval '24 hours'
WHERE agent_turn_id=$1`, id+"-turn", backendNow().Add(-time.Second))
if err != nil { t.Fatal(err) }

service := workerapp.RuntimeResidencyService{
    Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: backendNow,
}
_, err = service.CompleteStage(ctx, workerapp.CompleteResidentStageCommand{
    TenantID: "tenant", WorkerSessionID: id, WorkerRunID: id+"-run",
    AgentTurnID: id+"-turn", RuntimeInstanceID: id+"-runtime",
    RuntimeGeneration: 1, ExpectedSessionRevision: 1, ExpectedRunRevision: 1,
    ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
    SessionTarget: workerdomain.SessionInteractive,
    RunTarget: workerdomain.RunCompleted, TurnTarget: workerdomain.TurnCompleted,
})
if err != nil { t.Fatal(err) }
```

Use the existing sample-current readiness signal, real `Run` lifetime and HTTP receiver pattern. Assert matching safe Run and Turn events, exact scope, status and the supplied timestamp. Seed an old terminal on the same Backend clock before startup and reject its ID at the receiver. Exercise restart/no-backfill through a fresh runner. Keep receiver writes cancellation-aware so a failed assertion cannot leave shutdown blocked. Do not assert internal query strings or implementation call counts.

Run (the environment value is the dedicated synthetic-only DB):

```bash
SWITCHBACK_TEST_DATABASE_URL=postgres://fixture:clock-fixture-only@127.0.0.1:19296/switchback?sslmode=disable go test -race -tags=integration ./internal/localproduct -run '^TestTelemetryWorkerBackendClock/ahead$' -count=1 -v
```

Expected: FAIL for missing committed Run/Turn from the old database-clock upper bound; not a setup failure, skipped test, race, panic or constructor-clock mismatch.

- [x] Step 2: Replace only the database-clock upper bound and watch the same regression pass.

```go
// read's existing query tail becomes:
FROM terminals WHERE terminal_at <= $4
ORDER BY terminal_at DESC,kind,worker_run_id,agent_turn_id LIMIT 1025`,
    o.tenant, o.user, cutoff, through)
```

Default construction/startup/lookback/query ceiling all use Backend time. Do not alter the authoritative terminal writer or sampler counters. Document that new logs are still best effort, window-limited and event-time-based.

- [x] Step 3: Make the existing fixtures faithful to Backend timestamp authorship. The original runner test must use the real `CompleteStage` service instead of SQL-completing an `accepted` Turn. For rollback/replay/window fixtures bind `time.Now().UTC()` (or the same injected Backend clock), with exact 30-second and 61-second offsets; retain every existing safety/rollback/replay/overflow/scope assertion and deadline. Direct SQL remains acceptable only for testing committed/uncommitted database states, not pretending to exercise a production terminal command.

```go
terminalAt := time.Now().UTC()
_, err = tx.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=$1
WHERE agent_turn_id='window-turn'`, terminalAt.Add(-30*time.Second))
_, err = pool.Exec(ctx, `UPDATE worker_runs SET status='completed',terminal_at=$1
WHERE worker_run_id='window-run'`, terminalAt.Add(-61*time.Second))
```

- [x] Step 4: Run the fresh focused suite and local short suite; save complete commands, outputs and source fingerprints in `terminal-clock-report.md` under the private claim. The regression verdict must demonstrate both positive and negative offset cases; rollback/history/future exclusion, overflow and cancellation must remain covered.

```bash
SWITCHBACK_TEST_DATABASE_URL=postgres://fixture:clock-fixture-only@127.0.0.1:19296/switchback?sslmode=disable go test -race -tags=integration ./internal/localproduct -run '^TestTelemetry(Worker|Runner|Failure)' -count=1 -v
go test ./internal/localproduct ./internal/platform/telemetry -short -count=1
git diff --check
```

- [x] Step 5: Self-review and report. Do not stage or commit. Root will generate a task-scoped diff package, obtain independent Spec/Quality review, resolve concrete findings and freshly verify the final source before publication. Report exact limitations and any test warnings. Do not infer resolution of the separate `capture_cancelled` failure from these terminal results.

## ⚠️ Root closeout boundary

After task approval: one fresh complete `localproduct` race/integration run, whole short tests, scoped vet/static/OpenSpec checks, source-diff inspection and safe cleanup of this exact container while retaining its volume/evidence. The root records this slice's result in the existing G4 progress ledger. Publish only after verification through the existing same-tree noreply fast-forward branch routine. No Ready/merge or G4 completion is authorized merely by this task's pass.
