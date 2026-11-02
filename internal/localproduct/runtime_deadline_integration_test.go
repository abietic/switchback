//go:build integration

package localproduct

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestLocalScheduledDeadlineRoutesToDurableSourceWithoutLiveRuntime(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	handler := localScheduledCommandHandler{
		deadline: backendDeadlineClaimHandler{
			Pool: pool,
			Source: workerapp.TurnSettlementSourceService{
				Store: workerpostgres.NewTurnSettlementRepository(pool),
				Now: func() time.Time {
					return deadlineAt.Add(time.Second)
				},
			},
		},
	}

	if err := handler.HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	settlement, err := workerpostgres.NewTurnSettlementRepository(pool).
		LoadTurnSettlement(
			ctx,
			"turn-settlement/turn-deadline-claim",
		)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline {
		t.Fatalf("source kind=%s", settlement.Source.Kind)
	}
}
