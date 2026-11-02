# View Continuity Reader Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the split Frontier snapshot/replay authority with one `ViewContinuityReader` that returns only an anchored snapshot, a verified contiguous replay, or an existing fail-closed control.

**Architecture:** A concrete application module owns query validation, snapshot assembly, command reconciliation, continuity classification, and internal reason codes. A module-internal `ContinuityStore` seam lets the PostgreSQL adapter acquire raw View state inside short `REPEATABLE READ READ ONLY` transactions; the same reader instance is injected into Frontier HTTP, Frontier SSE, and Materializer bootstrap by `internal/localproduct`.

**Tech Stack:** Go 1.26, pgx v5.10, PostgreSQL MVCC, `net/http`, Go table-driven tests, Vitest, Playwright.

## Global Constraints

- Preserve `ProjectionSnapshot`, `ProjectionDelta`, and `ProjectionControlEvent` JSON.
- Preserve `Last-Event-ID = view_epoch:view_cursor`; transient events never receive a durable SSE ID.
- Add no migration and make no change under `api/schema/v1/`.
- Keep replay retention policy in the PostgreSQL writer; keep resume page size in `ViewContinuityReaderOptions` with default `128` and maximum `1000`.
- Use one concrete `ViewContinuityReader`; do not add a second transport-facing reader interface or retain legacy wrapper services.
- Keep the production storage adapter PostgreSQL-only; the deterministic in-memory adapter exists only in module tests.
- Use `REPEATABLE READ READ ONLY` for both snapshot-state and replay-window reads.
- Bound replay rows with `view_cursor > after_cursor` and `view_cursor <= anchored_head_cursor`.
- Validate stored identity before schema compatibility so corrupt state cannot be reported as `client_upgrade_required`.
- Return `resync_required` for recoverable cursor/version discontinuity and `ErrProjectionIntegrity` for untrustworthy stored state.
- Preserve all pre-existing user changes; stage only files named by the current task.
- PostgreSQL tests are invalid evidence when `SWITCHBACK_TEST_DATABASE_URL` is unset or the test output contains `SKIP`.

---

### Task 1: Build the application continuity module through its interface

**Files:**

- Create: `internal/eventingprojection/application/view_continuity_reader.go`
- Create: `internal/eventingprojection/application/view_continuity_reader_test.go`
- Track: `docs/superpowers/specs/2026-08-24-view-continuity-reader-design.md`
- Track: `docs/superpowers/plans/2026-08-24-view-continuity-reader.md`

**Interfaces:**

- Consumes: existing `Viewer`, `ViewProjection`, `ProjectionSnapshot`, `ProjectionDelta`, `ProjectionControlEvent`, `CommandReceipt`, and normalization helpers.
- Produces: `NewViewContinuityReader(ContinuityStore, ViewContinuityReaderOptions)`, `ReadSnapshot(context.Context, SnapshotQuery)`, and `Resume(context.Context, ResumeQuery)`.
- Produces for Task 2: `SnapshotStoreQuery`, `StoredSnapshotState`, `ReplayStoreQuery`, `StoredReplayWindow`, and `StoredViewHead`.

- [ ] **Step 1: Write the failing constructor and snapshot contract tests**

Add a deterministic raw-store adapter in `view_continuity_reader_test.go`:

```go
type continuityStoreStub struct {
	snapshotQuery SnapshotStoreQuery
	snapshotState StoredSnapshotState
	snapshotErr   error
	replayQuery   ReplayStoreQuery
	replayWindow  StoredReplayWindow
	replayErr     error
}

func (stub *continuityStoreStub) LoadSnapshotState(
	_ context.Context,
	query SnapshotStoreQuery,
) (StoredSnapshotState, error) {
	stub.snapshotQuery = query
	return stub.snapshotState, stub.snapshotErr
}

func (stub *continuityStoreStub) LoadReplayWindow(
	_ context.Context,
	query ReplayStoreQuery,
) (StoredReplayWindow, error) {
	stub.replayQuery = query
	return stub.replayWindow, stub.replayErr
}
```

Add these tests with exact assertions:

- `TestNewViewContinuityReaderValidatesDependenciesAndReplayLimit`
  - nil store fails
  - nil clock fails
  - limit `-1` and `1001` fail
  - limit `0` becomes `128`
- `TestViewContinuityReaderReadSnapshotAnchorsReceiptsAndGeneratedTime`
  - duplicate requested command IDs normalize and sort before storage
  - `ReceiptNotBefore` equals the single sampled clock minus 30 days
  - returned receipts normalize one-to-one
  - absent reconciliation produces `nil` receipts
  - present empty reconciliation produces non-nil empty receipts
- `TestViewContinuityReaderReadSnapshotRejectsUntrustedStoredState`
  - crossed tenant/user/View identity, wrong schema, zero epoch/version, conflicting receipts, zero clock, and storage error all fail with the specified sentinel.

