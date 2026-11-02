package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/secretcheck"
)

func event() Event {
	return Event{TS: time.Now().UTC(), Level: LevelInfo, Service: Service, Component: ComponentBackend, Event: EventBackendStarted, SchemaVersion: SchemaVersion}
}
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	return w.Body.String()
}

func TestEventRejectsHighCardinalityAndBadNumbers(t *testing.T) {
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: "http://localhost:12345/push", ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	e := event()
	e.Component = "dynamic-user-input"
	if s.Enqueue(e) {
		t.Fatal("accepted dynamic component")
	}
	e = event()
	e.Scope.WorkerRunID = strings.Repeat("x", 129)
	if s.Enqueue(e) {
		t.Fatal("accepted long identifier")
	}
	e = event()
	negative := int64(-1)
	e.Count = &negative
	if s.Enqueue(e) {
		t.Fatal("accepted negative count")
	}
	if metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "invalid"}) != 3 {
		t.Fatal(scrape(t, m))
	}
}

func TestSenderPushLabelsScannerAndGapRecovery(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	seen := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Error(err)
		}
		b, _ := json.Marshal(v)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		seen <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := NewMetrics()
	scanned := make(chan struct{}, 4)
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { scanned <- struct{}{}; return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	bad := event()
	bad.Level = "secret"
	if s.Enqueue(bad) {
		t.Fatal("bad accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	good := event()
	good.Scope.WorkerRunID = "run-1"
	if !s.Enqueue(good) {
		t.Fatal("good rejected")
	}
	select {
	case <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("push timeout")
	}
	select {
	case <-scanned:
	default:
		t.Fatal("scanner not invoked")
	}
	deadline := time.After(3 * time.Second)
	for metricValue(t, m, "switchback_telemetry_receiver_accepted_total", nil) != 2 {
		select {
		case <-deadline:
			t.Fatal(scrape(t, m))
		default:
		}
	}
	cancel()
	<-done
	mu.Lock()
	payload := strings.Join(bodies, "")
	mu.Unlock()
	if !strings.Contains(payload, "log_gap") || !strings.Contains(payload, "run-1") {
		t.Fatal(payload)
	}
	if metricValue(t, m, "switchback_telemetry_receiver_accepted_total", nil) != 2 {
		t.Fatal(scrape(t, m))
	}
	if s.Enqueue(good) {
		t.Fatal("accepted after shutdown")
	}
}

func TestSenderRejectsRedirectAndCancellationStopsHeldRequest(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(released)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	if !s.Enqueue(event()) {
		t.Fatal("enqueue")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not stop")
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("request not canceled")
	}
	s.Run(context.Background())
}

func TestSenderRedirectIsNotFollowed(t *testing.T) {
	redirected := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			redirected <- struct{}{}
			return
		}
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Enqueue(event())
	deadline := time.After(3 * time.Second)
	for metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "remote_rejected"}) != 1 {
		select {
		case <-deadline:
			t.Fatal(scrape(t, m))
		default:
		}
	}
	cancel()
	<-done
	select {
	case <-redirected:
		t.Fatal("followed redirect")
	default:
	}
}

func TestQueueBoundAndBatchMaximum(t *testing.T) {
	received := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var push struct {
			Streams []struct {
				Values [][2]string `json:"values"`
			} `json:"streams"`
		}
		if err := json.NewDecoder(r.Body).Decode(&push); err != nil {
			t.Error(err)
		}
		n := 0
		for _, stream := range push.Streams {
			n += len(stream.Values)
		}
		received <- n
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < QueueCapacity; i++ {
		if !s.Enqueue(event()) {
			t.Fatalf("queue rejected %d", i)
		}
	}
	if s.Enqueue(event()) {
		t.Fatal("overflow accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case n := <-received:
		if n > BatchMaximum {
			t.Fatalf("batch size %d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no batch")
	}
	cancel()
	<-done
	if metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "queue_full"}) != 1 {
		t.Fatal(scrape(t, m))
	}
}

func TestScannerFailureDoesNotSend(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called <- struct{}{} }))
	defer server.Close()
	m := NewMetrics()
	scanned := make(chan struct{}, 1)
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error {
		scanned <- struct{}{}
		return errors.New("secret value must stay local")
	}}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Enqueue(event())
	select {
	case <-scanned:
	case <-time.After(3 * time.Second):
		t.Fatal("scan not called")
	}
	deadline := time.After(3 * time.Second)
	for metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "scan_failed"}) != 1 {
		select {
		case <-deadline:
			t.Fatal(scrape(t, m))
		default:
		}
	}
	cancel()
	<-done
	select {
	case <-called:
		t.Fatal("scan failure sent")
	default:
	}
}

