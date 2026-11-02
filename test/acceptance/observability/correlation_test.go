package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
)

func TestCorrelationSnapshotNeedsSourceProofsNotColdOutage(t *testing.T) {
	m := evidenceReport(t).Physical.Cold
	settlements := []map[string]any{}
	for _, s := range m.Settlements {
		settlements = append(settlements, map[string]any{"agent_turn_id": s.TurnID, "terminal_message_id": s.MessageID, "terminal_receipt_at": s.From, "completed_at": s.To, "terminal_receipt_to_settlement_ms": s.Milliseconds})
	}
	d := m.Dispatch
	value := map[string]any{"project": m.Project, "measurement_id": m.ID, "controlled_acp_fixture": true, "fixture_checksum": m.LegacyBefore, "v2_bytes_digest": m.LegacyBefore, "expected_models": m.Expected, "fragment_proofs": m.Sources, "runtimes": []map[string]any{{"worker_session_id": m.SessionID, "runtime_instance_id": m.RuntimeID, "generation": m.Generation}}, "measured_settlements": settlements, "measured_next_dispatch": map[string]any{"agent_turn_id": d.TurnID, "command_message_id": d.MessageID, "terminal_receipt_at": d.From, "acknowledged_at": d.To, "terminal_receipt_to_next_dispatch_ms": d.Milliseconds}}
	valid, _ := json.Marshal(value)
	for _, mode := range []string{"correlation-snapshot", "snapshot"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "smoke-pending.json"), valid, 0600); err != nil {
				t.Fatal(err)
			}
			err := FinalizeScenarioSnapshot(mode, m.Project, root, valid)
			if mode == "snapshot" {
				if err == nil || !strings.Contains(err.Error(), "cold-before.json") {
					t.Fatalf("cold gate bypassed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(root, "correlation-snapshot.json"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("missing private correlation snapshot")
			}
			if _, err := os.Stat(filepath.Join(root, "smoke-passed.json")); !os.IsNotExist(err) {
				t.Fatal("fabricated cold smoke proof")
			}
			if FinalizeScenarioSnapshot(mode, m.Project, root, valid) == nil {
				t.Fatal("overwrote snapshot")
			}
		})
	}
	for _, key := range []string{"fragment_proofs", "fixture_checksum", "expected_models"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "smoke-pending.json"), valid, 0600); err != nil {
				t.Fatal(err)
			}
			var bad map[string]any
			json.Unmarshal(valid, &bad)
			delete(bad, key)
			raw, _ := json.Marshal(bad)
			if FinalizeScenarioSnapshot("correlation-snapshot", m.Project, root, raw) == nil {
				t.Fatal("accepted missing source proof")
			}
			if _, err := os.Stat(filepath.Join(root, "correlation-snapshot.json")); !os.IsNotExist(err) {
				t.Fatal("published failed snapshot")
			}
		})
	}
}

func TestCorrelationLokiRejectsEmptyPositive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if correlationSeriesFixture(w, r) {
			return
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
	}))
	defer server.Close()
	scope := CorrelationScope{RunID: "run-a", TurnID: "turn-a", TenantID: "tenant", WorkspaceID: "workspace", SessionID: "session"}
	_, err := VerifyCorrelationLoki(context.Background(), server.URL, "synthetic", scope, time.Unix(100, 0), time.Unix(200, 0), false)
	if err == nil {
		t.Fatal("empty positive query passed")
	}
}

