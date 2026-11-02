package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

const (
	CommandRuntimeFirstHeartbeatEvaluate = "runtime_first_heartbeat_evaluate"

	runtimeFirstHeartbeatTargetType = "RuntimeFirstHeartbeatWatchdog"
	firstHeartbeatResumeAnchorKind  = "resume_complete_v1"
)

var errRuntimeFirstHeartbeatUnhealthy = errors.New(
	"Runtime first-heartbeat watchdog is unhealthy",
)

type runtimeFirstHeartbeatCommandPayload struct {
	RuntimeGeneration uint64 `json:"runtime_generation"`
	ChannelEpoch      uint64 `json:"channel_epoch"`
	AnchorKind        string `json:"anchor_kind"`
	AnchorRevision    uint64 `json:"anchor_revision"`
	Stage             string `json:"stage"`
}

func armRuntimeFirstHeartbeatWatchdog(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeControlChannelState,
	anchorAt time.Time,
) error {
	var status string
	err := tx.QueryRow(ctx, `
SELECT status
FROM runtime_first_heartbeat_watchdogs
WHERE runtime_instance_id=$1
FOR UPDATE`,
		state.RuntimeInstanceID,
	).Scan(&status)
	if err == nil {
		if status == "unhealthy" {
			return errRuntimeFirstHeartbeatUnhealthy
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock Runtime first-heartbeat watchdog: %w", err)
	}

	var heartbeatExists bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM runtime_heartbeat_observations
  WHERE runtime_instance_id=$1
    AND worker_session_id=$2
    AND runtime_generation=$3
    AND channel_epoch=$4
)`,
		state.RuntimeInstanceID,
		state.WorkerSessionID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
	).Scan(&heartbeatExists); err != nil {
		return fmt.Errorf("check first Runtime heartbeat observation: %w", err)
	}
	if heartbeatExists {
		return nil
	}

	if anchorAt.IsZero() || state.ResumeCompletedRevision == 0 {
		return fmt.Errorf(
			"Runtime first-heartbeat watchdog requires the completed resume anchor",
		)
	}
	anchorAt = anchorAt.UTC()
	suspectDueAt := anchorAt.Add(application.RuntimeSuspectAfter)
	unhealthyDueAt := anchorAt.Add(application.RuntimeUnhealthyAfter)
	var tenantID string
	if err := tx.QueryRow(ctx, `
SELECT tenant_id
FROM worker_sessions
WHERE worker_session_id=$1`,
		state.WorkerSessionID,
	).Scan(&tenantID); err != nil {
		return fmt.Errorf("resolve Runtime first-heartbeat tenant: %w", err)
	}
	tag := strconv.FormatUint(state.RuntimeGeneration, 10) + "/" +
		strconv.FormatUint(state.ChannelEpoch, 10)
	commandPayload, err := json.Marshal(runtimeFirstHeartbeatCommandPayload{
		RuntimeGeneration: state.RuntimeGeneration,
		ChannelEpoch:      state.ChannelEpoch,
		AnchorKind:        firstHeartbeatResumeAnchorKind,
		AnchorRevision:    state.ResumeCompletedRevision,
		Stage:             "suspect",
	})
	if err != nil {
		return fmt.Errorf("marshal Runtime first-heartbeat command: %w", err)
	}
	commandID := "runtime-first-heartbeat/" + state.RuntimeInstanceID + "/" +
		tag + "/suspect"
	idempotencyKey := "first-heartbeat/" + tag + "/suspect"

	command, err := tx.Exec(ctx, `
INSERT INTO runtime_first_heartbeat_watchdogs
  (runtime_instance_id,tenant_id,worker_session_id,runtime_generation,channel_epoch,
   anchor_kind,anchor_revision,first_authenticated_resume_at,
   suspect_due_at,unhealthy_due_at,status,
   revision,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'awaiting',1,$8,$8)
ON CONFLICT (runtime_instance_id) DO NOTHING`,
		state.RuntimeInstanceID,
		tenantID,
		state.WorkerSessionID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
		firstHeartbeatResumeAnchorKind,
		state.ResumeCompletedRevision,
		anchorAt,
		suspectDueAt,
		unhealthyDueAt,
	)
	if err != nil {
		return fmt.Errorf("arm Runtime first-heartbeat watchdog: %w", err)
	}
	if command.RowsAffected() == 0 {
		// The Session/Runtime lock serializes this path with heartbeat
		// persistence. A conflict here can only be a duplicate resume.
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,1,$6,$7,'pending',$8,$9,$9)
`,
		commandID,
		tenantID,
		CommandRuntimeFirstHeartbeatEvaluate,
		runtimeFirstHeartbeatTargetType,
		state.RuntimeInstanceID,
		commandPayload,
		suspectDueAt,
		idempotencyKey,
		anchorAt,
	); err != nil {
		return fmt.Errorf(
			"schedule Runtime first-heartbeat suspect evaluation: %w",
			err,
		)
	}
	return nil
}

