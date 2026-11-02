package langfuse

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	secretcheck "github.com/abietic/switchback/internal/platform/secretcheck"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestDurableProjectorV2GoldenBytes(t *testing.T) {
	projection := durableSource("turn-1", "model-2", time.Date(2026, 9, 6, 10, 0, 0, 123456789, time.FixedZone("offset", 8*60*60)))
	projection.Turn.ModelObservations = append(projection.Turn.ModelObservations, durableModel(projection.Turn.Identity, "model-1", projection.Turn.StartedAt.Add(time.Millisecond)))
	batch, _, err := (&DurableProjector{}).buildBatch(projection)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(encoded)); got != "7121b2d620c1f7aec5a50da528c4e9897642d246708d5a66510f4917dcf023a7" {
		t.Fatalf("v2 golden bytes sha256=%s", got)
	}
}

func TestDurableProjectorBuildsDeterministicSourceScopedBatch(t *testing.T) {
	t.Parallel()
	projection := durableSource("turn-1", "model-2", time.Date(2026, 9, 6, 10, 0, 0, 123456789, time.FixedZone("offset", 8*60*60)))
	projection.Turn.ModelObservations = append(projection.Turn.ModelObservations,
		durableModel(projection.Turn.Identity, "model-1", projection.Turn.StartedAt.Add(time.Millisecond)))
	api := &durableRecordingAPI{}
	projector, err := NewDurableProjector(api, acceptingScanner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	first := mustJSON(t, api.lastBatch())
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	second := mustJSON(t, api.lastBatch())
	if string(first) != string(second) {
		t.Fatalf("retry batch changed\nfirst=%s\nsecond=%s", first, second)
	}
	batch := api.lastBatch()
	if len(batch.Batch) != 4 {
		t.Fatalf("events=%d", len(batch.Batch))
	}
	if batch.Batch[0].ID != "02770e62ba99c5a3c986f243db4e4eb2" {
		t.Fatalf("root event id=%s", batch.Batch[0].ID)
	}
	if batch.Batch[0].Body["id"] != "6135086af1e39de5acf36b6603ee704e" {
		t.Fatalf("trace id=%s", batch.Batch[0].Body["id"])
	}
	wantTransport := projection.Turn.StartedAt.UTC().Truncate(time.Millisecond)
	for _, event := range batch.Batch {
		if !event.Timestamp.Equal(projection.Root.StartedAt.UTC().Truncate(time.Millisecond)) {
			t.Fatalf("event timestamp=%s", event.Timestamp)
		}
		if started, ok := event.Body["startTime"].(time.Time); ok && started.Nanosecond()%int(time.Millisecond) != 0 {
			t.Fatalf("transport time retained nanos: %s", started)
		}
	}
	if got := batch.Batch[1].Body["startTime"]; !got.(time.Time).Equal(wantTransport) {
		t.Fatalf("turn start=%s want=%s", got, wantTransport)
	}
	metadata := batch.Batch[1].Body["metadata"].(map[string]any)
	if metadata["source_started_at"] != mustMetadataTransport(t, projection.Turn.StartedAt.Format(time.RFC3339Nano)) {
		t.Fatalf("source timestamp=%v", metadata["source_started_at"])
	}
	if batch.Batch[1].Body["level"] != "DEFAULT" {
		t.Fatalf("turn level=%v", batch.Batch[1].Body["level"])
	}
	rootMetadata := batch.Batch[0].Body["metadata"].(map[string]any)
	if rootMetadata["source_started_at"] != mustMetadataTransport(t, projection.Root.StartedAt.Format(time.RFC3339Nano)) {
		t.Fatalf("root source timestamp=%v", rootMetadata["source_started_at"])
	}
	if len(rootMetadata) != 6 {
		t.Fatalf("root metadata contains mutable or empty fields: %#v", rootMetadata)
	}
	if batch.Batch[2].Body["metadata"].(map[string]any)["source_observation_id"] != mustMetadataTransport(t, "model-1") {
		t.Fatalf("models not sorted: %#v", batch.Batch)
	}
}

func TestDurableProjectorRoundTripsOfficialHTTPJSONShape(t *testing.T) {
	t.Parallel()
	projection := durableSource("turn-http", "model-http", time.Date(2026, 9, 6, 1, 2, 3, 456789123, time.UTC))
	projection.Turn.Identity.AgentSessionID = "9007199254740993"
	projection.Turn.Identity.RuntimeGeneration = ^uint32(0)
	projection.Turn.ModelObservations[0].Generation = uint64(^uint32(0))
	var traceResponse Trace
	observations := map[string]Observation{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == ingestionPath:
			var batch IngestionBatch
			if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
				t.Errorf("decode ingestion: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, event := range batch.Batch {
				body := event.Body
				switch event.Type {
				case "trace-create":
					traceResponse = Trace{ID: body["id"].(string), Name: body["name"].(string), Timestamp: parseHTTPTime(t, body["timestamp"]), Input: raw(body["input"]), Metadata: decodeMetadata(body["metadata"])}
				case "span-create", "generation-create":
					if body["level"] == nil {
						writer.WriteHeader(http.StatusMultiStatus)
						_, _ = writer.Write([]byte(`{"errors":[{"message":"level is required"}]}`))
						return
					}
					o := decodeObservation(body, event.Type)
					observations[o.ID] = o
				}
			}
			writer.WriteHeader(http.StatusAccepted)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, tracePathPrefix):
			_ = json.NewEncoder(writer).Encode(traceResponse)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, observationPathPrefix):
			id := strings.TrimPrefix(request.URL.Path, observationPathPrefix)
			o, ok := observations[id]
			if !ok {
				http.NotFound(writer, request)
				return
			}
			_ = json.NewEncoder(writer).Encode(o)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, PublicKey: "pk", SecretKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	projector, _ := NewDurableProjector(client, acceptingScanner{})
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		_, expected, _ := projector.buildBatch(projection)
		t.Fatalf("HTTP round trip: %v\ntrace=%#v\nexpected=%#v", err, traceResponse, expected)
	}
}

