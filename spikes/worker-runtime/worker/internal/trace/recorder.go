// Package trace records authoritative Worker-side runtime events as JSONL.
package trace

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"switchback.local/spikes/worker-runtime/internal/redact"
)

const (
	TypeRawFrame      = "acp.raw_frame"
	TypeUnknownUpdate = "acp.unknown_update"
)

// Event is one self-contained JSONL trace record.
type Event struct {
	Type       string `json:"type"`
	Direction  string `json:"direction,omitempty"`
	Frame      string `json:"frame,omitempty"`
	UpdateType string `json:"update_type,omitempty"`
}

// Recorder appends complete events to an authoritative trace.
type Recorder interface {
	Record(Event) error
}

// JSONLRecorder serializes concurrent records without interleaving lines.
type JSONLRecorder struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	redactor redact.Redactor
}

// NewJSONLRecorder writes one JSON object per line to destination.
func NewJSONLRecorder(destination io.Writer) (*JSONLRecorder, error) {
	return NewRedactingJSONLRecorder(destination, redact.Redactor{})
}

// NewRedactingJSONLRecorder removes configured exact tokens from frame payloads
// before they enter the authoritative JSONL trace.
func NewRedactingJSONLRecorder(
	destination io.Writer,
	redactor redact.Redactor,
) (*JSONLRecorder, error) {
	if destination == nil {
		return nil, fmt.Errorf("trace destination is required")
	}
	return &JSONLRecorder{
		encoder:  json.NewEncoder(destination),
		redactor: redactor,
	}, nil
}

// Record appends one complete event.
func (r *JSONLRecorder) Record(event Event) error {
	if r == nil || r.encoder == nil {
		return fmt.Errorf("trace recorder is not initialized")
	}
	if event.Type == "" {
		return fmt.Errorf("trace event type is required")
	}
	event.Frame = r.redactor.Redact(event.Frame)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.encoder.Encode(event); err != nil {
		return fmt.Errorf("append trace event: %w", err)
	}
	return nil
}
