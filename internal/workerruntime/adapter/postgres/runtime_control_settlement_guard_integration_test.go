//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeControlSettlementAdmissionGuardOnClaim(t *testing.T) {
	t.Run("ordinary reconnect without pending settlement is allowed", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)

		plan, err := runtimeControlSettlementGuardClaim(
			fixture,
			base.Add(7*time.Minute),
			2,
			fixture.ActiveTurn,
		)
		if err != nil {
			t.Fatalf("ordinary reconnect: %v", err)
		}
		if plan.Channel.Status != "resuming" {
			t.Fatalf("ordinary reconnect status=%q", plan.Channel.Status)
		}
	})

	t.Run("claimed Backend deadline is rejected", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 10, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		_ = claimRuntimeControlBackendDeadline(t, fixture)

		_, err := runtimeControlSettlementGuardClaim(
			fixture,
			fixture.DeadlineAt.Add(time.Second),
			2,
			fixture.ActiveTurn,
		)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlPendingTurnSettlement,
		) {
			t.Fatalf("claimed settlement reconnect error=%v", err)
		}
	})

	t.Run("unauthenticated Claim cannot enter the settlement lock path", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 12, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		_ = claimRuntimeControlBackendDeadline(t, fixture)
		command := runtimeControlClaim(
			fixture.Started,
			fixture.ProcessNonce,
			2,
			"settlement-guard-owner",
		)
		command.Credential = "wrong-runtime-control-secret"
		command.WorkerCommandLastSequence = fixture.Start.Sequence
		command.WorkerCommandAppliedSequence = fixture.Start.Sequence
		command.WorkerCommandCursorDigest = fixture.Start.CursorDigest
		command.ActiveTurn = &fixture.ActiveTurn

		_, err := runtimeControlSettlementGuardJournal(
			pool,
			fixture.DeadlineAt.Add(time.Second),
		).Claim(context.Background(), command)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlCredentialConflict,
		) {
			t.Fatalf("unauthenticated settlement guard Claim error=%v", err)
		}
	})

	t.Run("pending settlement without active proof is rejected", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 15, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		_ = claimRuntimeControlBackendDeadline(t, fixture)
		command := runtimeControlClaim(
			fixture.Started,
			fixture.ProcessNonce,
			2,
			"settlement-guard-owner",
		)
		command.WorkerCommandLastSequence = fixture.Start.Sequence
		command.WorkerCommandAppliedSequence = fixture.Start.Sequence
		command.WorkerCommandCursorDigest = fixture.Start.CursorDigest

		_, err := runtimeControlSettlementGuardJournal(
			pool,
			fixture.DeadlineAt.Add(time.Second),
		).Claim(context.Background(), command)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlPendingTurnSettlement,
		) {
			t.Fatalf("proofless pending settlement reconnect error=%v", err)
		}
	})

	t.Run("one exact future interrupt is allowed through CompleteResume", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 20, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		interrupted := interruptRuntimeControlBackendDeadline(t, fixture)
		state, err := workerapp.DecodeBackendDeadlineSourceState(
			interrupted.Source,
		)
		if err != nil {
			t.Fatal(err)
		}

		claimAt := fixture.DeadlineAt.Add(2 * time.Second)
		plan, err := runtimeControlSettlementGuardClaim(
			fixture,
			claimAt,
			2,
			fixture.ActiveTurn,
		)
		if err != nil {
			t.Fatalf("exact future interrupt Claim: %v", err)
		}
		if len(plan.PendingCommands) != 1 ||
			plan.PendingCommands[0].Sequence != state.Interrupt.Sequence {
			t.Fatalf(
				"future interrupt replay=%+v source=%+v",
				plan.PendingCommands,
				state,
			)
		}

		completed, err := runtimeControlSettlementGuardJournal(
			pool,
			claimAt.Add(time.Second),
		).CompleteResume(
			context.Background(),
			workerapp.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(plan.Channel),
				Ready:     runtimeControlSettlementGuardReady(plan),
			},
		)
		if err != nil {
			t.Fatalf("exact future interrupt CompleteResume: %v", err)
		}
		if completed.Status != "connected" {
			t.Fatalf("completed future interrupt status=%q", completed.Status)
		}
	})

	t.Run("future interrupt requires exact active identity", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 30, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		_ = interruptRuntimeControlBackendDeadline(t, fixture)
		mismatch := fixture.ActiveTurn
		mismatch.WorkerRunID = contractsv1.WorkerRunID("worker-run-other")

		_, err := runtimeControlSettlementGuardClaim(
			fixture,
			fixture.DeadlineAt.Add(2*time.Second),
			2,
			mismatch,
		)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlPendingTurnSettlement,
		) {
			t.Fatalf("mismatched future interrupt reconnect error=%v", err)
		}
	})

	t.Run("force boundary is closed", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 8, 40, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		_ = interruptRuntimeControlBackendDeadline(t, fixture)

		_, err := runtimeControlSettlementGuardClaim(
			fixture,
			fixture.DeadlineAt.Add(
				workerapp.BackendDeadlineForceGrace,
			),
			2,
			fixture.ActiveTurn,
		)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlPendingTurnSettlement,
		) {
			t.Fatalf("force boundary reconnect error=%v", err)
		}
	})
}

