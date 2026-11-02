package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCurrentPromptUsesServerBoundCorrelationWithoutInventedHeaders(t *testing.T) {
	correlation, err := promptCorrelation(map[string]any{"switchback": map[string]any{"agent_turn_id": "turn-current", "worker_session_id": "session-current", "runtime_id": "runtime-current", "runtime_generation": float64(1)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(correlation) != 0 {
		t.Fatalf("current profile must leave identity binding to server: %v", correlation)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, header := range []string{"x-litellm-call-id", "x-request-id", "x-switchback-trace-id", "x-switchback-worker-session-id", "x-switchback-worker-run-id", "x-switchback-agent-turn-id"} {
			if r.Header.Get(header) != "" {
				t.Errorf("invented identity header %s", header)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err = callModel(context.Background(), server.Client(), server.URL, "Bearer synthetic-only", correlation, 1); err != nil {
		t.Fatal(err)
	}
}

func TestControlledReaderOutlivesPromptAndStopsWithProcess(t *testing.T) {
	process, cancel := context.WithCancel(context.Background())
	defer cancel()
	registered := make(chan string, 2)
	release := make(chan struct{})
	finished := make(chan bool, 2)
	h := http.NewServeMux()
	h.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"x-litellm-call-id", "x-request-id", "x-switchback-trace-id", "x-switchback-worker-session-id", "x-switchback-worker-run-id", "x-switchback-agent-turn-id"} {
			if r.Header.Get(key) != "" {
				t.Errorf("invented header %s", key)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-litellm-call-id", "server-generated-id")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-original\",\"status\":\"in_progress\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-original\",\"status\":\"completed\",\"output\":[{\"text\":\"known\"}],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\ndata: [DONE]\n\n")
		case <-r.Context().Done():
		}
	})
	h.HandleFunc("/fixture/register", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		registered <- v["key"].(string)
		w.WriteHeader(200)
	})
	h.HandleFunc("/fixture/result", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		finished <- v["completed"].(bool)
		w.WriteHeader(204)
	})
	server := httptest.NewServer(h)
	defer server.Close()
	a := &fakeAgent{processCtx: process, client: &http.Client{Timeout: 10 * time.Second}, controlURL: server.URL}
	meta := func(turn string) map[string]any {
		return map[string]any{"switchback": map[string]any{"agent_turn_id": turn, "worker_session_id": "session", "runtime_id": "runtime", "runtime_generation": float64(1)}}
	}
	if err := a.startControlledModelStream(server.URL, "Bearer synthetic", "abcdefgh", meta("a")); err != nil {
		t.Fatal(err)
	}
	keyA := <-registered
	// Returning to ACP does not terminate the existing HTTP response.
	if err := a.startControlledModelStream(server.URL, "Bearer synthetic", "abcdefgh", meta("b")); err != nil {
		t.Fatal(err)
	}
	keyB := <-registered
	if keyA == keyB {
		t.Fatal("next Turn replaced original key")
	}
	select {
	case <-finished:
		t.Fatal("completion before release")
	default:
	}
	close(release)
	a.readers.Wait()
	for i := 0; i < 2; i++ {
		if !<-finished {
			t.Fatal("reader failed")
		}
	}
	// A second owner cancellation reaps a blocked reader without release.
	process2, cancel2 := context.WithCancel(context.Background())
	a.processCtx = process2
	release = make(chan struct{})
	if err := a.startControlledModelStream(server.URL, "Bearer synthetic", "abcdefgh", meta("c")); err != nil {
		t.Fatal(err)
	}
	<-registered
	cancel2()
	done := make(chan struct{})
	go func() { a.readers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("process cancellation did not reap reader")
	}
}

func TestLegacyPromptRetainsExactCorrelationAndRejectsPartialIdentity(t *testing.T) {
	metadata := map[string]any{"request_id": "request-1", "trace_id": "trace-1", "worker_session_id": "session-1", "worker_run_id": "run-1", "agent_turn_id": "turn-1"}
	correlation, err := promptCorrelation(map[string]any{"switchback": metadata})
	if err != nil || correlation["worker_run_id"] != "run-1" {
		t.Fatalf("legacy correlation lost: %v %v", correlation, err)
	}
	delete(metadata, "trace_id")
	if _, err = promptCorrelation(map[string]any{"switchback": metadata}); err == nil {
		t.Fatal("partial legacy identity accepted")
	}
}

func TestControlledStreamRejectsMissingIdentityTerminalFirstAndDisconnect(t *testing.T) {
	for _, variant := range []string{"missing-header", "terminal-first", "early-eof"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if variant != "missing-header" {
					w.Header().Set("x-litellm-call-id", "real-id")
				}
				if variant == "terminal-first" {
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"response\",\"status\":\"completed\"}}\n\n")
				}
			}))
			defer server.Close()
			a := &fakeAgent{processCtx: ctx, client: server.Client(), controlURL: server.URL}
			meta := map[string]any{"switchback": map[string]any{"agent_turn_id": "turn", "worker_session_id": "session", "runtime_id": "runtime", "runtime_generation": float64(1)}}
			if err := a.startControlledModelStream(server.URL, "Bearer synthetic", "abcdefgh", meta); err == nil {
				t.Fatal("invalid first response accepted")
			}
			a.readers.Wait()
		})
	}
}
