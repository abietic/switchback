package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/jackc/pgx/v5"
)

func (s *FragmentJobStore) Discover(ctx context.Context, version string, limit int) (int, error) {
	if s == nil || s.pool == nil || version == "" || limit <= 0 {
		return 0, fmt.Errorf("fragment store, version and positive limit are required")
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO observation_fragment_jobs(version,kind,source_id,frame_digest)
 SELECT $1,p.kind,p.source_id,p.frame_digest FROM observation_publications p
 WHERE ((EXISTS (SELECT 1 FROM observation_fragment_activations a WHERE a.version=$1)
 AND NOT EXISTS (SELECT 1 FROM observation_fragment_run_exclusions e WHERE e.version=$1 AND e.worker_run_id=p.worker_run_id))
 OR EXISTS (SELECT 1 FROM observation_fragment_requests r, jsonb_array_elements(r.source_set) selected
 WHERE r.version=$1 AND r.tenant_id=p.tenant_id AND r.actor_user_id=p.user_id
 AND selected->>'kind'=p.kind AND selected->>'source_id'=p.source_id AND selected->>'frame_digest'=p.frame_digest))
 AND NOT EXISTS (SELECT 1 FROM observation_fragment_jobs j WHERE j.version=$1 AND j.kind=p.kind AND j.source_id=p.source_id)
 ORDER BY p.kind,p.source_id LIMIT $2 ON CONFLICT DO NOTHING`, version, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *FragmentJobStore) Claim(ctx context.Context, version string, lease time.Duration) (artifactapp.FragmentJob, bool, error) {
	if s == nil || s.pool == nil || version == "" || lease <= 0 || lease > artifactapp.MaxProjectionLease {
		return artifactapp.FragmentJob{}, false, fmt.Errorf("fragment store, version and valid lease are required")
	}
	var j artifactapp.FragmentJob
	err := s.pool.QueryRow(ctx, `WITH next AS (
 SELECT version,kind,source_id FROM observation_fragment_jobs WHERE version=$1 AND
 ((status IN ('pending','retryable') AND next_attempt_at<=clock_timestamp()) OR (status='running' AND lease_until<=clock_timestamp()))
 ORDER BY next_attempt_at,source_id,kind FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE observation_fragment_jobs job SET status='running',attempts=job.attempts+1,lease_token=job.lease_token+1,
 lease_until=clock_timestamp()+$2::interval FROM next
 WHERE (job.version,job.kind,job.source_id)=(next.version,next.kind,next.source_id)
 RETURNING job.kind,job.source_id,job.frame_digest,job.version,job.lease_token,job.attempts,job.lease_until`, version, lease.String()).Scan(&j.Source.Kind, &j.Source.SourceID, &j.Source.FrameDigest, &j.Version, &j.LeaseToken, &j.Attempts, &j.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.FragmentJob{}, false, nil
	}
	return j, err == nil, err
}

func (s *FragmentJobStore) Finish(ctx context.Context, j artifactapp.FragmentJob, c artifactapp.ProjectionCompletion) error {
	if s == nil || s.pool == nil || (c.Status != "passed" && c.Status != "retryable" && c.Status != "blocked") {
		return fmt.Errorf("fragment store and valid completion are required")
	}
	if c.Status == "passed" && c.ErrorCode != "" || c.Status != "passed" && c.ErrorCode == "" {
		return fmt.Errorf("fragment completion code is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM observation_fragment_jobs
 WHERE version=$1 AND kind=$2 AND source_id=$3 FOR UPDATE`, j.Version, j.Source.Kind, j.Source.SourceID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.ErrProjectionLeaseLost
	}
	if err != nil {
		return err
	}
	// A lock-only holder can delay UPDATE after its WHERE was evaluated. Acquire
	// the row first, then recheck the full claim and database clock in a new statement.
	tag, err := tx.Exec(ctx, `UPDATE observation_fragment_jobs SET status=$6,error_code=NULLIF($7,''),
 next_attempt_at=CASE WHEN $6='retryable' THEN $8 ELSE clock_timestamp() END,lease_until=NULL
 WHERE version=$1 AND kind=$2 AND source_id=$3 AND frame_digest=$4 AND lease_token=$5
 AND status='running' AND lease_until>clock_timestamp()`, j.Version, j.Source.Kind, j.Source.SourceID, j.Source.FrameDigest, j.LeaseToken, c.Status, c.ErrorCode, c.RetryAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.ErrProjectionLeaseLost
	}
	return tx.Commit(ctx)
}

var _ artifactapp.FragmentJobStore = (*FragmentJobStore)(nil)