- [ ] **Step 2: Run the focused tests and verify the red state**

Run:

```bash
go test ./internal/eventingprojection/application \
  -run 'Test(NewViewContinuityReader|ViewContinuityReaderReadSnapshot)' \
  -count=1 -v
```

Expected: compilation fails because `ContinuityStore`, the raw state types, and `NewViewContinuityReader` do not exist.

- [ ] **Step 3: Define the concrete module and raw storage seam**

Add these exact public-in-`internal` types to `view_continuity_reader.go`:

```go
var ErrProjectionIntegrity = errors.New("View projection integrity is invalid")

type ProjectionIntegrityError struct {
	Reason string
}

func (err ProjectionIntegrityError) Error() string {
	return ErrProjectionIntegrity.Error() + ": " + err.Reason
}

func (err ProjectionIntegrityError) Unwrap() error {
	return ErrProjectionIntegrity
}

const (
	DefaultProjectionReplayLimit = 128
	MaxProjectionReplayLimit     = 1000
)

type SnapshotQuery struct {
	Viewer                    Viewer
	ViewType                  string
	ViewID                    string
	ReconcileClientCommandIDs []string
}

type ResumeQuery struct {
	Viewer      Viewer
	ViewType    string
	ViewID      string
	AfterEpoch  uint64
	AfterCursor uint64
}

type ContinuityReason string

const (
	ContinuitySchemaIncompatible ContinuityReason = "schema_incompatible"
	ContinuityEpochMismatch      ContinuityReason = "epoch_mismatch"
	ContinuityCursorFuture       ContinuityReason = "cursor_future"
	ContinuityWindowEvicted      ContinuityReason = "window_evicted"
	ContinuityCursorGap          ContinuityReason = "cursor_gap"
	ContinuityVersionGap         ContinuityReason = "version_gap"
	ContinuityBehindEmpty        ContinuityReason = "behind_empty"
)

type ResumeResult struct {
	Deltas  []ProjectionDelta
	Control *ProjectionControlEvent
	Reason  ContinuityReason
}

type SnapshotStoreQuery struct {
	Viewer                    Viewer
	ViewType                  string
	ViewID                    string
	ReconcileClientCommandIDs []string
	ReceiptNotBefore          time.Time
}

type StoredSnapshotState struct {
	View            ViewProjection
	CommandReceipts []CommandReceipt
}

type ReplayStoreQuery struct {
	Viewer      Viewer
	ViewType    string
	ViewID      string
	AfterCursor uint64
	Limit       int
}

type StoredViewHead struct {
	SchemaVersion     string
	TenantID          string
	UserID            string
	ViewType          string
	ViewID            string
	ViewEpoch         uint64
	ViewCursor        uint64
	ProjectionVersion uint64
}

type StoredReplayWindow struct {
	Head           StoredViewHead
	EarliestCursor *uint64
	Deltas         []ProjectionDelta
}

type ContinuityStore interface {
	LoadSnapshotState(context.Context, SnapshotStoreQuery) (StoredSnapshotState, error)
	LoadReplayWindow(context.Context, ReplayStoreQuery) (StoredReplayWindow, error)
}

type ViewContinuityReaderOptions struct {
	Now         func() time.Time
	ReplayLimit int
}

type ViewContinuityReader struct {
	store       ContinuityStore
	now         func() time.Time
	replayLimit int
}
```

`NewViewContinuityReader` validates its dependencies once, maps limit `0` to `128`, and rejects limits outside `1..1000`. Do not expose its fields.

- [ ] **Step 4: Implement `ReadSnapshot` behind the two-operation interface**

Implement this order exactly:

1. Validate canonical viewer/View identity and supported View type.
2. Normalize reconciliation IDs only when the input slice is non-nil.
3. Sample `Now().UTC()` exactly once and reject zero.
4. Pass the normalized IDs and `generatedAt.Add(-CommandReceiptRetentionHorizon)` to `LoadSnapshotState`.
5. Validate exact stored tenant, user, View type, View ID, schema, epoch, and projection version.
6. Construct `ProjectionSnapshot` without mutating the raw state.
7. Normalize returned receipts only when reconciliation was requested.

Use `ProjectionIntegrityError` for malformed durable state; its `Reason` must be a
stable lower-snake-case code such as `stored_snapshot_identity`,
`stored_snapshot_schema`, `stored_snapshot_position`, `stored_snapshot_receipts`, or
`clock_zero`. Tests must assert both `errors.Is(err, ErrProjectionIntegrity)` and the
exact typed reason with:

```go
var integrityErr ProjectionIntegrityError
if !errors.As(err, &integrityErr) || integrityErr.Reason != wantReason {
	t.Fatalf("integrity error = %v, want reason %q", err, wantReason)
}
```

