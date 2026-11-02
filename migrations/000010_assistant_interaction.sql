-- migrate:up

CREATE TABLE assistant_conversations (
    conversation_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    actor_user_id text NOT NULL,
    generation bigint NOT NULL DEFAULT 1,
    status text NOT NULL,
    workspace_id text,
    workspace_revision_id text,
    workspace_resolution_id text,
    workspace_resolution_revision bigint,
    membership_revision bigint,
    next_turn_sequence bigint NOT NULL DEFAULT 1,
    next_run_sequence bigint NOT NULL DEFAULT 1,
    revision bigint NOT NULL DEFAULT 1,
    current_task_draft_id text,
    current_readiness_assessment_id text,
    current_checkpoint_id text,
    active_assistant_run_id text,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    terminal_at timestamptz,
    terminal_reason text,
    UNIQUE (tenant_id, actor_user_id, conversation_id),
    CHECK (generation > 0),
    CHECK (status IN ('active', 'closed')),
    CHECK (next_turn_sequence > 0 AND next_run_sequence > 0 AND revision > 0),
    CHECK ((workspace_id IS NULL) = (workspace_revision_id IS NULL)),
    CHECK ((workspace_id IS NULL) = (workspace_resolution_id IS NULL)),
    CHECK ((workspace_id IS NULL) = (workspace_resolution_revision IS NULL)),
    CHECK ((workspace_id IS NULL) = (membership_revision IS NULL)),
    CHECK (workspace_resolution_revision IS NULL OR workspace_resolution_revision > 0),
    CHECK (membership_revision IS NULL OR membership_revision > 0),
    CHECK ((status = 'closed') = (terminal_at IS NOT NULL)),
    CHECK (updated_at >= created_at)
);

CREATE INDEX assistant_conversations_actor
    ON assistant_conversations (tenant_id, actor_user_id, updated_at DESC);

CREATE TABLE assistant_conversation_turns (
    turn_id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    turn_sequence bigint NOT NULL,
    role text NOT NULL,
    content text NOT NULL,
    visible boolean NOT NULL DEFAULT true,
    source_assistant_run_id text,
    correlation_id text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (conversation_id, turn_sequence),
    CHECK (turn_sequence > 0),
    CHECK (role IN ('user', 'assistant', 'system')),
    CHECK (length(content) > 0)
);

CREATE INDEX assistant_conversation_turns_recent
    ON assistant_conversation_turns (conversation_id, turn_sequence DESC);

