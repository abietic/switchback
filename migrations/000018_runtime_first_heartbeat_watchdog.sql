-- migrate:up

CREATE TABLE runtime_first_heartbeat_watchdogs (
    runtime_instance_id text PRIMARY KEY REFERENCES runtime_control_channels (runtime_instance_id),
    tenant_id text NOT NULL,
    worker_session_id text NOT NULL REFERENCES worker_sessions (worker_session_id),
    runtime_generation integer NOT NULL,
    channel_epoch bigint NOT NULL,
    anchor_kind text NOT NULL,
    anchor_revision bigint NOT NULL,
    first_authenticated_resume_at timestamptz NOT NULL,
    suspect_due_at timestamptz NOT NULL,
    unhealthy_due_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'awaiting',
    suspect_at timestamptz,
    first_heartbeat_sequence bigint,
    first_heartbeat_control_sequence bigint,
    first_heartbeat_received_at timestamptz,
    unhealthy_at timestamptz,
    unhealthy_runtime_revision bigint,
    recovery_intent jsonb,
    terminal_reason text,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (worker_session_id, runtime_generation, channel_epoch),
    CHECK (
        runtime_generation > 0
        AND channel_epoch > 0
        AND anchor_revision > 0
    ),
    CHECK (anchor_kind IN ('resume_complete_v1', 'legacy_runtime_started_v1')),
    CHECK (status IN ('awaiting', 'suspect', 'satisfied', 'unhealthy', 'cancelled')),
    CHECK (first_heartbeat_sequence IS NULL OR first_heartbeat_sequence > 0),
    CHECK (
        (first_heartbeat_sequence IS NULL)
        = (first_heartbeat_control_sequence IS NULL)
        AND (first_heartbeat_sequence IS NULL)
        = (first_heartbeat_received_at IS NULL)
    ),
    CHECK (
        first_heartbeat_control_sequence IS NULL
        OR first_heartbeat_control_sequence > 0
    ),
    CHECK (
        terminal_reason IS NULL
        OR terminal_reason IN (
            'replacement_first_heartbeat_timeout',
            'backend_deadline_owned',
            'turn_outcome_owned',
            'runtime_not_live'
        )
    ),
    CHECK (
        suspect_due_at = first_authenticated_resume_at + INTERVAL '45 seconds'
        AND unhealthy_due_at = first_authenticated_resume_at + INTERVAL '90 seconds'
    ),
    CHECK (
        revision > 0
        AND updated_at >= created_at
    ),
    CHECK (
        (
            status = 'awaiting'
            AND suspect_at IS NULL
            AND first_heartbeat_sequence IS NULL
            AND first_heartbeat_control_sequence IS NULL
            AND unhealthy_at IS NULL
            AND unhealthy_runtime_revision IS NULL
            AND recovery_intent IS NULL
            AND terminal_reason IS NULL
        )
        OR
        (
            status = 'suspect'
            AND suspect_at IS NOT NULL
            AND suspect_at >= suspect_due_at
            AND suspect_at < unhealthy_due_at
            AND first_heartbeat_sequence IS NULL
            AND first_heartbeat_control_sequence IS NULL
            AND unhealthy_at IS NULL
            AND unhealthy_runtime_revision IS NULL
            AND recovery_intent IS NULL
            AND terminal_reason IS NULL
        )
        OR
        (
            status = 'satisfied'
            AND first_heartbeat_sequence IS NOT NULL
            AND first_heartbeat_sequence > 0
            AND first_heartbeat_control_sequence IS NOT NULL
            AND first_heartbeat_control_sequence > 0
            AND first_heartbeat_received_at IS NOT NULL
            AND first_heartbeat_received_at < unhealthy_due_at
            AND unhealthy_at IS NULL
            AND unhealthy_runtime_revision IS NULL
            AND recovery_intent IS NULL
            AND terminal_reason IS NULL
        )
        OR
        (
            status = 'unhealthy'
            AND first_heartbeat_sequence IS NULL
            AND first_heartbeat_control_sequence IS NULL
            AND unhealthy_at IS NOT NULL
            AND unhealthy_at >= unhealthy_due_at
            AND unhealthy_runtime_revision IS NOT NULL
            AND unhealthy_runtime_revision > 0
            AND (
                (
                    recovery_intent IS NOT NULL
                    AND jsonb_typeof(recovery_intent) = 'object'
                    AND terminal_reason IS NULL
                )
                OR
                (
                    recovery_intent IS NULL
                    AND terminal_reason = 'replacement_first_heartbeat_timeout'
                )
            )
            AND (
                suspect_at IS NULL
                OR (
                    suspect_at >= suspect_due_at
                    AND suspect_at < unhealthy_due_at
                    AND suspect_at <= unhealthy_at
                )
            )
        )
        OR
        (
            status = 'cancelled'
            AND first_heartbeat_sequence IS NULL
            AND first_heartbeat_control_sequence IS NULL
            AND first_heartbeat_received_at IS NULL
            AND unhealthy_at IS NULL
            AND unhealthy_runtime_revision IS NULL
            AND recovery_intent IS NULL
            AND terminal_reason IN (
                'backend_deadline_owned',
                'turn_outcome_owned',
                'runtime_not_live'
            )
            AND (
                suspect_at IS NULL
                OR (
                    suspect_at >= suspect_due_at
                    AND suspect_at < unhealthy_due_at
                )
            )
        )
    )
);

