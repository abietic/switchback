package postgres

import (
	"context"
	"errors"

	"github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
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
	var verifiedRunID string
	err := store.pool.QueryRow(ctx, `
SELECT COALESCE(turn.started_at,turn.created_at,run.started_at,run.created_at,session.created_at),
       CASE WHEN $5<>'' THEN COALESCE(turn.terminal_at,CURRENT_TIMESTAMP)
            WHEN $4<>'' THEN COALESCE(run.terminal_at,CURRENT_TIMESTAMP)
            ELSE COALESCE(session.terminal_at,CURRENT_TIMESTAMP) END,
       CASE WHEN (
         SELECT count(DISTINCT candidate.kind)
         FROM observation_candidates candidate
         JOIN observation_fragment_jobs job USING(kind,source_id,frame_digest)
         LEFT JOIN worker_turn_settlements settlement
           ON settlement.settlement_id=candidate.provenance->>'settlement_id'
           AND settlement.worker_session_id=session.worker_session_id
           AND settlement.worker_run_id=trace_run.worker_run_id AND settlement.tenant_id=session.tenant_id
         WHERE candidate.worker_run_id=trace_run.worker_run_id
           AND candidate.worker_session_id=session.worker_session_id
           AND candidate.tenant_id=session.tenant_id AND candidate.user_id=session.user_id
           AND candidate.workspace_id=session.workspace_id AND candidate.status='published'
           AND job.version=$7 AND job.status='passed'
           AND (candidate.kind='run_basis'
                OR (candidate.kind='turn_basis' AND settlement.agent_turn_id=$5))
       )=CASE WHEN $5='' THEN 1 ELSE 2 END THEN trace_run.worker_run_id ELSE '' END
FROM worker_sessions session
LEFT JOIN worker_runs run ON run.worker_run_id=$4 AND run.worker_session_id=session.worker_session_id
LEFT JOIN agent_turns turn ON turn.agent_turn_id=$5 AND turn.worker_run_id=run.worker_run_id
  AND turn.worker_session_id=session.worker_session_id
LEFT JOIN LATERAL (
    SELECT worker_run_id FROM worker_runs
    WHERE worker_session_id=session.worker_session_id AND ($4='' OR worker_run_id=$4)
    ORDER BY created_at DESC,worker_run_id DESC LIMIT 1
) trace_run ON true
WHERE session.worker_session_id=$3 AND session.tenant_id=$1 AND session.user_id=$2
  AND session.workspace_id=$6
  AND ($4='' OR run.worker_run_id IS NOT NULL)
  AND ($5='' OR turn.agent_turn_id IS NOT NULL)`,
		query.Viewer.TenantID, query.Viewer.UserID, query.WorkerSessionID, query.WorkerRunID, query.AgentTurnID, query.WorkspaceID,
		artifactapp.FragmentProjectionVersion,
	).Scan(&target.StartedAt, &target.EndedAt, &verifiedRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, projectionapp.ErrViewUnavailable
	}
	if err == nil && verifiedRunID != "" {
		target.LangfuseTraceID = langfuse.FragmentTraceID(artifactdomain.ObservationScope{
			TenantID: query.Viewer.TenantID, UserID: query.Viewer.UserID, WorkspaceID: query.WorkspaceID,
			WorkerSessionID: query.WorkerSessionID, WorkerRunID: verifiedRunID,
		})
	}
	return target, err
}
