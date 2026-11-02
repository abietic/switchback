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

type DurableTerminalEventLoader struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewDurableTerminalEventLoader(
	pool *pgxpool.Pool,
	now func() time.Time,
) *DurableTerminalEventLoader {
	return &DurableTerminalEventLoader{pool: pool, now: now}
}

func (loader *DurableTerminalEventLoader) LoadDurableTerminalEvent(
	ctx context.Context,
	header application.DurableTerminalEventHeader,
) (application.WorkerControlEvent, error) {
	if loader == nil || loader.pool == nil || loader.now == nil {
		return application.WorkerControlEvent{},
			fmt.Errorf("durable terminal event loader dependencies are required")
	}
	var result application.WorkerControlEvent
	err := platformpostgres.InTransaction(
		ctx,
		loader.pool,
		func(tx platformpostgres.Tx) error {
			settlement, err := readTurnSettlement(
				ctx,
				tx,
				header.SettlementID,
				true,
			)
			if err != nil {
				return err
			}
			if !durableTerminalHeaderMatchesSettlement(
				header,
				settlement,
			) {
				return application.ErrTurnSettlementConflict
			}
			if err := validateDurableTerminalSettlementSource(
				settlement,
			); err != nil {
				return err
			}

			var (
				channelWorkerSessionID   string
				channelRuntimeGeneration uint64
				channelEpoch             uint64
				processNonce             string
				streamAttempt            uint64
				ownerFence               uint64
				channelStatus            string
				eventCommittedSequence   uint64
				eventCommittedDigest     string
				eventAppliedSequence     uint64
				messageID                string
				eventIdempotencyKey      string
				messageFingerprint       string
				messageCursorDigest      string
				encodedEnvelope          json.RawMessage
				effectStatus             string
				messageCreatedAt         time.Time
			)
			err = tx.QueryRow(ctx, `
SELECT
  channel.worker_session_id,
  channel.runtime_generation,
  channel.channel_epoch,
  COALESCE(channel.process_nonce,''),
  channel.stream_attempt,
  channel.owner_fence,
  channel.status,
  channel.event_committed_sequence,
  channel.event_committed_digest,
  channel.event_applied_sequence,
  message.message_id,
  message.idempotency_key,
  message.fingerprint,
  message.cursor_digest,
  message.envelope,
  message.effect_status,
  message.created_at
FROM worker_turn_settlements settlement
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=settlement.runtime_instance_id
JOIN runtime_control_messages message
  ON message.runtime_instance_id=settlement.runtime_instance_id
 AND message.direction=settlement.terminal_event_direction
 AND message.sequence=settlement.terminal_event_sequence
WHERE settlement.settlement_id=$1
  AND settlement.revision=$2
  AND settlement.worker_session_id=$3
  AND settlement.worker_run_id=$4
  AND settlement.agent_session_id=$5
  AND settlement.agent_turn_id=$6
  AND settlement.runtime_instance_id=$7
  AND settlement.runtime_generation=$8
  AND settlement.channel_epoch=$9
  AND settlement.terminal_event_direction='event'
  AND settlement.terminal_event_sequence=$10
  AND settlement.terminal_message_id=$11
  AND settlement.terminal_idempotency_key=$12
  AND settlement.terminal_envelope_digest=$13
  AND settlement.source_kind=$14
  AND settlement.source_state_digest=$15`,
				header.SettlementID,
				header.ExpectedSettlementRevision,
				header.WorkerSessionID,
				header.WorkerRunID,
				header.AgentSessionID,
				header.AgentTurnID,
				header.RuntimeInstanceID,
				header.RuntimeGeneration,
				header.ChannelEpoch,
				header.EventSequence,
				header.MessageID,
				header.EventIdempotencyKey,
				header.EnvelopeDigest,
				header.SourceKind,
				header.SourceStateDigest,
			).Scan(
				&channelWorkerSessionID,
				&channelRuntimeGeneration,
				&channelEpoch,
				&processNonce,
				&streamAttempt,
				&ownerFence,
				&channelStatus,
				&eventCommittedSequence,
				&eventCommittedDigest,
				&eventAppliedSequence,
				&messageID,
				&eventIdempotencyKey,
				&messageFingerprint,
				&messageCursorDigest,
				&encodedEnvelope,
				&effectStatus,
				&messageCreatedAt,
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrTurnSettlementConflict
			}
			if err != nil {
				return fmt.Errorf(
					"read durable terminal journal message: %w",
					err,
				)
			}
			if channelWorkerSessionID != header.WorkerSessionID ||
				channelRuntimeGeneration != header.RuntimeGeneration ||
				channelEpoch != header.ChannelEpoch ||
				(channelStatus != "connected" &&
					channelStatus != "resuming") ||
				processNonce == "" ||
				streamAttempt == 0 ||
				ownerFence == 0 {
				return application.ErrRuntimeControlStaleOwner
			}
			terminalCursorDigest, err := runtimeControlCursorDigest(
				ctx,
				tx,
				header.RuntimeInstanceID,
				application.RuntimeControlDirectionEvent,
				header.EventSequence,
			)
			if err != nil {
				return err
			}
			committedCursorDigest, err := runtimeControlCursorDigest(
				ctx,
				tx,
				header.RuntimeInstanceID,
				application.RuntimeControlDirectionEvent,
				eventCommittedSequence,
			)
			if err != nil {
				return err
			}
			if messageID != header.MessageID ||
				eventIdempotencyKey != header.EventIdempotencyKey ||
				messageFingerprint != header.EnvelopeDigest ||
				eventCommittedSequence < header.EventSequence ||
				terminalCursorDigest != messageCursorDigest ||
				committedCursorDigest != eventCommittedDigest {
				return application.ErrRuntimeControlMessageConflict
			}
			switch effectStatus {
			case "received":
				if eventAppliedSequence+1 != header.EventSequence {
					return application.ErrRuntimeControlSequenceGap
				}
			case "applied":
				if eventAppliedSequence < header.EventSequence {
					return application.ErrRuntimeControlCursorConflict
				}
			default:
				return application.ErrRuntimeControlMessageConflict
			}
			envelope, err := contractsv1.DecodeRuntimeControlEnvelope(
				encodedEnvelope,
			)
			if err != nil {
				return application.ErrRuntimeControlMessageConflict
			}
			fingerprint, err :=
				contractsv1.RuntimeControlEnvelopeDigest(envelope)
			if err != nil ||
				fingerprint != messageFingerprint ||
				envelope.Kind != "event" ||
				envelope.MessageType != "agent_turn.terminal" ||
				string(envelope.WorkerSessionID) !=
					header.WorkerSessionID ||
				string(envelope.WorkerRunID) != header.WorkerRunID ||
				string(envelope.AgentSessionID) !=
					header.AgentSessionID ||
				string(envelope.AgentTurnID) != header.AgentTurnID ||
				string(envelope.RuntimeInstanceID) !=
					header.RuntimeInstanceID ||
				uint64(envelope.RuntimeGeneration) !=
					header.RuntimeGeneration ||
				envelope.ChannelEpoch != header.ChannelEpoch ||
				envelope.Sequence != header.EventSequence ||
				envelope.MessageID != header.MessageID ||
				envelope.IdempotencyKey !=
					header.EventIdempotencyKey {
				return application.ErrRuntimeControlMessageConflict
			}
			if err := validateDurableTerminalObservedAt(
				settlement,
				messageCreatedAt.UTC(),
			); err != nil {
				return err
			}
			result = application.WorkerControlEvent{
				Envelope: envelope,
				Payload: append(
					json.RawMessage(nil),
					envelope.Payload...,
				),
				Delivery: &durableTerminalEventDelivery{
					journal: application.RuntimeControlJournalService{
						Store: NewRuntimeControlJournal(loader.pool),
						Now:   loader.now,
					},
					authority: application.RuntimeControlAuthority{
						WorkerSessionID:   header.WorkerSessionID,
						RuntimeInstanceID: header.RuntimeInstanceID,
						RuntimeGeneration: header.RuntimeGeneration,
						ChannelEpoch:      header.ChannelEpoch,
						ProcessNonce:      processNonce,
						StreamAttempt:     streamAttempt,
						OwnerFence:        ownerFence,
					},
					sequence:       header.EventSequence,
					messageID:      header.MessageID,
					idempotencyKey: header.EventIdempotencyKey,
				},
			}
			return nil
		},
	)
	return result, err
}

