-- migrate:up

CREATE TABLE delivery_reviews (
    review_run_id text PRIMARY KEY,
    task_id text NOT NULL REFERENCES tasks (task_id),
    worker_run_id text NOT NULL REFERENCES worker_runs (worker_run_id),
    artifact_id text NOT NULL REFERENCES artifact_refs (artifact_id),
    artifact_sha256 text NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (task_id, artifact_id)
);

-- migrate:down

DROP TABLE delivery_reviews;
