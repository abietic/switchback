-- migrate:up

CREATE TABLE worker_session_continuity_quotas (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    quota_limit integer NOT NULL DEFAULT 10,
    reserved_count integer NOT NULL DEFAULT 0,
    revision bigint NOT NULL DEFAULT 1,
    policy_revision text NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, user_id),
    CHECK (quota_limit > 0),
    CHECK (reserved_count >= 0 AND reserved_count <= quota_limit),
    CHECK (revision > 0)
);

CREATE TABLE worker_sessions (
    worker_session_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    workspace_revision_id text NOT NULL,
    working_copy_id text NOT NULL,
    status text NOT NULL,
    active_runtime_instance_id text,
    bound_task_id text,
    active_worker_run_id text,
    session_revision bigint NOT NULL DEFAULT 1,
    runtime_generation integer NOT NULL DEFAULT 0,
    quota_reservation_revision bigint NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    terminal_at timestamptz,
    terminal_reason text,
    CHECK (status IN ('allocating', 'interactive', 'running', 'recovering', 'waiting_user', 'idle', 'lost', 'expired', 'closed')),
    CHECK (session_revision > 0),
    CHECK (runtime_generation >= 0),
    CHECK (absolute_expires_at > created_at),
    CHECK (updated_at >= created_at),
    CHECK ((status IN ('expired', 'closed')) = (terminal_at IS NOT NULL))
);

CREATE INDEX worker_sessions_continuity_scope
    ON worker_sessions (tenant_id, user_id, status, created_at);
CREATE INDEX worker_sessions_workspace
    ON worker_sessions (tenant_id, workspace_id, created_at);

