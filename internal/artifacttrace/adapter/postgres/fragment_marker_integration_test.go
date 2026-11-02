//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// This complements the live same-Session queue test: the later Turn is the
// existing historical terminal-row seed in the SAME Run, not a live queue claim.
// Both sources are synthetic inputs to real immutable finalization; no existing
// Run/Session, checkpoint, publication, or first-freeze proof is seeded/reset.
func TestImmutableModelSupplementSameRunLegacyMarker(t *testing.T) {
	ctx := context.Background()
	first := newFragmentFixtureConfigured(t, false, false, func(f *fragmentFixture) {
		f.legacySource = func(identity artifactdomain.TurnIdentity, _ json.RawMessage, start, end time.Time) ([]byte, error) {
			return artifactapp.EncodeTurnObservationSource(artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: identity, StartedAt: start, EndedAt: end, ErrorCode: "source_incomplete"})
		}
	})
	first.publishCore(t)
	marker, err := artifactapp.ReadTurnObservationSource(ctx, first.objects, first.finalized.Manifest)
	if err != nil || marker.ErrorCode != "source_incomplete" || len(marker.ModelObservations) != 0 {
		t.Fatalf("real first source is not the content-free legacy marker: %+v %v", marker, err)
	}
	ledger := artifactpostgres.NewObservationLedger(first.pool)
	publisher := artifactapp.FragmentPublisher{Store: ledger, Objects: first.objects}
	for range 2 {
		if err := publisher.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runRef := fragmentRef(t, first.pool, "run_basis")
	originalRun, err := ledger.ReadFragmentEvidence(ctx, runRef)
	if err != nil {
		t.Fatal(err)
	}
	before := markerFirstSnapshot(t, first)
	objects := map[string][]byte{}
	if err := filepath.WalkDir(first.objectRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		objects[path] = raw
		return err
	}); err != nil {
		t.Fatal(err)
	}
	later := seedFragmentLaterTurn(t, first)
	later.legacySource = func(identity artifactdomain.TurnIdentity, input json.RawMessage, start, end time.Time) ([]byte, error) {
		m := workerapp.ModelObservation{ObservationID: "later-legacy-model", WorkerSessionID: string(identity.WorkerSessionID), WorkerRunID: string(identity.WorkerRunID), AgentTurnID: string(identity.AgentTurnID), RuntimeID: string(identity.RuntimeInstanceID), Generation: uint64(identity.RuntimeGeneration), Model: "test-model", Route: "test-route", StartedAt: start, EndedAt: end, Status: "ok", Input: input, Output: json.RawMessage(`{"answer":"later response"}`), InputTokens: 1, OutputTokens: 2}
		return artifactapp.EncodeTurnObservationSource(artifactapp.TurnObservationSource{SchemaVersion: artifactapp.ObservationSourceSchemaVersion, Identity: identity, StartedAt: start, EndedAt: end, Input: input, ModelObservations: []workerapp.ModelObservation{m}})
	}
	later.journal.Now = func() time.Time { return later.now }
	later.settlements.Now = func() time.Time { return later.now }
	e := later.envelope("event", 2, "later-terminal", "agent_turn.terminal", json.RawMessage(`{}`))
	e.AckSequence = later.start.Sequence
	if _, err := later.journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: later.authority, Sequence: e.Sequence, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: fragmentJSON(t, e)}); err != nil {
		t.Fatal(err)
	}
	id, _ := workerapp.TurnSettlementID(later.started.AgentTurnID)
	later.current, err = later.settlements.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: id, ExpectedRevision: 1, OwnerID: "later-marker-owner"})
	if err != nil {
		t.Fatal(err)
	}
	later.freezeAndFinalize(t)
	later.publishCore(t)
	normal, err := artifactapp.ReadTurnObservationSource(ctx, later.objects, later.finalized.Manifest)
	if err != nil || normal.ErrorCode != "" || len(normal.ModelObservations) != 1 || normal.Identity.WorkerRunID != marker.Identity.WorkerRunID || normal.Identity.AgentTurnID == marker.Identity.AgentTurnID {
		t.Fatalf("later source is not normal within the same Run: %+v %v", normal, err)
	}
	if !artifactapp.ProjectionJSONEqual(normal.Input, []byte(`{"text":"second input"}`)) {
		t.Fatal("later legacy source lost exact input")
	}
	if err := publisher.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var reportJSON string
	for _, object := range later.finalized.Objects {
		if object.Kind == artifactdomain.KindReport {
			reportJSON = string(fragmentJSON(t, string(readFragmentLegacyObject(t, later.objects, object.Stored.Key))))
		}
	}
	if reportJSON == "" {
		t.Fatal("later finalized report missing")
	}
	var ref artifactapp.FragmentRef
	if err := later.pool.QueryRow(ctx, `SELECT kind,source_id,frame_digest FROM observation_candidates WHERE kind='turn_basis' AND convert_from(frame,'UTF8')::jsonb->'turn'->>'agent_turn_id'=$1`, later.started.AgentTurnID).Scan(&ref.Kind, &ref.SourceID, &ref.FrameDigest); err != nil {
		t.Fatal(err)
	}
	loaded, err := (artifactapp.FragmentLoader{Metadata: ledger, Objects: later.objects}).Load(ctx, artifactapp.FragmentJob{Source: ref, Version: fragmentVersion})
	if err != nil || len(loaded.Frames) != 2 {
		t.Fatalf("marker blocked exact later parent closure: frames=%d err=%v", len(loaded.Frames), err)
	}
	var rootID, parentID string
	for _, raw := range loaded.Frames {
		frame, err := artifactdomain.DecodeObservationFragment(raw)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Scope.WorkerRunID != first.started.WorkerRunID || frame.Scope.WorkerSessionID != first.started.WorkerSessionID || frame.Scope.TenantID != "tenant-local" || frame.Scope.UserID != "user-local" || frame.Scope.WorkspaceID != "workspace-demo" {
			t.Fatalf("lost original scope: %+v", frame.Scope)
		}
		wire, err := (lf.FragmentCompiler{}).CompileObservationFragment(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		var batch lf.IngestionBatch
		if err := json.Unmarshal(wire, &batch); err != nil || len(batch.Batch) != 1 {
			t.Fatalf("compile failed: %v", err)
		}
		body := batch.Batch[0].Body
		wireInput, _ := body["input"].(map[string]any)
		wireOutput, _ := body["output"].(map[string]any)
		switch frame.Kind {
		case artifactdomain.FragmentRunBasis:
			if frame.InitialTurnID != first.started.AgentTurnID || !artifactapp.ProjectionJSONEqual([]byte(frame.InputJSON), []byte(`{"text":"execute task","n":9007199254740993}`)) || batch.Batch[0].Type != "trace-create" || wireInput["json"] != frame.InputJSON || wireInput["encoding"] != "json-text.v1" {
				t.Fatalf("root not derived from unique initial Turn: %+v", frame)
			}
			rootID, _ = body["id"].(string)
		case artifactdomain.FragmentTurnBasis:
			if frame.Turn == nil || string(frame.Turn.AgentTurnID) != later.started.AgentTurnID || !artifactapp.ProjectionJSONEqual([]byte(frame.InputJSON), normal.Input) || frame.OutputJSON != reportJSON || batch.Batch[0].Type != "span-create" || wireInput["json"] != frame.InputJSON || wireOutput["json"] != frame.OutputJSON || wireInput["encoding"] != "json-text.v1" || wireOutput["encoding"] != "json-text.v1" {
				t.Fatalf("later Turn lost input/report: %+v body=%+v", frame, body)
			}
			parentID, _ = body["traceId"].(string)
		default:
			t.Fatalf("unexpected closure kind %s", frame.Kind)
		}
	}
	if rootID == "" || parentID != rootID {
		t.Fatal("later span not bound to original root")
	}
	afterRun, err := ledger.ReadFragmentEvidence(ctx, runRef)
	if err != nil || afterRun.Object != originalRun.Object || !bytes.Equal(afterRun.Frame, originalRun.Frame) || !bytes.Equal(afterRun.Receipt, originalRun.Receipt) {
		t.Fatalf("later source changed original Run publication/proof: %v", err)
	}
	if !bytes.Equal(before, markerFirstSnapshot(t, first)) {
		t.Fatal("first checkpoint/manifest metadata changed")
	}
	for path, before := range objects {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("first frozen object changed: %s %v", filepath.Base(path), err)
		}
	}
	assertCount(t, later.pool, "trace_manifests", 2)
	assertCount(t, later.pool, "observation_publications", 3)
}

func markerFirstSnapshot(t *testing.T, f *fragmentFixture) []byte {
	t.Helper()
	var raw []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('settlement',(SELECT to_jsonb(s) FROM worker_turn_settlements s WHERE agent_turn_id=$1),'manifest',(SELECT jsonb_agg(to_jsonb(m)) FROM trace_manifests m WHERE agent_turn_id=$1))`, f.started.AgentTurnID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
