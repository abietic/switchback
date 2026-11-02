//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	lf "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgconn"
)

type historicalPreparer struct {
	calls       int
	unavailable bool
}

// Advance real published evidence and the real Task delivery to terminal Apply.
// No business row reset or forged applied journal/settlement row is used here.
func historicalTerminalApply(t *testing.T, f *fragmentFixture) workerapp.ApplyRuntimeControlEventCommand {
	t.Helper()
	ctx := context.Background()
	var checkpoint map[string]json.RawMessage
	if err := json.Unmarshal(f.current.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	artifactIDs := make([]string, 0, len(f.finalized.ArtifactRefs))
	for _, ref := range f.finalized.ArtifactRefs {
		artifactIDs = append(artifactIDs, string(ref.ArtifactID))
	}
	checkpoint["publication"] = fragmentJSON(t, map[string]any{"trace_stream_id": f.finalized.Manifest.TraceStreamID, "artifact_ids": artifactIDs})
	advance := func(next workerapp.TurnSettlementPhase) {
		var err error
		f.current, err = f.settlements.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, FromPhase: f.current.Phase, ToPhase: next, Checkpoint: fragmentJSON(t, checkpoint)})
		if err != nil {
			t.Fatal(err)
		}
	}
	advance(workerapp.TurnSettlementEvidencePublished)
	var revision uint64
	if err := f.pool.QueryRow(ctx, `SELECT revision FROM tasks WHERE task_id='task-01'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	delivered, err := (taskapp.TaskDeliveryService{Store: taskpostgres.NewDeliveryStore(f.pool), IDs: f.ids, Now: func() time.Time { return f.now }}).RecordStageResult(ctx, taskapp.RecordStageResultCommand{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, TaskID: "task-01", WorkerSessionID: f.started.WorkerSessionID, WorkerRunID: f.started.WorkerRunID, ExpectedTaskRevision: revision, ArtifactRefs: f.finalized.ArtifactRefs, IdempotencyKey: "history-terminal-delivery"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint["task_plan"] = fragmentJSON(t, map[string]any{"action": "record_stage_result", "task_id": "task-01", "expected_task_revision": revision, "idempotency_key": "history-terminal-delivery"})
	checkpoint["task_result"] = fragmentJSON(t, map[string]any{"status": "applied", "revision": delivered.Revision, "event_id": delivered.EventID})
	advance(workerapp.TurnSettlementTaskSettled)
	return workerapp.ApplyRuntimeControlEventCommand{Authority: f.authority, Sequence: 1, MessageID: "terminal", IdempotencyKey: "terminal-apply", Outcome: json.RawMessage(`{"effect_disposition":"commit"}`), TurnSettlement: &workerapp.TurnSettlementApplyAuthority{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence}}
}

func TestFragmentHistoryDoesNotBlockTerminalApplyOrReplay(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "duplicate_replay"}[duplicate], func(t *testing.T) {
			f := newFragmentFixtureMode(t, false, true)
			f.publishCore(t)
			apply := historicalTerminalApply(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if duplicate {
				if _, err := f.journal.ApplyEvent(ctx, apply); err != nil {
					t.Fatal(err)
				}
			}
			blocker, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(ctx, `SELECT runtime_instance_id FROM runtime_instances WHERE runtime_instance_id=$1 FOR UPDATE`, f.started.RuntimeInstanceID); err != nil {
				t.Fatal(err)
			}
			type applyResult struct {
				value workerapp.RuntimeControlEventAcceptance
				err   error
			}
			applied := make(chan applyResult, 1)
			go func() { value, err := f.journal.ApplyEvent(ctx, apply); applied <- applyResult{value, err} }()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			var applyPID int32
			for applyPID == 0 {
				if err = f.pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, int32(blocker.Conn().PgConn().PID())).Scan(&applyPID); err != nil {
					t.Fatal(err)
				}
				if applyPID == 0 {
					select {
					case <-ticker.C:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}
			// PostgreSQL reports Apply waiting on Runtime. Prove it already owns
			// Session too, independently of the join's row-lock acquisition order.
			_, err = f.pool.Exec(ctx, `SELECT worker_session_id FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE NOWAIT`, f.started.WorkerSessionID)
			var busy *pgconn.PgError
			if !errors.As(err, &busy) || busy.Code != "55P03" {
				t.Fatalf("terminal Apply did not hold Session: %v", err)
			}
			h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
			r := historicalRequest(t, f, false)
			historyDone := make(chan error, 1)
			go func() { _, err := h.Request(ctx, r); historyDone <- err }()
			var historyErr error
			blocked := false
		waitHistory:
			for {
				select {
				case historyErr = <-historyDone:
					break waitHistory
				default:
				}
				if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, applyPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			// On the broken implementation this releases the last edge of the
			// actual history -> Session -> Apply -> settlement -> history cycle.
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if blocked {
				historyErr = <-historyDone
			}
			result := <-applied
			if blocked || result.err != nil {
				t.Fatalf("history waited on terminal owner: blocked=%v history=%v apply=%v", blocked, historyErr, result.err)
			}
			if !errors.As(historyErr, &busy) || busy.Code != "55P03" {
				t.Fatalf("history must fail promptly on owner contention: %v", historyErr)
			}
			if result.value.Message.Duplicate != duplicate {
				t.Fatalf("wrong terminal result: %+v", result.value)
			}
			assertCount(t, f.pool, "observation_candidates", 0)
			assertCount(t, f.pool, "observation_fragment_requests", 0)
			if _, err = h.Request(ctx, r); err != nil {
				t.Fatal(err)
			}
			if replay, err := f.journal.ApplyEvent(ctx, apply); err != nil || !replay.Message.Duplicate {
				t.Fatalf("terminal replay after history: %+v %v", replay, err)
			}
		})
	}
}

func TestFragmentHistoryDoesNotInvertSettlementDomainLocks(t *testing.T) {
	f := newFragmentFixtureMode(t, false, true)
	f.publishCore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `SELECT settlement_id FROM worker_turn_settlements WHERE settlement_id=$1 FOR UPDATE`, f.current.SettlementID); err != nil {
		t.Fatal(err)
	}
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
	r := historicalRequest(t, f, false)
	// The competing settlement UPDATE is held before Request starts. A real
	// lock-not-available response must roll history back without waiting for
	// this owner's Session lock; a context timeout is not a successful guard.
	requestCtx, requestCancel := context.WithTimeout(ctx, time.Second)
	defer requestCancel()
	_, err = h.Request(requestCtx, r)
	var busy *pgconn.PgError
	if !errors.As(err, &busy) || busy.Code != "55P03" {
		t.Fatalf("history must yield to settlement owner: %v", err)
	}
	_, err = blocker.Exec(ctx, `SELECT worker_session_id FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE NOWAIT`, f.started.WorkerSessionID)
	if err != nil {
		t.Fatalf("history held Session while waiting for settlement: %v", err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertCount(t, f.pool, "observation_candidates", 0)
	assertCount(t, f.pool, "observation_fragment_requests", 0)
	if _, err = h.Request(ctx, r); err != nil {
		t.Fatal(err)
	}
	replayed, err := (workerapp.TurnSettlementDomainService{Store: workerpostgres.NewTurnSettlementRepository(f.pool), Now: func() time.Time { return f.now }}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{SettlementID: f.current.SettlementID, ExpectedRevision: f.current.Revision - 1, LeaseOwnerID: f.current.LeaseOwnerID, LeaseFence: f.current.LeaseFence, Resident: f.stage})
	if err != nil || replayed.Proof != f.proof {
		t.Fatalf("history interfered with actual domain replay: %+v %v", replayed, err)
	}
}

func (p *historicalPreparer) PrepareHistoricalObservation(ctx context.Context, in artifactapp.HistoricalObservationCapture) artifactapp.PreparedObservation {
	p.calls++
	if p.unavailable {
		return artifactapp.PreparedObservation{ErrorCode: "scanner_unavailable"}
	}
	return (artifactapp.ObservationCapture{Compiler: lf.FragmentCompiler{}, Scanner: fragmentScanner{}, Scope: in.Scope}).Prepare(ctx, in.Frame, in.Evidence, in.Provenance)
}
func historicalRequest(t *testing.T, f *fragmentFixture, model bool) artifactapp.RequestHistoricalFragments {
	t.Helper()
	s := f.started
	r := artifactapp.RequestHistoricalFragments{ActorID: "user-local", Scope: artifactdomain.ObservationScope{TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo", WorkerSessionID: s.WorkerSessionID, WorkerRunID: s.WorkerRunID}, Version: fragmentVersion, Initial: artifactapp.HistoricalTurnRef{AgentTurnID: s.AgentTurnID, ManifestChecksum: f.finalized.ManifestObject.Checksum}, Target: artifactapp.HistoricalTurnRef{AgentTurnID: s.AgentTurnID, ManifestChecksum: f.finalized.ManifestObject.Checksum}, IdempotencyKey: "history-one"}
	if model {
		var m artifactapp.HistoricalModelRef
		if err := f.pool.QueryRow(context.Background(), `SELECT message_id,sequence,fingerprint FROM runtime_control_messages WHERE direction='event' AND envelope->>'message_type'='model.observation'`).Scan(&m.MessageID, &m.Sequence, &m.EnvelopeDigest); err != nil {
			t.Fatal(err)
		}
		r.Models = []artifactapp.HistoricalModelRef{m}
	}
	return r
}
func TestFragmentHistoryRecapturesExcludedExactCore(t *testing.T) {
	f := newFragmentFixtureMode(t, true, true)
	f.publishCore(t)
	ctx := context.Background()
	p := &historicalPreparer{}
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, p)
	r := historicalRequest(t, f, true)
	before := historicalCoreSnapshot(t, f)
	selected, err := h.Request(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if selected.RequestDigest == "" || p.calls != 3 {
		t.Fatalf("selection=%+v captures=%d", selected, p.calls)
	}
	p.unavailable = true
	again, err := h.Request(ctx, r)
	if err != nil || again != selected || p.calls != 3 {
		t.Fatalf("idempotent selection=%+v err=%v captures=%d", again, err, p.calls)
	}
	ledger := artifactpostgres.NewObservationLedger(f.pool)
	publisher := artifactapp.FragmentPublisher{Store: ledger, Objects: f.objects}
	for range 4 {
		if err := publisher.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var published int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_publications`).Scan(&published); err != nil || published != 3 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	if !bytes.Equal(before, historicalCoreSnapshot(t, f)) {
		t.Fatal("history changed original core bytes")
	}
	for _, change := range []func(*artifactapp.RequestHistoricalFragments){
		func(r *artifactapp.RequestHistoricalFragments) { r.Models = nil },
		func(r *artifactapp.RequestHistoricalFragments) { r.ActorID = "other"; r.Scope.UserID = "other" },
		func(r *artifactapp.RequestHistoricalFragments) { r.Scope.WorkspaceID = "other" },
		func(r *artifactapp.RequestHistoricalFragments) { r.Version = "langfuse.projection.v2" },
	} {
		changed := r
		change(&changed)
		if _, err := h.Request(ctx, changed); err == nil {
			t.Fatal("changed same-key selection accepted")
		}
	}
}

