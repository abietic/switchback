package postgres

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type RuntimeControlJournal struct {
	pool        *pgxpool.Pool
	observation ObservationCommitParticipant
}

func NewRuntimeControlJournal(pool *pgxpool.Pool) *RuntimeControlJournal {
	return &RuntimeControlJournal{pool: pool}
}

func (journal *RuntimeControlJournal) BindRuntimeControlChannel(
	ctx context.Context,
	record application.BindRuntimeControlChannelRecord,
) (application.RuntimeControlChannelState, error) {
	var result application.RuntimeControlChannelState
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		if err := lockExactRuntimeControlIdentity(ctx, tx, runtimeControlIdentity{
			workerSessionID: record.Command.WorkerSessionID,
			runtimeID:       record.Command.RuntimeInstanceID,
			generation:      record.Command.RuntimeGeneration,
			channelEpoch:    record.Command.ChannelEpoch,
		}); err != nil {
			return err
		}
		state, err := readRuntimeControlChannel(ctx, tx, record.Command.RuntimeInstanceID, true)
		if err == nil {
			if state.WorkerSessionID != record.Command.WorkerSessionID ||
				state.RuntimeGeneration != record.Command.RuntimeGeneration ||
				state.ChannelEpoch != record.Command.ChannelEpoch {
				return application.ErrRuntimeControlIdentityConflict
			}
			if !constantTimeStringEqual(state.CredentialDigest, record.CredentialDigest) {
				return application.ErrRuntimeControlCredentialConflict
			}
			result = state
			return nil
		}
		if !errors.Is(err, platformpostgres.ErrNotFound) {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO runtime_control_channels
  (runtime_instance_id,worker_session_id,runtime_generation,channel_epoch,credential_digest,
   status,revision,bound_at,updated_at)
VALUES ($1,$2,$3,$4,$5,'bound',1,$6,$6)`,
			record.Command.RuntimeInstanceID,
			record.Command.WorkerSessionID,
			record.Command.RuntimeGeneration,
			record.Command.ChannelEpoch,
			record.CredentialDigest,
			record.BoundAt,
		); err != nil {
			return fmt.Errorf("bind Runtime control channel: %w", err)
		}
		state, err = readRuntimeControlChannel(ctx, tx, record.Command.RuntimeInstanceID, false)
		if err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}

func (journal *RuntimeControlJournal) ClaimRuntimeControlStream(
	ctx context.Context,
	record application.ClaimRuntimeControlStreamRecord,
) (application.RuntimeControlReplayPlan, error) {
	var result application.RuntimeControlReplayPlan
	err := InCapacityTransaction(ctx, journal.pool, func(tx pgx.Tx, capacity *CapacityTx) error {
		identity := runtimeControlIdentity{
			workerSessionID: record.Command.WorkerSessionID,
			runtimeID:       record.Command.RuntimeInstanceID,
			generation:      record.Command.RuntimeGeneration,
			channelEpoch:    record.Command.ChannelEpoch,
		}
		if err := authenticateRuntimeControlSettlementGuardClaim(
			ctx,
			tx,
			identity,
			record.CredentialDigest,
		); err != nil {
			return err
		}
		if err := guardRuntimeControlSettlementAdmission(
			ctx,
			tx,
			identity,
			record.Command.ActiveTurn,
			nil,
			record.ClaimedAt,
		); err != nil {
			return err
		}
		if err := capacity.authorizeControlStarts(ctx, identity.runtimeID, record.Command.ActiveTurn); err != nil {
			return err
		}
		state, err := lockRuntimeControlChannel(ctx, tx, identity)
		if err != nil {
			return err
		}
		if state.Status == "fenced" {
			return application.ErrRuntimeControlStaleOwner
		}
		if !constantTimeStringEqual(state.CredentialDigest, record.CredentialDigest) {
			return application.ErrRuntimeControlCredentialConflict
		}
		if state.ProcessNonce != "" && state.ProcessNonce != record.Command.ProcessNonce {
			return application.ErrRuntimeControlIdentityConflict
		}
		switch {
		case record.Command.StreamAttempt < state.StreamAttempt:
			return application.ErrRuntimeControlStreamAttemptStale
		case record.Command.StreamAttempt == state.StreamAttempt:
			if state.Status == "disconnected" || state.StreamAttempt == 0 {
				return application.ErrRuntimeControlStreamAttemptStale
			}
			if state.OwnerID != record.Command.OwnerID {
				return application.ErrRuntimeControlLeaseHeld
			}
		default:
			state.ProcessNonce = record.Command.ProcessNonce
			state.StreamAttempt = record.Command.StreamAttempt
			state.OwnerID = record.Command.OwnerID
			state.OwnerFence++
		}
		if record.Command.WorkerCommandLastSequence < state.CommandAcknowledgedSequence ||
			record.Command.WorkerCommandLastSequence > state.CommandCommittedSequence ||
			record.Command.WorkerCommandAppliedSequence < state.CommandAppliedSequence ||
			record.Command.WorkerCommandAppliedSequence > record.Command.WorkerCommandLastSequence {
			return application.ErrRuntimeControlCursorConflict
		}
		commandDigest, err := runtimeControlCursorDigest(
			ctx,
			tx,
			state.RuntimeInstanceID,
			application.RuntimeControlDirectionCommand,
			record.Command.WorkerCommandLastSequence,
		)
		if err != nil || commandDigest != record.Command.WorkerCommandCursorDigest {
			return application.ErrRuntimeControlCursorConflict
		}
		if record.Command.WorkerEventBackendAcknowledgedSequence > state.EventAcknowledgedSequence ||
			record.Command.WorkerEventLastSequence < state.EventCommittedSequence ||
			record.Command.WorkerEventBackendAcknowledgedSequence > record.Command.WorkerEventLastSequence {
			return application.ErrRuntimeControlCursorConflict
		}
		if record.Command.WorkerEventLastSequence == state.EventCommittedSequence {
			eventDigest, err := runtimeControlCursorDigest(
				ctx,
				tx,
				state.RuntimeInstanceID,
				application.RuntimeControlDirectionEvent,
				record.Command.WorkerEventLastSequence,
			)
			if err != nil || eventDigest != record.Command.WorkerEventCursorDigest {
				return application.ErrRuntimeControlCursorConflict
			}
		}
		if record.Command.WorkerCommandLastSequence > state.CommandAcknowledgedSequence {
			if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET acknowledged_at=COALESCE(acknowledged_at,$3),
    effect_status=CASE WHEN effect_status='journaled' THEN 'received' ELSE effect_status END
WHERE runtime_instance_id=$1 AND direction='command' AND sequence <= $2`,
				state.RuntimeInstanceID,
				record.Command.WorkerCommandLastSequence,
				record.ClaimedAt,
			); err != nil {
				return fmt.Errorf("acknowledge Runtime control commands during resume: %w", err)
			}
			state.CommandAcknowledgedSequence = record.Command.WorkerCommandLastSequence
		}
		turnConsumed, err := reconcileClaimedActiveTurn(ctx, tx, &state, record)
		if err != nil {
			return err
		}
		if err := advanceRuntimeControlCommandApplied(
			ctx, tx, &state, record.Command.WorkerCommandAppliedSequence,
			record.Command.ActiveTurn, record.ClaimedAt,
		); err != nil {
			return err
		}
		activeSnapshot, err := marshalNullableActiveTurn(state.ActiveEffectSnapshot)
		if err != nil {
			return err
		}
		reportedSnapshot, err := marshalNullableActiveTurn(state.ReportedActiveTurn)
		if err != nil {
			return err
		}
		var eventReplayCheckpointSequence *uint64
		var eventReplayCheckpointDigest *string
		var eventReplayTargetSequence *uint64
		var eventReplayTargetDigest *string
		if record.Command.WorkerEventLastSequence > state.EventCommittedSequence {
			if state.EventReplayCheckpointSequence == 0 {
				state.EventReplayCheckpointSequence = record.Command.WorkerEventLastSequence
				state.EventReplayCheckpointDigest = record.Command.WorkerEventCursorDigest
			} else if state.EventReplayCheckpointSequence <= state.EventCommittedSequence {
				verifiedDigest, err := runtimeControlCursorDigest(
					ctx,
					tx,
					state.RuntimeInstanceID,
					application.RuntimeControlDirectionEvent,
					state.EventReplayCheckpointSequence,
				)
				if err != nil || verifiedDigest != state.EventReplayCheckpointDigest {
					return application.ErrRuntimeControlCursorConflict
				}
				state.EventReplayCheckpointSequence = record.Command.WorkerEventLastSequence
				state.EventReplayCheckpointDigest = record.Command.WorkerEventCursorDigest
			}
			if record.Command.WorkerEventLastSequence < state.EventReplayTargetSequence ||
				(record.Command.WorkerEventLastSequence == state.EventReplayTargetSequence &&
					record.Command.WorkerEventCursorDigest != state.EventReplayTargetDigest) ||
				(record.Command.WorkerEventLastSequence == state.EventReplayCheckpointSequence &&
					record.Command.WorkerEventCursorDigest != state.EventReplayCheckpointDigest) {
				return application.ErrRuntimeControlCursorConflict
			}
			targetSequence := record.Command.WorkerEventLastSequence
			targetDigest := record.Command.WorkerEventCursorDigest
			eventReplayTargetSequence = &targetSequence
			eventReplayTargetDigest = &targetDigest
			state.EventReplayTargetSequence = targetSequence
			state.EventReplayTargetDigest = targetDigest
			checkpointSequence := state.EventReplayCheckpointSequence
			checkpointDigest := state.EventReplayCheckpointDigest
			eventReplayCheckpointSequence = &checkpointSequence
			eventReplayCheckpointDigest = &checkpointDigest
		} else if state.EventReplayTargetSequence > 0 {
			if record.Command.WorkerEventLastSequence != state.EventReplayTargetSequence ||
				record.Command.WorkerEventCursorDigest != state.EventReplayTargetDigest {
				return application.ErrRuntimeControlCursorConflict
			}
			checkpointSequence := state.EventReplayCheckpointSequence
			checkpointDigest := state.EventReplayCheckpointDigest
			targetSequence := state.EventReplayTargetSequence
			targetDigest := state.EventReplayTargetDigest
			eventReplayCheckpointSequence = &checkpointSequence
			eventReplayCheckpointDigest = &checkpointDigest
			eventReplayTargetSequence = &targetSequence
			eventReplayTargetDigest = &targetDigest
		}
		if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET process_nonce=$2,stream_attempt=$3,owner_id=$4,owner_fence=$5,
    resume_claim_revision=revision+1,
    command_acknowledged_sequence=$6,command_applied_sequence=$7,
    event_replay_checkpoint_sequence=$8,event_replay_checkpoint_digest=$9,
    event_replay_target_sequence=$10,event_replay_target_digest=$11,
    active_effect_message_id=$12,active_effect_agent_turn_id=$13,
    active_effect_command_sequence=$14,active_effect_phase=$15,active_effect_snapshot=$16,
    reported_active_turn=$17,terminal_proof_pending=$18,
	    resume_ready_digest=NULL,resume_completed_revision=NULL,resume_completed_at=NULL,
	    resume_completed_state=NULL,
    status='resuming',revision=revision+1,
    claimed_at=$19,updated_at=$19,disconnected_at=NULL
