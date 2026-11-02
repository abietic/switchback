-- migrate:up

CREATE TABLE display_checkpoints (
    display_stream_id text NOT NULL,
    checkpoint_version bigint NOT NULL,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    worker_session_id text NOT NULL,
    worker_run_id text NOT NULL,
    agent_turn_id text NOT NULL,
    status text NOT NULL,
    last_observed_transient_sequence bigint NOT NULL,
    checkpoint jsonb NOT NULL,
    checkpoint_digest text NOT NULL,
    source_event_id text NOT NULL,
    checkpointed_at timestamptz NOT NULL,
    PRIMARY KEY (display_stream_id, checkpoint_version),
    UNIQUE (source_event_id),
    CHECK (checkpoint_version > 0),
    CHECK (status IN ('streaming', 'waiting_user', 'completed', 'failed', 'cancelled', 'timed_out')),
    CHECK (last_observed_transient_sequence >= 0),
    CHECK (jsonb_typeof(checkpoint) = 'object'),
    CHECK (checkpoint_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE INDEX display_checkpoints_latest
    ON display_checkpoints (tenant_id, user_id, worker_session_id, display_stream_id, checkpoint_version DESC);

-- migrate:down

DROP TABLE display_checkpoints;
