package telemetry

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const (
	Service       = "switchback-backend"
	Environment   = "local"
	SchemaVersion = "switchback.log.v1"
)

// Level is one of the finite structured log severities.
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Component identifies the bounded backend subsystem.
type Component string

const (
	ComponentBackend    Component = "backend"
	ComponentWorker     Component = "worker"
	ComponentProjection Component = "projection"
	ComponentTelemetry  Component = "telemetry"
)

// EventKind identifies a safe, finite structured event.
type EventKind string

const (
	EventBackendStarted          EventKind = "backend_started"
	EventBackendStopped          EventKind = "backend_stopped"
	EventSampleFailed            EventKind = "sample_failed"
	EventSampleRecovered         EventKind = "sample_recovered"
	EventRuntimeOutputSuppressed EventKind = "runtime_output_suppressed"
	EventRunTerminal             EventKind = "run_terminal"
	EventTurnTerminal            EventKind = "turn_terminal"
	EventProjectionFailed        EventKind = "projection_failed"
	EventLogGap                  EventKind = "log_gap"
)

// Code is a finite, low-cardinality outcome code.
type Code string

const (
	CodeOK        Code = "ok"
	CodeFailed    Code = "failed"
	CodeCancelled Code = "cancelled"
	CodeTimedOut  Code = "timed_out"
	CodeUnknown   Code = "unknown"
)

// OutputSource describes a Docker stream, never a process name or raw output.
type OutputSource string

const (
	OutputStdout   OutputSource = "stdout"
	OutputStderr   OutputSource = "stderr"
	OutputCombined OutputSource = "combined"
)

// Scope contains only optional canonical identifiers; it is never a Loki label.
type Scope struct {
	TenantID          string `json:"tenant_id,omitempty"`
	WorkspaceID       string `json:"workspace_id,omitempty"`
	WorkerSessionID   string `json:"worker_session_id,omitempty"`
	WorkerRunID       string `json:"worker_run_id,omitempty"`
	AgentTurnID       string `json:"agent_turn_id,omitempty"`
	RuntimeInstanceID string `json:"runtime_instance_id,omitempty"`
	RuntimeGeneration uint64 `json:"runtime_generation,omitempty"`
}

// Event is a bounded structured log record without arbitrary payload or message fields.
type Event struct {
	TS             time.Time    `json:"ts"`
	Level          Level        `json:"level"`
	Service        string       `json:"service"`
	Component      Component    `json:"component"`
	Event          EventKind    `json:"event"`
	SchemaVersion  string       `json:"schema_version"`
	Scope          Scope        `json:"scope,omitempty"`
	Code           Code         `json:"code,omitempty"`
	DurationMillis *int64       `json:"duration_ms,omitempty"`
	Bytes          *int64       `json:"bytes,omitempty"`
	Count          *int64       `json:"count,omitempty"`
	OutputSource   OutputSource `json:"output_source,omitempty"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (e Event) valid() bool {
	if e.TS.IsZero() || e.TS.Before(time.Unix(0, 0)) || e.TS.After(time.Now().Add(24*time.Hour)) {
		return false
	}
	if e.Service != Service || e.SchemaVersion != SchemaVersion {
		return false
	}
	switch e.Level {
	case LevelInfo, LevelWarn, LevelError:
	default:
		return false
	}
	switch e.Component {
	case ComponentBackend, ComponentWorker, ComponentProjection, ComponentTelemetry:
	default:
		return false
	}
	switch e.Event {
	case EventBackendStarted, EventBackendStopped, EventSampleFailed, EventSampleRecovered, EventRuntimeOutputSuppressed, EventRunTerminal, EventTurnTerminal, EventProjectionFailed, EventLogGap:
	default:
		return false
	}
	switch e.Code {
	case "", CodeOK, CodeFailed, CodeCancelled, CodeTimedOut, CodeUnknown:
	default:
		return false
	}
	switch e.OutputSource {
	case "", OutputStdout, OutputStderr, OutputCombined:
	default:
		return false
	}
	if (e.Scope.RuntimeInstanceID == "") != (e.Scope.RuntimeGeneration == 0) {
		return false
	}
	// A resident container can contain processes from earlier Turns. Output
	// metadata cannot assert a Run/Turn owner even when a Run is currently active.
	if e.OutputSource != "" && (e.Event != EventRuntimeOutputSuppressed || e.Scope.RuntimeInstanceID == "" || e.Scope.WorkerSessionID == "" || e.Scope.WorkerRunID != "" || e.Scope.AgentTurnID != "") {
		return false
	}
	for _, id := range []string{e.Scope.TenantID, e.Scope.WorkspaceID, e.Scope.WorkerSessionID, e.Scope.WorkerRunID, e.Scope.AgentTurnID, e.Scope.RuntimeInstanceID} {
		if id != "" && !idPattern.MatchString(id) {
			return false
		}
	}
	for _, v := range []*int64{e.DurationMillis, e.Bytes, e.Count} {
		if v != nil && *v < 0 {
			return false
		}
	}
	b, err := json.Marshal(e)
	return err == nil && len(b) <= 4096
}

func (e Event) marshaled() ([]byte, error) {
	if !e.valid() {
		return nil, errors.New("invalid telemetry event")
	}
	return json.Marshal(e)
}
