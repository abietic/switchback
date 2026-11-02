package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

const CommandBackendDeadlineSourceReconcile = "backend_deadline_source_reconcile"

func (repository *TurnSettlementRepository) ClaimBackendDeadlineTurnSettlement(
	ctx context.Context,
	record application.ClaimBackendDeadlineRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	if err := application.ValidateClaimBackendDeadlineRecord(record); err != nil {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}

	var result application.TurnSettlement
	err := platformpostgres.InTransaction(
		ctx,
		repository.pool,
		func(tx platformpostgres.Tx) error {
			if err := lockAgentTurnOutcome(
				ctx,
				tx,
				record.Command.AgentTurnID,
			); err != nil {
				return err
			}

			existing, err := readTurnSettlement(
				ctx,
				tx,
				record.SettlementID,
				true,
			)
			switch {
			case err == nil:
				if existing.Source.Kind !=
					application.TurnSettlementSourceBackendDeadline {
					return application.ErrTurnOutcomeAuthorityClaimed
				}
				if !backendDeadlineSettlementMatchesRecord(
					existing,
					record,
				) {
					return application.ErrTurnSettlementSourceConflict
				}
				result = existing
				return nil
			case !errors.Is(err, application.ErrTurnSettlementNotFound):
				return err
			}

			preemption, err := lockRuntimeRecoveryForBackendDeadline(
				ctx,
				tx,
				record,
			)
			if err != nil {
				return err
			}
			if err := lockBackendDeadlineSourceGraph(
				ctx,
				tx,
				record,
			); err != nil {
				return err
			}
			source := record.Source
			if preemption.Found {
				source, err =
					application.BindBackendDeadlineRecoveryPreemption(
						source,
						preemption.IncidentID,
						preemption.Revision+1,
						record.ClaimedAt,
					)
				if err != nil {
					return err
				}
			}
			tag, err := tx.Exec(ctx, `
INSERT INTO worker_turn_settlements
  (settlement_id,tenant_id,worker_session_id,worker_run_id,agent_session_id,
   agent_turn_id,runtime_instance_id,runtime_generation,channel_epoch,
   terminal_event_direction,terminal_event_sequence,terminal_message_id,
   terminal_idempotency_key,terminal_envelope_digest,
   source_kind,source_reason,source_occurred_at,source_ref,source_digest,
   source_state,source_state_digest,accepted_at,updated_at)
VALUES
  ($1,$2,$3,$4,$5,$6,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,
   $7,$8,$9,$10,$11,$12,$13,$14,$14)
ON CONFLICT DO NOTHING`,
				record.SettlementID,
				record.Command.TenantID,
				record.Command.WorkerSessionID,
				record.Command.WorkerRunID,
				record.Command.AgentSessionID,
				record.Command.AgentTurnID,
				source.Kind,
				source.Reason,
				source.OccurredAt,
				source.Ref,
				source.Digest,
				source.State,
				source.StateDigest,
				record.ClaimedAt,
			)
			if err != nil {
				return fmt.Errorf(
					"insert Backend deadline Turn settlement: %w",
					err,
				)
			}
			if preemption.Found {
				if err := applyBackendDeadlineRecoveryPreemption(
					ctx,
					tx,
					preemption,
					record,
				); err != nil {
					return err
				}
			}
			current, err := readTurnSettlement(
				ctx,
				tx,
				record.SettlementID,
				true,
			)
			if err != nil {
				if tag.RowsAffected() == 0 {
					return application.ErrTurnSettlementSourceConflict
				}
				return err
			}
			if current.Source.Kind !=
				application.TurnSettlementSourceBackendDeadline {
				return application.ErrTurnOutcomeAuthorityClaimed
			}
			if !backendDeadlineSettlementMatchesRecord(current, record) {
				return application.ErrTurnSettlementSourceConflict
			}
			result = current
			return nil
		},
	)
	return result, err
}

