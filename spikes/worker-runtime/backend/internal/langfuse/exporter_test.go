package langfuse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLangfuseExporterMapsAuthoritativeFactsWithoutSecrets(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-must-not-project"
		secretKey = "sk-must-not-project"
	)
	ingester := &recordingIngester{}
	eventIDs := []string{"event-trace", "event-span-1", "event-generation-1", "event-span-2", "event-generation-2"}
	exporter, err := NewExporter(ingester, ExporterOptions{
		Now: func() time.Time {
			return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
		},
		NewEventID: func() string {
			id := eventIDs[0]
			eventIDs = eventIDs[1:]
			return id
		},
	})
	if err != nil {
		t.Fatalf("NewExporter() error = %v", err)
	}
	started := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	projection := RunProjection{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-1",
		TraceID:           "trace-1",
		Turns: []CompletedTurn{
			{
				AgentTurnID: "turn-1",
				StartedAt:   started,
				EndedAt:     started.Add(2 * time.Second),
				ModelObservations: []ModelObservationFact{{
					ID:           "generation-1",
					Model:        "openai/codex",
					Route:        "codex-primary",
					StartedAt:    started.Add(100 * time.Millisecond),
					EndedAt:      started.Add(1900 * time.Millisecond),
					LatencyMS:    1800,
					InputTokens:  101,
					OutputTokens: 23,
					TotalTokens:  124,
					CostUSD:      0.0125,
					Status:       "ok",
					MetadataJSON: []byte(fmt.Sprintf(
						`{"authorization":"Basic raw","public":"%s","secret":"%s"}`,
						publicKey,
						secretKey,
					)),
				}},
			},
			{
				AgentTurnID: "turn-2",
				StartedAt:   started.Add(3 * time.Second),
				EndedAt:     started.Add(5 * time.Second),
				ModelObservations: []ModelObservationFact{{
					ID:           "generation-2",
					Model:        "openai/codex",
					Route:        "codex-primary",
					StartedAt:    started.Add(3100 * time.Millisecond),
					EndedAt:      started.Add(4900 * time.Millisecond),
					LatencyMS:    1800,
					InputTokens:  80,
					OutputTokens: 20,
					TotalTokens:  100,
					CostUSD:      0.01,
					Status:       "error",
					ErrorKind:    "model_error",
					MetadataJSON: []byte(`{"Authorization":"Bearer raw-token"}`),
				}},
			},
		},
	}

	if err := exporter.Export(context.Background(), projection); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if len(ingester.batches) != 1 {
		t.Fatalf("ingestion calls = %d, want one batch", len(ingester.batches))
	}
	events := ingester.batches[0].Batch
	if len(events) != 5 {
		t.Fatalf("events = %d, want trace + 2 spans + 2 generations", len(events))
	}
	wantTypes := []string{"trace-create", "span-create", "generation-create", "span-create", "generation-create"}
	for index, event := range events {
		if event.Type != wantTypes[index] {
			t.Errorf("event %d type = %q, want %q", index, event.Type, wantTypes[index])
		}
		metadata, ok := event.Body["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("event %d metadata type = %T", index, event.Body["metadata"])
		}
		assertProjectionMetadata(t, metadata, projection)
	}
	if events[0].Body["id"] != "trace-1" {
		t.Errorf("trace body id = %v", events[0].Body["id"])
	}
	if events[1].Body["id"] != "turn-1" ||
		events[1].Body["traceId"] != "trace-1" ||
		events[1].Body["name"] != "agent.turn" {
		t.Errorf("span body = %+v", events[1].Body)
	}
	generation := events[2].Body
	if generation["id"] != "generation-1" ||
		generation["traceId"] != "trace-1" ||
		generation["parentObservationId"] != "turn-1" ||
		generation["model"] != "openai/codex" ||
		generation["statusMessage"] != "ok" {
		t.Errorf("generation identity/model/status = %+v", generation)
	}
	usage, ok := generation["usageDetails"].(map[string]any)
	if !ok || usage["input"] != int64(101) || usage["output"] != int64(23) || usage["total"] != int64(124) {
		t.Errorf("generation usageDetails = %#v", generation["usageDetails"])
	}
	cost, ok := generation["costDetails"].(map[string]any)
	if !ok || cost["total"] != 0.0125 {
		t.Errorf("generation costDetails = %#v", generation["costDetails"])
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{publicKey, secretKey, "Authorization", "authorization", "raw-token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("exported batch contains forbidden value %q", forbidden)
		}
	}
}

