package localproduct

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelemetryConstructorAndScrapePerformNoIO(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Observe both potential remote destinations without running telemetry.
	runner := newTelemetryRunner(Config{TelemetryAddress: listener.Addr().String(), DatabaseURL: "postgres://user@" + listener.Addr().String() + "/test?sslmode=disable", AlloyPushURL: "http://" + listener.Addr().String() + "/push"})
	response := httptest.NewRecorder()
	runner.metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "switchback_telemetry_sample_available") {
		t.Fatal("pre-sample metrics unavailable")
	}
	if err = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if connection, err := listener.Accept(); err == nil {
		connection.Close()
		t.Fatal("constructor or scrape performed network I/O")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("telemetry did not cancel")
	}
}

func TestTelemetrySenderUsesSafeLifecycleEvents(t *testing.T) {
	events := make(chan string, 8)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Streams []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"streams"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		for _, stream := range body.Streams {
			for _, value := range stream.Values {
				if len(value) != 2 {
					t.Error("invalid Loki value")
					continue
				}
				if strings.Contains(value[1], "sensitive-dsn") || strings.Contains(value[1], "known-test-secret") {
					t.Error("raw configured value escaped")
				}
				var event struct {
					Event string `json:"event"`
				}
				if err := json.Unmarshal([]byte(value[1]), &event); err != nil {
					t.Error(err)
				}
				events <- event.Event
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	runner := newTelemetryRunner(Config{AlloyPushURL: receiver.URL + "/loki/api/v1/push", DatabaseURL: "sensitive-dsn", AssistantLiteLLMAPIKey: "known-test-secret"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	seen := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for len(seen) < 2 {
		select {
		case event := <-events:
			seen[event] = true
		case <-deadline:
			t.Fatal("missing backend_started/sample_failed events")
		}
	}
	if !seen["backend_started"] || !seen["sample_failed"] {
		t.Fatalf("unexpected events=%v", seen)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sender shutdown blocked")
	}
}

func TestTelemetryConfigurationIsOptional(t *testing.T) {
	for _, address := range []string{"", "127.0.0.1:9091", ":9091", "127.0.0.1:65536", "127.0.0.1:not-a-port"} {
		c, err := LoadConfig(mapLookup(map[string]string{"SWITCHBACK_DATABASE_URL": "postgres://localhost/switchback", "SWITCHBACK_DATA_ROOT": t.TempDir(), "SWITCHBACK_TELEMETRY_ADDRESS": address, "SWITCHBACK_ALLOY_PUSH_URL": "http://user:secret@bad/push"}))
		if err != nil {
			t.Fatal(err)
		}
		if c.TelemetryAddress != address || c.AlloyPushURL != "http://user:secret@bad/push" {
			t.Fatal("telemetry env not loaded")
		}
	}
	runner := newTelemetryRunner(Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled telemetry started work")
	}
}

func TestTelemetrySenderEndpointRejectsInvalidPortsAndCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://alloy:99999/loki/api/v1/push", "http://alloy:0/push", "http://user:secret@alloy/push", "http:///push", "http://alloy/push?token=secret", "http://alloy/push#secret", " http://alloy/push", "http://:123/push"} {
		if validTelemetryPushURL(endpoint) {
			t.Fatalf("accepted invalid endpoint %q", endpoint)
		}
	}
	for _, endpoint := range []string{"http://alloy:12345/loki/api/v1/push", "https://alloy/push", "http://[::1]:12345/push"} {
		if !validTelemetryPushURL(endpoint) {
			t.Fatal("valid endpoint rejected")
		}
	}
}

func TestTelemetryLogFailureLeavesMetricsListenerAvailable(t *testing.T) {
	for _, mode := range []string{"scanner-unavailable", "invalid-url", "invalid-port"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			c := Config{TelemetryAddress: address, DatabaseURL: "invalid-dsn", AlloyPushURL: "http://127.0.0.1:1/push"}
			if mode == "invalid-url" {
				c.AlloyPushURL = "http://user:secret@alloy/push"
				c.AssistantLiteLLMAPIKey = "test-known-token"
			}
			if mode == "invalid-port" {
				c.AlloyPushURL = "http://alloy:99999/push"
				c.AssistantLiteLLMAPIKey = "test-known-token"
			}
			runner := newTelemetryRunner(c)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { runner.Run(ctx); close(done) }()
			client := http.Client{Timeout: time.Second}
			deadline := time.Now().Add(2 * time.Second)
			for {
				response, err := client.Get("http://" + address + "/metrics")
				if err == nil {
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						t.Fatal("metrics failed")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancel blocked")
			}
		})
	}
}
