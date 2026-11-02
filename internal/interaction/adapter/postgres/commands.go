package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
)

type CommandService struct {
	Store               Store
	IDs                 interactionapp.IDGenerator
	Clock               interactionapp.Clock
	ModelProfile        config.AssistantModelProfileRef
	PolicyRevision      string
	ContextBudgetTokens int
	RunTimeout          time.Duration
}

func (service CommandService) ApplyCheckpointAction(
	ctx context.Context,
	command interactionapp.CheckpointActionCommand,
) (interactionapp.ConversationCommandResult, error) {
	if err := service.validateBase(
		command.Actor.TenantID, command.Actor.UserID, command.ConversationID,
		command.ExpectedConversationRevision, command.IdempotencyKey,
	); err != nil || command.CheckpointID == "" || command.CheckpointRevision == 0 {
		return interactionapp.ConversationCommandResult{}, fmt.Errorf("checkpoint command is incomplete")
	}
	digest, err := commandDigest(command)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txStore := transaction{tx: tx}
	if previous, found, err := txStore.FindIdempotencyResult(
		ctx, command.Actor.TenantID, command.Actor.UserID,
		"assistant.checkpoint_action", command.IdempotencyKey,
	); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	} else if found {
		if previous.RequestDigest != digest {
			return interactionapp.ConversationCommandResult{}, interactionapp.ErrIdempotencyConflict
		}
		var result interactionapp.ConversationCommandResult
		if err := json.Unmarshal(previous.Response, &result); err != nil {
			return interactionapp.ConversationCommandResult{}, err
		}
		return result, nil
	}
	conversation, err := txStore.LoadConversation(
		ctx, command.Actor.TenantID, command.Actor.UserID, command.ConversationID, true,
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if conversation.Revision != command.ExpectedConversationRevision ||
		conversation.CurrentCheckpointID != command.CheckpointID {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	checkpoint, err := loadCheckpointForUpdate(ctx, tx, command.CheckpointID, command.ConversationID)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	now := service.Clock.Now().UTC()
	if err := checkpoint.Apply(command.CheckpointRevision, command.Action, now); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE assistant_clarification_checkpoints
SET revision=$1,status=$2,resolved_action=$3,resolved_at=$4
WHERE checkpoint_id=$5 AND conversation_id=$6 AND revision=$7 AND status='active'`,
		checkpoint.Revision, checkpoint.Status, checkpoint.ResolvedAction, checkpoint.ResolvedAt,
		checkpoint.ID, checkpoint.ConversationID, command.CheckpointRevision,
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	expectedRevision := conversation.Revision
	conversation.CurrentCheckpointID = ""
	conversation.Revision++
	conversation.UpdatedAt = now
	if err := txStore.UpdateConversation(ctx, conversation, expectedRevision); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	result, err := service.finishCommand(
		ctx, txStore, command.Actor.TenantID, command.Actor.UserID,
		"assistant.checkpoint_action", command.IdempotencyKey, digest,
		conversation, "", 0, now, "ClarificationCheckpointActionApplied.v1",
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	return result, nil
}

func (service CommandService) CancelQueuedRun(
	ctx context.Context,
	command interactionapp.CancelQueuedRunCommand,
) (interactionapp.ConversationCommandResult, error) {
	if err := service.validateBase(
		command.Actor.TenantID, command.Actor.UserID, command.ConversationID,
		command.ExpectedConversationRevision, command.IdempotencyKey,
	); err != nil || command.AssistantRunID == "" || command.AssistantRunRevision == 0 {
		return interactionapp.ConversationCommandResult{}, fmt.Errorf("cancel queued AssistantRun command is incomplete")
	}
	digest, _ := commandDigest(command)
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txStore := transaction{tx: tx}
	if result, found, err := existingExactCommand(
		ctx, txStore, command.Actor.TenantID, command.Actor.UserID,
		"assistant.cancel_run", command.IdempotencyKey, digest,
	); err != nil || found {
		return result, err
	}
	conversation, err := txStore.LoadConversation(
		ctx, command.Actor.TenantID, command.Actor.UserID, command.ConversationID, true,
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if conversation.Revision != command.ExpectedConversationRevision {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	var status string
	var revision uint64
	err = tx.QueryRow(ctx, `
SELECT status,revision FROM assistant_runs
WHERE assistant_run_id=$1 AND conversation_id=$2 FOR UPDATE`,
		command.AssistantRunID, command.ConversationID,
	).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.ConversationCommandResult{}, interactionapp.ErrConversationUnavailable
	}
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if status != string(domain.RunQueued) || revision != command.AssistantRunRevision {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	now := service.Clock.Now().UTC()
	tag, err := tx.Exec(ctx, `
UPDATE assistant_runs
SET status='cancelled',error_code='user_cancelled',retryable=false,completed_at=$1,
    revision=revision+1,updated_at=$1
WHERE assistant_run_id=$2 AND revision=$3 AND status='queued'`,
		now, command.AssistantRunID, command.AssistantRunRevision,
	)
	if err != nil || tag.RowsAffected() != 1 {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	expectedRevision := conversation.Revision
	conversation.Revision++
	conversation.UpdatedAt = now
	if err := txStore.UpdateConversation(ctx, conversation, expectedRevision); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	result, err := service.finishCommand(
		ctx, txStore, command.Actor.TenantID, command.Actor.UserID,
		"assistant.cancel_run", command.IdempotencyKey, digest, conversation,
		command.AssistantRunID, command.AssistantRunRevision+1, now, "AssistantRunCancelled.v1",
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	return result, nil
}

func (service CommandService) RetryFailedRun(
	ctx context.Context,
	command interactionapp.RetryFailedRunCommand,
) (interactionapp.ConversationCommandResult, error) {
	if err := service.validateBase(
		command.Actor.TenantID, command.Actor.UserID, command.ConversationID,
		command.ExpectedConversationRevision, command.IdempotencyKey,
	); err != nil || command.AssistantRunID == "" || command.AssistantRunRevision == 0 ||
		service.ModelProfile.ID == "" || service.ModelProfile.Revision == 0 ||
		service.ModelProfile.Digest == "" || service.PolicyRevision == "" ||
		service.ContextBudgetTokens <= 0 || service.RunTimeout <= 0 {
		return interactionapp.ConversationCommandResult{}, fmt.Errorf("retry failed AssistantRun command is incomplete")
	}
	digest, _ := commandDigest(command)
	tx, err := service.Store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	txStore := transaction{tx: tx}
	if result, found, err := existingExactCommand(
		ctx, txStore, command.Actor.TenantID, command.Actor.UserID,
		"assistant.retry_run", command.IdempotencyKey, digest,
	); err != nil || found {
		return result, err
	}
	conversation, err := txStore.LoadConversation(
		ctx, command.Actor.TenantID, command.Actor.UserID, command.ConversationID, true,
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if conversation.Revision != command.ExpectedConversationRevision ||
		conversation.ActiveAssistantRunID != "" {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	var sourceTurnID, status string
	var retryable bool
	var revision uint64
	err = tx.QueryRow(ctx, `
SELECT source_turn_id,status,retryable,revision
FROM assistant_runs
WHERE assistant_run_id=$1 AND conversation_id=$2 FOR UPDATE`,
		command.AssistantRunID, command.ConversationID,
	).Scan(
		&sourceTurnID, &status, &retryable, &revision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.ConversationCommandResult{}, interactionapp.ErrConversationUnavailable
	}
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if status != string(domain.RunFailed) || !retryable || revision != command.AssistantRunRevision {
		return interactionapp.ConversationCommandResult{}, domain.ErrStaleRevision
	}
	runID, err := service.IDs.NewID("assistant-run")
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	now := service.Clock.Now().UTC()
	if _, err := tx.Exec(ctx, `
INSERT INTO assistant_runs
  (assistant_run_id,conversation_id,source_turn_id,run_sequence,status,model_profile,
   model_profile_revision,model_profile_digest,policy_revision,context_budget_tokens,
   timeout_at,revision,created_at,updated_at)
VALUES ($1,$2,$3,$4,'queued',$5,$6,$7,$8,$9,$10,1,$11,$11)`,
		runID, conversation.ID, sourceTurnID, conversation.NextRunSequence, service.ModelProfile.ID,
		strconv.FormatUint(service.ModelProfile.Revision, 10), service.ModelProfile.Digest,
		service.PolicyRevision, service.ContextBudgetTokens, now.Add(service.RunTimeout), now,
	); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	expectedRevision := conversation.Revision
	conversation.NextRunSequence++
	conversation.Revision++
	conversation.UpdatedAt = now
	if err := txStore.UpdateConversation(ctx, conversation, expectedRevision); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	result, err := service.finishCommand(
		ctx, txStore, command.Actor.TenantID, command.Actor.UserID,
		"assistant.retry_run", command.IdempotencyKey, digest, conversation,
		runID, 1, now, "AssistantRunRetried.v1",
	)
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	return result, nil
}

func (service CommandService) validateBase(
	tenantID, userID, conversationID string,
	conversationRevision uint64,
	idempotencyKey string,
) error {
	if service.Store.Pool == nil || service.IDs == nil || service.Clock == nil ||
		tenantID == "" || userID == "" || conversationID == "" ||
		conversationRevision == 0 || idempotencyKey == "" {
		return fmt.Errorf("Assistant exact command dependencies are incomplete")
	}
	return nil
}

func (service CommandService) finishCommand(
	ctx context.Context,
	txStore transaction,
	tenantID, userID, operation, idempotencyKey, requestDigest string,
	conversation domain.Conversation,
	runID string,
	runRevision uint64,
	now time.Time,
	eventType string,
) (interactionapp.ConversationCommandResult, error) {
	commandID, err := service.IDs.NewID("assistant-command")
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	result := interactionapp.ConversationCommandResult{
		CommandID: commandID, ConversationID: conversation.ID,
		ConversationRevision: conversation.Revision, AssistantRunID: runID,
		AssistantRunRevision: runRevision,
		ProjectionRef:        "/api/v1/views/assistant_conversation/" + conversation.ID,
		Status:               "accepted",
	}
	payload, _ := json.Marshal(result)
	if err := txStore.InsertOutboxEvent(ctx, contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       eventID, EventType: eventType, ProducerContext: "interaction",
		StreamID: "conversation/" + conversation.ID, Sequence: 1, TenantID: tenantID,
		AggregateType: "Conversation", AggregateID: conversation.ID, TraceID: coalesce(runID, conversation.ID),
		CorrelationID: commandID, CausationID: commandID,
		IdempotencyKey:  operation + "/" + idempotencyKey,
		ClientCommandID: idempotencyKey,
		Visibility:      "user", Payload: payload, OccurredAt: now,
	}); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	response, _ := json.Marshal(result)
	if err := txStore.InsertIdempotencyResult(
		ctx, tenantID, userID, operation, idempotencyKey,
		interactionapp.IdempotencyResult{
			RequestDigest: requestDigest, ResourceType: "Conversation",
			ResourceID: conversation.ID, Status: 202, Response: response,
		}, now,
	); err != nil {
		return interactionapp.ConversationCommandResult{}, err
	}
	return result, nil
}

func existingExactCommand(
	ctx context.Context,
	txStore transaction,
	tenantID, userID, operation, idempotencyKey, requestDigest string,
) (interactionapp.ConversationCommandResult, bool, error) {
	previous, found, err := txStore.FindIdempotencyResult(
		ctx, tenantID, userID, operation, idempotencyKey,
	)
	if err != nil || !found {
		return interactionapp.ConversationCommandResult{}, found, err
	}
	if previous.RequestDigest != requestDigest {
		return interactionapp.ConversationCommandResult{}, true, interactionapp.ErrIdempotencyConflict
	}
	var result interactionapp.ConversationCommandResult
	if err := json.Unmarshal(previous.Response, &result); err != nil {
		return interactionapp.ConversationCommandResult{}, true, err
	}
	return result, true, nil
}

func commandDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func loadCheckpointForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	checkpointID, conversationID string,
) (domain.ClarificationCheckpoint, error) {
	var checkpoint domain.ClarificationCheckpoint
	var assumptions, blockers, evidence, actions json.RawMessage
	var taskDraftID *string
	var taskDraftRevision *uint64
	err := tx.QueryRow(ctx, `
SELECT checkpoint_id,conversation_id,revision,task_draft_id,task_draft_revision,working_draft,
       readiness_assessment_id,assumptions,blockers,evidence_refs,allowed_actions,status,
       COALESCE(resolved_action,''),created_at,COALESCE(resolved_at,'epoch'::timestamptz)
FROM assistant_clarification_checkpoints
WHERE checkpoint_id=$1 AND conversation_id=$2 FOR UPDATE`,
		checkpointID, conversationID,
	).Scan(
		&checkpoint.ID, &checkpoint.ConversationID, &checkpoint.Revision, &taskDraftID,
		&taskDraftRevision, &checkpoint.WorkingDraft, &checkpoint.ReadinessAssessmentID,
		&assumptions, &blockers, &evidence, &actions, &checkpoint.Status,
		&checkpoint.ResolvedAction, &checkpoint.CreatedAt, &checkpoint.ResolvedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ClarificationCheckpoint{}, domain.ErrStaleRevision
	}
	if err != nil {
		return domain.ClarificationCheckpoint{}, err
	}
	if checkpoint.ResolvedAt.Equal(time.Unix(0, 0).UTC()) {
		checkpoint.ResolvedAt = time.Time{}
	}
	if taskDraftID != nil {
		checkpoint.TaskDraftID = *taskDraftID
		checkpoint.TaskDraftRevision = *taskDraftRevision
	}
	_ = json.Unmarshal(assumptions, &checkpoint.Assumptions)
	_ = json.Unmarshal(blockers, &checkpoint.Blockers)
	_ = json.Unmarshal(evidence, &checkpoint.EvidenceRefs)
	_ = json.Unmarshal(actions, &checkpoint.AllowedActions)
	return checkpoint, nil
}

func coalesce(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

var _ interactionapp.ExactCommandService = CommandService{}
