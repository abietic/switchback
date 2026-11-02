//go:build integration

package localproduct

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

type initialExperienceHTTPReceipt struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

// Only the remote Langfuse storage boundary is simulated. This stores actual
// production ingestion and serves bounded public HTTP readback. It never sees
// PG, writes projection jobs, or returns an ExportAndVerify verdict.
type initialExperienceStorage struct {
	server       *httptest.Server
	mu           sync.Mutex
	traces       map[string]json.RawMessage
	observations map[string]json.RawMessage
	receipts     []initialExperienceHTTPReceipt
}

func newInitialExperienceStorage(t *testing.T) *initialExperienceStorage {
	t.Helper()
	s := &initialExperienceStorage{traces: map[string]json.RawMessage{}, observations: map[string]json.RawMessage{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.receipts) >= 24 {
			t.Error("unbounded projection HTTP traffic")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/public/ingestion" {
			raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			if err != nil || len(raw) > 1<<20 {
				t.Error("invalid ingestion size")
				w.WriteHeader(400)
				return
			}
			var batch lf.IngestionBatch
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&batch); err != nil || len(batch.Batch) < 1 || len(batch.Batch) > 3 {
				t.Error("invalid ingestion")
				w.WriteHeader(400)
				return
			}
			s.receipts = append(s.receipts, initialExperienceHTTPReceipt{r.Method, r.URL.Path, append([]byte(nil), raw...)})
			for _, event := range batch.Batch {
				metadata, ok := event.Body["metadata"].(map[string]any)
				if !ok {
					t.Error("missing metadata")
					w.WriteHeader(400)
					return
				}
				for key, value := range metadata {
					encoded, ok := value.(string)
					if !ok {
						t.Error("invalid encoded metadata")
						w.WriteHeader(400)
						return
					}
					var decoded any
					d := json.NewDecoder(strings.NewReader(encoded))
					d.UseNumber()
					if err := d.Decode(&decoded); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					metadata[key] = decoded
				}
				id, ok := event.Body["id"].(string)
				if !ok || id == "" {
					t.Error("missing storage ID")
					w.WriteHeader(400)
					return
				}
				var stored []byte
				if event.Type == "trace-create" {
					stored, _ = json.Marshal(event.Body)
					s.traces[id] = stored
				} else {
					event.Body["type"] = "SPAN"
					if event.Type == "generation-create" {
						event.Body["type"] = "GENERATION"
						usage := event.Body["usageDetails"].(map[string]any)
						event.Body["usage"] = map[string]any{"input": usage["input"], "output": usage["output"], "total": usage["total"], "unit": "TOKENS"}
					} else if event.Type != "span-create" {
						t.Errorf("unexpected ingestion type %s", event.Type)
						w.WriteHeader(400)
						return
					}
					stored, _ = json.Marshal(event.Body)
					s.observations[id] = stored
				}
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var raw []byte
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/public/traces/") {
			raw = s.traces[strings.TrimPrefix(r.URL.Path, "/api/public/traces/")]
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/public/observations/") {
			raw = s.observations[strings.TrimPrefix(r.URL.Path, "/api/public/observations/")]
		}
		if len(raw) == 0 {
			t.Errorf("unrecognized/missing readback %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.receipts = append(s.receipts, initialExperienceHTTPReceipt{r.Method, r.URL.Path, append([]byte(nil), raw...)})
		_, _ = w.Write(raw)
	}))
	t.Cleanup(s.server.Close)
	return s
}
func (s *initialExperienceStorage) logReceipts(t *testing.T) {
	t.Helper()
	s.server.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, receipt := range s.receipts {
		raw, err := json.Marshal(receipt)
		supplementMust(t, err)
		t.Logf("synthetic HTTP storage receipt=%s", raw)
	}
}

type initialExperienceDiagnostics struct {
	Status string `json:"langfuse_status"`
	Target struct {
		Session string `json:"worker_session_id"`
		Run     string `json:"worker_run_id"`
		Turn    string `json:"agent_turn_id"`
	} `json:"target"`
	Identity *struct {
		Version     string `json:"version"`
		Trace       string `json:"trace_id"`
		Observation string `json:"selected_observation_id"`
		Sources     []struct {
			Kind   string `json:"kind"`
			ID     string `json:"source_id"`
			Digest string `json:"frame_digest"`
		} `json:"sources"`
	} `json:"identity"`
	Links []struct{ Kind, URL string } `json:"links"`
}