func TestRuntimeControlSettlementAdmissionGuardRechecksCompleteResume(
	t *testing.T,
) {
	t.Run("settlement created after Claim blocks commit", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		claimAt := fixture.DeadlineAt.Add(-time.Second)
		plan, err := runtimeControlSettlementGuardClaim(
			fixture,
			claimAt,
			2,
			fixture.ActiveTurn,
		)
		if err != nil {
			t.Fatalf("pre-settlement Claim: %v", err)
		}

		_ = claimRuntimeControlBackendDeadline(t, fixture)
		_, err = runtimeControlSettlementGuardJournal(
			pool,
			fixture.DeadlineAt.Add(time.Second),
		).CompleteResume(
			context.Background(),
			workerapp.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(plan.Channel),
				Ready:     runtimeControlSettlementGuardReady(plan),
			},
		)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlPendingTurnSettlement,
		) {
			t.Fatalf("CompleteResume pending settlement error=%v", err)
		}
	})

	t.Run("interrupt appended after Claim requires a fresh replay", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 9, 5, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		firstPlan, err := runtimeControlSettlementGuardClaim(
			fixture,
			fixture.DeadlineAt.Add(-time.Second),
			2,
			fixture.ActiveTurn,
		)
		if err != nil {
			t.Fatalf("pre-interrupt Claim: %v", err)
		}
		_ = interruptRuntimeControlBackendDeadline(t, fixture)

		_, err = runtimeControlSettlementGuardJournal(
			pool,
			fixture.DeadlineAt.Add(2*time.Second),
		).CompleteResume(
			context.Background(),
			workerapp.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(firstPlan.Channel),
				Ready:     runtimeControlSettlementGuardReady(firstPlan),
			},
		)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlResumeSnapshotChanged,
		) {
			t.Fatalf("stale pre-interrupt Ready error=%v", err)
		}

		replayed, err := runtimeControlSettlementGuardClaim(
			fixture,
			fixture.DeadlineAt.Add(3*time.Second),
			3,
			fixture.ActiveTurn,
		)
		if err != nil {
			t.Fatalf("post-interrupt replay Claim: %v", err)
		}
		if len(replayed.PendingCommands) != 1 {
			t.Fatalf("post-interrupt replay=%+v", replayed.PendingCommands)
		}
		if _, err := runtimeControlSettlementGuardJournal(
			pool,
			fixture.DeadlineAt.Add(4*time.Second),
		).CompleteResume(
			context.Background(),
			workerapp.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(replayed.Channel),
				Ready:     runtimeControlSettlementGuardReady(replayed),
			},
		); err != nil {
			t.Fatalf("post-interrupt CompleteResume: %v", err)
		}
	})

	t.Run("force boundary reached after Claim blocks commit", func(t *testing.T) {
		pool := postgrestest.Open(t)
		base := time.Date(2026, 7, 27, 9, 10, 0, 0, time.UTC)
		fixture := setupRuntimeControlSettlementGuardFixture(t, pool, base)
		_ = interruptRuntimeControlBackendDeadline(t, fixture)
		forceAfter := fixture.DeadlineAt.Add(
			workerapp.BackendDeadlineForceGrace,
		)
		plan, err := runtimeControlSettlementGuardClaim(
			fixture,
			forceAfter.Add(-time.Microsecond),
			2,
			fixture.ActiveTurn,
		)
		if err != nil {
			t.Fatalf("pre-force Claim: %v", err)
		}

		_, err = runtimeControlSettlementGuardJournal(
			pool,
			forceAfter,
		).CompleteResume(
			context.Background(),
			workerapp.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(plan.Channel),
				Ready:     runtimeControlSettlementGuardReady(plan),
			},
		)
		if !errors.Is(
			err,
			workerapp.ErrRuntimeControlPendingTurnSettlement,
		) {
			t.Fatalf("force-boundary CompleteResume error=%v", err)
		}
	})
}

type runtimeControlSettlementGuardFixture struct {
	Pool         *pgxpool.Pool
	Started      workerapp.StartedWorkerRun
	Start        workerapp.RuntimeControlMessage
	ActiveTurn   contractsv1.RuntimeControlActiveTurn
	DeadlineAt   time.Time
	SourceRef    string
	ProcessNonce string
}

func setupRuntimeControlSettlementGuardFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
) runtimeControlSettlementGuardFixture {
	t.Helper()
	ctx := context.Background()
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	service, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		base.Add(6*time.Minute),
	)
	start, err := service.ReserveCommand(
		ctx,
		workerapp.ReserveRuntimeControlCommandCommand{
			Authority:      authority,
			MessageID:      "settlement-guard-start",
			IdempotencyKey: "settlement-guard-start",
			Envelope: runtimeControlEnvelope(
				started,
				"command",
				0,
				"settlement-guard-start",
				"settlement-guard-start",
				"agent_turn.start",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(
		t,
		started,
		start,
		contractsv1.RuntimeControlTurnEffectActive,
	)
	if _, err := service.ReconcileEffect(
		ctx,
		workerapp.ReconcileRuntimeControlEffectCommand{
			Authority:       authority,
			CommandSequence: start.Sequence,
			MessageID:       start.MessageID,
			AgentTurnID:     started.AgentTurnID,
			Phase:           workerapp.RuntimeControlEffectActive,
			ActiveTurn:      &active,
		},
	); err != nil {
		t.Fatal(err)
	}
	return runtimeControlSettlementGuardFixture{
		Pool:         pool,
		Started:      started,
		Start:        start,
		ActiveTurn:   active,
		DeadlineAt:   deadlineAt,
		SourceRef:    sourceRef,
		ProcessNonce: "process-1",
	}
}

func runtimeControlSettlementGuardClaim(
	fixture runtimeControlSettlementGuardFixture,
	at time.Time,
	streamAttempt uint64,
	active contractsv1.RuntimeControlActiveTurn,
) (workerapp.RuntimeControlReplayPlan, error) {
	command := runtimeControlClaim(
		fixture.Started,
		fixture.ProcessNonce,
		streamAttempt,
		"settlement-guard-owner",
	)
	command.WorkerCommandLastSequence = fixture.Start.Sequence
	command.WorkerCommandAppliedSequence = fixture.Start.Sequence
	command.WorkerCommandCursorDigest = fixture.Start.CursorDigest
	command.ActiveTurn = &active
	return runtimeControlSettlementGuardJournal(
		fixture.Pool,
		at,
	).Claim(context.Background(), command)
}

func runtimeControlSettlementGuardJournal(
	pool *pgxpool.Pool,
	at time.Time,
) workerapp.RuntimeControlJournalService {
	return workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return at },
	}
}

func claimRuntimeControlBackendDeadline(
	t *testing.T,
	fixture runtimeControlSettlementGuardFixture,
) workerapp.TurnSettlement {
	t.Helper()
	settlement, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(fixture.Pool),
		Now:   func() time.Time { return fixture.DeadlineAt },
	}).ClaimBackendDeadline(
		context.Background(),
		backendDeadlineClaimCommand(
			fixture.Started,
			fixture.DeadlineAt,
			fixture.SourceRef,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return settlement
}

func interruptRuntimeControlBackendDeadline(
	t *testing.T,
	fixture runtimeControlSettlementGuardFixture,
) workerapp.TurnSettlement {
	t.Helper()
	repository := workerpostgres.NewTurnSettlementRepository(fixture.Pool)
	claimed := claimRuntimeControlBackendDeadline(t, fixture)
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return fixture.DeadlineAt },
	}).Claim(
		context.Background(),
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			OwnerID:          "settlement-guard-source",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := (workerapp.TurnSettlementSourceService{
		Store: repository,
		Now: func() time.Time {
			return fixture.DeadlineAt.Add(time.Second)
		},
	}).AdvanceBackendDeadline(
		context.Background(),
		backendDeadlineInterruptAdvanceCommand(
			fixture.Started,
			leased,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return advanced
}

func runtimeControlSettlementGuardReady(
	plan workerapp.RuntimeControlReplayPlan,
) contractsv1.RuntimeControlResumeReady {
	channel := plan.Channel
	return contractsv1.RuntimeControlResumeReady{
		ServerConnectionID:              channel.OwnerID,
		AcknowledgedJournalRevision:     channel.ResumeClaimRevision,
		WorkerTXReplayedThroughSequence: channel.EventCommittedSequence,
		WorkerTXCursorDigest:            channel.EventCommittedDigest,
		WorkerRXReceivedThroughSequence: channel.CommandCommittedSequence,
		WorkerRXAppliedSequence:         channel.CommandAppliedSequence,
		WorkerRXCursorDigest:            channel.CommandCommittedDigest,
		ActiveTurn:                      channel.ActiveEffectSnapshot,
	}
}