WHERE runtime_instance_id=$1
RETURNING revision`,
			state.RuntimeInstanceID,
			state.ProcessNonce,
			state.StreamAttempt,
			state.OwnerID,
			state.OwnerFence,
			state.CommandAcknowledgedSequence,
			state.CommandAppliedSequence,
			eventReplayCheckpointSequence,
			eventReplayCheckpointDigest,
			eventReplayTargetSequence,
			eventReplayTargetDigest,
			nullableString(state.ActiveEffectMessageID),
			nullableString(state.ActiveEffectAgentTurnID),
			nullableRuntimeControlUint64(state.ActiveEffectCommandSequence),
			nullableString(string(state.ActiveEffectPhase)),
			activeSnapshot,
			reportedSnapshot,
			state.TerminalProofPending,
			record.ClaimedAt,
		).Scan(&state.Revision); err != nil {
			return fmt.Errorf("claim Runtime control stream: %w", err)
		}
		state.ResumeClaimRevision = state.Revision
		state.Status = "resuming"
		state.ResumeReadyDigest = ""
		state.ResumeCompletedRevision = 0
		state.ResumeCompletedAt = time.Time{}
		pending, err := loadRuntimeControlCommandsAfter(
			ctx,
			tx,
			state.RuntimeInstanceID,
			state.CommandAcknowledgedSequence,
		)
		if err != nil {
			return err
		}
		pendingEvents, err := loadRuntimeControlPendingEvents(
			ctx, tx, state.RuntimeInstanceID, state.EventAppliedSequence,
		)
		if err != nil {
			return err
		}
		result = application.RuntimeControlReplayPlan{
			Channel:              state,
			PendingCommands:      pending,
			PendingEvents:        pendingEvents,
			EventReplayFrom:      state.EventCommittedSequence + 1,
			EventReplayThrough:   record.Command.WorkerEventLastSequence,
			ReportedActiveTurn:   state.ReportedActiveTurn,
			TerminalProofPending: state.TerminalProofPending,
			TurnConsumed:         turnConsumed,
		}
		return nil
	})
	return result, err
}

func (journal *RuntimeControlJournal) ReserveRuntimeControlCommand(
	ctx context.Context,
	record application.ReserveRuntimeControlCommandRecord,
) (application.RuntimeControlMessage, error) {
	var result application.RuntimeControlMessage
	err := InCapacityTransaction(ctx, journal.pool, func(tx pgx.Tx, capacity *CapacityTx) error {
		if record.LogicalEnvelope.MessageType == "agent_turn.start" {
			e, a := record.LogicalEnvelope, record.Command.Authority
			if string(e.WorkerSessionID) != a.WorkerSessionID || string(e.RuntimeInstanceID) != a.RuntimeInstanceID || uint64(e.RuntimeGeneration) != a.RuntimeGeneration || e.ChannelEpoch != a.ChannelEpoch {
				return application.CapacityBindingMismatch
			}
			if err := capacity.authorizeStartEnvelope(ctx, record.LogicalEnvelope, true); err != nil {
				return err
			}
		}
		state, err := lockRuntimeControlAuthority(ctx, tx, record.Command.Authority)
		if err != nil {
			return err
		}
		if state.Status != "connected" {
			return application.ErrRuntimeControlStaleOwner
		}
		result, err = reserveRuntimeControlCommandLocked(
			ctx,
			tx,
			state,
			record,
		)
		return err
	})
	return result, err
}

func reserveRuntimeControlCommandLocked(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeControlChannelState,
	record application.ReserveRuntimeControlCommandRecord,
) (application.RuntimeControlMessage, error) {
	existing, found, err := findRuntimeControlMessage(
		ctx,
		tx,
		state.RuntimeInstanceID,
		application.RuntimeControlDirectionCommand,
		record.Command.MessageID,
		record.Command.IdempotencyKey,
	)
	if err != nil {
		return application.RuntimeControlMessage{}, err
	}
	if found {
		existingEnvelope, decodeErr :=
			contractsv1.DecodeRuntimeControlEnvelope(existing.Envelope)
		if decodeErr != nil ||
			existing.MessageID != record.Command.MessageID ||
			existing.IdempotencyKey != record.Command.IdempotencyKey ||
			!sameRuntimeControlLogicalEnvelope(
				existingEnvelope,
				record.LogicalEnvelope,
			) {
			return application.RuntimeControlMessage{},
				application.ErrRuntimeControlMessageConflict
		}
		existing.Duplicate = true
		return existing, nil
	}
	if record.LogicalEnvelope.Kind == "command" &&
		record.LogicalEnvelope.MessageType == "agent_turn.start" {
		var pending bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM worker_turn_settlements
    WHERE worker_session_id=$1
      AND phase <> 'completed'
)`,
			state.WorkerSessionID,
		).Scan(&pending); err != nil {
			return application.RuntimeControlMessage{}, fmt.Errorf(
				"check pending Turn settlement before AgentTurn start: %w",
				err,
			)
		}
		if pending {
			return application.RuntimeControlMessage{},
				application.ErrRuntimeControlPendingTurnSettlement
		}
	}
	sequence := state.CommandCommittedSequence + 1
	envelope := record.LogicalEnvelope
	envelope.Sequence = sequence
	envelope.AckSequence = state.EventCommittedSequence
	envelope.SentAt = record.ReservedAt
	if err := envelope.Validate(); err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"validate reserved Runtime control command: %w",
			err,
		)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"marshal reserved Runtime control command: %w",
			err,
		)
	}
	fingerprint, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		return application.RuntimeControlMessage{}, err
	}
	cursorDigest, err := contractsv1.AdvanceRuntimeControlCursor(
		state.CommandCommittedDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		envelope,
	)
	if err != nil {
		return application.RuntimeControlMessage{}, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO runtime_control_messages
  (runtime_instance_id,direction,sequence,message_id,idempotency_key,fingerprint,
   cursor_digest,envelope,outcome,effect_status,created_at)
VALUES ($1,'command',$2,$3,$4,$5,$6,$7,NULL,'journaled',$8)`,
		state.RuntimeInstanceID,
		sequence,
		record.Command.MessageID,
		record.Command.IdempotencyKey,
		fingerprint,
		cursorDigest,
		encoded,
		record.ReservedAt,
	); err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"journal Runtime control command: %w",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_control_channels
SET command_committed_sequence=$2,command_committed_digest=$3,
    revision=revision+1,updated_at=$4
WHERE runtime_instance_id=$1`,
		state.RuntimeInstanceID,
		sequence,
		cursorDigest,
		record.ReservedAt,
	); err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"advance Runtime control command cursor: %w",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_instances
SET last_command_sequence=$2,revision=revision+$3
WHERE runtime_instance_id=$1`,
		state.RuntimeInstanceID,
		sequence,
		runtimeControlLifecycleRevisionDelta(record.LogicalEnvelope),
	); err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"advance Runtime command aggregate cursor: %w",
			err,
		)
	}
	return application.RuntimeControlMessage{
		Direction:      application.RuntimeControlDirectionCommand,
		Sequence:       sequence,
		MessageID:      record.Command.MessageID,
		IdempotencyKey: record.Command.IdempotencyKey,
		Fingerprint:    fingerprint,
		CursorDigest:   cursorDigest,
		Envelope:       append([]byte(nil), encoded...),
		EffectStatus:   "journaled",
		CreatedAt:      record.ReservedAt,
	}, nil
}

func (journal *RuntimeControlJournal) AcknowledgeRuntimeControlCommands(
	ctx context.Context,
	record application.AcknowledgeRuntimeControlCommandsRecord,
) (application.RuntimeControlChannelState, error) {
	var result application.RuntimeControlChannelState
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		state, err := lockRuntimeControlAuthority(ctx, tx, record.Command.Authority)
		if err != nil {
			return err
		}
		if err := advanceRuntimeControlCommandAcknowledgement(
			ctx, tx, &state, record.Command.ThroughSequence, record.AcknowledgedAt,
		); err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}

func (journal *RuntimeControlJournal) ApplyRuntimeControlEvent(
	ctx context.Context,
	record application.ApplyRuntimeControlEventRecord,
) (application.RuntimeControlEventAcceptance, error) {
	if record.Command.ObservationCapture != nil {
		capture := record.Command.ObservationCapture.Clone()
		record.Command.ObservationCapture = &capture
	}
	var result application.RuntimeControlEventAcceptance
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		return journal.applyRuntimeControlEventTx(ctx, tx, record, &result)
	})
	return result, err
}

func (journal *RuntimeControlJournal) applyRuntimeControlEventTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.ApplyRuntimeControlEventRecord,
	result *application.RuntimeControlEventAcceptance,
) error {
	// A terminal replay belongs to its original Turn, even after continuation
	// changes the Runtime's active Run. Discover without graph/row locks, then
	// serialize on that outcome and its existing settlement before taking
	// Session/Runtime/channel locks, matching domain commit and replay.
	terminalIdentity, err := lockTerminalApplyOutcomeBeforeGraph(ctx, tx, record)
	if err != nil {
		return err
	}
	relaxedBackendDeadlineAuthority := false
	state, err := lockRuntimeControlAuthority(ctx, tx, record.Command.Authority)
	if err != nil {
		if record.EffectDisposition !=
			application.RuntimeControlEffectDispositionIgnore ||
			record.Command.TurnSettlement == nil ||
			(!errors.Is(
				err,
				application.ErrRuntimeControlIdentityConflict,
			) && !errors.Is(
				err,
				application.ErrRuntimeControlStaleOwner,
			)) {
			return err
		}
		state, err = lockFenceRuntimeControlChannel(
			ctx,
			tx,
			runtimeControlIdentity{
				workerSessionID: record.Command.Authority.
					WorkerSessionID,
				runtimeID: record.Command.Authority.
					RuntimeInstanceID,
				generation: record.Command.Authority.
					RuntimeGeneration,
				channelEpoch: record.Command.Authority.
					ChannelEpoch,
			},
		)
		if err != nil {
			return err
		}
		relaxedBackendDeadlineAuthority = true
	}
	message, err := readRuntimeControlMessage(
		ctx, tx, state.RuntimeInstanceID, application.RuntimeControlDirectionEvent,
		record.Command.Sequence,
	)
	if err != nil || message.MessageID != record.Command.MessageID {
		return application.ErrRuntimeControlMessageConflict
	}
	if terminalIdentity != nil {
		envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
		if err != nil || envelope.Kind != "event" || envelope.MessageType != "agent_turn.terminal" ||
			envelope.AgentTurnID != terminalIdentity.AgentTurnID ||
			message.Fingerprint != terminalIdentity.Fingerprint ||
			!terminalApplyEnvelopeMatchesAuthority(envelope, record) {
			return application.ErrRuntimeControlIdentityConflict
		}
		digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
		if err != nil || digest != terminalIdentity.Fingerprint {
			return application.ErrRuntimeControlMessageConflict
		}
	}
	if err := journal.applyModelObservation(ctx, tx, state, message, &record, relaxedBackendDeadlineAuthority); err != nil {
		return err
	}
	if message.EffectStatus == "applied" {
		if message.ApplyIdempotencyKey != record.Command.IdempotencyKey ||
			message.ApplyOutcomeDigest != record.OutcomeDigest {
			return application.ErrRuntimeControlMessageConflict
		}
		plan, err := lockTerminalSettlementForApply(
			ctx, tx, state, message, record,
		)
		if err != nil {
			return err
		}
		if relaxedBackendDeadlineAuthority &&
			plan.BackendDeadline == nil {
			return application.ErrRuntimeControlStaleOwner
		}
		message.Duplicate = true
		*result = application.RuntimeControlEventAcceptance{Message: message, Channel: state}
		return nil
	}
	if message.EffectStatus != "received" {
		return application.ErrRuntimeControlMessageConflict
	}
	if record.Command.Sequence != state.EventAppliedSequence+1 {
		return application.ErrRuntimeControlSequenceGap
	}
	settlementPlan, err := lockTerminalSettlementForApply(
		ctx, tx, state, message, record,
	)
	if err != nil {
		return err
	}
	if relaxedBackendDeadlineAuthority &&
		settlementPlan.BackendDeadline == nil {
		return application.ErrRuntimeControlStaleOwner
	}
	if err := journal.reconcileRuntimeControlEventEffectTx(
		ctx,
		tx,
		&state,
		message,
		record,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET outcome=$4,effect_status='applied',apply_idempotency_key=$5,
    apply_outcome_digest=$6,applied_at=$7
WHERE runtime_instance_id=$1 AND direction='event' AND sequence=$2 AND message_id=$3`,
		state.RuntimeInstanceID, record.Command.Sequence, record.Command.MessageID,
		record.Command.Outcome, record.Command.IdempotencyKey, record.OutcomeDigest, record.AppliedAt,
	); err != nil {
		return fmt.Errorf("apply Runtime control event: %w", err)
	}
	if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET event_applied_sequence=$2,revision=revision+1,updated_at=$3