Preserve
`ErrViewUnavailable`, `ErrInvalidViewQuery`, `ErrInvalidCommandReconciliation`, and
storage errors with `%w`; do not include raw stored payloads in errors.

- [ ] **Step 5: Write the failing resume continuity matrix**

Add `TestViewContinuityReaderResumeClassifiesAnchoredWindows` as a table-driven test. Every row uses a complete `StoredViewHead` and asserts exact deltas, control, reason, or sentinel:

| Case | Raw state | Expected |
| --- | --- | --- |
| caught up | head `epoch=1,cursor=4,version=7`, after `1:4` | non-nil empty deltas |
| retained prefix | head `1:6:9`, after `1:4`, rows `5:7→8,6:8→9` | cursors `[5,6]` |
| page prefix | head `1:8:11`, after `1:4`, rows `5:7→8,6:8→9` | cursors `[5,6]` |
| old epoch | head epoch `2`, after epoch `1` | `resync_required/epoch_mismatch` |
| future cursor | head cursor `4`, after cursor `5` | `resync_required/cursor_future` |
| evicted | earliest `6`, after cursor `4` | `resync_required/window_evicted` |
| behind empty | head cursor `5`, after cursor `4`, no rows | `resync_required/behind_empty` |
| first cursor gap | first row cursor `6`, after cursor `4` | `resync_required/cursor_gap` |
| internal cursor gap | rows cursor `5,7` | `resync_required/cursor_gap` |
| first base gap | head `cursor=6,version=9`, after `4`, first base `6` | `resync_required/version_gap` |
| internal base gap | first result `8`, second base `9` | `resync_required/version_gap` |
| row identity/epoch crossover | row differs from requested View or head epoch | `resync_required/cursor_gap` |
| incompatible schema | exact identity with snapshot schema `v2` | `client_upgrade_required/schema_incompatible` |
| impossible head distance | `head cursor=8,version=2`, after cursor `4` | `ErrProjectionIntegrity` |

Also add explicit tests for invalid query, more rows than the configured limit, malformed delta schema/identity/payload, and a storage error.

- [ ] **Step 6: Run the resume matrix and verify the red state**

Run:

```bash
go test ./internal/eventingprojection/application \
  -run 'TestViewContinuityReaderResume' \
  -count=1 -v
```

Expected: resume tests fail because `Resume` and continuity classification are absent.

- [ ] **Step 7: Implement `Resume` and continuity classification**

The implementation must:

1. Validate query and call `LoadReplayWindow` with the reader-owned limit.
2. Validate head identity and non-zero epoch/version before checking schema.
3. Return the existing control schema and anchored cursor for upgrade/resync.
4. Avoid `uint64` overflow when computing `after+1`.
5. Derive the first expected base with:

```go
distance := window.Head.ViewCursor - query.AfterCursor
if window.Head.ProjectionVersion < distance {
	return ResumeResult{}, ProjectionIntegrityError{Reason: "head_version_underflow"}
}
expectedBase := window.Head.ProjectionVersion - distance
```

6. Require first cursor `after+1`, first base `expectedBase`, adjacent cursor `+1`, adjacent base equal to prior result, and every `result=base+1`.
7. Require a batch ending at the head cursor to end at the head projection version.
8. Return a non-nil empty delta slice only when caught up.
9. Return `ProjectionIntegrityError` with a stable reason such as
   `stored_head_identity`, `stored_head_position`, `head_version_underflow`,
   `replay_limit_exceeded`, or `stored_delta_invalid` for untrustworthy state.
10. Keep internal reason codes out of `ProjectionControlEvent` JSON.

- [ ] **Step 8: Run application tests and race detector**

Run:

```bash
go test ./internal/eventingprojection/application -count=1
go test -race ./internal/eventingprojection/application -count=1
```

Expected: both commands pass; the contract matrix covers every reason listed in the design.

- [ ] **Step 9: Commit the application module**

```bash
git add \
  docs/superpowers/specs/2026-08-24-view-continuity-reader-design.md \
  docs/superpowers/plans/2026-08-24-view-continuity-reader.md \
  internal/eventingprojection/application/view_continuity_reader.go \
  internal/eventingprojection/application/view_continuity_reader_test.go
git commit -m "feat: add Frontier View continuity reader"
```

### Task 2: Acquire raw PostgreSQL state from one repeatable snapshot

**Files:**

- Create: `internal/eventingprojection/adapter/postgres/continuity_reader.go`
- Create: `internal/eventingprojection/adapter/postgres/continuity_race_integration_test.go`
- Modify: `internal/testsupport/postgrestest/database.go`
- Modify: `internal/eventingprojection/adapter/postgres/replay_integration_test.go`

