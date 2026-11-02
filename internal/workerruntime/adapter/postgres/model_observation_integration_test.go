//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactpostgres "github.com/abietic/switchback/internal/artifacttrace/adapter/postgres"
	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestModelObservationFenceAfterScanBeforeApplyRejectsReceipt(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	ctx := context.Background()
	// Fixture preparation has already acquired the original scanner and sealed
	// its bounded result. A durable fence winning before Apply revokes authority.
	a := f.command.Authority
	if _, err := f.service.Fence(ctx, workerapp.FenceRuntimeControlChannelCommand{WorkerSessionID: a.WorkerSessionID, RuntimeInstanceID: a.RuntimeInstanceID, RuntimeGeneration: a.RuntimeGeneration, ChannelEpoch: a.ChannelEpoch}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyEvent(ctx, f.command); err == nil {
		t.Fatal("pre-fence scan granted post-fence Apply authority")
	}
	var count, cursor int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT event_applied_sequence FROM runtime_control_channels WHERE runtime_instance_id=$1`, a.RuntimeInstanceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if count != 0 || cursor != 0 {
		t.Fatalf("fence loser committed receipt/cursor: %d/%d", count, cursor)
	}
}

func TestModelObservationAcceptedReceiptSurvivesFenceAndRestart(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	ctx := context.Background()
	if _, err := f.service.ApplyEvent(ctx, f.command); err != nil {
		t.Fatal(err)
	}
	var receipt []byte
	if err := f.pool.QueryRow(ctx, `SELECT receipt FROM observation_candidates WHERE kind='model'`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	a := f.command.Authority
	if _, err := f.service.Fence(ctx, workerapp.FenceRuntimeControlChannelCommand{WorkerSessionID: a.WorkerSessionID, RuntimeInstanceID: a.RuntimeInstanceID, RuntimeGeneration: a.RuntimeGeneration, ChannelEpoch: a.ChannelEpoch}); err != nil {
		t.Fatal(err)
	}
	// A newly constructed ledger needs no Hub/scanner/provider to consume the
	// already accepted candidate. Receipt bytes are never re-signed at restart.
	candidate, found, err := artifactpostgres.NewObservationLedger(f.pool).ClaimCandidate(ctx, time.Minute)
	if err != nil || !found || !bytes.Equal(candidate.Receipt, receipt) {
		t.Fatalf("restart lost immutable receipt: found=%v err=%v", found, err)
	}
}

type modelPGFixture struct {
	pool     *pgxpool.Pool
	service  workerapp.RuntimeControlJournalService
	command  workerapp.ApplyRuntimeControlEventCommand
	started  workerapp.StartedWorkerRun
	envelope contractsv1.RuntimeControlEnvelope
}

func newModelPGFixture(t *testing.T, historicalStart bool, participant workerpostgres.ObservationCommitParticipant) modelPGFixture {
	t.Helper()
	pool := postgrestest.Open(t)
	ctx := context.Background()
	if err := artifactpostgres.NewFragmentJobStore(pool).Activate(ctx, "langfuse.projection.v3"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	store, err := workerpostgres.NewRuntimeControlJournalWithObservation(pool, participant)
	if err != nil {
		t.Fatal(err)
	}
	service := workerapp.RuntimeControlJournalService{Store: store, Now: func() time.Time { return now.Add(6 * time.Minute) }}
	if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(ctx, runtimeControlClaim(started, "process-1", 1, "backend-1"))
	if err != nil {
		t.Fatal(err)
	}
	authority := authorityFrom(completeRuntimeControlClaim(t, service, plan))
	if historicalStart {
		start, err := service.ReserveCommand(ctx, workerapp.ReserveRuntimeControlCommandCommand{Authority: authority, MessageID: "model-start", IdempotencyKey: "model-start", Envelope: runtimeControlEnvelope(started, "command", 0, "model-start", "model-start", "agent_turn.start")})
		if err != nil {
			t.Fatal(err)
		}
		proof := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
		if _, err := service.ReconcileEffect(ctx, workerapp.ReconcileRuntimeControlEffectCommand{Authority: authority, CommandSequence: start.Sequence, MessageID: start.MessageID, AgentTurnID: started.AgentTurnID, Phase: workerapp.RuntimeControlEffectActive, ActiveTurn: &proof}); err != nil {
			t.Fatal(err)
		}
	}
	e := decodeRuntimeControlEnvelope(t, runtimeControlEnvelope(started, "event", 1, "model-event", "model-event", "model.observation"))
	if historicalStart {
		e.AckSequence = 1
	}
	m := workerapp.ModelObservation{ObservationID: "observation-1", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeID: started.RuntimeInstanceID, Generation: started.RuntimeGeneration, Model: "fixture-model", Route: "fixture-route", Input: json.RawMessage(`{"n":9007199254740993}`), Output: json.RawMessage(`{"answer":"fixture"}`), StartedAt: now, EndedAt: now.Add(time.Second), Status: "ok"}
	e.Payload, _ = json.Marshal(m)
	encoded, _ := json.Marshal(e)
	if _, err := service.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: authority, Sequence: 1, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: encoded}); err != nil {
		t.Fatal(err)
	}
	var scope artifactdomain.ObservationScope
	if err := pool.QueryRow(ctx, `SELECT s.tenant_id,s.user_id,s.workspace_id,s.worker_session_id,r.worker_run_id FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) WHERE r.worker_run_id=$1`, started.WorkerRunID).Scan(&scope.TenantID, &scope.UserID, &scope.WorkspaceID, &scope.WorkerSessionID, &scope.WorkerRunID); err != nil {
		t.Fatal(err)
	}
	frame, err := artifactapp.ModelObservationFragment(scope, e)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := artifactdomain.EncodeObservationFragment(frame)
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "fixture", Value: "fixture-secret-never-in-content"}})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	prepared := (artifactapp.ObservationCapture{Compiler: artifactlangfuse.FragmentCompiler{}, Scanner: scanner, Scope: artifactapp.ObservationScanScope{WorkerSessionID: scope.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: started.RuntimeGeneration}}).Prepare(ctx, raw, e, artifactapp.ObservationProvenance{EnvelopeDigest: digest})
	if prepared.ErrorCode != "" {
		t.Fatal(prepared.ErrorCode)
	}
	sealed, err := artifactapp.EncodeSealedObservationBundle(artifactapp.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: []artifactapp.PreparedObservation{prepared}})
	if err != nil {
		t.Fatal(err)
	}
	return modelPGFixture{pool: pool, service: service, started: started, envelope: e, command: workerapp.ApplyRuntimeControlEventCommand{Authority: authority, Sequence: 1, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Outcome: json.RawMessage(`{"status":"model_observation_recorded"}`), ObservationCapture: &workerapp.RuntimeObservationCapture{Sealed: sealed}}}
}

type rollbackModelParticipant struct {
	artifactpostgres.ObservationCaptureStore
	fail bool
}

func (p *rollbackModelParticipant) CommitModelObservation(ctx context.Context, tx platformpostgres.Tx, event, start workerapp.RuntimeControlMessage, record workerapp.ApplyRuntimeControlEventRecord) error {
	if err := p.ObservationCaptureStore.CommitModelObservation(ctx, tx, event, start, record); err != nil {
		return err
	}
	if p.fail {
		return errors.New("injected receipt transaction failure")
	}
	return nil
}

func TestModelObservationApplyReceiptRollback(t *testing.T) {
	participant := &rollbackModelParticipant{fail: true}
	f := newModelPGFixture(t, true, participant)
	ctx := context.Background()
	if _, err := f.service.ApplyEvent(ctx, f.command); err == nil {
		t.Fatal("injected failure accepted")
	}
	var count, cursor int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT event_applied_sequence FROM runtime_control_channels WHERE runtime_instance_id=$1`, f.started.RuntimeInstanceID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if count != 0 || cursor != 0 {
		t.Fatalf("partial receipt/cursor commit %d/%d", count, cursor)
	}
	participant.fail = false
	got, err := f.service.ApplyEvent(ctx, f.command)
	if err != nil || got.Channel.EventAppliedSequence != 1 {
		t.Fatalf("retry apply=%+v err=%v", got, err)
	}
	candidate, ok, err := artifactpostgres.NewObservationLedger(f.pool).ClaimCandidate(ctx, time.Minute)
	if err != nil || !ok || len(candidate.Receipt) == 0 {
		t.Fatalf("atomic receipt missing: ok=%v err=%v", ok, err)
	}
	duplicate, err := f.service.ApplyEvent(ctx, f.command)
	if err != nil || !duplicate.Message.Duplicate {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	// Missing durable evidence cannot be re-signed from this replay's sealed data.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE observation_candidates DISABLE TRIGGER observation_candidates_immutable; DELETE FROM observation_candidates; ALTER TABLE observation_candidates ENABLE TRIGGER observation_candidates_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyEvent(ctx, f.command); err == nil {
		t.Fatal("duplicate repaired missing receipt")
	}
}

