package localproduct

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
	"github.com/moby/moby/client"
)

type uninterruptibleWorkerReader struct {
	release <-chan struct{}
	entered chan struct{}
}

func (r uninterruptibleWorkerReader) Read([]byte) (int, error) {
	close(r.entered)
	<-r.release
	return 0, io.EOF
}
func (r uninterruptibleWorkerReader) Close() error { return nil }

type uninterruptibleWorkerDocker struct {
	*workerDockerFixture
	release <-chan struct{}
	entered chan struct{}
}

func (d uninterruptibleWorkerDocker) ContainerLogs(_ context.Context, id string, _ client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	d.opened <- id
	return uninterruptibleWorkerReader{d.release, d.entered}, nil
}
func TestTelemetryWorkerShutdownBoundDisclosesUncooperativeReader(t *testing.T) {
	a := fixtureWorkerAuthority("runtime", 1)
	release := make(chan struct{})
	defer close(release)
	docker := uninterruptibleWorkerDocker{workerDockerFixture: &workerDockerFixture{opened: make(chan string, 1), labels: map[string]map[string]string{"runtime": fixtureWorkerLabels(a)}}, release: release, entered: make(chan struct{})}
	metrics := telemetry.NewMetrics()
	c := workerOutputCollector{discover: func(context.Context) ([]workerOutputAuthority, error) { return []workerOutputAuthority{a}, nil }, docker: docker, emit: func(telemetry.Event) {}, metrics: metrics}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.run(ctx); close(done) }()
	<-docker.opened
	<-docker.entered
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("shutdown exceeded bounded wait")
	}
	if time.Since(start) < workerOutputShutdown {
		t.Fatal("unclosed reader reported completed")
	}
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(response.Body.String(), `switchback_telemetry_worker_gaps_total{environment="local",reason="shutdown",service="switchback-backend"} 1`) {
		t.Fatal("shutdown gap not disclosed")
	}
}

func TestTelemetryWorkerCollectorFailedAndStaleAuthorityCloseReaders(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmtUint(uint64(boolInt(stale))), func(t *testing.T) {
			a := fixtureWorkerAuthority("runtime", 1)
			docker := &workerDockerFixture{opened: make(chan string, 10), writers: map[string]*io.PipeWriter{}, labels: map[string]map[string]string{"runtime": fixtureWorkerLabels(a)}}
			var calls int
			collector := workerOutputCollector{discover: func(ctx context.Context) ([]workerOutputAuthority, error) {
				calls++
				if calls == 1 {
					return []workerOutputAuthority{a}, nil
				}
				if stale {
					select {
					case <-ctx.Done():
					case <-time.After(200 * time.Millisecond):
					}
				}
				return nil, errors.New("credential-SENTINEL")
			}, docker: docker, emit: func(telemetry.Event) {}, metrics: telemetry.NewMetrics(), interval: 20 * time.Millisecond, freshness: 80 * time.Millisecond}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { collector.run(ctx); close(done) }()
			select {
			case <-docker.opened:
			case <-time.After(time.Second):
				t.Fatal("no initial reader")
			}
			docker.mu.Lock()
			writer := docker.writers["runtime"]
			docker.mu.Unlock()
			closed := make(chan error, 1)
			go func() { _, err := writer.Write([]byte{1}); closed <- err }()
			// The first byte may be consumed before authority expires; the second
			// write verifies the actual blocked header boundary is closed.
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("first byte blocked")
			}
			time.Sleep(120 * time.Millisecond)
			go func() { _, err := writer.Write([]byte{0}); closed <- err }()
			select {
			case err := <-closed:
				if err == nil {
					t.Fatal("expired authority reader open")
				}
			case <-time.After(time.Second):
				t.Fatal("expired read not closed")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("collector shutdown blocked")
			}
		})
	}
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestTelemetryWorkerCollectorCapacityIsBounded(t *testing.T) {
	docker := &workerDockerFixture{opened: make(chan string, 20), writers: map[string]*io.PipeWriter{}, labels: map[string]map[string]string{}}
	var candidates []workerOutputAuthority
	for i := 0; i < 11; i++ {
		a := fixtureWorkerAuthority("runtime"+fmtUint(uint64(i)), 1)
		a.scope.WorkerSessionID = "session" + fmtUint(uint64(i))
		candidates = append(candidates, a)
		docker.labels[a.containerID] = fixtureWorkerLabels(a)
	}
	source := &workerAuthorityFixture{candidates: candidates}
	metrics := telemetry.NewMetrics()
	collector := workerOutputCollector{discover: source.discover, docker: docker, emit: func(telemetry.Event) {}, metrics: metrics, interval: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { collector.run(ctx); close(done) }()
	for i := 0; i < 10; i++ {
		select {
		case <-docker.opened:
		case <-time.After(time.Second):
			t.Fatal("missing admitted reader")
		}
	}
	select {
	case <-docker.opened:
		t.Fatal("reader cap exceeded")
	case <-time.After(60 * time.Millisecond):
	}
	cancel()
	<-done
}

type workerAuthorityFixture struct {
	mu         sync.Mutex
	candidates []workerOutputAuthority
	err        error
}

func (s *workerAuthorityFixture) discover(context.Context) ([]workerOutputAuthority, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workerOutputAuthority(nil), s.candidates...), s.err
}

