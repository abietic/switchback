//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

type progressPublicationStore struct {
	*artifactpostgres.MetadataStore
	ready  chan artifactapp.PublishTurnEvidenceRecord
	resume chan struct{}
	result chan error
}

type progressQueuedStore struct {
	*workerpostgres.QueuedTurnStore
	ready  chan workerapp.ContinueQueuedTurnRecord
	resume chan struct{}
	result chan error
}

func (s *progressQueuedStore) ContinueQueuedTurn(ctx context.Context, r workerapp.ContinueQueuedTurnRecord) (workerapp.ContinuedQueuedTurn, error) {
	s.ready <- r
	select {
	case <-s.resume:
	case <-ctx.Done():
		return workerapp.ContinuedQueuedTurn{}, ctx.Err()
	}
	v, err := s.QueuedTurnStore.ContinueQueuedTurn(ctx, r)
	s.result <- err
	return v, err
}

func (s *progressQueuedStore) ContinueSettlementQueuedTurn(ctx context.Context, settlement workerapp.TurnSettlement, r workerapp.ContinueQueuedTurnRecord) (workerapp.ContinuedQueuedTurn, error) {
	s.ready <- r
	select {
	case <-s.resume:
	case <-ctx.Done():
		return workerapp.ContinuedQueuedTurn{}, ctx.Err()
	}
	v, err := s.QueuedTurnStore.ContinueSettlementQueuedTurn(ctx, settlement, r)
	s.result <- err
	return v, err
}

func TestSettlementContinuationRuntimeProgress(t *testing.T) {
	var store *progressQueuedStore
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.runtime.heartbeatStart = make(chan struct{})
		store = &progressQueuedStore{QueuedTurnStore: workerpostgres.NewQueuedTurnStore(f.pool), ready: make(chan workerapp.ContinueQueuedTurnRecord, 10), resume: make(chan struct{}), result: make(chan error, 10)}
		f.app.runtime.queued.Store = store
	})
	turn, callback := f.standalone()
	queueID := f.queue(turn)
	f.finish(turn, callback)
	r := supplementReceive(t, f.ctx, store.ready)
	e := turn.start.Envelope
	e.MessageID, e.IdempotencyKey = "settlement-progress-heartbeat", "settlement-progress-heartbeat"
	e.Kind, e.MessageType, e.AgentTurnID = "heartbeat", "runtime.heartbeat", "runtime-heartbeat"
	e.Payload = []byte(`{"heartbeat_sequence":1}`)
	if err := turn.client.TryPublishHeartbeat(workerapp.WorkerControlEvent{Envelope: e}); err != nil {
		t.Fatal(err)
	}
	for {
		a := supplementReceive(t, f.ctx, f.journal.accepted)
		if a.Message.MessageID == e.MessageID {
			break
		}
	}
	var revision uint64
	if err := f.pool.QueryRow(f.ctx, `SELECT revision FROM runtime_instances WHERE runtime_instance_id=$1`, string(e.RuntimeInstanceID)).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision <= r.Command.ExpectedRuntimeRevision {
		t.Fatal("heartbeat did not advance Runtime")
	}
	settlement, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueuedTurnStore.ContinueQueuedTurn(f.ctx, r); !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("generic queue accepted stale Runtime: %v", err)
	}
	assertSettlementProgressFencing(t, f, settlement, func(s workerapp.TurnSettlement) error {
		_, err := store.QueuedTurnStore.ContinueSettlementQueuedTurn(f.ctx, s, r)
		return err
	})
	wrong := r
	wrong.Command.QueueCommandID = "wrong-command"
	if _, err := store.QueuedTurnStore.ContinueSettlementQueuedTurn(f.ctx, settlement, wrong); err == nil {
		t.Fatal("accepted mismatched frozen queue command")
	}
	rollback := r
	if err := f.pool.QueryRow(f.ctx, `SELECT event_id FROM outbox_events LIMIT 1`).Scan(&rollback.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueuedTurnStore.ContinueSettlementQueuedTurn(f.ctx, settlement, rollback); err == nil {
		t.Fatal("expected duplicate outbox failure")
	}
	if f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, settlement.WorkerRunID) != 0 {
		t.Fatal("failed outbox leaked successor")
	}
	first, err := store.QueuedTurnStore.ContinueSettlementQueuedTurn(f.ctx, settlement, r)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.QueuedTurnStore.ContinueSettlementQueuedTurn(f.ctx, settlement, r)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("lost phase advance replay=%+v err=%v first=%+v", replay, err, first)
	}
	stale := settlement
	stale.LeaseFence++
	if _, err := store.QueuedTurnStore.ContinueSettlementQueuedTurn(f.ctx, stale, r); err == nil {
		t.Fatal("stored successor bypassed current lease")
	}
	if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID)); n != 1 {
		t.Fatalf("successors=%d", n)
	}
	assertSettlementProgressFrozen(t, f, settlement)
	close(store.resume)
	if err := supplementReceive(t, f.ctx, store.result); err != nil {
		n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, string(turn.start.Envelope.WorkerRunID))
		if !errors.Is(err, platformpostgres.ErrRevisionConflict) || n != 0 {
			t.Fatalf("unexpected continuation failure=%v successors=%d", err, n)
		}
		t.Fatalf("continuation rejected authenticated Runtime progress: %v; successors=%d", err, n)
	}
	next, _ := f.next()
	f.assertContinuation(turn, next, queueID)
}

