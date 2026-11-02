package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func (repository *TurnSettlementRepository) RenewTurnSettlementLease(
	ctx context.Context,
	record application.RenewTurnSettlementLeaseRecord,
) (application.TurnSettlement, error) {
	if repository == nil || repository.pool == nil {
		return application.TurnSettlement{}, fmt.Errorf(
			"Turn settlement PostgreSQL pool is required",
		)
	}
	var result application.TurnSettlement
	err := platformpostgres.InTransaction(
		ctx,
		repository.pool,
		func(tx platformpostgres.Tx) error {
			current, err := readTurnSettlement(
				ctx,
				tx,
				record.Command.SettlementID,
				true,
			)
			if err != nil {
				return err
			}
			if current.Revision != record.Command.ExpectedRevision {
				return application.ErrTurnSettlementRevisionConflict
			}
			if current.Phase == application.TurnSettlementCompleted {
				return application.ErrTurnSettlementPhaseConflict
			}
			if current.LeaseOwnerID != record.Command.LeaseOwnerID ||
				current.LeaseFence != record.Command.LeaseFence ||
				!current.LeaseExpiresAt.After(record.RenewedAt) {
				return application.ErrTurnSettlementLeaseStale
			}
			leaseExpiresAt := record.LeaseExpiresAt
			if current.LeaseExpiresAt.After(leaseExpiresAt) {
				leaseExpiresAt = current.LeaseExpiresAt
			}
			err = tx.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET lease_expires_at=$2
WHERE settlement_id=$1
  AND revision=$3
  AND phase <> 'completed'
  AND lease_owner_id=$4
  AND lease_fence=$5
  AND lease_expires_at > $6
RETURNING lease_expires_at`,
				current.SettlementID,
				leaseExpiresAt,
				current.Revision,
				current.LeaseOwnerID,
				current.LeaseFence,
				record.RenewedAt,
			).Scan(&current.LeaseExpiresAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrTurnSettlementLeaseStale
			}
			if err != nil {
				return fmt.Errorf(
					"renew Turn settlement lease: %w",
					err,
				)
			}
			current.LeaseExpiresAt = current.LeaseExpiresAt.UTC()
			result = current
			return nil
		},
	)
	return result, err
}