WHERE runtime_instance_id=$1
RETURNING revision`,
		state.RuntimeInstanceID, record.Command.Sequence, record.AppliedAt,
	).Scan(&state.Revision); err != nil {
		return fmt.Errorf("advance applied Runtime control event cursor: %w", err)
	}
	if err := advanceTerminalSettlementForApply(
		ctx, tx, settlementPlan, record,
	); err != nil {
		return err
	}
	state.EventAppliedSequence = record.Command.Sequence
	message.Outcome = append([]byte(nil), record.Command.Outcome...)
	message.EffectStatus = "applied"
	message.ApplyIdempotencyKey = record.Command.IdempotencyKey
	message.ApplyOutcomeDigest = record.OutcomeDigest
	message.AppliedAt = timePointer(record.AppliedAt)
	*result = application.RuntimeControlEventAcceptance{Message: message, Channel: state}
	return nil
}

type terminalApplyOutcomeIdentity struct {
	AgentTurnID contractsv1.AgentTurnID
	Fingerprint string
}

func lockTerminalApplyOutcomeBeforeGraph(ctx context.Context, tx platformpostgres.Tx, record application.ApplyRuntimeControlEventRecord) (*terminalApplyOutcomeIdentity, error) {
	var raw json.RawMessage
	var fingerprint, messageID string
	err := tx.QueryRow(ctx, `SELECT envelope,fingerprint,message_id FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='event' AND sequence=$2`, record.Command.Authority.RuntimeInstanceID, record.Command.Sequence).Scan(&raw, &fingerprint, &messageID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrRuntimeControlMessageConflict
		}
		return nil, err
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(raw)
	if err != nil {
		return nil, application.ErrRuntimeControlMessageConflict
	}
	if envelope.Kind != "event" || envelope.MessageType != "agent_turn.terminal" {
		return nil, nil
	}
	if messageID != record.Command.MessageID || !terminalApplyEnvelopeMatchesAuthority(envelope, record) {
		return nil, application.ErrRuntimeControlIdentityConflict
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil || fingerprint != digest {
		return nil, application.ErrRuntimeControlMessageConflict
	}
	if err := lockAgentTurnOutcome(ctx, tx, string(envelope.AgentTurnID)); err != nil {
		return nil, err
	}
	// Domain commit/replay locks this settlement before the resident graph.
	// Use the immutable terminal envelope's Turn, never the current active Turn
	// or the caller's settlement authority; continuation may have advanced both.
	settlementID, err := application.TurnSettlementID(string(envelope.AgentTurnID))
	if err != nil {
		return nil, application.ErrTurnSettlementConflict
	}
	if _, err := readTurnSettlement(ctx, tx, settlementID, true); err != nil {
		if errors.Is(err, application.ErrTurnSettlementNotFound) {
			return nil, application.ErrTurnSettlementConflict
		}
		return nil, err
	}
	return &terminalApplyOutcomeIdentity{AgentTurnID: envelope.AgentTurnID, Fingerprint: fingerprint}, nil
}

func terminalApplyEnvelopeMatchesAuthority(envelope contractsv1.RuntimeControlEnvelope, record application.ApplyRuntimeControlEventRecord) bool {
	a := record.Command.Authority
	return string(envelope.WorkerSessionID) == a.WorkerSessionID && string(envelope.RuntimeInstanceID) == a.RuntimeInstanceID &&
		uint64(envelope.RuntimeGeneration) == a.RuntimeGeneration && uint64(envelope.ChannelEpoch) == a.ChannelEpoch &&
		envelope.Sequence == record.Command.Sequence && envelope.MessageID == record.Command.MessageID && envelope.AgentTurnID != ""
}

func (journal *RuntimeControlJournal) reconcileRuntimeControlEventEffectTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	state *application.RuntimeControlChannelState,
	event application.RuntimeControlMessage,
	record application.ApplyRuntimeControlEventRecord,
) error {
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(event.Envelope)
	if err != nil {
		return application.ErrRuntimeControlMessageConflict
	}
	isTurnEffect := envelope.MessageType == "agent_turn.effect" ||
		envelope.MessageType == "agent_turn.terminal"
	if envelope.Kind != "event" {
		if isTurnEffect || record.EffectDisposition != "" {
			return application.ErrRuntimeControlMessageConflict
		}
		return nil
	}
	if !isTurnEffect {
		if record.EffectDisposition != "" {
			return application.ErrRuntimeControlMessageConflict
		}
		return nil
	}
	switch record.EffectDisposition {
	case application.RuntimeControlEffectDispositionIgnore:
		return nil
	case application.RuntimeControlEffectDispositionCommit:
	default:
		return application.ErrRuntimeControlMessageConflict
	}

	var (
		command application.ReconcileRuntimeControlEffectCommand
		start   application.RuntimeControlMessage
	)
	switch envelope.MessageType {
	case "agent_turn.effect":
		var payload struct {
			ActiveTurn *contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil ||
			payload.ActiveTurn == nil {
			return application.ErrRuntimeControlMessageConflict
		}
		proof := cloneActiveTurn(payload.ActiveTurn)
		if err := proof.Validate(); err != nil ||
			proof.Phase != contractsv1.RuntimeControlTurnEffectActive ||
			proof.WorkerRunID != envelope.WorkerRunID ||
			proof.AgentSessionID != envelope.AgentSessionID ||
			proof.AgentTurnID != envelope.AgentTurnID {
			return application.ErrRuntimeControlIdentityConflict
		}
		start, err = readRuntimeControlStartCommandForTurn(
			ctx,
			tx,
			state.RuntimeInstanceID,
			string(envelope.AgentTurnID),
		)
		if err != nil ||
			start.Sequence != proof.StartCommandSequence ||
			start.MessageID != proof.StartMessageID ||
			start.IdempotencyKey != proof.StartIdempotencyKey ||
			start.Fingerprint != proof.StartEnvelopeDigest {
			return application.ErrRuntimeControlMessageConflict
		}
		startEnvelope, err := contractsv1.DecodeRuntimeControlEnvelope(start.Envelope)
		if err != nil ||
			startEnvelope.Kind != "command" ||
			startEnvelope.MessageType != "agent_turn.start" ||
			startEnvelope.WorkerRunID != envelope.WorkerRunID ||
			startEnvelope.AgentSessionID != envelope.AgentSessionID ||
			startEnvelope.AgentTurnID != envelope.AgentTurnID {
			return application.ErrRuntimeControlMessageConflict
		}
		command = application.ReconcileRuntimeControlEffectCommand{
			Authority:       record.Command.Authority,
			CommandSequence: proof.StartCommandSequence,
			MessageID:       proof.StartMessageID,
			AgentTurnID:     string(proof.AgentTurnID),
			Phase:           application.RuntimeControlEffectActive,
			ActiveTurn:      proof,
		}
	case "agent_turn.terminal":
		start, err = readRuntimeControlStartCommandForTurn(
			ctx,
			tx,
			state.RuntimeInstanceID,
			string(envelope.AgentTurnID),
		)
		if err != nil {
			return err
		}
		startEnvelope, err := contractsv1.DecodeRuntimeControlEnvelope(start.Envelope)
		if err != nil ||
			startEnvelope.Kind != "command" ||
			startEnvelope.MessageType != "agent_turn.start" ||
			startEnvelope.WorkerRunID != envelope.WorkerRunID ||
			startEnvelope.AgentSessionID != envelope.AgentSessionID ||
			startEnvelope.AgentTurnID != envelope.AgentTurnID ||
			start.EffectStatus != string(application.RuntimeControlEffectActive) &&
				start.EffectStatus != string(application.RuntimeControlEffectTerminal) {
			return application.ErrRuntimeControlMessageConflict
		}
		digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
		if err != nil || event.Fingerprint != digest {
			return application.ErrRuntimeControlMessageConflict
		}
		proof := &contractsv1.RuntimeControlActiveTurn{
			WorkerRunID:            startEnvelope.WorkerRunID,
			AgentSessionID:         startEnvelope.AgentSessionID,
			AgentTurnID:            startEnvelope.AgentTurnID,
			StartCommandSequence:   start.Sequence,
			StartMessageID:         start.MessageID,
			StartIdempotencyKey:    start.IdempotencyKey,
			StartEnvelopeDigest:    start.Fingerprint,
			LastUpdateSequence:     event.Sequence,
			TerminalEnvelopeDigest: digest,
			Phase:                  contractsv1.RuntimeControlTurnEffectTerminal,
		}
		if err := proof.Validate(); err != nil {
			return application.ErrRuntimeControlMessageConflict
		}
		command = application.ReconcileRuntimeControlEffectCommand{
			Authority:              record.Command.Authority,
			CommandSequence:        start.Sequence,
			MessageID:              start.MessageID,
			AgentTurnID:            string(startEnvelope.AgentTurnID),
			Phase:                  application.RuntimeControlEffectTerminal,
			TerminalEventSequence:  event.Sequence,
			TerminalEnvelopeDigest: digest,
			ActiveTurn:             proof,
		}
	}
	satisfied, err := historicalRuntimeControlEffectSatisfied(*state, start, command)
	if err != nil {
		return err
	}
	if satisfied {
		return nil
	}
	var effect application.RuntimeControlEffectResult
	if err := journal.reconcileRuntimeControlEffectTx(
		ctx,
		tx,
		application.ReconcileRuntimeControlEffectRecord{
			Command:      command,
			ReconciledAt: record.AppliedAt,
		},
		&effect,
	); err != nil {
		return err
	}
	*state = effect.Channel
	return nil
}

func historicalRuntimeControlEffectSatisfied(
	state application.RuntimeControlChannelState,
	start application.RuntimeControlMessage,
	command application.ReconcileRuntimeControlEffectCommand,
) (bool, error) {
	currentRank := runtimeControlEffectRank(
		application.RuntimeControlEffectPhase(start.EffectStatus),
	)
	requiredRank := runtimeControlEffectRank(command.Phase)
	if currentRank < requiredRank {
		return false, nil
	}
	if state.ActiveEffectMessageID == start.MessageID {
		if runtimeControlEffectProofSatisfies(command.ActiveTurn, state.ActiveEffectSnapshot) {
			return true, nil
		}
		return false, application.ErrRuntimeControlMessageConflict
	}
	if currentRank != runtimeControlEffectRank(application.RuntimeControlEffectTerminal) ||
		state.ActiveEffectCommandSequence <= start.Sequence ||
		state.ActiveEffectSnapshot == nil {
		return false, application.ErrRuntimeControlMessageConflict
	}
	return true, nil
}

func runtimeControlEffectProofSatisfies(
	required *contractsv1.RuntimeControlActiveTurn,
	actual *contractsv1.RuntimeControlActiveTurn,
) bool {
	if required == nil || actual == nil ||
		required.WorkerRunID != actual.WorkerRunID ||
		required.AgentSessionID != actual.AgentSessionID ||
		required.AgentTurnID != actual.AgentTurnID ||
		required.StartCommandSequence != actual.StartCommandSequence ||
		required.StartMessageID != actual.StartMessageID ||
		required.StartIdempotencyKey != actual.StartIdempotencyKey ||
		required.StartEnvelopeDigest != actual.StartEnvelopeDigest {
		return false
	}
	switch required.Phase {
	case contractsv1.RuntimeControlTurnEffectActive:
		return actual.Phase == contractsv1.RuntimeControlTurnEffectActive ||
			actual.Phase == contractsv1.RuntimeControlTurnEffectTerminal
	case contractsv1.RuntimeControlTurnEffectTerminal:
		return actual.Phase == contractsv1.RuntimeControlTurnEffectTerminal &&
			required.LastUpdateSequence == actual.LastUpdateSequence &&
			required.TerminalEnvelopeDigest == actual.TerminalEnvelopeDigest
	default:
		return false
	}
}

func (journal *RuntimeControlJournal) AcceptRuntimeControlEvent(
	ctx context.Context,
	record application.AcceptRuntimeControlEventRecord,
) (application.RuntimeControlEventAcceptance, error) {
	var result application.RuntimeControlEventAcceptance
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		terminalEvent := record.DecodedEnvelope.Kind == "event" &&
			record.DecodedEnvelope.MessageType == "agent_turn.terminal"
		if terminalEvent {
			agentTurnID := string(record.DecodedEnvelope.AgentTurnID)
			if err := lockAgentTurnOutcome(ctx, tx, agentTurnID); err != nil {
				return err
			}
			if err := rejectTerminalEventClaimedByRuntimeRecovery(
				ctx,
				tx,
				agentTurnID,
				record.Command.Authority.RuntimeInstanceID,
				record.Command.Authority.RuntimeGeneration,
				record.Command.Authority.ChannelEpoch,
				record.AcceptedAt,
			); err != nil {
				return err
			}
		}
		var terminalSettlement *application.TurnSettlement
		if terminalEvent {
			locked, found, err := lockExistingTerminalSettlementBeforeRuntime(
				ctx,
				tx,
				record,
			)
			if err != nil {
				return err
			}
			if found {
				terminalSettlement = &locked
			}
		}
		fencedTerminalEvidence := false
		var state application.RuntimeControlChannelState
		if terminalSettlement != nil {
			forced, err := forcedBackendDeadlineTerminalSettlement(
				*terminalSettlement,
			)
			if err != nil {
				return err
			}
			if forced {
				state, err = lockFencedBackendDeadlineTerminalEvidence(
					ctx,
					tx,
					record,
					*terminalSettlement,
				)
				if err != nil {
					return err
				}
				fencedTerminalEvidence = true
			}
		}
		if !fencedTerminalEvidence {
			var err error
			state, err = lockRuntimeControlAuthority(
				ctx,
				tx,
				record.Command.Authority,
			)
			if err != nil {
				return err
			}
		}
		if record.Command.Sequence <= state.EventCommittedSequence {
			existing, err := readRuntimeControlMessage(
				ctx,
				tx,
				state.RuntimeInstanceID,
				application.RuntimeControlDirectionEvent,
				record.Command.Sequence,
			)
			if err != nil {
				return application.ErrRuntimeControlMessageConflict
			}
			if existing.MessageID != record.Command.MessageID ||
				existing.IdempotencyKey != record.Command.IdempotencyKey ||
				existing.Fingerprint != record.Fingerprint ||
				!bytes.Equal(existing.Envelope, record.Command.Envelope) {
				return application.ErrRuntimeControlMessageConflict
			}
			if err := ensureTerminalTurnSettlementHeader(
				ctx,
				tx,
				state,
				record,
				existing.CreatedAt,
			); err != nil {
				return err
			}
			existing.Duplicate = true
			result = application.RuntimeControlEventAcceptance{Message: existing, Channel: state}
			return nil
		}
		if err := advanceRuntimeControlCommandAcknowledgement(
			ctx, tx, &state, record.DecodedEnvelope.AckSequence, record.AcceptedAt,
		); err != nil {
			return err
		}
		if record.Command.Sequence != state.EventCommittedSequence+1 {
			return application.ErrRuntimeControlSequenceGap
		}
		if existing, found, err := findRuntimeControlMessage(
			ctx,
			tx,
			state.RuntimeInstanceID,
			application.RuntimeControlDirectionEvent,
			record.Command.MessageID,
			record.Command.IdempotencyKey,
		); err != nil {
			return err
		} else if found {
			_ = existing
			return application.ErrRuntimeControlMessageConflict
		}
		cursorDigest, err := contractsv1.AdvanceRuntimeControlCursor(
			state.EventCommittedDigest,
			contractsv1.RuntimeControlDirectionRuntimeToBackend,
			record.DecodedEnvelope,
		)
		if err != nil {
			return err
		}
		if state.EventReplayTargetSequence > 0 {
			if record.Command.Sequence > state.EventReplayTargetSequence ||
				(record.Command.Sequence == state.EventReplayTargetSequence &&
					cursorDigest != state.EventReplayTargetDigest) {
				return application.ErrRuntimeControlCursorConflict
			}
		}
		if state.EventReplayCheckpointSequence > 0 &&
			record.Command.Sequence == state.EventReplayCheckpointSequence &&
			cursorDigest != state.EventReplayCheckpointDigest {
			return application.ErrRuntimeControlCursorConflict
		}
		if state.TerminalProofPending && state.ReportedActiveTurn != nil &&
			record.Command.Sequence == state.ReportedActiveTurn.LastUpdateSequence {
			if record.Fingerprint != state.ReportedActiveTurn.TerminalEnvelopeDigest ||
				record.DecodedEnvelope.Kind != "event" ||
				record.DecodedEnvelope.MessageType != "agent_turn.terminal" ||
				string(record.DecodedEnvelope.WorkerRunID) != string(state.ReportedActiveTurn.WorkerRunID) ||
				string(record.DecodedEnvelope.AgentSessionID) != string(state.ReportedActiveTurn.AgentSessionID) ||
				string(record.DecodedEnvelope.AgentTurnID) != string(state.ReportedActiveTurn.AgentTurnID) ||
				string(record.DecodedEnvelope.WorkerSessionID) != state.WorkerSessionID ||
				string(record.DecodedEnvelope.RuntimeInstanceID) != state.RuntimeInstanceID ||
				uint64(record.DecodedEnvelope.RuntimeGeneration) != state.RuntimeGeneration ||
				record.DecodedEnvelope.ChannelEpoch != state.ChannelEpoch {
				return application.ErrRuntimeControlMessageConflict
			}
			state.ActiveEffectSnapshot = cloneActiveTurn(state.ReportedActiveTurn)
			state.ActiveEffectPhase = application.RuntimeControlEffectTerminal
			state.ReportedActiveTurn = nil
			state.TerminalProofPending = false
			if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET effect_status='terminal'
WHERE runtime_instance_id=$1 AND direction='command'
  AND sequence=$2 AND message_id=$3`,
				state.RuntimeInstanceID,
				state.ActiveEffectCommandSequence,
				state.ActiveEffectMessageID,
			); err != nil {
				return fmt.Errorf("promote terminal Runtime control start effect: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO runtime_control_messages
  (runtime_instance_id,direction,sequence,message_id,idempotency_key,fingerprint,
   cursor_digest,envelope,outcome,effect_status,created_at,acknowledged_at)
VALUES ($1,'event',$2,$3,$4,$5,$6,$7,NULL,'received',$8,$8)`,
			state.RuntimeInstanceID,
			record.Command.Sequence,
			record.Command.MessageID,
			record.Command.IdempotencyKey,
			record.Fingerprint,
			cursorDigest,
			record.Command.Envelope,
			record.AcceptedAt,
		); err != nil {
			return fmt.Errorf("journal Runtime control event: %w", err)
		}
		if err := ensureTerminalTurnSettlementHeader(
			ctx,
			tx,
			state,
			record,
			record.AcceptedAt,
		); err != nil {
			return err
		}
		activeSnapshot, err := marshalNullableActiveTurn(state.ActiveEffectSnapshot)
		if err != nil {
			return err
		}
		reportedSnapshot, err := marshalNullableActiveTurn(state.ReportedActiveTurn)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET event_committed_sequence=$2,event_committed_digest=$3,
    event_acknowledged_sequence=$2,
    event_replay_checkpoint_sequence=CASE
      WHEN event_replay_checkpoint_sequence=$2 AND event_replay_target_sequence>$2
        THEN event_replay_target_sequence
      ELSE event_replay_checkpoint_sequence END,
    event_replay_checkpoint_digest=CASE
      WHEN event_replay_checkpoint_sequence=$2 AND event_replay_target_sequence>$2
        THEN event_replay_target_digest
      ELSE event_replay_checkpoint_digest END,
    active_effect_phase=$4,active_effect_snapshot=$5,
    reported_active_turn=$6,terminal_proof_pending=$7,
    revision=revision+1,updated_at=$8
WHERE runtime_instance_id=$1
RETURNING revision`,
			state.RuntimeInstanceID,
			record.Command.Sequence,
			cursorDigest,
			nullableString(string(state.ActiveEffectPhase)),
			activeSnapshot,
			reportedSnapshot,
			state.TerminalProofPending,
			record.AcceptedAt,
		).Scan(&state.Revision); err != nil {
			return fmt.Errorf("advance Runtime control event cursor: %w", err)
		}
		if !fencedTerminalEvidence {
			if _, err := tx.Exec(ctx, `
UPDATE runtime_instances
SET last_event_sequence=$2,revision=revision+$3
WHERE runtime_instance_id=$1`,
				state.RuntimeInstanceID,
				record.Command.Sequence,
				runtimeControlLifecycleRevisionDelta(record.DecodedEnvelope),
			); err != nil {
				return fmt.Errorf(
					"advance Runtime event aggregate cursor: %w",
					err,
				)
			}
		}
		state.EventCommittedSequence = record.Command.Sequence
		state.EventCommittedDigest = cursorDigest
		state.EventAcknowledgedSequence = record.Command.Sequence
		if state.EventReplayCheckpointSequence == record.Command.Sequence &&
			state.EventReplayTargetSequence > record.Command.Sequence {
			state.EventReplayCheckpointSequence = state.EventReplayTargetSequence
			state.EventReplayCheckpointDigest = state.EventReplayTargetDigest
		}
		result = application.RuntimeControlEventAcceptance{
			Message: application.RuntimeControlMessage{
				Direction: application.RuntimeControlDirectionEvent,
				Sequence:  record.Command.Sequence, MessageID: record.Command.MessageID,
				IdempotencyKey: record.Command.IdempotencyKey,
				Fingerprint:    record.Fingerprint, CursorDigest: cursorDigest,
				Envelope:     append([]byte(nil), record.Command.Envelope...),
				EffectStatus: "received", CreatedAt: record.AcceptedAt,
				AcknowledgedAt: timePointer(record.AcceptedAt),
			},
			Channel: state,
		}
		return nil
	})
	return result, err
}

