package application

import (
	"context"
	"fmt"
)

// DurableTerminalEventHeader freezes the exact immutable journal identity a
// Turn settlement accepted. The settlement revision and source-state digest
// make a caller's startup snapshot explicit instead of asking an adapter to
// silently follow a newer branch.
type DurableTerminalEventHeader struct {
	SettlementID               string
	ExpectedSettlementRevision uint64
	WorkerSessionID            string
	WorkerRunID                string
	AgentSessionID             string
	AgentTurnID                string
	RuntimeInstanceID          string
	RuntimeGeneration          uint64
	ChannelEpoch               uint64
	EventSequence              uint64
	MessageID                  string
	EventIdempotencyKey        string
	EnvelopeDigest             string
	SourceKind                 TurnSettlementSourceKind
	SourceStateDigest          string
}

type DurableTerminalEventStore interface {
	LoadDurableTerminalEvent(
		context.Context,
		DurableTerminalEventHeader,
	) (WorkerControlEvent, error)
}

type DurableTerminalEventLoaderService struct {
	Store DurableTerminalEventStore
}

func (service DurableTerminalEventLoaderService) Load(
	ctx context.Context,
	settlement TurnSettlement,
) (WorkerControlEvent, error) {
	if service.Store == nil {
		return WorkerControlEvent{},
			fmt.Errorf("durable terminal event store is required")
	}
	header, err := newDurableTerminalEventHeader(settlement)
	if err != nil {
		return WorkerControlEvent{}, err
	}
	return service.Store.LoadDurableTerminalEvent(ctx, header)
}

func newDurableTerminalEventHeader(
	settlement TurnSettlement,
) (DurableTerminalEventHeader, error) {
	if err := validateCanonical(
		"settlement_id",
		settlement.SettlementID,
	); err != nil {
		return DurableTerminalEventHeader{}, err
	}
	for name, value := range map[string]string{
		"worker_session_id":          settlement.WorkerSessionID,
		"worker_run_id":              settlement.WorkerRunID,
		"agent_session_id":           settlement.AgentSessionID,
		"agent_turn_id":              settlement.AgentTurnID,
		"runtime_instance_id":        settlement.RuntimeInstanceID,
		"terminal_message_id":        settlement.TerminalMessageID,
		"terminal_idempotency_key":   settlement.TerminalIdempotencyKey,
		"terminal_envelope_digest":   settlement.TerminalEnvelopeDigest,
		"terminal_source_state_hash": settlement.Source.StateDigest,
	} {
		if err := validateCanonical(name, value); err != nil {
			return DurableTerminalEventHeader{}, err
		}
	}
	if settlement.Revision == 0 ||
		settlement.RuntimeGeneration == 0 ||
		settlement.ChannelEpoch == 0 ||
		settlement.TerminalEventSequence == 0 {
		return DurableTerminalEventHeader{},
			ErrTurnSettlementConflict
	}
	if err := ValidateTurnSettlementSource(settlement.Source); err != nil {
		return DurableTerminalEventHeader{}, err
	}
	switch settlement.Source.Kind {
	case TurnSettlementSourceRuntimeEvent:
		if settlement.Source.Ref != settlement.TerminalMessageID ||
			settlement.Source.Digest !=
				settlement.TerminalEnvelopeDigest {
			return DurableTerminalEventHeader{},
				ErrTurnSettlementSourceConflict
		}
	case TurnSettlementSourceBackendDeadline:
		state, err := DecodeBackendDeadlineSourceState(settlement.Source)
		if err != nil ||
			state.Phase != BackendDeadlineSourceTerminalObserved ||
			state.RuntimeInstanceID != settlement.RuntimeInstanceID ||
			state.RuntimeGeneration != settlement.RuntimeGeneration ||
			state.ChannelEpoch != settlement.ChannelEpoch {
			return DurableTerminalEventHeader{},
				ErrTurnSettlementSourceConflict
		}
	default:
		return DurableTerminalEventHeader{},
			ErrTurnSettlementSourceConflict
	}
	return DurableTerminalEventHeader{
		SettlementID:               settlement.SettlementID,
		ExpectedSettlementRevision: settlement.Revision,
		WorkerSessionID:            settlement.WorkerSessionID,
		WorkerRunID:                settlement.WorkerRunID,
		AgentSessionID:             settlement.AgentSessionID,
		AgentTurnID:                settlement.AgentTurnID,
		RuntimeInstanceID:          settlement.RuntimeInstanceID,
		RuntimeGeneration:          settlement.RuntimeGeneration,
		ChannelEpoch:               settlement.ChannelEpoch,
		EventSequence:              settlement.TerminalEventSequence,
		MessageID:                  settlement.TerminalMessageID,
		EventIdempotencyKey:        settlement.TerminalIdempotencyKey,
		EnvelopeDigest:             settlement.TerminalEnvelopeDigest,
		SourceKind:                 settlement.Source.Kind,
		SourceStateDigest:          settlement.Source.StateDigest,
	}, nil
}
