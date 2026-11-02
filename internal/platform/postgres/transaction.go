// Package postgres contains shared PostgreSQL transaction mechanics. Business
// SQL remains owned by each bounded context adapter.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound            = errors.New("aggregate not found")
	ErrRevisionConflict    = errors.New("aggregate revision conflict")
	ErrStreamOwnerConflict = errors.New("owner stream identity conflict")
)

type Tx = pgx.Tx

type Transactor interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type Execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func InTransaction(ctx context.Context, database Transactor, action func(Tx) error) error {
	tx, err := database.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if err := action(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
