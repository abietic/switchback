//go:build integration

package localproduct

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTelemetryBlockedSourceKeepsSnapshotAndScrapeIndependent(t *testing.T) {
	pool := postgrestest.Open(t)
	seedTelemetryRows(t, pool)
	runner := newTelemetryRunner(Config{Profile: config.SingleUserLocalProfile{TenantID: "tenant-a", ActorUserID: "user-a"}})
	runner.sample(context.Background(), pool, nil)
	before := telemetryScrape(runner)
	if !strings.Contains(before, `switchback_telemetry_sample_current{environment="local",service="switchback-backend"} 1`) {
		t.Fatal(before)
	}
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `LOCK TABLE worker_runs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { runner.sample(context.Background(), pool, nil); close(done) }()
	// PostgreSQL confirms an actual blocked query, rather than a fake source.
	deadline := time.Now().Add(time.Second)
	for {
		var blocked bool
		err = pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='worker_runs'::regclass AND NOT granted)`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sample did not reach blocked source")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	if got := telemetryScrape(runner); got != before {
		t.Fatal("in-flight sampling changed snapshot")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("scrape waited on PostgreSQL")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sample deadline not bounded")
	}
	after := telemetryScrape(runner)
	if !strings.Contains(after, `switchback_telemetry_sample_current{environment="local",service="switchback-backend"} 0`) || !strings.Contains(after, `switchback_worker_run_terminal{environment="local",harness="codex",service="switchback-backend",status="completed"} 2`) {
		t.Fatal(after)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	runner.sample(context.Background(), pool, nil)
	if !strings.Contains(telemetryScrape(runner), `switchback_telemetry_sample_current{environment="local",service="switchback-backend"} 1`) {
		t.Fatal("sample did not recover")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	runner.sample(ctx, pool, nil)
	if time.Since(start) > time.Second {
		t.Fatal("sample cancellation not bounded")
	}
}

func telemetryScrape(runner *telemetryRunner) string {
	response := httptest.NewRecorder()
	runner.metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return response.Body.String()
}

func TestTelemetrySampleCommittedRowsScopedAndReplayStable(t *testing.T) {
	pool := postgrestest.Open(t)
	seedTelemetryRows(t, pool)
	sampler := telemetrySampler{pool: pool, tenantID: "tenant-a", userID: "user-a"}
	first, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metrics := telemetry.NewMetrics()
	if err = metrics.Replace(first); err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, v := range first.Counts {
		counts[string(v.Family)+"/"+string(v.Status)+"/"+string(v.Harness)] = v.Value
	}
	want := map[string]float64{
		"worker_run_terminal/completed/codex": 2, "worker_run_terminal/completed/unknown": 3,
		"agent_turn_terminal/completed/codex": 3, "agent_turn_terminal/completed/unknown": 3,
		"session_state/idle/unknown": 1, "runtime_state/active/unknown": 1,
		"queue_state/queued/unknown": 1, "queue_state/capacity_wait/unknown": 1,
		"candidate_projection/pending/unknown": 1, "candidate_projection/projected/unknown": 1, "candidate_projection/failed/unknown": 1,
		"fragment_projection/pending/unknown": 1, "fragment_projection/projected/unknown": 1,
	}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts=%v want=%v", counts, want)
	}
	for _, d := range first.Durations {
		if d.Family == telemetry.FamilyWorkerRunTerminal && d.Harness == telemetry.HarnessCodex {
			if d.Count != 1 || d.SumSeconds != 10 || d.Buckets[5] != 1 || d.Buckets[4] != 0 {
				t.Fatalf("duration=%+v", d)
			}
		}
	}
	if len(first.Durations) != 4 || len(first.OldestPending) != 2 {
		t.Fatalf("missing measurements: %+v", first)
	}
	for _, age := range first.OldestPending {
		if age.Seconds < 59 || age.Seconds > 70 {
			t.Fatalf("pending age=%v", age)
		}
	}
	second, err := sampler.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Counts, second.Counts) || !reflect.DeepEqual(first.Durations, second.Durations) {
		t.Fatal("repeated sample recounted terminal rows")
	}
}