type workerDockerFixture struct {
	mu      sync.Mutex
	opened  chan string
	writers map[string]*io.PipeWriter
	labels  map[string]map[string]string
}

func (d *workerDockerFixture) inspect(_ context.Context, a workerOutputAuthority) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return false, verifyWorkerOutputLabels(a, a.containerID, d.labels[a.containerID])
}
func (d *workerDockerFixture) ContainerLogs(_ context.Context, id string, o client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	if o.Tail != "0" || !o.Follow || !o.ShowStdout || !o.ShowStderr || o.Timestamps || o.Details {
		panic("unsafe log options")
	}
	r, w := io.Pipe()
	d.mu.Lock()
	d.writers[id] = w
	d.mu.Unlock()
	d.opened <- id
	return r, nil
}
func fixtureWorkerAuthority(id string, g uint64) workerOutputAuthority {
	return workerOutputAuthority{scope: telemetry.Scope{TenantID: "t", WorkspaceID: "w", WorkerSessionID: "s", RuntimeInstanceID: id, RuntimeGeneration: g}, containerID: id, resourceProfile: "resource@1:digest", agentProfile: "agent@1:digest", networkID: "network", networkRevision: 1, networkDigest: "digest", networkStatus: "active"}
}
func fixtureWorkerLabels(a workerOutputAuthority) map[string]string {
	return map[string]string{"com.switchback.managed": "worker-runtime", "com.switchback.worker-session-id": a.scope.WorkerSessionID, "com.switchback.runtime-instance-id": a.scope.RuntimeInstanceID, "com.switchback.runtime-generation": fmtUint(a.scope.RuntimeGeneration), "com.switchback.resource-profile": a.resourceProfile, "com.switchback.agent-profile": a.agentProfile, "com.switchback.network-policy-id": a.networkID, "com.switchback.network-policy-revision": fmtUint(a.networkRevision), "com.switchback.network-policy-digest": a.networkDigest, "com.switchback.network-enforcement-status": a.networkStatus, "com.switchback.worker-run-id": "old-run"}
}
func TestTelemetryWorkerCollectorReplacementClosesRealStream(t *testing.T) {
	a, b := fixtureWorkerAuthority("old", 1), fixtureWorkerAuthority("new", 2)
	source := &workerAuthorityFixture{candidates: []workerOutputAuthority{a}}
	docker := &workerDockerFixture{opened: make(chan string, 10), writers: map[string]*io.PipeWriter{}, labels: map[string]map[string]string{"old": fixtureWorkerLabels(a), "new": fixtureWorkerLabels(b)}}
	collector := workerOutputCollector{discover: source.discover, docker: docker, emit: func(telemetry.Event) {}, metrics: telemetry.NewMetrics(), interval: 20 * time.Millisecond, freshness: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { collector.run(ctx); close(done) }()
	await := func(want string) {
		t.Helper()
		select {
		case got := <-docker.opened:
			if got != want {
				t.Fatalf("opened %s", got)
			}
		case <-time.After(time.Second):
			t.Fatal("reader missing")
		}
	}
	await("old")
	source.mu.Lock()
	source.candidates = []workerOutputAuthority{b}
	source.mu.Unlock()
	await("new")
	docker.mu.Lock()
	old := docker.writers["old"]
	docker.mu.Unlock()
	if _, err := old.Write([]byte("secret")); err == nil {
		t.Fatal("old reader remained open")
	}
	source.mu.Lock()
	source.candidates = nil
	source.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	docker.mu.Lock()
	current := docker.writers["new"]
	docker.mu.Unlock()
	if _, err := current.Write([]byte("secret")); err == nil {
		t.Fatal("terminal reader remained open")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}

func TestTelemetryWorkerLabelsIgnoreStaleRunButRejectImmutableMismatch(t *testing.T) {
	a := fixtureWorkerAuthority("runtime", 1)
	labels := fixtureWorkerLabels(a)
	if err := verifyWorkerOutputLabels(a, a.containerID, labels); err != nil {
		t.Fatal(err)
	}
	for key := range labels {
		if key == "com.switchback.worker-run-id" {
			continue
		}
		t.Run(key, func(t *testing.T) {
			bad := fixtureWorkerLabels(a)
			bad[key] = "mismatch"
			if verifyWorkerOutputLabels(a, a.containerID, bad) == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
	if verifyWorkerOutputLabels(a, "another", labels) == nil {
		t.Fatal("wrong exact container accepted")
	}
}
