package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestTurnEvidenceFinalizerPersistsVerifiedObjectsAndExactProvenance(t *testing.T) {
	t.Parallel()
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 23, 9, 30, 0, 0, time.UTC)
	finalizer := artifactapp.TurnEvidenceFinalizer{
		Store: store,
		IDs:   &sequenceIDs{values: []string{"artifact-report-1", "artifact-patch-1"}},
		Now:   func() time.Time { return createdAt },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission: contractsv1.ArtifactPermission{
				Visibility: "private", PolicyRevision: "single-user-local-v1",
			},
			ArtifactRetentionPolicyRef: "artifact-default-v1",
			TraceLifecyclePolicyRef:    "trace-default-v1",
		},
	}
	command := validFinalizeCommand(t)

	result, err := finalizer.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.ArtifactRefs) != 2 {
		t.Fatalf("ArtifactRefs count = %d, want 2", len(result.ArtifactRefs))
	}
	if len(result.Manifest.Segments) != 2 {
		t.Fatalf("Trace segments count = %d, want 2", len(result.Manifest.Segments))
	}
	if result.Manifest.WorkerSessionID != command.Identity.WorkerSessionID ||
		result.Manifest.WorkerRunID != command.Identity.WorkerRunID ||
		result.Manifest.AgentTurnID != command.Identity.AgentTurnID ||
		result.Manifest.RuntimeInstanceID != command.Identity.RuntimeInstanceID ||
		result.Manifest.RuntimeGeneration != command.Identity.RuntimeGeneration {
		t.Fatalf("manifest provenance = %#v, want exact command identity", result.Manifest)
	}
	if result.Manifest.FirstRecordedAt != command.ServerRecordedAt ||
		result.Manifest.LastRecordedAt != command.ServerRecordedAt {
		t.Fatalf("manifest timestamps = %s..%s", result.Manifest.FirstRecordedAt, result.Manifest.LastRecordedAt)
	}
	for _, segment := range result.Manifest.Segments {
		if segment.FirstSequence != 1 || segment.LastSequence != 2 || segment.RecordCount != 2 {
			t.Fatalf("segment %#v does not describe both Trace records", segment)
		}
		if !strings.HasPrefix(segment.URI, "object://objects/tenants/tenant-local/users/user-local/workspaces/workspace-demo/") {
			t.Fatalf("segment URI = %q, want scoped object URI", segment.URI)
		}
	}
	for _, artifact := range result.ArtifactRefs {
		if err := artifact.Validate(); err != nil {
			t.Fatalf("ArtifactRef %q invalid: %v", artifact.Type, err)
		}
		if artifact.WorkerSessionID != command.Identity.WorkerSessionID ||
			artifact.WorkerRunID != command.Identity.WorkerRunID ||
			artifact.AgentTurnID != command.Identity.AgentTurnID ||
			artifact.RuntimeInstanceID != command.Identity.RuntimeInstanceID ||
			artifact.RuntimeGeneration != command.Identity.RuntimeGeneration {
			t.Fatalf("ArtifactRef provenance = %#v, want exact command identity", artifact)
		}
		if artifact.CreatedAt != createdAt || artifact.SourceCheckpointRef != command.SourceCheckpointRef {
			t.Fatalf("ArtifactRef metadata = %#v", artifact)
		}
	}
	if err := result.Manifest.Validate(); err != nil {
		t.Fatalf("TraceManifest invalid: %v", err)
	}
	manifestReader, err := store.Open(context.Background(), result.ManifestObject.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer manifestReader.Close()
	manifestBytes, err := io.ReadAll(manifestReader)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := contractsv1.DecodeTraceManifest(manifestBytes)
	if err != nil {
		t.Fatalf("stored manifest is not a production contract: %v", err)
	}
	if decoded.TraceStreamID != command.Identity.TraceStreamID {
		t.Fatalf("stored trace stream = %q", decoded.TraceStreamID)
	}

	for _, object := range result.Objects {
		reader, err := store.Open(context.Background(), object.Stored.Key)
		if err != nil {
			t.Fatal(err)
		}
		payload, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		switch object.Kind {
		case artifactdomain.KindRawACP, artifactdomain.KindNormalized:
			assertTraceRecordProvenance(t, payload, command.Identity)
		case artifactdomain.KindReport:
			text := string(payload)
			if !strings.Contains(text, "Implemented the change.") ||
				!strings.Contains(text, "## Verification Appendix") ||
				!strings.Contains(text, "`go test ./...`") {
				t.Fatalf("report content = %q", text)
			}
		case artifactdomain.KindPatch:
			if string(payload) != string(command.Patch) {
				t.Fatalf("patch content = %q, want %q", payload, command.Patch)
			}
		}
	}
}

