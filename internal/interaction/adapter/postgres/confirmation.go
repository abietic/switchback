package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
)

func (store Store) LoadConfirmationState(
	ctx context.Context,
	command interactionapp.ConfirmTaskDraftCommand,
) (interactionapp.ConfirmationState, error) {
	if store.Pool == nil {
		return interactionapp.ConfirmationState{}, fmt.Errorf("Confirmation state store is unavailable")
	}
	var state interactionapp.ConfirmationState
	var status string
	var workspaceID, workspaceRevisionID, resolutionID string
	var resolutionRevision, membershipRevision uint64
	err := store.Pool.QueryRow(ctx, `
SELECT conversation_id,tenant_id,actor_user_id,generation,status,revision,
       COALESCE(workspace_id,''),COALESCE(workspace_revision_id,''),
       COALESCE(workspace_resolution_id,''),COALESCE(workspace_resolution_revision,0),
       COALESCE(membership_revision,0),COALESCE(current_task_draft_id,''),
       COALESCE(current_readiness_assessment_id,''),COALESCE(current_checkpoint_id,''),
       COALESCE(active_assistant_run_id,''),next_turn_sequence,next_run_sequence,created_at,updated_at
FROM assistant_conversations
WHERE conversation_id=$1 AND tenant_id=$2 AND actor_user_id=$3`,
		command.ConversationID, command.Actor.TenantID, command.Actor.UserID,
	).Scan(
		&state.Conversation.ID, &state.Conversation.TenantID, &state.Conversation.ActorUserID,
		&state.Conversation.Generation, &status, &state.Conversation.Revision,
		&workspaceID, &workspaceRevisionID, &resolutionID, &resolutionRevision, &membershipRevision,
		&state.Conversation.CurrentTaskDraftID, &state.Conversation.CurrentReadinessAssessmentID,
		&state.Conversation.CurrentCheckpointID, &state.Conversation.ActiveAssistantRunID,
		&state.Conversation.NextTurnSequence, &state.Conversation.NextRunSequence,
		&state.Conversation.CreatedAt, &state.Conversation.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.ConfirmationState{}, interactionapp.ErrConfirmationStale
	}
	if err != nil {
		return interactionapp.ConfirmationState{}, fmt.Errorf("load confirmation Conversation: %w", err)
	}
	state.Conversation.Status = domain.ConversationStatus(status)
	if workspaceID != "" {
		state.Conversation.Workspace = &domain.WorkspaceResolution{
			WorkspaceID: workspaceID, WorkspaceRevisionID: workspaceRevisionID,
			ResolutionID: resolutionID, ResolutionRevision: resolutionRevision,
			MembershipRevision: membershipRevision,
		}
	}
	var parentID, draftStatus string
	var taskSpecJSON, assumptionsJSON json.RawMessage
	var invalidatedAt *time.Time
	err = store.Pool.QueryRow(ctx, `
SELECT task_draft_id,conversation_id,initiator_user_id,revision,COALESCE(parent_task_draft_id,''),
       readiness_assessment_id,workspace_id,workspace_revision_id,task_spec_snapshot,
       task_spec_digest,assumptions,status,created_at,invalidated_at
FROM assistant_task_drafts WHERE task_draft_id=$1 AND conversation_id=$2`,
		command.TaskDraftID, command.ConversationID,
	).Scan(
		&state.Draft.ID, &state.Draft.ConversationID, &state.Draft.InitiatorUserID,
		&state.Draft.Revision, &parentID, &state.Draft.ReadinessAssessmentID,
		&state.Draft.WorkspaceID, &state.Draft.WorkspaceRevisionID, &taskSpecJSON,
		&state.Draft.TaskSpecDigest, &assumptionsJSON, &draftStatus,
		&state.Draft.CreatedAt, &invalidatedAt,
	)
	if err != nil {
		return interactionapp.ConfirmationState{}, interactionapp.ErrConfirmationStale
	}
	state.Draft.ParentTaskDraftID = parentID
	state.Draft.Status = domain.TaskDraftStatus(draftStatus)
	if invalidatedAt != nil {
		state.Draft.InvalidatedAt = *invalidatedAt
	}
	if err := json.Unmarshal(taskSpecJSON, &state.TaskSpec); err != nil {
		return interactionapp.ConfirmationState{}, fmt.Errorf("decode confirmed TaskSpec: %w", err)
	}
	_ = json.Unmarshal(assumptionsJSON, &state.Draft.Assumptions)

	var dimensionsJSON, blockersJSON, readinessAssumptionsJSON json.RawMessage
	var readinessStatus, readinessPolicy string
	err = store.Pool.QueryRow(ctx, `
SELECT assessment_id,conversation_id,assistant_run_id,assessment_revision,policy_revision,
       dimensions,blocking_unknowns,assumptions,clarification_question_count,status,evaluated_at
FROM assistant_readiness_assessments
WHERE assessment_id=$1 AND conversation_id=$2`,
		command.ReadinessAssessmentID, command.ConversationID,
	).Scan(
		&state.Readiness.ID, &state.Readiness.ConversationID, &state.Readiness.AssistantRunID,
		&state.Readiness.Revision, &readinessPolicy, &dimensionsJSON, &blockersJSON,
		&readinessAssumptionsJSON, &state.Readiness.ClarificationQuestions,
		&readinessStatus, &state.Readiness.EvaluatedAt,
	)
	if err != nil {
		return interactionapp.ConfirmationState{}, interactionapp.ErrConfirmationStale
	}
	state.Readiness.PolicyRevision = readinessPolicy
	state.Readiness.Status = domain.ReadinessStatus(readinessStatus)
	_ = json.Unmarshal(blockersJSON, &state.Readiness.BlockingUnknowns)
	_ = json.Unmarshal(readinessAssumptionsJSON, &state.Readiness.Assumptions)
	var encodedDimensions map[string]struct {
		Status       string   `json:"status"`
		Summary      string   `json:"summary"`
		EvidenceRefs []string `json:"evidence_refs"`
	}
	if err := json.Unmarshal(dimensionsJSON, &encodedDimensions); err != nil {
		return interactionapp.ConfirmationState{}, fmt.Errorf("decode Assistant readiness dimensions: %w", err)
	}
	state.Readiness.Dimensions = make(map[domain.ReadinessDimension]domain.DimensionState, len(encodedDimensions))
	for dimension, value := range encodedDimensions {
		state.Readiness.Dimensions[domain.ReadinessDimension(dimension)] = domain.DimensionState{
			Status: domain.DimensionStatus(value.Status), Summary: value.Summary,
			EvidenceRefs: append([]string{}, value.EvidenceRefs...),
		}
	}
	return state, nil
}

