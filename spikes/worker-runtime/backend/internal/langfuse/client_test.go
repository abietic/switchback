package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLangfuseClientIngestsLegacyBatchWithBasicAuth(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-test-public"
		secretKey = "sk-test-secret"
	)
	requests := make(chan IngestionBatch, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if request.URL.Path != "/api/public/ingestion" || request.URL.RawQuery != "" {
			t.Errorf("request URL = %q, want exact ingestion path", request.URL.String())
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != publicKey || password != secretKey {
			t.Errorf("Basic Auth = (%q, %q, %t), want configured keys", username, password, ok)
		}
		var batch IngestionBatch
		if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
			t.Errorf("decode ingestion batch: %v", err)
		}
		requests <- batch
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusMultiStatus)
		_, _ = response.Write([]byte(`{"successes":[{"id":"event-1","status":201}],"errors":[]}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:   server.URL,
		PublicKey: publicKey,
		SecretKey: secretKey,
		HTTPClient: &http.Client{
			Timeout: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	batch := IngestionBatch{Batch: []IngestionEvent{{
		ID:        "event-1",
		Timestamp: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
		Type:      "trace-create",
		Body:      map[string]any{"id": "trace-1"},
	}}}
	if err := client.Ingest(context.Background(), batch); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}

	got := <-requests
	if len(got.Batch) != 1 || got.Batch[0].ID != "event-1" || got.Batch[0].Type != "trace-create" {
		t.Fatalf("ingestion batch = %+v, want exact event", got)
	}
	formatted := fmt.Sprintf("%+v", client)
	if strings.Contains(formatted, publicKey) || strings.Contains(formatted, secretKey) {
		t.Fatalf("formatted client leaked credentials: %q", formatted)
	}
}

func TestLangfuseClientRejectsPartial207WithoutLeakingKeys(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-partial-public"
		secretKey = "sk-partial-secret"
	)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.String(), publicKey) || strings.Contains(request.URL.String(), secretKey) {
			t.Errorf("request URL leaked credentials: %q", request.URL.String())
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprintf(
			response,
			`{"successes":[{"id":"event-1","status":201}],"errors":[{"id":"event-2","status":400,"message":"rejected %s %s Authorization: Basic raw"}]}`,
			publicKey,
			secretKey,
		)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:   server.URL,
		PublicKey: publicKey,
		SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	err = client.Ingest(context.Background(), IngestionBatch{Batch: []IngestionEvent{{
		ID:        "event-1",
		Timestamp: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
		Type:      "trace-create",
		Body:      map[string]any{"id": "trace-1"},
	}}})
	if err == nil {
		t.Fatal("Ingest() error = nil, want partial 207 failure")
	}
	if strings.Contains(err.Error(), publicKey) ||
		strings.Contains(err.Error(), secretKey) ||
		strings.Contains(strings.ToLower(err.Error()), "authorization") {
		t.Fatalf("Ingest() error leaked response credentials: %q", err)
	}
}

func TestLangfuseClientGetTraceEscapesIdentityAndRequestsCompleteTrace(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-query-public"
		secretKey = "sk-query-secret"
		traceID   = "trace/with ?#%"
	)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", request.Method)
		}
		if request.URL.EscapedPath() != "/api/public/traces/trace%2Fwith%20%3F%23%25" {
			t.Errorf("escaped path = %q", request.URL.EscapedPath())
		}
		if request.URL.RawQuery != "" {
			t.Errorf("trace query = %q, want complete unfiltered response", request.URL.RawQuery)
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != publicKey || password != secretKey {
			t.Errorf("Basic Auth = (%q, %q, %t), want configured keys", username, password, ok)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"id":"trace/with ?#%",
			"metadata":{
				"worker_session_id":"session-1",
				"worker_run_id":"run-1",
				"runtime_instance_id":"runtime-1",
				"runtime_generation":1,
				"agent_session_id":"agent-session-1",
				"agent_turn_ids":["turn-1","turn-2"]
			},
			"observations":[{
				"id":"turn-1",
				"traceId":"trace/with ?#%",
				"parentObservationId":null,
				"name":"agent.turn",
				"type":"SPAN"
			}]
		}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:   server.URL,
		PublicKey: publicKey,
		SecretKey: secretKey,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	trace, err := client.GetTrace(context.Background(), traceID)
	if err != nil {
		t.Fatalf("GetTrace() error = %v", err)
	}
	if trace.ID != traceID ||
		trace.Metadata.WorkerSessionID != "session-1" ||
		len(trace.Observations) != 1 ||
		trace.Observations[0].ID != "turn-1" ||
		trace.Observations[0].Type != "SPAN" {
		t.Fatalf("GetTrace() = %+v, want decoded exact trace", trace)
	}
}

func TestLangfuseVerifierScriptRejectsInvalidConfigurationWithoutLeakingKeys(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-script-must-not-leak"
		secretKey = "sk-script-must-not-leak"
	)
	script := langfuseVerifierScript(t)
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("stat verifier script: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("verifier script mode = %s, want executable", info.Mode())
	}
	command := exec.Command(
		script,
		"trace-1",
		"turn-1",
		"turn-2",
		"generation-1",
		"turn-1",
		"generation-2",
		"turn-2",
	)
	command.Env = append(os.Environ(),
		"LANGFUSE_BASE_URL=not-a-url",
		"LANGFUSE_PUBLIC_KEY="+publicKey,
		"LANGFUSE_SECRET_KEY="+secretKey,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("verify-langfuse succeeded with invalid base URL")
	}
	if !strings.Contains(string(output), "LANGFUSE_BASE_URL must be an HTTP(S) origin") {
		t.Fatalf("verify-langfuse output = %q, want explicit base URL validation", output)
	}
	if strings.Contains(string(output), publicKey) || strings.Contains(string(output), secretKey) {
		t.Fatalf("verify-langfuse output leaked keys: %q", output)
	}
}

func TestLangfuseVerifierScriptAcceptsExactProjection(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-script-public"
		secretKey = "sk-script-secret"
	)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/public/traces/trace-1" ||
			request.URL.RawQuery != "" {
			t.Errorf("script query URL = %q", request.URL.String())
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != publicKey || password != secretKey {
			t.Errorf("script Basic Auth = (%q, %q, %t)", username, password, ok)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"id":"trace-1",
			"metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"]},
			"observations":[
				{"id":"turn-1","traceId":"trace-1","parentObservationId":null,"name":"agent.turn","type":"SPAN","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-1"}},
				{"id":"turn-2","traceId":"trace-1","parentObservationId":null,"name":"agent.turn","type":"SPAN","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-2"}},
				{"id":"generation-1","traceId":"trace-1","parentObservationId":"turn-1","name":"model.generation","type":"GENERATION","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-1"}},
				{"id":"generation-2","traceId":"trace-1","parentObservationId":"turn-2","name":"model.generation","type":"GENERATION","metadata":{"worker_session_id":"session-1","worker_run_id":"run-1","runtime_instance_id":"runtime-1","runtime_generation":1,"agent_session_id":"agent-session-1","agent_turn_ids":["turn-1","turn-2"],"agent_turn_id":"turn-2"}}
			]
		}`))
	}))
	defer server.Close()

	script := langfuseVerifierScript(t)
	command := exec.Command(
		script,
		"trace-1",
		"turn-1",
		"turn-2",
		"generation-1",
		"turn-1",
		"generation-2",
		"turn-2",
	)
	command.Env = append(os.Environ(),
		"LANGFUSE_BASE_URL="+server.URL,
		"LANGFUSE_PUBLIC_KEY="+publicKey,
		"LANGFUSE_SECRET_KEY="+secretKey,
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_SESSION_ID=session-1",
		"SWITCHBACK_SPIKE_LANGFUSE_WORKER_RUN_ID=run-1",
		"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_INSTANCE_ID=runtime-1",
		"SWITCHBACK_SPIKE_LANGFUSE_RUNTIME_GENERATION=1",
		"SWITCHBACK_SPIKE_LANGFUSE_AGENT_SESSION_ID=agent-session-1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("verify-langfuse error = %v output = %q", err, output)
	}
	if strings.TrimSpace(string(output)) != "Langfuse projection verified" {
		t.Fatalf("verify-langfuse output = %q", output)
	}
}

func TestLangfuseClientDoesNotLeakKeysFromTransportErrors(t *testing.T) {
	t.Parallel()

	const (
		publicKey = "pk-transport-must-not-leak"
		secretKey = "sk-transport-must-not-leak"
	)
	client, err := NewClient(ClientOptions{
		BaseURL:   "https://langfuse.invalid",
		PublicKey: publicKey,
		SecretKey: secretKey,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport echoed " + publicKey + " " + secretKey + " Authorization")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Ingest(context.Background(), IngestionBatch{Batch: []IngestionEvent{{
		ID:        "event-1",
		Timestamp: time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
		Type:      "trace-create",
		Body:      map[string]any{"id": "trace-1"},
	}}})
	if err == nil {
		t.Fatal("Ingest() error = nil, want transport failure")
	}
	for _, forbidden := range []string{publicKey, secretKey, "Authorization"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("transport error contains forbidden value %q: %q", forbidden, err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func langfuseVerifierScript(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../../scripts/verify-langfuse"))
}
