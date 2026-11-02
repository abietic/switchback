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

func (store Store) SaveCheckpoint(ctx context.Context, checkpoint domain.ClarificationCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	assumptions, _ := json.Marshal(append([]string{}, checkpoint.Assumptions...))
	blockers, _ := json.Marshal(append([]string{}, checkpoint.Blockers...))
	evidence, _ := json.Marshal(append([]string{}, checkpoint.EvidenceRefs...))
	actions, _ := json.Marshal(append([]domain.CheckpointAction{}, checkpoint.AllowedActions...))
	var taskDraftID, taskDraftRevision any
	if checkpoint.TaskDraftID != "" {
		taskDraftID = checkpoint.TaskDraftID
		taskDraftRevision = checkpoint.TaskDraftRevision
	}
	_, err := store.Pool.Exec(ctx, `
INSERT INTO assistant_clarification_checkpoints
  (checkpoint_id,conversation_id,revision,task_draft_id,task_draft_revision,working_draft,
   readiness_assessment_id,assumptions,blockers,evidence_refs,allowed_actions,status,
   resolved_action,created_at,resolved_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL,$13,NULL)`,
		checkpoint.ID, checkpoint.ConversationID, checkpoint.Revision, taskDraftID, taskDraftRevision,
		checkpoint.WorkingDraft, checkpoint.ReadinessAssessmentID, assumptions, blockers, evidence,
		actions, checkpoint.Status, checkpoint.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert ClarificationCheckpoint: %w", err)
	}
	return nil
}

func (store Store) ApplyCheckpointAction(
	ctx context.Context,
	conversationID, checkpointID string,
	expectedRevision uint64,
	action domain.CheckpointAction,
	now time.Time,
) (domain.ClarificationCheckpoint, error) {
	status := domain.CheckpointResolved
	if action == domain.CheckpointCancel {
		status = domain.CheckpointCancelled
	}
	var checkpoint domain.ClarificationCheckpoint
	var assumptions, blockers, evidence, actions json.RawMessage
	var taskDraftID *string
	var taskDraftRevision *uint64
	err := store.Pool.QueryRow(ctx, `
UPDATE assistant_clarification_checkpoints
SET revision=revision+1,status=$1,resolved_action=$2,resolved_at=$3
WHERE conversation_id=$4 AND checkpoint_id=$5 AND revision=$6 AND status='active'
  AND allowed_actions ? $7
RETURNING checkpoint_id,conversation_id,revision,task_draft_id,task_draft_revision,working_draft,
          readiness_assessment_id,assumptions,blockers,evidence_refs,allowed_actions,status,
          COALESCE(resolved_action,''),created_at,resolved_at`,
		status, action, now, conversationID, checkpointID, expectedRevision, string(action),
	).Scan(
		&checkpoint.ID, &checkpoint.ConversationID, &checkpoint.Revision, &taskDraftID, &taskDraftRevision,
		&checkpoint.WorkingDraft, &checkpoint.ReadinessAssessmentID, &assumptions, &blockers,
		&evidence, &actions, &checkpoint.Status, &checkpoint.ResolvedAction,
		&checkpoint.CreatedAt, &checkpoint.ResolvedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ClarificationCheckpoint{}, domain.ErrStaleRevision
	}
	if err != nil {
		return domain.ClarificationCheckpoint{}, fmt.Errorf("apply ClarificationCheckpoint action: %w", err)
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

var _ interactionapp.CheckpointRepository = Store{}
