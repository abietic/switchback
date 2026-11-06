//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeRecoveryOpenRejectsPendingTerminalSettlementAuthority(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		base.Add(6*time.Minute),
	)
	terminalEnvelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"terminal-before-recovery",
		"terminal-before-recovery",
		"agent_turn.terminal",
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-before-recovery",
			IdempotencyKey: "terminal-before-recovery",
			Envelope:       terminalEnvelope,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT revision FROM runtime_instances WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&active.RuntimeRevision); err != nil {
		t.Fatal(err)
	}

	_, err := (workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}).Open(ctx, workerapplication.OpenRuntimeRecoveryCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1,
		FailedChannelEpoch:      1,
		Trigger:                 domain.RecoveryTriggerContainerExit,
		EvidenceRef:             "runtime-inspection/" + started.RuntimeInstanceID + "/exit",
		EvidenceAt:              base.Add(6*time.Minute + time.Second),
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision:     active.RunRevision,
		ExpectedTurnRevision:    active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" +
			started.WorkerSessionID +
			"/g1/container_exit",
	})
	if !errors.Is(err, workerapplication.ErrTurnOutcomeAuthorityClaimed) {
		t.Fatalf("Runtime recovery opened over pending terminal settlement: %v", err)
	}

	var incidents int
	var settlementPhase, sessionStatus, runtimeStatus string
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM runtime_recovery_incidents),
  settlement.phase,
  session.status,
  runtime.status
FROM worker_turn_settlements settlement
JOIN worker_sessions session
  ON session.worker_session_id=settlement.worker_session_id
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=settlement.runtime_instance_id
WHERE settlement.agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(
		&incidents,
		&settlementPhase,
		&sessionStatus,
		&runtimeStatus,
	); err != nil {
		t.Fatal(err)
	}
	if incidents != 0 ||
		settlementPhase != "accepted" ||
		sessionStatus != "running" ||
		runtimeStatus != "active" {
		t.Fatalf(
			"incidents=%d settlement=%s session=%s runtime=%s",
			incidents,
			settlementPhase,
			sessionStatus,
			runtimeStatus,
		)
	}
}

func TestRuntimeTerminalAcceptRejectsEarlierRecoveryAuthorityWithoutJournalMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 8, 30, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		base.Add(6*time.Minute),
	)
	recovery := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}
	if _, err := recovery.Open(
		ctx,
		runtimeRecoveryOpenCommand(
			started,
			active,
			base.Add(6*time.Minute+time.Second),
			domain.RecoveryTriggerContainerExit,
		),
	); err != nil {
		t.Fatal(err)
	}

	terminalEnvelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"terminal-after-recovery",
		"terminal-after-recovery",
		"agent_turn.terminal",
	)
	_, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-after-recovery",
			IdempotencyKey: "terminal-after-recovery",
			Envelope:       terminalEnvelope,
		},
	)
	if !errors.Is(err, workerapplication.ErrTurnOutcomeAuthorityClaimed) {
		t.Fatalf("terminal Accept after Runtime recovery error=%v", err)
	}

	var eventMessages, settlements int
	var eventCommittedSequence, runtimeLastEventSequence uint64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*)
   FROM runtime_control_messages
   WHERE runtime_instance_id=$1 AND direction='event'),
  (SELECT count(*)
   FROM worker_turn_settlements
   WHERE agent_turn_id=$2),
  channel.event_committed_sequence,
  runtime.last_event_sequence
FROM runtime_control_channels channel
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=channel.runtime_instance_id
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		started.AgentTurnID,
	).Scan(
		&eventMessages,
		&settlements,
		&eventCommittedSequence,
		&runtimeLastEventSequence,
	); err != nil {
		t.Fatal(err)
	}
	if eventMessages != 0 ||
		settlements != 0 ||
		eventCommittedSequence != 0 ||
		runtimeLastEventSequence != 0 {
		t.Fatalf(
			"terminal mutated journal after recovery: messages=%d settlements=%d channel=%d runtime=%d",
			eventMessages,
			settlements,
			eventCommittedSequence,
			runtimeLastEventSequence,
		)
	}
}

