package postgres

import (
	"context"
	"encoding/json"
	"errors"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InCapacityTransaction owns the mandatory pool -> business lock prefix. Read
// the deployment nonce BEFORE acquiring a transaction connection; otherwise a
// one-connection pool deadlocks during initialization. The callback receives
// only the real transaction and the private handle bound to that transaction.
func InCapacityTransaction(ctx context.Context, pool *pgxpool.Pool, action func(pgx.Tx, *CapacityTx) error) error {
	store, err := NewCapacityStore(ctx, pool)
	if err != nil {
		return err
	}
	return platformpostgres.InTransaction(ctx, pool, func(tx pgx.Tx) error {
		capacity, err := store.LockCapacityPool(ctx, tx)
		if err != nil {
			return err
		}
		return action(tx, capacity)
	})
}

// BoundTaskTarget recovers original authority for response-loss replay. Callers
// must compare it to the locked business graph before reserving or consuming.
// Caller text and legacy permits cannot manufacture this ledger binding.
func (h *CapacityTx) BoundTaskTarget(ctx context.Context, token, permitID string) (app.CapacityTarget, error) {
	var target app.CapacityTarget
	if err := h.valid(ctx); err != nil {
		return target, err
	}
	var raw []byte
	var digest string
	err := h.tx.QueryRow(ctx, `SELECT target_bytes,target_digest FROM runtime_capacity_executions WHERE pool_id='local' AND token::text=$1 AND permit_id=$2`, token, permitID).Scan(&raw, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, app.CapacityBindingMismatch
	}
	if err != nil {
		return target, err
	}
	if json.Unmarshal(raw, &target) != nil || !validTarget(target) {
		return target, app.CapacityBindingMismatch
	}
	canonical, _ := json.Marshal(target)
	if capacitysnapshot.Digest(canonical) != digest {
		return target, app.CapacityBindingMismatch
	}
	if _, err = capacityProfile(target.Profile); err != nil {
		return target, err
	}
	return target, nil
}