func (s *progressPublicationStore) PublishTurnEvidence(ctx context.Context, r artifactapp.PublishTurnEvidenceRecord) (artifactapp.PublishedTurnEvidence, error) {
	s.ready <- r
	select {
	case <-s.resume:
	case <-ctx.Done():
		return artifactapp.PublishedTurnEvidence{}, ctx.Err()
	}
	v, err := s.MetadataStore.PublishTurnEvidence(ctx, r)
	s.result <- err
	return v, err
}

func (s *progressPublicationStore) PublishSettlementEvidence(ctx context.Context, settlement workerapp.TurnSettlement, r artifactapp.PublishTurnEvidenceRecord) (artifactapp.PublishedTurnEvidence, error) {
	s.ready <- r
	select {
	case <-s.resume:
	case <-ctx.Done():
		return artifactapp.PublishedTurnEvidence{}, ctx.Err()
	}
	v, err := s.MetadataStore.PublishSettlementEvidence(ctx, settlement, r)
	s.result <- err
	return v, err
}

func TestSettlementPublicationRuntimeProgress(t *testing.T) {
	var store *progressPublicationStore
	f := newSupplementFixture(t, func(f *supplementFixture) {
		f.runtime.heartbeatStart = make(chan struct{})
		store = &progressPublicationStore{MetadataStore: artifactpostgres.NewMetadataStore(f.pool), ready: make(chan artifactapp.PublishTurnEvidenceRecord, 10), resume: make(chan struct{}), result: make(chan error, 10)}
		f.app.runtime.publisher.Metadata = store
	})
	createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "settlement-progress", "Publish evidence after authenticated heartbeat")
	turn, callback := f.next()
	f.finish(turn, callback)
	r := supplementReceive(t, f.ctx, store.ready)
	e := turn.start.Envelope
	e.MessageID, e.IdempotencyKey = "settlement-progress-heartbeat", "settlement-progress-heartbeat"
	e.Kind, e.MessageType, e.AgentTurnID = "heartbeat", "runtime.heartbeat", "runtime-heartbeat"
	e.Payload = []byte(`{"heartbeat_sequence":1}`)
	if err := turn.client.TryPublishHeartbeat(workerapp.WorkerControlEvent{Envelope: e}); err != nil {
		t.Fatal(err)
	}
	for {
		a := supplementReceive(t, f.ctx, f.journal.accepted)
		if a.Message.MessageID == e.MessageID {
			break
		}
	}
	var revision uint64
	if err := f.pool.QueryRow(f.ctx, `SELECT revision FROM runtime_instances WHERE runtime_instance_id=$1`, string(e.RuntimeInstanceID)).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision <= r.Command.ExpectedRuntimeRevision {
		t.Fatalf("heartbeat did not advance Runtime: frozen=%d current=%d", r.Command.ExpectedRuntimeRevision, revision)
	}
	settlement, err := f.app.runtime.settlements.Load(f.ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MetadataStore.PublishTurnEvidence(f.ctx, r); !errors.Is(err, artifactpostgres.ErrEvidenceTargetStale) {
		t.Fatalf("generic publication accepted stale Runtime: %v", err)
	}
	assertSettlementProgressFencing(t, f, settlement, func(s workerapp.TurnSettlement) error {
		_, err := store.MetadataStore.PublishSettlementEvidence(f.ctx, s, r)
		return err
	})
	wrong := r
	wrong.Command.Evidence.ManifestObject.Key += "-different"
	if _, err := store.MetadataStore.PublishSettlementEvidence(f.ctx, settlement, wrong); err == nil {
		t.Fatal("accepted mismatched frozen evidence")
	}
	rollback := r
	if err := f.pool.QueryRow(f.ctx, `SELECT event_id FROM outbox_events LIMIT 1`).Scan(&rollback.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MetadataStore.PublishSettlementEvidence(f.ctx, settlement, rollback); err == nil {
		t.Fatal("expected duplicate outbox failure")
	}
	if f.count(`SELECT count(*) FROM trace_manifests WHERE agent_turn_id=$1`, settlement.AgentTurnID) != 0 {
		t.Fatal("failed outbox leaked publication")
	}
	first, err := store.MetadataStore.PublishSettlementEvidence(f.ctx, settlement, r)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.MetadataStore.PublishSettlementEvidence(f.ctx, settlement, r)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("lost publication phase advance replay=%+v err=%v", replay, err)
	}
	stale := settlement
	stale.LeaseFence++
	if _, err := store.MetadataStore.PublishSettlementEvidence(f.ctx, stale, r); err == nil {
		t.Fatal("stored publication bypassed current lease")
	}
	if n := f.count(`SELECT count(*) FROM trace_manifests WHERE agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)); n != 1 {
		t.Fatalf("publications=%d", n)
	}
	if n := f.count(`SELECT count(*) FROM artifact_refs WHERE agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)); n != 2 {
		t.Fatalf("artifacts=%d", n)
	}
	assertSettlementProgressFrozen(t, f, settlement)
	close(store.resume)
	err = supplementReceive(t, f.ctx, store.result)
	if err != nil {
		var count int
		if queryErr := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM trace_manifests WHERE agent_turn_id=$1`, string(turn.start.Envelope.AgentTurnID)).Scan(&count); queryErr != nil {
			t.Fatal(queryErr)
		}
		if !errors.Is(err, artifactpostgres.ErrEvidenceTargetStale) || count != 0 {
			t.Fatalf("unexpected publication failure=%v count=%d", err, count)
		}
		t.Fatalf("publication rejected authenticated Runtime progress: %v; visible publications=%d", err, count)
	}
	select {
	case <-f.settlements.completed:
	case <-time.After(5 * time.Second):
		t.Fatal("settlement did not complete")
	}
}

func assertSettlementProgressFrozen(t *testing.T, f *supplementFixture, s workerapp.TurnSettlement) {
	t.Helper()
	current, err := f.app.runtime.settlements.Load(f.ctx, s.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.PhaseStateDigest != s.PhaseStateDigest || !reflect.DeepEqual(current.PhaseState, s.PhaseState) {
		t.Fatal("effect changed frozen checkpoint")
	}
}

func settlementEffectDatabaseSnapshot(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	// Isolated fixture schema: compare all business rows, not only row counts,
	// so an expired first write cannot leave queue outcomes or graph CAS behind.
	for _, table := range []string{"trace_manifests", "artifact_refs", "evidence_object_refs", "worker_runs", "agent_turns", "runtime_instances", "worker_sessions", "tasks", "worker_user_commands", "scheduled_commands", "outbox_events", "owner_stream_positions"} {
		var encoded string
		if err := pool.QueryRow(context.Background(), fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text),'[]'::jsonb)::text FROM %s AS row`, table)).Scan(&encoded); err != nil {
			t.Fatal(err)
		}
		snapshot[table] = encoded
	}
	return snapshot
}

