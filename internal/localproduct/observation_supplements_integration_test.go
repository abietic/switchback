//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type supplementFrozen struct {
	rows, checkpoint, source, manifest, job []byte
	sourceFile, manifestKey                 string
	objects                                 map[string][]byte
}

func (f *supplementFixture) frozen(turn observationFixtureTurn) supplementFrozen {
	f.t.Helper()
	id := string(turn.start.Envelope.AgentTurnID)
	b := supplementFrozen{objects: map[string][]byte{}}
	b.checkpoint = f.jsonRow(`SELECT phase_state FROM worker_turn_settlements WHERE agent_turn_id=$1 AND phase='completed'`, id)
	var cp runtimeTurnSettlementCheckpoint
	supplementMust(f.t, json.Unmarshal(b.checkpoint, &cp))
	b.sourceFile = cp.EvidenceInput.ObservationSourceSnapshotFile
	storage, err := f.app.runtime.environment.storage.OpenExisting(string(turn.start.Envelope.WorkerSessionID))
	supplementMust(f.t, err)
	b.source, err = readRuntimeSettlementPatchSnapshot(storage.TraceSpool.BackendPath, b.sourceFile)
	supplementMust(f.t, err)
	b.manifestKey = cp.Evidence.Finalized.ManifestObject.Key
	r, err := f.app.runtime.objects.Open(f.ctx, b.manifestKey)
	supplementMust(f.t, err)
	b.manifest, err = io.ReadAll(r)
	supplementMust(f.t, err)
	supplementMust(f.t, r.Close())
	for _, object := range cp.Evidence.Finalized.Objects {
		reader, err := f.app.runtime.objects.Open(f.ctx, object.Stored.Key)
		supplementMust(f.t, err)
		b.objects[object.Stored.Key], err = io.ReadAll(reader)
		supplementMust(f.t, err)
		supplementMust(f.t, reader.Close())
	}
	b.rows = f.jsonRow(`SELECT jsonb_build_object('manifest',to_jsonb(m),'artifacts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY artifact_id) FROM artifact_refs a WHERE a.agent_turn_id=m.agent_turn_id)) FROM trace_manifests m WHERE agent_turn_id=$1`, id)
	b.job = f.jsonRow(`SELECT to_jsonb(j) FROM observability_projection_jobs j JOIN trace_manifests m USING(trace_stream_id) WHERE m.agent_turn_id=$1`, id)
	return b
}
func (f *supplementFixture) unchanged(turn observationFixtureTurn, before supplementFrozen) {
	f.t.Helper()
	after := f.frozen(turn)
	for name, pair := range map[string][2][]byte{"checkpoint": {before.checkpoint, after.checkpoint}, "source": {before.source, after.source}, "manifest_object": {before.manifest, after.manifest}, "metadata": {before.rows, after.rows}, "v2_job": {before.job, after.job}} {
		if !bytes.Equal(pair[0], pair[1]) {
			f.t.Fatalf("late supplement rewrote frozen %s", name)
		}
	}
	if before.sourceFile != after.sourceFile || before.manifestKey != after.manifestKey {
		f.t.Fatal("frozen object identity changed")
	}
	if len(before.objects) != len(after.objects) {
		f.t.Fatal("frozen object inventory changed")
	}
	for key, raw := range before.objects {
		if !bytes.Equal(raw, after.objects[key]) {
			f.t.Fatal("frozen object bytes changed", key)
		}
	}
}
func (f *supplementFixture) discoverV2() {
	f.t.Helper()
	_, err := artifactpostgres.NewProjectionJobStore(f.pool).Discover(f.ctx, artifactapp.DurableProjectionSchemaVersion, 50)
	supplementMust(f.t, err)
}

type supplementExpectedPublication struct {
	ref   artifactapp.FragmentRef
	scope artifactdomain.ObservationScope
	frame []byte
}

