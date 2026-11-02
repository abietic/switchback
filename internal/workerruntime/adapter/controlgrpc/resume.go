package controlgrpc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

// durableRuntimeControlJournal is the application seam used by the transport.
// PostgreSQL remains the sole owner of logical sequences, cumulative ACKs,
// journal bytes, and stream ownership; the Hub only holds live sockets.
type durableRuntimeControlJournal interface {
	Bind(
		context.Context,
		application.BindRuntimeControlChannelCommand,
	) (application.RuntimeControlChannelState, error)
	Claim(
		context.Context,
		application.ClaimRuntimeControlStreamCommand,
	) (application.RuntimeControlReplayPlan, error)
	ReserveCommand(
		context.Context,
		application.ReserveRuntimeControlCommandCommand,
	) (application.RuntimeControlMessage, error)
	AcceptEvent(
		context.Context,
		application.AcceptRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error)
	ApplyEvent(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error)
	Disconnect(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlChannelState, error)
	Fence(
		context.Context,
		application.FenceRuntimeControlChannelCommand,
	) (application.RuntimeControlChannelState, error)
	CompleteResume(
		context.Context,
		application.CompleteRuntimeControlResumeCommand,
	) (application.RuntimeControlChannelState, error)
	ReplayPlan(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error)
}

func acceptedResumeAck(plan application.RuntimeControlReplayPlan) (contractsv1.RuntimeControlResumeAck, error) {
	state := plan.Channel
	claimRevision := state.ResumeClaimRevision
	if claimRevision == 0 {
		return contractsv1.RuntimeControlResumeAck{}, fmt.Errorf("Runtime control claim revision is missing")
	}
	if plan.EventReplayFrom != 0 && plan.EventReplayFrom != state.EventCommittedSequence+1 {
		return contractsv1.RuntimeControlResumeAck{}, fmt.Errorf("Runtime control event replay checkpoint disagrees with plan")
	}
	disposition, authoritative := activeTurnDisposition(plan.TerminalProofPending, state.ActiveEffectSnapshot)
	ack := contractsv1.RuntimeControlResumeAck{
		Accepted:                         true,
		ServerConnectionID:               state.OwnerID,
		JournalRevision:                  claimRevision,
		BackendRXCommittedWorkerSequence: state.EventCommittedSequence,
		BackendRXAppliedWorkerSequence:   state.EventAppliedSequence,
		BackendRXCursorDigest:            state.EventCommittedDigest,
		BackendTXLastCommandSequence:     state.CommandCommittedSequence,
		BackendTXWorkerAckedSequence:     state.CommandAcknowledgedSequence,
		BackendTXWorkerAppliedSequence:   state.CommandAppliedSequence,
		BackendTXCursorDigest:            state.CommandCommittedDigest,
		ReplayWorkerFromSequence:         state.EventCommittedSequence + 1,
		ReplayBackendFromSequence:        state.CommandAcknowledgedSequence + 1,
		ActiveTurnDisposition:            disposition,
		AuthoritativeStart:               authoritative,
	}
	if err := ack.Validate(); err != nil {
		return contractsv1.RuntimeControlResumeAck{}, err
	}
	return ack, nil
}

func completedResume(
	connectionID string,
	state application.RuntimeControlChannelState,
) (contractsv1.RuntimeControlResumeComplete, error) {
	completedRevision := state.ResumeCompletedRevision
	if completedRevision == 0 {
		return contractsv1.RuntimeControlResumeComplete{},
			fmt.Errorf("Runtime control completed revision is missing")
	}
	disposition, authoritative := activeTurnDisposition(
		state.TerminalProofPending,
		state.ActiveEffectSnapshot,
	)
	complete := contractsv1.RuntimeControlResumeComplete{
		ServerConnectionID:               connectionID,
		JournalRevision:                  completedRevision,
		BackendRXCommittedWorkerSequence: state.EventCommittedSequence,
		BackendRXAppliedWorkerSequence:   state.EventAppliedSequence,
		BackendRXCursorDigest:            state.EventCommittedDigest,
		BackendTXLastCommandSequence:     state.CommandCommittedSequence,
		BackendTXWorkerAckedSequence:     state.CommandAcknowledgedSequence,
		BackendTXWorkerAppliedSequence:   state.CommandAppliedSequence,
		BackendTXCursorDigest:            state.CommandCommittedDigest,
		ActiveTurnDisposition:            disposition,
		AuthoritativeStart:               authoritative,
	}
	if err := complete.Validate(); err != nil {
		return contractsv1.RuntimeControlResumeComplete{}, err
	}
	return complete, nil
}

