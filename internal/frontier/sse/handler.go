// Package frontiersse streams durable projection deltas and control events.
package frontiersse

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

type Options struct {
	Viewer       projectionapp.Viewer
	Views        *projectionapp.ViewContinuityReader
	Transient    *projectionapp.TransientBroker
	PollInterval time.Duration
	Heartbeat    time.Duration
}

type Handler struct {
	viewer       projectionapp.Viewer
	views        *projectionapp.ViewContinuityReader
	transient    *projectionapp.TransientBroker
	pollInterval time.Duration
	heartbeat    time.Duration
}

func NewHandler(options Options) (*Handler, error) {
	if options.Viewer.TenantID == "" || options.Viewer.UserID == "" || options.Views == nil {
		return nil, fmt.Errorf("Frontier SSE viewer and continuity reader are required")
	}
	poll := options.PollInterval
	if poll == 0 {
		poll = 250 * time.Millisecond
	}
	heartbeat := options.Heartbeat
	if heartbeat == 0 {
		heartbeat = 15 * time.Second
	}
	if poll <= 0 || heartbeat <= 0 {
		return nil, fmt.Errorf("Frontier SSE intervals are invalid")
	}
	return &Handler{
		viewer: options.Viewer, views: options.Views, transient: options.Transient,
		pollInterval: poll, heartbeat: heartbeat,
	}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	epoch, cursor, err := requestedCursor(request)
	if err != nil {
		http.Error(response, "invalid replay cursor", http.StatusBadRequest)
		return
	}
	resumeQuery := projectionapp.ResumeQuery{
		Viewer: handler.viewer, ViewType: request.PathValue("view_type"), ViewID: request.PathValue("view_id"),
		AfterEpoch: epoch, AfterCursor: cursor,
	}
	batch, err := handler.views.Resume(request.Context(), resumeQuery)
	if err != nil {
		if errors.Is(err, projectionapp.ErrViewUnavailable) || errors.Is(err, projectionapp.ErrInvalidViewQuery) {
			http.Error(response, "view unavailable", http.StatusNotFound)
			return
		}
		http.Error(response, "projection unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "stream unsupported", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache, no-transform")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	if writeBatch(response, batch) || request.URL.Query().Get("once") == "1" {
		flusher.Flush()
		return
	}
	var transient <-chan projectionapp.TransientDisplayDelta
	var cancelTransient func()
	if handler.transient != nil && resumeQuery.ViewType == projectionapp.ViewWorkerDetail {
		transient, cancelTransient, err = handler.transient.Subscribe(resumeQuery.ViewID)
		if err != nil {
			return
		}
		defer cancelTransient()
	}
	if len(batch.Deltas) > 0 {
		last := batch.Deltas[len(batch.Deltas)-1]
		resumeQuery.AfterEpoch, resumeQuery.AfterCursor = last.ViewEpoch, last.ViewCursor
	}
	flusher.Flush()
	poll := time.NewTicker(handler.pollInterval)
	heartbeat := time.NewTicker(handler.heartbeat)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case delta, ok := <-transient:
			if !ok {
				transient = nil
				continue
			}
			if err := WriteTransient(response, delta); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(response, ": heartbeat\n\n")
			flusher.Flush()
		case <-poll.C:
			batch, err := handler.views.Resume(request.Context(), resumeQuery)
			if err != nil {
				return
			}
			if writeBatch(response, batch) {
				flusher.Flush()
				return
			}
			if len(batch.Deltas) > 0 {
				last := batch.Deltas[len(batch.Deltas)-1]
				resumeQuery.AfterEpoch, resumeQuery.AfterCursor = last.ViewEpoch, last.ViewCursor
				flusher.Flush()
			}
		}
	}
}

func requestedCursor(request *http.Request) (uint64, uint64, error) {
	value := request.Header.Get("Last-Event-ID")
	if value == "" {
		value = request.URL.Query().Get("cursor")
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("cursor must contain epoch and sequence")
	}
	epoch, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || epoch == 0 {
		return 0, 0, fmt.Errorf("cursor epoch is invalid")
	}
	cursor, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("cursor sequence is invalid")
	}
	return epoch, cursor, nil
}

func writeBatch(response http.ResponseWriter, batch projectionapp.ResumeResult) bool {
	if batch.Control != nil {
		writeSSE(response, "", batch.Control.EventType, batch.Control)
		return true
	}
	for _, delta := range batch.Deltas {
		writeSSE(response, fmt.Sprintf("%d:%d", delta.ViewEpoch, delta.ViewCursor), "projection_delta", delta)
	}
	return false
}

func writeSSE(response http.ResponseWriter, id, event string, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	if id != "" {
		_, _ = fmt.Fprintf(response, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, encoded)
}

// WriteTransient emits a best-effort display event without an SSE id. It can
// never advance a durable View cursor.
func WriteTransient(writer io.Writer, delta projectionapp.TransientDisplayDelta) error {
	if writer == nil {
		return fmt.Errorf("transient SSE writer is required")
	}
	if err := projectionapp.ValidateTransientDelta(delta); err != nil {
		return err
	}
	encoded, err := json.Marshal(delta)
	if err != nil {
		return fmt.Errorf("encode transient display event: %w", err)
	}
	_, err = fmt.Fprintf(writer, "event: transient_display_delta\ndata: %s\n\n", encoded)
	return err
}
