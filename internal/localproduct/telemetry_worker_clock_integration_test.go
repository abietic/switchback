//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTelemetryWorkerBackendClock(t *testing.T) {
	for _, test := range []struct {
		name string
		skew time.Duration
	}{{"ahead", 2 * time.Minute}, {"behind", -2 * time.Minute}} {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Calibrate once against this database. Only event time is offset;
			// polling, cancellation and deadlines continue to use real time.
			before := time.Now()
			var databaseNow time.Time
			if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
				t.Fatal(err)
			}
			offset := databaseNow.Sub(before.Add(time.Since(before)/2)) + test.skew
			backendNow := func() time.Time { return time.Now().Add(offset).UTC() }
			historyNow := func() time.Time { return backendNow().Add(-30 * time.Second) }
			seedWorkerTelemetry(t, pool, "history", "tenant", "user", "missing-fixture")
			prepareWorkerTelemetryStage(t, pool, "history", historyNow)
			completeWorkerTelemetryStage(t, pool, "history", historyNow)
			seedWorkerTelemetry(t, pool, "clock", "tenant", "user", "missing-fixture")
			prepareWorkerTelemetryStage(t, pool, "clock", backendNow)
			// Deliberate future database state, not a production completion.
			seedWorkerTelemetry(t, pool, "future", "tenant", "user", "missing-fixture")
			seedWorkerTelemetry(t, pool, "other-user", "tenant", "other", "missing-fixture")
			seedWorkerTelemetry(t, pool, "other-tenant", "other", "user", "missing-fixture")
			if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=$1 WHERE agent_turn_id='future-turn'`, backendNow().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}

			seen, server := workerTelemetryReceiver(ctx)
			defer server.Close()
			configuration := Config{Profile: config.SingleUserLocalProfile{TenantID: "tenant", ActorUserID: "user"}, DatabaseURL: telemetryScopedDatabaseURL(t, pool), AlloyPushURL: server.URL, AssistantLiteLLMAPIKey: "credential-SENTINEL"}
			runner := newTelemetryRunner(configuration)
			runner.now = backendNow
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); runner.Run(runCtx) }()
			defer func() { stop(); <-done }()
			deadline := time.After(8 * time.Second)
			waitWorkerTelemetryReady(t, runner, deadline)
			// Foreign committed fixture states share current event time, so the
			// startup cutoff cannot hide a missing tenant/user filter.
			if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=$1 WHERE agent_turn_id IN ('other-user-turn','other-tenant-turn')`, backendNow()); err != nil {
				t.Fatal(err)
			}
			terminalAt := completeWorkerTelemetryStage(t, pool, "clock", backendNow)
			var databaseAfter time.Time
			if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseAfter); err != nil {
				t.Fatal(err)
			}
			if test.skew > 0 && !terminalAt.After(databaseAfter) || test.skew < 0 && !terminalAt.Before(databaseAfter) {
				t.Fatalf("clock fixture did not establish %s skew: terminal=%s database=%s", test.name, terminalAt, databaseAfter)
			}
			t.Logf("real CompleteStage committed Run/Turn at %s; database after commit %s; calibrated offset %s", terminalAt.Format(time.RFC3339Nano), databaseAfter.Format(time.RFC3339Nano), offset)
			found := map[telemetry.EventKind]bool{}
			for len(found) != 2 {
				select {
				case e := <-seen:
					if e.Event != telemetry.EventRunTerminal && e.Event != telemetry.EventTurnTerminal {
						continue
					}
					assertWorkerTelemetryTerminal(t, e, "clock", terminalAt)
					if found[e.Event] {
						t.Fatalf("duplicate terminal event %+v", e)
					}
					found[e.Event] = true
				case <-deadline:
					t.Fatalf("production runner did not emit committed Run/Turn: received=%v terminal=%s database_after_commit=%s", found, terminalAt, databaseAfter)
				}
			}
			stop()
			<-done

			// A new production runner has its own startup cutoff and must not
			// replay the committed pair or backfill pre-start/future state.
			seedWorkerTelemetry(t, pool, "restart", "tenant", "user", "missing-fixture")
			prepareWorkerTelemetryStage(t, pool, "restart", backendNow)
			restarted := newTelemetryRunner(configuration)
			restarted.now = backendNow
			restartCtx, stopRestart := context.WithCancel(ctx)
			restartDone := make(chan struct{})
			go func() { defer close(restartDone); restarted.Run(restartCtx) }()
			defer func() { stopRestart(); <-restartDone }()
			restartDeadline := time.After(8 * time.Second)
			waitWorkerTelemetryReady(t, restarted, restartDeadline)
			restartAt := completeWorkerTelemetryStage(t, pool, "restart", backendNow)
			found = map[telemetry.EventKind]bool{}
			for len(found) != 2 {
				select {
				case e := <-seen:
					if e.Event == telemetry.EventRunTerminal || e.Event == telemetry.EventTurnTerminal {
						assertWorkerTelemetryTerminal(t, e, "restart", restartAt)
						if found[e.Event] {
							t.Fatalf("restart duplicated terminal %+v", e)
						}
						found[e.Event] = true
					}
				case <-restartDeadline:
					t.Fatalf("restarted runner did not emit its new committed Run/Turn: %v", found)
				}
			}
		})
	}
}

