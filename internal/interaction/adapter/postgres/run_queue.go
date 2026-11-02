package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
)

func (store Store) ClaimNextRun(
	ctx context.Context,
	leaseOwner string,
	leaseDuration time.Duration,
	now time.Time,
) (interactionapp.RunClaim, error) {
	if store.Pool == nil || leaseOwner == "" || leaseDuration <= 0 || now.IsZero() {
		return interactionapp.RunClaim{}, fmt.Errorf("AssistantRun claim inputs are invalid")
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return interactionapp.RunClaim{}, fmt.Errorf("begin AssistantRun claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var runID string
	err = tx.QueryRow(ctx, `
SELECT run.assistant_run_id
FROM assistant_runs run
JOIN assistant_conversations conversation ON conversation.conversation_id=run.conversation_id
WHERE run.status='queued' AND run.timeout_at>$1 AND conversation.status='active'
  AND conversation.active_assistant_run_id IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM assistant_runs earlier
    WHERE earlier.conversation_id=run.conversation_id
      AND earlier.run_sequence<run.run_sequence
      AND earlier.status IN ('queued','claimed'))
ORDER BY run.created_at,run.assistant_run_id
FOR UPDATE OF run SKIP LOCKED
LIMIT 1`, now).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return interactionapp.RunClaim{}, interactionapp.ErrNoQueuedRun
	}
	if err != nil {
		return interactionapp.RunClaim{}, fmt.Errorf("select next AssistantRun: %w", err)
	}

	run, profileRevision, profileDigest, err := loadRun(ctx, tx, runID, true)
	if err != nil {
		return interactionapp.RunClaim{}, err
	}
	txStore := transaction{tx: tx}
	conversation, err := txStore.LoadConversation(ctx, runConversationTenant(ctx, tx, run.ConversationID),
		runConversationActor(ctx, tx, run.ConversationID), run.ConversationID, true)
	if err != nil {
		return interactionapp.RunClaim{}, err
	}
	expectedConversationRevision := conversation.Revision
	if err := conversation.ClaimRun(expectedConversationRevision, run.ID, now); err != nil {
		return interactionapp.RunClaim{}, err
	}
	expectedRunRevision := run.Revision
	if err := run.Claim(expectedRunRevision, leaseOwner, now.Add(leaseDuration), now); err != nil {
		return interactionapp.RunClaim{}, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE assistant_runs
SET status=$1,lease_owner=$2,lease_expires_at=$3,fencing_token=$4,attempt_count=$5,
    started_at=$6,updated_at=$7,revision=$8
WHERE assistant_run_id=$9 AND revision=$10 AND status='queued'`,
		run.Status, run.LeaseOwner, run.LeaseExpiresAt, run.FencingToken, run.AttemptCount,
		run.StartedAt, run.UpdatedAt, run.Revision, run.ID, expectedRunRevision,
	)
	if err != nil {
		return interactionapp.RunClaim{}, fmt.Errorf("claim AssistantRun: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.RunClaim{}, interactionapp.ErrLeaseLost
	}
	if err := txStore.UpdateConversation(ctx, conversation, expectedConversationRevision); err != nil {
		return interactionapp.RunClaim{}, err
	}
	payload, err := json.Marshal(map[string]string{
		"assistant_run_id": run.ID,
		"status":           string(run.Status),
	})
	if err != nil {
		return interactionapp.RunClaim{}, err
	}
	claimedEventID := "assistant-run-claimed-" + run.ID + "-" + strconv.FormatUint(run.FencingToken, 10)
	if err := txStore.InsertOutboxEvent(ctx, contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       claimedEventID, EventType: "AssistantRunClaimed.v1",
		ProducerContext: "interaction", StreamID: "conversation/" + conversation.ID, Sequence: 1,
		TenantID: conversation.TenantID, AggregateType: "Conversation", AggregateID: conversation.ID,
		TraceID: run.ID, CorrelationID: run.ID, CausationID: run.SourceTurnID,
		IdempotencyKey: claimedEventID, Visibility: "user", Payload: payload, OccurredAt: now,
	}); err != nil {
		return interactionapp.RunClaim{}, err
	}
	turns, err := loadConversationTurns(ctx, tx, run.ConversationID)
	if err != nil {
		return interactionapp.RunClaim{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return interactionapp.RunClaim{}, fmt.Errorf("commit AssistantRun claim: %w", err)
	}
	return interactionapp.RunClaim{
		Conversation: conversation, Run: run, Turns: turns,
		ModelProfileRevision: profileRevision, ModelProfileDigest: profileDigest,
	}, nil
}

func (store Store) LoadRun(ctx context.Context, runID string) (domain.AssistantRun, error) {
	if store.Pool == nil {
		return domain.AssistantRun{}, fmt.Errorf("AssistantRun store is unavailable")
	}
	run, _, _, err := loadRun(ctx, store.Pool, runID, false)
	return run, err
}

func (store Store) MarkInvocationStarted(
	ctx context.Context,
	runID string,
	expectedRevision, fencingToken uint64,
	now time.Time,
) (uint64, error) {
	var revision uint64
	err := store.Pool.QueryRow(ctx, `
UPDATE assistant_runs
SET invocation_state='request_started',invocation_started_at=$1,revision=revision+1,updated_at=$1
WHERE assistant_run_id=$2 AND revision=$3 AND fencing_token=$4 AND status='claimed'
  AND invocation_state='not_started' AND lease_expires_at>$1 AND timeout_at>$1
RETURNING revision`, now, runID, expectedRevision, fencingToken).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, interactionapp.ErrLeaseLost
	}
	if err != nil {
		return 0, fmt.Errorf("mark Assistant model invocation started: %w", err)
	}
	return revision, nil
}

func (store Store) RequeueExpired(
	ctx context.Context,
	runID string,
	expectedRevision, fencingToken uint64,
	now time.Time,
) error {
	tag, err := store.Pool.Exec(ctx, `
UPDATE assistant_runs
SET status='queued',lease_owner=NULL,lease_expires_at=NULL,revision=revision+1,updated_at=$1
WHERE assistant_run_id=$2 AND revision=$3 AND fencing_token=$4 AND status='claimed'
  AND invocation_state='not_started' AND lease_expires_at<=$1`, now, runID, expectedRevision, fencingToken)
	if err != nil {
		return fmt.Errorf("requeue expired AssistantRun: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.ErrLeaseLost
	}
	return nil
}

func (store Store) RecoverExpiredClaims(ctx context.Context, now time.Time) (interactionapp.RunRecoveryResult, error) {
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return interactionapp.RunRecoveryResult{}, fmt.Errorf("begin AssistantRun recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
SELECT run.assistant_run_id,run.conversation_id,run.invocation_state,run.fencing_token,
       run.policy_revision,conversation.tenant_id,conversation.actor_user_id,
       conversation.generation,COALESCE(conversation.workspace_id,'')
FROM assistant_runs run
JOIN assistant_conversations conversation ON conversation.conversation_id=run.conversation_id
WHERE run.status='claimed' AND run.lease_expires_at<=$1
ORDER BY run.lease_expires_at,run.assistant_run_id
FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return interactionapp.RunRecoveryResult{}, fmt.Errorf("list expired AssistantRun claims: %w", err)
	}
	type expired struct {
		runID, conversationID, invocationState, policyRevision string
		tenantID, userID, workspaceID                          string
		fencingToken, conversationGeneration                   uint64
	}
	expiredRuns := make([]expired, 0)
	for rows.Next() {
		var item expired
		if err := rows.Scan(
			&item.runID, &item.conversationID, &item.invocationState, &item.fencingToken,
			&item.policyRevision, &item.tenantID, &item.userID,
			&item.conversationGeneration, &item.workspaceID,
		); err != nil {
			rows.Close()
			return interactionapp.RunRecoveryResult{}, err
		}
		expiredRuns = append(expiredRuns, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return interactionapp.RunRecoveryResult{}, err
	}
	result := interactionapp.RunRecoveryResult{Count: len(expiredRuns)}
	txStore := transaction{tx: tx}
	for _, item := range expiredRuns {
		eventType := "AssistantRunRequeued.v1"
		eventIDPrefix := "assistant-run-requeued-"
		eventStatus := "queued"
		errorCode := ""
		if item.invocationState == "not_started" {
			_, err = tx.Exec(ctx, `
UPDATE assistant_runs
SET status='queued',lease_owner=NULL,lease_expires_at=NULL,revision=revision+1,updated_at=$1
WHERE assistant_run_id=$2`, now, item.runID)
		} else {
			eventType = "AssistantRunRecoveryFailed.v1"
			eventIDPrefix = "assistant-run-recovery-failed-"
			eventStatus = "failed"
			errorCode = "model_effect_unknown"
			_, err = tx.Exec(ctx, `
UPDATE assistant_runs
SET status='failed',lease_owner=NULL,lease_expires_at=NULL,error_code='model_effect_unknown',
    retryable=false,completed_at=$1,revision=revision+1,updated_at=$1
WHERE assistant_run_id=$2`, now, item.runID)
		}
		if err != nil {
			return interactionapp.RunRecoveryResult{}, fmt.Errorf("recover AssistantRun %s: %w", item.runID, err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE assistant_conversations
SET active_assistant_run_id=NULL,revision=revision+1,updated_at=$1
WHERE conversation_id=$2 AND active_assistant_run_id=$3`, now, item.conversationID, item.runID); err != nil {
			return interactionapp.RunRecoveryResult{}, fmt.Errorf("release recovered Conversation: %w", err)
		}
		payload, marshalErr := json.Marshal(map[string]string{
			"assistant_run_id": item.runID, "status": eventStatus, "error_code": errorCode,
		})
		if marshalErr != nil {
			return interactionapp.RunRecoveryResult{}, marshalErr
		}
		if err := txStore.InsertOutboxEvent(ctx, contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
			EventID:       eventIDPrefix + item.runID + "-" + strconv.FormatUint(item.fencingToken, 10),
			EventType:     eventType, ProducerContext: "interaction",
			StreamID: "conversation/" + item.conversationID, Sequence: 1,
			TenantID: item.tenantID, AggregateType: "Conversation", AggregateID: item.conversationID,
			TraceID: item.runID, CorrelationID: item.runID, CausationID: item.runID,
			IdempotencyKey: eventIDPrefix + item.runID + "-" + strconv.FormatUint(item.fencingToken, 10),
			Visibility:     "user", Payload: payload, OccurredAt: now,
		}); err != nil {
			return interactionapp.RunRecoveryResult{}, err
		}
	}
	pendingRows, err := tx.Query(ctx, `
SELECT run.assistant_run_id,run.policy_revision,conversation.tenant_id,
       conversation.actor_user_id,conversation.generation,
       COALESCE(conversation.workspace_id,''),conversation.conversation_id
FROM assistant_runs run
JOIN assistant_conversations conversation ON conversation.conversation_id=run.conversation_id
WHERE run.status='failed' AND run.error_code='model_effect_unknown'
  AND run.evidence_status='pending'
ORDER BY run.completed_at,run.assistant_run_id
LIMIT 64`)
	if err != nil {
		return interactionapp.RunRecoveryResult{}, fmt.Errorf("list pending Assistant recovery effects: %w", err)
	}
	for pendingRows.Next() {
		var failure interactionapp.RecoveredRunFailure
		failure.ErrorCode = "model_effect_unknown"
		if err := pendingRows.Scan(
			&failure.AssistantRunID, &failure.PolicyRevision, &failure.TenantID,
			&failure.UserID, &failure.ConversationGeneration, &failure.WorkspaceID,
			&failure.ConversationID,
		); err != nil {
			pendingRows.Close()
			return interactionapp.RunRecoveryResult{}, err
		}
		result.Failures = append(result.Failures, failure)
	}
	pendingRows.Close()
	if err := pendingRows.Err(); err != nil {
		return interactionapp.RunRecoveryResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return interactionapp.RunRecoveryResult{}, fmt.Errorf("commit AssistantRun recovery: %w", err)
	}
	return result, nil
}

