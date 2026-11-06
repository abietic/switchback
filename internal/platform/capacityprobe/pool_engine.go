package capacityprobe

import (
	"context"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"
)

const poolSocket = "/run/switchback-engine/docker.sock"

func poolEndpoint(p string) bool {
	if p == "/v1.56/version" || p == "/v1.56/info" || p == "/v1.56/containers/json?all=1" {
		return true
	}
	const prefix = "/v1.56/containers/"
	return strings.HasPrefix(p, prefix) && strings.HasSuffix(p, "/json") && hexID.MatchString(strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/json"))
}
func newPoolEngineReader() poolGet {
	return poolEngineReader(func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", poolSocket)
	})
}

// The sole injected transport seam is private. Production supplies only the fixed
// Unix dialer; no proxy, environment endpoint, redirects or TCP fallback exists.
func poolEngineReader(dial func(context.Context, string, string) (net.Conn, error)) poolGet {
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: dial, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: MaxResponse}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrPoolInvalid }}
	return func(ctx context.Context, p string) ([]byte, error) {
		if ctx == nil || !poolEndpoint(p) {
			return nil, poolError(ctx, ErrPoolInvalid)
		}
		if e := ctx.Err(); e != nil {
			return nil, poolError(ctx, e)
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine"+p, nil)
		if e != nil {
			return nil, poolError(ctx, ErrPoolInvalid)
		}
		res, e := client.Do(req)
		if e != nil {
			return nil, poolError(ctx, e)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNotFound && strings.HasSuffix(p, "/json") && p != "/v1.56/containers/json?all=1" {
			return nil, poolError(ctx, ErrPoolUnstable)
		}
		if res.StatusCode != http.StatusOK {
			return nil, poolError(ctx, ErrPoolUnavailable)
		}
		raw, e := io.ReadAll(io.LimitReader(res.Body, MaxResponse+1))
		if ce := ctx.Err(); ce != nil {
			return nil, poolError(ctx, ce)
		}
		if e != nil || len(raw) > MaxResponse {
			return nil, poolError(ctx, ErrPoolUnavailable)
		}
		return raw, nil
	}
}
func poolCensus(ctx context.Context, get poolGet) (o PoolEngineReadback, e error) {
	var raw []byte
	read := func(p string) error {
		return poolCall(ctx, func() error { var er error; raw, er = get(ctx, p); return er })
	}
	if e = read("/v1.56/version"); e != nil {
		return
	}
	api, ver, e := poolDecodeVersion(raw)
	if e != nil {
		return o, e
	}
	if e = read("/v1.56/info"); e != nil {
		return
	}
	o, e = poolDecodeInfo(raw)
	if e != nil {
		return
	}
	if o.Version != ver {
		return o, ErrPoolInvalid
	}
	o.APIVersion = api
	if e = read("/v1.56/containers/json?all=1"); e != nil {
		return
	}
	ids, e := poolDecodeList(raw)
	if e != nil {
		return o, e
	}
	o.Containers = []PoolContainerInspect{}
	for _, id := range ids {
		if e = read("/v1.56/containers/" + id + "/json"); e != nil {
			return
		}
		var i PoolContainerInspect
		i, e = poolDecodeInspect(raw)
		if e != nil {
			return
		}
		if i.ID != id {
			return o, ErrPoolInvalid
		}
		o.Containers = append(o.Containers, i)
	}
	// An all-list is never filtered to manufacture a running-only census.
	for _, i := range o.Containers {
		if i.Status != "running" || !i.Running || i.Paused || i.Restarting || i.Dead {
			return o, ErrPoolUnstable
		}
		if i.PID == 0 || i.StartedAt.IsZero() {
			return o, ErrPoolInvalid
		}
	}
	return
}
func poolSameCensus(a, b PoolEngineReadback) bool { return reflect.DeepEqual(a, b) }
