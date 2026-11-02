package frontierhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	frontierhttp "github.com/abietic/switchback/internal/frontier/http"
)

func TestSnapshotEndpointReturnsCompleteCommittedViewAndETag(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 23, 16, 0, 0, 0, time.UTC)
	store := &continuityStoreStub{snapshot: projectionapp.StoredSnapshotState{View: projectionapp.ViewProjection{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion,
		TenantID:      "tenant-local", UserID: "user-local", ViewType: projectionapp.ViewWorkerDetail,
		ViewID: "session-1", ViewEpoch: 2, ViewCursor: 8, ProjectionVersion: 7,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{{Action: "follow-up", TargetID: "session-1", ExpectedRevision: 4}},
		RelatedRefs:      []projectionapp.RelatedRef{}, UpdatedAt: now.Add(-time.Minute),
	}}}
	handler := snapshotHandler(t, store, now)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/worker_detail/session-1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"worker_detail:session-1:2:7"` {
		t.Fatalf("status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var snapshot projectionapp.ProjectionSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != projectionapp.ProjectionSnapshotSchemaVersion || snapshot.ViewCursor != 8 ||
		snapshot.ProjectionVersion != 7 || snapshot.GeneratedAt != now || len(snapshot.AvailableActions) != 1 ||
		snapshot.CommandReceipts != nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if query := store.lastSnapshotQuery(); query.Viewer != (projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}) {
		t.Fatalf("query = %#v", query)
	}
}

func TestSnapshotEndpointSupportsETagAndDoesNotDiscloseUnavailableView(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 23, 16, 0, 0, 0, time.UTC)
	store := &continuityStoreStub{snapshot: projectionapp.StoredSnapshotState{View: projectionapp.ViewProjection{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion,
		TenantID:      "tenant-local", UserID: "user-local", ViewType: projectionapp.ViewTaskSummary,
		ViewID: "task-1", ViewEpoch: 1, ViewCursor: 1, ProjectionVersion: 1,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
	}}}
	handler := snapshotHandler(t, store, now)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/task-1", nil)
	request.Header.Set("If-None-Match", `"task_summary:task-1:1:1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
		t.Fatalf("conditional status=%d body=%q", response.Code, response.Body.String())
	}

	store.setSnapshotError(projectionapp.ErrViewUnavailable)
	request = httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/task-secret", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"code\":\"view_unavailable\"}\n" {
		t.Fatalf("unavailable status=%d body=%q", response.Code, response.Body.String())
	}

	store.setSnapshotError(errors.New("store unavailable"))
	request = httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/task-1", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"code\":\"projection_unavailable\"}\n" {
		t.Fatalf("storage status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestSnapshotEndpointReturnsOnlyRequestedCoveredCommandReceipts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	store := &continuityStoreStub{snapshot: projectionapp.StoredSnapshotState{View: projectionapp.ViewProjection{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion,
		TenantID:      "tenant-local", UserID: "user-local", ViewType: projectionapp.ViewWorkerDetail,
		ViewID: "session-1", ViewEpoch: 2, ViewCursor: 8, ProjectionVersion: 7,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
	}, CommandReceipts: []projectionapp.CommandReceipt{{
		ClientCommandID: "client-command-a", CommandID: "server-command-a",
	}}}}
	handler := snapshotHandler(t, store, now)
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/views/worker_detail/session-1?reconcile_client_command_id=client-command-a&reconcile_client_command_id=client-command-a", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var snapshot projectionapp.ProjectionSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	query := store.lastSnapshotQuery()
	if response.Code != http.StatusOK || len(snapshot.CommandReceipts) != 1 ||
		snapshot.CommandReceipts[0].ClientCommandID != "client-command-a" ||
		len(query.ReconcileClientCommandIDs) != 1 ||
		query.ReconcileClientCommandIDs[0] != "client-command-a" ||
		!query.ReceiptNotBefore.Equal(now.Add(-projectionapp.CommandReceiptRetentionHorizon)) {
		t.Fatalf("status=%d snapshot=%+v requested=%v body=%s",
			response.Code, snapshot, query.ReconcileClientCommandIDs, response.Body.String())
	}
	if response.Header().Get("ETag") == `"worker_detail:session-1:2:7"` {
		t.Fatalf("receipt-bearing representation reused legacy ETag")
	}
}

func TestSnapshotEndpointETagIncludesNormalizedEmptyReconciliationRequestSet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 10, 5, 0, 0, time.UTC)
	store := &continuityStoreStub{snapshot: projectionapp.StoredSnapshotState{View: projectionapp.ViewProjection{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion,
		TenantID:      "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1",
		ViewEpoch: 2, ViewCursor: 8, ProjectionVersion: 7,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
	}, CommandReceipts: []projectionapp.CommandReceipt{}}}
	handler := snapshotHandler(t, store, now)

	requestA := httptest.NewRequest(http.MethodGet,
		"/api/v1/views/worker_detail/session-1?reconcile_client_command_id=client-command-a", nil)
	responseA := httptest.NewRecorder()
	handler.ServeHTTP(responseA, requestA)
	requestB := httptest.NewRequest(http.MethodGet,
		"/api/v1/views/worker_detail/session-1?reconcile_client_command_id=client-command-b", nil)
	responseB := httptest.NewRecorder()
	handler.ServeHTTP(responseB, requestB)
	if responseA.Code != http.StatusOK || responseB.Code != http.StatusOK {
		t.Fatalf("statuses=%d/%d bodies=%q/%q",
			responseA.Code, responseB.Code, responseA.Body.String(), responseB.Body.String())
	}
	if responseA.Header().Get("ETag") == responseB.Header().Get("ETag") {
		t.Fatalf("query-sensitive empty receipt sets reused ETag %q", responseA.Header().Get("ETag"))
	}

	requestNormalized := httptest.NewRequest(http.MethodGet,
		"/api/v1/views/worker_detail/session-1?reconcile_client_command_id=client-command-b&reconcile_client_command_id=client-command-a&reconcile_client_command_id=client-command-a", nil)
	responseNormalized := httptest.NewRecorder()
	handler.ServeHTTP(responseNormalized, requestNormalized)
	requestCanonical := httptest.NewRequest(http.MethodGet,
		"/api/v1/views/worker_detail/session-1?reconcile_client_command_id=client-command-a&reconcile_client_command_id=client-command-b", nil)
	responseCanonical := httptest.NewRecorder()
	handler.ServeHTTP(responseCanonical, requestCanonical)
	if responseNormalized.Header().Get("ETag") != responseCanonical.Header().Get("ETag") {
		t.Fatalf("equivalent normalized request sets produced ETags %q/%q",
			responseNormalized.Header().Get("ETag"), responseCanonical.Header().Get("ETag"))
	}
}