func (store Store) CommitDecision(ctx context.Context, commit interactionapp.RunDecisionCommit) error {
	if store.Pool == nil {
		return fmt.Errorf("AssistantRun store is unavailable")
	}
	if err := validateDecisionCommit(commit); err != nil {
		return err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin Assistant decision commit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID, actorUserID, invocationState string
	if err := tx.QueryRow(ctx, `
SELECT conversation.tenant_id,conversation.actor_user_id,run.invocation_state
FROM assistant_conversations conversation
JOIN assistant_runs run ON run.conversation_id=conversation.conversation_id
WHERE conversation.conversation_id=$1 AND run.assistant_run_id=$2
FOR UPDATE OF conversation,run`,
		commit.ConversationID, commit.RunID,
	).Scan(&tenantID, &actorUserID, &invocationState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return interactionapp.ErrLeaseLost
		}
		return fmt.Errorf("lock Assistant decision state: %w", err)
	}
	txStore := transaction{tx: tx}
	conversation, err := txStore.LoadConversation(
		ctx, tenantID, actorUserID, commit.ConversationID, false,
	)
	if err != nil {
		return err
	}
	run, _, _, err := loadRun(ctx, tx, commit.RunID, false)
	if err != nil {
		return err
	}
	if conversation.Revision != commit.ExpectedConversationRevision ||
		run.Revision != commit.ExpectedRunRevision ||
		run.FencingToken != commit.FencingToken ||
		run.ConversationID != conversation.ID ||
		invocationState != "request_started" ||
		!commit.OccurredAt.Before(run.LeaseExpiresAt) ||
		!commit.OccurredAt.Before(run.TimeoutAt) {
		return interactionapp.ErrLeaseLost
	}

	decisionJSON, err := json.Marshal(commit.Decision)
	if err != nil {
		return fmt.Errorf("encode Assistant decision: %w", err)
	}
	if err := run.Finish(commit.ExpectedRunRevision, commit.FencingToken, domain.TerminalOutcome{
		Status: domain.RunCompleted, Decision: decisionJSON, OccurredAt: commit.OccurredAt,
	}); err != nil {
		return err
	}
	taskDraftID := ""
	if commit.TaskDraft != nil {
		taskDraftID = commit.TaskDraft.Draft.ID
	}
	checkpointID := ""
	if commit.Checkpoint != nil {
		checkpointID = commit.Checkpoint.ID
	}
	assistantTurn, err := conversation.CommitAssistantDecision(
		commit.ExpectedConversationRevision, commit.RunID, commit.AssistantTurn.ID,
		commit.AssistantTurn.CorrelationID, commit.Decision.UserVisibleContent,
		taskDraftID, commit.Readiness.Assessment.ID, checkpointID, commit.OccurredAt,
	)
	if err != nil {
		return err
	}
	if err := insertReadiness(ctx, tx, commit.Readiness); err != nil {
		return err
	}
	if err := invalidateCurrentDraft(ctx, tx, conversation.ID, commit.OccurredAt); err != nil {
		return err
	}
	if commit.TaskDraft != nil {
		if err := insertTaskDraft(ctx, tx, *commit.TaskDraft); err != nil {
			return err
		}
	}
	if commit.Checkpoint != nil {
		if err := insertCheckpoint(ctx, tx, *commit.Checkpoint); err != nil {
			return err
		}
	}
	if err := txStore.InsertTurn(ctx, assistantTurn); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE assistant_runs
SET status=$1,lease_owner=NULL,lease_expires_at=NULL,invocation_state='response_received',
    result_decision=$2,error_code=NULL,retryable=false,completed_at=$3,updated_at=$3,revision=$4
WHERE assistant_run_id=$5 AND revision=$6 AND fencing_token=$7 AND status='claimed'
  AND invocation_state='request_started'`,
		run.Status, decisionJSON, commit.OccurredAt, run.Revision, run.ID,
		commit.ExpectedRunRevision, commit.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("complete AssistantRun: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.ErrLeaseLost
	}
	if err := txStore.UpdateConversation(ctx, conversation, commit.ExpectedConversationRevision); err != nil {
		return err
	}
	event := commit.Event
	event.TenantID = tenantID
	event.AggregateID = conversation.ID
	event.OccurredAt = commit.OccurredAt
	if err := txStore.InsertOutboxEvent(ctx, event); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Assistant decision: %w", err)
	}
	return nil
}

// CommitFailure makes a failed run and the release of its Conversation slot
// indivisible. It is intentionally separate from CommitDecision because no
// model output is authoritative on this path.
func (store Store) CommitFailure(ctx context.Context, commit interactionapp.RunFailureCommit) error {
	if store.Pool == nil {
		return fmt.Errorf("AssistantRun store is unavailable")
	}
	if err := validateFailureCommit(commit); err != nil {
		return err
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin Assistant failure commit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID, actorUserID string
	if err := tx.QueryRow(ctx, `
SELECT conversation.tenant_id,conversation.actor_user_id
FROM assistant_conversations conversation
JOIN assistant_runs run ON run.conversation_id=conversation.conversation_id
WHERE conversation.conversation_id=$1 AND run.assistant_run_id=$2
FOR UPDATE OF conversation,run`, commit.ConversationID, commit.RunID).Scan(&tenantID, &actorUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return interactionapp.ErrLeaseLost
		}
		return fmt.Errorf("lock Assistant failure state: %w", err)
	}
	txStore := transaction{tx: tx}
	conversation, err := txStore.LoadConversation(ctx, tenantID, actorUserID, commit.ConversationID, false)
	if err != nil {
		return err
	}
	run, _, _, err := loadRun(ctx, tx, commit.RunID, false)
	if err != nil {
		return err
	}
	if conversation.Revision != commit.ExpectedConversationRevision || run.Revision != commit.ExpectedRunRevision ||
		run.FencingToken != commit.FencingToken || run.ConversationID != conversation.ID || run.Status != domain.RunClaimed ||
		!commit.OccurredAt.Before(run.LeaseExpiresAt) {
		return interactionapp.ErrLeaseLost
	}
	if err := run.Finish(commit.ExpectedRunRevision, commit.FencingToken, domain.TerminalOutcome{
		Status: domain.RunFailed, ErrorCode: commit.ErrorCode, Retryable: commit.Retryable, OccurredAt: commit.OccurredAt,
	}); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE assistant_runs
SET status=$1,lease_owner=NULL,lease_expires_at=NULL,error_code=$2,retryable=$3,
    completed_at=$4,updated_at=$4,revision=$5
WHERE assistant_run_id=$6 AND revision=$7 AND fencing_token=$8 AND status='claimed'`,
		run.Status, run.ErrorCode, run.Retryable, commit.OccurredAt, run.Revision,
		run.ID, commit.ExpectedRunRevision, commit.FencingToken)
	if err != nil {
		return fmt.Errorf("fail AssistantRun: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.ErrLeaseLost
	}
	tag, err = tx.Exec(ctx, `
UPDATE assistant_conversations
SET active_assistant_run_id=NULL,revision=revision+1,updated_at=$1
WHERE conversation_id=$2 AND revision=$3 AND active_assistant_run_id=$4`,
		commit.OccurredAt, conversation.ID, commit.ExpectedConversationRevision, commit.RunID)
	if err != nil {
		return fmt.Errorf("release failed Assistant Conversation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return interactionapp.ErrLeaseLost
	}
	event := commit.Event
	event.TenantID = tenantID
	event.AggregateID = conversation.ID
	event.OccurredAt = commit.OccurredAt
	if err := txStore.InsertOutboxEvent(ctx, event); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit Assistant failure: %w", err)
	}
	return nil
}