func historicalCoreSnapshot(t *testing.T, f *fragmentFixture) []byte {
	t.Helper()
	var raw []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('settlement',(SELECT jsonb_agg(to_jsonb(o)) FROM worker_turn_settlements o),'manifest',(SELECT jsonb_agg(to_jsonb(m)) FROM trace_manifests m),'artifacts',(SELECT jsonb_agg(to_jsonb(a)) FROM artifact_refs a),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY direction,sequence) FROM runtime_control_messages e),'exclusion',(SELECT jsonb_agg(to_jsonb(x)) FROM observation_fragment_run_exclusions x))`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFragmentHistoryDistinctInitialAndTargetKeepExactParents(t *testing.T) {
	first := newFragmentFixtureMode(t, false, true)
	first.publishCore(t)
	ctx := context.Background()
	h := artifactpostgres.NewFragmentHistoryStore(first.pool, "tenant-local", "user-local", first.objects, &historicalPreparer{})
	initialRequest := historicalRequest(t, first, false)
	if _, err := h.Request(ctx, initialRequest); err != nil {
		t.Fatal(err)
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
	initialTurnRef := fragmentRef(t, first.pool, "turn_basis")
	originalTurn, err := ledger.ReadFragmentEvidence(ctx, initialTurnRef)
	if err != nil {
		t.Fatal(err)
	}
	// Finish the first journal settlement through its real owners so the second
	// exact start/model facts can be reserved and applied normally.
	apply := historicalTerminalApply(t, first)
	if _, err = first.journal.ApplyEvent(ctx, apply); err != nil {
		t.Fatal(err)
	}
	first.current, err = first.settlements.Load(ctx, first.current.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]json.RawMessage
	if err = json.Unmarshal(first.current.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint["continuation_plan"] = json.RawMessage(`{"action":"none","reason":"no_queued_turn"}`)
	checkpoint["continuation_result"] = json.RawMessage(`{"status":"not_applicable"}`)
	for _, phase := range []workerapp.TurnSettlementPhase{workerapp.TurnSettlementContinuationSettled, workerapp.TurnSettlementCompleted} {
		first.current, err = first.settlements.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{SettlementID: first.current.SettlementID, ExpectedRevision: first.current.Revision, LeaseOwnerID: first.current.LeaseOwnerID, LeaseFence: first.current.LeaseFence, FromPhase: first.current.Phase, ToPhase: phase, Checkpoint: fragmentJSON(t, checkpoint)})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Reuse Task6's historical terminal-row seed, not a user queue/continuation
	// claim. No existing business row is reset. Both models use real journal I/O.
	f := seedFragmentLaterTurn(t, first)
	f.journal.Now = func() time.Time { return f.now }
	f.settlements.Now = func() time.Time { return f.now }
	start := f.envelope("command", 0, "later-start", "agent_turn.start", json.RawMessage(`{}`))
	start.SentAt = time.Time{}
	f.start, err = f.journal.ReserveCommand(ctx, workerapp.ReserveRuntimeControlCommandCommand{Authority: f.authority, MessageID: start.MessageID, IdempotencyKey: start.IdempotencyKey, Envelope: fragmentJSON(t, start)})
	if err != nil {
		t.Fatal(err)
	}
	// The terminal-row fixture also needs its historical start-effect fact.
	// ReconcileEffect rightly refuses to start a new Turn in a completed Run;
	// seed only this newly reserved command's terminal status, never reset the
	// Run/Session to manufacture a live continuation.
	if _, err = f.pool.Exec(ctx, `UPDATE runtime_control_messages SET effect_status='terminal',applied_at=$3 WHERE runtime_instance_id=$1 AND direction='command' AND message_id=$2`, f.started.RuntimeInstanceID, f.start.MessageID, f.now); err != nil {
		t.Fatal(err)
	}
	f.captureModel(t, 2, "target-selected")
	f.captureModel(t, 3, "target-unselected")
	e := f.envelope("event", 4, "later-terminal", "agent_turn.terminal", json.RawMessage(`{}`))
	e.AckSequence = f.start.Sequence
	if _, err = f.journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: f.authority, Sequence: e.Sequence, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: fragmentJSON(t, e)}); err != nil {
		t.Fatal(err)
	}
	id, _ := workerapp.TurnSettlementID(f.started.AgentTurnID)
	f.current, err = f.settlements.Claim(ctx, workerapp.ClaimTurnSettlementCommand{SettlementID: id, ExpectedRevision: 1, OwnerID: "later-owner"})
	if err != nil {
		t.Fatal(err)
	}
	f.freezeAndFinalize(t)
	f.publishCore(t)
	before := historicalCoreSnapshot(t, f)
	r := initialRequest
	r.IdempotencyKey = "history-distinct-target"
	r.Target = artifactapp.HistoricalTurnRef{AgentTurnID: f.started.AgentTurnID, ManifestChecksum: f.finalized.ManifestObject.Checksum}
	var selectedModel artifactapp.HistoricalModelRef
	if err = f.pool.QueryRow(ctx, `SELECT message_id,sequence,fingerprint FROM runtime_control_messages WHERE direction='event' AND message_id='target-selected-event'`).Scan(&selectedModel.MessageID, &selectedModel.Sequence, &selectedModel.EnvelopeDigest); err != nil {
		t.Fatal(err)
	}
	r.Models = []artifactapp.HistoricalModelRef{selectedModel}
	if r.Initial == r.Target || r.Initial.ManifestChecksum == r.Target.ManifestChecksum {
		t.Fatal("fixture did not provide distinct exact manifests")
	}
	if _, err = h.Request(ctx, r); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err = publisher.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	modelRef := fragmentRef(t, f.pool, "model")
	loaded, err := (artifactapp.FragmentLoader{Metadata: ledger, Objects: f.objects}).Load(ctx, artifactapp.FragmentJob{Source: modelRef, Version: fragmentVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Frames) != 3 {
		t.Fatalf("model parent closure = %d", len(loaded.Frames))
	}
	seen := map[artifactdomain.FragmentKind]bool{}
	for _, raw := range loaded.Frames {
		frame, err := artifactdomain.DecodeObservationFragment(raw)
		if err != nil {
			t.Fatal(err)
		}
		seen[frame.Kind] = true
		if frame.Scope != r.Scope {
			t.Fatalf("wrong scope: %+v", frame.Scope)
		}
		if frame.Kind == artifactdomain.FragmentRunBasis {
			if frame.InitialTurnID != first.started.AgentTurnID || !artifactapp.ProjectionJSONEqual([]byte(frame.InputJSON), []byte(`{"text":"execute task","n":9007199254740993}`)) {
				t.Fatalf("Run lost original input: %+v", frame)
			}
		} else if frame.Turn == nil || string(frame.Turn.AgentTurnID) != f.started.AgentTurnID {
			t.Fatalf("model/Turn parent did not stay target-bound: %+v", frame)
		} else if frame.Kind == artifactdomain.FragmentTurnBasis && !artifactapp.ProjectionJSONEqual([]byte(frame.InputJSON), []byte(`{"text":"second input"}`)) {
			t.Fatal("target input replaced by initial input")
		} else if frame.Kind == artifactdomain.FragmentModel && frame.ObservationID != "target-selected" {
			t.Fatal("wrong target model selected")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("missing distinct parent kinds: %v", seen)
	}
	nowRun, err := ledger.ReadFragmentEvidence(ctx, runRef)
	if err != nil {
		t.Fatal(err)
	}
	nowTurn, err := ledger.ReadFragmentEvidence(ctx, initialTurnRef)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(originalRun, nowRun) || !reflect.DeepEqual(originalTurn, nowTurn) {
		t.Fatal("history rewrote initial evidence or old bytes")
	}
	if _, err = (artifactapp.FragmentLoader{Metadata: ledger, Objects: f.objects}).Load(ctx, artifactapp.FragmentJob{Source: initialTurnRef, Version: fragmentVersion}); err != nil {
		t.Fatalf("original Turn object bytes no longer match retained proof: %v", err)
	}
	if !strings.Contains(nowRun.Object.Key, "/turns/"+first.started.AgentTurnID+"/") {
		t.Fatal("Run object no longer initial-bound")
	}
	modelEvidence, err := ledger.ReadFragmentEvidence(ctx, modelRef)
	if err != nil {
		t.Fatal(err)
	}
	targetEvidence, err := ledger.ReadFragmentParent(ctx, modelRef, "turn_basis")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(modelEvidence.Object.Key, "/turns/"+f.started.AgentTurnID+"/") || !strings.Contains(targetEvidence.Object.Key, "/turns/"+f.started.AgentTurnID+"/") {
		t.Fatal("target object identity changed")
	}
	if targetEvidence.Core.ManifestObject != f.finalized.ManifestObject || nowRun.Core.ManifestObject != first.finalized.ManifestObject {
		t.Fatal("initial/target manifest bindings crossed")
	}
	exactMembers := []map[string]string{}
	for _, ref := range []artifactapp.FragmentRef{runRef, targetEvidence.Source, modelRef} {
		exactMembers = append(exactMembers, map[string]string{"kind": ref.Kind, "source_id": ref.SourceID, "frame_digest": ref.FrameDigest})
	}
	var exact bool
	if err = f.pool.QueryRow(ctx, `SELECT jsonb_array_length(source_set)=3 AND source_set @> $2::jsonb FROM observation_fragment_requests WHERE idempotency_key=$1`, r.IdempotencyKey, fragmentJSON(t, exactMembers)).Scan(&exact); err != nil || !exact {
		t.Fatalf("distinct request did not bind exact parent/model membership: %v %v", exact, err)
	}
	if !bytes.Equal(before, historicalCoreSnapshot(t, f)) {
		t.Fatal("history changed immutable core")
	}
	coverage, err := artifactpostgres.NewFragmentCoverageStore(f.pool, "tenant-local", "user-local").Read(ctx, r.ActorID, r.Scope, r.Version)
	if err != nil || len(coverage.Sources) != 4 || coverage.Accepted != 4 || coverage.Published != 4 {
		t.Fatalf("selected coverage=%+v err=%v", coverage, err)
	}
	if count, err := artifactpostgres.NewFragmentJobStore(f.pool).Discover(ctx, r.Version, 10); err != nil || count != 4 {
		t.Fatalf("unselected target model leaked: count=%d err=%v", count, err)
	}
	assertCount(t, f.pool, "observation_candidates", 4)
	assertCount(t, f.pool, "observation_publications", 4)
}

func TestFragmentHistoryUnavailableRegistryDoesNotSealGap(t *testing.T) {
	f := newFragmentFixtureMode(t, true, true)
	f.publishCore(t)
	p := &historicalPreparer{unavailable: true}
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, p)
	r := historicalRequest(t, f, true)
	if _, err := h.Request(context.Background(), r); err == nil {
		t.Fatal("missing original registry accepted")
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM observation_candidates)+(SELECT count(*) FROM observation_fragment_requests)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed capture left evidence count=%d err=%v", count, err)
	}
	p.unavailable = false
	if _, err := h.Request(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestFragmentHistoryRejectsSnapshotChangedDuringCapture(t *testing.T) {
	f := newFragmentFixtureMode(t, false, true)
	f.publishCore(t)
	p := historicalPrepareFunc(func(ctx context.Context, in artifactapp.HistoricalObservationCapture) artifactapp.PreparedObservation {
		if _, err := f.pool.Exec(ctx, `UPDATE worker_turn_settlements SET phase_state=phase_state||'{"historical_test_extension":true}'::jsonb`); err != nil {
			t.Fatal(err)
		}
		return (&historicalPreparer{}).PrepareHistoricalObservation(ctx, in)
	})
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, p)
	if _, err := h.Request(context.Background(), historicalRequest(t, f, false)); err == nil {
		t.Fatal("changed capture snapshot accepted")
	}
}

type historicalPrepareFunc func(context.Context, artifactapp.HistoricalObservationCapture) artifactapp.PreparedObservation

func (f historicalPrepareFunc) PrepareHistoricalObservation(ctx context.Context, in artifactapp.HistoricalObservationCapture) artifactapp.PreparedObservation {
	return f(ctx, in)
}

func TestFragmentHistoryExistingProofDoesNotNeedRegistry(t *testing.T) {
	f := newFragmentFixture(t, true)
	f.publishCore(t)
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", nil, nil)
	if _, err := h.Request(context.Background(), historicalRequest(t, f, true)); err != nil {
		t.Fatal(err)
	}
}

func TestFragmentHistoryHistoricalAttestationSurvivesCheckpointExtension(t *testing.T) {
	f := newFragmentFixtureMode(t, false, true)
	f.publishCore(t)
	ctx := context.Background()
	h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
	if _, err := h.Request(ctx, historicalRequest(t, f, false)); err != nil {
		t.Fatal(err)
	}
	var phase map[string]any
	if err := json.Unmarshal(f.current.PhaseState, &phase); err != nil {
		t.Fatal(err)
	}
	phase["historical_test_extension"] = true
	checkpoint, digest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(fragmentJSON(t, phase))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE worker_turn_settlements SET phase_state=$1,phase_state_digest=$2`, checkpoint, digest); err != nil {
		t.Fatal(err)
	}
	publisher := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
	for range 2 {
		if err = publisher.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_publications`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("extension invalidated historical capture count=%d err=%v", count, err)
	}
}

func TestFragmentHistoryRejectsIgnoredAndMissingApplicableProof(t *testing.T) {
	for _, mode := range []string{"ignored", "missing_applicable_proof", "wrong_manifest", "wrong_envelope"} {
		t.Run(mode, func(t *testing.T) {
			f := newFragmentFixtureMode(t, true, true)
			f.publishCore(t)
			ctx := context.Background()
			r := historicalRequest(t, f, true)
			switch mode {
			case "ignored":
				// Persisted ignored outcome fixture. The request must never rewrite it.
				if _, err := f.pool.Exec(ctx, `UPDATE runtime_control_messages SET outcome='{"status":"model_observation_ignored"}',apply_outcome_digest=$1 WHERE direction='event' AND message_id=$2`, artifactdomain.Checksum([]byte(`{"status":"model_observation_ignored"}`)), r.Models[0].MessageID); err != nil {
					t.Fatal(err)
				}
			case "missing_applicable_proof":
				// Explicit integrity-corruption fixture, isolated to this test schema.
				if _, err := f.pool.Exec(ctx, `ALTER TABLE observation_fragment_run_exclusions DISABLE TRIGGER observation_fragment_run_exclusions_immutable; DELETE FROM observation_fragment_run_exclusions; ALTER TABLE observation_fragment_run_exclusions ENABLE TRIGGER observation_fragment_run_exclusions_immutable`); err != nil {
					t.Fatal(err)
				}
			case "wrong_manifest":
				r.Target.ManifestChecksum = artifactdomain.Checksum([]byte("wrong manifest"))
				r.Initial = r.Target
			case "wrong_envelope":
				r.Models[0].EnvelopeDigest = artifactdomain.Checksum([]byte("wrong envelope"))
			}
			before := historicalCoreSnapshot(t, f)
			h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
			if _, err := h.Request(ctx, r); err == nil {
				t.Fatal("invalid historical source accepted")
			}
			if !bytes.Equal(before, historicalCoreSnapshot(t, f)) {
				t.Fatal("request rewrote authoritative outcome")
			}
		})
	}
}

func TestFragmentHistoryForgedOrUnselectedAttestationCannotPublish(t *testing.T) {
	for _, mode := range []string{"forged_provenance", "bare_candidate", "unselected_model"} {
		t.Run(mode, func(t *testing.T) {
			f := newFragmentFixtureMode(t, true, true)
			f.publishCore(t)
			ctx := context.Background()
			h := artifactpostgres.NewFragmentHistoryStore(f.pool, "tenant-local", "user-local", f.objects, &historicalPreparer{})
			r := historicalRequest(t, f, true)
			if _, err := h.Request(ctx, r); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "forged_provenance":
				if _, err := f.pool.Exec(ctx, `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable; UPDATE observation_candidates SET provenance=jsonb_set(provenance,'{request_digest}','"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"'); SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE observation_candidates ENABLE TRIGGER observation_candidates_immutable`); err != nil {
					t.Fatal(err)
				}
			case "bare_candidate":
				if _, err := f.pool.Exec(ctx, `ALTER TABLE observation_fragment_requests DISABLE TRIGGER observation_fragment_requests_immutable; DELETE FROM observation_fragment_requests; ALTER TABLE observation_fragment_requests ENABLE TRIGGER observation_fragment_requests_immutable`); err != nil {
					t.Fatal(err)
				}
			case "unselected_model":
				if _, err := f.pool.Exec(ctx, `ALTER TABLE observation_fragment_requests DISABLE TRIGGER observation_fragment_requests_immutable; UPDATE observation_fragment_requests SET source_set=(SELECT jsonb_agg(x) FROM jsonb_array_elements(source_set) x WHERE x->>'kind'<>'model'); ALTER TABLE observation_fragment_requests ENABLE TRIGGER observation_fragment_requests_immutable`); err != nil {
					t.Fatal(err)
				}
			}
			p := artifactapp.FragmentPublisher{Store: artifactpostgres.NewObservationLedger(f.pool), Objects: f.objects}
			for range 4 {
				if err := p.Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_publications WHERE kind='model'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid attestation published model=%d err=%v", count, err)
			}
		})
	}
}
