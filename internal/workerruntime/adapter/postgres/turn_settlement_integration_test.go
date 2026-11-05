//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeControlTerminalAcceptanceCreatesDurableSettlementBeforeRunner(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 21, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	envelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"terminal-before-runner",
		"terminal-before-runner",
		"agent_turn.terminal",
	)
	accepted, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID: "terminal-before-runner", IdempotencyKey: "terminal-before-runner",
			Envelope: envelope,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	restarted := postgrestest.Restart(t, pool)
	settlements := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
		Now:   func() time.Time { return now.Add(7 * time.Minute) },
	}
	settlementID, err := workerapp.TurnSettlementID(started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	settlement, err := settlements.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.SettlementID != settlementID ||
		settlement.TenantID != "tenant-local" ||
		settlement.WorkerSessionID != started.WorkerSessionID ||
		settlement.WorkerRunID != started.WorkerRunID ||
		settlement.AgentSessionID != started.AgentSessionID ||
		settlement.AgentTurnID != started.AgentTurnID ||
		settlement.RuntimeInstanceID != started.RuntimeInstanceID ||
		settlement.RuntimeGeneration != started.RuntimeGeneration ||
		settlement.ChannelEpoch != authority.ChannelEpoch ||
		settlement.TerminalEventSequence != accepted.Message.Sequence ||
		settlement.TerminalMessageID != accepted.Message.MessageID ||
		settlement.TerminalIdempotencyKey != accepted.Message.IdempotencyKey ||
		settlement.TerminalEnvelopeDigest != accepted.Message.Fingerprint ||
		settlement.Source.Kind != workerapp.TurnSettlementSourceRuntimeEvent ||
		settlement.Source.Reason != "runtime_terminal" ||
		!settlement.Source.OccurredAt.Equal(accepted.Message.CreatedAt) ||
		settlement.Source.Ref != accepted.Message.MessageID ||
		settlement.Source.Digest != accepted.Message.Fingerprint ||
		string(settlement.Source.State) !=
			`{"phase":"terminal_observed","schema_version":1}` ||
		settlement.Source.StateDigest !=
			"sha256:eade79e0a1ca190d82f879fee630b0a45261063eefac1d74cd68c417e861348f" ||
		settlement.Phase != workerapp.TurnSettlementAccepted ||
		settlement.Revision != 1 {
		t.Fatalf("durable settlement=%+v accepted=%+v", settlement, accepted)
	}

	restartedJournal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(restarted),
		Now:   func() time.Time { return now.Add(7 * time.Minute) },
	}
	duplicate, err := restartedJournal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID: "terminal-before-runner", IdempotencyKey: "terminal-before-runner",
			Envelope: envelope,
		},
	)
	if err != nil || !duplicate.Message.Duplicate {
		t.Fatalf("duplicate terminal=%+v err=%v", duplicate, err)
	}
	reloaded, err := settlements.Load(ctx, settlementID)
	if err != nil || reloaded.Revision != settlement.Revision {
		t.Fatalf("duplicate changed settlement=%+v err=%v", reloaded, err)
	}

	conflicting := runtimeControlEnvelope(
		started,
		"event",
		2,
		"terminal-conflict",
		"terminal-conflict",
		"agent_turn.terminal",
	)
	if _, err := restartedJournal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 2,
			MessageID: "terminal-conflict", IdempotencyKey: "terminal-conflict",
			Envelope: conflicting,
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementConflict) {
		t.Fatalf("conflicting terminal error=%v", err)
	}
	var committedSequence uint64
	if err := restarted.QueryRow(ctx, `
SELECT event_committed_sequence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(&committedSequence); err != nil {
		t.Fatal(err)
	}
	if committedSequence != 1 {
		t.Fatalf("conflicting terminal advanced event cursor to %d", committedSequence)
	}
}

func TestWorkerTurnSettlementMigrationBackfillsPendingAndAppliedTerminalJournal(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 21, 45, 0, 0, time.UTC)
	started := startActiveSettlementFixtures(t, pool, now, 2)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started[0],
		now.Add(6*time.Minute),
	)
	accepted, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      "terminal-before-migration",
			IdempotencyKey: "terminal-before-migration",
			Envelope: runtimeControlEnvelope(
				started[0],
				"event",
				1,
				"terminal-before-migration",
				"terminal-before-migration",
				"agent_turn.terminal",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	appliedJournal, appliedAuthority := bindAndClaimRuntimeControl(
		t,
		pool,
		started[1],
		now.Add(6*time.Minute),
	)
	if _, err := appliedJournal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: appliedAuthority, Sequence: 1,
			MessageID:      "applied-terminal-before-migration",
			IdempotencyKey: "applied-terminal-before-migration",
			Envelope: runtimeControlEnvelope(
				started[1],
				"event",
				1,
				"applied-terminal-before-migration",
				"applied-terminal-before-migration",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE runtime_control_messages
SET outcome='{"effect_disposition":"commit","status":"terminal_committed"}'::jsonb,
    effect_status='applied',
    apply_idempotency_key='applied-terminal-before-migration/apply',
    apply_outcome_digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    applied_at=$2
WHERE runtime_instance_id=$1 AND direction='event' AND sequence=1`,
		started[1].RuntimeInstanceID,
		now.Add(7*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	dropTurnSettlementSchemaForMigrationFixture(t, pool)
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Turn settlement integration test")
	}
	migrationPath := filepath.Join(
		filepath.Dir(sourceFile),
		"..",
		"..",
		"..",
		"..",
		"migrations",
		"000015_worker_turn_settlement.sql",
	)
	data, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	up, _, ok := strings.Cut(string(data), "-- migrate:down")
	if !ok {
		t.Fatal("Turn settlement migration lacks down marker")
	}
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatalf("apply Turn settlement upgrade migration: %v", err)
	}
	sourceUnionMigrationPath := filepath.Join(
		filepath.Dir(sourceFile),
		"..",
		"..",
		"..",
		"..",
		"migrations",
		"000016_turn_settlement_source_union.sql",
	)
	sourceUnionData, err := os.ReadFile(sourceUnionMigrationPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceUnionUp, _, ok := strings.Cut(
		string(sourceUnionData),
		"-- migrate:down",
	)
	if !ok {
		t.Fatal("Turn settlement source-union migration lacks down marker")
	}
	if _, err := pool.Exec(ctx, sourceUnionUp); err != nil {
		t.Fatalf(
			"apply Turn settlement source-union migration: %v",
			err,
		)
	}

	restarted := postgrestest.Restart(t, pool)
	settlementID, _ := workerapp.TurnSettlementID(started[0].AgentTurnID)
	settlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
	}).Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Phase != workerapp.TurnSettlementAccepted ||
		settlement.TerminalEventSequence != accepted.Message.Sequence ||
		settlement.TerminalMessageID != accepted.Message.MessageID ||
		settlement.TerminalEnvelopeDigest != accepted.Message.Fingerprint ||
		!settlement.AcceptedAt.Equal(accepted.Message.CreatedAt) {
		t.Fatalf("backfilled pending terminal settlement=%+v accepted=%+v", settlement, accepted)
	}
	appliedSettlementID, _ := workerapp.TurnSettlementID(started[1].AgentTurnID)
	appliedSettlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
	}).Load(ctx, appliedSettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if appliedSettlement.Phase != workerapp.TurnSettlementCompleted ||
		appliedSettlement.TerminalEventDisposition !=
			workerapp.RuntimeControlEffectDispositionCommit ||
		string(appliedSettlement.PhaseState) !=
			`{"legacy_backfill":true,"schema_version":1}` ||
		appliedSettlement.PhaseStateDigest !=
			"sha256:cf47ef167e70344cd3e6e48d4426c1e34055aa32da428e623c15b9ff82fa8079" ||
		appliedSettlement.CompletedAt.IsZero() {
		t.Fatalf("backfilled applied terminal settlement=%+v", appliedSettlement)
	}
}

func TestWorkerTurnSettlementMigrationRejectsPartiallySettledReceivedTerminal(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 21, 47, 0, 0, time.UTC)
	started := startActiveSettlementFixtures(t, pool, now, 1)[0]
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      "partial-terminal-before-migration",
			IdempotencyKey: "partial-terminal-before-migration",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"partial-terminal-before-migration",
				"partial-terminal-before-migration",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	dropTurnSettlementSchemaForMigrationFixture(t, pool)
	if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='interactive',session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1`,
		started.WorkerSessionID,
		now.Add(7*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Turn settlement integration test")
	}
	migrationPath := filepath.Join(
		filepath.Dir(sourceFile),
		"..",
		"..",
		"..",
		"..",
		"migrations",
		"000015_worker_turn_settlement.sql",
	)
	data, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	up, _, ok := strings.Cut(string(data), "-- migrate:down")
	if !ok {
		t.Fatal("Turn settlement migration lacks down marker")
	}
	if _, err := pool.Exec(ctx, up); err == nil ||
		!strings.Contains(
			err.Error(),
			"received terminal has partial business settlement",
		) {
		t.Fatalf("partial settlement migration error=%v", err)
	}
}

func TestTurnSettlementLoadRejectsCheckpointDigestMismatch(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 21, 50, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      "terminal-tampered-checkpoint",
			IdempotencyKey: "terminal-tampered-checkpoint",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-tampered-checkpoint",
				"terminal-tampered-checkpoint",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET phase_state='{"tampered":true}'::jsonb
WHERE settlement_id=$1`,
		settlementID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlementID); !errors.Is(err, workerapp.ErrTurnSettlementConflict) {
		t.Fatalf("tampered checkpoint Load error=%v", err)
	}
}