type runtimeFirstHeartbeatWatchdog struct {
	Exists            bool
	Status            string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
	UnhealthyDueAt    time.Time
	Revision          uint64
}

func lockRuntimeFirstHeartbeatWatchdog(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
) (runtimeFirstHeartbeatWatchdog, error) {
	var watchdog runtimeFirstHeartbeatWatchdog
	err := tx.QueryRow(ctx, `
SELECT status,runtime_generation,channel_epoch,unhealthy_due_at,revision
FROM runtime_first_heartbeat_watchdogs
WHERE runtime_instance_id=$1
FOR UPDATE`,
		runtimeInstanceID,
	).Scan(
		&watchdog.Status,
		&watchdog.RuntimeGeneration,
		&watchdog.ChannelEpoch,
		&watchdog.UnhealthyDueAt,
		&watchdog.Revision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeFirstHeartbeatWatchdog{}, nil
	}
	if err != nil {
		return runtimeFirstHeartbeatWatchdog{},
			fmt.Errorf("lock Runtime first-heartbeat watchdog: %w", err)
	}
	watchdog.Exists = true
	return watchdog, nil
}

func satisfyRuntimeFirstHeartbeatWatchdog(
	ctx context.Context,
	tx platformpostgres.Tx,
	command application.ObserveRuntimeHeartbeatCommand,
) (bool, error) {
	watchdog, err := lockRuntimeFirstHeartbeatWatchdog(
		ctx,
		tx,
		command.RuntimeInstanceID,
	)
	if err != nil || !watchdog.Exists {
		return false, err
	}
	if watchdog.RuntimeGeneration != command.RuntimeGeneration ||
		watchdog.ChannelEpoch != command.ChannelEpoch {
		return false, platformpostgres.ErrRevisionConflict
	}
	switch watchdog.Status {
	case "satisfied":
		return false, nil
	case "cancelled":
		return false, nil
	case "unhealthy":
		return true, nil
	case "awaiting", "suspect":
	default:
		return false, fmt.Errorf(
			"Runtime first-heartbeat watchdog status %q is invalid",
			watchdog.Status,
		)
	}
	if !command.ReceivedAt.Before(watchdog.UnhealthyDueAt) {
		return true, nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_first_heartbeat_watchdogs
SET status='satisfied',
    first_heartbeat_sequence=$2,
    first_heartbeat_control_sequence=$3,
    first_heartbeat_received_at=$4,
    revision=revision+1,
    updated_at=GREATEST(updated_at,$4)
WHERE runtime_instance_id=$1
  AND revision=$5
  AND status IN ('awaiting','suspect')`,
		command.RuntimeInstanceID,
		command.HeartbeatSequence,
		command.ControlSequence,
		command.ReceivedAt,
		watchdog.Revision,
	); err != nil {
		return false, fmt.Errorf(
			"satisfy Runtime first-heartbeat watchdog: %w",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands
SET status='cancelled',lease_owner=NULL,lease_expires_at=NULL,
    updated_at=GREATEST(updated_at,$2)
WHERE command_type=$3
  AND target_type=$4
  AND target_id=$1
  AND status='pending'`,
		command.RuntimeInstanceID,
		command.ReceivedAt,
		CommandRuntimeFirstHeartbeatEvaluate,
		runtimeFirstHeartbeatTargetType,
	); err != nil {
		return false, fmt.Errorf(
			"cancel Runtime first-heartbeat evaluations: %w",
			err,
		)
	}
	return false, nil
}

type RuntimeFirstHeartbeatStore struct {
	pool *pgxpool.Pool
}