func TestDurableProjectorPreservesEveryIOAsLosslessJSONText(t *testing.T) {
	projection := durableSource("turn-envelope", "model-envelope", time.Now().UTC())
	projection.Root.Input = json.RawMessage(" {\n \"unsafe\":9007199254740993,\"decimal\":1.234567890123456789,\"huge\":1e400 }")
	projection.Turn.Input = json.RawMessage(`[-9007199254740993,true,"escaped\\\\value"]`)
	projection.Output = json.RawMessage(`{"small":1e-400,"text":"unchanged"}`)
	projection.Turn.ModelObservations[0].Input = json.RawMessage(`"model input"`)
	projection.Turn.ModelObservations[0].Output = json.RawMessage(`{"answer":9007199254740993}`)
	original := mustJSON(t, projection)
	api := &durableRecordingAPI{}
	projector, _ := NewDurableProjector(api, acceptingScanner{})
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	batch := api.lastBatch()
	values := []struct {
		name string
		got  any
		want json.RawMessage
	}{
		{"root input", batch.Batch[0].Body["input"], projection.Root.Input},
		{"turn input", batch.Batch[1].Body["input"], projection.Turn.Input},
		{"turn output", batch.Batch[1].Body["output"], projection.Output},
		{"model input", batch.Batch[2].Body["input"], projection.Turn.ModelObservations[0].Input},
		{"model output", batch.Batch[2].Body["output"], projection.Turn.ModelObservations[0].Output},
	}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			encoded := raw(value.got)
			var envelope struct {
				Encoding string `json:"encoding"`
				JSON     string `json:"json"`
			}
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Encoding != "json-text.v1" || !bytes.Equal([]byte(envelope.JSON), value.want) {
				t.Fatalf("envelope=%s recovered=%q want=%q", encoded, envelope.JSON, value.want)
			}
		})
	}
	if after := mustJSON(t, projection); !bytes.Equal(after, original) {
		t.Fatalf("source mutated\nbefore=%s\nafter=%s", original, after)
	}
	if got := batch.Batch[1].Body["metadata"].(map[string]any)["runtime_generation"]; got != mustMetadataTransport(t, uint32(7)) {
		t.Fatalf("runtime_generation=%#v", got)
	}
}

