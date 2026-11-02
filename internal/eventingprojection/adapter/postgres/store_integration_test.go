//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestProjectionEventAtomicallyBuildsViewsAndIsIdempotent(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	projector := projectionapp.Projector{Store: store}
	event := projectionEvent()

	first, err := projector.Execute(ctx, event)
	if err != nil {
		t.Fatalf("project event: %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("projection result count = %d", len(first))
	}
	assertProjectionCount(t, pool, "view_projections", 4)
	assertProjectionCount(t, pool, "projection_deltas", 4)
	assertProjectionCount(t, pool, "projection_event_receipts", 1)
	assertProjectionCount(t, pool, "projection_source_streams", 1)
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now: func() time.Time { return event.ProjectedAt.Add(time.Second) },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewWorkerDetail,
		ViewID:   "session-1",
	})
	if err != nil {
		t.Fatalf("read committed Snapshot: %v", err)
	}
	if snapshot.ViewCursor != 1 || snapshot.ProjectionVersion != 1 || len(snapshot.AvailableActions) != 1 ||
		snapshot.GeneratedAt != event.ProjectedAt.Add(time.Second) {
		t.Fatalf("Snapshot = %#v", snapshot)
	}

	retry, err := projector.Execute(ctx, event)
	if err != nil || len(retry) != 4 {
		t.Fatalf("retry result count=%d error=%v", len(retry), err)
	}
	assertProjectionCount(t, pool, "projection_deltas", 4)
	var taskCursor, taskVersion uint64
	if err := pool.QueryRow(ctx, `
SELECT view_cursor,projection_version FROM view_projections
WHERE view_type='task_summary' AND view_id='task-1'`).Scan(&taskCursor, &taskVersion); err != nil {
		t.Fatal(err)
	}
	if taskCursor != 1 || taskVersion != 1 {
		t.Fatalf("Task View cursor=%d version=%d", taskCursor, taskVersion)
	}
}

func TestProjectionEventRejectsContentConflictAndStreamGap(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	projector := projectionapp.Projector{Store: projectionpostgres.NewStore(pool)}
	first := projectionEvent()
	if _, err := projector.Execute(ctx, first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Updates = append([]projectionapp.ViewUpdate(nil), first.Updates...)
	conflict.Updates[0].Data = json.RawMessage(`{"status":"completed"}`)
	if _, err := projector.Execute(ctx, conflict); !errors.Is(err, projectionpostgres.ErrProjectionEventConflict) {
		t.Fatalf("content conflict error = %v", err)
	}

	gap := projectionapp.ProjectionEvent{
		SourceEventID: "source-event-3", SourceStreamID: first.SourceStreamID, SourceSequence: 3,
		ProjectedAt: first.ProjectedAt.Add(time.Minute),
		Updates:     []projectionapp.ViewUpdate{viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-worker-gap")},
	}
	if _, err := projector.Execute(ctx, gap); !errors.Is(err, projectionpostgres.ErrProjectionStreamGap) {
		t.Fatalf("stream gap error = %v", err)
	}
	assertProjectionCount(t, pool, "projection_deltas", 4)
	assertProjectionCount(t, pool, "projection_event_receipts", 1)

	second := gap
	second.SourceEventID = "source-event-2"
	second.SourceSequence = 2
	second.Updates[0].DeltaID = "delta-worker-2"
	if _, err := projector.Execute(ctx, second); err != nil {
		t.Fatalf("project sequence 2: %v", err)
	}
	var workerCursor, workerVersion uint64
	if err := pool.QueryRow(ctx, `
SELECT view_cursor,projection_version FROM view_projections
WHERE view_type='worker_detail' AND view_id='session-1'`).Scan(&workerCursor, &workerVersion); err != nil {
		t.Fatal(err)
	}
	if workerCursor != 2 || workerVersion != 2 {
		t.Fatalf("Worker View cursor=%d version=%d", workerCursor, workerVersion)
	}
}

func projectionEvent() projectionapp.ProjectionEvent {
	updates := []projectionapp.ViewUpdate{
		viewUpdate(projectionapp.ViewTaskSummary, "task-1", "delta-task-1"),
		viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-worker-1"),
		viewUpdate(projectionapp.ViewArtifactDetail, "artifact-1", "delta-artifact-1"),
		viewUpdate(projectionapp.ViewCommandStatus, "command-1", "delta-command-1"),
	}
	updates[0].RelatedRefs = []projectionapp.RelatedRef{{
		Type: "worker_detail", ID: "session-1", Href: "/api/v1/views/worker_detail/session-1",
	}}
	updates[1].AvailableActions = []projectionapp.ActionRef{{
		Action: "follow-up", TargetID: "session-1", ExpectedRevision: 4,
	}}
	updates[2].EventType = "view.artifact_ready.v1"
	updates[3].CommandID = "command-1"
	return projectionapp.ProjectionEvent{
		SourceEventID: "source-event-1", SourceStreamID: "task-stream-1", SourceSequence: 1,
		ProjectedAt: time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC), Updates: updates,
	}
}

func viewUpdate(viewType, viewID, deltaID string) projectionapp.ViewUpdate {
	return projectionapp.ViewUpdate{
		DeltaID: deltaID, TenantID: "tenant-local", UserID: "user-local", ViewType: viewType, ViewID: viewID,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
		EventType: "view.entity_upserted.v1", Critical: true,
		SourceRefs:   []projectionapp.SourceRef{{Type: "integration_event", ID: "source-event-1"}},
		DeltaPayload: json.RawMessage(`{"entity":{"status":"running"}}`),
	}
}

func assertProjectionCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("%s count=%d, want %d", table, count, want)
	}
}
