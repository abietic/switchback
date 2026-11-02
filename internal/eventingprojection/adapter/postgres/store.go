// Package postgres persists page-optimized Frontier projections.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

var (
	ErrProjectionEventConflict = errors.New("projection source event content conflicts with receipt")
	ErrProjectionStreamGap     = errors.New("projection source stream has a sequence gap")
)

type sourceEventReceiptResolution uint8

const (
	sourceEventReceiptMissing sourceEventReceiptResolution = iota
	sourceEventReceiptVerifiedDuplicate
	sourceEventReceiptLegacyTerminal
)

type Store struct {
	pool            *pgxpool.Pool
	maxReplayDeltas int
	maxReplayBytes  int64
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, maxReplayDeltas: 512, maxReplayBytes: 8 << 20}
}

func NewStoreWithReplayLimits(pool *pgxpool.Pool, maxDeltas int, maxBytes int64) (*Store, error) {
	if pool == nil || maxDeltas < 1 || maxBytes < 1024 {
		return nil, fmt.Errorf("projection replay limits are invalid")
	}
	return &Store{pool: pool, maxReplayDeltas: maxDeltas, maxReplayBytes: maxBytes}, nil
}

func (store *Store) ApplyProjectionEvent(
	ctx context.Context,
	event projectionapp.ProjectionEvent,
	digest string,
) ([]projectionapp.ViewProjection, error) {
	return store.applyProjectionEvent(ctx, event, digest, "")
}

func (store *Store) ApplyProjectionEventWithSourceDigest(
	ctx context.Context,
	event projectionapp.ProjectionEvent,
	digest string,
	sourceDigest string,
) ([]projectionapp.ViewProjection, error) {
	if sourceDigest == "" {
		return nil, fmt.Errorf("projection source event digest is required")
	}
	return store.applyProjectionEvent(ctx, event, digest, sourceDigest)
}

func (store *Store) applyProjectionEvent(
	ctx context.Context,
	event projectionapp.ProjectionEvent,
	digest string,
	sourceDigest string,
) ([]projectionapp.ViewProjection, error) {
	if store == nil || store.pool == nil {
		return nil, fmt.Errorf("projection PostgreSQL pool is required")
	}
	var result []projectionapp.ViewProjection
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var err error
		result, err = store.applyProjectionEventTx(ctx, tx, event, digest, sourceDigest)
		return err
	})
	return result, err
}

func (store *Store) applyProjectionEventTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	event projectionapp.ProjectionEvent,
	digest string,
	sourceDigest string,
) ([]projectionapp.ViewProjection, error) {
	existing, found, err := findReceipt(
		ctx, tx, event.SourceEventID, sourceDigest, digest,
	)
	if err != nil {
		return nil, err
	}
	if found {
		return existing, nil
	}
	if err := advanceSourceStream(ctx, tx, event); err != nil {
		return nil, err
	}
	result := make([]projectionapp.ViewProjection, 0, len(event.Updates))
	for _, update := range event.Updates {
		projection, err := applyViewUpdate(ctx, tx, update, event.SourceEventID, event.ProjectedAt)
		if err != nil {
			return nil, err
		}
		if err := store.pruneReplay(ctx, tx, update); err != nil {
			return nil, err
		}
		if err := store.cleanupCommandReceipts(ctx, tx, update, event.ProjectedAt); err != nil {
			return nil, err
		}
		result = append(result, projection)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode projection event result: %w", err)
	}
	_, err = tx.Exec(ctx, `
INSERT INTO projection_event_receipts
  (source_event_id,source_stream_id,source_sequence,event_digest,source_event_digest,
   projection_result,applied_at)
VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7)`, event.SourceEventID, event.SourceStreamID,
		event.SourceSequence, digest, sourceDigest, encoded, event.ProjectedAt)
	if err != nil {
		return nil, fmt.Errorf("insert projection event receipt: %w", err)
	}
	return result, nil
}

func (store *Store) pruneReplay(
	ctx context.Context,
	tx platformpostgres.Tx,
	update projectionapp.ViewUpdate,
) error {
	_, err := tx.Exec(ctx, `
WITH ranked AS (
  SELECT delta_id,
         row_number() OVER (ORDER BY view_epoch DESC,view_cursor DESC) AS item_rank,
         sum(encoded_size_bytes) OVER (ORDER BY view_epoch DESC,view_cursor DESC) AS cumulative_bytes
  FROM projection_deltas
  WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
)
DELETE FROM projection_deltas delta USING ranked
WHERE delta.delta_id=ranked.delta_id AND ranked.item_rank>1
  AND (ranked.item_rank>$5 OR ranked.cumulative_bytes>$6)`,
		update.TenantID, update.UserID, update.ViewType, update.ViewID,
		store.maxReplayDeltas, store.maxReplayBytes)
	if err != nil {
		return fmt.Errorf("prune projection replay window: %w", err)
	}
	return nil
}