func (repository *TurnSettlementRepository) AdvanceBackendDeadlineSource(
	ctx context.Context,
	record application.AdvanceBackendDeadlineSourceRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	if record.Command.SettlementID == "" ||
		record.Command.ExpectedRevision == 0 ||
		record.Command.ExpectedStateDigest == "" ||
		record.Command.LeaseOwnerID == "" ||
		record.Command.LeaseFence == 0 ||
		record.AdvancedAt.IsZero() {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	snapshot, err := readTurnSettlement(
		ctx,
		repository.pool,
		record.Command.SettlementID,
		false,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	if snapshot.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	var snapshotState application.BackendDeadlineSourceState
	if err := json.Unmarshal(snapshot.Source.State, &snapshotState); err != nil {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}

	var result application.TurnSettlement
	err = platformpostgres.InTransaction(
		ctx,
		repository.pool,
		func(tx platformpostgres.Tx) error {
			if err := lockAgentTurnOutcome(
				ctx,
				tx,
				snapshot.AgentTurnID,
			); err != nil {
				return err
			}
			current, err := readTurnSettlement(
				ctx,
				tx,
				record.Command.SettlementID,
				false,
			)
			if err != nil {
				return err
			}
			if current.Source.Kind !=
				application.TurnSettlementSourceBackendDeadline ||
				application.ValidateTurnSettlementSource(current.Source) != nil {
				return application.ErrTurnSettlementSourceConflict
			}
			var currentState application.BackendDeadlineSourceState
			if err := json.Unmarshal(
				current.Source.State,
				&currentState,
			); err != nil {
				return application.ErrTurnSettlementSourceConflict
			}
			target, err := lockBackendDeadlineSourceTarget(
				ctx,
				tx,
				current,
				currentState,
			)
			if err != nil {
				return err
			}
			switch record.Command.ToPhase {
			case application.BackendDeadlineSourceInterruptQueued:
				advanced, err := advanceBackendDeadlineInterrupt(
					ctx,
					tx,
					current,
					currentState,
					record,
				)
				if err != nil {
					return err
				}
				result = advanced
				return nil
			case application.BackendDeadlineSourceForceStopIntent:
				advanced, err := advanceBackendDeadlineForceStopIntent(
					ctx,
					tx,
					current,
					currentState,
					target,
					record,
				)
				if err != nil {
					return err
				}
				result = advanced
				return nil
			case application.BackendDeadlineSourceWriteFenced:
				advanced, err := advanceBackendDeadlineWriteFence(
					ctx,
					tx,
					current,
					currentState,
					target,
					record,
				)
				if err != nil {
					return err
				}
				result = advanced
				return nil
			case application.BackendDeadlineSourceCleanupConfirmed:
				advanced, err := advanceBackendDeadlineCleanupConfirmed(
					ctx,
					tx,
					current,
					currentState,
					target,
					record,
				)
				if err != nil {
					return err
				}
				result = advanced
				return nil
			default:
				return application.ErrTurnSettlementSourceConflict
			}
		},
	)
	return result, err
}

func (repository *TurnSettlementRepository) ApplyBackendDeadlineTurnSettlement(
	ctx context.Context,
	record application.ApplyBackendDeadlineSourceRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	if err := application.ValidateApplyBackendDeadlineSourceRecord(
		record,
	); err != nil {
		return application.TurnSettlement{}, err
	}
	snapshot, err := readTurnSettlement(
		ctx,
		repository.pool,
		record.Command.SettlementID,
		false,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	if snapshot.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}

	var result application.TurnSettlement
	err = platformpostgres.InTransaction(
		ctx,
		repository.pool,
		func(tx platformpostgres.Tx) error {
			if err := lockAgentTurnOutcome(
				ctx,
				tx,
				snapshot.AgentTurnID,
			); err != nil {
				return err
			}
			current, err := readTurnSettlement(
				ctx,
				tx,
				record.Command.SettlementID,
				true,
			)
			if err != nil {
				return err
			}
			if current.Source.Kind !=
				application.TurnSettlementSourceBackendDeadline ||
				application.ValidateTurnSettlementSource(
					current.Source,
				) != nil ||
				current.Source.Digest !=
					record.Command.ExpectedSourceDigest ||
				current.Source.StateDigest !=
					record.Command.ExpectedSourceStateDigest {
				return application.ErrTurnSettlementSourceConflict
			}
			state, err := application.DecodeBackendDeadlineSourceState(
				current.Source,
			)
			if err != nil ||
				state.Phase !=
					application.BackendDeadlineSourceCleanupConfirmed ||
				state.Cleanup == nil {
				return application.ErrTurnSettlementSourceConflict
			}
			applyTargetAuthority, err :=
				application.ResolveBackendDeadlineSourceApplyTarget(
					current.PhaseState,
					current.Source,
				)
			if err != nil {
				return err
			}
			target, err := lockBackendDeadlineSourceApplyTarget(
				ctx,
				tx,
				current,
				state,
			)
			if err != nil {
				return err
			}
			if current.Phase ==
				application.TurnSettlementTerminalEventApplied {
				if err := validateBackendDeadlineSourceApplyReplay(
					current,
					state,
					target,
					applyTargetAuthority,
					record,
				); err != nil {
					return err
				}
				result = current
				return nil
			}
			prepared, err := prepareBackendDeadlineSourceApply(
				ctx,
				tx,
				current,
				state,
				target,
				applyTargetAuthority,
				record,
				false,
			)
			if err != nil {
				return err
			}
			if err := applyBackendDeadlineSourceTarget(
				ctx,
				tx,
				current,
				prepared,
				record.AppliedAt,
			); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `
UPDATE worker_turn_settlements
SET phase=$2,
    terminal_event_disposition='commit',
    terminal_apply_outcome_digest=$3,
    terminal_event_applied_at=$4,
    phase_state=$5,
    phase_state_digest=$6,
    revision=revision+1,
    updated_at=$4
WHERE settlement_id=$1
  AND revision=$7
  AND phase='task_settled'
  AND source_kind='backend_deadline'
  AND source_digest=$8
  AND source_state_digest=$9
  AND lease_owner_id=$10
  AND lease_fence=$11
  AND lease_expires_at > $4
  AND runtime_instance_id IS NULL
  AND runtime_generation IS NULL
  AND channel_epoch IS NULL
  AND terminal_event_direction IS NULL
  AND terminal_event_sequence IS NULL
  AND terminal_message_id IS NULL
  AND terminal_idempotency_key IS NULL
  AND terminal_envelope_digest IS NULL`,
				current.SettlementID,
				application.TurnSettlementTerminalEventApplied,
				prepared.SourceProofDigest,
				record.AppliedAt,
				prepared.Checkpoint,
				prepared.CheckpointDigest,
				current.Revision,
				current.Source.Digest,
				current.Source.StateDigest,
				current.LeaseOwnerID,
				current.LeaseFence,
			)
			if err != nil {
				return fmt.Errorf(
					"apply Backend deadline Turn settlement source: %w",
					err,
				)
			}
			if tag.RowsAffected() != 1 {
				return application.ErrTurnSettlementRevisionConflict
			}
			current.Phase =
				application.TurnSettlementTerminalEventApplied
			current.TerminalEventDisposition =
				application.RuntimeControlEffectDispositionCommit
			current.TerminalApplyOutcomeDigest =
				prepared.SourceProofDigest
			current.TerminalEventAppliedAt = record.AppliedAt
			current.PhaseState = prepared.Checkpoint
			current.PhaseStateDigest =
				prepared.CheckpointDigest
			current.Revision++
			current.UpdatedAt = record.AppliedAt
			result = current
			return nil
		},
	)
	return result, err
}

type backendDeadlineSourceApplyPreparation struct {
	State             application.BackendDeadlineSourceState
	TargetAuthority   application.BackendDeadlineSourceApplyTargetAuthority
	RuntimeStatus     string
	Checkpoint        json.RawMessage
	CheckpointDigest  string
	SourceProofDigest string
}

func prepareBackendDeadlineSourceApply(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.TurnSettlement,
	state application.BackendDeadlineSourceState,
	target backendDeadlineSourceApplyTarget,
	targetAuthority application.BackendDeadlineSourceApplyTargetAuthority,
	record application.ApplyBackendDeadlineSourceRecord,
	expectTerminalWitness bool,
) (backendDeadlineSourceApplyPreparation, error) {
	if current.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline ||
		current.Source.Digest !=
			record.Command.ExpectedSourceDigest ||
		current.Source.StateDigest !=
			record.Command.ExpectedSourceStateDigest ||
		state.Phase !=
			application.BackendDeadlineSourceCleanupConfirmed ||
		state.Cleanup == nil ||
		state.ForceStop == nil {
		return backendDeadlineSourceApplyPreparation{},
			application.ErrTurnSettlementSourceConflict
	}
	if current.Revision != record.Command.ExpectedRevision {
		return backendDeadlineSourceApplyPreparation{},
			application.ErrTurnSettlementRevisionConflict
	}
	if current.Phase != application.TurnSettlementTaskSettled {
		return backendDeadlineSourceApplyPreparation{},
			application.ErrTurnSettlementPhaseConflict
	}
	witnessEmpty := terminalEventWitnessEmpty(current)
	if witnessEmpty == expectTerminalWitness {
		return backendDeadlineSourceApplyPreparation{},
			application.ErrTurnSettlementSourceConflict
	}
	if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
		current.LeaseFence != record.Command.LeaseFence ||
		!current.LeaseExpiresAt.After(record.AppliedAt) {
		return backendDeadlineSourceApplyPreparation{},
			application.ErrTurnSettlementLeaseStale
	}
	if err := validateBackendDeadlineSourceApplyTargetBefore(
		ctx,
		tx,
		current,
		state,
		target,
		targetAuthority,
	); err != nil {
		return backendDeadlineSourceApplyPreparation{}, err
	}
	checkpoint, checkpointDigest, sourceProofDigest, err :=
		application.BuildBackendDeadlineSourceAppliedCheckpoint(
			current.PhaseState,
			current.Source,
			record.Command,
		)
	if err != nil {
		return backendDeadlineSourceApplyPreparation{}, err
	}
	return backendDeadlineSourceApplyPreparation{
		State:             state,
		TargetAuthority:   targetAuthority,
		RuntimeStatus:     target.RuntimeStatus,
		Checkpoint:        checkpoint,
		CheckpointDigest:  checkpointDigest,
		SourceProofDigest: sourceProofDigest,
	}, nil
}

func applyBackendDeadlineSourceTarget(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.TurnSettlement,
	prepared backendDeadlineSourceApplyPreparation,
	appliedAt time.Time,
) error {
	state := prepared.State
	authority := prepared.TargetAuthority
	var runtimeRevision uint64
	err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='terminated',
    cleanup_confirmed=true,
    cleanup_diagnostic=jsonb_build_object(
      'status','confirmed',
      'reason','agent_turn_deadline',
      'settlement_id',$6::text),
    ended_at=$2,
    revision=revision+1
WHERE runtime_instance_id=$1
  AND worker_session_id=$3
  AND worker_run_id=$4
  AND generation=$5
  AND revision=$7
  AND status=$8
  AND cleanup_confirmed=false
RETURNING revision`,
		state.RuntimeInstanceID,
		appliedAt,
		current.WorkerSessionID,
		current.WorkerRunID,
		state.RuntimeGeneration,
		current.SettlementID,
		authority.ExpectedRuntimeRevision,
		prepared.RuntimeStatus,
	).Scan(&runtimeRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrTurnSettlementRevisionConflict
	}
	if err != nil {
		return fmt.Errorf(
			"terminate Backend deadline Runtime at source apply: %w",
			err,
		)
	}
	if runtimeRevision != authority.RuntimeRevision {
		return application.ErrTurnSettlementRevisionConflict
	}
	var sessionRevision uint64
	err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status=$4,
    active_runtime_instance_id=NULL,
    session_revision=session_revision+1,
    updated_at=$2
WHERE worker_session_id=$1
  AND tenant_id=$3
  AND status=$5
  AND session_revision=$6
  AND active_runtime_instance_id=$7
RETURNING session_revision`,
		current.WorkerSessionID,
		appliedAt,
		current.TenantID,
		authority.SessionStatus,
		authority.ExpectedSessionStatus,
		authority.ExpectedSessionRevision,
		state.RuntimeInstanceID,
	).Scan(&sessionRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrTurnSettlementRevisionConflict
	}
	if err != nil {
		return fmt.Errorf(
			"unbind Backend deadline Runtime at source apply: %w",
			err,
		)
	}
	if sessionRevision != authority.SessionRevision {
		return application.ErrTurnSettlementRevisionConflict
	}
	return nil
}

