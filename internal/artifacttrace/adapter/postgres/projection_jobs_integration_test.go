//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProjectionActivationDoesNotExcludePublicationCommittedAfterward(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 11, 0, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	insertSecondProjectionTarget(t, pool, now)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	insertProjectionManifestWithExecutor(t, tx, "trace-late-commit", "turn-2", "sha256:"+repeatHex("7"), now.Add(-time.Hour))
	store := artifactpostgres.NewProjectionJobStore(pool)
	if err := store.Activate(ctx, artifactapp.DurableProjectionSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, artifactapp.DurableProjectionSchemaVersion, 50); err != nil || n != 1 {
		t.Fatalf("post-activation commit discovery: n=%d err=%v", n, err)
	}
}

func TestProjectionConcurrentFirstActivationKeepsOnePersistedBaseline(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 11, 30, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	insertProjectionManifest(t, pool, "trace-baseline", "turn-1", "sha256:"+repeatHex("8"), now)
	store := artifactpostgres.NewProjectionJobStore(pool)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- store.Activate(ctx, artifactapp.DurableProjectionSchemaVersion)
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.Discover(ctx, artifactapp.DurableProjectionSchemaVersion, 50); err != nil || n != 0 {
		t.Fatalf("concurrent activation baseline moved: n=%d err=%v", n, err)
	}
	var activations, exclusions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observability_projection_activations WHERE schema_version=$1`, artifactapp.DurableProjectionSchemaVersion).Scan(&activations); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM observability_projection_activation_exclusions WHERE schema_version=$1`, artifactapp.DurableProjectionSchemaVersion).Scan(&exclusions); err != nil {
		t.Fatal(err)
	}
	if activations != 1 || exclusions != 1 {
		t.Fatalf("activations=%d exclusions=%d, want 1/1", activations, exclusions)
	}
}

func TestProjectionRequestSourceReturnsRetryableExistingJobLookupFailure(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 11, 45, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	checksum := "sha256:" + repeatHex("9")
	insertProjectionManifest(t, pool, "trace-request-lookup", "turn-1", checksum, now)
	sibling := postgrestest.OpenSiblingWithTracer(t, pool, closeProjectionJobLookupTracer{})
	err := artifactpostgres.NewProjectionJobStore(sibling).RequestSource(ctx, artifactapp.DurableProjectionSchemaVersion, artifactapp.ProjectionSourceRef{TraceStreamID: "trace-request-lookup", ManifestChecksum: checksum})
	var projectionErr *artifactapp.ProjectionError
	if !errors.As(err, &projectionErr) || projectionErr.Permanent || projectionErr.Code != "projection_job_lookup_unavailable" {
		t.Fatalf("error=%#v", err)
	}
}

type closeProjectionJobLookupTracer struct{}

func (closeProjectionJobLookupTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT manifest_checksum FROM observability_projection_jobs") {
		_ = conn.PgConn().Close(ctx)
	}
	return ctx
}

