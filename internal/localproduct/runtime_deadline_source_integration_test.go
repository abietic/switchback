//go:build integration

package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestPendingSettlementScannerIncludesBackendDeadlineTerminalWitness(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 2, 40, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	acceptedAt := deadlineAt.Add(time.Second)
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return acceptedAt },
	}
	const credential = "terminal-witness-runtime-control-secret"
	if _, err := journal.Bind(
		ctx,
		workerapp.BindRuntimeControlChannelCommand{
			WorkerSessionID:   "session-deadline-claim",
			RuntimeInstanceID: "runtime-deadline-claim",
			RuntimeGeneration: 1,
			ChannelEpoch:      7,
			Credential:        credential,
		},
	); err != nil {
		t.Fatal(err)
	}
	plan, err := journal.Claim(
		ctx,
		workerapp.ClaimRuntimeControlStreamCommand{
			WorkerSessionID:           "session-deadline-claim",
			RuntimeInstanceID:         "runtime-deadline-claim",
			RuntimeGeneration:         1,
			ChannelEpoch:              7,
			Credential:                credential,
			ProcessNonce:              "terminal-witness-process",
			StreamAttempt:             1,
			OwnerID:                   "terminal-witness-stream",
			WorkerEventCursorDigest:   contractsv1.RuntimeControlCursorGenesisDigest,
			WorkerCommandCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	channel, err := journal.CompleteResume(
		ctx,
		workerapp.CompleteRuntimeControlResumeCommand{
			Authority: runtimeControlAuthorityForDeadlineSourceTest(
				plan.Channel,
			),
			Ready: contractsv1.RuntimeControlResumeReady{
				ServerConnectionID: plan.Channel.OwnerID,
				AcknowledgedJournalRevision: plan.Channel.
					ResumeClaimRevision,
				WorkerTXReplayedThroughSequence: plan.Channel.
					EventCommittedSequence,
				WorkerTXCursorDigest: plan.Channel.
					EventCommittedDigest,
				WorkerRXReceivedThroughSequence: plan.Channel.
					CommandCommittedSequence,
				WorkerRXAppliedSequence: plan.Channel.
					CommandAppliedSequence,
				WorkerRXCursorDigest: plan.Channel.
					CommandCommittedDigest,
				ActiveTurn: plan.Channel.ActiveEffectSnapshot,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := (backendDeadlineClaimHandler{
		Pool: pool,
		Source: workerapp.TurnSettlementSourceService{
			Store: repository,
			Now:   func() time.Time { return deadlineAt },
		},
	}).HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	authority := runtimeControlAuthorityForDeadlineSourceTest(channel)
	envelope, err := json.Marshal(contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		MessageID:         "deadline-terminal-witness",
		Kind:              "event",
		WorkerSessionID:   "session-deadline-claim",
		WorkerRunID:       "run-deadline-claim",
		RuntimeInstanceID: "runtime-deadline-claim",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-deadline-claim",
		AgentTurnID:       "turn-deadline-claim",
		ChannelEpoch:      7,
		Sequence:          1,
		IdempotencyKey:    "deadline-terminal-witness",
		MessageType:       "agent_turn.terminal",
		Payload:           json.RawMessage(`{}`),
		SentAt:            acceptedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return acceptedAt },
	}).AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{
		Authority:      authority,
		Sequence:       1,
		MessageID:      "deadline-terminal-witness",
		IdempotencyKey: "deadline-terminal-witness",
		Envelope:       envelope,
	}); err != nil {
		t.Fatal(err)
	}
	live := &liveRuntime{identity: controlgrpc.RuntimeIdentity{
		WorkerSessionID:   "session-deadline-claim",
		RuntimeInstanceID: "runtime-deadline-claim",
		RuntimeGeneration: 1,
		ChannelEpoch:      7,
	}}
	scanNow := acceptedAt
	runner := &runtimeRunner{
		configuration: Config{Profile: platformconfig.SingleUserLocalProfile{
			TenantID: "tenant-local",
		}},
		pool:      pool,
		now:       func() time.Time { return scanNow },
		settlerID: "terminal-witness-settler",
		settlements: workerapp.TurnSettlementService{
			Store: repository,
			Now:   func() time.Time { return scanNow },
		},
		runtimes: make(map[string]*liveRuntime),
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET lease_owner_id='foreign-startup-owner',
    lease_fence=4,
    lease_expires_at=$2,
    revision=revision+1,
    updated_at=$1
WHERE settlement_id=$3`,
		scanNow,
		scanNow.Add(time.Minute),
		"turn-settlement/turn-deadline-claim",
	); err != nil {
		t.Fatal(err)
	}
	if turnID, found, err := runner.reconcileOnePendingTurnSettlement(
		ctx,
	); err != nil || found || turnID != "" {
		t.Fatalf(
			"foreign terminal lease turn=%s found=%t error=%v",
			turnID,
			found,
			err,
		)
	}
	scanNow = scanNow.Add(2 * time.Minute)
	var durableReconciled bool
	runner.durableTerminalReconciler = func(
		_ context.Context,
		settlementID string,
	) (bool, error) {
		if settlementID !=
			"turn-settlement/turn-deadline-claim" {
			t.Fatalf("durable settlement id=%s", settlementID)
		}
		durableReconciled = true
		return true, nil
	}
	runner.unownedRuntimeRestorer = func(
		_ context.Context,
		runtimeInstanceID string,
	) (*restoredStartupTurn, error) {
		if !durableReconciled {
			t.Fatal("unowned Runtime restored before durable terminal settlement")
		}
		if runtimeInstanceID != "runtime-deadline-claim" {
			t.Fatalf("unowned Runtime id=%s", runtimeInstanceID)
		}
		runner.runtimes[runtimeInstanceID] = live
		return nil, nil
	}
	turnID, found, err := runner.reconcileOnePendingTurnSettlement(ctx)
	if err != nil || !found ||
		turnID != "turn-deadline-claim" ||
		!durableReconciled ||
		runner.runtimes["runtime-deadline-claim"] != live {
		t.Fatalf(
			"no-live durable fallback turn=%s found=%t reconciled=%t live=%p error=%v",
			turnID,
			found,
			durableReconciled,
			runner.runtimes["runtime-deadline-claim"],
			err,
		)
	}
	runner.durableTerminalReconciler = nil
	runner.unownedRuntimeRestorer = nil
	current, gotLive, unlock, found, err :=
		runner.claimNextPendingTurnSettlement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !found || gotLive != live || unlock == nil ||
		current.Source.Kind !=
			workerapp.TurnSettlementSourceBackendDeadline {
		t.Fatalf(
			"pending Backend terminal witness current=%+v live=%p found=%t",
			current,
			gotLive,
			found,
		)
	}
	defer unlock()
	state, err := workerapp.DecodeBackendDeadlineSourceState(
		current.Source,
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase !=
		workerapp.BackendDeadlineSourceTerminalObserved {
		t.Fatalf("Backend terminal witness source state=%+v", state)
	}
}

func runtimeControlAuthorityForDeadlineSourceTest(
	channel workerapp.RuntimeControlChannelState,
) workerapp.RuntimeControlAuthority {
	return workerapp.RuntimeControlAuthority{
		WorkerSessionID:   channel.WorkerSessionID,
		RuntimeInstanceID: channel.RuntimeInstanceID,
		RuntimeGeneration: channel.RuntimeGeneration,
		ChannelEpoch:      channel.ChannelEpoch,
		ProcessNonce:      channel.ProcessNonce,
		StreamAttempt:     channel.StreamAttempt,
		OwnerFence:        channel.OwnerFence,
	}
}

func TestBackendDeadlineSourceWorkerQueuesInterruptThenFencesWithoutLiveRuntime(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	deadlineAt := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	command := seedBackendDeadlineClaimGraph(t, pool, deadlineAt)
	now := deadlineAt.Add(time.Second)
	journal := newRuntimeControlJournalService(
		pool,
		func() time.Time { return now },
	)
	hub := controlgrpc.NewHubWithJournal(journal)
	if err := hub.RegisterCredential(controlgrpc.Credential{
		Identity: controlgrpc.RuntimeIdentity{
			WorkerSessionID:   "session-deadline-claim",
			RuntimeInstanceID: "runtime-deadline-claim",
			RuntimeGeneration: 1,
			ChannelEpoch:      7,
		},
		Token: "deadline-claim-runtime-token-00000001",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_control_channels
SET process_nonce='deadline-source-process',
    stream_attempt=1,owner_id='deadline-source-stream',
    owner_fence=1,claimed_at=$2,status='disconnected',
    revision=revision+1,updated_at=$2,disconnected_at=$2
WHERE runtime_instance_id=$1`,
		"runtime-deadline-claim",
		now,
	); err != nil {
		t.Fatal(err)
	}
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	source := workerapp.TurnSettlementSourceService{
		Store: repository,
		Now:   func() time.Time { return now },
	}
	if err := (backendDeadlineClaimHandler{
		Pool:   pool,
		Source: source,
	}).HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	beforeInterrupt := loadBackendDeadlineClaimDomainSnapshot(t, pool)
	runner := &runtimeRunner{
		configuration: Config{
			Profile: platformconfig.SingleUserLocalProfile{
				TenantID:    "tenant-local",
				ActorUserID: "user-local",
			},
		},
		pool:      pool,
		now:       func() time.Time { return now },
		hub:       hub,
		settlerID: "deadline-source-owner",
		settlements: workerapp.TurnSettlementService{
			Store: repository,
			Now:   func() time.Time { return now },
		},
		runtimes: make(map[string]*liveRuntime),
	}
	runner.deadlineSourceEffects = localBackendDeadlineSourceEffects{
		runner: runner,
	}

	found, err := runner.reconcileBackendDeadlineSource(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil || !found {
		t.Fatalf("interrupt reconcile found=%t error=%v", found, err)
	}
	settlement, err := repository.LoadTurnSettlement(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil {
		t.Fatal(err)
	}
	state := decodeBackendDeadlineSourceStateForTest(t, settlement)
	if state.Phase != workerapp.BackendDeadlineSourceInterruptQueued ||
		state.Interrupt == nil ||
		state.Interrupt.MessageID !=
			"deadline-interrupt-turn-deadline-claim" {
		t.Fatalf("interrupt state=%+v", state)
	}
	var interruptCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM runtime_control_messages
WHERE runtime_instance_id='runtime-deadline-claim'
  AND direction='command'
  AND message_id='deadline-interrupt-turn-deadline-claim'
  AND effect_status='journaled'`).Scan(&interruptCount); err != nil {
		t.Fatal(err)
	}
	if interruptCount != 1 {
		t.Fatalf("interrupt journal count=%d, want 1", interruptCount)
	}
	handoff, err := runner.backendDeadlineSourceWaiterHandoff(
		ctx,
		pendingTurn{AgentTurnID: "turn-deadline-claim"},
	)
	if err != nil || handoff {
		t.Fatalf(
			"interrupt-only source handoff=%t error=%v",
			handoff,
			err,
		)
	}
	afterInterrupt := loadBackendDeadlineClaimDomainSnapshot(t, pool)
	wantAfterInterrupt := beforeInterrupt
	wantAfterInterrupt.RuntimeRevision++
	if afterInterrupt != wantAfterInterrupt {
		t.Fatalf(
			"interrupt changed more than the Runtime command cursor: before=%+v after=%+v",
			beforeInterrupt,
			afterInterrupt,
		)
	}
	if _, err := pool.Exec(ctx, `
UPDATE worker_runs
SET start_snapshot=jsonb_build_object(
      'working_copy_checkpoint_digest',
      'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    )
WHERE worker_run_id='run-deadline-claim'`); err != nil {
		t.Fatal(err)
	}
	interruptCandidates, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil || len(interruptCandidates) != 1 {
		t.Fatalf(
			"interrupt startup candidates=%+v error=%v",
			interruptCandidates,
			err,
		)
	}
	interruptCatalog, err := repository.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{
			TenantID: "tenant-local",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	interruptQuarantine, _, err := buildStartupSettlementQuarantine(
		interruptCatalog,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	interruptCandidate, allowed :=
		interruptQuarantine.PrepareRuntimeCandidate(
			interruptCandidates[0],
		)
	if !allowed ||
		!interruptCandidate.SettlementReconnectUntil.Equal(
			deadlineAt.Add(workerapp.BackendDeadlineForceGrace),
		) {
		t.Fatalf(
			"interrupt candidate allowed=%t candidate=%+v",
			allowed,
			interruptCandidate,
		)
	}

	now = deadlineAt.Add(
		workerapp.BackendDeadlineForceGrace + time.Second,
	)
	found, err = runner.reconcileBackendDeadlineSource(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil || !found {
		t.Fatalf("force reconcile found=%t error=%v", found, err)
	}
	settlement, err = repository.LoadTurnSettlement(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil {
		t.Fatal(err)
	}
	state = decodeBackendDeadlineSourceStateForTest(t, settlement)
	if state.Phase != workerapp.BackendDeadlineSourceForceStopIntent ||
		state.ForceStop == nil ||
		!state.ForceStop.DecidedAt.Equal(
			deadlineAt.Add(workerapp.BackendDeadlineForceGrace),
		) ||
		state.ForceStop.ExpectedContainerID !=
			"container-deadline-claim" {
		t.Fatalf("force state=%+v", state)
	}
	var channelStatus string
	var cleanupCommands int
	if err := pool.QueryRow(ctx, `
SELECT status
FROM runtime_control_channels
WHERE runtime_instance_id='runtime-deadline-claim'`).
		Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM scheduled_commands
WHERE scheduled_command_id='deadline-cleanup-command-turn-deadline-claim'
  AND command_type=$1
  AND target_id='turn-settlement/turn-deadline-claim'`,
		workerpostgres.CommandBackendDeadlineSourceReconcile,
	).Scan(&cleanupCommands); err != nil {
		t.Fatal(err)
	}
	if channelStatus != "fenced" || cleanupCommands != 1 {
		t.Fatalf(
			"channel_status=%s cleanup_commands=%d",
			channelStatus,
			cleanupCommands,
		)
	}
	handoff, err = runner.backendDeadlineSourceWaiterHandoff(
		ctx,
		pendingTurn{AgentTurnID: "turn-deadline-claim"},
	)
	if err != nil || !handoff {
		t.Fatalf(
			"force-owned source handoff=%t error=%v",
			handoff,
			err,
		)
	}
	startupCandidates, err := runner.loadStartupRuntimeCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(startupCandidates) != 1 {
		t.Fatalf(
			"raw startup Runtime candidates=%+v",
			startupCandidates,
		)
	}
	listed, err := repository.ListPendingTurnSettlementsForStartup(
		ctx,
		workerapp.ListPendingTurnSettlementsForStartupRecord{
			TenantID: "tenant-local",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	quarantine, _, err := buildStartupSettlementQuarantine(
		listed,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, allowed := quarantine.PrepareRuntimeCandidate(
		startupCandidates[0],
	); allowed {
		t.Fatal("durably forced Runtime crossed the startup settlement barrier")
	}
	if err := runner.rejectUnownedRuntimes(ctx); err != nil {
		t.Fatalf(
			"durably forced Runtime entered disabled-recovery loss handling: %v",
			err,
		)
	}
	var recoveryIncidentCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM runtime_recovery_incidents
WHERE failed_runtime_instance_id='runtime-deadline-claim'`).
		Scan(&recoveryIncidentCount); err != nil {
		t.Fatal(err)
	}
	if recoveryIncidentCount != 0 {
		t.Fatalf(
			"durably forced Runtime opened %d recovery incidents",
			recoveryIncidentCount,
		)
	}
	afterForce := loadBackendDeadlineClaimDomainSnapshot(t, pool)
	if afterForce != afterInterrupt {
		t.Fatalf(
			"force intent mutated domain: before=%+v after=%+v",
			afterInterrupt,
			afterForce,
		)
	}

	var reconcile platformpostgres.ScheduledCommand
	reconcile.ID =
		"deadline-cleanup-command-turn-deadline-claim"
	if err := pool.QueryRow(ctx, `
SELECT tenant_id,command_type,target_type,target_id,expected_revision,
       payload,due_at
FROM scheduled_commands
WHERE scheduled_command_id=$1`,
		reconcile.ID,
	).Scan(
		&reconcile.TenantID,
		&reconcile.CommandType,
		&reconcile.TargetType,
		&reconcile.TargetID,
		&reconcile.ExpectedRevision,
		&reconcile.Payload,
		&reconcile.DueAt,
	); err != nil {
		t.Fatal(err)
	}
	reconcile.FencingToken = 1
	containerAbsentAt := now.Add(time.Second)
	cleanupObservedAt := containerAbsentAt.Add(time.Second)
	cleaner := &scriptedBackendDeadlineCleaner{
		observations: []workerapp.RuntimeCleanupObservation{
			{
				ContainerExists:    false,
				SecretVolumeExists: true,
				ContainerRef:       "switchback-runtime-runtime-deadline-claim",
				SecretVolumeRef:    "switchback-runtime-secrets-runtime-deadline-claim-g1",
				ObservedAt:         containerAbsentAt,
			},
			{
				Confirmed:          true,
				ContainerExists:    false,
				SecretVolumeExists: false,
				ContainerRef:       "switchback-runtime-runtime-deadline-claim",
				SecretVolumeRef:    "switchback-runtime-secrets-runtime-deadline-claim-g1",
				ObservedAt:         cleanupObservedAt,
			},
		},
		errors: []error{
			errors.New("injected secret-volume residue"),
			nil,
		},
	}
	localCleanupCalls := 0
	runner.deadlineSourceEffects = localBackendDeadlineSourceEffects{
		runner:  runner,
		cleaner: cleaner,
		cleanupLocal: func(
			workerapp.BackendDeadlineSourceAuthority,
		) (workerapp.SessionStorage, error) {
			localCleanupCalls++
			return workerapp.SessionStorage{}, nil
		},
	}
	now = containerAbsentAt
	if err := runner.handleBackendDeadlineReconcileCommand(
		ctx,
		reconcile,
	); err != nil {
		t.Fatal(err)
	}
	settlement, err = repository.LoadTurnSettlement(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil {
		t.Fatal(err)
	}
	state = decodeBackendDeadlineSourceStateForTest(t, settlement)
	if state.Phase != workerapp.BackendDeadlineSourceWriteFenced {
		t.Fatalf("write-fence state=%+v", state)
	}

	// Replaying the same frozen command after the source revision/digest has
	// advanced is safe: its immutable force prefix is revalidated, then only
	// the next proof phase is applied.
	now = cleanupObservedAt
	if err := runner.handleBackendDeadlineReconcileCommand(
		ctx,
		reconcile,
	); err != nil {
		t.Fatal(err)
	}
	settlement, err = repository.LoadTurnSettlement(
		ctx,
		"turn-settlement/turn-deadline-claim",
	)
	if err != nil {
		t.Fatal(err)
	}
	state = decodeBackendDeadlineSourceStateForTest(t, settlement)
	if state.Phase !=
		workerapp.BackendDeadlineSourceCleanupConfirmed {
		t.Fatalf("cleanup state=%+v", state)
	}
	handoff, err = runner.backendDeadlineSourceWaiterHandoff(
		ctx,
		pendingTurn{AgentTurnID: "turn-deadline-claim"},
	)
	if err != nil || !handoff {
		t.Fatalf(
			"cleanup-owned source handoff=%t error=%v",
			handoff,
			err,
		)
	}
	afterCleanup := loadBackendDeadlineClaimDomainSnapshot(t, pool)
	if afterCleanup != afterForce {
		t.Fatalf(
			"proof reconciliation mutated domain: before=%+v after=%+v",
			afterForce,
			afterCleanup,
		)
	}
	if localCleanupCalls != 2 || len(cleaner.requests) != 2 {
		t.Fatalf(
			"local cleanup calls=%d Runtime cleanup requests=%d",
			localCleanupCalls,
			len(cleaner.requests),
		)
	}
	for _, request := range cleaner.requests {
		if request.RuntimeInstanceID != "runtime-deadline-claim" ||
			request.ExpectedContainerID !=
				"container-deadline-claim" ||
			request.Generation != 1 {
			t.Fatalf("cleanup request=%+v", request)
		}
	}
}

func decodeBackendDeadlineSourceStateForTest(
	t *testing.T,
	settlement workerapp.TurnSettlement,
) workerapp.BackendDeadlineSourceState {
	t.Helper()
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(settlement.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

type scriptedBackendDeadlineCleaner struct {
	observations []workerapp.RuntimeCleanupObservation
	errors       []error
	requests     []workerapp.RuntimeCleanupRequest
}

func (cleaner *scriptedBackendDeadlineCleaner) Cleanup(
	_ context.Context,
	request workerapp.RuntimeCleanupRequest,
) (workerapp.RuntimeCleanupObservation, error) {
	cleaner.requests = append(cleaner.requests, request)
	index := len(cleaner.requests) - 1
	return cleaner.observations[index], cleaner.errors[index]
}