func validateDecisionCommit(commit interactionapp.RunDecisionCommit) error {
	if commit.ConversationID == "" || commit.ExpectedConversationRevision == 0 ||
		commit.RunID == "" || commit.ExpectedRunRevision == 0 || commit.FencingToken == 0 ||
		commit.AssistantTurn.ID == "" || commit.AssistantTurn.CorrelationID == "" ||
		commit.OccurredAt.IsZero() {
		return fmt.Errorf("Assistant decision commit is incomplete")
	}
	if err := commit.Decision.Validate(); err != nil {
		return err
	}
	if err := commit.Readiness.Assessment.Validate(); err != nil || commit.Readiness.Digest == "" {
		return fmt.Errorf("Assistant readiness commit is invalid")
	}
	if commit.Readiness.Assessment.ID != commit.Decision.Readiness.AssessmentID ||
		commit.Readiness.Assessment.ConversationID != commit.ConversationID ||
		commit.Readiness.Assessment.AssistantRunID != commit.RunID {
		return fmt.Errorf("Assistant readiness commit target is inconsistent")
	}
	if (commit.TaskDraft == nil) != (commit.Decision.TaskDraft == nil) {
		return fmt.Errorf("Assistant TaskDraft commit is inconsistent")
	}
	if commit.TaskDraft != nil {
		if err := commit.TaskDraft.Draft.Validate(); err != nil ||
			commit.TaskDraft.Draft.ID != commit.Decision.TaskDraft.TaskDraftID ||
			commit.TaskDraft.Draft.ConversationID != commit.ConversationID ||
			commit.TaskDraft.Draft.ReadinessAssessmentID != commit.Readiness.Assessment.ID {
			return fmt.Errorf("Assistant TaskDraft commit is invalid")
		}
	}
	if (commit.Checkpoint == nil) != (commit.Decision.Checkpoint == nil) {
		return fmt.Errorf("Assistant checkpoint commit is inconsistent")
	}
	if commit.Checkpoint != nil {
		if err := commit.Checkpoint.Validate(); err != nil ||
			commit.Checkpoint.ID != commit.Decision.Checkpoint.CheckpointID ||
			commit.Checkpoint.ConversationID != commit.ConversationID ||
			commit.Checkpoint.ReadinessAssessmentID != commit.Readiness.Assessment.ID {
			return fmt.Errorf("Assistant checkpoint commit is invalid")
		}
	}
	if err := commit.Event.Validate(); err != nil ||
		commit.Event.AggregateType != "Conversation" ||
		commit.Event.AggregateID != commit.ConversationID {
		return fmt.Errorf("Assistant decision event is invalid")
	}
	return nil
}