CREATE TABLE task_readiness_assessments (
    assessment_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_user_id text NOT NULL,
    workspace_id text NOT NULL,
    workspace_revision_id text NOT NULL,
    source text NOT NULL,
    policy_revision text NOT NULL,
    dimensions jsonb NOT NULL,
    blocking_unknowns jsonb NOT NULL,
    assumptions jsonb NOT NULL,
    status text NOT NULL,
    content_digest text NOT NULL,
    evaluated_at timestamptz NOT NULL,
    CHECK (source IN ('assistant', 'delivery', 'api')),
    CHECK (jsonb_typeof(dimensions) = 'object'),
    CHECK (jsonb_typeof(blocking_unknowns) = 'array'),
    CHECK (jsonb_typeof(assumptions) = 'array'),
    CHECK (status IN ('clarifying', 'ready_confirmed', 'ready_with_assumptions')),
    CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE task_creation_intents (
    intent_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_user_id text NOT NULL,
    workspace_id text NOT NULL,
    workspace_revision_id text NOT NULL,
    readiness_assessment_id text NOT NULL REFERENCES task_readiness_assessments (assessment_id),
    task_spec_digest text NOT NULL,
    trigger_policy_revision text NOT NULL,
    status text NOT NULL,
    consumed_by_task_id text,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    CHECK (task_spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('active', 'consumed', 'expired', 'revoked')),
    CHECK (expires_at > created_at),
    CHECK ((status = 'consumed') = (consumed_by_task_id IS NOT NULL AND consumed_at IS NOT NULL))
);

CREATE INDEX task_creation_intents_active
    ON task_creation_intents (tenant_id, actor_user_id, intent_id, expires_at)
    WHERE status = 'active';

CREATE TABLE worker_session_target_intents (
    intent_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_user_id text NOT NULL,
    workspace_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_session_revision bigint NOT NULL,
    working_copy_id text NOT NULL,
    task_spec_digest text NOT NULL,
    source_ref text NOT NULL,
    source_revision bigint NOT NULL,
    busy_policy text NOT NULL,
    status text NOT NULL,
    consumed_by_task_id text,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    UNIQUE (tenant_id, actor_user_id, idempotency_key),
    CHECK (worker_session_revision > 0 AND source_revision > 0),
    CHECK (task_spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (busy_policy = 'queue'),
    CHECK (status IN ('active', 'consumed', 'expired', 'revoked')),
    CHECK (expires_at > created_at),
    CHECK ((status = 'consumed') = (consumed_by_task_id IS NOT NULL AND consumed_at IS NOT NULL))
);

CREATE INDEX worker_session_target_intents_active
    ON worker_session_target_intents (tenant_id, actor_user_id, worker_session_id, expires_at)
    WHERE status = 'active';

CREATE TABLE tasks (
    task_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    initiator_user_id text NOT NULL,
    workspace_id text NOT NULL,
    workspace_revision_id text NOT NULL,
    task_spec_snapshot jsonb NOT NULL,
    task_spec_digest text NOT NULL,
    creation_intent_id text NOT NULL REFERENCES task_creation_intents (intent_id),
    requested_worker_session_id text,
    worker_session_target_intent_id text REFERENCES worker_session_target_intents (intent_id),
    session_context_policy text NOT NULL DEFAULT 'fresh_session',
    worker_session_id text,
    current_worker_run_id text,
    status text NOT NULL,
    acceptance_mode text NOT NULL,
    result_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
    waiting_reason jsonb,
    allocation_wait_reason text,
    session_allocation_sequence bigint,
    session_queue_sequence bigint,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CHECK (status IN ('queued', 'running', 'waiting_user', 'delivered', 'completed', 'failed', 'cancelled')),
    CHECK (acceptance_mode IN ('auto', 'manual')),
    CHECK (session_context_policy IN ('inherit_current', 'fresh_session', 'exact_base')),
    CHECK ((requested_worker_session_id IS NULL) = (worker_session_target_intent_id IS NULL)),
    CHECK ((worker_session_target_intent_id IS NULL) OR session_context_policy = 'inherit_current'),
    CHECK (task_spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(task_spec_snapshot) = 'object'),
    CHECK (jsonb_typeof(result_refs) = 'array'),
    CHECK (waiting_reason IS NULL OR jsonb_typeof(waiting_reason) = 'object'),
    CHECK (allocation_wait_reason IS NULL OR allocation_wait_reason = 'session_capacity'),
    CHECK (NOT (session_allocation_sequence IS NOT NULL AND session_queue_sequence IS NOT NULL)),
    CHECK (revision > 0),
    CHECK (updated_at >= created_at)
);

CREATE INDEX tasks_initiator_status
    ON tasks (tenant_id, initiator_user_id, status, created_at);
CREATE INDEX tasks_workspace_status
    ON tasks (tenant_id, workspace_id, status, created_at);

CREATE TABLE task_transitions (
    transition_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks (task_id),
    idempotency_key text NOT NULL,
    expected_revision bigint NOT NULL,
    result_revision bigint NOT NULL,
    from_state text,
    to_state text NOT NULL,
    trigger text NOT NULL,
    result jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    UNIQUE (task_id, result_revision),
    UNIQUE (task_id, idempotency_key),
    CHECK (expected_revision >= 0),
    CHECK (result_revision = expected_revision + 1),
    CHECK (from_state IS NULL OR from_state IN ('queued', 'running', 'waiting_user', 'delivered', 'completed', 'failed', 'cancelled')),
    CHECK (to_state IN ('queued', 'running', 'waiting_user', 'delivered', 'completed', 'failed', 'cancelled')),
    CHECK (jsonb_typeof(result) = 'object')
);

CREATE TABLE session_allocation_requests (
    allocation_request_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    workspace_id text NOT NULL,
    workspace_revision_id text NOT NULL,
    request_kind text NOT NULL,
    owner_id text NOT NULL,
    owner_revision bigint NOT NULL,
    context_policy text NOT NULL,
    requested_worker_session_id text,
    session_allocation_sequence bigint NOT NULL,
    status text NOT NULL,
    allocation_wait_reason text,
    allocated_worker_session_id text,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, user_id, session_allocation_sequence),
    UNIQUE (tenant_id, user_id, idempotency_key),
    CHECK (request_kind IN ('task', 'session_continuation')),
    CHECK (owner_revision > 0),
    CHECK (context_policy IN ('inherit_current', 'fresh_session', 'exact_base')),
    CHECK (session_allocation_sequence > 0),
    CHECK (status IN ('pending', 'allocating', 'allocated', 'cancelled', 'rejected')),
    CHECK (allocation_wait_reason IS NULL OR allocation_wait_reason = 'session_capacity'),
    CHECK (updated_at >= created_at)
);

CREATE TABLE session_allocation_queue_positions (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    next_sequence bigint NOT NULL DEFAULT 1,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, user_id),
    CHECK (next_sequence > 0),
    CHECK (revision > 0)
);

