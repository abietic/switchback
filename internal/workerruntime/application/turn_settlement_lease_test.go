package application_test

import (
	"context"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestTurnSettlementLeaseServiceRenewsExactAuthorityWithoutChangingFence(
	t *testing.T,
) {
	now := time.Date(2026, 7, 27, 3, 4, 5, 123456789, time.UTC)
	store := &recordingTurnSettlementLeaseStore{
		state: workerapp.TurnSettlement{
			SettlementID:   "turn-settlement/turn-1",
			Revision:       7,
			LeaseOwnerID:   "settler-1",
			LeaseFence:     3,
			LeaseExpiresAt: now.Add(5 * time.Second),
		},
	}
	service := workerapp.TurnSettlementLeaseService{
		Store: store,
		Now:   func() time.Time { return now },
	}

	renewed, err := service.Renew(
		context.Background(),
		workerapp.RenewTurnSettlementLeaseCommand{
			SettlementID:     "turn-settlement/turn-1",
			ExpectedRevision: 7,
			LeaseOwnerID:     "settler-1",
			LeaseFence:       3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantRenewedAt := now.UTC().Truncate(time.Microsecond)
	if store.record.RenewedAt != wantRenewedAt {
		t.Fatalf("renewed_at=%s want=%s", store.record.RenewedAt, wantRenewedAt)
	}
	wantExpiry := wantRenewedAt.Add(workerapp.TurnSettlementLeaseDuration)
	if store.record.LeaseExpiresAt != wantExpiry {
		t.Fatalf(
			"lease_expires_at=%s want=%s",
			store.record.LeaseExpiresAt,
			wantExpiry,
		)
	}
	if renewed.Revision != 7 ||
		renewed.LeaseFence != 3 ||
		renewed.LeaseOwnerID != "settler-1" {
		t.Fatalf("renewed authority changed: %+v", renewed)
	}
}

type recordingTurnSettlementLeaseStore struct {
	state  workerapp.TurnSettlement
	record workerapp.RenewTurnSettlementLeaseRecord
}

func (store *recordingTurnSettlementLeaseStore) RenewTurnSettlementLease(
	_ context.Context,
	record workerapp.RenewTurnSettlementLeaseRecord,
) (workerapp.TurnSettlement, error) {
	store.record = record
	store.state.LeaseExpiresAt = record.LeaseExpiresAt
	return store.state, nil
}