func validateFailureCommit(commit interactionapp.RunFailureCommit) error {
	if commit.ConversationID == "" || commit.ExpectedConversationRevision == 0 || commit.RunID == "" ||
		commit.ExpectedRunRevision == 0 || commit.FencingToken == 0 || commit.ErrorCode == "" || commit.OccurredAt.IsZero() {
		return fmt.Errorf("Assistant failure commit is incomplete")
	}
	if err := commit.Event.Validate(); err != nil || commit.Event.AggregateType != "Conversation" ||
		commit.Event.AggregateID != commit.ConversationID {
		return fmt.Errorf("Assistant failure event is invalid")
	}
	return nil
}

func insertReadiness(ctx context.Context, tx pgx.Tx, record interactionapp.ReadinessRecord) error {
	dimensions := make(map[string]any, len(record.Assessment.Dimensions))
	for dimension, state := range record.Assessment.Dimensions {
		dimensions[string(dimension)] = map[string]any{
			"status": state.Status, "summary": state.Summary, "evidence_refs": state.EvidenceRefs,
		}
	}
	dimensionsJSON, _ := json.Marshal(dimensions)
	blockersJSON, _ := json.Marshal(append([]string{}, record.Assessment.BlockingUnknowns...))
	assumptionsJSON, _ := json.Marshal(append([]string{}, record.Assessment.Assumptions...))
	var workspaceID, workspaceRevisionID any
	if record.Workspace != nil {
		workspaceID = record.Workspace.WorkspaceID
		workspaceRevisionID = record.Workspace.WorkspaceRevisionID
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO assistant_readiness_assessments
  (assessment_id,conversation_id,assistant_run_id,assessment_revision,policy_revision,
   workspace_id,workspace_revision_id,dimensions,blocking_unknowns,assumptions,
   clarification_question_count,status,content_digest,evaluated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		record.Assessment.ID, record.Assessment.ConversationID, record.Assessment.AssistantRunID,
		record.Assessment.Revision, record.Assessment.PolicyRevision, workspaceID, workspaceRevisionID,
		dimensionsJSON, blockersJSON, assumptionsJSON, record.Assessment.ClarificationQuestions,
		record.Assessment.Status, record.Digest, record.Assessment.EvaluatedAt,
	); err != nil {
		return fmt.Errorf("insert immutable Assistant readiness assessment: %w", err)
	}
	return nil
}

func invalidateCurrentDraft(ctx context.Context, tx pgx.Tx, conversationID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `
UPDATE assistant_task_drafts
SET status='invalidated',invalidated_at=$1
WHERE conversation_id=$2 AND status='current'`, now, conversationID); err != nil {
		return fmt.Errorf("invalidate current Assistant TaskDraft: %w", err)
	}
	return nil
}

