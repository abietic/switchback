-- migrate:up

CREATE TABLE worker_message_queue_positions (
    worker_session_id text PRIMARY KEY REFERENCES worker_sessions (worker_session_id),
    next_sequence bigint NOT NULL DEFAULT 1,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    CHECK (next_sequence > 0),
    CHECK (revision > 0)
);

CREATE TABLE worker_user_commands (
    command_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    agent_turn_id text NOT NULL REFERENCES agent_turns (agent_turn_id),
    command_type text NOT NULL,
    content text NOT NULL DEFAULT '',
    expected_session_revision bigint NOT NULL,
    expected_run_revision bigint NOT NULL,
    expected_turn_revision bigint NOT NULL,
    queue_sequence bigint,
    status text NOT NULL,
    outcome jsonb NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, user_id, idempotency_key),
    UNIQUE (worker_session_id, queue_sequence),
    CHECK (command_type IN ('queue', 'interrupt')),
    CHECK ((command_type = 'queue') = (queue_sequence IS NOT NULL)),
    CHECK (queue_sequence IS NULL OR queue_sequence > 0),
    CHECK (expected_session_revision > 0 AND expected_run_revision > 0 AND expected_turn_revision > 0),
    CHECK (status IN ('accepted', 'dispatched', 'applied', 'rejected')),
    CHECK (jsonb_typeof(outcome) = 'object'),
    CHECK (updated_at >= created_at)
);

CREATE INDEX worker_user_commands_pending
    ON worker_user_commands (worker_session_id, command_type, status, queue_sequence, created_at)
    WHERE status IN ('accepted', 'dispatched');

-- migrate:down

DROP TABLE worker_user_commands;
DROP TABLE worker_message_queue_positions;