func (f *supplementFixture) expectedModelPublication(callback supplementCallback) supplementExpectedPublication {
	f.t.Helper()
	e := callback.event.Envelope
	scope := artifactdomain.ObservationScope{
		TenantID:        f.app.runtime.configuration.Profile.TenantID,
		UserID:          f.app.runtime.configuration.Profile.ActorUserID,
		WorkspaceID:     f.app.runtime.configuration.WorkspaceID,
		WorkerSessionID: string(e.WorkerSessionID),
		WorkerRunID:     string(e.WorkerRunID),
	}
	frame, err := artifactapp.ModelObservationFragment(scope, e)
	supplementMust(f.t, err)
	sourceID, err := artifactapp.ModelObservationSourceID(frame)
	supplementMust(f.t, err)
	canonical, err := artifactdomain.EncodeObservationFragment(frame)
	supplementMust(f.t, err)
	wantDigest := artifactdomain.Checksum(canonical)
	var status, digest, tenant, user, workspace, session, run string
	var accepted []byte
	supplementMust(f.t, f.pool.QueryRow(f.ctx, `SELECT status,frame_digest,frame,tenant_id,user_id,workspace_id,worker_session_id,worker_run_id
FROM observation_candidates WHERE kind='model' AND source_id=$1`, sourceID).Scan(&status, &digest, &accepted, &tenant, &user, &workspace, &session, &run))
	if status != "pending" || digest != wantDigest || !bytes.Equal(accepted, canonical) ||
		tenant != scope.TenantID || user != scope.UserID || workspace != scope.WorkspaceID || session != scope.WorkerSessionID || run != scope.WorkerRunID {
		f.t.Fatalf("accepted model candidate does not match original callback: source=%s status=%s digest=%s scope=%s/%s/%s/%s/%s", sourceID, status, digest, tenant, user, workspace, session, run)
	}
	return supplementExpectedPublication{ref: artifactapp.FragmentRef{Kind: "model", SourceID: sourceID, FrameDigest: digest}, scope: scope, frame: append([]byte(nil), accepted...)}
}

func (f *supplementFixture) modelPublicationError(expected supplementExpectedPublication) error {
	f.t.Helper()
	var candidates, publications int
	err := f.pool.QueryRow(f.ctx, `SELECT
 (SELECT count(*) FROM observation_candidates c
  WHERE c.kind=$1 AND c.source_id=$2 AND c.frame_digest=$3 AND c.frame=$4 AND c.status='published'
    AND c.tenant_id=$5 AND c.user_id=$6 AND c.workspace_id=$7 AND c.worker_session_id=$8 AND c.worker_run_id=$9),
 (SELECT count(*) FROM observation_publications p
  WHERE p.kind=$1 AND p.source_id=$2 AND p.frame_digest=$3 AND p.object_checksum=$3
    AND p.tenant_id=$5 AND p.user_id=$6 AND p.workspace_id=$7 AND p.worker_session_id=$8 AND p.worker_run_id=$9)`,
		expected.ref.Kind, expected.ref.SourceID, expected.ref.FrameDigest, expected.frame,
		expected.scope.TenantID, expected.scope.UserID, expected.scope.WorkspaceID, expected.scope.WorkerSessionID, expected.scope.WorkerRunID).Scan(&candidates, &publications)
	if err != nil {
		return err
	}
	if candidates != 1 || publications != 1 {
		return fmt.Errorf("exact model publication missing: source=%s digest=%s scope=%s/%s/%s/%s/%s candidates=%d publications=%d", expected.ref.SourceID, expected.ref.FrameDigest, expected.scope.TenantID, expected.scope.UserID, expected.scope.WorkspaceID, expected.scope.WorkerSessionID, expected.scope.WorkerRunID, candidates, publications)
	}
	return nil
}

