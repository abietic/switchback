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
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

type Store struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (store *Store) RecordDirectAdmission(ctx context.Context, result application.DirectAdmissionResult) error {
	if store == nil || store.pool == nil {
		return fmt.Errorf("PostgreSQL Task store is required")
	}
	dimensions, err := json.Marshal(result.Assessment.Dimensions)
	if err != nil {
		return err
	}
	blockers, err := json.Marshal(result.Assessment.BlockingUnknowns)
	if err != nil {
		return err
	}
	assumptions, err := json.Marshal(result.Assessment.Assumptions)
	if err != nil {
		return err
	}
	return platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		_, err := tx.Exec(ctx, `
INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
   dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			result.Assessment.AssessmentID, result.Intent.TenantID, result.Intent.ActorUserID,
			result.Intent.WorkspaceID, result.Intent.WorkspaceRevisionID, result.Assessment.Source,
			result.Assessment.PolicyRevision, dimensions, blockers, assumptions, result.Assessment.Status,
			result.TaskSpec.ReadinessAssessmentRef.ContentDigest, result.Assessment.EvaluatedAt)
		if err != nil {
			return fmt.Errorf("insert readiness assessment: %w", err)
		}
		_, err = tx.Exec(ctx, `
INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
   task_spec_digest,trigger_policy_revision,status,created_at,expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,$10)`,
			result.Intent.IntentID, result.Intent.TenantID, result.Intent.ActorUserID,
			result.Intent.WorkspaceID, result.Intent.WorkspaceRevisionID,
			result.Intent.ReadinessAssessmentID, result.Intent.TaskSpecDigest,
			result.Intent.TriggerPolicyRevision, result.Intent.CreatedAt, result.Intent.ExpiresAt)
		if err != nil {
			return fmt.Errorf("insert creation intent: %w", err)
		}
		return nil
	})
}

func (store *Store) CreateTask(ctx context.Context, record application.CreateTaskRecord) (application.CreatedTask, error) {
	if store == nil || store.pool == nil {
		return application.CreatedTask{}, fmt.Errorf("PostgreSQL Task store is required")
	}
	var result application.CreatedTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		stored, found, err := readIdempotency(ctx, tx, record)
		if err != nil {
			return err
		}
		if found {
			result = stored
			return nil
		}
		if record.AssistantAdmission != nil {
			if err := recordAssistantAdmission(ctx, tx, record); err != nil {
				return err
			}
		}
		if err := validateAndLockIntent(ctx, tx, record); err != nil {
			return err
		}
		var target sessionTarget
		var allocationSequence, sessionQueueSequence, queueRevision uint64
		if record.WorkerSessionTargetIntentID != "" {
			target, err = validateAndLockSessionTarget(ctx, tx, record)
			if err != nil {
				return err
			}
			sessionQueueSequence, queueRevision, err = nextTargetSessionQueuePosition(ctx, tx, target.WorkerSessionID, record.Now)
			if err != nil {
				return err
			}
		} else {
			allocationSequence, err = nextAllocationSequence(ctx, tx, record.Actor, record.Now)
			if err != nil {
				return err
			}
		}
		taskSpecJSON, err := json.Marshal(record.TaskSpec)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO tasks
  (task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,
   task_spec_digest,creation_intent_id,requested_worker_session_id,worker_session_target_intent_id,
   session_context_policy,worker_session_id,status,acceptance_mode,result_refs,
   session_allocation_sequence,session_queue_sequence,revision,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'queued',$13,'[]'::jsonb,$14,$15,1,$16,$16)`,
			record.IDs.TaskID, record.Actor.TenantID, record.Actor.UserID, record.TaskSpec.WorkspaceID,
			record.TaskSpec.WorkspaceResolutionRef.WorkspaceRevisionID, taskSpecJSON,
			record.TaskSpecDigest, record.TaskSpec.CreationIntentRef.IntentID,
			nullIfEmpty(target.WorkerSessionID), nullIfEmpty(record.WorkerSessionTargetIntentID),
			contextPolicy(record.WorkerSessionTargetIntentID), nullIfEmpty(target.WorkerSessionID),
			record.AcceptanceMode, nullIfZero(allocationSequence), nullIfZero(sessionQueueSequence), record.Now); err != nil {
			return fmt.Errorf("insert Task: %w", err)
		}
		transitionTrigger := "direct_create"
		if record.AssistantAdmission != nil {
			transitionTrigger = "assistant_confirm"
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,0,1,NULL,'queued',$4,'{"status":"queued"}'::jsonb,$5)`,
			record.IDs.TransitionID, record.IDs.TaskID, record.IdempotencyKey, transitionTrigger, record.Now); err != nil {
			return fmt.Errorf("insert initial TaskTransition: %w", err)
		}
		if record.WorkerSessionTargetIntentID == "" {
			if _, err := tx.Exec(ctx, `
