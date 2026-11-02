package localproduct

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

var errBackendDeadlineClaimRetryable = errors.New(
	"Backend deadline claim is awaiting its durable Runtime graph",
)

var errBackendDeadlineClaimCommandConflict = errors.New(
	"Backend deadline ScheduledCommand conflicts with its durable target",
)

type backendDeadlineClaimHandler struct {
	Pool   *pgxpool.Pool
	Source workerapp.TurnSettlementSourceService
}

func (handler backendDeadlineClaimHandler) HandleScheduledCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if handler.Pool == nil || handler.Source.Store == nil ||
		handler.Source.Now == nil {
		return fmt.Errorf(
			"Backend deadline claim handler dependencies are required",
		)
	}
	if command.ID == "" ||
		command.CommandType != workerpostgres.CommandAgentTurnDeadline ||
		command.TargetType != "AgentTurn" ||
		command.TargetID == "" ||
		command.TenantID == "" ||
		command.ExpectedRevision == 0 ||
		command.DueAt.IsZero() {
		return errBackendDeadlineClaimCommandConflict
	}
	target, err := loadBackendDeadlineClaimTarget(
		ctx,
		handler.Pool,
		command,
	)
	if err != nil {
		return err
	}
	if err := target.validateCommand(command); err != nil {
		return err
	}
	if workerdomain.AgentTurnStatus(target.TurnStatus).IsTerminal() {
		return nil
	}
	if err := target.validateRuntimeGraph(); err != nil {
		return err
	}
	_, err = handler.Source.ClaimBackendDeadline(
		ctx,
		workerapp.ClaimBackendDeadlineCommand{
			TenantID:          target.TenantID,
			WorkerSessionID:   target.WorkerSessionID,
			WorkerRunID:       target.WorkerRunID,
			AgentSessionID:    target.AgentSessionID,
			AgentTurnID:       target.AgentTurnID,
			RuntimeInstanceID: target.RuntimeInstanceID,
			RuntimeGeneration: target.RuntimeGeneration,
			ChannelEpoch:      target.ChannelEpoch,
			DeadlineAt:        target.DeadlineAt,
			SourceRef:         command.ID,
		},
	)
	if errors.Is(err, workerapp.ErrTurnOutcomeAuthorityClaimed) {
		return nil
	}
	if errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		terminal, readErr := backendDeadlineClaimTurnTerminal(
			ctx,
			handler.Pool,
			command.TargetID,
		)
		if readErr == nil && terminal {
			return nil
		}
		if readErr != nil {
			return errors.Join(err, readErr)
		}
	}
	return err
}

type backendDeadlineClaimTarget struct {
	TenantID                 string
	WorkerSessionID          string
	ActiveWorkerRunID        string
	ActiveRuntimeInstanceID  string
	SessionRuntimeGeneration uint64
	WorkerRunID              string
	AgentSessionID           string
	AgentTurnID              string
	TurnStatus               string
	TurnRevision             uint64
	DeadlineAt               time.Time
	RuntimeInstanceID        string
	RuntimeStatus            string
	RuntimeGeneration        uint64
	ChannelEpoch             uint64
	ScheduledCommandID       string
	ScheduledTenantID        string
	ScheduledCommandType     string
	ScheduledTargetType      string
	ScheduledTargetID        string
	ScheduledExpected        uint64
	ScheduledDueAt           time.Time
}