func TestTurnEvidenceFinalizerWithoutObservationSourceKeepsLegacySegments(t *testing.T) {
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := validFinalizer(store).Execute(context.Background(), validFinalizeCommand(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.Segments) != 2 || len(result.Objects) != 4 {
		t.Fatalf("legacy finalization changed: segments=%d objects=%d", len(result.Manifest.Segments), len(result.Objects))
	}
}

func TestTurnEvidenceFinalizerRejectsACPAgentSessionMismatchBeforeUpload(t *testing.T) {
	t.Parallel()
	store := &recordingObjectStore{}
	finalizer := validFinalizer(store)
	command := validFinalizeCommand(t)
	command.RawACPJSONL = rawACPJSONL(t, rawFrame(t, "agent_to_client", map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{
			"sessionId": "agent-other",
			"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "wrong"}},
		},
	}))

	result, err := finalizer.Execute(context.Background(), command)
	if err == nil || !strings.Contains(err.Error(), "AgentSession identity mismatch") {
		t.Fatalf("Execute() error = %v, want identity mismatch", err)
	}
	if len(result.ArtifactRefs) != 0 || store.putCalls != 0 {
		t.Fatalf("invalid identity produced refs=%d uploads=%d", len(result.ArtifactRefs), store.putCalls)
	}
}

func TestTurnEvidenceFinalizerRejectsStoredChecksumMismatchWithoutPublishingRefs(t *testing.T) {
	t.Parallel()
	store := &recordingObjectStore{tamperFinalChecksum: true}
	finalizer := validFinalizer(store)

	result, err := finalizer.Execute(context.Background(), validFinalizeCommand(t))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Execute() error = %v, want checksum mismatch", err)
	}
	if len(result.ArtifactRefs) != 0 || result.Manifest.TraceStreamID != "" {
		t.Fatalf("checksum failure returned publishable evidence: %#v", result)
	}
}

func TestTurnEvidenceFinalizerRequiresExplicitNoFilesChanged(t *testing.T) {
	t.Parallel()
	store := &recordingObjectStore{}
	finalizer := validFinalizer(store)
	command := validFinalizeCommand(t)
	command.Patch = nil

	if _, err := finalizer.Execute(context.Background(), command); err == nil || !strings.Contains(err.Error(), "no files changed") {
		t.Fatalf("Execute() error = %v, want explicit no-files-changed failure", err)
	}
	if store.putCalls != 0 {
		t.Fatalf("invalid empty patch caused %d uploads", store.putCalls)
	}
}

func TestTurnEvidenceFinalizerAcceptsExplicitNoFilesChangedPatch(t *testing.T) {
	t.Parallel()
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalizer := validFinalizer(store)
	command := validFinalizeCommand(t)
	command.Patch = nil
	command.NoFilesChanged = true

	result, err := finalizer.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, object := range result.Objects {
		if object.Kind != artifactdomain.KindReport {
			continue
		}
		reader, err := store.Open(context.Background(), object.Stored.Key)
		if err != nil {
			t.Fatal(err)
		}
		payload, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(payload), "No files changed.") {
			t.Fatalf("report content = %q", payload)
		}
		return
	}
	t.Fatal("report object not finalized")
}

