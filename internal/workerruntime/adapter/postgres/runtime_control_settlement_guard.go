package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

// authenticateRuntimeControlSettlementGuardClaim keeps unauthenticated callers
// out of the AgentTurn outcome lock domain. The channel identity and credential
// are immutable after Bind; Claim revalidates them under the normal graph and
// channel locks after the settlement guard.
func authenticateRuntimeControlSettlementGuardClaim(
	ctx context.Context,
	tx platformpostgres.Tx,
	identity runtimeControlIdentity,
	credentialDigest string,
) error {
	state, err := readRuntimeControlChannel(
		ctx,
		tx,
		identity.runtimeID,
		false,
	)
	if errors.Is(err, platformpostgres.ErrNotFound) {
		return application.ErrRuntimeControlIdentityConflict
	}
	if err != nil {
		return err
	}
	if state.WorkerSessionID != identity.workerSessionID ||
		state.RuntimeGeneration != identity.generation ||
		state.ChannelEpoch != identity.channelEpoch {
		return application.ErrRuntimeControlIdentityConflict
	}
	if !constantTimeStringEqual(
		state.CredentialDigest,
		credentialDigest,
	) {
		return application.ErrRuntimeControlCredentialConflict
	}
	return nil
}

// guardRuntimeControlSettlementAdmission serializes with every durable owner
// of an AgentTurn outcome before the caller locks Session/Runtime/channel.
// An unfinished settlement admits only the exact Backend deadline reconnect
// window after its interrupt is durable and strictly before force_after.
func guardRuntimeControlSettlementAdmission(
	ctx context.Context,
	tx platformpostgres.Tx,
	identity runtimeControlIdentity,
	activeTurn *contractsv1.RuntimeControlActiveTurn,
	receivedCommandThrough *uint64,
	at time.Time,
) error {
	turnIDs, err := runtimeControlSettlementGuardTurnIDs(
		ctx,
		tx,
		identity,
		activeTurn,
	)
	if err != nil {
		return err
	}
	lockedTurns := make(map[string]struct{}, len(turnIDs))
	for _, agentTurnID := range turnIDs {
		if err := lockAgentTurnOutcome(ctx, tx, agentTurnID); err != nil {
			return err
		}
		lockedTurns[agentTurnID] = struct{}{}
	}

	pending, err := runtimeControlPendingSettlementIDs(
		ctx,
		tx,
		identity.workerSessionID,
	)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if len(pending) != 1 || activeTurn == nil {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	if _, locked := lockedTurns[pending[0].agentTurnID]; !locked {
		return application.ErrRuntimeControlPendingTurnSettlement
	}

	settlement, err := readTurnSettlement(
		ctx,
		tx,
		pending[0].settlementID,
		true,
	)
	if err != nil {
		return fmt.Errorf(
			"lock pending Turn settlement for Runtime control admission: %w",
			err,
		)
	}
	if settlement.Phase == application.TurnSettlementCompleted {
		return nil
	}
	if !runtimeControlSettlementIdentityMatches(
		settlement,
		identity,
		*activeTurn,
	) {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	disposition, err :=
		application.ClassifyTurnSettlementForStartup(settlement, at)
	if err != nil ||
		!disposition.AllowExactReconnect ||
		disposition.Action !=
			application.TurnSettlementStartupWaitForceAfter {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	sourceState, err :=
		application.DecodeBackendDeadlineSourceState(settlement.Source)
	if err != nil ||
		sourceState.Phase !=
			application.BackendDeadlineSourceInterruptQueued ||
		!at.Before(sourceState.ForceAfter) ||
		sourceState.RuntimeInstanceID != identity.runtimeID ||
		sourceState.RuntimeGeneration != identity.generation ||
		sourceState.ChannelEpoch != identity.channelEpoch ||
		sourceState.Interrupt == nil {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	if receivedCommandThrough != nil &&
		*receivedCommandThrough < sourceState.Interrupt.Sequence {
		return application.ErrRuntimeControlResumeSnapshotChanged
	}
	if err := validateRuntimeControlSettlementInterrupt(
		ctx,
		tx,
		settlement,
		sourceState,
		*activeTurn,
	); err != nil {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	return nil
}

type runtimeControlPendingSettlementID struct {
	settlementID string
	agentTurnID  string
}

func runtimeControlSettlementGuardTurnIDs(
	ctx context.Context,
	tx platformpostgres.Tx,
	identity runtimeControlIdentity,
	activeTurn *contractsv1.RuntimeControlActiveTurn,
) ([]string, error) {
	var reportedAgentTurnID string
	if activeTurn != nil {
		reportedAgentTurnID = string(activeTurn.AgentTurnID)
	}
	rows, err := tx.Query(ctx, `
SELECT candidate.agent_turn_id
FROM (
  SELECT NULLIF($3,'') AS agent_turn_id
  UNION
  SELECT channel.active_effect_agent_turn_id
  FROM runtime_control_channels channel
  WHERE channel.worker_session_id=$1
    AND channel.runtime_instance_id=$2
    AND channel.active_effect_agent_turn_id IS NOT NULL
  UNION
  SELECT turn.agent_turn_id
  FROM worker_sessions session
  JOIN worker_runs run
    ON run.worker_session_id=session.worker_session_id
   AND run.worker_run_id=session.active_worker_run_id
  JOIN agent_turns turn
    ON turn.worker_session_id=session.worker_session_id
   AND turn.worker_run_id=run.worker_run_id
  WHERE session.worker_session_id=$1
    AND turn.status IN ('accepted','delivered','consumed')
  UNION
  SELECT settlement.agent_turn_id
  FROM worker_turn_settlements settlement
  WHERE settlement.worker_session_id=$1
    AND settlement.phase <> 'completed'
) candidate
WHERE candidate.agent_turn_id IS NOT NULL
ORDER BY candidate.agent_turn_id`,
		identity.workerSessionID,
		identity.runtimeID,
		reportedAgentTurnID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list AgentTurn outcome locks for Runtime control admission: %w",
			err,
		)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var agentTurnID string
		if err := rows.Scan(&agentTurnID); err != nil {
			return nil, fmt.Errorf(
				"scan AgentTurn outcome lock for Runtime control admission: %w",
				err,
			)
		}
		result = append(result, agentTurnID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate AgentTurn outcome locks for Runtime control admission: %w",
			err,
		)
	}
	return result, nil
}

func runtimeControlPendingSettlementIDs(
	ctx context.Context,
	tx platformpostgres.Tx,
	workerSessionID string,
) ([]runtimeControlPendingSettlementID, error) {
	rows, err := tx.Query(ctx, `
SELECT settlement_id,agent_turn_id
FROM worker_turn_settlements
WHERE worker_session_id=$1
  AND phase <> 'completed'
ORDER BY accepted_at,settlement_id`,
		workerSessionID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list pending Turn settlements for Runtime control admission: %w",
			err,
		)
	}
	defer rows.Close()
	var result []runtimeControlPendingSettlementID
	for rows.Next() {
		var pending runtimeControlPendingSettlementID
		if err := rows.Scan(
			&pending.settlementID,
			&pending.agentTurnID,
		); err != nil {
			return nil, fmt.Errorf(
				"scan pending Turn settlement for Runtime control admission: %w",
				err,
			)
		}
		result = append(result, pending)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate pending Turn settlements for Runtime control admission: %w",
			err,
		)
	}
	return result, nil
}

func runtimeControlSettlementIdentityMatches(
	settlement application.TurnSettlement,
	identity runtimeControlIdentity,
	activeTurn contractsv1.RuntimeControlActiveTurn,
) bool {
	return settlement.WorkerSessionID == identity.workerSessionID &&
		settlement.WorkerRunID == string(activeTurn.WorkerRunID) &&
		settlement.AgentSessionID == string(activeTurn.AgentSessionID) &&
		settlement.AgentTurnID == string(activeTurn.AgentTurnID)
}

func validateRuntimeControlSettlementInterrupt(
	ctx context.Context,
	tx platformpostgres.Tx,
	settlement application.TurnSettlement,
	sourceState application.BackendDeadlineSourceState,
	activeTurn contractsv1.RuntimeControlActiveTurn,
) error {
	proof := sourceState.Interrupt
	if proof == nil {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	message, err := readRuntimeControlSettlementMessageSnapshot(
		ctx,
		tx,
		sourceState.RuntimeInstanceID,
		application.RuntimeControlDirectionCommand,
		proof.Sequence,
	)
	if err != nil ||
		message.MessageID != proof.MessageID ||
		message.IdempotencyKey != proof.IdempotencyKey ||
		message.Fingerprint != proof.EnvelopeDigest ||
		message.CursorDigest != proof.CursorDigest ||
		!message.CreatedAt.Equal(proof.JournaledAt) {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil ||
		envelope.Kind != "command" ||
		envelope.MessageType != "agent_turn.interrupt" ||
		envelope.Sequence != proof.Sequence ||
		envelope.MessageID != proof.MessageID ||
		envelope.IdempotencyKey != proof.IdempotencyKey ||
		string(envelope.WorkerSessionID) != settlement.WorkerSessionID ||
		string(envelope.WorkerRunID) != settlement.WorkerRunID ||
		string(envelope.AgentSessionID) != settlement.AgentSessionID ||
		string(envelope.AgentTurnID) != settlement.AgentTurnID ||
		envelope.WorkerRunID != activeTurn.WorkerRunID ||
		envelope.AgentSessionID != activeTurn.AgentSessionID ||
		envelope.AgentTurnID != activeTurn.AgentTurnID ||
		string(envelope.RuntimeInstanceID) != sourceState.RuntimeInstanceID ||
		uint64(envelope.RuntimeGeneration) !=
			sourceState.RuntimeGeneration ||
		envelope.ChannelEpoch != sourceState.ChannelEpoch {
		return application.ErrRuntimeControlPendingTurnSettlement
	}
	return nil
}

// readRuntimeControlSettlementMessageSnapshot reads only an immutable proof
// snapshot. The settlement guard already owns the AgentTurn outcome advisory
// lock and the settlement row; taking the command-message row lock here would
// invert the ordinary channel -> message acknowledgement order.
func readRuntimeControlSettlementMessageSnapshot(
	ctx context.Context,
	tx platformpostgres.Tx,
	runtimeInstanceID string,
	direction application.RuntimeControlDirection,
	sequence uint64,
) (application.RuntimeControlMessage, error) {
	row := tx.QueryRow(ctx, `
SELECT direction,sequence,message_id,idempotency_key,fingerprint,cursor_digest,
       envelope,outcome,effect_status,COALESCE(apply_idempotency_key,''),
       COALESCE(apply_outcome_digest,''),
       created_at,acknowledged_at,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction=$2 AND sequence=$3`,
		runtimeInstanceID,
		direction,
		sequence,
	)
	message, err := scanRuntimeControlMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeControlMessage{},
			platformpostgres.ErrNotFound
	}
	return message, err
}
