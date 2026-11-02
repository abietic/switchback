//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestReplayWindowReturnsDeltasOrExplicitResync(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store, err := projectionpostgres.NewStoreWithReplayLimits(pool, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	projector := projectionapp.Projector{Store: store}
	base := time.Date(2026, time.July, 23, 17, 0, 0, 0, time.UTC)
	for sequence := uint64(1); sequence <= 3; sequence++ {
		update := viewUpdate(projectionapp.ViewTaskSummary, "task-1", "delta-task-"+string(rune('0'+sequence)))
		event := projectionapp.ProjectionEvent{
			SourceEventID:  "source-task-" + string(rune('0'+sequence)),
			SourceStreamID: "task-stream-1", SourceSequence: sequence,
			ProjectedAt: base.Add(time.Duration(sequence) * time.Second), Updates: []projectionapp.ViewUpdate{update},
		}
		if _, err := projector.Execute(ctx, event); err != nil {
			t.Fatalf("project sequence %d: %v", sequence, err)
		}
	}
	assertProjectionCount(t, pool, "projection_deltas", 2)
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now:         func() time.Time { return base.Add(4 * time.Second) },
			ReplayLimit: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := projectionapp.ResumeQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewTaskSummary, ViewID: "task-1", AfterEpoch: 1,
	}
	result, err := reader.Resume(ctx, request)
	if err != nil || result.Control == nil || result.Control.EventType != "resync_required" ||
		result.Control.SnapshotCursor != 3 || result.Reason != projectionapp.ContinuityWindowEvicted {
		t.Fatalf("evicted replay result=%#v error=%v", result, err)
	}
	request.AfterCursor = 1
	result, err = reader.Resume(ctx, request)
	if err != nil || result.Control != nil || len(result.Deltas) != 2 ||
		result.Deltas[0].ViewCursor != 2 || result.Deltas[1].ViewCursor != 3 {
		t.Fatalf("retained replay result=%#v error=%v", result, err)
	}
	request.AfterEpoch = 2
	result, err = reader.Resume(ctx, request)
	if err != nil || result.Control == nil || result.Control.EventType != "resync_required" ||
		result.Reason != projectionapp.ContinuityEpochMismatch {
		t.Fatalf("stale epoch result=%#v error=%v", result, err)
	}
}

func TestReplayRequiresClientUpgradeForUnknownSnapshotSchema(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	event := projectionapp.ProjectionEvent{
		SourceEventID: "source-1", SourceStreamID: "stream-1", SourceSequence: 1,
		ProjectedAt: time.Date(2026, time.July, 23, 17, 30, 0, 0, time.UTC),
		Updates:     []projectionapp.ViewUpdate{viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-1")},
	}
	if _, err := (projectionapp.Projector{Store: store}).Execute(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE view_projections SET schema_version='frontier.projection_snapshot.v2'`); err != nil {
		t.Fatal(err)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now:         func() time.Time { return event.ProjectedAt.Add(time.Second) },
			ReplayLimit: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Resume(ctx, projectionapp.ResumeQuery{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", AfterEpoch: 1,
	})
	if err != nil || result.Control == nil || result.Control.EventType != "client_upgrade_required" ||
		result.Reason != projectionapp.ContinuitySchemaIncompatible {
		t.Fatalf("upgrade result=%#v error=%v", result, err)
	}
}

func TestClientCommandIdentityPersistsAndReplays(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	update := viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-client-command-1")
	update.CommandID = "server-command-1"
	update.ClientCommandID = "client-command-1"
	event := projectionapp.ProjectionEvent{
		SourceEventID: "source-client-command-1", SourceStreamID: "stream-client-command-1", SourceSequence: 1,
		ProjectedAt: time.Date(2026, time.July, 30, 5, 0, 0, 0, time.UTC),
		Updates:     []projectionapp.ViewUpdate{update},
	}
	if _, err := (projectionapp.Projector{Store: store}).Execute(ctx, event); err != nil {
		t.Fatalf("project client-correlated delta: %v", err)
	}

	var persisted string
	if err := pool.QueryRow(ctx, `
SELECT client_command_id FROM projection_deltas WHERE delta_id=$1`,
		update.DeltaID,
	).Scan(&persisted); err != nil {
		t.Fatalf("read persisted client command identity: %v", err)
	}
	if persisted != update.ClientCommandID {
		t.Fatalf("persisted client command identity=%q want=%q", persisted, update.ClientCommandID)
	}

	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now:         func() time.Time { return event.ProjectedAt.Add(time.Second) },
			ReplayLimit: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.Resume(ctx, projectionapp.ResumeQuery{
		Viewer:   projectionapp.Viewer{TenantID: update.TenantID, UserID: update.UserID},
		ViewType: update.ViewType, ViewID: update.ViewID, AfterEpoch: 1,
	})
	if err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if result.Control != nil || len(result.Deltas) != 1 {
		t.Fatalf("replay result=%#v", result)
	}
	if got := result.Deltas[0]; got.CommandID != update.CommandID || got.ClientCommandID != update.ClientCommandID {
		t.Fatalf("replayed command identities=%#v", got)
	}
}

func TestViewContinuitySnapshotLoadsViewAndRequestedReceipts(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	projectedAt := time.Date(2026, time.August, 24, 13, 0, 0, 0, time.UTC)
	update := viewUpdate(projectionapp.ViewWorkerDetail, "session-snapshot-state", "delta-snapshot-state-1")
	update.CommandID = "server-command-snapshot-1"
	update.ClientCommandID = "client-command-snapshot-1"
	if _, err := (projectionapp.Projector{Store: store}).Execute(ctx, projectionapp.ProjectionEvent{
		SourceEventID:  "source-snapshot-state-1",
		SourceStreamID: "stream-snapshot-state-1",
		SourceSequence: 1,
		ProjectedAt:    projectedAt,
		Updates:        []projectionapp.ViewUpdate{update},
	}); err != nil {
		t.Fatalf("project snapshot state: %v", err)
	}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now:         func() time.Time { return projectedAt.Add(time.Second) },
			ReplayLimit: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	query := projectionapp.SnapshotQuery{
		Viewer:                    projectionapp.Viewer{TenantID: update.TenantID, UserID: update.UserID},
		ViewType:                  update.ViewType,
		ViewID:                    update.ViewID,
		ReconcileClientCommandIDs: []string{update.ClientCommandID},
	}
	snapshot, err := reader.ReadSnapshot(ctx, query)
	if err != nil {
		t.Fatalf("read View snapshot with receipt: %v", err)
	}
	var data struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(snapshot.Data, &data); err != nil {
		t.Fatalf("decode View snapshot data: %v", err)
	}
	if snapshot.ViewCursor != 1 || snapshot.ProjectionVersion != 1 ||
		data.Status != "running" || len(snapshot.CommandReceipts) != 1 ||
		snapshot.CommandReceipts[0].ClientCommandID != update.ClientCommandID ||
		snapshot.CommandReceipts[0].CommandID != update.CommandID {
		t.Fatalf("snapshot state=%#v", snapshot)
	}

	query.ReconcileClientCommandIDs = []string{}
	empty, err := reader.ReadSnapshot(ctx, query)
	if err != nil {
		t.Fatalf("read View snapshot with present-empty reconciliation: %v", err)
	}
	if empty.CommandReceipts == nil || len(empty.CommandReceipts) != 0 {
		t.Fatalf("present-empty command receipts=%#v", empty.CommandReceipts)
	}
}
