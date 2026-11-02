package capacityprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCreatedLayerEngineRejectsRequestsBeforeDial(t *testing.T) {
	reads := 0
	read := newCreatedLayerEngineReader(func(context.Context, string, string) (net.Conn, error) {
		reads++
		return nil, errors.New("unexpected dial")
	})
	for _, p := range []string{"", "/v1.55/info", "/v1.56/containers/json?all=1", "/v1.56/info?x=secret", "/v1.56/info#fragment", "//v1.56/info", "http://evil/v1.56/info", "/v1.56/containers/" + strings.Repeat("A", 64) + "/json", "/v1.56/containers/" + nativeCID + "/json?x=1", "/v1.56/containers/../info", "/v1.56/containers/%2e%2e/json", "/v1.56/containers/" + nativeCID + "/json/"} {
		if _, e := read(context.Background(), p); e == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := read(ctx, "/v1.56/info"); !errors.Is(e, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	if _, e := read(nil, "/v1.56/info"); e == nil {
		t.Fatal("nil context")
	}
	if reads != 0 {
		t.Fatal("invalid request caused IO")
	}
}
func pipeLayerReader(t *testing.T, response string) (func(context.Context, string) ([]byte, error), *int, <-chan struct{}) {
	t.Helper()
	calls := 0
	done := make(chan struct{})
	read := newCreatedLayerEngineReader(func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls++
		if calls != 1 {
			t.Error("fallback/redirect dial")
		}
		if network != "unix" || addr != "/run/switchback-engine/docker.sock" {
			t.Error("non-fixed transport")
		}
		client, server := net.Pipe()
		go func() {
			defer close(done)
			defer server.Close()
			req, e := http.ReadRequest(bufio.NewReader(server))
			if e != nil {
				t.Error("request read", e)
				return
			}
			defer req.Body.Close()
			if req.Method != "GET" || req.Host != "created-layer-engine" || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "" || req.Header.Get("Proxy-Authorization") != "" {
				t.Error("request authority/method changed")
			}
			_, _ = io.WriteString(server, response)
		}()
		return client, nil
	})
	return read, &calls, done
}
func awaitLayerPipe(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("transport goroutine leak")
	}
}
func TestCreatedLayerEngineFixedGETAndPrivateFailures(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://network-canary:2375")
	t.Setenv("HTTP_PROXY", "http://proxy-canary:9999")
	t.Setenv("SWITCHBACK_ENGINE_SOCKET", "/secret-fallback.sock")
	for _, p := range []string{"/v1.56/version", "/v1.56/info", "/v1.56/containers/" + nativeCID + "/json"} {
		t.Run(p, func(t *testing.T) {
			r, c, done := pipeLayerReader(t, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
			b, e := r(context.Background(), p)
			awaitLayerPipe(t, done)
			if e != nil || string(b) != "{}" || *c != 1 {
				t.Fatalf("GET failed %s %v", b, e)
			}
		})
	}
	cases := map[string]string{
		"redirect":   "HTTP/1.1 302 Found\r\nLocation: http://secret-redirect-canary/elsewhere\r\nContent-Length: 16\r\n\r\nSECRET-BODY-TEST",
		"status":     "HTTP/1.1 500 Internal Server Error\r\nContent-Length: 16\r\n\r\nSECRET-BODY-TEST",
		"oversize":   fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", MaxResponse+1, strings.Repeat("x", MaxResponse+1)),
		"truncated":  "HTTP/1.1 200 OK\r\nContent-Length: 999\r\n\r\nSECRET-BODY-TEST",
		"bad-header": "HTTP/1.1 200 OK\r\nSecret-Header-Canary\r\n\r\n",
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			r, c, done := pipeLayerReader(t, response)
			b, e := r(context.Background(), "/v1.56/info")
			awaitLayerPipe(t, done)
			if e == nil || b != nil || *c != 1 {
				t.Fatal("response accepted or fallback attempted")
			}
			if strings.Contains(strings.ToLower(e.Error()), "secret") {
				t.Fatal("transport/body canary leaked")
			}
			var typed *createdLayerReadFailure
			if !errors.As(e, &typed) {
				t.Fatal("untyped error")
			}
		})
	}
	t.Run("exact-limit", func(t *testing.T) {
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", MaxResponse, strings.Repeat("x", MaxResponse))
		r, _, done := pipeLayerReader(t, response)
		b, e := r(context.Background(), "/v1.56/version")
		awaitLayerPipe(t, done)
		if e != nil || len(b) != MaxResponse {
			t.Fatal("bounded response rejected")
		}
	})
}
func TestCreatedLayerEngineErrnoAndInflightCancellation(t *testing.T) {
	calls := 0
	r := newCreatedLayerEngineReader(func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, fmt.Errorf("secret-path-token: %w", syscall.ECONNREFUSED)
	})
	_, e := r(context.Background(), "/v1.56/info")
	var n syscall.Errno
	if !errors.As(e, &n) || n != syscall.ECONNREFUSED || strings.Contains(e.Error(), "secret") || calls != 1 {
		t.Fatalf("unsafe/lost transport failure %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r = newCreatedLayerEngineReader(func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer close(done)
			defer b.Close()
			_, _ = http.ReadRequest(bufio.NewReader(b))
			cancel()
			var buf [1]byte
			_, _ = b.Read(buf[:])
		}()
		return a, nil
	})
	_, e = r(ctx, "/v1.56/info")
	awaitLayerPipe(t, done)
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("inflight cancellation not propagated %v", e)
	}
}
