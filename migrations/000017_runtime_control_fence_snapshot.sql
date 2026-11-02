-- migrate:up

ALTER TABLE runtime_control_channels
    ADD COLUMN fenced_at timestamptz,
    ADD COLUMN fence_revision bigint,
    ADD COLUMN fence_owner_fence bigint;

-- Before this migration a fenced channel could no longer accept journal
-- progress, so its current mutable values are still the original fence proof.
UPDATE runtime_control_channels
SET fenced_at = updated_at,
    fence_revision = revision,
    fence_owner_fence = owner_fence
WHERE status = 'fenced';

ALTER TABLE runtime_control_channels
    ADD CONSTRAINT runtime_control_channels_fence_snapshot_check
        CHECK (
            (
                status = 'fenced'
                AND fenced_at IS NOT NULL
                AND fence_revision IS NOT NULL
                AND fence_owner_fence IS NOT NULL
                AND fenced_at >= bound_at
                AND fenced_at <= updated_at
                AND fence_revision > 0
                AND fence_revision <= revision
                AND fence_owner_fence > 0
                AND fence_owner_fence = owner_fence
            )
            OR
            (
                status <> 'fenced'
                AND fenced_at IS NULL
                AND fence_revision IS NULL
                AND fence_owner_fence IS NULL
            )
        );

-- migrate:down

-- Once a fenced channel has accepted post-fence terminal evidence, the old
-- mutable revision/updated_at columns no longer identify the fence boundary.
-- Refuse an unsafe downgrade instead of silently changing proof semantics.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_control_channels
        WHERE status = 'fenced'
          AND (
              fence_revision IS DISTINCT FROM revision
              OR fenced_at IS DISTINCT FROM updated_at
              OR fence_owner_fence IS DISTINCT FROM owner_fence
          )
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade Runtime control fence snapshot after post-fence journal progress';
    END IF;
END
$$;

ALTER TABLE runtime_control_channels
    DROP CONSTRAINT runtime_control_channels_fence_snapshot_check,
    DROP COLUMN fence_owner_fence,
    DROP COLUMN fence_revision,
    DROP COLUMN fenced_at;
