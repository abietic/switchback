package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	"github.com/abietic/switchback/internal/platform/secretcheck"
)

func preparedFragmentProjection(t *testing.T) artifactapp.LoadedFragmentProjection {
	t.Helper()
	source := artifactapp.LoadedFragmentProjection{Version: artifactapp.FragmentProjectionVersion}
	for _, kind := range []artifactdomain.FragmentKind{artifactdomain.FragmentModel, artifactdomain.FragmentTurnBasis, artifactdomain.FragmentRunBasis} {
		f := compilerFragment(kind)
		if kind == artifactdomain.FragmentModel {
			f.Status, f.OutputJSON = "failed", " \n null \t"
		}
		frame, err := artifactdomain.EncodeObservationFragment(f)
		if err != nil {
			t.Fatal(err)
		}
		provenance := artifactapp.ObservationProvenance{CheckpointDigest: artifactdomain.Checksum([]byte("first")), InputDigest: artifactdomain.Checksum([]byte(f.InputJSON)), ReportDigest: artifactdomain.Checksum([]byte("report"))}
		if kind == artifactdomain.FragmentModel {
			provenance = artifactapp.ObservationProvenance{EnvelopeDigest: artifactdomain.Checksum([]byte("event"))}
		}
		prepared := (artifactapp.ObservationCapture{Compiler: FragmentCompiler{}, Scanner: acceptingScanner{}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: "session", RuntimeInstanceID: "runtime", RuntimeGeneration: 1}}).Prepare(context.Background(), frame, map[string]string{"evidence": "test"}, provenance)
		if prepared.ErrorCode != "" {
			t.Fatal(prepared.ErrorCode)
		}
		source.Frames = append(source.Frames, prepared.Frame)
		source.Receipts = append(source.Receipts, prepared.Receipt)
		if kind == artifactdomain.FragmentModel {
			id, _ := artifactdomain.ObservationFragmentID(f)
			source.Source = artifactapp.FragmentRef{Kind: string(kind), SourceID: id, FrameDigest: artifactdomain.Checksum(prepared.Frame)}
		}
	}
	return source
}

func TestFragmentProjectorProofTamperBeforeHTTP(t *testing.T) {
	for _, mutation := range []string{"wire", "scope", "frame", "version", "duplicate", "missing_parent", "wrong_parent"} {
		t.Run(mutation, func(t *testing.T) {
			source := preparedFragmentProjection(t)
			switch mutation {
			case "wire", "scope":
				var receipt artifactapp.ObservationScanReceipt
				if err := json.Unmarshal(source.Receipts[0], &receipt); err != nil {
					t.Fatal(err)
				}
				if mutation == "wire" {
					receipt.WireSHA256 = artifactdomain.Checksum([]byte("wrong wire"))
				} else {
					receipt.Scope.RuntimeInstanceID = "other-runtime"
				}
				source.Receipts[0], _ = json.Marshal(receipt)
			case "frame":
				source.Frames[0] = append(source.Frames[0], ' ')
			case "version":
				source.Version = "langfuse.projection.v2"
			case "duplicate":
				source.Frames[2] = source.Frames[1]
				source.Receipts[2] = source.Receipts[1]
			case "missing_parent":
				source.Frames = source.Frames[:2]
				source.Receipts = source.Receipts[:2]
			case "wrong_parent":
				other := preparedFragmentProjection(t)
				frame, _ := artifactdomain.DecodeObservationFragment(other.Frames[1])
				frame.Turn.AgentSessionID = "other-agent"
				raw, _ := artifactdomain.EncodeObservationFragment(frame)
				p := (artifactapp.ObservationCapture{Compiler: FragmentCompiler{}, Scanner: acceptingScanner{}, Scope: artifactapp.ObservationScanScope{WorkerSessionID: "session", RuntimeInstanceID: "runtime", RuntimeGeneration: 1}}).Prepare(context.Background(), raw, map[string]string{"proof": "test"}, artifactapp.ObservationProvenance{CheckpointDigest: artifactdomain.Checksum([]byte("first")), InputDigest: artifactdomain.Checksum([]byte(frame.InputJSON)), ReportDigest: artifactdomain.Checksum([]byte("report"))})
				if p.ErrorCode != "" {
					t.Fatal(p.ErrorCode)
				}
				source.Frames[1], source.Receipts[1] = p.Frame, p.Receipt
			}
			api := &durableRecordingAPI{}
			p, _ := NewFragmentProjector(api, acceptingScanner{})
			err := p.ExportAndVerify(context.Background(), source)
			var typed *artifactapp.ProjectionError
			if !errors.As(err, &typed) || typed.Code != "fragment_proof_invalid" || !typed.Permanent {
				t.Fatalf("proof tamper %v", err)
			}
			if api.ingestCalls != 0 {
				t.Fatal("invalid proof reached HTTP")
			}
		})
	}
}

