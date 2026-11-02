package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type RuntimeResidencyStore struct {
	pool *pgxpool.Pool
}

func NewRuntimeResidencyStore(pool *pgxpool.Pool) *RuntimeResidencyStore {
	return &RuntimeResidencyStore{pool: pool}
}

func (store *RuntimeResidencyStore) ActivateResidentRuntime(
	ctx context.Context,
	record application.ActivateResidentRuntimeRecord,
) (application.ResidentRuntimeState, error) {
	var result application.ResidentRuntimeState
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		row, err := lockResidentRuntime(ctx, tx, record.Command.WorkerSessionID,
			record.Command.WorkerRunID, record.Command.AgentTurnID, record.Command.RuntimeInstanceID)
		if err != nil {
			return err
		}
		if row.TenantID != record.Command.TenantID ||
			row.RuntimeGeneration != record.Command.RuntimeGeneration ||
			row.ControlChannelEpoch != record.Command.ControlChannelEpoch ||
			row.SessionRevision != record.Command.ExpectedSessionRevision ||
			row.RunRevision != record.Command.ExpectedRunRevision ||
			row.TurnRevision != record.Command.ExpectedTurnRevision ||
			row.RuntimeRevision != record.Command.ExpectedRuntimeRevision ||
			row.SessionStatus != domain.SessionRunning || row.RunStatus != domain.RunStarting ||
			row.TurnStatus != domain.TurnDelivered || row.RuntimeStatus != "starting" ||
			row.ContainerID != "" {
			return platformpostgres.ErrRevisionConflict
		}
		if _, err := row.RunStatus.Transition(domain.RunRunning); err != nil {
			return err
		}
		var runtimeRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET container_id=$2,status='active',revision=revision+1,started_at=$3
WHERE runtime_instance_id=$1 AND revision=$4 AND status='starting'
RETURNING revision`, record.Command.RuntimeInstanceID, record.Command.ContainerID,
			record.Now, record.Command.ExpectedRuntimeRevision).Scan(&runtimeRevision); err != nil {
			return fmt.Errorf("activate resident Runtime: %w", err)
		}
		var runRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE worker_runs
SET status='running',revision=revision+1,started_at=$2
WHERE worker_run_id=$1 AND revision=$3 AND status='starting'
RETURNING revision`, record.Command.WorkerRunID, record.Now,
			record.Command.ExpectedRunRevision).Scan(&runRevision); err != nil {
			return fmt.Errorf("activate resident WorkerRun: %w", err)
		}
		var sessionRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND session_revision=$4 AND status='running'
  AND active_worker_run_id=$5 AND active_runtime_instance_id=$2
RETURNING session_revision`, record.Command.WorkerSessionID, record.Command.RuntimeInstanceID,
			record.Now, record.Command.ExpectedSessionRevision, record.Command.WorkerRunID).Scan(&sessionRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("advance resident Runtime ownership: %w", err)
		}
		row.ContainerID = record.Command.ContainerID
		row.RuntimeStatus = "active"
		row.RuntimeRevision = runtimeRevision
		row.RunStatus = domain.RunRunning
		row.RunRevision = runRevision
		row.SessionRevision = sessionRevision
		result = row.state()
		return nil
	})
	return result, err
}

func (store *RuntimeResidencyStore) CompleteResidentStage(
	ctx context.Context,
	record application.CompleteResidentStageRecord,
) (application.ResidentRuntimeState, error) {
	var result application.ResidentRuntimeState
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var err error
		result, err = completeResidentStageTx(ctx, tx, record)
		return err
	})
	return result, err
}

func completeResidentStageTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.CompleteResidentStageRecord,
) (application.ResidentRuntimeState, error) {
	row, err := lockResidentRuntime(
		ctx,
		tx,
		record.Command.WorkerSessionID,
		record.Command.WorkerRunID,
		record.Command.AgentTurnID,
		record.Command.RuntimeInstanceID,
	)
	if err != nil {
		return application.ResidentRuntimeState{}, err
	}
	return completeResidentStageWithLockedRuntimeTx(
		ctx,
		tx,
		row,
		record,
		false,
	)
}

func completeResidentStageWithLockedRuntimeTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	row residentRuntimeRow,
	record application.CompleteResidentStageRecord,
	allowSuspectRuntime bool,
) (application.ResidentRuntimeState, error) {
	runtimeStatusAllowed := row.RuntimeStatus == "active" ||
		(allowSuspectRuntime && row.RuntimeStatus == "suspect")
	if row.TenantID != record.Command.TenantID ||
		row.RuntimeGeneration != record.Command.RuntimeGeneration ||
		row.SessionRevision != record.Command.ExpectedSessionRevision ||
		row.RunRevision != record.Command.ExpectedRunRevision ||
		row.TurnRevision != record.Command.ExpectedTurnRevision ||
		row.RuntimeRevision != record.Command.ExpectedRuntimeRevision ||
		row.SessionStatus != domain.SessionRunning ||
		row.RunStatus != domain.RunRunning ||
		(row.TurnStatus != domain.TurnDelivered &&
			row.TurnStatus != domain.TurnConsumed) ||
		!runtimeStatusAllowed ||
		row.ContainerID == "" {
		return application.ResidentRuntimeState{},
			platformpostgres.ErrRevisionConflict
	}
	if _, err := row.SessionStatus.Transition(
		record.Command.SessionTarget,
	); err != nil {
		return application.ResidentRuntimeState{}, err
	}
	if _, err := row.RunStatus.Transition(
		record.Command.RunTarget,
	); err != nil {
		return application.ResidentRuntimeState{}, err
	}
	if _, err := row.TurnStatus.Transition(
		record.Command.TurnTarget,
	); err != nil {
		return application.ResidentRuntimeState{}, err
	}
	var turnRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE agent_turns
SET status=$2,revision=revision+1,terminal_at=$3,
    terminal_reason=CASE WHEN $2 IN ('cancelled','timed_out','failed') THEN NULLIF($4,'') ELSE NULL END
WHERE agent_turn_id=$1 AND revision=$5 AND status=$6
RETURNING revision`, record.Command.AgentTurnID, record.Command.TurnTarget, record.Now,
		record.Command.TerminalReason,
		record.Command.ExpectedTurnRevision,
		row.TurnStatus,
	).Scan(&turnRevision); err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf("complete resident AgentTurn: %w", err)
	}
	var runRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE worker_runs