type backendDeadlineSourceApplyTarget struct {
	SessionStatus           string
	SessionRevision         uint64
	ActiveRuntimeInstanceID string
	RuntimeStatus           string
	RuntimeRevision         uint64
	RuntimeGeneration       uint64
	ControlChannelEpoch     uint64
	CleanupConfirmed        bool
	EndedAt                 *time.Time
}

func lockBackendDeadlineSourceApplyTarget(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	state application.BackendDeadlineSourceState,
) (backendDeadlineSourceApplyTarget, error) {
	var target backendDeadlineSourceApplyTarget
	err := tx.QueryRow(ctx, `
SELECT session.status,session.session_revision,
       COALESCE(session.active_runtime_instance_id,''),
       runtime.status,runtime.revision,runtime.generation,
       runtime.control_channel_epoch,
       runtime.cleanup_confirmed,runtime.ended_at
FROM worker_sessions session
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$2
  AND session.tenant_id=$1
  AND runtime.worker_run_id=$3
FOR UPDATE OF session,runtime`,
		settlement.TenantID,
		settlement.WorkerSessionID,
		settlement.WorkerRunID,
		state.RuntimeInstanceID,
	).Scan(
		&target.SessionStatus,
		&target.SessionRevision,
		&target.ActiveRuntimeInstanceID,
		&target.RuntimeStatus,
		&target.RuntimeRevision,
		&target.RuntimeGeneration,
		&target.ControlChannelEpoch,
		&target.CleanupConfirmed,
		&target.EndedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return backendDeadlineSourceApplyTarget{},
			application.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return backendDeadlineSourceApplyTarget{}, fmt.Errorf(
			"lock Backend deadline source apply target: %w",
			err,
		)
	}
	if target.RuntimeGeneration != state.RuntimeGeneration ||
		target.ControlChannelEpoch != state.ChannelEpoch {
		return backendDeadlineSourceApplyTarget{},
			application.ErrTurnSettlementSourceConflict
	}
	if target.EndedAt != nil {
		endedAt := target.EndedAt.UTC()
		target.EndedAt = &endedAt
	}
	return target, nil
}

func validateBackendDeadlineSourceApplyTargetBefore(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	state application.BackendDeadlineSourceState,
	target backendDeadlineSourceApplyTarget,
	authority application.BackendDeadlineSourceApplyTargetAuthority,
) error {
	if state.ForceStop == nil ||
		target.SessionStatus != authority.ExpectedSessionStatus ||
		target.SessionRevision !=
			authority.ExpectedSessionRevision ||
		target.ActiveRuntimeInstanceID != state.RuntimeInstanceID ||
		target.RuntimeRevision !=
			authority.ExpectedRuntimeRevision ||
		target.CleanupConfirmed ||
		target.EndedAt != nil {
		return application.ErrTurnSettlementSourceConflict
	}
	switch target.RuntimeStatus {
	case "active", "suspect":
		if state.RecoveryIncidentID != "" ||
			state.RecoveryIncidentRevision != 0 ||
			!state.RecoveryPreemptedAt.IsZero() {
			return application.ErrTurnSettlementSourceConflict
		}
		return nil
	case "unhealthy":
		err := validateExactDeadlinePreemptedRecovery(
			ctx,
			tx,
			settlement,
			state,
			residentRuntimeRow{
				RuntimeInstanceID:   state.RuntimeInstanceID,
				RuntimeGeneration:   target.RuntimeGeneration,
				ControlChannelEpoch: target.ControlChannelEpoch,
			},
		)
		if errors.Is(err, application.ErrTurnSettlementConflict) {
			return application.ErrTurnSettlementSourceConflict
		}
		return err
	default:
		return application.ErrTurnSettlementSourceConflict
	}
}

