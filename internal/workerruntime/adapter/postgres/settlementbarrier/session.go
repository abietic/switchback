package settlementbarrier

import (
	"context"
	"fmt"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// EnsureSessionClear rejects new top-level execution while any prior Turn for
// the same WorkerSession still has durable settlement work to finish.
//
// Callers must first lock the WorkerSession row. This query deliberately does
// not lock settlement rows: settlement completion owns the opposite
// settlement -> Session lock order, and a READ COMMITTED snapshot is enough.
// If completion is waiting on the caller's Session lock, this transaction sees
// the still-pending row, rejects, and lets completion proceed without a
// Session/settlement deadlock.
func EnsureSessionClear(
	ctx context.Context,
	tx platformpostgres.Tx,
	workerSessionID string,
) error {
	if tx == nil || workerSessionID == "" {
		return fmt.Errorf("Turn settlement dispatch barrier requires a WorkerSession")
	}
	var pending bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM worker_turn_settlements
  WHERE worker_session_id=$1
    AND phase <> 'completed'
)`,
		workerSessionID,
	).Scan(&pending); err != nil {
		return fmt.Errorf("check pending Turn settlement before dispatch: %w", err)
	}
	if pending {
		return workerapp.ErrRuntimeControlPendingTurnSettlement
	}
	return nil
}
