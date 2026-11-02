package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
)

type ProjectionJobStore struct{ pool *pgxpool.Pool }

func NewProjectionJobStore(pool *pgxpool.Pool) *ProjectionJobStore {
	return &ProjectionJobStore{pool: pool}
}

func (store *ProjectionJobStore) Activate(ctx context.Context, version string) error {
	if store == nil || store.pool == nil || version == "" {
		return fmt.Errorf("projection store and schema version are required")
	}
	return platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		_, err := tx.Exec(ctx, `WITH activated AS (
INSERT INTO observability_projection_activations(schema_version) VALUES ($1)
ON CONFLICT DO NOTHING RETURNING schema_version)
INSERT INTO observability_projection_activation_exclusions(schema_version,trace_stream_id,manifest_checksum)
SELECT activated.schema_version,manifest.trace_stream_id,manifest.manifest_checksum
FROM activated CROSS JOIN trace_manifests manifest`, version)
		if err != nil {
			return fmt.Errorf("activate projection schema: %w", err)
		}
		return nil
	})
}

func (store *ProjectionJobStore) Discover(ctx context.Context, version string, limit int) (int, error) {
	if store == nil || store.pool == nil || version == "" || limit <= 0 {
		return 0, fmt.Errorf("projection store, schema version and positive limit are required")
	}
	tag, err := store.pool.Exec(ctx, `INSERT INTO observability_projection_jobs(schema_version,trace_stream_id,manifest_checksum)
SELECT $1,manifest.trace_stream_id,manifest.manifest_checksum FROM trace_manifests manifest
JOIN observability_projection_activations activation ON activation.schema_version=$1
LEFT JOIN observability_projection_activation_exclusions excluded
 ON excluded.schema_version=$1 AND excluded.trace_stream_id=manifest.trace_stream_id
LEFT JOIN observability_projection_jobs job
 ON job.schema_version=$1 AND job.trace_stream_id=manifest.trace_stream_id
WHERE excluded.trace_stream_id IS NULL AND job.trace_stream_id IS NULL
ORDER BY manifest.trace_stream_id LIMIT $2 ON CONFLICT DO NOTHING`, version, limit)
	if err != nil {
		return 0, fmt.Errorf("discover projection sources: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (store *ProjectionJobStore) Claim(ctx context.Context, version string, lease time.Duration) (artifactapp.ProjectionJob, bool, error) {
	if store == nil || store.pool == nil || version == "" || lease <= 0 || lease > artifactapp.MaxProjectionLease {
		return artifactapp.ProjectionJob{}, false, fmt.Errorf("projection store, schema version and lease within the supported range are required")
	}
	var job artifactapp.ProjectionJob
	err := store.pool.QueryRow(ctx, `WITH candidate AS (
SELECT schema_version,trace_stream_id,manifest_checksum FROM observability_projection_jobs
WHERE schema_version=$1 AND ((status IN ('pending','retryable') AND next_attempt_at<=clock_timestamp())
 OR (status='running' AND lease_until<=clock_timestamp()))
ORDER BY next_attempt_at,created_at,trace_stream_id FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE observability_projection_jobs job SET status='running',attempts=job.attempts+1,
 lease_token=job.lease_token+1,lease_until=clock_timestamp()+$2::interval,updated_at=clock_timestamp()
FROM candidate WHERE job.schema_version=candidate.schema_version AND job.trace_stream_id=candidate.trace_stream_id
RETURNING job.trace_stream_id,job.manifest_checksum,job.schema_version,job.lease_token,job.attempts,
 job.status,job.next_attempt_at,job.lease_until`, version, lease.String()).Scan(
		&job.Source.TraceStreamID, &job.Source.ManifestChecksum, &job.SchemaVersion, &job.LeaseToken,
		&job.Attempts, &job.Status, &job.NextAttemptAt, &job.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.ProjectionJob{}, false, nil
	}
	if err != nil {
		return artifactapp.ProjectionJob{}, false, fmt.Errorf("claim projection job: %w", err)
	}
	return job, true, nil
}

func (store *ProjectionJobStore) Finish(ctx context.Context, job artifactapp.ProjectionJob, completion artifactapp.ProjectionCompletion) error {
	if completion.Status != "passed" && completion.Status != "retryable" && completion.Status != "blocked" {
		return fmt.Errorf("projection completion status is invalid")
	}
	next := any(completion.RetryAt)
	if completion.Status != "retryable" {
		next = time.Now().UTC()
	}
	tag, err := store.pool.Exec(ctx, `UPDATE observability_projection_jobs
SET status=$4,error_code=NULLIF($5,''),next_attempt_at=$6,lease_until=NULL,updated_at=clock_timestamp()
WHERE schema_version=$1 AND trace_stream_id=$2 AND manifest_checksum=$3 AND lease_token=$7
 AND status='running' AND lease_until > clock_timestamp()`, job.SchemaVersion, job.Source.TraceStreamID,
		job.Source.ManifestChecksum, completion.Status, completion.ErrorCode, next, job.LeaseToken)
	if err != nil {
		return fmt.Errorf("finish projection job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.ErrProjectionLeaseLost
	}
	return nil
}

func (store *ProjectionJobStore) RequestSource(ctx context.Context, version string, source artifactapp.ProjectionSourceRef) error {
	if version == "" || source.TraceStreamID == "" || source.ManifestChecksum == "" {
		return fmt.Errorf("projection source identity is required")
	}
	var checksum string
	if err := store.pool.QueryRow(ctx, `SELECT manifest_checksum FROM trace_manifests WHERE trace_stream_id=$1`, source.TraceStreamID).Scan(&checksum); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &artifactapp.ProjectionError{Code: "source_not_found", Permanent: true}
		}
		return fmt.Errorf("read requested projection source: %w", err)
	}
	if checksum != source.ManifestChecksum {
		return &artifactapp.ProjectionError{Code: "source_checksum_mismatch", Permanent: true}
	}
	tag, err := store.pool.Exec(ctx, `INSERT INTO observability_projection_jobs(schema_version,trace_stream_id,manifest_checksum)
SELECT $1,$2,$3 FROM observability_projection_activations WHERE schema_version=$1 ON CONFLICT DO NOTHING`, version, source.TraceStreamID, source.ManifestChecksum)
	if err != nil {
		return fmt.Errorf("request projection source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var existing string
		err := store.pool.QueryRow(ctx, `SELECT manifest_checksum FROM observability_projection_jobs WHERE schema_version=$1 AND trace_stream_id=$2`, version, source.TraceStreamID).Scan(&existing)
		if err == nil && existing != source.ManifestChecksum {
			return &artifactapp.ProjectionError{Code: "source_checksum_mismatch", Permanent: true}
		} else if errors.Is(err, pgx.ErrNoRows) {
			return &artifactapp.ProjectionError{Code: "projection_schema_inactive", Permanent: true}
		} else if err != nil {
			return &artifactapp.ProjectionError{Code: "projection_job_lookup_unavailable", Permanent: false}
		}
	}
	return nil
}

var _ artifactapp.ProjectionJobStore = (*ProjectionJobStore)(nil)