func NewRuntimeFirstHeartbeatStore(
	pool *pgxpool.Pool,
) *RuntimeFirstHeartbeatStore {
	return &RuntimeFirstHeartbeatStore{pool: pool}
}

type runtimeFirstHeartbeatEvaluationRow struct {
	TenantID                 string
	WorkerSessionID          string
	SessionStatus            domain.WorkerSessionStatus
	SessionRevision          uint64
	ActiveRuntimeInstanceID  string
	ActiveWorkerRunID        string
	RuntimeInstanceID        string
	RuntimeGeneration        uint64
	ChannelEpoch             uint64
	RuntimeStatus            string
	RuntimeRevision          uint64
	AnchorKind               application.RuntimeFirstHeartbeatAnchorKind
	AnchorRevision           uint64
	AnchorAt                 time.Time
	SuspectDueAt             time.Time
	UnhealthyDueAt           time.Time
	Status                   application.RuntimeFirstHeartbeatStatus
	UnhealthyRuntimeRevision uint64
	RecoveryIntent           []byte
	TerminalReason           application.RuntimeFirstHeartbeatTerminalReason
	WatchdogRevision         uint64
}

func (store *RuntimeFirstHeartbeatStore) EvaluateRuntimeFirstHeartbeat(
	ctx context.Context,
	record application.EvaluateRuntimeFirstHeartbeatRecord,
) (application.RuntimeFirstHeartbeatEvaluation, error) {
	for attempt := 0; attempt < runtimeTurnOutcomeGraphLockAttempts; attempt++ {
		var result application.RuntimeFirstHeartbeatEvaluation
		err := platformpostgres.InTransaction(
			ctx,
			store.pool,
			func(tx platformpostgres.Tx) error {
				outcomeLock, err :=
					lockRuntimeTurnOutcomeBeforeGraph(
						ctx,
						tx,
						record.Command.RuntimeInstanceID,
					)
				if err != nil {
					return err
				}
				row, found, err := lockRuntimeFirstHeartbeatEvaluation(
					ctx,
					tx,
					record.Command.RuntimeInstanceID,
				)
				if err != nil || !found {
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
				if row.TenantID != record.Command.TenantID ||
					row.RuntimeInstanceID !=
						record.Command.RuntimeInstanceID ||
					row.RuntimeGeneration !=
						record.Command.RuntimeGeneration ||
					row.ChannelEpoch != record.Command.ChannelEpoch ||
					row.AnchorKind != record.Command.AnchorKind ||
					row.AnchorRevision != record.Command.AnchorRevision {
					return nil
				}

				switch row.Status {
				case application.RuntimeFirstHeartbeatSatisfied,
					application.RuntimeFirstHeartbeatCancelled:
					result = firstHeartbeatEvaluationFromRow(
						row,
						false,
						nil,
					)
					return nil
				case application.RuntimeFirstHeartbeatUnhealthy:
					row.RuntimeRevision =
						row.UnhealthyRuntimeRevision
					var recovery *application.OpenRuntimeRecoveryCommand
					if len(row.RecoveryIntent) != 0 {
						var intent application.OpenRuntimeRecoveryCommand
						if err := json.Unmarshal(
							row.RecoveryIntent,
							&intent,
						); err != nil {
							return fmt.Errorf(
								"decode persisted first-heartbeat recovery intent: %w",
								err,
							)
						}
						recovery = &intent
					}
					result = firstHeartbeatEvaluationFromRow(
						row,
						false,
						recovery,
					)
					return nil
				case application.RuntimeFirstHeartbeatAwaiting,
					application.RuntimeFirstHeartbeatSuspect:
				default:
					return fmt.Errorf(
						"Runtime first-heartbeat watchdog status %q is invalid",
						row.Status,
					)
				}
				if row.WatchdogRevision !=
					record.Command.ExpectedWatchdogRevision {
					return nil
				}
				if row.ActiveRuntimeInstanceID != row.RuntimeInstanceID ||
					!firstHeartbeatSessionStatusAllowed(row.SessionStatus) ||
					!firstHeartbeatRuntimeStatusAllowed(row.RuntimeStatus) {
					updated, err := cancelRuntimeFirstHeartbeat(
						ctx,
						tx,
						row,
						application.RuntimeFirstHeartbeatRuntimeNotLive,
						record.Command.EvaluatedAt,
					)
					if err != nil {
						return err
					}
					result = firstHeartbeatEvaluationFromRow(
						updated,
						true,
						nil,
					)
					return nil
				}
				outcomeSource, outcomeOwned, err :=
					pendingTurnOutcomeOwnsRuntime(
						ctx,
						tx,
						runtimeLivenessRow{
							TenantID: row.TenantID,
							WorkerSessionID: row.
								WorkerSessionID,
							RuntimeInstanceID: row.
								RuntimeInstanceID,
							RuntimeGeneration: row.
								RuntimeGeneration,
							ChannelEpoch: row.ChannelEpoch,
						},
					)
				if err != nil {
					return err
				}
				if outcomeOwned {
					reason :=
						application.RuntimeFirstHeartbeatTurnOutcome
					if outcomeSource ==
						application.TurnSettlementSourceBackendDeadline {
						reason =
							application.RuntimeFirstHeartbeatBackendDeadline
					}
					updated, err := cancelRuntimeFirstHeartbeat(
						ctx,
						tx,
						row,
						reason,
						record.Command.EvaluatedAt,
					)
					if err != nil {
						return err
					}
					result = firstHeartbeatEvaluationFromRow(
						updated,
						true,
						nil,
					)
					return nil
				}

				projected, err :=
					projectAcceptedRuntimeHeartbeatBeforeDecision(
						ctx,
						tx,
						row,
					)
				if err != nil {
					return err
				}
				if projected {
					updated, found, err :=
						lockRuntimeFirstHeartbeatEvaluation(
							ctx,
							tx,
							row.RuntimeInstanceID,
						)
					if err != nil {
						return err
					}
					if !found {
						return fmt.Errorf(
							"projected Runtime heartbeat lost its watchdog",
						)
					}
					result = firstHeartbeatEvaluationFromRow(
						updated,
						true,
						nil,
					)
					return nil
				}

				classified, err :=
					application.ClassifyRuntimeFirstHeartbeat(
						row.AnchorAt,
						record.Command.EvaluatedAt,
					)
				if err != nil {
					return err
				}
				if classified ==
					application.RuntimeFirstHeartbeatAwaiting ||
					(record.Command.Stage ==
						application.RuntimeFirstHeartbeatStageUnhealthy &&
						classified !=
							application.RuntimeFirstHeartbeatUnhealthy) {
					return nil
				}
				if classified ==
					application.RuntimeFirstHeartbeatSuspect {
					if row.Status ==
						application.RuntimeFirstHeartbeatSuspect {
						return nil
					}
					updated, err := markRuntimeFirstHeartbeatSuspect(
						ctx,
						tx,
						row,
						record,
					)
					if err != nil {
						return err
					}
					result = firstHeartbeatEvaluationFromRow(
						updated,
						true,
						nil,
					)
					return nil
				}

				updated, recovery, err :=
					markRuntimeFirstHeartbeatUnhealthy(
						ctx,
						tx,
						row,
						record.Command.EvaluatedAt,
					)
				if err != nil {
					return err
				}
				result = firstHeartbeatEvaluationFromRow(
					updated,
					true,
					recovery,
				)
				return nil
			},
		)
		if errors.Is(err, platformpostgres.ErrRevisionConflict) &&
			attempt+1 < runtimeTurnOutcomeGraphLockAttempts {
			continue
		}
		return result, err
	}
	return application.RuntimeFirstHeartbeatEvaluation{},
		platformpostgres.ErrRevisionConflict
}

func lockRuntimeFirstHeartbeatEvaluation(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
) (runtimeFirstHeartbeatEvaluationRow, bool, error) {
	var row runtimeFirstHeartbeatEvaluationRow
	var activeRuntimeID, activeRunID *string
	var anchorKind, status string
	var recoveryIntent []byte
	var terminalReason *string
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.worker_session_id,session.status,
       session.session_revision,session.active_runtime_instance_id,
       session.active_worker_run_id,
       runtime.runtime_instance_id,runtime.generation,
       runtime.control_channel_epoch,runtime.status,runtime.revision,
       watchdog.anchor_kind,watchdog.anchor_revision,
       watchdog.first_authenticated_resume_at,
       watchdog.suspect_due_at,watchdog.unhealthy_due_at,
       watchdog.status,COALESCE(watchdog.unhealthy_runtime_revision,0),
       watchdog.recovery_intent,
       watchdog.terminal_reason,watchdog.revision
FROM runtime_first_heartbeat_watchdogs watchdog
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=watchdog.runtime_instance_id
JOIN worker_sessions session
  ON session.worker_session_id=watchdog.worker_session_id
 AND session.worker_session_id=runtime.worker_session_id
WHERE watchdog.runtime_instance_id=$1
FOR UPDATE OF session,runtime,watchdog`,
		runtimeInstanceID,
	).Scan(
		&row.TenantID,
		&row.WorkerSessionID,
		&row.SessionStatus,
		&row.SessionRevision,
		&activeRuntimeID,
		&activeRunID,
		&row.RuntimeInstanceID,
		&row.RuntimeGeneration,
		&row.ChannelEpoch,
		&row.RuntimeStatus,
		&row.RuntimeRevision,
		&anchorKind,
		&row.AnchorRevision,
		&row.AnchorAt,
		&row.SuspectDueAt,
		&row.UnhealthyDueAt,
		&status,
		&row.UnhealthyRuntimeRevision,
		&recoveryIntent,
		&terminalReason,
		&row.WatchdogRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return runtimeFirstHeartbeatEvaluationRow{}, false, nil
	}
	if err != nil {
		return runtimeFirstHeartbeatEvaluationRow{},
			false,
			fmt.Errorf(
				"lock Runtime first-heartbeat evaluation: %w",
				err,
			)
	}
	if activeRuntimeID != nil {
		row.ActiveRuntimeInstanceID = *activeRuntimeID
	}
	if activeRunID != nil {
		row.ActiveWorkerRunID = *activeRunID
	}
	row.AnchorKind =
		application.RuntimeFirstHeartbeatAnchorKind(anchorKind)
	row.Status = application.RuntimeFirstHeartbeatStatus(status)
	row.RecoveryIntent = recoveryIntent
	if terminalReason != nil {
		row.TerminalReason =
			application.RuntimeFirstHeartbeatTerminalReason(
				*terminalReason,
			)
	}
	return row, true, nil
}

func projectAcceptedRuntimeHeartbeatBeforeDecision(
	ctx context.Context,
	tx platformpostgres.Tx,
	row runtimeFirstHeartbeatEvaluationRow,
) (bool, error) {
	rows, err := tx.Query(ctx, `
SELECT envelope::text,created_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1
  AND direction='event'
  AND effect_status IN ('received','applied')
  AND created_at < $2
  AND (
    envelope->>'kind'='heartbeat'
    OR envelope->>'message_type'='runtime.heartbeat'
  )
ORDER BY sequence`,
		row.RuntimeInstanceID,
		row.UnhealthyDueAt,
	)
	if err != nil {
		return false,
			fmt.Errorf(
				"load accepted Runtime heartbeat evidence: %w",
				err,
			)
	}
	defer rows.Close()

	var accepted []application.ObserveRuntimeHeartbeatCommand
	for rows.Next() {
		var encoded []byte
		var receivedAt time.Time
		if err := rows.Scan(&encoded, &receivedAt); err != nil {
			return false,
				fmt.Errorf(
					"scan accepted Runtime heartbeat evidence: %w",
					err,
				)
		}
		envelope, err :=
			contractsv1.DecodeRuntimeControlEnvelope(encoded)
		if err != nil {
			return false,
				fmt.Errorf(
					"decode accepted Runtime heartbeat envelope: %w",
					err,
				)
		}
		command, heartbeat, err :=
			application.DecodeRuntimeHeartbeatEvent(
				application.WorkerControlEvent{
					Envelope: envelope,
					Payload: append(
						json.RawMessage(nil),
						envelope.Payload...,
					),
				},
				receivedAt,
			)
		if err != nil || !heartbeat {
			continue
		}
		if command.WorkerSessionID != row.WorkerSessionID ||
			command.RuntimeInstanceID != row.RuntimeInstanceID ||
			command.RuntimeGeneration != row.RuntimeGeneration ||
			command.ChannelEpoch != row.ChannelEpoch {
			return false,
				fmt.Errorf(
					"accepted Runtime heartbeat identity disagrees with watchdog",
				)
		}
		accepted = append(accepted, command)
	}
	if err := rows.Err(); err != nil {
		return false,
			fmt.Errorf(
				"iterate accepted Runtime heartbeat evidence: %w",
				err,
			)
	}
	rows.Close()

	for _, command := range accepted {
		tag := strconv.FormatUint(
			command.RuntimeGeneration,
			10,
		) + "/" + strconv.FormatUint(
			command.HeartbeatSequence,
			10,
		)
		if _, err := recordRuntimeHeartbeatTx(
			ctx,
			tx,
			application.RecordRuntimeHeartbeat{
				Command: command,
				ScheduledCommandID: "runtime-heartbeat-repair/" +
					row.RuntimeInstanceID + "/" + tag +
					"/suspect",
				SuspectDueAt: command.ReceivedAt.Add(
					application.RuntimeSuspectAfter,
				),
				UnhealthyDueAt: command.ReceivedAt.Add(
					application.RuntimeUnhealthyAfter,
				),
			},
		); err != nil {
			return false,
				fmt.Errorf(
					"project accepted Runtime heartbeat evidence: %w",
					err,
				)
		}
	}
	return len(accepted) != 0, nil
}

func markRuntimeFirstHeartbeatSuspect(
	ctx context.Context,
	tx platformpostgres.Tx,
	row runtimeFirstHeartbeatEvaluationRow,
	record application.EvaluateRuntimeFirstHeartbeatRecord,
) (runtimeFirstHeartbeatEvaluationRow, error) {
	if record.UnhealthyCommandID == "" ||
		record.UnhealthyIdempotencyKey == "" {
		return row, fmt.Errorf(
			"Runtime first-heartbeat suspect transition requires its unhealthy command",
		)
	}
	if row.SessionStatus != domain.SessionRecovering &&
		(row.RuntimeStatus == "starting" ||
			row.RuntimeStatus == "active") {
		if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='suspect',revision=revision+1
WHERE runtime_instance_id=$1
  AND revision=$2
  AND status=$3
RETURNING revision`,
			row.RuntimeInstanceID,
			row.RuntimeRevision,
			row.RuntimeStatus,
		).Scan(&row.RuntimeRevision); err != nil {
			return row,
				fmt.Errorf(
					"mark Runtime suspect before first heartbeat: %w",
					err,
				)
		}
		row.RuntimeStatus = "suspect"
	}
	if err := tx.QueryRow(ctx, `
UPDATE runtime_first_heartbeat_watchdogs
SET status='suspect',suspect_at=$3,revision=revision+1,
    updated_at=GREATEST(updated_at,$3)
WHERE runtime_instance_id=$1
  AND revision=$2
  AND status='awaiting'
RETURNING revision`,
		row.RuntimeInstanceID,
		row.WatchdogRevision,
		record.Command.EvaluatedAt,
	).Scan(&row.WatchdogRevision); err != nil {
		return row,
			fmt.Errorf(
				"mark Runtime first-heartbeat watchdog suspect: %w",
				err,
			)
	}
	row.Status = application.RuntimeFirstHeartbeatSuspect
	payload, err := json.Marshal(runtimeFirstHeartbeatCommandPayload{
		RuntimeGeneration: row.RuntimeGeneration,
		ChannelEpoch:      row.ChannelEpoch,
		AnchorKind:        string(row.AnchorKind),
		AnchorRevision:    row.AnchorRevision,
		Stage: string(
			application.RuntimeFirstHeartbeatStageUnhealthy,
		),
	})
	if err != nil {
		return row, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$10)