func TestTurnEvidenceFinalizerReplaysFrozenArtifactMetadataExactly(t *testing.T) {
	t.Parallel()
	store, err := artifactlocal.NewObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	finalizer := validFinalizer(store)
	finalizer.IDs = rejectingIDs{}
	finalizer.Now = func() time.Time {
		t.Fatal("wall clock must not be consulted for frozen settlement evidence")
		return time.Time{}
	}
	command := validFinalizeCommand(t)
	command.FrozenArtifacts = &artifactapp.FrozenArtifactMetadata{
		ReportArtifactID: "artifact-report-frozen",
		PatchArtifactID:  "artifact-patch-frozen",
		CreatedAt:        time.Date(2026, time.July, 23, 9, 31, 0, 0, time.UTC),
	}

	first, err := finalizer.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := finalizer.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("frozen evidence retry changed identity:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if len(first.ArtifactRefs) != 2 ||
		first.ArtifactRefs[0].ArtifactID != command.FrozenArtifacts.ReportArtifactID ||
		first.ArtifactRefs[1].ArtifactID != command.FrozenArtifacts.PatchArtifactID {
		t.Fatalf("frozen Artifact ids were not used: %#v", first.ArtifactRefs)
	}
	for _, ref := range first.ArtifactRefs {
		if ref.CreatedAt != command.FrozenArtifacts.CreatedAt {
			t.Fatalf("Artifact %s created_at=%s want=%s",
				ref.ArtifactID, ref.CreatedAt, command.FrozenArtifacts.CreatedAt)
		}
	}
}

func TestTurnEvidenceFinalizerRejectsIncompleteFrozenArtifactMetadataBeforeUpload(t *testing.T) {
	t.Parallel()
	for name, frozen := range map[string]artifactapp.FrozenArtifactMetadata{
		"missing report id": {
			PatchArtifactID: "artifact-patch-frozen",
			CreatedAt:       time.Date(2026, time.July, 23, 9, 31, 0, 0, time.UTC),
		},
		"missing patch id": {
			ReportArtifactID: "artifact-report-frozen",
			CreatedAt:        time.Date(2026, time.July, 23, 9, 31, 0, 0, time.UTC),
		},
		"missing created at": {
			ReportArtifactID: "artifact-report-frozen",
			PatchArtifactID:  "artifact-patch-frozen",
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := &recordingObjectStore{}
			finalizer := validFinalizer(store)
			command := validFinalizeCommand(t)
			command.FrozenArtifacts = &frozen

			if _, err := finalizer.Execute(context.Background(), command); err == nil {
				t.Fatal("incomplete frozen Artifact metadata was accepted")
			}
			if store.putCalls != 0 {
				t.Fatalf("invalid frozen metadata caused %d uploads", store.putCalls)
			}
		})
	}
}

func validFinalizeCommand(t *testing.T) artifactapp.FinalizeTurnEvidenceCommand {
	t.Helper()
	serverTime := time.Date(2026, time.July, 23, 9, 29, 0, 0, time.UTC)
	workerTime := serverTime.Add(-time.Second)
	return artifactapp.FinalizeTurnEvidenceCommand{
		Identity: artifactdomain.TurnIdentity{
			TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
			TaskID: "task-1", WorkerSessionID: "session-1", WorkerRunID: "run-1",
			AgentSessionID: "agent-1", AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1, TraceStreamID: "trace-stream-1", LangfuseTraceID: "trace-1",
			RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{
				ID: "m1_public_web_restricted_v1", EnforcementStatus: "legacy_unenforced",
			},
		},
		ServerRecordedAt: serverTime,
		WorkerRecordedAt: workerTime,
		RawACPJSONL: rawACPJSONL(t,
			rawFrame(t, "agent_to_client", map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": "agent-1",
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Implemented the change."}},
				},
			}),
			rawFrame(t, "agent_to_client", map[string]any{
				"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{
					"sessionId": "agent-1",
					"update":    map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tool-1", "title": "Run tests", "rawInput": map[string]any{"command": "go test ./..."}, "status": "completed"},
				},
			}),
		),
		Patch: []byte("diff --git a/hello.txt b/hello.txt\n"),
		SourceCheckpointRef: contractsv1.CheckpointRef{
			CheckpointID: "checkpoint-1",
			Digest:       "sha256:" + strings.Repeat("c", 64),
		},
	}
}