INSERT INTO session_allocation_requests
  (allocation_request_id,tenant_id,user_id,workspace_id,workspace_revision_id,request_kind,owner_id,
   owner_revision,context_policy,session_allocation_sequence,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,'task',$6,1,'fresh_session',$7,'pending',$8,$9,$9)`,
				record.IDs.AllocationRequestID, record.Actor.TenantID, record.Actor.UserID,
				record.TaskSpec.WorkspaceID, record.TaskSpec.WorkspaceResolutionRef.WorkspaceRevisionID,
				record.IDs.TaskID, allocationSequence, "allocate/"+record.IdempotencyKey, record.Now); err != nil {
				return fmt.Errorf("insert SessionAllocationRequest: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx, `
INSERT INTO session_task_queue_entries
  (queue_entry_id,worker_session_id,task_id,session_queue_sequence,queue_revision,
   expected_session_revision,status,idempotency_key,enqueued_at)
VALUES ($1,$2,$3,$4,$5,$6,'pending',$7,$8)`,
				record.IDs.QueueEntryID, target.WorkerSessionID, record.IDs.TaskID,
				sessionQueueSequence, queueRevision, target.SessionRevision,
				"target/"+record.IdempotencyKey, record.Now); err != nil {
				return fmt.Errorf("insert targeted SessionTaskQueue entry: %w", err)
			}
		}
		stream := platformpostgres.OwnerStream{
			StreamID: "task/" + record.IDs.TaskID, OwnerContext: "task-orchestration",
			AggregateType: "Task", AggregateID: record.IDs.TaskID,
		}
		sequence, err := store.streams.Next(ctx, tx, stream)
		if err != nil {
			return err
		}
		event, err := taskCreatedEvent(record, sequence, allocationSequence, target.WorkerSessionID, sessionQueueSequence)
		if err != nil {
			return err
		}
		if err := insertOutboxEvent(ctx, tx, event); err != nil {
			return err
		}
		commandTag, err := tx.Exec(ctx, `
UPDATE task_creation_intents
SET status='consumed', consumed_by_task_id=$2, consumed_at=$3
WHERE intent_id=$1 AND status='active'`, record.TaskSpec.CreationIntentRef.IntentID, record.IDs.TaskID, record.Now)
		if err != nil || commandTag.RowsAffected() != 1 {
			return application.ErrCreationIntentInvalid
		}
		if record.WorkerSessionTargetIntentID != "" {
			commandTag, err = tx.Exec(ctx, `