func TestTurnSettlementClaimNextPendingUsesStableOrderAndReclaimsExpiredLease(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 0, 0, 0, 123456789, time.UTC)
	started := startActiveSettlementFixtures(t, pool, now, 3)
	acceptedAt := now.Add(10 * time.Minute)
	var settlementIDs []string
	for index, fixture := range started {
		journal, authority := bindAndClaimRuntimeControl(
			t,
			pool,
			fixture,
			acceptedAt,
		)
		messageID := "claim-next-terminal-" + string(rune('a'+index))
		if _, err := journal.AcceptEvent(
			ctx,
			workerapp.AcceptRuntimeControlEventCommand{
				Authority: authority, Sequence: 1,
				MessageID: messageID, IdempotencyKey: messageID,
				Envelope: runtimeControlEnvelope(
					fixture,
					"event",
					1,
					messageID,
					messageID,
					"agent_turn.terminal",
				),
			},
		); err != nil {
			t.Fatal(err)
		}
		settlementID, _ := workerapp.TurnSettlementID(fixture.AgentTurnID)
		settlementIDs = append(settlementIDs, settlementID)
	}
	sort.Strings(settlementIDs)

	current := acceptedAt.Add(time.Second + 987*time.Nanosecond)
	service := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return current },
	}
	first, err := service.ClaimNextPending(ctx, workerapp.ClaimNextPendingTurnSettlementCommand{
		OwnerID: "settler-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ClaimNextPending(ctx, workerapp.ClaimNextPendingTurnSettlementCommand{
		OwnerID: "settler-two",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.SettlementID != settlementIDs[0] ||
		second.SettlementID != settlementIDs[1] {
		t.Fatalf(
			"stable claims first=%s second=%s want=%v",
			first.SettlementID,
			second.SettlementID,
			settlementIDs,
		)
	}
	current = first.LeaseExpiresAt.Add(time.Millisecond)
	reclaimed, err := service.ClaimNextPending(
		ctx,
		workerapp.ClaimNextPendingTurnSettlementCommand{OwnerID: "settler-three"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.SettlementID != settlementIDs[0] ||
		reclaimed.LeaseFence != first.LeaseFence+1 {
		t.Fatalf("reclaimed settlement=%+v first=%+v", reclaimed, first)
	}
}

func TestTurnSettlementClaimNextPendingSkipsLockedHead(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 0, 30, 0, 0, time.UTC)
	started := startActiveSettlementFixtures(t, pool, now, 2)
	acceptedAt := now.Add(10 * time.Minute)
	var settlementIDs []string
	for index, fixture := range started {
		journal, authority := bindAndClaimRuntimeControl(t, pool, fixture, acceptedAt)
		messageID := "skip-locked-terminal-" + string(rune('a'+index))
		if _, err := journal.AcceptEvent(
			ctx,
			workerapp.AcceptRuntimeControlEventCommand{
				Authority: authority, Sequence: 1,
				MessageID: messageID, IdempotencyKey: messageID,
				Envelope: runtimeControlEnvelope(
					fixture,
					"event",
					1,
					messageID,
					messageID,
					"agent_turn.terminal",
				),
			},
		); err != nil {
			t.Fatal(err)
		}
		settlementID, _ := workerapp.TurnSettlementID(fixture.AgentTurnID)
		settlementIDs = append(settlementIDs, settlementID)
	}
	sort.Strings(settlementIDs)
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	var locked string
	if err := lock.QueryRow(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE settlement_id=$1
FOR UPDATE`,
		settlementIDs[0],
	).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	claimed, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return acceptedAt.Add(time.Second) },
	}).ClaimNextPending(ctx, workerapp.ClaimNextPendingTurnSettlementCommand{
		OwnerID: "skip-locked-settler",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.SettlementID != settlementIDs[1] {
		t.Fatalf("claimed locked head: got=%s want=%s", claimed.SettlementID, settlementIDs[1])
	}
}

func TestTurnSettlementCheckpointFreezeIsIdempotentCumulativeAndMicrosecondStable(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 1, 0, 0, 987654321, time.FixedZone("test", 8*60*60))
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID: "freeze-terminal", IdempotencyKey: "freeze-terminal",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"freeze-terminal",
				"freeze-terminal",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	currentTime := now.Add(7 * time.Minute)
	service := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}
	accepted, err := service.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	wantAcceptedAt := now.Add(6 * time.Minute).UTC().Truncate(time.Microsecond)
	if !accepted.AcceptedAt.Equal(wantAcceptedAt) ||
		accepted.AcceptedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("accepted timestamp=%s want=%s", accepted.AcceptedAt, wantAcceptedAt)
	}
	claimed, err := service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: accepted.Revision,
		OwnerID: "checkpoint-settler",
	})
	if err != nil {
		t.Fatal(err)
	}
	proofRecordedAt := now.Add(7 * time.Minute).Truncate(time.Microsecond)
	checkpoint := typedTurnSettlementCheckpoint(
		t,
		validTurnSettlementCheckpoint(
			t,
			started,
			workerapp.CompleteResidentStageCommand{},
			"artifact-report-freeze",
			"artifact-patch-freeze",
			proofRecordedAt,
			workerapp.TurnSettlementAccepted,
		),
	)
	freezeCommand := workerapp.FreezeTurnSettlementCheckpointCommand{
		SettlementID: settlementID, ExpectedRevision: claimed.Revision,
		LeaseOwnerID: claimed.LeaseOwnerID, LeaseFence: claimed.LeaseFence,
		Checkpoint: checkpoint,
	}
	frozen, err := service.FreezeCheckpoint(ctx, freezeCommand)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.FreezeCheckpoint(ctx, freezeCommand)
	if err != nil || duplicate.Revision != frozen.Revision ||
		duplicate.PhaseStateDigest != frozen.PhaseStateDigest {
		t.Fatalf("duplicate freeze=%+v err=%v frozen=%+v", duplicate, err, frozen)
	}
	conflict := freezeCommand
	conflict.Checkpoint.FrozenArtifacts = &workerapp.FrozenArtifactMetadata{
		ReportArtifactID: "artifact-report-conflict",
		PatchArtifactID:  "artifact-patch-freeze",
		CreatedAt:        checkpoint.FrozenArtifacts.CreatedAt,
	}
	if _, err := service.FreezeCheckpoint(ctx, conflict); !errors.Is(
		err,
		workerapp.ErrTurnSettlementCheckpointConflict,
	) {
		t.Fatalf("conflicting freeze error=%v", err)
	}

	cumulative := validTurnSettlementCheckpoint(
		t,
		started,
		workerapp.CompleteResidentStageCommand{},
		"artifact-report-freeze",
		"artifact-patch-freeze",
		proofRecordedAt,
		workerapp.TurnSettlementEvidencePrepared,
	)
	currentTime = currentTime.Add(time.Millisecond)
	prepared, err := service.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: frozen.Revision,
		LeaseOwnerID: frozen.LeaseOwnerID, LeaseFence: frozen.LeaseFence,
		FromPhase: frozen.Phase, ToPhase: workerapp.TurnSettlementEvidencePrepared,
		Checkpoint: cumulative,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: prepared.Revision,
		LeaseOwnerID: prepared.LeaseOwnerID, LeaseFence: prepared.LeaseFence,
		FromPhase: prepared.Phase, ToPhase: workerapp.TurnSettlementDomainCommitted,
		Checkpoint: json.RawMessage(`{"schema_version":1,"domain_committed":true}`),
	}); !errors.Is(err, workerapp.ErrTurnSettlementPhaseConflict) {
		t.Fatalf("non-atomic domain Advance error=%v", err)
	}
	taskPlan := json.RawMessage(`{"action":"none","reason":"no_task"}`)
	laterCheckpoint := typedTurnSettlementCheckpoint(t, prepared.PhaseState)
	laterCheckpoint.AdditionalFields["task_plan"] = taskPlan
	freezeLater := workerapp.FreezeTurnSettlementCheckpointCommand{
		SettlementID: settlementID, ExpectedRevision: prepared.Revision,
		LeaseOwnerID: prepared.LeaseOwnerID, LeaseFence: prepared.LeaseFence,
		Checkpoint: laterCheckpoint,
	}
	currentTime = currentTime.Add(time.Millisecond)
	later, err := service.FreezeCheckpoint(ctx, freezeLater)
	if err != nil {
		t.Fatal(err)
	}
	laterDuplicate, err := service.FreezeCheckpoint(ctx, freezeLater)
	if err != nil || laterDuplicate.Revision != later.Revision {
		t.Fatalf("later duplicate freeze=%+v err=%v", laterDuplicate, err)
	}
	freezeLater.Checkpoint.AdditionalFields["task_plan"] =
		json.RawMessage(`{"action":"different"}`)
	if _, err := service.FreezeCheckpoint(ctx, freezeLater); !errors.Is(
		err,
		workerapp.ErrTurnSettlementCheckpointConflict,
	) {
		t.Fatalf("later conflicting freeze error=%v", err)
	}
	if later.UpdatedAt.Nanosecond()%1000 != 0 ||
		later.LeaseExpiresAt.Nanosecond()%1000 != 0 {
		t.Fatalf("non-microsecond settlement timestamps=%+v", later)
	}
}

func TestTurnSettlementDomainCommitIsAtomicAndReplaysStoredResult(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 1, 30, 0, 0, time.UTC)
	prepared, stage := prepareTurnSettlementDomainCommit(
		t,
		pool,
		now,
	)
	service := workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return now.Add(7*time.Minute + time.Second) },
	}
	command := workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     prepared.SettlementID,
		ExpectedRevision: prepared.Revision,
		LeaseOwnerID:     prepared.LeaseOwnerID,
		LeaseFence:       prepared.LeaseFence,
		Resident:         stage,
	}
	committed, err := service.Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Settlement.Phase !=
		workerapp.TurnSettlementDomainCommitted ||
		committed.Resident.SessionStatus !=
			workerdomain.SessionInteractive ||
		committed.Resident.RunStatus != workerdomain.RunCompleted ||
		committed.Resident.TurnStatus != workerdomain.TurnCompleted ||
		committed.Proof.SessionRevision !=
			committed.Resident.SessionRevision ||
		committed.Proof.RunRevision != committed.Resident.RunRevision ||
		committed.Proof.TurnRevision != committed.Resident.TurnRevision ||
		committed.Proof.RuntimeRevision !=
			committed.Resident.RuntimeRevision {
		t.Fatalf("committed domain=%+v", committed)
	}
	var durable struct {
		Domain workerapp.TurnSettlementDomainProof `json:"domain"`
	}
	if err := json.Unmarshal(
		committed.Settlement.PhaseState,
		&durable,
	); err != nil {
		t.Fatal(err)
	}
	if durable.Domain != committed.Proof {
		t.Fatalf(
			"durable domain proof=%+v returned=%+v",
			durable.Domain,
			committed.Proof,
		)
	}

	replayed, err := service.Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Settlement.Revision !=
		committed.Settlement.Revision ||
		replayed.Proof != committed.Proof ||
		replayed.Resident.SessionRevision !=
			committed.Resident.SessionRevision ||
		replayed.Resident.RunRevision !=
			committed.Resident.RunRevision ||
		replayed.Resident.TurnRevision !=
			committed.Resident.TurnRevision {
		t.Fatalf(
			"replayed domain=%+v committed=%+v",
			replayed,
			committed,
		)
	}
}

func TestTurnSettlementDomainCommitAllowsDispatchAndRuntimeControlRevisionAdvance(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 1, 40, 0, 0, time.UTC)
	prepared, stage := prepareTurnSettlementDomainCommit(t, pool, now)
	var currentTurnRevision uint64
	if err := pool.QueryRow(ctx, `
UPDATE agent_turns
SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND revision=$2 AND status='delivered'
RETURNING revision`,
		stage.AgentTurnID,
		stage.ExpectedTurnRevision,
	).Scan(&currentTurnRevision); err != nil {
		t.Fatal(err)
	}
	var currentRuntimeRevision uint64
	if err := pool.QueryRow(ctx, `
UPDATE runtime_instances
SET last_event_sequence=last_event_sequence+1,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$2 AND status='active'
RETURNING revision`,
		stage.RuntimeInstanceID,
		stage.ExpectedRuntimeRevision,
	).Scan(&currentRuntimeRevision); err != nil {
		t.Fatal(err)
	}
	committed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return now.Add(7*time.Minute + time.Second) },
	}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     prepared.SettlementID,
		ExpectedRevision: prepared.Revision,
		LeaseOwnerID:     prepared.LeaseOwnerID,
		LeaseFence:       prepared.LeaseFence,
		Resident:         stage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed.Settlement.Phase !=
		workerapp.TurnSettlementDomainCommitted ||
		committed.Proof.TurnRevision != currentTurnRevision+1 ||
		committed.Resident.TurnRevision != currentTurnRevision+1 ||
		committed.Proof.RuntimeRevision != currentRuntimeRevision ||
		committed.Resident.RuntimeRevision != currentRuntimeRevision {
		t.Fatalf(
			"domain commit after Runtime cursor advance=%+v current_runtime_revision=%d",
			committed,
			currentRuntimeRevision,
		)
	}
}

func TestTurnSettlementDomainCommitRollsBackResidentStageWhenPhaseWriteFails(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 1, 45, 0, 0, time.UTC)
	prepared, stage := prepareTurnSettlementDomainCommit(
		t,
		pool,
		now,
	)
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION switchback_test_reject_domain_settlement_commit()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.phase = 'domain_committed' THEN
    RAISE EXCEPTION 'injected domain settlement failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER switchback_test_reject_domain_settlement_commit
BEFORE UPDATE ON worker_turn_settlements
FOR EACH ROW
EXECUTE FUNCTION switchback_test_reject_domain_settlement_commit()`); err != nil {
		t.Fatal(err)
	}
	_, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return now.Add(7*time.Minute + time.Second) },
	}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     prepared.SettlementID,
		ExpectedRevision: prepared.Revision,
		LeaseOwnerID:     prepared.LeaseOwnerID,
		LeaseFence:       prepared.LeaseFence,
		Resident:         stage,
	})
	if err == nil || !strings.Contains(
		err.Error(),
		"injected domain settlement failure",
	) {
		t.Fatalf("domain settlement failure=%v", err)
	}
	reloaded, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, prepared.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Phase != workerapp.TurnSettlementEvidencePrepared ||
		reloaded.Revision != prepared.Revision {
		t.Fatalf(
			"settlement mutated after rollback=%+v prepared=%+v",
			reloaded,
			prepared,
		)
	}
	var sessionStatus, runStatus, turnStatus string
	var sessionRevision, runRevision, turnRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.status,run.status,turn.status,
       session.session_revision,run.revision,turn.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