func TestModelObservationHistoricalStartRequired(t *testing.T) {
	f := newModelPGFixture(t, false, &artifactpostgres.ObservationCaptureStore{})
	got, err := f.service.ApplyEvent(context.Background(), f.command)
	if err != nil || string(got.Message.Outcome) != `{"status":"model_observation_ignored"}` || got.Channel.EventAppliedSequence != 1 {
		t.Fatalf("unproven model apply=%+v err=%v", got, err)
	}
}

func TestModelObservationIgnoredDuplicateStaysIgnored(t *testing.T) {
	f := newModelPGFixture(t, false, &artifactpostgres.ObservationCaptureStore{})
	first, err := f.service.ApplyEvent(context.Background(), f.command)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.service.ApplyEvent(context.Background(), f.command)
	if err != nil || !duplicate.Message.Duplicate || string(duplicate.Message.Outcome) != string(first.Message.Outcome) {
		t.Fatalf("duplicate changed ignored outcome: %+v err=%v", duplicate, err)
	}
}

func TestModelObservationActivationCannotBeBypassed(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	f.command.ObservationCapture = nil
	if _, err := f.service.ApplyEvent(context.Background(), f.command); err == nil {
		t.Fatal("nil capture bypassed durable activation")
	}
	f.service.Store = workerpostgres.NewRuntimeControlJournal(f.pool)
	if _, err := f.service.ApplyEvent(context.Background(), f.command); err == nil {
		t.Fatal("legacy constructor bypassed durable activation")
	}
}