func insertTaskDraft(ctx context.Context, tx pgx.Tx, record interactionapp.TaskDraftRecord) error {
	if record.Draft.Status != domain.TaskDraftCurrent ||
		record.Draft.TaskSpecDigest != record.TaskSpec.CreationIntentRef.TaskSpecDigest {
		return fmt.Errorf("Assistant TaskDraft record is invalid")
	}
	taskSpec, err := json.Marshal(record.TaskSpec)
	if err != nil {
		return fmt.Errorf("encode Assistant TaskSpec: %w", err)
	}
	assumptions, _ := json.Marshal(append([]string{}, record.Draft.Assumptions...))
	if _, err := tx.Exec(ctx, `
INSERT INTO assistant_task_drafts
  (task_draft_id,conversation_id,initiator_user_id,revision,parent_task_draft_id,
   readiness_assessment_id,workspace_id,workspace_revision_id,task_spec_snapshot,
   task_spec_digest,assumptions,status,created_at)
VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,'current',$12)`,
		record.Draft.ID, record.Draft.ConversationID, record.Draft.InitiatorUserID,
		record.Draft.Revision, record.Draft.ParentTaskDraftID, record.Draft.ReadinessAssessmentID,
		record.Draft.WorkspaceID, record.Draft.WorkspaceRevisionID, taskSpec,
		record.Draft.TaskSpecDigest, assumptions, record.Draft.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert current Assistant TaskDraft: %w", err)
	}
	return nil
}