func prepareWorkerTelemetryStage(t *testing.T, pool *pgxpool.Pool, id string, now func() time.Time) {
	t.Helper()
	ctx := context.Background()
	startedAt := now().Add(-time.Second)
	createdAt := startedAt.Add(-time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='running',active_worker_run_id=$2,created_at=$3,updated_at=$4 WHERE worker_session_id=$1`, id, id+"-run", createdAt, startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='delivered',created_at=$3,started_at=$2,deadline_at=$2::timestamptz+interval '24 hours' WHERE agent_turn_id=$1`, id+"-turn", startedAt, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_runs SET created_at=$3,started_at=$2 WHERE worker_run_id=$1`, id+"-run", startedAt, createdAt); err != nil {
		t.Fatal(err)
	}
}

func completeWorkerTelemetryStage(t *testing.T, pool *pgxpool.Pool, id string, now func() time.Time) time.Time {
	t.Helper()
	var terminalAt time.Time
	service := workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time {
		terminalAt = now().UTC().Truncate(time.Microsecond)
		return terminalAt
	}}
	state, err := service.CompleteStage(context.Background(), workerapp.CompleteResidentStageCommand{
		TenantID: "tenant", WorkerSessionID: id, WorkerRunID: id + "-run", AgentTurnID: id + "-turn", RuntimeInstanceID: id + "-runtime",
		RuntimeGeneration: 1, ExpectedSessionRevision: 1, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
		SessionTarget: workerdomain.SessionInteractive, RunTarget: workerdomain.RunCompleted, TurnTarget: workerdomain.TurnCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.RunStatus != workerdomain.RunCompleted || state.TurnStatus != workerdomain.TurnCompleted {
		t.Fatalf("stage was not completed: %+v", state)
	}
	return terminalAt
}

func workerTelemetryReceiver(ctx context.Context) (<-chan telemetry.Event, *httptest.Server) {
	seen := make(chan telemetry.Event, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Streams []struct {
				Values [][]string `json:"values"`
			} `json:"streams"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, stream := range body.Streams {
			for _, value := range stream.Values {
				if len(value) == 2 {
					var e telemetry.Event
					if json.Unmarshal([]byte(value[1]), &e) == nil {
						select {
						case seen <- e:
						case <-ctx.Done():
							return
						case <-r.Context().Done():
							return
						}
					}
				}
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return seen, server
}

func waitWorkerTelemetryReady(t *testing.T, runner *telemetryRunner, deadline <-chan time.Time) {
	t.Helper()
	for !strings.Contains(telemetryScrape(runner), `switchback_telemetry_sample_current{environment="local",service="switchback-backend"} 1`) {
		select {
		case <-deadline:
			t.Fatal("runner sample not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertWorkerTelemetryTerminal(t *testing.T, e telemetry.Event, id string, terminalAt time.Time) {
	t.Helper()
	encoded, err := json.Marshal(e)
	if err != nil || strings.Contains(string(encoded), "credential-SENTINEL") || e.Code != telemetry.CodeOK || !e.TS.Equal(terminalAt) ||
		e.Scope.TenantID != "tenant" || e.Scope.WorkspaceID != "workspace" || e.Scope.WorkerSessionID != id || e.Scope.WorkerRunID != id+"-run" ||
		e.Event == telemetry.EventRunTerminal && e.Scope.AgentTurnID != "" || e.Event == telemetry.EventTurnTerminal && e.Scope.AgentTurnID != id+"-turn" {
		t.Fatalf("unsafe or unexpected terminal %s: %v", encoded, err)
	}
}
