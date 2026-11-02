package application

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/interaction/domain"
)

func TestClarificationPlannerAsksOnlyHighestPriorityBlocker(t *testing.T) {
	assessment := blockingAssessment(2)
	assessment.Dimensions[domain.DimensionGoal] = domain.DimensionState{
		Status: domain.DimensionBlockingUnknown, Summary: "goal missing",
	}
	assessment.BlockingUnknowns = []string{"goal", "context_access"}
	ids := contextIDs(0)
	plan, err := (ClarificationPlanner{IDs: &ids, Clock: fixedClock(assessment.EvaluatedAt)}).Plan(
		assessment, json.RawMessage(`{"summary":"partial"}`), "", 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Dimension != domain.DimensionGoal ||
		plan.Question != "What concrete outcome should this work achieve?" ||
		plan.Checkpoint != nil {
		t.Fatalf("unexpected clarification plan: %+v", plan)
	}
}

func TestClarificationPlannerCreatesCheckpointOnThirdQuestion(t *testing.T) {
	assessment := blockingAssessment(3)
	ids := contextIDs(0)
	plan, err := (ClarificationPlanner{IDs: &ids, Clock: fixedClock(assessment.EvaluatedAt)}).Plan(
		assessment, json.RawMessage(`{"summary":"partial"}`), "", 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Checkpoint == nil || plan.Checkpoint.TaskDraftID != "" ||
		len(plan.Checkpoint.AllowedActions) != 3 {
		t.Fatalf("third clarification did not checkpoint partial work: %+v", plan)
	}
}

func TestClarificationCheckpointFencesStaleActionAndResetsBudget(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	checkpoint := domain.ClarificationCheckpoint{
		ID: "checkpoint-1", ConversationID: "conversation-1", Revision: 1,
		WorkingDraft:          json.RawMessage(`{"summary":"partial"}`),
		ReadinessAssessmentID: "assessment-1", Blockers: []string{"context_access"},
		Assumptions: []string{}, EvidenceRefs: []string{},
		AllowedActions: []domain.CheckpointAction{
			domain.CheckpointContinue, domain.CheckpointNarrowScope, domain.CheckpointCancel,
		},
		Status: domain.CheckpointActive, CreatedAt: now,
	}
	if err := checkpoint.Apply(2, domain.CheckpointContinue, now.Add(time.Second)); !errors.Is(err, domain.ErrStaleRevision) {
		t.Fatalf("stale checkpoint action = %v", err)
	}
	if err := checkpoint.Apply(1, domain.CheckpointNarrowScope, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if budget, err := ResetClarificationQuestionBudget(domain.CheckpointNarrowScope); err != nil || budget != 0 {
		t.Fatalf("question budget reset: budget=%d error=%v", budget, err)
	}
}

func blockingAssessment(questionCount uint64) domain.TaskReadinessAssessment {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	dimensions := make(map[domain.ReadinessDimension]domain.DimensionState)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		dimensions[dimension] = domain.DimensionState{Status: domain.DimensionKnown, Summary: "known"}
	}
	dimensions[domain.DimensionContextAccess] = domain.DimensionState{
		Status: domain.DimensionBlockingUnknown, Summary: "Workspace is unresolved",
		EvidenceRefs: []string{"turn-1"},
	}
	return domain.TaskReadinessAssessment{
		ID: "assessment-1", ConversationID: "conversation-1", AssistantRunID: "run-1",
		Revision: 2, PolicyRevision: "assistant-readiness-v1", Dimensions: dimensions,
		BlockingUnknowns: []string{"context_access"}, Assumptions: []string{},
		ClarificationQuestions: questionCount, Status: domain.ReadinessClarifying, EvaluatedAt: now,
	}
}
