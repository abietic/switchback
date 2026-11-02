package contractsv1

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	IntegrationEventLegacySchemaVersion = "event.v1"
	IntegrationEventSchemaVersion       = "event.v2"
)

type IntegrationEvent struct {
	SchemaVersion   string          `json:"schema_version"`
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	ProducerContext string          `json:"producer_context"`
	StreamID        string          `json:"stream_id"`
	Sequence        uint64          `json:"sequence"`
	TenantID        string          `json:"tenant_id"`
	AggregateType   string          `json:"aggregate_type"`
	AggregateID     string          `json:"aggregate_id"`
	TraceID         string          `json:"trace_id"`
	CorrelationID   string          `json:"correlation_id"`
	CausationID     string          `json:"causation_id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	ClientCommandID string          `json:"client_command_id,omitempty"`
	Visibility      string          `json:"visibility"`
	OccurredAt      time.Time       `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload"`
}

func DecodeIntegrationEvent(data []byte) (IntegrationEvent, error) {
	var header struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return IntegrationEvent{}, fmt.Errorf("decode IntegrationEvent schema version: %w", err)
	}
	switch header.SchemaVersion {
	case IntegrationEventLegacySchemaVersion:
		legacy, err := decodeStrict(data, func(event integrationEventV1) error {
			return event.current().Validate()
		})
		if err != nil {
			return IntegrationEvent{}, err
		}
		return legacy.current(), nil
	case IntegrationEventSchemaVersion:
		return decodeStrict(data, IntegrationEvent.Validate)
	default:
		return IntegrationEvent{}, fmt.Errorf("unsupported IntegrationEvent schema %q", header.SchemaVersion)
	}
}

func (event IntegrationEvent) Validate() error {
	if event.SchemaVersion != IntegrationEventLegacySchemaVersion &&
		event.SchemaVersion != IntegrationEventSchemaVersion {
		return fmt.Errorf("unsupported IntegrationEvent schema %q", event.SchemaVersion)
	}
	if event.SchemaVersion == IntegrationEventLegacySchemaVersion && event.ClientCommandID != "" {
		return fmt.Errorf("client_command_id requires IntegrationEvent schema %q", IntegrationEventSchemaVersion)
	}
	for name, value := range map[string]string{
		"event_id": event.EventID, "event_type": event.EventType,
		"producer_context": event.ProducerContext, "stream_id": event.StreamID,
		"tenant_id": event.TenantID, "aggregate_type": event.AggregateType,
		"aggregate_id": event.AggregateID, "trace_id": event.TraceID,
		"correlation_id": event.CorrelationID, "causation_id": event.CausationID,
		"idempotency_key": event.IdempotencyKey,
	} {
		if err := requireString(name, value); err != nil {
			return err
		}
	}
	if event.Sequence == 0 {
		return fmt.Errorf("IntegrationEvent sequence must be positive")
	}
	if event.ClientCommandID != "" {
		if err := requireString("client_command_id", event.ClientCommandID); err != nil {
			return err
		}
	}
	if event.Visibility != "internal" && event.Visibility != "user" {
		return fmt.Errorf("unsupported event visibility %q", event.Visibility)
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("occurred_at is required")
	}
	return requireJSONObject("payload", event.Payload)
}

// integrationEventV1 is deliberately a separate wire type. It keeps the
// legacy schema closed so a v2-only client identity cannot be silently lost by
// an event.v1 consumer.
type integrationEventV1 struct {
	SchemaVersion   string          `json:"schema_version"`
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	ProducerContext string          `json:"producer_context"`
	StreamID        string          `json:"stream_id"`
	Sequence        uint64          `json:"sequence"`
	TenantID        string          `json:"tenant_id"`
	AggregateType   string          `json:"aggregate_type"`
	AggregateID     string          `json:"aggregate_id"`
	TraceID         string          `json:"trace_id"`
	CorrelationID   string          `json:"correlation_id"`
	CausationID     string          `json:"causation_id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Visibility      string          `json:"visibility"`
	OccurredAt      time.Time       `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload"`
}

func (event integrationEventV1) current() IntegrationEvent {
	return IntegrationEvent{
		SchemaVersion: event.SchemaVersion, EventID: event.EventID, EventType: event.EventType,
		ProducerContext: event.ProducerContext, StreamID: event.StreamID, Sequence: event.Sequence,
		TenantID: event.TenantID, AggregateType: event.AggregateType, AggregateID: event.AggregateID,
		TraceID: event.TraceID, CorrelationID: event.CorrelationID, CausationID: event.CausationID,
		IdempotencyKey: event.IdempotencyKey, Visibility: event.Visibility, OccurredAt: event.OccurredAt,
		Payload: event.Payload,
	}
}
