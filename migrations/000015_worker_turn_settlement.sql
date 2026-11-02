-- migrate:up

CREATE TABLE worker_turn_settlements (
    settlement_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    agent_session_id text NOT NULL,
    agent_turn_id text NOT NULL UNIQUE REFERENCES agent_turns (agent_turn_id),
    runtime_instance_id text NOT NULL REFERENCES runtime_control_channels (runtime_instance_id),
    runtime_generation integer NOT NULL,
    channel_epoch bigint NOT NULL,
    terminal_event_direction text NOT NULL DEFAULT 'event',
    terminal_event_sequence bigint NOT NULL,
    terminal_message_id text NOT NULL,
    terminal_idempotency_key text NOT NULL,
    terminal_envelope_digest text NOT NULL,
    terminal_event_disposition text,
    terminal_apply_outcome_digest text,
    phase text NOT NULL DEFAULT 'accepted',
    phase_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    phase_state_digest text NOT NULL
        DEFAULT 'sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a',
    revision bigint NOT NULL DEFAULT 1,
    lease_owner_id text,
    lease_fence bigint NOT NULL DEFAULT 0,
    lease_expires_at timestamptz,
    accepted_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    terminal_event_applied_at timestamptz,
    completed_at timestamptz,
    UNIQUE (runtime_instance_id, terminal_event_sequence),
    UNIQUE (runtime_instance_id, terminal_message_id),
    FOREIGN KEY (runtime_instance_id, terminal_event_direction, terminal_event_sequence)
        REFERENCES runtime_control_messages (runtime_instance_id, direction, sequence),
    CHECK (runtime_generation > 0 AND channel_epoch > 0 AND terminal_event_sequence > 0),
    CHECK (terminal_event_direction = 'event'),
    CHECK (terminal_envelope_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        terminal_apply_outcome_digest IS NULL
        OR terminal_apply_outcome_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    CHECK (phase_state_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(phase_state) = 'object'),
    CHECK (
        phase IN (
            'accepted',
            'evidence_prepared',
            'domain_committed',
            'evidence_published',
            'task_settled',
            'terminal_event_applied',
            'continuation_settled',
            'completed'
        )
    ),
    CHECK (
        (
            phase IN (
                'accepted',
                'evidence_prepared',
                'domain_committed',
                'evidence_published',
                'task_settled'
            )
            AND terminal_event_disposition IS NULL
            AND terminal_apply_outcome_digest IS NULL
            AND terminal_event_applied_at IS NULL
            AND completed_at IS NULL
        )
        OR
        (
            phase IN ('terminal_event_applied', 'continuation_settled')
            AND terminal_event_disposition = 'commit'
            AND terminal_apply_outcome_digest IS NOT NULL
            AND terminal_event_applied_at IS NOT NULL
            AND completed_at IS NULL
        )
        OR
        (
            phase = 'completed'
            AND terminal_event_disposition IN ('commit', 'ignore')
            AND terminal_apply_outcome_digest IS NOT NULL
            AND terminal_event_applied_at IS NOT NULL
            AND completed_at IS NOT NULL
        )
    ),
    CHECK (revision > 0 AND lease_fence >= 0),
    CHECK (
        (lease_owner_id IS NULL AND lease_expires_at IS NULL)
        OR
        (lease_owner_id IS NOT NULL AND lease_fence > 0 AND lease_expires_at IS NOT NULL)
    ),
    CHECK (updated_at >= accepted_at),
    CHECK (terminal_event_applied_at IS NULL OR terminal_event_applied_at >= accepted_at),
    CHECK (completed_at IS NULL OR completed_at >= accepted_at)
);

-- Existing Runtime-control journals may already contain a durable terminal
-- inbox row when this migration is installed. Backfill those rows so a Backend
-- restart after upgrade does not turn an otherwise valid Apply retry into a
-- missing-settlement conflict. Ambiguous or identity-invalid historical rows
-- stop the migration instead of being guessed or silently skipped.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_control_messages message
        WHERE message.direction = 'event'
          AND message.envelope->>'kind' = 'event'
          AND message.envelope->>'message_type' = 'agent_turn.terminal'
          AND (
              message.effect_status NOT IN ('received', 'applied')
              OR (
                  message.effect_status = 'applied'
                  AND (
                      COALESCE(message.outcome->>'effect_disposition','') NOT IN ('commit', 'ignore')
                      OR message.apply_outcome_digest IS NULL
                      OR message.applied_at IS NULL
                  )
              )
          )
    ) THEN
        RAISE EXCEPTION
            'cannot backfill worker_turn_settlements: invalid terminal Runtime-control outcome';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM runtime_control_messages message
        WHERE message.direction = 'event'
          AND message.envelope->>'kind' = 'event'
          AND message.envelope->>'message_type' = 'agent_turn.terminal'
          AND NOT EXISTS (
              SELECT 1
              FROM runtime_control_channels channel
              JOIN worker_sessions session
                ON session.worker_session_id=channel.worker_session_id
              JOIN worker_runs run
                ON run.worker_session_id=session.worker_session_id
               AND run.worker_run_id=message.envelope->>'worker_run_id'
              JOIN agent_turns turn
                ON turn.worker_session_id=session.worker_session_id
               AND turn.worker_run_id=run.worker_run_id
               AND turn.agent_turn_id=message.envelope->>'agent_turn_id'
               AND turn.agent_session_id=message.envelope->>'agent_session_id'
              JOIN runtime_instances runtime
                ON runtime.runtime_instance_id=channel.runtime_instance_id
               AND runtime.worker_session_id=session.worker_session_id
               AND runtime.generation=(message.envelope->>'runtime_generation')::integer
               AND runtime.control_channel_epoch=(message.envelope->>'channel_epoch')::bigint
              WHERE channel.runtime_instance_id=message.runtime_instance_id
                AND message.envelope->>'worker_session_id'=session.worker_session_id
                AND message.envelope->>'runtime_instance_id'=runtime.runtime_instance_id
          )
    ) THEN
        RAISE EXCEPTION
            'cannot backfill worker_turn_settlements: terminal Runtime-control identity is invalid';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM runtime_control_messages message
        JOIN runtime_control_channels channel
          ON channel.runtime_instance_id=message.runtime_instance_id
        JOIN worker_sessions session
          ON session.worker_session_id=channel.worker_session_id
        JOIN worker_runs run
          ON run.worker_session_id=session.worker_session_id
         AND run.worker_run_id=message.envelope->>'worker_run_id'
        JOIN agent_turns turn
          ON turn.worker_session_id=session.worker_session_id
         AND turn.worker_run_id=run.worker_run_id
         AND turn.agent_turn_id=message.envelope->>'agent_turn_id'
        JOIN runtime_instances runtime
          ON runtime.runtime_instance_id=channel.runtime_instance_id
         AND runtime.worker_session_id=session.worker_session_id
        WHERE message.direction = 'event'
          AND message.envelope->>'kind' = 'event'
          AND message.envelope->>'message_type' = 'agent_turn.terminal'
          AND message.effect_status = 'received'
          AND (
              session.status <> 'running'
              OR run.status <> 'running'
              OR turn.status NOT IN ('delivered','consumed')
              OR runtime.status <> 'active'
          )
    ) THEN
        RAISE EXCEPTION
            'cannot backfill worker_turn_settlements: received terminal has partial business settlement';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM runtime_control_messages message
        WHERE message.direction = 'event'
          AND message.envelope->>'kind' = 'event'
          AND message.envelope->>'message_type' = 'agent_turn.terminal'
        GROUP BY message.envelope->>'agent_turn_id'
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot backfill worker_turn_settlements: multiple terminal events exist for one AgentTurn';
    END IF;
