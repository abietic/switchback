package queryevidence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	qe "github.com/abietic/switchback/test/acceptance/observability/queryevidence"
)

type fileSnapshot struct {
	Mode     os.FileMode
	Size     int64
	Modified time.Time
	Bytes    string
}

func snapshot(t *testing.T, directory string) map[string]fileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]fileSnapshot{}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if info.Mode().IsRegular() {
			raw, err = os.ReadFile(path)
		} else if info.Mode()&os.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(path)
			raw = []byte(target)
		}
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = fileSnapshot{info.Mode(), info.Size(), info.ModTime(), string(raw)}
	}
	return result
}
func assertSnapshot(t *testing.T, directory string, before map[string]fileSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(before, snapshot(t, directory)) {
		t.Fatal("evidence changed")
	}
}

func assertReplayOpens(t *testing.T, directory string, binding qe.Binding) {
	t.Helper()
	before := snapshot(t, directory)
	if _, err := qe.OpenReplay(directory, binding); err != nil {
		t.Fatal("valid evidence fixture rejected before mutation:", err)
	}
	assertSnapshot(t, directory, before)
}

func TestReplayFinalAttemptFIFOAndReadOnly(t *testing.T) {
	aCount := 0
	binding, close := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pending") == "1" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("value") == "a" {
			aCount++
			fmt.Fprintf(w, "a%d", aCount)
			return
		}
		io.WriteString(w, r.URL.Query().Get("value"))
	})
	directory := filepath.Join(canonicalTempDir(t), "q")
	r, err := qe.NewRecorder(directory, binding, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, binding.Origins["prometheus"]+"/api/v1/query?pending=1", nil)
	res, err := r.RoundTrip(req)
	if err != nil || res.StatusCode != 404 {
		t.Fatal("pending response lost", err)
	}
	res.Body.Close()
	if err := r.Seal(); err == nil {
		t.Fatal("pending sealed")
	}
	if err := r.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	client := qe.HTTPClient(qe.WithTransport(context.Background(), r), nil)
	a := binding.Origins["prometheus"] + "/api/v1/query?value=a"
	b := binding.Origins["langfuse"] + "/api/public/traces/id?value=b"
	if get(t, client, a) != "a1" || get(t, client, b) != "b" || get(t, client, a) != "a2" {
		t.Fatal("record response changed")
	}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	close()
	before := snapshot(t, directory)
	replay, err := qe.OpenReplay(directory, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.AssertConsumed(); err == nil {
		t.Fatal("unconsumed evidence accepted")
	}
	offline := qe.HTTPClient(qe.WithTransport(context.Background(), replay), nil)
	if get(t, offline, b) != "b" || get(t, offline, a) != "a1" || get(t, offline, a) != "a2" {
		t.Fatal("response order or bytes changed")
	}
	if err := replay.AssertConsumed(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{a, req.URL.String(), binding.Origins["prometheus"] + "/api/v1/query?unknown=1"} {
		if res, err := offline.Get(endpoint); err == nil {
			res.Body.Close()
			t.Fatal("unknown or duplicate replay accepted")
		}
	}
	assertSnapshot(t, directory, before)
}

func TestReplayRejectsUnsafeAndCanceledRequests(t *testing.T) {
	cases := map[string]func(*http.Request){"POST": func(r *http.Request) { r.Method = "POST" }, "body": func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")); r.ContentLength = 1 }, "Host": func(r *http.Request) { r.Host = "other.example" }, "canceled": func(r *http.Request) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		*r = *r.WithContext(ctx)
	}}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			directory, binding := literalEvidence(t)
			before := snapshot(t, directory)
			replay, err := qe.OpenReplay(directory, binding)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest("GET", "http://127.0.0.1:3/api/v1/query?query=up", nil)
			change(request)
			if _, err := replay.RoundTrip(request); err == nil {
				t.Fatal("unsafe or canceled query accepted")
			}
			if err := replay.AssertConsumed(); err == nil {
				t.Fatal("rejected request consumed response")
			}
			assertSnapshot(t, directory, before)
		})
	}
}

