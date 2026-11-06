package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

const (
	CommandAgentTurnDeadline     = "agent_turn_deadline"
	CommandWorkerSessionIdle     = "worker_session_idle_expiry"
	CommandWorkerSessionAbsolute = "worker_session_absolute_expiry"
)

type LifecycleCommandHandler struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

func (handler LifecycleCommandHandler) HandleScheduledCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if handler.Pool == nil || handler.Now == nil {
		return fmt.Errorf("lifecycle command handler dependencies are required")
	}
	switch command.CommandType {
	case CommandAgentTurnDeadline:
		return handler.timeoutAgentTurn(ctx, command)
	case CommandWorkerSessionIdle:
		return handler.expireIdleSession(ctx, command)
	case CommandWorkerSessionAbsolute:
		return handler.expireAbsoluteSession(ctx, command)
	default:
		return fmt.Errorf("unsupported lifecycle command %q", command.CommandType)
	}
}

func (handler LifecycleCommandHandler) timeoutAgentTurn(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.TargetType != "AgentTurn" {
		return fmt.Errorf("deadline target must be AgentTurn")
	}
	now := handler.Now().UTC()
	return inTerminalCapacityTransaction(ctx, handler.Pool, func() string { return command.TargetID }, func(tx platformpostgres.Tx) error {
		if err := lockAgentTurnOutcome(ctx, tx, command.TargetID); err != nil {
			return err
		}
		var tenantID, sessionStatus, status string
		var revision uint64
		var deadlineAt *time.Time
		err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.status,turn.status,turn.revision,turn.deadline_at
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
WHERE turn.agent_turn_id=$1 FOR UPDATE OF turn`, command.TargetID).Scan(
			&tenantID, &sessionStatus, &status, &revision, &deadlineAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if tenantID != command.TenantID || revision < command.ExpectedRevision ||
			deadlineAt == nil || !command.DueAt.Equal(*deadlineAt) ||
			now.Before(*deadlineAt) || isTurnTerminal(status) {
			return nil
		}
		if sessionStatus == "recovering" {
			// RuntimeRecovery owns the cross-aggregate preemption. The incident
			// retains the original deadline and its reconciler atomically settles
			// the Turn, Run, Session and Task after this command is consumed.
			return nil
		}
		if status != "delivered" && status != "consumed" {
			return nil
		}
		tag, err := tx.Exec(ctx, `
UPDATE agent_turns
SET status='timed_out',revision=revision+1,terminal_at=$2,terminal_reason='agent_turn_deadline'
WHERE agent_turn_id=$1 AND revision=$3`, command.TargetID, now, revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return platformpostgres.ErrRevisionConflict
		}
		return nil
	})
}

func (handler LifecycleCommandHandler) expireIdleSession(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.TargetType != "WorkerSession" {
		return fmt.Errorf("idle expiry target must be WorkerSession")
	}
	now := handler.Now().UTC()
	return platformpostgres.InTransaction(ctx, handler.Pool, func(tx platformpostgres.Tx) error {
		session, err := lockLifecycleSession(ctx, tx, command.TargetID)
		if errors.Is(err, platformpostgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if session.TenantID != command.TenantID || !command.DueAt.Equal(session.IdleExpiresAt) ||
			now.Before(session.IdleExpiresAt) || session.Status == "waiting_user" ||
			session.Status == "closed" || session.Status == "expired" {
			return nil
		}
		if session.ActiveRunID != "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `
UPDATE worker_sessions
SET status='closed',session_revision=session_revision+1,terminal_at=$2,
    terminal_reason='worker_session_idle_expired',updated_at=$2
WHERE worker_session_id=$1 AND session_revision=$3`, command.TargetID, now, command.ExpectedRevision); err != nil {
			return err
		}
		return releaseContinuitySlot(ctx, tx, session, now)
	})
}

func (handler LifecycleCommandHandler) expireAbsoluteSession(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.TargetType != "WorkerSession" {
		return fmt.Errorf("absolute expiry target must be WorkerSession")
	}
	now := handler.Now().UTC()
	return InCapacityTransaction(ctx, handler.Pool, func(tx pgx.Tx, capacity *CapacityTx) error {
		task, err := lockRuntimeTaskBeforeGraph(ctx, tx, command.TargetID)
		if errors.Is(err, platformpostgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		session, err := lockLifecycleSession(ctx, tx, command.TargetID)
		if errors.Is(err, platformpostgres.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := task.revalidate(ctx, tx, command.TargetID); err != nil {
			return err
		}
		if session.TenantID != command.TenantID || now.Before(session.AbsoluteExpiresAt) ||
			session.Status == "closed" || session.Status == "expired" {
			return nil
		}
		if session.Status == "recovering" {
			// RuntimeRecovery owns the cross-aggregate preemption so the incident,
			// Turn, Run, Task and Session close in one transaction. The recovery
			// reconciler observes the same immutable absolute_expires_at value.
			return nil
		}
		if err := rejectUnprovenInitialAbsoluteExpiry(ctx, tx, command.TargetID); err != nil {
			return err
		}
		if session.ActiveRunID != "" {
			if _, err := tx.Exec(ctx, `
UPDATE worker_runs
SET status='timed_out',revision=revision+1,terminal_at=$2,
    terminal_reason='worker_session_absolute_expired'
WHERE worker_run_id=$1 AND status IN ('starting','running','waiting_user')`,
				session.ActiveRunID, now); err != nil {
				return err
			}
		}
		if session.BoundTaskID != "" {
			if _, err := tx.Exec(ctx, `
UPDATE tasks
SET status=CASE WHEN status='delivered' THEN status
                WHEN status IN ('completed','failed','cancelled') THEN status
                ELSE 'waiting_user' END,
    waiting_reason=CASE WHEN status='delivered' OR status IN ('completed','failed','cancelled')
                        THEN waiting_reason
                        ELSE '{"code":"worker_session_expired"}'::jsonb END,
    current_worker_run_id=NULL,worker_session_id=NULL,revision=revision+1,updated_at=$2
WHERE task_id=$1`, session.BoundTaskID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE worker_sessions
SET status='expired',bound_task_id=NULL,active_worker_run_id=NULL,
    session_revision=session_revision+1,terminal_at=$2,
    terminal_reason='worker_session_absolute_expired',updated_at=$2
WHERE worker_session_id=$1`, command.TargetID, now); err != nil {
			return err
		}
		return releaseContinuitySlot(ctx, tx, session, now)
	})
}