-- Upgrade compatibility for generations already represented in the durable
-- Runtime control journal. A pre-000014 Runtime with no control-channel row
-- has no authenticated Resume proof and deliberately remains on the explicit
-- startup legacy-anchor fallback instead of fabricating one here.
-- An existing real heartbeat already proves liveness. For a bound live
-- generation without one, preserve the first completed Resume when available
-- and otherwise use an explicitly-labelled legacy Runtime anchor.
INSERT INTO runtime_first_heartbeat_watchdogs
  (runtime_instance_id,tenant_id,worker_session_id,runtime_generation,
   channel_epoch,anchor_kind,anchor_revision,
   first_authenticated_resume_at,suspect_due_at,unhealthy_due_at,status,
   revision,created_at,updated_at)
SELECT runtime.runtime_instance_id,
       session.tenant_id,
       session.worker_session_id,
       runtime.generation,
       runtime.control_channel_epoch,
       CASE
         WHEN channel.resume_completed_at IS NOT NULL
           THEN 'resume_complete_v1'
         ELSE 'legacy_runtime_started_v1'
       END,
       COALESCE(channel.resume_completed_revision,runtime.revision),
       anchor.at,
       anchor.at + INTERVAL '45 seconds',
       anchor.at + INTERVAL '90 seconds',
       'awaiting',
       1,
       CURRENT_TIMESTAMP,
       CURRENT_TIMESTAMP
FROM runtime_instances runtime
JOIN worker_sessions session
  ON session.worker_session_id=runtime.worker_session_id
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=runtime.runtime_instance_id
 AND channel.worker_session_id=runtime.worker_session_id
 AND channel.runtime_generation=runtime.generation
 AND channel.channel_epoch=runtime.control_channel_epoch
CROSS JOIN LATERAL (
  SELECT COALESCE(
      channel.resume_completed_at,
      runtime.started_at,
      runtime.created_at
  ) AS at
) anchor
LEFT JOIN runtime_heartbeat_observations heartbeat
  ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
 AND heartbeat.worker_session_id=runtime.worker_session_id
 AND heartbeat.runtime_generation=runtime.generation
 AND heartbeat.channel_epoch=runtime.control_channel_epoch
WHERE heartbeat.runtime_instance_id IS NULL
  AND session.status IN (
      'interactive',
      'running',
      'recovering',
      'waiting_user',
      'idle'
  )
  AND runtime.status IN ('starting', 'active', 'suspect');

INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,
   expected_revision,payload,due_at,status,idempotency_key,created_at,updated_at)
