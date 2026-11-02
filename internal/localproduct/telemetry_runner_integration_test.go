//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTelemetryFailureDoesNotStopProductOutbox(t *testing.T) {
	for _, mode := range []string{"disabled", "bind-failure", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			pool := postgrestest.Open(t)
			profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
			if err != nil {
				t.Fatal(err)
			}
			c := Config{Profile: profile, NetworkProfile: integrationNetworkProfile(t), DatabaseURL: telemetryScopedDatabaseURL(t, pool), DataRoot: t.TempDir(), ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID, WorkspaceRevisionID: defaultWorkspaceRevisionID, GRPCAddress: defaultGRPCAddress}
			if mode == "bind-failure" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				c.TelemetryAddress = listener.Addr().String()
			}
			if mode == "invalid" {
				c.TelemetryAddress = "127.0.0.1:65536"
				c.AlloyPushURL = "http://user:secret@invalid/push"
			}
			if err = c.Validate(); err != nil {
				t.Fatal(err)
			}
			app, err := New(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			if app.telemetry == nil {
				t.Fatal("App telemetry lifecycle is not wired")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- app.Run(ctx) }()
			body, _ := json.Marshal(map[string]any{
				"schema_version": "gateway.direct_task_input.v1", "idempotency_key": "telemetry-core", "project_id": defaultProjectID, "workspace_id": defaultWorkspaceID, "workspace_revision_id": defaultWorkspaceRevisionID,
				"title": "Telemetry failure does not block core", "goal": "Create and project a Task while telemetry is disabled.", "target": map[string]string{"kind": "workspace", "ref": defaultWorkspaceRevisionID},
				"constraints": []string{"Keep core independent."}, "acceptance_criteria": []string{"Frontier index contains the Task."}, "agent_profile_id": config.DeepSeekCodexOpenAIV1, "acceptance_mode": "manual", "user_visible_types": []string{"report", "patch"},
			})
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(body)))
			if response.Code != http.StatusAccepted {
				t.Fatalf("create=%d %s", response.Code, response.Body.String())
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				select {
				case err := <-done:
					t.Fatalf("App stopped: %v", err)
				default:
				}
				response = httptest.NewRecorder()
				app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/views/task_summary/index", nil))
				if response.Code == http.StatusOK && strings.Contains(response.Body.String(), "Telemetry failure does not block core") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("core Outbox did not materialize Task")
				}
				time.Sleep(20 * time.Millisecond)
			}
			response = httptest.NewRecorder()
			app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if response.Code == http.StatusOK {
				t.Fatal("metrics exposed on product router")
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("App cancellation blocked on telemetry")
			}
		})
	}
}

func TestTelemetryRunnerUsesDedicatedPoolAndOwnsCancellation(t *testing.T) {
	pool := postgrestest.Open(t)
	seedTelemetryRows(t, pool)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	runner := newTelemetryRunner(Config{Profile: config.SingleUserLocalProfile{TenantID: "tenant-a", ActorUserID: "user-a"}, DatabaseURL: telemetryScopedDatabaseURL(t, pool), TelemetryAddress: address})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if strings.Contains(telemetryScrape(runner), `switchback_worker_run_terminal{environment="local",harness="codex",service="switchback-backend",status="completed"} 2`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dedicated pool did not sample test schema")
		}
		time.Sleep(10 * time.Millisecond)
	}
	client := http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("metrics endpoint unavailable")
	}
	response, err = client.Get("http://" + address + "/api/v1/tasks")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("telemetry listener served product path")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not cancel")
	}
	if _, err = client.Get("http://" + address + "/metrics"); err == nil {
		t.Fatal("listener survived cancellation")
	}
	if err = pool.Ping(context.Background()); err != nil {
		t.Fatal("telemetry closed core pool")
	}
}

func telemetryScopedDatabaseURL(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var schema string
	if err := pool.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("SWITCHBACK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