func TestFragmentProjectorCurrentTokensAndHTTPFailures(t *testing.T) {
	for _, mode := range []string{"metadata_token", "io_token", "500", "207", "404", "read500", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			source := preparedFragmentProjection(t)
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
					if mode == "500" {
						w.WriteHeader(500)
					} else if mode == "207" {
						w.WriteHeader(207)
						_, _ = w.Write([]byte(`{"errors":[{"message":"synthetic-private-error"}]}`))
					} else {
						w.WriteHeader(202)
					}
					return
				}
				if mode == "404" {
					w.WriteHeader(404)
				} else {
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			client, _ := NewClient(ClientOptions{BaseURL: server.URL, PublicKey: "pk", SecretKey: "sk"})
			var scanner KnownTokenScanner = acceptingScanner{}
			if mode == "metadata_token" || mode == "io_token" {
				// compiler_revision exists only in emitted metadata, not the frame.
				token := FragmentCompilerRevision
				if mode == "io_token" {
					token = "9007199254740993"
				}
				var err error
				scanner, err = secretcheck.New([]secretcheck.KnownToken{{Label: "current-test-token", Value: token}})
				if err != nil {
					t.Fatal(err)
				}
			}
			p, _ := NewFragmentProjector(client, scanner)
			ctx := context.Background()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := p.ExportAndVerify(ctx, source)
			var typed *artifactapp.ProjectionError
			want := map[string]string{"metadata_token": "credential_detected", "io_token": "credential_detected", "500": "ingestion_unavailable", "207": "ingestion_unavailable", "404": "projection_not_ready", "read500": "readback_unavailable", "cancel": "projection_cancelled"}[mode]
			if !errors.As(err, &typed) || typed.Code != want || typed.Permanent != (mode == "metadata_token" || mode == "io_token") {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			if (mode == "metadata_token" || mode == "io_token" || mode == "cancel") && posts != 0 {
				t.Fatal("unsafe/cancelled data sent")
			}
		})
	}
}

type fragmentMemoryAPI struct {
	root         Trace
	observations map[string]Observation
}

func (m *fragmentMemoryAPI) Ingest(_ context.Context, b IngestionBatch) error {
	if m.observations == nil {
		m.observations = map[string]Observation{}
	}
	for _, event := range b.Batch {
		if event.Type == "trace-create" {
			body := map[string]any{}
			for k, v := range event.Body {
				body[k] = v
			}
			body["metadata"] = decodePinnedMetadata(body["metadata"])
			raw, _ := json.Marshal(body)
			if err := json.Unmarshal(raw, &m.root); err != nil {
				return err
			}
		} else {
			o := decodeObservation(event.Body, event.Type)
			m.observations[o.ID] = o
		}
	}
	return nil
}
func (m *fragmentMemoryAPI) GetTraceRoot(context.Context, string) (Trace, error) { return m.root, nil }
func (m *fragmentMemoryAPI) GetObservation(_ context.Context, id string) (Observation, error) {
	return m.observations[id], nil
}

