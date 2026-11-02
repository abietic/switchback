//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTelemetryWorkerRunnerEmitsCommittedTerminal(t *testing.T) {
	pool := postgrestest.Open(t)
	seedWorkerTelemetry(t, pool, "wired", "tenant", "user", "missing-fixture")
	prepareWorkerTelemetryStage(t, pool, "wired", time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen, server := workerTelemetryReceiver(ctx)
	defer server.Close()
	runner := newTelemetryRunner(Config{Profile: config.SingleUserLocalProfile{TenantID: "tenant", ActorUserID: "user"}, DatabaseURL: telemetryScopedDatabaseURL(t, pool), AlloyPushURL: server.URL, AssistantLiteLLMAPIKey: "credential-SENTINEL"})
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case e := <-seen:
			if e.Event == telemetry.EventBackendStarted {
				goto started
			}
		case <-deadline:
			t.Fatal("runner did not start")
		}
	}
started:
	// Wait for the private sample: pool construction and the terminal observer's
	// startup lower bound have now been established inside production Run.
	for !strings.Contains(telemetryScrape(runner), `switchback_telemetry_sample_current{environment="local",service="switchback-backend"} 1`) {
		select {
		case <-deadline:
			t.Fatal("runner sample not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	terminalAt := completeWorkerTelemetryStage(t, pool, "wired", runner.now)
	found := map[telemetry.EventKind]bool{}
	for len(found) != 2 {
		select {
		case e := <-seen:
			if e.Event == telemetry.EventTurnTerminal || e.Event == telemetry.EventRunTerminal {
				assertWorkerTelemetryTerminal(t, e, "wired", terminalAt)
				found[e.Event] = true
			}
		case <-deadline:
			t.Fatalf("production runner did not emit committed Run/Turn: %v", found)
		}
	}
}

func TestTelemetryWorkerLateCommitWindowAndOverflow(t *testing.T) {
	pool := postgrestest.Open(t)
	seedWorkerTelemetry(t, pool, "window", "tenant", "user", "fixture")
	ctx := context.Background()
	var events []telemetry.Event
	metrics := telemetry.NewMetrics()
	o := newWorkerTerminalObserver(pool, "tenant", "user", metrics, func(e telemetry.Event) { events = append(events, e) }, time.Now)
	o.started = time.Now().Add(-2 * time.Minute)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	terminalAt := time.Now().UTC()
	if _, err = tx.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=$1 WHERE agent_turn_id='window-turn'`, terminalAt.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}
	o.sample(ctx)
	if len(events) != 0 {
		t.Fatal("uncommitted late row emitted")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	o.sample(ctx)
	if len(events) != 1 {
		t.Fatal("in-window late commit not observed")
	}
	if _, err = pool.Exec(ctx, `UPDATE worker_runs SET status='completed',terminal_at=$1 WHERE worker_run_id='window-run'`, terminalAt.Add(-61*time.Second)); err != nil {
		t.Fatal(err)
	}
	o.sample(ctx)
	if len(events) != 1 {
		t.Fatal("out-of-window row backfilled")
	}
	if _, err = pool.Exec(ctx, `INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,terminal_at)
SELECT 'overflow-'||n,'window','window-run','agent','prompt','{}','completed','overflow-'||n,$1,$1 FROM generate_series(1,1025) n`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	o.sample(ctx)
	if len(o.seen) > 1024 || len(events) > 1024 {
		t.Fatalf("unbounded window: %d/%d", len(o.seen), len(events))
	}
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(response.Body.String(), `reason="lifecycle_overflow"`) {
		t.Fatal("overflow gap unavailable")
	}
}

func seedWorkerTelemetry(t *testing.T, pool *pgxpool.Pool, id, tenant, user, container string) {
	t.Helper()
	ctx := context.Background()
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO worker_sessions(worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at,active_runtime_instance_id,runtime_generation) VALUES($1,$2,$3,'workspace','revision','copy','idle',1,now()+interval '2 days',now()+interval '14 days',now(),now(),$4,1)`, []any{id, tenant, user, id + "-runtime"}},
		{`INSERT INTO worker_runs(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,started_at) VALUES($1,$2,'direct_agent_turn','{}',$3,$4,$5,'running',now(),now())`, []any{id + "-run", id, profile.ID, fmtUint(profile.Revision), profile.Digest}},
		{`INSERT INTO runtime_instances(runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,container_id,status,image_ref,resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,network_enforcement_status,created_at) VALUES($1,$2,$3,1,'docker_container',$4,'active','fixture-image',$5,$6,$7,$8,'active',now())`, []any{id + "-runtime", id, id + "-run", container, config.LocalStandardV1, network.ID, network.Revision, network.Digest}},
		{`INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at) VALUES($1,$2,$3,'agent','prompt','{"secret":"credential-SENTINEL"}','accepted',$1,now())`, []any{id + "-turn", id, id + "-run"}},
	}
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTelemetryWorkerCommittedFactsAndReplay(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	seedWorkerTelemetry(t, pool, "own", "tenant", "user", "container")
	var events []telemetry.Event
	observer := newWorkerTerminalObserver(pool, "tenant", "user", telemetry.NewMetrics(), func(e telemetry.Event) { events = append(events, e) }, time.Now)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	terminalAt := time.Now().UTC()
	_, err = tx.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=$1,terminal_reason='credential-SENTINEL' WHERE agent_turn_id='own-turn'`, terminalAt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE worker_runs SET status='completed',terminal_at=$1,terminal_reason='credential-SENTINEL' WHERE worker_run_id='own-run'`, terminalAt)
	if err != nil {
		t.Fatal(err)
	}
	observer.sample(ctx)
	if len(events) != 0 {
		t.Fatal("uncommitted facts emitted")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	observer.sample(ctx)
	if len(events) != 0 {
		t.Fatal("rolled back facts emitted")
	}
	terminalAt = time.Now().UTC()
	_, err = pool.Exec(ctx, `UPDATE agent_turns SET status='cancelled',terminal_at=$1,terminal_reason='credential-SENTINEL' WHERE agent_turn_id='own-turn'`, terminalAt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE worker_runs SET status='completed',terminal_at=$1,terminal_reason='credential-SENTINEL' WHERE worker_run_id='own-run'`, terminalAt)
	if err != nil {
		t.Fatal(err)
	}
	observer.sample(ctx)
	if len(events) != 2 {
		t.Fatalf("committed events %d", len(events))
	}
	for _, e := range events {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "credential-SENTINEL") || e.Scope.WorkerSessionID != "own" || e.Scope.WorkerRunID != "own-run" || e.Scope.TenantID != "tenant" || e.Scope.WorkspaceID != "workspace" {
			t.Fatalf("unsafe scope/event %s", b)
		}
		if e.Event == telemetry.EventTurnTerminal && (e.Scope.AgentTurnID != "own-turn" || e.Code != telemetry.CodeCancelled || e.DurationMillis != nil) {
			t.Fatalf("turn event %+v", e)
		}
	}
	before, err := (telemetrySampler{pool: pool, tenantID: "tenant", userID: "user"}).Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observer.sample(ctx)
	if len(events) != 2 {
		t.Fatal("replay duplicated in-window event")
	}
	after, err := (telemetrySampler{pool: pool, tenantID: "tenant", userID: "user"}).Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range before.Counts {
		if after.Counts[i] != v {
			t.Fatal("event replay changed snapshot metric")
		}
	}
	restarted := newWorkerTerminalObserver(pool, "tenant", "user", telemetry.NewMetrics(), func(e telemetry.Event) { t.Fatal("restart backfilled old row") }, time.Now)
	restarted.sample(ctx)
}

func TestTelemetryWorkerPostgresAuthorityReplacement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	seedWorkerTelemetry(t, pool, "own", "tenant", "user", "old-container")
	seedWorkerTelemetry(t, pool, "other", "tenant", "other", "other-container")
	a, err := discoverWorkerOutput(ctx, pool, "tenant", "user")
	if err != nil || len(a) != 1 || a[0].scope.WorkerSessionID != "own" {
		t.Fatalf("authority %+v %v", a, err)
	}
	initial := a[0]
	if _, err = pool.Exec(ctx, `UPDATE worker_runs SET status='completed',terminal_at=now() WHERE worker_run_id='own-run';
INSERT INTO worker_runs(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,started_at)
SELECT 'later-run',worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,'running',now(),now() FROM worker_runs WHERE worker_run_id='own-run';
UPDATE worker_sessions SET active_worker_run_id='later-run' WHERE worker_session_id='own'`); err != nil {
		t.Fatal(err)
	}
	a, err = discoverWorkerOutput(ctx, pool, "tenant", "user")
	if err != nil || len(a) != 1 || a[0] != initial || a[0].scope.WorkerRunID != "" {
		t.Fatal("resident later Run changed output authority or attribution")
	}
	if _, err = pool.Exec(ctx, `UPDATE runtime_instances SET status='terminated',ended_at=now() WHERE runtime_instance_id='own-runtime'; INSERT INTO runtime_instances SELECT 'replacement',worker_session_id,worker_run_id,2,runtime_type,'new-container','active',image_ref,resource_profile_id,network_policy_id,control_channel_epoch,last_command_sequence,last_event_sequence,cleanup_confirmed,loss_diagnostic,cleanup_diagnostic,revision,created_at,started_at,NULL,network_policy_revision,network_policy_digest,network_enforcement_status FROM runtime_instances WHERE runtime_instance_id='own-runtime'; UPDATE worker_sessions SET active_runtime_instance_id='replacement',runtime_generation=2 WHERE worker_session_id='own'`); err != nil {
		t.Fatal(err)
	}
	a, err = discoverWorkerOutput(ctx, pool, "tenant", "user")
	if err != nil || len(a) != 1 || a[0].scope.RuntimeGeneration != 2 || a[0].containerID != "new-container" {
		t.Fatalf("replacement %+v %v", a, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE worker_sessions SET status='closed',terminal_at=now() WHERE worker_session_id='own'`); err != nil {
		t.Fatal(err)
	}
	a, err = discoverWorkerOutput(ctx, pool, "tenant", "user")
	if err != nil || len(a) != 0 {
		t.Fatalf("terminal authority %+v %v", a, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	start := time.Now()
	if _, err = discoverWorkerOutput(cancelCtx, pool, "tenant", "user"); err == nil || time.Since(start) > time.Second {
		t.Fatal("authority cancellation")
	}
}