CREATE INDEX session_allocation_requests_head
    ON session_allocation_requests (tenant_id, user_id, session_allocation_sequence)
    WHERE status = 'pending';

CREATE TABLE session_task_queue_positions (
    worker_session_id text PRIMARY KEY REFERENCES worker_sessions (worker_session_id),
    next_sequence bigint NOT NULL DEFAULT 1,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    CHECK (next_sequence > 0),
    CHECK (revision > 0)
);

CREATE TABLE session_task_queue_entries (
    queue_entry_id text PRIMARY KEY,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    task_id text NOT NULL REFERENCES tasks (task_id),
    session_queue_sequence bigint NOT NULL,
    queue_revision bigint NOT NULL,
    expected_session_revision bigint NOT NULL,
    status text NOT NULL,
    idempotency_key text NOT NULL,
    enqueued_at timestamptz NOT NULL,
    dispatched_at timestamptz,
    UNIQUE (worker_session_id, session_queue_sequence),
    UNIQUE (worker_session_id, idempotency_key),
    CHECK (session_queue_sequence > 0),
    CHECK (queue_revision > 0),
    CHECK (expected_session_revision > 0),
    CHECK (status IN ('pending', 'permit_issued', 'dispatched', 'cancelled'))
);

CREATE INDEX session_task_queue_entries_head
    ON session_task_queue_entries (worker_session_id, session_queue_sequence)
    WHERE status = 'pending';

