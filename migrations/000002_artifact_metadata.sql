-- migrate:up

CREATE TABLE trace_manifests (
    trace_stream_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    agent_turn_id text NOT NULL REFERENCES agent_turns (agent_turn_id),
    runtime_instance_id text NOT NULL REFERENCES runtime_instances (runtime_instance_id),
    runtime_generation integer NOT NULL,
    manifest_object_key text NOT NULL,
    manifest_checksum text NOT NULL,
    manifest_size_bytes bigint NOT NULL,
    manifest jsonb NOT NULL,
    publication_idempotency_key text NOT NULL,
    visible_at timestamptz NOT NULL,
    UNIQUE (agent_turn_id),
    UNIQUE (tenant_id, publication_idempotency_key),
    CHECK (runtime_generation > 0),
    CHECK (manifest_checksum ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (manifest_size_bytes >= 0),
    CHECK (jsonb_typeof(manifest) = 'object')
);

CREATE INDEX trace_manifests_workspace_visible
    ON trace_manifests (tenant_id, workspace_id, visible_at, trace_stream_id);

CREATE TABLE artifact_refs (
    artifact_id text PRIMARY KEY,
    trace_stream_id text NOT NULL REFERENCES trace_manifests (trace_stream_id),
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    task_id text REFERENCES tasks (task_id),
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    agent_turn_id text NOT NULL REFERENCES agent_turns (agent_turn_id),
    runtime_instance_id text NOT NULL REFERENCES runtime_instances (runtime_instance_id),
    runtime_generation integer NOT NULL,
    artifact_type text NOT NULL,
    object_key text NOT NULL,
    object_checksum text NOT NULL,
    size_bytes bigint NOT NULL,
    artifact_ref jsonb NOT NULL,
    visible_at timestamptz NOT NULL,
    UNIQUE (agent_turn_id, artifact_type),
    CHECK (runtime_generation > 0),
    CHECK (artifact_type IN ('report', 'patch')),
    CHECK (object_checksum ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes >= 0),
    CHECK (jsonb_typeof(artifact_ref) = 'object')
);

CREATE INDEX artifact_refs_workspace_visible
    ON artifact_refs (tenant_id, workspace_id, visible_at, artifact_id);
CREATE INDEX artifact_refs_worker_detail
    ON artifact_refs (worker_session_id, worker_run_id, agent_turn_id, artifact_type);

-- migrate:down

DROP TABLE artifact_refs;
DROP TABLE trace_manifests;