WHERE session.worker_session_id=$1`,
		stage.WorkerSessionID,
		stage.WorkerRunID,
		stage.AgentTurnID,
	).Scan(
		&sessionStatus,
		&runStatus,
		&turnStatus,
		&sessionRevision,
		&runRevision,
		&turnRevision,
	); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != string(workerdomain.SessionRunning) ||
		runStatus != string(workerdomain.RunRunning) ||
		(turnStatus != string(workerdomain.TurnDelivered) &&
			turnStatus != string(workerdomain.TurnConsumed)) ||
		sessionRevision != stage.ExpectedSessionRevision ||
		runRevision != stage.ExpectedRunRevision ||
		turnRevision != stage.ExpectedTurnRevision {
		t.Fatalf(
			"resident stage mutated after rollback status=%s/%s/%s revision=%d/%d/%d",
			sessionStatus,
			runStatus,
			turnStatus,
			sessionRevision,
			runRevision,
			turnRevision,
		)
	}
}

func TestTurnSettlementDomainCommitRejectsWeakEvidenceProofBeforeResidentMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 1, 50, 0, 0, time.UTC)
	prepared, stage := prepareTurnSettlementDomainCommit(t, pool, now)
	weak, digest := removeTurnSettlementCheckpointField(
		t,
		prepared.PhaseState,
		"evidence",
	)
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET phase_state=$2,phase_state_digest=$3
WHERE settlement_id=$1`,
		prepared.SettlementID,
		weak,
		digest,
	); err != nil {
		t.Fatal(err)
	}

	_, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return now.Add(7*time.Minute + time.Second) },
	}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     prepared.SettlementID,
		ExpectedRevision: prepared.Revision,
		LeaseOwnerID:     prepared.LeaseOwnerID,
		LeaseFence:       prepared.LeaseFence,
		Resident:         stage,
	})
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("weak evidence proof commit error=%v", err)
	}

	var phase workerapp.TurnSettlementPhase
	var sessionRevision, runRevision, turnRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT settlement.phase,session.session_revision,run.revision,turn.revision
FROM worker_turn_settlements settlement
JOIN worker_sessions session
  ON session.worker_session_id=settlement.worker_session_id
JOIN worker_runs run ON run.worker_run_id=settlement.worker_run_id
JOIN agent_turns turn ON turn.agent_turn_id=settlement.agent_turn_id
WHERE settlement.settlement_id=$1`,
		prepared.SettlementID,
	).Scan(
		&phase,
		&sessionRevision,
		&runRevision,
		&turnRevision,
	); err != nil {
		t.Fatal(err)
	}
	if phase != workerapp.TurnSettlementEvidencePrepared ||
		sessionRevision != stage.ExpectedSessionRevision ||
		runRevision != stage.ExpectedRunRevision ||
		turnRevision != stage.ExpectedTurnRevision {
		t.Fatalf(
			"weak proof mutated domain phase=%s revisions=%d/%d/%d",
			phase,
			sessionRevision,
			runRevision,
			turnRevision,
		)
	}
}

func TestRuntimeControlStartReservationFailsClosedForSameSessionPendingSettlement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	started := startActiveSettlementFixtures(t, pool, now, 2)
	firstJournal, firstAuthority := bindAndClaimRuntimeControl(
		t,
		pool,
		started[0],
		now.Add(10*time.Minute),
	)
	secondJournal, secondAuthority := bindAndClaimRuntimeControl(
		t,
		pool,
		started[1],
		now.Add(10*time.Minute),
	)
	if _, err := firstJournal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: firstAuthority, Sequence: 1,
			MessageID: "pending-before-start", IdempotencyKey: "pending-before-start",
			Envelope: runtimeControlEnvelope(
				started[0],
				"event",
				1,
				"pending-before-start",
				"pending-before-start",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := firstJournal.ReserveCommand(
		ctx,
		workerapp.ReserveRuntimeControlCommandCommand{
			Authority: firstAuthority, MessageID: "blocked-next-start",
			IdempotencyKey: "blocked-next-start",
			Envelope: runtimeControlEnvelope(
				started[0],
				"command",
				0,
				"blocked-next-start",
				"blocked-next-start",
				"agent_turn.start",
			),
		},
	); !errors.Is(err, workerapp.ErrRuntimeControlPendingTurnSettlement) {
		t.Fatalf("same-Session start error=%v", err)
	}
	var firstCommands int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='command'`,
		started[0].RuntimeInstanceID,
	).Scan(&firstCommands); err != nil {
		t.Fatal(err)
	}
	if firstCommands != 0 {
		t.Fatalf("blocked start journaled %d commands", firstCommands)
	}
	other, err := secondJournal.ReserveCommand(
		ctx,
		workerapp.ReserveRuntimeControlCommandCommand{
			Authority: secondAuthority, MessageID: "other-session-start",
			IdempotencyKey: "other-session-start",
			Envelope: runtimeControlEnvelope(
				started[1],
				"command",
				0,
				"other-session-start",
				"other-session-start",
				"agent_turn.start",
			),
		},
	)
	if err != nil || other.Sequence != 1 {
		t.Fatalf("other-Session start=%+v err=%v", other, err)
	}
}

