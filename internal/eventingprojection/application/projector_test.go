package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

func TestProjectorRejectsDuplicateViewTargetBeforeStore(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	event := projectionEvent()
	event.Updates = append(event.Updates, event.Updates[0])
	event.Updates[len(event.Updates)-1].DeltaID = "delta-duplicate-target"

	if _, err := (projectionapp.Projector{Store: store}).Execute(context.Background(), event); err == nil {
		t.Fatal("duplicate View target was accepted")
	}
	if store.calls != 0 {
		t.Fatal("invalid projection event reached store")
	}
}

func TestProjectorAcceptsHomeDashboardView(t *testing.T) {
	t.Parallel()
	store := &recordingStore{}
	event := projectionEvent()
	event.Updates[0] = viewUpdate(projectionapp.ViewHomeDashboard, "index", "delta-home-index")

	if _, err := (projectionapp.Projector{Store: store}).Execute(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls=%d want=1", store.calls)
	}
}

func TestProjectorValidatesOptionalClientCommandIdentity(t *testing.T) {
	t.Parallel()
	valid := projectionEvent()
	valid.Updates[0].CommandID = "server-command-1"
	valid.Updates[0].ClientCommandID = "client-command-1"
	if _, err := (projectionapp.Projector{Store: &recordingStore{}}).Execute(context.Background(), valid); err != nil {
		t.Fatalf("valid client command identity rejected: %v", err)
	}

	invalid := projectionEvent()
	invalid.Updates[0].CommandID = "server-command-1"
	invalid.Updates[0].ClientCommandID = " client-command-1 "
	if _, err := (projectionapp.Projector{Store: &recordingStore{}}).Execute(context.Background(), invalid); err == nil {
		t.Fatal("non-canonical client command identity was accepted")
	}
}

func projectionEvent() projectionapp.ProjectionEvent {
	return projectionapp.ProjectionEvent{
		SourceEventID: "source-event-1", SourceStreamID: "task-stream-1", SourceSequence: 1,
		ProjectedAt: time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC),
		Updates:     []projectionapp.ViewUpdate{viewUpdate(projectionapp.ViewTaskSummary, "task-1", "delta-task-1")},
	}
}

func viewUpdate(viewType, viewID, deltaID string) projectionapp.ViewUpdate {
	return projectionapp.ViewUpdate{
		DeltaID: deltaID, TenantID: "tenant-local", UserID: "user-local", ViewType: viewType, ViewID: viewID,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
		EventType: "view.entity_upserted.v1", Critical: true, SourceRefs: []projectionapp.SourceRef{},
		DeltaPayload: json.RawMessage(`{"entity":{"status":"running"}}`),
	}
}

type recordingStore struct{ calls int }

func (store *recordingStore) ApplyProjectionEvent(context.Context, projectionapp.ProjectionEvent, string) ([]projectionapp.ViewProjection, error) {
	store.calls++
	return nil, nil
}
