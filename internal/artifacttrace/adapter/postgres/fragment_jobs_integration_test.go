//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

const fragmentVersion = "langfuse.projection.v3"

func TestFragmentActivationIncludesLateCommittedRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedEvidenceTarget(t, pool, now)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	insertFragmentRun(t, tx, "run-late", now.Add(-24*time.Hour))
	seedFragmentPublication(t, tx, "model", "source-late", "run-late")
	store := artifactpostgres.NewFragmentJobStore(pool)
	if err := store.Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, fragmentVersion, 10); err != nil || n != 0 {
		t.Fatalf("uncommitted source discovered %d %v", n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, fragmentVersion, 1); err != nil || n != 1 {
		t.Fatalf("late source missed %d %v", n, err)
	}
	j, ok, err := store.Claim(ctx, fragmentVersion, time.Minute)
	if err != nil || !ok || j.Source.SourceID != "source-late" || j.Source.Kind != "model" {
		t.Fatalf("late job %#v %v %v", j, ok, err)
	}
	assertCount(t, pool, "observation_fragment_run_exclusions", 1)
	assertCount(t, pool, "observation_fragment_jobs", 1)
	var baseline string
	if err := pool.QueryRow(ctx, `SELECT worker_run_id FROM observation_fragment_run_exclusions`).Scan(&baseline); err != nil || baseline != "run-1" {
		t.Fatalf("baseline %s %v", baseline, err)
	}
}

func TestFragmentActivationConcurrentBaselineSurvivesRestartAndPageChanges(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	seedEvidenceTarget(t, pool, time.Now().UTC())
	store := artifactpostgres.NewFragmentJobStore(pool)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() { <-start; errs <- store.Activate(ctx, fragmentVersion) }()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	assertCount(t, pool, "observation_fragment_activations", 1)
	assertCount(t, pool, "observation_fragment_run_exclusions", 1)
	insertFragmentRun(t, pool, "run-new", time.Now().UTC())
	for _, id := range []string{"a", "b", "c"} {
		seedFragmentPublication(t, pool, "model", id, "run-new")
	}
	if n, err := store.Discover(ctx, fragmentVersion, 1); err != nil || n != 1 {
		t.Fatalf("page1 %d %v", n, err)
	}
	pool = postgrestest.Restart(t, pool)
	store = artifactpostgres.NewFragmentJobStore(pool)
	if err := store.Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, fragmentVersion, 50); err != nil || n != 2 {
		t.Fatalf("restart page %d %v", n, err)
	}
	if n, err := store.Discover(ctx, fragmentVersion, 2); err != nil || n != 0 {
		t.Fatalf("duplicate page %d %v", n, err)
	}
	assertCount(t, pool, "observation_fragment_run_exclusions", 1)
	assertCount(t, pool, "observation_fragment_jobs", 3)
	var wg sync.WaitGroup
	claimed := make(chan artifactapp.FragmentJob, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, ok, err := store.Claim(ctx, fragmentVersion, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				claimed <- j
			}
		}()
	}
	wg.Wait()
	close(claimed)
	seen := map[string]bool{}
	for j := range claimed {
		if seen[j.Source.SourceID] || j.LeaseToken != 1 || j.Attempts != 1 {
			t.Fatalf("duplicate claim %#v", j)
		}
		seen[j.Source.SourceID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("claims %v", seen)
	}
}

