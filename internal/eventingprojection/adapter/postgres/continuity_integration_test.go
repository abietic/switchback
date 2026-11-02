//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestSnapshotSubscribeRaceReconnectProjectionLagAndAccessClear(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	projector := projectionapp.Projector{Store: store}
	base := time.Date(2026, time.July, 23, 19, 0, 0, 0, time.UTC)
	first := continuityEvent(1, "running", base)
	if _, err := projector.Execute(ctx, first); err != nil {
		t.Fatal(err)
	}
	viewer := projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now: func() time.Time { return base.Add(time.Second) }, ReplayLimit: 16,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1",
	})
	if err != nil || snapshot.ViewCursor != 1 || snapshot.ProjectionVersion != 1 {
		t.Fatalf("initial Snapshot=%#v error=%v", snapshot, err)
	}

	// This commit lands after Snapshot and before subscribe.
	second := continuityEvent(2, "waiting_user", base.Add(2*time.Second))
	if _, err := projector.Execute(ctx, second); err != nil {
		t.Fatal(err)
	}
	request := projectionapp.ResumeQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1",
		AfterEpoch: snapshot.ViewEpoch, AfterCursor: snapshot.ViewCursor,
	}
	batch, err := reader.Resume(ctx, request)
	if err != nil || len(batch.Deltas) != 1 || batch.Deltas[0].ViewCursor != 2 ||
		batch.Deltas[0].BaseProjectionVersion != 1 || batch.Deltas[0].ResultProjectionVersion != 2 {
		t.Fatalf("race replay=%#v error=%v", batch, err)
	}

	// Response loss and reconnect must neither duplicate projection state nor change delta identity.
	if _, err := projector.Execute(ctx, second); err != nil {
		t.Fatalf("duplicate source event: %v", err)
	}
	reconnected, err := reader.Resume(ctx, request)
	if err != nil || len(reconnected.Deltas) != 1 || reconnected.Deltas[0].DeltaID != batch.Deltas[0].DeltaID {
		t.Fatalf("reconnect replay=%#v error=%v", reconnected, err)
	}
	request.AfterCursor = 2
	caughtUp, err := reader.Resume(ctx, request)
	if err != nil || caughtUp.Control != nil || len(caughtUp.Deltas) != 0 {
		t.Fatalf("caught-up replay=%#v error=%v", caughtUp, err)
	}

	// An owner change that has not been projected must not appear in a Snapshot.
	lagged, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1",
	})
	var laggedData struct {
		Status string `json:"status"`
	}
	decodeErr := json.Unmarshal(lagged.Data, &laggedData)
	if err != nil || decodeErr != nil || lagged.ViewCursor != 2 || laggedData.Status != "waiting_user" {
		t.Fatalf("lagged Snapshot=%#v error=%v", lagged, err)
	}
	third := continuityEvent(3, "completed", base.Add(3*time.Second))
	if _, err := projector.Execute(ctx, third); err != nil {
		t.Fatal(err)
	}
	request.AfterCursor = 2
	advanced, err := reader.Resume(ctx, request)
	if err != nil || len(advanced.Deltas) != 1 || advanced.Deltas[0].ViewCursor != 3 {
		t.Fatalf("advanced replay=%#v error=%v", advanced, err)
	}

	cleared := projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-cleared"}
	if _, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: cleared, ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1",
	}); !errors.Is(err, projectionapp.ErrViewUnavailable) {
		t.Fatalf("cleared Snapshot error = %v", err)
	}
	request.Viewer = cleared
	if _, err := reader.Resume(ctx, request); !errors.Is(err, projectionapp.ErrViewUnavailable) {
		t.Fatalf("cleared replay error = %v", err)
	}
}