**Interfaces:**

- Consumes: `ContinuityStore`, `SnapshotStoreQuery`, `ReplayStoreQuery`, and raw state types from Task 1.
- Produces: `Store.LoadSnapshotState` and `Store.LoadReplayWindow`.
- Produces for Task 3: a PostgreSQL `Store` that can construct the real `ViewContinuityReader` in transport tests and production wiring.

- [ ] **Step 1: Add a traced sibling pool without changing production Store**

Add this helper to `postgrestest/database.go`:

```go
func OpenSiblingWithTracer(
	t *testing.T,
	source *pgxpool.Pool,
	tracer pgx.QueryTracer,
) *pgxpool.Pool {
	t.Helper()
	if source == nil || tracer == nil {
		t.Fatal("PostgreSQL source pool and tracer are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config := source.Config()
	config.ConnConfig.Tracer = tracer
	sibling, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open traced PostgreSQL sibling pool: %v", err)
	}
	if err := sibling.Ping(ctx); err != nil {
		sibling.Close()
		t.Fatalf("ping traced PostgreSQL sibling pool: %v", err)
	}
	t.Cleanup(sibling.Close)
	return sibling
}
```

This preserves the original pool for concurrent writes and reuses its isolated schema through the copied `AfterConnect` hook.

- [ ] **Step 2: Write two deterministic failing race tests against the current replay path**

In `continuity_race_integration_test.go`, implement a `pgx.QueryTracer` that blocks the replay delta `SELECT` in `TraceQueryStart`, records the normalized `BEGIN`, and releases through a channel. Goroutines return results through a buffered channel and never call `t.Fatal`.

Add:

- `TestViewContinuityResumeAnchorsHeadAcrossConcurrentCommit`
  - project cursors `1,2`
  - resume after `1`
  - block after head/floor reads and before the delta query
  - commit cursor `3`
  - assert the live head is `3`
  - release and require the reader result to contain only cursor `[2]`
- `TestViewContinuityResumePreservesRetentionFloorAcrossConcurrentPrune`
  - use retention limit `1`
  - project cursors `1,2` and assert only cursor `2` remains
  - resume after `1` and block before the delta query
  - commit cursor `3`, assert only cursor `3` remains live
  - release and require the reader result to contain the old cursor `[2]`

First run the semantic assertions through the existing `ReplayService`; do not change their expected cursors after migrating the harness to `ViewContinuityReader`.

- [ ] **Step 3: Run the race tests and prove the behavioral red state**

Run:

```bash
: "${SWITCHBACK_TEST_DATABASE_URL:?set a disposable PostgreSQL test URL}"
go test -tags=integration \
  ./internal/eventingprojection/adapter/postgres \
  -run 'TestViewContinuityResume(AnchorsHeadAcrossConcurrentCommit|PreservesRetentionFloorAcrossConcurrentPrune)$' \
  -count=1 -v
```

Expected: the current three-statement path returns `[2,3]` in the first test and `[3]` in the second. A compile failure or `SKIP` does not satisfy this red gate.

- [ ] **Step 4: Implement raw snapshot-state loading**

`Store.LoadSnapshotState` must:

1. Begin `pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}`.
2. Read all `ViewProjection` fields with the exact viewer/View predicate.
3. Map `pgx.ErrNoRows` to `ErrViewUnavailable`.
4. When reconciliation IDs are non-nil, read receipts in the same transaction bounded by epoch, cursor, projection version, and `ReceiptNotBefore`.
5. Return non-nil empty receipts for a present empty reconciliation request.
6. Close rows before committing and defer a background rollback for every error path.

Use the existing receipt SQL predicate unchanged; move it behind the new method rather than widening retention semantics.

- [ ] **Step 5: Implement raw replay-window loading**

Use this exact transaction shape and SQL upper bound:

```go
tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{
	IsoLevel:   pgx.RepeatableRead,
	AccessMode: pgx.ReadOnly,
})
```

```sql
SELECT schema_version,tenant_id,user_id,view_type,view_id,
       view_epoch,view_cursor,projection_version
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
```

```sql
SELECT min(view_cursor)
FROM projection_deltas
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
  AND view_epoch=$5
```

```sql
SELECT delta_id,view_type,view_id,view_epoch,view_cursor,
       base_projection_version,result_projection_version,event_type,
       critical,command_id,client_command_id,source_refs,payload,
       encoded_size_bytes
FROM projection_deltas
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
  AND view_epoch=$5
  AND view_cursor>$6
  AND view_cursor<=$7
ORDER BY view_cursor
LIMIT $8
```

Scan `min(view_cursor)` through `sql.NullInt64` and convert it to `*uint64` only when valid and non-negative. The adapter returns raw head/floor/rows; it does not construct control events or classify gaps.