func (f *supplementFixture) publishAll(expected ...supplementExpectedPublication) {
	f.t.Helper()
	publisher := &artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.app.runtime.objects}
	n := f.count(`SELECT count(*) FROM observation_candidates WHERE status='pending'`)
	for i := 0; i < n; i++ {
		supplementMust(f.t, publisher.Tick(f.ctx))
	}
	// A model accepted before its basis is published legitimately becomes
	// retryable. Await the exact persisted eligibility time, never reset the row.
	retries := f.count(`SELECT count(*) FROM observation_candidates WHERE status='retryable'`)
	for i := 0; i < retries; i++ {
		var at time.Time
		var kind, id string
		var attempts int
		supplementMust(f.t, f.pool.QueryRow(f.ctx, `SELECT kind,source_id,attempts,next_attempt_at FROM observation_candidates WHERE status='retryable' ORDER BY next_attempt_at,source_id,kind LIMIT 1`).Scan(&kind, &id, &attempts, &at))
		f.awaitDatabaseTime(at)
		supplementMust(f.t, publisher.Tick(f.ctx))
		if f.count(`SELECT count(*) FROM observation_candidates WHERE kind=$1 AND source_id=$2 AND status='published' AND attempts=$3`, kind, id, attempts+1) != 1 {
			f.t.Fatal("exact eligible candidate did not complete retry", kind, id)
		}
	}
	if n := f.count(`SELECT count(*) FROM observation_candidates WHERE status NOT IN ('published','blocked')`); n != 0 {
		f.t.Fatalf("unpublished candidates=%d: %s", n, f.jsonRow(`SELECT jsonb_agg(jsonb_build_object('kind',kind,'status',status,'code',error_code,'run',worker_run_id)) FROM observation_candidates`))
	}
	if len(expected) > 0 {
		if n := f.count(`SELECT count(*) FROM observation_candidates WHERE status='blocked'`); n != 0 {
			f.t.Fatalf("normal publication path unexpectedly blocked candidates=%d: %s", n, f.jsonRow(`SELECT jsonb_agg(jsonb_build_object('kind',kind,'source',source_id,'status',status,'code',error_code,'run',worker_run_id) ORDER BY kind,source_id) FROM observation_candidates WHERE status='blocked'`))
		}
		for _, want := range expected {
			if err := f.modelPublicationError(want); err != nil {
				f.t.Fatal(err)
			}
		}
	}
}

