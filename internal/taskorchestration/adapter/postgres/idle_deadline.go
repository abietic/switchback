package postgres

import (
	"context"
	"fmt"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

func refreshSessionIdleDeadline(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	expectedRevision uint64,
	now time.Time,
) error {
	dueAt := now.UTC().Add(48 * time.Hour)
	tag, err := tx.Exec(ctx, `UPDATE worker_sessions SET idle_expires_at=$3,updated_at=$4
WHERE worker_session_id=$1 AND session_revision=$2 AND status NOT IN ('closed','expired')`,
		sessionID, expectedRevision, dueAt, now.UTC())
	if err != nil {
		return fmt.Errorf("refresh Task WorkerSession idle deadline: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("refresh Task WorkerSession idle deadline: %w", platformpostgres.ErrRevisionConflict)
	}
	tag, err = tx.Exec(ctx, `UPDATE scheduled_commands
SET expected_revision=$2,due_at=$3,status='pending',lease_owner=NULL,lease_expires_at=NULL,
    last_error_code=NULL,updated_at=$4
WHERE target_type='WorkerSession' AND target_id=$1
  AND command_type='worker_session_idle_expiry' AND status IN ('pending','leased','applied')`,
		sessionID, expectedRevision, dueAt, now.UTC())
	if err != nil {
		return fmt.Errorf("rearm Task WorkerSession idle command: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("rearm Task WorkerSession idle command: %w", platformpostgres.ErrRevisionConflict)
	}
	return nil
}