func insertCheckpoint(ctx context.Context, tx pgx.Tx, checkpoint domain.ClarificationCheckpoint) error {
	assumptions, _ := json.Marshal(append([]string{}, checkpoint.Assumptions...))
	blockers, _ := json.Marshal(append([]string{}, checkpoint.Blockers...))
	evidence, _ := json.Marshal(append([]string{}, checkpoint.EvidenceRefs...))
	actions, _ := json.Marshal(append([]domain.CheckpointAction{}, checkpoint.AllowedActions...))
	var taskDraftID, taskDraftRevision any
	if checkpoint.TaskDraftID != "" {
		taskDraftID = checkpoint.TaskDraftID
		taskDraftRevision = checkpoint.TaskDraftRevision
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO assistant_clarification_checkpoints
  (checkpoint_id,conversation_id,revision,task_draft_id,task_draft_revision,working_draft,
   readiness_assessment_id,assumptions,blockers,evidence_refs,allowed_actions,status,
   resolved_action,created_at,resolved_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL,$13,NULL)`,
		checkpoint.ID, checkpoint.ConversationID, checkpoint.Revision, taskDraftID, taskDraftRevision,
		checkpoint.WorkingDraft, checkpoint.ReadinessAssessmentID, assumptions, blockers, evidence,
		actions, checkpoint.Status, checkpoint.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert Assistant ClarificationCheckpoint: %w", err)
	}
	return nil
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadRun(
	ctx context.Context,
	query rowQuerier,
	runID string,
	forUpdate bool,
) (domain.AssistantRun, uint64, string, error) {
	statement := `
SELECT assistant_run_id,conversation_id,source_turn_id,run_sequence,status,model_profile,
       model_profile_revision,model_profile_digest,policy_revision,context_budget_tokens,timeout_at,
       COALESCE(lease_owner,''),COALESCE(lease_expires_at,'epoch'::timestamptz),fencing_token,
       attempt_count,COALESCE(result_decision,'null'::jsonb),COALESCE(error_code,''),retryable,
       revision,created_at,COALESCE(started_at,'epoch'::timestamptz),
       COALESCE(completed_at,'epoch'::timestamptz),updated_at
FROM assistant_runs WHERE assistant_run_id=$1`
	if forUpdate {
		statement += " FOR UPDATE"
	}
	var run domain.AssistantRun
	var status, profileRevisionText, profileDigest string
	var decision json.RawMessage
	err := query.QueryRow(ctx, statement, runID).Scan(
		&run.ID, &run.ConversationID, &run.SourceTurnID, &run.Sequence, &status, &run.ModelProfile,
		&profileRevisionText, &profileDigest, &run.PolicyRevision, &run.ContextBudgetTokens, &run.TimeoutAt,
		&run.LeaseOwner, &run.LeaseExpiresAt, &run.FencingToken, &run.AttemptCount,
		&decision, &run.ErrorCode, &run.Retryable, &run.Revision, &run.CreatedAt,
		&run.StartedAt, &run.CompletedAt, &run.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AssistantRun{}, 0, "", interactionapp.ErrNoQueuedRun
	}
	if err != nil {
		return domain.AssistantRun{}, 0, "", fmt.Errorf("load AssistantRun: %w", err)
	}
	run.Status = domain.RunStatus(status)
	if string(decision) != "null" {
		run.Decision = append([]byte(nil), decision...)
	}
	for _, target := range []*time.Time{&run.LeaseExpiresAt, &run.StartedAt, &run.CompletedAt} {
		if target.Equal(time.Unix(0, 0).UTC()) {
			*target = time.Time{}
		}
	}
	profileRevision, err := strconv.ParseUint(profileRevisionText, 10, 64)
	if err != nil || profileRevision == 0 {
		return domain.AssistantRun{}, 0, "", fmt.Errorf("Assistant model profile revision is invalid")
	}
	return run, profileRevision, profileDigest, nil
}

func loadConversationTurns(ctx context.Context, tx pgx.Tx, conversationID string) ([]domain.Turn, error) {
	rows, err := tx.Query(ctx, `
SELECT turn_id,conversation_id,turn_sequence,role,content,visible,
       COALESCE(source_assistant_run_id,''),correlation_id,created_at
FROM assistant_conversation_turns
WHERE conversation_id=$1
ORDER BY turn_sequence`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("load Conversation Turns: %w", err)
	}
	defer rows.Close()
	turns := make([]domain.Turn, 0)
	for rows.Next() {
		var turn domain.Turn
		var role string
		if err := rows.Scan(
			&turn.ID, &turn.ConversationID, &turn.Sequence, &role, &turn.Content, &turn.Visible,
			&turn.SourceAssistantRunID, &turn.CorrelationID, &turn.CreatedAt,
		); err != nil {
			return nil, err
		}
		turn.Role = domain.TurnRole(role)
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

func runConversationTenant(ctx context.Context, tx pgx.Tx, conversationID string) string {
	var tenantID string
	_ = tx.QueryRow(ctx, `SELECT tenant_id FROM assistant_conversations WHERE conversation_id=$1`, conversationID).Scan(&tenantID)
	return tenantID
}

func runConversationActor(ctx context.Context, tx pgx.Tx, conversationID string) string {
	var actorUserID string
	_ = tx.QueryRow(ctx, `SELECT actor_user_id FROM assistant_conversations WHERE conversation_id=$1`, conversationID).Scan(&actorUserID)
	return actorUserID
}