func (store *Store) cleanupCommandReceipts(
	ctx context.Context,
	tx platformpostgres.Tx,
	update projectionapp.ViewUpdate,
	projectedAt time.Time,
) error {
	_, err := tx.Exec(ctx, `
WITH eligible AS (
  SELECT client_command_id,applied_at
  FROM projection_command_receipts
  WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
), cleanup_candidates AS (
  SELECT client_command_id
  FROM eligible
  WHERE applied_at<$5
  ORDER BY applied_at,client_command_id
  LIMIT $6
)
DELETE FROM projection_command_receipts receipt
USING cleanup_candidates candidate
WHERE receipt.tenant_id=$1 AND receipt.user_id=$2
  AND receipt.view_type=$3 AND receipt.view_id=$4
  AND receipt.client_command_id=candidate.client_command_id`,
		update.TenantID, update.UserID, update.ViewType, update.ViewID,
		projectedAt.Add(-projectionapp.CommandReceiptRetentionHorizon),
		projectionapp.CommandReceiptCleanupBatchSize,
	)
	if err != nil {
		return fmt.Errorf("clean up projection command receipts: %w", err)
	}
	return nil
}

func findReceipt(
	ctx context.Context,
	tx platformpostgres.Tx,
	eventID, sourceDigest, digest string,
) ([]projectionapp.ViewProjection, bool, error) {
	var storedDigest string
	var storedSourceDigest sql.NullString
	var result []projectionapp.ViewProjection
	err := tx.QueryRow(ctx, `
SELECT event_digest,source_event_digest,projection_result
FROM projection_event_receipts WHERE source_event_id=$1`, eventID).Scan(
		&storedDigest, &storedSourceDigest, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read projection event receipt: %w", err)
	}
	if storedSourceDigest.Valid && sourceDigest != "" && storedSourceDigest.String != sourceDigest {
		return nil, false, ErrProjectionEventConflict
	}
	if storedDigest != digest {
		return nil, false, ErrProjectionEventConflict
	}
	return result, true, nil
}

// resolveSourceEventReceipt resolves semantic IntegrationEvent duplicates before
// a materializer queries mutable owner tables. It distinguishes a verified
// duplicate from a legacy terminal receipt whose original source bytes are no
// longer retained. Legacy receipts cannot authorize follow-on projection
// repair; their digest is backfilled only from the exact retained Outbox event.
func (store *Store) resolveSourceEventReceipt(
	ctx context.Context,
	event contractsv1.IntegrationEvent,
	sourceDigest string,
) (sourceEventReceiptResolution, error) {
	if store == nil || store.pool == nil || event.EventID == "" || event.StreamID == "" ||
		event.Sequence == 0 || sourceDigest == "" {
		return sourceEventReceiptMissing, fmt.Errorf("projection source receipt inputs are required")
	}
	var storedSourceDigest sql.NullString
	var storedStreamID string
	var storedSequence uint64
	err := store.pool.QueryRow(ctx, `
SELECT source_stream_id,source_sequence,source_event_digest
FROM projection_event_receipts
WHERE source_event_id=$1`, event.EventID).Scan(
		&storedStreamID, &storedSequence, &storedSourceDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return sourceEventReceiptMissing, nil
	}
	if err != nil {
		return sourceEventReceiptMissing, fmt.Errorf("read projection source receipt: %w", err)
	}
	if storedStreamID != event.StreamID || storedSequence != event.Sequence {
		return sourceEventReceiptMissing, ErrProjectionEventConflict
	}
	if !storedSourceDigest.Valid {
		durableDigest, found, err := store.outboxSourceEventDigest(ctx, event.EventID)
		if err != nil {
			return sourceEventReceiptMissing, err
		}
		if !found {
			// A pre-migration receipt is terminal even when its original source
			// event is no longer retained. Without durable source bytes there is
			// no safe digest to backfill and no permission to rebuild a newer
			// projection shape from mutable owner state.
			return sourceEventReceiptLegacyTerminal, nil
		}
		if durableDigest != sourceDigest {
			return sourceEventReceiptMissing, ErrProjectionEventConflict
		}
		if _, err := store.pool.Exec(ctx, `
UPDATE projection_event_receipts
SET source_event_digest=$2
WHERE source_event_id=$1 AND source_event_digest IS NULL`,
			event.EventID, durableDigest); err != nil {
			return sourceEventReceiptMissing, fmt.Errorf("backfill projection source receipt: %w", err)
		}
		return sourceEventReceiptVerifiedDuplicate, nil
	}
	if storedSourceDigest.String != sourceDigest {
		return sourceEventReceiptMissing, ErrProjectionEventConflict
	}
	return sourceEventReceiptVerifiedDuplicate, nil
}