func TestTurnSettlementLeaseAndPhaseCASFenceConcurrentWorkers(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 22, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	if _, err := journal.AcceptEvent(ctx, workerapp.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "lease-terminal",
		IdempotencyKey: "lease-terminal",
		Envelope: runtimeControlEnvelope(
			started,
			"event",
			1,
			"lease-terminal",
			"lease-terminal",
			"agent_turn.terminal",
		),
	}); err != nil {
		t.Fatal(err)
	}
	current := now.Add(7 * time.Minute)
	service := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return current },
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	first, err := service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: 1, OwnerID: "settler-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: first.Revision, OwnerID: "settler-two",
	}); !errors.Is(err, workerapp.ErrTurnSettlementLeaseHeld) {
		t.Fatalf("concurrent claim error=%v", err)
	}
	if _, err := service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: 1, OwnerID: "settler-one",
	}); !errors.Is(err, workerapp.ErrTurnSettlementRevisionConflict) {
		t.Fatalf("stale claim revision error=%v", err)
	}
	frozen, err := service.FreezeCheckpoint(ctx, workerapp.FreezeTurnSettlementCheckpointCommand{
		SettlementID: settlementID, ExpectedRevision: first.Revision,
		LeaseOwnerID: first.LeaseOwnerID, LeaseFence: first.LeaseFence,
		Checkpoint: typedTurnSettlementCheckpoint(
			t,
			validTurnSettlementCheckpoint(
				t,
				started,
				workerapp.CompleteResidentStageCommand{},
				"artifact-report-1",
				"artifact-patch-1",
				current,
				workerapp.TurnSettlementAccepted,
			),
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	preparedCheckpoint := validTurnSettlementCheckpoint(
		t,
		started,
		workerapp.CompleteResidentStageCommand{},
		"artifact-report-1",
		"artifact-patch-1",
		current,
		workerapp.TurnSettlementEvidencePrepared,
	)
	advanced, err := service.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: frozen.Revision,
		LeaseOwnerID: frozen.LeaseOwnerID, LeaseFence: frozen.LeaseFence,
		FromPhase:  workerapp.TurnSettlementAccepted,
		ToPhase:    workerapp.TurnSettlementEvidencePrepared,
		Checkpoint: preparedCheckpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Phase != workerapp.TurnSettlementEvidencePrepared ||
		advanced.Revision != frozen.Revision+1 {
		t.Fatalf("advanced settlement=%+v", advanced)
	}
	staleCheckpoint := typedTurnSettlementCheckpoint(
		t,
		advanced.PhaseState,
	)
	staleCheckpoint.AdditionalFields["stale_probe"] =
		json.RawMessage(`{"status":"blocked"}`)
	if _, err := service.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: advanced.Revision,
			LeaseOwnerID: "settler-one", LeaseFence: first.LeaseFence + 1,
			Checkpoint: staleCheckpoint,
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
		t.Fatalf("stale lease fence error=%v", err)
	}

	current = first.LeaseExpiresAt.Add(time.Millisecond)
	reclaimed, err := service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: advanced.Revision, OwnerID: "settler-two",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.LeaseFence != first.LeaseFence+1 ||
		reclaimed.LeaseOwnerID != "settler-two" {
		t.Fatalf("reclaimed settlement=%+v", reclaimed)
	}
	if _, err := service.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: reclaimed.Revision,
			LeaseOwnerID: "settler-one", LeaseFence: first.LeaseFence,
			Checkpoint: staleCheckpoint,
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
		t.Fatalf("expired owner advance error=%v", err)
	}
}

func TestRuntimeControlTerminalCommitWaitsForTaskSettlementAndAppliesAtomically(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 23, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	start, err := journal.ReserveCommand(
		ctx,
		workerapp.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "settlement-atomic-start",
			IdempotencyKey: "settlement-atomic-start",
			Envelope: runtimeControlEnvelope(
				started,
				"command",
				0,
				"settlement-atomic-start",
				"settlement-atomic-start",
				"agent_turn.start",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, "active")
	if _, err := journal.ReconcileEffect(
		ctx,
		workerapp.ReconcileRuntimeControlEffectCommand{
			Authority: authority, CommandSequence: start.Sequence,
			MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
			Phase: workerapp.RuntimeControlEffectActive, ActiveTurn: &active,
		},
	); err != nil {
		t.Fatal(err)
	}
	terminalEnvelope := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(
			started,
			"event",
			1,
			"settlement-atomic-terminal",
			"settlement-atomic-terminal",
			"agent_turn.terminal",
		),
		start.Sequence,
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      "settlement-atomic-terminal",
			IdempotencyKey: "settlement-atomic-terminal",
			Envelope:       terminalEnvelope,
		},
	); err != nil {
		t.Fatal(err)
	}
	apply := workerapp.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1,
		MessageID:      "settlement-atomic-terminal",
		IdempotencyKey: "settlement-atomic-terminal-apply",
		Outcome: json.RawMessage(
			`{"status":"terminal_committed","effect_disposition":"commit"}`,
		),
	}
	if _, err := journal.ApplyEvent(ctx, apply); !errors.Is(
		err,
		workerapp.ErrTurnSettlementPhaseConflict,
	) {
		t.Fatalf("terminal Apply before task settlement error=%v", err)
	}
	assertTerminalSettlementApplyState(
		t,
		pool,
		started,
		workerapp.TurnSettlementAccepted,
		"active",
		"received",
		0,
	)

	settlementID, err := workerapp.TurnSettlementID(started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	settlementNow := now.Add(7 * time.Minute)
	settlements := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return settlementNow },
	}
	claimed, err := settlements.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: 1, OwnerID: "settler-atomic",
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	stage := workerapp.CompleteResidentStageCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		SessionTarget:           workerdomain.SessionInteractive,
		RunTarget:               workerdomain.RunCompleted,
		TurnTarget:              workerdomain.TurnCompleted,
		TerminalReason:          "end_turn",
	}
	current, err := settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: claimed.Revision,
			LeaseOwnerID: claimed.LeaseOwnerID, LeaseFence: claimed.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(
				t,
				validTurnSettlementCheckpoint(
					t,
					started,
					stage,
					"report-atomic",
					"patch-atomic",
					settlementNow,
					workerapp.TurnSettlementAccepted,
				),
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		"report-atomic",
		"patch-atomic",
		settlementNow,
		workerapp.TurnSettlementEvidencePrepared,
	)
	settlementNow = settlementNow.Add(time.Millisecond)
	current, err = settlements.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: current.Revision,
		LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
		FromPhase:  workerapp.TurnSettlementAccepted,
		ToPhase:    workerapp.TurnSettlementEvidencePrepared,
		Checkpoint: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	settlementNow = settlementNow.Add(time.Second)
	domainCommitted, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return settlementNow },
	}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{
		SettlementID:     settlementID,
		ExpectedRevision: current.Revision,
		LeaseOwnerID:     current.LeaseOwnerID,
		LeaseFence:       current.LeaseFence,
		Resident:         stage,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := domainCommitted.Resident
	current = domainCommitted.Settlement
	if completed.RunStatus != workerdomain.RunCompleted ||
		completed.TurnStatus != workerdomain.TurnCompleted {
		t.Fatalf("completed resident state=%+v", completed)
	}
	for _, next := range []workerapp.TurnSettlementPhase{
		workerapp.TurnSettlementEvidencePublished,
		workerapp.TurnSettlementTaskSettled,
	} {
		checkpoint = validTurnSettlementCheckpoint(
			t,
			started,
			stage,
			"report-atomic",
			"patch-atomic",
			now.Add(7*time.Minute),
			next,
		)
		settlementNow = settlementNow.Add(time.Second)
		current, err = settlements.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			FromPhase: current.Phase, ToPhase: next,
			Checkpoint: checkpoint,
		})
		if err != nil {
			t.Fatalf("advance to %s: %v", next, err)
		}
	}
	applyNow := settlementNow.Add(time.Second)
	apply.TurnSettlement = &workerapp.TurnSettlementApplyAuthority{
		SettlementID:     current.SettlementID,
		ExpectedRevision: current.Revision,
		LeaseOwnerID:     current.LeaseOwnerID,
		LeaseFence:       current.LeaseFence,
	}
	journal = workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return applyNow },
	}

	if _, err := pool.Exec(ctx, `
CREATE FUNCTION switchback_test_reject_terminal_settlement_apply()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.phase = 'terminal_event_applied' THEN
    RAISE EXCEPTION 'injected terminal settlement Apply failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER switchback_test_reject_terminal_settlement_apply
BEFORE UPDATE ON worker_turn_settlements
FOR EACH ROW
EXECUTE FUNCTION switchback_test_reject_terminal_settlement_apply()`); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ApplyEvent(ctx, apply); err == nil {
		t.Fatal("injected settlement failure did not fail terminal Apply")
	}
	assertTerminalSettlementApplyState(
		t,
		pool,
		started,
		workerapp.TurnSettlementTaskSettled,
		"active",
		"received",
		0,
	)
	if _, err := pool.Exec(ctx, `
DROP TRIGGER switchback_test_reject_terminal_settlement_apply ON worker_turn_settlements;
DROP FUNCTION switchback_test_reject_terminal_settlement_apply()`); err != nil {
		t.Fatal(err)
	}

	applied, err := journal.ApplyEvent(ctx, apply)
	if err != nil {
		t.Fatalf("apply durable terminal settlement: %v", err)
	}
	if applied.Channel.EventAppliedSequence != 1 ||
		applied.Channel.ActiveEffectPhase != workerapp.RuntimeControlEffectTerminal {
		t.Fatalf("terminal Apply=%+v", applied)
	}
	assertTerminalSettlementApplyState(
		t,
		pool,
		started,
		workerapp.TurnSettlementTerminalEventApplied,
		"terminal",
		"applied",
		1,
	)
	terminalSettlement, err := settlements.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	continuationCheckpoint := typedTurnSettlementCheckpoint(
		t,
		terminalSettlement.PhaseState,
	)
	continuationCheckpoint.AdditionalFields["continuation_plan"] =
		json.RawMessage(
			`{"action":"none","reason":"no_queued_turn"}`,
		)
	settlementNow = applyNow.Add(time.Millisecond)
	frozenContinuation, err := settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     settlementID,
			ExpectedRevision: terminalSettlement.Revision,
			LeaseOwnerID:     terminalSettlement.LeaseOwnerID,
			LeaseFence:       terminalSettlement.LeaseFence,
			Checkpoint:       continuationCheckpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	duplicateContinuation, err := settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     settlementID,
			ExpectedRevision: terminalSettlement.Revision,
			LeaseOwnerID:     terminalSettlement.LeaseOwnerID,
			LeaseFence:       terminalSettlement.LeaseFence,
			Checkpoint:       continuationCheckpoint,
		},
	)
	if err != nil || duplicateContinuation.Revision != frozenContinuation.Revision {
		t.Fatalf(
			"duplicate continuation freeze=%+v error=%v",
			duplicateContinuation,
			err,
		)
	}
	continuationCheckpoint.AdditionalFields["continuation_plan"] =
		json.RawMessage(`{"action":"create_queued_turn"}`)
	if _, err := settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     settlementID,
			ExpectedRevision: terminalSettlement.Revision,
			LeaseOwnerID:     terminalSettlement.LeaseOwnerID,
			LeaseFence:       terminalSettlement.LeaseFence,
			Checkpoint:       continuationCheckpoint,
		},
	); !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("conflicting continuation freeze error=%v", err)
	}
	duplicate, err := journal.ApplyEvent(ctx, apply)
	if err != nil || !duplicate.Message.Duplicate {
		t.Fatalf("duplicate terminal Apply=%+v err=%v", duplicate, err)
	}
	conflict := apply
	conflict.Outcome = json.RawMessage(
		`{"status":"different","effect_disposition":"commit"}`,
	)
	if _, err := journal.ApplyEvent(ctx, conflict); !errors.Is(
		err,
		workerapp.ErrRuntimeControlMessageConflict,
	) {
		t.Fatalf("conflicting terminal Apply error=%v", err)
	}
}

