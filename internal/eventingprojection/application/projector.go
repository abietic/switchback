package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ViewHomeDashboard         = "home_dashboard"
	ViewTaskSummary           = "task_summary"
	ViewWorkerDetail          = "worker_detail"
	ViewArtifactDetail        = "artifact_detail"
	ViewCommandStatus         = "command_status"
	ViewAssistantConversation = "assistant_conversation"

	ProjectionSnapshotSchemaVersion = "frontier.projection_snapshot.v1"
	ProjectionDeltaSchemaVersion    = "frontier.projection_delta.v1"
)

type ActionRef struct {
	Action           string          `json:"action"`
	TargetID         string          `json:"target_id"`
	ExpectedRevision uint64          `json:"expected_revision"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type RelatedRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Href string `json:"href"`
}

type SourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type ViewUpdate struct {
	DeltaID          string          `json:"delta_id"`
	TenantID         string          `json:"tenant_id"`
	UserID           string          `json:"user_id"`
	ViewType         string          `json:"view_type"`
	ViewID           string          `json:"view_id"`
	QueryFingerprint string          `json:"query_fingerprint"`
	Data             json.RawMessage `json:"data"`
	AvailableActions []ActionRef     `json:"available_actions"`
	RelatedRefs      []RelatedRef    `json:"related_refs"`
	EventType        string          `json:"event_type"`
	Critical         bool            `json:"critical"`
	CommandID        string          `json:"command_id,omitempty"`
	ClientCommandID  string          `json:"client_command_id,omitempty"`
	SourceRefs       []SourceRef     `json:"source_refs"`
	DeltaPayload     json.RawMessage `json:"delta_payload"`
}

type ProjectionEvent struct {
	SourceEventID  string       `json:"source_event_id"`
	SourceStreamID string       `json:"source_stream_id"`
	SourceSequence uint64       `json:"source_sequence"`
	ProjectedAt    time.Time    `json:"projected_at"`
	Updates        []ViewUpdate `json:"updates"`
}

type ViewProjection struct {
	SchemaVersion     string          `json:"schema_version"`
	TenantID          string          `json:"tenant_id"`
	UserID            string          `json:"user_id"`
	ViewType          string          `json:"view_type"`
	ViewID            string          `json:"view_id"`
	ViewEpoch         uint64          `json:"view_epoch"`
	ViewCursor        uint64          `json:"view_cursor"`
	ProjectionVersion uint64          `json:"projection_version"`
	QueryFingerprint  string          `json:"query_fingerprint"`
	Data              json.RawMessage `json:"data"`
	AvailableActions  []ActionRef     `json:"available_actions"`
	RelatedRefs       []RelatedRef    `json:"related_refs"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type ProjectionStore interface {
	ApplyProjectionEvent(context.Context, ProjectionEvent, string) ([]ViewProjection, error)
}

type SourceDigestProjectionStore interface {
	ApplyProjectionEventWithSourceDigest(
		context.Context,
		ProjectionEvent,
		string,
		string,
	) ([]ViewProjection, error)
}

type Projector struct {
	Store ProjectionStore
}

func (projector Projector) Execute(ctx context.Context, event ProjectionEvent) ([]ViewProjection, error) {
	if projector.Store == nil {
		return nil, fmt.Errorf("projection store is required")
	}
	if err := ValidateProjectionEvent(event); err != nil {
		return nil, err
	}
	digest, err := ProjectionEventDigest(event)
	if err != nil {
		return nil, err
	}
	return projector.Store.ApplyProjectionEvent(ctx, event, digest)
}

// ExecuteWithSourceDigest persists a canonical source-event digest as receipt
// metadata without changing the backward-compatible ProjectionEvent digest.
func (projector Projector) ExecuteWithSourceDigest(
	ctx context.Context,
	event ProjectionEvent,
	sourceDigest string,
) ([]ViewProjection, error) {
	store, ok := projector.Store.(SourceDigestProjectionStore)
	if projector.Store == nil || !ok {
		return nil, fmt.Errorf("source-digest projection store is required")
	}
	if err := ValidateProjectionEvent(event); err != nil {
		return nil, err
	}
	if !validSHA256Digest(sourceDigest) {
		return nil, fmt.Errorf("projection source digest is invalid")
	}
	digest, err := ProjectionEventDigest(event)
	if err != nil {
		return nil, err
	}
	return store.ApplyProjectionEventWithSourceDigest(ctx, event, digest, sourceDigest)
}

func ValidateProjectionEvent(event ProjectionEvent) error {
	if !canonical(event.SourceEventID) || !canonical(event.SourceStreamID) || event.SourceSequence == 0 ||
		event.ProjectedAt.IsZero() || len(event.Updates) == 0 {
		return fmt.Errorf("projection source event is invalid")
	}
	targets := make(map[string]struct{}, len(event.Updates))
	deltas := make(map[string]struct{}, len(event.Updates))
	for _, update := range event.Updates {
		if !canonical(update.DeltaID) || !canonical(update.TenantID) || !canonical(update.UserID) ||
			!validViewType(update.ViewType) || !canonical(update.ViewID) || !canonical(update.QueryFingerprint) ||
			update.AvailableActions == nil || update.RelatedRefs == nil || update.SourceRefs == nil ||
			!validEventType(update.EventType) || !jsonObject(update.Data) || !jsonObject(update.DeltaPayload) {
			return fmt.Errorf("projection View update is invalid")
		}
		if update.CommandID != "" && !canonical(update.CommandID) {
			return fmt.Errorf("projection command identity is invalid")
		}
		if update.ClientCommandID != "" && !canonical(update.ClientCommandID) {
			return fmt.Errorf("projection client command identity is invalid")
		}
		if update.ClientCommandID != "" && update.CommandID == "" {
			return fmt.Errorf("projection client command identity requires server command identity")
		}
		for _, action := range update.AvailableActions {
			if !canonical(action.Action) || !canonical(action.TargetID) || action.ExpectedRevision == 0 ||
				(action.Metadata != nil && !jsonObject(action.Metadata)) {
				return fmt.Errorf("projection action reference is invalid")
			}
		}
		for _, ref := range update.RelatedRefs {
			if !canonical(ref.Type) || !canonical(ref.ID) || !strings.HasPrefix(ref.Href, "/api/v1/") {
				return fmt.Errorf("projection related reference is invalid")
			}
		}
		for _, ref := range update.SourceRefs {
			if !canonical(ref.Type) || !canonical(ref.ID) {
				return fmt.Errorf("projection source reference is invalid")
			}
		}
		target := update.TenantID + "\x00" + update.UserID + "\x00" + update.ViewType + "\x00" + update.ViewID
		if _, duplicate := targets[target]; duplicate {
			return fmt.Errorf("projection event updates one View more than once")
		}
		if _, duplicate := deltas[update.DeltaID]; duplicate {
			return fmt.Errorf("projection delta identity is duplicated")
		}
		targets[target] = struct{}{}
		deltas[update.DeltaID] = struct{}{}
	}
	return nil
}

func ProjectionEventDigest(event ProjectionEvent) (string, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("encode projection event for digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validViewType(value string) bool {
	return value == ViewHomeDashboard || value == ViewTaskSummary || value == ViewWorkerDetail || value == ViewArtifactDetail ||
		value == ViewCommandStatus || value == ViewAssistantConversation
}

func validEventType(value string) bool {
	switch value {
	case "view.entity_upserted.v1", "view.entity_removed.v1", "view.timeline_appended.v1",
		"view.available_actions_changed.v1", "view.attention_changed.v1", "view.artifact_ready.v1",
		"view.stream_checkpointed.v1":
		return true
	default:
		return false
	}
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(value) > 0 && json.Unmarshal(value, &object) == nil && object != nil
}

func canonical(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
