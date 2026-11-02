package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ObservationLedger struct{ pool *pgxpool.Pool }

func NewObservationLedger(pool *pgxpool.Pool) *ObservationLedger {
	return &ObservationLedger{pool: pool}
}

// observationCandidateRecord is prepared, private persistence data. The owning
// journal/checkpoint participant must lock and validate durable provenance before
// calling insertObservationCandidate; this record does not grant acceptance.
type observationCandidateRecord struct {
	Source         artifactapp.FragmentRef
	Frame, Receipt []byte
	Scope          artifactdomain.ObservationScope
	Provenance     []byte
	ErrorCode      string
}

// insertObservationCandidate participates in the owner's transaction. A conflict
// is an outcome, not a transaction error: the accepting participant returns nil
// so the journal/cursor and digest-only audit commit together. Only an independent
// caller may classify ErrFragmentSourceConflict after its transaction commits.
// Exact replay never rewrites
// the original frame, receipt, scope, provenance or work state.
func insertObservationCandidate(ctx context.Context, tx pgx.Tx, c observationCandidateRecord) (conflict bool, err error) {
	if tx == nil {
		return false, fmt.Errorf("candidate transaction is required")
	}
	status := "pending"
	if c.ErrorCode != "" {
		if len(c.Frame) != 0 || len(c.Receipt) != 0 {
			return false, fmt.Errorf("blocked candidate must have no content")
		}
		c.Frame = nil
		c.Receipt = nil
		status = "blocked"
	} else if len(c.Frame) == 0 || len(c.Receipt) == 0 || c.Source.FrameDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(c.Frame)) {
		return false, fmt.Errorf("candidate content digest is invalid")
	}
	var scopeOK bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_runs r JOIN worker_sessions s USING(worker_session_id)
 WHERE r.worker_run_id=$1 AND s.worker_session_id=$2 AND s.tenant_id=$3 AND s.user_id=$4 AND s.workspace_id=$5)`, c.Scope.WorkerRunID, c.Scope.WorkerSessionID, c.Scope.TenantID, c.Scope.UserID, c.Scope.WorkspaceID).Scan(&scopeOK)
	if err != nil {
		return false, err
	}
	if !scopeOK {
		return false, fmt.Errorf("candidate authoritative scope mismatch")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO observation_candidates(kind,source_id,frame_digest,frame,receipt,
 tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,status,error_code)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,'')) ON CONFLICT(kind,source_id) DO NOTHING`, c.Source.Kind, c.Source.SourceID, c.Source.FrameDigest, c.Frame, c.Receipt, c.Scope.TenantID, c.Scope.UserID, c.Scope.WorkspaceID, c.Scope.WorkerSessionID, c.Scope.WorkerRunID, c.Provenance, status, c.ErrorCode)
	if err != nil || tag.RowsAffected() == 1 {
		return false, err
	}
	var same bool
	var original string
	err = tx.QueryRow(ctx, `SELECT frame_digest,
 frame_digest=$3 AND frame IS NOT DISTINCT FROM $4::bytea AND receipt IS NOT DISTINCT FROM $5::bytea
 AND tenant_id=$6 AND user_id=$7 AND workspace_id=$8 AND worker_session_id=$9 AND worker_run_id=$10 AND provenance=$11::jsonb
 FROM observation_candidates WHERE kind=$1 AND source_id=$2`, c.Source.Kind, c.Source.SourceID, c.Source.FrameDigest, c.Frame, c.Receipt, c.Scope.TenantID, c.Scope.UserID, c.Scope.WorkspaceID, c.Scope.WorkerSessionID, c.Scope.WorkerRunID, c.Provenance).Scan(&original, &same)
	if err != nil || same {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observation_fragment_conflicts(kind,source_id,original_digest,conflicting_digest)
 VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.Source.Kind, c.Source.SourceID, original, c.Source.FrameDigest)
	return err == nil, err
}

func (s *ObservationLedger) ClaimCandidate(ctx context.Context, lease time.Duration) (artifactapp.FragmentCandidate, bool, error) {
	if s == nil || s.pool == nil || lease <= 0 || lease > artifactapp.MaxProjectionLease {
		return artifactapp.FragmentCandidate{}, false, fmt.Errorf("candidate store and valid lease are required")
	}
	var c artifactapp.FragmentCandidate
	err := s.pool.QueryRow(ctx, `WITH next AS (
 SELECT kind,source_id FROM observation_candidates WHERE
 ((status IN ('pending','retryable') AND next_attempt_at<=clock_timestamp()) OR (status='running' AND lease_until<=clock_timestamp()))
 ORDER BY next_attempt_at,source_id,kind FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE observation_candidates c SET status='running',attempts=c.attempts+1,lease_token=c.lease_token+1,lease_until=clock_timestamp()+$1::interval
 FROM next WHERE (c.kind,c.source_id)=(next.kind,next.source_id)
 RETURNING c.kind,c.source_id,c.frame_digest,c.frame,c.receipt,c.lease_token,c.lease_until`, lease.String()).Scan(&c.Source.Kind, &c.Source.SourceID, &c.Source.FrameDigest, &c.Frame, &c.Receipt, &c.LeaseToken, &c.LeaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.FragmentCandidate{}, false, nil
	}
	return c, err == nil, err
}

