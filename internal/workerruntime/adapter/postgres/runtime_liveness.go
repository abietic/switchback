package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func (store *RuntimeHeartbeatStore) EvaluateRuntimeLiveness(
	ctx context.Context,
	record application.EvaluateRuntimeLivenessRecord,
) (application.RuntimeLivenessEvaluation, error) {
	for attempt := 0; attempt < runtimeTurnOutcomeGraphLockAttempts; attempt++ {
		var result application.RuntimeLivenessEvaluation
		err := platformpostgres.InTransaction(
			ctx,
			store.pool,
			func(tx platformpostgres.Tx) error {
				outcomeLock, err := lockRuntimeTurnOutcomeBeforeGraph(
					ctx,
					tx,
					record.Command.RuntimeInstanceID,
				)
				if err != nil {
					return err
				}
				state, found, err := lockRuntimeLiveness(
					ctx,
					tx,
					record.Command.RuntimeInstanceID,
				)
				if err != nil {
					return err
				}
				if err := validateRuntimeTurnOutcomeAfterGraphLock(
					ctx,
					tx,
					record.Command.RuntimeInstanceID,
					outcomeLock,
				); err != nil {
					return err
				}
				if !found ||
					state.TenantID != record.Command.TenantID ||
					state.ActiveRuntimeInstanceID !=
						record.Command.RuntimeInstanceID ||
					state.RuntimeGeneration !=
						record.Command.RuntimeGeneration ||
					state.ChannelEpoch != record.Command.ChannelEpoch ||
					state.HeartbeatSequence !=
						record.Command.HeartbeatSequence ||
					state.ObservationRevision !=
						record.Command.ExpectedObservationRevision ||
					!livenessSessionStatusAllowed(state.SessionStatus) ||
					!livenessRuntimeStatusAllowed(state.RuntimeStatus) {
					return nil
				}
				_, outcomeOwned, err := pendingTurnOutcomeOwnsRuntime(
					ctx,
					tx,
					state,
				)
				if err != nil {
					return err
				}
				if outcomeOwned {
					// A durable Turn outcome source owns terminality and may
					// have frozen the exact Runtime revision in its source
					// proof. Liveness may still observe heartbeats, but must
					// not mutate Runtime state or open competing recovery.
					return nil
				}
				status := application.ClassifyRuntimeLiveness(
					state.LastReceivedAt,
					record.Command.EvaluatedAt,
				)
				if status == application.RuntimeLivenessHealthy ||
					(record.Command.Stage ==
						application.RuntimeLivenessStageUnhealthy &&
						status !=
							application.RuntimeLivenessUnhealthy) {
					return nil
				}
				if status ==
					application.RuntimeLivenessUnhealthy {
					open, err := buildHeartbeatRecoveryCommand(
						ctx,
						tx,
						state,
						record.Command.EvaluatedAt,
					)
					if err != nil {
						return err
					}
					result = application.RuntimeLivenessEvaluation{
						Applied:             true,
						LivenessStatus:      status,
						ObservationRevision: state.ObservationRevision,
						RuntimeRevision:     state.RuntimeRevision,
						OpenRecovery:        &open,
					}
					return nil
				}

				var observationRevision uint64
				if err := tx.QueryRow(ctx, `
UPDATE runtime_heartbeat_observations
SET liveness_status='suspect',revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$2 AND liveness_status='healthy'
RETURNING revision`,
					state.RuntimeInstanceID,
					state.ObservationRevision,
				).Scan(&observationRevision); err != nil {
					return fmt.Errorf(
						"mark Runtime heartbeat suspect: %w",
						err,
					)
				}
				runtimeRevision := state.RuntimeRevision
				if state.RuntimeStatus == "starting" ||
					state.RuntimeStatus == "active" {
					if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='suspect',revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$2 AND status=$3
RETURNING revision`,
						state.RuntimeInstanceID,
						state.RuntimeRevision,
						state.RuntimeStatus,
					).Scan(&runtimeRevision); err != nil {
						return fmt.Errorf(
							"mark Runtime suspect: %w",
							err,
						)
					}
				}
				payload, err := json.Marshal(struct {
					RuntimeGeneration uint64                           `json:"runtime_generation"`
					ChannelEpoch      uint64                           `json:"channel_epoch"`
					HeartbeatSequence uint64                           `json:"heartbeat_sequence"`
					Stage             application.RuntimeLivenessStage `json:"stage"`
				}{
					RuntimeGeneration: state.RuntimeGeneration,
					ChannelEpoch:      state.ChannelEpoch,
					HeartbeatSequence: state.HeartbeatSequence,
					Stage: application.
						RuntimeLivenessStageUnhealthy,
				})
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'runtime_liveness_evaluate','RuntimeHeartbeat',$3,$4,$5,$6,'pending',$7,$8,$8)
ON CONFLICT (tenant_id,command_type,target_type,target_id,idempotency_key) DO NOTHING`,
					record.UnhealthyCommandID,
					state.TenantID,
					state.RuntimeInstanceID,
					observationRevision,
					payload,
					state.UnhealthyDueAt,
					record.UnhealthyIdempotencyKey,
					record.Command.EvaluatedAt,
				); err != nil {
					return fmt.Errorf(
						"schedule unhealthy Runtime evaluation: %w",
						err,
					)
				}
				result = application.RuntimeLivenessEvaluation{
					Applied:             true,
					LivenessStatus:      application.RuntimeLivenessSuspect,
					ObservationRevision: observationRevision,
					RuntimeRevision:     runtimeRevision,
				}
				return nil
			},
		)
		if errors.Is(err, platformpostgres.ErrRevisionConflict) &&
			attempt+1 < runtimeTurnOutcomeGraphLockAttempts {
			continue
		}
		return result, err
	}
	return application.RuntimeLivenessEvaluation{},
		platformpostgres.ErrRevisionConflict
}

