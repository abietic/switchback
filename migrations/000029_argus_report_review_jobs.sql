-- migrate:up

CREATE TABLE argus_report_review_jobs (
    task_id text NOT NULL REFERENCES tasks (task_id),
    task_revision bigint NOT NULL CHECK (task_revision > 0),
    delivery_event_id text NOT NULL,
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    artifact_id text NOT NULL REFERENCES artifact_refs (artifact_id),
    artifact_sha256 text NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'),
    profile_sha256 text NOT NULL CHECK (profile_sha256 ~ '^[0-9a-f]{64}$'),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'recorded', 'superseded', 'failed')),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_until timestamptz,
    last_error_code text,
    PRIMARY KEY (task_id, task_revision),
    CHECK ((status = 'running') = (lease_until IS NOT NULL))
);

CREATE INDEX argus_report_review_jobs_active ON argus_report_review_jobs (available_at, task_id, task_revision)
    WHERE status IN ('pending', 'running');

-- migrate:down

DROP TABLE argus_report_review_jobs;
