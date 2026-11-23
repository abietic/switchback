-- migrate:up
-- A first-report deployment authorizes a single initial Worker. Keep that
-- commitment for the life of this deployment, including after Turn terminal.
CREATE TABLE first_report_capacity_reservations (
    permit_id text PRIMARY KEY REFERENCES session_dispatch_permits (permit_id),
    engine_id text NOT NULL UNIQUE CHECK (length(engine_id) BETWEEN 1 AND 128),
    task_id text NOT NULL REFERENCES tasks (task_id),
    task_revision bigint NOT NULL CHECK (task_revision > 0),
    resource_profile_id text NOT NULL,
    resource_profile_digest text NOT NULL,
    available_nano_cpus bigint NOT NULL CHECK (available_nano_cpus > 0),
    available_memory_bytes bigint NOT NULL CHECK (available_memory_bytes > 0),
    available_storage_bytes bigint NOT NULL CHECK (available_storage_bytes > 0),
    observed_at timestamptz NOT NULL,
    reserved_at timestamptz NOT NULL,
    CHECK (observed_at <= reserved_at)
);

-- migrate:down
-- A rollback must not clear a first-report commitment from an active Engine.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM first_report_capacity_reservations) THEN
        RAISE EXCEPTION 'retire the isolated first-report deployment before rolling back its capacity ledger';
    END IF;
END $$;
DROP TABLE first_report_capacity_reservations;