func TestFragmentJobsExactHistoricalRequestDoesNotUnlockRun(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	seedEvidenceTarget(t, pool, time.Now().UTC())
	store := artifactpostgres.NewFragmentJobStore(pool)
	if err := store.Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	seedFragmentPublication(t, pool, "model", "selected", "run-1")
	seedFragmentPublication(t, pool, "model", "unselected", "run-1")
	if n, err := store.Discover(ctx, fragmentVersion, 10); err != nil || n != 0 {
		t.Fatalf("historical %d %v", n, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO observation_fragment_requests(tenant_id,actor_user_id,idempotency_key,request_bytes,request_digest,version,source_set)
 VALUES('tenant-local','user-local','request', $1,$2,$3,jsonb_build_array(jsonb_build_object('kind','model','source_id','selected','frame_digest',$4::text)))`, []byte(`{"source":"selected"}`), fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{"source":"selected"}`))), fragmentVersion, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{}`))))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, fragmentVersion, 10); err != nil || n != 1 {
		t.Fatalf("requested %d %v", n, err)
	}
	j, ok, err := store.Claim(ctx, fragmentVersion, time.Minute)
	if err != nil || !ok || j.Source.SourceID != "selected" {
		t.Fatalf("wrong request %#v %v %v", j, ok, err)
	}
	assertCount(t, pool, "observation_fragment_jobs", 1)
	assertCount(t, pool, "observation_fragment_run_exclusions", 1)
}

func TestFragmentLedgerMigrationPreservesV2AndRejectsDowngrade(t *testing.T) {
	pool := postgrestest.OpenThroughMigration(t, 23)
	ctx := context.Background()
	now := time.Now().UTC()
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := finalizeCommand(t, now)
	command.ObservationSourceJSONL, err = artifactapp.EncodeTurnObservationSource(artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: command.Identity, StartedAt: now, EndedAt: now.Add(time.Second), Input: []byte(`{"value":9007199254740993}`), ModelObservations: []workerapp.ModelObservation{{ObservationID: "observation-old", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1", RuntimeID: "runtime-1", Generation: 1, Model: "model", Route: "route", Input: []byte(`{}`), Output: []byte(`{"old":true}`), Status: "ok", StartedAt: now, EndedAt: now.Add(time.Second)}}})
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{Store: objects, IDs: &sequenceIDs{values: []string{"report-old", "patch-old"}}, Now: func() time.Time { return now.Add(time.Minute) }, Policy: artifactapp.FinalizationPolicy{ArtifactPermission: contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "policy"}, ArtifactRetentionPolicyRef: "retention", TraceLifecyclePolicyRef: "lifecycle"}}).Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	publisher := artifactapp.ArtifactMetadataPublisher{Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool), IDs: &sequenceIDs{values: []string{"event-old"}}, Now: func() time.Time { return now.Add(2 * time.Minute) }}
	if _, err := publisher.Execute(ctx, artifactapp.PublishTurnEvidenceCommand{Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-old"}); err != nil {
		t.Fatal(err)
	}
	var sourceKey string
	for _, segment := range finalized.Manifest.Segments {
		if segment.Kind == artifactapp.ObservationSourceSegmentKind {
			sourceKey = strings.TrimPrefix(segment.URI, "object://")
		}
	}
	if sourceKey == "" {
		t.Fatal("missing frozen v2 source object")
	}
	sourceBefore := readFragmentLegacyObject(t, objects, sourceKey)
	manifestBefore := readFragmentLegacyObject(t, objects, finalized.ManifestObject.Key)
	if !bytes.Equal(sourceBefore, command.ObservationSourceJSONL) {
		t.Fatal("legacy source fixture did not preserve exact bytes")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO observability_projection_activations(schema_version) VALUES('langfuse.projection.v2');
 INSERT INTO observability_projection_jobs(schema_version,trace_stream_id,manifest_checksum,status,attempts,lease_token,error_code)
 VALUES('langfuse.projection.v2','pending','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','pending',0,0,NULL),
 ('langfuse.projection.v2','blocked','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','blocked',2,2,'source_invalid'),
 ('langfuse.projection.v2','passed','sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','passed',3,3,NULL);
 INSERT INTO worker_turn_settlements(settlement_id,tenant_id,worker_session_id,worker_run_id,agent_session_id,agent_turn_id,accepted_at,updated_at,phase_state,phase_state_digest,source_kind,source_reason,source_occurred_at,source_ref,source_digest,source_state,source_state_digest)
 VALUES('settlement','tenant-local','session-1','run-1','agent-1','turn-1',clock_timestamp(),clock_timestamp(),'{"checkpoint":"frozen","initial_input":{"value":9007199254740993}}','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','backend_deadline','hard_deadline',clock_timestamp(),'deadline-source','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','{"source":"frozen","output":"old bytes"}','sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc')`); err != nil {
		t.Fatal(err)
	}
	before := fragmentLegacySnapshot(t, pool)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate migration fixture")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "migrations", "000024_observation_fragments.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(data), "-- migrate:down")
	if !ok {
		t.Fatal("no down")
	}
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, fragmentLegacySnapshot(t, pool)) {
		t.Fatal("migration rewrote legacy evidence")
	}
	seedFragmentPublication(t, pool, "model", "retained", "run-1")
	if _, err := pool.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "observation fragment evidence is retained") {
		t.Fatalf("destructive downgrade %v", err)
	}
	if !bytes.Equal(before, fragmentLegacySnapshot(t, pool)) {
		t.Fatal("downgrade changed legacy evidence")
	}
	if !bytes.Equal(sourceBefore, readFragmentLegacyObject(t, objects, sourceKey)) || !bytes.Equal(manifestBefore, readFragmentLegacyObject(t, objects, finalized.ManifestObject.Key)) {
		t.Fatal("migration or refused downgrade rewrote source/manifest objects")
	}
	assertCount(t, pool, "observation_publications", 1)
	assertCount(t, pool, "observation_candidates", 1)
	assertCount(t, pool, "observability_projection_jobs", 3)
}

