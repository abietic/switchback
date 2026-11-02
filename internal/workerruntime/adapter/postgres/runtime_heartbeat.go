package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type RuntimeHeartbeatStore struct {
	pool *pgxpool.Pool
}

func NewRuntimeHeartbeatStore(pool *pgxpool.Pool) *RuntimeHeartbeatStore {
	return &RuntimeHeartbeatStore{pool: pool}
}

func (store *RuntimeHeartbeatStore) RecordRuntimeHeartbeat(
	ctx context.Context,
	record application.RecordRuntimeHeartbeat,
) (application.RuntimeHeartbeatState, error) {
	for attempt := 0; attempt < runtimeTurnOutcomeGraphLockAttempts; attempt++ {
		var result application.RuntimeHeartbeatState
		err := platformpostgres.InTransaction(
			ctx,
			store.pool,
			func(tx platformpostgres.Tx) error {
				var err error
				result, err = recordRuntimeHeartbeatTx(
					ctx,
					tx,
					record,
				)
				return err
			},
		)
		if errors.Is(err, platformpostgres.ErrRevisionConflict) &&
			attempt+1 < runtimeTurnOutcomeGraphLockAttempts {
			// A queued Turn can replace active Run/Turn identity after the
			// advisory pre-read but before the graph lock. Retry once so a
			// valid heartbeat re-enters the new outcome lock domain instead
			// of closing the authenticated control stream.
			continue
		}
		return result, err
	}
	return application.RuntimeHeartbeatState{},
		platformpostgres.ErrRevisionConflict
}

func recordRuntimeHeartbeatTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.RecordRuntimeHeartbeat,
) (application.RuntimeHeartbeatState, error) {
	var result application.RuntimeHeartbeatState
	err := func() error {
		outcomeLock, err := lockRuntimeTurnOutcomeBeforeGraph(
			ctx,
			tx,
			record.Command.RuntimeInstanceID,
		)
		if err != nil {
			return err
		}
		var tenantID, activeRuntimeID, sessionStatus, runtimeStatus string
		var sessionGeneration, runtimeGeneration, channelEpoch, runtimeRevision uint64
		err = tx.QueryRow(ctx, `
SELECT session.tenant_id,COALESCE(session.active_runtime_instance_id,''),session.runtime_generation,
       session.status,runtime.generation,runtime.control_channel_epoch,runtime.status,runtime.revision
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$2
  AND runtime.worker_session_id=session.worker_session_id
WHERE session.worker_session_id=$1
FOR UPDATE OF session,runtime`,
			record.Command.WorkerSessionID,
			record.Command.RuntimeInstanceID,
		).Scan(
			&tenantID, &activeRuntimeID, &sessionGeneration, &sessionStatus,
			&runtimeGeneration, &channelEpoch, &runtimeStatus, &runtimeRevision,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Runtime heartbeat identity: %w", err)
		}
		if err := validateRuntimeTurnOutcomeAfterGraphLock(
			ctx,
			tx,
			record.Command.RuntimeInstanceID,
			outcomeLock,
		); err != nil {
			return err
		}
		if activeRuntimeID != record.Command.RuntimeInstanceID ||
			sessionGeneration != record.Command.RuntimeGeneration ||
			runtimeGeneration != record.Command.RuntimeGeneration ||
			channelEpoch != record.Command.ChannelEpoch ||
			!heartbeatSessionStatusAllowed(sessionStatus) ||
			!heartbeatRuntimeStatusAllowed(runtimeStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		_, outcomeOwned, err := pendingTurnOutcomeOwnsRuntime(
			ctx,
			tx,
			runtimeLivenessRow{
				TenantID:                tenantID,
				WorkerSessionID:         record.Command.WorkerSessionID,
				ActiveRuntimeInstanceID: activeRuntimeID,
				RuntimeInstanceID:       record.Command.RuntimeInstanceID,
				RuntimeGeneration:       runtimeGeneration,
				ChannelEpoch:            channelEpoch,
				RuntimeStatus:           runtimeStatus,
				RuntimeRevision:         runtimeRevision,
			},
		)
		if err != nil {
			return err
		}
		firstHeartbeatTimedOut, err :=
			satisfyRuntimeFirstHeartbeatWatchdog(
				ctx,
				tx,
				record.Command,
			)
		if err != nil {
			return err
		}
		if firstHeartbeatTimedOut && !outcomeOwned {
			return errRuntimeFirstHeartbeatUnhealthy
		}

		var previous application.RuntimeHeartbeatState
		previous.WorkerSessionID = record.Command.WorkerSessionID
		previous.RuntimeInstanceID = record.Command.RuntimeInstanceID
		previous.RuntimeGeneration = runtimeGeneration
		previous.ChannelEpoch = channelEpoch
		err = tx.QueryRow(ctx, `
SELECT heartbeat_sequence,last_control_sequence,last_received_at,liveness_status,
       suspect_due_at,unhealthy_due_at,revision
FROM runtime_heartbeat_observations
WHERE runtime_instance_id=$1
FOR UPDATE`, record.Command.RuntimeInstanceID).Scan(
			&previous.HeartbeatSequence, &previous.ControlSequence, &previous.LastReceivedAt,
			&previous.LivenessStatus, &previous.SuspectDueAt, &previous.UnhealthyDueAt,
			&previous.ObservationRevision,
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lock Runtime heartbeat observation: %w", err)
		}
		if err == nil && record.Command.HeartbeatSequence <= previous.HeartbeatSequence {
			previous.RuntimeRevision = runtimeRevision
			previous.Duplicate = record.Command.HeartbeatSequence == previous.HeartbeatSequence
			previous.Stale = record.Command.HeartbeatSequence < previous.HeartbeatSequence
			result = previous
			return nil
		}

		observationRevision := uint64(1)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := tx.Exec(ctx, `
INSERT INTO runtime_heartbeat_observations
  (runtime_instance_id,worker_session_id,runtime_generation,channel_epoch,heartbeat_sequence,
   last_control_sequence,last_received_at,liveness_status,suspect_due_at,unhealthy_due_at,revision)
VALUES ($1,$2,$3,$4,$5,$6,$7,'healthy',$8,$9,1)`,
				record.Command.RuntimeInstanceID, record.Command.WorkerSessionID,
				record.Command.RuntimeGeneration, record.Command.ChannelEpoch,
				record.Command.HeartbeatSequence, record.Command.ControlSequence,
				record.Command.ReceivedAt, record.SuspectDueAt, record.UnhealthyDueAt,
			); err != nil {
				return fmt.Errorf("insert Runtime heartbeat observation: %w", err)
			}
		} else {
			if err := tx.QueryRow(ctx, `
UPDATE runtime_heartbeat_observations
SET heartbeat_sequence=$2,last_control_sequence=$3,last_received_at=$4,liveness_status='healthy',
    suspect_due_at=$5,unhealthy_due_at=$6,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$7
RETURNING revision`,
				record.Command.RuntimeInstanceID, record.Command.HeartbeatSequence,
				record.Command.ControlSequence, record.Command.ReceivedAt,
				record.SuspectDueAt, record.UnhealthyDueAt, previous.ObservationRevision,
			).Scan(&observationRevision); err != nil {
				return fmt.Errorf("advance Runtime heartbeat observation: %w", err)
			}
		}
		if outcomeOwned {
			result = application.RuntimeHeartbeatState{
				WorkerSessionID: record.Command.WorkerSessionID,
				RuntimeInstanceID: record.Command.
					RuntimeInstanceID,
				RuntimeGeneration: record.Command.
					RuntimeGeneration,
				ChannelEpoch:        record.Command.ChannelEpoch,
				HeartbeatSequence:   record.Command.HeartbeatSequence,
				ControlSequence:     record.Command.ControlSequence,
				LastReceivedAt:      record.Command.ReceivedAt,
				LivenessStatus:      application.RuntimeLivenessHealthy,
				SuspectDueAt:        record.SuspectDueAt,
				UnhealthyDueAt:      record.UnhealthyDueAt,
				ObservationRevision: observationRevision,
				RuntimeRevision:     runtimeRevision,
			}
			return nil
		}
		if runtimeStatus == "suspect" {
			if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='active',revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$2 AND status='suspect'
RETURNING revision`, record.Command.RuntimeInstanceID, runtimeRevision).Scan(&runtimeRevision); err != nil {
				return fmt.Errorf("restore Runtime liveness after heartbeat: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands
SET status='cancelled',updated_at=GREATEST(updated_at,$2)
WHERE command_type='runtime_liveness_evaluate'
  AND target_type='RuntimeHeartbeat'
  AND target_id=$1
  AND status='pending'`,
			record.Command.RuntimeInstanceID, record.Command.ReceivedAt,
		); err != nil {
			return fmt.Errorf("cancel stale Runtime liveness evaluations: %w", err)
		}
		payload, _ := json.Marshal(struct {
			RuntimeGeneration uint64 `json:"runtime_generation"`
			ChannelEpoch      uint64 `json:"channel_epoch"`
			HeartbeatSequence uint64 `json:"heartbeat_sequence"`
			Stage             string `json:"stage"`
		}{
			RuntimeGeneration: record.Command.RuntimeGeneration,
			ChannelEpoch:      record.Command.ChannelEpoch,
			HeartbeatSequence: record.Command.HeartbeatSequence,
			Stage:             "suspect",
		})
		if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'runtime_liveness_evaluate','RuntimeHeartbeat',$3,$4,$5,$6,'pending',$7,$8,$8)`,
			record.ScheduledCommandID, tenantID, record.Command.RuntimeInstanceID,
			observationRevision, payload, record.SuspectDueAt,
			"heartbeat/"+strconv.FormatUint(record.Command.RuntimeGeneration, 10)+"/"+
				strconv.FormatUint(record.Command.HeartbeatSequence, 10)+"/suspect",
			record.Command.ReceivedAt,
		); err != nil {
			return fmt.Errorf("schedule Runtime liveness evaluation: %w", err)
		}
		result = application.RuntimeHeartbeatState{
			WorkerSessionID:   record.Command.WorkerSessionID,
			RuntimeInstanceID: record.Command.RuntimeInstanceID,
			RuntimeGeneration: record.Command.RuntimeGeneration,
			ChannelEpoch:      record.Command.ChannelEpoch,
			HeartbeatSequence: record.Command.HeartbeatSequence,
			ControlSequence:   record.Command.ControlSequence,
			LastReceivedAt:    record.Command.ReceivedAt,
			LivenessStatus:    application.RuntimeLivenessHealthy,
			SuspectDueAt:      record.SuspectDueAt, UnhealthyDueAt: record.UnhealthyDueAt,
			ObservationRevision: observationRevision, RuntimeRevision: runtimeRevision,
		}
		return nil
	}()
	return result, err
}

func heartbeatSessionStatusAllowed(status string) bool {
	switch status {
	case "interactive", "running", "recovering", "waiting_user", "idle":
		return true
	default:
		return false
	}
}

func heartbeatRuntimeStatusAllowed(status string) bool {
	return status == "starting" || status == "active" || status == "suspect"
}

var _ application.RuntimeHeartbeatStore = (*RuntimeHeartbeatStore)(nil)