CREATE TABLE session_dispatch_permits (
    permit_id text PRIMARY KEY,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    target_kind text NOT NULL,
    target_id text NOT NULL,
    session_queue_sequence bigint,
    queue_revision bigint NOT NULL,
    expected_session_revision bigint NOT NULL,
    runtime_generation integer NOT NULL,
    capacity_admission_ref text NOT NULL,
    status text NOT NULL,
    idempotency_key text NOT NULL,
    consumed_by_worker_run_id text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz,
    UNIQUE (worker_session_id, idempotency_key),
    CHECK (target_kind IN ('task', 'standalone_run')),
    CHECK ((target_kind = 'task') = (session_queue_sequence IS NOT NULL)),
    CHECK (queue_revision > 0),
    CHECK (expected_session_revision > 0),
    CHECK (runtime_generation > 0),
    CHECK (status IN ('active', 'consumed', 'expired', 'revoked')),
    CHECK ((status = 'consumed') = (consumed_by_worker_run_id IS NOT NULL AND consumed_at IS NOT NULL)),
    CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX session_dispatch_permits_one_active_per_session
    ON session_dispatch_permits (worker_session_id)
    WHERE status = 'active';

CREATE TABLE worker_runs (
    worker_run_id text PRIMARY KEY,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    task_id text REFERENCES tasks (task_id),
    origin_task_id text REFERENCES tasks (task_id),
    trigger_type text NOT NULL,
    previous_worker_run_id text REFERENCES worker_runs (worker_run_id),
    start_snapshot jsonb NOT NULL,
    agent_profile_id text NOT NULL,
    agent_profile_revision text NOT NULL,
    agent_profile_digest text NOT NULL,
    status text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    terminal_reason text,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    terminal_at timestamptz,
    CHECK (trigger_type IN ('task_initial', 'task_rework', 'task_session_continuation', 'direct_agent_turn')),
    CHECK ((trigger_type = 'direct_agent_turn') OR task_id IS NOT NULL),
    CHECK (jsonb_typeof(start_snapshot) = 'object'),
    CHECK (agent_profile_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('starting', 'running', 'waiting_user', 'completed', 'failed', 'cancelled', 'timed_out')),
    CHECK (revision > 0),
    CHECK ((status IN ('completed', 'failed', 'cancelled', 'timed_out')) = (terminal_at IS NOT NULL))
);

CREATE UNIQUE INDEX worker_runs_one_active_per_session
    ON worker_runs (worker_session_id)
    WHERE status IN ('starting', 'running', 'waiting_user');

CREATE TABLE agent_turns (
    agent_turn_id text PRIMARY KEY,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    agent_session_id text NOT NULL,
    mode text NOT NULL,
    content jsonb NOT NULL,
    status text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    deadline_at timestamptz,
    terminal_at timestamptz,
    terminal_reason text,
    UNIQUE (worker_session_id, idempotency_key),
    CHECK (mode IN ('prompt', 'queue', 'interrupt', 'steer')),
    CHECK (jsonb_typeof(content) = 'object'),
    CHECK (status IN ('accepted', 'queued', 'delivered', 'consumed', 'completed', 'cancelled', 'timed_out', 'failed')),
    CHECK (revision > 0),
    CHECK ((started_at IS NULL AND deadline_at IS NULL) OR deadline_at = started_at + interval '24 hours'),
    CHECK ((status IN ('completed', 'cancelled', 'timed_out', 'failed')) = (terminal_at IS NOT NULL))
);

CREATE UNIQUE INDEX agent_turns_one_active_prompt_per_run
    ON agent_turns (worker_run_id)
    WHERE status IN ('accepted', 'delivered', 'consumed') AND mode IN ('prompt', 'queue');

CREATE TABLE runtime_instances (
    runtime_instance_id text PRIMARY KEY,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text REFERENCES worker_runs (worker_run_id),
    generation integer NOT NULL,
    runtime_type text NOT NULL,
    container_id text,
    status text NOT NULL,
    image_ref text NOT NULL,
    resource_profile_id text NOT NULL,
    network_policy_id text NOT NULL,
    control_channel_epoch bigint NOT NULL DEFAULT 0,
    last_command_sequence bigint NOT NULL DEFAULT 0,
    last_event_sequence bigint NOT NULL DEFAULT 0,
    cleanup_confirmed boolean NOT NULL DEFAULT false,
    loss_diagnostic jsonb,
    cleanup_diagnostic jsonb,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    ended_at timestamptz,
    CHECK (generation > 0),
    CHECK (runtime_type = 'docker_container'),
    CHECK (status IN ('starting', 'active', 'suspect', 'unhealthy', 'draining', 'terminated', 'lost')),
    CHECK ((status IN ('terminated', 'lost')) = (ended_at IS NOT NULL)),
    CHECK (control_channel_epoch >= 0 AND last_command_sequence >= 0 AND last_event_sequence >= 0),
    CHECK (loss_diagnostic IS NULL OR jsonb_typeof(loss_diagnostic) = 'object'),
    CHECK (cleanup_diagnostic IS NULL OR jsonb_typeof(cleanup_diagnostic) = 'object'),
    CHECK (revision > 0),
    UNIQUE (worker_session_id, generation)
);

CREATE UNIQUE INDEX runtime_instances_one_active_per_session
    ON runtime_instances (worker_session_id)
    WHERE status IN ('starting', 'active', 'suspect', 'unhealthy', 'draining');

CREATE TABLE scheduled_commands (
    scheduled_command_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    command_type text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    expected_revision bigint NOT NULL,
    payload jsonb NOT NULL,
    due_at timestamptz NOT NULL,
    status text NOT NULL,
    lease_owner text,
    lease_expires_at timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    attempt_count integer NOT NULL DEFAULT 0,
    idempotency_key text NOT NULL,
    last_error_code text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, command_type, target_type, target_id, idempotency_key),
    CHECK (expected_revision > 0),
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (status IN ('pending', 'leased', 'applied', 'cancelled', 'dead_letter')),
    CHECK ((status = 'leased') = (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CHECK (fencing_token >= 0 AND attempt_count >= 0),
    CHECK (updated_at >= created_at)
);

CREATE INDEX scheduled_commands_due
    ON scheduled_commands (due_at, scheduled_command_id)
    WHERE status = 'pending';

CREATE TABLE idempotency_results (
    tenant_id text NOT NULL,
    actor_user_id text NOT NULL,
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_digest text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    response_status integer NOT NULL,
    response_body jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz,
    PRIMARY KEY (tenant_id, actor_user_id, operation, idempotency_key),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (response_status BETWEEN 100 AND 599),
    CHECK (jsonb_typeof(response_body) = 'object')
);

CREATE TABLE owner_stream_positions (
    stream_id text PRIMARY KEY,
    owner_context text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    next_sequence bigint NOT NULL DEFAULT 1,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL,
    UNIQUE (owner_context, aggregate_type, aggregate_id),
    CHECK (next_sequence > 0),
    CHECK (revision > 0)
);

CREATE TABLE outbox_events (
    event_id text PRIMARY KEY,
    schema_version text NOT NULL,
    event_type text NOT NULL,
    producer_context text NOT NULL,
    stream_id text NOT NULL REFERENCES owner_stream_positions (stream_id),
    sequence bigint NOT NULL,
    tenant_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    trace_id text NOT NULL,
    correlation_id text NOT NULL,
    causation_id text NOT NULL,
    idempotency_key text NOT NULL,
    visibility text NOT NULL,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL,
    published_at timestamptz,
    lease_owner text,
    lease_expires_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0,
    last_error_code text,
    UNIQUE (stream_id, sequence),
    UNIQUE (producer_context, idempotency_key),
    CHECK (schema_version = 'event.v1'),
    CHECK (sequence > 0),
    CHECK (visibility IN ('internal', 'user')),
    CHECK (jsonb_typeof(payload) = 'object'),
    CHECK (attempt_count >= 0),
    CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL))
);

