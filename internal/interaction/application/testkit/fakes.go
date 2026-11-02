// Package testkit contains deterministic Interaction test adapters.
package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
)

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(now time.Time) *Clock {
	return &Clock{now: now.UTC()}
}

func (clock *Clock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *Clock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

type IDs struct {
	mu       sync.Mutex
	counters map[string]int
}

func (ids *IDs) NewID(kind string) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("identity kind is required")
	}
	ids.mu.Lock()
	defer ids.mu.Unlock()
	if ids.counters == nil {
		ids.counters = make(map[string]int)
	}
	ids.counters[kind]++
	return fmt.Sprintf("%s-%d", kind, ids.counters[kind]), nil
}

type Model struct {
	mu       sync.Mutex
	Results  []interactionapp.ModelResult
	Errors   []error
	Requests []interactionapp.ModelRequest
}

func (model *Model) Complete(_ context.Context, request interactionapp.ModelRequest) (interactionapp.ModelResult, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	model.Requests = append(model.Requests, request)
	index := len(model.Requests) - 1
	if index < len(model.Errors) && model.Errors[index] != nil {
		return interactionapp.ModelResult{}, model.Errors[index]
	}
	if index >= len(model.Results) {
		return interactionapp.ModelResult{}, fmt.Errorf("scripted Assistant model result %d is missing", index)
	}
	return model.Results[index], nil
}

type ObjectStore struct {
	mu         sync.Mutex
	Boundaries []interactionapp.EvidenceBoundary
	Result     interactionapp.EvidenceResult
	Err        error
}

func (store *ObjectStore) WriteConversationEvidence(
	_ context.Context,
	boundary interactionapp.EvidenceBoundary,
) (interactionapp.EvidenceResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.Boundaries = append(store.Boundaries, boundary)
	return store.Result, store.Err
}

type Projection struct {
	mu      sync.Mutex
	Updates []interactionapp.ProjectionUpdate
	Err     error
}

func (projection *Projection) PublishAssistantProjection(
	_ context.Context,
	update interactionapp.ProjectionUpdate,
) error {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	projection.Updates = append(projection.Updates, update)
	return projection.Err
}

type Observability struct {
	mu           sync.Mutex
	Observations []interactionapp.ModelObservation
	Err          error
}

func (observability *Observability) RecordAssistantModel(
	_ context.Context,
	observation interactionapp.ModelObservation,
) error {
	observability.mu.Lock()
	defer observability.mu.Unlock()
	observability.Observations = append(observability.Observations, observation)
	return observability.Err
}

func ClarificationDecision(assessment contractsv1.AssistantReadinessAssessment, question string) interactionapp.ModelResult {
	return interactionapp.ModelResult{Decision: contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "clarification",
		UserVisibleContent: question, ClarificationQuestion: question, Readiness: assessment,
	}}
}