func readFragmentLegacyObject(t *testing.T, store workerapp.ObjectStore, key string) []byte {
	t.Helper()
	r, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fragmentLegacySnapshot(t *testing.T, pool *pgxpool.Pool) []byte {
	t.Helper()
	var snapshot []byte
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY trace_stream_id) FROM observability_projection_jobs j),
 'manifests',(SELECT jsonb_agg(to_jsonb(m) ORDER BY trace_stream_id) FROM trace_manifests m),
 'settlements',(SELECT jsonb_agg(to_jsonb(s) ORDER BY settlement_id) FROM worker_turn_settlements s))`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func insertFragmentRun(t *testing.T, executor projectionManifestExecutor, id string, now time.Time) {
	t.Helper()
	_, err := executor.Exec(context.Background(), `INSERT INTO worker_runs(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,terminal_at)
 VALUES($1,'session-1','direct_agent_turn','{}','profile','1','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','completed',$2,$2)`, id, now)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFragmentActivationExcludesRunningRunWithoutManifest(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	seedEvidenceTarget(t, pool, time.Now().UTC())
	if _, err := pool.Exec(ctx, `UPDATE worker_runs SET status='running',terminal_at=NULL WHERE worker_run_id='run-1'`); err != nil {
		t.Fatal(err)
	}
	if err := artifactpostgres.NewFragmentJobStore(pool).Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	var run, tenant, user, workspace, session string
	if err := pool.QueryRow(ctx, `SELECT worker_run_id,tenant_id,user_id,workspace_id,worker_session_id FROM observation_fragment_run_exclusions WHERE version=$1`, fragmentVersion).Scan(&run, &tenant, &user, &workspace, &session); err != nil {
		t.Fatal(err)
	}
	if run != "run-1" || tenant != "tenant-local" || user != "user-local" || workspace != "workspace-demo" || session != "session-1" {
		t.Fatalf("wrong exclusion scope: %s %s %s %s %s", run, tenant, user, workspace, session)
	}
	assertCount(t, pool, "observation_fragment_run_exclusions", 1)
	assertCount(t, pool, "trace_manifests", 0)
}

func TestFragmentJobsRejectExpiredCompletionWithoutNewClaim(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := artifactpostgres.NewFragmentJobStore(pool)
	if err := store.Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	seedEvidenceTarget(t, pool, time.Now().UTC())
	seedFragmentPublication(t, pool, "model", "source-1", "run-1")
	if n, err := store.Discover(ctx, fragmentVersion, 1); err != nil || n != 1 {
		t.Fatalf("discover %d %v", n, err)
	}
	job, ok, err := store.Claim(ctx, fragmentVersion, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE observation_fragment_jobs SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, job, artifactapp.ProjectionCompletion{Status: "passed"}); !errors.Is(err, artifactapp.ErrProjectionLeaseLost) {
		t.Fatalf("expired finish %v", err)
	}
	var status, digest string
	if err := pool.QueryRow(ctx, `SELECT status,frame_digest FROM observation_fragment_jobs WHERE version=$1 AND kind='model' AND source_id='source-1'`, fragmentVersion).Scan(&status, &digest); err != nil {
		t.Fatal(err)
	}
	if status != "running" || digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{}`))) {
		t.Fatalf("changed expired job %s %s", status, digest)
	}
	assertCount(t, pool, "observation_fragment_jobs", 1)
	next, ok, err := store.Claim(ctx, fragmentVersion, time.Minute)
	if err != nil || !ok || next.LeaseToken <= job.LeaseToken || next.Attempts != job.Attempts+1 {
		t.Fatalf("reclaim %#v %v %v", next, ok, err)
	}
	if err := store.Finish(ctx, job, artifactapp.ProjectionCompletion{Status: "passed"}); !errors.Is(err, artifactapp.ErrProjectionLeaseLost) {
		t.Fatalf("stale finish %v", err)
	}
	wrong := next
	wrong.Source.FrameDigest = "sha256:" + repeatHex("f")
	if err := store.Finish(ctx, wrong, artifactapp.ProjectionCompletion{Status: "passed"}); !errors.Is(err, artifactapp.ErrProjectionLeaseLost) {
		t.Fatalf("wrong digest finish %v", err)
	}
	if err := store.Finish(ctx, next, artifactapp.ProjectionCompletion{Status: "passed"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Claim(ctx, fragmentVersion, time.Minute); err != nil || ok {
		t.Fatalf("passed job reclaimed %v %v", ok, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM observation_fragment_jobs`).Scan(&status); err != nil || status != "passed" {
		t.Fatalf("finished %s %v", status, err)
	}
}

func TestFragmentJobsRejectCompletionExpiredDuringRowLockWait(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := artifactpostgres.NewFragmentJobStore(pool)
	if err := store.Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	seedEvidenceTarget(t, pool, time.Now().UTC())
	seedFragmentPublication(t, pool, "model", "source-lock-wait", "run-1")
	if n, err := store.Discover(ctx, fragmentVersion, 1); err != nil || n != 1 {
		t.Fatalf("discover %d %v", n, err)
	}
	job, ok, err := store.Claim(ctx, fragmentVersion, time.Second)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	snapshot := func() string {
		var rows string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(j) FROM observation_fragment_jobs j),
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
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM observation_fragment_jobs
 WHERE version=$1 AND kind=$2 AND source_id=$3 FOR UPDATE`, job.Version, job.Source.Kind, job.Source.SourceID).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- store.Finish(ctx, job, artifactapp.ProjectionCompletion{Status: "passed"}) }()
	waitFragmentJobBarrier(t, ctx, pool, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, holderPID)
	var live bool
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()<$1`, job.LeaseUntil).Scan(&live); err != nil || !live {
		t.Fatalf("completion must enter lock wait before expiry: live=%v err=%v", live, err)
	}
	waitFragmentJobBarrier(t, ctx, pool, `SELECT clock_timestamp()>=$1`, job.LeaseUntil)
	// The holder never changes the tuple: unlocking alone must not authorize Finish.
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, artifactapp.ErrProjectionLeaseLost) {
			t.Errorf("completion after lease expiry returned %v, want lease lost", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if after := snapshot(); after != before {
		t.Fatal("expired completion changed job state or immutable candidate/publication evidence")
	}
}

func waitFragmentJobBarrier(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, arg any) {
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

func TestFragmentJobsRetryBlockedAndInvalidInputs(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	store := artifactpostgres.NewFragmentJobStore(pool)
	if err := store.Activate(ctx, fragmentVersion); err != nil {
		t.Fatal(err)
	}
	seedEvidenceTarget(t, pool, time.Now().UTC())
	seedFragmentPublication(t, pool, "model", "source", "run-1")
	for _, lease := range []time.Duration{0, -time.Second, artifactapp.MaxProjectionLease + 1} {
		if _, _, err := store.Claim(ctx, fragmentVersion, lease); err == nil {
			t.Fatalf("invalid lease %v", lease)
		}
	}
	if _, err := store.Discover(ctx, fragmentVersion, 0); err == nil {
		t.Fatal("invalid page accepted")
	}
	if _, err := store.Discover(ctx, fragmentVersion, 1); err != nil {
		t.Fatal(err)
	}
	j, ok, err := store.Claim(ctx, fragmentVersion, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	for _, c := range []artifactapp.ProjectionCompletion{{Status: "published"}, {Status: "blocked", ErrorCode: "arbitrary provider text"}, {Status: "passed", ErrorCode: "source_invalid"}} {
		if err := store.Finish(ctx, j, c); err == nil {
			t.Fatalf("invalid completion %#v", c)
		}
	}
	if err := store.Finish(ctx, j, artifactapp.ProjectionCompletion{Status: "retryable", ErrorCode: "projection_unavailable", RetryAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Claim(ctx, fragmentVersion, time.Minute); err != nil || ok {
		t.Fatalf("early retry %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE observation_fragment_jobs SET next_attempt_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	j, ok, err = store.Claim(ctx, fragmentVersion, time.Minute)
	if err != nil || !ok || j.Attempts != 2 || j.LeaseToken != 2 {
		t.Fatalf("retry %#v %v %v", j, ok, err)
	}
	if err := store.Finish(ctx, j, artifactapp.ProjectionCompletion{Status: "blocked", ErrorCode: "parent_basis_unavailable"}); err != nil {
		t.Fatal(err)
	}
	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status,error_code FROM observation_fragment_jobs`).Scan(&status, &code); err != nil || status != "blocked" || code != "parent_basis_unavailable" {
		t.Fatalf("blocked %s %s %v", status, code, err)
	}
}

func seedFragmentPublication(t *testing.T, executor projectionManifestExecutor, kind, id, run string) {
	t.Helper()
	ctx := context.Background()
	_, err := executor.Exec(ctx, `INSERT INTO observation_candidates(kind,source_id,frame_digest,frame,receipt,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance)
 VALUES($1,$2,$3,$4,$5,'tenant-local','user-local','workspace-demo','session-1',$6,'{"schema_version":"observation.provenance.v1"}')`, kind, id, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`{}`))), []byte(`{}`), []byte(`{"safe":true}`), run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Exec(ctx, `WITH published AS (UPDATE observation_candidates SET status='published' WHERE kind=$1 AND source_id=$2 RETURNING *)
 INSERT INTO observation_publications(kind,source_id,frame_digest,object_key,object_size,object_checksum,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
 SELECT kind,source_id,frame_digest,source_id||'-object',2,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id FROM published`, kind, id)
	if err != nil {
		t.Fatal(err)
	}
}