// lockExistingTerminalSettlementBeforeRuntime establishes the global row-lock
// order for terminal delivery: settlement -> Session/Runtime/channel. Domain
// commit and source Apply already use that order. A first terminal has no row
// yet and remains serialized against deadline claim by the AgentTurn advisory
// lock acquired by the caller.
func lockExistingTerminalSettlementBeforeRuntime(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AcceptRuntimeControlEventRecord,
) (application.TurnSettlement, bool, error) {
	settlementID, err := application.TurnSettlementID(
		string(record.DecodedEnvelope.AgentTurnID),
	)
	if err != nil {
		return application.TurnSettlement{}, false,
			application.ErrTurnSettlementConflict
	}
	snapshot, err := readTurnSettlement(ctx, tx, settlementID, false)
	if errors.Is(err, application.ErrTurnSettlementNotFound) {
		return application.TurnSettlement{}, false, nil
	}
	if err != nil {
		return application.TurnSettlement{}, false, err
	}
	locked, err := readTurnSettlement(ctx, tx, snapshot.SettlementID, true)
	if err != nil {
		return application.TurnSettlement{}, false, err
	}
	return locked, true, nil
}

func forcedBackendDeadlineTerminalSettlement(
	settlement application.TurnSettlement,
) (bool, error) {
	if settlement.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline {
		return false, nil
	}
	source, err := application.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil {
		return false, application.ErrTurnSettlementConflict
	}
	switch source.Phase {
	case application.BackendDeadlineSourceForceStopIntent,
		application.BackendDeadlineSourceWriteFenced,
		application.BackendDeadlineSourceCleanupConfirmed:
		if source.ForceStop == nil {
			return false, application.ErrTurnSettlementConflict
		}
		return true, nil
	default:
		return false, nil
	}
}

func lockFencedBackendDeadlineTerminalEvidence(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AcceptRuntimeControlEventRecord,
	settlement application.TurnSettlement,
) (application.RuntimeControlChannelState, error) {
	authority := record.Command.Authority
	envelope := record.DecodedEnvelope
	if envelope.Kind != "event" ||
		envelope.MessageType != "agent_turn.terminal" {
		return application.RuntimeControlChannelState{},
			application.ErrRuntimeControlStaleOwner
	}
	state, err := lockFenceRuntimeControlChannel(
		ctx,
		tx,
		runtimeControlIdentity{
			workerSessionID: authority.WorkerSessionID,
			runtimeID:       authority.RuntimeInstanceID,
			generation:      authority.RuntimeGeneration,
			channelEpoch:    authority.ChannelEpoch,
		},
	)
	if err != nil {
		return application.RuntimeControlChannelState{}, err
	}
	if state.Status != "fenced" ||
		state.ProcessNonce != authority.ProcessNonce ||
		state.StreamAttempt != authority.StreamAttempt ||
		state.FencedAt.IsZero() ||
		state.FenceRevision == 0 ||
		state.FenceRevision > state.Revision ||
		state.FenceOwnerFence == 0 ||
		state.FenceOwnerFence != state.OwnerFence ||
		authority.OwnerFence == ^uint64(0) ||
		state.FenceOwnerFence != authority.OwnerFence+1 ||
		record.AcceptedAt.Before(state.FencedAt) {
		return application.RuntimeControlChannelState{},
			application.ErrRuntimeControlStaleOwner
	}
	settlementID, err := application.TurnSettlementID(
		string(envelope.AgentTurnID),
	)
	if err != nil {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementConflict
	}
	if settlement.SettlementID != settlementID {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementConflict
	}
	sourceState, err := application.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementConflict
	}
	forcedPhase := sourceState.Phase ==
		application.BackendDeadlineSourceForceStopIntent ||
		sourceState.Phase ==
			application.BackendDeadlineSourceWriteFenced ||
		sourceState.Phase ==
			application.BackendDeadlineSourceCleanupConfirmed
	if settlement.Source.Kind !=
		application.TurnSettlementSourceBackendDeadline ||
		!forcedPhase ||
		sourceState.ForceStop == nil ||
		settlement.WorkerSessionID != state.WorkerSessionID ||
		settlement.WorkerRunID != string(envelope.WorkerRunID) ||
		settlement.AgentSessionID != string(envelope.AgentSessionID) ||
		settlement.AgentTurnID != string(envelope.AgentTurnID) ||
		sourceState.RuntimeInstanceID != state.RuntimeInstanceID ||
		sourceState.RuntimeGeneration != state.RuntimeGeneration ||
		sourceState.ChannelEpoch != state.ChannelEpoch ||
		sourceState.ForceStop.RuntimeInstanceID != state.RuntimeInstanceID ||
		sourceState.ForceStop.RuntimeGeneration != state.RuntimeGeneration ||
		sourceState.ForceStop.ChannelEpoch != state.ChannelEpoch ||
		!sourceState.ForceStop.DecidedAt.Equal(state.FencedAt) ||
		record.AcceptedAt.Before(sourceState.ForceStop.DecidedAt) {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementConflict
	}
	witnessEmpty := terminalEventWitnessEmpty(settlement)
	if witnessEmpty &&
		turnSettlementPhaseRank(settlement.Phase) >=
			turnSettlementPhaseRank(
				application.TurnSettlementTerminalEventApplied,
			) {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementPhaseConflict
	}
	if !witnessEmpty &&
		!terminalSettlementMatchesEvent(
			settlement,
			state,
			envelope,
			record.Command.Sequence,
			record.Command.MessageID,
			record.Command.IdempotencyKey,
			record.Fingerprint,
			record.AcceptedAt,
		) {
		return application.RuntimeControlChannelState{},
			application.ErrTurnSettlementConflict
	}
	return state, nil
}

func (journal *RuntimeControlJournal) ReconcileRuntimeControlEffect(
	ctx context.Context,
	record application.ReconcileRuntimeControlEffectRecord,
) (application.RuntimeControlEffectResult, error) {
	var result application.RuntimeControlEffectResult
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		return journal.reconcileRuntimeControlEffectTx(ctx, tx, record, &result)
	})
	return result, err
}