UPDATE worker_session_target_intents
SET status='consumed',consumed_by_task_id=$2,consumed_at=$3
WHERE intent_id=$1 AND status='active'`, record.WorkerSessionTargetIntentID, record.IDs.TaskID, record.Now)
			if err != nil || commandTag.RowsAffected() != 1 {
				return application.ErrSessionReuseInvalid
			}
		}
		result = application.CreatedTask{
			TaskID: record.IDs.TaskID, Revision: 1, Status: "queued",
			SessionAllocationSequence: allocationSequence,
			AllocationRequestID:       record.IDs.AllocationRequestID, EventID: record.IDs.EventID,
			WorkerSessionID: target.WorkerSessionID, SessionQueueSequence: sessionQueueSequence,
		}
		response, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO idempotency_results
  (tenant_id,actor_user_id,operation,idempotency_key,request_digest,resource_type,resource_id,
   response_status,response_body,created_at)
VALUES ($1,$2,'create_task',$3,$4,'Task',$5,202,$6,$7)`,
			record.Actor.TenantID, record.Actor.UserID, record.IdempotencyKey,
			record.RequestDigest, record.IDs.TaskID, response, record.Now); err != nil {
			return fmt.Errorf("insert CreateTask idempotency result: %w", err)
		}
		return nil
	})
	return result, err
}