func (closeProjectionJobLookupTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestProjectionActivationDiscoveryAndFencedClaims(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	insertProjectionManifest(t, pool, "trace-old", "turn-1", "sha256:"+repeatHex("1"), now)
	store := artifactpostgres.NewProjectionJobStore(pool)
	if _, _, err := store.Claim(ctx, artifactapp.DurableProjectionSchemaVersion, artifactapp.MaxProjectionLease+time.Nanosecond); err == nil {
		t.Fatal("lease above MaxProjectionLease accepted")
	}
	if err := store.Activate(ctx, artifactapp.DurableProjectionSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, artifactapp.DurableProjectionSchemaVersion, 50); err != nil || n != 0 {
		t.Fatalf("historical discovery: n=%d err=%v", n, err)
	}
	insertSecondProjectionTarget(t, pool, now.Add(time.Minute))
	// visible_at is deliberately older than activation; commit/order, not this mutable timestamp, controls discovery.
	insertProjectionManifest(t, pool, "trace-new", "turn-2", "sha256:"+repeatHex("2"), now.Add(-time.Hour))
	if err := store.Activate(ctx, artifactapp.DurableProjectionSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, artifactapp.DurableProjectionSchemaVersion, 50); err != nil || n != 1 {
		t.Fatalf("new source discovery: n=%d err=%v", n, err)
	}
	if n, err := store.Discover(ctx, artifactapp.DurableProjectionSchemaVersion, 50); err != nil || n != 0 {
		t.Fatalf("duplicate discovery: n=%d err=%v", n, err)
	}
	if err := store.Activate(ctx, "langfuse.projection.v3"); err != nil {
		t.Fatal(err)
	}
	if n, err := store.Discover(ctx, "langfuse.projection.v3", 50); err != nil || n != 0 {
		t.Fatalf("v3 historical discovery: n=%d err=%v", n, err)
	}
	if err := store.RequestSource(ctx, artifactapp.DurableProjectionSchemaVersion, artifactapp.ProjectionSourceRef{TraceStreamID: "trace-new", ManifestChecksum: "sha256:" + repeatHex("3")}); err == nil {
		t.Fatal("checksum mismatch request succeeded")
	}

	var wg sync.WaitGroup
	claimed := make(chan artifactapp.ProjectionJob, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, ok, err := store.Claim(ctx, artifactapp.DurableProjectionSchemaVersion, time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				claimed <- job
			}
		}()
	}
	wg.Wait()
	close(claimed)
	jobs := make([]artifactapp.ProjectionJob, 0, 1)
	for job := range claimed {
		jobs = append(jobs, job)
	}
	if len(jobs) != 1 {
		t.Fatalf("concurrent claims=%d, want 1", len(jobs))
	}
	if _, err := pool.Exec(ctx, `UPDATE observability_projection_jobs SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	reclaimed, ok, err := store.Claim(ctx, artifactapp.DurableProjectionSchemaVersion, time.Minute)
	if err != nil || !ok || reclaimed.LeaseToken <= jobs[0].LeaseToken {
		t.Fatalf("reclaim=%#v ok=%v err=%v", reclaimed, ok, err)
	}
	if err := store.Finish(ctx, jobs[0], artifactapp.ProjectionCompletion{Status: "passed"}); !errors.Is(err, artifactapp.ErrProjectionLeaseLost) {
		t.Fatalf("stale Finish=%v", err)
	}
	if err := store.Finish(ctx, reclaimed, artifactapp.ProjectionCompletion{Status: "passed"}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionMetadataAndLoaderUseExactInitialTurnSource(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 13, 0, 0, 0, time.UTC)
	seedEvidenceTarget(t, pool, now)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := finalizeCommand(t, now)
	input := []byte(`{}`)
	source := artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: command.Identity, StartedAt: now, EndedAt: now.Add(time.Second), Input: input, ModelObservations: []workerapp.ModelObservation{{ObservationID: "observation-1", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1", RuntimeID: "runtime-1", Generation: 1, Model: "model", Route: "route", Input: input, Output: []byte(`{"ok":true}`), Status: "ok", StartedAt: now, EndedAt: now.Add(time.Second)}}}
	command.ObservationSourceJSONL, err = artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := (artifactapp.TurnEvidenceFinalizer{Store: objects, IDs: &sequenceIDs{values: []string{"artifact-report-projection", "artifact-patch-projection"}}, Now: func() time.Time { return now.Add(time.Minute) }, Policy: artifactapp.FinalizationPolicy{ArtifactPermission: contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "policy"}, ArtifactRetentionPolicyRef: "retention", TraceLifecyclePolicyRef: "lifecycle"}}).Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	publisher := artifactapp.ArtifactMetadataPublisher{Objects: objects, Metadata: artifactpostgres.NewMetadataStore(pool), IDs: &sequenceIDs{values: []string{"event-invalid-kind", "event-duplicate-kind", "event-projection", "event-retry-unused"}}, Now: func() time.Time { return now.Add(2 * time.Minute) }}
	invalid := finalized
	invalid.Manifest.Segments = append([]contractsv1.TraceSegment(nil), finalized.Manifest.Segments...)
	invalid.Manifest.Segments[0].Kind = "unknown"
	if _, err := publisher.Execute(ctx, artifactapp.PublishTurnEvidenceCommand{Evidence: invalid, ExpectedSessionRevision: 4, ExpectedRunRevision: 3, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "invalid-kind"}); err == nil {
		t.Fatal("unknown evidence kind published")
	}
	duplicate := finalized
	duplicate.Manifest.Segments = append([]contractsv1.TraceSegment(nil), finalized.Manifest.Segments...)
	duplicate.Manifest.Segments[1].Kind = duplicate.Manifest.Segments[0].Kind
	if _, err := publisher.Execute(ctx, artifactapp.PublishTurnEvidenceCommand{Evidence: duplicate, ExpectedSessionRevision: 4, ExpectedRunRevision: 3, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "duplicate-kind"}); err == nil {
		t.Fatal("duplicate evidence kind published")
	}
	publishCommand := artifactapp.PublishTurnEvidenceCommand{Evidence: finalized, ExpectedSessionRevision: 4, ExpectedRunRevision: 3, ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2, IdempotencyKey: "publish-projection"}
	_, err = publisher.Execute(ctx, publishCommand)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Execute(ctx, publishCommand); err != nil {
		t.Fatalf("idempotent source publication: %v", err)
	}
	assertCount(t, pool, "evidence_object_refs", 6)
	store := artifactpostgres.NewProjectionJobStore(pool)
	ref := artifactapp.ProjectionSourceRef{TraceStreamID: finalized.Manifest.TraceStreamID, ManifestChecksum: finalized.ManifestObject.Checksum}
	metadata, err := store.LoadProjectionSourceMetadata(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Root.WorkerRunID != "run-1" || string(metadata.RootManifest.AgentTurnID) != "turn-1" || string(metadata.Root.Input) != "{}" {
		t.Fatalf("metadata=%#v", metadata)
	}
	projection, err := (artifactapp.ProjectionSourceLoader{Metadata: store, Objects: objects}).Load(ctx, artifactapp.ProjectionJob{SchemaVersion: artifactapp.DurableProjectionSchemaVersion, Source: ref})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Root.WorkerRunID != "run-1" || projection.Turn.Identity.TraceStreamID != "trace-stream-1" {
		t.Fatalf("projection=%#v", projection)
	}
}

func repeatHex(s string) string {
	out := ""
	for range 64 {
		out += s
	}
	return out
}

func insertSecondProjectionTarget(t *testing.T, pool *pgxpool.Pool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO worker_runs(worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at,terminal_at) VALUES ('run-2','session-1','task-1','task_rework','{}','profile','1','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','completed',1,$1,$1,$1)`,
		`INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,idempotency_key,created_at,started_at,deadline_at,terminal_at) VALUES ('turn-2','session-1','run-2','agent-2','prompt','{}','completed',1,'turn-2-key',$1,$1,$1::timestamptz+interval '24 hours',$1)`,
		`INSERT INTO runtime_instances(runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,status,image_ref,resource_profile_id,network_policy_id,revision,created_at,started_at,ended_at) VALUES ('runtime-2','session-1','run-2',2,'docker_container','lost','image','profile','network',1,$1,$1,$1)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatal(err)
		}
	}
}

func insertProjectionManifest(t *testing.T, pool *pgxpool.Pool, stream, turn, checksum string, visibleAt time.Time) {
	insertProjectionManifestWithExecutor(t, pool, stream, turn, checksum, visibleAt)
}

type projectionManifestExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertProjectionManifestWithExecutor(t *testing.T, executor projectionManifestExecutor, stream, turn, checksum string, visibleAt time.Time) {
	t.Helper()
	run, runtime := "run-1", "runtime-1"
	if turn == "turn-2" {
		run, runtime = "run-2", "runtime-2"
	}
	generation := 1
	if turn == "turn-2" {
		generation = 2
	}
	_, err := executor.Exec(context.Background(), `INSERT INTO trace_manifests(trace_stream_id,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,agent_turn_id,runtime_instance_id,runtime_generation,manifest_object_key,manifest_checksum,manifest_size_bytes,manifest,publication_idempotency_key,visible_at) VALUES ($1,'tenant-local','user-local','workspace-demo','session-1',$2,$3,$4,$7,$1||'-object',$5,2,'{}',$1||'-publication',$6)`, stream, run, turn, runtime, checksum, visibleAt, generation)
	if err != nil {
		t.Fatal(err)
	}
}
