//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMaterializerSerializesHomeRefreshWithOneSnapshot(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC)
	seedHomeFenceTask(t, pool, now)
	store := NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}
	if err := base.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}

	aObserved := make(chan struct{})
	releaseA := make(chan struct{})
	var observedOnce sync.Once
	materializerA := base
	materializerA.homeRefreshObserved = func() {
		observedOnce.Do(func() { close(aObserved) })
		<-releaseA
	}
	eventA := homeFenceEvent("home-fence-event-a", "task/home-fence-a", now.Add(time.Hour))
	eventB := homeFenceEvent("home-fence-event-b", "task/home-fence-b", now.Add(2*time.Hour))

	aErrors := make(chan error, 1)
	go func() { aErrors <- materializerA.Publish(ctx, eventA) }()
	select {
	case <-aObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("event A did not reach the controlled Home snapshot boundary")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE tasks
SET task_spec_snapshot=jsonb_build_object('title','Fence new'),
    updated_at=$2
WHERE task_id=$1`, "task-home-fence", now.Add(2*time.Hour)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE worker_session_continuity_quotas
SET reserved_count=2,revision=revision+1,updated_at=$3
WHERE tenant_id=$1 AND user_id=$2`, "tenant-local", "user-local", now.Add(2*time.Hour)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	bErrors := make(chan error, 1)
	type lockAttempt struct {
		pid uint32
		key string
	}
	bLockAttempted := make(chan lockAttempt, 1)
	materializerB := base
	materializerB.homeRefreshLockAttempt = func(pid uint32, key string) {
		bLockAttempted <- lockAttempt{pid: pid, key: key}
	}
	go func() { bErrors <- materializerB.Publish(ctx, eventB) }()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseA) }) }
	defer release()
	var attempted lockAttempt
	select {
	case attempted = <-bLockAttempted:
	case <-time.After(5 * time.Second):
		t.Fatal("event B did not attempt the Home advisory lock")
	}
	const expectedLockKey = "home_dashboard:12:tenant-local:10:user-local"
	if attempted.key != expectedLockKey {
		t.Fatalf("event B Home lock key=%q, want %q", attempted.key, expectedLockKey)
	}
	waitForHomeFenceWaiter(t, pool, attempted.pid, attempted.key)
	select {
	case err := <-bErrors:
		t.Fatalf("event B completed before event A released the Home fence: %v", err)
	default:
	}
	release()
	if err := <-aErrors; err != nil {
		t.Fatalf("publish event A: %v", err)
	}
	if err := <-bErrors; err != nil {
		t.Fatalf("publish event B: %v", err)
	}

	clock = now.Add(3 * time.Hour)
	home, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewHomeDashboard,
		ViewID:   HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		AttentionTasks []struct {
			Title string `json:"title"`
		} `json:"attention_tasks"`
		Capacity struct {
			Used int `json:"used"`
		} `json:"capacity"`
	}
	if err := json.Unmarshal(home.Data, &dashboard); err != nil {
		t.Fatal(err)
	}
	var eventAResult []projectionapp.ViewProjection
	if err := pool.QueryRow(ctx, `
SELECT projection_result
FROM projection_event_receipts
WHERE source_event_id=$1`,
		"local-home-dashboard-refresh/"+eventA.EventID,
	).Scan(&eventAResult); err != nil {
		t.Fatal(err)
	}
	if len(eventAResult) != 1 {
		t.Fatalf("event A Home projection result=%+v", eventAResult)
	}
	var eventADashboard struct {
		AttentionTasks []struct {
			Title string `json:"title"`
		} `json:"attention_tasks"`
		Capacity struct {
			Used int `json:"used"`
		} `json:"capacity"`
	}
	if err := json.Unmarshal(eventAResult[0].Data, &eventADashboard); err != nil {
		t.Fatal(err)
	}
	if len(eventADashboard.AttentionTasks) != 1 ||
		eventADashboard.AttentionTasks[0].Title != "Fence old" ||
		eventADashboard.Capacity.Used != 1 {
		t.Fatalf("event A Home mixed snapshots: %s", eventAResult[0].Data)
	}
	if len(dashboard.AttentionTasks) != 1 ||
		dashboard.AttentionTasks[0].Title != "Fence new" ||
		dashboard.Capacity.Used != 2 {
		t.Fatalf("final Home is stale or mixed: %s", home.Data)
	}
}

