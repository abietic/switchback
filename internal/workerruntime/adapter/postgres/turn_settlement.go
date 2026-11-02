package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type TurnSettlementRepository struct {
	pool        *pgxpool.Pool
	observation ObservationCommitParticipant
}

func NewTurnSettlementRepository(pool *pgxpool.Pool) *TurnSettlementRepository {
	return &TurnSettlementRepository{pool: pool}
}

func (repository *TurnSettlementRepository) LoadTurnSettlement(
	ctx context.Context,
	settlementID string,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	return readTurnSettlement(ctx, repository.pool, settlementID, false)
}

func (repository *TurnSettlementRepository) ClaimTurnSettlement(
	ctx context.Context,
	record application.ClaimTurnSettlementRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	var result application.TurnSettlement
	err := platformpostgres.InTransaction(ctx, repository.pool, func(tx platformpostgres.Tx) error {
		current, err := readTurnSettlement(ctx, tx, record.Command.SettlementID, true)
		if err != nil {
			return err
		}
		if current.Revision != record.Command.ExpectedRevision {
			return application.ErrTurnSettlementRevisionConflict
		}
		if current.Phase == application.TurnSettlementCompleted {
			return application.ErrTurnSettlementPhaseConflict
		}
		if current.LeaseOwnerID != "" &&
			current.LeaseOwnerID != record.Command.OwnerID &&
			current.LeaseExpiresAt.After(record.ClaimedAt) {
			return application.ErrTurnSettlementLeaseHeld
		}
		err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET lease_owner_id=$2,lease_fence=lease_fence+1,lease_expires_at=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1 AND revision=$5
RETURNING lease_fence,revision`,
			record.Command.SettlementID,
			record.Command.OwnerID,
			record.LeaseExpiresAt,
			record.ClaimedAt,
			record.Command.ExpectedRevision,
		).Scan(&current.LeaseFence, &current.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrTurnSettlementRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("claim Turn settlement: %w", err)
		}
		current.LeaseOwnerID = record.Command.OwnerID
		current.LeaseExpiresAt = record.LeaseExpiresAt
		current.UpdatedAt = record.ClaimedAt
		result = current
		return nil
	})
	return result, err
}

func (repository *TurnSettlementRepository) ClaimNextPendingTurnSettlement(
	ctx context.Context,
	record application.ClaimNextPendingTurnSettlementRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	var result application.TurnSettlement
	err := platformpostgres.InTransaction(ctx, repository.pool, func(tx platformpostgres.Tx) error {
		var settlementID string
		err := tx.QueryRow(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE phase <> 'completed'
  AND (lease_owner_id IS NULL OR lease_expires_at <= $1)
ORDER BY accepted_at,settlement_id
FOR UPDATE SKIP LOCKED
LIMIT 1`,
			record.ClaimedAt,
		).Scan(&settlementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrTurnSettlementNotFound
		}
		if err != nil {
			return fmt.Errorf("select next pending Turn settlement: %w", err)
		}
		current, err := readTurnSettlement(ctx, tx, settlementID, false)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET lease_owner_id=$2,lease_fence=lease_fence+1,lease_expires_at=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1
  AND revision=$5
  AND phase <> 'completed'
  AND (lease_owner_id IS NULL OR lease_expires_at <= $4)
RETURNING lease_fence,revision`,
			settlementID,
			record.Command.OwnerID,
			record.LeaseExpiresAt,
			record.ClaimedAt,
			current.Revision,
		).Scan(&current.LeaseFence, &current.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrTurnSettlementRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("claim next pending Turn settlement: %w", err)
		}
		current.LeaseOwnerID = record.Command.OwnerID
		current.LeaseExpiresAt = record.LeaseExpiresAt
		current.UpdatedAt = record.ClaimedAt
		result = current
		return nil
	})
	return result, err
}

func (repository *TurnSettlementRepository) FreezeTurnSettlementCheckpoint(
	ctx context.Context,
	record application.FreezeTurnSettlementCheckpointRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	canonical, digest, err := application.CanonicalizeTurnSettlementCheckpoint(
		record.CanonicalCheckpoint,
	)
	if err != nil || digest != record.CheckpointDigest {
		return application.TurnSettlement{}, application.ErrTurnSettlementCheckpointConflict
	}
	var result application.TurnSettlement
	err = platformpostgres.InTransaction(ctx, repository.pool, func(tx platformpostgres.Tx) error {
		current, err := readTurnSettlement(ctx, tx, record.Command.SettlementID, true)
		if err != nil {
			return err
		}
		if current.Phase == application.TurnSettlementCompleted {
			return application.ErrTurnSettlementPhaseConflict
		}
		if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
			current.LeaseFence != record.Command.LeaseFence ||
			!current.LeaseExpiresAt.After(record.FrozenAt) {
			return application.ErrTurnSettlementLeaseStale
		}
		if len(current.PhaseState) == 0 || string(current.PhaseState) == "{}" {
			if current.Phase != application.TurnSettlementAccepted {
				return application.ErrTurnSettlementCheckpointConflict
			}
			if err := application.ValidateInitialTurnSettlementCheckpoint(canonical); err != nil {
				return err
			}
		} else if err := application.ValidateTurnSettlementCheckpointExtension(
			current.PhaseState,
			canonical,
		); err != nil {
			return err
		}
		if current.PhaseStateDigest == digest {
			if record.Command.ExpectedRevision != current.Revision &&
				record.Command.ExpectedRevision+1 != current.Revision {
				return application.ErrTurnSettlementRevisionConflict
			}
			if repository.observation != nil {
				if err := repository.observation.CommitTurnBasis(ctx, tx, current, record); err != nil {
					return err
				}
			}
			result = current
			return nil
		}
		if current.Revision != record.Command.ExpectedRevision {
			return application.ErrTurnSettlementRevisionConflict
		}
		if repository.observation != nil {
			if err := repository.observation.CommitTurnBasis(ctx, tx, current, record); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET phase_state=$2,phase_state_digest=$3,revision=revision+1,updated_at=$4
WHERE settlement_id=$1 AND revision=$5 AND phase=$6
RETURNING revision`,
			current.SettlementID,
			canonical,
			digest,
			record.FrozenAt,
			current.Revision,
			current.Phase,
		).Scan(&current.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrTurnSettlementRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("freeze Turn settlement checkpoint: %w", err)
		}
		current.PhaseState = append([]byte(nil), canonical...)
		current.PhaseStateDigest = digest
		current.UpdatedAt = record.FrozenAt
		result = current
		return nil
	})
	return result, err
}