END
$$;

INSERT INTO worker_turn_settlements (
    settlement_id,
    tenant_id,
    worker_session_id,
    worker_run_id,
    agent_session_id,
    agent_turn_id,
    runtime_instance_id,
    runtime_generation,
    channel_epoch,
    terminal_event_sequence,
    terminal_message_id,
    terminal_idempotency_key,
    terminal_envelope_digest,
    terminal_event_disposition,
    terminal_apply_outcome_digest,
    phase,
    phase_state,
    phase_state_digest,
    accepted_at,
    updated_at,
    terminal_event_applied_at,
    completed_at
)
SELECT
    'turn-settlement/' || (message.envelope->>'agent_turn_id'),
    session.tenant_id,
    session.worker_session_id,
    run.worker_run_id,
    turn.agent_session_id,
    turn.agent_turn_id,
    runtime.runtime_instance_id,
    runtime.generation,
    runtime.control_channel_epoch,
    message.sequence,
    message.message_id,
    message.idempotency_key,
    message.fingerprint,
    CASE
        WHEN message.effect_status = 'applied'
            THEN message.outcome->>'effect_disposition'
        ELSE NULL
    END,
    CASE
        WHEN message.effect_status = 'applied'
            THEN message.apply_outcome_digest
        ELSE NULL
    END,
    CASE
        WHEN message.effect_status = 'received' THEN 'accepted'
        ELSE 'completed'
    END,
    CASE
        WHEN message.effect_status = 'received' THEN '{}'::jsonb
        ELSE '{"legacy_backfill":true,"schema_version":1}'::jsonb
    END,
    CASE
        WHEN message.effect_status = 'received'
            THEN 'sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a'
        ELSE 'sha256:cf47ef167e70344cd3e6e48d4426c1e34055aa32da428e623c15b9ff82fa8079'
    END,
    message.created_at,
    COALESCE(message.applied_at, message.created_at),
    message.applied_at,
    CASE
        WHEN message.effect_status = 'applied' THEN message.applied_at
        ELSE NULL
    END
FROM runtime_control_messages message
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=message.runtime_instance_id
JOIN worker_sessions session
  ON session.worker_session_id=channel.worker_session_id
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=message.envelope->>'worker_run_id'
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id=message.envelope->>'agent_turn_id'
 AND turn.agent_session_id=message.envelope->>'agent_session_id'
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=channel.runtime_instance_id
 AND runtime.worker_session_id=session.worker_session_id
 AND runtime.generation=(message.envelope->>'runtime_generation')::integer
 AND runtime.control_channel_epoch=(message.envelope->>'channel_epoch')::bigint
WHERE message.direction = 'event'
  AND message.envelope->>'kind' = 'event'
  AND message.envelope->>'message_type' = 'agent_turn.terminal'
  AND message.envelope->>'worker_session_id'=session.worker_session_id
  AND message.envelope->>'runtime_instance_id'=runtime.runtime_instance_id;

CREATE INDEX worker_turn_settlements_pending
    ON worker_turn_settlements (accepted_at, settlement_id)
    WHERE phase <> 'completed';

CREATE INDEX worker_turn_settlements_expired_leases
    ON worker_turn_settlements (lease_expires_at, settlement_id)
    WHERE phase <> 'completed' AND lease_expires_at IS NOT NULL;

CREATE INDEX worker_turn_settlements_session_pending
    ON worker_turn_settlements (worker_session_id)
    WHERE phase <> 'completed';

-- migrate:down

DROP INDEX worker_turn_settlements_session_pending;
DROP INDEX worker_turn_settlements_expired_leases;
DROP INDEX worker_turn_settlements_pending;
DROP TABLE worker_turn_settlements;