func (s *ObservationLedger) Publish(ctx context.Context, c artifactapp.FragmentCandidate, o workerapp.StoredObject) (artifactapp.FragmentRef, error) {
	if s == nil || s.pool == nil || o.Key == "" || o.Size != int64(len(c.Frame)) || o.Checksum != c.Source.FrameDigest || o.Checksum != fmt.Sprintf("sha256:%x", sha256.Sum256(c.Frame)) {
		return artifactapp.FragmentRef{}, fmt.Errorf("fragment object binding is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return artifactapp.FragmentRef{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM observation_candidates WHERE kind=$1 AND source_id=$2 AND frame_digest=$3
 AND frame=$4 AND receipt=$5 AND lease_token=$6 AND status='running' AND lease_until>clock_timestamp() FOR UPDATE`, c.Source.Kind, c.Source.SourceID, c.Source.FrameDigest, c.Frame, c.Receipt, c.LeaseToken).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.FragmentRef{}, artifactapp.ErrFragmentPublicationLeaseLost
	}
	if err != nil {
		return artifactapp.FragmentRef{}, err
	}
	// Recheck after waiting for a row lock, before historical reads or writes.
	if err := tx.QueryRow(ctx, `SELECT lease_until>clock_timestamp() FROM observation_candidates WHERE kind=$1 AND source_id=$2`, c.Source.Kind, c.Source.SourceID).Scan(&exists); err != nil {
		return artifactapp.FragmentRef{}, err
	}
	if !exists {
		return artifactapp.FragmentRef{}, artifactapp.ErrFragmentPublicationLeaseLost
	}
	evidence, err := readFragmentEvidence(ctx, tx, c.Source)
	if err != nil {
		return artifactapp.FragmentRef{}, err
	}
	if c.Evidence == nil || !reflect.DeepEqual(evidence, *c.Evidence) {
		return artifactapp.FragmentRef{}, fragmentProofError()
	}
	f, err := artifactdomain.DecodeObservationFragment(c.Frame)
	if err != nil || o.Key != fragmentObjectKey(f, c.Source.FrameDigest) {
		return artifactapp.FragmentRef{}, fragmentProofError()
	}
	_, err = tx.Exec(ctx, `INSERT INTO evidence_object_refs(object_key,trace_stream_id,tenant_id,user_id,workspace_id,object_kind,object_checksum,size_bytes,visible_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp()) ON CONFLICT(object_key) DO NOTHING`, o.Key, evidence.Core.Manifest.TraceStreamID, f.Scope.TenantID, f.Scope.UserID, f.Scope.WorkspaceID, artifactapp.ObservationFragmentObjectKind, o.Checksum, o.Size)
	if err != nil {
		return artifactapp.FragmentRef{}, err
	}
	if err := checkFragmentObjectRef(ctx, tx, evidence, o); err != nil {
		return artifactapp.FragmentRef{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observation_publications(kind,source_id,frame_digest,object_key,object_size,object_checksum,
 tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
 SELECT kind,source_id,frame_digest,$4,$5,$6,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id
 FROM observation_candidates WHERE kind=$1 AND source_id=$2 AND frame_digest=$3`, c.Source.Kind, c.Source.SourceID, c.Source.FrameDigest, o.Key, o.Size, o.Checksum)
	if err != nil {
		return artifactapp.FragmentRef{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE observation_candidates SET status='published',lease_until=NULL,error_code=NULL
 WHERE kind=$1 AND source_id=$2 AND frame_digest=$3 AND lease_token=$4 AND status='running' AND lease_until>clock_timestamp()`, c.Source.Kind, c.Source.SourceID, c.Source.FrameDigest, c.LeaseToken)
	if err != nil {
		return artifactapp.FragmentRef{}, err
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.FragmentRef{}, artifactapp.ErrFragmentPublicationLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactapp.FragmentRef{}, err
	}
	return c.Source, nil
}

func (s *ObservationLedger) DeferCandidate(ctx context.Context, c artifactapp.FragmentCandidate, code string, permanent bool) error {
	if s == nil || s.pool == nil || code == "" {
		return fmt.Errorf("candidate store and safe error code are required")
	}
	status := "retryable"
	if permanent {
		status = "blocked"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM observation_candidates
 WHERE kind=$1 AND source_id=$2 FOR UPDATE`, c.Source.Kind, c.Source.SourceID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return artifactapp.ErrFragmentPublicationLeaseLost
	}
	if err != nil {
		return err
	}
	// Recheck the full claim and database clock after any row-lock wait; an
	// UPDATE predicate alone may have been evaluated before a lock-only holder.
	tag, err := tx.Exec(ctx, `UPDATE observation_candidates SET status=$7,error_code=$8,lease_until=NULL,
 next_attempt_at=clock_timestamp()+least(interval '5 minutes',interval '5 seconds'*power(2,least(attempts-1,6)))
 WHERE kind=$1 AND source_id=$2 AND frame_digest=$3 AND frame=$4 AND receipt=$5 AND lease_token=$6
 AND status='running' AND lease_until>clock_timestamp()`, c.Source.Kind, c.Source.SourceID, c.Source.FrameDigest, c.Frame, c.Receipt, c.LeaseToken, status, code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return artifactapp.ErrFragmentPublicationLeaseLost
	}
	return tx.Commit(ctx)
}

var _ artifactapp.FragmentPublicationStore = (*ObservationLedger)(nil)