func (repository *TurnSettlementRepository) AdvanceTurnSettlement(
	ctx context.Context,
	record application.AdvanceTurnSettlementRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	var result application.TurnSettlement
	err := platformpostgres.InTransaction(ctx, repository.pool, func(tx platformpostgres.Tx) error {
		current, err := readTurnSettlement(ctx, tx, record.Command.SettlementID, true)
		if err != nil {
			return err
		}
		if current.Revision != record.Command.ExpectedRevision {
			return application.ErrTurnSettlementRevisionConflict
		}
		if current.Phase != record.Command.FromPhase {
			return application.ErrTurnSettlementPhaseConflict
		}
		if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
			current.LeaseFence != record.Command.LeaseFence ||
			!current.LeaseExpiresAt.After(record.AdvancedAt) {
			return application.ErrTurnSettlementLeaseStale
		}
		canonical, digest, err := application.CanonicalizeTurnSettlementCheckpoint(
			record.Command.Checkpoint,
		)
		if err != nil || digest != record.CheckpointDigest {
			return application.ErrTurnSettlementCheckpointConflict
		}
		if err := application.ValidateTurnSettlementCheckpointExtension(
			current.PhaseState,
			canonical,
		); err != nil {
			return err
		}
		if err := application.ValidateTurnSettlementCheckpointProof(
			record.Command.ToPhase,
			canonical,
		); err != nil {
			return err
		}
		var completedAt any
		var leaseOwner any = current.LeaseOwnerID
		var leaseExpires any = current.LeaseExpiresAt
		if record.Command.ToPhase == application.TurnSettlementCompleted {
			completedAt = record.AdvancedAt
			leaseOwner = nil
			leaseExpires = nil
		}
		err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET phase=$2,phase_state=$3,phase_state_digest=$4,
    lease_owner_id=$5,lease_expires_at=$6,completed_at=$7,
    revision=revision+1,updated_at=$8
WHERE settlement_id=$1 AND revision=$9 AND phase=$10
RETURNING revision`,
			record.Command.SettlementID,
			record.Command.ToPhase,
			canonical,
			digest,
			leaseOwner,
			leaseExpires,
			completedAt,
			record.AdvancedAt,
			record.Command.ExpectedRevision,
			record.Command.FromPhase,
		).Scan(&current.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrTurnSettlementRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("advance Turn settlement: %w", err)
		}
		current.Phase = record.Command.ToPhase
		current.PhaseState = append([]byte(nil), canonical...)
		current.PhaseStateDigest = digest
		current.UpdatedAt = record.AdvancedAt
		if record.Command.ToPhase == application.TurnSettlementCompleted {
			current.LeaseOwnerID = ""
			current.LeaseExpiresAt = time.Time{}
			current.CompletedAt = record.AdvancedAt
		}
		result = current
		return nil
	})
	return result, err
}

type turnSettlementQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readTurnSettlement(
	ctx context.Context,
	queryer turnSettlementQueryer,
	settlementID string,
	forUpdate bool,
) (application.TurnSettlement, error) {
	query := `
SELECT settlement.settlement_id,settlement.tenant_id,
       settlement.worker_session_id,settlement.worker_run_id,
       settlement.agent_session_id,settlement.agent_turn_id,
       settlement.runtime_instance_id,settlement.runtime_generation,
       settlement.channel_epoch,settlement.terminal_event_sequence,
       settlement.terminal_message_id,settlement.terminal_idempotency_key,
       settlement.terminal_envelope_digest,
       settlement.terminal_event_disposition,
       settlement.terminal_apply_outcome_digest,settlement.source_kind,
       settlement.source_reason,settlement.source_occurred_at,
       settlement.source_ref,settlement.source_digest,
       settlement.source_state,settlement.source_state_digest,
       settlement.phase,settlement.phase_state,
       settlement.phase_state_digest,settlement.revision,
       settlement.lease_owner_id,settlement.lease_fence,
       settlement.lease_expires_at,settlement.accepted_at,
       settlement.updated_at,settlement.terminal_event_applied_at,
       settlement.completed_at,witness.message_id,
       witness.idempotency_key,witness.fingerprint,witness.created_at,
       COALESCE(witness.envelope,'null'::json)
FROM worker_turn_settlements settlement
LEFT JOIN runtime_control_messages witness
  ON witness.runtime_instance_id=settlement.runtime_instance_id
 AND witness.direction='event'
 AND witness.sequence=settlement.terminal_event_sequence
WHERE settlement.settlement_id=$1`
	if forUpdate {
		query += " FOR UPDATE OF settlement"
	}
	var result application.TurnSettlement
	var runtimeInstanceID, terminalMessageID, terminalIdempotencyKey *string
	var terminalEnvelopeDigest *string
	var runtimeGeneration, channelEpoch, terminalEventSequence *uint64
	var disposition, applyDigest, leaseOwner *string
	var leaseExpiresAt, eventAppliedAt, completedAt *time.Time
	var witnessMessageID, witnessIdempotencyKey, witnessFingerprint *string
	var witnessCreatedAt *time.Time
	var witnessEnvelope json.RawMessage
	err := queryer.QueryRow(ctx, query, settlementID).Scan(
		&result.SettlementID,
		&result.TenantID,
		&result.WorkerSessionID,
		&result.WorkerRunID,
		&result.AgentSessionID,
		&result.AgentTurnID,
		&runtimeInstanceID,
		&runtimeGeneration,
		&channelEpoch,
		&terminalEventSequence,
		&terminalMessageID,
		&terminalIdempotencyKey,
		&terminalEnvelopeDigest,
		&disposition,
		&applyDigest,
		&result.Source.Kind,
		&result.Source.Reason,
		&result.Source.OccurredAt,
		&result.Source.Ref,
		&result.Source.Digest,
		&result.Source.State,
		&result.Source.StateDigest,
		&result.Phase,
		&result.PhaseState,
		&result.PhaseStateDigest,
		&result.Revision,
		&leaseOwner,
		&result.LeaseFence,
		&leaseExpiresAt,
		&result.AcceptedAt,
		&result.UpdatedAt,
		&eventAppliedAt,
		&completedAt,
		&witnessMessageID,
		&witnessIdempotencyKey,
		&witnessFingerprint,
		&witnessCreatedAt,
		&witnessEnvelope,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TurnSettlement{}, application.ErrTurnSettlementNotFound
	}
	if err != nil {
		return application.TurnSettlement{}, fmt.Errorf("read Turn settlement: %w", err)
	}
	if runtimeInstanceID != nil {
		result.RuntimeInstanceID = *runtimeInstanceID
	}
	if runtimeGeneration != nil {
		result.RuntimeGeneration = *runtimeGeneration
	}
	if channelEpoch != nil {
		result.ChannelEpoch = *channelEpoch
	}
	if terminalEventSequence != nil {
		result.TerminalEventSequence = *terminalEventSequence
	}
	if terminalMessageID != nil {
		result.TerminalMessageID = *terminalMessageID
	}
	if terminalIdempotencyKey != nil {
		result.TerminalIdempotencyKey = *terminalIdempotencyKey
	}
	if terminalEnvelopeDigest != nil {
		result.TerminalEnvelopeDigest = *terminalEnvelopeDigest
	}
	if disposition != nil {
		result.TerminalEventDisposition = application.RuntimeControlEffectDisposition(*disposition)
	}
	if applyDigest != nil {
		result.TerminalApplyOutcomeDigest = *applyDigest
	}
	if leaseOwner != nil {
		result.LeaseOwnerID = *leaseOwner
	}
	if leaseExpiresAt != nil {
		result.LeaseExpiresAt = leaseExpiresAt.UTC()
	}
	if eventAppliedAt != nil {
		result.TerminalEventAppliedAt = eventAppliedAt.UTC()
	}
	if completedAt != nil {
		result.CompletedAt = completedAt.UTC()
	}
	result.Source.OccurredAt = result.Source.OccurredAt.UTC()
	result.AcceptedAt = result.AcceptedAt.UTC()
	result.UpdatedAt = result.UpdatedAt.UTC()
	canonicalSourceState, sourceStateDigest, canonicalSourceErr :=
		application.CanonicalizeTurnSettlementSourceState(
			result.Source.State,
		)
	if canonicalSourceErr != nil ||
		sourceStateDigest != result.Source.StateDigest {
		return application.TurnSettlement{}, fmt.Errorf(
			"%w: durable terminal source proof is invalid",
			application.ErrTurnSettlementSourceConflict,
		)
	}
	result.Source.State = canonicalSourceState
	if err := application.ValidateTurnSettlementSource(
		result.Source,
	); err != nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"%w: durable terminal source is invalid",
			application.ErrTurnSettlementSourceConflict,
		)
	}
	if err := validateTurnSettlementEventWitness(
		result,
		witnessMessageID,
		witnessIdempotencyKey,
		witnessFingerprint,
		witnessCreatedAt,
		witnessEnvelope,
	); err != nil {
		return application.TurnSettlement{}, err
	}
	canonicalPhaseState, phaseStateDigest, canonicalErr :=
		application.CanonicalizeTurnSettlementCheckpoint(result.PhaseState)
	if canonicalErr != nil || phaseStateDigest != result.PhaseStateDigest {
		return application.TurnSettlement{}, fmt.Errorf(
			"%w: durable phase checkpoint proof is invalid",
			application.ErrTurnSettlementConflict,
		)
	}
	result.PhaseState = canonicalPhaseState
	return result, nil
}

func validateTurnSettlementEventWitness(
	settlement application.TurnSettlement,
	witnessMessageID *string,
	witnessIdempotencyKey *string,
	witnessFingerprint *string,
	witnessCreatedAt *time.Time,
	witnessEnvelope json.RawMessage,
) error {
	eventAttached := !terminalEventWitnessEmpty(settlement)
	witnessPresent := witnessMessageID != nil ||
		witnessIdempotencyKey != nil ||
		witnessFingerprint != nil ||
		witnessCreatedAt != nil
	if !eventAttached {
		if witnessPresent ||
			settlement.Source.Kind !=
				application.TurnSettlementSourceBackendDeadline {
			return application.ErrTurnSettlementSourceConflict
		}
		return nil
	}
	if witnessMessageID == nil ||
		witnessIdempotencyKey == nil ||
		witnessFingerprint == nil ||
		witnessCreatedAt == nil ||
		*witnessMessageID != settlement.TerminalMessageID ||
		*witnessIdempotencyKey !=
			settlement.TerminalIdempotencyKey ||
		*witnessFingerprint != settlement.TerminalEnvelopeDigest {
		return application.ErrTurnSettlementSourceConflict
	}
	witnessAcceptedAt := witnessCreatedAt.UTC()
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(
		witnessEnvelope,
	)
	if err != nil ||
		envelope.Kind != "event" ||
		envelope.MessageType != "agent_turn.terminal" ||
		string(envelope.WorkerSessionID) != settlement.WorkerSessionID ||
		string(envelope.WorkerRunID) != settlement.WorkerRunID ||
		string(envelope.AgentSessionID) != settlement.AgentSessionID ||
		string(envelope.AgentTurnID) != settlement.AgentTurnID ||
		string(envelope.RuntimeInstanceID) !=
			settlement.RuntimeInstanceID ||
		uint64(envelope.RuntimeGeneration) !=
			settlement.RuntimeGeneration ||
		envelope.ChannelEpoch != settlement.ChannelEpoch ||
		envelope.Sequence != settlement.TerminalEventSequence ||
		envelope.MessageID != settlement.TerminalMessageID ||
		envelope.IdempotencyKey !=
			settlement.TerminalIdempotencyKey {
		return application.ErrTurnSettlementSourceConflict
	}
	switch settlement.Source.Kind {
	case application.TurnSettlementSourceRuntimeEvent:
		if !settlement.Source.OccurredAt.Equal(witnessAcceptedAt) ||
			!settlement.AcceptedAt.Equal(witnessAcceptedAt) ||
			settlement.Source.Ref != settlement.TerminalMessageID ||
			settlement.Source.Digest !=
				settlement.TerminalEnvelopeDigest {
			return application.ErrTurnSettlementSourceConflict
		}
	case application.TurnSettlementSourceBackendDeadline:
		if witnessAcceptedAt.Before(settlement.Source.OccurredAt) {
			return application.ErrTurnSettlementSourceConflict
		}
	default:
		return application.ErrTurnSettlementSourceConflict
	}
	return nil
}

func ensureTerminalTurnSettlementHeader(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeControlChannelState,
	record application.AcceptRuntimeControlEventRecord,
	acceptedAt time.Time,
) error {
	envelope := record.DecodedEnvelope
	if envelope.Kind != "event" || envelope.MessageType != "agent_turn.terminal" {
		return nil
	}
	admission, err := loadTerminalSettlementAdmission(
		ctx,
		tx,
		state,
		envelope,
	)
	if err != nil {
		return err
	}
	settlementID, err := application.TurnSettlementID(string(envelope.AgentTurnID))
	if err != nil {
		return application.ErrTurnSettlementConflict
	}
	existing, err := readTurnSettlement(ctx, tx, settlementID, true)
	if err == nil {
		switch existing.Source.Kind {
		case application.TurnSettlementSourceRuntimeEvent:
			if !terminalSettlementMatchesEvent(
				existing,
				state,
				envelope,
				record.Command.Sequence,
				record.Command.MessageID,
				record.Command.IdempotencyKey,
				record.Fingerprint,
				acceptedAt,
			) {
				return application.ErrTurnSettlementConflict
			}
			return nil
		case application.TurnSettlementSourceBackendDeadline:
			if acceptedAt.Before(existing.Source.OccurredAt) {
				return application.ErrTurnSettlementConflict
			}
			if terminalEventWitnessEmpty(existing) {
				return attachTerminalEventWitness(
					ctx,
					tx,
					existing,
					state,
					envelope,
					record,
					acceptedAt,
				)
			}
			if !terminalSettlementMatchesEvent(
				existing,
				state,
				envelope,
				record.Command.Sequence,
				record.Command.MessageID,
				record.Command.IdempotencyKey,
				record.Fingerprint,
				acceptedAt,
			) {
				return application.ErrTurnSettlementConflict
			}
			return nil
		default:
			return application.ErrTurnSettlementConflict
		}
	}
	if !errors.Is(err, application.ErrTurnSettlementNotFound) {
		return err
	}

	if !acceptedAt.Before(admission.DeadlineAt) {
		deadlineRecord, err := application.NewBackendDeadlineClaimRecord(
			application.ClaimBackendDeadlineCommand{
				TenantID:          admission.TenantID,
				WorkerSessionID:   state.WorkerSessionID,
				WorkerRunID:       string(envelope.WorkerRunID),
				AgentSessionID:    string(envelope.AgentSessionID),
				AgentTurnID:       string(envelope.AgentTurnID),
				RuntimeInstanceID: state.RuntimeInstanceID,
				RuntimeGeneration: state.RuntimeGeneration,
				ChannelEpoch:      state.ChannelEpoch,
				DeadlineAt:        admission.DeadlineAt,
				SourceRef:         admission.SourceRef,
			},
			acceptedAt,
		)
		if err != nil {
			return err
		}
		preemption, err := lockRuntimeRecoveryForBackendDeadline(
			ctx,
			tx,
			deadlineRecord,
		)
		if err != nil {
			return err
		}
		if err := lockBackendDeadlineSourceGraph(
			ctx,
			tx,
			deadlineRecord,
		); err != nil {
			return err
		}
		source := deadlineRecord.Source
		if preemption.Found {
			source, err =
				application.BindBackendDeadlineRecoveryPreemption(
					source,
					preemption.IncidentID,
					preemption.Revision+1,
					acceptedAt,
				)
			if err != nil {
				return err
			}
		}
		source, _, err =
			application.ReconcileBackendDeadlineTerminalWitness(
				source,
				acceptedAt,
			)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO worker_turn_settlements
  (settlement_id,tenant_id,worker_session_id,worker_run_id,agent_session_id,
   agent_turn_id,runtime_instance_id,runtime_generation,channel_epoch,
   terminal_event_direction,terminal_event_sequence,terminal_message_id,
   terminal_idempotency_key,terminal_envelope_digest,
   source_kind,source_reason,source_occurred_at,source_ref,source_digest,
   source_state,source_state_digest,accepted_at,updated_at)
VALUES
  ($1,$2,$3,$4,$5,$6,$7,$8,$9,'event',$10,$11,$12,$13,
   $14,$15,$16,$17,$18,$19,$20,$21,$21)`,
			settlementID,
			admission.TenantID,
			state.WorkerSessionID,
			string(envelope.WorkerRunID),
			string(envelope.AgentSessionID),
			string(envelope.AgentTurnID),
			state.RuntimeInstanceID,
			state.RuntimeGeneration,
			state.ChannelEpoch,
			record.Command.Sequence,
			record.Command.MessageID,
			record.Command.IdempotencyKey,
			record.Fingerprint,
			source.Kind,
			source.Reason,
			source.OccurredAt,
			source.Ref,
			source.Digest,
			source.State,
			source.StateDigest,
			acceptedAt,
		); err != nil {
			return fmt.Errorf(
				"create Backend deadline settlement from terminal event: %w",
				err,
			)
		}
		if preemption.Found {
			if err := applyBackendDeadlineRecoveryPreemption(
				ctx,
				tx,
				preemption,
				deadlineRecord,
			); err != nil {
				return err
			}
		}
		return nil
	}

	source, err := application.NewRuntimeEventTurnSettlementSource(
		acceptedAt,
		record.Command.MessageID,
		record.Fingerprint,
	)
	if err != nil {
		return application.ErrTurnSettlementConflict
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO worker_turn_settlements
  (settlement_id,tenant_id,worker_session_id,worker_run_id,agent_session_id,
   agent_turn_id,runtime_instance_id,runtime_generation,channel_epoch,
   terminal_event_direction,terminal_event_sequence,terminal_message_id,
   terminal_idempotency_key,
   terminal_envelope_digest,source_kind,source_reason,source_occurred_at,
   source_ref,source_digest,source_state,source_state_digest,accepted_at,updated_at)
SELECT $1,session.tenant_id,session.worker_session_id,run.worker_run_id,
       turn.agent_session_id,turn.agent_turn_id,runtime.runtime_instance_id,
       runtime.generation,runtime.control_channel_epoch,'event',$8,$9,$10,$11,
       $14,$15,$16,$17,$18,$19,$20,$12,$12
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$3
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id=$5
 AND turn.agent_session_id=$4
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.runtime_instance_id=$6
 AND runtime.generation=$7
	AND runtime.control_channel_epoch=$13
WHERE session.worker_session_id=$2
ON CONFLICT DO NOTHING`,
		settlementID,
		state.WorkerSessionID,
		string(envelope.WorkerRunID),
		string(envelope.AgentSessionID),
		string(envelope.AgentTurnID),
		state.RuntimeInstanceID,
		state.RuntimeGeneration,
		record.Command.Sequence,
		record.Command.MessageID,
		record.Command.IdempotencyKey,
		record.Fingerprint,
		acceptedAt,
		state.ChannelEpoch,
		source.Kind,
		source.Reason,
		source.OccurredAt,
		source.Ref,
		source.Digest,
		source.State,
		source.StateDigest,
	)
	if err != nil {
		return fmt.Errorf("create terminal Turn settlement header: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrTurnSettlementConflict
	}
	return nil
}

type terminalSettlementAdmission struct {
	TenantID   string
	DeadlineAt time.Time
	SourceRef  string
}

func loadTerminalSettlementAdmission(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeControlChannelState,
	envelope contractsv1.RuntimeControlEnvelope,
) (terminalSettlementAdmission, error) {
	var result terminalSettlementAdmission
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,turn.deadline_at,command.scheduled_command_id
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_session_id=$3
 AND turn.agent_turn_id=$4
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$5
 AND runtime.generation=$6
 AND runtime.control_channel_epoch=$7
JOIN scheduled_commands command
  ON command.command_type='agent_turn_deadline'
 AND command.target_type='AgentTurn'
 AND command.target_id=turn.agent_turn_id
 AND command.due_at=turn.deadline_at
WHERE session.worker_session_id=$1`,
		state.WorkerSessionID,
		string(envelope.WorkerRunID),
		string(envelope.AgentSessionID),
		string(envelope.AgentTurnID),
		state.RuntimeInstanceID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
	).Scan(
		&result.TenantID,
		&result.DeadlineAt,
		&result.SourceRef,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return terminalSettlementAdmission{},
			application.ErrTurnSettlementConflict
	}
	if err != nil {
		return terminalSettlementAdmission{}, fmt.Errorf(
			"load terminal settlement admission: %w",
			err,
		)
	}
	result.DeadlineAt = result.DeadlineAt.UTC()
	return result, nil
}