func TestDurableProjectorEncodesEveryFlatMetadataValueForPinnedLangfuse(t *testing.T) {
	projection := durableSource("turn-metadata", "model-metadata", time.Now().UTC())
	projection.Root.TenantID = "1"
	projection.Root.UserID = "true"
	projection.Root.WorkspaceID = "null"
	projection.Root.WorkerSessionID = `{"scope":1}`
	projection.Root.WorkerRunID = `[1,2]`
	projection.Turn.Identity.TenantID = projection.Root.TenantID
	projection.Turn.Identity.UserID = projection.Root.UserID
	projection.Turn.Identity.WorkspaceID = projection.Root.WorkspaceID
	projection.Turn.Identity.WorkerSessionID = contractsv1.WorkerSessionID(projection.Root.WorkerSessionID)
	projection.Turn.Identity.WorkerRunID = contractsv1.WorkerRunID(projection.Root.WorkerRunID)
	projection.Turn.Identity.AgentSessionID = `"quoted"`
	projection.Turn.Identity.RuntimeGeneration = ^uint32(0)
	projection.Turn.ModelObservations[0].WorkerSessionID = projection.Root.WorkerSessionID
	projection.Turn.ModelObservations[0].WorkerRunID = projection.Root.WorkerRunID
	projection.Turn.ModelObservations[0].AgentTurnID = string(projection.Turn.Identity.AgentTurnID)
	projection.Turn.ModelObservations[0].RuntimeID = string(projection.Turn.Identity.RuntimeInstanceID)
	projection.Turn.ModelObservations[0].Generation = uint64(^uint32(0))

	projector, _ := NewDurableProjector(newDurableMemoryAPI(), acceptingScanner{})
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatalf("pinned fake API round trip: %v", err)
	}
	batch, _, err := projector.buildBatch(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range batch.Batch {
		metadata := event.Body["metadata"].(map[string]any)
		for key, transported := range metadata {
			encoded, ok := transported.(string)
			if !ok {
				t.Fatalf("%s metadata %s transport type=%T want string", event.Type, key, transported)
			}
			var restored any
			if err := json.Unmarshal([]byte(encoded), &restored); err != nil {
				t.Fatalf("%s metadata %s invalid transport %q: %v", event.Type, key, encoded, err)
			}
			if key == "runtime_generation" && restored != float64(^uint32(0)) {
				t.Fatalf("runtime_generation restored=%#v", restored)
			}
		}
	}
}

func TestPinnedMetadataDecodeExposesUnsafeIntegerIdentityCollisionWithoutTransportEncoding(t *testing.T) {
	decoded := decodePinnedMetadata(map[string]any{"agent_session_id": "9007199254740993"})
	if decoded["agent_session_id"] != float64(9007199254740992) {
		t.Fatalf("decoded=%#v", decoded["agent_session_id"])
	}
	if metadataEqual(decoded, map[string]any{"agent_session_id": "9007199254740993"}) {
		t.Fatal("unsafe integer identity collision was not detected")
	}
}

