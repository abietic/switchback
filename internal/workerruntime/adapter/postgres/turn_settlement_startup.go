package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

var _ application.PendingTurnSettlementStartupStore = (*TurnSettlementRepository)(nil)

func (repository *TurnSettlementRepository) ListPendingTurnSettlementsForStartup(
	ctx context.Context,
	record application.ListPendingTurnSettlementsForStartupRecord,
) ([]application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return nil, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"begin pending Turn settlement startup catalog: %w",
			err,
		)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	rows, err := tx.Query(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE tenant_id=$1
  AND phase <> 'completed'
ORDER BY accepted_at,settlement_id`,
		record.TenantID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list pending Turn settlement startup identities: %w",
			err,
		)
	}
	var settlementIDs []string
	for rows.Next() {
		var settlementID string
		if err := rows.Scan(&settlementID); err != nil {
			rows.Close()
			return nil, fmt.Errorf(
				"scan pending Turn settlement startup identity: %w",
				err,
			)
		}
		settlementIDs = append(settlementIDs, settlementID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf(
			"iterate pending Turn settlement startup identities: %w",
			err,
		)
	}
	rows.Close()

	settlements := make([]application.TurnSettlement, 0, len(settlementIDs))
	for _, settlementID := range settlementIDs {
		settlement, err := readTurnSettlement(
			ctx,
			tx,
			settlementID,
			false,
		)
		if err != nil {
			return nil, err
		}
		if settlement.TenantID != record.TenantID ||
			settlement.Phase == application.TurnSettlementCompleted {
			return nil, application.ErrTurnSettlementConflict
		}
		settlements = append(settlements, settlement)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf(
			"commit pending Turn settlement startup catalog: %w",
			err,
		)
	}
	return settlements, nil
}