func TestFragmentProjectorReplaysOldSourceWithoutOverwritingSibling(t *testing.T) {
	first := preparedFragmentProjection(t)
	second := preparedFragmentProjection(t)
	f, _ := artifactdomain.DecodeObservationFragment(second.Frames[0])
	f.ObservationID = "later-observation"
	raw, _ := artifactdomain.EncodeObservationFragment(f)
	var receipt artifactapp.ObservationScanReceipt
	if err := json.Unmarshal(second.Receipts[0], &receipt); err != nil {
		t.Fatal(err)
	}
	p := (artifactapp.ObservationCapture{Compiler: FragmentCompiler{}, Scanner: acceptingScanner{}, Scope: receipt.Scope}).Prepare(context.Background(), raw, map[string]string{"test": "sibling"}, receipt.Provenance)
	if p.ErrorCode != "" {
		t.Fatal(p.ErrorCode)
	}
	second.Frames[0], second.Receipts[0] = p.Frame, p.Receipt
	id, _ := artifactdomain.ObservationFragmentID(f)
	second.Source = artifactapp.FragmentRef{Kind: "model", SourceID: id, FrameDigest: artifactdomain.Checksum(p.Frame)}
	api := &fragmentMemoryAPI{}
	projector, _ := NewFragmentProjector(api, acceptingScanner{})
	for _, source := range []artifactapp.LoadedFragmentProjection{first, second, first} {
		if err := projector.ExportAndVerify(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.observations) != 3 {
		t.Fatalf("sibling was overwritten: %d observations", len(api.observations))
	}
}

func TestFragmentProjectorClientPreservesMetadataPrecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test","metadata":{"projection_version":"langfuse.projection.v3","huge":9007199254740993,"decimal":1.234567890123456789,"exponent":1e400}}`))
	}))
	defer server.Close()
	client, _ := NewClient(ClientOptions{BaseURL: server.URL, PublicKey: "pk", SecretKey: "sk"})
	root, err := client.GetTraceRoot(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := client.GetObservation(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []map[string]any{root.ExactMetadata, observation.Metadata} {
		for key, want := range map[string]string{"huge": "9007199254740993", "decimal": "1.234567890123456789", "exponent": "1e400"} {
			got, ok := metadata[key].(json.Number)
			if !ok || got.String() != want {
				t.Fatalf("metadata[%s]=%v precision/type lost", key, metadata[key])
			}
		}
	}
}

func TestFragmentProjectorExactHTTPReadback(t *testing.T) {
	for _, mutation := range []string{"none", "root_metadata_type", "root_metadata_extra", "root_metadata_missing", "span_parent", "model_parent", "model_type", "usage", "model_output"} {
		t.Run(mutation, func(t *testing.T) { testFragmentHTTPReadback(t, mutation) })
	}
}

func testFragmentHTTPReadback(t *testing.T, mutation string) {
	source := preparedFragmentProjection(t)
	var root map[string]any
	observations := map[string]Observation{}
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			var batch IngestionBatch
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if len(batch.Batch) != 3 || batch.Batch[0].Type != "trace-create" || batch.Batch[1].Type != "span-create" || batch.Batch[2].Type != "generation-create" {
				t.Errorf("parent ordering: %+v", batch)
			}
			for _, event := range batch.Batch {
				if event.Type == "trace-create" {
					root = event.Body
					root["metadata"] = decodePinnedMetadata(root["metadata"])
				} else {
					o := decodeObservation(event.Body, event.Type)
					observations[o.ID] = o
				}
			}
			w.WriteHeader(202)
			return
		}
		if strings.HasPrefix(r.URL.Path, tracePathPrefix) {
			metadata := root["metadata"].(map[string]any)
			switch mutation {
			case "root_metadata_type":
				metadata["tenant_id"] = 1
			case "root_metadata_extra":
				metadata["unexpected"] = "collision"
			case "root_metadata_missing":
				delete(metadata, "compiler_revision")
			}
			_ = json.NewEncoder(w).Encode(root)
			return
		}
		o := observations[strings.TrimPrefix(r.URL.Path, observationPathPrefix)]
		if o.Type == "SPAN" && mutation == "span_parent" {
			parent := "unexpected"
			o.ParentObservationID = &parent
		}
		if o.Type == "GENERATION" {
			switch mutation {
			case "model_parent":
				parent := "wrong"
				o.ParentObservationID = &parent
			case "model_type":
				o.Type = "SPAN"
			case "usage":
				o.Usage.Input++
			case "model_output":
				o.Output = json.RawMessage(`{"encoding":"json-text.v1","json":"null"}`)
			}
		}
		_ = json.NewEncoder(w).Encode(o)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, PublicKey: "pk", SecretKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := NewFragmentProjector(client, acceptingScanner{})
	err = p.ExportAndVerify(context.Background(), source)
	if mutation == "none" {
		if err != nil {
			t.Fatal(err)
		}
	} else {
		var typed *artifactapp.ProjectionError
		if !errors.As(err, &typed) || typed.Code != "projection_collision" || !typed.Permanent {
			t.Fatalf("collision classified as %v", err)
		}
	}
	if posts != 1 {
		t.Fatalf("posts=%d", posts)
	}
}
