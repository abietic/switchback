package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5"
)

// ReadRuntimeFailureCancellation is a pure SELECT on the caller's transaction.
// It resolves only the immutable disabled-failure outcome for this exact Run;
// a malformed matching outcome is a conflict, not permission to interrupt.
func ReadRuntimeFailureCancellation(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant, session, run string) (application.RuntimeRecoveryState, bool, error) {
	var original application.RuntimeRecoveryState
	var encoded []byte
	err := query.QueryRow(ctx, `SELECT saved.result
FROM runtime_recovery_incidents incident
LEFT JOIN runtime_recovery_idempotency_results saved ON saved.incident_id=incident.incident_id AND saved.command_kind='exhaust'
WHERE incident.worker_session_id=$1 AND (incident.worker_run_id=$2 OR incident.worker_run_id IS NULL)
 AND incident.phase='exhausted' AND incident.attempt_count=0 AND incident.outcome_reason='runtime_recovery_disabled'
ORDER BY incident.opened_at DESC LIMIT 1`, session, run).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return original, false, nil
	}
	if err != nil {
		return original, false, err
	}
	if err := json.Unmarshal(encoded, &original); err != nil {
		return original, false, err
	}
	var valid bool
	if original.WorkerRunID == "" {
		err = query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_recovery_incidents incident JOIN runtime_instances runtime ON runtime.runtime_instance_id=incident.failed_runtime_instance_id JOIN worker_runs run ON run.worker_run_id=$4 WHERE incident.incident_id=$1 AND incident.tenant_id=$2 AND incident.worker_session_id=$3 AND incident.worker_run_id IS NULL AND incident.agent_turn_id IS NULL AND incident.failed_runtime_instance_id=$5 AND incident.failed_generation=$6 AND incident.failed_channel_epoch=$7 AND incident.revision=$8 AND runtime.worker_session_id=$3 AND runtime.generation=$6 AND runtime.control_channel_epoch=$7 AND run.worker_session_id=$3 AND run.status IN ('completed','failed','cancelled','timed_out'))`, original.IncidentID, tenant, session, run, original.FailedRuntimeInstanceID, original.FailedGeneration, original.FailedChannelEpoch, original.IncidentRevision).Scan(&valid)
		if err != nil {
			return original, false, err
		}
		if !valid || original.TenantID != tenant || original.WorkerSessionID != session || original.AgentTurnID != "" || original.SessionRevision == 0 || original.FailedRuntimeRevision == 0 {
			return original, false, platformpostgres.ErrRevisionConflict
		}
		return original, true, nil
	}
	err = query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_recovery_incidents incident
JOIN worker_runs run ON run.worker_run_id=incident.worker_run_id
JOIN agent_turns turn ON turn.agent_turn_id=incident.agent_turn_id
JOIN runtime_instances runtime ON runtime.runtime_instance_id=incident.failed_runtime_instance_id
WHERE incident.incident_id=$1 AND incident.tenant_id=$2 AND incident.worker_session_id=$3 AND incident.worker_run_id=$4
 AND incident.agent_turn_id=$5 AND incident.failed_runtime_instance_id=$6 AND incident.failed_generation=$7
 AND incident.failed_channel_epoch=$8 AND incident.revision=$9
 AND run.worker_session_id=$3 AND turn.worker_session_id=$3 AND turn.worker_run_id=$4
 AND runtime.worker_session_id=$3 AND runtime.worker_run_id=$4 AND runtime.generation=$7 AND runtime.control_channel_epoch=$8
 AND turn.status='failed' AND turn.terminal_reason='runtime_recovery_failed' AND turn.revision=$10
 AND turn.agent_turn_id=(SELECT latest.agent_turn_id FROM agent_turns latest WHERE latest.worker_run_id=$4 ORDER BY latest.created_at DESC,latest.agent_turn_id DESC LIMIT 1))`, original.IncidentID, tenant, session, run, original.AgentTurnID, original.FailedRuntimeInstanceID, original.FailedGeneration, original.FailedChannelEpoch, original.IncidentRevision, original.TurnRevision).Scan(&valid)
	if err != nil {
		return original, false, err
	}
	if !valid || original.TenantID != tenant || original.WorkerSessionID != session || original.WorkerRunID != run || original.EventID == "" || original.RunRevision == 0 || original.SessionRevision == 0 || original.FailedRuntimeRevision == 0 {
		return original, false, platformpostgres.ErrRevisionConflict
	}
	return original, true, nil
}

// RuntimeFailureCancellationLock is an unforgeable owner token for the caller's
// transaction. Task owner must acquire it BEFORE locking any mutable graph row.
// Nil means the existing resident/deadline cancellation path applies. Matching
// failure with missing proof or DB errors must never fall back to that path.
type RuntimeFailureCancellationLock struct {
	original application.RuntimeRecoveryState
	tx       platformpostgres.Tx
}

func LockRuntimeFailureCancellation(ctx context.Context, tx platformpostgres.Tx, command application.RuntimeFailureCancellationCommand) (*RuntimeFailureCancellationLock, error) {
	original, found, err := ReadRuntimeFailureCancellation(ctx, tx, command.TenantID, command.WorkerSessionID, command.WorkerRunID)
	if err != nil || !found {
		return nil, err
	}
	if original.AgentTurnID != "" {
		if err := lockAgentTurnOutcome(ctx, tx, original.AgentTurnID); err != nil {
			return nil, err
		}
	}
	return &RuntimeFailureCancellationLock{original: original, tx: tx}, nil
}

