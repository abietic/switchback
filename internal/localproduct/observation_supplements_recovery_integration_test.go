//go:build integration

package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgconn"
)

// Real repository delegates retain persisted rows and server clocks. Shorter
// valid test leases make crash reclamation bounded; no row is reset or updated.
type supplementCandidateLease struct {
	*artifactpostgres.ObservationLedger
	claims chan artifactapp.FragmentCandidate
}

func (s supplementCandidateLease) ClaimCandidate(ctx context.Context, _ time.Duration) (artifactapp.FragmentCandidate, bool, error) {
	v, ok, err := s.ObservationLedger.ClaimCandidate(ctx, time.Second)
	if ok && err == nil {
		s.claims <- v
	}
	return v, ok, err
}

type supplementFinalize struct {
	workerapp.ObjectStore
	objects chan workerapp.StoredObject
	barrier supplementBarrier
}

func (s supplementFinalize) Finalize(ctx context.Context, id workerapp.ObjectIdentity, temp workerapp.TemporaryObject) (workerapp.StoredObject, error) {
	v, err := s.ObjectStore.Finalize(ctx, id, temp)
	if err != nil {
		return v, err
	}
	s.objects <- v
	return v, s.barrier.block(ctx)
}

type supplementFinish struct {
	*artifactpostgres.FragmentJobStore
	claims    chan artifactapp.FragmentJob
	finishing chan artifactapp.FragmentJob
	barrier   supplementBarrier
}

func (s supplementFinish) Claim(ctx context.Context, v string, _ time.Duration) (artifactapp.FragmentJob, bool, error) {
	j, ok, err := s.FragmentJobStore.Claim(ctx, v, time.Second)
	if ok && err == nil {
		s.claims <- j
	}
	return j, ok, err
}
func (s supplementFinish) Finish(ctx context.Context, j artifactapp.FragmentJob, c artifactapp.ProjectionCompletion) error {
	if c.Status != "passed" {
		return errors.New("fixture expected verified HTTP readback before Finish")
	}
	s.finishing <- j
	if err := s.barrier.block(ctx); err != nil {
		return err
	}
	return s.FragmentJobStore.Finish(ctx, j, c)
}

func (f *supplementFixture) sealedProof() []byte {
	return f.jsonRow(`SELECT jsonb_agg(jsonb_build_object('kind',kind,'id',source_id,'frame',encode(frame,'hex'),'receipt',encode(receipt,'hex'),'provenance',provenance) ORDER BY kind,source_id) FROM observation_candidates`)
}
func (f *supplementFixture) fenceAndStop(turn observationFixtureTurn) {
	f.t.Helper()
	e := turn.start.Envelope
	identity := controlgrpc.RuntimeIdentity{WorkerSessionID: string(e.WorkerSessionID), RuntimeInstanceID: string(e.RuntimeInstanceID), RuntimeGeneration: e.RuntimeGeneration, ChannelEpoch: e.ChannelEpoch}
	supplementMust(f.t, f.app.runtime.environment.fenceObservationRegistry(f.ctx, identity))
	if _, ok := f.app.runtime.environment.get(identity.RuntimeInstanceID); ok {
		f.t.Fatal("fenced exact registry remained prepared")
	}
	f.stop()
	var cancel context.CancelFunc
	f.ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
	f.t.Cleanup(cancel)
}
func (f *supplementFixture) restartPool() { f.t.Helper(); f.pool = postgrestest.Restart(f.t, f.pool) }
func newSupplementSealed(t *testing.T) (*supplementFixture, supplementExpectedPublication) {
	f := newSupplementFixture(t, nil)
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "crash-source", "Produce deterministic report")
	turn, callback := f.next()
	f.finish(turn, callback)
	f.settled(turn)
	// Basis first means crash tests exercise their named window, not the
	// independent model-before-parent retry scheduler.
	f.publishAll()
	f.send(callback)
	expected := f.expectedModelPublication(callback)
	f.fenceAndStop(turn)
	return f, expected
}

