package application

import (
	"context"

	"github.com/abietic/switchback/internal/interaction/domain"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

type CheckpointActionCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	CheckpointID                 string
	CheckpointRevision           uint64
	Action                       domain.CheckpointAction
	IdempotencyKey               string
}

type CancelQueuedRunCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	AssistantRunID               string
	AssistantRunRevision         uint64
	IdempotencyKey               string
}

type RetryFailedRunCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	AssistantRunID               string
	AssistantRunRevision         uint64
	IdempotencyKey               string
}

type ExactCommandService interface {
	ApplyCheckpointAction(context.Context, CheckpointActionCommand) (ConversationCommandResult, error)
	CancelQueuedRun(context.Context, CancelQueuedRunCommand) (ConversationCommandResult, error)
	RetryFailedRun(context.Context, RetryFailedRunCommand) (ConversationCommandResult, error)
}
