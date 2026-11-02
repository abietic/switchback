// Package postgres persists the Interaction bounded context.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
)

type Store struct {
	Pool           *pgxpool.Pool
	MaxRecentTurns int
}

func (store Store) WithinTransaction(ctx context.Context, operation func(interactionapp.Transaction) error) error {
	if store.Pool == nil || operation == nil {
		return fmt.Errorf("Interaction PostgreSQL transaction dependencies are required")
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin Interaction transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := operation(transaction{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Interaction transaction: %w", err)
	}
	return nil
}

type transaction struct {
	tx pgx.Tx
}

func (store transaction) LoadConversation(
	ctx context.Context,
	tenantID, actorUserID, conversationID string,
	forUpdate bool,
) (domain.Conversation, error) {
	query := `
SELECT conversation_id,tenant_id,actor_user_id,generation,status,
       COALESCE(workspace_id,''),COALESCE(workspace_revision_id,''),
       COALESCE(workspace_resolution_id,''),COALESCE(workspace_resolution_revision,0),
       COALESCE(membership_revision,0),next_turn_sequence,next_run_sequence,revision,
       COALESCE(current_task_draft_id,''),COALESCE(current_readiness_assessment_id,''),
       COALESCE(current_checkpoint_id,''),COALESCE(active_assistant_run_id,''),
       created_at,updated_at,COALESCE(terminal_at,'epoch'::timestamptz),COALESCE(terminal_reason,'')
FROM assistant_conversations
WHERE tenant_id=$1 AND actor_user_id=$2 AND conversation_id=$3`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var conversation domain.Conversation
	var status string
	var workspaceID, workspaceRevisionID, resolutionID string
	var resolutionRevision, membershipRevision uint64
	err := store.tx.QueryRow(ctx, query, tenantID, actorUserID, conversationID).Scan(
		&conversation.ID, &conversation.TenantID, &conversation.ActorUserID, &conversation.Generation, &status,
		&workspaceID, &workspaceRevisionID, &resolutionID, &resolutionRevision, &membershipRevision,
		&conversation.NextTurnSequence, &conversation.NextRunSequence, &conversation.Revision,
		&conversation.CurrentTaskDraftID, &conversation.CurrentReadinessAssessmentID,
		&conversation.CurrentCheckpointID, &conversation.ActiveAssistantRunID,
		&conversation.CreatedAt, &conversation.UpdatedAt, &conversation.TerminalAt, &conversation.TerminalReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Conversation{}, interactionapp.ErrConversationUnavailable
	}
	if err != nil {
		return domain.Conversation{}, fmt.Errorf("load Conversation: %w", err)
	}
	conversation.Status = domain.ConversationStatus(status)
	if workspaceID != "" {
		conversation.Workspace = &domain.WorkspaceResolution{
			WorkspaceID: workspaceID, WorkspaceRevisionID: workspaceRevisionID,
			ResolutionID: resolutionID, ResolutionRevision: resolutionRevision,
			MembershipRevision: membershipRevision,
		}
	}
	if conversation.TerminalAt.Equal(time.Unix(0, 0).UTC()) {
		conversation.TerminalAt = time.Time{}
	}
	return conversation, nil
}

func (store transaction) InsertConversation(ctx context.Context, conversation domain.Conversation) error {
	var workspaceID, workspaceRevisionID, resolutionID any
	var resolutionRevision, membershipRevision any
	if conversation.Workspace != nil {
		workspaceID = conversation.Workspace.WorkspaceID
		workspaceRevisionID = conversation.Workspace.WorkspaceRevisionID
		resolutionID = conversation.Workspace.ResolutionID
		resolutionRevision = conversation.Workspace.ResolutionRevision
		membershipRevision = conversation.Workspace.MembershipRevision
	}
	_, err := store.tx.Exec(ctx, `
INSERT INTO assistant_conversations
  (conversation_id,tenant_id,actor_user_id,generation,status,workspace_id,workspace_revision_id,
   workspace_resolution_id,workspace_resolution_revision,membership_revision,next_turn_sequence,
   next_run_sequence,revision,current_task_draft_id,current_readiness_assessment_id,current_checkpoint_id,
   active_assistant_run_id,created_at,updated_at,terminal_at,terminal_reason)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),NULLIF($15,''),
        NULLIF($16,''),NULLIF($17,''),$18,$19,NULLIF($20,'epoch'::timestamptz),NULLIF($21,''))`,
		conversation.ID, conversation.TenantID, conversation.ActorUserID, conversation.Generation,
		conversation.Status, workspaceID, workspaceRevisionID, resolutionID, resolutionRevision, membershipRevision,
		conversation.NextTurnSequence, conversation.NextRunSequence, conversation.Revision,
		conversation.CurrentTaskDraftID, conversation.CurrentReadinessAssessmentID, conversation.CurrentCheckpointID,
		conversation.ActiveAssistantRunID, conversation.CreatedAt, conversation.UpdatedAt,
		nullableTime(conversation.TerminalAt), conversation.TerminalReason,
	)
	if err != nil {
		return fmt.Errorf("insert Conversation: %w", err)
	}
	return nil
}

func (store transaction) UpdateConversation(
	ctx context.Context,
	conversation domain.Conversation,
	expectedRevision uint64,
) error {
	var workspaceID, workspaceRevisionID, resolutionID any
	var resolutionRevision, membershipRevision any
	if conversation.Workspace != nil {
		workspaceID = conversation.Workspace.WorkspaceID
		workspaceRevisionID = conversation.Workspace.WorkspaceRevisionID
		resolutionID = conversation.Workspace.ResolutionID
		resolutionRevision = conversation.Workspace.ResolutionRevision
		membershipRevision = conversation.Workspace.MembershipRevision
	}
	tag, err := store.tx.Exec(ctx, `
UPDATE assistant_conversations
SET status=$1,workspace_id=$2,workspace_revision_id=$3,workspace_resolution_id=$4,
    workspace_resolution_revision=$5,membership_revision=$6,next_turn_sequence=$7,next_run_sequence=$8,
    revision=$9,current_task_draft_id=NULLIF($10,''),current_readiness_assessment_id=NULLIF($11,''),
    current_checkpoint_id=NULLIF($12,''),active_assistant_run_id=NULLIF($13,''),updated_at=$14,
    terminal_at=NULLIF($15,'epoch'::timestamptz),terminal_reason=NULLIF($16,'')
WHERE conversation_id=$17 AND tenant_id=$18 AND actor_user_id=$19 AND revision=$20`,
		conversation.Status, workspaceID, workspaceRevisionID, resolutionID, resolutionRevision, membershipRevision,
		conversation.NextTurnSequence, conversation.NextRunSequence, conversation.Revision,
		conversation.CurrentTaskDraftID, conversation.CurrentReadinessAssessmentID, conversation.CurrentCheckpointID,
		conversation.ActiveAssistantRunID, conversation.UpdatedAt, nullableTime(conversation.TerminalAt),
		conversation.TerminalReason, conversation.ID, conversation.TenantID, conversation.ActorUserID,
		expectedRevision,
	)
	if err != nil {
		return fmt.Errorf("update Conversation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrStaleRevision
	}
	return nil
}

func (store transaction) InsertTurn(ctx context.Context, turn domain.Turn) error {
	_, err := store.tx.Exec(ctx, `
INSERT INTO assistant_conversation_turns
  (turn_id,conversation_id,turn_sequence,role,content,visible,source_assistant_run_id,correlation_id,created_at)
VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9)`,
		turn.ID, turn.ConversationID, turn.Sequence, turn.Role, turn.Content, turn.Visible,
		turn.SourceAssistantRunID, turn.CorrelationID, turn.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert Conversation Turn: %w", err)
	}
	return nil
}

func (store transaction) InsertRun(
	ctx context.Context,
	run domain.AssistantRun,
	profileRevision, profileDigest string,
) error {
	_, err := store.tx.Exec(ctx, `
INSERT INTO assistant_runs
  (assistant_run_id,conversation_id,source_turn_id,run_sequence,status,model_profile,
   model_profile_revision,model_profile_digest,policy_revision,context_budget_tokens,timeout_at,
   lease_owner,lease_expires_at,fencing_token,attempt_count,result_decision,error_code,retryable,
   created_at,started_at,completed_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),NULLIF($13,'epoch'::timestamptz),
        $14,$15,NULLIF($16::jsonb,'null'::jsonb),NULLIF($17,''),$18,$19,
        NULLIF($20,'epoch'::timestamptz),NULLIF($21,'epoch'::timestamptz),$22)`,
		run.ID, run.ConversationID, run.SourceTurnID, run.Sequence, run.Status, run.ModelProfile,
		profileRevision, profileDigest, run.PolicyRevision, run.ContextBudgetTokens, run.TimeoutAt,
		run.LeaseOwner, nullableTime(run.LeaseExpiresAt), run.FencingToken, run.AttemptCount,
		nullableJSON(run.Decision), run.ErrorCode, run.Retryable, run.CreatedAt,
		nullableTime(run.StartedAt), nullableTime(run.CompletedAt), run.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert AssistantRun: %w", err)
	}
	return nil
}

func (store transaction) InsertOutboxEvent(ctx context.Context, event contractsv1.IntegrationEvent) error {
	validationEvent := event
	if validationEvent.Sequence == 0 {
		validationEvent.Sequence = 1
	}
	if err := validationEvent.Validate(); err != nil {
		return err
	}
	if _, err := store.tx.Exec(ctx, `
INSERT INTO owner_stream_positions
  (stream_id,owner_context,aggregate_type,aggregate_id,next_sequence,revision,updated_at)
VALUES ($1,'interaction',$2,$3,1,1,$4)
ON CONFLICT (stream_id) DO NOTHING`,
		event.StreamID, event.AggregateType, event.AggregateID, event.OccurredAt,
	); err != nil {
		return fmt.Errorf("ensure Assistant owner stream: %w", err)
	}
	var sequence uint64
	err := store.tx.QueryRow(ctx, `
UPDATE owner_stream_positions
SET next_sequence=next_sequence+1,revision=revision+1,updated_at=$2
WHERE stream_id=$1
RETURNING next_sequence-1`, event.StreamID, event.OccurredAt).Scan(&sequence)
	if err != nil {
		return fmt.Errorf("allocate Assistant owner sequence: %w", err)
	}
	event.Sequence = sequence
	_, err = store.tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,client_command_id,
   visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),$15,$16,$17)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext, event.StreamID,
		event.Sequence, event.TenantID, event.AggregateType, event.AggregateID, event.TraceID,
		event.CorrelationID, event.CausationID, event.IdempotencyKey, event.ClientCommandID, event.Visibility,
		event.Payload, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("insert Assistant Outbox event: %w", err)
	}
	return nil
}

