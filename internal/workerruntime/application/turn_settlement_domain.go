package application

import (
	"context"
	"fmt"
	"time"
)

type TurnSettlementDomainProof struct {
	SessionRevision uint64 `json:"session_revision"`
	RunRevision     uint64 `json:"run_revision"`
	TurnRevision    uint64 `json:"turn_revision"`
	RuntimeRevision uint64 `json:"runtime_revision"`
}

type CommitTurnSettlementDomainCommand struct {
	SettlementID     string
	ExpectedRevision uint64
	LeaseOwnerID     string
	LeaseFence       uint64
	Resident         CompleteResidentStageCommand
}

type CommitTurnSettlementDomainRecord struct {
	Command     CommitTurnSettlementDomainCommand
	CommittedAt time.Time
}

type CommitTurnSettlementDomainResult struct {
	Settlement TurnSettlement
	Resident   ResidentRuntimeState
	Proof      TurnSettlementDomainProof
}

type TurnSettlementDomainStore interface {
	CommitTurnSettlementDomain(
		context.Context,
		CommitTurnSettlementDomainRecord,
	) (CommitTurnSettlementDomainResult, error)
}

type TurnSettlementDomainService struct {
	Store TurnSettlementDomainStore
	Now   func() time.Time
}

func (service TurnSettlementDomainService) Commit(
	ctx context.Context,
	command CommitTurnSettlementDomainCommand,
) (CommitTurnSettlementDomainResult, error) {
	if service.Store == nil || service.Now == nil {
		return CommitTurnSettlementDomainResult{}, fmt.Errorf(
			"Turn settlement domain dependencies are required",
		)
	}
	if err := validateCanonical(
		"settlement_id",
		command.SettlementID,
	); err != nil {
		return CommitTurnSettlementDomainResult{}, err
	}
	if err := validateCanonical(
		"lease_owner_id",
		command.LeaseOwnerID,
	); err != nil {
		return CommitTurnSettlementDomainResult{}, err
	}
	if command.ExpectedRevision == 0 || command.LeaseFence == 0 {
		return CommitTurnSettlementDomainResult{}, fmt.Errorf(
			"Turn settlement revision and lease fence must be positive",
		)
	}
	if err := validateCompleteResidentStageCommand(
		command.Resident,
	); err != nil {
		return CommitTurnSettlementDomainResult{}, err
	}
	expectedSettlementID, err := TurnSettlementID(
		command.Resident.AgentTurnID,
	)
	if err != nil {
		return CommitTurnSettlementDomainResult{}, err
	}
	if command.SettlementID != expectedSettlementID {
		return CommitTurnSettlementDomainResult{},
			ErrTurnSettlementConflict
	}
	now := normalizePostgresTimestamp(service.Now())
	if now.IsZero() {
		return CommitTurnSettlementDomainResult{}, fmt.Errorf(
			"Turn settlement domain commit time is required",
		)
	}
	return service.Store.CommitTurnSettlementDomain(
		ctx,
		CommitTurnSettlementDomainRecord{
			Command:     command,
			CommittedAt: now,
		},
	)
}
