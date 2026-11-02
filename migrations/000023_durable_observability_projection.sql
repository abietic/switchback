-- migrate:up

CREATE TABLE observability_projection_activations (
    schema_version text PRIMARY KEY,
    activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (schema_version <> '')
);

CREATE TABLE observability_projection_activation_exclusions (
    schema_version text NOT NULL REFERENCES observability_projection_activations(schema_version) ON DELETE CASCADE,
    trace_stream_id text NOT NULL,
    manifest_checksum text NOT NULL,
    PRIMARY KEY (schema_version, trace_stream_id),
    CHECK (manifest_checksum ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE observability_projection_jobs (
    schema_version text NOT NULL REFERENCES observability_projection_activations(schema_version) ON DELETE CASCADE,
    trace_stream_id text NOT NULL,
    manifest_checksum text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    lease_token bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    error_code text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (schema_version, trace_stream_id, manifest_checksum),
    UNIQUE (schema_version, trace_stream_id),
    CHECK (manifest_checksum ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('pending','running','passed','retryable','blocked')),
    CHECK (attempts >= 0 AND lease_token >= 0),
    CHECK ((status = 'running') = (lease_until IS NOT NULL))
);

CREATE INDEX observability_projection_jobs_claimable
ON observability_projection_jobs(schema_version, next_attempt_at, created_at)
WHERE status IN ('pending','retryable','running');

-- migrate:down

DROP TABLE observability_projection_jobs;
DROP TABLE observability_projection_activation_exclusions;
DROP TABLE observability_projection_activations;