func (store transaction) FindIdempotencyResult(
	ctx context.Context,
	tenantID, actorUserID, operation, idempotencyKey string,
) (interactionapp.IdempotencyResult, bool, error) {
	var result interactionapp.IdempotencyResult
	err := store.tx.QueryRow(ctx, `
SELECT request_digest,resource_type,resource_id,response_status,response_body
FROM idempotency_results
WHERE tenant_id=$1 AND actor_user_id=$2 AND operation=$3 AND idempotency_key=$4`,
		tenantID, actorUserID, operation, idempotencyKey,
	).Scan(&result.RequestDigest, &result.ResourceType, &result.ResourceID, &result.Status, &result.Response)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.IdempotencyResult{}, false, nil
	}
	if err != nil {
		return interactionapp.IdempotencyResult{}, false, fmt.Errorf("load Interaction idempotency result: %w", err)
	}
	return result, true, nil
}

func (store transaction) InsertIdempotencyResult(
	ctx context.Context,
	tenantID, actorUserID, operation, idempotencyKey string,
	result interactionapp.IdempotencyResult,
	now time.Time,
) error {
	_, err := store.tx.Exec(ctx, `
INSERT INTO idempotency_results
  (tenant_id,actor_user_id,operation,idempotency_key,request_digest,resource_type,resource_id,
   response_status,response_body,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		tenantID, actorUserID, operation, idempotencyKey, result.RequestDigest,
		result.ResourceType, result.ResourceID, result.Status, result.Response, now,
	)
	if err != nil {
		return fmt.Errorf("insert Interaction idempotency result: %w", err)
	}
	return nil
}

func nullableTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return value.UTC()
}

func nullableJSON(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(value)
}