func TestRuntimeTerminalAcceptAndRecoveryOpenShareOneAgentTurnOutcomeLock(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 8, 45, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		base.Add(6*time.Minute),
	)
	recovery := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}
	recoveryCommand := runtimeRecoveryOpenCommand(
		started,
		active,
		base.Add(6*time.Minute+time.Second),
		domain.RecoveryTriggerContainerExit,
	)
	terminalEnvelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"terminal-racing-recovery",
		"terminal-racing-recovery",
		"agent_turn.terminal",
	)

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	lockKey := "worker-runtime:agent-turn-outcome:" + started.AgentTurnID
	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_lock(hashtextextended($1, 0))`,
		lockKey,
	); err != nil {
		t.Fatal(err)
	}
	lockHeld := true
	unlock := func() {
		if !lockHeld {
			return
		}
		var unlocked bool
		if err := blocker.QueryRow(
			ctx,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`,
			lockKey,
		).Scan(&unlocked); err != nil {
			t.Errorf("unlock AgentTurn outcome test barrier: %v", err)
		} else if !unlocked {
			t.Error("AgentTurn outcome test barrier was not held")
		}
		lockHeld = false
	}
	defer unlock()

	start := make(chan struct{})
	terminalResult := make(chan error, 1)
	recoveryResult := make(chan error, 1)
	go func() {
		<-start
		_, err := journal.AcceptEvent(
			ctx,
			workerapplication.AcceptRuntimeControlEventCommand{
				Authority:      authority,
				Sequence:       1,
				MessageID:      "terminal-racing-recovery",
				IdempotencyKey: "terminal-racing-recovery",
				Envelope:       terminalEnvelope,
			},
		)
		terminalResult <- err
	}()
	go func() {
		<-start
		_, err := recovery.Open(ctx, recoveryCommand)
		recoveryResult <- err
	}()
	close(start)

	waitDeadline := time.Now().Add(3 * time.Second)
	waiters := 0
	for time.Now().Before(waitDeadline) {
		if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_locks
WHERE locktype='advisory'
  AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
  AND classid=((hashtextextended($1,0) >> 32) & 4294967295)::oid
  AND objid=(hashtextextended($1,0) & 4294967295)::oid
  AND objsubid=1
  AND NOT granted`,
			lockKey,
		).Scan(&waiters); err != nil {
			unlock()
			t.Fatal(err)
		}
		if waiters == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiters != 2 {
		unlock()
		t.Fatalf(
			"terminal Accept and recovery Open did not share the AgentTurn outcome lock: waiters=%d",
			waiters,
		)
	}
	unlock()

	var terminalErr, recoveryErr error
	select {
	case terminalErr = <-terminalResult:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal Accept did not finish after releasing outcome lock")
	}
	select {
	case recoveryErr = <-recoveryResult:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery Open did not finish after releasing outcome lock")
	}
	switch {
	case terminalErr == nil &&
		errors.Is(recoveryErr, workerapplication.ErrTurnOutcomeAuthorityClaimed):
	case recoveryErr == nil &&
		errors.Is(terminalErr, workerapplication.ErrTurnOutcomeAuthorityClaimed):
	default:
		t.Fatalf(
			"outcome sources did not linearize: terminal=%v recovery=%v",
			terminalErr,
			recoveryErr,
		)
	}

	var incidents, settlements, eventMessages int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM runtime_recovery_incidents),
  (SELECT count(*)
   FROM worker_turn_settlements
   WHERE agent_turn_id=$1),
  (SELECT count(*)
   FROM runtime_control_messages
   WHERE runtime_instance_id=$2 AND direction='event')`,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(&incidents, &settlements, &eventMessages); err != nil {
		t.Fatal(err)
	}
	if incidents+settlements != 1 || eventMessages != settlements {
		t.Fatalf(
			"durable outcome authority diverged: incidents=%d settlements=%d event_messages=%d",
			incidents,
			settlements,
			eventMessages,
		)
	}
}