CREATE TABLE assistant_runs (
    assistant_run_id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    source_turn_id text NOT NULL REFERENCES assistant_conversation_turns (turn_id),
    run_sequence bigint NOT NULL,
    status text NOT NULL,
    model_profile text NOT NULL,
    model_profile_revision text NOT NULL,
    model_profile_digest text NOT NULL,
    policy_revision text NOT NULL,
    context_budget_tokens integer NOT NULL,
    timeout_at timestamptz NOT NULL,
    lease_owner text,
    lease_expires_at timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    attempt_count integer NOT NULL DEFAULT 0,
    invocation_state text NOT NULL DEFAULT 'not_started',
    invocation_started_at timestamptz,
    result_decision jsonb,
    error_code text,
    retryable boolean NOT NULL DEFAULT false,
    evidence_status text NOT NULL DEFAULT 'pending',
    evidence_error_code text,
    evidence_object_key text,
    observability_status text NOT NULL DEFAULT 'pending',
    observability_error_code text,
    langfuse_trace_id text,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL,
    UNIQUE (conversation_id, run_sequence),
    CHECK (run_sequence > 0),
    CHECK (status IN ('queued', 'claimed', 'completed', 'failed', 'cancelled')),
    CHECK (model_profile_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (context_budget_tokens > 0),
    CHECK (timeout_at > created_at),
    CHECK (fencing_token >= 0 AND attempt_count >= 0),
    CHECK (revision > 0),
    CHECK (invocation_state IN ('not_started', 'request_started', 'response_received')),
    CHECK ((invocation_state = 'not_started') = (invocation_started_at IS NULL)),
    CHECK ((status = 'claimed') = (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)),
    CHECK (result_decision IS NULL OR jsonb_typeof(result_decision) = 'object'),
    CHECK (evidence_status IN ('pending', 'available', 'degraded', 'disabled')),
    CHECK (observability_status IN ('pending', 'passed', 'degraded', 'disabled')),
    CHECK ((evidence_status = 'available') = (evidence_object_key IS NOT NULL)),
    CHECK ((observability_status = 'passed') = (langfuse_trace_id IS NOT NULL)),
    CHECK ((status IN ('completed', 'failed', 'cancelled')) = (completed_at IS NOT NULL)),
    CHECK (updated_at >= created_at)
);

CREATE UNIQUE INDEX assistant_runs_one_active_per_conversation
    ON assistant_runs (conversation_id)
    WHERE status = 'claimed';

CREATE INDEX assistant_runs_dispatch
    ON assistant_runs (created_at, assistant_run_id)
    WHERE status = 'queued';

ALTER TABLE assistant_conversations
    ADD CONSTRAINT assistant_conversations_active_run_fk
    FOREIGN KEY (active_assistant_run_id) REFERENCES assistant_runs (assistant_run_id);

CREATE TABLE assistant_context_packs (
    context_pack_id text PRIMARY KEY,
    assistant_run_id text NOT NULL UNIQUE REFERENCES assistant_runs (assistant_run_id),
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    schema_version text NOT NULL,
    selected_items jsonb NOT NULL,
    dropped_items jsonb NOT NULL,
    compiled_payload jsonb NOT NULL,
    estimated_tokens integer NOT NULL,
    budget_tokens integer NOT NULL,
    content_digest text NOT NULL,
    created_at timestamptz NOT NULL,
    CHECK (schema_version = 'assistant.context_pack.v1'),
    CHECK (jsonb_typeof(selected_items) = 'array'),
    CHECK (jsonb_typeof(dropped_items) = 'array'),
    CHECK (jsonb_typeof(compiled_payload) = 'object'),
    CHECK (estimated_tokens >= 0 AND budget_tokens > 0 AND estimated_tokens <= budget_tokens),
    CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE assistant_readiness_assessments (
    assessment_id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    assistant_run_id text NOT NULL REFERENCES assistant_runs (assistant_run_id),
    assessment_revision bigint NOT NULL,
    policy_revision text NOT NULL,
    workspace_id text,
    workspace_revision_id text,
    dimensions jsonb NOT NULL,
    blocking_unknowns jsonb NOT NULL,
    assumptions jsonb NOT NULL,
    clarification_question_count integer NOT NULL DEFAULT 0,
    status text NOT NULL,
    content_digest text NOT NULL,
    evaluated_at timestamptz NOT NULL,
    UNIQUE (conversation_id, assessment_revision),
    CHECK (assessment_revision > 0),
    CHECK ((workspace_id IS NULL) = (workspace_revision_id IS NULL)),
    CHECK (jsonb_typeof(dimensions) = 'object'),
    CHECK (jsonb_typeof(blocking_unknowns) = 'array'),
    CHECK (jsonb_typeof(assumptions) = 'array'),
    CHECK (clarification_question_count >= 0),
    CHECK (status IN ('clarifying', 'ready_confirmed', 'ready_with_assumptions')),
    CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE assistant_task_drafts (
    task_draft_id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    initiator_user_id text NOT NULL,
    revision bigint NOT NULL,
    parent_task_draft_id text REFERENCES assistant_task_drafts (task_draft_id),
    readiness_assessment_id text NOT NULL REFERENCES assistant_readiness_assessments (assessment_id),
    workspace_id text NOT NULL,
    workspace_revision_id text NOT NULL,
    task_spec_snapshot jsonb NOT NULL,
    task_spec_digest text NOT NULL,
    assumptions jsonb NOT NULL,
    status text NOT NULL,
    invalidated_at timestamptz,
    created_at timestamptz NOT NULL,
    UNIQUE (conversation_id, revision),
    CHECK (revision > 0),
    CHECK (jsonb_typeof(task_spec_snapshot) = 'object'),
    CHECK (task_spec_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(assumptions) = 'array'),
    CHECK (status IN ('current', 'invalidated', 'confirmed')),
    CHECK ((status = 'invalidated') = (invalidated_at IS NOT NULL))
);

CREATE UNIQUE INDEX assistant_task_drafts_one_current
    ON assistant_task_drafts (conversation_id)
    WHERE status = 'current';

CREATE TABLE assistant_clarification_checkpoints (
    checkpoint_id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    revision bigint NOT NULL,
    task_draft_id text REFERENCES assistant_task_drafts (task_draft_id),
    task_draft_revision bigint,
    working_draft jsonb NOT NULL,
    readiness_assessment_id text NOT NULL REFERENCES assistant_readiness_assessments (assessment_id),
    assumptions jsonb NOT NULL,
    blockers jsonb NOT NULL,
    evidence_refs jsonb NOT NULL,
    allowed_actions jsonb NOT NULL,
    status text NOT NULL,
    resolved_action text,
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    UNIQUE (conversation_id, revision),
    CHECK (revision > 0),
    CHECK ((task_draft_id IS NULL) = (task_draft_revision IS NULL)),
    CHECK (task_draft_revision IS NULL OR task_draft_revision > 0),
    CHECK (jsonb_typeof(working_draft) = 'object'),
    CHECK (jsonb_typeof(assumptions) = 'array'),
    CHECK (jsonb_typeof(blockers) = 'array'),
    CHECK (jsonb_typeof(evidence_refs) = 'array'),
    CHECK (jsonb_typeof(allowed_actions) = 'array'),
    CHECK (status IN ('active', 'resolved', 'cancelled')),
    CHECK (resolved_action IS NULL OR resolved_action IN ('continue', 'narrow_scope', 'cancel')),
    CHECK ((status = 'active') = (resolved_at IS NULL))
);

CREATE UNIQUE INDEX assistant_clarification_checkpoints_one_active
    ON assistant_clarification_checkpoints (conversation_id)
    WHERE status = 'active';

CREATE TABLE assistant_creation_intent_bindings (
    intent_id text PRIMARY KEY REFERENCES task_creation_intents (intent_id),
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    conversation_revision bigint NOT NULL,
    task_draft_id text NOT NULL REFERENCES assistant_task_drafts (task_draft_id),
    task_draft_revision bigint NOT NULL,
    assistant_readiness_assessment_id text NOT NULL REFERENCES assistant_readiness_assessments (assessment_id),
    task_readiness_assessment_id text NOT NULL REFERENCES task_readiness_assessments (assessment_id),
    workspace_resolution_id text NOT NULL,
    workspace_resolution_revision bigint NOT NULL,
    membership_revision bigint NOT NULL,
    task_spec_digest text NOT NULL,
    trigger_policy_revision text NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (conversation_id, idempotency_key),
    CHECK (conversation_revision > 0 AND task_draft_revision > 0),
    CHECK (workspace_resolution_revision > 0 AND membership_revision > 0),
    CHECK (task_spec_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE assistant_evidence_refs (
    evidence_ref_id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES assistant_conversations (conversation_id),
    assistant_run_id text REFERENCES assistant_runs (assistant_run_id),
    boundary_type text NOT NULL,
    object_key text NOT NULL,
    checksum text NOT NULL,
    byte_size bigint NOT NULL,
    status text NOT NULL,
    error_code text,
    created_at timestamptz NOT NULL,
    finalized_at timestamptz,
    UNIQUE (conversation_id, boundary_type, object_key),
    CHECK (boundary_type IN ('checkpoint', 'ready_draft', 'task_created', 'cancelled', 'run_failed')),
    CHECK (checksum ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (byte_size >= 0),
    CHECK (status IN ('pending', 'available', 'degraded')),
    CHECK ((status = 'available') = (finalized_at IS NOT NULL))
);

ALTER TABLE view_projections
    DROP CONSTRAINT view_projections_view_type_check;

ALTER TABLE view_projections
    ADD CONSTRAINT view_projections_view_type_check
    CHECK (view_type IN ('task_summary', 'worker_detail', 'artifact_detail', 'command_status', 'assistant_conversation'));

-- migrate:down

DELETE FROM projection_deltas WHERE view_type = 'assistant_conversation';
DELETE FROM view_projections WHERE view_type = 'assistant_conversation';

ALTER TABLE view_projections
    DROP CONSTRAINT view_projections_view_type_check;

ALTER TABLE view_projections
    ADD CONSTRAINT view_projections_view_type_check
    CHECK (view_type IN ('task_summary', 'worker_detail', 'artifact_detail', 'command_status'));

DROP TABLE assistant_evidence_refs;
DROP TABLE assistant_creation_intent_bindings;
DROP TABLE assistant_clarification_checkpoints;
DROP TABLE assistant_task_drafts;
DROP TABLE assistant_readiness_assessments;
DROP TABLE assistant_context_packs;
ALTER TABLE assistant_conversations DROP CONSTRAINT assistant_conversations_active_run_fk;
DROP TABLE assistant_runs;
DROP TABLE assistant_conversation_turns;
DROP TABLE assistant_conversations;
