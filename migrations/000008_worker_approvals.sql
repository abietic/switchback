-- migrate:up

CREATE TABLE worker_approval_requests (
    approval_request_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    agent_turn_id text NOT NULL REFERENCES agent_turns (agent_turn_id),
    capability text NOT NULL,
    target text NOT NULL,
    effect text NOT NULL,
    reason text NOT NULL,
    scope_options text[] NOT NULL,
    status text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    CHECK (cardinality(scope_options) > 0),
    CHECK (scope_options <@ ARRAY['once','session']::text[]),
    CHECK (status IN ('pending','approved','denied','expired')),
    CHECK (revision > 0),
    CHECK ((status = 'pending') = (resolved_at IS NULL)),
    CHECK (expires_at > created_at)
);

CREATE INDEX worker_approval_requests_pending
    ON worker_approval_requests (worker_session_id, status, expires_at, created_at);

CREATE TABLE worker_approval_decisions (
    command_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    approval_request_id text NOT NULL REFERENCES worker_approval_requests (approval_request_id),
    decision text NOT NULL,
    scope text NOT NULL,
    request_digest text NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, user_id, idempotency_key),
    CHECK (decision IN ('approve','deny')),
    CHECK (scope IN ('once','session')),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE worker_approval_grants (
    approval_grant_id text PRIMARY KEY,
    approval_request_id text NOT NULL REFERENCES worker_approval_requests (approval_request_id),
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    scope text NOT NULL,
    capability text NOT NULL,
    target text NOT NULL,
    effect text NOT NULL,
    status text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (scope IN ('once','session')),
    CHECK (status IN ('active','consumed','revoked')),
    CHECK ((scope = 'once' AND status = 'consumed') = (consumed_at IS NOT NULL))
);

-- migrate:down

DROP TABLE worker_approval_grants;
DROP TABLE worker_approval_decisions;
DROP TABLE worker_approval_requests;