func checkSourceEventReceiptTx(
	ctx context.Context,
	tx platformpostgres.Tx,
	event contractsv1.IntegrationEvent,
	sourceDigest string,
) (bool, error) {
	var storedSourceDigest sql.NullString
	var storedStreamID string
	var storedSequence uint64
	err := tx.QueryRow(ctx, `
SELECT source_stream_id,source_sequence,source_event_digest
FROM projection_event_receipts
WHERE source_event_id=$1`, event.EventID).Scan(
		&storedStreamID, &storedSequence, &storedSourceDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read projection source receipt in transaction: %w", err)
	}
	if storedStreamID != event.StreamID || storedSequence != event.Sequence {
		return false, ErrProjectionEventConflict
	}
	if !storedSourceDigest.Valid {
		return true, nil
	}
	if storedSourceDigest.String != sourceDigest {
		return false, ErrProjectionEventConflict
	}
	return true, nil
}

func (store *Store) outboxSourceEventDigest(
	ctx context.Context,
	eventID string,
) (string, bool, error) {
	var event contractsv1.IntegrationEvent
	var clientCommandID sql.NullString
	err := store.pool.QueryRow(ctx, `
SELECT schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,
       aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,
       client_command_id,visibility,payload,occurred_at
FROM outbox_events
WHERE event_id=$1`, eventID).Scan(
		&event.SchemaVersion, &event.EventID, &event.EventType, &event.ProducerContext,
		&event.StreamID, &event.Sequence, &event.TenantID, &event.AggregateType,
		&event.AggregateID, &event.TraceID, &event.CorrelationID, &event.CausationID,
		&event.IdempotencyKey, &clientCommandID, &event.Visibility, &event.Payload, &event.OccurredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read durable source Outbox event: %w", err)
	}
	event.ClientCommandID = clientCommandID.String
	event.OccurredAt = event.OccurredAt.UTC()
	digest, err := platformpostgres.IntegrationEventDigest(event)
	if err != nil {
		return "", false, fmt.Errorf("digest durable source Outbox event: %w", err)
	}
	return digest, true, nil
}

func advanceSourceStream(ctx context.Context, tx platformpostgres.Tx, event projectionapp.ProjectionEvent) error {
	var last uint64
	err := tx.QueryRow(ctx, `
SELECT last_source_sequence FROM projection_source_streams WHERE source_stream_id=$1 FOR UPDATE`,
		event.SourceStreamID).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		if event.SourceSequence != 1 {
			return ErrProjectionStreamGap
		}
		_, err = tx.Exec(ctx, `
INSERT INTO projection_source_streams (source_stream_id,last_source_sequence,updated_at)
VALUES ($1,$2,$3)`, event.SourceStreamID, event.SourceSequence, event.ProjectedAt)
		return err
	}
	if err != nil {
		return fmt.Errorf("lock projection source stream: %w", err)
	}
	if event.SourceSequence != last+1 {
		return ErrProjectionStreamGap
	}
	_, err = tx.Exec(ctx, `
UPDATE projection_source_streams SET last_source_sequence=$2,updated_at=$3 WHERE source_stream_id=$1`,
		event.SourceStreamID, event.SourceSequence, event.ProjectedAt)
	return err
}

func applyViewUpdate(
	ctx context.Context,
	tx platformpostgres.Tx,
	update projectionapp.ViewUpdate,
	sourceEventID string,
	projectedAt time.Time,
) (projectionapp.ViewProjection, error) {
	projection := projectionapp.ViewProjection{
		SchemaVersion: projectionapp.ProjectionSnapshotSchemaVersion,
		TenantID:      update.TenantID, UserID: update.UserID, ViewType: update.ViewType, ViewID: update.ViewID,
		ViewEpoch: 1, QueryFingerprint: update.QueryFingerprint, Data: update.Data,
		AvailableActions: update.AvailableActions, RelatedRefs: update.RelatedRefs,
	}
	var existingEpoch, existingCursor, existingVersion uint64
	err := tx.QueryRow(ctx, `
SELECT view_epoch,view_cursor,projection_version
FROM view_projections
WHERE tenant_id=$1 AND user_id=$2 AND view_type=$3 AND view_id=$4
FOR UPDATE`, update.TenantID, update.UserID, update.ViewType, update.ViewID).Scan(
		&existingEpoch, &existingCursor, &existingVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return projectionapp.ViewProjection{}, fmt.Errorf("lock View projection: %w", err)
	}
	if err == nil {
		projection.ViewEpoch = existingEpoch
		projection.ViewCursor = existingCursor
		projection.ProjectionVersion = existingVersion
	}
	baseVersion := projection.ProjectionVersion
	projection.ViewCursor++
	projection.ProjectionVersion++
	projection.UpdatedAt = projectedAt.UTC()
	return upsertProjection(ctx, tx, update, projection, baseVersion, sourceEventID, projectedAt)
}

