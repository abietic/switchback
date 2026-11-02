-- migrate:up

CREATE TABLE runtime_control_channels (
    runtime_instance_id text PRIMARY KEY REFERENCES runtime_instances (runtime_instance_id),
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    runtime_generation integer NOT NULL,
    channel_epoch bigint NOT NULL,
    credential_digest text NOT NULL,
    process_nonce text,
    stream_attempt bigint NOT NULL DEFAULT 0,
    owner_id text,
    owner_fence bigint NOT NULL DEFAULT 0,
    resume_claim_revision bigint,
    resume_ready_digest text,
    resume_completed_revision bigint,
    resume_completed_at timestamptz,
    resume_completed_state json,
    command_committed_sequence bigint NOT NULL DEFAULT 0,
    command_committed_digest text NOT NULL DEFAULT 'sha256:4f4de1293c07ebd69a11800878dd689311974c66f87a0582375b21c72bb787af',
    command_acknowledged_sequence bigint NOT NULL DEFAULT 0,
    command_applied_sequence bigint NOT NULL DEFAULT 0,
    event_committed_sequence bigint NOT NULL DEFAULT 0,
    event_committed_digest text NOT NULL DEFAULT 'sha256:4f4de1293c07ebd69a11800878dd689311974c66f87a0582375b21c72bb787af',
    event_acknowledged_sequence bigint NOT NULL DEFAULT 0,
    event_applied_sequence bigint NOT NULL DEFAULT 0,
    event_replay_checkpoint_sequence bigint,
    event_replay_checkpoint_digest text,
    event_replay_target_sequence bigint,
    event_replay_target_digest text,
    active_effect_message_id text,
    active_effect_agent_turn_id text REFERENCES agent_turns (agent_turn_id),
    active_effect_command_sequence bigint,
    active_effect_phase text,
    active_effect_snapshot json,
    reported_active_turn json,
    terminal_proof_pending boolean NOT NULL DEFAULT false,
    status text NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    bound_at timestamptz NOT NULL,
    claimed_at timestamptz,
    updated_at timestamptz NOT NULL,
    disconnected_at timestamptz,
    UNIQUE (worker_session_id, runtime_generation, channel_epoch),
    CHECK (runtime_generation > 0 AND channel_epoch > 0),
    CHECK (credential_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (stream_attempt >= 0 AND owner_fence >= 0),
    CHECK (
        (stream_attempt = 0 AND process_nonce IS NULL AND owner_id IS NULL
            AND owner_fence = 0 AND claimed_at IS NULL AND status = 'bound')
        OR
        (stream_attempt = 0 AND process_nonce IS NULL AND owner_id IS NULL
            AND owner_fence > 0 AND claimed_at IS NULL AND status = 'fenced')
        OR
        (stream_attempt > 0 AND process_nonce IS NOT NULL AND owner_id IS NOT NULL AND owner_fence > 0 AND claimed_at IS NOT NULL)
    ),
    CHECK (resume_claim_revision IS NULL OR resume_claim_revision > 0),
    CHECK (
        (
            resume_ready_digest IS NULL
            AND resume_completed_revision IS NULL
            AND resume_completed_at IS NULL
            AND resume_completed_state IS NULL
        )
        OR
        (
            resume_ready_digest ~ '^sha256:[0-9a-f]{64}$'
            AND resume_completed_revision > 0
            AND resume_completed_at IS NOT NULL
            AND json_typeof(resume_completed_state) = 'object'
        )
    ),
    CHECK (command_committed_sequence >= 0 AND command_acknowledged_sequence >= 0 AND command_applied_sequence >= 0),
    CHECK (command_applied_sequence <= command_acknowledged_sequence),
    CHECK (command_acknowledged_sequence <= command_committed_sequence),
    CHECK (event_committed_sequence >= 0 AND event_acknowledged_sequence >= 0 AND event_applied_sequence >= 0),
    CHECK (event_applied_sequence <= event_acknowledged_sequence AND event_acknowledged_sequence <= event_committed_sequence),
    CHECK (command_committed_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (event_committed_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        (
            event_replay_checkpoint_sequence IS NULL
            AND event_replay_checkpoint_digest IS NULL
        )
        OR
        (
            event_replay_checkpoint_sequence >= event_committed_sequence
            AND event_replay_checkpoint_digest ~ '^sha256:[0-9a-f]{64}$'
        )
    ),
    CHECK (
        (
            event_replay_target_sequence IS NULL
            AND event_replay_target_digest IS NULL
        )
        OR
        (
            event_replay_target_sequence >= event_committed_sequence
            AND event_replay_target_digest ~ '^sha256:[0-9a-f]{64}$'
            AND event_replay_checkpoint_sequence IS NOT NULL
            AND event_replay_checkpoint_sequence <= event_replay_target_sequence
        )
    ),
    CHECK (
        (
            active_effect_message_id IS NULL
            AND active_effect_agent_turn_id IS NULL
            AND active_effect_command_sequence IS NULL
            AND active_effect_phase IS NULL
            AND active_effect_snapshot IS NULL
        )
        OR
        (
            active_effect_message_id IS NOT NULL
            AND active_effect_agent_turn_id IS NOT NULL
            AND active_effect_command_sequence > 0
            AND active_effect_phase IN ('received', 'prompt_invocation_intent', 'active', 'terminal')
            AND json_typeof(active_effect_snapshot) = 'object'
        )
    ),
    CHECK (reported_active_turn IS NULL OR json_typeof(reported_active_turn) = 'object'),
    CHECK (NOT terminal_proof_pending OR reported_active_turn IS NOT NULL),
    CHECK (status IN ('bound', 'resuming', 'connected', 'disconnected', 'fenced')),
    CHECK (revision > 0 AND updated_at >= bound_at),
    CHECK ((status = 'disconnected') = (disconnected_at IS NOT NULL))
);

CREATE TABLE runtime_control_messages (
    runtime_instance_id text NOT NULL REFERENCES runtime_control_channels (runtime_instance_id),
    direction text NOT NULL,
    sequence bigint NOT NULL,
    message_id text NOT NULL,
    idempotency_key text NOT NULL,
    fingerprint text NOT NULL,
    cursor_digest text NOT NULL,
    envelope json NOT NULL,
    outcome json,
    effect_status text NOT NULL,
    apply_idempotency_key text,
    apply_outcome_digest text,
    created_at timestamptz NOT NULL,
    acknowledged_at timestamptz,
    applied_at timestamptz,
    PRIMARY KEY (runtime_instance_id, direction, sequence),
    UNIQUE (runtime_instance_id, direction, message_id),
    UNIQUE (runtime_instance_id, direction, idempotency_key),
    CHECK (direction IN ('command', 'event')),
    CHECK (sequence > 0),
    CHECK (fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (cursor_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (json_typeof(envelope) = 'object'),
    CHECK (outcome IS NULL OR json_typeof(outcome) = 'object'),
    CHECK (
        (
            direction = 'command'
            AND outcome IS NULL
            AND apply_idempotency_key IS NULL
            AND apply_outcome_digest IS NULL
            AND (
                (
                    effect_status IN ('journaled', 'received', 'prompt_invocation_intent')
                    AND applied_at IS NULL
                )
                OR
                (
                    effect_status IN ('applied', 'active', 'terminal')
                    AND applied_at IS NOT NULL
                )
            )
        )
        OR
        (
            direction = 'event'
            AND (
                (
                    effect_status = 'received'
                    AND outcome IS NULL
                    AND apply_idempotency_key IS NULL
                    AND apply_outcome_digest IS NULL
                    AND applied_at IS NULL
                )
                OR
                (
                    effect_status = 'applied'
                    AND outcome IS NOT NULL
                    AND apply_idempotency_key IS NOT NULL
                    AND apply_outcome_digest ~ '^sha256:[0-9a-f]{64}$'
                    AND applied_at IS NOT NULL
                )
            )
        )
    )
);

CREATE INDEX runtime_control_messages_pending_commands
    ON runtime_control_messages (runtime_instance_id, sequence)
    WHERE direction = 'command' AND acknowledged_at IS NULL;

CREATE INDEX runtime_control_messages_pending_events
    ON runtime_control_messages (runtime_instance_id, sequence)
    WHERE direction = 'event' AND effect_status = 'received';

-- migrate:down

DROP INDEX runtime_control_messages_pending_events;
DROP INDEX runtime_control_messages_pending_commands;
DROP TABLE runtime_control_messages;
DROP TABLE runtime_control_channels;
