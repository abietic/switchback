// Package postgres implements Task Orchestration persistence against PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

type Repository struct{}

func (Repository) AdvanceStatus(
	ctx context.Context,
	tx platformpostgres.Tx,
	taskID string,
	expectedRevision uint64,
	from domain.TaskStatus,
	to domain.TaskStatus,
	now time.Time,
) (uint64, error) {
	if _, err := from.Transition(to); err != nil {
		return 0, err
	}
	if taskID == "" || expectedRevision == 0 || now.IsZero() {
		return 0, fmt.Errorf("Task CAS requires identity, revision, and server time")
	}
	var revision uint64
	err := tx.QueryRow(ctx, `
UPDATE tasks
SET status = $4, revision = revision + 1, updated_at = $5
WHERE task_id = $1 AND revision = $2 AND status = $3
RETURNING revision`, taskID, expectedRevision, from, to, now).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, classifyTaskMiss(ctx, tx, taskID)
	}
	if err != nil {
		return 0, fmt.Errorf("advance Task status: %w", err)
	}
	return revision, nil
}

func classifyTaskMiss(ctx context.Context, tx platformpostgres.Tx, taskID string) error {
	var exists bool
	err := tx.QueryRow(ctx, "SELECT true FROM tasks WHERE task_id=$1", taskID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return platformpostgres.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("classify Task CAS miss: %w", err)
	}
	return platformpostgres.ErrRevisionConflict
}
