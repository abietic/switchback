package postgres

import (
	"context"
	"errors"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

// runtimeTaskLock keeps Task before Session/Run/Turn/Runtime in the lock order.
// The unlocked discovery is only a hint: callers must revalidate after locking
// the Session, including the no-binding case. Never acquire a newly bound Task
// while holding the graph locks.
type runtimeTaskLock struct {
	TaskID, Status                string
	Revision                      uint64
	tenantID, userID, workspaceID string
}

func lockRuntimeTaskBeforeGraph(ctx context.Context, tx platformpostgres.Tx, sessionID string) (runtimeTaskLock, error) {
	var locked runtimeTaskLock
	err := tx.QueryRow(ctx, `SELECT COALESCE(bound_task_id,''),tenant_id,user_id,workspace_id
FROM worker_sessions WHERE worker_session_id=$1`, sessionID).Scan(&locked.TaskID, &locked.tenantID, &locked.userID, &locked.workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return locked, platformpostgres.ErrNotFound
	}
	if err != nil || locked.TaskID == "" {
		return locked, err
	}
	var tenantID, workspaceID, workerSessionID string
	err = tx.QueryRow(ctx, `SELECT status,revision,tenant_id,workspace_id,COALESCE(worker_session_id,'')
FROM tasks WHERE task_id=$1 FOR UPDATE`, locked.TaskID).Scan(&locked.Status, &locked.Revision, &tenantID, &workspaceID, &workerSessionID)
	if err != nil {
		return locked, err
	}
	if tenantID != locked.tenantID || workspaceID != locked.workspaceID || workerSessionID != sessionID {
		return locked, platformpostgres.ErrRevisionConflict
	}
	return locked, nil
}

// Session must already be locked by the caller's graph read.
func (locked runtimeTaskLock) revalidate(ctx context.Context, tx platformpostgres.Tx, sessionID string) error {
	var current runtimeTaskLock
	if err := tx.QueryRow(ctx, `SELECT COALESCE(bound_task_id,''),tenant_id,user_id,workspace_id
FROM worker_sessions WHERE worker_session_id=$1`, sessionID).Scan(&current.TaskID, &current.tenantID, &current.userID, &current.workspaceID); err != nil {
		return err
	}
	if current.TaskID != locked.TaskID || current.tenantID != locked.tenantID || current.userID != locked.userID || current.workspaceID != locked.workspaceID {
		return platformpostgres.ErrRevisionConflict
	}
	return nil
}
