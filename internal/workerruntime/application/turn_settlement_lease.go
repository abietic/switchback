package application

import (
	"context"
	"fmt"
	"time"
)

// RenewTurnSettlementLeaseCommand identifies the exact settlement ownership
// generation that is allowed to keep a long-running reconciliation alive.
// Renewal deliberately does not mint a new fence or business revision.
type RenewTurnSettlementLeaseCommand struct {
	SettlementID     string
	ExpectedRevision uint64
	LeaseOwnerID     string
	LeaseFence       uint64
}

type RenewTurnSettlementLeaseRecord struct {
	Command        RenewTurnSettlementLeaseCommand
	RenewedAt      time.Time
	LeaseExpiresAt time.Time
}

type TurnSettlementLeaseStore interface {
	RenewTurnSettlementLease(
		context.Context,
		RenewTurnSettlementLeaseRecord,
	) (TurnSettlement, error)
}

type TurnSettlementLeaseService struct {
	Store TurnSettlementLeaseStore
	Now   func() time.Time
}

func (service TurnSettlementLeaseService) Renew(
	ctx context.Context,
	command RenewTurnSettlementLeaseCommand,
) (TurnSettlement, error) {
	if service.Store == nil || service.Now == nil {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement lease dependencies are required",
		)
	}
	for name, value := range map[string]string{
		"settlement_id":  command.SettlementID,
		"lease_owner_id": command.LeaseOwnerID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return TurnSettlement{}, err
		}
	}
	if command.ExpectedRevision == 0 || command.LeaseFence == 0 {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement revision and lease fence must be positive",
		)
	}
	now := normalizePostgresTimestamp(service.Now())
	if now.IsZero() {
		return TurnSettlement{}, fmt.Errorf(
			"Turn settlement lease renewal time is required",
		)
	}
	return service.Store.RenewTurnSettlementLease(
		ctx,
		RenewTurnSettlementLeaseRecord{
			Command:        command,
			RenewedAt:      now,
			LeaseExpiresAt: now.Add(TurnSettlementLeaseDuration),
		},
	)
}