func validateBackendDeadlineSourceApplyReplay(
	current application.TurnSettlement,
	state application.BackendDeadlineSourceState,
	target backendDeadlineSourceApplyTarget,
	authority application.BackendDeadlineSourceApplyTargetAuthority,
	record application.ApplyBackendDeadlineSourceRecord,
) error {
	if !terminalEventWitnessEmpty(current) ||
		current.TerminalEventDisposition !=
			application.RuntimeControlEffectDispositionCommit ||
		current.TerminalApplyOutcomeDigest == "" ||
		current.TerminalEventAppliedAt.IsZero() {
		return application.ErrTurnSettlementSourceConflict
	}
	if err := validateBackendDeadlineSourceApplyReplayTarget(
		current,
		state,
		target,
		authority,
		record,
	); err != nil {
		return err
	}
	if err := application.ValidateBackendDeadlineSourceAppliedCheckpoint(
		current.Source,
		current.PhaseState,
		current.TerminalApplyOutcomeDigest,
		record.Command,
	); err != nil {
		return err
	}
	return nil
}

func validateBackendDeadlineSourceApplyReplayTarget(
	current application.TurnSettlement,
	state application.BackendDeadlineSourceState,
	target backendDeadlineSourceApplyTarget,
	authority application.BackendDeadlineSourceApplyTargetAuthority,
	record application.ApplyBackendDeadlineSourceRecord,
) error {
	if current.Revision < record.Command.ExpectedRevision+1 {
		return application.ErrTurnSettlementRevisionConflict
	}
	if current.TerminalEventAppliedAt.IsZero() ||
		state.ForceStop == nil ||
		target.SessionStatus != authority.SessionStatus ||
		target.SessionRevision !=
			authority.SessionRevision ||
		target.ActiveRuntimeInstanceID != "" ||
		target.RuntimeStatus != "terminated" ||
		target.RuntimeRevision !=
			authority.RuntimeRevision ||
		!target.CleanupConfirmed ||
		target.EndedAt == nil ||
		!target.EndedAt.Equal(current.TerminalEventAppliedAt) {
		return application.ErrTurnSettlementSourceConflict
	}
	return nil
}

type backendDeadlineSourceTarget struct {
	TenantID          string
	SessionRevision   uint64
	RuntimeRevision   uint64
	RuntimeStatus     string
	ContainerID       string
	RuntimeGeneration uint64
	ChannelEpoch      uint64
}

func lockBackendDeadlineSourceTarget(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	state application.BackendDeadlineSourceState,
) (backendDeadlineSourceTarget, error) {
	var result backendDeadlineSourceTarget
	var turnStatus string
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.session_revision,
       runtime.revision,runtime.status,COALESCE(runtime.container_id,''),
       runtime.generation,runtime.control_channel_epoch,turn.status
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$3
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_session_id=$4
 AND turn.agent_turn_id=$5
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$6
WHERE session.worker_session_id=$2
  AND session.tenant_id=$1
  AND session.active_worker_run_id=$3
  AND session.active_runtime_instance_id=$6
  AND session.runtime_generation=$7
  AND runtime.generation=$7
  AND runtime.control_channel_epoch=$8
FOR UPDATE OF session,run,turn,runtime`,
		settlement.TenantID,
		settlement.WorkerSessionID,
		settlement.WorkerRunID,
		settlement.AgentSessionID,
		settlement.AgentTurnID,
		state.RuntimeInstanceID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
	).Scan(
		&result.TenantID,
		&result.SessionRevision,
		&result.RuntimeRevision,
		&result.RuntimeStatus,
		&result.ContainerID,
		&result.RuntimeGeneration,
		&result.ChannelEpoch,
		&turnStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return backendDeadlineSourceTarget{},
			application.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return backendDeadlineSourceTarget{}, fmt.Errorf(
			"lock Backend deadline source target: %w",
			err,
		)
	}
	if result.TenantID != settlement.TenantID ||
		result.RuntimeGeneration != state.RuntimeGeneration ||
		result.ChannelEpoch != state.ChannelEpoch {
		return backendDeadlineSourceTarget{},
			application.ErrTurnSettlementSourceConflict
	}
	return result, nil
}

func advanceBackendDeadlineInterrupt(
	ctx context.Context,
	tx platformpostgres.Tx,
	snapshot application.TurnSettlement,
	snapshotState application.BackendDeadlineSourceState,
	record application.AdvanceBackendDeadlineSourceRecord,
) (application.TurnSettlement, error) {
	if record.InterruptLogicalEnvelope == nil ||
		record.Transition.ToPhase !=
			application.BackendDeadlineSourceInterruptQueued ||
		record.Transition.ForceStop != nil ||
		record.Transition.WriteFence != nil ||
		record.Transition.Cleanup != nil ||
		!backendDeadlineInterruptEnvelopeMatchesSettlement(
			*record.InterruptLogicalEnvelope,
			snapshot,
			snapshotState,
			record,
		) {
		return application.TurnSettlement{},
			fmt.Errorf(
				"validate Backend deadline interrupt intent: %w",
				application.ErrTurnSettlementSourceConflict,
			)
	}

	channel, channelErr := readRuntimeControlChannel(
		ctx,
		tx,
		snapshotState.RuntimeInstanceID,
		true,
	)
	channelFound := channelErr == nil
	if channelErr != nil &&
		!errors.Is(channelErr, platformpostgres.ErrNotFound) {
		return application.TurnSettlement{}, channelErr
	}
	message, found, err := findRuntimeControlMessage(
		ctx,
		tx,
		snapshotState.RuntimeInstanceID,
		application.RuntimeControlDirectionCommand,
		record.Command.InterruptMessageID,
		record.Command.InterruptIdempotencyKey,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	if found {
		if err := validateBackendDeadlineInterruptJournalMessage(
			message,
			*record.InterruptLogicalEnvelope,
		); err != nil {
			return application.TurnSettlement{}, fmt.Errorf(
				"validate Backend deadline interrupt journal replay: %w",
				err,
			)
		}
		message.Duplicate = true
	} else {
		if !channelFound ||
			!backendDeadlineInterruptJournalStatus(channel.Status) ||
			channel.WorkerSessionID != snapshot.WorkerSessionID ||
			channel.RuntimeGeneration != snapshotState.RuntimeGeneration ||
			channel.ChannelEpoch != snapshotState.ChannelEpoch {
			return application.TurnSettlement{},
				fmt.Errorf(
					"Backend deadline interrupt channel is not appendable: %w",
					application.ErrTurnSettlementSourceConflict,
				)
		}
		message, err = reserveRuntimeControlCommandLocked(
			ctx,
			tx,
			channel,
			application.ReserveRuntimeControlCommandRecord{
				Command: application.ReserveRuntimeControlCommandCommand{
					MessageID: record.Command.InterruptMessageID,
					IdempotencyKey: record.Command.
						InterruptIdempotencyKey,
				},
				LogicalEnvelope: *record.InterruptLogicalEnvelope,
				ReservedAt:      record.AdvancedAt,
			},
		)
		if err != nil {
			return application.TurnSettlement{}, err
		}
	}

	current, err := readTurnSettlement(
		ctx,
		tx,
		record.Command.SettlementID,
		true,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"apply Backend deadline interrupt proof: %w",
			err,
		)
	}
	proof := application.BackendDeadlineInterruptProof{
		MessageID:      message.MessageID,
		IdempotencyKey: message.IdempotencyKey,
		Sequence:       message.Sequence,
		EnvelopeDigest: message.Fingerprint,
		CursorDigest:   message.CursorDigest,
		JournaledAt:    message.CreatedAt,
	}
	next, err := application.ApplyBackendDeadlineSourceTransition(
		current.Source,
		application.BackendDeadlineSourceTransition{
			ToPhase:   application.BackendDeadlineSourceInterruptQueued,
			Interrupt: &proof,
		},
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"apply Backend deadline interrupt proof: %w",
			err,
		)
	}
	if next.StateDigest == current.Source.StateDigest {
		return current, nil
	}
	if current.Revision != record.Command.ExpectedRevision {
		return application.TurnSettlement{},
			application.ErrTurnSettlementRevisionConflict
	}
	if current.Source.StateDigest != record.Command.ExpectedStateDigest {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
		current.LeaseFence != record.Command.LeaseFence ||
		!current.LeaseExpiresAt.After(record.AdvancedAt) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementLeaseStale
	}
	err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET source_state=$2,source_state_digest=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1
  AND revision=$5
  AND source_state_digest=$6
  AND lease_owner_id=$7
  AND lease_fence=$8
  AND lease_expires_at > $4
RETURNING revision`,
		current.SettlementID,
		next.State,
		next.StateDigest,
		record.AdvancedAt,
		current.Revision,
		current.Source.StateDigest,
		current.LeaseOwnerID,
		current.LeaseFence,
	).Scan(&current.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementRevisionConflict
	}
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"advance Backend deadline interrupt source: %w",
			err,
		)
	}
	current.Source = next
	current.UpdatedAt = record.AdvancedAt
	return current, nil
}

