package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestProjectorExportsAndVerifiesExactRunProjection(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	api := &recordingAPI{trace: exactTrace(projection)}
	projector := testProjector(t, api)

	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatalf("ExportAndVerify() error = %v", err)
	}
	if api.ingestCalls != 1 || api.queryCalls != 1 || len(api.batches) != 1 {
		t.Fatalf("calls: ingest=%d query=%d batches=%d", api.ingestCalls, api.queryCalls, len(api.batches))
	}
	batch := api.batches[0].Batch
	if len(batch) != 3 {
		t.Fatalf("event count = %d, want trace + span + generation", len(batch))
	}
	if batch[0].Type != "trace-create" || batch[1].Type != "span-create" || batch[2].Type != "generation-create" {
		t.Fatalf("event types = %q, %q, %q", batch[0].Type, batch[1].Type, batch[2].Type)
	}
	for _, event := range batch {
		if event.ID == "" || strings.Contains(event.ID, "secret") {
			t.Fatalf("unsafe event identity = %q", event.ID)
		}
	}
	if !reflect.DeepEqual(batch[0].Body["input"], projection.Turns[0].Input) ||
		!reflect.DeepEqual(batch[0].Body["output"], projection.Turns[0].Output) ||
		!reflect.DeepEqual(batch[1].Body["input"], projection.Turns[0].Input) ||
		!reflect.DeepEqual(batch[1].Body["output"], projection.Turns[0].Output) ||
		!reflect.DeepEqual(batch[2].Body["input"], projection.Turns[0].ModelObservations[0].Input) ||
		!reflect.DeepEqual(batch[2].Body["output"], projection.Turns[0].ModelObservations[0].Output) {
		t.Fatalf("Langfuse input/output bodies were not preserved: %#v", batch)
	}
}

func TestProjectorRetriesTheSameIdempotentBatch(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	api := &recordingAPI{ingestFailures: 1, trace: exactTrace(projection)}
	projector := testProjector(t, api)

	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatalf("ExportAndVerify() error = %v", err)
	}
	if len(api.batches) != 2 || !reflect.DeepEqual(api.batches[0], api.batches[1]) {
		t.Fatalf("retry batches changed: %#v", api.batches)
	}
}

func TestProjectionMatchesRejectsExtraGeneration(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	metadata := runtimeMetadata(projection, []string{"turn-1"})
	expected := expectedProjection{
		Metadata: metadata, TurnIDs: []string{"turn-1"},
		Generations: map[string]string{"model-1": "turn-1"},
		TraceInput:  projection.Turns[0].Input, TraceOutput: projection.Turns[0].Output,
		TurnContent: map[string]inputOutput{
			"turn-1": {Input: projection.Turns[0].Input, Output: projection.Turns[0].Output},
		},
		ModelContent: map[string]inputOutput{
			"model-1": {
				Input:  projection.Turns[0].ModelObservations[0].Input,
				Output: projection.Turns[0].ModelObservations[0].Output,
			},
		},
	}
	trace := exactTrace(projection)
	parent := "turn-1"
	trace.Observations = append(trace.Observations, Observation{
		ID: "unexpected-model", TraceID: projection.TraceID, ParentObservationID: &parent,
		Name: "model.generation", Type: "GENERATION",
		Metadata: observationMetadata(metadata, parent),
	})

	matched, summary := projectionMatches(trace, projection.TraceID, expected)
	if matched || !strings.Contains(summary, "generations=2") || !strings.Contains(summary, "matched_generations=1/1") {
		t.Fatalf("matched=%t summary=%q", matched, summary)
	}
}

func TestProjectionMatchesRejectsNullGenerationContent(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	trace := exactTrace(projection)
	trace.Observations[1].Input = json.RawMessage(`null`)
	_, expected, err := testProjector(t, &recordingAPI{}).buildBatch(projection)
	if err != nil {
		t.Fatal(err)
	}

	matched, summary := projectionMatches(trace, projection.TraceID, expected)
	if matched || !strings.Contains(summary, "matched_generations=0/1") {
		t.Fatalf("matched=%t summary=%q", matched, summary)
	}
}

func TestProjectorRejectsKnownTokenInLangfuseQueryWithoutEchoingValue(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	trace := exactTrace(projection)
	trace.Observations[0].Metadata["unsafe"] = "known-value-never-persist-12345"
	projector := testProjector(t, &recordingAPI{trace: trace})

	err := projector.ExportAndVerify(context.Background(), projection)
	if !secretcheck.IsExposure(err) || strings.Contains(err.Error(), "known-value-never-persist-12345") {
		t.Fatalf("ExportAndVerify() error = %v", err)
	}
}