`,
		record.UnhealthyCommandID,
		row.TenantID,
		CommandRuntimeFirstHeartbeatEvaluate,
		runtimeFirstHeartbeatTargetType,
		row.RuntimeInstanceID,
		row.WatchdogRevision,
		payload,
		row.UnhealthyDueAt,
		record.UnhealthyIdempotencyKey,
		record.Command.EvaluatedAt,
	); err != nil {
		return row,
			fmt.Errorf(
				"schedule Runtime first-heartbeat unhealthy evaluation: %w",
				err,
			)
	}
	return row, nil
}

func markRuntimeFirstHeartbeatUnhealthy(
	ctx context.Context,
	tx platformpostgres.Tx,
	row runtimeFirstHeartbeatEvaluationRow,
	evaluatedAt time.Time,
) (
	runtimeFirstHeartbeatEvaluationRow,
	*application.OpenRuntimeRecoveryCommand,
	error,
) {
	if row.SessionStatus == domain.SessionRecovering {
		if err := tx.QueryRow(ctx, `
UPDATE runtime_first_heartbeat_watchdogs
SET status='unhealthy',unhealthy_at=unhealthy_due_at,
    unhealthy_runtime_revision=$3,
    terminal_reason=$4,revision=revision+1,
    updated_at=GREATEST(updated_at,$5)
