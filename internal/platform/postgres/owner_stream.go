package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type OwnerStream struct {
	StreamID      string
	OwnerContext  string
	AggregateType string
	AggregateID   string
}

type OwnerStreamRepository struct{}

func (OwnerStreamRepository) Next(ctx context.Context, tx Tx, stream OwnerStream) (uint64, error) {
	if stream.StreamID == "" || stream.OwnerContext == "" || stream.AggregateType == "" || stream.AggregateID == "" {
		return 0, fmt.Errorf("owner stream identity is incomplete")
	}
	_, err := tx.Exec(ctx, `
INSERT INTO owner_stream_positions
  (stream_id, owner_context, aggregate_type, aggregate_id, next_sequence, revision, updated_at)
VALUES ($1, $2, $3, $4, 1, 1, statement_timestamp())
ON CONFLICT DO NOTHING`, stream.StreamID, stream.OwnerContext, stream.AggregateType, stream.AggregateID)
	if err != nil {
		return 0, fmt.Errorf("ensure owner stream: %w", err)
	}
	var sequence uint64
	err = tx.QueryRow(ctx, `
UPDATE owner_stream_positions
SET next_sequence = next_sequence + 1,
    revision = revision + 1,
    updated_at = statement_timestamp()
WHERE stream_id = $1
  AND owner_context = $2
  AND aggregate_type = $3
  AND aggregate_id = $4
RETURNING next_sequence - 1`, stream.StreamID, stream.OwnerContext, stream.AggregateType, stream.AggregateID).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrStreamOwnerConflict
	}
	if err != nil {
		return 0, fmt.Errorf("allocate owner stream sequence: %w", err)
	}
	return sequence, nil
}