type lifecycleSession struct {
	TenantID          string
	UserID            string
	Status            string
	Revision          uint64
	ActiveRunID       string
	BoundTaskID       string
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

func lockLifecycleSession(ctx context.Context, tx platformpostgres.Tx, sessionID string) (lifecycleSession, error) {
	var session lifecycleSession
	var activeRunID, boundTaskID *string
	err := tx.QueryRow(ctx, `
SELECT tenant_id,user_id,status,session_revision,active_worker_run_id,bound_task_id,
  idle_expires_at,absolute_expires_at
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, sessionID).Scan(
		&session.TenantID, &session.UserID, &session.Status, &session.Revision,
		&activeRunID, &boundTaskID, &session.IdleExpiresAt, &session.AbsoluteExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycleSession{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return lifecycleSession{}, err
	}
	if activeRunID != nil {
		session.ActiveRunID = *activeRunID
	}
	if boundTaskID != nil {
		session.BoundTaskID = *boundTaskID
	}
	return session, nil
}

func releaseContinuitySlot(
	ctx context.Context,
	tx platformpostgres.Tx,
	session lifecycleSession,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `
UPDATE worker_session_continuity_quotas
SET reserved_count=reserved_count-1,revision=revision+1,updated_at=$3
WHERE tenant_id=$1 AND user_id=$2 AND reserved_count > 0`, session.TenantID, session.UserID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("release WorkerSession continuity slot: %w", platformpostgres.ErrRevisionConflict)
	}
	return nil
}

func isTurnTerminal(status string) bool {
	return status == "completed" || status == "cancelled" || status == "timed_out" || status == "failed"
}