- [ ] **Step 6: Migrate the race harness and replay integration tests to the real reader**

Construct once per test:

```go
reader, err := projectionapp.NewViewContinuityReader(
	projectionpostgres.NewStore(readerPool),
	projectionapp.ViewContinuityReaderOptions{
		Now:         func() time.Time { return fixedNow },
		ReplayLimit: 10,
	},
)
if err != nil {
	t.Fatal(err)
}
```

Migrate the existing eviction, stale epoch, unknown schema, and client command identity tests in `replay_integration_test.go` to `ResumeQuery`/`ResumeResult`. Add a compile-time assertion:

```go
var _ projectionapp.ContinuityStore = (*Store)(nil)
```

- [ ] **Step 7: Prove the green race behavior and repeatability**

Run:

```bash
go test -tags=integration \
  ./internal/eventingprojection/adapter/postgres \
  -run 'TestViewContinuityResume(AnchorsHeadAcrossConcurrentCommit|PreservesRetentionFloorAcrossConcurrentPrune)$' \
  -count=20

go test -race -tags=integration \
  ./internal/eventingprojection/adapter/postgres \
  -run 'TestViewContinuityResume(AnchorsHeadAcrossConcurrentCommit|PreservesRetentionFloorAcrossConcurrentPrune)$' \
  -count=10
```

Expected: no `SKIP`; both tests always return `[2]`; the tracer records `begin isolation level repeatable read read only`; writer commits complete before release, proving no reader row lock blocks writes.

- [ ] **Step 8: Run all PostgreSQL continuity tests**

```bash
go test -tags=integration \
  ./internal/eventingprojection/adapter/postgres \
  -run 'Test(ViewContinuity|Replay|SnapshotSubscribe|AssistantSnapshotSubscribe|ClientCommandIdentity)' \
  -count=1 -v
```

Expected: all selected tests pass without `SKIP`.

- [ ] **Step 9: Commit the PostgreSQL adapter**

```bash
git add \
  internal/eventingprojection/adapter/postgres/continuity_reader.go \
  internal/eventingprojection/adapter/postgres/continuity_race_integration_test.go \
  internal/eventingprojection/adapter/postgres/replay_integration_test.go \
  internal/testsupport/postgrestest/database.go
git commit -m "fix: anchor Frontier replay reads"
```

### Task 3: Route HTTP and SSE through the deep module

**Files:**

- Modify: `internal/frontier/http/handler.go`
- Modify: `internal/frontier/http/handler_test.go`
- Modify: `internal/frontier/sse/handler.go`
- Modify: `internal/frontier/sse/handler_test.go`

**Interfaces:**

- Consumes: `*ViewContinuityReader`, `SnapshotQuery`, `ResumeQuery`, and `ResumeResult` from Task 1.
- Produces for Task 4: `frontierhttp.Options.Views` and `frontiersse.Options.Views`, both accepting the same concrete reader.

- [ ] **Step 1: Replace transport fakes with raw-store fixtures and verify compile-time red**

In each handler test package, replace `fakeViewReader`/`fakeReplayReader` with a `continuityStoreStub` implementing both `ContinuityStore` methods. Construct a real reader with a fixed clock and replay limit `32`.

Preserve these exact assertions:

- HTTP 200/304 ETags, query-sensitive receipt digest, 400 reconciliation error, 404 unavailable, and 503 integrity/storage error.
- SSE `id: epoch:cursor`, control without durable ID, Assistant command causation, invalid cursor 400, and transient events without durable ID.
- Polling advances `AfterCursor` only after the real reader returns a verified delta batch.

Run:

```bash
go test ./internal/frontier/http ./internal/frontier/sse -count=1
```

Expected: compilation fails because handler options still require `SnapshotService` and `ReplayService`.

- [ ] **Step 2: Migrate the HTTP adapter**

Change its construction seam to:

```go
type Options struct {
	Viewer   projectionapp.Viewer
	Views    *projectionapp.ViewContinuityReader
	Streams  http.Handler
	Timeline projectionapp.WorkerTimelineReader
}
```

The snapshot handler creates one `SnapshotQuery`; a non-nil
`ReconcileClientCommandIDs` distinguishes a present reconciliation query, including an
empty normalized set. Keep `CommandReconciliationDigest`, ETag, cache headers, and
400/404/503 mappings unchanged.

- [ ] **Step 3: Migrate the SSE adapter**

Change its construction seam to `Views *projectionapp.ViewContinuityReader`. Replace
both initial and polling calls with `Views.Resume`. Keep `requestedCursor` in the
transport adapter, preserve the existing `writeBatch` wire behavior, and update the
next `ResumeQuery` only after non-empty verified deltas.

Do not add an SSE ID to control or transient events. A read error after headers closes
the stream without writing untrusted data.

- [ ] **Step 4: Run focused transport and race tests**