func TestBootstrapUpgradesLegacyHomeDeliveryContractOnceWithoutChangingV1Receipt(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	bootstrapAt := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	seedHomeFenceTask(t, pool, now)
	if _, err := pool.Exec(ctx, `
UPDATE tasks
SET status='delivered', updated_at=$1
WHERE task_id='task-home-fence'`, now); err != nil {
		t.Fatal(err)
	}
	store := NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 8},
	)
	if err != nil {
		t.Fatal(err)
	}
	materializer := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}

	attention, err := materializer.homeAttentionTasks(ctx, pool, "tenant-local")
	if err != nil {
		t.Fatal(err)
	}
	workers, err := materializer.homeRecentWorkers(ctx, pool, "tenant-local")
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := materializer.homeRecentDeliveries(ctx, pool, "tenant-local")
	if err != nil {
		t.Fatal(err)
	}
	legacyDeliveries := make([]homeDeliveryItemV1Fixture, 0, len(deliveries))
	for _, delivery := range deliveries {
		legacyDeliveries = append(legacyDeliveries, homeDeliveryItemV1Fixture{
			homeTaskItem: delivery.homeTaskItem,
			Artifacts:    delivery.Artifacts,
		})
	}
	legacyData, err := marshalObject(map[string]any{
		"attention_tasks":   attention,
		"recent_workers":    workers,
		"recent_deliveries": legacyDeliveries,
		"capacity":          map[string]any{"used": 1, "limit": 10},
		"updated_at":        bootstrapAt.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, refs, err := materializer.homeDashboardAt(ctx, pool, "tenant-local", bootstrapAt)
	if err != nil {
		t.Fatal(err)
	}
	legacyEvent := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       homeDashboardBootstrapEventID, EventType: "LocalHomeDashboardBootstrap.v1",
		ProducerContext: "eventing-projection", StreamID: homeDashboardBootstrapStreamID,
		Sequence: 1, TenantID: "tenant-local", AggregateType: "HomeDashboard",
		AggregateID: HomeDashboardViewID, TraceID: homeDashboardBootstrapStreamID,
		CorrelationID: "local-product-startup", CausationID: "local-product-startup",
		IdempotencyKey: homeDashboardBootstrapEventID, Visibility: "internal",
		OccurredAt: bootstrapAt, Payload: json.RawMessage(`{"bootstrap":true}`),
	}
	legacyProjection := projectionapp.ProjectionEvent{
		SourceEventID: legacyEvent.EventID, SourceStreamID: legacyEvent.StreamID,
		SourceSequence: legacyEvent.Sequence, ProjectedAt: legacyEvent.OccurredAt,
		Updates: []projectionapp.ViewUpdate{
			materializer.update(
				legacyEvent, projectionapp.ViewHomeDashboard,
				HomeDashboardViewID, legacyData, nil, refs,
			),
		},
	}
	legacyDigest, err := projectionapp.ProjectionEventDigest(legacyProjection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Projector.Execute(ctx, legacyProjection); err != nil {
		t.Fatal(err)
	}

	viewer := projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}
	before, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard, ViewID: HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.ViewEpoch != 1 || before.ViewCursor != 1 || before.ProjectionVersion != 1 {
		t.Fatalf("legacy Home identity=%+v, want epoch/cursor/version 1", before)
	}

	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}
	after, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard, ViewID: HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		RecentDeliveries []struct {
			TaskID         string `json:"task_id"`
			AcceptanceMode string `json:"acceptance_mode"`
		} `json:"recent_deliveries"`
	}
	if err := json.Unmarshal(after.Data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if len(dashboard.RecentDeliveries) != 1 ||
		dashboard.RecentDeliveries[0].TaskID != "task-home-fence" ||
		dashboard.RecentDeliveries[0].AcceptanceMode != "manual" {
		t.Fatalf("upgraded Home deliveries=%+v data=%s", dashboard.RecentDeliveries, after.Data)
	}
	if after.ViewEpoch != before.ViewEpoch ||
		after.ViewCursor != before.ViewCursor+1 ||
		after.ProjectionVersion != before.ProjectionVersion+1 {
		t.Fatalf(
			"upgraded Home identity epoch/cursor/version=%d/%d/%d, want %d/%d/%d",
			after.ViewEpoch, after.ViewCursor, after.ProjectionVersion,
			before.ViewEpoch, before.ViewCursor+1, before.ProjectionVersion+1,
		)
	}
	var storedV1Digest string
	if err := pool.QueryRow(ctx, `
SELECT event_digest
FROM projection_event_receipts
WHERE source_event_id=$1`, homeDashboardBootstrapEventID).Scan(&storedV1Digest); err != nil {
		t.Fatal(err)
	}
	if storedV1Digest != legacyDigest {
		t.Fatalf("v1 bootstrap digest=%q, want legacy digest %q", storedV1Digest, legacyDigest)
	}
	replay, err := views.Resume(ctx, projectionapp.ResumeQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard,
		ViewID: HomeDashboardViewID, AfterEpoch: before.ViewEpoch,
		AfterCursor: before.ViewCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Control != nil || len(replay.Deltas) != 1 ||
		replay.Deltas[0].ViewCursor != after.ViewCursor {
		t.Fatalf("Home upgrade replay=%+v", replay)
	}

	clock = now.Add(time.Hour)
	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatalf("repeat bootstrap: %v", err)
	}
	restarted, err := views.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewHomeDashboard, ViewID: HomeDashboardViewID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.ViewEpoch != after.ViewEpoch ||
		restarted.ViewCursor != after.ViewCursor ||
		restarted.ProjectionVersion != after.ProjectionVersion {
		t.Fatalf("restart rewrote upgraded Home: before=%+v after=%+v", after, restarted)
	}
	var upgradeReceipts int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM projection_event_receipts
WHERE left(source_event_id,length($1))=$1`,
		"local-home-dashboard-contract-upgrade-v2/",
	).Scan(&upgradeReceipts); err != nil {
		t.Fatal(err)
	}
	if upgradeReceipts != 1 {
		t.Fatalf("Home contract upgrade receipts=%d, want 1", upgradeReceipts)
	}

	_, down := homeFenceMigration(t)
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatalf("migrate Home contract upgrade down: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM projection_event_receipts
WHERE left(source_event_id,length($1))=$1`,
		"local-home-dashboard-contract-upgrade-v2/",
	).Scan(&upgradeReceipts); err != nil {
		t.Fatal(err)
	}
	if upgradeReceipts != 0 {
		t.Fatalf("Home contract upgrade receipts after down=%d, want 0", upgradeReceipts)
	}
}