func terminalEventWitnessEmpty(
	settlement application.TurnSettlement,
) bool {
	return settlement.RuntimeInstanceID == "" &&
		settlement.RuntimeGeneration == 0 &&
		settlement.ChannelEpoch == 0 &&
		settlement.TerminalEventSequence == 0 &&
		settlement.TerminalMessageID == "" &&
		settlement.TerminalIdempotencyKey == "" &&
		settlement.TerminalEnvelopeDigest == ""
}

func attachTerminalEventWitness(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	state application.RuntimeControlChannelState,
	envelope contractsv1.RuntimeControlEnvelope,
	record application.AcceptRuntimeControlEventRecord,
	acceptedAt time.Time,
) error {
	if settlement.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline ||
		acceptedAt.Before(settlement.Source.OccurredAt) ||
		settlement.WorkerSessionID != state.WorkerSessionID ||
		settlement.WorkerRunID != string(envelope.WorkerRunID) ||
		settlement.AgentSessionID != string(envelope.AgentSessionID) ||
		settlement.AgentTurnID != string(envelope.AgentTurnID) {
		return application.ErrTurnSettlementConflict
	}
	source, sourceAdvanced, err :=
		application.ReconcileBackendDeadlineTerminalWitness(
			settlement.Source,
			acceptedAt,
		)
	if err != nil {
		return application.ErrTurnSettlementConflict
	}
	if !sourceAdvanced {
		tag, err := tx.Exec(ctx, `
UPDATE worker_turn_settlements
SET runtime_instance_id=$2,
    runtime_generation=$3,
    channel_epoch=$4,
    terminal_event_direction='event',
    terminal_event_sequence=$5,
    terminal_message_id=$6,
    terminal_idempotency_key=$7,
    terminal_envelope_digest=$8,
    revision=revision+1,
    updated_at=GREATEST(updated_at,$9)
WHERE settlement_id=$1
  AND revision=$10
  AND source_kind='backend_deadline'
  AND source_digest=$11
  AND source_state_digest=$12
  AND runtime_instance_id IS NULL
  AND runtime_generation IS NULL
  AND channel_epoch IS NULL
  AND terminal_event_direction IS NULL
  AND terminal_event_sequence IS NULL
  AND terminal_message_id IS NULL
  AND terminal_idempotency_key IS NULL
  AND terminal_envelope_digest IS NULL`,
			settlement.SettlementID,
			state.RuntimeInstanceID,
			state.RuntimeGeneration,
			state.ChannelEpoch,
			record.Command.Sequence,
			record.Command.MessageID,
			record.Command.IdempotencyKey,
			record.Fingerprint,
			acceptedAt,
			settlement.Revision,
			settlement.Source.Digest,
			settlement.Source.StateDigest,
		)
		if err != nil {
			return fmt.Errorf(
				"attach forced terminal event to Backend deadline settlement: %w",
				err,
			)
		}
		if tag.RowsAffected() != 1 {
			return application.ErrTurnSettlementRevisionConflict
		}
		return nil
	}
	tag, err := tx.Exec(ctx, `
UPDATE worker_turn_settlements
SET runtime_instance_id=$2,
    runtime_generation=$3,
    channel_epoch=$4,
    terminal_event_direction='event',
    terminal_event_sequence=$5,
    terminal_message_id=$6,
    terminal_idempotency_key=$7,
    terminal_envelope_digest=$8,
    source_state=$9,
    source_state_digest=$10,
    revision=revision+1,
    updated_at=GREATEST(updated_at,$11)
WHERE settlement_id=$1
  AND revision=$12
  AND runtime_instance_id IS NULL
  AND runtime_generation IS NULL
  AND channel_epoch IS NULL
  AND terminal_event_direction IS NULL
  AND terminal_event_sequence IS NULL
  AND terminal_message_id IS NULL
  AND terminal_idempotency_key IS NULL
  AND terminal_envelope_digest IS NULL
  AND source_kind='backend_deadline'
  AND source_digest=$13
  AND source_state_digest=$14`,
		settlement.SettlementID,
		state.RuntimeInstanceID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
		record.Command.Sequence,
		record.Command.MessageID,
		record.Command.IdempotencyKey,
		record.Fingerprint,
		source.State,
		source.StateDigest,
		acceptedAt,
		settlement.Revision,
		settlement.Source.Digest,
		settlement.Source.StateDigest,
	)
	if err != nil {
		return fmt.Errorf(
			"attach terminal event to Backend deadline settlement: %w",
			err,
		)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrTurnSettlementRevisionConflict
	}
	return nil
}

