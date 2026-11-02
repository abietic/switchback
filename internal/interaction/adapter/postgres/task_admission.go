package postgres

import (
	"context"
	"time"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	taskdomain "github.com/abietic/switchback/internal/taskorchestration/domain"
)

type TaskAdmissionAdapter struct {
	Store *taskpostgres.Store
	IDs   taskapp.IDGenerator
	Now   func() time.Time
}

func (adapter TaskAdmissionAdapter) CreateAssistantTask(
	ctx context.Context,
	request interactionapp.AssistantTaskRequest,
) (taskapp.CreatedTask, error) {
	dimensions := make(map[taskdomain.ReadinessDimension]taskdomain.DimensionStatus, len(request.Readiness.Dimensions))
	for dimension, state := range request.Readiness.Dimensions {
		dimensions[taskdomain.ReadinessDimension(dimension)] = taskdomain.DimensionStatus(state.Status)
	}
	readiness := taskdomain.TaskReadinessAssessment{
		AssessmentID: request.Readiness.ID, Source: "assistant",
		PolicyRevision: request.Readiness.PolicyRevision, Dimensions: dimensions,
		BlockingUnknowns:       append([]string{}, request.Readiness.BlockingUnknowns...),
		Assumptions:            append([]string{}, request.Readiness.Assumptions...),
		ClarificationQuestions: int(request.Readiness.ClarificationQuestions),
		Status:                 taskdomain.ReadinessStatus(request.Readiness.Status),
		EvaluatedAt:            request.Readiness.EvaluatedAt,
	}
	return (taskapp.CreateTaskService{Store: adapter.Store, IDs: adapter.IDs, Now: adapter.Now}).Execute(
		ctx,
		taskapp.CreateTaskCommand{
			Actor: request.Actor, TaskSpec: request.TaskSpec, AcceptanceMode: request.AcceptanceMode,
			IdempotencyKey: request.IdempotencyKey,
			AssistantAdmission: &taskapp.AssistantAdmissionEvidence{
				Intent: request.AssistantIntent, Readiness: readiness,
				ConversationRevision:       request.AssistantIntent.ConversationRevision,
				TaskDraftRevision:          request.AssistantIntent.TaskDraftRevision,
				AssistantReadinessRevision: request.AssistantReadinessRevision,
			},
		},
	)
}

var _ interactionapp.TaskAdmission = TaskAdmissionAdapter{}