func TestLangfuseProjectorBoundsExportRetriesWithinOperation(t *testing.T) {
	t.Parallel()

	projection := validRunProjection()
	exporter := &scriptedProjectionExporter{
		errors: []error{fmt.Errorf("temporary export failure"), fmt.Errorf("temporary export failure"), nil},
	}
	verifier := &recordingProjectionVerifier{}
	clock := &advancingClock{now: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)}
	projector, err := NewProjector(exporter, verifier, ProjectorOptions{
		ExportAttempts: 3,
		RetryInterval:  time.Second,
		Clock:          clock,
	})
	if err != nil {
		t.Fatalf("NewProjector() error = %v", err)
	}
	if err := projector.Project(context.Background(), projection); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if exporter.calls != 3 {
		t.Fatalf("export attempts = %d, want 3", exporter.calls)
	}
	if verifier.calls != 1 {
		t.Fatalf("verification calls = %d, want 1", verifier.calls)
	}
	if len(clock.waits) != 2 || clock.waits[0] != time.Second || clock.waits[1] != time.Second {
		t.Fatalf("retry waits = %v, want two 1s waits", clock.waits)
	}
	if len(verifier.expected.TurnIDs) != 2 ||
		len(verifier.expected.Generations) != 2 ||
		verifier.expected.Generations[1].ParentTurnID != "turn-2" {
		t.Fatalf("verification expectation = %+v", verifier.expected)
	}
}

func assertProjectionMetadata(t *testing.T, metadata map[string]any, projection RunProjection) {
	t.Helper()

	if metadata["worker_session_id"] != projection.WorkerSessionID ||
		metadata["worker_run_id"] != projection.WorkerRunID ||
		metadata["runtime_instance_id"] != projection.RuntimeInstanceID ||
		metadata["runtime_generation"] != projection.RuntimeGeneration ||
		metadata["agent_session_id"] != projection.AgentSessionID {
		t.Errorf("identity metadata = %#v", metadata)
	}
	turnIDs, ok := metadata["agent_turn_ids"].([]string)
	if !ok || len(turnIDs) != 2 || turnIDs[0] != "turn-1" || turnIDs[1] != "turn-2" {
		t.Errorf("agent_turn_ids = %#v", metadata["agent_turn_ids"])
	}
}

type recordingIngester struct {
	batches []IngestionBatch
	err     error
}

func (i *recordingIngester) Ingest(_ context.Context, batch IngestionBatch) error {
	i.batches = append(i.batches, batch)
	return i.err
}

func validRunProjection() RunProjection {
	started := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	return RunProjection{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-1",
		TraceID:           "trace-1",
		Turns: []CompletedTurn{
			{
				AgentTurnID: "turn-1",
				StartedAt:   started,
				EndedAt:     started.Add(time.Second),
				ModelObservations: []ModelObservationFact{{
					ID:           "generation-1",
					Model:        "openai/codex",
					Route:        "codex-primary",
					StartedAt:    started,
					EndedAt:      started.Add(time.Second),
					InputTokens:  10,
					OutputTokens: 2,
					TotalTokens:  12,
					CostUSD:      0.001,
					Status:       "ok",
				}},
			},
			{
				AgentTurnID: "turn-2",
				StartedAt:   started.Add(2 * time.Second),
				EndedAt:     started.Add(3 * time.Second),
				ModelObservations: []ModelObservationFact{{
					ID:           "generation-2",
					Model:        "openai/codex",
					Route:        "codex-primary",
					StartedAt:    started.Add(2 * time.Second),
					EndedAt:      started.Add(3 * time.Second),
					InputTokens:  8,
					OutputTokens: 2,
					TotalTokens:  10,
					CostUSD:      0.001,
					Status:       "ok",
				}},
			},
		},
	}
}

type scriptedProjectionExporter struct {
	errors []error
	calls  int
}

func (e *scriptedProjectionExporter) Export(context.Context, RunProjection) error {
	index := e.calls
	e.calls++
	return e.errors[index]
}

type recordingProjectionVerifier struct {
	calls    int
	traceID  string
	expected ExpectedProjection
	err      error
}

func (v *recordingProjectionVerifier) Verify(
	_ context.Context,
	traceID string,
	expected ExpectedProjection,
) error {
	v.calls++
	v.traceID = traceID
	v.expected = expected
	return v.err
}
