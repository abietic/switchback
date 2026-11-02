package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type SessionReader struct {
	Pool *pgxpool.Pool
}

type RuntimeReader struct {
	Pool *pgxpool.Pool
}

func (reader SessionReader) ReadWorkerSession(
	ctx context.Context,
	sessionID string,
) (application.WorkerSessionSnapshot, error) {
	var snapshot application.WorkerSessionSnapshot
	var activeRunID, boundTaskID *string
	err := reader.Pool.QueryRow(ctx, `
SELECT worker_session_id,tenant_id,user_id,workspace_id,working_copy_id,status,
  active_worker_run_id,bound_task_id,session_revision,runtime_generation,
  idle_expires_at,absolute_expires_at
FROM worker_sessions WHERE worker_session_id=$1`, sessionID).Scan(
		&snapshot.WorkerSessionID, &snapshot.TenantID, &snapshot.UserID, &snapshot.WorkspaceID,
		&snapshot.WorkingCopyID, &snapshot.Status, &activeRunID, &boundTaskID,
		&snapshot.Revision, &snapshot.RuntimeGeneration, &snapshot.IdleExpiresAt,
		&snapshot.AbsoluteExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.WorkerSessionSnapshot{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return application.WorkerSessionSnapshot{}, fmt.Errorf("read WorkerSession snapshot: %w", err)
	}
	if activeRunID != nil {
		snapshot.ActiveWorkerRunID = *activeRunID
	}
	if boundTaskID != nil {
		snapshot.BoundTaskID = *boundTaskID
	}
	return snapshot, nil
}

func (reader RuntimeReader) ReadRuntimeInstance(
	ctx context.Context,
	runtimeInstanceID string,
) (application.RuntimeInstanceSnapshot, error) {
	var snapshot application.RuntimeInstanceSnapshot
	err := reader.Pool.QueryRow(ctx, `
SELECT runtime_instance_id,worker_session_id,COALESCE(worker_run_id,''),generation,status,
       resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,
       network_enforcement_status,revision,created_at
FROM runtime_instances
WHERE runtime_instance_id=$1`, runtimeInstanceID).Scan(
		&snapshot.RuntimeInstanceID, &snapshot.WorkerSessionID, &snapshot.WorkerRunID,
		&snapshot.Generation, &snapshot.Status, &snapshot.ResourceProfileID,
		&snapshot.NetworkProfile.ID, &snapshot.NetworkProfile.Revision,
		&snapshot.NetworkProfile.Digest, &snapshot.NetworkEnforcementStatus,
		&snapshot.Revision, &snapshot.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeInstanceSnapshot{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return application.RuntimeInstanceSnapshot{}, fmt.Errorf("read RuntimeInstance snapshot: %w", err)
	}
	return snapshot, nil
}
