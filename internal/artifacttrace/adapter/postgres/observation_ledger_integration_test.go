//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFragmentLedgerRejectsConflictingDigest(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	record := seedObservationRun(t, pool)
	persistObservation(t, pool, record, false)
	persistObservation(t, pool, record, false)
	changed := record
	changed.Frame = []byte(`{"different":true}`)
	changed.Source.FrameDigest = fragmentTestDigest(changed.Frame)
	persistObservation(t, pool, changed, true)
	var frame, receipt []byte
	var digest, status string
	var count int
	if err := pool.QueryRow(ctx, `SELECT frame,receipt,frame_digest,status FROM observation_candidates`).Scan(&frame, &receipt, &digest, &status); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, record.Frame) || !bytes.Equal(receipt, record.Receipt) || digest != record.Source.FrameDigest || status != "pending" {
		t.Fatal("conflict changed original bytes or state")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("candidates %d %v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observation_fragment_conflicts WHERE source_id=$1 AND original_digest=$2 AND conflicting_digest=$3`, record.Source.SourceID, record.Source.FrameDigest, changed.Source.FrameDigest).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conflicts %d %v", count, err)
	}
}

func TestFragmentPublicationRejectsReceiptWithoutCommittedCore(t *testing.T) {
	pool := postgrestest.Open(t)
	record := seedObservationRun(t, pool)
	persistObservation(t, pool, record, false)
	store := NewObservationLedger(pool)
	c, ok, err := store.ClaimCandidate(context.Background(), 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	_, err = store.Publish(context.Background(), c, workerapp.StoredObject{Key: "fragment-object", Size: int64(len(c.Frame)), Checksum: c.Source.FrameDigest})
	if err == nil {
		t.Fatal("receipt without historical authority/core publication accepted")
	}
}

func TestFragmentLedgerPublicationRequiresLiveExactLease(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	record := seedObservationRun(t, pool)
	persistObservation(t, pool, record, false)
	store := NewObservationLedger(pool)
	c, ok, err := store.ClaimCandidate(ctx, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	object := workerapp.StoredObject{Key: "fragment-object", Size: int64(len(c.Frame)), Checksum: c.Source.FrameDigest}
	if _, err := pool.Exec(ctx, `UPDATE observation_candidates SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(ctx, c, object); !errors.Is(err, artifactapp.ErrFragmentPublicationLeaseLost) {
		t.Fatalf("expired publish %v", err)
	}
	if err := store.DeferCandidate(ctx, c, "object_store_unavailable", false); !errors.Is(err, artifactapp.ErrFragmentPublicationLeaseLost) {
		t.Fatalf("expired defer %v", err)
	}
	next, ok, err := store.ClaimCandidate(ctx, time.Minute)
	if err != nil || !ok || next.LeaseToken <= c.LeaseToken {
		t.Fatalf("reclaim %#v %v %v", next, ok, err)
	}
	if _, err := store.Publish(ctx, c, object); !errors.Is(err, artifactapp.ErrFragmentPublicationLeaseLost) {
		t.Fatalf("stale publish %v", err)
	}
	// This narrow ledger fixture has no first-freeze/core authority. A live
	// lease alone must now be rejected; real publication is covered separately.
	if _, err := store.Publish(ctx, next, object); err == nil {
		t.Fatal("live lease replaced missing core proof")
	}
	var status string
	var count int
	if err := pool.QueryRow(ctx, `SELECT status FROM observation_candidates`).Scan(&status); err != nil || status != "running" {
		t.Fatalf("status %s %v", status, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observation_publications`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("publication %d %v", count, err)
	}
}

func TestObservationLedgerRejectCompletionExpiredDuringRowLockWait(t *testing.T) {
	for _, mode := range []string{"defer_retryable", "defer_blocked", "publish_control"} {
		t.Run(mode, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			record := seedObservationRun(t, pool)
			persistObservation(t, pool, record, false)
			store := NewObservationLedger(pool)
			candidate, ok, err := store.ClaimCandidate(ctx, time.Second)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			snapshot := func() string {
				var rows string
				if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(c) FROM observation_candidates c),
 (SELECT jsonb_agg(p) FROM observation_publications p))::text`).Scan(&rows); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			before := snapshot()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var holderPID int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM observation_candidates
 WHERE kind=$1 AND source_id=$2 FOR UPDATE`, candidate.Source.Kind, candidate.Source.SourceID).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				if mode == "publish_control" {
					_, err := store.Publish(ctx, candidate, workerapp.StoredObject{Key: "fragment-object", Size: int64(len(candidate.Frame)), Checksum: candidate.Source.FrameDigest})
					result <- err
					return
				}
				result <- store.DeferCandidate(ctx, candidate, "object_store_unavailable", mode == "defer_blocked")
			}()
			waitObservationLeaseBarrier(t, ctx, pool, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, holderPID)
			var live bool
			if err := pool.QueryRow(ctx, `SELECT clock_timestamp()<$1`, candidate.LeaseUntil).Scan(&live); err != nil || !live {
				t.Fatalf("completion must enter lock wait before expiry: live=%v err=%v", live, err)
			}
			waitObservationLeaseBarrier(t, ctx, pool, `SELECT clock_timestamp()>=$1`, candidate.LeaseUntil)
			// No tuple update occurs in the holder transaction.
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, artifactapp.ErrFragmentPublicationLeaseLost) {
					t.Errorf("completion after lease expiry returned %v, want lease lost", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if after := snapshot(); after != before {
				t.Fatal("expired completion changed candidate state/evidence or committed a publication")
			}
		})
	}
}