func upsertProjection(
	ctx context.Context,
	tx platformpostgres.Tx,
	update projectionapp.ViewUpdate,
	projection projectionapp.ViewProjection,
	baseVersion uint64,
	sourceEventID string,
	projectedAt time.Time,
) (projectionapp.ViewProjection, error) {
	// projectedAt is passed to SQL separately to keep PostgreSQL as the timestamp codec owner.
	_, err := tx.Exec(ctx, `
INSERT INTO view_projections
  (tenant_id,user_id,view_type,view_id,schema_version,view_epoch,view_cursor,projection_version,
   query_fingerprint,data,available_actions,related_refs,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (tenant_id,user_id,view_type,view_id) DO UPDATE SET
  schema_version=EXCLUDED.schema_version,view_epoch=EXCLUDED.view_epoch,
  view_cursor=EXCLUDED.view_cursor,projection_version=EXCLUDED.projection_version,
  query_fingerprint=EXCLUDED.query_fingerprint,data=EXCLUDED.data,
  available_actions=EXCLUDED.available_actions,related_refs=EXCLUDED.related_refs,
  updated_at=EXCLUDED.updated_at`,
		projection.TenantID, projection.UserID, projection.ViewType, projection.ViewID,
		projection.SchemaVersion, projection.ViewEpoch, projection.ViewCursor, projection.ProjectionVersion,
		projection.QueryFingerprint, projection.Data, projection.AvailableActions,
		projection.RelatedRefs, projectedAt)
	if err != nil {
		return projectionapp.ViewProjection{}, fmt.Errorf("upsert View projection: %w", err)
	}
	deltaDocument := map[string]any{
		"schema_version": projectionapp.ProjectionDeltaSchemaVersion, "delta_id": update.DeltaID,
		"view_type": update.ViewType, "view_id": update.ViewID, "view_epoch": projection.ViewEpoch,
		"view_cursor": projection.ViewCursor, "base_projection_version": baseVersion,
		"result_projection_version": projection.ProjectionVersion, "event_type": update.EventType,
		"critical": update.Critical, "command_id": update.CommandID,
		"source_refs": update.SourceRefs, "payload": json.RawMessage(update.DeltaPayload),
	}
	if update.ClientCommandID != "" {
		deltaDocument["client_command_id"] = update.ClientCommandID
	}
	encoded, err := json.Marshal(deltaDocument)
	if err != nil {
		return projectionapp.ViewProjection{}, fmt.Errorf("encode projection delta: %w", err)
	}
	var commandID any
	if update.CommandID != "" {
		commandID = update.CommandID
	}
	var clientCommandID any
	if update.ClientCommandID != "" {
		clientCommandID = update.ClientCommandID
	}
	_, err = tx.Exec(ctx, `
INSERT INTO projection_deltas
  (delta_id,tenant_id,user_id,view_type,view_id,view_epoch,view_cursor,
   base_projection_version,result_projection_version,event_type,critical,command_id,client_command_id,
   source_refs,payload,encoded_size_bytes,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		update.DeltaID, update.TenantID, update.UserID, update.ViewType, update.ViewID,
		projection.ViewEpoch, projection.ViewCursor, baseVersion, projection.ProjectionVersion,
		update.EventType, update.Critical, commandID, clientCommandID, update.SourceRefs, update.DeltaPayload,
		len(encoded), projectedAt)
	if err != nil {
		return projectionapp.ViewProjection{}, fmt.Errorf("insert projection delta: %w", err)
	}
	if update.ClientCommandID != "" {
		_, err = tx.Exec(ctx, `
INSERT INTO projection_command_receipts
  (tenant_id,user_id,view_type,view_id,client_command_id,command_id,view_epoch,view_cursor,
   projection_version,source_event_id,applied_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			update.TenantID, update.UserID, update.ViewType, update.ViewID,
			update.ClientCommandID, update.CommandID, projection.ViewEpoch,
			projection.ViewCursor, projection.ProjectionVersion, sourceEventID, projectedAt)
		if err != nil {
			return projectionapp.ViewProjection{}, fmt.Errorf("insert projection command receipt: %w", err)
		}
	}
	return projection, nil
}

var _ projectionapp.ProjectionStore = (*Store)(nil)