type terminalSettlementApplyPlan struct {
	Settlement      application.TurnSettlement
	Terminal        bool
	Duplicate       bool
	BackendDeadline *backendDeadlineSourceApplyPreparation
}

func lockTerminalSettlementForApply(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeControlChannelState,
	message application.RuntimeControlMessage,
	record application.ApplyRuntimeControlEventRecord,
) (terminalSettlementApplyPlan, error) {
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil {
		return terminalSettlementApplyPlan{}, application.ErrRuntimeControlMessageConflict
	}
	if envelope.Kind != "event" || envelope.MessageType != "agent_turn.terminal" {
		return terminalSettlementApplyPlan{}, nil
	}
	settlementID, err := application.TurnSettlementID(string(envelope.AgentTurnID))
	if err != nil {
		return terminalSettlementApplyPlan{}, application.ErrTurnSettlementConflict
	}
	settlement, err := readTurnSettlement(ctx, tx, settlementID, true)
	if err != nil {
		if errors.Is(err, application.ErrTurnSettlementNotFound) {
			return terminalSettlementApplyPlan{}, application.ErrTurnSettlementConflict
		}
		return terminalSettlementApplyPlan{}, err
	}
	if !terminalSettlementMatchesEvent(
		settlement,
		state,
		envelope,
		message.Sequence,
		message.MessageID,
		message.IdempotencyKey,
		message.Fingerprint,
		message.CreatedAt,
	) {
		return terminalSettlementApplyPlan{}, application.ErrTurnSettlementConflict
	}
	plan := terminalSettlementApplyPlan{
		Settlement: settlement,
		Terminal:   true,
		Duplicate:  message.EffectStatus == "applied",
	}
	if plan.Duplicate {
		if settlement.TerminalEventDisposition != record.EffectDisposition ||
			settlement.TerminalApplyOutcomeDigest != record.OutcomeDigest {
			return terminalSettlementApplyPlan{}, application.ErrTurnSettlementConflict
		}
		switch record.EffectDisposition {
		case application.RuntimeControlEffectDispositionCommit:
			if turnSettlementPhaseRank(settlement.Phase) <
				turnSettlementPhaseRank(application.TurnSettlementTerminalEventApplied) {
				return terminalSettlementApplyPlan{}, application.ErrTurnSettlementPhaseConflict
			}
			if err := application.ValidateTurnSettlementCheckpointProof(
				settlement.Phase,
				settlement.PhaseState,
			); err != nil {
				return terminalSettlementApplyPlan{}, err
			}
		case application.RuntimeControlEffectDispositionIgnore:
			prepared, special, err :=
				lockBackendDeadlineTerminalSourceApply(
					ctx,
					tx,
					settlement,
					record,
					true,
				)
			if err != nil {
				return terminalSettlementApplyPlan{}, err
			}
			if special {
				plan.BackendDeadline = &prepared
				return plan, nil
			}
			if settlement.Phase != application.TurnSettlementCompleted {
				return terminalSettlementApplyPlan{}, application.ErrTurnSettlementPhaseConflict
			}
		default:
			return terminalSettlementApplyPlan{}, application.ErrTurnSettlementConflict
		}
		return plan, nil
	}
	switch record.EffectDisposition {
	case application.RuntimeControlEffectDispositionCommit:
		if settlement.Phase != application.TurnSettlementTaskSettled {
			return terminalSettlementApplyPlan{}, application.ErrTurnSettlementPhaseConflict
		}
		if err := application.ValidateTurnSettlementCheckpointProof(
			application.TurnSettlementTaskSettled,
			settlement.PhaseState,
		); err != nil {
			return terminalSettlementApplyPlan{}, err
		}
		authority := record.Command.TurnSettlement
		if authority == nil ||
			authority.SettlementID != settlement.SettlementID ||
			authority.ExpectedRevision != settlement.Revision ||
			authority.LeaseOwnerID != settlement.LeaseOwnerID ||
			authority.LeaseFence != settlement.LeaseFence ||
			!settlement.LeaseExpiresAt.After(record.AppliedAt) {
			return terminalSettlementApplyPlan{},
				application.ErrTurnSettlementLeaseStale
		}
	case application.RuntimeControlEffectDispositionIgnore:
		prepared, special, err :=
			lockBackendDeadlineTerminalSourceApply(
				ctx,
				tx,
				settlement,
				record,
				false,
			)
		if err != nil {
			return terminalSettlementApplyPlan{}, err
		}
		if special {
			plan.BackendDeadline = &prepared
			return plan, nil
		}
		if settlement.Phase != application.TurnSettlementAccepted {
			return terminalSettlementApplyPlan{}, application.ErrTurnSettlementPhaseConflict
		}
	default:
		return terminalSettlementApplyPlan{}, application.ErrTurnSettlementConflict
	}
	return plan, nil
}

