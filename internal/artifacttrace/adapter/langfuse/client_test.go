package langfuse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientUsesBasicAuthWithoutExposingCredentialsInDiagnostics(t *testing.T) {
	t.Parallel()
	const publicKey = "pk-known-token"
	const secretKey = "sk-known-token"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPublic, gotSecret, ok := request.BasicAuth()
		if !ok || gotPublic != publicKey || gotSecret != secretKey {
			t.Errorf("BasicAuth() = %q, %q, %t", gotPublic, gotSecret, ok)
		}
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("upstream accidentally echoed " + secretKey))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, PublicKey: publicKey, SecretKey: secretKey})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.Ingest(context.Background(), IngestionBatch{Batch: []IngestionEvent{{
		ID: "event-1", Type: "trace-create", Body: map[string]any{"id": "trace-1"},
	}}})
	combined := fmt.Sprintf("%v %s %#v", err, client, client)
	if err == nil || strings.Contains(combined, publicKey) || strings.Contains(combined, secretKey) {
		t.Fatalf("unsafe diagnostics = %q", combined)
	}
}

func TestClientDurableReadsUseBoundedSourceScopedEndpoints(t *testing.T) {
	t.Parallel()
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.URL.RequestURI()
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.EscapedPath() {
		case "/api/public/traces/trace%2Fid":
			_ = json.NewEncoder(writer).Encode(Trace{ID: "trace/id", Input: json.RawMessage(`{"large":9007199254740993}`)})
		case "/api/public/observations/observation%2Fid":
			_ = json.NewEncoder(writer).Encode(Observation{ID: "observation/id", Model: "model", StartTime: time.Unix(1, 0).UTC(), Usage: Usage{Input: 2, Output: 3, Total: 5, Unit: "TOKENS"}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, PublicKey: "pk", SecretKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := client.GetTraceRoot(context.Background(), "trace/id")
	if err != nil || trace.ID != "trace/id" {
		t.Fatalf("trace=%#v error=%v", trace, err)
	}
	observation, err := client.GetObservation(context.Background(), "observation/id")
	if err != nil || observation.Model != "model" || observation.Usage.Input != 2 {
		t.Fatalf("observation=%#v error=%v", observation, err)
	}
	if got := <-requests; got != "/api/public/traces/trace%2Fid?fields=core%2Cio" {
		t.Fatalf("trace request=%q", got)
	}
	if got := <-requests; got != "/api/public/observations/observation%2Fid" {
		t.Fatalf("observation request=%q", got)
	}
}

func TestClientRejectsBaseURLWithEmbeddedAuthorityOrPath(t *testing.T) {
	t.Parallel()
	for _, baseURL := range []string{
		"https://user:password@langfuse.example", "https://langfuse.example/api", "file:///tmp/langfuse",
	} {
		if _, err := NewClient(ClientOptions{BaseURL: baseURL, PublicKey: "pk", SecretKey: "sk"}); err == nil {
			t.Fatalf("NewClient(%q) succeeded", baseURL)
		}
	}
}
