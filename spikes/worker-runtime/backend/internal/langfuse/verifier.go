package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	defaultPollInterval = time.Second
	defaultDeadline     = 90 * time.Second
)

type TraceReader interface {
	GetTrace(context.Context, string) (Trace, error)
}

type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

type VerifierClock interface {
	Clock
	WithTimeout(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

type ExpectedGeneration struct {
	ID           string
	ParentTurnID string
}

type ExpectedProjection struct {
	Metadata    RuntimeMetadata
	TurnIDs     []string
	Generations []ExpectedGeneration
}

type VerifierOptions struct {
	Clock VerifierClock
}

type Verifier struct {
	reader       TraceReader
	pollInterval time.Duration
	deadline     time.Duration
	clock        VerifierClock
}

func NewVerifier(reader TraceReader, options VerifierOptions) (*Verifier, error) {
	if reader == nil {
		return nil, fmt.Errorf("Langfuse trace reader is required")
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	return &Verifier{
		reader:       reader,
		pollInterval: defaultPollInterval,
		deadline:     defaultDeadline,
		clock:        clock,
	}, nil
}

func (v *Verifier) Verify(
	ctx context.Context,
	traceID string,
	expected ExpectedProjection,
) error {
	if traceID == "" {
		return fmt.Errorf("Langfuse trace ID is required")
	}
	if err := validateExpectedProjection(expected); err != nil {
		return err
	}
	deadlineAt := v.clock.Now().Add(v.deadline)
	lastSummary := "no response observed"
	for {
		if !v.clock.Now().Before(deadlineAt) {
			return &VerificationTimeoutError{
				Deadline:    v.deadline,
				LastSummary: lastSummary,
			}
		}
		remaining := deadlineAt.Sub(v.clock.Now())
		queryContext, cancelQuery := v.clock.WithTimeout(ctx, remaining)
		trace, err := v.reader.GetTrace(queryContext, traceID)
		queryDeadlineExceeded := errors.Is(queryContext.Err(), context.DeadlineExceeded) &&
			ctx.Err() == nil
		cancelQuery()
		if queryDeadlineExceeded {
			return &VerificationTimeoutError{
				Deadline:    v.deadline,
				LastSummary: lastSummary,
			}
		}
		switch {
		case err == nil:
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

		remaining = deadlineAt.Sub(v.clock.Now())
		if remaining <= 0 {
			continue
		}
		wait := min(v.pollInterval, remaining)
		if err := v.clock.Wait(ctx, wait); err != nil {
			return fmt.Errorf("wait for Langfuse projection: %w", err)
		}
	}
}

type VerificationTimeoutError struct {
	Deadline    time.Duration
	LastSummary string
}

func (e *VerificationTimeoutError) Error() string {
	return fmt.Sprintf(
		"Langfuse verification timed out after %s; last observed: %s",
		e.Deadline,
		e.LastSummary,
	)
}

func validateExpectedProjection(expected ExpectedProjection) error {
	if len(expected.TurnIDs) != 2 {
		return fmt.Errorf("Langfuse verification requires exactly two AgentTurn IDs")
	}
	turnIDs := make(map[string]struct{}, len(expected.TurnIDs))
	for _, turnID := range expected.TurnIDs {
		if turnID == "" {
			return fmt.Errorf("Langfuse expected AgentTurn ID is required")
		}
		if _, duplicate := turnIDs[turnID]; duplicate {
			return fmt.Errorf("Langfuse expected AgentTurn ID is duplicated")
		}
		turnIDs[turnID] = struct{}{}
	}
	if !runtimeMetadataComplete(expected.Metadata) ||
		!slices.Equal(expected.Metadata.AgentTurnIDs, expected.TurnIDs) {
		return fmt.Errorf("Langfuse expected runtime metadata is incomplete or inconsistent")
	}
	generationIDs := make(map[string]struct{}, len(expected.Generations))
	generationsByTurn := make(map[string]int, len(expected.TurnIDs))
	for _, generation := range expected.Generations {
		if generation.ID == "" {
			return fmt.Errorf("Langfuse expected generation ID is required")
		}
		if _, duplicate := generationIDs[generation.ID]; duplicate {
			return fmt.Errorf("Langfuse expected generation ID is duplicated")
		}
		if _, knownTurn := turnIDs[generation.ParentTurnID]; !knownTurn {
			return fmt.Errorf("Langfuse expected generation parent is not an expected AgentTurn")
		}
		generationIDs[generation.ID] = struct{}{}
		generationsByTurn[generation.ParentTurnID]++
	}
	for _, turnID := range expected.TurnIDs {
		if generationsByTurn[turnID] == 0 {
			return fmt.Errorf("Langfuse verification requires at least one expected generation per AgentTurn")
		}
	}
	return nil
}

func projectionMatches(
	trace Trace,
	expectedTraceID string,
	expected ExpectedProjection,
) (bool, string) {
	traceIdentityMatches := trace.ID == expectedTraceID
	runtimeMetadataMatches := runtimeMetadataEqual(trace.Metadata, expected.Metadata)
	expectedTurns := make(map[string]struct{}, len(expected.TurnIDs))
	for _, turnID := range expected.TurnIDs {
		expectedTurns[turnID] = struct{}{}
	}
	agentTurnSpans := 0
	observationMetadataMatches := true
	matchedTurns := make(map[string]struct{}, len(expected.TurnIDs))
	generations := 0
	matchedGenerations := make(map[string]struct{}, len(expected.Generations))
	expectedGenerations := make(map[string]string, len(expected.Generations))
	for _, generation := range expected.Generations {
		expectedGenerations[generation.ID] = generation.ParentTurnID
	}
	for _, observation := range trace.Observations {
		if observation.Type == "SPAN" && observation.Name == "agent.turn" {
			agentTurnSpans++
			if !observationMetadataEqual(observation.Metadata, expected.Metadata, observation.ID) {
				observationMetadataMatches = false
			}
			if _, expectedTurn := expectedTurns[observation.ID]; expectedTurn &&
				observation.TraceID == expectedTraceID &&
				observationMetadataEqual(observation.Metadata, expected.Metadata, observation.ID) {
				matchedTurns[observation.ID] = struct{}{}
			}
		}
		if observation.Type == "GENERATION" {
			generations++
			parent, expectedGeneration := expectedGenerations[observation.ID]
			if !observationMetadataEqual(observation.Metadata, expected.Metadata, parent) {
				observationMetadataMatches = false
			}
			if expectedGeneration &&
				observation.TraceID == expectedTraceID &&
				observation.ParentObservationID != nil &&
				*observation.ParentObservationID == parent &&
				observationMetadataEqual(observation.Metadata, expected.Metadata, parent) {
				matchedGenerations[observation.ID] = struct{}{}
			}
		}
	}
	summary := fmt.Sprintf(
		"trace_identity_match=%t runtime_metadata_match=%t observation_metadata_match=%t observations=%d agent_turn_spans=%d matched_turns=%d/2 generations=%d matched_generations=%d/%d",
		traceIdentityMatches,
		runtimeMetadataMatches,
		observationMetadataMatches,
		len(trace.Observations),
		agentTurnSpans,
		len(matchedTurns),
		generations,
		len(matchedGenerations),
		len(expected.Generations),
	)
	return traceIdentityMatches &&
		runtimeMetadataMatches &&
		observationMetadataMatches &&
		agentTurnSpans == 2 &&
		len(matchedTurns) == 2 &&
		generations == len(expected.Generations) &&
		len(matchedGenerations) == len(expected.Generations), summary
}

type observationRuntimeMetadata struct {
	RuntimeMetadata
	AgentTurnID string `json:"agent_turn_id"`
}

func observationMetadataEqual(
	metadata map[string]any,
	want RuntimeMetadata,
	wantTurnID string,
) bool {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return false
	}
	var got observationRuntimeMetadata
	if err := json.Unmarshal(encoded, &got); err != nil {
		return false
	}
	return got.AgentTurnID == wantTurnID && runtimeMetadataEqual(got.RuntimeMetadata, want)
}

func runtimeMetadataComplete(metadata RuntimeMetadata) bool {
	return metadata.WorkerSessionID != "" &&
		metadata.WorkerRunID != "" &&
		metadata.RuntimeInstanceID != "" &&
		metadata.RuntimeGeneration != 0 &&
		metadata.AgentSessionID != "" &&
		len(metadata.AgentTurnIDs) == 2
}

func runtimeMetadataEqual(got, want RuntimeMetadata) bool {
	return got.WorkerSessionID == want.WorkerSessionID &&
		got.WorkerRunID == want.WorkerRunID &&
		got.RuntimeInstanceID == want.RuntimeInstanceID &&
		got.RuntimeGeneration == want.RuntimeGeneration &&
		got.AgentSessionID == want.AgentSessionID &&
		slices.Equal(got.AgentTurnIDs, want.AgentTurnIDs)
}

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now()
}

func (wallClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (wallClock) WithTimeout(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, duration)
}