func validFinalizer(store workerapp.ObjectStore) artifactapp.TurnEvidenceFinalizer {
	return artifactapp.TurnEvidenceFinalizer{
		Store: store, IDs: &sequenceIDs{values: []string{"artifact-report-1", "artifact-patch-1"}},
		Now: func() time.Time { return time.Date(2026, time.July, 23, 9, 30, 0, 0, time.UTC) },
		Policy: artifactapp.FinalizationPolicy{
			ArtifactPermission:         contractsv1.ArtifactPermission{Visibility: "private", PolicyRevision: "single-user-local-v1"},
			ArtifactRetentionPolicyRef: "artifact-default-v1", TraceLifecyclePolicyRef: "trace-default-v1",
		},
	}
}

func rawFrame(t *testing.T, direction string, frame map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"type": "acp.raw_frame", "direction": direction, "frame": string(append(encoded, '\n'))}
}

func rawACPJSONL(t *testing.T, events ...map[string]any) []byte {
	t.Helper()
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	return []byte(output.String())
}

func assertTraceRecordProvenance(t *testing.T, payload []byte, identity artifactdomain.TurnIdentity) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	count := 0
	for decoder.More() {
		var record artifactdomain.TraceRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		count++
		if record.Sequence != uint64(count) || record.Identity != identity {
			t.Fatalf("Trace record provenance = %#v", record)
		}
	}
	if count != 2 {
		t.Fatalf("Trace record count = %d, want 2", count)
	}
}

type sequenceIDs struct {
	values []string
	next   int
}

func (ids *sequenceIDs) NewID(string) (string, error) {
	if ids.next >= len(ids.values) {
		return "", errors.New("no test ID")
	}
	value := ids.values[ids.next]
	ids.next++
	return value, nil
}

type rejectingIDs struct{}

func (rejectingIDs) NewID(string) (string, error) {
	return "", errors.New("ID generator must not be used")
}

type recordingObjectStore struct {
	putCalls            int
	tamperFinalChecksum bool
	objects             map[string][]byte
}

func (store *recordingObjectStore) PutTemporary(_ context.Context, identity workerapp.ObjectIdentity, reader io.Reader) (workerapp.TemporaryObject, error) {
	store.putCalls++
	payload, err := io.ReadAll(reader)
	if err != nil {
		return workerapp.TemporaryObject{}, err
	}
	if store.objects == nil {
		store.objects = make(map[string][]byte)
	}
	store.objects[identity.Kind] = payload
	return workerapp.TemporaryObject{UploadID: identity.Kind, Size: int64(len(payload)), Checksum: artifactdomain.Checksum(payload)}, nil
}

func (store *recordingObjectStore) Finalize(_ context.Context, _ workerapp.ObjectIdentity, temporary workerapp.TemporaryObject) (workerapp.StoredObject, error) {
	checksum := temporary.Checksum
	if store.tamperFinalChecksum {
		checksum = "sha256:" + strings.Repeat("f", 64)
	}
	return workerapp.StoredObject{Key: temporary.UploadID, Size: temporary.Size, Checksum: checksum}, nil
}

func (store *recordingObjectStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	payload, ok := store.objects[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(strings.NewReader(string(payload))), nil
}

func (store *recordingObjectStore) Verify(_ context.Context, _ string, _ string) error { return nil }