func TestReplayRechecksBodiesAndDirectory(t *testing.T) {
	t.Run("body replaced after open", func(t *testing.T) {
		directory, binding := literalEvidence(t)
		replay, err := qe.OpenReplay(directory, binding)
		if err != nil {
			t.Fatal(err)
		}
		putBytes(t, filepath.Join(directory, "000002.body"), []byte("abd"))
		before := snapshot(t, directory)
		request, _ := http.NewRequest("GET", "http://127.0.0.1:3/api/v1/query?query=up", nil)
		if _, err := replay.RoundTrip(request); err == nil {
			t.Fatal("replaced bytes replayed")
		}
		assertSnapshot(t, directory, before)
	})
	t.Run("symlink directory", func(t *testing.T) {
		directory, binding := literalEvidence(t)
		link := filepath.Join(canonicalTempDir(t), "q")
		if err := os.Symlink(directory, link); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, directory)
		if _, err := qe.OpenReplay(link, binding); err == nil {
			t.Fatal("symlink directory accepted")
		}
		assertSnapshot(t, directory, before)
	})
}

func hashBytes(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func putBytes(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("fixture write failed:", err)
	}
}
func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture read failed:", err)
	}
	return raw
}
func removeFixtureFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal("fixture removal failed:", err)
	}
}
func putJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

