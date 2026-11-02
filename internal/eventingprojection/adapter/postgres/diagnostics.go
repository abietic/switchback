package postgres

import (
	"context"
	"errors"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	"github.com/jackc/pgx/v5"
)

// ReadWorkerDiagnosticTarget reads current authority in one statement, never a
// stale View/timeline. Closed/expired Sessions retain historical read access.
func (store *Store) ReadWorkerDiagnosticTarget(ctx context.Context, query projectionapp.WorkerDiagnosticQuery) (projectionapp.WorkerDiagnosticTarget, error) {
	var target projectionapp.WorkerDiagnosticTarget
	if query.Viewer.TenantID == "" || query.Viewer.UserID == "" || query.WorkspaceID == "" || query.WorkerSessionID == "" || (query.AgentTurnID != "" && query.WorkerRunID == "") {
		return target, projectionapp.ErrInvalidViewQuery
	}
	err := store.pool.QueryRow(ctx, `
SELECT COALESCE(turn.started_at,turn.created_at,run.started_at,run.created_at,session.created_at),
       CASE WHEN $5<>'' THEN COALESCE(turn.terminal_at,CURRENT_TIMESTAMP)
            WHEN $4<>'' THEN COALESCE(run.terminal_at,CURRENT_TIMESTAMP)
            ELSE COALESCE(session.terminal_at,CURRENT_TIMESTAMP) END
FROM worker_sessions session
LEFT JOIN worker_runs run ON run.worker_run_id=$4 AND run.worker_session_id=session.worker_session_id
LEFT JOIN agent_turns turn ON turn.agent_turn_id=$5 AND turn.worker_run_id=run.worker_run_id
  AND turn.worker_session_id=session.worker_session_id
WHERE session.worker_session_id=$3 AND session.tenant_id=$1 AND session.user_id=$2
  AND session.workspace_id=$6
  AND ($4='' OR run.worker_run_id IS NOT NULL)
  AND ($5='' OR turn.agent_turn_id IS NOT NULL)`,
		query.Viewer.TenantID, query.Viewer.UserID, query.WorkerSessionID, query.WorkerRunID, query.AgentTurnID, query.WorkspaceID,
	).Scan(&target.StartedAt, &target.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, projectionapp.ErrViewUnavailable
	}
	return target, err
}