func lockBackendDeadlineTerminalSourceApply(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	record application.ApplyRuntimeControlEventRecord,
	replay bool,
) (
	backendDeadlineSourceApplyPreparation,
	bool,
	error,
) {
	if settlement.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline {
		return backendDeadlineSourceApplyPreparation{}, false, nil
	}
	state, err := application.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil {
		return backendDeadlineSourceApplyPreparation{}, true, err
	}
	if state.Phase !=
		application.BackendDeadlineSourceCleanupConfirmed {
		return backendDeadlineSourceApplyPreparation{}, false, nil
	}
	authority := record.Command.TurnSettlement
	if authority == nil ||
		authority.SettlementID != settlement.SettlementID {
		return backendDeadlineSourceApplyPreparation{},
			true,
			application.ErrTurnSettlementLeaseStale
	}
	sourceRecord := application.ApplyBackendDeadlineSourceRecord{
		Command: application.ApplyBackendDeadlineSourceCommand{
			SettlementID:              authority.SettlementID,
			ExpectedRevision:          authority.ExpectedRevision,
			ExpectedSourceDigest:      settlement.Source.Digest,
			ExpectedSourceStateDigest: settlement.Source.StateDigest,
			LeaseOwnerID:              authority.LeaseOwnerID,
			LeaseFence:                authority.LeaseFence,
		},
		AppliedAt: record.AppliedAt,
	}
	if err := application.ValidateApplyBackendDeadlineSourceRecord(
		sourceRecord,
	); err != nil {
		return backendDeadlineSourceApplyPreparation{}, true, err
	}
	targetAuthority, err :=
		application.ResolveBackendDeadlineSourceApplyTarget(
			settlement.PhaseState,
			settlement.Source,
		)
	if err != nil {
		return backendDeadlineSourceApplyPreparation{}, true, err
	}
	target, err := lockBackendDeadlineSourceApplyTarget(
		ctx,
		tx,
		settlement,
		state,
	)
	if err != nil {
		return backendDeadlineSourceApplyPreparation{}, true, err
	}
	if replay {
		if settlement.Phase !=
			application.TurnSettlementTerminalEventApplied ||
			terminalEventWitnessEmpty(settlement) ||
			settlement.TerminalEventDisposition !=
				application.RuntimeControlEffectDispositionIgnore ||
			!settlement.CompletedAt.IsZero() {
			return backendDeadlineSourceApplyPreparation{},
				true,
				application.ErrTurnSettlementSourceConflict
		}
		if err := validateBackendDeadlineSourceApplyReplayTarget(
			settlement,
			state,
			target,
			targetAuthority,
			sourceRecord,
		); err != nil {
			return backendDeadlineSourceApplyPreparation{}, true, err
		}
		if err := application.
			ValidateBackendDeadlineSourceAppliedCheckpointProof(
				settlement.Source,
				settlement.PhaseState,
				sourceRecord.Command,
			); err != nil {
			return backendDeadlineSourceApplyPreparation{}, true, err
		}
		return backendDeadlineSourceApplyPreparation{
			State:           state,
			TargetAuthority: targetAuthority,
		}, true, nil
	}
	prepared, err := prepareBackendDeadlineSourceApply(
		ctx,
		tx,
		settlement,
		state,
		target,
		targetAuthority,
		sourceRecord,
		true,
	)
	if err != nil {
		return backendDeadlineSourceApplyPreparation{}, true, err
	}
	return prepared, true, nil
}