func waitObservationLeaseBarrier(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arg any) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var ready bool
		if err := pool.QueryRow(ctx, query, arg).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
		// Poll observable database state; elapsed time alone never opens the barrier.
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestObservationLedgerGuardsImmutableEvidence(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	r := seedObservationRun(t, pool)
	persistObservation(t, pool, r, false)
	for _, sql := range []string{
		`UPDATE observation_candidates SET frame='changed'::bytea`,
		`UPDATE observation_candidates SET receipt='changed'::bytea`,
		`UPDATE observation_candidates SET frame_digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'`,
		`UPDATE observation_candidates SET user_id='other'`,
		`UPDATE observation_candidates SET provenance='{"schema_version":"observation.provenance.v1","other":true}'`,
		`DELETE FROM observation_candidates`,
		`UPDATE observation_candidates SET status='published'`,
	} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatalf("unsafe mutation accepted: %s", sql)
		}
	}
	store := NewObservationLedger(pool)
	c, ok, err := store.ClaimCandidate(ctx, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if _, err := store.Publish(ctx, c, workerapp.StoredObject{Key: "object", Size: int64(len(c.Frame)), Checksum: c.Source.FrameDigest}); err == nil {
		t.Fatal("synthetic receipt was treated as core publication proof")
	}
	// Published-row immutability now uses the real first-freeze/finalizer fixture
	// in TestFragmentPublicationActualCoreBeforeCheckpointPublication.
}

