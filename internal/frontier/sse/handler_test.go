package frontiersse_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	frontiersse "github.com/abietic/switchback/internal/frontier/sse"
)

func TestHandlerReplaysTypedDeltaWithDurableEventID(t *testing.T) {
	t.Parallel()
	store := &continuityStoreStub{replayWindow: projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 3, ViewCursor: 9, ProjectionVersion: 9,
	}, Deltas: []projectionapp.ProjectionDelta{{
		SchemaVersion: projectionapp.ProjectionDeltaSchemaVersion, DeltaID: "delta-2",
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 3, ViewCursor: 9,
		BaseProjectionVersion: 8, ResultProjectionVersion: 9,
		EventType: "view.timeline_appended.v1", Critical: true,
		SourceRefs: []projectionapp.SourceRef{}, Payload: json.RawMessage(`{"entry":{"status":"completed"}}`),
	}}}}
	handler := sseHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/session-1/events?once=1", nil)
	request.Header.Set("Last-Event-ID", "3:8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d headers=%v", response.Code, response.Header())
	}
	body := response.Body.String()
	if !strings.Contains(body, "id: 3:9\nevent: projection_delta\n") ||
		!strings.Contains(body, `"schema_version":"frontier.projection_delta.v1"`) ||
		strings.Contains(body, "IntegrationEvent") {
		t.Fatalf("SSE body = %q", body)
	}
	if query := store.lastReplayQuery(); query.AfterCursor != 8 ||
		query.ViewType != projectionapp.ViewWorkerDetail || query.ViewID != "session-1" {
		t.Fatalf("replay query = %#v", query)
	}
}

func TestHandlerWritesControlWithoutAdvancingDurableCursor(t *testing.T) {
	t.Parallel()
	store := &continuityStoreStub{replayWindow: projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewTaskSummary, ViewID: "task-1", ViewEpoch: 2, ViewCursor: 14, ProjectionVersion: 14,
	}}}
	handler := sseHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/task-1/events?cursor=1:4&once=1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "id:") ||
		!strings.Contains(body, "event: resync_required\n") {
		t.Fatalf("control SSE status=%d body=%q", response.Code, body)
	}
}

func TestHandlerRejectsInvalidLastEventID(t *testing.T) {
	t.Parallel()
	store := &continuityStoreStub{}
	handler := sseHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/task-1/events?once=1", nil)
	request.Header.Set("Last-Event-ID", "not-a-cursor")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Body.String() != "invalid replay cursor\n" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestHandlerReplaysAssistantConversationDeltaWithCommandCausation(t *testing.T) {
	t.Parallel()
	store := &continuityStoreStub{replayWindow: projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewAssistantConversation, ViewID: "conversation-1", ViewEpoch: 1, ViewCursor: 2, ProjectionVersion: 2,
	}, Deltas: []projectionapp.ProjectionDelta{{
		SchemaVersion: projectionapp.ProjectionDeltaSchemaVersion,
		DeltaID:       "assistant-delta-2", ViewType: projectionapp.ViewAssistantConversation,
		ViewID: "conversation-1", ViewEpoch: 1, ViewCursor: 2,
		BaseProjectionVersion: 1, ResultProjectionVersion: 2,
		EventType: "view.entity_upserted.v1", Critical: true,
		CommandID:  "assistant-command-1",
		SourceRefs: []projectionapp.SourceRef{{Type: "integration_event", ID: "assistant-event-2"}},
		Payload:    json.RawMessage(`{"data":{"conversation_id":"conversation-1","revision":2}}`),
	}}}}
	handler := sseHandler(t, store)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/views/assistant_conversation/conversation-1/events?once=1",
		nil,
	)
	request.Header.Set("Last-Event-ID", "1:1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(body, "id: 1:2\nevent: projection_delta\n") ||
		!strings.Contains(body, `"view_type":"assistant_conversation"`) ||
		!strings.Contains(body, `"command_id":"assistant-command-1"`) ||
		strings.Contains(body, "AssistantDecisionCommitted") {
		t.Fatalf("Assistant SSE status=%d body=%q", response.Code, body)
	}
	if query := store.lastReplayQuery(); query.ViewType != projectionapp.ViewAssistantConversation ||
		query.ViewID != "conversation-1" ||
		query.AfterCursor != 1 {
		t.Fatalf("Assistant replay query = %#v", query)
	}
}

func TestTransientDisplayEventNeverCarriesDurableSSEID(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	delta := projectionapp.TransientDisplayDelta{
		SchemaVersion: projectionapp.TransientDeltaSchemaVersion, DisplayStreamID: "display-1", PartID: "part-1",
		BaseCheckpointVersion: 2, TransientSequence: 1, DeltaType: "text_delta",
		Payload: json.RawMessage(`{"text":"next"}`), EmittedAt: time.Now().UTC(),
	}
	if err := frontiersse.WriteTransient(&output, delta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "id:") || !strings.HasPrefix(output.String(), "event: transient_display_delta\n") {
		t.Fatalf("transient SSE = %q", output.String())
	}
}

