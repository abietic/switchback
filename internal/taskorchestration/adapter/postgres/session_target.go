package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func (store *Store) CreateSessionTargetIntent(
	ctx context.Context,
	record application.SessionTargetIntentRecord,
) (application.SessionTargetIntent, error) {
	var result application.SessionTargetIntent
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		existing, found, err := readSessionTargetIntentByKey(ctx, tx, record)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		var tenantID, userID, workspaceID, workingCopyID, status string
		var sessionRevision uint64
		err = tx.QueryRow(ctx, `
SELECT tenant_id,user_id,workspace_id,working_copy_id,status,session_revision
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, record.Command.WorkerSessionID).Scan(
			&tenantID, &userID, &workspaceID, &workingCopyID, &status, &sessionRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrSessionReuseInvalid
		}
		if err != nil {
			return fmt.Errorf("lock exact reuse WorkerSession: %w", err)
		}
		if tenantID != record.Command.Actor.TenantID || userID != record.Command.Actor.UserID ||
			workspaceID != record.Command.WorkspaceID || status == "closed" || status == "expired" || status == "lost" {
			return application.ErrSessionReuseInvalid
		}
		_, err = tx.Exec(ctx, `
INSERT INTO worker_session_target_intents
  (intent_id,tenant_id,actor_user_id,workspace_id,worker_session_id,worker_session_revision,
   working_copy_id,task_spec_digest,source_ref,source_revision,busy_policy,status,idempotency_key,
   created_at,expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queue','active',$11,$12,$13)`,
			record.IntentID, tenantID, userID, workspaceID, record.Command.WorkerSessionID,
			sessionRevision, workingCopyID, record.Command.TaskSpecDigest, record.Command.SourceRef,
			record.Command.SourceRevision, record.Command.IdempotencyKey, record.CreatedAt, record.ExpiresAt)
		if err != nil {
			return fmt.Errorf("insert WorkerSessionTargetIntent: %w", err)
		}
		result = application.SessionTargetIntent{
			IntentID: record.IntentID, WorkerSessionID: record.Command.WorkerSessionID,
			WorkerSessionRevision: sessionRevision, WorkingCopyID: workingCopyID,
			TaskSpecDigest: record.Command.TaskSpecDigest, ExpiresAt: record.ExpiresAt,
		}
		return nil
	})
	return result, err
}

func readSessionTargetIntentByKey(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.SessionTargetIntentRecord,
) (application.SessionTargetIntent, bool, error) {
	var result application.SessionTargetIntent
	var workspaceID, sourceRef, busyPolicy string
	var sourceRevision uint64
	err := tx.QueryRow(ctx, `
SELECT intent_id,workspace_id,worker_session_id,worker_session_revision,working_copy_id,
  task_spec_digest,source_ref,source_revision,busy_policy,expires_at
FROM worker_session_target_intents
WHERE tenant_id=$1 AND actor_user_id=$2 AND idempotency_key=$3`,
		record.Command.Actor.TenantID, record.Command.Actor.UserID, record.Command.IdempotencyKey).Scan(
		&result.IntentID, &workspaceID, &result.WorkerSessionID, &result.WorkerSessionRevision,
		&result.WorkingCopyID, &result.TaskSpecDigest, &sourceRef, &sourceRevision, &busyPolicy,
		&result.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SessionTargetIntent{}, false, nil
	}
	if err != nil {
		return application.SessionTargetIntent{}, false, fmt.Errorf("read idempotent Session target intent: %w", err)
	}
	if workspaceID != record.Command.WorkspaceID || result.WorkerSessionID != record.Command.WorkerSessionID ||
		result.TaskSpecDigest != record.Command.TaskSpecDigest || sourceRef != record.Command.SourceRef ||
		sourceRevision != record.Command.SourceRevision || busyPolicy != record.Command.BusyPolicy {
		return application.SessionTargetIntent{}, false, application.ErrIdempotencyConflict
	}
	return result, true, nil
}