CREATE INDEX outbox_events_unpublished
    ON outbox_events (occurred_at, event_id)
    WHERE published_at IS NULL;

CREATE TABLE consumer_receipts (
    consumer_name text NOT NULL,
    event_id text NOT NULL,
    stream_id text NOT NULL,
    sequence bigint NOT NULL,
    event_digest text NOT NULL,
    applied_at timestamptz NOT NULL,
    PRIMARY KEY (consumer_name, event_id),
    UNIQUE (consumer_name, stream_id, sequence),
    CHECK (sequence > 0),
    CHECK (event_digest ~ '^sha256:[0-9a-f]{64}$')
);

-- migrate:down

DROP TABLE consumer_receipts;
DROP TABLE outbox_events;
DROP TABLE owner_stream_positions;
DROP TABLE idempotency_results;
DROP TABLE scheduled_commands;
DROP TABLE runtime_instances;
DROP TABLE agent_turns;
DROP TABLE worker_runs;
DROP TABLE session_dispatch_permits;
DROP TABLE session_task_queue_entries;
DROP TABLE session_task_queue_positions;
DROP TABLE session_allocation_queue_positions;
DROP TABLE session_allocation_requests;
DROP TABLE task_transitions;
DROP TABLE tasks;
DROP TABLE worker_session_target_intents;
DROP TABLE task_creation_intents;
DROP TABLE task_readiness_assessments;
DROP TABLE worker_sessions;
DROP TABLE worker_session_continuity_quotas;
