package frontierhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
	frontierhttp "github.com/abietic/switchback/internal/frontier/http"
)

type diagnosticReader struct{ err error }

func (reader diagnosticReader) ReadWorkerDiagnosticTarget(_ context.Context, query projectionapp.WorkerDiagnosticQuery) (projectionapp.WorkerDiagnosticTarget, error) {
	return projectionapp.WorkerDiagnosticTarget{WorkerSessionID: query.WorkerSessionID, WorkerRunID: query.WorkerRunID, AgentTurnID: query.AgentTurnID, StartedAt: time.Unix(1000, 0), EndedAt: time.Unix(2000, 0)}, reader.err
}

type navigationReader struct {
	result  artifactapp.ProjectionNavigation
	err     error
	queries []artifactapp.ProjectionNavigationQuery
}

func (reader *navigationReader) ReadProjectionNavigation(_ context.Context, query artifactapp.ProjectionNavigationQuery) (artifactapp.ProjectionNavigation, error) {
	reader.queries = append(reader.queries, query)
	return reader.result, reader.err
}

func TestDiagnosticsHistoricalIdentityAndSafeDependencyFailure(t *testing.T) {
	for _, tc := range []struct {
		name, state, want string
		failure           error
		denied            bool
	}{
		{"verified", "verified", "historically_verified", nil, false},
		{"pending", "pending", "pending", nil, false},
		{"blocked", "blocked", "blocked", nil, false},
		{"absent", "identity_unavailable", "identity_unavailable", nil, false},
		{"failure", "verified", "unavailable", errors.New("secret-private-exception"), false},
		{"denied", "verified", "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &navigationReader{result: artifactapp.ProjectionNavigation{Status: artifactapp.ProjectionNavigationStatus(tc.state), Identity: &artifactapp.ProjectionNavigationIdentity{Version: artifactapp.FragmentProjectionVersion, TraceID: strings.Repeat("a", 32), SelectedObservationID: strings.Repeat("b", 32), Sources: []artifactapp.FragmentRef{{Kind: "run_basis", SourceID: "run-source", FrameDigest: "sha256:" + strings.Repeat("c", 64)}, {Kind: "turn_basis", SourceID: "turn-source", FrameDigest: "sha256:" + strings.Repeat("d", 64)}}}}, err: tc.failure}
			authority := diagnosticReader{}
			if tc.denied {
				authority.err = projectionapp.ErrViewUnavailable
			}
			handler, err := frontierhttp.NewHandler(frontierhttp.Options{
				Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, Views: newContinuityReader(t, &continuityStoreStub{}, time.Now()),
				Diagnostics: authority, WorkspaceID: "workspace-local", GrafanaBrowserBaseURL: "http://localhost:3001",
				LangfuseBrowserBaseURL: "http://localhost:3000", LangfuseProjectID: "switchback-m1", ProjectionNavigation: bridge,
			})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics?worker_run_id=old-run&agent_turn_id=old-turn", nil))
			if response.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(response.Body.String(), "secret-private") {
				t.Fatal(response.Body.String())
			}
			if tc.denied {
				if response.Code != 404 || len(bridge.queries) != 0 {
					t.Fatalf("denial status=%d bridge queries=%v", response.Code, bridge.queries)
				}
				return
			}
			var body struct {
				Links    []struct{ Kind, URL string }
				Status   string `json:"langfuse_status"`
				Identity *struct {
					TraceID string `json:"trace_id"`
				}
				Target struct{ Session, Run, Turn string }
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Status != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if len(bridge.queries) != 1 || bridge.queries[0] != (artifactapp.ProjectionNavigationQuery{ActorID: "user-local", Scope: artifactdomain.ObservationScope{TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-local", WorkerSessionID: "session-1", WorkerRunID: "old-run"}, AgentTurnID: "old-turn"}) {
				t.Fatalf("queries=%+v", bridge.queries)
			}
			if tc.want == "historically_verified" {
				if len(body.Links) != 3 || body.Identity == nil || body.Links[0].Kind != "langfuse" || body.Links[0].URL != "http://localhost:3000/project/switchback-m1/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
					t.Fatal(response.Body.String())
				}
			} else if len(body.Links) != 2 || body.Identity != nil {
				t.Fatal(response.Body.String())
			}
		})
	}
}

func TestDiagnosticsAbsentBrowserConfigurationIsDisabled(t *testing.T) {
	handler, err := frontierhttp.NewHandler(frontierhttp.Options{
		Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"},
		Views:  newContinuityReader(t, &continuityStoreStub{}, time.Now()), Diagnostics: diagnosticReader{}, WorkspaceID: "workspace-local",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics", nil))
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || body["langfuse_status"] != "disabled" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDiagnosticsOptionalBrowserConfigurationAndSessionDoNotReadIdentity(t *testing.T) {
	for _, tc := range []struct{ origin, project, want string }{
		{"", "", "disabled"}, {"http://localhost:3000", "", "invalid_config"}, {"", "switchback-m1", "invalid_config"},
		{"http://langfuse-web:3000", "project", "invalid_config"}, {"https://example.com", "project", "invalid_config"},
		{"http://secret@localhost:3000", "project", "invalid_config"}, {"http://localhost:3000/path", "project", "invalid_config"},
		{"http://localhost:3000?token=secret", "project", "invalid_config"}, {"http://localhost:3000#", "project", "invalid_config"},
		{"http://[::ffff:127.0.0.1]:3000", "project", "invalid_config"}, {"http://localhost:65536", "project", "invalid_config"},
		{"http://localhost:3000", "../project", "invalid_config"}, {"http://localhost:3000", "project%2Fpath", "invalid_config"},
		{"http://localhost:3000", "project?key=secret", "invalid_config"}, {"http://localhost:3000", " project", "invalid_config"},
		{"http://localhost:3000", strings.Repeat("a", 513), "invalid_config"},
		{"http://localhost:3000", "switchback-m1", "identity_unavailable"}, {"http://127.0.0.1:3000", "switchback-m1", "identity_unavailable"}, {"https://[::1]:3000", "switchback-m1", "identity_unavailable"},
	} {
		t.Run(tc.origin+"/"+tc.project, func(t *testing.T) {
			bridge := &navigationReader{err: errors.New("must not read")}
			handler, err := frontierhttp.NewHandler(frontierhttp.Options{Viewer: projectionapp.Viewer{TenantID: "tenant-local", UserID: "user-local"}, Views: newContinuityReader(t, &continuityStoreStub{}, time.Now()), Diagnostics: diagnosticReader{}, WorkspaceID: "workspace-local", GrafanaBrowserBaseURL: "http://localhost:3001", LangfuseBrowserBaseURL: tc.origin, LangfuseProjectID: tc.project, ProjectionNavigation: bridge})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/views/worker_detail/session-1/diagnostics", nil))
			var body struct {
				Status   string `json:"langfuse_status"`
				Links    []any
				Identity any
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Status != tc.want || len(body.Links) != 2 || body.Identity != nil || len(bridge.queries) != 0 || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("status=%d body=%s queries=%v", response.Code, response.Body.String(), bridge.queries)
			}
		})
	}
}