func TestSettlementProgressLeaseReclaimSerializesEffects(t *testing.T) {
	for _, kind := range []string{"publication", "continuation"} {
		t.Run(kind, func(t *testing.T) {
			var publication *progressPublicationStore
			var queued *progressQueuedStore
			f := newSupplementFixture(t, func(f *supplementFixture) {
				f.runtime.heartbeatStart = make(chan struct{})
				if kind == "publication" {
					publication = &progressPublicationStore{MetadataStore: artifactpostgres.NewMetadataStore(f.pool), ready: make(chan artifactapp.PublishTurnEvidenceRecord, 10), resume: make(chan struct{}), result: make(chan error, 10)}
					f.app.runtime.publisher.Metadata = publication
				} else {
					queued = &progressQueuedStore{QueuedTurnStore: workerpostgres.NewQueuedTurnStore(f.pool), ready: make(chan workerapp.ContinueQueuedTurnRecord, 10), resume: make(chan struct{}), result: make(chan error, 10)}
					f.app.runtime.queued.Store = queued
				}
			})
			var turn observationFixtureTurn
			var callback supplementCallback
			if kind == "publication" {
				createRuntimeRunnerTask(t, f.app.Handler(), f.pool, "reclaim-publication", "Publish once")
				turn, callback = f.next()
			} else {
				turn, callback = f.standalone()
				f.queue(turn)
			}
			f.finish(turn, callback)
			ctx := context.Background()
			var effect func(workerapp.TurnSettlement) (any, error)
			if kind == "publication" {
				r := supplementReceive(t, f.ctx, publication.ready)
				effect = func(s workerapp.TurnSettlement) (any, error) {
					return publication.MetadataStore.PublishSettlementEvidence(ctx, s, r)
				}
			} else {
				r := supplementReceive(t, f.ctx, queued.ready)
				effect = func(s workerapp.TurnSettlement) (any, error) {
					return queued.QueuedTurnStore.ContinueSettlementQueuedTurn(ctx, s, r)
				}
			}
			// Stop the old coordinator while its effect is paused; persisted work is
			// then reclaimed by a different real settlement service owner.
			f.stop()
			service := workerapp.TurnSettlementService{Store: workerpostgres.NewTurnSettlementRepository(f.pool), Now: time.Now}
			old, err := service.Load(ctx, "turn-settlement/"+string(turn.start.Envelope.AgentTurnID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE worker_turn_settlements SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE settlement_id=$1`, old.SettlementID); err != nil {
				t.Fatal(err)
			}
			current, err := service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: old.SettlementID, ExpectedRevision: old.Revision, OwnerID: "reclaimed-effect-holder"})
			if err != nil {
				t.Fatal(err)
			}
			t.Run("first_write_owner_stream_expiry", func(t *testing.T) {
				cfg := f.pool.Config()
				name := "settlement-first-write-" + kind
				cfg.ConnConfig.RuntimeParams["application_name"] = name
				waitPool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer waitPool.Close()
				if publication != nil {
					publication.MetadataStore = artifactpostgres.NewMetadataStore(waitPool)
					defer func() { publication.MetadataStore = artifactpostgres.NewMetadataStore(f.pool) }()
				} else {
					queued.QueuedTurnStore = workerpostgres.NewQueuedTurnStore(waitPool)
					defer func() { queued.QueuedTurnStore = workerpostgres.NewQueuedTurnStore(f.pool) }()
				}
				before := settlementEffectDatabaseSnapshot(t, f.pool)
				var expires time.Time
				if err := f.pool.QueryRow(ctx, `UPDATE worker_turn_settlements SET lease_expires_at=clock_timestamp()+interval '1 second' WHERE settlement_id=$1 RETURNING lease_expires_at`, current.SettlementID).Scan(&expires); err != nil {
					t.Fatal(err)
				}
				blocker, err := f.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(ctx)
				var streamID string
				if err := blocker.QueryRow(ctx, `SELECT stream_id FROM owner_stream_positions WHERE stream_id=$1 FOR UPDATE`, "worker-session/"+current.WorkerSessionID).Scan(&streamID); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() { _, err := effect(current); result <- err }()
				deadline := time.Now().Add(3 * time.Second)
				for {
					var waiting bool
					if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%owner_stream_positions%')`, name).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("first effect did not reach owner-stream lock after business writes")
					}
					time.Sleep(5 * time.Millisecond)
				}
				for {
					var expired bool
					if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, expires).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if expired {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-result; !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
					t.Fatalf("first effect committed after lease expiry: %v", err)
				}
				if after := settlementEffectDatabaseSnapshot(t, f.pool); !reflect.DeepEqual(before, after) {
					t.Fatalf("expired first effect left partial metadata/graph/queue/deadline/outbox/stream changes: before=%v after=%v", before, after)
				}
			})
			current, err = service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: current.SettlementID, ExpectedRevision: current.Revision, OwnerID: "post-expiry-effect-holder"})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			type result struct {
				value any
				err   error
				old   bool
			}
			results := make(chan result, 3)
			for _, s := range []workerapp.TurnSettlement{old, current, current} {
				go func(s workerapp.TurnSettlement) {
					<-start
					v, err := effect(s)
					results <- result{v, err, s.LeaseFence == old.LeaseFence}
				}(s)
			}
			close(start)
			var first any
			for range 3 {
				select {
				case r := <-results:
					if r.old {
						if r.err == nil {
							t.Fatal("reclaimed holder committed effect")
						}
					} else {
						if r.err != nil {
							t.Fatal(r.err)
						}
						if first == nil {
							first = r.value
						} else if !reflect.DeepEqual(first, r.value) {
							t.Fatal("competing holders returned distinct effects")
						}
					}
				case <-time.After(5 * time.Second):
					t.Fatal("competing settlement effects did not finish")
				}
			}
			// A live lease can expire while a real transaction waits for the
			// Session graph lock, including during an idempotent replay.
			cfg := f.pool.Config()
			cfg.ConnConfig.RuntimeParams["application_name"] = "settlement-lease-wait-" + kind
			waitPool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer waitPool.Close()
			if publication != nil {
				publication.MetadataStore = artifactpostgres.NewMetadataStore(waitPool)
			} else {
				queued.QueuedTurnStore = workerpostgres.NewQueuedTurnStore(waitPool)
			}
			var expires time.Time
			if err := f.pool.QueryRow(ctx, `UPDATE worker_turn_settlements SET lease_expires_at=clock_timestamp()+interval '1 second' WHERE settlement_id=$1 RETURNING lease_expires_at`, current.SettlementID).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if _, err := blocker.Exec(ctx, `SELECT worker_session_id FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, current.WorkerSessionID); err != nil {
				t.Fatal(err)
			}
			waitResult := make(chan error, 1)
			go func() { _, err := effect(current); waitResult <- err }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock')`, "settlement-lease-wait-"+kind).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("effect did not wait on real graph lock")
				}
				time.Sleep(time.Millisecond * 5)
			}
			for {
				var now time.Time
				if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
					t.Fatal(err)
				}
				if !now.Before(expires) {
					break
				}
				time.Sleep(time.Millisecond * 10)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-waitResult; !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
				t.Fatalf("effect committed after lease expired while blocked: %v", err)
			}
			var count int
			query := `SELECT count(*) FROM trace_manifests WHERE agent_turn_id=$1`
			id := old.AgentTurnID
			if kind == "continuation" {
				query = `SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`
				id = old.WorkerRunID
			}
			if err := f.pool.QueryRow(ctx, query, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("effect count=%d err=%v", count, err)
			}
		})
	}
}