func recordAssistantAdmission(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.CreateTaskRecord,
) error {
	admission := record.AssistantAdmission
	if admission == nil || admission.Intent.Validate() != nil ||
		admission.Intent.IntentID != record.TaskSpec.CreationIntentRef.IntentID ||
		admission.Intent.TaskSpecDigest != record.TaskSpecDigest ||
		admission.Intent.ReadinessAssessmentID != record.TaskSpec.ReadinessAssessmentRef.AssessmentID ||
		admission.Intent.ActorUserID != record.Actor.UserID ||
		admission.Intent.TenantID != record.Actor.TenantID ||
		admission.Intent.WorkspaceID != record.TaskSpec.WorkspaceID ||
		admission.Intent.WorkspaceRevisionID != record.TaskSpec.WorkspaceResolutionRef.WorkspaceRevisionID ||
		admission.Intent.WorkspaceResolutionID != record.TaskSpec.WorkspaceResolutionRef.ResolutionID ||
		admission.Intent.WorkspaceResolutionRevision != record.TaskSpec.WorkspaceResolutionRef.Revision ||
		admission.Intent.MembershipRevision != record.TaskSpec.WorkspaceResolutionRef.MembershipRevision ||
		admission.ConversationRevision != admission.Intent.ConversationRevision ||
		admission.TaskDraftRevision != admission.Intent.TaskDraftRevision ||
		admission.Readiness.AssessmentID != admission.Intent.ReadinessAssessmentID ||
		admission.Readiness.PolicyRevision != record.TaskSpec.ReadinessAssessmentRef.PolicyRevision ||
		len(admission.Readiness.BlockingUnknowns) != 0 ||
		(admission.Readiness.Status != domain.ReadinessReadyConfirmed &&
			admission.Readiness.Status != domain.ReadinessReadyWithAssumptions) {
		return application.ErrCreationIntentInvalid
	}
	var tenantID, actorUserID, status, workspaceID, workspaceRevisionID, resolutionID string
	var conversationRevision, resolutionRevision, membershipRevision uint64
	var currentDraftID, currentReadinessID string
	err := tx.QueryRow(ctx, `
SELECT tenant_id,actor_user_id,status,revision,COALESCE(workspace_id,''),
       COALESCE(workspace_revision_id,''),COALESCE(workspace_resolution_id,''),
       COALESCE(workspace_resolution_revision,0),COALESCE(membership_revision,0),
       COALESCE(current_task_draft_id,''),COALESCE(current_readiness_assessment_id,'')
FROM assistant_conversations
WHERE conversation_id=$1 FOR UPDATE`, admission.Intent.ConversationID).Scan(
		&tenantID, &actorUserID, &status, &conversationRevision, &workspaceID, &workspaceRevisionID,
		&resolutionID, &resolutionRevision, &membershipRevision, &currentDraftID, &currentReadinessID,
	)
	if err != nil || tenantID != record.Actor.TenantID || actorUserID != record.Actor.UserID ||
		status != "active" || conversationRevision != admission.ConversationRevision ||
		workspaceID != admission.Intent.WorkspaceID ||
		workspaceRevisionID != admission.Intent.WorkspaceRevisionID ||
		resolutionID != admission.Intent.WorkspaceResolutionID ||
		resolutionRevision != admission.Intent.WorkspaceResolutionRevision ||
		membershipRevision != admission.Intent.MembershipRevision ||
		currentDraftID != admission.Intent.TaskDraftID ||
		currentReadinessID != admission.Intent.ReadinessAssessmentID {
		return application.ErrCreationIntentInvalid
	}
	var draftRevision uint64
	var draftInitiator, draftReadinessID, draftWorkspaceID, draftWorkspaceRevisionID string
	var draftDigest, draftStatus string
	err = tx.QueryRow(ctx, `
SELECT revision,initiator_user_id,readiness_assessment_id,workspace_id,workspace_revision_id,
       task_spec_digest,status
FROM assistant_task_drafts
WHERE task_draft_id=$1 AND conversation_id=$2 FOR UPDATE`,
		admission.Intent.TaskDraftID, admission.Intent.ConversationID).Scan(
		&draftRevision, &draftInitiator, &draftReadinessID, &draftWorkspaceID,
		&draftWorkspaceRevisionID, &draftDigest, &draftStatus,
	)
	if err != nil || draftStatus != "current" || draftRevision != admission.TaskDraftRevision ||
		draftInitiator != record.Actor.UserID || draftReadinessID != admission.Intent.ReadinessAssessmentID ||
		draftWorkspaceID != admission.Intent.WorkspaceID ||
		draftWorkspaceRevisionID != admission.Intent.WorkspaceRevisionID ||
		draftDigest != record.TaskSpecDigest {
		return application.ErrCreationIntentInvalid
	}
	var assistantReadinessRevision uint64
	var assistantReadinessStatus, assistantReadinessDigest, assistantPolicy string
	var blockerCount int
	err = tx.QueryRow(ctx, `
SELECT assessment_revision,status,content_digest,policy_revision,jsonb_array_length(blocking_unknowns)
FROM assistant_readiness_assessments
WHERE assessment_id=$1 AND conversation_id=$2 FOR UPDATE`,
		admission.Intent.ReadinessAssessmentID, admission.Intent.ConversationID).Scan(
		&assistantReadinessRevision, &assistantReadinessStatus, &assistantReadinessDigest,
		&assistantPolicy, &blockerCount,
	)
	if err != nil || assistantReadinessRevision != admission.AssistantReadinessRevision ||
		blockerCount != 0 ||
		(assistantReadinessStatus != "ready_confirmed" && assistantReadinessStatus != "ready_with_assumptions") ||
		assistantReadinessDigest != record.TaskSpec.ReadinessAssessmentRef.ContentDigest ||
		assistantPolicy != record.TaskSpec.ReadinessAssessmentRef.PolicyRevision {
		return application.ErrCreationIntentInvalid
	}
	dimensions, err := json.Marshal(admission.Readiness.Dimensions)
	if err != nil {
		return err
	}
	blockers, _ := json.Marshal(admission.Readiness.BlockingUnknowns)
	assumptions, _ := json.Marshal(admission.Readiness.Assumptions)
	if _, err := tx.Exec(ctx, `
INSERT INTO task_readiness_assessments
  (assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,
   dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
VALUES ($1,$2,$3,$4,$5,'assistant',$6,$7,$8,$9,$10,$11,$12)`,
		admission.Readiness.AssessmentID, record.Actor.TenantID, record.Actor.UserID,
		admission.Intent.WorkspaceID, admission.Intent.WorkspaceRevisionID,
		admission.Readiness.PolicyRevision, dimensions, blockers, assumptions,
		admission.Readiness.Status, record.TaskSpec.ReadinessAssessmentRef.ContentDigest,
		admission.Readiness.EvaluatedAt,
	); err != nil {
		return application.ErrCreationIntentInvalid
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO task_creation_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
   task_spec_digest,trigger_policy_revision,status,created_at,expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,$10)`,
		admission.Intent.IntentID, admission.Intent.TenantID, admission.Intent.ActorUserID,
		admission.Intent.WorkspaceID, admission.Intent.WorkspaceRevisionID,
		admission.Intent.ReadinessAssessmentID, admission.Intent.TaskSpecDigest,
		admission.Intent.TriggerPolicyRevision, admission.Intent.CreatedAt, admission.Intent.ExpiresAt,
	); err != nil {
		return application.ErrCreationIntentInvalid
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO assistant_creation_intent_bindings
  (intent_id,conversation_id,conversation_revision,task_draft_id,task_draft_revision,
   assistant_readiness_assessment_id,task_readiness_assessment_id,workspace_resolution_id,
   workspace_resolution_revision,membership_revision,task_spec_digest,trigger_policy_revision,
   idempotency_key,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$10,$11,$12,$13)`,
		admission.Intent.IntentID, admission.Intent.ConversationID, admission.Intent.ConversationRevision,
		admission.Intent.TaskDraftID, admission.Intent.TaskDraftRevision,
		admission.Intent.ReadinessAssessmentID, admission.Intent.WorkspaceResolutionID,
		admission.Intent.WorkspaceResolutionRevision, admission.Intent.MembershipRevision,
		admission.Intent.TaskSpecDigest, admission.Intent.TriggerPolicyRevision,
		record.IdempotencyKey, admission.Intent.CreatedAt,
	); err != nil {
		return application.ErrCreationIntentInvalid
	}
	if tag, err := tx.Exec(ctx, `
UPDATE assistant_task_drafts
SET status='confirmed'
WHERE task_draft_id=$1 AND status='current' AND revision=$2`,
		admission.Intent.TaskDraftID, admission.Intent.TaskDraftRevision,
	); err != nil || tag.RowsAffected() != 1 {
		return application.ErrCreationIntentInvalid
	}
	return nil
}

