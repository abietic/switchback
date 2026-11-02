// Package postgres implements Worker Runtime persistence against PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type Repository struct{}

func (Repository) AdvanceSessionStatus(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	expectedRevision uint64,
	from domain.WorkerSessionStatus,
	to domain.WorkerSessionStatus,
	now time.Time,
	terminalReason string,
) (uint64, error) {
	if _, err := from.Transition(to); err != nil {
		return 0, err
	}
	if sessionID == "" || expectedRevision == 0 || now.IsZero() {
		return 0, fmt.Errorf("WorkerSession CAS requires identity, revision, and server time")
	}
	var revision uint64
	err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status = $4::text,
    session_revision = session_revision + 1,
    updated_at = $5::timestamptz,
    terminal_at = CASE WHEN $4::text IN ('expired','closed') THEN $5::timestamptz ELSE NULL END,
    terminal_reason = CASE WHEN $4::text IN ('expired','closed') THEN NULLIF($6::text, '') ELSE NULL END
WHERE worker_session_id = $1 AND session_revision = $2 AND status = $3::text
RETURNING session_revision`, sessionID, expectedRevision, from, to, now, terminalReason).Scan(&revision)
	return revision, classifyWorkerCAS(ctx, tx, err, "worker_sessions", "worker_session_id", sessionID, "advance WorkerSession status")
}

func (Repository) AdvanceRunStatus(
	ctx context.Context,
	tx platformpostgres.Tx,
	runID string,
	expectedRevision uint64,
	from domain.WorkerRunStatus,
	to domain.WorkerRunStatus,
	now time.Time,
	terminalReason string,
) (uint64, error) {
	if _, err := from.Transition(to); err != nil {
		return 0, err
	}
	if runID == "" || expectedRevision == 0 || now.IsZero() {
		return 0, fmt.Errorf("WorkerRun CAS requires identity, revision, and server time")
	}
	var revision uint64
	err := tx.QueryRow(ctx, `
UPDATE worker_runs
SET status = $4::text,
    revision = revision + 1,
    started_at = CASE WHEN $4::text = 'running' THEN COALESCE(started_at, $5::timestamptz) ELSE started_at END,
    terminal_at = CASE WHEN $4::text IN ('completed','failed','cancelled','timed_out') THEN $5::timestamptz ELSE NULL END,
    terminal_reason = CASE WHEN $4::text IN ('failed','cancelled','timed_out') THEN NULLIF($6::text, '') ELSE NULL END
WHERE worker_run_id = $1 AND revision = $2 AND status = $3::text
RETURNING revision`, runID, expectedRevision, from, to, now, terminalReason).Scan(&revision)
	return revision, classifyWorkerCAS(ctx, tx, err, "worker_runs", "worker_run_id", runID, "advance WorkerRun status")
}

func (Repository) AdvanceTurnStatus(
	ctx context.Context,
	tx platformpostgres.Tx,
	turnID string,
	expectedRevision uint64,
	from domain.AgentTurnStatus,
	to domain.AgentTurnStatus,
	now time.Time,
	terminalReason string,
) (uint64, error) {
	if _, err := from.Transition(to); err != nil {
		return 0, err
	}
	if turnID == "" || expectedRevision == 0 || now.IsZero() {
		return 0, fmt.Errorf("AgentTurn CAS requires identity, revision, and server time")
	}
	var revision uint64
	err := tx.QueryRow(ctx, `
UPDATE agent_turns
SET status = $4::text,
    revision = revision + 1,
    started_at = CASE WHEN $4::text = 'delivered' AND mode IN ('prompt','queue') THEN COALESCE(started_at, $5::timestamptz) ELSE started_at END,
    deadline_at = CASE WHEN $4::text = 'delivered' AND mode IN ('prompt','queue') THEN COALESCE(deadline_at, $5::timestamptz + interval '24 hours') ELSE deadline_at END,
    terminal_at = CASE WHEN $4::text IN ('completed','cancelled','timed_out','failed') THEN $5::timestamptz ELSE NULL END,
    terminal_reason = CASE WHEN $4::text IN ('cancelled','timed_out','failed') THEN NULLIF($6::text, '') ELSE NULL END
WHERE agent_turn_id = $1 AND revision = $2 AND status = $3::text
RETURNING revision`, turnID, expectedRevision, from, to, now, terminalReason).Scan(&revision)
	return revision, classifyWorkerCAS(ctx, tx, err, "agent_turns", "agent_turn_id", turnID, "advance AgentTurn status")
}

func classifyWorkerCAS(
	ctx context.Context,
	tx platformpostgres.Tx,
	updateErr error,
	table string,
	idColumn string,
	id string,
	operation string,
) error {
	if updateErr == nil {
		return nil
	}
	if !errors.Is(updateErr, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, updateErr)
	}
	var exists bool
	query := fmt.Sprintf("SELECT true FROM %s WHERE %s=$1", table, idColumn)
	err := tx.QueryRow(ctx, query, id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return platformpostgres.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("classify %s CAS miss: %w", operation, err)
	}
	return platformpostgres.ErrRevisionConflict
}
