//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestPendingTurnSettlementStartupCatalogIsTenantScopedStableAndReadOnly(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	started := startActiveSettlementFixtures(t, pool, base, 4)
	acceptedAt := base.Add(25 * time.Hour)
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	sourceService := workerapp.TurnSettlementSourceService{
		Store: repository,
		Now:   func() time.Time { return acceptedAt },
	}
	settlementIDs := make([]string, 0, len(started))
	for _, fixture := range started {
		deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
			t,
			pool,
			fixture.AgentTurnID,
			fixture.RuntimeInstanceID,
		)
		settlement, err := sourceService.ClaimBackendDeadline(
			ctx,
			backendDeadlineClaimCommand(fixture, deadlineAt, sourceRef),
		)
		if err != nil {
			t.Fatal(err)
		}
		settlementIDs = append(settlementIDs, settlement.SettlementID)
	}
	sort.Strings(settlementIDs)

	completedAt := acceptedAt.Add(time.Minute)
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET phase='completed',
    terminal_event_disposition='ignore',
    terminal_apply_outcome_digest=$2,
    terminal_event_applied_at=$3,
    completed_at=$3,
    updated_at=$3
WHERE settlement_id=$1`,
		settlementIDs[0],
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		completedAt,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET tenant_id='tenant-foreign'
WHERE settlement_id=$1`,
		settlementIDs[1],
	); err != nil {
		t.Fatal(err)
	}

	leaseNow := acceptedAt.Add(2 * time.Minute)
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return leaseNow },
	}).Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID:     settlementIDs[2],
		ExpectedRevision: 1,
		OwnerID:          "foreign-startup-owner",
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted := postgrestest.Restart(t, pool)
	catalog := workerpostgres.NewTurnSettlementRepository(restarted)
	listed, err := catalog.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{
			TenantID: "tenant-local",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := settlementIDs[2:]
	if len(listed) != len(wantIDs) {
		t.Fatalf("listed=%v want IDs=%v", listed, wantIDs)
	}
	for index, settlement := range listed {
		if settlement.SettlementID != wantIDs[index] ||
			!settlement.AcceptedAt.Equal(acceptedAt) ||
			settlement.Phase == workerapp.TurnSettlementCompleted {
			t.Fatalf(
				"listed[%d]=%+v want ID=%s accepted_at=%s",
				index,
				settlement,
				wantIDs[index],
				acceptedAt,
			)
		}
		if _, err := workerapp.DecodeBackendDeadlineSourceState(
			settlement.Source,
		); err != nil {
			t.Fatalf("listed[%d] source was not strictly decoded: %v", index, err)
		}
	}
	if listed[0].LeaseOwnerID != leased.LeaseOwnerID ||
		listed[0].LeaseFence != leased.LeaseFence ||
		!listed[0].LeaseExpiresAt.Equal(leased.LeaseExpiresAt) ||
		listed[0].Revision != leased.Revision {
		t.Fatalf("catalog changed or hid foreign lease: got=%+v want=%+v", listed[0], leased)
	}
	disposition, err := workerapp.ClassifyTurnSettlementForStartup(
		listed[0],
		leaseNow.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if disposition.Action != workerapp.TurnSettlementStartupReconcileSource ||
		disposition.Lease.OwnerID != leased.LeaseOwnerID ||
		disposition.Lease.Fence != leased.LeaseFence ||
		!disposition.Lease.Active {
		t.Fatalf("disposition=%+v", disposition)
	}

	foreign, err := catalog.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{
			TenantID: "tenant-foreign",
		},
	)
	if err != nil || len(foreign) != 1 ||
		foreign[0].SettlementID != settlementIDs[1] {
		t.Fatalf("foreign settlements=%+v error=%v", foreign, err)
	}
	if _, err := catalog.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{},
	); !errors.Is(err, workerapp.ErrTurnSettlementConflict) {
		t.Fatalf("empty tenant error=%v", err)
	}
}

func TestPendingTurnSettlementStartupCatalogFailsClosedOnInvalidTypedSource(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	acceptedAt := deadlineAt.Add(time.Second)
	settlement, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return acceptedAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidState, invalidDigest, err :=
		workerapp.CanonicalizeTurnSettlementSourceState(
			[]byte(
				`{"deadline_at":"2026-07-27T03:00:00Z","force_after":"2026-07-27T03:00:30Z","phase":"future_phase","runtime_generation":1,"runtime_instance_id":"runtime-invalid","channel_epoch":1,"schema_version":2}`,
			),
		)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET tenant_id='tenant-foreign',
    source_state=$2,
    source_state_digest=$3
WHERE settlement_id=$1`,
		settlement.SettlementID,
		invalidState,
		invalidDigest,
	); err != nil {
		t.Fatal(err)
	}

	catalog := workerpostgres.NewTurnSettlementRepository(pool)
	local, err := catalog.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{
			TenantID: "tenant-local",
		},
	)
	if err != nil || len(local) != 0 {
		t.Fatalf("local catalog crossed tenant boundary: %+v error=%v", local, err)
	}
	if _, err := catalog.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{
			TenantID: "tenant-foreign",
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("invalid typed source error=%v", err)
	}
}