func pendingTurnOutcomeOwnsRuntime(
	ctx context.Context,
	tx platformpostgres.Tx,
	state runtimeLivenessRow,
) (
	application.TurnSettlementSourceKind,
	bool,
	error,
) {
	var source string
	err := tx.QueryRow(ctx, `
SELECT settlement.source_kind
FROM worker_turn_settlements settlement
WHERE settlement.tenant_id=$1
  AND settlement.worker_session_id=$2
  AND settlement.phase <> 'completed'
  AND (
    (
      settlement.source_kind='runtime_event'
      AND settlement.runtime_instance_id=$3
      AND settlement.runtime_generation=$4
      AND settlement.channel_epoch=$5
    )
    OR
    (
      settlement.source_kind='backend_deadline'
      AND settlement.source_state->>'runtime_instance_id'=$3
      AND (settlement.source_state->>'runtime_generation')::bigint=$4
      AND (settlement.source_state->>'channel_epoch')::bigint=$5
    )
  )
ORDER BY settlement.accepted_at,settlement.settlement_id
LIMIT 1`,
		state.TenantID,
		state.WorkerSessionID,
		state.RuntimeInstanceID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
	).Scan(&source)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf(
			"check pending Turn outcome Runtime ownership: %w",
			err,
		)
	}
	kind := application.TurnSettlementSourceKind(source)
	switch kind {
	case application.TurnSettlementSourceRuntimeEvent,
		application.TurnSettlementSourceBackendDeadline:
		return kind, true, nil
	default:
		return "", false,
			fmt.Errorf(
				"pending Turn outcome source %q is invalid",
				source,
			)
	}
}

type runtimeLivenessRow struct {
	TenantID                string
	WorkerSessionID         string
	SessionStatus           domain.WorkerSessionStatus
	SessionRevision         uint64
	ActiveRuntimeInstanceID string
	ActiveWorkerRunID       string
	RuntimeInstanceID       string
	RuntimeGeneration       uint64
	ChannelEpoch            uint64
	RuntimeStatus           string
	RuntimeRevision         uint64
	HeartbeatSequence       uint64
	LastReceivedAt          time.Time
	UnhealthyDueAt          time.Time
	ObservationRevision     uint64
}

