package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

type ConversationCommandResult struct {
	CommandID            string `json:"command_id"`
	ConversationID       string `json:"conversation_id"`
	ConversationRevision uint64 `json:"conversation_revision"`
	AssistantRunID       string `json:"assistant_run_id,omitempty"`
	AssistantRunRevision uint64 `json:"assistant_run_revision,omitempty"`
	TaskID               string `json:"task_id,omitempty"`
	TaskRevision         uint64 `json:"task_revision,omitempty"`
	ProjectionRef        string `json:"projection_ref"`
	Status               string `json:"status"`
}

type CreateConversationCommand struct {
	Actor          taskapp.Actor
	IdempotencyKey string
}

type CreateConversation struct {
	Transactions TransactionManager
	IDs          IDGenerator
	Clock        Clock
}

func (service CreateConversation) Execute(
	ctx context.Context,
	command CreateConversationCommand,
) (ConversationCommandResult, error) {
	if service.Transactions == nil || service.IDs == nil || service.Clock == nil ||
		command.Actor.TenantID == "" || command.Actor.UserID == "" || command.IdempotencyKey == "" {
		return ConversationCommandResult{}, fmt.Errorf("CreateConversation inputs are incomplete")
	}
	return service.execute(ctx, command)
}

func (service CreateConversation) execute(
	ctx context.Context,
	command CreateConversationCommand,
) (ConversationCommandResult, error) {
	requestDigest, err := digestJSON(command)
	if err != nil {
		return ConversationCommandResult{}, err
	}
	var result ConversationCommandResult
	err = service.Transactions.WithinTransaction(ctx, func(tx Transaction) error {
		previous, found, err := tx.FindIdempotencyResult(
			ctx, command.Actor.TenantID, command.Actor.UserID,
			"assistant.create_conversation", command.IdempotencyKey,
		)
		if err != nil {
			return err
		}
		if found {
			if previous.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			return json.Unmarshal(previous.Response, &result)
		}
		conversationID, err := service.IDs.NewID("assistant-conversation")
		if err != nil {
			return err
		}
		commandID, err := service.IDs.NewID("assistant-command")
		if err != nil {
			return err
		}
		eventID, err := service.IDs.NewID("integration-event")
		if err != nil {
			return err
		}
		now := service.Clock.Now().UTC()
		conversation, err := domain.NewConversation(
			conversationID, command.Actor.TenantID, command.Actor.UserID, now,
		)
		if err != nil {
			return err
		}
		if err := tx.InsertConversation(ctx, conversation); err != nil {
			return err
		}
		result = conversationResult(commandID, conversation)
		payload, _ := json.Marshal(result)
		if err := tx.InsertOutboxEvent(ctx, contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
			EventID:       eventID, EventType: "ConversationCreated.v1", ProducerContext: "interaction",
			StreamID: "conversation/" + conversation.ID, Sequence: 1,
			TenantID: command.Actor.TenantID, AggregateType: "Conversation", AggregateID: conversation.ID,
			TraceID: conversation.ID, CorrelationID: commandID, CausationID: commandID,
			IdempotencyKey:  "conversation-created/" + command.IdempotencyKey,
			ClientCommandID: command.IdempotencyKey,
			Visibility:      "user", Payload: payload, OccurredAt: now,
		}); err != nil {
			return err
		}
		return insertConversationIdempotency(
			ctx, tx, command.Actor, "assistant.create_conversation", command.IdempotencyKey,
			requestDigest, result, now,
		)
	})
	return result, err
}

type OpenConversationCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	IdempotencyKey               string
}

type OpenConversation struct {
	Transactions TransactionManager
	IDs          IDGenerator
	Clock        Clock
}

func (service OpenConversation) Execute(
	ctx context.Context,
	command OpenConversationCommand,
) (ConversationCommandResult, error) {
	if service.Transactions == nil || service.IDs == nil || service.Clock == nil ||
		command.Actor.TenantID == "" || command.Actor.UserID == "" ||
		command.ConversationID == "" || command.ExpectedConversationRevision == 0 ||
		command.IdempotencyKey == "" {
		return ConversationCommandResult{}, fmt.Errorf("OpenConversation inputs are incomplete")
	}
	requestDigest, err := digestJSON(command)
	if err != nil {
		return ConversationCommandResult{}, err
	}
	var result ConversationCommandResult
	err = service.Transactions.WithinTransaction(ctx, func(tx Transaction) error {
		previous, found, err := tx.FindIdempotencyResult(
			ctx, command.Actor.TenantID, command.Actor.UserID,
			"assistant.open_conversation", command.IdempotencyKey,
		)
		if err != nil {
			return err
		}
		if found {
			if previous.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			return json.Unmarshal(previous.Response, &result)
		}
		conversation, err := tx.LoadConversation(
			ctx, command.Actor.TenantID, command.Actor.UserID, command.ConversationID, false,
		)
		if err != nil {
			return err
		}
		if conversation.Revision != command.ExpectedConversationRevision {
			return domain.ErrStaleRevision
		}
		commandID, err := service.IDs.NewID("assistant-command")
		if err != nil {
			return err
		}
		result = conversationResult(commandID, conversation)
		result.Status = "completed"
		return insertConversationIdempotency(
			ctx, tx, command.Actor, "assistant.open_conversation", command.IdempotencyKey,
			requestDigest, result, service.Clock.Now().UTC(),
		)
	})
	return result, err
}

type SelectWorkspaceCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	IdempotencyKey               string
	Workspace                    domain.WorkspaceResolution
}

type SelectConversationWorkspace struct {
	Transactions TransactionManager
	IDs          IDGenerator
	Clock        Clock
}

func (service SelectConversationWorkspace) Execute(
	ctx context.Context,
	command SelectWorkspaceCommand,
) (ConversationCommandResult, error) {
	if service.Transactions == nil || service.IDs == nil || service.Clock == nil ||
		command.Actor.TenantID == "" || command.Actor.UserID == "" ||
		command.ConversationID == "" || command.ExpectedConversationRevision == 0 ||
		command.IdempotencyKey == "" {
		return ConversationCommandResult{}, fmt.Errorf("SelectConversationWorkspace inputs are incomplete")
	}
	requestDigest, err := digestJSON(command)
	if err != nil {
		return ConversationCommandResult{}, err
	}
	var result ConversationCommandResult
	err = service.Transactions.WithinTransaction(ctx, func(tx Transaction) error {
		previous, found, err := tx.FindIdempotencyResult(
			ctx, command.Actor.TenantID, command.Actor.UserID,
			"assistant.select_workspace", command.IdempotencyKey,
		)
		if err != nil {
			return err
		}
		if found {
			if previous.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			return json.Unmarshal(previous.Response, &result)
		}
		conversation, err := tx.LoadConversation(
			ctx, command.Actor.TenantID, command.Actor.UserID, command.ConversationID, true,
		)
		if err != nil {
			return err
		}
		commandID, err := service.IDs.NewID("assistant-command")
		if err != nil {
			return err
		}
		eventID, err := service.IDs.NewID("integration-event")
		if err != nil {
			return err
		}
		now := service.Clock.Now().UTC()
		if err := conversation.SelectWorkspace(
			command.ExpectedConversationRevision, command.Workspace, now,
		); err != nil {
			return err
		}
		if err := tx.UpdateConversation(
			ctx, conversation, command.ExpectedConversationRevision,
		); err != nil {
			return err
		}
		result = conversationResult(commandID, conversation)
		payload, _ := json.Marshal(result)
		if err := tx.InsertOutboxEvent(ctx, contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
			EventID:       eventID, EventType: "ConversationWorkspaceSelected.v1",
			ProducerContext: "interaction", StreamID: "conversation/" + conversation.ID, Sequence: 1,
			TenantID: command.Actor.TenantID, AggregateType: "Conversation", AggregateID: conversation.ID,
			TraceID: conversation.ID, CorrelationID: commandID, CausationID: commandID,
			IdempotencyKey:  "workspace-selected/" + command.IdempotencyKey,
			ClientCommandID: command.IdempotencyKey,
			Visibility:      "user", Payload: payload, OccurredAt: now,
		}); err != nil {
			return err
		}
		return insertConversationIdempotency(
			ctx, tx, command.Actor, "assistant.select_workspace", command.IdempotencyKey,
			requestDigest, result, now,
		)
	})
	return result, err
}

func conversationResult(commandID string, conversation domain.Conversation) ConversationCommandResult {
	return ConversationCommandResult{
		CommandID: commandID, ConversationID: conversation.ID,
		ConversationRevision: conversation.Revision,
		ProjectionRef:        "/api/v1/views/assistant_conversation/" + conversation.ID,
		Status:               "accepted",
	}
}

func insertConversationIdempotency(
	ctx context.Context,
	tx Transaction,
	actor taskapp.Actor,
	operation, idempotencyKey, requestDigest string,
	result ConversationCommandResult,
	now time.Time,
) error {
	response, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return tx.InsertIdempotencyResult(
		ctx, actor.TenantID, actor.UserID, operation, idempotencyKey,
		IdempotencyResult{
			RequestDigest: requestDigest, ResourceType: "Conversation",
			ResourceID: result.ConversationID, Status: 202, Response: response,
		},
		now,
	)
}
