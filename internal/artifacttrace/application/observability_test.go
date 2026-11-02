package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestObservabilityProjectionServiceRecordsPassedWithoutChangingExecutionOutcome(t *testing.T) {
	t.Parallel()
	projector := &fakeLangfuseProjector{}
	store := &recordingObservabilityStore{}
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	service := artifactapp.ObservabilityProjectionService{
		Projector: projector, Store: store, Now: func() time.Time { return now },
	}
	projection := validObservabilityProjection()

	result, err := service.Execute(context.Background(), artifactapp.ProjectRunObservabilityCommand{
		Projection: projection, ExecutionOutcome: "completed",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.ExecutionOutcome != "completed" || result.ObservabilityStatus != artifactapp.ObservabilityPassed {
		t.Fatalf("result = %#v", result)
	}
	if projector.calls != 1 || store.calls != 1 || store.record.Status != artifactapp.ObservabilityPassed ||
		store.record.ErrorCode != "" || store.record.RecordedAt != now {
		t.Fatalf("projector calls=%d store=%#v", projector.calls, store.record)
	}
}

func TestObservabilityProjectionFailureIsDegradedAndPreservesCompletedExecution(t *testing.T) {
	t.Parallel()
	projector := &fakeLangfuseProjector{err: errors.New("upstream included sk-sensitive-raw")}
	store := &recordingObservabilityStore{}
	service := artifactapp.ObservabilityProjectionService{
		Projector: projector, Store: store, Now: time.Now,
	}

	result, err := service.Execute(context.Background(), artifactapp.ProjectRunObservabilityCommand{
		Projection: validObservabilityProjection(), ExecutionOutcome: "completed",
	})
	if err != nil {
		t.Fatalf("derived projection failure escaped as execution failure: %v", err)
	}
	if result.ExecutionOutcome != "completed" || result.ObservabilityStatus != artifactapp.ObservabilityDegraded ||
		result.ObservabilityErrorCode != "langfuse_projection_failed" {
		t.Fatalf("result = %#v", result)
	}
	if store.record.Status != artifactapp.ObservabilityDegraded ||
		store.record.ErrorCode != "langfuse_projection_failed" {
		t.Fatalf("degraded record = %#v", store.record)
	}
}

func TestMissingModelObservationIsRecordedAsDegradedWithoutCallingLangfuse(t *testing.T) {
	t.Parallel()
	projector := &fakeLangfuseProjector{}
	store := &recordingObservabilityStore{}
	service := artifactapp.ObservabilityProjectionService{
		Projector: projector,
		Store:     store,
		Now:       func() time.Time { return time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC) },
	}
	projection := validObservabilityProjection()
	projection.Turns[0].ModelObservations = nil

	result, err := service.Execute(context.Background(), artifactapp.ProjectRunObservabilityCommand{
		Projection: projection, ExecutionOutcome: "completed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionOutcome != "completed" || result.ObservabilityStatus != artifactapp.ObservabilityDegraded ||
		result.ObservabilityErrorCode != "model_observation_missing" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if projector.calls != 0 {
		t.Fatalf("Langfuse projector calls = %d, want 0", projector.calls)
	}
	if store.calls != 1 || store.record.Status != artifactapp.ObservabilityDegraded ||
		store.record.ErrorCode != "model_observation_missing" {
		t.Fatalf("unexpected record: %+v", store.record)
	}
}

func TestValidateRunObservabilityProjectionRejectsNullContent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*artifactapp.RunObservabilityProjection)
	}{
		{
			name: "Turn input",
			mutate: func(projection *artifactapp.RunObservabilityProjection) {
				projection.Turns[0].Input = json.RawMessage(`null`)
			},
		},
		{
			name: "successful model input",
			mutate: func(projection *artifactapp.RunObservabilityProjection) {
				projection.Turns[0].ModelObservations[0].Input = json.RawMessage(`null`)
			},
		},
		{
			name: "successful model output",
			mutate: func(projection *artifactapp.RunObservabilityProjection) {
				projection.Turns[0].ModelObservations[0].Output = json.RawMessage(`null`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := validObservabilityProjection()
			test.mutate(&projection)
			if err := artifactapp.ValidateRunObservabilityProjection(projection); err == nil {
				t.Fatal("ValidateRunObservabilityProjection() unexpectedly accepted null content")
			}
		})
	}
}

func validObservabilityProjection() artifactapp.RunObservabilityProjection {
	started := time.Date(2026, time.July, 23, 11, 0, 0, 0, time.UTC)
	return artifactapp.RunObservabilityProjection{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, AgentSessionID: "agent-1", TraceID: "trace-1",
		Turns: []artifactapp.CompletedTurnObservation{{
			AgentTurnID: "turn-1", StartedAt: started, EndedAt: started.Add(time.Second),
			Input: json.RawMessage(`{"text":"complete-turn-input"}`), Output: json.RawMessage(`"complete-turn-output"`),
			ModelObservations: []workerapp.ModelObservation{{
				ObservationID: "model-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
				AgentTurnID: "turn-1", RuntimeID: "runtime-1", Generation: 1,
				Model: "deepseek/deepseek-chat", Route: "deepseek-codex-openai-v1",
				InputTokens: 10, OutputTokens: 2, StartedAt: started, EndedAt: started.Add(time.Second), Status: "ok",
				Input:  json.RawMessage(`{"messages":[{"role":"user","content":"complete-model-input"}]}`),
				Output: json.RawMessage(`{"choices":[{"message":{"content":"complete-model-output"}}]}`),
			}},
			TraceManifest: contractsv1.TraceManifest{
				SchemaVersion: contractsv1.TraceManifestSchemaVersion, TraceStreamID: "trace-stream-1",
				TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
				WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
				RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1,
				Segments:        []contractsv1.TraceSegment{{Kind: "trace-normalized", URI: "object://trace", FirstSequence: 1, LastSequence: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 1, RecordCount: 1}},
				FirstRecordedAt: started, LastRecordedAt: started, SchemaVersions: []string{"trace.record.v1"},
				LifecyclePolicyRef: "trace-default-v1", StorageTier: "hot", Availability: "online", AnalyticsCursor: "1",
				LangfuseExport: contractsv1.LangfuseExportRef{Status: "pending", TraceID: "trace-1"},
			},
		}},
	}
}

type fakeLangfuseProjector struct {
	calls int
	err   error
}

func (projector *fakeLangfuseProjector) ExportAndVerify(context.Context, artifactapp.RunObservabilityProjection) error {
	projector.calls++
	return projector.err
}

type recordingObservabilityStore struct {
	calls  int
	record artifactapp.ObservabilityProjectionRecord
	err    error
}

func (store *recordingObservabilityStore) RecordObservabilityProjection(_ context.Context, record artifactapp.ObservabilityProjectionRecord) error {
	store.calls++
	store.record = record
	return store.err
}