SELECT 'runtime-first-heartbeat/' || watchdog.runtime_instance_id || '/' ||
           watchdog.runtime_generation || '/' || watchdog.channel_epoch ||
           '/suspect',
       watchdog.tenant_id,
       'runtime_first_heartbeat_evaluate',
       'RuntimeFirstHeartbeatWatchdog',
       watchdog.runtime_instance_id,
       watchdog.revision,
       jsonb_build_object(
           'runtime_generation', watchdog.runtime_generation,
           'channel_epoch', watchdog.channel_epoch,
           'anchor_kind', watchdog.anchor_kind,
           'anchor_revision', watchdog.anchor_revision,
           'stage', 'suspect'
       ),
       watchdog.suspect_due_at,
       'pending',
       'first-heartbeat/' || watchdog.runtime_generation || '/' ||
           watchdog.channel_epoch || '/suspect',
       CURRENT_TIMESTAMP,
       CURRENT_TIMESTAMP
FROM runtime_first_heartbeat_watchdogs watchdog;

CREATE INDEX runtime_first_heartbeat_watchdogs_outstanding
    ON runtime_first_heartbeat_watchdogs (
        suspect_due_at,
        unhealthy_due_at,
        runtime_instance_id
    )
    WHERE status IN ('awaiting', 'suspect');

CREATE FUNCTION runtime_first_heartbeat_watchdogs_guard_immutable_proof()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF ROW(
        NEW.runtime_instance_id,
        NEW.tenant_id,
        NEW.worker_session_id,
        NEW.runtime_generation,
        NEW.channel_epoch,
        NEW.anchor_kind,
        NEW.anchor_revision,
        NEW.first_authenticated_resume_at,
        NEW.suspect_due_at,
        NEW.unhealthy_due_at,
        NEW.created_at
    ) IS DISTINCT FROM ROW(
        OLD.runtime_instance_id,
        OLD.tenant_id,
        OLD.worker_session_id,
        OLD.runtime_generation,
        OLD.channel_epoch,
        OLD.anchor_kind,
        OLD.anchor_revision,
        OLD.first_authenticated_resume_at,
        OLD.suspect_due_at,
        OLD.unhealthy_due_at,
        OLD.created_at
    ) THEN
        RAISE EXCEPTION
            'First-heartbeat watchdog identity and deadline anchor are immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF (
        OLD.suspect_at IS NOT NULL
        AND NEW.suspect_at IS DISTINCT FROM OLD.suspect_at
    ) OR (
        OLD.first_heartbeat_sequence IS NOT NULL
        AND (
            NEW.first_heartbeat_sequence IS DISTINCT FROM OLD.first_heartbeat_sequence
            OR NEW.first_heartbeat_control_sequence IS DISTINCT FROM
                OLD.first_heartbeat_control_sequence
            OR NEW.first_heartbeat_received_at IS DISTINCT FROM OLD.first_heartbeat_received_at
        )
    ) OR (
        OLD.unhealthy_at IS NOT NULL
        AND (
            NEW.unhealthy_at IS DISTINCT FROM OLD.unhealthy_at
            OR NEW.unhealthy_runtime_revision IS DISTINCT FROM OLD.unhealthy_runtime_revision
            OR NEW.recovery_intent IS DISTINCT FROM OLD.recovery_intent
            OR NEW.terminal_reason IS DISTINCT FROM OLD.terminal_reason
        )
    ) OR (
        OLD.status = 'cancelled'
        AND NEW.terminal_reason IS DISTINCT FROM OLD.terminal_reason
    ) THEN
        RAISE EXCEPTION
            'First-heartbeat watchdog transition proof is immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    IF OLD.status IN ('satisfied', 'unhealthy', 'cancelled')
        AND NEW.status IS DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION
            'First-heartbeat watchdog terminal status is immutable'
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END
$$;

CREATE TRIGGER runtime_first_heartbeat_watchdogs_immutable_proof
BEFORE UPDATE ON runtime_first_heartbeat_watchdogs
FOR EACH ROW
EXECUTE FUNCTION runtime_first_heartbeat_watchdogs_guard_immutable_proof();

-- migrate:down

DELETE FROM scheduled_commands
WHERE command_type = 'runtime_first_heartbeat_evaluate';

DROP TRIGGER runtime_first_heartbeat_watchdogs_immutable_proof
    ON runtime_first_heartbeat_watchdogs;
DROP FUNCTION runtime_first_heartbeat_watchdogs_guard_immutable_proof();
DROP INDEX runtime_first_heartbeat_watchdogs_outstanding;
DROP TABLE runtime_first_heartbeat_watchdogs;