func loadBackendDeadlineClaimTarget(
	ctx context.Context,
	pool *pgxpool.Pool,
	command platformpostgres.ScheduledCommand,
) (backendDeadlineClaimTarget, error) {
	var target backendDeadlineClaimTarget
	var deadlineAt, scheduledDueAt *time.Time
	err := pool.QueryRow(ctx, `
SELECT
  COALESCE(session.tenant_id,''),
  COALESCE(session.worker_session_id,''),
  COALESCE(session.active_worker_run_id,''),
  COALESCE(session.active_runtime_instance_id,''),
  COALESCE(session.runtime_generation,0),
  COALESCE(run.worker_run_id,''),
  turn.agent_session_id,
  turn.agent_turn_id,
  turn.status,
  turn.revision,
  turn.deadline_at,
  COALESCE(runtime.runtime_instance_id,''),
  COALESCE(runtime.status,''),
  COALESCE(runtime.generation,0),
  COALESCE(runtime.control_channel_epoch,0),
  COALESCE(scheduled.scheduled_command_id,''),
  COALESCE(scheduled.tenant_id,''),
  COALESCE(scheduled.command_type,''),
  COALESCE(scheduled.target_type,''),
  COALESCE(scheduled.target_id,''),
  COALESCE(scheduled.expected_revision,0),
  scheduled.due_at
FROM agent_turns turn
LEFT JOIN worker_sessions session
  ON session.worker_session_id=turn.worker_session_id
LEFT JOIN worker_runs run
  ON run.worker_run_id=turn.worker_run_id
 AND run.worker_session_id=turn.worker_session_id
LEFT JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=session.active_runtime_instance_id
 AND runtime.worker_session_id=turn.worker_session_id
 AND runtime.worker_run_id=turn.worker_run_id
LEFT JOIN scheduled_commands scheduled
  ON scheduled.scheduled_command_id=$2
WHERE turn.agent_turn_id=$1`,
		command.TargetID,
		command.ID,
	).Scan(
		&target.TenantID,
		&target.WorkerSessionID,
		&target.ActiveWorkerRunID,
		&target.ActiveRuntimeInstanceID,
		&target.SessionRuntimeGeneration,
		&target.WorkerRunID,
		&target.AgentSessionID,
		&target.AgentTurnID,
		&target.TurnStatus,
		&target.TurnRevision,
		&deadlineAt,
		&target.RuntimeInstanceID,
		&target.RuntimeStatus,
		&target.RuntimeGeneration,
		&target.ChannelEpoch,
		&target.ScheduledCommandID,
		&target.ScheduledTenantID,
		&target.ScheduledCommandType,
		&target.ScheduledTargetType,
		&target.ScheduledTargetID,
		&target.ScheduledExpected,
		&scheduledDueAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return backendDeadlineClaimTarget{}, fmt.Errorf(
			"%w: AgentTurn %s is not readable",
			errBackendDeadlineClaimRetryable,
			command.TargetID,
		)
	}
	if err != nil {
		return backendDeadlineClaimTarget{}, fmt.Errorf(
			"load Backend deadline claim target: %w",
			err,
		)
	}
	if deadlineAt != nil {
		target.DeadlineAt = deadlineAt.UTC()
	}
	if scheduledDueAt != nil {
		target.ScheduledDueAt = scheduledDueAt.UTC()
	}
	return target, nil
}

func (target backendDeadlineClaimTarget) validateCommand(
	command platformpostgres.ScheduledCommand,
) error {
	if target.ScheduledCommandID != command.ID ||
		target.ScheduledTenantID != command.TenantID ||
		target.ScheduledCommandType != command.CommandType ||
		target.ScheduledTargetType != command.TargetType ||
		target.ScheduledTargetID != command.TargetID ||
		target.ScheduledExpected != command.ExpectedRevision ||
		target.TenantID != command.TenantID ||
		target.AgentTurnID != command.TargetID ||
		target.TurnRevision < command.ExpectedRevision ||
		target.DeadlineAt.IsZero() ||
		target.ScheduledDueAt.IsZero() ||
		!target.DeadlineAt.Equal(command.DueAt) ||
		!target.ScheduledDueAt.Equal(command.DueAt) {
		return errBackendDeadlineClaimCommandConflict
	}
	return nil
}

func (target backendDeadlineClaimTarget) validateRuntimeGraph() error {
	if target.WorkerSessionID == "" ||
		target.WorkerRunID == "" ||
		target.AgentSessionID == "" ||
		target.ActiveWorkerRunID != target.WorkerRunID ||
		target.ActiveRuntimeInstanceID == "" ||
		target.RuntimeInstanceID != target.ActiveRuntimeInstanceID ||
		target.RuntimeStatus == "" ||
		target.RuntimeGeneration == 0 ||
		target.SessionRuntimeGeneration != target.RuntimeGeneration ||
		target.ChannelEpoch == 0 {
		return fmt.Errorf(
			"%w: AgentTurn %s has no exact current Runtime",
			errBackendDeadlineClaimRetryable,
			target.AgentTurnID,
		)
	}
	return nil
}

func backendDeadlineClaimTurnTerminal(
	ctx context.Context,
	pool *pgxpool.Pool,
	agentTurnID string,
) (bool, error) {
	var status string
	err := pool.QueryRow(ctx, `
SELECT status
FROM agent_turns
WHERE agent_turn_id=$1`,
		agentTurnID,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf(
			"%w: AgentTurn %s is not readable",
			errBackendDeadlineClaimRetryable,
			agentTurnID,
		)
	}
	if err != nil {
		return false, fmt.Errorf(
			"reload Backend deadline AgentTurn status: %w",
			err,
		)
	}
	return workerdomain.AgentTurnStatus(status).IsTerminal(), nil
}

var _ platformpostgres.ScheduledCommandHandler = backendDeadlineClaimHandler{}
