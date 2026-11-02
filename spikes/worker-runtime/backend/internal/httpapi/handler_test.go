package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	"switchback.local/spikes/worker-runtime/backend/internal/service"
)

func TestPublicRunContractIsStrictAndDoesNotLeakSecrets(t *testing.T) {
	handler := newTestHandler(t)
	for _, field := range []string{
		`"runtime_instance_id":"client-value"`,
		`"agent_profile":"integration-fake"`,
		`"agent_executable":"/tmp/client-agent"`,
		`"upstream_api_base":"http://client-upstream.invalid/v1"`,
	} {
		invalid := httptest.NewRequest(
			http.MethodPost,
			"/spike/runs",
			strings.NewReader(`{"goal":"fix","acceptance_criteria":["pass"],`+field+`}`),
		)
		invalidResponse := httptest.NewRecorder()
		handler.ServeHTTP(invalidResponse, invalid)
		if invalidResponse.Code != http.StatusBadRequest {
			t.Fatalf("unknown field %s status = %d, want 400", field, invalidResponse.Code)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/spike/runs", strings.NewReader(`{"goal":" fix the fixture ","acceptance_criteria":[" tests pass "],"constraints":["keep scope"]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var created createdRunResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.WorkerSessionID == "" || created.WorkerRunID == "" || created.TurnID == "" {
		t.Fatalf("create response missing IDs: %+v", created)
	}
	privateValues := []string{
		"test-worker-token",
		"/distinctive-container/working-copy",
		"/distinctive-host/working-copy",
		"/distinctive-container/agent-state",
		"/distinctive-host/trace-spool",
		"/distinctive-container/runtime-logs",
	}
	for _, privateValue := range privateValues {
		if strings.Contains(response.Body.String(), privateValue) {
			t.Fatalf("create response leaked private value %q: %s", privateValue, response.Body.String())
		}
	}

	get := httptest.NewRequest(http.MethodGet, "/spike/runs/"+created.WorkerRunID, nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getResponse.Code, getResponse.Body.String())
	}
	if strings.Contains(getResponse.Body.String(), "test-worker-token") || strings.Contains(getResponse.Body.String(), "host_path") || strings.Contains(getResponse.Body.String(), "container_path") {
		t.Fatalf("snapshot leaked private state: %s", getResponse.Body.String())
	}
	for _, privateValue := range privateValues {
		if strings.Contains(getResponse.Body.String(), privateValue) {
			t.Fatalf("snapshot leaked private value %q: %s", privateValue, getResponse.Body.String())
		}
	}
	if !strings.Contains(getResponse.Body.String(), `"execution_outcome":"pending"`) || !strings.Contains(getResponse.Body.String(), `"spike_acceptance":"pending"`) {
		t.Fatalf("snapshot did not expose separate pending outcomes: %s", getResponse.Body.String())
	}
}

func TestPublicTurnConflictAndStrictBodyLimits(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)

	turn := httptest.NewRequest(http.MethodPost, "/spike/sessions/"+created.WorkerSessionID+"/turns", strings.NewReader(`{"message":"next"}`))
	turnResponse := httptest.NewRecorder()
	handler.ServeHTTP(turnResponse, turn)
	if turnResponse.Code != http.StatusConflict {
		t.Fatalf("turn before completion status = %d, body = %s", turnResponse.Code, turnResponse.Body.String())
	}

	overseized := `{"goal":"` + strings.Repeat("x", publicBodyLimit) + `","acceptance_criteria":["pass"]}`
	bigRequest := httptest.NewRequest(http.MethodPost, "/spike/runs", strings.NewReader(overseized))
	bigResponse := httptest.NewRecorder()
	handler.ServeHTTP(bigResponse, bigRequest)
	if bigResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized public body status = %d, want 413", bigResponse.Code)
	}

	trailing := httptest.NewRequest(http.MethodPost, "/spike/runs", strings.NewReader(`{"goal":"fix","acceptance_criteria":["pass"]} {}`))
	trailingResponse := httptest.NewRecorder()
	handler.ServeHTTP(trailingResponse, trailing)
	if trailingResponse.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d, want 400", trailingResponse.Code)
	}

	invalidAcceptance := httptest.NewRequest(http.MethodPost, "/spike/runs", strings.NewReader(`{"goal":"fix","acceptance_criteria":[" "]}`))
	invalidAcceptanceResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidAcceptanceResponse, invalidAcceptance)
	if invalidAcceptanceResponse.Code != http.StatusBadRequest {
		t.Fatalf("blank acceptance status = %d, want 400", invalidAcceptanceResponse.Code)
	}
}

func TestCleanupEndpointIsIdempotent(t *testing.T) {
	handler := newTestHandler(t)
	created := createRun(t, handler)
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/spike/sessions/"+created.WorkerSessionID+"/cleanup", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("cleanup status = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func newTestHandler(t *testing.T) http.Handler {
	return newTestHandlerWithStore(t, memoryObjectStore{})
}

func newTestHandlerWithStore(t *testing.T, store service.EvidenceStore) http.Handler {
	t.Helper()
	identifiers := []string{"session-1", "run-1", "runtime-1", "trace-1", "turn-1"}
	coordinator := service.NewCoordinator(service.Options{
		RootContext:  context.Background(),
		Materializer: materializerFunc(func(context.Context, string) error { return nil }),
		ObjectStore:  store,
		NewID: func() string {
			value := identifiers[0]
			identifiers = identifiers[1:]
			return value
		},
		NewToken: func() (string, error) { return "test-worker-token", nil },
	})
	return NewHandler(coordinator)
}

type materializerFunc func(context.Context, string) error

func (f materializerFunc) Materialize(ctx context.Context, sessionID string) (service.MaterializedSession, error) {
	materialized := service.MaterializedSession{
		WorkingCopy: runtime.PathPair{ContainerPath: "/distinctive-container/working-copy", HostPath: "/distinctive-host/working-copy"},
		AgentState:  runtime.PathPair{ContainerPath: "/distinctive-container/agent-state", HostPath: "/distinctive-host/agent-state"},
		TraceSpool:  runtime.PathPair{ContainerPath: "/distinctive-container/trace-spool", HostPath: "/distinctive-host/trace-spool"},
		RuntimeLogs: runtime.PathPair{ContainerPath: "/distinctive-container/runtime-logs", HostPath: "/distinctive-host/runtime-logs"},
	}
	return materialized, f(ctx, sessionID)
}

func (f materializerFunc) Rollback(context.Context, string, service.MaterializedSession) error {
	return nil
}

type memoryObjectStore struct{}

func (memoryObjectStore) Put(identity objectstore.ObjectIdentity, payload []byte) (objectstore.StoredObject, error) {
	return objectstore.StoredObject{SHA256: "test-digest", Bytes: int64(len(payload))}, nil
}

func createRun(t *testing.T, handler http.Handler) createdRunResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/spike/runs", bytes.NewBufferString(`{"goal":"fix","acceptance_criteria":["tests pass"]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var created createdRunResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode created run: %v", err)
	}
	return created
}