func TestDurableProjectorRejectsInvalidUTF8AndUnsafeUsageBeforeScan(t *testing.T) {
	base := durableSource("turn-invalid", "model-invalid", time.Now().UTC())
	mutations := []struct {
		name   string
		mutate func(*artifactapp.SourceProjection)
	}{
		{"root input utf8", func(p *artifactapp.SourceProjection) { p.Root.Input = json.RawMessage{'"', 0xff, '"'} }},
		{"turn input utf8", func(p *artifactapp.SourceProjection) { p.Turn.Input = json.RawMessage{'"', 0xff, '"'} }},
		{"turn output utf8", func(p *artifactapp.SourceProjection) { p.Output = json.RawMessage{'"', 0xff, '"'} }},
		{"model input utf8", func(p *artifactapp.SourceProjection) {
			p.Turn.ModelObservations[0].Input = json.RawMessage{'"', 0xff, '"'}
		}},
		{"model output utf8", func(p *artifactapp.SourceProjection) {
			p.Turn.ModelObservations[0].Output = json.RawMessage{'"', 0xff, '"'}
		}},
		{"input above safe", func(p *artifactapp.SourceProjection) { p.Turn.ModelObservations[0].InputTokens = 1 << 53 }},
		{"output above safe", func(p *artifactapp.SourceProjection) { p.Turn.ModelObservations[0].OutputTokens = 1 << 53 }},
		{"sum above safe", func(p *artifactapp.SourceProjection) {
			p.Turn.ModelObservations[0].InputTokens = (1 << 53) - 1
			p.Turn.ModelObservations[0].OutputTokens = 1
		}},
		{"addition overflow", func(p *artifactapp.SourceProjection) {
			p.Turn.ModelObservations[0].InputTokens = int64(^uint64(0) >> 1)
			p.Turn.ModelObservations[0].OutputTokens = 1
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Turn.ModelObservations = append([]workerapp.ModelObservation(nil), base.Turn.ModelObservations...)
			tc.mutate(&p)
			api := &durableRecordingAPI{}
			scanner := &recordingScanner{}
			projector, _ := NewDurableProjector(api, scanner)
			assertDurableError(t, projector.ExportAndVerify(context.Background(), p), "source_invalid", true)
			if scanner.calls != 0 || api.ingestCalls != 0 {
				t.Fatalf("scanner=%d HTTP=%d", scanner.calls, api.ingestCalls)
			}
		})
	}
	valid := base
	valid.Turn.ModelObservations = append([]workerapp.ModelObservation(nil), base.Turn.ModelObservations...)
	valid.Turn.ModelObservations[0].InputTokens = (1 << 53) - 2
	valid.Turn.ModelObservations[0].OutputTokens = 1
	if err := mustProjector(t).ExportAndVerify(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
}

func TestDurableProjectorScansOriginalSourceAndEncodedBatch(t *testing.T) {
	projection := durableSource("turn-scan", "model-scan", time.Now().UTC())
	scanner := &recordingScanner{}
	projector, _ := NewDurableProjector(newDurableMemoryAPI(), scanner)
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if scanner.calls != 2 || !reflect.DeepEqual(scanner.sources, []string{"langfuse.source_projection", "langfuse.ingestion.batch"}) {
		t.Fatalf("calls=%d sources=%v", scanner.calls, scanner.sources)
	}
	token := `token\\segment`
	projection.Turn.Input = json.RawMessage(`{"credential":"token\\segment"}`)
	escaping, err := secretcheck.New([]secretcheck.KnownToken{{Label: "configured-token", Value: token}})
	if err != nil {
		t.Fatal(err)
	}
	api := &durableRecordingAPI{}
	projector, _ = NewDurableProjector(api, escaping)
	assertDurableError(t, projector.ExportAndVerify(context.Background(), projection), "credential_detected", true)
	if api.ingestCalls != 0 {
		t.Fatalf("HTTP=%d", api.ingestCalls)
	}
}

func parseHTTPTime(t *testing.T, value any) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, fmt.Sprint(value))
	if err != nil {
		t.Fatalf("parse time %v: %v", value, err)
	}
	return parsed
}