func assertSettlementProgressFencing(t *testing.T, f *supplementFixture, s workerapp.TurnSettlement, effect func(workerapp.TurnSettlement) error) {
	t.Helper()
	for _, test := range []struct {
		name   string
		mutate func(*workerapp.TurnSettlement)
	}{
		{"old_fence", func(s *workerapp.TurnSettlement) { s.LeaseFence++ }},
		{"old_owner", func(s *workerapp.TurnSettlement) { s.LeaseOwnerID = "another-holder" }},
		{"old_revision", func(s *workerapp.TurnSettlement) { s.Revision-- }},
		{"wrong_phase", func(s *workerapp.TurnSettlement) { s.Phase = workerapp.TurnSettlementCompleted }},
		{"wrong_digest", func(s *workerapp.TurnSettlement) { s.PhaseStateDigest = "sha256:wrong" }},
		{"cross_tenant", func(s *workerapp.TurnSettlement) { s.TenantID = "another-tenant" }},
		{"wrong_runtime", func(s *workerapp.TurnSettlement) { s.RuntimeInstanceID = "another-runtime" }},
		{"wrong_generation", func(s *workerapp.TurnSettlement) { s.RuntimeGeneration++ }},
		{"wrong_epoch", func(s *workerapp.TurnSettlement) { s.ChannelEpoch++ }},
		{"wrong_agent", func(s *workerapp.TurnSettlement) { s.AgentSessionID = "another-agent" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := s
			test.mutate(&changed)
			if err := effect(changed); err == nil {
				t.Fatal("invalid settlement authority accepted")
			}
		})
	}
	for _, test := range []struct {
		name, change, restore string
		id                    string
	}{
		{"session_revision", `UPDATE worker_sessions SET session_revision=session_revision+1 WHERE worker_session_id=$1`, `UPDATE worker_sessions SET session_revision=session_revision-1 WHERE worker_session_id=$1`, s.WorkerSessionID},
		{"run_revision", `UPDATE worker_runs SET revision=revision+1 WHERE worker_run_id=$1`, `UPDATE worker_runs SET revision=revision-1 WHERE worker_run_id=$1`, s.WorkerRunID},
		{"turn_revision", `UPDATE agent_turns SET revision=revision+1 WHERE agent_turn_id=$1`, `UPDATE agent_turns SET revision=revision-1 WHERE agent_turn_id=$1`, s.AgentTurnID},
		{"runtime_epoch", `UPDATE runtime_instances SET control_channel_epoch=control_channel_epoch+1 WHERE runtime_instance_id=$1`, `UPDATE runtime_instances SET control_channel_epoch=control_channel_epoch-1 WHERE runtime_instance_id=$1`, s.RuntimeInstanceID},
		{"runtime_generation", `UPDATE runtime_instances SET generation=generation+1 WHERE runtime_instance_id=$1`, `UPDATE runtime_instances SET generation=generation-1 WHERE runtime_instance_id=$1`, s.RuntimeInstanceID},
		{"runtime_draining", `UPDATE runtime_instances SET status='draining' WHERE runtime_instance_id=$1`, `UPDATE runtime_instances SET status='active' WHERE runtime_instance_id=$1`, s.RuntimeInstanceID},
		{"channel_fenced", `UPDATE runtime_control_channels SET status='fenced',fenced_at=updated_at,fence_revision=revision,fence_owner_fence=owner_fence WHERE runtime_instance_id=$1`, `UPDATE runtime_control_channels SET status='connected',fenced_at=NULL,fence_revision=NULL,fence_owner_fence=NULL WHERE runtime_instance_id=$1`, s.RuntimeInstanceID},
		{"cross_workspace", `UPDATE worker_sessions SET workspace_id='different-workspace' WHERE worker_session_id=$1`, `UPDATE worker_sessions SET workspace_id='workspace-local' WHERE worker_session_id=$1`, s.WorkerSessionID},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Isolated fixture; state injection models conflicting owners, and is
			// restored before the coordinator is released.
			var workspace string
			if test.name == "cross_workspace" {
				if err := f.pool.QueryRow(f.ctx, `SELECT workspace_id FROM worker_sessions WHERE worker_session_id=$1`, s.WorkerSessionID).Scan(&workspace); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pool.Exec(f.ctx, test.change, test.id); err != nil {
				t.Fatal(err)
			}
			err := effect(s)
			if test.name == "cross_workspace" {
				_, restoreErr := f.pool.Exec(f.ctx, `UPDATE worker_sessions SET workspace_id=$2 WHERE worker_session_id=$1`, test.id, workspace)
				if restoreErr != nil {
					t.Fatal(restoreErr)
				}
			} else if _, restoreErr := f.pool.Exec(f.ctx, test.restore, test.id); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if err == nil {
				t.Fatal("changed graph accepted")
			}
		})
	}
	t.Run("runtime_revision_rewind", func(t *testing.T) {
		var revision uint64
		if err := f.pool.QueryRow(f.ctx, `SELECT revision FROM runtime_instances WHERE runtime_instance_id=$1`, s.RuntimeInstanceID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE runtime_instances SET revision=1 WHERE runtime_instance_id=$1`, s.RuntimeInstanceID); err != nil {
			t.Fatal(err)
		}
		err := effect(s)
		if _, restoreErr := f.pool.Exec(f.ctx, `UPDATE runtime_instances SET revision=$2 WHERE runtime_instance_id=$1`, s.RuntimeInstanceID, revision); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if err == nil {
			t.Fatal("Runtime revision rewind accepted")
		}
	})
	t.Run("expired_lease", func(t *testing.T) {
		if _, err := f.pool.Exec(f.ctx, `UPDATE worker_turn_settlements SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE settlement_id=$1`, s.SettlementID); err != nil {
			t.Fatal(err)
		}
		err := effect(s)
		if _, restoreErr := f.pool.Exec(f.ctx, `UPDATE worker_turn_settlements SET lease_expires_at=$2 WHERE settlement_id=$1`, s.SettlementID, s.LeaseExpiresAt); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
			t.Fatalf("expired lease=%v", err)
		}
	})
	t.Run("incomplete_legacy_checkpoint", func(t *testing.T) {
		var checkpoint map[string]json.RawMessage
		if err := json.Unmarshal(s.PhaseState, &checkpoint); err != nil {
			t.Fatal(err)
		}
		delete(checkpoint, "evidence_input")
		encoded, _ := json.Marshal(checkpoint)
		encoded, digest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(f.ctx, `UPDATE worker_turn_settlements SET phase_state=$2,phase_state_digest=$3 WHERE settlement_id=$1`, s.SettlementID, encoded, digest); err != nil {
			t.Fatal(err)
		}
		changed := s
		changed.PhaseState = encoded
		changed.PhaseStateDigest = digest
		err = effect(changed)
		if _, restoreErr := f.pool.Exec(f.ctx, `UPDATE worker_turn_settlements SET phase_state=$2,phase_state_digest=$3 WHERE settlement_id=$1`, s.SettlementID, s.PhaseState, s.PhaseStateDigest); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if err == nil {
			t.Fatal("incomplete legacy checkpoint accepted")
		}
	})
	if n := f.count(`SELECT count(*) FROM worker_runs WHERE previous_worker_run_id=$1`, s.WorkerRunID); n != 0 {
		t.Fatalf("rejection leaked successors=%d", n)
	}
	if s.Phase == workerapp.TurnSettlementDomainCommitted && f.count(`SELECT count(*) FROM trace_manifests WHERE agent_turn_id=$1`, s.AgentTurnID) != 0 {
		t.Fatal("rejection leaked publication")
	}
}
