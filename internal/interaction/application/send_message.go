package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

const sendMessageOperation = "assistant.send_message"

type SendConversationMessageCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	IdempotencyKey               string
	Content                      string
}

type SendConversationMessageResult struct {
	CommandID            string `json:"command_id"`
	ConversationID       string `json:"conversation_id"`
	ConversationRevision uint64 `json:"conversation_revision"`
	TurnID               string `json:"turn_id"`
	AssistantRunID       string `json:"assistant_run_id"`
	ProjectionRef        string `json:"projection_ref"`
	Status               string `json:"status"`
}

type SendConversationMessage struct {
	Transactions        TransactionManager
	IDs                 IDGenerator
	Clock               Clock
	ModelProfile        config.AssistantModelProfileRef
	PolicyRevision      string
	ContextBudgetTokens int
	RunTimeout          time.Duration
}

func (service SendConversationMessage) Execute(
	ctx context.Context,
	command SendConversationMessageCommand,
) (SendConversationMessageResult, error) {
	if err := service.validate(command); err != nil {
		return SendConversationMessageResult{}, err
	}
	requestDigest, err := digestJSON(command)
	if err != nil {
		return SendConversationMessageResult{}, err
	}
	var result SendConversationMessageResult
	err = service.Transactions.WithinTransaction(ctx, func(tx Transaction) error {
		previous, found, err := tx.FindIdempotencyResult(
			ctx, command.Actor.TenantID, command.Actor.UserID, sendMessageOperation, command.IdempotencyKey,
		)
		if err != nil {
			return err
		}
		if found {
			if previous.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			if err := json.Unmarshal(previous.Response, &result); err != nil {
				return fmt.Errorf("decode stored message response: %w", err)
			}
			return nil
		}

		conversation, err := tx.LoadConversation(
			ctx, command.Actor.TenantID, command.Actor.UserID, command.ConversationID, true,
		)
		if err != nil {
			return err
		}
		commandID, err := service.IDs.NewID("assistant-command")
		if err != nil {
			return fmt.Errorf("generate Assistant command id: %w", err)
		}
		turnID, err := service.IDs.NewID("conversation-turn")
		if err != nil {
			return fmt.Errorf("generate Conversation Turn id: %w", err)
		}
		runID, err := service.IDs.NewID("assistant-run")
		if err != nil {
			return fmt.Errorf("generate AssistantRun id: %w", err)
		}
		eventID, err := service.IDs.NewID("integration-event")
		if err != nil {
			return fmt.Errorf("generate IntegrationEvent id: %w", err)
		}
		now := service.Clock.Now().UTC()
		accepted, err := conversation.AcceptMessage(
			command.ExpectedConversationRevision, turnID, runID, commandID, command.Content,
			service.ModelProfile.ID, service.PolicyRevision, service.ContextBudgetTokens,
			now, now.Add(service.RunTimeout),
		)
		if err != nil {
			return err
		}
		if err := tx.InsertTurn(ctx, accepted.Turn); err != nil {
			return err
		}
		if err := tx.InsertRun(
			ctx, accepted.Run, fmt.Sprintf("%d", service.ModelProfile.Revision), service.ModelProfile.Digest,
		); err != nil {
			return err
		}
		if err := tx.UpdateConversation(ctx, conversation, command.ExpectedConversationRevision); err != nil {
			return err
		}
		result = SendConversationMessageResult{
			CommandID: commandID, ConversationID: conversation.ID, ConversationRevision: conversation.Revision,
			TurnID: turnID, AssistantRunID: runID,
			ProjectionRef: "/api/v1/views/assistant_conversation/" + conversation.ID,
			Status:        "accepted",
		}
		payload, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode Assistant message event: %w", err)
		}
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: eventID,
			EventType: "ConversationMessageAccepted.v1", ProducerContext: "interaction",
			StreamID: "conversation/" + conversation.ID, Sequence: 1,
			TenantID: command.Actor.TenantID, AggregateType: "Conversation", AggregateID: conversation.ID,
			TraceID: runID, CorrelationID: commandID, CausationID: commandID,
			IdempotencyKey:  "message-accepted/" + command.IdempotencyKey,
			ClientCommandID: command.IdempotencyKey,
			Visibility:      "user", Payload: payload, OccurredAt: now,
		}
		if err := tx.InsertOutboxEvent(ctx, event); err != nil {
			return err
		}
		response, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode message acceptance response: %w", err)
		}
		return tx.InsertIdempotencyResult(
			ctx, command.Actor.TenantID, command.Actor.UserID, sendMessageOperation, command.IdempotencyKey,
			IdempotencyResult{
				RequestDigest: requestDigest, ResourceType: "Conversation", ResourceID: conversation.ID,
				Status: 202, Response: response,
			},
			now,
		)
	})
	return result, err
}

func (service SendConversationMessage) validate(command SendConversationMessageCommand) error {
	if service.Transactions == nil || service.IDs == nil || service.Clock == nil {
		return fmt.Errorf("SendConversationMessage dependencies are required")
	}
	if command.Actor.TenantID == "" || command.Actor.UserID == "" || command.ConversationID == "" ||
		command.ExpectedConversationRevision == 0 || command.IdempotencyKey == "" || command.Content == "" {
		return fmt.Errorf("SendConversationMessage command is incomplete")
	}
	if service.ModelProfile.ID == "" || service.ModelProfile.Revision == 0 || service.ModelProfile.Digest == "" ||
		service.PolicyRevision == "" || service.ContextBudgetTokens <= 0 || service.RunTimeout <= 0 {
		return fmt.Errorf("SendConversationMessage server policy is incomplete")
	}
	return nil
}

func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode digest payload: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func IsStaleConversation(err error) bool {
	return errors.Is(err, ErrConversationUnavailable) || errors.Is(err, domain.ErrStaleRevision)
}
