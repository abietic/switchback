package queryevidence_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	qe "github.com/abietic/switchback/test/acceptance/observability/queryevidence"
)

func TestHTTPClientPreservesCopyAndFallback(t *testing.T) {
	fallbackTransport := boundaryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(&literalReader{})}, nil
	})
	sentinel := errors.New("redirect stopped")
	fallback := &http.Client{Timeout: 37 * time.Millisecond, Transport: fallbackTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return sentinel }}
	copied := qe.HTTPClient(context.Background(), fallback)
	if copied == fallback || copied.Timeout != 37*time.Millisecond {
		t.Fatal("client not copied with timeout")
	}
	if copied.CheckRedirect(nil, nil) != sentinel {
		t.Fatal("redirect policy changed")
	}
	if get(t, copied, "http://127.0.0.1:1/api/v1/query") != "literal" {
		t.Fatal("fallback transport changed")
	}
	override := boundaryTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("override") })
	overridden := qe.HTTPClient(qe.WithTransport(context.Background(), override), fallback)
	request, _ := http.NewRequest("GET", "http://127.0.0.1:1/api/v1/query", nil)
	if _, err := overridden.Transport.RoundTrip(request); err == nil || err.Error() != "override" {
		t.Fatal("context transport not selected")
	}
	overridden.Timeout = time.Hour
	if fallback.Timeout != 37*time.Millisecond || get(t, fallback, request.URL.String()) != "literal" {
		t.Fatal("fallback mutated")
	}
	if qe.HTTPClient(context.Background(), nil) == http.DefaultClient {
		t.Fatal("default client not copied")
	}
}

type literalReader struct{ done bool }

func (r *literalReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, "literal"), io.EOF
}

func TestConcurrentPublicGETsRemainConsistent(t *testing.T) {
	binding, closeServers := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.URL.Query().Get("value")) })
	directory := filepath.Join(canonicalTempDir(t), "q")
	recorder, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	run := func(transport http.RoundTripper) {
		client := qe.HTTPClient(qe.WithTransport(context.Background(), transport), &http.Client{Timeout: 3 * time.Second})
		var wg sync.WaitGroup
		failures := make(chan error, 24)
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				want := fmt.Sprintf("response-%d", i)
				res, err := client.Get(binding.Origins["prometheus"] + "/api/v1/query?value=" + want)
				if err != nil {
					failures <- err
					return
				}
				raw, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil || res.StatusCode != 200 || string(raw) != want {
					failures <- fmt.Errorf("query %d response mismatch", i)
				}
			}(i)
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
	}
	run(recorder)
	if err := recorder.Seal(); err != nil {
		t.Fatal(err)
	}
	closeServers()
	before := snapshot(t, directory)
	replay, err := qe.OpenReplay(directory, binding)
	if err != nil {
		t.Fatal(err)
	}
	run(replay)
	if err := replay.AssertConsumed(); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(t, directory, before)
}
