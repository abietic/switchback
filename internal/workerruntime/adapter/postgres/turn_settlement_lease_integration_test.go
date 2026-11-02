//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestTurnSettlementLeaseRenewalFencesReclaimWithoutChangingBusinessRevision(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 3, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "renew-terminal",
			IdempotencyKey: "renew-terminal",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"renew-terminal",
				"renew-terminal",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}

	currentTime := now.Add(7 * time.Minute)
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	settlements := workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return currentTime },
	}
	leases := workerapp.TurnSettlementLeaseService{
		Store: repository,
		Now:   func() time.Time { return currentTime },
	}
	settlementID, err := workerapp.TurnSettlementID(started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     settlementID,
			ExpectedRevision: 1,
			OwnerID:          "settler-one",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry := claimed.LeaseExpiresAt
	currentTime = originalExpiry.Add(-time.Second)
	renewed, err := leases.Renew(
		ctx,
		workerapp.RenewTurnSettlementLeaseCommand{
			SettlementID:     settlementID,
			ExpectedRevision: claimed.Revision,
			LeaseOwnerID:     claimed.LeaseOwnerID,
			LeaseFence:       claimed.LeaseFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Revision != claimed.Revision ||
		renewed.LeaseFence != claimed.LeaseFence {
		t.Fatalf(
			"renew changed business authority: claimed=%+v renewed=%+v",
			claimed,
			renewed,
		)
	}
	if !renewed.LeaseExpiresAt.After(originalExpiry) {
		t.Fatalf(
			"renewed expiry=%s original=%s",
			renewed.LeaseExpiresAt,
			originalExpiry,
		)
	}

	currentTime = originalExpiry.Add(time.Millisecond)
	if _, err := settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     settlementID,
			ExpectedRevision: renewed.Revision,
			OwnerID:          "settler-two",
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementLeaseHeld) {
		t.Fatalf("reclaim inside renewed lease error=%v", err)
	}

	currentTime = renewed.LeaseExpiresAt.Add(time.Millisecond)
	if _, err := leases.Renew(
		ctx,
		workerapp.RenewTurnSettlementLeaseCommand{
			SettlementID:     settlementID,
			ExpectedRevision: renewed.Revision,
			LeaseOwnerID:     renewed.LeaseOwnerID,
			LeaseFence:       renewed.LeaseFence,
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
		t.Fatalf("expired owner resurrected lease, error=%v", err)
	}
	reclaimed, err := settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     settlementID,
			ExpectedRevision: renewed.Revision,
			OwnerID:          "settler-two",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.LeaseFence != renewed.LeaseFence+1 {
		t.Fatalf("reclaimed fence=%d", reclaimed.LeaseFence)
	}
	if _, err := leases.Renew(
		ctx,
		workerapp.RenewTurnSettlementLeaseCommand{
			SettlementID:     settlementID,
			ExpectedRevision: renewed.Revision,
			LeaseOwnerID:     renewed.LeaseOwnerID,
			LeaseFence:       renewed.LeaseFence,
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementRevisionConflict) {
		t.Fatalf("stale owner renewal error=%v", err)
	}
}