func TestDurableProjectorPreservesLaterTurnsWhenOlderSourceRetries(t *testing.T) {
	t.Parallel()
	api := newDurableMemoryAPI()
	projector, _ := NewDurableProjector(api, acceptingScanner{})
	one := durableSource("turn-1", "model-1", time.Date(2026, 9, 6, 1, 2, 3, 4, time.UTC))
	two := durableSource("turn-2", "model-2", one.Turn.StartedAt.Add(time.Minute))
	for _, source := range []artifactapp.SourceProjection{one, two, one} {
		if err := projector.ExportAndVerify(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	if api.rootWrites != 3 || len(api.observations) != 4 {
		t.Fatalf("root writes=%d observations=%d", api.rootWrites, len(api.observations))
	}
	if _, ok := api.observations[durableObservationID(two, "turn")]; !ok {
		t.Fatal("later turn was overwritten")
	}
	wantRoot, _ := encodeJSONText(one.Root.Input)
	if !artifactapp.ProjectionJSONEqual(api.trace.Input, wantRoot) || len(api.trace.Output) != 0 {
		t.Fatalf("root mutated: %#v", api.trace)
	}
}

func TestDurableProjectorFailsClosedBeforeHTTP(t *testing.T) {
	t.Parallel()
	valid := durableSource("turn-1", "model-1", time.Now().UTC())
	tests := []struct {
		name   string
		mutate func(*artifactapp.SourceProjection)
		code   string
	}{
		{"schema", func(p *artifactapp.SourceProjection) { p.SchemaVersion = "other" }, "source_invalid"},
		{"root scope", func(p *artifactapp.SourceProjection) { p.Root.TenantID = "other" }, "source_invalid"},
		{"manifest", func(p *artifactapp.SourceProjection) { p.Source.ManifestChecksum = "secret input text" }, "source_invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			projection := valid
			tc.mutate(&projection)
			api := &durableRecordingAPI{}
			scanner := &recordingScanner{}
			projector, _ := NewDurableProjector(api, scanner)
			err := projector.ExportAndVerify(context.Background(), projection)
			assertDurableError(t, err, tc.code, true)
			if scanner.calls != 0 || api.ingestCalls != 0 {
				t.Fatalf("scanner=%d HTTP=%d", scanner.calls, api.ingestCalls)
			}
		})
	}
	api := &durableRecordingAPI{}
	scanner := &recordingScanner{err: &secretcheck.ExposureError{Source: "langfuse.ingestion.batch", TokenLabel: "configured-token"}}
	projector, _ := NewDurableProjector(api, scanner)
	err := projector.ExportAndVerify(context.Background(), valid)
	assertDurableError(t, err, "credential_detected", true)
	if api.ingestCalls != 0 || strings.Contains(err.Error(), "input") {
		t.Fatalf("unsafe error=%q HTTP=%d", err, api.ingestCalls)
	}
	unknownAPI := &durableRecordingAPI{}
	unknown, _ := NewDurableProjector(unknownAPI, &recordingScanner{err: errors.New("scanner backend detail")})
	assertDurableError(t, unknown.ExportAndVerify(context.Background(), valid), "scanner_unavailable", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, _ := NewDurableProjector(&durableRecordingAPI{}, &recordingScanner{err: ctx.Err()})
	assertDurableError(t, cancelled.ExportAndVerify(ctx, valid), "projection_cancelled", false)
	finalAPI := &durableRecordingAPI{}
	finalScanner := &recordingScanner{err: errors.New("scanner backend detail"), errAt: 2}
	final, _ := NewDurableProjector(finalAPI, finalScanner)
	assertDurableError(t, final.ExportAndVerify(context.Background(), valid), "scanner_unavailable", false)
	if finalScanner.calls != 2 || finalAPI.ingestCalls != 0 {
		t.Fatalf("final scanner=%d HTTP=%d", finalScanner.calls, finalAPI.ingestCalls)
	}
}

func TestDurableProjectorIsRaceSafeForConcurrentSources(t *testing.T) {
	t.Parallel()
	api := newDurableMemoryAPI()
	projector, _ := NewDurableProjector(api, acceptingScanner{})
	rootStartedAt := time.Date(2026, 9, 6, 0, 0, 0, 123456789, time.UTC)
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			source := durableSource("turn-"+string(rune('a'+index)), "model-1", time.Date(2026, 9, 6, 1, index, 0, index, time.UTC))
			source.Root.StartedAt = rootStartedAt
			if err := projector.ExportAndVerify(context.Background(), source); err != nil {
				t.Errorf("source %d: %v", index, err)
			}
		}(index)
	}
	wait.Wait()
}

