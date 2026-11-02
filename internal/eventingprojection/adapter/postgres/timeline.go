package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

func (store *Store) ReadWorkerTimeline(
	ctx context.Context,
	query projectionapp.WorkerTimelineQuery,
) (projectionapp.WorkerTimelinePage, error) {
	if store == nil || store.pool == nil || query.Viewer.TenantID == "" ||
		query.Viewer.UserID == "" || query.WorkerSessionID == "" {
		return projectionapp.WorkerTimelinePage{}, projectionapp.ErrInvalidViewQuery
	}
	limit := query.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return projectionapp.WorkerTimelinePage{}, projectionapp.ErrInvalidViewQuery
	}
	var beforeAt *time.Time
	var beforeTurnID string
	if query.BeforeCursor != "" {
		decodedAt, decodedTurnID, err := projectionapp.DecodeTimelineCursor(query.BeforeCursor)
		if err != nil {
			return projectionapp.WorkerTimelinePage{}, projectionapp.ErrInvalidViewQuery
		}
		beforeAt, beforeTurnID = &decodedAt, decodedTurnID
	}
	rows, err := store.pool.Query(ctx, `
SELECT timeline.worker_run_id,timeline.agent_turn_id,timeline.display_stream_id,
       timeline.turn_status,timeline.turn_started_at,timeline.turn_terminal_at,
       turn.content,timeline.latest_checkpoint_version,timeline.checkpoint_complete,
       timeline.backfill_status,timeline.latest_checkpointed_at,
       checkpoint.checkpoint
FROM worker_timeline_turns timeline
JOIN agent_turns turn ON turn.agent_turn_id=timeline.agent_turn_id
LEFT JOIN LATERAL (
  SELECT checkpoint
  FROM display_checkpoints
  WHERE display_stream_id=timeline.display_stream_id
    AND checkpoint_version=timeline.latest_checkpoint_version
) checkpoint ON true
WHERE timeline.tenant_id=$1 AND timeline.user_id=$2 AND timeline.worker_session_id=$3
  AND ($4::timestamptz IS NULL OR
       (timeline.turn_started_at,timeline.agent_turn_id) < ($4,$5))
ORDER BY timeline.turn_started_at DESC,timeline.agent_turn_id DESC
LIMIT $6`,
		query.Viewer.TenantID, query.Viewer.UserID, query.WorkerSessionID,
		beforeAt, beforeTurnID, limit+1,
	)
	if err != nil {
		return projectionapp.WorkerTimelinePage{}, fmt.Errorf("query Worker timeline page: %w", err)
	}
	defer rows.Close()
	turns := make([]projectionapp.WorkerTimelineTurn, 0, limit+1)
	for rows.Next() {
		var turn projectionapp.WorkerTimelineTurn
		var content json.RawMessage
		var checkpointBytes []byte
		if err := rows.Scan(
			&turn.WorkerRunID, &turn.AgentTurnID, &turn.DisplayStreamID,
			&turn.Status, &turn.StartedAt, &turn.TerminalAt, &content,
			&turn.CheckpointVersion, &turn.CheckpointComplete, &turn.BackfillStatus,
			&turn.LatestCheckpointedAt, &checkpointBytes,
		); err != nil {
			return projectionapp.WorkerTimelinePage{}, fmt.Errorf("scan Worker timeline page: %w", err)
		}
		var prompt struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(content, &prompt)
		turn.Prompt = prompt.Text
		turn.Parts = []projectionapp.DisplayPart{}
		if len(checkpointBytes) > 0 {
			checkpoint, err := projectionapp.DecodeCheckpoint(checkpointBytes)
			if err != nil || checkpoint.DisplayStreamID != turn.DisplayStreamID ||
				checkpoint.CheckpointVersion != turn.CheckpointVersion {
				return projectionapp.WorkerTimelinePage{}, projectionapp.ErrDisplayCheckpointConflict
			}
			turn.Parts = checkpoint.Parts
		}
		if turn.TerminalAt != nil && turn.LatestCheckpointedAt != nil {
			turn.ProjectionLagMillis = max(
				0, turn.LatestCheckpointedAt.Sub(*turn.TerminalAt).Milliseconds(),
			)
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return projectionapp.WorkerTimelinePage{}, fmt.Errorf("read Worker timeline page: %w", err)
	}
	page := projectionapp.WorkerTimelinePage{
		Turns: make([]projectionapp.WorkerTimelineTurn, min(len(turns), limit)),
	}
	copy(page.Turns, turns[:min(len(turns), limit)])
	if len(turns) > limit {
		page.HasMore = true
		oldest := page.Turns[len(page.Turns)-1]
		page.NextCursor = projectionapp.EncodeTimelineCursor(oldest.StartedAt, oldest.AgentTurnID)
	}
	return page, nil
}