type backendDeadlineReconcilePayload struct {
	SchemaVersion             uint32 `json:"schema_version"`
	SettlementID              string `json:"settlement_id"`
	ExpectedSourceStateDigest string `json:"expected_source_state_digest"`
}

func advanceBackendDeadlineForceStopIntent(
	ctx context.Context,
	tx platformpostgres.Tx,
	snapshot application.TurnSettlement,
	snapshotState application.BackendDeadlineSourceState,
	target backendDeadlineSourceTarget,
	record application.AdvanceBackendDeadlineSourceRecord,
) (application.TurnSettlement, error) {
	if record.InterruptLogicalEnvelope != nil ||
		record.Transition.ToPhase !=
			application.BackendDeadlineSourceForceStopIntent ||
		record.Transition.ForceStop == nil ||
		record.Transition.Interrupt != nil ||
		record.Transition.WriteFence != nil ||
		record.Transition.Cleanup != nil ||
		record.AdvancedAt.Before(snapshotState.ForceAfter) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	proof := *record.Transition.ForceStop
	if proof.RuntimeInstanceID != snapshotState.RuntimeInstanceID ||
		proof.RuntimeGeneration != target.RuntimeGeneration ||
		proof.ChannelEpoch != target.ChannelEpoch ||
		proof.ExpectedSessionRevision != target.SessionRevision ||
		proof.ExpectedRuntimeRevision != target.RuntimeRevision ||
		proof.ExpectedContainerID != target.ContainerID {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}

	// Apply once before fencing. The AgentTurn advisory lock makes this source
	// snapshot authoritative against terminal-event and competing source
	// transitions, so a terminal witness that already won cannot be followed by
	// a destructive Runtime fence.
	provisional, err := application.ApplyBackendDeadlineSourceTransition(
		snapshot.Source,
		record.Transition,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"validate Backend deadline force-stop intent: %w",
			err,
		)
	}

	channel, err := readRuntimeControlChannel(
		ctx,
		tx,
		snapshotState.RuntimeInstanceID,
		true,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"lock Backend deadline Runtime control channel: %w",
			err,
		)
	}
	if channel.WorkerSessionID != snapshot.WorkerSessionID ||
		channel.RuntimeGeneration != snapshotState.RuntimeGeneration ||
		channel.ChannelEpoch != snapshotState.ChannelEpoch {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	if _, err := fenceRuntimeControlChannelLocked(
		ctx,
		tx,
		channel,
		application.FenceRuntimeControlChannelCommand{
			WorkerSessionID:   snapshot.WorkerSessionID,
			RuntimeInstanceID: snapshotState.RuntimeInstanceID,
			RuntimeGeneration: snapshotState.RuntimeGeneration,
			ChannelEpoch:      snapshotState.ChannelEpoch,
		},
		record.AdvancedAt,
	); err != nil {
		return application.TurnSettlement{}, err
	}

	current, err := readTurnSettlement(
		ctx,
		tx,
		record.Command.SettlementID,
		true,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"lock Backend deadline force-stop source: %w",
			err,
		)
	}
	next, err := application.ApplyBackendDeadlineSourceTransition(
		current.Source,
		record.Transition,
	)
	if err != nil || next.StateDigest != provisional.StateDigest {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	transitionApplied := next.StateDigest != current.Source.StateDigest
	if transitionApplied {
		if current.Revision != record.Command.ExpectedRevision {
			return application.TurnSettlement{},
				application.ErrTurnSettlementRevisionConflict
		}
		if current.Source.StateDigest !=
			record.Command.ExpectedStateDigest {
			return application.TurnSettlement{},
				application.ErrTurnSettlementSourceConflict
		}
		if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
			current.LeaseFence != record.Command.LeaseFence ||
			!current.LeaseExpiresAt.After(record.AdvancedAt) {
			return application.TurnSettlement{},
				application.ErrTurnSettlementLeaseStale
		}
	}

	scheduledRevision := current.Revision
	if transitionApplied {
		scheduledRevision++
	}
	if err := ensureBackendDeadlineReconcileScheduledCommand(
		ctx,
		tx,
		current,
		proof,
		next.StateDigest,
		scheduledRevision,
		record.AdvancedAt,
	); err != nil {
		return application.TurnSettlement{}, err
	}
	if !transitionApplied {
		return current, nil
	}
	err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET source_state=$2,source_state_digest=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1
  AND revision=$5
  AND source_state_digest=$6
  AND lease_owner_id=$7
  AND lease_fence=$8
  AND lease_expires_at > $4
RETURNING revision`,
		current.SettlementID,
		next.State,
		next.StateDigest,
		record.AdvancedAt,
		current.Revision,
		current.Source.StateDigest,
		current.LeaseOwnerID,
		current.LeaseFence,
	).Scan(&current.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementRevisionConflict
	}
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"advance Backend deadline force-stop source: %w",
			err,
		)
	}
	current.Source = next
	current.UpdatedAt = record.AdvancedAt
	return current, nil
}

func ensureBackendDeadlineReconcileScheduledCommand(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	proof application.BackendDeadlineForceStopProof,
	expectedSourceStateDigest string,
	expectedRevision uint64,
	createdAt time.Time,
) error {
	payload, err := json.Marshal(backendDeadlineReconcilePayload{
		SchemaVersion:             1,
		SettlementID:              settlement.SettlementID,
		ExpectedSourceStateDigest: expectedSourceStateDigest,
	})
	if err != nil {
		return fmt.Errorf(
			"encode Backend deadline reconciliation command: %w",
			err,
		)
	}
	payload, _, err =
		application.CanonicalizeTurnSettlementCheckpoint(payload)
	if err != nil {
		return fmt.Errorf(
			"canonicalize Backend deadline reconciliation command: %w",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,'TurnSettlement',$4,$5,$6,$7,'pending',$8,$9,$9)
ON CONFLICT DO NOTHING`,
		proof.CleanupScheduledCommandID,
		settlement.TenantID,
		CommandBackendDeadlineSourceReconcile,
		settlement.SettlementID,
		expectedRevision,
		payload,
		proof.DecidedAt,
		proof.CleanupIdempotencyKey,
		createdAt,
	); err != nil {
		return fmt.Errorf(
			"schedule Backend deadline reconciliation: %w",
			err,
		)
	}

	var tenantID, commandType, targetType, targetID, idempotencyKey string
	var storedPayload json.RawMessage
	var dueAt time.Time
	var storedExpectedRevision uint64
	err = tx.QueryRow(ctx, `
SELECT tenant_id,command_type,target_type,target_id,expected_revision,
       payload,due_at,idempotency_key
FROM scheduled_commands
WHERE scheduled_command_id=$1
FOR UPDATE`,
		proof.CleanupScheduledCommandID,
	).Scan(
		&tenantID,
		&commandType,
		&targetType,
		&targetID,
		&storedExpectedRevision,
		&storedPayload,
		&dueAt,
		&idempotencyKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return fmt.Errorf(
			"read Backend deadline reconciliation command: %w",
			err,
		)
	}
	storedPayload, _, err =
		application.CanonicalizeTurnSettlementCheckpoint(storedPayload)
	if err != nil ||
		tenantID != settlement.TenantID ||
		commandType != CommandBackendDeadlineSourceReconcile ||
		targetType != "TurnSettlement" ||
		targetID != settlement.SettlementID ||
		storedExpectedRevision == 0 ||
		idempotencyKey != proof.CleanupIdempotencyKey ||
		!dueAt.Equal(proof.DecidedAt) ||
		string(storedPayload) != string(payload) {
		return application.ErrTurnSettlementSourceConflict
	}
	return nil
}

