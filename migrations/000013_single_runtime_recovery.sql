-- migrate:up

CREATE TABLE runtime_recovery_incidents (
    incident_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text REFERENCES worker_runs (worker_run_id),
    agent_turn_id text REFERENCES agent_turns (agent_turn_id),
    failed_runtime_instance_id text NOT NULL REFERENCES runtime_instances (runtime_instance_id),
    failed_generation integer NOT NULL,
    failed_channel_epoch bigint NOT NULL,
    trigger text NOT NULL,
    phase text NOT NULL,
    previous_session_status text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    replacement_runtime_instance_id text REFERENCES runtime_instances (runtime_instance_id),
    replacement_generation integer,
    replacement_channel_epoch bigint,
    fence_status text NOT NULL DEFAULT 'pending',
    mount_fence_token bigint,
    continuity_status text NOT NULL DEFAULT 'pending',
    evidence_ref text NOT NULL,
    evidence_at timestamptz NOT NULL,
    outcome text,
    outcome_reason text,
    revision bigint NOT NULL DEFAULT 1,
    opened_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    closed_at timestamptz,
    UNIQUE (worker_session_id, failed_generation),
    CHECK (failed_generation > 0 AND failed_channel_epoch > 0),
    CHECK (trigger IN (
        'container_exit',
        'container_missing',
        'worker_control_closed',
        'acp_eof',
        'heartbeat_unhealthy'
    )),
    CHECK (phase IN (
        'open',
        'fencing',
        'provisioning',
        'reattaching',
        'verifying',
        'recovered',
        'exhausted'
    )),
    CHECK (previous_session_status IN ('interactive', 'running', 'waiting_user', 'idle')),
    CHECK (attempt_count BETWEEN 0 AND 1),
    CHECK (fence_status IN ('pending', 'confirmed', 'unproven')),
    CHECK (continuity_status IN ('pending', 'proven', 'unproven')),
    CHECK (revision > 0),
    CHECK (updated_at >= opened_at AND evidence_at <= opened_at),
    CHECK (
        (
            attempt_count = 0
            AND replacement_runtime_instance_id IS NULL
            AND replacement_generation IS NULL
            AND replacement_channel_epoch IS NULL
            AND mount_fence_token IS NULL
        )
        OR
        (
            attempt_count = 1
            AND replacement_runtime_instance_id IS NOT NULL
            AND replacement_generation = failed_generation + 1
            AND replacement_channel_epoch > failed_channel_epoch
            AND mount_fence_token > 0
            AND fence_status = 'confirmed'
        )
    ),
    CHECK (
        (
            phase IN ('recovered', 'exhausted')
            AND closed_at IS NOT NULL
            AND outcome = phase
            AND outcome_reason IS NOT NULL
            AND outcome_reason <> ''
        )
        OR
        (
            phase NOT IN ('recovered', 'exhausted')
            AND closed_at IS NULL
            AND outcome IS NULL
            AND outcome_reason IS NULL
        )
    )
);

CREATE UNIQUE INDEX runtime_recovery_incidents_one_active_per_session
    ON runtime_recovery_incidents (worker_session_id)
    WHERE phase NOT IN ('recovered', 'exhausted');

CREATE INDEX runtime_recovery_incidents_phase
    ON runtime_recovery_incidents (phase, updated_at, incident_id);

CREATE TABLE runtime_heartbeat_observations (
    runtime_instance_id text PRIMARY KEY REFERENCES runtime_instances (runtime_instance_id),
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    runtime_generation integer NOT NULL,
    channel_epoch bigint NOT NULL,
    heartbeat_sequence bigint NOT NULL,
    last_control_sequence bigint NOT NULL,
    last_received_at timestamptz NOT NULL,
    liveness_status text NOT NULL,
    suspect_due_at timestamptz NOT NULL,
    unhealthy_due_at timestamptz NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    CHECK (
        runtime_generation > 0
        AND channel_epoch > 0
        AND heartbeat_sequence > 0
        AND last_control_sequence > 0
    ),
    CHECK (liveness_status IN ('healthy', 'suspect', 'unhealthy')),
    CHECK (unhealthy_due_at > suspect_due_at AND suspect_due_at > last_received_at),
    CHECK (revision > 0)
);

CREATE TABLE runtime_mount_fences (
    incident_id text PRIMARY KEY REFERENCES runtime_recovery_incidents (incident_id),
    failed_runtime_instance_id text NOT NULL REFERENCES runtime_instances (runtime_instance_id),
    failed_generation integer NOT NULL,
    fencing_token bigint NOT NULL,
    status text NOT NULL,
    evidence jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (failed_generation > 0 AND fencing_token > 0),
    CHECK (status IN ('pending', 'confirmed', 'unproven', 'released')),
    CHECK (jsonb_typeof(evidence) = 'object'),
    CHECK (revision > 0 AND updated_at >= created_at)
);

CREATE TABLE runtime_replacement_intents (
    incident_id text PRIMARY KEY REFERENCES runtime_recovery_incidents (incident_id),
    replacement_runtime_instance_id text NOT NULL REFERENCES runtime_instances (runtime_instance_id),
    replacement_generation integer NOT NULL,
    replacement_channel_epoch bigint NOT NULL,
    provision_request_digest text NOT NULL,
    status text NOT NULL,
    container_id text,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (replacement_generation > 1 AND replacement_channel_epoch > 0),
    CHECK (provision_request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('reserved', 'provisioning', 'provisioned', 'failed')),
    CHECK ((status = 'provisioned') = (container_id IS NOT NULL)),
    CHECK (revision > 0 AND updated_at >= created_at)
);

CREATE TABLE runtime_recovery_idempotency_results (
    incident_id text NOT NULL REFERENCES runtime_recovery_incidents (incident_id),
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    command_kind text NOT NULL,
    idempotency_key text NOT NULL,
    command_digest text NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (incident_id, command_kind, idempotency_key),
    UNIQUE (worker_session_id, command_kind, idempotency_key),
    CHECK (command_kind IN (
        'open',
        'begin_fencing',
        'reserve_replacement',
        'record_provisioned',
        'record_reattached',
        'begin_verifying',
        'recover',
        'exhaust'
    )),
    CHECK (command_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(result) = 'object')
);

-- migrate:down

DROP TABLE runtime_recovery_idempotency_results;
DROP TABLE runtime_replacement_intents;
DROP TABLE runtime_mount_fences;
DROP TABLE runtime_heartbeat_observations;
DROP INDEX runtime_recovery_incidents_phase;
DROP INDEX runtime_recovery_incidents_one_active_per_session;
DROP TABLE runtime_recovery_incidents;