func TestObservationLedgerCandidateRetryBlockingAndInputValidation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	r := seedObservationRun(t, pool)
	persistObservation(t, pool, r, false)
	store := NewObservationLedger(pool)
	for _, lease := range []time.Duration{0, -time.Second, artifactapp.MaxProjectionLease + 1} {
		if _, _, err := store.ClaimCandidate(ctx, lease); err == nil {
			t.Fatalf("invalid lease %v accepted", lease)
		}
	}
	var wg sync.WaitGroup
	claims := make(chan artifactapp.FragmentCandidate, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, ok, err := store.ClaimCandidate(ctx, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims <- c
			}
		}()
	}
	wg.Wait()
	close(claims)
	var first artifactapp.FragmentCandidate
	var count int
	for c := range claims {
		first = c
		count++
	}
	if count != 1 {
		t.Fatalf("concurrent claims %d", count)
	}
	forged := first
	forged.Source.SourceID = "another-source"
	if err := store.DeferCandidate(ctx, forged, "object_store_unavailable", true); !errors.Is(err, artifactapp.ErrFragmentPublicationLeaseLost) {
		t.Fatalf("forged defer %v", err)
	}
	forged = first
	forged.Source.FrameDigest = fragmentTestDigest([]byte("other"))
	if err := store.DeferCandidate(ctx, forged, "object_store_unavailable", true); !errors.Is(err, artifactapp.ErrFragmentPublicationLeaseLost) {
		t.Fatalf("wrong digest defer %v", err)
	}
	if err := store.DeferCandidate(ctx, first, "untrusted provider body", false); err == nil || strings.Contains(err.Error(), "untrusted provider body") {
		t.Fatalf("untrusted code persisted or echoed: %v", err)
	}
	if err := store.DeferCandidate(ctx, first, "object_store_unavailable", false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimCandidate(ctx, time.Minute); err != nil || ok {
		t.Fatalf("retry was not delayed %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE observation_candidates SET next_attempt_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	pool = postgrestest.Restart(t, pool)
	store = NewObservationLedger(pool)
	c, ok, err := store.ClaimCandidate(ctx, time.Minute)
	if err != nil || !ok || c.LeaseToken <= first.LeaseToken {
		t.Fatalf("restart claim %#v %v %v", c, ok, err)
	}
	if err := store.DeferCandidate(ctx, c, "source_invalid", true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimCandidate(ctx, time.Minute); err != nil || ok {
		t.Fatalf("blocked candidate reclaimed %v %v", ok, err)
	}
	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status,error_code FROM observation_candidates`).Scan(&status, &code); err != nil || status != "blocked" || code != "source_invalid" {
		t.Fatalf("blocked state %s %s %v", status, code, err)
	}
	blocked := r
	blocked.Source.SourceID = "no-content"
	blocked.Frame = nil
	blocked.Receipt = nil
	blocked.ErrorCode = "scanner_unavailable"
	persistObservation(t, pool, blocked, false)
	var empty bool
	if err := pool.QueryRow(ctx, `SELECT frame IS NULL AND receipt IS NULL AND status='blocked' FROM observation_candidates WHERE source_id='no-content'`).Scan(&empty); err != nil || !empty {
		t.Fatalf("unsafe blocked row %v %v", empty, err)
	}
}

func TestObservationLedgerPersistenceParticipantRollsBackAndValidatesScope(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	r := seedObservationRun(t, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if conflict, err := insertObservationCandidate(ctx, tx, r); err != nil || conflict {
		t.Fatalf("insert %v %v", conflict, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("candidate escaped owner rollback %d %v", count, err)
	}
	r.Scope.WorkspaceID = "wrong-workspace"
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := insertObservationCandidate(ctx, tx, r); err == nil {
		t.Fatal("caller scope bypassed Run/Session authority")
	}
}

func TestObservationLedgerRejectsUnfencedLeaseMutation(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	r := seedObservationRun(t, pool)
	persistObservation(t, pool, r, false)
	if _, err := pool.Exec(ctx, `UPDATE observation_candidates SET status='running',lease_until=clock_timestamp()+interval '1 minute'`); err == nil {
		t.Fatal("running candidate accepted without advancing fence")
	}
	store := NewObservationLedger(pool)
	c, ok, err := store.ClaimCandidate(ctx, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE observation_candidates SET lease_until=lease_until+interval '1 minute'`); err == nil {
		t.Fatal("lease extended without a new fence")
	}
	if _, err := pool.Exec(ctx, `UPDATE observation_candidates SET lease_token=lease_token-1`); err == nil {
		t.Fatal("lease fence regressed")
	}
	if err := store.DeferCandidate(ctx, c, "source_invalid", true); err != nil {
		t.Fatal(err)
	}
}

func TestObservationLedgerRequiresVersionedProvenance(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	r := seedObservationRun(t, pool)
	for _, provenance := range []string{`{}`, `{"schema_version":null}`, `{"schema_version":"other"}`} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		r.Provenance = []byte(provenance)
		_, err = insertObservationCandidate(ctx, tx, r)
		tx.Rollback(ctx)
		if err == nil {
			t.Fatalf("invalid provenance accepted: %s", provenance)
		}
	}
}

func persistObservation(t *testing.T, pool *pgxpool.Pool, r observationCandidateRecord, wantConflict bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	conflict, err := insertObservationCandidate(ctx, tx, r)
	if err != nil || conflict != wantConflict {
		t.Fatalf("insert conflict=%v err=%v", conflict, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func fragmentTestDigest(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

func seedObservationRun(t *testing.T, pool *pgxpool.Pool) observationCandidateRecord {
	t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO worker_sessions(worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at) VALUES('session-ledger','tenant','user','workspace','revision','working-copy','idle',1,clock_timestamp()+interval '48 hours',clock_timestamp()+interval '14 days',clock_timestamp(),clock_timestamp())`,
		`INSERT INTO worker_runs(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,terminal_at) VALUES('run-ledger','session-ledger','direct_agent_turn','{}','profile','1','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','completed',clock_timestamp(),clock_timestamp())`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	frame := []byte(`{"safe":"original"}`)
	return observationCandidateRecord{Source: artifactapp.FragmentRef{Kind: "model", SourceID: "source-ledger", FrameDigest: fragmentTestDigest(frame)}, Frame: frame, Receipt: []byte(`{"safe":"receipt"}`), Scope: artifactdomain.ObservationScope{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session-ledger", WorkerRunID: "run-ledger"}, Provenance: []byte(`{"schema_version":"observation.provenance.v1"}`)}
}
