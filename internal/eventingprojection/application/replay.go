package application

import (
	"encoding/json"
)

const ProjectionControlSchemaVersion = "frontier.projection_control.v1"

type ProjectionDelta struct {
	SchemaVersion           string          `json:"schema_version"`
	DeltaID                 string          `json:"delta_id"`
	ViewType                string          `json:"view_type"`
	ViewID                  string          `json:"view_id"`
	ViewEpoch               uint64          `json:"view_epoch"`
	ViewCursor              uint64          `json:"view_cursor"`
	BaseProjectionVersion   uint64          `json:"base_projection_version"`
	ResultProjectionVersion uint64          `json:"result_projection_version"`
	EventType               string          `json:"event_type"`
	Critical                bool            `json:"critical"`
	CommandID               string          `json:"command_id,omitempty"`
	ClientCommandID         string          `json:"client_command_id,omitempty"`
	SourceRefs              []SourceRef     `json:"source_refs"`
	Payload                 json.RawMessage `json:"payload"`
	EncodedSizeBytes        int64           `json:"-"`
}

type ProjectionControlEvent struct {
	SchemaVersion  string `json:"schema_version"`
	EventType      string `json:"event_type"`
	ViewType       string `json:"view_type"`
	ViewID         string `json:"view_id"`
	ViewEpoch      uint64 `json:"view_epoch"`
	SnapshotCursor uint64 `json:"snapshot_cursor"`
}