var _ interactionapp.ConfirmationStateRepository = Store{}

func (store Store) FindConfirmationResult(
	ctx context.Context,
	command interactionapp.ConfirmTaskDraftCommand,
) (interactionapp.ConfirmTaskDraftResult, bool, error) {
	var response json.RawMessage
	var taskID, eventID, taskStatus, taskDigest string
	var taskRevision uint64
	var conversationID, taskDraftID, assistantReadinessID, resolutionID string
	var conversationRevision, taskDraftRevision, resolutionRevision, membershipRevision uint64
	err := store.Pool.QueryRow(ctx, `
SELECT result.response_body,task.task_id,task.revision,task.status,task.task_spec_digest,
       binding.conversation_id,binding.conversation_revision,binding.task_draft_id,
       binding.task_draft_revision,binding.assistant_readiness_assessment_id,
       binding.workspace_resolution_id,binding.workspace_resolution_revision,binding.membership_revision,
       task_event.event_id
FROM idempotency_results result
JOIN tasks task ON task.task_id=result.resource_id
JOIN assistant_creation_intent_bindings binding ON binding.intent_id=task.creation_intent_id
JOIN outbox_events task_event ON task_event.aggregate_type='Task'
  AND task_event.aggregate_id=task.task_id AND task_event.event_type='TaskCreated.v1'
WHERE result.tenant_id=$1 AND result.actor_user_id=$2
  AND result.operation='create_task' AND result.idempotency_key=$3`,
		command.Actor.TenantID, command.Actor.UserID, command.IdempotencyKey,
	).Scan(
		&response, &taskID, &taskRevision, &taskStatus, &taskDigest,
		&conversationID, &conversationRevision, &taskDraftID, &taskDraftRevision,
		&assistantReadinessID, &resolutionID, &resolutionRevision, &membershipRevision, &eventID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.ConfirmTaskDraftResult{}, false, nil
	}
	if err != nil {
		return interactionapp.ConfirmTaskDraftResult{}, false, fmt.Errorf("read confirmation idempotency result: %w", err)
	}
	if conversationID != command.ConversationID ||
		conversationRevision != command.ExpectedConversationRevision ||
		taskDraftID != command.TaskDraftID || taskDraftRevision != command.TaskDraftRevision ||
		assistantReadinessID != command.ReadinessAssessmentID ||
		resolutionID != command.WorkspaceResolutionID ||
		resolutionRevision != command.WorkspaceResolutionRevision ||
		membershipRevision != command.MembershipRevision || taskDigest != command.TaskSpecDigest {
		return interactionapp.ConfirmTaskDraftResult{}, false, interactionapp.ErrConfirmationStale
	}
	return interactionapp.ConfirmTaskDraftResult{
		CommandID: eventID, TaskID: taskID, TaskRevision: taskRevision, Status: taskStatus,
		TaskViewRef: "/api/v1/views/task_summary/" + taskID,
	}, true, nil
}