func TestRuntimeControlTerminalApplyRejectsExpiredSettlementOwnerAfterReclaim(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 2, 30, 0, 0, time.UTC)
	fixture := prepareTerminalSettlementApplyFixture(
		t,
		pool,
		now,
		"settlement-fence",
	)

	staleAuthority := workerapp.TurnSettlementApplyAuthority{
		SettlementID:     fixture.Settlement.SettlementID,
		ExpectedRevision: fixture.Settlement.Revision,
		LeaseOwnerID:     fixture.Settlement.LeaseOwnerID,
		LeaseFence:       fixture.Settlement.LeaseFence,
	}
	reclaimAt := fixture.Settlement.LeaseExpiresAt.Add(time.Microsecond)
	reclaimed, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return reclaimAt },
	}).Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID:     fixture.Settlement.SettlementID,
		ExpectedRevision: fixture.Settlement.Revision,
		OwnerID:          "settlement-fence-new-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.LeaseOwnerID != "settlement-fence-new-owner" ||
		reclaimed.LeaseFence != fixture.Settlement.LeaseFence+1 ||
		reclaimed.Phase != workerapp.TurnSettlementTaskSettled {
		t.Fatalf("reclaimed settlement=%+v", reclaimed)
	}

	before := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	applyAt := reclaimAt.Add(time.Microsecond)
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return applyAt },
	}
	apply := fixture.Apply
	apply.TurnSettlement = &staleAuthority
	if _, err := journal.ApplyEvent(ctx, apply); !errors.Is(
		err,
		workerapp.ErrTurnSettlementLeaseStale,
	) {
		t.Fatalf("expired settlement owner Apply error=%v", err)
	}
	afterStale := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	if afterStale != before {
		t.Fatalf(
			"stale terminal Apply mutated durable state:\nbefore=%+v\nafter=%+v",
			before,
			afterStale,
		)
	}

	apply.TurnSettlement = &workerapp.TurnSettlementApplyAuthority{
		SettlementID:     reclaimed.SettlementID,
		ExpectedRevision: reclaimed.Revision,
		LeaseOwnerID:     reclaimed.LeaseOwnerID,
		LeaseFence:       reclaimed.LeaseFence,
	}
	applied, err := journal.ApplyEvent(ctx, apply)
	if err != nil {
		t.Fatalf("new settlement owner Apply: %v", err)
	}
	if applied.Channel.EventAppliedSequence != 1 ||
		applied.Channel.ActiveEffectPhase !=
			workerapp.RuntimeControlEffectTerminal {
		t.Fatalf("new settlement owner Apply=%+v", applied)
	}
	afterSuccess := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	if afterSuccess.EventEffect != "applied" ||
		afterSuccess.SettlementPhase !=
			workerapp.TurnSettlementTerminalEventApplied ||
		afterSuccess.SettlementRevision != reclaimed.Revision+1 ||
		afterSuccess.EventAppliedSequence != 1 {
		t.Fatalf("successful fenced terminal Apply state=%+v", afterSuccess)
	}
}

func TestRuntimeControlTerminalApplyRejectsWeakTaskProofBeforeInboxMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 3, 15, 0, 0, time.UTC)
	fixture := prepareTerminalSettlementApplyFixture(
		t,
		pool,
		now,
		"terminal-weak-proof",
	)
	weak, digest := removeTurnSettlementCheckpointField(
		t,
		fixture.Settlement.PhaseState,
		"task_result",
	)
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET phase_state=$2,phase_state_digest=$3
WHERE settlement_id=$1`,
		fixture.Settlement.SettlementID,
		weak,
		digest,
	); err != nil {
		t.Fatal(err)
	}
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now: func() time.Time {
			return fixture.Settlement.UpdatedAt.Add(time.Second)
		},
	}
	fixture.Apply.TurnSettlement = &workerapp.TurnSettlementApplyAuthority{
		SettlementID:     fixture.Settlement.SettlementID,
		ExpectedRevision: fixture.Settlement.Revision,
		LeaseOwnerID:     fixture.Settlement.LeaseOwnerID,
		LeaseFence:       fixture.Settlement.LeaseFence,
	}
	if _, err := journal.ApplyEvent(
		ctx,
		fixture.Apply,
	); !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("weak Task proof terminal Apply error=%v", err)
	}
	after := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	if after.SettlementPhase != workerapp.TurnSettlementTaskSettled ||
		after.EventEffect != "received" ||
		after.EventAppliedSequence != 0 {
		t.Fatalf("weak Task proof mutated terminal Apply state=%+v", after)
	}
}

func TestRuntimeControlIgnoredTerminalCompletesWithoutBusinessPhasesAndUnblocksInbox(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 23, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      "settlement-ignore-terminal",
			IdempotencyKey: "settlement-ignore-terminal",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"settlement-ignore-terminal",
				"settlement-ignore-terminal",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	ignore := workerapp.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1,
		MessageID:      "settlement-ignore-terminal",
		IdempotencyKey: "settlement-ignore-terminal-apply",
		Outcome: json.RawMessage(
			`{"status":"stale_terminal","effect_disposition":"ignore"}`,
		),
	}
	applied, err := journal.ApplyEvent(ctx, ignore)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Channel.EventAppliedSequence != 1 {
		t.Fatalf("ignored terminal Apply=%+v", applied)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	settlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Phase != workerapp.TurnSettlementCompleted ||
		settlement.TerminalEventDisposition != workerapp.RuntimeControlEffectDispositionIgnore ||
		settlement.TerminalApplyOutcomeDigest == "" ||
		settlement.TerminalEventAppliedAt.IsZero() ||
		settlement.CompletedAt.IsZero() {
		t.Fatalf("ignored terminal settlement=%+v", settlement)
	}
	duplicate, err := journal.ApplyEvent(ctx, ignore)
	if err != nil || !duplicate.Message.Duplicate {
		t.Fatalf("duplicate ignored terminal=%+v err=%v", duplicate, err)
	}

	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 2,
			MessageID:      "after-ignored-terminal",
			IdempotencyKey: "after-ignored-terminal",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				2,
				"after-ignored-terminal",
				"after-ignored-terminal",
				"runtime.update",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	next, err := journal.ApplyEvent(ctx, workerapp.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 2,
		MessageID:      "after-ignored-terminal",
		IdempotencyKey: "after-ignored-terminal-apply",
		Outcome:        json.RawMessage(`{"status":"applied"}`),
	})
	if err != nil || next.Channel.EventAppliedSequence != 2 {
		t.Fatalf("event after ignored terminal=%+v err=%v", next, err)
	}
}

type terminalSettlementApplyFixture struct {
	Settlement workerapp.TurnSettlement
	Started    workerapp.StartedWorkerRun
	MessageID  string
	Apply      workerapp.ApplyRuntimeControlEventCommand
}

func prepareTerminalSettlementApplyFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
	prefix string,
	beforeFreeze ...func(workerapp.TurnSettlement, workerapp.FreezeTurnSettlementCheckpointCommand),
) terminalSettlementApplyFixture {
	t.Helper()
	ctx := context.Background()
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	startMessageID := prefix + "-start"
	start, err := journal.ReserveCommand(
		ctx,
		workerapp.ReserveRuntimeControlCommandCommand{
			Authority:      authority,
			MessageID:      startMessageID,
			IdempotencyKey: startMessageID,
			Envelope: runtimeControlEnvelope(
				started,
				"command",
				0,
				startMessageID,
				startMessageID,
				"agent_turn.start",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, "active")
	if _, err := journal.ReconcileEffect(
		ctx,
		workerapp.ReconcileRuntimeControlEffectCommand{
			Authority: authority, CommandSequence: start.Sequence,
			MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
			Phase: workerapp.RuntimeControlEffectActive, ActiveTurn: &active,
		},
	); err != nil {
		t.Fatal(err)
	}

	terminalMessageID := prefix + "-terminal"
	terminalEnvelope := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(
			started,
			"event",
			1,
			terminalMessageID,
			terminalMessageID,
			"agent_turn.terminal",
		),
		start.Sequence,
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID: terminalMessageID, IdempotencyKey: terminalMessageID,
			Envelope: terminalEnvelope,
		},
	); err != nil {
		t.Fatal(err)
	}

	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	stage := workerapp.CompleteResidentStageCommand{
		TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID,
		WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		SessionTarget:           workerdomain.SessionInteractive,
		RunTarget:               workerdomain.RunCompleted,
		TurnTarget:              workerdomain.TurnCompleted,
		TerminalReason:          "end_turn",
	}
	currentTime := now.Add(7 * time.Minute)
	settlements := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}
	settlementID, err := workerapp.TurnSettlementID(started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := settlements.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			OwnerID: prefix + "-old-owner",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reportArtifactID := prefix + "-report"
	patchArtifactID := prefix + "-patch"
	if len(beforeFreeze) != 0 {
		beforeFreeze[0](current, workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(t, validTurnSettlementCheckpoint(t, started, stage, reportArtifactID, patchArtifactID, currentTime, workerapp.TurnSettlementAccepted)),
		})
		return terminalSettlementApplyFixture{Settlement: current, Started: started, MessageID: terminalMessageID}
	}
	current, err = settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(
				t,
				validTurnSettlementCheckpoint(
					t,
					started,
					stage,
					reportArtifactID,
					patchArtifactID,
					currentTime,
					workerapp.TurnSettlementAccepted,
				),
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		reportArtifactID,
		patchArtifactID,
		currentTime,
		workerapp.TurnSettlementEvidencePrepared,
	)
	currentTime = currentTime.Add(time.Second)
	current, err = settlements.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			FromPhase:  current.Phase,
			ToPhase:    workerapp.TurnSettlementEvidencePrepared,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(time.Second)
	domainCommitted, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}).Commit(ctx, workerapp.CommitTurnSettlementDomainCommand{
		SettlementID: settlementID, ExpectedRevision: current.Revision,
		LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
		Resident: stage,
	})
	if err != nil {
		t.Fatal(err)
	}
	current = domainCommitted.Settlement

	checkpoint = validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		reportArtifactID,
		patchArtifactID,
		now.Add(7*time.Minute),
		workerapp.TurnSettlementEvidencePublished,
	)
	currentTime = currentTime.Add(time.Second)
	current, err = settlements.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			FromPhase:  current.Phase,
			ToPhase:    workerapp.TurnSettlementEvidencePublished,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint = validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		reportArtifactID,
		patchArtifactID,
		now.Add(7*time.Minute),
		workerapp.TurnSettlementTaskSettled,
	)
	currentTime = currentTime.Add(time.Second)
	current, err = settlements.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			FromPhase:  current.Phase,
			ToPhase:    workerapp.TurnSettlementTaskSettled,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return terminalSettlementApplyFixture{
		Settlement: current,
		Started:    started,
		MessageID:  terminalMessageID,
		Apply: workerapp.ApplyRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      terminalMessageID,
			IdempotencyKey: prefix + "-terminal-apply",
			Outcome: json.RawMessage(
				`{"status":"terminal_committed","effect_disposition":"commit"}`,
			),
		},
	}
}

type terminalSettlementApplySnapshot struct {
	EventEffect                string
	EventApplyIdempotencyKey   string
	EventApplyOutcomeDigest    string
	EventOutcome               string
	SettlementPhase            workerapp.TurnSettlementPhase
	SettlementRevision         uint64
	SettlementPhaseStateDigest string
	SettlementDisposition      string
	SettlementOutcomeDigest    string
	EventAppliedSequence       uint64
	ChannelRevision            uint64
}

func readTerminalSettlementApplySnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	settlementID string,
	messageID string,
) terminalSettlementApplySnapshot {
	t.Helper()
	var snapshot terminalSettlementApplySnapshot
	if err := pool.QueryRow(context.Background(), `