func seedTelemetryRows(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []struct{ id, tenant, user string }{{"own", "tenant-a", "user-a"}, {"other-user", "tenant-a", "user-b"}, {"other-tenant", "tenant-b", "user-a"}} {
		exec(`INSERT INTO worker_sessions(worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at)
		VALUES($1,$2,$3,'ws','rev','copy','idle',1,now()+interval '2 days',now()+interval '14 days',now()-interval '1 hour',now())`, scope.id, scope.tenant, scope.user)
		exec(`INSERT INTO task_readiness_assessments(assessment_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,source,policy_revision,dimensions,blocking_unknowns,assumptions,status,content_digest,evaluated_at)
		VALUES($1,$2,$3,'ws','rev','api','policy','{}','[]','[]','ready_confirmed','sha256:'||repeat('a',64),now())`, scope.id, scope.tenant, scope.user)
		for _, kind := range []string{"queued", "capacity", "running"} {
			id := scope.id + "-task-" + kind
			exec(`INSERT INTO task_creation_intents(intent_id,tenant_id,actor_user_id,workspace_id,workspace_revision_id,readiness_assessment_id,task_spec_digest,trigger_policy_revision,status,consumed_by_task_id,created_at,expires_at,consumed_at)
			VALUES($1,$2,$3,'ws','rev',$4,'sha256:'||repeat('a',64),'policy','consumed',$1,now(),now()+interval '1 hour',now())`, id, scope.tenant, scope.user, scope.id)
			exec(`INSERT INTO tasks(task_id,tenant_id,initiator_user_id,workspace_id,workspace_revision_id,task_spec_snapshot,task_spec_digest,creation_intent_id,status,acceptance_mode,allocation_wait_reason,created_at,updated_at)
			VALUES($1,$2,$3,'ws','rev','{}','sha256:'||repeat('a',64),$1,CASE WHEN $4='running' THEN 'running' ELSE 'queued' END,'manual',CASE WHEN $4='capacity' THEN 'session_capacity' END,now(),now())`, id, scope.tenant, scope.user, kind)
		}
		for index, id := range []string{config.DeepSeekCodexOpenAIV1, config.KimiCodexAnthropicV1, "unknown-profile", "bad-revision", "bad-digest"} {
			profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
			if id == config.KimiCodexAnthropicV1 {
				profile, _ = config.ProductionProfileRegistry().Agent(id)
			}
			revision := strconv.FormatUint(profile.Revision, 10)
			if id == "unknown-profile" {
				profile.ID = id
			}
			if id == "bad-revision" {
				revision = "99"
			}
			if id == "bad-digest" {
				profile.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			run := scope.id + "-" + strconv.Itoa(index)
			exec(`INSERT INTO worker_runs(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,started_at,terminal_at)
			VALUES($1,$2,'direct_agent_turn','{}',$3,$4,$5,'completed',now()-interval '1 hour',CASE WHEN $6 THEN NULL ELSE now()-interval '10 seconds' END,now()) ON CONFLICT DO NOTHING`, run, scope.id, profile.ID, revision, profile.Digest, index == 1)
			exec(`INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,started_at,deadline_at,terminal_at)
			VALUES($1,$2,$3,'agent','prompt','{}','completed',$1,now()-interval '1 hour',now()-interval '5 seconds',now()-interval '5 seconds'+interval '24 hours',now())`, run+"-turn", scope.id, run)
		}
		exec(`INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,terminal_at)
		VALUES($1,$2,$3,'agent','prompt','{}','completed',$1,now()-interval '1 hour',now())`, scope.id+"-extra-turn", scope.id, scope.id+"-0")
		exec(`INSERT INTO runtime_instances(runtime_instance_id,worker_session_id,generation,runtime_type,status,image_ref,resource_profile_id,network_policy_id,created_at)
		VALUES($1,$2,1,'docker_container','active','image','profile','network',now())`, scope.id+"-runtime", scope.id)
		for _, state := range []string{"pending", "published", "blocked"} {
			source := scope.id + "-" + state
			// Publication binding is checked at commit; fixture writes both halves in one transaction.
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO observation_candidates(kind,source_id,frame_digest,frame,receipt,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,provenance,status,created_at)
			VALUES('run_basis',$1,'sha256:'||encode(sha256('frame'::bytea),'hex'),'frame','receipt',$2,$3,'ws',$4,$5,'{"schema_version":"observation.provenance.v1"}',$6,now()-interval '1 minute')`, source, scope.tenant, scope.user, scope.id, scope.id+"-0", state)
			if err == nil && state == "published" {
				_, err = tx.Exec(ctx, `INSERT INTO observation_publications(kind,source_id,frame_digest,object_key,object_size,object_checksum,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id)
				SELECT kind,source_id,frame_digest,'key',5,frame_digest,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id FROM observation_candidates WHERE source_id=$1;
				`, source)
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO observation_fragment_jobs(version,kind,source_id,frame_digest,status,created_at)
				SELECT v,kind,source_id,frame_digest,s,now()-interval '1 minute' FROM observation_candidates CROSS JOIN (VALUES('v1','pending'),('v2','passed')) versions(v,s) WHERE source_id=$1`, source)
				}
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Replaying a terminal primary key must not add another count.
	exec(`INSERT INTO worker_runs SELECT * FROM worker_runs WHERE worker_run_id='own-0' ON CONFLICT DO NOTHING`)
}