func (journal *RuntimeControlJournal) reconcileRuntimeControlEffectTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.ReconcileRuntimeControlEffectRecord,
	result *application.RuntimeControlEffectResult,
) error {
	state, err := lockRuntimeControlAuthority(ctx, tx, record.Command.Authority)
	if err != nil {
		return err
	}
	if state.Status != "connected" {
		return application.ErrRuntimeControlStaleOwner
	}
	message, err := readRuntimeControlMessage(
		ctx,
		tx,
		state.RuntimeInstanceID,
		application.RuntimeControlDirectionCommand,
		record.Command.CommandSequence,
	)
	if err != nil || message.MessageID != record.Command.MessageID {
		return application.ErrRuntimeControlMessageConflict
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil ||
		envelope.Kind != "command" ||
		envelope.MessageType != "agent_turn.start" ||
		envelope.MessageID != record.Command.MessageID ||
		envelope.Sequence != record.Command.CommandSequence ||
		string(envelope.AgentTurnID) != record.Command.AgentTurnID {
		return application.ErrRuntimeControlMessageConflict
	}
	proof := record.Command.ActiveTurn
	if proof == nil ||
		proof.StartEnvelopeDigest != message.Fingerprint ||
		proof.StartIdempotencyKey != message.IdempotencyKey ||
		string(proof.WorkerRunID) != string(envelope.WorkerRunID) ||
		string(proof.AgentSessionID) != string(envelope.AgentSessionID) ||
		string(proof.AgentTurnID) != string(envelope.AgentTurnID) {
		return application.ErrRuntimeControlIdentityConflict
	}
	if state.ActiveEffectMessageID != "" &&
		(state.ActiveEffectMessageID != record.Command.MessageID ||
			state.ActiveEffectAgentTurnID != record.Command.AgentTurnID) {
		if state.ActiveEffectSnapshot == nil ||
			state.ActiveEffectSnapshot.Phase != contractsv1.RuntimeControlTurnEffectTerminal ||
			record.Command.CommandSequence <= state.ActiveEffectCommandSequence {
			return application.ErrRuntimeControlMessageConflict
		}
	}
	currentRank := runtimeControlEffectRank(application.RuntimeControlEffectPhase(message.EffectStatus))
	nextRank := runtimeControlEffectRank(record.Command.Phase)
	if nextRank == 0 || (currentRank > 0 && nextRank < currentRank) {
		return application.ErrRuntimeControlMessageConflict
	}
	if currentRank == nextRank {
		if !sameNullableActiveTurn(proof, state.ActiveEffectSnapshot) {
			return application.ErrRuntimeControlMessageConflict
		}
		*result = application.RuntimeControlEffectResult{
			Channel: state, Duplicate: true,
		}
		return nil
	}
	if record.Command.Phase == application.RuntimeControlEffectTerminal {
		terminal, err := readRuntimeControlMessage(
			ctx, tx, state.RuntimeInstanceID, application.RuntimeControlDirectionEvent,
			record.Command.TerminalEventSequence,
		)
		if err != nil || terminal.Fingerprint != record.Command.TerminalEnvelopeDigest {
			return application.ErrRuntimeControlMessageConflict
		}
		terminalEnvelope, err := contractsv1.DecodeRuntimeControlEnvelope(terminal.Envelope)
		if err != nil || terminalEnvelope.Kind != "event" ||
			terminalEnvelope.MessageType != "agent_turn.terminal" ||
			string(terminalEnvelope.WorkerRunID) != string(envelope.WorkerRunID) ||
			string(terminalEnvelope.AgentSessionID) != string(envelope.AgentSessionID) ||
			string(terminalEnvelope.AgentTurnID) != record.Command.AgentTurnID ||
			string(terminalEnvelope.WorkerSessionID) != state.WorkerSessionID ||
			string(terminalEnvelope.RuntimeInstanceID) != state.RuntimeInstanceID ||
			uint64(terminalEnvelope.RuntimeGeneration) != state.RuntimeGeneration ||
			terminalEnvelope.ChannelEpoch != state.ChannelEpoch ||
			record.Command.TerminalEventSequence > state.EventCommittedSequence {
			return application.ErrRuntimeControlMessageConflict
		}
	}
	var turnStatus string
	if record.Command.Phase == application.RuntimeControlEffectTerminal {
		var sessionStatus, runStatus, runtimeStatus, activeRunID string
		if err := tx.QueryRow(ctx, `
SELECT turn.status,session.status,run.status,runtime.status,
       COALESCE(session.active_worker_run_id,'')
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
JOIN worker_runs run ON run.worker_run_id=turn.worker_run_id
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$3
WHERE turn.agent_turn_id=$1 AND turn.worker_session_id=$2
  AND turn.worker_run_id=$4 AND turn.agent_session_id=$5
  AND session.active_runtime_instance_id=runtime.runtime_instance_id
  AND runtime.worker_session_id=session.worker_session_id
  AND runtime.worker_run_id=run.worker_run_id
  AND runtime.generation=$6 AND runtime.control_channel_epoch=$7
FOR UPDATE OF turn,session,run,runtime`,
			record.Command.AgentTurnID,
			state.WorkerSessionID,
			state.RuntimeInstanceID,
			string(envelope.WorkerRunID),
			string(envelope.AgentSessionID),
			state.RuntimeGeneration,
			state.ChannelEpoch,
		).Scan(
			&turnStatus,
			&sessionStatus,
			&runStatus,
			&runtimeStatus,
			&activeRunID,
		); errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRuntimeControlIdentityConflict
		} else if err != nil {
			return fmt.Errorf("lock terminal Runtime control AgentTurn: %w", err)
		}
		isCurrentRun := activeRunID == string(envelope.WorkerRunID)
		isExactReleasedTerminalRun := activeRunID == "" &&
			runtimeControlRunStatusTerminal(runStatus) &&
			runtimeControlAgentTurnStatusTerminal(turnStatus)
		if !runtimeControlSessionStatusAllowed(sessionStatus) ||
			!runtimeControlRuntimeStatusAllowed(runtimeStatus) ||
			(!isCurrentRun && !isExactReleasedTerminalRun) {
			return application.ErrRuntimeControlIdentityConflict
		}
	} else if err := tx.QueryRow(ctx, `
SELECT turn.status
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
JOIN worker_runs run ON run.worker_run_id=turn.worker_run_id
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$3
WHERE turn.agent_turn_id=$1 AND turn.worker_session_id=$2
  AND turn.worker_run_id=$4 AND turn.agent_session_id=$5
  AND session.active_worker_run_id=run.worker_run_id
  AND session.active_runtime_instance_id=runtime.runtime_instance_id
  AND runtime.worker_session_id=session.worker_session_id
  AND runtime.worker_run_id=run.worker_run_id
  AND runtime.generation=$6 AND runtime.control_channel_epoch=$7
FOR UPDATE OF turn,session,run,runtime`,
		record.Command.AgentTurnID,
		state.WorkerSessionID,
		state.RuntimeInstanceID,
		string(envelope.WorkerRunID),
		string(envelope.AgentSessionID),
		state.RuntimeGeneration,
		state.ChannelEpoch,
	).Scan(&turnStatus); errors.Is(err, pgx.ErrNoRows) {
		return application.ErrRuntimeControlIdentityConflict
	} else if err != nil {
		return fmt.Errorf("lock active Runtime control AgentTurn: %w", err)
	}
	if !runtimeControlTurnStatusAllowsEffect(turnStatus, record.Command.Phase) {
		return application.ErrRuntimeControlIdentityConflict
	}
	turnConsumed := false
	if record.Command.Phase == application.RuntimeControlEffectActive ||
		record.Command.Phase == application.RuntimeControlEffectTerminal {
		if err := advanceRuntimeControlCommandAcknowledgement(
			ctx,
			tx,
			&state,
			max(state.CommandAcknowledgedSequence, record.Command.CommandSequence),
			record.ReconciledAt,
		); err != nil {
			return err
		}
		if err := advanceRuntimeControlCommandApplied(
			ctx,
			tx,
			&state,
			record.Command.CommandSequence,
			proof,
			record.ReconciledAt,
		); err != nil {
			return err
		}
		switch turnStatus {
		case "delivered":
			tag, err := tx.Exec(ctx, `
UPDATE agent_turns
SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND status='delivered'`,
				record.Command.AgentTurnID,
			)
			if err != nil {
				return fmt.Errorf("consume active Runtime control AgentTurn: %w", err)
			}
			turnConsumed = tag.RowsAffected() == 1
		case "consumed":
		case "completed", "failed", "cancelled", "timed_out":
			if record.Command.Phase != application.RuntimeControlEffectTerminal {
				return application.ErrRuntimeControlIdentityConflict
			}
		default:
			return application.ErrRuntimeControlIdentityConflict
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET effect_status=$4,
    acknowledged_at=CASE WHEN $4 IN ('active','terminal')
        THEN COALESCE(acknowledged_at,$5) ELSE acknowledged_at END,
    applied_at=CASE WHEN $4 IN ('active','terminal')
        THEN COALESCE(applied_at,$5) ELSE applied_at END
WHERE runtime_instance_id=$1 AND direction='command' AND sequence=$2 AND message_id=$3`,
		state.RuntimeInstanceID,
		record.Command.CommandSequence,
		record.Command.MessageID,
		record.Command.Phase,
		record.ReconciledAt,
	); err != nil {
		return fmt.Errorf("advance Runtime control command effect: %w", err)
	}
	snapshot := cloneActiveTurn(proof)
	encodedSnapshot, err := marshalNullableActiveTurn(snapshot)
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET active_effect_message_id=$2,active_effect_agent_turn_id=$3,
    active_effect_command_sequence=$4,active_effect_phase=$5,active_effect_snapshot=$6,
    command_acknowledged_sequence=$7,command_applied_sequence=$8,
    revision=revision+1,updated_at=$9
WHERE runtime_instance_id=$1
RETURNING revision`,
		state.RuntimeInstanceID,
		record.Command.MessageID,
		record.Command.AgentTurnID,
		record.Command.CommandSequence,
		record.Command.Phase,
		encodedSnapshot,
		state.CommandAcknowledgedSequence,
		state.CommandAppliedSequence,
		record.ReconciledAt,
	).Scan(&state.Revision); err != nil {
		return fmt.Errorf("advance Runtime control active effect: %w", err)
	}
	state.ActiveEffectMessageID = record.Command.MessageID
	state.ActiveEffectAgentTurnID = record.Command.AgentTurnID
	state.ActiveEffectCommandSequence = record.Command.CommandSequence
	state.ActiveEffectPhase = record.Command.Phase
	state.ActiveEffectSnapshot = snapshot
	*result = application.RuntimeControlEffectResult{
		Channel: state, TurnConsumed: turnConsumed,
	}
	return nil
}

func (journal *RuntimeControlJournal) LoadRuntimeControlReplayPlan(
	ctx context.Context,
	authority application.RuntimeControlAuthority,
) (application.RuntimeControlReplayPlan, error) {
	var result application.RuntimeControlReplayPlan
	err := InCapacityTransaction(ctx, journal.pool, func(tx pgx.Tx, capacity *CapacityTx) error {
		if err := capacity.authorizeControlStarts(ctx, authority.RuntimeInstanceID, nil); err != nil {
			return err
		}
		state, err := lockRuntimeControlAuthority(ctx, tx, authority)
		if err != nil {
			return err
		}
		pending, err := loadRuntimeControlCommandsAfter(
			ctx,
			tx,
			state.RuntimeInstanceID,
			state.CommandAcknowledgedSequence,
		)
		if err != nil {
			return err
		}
		pendingEvents, err := loadRuntimeControlPendingEvents(
			ctx, tx, state.RuntimeInstanceID, state.EventAppliedSequence,
		)
		if err != nil {
			return err
		}
		eventReplayThrough := state.EventCommittedSequence
		if state.EventReplayTargetSequence > eventReplayThrough {
			eventReplayThrough = state.EventReplayTargetSequence
		}
		result = application.RuntimeControlReplayPlan{
			Channel:              state,
			PendingCommands:      pending,
			PendingEvents:        pendingEvents,
			EventReplayFrom:      state.EventCommittedSequence + 1,
			EventReplayThrough:   eventReplayThrough,
			ReportedActiveTurn:   state.ReportedActiveTurn,
			TerminalProofPending: state.TerminalProofPending,
		}
		return nil
	})
	return result, err
}

func (journal *RuntimeControlJournal) DisconnectRuntimeControl(
	ctx context.Context,
	authority application.RuntimeControlAuthority,
	at time.Time,
) (application.RuntimeControlChannelState, error) {
	var result application.RuntimeControlChannelState
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		state, err := lockRuntimeControlAuthority(ctx, tx, authority)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET owner_fence=owner_fence+1,status='disconnected',disconnected_at=$2,
    revision=revision+1,updated_at=$2
WHERE runtime_instance_id=$1
RETURNING owner_fence,revision`,
			state.RuntimeInstanceID, at,
		).Scan(&state.OwnerFence, &state.Revision); err != nil {
			return fmt.Errorf("disconnect Runtime control stream: %w", err)
		}
		state.Status = "disconnected"
		result = state
		return nil
	})
	return result, err
}

func (journal *RuntimeControlJournal) FenceRuntimeControl(
	ctx context.Context,
	command application.FenceRuntimeControlChannelCommand,
	at time.Time,
) (application.RuntimeControlChannelState, error) {
	var result application.RuntimeControlChannelState
	err := platformpostgres.InTransaction(ctx, journal.pool, func(tx platformpostgres.Tx) error {
		state, err := lockFenceRuntimeControlChannel(ctx, tx, runtimeControlIdentity{
			workerSessionID: command.WorkerSessionID,
			runtimeID:       command.RuntimeInstanceID,
			generation:      command.RuntimeGeneration,
			channelEpoch:    command.ChannelEpoch,
		})
		if err != nil {
			return err
		}
		result, err = fenceRuntimeControlChannelLocked(
			ctx,
			tx,
			state,
			command,
			at,
		)
		return err
	})
	return result, err
}

func fenceRuntimeControlChannelLocked(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeControlChannelState,
	command application.FenceRuntimeControlChannelCommand,
	at time.Time,
) (application.RuntimeControlChannelState, error) {
	if command.OnlyIfNotConnected && state.Status == "connected" &&
		!state.ResumeCompletedAt.IsZero() &&
		!state.ResumeCompletedAt.After(command.ConnectedBy) {
		return state, nil
	}
	if state.Status == "fenced" {
		return state, nil
	}
	if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET owner_fence=owner_fence+1,status='fenced',disconnected_at=NULL,
    fenced_at=$2,fence_revision=revision+1,
    fence_owner_fence=owner_fence+1,
    revision=revision+1,updated_at=$2
WHERE runtime_instance_id=$1
RETURNING owner_fence,revision,fenced_at,fence_revision,fence_owner_fence`,
		state.RuntimeInstanceID,
		at,
	).Scan(
		&state.OwnerFence,
		&state.Revision,
		&state.FencedAt,
		&state.FenceRevision,
		&state.FenceOwnerFence,
	); err != nil {
		return application.RuntimeControlChannelState{}, fmt.Errorf(
			"fence Runtime control channel: %w",
			err,
		)
	}
	state.Status = "fenced"
	return state, nil
}