// Native PG + real control/settlement/object paths. Runtime ACP and exporter
// boundaries are fixtures; these cases make no Docker/provider latency claim.
func TestImmutableModelSupplement(t *testing.T) {
	for _, name := range []string{"idle", "next_turn_active", "settlement_replay", "same_turn_early_A_late_B", "old_source_passed"} {
		t.Run(name, func(t *testing.T) {
			f := newSupplementFixture(t, nil)
			turn, callback := f.standalone()
			expectedCallbacks := make([]supplementCallback, 0, 2)
			if name == "same_turn_early_A_late_B" || name == "old_source_passed" {
				early := callback
				early.event.Envelope.MessageID += "-early-A"
				early.event.Envelope.IdempotencyKey = early.event.Envelope.MessageID
				var model workerapp.ModelObservation
				supplementMust(t, json.Unmarshal(early.event.Envelope.Payload, &model))
				model.ObservationID = early.event.Envelope.MessageID
				early.event.Envelope.Payload, _ = json.Marshal(model)
				supplementMust(t, f.binder.Bind(early.event.Envelope.MessageID))
				f.send(early)
				expectedCallbacks = append(expectedCallbacks, early)
			}
			expectedCallbacks = append(expectedCallbacks, callback)
			barrier := newSupplementBarrier()
			sent := make(chan error, 1)
			// Deliberately block before any callback send, outside the journal.
			go func() {
				if err := barrier.block(f.ctx); err != nil {
					sent <- err
					return
				}
				c, err := f.binder.Take(callback.event.Envelope.MessageID)
				if err == nil && c.AgentTurnID != string(turn.start.Envelope.AgentTurnID) {
					err = io.ErrUnexpectedEOF
				}
				if err == nil {
					err = callback.client.Publish(f.ctx, callback.event)
				}
				sent <- err
			}()
			supplementReceive(t, f.ctx, barrier.reached)
			queueID := ""
			if name == "next_turn_active" {
				queueID = f.queue(turn)
			}
			f.finish(turn, callback)
			settlement := f.settled(turn)
			if queueID != "" {
				next, _ := f.next()
				f.assertContinuation(turn, next, queueID)
			}
			f.assertNoModel(callback)
			f.discoverV2()
			if name == "old_source_passed" {
				jobs := artifactpostgres.NewProjectionJobStore(f.pool)
				gate := &observationFixtureProjector{entered: make(chan artifactapp.SourceProjection, 32)}
				w := &artifactapp.ProjectionWorker{Jobs: jobs, Loader: artifactapp.ProjectionSourceLoader{Metadata: jobs, Objects: f.app.runtime.objects}, Projector: gate}
				for i := 0; i < 2; i++ {
					supplementMust(t, w.Tick(f.ctx))
				}
				if n := f.count(`SELECT count(*) FROM observability_projection_jobs j JOIN trace_manifests m USING(trace_stream_id) WHERE m.agent_turn_id=$1 AND j.status='passed'`, string(turn.start.Envelope.AgentTurnID)); n != 1 {
					t.Fatal("old source did not pass before late callback")
				}
			}
			before := f.frozen(turn)
			execution := f.jsonRow(`SELECT jsonb_build_object('session',to_jsonb(s),'run',to_jsonb(r),'turn',to_jsonb(a)) FROM agent_turns a JOIN worker_runs r USING(worker_run_id) JOIN worker_sessions s ON s.worker_session_id=a.worker_session_id WHERE a.agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID))
			if name == "settlement_replay" {
				again, err := (runtimeTurnSettlementCoordinator{controller: runtimeSettlementPersistence{service: f.app.runtime.settlements}}).reconcileClaimed(f.ctx, settlement)
				supplementMust(t, err)
				if !bytes.Equal(again.PhaseState, settlement.PhaseState) {
					t.Fatal("completed settlement replay changed checkpoint")
				}
			}
			close(barrier.release)
			supplementMust(t, supplementReceive(t, f.ctx, sent))
			f.waitApplied(callback.event.Envelope.MessageID)
			f.assertModel(callback)
			if !bytes.Equal(execution, f.jsonRow(`SELECT jsonb_build_object('session',to_jsonb(s),'run',to_jsonb(r),'turn',to_jsonb(a)) FROM agent_turns a JOIN worker_runs r USING(worker_run_id) JOIN worker_sessions s ON s.worker_session_id=a.worker_session_id WHERE a.agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID))) {
				t.Fatal("late callback changed execution state or idle clock")
			}
			expected := make([]supplementExpectedPublication, 0, len(expectedCallbacks))
			for _, observed := range expectedCallbacks {
				expected = append(expected, f.expectedModelPublication(observed))
			}
			f.publishAll(expected...)
			f.unchanged(turn, before)
			want := 1
			if name == "same_turn_early_A_late_B" || name == "old_source_passed" {
				want = 2
			}
			if n := f.count(`SELECT count(*) FROM observation_candidates WHERE kind='model' AND worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != want {
				t.Fatalf("model facts=%d want=%d", n, want)
			}
		})
	}
	t.Run("first_turn_marker_subsequent_turn_normal", testImmutableModelSupplementFirstTurnMarkerSubsequentTurnNormal)
}

func testImmutableModelSupplementFirstTurnMarkerSubsequentTurnNormal(t *testing.T) {
	f := newSupplementFixture(t, nil)
	first, late := f.standalone()
	queue := f.queue(first)
	f.finish(first, late)
	f.settled(first)
	next, nextCallback := f.next()
	f.assertContinuation(first, next, queue)
	f.discoverV2()
	before := f.frozen(first)
	marker, err := artifactapp.DecodeTurnObservationSource(before.source)
	supplementMust(t, err)
	if marker.ErrorCode != "source_incomplete" {
		t.Fatalf("first zero-model source marker=%q", marker.ErrorCode)
	}
	f.send(late)
	f.send(nextCallback)
	f.finish(next, nextCallback)
	f.settled(next)
	f.discoverV2()
	after := f.frozen(next)
	normal, err := artifactapp.DecodeTurnObservationSource(after.source)
	supplementMust(t, err)
	if normal.ErrorCode != "" || len(normal.ModelObservations) != 1 {
		t.Fatalf("later Turn source not normal: %+v", normal)
	}
	f.publishAll(f.expectedModelPublication(late), f.expectedModelPublication(nextCallback))
	f.unchanged(first, before)
}

func TestImmutableModelSupplementPublicationAssertionFaultSensitivity(t *testing.T) {
	f := newSupplementFixture(t, nil)
	turn, callback := f.standalone()
	f.send(callback)
	f.finish(turn, callback)
	f.settled(turn)
	expected := f.expectedModelPublication(callback)
	if err := f.modelPublicationError(expected); err == nil {
		t.Fatal("exact publication assertion accepted a not-yet-published candidate")
	}
	f.publishAll(expected)

	for name, mutate := range map[string]func(*supplementExpectedPublication){
		"wrong_source": func(want *supplementExpectedPublication) { want.ref.SourceID += "-wrong" },
		"wrong_digest": func(want *supplementExpectedPublication) {
			want.ref.FrameDigest = artifactdomain.Checksum([]byte("wrong frame"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			wrong := expected
			mutate(&wrong)
			if err := f.modelPublicationError(wrong); err == nil {
				t.Fatal("exact publication assertion accepted a mismatched expectation")
			}
		})
	}
}

type supplementBlockedProjector struct {
	entered chan artifactapp.LoadedFragmentProjection
	barrier supplementBarrier
}

func (p *supplementBlockedProjector) ExportAndVerify(ctx context.Context, s artifactapp.LoadedFragmentProjection) error {
	p.entered <- s
	return p.barrier.block(ctx)
}
func TestImmutableModelSupplementBlockedProjector(t *testing.T) {
	f := newSupplementFixture(t, nil)
	turn, callback := f.standalone()
	f.send(callback)
	// The bootstrap source is already complete and can enter the exporter while
	// this standalone Turn and its queued successor continue on the same runtime.
	// Publish only bootstrap candidates: the active model lacks core evidence.
	publisher := &artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.app.runtime.objects}
	for i := 0; i < 3; i++ {
		supplementMust(t, publisher.Tick(f.ctx))
	}
	gate := &supplementBlockedProjector{entered: make(chan artifactapp.LoadedFragmentProjection, 1), barrier: newSupplementBarrier()}
	ledger := artifactpostgres.NewObservationLedger(f.pool)
	jobs := artifactpostgres.NewFragmentJobStore(f.pool)
	w := &artifactapp.FragmentWorker{Jobs: jobs, Loader: artifactapp.FragmentLoader{Metadata: ledger, Objects: f.app.runtime.objects}, Projector: gate}
	done := make(chan error, 1)
	go func() { done <- w.Tick(f.ctx) }()
	loaded := supplementReceive(t, f.ctx, gate.entered)
	if f.count(`SELECT count(*) FROM observation_publications WHERE kind=$1 AND source_id=$2 AND frame_digest=$3 AND worker_run_id=$4 AND worker_session_id=$5`, loaded.Source.Kind, loaded.Source.SourceID, loaded.Source.FrameDigest, string(f.bootstrap.start.Envelope.WorkerRunID), string(f.bootstrap.start.Envelope.WorkerSessionID)) != 1 {
		t.Fatal("projector did not enter exact bootstrap source")
	}
	supplementReceive(t, f.ctx, gate.barrier.reached)
	queue := f.queue(turn)
	f.finish(turn, callback)
	f.settled(turn)
	next, _ := f.next()
	f.assertContinuation(turn, next, queue)
	if n := f.count(`SELECT count(*) FROM observation_fragment_jobs WHERE kind=$1 AND source_id=$2 AND status='running'`, loaded.Source.Kind, loaded.Source.SourceID); n != 1 {
		t.Fatal("blocked projector did not retain its exact running job")
	}
	select {
	case err := <-done:
		t.Fatalf("projector returned before release: %v", err)
	default:
	}
	close(gate.barrier.release)
	supplementMust(t, supplementReceive(t, f.ctx, done))
}