SELECT event.effect_status,
       COALESCE(event.apply_idempotency_key,''),
       COALESCE(event.apply_outcome_digest,''),
       COALESCE(event.outcome::text,''),
       settlement.phase,
       settlement.revision,
       settlement.phase_state_digest,
       COALESCE(settlement.terminal_event_disposition,''),
       COALESCE(settlement.terminal_apply_outcome_digest,''),
       channel.event_applied_sequence,
       channel.revision
FROM worker_turn_settlements settlement
JOIN runtime_control_messages event
  ON event.runtime_instance_id=settlement.runtime_instance_id
 AND event.direction='event'
 AND event.message_id=$2
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=settlement.runtime_instance_id
WHERE settlement.settlement_id=$1`,
		settlementID,
		messageID,
	).Scan(
		&snapshot.EventEffect,
		&snapshot.EventApplyIdempotencyKey,
		&snapshot.EventApplyOutcomeDigest,
		&snapshot.EventOutcome,
		&snapshot.SettlementPhase,
		&snapshot.SettlementRevision,
		&snapshot.SettlementPhaseStateDigest,
		&snapshot.SettlementDisposition,
		&snapshot.SettlementOutcomeDigest,
		&snapshot.EventAppliedSequence,
		&snapshot.ChannelRevision,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func marshalTurnSettlementTestValue(
	t *testing.T,
	value any,
) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertTerminalSettlementApplyState(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	wantPhase workerapp.TurnSettlementPhase,
	wantStartEffect string,
	wantEventEffect string,
	wantAppliedSequence uint64,
) {
	t.Helper()
	var phase workerapp.TurnSettlementPhase
	var startEffect, eventEffect string
	var appliedSequence uint64
	err := pool.QueryRow(context.Background(), `
SELECT settlement.phase,start.effect_status,event.effect_status,channel.event_applied_sequence
FROM worker_turn_settlements settlement
JOIN runtime_control_messages start
  ON start.runtime_instance_id=settlement.runtime_instance_id
 AND start.direction='command'
 AND start.message_id='settlement-atomic-start'
JOIN runtime_control_messages event
  ON event.runtime_instance_id=settlement.runtime_instance_id
 AND event.direction='event'
 AND event.message_id='settlement-atomic-terminal'
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=settlement.runtime_instance_id
WHERE settlement.agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(&phase, &startEffect, &eventEffect, &appliedSequence)
	if err != nil {
		t.Fatal(err)
	}
	if phase != wantPhase || startEffect != wantStartEffect ||
		eventEffect != wantEventEffect || appliedSequence != wantAppliedSequence {
		t.Fatalf(
			"terminal Apply state phase=%s start=%s event=%s applied=%d; want %s/%s/%s/%d",
			phase,
			startEffect,
			eventEffect,
			appliedSequence,
			wantPhase,
			wantStartEffect,
			wantEventEffect,
			wantAppliedSequence,
		)
	}
}

func prepareTurnSettlementDomainCommit(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
) (
	workerapp.TurnSettlement,
	workerapp.CompleteResidentStageCommand,
) {
	t.Helper()
	ctx := context.Background()
	started, _ := startActiveRecoveryFixture(t, pool, now)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: 1,
			MessageID:      "domain-terminal-" + started.AgentTurnID,
			IdempotencyKey: "domain-terminal-" + started.AgentTurnID,
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"domain-terminal-"+started.AgentTurnID,
				"domain-terminal-"+started.AgentTurnID,
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	stage := workerapp.CompleteResidentStageCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		SessionTarget:           workerdomain.SessionInteractive,
		RunTarget:               workerdomain.RunCompleted,
		TurnTarget:              workerdomain.TurnCompleted,
		TerminalReason:          "end_turn",
	}
	currentTime := now.Add(7 * time.Minute)
	settlements := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	current, err := settlements.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			OwnerID: "domain-settler",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err = settlements.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(
				t,
				validTurnSettlementCheckpoint(
					t,
					started,
					stage,
					"domain-report-"+started.AgentTurnID,
					"domain-patch-"+started.AgentTurnID,
					currentTime,
					workerapp.TurnSettlementAccepted,
				),
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		"domain-report-"+started.AgentTurnID,
		"domain-patch-"+started.AgentTurnID,
		currentTime,
		workerapp.TurnSettlementEvidencePrepared,
	)
	currentTime = currentTime.Add(time.Millisecond)
	current, err = settlements.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			FromPhase:  current.Phase,
			ToPhase:    workerapp.TurnSettlementEvidencePrepared,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return current, stage
}

