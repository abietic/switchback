package frontierhttp

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	projectionapp "github.com/abietic/switchback/internal/eventingprojection/application"
)

type diagnosticLink struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

type diagnosticLinks struct {
	Links          []diagnosticLink `json:"links"`
	GrafanaStatus  string           `json:"grafana_status"`
	LangfuseStatus string           `json:"langfuse_status"`
}

func (handler *handler) workerDiagnostics(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "private, no-store")
	values, parseErr := url.ParseQuery(request.URL.RawQuery)
	if parseErr != nil {
		writeError(response, http.StatusBadRequest, "invalid_diagnostic_query")
		return
	}
	for key, items := range values {
		if (key != "worker_run_id" && key != "agent_turn_id") || len(items) != 1 || items[0] == "" || len(items[0]) > 512 {
			writeError(response, 400, "invalid_diagnostic_query")
			return
		}
	}
	query := projectionapp.WorkerDiagnosticQuery{Viewer: handler.viewer, WorkspaceID: handler.workspaceID, WorkerSessionID: request.PathValue("view_id"), WorkerRunID: values.Get("worker_run_id"), AgentTurnID: values.Get("agent_turn_id")}
	if len(query.WorkerSessionID) > 512 || (query.AgentTurnID != "" && query.WorkerRunID == "") {
		writeError(response, 400, "invalid_diagnostic_query")
		return
	}
	target, err := handler.diagnostics.ReadWorkerDiagnosticTarget(request.Context(), query)
	if err != nil {
		switch {
		case errors.Is(err, projectionapp.ErrInvalidViewQuery):
			writeError(response, 400, "invalid_diagnostic_query")
		case errors.Is(err, projectionapp.ErrViewUnavailable):
			writeError(response, 404, "view_unavailable")
		default:
			writeError(response, 503, "diagnostics_unavailable")
		}
		return
	}
	result := diagnosticLinks{Links: []diagnosticLink{}, GrafanaStatus: "disabled", LangfuseStatus: "identity_unavailable"}
	if handler.grafanaBrowserBaseURL != "" {
		base, valid := localBrowserOrigin(handler.grafanaBrowserBaseURL)
		result.GrafanaStatus = "invalid_config"
		if valid {
			result.GrafanaStatus = "configured"
			from := strconv.FormatInt(target.StartedAt.Add(-time.Minute).UnixMilli(), 10)
			to := strconv.FormatInt(target.EndedAt.Add(time.Minute).UnixMilli(), 10)
			base.Path = "/d/sb-worker-runtime/worker-runtime"
			base.RawQuery = url.Values{"from": {from}, "to": {to}}.Encode()
			result.Links = append(result.Links, diagnosticLink{Kind: "grafana", URL: base.String()})
			// User ownership was verified above; the safe telemetry envelope has no user_id.
			expr := `{service="switchback-backend",environment="local"} | json tenant_id="scope.tenant_id", workspace_id="scope.workspace_id", session_id="scope.worker_session_id", run_id="scope.worker_run_id", turn_id="scope.agent_turn_id"`
			for _, filter := range [][2]string{{"tenant_id", query.Viewer.TenantID}, {"workspace_id", query.WorkspaceID}, {"session_id", query.WorkerSessionID}, {"run_id", query.WorkerRunID}, {"turn_id", query.AgentTurnID}} {
				if filter[1] != "" {
					expr += " | " + filter[0] + " = " + strconv.Quote(filter[1])
				}
			}
			panes, _ := json.Marshal(map[string]any{"local": map[string]any{"datasource": "switchback-loki", "queries": []any{map[string]any{"refId": "A", "expr": expr, "queryType": "range", "datasource": map[string]string{"type": "loki", "uid": "switchback-loki"}}}, "range": map[string]string{"from": from, "to": to}}})
			base.Path = "/explore"
			base.RawQuery = url.Values{"schemaVersion": {"1"}, "panes": {string(panes)}}.Encode()
			result.Links = append(result.Links, diagnosticLink{Kind: "logs", URL: base.String()})
		}
	}
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(result)
}

// Only explicit loopback HTTP(S) origins are accepted. No DNS resolution,
// credentials, path, query, fragment or internal Compose service is supported.
func localBrowserOrigin(raw string) (*url.URL, bool) {
	base, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(raw) != raw || strings.Contains(raw, "#") || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Opaque != "" {
		return nil, false
	}
	host := base.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		// Browsers normalize mapped IPv6 to a hostname the client deliberately
		// does not allow. Reject it here rather than advertising unusable links.
		if ip == nil || !ip.IsLoopback() || (strings.Contains(host, ":") && ip.To4() != nil) {
			return nil, false
		}
	}
	if port := base.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, false
		}
	} else if strings.HasSuffix(base.Host, ":") {
		return nil, false
	}
	return base, true
}
