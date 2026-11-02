package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	DisplayCheckpointSchemaVersion = "frontier.display_checkpoint.v1"
	DisplayPartSchemaVersion       = "frontier.display_part.v1"
	TransientDeltaSchemaVersion    = "frontier.transient_display_delta.v1"
)

var (
	ErrDisplayCheckpointConflict = errors.New("DisplayCheckpoint content conflicts with durable state")
	ErrTransientGap              = errors.New("transient display sequence has a gap")
	ErrTransientBaseMismatch     = errors.New("transient display checkpoint base is stale")
)

type DisplayPart struct {
	SchemaVersion    string          `json:"schema_version"`
	PartID           string          `json:"part_id"`
	PartType         string          `json:"part_type"`
	PartRevision     uint64          `json:"part_revision"`
	Ordinal          uint64          `json:"ordinal"`
	LifecycleStatus  string          `json:"lifecycle_status"`
	Critical         bool            `json:"critical"`
	Visibility       json.RawMessage `json:"visibility"`
	Provenance       json.RawMessage `json:"provenance"`
	AvailableActions []ActionRef     `json:"available_actions"`
	Payload          json.RawMessage `json:"payload"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type DisplayCheckpoint struct {
	SchemaVersion                 string          `json:"schema_version"`
	DisplayStreamID               string          `json:"display_stream_id"`
	TenantID                      string          `json:"tenant_id"`
	UserID                        string          `json:"user_id"`
	WorkspaceID                   string          `json:"workspace_id"`
	WorkerSessionID               string          `json:"worker_session_id"`
	WorkerRunID                   string          `json:"worker_run_id"`
	AgentTurnID                   string          `json:"agent_turn_id"`
	MessageID                     string          `json:"message_id,omitempty"`
	CheckpointVersion             uint64          `json:"checkpoint_version"`
	Status                        string          `json:"status"`
	Parts                         []DisplayPart   `json:"parts"`
	LastObservedTransientSequence uint64          `json:"last_observed_transient_sequence"`
	TraceCursorRef                json.RawMessage `json:"trace_cursor_ref"`
	DurabilityPolicyRef           json.RawMessage `json:"durability_policy_ref"`
	CheckpointedAt                time.Time       `json:"checkpointed_at"`
}

type TransientDisplayDelta struct {
	SchemaVersion         string          `json:"schema_version"`
	DisplayStreamID       string          `json:"display_stream_id"`
	MessageID             string          `json:"message_id,omitempty"`
	PartID                string          `json:"part_id"`
	BaseCheckpointVersion uint64          `json:"base_checkpoint_version"`
	TransientSequence     uint64          `json:"transient_sequence"`
	DeltaType             string          `json:"delta_type"`
	Payload               json.RawMessage `json:"payload"`
	EmittedAt             time.Time       `json:"emitted_at"`
}

type CheckpointStore interface {
	SaveDisplayCheckpoint(context.Context, DisplayCheckpoint, string, string) error
}

type DisplayCheckpointService struct {
	Checkpoints CheckpointStore
	Projector   Projector
}

func (service DisplayCheckpointService) Save(
	ctx context.Context,
	checkpoint DisplayCheckpoint,
	event ProjectionEvent,
) ([]ViewProjection, error) {
	if service.Checkpoints == nil || service.Projector.Store == nil {
		return nil, fmt.Errorf("DisplayCheckpoint dependencies are required")
	}
	if err := ValidateDisplayCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	if err := validateCheckpointProjection(checkpoint, event); err != nil {
		return nil, err
	}
	digest, err := DisplayCheckpointDigest(checkpoint)
	if err != nil {
		return nil, err
	}
	if err := service.Checkpoints.SaveDisplayCheckpoint(ctx, checkpoint, digest, event.SourceEventID); err != nil {
		return nil, err
	}
	return service.Projector.Execute(ctx, event)
}

func ValidateDisplayCheckpoint(checkpoint DisplayCheckpoint) error {
	if checkpoint.SchemaVersion != DisplayCheckpointSchemaVersion || !canonical(checkpoint.DisplayStreamID) ||
		!canonical(checkpoint.TenantID) || !canonical(checkpoint.UserID) || !canonical(checkpoint.WorkspaceID) ||
		!canonical(checkpoint.WorkerSessionID) || !canonical(checkpoint.WorkerRunID) ||
		!canonical(checkpoint.AgentTurnID) || checkpoint.CheckpointVersion == 0 ||
		!validDisplayStatus(checkpoint.Status) || checkpoint.Parts == nil ||
		!jsonObject(checkpoint.TraceCursorRef) || !jsonObject(checkpoint.DurabilityPolicyRef) ||
		checkpoint.CheckpointedAt.IsZero() {
		return fmt.Errorf("DisplayCheckpoint is invalid")
	}
	partIDs := make(map[string]struct{}, len(checkpoint.Parts))
	ordinals := make(map[uint64]struct{}, len(checkpoint.Parts))
	for _, part := range checkpoint.Parts {
		if part.SchemaVersion != DisplayPartSchemaVersion || !canonical(part.PartID) ||
			!validPartType(part.PartType) || part.PartRevision == 0 || !validPartStatus(part.LifecycleStatus) ||
			!jsonObject(part.Visibility) || !jsonObject(part.Provenance) || part.AvailableActions == nil ||
			!jsonObject(part.Payload) || part.CreatedAt.IsZero() || part.UpdatedAt.Before(part.CreatedAt) {
			return fmt.Errorf("DisplayPart is invalid")
		}
		if _, exists := partIDs[part.PartID]; exists {
			return fmt.Errorf("DisplayPart identity is duplicated")
		}
		if _, exists := ordinals[part.Ordinal]; exists {
			return fmt.Errorf("DisplayPart ordinal is duplicated")
		}
		partIDs[part.PartID] = struct{}{}
		ordinals[part.Ordinal] = struct{}{}
	}
	return nil
}

func ValidateTransientDelta(delta TransientDisplayDelta) error {
	if delta.SchemaVersion != TransientDeltaSchemaVersion || !canonical(delta.DisplayStreamID) ||
		!canonical(delta.PartID) || delta.BaseCheckpointVersion == 0 || delta.TransientSequence == 0 ||
		!validDeltaType(delta.DeltaType) || !jsonObject(delta.Payload) || delta.EmittedAt.IsZero() {
		return fmt.Errorf("TransientDisplayDelta is invalid")
	}
	return nil
}

func validateCheckpointProjection(checkpoint DisplayCheckpoint, event ProjectionEvent) error {
	if err := ValidateProjectionEvent(event); err != nil || len(event.Updates) != 1 {
		return fmt.Errorf("DisplayCheckpoint projection event is invalid")
	}
	update := event.Updates[0]
	if update.TenantID != checkpoint.TenantID || update.UserID != checkpoint.UserID ||
		update.ViewType != ViewWorkerDetail || update.ViewID != checkpoint.WorkerSessionID ||
		update.EventType != "view.stream_checkpointed.v1" || event.ProjectedAt != checkpoint.CheckpointedAt {
		return fmt.Errorf("DisplayCheckpoint projection provenance mismatch")
	}
	return nil
}

func DisplayCheckpointDigest(checkpoint DisplayCheckpoint) (string, error) {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return "", fmt.Errorf("encode DisplayCheckpoint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type TransientPolicy struct {
	MaxItems int
	MaxBytes int64
}

type transientStream struct {
	baseVersion  uint64
	lastSequence uint64
	items        []transientItem
	bytes        int64
}

type transientItem struct {
	delta TransientDisplayDelta
	size  int64
}

type TransientBroker struct {
	mu          sync.Mutex
	policy      TransientPolicy
	streams     map[string]*transientStream
	owners      map[string]string
	subscribers map[string]map[chan TransientDisplayDelta]struct{}
}

func NewTransientBroker(policy TransientPolicy) (*TransientBroker, error) {
	if policy.MaxItems < 1 || policy.MaxBytes < 1024 {
		return nil, fmt.Errorf("transient display policy is invalid")
	}
	return &TransientBroker{
		policy: policy, streams: make(map[string]*transientStream),
		owners:      make(map[string]string),
		subscribers: make(map[string]map[chan TransientDisplayDelta]struct{}),
	}, nil
}

func (broker *TransientBroker) Register(
	displayStreamID, workerSessionID string,
) error {
	if broker == nil || !canonical(displayStreamID) || !canonical(workerSessionID) {
		return fmt.Errorf("transient Display ownership is invalid")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if existing := broker.owners[displayStreamID]; existing != "" && existing != workerSessionID {
		return ErrDisplayCheckpointConflict
	}
	broker.owners[displayStreamID] = workerSessionID
	return nil
}

func (broker *TransientBroker) Subscribe(
	workerSessionID string,
) (<-chan TransientDisplayDelta, func(), error) {
	if broker == nil || !canonical(workerSessionID) {
		return nil, nil, fmt.Errorf("transient Display subscription is invalid")
	}
	updates := make(chan TransientDisplayDelta, 64)
	broker.mu.Lock()
	if broker.subscribers[workerSessionID] == nil {
		broker.subscribers[workerSessionID] = make(map[chan TransientDisplayDelta]struct{})
	}
	broker.subscribers[workerSessionID][updates] = struct{}{}
	broker.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			broker.mu.Lock()
			delete(broker.subscribers[workerSessionID], updates)
			if len(broker.subscribers[workerSessionID]) == 0 {
				delete(broker.subscribers, workerSessionID)
			}
			close(updates)
			broker.mu.Unlock()
		})
	}
	return updates, cancel, nil
}

func (broker *TransientBroker) Publish(delta TransientDisplayDelta) error {
	if err := ValidateTransientDelta(delta); err != nil {
		return err
	}
	encoded, err := json.Marshal(delta)
	if err != nil {
		return fmt.Errorf("encode TransientDisplayDelta: %w", err)
	}
	size := int64(len(encoded))
	if size > broker.policy.MaxBytes {
		return fmt.Errorf("TransientDisplayDelta exceeds lane byte limit")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	stream := broker.streams[delta.DisplayStreamID]
	if stream == nil {
		stream = &transientStream{baseVersion: delta.BaseCheckpointVersion}
		broker.streams[delta.DisplayStreamID] = stream
	}
	if stream.baseVersion != delta.BaseCheckpointVersion {
		return ErrTransientBaseMismatch
	}
	if delta.TransientSequence != stream.lastSequence+1 {
		return ErrTransientGap
	}
	stream.items = append(stream.items, transientItem{delta: delta, size: size})
	stream.bytes += size
	stream.lastSequence = delta.TransientSequence
	for len(stream.items) > broker.policy.MaxItems || stream.bytes > broker.policy.MaxBytes {
		stream.bytes -= stream.items[0].size
		stream.items = stream.items[1:]
	}
	if workerSessionID := broker.owners[delta.DisplayStreamID]; workerSessionID != "" {
		for subscriber := range broker.subscribers[workerSessionID] {
			select {
			case subscriber <- delta:
			default:
			}
		}
	}
	return nil
}

func (broker *TransientBroker) ReadAfter(
	displayStreamID string,
	baseVersion, afterSequence uint64,
) ([]TransientDisplayDelta, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	stream := broker.streams[displayStreamID]
	if stream == nil || stream.baseVersion != baseVersion {
		return nil, ErrTransientBaseMismatch
	}
	if len(stream.items) > 0 && afterSequence+1 < stream.items[0].delta.TransientSequence {
		return nil, ErrTransientGap
	}
	result := make([]TransientDisplayDelta, 0, len(stream.items))
	for _, item := range stream.items {
		if item.delta.TransientSequence > afterSequence {
			result = append(result, item.delta)
		}
	}
	return result, nil
}

func (broker *TransientBroker) Supersede(checkpoint DisplayCheckpoint) error {
	if err := ValidateDisplayCheckpoint(checkpoint); err != nil {
		return err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	broker.streams[checkpoint.DisplayStreamID] = &transientStream{
		baseVersion: checkpoint.CheckpointVersion, lastSequence: checkpoint.LastObservedTransientSequence,
	}
	return nil
}

func validDisplayStatus(value string) bool {
	return value == "streaming" || value == "waiting_user" || value == "completed" || value == "failed" ||
		value == "cancelled" || value == "timed_out"
}

func validPartStatus(value string) bool {
	return value == "pending" || value == "streaming" || value == "waiting_user" || value == "completed" ||
		value == "failed" || value == "cancelled" || value == "timed_out" || value == "expired"
}

func validPartType(value string) bool {
	return value == "text" || value == "reasoning_summary" || value == "tool" || value == "activity" ||
		value == "approval" || value == "artifact" || value == "source" || value == "error"
}

func validDeltaType(value string) bool {
	return value == "text_delta" || value == "reasoning_summary_delta" || value == "tool_args_delta" ||
		value == "progress_pulse"
}