func TestKnownTokenScannerRejectsScopeBeforePush(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called <- struct{}{} }))
	defer server.Close()
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "test-token", Value: "secret12345"}})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: scanner.ScanJSON}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	e := event()
	e.Scope.WorkerRunID = "secret12345"
	if !s.Enqueue(e) {
		t.Fatal("enqueue should only validate grammar")
	}
	deadline := time.After(3 * time.Second)
	for metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "scan_failed"}) != 1 {
		select {
		case <-deadline:
			t.Fatal(scrape(t, m))
		default:
		}
	}
	cancel()
	<-done
	select {
	case <-called:
		t.Fatal("secret pushed")
	default:
	}
}

func TestSendTimeout(t *testing.T) {
	started := make(chan struct{})
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(released)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Enqueue(event())
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	deadline := time.After(3 * time.Second)
	for metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "transport_failed"}) != 1 {
		select {
		case <-deadline:
			t.Fatal(scrape(t, m))
		default:
		}
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("request remained active")
	}
	cancel()
	<-done
}

func TestConcurrentShutdownDiscardsBacklog(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < QueueCapacity; i++ {
		if !s.Enqueue(event()) {
			t.Fatalf("enqueue %d", i)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Enqueue(event())
			}
		}()
	}
	cancel()
	wg.Wait()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stuck")
	}
	if s.Enqueue(event()) {
		t.Fatal("accepted after shutdown")
	}
	if metricValue(t, m, "switchback_telemetry_queue_depth", nil) != 0 || metricValue(t, m, "switchback_telemetry_dropped_total", map[string]string{"reason": "shutdown"}) == 0 {
		t.Fatal(scrape(t, m))
	}
}

func TestGapPreservesDropsDuringSuccessfulSend(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	pushes := make(chan string, 2)
	var requests int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests++
		ordinal := requests
		mu.Unlock()
		if ordinal == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		pushes <- string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Enqueue(event())
	select {
	case <-firstStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first push not started")
	}
	bad := event()
	bad.Component = "unbounded"
	if s.Enqueue(bad) {
		t.Fatal("bad accepted")
	}
	close(releaseFirst)
	select {
	case body := <-pushes:
		if strings.Contains(body, "log_gap") {
			t.Fatal("gap included in already active push")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first push not completed")
	}
	if !s.Enqueue(event()) {
		t.Fatal("second enqueue")
	}
	select {
	case body := <-pushes:
		if !strings.Contains(body, "log_gap") {
			t.Fatal(body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second push not completed")
	}
	cancel()
	<-done
}

func TestSenderOwnsOptionalNumbersAfterEnqueue(t *testing.T) {
	received := make(chan Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var push struct {
			Streams []struct {
				Values [][2]string `json:"values"`
			} `json:"streams"`
		}
		if err := json.NewDecoder(r.Body).Decode(&push); err != nil {
			t.Error(err)
		}
		for _, stream := range push.Streams {
			for _, value := range stream.Values {
				var got Event
				if err := json.Unmarshal([]byte(value[1]), &got); err != nil {
					t.Error(err)
				}
				received <- got
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	duration, bytesValue, count := int64(12), int64(34), int64(7)
	e := event()
	e.DurationMillis = &duration
	e.Bytes = &bytesValue
	e.Count = &count
	if !s.Enqueue(e) {
		t.Fatal("enqueue")
	}
	duration, bytesValue, count = -1, -1, -1
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case got := <-received:
		if got.DurationMillis == nil || *got.DurationMillis != 12 || got.Bytes == nil || *got.Bytes != 34 || got.Count == nil || *got.Count != 7 {
			t.Fatalf("queued event changed: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted event was lost after producer mutation")
	}
	cancel()
	<-done
}

func TestConcurrentProducerReuseAfterEnqueue(t *testing.T) {
	const total = 400
	var delivered atomic.Int64
	complete := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var push struct {
			Streams []struct {
				Values [][2]string `json:"values"`
			} `json:"streams"`
		}
		if err := json.NewDecoder(r.Body).Decode(&push); err != nil {
			t.Error(err)
		}
		for _, stream := range push.Streams {
			if delivered.Add(int64(len(stream.Values))) >= total {
				once.Do(func() { close(complete) })
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := NewMetrics()
	s, err := NewSender(SenderOptions{PushURL: server.URL, ScanJSON: func(context.Context, string, any) error { return nil }}, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < total/4; i++ {
				n := int64(i)
				e := event()
				e.Count = &n
				if !s.Enqueue(e) {
					t.Error("enqueue rejected")
				}
				n = -1
			}
		}()
	}
	wg.Wait()
	select {
	case <-complete:
	case <-time.After(3 * time.Second):
		t.Fatalf("delivered %d events", delivered.Load())
	}
	cancel()
	<-done
}
