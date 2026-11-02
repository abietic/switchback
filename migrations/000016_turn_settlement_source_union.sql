-- migrate:up

ALTER TABLE worker_turn_settlements
    ADD COLUMN source_kind text,
    ADD COLUMN source_reason text,
    ADD COLUMN source_occurred_at timestamptz,
    ADD COLUMN source_ref text,
    ADD COLUMN source_digest text,
    ADD COLUMN source_state jsonb,
    ADD COLUMN source_state_digest text;

-- Before this migration every settlement was admitted from a durable
-- Runtime-control terminal event. Preserve that authority explicitly rather
-- than attempting to infer a new source from later phase state.
UPDATE worker_turn_settlements
SET source_kind = 'runtime_event',
    source_reason = 'runtime_terminal',
    source_occurred_at = accepted_at,
    source_ref = terminal_message_id,
    source_digest = terminal_envelope_digest,
    source_state = '{"phase":"terminal_observed","schema_version":1}'::jsonb,
    source_state_digest =
        'sha256:eade79e0a1ca190d82f879fee630b0a45261063eefac1d74cd68c417e861348f';

ALTER TABLE worker_turn_settlements
    ALTER COLUMN source_kind SET NOT NULL,
    ALTER COLUMN source_reason SET NOT NULL,
    ALTER COLUMN source_occurred_at SET NOT NULL,
    ALTER COLUMN source_ref SET NOT NULL,
    ALTER COLUMN source_digest SET NOT NULL,
    ALTER COLUMN source_state SET NOT NULL,
    ALTER COLUMN source_state_digest SET NOT NULL,
    ALTER COLUMN runtime_instance_id DROP NOT NULL,
    ALTER COLUMN runtime_generation DROP NOT NULL,
    ALTER COLUMN channel_epoch DROP NOT NULL,
    ALTER COLUMN terminal_event_direction DROP DEFAULT,
    ALTER COLUMN terminal_event_direction DROP NOT NULL,
    ALTER COLUMN terminal_event_sequence DROP NOT NULL,
    ALTER COLUMN terminal_message_id DROP NOT NULL,
    ALTER COLUMN terminal_idempotency_key DROP NOT NULL,
    ALTER COLUMN terminal_envelope_digest DROP NOT NULL,
    ADD CONSTRAINT worker_turn_settlements_source_kind_check
        CHECK (source_kind IN ('runtime_event', 'backend_deadline')),
    ADD CONSTRAINT worker_turn_settlements_source_reason_check
        CHECK (length(btrim(source_reason)) > 0),
    ADD CONSTRAINT worker_turn_settlements_source_ref_check
        CHECK (length(btrim(source_ref)) > 0),
    ADD CONSTRAINT worker_turn_settlements_source_digest_check
        CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT worker_turn_settlements_source_state_check
        CHECK (jsonb_typeof(source_state) = 'object'),
    ADD CONSTRAINT worker_turn_settlements_source_state_digest_check
        CHECK (source_state_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT worker_turn_settlements_event_witness_check
        CHECK (
            (
                runtime_instance_id IS NULL
                AND runtime_generation IS NULL
                AND channel_epoch IS NULL
                AND terminal_event_direction IS NULL
                AND terminal_event_sequence IS NULL
                AND terminal_message_id IS NULL
                AND terminal_idempotency_key IS NULL
                AND terminal_envelope_digest IS NULL
            )
            OR
            (
                runtime_instance_id IS NOT NULL
                AND runtime_generation IS NOT NULL
                AND channel_epoch IS NOT NULL
                AND terminal_event_direction IS NOT NULL
                AND terminal_event_sequence IS NOT NULL
                AND terminal_message_id IS NOT NULL
                AND terminal_idempotency_key IS NOT NULL
                AND terminal_envelope_digest IS NOT NULL
            )
        ),
    ADD CONSTRAINT worker_turn_settlements_source_union_check
        CHECK (
            (
                source_kind = 'runtime_event'
                AND runtime_instance_id IS NOT NULL
                AND runtime_generation IS NOT NULL
                AND channel_epoch IS NOT NULL
                AND terminal_event_direction IS NOT NULL
                AND terminal_event_sequence IS NOT NULL
                AND terminal_message_id IS NOT NULL
                AND terminal_idempotency_key IS NOT NULL
                AND terminal_envelope_digest IS NOT NULL
                AND source_ref = terminal_message_id
                AND source_digest = terminal_envelope_digest
            )
            OR source_kind = 'backend_deadline'
        );

-- A forced Backend deadline may own terminality while an exact late Runtime
-- event is applied only to unblock the ordered inbox. Preserve that event's
-- ignore disposition through source-applied and continuation phases without
-- granting the old Accepted -> Completed shortcut to the forced path.
ALTER TABLE worker_turn_settlements
    DROP CONSTRAINT worker_turn_settlements_check1,
    ADD CONSTRAINT worker_turn_settlements_phase_lifecycle_check
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
                phase IN (
                    'terminal_event_applied',
                    'continuation_settled'
                )
                AND (
                    terminal_event_disposition = 'commit'
                    OR (
                        source_kind = 'backend_deadline'
                        AND terminal_event_disposition = 'ignore'
                    )
                )
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
        );