func (store *Store) ReconcileTerminalDisplayHistory(
	ctx context.Context,
	tenantID, userID string,
) (int64, error) {
	if store == nil || store.pool == nil || tenantID == "" || userID == "" {
		return 0, projectionapp.ErrInvalidViewQuery
	}
	updated, err := store.pool.Exec(ctx, `
UPDATE worker_timeline_turns timeline
SET turn_terminal_at=turn.terminal_at,
    turn_status=turn.status,
    backfill_status=CASE
      WHEN timeline.checkpoint_complete THEN timeline.backfill_status
      ELSE 'non_backfillable'
    END
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
WHERE timeline.worker_session_id=turn.worker_session_id
  AND timeline.agent_turn_id=turn.agent_turn_id
  AND session.tenant_id=$1 AND session.user_id=$2
  AND turn.status IN ('completed','failed','cancelled','timed_out')
  AND (
    timeline.turn_status<>turn.status OR
    timeline.turn_terminal_at IS DISTINCT FROM turn.terminal_at OR
    (NOT timeline.checkpoint_complete AND timeline.backfill_status<>'non_backfillable')
  )`, tenantID, userID)
	if err != nil {
		return 0, fmt.Errorf("refresh terminal Display history: %w", err)
	}
	inserted, err := store.pool.Exec(ctx, `
INSERT INTO worker_timeline_turns
  (tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,agent_turn_id,
   display_stream_id,turn_started_at,turn_terminal_at,turn_status,
   latest_checkpoint_version,checkpoint_complete,backfill_status)
SELECT session.tenant_id,session.user_id,session.workspace_id,turn.worker_session_id,
       turn.worker_run_id,turn.agent_turn_id,'display-legacy-' || turn.agent_turn_id,
       COALESCE(turn.started_at,turn.created_at),turn.terminal_at,turn.status,
       0,false,'non_backfillable'
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
WHERE session.tenant_id=$1 AND session.user_id=$2
  AND turn.status IN ('completed','failed','cancelled','timed_out')
  AND NOT EXISTS (
    SELECT 1 FROM worker_timeline_turns existing
    WHERE existing.worker_session_id=turn.worker_session_id
      AND existing.agent_turn_id=turn.agent_turn_id
  )
ON CONFLICT DO NOTHING`, tenantID, userID)
	if err != nil {
		return 0, fmt.Errorf("reconcile terminal Display history: %w", err)
	}
	return updated.RowsAffected() + inserted.RowsAffected(), nil
}

func (store *Store) LatestDisplayCheckpoint(
	ctx context.Context,
	displayStreamID string,
) (projectionapp.DisplayCheckpoint, error) {
	var raw json.RawMessage
	err := store.pool.QueryRow(ctx, `
SELECT checkpoint FROM display_checkpoints
WHERE display_stream_id=$1 ORDER BY checkpoint_version DESC LIMIT 1`, displayStreamID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return projectionapp.DisplayCheckpoint{}, projectionapp.ErrViewUnavailable
	}
	if err != nil {
		return projectionapp.DisplayCheckpoint{}, err
	}
	return projectionapp.DecodeCheckpoint(raw)
}

var _ projectionapp.WorkerTimelineReader = (*Store)(nil)