func TestModelObservationSafeGapAdvancesCursor(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	f.command.ObservationCapture = &workerapp.RuntimeObservationCapture{SafeCode: workerapp.ObservationCaptureUnavailable}
	got, err := f.service.ApplyEvent(context.Background(), f.command)
	if err != nil || got.Channel.EventAppliedSequence != 1 {
		t.Fatalf("safe gap=%+v err=%v", got, err)
	}
	var code string
	var hasContent bool
	if err := f.pool.QueryRow(context.Background(), `SELECT error_code,frame IS NOT NULL OR receipt IS NOT NULL FROM observation_candidates`).Scan(&code, &hasContent); err != nil {
		t.Fatal(err)
	}
	if code != "scanner_unavailable" || hasContent {
		t.Fatalf("unsafe gap %q content=%v", code, hasContent)
	}
	if _, err := f.service.ApplyEvent(context.Background(), f.command); err != nil {
		t.Fatal(err)
	}
}

func TestModelObservationConflictAdvancesCursorAndRetainsFirst(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	ctx := context.Background()
	if _, err := f.service.ApplyEvent(ctx, f.command); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := f.pool.QueryRow(ctx, `SELECT frame FROM observation_candidates`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	e := f.envelope
	e.Sequence = 2
	e.MessageID = "conflicting-model"
	e.IdempotencyKey = e.MessageID
	var m workerapp.ModelObservation
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		t.Fatal(err)
	}
	m.Output = json.RawMessage(`{"answer":"conflict"}`)
	e.Payload, _ = json.Marshal(m)
	raw, _ := json.Marshal(e)
	if _, err := f.service.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{Authority: f.command.Authority, Sequence: 2, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: raw}); err != nil {
		t.Fatal(err)
	}
	c := f.command
	c.Sequence = 2
	c.MessageID = e.MessageID
	c.IdempotencyKey = e.IdempotencyKey
	c.ObservationCapture = &workerapp.RuntimeObservationCapture{SafeCode: workerapp.ObservationCaptureUnavailable}
	got, err := f.service.ApplyEvent(ctx, c)
	if err != nil || got.Channel.EventAppliedSequence != 2 {
		t.Fatalf("conflict blocked cursor: %+v err=%v", got, err)
	}
	var first []byte
	var conflicts int
	if err := f.pool.QueryRow(ctx, `SELECT frame FROM observation_candidates`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM observation_fragment_conflicts`).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if string(first) != string(original) || conflicts != 1 {
		t.Fatalf("conflict rewrote original or lacks audit: %d", conflicts)
	}
	if _, err := f.service.ApplyEvent(ctx, c); err != nil {
		t.Fatalf("conflict replay failed: %v", err)
	}
}

func TestModelObservationHistoricalTurnDoesNotGrantOldRuntimeAuthority(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	ctx := context.Background()
	c := f.command
	c.Authority.RuntimeGeneration++
	if _, err := f.service.ApplyEvent(ctx, c); err == nil {
		t.Fatal("old/mismatched runtime gained authority through historical start")
	}
	// The same valid historical start remains acceptable after the current Turn
	// tracking moves elsewhere; no current Turn equality belongs in model Apply.
	if _, err := f.pool.Exec(ctx, `UPDATE runtime_control_messages SET effect_status='terminal' WHERE direction='command' AND message_id='model-start'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_turns SET status='completed',terminal_at=clock_timestamp() WHERE agent_turn_id=$1`, f.started.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_turns(agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,idempotency_key,created_at,started_at,deadline_at) SELECT 'newer-turn',worker_session_id,worker_run_id,agent_session_id,'prompt','{}','delivered',1,'newer-turn',created_at,started_at,deadline_at FROM agent_turns WHERE agent_turn_id=$1`, f.started.AgentTurnID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime_control_channels SET active_effect_agent_turn_id='newer-turn' WHERE runtime_instance_id=$1`, f.started.RuntimeInstanceID); err != nil {
		t.Fatal(err)
	}
	got, err := f.service.ApplyEvent(ctx, f.command)
	if err != nil || got.Channel.EventAppliedSequence != 1 {
		t.Fatalf("historical turn blocked: %+v err=%v", got, err)
	}
	if got.Channel.ActiveEffectAgentTurnID != "newer-turn" {
		t.Fatal("late observation rewound Turn tracking")
	}
}

func TestModelObservationInvalidReceiptBecomesNoContent(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	f.command.ObservationCapture = &workerapp.RuntimeObservationCapture{Sealed: []byte(`{"schema_version":"observation.capture.bundle.v1","observations":[],"token":"untrusted"}`)}
	got, err := f.service.ApplyEvent(context.Background(), f.command)
	if err != nil || got.Channel.EventAppliedSequence != 1 {
		t.Fatalf("invalid receipt blocks cursor: %+v err=%v", got, err)
	}
	var code string
	var content bool
	if err := f.pool.QueryRow(context.Background(), `SELECT error_code,frame IS NOT NULL OR receipt IS NOT NULL FROM observation_candidates`).Scan(&code, &content); err != nil {
		t.Fatal(err)
	}
	if code != "capture_invalid" || content {
		t.Fatalf("invalid receipt persisted: %s %v", code, content)
	}
}

func TestModelObservationLegacyActivationExclusion(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	// Explicit server-owned exclusion models a Run present at activation.
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO observation_fragment_run_exclusions(version,worker_run_id,tenant_id,user_id,workspace_id,worker_session_id) SELECT 'langfuse.projection.v3',r.worker_run_id,s.tenant_id,s.user_id,s.workspace_id,s.worker_session_id FROM worker_runs r JOIN worker_sessions s USING(worker_session_id) WHERE r.worker_run_id=$1`, f.started.WorkerRunID); err != nil {
		t.Fatal(err)
	}
	f.service.Store = workerpostgres.NewRuntimeControlJournal(f.pool)
	f.command.ObservationCapture = nil
	got, err := f.service.ApplyEvent(context.Background(), f.command)
	if err != nil || got.Channel.EventAppliedSequence != 1 {
		t.Fatalf("legacy exclusion changed: %+v %v", got, err)
	}
}

func TestModelObservationJournalIntegrityFailureRollsBack(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE runtime_control_messages SET fingerprint='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE direction='command' AND message_id='model-start'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyEvent(ctx, f.command); err == nil {
		t.Fatal("journal integrity failure was converted into an ignored cursor commit")
	}
}

func TestModelObservationCannotBorrowDeadlineRelaxedAuthority(t *testing.T) {
	f := newModelPGFixture(t, true, &artifactpostgres.ObservationCaptureStore{})
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE worker_sessions SET active_runtime_instance_id=NULL WHERE worker_session_id=$1`, f.started.WorkerSessionID); err != nil {
		t.Fatal(err)
	}
	f.command.Outcome = json.RawMessage(`{"status":"model_observation_recorded","effect_disposition":"ignore"}`)
	f.command.TurnSettlement = &workerapp.TurnSettlementApplyAuthority{SettlementID: "not-an-authority", ExpectedRevision: 1, LeaseOwnerID: "not-an-owner", LeaseFence: 1}
	if _, err := f.service.ApplyEvent(ctx, f.command); !errors.Is(err, workerapp.ErrRuntimeControlStaleOwner) {
		t.Fatalf("model borrowed relaxed deadline authority: %v", err)
	}
}