func TestImmutableModelSupplementRecovery(t *testing.T) {
	t.Run("core_manifest_finalize_before_metadata", func(t *testing.T) {
		barrier := newSupplementBarrier()
		objects := make(chan workerapp.StoredObject, 1)
		f := newSupplementFixture(t, func(f *supplementFixture) {
			f.app.runtime.finalizer.Store = &supplementCoreFinalize{ObjectStore: f.app.runtime.objects, barrier: barrier, objects: objects}
		})
		createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "core-manifest-window", "Produce report")
		turn, callback := f.next()
		f.finish(turn, callback)
		object := supplementReceive(t, f.ctx, objects)
		supplementReceive(t, f.ctx, barrier.reached)
		if f.count(`SELECT count(*) FROM trace_manifests`) != 0 {
			t.Fatal("core metadata preceded manifest Finalize")
		}
		supplementMust(t, f.app.runtime.objects.Verify(f.ctx, object.Key, object.Checksum))
		proof := f.sealedProof()
		close(barrier.release)
		f.settled(turn)
		if f.count(`SELECT count(*) FROM trace_manifests WHERE manifest_object_key=$1 AND manifest_checksum=$2`, object.Key, object.Checksum) != 1 {
			t.Fatal("core recovery changed manifest identity")
		}
		if !bytes.Equal(proof, f.sealedProof()) {
			t.Fatal("core recovery replaced first basis receipt")
		}
	})
	t.Run("scan_before_apply", func(t *testing.T) {
		barrier := newSupplementBarrier()
		scanned := make(chan workerapp.RuntimeObservationCapture, 1)
		var attempts atomic.Int32
		f := newSupplementFixture(t, func(f *supplementFixture) {
			preparer := supplementPreparer{delegate: f.app.runtime, barrier: barrier, scanned: scanned}
			supplementMust(t, f.app.runtime.hub.SetModelObservationAcceptor(workerapp.ModelObservationAcceptor{Preparer: &preparer}))
			f.journal.beforeApply = func(ctx context.Context, r workerapp.ApplyRuntimeControlEventRecord) error {
				if r.Command.ObservationCapture != nil && attempts.Add(1) == 1 {
					return errors.New("fixture interruption after scan before Apply")
				}
				return nil
			}
		})
		createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "scan-window", "Produce report")
		turn, callback := f.next()
		supplementMust(t, callback.client.Publish(f.ctx, callback.event))
		capture := supplementReceive(t, f.ctx, scanned)
		supplementReceive(t, f.ctx, barrier.reached)
		if len(capture.Sealed) == 0 || f.count(`SELECT count(*) FROM observation_candidates`) != 0 {
			t.Fatal("scan leaked uncommitted receipt")
		}
		if f.count(`SELECT count(*) FROM runtime_control_messages WHERE message_id=$1 AND effect_status='applied'`, callback.event.Envelope.MessageID) != 0 {
			t.Fatal("scan advanced Apply cursor")
		}
		close(barrier.release)
		f.waitApplied(callback.event.Envelope.MessageID)
		f.assertModel(callback)
		if attempts.Load() < 2 {
			t.Fatal("real delivery did not replay failed Apply")
		}
		f.finish(turn, callback)
		f.settled(turn)
	})
	t.Run("apply_commit_before_response", func(t *testing.T) {
		barrier := newSupplementBarrier()
		records := make(chan workerapp.ApplyRuntimeControlEventRecord, 1)
		var once atomic.Bool
		f := newSupplementFixture(t, func(f *supplementFixture) {
			f.journal.afterApply = func(ctx context.Context, r workerapp.ApplyRuntimeControlEventRecord, _ workerapp.RuntimeControlEventAcceptance) error {
				if r.Command.ObservationCapture != nil && once.CompareAndSwap(false, true) {
					records <- r
					if err := barrier.block(ctx); err != nil {
						return err
					}
					return errors.New("fixture response lost after commit")
				}
				return nil
			}
		})
		createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "response-window", "Produce report")
		turn, callback := f.next()
		supplementMust(t, callback.client.Publish(f.ctx, callback.event))
		record := supplementReceive(t, f.ctx, records)
		supplementReceive(t, f.ctx, barrier.reached)
		f.assertModel(callback)
		before := f.sealedProof()
		if f.count(`SELECT count(*) FROM runtime_control_messages WHERE message_id=$1 AND effect_status='applied'`, callback.event.Envelope.MessageID) != 1 {
			t.Fatal("response boundary preceded actual Apply")
		}
		close(barrier.release)
		// Repeat the exact durable command through the real repository. Its
		// committed outcome, receipt and cursor must survive response loss.
		again, err := f.journal.RuntimeControlJournal.ApplyRuntimeControlEvent(f.ctx, record)
		supplementMust(t, err)
		if again.Message.EffectStatus != "applied" {
			t.Fatal("replay lost committed outcome")
		}
		if !bytes.Equal(before, f.sealedProof()) {
			t.Fatal("Apply replay mutated original receipt")
		}
		f.finish(turn, callback)
		f.settled(turn)
	})
	t.Run("first_freeze_before_finalize", func(t *testing.T) {
		barrier := newSupplementBarrier()
		frozen := make(chan workerapp.TurnSettlement, 1)
		var once atomic.Bool
		f := newSupplementFixture(t, func(f *supplementFixture) {
			f.settlements.afterFreeze = func(ctx context.Context, s workerapp.TurnSettlement) error {
				var cp runtimeTurnSettlementCheckpoint
				if json.Unmarshal(s.PhaseState, &cp) == nil && s.Phase == workerapp.TurnSettlementAccepted && cp.EvidenceInput != nil && once.CompareAndSwap(false, true) {
					frozen <- s
					if err := barrier.block(ctx); err != nil {
						return err
					}
					return errors.New("fixture interrupted after committed first freeze")
				}
				return nil
			}
		})
		createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "freeze-window", "Produce report")
		turn, callback := f.next()
		f.finish(turn, callback)
		first := supplementReceive(t, f.ctx, frozen)
		supplementReceive(t, f.ctx, barrier.reached)
		proof := f.sealedProof()
		if f.count(`SELECT count(*) FROM observation_candidates WHERE kind IN ('run_basis','turn_basis')`) != 2 || f.count(`SELECT count(*) FROM trace_manifests`) != 0 {
			t.Fatal("first freeze was not atomic before finalization")
		}
		close(barrier.release)
		completed := f.settled(turn)
		var a, b runtimeTurnSettlementCheckpoint
		supplementMust(t, json.Unmarshal(first.PhaseState, &a))
		supplementMust(t, json.Unmarshal(completed.PhaseState, &b))
		aa, _ := json.Marshal(a.EvidenceInput)
		bb, _ := json.Marshal(b.EvidenceInput)
		if !bytes.Equal(aa, bb) || !bytes.Equal(proof, f.sealedProof()) {
			t.Fatal("settlement recovery rescanned or replaced frozen basis")
		}
	})
	t.Run("fragment_finalize_before_publication", func(t *testing.T) {
		f, expected := newSupplementSealed(t)
		before := f.sealedProof()
		store := supplementCandidateLease{artifactpostgres.NewObservationLedger(f.pool), make(chan artifactapp.FragmentCandidate, 1)}
		objects := supplementFinalize{f.app.runtime.objects, make(chan workerapp.StoredObject, 1), newSupplementBarrier()}
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- (&artifactapp.FragmentPublisher{Store: store, Objects: objects}).Tick(ctx) }()
		claim := supplementReceive(t, f.ctx, store.claims)
		object := supplementReceive(t, f.ctx, objects.objects)
		supplementReceive(t, f.ctx, objects.barrier.reached)
		if f.count(`SELECT count(*) FROM observation_publications WHERE kind=$1 AND source_id=$2`, claim.Source.Kind, claim.Source.SourceID) != 0 {
			t.Fatal("fragment published before boundary")
		}
		supplementMust(t, f.app.runtime.objects.Verify(f.ctx, object.Key, object.Checksum))
		cancel()
		if err := supplementReceive(t, f.ctx, done); !errors.Is(err, context.Canceled) {
			t.Fatalf("crash result %v", err)
		}
		f.restartPool()
		f.awaitDatabaseTime(claim.LeaseUntil)
		supplementMust(t, (&artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.app.runtime.objects}).Tick(f.ctx))
		if f.count(`SELECT count(*) FROM observation_publications WHERE kind=$1 AND source_id=$2 AND object_key=$3 AND object_checksum=$4`, claim.Source.Kind, claim.Source.SourceID, object.Key, object.Checksum) != 1 {
			t.Fatal("reclaim did not publish identical finalized object")
		}
		var fence int64
		supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT lease_token FROM observation_candidates WHERE kind=$1 AND source_id=$2`, claim.Source.Kind, claim.Source.SourceID).Scan(&fence))
		if fence != claim.LeaseToken+1 {
			t.Fatal("publisher reclaim did not advance fencing token")
		}
		if err := f.modelPublicationError(expected); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, f.sealedProof()) {
			t.Fatal("restart changed sealed source or receipt")
		}
	})
	t.Run("publication_before_job", func(t *testing.T) {
		f, expected := newSupplementSealed(t)
		f.publishAll(expected)
		before := f.sealedProof()
		if f.count(`SELECT count(*) FROM observation_publications`) != 3 || f.count(`SELECT count(*) FROM observation_fragment_jobs`) != 0 {
			t.Fatal("publication/job boundary not established")
		}
		f.restartPool()
		jobs := artifactpostgres.NewFragmentJobStore(f.pool)
		for i := 0; i < 2; i++ {
			_, err := jobs.Discover(f.ctx, artifactapp.FragmentProjectionVersion, 50)
			supplementMust(t, err)
		}
		if f.count(`SELECT count(*) FROM observation_fragment_jobs`) != 3 {
			t.Fatal("discovery lost or duplicated publication")
		}
		if !bytes.Equal(before, f.sealedProof()) {
			t.Fatal("discovery changed sealed source")
		}
	})
	t.Run("real_http_before_finish", func(t *testing.T) {
		f, expected := newSupplementSealed(t)
		f.publishAll(expected)
		before := f.sealedProof()
		api := newSupplementHTTP(t)
		client, err := lf.NewClient(lf.ClientOptions{BaseURL: api.server.URL, PublicKey: "fixture-pk", SecretKey: "fixture-sk"})
		supplementMust(t, err)
		scanner, err := newRuntimeKnownTokenScanner(f.app.runtime.configuration)
		supplementMust(t, err)
		projector, err := lf.NewFragmentProjector(client, scanner)
		supplementMust(t, err)
		jobs := supplementFinish{artifactpostgres.NewFragmentJobStore(f.pool), make(chan artifactapp.FragmentJob, 1), make(chan artifactapp.FragmentJob, 1), newSupplementBarrier()}
		ctx, cancel := context.WithCancel(f.ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- (&artifactapp.FragmentWorker{Jobs: jobs, Loader: artifactapp.FragmentLoader{Metadata: artifactpostgres.NewObservationLedger(f.pool), Objects: f.app.runtime.objects}, Projector: projector}).Tick(ctx)
		}()
		claim := supplementReceive(t, f.ctx, jobs.claims)
		finished := supplementReceive(t, f.ctx, jobs.finishing)
		supplementReceive(t, f.ctx, jobs.barrier.reached)
		if finished != claim {
			t.Fatal("HTTP receipt did not bind claimed job")
		}
		first := api.snapshot()
		if len(first) == 0 {
			t.Fatal("no real HTTP ingestion/readback")
		}
		if f.count(`SELECT count(*) FROM observation_fragment_jobs WHERE kind=$1 AND source_id=$2 AND status='running'`, claim.Source.Kind, claim.Source.SourceID) != 1 {
			t.Fatal("Finish committed before barrier")
		}
		cancel()
		if err := supplementReceive(t, f.ctx, done); err == nil {
			t.Fatal("interrupted Finish succeeded")
		}
		f.restartPool()
		f.awaitDatabaseTime(claim.LeaseUntil)
		fresh := artifactpostgres.NewFragmentJobStore(f.pool)
		w := &artifactapp.FragmentWorker{Jobs: fresh, Loader: artifactapp.FragmentLoader{Metadata: artifactpostgres.NewObservationLedger(f.pool), Objects: f.app.runtime.objects}, Projector: projector}
		for i := 0; i < 3; i++ {
			supplementMust(t, w.Tick(f.ctx))
		}
		if f.count(`SELECT count(*) FROM observation_fragment_jobs WHERE status='passed'`) != 3 {
			t.Fatal("real HTTP recovery did not pass all sources")
		}
		var fence int64
		supplementMust(t, f.pool.QueryRow(f.ctx, `SELECT lease_token FROM observation_fragment_jobs WHERE kind=$1 AND source_id=$2`, claim.Source.Kind, claim.Source.SourceID).Scan(&fence))
		if fence != claim.LeaseToken+1 {
			t.Fatal("projector reclaim did not fence old worker")
		}
		if err := fresh.Finish(f.ctx, claim, artifactapp.ProjectionCompletion{Status: "passed"}); !errors.Is(err, artifactapp.ErrProjectionLeaseLost) {
			t.Fatalf("old Finish accepted: %v", err)
		}
		api.assertReplay(t, first)
		if !bytes.Equal(before, f.sealedProof()) {
			t.Fatal("HTTP replay changed frozen source")
		}
	})
}

type supplementPreparer struct {
	delegate workerapp.ModelObservationPreparer
	barrier  supplementBarrier
	scanned  chan workerapp.RuntimeObservationCapture
	once     atomic.Bool
}

func (p *supplementPreparer) PrepareModelObservation(ctx context.Context, e workerapp.WorkerControlEvent) workerapp.RuntimeObservationCapture {
	v := p.delegate.PrepareModelObservation(ctx, e)
	if p.once.CompareAndSwap(false, true) {
		p.scanned <- v.Clone()
		_ = p.barrier.block(ctx)
	}
	return v
}

type supplementCoreFinalize struct {
	workerapp.ObjectStore
	barrier supplementBarrier
	objects chan workerapp.StoredObject
	once    atomic.Bool
}

func (s *supplementCoreFinalize) Finalize(ctx context.Context, id workerapp.ObjectIdentity, temp workerapp.TemporaryObject) (workerapp.StoredObject, error) {
	v, err := s.ObjectStore.Finalize(ctx, id, temp)
	if err == nil && id.Kind == "trace-manifest" && s.once.CompareAndSwap(false, true) {
		s.objects <- v
		if err = s.barrier.block(ctx); err == nil {
			err = errors.New("fixture interrupted after core manifest Finalize")
		}
	}
	return v, err
}

func TestImmutableModelSupplementIgnoredStaleReplay(t *testing.T) {
	records := make(chan workerapp.ApplyRuntimeControlEventRecord, 4)
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.journal.afterApply = func(_ context.Context, r workerapp.ApplyRuntimeControlEventRecord, _ workerapp.RuntimeControlEventAcceptance) error {
			if r.Command.ObservationCapture != nil {
				records <- r
			}
			return nil
		}
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "stale-model", "Produce report")
	turn, callback := f.next()
	stale := callback.event
	stale.Envelope.MessageID = "stale-model"
	stale.Envelope.IdempotencyKey = "stale-model"
	var m workerapp.ModelObservation
	supplementMust(t, json.Unmarshal(stale.Envelope.Payload, &m))
	m.ObservationID = "stale-model"
	m.WorkerRunID = "different-run"
	stale.Envelope.Payload, _ = json.Marshal(m)
	supplementMust(t, callback.client.Publish(f.ctx, stale))
	applied := f.waitApplied("stale-model")
	record := supplementReceive(t, f.ctx, records)
	if !bytes.Contains(applied.Message.Outcome, []byte("model_observation_ignored")) || f.count(`SELECT count(*) FROM observation_candidates`) != 0 {
		t.Fatal("stale model was not ignored")
	}
	before := f.jsonRow(`SELECT to_jsonb(m) FROM runtime_control_messages m WHERE message_id='stale-model'`)
	record.Command.ObservationCapture = nil
	again, err := f.journal.RuntimeControlJournal.ApplyRuntimeControlEvent(f.ctx, record)
	supplementMust(t, err)
	if !bytes.Equal(applied.Message.Outcome, again.Message.Outcome) || !bytes.Equal(before, f.jsonRow(`SELECT to_jsonb(m) FROM runtime_control_messages m WHERE message_id='stale-model'`)) || f.count(`SELECT count(*) FROM observation_candidates`) != 0 {
		t.Fatal("replay upgraded ignored stale model")
	}
	f.finish(turn, callback)
	f.settled(turn)
}

func TestImmutableModelSupplementPermanentScanFailureAllowsHeartbeat(t *testing.T) {
	f := newSupplementFixture(t, nil)
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "scan-failure", "Produce report")
	turn, callback := f.next()
	var m workerapp.ModelObservation
	supplementMust(t, json.Unmarshal(callback.event.Envelope.Payload, &m))
	m.Input = json.RawMessage(`{"unsafe":"fixture-provider-credential"}`)
	callback.event.Envelope.Payload, _ = json.Marshal(m)
	supplementMust(t, callback.client.Publish(f.ctx, callback.event))
	f.waitApplied(callback.event.Envelope.MessageID)
	if f.count(`SELECT count(*) FROM observation_candidates WHERE kind='model' AND status='blocked' AND frame IS NULL AND receipt IS NULL AND error_code='credential_detected'`) != 1 {
		t.Fatal("permanent scan failure did not persist content-free blocked evidence")
	}
	heartbeat := callback.event
	heartbeat.Envelope.Kind = "heartbeat"
	heartbeat.Envelope.AgentTurnID = "runtime-heartbeat"
	heartbeat.Envelope.MessageID = "heartbeat-after-scan"
	heartbeat.Envelope.IdempotencyKey = heartbeat.Envelope.MessageID
	heartbeat.Envelope.MessageType = "runtime.heartbeat"
	heartbeat.Envelope.Payload = json.RawMessage(`{"heartbeat_sequence":1}`)
	supplementMust(t, callback.client.Publish(f.ctx, heartbeat))
	f.waitApplied(heartbeat.Envelope.MessageID)
	if f.count(`SELECT count(*) FROM runtime_heartbeat_observations WHERE runtime_instance_id=$1 AND heartbeat_sequence=1`, string(turn.start.Envelope.RuntimeInstanceID)) != 1 {
		t.Fatal("scan failure blocked heartbeat liveness commit")
	}
	f.finish(turn, callback)
	f.settled(turn)
}

func TestImmutableModelSupplementPostgresFailureRollsBack(t *testing.T) {
	entered := make(chan struct{}, 1)
	barrier := newSupplementBarrier()
	var first atomic.Bool
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.journal.beforeApply = func(ctx context.Context, r workerapp.ApplyRuntimeControlEventRecord) error {
			if r.Command.ObservationCapture != nil && first.CompareAndSwap(false, true) {
				entered <- struct{}{}
				return barrier.block(ctx)
			}
			return nil
		}
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "rollback-window", "Produce report")
	turn, callback := f.next()
	supplementMust(t, callback.client.Publish(f.ctx, callback.event))
	supplementReceive(t, f.ctx, entered)
	supplementReceive(t, f.ctx, barrier.reached)
	// A real PostgreSQL constraint failure inside the journal transaction; DDL
	// only affects this synthetic schema and never rewrites business rows.
	_, err := f.pool.Exec(f.ctx, `ALTER TABLE observation_candidates ADD CONSTRAINT task8_fail_model CHECK(kind<>'model')`)
	supplementMust(t, err)
	// Exercise the same repository Apply with the accepted event's authority.
	var event workerapp.RuntimeControlEventAcceptance
	for {
		event = supplementReceive(t, f.ctx, f.journal.accepted)
		if event.Message.MessageID == callback.event.Envelope.MessageID {
			break
		}
	}
	channel := event.Channel
	a := workerapp.RuntimeControlAuthority{WorkerSessionID: channel.WorkerSessionID, RuntimeInstanceID: channel.RuntimeInstanceID, RuntimeGeneration: channel.RuntimeGeneration, ChannelEpoch: channel.ChannelEpoch, ProcessNonce: channel.ProcessNonce, StreamAttempt: channel.StreamAttempt, OwnerFence: channel.OwnerFence}
	var accepted workerapp.WorkerControlEvent
	supplementMust(t, json.Unmarshal(event.Message.Envelope, &accepted.Envelope))
	capture := f.app.runtime.PrepareModelObservation(f.ctx, accepted)
	service := workerapp.RuntimeControlJournalService{Store: f.journal.RuntimeControlJournal, Now: time.Now}
	_, err = service.ApplyEvent(f.ctx, workerapp.ApplyRuntimeControlEventCommand{Authority: a, Sequence: event.Message.Sequence, MessageID: event.Message.MessageID, IdempotencyKey: event.Message.MessageID, Outcome: json.RawMessage(`{"status":"model_observation_recorded"}`), ObservationCapture: &capture})
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23514" || pgError.ConstraintName != "task8_fail_model" {
		t.Fatalf("Apply did not reach the injected PostgreSQL constraint: %v", err)
	}
	if f.count(`SELECT count(*) FROM observation_candidates`) != 0 || f.count(`SELECT count(*) FROM runtime_control_messages WHERE message_id=$1 AND effect_status='applied'`, event.Message.MessageID) != 0 {
		t.Fatal("PostgreSQL fault partially committed capture/cursor")
	}
	_, err = f.pool.Exec(f.ctx, `ALTER TABLE observation_candidates DROP CONSTRAINT task8_fail_model`)
	supplementMust(t, err)
	close(barrier.release)
	f.waitApplied(callback.event.Envelope.MessageID)
	f.assertModel(callback)
	f.finish(turn, callback)
	f.settled(turn)
}

// Deterministic local HTTP ingestion + source-scoped readback. The production
// client/compiler/projector run unmodified; this is not external Langfuse proof.
type supplementHTTP struct {
	server              *httptest.Server
	mu                  sync.Mutex
	roots, observations map[string]map[string]any
	wire                map[string][]byte
	seen                map[string]int
}

func newSupplementHTTP(t *testing.T) *supplementHTTP {
	a := &supplementHTTP{roots: map[string]map[string]any{}, observations: map[string]map[string]any{}, wire: map[string][]byte{}, seen: map[string]int{}}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var batch lf.IngestionBatch
			d := json.NewDecoder(r.Body)
			d.UseNumber()
			if err := d.Decode(&batch); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			for _, e := range batch.Batch {
				b := e.Body
				id := b["id"].(string)
				raw, _ := json.Marshal(b)
				if old, ok := a.wire[id]; ok && !bytes.Equal(old, raw) {
					t.Errorf("replay changed stable wire ID %s", id)
				}
				a.wire[id] = raw
				a.seen[id]++
				metadata := b["metadata"].(map[string]any)
				for k, v := range metadata {
					var decoded any
					d := json.NewDecoder(strings.NewReader(v.(string)))
					d.UseNumber()
					if err := d.Decode(&decoded); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					metadata[k] = decoded
				}
				if e.Type == "trace-create" {
					a.roots[id] = b
				} else {
					b["type"] = "SPAN"
					if e.Type == "generation-create" {
						b["type"] = "GENERATION"
					}
					a.observations[id] = b
				}
			}
			w.WriteHeader(202)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/public/traces/") {
			_ = json.NewEncoder(w).Encode(a.roots[strings.TrimPrefix(r.URL.Path, "/api/public/traces/")])
			return
		}
		_ = json.NewEncoder(w).Encode(a.observations[strings.TrimPrefix(r.URL.Path, "/api/public/observations/")])
	}))
	t.Cleanup(a.server.Close)
	return a
}
func (a *supplementHTTP) snapshot() map[string][]byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string][]byte{}
	for k, v := range a.wire {
		m[k] = bytes.Clone(v)
	}
	return m
}
func (a *supplementHTTP) assertReplay(t *testing.T, first map[string][]byte) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, v := range first {
		if a.seen[id] < 2 || !bytes.Equal(v, a.wire[id]) {
			t.Fatal("HTTP crash replay lost stable identity/bytes", id)
		}
	}
}
