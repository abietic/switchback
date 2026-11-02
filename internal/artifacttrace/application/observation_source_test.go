package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestTurnObservationSourceRoundTripsDeterministically(t *testing.T) {
	source := validObservationSource(t)
	before, err := artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(source.ModelObservations)
	after, err := artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source encoding changed with model order")
	}
	decoded, err := artifactapp.DecodeTurnObservationSource(before)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.StartedAt.Equal(source.StartedAt) {
		t.Fatal("original start time lost")
	}
	if !decoded.EndedAt.Equal(source.EndedAt) {
		t.Fatal("original end time lost")
	}
	if string(decoded.Input) != string(source.Input) || len(decoded.ModelObservations) != 2 ||
		string(decoded.ModelObservations[0].Input) != `{"prompt":{"nested":[1,2,3]}}` ||
		string(decoded.ModelObservations[0].Output) != `{"answer":{"text":"complete"}}` {
		t.Fatalf("complete nested I/O not preserved: %#v", decoded)
	}
}

func TestTurnObservationSourceRejectsInvalidModelsAndIncompleteNormalSource(t *testing.T) {
	for name, mutate := range map[string]func(*artifactapp.TurnObservationSource){
		"duplicate observation": func(s *artifactapp.TurnObservationSource) {
			s.ModelObservations[1].ObservationID = s.ModelObservations[0].ObservationID
		},
		"mismatched run": func(s *artifactapp.TurnObservationSource) { s.ModelObservations[0].WorkerRunID = "run-other" },
		"null input":     func(s *artifactapp.TurnObservationSource) { s.Input = json.RawMessage(`null`) },
		"zero models":    func(s *artifactapp.TurnObservationSource) { s.ModelObservations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			source := validObservationSource(t)
			mutate(&source)
			if _, err := artifactapp.EncodeTurnObservationSource(source); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}

func TestTurnObservationSourceAcceptsSafeIncompleteMarkers(t *testing.T) {
	for _, code := range []string{"source_incomplete", "credential_detected"} {
		source := validObservationSource(t)
		source.ErrorCode, source.Input, source.ModelObservations = code, nil, nil
		if _, err := artifactapp.EncodeTurnObservationSource(source); err != nil {
			t.Fatalf("marker %q: %v", code, err)
		}
	}
}

func TestTurnObservationSourceRejectsPayloadOnErrorMarkers(t *testing.T) {
	for _, code := range []string{"source_incomplete", "credential_detected"} {
		for _, payload := range []struct {
			name   string
			mutate func(*artifactapp.TurnObservationSource)
		}{
			{name: "input", mutate: func(source *artifactapp.TurnObservationSource) {
				source.Input = json.RawMessage(`{"secret":"synthetic-marker-secret"}`)
			}},
			{name: "models", mutate: func(source *artifactapp.TurnObservationSource) {
				source.ModelObservations = validObservationSource(t).ModelObservations[:1]
			}},
		} {
			t.Run(code+"/"+payload.name, func(t *testing.T) {
				source := validObservationSource(t)
				source.ErrorCode, source.Input, source.ModelObservations = code, nil, nil
				payload.mutate(&source)
				if _, err := artifactapp.EncodeTurnObservationSource(source); err == nil {
					t.Fatal("error marker payload accepted by encoder")
				}

				encoded, err := json.Marshal(source)
				if err != nil {
					t.Fatal(err)
				}
				encoded = append(encoded, '\n')
				if _, err := artifactapp.DecodeTurnObservationSource(encoded); err == nil {
					t.Fatal("error marker payload accepted by decoder")
				}
			})
		}
	}
}

func TestEncodeTurnObservationSourceRejectsMalformedModelInput(t *testing.T) {
	source := validObservationSource(t)
	source.ModelObservations[0].Input = json.RawMessage(`{"unterminated":`)
	if _, err := artifactapp.EncodeTurnObservationSource(source); err == nil {
		t.Fatal("malformed model input accepted")
	}
}

func TestTurnObservationSourceErrorsDoNotEchoPayload(t *testing.T) {
	const secret = "synthetic-source-secret-7f3c"

	source := validObservationSource(t)
	source.SchemaVersion = secret
	_, err := artifactapp.EncodeTurnObservationSource(source)
	assertErrorDoesNotContain(t, err, secret)

	data, err := artifactapp.EncodeTurnObservationSource(validObservationSource(t))
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"schema_version"`), []byte(`"`+secret+`"`), 1)
	_, err = artifactapp.DecodeTurnObservationSource(data)
	assertErrorDoesNotContain(t, err, secret)
}

func assertErrorDoesNotContain(t *testing.T, err error, forbidden string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), forbidden) {
		t.Fatalf("error echoed unsafe payload: %q", err)
	}
}

func TestReadTurnObservationSourceVerifiesSealedObjectAndManifestIdentity(t *testing.T) {
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalizer := validFinalizer(store)
	command := validFinalizeCommand(t)
	source := validObservationSource(t)
	command.ObservationSourceJSONL, err = artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := finalizer.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.Segments) != 3 {
		t.Fatalf("Trace segments count = %d, want 3", len(result.Manifest.Segments))
	}
	sourceSegment := result.Manifest.Segments[2]
	if sourceSegment.Kind != artifactapp.ObservationSourceSegmentKind || sourceSegment.RecordCount != 1 ||
		sourceSegment.FirstSequence != 1 || sourceSegment.LastSequence != 1 ||
		sourceSegment.SizeBytes != int64(len(command.ObservationSourceJSONL)) {
		t.Fatalf("observation source segment = %#v", sourceSegment)
	}
	loaded, err := artifactapp.ReadTurnObservationSource(context.Background(), store, result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.StartedAt.Equal(source.StartedAt) || string(loaded.Input) != string(source.Input) || len(loaded.ModelObservations) != 2 {
		t.Fatalf("reloaded source lost evidence: %#v", loaded)
	}

	tampered := result.Manifest
	for index := range tampered.Segments {
		if tampered.Segments[index].Kind == artifactapp.ObservationSourceSegmentKind {
			tampered.Segments[index].SHA256 = strings.Repeat("0", 64)
		}
	}
	if _, err := artifactapp.ReadTurnObservationSource(context.Background(), store, tampered); err == nil {
		t.Fatal("tampered checksum accepted")
	}
	mismatched := result.Manifest
	mismatched.WorkerRunID = "run-other"
	if _, err := artifactapp.ReadTurnObservationSource(context.Background(), store, mismatched); err == nil {
		t.Fatal("manifest identity mismatch accepted")
	}
	missing := result.Manifest
	missing.Segments = missing.Segments[:2]
	if _, err := artifactapp.ReadTurnObservationSource(context.Background(), store, missing); err == nil {
		t.Fatal("missing source segment accepted")
	}
	duplicate := result.Manifest
	duplicate.Segments = append(duplicate.Segments, duplicate.Segments[2])
	if _, err := artifactapp.ReadTurnObservationSource(context.Background(), store, duplicate); err == nil {
		t.Fatal("duplicate source segment accepted")
	}
}

func TestReadTurnObservationSourceRejectsTamperedObjectBytes(t *testing.T) {
	store := &recordingObjectStore{}
	command := validFinalizeCommand(t)
	var err error
	command.ObservationSourceJSONL, err = artifactapp.EncodeTurnObservationSource(validObservationSource(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := validFinalizer(store).Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[artifactapp.ObservationSourceSegmentKind] = append([]byte(nil), store.objects[artifactapp.ObservationSourceSegmentKind]...)
	store.objects[artifactapp.ObservationSourceSegmentKind][0] ^= 1
	if _, err := artifactapp.ReadTurnObservationSource(context.Background(), store, result.Manifest); err == nil || err.Error() != "source_checksum_mismatch" {
		t.Fatalf("tampered source error = %v", err)
	}
}

func TestReadTurnObservationSourceClassifiesOpenFailureAsRetryableAndSafe(t *testing.T) {
	const secret = "/private/source/object"
	manifest := contractsv1.TraceManifest{Segments: []contractsv1.TraceSegment{{Kind: artifactapp.ObservationSourceSegmentKind, URI: "object://source", SHA256: strings.Repeat("0", 64), SizeBytes: 1, RecordCount: 1, FirstSequence: 1, LastSequence: 1}}}
	_, err := artifactapp.ReadTurnObservationSource(context.Background(), openFailureStore{err: errors.New(secret)}, manifest)
	var projectionErr *artifactapp.ProjectionError
	if !errors.As(err, &projectionErr) || projectionErr.Permanent || projectionErr.Code != "source_object_unavailable" {
		t.Fatalf("error=%#v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed unsafe object detail: %q", err)
	}
}

func TestReadTurnObservationSourceClassifiesMissingObjectAsPermanentAndSafe(t *testing.T) {
	const secret = "/private/source/missing-object"
	manifest := contractsv1.TraceManifest{Segments: []contractsv1.TraceSegment{{Kind: artifactapp.ObservationSourceSegmentKind, URI: "object://source", SHA256: strings.Repeat("0", 64), SizeBytes: 1, RecordCount: 1, FirstSequence: 1, LastSequence: 1}}}
	_, err := artifactapp.ReadTurnObservationSource(context.Background(), openFailureStore{err: fmt.Errorf("lstat %s: %w", secret, fs.ErrNotExist)}, manifest)
	var projectionErr *artifactapp.ProjectionError
	if !errors.As(err, &projectionErr) || !projectionErr.Permanent || projectionErr.Code != "source_object_missing" {
		t.Fatalf("error=%#v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed missing object path: %q", err)
	}
}

func TestReadTurnObservationSourceClassifiesReadCancellationAsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manifest := contractsv1.TraceManifest{Segments: []contractsv1.TraceSegment{{Kind: artifactapp.ObservationSourceSegmentKind, URI: "object://source", SHA256: strings.Repeat("0", 64), SizeBytes: 1, RecordCount: 1, FirstSequence: 1, LastSequence: 1}}}
	_, err := artifactapp.ReadTurnObservationSource(ctx, openFailureStore{reader: errorReader{err: ctx.Err()}}, manifest)
	var projectionErr *artifactapp.ProjectionError
	if !errors.As(err, &projectionErr) || projectionErr.Permanent || projectionErr.Code != "source_read_cancelled" {
		t.Fatalf("error=%#v", err)
	}
}

type openFailureStore struct {
	workerapp.ObjectStore
	reader io.Reader
	err    error
}

func (store openFailureStore) Open(context.Context, string) (io.ReadCloser, error) {
	if store.err != nil {
		return nil, store.err
	}
	return io.NopCloser(store.reader), nil
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

func TestFinalizerRejectsObservationIdentityBeforeWrites(t *testing.T) {
	store := &recordingObjectStore{}
	command := validFinalizeCommand(t)
	source := validObservationSource(t)
	var err error
	command.ObservationSourceJSONL, err = artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	command.Identity.WorkerRunID = "run-other"
	if _, err := validFinalizer(store).Execute(context.Background(), command); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("error = %v", err)
	}
	if store.putCalls != 0 {
		t.Fatalf("identity mismatch caused %d writes", store.putCalls)
	}
}

func validObservationSource(t *testing.T) artifactapp.TurnObservationSource {
	t.Helper()
	identity := validFinalizeCommand(t).Identity
	started := time.Date(2026, 7, 23, 9, 28, 58, 123456789, time.FixedZone("source", 8*60*60))
	models := []workerapp.ModelObservation{
		{ObservationID: "model-2", WorkerSessionID: string(identity.WorkerSessionID), WorkerRunID: string(identity.WorkerRunID), AgentTurnID: string(identity.AgentTurnID), RuntimeID: string(identity.RuntimeInstanceID), Generation: uint64(identity.RuntimeGeneration), Model: "model-b", Route: "route-b", Input: json.RawMessage(`{"prompt":"second"}`), Output: json.RawMessage(`{"answer":"second"}`), InputTokens: 2, OutputTokens: 3, StartedAt: started.Add(time.Second), EndedAt: started.Add(2 * time.Second), Status: "ok"},
		{ObservationID: "model-1", WorkerSessionID: string(identity.WorkerSessionID), WorkerRunID: string(identity.WorkerRunID), AgentTurnID: string(identity.AgentTurnID), RuntimeID: string(identity.RuntimeInstanceID), Generation: uint64(identity.RuntimeGeneration), Model: "model-a", Route: "route-a", Input: json.RawMessage(`{"prompt":{"nested":[1,2,3]}}`), Output: json.RawMessage(`{"answer":{"text":"complete"}}`), InputTokens: 4, OutputTokens: 5, StartedAt: started, EndedAt: started.Add(time.Second), Status: "ok"},
	}
	return artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: identity, StartedAt: started, EndedAt: started.Add(3 * time.Second), Input: json.RawMessage(`{"task":{"goal":"preserve all I/O"}}`), ModelObservations: models}
}