SET status=$2,revision=revision+1,
    terminal_at=CASE WHEN $2 IN ('completed','failed','cancelled','timed_out') THEN $3::timestamptz ELSE NULL END,
    terminal_reason=CASE WHEN $2 IN ('failed','cancelled','timed_out') THEN NULLIF($4,'') ELSE NULL END
WHERE worker_run_id=$1 AND revision=$5 AND status='running'
RETURNING revision`, record.Command.WorkerRunID, record.Command.RunTarget, record.Now,
		record.Command.TerminalReason,
		record.Command.ExpectedRunRevision,
	).Scan(&runRevision); err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf("complete resident WorkerRun stage: %w", err)
	}
	var sessionRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status=$2,session_revision=session_revision+1,
    active_worker_run_id=CASE WHEN $3 THEN NULL ELSE active_worker_run_id END,
    updated_at=$4
WHERE worker_session_id=$1 AND session_revision=$5 AND status='running'
  AND active_worker_run_id=$6
RETURNING session_revision`, record.Command.WorkerSessionID, record.Command.SessionTarget,
		record.Command.RunTarget.IsTerminal(),
		record.Now,
		record.Command.ExpectedSessionRevision,
		record.Command.WorkerRunID,
	).Scan(&sessionRevision); err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf("keep WorkerSession resident after stage: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands
SET status='cancelled',updated_at=$2
WHERE target_type='AgentTurn' AND target_id=$1 AND status='pending'`,
		record.Command.AgentTurnID,
		record.Now,
	); err != nil {
		return application.ResidentRuntimeState{},
			fmt.Errorf("cancel completed AgentTurn deadline: %w", err)
	}
	row.SessionStatus = record.Command.SessionTarget
	row.RunStatus = record.Command.RunTarget
	row.TurnStatus = record.Command.TurnTarget
	row.SessionRevision = sessionRevision
	row.RunRevision = runRevision
	row.TurnRevision = turnRevision
	return row.state(), nil
}

type residentRuntimeRow struct {
	TenantID            string
	WorkerSessionID     string
	WorkerRunID         string
	AgentTurnID         string
	RuntimeInstanceID   string
	ContainerID         string
	RuntimeGeneration   uint64
	ControlChannelEpoch uint64
	SessionStatus       domain.WorkerSessionStatus
	RunStatus           domain.WorkerRunStatus
	TurnStatus          domain.AgentTurnStatus
	RuntimeStatus       string
	SessionRevision     uint64
	RunRevision         uint64
	TurnRevision        uint64
	RuntimeRevision     uint64
}

func lockResidentRuntime(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	runID string,
	turnID string,
	runtimeID string,
) (residentRuntimeRow, error) {
	var row residentRuntimeRow
	var containerID *string
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.worker_session_id,run.worker_run_id,turn.agent_turn_id,
  runtime.runtime_instance_id,runtime.container_id,runtime.generation,runtime.control_channel_epoch,
  session.status,run.status,turn.status,runtime.status,
  session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN agent_turns turn ON turn.worker_session_id=session.worker_session_id
  AND turn.worker_run_id=run.worker_run_id AND turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.worker_session_id=session.worker_session_id
  AND runtime.worker_run_id=run.worker_run_id AND runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1
FOR UPDATE OF session,run,turn,runtime`, sessionID, runID, turnID, runtimeID).Scan(
		&row.TenantID, &row.WorkerSessionID, &row.WorkerRunID, &row.AgentTurnID,
		&row.RuntimeInstanceID, &containerID, &row.RuntimeGeneration, &row.ControlChannelEpoch,
		&row.SessionStatus, &row.RunStatus, &row.TurnStatus, &row.RuntimeStatus,
		&row.SessionRevision, &row.RunRevision, &row.TurnRevision, &row.RuntimeRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return residentRuntimeRow{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return residentRuntimeRow{}, fmt.Errorf("lock resident Runtime ownership: %w", err)
	}
	if containerID != nil {
		row.ContainerID = *containerID
	}
	return row, nil
}

func (row residentRuntimeRow) state() application.ResidentRuntimeState {
	return application.ResidentRuntimeState{
		WorkerSessionID: row.WorkerSessionID, WorkerRunID: row.WorkerRunID,
		AgentTurnID: row.AgentTurnID, RuntimeInstanceID: row.RuntimeInstanceID,
		ContainerID: row.ContainerID, RuntimeGeneration: row.RuntimeGeneration,
		SessionStatus: row.SessionStatus, RunStatus: row.RunStatus,
		TurnStatus: row.TurnStatus, RuntimeStatus: row.RuntimeStatus,
		SessionRevision: row.SessionRevision, RunRevision: row.RunRevision,
		TurnRevision: row.TurnRevision, RuntimeRevision: row.RuntimeRevision,
	}
}

var _ application.RuntimeResidencyStore = (*RuntimeResidencyStore)(nil)