```bash
go test ./internal/frontier/http ./internal/frontier/sse -count=1 -v
go test -race ./internal/frontier/http ./internal/frontier/sse -count=1
```

Expected: both commands pass with unchanged status, ETag, and SSE payload assertions.

- [ ] **Step 5: Commit the transport migration**

```bash
git add \
  internal/frontier/http/handler.go \
  internal/frontier/http/handler_test.go \
  internal/frontier/sse/handler.go \
  internal/frontier/sse/handler_test.go
git commit -m "refactor: route Frontier reads through continuity module"
```

### Task 4: Share one reader across production wiring and Materializer bootstrap

**Files:**

- Modify: `internal/eventingprojection/adapter/postgres/materializer.go`
- Modify: `internal/eventingprojection/adapter/postgres/materializer_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/home_refresh_fencing_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/worker_detail_contract_upgrade_integration_test.go`
- Modify: `internal/localproduct/app.go`

**Interfaces:**

- Consumes: `frontierhttp.Options.Views`, `frontiersse.Options.Views`, and `*ViewContinuityReader`.
- Produces: `Materializer.Views *ViewContinuityReader` as an explicit read dependency; `Projector.Store` remains write-only.

- [ ] **Step 1: Update bootstrap tests to require explicit View reads**

For every Materializer fixture that calls `Bootstrap`, construct the reader from the
same PostgreSQL Store and the same mutable clock closure, then set `Views`:

```go
views, err := projectionapp.NewViewContinuityReader(
	store,
	projectionapp.ViewContinuityReaderOptions{Now: now, ReplayLimit: 128},
)
if err != nil {
	t.Fatal(err)
}
materializer := projectionpostgres.Materializer{
	Pool: pool, Projector: projectionapp.Projector{Store: store},
	Views: views, Objects: objects, UserID: "user-local", Now: now,
}
```

Keep the existing repeated-bootstrap assertions for TaskIndex/Home and WorkerDetail
contract upgrades. Add one dependency test proving `Bootstrap` fails before any write
when `Views` is nil.

- [ ] **Step 2: Run bootstrap tests and verify the red state**

```bash
: "${SWITCHBACK_TEST_DATABASE_URL:?set a disposable PostgreSQL test URL}"
go test -tags=integration \
  ./internal/eventingprojection/adapter/postgres \
  -run 'Test(MaterializerBootstraps|HomeDashboard|MaterializerSerializes|WorkerDetailContract)' \
  -count=1
```

Expected: compilation fails because `Materializer.Views` does not exist.

- [ ] **Step 3: Give Materializer an explicit read dependency**

Add:

```go
Views *projectionapp.ViewContinuityReader
```

to `Materializer`. Require it in `validateBootstrapDependencies`. Replace both
`Projector.Store.(ViewReader)` type assertions and temporary `SnapshotService`
constructions with `m.Views.ReadSnapshot`. Preserve `ErrViewUnavailable` as the only
condition that authorizes a bootstrap write.

- [ ] **Step 4: Construct one reader in `internal/localproduct`**

Immediately after `projectionStore` and `viewer`, construct:

```go
views, err := projectionapp.NewViewContinuityReader(
	projectionStore,
	projectionapp.ViewContinuityReaderOptions{Now: now, ReplayLimit: 128},
)
if err != nil {
	return nil, fmt.Errorf("compose Frontier View reader: %w", err)
}
```

Inject the same pointer into SSE, HTTP, and Materializer. The production dependency
order must be `Store -> ViewContinuityReader -> SSE/HTTP/Materializer -> Bootstrap`.

- [ ] **Step 5: Run production composition and bootstrap idempotency gates**

```bash
go test -count=1 -run '^$' ./internal/localproduct ./internal/eventingprojection/adapter/postgres

go test -tags=integration ./internal/localproduct \
  -run '^TestProductCompositionServesGatewayAndLiveFrontierViews$' \
  -count=1 -v

go test -tags=integration ./internal/eventingprojection/adapter/postgres \
  -run 'Test(MaterializerBootstrapsEmptyHomeDashboard|HomeDashboardMigrationRoundTripRebootsSyntheticIdentity|WorkerDetailContract)' \
  -count=1 -v
```

Expected: no `SKIP`; production HTTP/SSE serve live Views and a second Bootstrap does not advance epoch/cursor/version.

- [ ] **Step 6: Commit production wiring**

```bash
git add \
  internal/eventingprojection/adapter/postgres/materializer.go \
  internal/eventingprojection/adapter/postgres/materializer_integration_test.go \
  internal/eventingprojection/adapter/postgres/home_refresh_fencing_integration_test.go \
  internal/eventingprojection/adapter/postgres/worker_detail_contract_upgrade_integration_test.go \
  internal/localproduct/app.go
git commit -m "refactor: share Frontier continuity reader"
```