func TestDurableProjectorRejectsReadbackCollisionsAndRetriesMissing(t *testing.T) {
	t.Parallel()
	projection := durableSource("turn-1", "model-1", time.Date(2026, 9, 6, 1, 2, 3, 456789123, time.UTC))
	base := newDurableMemoryAPI()
	projector, _ := NewDurableProjector(base, acceptingScanner{})
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	turnID := durableObservationID(projection, "turn")
	modelID := durableObservationID(projection, "model:model-1")
	base.observations["unrelated-newer"] = Observation{ID: "unrelated-newer", TraceID: base.trace.ID}
	if err := projector.ExportAndVerify(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		id     string
		mutate func(*Observation)
	}{
		{"scope", turnID, func(o *Observation) { o.TraceID = "other" }},
		{"io", turnID, func(o *Observation) { o.Input = json.RawMessage(`{"secret":"wrong"}`) }},
		{"model input", modelID, func(o *Observation) { o.Input = json.RawMessage(`{"encoding":"json-text.v1","json":"wrong"}`) }},
		{"model output", modelID, func(o *Observation) { o.Output = json.RawMessage(`{"encoding":"json-text.v1","json":"wrong"}`) }},
		{"time", turnID, func(o *Observation) { o.StartTime = o.StartTime.Add(time.Second) }},
		{"model", modelID, func(o *Observation) { o.Model = "wrong" }},
		{"token", modelID, func(o *Observation) { o.Usage.Input++ }},
		{"status", modelID, func(o *Observation) { o.StatusMessage = "failed" }},
		{"generation metadata", modelID, func(o *Observation) {
			metadata := make(map[string]any, len(o.Metadata))
			for key, value := range o.Metadata {
				metadata[key] = value
			}
			metadata["runtime_generation"] = "8"
			o.Metadata = metadata
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			clone := base.clone()
			clone.ignoreIngest = true
			observation := clone.observations[tc.id]
			tc.mutate(&observation)
			clone.observations[tc.id] = observation
			p, _ := NewDurableProjector(clone, acceptingScanner{})
			err := p.ExportAndVerify(context.Background(), projection)
			assertDurableError(t, err, "projection_collision", true)
		})
	}
	rootMutations := []struct {
		name   string
		mutate func(*Trace)
	}{
		{"name", func(trace *Trace) { trace.Name = "wrong" }},
		{"transport timestamp", func(trace *Trace) { trace.Timestamp = trace.Timestamp.Add(time.Millisecond) }},
		{"source nanosecond timestamp", func(trace *Trace) {
			trace.Metadata.SourceStartedAt = trace.Metadata.SourceStartedAt[:len(trace.Metadata.SourceStartedAt)-1] + "0"
		}},
		{"envelope marker", func(trace *Trace) {
			trace.Input = json.RawMessage(`{"encoding":"json-text.v0","json":"{\\"root\\":\\"original\\"}"}`)
		}},
		{"envelope content", func(trace *Trace) {
			trace.Input = json.RawMessage(`{"encoding":"json-text.v1","json":"{\\"root\\":\\"changed\\"}"}`)
		}},
	}
	for _, tc := range rootMutations {
		t.Run("root "+tc.name, func(t *testing.T) {
			clone := base.clone()
			clone.ignoreIngest = true
			tc.mutate(&clone.trace)
			p, _ := NewDurableProjector(clone, acceptingScanner{})
			assertDurableError(t, p.ExportAndVerify(context.Background(), projection), "projection_collision", true)
		})
	}
	missing := base.clone()
	missing.ignoreIngest = true
	delete(missing.observations, turnID)
	p, _ := NewDurableProjector(missing, acceptingScanner{})
	err := p.ExportAndVerify(context.Background(), projection)
	assertDurableError(t, err, "projection_not_ready", false)
}