type homeDeliveryItemV1Fixture struct {
	homeTaskItem
	Artifacts []map[string]any `json:"artifacts"`
}

func TestMaterializerDoesNotRefreshHomeForTerminalLegacyReceipt(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	store := NewStore(pool)
	event := homeFenceEvent(
		"home-legacy-terminal-owner",
		"task/home-legacy-terminal-owner",
		now,
	)
	legacyData := json.RawMessage(`{"legacy":true}`)
	if _, err := (projectionapp.Projector{Store: store}).Execute(
		ctx,
		projectionapp.ProjectionEvent{
			SourceEventID: event.EventID, SourceStreamID: event.StreamID,
			SourceSequence: event.Sequence, ProjectedAt: now,
			Updates: []projectionapp.ViewUpdate{{
				DeltaID:  event.EventID + "-legacy-task",
				TenantID: event.TenantID, UserID: "user-local",
				ViewType: projectionapp.ViewTaskSummary, ViewID: "legacy-terminal",
				QueryFingerprint: "single-user-local-v1", Data: legacyData,
				AvailableActions: []projectionapp.ActionRef{},
				RelatedRefs:      []projectionapp.RelatedRef{},
				EventType:        "view.entity_upserted.v1", Critical: true,
				SourceRefs: []projectionapp.SourceRef{{
					Type: "integration_event", ID: event.EventID,
				}},
				DeltaPayload: json.RawMessage(
					`{"data":{"legacy":true},"available_actions":[],"related_refs":[]}`,
				),
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	var legacyReceiptHasNullDigest bool
	if err := pool.QueryRow(ctx, `
SELECT source_event_digest IS NULL
FROM projection_event_receipts
WHERE source_event_id=$1`, event.EventID).Scan(&legacyReceiptHasNullDigest); err != nil {
		t.Fatal(err)
	}
	if !legacyReceiptHasNullDigest {
		t.Fatal("legacy owner receipt unexpectedly has a source digest")
	}

	materializer := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		UserID: "user-local", Now: func() time.Time { return now.Add(time.Hour) },
	}
	if err := materializer.Publish(ctx, event); err != nil {
		t.Fatalf("publish terminal legacy owner receipt: %v", err)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: func() time.Time { return now.Add(2 * time.Hour) }},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: event.TenantID, UserID: "user-local"},
		ViewType: projectionapp.ViewHomeDashboard,
		ViewID:   HomeDashboardViewID,
	}); !errors.Is(err, projectionapp.ErrViewUnavailable) {
		t.Fatalf("terminal legacy owner created Home snapshot: %v", err)
	}
	var homeDeltas, syntheticReceipts, syntheticStreams int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM projection_deltas WHERE view_type='home_dashboard'),
  (SELECT count(*) FROM projection_event_receipts
     WHERE left(source_event_id,length('local-home-dashboard-refresh/'))
           = 'local-home-dashboard-refresh/'),
  (SELECT count(*) FROM projection_source_streams
     WHERE left(source_stream_id,length('local-home-dashboard-refresh/'))
           = 'local-home-dashboard-refresh/')`,
	).Scan(&homeDeltas, &syntheticReceipts, &syntheticStreams); err != nil {
		t.Fatal(err)
	}
	if homeDeltas != 0 || syntheticReceipts != 0 || syntheticStreams != 0 {
		t.Fatalf(
			"terminal legacy owner created Home state deltas=%d receipts=%d streams=%d",
			homeDeltas,
			syntheticReceipts,
			syntheticStreams,
		)
	}
}

func TestMaterializerDiscardsCancelledHomeLockWaiter(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	seedHomeFenceTask(t, pool, now)
	store := NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}
	if err := base.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}

	aObserved := make(chan struct{})
	releaseA := make(chan struct{})
	var observedOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseA) }) }
	defer release()
	materializerA := base
	materializerA.homeRefreshObserved = func() {
		observedOnce.Do(func() { close(aObserved) })
		<-releaseA
	}
	aErrors := make(chan error, 1)
	go func() {
		aErrors <- materializerA.Publish(
			ctx,
			homeFenceEvent("home-cancel-event-a", "task/home-cancel-a", now.Add(time.Hour)),
		)
	}()
	select {
	case <-aObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("event A did not hold the Home refresh fence")
	}

	type lockAttempt struct {
		pid uint32
		key string
	}
	bLockAttempted := make(chan lockAttempt, 1)
	materializerB := base
	materializerB.homeRefreshLockAttempt = func(pid uint32, key string) {
		bLockAttempted <- lockAttempt{pid: pid, key: key}
	}
	bCtx, cancelB := context.WithCancel(ctx)
	bErrors := make(chan error, 1)
	go func() {
		bErrors <- materializerB.Publish(
			bCtx,
			homeFenceEvent("home-cancel-event-b", "task/home-cancel-b", now.Add(2*time.Hour)),
		)
	}()
	var attempted lockAttempt
	select {
	case attempted = <-bLockAttempted:
	case <-time.After(5 * time.Second):
		t.Fatal("event B did not attempt the Home advisory lock")
	}
	waitForHomeFenceWaiter(t, pool, attempted.pid, attempted.key)
	cancelB()
	select {
	case err := <-bErrors:
		if err == nil {
			t.Fatal("cancelled event B unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled event B did not return")
	}
	waitForBackendClosed(t, pool, attempted.pid)

	release()
	select {
	case err := <-aErrors:
		if err != nil {
			t.Fatalf("publish event A: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event A did not complete after releasing the Home fence")
	}
}

func TestMaterializerDiscardsConnectionAfterHomeLockAcquisitionFailure(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	now := time.Date(2026, time.July, 28, 9, 30, 0, 0, time.UTC)
	seedHomeFenceTask(t, pool, now)
	store := NewStore(pool)
	clock := now
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}
	if err := base.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}

	var failedPID uint32
	failing := base
	failing.homeRefreshLockAcquired = func(pid uint32, _ string) error {
		failedPID = pid
		return errors.New("injected post-acquire failure")
	}
	err = failing.Publish(
		ctx,
		homeFenceEvent("home-acquire-failure", "task/home-acquire-failure", now.Add(time.Hour)),
	)
	if err == nil || !strings.Contains(err.Error(), "injected post-acquire failure") {
		t.Fatalf("post-acquire failure=%v", err)
	}
	waitForBackendClosed(t, pool, failedPID)

	completed := make(chan error, 1)
	go func() {
		completed <- base.Publish(
			ctx,
			homeFenceEvent("home-after-acquire-failure", "task/home-after-acquire-failure", now.Add(2*time.Hour)),
		)
	}()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("publish after discarded lock connection: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Home refresh remained blocked after lock acquisition failure")
	}
}

func waitForHomeFenceWaiter(t *testing.T, pool *pgxpool.Pool, pid uint32, lockKey string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
WITH target AS (
  SELECT hashtextextended($2,0) AS lock_id
)
SELECT EXISTS (
  SELECT 1
  FROM pg_locks AS waiter
  CROSS JOIN target
  WHERE waiter.pid=$1
    AND waiter.locktype='advisory'
    AND waiter.classid=((target.lock_id >> 32) & 4294967295)::oid
    AND waiter.objid=(target.lock_id & 4294967295)::oid
    AND waiter.objsubid=1
    AND NOT waiter.granted
)`, pid, lockKey).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("event B did not wait on event A's Home advisory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForBackendClosed(t *testing.T, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var exists bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid=$1)`, pid).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled Home waiter backend pid %d remained pooled", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHomeMigrationRollbackPreservesOldOwnerProjectionReplay(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	firstNow := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	replayNow := firstNow.Add(24 * time.Hour)
	seedHomeFenceTask(t, pool, firstNow)
	store := NewStore(pool)
	clock := firstNow
	nowFn := func() time.Time { return clock }
	views, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{Now: nowFn, ReplayLimit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	materializer := Materializer{
		Pool: pool, Projector: projectionapp.Projector{Store: store},
		Views: views, UserID: "user-local", Now: nowFn,
	}
	if err := materializer.Bootstrap(ctx, "tenant-local"); err != nil {
		t.Fatal(err)
	}

	acked := homeFenceEvent(
		"home-rollback-acked", "task/home-rollback", firstNow.Add(time.Hour),
	)
	unpublished := homeFenceEvent(
		"home-rollback-unpublished", acked.StreamID, firstNow.Add(2*time.Hour),
	)
	unpublished.Sequence = 2
	seedHomeFenceOutboxEvent(t, pool, acked, true)
	seedHomeFenceOutboxEvent(t, pool, unpublished, false)
	if err := materializer.Publish(ctx, acked); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Publish(ctx, unpublished); err != nil {
		t.Fatal(err)
	}

	sharedRefUpdate := materializer.update(
		unpublished,
		projectionapp.ViewCommandStatus,
		"shared-ref-retained",
		json.RawMessage(`{"status":"retained"}`),
		nil,
		nil,
	)
	sharedRefUpdate.DeltaID = "home-rollback-shared-ref-retained"
	if _, err := materializer.Projector.Execute(ctx, projectionapp.ProjectionEvent{
		SourceEventID:  "home-rollback-shared-ref-owner",
		SourceStreamID: "home-rollback-shared-ref-owner",
		SourceSequence: 1,
		ProjectedAt:    firstNow.Add(3 * time.Hour),
		Updates:        []projectionapp.ViewUpdate{sharedRefUpdate},
	}); err != nil {
		t.Fatal(err)
	}

	lookalikeID := homeDashboardRefreshSourceIDPrefix + "lookalike"
	lookalikeEvent := unpublished
	lookalikeEvent.EventID = lookalikeID
	lookalikeEvent.StreamID = lookalikeID
	lookalikeUpdate := materializer.update(
		lookalikeEvent,
		projectionapp.ViewCommandStatus,
		"prefix-lookalike",
		json.RawMessage(`{"status":"retained"}`),
		nil,
		nil,
	)
	if _, err := materializer.Projector.Execute(ctx, projectionapp.ProjectionEvent{
		SourceEventID:  lookalikeID,
		SourceStreamID: lookalikeID,
		SourceSequence: 1,
		ProjectedAt:    firstNow.Add(4 * time.Hour),
		Updates:        []projectionapp.ViewUpdate{lookalikeUpdate},
	}); err != nil {
		t.Fatal(err)
	}

	oldUpdates, err := materializer.buildUpdates(ctx, unpublished)
	if err != nil {
		t.Fatal(err)
	}
	oldUpdates = withoutHomeUpdate(oldUpdates)
	oldReplay := projectionapp.ProjectionEvent{
		SourceEventID: unpublished.EventID, SourceStreamID: unpublished.StreamID,
		SourceSequence: unpublished.Sequence, ProjectedAt: replayNow, Updates: oldUpdates,
	}
	firstOldShape := oldReplay
	firstOldShape.ProjectedAt = firstNow
	wantOwnerDigest, err := projectionapp.ProjectionEventDigest(firstOldShape)
	if err != nil {
		t.Fatal(err)
	}
	var storedOwnerDigest string
	if err := pool.QueryRow(ctx, `
SELECT event_digest
FROM projection_event_receipts
WHERE source_event_id=$1`, unpublished.EventID).Scan(&storedOwnerDigest); err != nil {
		t.Fatal(err)
	}
	if storedOwnerDigest != wantOwnerDigest {
		t.Fatalf("ordinary owner digest=%q, want pre-Home shape %q",
			storedOwnerDigest, wantOwnerDigest)
	}
	var priorViewEpoch, priorViewCursor, priorProjectionVersion uint64
	if err := pool.QueryRow(ctx, `
SELECT view_epoch,view_cursor,projection_version
FROM view_projections
WHERE tenant_id='tenant-local' AND user_id='user-local'
  AND view_type='task_summary' AND view_id='index'`).Scan(
		&priorViewEpoch, &priorViewCursor, &priorProjectionVersion,
	); err != nil {
		t.Fatal(err)
	}

	_, down := homeFenceMigration(t)
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatalf("migrate Home down: %v", err)
	}
	var ackedReceipts int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM projection_event_receipts
WHERE source_event_id=$1`, acked.EventID).Scan(&ackedReceipts); err != nil {
		t.Fatal(err)
	}
	if ackedReceipts != 1 {
		t.Fatalf("rollback removed acknowledged owner receipt count=%d", ackedReceipts)
	}
	var unpublishedReceipts, syntheticHomeRefreshReceipts int
	var lastSourceSequence int64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM projection_event_receipts WHERE source_event_id=$1),
  (SELECT count(*) FROM projection_event_receipts
     WHERE left(source_event_id,length($3)) = $3
       AND source_event_id = source_stream_id
       AND source_sequence = 1
       AND projection_result @> '[{"view_type":"home_dashboard","view_id":"index"}]'::jsonb),
  (SELECT last_source_sequence FROM projection_source_streams WHERE source_stream_id=$2)`,
		unpublished.EventID, unpublished.StreamID, homeDashboardRefreshSourceIDPrefix,
	).Scan(&unpublishedReceipts, &syntheticHomeRefreshReceipts, &lastSourceSequence); err != nil {
		t.Fatal(err)
	}
	if unpublishedReceipts != 0 {
		t.Fatalf("rollback retained unpublished owner receipt count=%d", unpublishedReceipts)
	}
	if syntheticHomeRefreshReceipts != 0 {
		t.Fatalf("rollback retained Home synthetic receipts count=%d", syntheticHomeRefreshReceipts)
	}
	if lastSourceSequence != int64(acked.Sequence) {
		t.Fatalf("rollback source watermark=%d, want acknowledged sequence %d",
			lastSourceSequence, acked.Sequence)
	}
	var rolledBackEpoch, rolledBackCursor, rolledBackVersion uint64
	if err := pool.QueryRow(ctx, `
SELECT view_epoch,view_cursor,projection_version
FROM view_projections
WHERE tenant_id='tenant-local' AND user_id='user-local'
  AND view_type='task_summary' AND view_id='index'`).Scan(
		&rolledBackEpoch, &rolledBackCursor, &rolledBackVersion,
	); err != nil {
		t.Fatal(err)
	}
	if rolledBackEpoch != priorViewEpoch+1 || rolledBackCursor != 0 || rolledBackVersion != 0 {
		t.Fatalf(
			"rollback View identity epoch=%d cursor=%d version=%d, want epoch=%d cursor=0 version=0",
			rolledBackEpoch, rolledBackCursor, rolledBackVersion, priorViewEpoch+1,
		)
	}
	ackedDeltaID := acked.EventID + "-task_summary-index"
	var ackedDeltas int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM projection_deltas WHERE delta_id=$1`, ackedDeltaID).Scan(&ackedDeltas); err != nil {
		t.Fatal(err)
	}
	if ackedDeltas != 1 {
		t.Fatalf("rollback removed acknowledged delta count=%d", ackedDeltas)
	}
	unpublishedDeltaID := unpublished.EventID + "-task_summary-index"
	var unpublishedDeltas, sharedRefDeltas, lookalikeReceipts, lookalikeStreams int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM projection_deltas WHERE delta_id=$2),
  (SELECT count(*) FROM projection_deltas WHERE delta_id='home-rollback-shared-ref-retained'),
  (SELECT count(*) FROM projection_event_receipts WHERE source_event_id=$1),
  (SELECT count(*) FROM projection_source_streams WHERE source_stream_id=$1)`,
		lookalikeID,
		unpublishedDeltaID,
	).Scan(
		&unpublishedDeltas,
		&sharedRefDeltas,
		&lookalikeReceipts,
		&lookalikeStreams,
	); err != nil {
		t.Fatal(err)
	}
	if unpublishedDeltas != 0 {
		t.Fatalf("rollback retained exact unpublished owner delta count=%d", unpublishedDeltas)
	}
	if sharedRefDeltas != 1 {
		t.Fatalf("rollback removed unrelated shared-source-ref delta count=%d", sharedRefDeltas)
	}
	if lookalikeReceipts != 1 || lookalikeStreams != 1 {
		t.Fatalf("rollback removed reserved-prefix lookalike receipt=%d stream=%d",
			lookalikeReceipts, lookalikeStreams)
	}

	if err := applyOldProjectionEvent(ctx, store, pool, oldReplay); err != nil {
		t.Fatalf("old-shape owner replay after rollback: %v", err)
	}
	var replayReceipts, replayStreams int
	var replayLastSourceSequence int64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM projection_event_receipts WHERE source_event_id=$1),
  (SELECT count(*) FROM projection_source_streams WHERE source_stream_id=$2),
  (SELECT last_source_sequence FROM projection_source_streams WHERE source_stream_id=$2)`,
		unpublished.EventID, unpublished.StreamID,
	).Scan(&replayReceipts, &replayStreams, &replayLastSourceSequence); err != nil {
		t.Fatal(err)
	}
	if replayReceipts != 1 || replayStreams != 1 ||
		replayLastSourceSequence != int64(unpublished.Sequence) {
		t.Fatalf("old replay identities receipts=%d streams=%d last_sequence=%d",
			replayReceipts, replayStreams, replayLastSourceSequence)
	}
	var replayEpoch, replayCursor, replayVersion uint64
	if err := pool.QueryRow(ctx, `
