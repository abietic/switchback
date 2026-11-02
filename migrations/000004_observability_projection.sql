-- migrate:up

CREATE TABLE run_observability_projections (
    worker_run_id text PRIMARY KEY REFERENCES worker_runs (worker_run_id),
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    runtime_instance_id text NOT NULL REFERENCES runtime_instances (runtime_instance_id),
    runtime_generation integer NOT NULL,
    agent_session_id text NOT NULL,
    trace_id text NOT NULL,
    agent_turn_ids text[] NOT NULL,
    model_observation_count integer NOT NULL,
    execution_outcome text NOT NULL,
    status text NOT NULL,
    error_code text,
    attempted_at timestamptz NOT NULL,
    verified_at timestamptz,
    revision bigint NOT NULL,
    UNIQUE (tenant_id, trace_id),
    CHECK (runtime_generation > 0),
    CHECK (cardinality(agent_turn_ids) > 0),
    CHECK (model_observation_count > 0),
    CHECK (execution_outcome IN ('completed', 'failed', 'cancelled', 'timed_out')),
    CHECK (status IN ('passed', 'degraded')),
    CHECK ((status = 'passed' AND error_code IS NULL AND verified_at IS NOT NULL) OR
           (status = 'degraded' AND error_code IS NOT NULL AND verified_at IS NULL)),
    CHECK (revision > 0)
);

CREATE INDEX run_observability_workspace_status
    ON run_observability_projections (tenant_id, workspace_id, status, attempted_at);

-- migrate:down

DROP TABLE run_observability_projections;
