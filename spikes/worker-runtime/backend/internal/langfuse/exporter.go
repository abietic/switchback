package langfuse

import (
	"context"
	"fmt"
	"math"
	"time"

	"switchback.local/spikes/worker-runtime/internal/ids"
)

const (
	defaultExportAttempts = 3
	maxExportAttempts     = 10
	defaultRetryInterval  = time.Second
)

type Ingester interface {
	Ingest(context.Context, IngestionBatch) error
}

type RunProjection struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	RuntimeGeneration uint32
	AgentSessionID    string
	TraceID           string
	Turns             []CompletedTurn
}

type CompletedTurn struct {
	AgentTurnID       string
	StartedAt         time.Time
	EndedAt           time.Time
	ModelObservations []ModelObservationFact
}

// ModelObservationFact is the complete Backend-owned observation received over
// WorkerControl. MetadataJSON is retained for evidence but is deliberately not
// copied into the Langfuse projection.
type ModelObservationFact struct {
	ID           string
	Model        string
	Route        string
	StartedAt    time.Time
	EndedAt      time.Time
	LatencyMS    int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CostUSD      float64
	Status       string
	ErrorKind    string
	MetadataJSON []byte
}

type ExporterOptions struct {
	Now        func() time.Time
	NewEventID func() string
}

type Exporter struct {
	ingester   Ingester
	now        func() time.Time
	newEventID func() string
}

type ProjectionExporter interface {
	Export(context.Context, RunProjection) error
}

type ProjectionVerifier interface {
	Verify(context.Context, string, ExpectedProjection) error
}

type ProjectorOptions struct {
	ExportAttempts int
	RetryInterval  time.Duration
	Clock          Clock
}

type Projector struct {
	exporter       ProjectionExporter
	verifier       ProjectionVerifier
	exportAttempts int
	retryInterval  time.Duration
	clock          Clock
}