func advanceTerminalSettlementToTaskSettled(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	now time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	ctx := context.Background()
	settlementID, err := workerapp.TurnSettlementID(started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	currentTime := now
	service := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}
	current, err := service.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	current, err = service.Claim(ctx, workerapp.ClaimTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: current.Revision,
		OwnerID: "legacy-terminal-test-settler",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err = service.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			Checkpoint: workerapp.TurnSettlementCheckpoint{
				SchemaVersion: workerapp.TurnSettlementCheckpointSchemaVersion,
				FrozenArtifacts: &workerapp.FrozenArtifactMetadata{
					ReportArtifactID: "legacy-terminal-report",
					PatchArtifactID:  "legacy-terminal-patch",
					CreatedAt:        currentTime,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_run_id=$2
JOIN agent_turns turn ON turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	stage := workerapp.CompleteResidentStageCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         started.WorkerSessionID,
		WorkerRunID:             started.WorkerRunID,
		AgentTurnID:             started.AgentTurnID,
		RuntimeInstanceID:       started.RuntimeInstanceID,
		RuntimeGeneration:       started.RuntimeGeneration,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		SessionTarget:           workerdomain.SessionInteractive,
		RunTarget:               workerdomain.RunCompleted,
		TurnTarget:              workerdomain.TurnCompleted,
		TerminalReason:          "end_turn",
	}
	checkpoint := validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		"legacy-terminal-report",
		"legacy-terminal-patch",
		currentTime,
		workerapp.TurnSettlementTaskSettled,
	)
	canonical, digest, err :=
		workerapp.CanonicalizeTurnSettlementCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementTaskSettled,
		canonical,
	); err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(time.Millisecond)
	var revision uint64
	if err := pool.QueryRow(ctx, `
UPDATE worker_turn_settlements
SET phase='task_settled',
    phase_state=$2,
    phase_state_digest=$3,
    revision=revision+1,
    updated_at=$4
WHERE settlement_id=$1
  AND revision=$5
  AND phase='accepted'
  AND lease_owner_id=$6
  AND lease_fence=$7
RETURNING revision`,
		settlementID,
		canonical,
		digest,
		currentTime,
		current.Revision,
		current.LeaseOwnerID,
		current.LeaseFence,
	).Scan(&revision); err != nil {
		t.Fatalf("materialize legacy task-settled fixture: %v", err)
	}
	current, err = service.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != revision ||
		current.Phase != workerapp.TurnSettlementTaskSettled {
		t.Fatalf("legacy task-settled fixture=%+v", current)
	}
	return current
}

func turnSettlementApplyAuthority(
	settlement workerapp.TurnSettlement,
) *workerapp.TurnSettlementApplyAuthority {
	return &workerapp.TurnSettlementApplyAuthority{
		SettlementID:     settlement.SettlementID,
		ExpectedRevision: settlement.Revision,
		LeaseOwnerID:     settlement.LeaseOwnerID,
		LeaseFence:       settlement.LeaseFence,
	}
}

