package queryevidence_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qe "github.com/abietic/switchback/test/acceptance/observability/queryevidence"
)

func fixture(t *testing.T, handler http.HandlerFunc) (qe.Binding, func()) {
	t.Helper()
	services := []*httptest.Server{httptest.NewServer(handler), httptest.NewServer(handler), httptest.NewServer(handler)}
	binding := qe.Binding{Project: "query-test", SnapshotSHA256: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Origins: map[string]string{"grafana": services[0].URL, "langfuse": services[1].URL, "prometheus": services[2].URL}}
	close := func() {
		for _, s := range services {
			s.Close()
		}
	}
	t.Cleanup(close)
	return binding, close
}

type boundaryTransport func(*http.Request) (*http.Response, error)

func (f boundaryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve temporary fixture directory:", err)
	}
	return directory
}

func TestRecordAndReplayWithAliasedTempRoot(t *testing.T) {
	root := canonicalTempDir(t)
	alias := filepath.Join(root, "temp-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal("create temporary root alias:", err)
	}
	t.Run("record and replay", func(t *testing.T) {
		t.Setenv("GOTMPDIR", "")
		t.Setenv("TMPDIR", alias)
		binding, closeServers := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"status":"success"}`)
		})
		directory := filepath.Join(canonicalTempDir(t), "queries")
		recorder, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
		if err != nil {
			t.Fatal("record through temporary root alias:", err)
		}
		if err := recorder.BeginAttempt(); err != nil {
			t.Fatal(err)
		}
		endpoint := binding.Origins["prometheus"] + "/api/v1/query?query=up"
		client := qe.HTTPClient(qe.WithTransport(context.Background(), recorder), &http.Client{Timeout: time.Second})
		if got := get(t, client, endpoint); got != `{"status":"success"}` {
			t.Fatalf("recorded body %q", got)
		}
		if err := recorder.Seal(); err != nil {
			t.Fatal(err)
		}
		closeServers()
		before := snapshot(t, directory)
		replay, err := qe.OpenReplay(directory, binding)
		if err != nil {
			t.Fatal("replay through temporary root alias:", err)
		}
		if got := get(t, qe.HTTPClient(qe.WithTransport(context.Background(), replay), client), endpoint); got != `{"status":"success"}` {
			t.Fatalf("replayed body %q", got)
		}
		if err := replay.AssertConsumed(); err != nil {
			t.Fatal(err)
		}
		assertSnapshot(t, directory, before)
	})
}

type brokenBody struct {
	read   bool
	closed bool
}

func (b *brokenBody) Read(p []byte) (int, error) {
	if !b.read {
		b.read = true
		return copy(p, "partial"), errors.New("sensitive-read-error")
	}
	return 0, io.EOF
}
func (b *brokenBody) Close() error { b.closed = true; return nil }

func TestRecorderFailureMaterialsAreSticky(t *testing.T) {
	binding := qe.Binding{Project: "failure-test", SnapshotSHA256: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Origins: map[string]string{"grafana": "http://127.0.0.1:1", "langfuse": "http://127.0.0.1:2", "prometheus": "http://127.0.0.1:3"}}
	for _, kind := range []string{"transport", "read", "oversize", "total", "count", "disk"} {
		t.Run(kind, func(t *testing.T) {
			broken := &brokenBody{}
			transport := boundaryTransport(func(*http.Request) (*http.Response, error) {
				if kind == "transport" {
					return nil, errors.New("sensitive-transport-error")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader("ok"))
				if kind == "read" {
					body = broken
				}
				if kind == "oversize" {
					body = io.NopCloser(io.LimitReader(infiniteByte{}, (4<<20)+1))
				}
				if kind == "total" {
					body = io.NopCloser(io.LimitReader(infiniteByte{}, 4<<20))
				}
				return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
			})
			directory := filepath.Join(canonicalTempDir(t), "q")
			r, err := qe.NewRecorder(directory, binding, transport)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.BeginAttempt(); err != nil {
				t.Fatal(err)
			}
			if kind == "disk" {
				if err := os.WriteFile(filepath.Join(directory, "000001.json"), []byte("sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			req, _ := http.NewRequest(http.MethodGet, binding.Origins["prometheus"]+"/api/v1/query", nil)
			previous := 0
			if kind == "total" {
				previous = 16
			}
			if kind == "count" {
				previous = 4096
			}
			if previous > 0 {
				body := []byte("ok")
				if kind == "total" {
					body = []byte(strings.Repeat("x", 4<<20))
				}
				// Independently open equivalent valid material at the exact limit;
				// the live recorder below then adds one response beyond it.
				wholeEvidence(t, previous, body)
			}
			for i := 0; i < previous; i++ {
				res, err := r.RoundTrip(req)
				if err != nil {
					t.Fatalf("record %d: %v", i, err)
				}
				res.Body.Close()
			}
			if _, err := r.RoundTrip(req); err == nil {
				t.Fatal("failure accepted")
			}
			if kind == "read" && !broken.closed {
				t.Fatal("original body not closed")
			}
			if err := r.Seal(); err == nil {
				t.Fatal("failure sealed")
			}
			if err := r.BeginAttempt(); err == nil {
				t.Fatal("sticky failure cleared")
			}
			if _, err := os.Stat(filepath.Join(directory, "manifest.json")); !os.IsNotExist(err) {
				t.Fatal("failure manifest exists")
			}
			if kind == "read" || kind == "transport" || kind == "oversize" {
				raw, err := os.ReadFile(filepath.Join(directory, "000001.json"))
				if err != nil {
					t.Fatal("failure metadata missing", err)
				}
				var record map[string]any
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				code := map[string]string{"transport": "transport_error", "read": "body_read_error", "oversize": "body_oversize"}[kind]
				if record["failure"] != code {
					t.Fatalf("failure %v", record["failure"])
				}
				if bytes.Contains(raw, []byte("sensitive")) {
					t.Fatal("raw error persisted")
				}
				body, err := os.ReadFile(filepath.Join(directory, "000001.body"))
				if err != nil {
					t.Fatal(err)
				}
				if kind == "read" && string(body) != "partial" {
					t.Fatalf("partial body %q", body)
				}
				if len(body) > 4<<20 {
					t.Fatal("oversize persisted")
				}
			}
			if kind == "disk" {
				raw := readBytes(t, filepath.Join(directory, "000001.json"))
				if string(raw) != "sentinel" {
					t.Fatal("existing metadata overwritten")
				}
				if _, err := os.Stat(filepath.Join(directory, "000001.body")); err != nil {
					t.Fatal("partial disk material removed")
				}
			}
		})
	}
}

type infiniteByte struct{}

func (infiniteByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestRecorderRejectsRedirectFollowing(t *testing.T) {
	binding, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/v1/query?redirect=1", http.StatusFound)
	})
	r, err := qe.NewRecorder(filepath.Join(canonicalTempDir(t), "q"), binding, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	client := qe.HTTPClient(qe.WithTransport(context.Background(), r), &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	res, err := client.Get(binding.Origins["prometheus"] + "/api/v1/query")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 {
		t.Fatal("redirect status changed")
	}
	if err := r.Seal(); err == nil {
		t.Fatal("redirect sealed")
	}
	r, err = qe.NewRecorder(filepath.Join(canonicalTempDir(t), "q"), binding, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	client = qe.HTTPClient(qe.WithTransport(context.Background(), r), &http.Client{Timeout: time.Second})
	if res, err := client.Get(binding.Origins["prometheus"] + "/api/v1/query"); err == nil {
		res.Body.Close()
		t.Fatal("automatic redirect followed")
	}
	if err := r.Seal(); err == nil {
		t.Fatal("redirect failure sealed")
	}
}

func TestRecorderSealPersistenceFailureStaysFailed(t *testing.T) {
	binding := qe.Binding{Project: "seal-test", SnapshotSHA256: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Origins: map[string]string{"grafana": "http://127.0.0.1:1", "langfuse": "http://127.0.0.1:2", "prometheus": "http://127.0.0.1:3"}}
	transport := boundaryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	directory := filepath.Join(canonicalTempDir(t), "q")
	r, err := qe.NewRecorder(directory, binding, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", binding.Origins["prometheus"]+"/api/v1/query", nil)
	res, err := r.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, directory)
	if err := r.Seal(); err == nil {
		t.Fatal("existing manifest overwritten")
	}
	if err := r.BeginAttempt(); err == nil {
		t.Fatal("manifest failure not sticky")
	}
	assertSnapshot(t, directory, before)
}

func cloneBinding(b qe.Binding) qe.Binding {
	result := b
	result.Origins = map[string]string{}
	for k, v := range b.Origins {
		result.Origins[k] = v
	}
	return result
}

func recordValidBaseline(t *testing.T, directory string, binding qe.Binding) {
	t.Helper()
	recorder, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
	if err != nil {
		t.Fatal("valid recorder path/binding rejected before mutation:", err)
	}
	if err := recorder.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	client := qe.HTTPClient(qe.WithTransport(context.Background(), recorder), &http.Client{Timeout: time.Second})
	if got := get(t, client, binding.Origins["prometheus"]+"/api/v1/query"); got != "ok" {
		t.Fatalf("baseline body %q", got)
	}
	if err := recorder.Seal(); err != nil {
		t.Fatal(err)
	}
	assertReplayOpens(t, directory, binding)
}

func TestRecorderRejectsUnsafeBindingsAndDirectories(t *testing.T) {
	binding, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	cases := map[string]func(*qe.Binding){
		"empty project": func(b *qe.Binding) { b.Project = "" }, "project traversal": func(b *qe.Binding) { b.Project = "../x" }, "uppercase project": func(b *qe.Binding) { b.Project = "X" }, "long project": func(b *qe.Binding) { b.Project = strings.Repeat("x", 129) },
		"bad digest":     func(b *qe.Binding) { b.SnapshotSHA256 = "sha256:ABC" },
		"missing origin": func(b *qe.Binding) { delete(b.Origins, "grafana") }, "extra origin": func(b *qe.Binding) { b.Origins["extra"] = "http://127.0.0.1:9" }, "duplicate origin": func(b *qe.Binding) { b.Origins["grafana"] = b.Origins["prometheus"] },
	}
	for index, origin := range []string{"https://127.0.0.1:9", "http://localhost:9", "http://192.0.2.1:9", "http://127.0.0.1:09", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1", "http://u:p@127.0.0.1:9", "http://127.0.0.1:9/", "http://127.0.0.1:9?q=x", "http://127.0.0.1:9#x"} {
		cases[fmt.Sprintf("unsafe origin %02d", index)] = func(b *qe.Binding) { b.Origins["grafana"] = origin }
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			parent := canonicalTempDir(t)
			recordValidBaseline(t, filepath.Join(parent, "baseline"), binding)
			b := cloneBinding(binding)
			change(&b)
			directory := filepath.Join(parent, "q")
			before := snapshot(t, parent)
			if _, err := qe.NewRecorder(directory, b, http.DefaultTransport); err == nil {
				t.Fatal("unsafe binding accepted")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("invalid binding created evidence")
			}
			assertSnapshot(t, parent, before)
		})
	}
	t.Run("relative", func(t *testing.T) {
		if _, err := qe.NewRecorder("relative-query-evidence", binding, http.DefaultTransport); err == nil {
			t.Fatal("relative path accepted")
		}
	})
	t.Run("symlink parent", func(t *testing.T) {
		parent := canonicalTempDir(t)
		recordValidBaseline(t, filepath.Join(parent, "baseline"), binding)
		link := filepath.Join(parent, "link")
		if err := os.Symlink(parent, link); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, parent)
		if _, err := qe.NewRecorder(filepath.Join(link, "q"), binding, http.DefaultTransport); err == nil {
			t.Fatal("symlink parent accepted")
		}
		assertSnapshot(t, parent, before)
	})
}

func TestRecorderControlledRoutesAndPrivateHeaders(t *testing.T) {
	binding, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "fixture-secret" {
			t.Error("BasicAuth not delivered")
		}
		w.Header().Set("Set-Cookie", "private-cookie")
		w.Header().Set("X-Secret", "response-secret")
		io.WriteString(w, "ok")
	})
	directory := filepath.Join(canonicalTempDir(t), "q")
	recorder, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	paths := map[string][]string{"grafana": {"/api/datasources/proxy/uid/switchback-loki/loki/api/v1/series", "/api/datasources/proxy/uid/switchback-loki/loki/api/v1/query_range"}, "langfuse": {"/api/public/traces/trace-1", "/api/public/observations/observation-1"}, "prometheus": {"/api/v1/query?query=up"}}
	for backend, routes := range paths {
		for _, path := range routes {
			req, _ := http.NewRequest(http.MethodGet, binding.Origins[backend]+path, nil)
			req.SetBasicAuth("reader", "fixture-secret")
			req.Header.Set("Cookie", "request-cookie")
			res, err := recorder.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
		}
	}
	if err := recorder.Seal(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(directory, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"reader", "fixture-secret", "Authorization", "Cookie", "request-cookie", "private-cookie", "response-secret"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("private value persisted in %s", e.Name())
			}
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode %s", info.Mode())
		}
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("directory mode %s", info.Mode())
	}
}

func TestRecorderRejectsUnsafeRequests(t *testing.T) {
	binding, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unsafe request reached upstream")
		io.WriteString(w, "ok")
	})
	safe := binding.Origins["prometheus"] + "/api/v1/query"
	cases := map[string]func(*http.Request){"POST": func(r *http.Request) { r.Method = http.MethodPost }, "body": func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("secret")); r.ContentLength = 6 }, "Host": func(r *http.Request) { r.Host = "other.example" }, "Host header": func(r *http.Request) { r.Header.Set("Host", "other.example") }}
	for index, endpoint := range []string{binding.Origins["prometheus"] + "/api/v1/query#fragment", binding.Origins["prometheus"] + "/api/v1/query/", binding.Origins["prometheus"] + "/api/v1/query%2f", binding.Origins["prometheus"] + "/api/public/traces/x", binding.Origins["langfuse"] + "/api/public/ingestion", binding.Origins["langfuse"] + "/api/public/traces/../x", binding.Origins["langfuse"] + "/api/public/traces/x/y", "http://user:password@" + strings.TrimPrefix(binding.Origins["prometheus"], "http://") + "/api/v1/query", "http://127.0.0.1:1/api/v1/query", safe + "?x=" + strings.Repeat("x", 4096)} {
		cases[fmt.Sprintf("unsafe URL %02d", index)] = func(r *http.Request) {
			replacement, _ := http.NewRequest(http.MethodGet, endpoint, nil)
			*r = *replacement
		}
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := qe.NewRecorder(filepath.Join(canonicalTempDir(t), "q"), binding, http.DefaultTransport)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.BeginAttempt(); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodGet, safe, nil)
			change(req)
			if _, err := r.RoundTrip(req); err == nil {
				t.Fatal("unsafe request accepted")
			}
			if err := r.Seal(); err == nil {
				t.Fatal("invalid request sealed")
			}
		})
	}
}

func get(t *testing.T, client *http.Client, endpoint string) string {
	t.Helper()
	res, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	return string(raw)
}

func TestRecordAndReplayOffline(t *testing.T) {
	binding, close := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"success"}`) })
	directory := filepath.Join(canonicalTempDir(t), "queries")
	recorder, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	endpoint := binding.Origins["prometheus"] + "/api/v1/query?query=up"
	client := qe.HTTPClient(qe.WithTransport(context.Background(), recorder), &http.Client{Timeout: time.Second})
	if got := get(t, client, endpoint); got != `{"status":"success"}` {
		t.Fatalf("body %q", got)
	}
	if err := recorder.Seal(); err != nil {
		t.Fatal(err)
	}
	close()
	replay, err := qe.OpenReplay(directory, binding)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, qe.HTTPClient(qe.WithTransport(context.Background(), replay), client), endpoint); got != `{"status":"success"}` {
		t.Fatalf("replayed body %q", got)
	}
	if err := replay.AssertConsumed(); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderAttemptLifecycle(t *testing.T) {
	binding, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	t.Run("before begin", func(t *testing.T) {
		r, err := qe.NewRecorder(filepath.Join(canonicalTempDir(t), "q"), binding, http.DefaultTransport)
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest(http.MethodGet, binding.Origins["prometheus"]+"/api/v1/query", nil)
		if _, err := r.RoundTrip(request); err == nil {
			t.Fatal("request before attempt accepted")
		}
		if err := r.Seal(); err == nil {
			t.Fatal("empty evidence sealed")
		}
	})
	t.Run("empty attempt", func(t *testing.T) {
		r, err := qe.NewRecorder(filepath.Join(canonicalTempDir(t), "q"), binding, http.DefaultTransport)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.BeginAttempt(); err != nil {
			t.Fatal(err)
		}
		if err := r.BeginAttempt(); err == nil {
			t.Fatal("empty attempt skipped")
		}
		if err := r.Seal(); err == nil {
			t.Fatal("empty attempt sealed")
		}
	})
	t.Run("sealed immutable", func(t *testing.T) {
		directory := filepath.Join(canonicalTempDir(t), "q")
		r, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.BeginAttempt(); err != nil {
			t.Fatal(err)
		}
		get(t, qe.HTTPClient(qe.WithTransport(context.Background(), r), nil), binding.Origins["prometheus"]+"/api/v1/query")
		if err := r.Seal(); err != nil {
			t.Fatal(err)
		}
		if err := r.Seal(); err == nil {
			t.Fatal("second seal accepted")
		}
		if err := r.BeginAttempt(); err == nil {
			t.Fatal("attempt after seal accepted")
		}
		req, _ := http.NewRequest(http.MethodGet, binding.Origins["prometheus"]+"/api/v1/query", nil)
		if _, err := r.RoundTrip(req); err == nil {
			t.Fatal("query after seal accepted")
		}
		if _, err := qe.NewRecorder(directory, binding, http.DefaultTransport); err == nil {
			t.Fatal("existing evidence overwritten")
		}
	})
}
