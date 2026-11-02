-- migrate:up

ALTER TABLE view_projections
    DROP CONSTRAINT view_projections_view_type_check;

ALTER TABLE view_projections
    ADD CONSTRAINT view_projections_view_type_check
    CHECK (view_type IN (
        'home_dashboard',
        'task_summary',
        'worker_detail',
        'artifact_detail',
        'command_status',
        'assistant_conversation'
    ));

ALTER TABLE projection_event_receipts
    ADD COLUMN source_event_digest text;

ALTER TABLE projection_event_receipts
    ADD CONSTRAINT projection_event_receipts_source_event_digest_check
    CHECK (
        source_event_digest IS NULL
        OR source_event_digest ~ '^sha256:[0-9a-f]{64}$'
    );

-- migrate:down

WITH affected_views AS (
    SELECT DISTINCT
           item.value->>'tenant_id' AS tenant_id,
           item.value->>'user_id' AS user_id,
           item.value->>'view_type' AS view_type,
           item.value->>'view_id' AS view_id
    FROM projection_event_receipts AS receipt
    JOIN outbox_events AS event
      ON event.event_id = receipt.source_event_id
     AND event.stream_id = receipt.source_stream_id
     AND event.sequence = receipt.source_sequence
    CROSS JOIN LATERAL jsonb_array_elements(receipt.projection_result) AS item(value)
    WHERE event.published_at IS NULL
      AND item.value->>'schema_version' = 'frontier.projection_snapshot.v1'
      AND item.value->>'view_type' <> 'home_dashboard'
)
UPDATE view_projections AS view
SET view_epoch = view.view_epoch + 1,
    view_cursor = 0,
    projection_version = 0
FROM affected_views AS affected
WHERE view.tenant_id = affected.tenant_id
  AND view.user_id = affected.user_id
  AND view.view_type = affected.view_type
  AND view.view_id = affected.view_id;

WITH rollback_targets AS (
    SELECT event.event_id,
           item.value->>'tenant_id' AS tenant_id,
           item.value->>'user_id' AS user_id,
           item.value->>'view_type' AS view_type,
           item.value->>'view_id' AS view_id,
           (item.value->>'view_epoch')::bigint AS view_epoch,
           (item.value->>'view_cursor')::bigint AS view_cursor,
           (item.value->>'projection_version')::bigint AS projection_version
    FROM projection_event_receipts AS receipt
    JOIN outbox_events AS event
      ON event.event_id = receipt.source_event_id
     AND event.stream_id = receipt.source_stream_id
     AND event.sequence = receipt.source_sequence
    CROSS JOIN LATERAL jsonb_array_elements(receipt.projection_result) AS item(value)
    WHERE event.published_at IS NULL
      AND item.value->>'schema_version' = 'frontier.projection_snapshot.v1'
      AND item.value->>'view_type' <> 'home_dashboard'
)
DELETE FROM projection_deltas AS delta
USING rollback_targets AS target
WHERE delta.delta_id = target.event_id || '-' ||
        replace(target.view_type || '-' || target.view_id, '/', '-')
  AND delta.tenant_id = target.tenant_id
  AND delta.user_id = target.user_id
  AND delta.view_type = target.view_type
  AND delta.view_id = target.view_id
  AND delta.view_epoch = target.view_epoch
  AND delta.view_cursor = target.view_cursor
  AND delta.base_projection_version = target.projection_version - 1
  AND delta.result_projection_version = target.projection_version
  AND delta.source_refs @> jsonb_build_array(
      jsonb_build_object('type', 'integration_event', 'id', target.event_id)
  );

DELETE FROM projection_event_receipts AS receipt
USING outbox_events AS event
WHERE event.published_at IS NULL
  AND receipt.source_event_id = event.event_id
  AND receipt.source_stream_id = event.stream_id
  AND receipt.source_sequence = event.sequence;

WITH retained AS (
    SELECT DISTINCT ON (receipt.source_stream_id)
           receipt.source_stream_id,
           receipt.source_sequence AS last_source_sequence,
           receipt.applied_at AS updated_at
    FROM projection_event_receipts AS receipt
    WHERE EXISTS (
        SELECT 1
        FROM outbox_events AS event
        WHERE event.published_at IS NULL
          AND event.stream_id = receipt.source_stream_id
    )
    ORDER BY receipt.source_stream_id, receipt.source_sequence DESC
)
UPDATE projection_source_streams AS stream
SET last_source_sequence = retained.last_source_sequence,
    updated_at = retained.updated_at
FROM retained
WHERE stream.source_stream_id = retained.source_stream_id;

DELETE FROM projection_source_streams AS stream
WHERE EXISTS (
    SELECT 1
    FROM outbox_events AS event
    WHERE event.published_at IS NULL
      AND event.stream_id = stream.source_stream_id
)
  AND NOT EXISTS (
      SELECT 1
      FROM projection_event_receipts AS receipt
      WHERE receipt.source_stream_id = stream.source_stream_id
  );

WITH owned_home_receipts AS MATERIALIZED (
    SELECT receipt.source_event_id, receipt.source_stream_id
    FROM projection_event_receipts AS receipt
    WHERE receipt.source_sequence = 1
      AND jsonb_array_length(receipt.projection_result) = 1
      AND receipt.projection_result->0->>'schema_version' =
          'frontier.projection_snapshot.v1'
      AND receipt.projection_result->0->>'view_type' = 'home_dashboard'
      AND receipt.projection_result->0->>'view_id' = 'index'
      AND (
          (
              receipt.source_event_id = receipt.source_stream_id
              AND receipt.source_event_digest IS NOT NULL
              AND left(
                  receipt.source_event_id,
                  length('local-home-dashboard-refresh/')
              ) = 'local-home-dashboard-refresh/'
          )
          OR (
              receipt.source_event_id = 'local-home-dashboard-bootstrap-v1'
              AND receipt.source_stream_id = 'local-home-dashboard-bootstrap'
          )
          OR (
              receipt.source_event_id = receipt.source_stream_id
              AND left(
                  receipt.source_event_id,
                  length('local-home-dashboard-contract-upgrade-v2/')
              ) = 'local-home-dashboard-contract-upgrade-v2/'
          )
      )
),
deleted_home_streams AS (
    DELETE FROM projection_source_streams AS stream
    USING owned_home_receipts AS owned
    WHERE stream.source_stream_id = owned.source_stream_id
    RETURNING stream.source_stream_id
)
DELETE FROM projection_event_receipts AS receipt
USING owned_home_receipts AS owned
WHERE receipt.source_event_id = owned.source_event_id
  AND receipt.source_stream_id = owned.source_stream_id;

ALTER TABLE projection_event_receipts
    DROP CONSTRAINT projection_event_receipts_source_event_digest_check;

ALTER TABLE projection_event_receipts
    DROP COLUMN source_event_digest;

DELETE FROM projection_deltas WHERE view_type = 'home_dashboard';
DELETE FROM view_projections WHERE view_type = 'home_dashboard';

ALTER TABLE view_projections
    DROP CONSTRAINT view_projections_view_type_check;

ALTER TABLE view_projections
    ADD CONSTRAINT view_projections_view_type_check
    CHECK (view_type IN (
        'task_summary',
        'worker_detail',
        'artifact_detail',
        'command_status',
        'assistant_conversation'
    ));