WHERE runtime_instance_id=$1
  AND revision=$2
  AND status IN ('awaiting','suspect')
RETURNING revision`,
			row.RuntimeInstanceID,
			row.WatchdogRevision,
			row.RuntimeRevision,
			application.RuntimeFirstHeartbeatReplacementTimeout,
			evaluatedAt,
		).Scan(&row.WatchdogRevision); err != nil {
			return row, nil,
				fmt.Errorf(
					"mark replacement first-heartbeat timeout: %w",
					err,
				)
		}
		row.Status = application.RuntimeFirstHeartbeatUnhealthy
		row.UnhealthyRuntimeRevision = row.RuntimeRevision
		row.TerminalReason =
			application.RuntimeFirstHeartbeatReplacementTimeout
		return row, nil, nil
	}

	if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='unhealthy',revision=revision+1
WHERE runtime_instance_id=$1
  AND revision=$2
  AND status=$3
RETURNING revision`,
		row.RuntimeInstanceID,
		row.RuntimeRevision,
		row.RuntimeStatus,
	).Scan(&row.RuntimeRevision); err != nil {
		return row, nil,
			fmt.Errorf(
				"mark Runtime unhealthy before first heartbeat: %w",
				err,
			)
	}
	row.RuntimeStatus = "unhealthy"
	recovery, err := buildFirstHeartbeatRecoveryIntent(
		ctx,
		tx,
		row,
	)
	if err != nil {
		return row, nil, err
	}
	encodedRecovery, err := json.Marshal(recovery)
	if err != nil {
		return row, nil,
			fmt.Errorf(
				"marshal Runtime first-heartbeat recovery intent: %w",
				err,
			)
	}
	if err := tx.QueryRow(ctx, `
UPDATE runtime_first_heartbeat_watchdogs
SET status='unhealthy',unhealthy_at=unhealthy_due_at,
    unhealthy_runtime_revision=$3,recovery_intent=$4,
    revision=revision+1,updated_at=GREATEST(updated_at,$5)
WHERE runtime_instance_id=$1
  AND revision=$2
  AND status IN ('awaiting','suspect')
RETURNING revision`,
		row.RuntimeInstanceID,
		row.WatchdogRevision,
		row.RuntimeRevision,
		encodedRecovery,
		evaluatedAt,
	).Scan(&row.WatchdogRevision); err != nil {
		return row, nil,
			fmt.Errorf(
				"persist Runtime first-heartbeat recovery intent: %w",
				err,
			)
	}
	row.Status = application.RuntimeFirstHeartbeatUnhealthy
	row.UnhealthyRuntimeRevision = row.RuntimeRevision
	row.RecoveryIntent = encodedRecovery
	return row, &recovery, nil
}

