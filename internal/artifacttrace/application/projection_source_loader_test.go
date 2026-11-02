package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestProjectionSourceLoaderReconstructsImmutableSourceAndReport(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	identity := artifactdomain.TurnIdentity{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", TaskID: "task", WorkerSessionID: "session", WorkerRunID: "run", AgentSessionID: "agent", AgentTurnID: "turn", RuntimeInstanceID: "runtime", RuntimeGeneration: 1, TraceStreamID: "trace", LangfuseTraceID: "langfuse", RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{ID: contractsv1.RuntimeNetworkPolicyLegacyRestrictedV1, EnforcementStatus: "legacy_unenforced"}}
	input := json.RawMessage(`{"messages":[{"role":"user","content":"initial"}],"id":9007199254740992}`)
	source := artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: identity, StartedAt: now, EndedAt: now.Add(time.Second), Input: input, ModelObservations: []workerapp.ModelObservation{{ObservationID: "model-1", WorkerSessionID: "session", WorkerRunID: "run", AgentTurnID: "turn", RuntimeID: "runtime", Generation: 1, Model: "model", Route: "route", Input: input, Output: json.RawMessage(`{"text":"answer"}`), Status: "ok", StartedAt: now, EndedAt: now.Add(time.Second)}}}
	encoded, err := artifactapp.EncodeTurnObservationSource(source)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"type":"acp.raw_frame","direction":"agent_to_client","frame":"{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"agent\",\"update\":{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"Done.\"}}}}\n"}` + "\n")
	finalized, err := (artifactapp.TurnEvidenceFinalizer{Store: objects, IDs: &loaderIDs{}, Now: func() time.Time { return now }, Policy: artifactapp.FinalizationPolicy{ArtifactPermission: contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "policy"}, ArtifactRetentionPolicyRef: "retention", TraceLifecyclePolicyRef: "lifecycle"}}).Execute(ctx, artifactapp.FinalizeTurnEvidenceCommand{Identity: identity, ServerRecordedAt: now, WorkerRecordedAt: now, RawACPJSONL: raw, ObservationSourceJSONL: encoded, NoFilesChanged: true, SourceCheckpointRef: contractsv1.CheckpointRef{CheckpointID: "checkpoint", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}})
	if err != nil {
		t.Fatal(err)
	}
	var report contractsv1.ArtifactRef
	for _, ref := range finalized.ArtifactRefs {
		if ref.Type == "report" {
			report = ref
		}
	}
	metadata := artifactapp.ProjectionSourceMetadata{Root: artifactapp.ProjectionRunRoot{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", StartedAt: now, Input: input}, RootManifest: finalized.Manifest, Manifest: finalized.Manifest, ManifestObject: finalized.ManifestObject, Report: report}
	const objectSecret = "/private/tenant/object/path"
	_, err = (artifactapp.ProjectionSourceLoader{Metadata: fixedProjectionMetadata{value: metadata}, Objects: failingOpenObjectStore{err: errors.New(objectSecret)}}).Load(ctx, artifactapp.ProjectionJob{})
	assertProjectionError(t, err, "manifest_object_unavailable", false, objectSecret)
	_, err = (artifactapp.ProjectionSourceLoader{Metadata: fixedProjectionMetadata{value: metadata}, Objects: failingOpenObjectStore{err: fmt.Errorf("lstat %s: %w", objectSecret, fs.ErrNotExist)}}).Load(ctx, artifactapp.ProjectionJob{})
	assertProjectionError(t, err, "manifest_object_missing", true, objectSecret)
	projection, err := (artifactapp.ProjectionSourceLoader{Metadata: fixedProjectionMetadata{value: metadata}, Objects: objects}).Load(ctx, artifactapp.ProjectionJob{SchemaVersion: artifactapp.DurableProjectionSchemaVersion, Source: artifactapp.ProjectionSourceRef{TraceStreamID: "trace", ManifestChecksum: finalized.ManifestObject.Checksum}})
	if err != nil {
		t.Fatal(err)
	}
	var reportText string
	if err := json.Unmarshal(projection.Output, &reportText); err != nil {
		t.Fatal(err)
	}
	if projection.Root.WorkerRunID != "run" || projection.Turn.Identity.AgentTurnID != "turn" || reportText == "" {
		t.Fatalf("projection=%#v", projection)
	}
	metadata.Root.Input = json.RawMessage(`{"messages":[{"role":"user","content":"initial"}],"id":9007199254740993}`)
	_, err = (artifactapp.ProjectionSourceLoader{Metadata: fixedProjectionMetadata{value: metadata}, Objects: objects}).Load(ctx, artifactapp.ProjectionJob{})
	assertProjectionError(t, err, "root_input_mismatch", true, "")
	metadata.Root.Input = input
	temporary, err := objects.PutTemporary(ctx, workerapp.ObjectIdentity{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", AgentTurnID: "turn", Kind: "invalid-report"}, strings.NewReader(string([]byte{0xff})))
	if err != nil {
		t.Fatal(err)
	}
	bad, err := objects.Finalize(ctx, workerapp.ObjectIdentity{TenantID: "tenant", UserID: "user", WorkspaceID: "workspace", WorkerSessionID: "session", WorkerRunID: "run", AgentTurnID: "turn", Kind: "invalid-report"}, temporary)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Report.URI = "object://" + bad.Key
	metadata.Report.SizeBytes = bad.Size
	metadata.Report.SHA256 = strings.TrimPrefix(bad.Checksum, "sha256:")
	_, err = (artifactapp.ProjectionSourceLoader{Metadata: fixedProjectionMetadata{value: metadata}, Objects: objects}).Load(ctx, artifactapp.ProjectionJob{})
	var permanent *artifactapp.ProjectionError
	if !errors.As(err, &permanent) || permanent.Code != "report_encoding_invalid" || !permanent.Permanent {
		t.Fatalf("invalid UTF-8 error=%v", err)
	}
}

func TestProjectionSourceLoaderReturnsSafePermanentError(t *testing.T) {
	_, err := (artifactapp.ProjectionSourceLoader{Metadata: fixedProjectionMetadata{err: errors.New("database detail")}}).Load(context.Background(), artifactapp.ProjectionJob{})
	var projectionErr *artifactapp.ProjectionError
	if !errors.As(err, &projectionErr) || !projectionErr.Permanent || projectionErr.Code != "source_dependencies_invalid" {
		t.Fatalf("error=%#v", err)
	}
}

func TestProjectionSourceLoaderKeepsMetadataFailuresRetryableAndSafe(t *testing.T) {
	const secret = "postgresql://user:password@private-host/database"
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (artifactapp.ProjectionSourceLoader{
		Metadata: fixedProjectionMetadata{err: errors.New(secret)},
		Objects:  objects,
	}).Load(context.Background(), artifactapp.ProjectionJob{})
	assertProjectionError(t, err, "source_metadata_unavailable", false, secret)
}

func TestProjectionSourceLoaderKeepsContextCancellationRetryableAndSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	objects, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = (artifactapp.ProjectionSourceLoader{
		Metadata: fixedProjectionMetadata{err: ctx.Err()},
		Objects:  objects,
	}).Load(ctx, artifactapp.ProjectionJob{})
	assertProjectionError(t, err, "source_load_cancelled", false, "")
}

func TestProjectionSourceLoaderComparesJSONNumbersLosslessly(t *testing.T) {
	if artifactapp.ProjectionJSONEqual(json.RawMessage(`{"id":9007199254740992}`), json.RawMessage(`{"id":9007199254740993}`)) {
		t.Fatal("distinct integers beyond float64 precision compared equal")
	}
	if !artifactapp.ProjectionJSONEqual(json.RawMessage(`{"b":[1,2],"a":1}`), json.RawMessage(`{"a":1.0,"b":[1.0,2e0]}`)) {
		t.Fatal("semantic numbers or object key ordering compared unequal")
	}
	if artifactapp.ProjectionJSONEqual(json.RawMessage(`[1,2]`), json.RawMessage(`[2,1]`)) {
		t.Fatal("array ordering was ignored")
	}
	if artifactapp.ProjectionJSONEqual(json.RawMessage(`1e999999999`), json.RawMessage(`10e999999998`)) {
		t.Fatal("unbounded exponent must be rejected conservatively")
	}
}

func assertProjectionError(t *testing.T, err error, code string, permanent bool, forbidden string) {
	t.Helper()
	var projectionErr *artifactapp.ProjectionError
	if !errors.As(err, &projectionErr) || projectionErr.Code != code || projectionErr.Permanent != permanent {
		t.Fatalf("error=%#v, want code=%q permanent=%v", err, code, permanent)
	}
	if forbidden != "" && strings.Contains(err.Error(), forbidden) {
		t.Fatalf("error echoed unsafe detail: %q", err)
	}
}

type failingOpenObjectStore struct {
	workerapp.ObjectStore
	err error
}

func (store failingOpenObjectStore) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, store.err
}

type fixedProjectionMetadata struct {
	value artifactapp.ProjectionSourceMetadata
	err   error
}

func (store fixedProjectionMetadata) LoadProjectionSourceMetadata(context.Context, artifactapp.ProjectionSourceRef) (artifactapp.ProjectionSourceMetadata, error) {
	return store.value, store.err
}

type loaderIDs struct{ next int }

func (ids *loaderIDs) NewID(kind string) (string, error) { ids.next++; return kind + "-id", nil }