func activeTurnDisposition(
	terminalProofPending bool,
	snapshot *contractsv1.RuntimeControlActiveTurn,
) (string, *contractsv1.RuntimeControlActiveTurn) {
	if snapshot == nil {
		return contractsv1.RuntimeControlResumeNoActiveTurn, nil
	}
	cloned := *snapshot
	if terminalProofPending {
		return contractsv1.RuntimeControlResumeContinuityUnproven, &cloned
	}
	switch snapshot.Phase {
	case contractsv1.RuntimeControlTurnEffectActive:
		return contractsv1.RuntimeControlResumeContinueActive, &cloned
	case contractsv1.RuntimeControlTurnEffectTerminal:
		return contractsv1.RuntimeControlResumeAlreadyTerminal, &cloned
	default:
		return contractsv1.RuntimeControlResumeContinuityUnproven, &cloned
	}
}

func decodeJournalEnvelope(message application.RuntimeControlMessage) (contractsv1.RuntimeControlEnvelope, error) {
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(message.Envelope)
	if err != nil {
		return contractsv1.RuntimeControlEnvelope{}, fmt.Errorf(
			"decode journaled Runtime control envelope at sequence %d: %w",
			message.Sequence,
			err,
		)
	}
	if envelope.Sequence != message.Sequence || envelope.MessageID != message.MessageID ||
		envelope.IdempotencyKey != message.IdempotencyKey {
		return contractsv1.RuntimeControlEnvelope{}, fmt.Errorf(
			"journaled Runtime control envelope identity disagrees at sequence %d",
			message.Sequence,
		)
	}
	return envelope, nil
}

type journalEventDelivery struct {
	hub       *Hub
	key       runtimeKey
	journal   durableRuntimeControlJournal
	authority application.RuntimeControlAuthority
	message   application.RuntimeControlMessage
	applied   chan struct{}
	retry     chan struct{}
	mu        sync.Mutex
	published bool
	observed  bool
}

func (delivery *journalEventDelivery) Apply(ctx context.Context, outcome json.RawMessage) error {
	return delivery.apply(ctx, outcome, nil, nil)
}

func (delivery *journalEventDelivery) ApplyTurnSettlement(
	ctx context.Context,
	outcome json.RawMessage,
	authority application.TurnSettlementApplyAuthority,
) error {
	return delivery.apply(ctx, outcome, &authority, nil)
}

func (delivery *journalEventDelivery) ApplyModelObservation(ctx context.Context, outcome json.RawMessage, capture application.RuntimeObservationCapture) error {
	capture = capture.Clone()
	return delivery.apply(ctx, outcome, nil, &capture)
}