func dropTurnSettlementSchemaForMigrationFixture(
	t *testing.T,
	pool *pgxpool.Pool,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
ALTER TABLE runtime_recovery_incidents
    DROP CONSTRAINT IF EXISTS runtime_recovery_incidents_deadline_settlement_check,
    DROP COLUMN IF EXISTS preempted_by_turn_settlement_id;
DROP TABLE worker_turn_settlements`); err != nil {
		t.Fatal(err)
	}
}

func completeAppliedTerminalSettlement(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	now time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	ctx := context.Background()
	settlementID, err := workerapp.TurnSettlementID(started.AgentTurnID)
	if err != nil {
		t.Fatal(err)
	}
	currentTime := now
	service := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}
	current, err := service.Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != workerapp.TurnSettlementTerminalEventApplied {
		t.Fatalf("complete settlement from phase %s", current.Phase)
	}
	checkpoint := typedTurnSettlementCheckpoint(t, current.PhaseState)
	checkpoint.AdditionalFields["continuation_plan"] =
		json.RawMessage(`{"action":"none","reason":"no_queued_turn"}`)
	current, err = service.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: settlementID, ExpectedRevision: current.Revision,
			LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(time.Millisecond)
	continuationCheckpoint := addTurnSettlementCheckpointField(
		t,
		current.PhaseState,
		"continuation_result",
		json.RawMessage(`{"status":"not_applicable"}`),
	)
	current, err = service.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: current.Revision,
		LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
		FromPhase: current.Phase, ToPhase: workerapp.TurnSettlementContinuationSettled,
		Checkpoint: continuationCheckpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(time.Millisecond)
	current, err = service.Advance(ctx, workerapp.AdvanceTurnSettlementCommand{
		SettlementID: settlementID, ExpectedRevision: current.Revision,
		LeaseOwnerID: current.LeaseOwnerID, LeaseFence: current.LeaseFence,
		FromPhase: current.Phase, ToPhase: workerapp.TurnSettlementCompleted,
		Checkpoint: current.PhaseState,
	})
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func startActiveSettlementFixtures(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
	count int,
) []workerapp.StartedWorkerRun {
	t.Helper()
	ctx := context.Background()
	seedAllocationRequests(t, pool, count, now)
	ids := &concurrentAllocationIDs{}
	started := make([]workerapp.StartedWorkerRun, 0, count)
	for sequence := 1; sequence <= count; sequence++ {
		allocationRequestID := fmt.Sprintf("allocation-%02d", sequence)
		taskID := fmt.Sprintf("task-%02d", sequence)
		allocated, err := (workerapp.WorkerSessionAllocator{
			Store: workerpostgres.NewAllocatorStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return now.Add(time.Minute) },
		}).Execute(ctx, workerapp.AllocateWorkerSessionCommand{
			TenantID:            "tenant-local",
			UserID:              "user-local",
			AllocationRequestID: allocationRequestID,
		})
		if err != nil {
			t.Fatalf("allocate settlement fixture %d: %v", sequence, err)
		}
		if _, err := pool.Exec(ctx, `
UPDATE worker_sessions
SET status='interactive',runtime_generation=1,session_revision=2,updated_at=$2
WHERE worker_session_id=$1`,
			allocated.WorkerSessionID,
			now.Add(2*time.Minute),
		); err != nil {
			t.Fatalf("activate settlement fixture %d: %v", sequence, err)
		}
		dispatch := taskapp.SessionDispatchService{
			Store:     taskpostgres.NewStore(pool),
			IDs:       ids,
			Now:       func() time.Time { return now.Add(3 * time.Minute) },
			PermitTTL: 5 * time.Minute,
		}
		if _, err := dispatch.EnqueueTask(ctx, taskapp.EnqueueTaskCommand{
			TenantID: "tenant-local", UserID: "user-local", TaskID: taskID,
			WorkerSessionID: allocated.WorkerSessionID, ExpectedTaskRevision: 1,
			ExpectedSessionRevision: 2,
			IdempotencyKey:          "enqueue-" + taskID,
		}); err != nil {
			t.Fatalf("enqueue settlement fixture %d: %v", sequence, err)
		}
		permit, err := dispatch.IssueTaskPermit(ctx, taskapp.IssueTaskPermitCommand{
			TenantID: "tenant-local", UserID: "user-local",
			WorkerSessionID:   allocated.WorkerSessionID,
			RuntimeGeneration: 1, CapacityAdmissionRef: "capacity-local",
			IdempotencyKey: "permit-" + taskID,
		})
		if err != nil {
			t.Fatalf("issue settlement fixture permit %d: %v", sequence, err)
		}
		fixture, err := (workerapp.StartWorkerRunService{
			Store: workerpostgres.NewRunStore(pool),
			IDs:   ids,
			Now:   func() time.Time { return now.Add(4 * time.Minute) },
		}).Execute(ctx, residentStartCommand(permit))
		if err != nil {
			t.Fatalf("start settlement fixture %d: %v", sequence, err)
		}
		if _, err := (workerapp.RuntimeResidencyService{
			Store: workerpostgres.NewRuntimeResidencyStore(pool),
			Now:   func() time.Time { return now.Add(5 * time.Minute) },
		}).Activate(ctx, workerapp.ActivateResidentRuntimeCommand{
			TenantID: "tenant-local", WorkerSessionID: fixture.WorkerSessionID,
			WorkerRunID: fixture.WorkerRunID, AgentTurnID: fixture.AgentTurnID,
			RuntimeInstanceID: fixture.RuntimeInstanceID,
			ContainerID:       fmt.Sprintf("container-settlement-%02d", sequence),
			RuntimeGeneration: 1, ControlChannelEpoch: 1,
			ExpectedSessionRevision: fixture.SessionRevision,
			ExpectedRunRevision:     1,
			ExpectedTurnRevision:    1,
			ExpectedRuntimeRevision: 1,
		}); err != nil {
			t.Fatalf("activate settlement Runtime %d: %v", sequence, err)
		}
		started = append(started, fixture)
	}
	return started
}

const turnSettlementTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validTurnSettlementCheckpoint(
	t *testing.T,
	started workerapp.StartedWorkerRun,
	stage workerapp.CompleteResidentStageCommand,
	reportArtifactID string,
	patchArtifactID string,
	recordedAt time.Time,
	phase workerapp.TurnSettlementPhase,
) json.RawMessage {
	t.Helper()
	if stage.ExpectedSessionRevision == 0 {
		stage = workerapp.CompleteResidentStageCommand{
			ExpectedSessionRevision: 1,
			ExpectedRunRevision:     1,
			ExpectedTurnRevision:    1,
			ExpectedRuntimeRevision: 1,
			SessionTarget:           workerdomain.SessionInteractive,
			RunTarget:               workerdomain.RunCompleted,
			TurnTarget:              workerdomain.TurnCompleted,
			TerminalReason:          "end_turn",
		}
	}
	recordedAt = recordedAt.UTC().Truncate(time.Microsecond)
	checkpoint := map[string]any{
		"schema_version": 1,
		"frozen_artifacts": map[string]any{
			"report_artifact_id": reportArtifactID,
			"patch_artifact_id":  patchArtifactID,
			"created_at":         recordedAt,
		},
		"source": map[string]any{
			"kind":        "runtime_event",
			"reason":      stage.TerminalReason,
			"occurred_at": recordedAt,
		},
		"evidence_input": map[string]any{
			"tenant_id":                  "tenant-local",
			"user_id":                    "user-local",
			"workspace_id":               "workspace-local",
			"worker_session_id":          started.WorkerSessionID,
			"worker_run_id":              started.WorkerRunID,
			"agent_session_id":           started.AgentSessionID,
			"agent_turn_id":              started.AgentTurnID,
			"runtime_instance_id":        started.RuntimeInstanceID,
			"runtime_generation":         started.RuntimeGeneration,
			"network_policy_id":          "trusted_full_network_v1",
			"network_policy_revision":    1,
			"network_policy_digest":      turnSettlementTestDigest,
			"network_enforcement_status": "enforced",
			"trace_start_offset":         0,
			"trace_end_offset":           10,
			"trace_digest":               turnSettlementTestDigest,
			"patch_snapshot_file":        "turn-settlement-patch.patch",
			"patch_size":                 0,
			"no_files_changed":           true,
			"patch_digest":               turnSettlementTestDigest,
			"server_recorded_at":         recordedAt,
			"worker_recorded_at":         recordedAt,
			"report_artifact_id":         reportArtifactID,
			"patch_artifact_id":          patchArtifactID,
			"artifact_created_at":        recordedAt,
			"source_checkpoint_id":       "working-copy-start-run-1",
			"source_checkpoint_digest":   turnSettlementTestDigest,
		},
		"domain_plan": map[string]any{
			"session_target":            stage.SessionTarget,
			"run_target":                stage.RunTarget,
			"turn_target":               stage.TurnTarget,
			"terminal_reason":           stage.TerminalReason,
			"expected_session_revision": stage.ExpectedSessionRevision,
			"expected_run_revision":     stage.ExpectedRunRevision,
			"expected_turn_revision":    stage.ExpectedTurnRevision,
			"expected_runtime_revision": stage.ExpectedRuntimeRevision,
		},
	}
	if turnSettlementTestPhaseAtLeast(
		phase,
		workerapp.TurnSettlementEvidencePrepared,
	) {
		checkpoint["evidence"] = map[string]any{
			"manifest_digest": turnSettlementTestDigest,
			"finalized": validFinalizedTurnEvidenceCheckpoint(
				started,
				reportArtifactID,
				patchArtifactID,
				recordedAt,
			),
		}
	}
	if turnSettlementTestPhaseAtLeast(
		phase,
		workerapp.TurnSettlementDomainCommitted,
	) {
		checkpoint["domain"] = map[string]any{
			"session_revision": stage.ExpectedSessionRevision + 1,
			"run_revision":     stage.ExpectedRunRevision + 1,
			"turn_revision":    stage.ExpectedTurnRevision + 1,
			"runtime_revision": stage.ExpectedRuntimeRevision,
		}
	}
	if turnSettlementTestPhaseAtLeast(
		phase,
		workerapp.TurnSettlementEvidencePublished,
	) {
		checkpoint["publication"] = map[string]any{
			"trace_stream_id": "trace-turn-1",
			"artifact_ids": []string{
				reportArtifactID,
				patchArtifactID,
			},
		}
	}
	if turnSettlementTestPhaseAtLeast(
		phase,
		workerapp.TurnSettlementTaskSettled,
	) {
		checkpoint["task_plan"] = map[string]any{
			"action": "none",
			"reason": "no_task",
		}
		checkpoint["task_result"] = map[string]any{
			"status": "not_applicable",
		}
	}
	if turnSettlementTestPhaseAtLeast(
		phase,
		workerapp.TurnSettlementTerminalEventApplied,
	) {
		checkpoint["terminal_source_applied"] = true
	}
	if turnSettlementTestPhaseAtLeast(
		phase,
		workerapp.TurnSettlementContinuationSettled,
	) {
		checkpoint["continuation_plan"] = map[string]any{
			"action": "none",
			"reason": "no_queued_turn",
		}
		checkpoint["continuation_result"] = map[string]any{
			"status": "not_applicable",
		}
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func validFinalizedTurnEvidenceCheckpoint(
	started workerapp.StartedWorkerRun,
	reportArtifactID string,
	patchArtifactID string,
	recordedAt time.Time,
) map[string]any {
	sha256 := strings.TrimPrefix(turnSettlementTestDigest, "sha256:")
	stored := func(key string, size int64) map[string]any {
		return map[string]any{
			"Key":      key,
			"Size":     size,
			"Checksum": turnSettlementTestDigest,
		}
	}
	artifact := func(
		artifactID string,
		artifactType string,
		key string,
		size int64,
	) map[string]any {
		return map[string]any{
			"schema_version":      "artifact.v1",
			"artifact_id":         artifactID,
			"tenant_id":           "tenant-local",
			"user_id":             "user-local",
			"workspace_id":        "workspace-local",
			"worker_session_id":   started.WorkerSessionID,
			"worker_run_id":       started.WorkerRunID,
			"agent_turn_id":       started.AgentTurnID,
			"runtime_instance_id": started.RuntimeInstanceID,
			"runtime_generation":  started.RuntimeGeneration,
			"type":                artifactType,
			"uri":                 "object://" + key,
			"sha256":              sha256,
			"size_bytes":          size,
			"permission": map[string]any{
				"visibility":      "workspace",
				"policy_revision": "artifact-policy-v1",
			},
			"retention_policy_ref": "retention-v1",
			"storage_tier":         "hot",
			"availability":         "online",
			"completeness":         "complete",
			"completeness_reason":  "",
			"source_checkpoint_ref": map[string]any{
				"checkpoint_id": "working-copy-start-run-1",
				"digest":        turnSettlementTestDigest,
			},
			"available_actions": []string{"preview", "download"},
			"created_at":        recordedAt,
		}
	}
	return map[string]any{
		"Manifest": map[string]any{
			"schema_version":      "trace.manifest.v1",
			"trace_stream_id":     "trace-turn-1",
			"tenant_id":           "tenant-local",
			"user_id":             "user-local",
			"workspace_id":        "workspace-local",
			"worker_session_id":   started.WorkerSessionID,
			"worker_run_id":       started.WorkerRunID,
			"agent_turn_id":       started.AgentTurnID,
			"runtime_instance_id": started.RuntimeInstanceID,
			"runtime_generation":  started.RuntimeGeneration,
			"segments": []map[string]any{
				{
					"kind":           "trace-raw-acp",
					"uri":            "object://objects/raw",
					"first_sequence": 1,
					"last_sequence":  1,
					"sha256":         sha256,
					"size_bytes":     10,
					"record_count":   1,
				},
				{
					"kind":           "trace-normalized",
					"uri":            "object://objects/normalized",
					"first_sequence": 1,
					"last_sequence":  1,
					"sha256":         sha256,
					"size_bytes":     12,
					"record_count":   1,
				},
			},
			"first_recorded_at":    recordedAt,
			"last_recorded_at":     recordedAt,
			"schema_versions":      []string{"trace.record.v1"},
			"lifecycle_policy_ref": "trace-policy-v1",
			"storage_tier":         "hot",
			"availability":         "online",
			"analytics_cursor":     "1",
			"langfuse_export": map[string]any{
				"status":   "pending",
				"trace_id": "langfuse-turn-1",
			},
		},
		"ManifestObject": stored("objects/manifest", 20),
		"ArtifactRefs": []map[string]any{
			artifact(reportArtifactID, "report", "objects/report", 11),
			artifact(patchArtifactID, "patch", "objects/patch", 0),
		},
		"Objects": []map[string]any{
			{"Kind": "trace-raw-acp", "Stored": stored("objects/raw", 10)},
			{
				"Kind":   "trace-normalized",
				"Stored": stored("objects/normalized", 12),
			},
			{"Kind": "report", "Stored": stored("objects/report", 11)},
			{"Kind": "patch", "Stored": stored("objects/patch", 0)},
		},
	}
}

func turnSettlementTestPhaseAtLeast(
	current workerapp.TurnSettlementPhase,
	required workerapp.TurnSettlementPhase,
) bool {
	rank := func(phase workerapp.TurnSettlementPhase) int {
		switch phase {
		case workerapp.TurnSettlementAccepted:
			return 1
		case workerapp.TurnSettlementEvidencePrepared:
			return 2
		case workerapp.TurnSettlementDomainCommitted:
			return 3
		case workerapp.TurnSettlementEvidencePublished:
			return 4
		case workerapp.TurnSettlementTaskSettled:
			return 5
		case workerapp.TurnSettlementTerminalEventApplied:
			return 6
		case workerapp.TurnSettlementContinuationSettled:
			return 7
		case workerapp.TurnSettlementCompleted:
			return 8
		default:
			return 0
		}
	}
	return rank(current) >= rank(required)
}

func removeTurnSettlementCheckpointField(
	t *testing.T,
	current json.RawMessage,
	name string,
) (json.RawMessage, string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil {
		t.Fatalf("decode cumulative checkpoint: %v", err)
	}
	if _, exists := fields[name]; !exists {
		t.Fatalf("checkpoint field %q is missing", name)
	}
	delete(fields, name)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode weakened checkpoint: %v", err)
	}
	canonical, digest, err :=
		workerapp.CanonicalizeTurnSettlementCheckpoint(encoded)
	if err != nil {
		t.Fatalf("canonicalize weakened checkpoint: %v", err)
	}
	return canonical, digest
}

func addTurnSettlementCheckpointField(
	t *testing.T,
	current json.RawMessage,
	name string,
	value json.RawMessage,
) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil {
		t.Fatalf("decode cumulative checkpoint: %v", err)
	}
	if fields == nil {
		t.Fatal("cumulative checkpoint must be an object")
	}
	fields[name] = append(json.RawMessage(nil), value...)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode cumulative checkpoint: %v", err)
	}
	return encoded
}

func typedTurnSettlementCheckpoint(
	t *testing.T,
	state json.RawMessage,
) workerapp.TurnSettlementCheckpoint {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(state, &fields); err != nil {
		t.Fatalf("decode typed checkpoint: %v", err)
	}
	var schemaVersion uint32
	if err := json.Unmarshal(fields["schema_version"], &schemaVersion); err != nil {
		t.Fatalf("decode checkpoint schema version: %v", err)
	}
	var frozen workerapp.FrozenArtifactMetadata
	if err := json.Unmarshal(fields["frozen_artifacts"], &frozen); err != nil {
		t.Fatalf("decode frozen Artifact metadata: %v", err)
	}
	delete(fields, "schema_version")
	delete(fields, "frozen_artifacts")
	return workerapp.TurnSettlementCheckpoint{
		SchemaVersion:    schemaVersion,
		FrozenArtifacts:  &frozen,
		AdditionalFields: fields,
	}
}