// Release runs entirely inside Task owner's transaction. It revalidates actual
// initiator, immutable failure, current graph and committed cleanup, then writes
// only Worker-owned fields. Caller persists the Task owner receipt/event in the
// SAME transaction; its failure rolls these writes back. This does not settle a
// Turn, change Session lifecycle/quota, or grant execution. Replays are read from
// the Task owner's frozen receipt before invoking Release again.
func (locked *RuntimeFailureCancellationLock) Release(ctx context.Context, command application.RuntimeFailureCancellationCommand, now time.Time) (application.RuntimeFailureCancellationResult, error) {
	var result application.RuntimeFailureCancellationResult
	tx := locked.tx
	original := locked.original
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT tenant_id=$2 AND initiator_user_id=$3 AND worker_session_id=$4 AND current_worker_run_id=$5 AND status='cancelled' AND revision=$6 FROM tasks WHERE task_id=$1 FOR UPDATE`, command.TaskID, command.TenantID, command.UserID, command.WorkerSessionID, command.WorkerRunID, command.ExpectedTaskRevision).Scan(&valid); err != nil {
		return result, err
	}
	if !valid {
		return result, platformpostgres.ErrRevisionConflict
	}
	var row residentRuntimeRow
	var err error
	if original.WorkerRunID == "" {
		row, _, err = lockIdleRecoveryRuntime(ctx, tx, command.WorkerSessionID, original.FailedRuntimeInstanceID)
	} else {
		row, err = lockResidentRuntime(ctx, tx, command.WorkerSessionID, command.WorkerRunID, original.AgentTurnID, original.FailedRuntimeInstanceID)
	}
	if err != nil {
		return result, err
	}
	current, found, err := ReadRuntimeFailureCancellation(ctx, tx, command.TenantID, command.WorkerSessionID, command.WorkerRunID)
	if err != nil {
		return result, err
	}
	if !found || current.IncidentID != original.IncidentID || current.EventID != original.EventID || current.AgentTurnID != original.AgentTurnID || current.IncidentRevision != original.IncidentRevision {
		return result, platformpostgres.ErrRevisionConflict
	}
	if original.AgentTurnID != "" {
		if err := assertNoCompetingTurnOutcomeAuthority(ctx, tx, original.AgentTurnID); err != nil {
			return result, err
		}
	}
	if err := tx.QueryRow(ctx, `SELECT session.tenant_id=$2 AND session.bound_task_id=$3 AND COALESCE(session.active_worker_run_id,'')=$7 AND session.runtime_generation=$5 AND run.task_id=$3 AND (session.active_runtime_instance_id IS NULL OR session.active_runtime_instance_id=$6) FROM worker_sessions session JOIN worker_runs run ON run.worker_run_id=$4 WHERE session.worker_session_id=$1`, command.WorkerSessionID, command.TenantID, command.TaskID, command.WorkerRunID, original.FailedGeneration, original.FailedRuntimeInstanceID, original.WorkerRunID).Scan(&valid); err != nil {
		return result, err
	}
	if !valid || row.SessionRevision != command.ExpectedSessionRevision || row.SessionStatus != domain.SessionWaitingUser || original.WorkerRunID != "" && (row.RunStatus != domain.RunWaitingUser || row.RunRevision != original.RunRevision || row.TurnRevision != original.TurnRevision) {
		return result, platformpostgres.ErrRevisionConflict
	}
	proven, err := ReadConfirmedRuntimeCleanup(ctx, tx, ConfirmedRuntimeCleanupIdentity{TenantID: command.TenantID, WorkerSessionID: command.WorkerSessionID, RuntimeInstanceID: original.FailedRuntimeInstanceID, Generation: original.FailedGeneration, ChannelEpoch: original.FailedChannelEpoch, FailedRuntimeRevision: original.FailedRuntimeRevision, FailedSessionRevision: original.SessionRevision, CurrentSessionRevision: row.SessionRevision})
	if err != nil {
		return result, err
	}
	if !proven {
		return result, application.ErrRuntimeFailureCleanupPending
	}
	if original.WorkerRunID != "" {
		if _, err := row.RunStatus.Transition(domain.RunCancelled); err != nil {
			return result, err
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_runs SET status='cancelled',revision=revision+1,terminal_at=$2,terminal_reason='task_cancelled' WHERE worker_run_id=$1`, command.WorkerRunID, now); err != nil {
			return result, err
		}
	}
	if err := tx.QueryRow(ctx, `UPDATE worker_sessions SET bound_task_id=NULL,active_worker_run_id=NULL,session_revision=session_revision+1,updated_at=$2 WHERE worker_session_id=$1 AND active_runtime_instance_id IS NULL RETURNING session_revision`, command.WorkerSessionID, now).Scan(&result.SessionRevision); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `UPDATE session_dispatch_permits SET status='revoked' WHERE worker_session_id=$1 AND status='active'`, command.WorkerSessionID); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `UPDATE worker_user_commands SET status='rejected',outcome=outcome || '{"code":"task_cancelled"}'::jsonb,updated_at=$2 WHERE worker_session_id=$1 AND command_type='queue' AND status='accepted'`, command.WorkerSessionID, now); err != nil {
		return result, err
	}
	return result, nil
}