func verifyInitialExperienceObservability(t *testing.T, f *supplementFixture, remote *initialExperienceStorage, e contractsv1.RuntimeControlEnvelope) {
	t.Helper()
	path := "/api/v1/views/worker_detail/" + string(e.WorkerSessionID) + "/diagnostics?worker_run_id=" + string(e.WorkerRunID) + "&agent_turn_id=" + string(e.AgentTurnID)
	read := func() initialExperienceDiagnostics {
		raw := initialExperienceHTTP(t, f, http.MethodGet, path, nil, http.StatusOK)
		var d initialExperienceDiagnostics
		supplementMust(t, json.Unmarshal(raw, &d))
		t.Logf("same-task diagnostics=%s", raw)
		if d.Target.Session != string(e.WorkerSessionID) || d.Target.Run != string(e.WorkerRunID) || d.Target.Turn != string(e.AgentTurnID) {
			t.Fatal("diagnostics changed canonical target")
		}
		return d
	}
	before := read()
	if (before.Status != "pending" && before.Status != "identity_unavailable") || before.Identity != nil {
		t.Fatalf("unverified source received navigation identity: %+v", before)
	}
	runners, ok := f.app.observability.(fragmentBackgroundRunners)
	if !ok {
		t.Fatal("production v3 runners unavailable")
	}
	// Publish all three durable sources before any job is claimed. This avoids
	// pretending a retryable missing parent is a projection success.
	for n := 0; n < 3; n++ {
		supplementMust(t, runners.Publisher.Tick(f.ctx))
	}
	// A model may be selected before its basis. Respect the persisted database
	// retry deadline and use the assembled publisher again; never rewrite jobs.
	retries := f.count(`SELECT count(*) FROM observation_candidates WHERE status='retryable'`)
	for n := 0; n < retries; n++ {
		var at time.Time
		var kind, id, code string
		var attempts int
		supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT kind,source_id,error_code,attempts,next_attempt_at FROM observation_candidates WHERE status='retryable' ORDER BY next_attempt_at,source_id,kind LIMIT 1`).Scan(&kind, &id, &code, &attempts, &at))
		t.Logf("publication awaiting persisted retry: kind=%s source=%s code=%s", kind, id, code)
		f.awaitDatabaseTime(at)
		supplementMust(t, runners.Publisher.Tick(f.ctx))
		if f.count(`SELECT count(*) FROM observation_candidates WHERE kind=$1 AND source_id=$2 AND status='published' AND attempts=$3`, kind, id, attempts+1) != 1 {
			t.Fatal("eligible publication did not converge")
		}
	}
	if n := f.count(`SELECT count(*) FROM observation_publications WHERE worker_run_id=$1`, string(e.WorkerRunID)); n != 3 {
		t.Fatalf("formal source publication count=%d", n)
	}
	for n := 0; n < 3; n++ {
		supplementMust(t, runners.Projector.Tick(f.ctx))
	}
	if n := f.count(`SELECT count(*) FROM observation_fragment_jobs WHERE version=$1 AND status='passed'`, artifactapp.FragmentProjectionVersion); n != 3 {
		t.Fatalf("production readback not verified: jobs=%s", f.jsonRow(`SELECT jsonb_agg(to_jsonb(j)) FROM observation_fragment_jobs j`))
	}
	after := read()
	if after.Status != "historically_verified" || after.Identity == nil || after.Identity.Version != artifactapp.FragmentProjectionVersion || len(after.Identity.Sources) != 2 {
		t.Fatalf("missing historically verified v3 navigation: %+v", after)
	}
	// Read the real accepted frames, compile them using the production compiler,
	// and independently bind wire/readback IDs and scopes to the original Run.
	rows, err := f.pool.Query(f.ctx, `SELECT kind,source_id,frame_digest,frame FROM observation_candidates WHERE worker_run_id=$1 ORDER BY kind`, string(e.WorkerRunID))
	supplementMust(t, err)
	defer rows.Close()
	seen := map[string]bool{}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for rows.Next() {
		var kind, id, digest string
		var frame []byte
		supplementMust(t, rows.Scan(&kind, &id, &digest, &frame))
		if artifactdomain.Checksum(frame) != digest {
			t.Fatal("candidate frame digest mismatch")
		}
		wire, err := (lf.FragmentCompiler{}).CompileObservationFragment(f.ctx, frame)
		supplementMust(t, err)
		var batch lf.IngestionBatch
		supplementMust(t, json.Unmarshal(wire, &batch))
		if len(batch.Batch) != 1 {
			t.Fatal("unexpected compiler event count")
		}
		event := batch.Batch[0]
		wireID := event.Body["id"].(string)
		raw := remote.observations[wireID]
		if kind == "run_basis" {
			raw = remote.traces[wireID]
		}
		var stored struct {
			ID       string         `json:"id"`
			Trace    string         `json:"traceId"`
			Metadata map[string]any `json:"metadata"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		supplementMust(t, decoder.Decode(&stored))
		for key, want := range map[string]string{"tenant_id": f.app.configuration.Profile.TenantID, "user_id": f.app.configuration.Profile.ActorUserID, "workspace_id": f.app.configuration.WorkspaceID, "worker_session_id": string(e.WorkerSessionID), "worker_run_id": string(e.WorkerRunID), "frame_sha256": digest, "source_role": kind, "projection_version": artifactapp.FragmentProjectionVersion, "compiler_revision": lf.FragmentCompilerRevision} {
			if stored.Metadata[key] != want {
				t.Fatalf("stored %s=%v want=%s", key, stored.Metadata[key], want)
			}
		}
		if kind != "run_basis" {
			for key, want := range map[string]string{"agent_session_id": string(e.AgentSessionID), "agent_turn_id": string(e.AgentTurnID), "runtime_instance_id": string(e.RuntimeInstanceID)} {
				if stored.Metadata[key] != want {
					t.Fatalf("stored %s=%v want=%s", key, stored.Metadata[key], want)
				}
			}
			if stored.Metadata["runtime_generation"] != json.Number(strconv.FormatUint(uint64(e.RuntimeGeneration), 10)) || stored.Trace != after.Identity.Trace {
				t.Fatal("remote Turn/model generation or trace mismatch")
			}
		}
		if (kind == "run_basis" && stored.ID != after.Identity.Trace) || (kind == "turn_basis" && stored.ID != after.Identity.Observation) {
			t.Fatal("compiler/readback/browser IDs disagree")
		}
		if kind != "model" {
			matched := false
			for _, source := range after.Identity.Sources {
				if source.Kind == kind && source.ID == id && source.Digest == digest {
					matched = true
				}
			}
			if !matched {
				t.Fatal("diagnostics source provenance mismatch")
			}
		}
		// At least one actual GET served the exact bytes being asserted, and each
		// accepted compiler event appeared in real production HTTP ingestion.
		gotRead, gotWire := false, false
		for _, receipt := range remote.receipts {
			if receipt.Method == http.MethodGet && bytes.Equal(receipt.Body, raw) && strings.HasSuffix(receipt.Path, "/"+wireID) {
				gotRead = true
				if kind == "model" {
					var response struct {
						StatusMessage string `json:"statusMessage"`
						Level         string `json:"level"`
					}
					supplementMust(t, json.Unmarshal(receipt.Body, &response))
					if response.StatusMessage != "ok" || response.Level != "DEFAULT" {
						t.Fatalf("successful model HTTP GET readback has unexpected status/level: %s", receipt.Body)
					}
				}
			}
			if receipt.Method == http.MethodPost {
				var sent lf.IngestionBatch
				supplementMust(t, json.Unmarshal(receipt.Body, &sent))
				for _, candidate := range sent.Batch {
					if reflect.DeepEqual(candidate, event) {
						gotWire = true
					}
				}
			}
		}
		if !gotRead || !gotWire {
			t.Fatal("compiled source lacks actual HTTP ingestion/readback receipt")
		}
		seen[kind] = true
	}
	supplementMust(t, rows.Err())
	if len(seen) != 3 {
		t.Fatalf("source kinds=%v", seen)
	}
	foundLogs, foundTrace := false, false
	for _, link := range after.Links {
		parsed, err := url.Parse(link.URL)
		supplementMust(t, err)
		if link.Kind == "langfuse" {
			if parsed.String() != remote.server.URL+"/project/initial-experience/traces/"+after.Identity.Trace {
				t.Fatal("browser trace URL differs from actual readback")
			}
			foundTrace = true
		}
		if link.Kind == "logs" {
			var panes map[string]struct{ Queries []struct{ Expr string } }
			supplementMust(t, json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes))
			want := `{service="switchback-backend",environment="local"} | json tenant_id="scope.tenant_id", workspace_id="scope.workspace_id", session_id="scope.worker_session_id", run_id="scope.worker_run_id", turn_id="scope.agent_turn_id"`
			for _, filter := range [][2]string{{"tenant_id", f.app.configuration.Profile.TenantID}, {"workspace_id", f.app.configuration.WorkspaceID}, {"session_id", string(e.WorkerSessionID)}, {"run_id", string(e.WorkerRunID)}, {"turn_id", string(e.AgentTurnID)}} {
				want += " | " + filter[0] + " = " + strconv.Quote(filter[1])
			}
			if parsed.Host != "localhost:3001" || parsed.Path != "/explore" || len(panes["local"].Queries) != 1 || panes["local"].Queries[0].Expr != want {
				t.Fatal("logs URL lost exact same-task selector")
			}
			foundLogs = true
		}
	}
	if !foundLogs || !foundTrace {
		t.Fatal("same-task diagnostic links incomplete")
	}
}