func TestRuntimeRecoveryOpenRequiresRecoverySignalInboxApplyFirst(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		base.Add(6*time.Minute),
	)
	terminalEnvelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"acp-eof-before-recovery",
		"acp-eof-before-recovery",
		"agent_turn.terminal",
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapplication.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "acp-eof-before-recovery",
			IdempotencyKey: "acp-eof-before-recovery",
			Envelope:       terminalEnvelope,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT revision FROM runtime_instances WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&active.RuntimeRevision); err != nil {
		t.Fatal(err)
	}

	recovery := workerapplication.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}
	command := runtimeRecoveryOpenCommand(
		started,
		active,
		base.Add(6*time.Minute+time.Second),
		domain.RecoveryTriggerACPEOF,
	)
	if _, err := recovery.Open(ctx, command); !errors.Is(
		err,
		workerapplication.ErrTurnOutcomeAuthorityClaimed,
	) {
		t.Fatalf("recovery before terminal inbox Apply error=%v", err)
	}

	if _, err := journal.ApplyEvent(
		ctx,
		workerapplication.ApplyRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "acp-eof-before-recovery",
			IdempotencyKey: "acp-eof-before-recovery/apply",
			Outcome: json.RawMessage(
				`{"status":"ignored_runtime_recovery_signal","effect_disposition":"ignore"}`,
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	opened, err := recovery.Open(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Phase != domain.RecoveryOpen ||
		opened.Trigger != domain.RecoveryTriggerACPEOF {
		t.Fatalf("opened recovery=%+v", opened)
	}

	var settlementPhase, disposition string
	if err := pool.QueryRow(ctx, `
SELECT phase,terminal_event_disposition
FROM worker_turn_settlements
WHERE agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(&settlementPhase, &disposition); err != nil {
		t.Fatal(err)
	}
	if settlementPhase != "completed" || disposition != "ignore" {
		t.Fatalf(
			"settlement=%s disposition=%s",
			settlementPhase,
			disposition,
		)
	}
}

func TestRuntimeRecoveryCoordinatorClaimsReloadedBackendDeadlineSource(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	recoveryStore := workerpostgres.NewRuntimeRecoveryStore(pool)
	recovery := workerapplication.RuntimeRecoveryService{
		Store: recoveryStore,
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}
	if _, err := recovery.Open(
		ctx,
		runtimeRecoveryOpenCommand(
			started,
			active,
			base.Add(6*time.Minute+time.Second),
			domain.RecoveryTriggerContainerExit,
		),
	); err != nil {
		t.Fatal(err)
	}

	state, found, err := recoveryStore.FindActiveRuntimeRecovery(
		ctx,
		"tenant-local",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !found || state.AgentTurnDeadlineAt.IsZero() ||
		state.AgentTurnDeadlineSourceRef == "" {
		t.Fatalf("reloaded recovery state=%+v found=%v", state, found)
	}
	var expectedSourceRef string
	if err := pool.QueryRow(ctx, `
SELECT scheduled_command_id
FROM scheduled_commands
WHERE tenant_id='tenant-local'
  AND command_type='agent_turn_deadline'
  AND target_type='AgentTurn'
  AND target_id=$1
  AND due_at=$2
  AND idempotency_key='agent-turn-deadline/' || $1`,
		state.AgentTurnID,
		state.AgentTurnDeadlineAt,
	).Scan(&expectedSourceRef); err != nil {
		t.Fatal(err)
	}
	if state.AgentTurnDeadlineSourceRef != expectedSourceRef {
		t.Fatalf(
			"deadline source ref=%q want=%q",
			state.AgentTurnDeadlineSourceRef,
			expectedSourceRef,
		)
	}

	deadlineNow := state.AgentTurnDeadlineAt
	unexpectedIO := &unexpectedRuntimeRecoveryIO{}
	result, err := (workerapplication.RuntimeRecoveryCoordinator{
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: recoveryStore,
			IDs:   &concurrentAllocationIDs{},
			Now:   func() time.Time { return deadlineNow },
		},
		DeadlineSources: workerapplication.TurnSettlementSourceService{
			Store: workerpostgres.NewTurnSettlementRepository(pool),
			Now:   func() time.Time { return deadlineNow },
		},
		Effects:     unexpectedIO,
		Provisioner: unexpectedIO,
	}).Reconcile(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if unexpectedIO.called {
		t.Fatal("deadline preemption performed an ordinary recovery effect")
	}
	if result.Phase != domain.RecoveryExhausted ||
		result.Outcome != domain.RecoveryOutcomeExhausted ||
		result.OutcomeReason != "preempted_by_backend_deadline" ||
		result.IncidentRevision != state.IncidentRevision+1 {
		t.Fatalf("deadline-preempted recovery=%+v", result)
	}

	var phase, outcomeReason, settlementID, sourceRef string
	if err := pool.QueryRow(ctx, `
SELECT incident.phase,incident.outcome_reason,
       incident.preempted_by_turn_settlement_id,settlement.source_ref
FROM runtime_recovery_incidents incident
JOIN worker_turn_settlements settlement
  ON settlement.settlement_id=incident.preempted_by_turn_settlement_id
WHERE incident.incident_id=$1`,
		state.IncidentID,
	).Scan(&phase, &outcomeReason, &settlementID, &sourceRef); err != nil {
		t.Fatal(err)
	}
	if phase != "exhausted" ||
		outcomeReason != "preempted_by_backend_deadline" ||
		settlementID != "turn-settlement/"+state.AgentTurnID ||
		sourceRef != expectedSourceRef {
		t.Fatalf(
			"phase=%s reason=%s settlement=%s source=%s",
			phase,
			outcomeReason,
			settlementID,
			sourceRef,
		)
	}
}

type unexpectedRuntimeRecoveryIO struct {
	called bool
}

func (io *unexpectedRuntimeRecoveryIO) unexpected() error {
	io.called = true
	return errors.New("unexpected ordinary Runtime recovery effect")
}

func (io *unexpectedRuntimeRecoveryIO) FenceFailedRuntime(
	context.Context,
	workerapplication.RuntimeRecoveryState,
) (workerapplication.RuntimeRecoveryFenceEvidence, error) {
	return workerapplication.RuntimeRecoveryFenceEvidence{}, io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) FenceReplacementRuntime(
	context.Context,
	workerapplication.RuntimeRecoveryState,
) error {
	return io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) FenceLateProvisionedRuntime(
	context.Context,
	workerapplication.RuntimeRecoveryState,
	workerapplication.ProvisionedRuntime,
) error {
	return io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) PrepareReplacementRuntime(
	context.Context,
	workerapplication.RuntimeRecoveryState,
) (workerapplication.RuntimeProvisionRequest, error) {
	return workerapplication.RuntimeProvisionRequest{}, io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) ReattachReplacementRuntime(
	context.Context,
	workerapplication.RuntimeRecoveryState,
) (workerapplication.RuntimeRecoveryAttachmentEvidence, error) {
	return workerapplication.RuntimeRecoveryAttachmentEvidence{}, io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) VerifyReplacementRuntime(
	context.Context,
	workerapplication.RuntimeRecoveryState,
) (workerapplication.RuntimeRecoveryHealthEvidence, error) {
	return workerapplication.RuntimeRecoveryHealthEvidence{}, io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) Provision(
	context.Context,
	workerapplication.RuntimeProvisionRequest,
	workerapplication.RuntimePreStartCheck,
) (workerapplication.ProvisionedRuntime, error) {
	return workerapplication.ProvisionedRuntime{}, io.unexpected()
}

func (io *unexpectedRuntimeRecoveryIO) Inspect(
	context.Context,
	string,
) (workerapplication.RuntimeInspection, error) {
	return workerapplication.RuntimeInspection{}, io.unexpected()
}

func runtimeRecoveryOpenCommand(
	started workerapplication.StartedWorkerRun,
	active workerapplication.ResidentRuntimeState,
	evidenceAt time.Time,
	trigger domain.RuntimeRecoveryTrigger,
) workerapplication.OpenRuntimeRecoveryCommand {
	return workerapplication.OpenRuntimeRecoveryCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		FailedRuntimeInstanceID: started.RuntimeInstanceID,
		FailedGeneration:        1,
		FailedChannelEpoch:      1,
		Trigger:                 trigger,
		EvidenceRef: "runtime-failure/" +
			started.RuntimeInstanceID +
			"/" +
			string(trigger),
		EvidenceAt:              evidenceAt,
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedRunRevision:     active.RunRevision,
		ExpectedTurnRevision:    active.TurnRevision,
		ExpectedRuntimeRevision: active.RuntimeRevision,
		IdempotencyKey: "runtime-recovery/" +
			started.WorkerSessionID +
			"/g1/" +
			string(trigger),
	}
}