// This literal, public evidence fixture is independent of Recorder internals.
func literalEvidence(t *testing.T) (string, qe.Binding) {
	t.Helper()
	directory := canonicalTempDir(t)
	binding := qe.Binding{Project: "replay-test", SnapshotSHA256: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Origins: map[string]string{"grafana": "http://127.0.0.1:1", "langfuse": "http://127.0.0.1:2", "prometheus": "http://127.0.0.1:3"}}
	responses := []any{}
	for i, status := range []int{404, 200} {
		name := fmt.Sprintf("%06d", i+1)
		if err := os.WriteFile(filepath.Join(directory, name+".body"), []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		putJSON(t, filepath.Join(directory, name+".json"), map[string]any{"schema": "observability.query_response.v1", "sequence": i + 1, "attempt": i + 1, "method": "GET", "url": "http://127.0.0.1:3/api/v1/query?query=up", "status": status, "started_at": "2026-10-01T00:00:00Z", "finished_at": "2026-10-01T00:00:01Z", "body_bytes": 3, "body_sha256": "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"})
		raw := readBytes(t, filepath.Join(directory, name+".json"))
		responses = append(responses, map[string]any{"sequence": i + 1, "name": name + ".json", "sha256": hashBytes(raw)})
	}
	putJSON(t, filepath.Join(directory, "manifest.json"), map[string]any{"schema": "observability.query_manifest.v1", "binding": binding, "successful_attempt": 2, "responses": responses})
	// Every mutation caller receives material already accepted through the public API.
	assertReplayOpens(t, directory, binding)
	return directory, binding
}
func alterMetadata(t *testing.T, directory string, sequence int, change func(map[string]any)) {
	t.Helper()
	name := fmt.Sprintf("%06d.json", sequence)
	meta := readJSON(t, filepath.Join(directory, name))
	change(meta)
	putJSON(t, filepath.Join(directory, name), meta)
	raw := readBytes(t, filepath.Join(directory, name))
	m := readJSON(t, filepath.Join(directory, "manifest.json"))
	m["responses"].([]any)[sequence-1].(map[string]any)["sha256"] = hashBytes(raw)
	putJSON(t, filepath.Join(directory, "manifest.json"), m)
}

func TestReplayRejectsBadEvidenceWithoutWrites(t *testing.T) {
	cases := map[string]func(*testing.T, string, *qe.Binding){
		"project binding": func(t *testing.T, d string, b *qe.Binding) { b.Project = "other" }, "snapshot binding": func(t *testing.T, d string, b *qe.Binding) { b.SnapshotSHA256 = "sha256:" + strings.Repeat("0", 64) }, "origin binding": func(t *testing.T, d string, b *qe.Binding) { b.Origins["grafana"] = "http://127.0.0.1:4" },
		"body replacement": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "000002.body"), []byte("abd"))
		}, "historical body replacement": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "000001.body"), []byte("abd"))
		},
		"metadata replacement": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "000002.json"), []byte(`{}`))
		},
		"extra": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "extra"), []byte("extra"))
		},
		"manifest trailing": func(t *testing.T, d string, b *qe.Binding) {
			raw := readBytes(t, filepath.Join(d, "manifest.json"))
			putBytes(t, filepath.Join(d, "manifest.json"), append(raw, []byte(`{}`)...))
		},
		"manifest oversized": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "manifest.json"), []byte(strings.Repeat(" ", (16<<20)+1)))
		},
		"body oversized": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "000002.body"), []byte(strings.Repeat("x", (4<<20)+1)))
		},
		"metadata oversized": func(t *testing.T, d string, b *qe.Binding) {
			putBytes(t, filepath.Join(d, "000002.json"), []byte(strings.Repeat(" ", (32<<10)+1)))
		},
		"metadata trailing": func(t *testing.T, d string, b *qe.Binding) {
			path := filepath.Join(d, "000002.json")
			raw := readBytes(t, path)
			raw = append(raw, []byte(`{}`)...)
			putBytes(t, path, raw)
			m := readJSON(t, filepath.Join(d, "manifest.json"))
			m["responses"].([]any)[1].(map[string]any)["sha256"] = hashBytes(raw)
			putJSON(t, filepath.Join(d, "manifest.json"), m)
		},
	}
	for _, file := range []string{"manifest.json", "000001.json", "000002.json", "000001.body", "000002.body"} {
		cases["missing "+file] = func(t *testing.T, d string, b *qe.Binding) {
			if err := os.Remove(filepath.Join(d, file)); err != nil {
				t.Fatal(err)
			}
		}
		cases["symlink "+file] = func(t *testing.T, d string, b *qe.Binding) {
			path := filepath.Join(d, file)
			target := filepath.Join(canonicalTempDir(t), "target")
			raw := readBytes(t, path)
			putBytes(t, target, raw)
			removeFixtureFile(t, path)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}
		cases["fifo "+file] = func(t *testing.T, d string, b *qe.Binding) {
			path := filepath.Join(d, file)
			removeFixtureFile(t, path)
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifestChanges := map[string]func(map[string]any){
		"unknown field": func(m map[string]any) { m["secret"] = "x" }, "schema": func(m map[string]any) { m["schema"] = "other" }, "success not last": func(m map[string]any) { m["successful_attempt"] = 1 }, "success empty": func(m map[string]any) { m["successful_attempt"] = 3 }, "empty responses": func(m map[string]any) { m["responses"] = []any{} },
		"sequence reversed": func(m map[string]any) { s := m["responses"].([]any); s[0], s[1] = s[1], s[0] }, "duplicate sequence": func(m map[string]any) { s := m["responses"].([]any); s[1] = s[0] }, "escaping basename": func(m map[string]any) { m["responses"].([]any)[0].(map[string]any)["name"] = "../000001.json" }, "wrong basename": func(m map[string]any) { m["responses"].([]any)[0].(map[string]any)["name"] = "000002.json" }, "wrong metadata hash": func(m map[string]any) {
			m["responses"].([]any)[0].(map[string]any)["sha256"] = "sha256:" + strings.Repeat("0", 64)
		},
	}
	for name, change := range manifestChanges {
		cases["manifest "+name] = func(t *testing.T, d string, b *qe.Binding) {
			path := filepath.Join(d, "manifest.json")
			m := readJSON(t, path)
			change(m)
			putJSON(t, path, m)
		}
	}
	metaChanges := map[string]func(map[string]any){
		"unknown": func(m map[string]any) { m["secret"] = "x" }, "schema": func(m map[string]any) { m["schema"] = "other" }, "sequence": func(m map[string]any) { m["sequence"] = 1 }, "zero attempt": func(m map[string]any) { m["attempt"] = 0 }, "skipped attempt": func(m map[string]any) { m["attempt"] = 3 }, "regressed attempt": func(m map[string]any) { m["attempt"] = 1 },
		"empty method": func(m map[string]any) { m["method"] = "" },
		"body hash":    func(m map[string]any) { m["body_sha256"] = "sha256:" + strings.Repeat("0", 64) }, "body size": func(m map[string]any) { m["body_bytes"] = 2 }, "negative size": func(m map[string]any) { m["body_bytes"] = -1 }, "status": func(m map[string]any) { m["status"] = 999 }, "final 404": func(m map[string]any) { m["status"] = 404 }, "time reversed": func(m map[string]any) { m["finished_at"] = "2026-09-01T00:00:00Z" }, "zero time": func(m map[string]any) { m["started_at"] = "0001-01-01T00:00:00Z" }, "invalid time": func(m map[string]any) { m["started_at"] = "bad" }, "failure": func(m map[string]any) { m["failure"] = "transport_error" }, "method": func(m map[string]any) { m["method"] = "POST" }, "URL path": func(m map[string]any) { m["url"] = "http://127.0.0.1:3/private" }, "URL userinfo": func(m map[string]any) { m["url"] = "http://user:secret@127.0.0.1:3/api/v1/query" }, "URL fragment": func(m map[string]any) { m["url"] = "http://127.0.0.1:3/api/v1/query#x" }, "URL origin": func(m map[string]any) { m["url"] = "http://127.0.0.1:9/api/v1/query" },
	}
	for name, change := range metaChanges {
		cases["metadata "+name] = func(t *testing.T, d string, b *qe.Binding) { alterMetadata(t, d, 2, change) }
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			directory, binding := literalEvidence(t)
			change(t, directory, &binding)
			before := snapshot(t, directory)
			if _, err := qe.OpenReplay(directory, binding); err == nil {
				t.Fatal("bad evidence accepted")
			}
			assertSnapshot(t, directory, before)
		})
	}
}