func durableSource(turnID, modelID string, started time.Time) artifactapp.SourceProjection {
	identity := artifactdomain.TurnIdentity{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", TaskID: "task", WorkerSessionID: "session", WorkerRunID: "run", AgentSessionID: "agent", AgentTurnID: contractsv1.AgentTurnID(turnID), RuntimeInstanceID: "runtime", RuntimeGeneration: 7, TraceStreamID: "stream-" + turnID, LangfuseTraceID: "legacy", RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{ID: contractsv1.RuntimeNetworkPolicyLegacyRestrictedV1, EnforcementStatus: "legacy_unenforced"}}
	input := json.RawMessage(`{"id":9007199254740993,"messages":[{"role":"user","content":"full input"}]}`)
	return artifactapp.SourceProjection{SchemaVersion: artifactapp.DurableProjectionSchemaVersion, Source: artifactapp.ProjectionSourceRef{TraceStreamID: identity.TraceStreamID, ManifestChecksum: "sha256:" + strings.Repeat("a", 64)}, Root: artifactapp.ProjectionRunRoot{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", StartedAt: started.Add(-time.Hour), Input: json.RawMessage(`{"root":"original"}`)}, Turn: artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: identity, StartedAt: started, EndedAt: started.Add(2 * time.Second), Input: input, ModelObservations: []workerapp.ModelObservation{durableModel(identity, modelID, started.Add(time.Millisecond))}}, Output: json.RawMessage(`"full report output"`)}
}
func durableModel(identity artifactdomain.TurnIdentity, id string, started time.Time) workerapp.ModelObservation {
	return workerapp.ModelObservation{ObservationID: id, WorkerSessionID: string(identity.WorkerSessionID), WorkerRunID: string(identity.WorkerRunID), AgentTurnID: string(identity.AgentTurnID), RuntimeID: string(identity.RuntimeInstanceID), Generation: uint64(identity.RuntimeGeneration), Model: "gpt-test", Route: "primary", Input: json.RawMessage(`{"prompt":"full model input"}`), Output: json.RawMessage(`{"answer":"full model output"}`), InputTokens: 11, OutputTokens: 13, StartedAt: started, EndedAt: started.Add(time.Second), Status: "ok"}
}
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func assertDurableError(t *testing.T, err error, code string, permanent bool) {
	t.Helper()
	var typed *artifactapp.ProjectionError
	if !errors.As(err, &typed) || typed.Code != code || typed.Permanent != permanent {
		t.Fatalf("error=%#v want=%s permanent=%t", err, code, permanent)
	}
}

type acceptingScanner struct{}

func (acceptingScanner) ScanJSON(context.Context, string, any) error { return nil }

type recordingScanner struct {
	calls   int
	err     error
	errAt   int
	sources []string
}

func (s *recordingScanner) ScanJSON(_ context.Context, source string, _ any) error {
	s.calls++
	s.sources = append(s.sources, source)
	if s.errAt > 0 && s.calls != s.errAt {
		return nil
	}
	return s.err
}

func mustProjector(t *testing.T) *DurableProjector {
	t.Helper()
	p, err := NewDurableProjector(newDurableMemoryAPI(), acceptingScanner{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type durableRecordingAPI struct {
	mu           sync.Mutex
	ingestCalls  int
	batches      []IngestionBatch
	trace        Trace
	observations map[string]Observation
}

func (a *durableRecordingAPI) Ingest(_ context.Context, b IngestionBatch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ingestCalls++
	a.batches = append(a.batches, b)
	if a.observations == nil {
		mem := newDurableMemoryAPI()
		mem.ingestLocked(b)
		a.trace = mem.trace
		a.observations = mem.observations
	}
	return nil
}
func (a *durableRecordingAPI) GetTraceRoot(context.Context, string) (Trace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trace, nil
}
func (a *durableRecordingAPI) GetObservation(_ context.Context, id string) (Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, ok := a.observations[id]
	if !ok {
		return Observation{}, &HTTPError{Operation: "observation query", StatusCode: 404}
	}
	return o, nil
}
func (a *durableRecordingAPI) lastBatch() IngestionBatch {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.batches[len(a.batches)-1]
}

type durableMemoryAPI struct {
	mu           sync.Mutex
	rootWrites   int
	ignoreIngest bool
	trace        Trace
	observations map[string]Observation
}

func newDurableMemoryAPI() *durableMemoryAPI {
	return &durableMemoryAPI{observations: map[string]Observation{}}
}
func (a *durableMemoryAPI) Ingest(_ context.Context, b IngestionBatch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ignoreIngest {
		return nil
	}
	a.ingestLocked(b)
	return nil
}
func (a *durableMemoryAPI) ingestLocked(b IngestionBatch) {
	for _, event := range b.Batch {
		body := event.Body
		switch event.Type {
		case "trace-create":
			a.rootWrites++
			a.trace = Trace{ID: body["id"].(string), Name: body["name"].(string), Timestamp: body["timestamp"].(time.Time), Input: raw(body["input"]), Metadata: decodeMetadata(body["metadata"])}
		case "span-create", "generation-create":
			o := decodeObservation(body, event.Type)
			a.observations[o.ID] = o
		}
	}
}
func (a *durableMemoryAPI) GetTraceRoot(context.Context, string) (Trace, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trace, nil
}
func (a *durableMemoryAPI) GetObservation(_ context.Context, id string) (Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o, ok := a.observations[id]
	if !ok {
		return Observation{}, &HTTPError{Operation: "observation query", StatusCode: 404}
	}
	return o, nil
}
func (a *durableMemoryAPI) clone() *durableMemoryAPI {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := newDurableMemoryAPI()
	result.trace = a.trace
	result.rootWrites = a.rootWrites
	for k, v := range a.observations {
		result.observations[k] = v
	}
	return result
}
func decodeObservation(b map[string]any, eventType string) Observation {
	o := Observation{ID: b["id"].(string), TraceID: b["traceId"].(string), Name: b["name"].(string), Input: raw(b["input"]), Output: raw(b["output"]), Metadata: decodePinnedMetadata(b["metadata"]), StartTime: parseBodyTime(b["startTime"]), EndTime: parseBodyTime(b["endTime"]), Level: b["level"].(string)}
	if eventType == "span-create" {
		o.Type = "SPAN"
	} else {
		o.Type = "GENERATION"
		parent := b["parentObservationId"].(string)
		o.ParentObservationID = &parent
		o.Model = b["model"].(string)
		o.StatusMessage = b["statusMessage"].(string)
		switch usage := b["usageDetails"].(type) {
		case map[string]int64:
			o.Usage = Usage{Input: usage["input"], Output: usage["output"], Total: usage["total"], Unit: "TOKENS"}
		case map[string]any:
			o.Usage = Usage{Input: bodyInt64(usage["input"]), Output: bodyInt64(usage["output"]), Total: bodyInt64(usage["total"]), Unit: "TOKENS"}
		}
	}
	return o
}
func bodyInt64(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		result, _ := typed.Int64()
		return result
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
func parseBodyTime(value any) time.Time {
	if typed, ok := value.(time.Time); ok {
		return typed
	}
	parsed, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(value))
	return parsed
}
func raw(v any) json.RawMessage { data, _ := json.Marshal(v); return data }
func decodeMetadata(v any) RuntimeMetadata {
	data, _ := json.Marshal(decodePinnedMetadata(v))
	var m RuntimeMetadata
	_ = json.Unmarshal(data, &m)
	return m
}
func decodePinnedMetadata(v any) map[string]any {
	transported := v.(map[string]any)
	decoded := make(map[string]any, len(transported))
	for key, value := range transported {
		encoded, ok := value.(string)
		if !ok {
			decoded[key] = value
			continue
		}
		var restored any
		if err := json.Unmarshal([]byte(encoded), &restored); err != nil {
			decoded[key] = value
			continue
		}
		decoded[key] = restored
	}
	return decoded
}
func mustMetadataTransport(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
func durableObservationID(p artifactapp.SourceProjection, role string) string {
	projector, _ := NewDurableProjector(newDurableMemoryAPI(), acceptingScanner{})
	batch, _, _ := projector.buildBatch(p)
	for _, event := range batch.Batch {
		if role == "turn" && event.Type == "span-create" {
			return event.Body["id"].(string)
		}
		if strings.HasPrefix(role, "model:") && event.Type == "generation-create" && decodePinnedMetadata(event.Body["metadata"])["source_observation_id"] == strings.TrimPrefix(role, "model:") {
			return event.Body["id"].(string)
		}
	}
	return ""
}
