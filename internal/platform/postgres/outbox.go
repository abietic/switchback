package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

var ErrOutboxLeaseLost = errors.New("Outbox event lease is no longer owned")

type EventPublisher interface {
	SupportedIntegrationEventSchemaVersions() []string
	Publish(context.Context, contractsv1.IntegrationEvent) error
}

type OutboxDispatcher struct {
	Pool          *pgxpool.Pool
	Publisher     EventPublisher
	LeaseOwner    string
	LeaseDuration time.Duration
	BatchSize     int
	Now           func() time.Time
}

func (dispatcher OutboxDispatcher) RunOnce(ctx context.Context) (int, error) {
	if dispatcher.Pool == nil || dispatcher.Publisher == nil || dispatcher.Now == nil ||
		dispatcher.LeaseOwner == "" || dispatcher.LeaseDuration <= 0 || dispatcher.BatchSize <= 0 {
		return 0, fmt.Errorf("Outbox dispatcher dependencies and lease configuration are required")
	}
	published := 0
	var publishErrors []error
	remaining := dispatcher.BatchSize
	for remaining > 0 {
		events, err := dispatcher.claim(ctx, dispatcher.Now().UTC(), remaining)
		if err != nil {
			return published, errors.Join(append(publishErrors, err)...)
		}
		if len(events) == 0 {
			break
		}
		remaining -= len(events)
		for _, event := range events {
			if err := requirePublisherSchemaSupport(dispatcher.Publisher, event.SchemaVersion); err != nil {
				if releaseErr := dispatcher.release(ctx, event.EventID, errorCode(err)); releaseErr != nil {
					publishErrors = append(publishErrors, errors.Join(err, releaseErr))
				} else {
					publishErrors = append(publishErrors, fmt.Errorf("publish Outbox event %s: %w", event.EventID, err))
				}
				continue
			}
			if err := dispatcher.Publisher.Publish(ctx, event); err != nil {
				if releaseErr := dispatcher.release(ctx, event.EventID, errorCode(err)); releaseErr != nil {
					publishErrors = append(publishErrors, errors.Join(err, releaseErr))
				} else {
					publishErrors = append(publishErrors, fmt.Errorf("publish Outbox event %s: %w", event.EventID, err))
				}
				continue
			}
			if err := dispatcher.markPublished(ctx, event.EventID, dispatcher.Now().UTC()); err != nil {
				publishErrors = append(publishErrors, err)
				continue
			}
			published++
		}
		if len(publishErrors) > 0 {
			break
		}
	}
	return published, errors.Join(publishErrors...)
}

func requirePublisherSchemaSupport(publisher EventPublisher, schemaVersion string) error {
	for _, supported := range publisher.SupportedIntegrationEventSchemaVersions() {
		if supported == schemaVersion {
			return nil
		}
	}
	return fmt.Errorf("consumer does not support IntegrationEvent schema %q", schemaVersion)
}

func (dispatcher OutboxDispatcher) claim(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]contractsv1.IntegrationEvent, error) {
	var events []contractsv1.IntegrationEvent
	err := InTransaction(ctx, dispatcher.Pool, func(tx Tx) error {
		rows, err := tx.Query(ctx, `
WITH claimable AS (
  SELECT event.event_id
  FROM outbox_events event
  WHERE event.published_at IS NULL
    AND (event.lease_owner IS NULL OR event.lease_expires_at <= $1)
    AND NOT EXISTS (
      SELECT 1
      FROM outbox_events earlier
      WHERE earlier.stream_id=event.stream_id
        AND earlier.sequence<event.sequence
        AND earlier.published_at IS NULL
    )
  ORDER BY event.occurred_at,event.event_id
  FOR UPDATE SKIP LOCKED
  LIMIT $2
), claimed AS (
  UPDATE outbox_events AS event
  SET lease_owner=$3, lease_expires_at=$4, attempt_count=event.attempt_count+1
  FROM claimable
  WHERE event.event_id=claimable.event_id
  RETURNING event.schema_version,event.event_id,event.event_type,event.producer_context,
    event.stream_id,event.sequence,event.tenant_id,event.aggregate_type,event.aggregate_id,
    event.trace_id,event.correlation_id,event.causation_id,event.idempotency_key,
    event.client_command_id,event.visibility,event.occurred_at,event.payload
)
SELECT schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,
  aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
  client_command_id,visibility,occurred_at,payload
FROM claimed ORDER BY stream_id,sequence`, now, limit, dispatcher.LeaseOwner, now.Add(dispatcher.LeaseDuration))
		if err != nil {
			return fmt.Errorf("claim Outbox batch: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var event contractsv1.IntegrationEvent
			var clientCommandID sql.NullString
			if err := rows.Scan(
				&event.SchemaVersion, &event.EventID, &event.EventType, &event.ProducerContext,
				&event.StreamID, &event.Sequence, &event.TenantID, &event.AggregateType,
				&event.AggregateID, &event.TraceID, &event.CorrelationID, &event.CausationID,
				&event.IdempotencyKey, &clientCommandID,
				&event.Visibility, &event.OccurredAt, &event.Payload,
			); err != nil {
				return fmt.Errorf("scan Outbox event: %w", err)
			}
			event.ClientCommandID = clientCommandID.String
			if err := event.Validate(); err != nil {
				return fmt.Errorf("invalid durable Outbox event %s: %w", event.EventID, err)
			}
			events = append(events, event)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate Outbox batch: %w", err)
		}
		return nil
	})
	return events, err
}

func (dispatcher OutboxDispatcher) markPublished(ctx context.Context, eventID string, now time.Time) error {
	commandTag, err := dispatcher.Pool.Exec(ctx, `
UPDATE outbox_events
SET published_at=$3,lease_owner=NULL,lease_expires_at=NULL,last_error_code=NULL
WHERE event_id=$1 AND lease_owner=$2 AND published_at IS NULL`, eventID, dispatcher.LeaseOwner, now)
	if err != nil {
		return fmt.Errorf("mark Outbox event %s published: %w", eventID, err)
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("%w: event=%s owner=%s", ErrOutboxLeaseLost, eventID, dispatcher.LeaseOwner)
	}
	return nil
}

func (dispatcher OutboxDispatcher) release(ctx context.Context, eventID, code string) error {
	commandTag, err := dispatcher.Pool.Exec(ctx, `
UPDATE outbox_events
SET lease_owner=NULL,lease_expires_at=NULL,last_error_code=$3
WHERE event_id=$1 AND lease_owner=$2 AND published_at IS NULL`, eventID, dispatcher.LeaseOwner, code)
	if err != nil {
		return fmt.Errorf("release Outbox event %s: %w", eventID, err)
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("%w: event=%s owner=%s", ErrOutboxLeaseLost, eventID, dispatcher.LeaseOwner)
	}
	return nil
}

func errorCode(err error) string {
	code := strings.TrimSpace(err.Error())
	if code == "" {
		return "publish_failed"
	}
	if len(code) > 200 {
		return code[:200]
	}
	return code
}
