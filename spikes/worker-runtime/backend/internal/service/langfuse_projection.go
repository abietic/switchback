package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"switchback.local/spikes/worker-runtime/backend/internal/langfuse"
	"switchback.local/spikes/worker-runtime/backend/internal/runstate"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
)

// LangfuseProjector is the Backend-only derived projection boundary. Worker
// code never receives this dependency or its credentials.
type LangfuseProjector interface {
	Project(context.Context, langfuse.RunProjection) error
}

type projectionState struct {
	turns map[string]*projectionTurn
}

type projectionTurn struct {
	startedAt    time.Time
	endedAt      time.Time
	completed    bool
	observations []langfuse.ModelObservationFact
}

type langfuseProjectionWork struct {
	managed       *managedRun
	projector     LangfuseProjector
	workerRunID   string
	fenceRevision uint64
	projection    langfuse.RunProjection
}

func recordProjectionTurnStarted(
	managed *managedRun,
	turnID string,
	startedAt time.Time,
) {
	if managed.projection.turns == nil {
		managed.projection.turns = make(map[string]*projectionTurn)
	}
	managed.projection.turns[turnID] = &projectionTurn{startedAt: startedAt}
}

func modelObservationFact(
	observation *workercontrolv1.ModelObservation,
) (langfuse.ModelObservationFact, error) {
	if observation == nil {
		return langfuse.ModelObservationFact{}, fmt.Errorf("%w: ModelObservation is required", ErrInvalid)
	}
	startedAt, err := validTimestamp(observation.GetStartedAt())
	if err != nil {
		return langfuse.ModelObservationFact{}, fmt.Errorf("%w: ModelObservation started_at", ErrInvalid)
	}
	endedAt, err := validTimestamp(observation.GetEndedAt())
	if err != nil {
		return langfuse.ModelObservationFact{}, fmt.Errorf("%w: ModelObservation ended_at", ErrInvalid)
	}
	if observation.GetModelObservationId() == "" ||
		observation.GetModel() == "" ||
		observation.GetRoute() == "" ||
		startedAt.IsZero() ||
		endedAt.Before(startedAt) ||
		observation.GetLatencyMs() < 0 ||
		observation.GetInputTokens() < 0 ||
		observation.GetOutputTokens() < 0 ||
		observation.GetTotalTokens() < 0 ||
		observation.GetCostUsd() < 0 ||
		math.IsNaN(observation.GetCostUsd()) ||
		math.IsInf(observation.GetCostUsd(), 0) ||
		(observation.GetStatus() != "ok" && observation.GetStatus() != "error") {
		return langfuse.ModelObservationFact{}, fmt.Errorf("%w: invalid ModelObservation facts", ErrInvalid)
	}
	metadata := append([]byte(nil), observation.GetMetadataJson()...)
	if len(metadata) > 0 {
		var controlled map[string]any
		if err := json.Unmarshal(metadata, &controlled); err != nil {
			return langfuse.ModelObservationFact{}, fmt.Errorf("%w: invalid ModelObservation metadata", ErrInvalid)
		}
	}
	return langfuse.ModelObservationFact{
		ID:           observation.GetModelObservationId(),
		Model:        observation.GetModel(),
		Route:        observation.GetRoute(),
		StartedAt:    startedAt,
		EndedAt:      endedAt,
		LatencyMS:    observation.GetLatencyMs(),
		InputTokens:  observation.GetInputTokens(),
		OutputTokens: observation.GetOutputTokens(),
		TotalTokens:  observation.GetTotalTokens(),
		CostUSD:      observation.GetCostUsd(),
		Status:       observation.GetStatus(),
		ErrorKind:    observation.GetErrorKind(),
		MetadataJSON: metadata,
	}, nil
}

func recordModelObservation(
	managed *managedRun,
	turnID string,
	fact langfuse.ModelObservationFact,
) error {
	turn := managed.projection.turns[turnID]
	if turn == nil || turn.completed {
		return fmt.Errorf("%w: projection turn %q is not active", ErrConflict, turnID)
	}
	for _, existingTurn := range managed.projection.turns {
		for _, existing := range existingTurn.observations {
			if existing.ID == fact.ID {
				return fmt.Errorf("%w: ModelObservation %q is duplicated", ErrConflict, fact.ID)
			}
		}
	}
	turn.observations = append(turn.observations, fact)
	return nil
}