SELECT view_epoch,view_cursor,projection_version
FROM view_projections
WHERE tenant_id='tenant-local' AND user_id='user-local'
  AND view_type='task_summary' AND view_id='index'`).Scan(
		&replayEpoch, &replayCursor, &replayVersion,
	); err != nil {
		t.Fatal(err)
	}
	if replayEpoch != rolledBackEpoch || replayCursor != 1 || replayVersion != 1 {
		t.Fatalf("old replay View epoch=%d cursor=%d version=%d, want epoch=%d cursor=1 version=1",
			replayEpoch, replayCursor, replayVersion, rolledBackEpoch)
	}
	var deltaEpoch, deltaCursor, deltaBaseVersion, deltaResultVersion uint64
	if err := pool.QueryRow(ctx, `
SELECT view_epoch,view_cursor,base_projection_version,result_projection_version
FROM projection_deltas
WHERE delta_id=$1`, unpublishedDeltaID).Scan(
		&deltaEpoch, &deltaCursor, &deltaBaseVersion, &deltaResultVersion,
	); err != nil {
		t.Fatal(err)
	}
	if deltaEpoch != rolledBackEpoch || deltaCursor != 1 ||
		deltaBaseVersion != 0 || deltaResultVersion != 1 {
		t.Fatalf("old replay delta epoch=%d cursor=%d base=%d result=%d",
			deltaEpoch, deltaCursor, deltaBaseVersion, deltaResultVersion)
	}
	staleBatch, err := views.Resume(ctx, projectionapp.ResumeQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewTaskSummary, ViewID: TaskIndexViewID,
		AfterEpoch: priorViewEpoch, AfterCursor: priorViewCursor - 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if staleBatch.Control == nil || staleBatch.Control.EventType != "resync_required" {
		t.Fatalf("old-epoch replay did not require resync: %+v", staleBatch)
	}
	currentBatch, err := views.Resume(ctx, projectionapp.ResumeQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewTaskSummary, ViewID: TaskIndexViewID,
		AfterEpoch: rolledBackEpoch, AfterCursor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if currentBatch.Control != nil || len(currentBatch.Deltas) != 1 ||
		currentBatch.Deltas[0].ViewCursor != 1 ||
		currentBatch.Deltas[0].BaseProjectionVersion != 0 {
		t.Fatalf("new-epoch replay is not contiguous: %+v", currentBatch)
	}
}

func withoutHomeUpdate(updates []projectionapp.ViewUpdate) []projectionapp.ViewUpdate {
	filtered := make([]projectionapp.ViewUpdate, 0, len(updates))
	for _, update := range updates {
		if update.ViewType != projectionapp.ViewHomeDashboard {
			filtered = append(filtered, update)
		}
	}
	return filtered
}

func applyOldProjectionEvent(
	ctx context.Context,
	store *Store,
	pool *pgxpool.Pool,
	event projectionapp.ProjectionEvent,
) error {
	if err := projectionapp.ValidateProjectionEvent(event); err != nil {
		return err
	}
	digest, err := projectionapp.ProjectionEventDigest(event)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var storedDigest string
	var result []projectionapp.ViewProjection
	err = tx.QueryRow(ctx, `
