// Package frontierhttp exposes permission-filtered projection snapshots.
package frontierhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

type Options struct {
	Viewer                projectionapp.Viewer
	Views                 *projectionapp.ViewContinuityReader
	Streams               http.Handler
	Timeline              projectionapp.WorkerTimelineReader
	Diagnostics           projectionapp.WorkerDiagnosticReader
	WorkspaceID           string
	GrafanaBrowserBaseURL string
}

func NewHandler(options Options) (http.Handler, error) {
	if options.Viewer.TenantID == "" || options.Viewer.UserID == "" || options.Views == nil {
		return nil, fmt.Errorf("Frontier viewer and continuity reader are required")
	}
	handler := &handler{
		viewer: options.Viewer, views: options.Views, timeline: options.Timeline,
		diagnostics: options.Diagnostics, workspaceID: options.WorkspaceID, grafanaBrowserBaseURL: options.GrafanaBrowserBaseURL,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/views/{view_type}/{view_id}", handler.snapshot)
	if options.Diagnostics != nil {
		mux.HandleFunc("GET /api/v1/views/worker_detail/{view_id}/diagnostics", handler.workerDiagnostics)
	}
	if options.Timeline != nil {
		mux.HandleFunc(
			"GET /api/v1/views/worker_detail/{view_id}/timeline",
			handler.workerTimeline,
		)
	}
	if options.Streams != nil {
		mux.Handle("GET /api/v1/views/{view_type}/{view_id}/events", options.Streams)
	}
	return mux, nil
}

type handler struct {
	viewer                projectionapp.Viewer
	views                 *projectionapp.ViewContinuityReader
	timeline              projectionapp.WorkerTimelineReader
	diagnostics           projectionapp.WorkerDiagnosticReader
	workspaceID           string
	grafanaBrowserBaseURL string
}

func (handler *handler) snapshot(response http.ResponseWriter, request *http.Request) {
	viewType := request.PathValue("view_type")
	viewID := request.PathValue("view_id")
	reconcileIDs, present := request.URL.Query()["reconcile_client_command_id"]
	if !present {
		reconcileIDs = nil
	}
	snapshot, err := handler.views.ReadSnapshot(request.Context(), projectionapp.SnapshotQuery{
		Viewer:                    handler.viewer,
		ViewType:                  viewType,
		ViewID:                    viewID,
		ReconcileClientCommandIDs: reconcileIDs,
	})
	if err != nil {
		if errors.Is(err, projectionapp.ErrInvalidCommandReconciliation) {
			writeError(response, http.StatusBadRequest, "invalid_command_reconciliation")
			return
		}
		if errors.Is(err, projectionapp.ErrViewUnavailable) || errors.Is(err, projectionapp.ErrInvalidViewQuery) {
			writeError(response, http.StatusNotFound, "view_unavailable")
			return
		}
		writeError(response, http.StatusServiceUnavailable, "projection_unavailable")
		return
	}
	etag := fmt.Sprintf(`"%s:%s:%d:%d"`, snapshot.ViewType, snapshot.ViewID,
		snapshot.ViewEpoch, snapshot.ProjectionVersion)
	if present {
		digest, digestErr := projectionapp.CommandReconciliationDigest(reconcileIDs, snapshot.CommandReceipts)
		if digestErr != nil {
			writeError(response, http.StatusServiceUnavailable, "projection_unavailable")
			return
		}
		etag = fmt.Sprintf(`"%s:%s:%d:%d:receipts-%s"`, snapshot.ViewType, snapshot.ViewID,
			snapshot.ViewEpoch, snapshot.ProjectionVersion, digest)
	}
	response.Header().Set("ETag", etag)
	response.Header().Set("Cache-Control", "private, no-cache")
	if request.Header.Get("If-None-Match") == etag {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(snapshot)
}

func (handler *handler) workerTimeline(response http.ResponseWriter, request *http.Request) {
	limit := 20
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_timeline_query")
			return
		}
		limit = parsed
	}
	page, err := handler.timeline.ReadWorkerTimeline(
		request.Context(),
		projectionapp.WorkerTimelineQuery{
			Viewer: handler.viewer, WorkerSessionID: request.PathValue("view_id"),
			BeforeCursor: request.URL.Query().Get("before"), Limit: limit,
		},
	)
	if err != nil {
		if errors.Is(err, projectionapp.ErrInvalidViewQuery) {
			writeError(response, http.StatusBadRequest, "invalid_timeline_query")
			return
		}
		if errors.Is(err, projectionapp.ErrViewUnavailable) {
			writeError(response, http.StatusNotFound, "view_unavailable")
			return
		}
		writeError(response, http.StatusServiceUnavailable, "projection_unavailable")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "private, no-cache")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(page)
}

func writeError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