### Task 5: Migrate remaining fixtures and delete the shallow seams

**Files:**

- Modify: `docs/superpowers/plans/2026-08-24-view-continuity-reader.md`
- Modify: `internal/acceptance/vertical_slice_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/continuity_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/home_refresh_fencing_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/materializer_client_command_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/materializer_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/runtime_recovery_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/store_integration_test.go`
- Modify: `internal/eventingprojection/adapter/postgres/worker_detail_contract_upgrade_integration_test.go`
- Modify: `internal/interaction/adapter/postgres/confirmation_integration_test.go`
- Modify: `internal/interaction/adapter/postgres/store_integration_test.go`
- Modify: `internal/eventingprojection/application/snapshot.go`
- Modify: `internal/eventingprojection/application/replay.go`
- Modify: `internal/eventingprojection/adapter/postgres/store.go`

**Interfaces:**

- Consumes: the production reader and PostgreSQL adapter completed in Tasks 1–4.
- Produces: one continuity authority with no remaining `SnapshotService`, `ReplayService`, `ViewReader`, `CommandReceiptReader`, or `ReplayReader`.

- [ ] **Step 1: Compile every integration package to expose the remaining migration set**

```bash
go test -count=1 -tags=integration -run '^$' \
  ./internal/eventingprojection/... \
  ./internal/frontier/... \
  ./internal/localproduct \
  ./internal/acceptance \
  ./internal/interaction/adapter/postgres
```

Expected: failures identify every remaining old constructor or direct Store read; do not suppress them with compatibility aliases.

- [ ] **Step 2: Migrate all remaining snapshot/replay fixtures**

For each listed file, construct `ViewContinuityReader` from its existing Store and clock.
Replace:

```text
SnapshotService.Get/GetWithCommandReceipts -> ReadSnapshot(SnapshotQuery)
ReplayService.Read                      -> Resume(ResumeQuery)
Store.ReadView                          -> ReadSnapshot(SnapshotQuery)
Store.ReadCommandReceipts               -> ReadSnapshot with reconciliation IDs
Store.ReadReplay                        -> Resume(ResumeQuery)
```

Preserve each test's existing identity, cursor, command receipt, restart, and access
assertions. When a test previously called a raw Store method to inspect live state, use
the reader's public interface unless the assertion is explicitly about raw SQL storage.
Rename test identifiers that contain a deleted seam name (for example,
`TestCommandReceiptReaderExcludesReceiptsOutsideThirtyDayHorizon`) so the zero-stale-
symbol gate checks source and test vocabulary consistently.

- [ ] **Step 3: Delete the five shallow seams and old Store methods**

Remove from `snapshot.go`:

- `ViewReader`
- `CommandReceiptReader`
- `SnapshotService`

Remove from `replay.go`:

- `ReplayRequest`
- `ReplayBatch`
- `ReplayReader`
- `ReplayService`

Remove from `store.go`:

- `ReadView`
- `ReadCommandReceipts`
- `ReadReplay`
- `controlEvent`
- the three old interface assertions

Keep `ProjectionSnapshot`, `ProjectionDelta`, `ProjectionControlEvent`, receipt
normalization/digest helpers, write-side retention fields, `pruneReplay`, and the new
`ContinuityStore` assertion.

- [ ] **Step 4: Prove there is no compatibility wrapper or stale callsite**

Run:

```bash
if rg -n 'SnapshotService|ReplayService|ViewReader|CommandReceiptReader|ReplayReader|\.ReadView\(|\.ReadCommandReceipts\(|\.ReadReplay\(' \
  --glob '*.go' internal; then
  exit 1
fi
```

Expected: no output and exit status `0`.

- [ ] **Step 5: Run compile closure, focused integration, and race gates**

```bash
go test -count=1 -run '^$' ./internal/eventingprojection/... ./internal/frontier/... ./internal/localproduct

go test -count=1 -tags=integration -run '^$' \
  ./internal/eventingprojection/... \
  ./internal/frontier/... \
  ./internal/localproduct \
  ./internal/acceptance \
  ./internal/interaction/adapter/postgres

go test -tags=integration ./internal/eventingprojection/adapter/postgres ./internal/acceptance ./internal/interaction/adapter/postgres -count=1

go test -race ./internal/eventingprojection/application ./internal/frontier/http ./internal/frontier/sse -count=1
```

Expected: all packages compile; integration output contains no `SKIP`; continuity, command receipts, bootstrap, restart, and access-clear tests pass.

- [ ] **Step 6: Commit legacy deletion**