func (journal *RuntimeControlJournal) CompleteRuntimeControlResume(
	ctx context.Context,
	record application.CompleteRuntimeControlResumeRecord,
) (application.RuntimeControlChannelState, error) {
	var result application.RuntimeControlChannelState
	err := InCapacityTransaction(ctx, journal.pool, func(tx pgx.Tx, capacity *CapacityTx) error {
		receivedCommandThrough :=
			record.Command.Ready.WorkerRXReceivedThroughSequence
		if err := guardRuntimeControlSettlementAdmission(
			ctx,
			tx,
			runtimeControlIdentity{
				workerSessionID: record.Command.Authority.WorkerSessionID,
				runtimeID:       record.Command.Authority.RuntimeInstanceID,
				generation:      record.Command.Authority.RuntimeGeneration,
				channelEpoch:    record.Command.Authority.ChannelEpoch,
			},
			record.Command.Ready.ActiveTurn,
			&receivedCommandThrough,
			record.CompletedAt,
		); err != nil {
			return err
		}
		if err := capacity.authorizeControlStarts(ctx, record.Command.Authority.RuntimeInstanceID, record.Command.Ready.ActiveTurn); err != nil {
			return err
		}
		state, err := lockRuntimeControlAuthority(ctx, tx, record.Command.Authority)
		if err != nil {
			return err
		}
		if state.Status == "connected" {
			if state.ResumeReadyDigest != record.ReadyDigest {
				return application.ErrRuntimeControlMessageConflict
			}
			if err := armRuntimeFirstHeartbeatWatchdog(
				ctx,
				tx,
				state,
				state.ResumeCompletedAt,
			); err != nil {
				return err
			}
			var completed []byte
			if err := tx.QueryRow(ctx, `
SELECT resume_completed_state
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
				state.RuntimeInstanceID,
			).Scan(&completed); err != nil {
				return fmt.Errorf("read completed Runtime control resume barrier: %w", err)
			}
			if err := json.Unmarshal(completed, &result); err != nil {
				return fmt.Errorf("decode completed Runtime control resume barrier: %w", err)
			}
			return nil
		}
		if state.Status != "resuming" {
			return application.ErrRuntimeControlStaleOwner
		}
		ready := record.Command.Ready
		if ready.ServerConnectionID != state.OwnerID ||
			ready.AcknowledgedJournalRevision != state.ResumeClaimRevision {
			return application.ErrRuntimeControlCursorConflict
		}
		// A command may be durably appended after Claim returned its replay
		// snapshot but before the peer sends ResumeReady. The peer cannot
		// acknowledge a command it was never sent; force an exact reconnect
		// so the new snapshot is replayed instead of classifying this expected
		// authority race as permanent cursor corruption.
		if ready.WorkerRXReceivedThroughSequence <
			state.CommandCommittedSequence {
			return application.ErrRuntimeControlResumeSnapshotChanged
		}
		expectedEventThrough := state.EventReplayTargetSequence
		if expectedEventThrough == 0 {
			expectedEventThrough = state.EventCommittedSequence
		}
		if ready.WorkerTXReplayedThroughSequence != expectedEventThrough ||
			state.EventCommittedSequence != expectedEventThrough ||
			ready.WorkerTXCursorDigest != state.EventCommittedDigest ||
			ready.WorkerRXReceivedThroughSequence != state.CommandCommittedSequence ||
			ready.WorkerRXCursorDigest != state.CommandCommittedDigest ||
			ready.WorkerRXAppliedSequence < state.CommandAppliedSequence ||
			ready.WorkerRXAppliedSequence > ready.WorkerRXReceivedThroughSequence {
			return application.ErrRuntimeControlCursorConflict
		}
		if !sameNullableActiveTurn(ready.ActiveTurn, state.ActiveEffectSnapshot) {
			return application.ErrRuntimeControlMessageConflict
		}
		if err := advanceRuntimeControlCommandAcknowledgement(
			ctx, tx, &state, ready.WorkerRXReceivedThroughSequence, record.CompletedAt,
		); err != nil {
			return err
		}
		if err := advanceRuntimeControlCommandApplied(
			ctx, tx, &state, ready.WorkerRXAppliedSequence, ready.ActiveTurn, record.CompletedAt,
		); err != nil {
			return err
		}
		state.CommandAppliedSequence = ready.WorkerRXAppliedSequence
		state.EventReplayCheckpointSequence = 0
		state.EventReplayCheckpointDigest = ""
		state.EventReplayTargetSequence = 0
		state.EventReplayTargetDigest = ""
		state.Status = "connected"
		state.ResumeReadyDigest = record.ReadyDigest
		state.ResumeCompletedRevision = state.Revision + 1
		state.ResumeCompletedAt = record.CompletedAt
		state.Revision = state.ResumeCompletedRevision
		completedState, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("marshal completed Runtime control resume barrier: %w", err)
		}
		if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET command_applied_sequence=$2,
    event_replay_checkpoint_sequence=NULL,event_replay_checkpoint_digest=NULL,
    event_replay_target_sequence=NULL,event_replay_target_digest=NULL,
	    status='connected',resume_ready_digest=$3,resume_completed_revision=$4,
	    resume_completed_at=$6,resume_completed_state=$5,revision=revision+1,updated_at=$6
WHERE runtime_instance_id=$1
RETURNING revision`,
			state.RuntimeInstanceID, ready.WorkerRXAppliedSequence, record.ReadyDigest,
			state.ResumeCompletedRevision, completedState, record.CompletedAt,
		).Scan(&state.Revision); err != nil {
			return fmt.Errorf("complete Runtime control resume barrier: %w", err)
		}
		if err := armRuntimeFirstHeartbeatWatchdog(
			ctx,
			tx,
			state,
			record.CompletedAt,
		); err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}

func reconcileClaimedActiveTurn(
	ctx context.Context,
	tx platformpostgres.Tx,
	state *application.RuntimeControlChannelState,
	record application.ClaimRuntimeControlStreamRecord,
) (bool, error) {
	proof := record.Command.ActiveTurn
	if state.TerminalProofPending {
		if proof == nil || state.ReportedActiveTurn == nil ||
			!sameNullableActiveTurn(state.ReportedActiveTurn, proof) {
			return false, application.ErrRuntimeControlMessageConflict
		}
	}
	if proof == nil {
		return false, nil
	}
	message, err := readRuntimeControlMessage(
		ctx, tx, state.RuntimeInstanceID, application.RuntimeControlDirectionCommand,
		proof.StartCommandSequence,
	)
	if err != nil {
		return false, application.ErrRuntimeControlMessageConflict
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil || envelope.Kind != "command" || envelope.MessageType != "agent_turn.start" ||
		envelope.Sequence != proof.StartCommandSequence ||
		envelope.MessageID != proof.StartMessageID ||
		envelope.IdempotencyKey != proof.StartIdempotencyKey ||
		string(envelope.WorkerSessionID) != state.WorkerSessionID ||
		string(envelope.RuntimeInstanceID) != state.RuntimeInstanceID ||
		uint64(envelope.RuntimeGeneration) != state.RuntimeGeneration ||
		envelope.ChannelEpoch != state.ChannelEpoch ||
		message.Fingerprint != proof.StartEnvelopeDigest {
		return false, application.ErrRuntimeControlMessageConflict
	}
	if string(envelope.WorkerRunID) != string(proof.WorkerRunID) ||
		string(envelope.AgentSessionID) != string(proof.AgentSessionID) ||
		string(envelope.AgentTurnID) != string(proof.AgentTurnID) {
		return false, application.ErrRuntimeControlIdentityConflict
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil || digest != proof.StartEnvelopeDigest {
		return false, application.ErrRuntimeControlMessageConflict
	}
	if proof.StartCommandSequence > record.Command.WorkerCommandLastSequence ||
		(runtimeControlEffectRank(application.RuntimeControlEffectPhase(proof.Phase)) >=
			runtimeControlEffectRank(application.RuntimeControlEffectActive) &&
			proof.StartCommandSequence > record.Command.WorkerCommandAppliedSequence) {
		return false, application.ErrRuntimeControlCursorConflict
	}
	var turnStatus string
	err = tx.QueryRow(ctx, `
SELECT turn.status
FROM agent_turns turn
JOIN worker_sessions session ON session.worker_session_id=turn.worker_session_id
JOIN worker_runs run ON run.worker_run_id=turn.worker_run_id
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE turn.agent_turn_id=$1 AND turn.worker_run_id=$2 AND turn.agent_session_id=$3
  AND turn.worker_session_id=$5
  AND session.active_worker_run_id=run.worker_run_id
  AND session.active_runtime_instance_id=runtime.runtime_instance_id
  AND runtime.worker_session_id=session.worker_session_id
  AND runtime.worker_run_id=run.worker_run_id
  AND runtime.generation=$6 AND runtime.control_channel_epoch=$7
FOR UPDATE OF turn,session,run,runtime`,
		string(proof.AgentTurnID), string(proof.WorkerRunID), string(proof.AgentSessionID),
		state.RuntimeInstanceID, state.WorkerSessionID, state.RuntimeGeneration, state.ChannelEpoch,
	).Scan(&turnStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, application.ErrRuntimeControlIdentityConflict
	}
	if err != nil {
		return false, fmt.Errorf("lock claimed Runtime control active Turn: %w", err)
	}
	if !runtimeControlTurnStatusAllowsEffect(
		turnStatus,
		application.RuntimeControlEffectPhase(proof.Phase),
	) {
		return false, application.ErrRuntimeControlIdentityConflict
	}
	if current := state.ActiveEffectSnapshot; current != nil {
		if !sameRuntimeControlStart(*current, *proof) {
			if current.Phase != contractsv1.RuntimeControlTurnEffectTerminal ||
				proof.StartCommandSequence <= current.StartCommandSequence {
				return false, application.ErrRuntimeControlMessageConflict
			}
		} else if current.Phase == contractsv1.RuntimeControlTurnEffectTerminal &&
			!sameNullableActiveTurn(current, proof) {
			return false, application.ErrRuntimeControlMessageConflict
		} else if runtimeControlEffectRank(application.RuntimeControlEffectPhase(proof.Phase)) <
			runtimeControlEffectRank(application.RuntimeControlEffectPhase(current.Phase)) ||
			proof.LastUpdateSequence < current.LastUpdateSequence {
			return false, application.ErrRuntimeControlMessageConflict
		}
	}
	authoritative := *proof
	state.ReportedActiveTurn = nil
	state.TerminalProofPending = false
	if proof.Phase == contractsv1.RuntimeControlTurnEffectTerminal {
		terminal, found, err := findRuntimeControlEventByFingerprint(
			ctx, tx, state.RuntimeInstanceID, proof.TerminalEnvelopeDigest,
		)
		if err != nil {
			return false, err
		}
		if !found {
			if proof.LastUpdateSequence <= state.EventCommittedSequence ||
				proof.LastUpdateSequence > record.Command.WorkerEventLastSequence {
				return false, application.ErrRuntimeControlCursorConflict
			}
			authoritative.Phase = contractsv1.RuntimeControlTurnEffectActive
			authoritative.TerminalEnvelopeDigest = ""
			state.ReportedActiveTurn = cloneActiveTurn(proof)
			state.TerminalProofPending = true
		} else {
			terminalEnvelope, decodeErr := contractsv1.DecodeRuntimeControlEnvelope(terminal.Envelope)
			if decodeErr != nil || terminalEnvelope.Kind != "event" ||
				terminalEnvelope.Sequence != proof.LastUpdateSequence ||
				string(terminalEnvelope.WorkerRunID) != string(proof.WorkerRunID) ||
				string(terminalEnvelope.AgentSessionID) != string(proof.AgentSessionID) ||
				string(terminalEnvelope.AgentTurnID) != string(proof.AgentTurnID) ||
				string(terminalEnvelope.WorkerSessionID) != state.WorkerSessionID ||
				string(terminalEnvelope.RuntimeInstanceID) != state.RuntimeInstanceID ||
				uint64(terminalEnvelope.RuntimeGeneration) != state.RuntimeGeneration ||
				terminalEnvelope.ChannelEpoch != state.ChannelEpoch ||
				terminalEnvelope.MessageType != "agent_turn.terminal" {
				return false, application.ErrRuntimeControlMessageConflict
			}
		}
	}
	turnConsumed := false
	if proof.Phase == contractsv1.RuntimeControlTurnEffectActive ||
		proof.Phase == contractsv1.RuntimeControlTurnEffectTerminal {
		switch turnStatus {
		case "delivered":
			tag, err := tx.Exec(ctx, `
UPDATE agent_turns SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND worker_run_id=$2 AND agent_session_id=$3 AND status='delivered'`,
				string(proof.AgentTurnID), string(proof.WorkerRunID), string(proof.AgentSessionID),
			)
			if err != nil {
				return false, fmt.Errorf("consume claimed Runtime control AgentTurn: %w", err)
			}
			turnConsumed = tag.RowsAffected() == 1
		case "consumed", "completed", "failed", "cancelled", "timed_out":
		default:
			return false, application.ErrRuntimeControlIdentityConflict
		}
	}
	effectPhase := authoritative.Phase
	if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET effect_status=$4,acknowledged_at=COALESCE(acknowledged_at,$5),
    applied_at=CASE WHEN $4 IN ('active','terminal')
      THEN COALESCE(applied_at,$5) ELSE applied_at END
WHERE runtime_instance_id=$1 AND direction='command' AND sequence=$2 AND message_id=$3`,
		state.RuntimeInstanceID, proof.StartCommandSequence, proof.StartMessageID,
		effectPhase, record.ClaimedAt,
	); err != nil {
		return false, fmt.Errorf("advance claimed Runtime control command effect: %w", err)
	}
	state.ActiveEffectMessageID = proof.StartMessageID
	state.ActiveEffectAgentTurnID = string(proof.AgentTurnID)
	state.ActiveEffectCommandSequence = proof.StartCommandSequence
	state.ActiveEffectPhase = application.RuntimeControlEffectPhase(effectPhase)
	state.ActiveEffectSnapshot = cloneActiveTurn(&authoritative)
	return turnConsumed, nil
}

func findRuntimeControlEventByFingerprint(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	fingerprint string,
) (application.RuntimeControlMessage, bool, error) {
	row := tx.QueryRow(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='event' AND fingerprint=$2
FOR UPDATE`,
		runtimeInstanceID, fingerprint,
	)
	message, err := scanRuntimeControlMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeControlMessage{}, false, nil
	}
	if err != nil {
		return application.RuntimeControlMessage{}, false, fmt.Errorf("find terminal Runtime control event: %w", err)
	}
	return message, true, nil
}

type runtimeControlIdentity struct {
	workerSessionID string
	runtimeID       string
	generation      uint64
	channelEpoch    uint64
}

func lockExactRuntimeControlIdentity(
	ctx context.Context,
	tx platformpostgres.Tx,
	identity runtimeControlIdentity,
) error {
	var activeRuntimeID, sessionStatus, runtimeStatus string
	var sessionGeneration, runtimeGeneration, channelEpoch uint64
	err := tx.QueryRow(ctx, `
SELECT COALESCE(session.active_runtime_instance_id,''),session.runtime_generation,session.status,
       runtime.generation,runtime.control_channel_epoch,runtime.status
FROM worker_sessions session
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=$2 AND runtime.worker_session_id=session.worker_session_id
WHERE session.worker_session_id=$1
FOR UPDATE OF session,runtime`,
		identity.workerSessionID,
		identity.runtimeID,
	).Scan(
		&activeRuntimeID,
		&sessionGeneration,
		&sessionStatus,
		&runtimeGeneration,
		&channelEpoch,
		&runtimeStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrRuntimeControlIdentityConflict
	}
	if err != nil {
		return fmt.Errorf("lock Runtime control identity: %w", err)
	}
	if activeRuntimeID != identity.runtimeID ||
		sessionGeneration != identity.generation ||
		runtimeGeneration != identity.generation ||
		channelEpoch != identity.channelEpoch ||
		!runtimeControlSessionStatusAllowed(sessionStatus) ||
		!runtimeControlRuntimeStatusAllowed(runtimeStatus) {
		return application.ErrRuntimeControlIdentityConflict
	}
	return nil
}

func lockRuntimeControlChannel(
	ctx context.Context,
	tx platformpostgres.Tx,
	identity runtimeControlIdentity,
) (application.RuntimeControlChannelState, error) {
	if err := lockExactRuntimeControlIdentity(ctx, tx, identity); err != nil {
		return application.RuntimeControlChannelState{}, err
	}
	state, err := readRuntimeControlChannel(ctx, tx, identity.runtimeID, true)
	if err != nil {
		return application.RuntimeControlChannelState{}, err
	}
	if state.WorkerSessionID != identity.workerSessionID ||
		state.RuntimeGeneration != identity.generation ||
		state.ChannelEpoch != identity.channelEpoch {
		return application.RuntimeControlChannelState{}, application.ErrRuntimeControlIdentityConflict
	}
	return state, nil
}

func lockFenceRuntimeControlChannel(
	ctx context.Context,
	tx platformpostgres.Tx,
	identity runtimeControlIdentity,
) (application.RuntimeControlChannelState, error) {
	var ignored int
	err := tx.QueryRow(ctx, `
SELECT 1
FROM worker_sessions session
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=$2 AND runtime.worker_session_id=session.worker_session_id
WHERE session.worker_session_id=$1
  AND runtime.generation=$3
  AND runtime.control_channel_epoch=$4
FOR UPDATE OF session,runtime`,
		identity.workerSessionID, identity.runtimeID, identity.generation, identity.channelEpoch,
	).Scan(&ignored)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeControlChannelState{}, application.ErrRuntimeControlIdentityConflict
	}
	if err != nil {
		return application.RuntimeControlChannelState{}, fmt.Errorf("lock fence Runtime control identity: %w", err)
	}
	state, err := readRuntimeControlChannel(ctx, tx, identity.runtimeID, true)
	if err != nil {
		return application.RuntimeControlChannelState{}, err
	}
	if state.WorkerSessionID != identity.workerSessionID ||
		state.RuntimeGeneration != identity.generation ||
		state.ChannelEpoch != identity.channelEpoch {
		return application.RuntimeControlChannelState{}, application.ErrRuntimeControlIdentityConflict
	}
	return state, nil
}

func lockRuntimeControlAuthority(
	ctx context.Context,
	tx platformpostgres.Tx,
	authority application.RuntimeControlAuthority,
) (application.RuntimeControlChannelState, error) {
	state, err := lockRuntimeControlChannel(ctx, tx, runtimeControlIdentity{
		workerSessionID: authority.WorkerSessionID,
		runtimeID:       authority.RuntimeInstanceID,
		generation:      authority.RuntimeGeneration,
		channelEpoch:    authority.ChannelEpoch,
	})
	if err != nil {
		return application.RuntimeControlChannelState{}, err
	}
	if state.ProcessNonce != authority.ProcessNonce ||
		state.StreamAttempt != authority.StreamAttempt ||
		state.OwnerFence != authority.OwnerFence ||
		(state.Status != "connected" && state.Status != "resuming") {
		return application.RuntimeControlChannelState{}, application.ErrRuntimeControlStaleOwner
	}
	return state, nil
}

const runtimeControlChannelColumns = `
worker_session_id,runtime_instance_id,runtime_generation,channel_epoch,credential_digest,
COALESCE(process_nonce,''),stream_attempt,COALESCE(owner_id,''),owner_fence,
COALESCE(resume_claim_revision,0),COALESCE(resume_ready_digest,''),
COALESCE(resume_completed_revision,0),resume_completed_at,
command_committed_sequence,command_committed_digest,command_acknowledged_sequence,
command_applied_sequence,event_committed_sequence,event_committed_digest,event_acknowledged_sequence,event_applied_sequence,
COALESCE(event_replay_checkpoint_sequence,0),COALESCE(event_replay_checkpoint_digest,''),
COALESCE(event_replay_target_sequence,0),COALESCE(event_replay_target_digest,''),
COALESCE(active_effect_message_id,''),COALESCE(active_effect_agent_turn_id,''),
COALESCE(active_effect_command_sequence,0),COALESCE(active_effect_phase,''),
active_effect_snapshot,reported_active_turn,terminal_proof_pending,status,
fenced_at,COALESCE(fence_revision,0),COALESCE(fence_owner_fence,0),revision`

func readRuntimeControlChannel(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	lock bool,
) (application.RuntimeControlChannelState, error) {
	query := "SELECT " + runtimeControlChannelColumns + `
 FROM runtime_control_channels WHERE runtime_instance_id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var state application.RuntimeControlChannelState
	var activeEffectPhase string
	var activeSnapshot, reportedSnapshot []byte
	var resumeCompletedAt, fencedAt *time.Time
	err := tx.QueryRow(ctx, query, runtimeInstanceID).Scan(
		&state.WorkerSessionID,
		&state.RuntimeInstanceID,
		&state.RuntimeGeneration,
		&state.ChannelEpoch,
		&state.CredentialDigest,
		&state.ProcessNonce,
		&state.StreamAttempt,
		&state.OwnerID,
		&state.OwnerFence,
		&state.ResumeClaimRevision,
		&state.ResumeReadyDigest,
		&state.ResumeCompletedRevision,
		&resumeCompletedAt,
		&state.CommandCommittedSequence,
		&state.CommandCommittedDigest,
		&state.CommandAcknowledgedSequence,
		&state.CommandAppliedSequence,
		&state.EventCommittedSequence,
		&state.EventCommittedDigest,
		&state.EventAcknowledgedSequence,
		&state.EventAppliedSequence,
		&state.EventReplayCheckpointSequence,
		&state.EventReplayCheckpointDigest,
		&state.EventReplayTargetSequence,
		&state.EventReplayTargetDigest,
		&state.ActiveEffectMessageID,
		&state.ActiveEffectAgentTurnID,
		&state.ActiveEffectCommandSequence,
		&activeEffectPhase,
		&activeSnapshot,
		&reportedSnapshot,
		&state.TerminalProofPending,
		&state.Status,
		&fencedAt,
		&state.FenceRevision,
		&state.FenceOwnerFence,
		&state.Revision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeControlChannelState{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return application.RuntimeControlChannelState{}, fmt.Errorf("read Runtime control channel: %w", err)
	}
	state.ActiveEffectPhase = application.RuntimeControlEffectPhase(activeEffectPhase)
	if resumeCompletedAt != nil {
		state.ResumeCompletedAt = resumeCompletedAt.UTC()
	}
	if fencedAt != nil {
		state.FencedAt = fencedAt.UTC()
	}
	if len(activeSnapshot) > 0 {
		state.ActiveEffectSnapshot = new(contractsv1.RuntimeControlActiveTurn)
		if err := json.Unmarshal(activeSnapshot, state.ActiveEffectSnapshot); err != nil {
			return application.RuntimeControlChannelState{}, fmt.Errorf("decode active Runtime control Turn: %w", err)
		}
	}
	if len(reportedSnapshot) > 0 {
		state.ReportedActiveTurn = new(contractsv1.RuntimeControlActiveTurn)
		if err := json.Unmarshal(reportedSnapshot, state.ReportedActiveTurn); err != nil {
			return application.RuntimeControlChannelState{}, fmt.Errorf("decode reported Runtime control Turn: %w", err)
		}
	}
	return state, nil
}

func runtimeControlCursorDigest(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	direction application.RuntimeControlDirection,
	sequence uint64,
) (string, error) {
	if sequence == 0 {
		return contractsv1.RuntimeControlCursorGenesisDigest, nil
	}
	var digest string
	if err := tx.QueryRow(ctx, `
SELECT cursor_digest
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction=$2 AND sequence=$3`,
		runtimeInstanceID,
		direction,
		sequence,
	).Scan(&digest); errors.Is(err, pgx.ErrNoRows) {
		return "", application.ErrRuntimeControlCursorConflict
	} else if err != nil {
		return "", fmt.Errorf("read Runtime control cursor digest: %w", err)
	}
	return digest, nil
}

func advanceRuntimeControlCommandAcknowledgement(
	ctx context.Context,
	tx platformpostgres.Tx,
	state *application.RuntimeControlChannelState,
	through uint64,
	at time.Time,
) error {
	switch {
	case through < state.CommandAcknowledgedSequence:
		return application.ErrRuntimeControlCursorConflict
	case through > state.CommandCommittedSequence:
		return application.ErrRuntimeControlSequenceGap
	case through == state.CommandAcknowledgedSequence:
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET acknowledged_at=COALESCE(acknowledged_at,$3),
    effect_status=CASE WHEN effect_status='journaled' THEN 'received' ELSE effect_status END
WHERE runtime_instance_id=$1 AND direction='command' AND sequence <= $2`,
		state.RuntimeInstanceID, through, at,
	); err != nil {
		return fmt.Errorf("acknowledge Runtime control commands: %w", err)
	}
	if err := tx.QueryRow(ctx, `
UPDATE runtime_control_channels
SET command_acknowledged_sequence=$2,revision=revision+1,updated_at=$3
WHERE runtime_instance_id=$1
RETURNING revision`,
		state.RuntimeInstanceID, through, at,
	).Scan(&state.Revision); err != nil {
		return fmt.Errorf("advance Runtime control acknowledgement: %w", err)
	}
	state.CommandAcknowledgedSequence = through
	return nil
}

func advanceRuntimeControlCommandApplied(
	ctx context.Context,
	tx platformpostgres.Tx,
	state *application.RuntimeControlChannelState,
	through uint64,
	proof *contractsv1.RuntimeControlActiveTurn,
	at time.Time,
) error {
	switch {
	case through < state.CommandAppliedSequence:
		return application.ErrRuntimeControlCursorConflict
	case through > state.CommandAcknowledgedSequence:
		return application.ErrRuntimeControlCursorConflict
	case through == state.CommandAppliedSequence:
		return nil
	}
	for sequence := state.CommandAppliedSequence + 1; sequence <= through; sequence++ {
		message, err := readRuntimeControlMessage(
			ctx, tx, state.RuntimeInstanceID, application.RuntimeControlDirectionCommand, sequence,
		)
		if err != nil {
			return application.ErrRuntimeControlCursorConflict
		}
		envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
		if err != nil {
			return application.ErrRuntimeControlMessageConflict
		}
		if envelope.MessageType == "agent_turn.start" {
			if proof == nil ||
				proof.Phase != contractsv1.RuntimeControlTurnEffectActive &&
					proof.Phase != contractsv1.RuntimeControlTurnEffectTerminal ||
				proof.StartCommandSequence != sequence ||
				proof.StartMessageID != message.MessageID ||
				proof.StartIdempotencyKey != message.IdempotencyKey ||
				proof.StartEnvelopeDigest != message.Fingerprint ||
				string(proof.WorkerRunID) != string(envelope.WorkerRunID) ||
				string(proof.AgentSessionID) != string(envelope.AgentSessionID) ||
				string(proof.AgentTurnID) != string(envelope.AgentTurnID) {
				return application.ErrRuntimeControlCursorConflict
			}
			if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET effect_status=$4,applied_at=COALESCE(applied_at,$5)
WHERE runtime_instance_id=$1 AND direction='command' AND sequence=$2 AND message_id=$3`,
				state.RuntimeInstanceID, sequence, message.MessageID, proof.Phase, at,
			); err != nil {
				return fmt.Errorf("mark Runtime control start command applied: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
UPDATE runtime_control_messages
SET effect_status='applied',applied_at=COALESCE(applied_at,$4)
WHERE runtime_instance_id=$1 AND direction='command' AND sequence=$2 AND message_id=$3`,
			state.RuntimeInstanceID, sequence, message.MessageID, at,
		); err != nil {
			return fmt.Errorf("mark Runtime control command applied: %w", err)
		}
	}
	state.CommandAppliedSequence = through
	return nil
}

func findRuntimeControlMessage(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	direction application.RuntimeControlDirection,
	messageID string,
	idempotencyKey string,
) (application.RuntimeControlMessage, bool, error) {
	rows, err := tx.Query(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction=$2
  AND (message_id=$3 OR idempotency_key=$4)
FOR UPDATE`,
		runtimeInstanceID,
		direction,
		messageID,
		idempotencyKey,
	)
	if err != nil {
		return application.RuntimeControlMessage{}, false, fmt.Errorf("find Runtime control message: %w", err)
	}
	defer rows.Close()
	var found application.RuntimeControlMessage
	count := 0
	for rows.Next() {
		message, err := scanRuntimeControlMessage(rows)
		if err != nil {
			return application.RuntimeControlMessage{}, false, err
		}
		found = message
		count++
	}
	if err := rows.Err(); err != nil {
		return application.RuntimeControlMessage{}, false, fmt.Errorf("iterate Runtime control messages: %w", err)
	}
	if count > 1 {
		return application.RuntimeControlMessage{}, false, application.ErrRuntimeControlMessageConflict
	}
	return found, count == 1, nil
}

func readRuntimeControlMessage(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	direction application.RuntimeControlDirection,
	sequence uint64,
) (application.RuntimeControlMessage, error) {
	row := tx.QueryRow(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction=$2 AND sequence=$3
FOR UPDATE`,
		runtimeInstanceID,
		direction,
		sequence,
	)
	message, err := scanRuntimeControlMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeControlMessage{}, platformpostgres.ErrNotFound
	}
	return message, err
}

