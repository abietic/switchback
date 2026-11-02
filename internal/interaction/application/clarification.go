package application

import (
	"encoding/json"
	"fmt"

	"github.com/abietic/switchback/internal/interaction/domain"
)

var clarificationPriority = []domain.ReadinessDimension{
	domain.DimensionGoal,
	domain.DimensionDeliverableAcceptance,
	domain.DimensionScopeTarget,
	domain.DimensionConstraints,
	domain.DimensionContextAccess,
	domain.DimensionRiskApproval,
	domain.DimensionExecutionPolicy,
}

var clarificationQuestions = map[domain.ReadinessDimension]string{
	domain.DimensionGoal:                  "What concrete outcome should this work achieve?",
	domain.DimensionDeliverableAcceptance: "What observable result should be used to accept the work?",
	domain.DimensionScopeTarget:           "Which exact target should the worker change or inspect?",
	domain.DimensionConstraints:           "Which constraints must the worker preserve?",
	domain.DimensionContextAccess:         "Which authorized Workspace should this work use?",
	domain.DimensionRiskApproval:          "Which external or protected effects may this work perform?",
	domain.DimensionExecutionPolicy:       "Which output and review mode should the worker use?",
}

type ClarificationPlan struct {
	Dimension              domain.ReadinessDimension
	Question               string
	Checkpoint             *domain.ClarificationCheckpoint
	NextQuestionBudgetUsed uint64
}

type ClarificationPlanner struct {
	IDs   IDGenerator
	Clock Clock
}

func (planner ClarificationPlanner) Plan(
	assessment domain.TaskReadinessAssessment,
	workingDraft json.RawMessage,
	taskDraftID string,
	taskDraftRevision uint64,
) (ClarificationPlan, error) {
	if planner.IDs == nil || planner.Clock == nil {
		return ClarificationPlan{}, fmt.Errorf("ClarificationPlanner dependencies are required")
	}
	if err := assessment.Validate(); err != nil || assessment.Status != domain.ReadinessClarifying {
		return ClarificationPlan{}, fmt.Errorf("clarification requires one valid blocking assessment")
	}
	var dimension domain.ReadinessDimension
	for _, candidate := range clarificationPriority {
		if assessment.Dimensions[candidate].Status == domain.DimensionBlockingUnknown {
			dimension = candidate
			break
		}
	}
	if dimension == "" {
		return ClarificationPlan{}, fmt.Errorf("clarification has no blocking dimension")
	}
	if len(workingDraft) == 0 || !json.Valid(workingDraft) {
		return ClarificationPlan{}, fmt.Errorf("clarification working draft must be JSON")
	}
	plan := ClarificationPlan{
		Dimension: dimension, Question: clarificationQuestions[dimension],
		NextQuestionBudgetUsed: assessment.ClarificationQuestions,
	}
	if assessment.ClarificationQuestions < 3 {
		return plan, nil
	}
	checkpointID, err := planner.IDs.NewID("clarification-checkpoint")
	if err != nil {
		return ClarificationPlan{}, fmt.Errorf("generate ClarificationCheckpoint id: %w", err)
	}
	evidence := make([]string, 0)
	for _, state := range assessment.Dimensions {
		evidence = append(evidence, state.EvidenceRefs...)
	}
	plan.Checkpoint = &domain.ClarificationCheckpoint{
		ID: checkpointID, ConversationID: assessment.ConversationID, Revision: 1,
		TaskDraftID: taskDraftID, TaskDraftRevision: taskDraftRevision,
		WorkingDraft:          append(json.RawMessage(nil), workingDraft...),
		ReadinessAssessmentID: assessment.ID,
		Assumptions:           append([]string{}, assessment.Assumptions...),
		Blockers:              append([]string{}, assessment.BlockingUnknowns...),
		EvidenceRefs:          evidence,
		AllowedActions: []domain.CheckpointAction{
			domain.CheckpointContinue, domain.CheckpointNarrowScope, domain.CheckpointCancel,
		},
		Status: domain.CheckpointActive, CreatedAt: planner.Clock.Now().UTC(),
	}
	if err := plan.Checkpoint.Validate(); err != nil {
		return ClarificationPlan{}, err
	}
	return plan, nil
}

func ResetClarificationQuestionBudget(action domain.CheckpointAction) (uint64, error) {
	switch action {
	case domain.CheckpointContinue, domain.CheckpointNarrowScope:
		return 0, nil
	case domain.CheckpointCancel:
		return 0, domain.ErrTerminal
	default:
		return 0, domain.ErrInvalidState
	}
}