func lockRuntimeLiveness(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
) (runtimeLivenessRow, bool, error) {
	var state runtimeLivenessRow
	var activeRuntimeID, activeRunID *string
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.worker_session_id,session.status,session.session_revision,
       session.active_runtime_instance_id,session.active_worker_run_id,
       runtime.runtime_instance_id,runtime.generation,runtime.control_channel_epoch,
       runtime.status,runtime.revision,heartbeat.heartbeat_sequence,
       heartbeat.last_received_at,heartbeat.unhealthy_due_at,heartbeat.revision
FROM runtime_heartbeat_observations heartbeat
JOIN runtime_instances runtime ON runtime.runtime_instance_id=heartbeat.runtime_instance_id
JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id
WHERE runtime.runtime_instance_id=$1
FOR UPDATE OF heartbeat,runtime,session`, runtimeInstanceID).Scan(
		&state.TenantID, &state.WorkerSessionID, &state.SessionStatus, &state.SessionRevision,
		&activeRuntimeID, &activeRunID, &state.RuntimeInstanceID, &state.RuntimeGeneration,
		&state.ChannelEpoch, &state.RuntimeStatus, &state.RuntimeRevision,
		&state.HeartbeatSequence, &state.LastReceivedAt, &state.UnhealthyDueAt,
		&state.ObservationRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeLivenessRow{}, false, nil
	}
	if err != nil {
		return runtimeLivenessRow{}, false, fmt.Errorf("lock Runtime liveness: %w", err)
	}
	if activeRuntimeID != nil {
		state.ActiveRuntimeInstanceID = *activeRuntimeID
	}
	if activeRunID != nil {
		state.ActiveWorkerRunID = *activeRunID
	}
	return state, true, nil
}

func buildHeartbeatRecoveryCommand(
	ctx context.Context,
	tx platformpostgres.Tx,
	state runtimeLivenessRow,
	evaluatedAt time.Time,
) (application.OpenRuntimeRecoveryCommand, error) {
	command := application.OpenRuntimeRecoveryCommand{
		TenantID: state.TenantID, WorkerSessionID: state.WorkerSessionID,
		FailedRuntimeInstanceID: state.RuntimeInstanceID,
		FailedGeneration:        state.RuntimeGeneration,
		FailedChannelEpoch:      state.ChannelEpoch,
		Trigger:                 domain.RecoveryTriggerHeartbeatUnhealthy,
		EvidenceRef: "runtime-heartbeat/" + state.RuntimeInstanceID + "/" +
			strconv.FormatUint(state.HeartbeatSequence, 10),
		EvidenceAt:              evaluatedAt,
		ExpectedSessionRevision: state.SessionRevision,
		ExpectedRuntimeRevision: state.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" + state.WorkerSessionID + "/g" +
			strconv.FormatUint(state.RuntimeGeneration, 10) + "/heartbeat_unhealthy",
	}
	if state.ActiveWorkerRunID == "" {
		return command, nil
	}
	var turnID string
	err := tx.QueryRow(ctx, `
SELECT run.worker_run_id,run.revision,turn.agent_turn_id,turn.revision
FROM worker_runs run
JOIN LATERAL (
  SELECT candidate.agent_turn_id,candidate.revision
  FROM agent_turns candidate
  WHERE candidate.worker_session_id=$1 AND candidate.worker_run_id=run.worker_run_id
  ORDER BY candidate.created_at DESC,candidate.agent_turn_id DESC
  LIMIT 1
) turn ON true
WHERE run.worker_run_id=$2 AND run.worker_session_id=$1
FOR UPDATE OF run`, state.WorkerSessionID, state.ActiveWorkerRunID).Scan(
		&command.WorkerRunID, &command.ExpectedRunRevision,
		&turnID, &command.ExpectedTurnRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.OpenRuntimeRecoveryCommand{}, fmt.Errorf(
			"active WorkerRun %s has no recoverable AgentTurn",
			state.ActiveWorkerRunID,
		)
	}
	if err != nil {
		return application.OpenRuntimeRecoveryCommand{}, fmt.Errorf("lock active recovery Run and Turn: %w", err)
	}
	command.AgentTurnID = turnID
	return command, nil
}

func livenessSessionStatusAllowed(status domain.WorkerSessionStatus) bool {
	switch status {
	case domain.SessionInteractive, domain.SessionRunning, domain.SessionWaitingUser, domain.SessionIdle:
		return true
	default:
		return false
	}
}

func livenessRuntimeStatusAllowed(status string) bool {
	return status == "starting" || status == "active" || status == "suspect"
}

var _ application.RuntimeLivenessStore = (*RuntimeHeartbeatStore)(nil)
