package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const maxRequestBytes = 1 << 20
const maxFaultIngestionBytes = 16 << 20

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	control := flag.String("control", "", "test-only status or release")
	key := flag.String("fixture-key", "", "exact fixture key")
	digest := flag.String("evidence-digest", "", "pre-release persisted evidence digest")
	flag.Parse()
	if *control != "" {
		if err := controlRequest(*control, *key, *digest); err != nil {
			log.Fatal(err)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: *listen, Handler: newFixtureHandler(), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { <-ctx.Done(); _ = server.Close() }()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newFixtureHandler() http.Handler {
	var sequence atomic.Uint64
	registry := &barrierRegistry{records: map[string]*barrier{}}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	registry.instance = hex.EncodeToString(nonce[:])
	handler := http.NewServeMux()
	handler.HandleFunc("/fixture/status", registry.status)
	handler.HandleFunc("/fixture/register", registry.register)
	handler.HandleFunc("/fixture/release", registry.release)
	handler.HandleFunc("/fixture/result", registry.result)
	fault := &faultRegistry{instance: registry.instance}
	handler.HandleFunc("/fixture/fault", fault.control)
	target, _ := url.Parse("http://langfuse-web:3000")
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Avoid the standard reverse proxy error logger: it includes arbitrary URLs.
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "fixture upstream unavailable", 502)
	}
	handler.Handle("/api/public/", fault.handler(proxy))
	handler.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	handler.HandleFunc("/v1/responses", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
		if err != nil || len(body) > maxRequestBytes {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model  string `json:"model"`
			Input  any    `json:"input"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Model == "" || payload.Input == nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		call := sequence.Add(1)
		if payload.Stream {
			registry.stream(writer, request, payload.Input)
			return
		}
		logEvent(map[string]any{
			"path":                  request.URL.Path,
			"call":                  call,
			"authorization_present": request.Header.Get("Authorization") != "",
		})
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id":         fmt.Sprintf("resp_fake_%d", call),
			"object":     "response",
			"created_at": time.Now().Unix(),
			"status":     "completed",
			"model":      "switchback-integration-fake",
			"output": []any{map[string]any{
				"id":     fmt.Sprintf("msg_fake_%d", call),
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
				"content": []any{map[string]any{
					"type":        "output_text",
					"text":        fmt.Sprintf("deterministic fake response %d", call),
					"annotations": []any{},
				}},
			}},
			"usage": map[string]int64{
				"input_tokens":  11,
				"output_tokens": 7,
				"total_tokens":  18,
			},
		})
	})
	return handler
}

type fixtureScope struct {
	Nonce      string `json:"nonce"`
	Session    string `json:"worker_session_id"`
	Runtime    string `json:"runtime_id"`
	Generation uint32 `json:"runtime_generation"`
	Turn       string `json:"agent_turn_id"`
	Ordinal    int    `json:"call_ordinal"`
}
type fixtureRecord struct {
	Instance          string       `json:"instance"`
	Key               string       `json:"key"`
	Scope             fixtureScope `json:"scope"`
	ExpectedID        string       `json:"expected_observation_id"`
	EnteredAt         time.Time    `json:"entered_at"`
	RegisteredAt      time.Time    `json:"registered_at"`
	ReleasedAt        time.Time    `json:"released_at"`
	CompletedAt       time.Time    `json:"completed_at"`
	ReaderCompletedAt time.Time    `json:"reader_completed_at"`
	ReleaseDigest     string       `json:"release_digest"`
	Failure           string       `json:"failure,omitempty"`
}
type barrier struct {
	record    fixtureRecord
	release   chan struct{}
	connected bool
}
type barrierRegistry struct {
	mu       sync.Mutex
	instance string
	records  map[string]*barrier
}
type controlInput struct {
	Key        string `json:"key"`
	ExpectedID string `json:"expected_observation_id"`
	Digest     string `json:"evidence_digest"`
	Completed  bool   `json:"completed"`
}

func decodeControl(w http.ResponseWriter, r *http.Request) (controlInput, bool) {
	var v controlInput
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return v, false
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 4097))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || len(v.Key) > 2048 || v.Key == "" {
		http.Error(w, "invalid control", 400)
		return v, false
	}
	return v, true
}
func loopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}
func (b *barrierRegistry) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !loopback(r) {
		http.Error(w, "local status only", 403)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := []fixtureRecord{}
	for _, v := range b.records {
		result = append(result, v.record)
	}
	_ = json.NewEncoder(w).Encode(result)
}
func (b *barrierRegistry) register(w http.ResponseWriter, r *http.Request) {
	v, ok := decodeControl(w, r)
	if !ok {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.records[v.Key]
	if entry == nil || !entry.connected || entry.record.Failure != "" || v.ExpectedID == "" || len(v.ExpectedID) > 256 || strings.ContainsAny(v.ExpectedID, "\r\n\t ") || (!entry.record.ReleasedAt.IsZero()) || (entry.record.ExpectedID != "" && entry.record.ExpectedID != v.ExpectedID) {
		http.Error(w, "registration conflict", 409)
		return
	}
	if entry.record.ExpectedID == "" {
		entry.record.ExpectedID = v.ExpectedID
		entry.record.RegisteredAt = time.Now().UTC()
	}
	_ = json.NewEncoder(w).Encode(entry.record)
}
func (b *barrierRegistry) release(w http.ResponseWriter, r *http.Request) {
	if !loopback(r) {
		http.Error(w, "local release only", 403)
		return
	}
	v, ok := decodeControl(w, r)
	if !ok {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.records[v.Key]
	if entry == nil || entry.record.ExpectedID == "" || entry.record.Failure != "" || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(v.Digest) || (entry.record.ReleaseDigest != "" && entry.record.ReleaseDigest != v.Digest) {
		http.Error(w, "release conflict", 409)
		return
	}
	if entry.record.ReleaseDigest == "" {
		if !entry.connected {
			http.Error(w, "stream disconnected", 409)
			return
		}
		entry.record.ReleaseDigest = v.Digest
		entry.record.ReleasedAt = time.Now().UTC()
		close(entry.release)
	}
	_ = json.NewEncoder(w).Encode(entry.record)
}
func (b *barrierRegistry) result(w http.ResponseWriter, r *http.Request) {
	v, ok := decodeControl(w, r)
	if !ok {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.records[v.Key]
	if entry == nil || entry.record.ExpectedID != v.ExpectedID {
		http.Error(w, "result conflict", 409)
		return
	}
	if !v.Completed {
		entry.record.Failure = "reader_failed"
	} else if entry.record.CompletedAt.IsZero() || entry.record.Failure != "" {
		http.Error(w, "completion not emitted", 409)
		return
	} else {
		entry.record.ReaderCompletedAt = time.Now().UTC()
	}
	w.WriteHeader(204)
}
func (b *barrierRegistry) stream(w http.ResponseWriter, r *http.Request, input any) {
	// LiteLLM may normalize a string input into a message array; search only
	// exact synthetic marker strings, never interpret arbitrary body content.
	var marker string
	var visit func(any)
	visit = func(v any) {
		switch v := v.(type) {
		case string:
			if strings.HasPrefix(v, "switchback-obsv-stream:") {
				marker = strings.TrimPrefix(v, "switchback-obsv-stream:")
			}
		case []any:
			for _, v := range v {
				visit(v)
			}
		case map[string]any:
			for _, v := range v {
				visit(v)
			}
		}
	}
	visit(input)
	raw, err := base64.RawURLEncoding.DecodeString(marker)
	var scope fixtureScope
	if err != nil || len(raw) > 1024 || json.Unmarshal(raw, &scope) != nil || scope.Nonce == "" || scope.Session == "" || scope.Runtime == "" || scope.Turn == "" || scope.Generation == 0 || scope.Ordinal != 1 {
		http.Error(w, "invalid fixture scope", 400)
		return
	}
	b.mu.Lock()
	if _, ok := b.records[marker]; ok || len(b.records) >= 1024 {
		b.mu.Unlock()
		http.Error(w, "duplicate or full fixture", 409)
		return
	}
	entry := &barrier{record: fixtureRecord{Instance: b.instance, Key: marker, Scope: scope, EnteredAt: time.Now().UTC()}, release: make(chan struct{}), connected: true}
	b.records[marker] = entry
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		entry.connected = false
		if entry.record.CompletedAt.IsZero() {
			entry.record.Failure = "stream_disconnected"
		}
		b.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	responseID := "resp_" + scope.Turn
	response := map[string]any{"id": responseID, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress", "model": "switchback-integration-fake", "output": []any{}, "error": nil, "incomplete_details": nil}
	write := func(kind string, seq int, extra map[string]any) bool {
		event := map[string]any{"type": kind, "sequence_number": seq}
		for k, v := range extra {
			event[k] = v
		}
		data, _ := json.Marshal(event)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
			return false
		}
		f, ok := w.(http.Flusher)
		if !ok {
			return false
		}
		f.Flush()
		return true
	}
	if !write("response.created", 0, map[string]any{"response": response}) {
		return
	}
	select {
	case <-entry.release:
	case <-r.Context().Done():
		return
	}
	text := "deterministic late response " + scope.Turn
	content := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
	item := map[string]any{"id": "msg_" + scope.Turn, "type": "message", "status": "completed", "role": "assistant", "content": []any{content}}
	response["status"] = "completed"
	response["output"] = []any{item}
	response["usage"] = map[string]any{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}
	// Ledger completion is committed before bytes, so the reader can report
	// success immediately after receiving the event without racing this write.
	b.mu.Lock()
	entry.record.CompletedAt = time.Now().UTC()
	b.mu.Unlock()
	if !write("response.completed", 1, map[string]any{"response": response}) {
		b.mu.Lock()
		entry.record.Failure = "write_failed"
		b.mu.Unlock()
		return
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func controlRequest(mode, key, digest string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	method, path := http.MethodGet, "/fixture/status"
	var body io.Reader
	if mode == "release" {
		method, path = http.MethodPost, "/fixture/release"
		data, _ := json.Marshal(controlInput{Key: key, Digest: digest})
		body = bytes.NewReader(data)
	} else if mode == "fault" {
		method, path = http.MethodPost, "/fixture/fault"
		data, _ := json.Marshal(map[string]string{"block": key})
		body = bytes.NewReader(data)
	} else if mode == "fault-status" {
		path = "/fixture/fault"
	} else if mode != "status" {
		return fmt.Errorf("unsupported control")
	}
	r, err := http.NewRequest(method, "http://127.0.0.1:8080"+path, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("fixture control unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("fixture control status %d", res.StatusCode)
	}
	_, err = io.Copy(os.Stdout, io.LimitReader(res.Body, 1<<20))
	return err
}

type faultRecord struct {
	Instance    string    `json:"instance"`
	ID          uint64    `json:"request_id"`
	Kind        string    `json:"kind"`
	ConnectedAt time.Time `json:"connected_at"`
	CancelledAt time.Time `json:"cancelled_at"`
	RecoveredAt time.Time `json:"recovered_at"`
}
type faultRegistry struct {
	mu              sync.Mutex
	instance, block string
	sequence        uint64
	records         []*faultRecord
}

func (f *faultRegistry) control(w http.ResponseWriter, r *http.Request) {
	if !loopback(r) {
		http.Error(w, "local fault control only", 403)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPost {
		var v struct {
			Block string `json:"block"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 128)).Decode(&v) != nil || (v.Block != "ingestion" && v.Block != "query" && v.Block != "off") {
			http.Error(w, "invalid fault", 400)
			return
		}
		f.block = v.Block
		if v.Block == "off" {
			now := time.Now().UTC()
			for _, record := range f.records {
				if record.RecoveredAt.IsZero() {
					record.RecoveredAt = now
				}
			}
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	_ = json.NewEncoder(w).Encode(f.records)
}
func (f *faultRegistry) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := ""
		if r.Method == http.MethodPost && r.URL.Path == "/api/public/ingestion" {
			kind = "ingestion"
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/public/") {
			kind = "query"
		}
		f.mu.Lock()
		if kind == "" || kind != f.block {
			f.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}
		if len(f.records) >= 8192 {
			f.mu.Unlock()
			http.Error(w, "fault ledger full", 503)
			return
		}
		f.sequence++
		record := &faultRecord{Instance: f.instance, ID: f.sequence, Kind: kind, ConnectedAt: time.Now().UTC()}
		f.records = append(f.records, record)
		f.mu.Unlock()
		if kind == "ingestion" {
			// A server that leaves an HTTP/1 request body unread may not observe
			// the caller closing its connection while waiting for a response.
			// Discard only a bounded amount; never retain or inspect its contents.
			read, _ := io.Copy(io.Discard, io.LimitReader(r.Body, maxFaultIngestionBytes+1))
			if read > maxFaultIngestionBytes {
				http.Error(w, "fault ingestion body too large", http.StatusRequestEntityTooLarge)
				return
			}
		}
		<-r.Context().Done()
		f.mu.Lock()
		record.CancelledAt = time.Now().UTC()
		f.mu.Unlock()
	})
}

func logEvent(event map[string]any) {
	encoded, err := json.Marshal(event)
	if err == nil {
		_, _ = fmt.Fprintf(os.Stderr, "SWITCHBACK_FAKE_OPENAI %s\n", encoded)
	}
}