func TestProjectorRejectsKnownTokenBeforeLangfuseIngestion(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	projection.Turns[0].ModelObservations[0].Input = json.RawMessage(
		`{"messages":[{"role":"user","content":"known-value-never-persist-12345"}]}`,
	)
	api := &recordingAPI{trace: exactTrace(projection)}
	projector := testProjector(t, api)

	err := projector.ExportAndVerify(context.Background(), projection)
	if !secretcheck.IsExposure(err) || strings.Contains(err.Error(), "known-value-never-persist-12345") {
		t.Fatalf("ExportAndVerify() error = %v", err)
	}
	if api.ingestCalls != 0 || api.queryCalls != 0 {
		t.Fatalf("unsafe projection reached Langfuse: ingest=%d query=%d", api.ingestCalls, api.queryCalls)
	}
}

func testProjector(t *testing.T, api API) *Projector {
	t.Helper()
	knownTokens, err := secretcheck.New([]secretcheck.KnownToken{{
		Label: "model-key", Value: "known-value-never-persist-12345",
	}})
	if err != nil {
		t.Fatalf("secretcheck.New() error = %v", err)
	}
	sequence := 0
	projector, err := NewProjector(api, ProjectorOptions{
		NewEventID: func() string {
			sequence++
			return "event-" + string(rune('0'+sequence))
		},
		KnownTokens:    knownTokens,
		ExportAttempts: 2,
		RetryInterval:  time.Nanosecond,
		PollInterval:   time.Millisecond,
		VerifyTimeout:  100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewProjector() error = %v", err)
	}
	return projector
}

func testProjection() artifactapp.RunObservabilityProjection {
	started := time.Date(2026, time.July, 23, 11, 0, 0, 0, time.UTC)
	return artifactapp.RunObservabilityProjection{
		TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
		WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, AgentSessionID: "agent-1", TraceID: "trace-1",
		Turns: []artifactapp.CompletedTurnObservation{{
			AgentTurnID: "turn-1", StartedAt: started, EndedAt: started.Add(time.Second),
			Input:  json.RawMessage(`{"text":"complete-turn-input"}`),
			Output: json.RawMessage(`{"stage_report":"complete-turn-output"}`),
			ModelObservations: []workerapp.ModelObservation{{
				ObservationID: "model-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
				AgentTurnID: "turn-1", RuntimeID: "runtime-1", Generation: 1,
				Model: "deepseek/deepseek-chat", Route: "deepseek-codex-openai-v1",
				InputTokens: 10, OutputTokens: 2, StartedAt: started, EndedAt: started.Add(time.Second), Status: "ok",
				Input:  json.RawMessage(`{"messages":[{"role":"user","content":"complete-model-input"}]}`),
				Output: json.RawMessage(`{"choices":[{"message":{"content":"complete-model-output","reasoning_content":"complete-reasoning-output"}}]}`),
			}},
			TraceManifest: contractsv1.TraceManifest{
				SchemaVersion: contractsv1.TraceManifestSchemaVersion, TraceStreamID: "trace-stream-1",
				TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
				WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1",
				RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1,
				Segments: []contractsv1.TraceSegment{{
					Kind: "trace-normalized", URI: "object://trace", FirstSequence: 1, LastSequence: 1,
					SHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					SizeBytes: 1, RecordCount: 1,
				}},
				FirstRecordedAt: started, LastRecordedAt: started, SchemaVersions: []string{"trace.record.v1"},
				LifecyclePolicyRef: "trace-default-v1", StorageTier: "hot", Availability: "online", AnalyticsCursor: "1",
				LangfuseExport: contractsv1.LangfuseExportRef{Status: "pending", TraceID: "trace-1"},
			},
		}},
	}
}

func exactTrace(projection artifactapp.RunObservabilityProjection) Trace {
	turnID := string(projection.Turns[0].AgentTurnID)
	metadata := runtimeMetadata(projection, []string{turnID})
	return Trace{
		ID: projection.TraceID, Metadata: metadata,
		Input: projection.Turns[0].Input, Output: projection.Turns[0].Output,
		Observations: []Observation{
			{
				ID: turnID, TraceID: projection.TraceID, Name: "agent.turn", Type: "SPAN",
				Metadata: observationMetadata(metadata, turnID),
				Input:    projection.Turns[0].Input, Output: projection.Turns[0].Output,
			},
			{
				ID: "model-1", TraceID: projection.TraceID, ParentObservationID: &turnID,
				Name: "model.generation", Type: "GENERATION", Metadata: observationMetadata(metadata, turnID),
				Input:  projection.Turns[0].ModelObservations[0].Input,
				Output: projection.Turns[0].ModelObservations[0].Output,
			},
		},
	}
}

type recordingAPI struct {
	ingestFailures int
	ingestCalls    int
	queryCalls     int
	batches        []IngestionBatch
	trace          Trace
}

func (api *recordingAPI) Ingest(_ context.Context, batch IngestionBatch) error {
	api.ingestCalls++
	api.batches = append(api.batches, batch)
	if api.ingestCalls <= api.ingestFailures {
		return errors.New("temporary ingestion failure")
	}
	return nil
}

func (api *recordingAPI) GetTrace(context.Context, string) (Trace, error) {
	api.queryCalls++
	return api.trace, nil
}
