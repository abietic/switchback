package capacitysnapshot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

type qualificationFakeRoundTrip func(*http.Request) (*http.Response, error)

func (f qualificationFakeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type qualificationShortBody struct{ once bool }

func (b *qualificationShortBody) Read(p []byte) (int, error) {
	if b.once {
		return 0, io.ErrUnexpectedEOF
	}
	b.once = true
	return copy(p, []byte(`{"Id":"partial`)), nil
}
func (*qualificationShortBody) Close() error { return nil }

func TestQualificationEngineTransportBoundsOrdinaryBodies(t *testing.T) {
	id := strings.Repeat("a", 64)
	valid := `{"Id":"` + id + `","Image":"sha256:` + strings.Repeat("b", 64) + `","State":{"Status":"created","Pid":0},"Config":{"Labels":{}},"HostConfig":{},"Mounts":[]}`
	for _, test := range []struct {
		name   string
		status int
		length int64
		body   func() io.ReadCloser
		cancel bool
		wantOK bool
	}{
		{"valid-sdk-inspect", 200, int64(len(valid)), func() io.ReadCloser { return io.NopCloser(strings.NewReader(valid)) }, false, true},
		{"content-length-oversize", 200, qualificationEngineBodyLimit + 1, func() io.ReadCloser { return io.NopCloser(strings.NewReader(valid)) }, false, false},
		{"chunked-oversize", 200, -1, func() io.ReadCloser {
			return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), qualificationEngineBodyLimit+1)))
		}, false, false},
		{"short-read", 200, -1, func() io.ReadCloser { return &qualificationShortBody{} }, false, false},
		{"cancelled", 200, int64(len(valid)), func() io.ReadCloser { return io.NopCloser(strings.NewReader(valid)) }, true, false},
		{"oversized-404-cannot-be-absence", 404, -1, func() io.ReadCloser {
			return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), qualificationEngineBodyLimit+1)))
		}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := qualificationFakeRoundTrip(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || !strings.Contains(req.URL.Path, "/containers/"+id+"/json") {
					t.Fatal("unexpected Engine operation", req.URL.Path)
				}
				return &http.Response{StatusCode: test.status, Body: test.body(), ContentLength: test.length, Header: make(http.Header), Request: req}, nil
			})
			hc := &http.Client{Transport: qualificationBoundedEngineTransport{base: base}}
			engine, err := client.New(client.WithHost(qualificationEngineSocket), client.WithHTTPClient(hc), client.WithAPIVersion("1.55"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancel {
				cancel()
			} else {
				defer cancel()
			}
			result, err := engine.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if test.wantOK {
				if err != nil || result.Container.ID != id {
					t.Fatalf("bounded real SDK decode failed: %v", err)
				}
			} else if err == nil || cerrdefs.IsNotFound(err) {
				t.Fatalf("bad response became success/404: %v", err)
			}
		})
	}
}

func TestQualificationEngineTransportCancelDuringBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := qualificationFakeRoundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: -1, Body: io.NopCloser(qualificationCancelReader{cancel})}, nil
	})
	_, err := (qualificationBoundedEngineTransport{base: base}).RoundTrip((&http.Request{}).WithContext(ctx))
	if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancellation not rejected")
	}
}

type qualificationCancelReader struct{ cancel context.CancelFunc }

func (r qualificationCancelReader) Read(p []byte) (int, error) { r.cancel(); return 0, io.EOF }