func readRuntimeControlStartCommandForTurn(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	agentTurnID string,
) (application.RuntimeControlMessage, error) {
	rows, err := tx.Query(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1
  AND direction='command'
  AND envelope->>'message_type'='agent_turn.start'
  AND envelope->>'agent_turn_id'=$2
ORDER BY sequence
FOR UPDATE`,
		runtimeInstanceID,
		agentTurnID,
	)
	if err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"read Runtime control AgentTurn start command: %w",
			err,
		)
	}
	defer rows.Close()
	var (
		found application.RuntimeControlMessage
		count int
	)
	for rows.Next() {
		message, err := scanRuntimeControlMessage(rows)
		if err != nil {
			return application.RuntimeControlMessage{}, err
		}
		found = message
		count++
	}
	if err := rows.Err(); err != nil {
		return application.RuntimeControlMessage{}, fmt.Errorf(
			"iterate Runtime control AgentTurn start commands: %w",
			err,
		)
	}
	if count != 1 {
		return application.RuntimeControlMessage{}, application.ErrRuntimeControlMessageConflict
	}
	return found, nil
}

type runtimeControlRow interface {
	Scan(...any) error
}

func scanRuntimeControlMessage(row runtimeControlRow) (application.RuntimeControlMessage, error) {
	var message application.RuntimeControlMessage
	var direction string
	if err := row.Scan(
		&direction,
		&message.Sequence,
		&message.MessageID,
		&message.IdempotencyKey,
		&message.Fingerprint,
		&message.CursorDigest,
		&message.Envelope,
		&message.Outcome,
		&message.EffectStatus,
		&message.ApplyIdempotencyKey,
		&message.ApplyOutcomeDigest,
		&message.CreatedAt,
		&message.AcknowledgedAt,
		&message.AppliedAt,
	); err != nil {
		return application.RuntimeControlMessage{}, err
	}
	message.Direction = application.RuntimeControlDirection(direction)
	return message, nil
}

func loadRuntimeControlCommandsAfter(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	sequence uint64,
) ([]application.RuntimeControlMessage, error) {
	rows, err := tx.Query(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='command' AND sequence > $2
ORDER BY sequence`,
		runtimeInstanceID,
		sequence,
	)
	if err != nil {
		return nil, fmt.Errorf("load Runtime control replay commands: %w", err)
	}
	defer rows.Close()
	var messages []application.RuntimeControlMessage
	for rows.Next() {
		message, err := scanRuntimeControlMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Runtime control replay command: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Runtime control replay commands: %w", err)
	}
	return messages, nil
}

