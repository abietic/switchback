package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	ObservabilityPending  = "pending"
	ObservabilityPassed   = "passed"
	ObservabilityDegraded = "degraded"
)

type CompletedTurnObservation struct {
	AgentTurnID       contractsv1.AgentTurnID
	StartedAt         time.Time
	EndedAt           time.Time
	Input             json.RawMessage
	Output            json.RawMessage
	TraceManifest     contractsv1.TraceManifest
	ModelObservations []workerapp.ModelObservation
}

type RunObservabilityProjection struct {
	TenantID          string
	UserID            string
	WorkspaceID       string
	WorkerSessionID   contractsv1.WorkerSessionID
	WorkerRunID       contractsv1.WorkerRunID
	RuntimeInstanceID contractsv1.RuntimeInstanceID
	RuntimeGeneration uint32
	AgentSessionID    contractsv1.AgentSessionID
	TraceID           string
	Turns             []CompletedTurnObservation
}

type LangfuseProjector interface {
	ExportAndVerify(context.Context, RunObservabilityProjection) error
}

type ProjectRunObservabilityCommand struct {
	Projection       RunObservabilityProjection
	ExecutionOutcome string
}

type ObservabilityProjectionRecord struct {
	Projection       RunObservabilityProjection
	ExecutionOutcome string
	Status           string
	ErrorCode        string
	RecordedAt       time.Time
}

type ObservabilityStatusStore interface {
	RecordObservabilityProjection(context.Context, ObservabilityProjectionRecord) error
}

type ObservabilityProjectionResult struct {
	ExecutionOutcome       string
	ObservabilityStatus    string
	ObservabilityErrorCode string
}

type ObservabilityProjectionService struct {
	Projector LangfuseProjector
	Store     ObservabilityStatusStore
	Now       func() time.Time
}

func (service ObservabilityProjectionService) Execute(
	ctx context.Context,
	command ProjectRunObservabilityCommand,
) (ObservabilityProjectionResult, error) {
	if service.Projector == nil || service.Store == nil || service.Now == nil {
		return ObservabilityProjectionResult{}, fmt.Errorf("observability projection dependencies are required")
	}
	if err := ValidateRunObservabilityProjection(command.Projection); err != nil {
		return ObservabilityProjectionResult{}, err
	}
	if command.ExecutionOutcome != "completed" && command.ExecutionOutcome != "failed" &&
		command.ExecutionOutcome != "cancelled" && command.ExecutionOutcome != "timed_out" {
		return ObservabilityProjectionResult{}, fmt.Errorf("terminal execution outcome is required")
	}
	status := ObservabilityPassed
	errorCode := ""
	if hasMissingModelObservation(command.Projection) {
		status = ObservabilityDegraded
		errorCode = "model_observation_missing"
	} else if err := service.Projector.ExportAndVerify(ctx, command.Projection); err != nil {
		status = ObservabilityDegraded
		errorCode = "langfuse_projection_failed"
	}
	result := ObservabilityProjectionResult{
		ExecutionOutcome: command.ExecutionOutcome, ObservabilityStatus: status,
		ObservabilityErrorCode: errorCode,
	}
	record := ObservabilityProjectionRecord{
		Projection: command.Projection, ExecutionOutcome: command.ExecutionOutcome,
		Status: status, ErrorCode: errorCode, RecordedAt: service.Now().UTC(),
	}
	if record.RecordedAt.IsZero() {
		return result, fmt.Errorf("observability projection record time is required")
	}
	if err := service.Store.RecordObservabilityProjection(ctx, record); err != nil {
		return result, fmt.Errorf("record observability projection status: %w", err)
	}
	return result, nil
}

func ValidateRunObservabilityProjection(projection RunObservabilityProjection) error {
	for name, value := range map[string]string{
		"tenant_id": projection.TenantID, "user_id": projection.UserID,
		"workspace_id":        projection.WorkspaceID,
		"worker_session_id":   string(projection.WorkerSessionID),
		"worker_run_id":       string(projection.WorkerRunID),
		"runtime_instance_id": string(projection.RuntimeInstanceID),
		"agent_session_id":    string(projection.AgentSessionID), "trace_id": projection.TraceID,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("Langfuse projection %s must be canonical", name)
		}
	}
	if projection.RuntimeGeneration == 0 || len(projection.Turns) == 0 {
		return fmt.Errorf("Langfuse projection generation and completed Turns are required")
	}
	turnIDs := make(map[contractsv1.AgentTurnID]struct{}, len(projection.Turns))
	observationIDs := make(map[string]struct{})
	for _, turn := range projection.Turns {
		if turn.AgentTurnID == "" || turn.StartedAt.IsZero() || turn.EndedAt.Before(turn.StartedAt) {
			return fmt.Errorf("Langfuse completed Turn is invalid")
		}
		if !nonNullJSON(turn.Input) || !nonNullJSON(turn.Output) {
			return fmt.Errorf("Langfuse completed Turn input and output JSON are required")
		}
		if _, duplicate := turnIDs[turn.AgentTurnID]; duplicate {
			return fmt.Errorf("Langfuse completed Turn identity is duplicated")
		}
		turnIDs[turn.AgentTurnID] = struct{}{}
		manifest := turn.TraceManifest
		if err := manifest.Validate(); err != nil {
			return fmt.Errorf("Langfuse Turn TraceManifest is invalid: %w", err)
		}
		if manifest.TenantID != projection.TenantID || manifest.UserID != projection.UserID ||
			manifest.WorkspaceID != projection.WorkspaceID || manifest.WorkerSessionID != projection.WorkerSessionID ||
			manifest.WorkerRunID != projection.WorkerRunID || manifest.AgentTurnID != turn.AgentTurnID ||
			manifest.RuntimeInstanceID != projection.RuntimeInstanceID ||
			manifest.RuntimeGeneration != projection.RuntimeGeneration ||
			manifest.LangfuseExport.TraceID != projection.TraceID {
			return fmt.Errorf("Langfuse Turn TraceManifest provenance mismatch")
		}
		for _, observation := range turn.ModelObservations {
			if observation.ObservationID == "" || observation.Model == "" || observation.Route == "" ||
				observation.WorkerSessionID != string(projection.WorkerSessionID) ||
				observation.WorkerRunID != string(projection.WorkerRunID) ||
				observation.AgentTurnID != string(turn.AgentTurnID) ||
				observation.RuntimeID != string(projection.RuntimeInstanceID) ||
				observation.Generation != uint64(projection.RuntimeGeneration) ||
				observation.StartedAt.IsZero() || observation.EndedAt.Before(observation.StartedAt) ||
				observation.InputTokens < 0 || observation.OutputTokens < 0 || observation.Status == "" {
				return fmt.Errorf("Langfuse model observation is invalid or mismatched")
			}
			if !nonNullJSON(observation.Input) ||
				len(observation.Output) == 0 || !json.Valid(observation.Output) ||
				observation.Status == "ok" && !nonNullJSON(observation.Output) {
				return fmt.Errorf("Langfuse model observation input and output JSON are required")
			}
			if _, duplicate := observationIDs[observation.ObservationID]; duplicate {
				return fmt.Errorf("Langfuse model observation identity is duplicated")
			}
			observationIDs[observation.ObservationID] = struct{}{}
		}
	}
	return nil
}

func nonNullJSON(value json.RawMessage) bool {
	return len(value) > 0 && json.Valid(value) &&
		!bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func hasMissingModelObservation(projection RunObservabilityProjection) bool {
	for _, turn := range projection.Turns {
		if len(turn.ModelObservations) == 0 {
			return true
		}
	}
	return false
}
