package langfuse

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
)

type ProjectorOptions struct {
	Now            func() time.Time
	NewEventID     func() string
	KnownTokens    KnownTokenScanner
	ExportAttempts int
	RetryInterval  time.Duration
	PollInterval   time.Duration
	VerifyTimeout  time.Duration
}

type Projector struct {
	api            API
	knownTokens    KnownTokenScanner
	now            func() time.Time
	newEventID     func() string
	exportAttempts int
	retryInterval  time.Duration
	pollInterval   time.Duration
	verifyTimeout  time.Duration
}

type KnownTokenScanner interface {
	ScanJSON(context.Context, string, any) error
}

func NewProjector(api API, options ProjectorOptions) (*Projector, error) {
	if api == nil || options.NewEventID == nil || options.KnownTokens == nil {
		return nil, fmt.Errorf("Langfuse API, event ID generator and known-token scanner are required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	attempts := options.ExportAttempts
	if attempts == 0 {
		attempts = 3
	}
	retry := options.RetryInterval
	if retry == 0 {
		retry = time.Second
	}
	poll := options.PollInterval
	if poll == 0 {
		poll = time.Second
	}
	timeout := options.VerifyTimeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	if attempts < 1 || attempts > 10 || retry < 0 || poll <= 0 || timeout <= 0 {
		return nil, fmt.Errorf("invalid Langfuse projection retry policy")
	}
	return &Projector{
		api: api, knownTokens: options.KnownTokens, now: now,
		newEventID: options.NewEventID, exportAttempts: attempts,
		retryInterval: retry, pollInterval: poll, verifyTimeout: timeout,
	}, nil
}

func (projector *Projector) ExportAndVerify(
	ctx context.Context,
	projection artifactapp.RunObservabilityProjection,
) error {
	if err := artifactapp.ValidateRunObservabilityProjection(projection); err != nil {
		return err
	}
	batch, expected, err := projector.buildBatch(projection)
	if err != nil {
		return err
	}
	if err := projector.knownTokens.ScanJSON(ctx, "langfuse.ingestion.batch", batch); err != nil {
		return fmt.Errorf("Langfuse ingestion batch failed known-token scan: %w", err)
	}
	var exportErr error
	for attempt := 1; attempt <= projector.exportAttempts; attempt++ {
		exportErr = projector.api.Ingest(ctx, batch)
		if exportErr == nil {
			break
		}
		if attempt < projector.exportAttempts {
			if err := wait(ctx, projector.retryInterval); err != nil {
				return fmt.Errorf("wait for Langfuse export retry: %w", err)
			}
		}
	}
	if exportErr != nil {
		return fmt.Errorf("Langfuse export failed after %d bounded attempts: %w", projector.exportAttempts, exportErr)
	}
	return projector.verify(ctx, projection.TraceID, expected)
}

type expectedProjection struct {
	Metadata     RuntimeMetadata
	TurnIDs      []string
	Generations  map[string]string
	TraceInput   json.RawMessage
	TraceOutput  json.RawMessage
	TurnContent  map[string]inputOutput
	ModelContent map[string]inputOutput
}

type inputOutput struct {
	Input  json.RawMessage
	Output json.RawMessage
}

func (projector *Projector) buildBatch(
	projection artifactapp.RunObservabilityProjection,
) (IngestionBatch, expectedProjection, error) {
	turnIDs := make([]string, len(projection.Turns))
	for index, turn := range projection.Turns {
		turnIDs[index] = string(turn.AgentTurnID)
	}
	metadata := runtimeMetadata(projection, turnIDs)
	expected := expectedProjection{
		Metadata: metadata, TurnIDs: turnIDs, Generations: make(map[string]string),
		TraceInput: projection.Turns[0].Input, TraceOutput: projection.Turns[len(projection.Turns)-1].Output,
		TurnContent: make(map[string]inputOutput), ModelContent: make(map[string]inputOutput),
	}
	eventTime := projector.now().UTC()
	events := make([]IngestionEvent, 0, 1+len(projection.Turns)*2)
	traceEvent, err := projector.event(eventTime, "trace-create", map[string]any{
		"id": projection.TraceID, "name": "worker.run", "timestamp": projection.Turns[0].StartedAt.UTC(),
		"input": expected.TraceInput, "output": expected.TraceOutput, "metadata": metadata,
	})
	if err != nil {
		return IngestionBatch{}, expectedProjection{}, err
	}
	events = append(events, traceEvent)
	for _, turn := range projection.Turns {
		expected.TurnContent[string(turn.AgentTurnID)] = inputOutput{Input: turn.Input, Output: turn.Output}
		turnMetadata := observationMetadata(metadata, string(turn.AgentTurnID))
		span, err := projector.event(eventTime, "span-create", map[string]any{
			"id": turn.AgentTurnID, "traceId": projection.TraceID, "name": "agent.turn",
			"startTime": turn.StartedAt.UTC(), "endTime": turn.EndedAt.UTC(),
			"input": turn.Input, "output": turn.Output, "metadata": turnMetadata,
		})
		if err != nil {
			return IngestionBatch{}, expectedProjection{}, err
		}
		events = append(events, span)
		for _, observation := range turn.ModelObservations {
			expected.Generations[observation.ObservationID] = string(turn.AgentTurnID)
			expected.ModelContent[observation.ObservationID] = inputOutput{
				Input: observation.Input, Output: observation.Output,
			}
			generationMetadata := observationMetadata(metadata, string(turn.AgentTurnID))
			generationMetadata["model_route"] = observation.Route
			generationMetadata["status"] = observation.Status
			level := "DEFAULT"
			if observation.Status != "ok" {
				level = "ERROR"
			}
			generation, err := projector.event(eventTime, "generation-create", map[string]any{
				"id": observation.ObservationID, "traceId": projection.TraceID,
				"parentObservationId": turn.AgentTurnID, "name": "model.generation",
				"model": observation.Model, "startTime": observation.StartedAt.UTC(),
				"endTime": observation.EndedAt.UTC(),
				"input":   observation.Input, "output": observation.Output,
				"usageDetails": map[string]any{
					"input": observation.InputTokens, "output": observation.OutputTokens,
					"total": observation.InputTokens + observation.OutputTokens,
				},
				"statusMessage": observation.Status, "level": level, "metadata": generationMetadata,
			})
			if err != nil {
				return IngestionBatch{}, expectedProjection{}, err
			}
			events = append(events, generation)
		}
	}
	return IngestionBatch{Batch: events}, expected, nil
}

func (projector *Projector) event(at time.Time, kind string, body map[string]any) (IngestionEvent, error) {
	id := projector.newEventID()
	if id == "" {
		return IngestionEvent{}, fmt.Errorf("Langfuse event ID is required")
	}
	return IngestionEvent{ID: id, Timestamp: at, Type: kind, Body: body}, nil
}

func (projector *Projector) verify(ctx context.Context, traceID string, expected expectedProjection) error {
	deadline := projector.now().Add(projector.verifyTimeout)
	lastSummary := "no response observed"
	for {
		if !projector.now().Before(deadline) {
			return fmt.Errorf("Langfuse exact verification timed out: %s", lastSummary)
		}
		trace, err := projector.api.GetTrace(ctx, traceID)
		switch {
		case err == nil:
			if err := projector.knownTokens.ScanJSON(ctx, "langfuse.query.trace", trace); err != nil {
				return fmt.Errorf("Langfuse query result failed known-token scan: %w", err)
			}
			matched, summary := projectionMatches(trace, traceID, expected)
			lastSummary = summary
			if matched {
				return nil
			}
		case IsNotFound(err):
			lastSummary = "HTTP 404"
		default:
			return fmt.Errorf("query Langfuse projection: %w", err)
		}
		remaining := deadline.Sub(projector.now())
		if remaining <= 0 {
			continue
		}
		if err := wait(ctx, min(projector.pollInterval, remaining)); err != nil {
			return fmt.Errorf("wait for Langfuse projection: %w", err)
		}
	}
}

func projectionMatches(trace Trace, traceID string, expected expectedProjection) (bool, string) {
	metadataMatch := runtimeMetadataEqual(trace.Metadata, expected.Metadata)
	traceContentMatch := jsonEqual(trace.Input, expected.TraceInput) && jsonEqual(trace.Output, expected.TraceOutput)
	expectedTurns := make(map[string]struct{}, len(expected.TurnIDs))
	for _, turnID := range expected.TurnIDs {
		expectedTurns[turnID] = struct{}{}
	}
	turnCount := 0
	generationCount := 0
	matchedTurns := make(map[string]struct{})
	matchedGenerations := make(map[string]struct{})
	metadataMatches := true
	for _, observation := range trace.Observations {
		switch {
		case observation.Type == "SPAN" && observation.Name == "agent.turn":
			turnCount++
			observationMatches := observationMetadataEqual(observation.Metadata, expected.Metadata, observation.ID)
			if !observationMatches {
				metadataMatches = false
			}
			content, exists := expected.TurnContent[observation.ID]
			contentMatches := exists && jsonEqual(observation.Input, content.Input) && jsonEqual(observation.Output, content.Output)
			if _, expectedTurn := expectedTurns[observation.ID]; expectedTurn && observation.TraceID == traceID &&
				observationMatches && contentMatches {
				matchedTurns[observation.ID] = struct{}{}
			}
		case observation.Type == "GENERATION":
			generationCount++
			parent, exists := expected.Generations[observation.ID]
			content := expected.ModelContent[observation.ID]
			contentMatches := jsonEqual(observation.Input, content.Input) && jsonEqual(observation.Output, content.Output)
			observationMatches := observationMetadataEqual(observation.Metadata, expected.Metadata, parent)
			if !observationMatches {
				metadataMatches = false
			}
			if exists && observation.TraceID == traceID && observation.ParentObservationID != nil &&
				*observation.ParentObservationID == parent && observationMatches && contentMatches {
				matchedGenerations[observation.ID] = struct{}{}
			}
		}
	}
	summary := fmt.Sprintf(
		"trace_match=%t metadata_match=%t trace_content_match=%t observation_metadata_match=%t turn_spans=%d matched_turns=%d/%d generations=%d matched_generations=%d/%d",
		trace.ID == traceID, metadataMatch, traceContentMatch, metadataMatches, turnCount, len(matchedTurns), len(expected.TurnIDs),
		generationCount, len(matchedGenerations), len(expected.Generations),
	)
	return trace.ID == traceID && metadataMatch && traceContentMatch && metadataMatches &&
		turnCount == len(expected.TurnIDs) && len(matchedTurns) == len(expected.TurnIDs) &&
		generationCount == len(expected.Generations) && len(matchedGenerations) == len(expected.Generations), summary
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue any
	var rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}

func runtimeMetadata(projection artifactapp.RunObservabilityProjection, turnIDs []string) RuntimeMetadata {
	return RuntimeMetadata{
		TenantID: projection.TenantID, UserID: projection.UserID, WorkspaceID: projection.WorkspaceID,
		WorkerSessionID: string(projection.WorkerSessionID), WorkerRunID: string(projection.WorkerRunID),
		RuntimeInstanceID: string(projection.RuntimeInstanceID), RuntimeGeneration: projection.RuntimeGeneration,
		AgentSessionID: string(projection.AgentSessionID), AgentTurnIDs: append([]string(nil), turnIDs...),
	}
}

func observationMetadata(metadata RuntimeMetadata, turnID string) map[string]any {
	encoded, _ := json.Marshal(metadata)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	result["agent_turn_id"] = turnID
	return result
}

func observationMetadataEqual(got map[string]any, want RuntimeMetadata, turnID string) bool {
	encoded, err := json.Marshal(got)
	if err != nil {
		return false
	}
	var decoded struct {
		RuntimeMetadata
		AgentTurnID string `json:"agent_turn_id"`
	}
	return json.Unmarshal(encoded, &decoded) == nil && decoded.AgentTurnID == turnID &&
		runtimeMetadataEqual(decoded.RuntimeMetadata, want)
}

func runtimeMetadataEqual(left, right RuntimeMetadata) bool {
	return left.TenantID == right.TenantID && left.UserID == right.UserID && left.WorkspaceID == right.WorkspaceID &&
		left.WorkerSessionID == right.WorkerSessionID && left.WorkerRunID == right.WorkerRunID &&
		left.RuntimeInstanceID == right.RuntimeInstanceID && left.RuntimeGeneration == right.RuntimeGeneration &&
		left.AgentSessionID == right.AgentSessionID && slices.Equal(left.AgentTurnIDs, right.AgentTurnIDs)
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ artifactapp.LangfuseProjector = (*Projector)(nil)