SELECT event_digest,projection_result
FROM projection_event_receipts
WHERE source_event_id=$1`, event.SourceEventID).Scan(&storedDigest, &result)
	if err == nil {
		if storedDigest != digest {
			return ErrProjectionEventConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := advanceSourceStream(ctx, tx, event); err != nil {
		return err
	}
	result = make([]projectionapp.ViewProjection, 0, len(event.Updates))
	for _, update := range event.Updates {
		projection, err := applyViewUpdate(ctx, tx, update, event.SourceEventID, event.ProjectedAt)
		if err != nil {
			return err
		}
		if err := store.pruneReplay(ctx, tx, update); err != nil {
			return err
		}
		result = append(result, projection)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO projection_event_receipts
  (source_event_id,source_stream_id,source_sequence,event_digest,projection_result,applied_at)
VALUES ($1,$2,$3,$4,$5,$6)`,
		event.SourceEventID, event.SourceStreamID, event.SourceSequence,
		digest, encoded, event.ProjectedAt,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func homeFenceMigration(t *testing.T) (string, string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Home fencing test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, "migrations", "000019_home_dashboard_view.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(content), "-- migrate:down")
	if !ok {
		t.Fatal("Home migration lacks down marker")
	}
	return up, strings.TrimSpace(down)
}