func loadRuntimeControlPendingEvents(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	appliedSequence uint64,
) ([]application.RuntimeControlMessage, error) {
	rows, err := tx.Query(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='event' AND sequence > $2
  AND effect_status='received'
ORDER BY sequence`,
		runtimeInstanceID, appliedSequence,
	)
	if err != nil {
		return nil, fmt.Errorf("load pending Runtime control events: %w", err)
	}
	defer rows.Close()
	var messages []application.RuntimeControlMessage
	for rows.Next() {
		message, err := scanRuntimeControlMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending Runtime control event: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending Runtime control events: %w", err)
	}
	return messages, nil
}

func runtimeControlEffectRank(phase application.RuntimeControlEffectPhase) int {
	switch phase {
	case application.RuntimeControlEffectReceived:
		return 1
	case application.RuntimeControlEffectPromptInvocationIntent:
		return 2
	case application.RuntimeControlEffectActive:
		return 3
	case application.RuntimeControlEffectTerminal:
		return 4
	default:
		return 0
	}
}

func runtimeControlTurnStatusAllowsEffect(
	status string,
	phase application.RuntimeControlEffectPhase,
) bool {
	switch status {
	case "delivered":
		return runtimeControlEffectRank(phase) > 0
	case "consumed":
		return phase == application.RuntimeControlEffectActive ||
			phase == application.RuntimeControlEffectTerminal
	case "completed", "failed", "cancelled", "timed_out":
		return phase == application.RuntimeControlEffectTerminal
	default:
		return false
	}
}

func runtimeControlRunStatusTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}

func runtimeControlAgentTurnStatusTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}

func runtimeControlSessionStatusAllowed(status string) bool {
	switch status {
	case "interactive", "running", "recovering", "waiting_user", "idle":
		return true
	default:
		return false
	}
}

func runtimeControlRuntimeStatusAllowed(status string) bool {
	switch status {
	case "starting", "active", "suspect":
		return true
	default:
		return false
	}
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func sameRuntimeControlLogicalEnvelope(
	left contractsv1.RuntimeControlEnvelope,
	right contractsv1.RuntimeControlEnvelope,
) bool {
	return left.SchemaVersion == right.SchemaVersion &&
		left.MessageID == right.MessageID &&
		left.Kind == right.Kind &&
		left.WorkerSessionID == right.WorkerSessionID &&
		left.WorkerRunID == right.WorkerRunID &&
		left.RuntimeInstanceID == right.RuntimeInstanceID &&
		left.RuntimeGeneration == right.RuntimeGeneration &&
		left.AgentSessionID == right.AgentSessionID &&
		left.AgentTurnID == right.AgentTurnID &&
		left.ChannelEpoch == right.ChannelEpoch &&
		left.IdempotencyKey == right.IdempotencyKey &&
		left.MessageType == right.MessageType &&
		sameCanonicalRuntimeControlJSON(left.Payload, right.Payload)
}

func sameCanonicalRuntimeControlJSON(left, right []byte) bool {
	canonical := func(value []byte) ([]byte, error) {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("JSON has trailing content")
		}
		return json.Marshal(decoded)
	}
	leftCanonical, leftErr := canonical(left)
	rightCanonical, rightErr := canonical(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func sameRuntimeControlStart(left, right contractsv1.RuntimeControlActiveTurn) bool {
	return left.WorkerRunID == right.WorkerRunID &&
		left.AgentSessionID == right.AgentSessionID &&
		left.AgentTurnID == right.AgentTurnID &&
		left.StartCommandSequence == right.StartCommandSequence &&
		left.StartMessageID == right.StartMessageID &&
		left.StartIdempotencyKey == right.StartIdempotencyKey &&
		left.StartEnvelopeDigest == right.StartEnvelopeDigest
}

func sameNullableActiveTurn(left, right *contractsv1.RuntimeControlActiveTurn) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return sameRuntimeControlStart(*left, *right) &&
		left.Phase == right.Phase &&
		left.LastUpdateSequence == right.LastUpdateSequence &&
		left.TerminalEnvelopeDigest == right.TerminalEnvelopeDigest
}

func cloneActiveTurn(value *contractsv1.RuntimeControlActiveTurn) *contractsv1.RuntimeControlActiveTurn {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func marshalNullableActiveTurn(value *contractsv1.RuntimeControlActiveTurn) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal Runtime control active Turn: %w", err)
	}
	return encoded, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableRuntimeControlUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func timePointer(value time.Time) *time.Time {
	return &value
}

var _ application.RuntimeControlJournal = (*RuntimeControlJournal)(nil)

// Pure transport receipts advance channel versions and mirrored cursors without
// changing Runtime lifecycle ownership. Every other kind/type pair retains CAS.
func runtimeControlLifecycleRevisionDelta(envelope contractsv1.RuntimeControlEnvelope) int64 {
	if (envelope.Kind == "heartbeat" && envelope.MessageType == "runtime.heartbeat") ||
		(envelope.Kind == "ack" && envelope.MessageType == "runtime.control.ack") {
		return 0
	}
	return 1
}
