package capacityprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func poolRewrite(t *testing.T, f *poolFixture, p string, change func(map[string]any)) {
	t.Helper()
	var m map[string]any
	if e := json.Unmarshal(f.raw[p], &m); e != nil {
		t.Fatal(e)
	}
	change(m)
	var e error
	f.raw[p], e = json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
}
func TestPoolEngineStrictJSON(t *testing.T) {
	f := newPoolFixture(true)
	p := "/v1.56/containers/" + poolFixtureWorker + "/json"
	valid := f.raw[p]
	if _, e := poolDecodeInspect(valid); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty", nil}, {"oversize", bytes.Repeat([]byte(" "), MaxResponse+1)}, {"duplicate", []byte(`{"Id":"a","Id":"b"}`)}, {"nested_duplicate", []byte(`{"Extension":{"x":1,"x":2}}`)}, {"trailing", append(bytes.Clone(valid), []byte(`{}`)...)}, {"root_array", []byte(`[]`)}, {"deep", []byte(strings.Repeat("[", 66) + strings.Repeat("]", 66))},
	}
	for _, replacement := range []string{`null`, `-1`, `9223372036854775808`, `1.0`, `1e2`, `"512"`, `true`} {
		raw := bytes.Replace(valid, []byte(`"PidsLimit":512`), []byte(`"PidsLimit":`+replacement), 1)
		cases = append(cases, struct {
			name string
			raw  []byte
		}{"pids_" + replacement, raw})
	}
	for _, key := range []string{"Id", "Image", "State", "Config", "HostConfig", "GraphDriver"} {
		var m map[string]any
		json.Unmarshal(valid, &m)
		delete(m, key)
		raw, _ := json.Marshal(m)
		cases = append(cases, struct {
			name string
			raw  []byte
		}{"missing_" + key, raw})
		json.Unmarshal(valid, &m)
		m[strings.ToLower(key)] = m[key]
		delete(m, key)
		raw, _ = json.Marshal(m)
		cases = append(cases, struct {
			name string
			raw  []byte
		}{"alias_" + key, raw})
	}
	for _, key := range []string{"Running", "Paused", "Restarting", "Dead"} {
		raw := bytes.Replace(valid, []byte(`"`+key+`":`+map[bool]string{true: "true", false: "false"}[key == "Running"]), []byte(`"`+key+`":null`), 1)
		cases = append(cases, struct {
			name string
			raw  []byte
		}{"bool_null_" + key, raw})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := poolDecodeInspect(tc.raw); !errors.Is(e, ErrPoolInvalid) {
				t.Fatalf("accepted %v", e)
			}
		})
	}
	// Unknown extensions are scanned but not copied, including secret-bearing data.
	o, e := poolDecodeInspect(valid)
	raw, _ := json.Marshal(o)
	if e != nil || bytes.Contains(raw, []byte("secret-canary")) {
		t.Fatalf("secret retention %v", e)
	}
}
func TestPoolEngineRequiredTypes(t *testing.T) {
	for _, kind := range []string{"version", "info", "inspect"} {
		t.Run(kind, func(t *testing.T) {
			f := newPoolFixture(true)
			p := "/v1.56/" + kind
			if kind == "inspect" {
				p = "/v1.56/containers/" + poolFixtureWorker + "/json"
			}
			var valid map[string]any
			json.Unmarshal(f.raw[p], &valid)
			var visit func(map[string]any, []string)
			visit = func(m map[string]any, prefix []string) {
				for key, v := range m {
					if key == "Env" {
						continue
					}
					keys := append(append([]string{}, prefix...), key)
					if nested, ok := v.(map[string]any); ok {
						if key != "Labels" {
							visit(nested, keys)
						}
					}
					for _, mode := range []string{"null", "missing", "wrong_type"} {
						t.Run(strings.Join(keys, "_")+"_"+mode, func(t *testing.T) {
							var copy map[string]any
							json.Unmarshal(f.raw[p], &copy)
							target := copy
							for _, k := range prefix {
								target = target[k].(map[string]any)
							}
							switch mode {
							case "null":
								target[key] = nil
							case "missing":
								delete(target, key)
							case "wrong_type":
								if _, ok := v.(string); ok {
									target[key] = 1
								} else {
									target[key] = "wrong"
								}
							}
							raw, _ := json.Marshal(copy)
							var e error
							switch kind {
							case "version":
								_, _, e = poolDecodeVersion(raw)
							case "info":
								_, e = poolDecodeInfo(raw)
							case "inspect":
								_, e = poolDecodeInspect(raw)
							}
							if e == nil {
								t.Fatal("accepted required field mutation")
							}
						})
					}
				}
			}
			visit(valid, nil)
		})
	}
}
func TestPoolEngineInventory(t *testing.T) {
	for _, name := range []string{"null", "duplicate", "short_id", "oversize", "unowned", "missing_core", "nonrunning", "pid_zero", "labels_count", "labels_oversize"} {
		t.Run(name, func(t *testing.T) {
			f := newPoolFixture(true)
			p := "/v1.56/containers/" + poolFixtureWorker + "/json"
			want := ErrPoolInvalid
			switch name {
			case "null":
				f.raw["/v1.56/containers/json?all=1"] = []byte(`null`)
			case "duplicate":
				f.raw["/v1.56/containers/json?all=1"] = []byte(`[{"Id":"` + poolFixtureCID + `"},{"Id":"` + poolFixtureCID + `"}]`)
			case "short_id":
				f.raw["/v1.56/containers/json?all=1"] = []byte(`[{"Id":"abc"}]`)
			case "oversize":
				a := []any{}
				for i := 0; i < 33; i++ {
					a = append(a, map[string]any{"Id": fmt.Sprintf("%064x", i)})
				}
				f.raw["/v1.56/containers/json?all=1"], _ = json.Marshal(a)
			case "unowned":
				poolRewrite(t, f, p, func(m map[string]any) { m["Config"].(map[string]any)["Labels"] = map[string]any{} })
			case "missing_core":
				f.raw["/v1.56/containers/json?all=1"] = []byte(`[]`)
				want = ErrPoolUnstable
			case "nonrunning":
				poolRewrite(t, f, p, func(m map[string]any) {
					s := m["State"].(map[string]any)
					s["Status"] = "created"
					s["Running"] = false
					s["Pid"] = 0
					s["StartedAt"] = "0001-01-01T00:00:00Z"
				})
				want = ErrPoolUnstable
			case "pid_zero":
				poolRewrite(t, f, p, func(m map[string]any) { m["State"].(map[string]any)["Pid"] = 0 })
			case "labels_count":
				poolRewrite(t, f, p, func(m map[string]any) {
					labels := map[string]string{}
					for i := 0; i < 65; i++ {
						labels[fmt.Sprint(i)] = "v"
					}
					m["Config"].(map[string]any)["Labels"] = labels
				})
			case "labels_oversize":
				poolRewrite(t, f, p, func(m map[string]any) {
					m["Config"].(map[string]any)["Labels"] = map[string]string{"large": strings.Repeat("a", 513)}
				})
			}
			rejectPool(t, f, want)
		})
	}
}
func poolPipeReader(response string, seen chan<- *http.Request, dials *atomic.Int32) poolGet {
	return poolEngineReader(func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			req, e := http.ReadRequest(bufio.NewReader(server))
			if e == nil {
				if seen != nil {
					seen <- req
				}
				io.WriteString(server, response)
			}
		}()
		return client, nil
	})
}
func TestPoolEngineTransport(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           error
	}{
		{"ok", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}", nil},
		{"redirect", "HTTP/1.1 302 Found\r\nLocation: http://secret-canary.invalid/token\r\nContent-Length: 13\r\n\r\nsecret-canary", ErrPoolInvalid},
		{"body_limit", "HTTP/1.1 200 OK\r\nContent-Length: 1048577\r\n\r\n" + strings.Repeat("x", MaxResponse+1), ErrPoolUnavailable},
		{"header_limit", "HTTP/1.1 200 OK\r\nX-Canary: " + strings.Repeat("x", MaxResponse+1) + "\r\nContent-Length: 2\r\n\r\n{}", ErrPoolUnavailable},
		{"error_body", "HTTP/1.1 500 Error\r\nContent-Length: 13\r\n\r\nsecret-canary", ErrPoolUnavailable},
		{"inspect_disappeared", "HTTP/1.1 404 Error\r\nContent-Length: 13\r\n\r\nsecret-canary", ErrPoolUnstable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			seen := make(chan *http.Request, 1)
			get := poolPipeReader(tc.response, seen, &calls)
			raw, e := get(context.Background(), "/v1.56/containers/"+poolFixtureCID+"/json")
			if tc.want == nil {
				if e != nil || string(raw) != "{}" {
					t.Fatal(e)
				}
			} else {
				if !errors.Is(e, tc.want) || raw != nil || strings.Contains(e.Error(), "secret") {
					t.Fatalf("unsafe response %v", e)
				}
			}
			req := <-seen
			if req.Method != "GET" || req.URL.RequestURI() != "/v1.56/containers/"+poolFixtureCID+"/json" || req.Header.Get("Authorization") != "" || calls.Load() != 1 {
				t.Fatal("transport widened")
			}
		})
	}
	for _, p := range []string{"/v1.56/containers/json", "/v1.56/containers/json?all=0", "/v1.56/containers/json?all=1&limit=1", "/v1.56/containers/%61/json", "http://host/path", "/v1.55/info", "/v1.56/info?x=1", "//v1.56/info", "/v1.56/containers/" + strings.Repeat("A", 64) + "/json"} {
		t.Run(p, func(t *testing.T) {
			var calls atomic.Int32
			get := poolPipeReader("", nil, &calls)
			if _, e := get(context.Background(), p); !errors.Is(e, ErrPoolInvalid) || calls.Load() != 0 {
				t.Fatalf("dialed forbidden path %v", e)
			}
		})
	}
	var calls atomic.Int32
	get := poolPipeReader("", nil, &calls)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if raw, e := get(ctx, "/v1.56/info"); raw != nil || !errors.Is(e, context.Canceled) || calls.Load() != 0 {
		t.Fatal("cancelled dial")
	}
}
func TestPoolCensusSecurityProjection(t *testing.T) {
	for _, name := range []string{"label", "pid", "limits", "image", "upper", "set", "nonrunning"} {
		t.Run(name, func(t *testing.T) {
			f := newPoolFixture(true)
			calls := 0
			f.hook = func(s string, _ int) error {
				if s == "get:/v1.56/version" {
					calls++
					if calls == 2 {
						p := "/v1.56/containers/" + poolFixtureWorker + "/json"
						poolRewrite(t, f, p, func(m map[string]any) {
							switch name {
							case "label":
								m["Config"].(map[string]any)["Labels"].(map[string]any)["new"] = "changed"
							case "pid":
								m["State"].(map[string]any)["Pid"] = 303
							case "limits":
								m["HostConfig"].(map[string]any)["Memory"] = 1
							case "image":
								m["Image"] = "sha256:" + strings.Repeat("e", 64)
							case "upper":
								m["GraphDriver"].(map[string]any)["Data"].(map[string]any)["UpperDir"] = Root + "/docker/overlay2/other/diff"
							case "nonrunning":
								m["State"].(map[string]any)["Running"] = false
							}
						})
						if name == "set" {
							f.raw["/v1.56/containers/json?all=1"] = []byte(`[{"Id":"` + poolFixtureCID + `"}]`)
						}
					}
				}
				return nil
			}
			want := ErrPoolInvalid
			if name == "set" || name == "nonrunning" {
				want = ErrPoolUnstable
			}
			rejectPool(t, f, want)
		})
	}
}
func TestPoolPlanDetached(t *testing.T) {
	f := newPoolFixture(false)
	p, e := poolPlan(f.plan)
	if e != nil {
		t.Fatal(e)
	}
	f.plan.Core[0].ContainerIDs[0] = "changed"
	f.plan.Storage[0].ID = "changed"
	if reflect.DeepEqual(p, f.plan) || p.Core[0].ContainerIDs[0] != poolFixtureCID || p.Storage[0].ID != "core" {
		t.Fatal("plan aliases")
	}
}
