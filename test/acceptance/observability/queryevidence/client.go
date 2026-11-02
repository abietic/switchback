package queryevidence

import (
	"context"
	"net/http"
)

type transportKey struct{}

// WithTransport selects query recording or replay without changing existing callers.
func WithTransport(ctx context.Context, transport http.RoundTripper) context.Context {
	return context.WithValue(ctx, transportKey{}, transport)
}

// HTTPClient copies the fallback and preserves its timeout and redirect policy.
func HTTPClient(ctx context.Context, fallback *http.Client) *http.Client {
	if fallback == nil {
		fallback = http.DefaultClient
	}
	client := *fallback
	if transport, ok := ctx.Value(transportKey{}).(http.RoundTripper); ok {
		client.Transport = transport
	}
	return &client
}
