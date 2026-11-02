package application

import (
	"context"
	"fmt"
)

const (
	DerivedEffectPending   = "pending"
	DerivedEffectAvailable = "available"
	DerivedEffectPassed    = "passed"
	DerivedEffectDegraded  = "degraded"
	DerivedEffectDisabled  = "disabled"
)

type PublishAssistantDerivedEffectsCommand struct {
	Evidence    *EvidenceBoundary
	Observation *ModelObservation
}

type PublishAssistantDerivedEffectsResult struct {
	EvidenceStatus         string
	EvidenceErrorCode      string
	ObservabilityStatus    string
	ObservabilityErrorCode string
}

type PublishAssistantDerivedEffects struct {
	Objects              ObjectStore
	Observability        ObservabilityPort
	ObservabilityEnabled bool
	Store                DerivedEffectStore
	Clock                Clock
}

func (service PublishAssistantDerivedEffects) Execute(
	ctx context.Context,
	command PublishAssistantDerivedEffectsCommand,
) (PublishAssistantDerivedEffectsResult, error) {
	if service.Store == nil || service.Clock == nil {
		return PublishAssistantDerivedEffectsResult{}, fmt.Errorf("Assistant derived-effect dependencies are required")
	}
	runID, err := derivedEffectRunID(command)
	if err != nil {
		return PublishAssistantDerivedEffectsResult{}, err
	}
	record := DerivedEffectRecord{
		AssistantRunID: runID, EvidenceStatus: DerivedEffectDisabled,
		ObservabilityStatus: DerivedEffectDisabled, RecordedAt: service.Clock.Now().UTC(),
	}
	if record.RecordedAt.IsZero() {
		return PublishAssistantDerivedEffectsResult{}, fmt.Errorf("Assistant derived-effect time is required")
	}
	if command.Evidence != nil {
		if service.Objects == nil {
			record.EvidenceStatus = DerivedEffectDegraded
			record.EvidenceErrorCode = "object_store_unavailable"
		} else if result, writeErr := service.Objects.WriteConversationEvidence(ctx, *command.Evidence); writeErr != nil {
			record.EvidenceStatus = DerivedEffectDegraded
			record.EvidenceErrorCode = "object_store_write_failed"
		} else if result.ObjectKey == "" || result.Checksum == "" || result.ByteSize < 0 {
			record.EvidenceStatus = DerivedEffectDegraded
			record.EvidenceErrorCode = "object_store_result_invalid"
		} else {
			record.EvidenceStatus = DerivedEffectAvailable
			record.EvidenceObjectKey = result.ObjectKey
		}
	}
	if command.Observation != nil {
		observabilityEnabled := service.ObservabilityEnabled || service.Observability != nil
		if !observabilityEnabled {
			record.ObservabilityStatus = DerivedEffectDisabled
		} else if service.Observability == nil {
			record.ObservabilityStatus = DerivedEffectDegraded
			record.ObservabilityErrorCode = "langfuse_unavailable"
		} else if observeErr := service.Observability.RecordAssistantModel(ctx, *command.Observation); observeErr != nil {
			record.ObservabilityStatus = DerivedEffectDegraded
			record.ObservabilityErrorCode = "langfuse_projection_failed"
		} else {
			record.ObservabilityStatus = DerivedEffectPassed
			record.LangfuseTraceID = "assistant/" + runID
		}
	}
	result := PublishAssistantDerivedEffectsResult{
		EvidenceStatus: record.EvidenceStatus, EvidenceErrorCode: record.EvidenceErrorCode,
		ObservabilityStatus:    record.ObservabilityStatus,
		ObservabilityErrorCode: record.ObservabilityErrorCode,
	}
	if err := service.Store.RecordAssistantDerivedEffects(ctx, record); err != nil {
		return result, fmt.Errorf("record Assistant derived-effect status: %w", err)
	}
	return result, nil
}

func derivedEffectRunID(command PublishAssistantDerivedEffectsCommand) (string, error) {
	runID := ""
	if command.Evidence != nil {
		runID = command.Evidence.AssistantRunID
	}
	if command.Observation != nil {
		if runID != "" && runID != command.Observation.AssistantRunID {
			return "", fmt.Errorf("Assistant derived-effect Run targets conflict")
		}
		runID = command.Observation.AssistantRunID
	}
	if runID == "" {
		return "", fmt.Errorf("Assistant derived-effect Run target is required")
	}
	return runID, nil
}