ALTER TABLE runtime_recovery_incidents
    ADD COLUMN preempted_by_turn_settlement_id text
        REFERENCES worker_turn_settlements (settlement_id),
    ADD CONSTRAINT runtime_recovery_incidents_deadline_settlement_check
        CHECK (
            (
                preempted_by_turn_settlement_id IS NULL
                AND outcome_reason IS DISTINCT FROM
                    'preempted_by_backend_deadline'
            )
            OR
            (
                preempted_by_turn_settlement_id IS NOT NULL
                AND phase = 'exhausted'
                AND outcome = 'exhausted'
                AND outcome_reason = 'preempted_by_backend_deadline'
            )
        );

-- PostgreSQL keeps the existing nullable composite foreign key and unique
-- constraints active: a complete event witness remains referentially checked,
-- while a Backend deadline may carry no event witness at all.

-- migrate:down

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM worker_turn_settlements
        WHERE source_kind = 'backend_deadline'
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade Turn settlement source union with Backend deadline rows';
    END IF;
END
$$;

ALTER TABLE runtime_recovery_incidents
    DROP CONSTRAINT runtime_recovery_incidents_deadline_settlement_check,
    DROP COLUMN preempted_by_turn_settlement_id;

ALTER TABLE worker_turn_settlements
    DROP CONSTRAINT worker_turn_settlements_phase_lifecycle_check,
    DROP CONSTRAINT worker_turn_settlements_source_union_check,
    DROP CONSTRAINT worker_turn_settlements_event_witness_check,
    DROP CONSTRAINT worker_turn_settlements_source_state_digest_check,
    DROP CONSTRAINT worker_turn_settlements_source_state_check,
    DROP CONSTRAINT worker_turn_settlements_source_digest_check,
    DROP CONSTRAINT worker_turn_settlements_source_ref_check,
    DROP CONSTRAINT worker_turn_settlements_source_reason_check,
    DROP CONSTRAINT worker_turn_settlements_source_kind_check,
    ALTER COLUMN runtime_instance_id SET NOT NULL,
    ALTER COLUMN runtime_generation SET NOT NULL,
    ALTER COLUMN channel_epoch SET NOT NULL,
    ALTER COLUMN terminal_event_direction SET DEFAULT 'event',
    ALTER COLUMN terminal_event_direction SET NOT NULL,
    ALTER COLUMN terminal_event_sequence SET NOT NULL,
    ALTER COLUMN terminal_message_id SET NOT NULL,
    ALTER COLUMN terminal_idempotency_key SET NOT NULL,
    ALTER COLUMN terminal_envelope_digest SET NOT NULL,
    DROP COLUMN source_state_digest,
    DROP COLUMN source_state,
    DROP COLUMN source_digest,
    DROP COLUMN source_ref,
    DROP COLUMN source_occurred_at,
    DROP COLUMN source_reason,
    DROP COLUMN source_kind,
    ADD CONSTRAINT worker_turn_settlements_check1
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
                phase IN (
                    'terminal_event_applied',
                    'continuation_settled'
                )
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
        );