func markProjectionTurnCompleted(managed *managedRun, turnID string, endedAt time.Time) {
	turn := managed.projection.turns[turnID]
	if turn == nil {
		turn = &projectionTurn{}
		if managed.projection.turns == nil {
			managed.projection.turns = make(map[string]*projectionTurn)
		}
		managed.projection.turns[turnID] = turn
	}
	turn.endedAt = endedAt
	turn.completed = true
}

func prepareLangfuseProjection(
	managed *managedRun,
	run runstate.Run,
	projector LangfuseProjector,
) (*langfuseProjectionWork, error) {
	work := &langfuseProjectionWork{
		managed:       managed,
		projector:     projector,
		workerRunID:   run.WorkerRunID,
		fenceRevision: run.Revision,
	}
	projection := langfuse.RunProjection{
		WorkerSessionID:   run.WorkerSessionID,
		WorkerRunID:       run.WorkerRunID,
		RuntimeInstanceID: run.RuntimeInstanceID,
		RuntimeGeneration: run.RuntimeGeneration,
		AgentSessionID:    run.AgentSessionID,
		TraceID:           run.TraceID,
		Turns:             make([]langfuse.CompletedTurn, 0, len(run.Turns)),
	}
	for _, runTurn := range run.Turns {
		turn := managed.projection.turns[runTurn.ID]
		if turn == nil || !turn.completed {
			return work, fmt.Errorf("Langfuse projection facts are incomplete for a completed AgentTurn")
		}
		observations := make([]langfuse.ModelObservationFact, len(turn.observations))
		for index, observation := range turn.observations {
			observation.MetadataJSON = append([]byte(nil), observation.MetadataJSON...)
			observations[index] = observation
		}
		projection.Turns = append(projection.Turns, langfuse.CompletedTurn{
			AgentTurnID:       runTurn.ID,
			StartedAt:         turn.startedAt,
			EndedAt:           turn.endedAt,
			ModelObservations: observations,
		})
	}
	work.projection = projection
	return work, nil
}

func (c *Coordinator) runLangfuseProjection(
	work *langfuseProjectionWork,
	projectionErr error,
) {
	if projectionErr == nil {
		projectionErr = work.projector.Project(c.root, work.projection)
	}
	c.finishLangfuseProjection(work, projectionErr)
}

func (c *Coordinator) finishLangfuseProjection(
	work *langfuseProjectionWork,
	projectionErr error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	managed := c.byRun[work.workerRunID]
	if managed == nil || managed != work.managed {
		return
	}
	run := managed.store.Snapshot()
	if run.Revision != work.fenceRevision ||
		run.State != runstate.StateCompleted ||
		run.ExecutionOutcome != runstate.ExecutionCompleted {
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   "langfuse_projection",
			Code:   "result_fenced",
			Detail: "projection result did not match the authoritative completed run revision",
		})
		return
	}
	acceptance := runstate.AcceptancePassed
	if projectionErr != nil {
		acceptance = runstate.AcceptanceFailed
	}
	if _, err := managed.store.SetSpikeAcceptance(run.Revision, acceptance); err != nil {
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   "langfuse_projection",
			Code:   "acceptance_write_failed",
			Detail: err.Error(),
		})
		return
	}
	if projectionErr != nil {
		appendDiagnostic(managed, RuntimeDiagnosticSnapshot{
			Kind:   "langfuse_projection",
			Code:   "acceptance_failed",
			Detail: projectionErr.Error(),
		})
	}
}

func validTimestamp(timestamp *timestamppb.Timestamp) (time.Time, error) {
	if timestamp == nil {
		return time.Time{}, nil
	}
	if err := timestamp.CheckValid(); err != nil {
		return time.Time{}, err
	}
	return timestamp.AsTime().UTC(), nil
}
