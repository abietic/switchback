package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

func (store *Store) SaveDisplayCheckpoint(
	ctx context.Context,
	checkpoint projectionapp.DisplayCheckpoint,
	digest, sourceEventID string,
) error {
	if store == nil || store.pool == nil {
		return fmt.Errorf("projection PostgreSQL pool is required")
	}
	return platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var existingDigest string
		err := tx.QueryRow(ctx, `
SELECT checkpoint_digest FROM display_checkpoints WHERE source_event_id=$1`, sourceEventID).Scan(&existingDigest)
		if err == nil {
			if existingDigest != digest {
				return projectionapp.ErrDisplayCheckpointConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read DisplayCheckpoint source receipt: %w", err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, checkpoint.DisplayStreamID); err != nil {
			return fmt.Errorf("lock DisplayCheckpoint stream: %w", err)
		}
		err = tx.QueryRow(ctx, `
SELECT checkpoint_digest FROM display_checkpoints
WHERE display_stream_id=$1 AND checkpoint_version=$2`,
			checkpoint.DisplayStreamID, checkpoint.CheckpointVersion).Scan(&existingDigest)
		if err == nil {
			if existingDigest != digest {
				return projectionapp.ErrDisplayCheckpointConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read idempotent DisplayCheckpoint: %w", err)
		}
		var latestVersion uint64
		if err := tx.QueryRow(ctx, `
SELECT coalesce(max(checkpoint_version),0) FROM display_checkpoints WHERE display_stream_id=$1`,
			checkpoint.DisplayStreamID).Scan(&latestVersion); err != nil {
			return fmt.Errorf("read latest DisplayCheckpoint version: %w", err)
		}
		if checkpoint.CheckpointVersion != latestVersion+1 {
			return projectionapp.ErrDisplayCheckpointConflict
		}
		_, err = tx.Exec(ctx, `
INSERT INTO display_checkpoints
  (display_stream_id,checkpoint_version,tenant_id,user_id,workspace_id,worker_session_id,
   worker_run_id,agent_turn_id,status,last_observed_transient_sequence,checkpoint,
   checkpoint_digest,source_event_id,checkpointed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			checkpoint.DisplayStreamID, checkpoint.CheckpointVersion, checkpoint.TenantID, checkpoint.UserID,
			checkpoint.WorkspaceID, checkpoint.WorkerSessionID, checkpoint.WorkerRunID, checkpoint.AgentTurnID,
			checkpoint.Status, checkpoint.LastObservedTransientSequence, checkpoint, digest,
			sourceEventID, checkpoint.CheckpointedAt)
		if err != nil {
			return fmt.Errorf("insert DisplayCheckpoint: %w", err)
		}
		var workspaceID, tenantID, userID, workerRunID, turnStatus string
		var turnStartedAt time.Time
		var turnTerminalAt *time.Time
		if err := tx.QueryRow(ctx, `
SELECT session.workspace_id,session.tenant_id,session.user_id,turn.worker_run_id,turn.status,
       COALESCE(turn.started_at,turn.created_at),turn.terminal_at
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
WHERE turn.worker_session_id=$1 AND turn.agent_turn_id=$2`,
			checkpoint.WorkerSessionID, checkpoint.AgentTurnID,
		).Scan(
			&workspaceID, &tenantID, &userID, &workerRunID, &turnStatus,
			&turnStartedAt, &turnTerminalAt,
		); err != nil {
			return fmt.Errorf("read DisplayCheckpoint Turn identity: %w", err)
		}
		if workspaceID != checkpoint.WorkspaceID || tenantID != checkpoint.TenantID ||
			userID != checkpoint.UserID || workerRunID != checkpoint.WorkerRunID {
			return projectionapp.ErrDisplayCheckpointConflict
		}
		checkpointComplete := checkpoint.Status == "completed" || checkpoint.Status == "failed" ||
			checkpoint.Status == "cancelled" || checkpoint.Status == "timed_out"
		command, err := tx.Exec(ctx, `
INSERT INTO worker_timeline_turns
  (tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,agent_turn_id,
   display_stream_id,turn_started_at,turn_terminal_at,turn_status,latest_checkpoint_version,
   checkpoint_complete,backfill_status,first_checkpointed_at,latest_checkpointed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'online',$13,$13)
ON CONFLICT (worker_session_id,agent_turn_id) DO UPDATE SET
  turn_terminal_at=EXCLUDED.turn_terminal_at,
  turn_status=EXCLUDED.turn_status,
  latest_checkpoint_version=EXCLUDED.latest_checkpoint_version,
  checkpoint_complete=EXCLUDED.checkpoint_complete,
  backfill_status='online',
  latest_checkpointed_at=EXCLUDED.latest_checkpointed_at
WHERE worker_timeline_turns.display_stream_id=EXCLUDED.display_stream_id
  AND worker_timeline_turns.latest_checkpoint_version < EXCLUDED.latest_checkpoint_version`,
			checkpoint.TenantID, checkpoint.UserID, checkpoint.WorkspaceID,
			checkpoint.WorkerSessionID, checkpoint.WorkerRunID, checkpoint.AgentTurnID,
			checkpoint.DisplayStreamID, turnStartedAt, turnTerminalAt, turnStatus,
			checkpoint.CheckpointVersion, checkpointComplete, checkpoint.CheckpointedAt,
		)
		if err != nil {
			return fmt.Errorf("upsert Worker timeline Turn: %w", err)
		}
		if command.RowsAffected() != 1 {
			return projectionapp.ErrDisplayCheckpointConflict
		}
		return nil
	})
}

var _ projectionapp.CheckpointStore = (*Store)(nil)