func NewExporter(ingester Ingester, options ExporterOptions) (*Exporter, error) {
	if ingester == nil {
		return nil, fmt.Errorf("Langfuse ingester is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newEventID := options.NewEventID
	if newEventID == nil {
		newEventID = ids.New
	}
	return &Exporter{ingester: ingester, now: now, newEventID: newEventID}, nil
}

func NewProjector(
	exporter ProjectionExporter,
	verifier ProjectionVerifier,
	options ProjectorOptions,
) (*Projector, error) {
	if exporter == nil || verifier == nil {
		return nil, fmt.Errorf("Langfuse exporter and verifier are required")
	}
	attempts := options.ExportAttempts
	if attempts == 0 {
		attempts = defaultExportAttempts
	}
	retryInterval := options.RetryInterval
	if retryInterval == 0 {
		retryInterval = defaultRetryInterval
	}
	if attempts < 1 || attempts > maxExportAttempts || retryInterval < 0 {
		return nil, fmt.Errorf("invalid Langfuse export retry policy")
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	return &Projector{
		exporter:       exporter,
		verifier:       verifier,
		exportAttempts: attempts,
		retryInterval:  retryInterval,
		clock:          clock,
	}, nil
}

func (p *Projector) Project(ctx context.Context, projection RunProjection) error {
	if err := validateProjection(projection); err != nil {
		return err
	}
	var exportErr error
	for attempt := 1; attempt <= p.exportAttempts; attempt++ {
		exportErr = p.exporter.Export(ctx, projection)
		if exportErr == nil {
			break
		}
		if attempt < p.exportAttempts {
			if err := p.clock.Wait(ctx, p.retryInterval); err != nil {
				return fmt.Errorf("wait to retry Langfuse export: %w", err)
			}
		}
	}
	if exportErr != nil {
		return fmt.Errorf(
			"Langfuse export failed after %d bounded attempt(s): %w",
			p.exportAttempts,
			exportErr,
		)
	}
	expected := ExpectedProjection{
		Metadata: RuntimeMetadata{
			WorkerSessionID:   projection.WorkerSessionID,
			WorkerRunID:       projection.WorkerRunID,
			RuntimeInstanceID: projection.RuntimeInstanceID,
			RuntimeGeneration: projection.RuntimeGeneration,
			AgentSessionID:    projection.AgentSessionID,
			AgentTurnIDs:      make([]string, 0, len(projection.Turns)),
		},
		TurnIDs:     make([]string, 0, len(projection.Turns)),
		Generations: make([]ExpectedGeneration, 0),
	}
	for _, turn := range projection.Turns {
		expected.TurnIDs = append(expected.TurnIDs, turn.AgentTurnID)
		expected.Metadata.AgentTurnIDs = append(expected.Metadata.AgentTurnIDs, turn.AgentTurnID)
		for _, observation := range turn.ModelObservations {
			expected.Generations = append(expected.Generations, ExpectedGeneration{
				ID:           observation.ID,
				ParentTurnID: turn.AgentTurnID,
			})
		}
	}
	if err := p.verifier.Verify(ctx, projection.TraceID, expected); err != nil {
		return fmt.Errorf("verify Langfuse projection: %w", err)
	}
	return nil
}

func (e *Exporter) Export(ctx context.Context, projection RunProjection) error {
	batch, err := e.buildBatch(projection)
	if err != nil {
		return err
	}
	if err := e.ingester.Ingest(ctx, batch); err != nil {
		return fmt.Errorf("export Langfuse projection: %w", err)
	}
	return nil
}

func (e *Exporter) buildBatch(projection RunProjection) (IngestionBatch, error) {
	if err := validateProjection(projection); err != nil {
		return IngestionBatch{}, err
	}
	turnIDs := make([]string, len(projection.Turns))
	for index, turn := range projection.Turns {
		turnIDs[index] = turn.AgentTurnID
	}
	eventTime := e.now().UTC()
	events := make([]IngestionEvent, 0, 1+len(projection.Turns)*2)
	traceEvent, err := e.event(eventTime, "trace-create", map[string]any{
		"id":        projection.TraceID,
		"name":      "worker.run",
		"timestamp": projection.Turns[0].StartedAt.UTC(),
		"metadata":  projectionMetadata(projection, turnIDs, ""),
	})
	if err != nil {
		return IngestionBatch{}, err
	}
	events = append(events, traceEvent)

	for _, turn := range projection.Turns {
		spanEvent, err := e.event(eventTime, "span-create", map[string]any{
			"id":        turn.AgentTurnID,
			"traceId":   projection.TraceID,
			"name":      "agent.turn",
			"startTime": turn.StartedAt.UTC(),
			"endTime":   turn.EndedAt.UTC(),
			"metadata":  projectionMetadata(projection, turnIDs, turn.AgentTurnID),
		})
		if err != nil {
			return IngestionBatch{}, err
		}
		events = append(events, spanEvent)
		for _, observation := range turn.ModelObservations {
			metadata := projectionMetadata(projection, turnIDs, turn.AgentTurnID)
			metadata["model_route"] = observation.Route
			metadata["latency_ms"] = observation.LatencyMS
			metadata["status"] = observation.Status
			metadata["error_kind"] = observation.ErrorKind
			level := "DEFAULT"
			if observation.Status != "ok" {
				level = "ERROR"
			}
			generationEvent, err := e.event(eventTime, "generation-create", map[string]any{
				"id":                  observation.ID,
				"traceId":             projection.TraceID,
				"parentObservationId": turn.AgentTurnID,
				"name":                "model.generation",
				"model":               observation.Model,
				"startTime":           observation.StartedAt.UTC(),
				"endTime":             observation.EndedAt.UTC(),
				"usageDetails": map[string]any{
					"input":  observation.InputTokens,
					"output": observation.OutputTokens,
					"total":  observation.TotalTokens,
				},
				"costDetails": map[string]any{
					"total": observation.CostUSD,
				},
				"statusMessage": observation.Status,
				"level":         level,
				"metadata":      metadata,
			})
			if err != nil {
				return IngestionBatch{}, err
			}
			events = append(events, generationEvent)
		}
	}
	return IngestionBatch{Batch: events}, nil
}

func (e *Exporter) event(timestamp time.Time, eventType string, body map[string]any) (IngestionEvent, error) {
	eventID := e.newEventID()
	if eventID == "" {
		return IngestionEvent{}, fmt.Errorf("Langfuse event ID is required")
	}
	return IngestionEvent{
		ID:        eventID,
		Timestamp: timestamp,
		Type:      eventType,
		Body:      body,
	}, nil
}

func projectionMetadata(projection RunProjection, turnIDs []string, turnID string) map[string]any {
	metadata := map[string]any{
		"worker_session_id":   projection.WorkerSessionID,
		"worker_run_id":       projection.WorkerRunID,
		"runtime_instance_id": projection.RuntimeInstanceID,
		"runtime_generation":  projection.RuntimeGeneration,
		"agent_session_id":    projection.AgentSessionID,
		"agent_turn_ids":      append([]string(nil), turnIDs...),
	}
	if turnID != "" {
		metadata["agent_turn_id"] = turnID
	}
	return metadata
}

func validateProjection(projection RunProjection) error {
	if projection.WorkerSessionID == "" ||
		projection.WorkerRunID == "" ||
		projection.RuntimeInstanceID == "" ||
		projection.RuntimeGeneration == 0 ||
		projection.AgentSessionID == "" ||
		projection.TraceID == "" {
		return fmt.Errorf("Langfuse projection identity is incomplete")
	}
	if len(projection.Turns) != 2 {
		return fmt.Errorf("Langfuse projection requires exactly two completed turns")
	}
	seenTurns := make(map[string]struct{}, len(projection.Turns))
	seenObservations := make(map[string]struct{})
	for _, turn := range projection.Turns {
		if turn.AgentTurnID == "" || turn.StartedAt.IsZero() || turn.EndedAt.Before(turn.StartedAt) {
			return fmt.Errorf("Langfuse completed turn is invalid")
		}
		if _, duplicate := seenTurns[turn.AgentTurnID]; duplicate {
			return fmt.Errorf("Langfuse completed turn identity is duplicated")
		}
		seenTurns[turn.AgentTurnID] = struct{}{}
		for _, observation := range turn.ModelObservations {
			if observation.ID == "" ||
				observation.Model == "" ||
				observation.StartedAt.IsZero() ||
				observation.EndedAt.Before(observation.StartedAt) ||
				observation.LatencyMS < 0 ||
				observation.InputTokens < 0 ||
				observation.OutputTokens < 0 ||
				observation.TotalTokens < 0 ||
				observation.CostUSD < 0 ||
				math.IsNaN(observation.CostUSD) ||
				math.IsInf(observation.CostUSD, 0) ||
				observation.Status == "" {
				return fmt.Errorf("Langfuse model observation is invalid")
			}
			if _, duplicate := seenObservations[observation.ID]; duplicate {
				return fmt.Errorf("Langfuse model observation identity is duplicated")
			}
			seenObservations[observation.ID] = struct{}{}
		}
	}
	return nil
}