func TestSnapshotEndpointBoundsCommandReconciliationBatch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	store := &continuityStoreStub{snapshot: projectionapp.StoredSnapshotState{View: projectionapp.ViewProjection{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion,
		TenantID:      "tenant-local", UserID: "user-local",
		ViewType: projectionapp.ViewWorkerDetail, ViewID: "session-1",
		ViewEpoch: 1, ViewCursor: 1, ProjectionVersion: 1,
		QueryFingerprint: "scope-v1", Data: json.RawMessage(`{"status":"running"}`),
		AvailableActions: []projectionapp.ActionRef{}, RelatedRefs: []projectionapp.RelatedRef{},
	}}}
	query := ""
	for index := 0; index <= projectionapp.MaxSnapshotCommandReceipts; index++ {
		if query != "" {
			query += "&"
		}
		query += fmt.Sprintf("reconcile_client_command_id=client-command-%d", index)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/views/worker_detail/session-1?"+query,
		nil,
	)
	response := httptest.NewRecorder()
	snapshotHandler(t, store, now).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		response.Body.String() != "{\"code\":\"invalid_command_reconciliation\"}\n" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestWorkerTimelineEndpointForwardsStableCursorAndLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 25, 16, 0, 0, 0, time.UTC)
	views := &continuityStoreStub{}
	timeline := &fakeTimelineReader{page: projectionapp.WorkerTimelinePage{
		Turns: []projectionapp.WorkerTimelineTurn{{
			WorkerRunID: "run-1", AgentTurnID: "turn-1", DisplayStreamID: "display-1",
			Status: "completed", StartedAt: now, Parts: []projectionapp.DisplayPart{},
			BackfillStatus: "online",
		}},
		NextCursor: "cursor-next", HasMore: true,
	}}
	handler, err := frontierhttp.NewHandler(frontierhttp.Options{
		Viewer:   projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		Views:    newContinuityReader(t, views, now),
		Timeline: timeline,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/views/worker_detail/session-1/timeline?before=cursor-old&limit=7",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || timeline.query.WorkerSessionID != "session-1" ||
		timeline.query.BeforeCursor != "cursor-old" || timeline.query.Limit != 7 ||
		timeline.query.Viewer.UserID != "user-local" {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, timeline.query, response.Body.String())
	}
	var page projectionapp.WorkerTimelinePage
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil ||
		len(page.Turns) != 1 || page.NextCursor != "cursor-next" || !page.HasMore {
		t.Fatalf("timeline page=%+v error=%v", page, err)
	}
}

func snapshotHandler(t *testing.T, store *continuityStoreStub, now time.Time) http.Handler {
	t.Helper()
	handler, err := frontierhttp.NewHandler(frontierhttp.Options{
		Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		Views:  newContinuityReader(t, store, now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func newContinuityReader(t *testing.T, store projectionapp.ContinuityStore, now time.Time) *projectionapp.ViewContinuityReader {
	t.Helper()
	reader, err := projectionapp.NewViewContinuityReader(store, projectionapp.ViewContinuityReaderOptions{
		Now: func() time.Time { return now }, ReplayLimit: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

type fakeTimelineReader struct {
	query projectionapp.WorkerTimelineQuery
	page  projectionapp.WorkerTimelinePage
	err   error
}

func (reader *fakeTimelineReader) ReadWorkerTimeline(
	_ context.Context,
	query projectionapp.WorkerTimelineQuery,
) (projectionapp.WorkerTimelinePage, error) {
	reader.query = query
	return reader.page, reader.err
}

type continuityStoreStub struct {
	mu              sync.Mutex
	snapshot        projectionapp.StoredSnapshotState
	snapshotErr     error
	snapshotQueries []projectionapp.SnapshotStoreQuery
}

func (store *continuityStoreStub) LoadSnapshotState(
	_ context.Context,
	query projectionapp.SnapshotStoreQuery,
) (projectionapp.StoredSnapshotState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.snapshotQueries = append(store.snapshotQueries, query)
	return store.snapshot, store.snapshotErr
}

func (store *continuityStoreStub) LoadReplayWindow(
	context.Context,
	projectionapp.ReplayStoreQuery,
) (projectionapp.StoredReplayWindow, error) {
	return projectionapp.StoredReplayWindow{}, errors.New("unexpected replay read")
}

func (store *continuityStoreStub) setSnapshotError(err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.snapshotErr = err
}

func (store *continuityStoreStub) lastSnapshotQuery() projectionapp.SnapshotStoreQuery {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.snapshotQueries[len(store.snapshotQueries)-1]
}
