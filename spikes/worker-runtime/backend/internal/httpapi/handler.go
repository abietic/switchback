// Package httpapi exposes the fixed, local HTTP surface for the runtime spike.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"switchback.local/spikes/worker-runtime/backend/internal/service"
)

const publicBodyLimit = 1 << 20

// CreateRunRequest is the complete public create-run contract.
type CreateRunRequest struct {
	Goal               string   `json:"goal"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Constraints        []string `json:"constraints,omitempty"`
}

// CreateTurnRequest is the complete public create-turn contract.
type CreateTurnRequest struct {
	Message string `json:"message"`
}

type createdRunResponse struct {
	WorkerSessionID string `json:"worker_session_id"`
	WorkerRunID     string `json:"worker_run_id"`
	TurnID          string `json:"turn_id"`
}

type createdTurnResponse struct {
	WorkerRunID string `json:"worker_run_id"`
	TurnID      string `json:"turn_id"`
}

// Handler is intentionally implemented with net/http's fixed-path primitives.
type Handler struct {
	coordinator *service.Coordinator
}

func NewHandler(coordinator *service.Coordinator) *Handler {
	return &Handler{coordinator: coordinator}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if h.coordinator == nil {
		writeError(writer, http.StatusServiceUnavailable, "runtime API is not configured")
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/spike/runs" {
		h.createRun(writer, request)
		return
	}
	parts := pathParts(request.URL.Path)
	if request.Method == http.MethodGet && len(parts) == 3 && parts[0] == "spike" && parts[1] == "runs" {
		h.getRun(writer, parts[2])
		return
	}
	if len(parts) == 4 && parts[0] == "spike" && parts[1] == "sessions" && request.Method == http.MethodPost {
		switch parts[3] {
		case "turns":
			h.createTurn(writer, request, parts[2])
			return
		case "cleanup":
			h.cleanup(writer, request, parts[2])
			return
		}
	}
	if request.Method == http.MethodPut && isInternalUploadPath(parts) {
		h.uploadEvidence(writer, request, parts)
		return
	}
	writeError(writer, http.StatusNotFound, "route not found")
}

func (h *Handler) createRun(writer http.ResponseWriter, request *http.Request) {
	var body CreateRunRequest
	if err := decodeStrictJSON(writer, request, publicBodyLimit, &body); err != nil {
		writeDecodeError(writer, err)
		return
	}
	created, err := h.coordinator.CreateRun(request.Context(), service.CreateRunInput{
		Goal:               body.Goal,
		AcceptanceCriteria: body.AcceptanceCriteria,
		Constraints:        body.Constraints,
	})
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, createdRunResponse{
		WorkerSessionID: created.WorkerSessionID,
		WorkerRunID:     created.WorkerRunID,
		TurnID:          created.TurnID,
	})
}

func (h *Handler) getRun(writer http.ResponseWriter, runID string) {
	snapshot, err := h.coordinator.Snapshot(runID)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, snapshot)
}

func (h *Handler) createTurn(writer http.ResponseWriter, request *http.Request, sessionID string) {
	var body CreateTurnRequest
	if err := decodeStrictJSON(writer, request, publicBodyLimit, &body); err != nil {
		writeDecodeError(writer, err)
		return
	}
	created, err := h.coordinator.CreateTurn(request.Context(), sessionID, body.Message)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, createdTurnResponse{WorkerRunID: created.WorkerRunID, TurnID: created.TurnID})
}

func (h *Handler) cleanup(writer http.ResponseWriter, request *http.Request, sessionID string) {
	if err := h.coordinator.Cleanup(request.Context(), sessionID); err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "cleanup_scheduled"})
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, limit int64, target any) error {
	if request.ContentLength > limit {
		return errBodyTooLarge
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errBodyTooLarge
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errTrailingJSON
		}
		return err
	}
	return nil
}

func pathParts(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func isInternalUploadPath(parts []string) bool {
	return len(parts) == 10 && parts[0] == "internal" && parts[1] == "v1" && parts[2] == "sessions" && parts[4] == "runs" && parts[6] == "turns" && parts[8] == "evidence"
}

var (
	errBodyTooLarge = errors.New("request body is too large")
	errTrailingJSON = errors.New("request contains trailing JSON")
)

func writeDecodeError(writer http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeError(writer, http.StatusRequestEntityTooLarge, "request body exceeds limit")
		return
	}
	writeError(writer, http.StatusBadRequest, "invalid JSON request")
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(writer, http.StatusNotFound, "runtime run was not found")
	case errors.Is(err, service.ErrConflict):
		writeError(writer, http.StatusConflict, "runtime session cannot accept a turn")
	case errors.Is(err, service.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "worker authentication failed")
	case errors.Is(err, service.ErrForbidden):
		writeError(writer, http.StatusForbidden, "worker identity does not match request")
	case errors.Is(err, service.ErrNotConfigured):
		writeError(writer, http.StatusServiceUnavailable, "runtime is not configured")
	default:
		writeError(writer, http.StatusBadRequest, "request was rejected")
	}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