func advanceTerminalSettlementForApply(
	ctx context.Context,
	tx platformpostgres.Tx,
	plan terminalSettlementApplyPlan,
	record application.ApplyRuntimeControlEventRecord,
) error {
	if !plan.Terminal || plan.Duplicate {
		return nil
	}
	target := application.TurnSettlementTerminalEventApplied
	var completedAt any
	var leaseOwner any = nullableString(plan.Settlement.LeaseOwnerID)
	var leaseExpires any
	if !plan.Settlement.LeaseExpiresAt.IsZero() {
		leaseExpires = plan.Settlement.LeaseExpiresAt
	}
	if record.EffectDisposition ==
		application.RuntimeControlEffectDispositionIgnore &&
		plan.BackendDeadline == nil {
		target = application.TurnSettlementCompleted
		completedAt = record.AppliedAt
		leaseOwner = nil
		leaseExpires = nil
	}
	phaseState := plan.Settlement.PhaseState
	phaseStateDigest := plan.Settlement.PhaseStateDigest
	if plan.BackendDeadline != nil {
		if record.EffectDisposition !=
			application.RuntimeControlEffectDispositionIgnore {
			return application.ErrTurnSettlementConflict
		}
		if err := applyBackendDeadlineSourceTarget(
			ctx,
			tx,
			plan.Settlement,
			*plan.BackendDeadline,
			record.AppliedAt,
		); err != nil {
			return err
		}
		phaseState = plan.BackendDeadline.Checkpoint
		phaseStateDigest =
			plan.BackendDeadline.CheckpointDigest
		target = application.TurnSettlementTerminalEventApplied
		completedAt = nil
	} else if record.EffectDisposition ==
		application.RuntimeControlEffectDispositionCommit {
		var err error
		phaseState, phaseStateDigest, err =
			appendTerminalSourceAppliedProof(
				plan.Settlement.PhaseState,
			)
		if err != nil {
			return err
		}
		if err := application.ValidateTurnSettlementCheckpointProof(
			application.TurnSettlementTerminalEventApplied,
			phaseState,
		); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
UPDATE worker_turn_settlements
SET phase=$2,terminal_event_disposition=$3,terminal_apply_outcome_digest=$4,
    terminal_event_applied_at=$5,completed_at=$6,
    lease_owner_id=$7,lease_expires_at=$8,
    phase_state=$9,phase_state_digest=$10,
    revision=revision+1,updated_at=$5
WHERE settlement_id=$1 AND revision=$11 AND phase=$12`,
		plan.Settlement.SettlementID,
		target,
		record.EffectDisposition,
		record.OutcomeDigest,
		record.AppliedAt,
		completedAt,
		leaseOwner,
		leaseExpires,
		phaseState,
		phaseStateDigest,
		plan.Settlement.Revision,
		plan.Settlement.Phase,
	)
	if err != nil {
		return fmt.Errorf("advance terminal Turn settlement inbox phase: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return application.ErrTurnSettlementRevisionConflict
	}
	return nil
}

func appendTerminalSourceAppliedProof(
	current json.RawMessage,
) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil ||
		fields == nil {
		return nil, "",
			application.ErrTurnSettlementCheckpointConflict
	}
	if _, exists := fields["terminal_source_applied"]; exists {
		return nil, "",
			application.ErrTurnSettlementCheckpointConflict
	}
	fields["terminal_source_applied"] = json.RawMessage(`true`)
	candidate, err := json.Marshal(fields)
	if err != nil {
		return nil, "", fmt.Errorf(
			"encode terminal Turn settlement source proof: %w",
			err,
		)
	}
	canonical, digest, err :=
		application.CanonicalizeTurnSettlementCheckpoint(candidate)
	if err != nil {
		return nil, "", err
	}
	if err := application.ValidateTurnSettlementCheckpointExtension(
		current,
		canonical,
	); err != nil {
		return nil, "", err
	}
	return canonical, digest, nil
}

func terminalSettlementMatchesEvent(
	settlement application.TurnSettlement,
	state application.RuntimeControlChannelState,
	envelope contractsv1.RuntimeControlEnvelope,
	sequence uint64,
	messageID string,
	idempotencyKey string,
	fingerprint string,
	acceptedAt time.Time,
) bool {
	if settlement.WorkerSessionID != state.WorkerSessionID ||
		settlement.WorkerRunID != string(envelope.WorkerRunID) ||
		settlement.AgentSessionID != string(envelope.AgentSessionID) ||
		settlement.AgentTurnID != string(envelope.AgentTurnID) ||
		settlement.RuntimeInstanceID != state.RuntimeInstanceID ||
		settlement.RuntimeGeneration != state.RuntimeGeneration ||
		settlement.ChannelEpoch != state.ChannelEpoch ||
		settlement.TerminalEventSequence != sequence ||
		settlement.TerminalMessageID != messageID ||
		settlement.TerminalIdempotencyKey != idempotencyKey ||
		settlement.TerminalEnvelopeDigest != fingerprint {
		return false
	}
	switch settlement.Source.Kind {
	case application.TurnSettlementSourceRuntimeEvent:
		return settlement.Source.Reason == "runtime_terminal" &&
			settlement.Source.OccurredAt.Equal(acceptedAt) &&
			settlement.Source.OccurredAt.Equal(settlement.AcceptedAt) &&
			settlement.Source.Ref == messageID &&
			settlement.Source.Digest == fingerprint
	case application.TurnSettlementSourceBackendDeadline:
		return settlement.Source.Reason == "agent_turn_deadline" &&
			!acceptedAt.Before(settlement.Source.OccurredAt)
	default:
		return false
	}
}

func turnSettlementPhaseRank(phase application.TurnSettlementPhase) int {
	switch phase {
	case application.TurnSettlementAccepted:
		return 1
	case application.TurnSettlementEvidencePrepared:
		return 2
	case application.TurnSettlementDomainCommitted:
		return 3
	case application.TurnSettlementEvidencePublished:
		return 4
	case application.TurnSettlementTaskSettled:
		return 5
	case application.TurnSettlementTerminalEventApplied:
		return 6
	case application.TurnSettlementContinuationSettled:
		return 7
	case application.TurnSettlementCompleted:
		return 8
	default:
		return 0
	}
}

var _ application.TurnSettlementStore = (*TurnSettlementRepository)(nil)
