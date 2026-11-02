-- migrate:up

CREATE TABLE worker_timeline_turns (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    worker_session_id text NOT NULL,
    worker_run_id text NOT NULL,
    agent_turn_id text NOT NULL,
    display_stream_id text NOT NULL,
    turn_started_at timestamptz NOT NULL,
    turn_terminal_at timestamptz,
    turn_status text NOT NULL,
    latest_checkpoint_version bigint NOT NULL DEFAULT 0,
    checkpoint_complete boolean NOT NULL DEFAULT false,
    backfill_status text NOT NULL,
    first_checkpointed_at timestamptz,
    latest_checkpointed_at timestamptz,
    PRIMARY KEY (worker_session_id, agent_turn_id),
    UNIQUE (display_stream_id),
    CHECK (turn_status IN ('accepted', 'delivered', 'consumed', 'completed', 'failed', 'cancelled', 'timed_out')),
    CHECK (latest_checkpoint_version >= 0),
    CHECK (backfill_status IN ('online', 'pending', 'non_backfillable')),
    CHECK ((latest_checkpoint_version > 0) = (latest_checkpointed_at IS NOT NULL)),
    CHECK (backfill_status <> 'online' OR latest_checkpoint_version > 0),
    CHECK (latest_checkpoint_version > 0 OR backfill_status <> 'online')
);

CREATE INDEX worker_timeline_turns_page
    ON worker_timeline_turns
       (tenant_id, user_id, worker_session_id, turn_started_at DESC, agent_turn_id DESC);

-- migrate:down

DROP TABLE worker_timeline_turns;
