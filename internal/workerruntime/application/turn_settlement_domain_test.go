package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestTurnSettlementDomainServiceValidatesAtomicDomainCommit(t *testing.T) {
	now := time.Date(
		2026,
		7,
		26,
		23,
		0,
		0,
		123456789,
		time.FixedZone("test", 8*60*60),
	)
	store := &recordingTurnSettlementDomainStore{}
	service := workerapp.TurnSettlementDomainService{
		Store: store,
		Now:   func() time.Time { return now },
	}
	command := validTurnSettlementDomainCommand()
	result, err := service.Commit(
		context.Background(),
		command,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantNow := now.UTC().Truncate(time.Microsecond)
	if !store.record.CommittedAt.Equal(wantNow) ||
		result.Proof.SessionRevision != 8 ||
		result.Settlement.Phase !=
			workerapp.TurnSettlementDomainCommitted {
		t.Fatalf(
			"record=%+v result=%+v",
			store.record,
			result,
		)
	}

	command.SettlementID = "turn-settlement/other-turn"
	if _, err := service.Commit(
		context.Background(),
		command,
	); !errors.Is(err, workerapp.ErrTurnSettlementConflict) {
		t.Fatalf("mismatched settlement identity error=%v", err)
	}
}

func validTurnSettlementDomainCommand() workerapp.CommitTurnSettlementDomainCommand {
	return workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     "turn-settlement/turn-1",
		ExpectedRevision: 4,
		LeaseOwnerID:     "settler-1",
		LeaseFence:       2,
		Resident: workerapp.CompleteResidentStageCommand{
			TenantID:                "tenant-1",
			WorkerSessionID:         "session-1",
			WorkerRunID:             "run-1",
			AgentTurnID:             "turn-1",
			RuntimeInstanceID:       "runtime-1",
			RuntimeGeneration:       1,
			ExpectedSessionRevision: 7,
			ExpectedRunRevision:     3,
			ExpectedTurnRevision:    2,
			ExpectedRuntimeRevision: 5,
			SessionTarget:           workerdomain.SessionInteractive,
			RunTarget:               workerdomain.RunCompleted,
			TurnTarget:              workerdomain.TurnCompleted,
			TerminalReason:          "end_turn",
		},
	}
}

type recordingTurnSettlementDomainStore struct {
	record workerapp.CommitTurnSettlementDomainRecord
}

func (store *recordingTurnSettlementDomainStore) CommitTurnSettlementDomain(
	_ context.Context,
	record workerapp.CommitTurnSettlementDomainRecord,
) (workerapp.CommitTurnSettlementDomainResult, error) {
	store.record = record
	return workerapp.CommitTurnSettlementDomainResult{
		Settlement: workerapp.TurnSettlement{
			SettlementID: record.Command.SettlementID,
			Phase:        workerapp.TurnSettlementDomainCommitted,
		},
		Proof: workerapp.TurnSettlementDomainProof{
			SessionRevision: 8,
			RunRevision:     4,
			TurnRevision:    3,
			RuntimeRevision: 5,
		},
	}, nil
}