func seedHomeFenceOutboxEvent(
	t *testing.T,
	pool *pgxpool.Pool,
	event contractsv1.IntegrationEvent,
	published bool,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
INSERT INTO owner_stream_positions
  (stream_id,owner_context,aggregate_type,aggregate_id,next_sequence,revision,updated_at)
VALUES ($1,$2,$3,$4,$5,1,$6)
ON CONFLICT (stream_id) DO UPDATE
SET next_sequence=GREATEST(owner_stream_positions.next_sequence,EXCLUDED.next_sequence),
    revision=owner_stream_positions.revision+1,
    updated_at=GREATEST(owner_stream_positions.updated_at,EXCLUDED.updated_at)`,
		event.StreamID, event.ProducerContext, event.AggregateType, event.AggregateID,
		event.Sequence+1, event.OccurredAt,
	); err != nil {
		t.Fatal(err)
	}
	var publishedAt any
	if published {
		publishedAt = event.OccurredAt.Add(time.Minute)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,
   aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
   visibility,payload,occurred_at,published_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
		event.StreamID, event.Sequence, event.TenantID, event.AggregateType,
		event.AggregateID, event.TraceID, event.CorrelationID, event.CausationID,
		event.IdempotencyKey, event.Visibility, event.Payload, event.OccurredAt, publishedAt,
	); err != nil {
		t.Fatal(fmt.Errorf("seed rollback Outbox event: %w", err))
	}
}

func seedHomeFenceTask(t *testing.T, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,
   policy_revision,dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ('assessment-home-fence','tenant-local','user-local','workspace-home',
  'workspace-revision-home','api','readiness-v1','{}'::jsonb,'[]'::jsonb,'[]'::jsonb,
  'ready_confirmed',
  'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,
   readiness_assessment_id,task_spec_digest,trigger_policy_revision,status,
   consumed_by_task_id,created_at,expires_at,consumed_at)
VALUES ('intent-home-fence','tenant-local','user-local','workspace-home',
  'workspace-revision-home','assessment-home-fence',
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'trigger-v1','consumed','task-home-fence',$1,$1::timestamptz + interval '1 hour',$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,
   task_spec_snapshot,task_spec_digest,creation_intent_id,status,acceptance_mode,
   revision,created_at,updated_at)
VALUES ('task-home-fence','tenant-local','user-local','workspace-home',
  'workspace-revision-home',jsonb_build_object('title','Fence old'),
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'intent-home-fence','waiting_user','manual',2,$1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_session_continuity_quotas
  (tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at)
VALUES ('tenant-local','user-local',10,1,1,'continuity-home-v1',$1)`, now); err != nil {
		t.Fatal(err)
	}
}

func homeFenceEvent(eventID, streamID string, at time.Time) contractsv1.IntegrationEvent {
	return contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       eventID, EventType: "TaskTransitioned.v1",
		ProducerContext: "task-orchestration", StreamID: streamID, Sequence: 1,
		TenantID: "tenant-local", AggregateType: "Task", AggregateID: "task-home-fence",
		TraceID: eventID, CorrelationID: eventID, CausationID: eventID,
		IdempotencyKey: eventID, Visibility: "user", OccurredAt: at,
		Payload: json.RawMessage(`{"fixture":true}`),
	}
}