func literalResponse(t *testing.T, directory string, sequence int, body []byte) any {
	t.Helper()
	name := fmt.Sprintf("%06d", sequence)
	putBytes(t, filepath.Join(directory, name+".body"), body)
	putJSON(t, filepath.Join(directory, name+".json"), map[string]any{"schema": "observability.query_response.v1", "sequence": sequence, "attempt": 1, "method": "GET", "url": "http://127.0.0.1:3/api/v1/query?query=up", "status": 200, "started_at": "2026-10-01T00:00:00Z", "finished_at": "2026-10-01T00:00:01Z", "body_bytes": len(body), "body_sha256": hashBytes(body)})
	return map[string]any{"sequence": sequence, "name": name + ".json", "sha256": hashBytes(readBytes(t, filepath.Join(directory, name+".json")))}
}

func putWholeManifest(t *testing.T, directory string, binding qe.Binding, responses []any) {
	t.Helper()
	putJSON(t, filepath.Join(directory, "manifest.json"), map[string]any{"schema": "observability.query_manifest.v1", "binding": binding, "successful_attempt": 1, "responses": responses})
}

// Build and publicly validate the exact below-limit material before adding overflow.
func wholeEvidence(t *testing.T, count int, body []byte) (string, qe.Binding, []any) {
	t.Helper()
	directory, binding := literalEvidence(t)
	responses := make([]any, 0, count+1)
	for i := 1; i <= count; i++ {
		responses = append(responses, literalResponse(t, directory, i, body))
	}
	putWholeManifest(t, directory, binding, responses)
	assertReplayOpens(t, directory, binding)
	return directory, binding, responses
}

func TestReplayEnforcesWholeEvidenceBounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		count int
		body  []byte
	}{{"record count", 4096, []byte("abc")}, {"total bytes", 16, []byte(strings.Repeat("x", 4<<20))}} {
		t.Run(test.name, func(t *testing.T) {
			directory, binding, responses := wholeEvidence(t, test.count, test.body)
			responses = append(responses, literalResponse(t, directory, test.count+1, test.body))
			putWholeManifest(t, directory, binding, responses)
			before := snapshot(t, directory)
			if _, err := qe.OpenReplay(directory, binding); err == nil {
				t.Fatal("evidence limit exceeded without rejection")
			}
			assertSnapshot(t, directory, before)
		})
	}
}
