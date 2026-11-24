//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeControlJournalReconnectsAfterResidentRunIsReleased(t *testing.T) {
	ctx := context.Background()
	pool := postgrestest.Open(t)
	initial := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, initial)
	now := initial.Add(6 * time.Minute)
	journal, oldAuthority := bindAndClaimRuntimeControl(t, pool, started, now)
	journal.Now = func() time.Time { return now }

	start, err := journal.ReserveCommand(ctx, workerapp.ReserveRuntimeControlCommandCommand{
		Authority: oldAuthority, MessageID: "resident-start", IdempotencyKey: "resident-start",
		Envelope: runtimeControlEnvelope(started, "command", 0, "resident-start", "resident-start", "agent_turn.start"),
	})
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
	if _, err := journal.ReconcileEffect(ctx, workerapp.ReconcileRuntimeControlEffectCommand{
		Authority: oldAuthority, CommandSequence: start.Sequence, MessageID: start.MessageID,
		AgentTurnID: started.AgentTurnID, Phase: workerapp.RuntimeControlEffectActive, ActiveTurn: &active,
	}); err != nil {
		t.Fatal(err)
	}
	event := runtimeControlEnvelopeWithAck(t,
		runtimeControlEnvelope(started, "event", 1, "resident-terminal", "resident-terminal", "agent_turn.terminal"),
		start.Sequence,
	)
	accepted, err := journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{
		Authority: oldAuthority, Sequence: 1, MessageID: "resident-terminal",
		IdempotencyKey: "resident-terminal", Envelope: event,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := active
	terminal.Phase = contractsv1.RuntimeControlTurnEffectTerminal
	terminal.LastUpdateSequence = 1
	terminal.TerminalEnvelopeDigest = runtimeControlEnvelopeFingerprint(t, event)
	if _, err := journal.ReconcileEffect(ctx, workerapp.ReconcileRuntimeControlEffectCommand{
		Authority: oldAuthority, CommandSequence: start.Sequence, MessageID: start.MessageID,
		AgentTurnID: started.AgentTurnID, Phase: workerapp.RuntimeControlEffectTerminal,
		TerminalEventSequence: 1, TerminalEnvelopeDigest: terminal.TerminalEnvelopeDigest, ActiveTurn: &terminal,
	}); err != nil {
		t.Fatal(err)
	}

	stage := workerapp.CompleteResidentStageCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID,
		AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: started.RuntimeGeneration, SessionTarget: workerdomain.SessionWaitingUser,
		RunTarget: workerdomain.RunCompleted, TurnTarget: workerdomain.TurnCompleted, TerminalReason: "end_turn",
	}
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM runtime_instances runtime
JOIN worker_sessions session ON session.worker_session_id=runtime.worker_session_id
JOIN worker_runs run ON run.worker_run_id=runtime.worker_run_id
JOIN agent_turns turn ON turn.agent_turn_id=$2
WHERE runtime.runtime_instance_id=$1`, started.RuntimeInstanceID, started.AgentTurnID).Scan(
		&stage.ExpectedSessionRevision, &stage.ExpectedRunRevision, &stage.ExpectedTurnRevision, &stage.ExpectedRuntimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	settlement := advanceTerminalSettlementToTaskSettled(t, pool, started, now)
	now = now.Add(5 * time.Millisecond)
	if _, err := (workerapp.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: journal.Now}).
		CompleteStage(ctx, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ApplyEvent(ctx, workerapp.ApplyRuntimeControlEventCommand{
		Authority: oldAuthority, Sequence: 1, MessageID: "resident-terminal", IdempotencyKey: "resident-terminal/apply",
		Outcome:        json.RawMessage(`{"status":"stage_committed","effect_disposition":"commit"}`),
		TurnSettlement: turnSettlementApplyAuthority(settlement),
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Millisecond)
	completeAppliedTerminalSettlement(t, pool, started, now)

	claim := runtimeControlClaim(started, oldAuthority.ProcessNonce, 2, "restarted-backend")
	claim.WorkerCommandLastSequence = start.Sequence
	claim.WorkerCommandAppliedSequence = start.Sequence
	claim.WorkerCommandCursorDigest = start.CursorDigest
	claim.WorkerEventLastSequence = accepted.Channel.EventCommittedSequence
	claim.WorkerEventBackendAcknowledgedSequence = accepted.Channel.EventAcknowledgedSequence
	claim.WorkerEventCursorDigest = accepted.Channel.EventCommittedDigest
	claim.ActiveTurn = &terminal
	journal.Store = workerpostgres.NewRuntimeControlJournal(pool)
	wrongDigest := terminal
	wrongDigest.TerminalEnvelopeDigest = contractsv1.RuntimeControlCursorGenesisDigest
	for _, badProof := range []*contractsv1.RuntimeControlActiveTurn{&active, &wrongDigest} {
		badClaim := claim
		badClaim.ActiveTurn = badProof
		if _, err := journal.Claim(ctx, badClaim); !errors.Is(err, workerapp.ErrRuntimeControlIdentityConflict) {
			t.Fatalf("released run accepted a different proof, err = %v", err)
		}
	}
	plan, err := journal.Claim(ctx, claim)
	if err != nil {
		t.Fatalf("reconnect the same resident Runtime after release: %v", err)
	}
	assertResumedTerminalWorker(t, journal, plan, terminal, oldAuthority)

	// Rework can be admitted before another reconnect completes, so the Runtime
	// now names a successor while the Shim still reports the previous terminal.
	const successor = "resident-next-run"
	if _, err := pool.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,previous_worker_run_id,start_snapshot,
   agent_profile_id,agent_profile_revision,agent_profile_digest,status,created_at,started_at)
SELECT $2,worker_session_id,task_id,'task_rework',worker_run_id,'{}',agent_profile_id,
       agent_profile_revision,agent_profile_digest,'running',$3,$3
FROM worker_runs WHERE worker_run_id=$1`, started.WorkerRunID, successor, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_instances SET worker_run_id=$2 WHERE runtime_instance_id=$1`, started.RuntimeInstanceID, successor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions SET active_worker_run_id=$2,status='running' WHERE worker_session_id=$1`,
		started.WorkerSessionID, successor); err != nil {
		t.Fatal(err)
	}
	claim.StreamAttempt++
	claim.OwnerID = "next-backend"
	plan, err = journal.Claim(ctx, claim)
	if err != nil {
		t.Fatalf("reconnect with a successor Run already admitted: %v", err)
	}
	assertResumedTerminalWorker(t, journal, plan, terminal, oldAuthority)
	var activeRun, turnStatus string
	if err := pool.QueryRow(ctx, `
SELECT session.active_worker_run_id,turn.status FROM worker_sessions session
JOIN agent_turns turn ON turn.worker_session_id=session.worker_session_id
WHERE turn.agent_turn_id=$1`, started.AgentTurnID).Scan(&activeRun, &turnStatus); err != nil {
		t.Fatal(err)
	}
	if activeRun != successor || turnStatus != "completed" {
		t.Fatalf("resume rewrote the Run or previous Turn, active Run %q, Turn %q", activeRun, turnStatus)
	}
	if _, err := pool.Exec(ctx, `UPDATE worker_runs SET previous_worker_run_id=NULL WHERE worker_run_id=$1`, successor); err != nil {
		t.Fatal(err)
	}
	claim.StreamAttempt++
	if _, err := journal.Claim(ctx, claim); !errors.Is(err, workerapp.ErrRuntimeControlIdentityConflict) {
		t.Fatalf("unrelated successor accepted the previous Turn proof, err = %v", err)
	}
}

func assertResumedTerminalWorker(
	t *testing.T,
	journal workerapp.RuntimeControlJournalService,
	plan workerapp.RuntimeControlReplayPlan,
	terminal contractsv1.RuntimeControlActiveTurn,
	oldAuthority workerapp.RuntimeControlAuthority,
) {
	t.Helper()
	resumed := completeRuntimeControlClaim(t, journal, plan)
	if resumed.Status != "connected" || resumed.ProcessNonce != oldAuthority.ProcessNonce ||
		resumed.RuntimeInstanceID != oldAuthority.RuntimeInstanceID || resumed.RuntimeGeneration != oldAuthority.RuntimeGeneration ||
		resumed.ChannelEpoch != oldAuthority.ChannelEpoch || resumed.OwnerFence <= oldAuthority.OwnerFence ||
		resumed.ActiveEffectSnapshot == nil || *resumed.ActiveEffectSnapshot != terminal {
		t.Fatalf("unexpected resumed Runtime authority: %+v", resumed)
	}
	if _, err := journal.ReplayPlan(context.Background(), oldAuthority); !errors.Is(err, workerapp.ErrRuntimeControlStaleOwner) {
		t.Fatalf("previous Backend owner was not fenced: %v", err)
	}
}
