package createdlayeripc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureBinding() Binding {
	return Binding{"sha256:" + strings.Repeat("a", 64), strings.Repeat("b", 64), "runtime-1", "session-1", "run-1", 1, "local_standard_v1", 1, "sha256:" + strings.Repeat("c", 64)}
}
func fixtureKey() []byte { return bytes.Repeat([]byte{23}, 32) }
func fixturePair(t *testing.T) (*Client, *Server) {
	t.Helper()
	b := fixtureBinding()
	c, e := NewClient(fixtureKey(), b.ManifestDigest)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewServer(fixtureKey(), b.ManifestDigest)
	if e != nil {
		t.Fatal(e)
	}
	c.peer = func(net.Conn, bool) error { return nil }
	s.peer = c.peer
	return c, s
}
func exchange(t *testing.T, c *Client, s *Server, ctx context.Context, b Binding) (capacityprobe.CreatedLayerCollection, error) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(ctx, server) }()
	c.dial = func(context.Context) (net.Conn, error) { return client, nil }
	got, e := c.Observe(ctx, b)
	client.Close()
	<-done
	return got, e
}
func partialCollector(ctx context.Context, _ Binding) capacityprobe.CreatedLayerCollection {
	return capacityprobe.CreatedLayerCollection{Schema: "switchback.capacity.untrusted_created_layer_collection.v1", Status: "preparation_blocked", Trust: "none", Started: time.Now().UTC(), Ended: time.Now().UTC(), Blockers: []string{"unsigned_diagnostic_only"}}
}
func TestPublicConstructorsProtocolPortableCollector(t *testing.T) {
	c, s := fixturePair(t)
	b := fixtureBinding()
	requests := []string{}
	physical := 0
	tick := 0
	base := time.Now().UTC()
	s.collect = func(ctx context.Context, b Binding) capacityprobe.CreatedLayerCollection {
		return capacityprobe.CollectCreatedLayer(ctx, capacityprobe.CreatedLayerTarget{ContainerID: b.ContainerID}, capacityprobe.CreatedLayerSources{Now: func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Microsecond) }, Get: func(ctx context.Context, path string) ([]byte, error) {
			requests = append(requests, path)
			switch path {
			case "/v1.56/version":
				return []byte(`{"ApiVersion":"1.56","Version":"29.9.0"}`), nil
			case "/v1.56/info":
				return []byte(`{"ID":"12345678-1234-5678-9abc-123456789abc","ServerVersion":"29.9.0","DockerRootDir":"/mnt/lima-sbquota/docker","Driver":"overlay2"}`), nil
			default:
				return []byte(`{"Id":"` + b.ContainerID + `","Image":"sha256:` + strings.Repeat("d", 64) + `","State":{"Status":"created","Pid":0,"Running":false,"Paused":false,"Restarting":false,"Dead":false,"StartedAt":"0001-01-01T00:00:00Z"},"GraphDriver":{"Name":"overlay2","Data":{"UpperDir":"/mnt/lima-sbquota/docker/overlay2/fixture/diff"}}}`), nil
			}
		}, Physical: func(ctx context.Context, path string) (*capacityprobe.CreatedLayerPhysical, error) {
			physical++
			if path != "/mnt/lima-sbquota/docker/overlay2/fixture/diff" {
				t.Error("path drift")
			}
			p := &capacityprobe.CreatedLayerPhysical{ProjectID: 41}
			p.TargetQuota[12] = 87
			return p, errors.New("secret-canary")
		}})
	}
	got, e := exchange(t, c, s, context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	if len(requests) != 6 || physical != 1 || got.Physical == nil || got.Physical.TargetQuota[12] != 87 || got.ObservationComplete || got.CapacityReady || got.ExecutionEnabled || got.Trust != "none" || got.Status != "preparation_blocked" {
		t.Fatalf("facts lost or authority added: %+v", got)
	}
	raw, _ := json.Marshal(got)
	if bytes.Contains(raw, []byte("secret-canary")) {
		t.Fatal("error leaked")
	}
}
func TestKeyCopyAndInputEffects(t *testing.T) {
	c, s := fixturePair(t)
	key := fixtureKey()
	c2, _ := NewClient(key, fixtureBinding().ManifestDigest)
	s2, _ := NewServer(key, fixtureBinding().ManifestDigest)
	key[0]++
	if bytes.Equal(c2.key, key) || bytes.Equal(s2.key, key) {
		t.Fatal("key alias")
	}
	for _, key := range [][]byte{nil, make([]byte, 32), make([]byte, 33)} {
		if _, e := NewServer(key, fixtureBinding().ManifestDigest); e == nil {
			t.Fatal("bad key")
		}
	}
	c.dial = func(context.Context) (net.Conn, error) { t.Fatal("invalid input dial"); return nil, nil }
	for _, kind := range []string{"manifest", "cid", "runtime", "profile", "generation", "revision", "session", "run"} {
		b := fixtureBinding()
		switch kind {
		case "manifest":
			b.ManifestDigest = "sha256:" + strings.Repeat("d", 64)
		case "cid":
			b.ContainerID = "../bad"
		case "runtime":
			b.RuntimeID = "bad\n"
		case "profile":
			b.ProfileDigest = "bad"
		case "generation":
			b.Generation = 0
		case "revision":
			b.ProfileRevision = 1 << 63
		case "session":
			b.SessionID = ""
		case "run":
			b.RunID = ""
		}
		if _, e := c.Observe(context.Background(), b); e == nil {
			t.Fatal(kind)
		}
	}
	s.listen = func(observerPlacement) (net.Listener, func(), error) {
		t.Fatal("cancelled listen")
		return nil, nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.Serve(ctx) == nil || s.Serve(nil) == nil {
		t.Fatal("context accepted")
	}
}
func TestServerRejectsAuthenticatedInvalidRequestBeforeCollection(t *testing.T) {
	for _, kind := range []string{"wrong_key", "wrong_challenge", "reflection", "cross_connection_replay", "duplicate", "missing", "case_alias", "noncanonical", "unknown", "trailing", "oversize", "truncation", "stale", "future", "nonce", "manifest", "cid", "runtime", "profile", "entropy", "peer"} {
		t.Run(kind, func(t *testing.T) {
			_, s := fixturePair(t)
			calls := 0
			s.collect = func(context.Context, Binding) capacityprobe.CreatedLayerCollection {
				calls++
				return partialCollector(context.Background(), Binding{})
			}
			s.entropy = bytes.NewReader(bytes.Repeat([]byte{7}, 32))
			if kind == "entropy" {
				s.entropy = bytes.NewReader(nil)
			}
			if kind == "peer" {
				s.peer = func(net.Conn, bool) error { return errors.New("secret-canary") }
			}
			a, b := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.handle(context.Background(), b) }()
			a.SetDeadline(time.Now().Add(time.Second))
			greeting := make([]byte, 40)
			_, e := io.ReadFull(a, greeting)
			if kind != "entropy" && kind != "peer" {
				if e != nil {
					t.Fatal(e)
				}
				req := request{version, strings.Repeat("e", 64), time.Now().UTC().Add(time.Second), fixtureBinding()}
				switch kind {
				case "stale":
					req.Deadline = time.Now().UTC().Add(-time.Second)
				case "future":
					req.Deadline = time.Now().UTC().Add(31 * time.Second)
				case "nonce":
					req.Nonce = "00"
				case "manifest":
					req.Binding.ManifestDigest = "sha256:" + strings.Repeat("d", 64)
				case "cid":
					req.Binding.ContainerID = "bad"
				case "runtime":
					req.Binding.RuntimeID = ""
				case "profile":
					req.Binding.ProfileID = "bad path"
				}
				raw, _ := json.Marshal(req)
				switch kind {
				case "duplicate":
					raw = bytes.Replace(raw, []byte(`"version":`), []byte(`"version":"bad","version":`), 1)
				case "missing":
					raw = bytes.Replace(raw, []byte(`"generation":1,`), nil, 1)
				case "case_alias":
					raw = bytes.Replace(raw, []byte(`"version"`), []byte(`"Version"`), 1)
				case "noncanonical":
					raw = append([]byte(" "), raw...)
				case "unknown":
					raw = append(raw[:len(raw)-1], []byte(`,"extra":0}`)...)
				case "trailing":
					raw = append(raw, []byte(`{}`)...)
				case "oversize":
					raw = bytes.Repeat([]byte{'x'}, requestLimit+1)
				}
				key := s.key
				challenge := greeting[8:]
				domain := requestDomain
				if kind == "wrong_key" {
					key = bytes.Repeat([]byte{9}, 32)
				}
				if kind == "wrong_challenge" || kind == "cross_connection_replay" {
					challenge = bytes.Repeat([]byte{8}, 32)
				}
				if kind == "reflection" {
					domain = responseDomain
				}
				if kind == "oversize" {
					var h [36]byte
					binary.BigEndian.PutUint32(h[:], uint32(len(raw)))
					writeAll(a, h[:])
				} else if kind == "truncation" {
					writeAll(a, []byte{0, 0, 0, 90})
				} else {
					_ = writeFrame(a, key, domain, challenge, nil, raw, requestLimit)
				}
			}
			a.Close()
			<-done
			if calls != 0 {
				t.Fatal("invalid request collected")
			}
		})
	}
}
func TestClientRejectsResponseBindingAuthenticationAndWindow(t *testing.T) {
	for _, kind := range []string{"mac", "nonce", "digest", "binding", "stale", "future", "long", "unknown", "short_array", "missing_array", "direction", "challenge"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := fixturePair(t)
			a, b := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer b.Close()
				challenge := bytes.Repeat([]byte{7}, 32)
				writeAll(b, append([]byte(magic), challenge...))
				raw, e := readFrame(b, c.key, requestDomain, challenge, nil, requestLimit)
				if e != nil {
					return
				}
				var req request
				json.Unmarshal(raw, &req)
				col := partialCollector(context.Background(), req.Binding)
				col.Physical = &capacityprobe.CreatedLayerPhysical{}
				res := response{version, req.Nonce, bodyDigest(raw), req.Binding, col}
				switch kind {
				case "nonce":
					res.Nonce = strings.Repeat("f", 64)
				case "digest":
					res.RequestDigest = "sha256:" + strings.Repeat("f", 64)
				case "binding":
					res.Binding.RuntimeID = "other"
				case "stale":
					res.Collection.Started = time.Now().Add(-time.Hour)
				case "future":
					res.Collection.Ended = time.Now().Add(time.Hour)
				case "long":
					res.Collection.Started = time.Now().Add(-31 * time.Second)
				}
				out, _ := json.Marshal(res)
				switch kind {
				case "unknown":
					out = append(out[:len(out)-1], []byte(`,"unknown":1}`)...)
				case "short_array":
					out = bytes.Replace(out, []byte(`"xfs_uuid":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]`), []byte(`"xfs_uuid":[0]`), 1)
				case "missing_array":
					out = bytes.Replace(out, []byte(`,"xfs_uuid":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]`), nil, 1)
				}
				hash := sha256.Sum256(raw)
				key := c.key
				domain := responseDomain
				if kind == "mac" {
					key = bytes.Repeat([]byte{8}, 32)
				}
				if kind == "direction" {
					domain = requestDomain
				}
				if kind == "challenge" {
					challenge = bytes.Repeat([]byte{8}, 32)
				}
				writeFrame(b, key, domain, challenge, hash[:], out, responseLimit)
			}()
			c.dial = func(context.Context) (net.Conn, error) { return a, nil }
			_, e := c.Observe(context.Background(), fixtureBinding())
			a.Close()
			<-done
			if e == nil {
				t.Fatal("invalid response accepted")
			}
			if strings.Contains(e.Error(), "secret-canary") {
				t.Fatal("error leak")
			}
		})
	}
}
func TestCancellationClosesOnlyConnection(t *testing.T) {
	c, s := fixturePair(t)
	started := make(chan struct{})
	returned := make(chan struct{})
	s.collect = func(ctx context.Context, b Binding) capacityprobe.CreatedLayerCollection {
		close(started)
		<-ctx.Done()
		close(returned)
		return partialCollector(ctx, b)
	}
	a, b := net.Pipe()
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	done := make(chan struct{})
	go func() { defer close(done); s.handle(serverCtx, b) }()
	ctx, cancel := context.WithCancel(context.Background())
	c.dial = func(context.Context) (net.Conn, error) { return a, nil }
	result := make(chan error, 1)
	go func() { _, e := c.Observe(ctx, fixtureBinding()); result <- e }()
	<-started
	cancel()
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("cancel accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("client cancellation hung")
	}
	select {
	case <-returned:
		t.Fatal("client touched sampler lifetime")
	default:
	}
	serverCancel()
	<-done
}
func TestSlowHandshakeBoundedByCallerDeadline(t *testing.T) {
	c, _ := fixturePair(t)
	a, b := net.Pipe()
	defer b.Close()
	c.dial = func(context.Context) (net.Conn, error) { return a, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, e := c.Observe(ctx, fixtureBinding()); e == nil {
		t.Fatal("slow peer accepted")
	}
}

// A listener backed solely by pipes drives the public Serve path without a socket.
type pipeListener struct {
	once   sync.Once
	conn   net.Conn
	closed chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *pipeListener) Addr() net.Addr { return &net.UnixAddr{Net: "unix", Name: "recorded"} }
func TestPublicServeSerialAndCancellation(t *testing.T) {
	c, s := fixturePair(t)
	a, b := net.Pipe()
	ln := &pipeListener{conn: b, closed: make(chan struct{})}
	cleaned := false
	s.listen = func(observerPlacement) (net.Listener, func(), error) { return ln, func() { cleaned = true }, nil }
	s.collect = partialCollector
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	c.dial = func(context.Context) (net.Conn, error) { return a, nil }
	if _, e := c.Observe(context.Background(), fixtureBinding()); e != nil {
		t.Fatal(e)
	}
	cancel()
	if e := <-done; e != nil || !cleaned {
		t.Fatal("serve cleanup", e)
	}
}
func TestFrameBoundsTruncationAndConstantErrors(t *testing.T) {
	key := fixtureKey()
	challenge := bytes.Repeat([]byte{4}, 32)
	for _, limit := range []int{requestLimit, responseLimit} {
		for _, n := range []uint32{0, uint32(limit + 1), ^uint32(0)} {
			var h [36]byte
			binary.BigEndian.PutUint32(h[:], n)
			if _, e := readFrame(bytes.NewReader(h[:]), key, requestDomain, challenge, nil, limit); e != errWire {
				t.Fatal("size accepted")
			}
		}
		var frame bytes.Buffer
		writeFrame(&frame, key, requestDomain, challenge, nil, []byte("{}"), limit)
		raw := frame.Bytes()
		for _, n := range []int{0, 3, 35, 36, len(raw) - 1} {
			if _, e := readFrame(bytes.NewReader(raw[:n]), key, requestDomain, challenge, nil, limit); e != errWire {
				t.Fatal("truncation")
			}
		}
	}
	c, s := fixturePair(t)
	s.collect = partialCollector
	c.entropy = errorReader{}
	if _, e := exchange(t, c, s, context.Background(), fixtureBinding()); e != errNative {
		t.Fatal("entropy", e)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("secret-canary") }
func TestCanonicalArraysAndJSON(t *testing.T) {
	for _, raw := range []string{`{"a":[1]}`, `{"a":[1,2,3]}`, `{"a":[1,2],"a":[1,2]}`, `{"A":[1,2]}`, `{}`, `{"a":[1,2]} {}`, ` {"a":[1,2]}`, `{"a":null}`, `{"a":[1.0,2]}`} {
		var v struct {
			A [2]byte `json:"a"`
		}
		if canonicalDecode([]byte(raw), &v) == nil {
			t.Fatal("canonical accepted", raw)
		}
	}
}
