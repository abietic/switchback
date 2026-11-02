-- migrate:up

LOCK TABLE outbox_events IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM outbox_events
        WHERE schema_version = 'event.v1'
          AND client_command_id IS NOT NULL
          AND published_at IS NULL
    ) THEN
        RAISE EXCEPTION
            'cannot upgrade IntegrationEvent v2 migration while unpublished event.v1 rows carry client_command_id; drain with v1-compatible binary before upgrade';
    END IF;
END
$$;

ALTER TABLE outbox_events
    DROP CONSTRAINT outbox_events_schema_version_check,
    ADD CONSTRAINT outbox_events_schema_version_check
        CHECK (schema_version IN ('event.v1', 'event.v2')),
    ADD CONSTRAINT outbox_events_client_command_schema_check
        CHECK (schema_version <> 'event.v1' OR client_command_id IS NULL)
        NOT VALID;

DROP INDEX projection_command_receipts_snapshot_lookup;

CREATE INDEX projection_command_receipts_cleanup
    ON projection_command_receipts
        (tenant_id, user_id, view_type, view_id, applied_at);

-- migrate:down

LOCK TABLE outbox_events IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM outbox_events
        WHERE schema_version = 'event.v2'
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade IntegrationEvent v2 migration while event.v2 Outbox rows exist';
    END IF;
END
$$;

DROP INDEX projection_command_receipts_cleanup;

CREATE INDEX projection_command_receipts_snapshot_lookup
    ON projection_command_receipts
        (tenant_id, user_id, view_type, view_id, client_command_id);

ALTER TABLE outbox_events
    DROP CONSTRAINT outbox_events_client_command_schema_check,
    DROP CONSTRAINT outbox_events_schema_version_check,
    ADD CONSTRAINT outbox_events_schema_version_check
        CHECK (schema_version = 'event.v1');