func readIdempotency(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.CreateTaskRecord,
) (application.CreatedTask, bool, error) {
	var requestDigest string
	var response []byte
	err := tx.QueryRow(ctx, `
SELECT request_digest,response_body
FROM idempotency_results
WHERE tenant_id=$1 AND actor_user_id=$2 AND operation='create_task' AND idempotency_key=$3`,
		record.Actor.TenantID, record.Actor.UserID, record.IdempotencyKey).Scan(&requestDigest, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CreatedTask{}, false, nil
	}
	if err != nil {
		return application.CreatedTask{}, false, fmt.Errorf("read CreateTask idempotency result: %w", err)
	}
	if requestDigest != record.RequestDigest {
		return application.CreatedTask{}, false, application.ErrIdempotencyConflict
	}
	var result application.CreatedTask
	if err := json.Unmarshal(response, &result); err != nil {
		return application.CreatedTask{}, false, fmt.Errorf("decode CreateTask idempotency result: %w", err)
	}
	return result, true, nil
}

func validateAndLockIntent(ctx context.Context, tx platformpostgres.Tx, record application.CreateTaskRecord) error {
	var tenantID, actorUserID, workspaceID, workspaceRevisionID string
	var assessmentID, taskSpecDigest, triggerPolicyRevision, status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
SELECT tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,
       task_spec_digest,trigger_policy_revision,status,expires_at
FROM task_creation_intents WHERE intent_id=$1 FOR UPDATE`, record.TaskSpec.CreationIntentRef.IntentID).Scan(
		&tenantID, &actorUserID, &workspaceID, &workspaceRevisionID, &assessmentID,
		&taskSpecDigest, &triggerPolicyRevision, &status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrCreationIntentInvalid
	}
	if err != nil {
		return fmt.Errorf("lock creation intent: %w", err)
	}
	if status != "active" || !expiresAt.After(record.Now) || tenantID != record.Actor.TenantID ||
		actorUserID != record.Actor.UserID || workspaceID != record.TaskSpec.WorkspaceID ||
		workspaceRevisionID != record.TaskSpec.WorkspaceResolutionRef.WorkspaceRevisionID ||
		taskSpecDigest != record.TaskSpecDigest || triggerPolicyRevision != record.TaskSpec.CreationIntentRef.TriggerPolicyRevision ||
		assessmentID != record.TaskSpec.ReadinessAssessmentRef.AssessmentID {
		return application.ErrCreationIntentInvalid
	}
	var readinessStatus, contentDigest string
	var blockerCount int
	err = tx.QueryRow(ctx, `
SELECT status,content_digest,jsonb_array_length(blocking_unknowns)
FROM task_readiness_assessments WHERE assessment_id=$1`, assessmentID).Scan(
		&readinessStatus, &contentDigest, &blockerCount)
	if err != nil || blockerCount != 0 ||
		(readinessStatus != "ready_confirmed" && readinessStatus != "ready_with_assumptions") ||
		contentDigest != record.TaskSpec.ReadinessAssessmentRef.ContentDigest {
		return application.ErrCreationIntentInvalid
	}
	return nil
}

func nextAllocationSequence(ctx context.Context, tx platformpostgres.Tx, actor application.Actor, now time.Time) (uint64, error) {
	_, err := tx.Exec(ctx, `
INSERT INTO session_allocation_queue_positions (tenant_id,user_id,next_sequence,revision,updated_at)
VALUES ($1,$2,1,1,$3) ON CONFLICT DO NOTHING`, actor.TenantID, actor.UserID, now)
	if err != nil {
		return 0, fmt.Errorf("ensure SessionAllocationQueue position: %w", err)
	}
	var sequence uint64
	err = tx.QueryRow(ctx, `
UPDATE session_allocation_queue_positions
SET next_sequence=next_sequence+1,revision=revision+1,updated_at=$3
WHERE tenant_id=$1 AND user_id=$2
RETURNING next_sequence-1`, actor.TenantID, actor.UserID, now).Scan(&sequence)
	if err != nil {
		return 0, fmt.Errorf("allocate SessionAllocationQueue sequence: %w", err)
	}
	return sequence, nil
}

type sessionTarget struct {
	WorkerSessionID string
	WorkingCopyID   string
	SessionRevision uint64
}

func validateAndLockSessionTarget(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.CreateTaskRecord,
) (sessionTarget, error) {
	var target sessionTarget
	var tenantID, actorUserID, workspaceID, taskSpecDigest, busyPolicy, status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `
SELECT tenant_id,actor_user_id,workspace_id,worker_session_id,worker_session_revision,
  working_copy_id,task_spec_digest,busy_policy,status,expires_at
FROM worker_session_target_intents WHERE intent_id=$1 FOR UPDATE`,
		record.WorkerSessionTargetIntentID).Scan(
		&tenantID, &actorUserID, &workspaceID, &target.WorkerSessionID,
		&target.SessionRevision, &target.WorkingCopyID, &taskSpecDigest,
		&busyPolicy, &status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionTarget{}, application.ErrSessionReuseInvalid
	}
	if err != nil {
		return sessionTarget{}, fmt.Errorf("lock WorkerSession target intent: %w", err)
	}
	if status != "active" || !expiresAt.After(record.Now) || tenantID != record.Actor.TenantID ||
		actorUserID != record.Actor.UserID || workspaceID != record.TaskSpec.WorkspaceID ||
		taskSpecDigest != record.TaskSpecDigest || busyPolicy != "queue" {
		return sessionTarget{}, application.ErrSessionReuseInvalid
	}
	var sessionTenant, sessionUser, sessionWorkspace, workingCopyID, sessionStatus string
	var sessionRevision uint64
	err = tx.QueryRow(ctx, `
SELECT tenant_id,user_id,workspace_id,working_copy_id,status,session_revision
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, target.WorkerSessionID).Scan(
		&sessionTenant, &sessionUser, &sessionWorkspace, &workingCopyID, &sessionStatus, &sessionRevision)
	if err != nil {
		return sessionTarget{}, application.ErrSessionReuseInvalid
	}
	if sessionTenant != tenantID || sessionUser != actorUserID || sessionWorkspace != workspaceID ||
		workingCopyID != target.WorkingCopyID || sessionRevision != target.SessionRevision ||
		sessionStatus == "closed" || sessionStatus == "expired" || sessionStatus == "lost" {
		return sessionTarget{}, application.ErrSessionReuseInvalid
	}
	return target, nil
}

