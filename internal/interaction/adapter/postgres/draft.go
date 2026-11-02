package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	interactionapp "github.com/abietic/switchback/internal/interaction/application"
	"github.com/abietic/switchback/internal/interaction/domain"
)

func (store Store) ReplaceCurrentDraft(
	ctx context.Context,
	previousDraftID string,
	record interactionapp.TaskDraftRecord,
	now time.Time,
) error {
	if store.Pool == nil || record.Draft.Status != domain.TaskDraftCurrent ||
		record.Draft.TaskSpecDigest != record.TaskSpec.CreationIntentRef.TaskSpecDigest {
		return fmt.Errorf("TaskDraft replacement is invalid")
	}
	tx, err := store.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin TaskDraft replacement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if previousDraftID == "" {
		var count int
		if err := tx.QueryRow(ctx, `
SELECT count(*) FROM assistant_task_drafts
WHERE conversation_id=$1 AND status='current'`, record.Draft.ConversationID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return domain.ErrStaleRevision
		}
	} else {
		tag, err := tx.Exec(ctx, `
UPDATE assistant_task_drafts
SET status='invalidated',invalidated_at=$1
WHERE task_draft_id=$2 AND conversation_id=$3 AND status='current' AND revision=$4`,
			now, previousDraftID, record.Draft.ConversationID, record.Draft.Revision-1)
		if err != nil {
			return fmt.Errorf("invalidate prior TaskDraft: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrStaleRevision
		}
	}
	taskSpec, err := json.Marshal(record.TaskSpec)
	if err != nil {
		return err
	}
	assumptions, _ := json.Marshal(append([]string{}, record.Draft.Assumptions...))
	_, err = tx.Exec(ctx, `
INSERT INTO assistant_task_drafts
  (task_draft_id,conversation_id,initiator_user_id,revision,parent_task_draft_id,
   readiness_assessment_id,workspace_id,workspace_revision_id,task_spec_snapshot,
   task_spec_digest,assumptions,status,created_at)
VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,'current',$12)`,
		record.Draft.ID, record.Draft.ConversationID, record.Draft.InitiatorUserID,
		record.Draft.Revision, record.Draft.ParentTaskDraftID, record.Draft.ReadinessAssessmentID,
		record.Draft.WorkspaceID, record.Draft.WorkspaceRevisionID, taskSpec,
		record.Draft.TaskSpecDigest, assumptions, record.Draft.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert current TaskDraft: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit TaskDraft replacement: %w", err)
	}
	return nil
}

var _ interactionapp.TaskDraftRepository = Store{}
