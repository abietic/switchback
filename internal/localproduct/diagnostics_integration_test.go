//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	projectionpostgres "github.com/abietic/switchback/internal/eventingprojection/adapter/postgres"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	frontierhttp "github.com/abietic/switchback/internal/frontier/http"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

func TestWorkerDiagnosticsExactAuthority(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
INSERT INTO worker_sessions
(worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at,terminal_at)
VALUES ('session-1','tenant-local','user-local','workspace-local','revision-1','copy-1','closed',1,'2026-10-01','2026-10-10','2026-09-28','2026-09-29','2026-09-29'),
('session-2','tenant-local','user-local','workspace-local','revision-1','copy-2','closed',1,'2026-10-01','2026-10-10','2026-09-28','2026-09-29','2026-09-29');
INSERT INTO worker_runs
(worker_run_id,worker_session_id,trigger_type,start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,terminal_at)
VALUES ('run-1','session-1','direct_agent_turn','{}','profile','1','sha256:'||repeat('a',64),'completed','2026-09-28','2026-09-29'),
('run-2','session-2','direct_agent_turn','{}','profile','1','sha256:'||repeat('a',64),'completed','2026-09-28','2026-09-29');
INSERT INTO agent_turns
(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,idempotency_key,created_at,terminal_at)
VALUES ('turn-1','session-1','run-1','agent-1','prompt','{}','completed','one','2026-09-28','2026-09-29'),
('turn-2','session-2','run-2','agent-2','prompt','{}','completed','two','2026-09-28','2026-09-29');`)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.LoadSingleUserLocalProfile(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	configuration := Config{Profile: profile, NetworkProfile: integrationNetworkProfile(t), DataRoot: t.TempDir(), ProjectID: defaultProjectID, WorkspaceID: defaultWorkspaceID, WorkspaceRevisionID: defaultWorkspaceRevisionID, GrafanaBrowserBaseURL: "http://localhost:3001"}
	app, err := newWithPool(ctx, configuration, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics?worker_run_id=run-1&agent_turn_id=turn-1", nil))
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Links []struct {
			Kind string
			URL  string
		}
		GrafanaStatus  string `json:"grafana_status"`
		LangfuseStatus string `json:"langfuse_status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Links) != 2 || result.GrafanaStatus != "configured" || result.LangfuseStatus != "identity_unavailable" {
		t.Fatalf("result=%+v", result)
	}
	parsed, err := url.Parse(result.Links[1].URL)
	if err != nil {
		t.Fatal(err)
	}
	var panes map[string]struct {
		Queries []struct{ Expr string }
		Range   map[string]string
	}
	if err := json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes); err != nil {
		t.Fatal(err)
	}
	want := `{service="switchback-backend",environment="local"} | json tenant_id="scope.tenant_id", workspace_id="scope.workspace_id", session_id="scope.worker_session_id", run_id="scope.worker_run_id", turn_id="scope.agent_turn_id" | tenant_id = "tenant-local" | workspace_id = "workspace-local" | session_id = "session-1" | run_id = "run-1" | turn_id = "turn-1"`
	// Every explicit JSON path must exist in the actual typed collector envelope.
	event, _ := json.Marshal(telemetry.Event{Scope: telemetry.Scope{TenantID: "tenant-local", WorkspaceID: "workspace-local", WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1"}})
	var envelope struct{ Scope map[string]string }
	_ = json.Unmarshal(event, &envelope)
	for _, match := range regexp.MustCompile(`([a-z_]+)="scope\.([a-z_]+)"`).FindAllStringSubmatch(panes["local"].Queries[0].Expr, -1) {
		value, exists := envelope.Scope[match[2]]
		if !exists || !strings.Contains(panes["local"].Queries[0].Expr, " | "+match[1]+" = "+strconv.Quote(value)) {
			t.Errorf("LogQL field %s missing from actual event: %s", match[2], event)
		}
	}
	if parsed.Host != "localhost:3001" || parsed.Path != "/explore" || panes["local"].Queries[0].Expr != want {
		t.Fatalf("unexpected scoped link: %s", result.Links[1].URL)
	}
	if panes["local"].Range["from"] != "1790553540000" || panes["local"].Range["to"] != "1790640060000" {
		t.Fatalf("range=%v", panes["local"].Range)
	}
	for _, tc := range []struct {
		name, session, query string
		status               int
	}{
		{"session only", "session-1", "", 200},
		{"historical run", "session-1", "?worker_run_id=run-1", 200},
		{"missing session", "missing", "", 404},
		{"foreign run", "session-1", "?worker_run_id=run-2", 404},
		{"foreign turn", "session-1", "?worker_run_id=run-1&agent_turn_id=turn-2", 404},
		{"missing run", "session-1", "?worker_run_id=missing", 404},
		{"missing turn", "session-1", "?worker_run_id=run-1&agent_turn_id=missing", 404},
		{"turn without run", "session-1", "?agent_turn_id=turn-1", 400},
		{"destination input", "session-1", "?url=http://localhost:99", 400},
		{"duplicate run", "session-1", "?worker_run_id=run-1&worker_run_id=run-2", 400},
		{"empty run", "session-1", "?worker_run_id=", 400},
		{"malformed encoding", "session-1", "?worker_run_id=%xx", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRecorder()
			app.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/"+tc.session+"/diagnostics"+tc.query, nil))
			if r.Code != tc.status {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
			if r.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("diagnostics must not be cached")
			}
		})
	}
	// A stale projection remains readable even after its authority has changed.
	_, err = pool.Exec(ctx, `INSERT INTO view_projections (tenant_id,user_id,view_type,view_id,schema_version,view_epoch,view_cursor,projection_version,query_fingerprint,data,available_actions,related_refs,updated_at)
VALUES ('tenant-local','user-local','worker_detail','session-1','frontier.projection_snapshot.v1',1,1,1,'scope','{}','[]','[]',now()),
('tenant-local','user-local','worker_detail','missing','frontier.projection_snapshot.v1',1,1,1,'scope','{}','[]','[]',now())`)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"tenant", "user", "workspace"} {
		t.Run("changed server "+scope, func(t *testing.T) {
			changed := configuration
			switch scope {
			case "tenant":
				changed.Profile.TenantID = "other"
			case "user":
				changed.Profile.ActorUserID = "other"
			case "workspace":
				changed.WorkspaceID = "other"
			}
			store := projectionpostgres.NewStore(pool)
			views, err := projectionapp.NewViewContinuityReader(store, projectionapp.ViewContinuityReaderOptions{Now: time.Now, ReplayLimit: 128})
			if err != nil {
				t.Fatal(err)
			}
			other, err := frontierhttp.NewHandler(frontierhttp.Options{Viewer: projectionapp.Viewer{TenantID: changed.Profile.TenantID, UserID: changed.Profile.ActorUserID}, Views: views, Diagnostics: store, WorkspaceID: changed.WorkspaceID, GrafanaBrowserBaseURL: changed.GrafanaBrowserBaseURL})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRecorder()
			other.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics", nil))
			if r.Code != 404 {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
		})
	}
	for _, column := range []string{"tenant_id", "user_id", "workspace_id"} {
		t.Run("changed row "+column, func(t *testing.T) {
			var original string
			if err := pool.QueryRow(ctx, "SELECT "+column+" FROM worker_sessions WHERE worker_session_id='session-1'").Scan(&original); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "UPDATE worker_sessions SET "+column+"='other' WHERE worker_session_id='session-1'"); err != nil {
				t.Fatal(err)
			}
			defer pool.Exec(ctx, "UPDATE worker_sessions SET "+column+"=$1 WHERE worker_session_id='session-1'", original)
			r := httptest.NewRecorder()
			app.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics", nil))
			if r.Code != 404 {
				t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
			}
		})
	}
	t.Run("missing authority despite projection", func(t *testing.T) {
		r := httptest.NewRecorder()
		app.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/missing/diagnostics", nil))
		if r.Code != 404 {
			t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
		}
	})
	t.Run("authorized expired history", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE worker_sessions SET status='expired' WHERE worker_session_id='session-1'`); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRecorder()
		app.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics?worker_run_id=run-1&agent_turn_id=turn-1", nil))
		if r.Code != 200 {
			t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
		}
	})
	t.Run("stored identifiers cannot inject LogQL", func(t *testing.T) {
		id := `session" | tenant_id != "safe` + "\n"
		_, err := pool.Exec(ctx, `INSERT INTO worker_sessions
(worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,quota_reservation_revision,idle_expires_at,absolute_expires_at,created_at,updated_at,terminal_at)
VALUES ($1,'tenant-local','user-local','workspace-local','revision-1','copy-quoted','closed',1,'2026-10-01','2026-10-10','2026-09-28','2026-09-29','2026-09-29')`, id)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRecorder()
		app.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/"+url.PathEscape(id)+"/diagnostics", nil))
		if r.Code != 200 {
			t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
		}
		var result struct{ Links []struct{ Kind, URL string } }
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		link, err := url.Parse(result.Links[1].URL)
		if err != nil {
			t.Fatal(err)
		}
		var panes map[string]struct{ Queries []struct{ Expr string } }
		if err := json.Unmarshal([]byte(link.Query().Get("panes")), &panes); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(panes["local"].Queries[0].Expr, ` | session_id = "session\" | tenant_id != \"safe\n"`) {
			t.Fatalf("unsafe expression %q", panes["local"].Queries[0].Expr)
		}
	})
	for _, base := range []string{"", "https://example.com", "http://grafana:3000", "http://localhost@evil.test", "http://secret@localhost:3001", "http://localhost:3001/path", "http://localhost:3001?token=secret", "http://localhost:3001#", "javascript:alert(1)", "http://localhost:65536", "http://localhost:", "http://localhost:3001?", "http://[::ffff:127.0.0.1]:3001", "http://[::ffff:7f00:1]:3001", "http://127.0.0.1:3001", "https://[::1]:3001"} {
		t.Run("configuration "+base, func(t *testing.T) {
			configured := configuration
			configured.GrafanaBrowserBaseURL = base
			other, err := newWithPool(ctx, configured, pool)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			r := httptest.NewRecorder()
			other.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics", nil))
			var body struct {
				Links  []any
				Status string `json:"grafana_status"`
			}
			if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &body) != nil {
				t.Fatalf("response=%s", r.Body.String())
			}
			status := "invalid_config"
			count := 0
			if base == "" {
				status = "disabled"
			}
			if base == "http://127.0.0.1:3001" || base == "https://[::1]:3001" {
				status = "configured"
				count = 2
			}
			if body.Status != status || len(body.Links) != count || strings.Contains(r.Body.String(), "secret") {
				t.Fatalf("response=%s", r.Body.String())
			}
		})
	}
}