func advanceBackendDeadlineWriteFence(
	ctx context.Context,
	tx platformpostgres.Tx,
	snapshot application.TurnSettlement,
	snapshotState application.BackendDeadlineSourceState,
	target backendDeadlineSourceTarget,
	record application.AdvanceBackendDeadlineSourceRecord,
) (application.TurnSettlement, error) {
	if record.InterruptLogicalEnvelope != nil ||
		record.Transition.ToPhase !=
			application.BackendDeadlineSourceWriteFenced ||
		record.Transition.WriteFence == nil ||
		record.Transition.Interrupt != nil ||
		record.Transition.ForceStop != nil ||
		record.Transition.Cleanup != nil ||
		snapshotState.ForceStop == nil ||
		record.AdvancedAt.Before(
			record.Transition.WriteFence.ContainerAbsentObservedAt,
		) ||
		!backendDeadlineForceTargetStillExact(
			snapshotState,
			target,
		) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	proof := *record.Transition.WriteFence
	if proof.ContainerRef !=
		snapshotState.ForceStop.ExpectedContainerID {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	next, err := application.ApplyBackendDeadlineSourceTransition(
		snapshot.Source,
		record.Transition,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"validate Backend deadline write-fence proof: %w",
			err,
		)
	}

	channel, err := lockBackendDeadlineFencedChannel(
		ctx,
		tx,
		snapshot,
		snapshotState,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	if proof.ChannelOwnerFence != channel.FenceOwnerFence ||
		proof.ChannelRevision != channel.FenceRevision ||
		!proof.ChannelFencedAt.Equal(channel.FencedAt) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	return persistBackendDeadlineProofTransition(
		ctx,
		tx,
		snapshot,
		next,
		record,
		"write-fence",
	)
}

func advanceBackendDeadlineCleanupConfirmed(
	ctx context.Context,
	tx platformpostgres.Tx,
	snapshot application.TurnSettlement,
	snapshotState application.BackendDeadlineSourceState,
	target backendDeadlineSourceTarget,
	record application.AdvanceBackendDeadlineSourceRecord,
) (application.TurnSettlement, error) {
	if record.InterruptLogicalEnvelope != nil ||
		record.Transition.ToPhase !=
			application.BackendDeadlineSourceCleanupConfirmed ||
		record.Transition.Cleanup == nil ||
		record.Transition.Interrupt != nil ||
		record.Transition.ForceStop != nil ||
		record.Transition.WriteFence != nil ||
		snapshotState.ForceStop == nil ||
		snapshotState.WriteFence == nil ||
		record.AdvancedAt.Before(record.Transition.Cleanup.ConfirmedAt) ||
		!backendDeadlineForceTargetStillExact(
			snapshotState,
			target,
		) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	proof := *record.Transition.Cleanup
	if proof.ContainerRef !=
		snapshotState.ForceStop.ExpectedContainerID {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	next, err := application.ApplyBackendDeadlineSourceTransition(
		snapshot.Source,
		record.Transition,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"validate Backend deadline cleanup proof: %w",
			err,
		)
	}

	channel, err := lockBackendDeadlineFencedChannel(
		ctx,
		tx,
		snapshot,
		snapshotState,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	if channel.FenceOwnerFence !=
		snapshotState.WriteFence.ChannelOwnerFence ||
		channel.FenceRevision !=
			snapshotState.WriteFence.ChannelRevision ||
		!channel.FencedAt.Equal(
			snapshotState.WriteFence.ChannelFencedAt,
		) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	return persistBackendDeadlineProofTransition(
		ctx,
		tx,
		snapshot,
		next,
		record,
		"cleanup",
	)
}

func backendDeadlineForceTargetStillExact(
	state application.BackendDeadlineSourceState,
	target backendDeadlineSourceTarget,
) bool {
	return state.ForceStop != nil &&
		state.ForceStop.RuntimeInstanceID == state.RuntimeInstanceID &&
		state.ForceStop.RuntimeGeneration == target.RuntimeGeneration &&
		state.ForceStop.ChannelEpoch == target.ChannelEpoch &&
		state.ForceStop.ExpectedSessionRevision ==
			target.SessionRevision &&
		state.ForceStop.ExpectedRuntimeRevision ==
			target.RuntimeRevision &&
		state.ForceStop.ExpectedContainerID == target.ContainerID
}

func lockBackendDeadlineFencedChannel(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	state application.BackendDeadlineSourceState,
) (application.RuntimeControlChannelState, error) {
	channel, err := readRuntimeControlChannel(
		ctx,
		tx,
		state.RuntimeInstanceID,
		true,
	)
	if err != nil {
		return application.RuntimeControlChannelState{},
			fmt.Errorf(
				"lock Backend deadline fenced channel: %w",
				err,
			)
	}
	if channel.WorkerSessionID != settlement.WorkerSessionID ||
		channel.RuntimeGeneration != state.RuntimeGeneration ||
		channel.ChannelEpoch != state.ChannelEpoch ||
		channel.Status != "fenced" ||
		channel.FencedAt.IsZero() ||
		channel.FenceRevision == 0 ||
		channel.FenceRevision > channel.Revision ||
		channel.FenceOwnerFence == 0 ||
		channel.FenceOwnerFence != channel.OwnerFence {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementSourceConflict
	}
	return channel, nil
}

func persistBackendDeadlineProofTransition(
	ctx context.Context,
	tx platformpostgres.Tx,
	snapshot application.TurnSettlement,
	next application.TurnSettlementSource,
	record application.AdvanceBackendDeadlineSourceRecord,
	label string,
) (application.TurnSettlement, error) {
	current, err := readTurnSettlement(
		ctx,
		tx,
		record.Command.SettlementID,
		true,
	)
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"lock Backend deadline %s source: %w",
			label,
			err,
		)
	}
	reconciled, err := application.ApplyBackendDeadlineSourceTransition(
		current.Source,
		record.Transition,
	)
	if err != nil || reconciled.StateDigest != next.StateDigest {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	if reconciled.StateDigest == current.Source.StateDigest {
		return current, nil
	}
	if current.Revision != record.Command.ExpectedRevision {
		return application.TurnSettlement{},
			application.ErrTurnSettlementRevisionConflict
	}
	if current.Source.StateDigest != record.Command.ExpectedStateDigest {
		return application.TurnSettlement{},
			application.ErrTurnSettlementSourceConflict
	}
	if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
		current.LeaseFence != record.Command.LeaseFence ||
		!current.LeaseExpiresAt.After(record.AdvancedAt) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementLeaseStale
	}
	err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET source_state=$2,source_state_digest=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1
  AND revision=$5
  AND source_state_digest=$6
  AND lease_owner_id=$7
  AND lease_fence=$8
  AND lease_expires_at > $4
RETURNING revision`,
		current.SettlementID,
		reconciled.State,
		reconciled.StateDigest,
		record.AdvancedAt,
		current.Revision,
		current.Source.StateDigest,
		current.LeaseOwnerID,
		current.LeaseFence,
	).Scan(&current.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TurnSettlement{},
			application.ErrTurnSettlementRevisionConflict
	}
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"advance Backend deadline %s source: %w",
			label,
			err,
		)
	}
	current.Source = reconciled
	current.UpdatedAt = record.AdvancedAt
	return current, nil
}

func backendDeadlineInterruptEnvelopeMatchesSettlement(
	envelope contractsv1.RuntimeControlEnvelope,
	settlement application.TurnSettlement,
	state application.BackendDeadlineSourceState,
	record application.AdvanceBackendDeadlineSourceRecord,
) bool {
	return envelope.SchemaVersion ==
		contractsv1.RuntimeControlSchemaVersion &&
		envelope.Kind == "command" &&
		envelope.MessageType == "agent_turn.interrupt" &&
		envelope.MessageID == record.Command.InterruptMessageID &&
		envelope.IdempotencyKey ==
			record.Command.InterruptIdempotencyKey &&
		string(envelope.WorkerSessionID) ==
			settlement.WorkerSessionID &&
		string(envelope.WorkerRunID) == settlement.WorkerRunID &&
		string(envelope.AgentSessionID) ==
			settlement.AgentSessionID &&
		string(envelope.AgentTurnID) == settlement.AgentTurnID &&
		string(envelope.RuntimeInstanceID) ==
			state.RuntimeInstanceID &&
		uint64(envelope.RuntimeGeneration) ==
			state.RuntimeGeneration &&
		envelope.ChannelEpoch == state.ChannelEpoch
}

func backendDeadlineInterruptJournalStatus(status string) bool {
	switch status {
	case "connected", "resuming", "disconnected":
		return true
	default:
		return false
	}
}

func validateBackendDeadlineInterruptJournalMessage(
	message application.RuntimeControlMessage,
	logical contractsv1.RuntimeControlEnvelope,
) error {
	if message.Direction != application.RuntimeControlDirectionCommand ||
		message.Sequence == 0 ||
		message.MessageID != logical.MessageID ||
		message.IdempotencyKey != logical.IdempotencyKey ||
		message.CreatedAt.IsZero() {
		return application.ErrRuntimeControlMessageConflict
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(
		message.Envelope,
	)
	if err != nil ||
		!sameRuntimeControlLogicalEnvelope(envelope, logical) ||
		envelope.Sequence != message.Sequence ||
		!envelope.SentAt.Equal(message.CreatedAt) {
		return application.ErrRuntimeControlMessageConflict
	}
	fingerprint, err :=
		contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil || fingerprint != message.Fingerprint {
		return application.ErrRuntimeControlMessageConflict
	}
	return nil
}

func lockBackendDeadlineSourceGraph(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.ClaimBackendDeadlineRecord,
) error {
	var tenantID, turnStatus, scheduledTenantID string
	var commandType, targetType, targetID string
	var deadlineAt, dueAt time.Time
	var runtimeGeneration, channelEpoch uint64
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,turn.status,turn.deadline_at,
       runtime.generation,runtime.control_channel_epoch,
       command.tenant_id,command.command_type,command.target_type,
       command.target_id,command.due_at
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$3
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_session_id=$4
 AND turn.agent_turn_id=$5
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$6
JOIN scheduled_commands command
  ON command.scheduled_command_id=$9
WHERE session.worker_session_id=$2
  AND session.tenant_id=$1
  AND session.active_worker_run_id=$3
  AND session.active_runtime_instance_id=$6
  AND session.runtime_generation=$7
  AND runtime.generation=$7
  AND runtime.control_channel_epoch=$8
FOR UPDATE OF session,run,turn,runtime,command`,
		record.Command.TenantID,
		record.Command.WorkerSessionID,
		record.Command.WorkerRunID,
		record.Command.AgentSessionID,
		record.Command.AgentTurnID,
		record.Command.RuntimeInstanceID,
		record.Command.RuntimeGeneration,
		record.Command.ChannelEpoch,
		record.Command.SourceRef,
	).Scan(
		&tenantID,
		&turnStatus,
		&deadlineAt,
		&runtimeGeneration,
		&channelEpoch,
		&scheduledTenantID,
		&commandType,
		&targetType,
		&targetID,
		&dueAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return fmt.Errorf("lock Backend deadline source graph: %w", err)
	}
	if tenantID != record.Command.TenantID ||
		runtimeGeneration != record.Command.RuntimeGeneration ||
		channelEpoch != record.Command.ChannelEpoch ||
		scheduledTenantID != record.Command.TenantID ||
		commandType != CommandAgentTurnDeadline ||
		targetType != "AgentTurn" ||
		targetID != record.Command.AgentTurnID ||
		!deadlineAt.Equal(record.Command.DeadlineAt) ||
		!dueAt.Equal(record.Command.DeadlineAt) ||
		record.ClaimedAt.Before(deadlineAt) ||
		isTurnTerminal(turnStatus) {
		return application.ErrTurnSettlementSourceConflict
	}
	return nil
}

