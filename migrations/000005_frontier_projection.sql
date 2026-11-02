-- migrate:up

CREATE TABLE projection_source_streams (
    source_stream_id text PRIMARY KEY,
    last_source_sequence bigint NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (last_source_sequence > 0)
);

CREATE TABLE view_projections (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    view_type text NOT NULL,
    view_id text NOT NULL,
    schema_version text NOT NULL,
    view_epoch bigint NOT NULL,
    view_cursor bigint NOT NULL,
    projection_version bigint NOT NULL,
    query_fingerprint text NOT NULL,
    data jsonb NOT NULL,
    available_actions jsonb NOT NULL,
    related_refs jsonb NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, user_id, view_type, view_id),
    CHECK (view_type IN ('task_summary', 'worker_detail', 'artifact_detail', 'command_status')),
    CHECK (view_epoch > 0),
    CHECK (view_cursor >= 0),
    CHECK (projection_version >= 0),
    CHECK (jsonb_typeof(data) = 'object'),
    CHECK (jsonb_typeof(available_actions) = 'array'),
    CHECK (jsonb_typeof(related_refs) = 'array')
);

CREATE INDEX view_projections_workspace_lookup
    ON view_projections (tenant_id, user_id, view_type, updated_at DESC, view_id);

CREATE TABLE projection_deltas (
    delta_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    view_type text NOT NULL,
    view_id text NOT NULL,
    view_epoch bigint NOT NULL,
    view_cursor bigint NOT NULL,
    base_projection_version bigint NOT NULL,
    result_projection_version bigint NOT NULL,
    event_type text NOT NULL,
    critical boolean NOT NULL,
    command_id text,
    source_refs jsonb NOT NULL,
    payload jsonb NOT NULL,
    encoded_size_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, user_id, view_type, view_id, view_epoch, view_cursor),
    FOREIGN KEY (tenant_id, user_id, view_type, view_id)
        REFERENCES view_projections (tenant_id, user_id, view_type, view_id),
    CHECK (view_epoch > 0),
    CHECK (view_cursor > 0),
    CHECK (base_projection_version >= 0),
    CHECK (result_projection_version = base_projection_version + 1),
    CHECK (jsonb_typeof(source_refs) = 'array'),
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (encoded_size_bytes > 0)
);

CREATE INDEX projection_deltas_replay
    ON projection_deltas (tenant_id, user_id, view_type, view_id, view_epoch, view_cursor);

CREATE TABLE projection_event_receipts (
    source_event_id text PRIMARY KEY,
    source_stream_id text NOT NULL,
    source_sequence bigint NOT NULL,
    event_digest text NOT NULL,
    projection_result jsonb NOT NULL,
    applied_at timestamptz NOT NULL,
    UNIQUE (source_stream_id, source_sequence),
    CHECK (source_sequence > 0),
    CHECK (event_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(projection_result) = 'array')
);

-- migrate:down

DROP TABLE projection_event_receipts;
DROP TABLE projection_deltas;
DROP TABLE view_projections;
DROP TABLE projection_source_streams;
