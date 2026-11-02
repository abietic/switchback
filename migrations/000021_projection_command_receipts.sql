-- migrate:up

ALTER TABLE outbox_events
    ADD COLUMN client_command_id text
    CHECK (client_command_id IS NULL OR client_command_id <> '');

CREATE TABLE projection_command_receipts (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    view_type text NOT NULL,
    view_id text NOT NULL,
    client_command_id text NOT NULL,
    command_id text NOT NULL,
    view_epoch bigint NOT NULL,
    view_cursor bigint NOT NULL,
    projection_version bigint NOT NULL,
    source_event_id text NOT NULL,
    applied_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, user_id, view_type, view_id, client_command_id),
    UNIQUE (tenant_id, user_id, view_type, view_id, command_id),
    FOREIGN KEY (tenant_id, user_id, view_type, view_id)
        REFERENCES view_projections (tenant_id, user_id, view_type, view_id)
        ON DELETE CASCADE,
    CHECK (client_command_id <> ''),
    CHECK (command_id <> ''),
    CHECK (view_epoch > 0),
    CHECK (view_cursor > 0),
    CHECK (projection_version > 0),
    CHECK (source_event_id <> '')
);

CREATE INDEX projection_command_receipts_snapshot_lookup
    ON projection_command_receipts
        (tenant_id, user_id, view_type, view_id, client_command_id);

-- migrate:down

DROP TABLE projection_command_receipts;

ALTER TABLE outbox_events
    DROP COLUMN client_command_id;