type backendDeadlineRecoveryPreemption struct {
	Found      bool
	IncidentID string
	Revision   uint64
	UpdatedAt  time.Time
}

func lockRuntimeRecoveryForBackendDeadline(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.ClaimBackendDeadlineRecord,
) (backendDeadlineRecoveryPreemption, error) {
	var result backendDeadlineRecoveryPreemption
	var tenantID, workerRunID, agentTurnID string
	err := tx.QueryRow(ctx, `
SELECT incident_id,tenant_id,COALESCE(worker_run_id,''),
       COALESCE(agent_turn_id,''),revision,updated_at
FROM runtime_recovery_incidents
WHERE worker_session_id=$1
  AND phase NOT IN ('recovered','exhausted')
FOR UPDATE`,
		record.Command.WorkerSessionID,
	).Scan(
		&result.IncidentID,
		&tenantID,
		&workerRunID,
		&agentTurnID,
		&result.Revision,
		&result.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return backendDeadlineRecoveryPreemption{}, fmt.Errorf(
			"lock Runtime recovery for Backend deadline preemption: %w",
			err,
		)
	}
	if tenantID != record.Command.TenantID ||
		workerRunID != record.Command.WorkerRunID ||
		agentTurnID != record.Command.AgentTurnID ||
		record.ClaimedAt.Before(result.UpdatedAt) {
		return backendDeadlineRecoveryPreemption{},
			application.ErrTurnSettlementSourceConflict
	}
	result.Found = true
	return result, nil
}