func buildFirstHeartbeatRecoveryIntent(
	ctx context.Context,
	tx platformpostgres.Tx,
	row runtimeFirstHeartbeatEvaluationRow,
) (application.OpenRuntimeRecoveryCommand, error) {
	recovery := application.OpenRuntimeRecoveryCommand{
		TenantID:                row.TenantID,
		WorkerSessionID:         row.WorkerSessionID,
		FailedRuntimeInstanceID: row.RuntimeInstanceID,
		FailedGeneration:        row.RuntimeGeneration,
		FailedChannelEpoch:      row.ChannelEpoch,
		Trigger:                 domain.RecoveryTriggerHeartbeatUnhealthy,
		EvidenceRef: "runtime-first-heartbeat/" +
			row.RuntimeInstanceID + "/g" +
			strconv.FormatUint(row.RuntimeGeneration, 10) + "/e" +
			strconv.FormatUint(row.ChannelEpoch, 10) + "/anchor/" +
			strconv.FormatUint(row.AnchorRevision, 10),
		EvidenceAt:              row.UnhealthyDueAt,
		ExpectedSessionRevision: row.SessionRevision,
		ExpectedRuntimeRevision: row.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" +
			row.WorkerSessionID + "/g" +
			strconv.FormatUint(row.RuntimeGeneration, 10) +
			"/heartbeat_unhealthy",
	}
	if row.ActiveWorkerRunID == "" {
		return recovery, nil
	}
	err := tx.QueryRow(ctx, `
SELECT run.worker_run_id,run.revision,turn.agent_turn_id,turn.revision
FROM worker_runs run
JOIN LATERAL (
  SELECT candidate.agent_turn_id,candidate.revision
  FROM agent_turns candidate
  WHERE candidate.worker_session_id=$1
    AND candidate.worker_run_id=run.worker_run_id
  ORDER BY candidate.created_at DESC,candidate.agent_turn_id DESC
  LIMIT 1
) turn ON true
WHERE run.worker_run_id=$2
  AND run.worker_session_id=$1
FOR UPDATE OF run`,
		row.WorkerSessionID,
		row.ActiveWorkerRunID,
	).Scan(
		&recovery.WorkerRunID,
		&recovery.ExpectedRunRevision,
		&recovery.AgentTurnID,
		&recovery.ExpectedTurnRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.OpenRuntimeRecoveryCommand{},
			fmt.Errorf(
				"active WorkerRun %s has no first-heartbeat recovery AgentTurn",
				row.ActiveWorkerRunID,
			)
	}
	if err != nil {
		return application.OpenRuntimeRecoveryCommand{},
			fmt.Errorf(
				"lock first-heartbeat recovery Run and Turn: %w",
				err,
			)
	}
	return recovery, nil
}

