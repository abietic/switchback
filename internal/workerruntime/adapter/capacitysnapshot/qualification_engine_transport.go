package capacitysnapshot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/moby/moby/client"
)

const qualificationEngineBodyLimit = 512 << 10

// Moby's ordinary JSON decoder buffers raw responses without a body cap.
// Buffer the entire bounded response before handing it to the SDK, including
// non-2xx responses, so an oversized/truncated error cannot be classified as
// a valid not-found or successful cleanup. Hijacked attach uses its separate
// finite frame transport and never passes through this HTTP RoundTripper.
type qualificationBoundedEngineTransport struct {
	base http.RoundTripper
}

func (t qualificationBoundedEngineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.base == nil || req == nil || req.Context().Err() != nil {
		return nil, errQualification
	}
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errQualification
	}
	defer response.Body.Close()
	if response.ContentLength > qualificationEngineBodyLimit {
		return nil, fmt.Errorf("qualification Engine response oversized")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, qualificationEngineBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > qualificationEngineBodyLimit || req.Context().Err() != nil {
		return nil, fmt.Errorf("qualification Engine response oversized/cancelled")
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}

func qualificationFixedEngineClient() (*client.Client, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	base := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192,
		ResponseHeaderTimeout: 3 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", "/run/switchback-engine/docker.sock")
		}}
	httpClient := &http.Client{Transport: qualificationBoundedEngineTransport{base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errQualification }}
	// WithHost must set the SDK's fixed Unix identity before the bounded
	// transport replaces its default HTTP transport. The SDK's hijack path
	// still dials that same fixed Unix address and is independently bounded.
	return client.NewClientWithOpts(client.WithHost(qualificationEngineSocket), client.WithHTTPClient(httpClient), client.WithAPIVersionNegotiation())
}