```bash
git add \
  docs/superpowers/plans/2026-08-24-view-continuity-reader.md \
  internal/acceptance/vertical_slice_integration_test.go \
  internal/eventingprojection/adapter/postgres/continuity_integration_test.go \
  internal/eventingprojection/adapter/postgres/home_refresh_fencing_integration_test.go \
  internal/eventingprojection/adapter/postgres/materializer_client_command_test.go \
  internal/eventingprojection/adapter/postgres/materializer_integration_test.go \
  internal/eventingprojection/adapter/postgres/runtime_recovery_integration_test.go \
  internal/eventingprojection/adapter/postgres/store_integration_test.go \
  internal/eventingprojection/adapter/postgres/worker_detail_contract_upgrade_integration_test.go \
  internal/interaction/adapter/postgres/confirmation_integration_test.go \
  internal/interaction/adapter/postgres/store_integration_test.go \
  internal/eventingprojection/application/snapshot.go \
  internal/eventingprojection/application/replay.go \
  internal/eventingprojection/adapter/postgres/store.go
git commit -m "refactor: remove shallow Frontier read seams"
```

### Task 6: Prove wire compatibility and full repository closure

**Files:**

- Verify only; modify a source file only when a failing gate exposes a defect within this plan's scope.

**Interfaces:**

- Consumes: the completed production path from Tasks 1–5.
- Produces: evidence that the implementation satisfies the design without migration, schema drift, skipped PostgreSQL tests, or lost browser defenses.

- [ ] **Step 1: Format and inspect the exact diff**

```bash
gofmt -w \
  internal/eventingprojection/application/view_continuity_reader.go \
  internal/eventingprojection/application/view_continuity_reader_test.go \
  internal/eventingprojection/adapter/postgres/continuity_reader.go \
  internal/eventingprojection/adapter/postgres/continuity_race_integration_test.go \
  internal/testsupport/postgrestest/database.go \
  internal/frontier/http/handler.go \
  internal/frontier/http/handler_test.go \
  internal/frontier/sse/handler.go \
  internal/frontier/sse/handler_test.go

git diff --check
git status --short
```

Expected: no whitespace errors; only planned files plus the user's pre-existing dirty files are present.

- [ ] **Step 2: Run the Go focused and deterministic database gates**

```bash
go test -v -count=1 \
  ./internal/eventingprojection/application \
  ./internal/frontier/http \
  ./internal/frontier/sse

go test -race -count=1 \
  ./internal/eventingprojection/application \
  ./internal/frontier/http \
  ./internal/frontier/sse

: "${SWITCHBACK_TEST_DATABASE_URL:?set a disposable PostgreSQL test URL}"
command -v psql
go test -v -count=1 -tags=integration ./internal/eventingprojection/adapter/postgres
```

Expected: no `SKIP`; the two barrier tests return `[2]`; all application/transport tests and the race detector pass.

- [ ] **Step 3: Run production composition and cross-module acceptance**

```bash
go test -v -count=1 -tags=integration ./internal/localproduct \
  -run '^TestProductCompositionServesGatewayAndLiveFrontierViews$'

go test -v -count=1 -tags=integration ./internal/acceptance \
  -run '^TestBackendRestartKeepsDeliveryAndProjectionWithoutDuplicateExecution$'

go test -v -count=1 -tags=integration ./internal/interaction/adapter/postgres \
  -run '^(TestAssistantConfirmationCreatesTaskAtomicallyAndIsIdempotent|TestCommitDecisionPublishesOrderedIdempotentConversationProjection)$'
```

Expected: the same reader serves HTTP/SSE/Materializer; restart and Assistant projection identity remain idempotent.

- [ ] **Step 4: Run browser wire defenses and build**

```bash
cd web
npm ci
npm test -- \
  src/state/clientViewReplica.test.ts \
  src/state/frontierViewController.test.ts \
  src/state/useFrontierView.test.tsx
npm run test:browser -- tests/continuity-flow.spec.ts
npm run build
cd ..
```

Expected: focused Vitest, five continuity browser scenarios, TypeScript checks, and Vite build all pass.

- [ ] **Step 5: Prove no migration or wire-schema change**

```bash
make migration-check
test -z "$(git status --short -- migrations api/schema/v1)"
make generated-protocol-check
```

Expected: migration/protocol checks pass and both paths have no tracked or untracked change.

- [ ] **Step 6: Run full repository closure**

```bash
make build
SWITCHBACK_TEST_DATABASE_URL="$SWITCHBACK_TEST_DATABASE_URL" make verify

cd web
npm test
npm run build
cd ..
```

Expected: build, format, migration, generated protocol, image provenance, unit, race, vet, non-skipped integration, secret scan, OpenSpec, complete Vitest, and Web build all pass.

- [ ] **Step 7: Commit any final in-scope verification repair**

If no source repair was required, skip this commit. Otherwise stage only the repaired
planned files and use:

```bash
git commit -m "test: close Frontier continuity verification"
```
