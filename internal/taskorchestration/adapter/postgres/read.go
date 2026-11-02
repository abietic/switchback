package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func (store *Store) ReadTask(ctx context.Context, taskID string) (application.TaskSnapshot, error) {
	var snapshot application.TaskSnapshot
	var workerSessionID, currentWorkerRunID *string
	err := store.pool.QueryRow(ctx, `
SELECT task_id,tenant_id,initiator_user_id,workspace_id,status,acceptance_mode,
  worker_session_id,current_worker_run_id,revision
FROM tasks WHERE task_id=$1`, taskID).Scan(
		&snapshot.TaskID, &snapshot.TenantID, &snapshot.InitiatorUserID, &snapshot.WorkspaceID,
		&snapshot.Status, &snapshot.AcceptanceMode, &workerSessionID, &currentWorkerRunID,
		&snapshot.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TaskSnapshot{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return application.TaskSnapshot{}, fmt.Errorf("read Task snapshot: %w", err)
	}
	if workerSessionID != nil {
		snapshot.WorkerSessionID = *workerSessionID
	}
	if currentWorkerRunID != nil {
		snapshot.CurrentWorkerRunID = *currentWorkerRunID
	}
	return snapshot, nil
}
