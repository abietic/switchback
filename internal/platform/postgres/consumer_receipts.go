package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

var (
	ErrConsumerSequenceGap    = errors.New("consumer stream sequence gap")
	ErrConsumerStreamConflict = errors.New("consumer stream integrity conflict")
)

type ConsumerMutation func(context.Context, Tx, contractsv1.IntegrationEvent) error

type ConsumerReceipts struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

func (receipts ConsumerReceipts) Apply(
	ctx context.Context,
	consumerName string,
	event contractsv1.IntegrationEvent,
	mutation ConsumerMutation,
) (bool, error) {
	if receipts.Pool == nil || receipts.Now == nil || consumerName == "" || mutation == nil {
		return false, fmt.Errorf("consumer receipt dependencies are required")
	}
	if err := event.Validate(); err != nil {
		return false, err
	}
	digest, err := IntegrationEventDigest(event)
	if err != nil {
		return false, err
	}
	applied := false
	err = InTransaction(ctx, receipts.Pool, func(tx Tx) error {
		lockKey := fmt.Sprintf("%d:%s%s", len(consumerName), consumerName, event.StreamID)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return fmt.Errorf("lock consumer stream: %w", err)
		}
		duplicate, err := verifyConsumerPosition(ctx, tx, consumerName, event, digest)
		if err != nil {
			return err
		}
		if duplicate {
			return nil
		}
		if err := mutation(ctx, tx, event); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO consumer_receipts (consumer_name,event_id,stream_id,sequence,event_digest,applied_at)
VALUES ($1,$2,$3,$4,$5,$6)`, consumerName, event.EventID, event.StreamID, event.Sequence, digest, receipts.Now().UTC()); err != nil {
			return fmt.Errorf("insert consumer receipt: %w", err)
		}
		applied = true
		return nil
	})
	return applied, err
}

func verifyConsumerPosition(
	ctx context.Context,
	tx Tx,
	consumerName string,
	event contractsv1.IntegrationEvent,
	digest string,
) (bool, error) {
	var storedStreamID, storedDigest string
	var storedSequence uint64
	err := tx.QueryRow(ctx, `
SELECT stream_id,sequence,event_digest FROM consumer_receipts
WHERE consumer_name=$1 AND event_id=$2`, consumerName, event.EventID).Scan(
		&storedStreamID, &storedSequence, &storedDigest)
	if err == nil {
		if storedStreamID == event.StreamID && storedSequence == event.Sequence && storedDigest == digest {
			return true, nil
		}
		return false, fmt.Errorf("%w: event id %s was reused", ErrConsumerStreamConflict, event.EventID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("read consumer receipt: %w", err)
	}

	var lastSequence uint64
	err = tx.QueryRow(ctx, `
SELECT sequence FROM consumer_receipts
WHERE consumer_name=$1 AND stream_id=$2
ORDER BY sequence DESC LIMIT 1`, consumerName, event.StreamID).Scan(&lastSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		lastSequence = 0
	} else if err != nil {
		return false, fmt.Errorf("read consumer stream position: %w", err)
	}
	want := lastSequence + 1
	if event.Sequence > want {
		return false, fmt.Errorf("%w: stream=%s got=%d want=%d", ErrConsumerSequenceGap, event.StreamID, event.Sequence, want)
	}
	if event.Sequence < want {
		return false, fmt.Errorf("%w: stream=%s sequence=%d already occupied", ErrConsumerStreamConflict, event.StreamID, event.Sequence)
	}
	return false, nil
}

// IntegrationEventDigest returns the canonical content identity used by
// durable consumers to distinguish semantic duplicates from identity reuse.
func IntegrationEventDigest(event contractsv1.IntegrationEvent) (string, error) {
	var normalizedPayload any
	if err := json.Unmarshal(event.Payload, &normalizedPayload); err != nil {
		return "", fmt.Errorf("normalize IntegrationEvent payload: %w", err)
	}
	event.Payload, _ = json.Marshal(normalizedPayload)
	data, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal IntegrationEvent digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
