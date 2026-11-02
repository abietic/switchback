-- migrate:up

ALTER TABLE runtime_instances
    ADD COLUMN network_policy_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN network_policy_digest text NOT NULL DEFAULT '',
    ADD COLUMN network_enforcement_status text NOT NULL DEFAULT 'legacy_unenforced',
    ADD CONSTRAINT runtime_instances_network_profile_shape CHECK (
        (
            network_enforcement_status = 'active'
            AND network_policy_revision > 0
            AND network_policy_digest ~ '^sha256:[0-9a-f]{64}$'
        )
        OR
        (
            network_enforcement_status = 'legacy_unenforced'
            AND network_policy_revision = 0
            AND network_policy_digest = ''
        )
    );

-- migrate:down

ALTER TABLE runtime_instances
    DROP CONSTRAINT runtime_instances_network_profile_shape,
    DROP COLUMN network_enforcement_status,
    DROP COLUMN network_policy_digest,
    DROP COLUMN network_policy_revision;