func nextTargetSessionQueuePosition(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	now time.Time,
) (uint64, uint64, error) {
	if _, err := tx.Exec(ctx, `
INSERT INTO session_task_queue_positions (worker_session_id,next_sequence,revision,updated_at)
VALUES ($1,1,1,$2) ON CONFLICT DO NOTHING`, sessionID, now); err != nil {
		return 0, 0, fmt.Errorf("ensure target SessionTaskQueue position: %w", err)
	}
	var sequence, revision uint64
	err := tx.QueryRow(ctx, `
UPDATE session_task_queue_positions
SET next_sequence=next_sequence+1,revision=revision+1,updated_at=$2
WHERE worker_session_id=$1
RETURNING next_sequence-1,revision`, sessionID, now).Scan(&sequence, &revision)
	if err != nil {
		return 0, 0, fmt.Errorf("allocate target SessionTaskQueue sequence: %w", err)
	}
	return sequence, revision, nil
}

func contextPolicy(targetIntentID string) string {
	if targetIntentID == "" {
		return "fresh_session"
	}
	return "inherit_current"
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullIfZero(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func taskCreatedEvent(
	record application.CreateTaskRecord,
	sequence uint64,
	allocationSequence uint64,
	workerSessionID string,
	sessionQueueSequence uint64,
) (contractsv1.IntegrationEvent, error) {
	payload, err := json.Marshal(struct {
		TaskID                    string `json:"task_id"`
		Status                    string `json:"status"`
		AllocationRequestID       string `json:"allocation_request_id,omitempty"`
		SessionAllocationSequence uint64 `json:"session_allocation_sequence,omitempty"`
		WorkerSessionID           string `json:"worker_session_id,omitempty"`
		SessionQueueSequence      uint64 `json:"session_queue_sequence,omitempty"`
	}{record.IDs.TaskID, "queued", record.IDs.AllocationRequestID, allocationSequence, workerSessionID, sessionQueueSequence})
	if err != nil {
		return contractsv1.IntegrationEvent{}, err
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.IDs.EventID, EventType: "TaskCreated.v1",
		ProducerContext: "task-orchestration", StreamID: "task/" + record.IDs.TaskID,
		Sequence: sequence, TenantID: record.Actor.TenantID, AggregateType: "Task",
		AggregateID: record.IDs.TaskID, TraceID: "task/" + record.IDs.TaskID,
		CorrelationID:   record.IDs.EventID,
		CausationID:     record.TaskSpec.CreationIntentRef.IntentID,
		ClientCommandID: record.IdempotencyKey,
		IdempotencyKey:  "task-created/" + record.IDs.TaskID,
		Visibility:      "user", OccurredAt: record.Now, Payload: payload,
	}
	return event, event.Validate()
}

func insertOutboxEvent(ctx context.Context, tx platformpostgres.Tx, event contractsv1.IntegrationEvent) error {
	_, err := tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,client_command_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
		event.StreamID, event.Sequence, event.TenantID, event.AggregateType, event.AggregateID,
		event.TraceID, event.CorrelationID, event.CausationID, nullIfEmpty(event.ClientCommandID),
		event.IdempotencyKey,
		event.Visibility, event.Payload, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("insert Outbox event: %w", err)
	}
	return nil
}