func cancelRuntimeFirstHeartbeat(
	ctx context.Context,
	tx platformpostgres.Tx,
	row runtimeFirstHeartbeatEvaluationRow,
	reason application.RuntimeFirstHeartbeatTerminalReason,
	evaluatedAt time.Time,
) (runtimeFirstHeartbeatEvaluationRow, error) {
	if err := tx.QueryRow(ctx, `
UPDATE runtime_first_heartbeat_watchdogs
SET status='cancelled',terminal_reason=$3,revision=revision+1,
    updated_at=GREATEST(updated_at,$4)
WHERE runtime_instance_id=$1
  AND revision=$2
  AND status IN ('awaiting','suspect')
RETURNING revision`,
		row.RuntimeInstanceID,
		row.WatchdogRevision,
		reason,
		evaluatedAt,
	).Scan(&row.WatchdogRevision); err != nil {
		return row,
			fmt.Errorf(
				"cancel Runtime first-heartbeat watchdog: %w",
				err,
			)
	}
	if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands
SET status='cancelled',lease_owner=NULL,lease_expires_at=NULL,
    updated_at=GREATEST(updated_at,$2)
WHERE command_type=$3
  AND target_type=$4
  AND target_id=$1
  AND status='pending'`,
		row.RuntimeInstanceID,
		evaluatedAt,
		CommandRuntimeFirstHeartbeatEvaluate,
		runtimeFirstHeartbeatTargetType,
	); err != nil {
		return row,
			fmt.Errorf(
				"cancel pending Runtime first-heartbeat commands: %w",
				err,
			)
	}
	row.Status = application.RuntimeFirstHeartbeatCancelled
	row.TerminalReason = reason
	return row, nil
}

func firstHeartbeatEvaluationFromRow(
	row runtimeFirstHeartbeatEvaluationRow,
	applied bool,
	recovery *application.OpenRuntimeRecoveryCommand,
) application.RuntimeFirstHeartbeatEvaluation {
	return application.RuntimeFirstHeartbeatEvaluation{
		Applied:          applied,
		Status:           row.Status,
		TerminalReason:   row.TerminalReason,
		WatchdogRevision: row.WatchdogRevision,
		RuntimeRevision:  row.RuntimeRevision,
		OpenRecovery:     recovery,
	}
}

func firstHeartbeatSessionStatusAllowed(
	status domain.WorkerSessionStatus,
) bool {
	switch status {
	case domain.SessionInteractive,
		domain.SessionRunning,
		domain.SessionRecovering,
		domain.SessionWaitingUser,
		domain.SessionIdle:
		return true
	default:
		return false
	}
}

func firstHeartbeatRuntimeStatusAllowed(status string) bool {
	return status == "starting" ||
		status == "active" ||
		status == "suspect"
}

var _ application.RuntimeFirstHeartbeatStore = (*RuntimeFirstHeartbeatStore)(nil)