func TestCorrelationLokiReadAPI(t *testing.T) {
	const turn = `{"ts":"1970-01-01T00:02:30Z","level":"info","service":"switchback-backend","component":"worker","event":"turn_terminal","schema_version":"switchback.log.v1","scope":{"tenant_id":"tenant","workspace_id":"workspace","worker_session_id":"session","worker_run_id":"run-a","agent_turn_id":"turn-a"},"code":"ok"}`
	run := strings.ReplaceAll(strings.ReplaceAll(turn, `"turn_terminal"`, `"run_terminal"`), `,"agent_turn_id":"turn-a"`, ``)
	response := func(labels string, lines ...string) string {
		values := [][]string{}
		for _, line := range lines {
			values = append(values, []string{"150000000000", line})
		}
		raw, _ := json.Marshal(values)
		return `{"status":"success","data":{"resultType":"streams","result":[{"stream":` + labels + `,"values":` + string(raw) + `}]}}`
	}
	labels := `{"service":"switchback-backend","environment":"local","component":"worker","level":"info"}`
	for _, tc := range []struct {
		name, body  string
		cross, pass bool
	}{
		{"valid", response(labels, turn, run), false, true},
		{"valid detected level", response(strings.TrimSuffix(labels, "}")+`,"detected_level":"info"}`, turn, run), false, true},
		{"wrong detected level", response(strings.TrimSuffix(labels, "}")+`,"detected_level":"error"}`, turn, run), false, false},
		{"other scope valid", response(labels, strings.ReplaceAll(turn, "-a", "-b"), strings.ReplaceAll(run, "-a", "-b")), false, false},
		{"wrong Turn", response(labels, strings.ReplaceAll(turn, "turn-a", "turn-b"), run), false, false},
		{"wrong Run", response(labels, strings.ReplaceAll(turn, "run-a", "run-b"), run), false, false},
		{"extra identity label", response(strings.TrimSuffix(labels, "}")+`,"worker_run_id":"run-a"}`, turn, run), false, false},
		{"unknown event field", response(labels, strings.TrimSuffix(turn, "}")+`,"message":"unsafe"}`, run), false, false},
		{"unknown scope field", response(labels, strings.ReplaceAll(turn, `"tenant_id":`, `"user_id":"user","tenant_id":`), run), false, false},
		{"failed terminal", response(labels, strings.ReplaceAll(turn, `"ok"`, `"failed"`), run), false, false},
		{"malformed", `{`, false, false},
		{"status error", `{"status":"error","data":{"resultType":"streams","result":[]}}`, false, false},
		{"empty positive", `{"status":"success","data":{"resultType":"streams","result":[]}}`, false, false},
		{"empty crossed", `{"status":"success","data":{"resultType":"streams","result":[]}}`, true, true},
		{"nonempty crossed", response(labels, turn), true, false},
		{"truncated limit", response(labels, repeatCorrelationLine(turn, 1000)...), false, false},
		{"oversized", strings.Repeat(" ", 4<<20) + `{}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/api/datasources/proxy/uid/switchback-loki/") {
					t.Error("not read API")
				}
				u, p, _ := r.BasicAuth()
				if u != "admin" || p != "synthetic" {
					t.Error("missing synthetic auth")
				}
				if strings.Contains(r.URL.Query().Get("query"), "| json") {
					t.Error("query hides stored labels")
				}
				if correlationSeriesFixture(w, r) {
					return
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := VerifyCorrelationLoki(context.Background(), server.URL, "synthetic", CorrelationScope{TenantID: "tenant", WorkspaceID: "workspace", SessionID: "session", RunID: "run-a", TurnID: "turn-a"}, time.Unix(100, 0), time.Unix(200, 0), tc.cross)
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v error=%v", tc.pass, err)
			}
		})
	}
}

func correlationSeriesFixture(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasSuffix(r.URL.Path, "/series") {
		return false
	}
	fmt.Fprint(w, `{"status":"success","data":[{"service":"switchback-backend","environment":"local","component":"worker","level":"info"}]}`)
	return true
}

func repeatCorrelationLine(line string, n int) []string {
	v := make([]string, n)
	for i := range v {
		v[i] = line
	}
	return v
}

func TestCorrelationLangfuseReadAPI(t *testing.T) {
	expected := lf.IngestionEvent{Type: "generation-create", Body: map[string]any{"id": "gen-a", "traceId": "trace-a", "parentObservationId": "span-a", "name": "model.generation", "startTime": "2026-01-01T00:00:00Z", "endTime": "2026-01-01T00:00:01Z", "model": "controlled", "level": "DEFAULT", "statusMessage": "ok", "metadata": map[string]any{"worker_run_id": `"run-a"`, "agent_turn_id": `"turn-a"`, "frame_sha256": `"sha256:abc"`}, "usageDetails": map[string]int64{"input": 11, "output": 7, "total": 18}, "input": "{}", "output": "{}"}}
	const valid = `{"id":"gen-a","traceId":"trace-a","parentObservationId":"span-a","name":"model.generation","type":"GENERATION","startTime":"2026-01-01T00:00:00Z","endTime":"2026-01-01T00:00:01Z","model":"controlled","level":"DEFAULT","statusMessage":"ok","metadata":{"worker_run_id":"run-a","agent_turn_id":"turn-a","frame_sha256":"sha256:abc"},"usageDetails":{"input":11,"output":7,"total":18},"input":"{}","output":"{}"}`
	for _, tc := range []struct {
		name, from, to string
		pass           bool
	}{{"valid", "", "", true}, {"id", "gen-a", "gen-b", false}, {"trace", "trace-a", "trace-b", false}, {"parent", "span-a", "span-b", false}, {"metadata", "run-a", "run-b", false}, {"Turn", "turn-a", "turn-b", false}, {"digest", "sha256:abc", "sha256:def", false}, {"usage", `"input":11`, `"input":12`, false}, {"truncated", "}", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			body := valid
			if tc.from != "" {
				body = strings.Replace(valid, tc.from, tc.to, 1)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/public/observations/gen-a" {
					t.Error("unexpected write or ID")
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client, err := lf.NewClient(lf.ClientOptions{BaseURL: server.URL, PublicKey: "synthetic", SecretKey: "synthetic", HTTPClient: correlationHTTP()})
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyCorrelationLangfuse(context.Background(), client, expected)
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v error=%v", tc.pass, err)
			}
		})
	}
}

func TestCorrelationRejectsRedirect(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	_, err := VerifyCorrelationLoki(context.Background(), redirect.URL, "secret", CorrelationScope{}, time.Unix(1, 0), time.Unix(2, 0), false)
	if err == nil || calls != 0 {
		t.Fatal("followed redirect")
	}
	client, _ := lf.NewClient(lf.ClientOptions{BaseURL: redirect.URL, PublicKey: "synthetic", SecretKey: "secret", HTTPClient: correlationHTTP()})
	_, err = client.GetObservation(context.Background(), "id")
	if err == nil || calls != 0 {
		t.Fatal("Langfuse followed redirect")
	}
}

func TestCorrelationTraceReadAPI(t *testing.T) {
	event := lf.IngestionEvent{Type: "trace-create", Body: map[string]any{"id": "trace-a", "name": "worker.run", "timestamp": "2026-01-01T00:00:00Z", "input": "{}", "metadata": map[string]any{"worker_run_id": `"run-a"`}}}
	for _, tc := range []struct {
		name, body string
		pass       bool
	}{
		{"valid", `{"id":"trace-a","name":"worker.run","timestamp":"2026-01-01T00:00:00Z","input":"{}","output":null,"metadata":{"worker_run_id":"run-a"}}`, true},
		{"wrong identity", `{"id":"trace-b","name":"worker.run","timestamp":"2026-01-01T00:00:00Z","input":"{}","metadata":{"worker_run_id":"run-a"}}`, false},
		{"wrong provenance", `{"id":"trace-a","name":"worker.run","timestamp":"2026-01-01T00:00:00Z","input":"{}","metadata":{"worker_run_id":"run-b"}}`, false},
		{"empty", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/public/traces/trace-a" || r.URL.Query().Get("fields") != "core,io" {
					t.Error("unexpected read API")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, _ := lf.NewClient(lf.ClientOptions{BaseURL: server.URL, PublicKey: "synthetic", SecretKey: "synthetic", HTTPClient: correlationHTTP()})
			err := VerifyCorrelationLangfuse(context.Background(), client, event)
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v error=%v", tc.pass, err)
			}
		})
	}
}

func TestCorrelationLokiRequiresExactDatabaseTerminalTimes(t *testing.T) {
	const body = `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"service":"switchback-backend","environment":"local","component":"worker","level":"info"},"values":[["150000000000","{\"ts\":\"1970-01-01T00:02:30Z\",\"level\":\"info\",\"service\":\"switchback-backend\",\"component\":\"worker\",\"event\":\"turn_terminal\",\"schema_version\":\"switchback.log.v1\",\"scope\":{\"tenant_id\":\"tenant\",\"workspace_id\":\"workspace\",\"worker_session_id\":\"session\",\"worker_run_id\":\"run\",\"agent_turn_id\":\"turn\"},\"code\":\"ok\"}"],["151000000000","{\"ts\":\"1970-01-01T00:02:31Z\",\"level\":\"info\",\"service\":\"switchback-backend\",\"component\":\"worker\",\"event\":\"run_terminal\",\"schema_version\":\"switchback.log.v1\",\"scope\":{\"tenant_id\":\"tenant\",\"workspace_id\":\"workspace\",\"worker_session_id\":\"session\",\"worker_run_id\":\"run\"},\"code\":\"ok\"}"]] }]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if correlationSeriesFixture(w, r) {
			return
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name      string
		turn, run int64
		pass      bool
	}{{"exact", 150, 151, true}, {"wrong Turn terminal", 149, 151, false}, {"wrong Run terminal", 150, 152, false}} {
		t.Run(tc.name, func(t *testing.T) {
			scope := CorrelationScope{TenantID: "tenant", WorkspaceID: "workspace", SessionID: "session", RunID: "run", TurnID: "turn", TerminalAt: time.Unix(tc.turn, 0), RunTerminalAt: time.Unix(tc.run, 0)}
			_, err := VerifyCorrelationLoki(context.Background(), server.URL, "synthetic", scope, time.Unix(100, 0), time.Unix(200, 0), false)
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v error=%v", tc.pass, err)
			}
		})
	}
}

