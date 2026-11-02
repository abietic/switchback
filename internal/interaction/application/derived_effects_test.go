package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/application/testkit"
)

func TestDerivedEffectsDegradeIndependentlyAndPersistSanitizedStatus(t *testing.T) {
	now := time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)
	objects := &testkit.ObjectStore{Err: errors.New("object failure with sk-sensitive")}
	observability := &testkit.Observability{Err: errors.New("Langfuse failure with secret")}
	statuses := &derivedEffectStore{}
	result, err := (interactionapp.PublishAssistantDerivedEffects{
		Objects: objects, Observability: observability, Store: statuses,
		Clock: testkit.NewClock(now),
	}).Execute(context.Background(), interactionapp.PublishAssistantDerivedEffectsCommand{
		Evidence: &interactionapp.EvidenceBoundary{
			TenantID: "tenant-local", UserID: "user-local", ConversationID: "conversation-1",
			ConversationGeneration: 1, AssistantRunID: "assistant-run-1",
			BoundaryType: "run_failed", Records: []json.RawMessage{json.RawMessage(`{"status":"failed"}`)},
			Metadata: map[string]string{},
		},
		Observation: &interactionapp.ModelObservation{
			TenantID: "tenant-local", UserID: "user-local", AssistantRunID: "assistant-run-1",
			ConversationID: "conversation-1", ProfileID: "assistant-profile",
			ProfileRevision:   1,
			ProfileDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProviderRequestID: "provider-request-1", Model: "provider/model",
			StartedAt: now.Add(-time.Second), CompletedAt: now,
			Status: "ok",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EvidenceStatus != interactionapp.DerivedEffectDegraded ||
		result.EvidenceErrorCode != "object_store_write_failed" ||
		result.ObservabilityStatus != interactionapp.DerivedEffectDegraded ||
		result.ObservabilityErrorCode != "langfuse_projection_failed" {
		t.Fatalf("derived effect result = %+v", result)
	}
	if len(objects.Boundaries) != 1 || len(observability.Observations) != 1 ||
		len(statuses.records) != 1 {
		t.Fatalf("independent effects were not all attempted")
	}
	record := statuses.records[0]
	if record.EvidenceErrorCode != "object_store_write_failed" ||
		record.ObservabilityErrorCode != "langfuse_projection_failed" ||
		record.LangfuseTraceID != "" {
		t.Fatalf("raw downstream failure escaped into status: %+v", record)
	}
}

func TestDerivedEffectsKeepUnconfiguredObservabilityDisabled(t *testing.T) {
	now := time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)
	statuses := &derivedEffectStore{}
	result, err := (interactionapp.PublishAssistantDerivedEffects{
		Store: statuses, Clock: testkit.NewClock(now),
	}).Execute(context.Background(), interactionapp.PublishAssistantDerivedEffectsCommand{
		Observation: &interactionapp.ModelObservation{
			TenantID: "tenant-local", UserID: "user-local", AssistantRunID: "assistant-run-1",
			ConversationID: "conversation-1", ProfileID: "assistant-profile",
			ProfileRevision:   1,
			ProfileDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProviderRequestID: "provider-request-1", Model: "provider/model",
			StartedAt: now.Add(-time.Second), CompletedAt: now, Status: "ok",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservabilityStatus != interactionapp.DerivedEffectDisabled ||
		result.ObservabilityErrorCode != "" || len(statuses.records) != 1 ||
		statuses.records[0].LangfuseTraceID != "" {
		t.Fatalf("unconfigured observability = result=%+v records=%+v", result, statuses.records)
	}
}

type derivedEffectStore struct {
	records []interactionapp.DerivedEffectRecord
}

func (store *derivedEffectStore) RecordAssistantDerivedEffects(
	_ context.Context,
	record interactionapp.DerivedEffectRecord,
) error {
	store.records = append(store.records, record)
	return nil
}
