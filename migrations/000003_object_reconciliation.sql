-- migrate:up

CREATE TABLE evidence_object_refs (
    object_key text PRIMARY KEY,
    trace_stream_id text NOT NULL REFERENCES trace_manifests (trace_stream_id),
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    object_kind text NOT NULL,
    object_checksum text NOT NULL,
    size_bytes bigint NOT NULL,
    visible_at timestamptz NOT NULL,
    CHECK (object_checksum ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes >= 0)
);

CREATE INDEX evidence_object_refs_trace
    ON evidence_object_refs (trace_stream_id, object_kind);

CREATE TABLE object_reconciliation_claims (
    object_key text PRIMARY KEY,
    object_state text NOT NULL,
    object_checksum text NOT NULL,
    size_bytes bigint NOT NULL,
    modified_at timestamptz NOT NULL,
    status text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 1,
    error_code text,
    claimed_at timestamptz NOT NULL,
    claim_expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    CHECK (object_state IN ('temporary', 'finalized')),
    CHECK (object_checksum ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes >= 0),
    CHECK (status IN ('claimed', 'deleted', 'failed')),
    CHECK (attempt_count > 0),
    CHECK (claim_expires_at > claimed_at),
    CHECK ((status = 'claimed') = (completed_at IS NULL)),
    CHECK ((status = 'failed') = (error_code IS NOT NULL))
);

CREATE INDEX object_reconciliation_claims_retry
    ON object_reconciliation_claims (status, claim_expires_at, object_key);

-- migrate:down

DROP TABLE object_reconciliation_claims;
DROP TABLE evidence_object_refs;

