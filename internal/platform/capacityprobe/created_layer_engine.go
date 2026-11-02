package capacityprobe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"time"
)

const createdLayerPrivateSocket = "/run/switchback-engine/docker.sock"

var createdLayerInspectRequest = regexp.MustCompile(`^/v1\.56/containers/[0-9a-f]{64}/json$`)

// NewCreatedLayerEngineReader has no environment endpoint, proxy, credentials,
// redirect, TCP/DNS dial or legacy socket fallback. It only returns bounded GETs.
func NewCreatedLayerEngineReader() func(context.Context, string) ([]byte, error) {
	read := newCreatedLayerEngineReader((&net.Dialer{Timeout: 2 * time.Second}).DialContext)
	return func(ctx context.Context, p string) ([]byte, error) {
		if runtime.GOOS != "linux" || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") {
			return nil, createdLayerFailure("unsupported_platform", nil)
		}
		return read(ctx, p)
	}
}

// The test seam replaces only the dial operation; the production HTTP transport,
// method/path checks, redirect policy, limits and privacy handling still execute.
func newCreatedLayerEngineReader(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string) ([]byte, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: MaxResponse, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx, "unix", createdLayerPrivateSocket)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}
	return func(ctx context.Context, p string) ([]byte, error) {
		if createdLayerContext(ctx) != nil || (p != "/v1.56/version" && p != "/v1.56/info" && !createdLayerInspectRequest.MatchString(p)) {
			return nil, createdLayerFailure("engine_request", createdLayerContext(ctx))
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://created-layer-engine"+p, nil)
		if e != nil {
			return nil, createdLayerFailure("engine_request", e)
		}
		response, e := client.Do(req)
		if e != nil {
			return nil, createdLayerFailure("engine_transport", e)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, createdLayerFailure("engine_status", nil)
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
		if e != nil {
			return nil, createdLayerFailure("engine_body", e)
		}
		if len(raw) > MaxResponse {
			return nil, createdLayerFailure("engine_size", nil)
		}
		if e = createdLayerContext(ctx); e != nil {
			return nil, createdLayerFailure("engine_context", e)
		}
		return raw, nil
	}
}