func applyBackendDeadlineRecoveryPreemption(
	ctx context.Context,
	tx platformpostgres.Tx,
	preemption backendDeadlineRecoveryPreemption,
	record application.ClaimBackendDeadlineRecord,
) error {
	var updatedRevision uint64
	err := tx.QueryRow(ctx, `
UPDATE runtime_recovery_incidents
SET phase='exhausted',
    outcome='exhausted',
    outcome_reason='preempted_by_backend_deadline',
    preempted_by_turn_settlement_id=$4,
    revision=revision+1,
    updated_at=$2,
    closed_at=$2
WHERE incident_id=$1
  AND revision=$3
  AND phase NOT IN ('recovered','exhausted')
RETURNING revision`,
		preemption.IncidentID,
		record.ClaimedAt,
		preemption.Revision,
		record.SettlementID,
	).Scan(&updatedRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return platformpostgres.ErrRevisionConflict
	}
	if err != nil {
		return fmt.Errorf(
			"preempt Runtime recovery for Backend deadline: %w",
			err,
		)
	}
	if updatedRevision != preemption.Revision+1 {
		return platformpostgres.ErrRevisionConflict
	}
	return appendRuntimeRecoveryDeadlinePreemptedEvent(
		ctx,
		tx,
		preemption,
		record,
	)
}

func appendRuntimeRecoveryDeadlinePreemptedEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	preemption backendDeadlineRecoveryPreemption,
	record application.ClaimBackendDeadlineRecord,
) error {
	stream := platformpostgres.OwnerStream{
		StreamID:      "worker-session/" + record.Command.WorkerSessionID,
		OwnerContext:  "worker-runtime",
		AggregateType: "WorkerSession",
		AggregateID:   record.Command.WorkerSessionID,
	}
	sequence, err := (platformpostgres.OwnerStreamRepository{}).Next(
		ctx,
		tx,
		stream,
	)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		IncidentID   string    `json:"incident_id"`
		SettlementID string    `json:"settlement_id"`
		AgentTurnID  string    `json:"agent_turn_id"`
		DeadlineAt   time.Time `json:"deadline_at"`
	}{
		IncidentID:   preemption.IncidentID,
		SettlementID: record.SettlementID,
		AgentTurnID:  record.Command.AgentTurnID,
		DeadlineAt:   record.Command.DeadlineAt,
	})
	if err != nil {
		return fmt.Errorf(
			"encode Runtime recovery deadline-preemption event: %w",
			err,
		)
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID: "runtime-recovery-preempted-by-deadline/" +
			preemption.IncidentID,
		EventType:       "RuntimeRecoveryPreemptedByBackendDeadline.v1",
		ProducerContext: "worker-runtime",
		StreamID:        stream.StreamID,
		Sequence:        sequence,
		TenantID:        record.Command.TenantID,
		AggregateType:   stream.AggregateType,
		AggregateID:     stream.AggregateID,
		TraceID:         stream.StreamID,
		CorrelationID:   preemption.IncidentID,
		CausationID:     record.SettlementID,
		IdempotencyKey: "runtime-recovery-preempted-by-deadline/" +
			preemption.IncidentID,
		Visibility: "internal",
		OccurredAt: record.ClaimedAt,
		Payload:    payload,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,
   tenant_id,aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,
   idempotency_key,visibility,payload,occurred_at)
VALUES
  ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		event.EventID,
		event.SchemaVersion,
		event.EventType,
		event.ProducerContext,
		event.StreamID,
		event.Sequence,
		event.TenantID,
		event.AggregateType,
		event.AggregateID,
		event.TraceID,
		event.CorrelationID,
		event.CausationID,
		event.IdempotencyKey,
		event.Visibility,
		event.Payload,
		event.OccurredAt,
	); err != nil {
		return fmt.Errorf(
			"insert Runtime recovery deadline-preemption event: %w",
			err,
		)
	}
	return nil
}

func backendDeadlineSettlementMatchesRecord(
	settlement application.TurnSettlement,
	record application.ClaimBackendDeadlineRecord,
) bool {
	return settlement.SettlementID == record.SettlementID &&
		settlement.TenantID == record.Command.TenantID &&
		settlement.WorkerSessionID == record.Command.WorkerSessionID &&
		settlement.WorkerRunID == record.Command.WorkerRunID &&
		settlement.AgentSessionID == record.Command.AgentSessionID &&
		settlement.AgentTurnID == record.Command.AgentTurnID &&
		settlement.Source.Kind == record.Source.Kind &&
		settlement.Source.Reason == record.Source.Reason &&
		settlement.Source.OccurredAt.Equal(record.Source.OccurredAt) &&
		settlement.Source.Ref == record.Source.Ref &&
		settlement.Source.Digest == record.Source.Digest
}

var _ application.TurnSettlementSourceStore = (*TurnSettlementRepository)(nil)