func durableTerminalHeaderMatchesSettlement(
	header application.DurableTerminalEventHeader,
	settlement application.TurnSettlement,
) bool {
	return settlement.SettlementID == header.SettlementID &&
		settlement.Revision == header.ExpectedSettlementRevision &&
		settlement.WorkerSessionID == header.WorkerSessionID &&
		settlement.WorkerRunID == header.WorkerRunID &&
		settlement.AgentSessionID == header.AgentSessionID &&
		settlement.AgentTurnID == header.AgentTurnID &&
		settlement.RuntimeInstanceID == header.RuntimeInstanceID &&
		settlement.RuntimeGeneration == header.RuntimeGeneration &&
		settlement.ChannelEpoch == header.ChannelEpoch &&
		settlement.TerminalEventSequence == header.EventSequence &&
		settlement.TerminalMessageID == header.MessageID &&
		settlement.TerminalIdempotencyKey ==
			header.EventIdempotencyKey &&
		settlement.TerminalEnvelopeDigest == header.EnvelopeDigest &&
		settlement.Source.Kind == header.SourceKind &&
		settlement.Source.StateDigest == header.SourceStateDigest
}

func validateDurableTerminalSettlementSource(
	settlement application.TurnSettlement,
) error {
	if err := application.ValidateTurnSettlementSource(
		settlement.Source,
	); err != nil {
		return err
	}
	switch settlement.Source.Kind {
	case application.TurnSettlementSourceRuntimeEvent:
		if settlement.Source.Ref != settlement.TerminalMessageID ||
			settlement.Source.Digest !=
				settlement.TerminalEnvelopeDigest {
			return application.ErrTurnSettlementSourceConflict
		}
	case application.TurnSettlementSourceBackendDeadline:
		state, err := application.DecodeBackendDeadlineSourceState(
			settlement.Source,
		)
		if err != nil ||
			state.Phase !=
				application.BackendDeadlineSourceTerminalObserved ||
			state.RuntimeInstanceID != settlement.RuntimeInstanceID ||
			state.RuntimeGeneration != settlement.RuntimeGeneration ||
			state.ChannelEpoch != settlement.ChannelEpoch {
			return application.ErrTurnSettlementSourceConflict
		}
	default:
		return application.ErrTurnSettlementSourceConflict
	}
	return nil
}