func TestCorrelationLokiIndexedSeriesFailClosed(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"extra identity", `{"status":"success","data":[{"service":"switchback-backend","environment":"local","component":"worker","level":"info","worker_run_id":"run"}]}`},
		{"metadata indexed", `{"status":"success","data":[{"service":"switchback-backend","environment":"local","component":"worker","level":"info","detected_level":"info"}]}`},
		{"missing label", `{"status":"success","data":[{"service":"switchback-backend","environment":"local","component":"worker"}]}`},
		{"missing data", `{"status":"success"}`},
		{"empty positive", `{"status":"success","data":[]}`},
		{"status error", `{"status":"error","data":[]}`},
		{"malformed", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.HasSuffix(r.URL.Path, "/series") {
					t.Error("queried lines after invalid indexed labels")
				}
				q := r.URL.Query()
				if q.Get("start") != "100000000000" || q.Get("end") != "200000000000" || q.Get("match[]") != `{service="switchback-backend",environment="local",component="worker",level="info"}` {
					t.Error("series query not exact bounded selector")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			_, err := VerifyCorrelationLoki(context.Background(), server.URL, "synthetic", CorrelationScope{RunID: "run", TurnID: "turn"}, time.Unix(100, 0), time.Unix(200, 0), false)
			if err == nil || calls != 1 {
				t.Fatalf("invalid series accepted: %v calls=%d", err, calls)
			}
		})
	}
}

func TestCorrelationPrometheusReadAPI(t *testing.T) {
	for _, tc := range []struct {
		name, override string
		pass           bool
	}{{"valid", "", true}, {"empty", `{"status":"success","data":{"resultType":"vector","result":[]}}`, false}, {"status error", `{"status":"error"}`, false}, {"malformed", `{`, false}, {"not numeric", `{"status":"success","data":{"resultType":"vector","result":[{"value":[200,"NaN"]}]}}`, false}, {"insufficient counts", `{"status":"success","data":{"resultType":"vector","result":[{"value":[200,"1"]}]}}`, false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v1/query" || r.URL.Query().Get("time") == "" {
					t.Error("unbounded or wrong query")
				}
				if tc.override != "" {
					fmt.Fprint(w, tc.override)
					return
				}
				value := "1"
				query := r.URL.Query().Get("query")
				if strings.HasPrefix(query, "sum(") {
					value = "2"
				}
				if strings.Contains(query, "last_success") {
					value = "190"
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[200,"%s"]}]}}`, value)
			}))
			defer server.Close()
			_, err := VerifyCorrelationPrometheus(context.Background(), server.URL, time.Unix(200, 0), time.Unix(180, 0))
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v error=%v", tc.pass, err)
			}
		})
	}
}
