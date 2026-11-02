package modelobs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServerAcceptsAuthenticatedObservationOnFixedLoopbackAddress(t *testing.T) {
	var got []Observation
	binder := NewRequestBinder()
	wantCorrelation := Correlation{
		TraceID:         "trace-1",
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentTurnID:     "turn-1",
	}
	if err := binder.Activate(wantCorrelation); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if err := binder.Bind("litellm-call-1"); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	server, err := NewServer("sink-secret", binder, func(observation Observation) error {
		got = append(got, observation)
		return nil
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	if got, want := server.Addr(), "127.0.0.1:7777"; got != want {
		t.Fatalf("Addr() = %q, want %q", got, want)
	}

	want := Observation{
		Model:     "gpt-5.2-codex",
		Route:     "switchback-codex",
		StartedAt: time.Date(2026, 7, 17, 1, 2, 3, 0, time.UTC),
		EndedAt:   time.Date(2026, 7, 17, 1, 2, 4, 500_000_000, time.UTC),
		LatencyMS: 1500,
		Usage:     Usage{InputTokens: 12, OutputTokens: 7, TotalTokens: 19},
		CostUSD:   0.0042,
		Error:     "",
		Correlation: Correlation{
			RequestID:       "litellm-call-1",
			TraceID:         wantCorrelation.TraceID,
			WorkerSessionID: wantCorrelation.WorkerSessionID,
			WorkerRunID:     wantCorrelation.WorkerRunID,
			AgentTurnID:     wantCorrelation.AgentTurnID,
		},
	}
	callback := want
	callback.Correlation = Correlation{RequestID: "litellm-call-1"}
	body, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:7777/", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(ModelSinkTokenHeader, "sink-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST observation: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	if !reflect.DeepEqual(got, []Observation{want}) {
		t.Fatalf("observations = %#v, want %#v", got, []Observation{want})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if _, err := http.Get("http://127.0.0.1:7777/"); err == nil {
		t.Fatal("request after Shutdown() unexpectedly succeeded")
	}
}

func TestServerBindsLiteLLMRequestAtPreCallAndRejectsUntrustedOrStaleCorrelation(t *testing.T) {
	var got []Observation
	binder := NewRequestBinder()
	turnOne := Correlation{
		TraceID:         "trace-1",
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentTurnID:     "turn-1",
	}
	if err := binder.Activate(turnOne); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer("sink-secret", binder, func(observation Observation) error {
		got = append(got, observation)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	postModelObservation(t, "/bind", map[string]string{"litellm_call_id": "call-turn-1"}, http.StatusAccepted)
	postModelObservation(t, "/bind", map[string]string{"litellm_call_id": "call-conflict"}, http.StatusAccepted)
	if err := binder.Deactivate(turnOne); err != nil {
		t.Fatal(err)
	}
	turnTwo := turnOne
	turnTwo.AgentTurnID = "turn-2"
	if err := binder.Activate(turnTwo); err != nil {
		t.Fatal(err)
	}

	postModelObservation(t, "/", callbackObservation("call-turn-1"), http.StatusAccepted)
	if len(got) != 1 || got[0].Correlation != correlationWithRequestID(turnOne, "call-turn-1") {
		t.Fatalf("late callback correlation = %#v, want immutable Turn 1 identity", got)
	}
	postModelObservation(t, "/bind", map[string]string{"litellm_call_id": "call-turn-1"}, http.StatusBadRequest)
	postModelObservation(t, "/", callbackObservation("call-turn-1"), http.StatusBadRequest)
	if len(got) != 1 {
		t.Fatalf("duplicate callback emitted %d observations, want exactly one", len(got))
	}

	postModelObservation(t, "/bind", map[string]string{"litellm_call_id": "call-turn-2"}, http.StatusAccepted)
	postModelObservation(t, "/", map[string]any{
		"model": "codex", "route": "litellm", "started_at": "2026-07-20T01:02:03Z", "ended_at": "2026-07-20T01:02:04Z",
		"latency_ms": 1000, "usage": map[string]int{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}, "cost_usd": 0,
		"correlation": map[string]string{"request_id": "call-turn-2", "agent_turn_id": "turn-1"},
	}, http.StatusBadRequest)
	postModelObservation(t, "/bind", map[string]string{"litellm_call_id": "call-conflict"}, http.StatusBadRequest)
	if err := binder.Deactivate(turnTwo); err != nil {
		t.Fatal(err)
	}
	postModelObservation(t, "/bind", map[string]string{"litellm_call_id": "stale-call"}, http.StatusBadRequest)
}

func correlationWithRequestID(correlation Correlation, requestID string) Correlation {
	correlation.RequestID = requestID
	return correlation
}

func callbackObservation(requestID string) map[string]any {
	return map[string]any{
		"model": "codex", "route": "litellm", "started_at": "2026-07-20T01:02:03Z", "ended_at": "2026-07-20T01:02:04Z",
		"latency_ms": 1000, "usage": map[string]int{"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}, "cost_usd": 0,
		"correlation": map[string]string{"request_id": requestID},
	}
}

func postModelObservation(t *testing.T, path string, payload any, wantStatus int) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:7777"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(ModelSinkTokenHeader, "sink-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s status = %d, want %d: %s", path, response.StatusCode, wantStatus, body)
	}
}

func TestServerRejectsMethodAuthContentTypeAndOversizedRawBodiesWithSafeErrors(t *testing.T) {
	server, err := NewServer("sink-secret", NewRequestBinder(), func(Observation) error { return nil })
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	tests := []struct {
		name        string
		method      string
		token       string
		contentType string
		body        []byte
		wantStatus  int
	}{
		{
			name:        "POST only",
			method:      http.MethodGet,
			token:       "sink-secret",
			contentType: "application/json",
			body:        []byte(`{"error":"request-secret"}`),
			wantStatus:  http.StatusMethodNotAllowed,
		},
		{
			name:        "authenticated",
			method:      http.MethodPost,
			token:       "wrong-token-with-different-length",
			contentType: "application/json",
			body:        []byte(`{"error":"request-secret"}`),
			wantStatus:  http.StatusUnauthorized,
		},
		{
			name:        "JSON only",
			method:      http.MethodPost,
			token:       "sink-secret",
			contentType: "text/plain",
			body:        []byte("request-secret"),
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "strict raw body limit",
			method:      http.MethodPost,
			token:       "sink-secret",
			contentType: "application/json",
			body:        bytes.Repeat([]byte("request-secret"), maxBodyBytes/len("request-secret")+2),
			wantStatus:  http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(
				test.method,
				"http://127.0.0.1:7777/",
				bytes.NewReader(test.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set(ModelSinkTokenHeader, test.token)
			request.Header.Set("Content-Type", test.contentType)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.StatusCode, test.wantStatus, body)
			}
			for _, protected := range []string{"request-secret", test.token, "sink-secret"} {
				if protected != "" && strings.Contains(string(body), protected) {
					t.Fatalf("safe error body leaked %q: %s", protected, body)
				}
			}
		})
	}
}