func (delivery *journalEventDelivery) apply(
	ctx context.Context,
	outcome json.RawMessage,
	settlement *application.TurnSettlementApplyAuthority,
	capture *application.RuntimeObservationCapture,
) error {
	if delivery == nil || delivery.journal == nil {
		return fmt.Errorf("WorkerControl event delivery is not configured")
	}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	authority := delivery.authority
	if delivery.hub != nil {
		var err error
		authority, err = delivery.hub.deliveryAuthority(delivery.key)
		if err != nil {
			delivery.releaseForRetryLocked()
			return err
		}
	}
	if err := delivery.validateTurnEffectDisposition(outcome); err != nil {
		delivery.releaseForRetryLocked()
		return err
	}
	_, err := delivery.journal.ApplyEvent(ctx, application.ApplyRuntimeControlEventCommand{
		Authority:          authority,
		Sequence:           delivery.message.Sequence,
		MessageID:          delivery.message.MessageID,
		IdempotencyKey:     delivery.message.IdempotencyKey,
		Outcome:            append(json.RawMessage(nil), outcome...),
		TurnSettlement:     settlement,
		ObservationCapture: capture,
	})
	if err != nil {
		delivery.releaseForRetryLocked()
		return err
	}
	select {
	case <-delivery.applied:
	default:
		close(delivery.applied)
	}
	if delivery.hub != nil {
		delivery.hub.forgetDelivery(delivery.key, delivery.message.Sequence, delivery)
	}
	return nil
}

func (delivery *journalEventDelivery) validateTurnEffectDisposition(
	outcome json.RawMessage,
) error {
	if len(delivery.message.Envelope) == 0 {
		return nil
	}
	envelope, err := decodeJournalEnvelope(delivery.message)
	if err != nil {
		return err
	}
	if envelope.MessageType != "agent_turn.effect" &&
		envelope.MessageType != "agent_turn.terminal" {
		return nil
	}
	var applicationOutcome struct {
		EffectDisposition string `json:"effect_disposition"`
	}
	if err := json.Unmarshal(outcome, &applicationOutcome); err != nil {
		return fmt.Errorf("decode AgentTurn effect application outcome")
	}
	switch applicationOutcome.EffectDisposition {
	case "ignore":
		return nil
	case "commit":
		return nil
	case "":
		return fmt.Errorf("AgentTurn effect application outcome lacks effect_disposition")
	default:
		return fmt.Errorf(
			"AgentTurn effect application disposition %q is invalid",
			applicationOutcome.EffectDisposition,
		)
	}
}

func (delivery *journalEventDelivery) publish(
	ctx context.Context,
	active *connection,
	event application.WorkerControlEvent,
	observer application.WorkerControlEventObserver,
	observedAt time.Time,
) (bool, error) {
	if delivery == nil || active == nil {
		return false, ErrNotConnected
	}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if delivery.published {
		return false, nil
	}
	if delivery.retry != nil {
		select {
		case <-delivery.retry:
		default:
		}
	}
	if observer != nil && !delivery.observed {
		if err := observer.ObserveWorkerControlEvent(ctx, event, observedAt); err != nil {
			return false, fmt.Errorf("observe WorkerControl event: %w", err)
		}
		delivery.observed = true
	}
	select {
	case <-active.done:
		return false, nil
	case active.events <- event:
		delivery.published = true
		return true, nil
	}
}

func (delivery *journalEventDelivery) releaseForRetry() {
	if delivery == nil {
		return
	}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	delivery.releaseForRetryLocked()
}

func (delivery *journalEventDelivery) ReleaseForRetry() {
	delivery.releaseForRetry()
}

func (delivery *journalEventDelivery) releaseForRetryLocked() {
	delivery.published = false
	if delivery.retry == nil {
		return
	}
	select {
	case delivery.retry <- struct{}{}:
	default:
	}
}

func (delivery *journalEventDelivery) isPublished() bool {
	if delivery == nil {
		return false
	}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	return delivery.published
}

func envelopeFingerprint(envelope contractsv1.RuntimeControlEnvelope) [32]byte {
	encoded, _ := json.Marshal(envelope)
	return sha256.Sum256(encoded)
}

func logicalFingerprint(envelope contractsv1.RuntimeControlEnvelope) [32]byte {
	envelope.Sequence = 0
	envelope.AckSequence = 0
	envelope.SentAt = time.Time{}
	return envelopeFingerprint(envelope)
}