func TestWorkerDetailHandlerStreamsBestEffortTransientDisplayWithoutDurableCursor(t *testing.T) {
	store := &continuityStoreStub{replayWindow: projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 1, ViewCursor: 0, ProjectionVersion: 1,
	}}, replayCalls: make(chan int, 4)}
	broker, err := projectionapp.NewTransientBroker(projectionapp.TransientPolicy{
		MaxItems: 8, MaxBytes: 16 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.Register("display-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	handler := sseHandlerWithTransient(t, store, broker)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/views/worker_detail/session-1/events?cursor=1:0",
		nil,
	).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	for calls := range store.replayCalls {
		if calls >= 2 {
			break
		}
	}
	delta := projectionapp.TransientDisplayDelta{
		SchemaVersion:   projectionapp.TransientDeltaSchemaVersion,
		DisplayStreamID: "display-1", PartID: "part-1",
		BaseCheckpointVersion: 1, TransientSequence: 1, DeltaType: "text_delta",
		Payload: json.RawMessage(`{"text":"streaming"}`), EmittedAt: time.Now().UTC(),
	}
	if err := broker.Publish(delta); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	cancel()
	<-done

	body := response.Body.String()
	if !strings.Contains(body, "event: transient_display_delta\n") ||
		!strings.Contains(body, `"display_stream_id":"display-1"`) ||
		strings.Contains(body, "id: 1:1\nevent: transient_display_delta") {
		t.Fatalf("transient WorkerDetail SSE = %q", body)
	}
}

func TestHandlerAdvancesPollingCursorOnlyAfterVerifiedDeltas(t *testing.T) {
	store := &continuityStoreStub{replayCalls: make(chan int, 4)}
	store.replay = func(query projectionapp.ReplayStoreQuery, call int) (projectionapp.StoredReplayWindow, error) {
		if call == 1 {
			return projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
				SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
				ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 1, ViewCursor: 1, ProjectionVersion: 2,
			}, Deltas: []projectionapp.ProjectionDelta{{
				SchemaVersion: projectionapp.ProjectionDeltaSchemaVersion, DeltaID: "delta-1",
				ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 1, ViewCursor: 1,
				BaseProjectionVersion: 1, ResultProjectionVersion: 2, EventType: "view.entity_upserted.v1",
				SourceRefs: []projectionapp.SourceRef{}, Payload: json.RawMessage(`{"status":"running"}`),
			}}}, nil
		}
		if query.AfterCursor != 1 {
			return projectionapp.StoredReplayWindow{}, errors.New("cursor advanced without verified deltas")
		}
		return projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
			SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
			ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 1, ViewCursor: 1, ProjectionVersion: 2,
		}}, nil
	}
	handler := sseHandler(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/session-1/events?cursor=1:0", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(response, request) }()
	for calls := range store.replayCalls {
		if calls >= 2 {
			break
		}
	}
	cancel()
	<-done
	if queries := store.allReplayQueries(); len(queries) < 2 || queries[1].AfterCursor != 1 {
		t.Fatalf("replay queries = %#v", queries)
	}
}

func TestHandlerClosesAfterHeaderReadErrorWithoutWritingUntrustedData(t *testing.T) {
	store := &continuityStoreStub{replayWindow: projectionapp.StoredReplayWindow{Head: projectionapp.StoredViewHead{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion, TenantID: "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1", ViewEpoch: 1, ViewCursor: 0, ProjectionVersion: 1,
	}}, replayCalls: make(chan int, 4)}
	store.replay = func(_ projectionapp.ReplayStoreQuery, call int) (projectionapp.StoredReplayWindow, error) {
		if call == 1 {
			return store.replayWindow, nil
		}
		return projectionapp.StoredReplayWindow{}, errors.New("store read failed")
	}
	handler := sseHandler(t, store)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/session-1/events?cursor=1:0", nil)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(response, request) }()
	for calls := range store.replayCalls {
		if calls >= 2 {
			break
		}
	}
	<-done
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func sseHandler(t *testing.T, store *continuityStoreStub) http.Handler {
	return sseHandlerWithTransient(t, store, nil)
}

func sseHandlerWithTransient(
	t *testing.T,
	store *continuityStoreStub,
	transient *projectionapp.TransientBroker,
) http.Handler {
	t.Helper()
	handler, err := frontiersse.NewHandler(frontiersse.Options{
		Viewer:       projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		Views:        newContinuityReader(t, store),
		Transient:    transient,
		PollInterval: time.Millisecond, Heartbeat: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/views/{view_type}/{view_id}/events", handler)
	return mux
}

func newContinuityReader(t *testing.T, store projectionapp.ContinuityStore) *projectionapp.ViewContinuityReader {
	t.Helper()
	reader, err := projectionapp.NewViewContinuityReader(store, projectionapp.ViewContinuityReaderOptions{
		Now: func() time.Time { return time.Date(2026, time.July, 23, 16, 0, 0, 0, time.UTC) }, ReplayLimit: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

type continuityStoreStub struct {
	mu            sync.Mutex
	replayWindow  projectionapp.StoredReplayWindow
	replay        func(projectionapp.ReplayStoreQuery, int) (projectionapp.StoredReplayWindow, error)
	replayQueries []projectionapp.ReplayStoreQuery
	replayCalls   chan int
}

func (store *continuityStoreStub) LoadSnapshotState(
	context.Context,
	projectionapp.SnapshotStoreQuery,
) (projectionapp.StoredSnapshotState, error) {
	return projectionapp.StoredSnapshotState{}, errors.New("unexpected snapshot read")
}

func (store *continuityStoreStub) LoadReplayWindow(
	_ context.Context,
	query projectionapp.ReplayStoreQuery,
) (projectionapp.StoredReplayWindow, error) {
	store.mu.Lock()
	store.replayQueries = append(store.replayQueries, query)
	call := len(store.replayQueries)
	replay := store.replay
	window := store.replayWindow
	store.mu.Unlock()
	select {
	case store.replayCalls <- call:
	default:
	}
	if replay != nil {
		return replay(query, call)
	}
	return window, nil
}

func (store *continuityStoreStub) lastReplayQuery() projectionapp.ReplayStoreQuery {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.replayQueries[len(store.replayQueries)-1]
}

func (store *continuityStoreStub) allReplayQueries() []projectionapp.ReplayStoreQuery {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]projectionapp.ReplayStoreQuery(nil), store.replayQueries...)
}
