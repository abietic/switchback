package application

import (
	"context"
	"time"
)

// PendingTurnSettlementStartupStore is the read-only startup catalog for one
// tenant. Listing does not acquire or renew a settlement lease: the caller
// must reconcile each returned durable identity through the normal owner.
type PendingTurnSettlementStartupStore interface {
	ListPendingTurnSettlementsForStartup(
		context.Context,
		ListPendingTurnSettlementsForStartupRecord,
	) ([]TurnSettlement, error)
}

type ListPendingTurnSettlementsForStartupRecord struct {
	TenantID string
}

func (record ListPendingTurnSettlementsForStartupRecord) Validate() error {
	if validateCanonical("tenant_id", record.TenantID) != nil {
		return ErrTurnSettlementConflict
	}
	return nil
}

type TurnSettlementStartupAction string

const (
	// TurnSettlementStartupReconcileSource resumes the source-owned state
	// machine. It does not imply that startup already owns the durable lease.
	TurnSettlementStartupReconcileSource TurnSettlementStartupAction = "reconcile_source"
	// TurnSettlementStartupWaitForceAfter preserves the exact reconnect window
	// after a durable interrupt and before the frozen force boundary.
	TurnSettlementStartupWaitForceAfter TurnSettlementStartupAction = "wait_force_after"
	// TurnSettlementStartupReconcileTerminal resumes the general terminal
	// settlement pipeline after source terminality is already durable.
	TurnSettlementStartupReconcileTerminal TurnSettlementStartupAction = "reconcile_terminal"
)

// TurnSettlementStartupLease is a read-only snapshot. OwnerID lets a startup
// coordinator distinguish its own lease from a live foreign lease without
// this classifier stealing, renewing, or otherwise mutating that authority.
type TurnSettlementStartupLease struct {
	OwnerID   string
	Fence     uint64
	ExpiresAt time.Time
	Active    bool
}

type TurnSettlementStartupDisposition struct {
	SettlementID        string
	Revision            uint64
	Phase               TurnSettlementPhase
	Action              TurnSettlementStartupAction
	AllowExactReconnect bool
	NextAttemptAt       time.Time
	Lease               TurnSettlementStartupLease
}

// ClassifyTurnSettlementForStartup is a pure decision over one typed durable
// settlement. It deliberately does not claim the settlement lease or inspect
// mutable Runtime state outside the frozen source proof.
func ClassifyTurnSettlementForStartup(
	settlement TurnSettlement,
	now time.Time,
) (TurnSettlementStartupDisposition, error) {
	now = normalizePostgresTimestamp(now)
	if now.IsZero() ||
		validateCanonical("settlement_id", settlement.SettlementID) != nil ||
		settlement.Revision == 0 {
		return TurnSettlementStartupDisposition{},
			ErrTurnSettlementConflict
	}
	if settlement.Phase == TurnSettlementCompleted {
		return TurnSettlementStartupDisposition{},
			ErrTurnSettlementPhaseConflict
	}
	switch settlement.Phase {
	case TurnSettlementAccepted,
		TurnSettlementEvidencePrepared,
		TurnSettlementDomainCommitted,
		TurnSettlementEvidencePublished,
		TurnSettlementTaskSettled,
		TurnSettlementTerminalEventApplied,
		TurnSettlementContinuationSettled:
	default:
		return TurnSettlementStartupDisposition{},
			ErrTurnSettlementConflict
	}
	if (settlement.LeaseOwnerID == "" &&
		!settlement.LeaseExpiresAt.IsZero()) ||
		(settlement.LeaseOwnerID != "" &&
			(settlement.LeaseFence == 0 ||
				settlement.LeaseExpiresAt.IsZero())) {
		return TurnSettlementStartupDisposition{},
			ErrTurnSettlementConflict
	}
	if err := ValidateTurnSettlementSource(settlement.Source); err != nil {
		return TurnSettlementStartupDisposition{},
			ErrTurnSettlementSourceConflict
	}

	disposition := TurnSettlementStartupDisposition{
		SettlementID: settlement.SettlementID,
		Revision:     settlement.Revision,
		Phase:        settlement.Phase,
		Lease: TurnSettlementStartupLease{
			OwnerID:   settlement.LeaseOwnerID,
			Fence:     settlement.LeaseFence,
			ExpiresAt: settlement.LeaseExpiresAt,
			Active: settlement.LeaseOwnerID != "" &&
				settlement.LeaseExpiresAt.After(now),
		},
	}
	switch settlement.Source.Kind {
	case TurnSettlementSourceRuntimeEvent:
		disposition.Action = TurnSettlementStartupReconcileTerminal
		return disposition, nil
	case TurnSettlementSourceBackendDeadline:
		state, err := DecodeBackendDeadlineSourceState(settlement.Source)
		if err != nil {
			return TurnSettlementStartupDisposition{},
				ErrTurnSettlementSourceConflict
		}
		switch state.Phase {
		case BackendDeadlineSourceClaimed,
			BackendDeadlineSourceForceStopIntent,
			BackendDeadlineSourceWriteFenced,
			BackendDeadlineSourceCleanupConfirmed:
			disposition.Action = TurnSettlementStartupReconcileSource
		case BackendDeadlineSourceInterruptQueued:
			if now.Before(state.ForceAfter) {
				disposition.Action =
					TurnSettlementStartupWaitForceAfter
				disposition.AllowExactReconnect = true
				disposition.NextAttemptAt = state.ForceAfter
			} else {
				disposition.Action =
					TurnSettlementStartupReconcileSource
			}
		case BackendDeadlineSourceTerminalObserved:
			disposition.Action =
				TurnSettlementStartupReconcileTerminal
		default:
			return TurnSettlementStartupDisposition{},
				ErrTurnSettlementSourceConflict
		}
		return disposition, nil
	default:
		return TurnSettlementStartupDisposition{},
			ErrTurnSettlementSourceConflict
	}
}