func TestAssistantSnapshotSubscribeRaceReconnectGapAndAccessFailure(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := projectionpostgres.NewStore(pool)
	projector := projectionapp.Projector{Store: store}
	base := time.Date(2026, time.July, 25, 5, 0, 0, 0, time.UTC)
	first := assistantContinuityEvent(1, 1, base)
	if _, err := projector.Execute(ctx, first); err != nil {
		t.Fatal(err)
	}
	viewer := projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}
	reader, err := projectionapp.NewViewContinuityReader(
		store,
		projectionapp.ViewContinuityReaderOptions{
			Now: func() time.Time { return base.Add(time.Second) }, ReplayLimit: 16,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: viewer, ViewType: projectionapp.ViewAssistantConversation, ViewID: "conversation-1",
	})
	if err != nil || snapshot.ViewCursor != 1 {
		t.Fatalf("Assistant snapshot=%+v error=%v", snapshot, err)
	}
	second := assistantContinuityEvent(2, 2, base.Add(2*time.Second))
	if _, err := projector.Execute(ctx, second); err != nil {
		t.Fatal(err)
	}
	request := projectionapp.ResumeQuery{
		Viewer: viewer, ViewType: projectionapp.ViewAssistantConversation,
		ViewID: "conversation-1", AfterEpoch: snapshot.ViewEpoch,
		AfterCursor: snapshot.ViewCursor,
	}
	batch, err := reader.Resume(ctx, request)
	if err != nil || len(batch.Deltas) != 1 ||
		batch.Deltas[0].ViewCursor != 2 ||
		batch.Deltas[0].CommandID != "assistant-command-2" {
		t.Fatalf("Assistant snapshot/subscribe replay=%+v error=%v", batch, err)
	}
	if _, err := projector.Execute(ctx, second); err != nil {
		t.Fatalf("Assistant duplicate source event: %v", err)
	}
	reconnected, err := reader.Resume(ctx, request)
	if err != nil || len(reconnected.Deltas) != 1 ||
		reconnected.Deltas[0].DeltaID != batch.Deltas[0].DeltaID {
		t.Fatalf("Assistant reconnect replay=%+v error=%v", reconnected, err)
	}
	if _, err := projector.Execute(
		ctx, assistantContinuityEvent(4, 4, base.Add(4*time.Second)),
	); !errors.Is(err, projectionpostgres.ErrProjectionStreamGap) {
		t.Fatalf("Assistant source gap error=%v", err)
	}
	cleared := projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-cleared"}
	if _, err := reader.ReadSnapshot(ctx, projectionapp.SnapshotQuery{
		Viewer: cleared, ViewType: projectionapp.ViewAssistantConversation, ViewID: "conversation-1",
	}); !errors.Is(err, projectionapp.ErrViewUnavailable) {
		t.Fatalf("Assistant cleared snapshot error=%v", err)
	}
	request.Viewer = cleared
	if _, err := reader.Resume(ctx, request); !errors.Is(err, projectionapp.ErrViewUnavailable) {
		t.Fatalf("Assistant cleared replay error=%v", err)
	}
}

func assistantContinuityEvent(
	sequence, conversationRevision uint64,
	at time.Time,
) projectionapp.ProjectionEvent {
	update := viewUpdate(
		projectionapp.ViewAssistantConversation,
		"conversation-1",
		"delta-assistant-"+strconv.FormatUint(sequence, 10),
	)
	update.CommandID = "assistant-command-" + strconv.FormatUint(sequence, 10)
	update.Data = json.RawMessage(
		`{"conversation_id":"conversation-1","revision":` +
			strconv.FormatUint(conversationRevision, 10) + `}`,
	)
	update.DeltaPayload = json.RawMessage(`{"data":` + string(update.Data) + `}`)
	return projectionapp.ProjectionEvent{
		SourceEventID:  "source-assistant-" + strconv.FormatUint(sequence, 10),
		SourceStreamID: "conversation/conversation-1", SourceSequence: sequence,
		ProjectedAt: at, Updates: []projectionapp.ViewUpdate{update},
	}
}

func continuityEvent(sequence uint64, status string, at time.Time) projectionapp.ProjectionEvent {
	update := viewUpdate(projectionapp.ViewWorkerDetail, "session-1", "delta-continuity-"+string(rune('0'+sequence)))
	update.Data = json.RawMessage(`{"status":"` + status + `"}`)
	update.DeltaPayload = json.RawMessage(`{"entity":{"status":"` + status + `"}}`)
	return projectionapp.ProjectionEvent{
		SourceEventID:  "source-continuity-" + string(rune('0'+sequence)),
		SourceStreamID: "worker-continuity-stream", SourceSequence: sequence,
		ProjectedAt: at, Updates: []projectionapp.ViewUpdate{update},
	}
}
