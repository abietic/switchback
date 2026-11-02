package langfuse

import (
	"context"
	"fmt"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
)

type KnownTokenScanner interface {
	ScanJSON(context.Context, string, any) error
}

type Exporter struct {
	api     artifactlangfuse.API
	scanner KnownTokenScanner
}

func NewExporter(api artifactlangfuse.API, scanner KnownTokenScanner) (*Exporter, error) {
	if api == nil || scanner == nil {
		return nil, fmt.Errorf("Assistant Langfuse API and known-token scanner are required")
	}
	return &Exporter{api: api, scanner: scanner}, nil
}

func (exporter *Exporter) RecordAssistantModel(
	ctx context.Context,
	observation interactionapp.ModelObservation,
) error {
	if err := validateObservation(observation); err != nil {
		return err
	}
	traceID := "assistant/" + observation.AssistantRunID
	spanID := "assistant-span/" + observation.AssistantRunID
	generationID := "assistant-generation/" + observation.AssistantRunID
	metadata := map[string]any{
		"tenant_id": observation.TenantID, "user_id": observation.UserID,
		"conversation_id": observation.ConversationID, "assistant_run_id": observation.AssistantRunID,
		"profile_id": observation.ProfileID, "profile_revision": observation.ProfileRevision,
		"profile_digest": observation.ProfileDigest, "provider_request_id": observation.ProviderRequestID,
		"status": observation.Status, "error_code": observation.ErrorCode,
	}
	batch := artifactlangfuse.IngestionBatch{Batch: []artifactlangfuse.IngestionEvent{
		{
			ID:        "assistant-trace-event/" + observation.AssistantRunID,
			Timestamp: observation.CompletedAt, Type: "trace-create",
			Body: map[string]any{
				"id": traceID, "name": "assistant.run", "timestamp": observation.StartedAt,
				"metadata": metadata,
			},
		},
		{
			ID:        "assistant-span-event/" + observation.AssistantRunID,
			Timestamp: observation.CompletedAt, Type: "span-create",
			Body: map[string]any{
				"id": spanID, "traceId": traceID, "name": "assistant.run",
				"startTime": observation.StartedAt, "endTime": observation.CompletedAt,
				"metadata": metadata,
			},
		},
		{
			ID:        "assistant-generation-event/" + observation.AssistantRunID,
			Timestamp: observation.CompletedAt, Type: "generation-create",
			Body: map[string]any{
				"id": generationID, "traceId": traceID, "parentObservationId": spanID,
				"name": "assistant.model", "model": observation.Model,
				"startTime": observation.StartedAt, "endTime": observation.CompletedAt,
				"usageDetails": map[string]any{
					"input": observation.InputTokens, "output": observation.OutputTokens,
					"total": observation.InputTokens + observation.OutputTokens,
				},
				"level": statusLevel(observation.Status), "statusMessage": observation.Status,
				"metadata": metadata,
			},
		},
	}}
	if err := exporter.scanner.ScanJSON(ctx, "assistant.langfuse.ingestion", batch); err != nil {
		return fmt.Errorf("Assistant Langfuse batch failed known-token scan: %w", err)
	}
	if err := exporter.api.Ingest(ctx, batch); err != nil {
		return fmt.Errorf("Assistant Langfuse export failed: %w", err)
	}
	return nil
}

func validateObservation(observation interactionapp.ModelObservation) error {
	for name, value := range map[string]string{
		"tenant": observation.TenantID, "user": observation.UserID,
		"AssistantRun": observation.AssistantRunID, "Conversation": observation.ConversationID,
		"profile": observation.ProfileID, "profile digest": observation.ProfileDigest,
		"provider request": observation.ProviderRequestID, "model": observation.Model,
		"status": observation.Status,
	} {
		if value == "" {
			return fmt.Errorf("Assistant Langfuse %s is required", name)
		}
	}
	if observation.ProfileRevision == 0 || observation.InputTokens < 0 || observation.OutputTokens < 0 ||
		observation.StartedAt.IsZero() || observation.CompletedAt.Before(observation.StartedAt) {
		return fmt.Errorf("Assistant Langfuse observation is invalid")
	}
	return nil
}

func statusLevel(status string) string {
	if status == "ok" {
		return "DEFAULT"
	}
	return "ERROR"
}

var _ interactionapp.ObservabilityPort = (*Exporter)(nil)