func validateDurableTerminalObservedAt(
	settlement application.TurnSettlement,
	messageCreatedAt time.Time,
) error {
	switch settlement.Source.Kind {
	case application.TurnSettlementSourceRuntimeEvent:
		if !settlement.Source.OccurredAt.Equal(messageCreatedAt) {
			return application.ErrTurnSettlementSourceConflict
		}
	case application.TurnSettlementSourceBackendDeadline:
		state, err := application.DecodeBackendDeadlineSourceState(
			settlement.Source,
		)
		if err != nil ||
			!state.TerminalObservedAt.Equal(messageCreatedAt) ||
			messageCreatedAt.Before(settlement.Source.OccurredAt) {
			return application.ErrTurnSettlementSourceConflict
		}
	default:
		return application.ErrTurnSettlementSourceConflict
	}
	return nil
}

type durableTerminalEventDelivery struct {
	journal        application.RuntimeControlJournalService
	authority      application.RuntimeControlAuthority
	sequence       uint64
	messageID      string
	idempotencyKey string
}

func (delivery *durableTerminalEventDelivery) Apply(
	ctx context.Context,
	outcome json.RawMessage,
) error {
	return delivery.apply(ctx, outcome, nil)
}

func (delivery *durableTerminalEventDelivery) ApplyTurnSettlement(
	ctx context.Context,
	outcome json.RawMessage,
	authority application.TurnSettlementApplyAuthority,
) error {
	return delivery.apply(ctx, outcome, &authority)
}

func (delivery *durableTerminalEventDelivery) apply(
	ctx context.Context,
	outcome json.RawMessage,
	settlement *application.TurnSettlementApplyAuthority,
) error {
	if delivery == nil {
		return fmt.Errorf("durable terminal event delivery is not configured")
	}
	_, err := delivery.journal.ApplyEvent(
		ctx,
		application.ApplyRuntimeControlEventCommand{
			Authority:      delivery.authority,
			Sequence:       delivery.sequence,
			MessageID:      delivery.messageID,
			IdempotencyKey: delivery.idempotencyKey,
			Outcome: append(
				json.RawMessage(nil),
				outcome...,
			),
			TurnSettlement: settlement,
		},
	)
	return err
}

// Durable database loads do not reserve an in-memory Hub delivery lease.
// RuntimeControlJournal.ApplyEvent owns idempotency and any retry.
func (*durableTerminalEventDelivery) ReleaseForRetry() {}

var _ application.DurableTerminalEventStore = (*DurableTerminalEventLoader)(nil)
var _ application.TurnSettlementWorkerControlEventDelivery = (*durableTerminalEventDelivery)(nil)
