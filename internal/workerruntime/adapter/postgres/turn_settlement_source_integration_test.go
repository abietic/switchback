//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestBackendDeadlineTurnSettlementClaimPersistsAcrossRestartAndIsIdempotent(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	command := backendDeadlineClaimCommand(started, deadlineAt, sourceRef)
	claimedAt := deadlineAt.Add(time.Second)
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return claimedAt },
	}

	settlement, err := service.ClaimBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.SettlementID !=
		"turn-settlement/"+started.AgentTurnID ||
		settlement.TenantID != "tenant-local" ||
		settlement.WorkerSessionID != started.WorkerSessionID ||
		settlement.WorkerRunID != started.WorkerRunID ||
		settlement.AgentSessionID != started.AgentSessionID ||
		settlement.AgentTurnID != started.AgentTurnID ||
		settlement.RuntimeInstanceID != "" ||
		settlement.RuntimeGeneration != 0 ||
		settlement.ChannelEpoch != 0 ||
		settlement.TerminalEventSequence != 0 ||
		settlement.TerminalMessageID != "" ||
		settlement.TerminalIdempotencyKey != "" ||
		settlement.TerminalEnvelopeDigest != "" ||
		settlement.Source.Kind !=
			workerapp.TurnSettlementSourceBackendDeadline ||
		settlement.Source.Reason != "agent_turn_deadline" ||
		!settlement.Source.OccurredAt.Equal(deadlineAt) ||
		settlement.Source.Ref != command.SourceRef ||
		settlement.Source.Digest == "" ||
		settlement.Source.StateDigest == "" ||
		settlement.Phase != workerapp.TurnSettlementAccepted ||
		settlement.Revision != 1 ||
		!settlement.AcceptedAt.Equal(claimedAt) {
		t.Fatalf("Backend deadline settlement=%+v", settlement)
	}
	if err := workerapp.ValidateTurnSettlementSource(
		settlement.Source,
	); err != nil {
		t.Fatalf("durable Backend deadline source is invalid: %v", err)
	}
	var count int
	var eventWitnessIsNull bool
	if err := pool.QueryRow(ctx, `
SELECT count(*),
       bool_and(
         runtime_instance_id IS NULL
         AND runtime_generation IS NULL
         AND channel_epoch IS NULL
         AND terminal_event_direction IS NULL
         AND terminal_event_sequence IS NULL
         AND terminal_message_id IS NULL
         AND terminal_idempotency_key IS NULL
         AND terminal_envelope_digest IS NULL
       )
FROM worker_turn_settlements
WHERE agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(&count, &eventWitnessIsNull); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !eventWitnessIsNull {
		t.Fatalf(
			"settlement count=%d event witness null=%t",
			count,
			eventWitnessIsNull,
		)
	}

	restarted := postgrestest.Restart(t, pool)
	restartedService := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
		Now:   func() time.Time { return claimedAt.Add(time.Minute) },
	}
	duplicate, err := restartedService.ClaimBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Revision != settlement.Revision ||
		!duplicate.AcceptedAt.Equal(settlement.AcceptedAt) ||
		duplicate.Source.Digest != settlement.Source.Digest ||
		duplicate.Source.StateDigest != settlement.Source.StateDigest ||
		string(duplicate.Source.State) != string(settlement.Source.State) {
		t.Fatalf(
			"duplicate changed settlement: first=%+v duplicate=%+v",
			settlement,
			duplicate,
		)
	}
	if err := restarted.QueryRow(ctx, `
SELECT count(*)
FROM worker_turn_settlements
WHERE agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate created %d settlements", count)
	}
}

func TestBackendDeadlineTurnSettlementClaimRejectsConflictingSourceWithoutMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	command := backendDeadlineClaimCommand(started, deadlineAt, sourceRef)
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt.Add(2 * time.Minute) },
	}
	original, err := service.ClaimBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}

	conflicts := map[string]func(*workerapp.ClaimBackendDeadlineCommand){
		"tenant": func(candidate *workerapp.ClaimBackendDeadlineCommand) {
			candidate.TenantID = "tenant-conflict"
		},
		"Runtime identity": func(candidate *workerapp.ClaimBackendDeadlineCommand) {
			candidate.RuntimeInstanceID = "runtime-conflict"
		},
		"deadline": func(candidate *workerapp.ClaimBackendDeadlineCommand) {
			candidate.DeadlineAt = candidate.DeadlineAt.Add(time.Second)
		},
		"source reference": func(candidate *workerapp.ClaimBackendDeadlineCommand) {
			candidate.SourceRef += "/conflict"
		},
	}
	for name, mutate := range conflicts {
		t.Run(name, func(t *testing.T) {
			candidate := command
			mutate(&candidate)
			if _, err := service.ClaimBackendDeadline(
				ctx,
				candidate,
			); !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
				t.Fatalf("conflicting claim error=%v", err)
			}
			current, err := (workerapp.TurnSettlementService{
				Store: workerpostgres.NewTurnSettlementRepository(pool),
			}).Load(ctx, original.SettlementID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Revision != original.Revision ||
				current.Source.Digest != original.Source.Digest ||
				current.Source.StateDigest != original.Source.StateDigest ||
				!current.UpdatedAt.Equal(original.UpdatedAt) {
				t.Fatalf(
					"conflict mutated settlement: original=%+v current=%+v",
					original,
					current,
				)
			}
		})
	}
}

func TestBackendDeadlineInterruptReservationAndSourceAdvanceCommitAtomically(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	_, _ = bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		deadlineAt,
	)
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	sourceService := workerapp.TurnSettlementSourceService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}
	claimed, err := sourceService.ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			OwnerID:          "deadline-source-worker-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	messageID := "deadline-interrupt-" + started.AgentTurnID
	command := workerapp.AdvanceBackendDeadlineSourceCommand{
		SettlementID:            leased.SettlementID,
		ExpectedRevision:        leased.Revision,
		ExpectedStateDigest:     leased.Source.StateDigest,
		LeaseOwnerID:            leased.LeaseOwnerID,
		LeaseFence:              leased.LeaseFence,
		ToPhase:                 workerapp.BackendDeadlineSourceInterruptQueued,
		InterruptMessageID:      messageID,
		InterruptIdempotencyKey: messageID,
		InterruptEnvelope: runtimeControlEnvelope(
			started,
			"command",
			0,
			messageID,
			messageID,
			"agent_turn.interrupt",
		),
	}
	advancedAt := deadlineAt.Add(time.Second)
	sourceService.Now = func() time.Time { return advancedAt }
	advanced, err := sourceService.AdvanceBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(advanced.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != workerapp.BackendDeadlineSourceInterruptQueued ||
		state.Interrupt == nil ||
		state.Interrupt.MessageID != messageID ||
		state.Interrupt.Sequence == 0 ||
		!state.Interrupt.JournaledAt.Equal(advancedAt) ||
		advanced.Revision != leased.Revision+1 {
		t.Fatalf("advanced settlement=%+v state=%+v", advanced, state)
	}
	var messageCount int
	var sequence uint64
	var fingerprint, cursorDigest string
	if err := pool.QueryRow(ctx, `
SELECT count(*),min(sequence),min(fingerprint),min(cursor_digest)
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='command' AND message_id=$2`,
		started.RuntimeInstanceID,
		messageID,
	).Scan(
		&messageCount,
		&sequence,
		&fingerprint,
		&cursorDigest,
	); err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 || sequence != state.Interrupt.Sequence ||
		fingerprint != state.Interrupt.EnvelopeDigest ||
		cursorDigest != state.Interrupt.CursorDigest {
		t.Fatalf(
			"journal count=%d sequence=%d fingerprint=%s cursor=%s proof=%+v",
			messageCount,
			sequence,
			fingerprint,
			cursorDigest,
			state.Interrupt,
		)
	}

	replayed, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(
			postgrestest.Restart(t, pool),
		),
		Now: func() time.Time { return advancedAt.Add(time.Second) },
	}).AdvanceBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != advanced.Revision ||
		replayed.Source.StateDigest != advanced.Source.StateDigest {
		t.Fatalf(
			"response-loss replay changed source: advanced=%+v replayed=%+v",
			advanced,
			replayed,
		)
	}
}

func TestBackendDeadlineInterruptReservationRollsBackJournalWhenSourceUpdateFails(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, leased, deadlineAt := setupLeasedBackendDeadlineSource(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 33, 0, 0, time.UTC),
	)
	var channelSequence, runtimeSequence, runtimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT channel.command_committed_sequence,runtime.last_command_sequence,
       runtime.revision
FROM runtime_control_channels channel
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=channel.runtime_instance_id
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&channelSequence,
		&runtimeSequence,
		&runtimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION reject_backend_deadline_interrupt_source_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.source_state->>'phase' = 'interrupt_enqueued'
     AND OLD.source_state->>'phase' = 'claimed' THEN
    RAISE EXCEPTION 'injected Backend deadline source failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER reject_backend_deadline_interrupt_source_update
BEFORE UPDATE ON worker_turn_settlements
FOR EACH ROW
EXECUTE FUNCTION reject_backend_deadline_interrupt_source_update()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS reject_backend_deadline_interrupt_source_update
  ON worker_turn_settlements;
DROP FUNCTION IF EXISTS reject_backend_deadline_interrupt_source_update()`)
	})

	_, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt.Add(time.Second) },
	}).AdvanceBackendDeadline(
		ctx,
		backendDeadlineInterruptAdvanceCommand(started, leased),
	)
	if err == nil {
		t.Fatal("injected source failure did not abort interrupt reservation")
	}
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, leased.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	var messageCount int
	var currentChannelSequence, currentRuntimeSequence, currentRuntimeRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM runtime_control_messages
   WHERE runtime_instance_id=$1 AND direction='command'
     AND message_id=$2),
  channel.command_committed_sequence,
  runtime.last_command_sequence,
  runtime.revision
FROM runtime_control_channels channel
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=channel.runtime_instance_id
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		"deadline-interrupt-"+started.AgentTurnID,
	).Scan(
		&messageCount,
		&currentChannelSequence,
		&currentRuntimeSequence,
		&currentRuntimeRevision,
	); err != nil {
		t.Fatal(err)
	}
	if messageCount != 0 ||
		currentChannelSequence != channelSequence ||
		currentRuntimeSequence != runtimeSequence ||
		currentRuntimeRevision != runtimeRevision ||
		current.Revision != leased.Revision ||
		current.Source.StateDigest != leased.Source.StateDigest {
		t.Fatalf(
			"partial commit survived: messages=%d channel=%d/%d runtime=%d/%d revision=%d/%d settlement=%+v",
			messageCount,
			channelSequence,
			currentChannelSequence,
			runtimeSequence,
			currentRuntimeSequence,
			runtimeRevision,
			currentRuntimeRevision,
			current,
		)
	}
}

func TestBackendDeadlineInterruptReservationPersistsWhileControlChannelDisconnected(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, leased, deadlineAt := setupLeasedBackendDeadlineSource(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 36, 0, 0, time.UTC),
	)
	disconnectedAt := deadlineAt.Add(time.Second)
	if _, err := pool.Exec(ctx, `
UPDATE runtime_control_channels
SET status='disconnected',disconnected_at=$2,
    revision=revision+1,updated_at=$2
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		disconnectedAt,
	); err != nil {
		t.Fatal(err)
	}
	advanced, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return disconnectedAt },
	}).AdvanceBackendDeadline(
		ctx,
		backendDeadlineInterruptAdvanceCommand(started, leased),
	)
	if err != nil {
		t.Fatal(err)
	}
	var sourceState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(advanced.Source.State, &sourceState); err != nil {
		t.Fatal(err)
	}
	if sourceState.Phase !=
		workerapp.BackendDeadlineSourceInterruptQueued ||
		sourceState.Interrupt == nil {
		t.Fatalf("disconnected source state=%+v", sourceState)
	}
	var persistedMessageID, persistedFingerprint string
	if err := pool.QueryRow(ctx, `
SELECT message_id,fingerprint
FROM runtime_control_messages
WHERE runtime_instance_id=$1
  AND direction='command'
  AND sequence=$2`,
		started.RuntimeInstanceID,
		sourceState.Interrupt.Sequence,
	).Scan(
		&persistedMessageID,
		&persistedFingerprint,
	); err != nil {
		t.Fatal(err)
	}
	if persistedMessageID != sourceState.Interrupt.MessageID ||
		persistedFingerprint != sourceState.Interrupt.EnvelopeDigest {
		t.Fatalf(
			"persisted interrupt message=%s fingerprint=%s source=%+v",
			persistedMessageID,
			persistedFingerprint,
			sourceState.Interrupt,
		)
	}

	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return disconnectedAt.Add(time.Second) },
	}
	_, err = journal.Claim(
		ctx,
		runtimeControlClaim(
			started,
			"process-1",
			2,
			"backend-reconnected",
		),
	)
	if !errors.Is(
		err,
		workerapp.ErrRuntimeControlPendingTurnSettlement,
	) {
		t.Fatalf("proofless reconnect error=%v", err)
	}
}

func TestBackendDeadlineForceIntentFreezesTargetAndSchedulesReconciliation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, leased, _ := setupLeasedBackendDeadlineSource(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 39, 0, 0, time.UTC),
	)
	command := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		leased,
	)
	forceAt := command.ForceStop.DecidedAt
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	command.ExpectedRevision = leased.Revision
	command.ExpectedStateDigest = leased.Source.StateDigest
	var initialChannelStatus string
	var initialOwnerFence, initialChannelRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT status,owner_fence,revision
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&initialChannelStatus,
		&initialOwnerFence,
		&initialChannelRevision,
	); err != nil {
		t.Fatal(err)
	}
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}
	advanced, err := service.AdvanceBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(advanced.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != workerapp.BackendDeadlineSourceForceStopIntent ||
		state.ForceStop == nil ||
		state.ForceStop.IntentID != command.ForceStop.IntentID ||
		state.ForceStop.ExpectedContainerID !=
			command.ForceStop.ExpectedContainerID ||
		advanced.Revision != leased.Revision+1 {
		t.Fatalf("force settlement=%+v state=%+v", advanced, state)
	}
	var channelStatus string
	var ownerFence, channelRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT status,owner_fence,revision
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&channelStatus,
		&ownerFence,
		&channelRevision,
	); err != nil {
		t.Fatal(err)
	}
	if initialChannelStatus == "fenced" ||
		channelStatus != "fenced" ||
		ownerFence != initialOwnerFence+1 ||
		channelRevision != initialChannelRevision+1 {
		t.Fatalf(
			"channel before=%s/%d/%d after=%s/%d/%d",
			initialChannelStatus,
			initialOwnerFence,
			initialChannelRevision,
			channelStatus,
			ownerFence,
			channelRevision,
		)
	}
	var commandType, targetType, targetID, idempotencyKey string
	var payload json.RawMessage
	var dueAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT command_type,target_type,target_id,idempotency_key,payload,due_at
FROM scheduled_commands
WHERE scheduled_command_id=$1`,
		command.ForceStop.CleanupScheduledCommandID,
	).Scan(
		&commandType,
		&targetType,
		&targetID,
		&idempotencyKey,
		&payload,
		&dueAt,
	); err != nil {
		t.Fatal(err)
	}
	var scheduledPayload struct {
		SchemaVersion             uint32 `json:"schema_version"`
		SettlementID              string `json:"settlement_id"`
		ExpectedSourceStateDigest string `json:"expected_source_state_digest"`
	}
	if err := json.Unmarshal(payload, &scheduledPayload); err != nil {
		t.Fatal(err)
	}
	if commandType !=
		workerpostgres.CommandBackendDeadlineSourceReconcile ||
		targetType != "TurnSettlement" ||
		targetID != leased.SettlementID ||
		idempotencyKey != command.ForceStop.CleanupIdempotencyKey ||
		!dueAt.Equal(forceAt) ||
		scheduledPayload.SchemaVersion != 1 ||
		scheduledPayload.SettlementID != leased.SettlementID ||
		scheduledPayload.ExpectedSourceStateDigest !=
			advanced.Source.StateDigest {
		t.Fatalf(
			"scheduled command type=%s target=%s/%s idem=%s due=%s payload=%s",
			commandType,
			targetType,
			targetID,
			idempotencyKey,
			dueAt,
			payload,
		)
	}
	service.Now = func() time.Time { return forceAt.Add(time.Second) }
	replayed, err := service.AdvanceBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != advanced.Revision ||
		replayed.Source.StateDigest != advanced.Source.StateDigest {
		t.Fatalf(
			"force response-loss replay changed state: advanced=%+v replayed=%+v",
			advanced,
			replayed,
		)
	}
	var replayOwnerFence, replayChannelRevision uint64
	var reconcileCount int
	if err := pool.QueryRow(ctx, `
SELECT channel.owner_fence,channel.revision,
       (SELECT count(*) FROM scheduled_commands
        WHERE scheduled_command_id=$2)
FROM runtime_control_channels channel
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		command.ForceStop.CleanupScheduledCommandID,
	).Scan(
		&replayOwnerFence,
		&replayChannelRevision,
		&reconcileCount,
	); err != nil {
		t.Fatal(err)
	}
	if replayOwnerFence != ownerFence ||
		replayChannelRevision != channelRevision ||
		reconcileCount != 1 {
		t.Fatalf(
			"force replay channel=%d/%d want=%d/%d reconcile_count=%d",
			replayOwnerFence,
			replayChannelRevision,
			ownerFence,
			channelRevision,
			reconcileCount,
		)
	}
}

func TestBackendDeadlineForceIntentRollsBackFenceAndScheduleWhenSourceUpdateFails(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, leased, _ := setupLeasedBackendDeadlineSource(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 42, 0, 0, time.UTC),
	)
	command := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		leased,
	)
	forceAt := command.ForceStop.DecidedAt
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	command.ExpectedRevision = leased.Revision
	command.ExpectedStateDigest = leased.Source.StateDigest

	var initialStatus string
	var initialOwnerFence, initialChannelRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT status,owner_fence,revision
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&initialStatus,
		&initialOwnerFence,
		&initialChannelRevision,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION reject_backend_deadline_force_source_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.source_state->>'phase' = 'force_stop_intent'
     AND OLD.source_state->>'phase' IN ('claimed','interrupt_enqueued') THEN
    RAISE EXCEPTION 'injected Backend deadline force source failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER reject_backend_deadline_force_source_update
BEFORE UPDATE ON worker_turn_settlements
FOR EACH ROW
EXECUTE FUNCTION reject_backend_deadline_force_source_update()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS reject_backend_deadline_force_source_update
  ON worker_turn_settlements;
DROP FUNCTION IF EXISTS reject_backend_deadline_force_source_update()`)
	})

	_, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}).AdvanceBackendDeadline(ctx, command)
	if err == nil {
		t.Fatal("injected force source failure did not abort transaction")
	}
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, leased.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var ownerFence, channelRevision uint64
	var reconcileCount int
	if err := pool.QueryRow(ctx, `
SELECT channel.status,channel.owner_fence,channel.revision,
       (SELECT count(*) FROM scheduled_commands
        WHERE scheduled_command_id=$2)
FROM runtime_control_channels channel
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		command.ForceStop.CleanupScheduledCommandID,
	).Scan(
		&status,
		&ownerFence,
		&channelRevision,
		&reconcileCount,
	); err != nil {
		t.Fatal(err)
	}
	if current.Revision != leased.Revision ||
		current.Source.StateDigest != leased.Source.StateDigest ||
		status != initialStatus ||
		ownerFence != initialOwnerFence ||
		channelRevision != initialChannelRevision ||
		reconcileCount != 0 {
		t.Fatalf(
			"partial force commit settlement=%+v channel=%s/%d/%d command_count=%d",
			current,
			status,
			ownerFence,
			channelRevision,
			reconcileCount,
		)
	}
}

func TestBackendDeadlineCleanupProofsConvergeWithoutDomainMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, leased, _ := setupLeasedBackendDeadlineSource(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 44, 0, 0, time.UTC),
	)
	forceCommand := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		leased,
	)
	forceAt := forceCommand.ForceStop.DecidedAt
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	forceCommand.ExpectedRevision = leased.Revision
	forceCommand.ExpectedStateDigest = leased.Source.StateDigest

	before := loadBackendDeadlineDomainSnapshot(t, pool, started)
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}
	forced, err := service.AdvanceBackendDeadline(ctx, forceCommand)
	if err != nil {
		t.Fatal(err)
	}
	var channelOwnerFence, channelRevision uint64
	var channelFencedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT owner_fence,revision,updated_at
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&channelOwnerFence,
		&channelRevision,
		&channelFencedAt,
	); err != nil {
		t.Fatal(err)
	}
	absentAt := forceAt.Add(time.Second)
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		channelOwnerFence,
		channelRevision,
		channelFencedAt,
		forceCommand.ForceStop.ExpectedContainerID,
		absentAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return absentAt }
	fenced, err := service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        forced.SettlementID,
			ExpectedRevision:    forced.Revision,
			ExpectedStateDigest: forced.Source.StateDigest,
			LeaseOwnerID:        forced.LeaseOwnerID,
			LeaseFence:          forced.LeaseFence,
			ToPhase:             workerapp.BackendDeadlineSourceWriteFenced,
			WriteFence:          &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	confirmedAt := absentAt.Add(time.Second)
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		forceCommand.ForceStop.ExpectedContainerID,
		"switchback-runtime-secret-"+started.RuntimeInstanceID,
		confirmedAt,
		confirmedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return confirmedAt }
	cleaned, err := service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        fenced.SettlementID,
			ExpectedRevision:    fenced.Revision,
			ExpectedStateDigest: fenced.Source.StateDigest,
			LeaseOwnerID:        fenced.LeaseOwnerID,
			LeaseFence:          fenced.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(cleaned.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase !=
		workerapp.BackendDeadlineSourceCleanupConfirmed ||
		state.WriteFence == nil ||
		state.Cleanup == nil {
		t.Fatalf("cleaned source state=%+v", state)
	}
	after := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if before != after {
		t.Fatalf(
			"proof-only cleanup mutated domain: before=%+v after=%+v",
			before,
			after,
		)
	}

	replayed, err := service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        fenced.SettlementID,
			ExpectedRevision:    fenced.Revision,
			ExpectedStateDigest: fenced.Source.StateDigest,
			LeaseOwnerID:        fenced.LeaseOwnerID,
			LeaseFence:          fenced.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != cleaned.Revision ||
		replayed.Source.StateDigest != cleaned.Source.StateDigest {
		t.Fatalf(
			"cleanup response-loss replay changed source: cleaned=%+v replayed=%+v",
			cleaned,
			replayed,
		)
	}
}

func TestBackendDeadlineSourceApplyAdvancesWithoutTerminalDeliveryAndReplays(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 44, 30, 0, time.UTC)
	started, cleaned, deadlineAt := prepareBackendDeadlineSourceForApply(
		t,
		pool,
		base,
		true,
	)
	taskSettled := seedBackendDeadlineTaskSettled(
		t,
		pool,
		started,
		cleaned,
		deadlineAt,
	)
	before := loadBackendDeadlineDomainSnapshot(t, pool, started)
	appliedAt := taskSettled.UpdatedAt.Add(time.Second)
	command := workerapp.ApplyBackendDeadlineSourceCommand{
		SettlementID:              taskSettled.SettlementID,
		ExpectedRevision:          taskSettled.Revision,
		ExpectedSourceDigest:      taskSettled.Source.Digest,
		ExpectedSourceStateDigest: taskSettled.Source.StateDigest,
		LeaseOwnerID:              taskSettled.LeaseOwnerID,
		LeaseFence:                taskSettled.LeaseFence,
	}
	applied, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return appliedAt },
	}).ApplyBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase != workerapp.TurnSettlementTerminalEventApplied ||
		applied.Revision != taskSettled.Revision+1 ||
		applied.TerminalEventDisposition !=
			workerapp.RuntimeControlEffectDispositionCommit ||
		applied.TerminalApplyOutcomeDigest == "" ||
		!applied.TerminalEventAppliedAt.Equal(appliedAt) ||
		applied.RuntimeInstanceID != "" ||
		applied.RuntimeGeneration != 0 ||
		applied.ChannelEpoch != 0 ||
		applied.TerminalEventSequence != 0 ||
		applied.TerminalMessageID != "" ||
		applied.TerminalIdempotencyKey != "" ||
		applied.TerminalEnvelopeDigest != "" {
		t.Fatalf("Backend deadline source apply=%+v", applied)
	}
	var checkpoint struct {
		TerminalSourceApplied bool `json:"terminal_source_applied"`
		BackendDeadline       struct {
			Kind                     string    `json:"kind"`
			SourceRef                string    `json:"source_ref"`
			SourceDigest             string    `json:"source_digest"`
			SourceStateDigest        string    `json:"source_state_digest"`
			CleanupConfirmedAt       time.Time `json:"cleanup_confirmed_at"`
			CleanupObservationDigest string    `json:"cleanup_observation_digest"`
			AppliedFromRevision      uint64    `json:"applied_from_revision"`
			LeaseOwnerID             string    `json:"lease_owner_id"`
			LeaseFence               uint64    `json:"lease_fence"`
			RuntimeInstanceID        string    `json:"runtime_instance_id"`
			RuntimeGeneration        uint64    `json:"runtime_generation"`
			SessionStatus            string    `json:"session_status"`
			SessionRevision          uint64    `json:"session_revision"`
			RuntimeRevision          uint64    `json:"runtime_revision"`
		} `json:"backend_deadline_source_applied"`
	}
	if err := json.Unmarshal(applied.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	var sourceState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(applied.Source.State, &sourceState); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.TerminalSourceApplied ||
		checkpoint.BackendDeadline.Kind != "backend_deadline" ||
		checkpoint.BackendDeadline.SourceRef != applied.Source.Ref ||
		checkpoint.BackendDeadline.SourceDigest != applied.Source.Digest ||
		checkpoint.BackendDeadline.SourceStateDigest !=
			applied.Source.StateDigest ||
		sourceState.Cleanup == nil ||
		!checkpoint.BackendDeadline.CleanupConfirmedAt.Equal(
			sourceState.Cleanup.ConfirmedAt,
		) ||
		checkpoint.BackendDeadline.CleanupObservationDigest !=
			sourceState.Cleanup.ObservationDigest ||
		checkpoint.BackendDeadline.AppliedFromRevision !=
			taskSettled.Revision ||
		checkpoint.BackendDeadline.LeaseOwnerID !=
			taskSettled.LeaseOwnerID ||
		checkpoint.BackendDeadline.LeaseFence !=
			taskSettled.LeaseFence ||
		checkpoint.BackendDeadline.RuntimeInstanceID !=
			sourceState.RuntimeInstanceID ||
		checkpoint.BackendDeadline.RuntimeGeneration !=
			sourceState.RuntimeGeneration ||
		checkpoint.BackendDeadline.SessionStatus != "waiting_user" ||
		sourceState.ForceStop == nil ||
		checkpoint.BackendDeadline.SessionRevision !=
			sourceState.ForceStop.ExpectedSessionRevision+2 ||
		checkpoint.BackendDeadline.RuntimeRevision !=
			sourceState.ForceStop.ExpectedRuntimeRevision+1 {
		t.Fatalf(
			"checkpoint=%+v source_state=%+v",
			checkpoint,
			sourceState,
		)
	}
	after := loadBackendDeadlineDomainSnapshot(
		t,
		pool,
		started,
	)
	if before.SessionStatus != string(domain.SessionWaitingUser) ||
		before.ActiveRuntimeInstanceID != started.RuntimeInstanceID ||
		before.RuntimeStatus != "active" ||
		before.RuntimeCleanupConfirmed ||
		after.SessionStatus != before.SessionStatus ||
		after.SessionRevision != before.SessionRevision+1 ||
		after.ActiveRuntimeInstanceID != "" ||
		after.RunStatus != before.RunStatus ||
		after.RunRevision != before.RunRevision ||
		after.TurnStatus != before.TurnStatus ||
		after.TurnRevision != before.TurnRevision ||
		after.RuntimeStatus != "terminated" ||
		after.RuntimeRevision != before.RuntimeRevision+1 ||
		!after.RuntimeCleanupConfirmed ||
		after.TaskStatus != before.TaskStatus ||
		after.TaskRevision != before.TaskRevision {
		t.Fatalf(
			"source apply domain transition: before=%+v after=%+v",
			before,
			after,
		)
	}

	restarted := postgrestest.Restart(t, pool)
	replayed, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
		Now:   func() time.Time { return appliedAt.Add(time.Second) },
	}).ApplyBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != applied.Revision ||
		replayed.PhaseStateDigest != applied.PhaseStateDigest ||
		replayed.TerminalApplyOutcomeDigest !=
			applied.TerminalApplyOutcomeDigest ||
		!replayed.TerminalEventAppliedAt.Equal(
			applied.TerminalEventAppliedAt,
		) {
		t.Fatalf(
			"response-loss replay changed apply: applied=%+v replayed=%+v",
			applied,
			replayed,
		)
	}
	replayedDomain := loadBackendDeadlineDomainSnapshot(
		t,
		restarted,
		started,
	)
	if replayedDomain != after {
		t.Fatalf(
			"source apply replay mutated domain: applied=%+v replayed=%+v",
			after,
			replayedDomain,
		)
	}

	reclaimAt := replayed.LeaseExpiresAt.Add(time.Microsecond)
	reclaimed, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
		Now:   func() time.Time { return reclaimAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     replayed.SettlementID,
			ExpectedRevision: replayed.Revision,
			OwnerID:          "backend-deadline-replay-owner",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	afterReclaimReplay, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
		Now: func() time.Time {
			return reclaimAt.Add(time.Second)
		},
	}).ApplyBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if afterReclaimReplay.Revision != reclaimed.Revision ||
		afterReclaimReplay.LeaseOwnerID != reclaimed.LeaseOwnerID ||
		afterReclaimReplay.LeaseFence != reclaimed.LeaseFence ||
		afterReclaimReplay.PhaseStateDigest !=
			applied.PhaseStateDigest ||
		afterReclaimReplay.TerminalApplyOutcomeDigest !=
			applied.TerminalApplyOutcomeDigest {
		t.Fatalf(
			"post-reclaim replay changed apply: reclaimed=%+v replayed=%+v",
			reclaimed,
			afterReclaimReplay,
		)
	}
	if afterReclaimDomain := loadBackendDeadlineDomainSnapshot(
		t,
		restarted,
		started,
	); afterReclaimDomain != after {
		t.Fatalf(
			"post-reclaim replay mutated domain: applied=%+v replayed=%+v",
			after,
			afterReclaimDomain,
		)
	}
}

func TestBackendDeadlineSourceApplyUsesCancelledTaskSessionRevision(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 44, 35, 0, time.UTC)
	started, cleaned, deadlineAt := prepareBackendDeadlineSourceForApply(
		t,
		pool,
		base,
		true,
	)
	taskSettled := seedBackendDeadlineCancelledTaskSettled(
		t,
		pool,
		started,
		cleaned,
		deadlineAt,
	)
	before := loadBackendDeadlineDomainSnapshot(t, pool, started)
	target, err := workerapp.ResolveBackendDeadlineSourceApplyTarget(
		taskSettled.PhaseState,
		taskSettled.Source,
	)
	if err != nil {
		t.Fatal(err)
	}
	var sourceState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(
		taskSettled.Source.State,
		&sourceState,
	); err != nil {
		t.Fatal(err)
	}
	if sourceState.ForceStop == nil ||
		target.ExpectedSessionStatus != "interactive" ||
		target.SessionStatus != "waiting_user" ||
		target.ExpectedSessionRevision != before.SessionRevision ||
		target.ExpectedSessionRevision !=
			sourceState.ForceStop.ExpectedSessionRevision+2 ||
		target.SessionRevision != before.SessionRevision+1 ||
		before.SessionStatus != "interactive" ||
		before.TaskStatus != "cancelled" {
		t.Fatalf(
			"cancelled target=%+v source=%+v domain=%+v",
			target,
			sourceState,
			before,
		)
	}
	appliedAt := taskSettled.UpdatedAt.Add(time.Second)
	applied, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return appliedAt },
	}).ApplyBackendDeadline(
		ctx,
		workerapp.ApplyBackendDeadlineSourceCommand{
			SettlementID:              taskSettled.SettlementID,
			ExpectedRevision:          taskSettled.Revision,
			ExpectedSourceDigest:      taskSettled.Source.Digest,
			ExpectedSourceStateDigest: taskSettled.Source.StateDigest,
			LeaseOwnerID:              taskSettled.LeaseOwnerID,
			LeaseFence:                taskSettled.LeaseFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	after := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if applied.Phase != workerapp.TurnSettlementTerminalEventApplied ||
		after.SessionStatus != "waiting_user" ||
		after.SessionRevision != target.SessionRevision ||
		after.ActiveRuntimeInstanceID != "" ||
		after.RuntimeStatus != "terminated" ||
		after.RuntimeRevision != target.RuntimeRevision ||
		!after.RuntimeCleanupConfirmed ||
		after.RunStatus != before.RunStatus ||
		after.RunRevision != before.RunRevision ||
		after.TurnStatus != before.TurnStatus ||
		after.TurnRevision != before.TurnRevision ||
		after.TaskStatus != before.TaskStatus ||
		after.TaskRevision != before.TaskRevision {
		t.Fatalf(
			"cancelled source apply before=%+v target=%+v after=%+v",
			before,
			target,
			after,
		)
	}
}

func TestBackendDeadlineSourceApplyRollsBackRuntimeAndSessionWhenSettlementWriteFails(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 44, 37, 0, time.UTC)
	started, cleaned, deadlineAt := prepareBackendDeadlineSourceForApply(
		t,
		pool,
		base,
		true,
	)
	taskSettled := seedBackendDeadlineTaskSettled(
		t,
		pool,
		started,
		cleaned,
		deadlineAt,
	)
	beforeDomain := loadBackendDeadlineDomainSnapshot(t, pool, started)
	command := workerapp.ApplyBackendDeadlineSourceCommand{
		SettlementID:              taskSettled.SettlementID,
		ExpectedRevision:          taskSettled.Revision,
		ExpectedSourceDigest:      taskSettled.Source.Digest,
		ExpectedSourceStateDigest: taskSettled.Source.StateDigest,
		LeaseOwnerID:              taskSettled.LeaseOwnerID,
		LeaseFence:                taskSettled.LeaseFence,
	}
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now: func() time.Time {
			return taskSettled.UpdatedAt.Add(time.Second)
		},
	}

	if _, err := pool.Exec(ctx, `
CREATE FUNCTION switchback_test_reject_backend_deadline_source_apply()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.phase = 'terminal_event_applied' THEN
    RAISE EXCEPTION 'injected Backend deadline source Apply failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER switchback_test_reject_backend_deadline_source_apply
BEFORE UPDATE ON worker_turn_settlements
FOR EACH ROW
EXECUTE FUNCTION switchback_test_reject_backend_deadline_source_apply()`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyBackendDeadline(ctx, command); err == nil {
		t.Fatal("injected settlement failure did not fail source Apply")
	}
	assertBackendDeadlineSourceApplyUnchanged(
		t,
		pool,
		started,
		taskSettled,
		beforeDomain,
	)

	if _, err := pool.Exec(ctx, `
DROP TRIGGER switchback_test_reject_backend_deadline_source_apply
ON worker_turn_settlements;
DROP FUNCTION switchback_test_reject_backend_deadline_source_apply()`); err != nil {
		t.Fatal(err)
	}
	applied, err := service.ApplyBackendDeadline(ctx, command)
	if err != nil {
		t.Fatalf("apply source after removing injected failure: %v", err)
	}
	if applied.Phase != workerapp.TurnSettlementTerminalEventApplied {
		t.Fatalf("source apply phase=%s", applied.Phase)
	}
}

func TestBackendDeadlineLateTerminalApplyIgnoresEventAndAppliesSourceAtomically(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	fixture := prepareBackendDeadlineLateTerminalApplyFixture(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 44, 38, 0, time.UTC),
		"backend-deadline-late-terminal",
	)
	beforeDomain := loadBackendDeadlineDomainSnapshot(
		t,
		pool,
		fixture.Started,
	)
	beforeApply := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)

	applied, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now: func() time.Time {
			return fixture.Settlement.UpdatedAt.Add(time.Second)
		},
	}).ApplyEvent(ctx, fixture.Apply)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Channel.EventAppliedSequence != fixture.Apply.Sequence ||
		applied.Message.EffectStatus != "applied" ||
		applied.Message.Duplicate {
		t.Fatalf("late terminal Apply=%+v", applied)
	}
	settlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, fixture.Settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Phase !=
		workerapp.TurnSettlementTerminalEventApplied ||
		settlement.Revision != fixture.Settlement.Revision+1 ||
		settlement.TerminalEventDisposition !=
			workerapp.RuntimeControlEffectDispositionIgnore ||
		settlement.TerminalApplyOutcomeDigest == "" ||
		settlement.TerminalApplyOutcomeDigest !=
			applied.Message.ApplyOutcomeDigest ||
		settlement.TerminalEventAppliedAt.IsZero() ||
		!settlement.CompletedAt.IsZero() {
		t.Fatalf("late terminal settlement=%+v", settlement)
	}
	var checkpoint struct {
		TerminalSourceApplied bool            `json:"terminal_source_applied"`
		BackendDeadline       json.RawMessage `json:"backend_deadline_source_applied"`
	}
	if err := json.Unmarshal(settlement.PhaseState, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.TerminalSourceApplied ||
		len(checkpoint.BackendDeadline) == 0 {
		t.Fatalf("late terminal checkpoint=%s", settlement.PhaseState)
	}
	afterDomain := loadBackendDeadlineDomainSnapshot(
		t,
		pool,
		fixture.Started,
	)
	if beforeDomain.SessionStatus != "waiting_user" ||
		beforeDomain.ActiveRuntimeInstanceID !=
			fixture.Started.RuntimeInstanceID ||
		beforeDomain.RuntimeStatus != "active" ||
		beforeDomain.RuntimeCleanupConfirmed ||
		afterDomain.SessionStatus != "waiting_user" ||
		afterDomain.SessionRevision != beforeDomain.SessionRevision+1 ||
		afterDomain.ActiveRuntimeInstanceID != "" ||
		afterDomain.RuntimeStatus != "terminated" ||
		afterDomain.RuntimeRevision != beforeDomain.RuntimeRevision+1 ||
		!afterDomain.RuntimeCleanupConfirmed ||
		afterDomain.RunStatus != beforeDomain.RunStatus ||
		afterDomain.RunRevision != beforeDomain.RunRevision ||
		afterDomain.TurnStatus != beforeDomain.TurnStatus ||
		afterDomain.TurnRevision != beforeDomain.TurnRevision ||
		afterDomain.TaskStatus != beforeDomain.TaskStatus ||
		afterDomain.TaskRevision != beforeDomain.TaskRevision {
		t.Fatalf(
			"late terminal source transition: before=%+v after=%+v",
			beforeDomain,
			afterDomain,
		)
	}
	afterApply := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	if beforeApply.EventEffect != "received" ||
		beforeApply.EventAppliedSequence+1 !=
			afterApply.EventAppliedSequence ||
		afterApply.EventEffect != "applied" ||
		afterApply.SettlementPhase !=
			workerapp.TurnSettlementTerminalEventApplied {
		t.Fatalf(
			"late terminal inbox transition: before=%+v after=%+v",
			beforeApply,
			afterApply,
		)
	}

	duplicate, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now: func() time.Time {
			return settlement.TerminalEventAppliedAt.Add(time.Second)
		},
	}).ApplyEvent(ctx, fixture.Apply)
	if err != nil || !duplicate.Message.Duplicate {
		t.Fatalf("late terminal response-loss replay=%+v error=%v", duplicate, err)
	}
	if replayedApply := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	); replayedApply != afterApply {
		t.Fatalf(
			"late terminal replay mutated apply: before=%+v after=%+v",
			afterApply,
			replayedApply,
		)
	}
	if replayedDomain := loadBackendDeadlineDomainSnapshot(
		t,
		pool,
		fixture.Started,
	); replayedDomain != afterDomain {
		t.Fatalf(
			"late terminal replay mutated domain: before=%+v after=%+v",
			afterDomain,
			replayedDomain,
		)
	}

	restarted := postgrestest.Restart(t, pool)
	reclaimAt := settlement.LeaseExpiresAt.Add(time.Microsecond)
	reclaimed, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
		Now:   func() time.Time { return reclaimAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     settlement.SettlementID,
			ExpectedRevision: settlement.Revision,
			OwnerID:          "late-terminal-replay-owner",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	postReclaimReplay, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(restarted),
		Now: func() time.Time {
			return reclaimAt.Add(time.Second)
		},
	}).ApplyEvent(ctx, fixture.Apply)
	if err != nil || !postReclaimReplay.Message.Duplicate {
		t.Fatalf(
			"late terminal post-reclaim replay=%+v error=%v",
			postReclaimReplay,
			err,
		)
	}
	postReclaimSettlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(restarted),
	}).Load(ctx, settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if postReclaimSettlement.Revision != reclaimed.Revision ||
		postReclaimSettlement.LeaseOwnerID !=
			reclaimed.LeaseOwnerID ||
		postReclaimSettlement.LeaseFence != reclaimed.LeaseFence ||
		postReclaimSettlement.PhaseStateDigest !=
			settlement.PhaseStateDigest ||
		postReclaimSettlement.TerminalApplyOutcomeDigest !=
			settlement.TerminalApplyOutcomeDigest {
		t.Fatalf(
			"late terminal post-reclaim settlement: reclaimed=%+v replayed=%+v",
			reclaimed,
			postReclaimSettlement,
		)
	}
	if postReclaimDomain := loadBackendDeadlineDomainSnapshot(
		t,
		restarted,
		fixture.Started,
	); postReclaimDomain != afterDomain {
		t.Fatalf(
			"late terminal post-reclaim replay mutated domain: before=%+v after=%+v",
			afterDomain,
			postReclaimDomain,
		)
	}
}

func TestBackendDeadlineSourceApplyAcceptsExactSuspectAndPreemptedRecoveryGraphs(
	t *testing.T,
) {
	tests := []struct {
		name               string
		runtimePredecessor string
		runtimeStatus      string
		lateWitness        bool
	}{
		{
			name:               "no witness suspect",
			runtimePredecessor: "suspect",
			runtimeStatus:      "suspect",
		},
		{
			name:               "late witness suspect",
			runtimePredecessor: "suspect",
			runtimeStatus:      "suspect",
			lateWitness:        true,
		},
		{
			name:               "no witness exact preempted recovery",
			runtimePredecessor: "unhealthy_preempted_recovery",
			runtimeStatus:      "unhealthy",
		},
		{
			name:               "late witness exact preempted recovery",
			runtimePredecessor: "unhealthy_preempted_recovery",
			runtimeStatus:      "unhealthy",
			lateWitness:        true,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Date(
				2026,
				7,
				27,
				3,
				0,
				index,
				0,
				time.UTC,
			)
			var (
				started workerapp.StartedWorkerRun
				before  backendDeadlineDomainSnapshot
				apply   func() error
			)
			if test.lateWitness {
				fixture :=
					prepareBackendDeadlineLateTerminalApplyFixtureWithRuntimePredecessor(
						t,
						pool,
						base,
						fmt.Sprintf(
							"backend-deadline-runtime-graph-%d",
							index,
						),
						test.runtimePredecessor,
					)
				started = fixture.Started
				before = loadBackendDeadlineDomainSnapshot(
					t,
					pool,
					started,
				)
				apply = func() error {
					_, err := (workerapp.RuntimeControlJournalService{
						Store: workerpostgres.
							NewRuntimeControlJournal(pool),
						Now: func() time.Time {
							return fixture.Settlement.UpdatedAt.Add(
								time.Second,
							)
						},
					}).ApplyEvent(ctx, fixture.Apply)
					return err
				}
			} else {
				var cleaned workerapp.TurnSettlement
				var deadlineAt time.Time
				started, cleaned, deadlineAt =
					prepareBackendDeadlineSourceForApplyWithRuntimePredecessor(
						t,
						pool,
						base,
						true,
						test.runtimePredecessor,
					)
				taskSettled := seedBackendDeadlineTaskSettled(
					t,
					pool,
					started,
					cleaned,
					deadlineAt,
				)
				before = loadBackendDeadlineDomainSnapshot(
					t,
					pool,
					started,
				)
				apply = func() error {
					_, err := (workerapp.TurnSettlementSourceService{
						Store: workerpostgres.
							NewTurnSettlementRepository(pool),
						Now: func() time.Time {
							return taskSettled.UpdatedAt.Add(
								time.Second,
							)
						},
					}).ApplyBackendDeadline(
						ctx,
						workerapp.ApplyBackendDeadlineSourceCommand{
							SettlementID: taskSettled.SettlementID,
							ExpectedRevision: taskSettled.
								Revision,
							ExpectedSourceDigest: taskSettled.
								Source.Digest,
							ExpectedSourceStateDigest: taskSettled.
								Source.StateDigest,
							LeaseOwnerID: taskSettled.LeaseOwnerID,
							LeaseFence:   taskSettled.LeaseFence,
						},
					)
					return err
				}
			}
			if before.SessionStatus != "waiting_user" ||
				before.RuntimeStatus != test.runtimeStatus ||
				before.RuntimeCleanupConfirmed {
				t.Fatalf(
					"source Apply predecessor=%+v want Runtime %s",
					before,
					test.runtimeStatus,
				)
			}
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			after := loadBackendDeadlineDomainSnapshot(
				t,
				pool,
				started,
			)
			if after.SessionStatus != "waiting_user" ||
				after.SessionRevision != before.SessionRevision+1 ||
				after.ActiveRuntimeInstanceID != "" ||
				after.RuntimeStatus != "terminated" ||
				after.RuntimeRevision != before.RuntimeRevision+1 ||
				!after.RuntimeCleanupConfirmed ||
				after.RunStatus != before.RunStatus ||
				after.RunRevision != before.RunRevision ||
				after.TurnStatus != before.TurnStatus ||
				after.TurnRevision != before.TurnRevision ||
				after.TaskStatus != before.TaskStatus ||
				after.TaskRevision != before.TaskRevision {
				t.Fatalf(
					"source Apply graph transition: before=%+v after=%+v",
					before,
					after,
				)
			}
		})
	}

}

func TestBackendDeadlineSourceApplyRejectsArbitraryUnhealthyAndMismatchedRecoveryBindings(
	t *testing.T,
) {
	tests := []struct {
		name               string
		runtimePredecessor string
		mutate             func(
			*testing.T,
			*pgxpool.Pool,
			workerapp.StartedWorkerRun,
			workerapp.TurnSettlement,
		) workerapp.TurnSettlement
	}{
		{
			name:               "unhealthy without recovery binding",
			runtimePredecessor: "active",
			mutate: func(
				t *testing.T,
				pool *pgxpool.Pool,
				started workerapp.StartedWorkerRun,
				settlement workerapp.TurnSettlement,
			) workerapp.TurnSettlement {
				tag, err := pool.Exec(
					context.Background(),
					`
UPDATE runtime_instances
SET status='unhealthy'
WHERE runtime_instance_id=$1
  AND status='active'`,
					started.RuntimeInstanceID,
				)
				if err != nil {
					t.Fatal(err)
				}
				if tag.RowsAffected() != 1 {
					t.Fatalf(
						"install arbitrary unhealthy Runtime rows=%d",
						tag.RowsAffected(),
					)
				}
				return settlement
			},
		},
		{
			name:               "suspect with recovery binding",
			runtimePredecessor: "suspect",
			mutate: func(
				t *testing.T,
				pool *pgxpool.Pool,
				_ workerapp.StartedWorkerRun,
				settlement workerapp.TurnSettlement,
			) workerapp.TurnSettlement {
				source, err :=
					workerapp.BindBackendDeadlineRecoveryPreemption(
						settlement.Source,
						"recovery-forged-suspect-binding",
						1,
						settlement.Source.OccurredAt,
					)
				if err != nil {
					t.Fatal(err)
				}
				return replaceBackendDeadlineSourceStateFixture(
					t,
					pool,
					settlement,
					source.State,
					source.StateDigest,
				)
			},
		},
		{
			name:               "unhealthy with mismatched recovery revision",
			runtimePredecessor: "unhealthy_preempted_recovery",
			mutate: func(
				t *testing.T,
				pool *pgxpool.Pool,
				_ workerapp.StartedWorkerRun,
				settlement workerapp.TurnSettlement,
			) workerapp.TurnSettlement {
				state, err :=
					workerapp.DecodeBackendDeadlineSourceState(
						settlement.Source,
					)
				if err != nil {
					t.Fatal(err)
				}
				state.RecoveryIncidentRevision++
				encoded, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				sourceState, sourceStateDigest, err :=
					workerapp.CanonicalizeTurnSettlementSourceState(
						encoded,
					)
				if err != nil {
					t.Fatal(err)
				}
				return replaceBackendDeadlineSourceStateFixture(
					t,
					pool,
					settlement,
					sourceState,
					sourceStateDigest,
				)
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			started, cleaned, deadlineAt :=
				prepareBackendDeadlineSourceForApplyWithRuntimePredecessor(
					t,
					pool,
					time.Date(
						2026,
						7,
						27,
						3,
						5,
						index,
						0,
						time.UTC,
					),
					true,
					test.runtimePredecessor,
				)
			taskSettled := seedBackendDeadlineTaskSettled(
				t,
				pool,
				started,
				cleaned,
				deadlineAt,
			)
			taskSettled = test.mutate(
				t,
				pool,
				started,
				taskSettled,
			)
			beforeDomain := loadBackendDeadlineDomainSnapshot(
				t,
				pool,
				started,
			)
			_, err := (workerapp.TurnSettlementSourceService{
				Store: workerpostgres.NewTurnSettlementRepository(pool),
				Now: func() time.Time {
					return taskSettled.UpdatedAt.Add(time.Second)
				},
			}).ApplyBackendDeadline(
				ctx,
				workerapp.ApplyBackendDeadlineSourceCommand{
					SettlementID: taskSettled.SettlementID,
					ExpectedRevision: taskSettled.
						Revision,
					ExpectedSourceDigest: taskSettled.Source.Digest,
					ExpectedSourceStateDigest: taskSettled.
						Source.StateDigest,
					LeaseOwnerID: taskSettled.LeaseOwnerID,
					LeaseFence:   taskSettled.LeaseFence,
				},
			)
			if !errors.Is(
				err,
				workerapp.ErrTurnSettlementSourceConflict,
			) {
				t.Fatalf("mismatched source graph error=%v", err)
			}
			assertBackendDeadlineSourceApplyUnchanged(
				t,
				pool,
				started,
				taskSettled,
				beforeDomain,
			)
		})
	}
}

func TestBackendDeadlineLateTerminalApplyRejectsWrongWitnessRevisionLeaseAndSource(
	t *testing.T,
) {
	tests := []struct {
		name   string
		mutate func(
			*testing.T,
			*pgxpool.Pool,
			backendDeadlineLateTerminalApplyFixture,
			*workerapp.ApplyRuntimeControlEventCommand,
		)
	}{
		{
			name: "witness sequence",
			mutate: func(
				_ *testing.T,
				_ *pgxpool.Pool,
				_ backendDeadlineLateTerminalApplyFixture,
				command *workerapp.ApplyRuntimeControlEventCommand,
			) {
				command.Sequence++
			},
		},
		{
			name: "witness message",
			mutate: func(
				_ *testing.T,
				_ *pgxpool.Pool,
				_ backendDeadlineLateTerminalApplyFixture,
				command *workerapp.ApplyRuntimeControlEventCommand,
			) {
				command.MessageID += "-wrong"
			},
		},
		{
			name: "settlement revision",
			mutate: func(
				_ *testing.T,
				_ *pgxpool.Pool,
				_ backendDeadlineLateTerminalApplyFixture,
				command *workerapp.ApplyRuntimeControlEventCommand,
			) {
				command.TurnSettlement.ExpectedRevision--
			},
		},
		{
			name: "settlement lease",
			mutate: func(
				_ *testing.T,
				_ *pgxpool.Pool,
				_ backendDeadlineLateTerminalApplyFixture,
				command *workerapp.ApplyRuntimeControlEventCommand,
			) {
				command.TurnSettlement.LeaseFence++
			},
		},
		{
			name: "source before cleanup",
			mutate: func(
				t *testing.T,
				pool *pgxpool.Pool,
				fixture backendDeadlineLateTerminalApplyFixture,
				_ *workerapp.ApplyRuntimeControlEventCommand,
			) {
				state, err :=
					workerapp.DecodeBackendDeadlineSourceState(
						fixture.Settlement.Source,
					)
				if err != nil {
					t.Fatal(err)
				}
				state.Phase =
					workerapp.BackendDeadlineSourceWriteFenced
				state.Cleanup = nil
				encoded, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				sourceState, sourceStateDigest, err :=
					workerapp.CanonicalizeTurnSettlementSourceState(
						encoded,
					)
				if err != nil {
					t.Fatal(err)
				}
				tag, err := pool.Exec(
					context.Background(),
					`
UPDATE worker_turn_settlements
SET source_state=$2,source_state_digest=$3
WHERE settlement_id=$1 AND revision=$4`,
					fixture.Settlement.SettlementID,
					sourceState,
					sourceStateDigest,
					fixture.Settlement.Revision,
				)
				if err != nil {
					t.Fatal(err)
				}
				if tag.RowsAffected() != 1 {
					t.Fatalf(
						"install pre-cleanup source rows=%d",
						tag.RowsAffected(),
					)
				}
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			fixture := prepareBackendDeadlineLateTerminalApplyFixture(
				t,
				pool,
				time.Date(
					2026,
					7,
					27,
					3,
					10,
					index,
					0,
					time.UTC,
				),
				fmt.Sprintf(
					"backend-deadline-late-reject-%d",
					index,
				),
			)
			command := fixture.Apply
			settlementAuthority := *fixture.Apply.TurnSettlement
			command.TurnSettlement = &settlementAuthority
			test.mutate(t, pool, fixture, &command)
			beforeSettlement, err := (workerapp.TurnSettlementService{
				Store: workerpostgres.NewTurnSettlementRepository(pool),
			}).Load(
				context.Background(),
				fixture.Settlement.SettlementID,
			)
			if err != nil {
				t.Fatal(err)
			}
			beforeApply := readTerminalSettlementApplySnapshot(
				t,
				pool,
				fixture.Settlement.SettlementID,
				fixture.MessageID,
			)
			beforeDomain := loadBackendDeadlineDomainSnapshot(
				t,
				pool,
				fixture.Started,
			)
			_, err = (workerapp.RuntimeControlJournalService{
				Store: workerpostgres.NewRuntimeControlJournal(pool),
				Now: func() time.Time {
					return fixture.Settlement.UpdatedAt.Add(
						time.Second,
					)
				},
			}).ApplyEvent(context.Background(), command)
			if err == nil {
				t.Fatal("invalid late-witness source Apply succeeded")
			}
			afterApply := readTerminalSettlementApplySnapshot(
				t,
				pool,
				fixture.Settlement.SettlementID,
				fixture.MessageID,
			)
			if afterApply != beforeApply {
				t.Fatalf(
					"rejected late Apply mutated inbox: before=%+v after=%+v",
					beforeApply,
					afterApply,
				)
			}
			assertBackendDeadlineSourceApplyUnchanged(
				t,
				pool,
				fixture.Started,
				beforeSettlement,
				beforeDomain,
			)
		})
	}
}

func TestBackendDeadlineLateTerminalApplyRollsBackInboxAndSourceGraph(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	fixture := prepareBackendDeadlineLateTerminalApplyFixture(
		t,
		pool,
		time.Date(2026, 7, 27, 3, 11, 0, 0, time.UTC),
		"backend-deadline-late-rollback",
	)
	beforeApply := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	beforeDomain := loadBackendDeadlineDomainSnapshot(
		t,
		pool,
		fixture.Started,
	)
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION switchback_test_reject_late_source_apply()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.phase = 'terminal_event_applied' THEN
    RAISE EXCEPTION 'injected late source Apply failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER switchback_test_reject_late_source_apply
BEFORE UPDATE ON worker_turn_settlements
FOR EACH ROW
EXECUTE FUNCTION switchback_test_reject_late_source_apply()`); err != nil {
		t.Fatal(err)
	}
	service := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now: func() time.Time {
			return fixture.Settlement.UpdatedAt.Add(time.Second)
		},
	}
	if _, err := service.ApplyEvent(
		ctx,
		fixture.Apply,
	); err == nil {
		t.Fatal("injected late source Apply failure did not fail")
	}
	afterFailure := readTerminalSettlementApplySnapshot(
		t,
		pool,
		fixture.Settlement.SettlementID,
		fixture.MessageID,
	)
	if afterFailure != beforeApply {
		t.Fatalf(
			"failed late Apply mutated inbox: before=%+v after=%+v",
			beforeApply,
			afterFailure,
		)
	}
	assertBackendDeadlineSourceApplyUnchanged(
		t,
		pool,
		fixture.Started,
		fixture.Settlement,
		beforeDomain,
	)
	if _, err := pool.Exec(ctx, `
DROP TRIGGER switchback_test_reject_late_source_apply
ON worker_turn_settlements;
DROP FUNCTION switchback_test_reject_late_source_apply()`); err != nil {
		t.Fatal(err)
	}
	applied, err := service.ApplyEvent(ctx, fixture.Apply)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Message.EffectStatus != "applied" ||
		applied.Channel.EventAppliedSequence != fixture.Apply.Sequence {
		t.Fatalf("late source Apply after rollback=%+v", applied)
	}
}

func TestBackendDeadlineSourceApplyRejectsBeforeCleanupAndStaleAuthority(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 44, 40, 0, time.UTC)
	started, writeFenced, deadlineAt := prepareBackendDeadlineSourceForApply(
		t,
		pool,
		base,
		false,
	)
	beforeDomain := loadBackendDeadlineDomainSnapshot(t, pool, started)
	beforeSettlement := writeFenced
	appliedAt := writeFenced.UpdatedAt.Add(time.Second)
	valid := workerapp.ApplyBackendDeadlineSourceCommand{
		SettlementID:              writeFenced.SettlementID,
		ExpectedRevision:          writeFenced.Revision,
		ExpectedSourceDigest:      writeFenced.Source.Digest,
		ExpectedSourceStateDigest: writeFenced.Source.StateDigest,
		LeaseOwnerID:              writeFenced.LeaseOwnerID,
		LeaseFence:                writeFenced.LeaseFence,
	}
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return appliedAt },
	}
	if _, err := service.ApplyBackendDeadline(
		ctx,
		valid,
	); !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("pre-cleanup source apply error=%v", err)
	}
	assertBackendDeadlineSourceApplyUnchanged(
		t,
		pool,
		started,
		beforeSettlement,
		beforeDomain,
	)

	var writeFencedState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(
		writeFenced.Source.State,
		&writeFencedState,
	); err != nil {
		t.Fatal(err)
	}
	if writeFencedState.ForceStop == nil {
		t.Fatalf("write-fenced source state=%+v", writeFencedState)
	}
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		writeFencedState.ForceStop.ExpectedContainerID,
		"switchback-runtime-secret-"+started.RuntimeInstanceID,
		appliedAt,
		appliedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return appliedAt },
	}).AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        writeFenced.SettlementID,
			ExpectedRevision:    writeFenced.Revision,
			ExpectedStateDigest: writeFenced.Source.StateDigest,
			LeaseOwnerID:        writeFenced.LeaseOwnerID,
			LeaseFence:          writeFenced.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	taskSettled := seedBackendDeadlineTaskSettled(
		t,
		pool,
		started,
		cleaned,
		deadlineAt,
	)
	valid.ExpectedRevision = taskSettled.Revision
	valid.ExpectedSourceStateDigest =
		taskSettled.Source.StateDigest
	valid.LeaseOwnerID = taskSettled.LeaseOwnerID
	valid.LeaseFence = taskSettled.LeaseFence
	beforeSettlement = taskSettled
	beforeDomain = loadBackendDeadlineDomainSnapshot(t, pool, started)
	service.Now = func() time.Time {
		return taskSettled.UpdatedAt.Add(time.Second)
	}
	tests := map[string]func(*workerapp.ApplyBackendDeadlineSourceCommand){
		"revision": func(command *workerapp.ApplyBackendDeadlineSourceCommand) {
			command.ExpectedRevision--
		},
		"source digest": func(command *workerapp.ApplyBackendDeadlineSourceCommand) {
			command.ExpectedSourceDigest =
				"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"source state digest": func(command *workerapp.ApplyBackendDeadlineSourceCommand) {
			command.ExpectedSourceStateDigest =
				"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		},
		"lease owner": func(command *workerapp.ApplyBackendDeadlineSourceCommand) {
			command.LeaseOwnerID = "different-source-worker"
		},
		"lease fence": func(command *workerapp.ApplyBackendDeadlineSourceCommand) {
			command.LeaseFence++
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			command := valid
			mutate(&command)
			if _, err := service.ApplyBackendDeadline(
				ctx,
				command,
			); err == nil {
				t.Fatal("stale source apply authority was accepted")
			}
			assertBackendDeadlineSourceApplyUnchanged(
				t,
				pool,
				started,
				beforeSettlement,
				beforeDomain,
			)
		})
	}
}

func TestBackendDeadlineTerminalWitnessPreventsForceFenceAndSchedule(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, leased, deadlineAt := setupLeasedBackendDeadlineSource(
		t,
		pool,
		time.Date(2026, 7, 27, 2, 45, 0, 0, time.UTC),
	)
	var processNonce string
	var streamAttempt, ownerFence uint64
	if err := pool.QueryRow(ctx, `
SELECT process_nonce,stream_attempt,owner_fence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&processNonce,
		&streamAttempt,
		&ownerFence,
	); err != nil {
		t.Fatal(err)
	}
	terminalAt := deadlineAt.Add(time.Second)
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return terminalAt },
	}
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority: workerapp.RuntimeControlAuthority{
				WorkerSessionID:   started.WorkerSessionID,
				RuntimeInstanceID: started.RuntimeInstanceID,
				RuntimeGeneration: started.RuntimeGeneration,
				ChannelEpoch:      started.ControlChannelEpoch,
				ProcessNonce:      processNonce,
				StreamAttempt:     streamAttempt,
				OwnerFence:        ownerFence,
			},
			Sequence:       1,
			MessageID:      "terminal-wins-before-force",
			IdempotencyKey: "terminal-wins-before-force",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-wins-before-force",
				"terminal-wins-before-force",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	terminalSettlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, leased.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	var terminalState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(
		terminalSettlement.Source.State,
		&terminalState,
	); err != nil {
		t.Fatal(err)
	}
	if terminalState.Phase !=
		workerapp.BackendDeadlineSourceTerminalObserved {
		t.Fatalf("terminal source state=%+v", terminalState)
	}
	forceAt := terminalState.ForceAfter
	terminalSettlement = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		terminalSettlement,
		forceAt.Add(-time.Second),
	)
	command := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		terminalSettlement,
	)

	var initialStatus string
	var initialChannelRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT status,revision
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&initialStatus,
		&initialChannelRevision,
	); err != nil {
		t.Fatal(err)
	}
	_, err = (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}).AdvanceBackendDeadline(ctx, command)
	if !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("terminal-first force error=%v", err)
	}
	var status string
	var channelRevision uint64
	var reconcileCount int
	if err := pool.QueryRow(ctx, `
SELECT channel.status,channel.revision,
       (SELECT count(*) FROM scheduled_commands
        WHERE scheduled_command_id=$2)
FROM runtime_control_channels channel
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		command.ForceStop.CleanupScheduledCommandID,
	).Scan(
		&status,
		&channelRevision,
		&reconcileCount,
	); err != nil {
		t.Fatal(err)
	}
	if status != initialStatus ||
		channelRevision != initialChannelRevision ||
		reconcileCount != 0 {
		t.Fatalf(
			"terminal-first force changed channel=%s/%d want=%s/%d command_count=%d",
			status,
			channelRevision,
			initialStatus,
			initialChannelRevision,
			reconcileCount,
		)
	}
}

func TestPendingBackendDeadlineSourcePreemptsRuntimeLivenessMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 48, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	heartbeatAt := deadlineAt.Add(-workerapp.RuntimeSuspectAfter)
	ids := &concurrentAllocationIDs{}
	observed, err := (workerapp.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
	}).Observe(
		ctx,
		workerapp.ObserveRuntimeHeartbeatCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
			HeartbeatSequence: 1,
			ControlSequence:   1,
			ReceivedAt:        heartbeatAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	); err != nil {
		t.Fatal(err)
	}

	evaluated, err := (workerapp.RuntimeLivenessService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
		Now:   func() time.Time { return deadlineAt },
	}).Evaluate(
		ctx,
		workerapp.EvaluateRuntimeLivenessCommand{
			TenantID:                    "tenant-local",
			RuntimeInstanceID:           started.RuntimeInstanceID,
			RuntimeGeneration:           started.RuntimeGeneration,
			ChannelEpoch:                started.ControlChannelEpoch,
			HeartbeatSequence:           1,
			Stage:                       workerapp.RuntimeLivenessStageSuspect,
			ExpectedObservationRevision: observed.ObservationRevision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeStatus, heartbeatStatus string
	var runtimeRevision, observationRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT runtime.status,runtime.revision,
       heartbeat.liveness_status,heartbeat.revision
FROM runtime_instances runtime
JOIN runtime_heartbeat_observations heartbeat
  ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
WHERE runtime.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&runtimeStatus,
		&runtimeRevision,
		&heartbeatStatus,
		&observationRevision,
	); err != nil {
		t.Fatal(err)
	}
	if evaluated.Applied ||
		runtimeStatus != "active" ||
		runtimeRevision != active.RuntimeRevision ||
		heartbeatStatus != "healthy" ||
		observationRevision != observed.ObservationRevision {
		t.Fatalf(
			"deadline-owned liveness=%+v runtime=%s/%d heartbeat=%s/%d",
			evaluated,
			runtimeStatus,
			runtimeRevision,
			heartbeatStatus,
			observationRevision,
		)
	}
}

func TestPendingBackendDeadlineRecordsHeartbeatWithoutMutatingRuntimeAuthority(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 2, 49, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	firstHeartbeatAt := deadlineAt.Add(-workerapp.RuntimeSuspectAfter)
	ids := &concurrentAllocationIDs{}
	first, err := (workerapp.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
	}).Observe(
		ctx,
		workerapp.ObserveRuntimeHeartbeatCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
			HeartbeatSequence: 1,
			ControlSequence:   1,
			ReceivedAt:        firstHeartbeatAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	suspect, err := (workerapp.RuntimeLivenessService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
		Now:   func() time.Time { return deadlineAt },
	}).Evaluate(
		ctx,
		workerapp.EvaluateRuntimeLivenessCommand{
			TenantID:                    "tenant-local",
			RuntimeInstanceID:           started.RuntimeInstanceID,
			RuntimeGeneration:           started.RuntimeGeneration,
			ChannelEpoch:                started.ControlChannelEpoch,
			HeartbeatSequence:           1,
			Stage:                       workerapp.RuntimeLivenessStageSuspect,
			ExpectedObservationRevision: first.ObservationRevision,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !suspect.Applied ||
		suspect.LivenessStatus != workerapp.RuntimeLivenessSuspect {
		t.Fatalf("suspect evaluation=%+v", suspect)
	}
	if _, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	); err != nil {
		t.Fatal(err)
	}
	var pendingBefore int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM scheduled_commands
WHERE command_type='runtime_liveness_evaluate'
  AND target_id=$1
  AND status='pending'`,
		started.RuntimeInstanceID,
	).Scan(&pendingBefore); err != nil {
		t.Fatal(err)
	}

	queuedBeforeDeadline := deadlineAt.Add(-time.Microsecond)
	second, err := (workerapp.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
		IDs:   ids,
	}).Observe(
		ctx,
		workerapp.ObserveRuntimeHeartbeatCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
			HeartbeatSequence: 2,
			ControlSequence:   2,
			ReceivedAt:        queuedBeforeDeadline,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	var (
		runtimeStatus, observationStatus string
		runtimeRevision                  uint64
		heartbeatSequence                uint64
		observationRevision              uint64
		pendingAfter, secondSchedule     int
	)
	if err := pool.QueryRow(ctx, `
SELECT runtime.status,runtime.revision,
       heartbeat.heartbeat_sequence,heartbeat.liveness_status,
       heartbeat.revision,
       (SELECT count(*) FROM scheduled_commands
        WHERE command_type='runtime_liveness_evaluate'
          AND target_id=runtime.runtime_instance_id
          AND status='pending'),
       (SELECT count(*) FROM scheduled_commands
        WHERE command_type='runtime_liveness_evaluate'
          AND target_id=runtime.runtime_instance_id
          AND idempotency_key='heartbeat/1/2/suspect')
FROM runtime_instances runtime
JOIN runtime_heartbeat_observations heartbeat
  ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
WHERE runtime.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&runtimeStatus,
		&runtimeRevision,
		&heartbeatSequence,
		&observationStatus,
		&observationRevision,
		&pendingAfter,
		&secondSchedule,
	); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus != "suspect" ||
		runtimeRevision != suspect.RuntimeRevision ||
		heartbeatSequence != 2 ||
		observationStatus != "healthy" ||
		observationRevision != suspect.ObservationRevision+1 ||
		second.RuntimeRevision != suspect.RuntimeRevision ||
		pendingAfter != pendingBefore ||
		secondSchedule != 0 {
		t.Fatalf(
			"deadline-owned heartbeat runtime=%s/r%d heartbeat=%d/%s/r%d result=%+v pending=%d->%d second_schedule=%d",
			runtimeStatus,
			runtimeRevision,
			heartbeatSequence,
			observationStatus,
			observationRevision,
			second,
			pendingBefore,
			pendingAfter,
			secondSchedule,
		)
	}
}

func TestRuntimeTerminalSourceCannotBeReplacedByBackendDeadline(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		base.Add(6*time.Minute),
	)
	accepted, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "runtime-source-wins",
			IdempotencyKey: "runtime-source-wins",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"runtime-source-wins",
				"runtime-source-wins",
				"agent_turn.terminal",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt.Add(time.Second) },
	}
	if _, err := service.ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	); !errors.Is(err, workerapp.ErrTurnOutcomeAuthorityClaimed) {
		t.Fatalf("competing Backend deadline error=%v", err)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Source.Kind != workerapp.TurnSettlementSourceRuntimeEvent ||
		current.Source.Ref != accepted.Message.MessageID ||
		current.Source.Digest != accepted.Message.Fingerprint ||
		current.Revision != 1 {
		t.Fatalf("terminal source was replaced: %+v", current)
	}
}

func TestRuntimeTerminalAtDeadlineCreatesBackendDeadlineSourceWithExactWitness(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		deadlineAt,
	)
	accepted, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-at-deadline",
			IdempotencyKey: "terminal-at-deadline",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-at-deadline",
				"terminal-at-deadline",
				"agent_turn.terminal",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	settlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline ||
		settlement.Source.Reason != "agent_turn_deadline" ||
		!settlement.Source.OccurredAt.Equal(deadlineAt) ||
		settlement.Source.Ref != sourceRef ||
		settlement.Source.Digest == accepted.Message.Fingerprint ||
		settlement.RuntimeInstanceID != started.RuntimeInstanceID ||
		settlement.RuntimeGeneration != started.RuntimeGeneration ||
		settlement.ChannelEpoch != started.ControlChannelEpoch ||
		settlement.TerminalEventSequence != accepted.Message.Sequence ||
		settlement.TerminalMessageID != accepted.Message.MessageID ||
		settlement.TerminalIdempotencyKey !=
			accepted.Message.IdempotencyKey ||
		settlement.TerminalEnvelopeDigest !=
			accepted.Message.Fingerprint ||
		!settlement.AcceptedAt.Equal(deadlineAt) {
		t.Fatalf(
			"boundary terminal settlement=%+v accepted=%+v",
			settlement,
			accepted,
		)
	}
	var boundaryState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(settlement.Source.State, &boundaryState); err != nil {
		t.Fatal(err)
	}
	if boundaryState.Phase !=
		workerapp.BackendDeadlineSourceTerminalObserved ||
		!boundaryState.TerminalObservedAt.Equal(accepted.Message.CreatedAt) {
		t.Fatalf(
			"boundary source state=%+v accepted=%+v",
			boundaryState,
			accepted,
		)
	}
}

func TestRuntimeTerminalAttachesWitnessToExistingBackendDeadlineWithoutReplacingSource(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 45, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	command := backendDeadlineClaimCommand(started, deadlineAt, sourceRef)
	claimedAt := deadlineAt.Add(time.Second)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		deadlineAt.Add(-time.Second),
	)
	sourceService := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return claimedAt },
	}
	original, err := sourceService.ClaimBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	journal = workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return claimedAt.Add(time.Second) },
	}
	accepted, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-after-deadline-source",
			IdempotencyKey: "terminal-after-deadline-source",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-after-deadline-source",
				"terminal-after-deadline-source",
				"agent_turn.terminal",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, original.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Source.Kind != original.Source.Kind ||
		current.Source.Reason != original.Source.Reason ||
		!current.Source.OccurredAt.Equal(original.Source.OccurredAt) ||
		current.Source.Ref != original.Source.Ref ||
		current.Source.Digest != original.Source.Digest ||
		!current.AcceptedAt.Equal(original.AcceptedAt) ||
		current.RuntimeInstanceID != started.RuntimeInstanceID ||
		current.RuntimeGeneration != started.RuntimeGeneration ||
		current.ChannelEpoch != started.ControlChannelEpoch ||
		current.TerminalEventSequence != accepted.Message.Sequence ||
		current.TerminalMessageID != accepted.Message.MessageID ||
		current.TerminalIdempotencyKey !=
			accepted.Message.IdempotencyKey ||
		current.TerminalEnvelopeDigest != accepted.Message.Fingerprint ||
		current.Revision != original.Revision+1 {
		t.Fatalf(
			"attached settlement: original=%+v current=%+v accepted=%+v",
			original,
			current,
			accepted,
		)
	}
	var attachedState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(current.Source.State, &attachedState); err != nil {
		t.Fatal(err)
	}
	if attachedState.Phase !=
		workerapp.BackendDeadlineSourceTerminalObserved ||
		!attachedState.TerminalObservedAt.Equal(accepted.Message.CreatedAt) ||
		current.Source.StateDigest == original.Source.StateDigest {
		t.Fatalf(
			"attached source state: original=%s current=%+v accepted=%+v",
			original.Source.State,
			attachedState,
			accepted,
		)
	}

	replayed, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return claimedAt.Add(3 * time.Second) },
	}).ClaimBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != current.Revision ||
		replayed.Source.Digest != current.Source.Digest ||
		replayed.TerminalMessageID != current.TerminalMessageID {
		t.Fatalf(
			"deadline replay after attachment changed settlement: current=%+v replayed=%+v",
			current,
			replayed,
		)
	}
}

func TestRuntimeTerminalAfterForceIntentAttachesWitnessWithoutChangingSourceBranch(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 48, 0, 0, time.UTC)
	started, leased, deadlineAt := setupLeasedBackendDeadlineSource(
		t,
		pool,
		base,
	)
	var processNonce string
	var streamAttempt, ownerFence uint64
	if err := pool.QueryRow(ctx, `
SELECT process_nonce,stream_attempt,owner_fence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&processNonce,
		&streamAttempt,
		&ownerFence,
	); err != nil {
		t.Fatal(err)
	}
	oldAuthority := workerapp.RuntimeControlAuthority{
		WorkerSessionID:   started.WorkerSessionID,
		RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: started.RuntimeGeneration,
		ChannelEpoch:      started.ControlChannelEpoch,
		ProcessNonce:      processNonce,
		StreamAttempt:     streamAttempt,
		OwnerFence:        ownerFence,
	}
	command := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		leased,
	)
	forceAt := command.ForceStop.DecidedAt
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	command.ExpectedRevision = leased.Revision
	command.ExpectedStateDigest = leased.Source.StateDigest
	forced, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}).AdvanceBackendDeadline(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	var fencedStatus string
	var fencedAt, channelUpdatedAt time.Time
	var fencedOwnerFence, fenceRevision, fenceOwnerFence uint64
	var channelRevision uint64
	var runtimeRevision, runtimeLastEventSequence uint64
	if err := pool.QueryRow(ctx, `
SELECT channel.status,channel.owner_fence,channel.revision,
       channel.updated_at,channel.fenced_at,channel.fence_revision,
       channel.fence_owner_fence,
       runtime.revision,runtime.last_event_sequence
FROM runtime_control_channels channel
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=channel.runtime_instance_id
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&fencedStatus,
		&fencedOwnerFence,
		&channelRevision,
		&channelUpdatedAt,
		&fencedAt,
		&fenceRevision,
		&fenceOwnerFence,
		&runtimeRevision,
		&runtimeLastEventSequence,
	); err != nil {
		t.Fatal(err)
	}
	if fencedStatus != "fenced" ||
		fencedOwnerFence != oldAuthority.OwnerFence+1 ||
		fenceOwnerFence != fencedOwnerFence ||
		fenceRevision != channelRevision ||
		!fencedAt.Equal(forceAt) ||
		!channelUpdatedAt.Equal(forceAt) {
		t.Fatalf(
			"durable force snapshot: status=%s owner=%d old=%d revision=%d fence=%d/%d/%s updated=%s",
			fencedStatus,
			fencedOwnerFence,
			oldAuthority.OwnerFence,
			channelRevision,
			fenceOwnerFence,
			fenceRevision,
			fencedAt,
			channelUpdatedAt,
		)
	}
	acceptedAt := forceAt.Add(time.Second)
	envelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"terminal-after-force-intent",
		"terminal-after-force-intent",
		"agent_turn.terminal",
	)
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return acceptedAt },
	}
	accepted := acceptForceFencedTerminalWithLockOrderProbe(
		t,
		pool,
		forced,
		acceptedAt,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      oldAuthority,
			Sequence:       1,
			MessageID:      "terminal-after-force-intent",
			IdempotencyKey: "terminal-after-force-intent",
			Envelope:       envelope,
		},
	)
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, forced.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	var currentState workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(current.Source.State, &currentState); err != nil {
		t.Fatal(err)
	}
	if currentState.Phase !=
		workerapp.BackendDeadlineSourceForceStopIntent ||
		string(current.Source.State) != string(forced.Source.State) ||
		current.Source.StateDigest != forced.Source.StateDigest ||
		current.TerminalMessageID != accepted.Message.MessageID ||
		current.TerminalEnvelopeDigest != accepted.Message.Fingerprint ||
		current.Revision != forced.Revision+1 {
		t.Fatalf(
			"late terminal changed force branch: settlement=%+v state=%+v",
			current,
			currentState,
		)
	}
	var currentRuntimeRevision, currentRuntimeLastEventSequence uint64
	if err := pool.QueryRow(ctx, `
SELECT revision,last_event_sequence
FROM runtime_instances
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&currentRuntimeRevision,
		&currentRuntimeLastEventSequence,
	); err != nil {
		t.Fatal(err)
	}
	if currentRuntimeRevision != runtimeRevision ||
		currentRuntimeLastEventSequence != runtimeLastEventSequence {
		t.Fatalf(
			"fenced terminal evidence mutated Runtime aggregate: before=%d/%d after=%d/%d",
			runtimeRevision,
			runtimeLastEventSequence,
			currentRuntimeRevision,
			currentRuntimeLastEventSequence,
		)
	}
	var afterChannelRevision, afterEventSequence uint64
	var afterChannelUpdatedAt, afterFencedAt time.Time
	var afterFenceRevision, afterFenceOwnerFence uint64
	if err := pool.QueryRow(ctx, `
SELECT revision,event_committed_sequence,updated_at,
       fenced_at,fence_revision,fence_owner_fence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&afterChannelRevision,
		&afterEventSequence,
		&afterChannelUpdatedAt,
		&afterFencedAt,
		&afterFenceRevision,
		&afterFenceOwnerFence,
	); err != nil {
		t.Fatal(err)
	}
	if afterChannelRevision <= channelRevision ||
		afterEventSequence != 1 ||
		!afterChannelUpdatedAt.Equal(acceptedAt) ||
		!afterFencedAt.Equal(fencedAt) ||
		afterFenceRevision != fenceRevision ||
		afterFenceOwnerFence != fenceOwnerFence ||
		!accepted.Channel.FencedAt.Equal(fencedAt) ||
		accepted.Channel.FenceRevision != fenceRevision ||
		accepted.Channel.FenceOwnerFence != fenceOwnerFence {
		t.Fatalf(
			"post-fence cursor did not preserve snapshot: accepted=%+v channel=%d/%d/%s snapshot=%d/%d/%s",
			accepted.Channel,
			afterChannelRevision,
			afterEventSequence,
			afterChannelUpdatedAt,
			afterFenceOwnerFence,
			afterFenceRevision,
			afterFencedAt,
		)
	}

	journal.Now = func() time.Time { return acceptedAt.Add(time.Second) }
	duplicate, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      oldAuthority,
			Sequence:       1,
			MessageID:      "terminal-after-force-intent",
			IdempotencyKey: "terminal-after-force-intent",
			Envelope:       envelope,
		},
	)
	if err != nil {
		t.Fatalf("replay exact fenced terminal evidence: %v", err)
	}
	if !duplicate.Message.Duplicate ||
		duplicate.Message.CursorDigest != accepted.Message.CursorDigest ||
		duplicate.Message.Fingerprint != accepted.Message.Fingerprint {
		t.Fatalf("fenced terminal duplicate=%+v", duplicate)
	}
	var duplicateChannelRevision, duplicateSettlementRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT channel.revision,settlement.revision
FROM runtime_control_channels channel
JOIN worker_turn_settlements settlement
  ON settlement.runtime_instance_id=channel.runtime_instance_id
 AND settlement.settlement_id=$2
WHERE channel.runtime_instance_id=$1`,
		started.RuntimeInstanceID,
		current.SettlementID,
	).Scan(
		&duplicateChannelRevision,
		&duplicateSettlementRevision,
	); err != nil {
		t.Fatal(err)
	}
	if duplicateChannelRevision != afterChannelRevision ||
		duplicateSettlementRevision != current.Revision {
		t.Fatalf(
			"duplicate changed durable state: channel=%d/%d settlement=%d/%d",
			afterChannelRevision,
			duplicateChannelRevision,
			current.Revision,
			duplicateSettlementRevision,
		)
	}

	tests := []struct {
		name        string
		authority   workerapp.RuntimeControlAuthority
		messageType string
	}{
		{
			name: "wrong process nonce",
			authority: func() workerapp.RuntimeControlAuthority {
				value := oldAuthority
				value.ProcessNonce = "wrong-process"
				return value
			}(),
			messageType: "agent_turn.terminal",
		},
		{
			name: "wrong stream attempt",
			authority: func() workerapp.RuntimeControlAuthority {
				value := oldAuthority
				value.StreamAttempt++
				return value
			}(),
			messageType: "agent_turn.terminal",
		},
		{
			name: "wrong owner fence",
			authority: func() workerapp.RuntimeControlAuthority {
				value := oldAuthority
				value.OwnerFence++
				return value
			}(),
			messageType: "agent_turn.terminal",
		},
		{
			name:        "ordinary event from fenced stream",
			authority:   oldAuthority,
			messageType: "agent_turn.update",
		},
		{
			name:        "different terminal evidence",
			authority:   oldAuthority,
			messageType: "agent_turn.terminal",
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messageID := fmt.Sprintf("rejected-fenced-event-%d", index)
			_, err := journal.AcceptEvent(
				ctx,
				workerapp.AcceptRuntimeControlEventCommand{
					Authority:      test.authority,
					Sequence:       2,
					MessageID:      messageID,
					IdempotencyKey: messageID,
					Envelope: runtimeControlEnvelope(
						started,
						"event",
						2,
						messageID,
						messageID,
						test.messageType,
					),
				},
			)
			if err == nil {
				t.Fatal("invalid post-fence event was accepted")
			}
			var eventCount, eventSequence, revision uint64
			if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM runtime_control_messages
   WHERE runtime_instance_id=$1 AND direction='event'),
  event_committed_sequence,
  revision
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
				started.RuntimeInstanceID,
			).Scan(
				&eventCount,
				&eventSequence,
				&revision,
			); err != nil {
				t.Fatal(err)
			}
			if eventCount != 1 ||
				eventSequence != 1 ||
				revision != afterChannelRevision {
				t.Fatalf(
					"rejected post-fence event partially committed: count=%d sequence=%d revision=%d want=%d",
					eventCount,
					eventSequence,
					revision,
					afterChannelRevision,
				)
			}
		})
	}

	containerAbsentAt := acceptedAt.Add(3 * time.Second)
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		fenceOwnerFence,
		fenceRevision,
		fencedAt,
		currentState.ForceStop.ExpectedContainerID,
		containerAbsentAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceNow := containerAbsentAt
	sourceService := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return sourceNow },
	}
	writeFenced, err := sourceService.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        current.SettlementID,
			ExpectedRevision:    current.Revision,
			ExpectedStateDigest: current.Source.StateDigest,
			LeaseOwnerID:        current.LeaseOwnerID,
			LeaseFence:          current.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	if err != nil {
		t.Fatalf("advance force-first source to write fenced: %v", err)
	}
	cleanupAt := containerAbsentAt.Add(time.Second)
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		currentState.ForceStop.ExpectedContainerID,
		"switchback-runtime-secret-"+started.RuntimeInstanceID,
		cleanupAt,
		cleanupAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceNow = cleanupAt
	cleaned, err := sourceService.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        writeFenced.SettlementID,
			ExpectedRevision:    writeFenced.Revision,
			ExpectedStateDigest: writeFenced.Source.StateDigest,
			LeaseOwnerID:        writeFenced.LeaseOwnerID,
			LeaseFence:          writeFenced.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatalf("advance force-first source to cleanup confirmed: %v", err)
	}
	taskSettled := seedBackendDeadlineTaskSettled(
		t,
		pool,
		started,
		cleaned,
		deadlineAt,
	)
	journal.Now = func() time.Time { return cleanupAt.Add(time.Second) }
	applied, err := journal.ApplyEvent(
		ctx,
		workerapp.ApplyRuntimeControlEventCommand{
			Authority:      oldAuthority,
			Sequence:       accepted.Message.Sequence,
			MessageID:      accepted.Message.MessageID,
			IdempotencyKey: "terminal-after-force-intent/apply",
			Outcome: json.RawMessage(
				`{"status":"forced_terminal_ignored","effect_disposition":"ignore"}`,
			),
			TurnSettlement: &workerapp.TurnSettlementApplyAuthority{
				SettlementID:     taskSettled.SettlementID,
				ExpectedRevision: taskSettled.Revision,
				LeaseOwnerID:     taskSettled.LeaseOwnerID,
				LeaseFence:       taskSettled.LeaseFence,
			},
		},
	)
	if err != nil {
		t.Fatalf("apply force-first terminal witness: %v", err)
	}
	if applied.Message.EffectStatus != "applied" ||
		applied.Channel.EventAppliedSequence != accepted.Message.Sequence {
		t.Fatalf("force-first terminal Apply=%+v", applied)
	}
	converged, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, taskSettled.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	domainAfter := loadBackendDeadlineDomainSnapshot(t, pool, started)
	if converged.Phase !=
		workerapp.TurnSettlementTerminalEventApplied ||
		converged.TerminalEventDisposition !=
			workerapp.RuntimeControlEffectDispositionIgnore ||
		domainAfter.RuntimeStatus != "terminated" ||
		!domainAfter.RuntimeCleanupConfirmed ||
		domainAfter.ActiveRuntimeInstanceID != "" ||
		domainAfter.SessionStatus != "waiting_user" {
		t.Fatalf(
			"force-first convergence settlement=%+v domain=%+v",
			converged,
			domainAfter,
		)
	}
}

func acceptForceFencedTerminalWithLockOrderProbe(
	t *testing.T,
	pool *pgxpool.Pool,
	settlement workerapp.TurnSettlement,
	acceptedAt time.Time,
	command workerapp.AcceptRuntimeControlEventCommand,
) workerapp.RuntimeControlEventAcceptance {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = holder.Rollback(context.Background())
		}
	}()
	var locked int
	if err := holder.QueryRow(ctx, `
SELECT 1
FROM worker_turn_settlements
WHERE settlement_id=$1
FOR UPDATE`,
		settlement.SettlementID,
	).Scan(&locked); err != nil {
		t.Fatal(err)
	}

	const applicationName = "switchback-test-force-terminal-lock-order"
	config := pool.Config().Copy()
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	acceptPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer acceptPool.Close()
	type acceptanceResult struct {
		accepted workerapp.RuntimeControlEventAcceptance
		err      error
	}
	result := make(chan acceptanceResult, 1)
	go func() {
		accepted, acceptErr := (workerapp.RuntimeControlJournalService{
			Store: workerpostgres.NewRuntimeControlJournal(acceptPool),
			Now:   func() time.Time { return acceptedAt },
		}).AcceptEvent(ctx, command)
		result <- acceptanceResult{accepted: accepted, err: acceptErr}
	}()

	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_stat_activity
  WHERE application_name=$1
    AND wait_event_type='Lock'
)`,
			applicationName,
		).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case completed := <-result:
			t.Fatalf(
				"terminal Accept did not wait for settlement row: result=%+v error=%v",
				completed.accepted,
				completed.err,
			)
		case <-ctx.Done():
			t.Fatalf(
				"terminal Accept did not reach settlement row lock: %v",
				ctx.Err(),
			)
		case <-time.After(10 * time.Millisecond):
		}
	}

	probe, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.QueryRow(ctx, `
SELECT 1
FROM worker_sessions
WHERE worker_session_id=$1
FOR UPDATE NOWAIT`,
		settlement.WorkerSessionID,
	).Scan(&locked); err != nil {
		_ = probe.Rollback(context.Background())
		t.Fatalf(
			"terminal Accept locked Session before settlement: %v",
			err,
		)
	}
	if err := probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	released = true

	select {
	case completed := <-result:
		if completed.err != nil {
			t.Fatalf(
				"accept exact terminal evidence from force-fenced stream: %v",
				completed.err,
			)
		}
		return completed.accepted
	case <-ctx.Done():
		t.Fatalf("terminal Accept did not finish after lock release: %v", ctx.Err())
		return workerapp.RuntimeControlEventAcceptance{}
	}
}

func TestPreDeadlineRuntimeTerminalDuplicateAfterDeadlinePreservesRuntimeSource(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 50, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, _ := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	firstAcceptedAt := deadlineAt.Add(-time.Microsecond)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		firstAcceptedAt,
	)
	envelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		"terminal-before-deadline",
		"terminal-before-deadline",
		"agent_turn.terminal",
	)
	first, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-before-deadline",
			IdempotencyKey: "terminal-before-deadline",
			Envelope:       envelope,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return deadlineAt.Add(time.Second) },
	}).AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-before-deadline",
			IdempotencyKey: "terminal-before-deadline",
			Envelope:       envelope,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	settlement, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlementID)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Message.Duplicate ||
		settlement.Source.Kind !=
			workerapp.TurnSettlementSourceRuntimeEvent ||
		!settlement.Source.OccurredAt.Equal(first.Message.CreatedAt) ||
		!settlement.AcceptedAt.Equal(first.Message.CreatedAt) ||
		settlement.Revision != 1 {
		t.Fatalf(
			"duplicate re-elected source: first=%+v duplicate=%+v settlement=%+v",
			first,
			duplicate,
			settlement,
		)
	}
}

func TestTurnSettlementLoadRejectsEventWitnessThatDiffersFromJournal(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 3, 55, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, _ := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	journal, authority := bindAndClaimRuntimeControl(
		t,
		pool,
		started,
		deadlineAt,
	)
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      "terminal-witness-original",
			IdempotencyKey: "terminal-witness-original",
			Envelope: runtimeControlEnvelope(
				started,
				"event",
				1,
				"terminal-witness-original",
				"terminal-witness-original",
				"agent_turn.terminal",
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	settlementID, _ := workerapp.TurnSettlementID(started.AgentTurnID)
	if _, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET terminal_message_id='terminal-witness-forged'
WHERE settlement_id=$1`,
		settlementID,
	); err != nil {
		t.Fatal(err)
	}
	_, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlementID)
	if !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("forged event witness load error=%v", err)
	}
}

func TestBackendDeadlineClaimPreemptsRuntimeRecoveryWithoutSettlingDomain(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	recovery := workerapp.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}
	opened, err := recovery.Open(
		ctx,
		runtimeRecoveryOpenCommand(
			started,
			active,
			base.Add(6*time.Minute+time.Second),
			domain.RecoveryTriggerContainerExit,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	before := loadRecoveryBusinessSnapshot(t, pool, started)

	claimedAt := deadlineAt.Add(time.Second)
	settlement, err := (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return claimedAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatal(err)
	}
	var sourceState map[string]any
	if err := json.Unmarshal(
		settlement.Source.State,
		&sourceState,
	); err != nil {
		t.Fatal(err)
	}
	if sourceState["recovery_incident_id"] != opened.IncidentID {
		t.Fatalf(
			"deadline source is not bound to recovery: state=%s",
			settlement.Source.State,
		)
	}

	var phase, outcome, outcomeReason string
	var preemptingSettlementID string
	var incidentRevision uint64
	var closedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT phase,outcome,outcome_reason,preempted_by_turn_settlement_id,
       revision,closed_at
FROM runtime_recovery_incidents
WHERE incident_id=$1`,
		opened.IncidentID,
	).Scan(
		&phase,
		&outcome,
		&outcomeReason,
		&preemptingSettlementID,
		&incidentRevision,
		&closedAt,
	); err != nil {
		t.Fatal(err)
	}
	if phase != "exhausted" ||
		outcome != "exhausted" ||
		outcomeReason != "preempted_by_backend_deadline" ||
		preemptingSettlementID != settlement.SettlementID ||
		incidentRevision != opened.IncidentRevision+1 ||
		!closedAt.Equal(claimedAt) {
		t.Fatalf(
			"preempted incident: phase=%s outcome=%s reason=%s revision=%d closed=%s",
			phase,
			outcome,
			outcomeReason,
			incidentRevision,
			closedAt,
		)
	}
	var eventCount int
	var eventSettlementID, eventIncidentID string
	if err := pool.QueryRow(ctx, `
SELECT count(*),
       COALESCE(max(payload->>'settlement_id'),''),
       COALESCE(max(payload->>'incident_id'),'')
FROM outbox_events
WHERE event_type='RuntimeRecoveryPreemptedByBackendDeadline.v1'
  AND correlation_id=$1`,
		opened.IncidentID,
	).Scan(
		&eventCount,
		&eventSettlementID,
		&eventIncidentID,
	); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 ||
		eventSettlementID != settlement.SettlementID ||
		eventIncidentID != opened.IncidentID {
		t.Fatalf(
			"preemption outbox count=%d settlement=%s incident=%s",
			eventCount,
			eventSettlementID,
			eventIncidentID,
		)
	}
	after := loadRecoveryBusinessSnapshot(t, pool, started)
	if after != before {
		t.Fatalf(
			"deadline claim settled mutable domain: before=%+v after=%+v",
			before,
			after,
		)
	}

	_, err = (workerapp.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return claimedAt.Add(time.Second) },
	}).Advance(ctx, workerapp.AdvanceRuntimeRecoveryCommand{
		TenantID:                 "tenant-local",
		WorkerSessionID:          started.WorkerSessionID,
		IncidentID:               opened.IncidentID,
		TargetPhase:              domain.RecoveryFencing,
		ExpectedIncidentRevision: opened.IncidentRevision,
		IdempotencyKey:           "stale-recovery-after-deadline",
	})
	if !errors.Is(err, platformpostgres.ErrRevisionConflict) {
		t.Fatalf("stale recovery Advance error=%v", err)
	}
}

func TestBackendDeadlineRecoveryPreemptionRollsBackWithOutboxFailure(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	recovery := workerapp.RuntimeRecoveryService{
		Store: workerpostgres.NewRuntimeRecoveryStore(pool),
		IDs:   &concurrentAllocationIDs{},
		Now:   func() time.Time { return base.Add(7 * time.Minute) },
	}
	opened, err := recovery.Open(
		ctx,
		runtimeRecoveryOpenCommand(
			started,
			active,
			base.Add(6*time.Minute+time.Second),
			domain.RecoveryTriggerContainerExit,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	before := loadRecoveryBusinessSnapshot(t, pool, started)
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION switchback_test_reject_deadline_preemption_outbox()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.event_type =
     'RuntimeRecoveryPreemptedByBackendDeadline.v1' THEN
    RAISE EXCEPTION 'injected deadline preemption Outbox failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER switchback_test_reject_deadline_preemption_outbox
BEFORE INSERT ON outbox_events
FOR EACH ROW
EXECUTE FUNCTION switchback_test_reject_deadline_preemption_outbox()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS switchback_test_reject_deadline_preemption_outbox
  ON outbox_events;
DROP FUNCTION IF EXISTS
  switchback_test_reject_deadline_preemption_outbox()`)
	})

	_, err = (workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return deadlineAt.Add(time.Second) },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err == nil {
		t.Fatal("injected Outbox failure did not fail deadline claim")
	}

	var settlementCount int
	var phase, outcomeReason string
	var incidentRevision uint64
	var preemptingSettlementID *string
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*)
   FROM worker_turn_settlements
   WHERE agent_turn_id=$2),
  phase,
  COALESCE(outcome_reason,''),
  revision,
  preempted_by_turn_settlement_id
FROM runtime_recovery_incidents
WHERE incident_id=$1`,
		opened.IncidentID,
		started.AgentTurnID,
	).Scan(
		&settlementCount,
		&phase,
		&outcomeReason,
		&incidentRevision,
		&preemptingSettlementID,
	); err != nil {
		t.Fatal(err)
	}
	if settlementCount != 0 ||
		phase != string(domain.RecoveryOpen) ||
		outcomeReason != "" ||
		incidentRevision != opened.IncidentRevision ||
		preemptingSettlementID != nil {
		t.Fatalf(
			"failed preemption partially committed: settlements=%d phase=%s reason=%s revision=%d binding=%v",
			settlementCount,
			phase,
			outcomeReason,
			incidentRevision,
			preemptingSettlementID,
		)
	}
	after := loadRecoveryBusinessSnapshot(t, pool, started)
	if after != before {
		t.Fatalf(
			"failed preemption mutated domain: before=%+v after=%+v",
			before,
			after,
		)
	}
}

type recoveryBusinessSnapshot struct {
	SessionStatus   string
	RunStatus       string
	TurnStatus      string
	RuntimeStatus   string
	TaskStatus      string
	SessionRevision uint64
	RunRevision     uint64
	TurnRevision    uint64
	RuntimeRevision uint64
	TaskRevision    uint64
}

func loadRecoveryBusinessSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
) recoveryBusinessSnapshot {
	t.Helper()
	var snapshot recoveryBusinessSnapshot
	if err := pool.QueryRow(context.Background(), `
SELECT session.status,run.status,turn.status,runtime.status,task.status,
       session.session_revision,run.revision,turn.revision,runtime.revision,
       task.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_run_id=session.active_worker_run_id
JOIN agent_turns turn
  ON turn.agent_turn_id=$2
 AND turn.worker_run_id=run.worker_run_id
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=$3
 AND runtime.worker_session_id=session.worker_session_id
JOIN tasks task
  ON task.task_id=run.task_id
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(
		&snapshot.SessionStatus,
		&snapshot.RunStatus,
		&snapshot.TurnStatus,
		&snapshot.RuntimeStatus,
		&snapshot.TaskStatus,
		&snapshot.SessionRevision,
		&snapshot.RunRevision,
		&snapshot.TurnRevision,
		&snapshot.RuntimeRevision,
		&snapshot.TaskRevision,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func loadAgentTurnDeadlineSource(
	t *testing.T,
	pool *pgxpool.Pool,
	agentTurnID string,
	runtimeInstanceID string,
) (time.Time, string) {
	t.Helper()
	var tenantID, sourceRef string
	var deadlineAt, dueAt time.Time
	var runtimeGeneration, channelEpoch uint64
	if err := pool.QueryRow(
		context.Background(),
		`
SELECT session.tenant_id,turn.deadline_at,command.scheduled_command_id,
       command.due_at,runtime.generation,runtime.control_channel_epoch
FROM agent_turns turn
JOIN worker_sessions session
  ON session.worker_session_id=turn.worker_session_id
JOIN runtime_instances runtime
  ON runtime.worker_session_id=turn.worker_session_id
 AND runtime.worker_run_id=turn.worker_run_id
JOIN scheduled_commands command
  ON command.command_type='agent_turn_deadline'
 AND command.target_type='AgentTurn'
 AND command.target_id=turn.agent_turn_id
WHERE turn.agent_turn_id=$1
  AND runtime.runtime_instance_id=$2`,
		agentTurnID,
		runtimeInstanceID,
	).Scan(
		&tenantID,
		&deadlineAt,
		&sourceRef,
		&dueAt,
		&runtimeGeneration,
		&channelEpoch,
	); err != nil {
		t.Fatal(err)
	}
	if tenantID != "tenant-local" ||
		!deadlineAt.Equal(dueAt) ||
		runtimeGeneration == 0 ||
		channelEpoch == 0 {
		t.Fatalf(
			"invalid deadline source fixture: tenant=%s deadline=%s due=%s generation=%d epoch=%d",
			tenantID,
			deadlineAt,
			dueAt,
			runtimeGeneration,
			channelEpoch,
		)
	}
	return deadlineAt.UTC(), sourceRef
}

func backendDeadlineClaimCommand(
	started workerapp.StartedWorkerRun,
	deadlineAt time.Time,
	sourceRef string,
) workerapp.ClaimBackendDeadlineCommand {
	return workerapp.ClaimBackendDeadlineCommand{
		TenantID:          "tenant-local",
		WorkerSessionID:   started.WorkerSessionID,
		WorkerRunID:       started.WorkerRunID,
		AgentSessionID:    started.AgentSessionID,
		AgentTurnID:       started.AgentTurnID,
		RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: started.RuntimeGeneration,
		ChannelEpoch:      started.ControlChannelEpoch,
		DeadlineAt:        deadlineAt,
		SourceRef:         sourceRef,
	}
}

type backendDeadlineLateTerminalApplyFixture struct {
	Started    workerapp.StartedWorkerRun
	Settlement workerapp.TurnSettlement
	MessageID  string
	Apply      workerapp.ApplyRuntimeControlEventCommand
}

// installBackendDeadlineLateTerminalWitnessFixture installs the same durable
// received-message/header state produced by AcceptEvent without widening the
// live transport's accepted Runtime statuses. RuntimeRecovery deliberately
// rejects new live events from an unhealthy generation; this fixture only
// exercises the already-durable late-witness source Apply branch.
func installBackendDeadlineLateTerminalWitnessFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	settlement workerapp.TurnSettlement,
	messageID string,
	envelope json.RawMessage,
	acceptedAt time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	ctx := context.Background()
	fingerprint := runtimeControlEnvelopeFingerprint(t, envelope)
	source, changed, err :=
		workerapp.ReconcileBackendDeadlineTerminalWitness(
			settlement.Source,
			acceptedAt,
		)
	if err != nil || !changed {
		t.Fatalf(
			"reconcile fixture terminal witness: changed=%t err=%v",
			changed,
			err,
		)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var committedSequence, channelRevision uint64
	var committedDigest string
	if err := tx.QueryRow(ctx, `
SELECT event_committed_sequence,event_committed_digest,revision
FROM runtime_control_channels
WHERE runtime_instance_id=$1
FOR UPDATE`,
		started.RuntimeInstanceID,
	).Scan(
		&committedSequence,
		&committedDigest,
		&channelRevision,
	); err != nil {
		t.Fatal(err)
	}
	if committedSequence != 0 {
		t.Fatalf(
			"fixture event predecessor sequence=%d",
			committedSequence,
		)
	}
	cursorDigest := runtimeControlCursor(
		t,
		committedDigest,
		contractsv1.RuntimeControlDirectionRuntimeToBackend,
		envelope,
	)
	if _, err := tx.Exec(ctx, `
INSERT INTO runtime_control_messages
  (runtime_instance_id,direction,sequence,message_id,idempotency_key,
   fingerprint,cursor_digest,envelope,outcome,effect_status,created_at,
   acknowledged_at)
VALUES ($1,'event',1,$2,$2,$3,$4,$5,NULL,'received',$6,$6)`,
		started.RuntimeInstanceID,
		messageID,
		fingerprint,
		cursorDigest,
		envelope,
		acceptedAt,
	); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, `
UPDATE runtime_control_channels
SET event_committed_sequence=1,event_committed_digest=$2,
    event_acknowledged_sequence=1,revision=revision+1,updated_at=$3
WHERE runtime_instance_id=$1
  AND event_committed_sequence=0
  AND revision=$4`,
		started.RuntimeInstanceID,
		cursorDigest,
		acceptedAt,
		channelRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf(
			"advance fixture event cursor rows=%d",
			tag.RowsAffected(),
		)
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_instances
SET last_event_sequence=1,revision=revision+1
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	tag, err = tx.Exec(ctx, `
UPDATE worker_turn_settlements
SET runtime_instance_id=$2,runtime_generation=$3,channel_epoch=$4,
    terminal_event_direction='event',terminal_event_sequence=1,
    terminal_message_id=$5,terminal_idempotency_key=$5,
    terminal_envelope_digest=$6,source_state=$7,source_state_digest=$8,
    revision=revision+1,updated_at=$9
WHERE settlement_id=$1
  AND revision=$10
  AND source_digest=$11
  AND source_state_digest=$12
  AND runtime_instance_id IS NULL
  AND terminal_event_sequence IS NULL`,
		settlement.SettlementID,
		started.RuntimeInstanceID,
		started.RuntimeGeneration,
		started.ControlChannelEpoch,
		messageID,
		fingerprint,
		source.State,
		source.StateDigest,
		acceptedAt,
		settlement.Revision,
		settlement.Source.Digest,
		settlement.Source.StateDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf(
			"attach fixture terminal witness rows=%d",
			tag.RowsAffected(),
		)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	attached, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	return attached
}

func prepareBackendDeadlineLateTerminalApplyFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
	prefix string,
) backendDeadlineLateTerminalApplyFixture {
	return prepareBackendDeadlineLateTerminalApplyFixtureWithRuntimePredecessor(
		t,
		pool,
		base,
		prefix,
		"active",
	)
}

func prepareBackendDeadlineLateTerminalApplyFixtureWithRuntimePredecessor(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
	prefix string,
	runtimePredecessor string,
) backendDeadlineLateTerminalApplyFixture {
	t.Helper()
	ctx := context.Background()
	started, leased, deadlineAt :=
		setupLeasedBackendDeadlineSourceWithRuntimePredecessor(
			t,
			pool,
			base,
			runtimePredecessor,
		)
	claimedSource := leased.Source
	var processNonce string
	var streamAttempt, ownerFence uint64
	if err := pool.QueryRow(ctx, `
SELECT process_nonce,stream_attempt,owner_fence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&processNonce,
		&streamAttempt,
		&ownerFence,
	); err != nil {
		t.Fatal(err)
	}
	authority := workerapp.RuntimeControlAuthority{
		WorkerSessionID:   started.WorkerSessionID,
		RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: started.RuntimeGeneration,
		ChannelEpoch:      started.ControlChannelEpoch,
		ProcessNonce:      processNonce,
		StreamAttempt:     streamAttempt,
		OwnerFence:        ownerFence,
	}
	messageID := prefix + "-event"
	forceAt := deadlineAt.Add(workerapp.BackendDeadlineForceGrace)
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	acceptedAt := forceAt.Add(time.Second)
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return acceptedAt },
	}
	eventEnvelope := runtimeControlEnvelope(
		started,
		"event",
		1,
		messageID,
		messageID,
		"agent_turn.terminal",
	)
	var attached workerapp.TurnSettlement
	if runtimePredecessor == "unhealthy_preempted_recovery" {
		attached = installBackendDeadlineLateTerminalWitnessFixture(
			t,
			pool,
			started,
			leased,
			messageID,
			eventEnvelope,
			acceptedAt,
		)
	} else {
		if _, err := journal.AcceptEvent(
			ctx,
			workerapp.AcceptRuntimeControlEventCommand{
				Authority:      authority,
				Sequence:       1,
				MessageID:      messageID,
				IdempotencyKey: messageID,
				Envelope:       eventEnvelope,
			},
		); err != nil {
			t.Fatalf("accept forced late terminal: %v", err)
		}
		var err error
		attached, err = (workerapp.TurnSettlementService{
			Store: workerpostgres.NewTurnSettlementRepository(pool),
		}).Load(ctx, leased.SettlementID)
		if err != nil {
			t.Fatalf("load forced late terminal settlement: %v", err)
		}
	}
	if attached.TerminalMessageID != messageID ||
		attached.TerminalEventSequence != 1 {
		t.Fatalf(
			"forced late terminal attachment: before=%+v after=%+v",
			leased,
			attached,
		)
	}
	forceCommand := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		attached,
	)
	fencedAt := acceptedAt.Add(time.Second)
	journal.Now = func() time.Time { return fencedAt }
	fenced, err := journal.Fence(
		ctx,
		workerapp.FenceRuntimeControlChannelCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
		},
	)
	if err != nil {
		t.Fatalf("fence forced late terminal channel: %v", err)
	}
	if fenced.Status != "fenced" ||
		fenced.OwnerFence != authority.OwnerFence+1 {
		t.Fatalf(
			"forced late terminal durable fence=%+v authority=%+v",
			fenced,
			authority,
		)
	}
	forced, err := workerapp.ApplyBackendDeadlineSourceTransition(
		claimedSource,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase:   workerapp.BackendDeadlineSourceForceStopIntent,
			ForceStop: forceCommand.ForceStop,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	containerAbsentAt := fencedAt.Add(time.Second)
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		fenced.OwnerFence,
		fenced.Revision,
		fencedAt,
		forceCommand.ForceStop.ExpectedContainerID,
		containerAbsentAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	writeFenced, err := workerapp.ApplyBackendDeadlineSourceTransition(
		forced,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase:    workerapp.BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAt := containerAbsentAt.Add(time.Second)
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		forceCommand.ForceStop.ExpectedContainerID,
		"switchback-runtime-secret-"+started.RuntimeInstanceID,
		cleanupAt,
		cleanupAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanedSource, err := workerapp.ApplyBackendDeadlineSourceTransition(
		writeFenced,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase: workerapp.BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := pool.Exec(ctx, `
UPDATE worker_turn_settlements
SET source_state=$2,source_state_digest=$3,
    revision=revision+1,updated_at=$4
WHERE settlement_id=$1
  AND revision=$5
  AND source_digest=$6
  AND source_state_digest=$7
  AND lease_owner_id=$8
  AND lease_fence=$9
  AND runtime_instance_id=$10
  AND terminal_event_sequence=1
  AND terminal_message_id=$11`,
		attached.SettlementID,
		cleanedSource.State,
		cleanedSource.StateDigest,
		cleanupAt,
		attached.Revision,
		attached.Source.Digest,
		attached.Source.StateDigest,
		attached.LeaseOwnerID,
		attached.LeaseFence,
		started.RuntimeInstanceID,
		messageID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("install forced late-witness cleanup rows=%d", tag.RowsAffected())
	}
	ready, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, attached.SettlementID)
	if err != nil {
		t.Fatalf("load forced late-witness cleanup: %v", err)
	}
	taskSettled := seedBackendDeadlineTaskSettled(
		t,
		pool,
		started,
		ready,
		deadlineAt,
	)
	return backendDeadlineLateTerminalApplyFixture{
		Started:    started,
		Settlement: taskSettled,
		MessageID:  messageID,
		Apply: workerapp.ApplyRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      messageID,
			IdempotencyKey: prefix + "-apply",
			Outcome: json.RawMessage(
				`{"status":"forced_terminal_ignored","effect_disposition":"ignore"}`,
			),
			TurnSettlement: &workerapp.TurnSettlementApplyAuthority{
				SettlementID:     taskSettled.SettlementID,
				ExpectedRevision: taskSettled.Revision,
				LeaseOwnerID:     taskSettled.LeaseOwnerID,
				LeaseFence:       taskSettled.LeaseFence,
			},
		},
	}
}

func prepareBackendDeadlineSourceForApply(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
	includeCleanup bool,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
	time.Time,
) {
	return prepareBackendDeadlineSourceForApplyWithRuntimePredecessor(
		t,
		pool,
		base,
		includeCleanup,
		"active",
	)
}

func prepareBackendDeadlineSourceForApplyWithRuntimePredecessor(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
	includeCleanup bool,
	runtimePredecessor string,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
	time.Time,
) {
	t.Helper()
	ctx := context.Background()
	started, leased, deadlineAt :=
		setupLeasedBackendDeadlineSourceWithRuntimePredecessor(
			t,
			pool,
			base,
			runtimePredecessor,
		)
	forceCommand := backendDeadlineForceStopAdvanceCommand(
		t,
		pool,
		started,
		leased,
	)
	forceAt := forceCommand.ForceStop.DecidedAt
	leased = renewBackendDeadlineSourceLeaseAt(
		t,
		pool,
		leased,
		forceAt.Add(-time.Second),
	)
	forceCommand.ExpectedRevision = leased.Revision
	forceCommand.ExpectedStateDigest = leased.Source.StateDigest
	service := workerapp.TurnSettlementSourceService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return forceAt },
	}
	forced, err := service.AdvanceBackendDeadline(ctx, forceCommand)
	if err != nil {
		t.Fatal(err)
	}
	var channelOwnerFence, channelRevision uint64
	var channelFencedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT owner_fence,revision,updated_at
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&channelOwnerFence,
		&channelRevision,
		&channelFencedAt,
	); err != nil {
		t.Fatal(err)
	}
	absentAt := forceAt.Add(time.Second)
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		channelOwnerFence,
		channelRevision,
		channelFencedAt,
		forceCommand.ForceStop.ExpectedContainerID,
		absentAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return absentAt }
	current, err := service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        forced.SettlementID,
			ExpectedRevision:    forced.Revision,
			ExpectedStateDigest: forced.Source.StateDigest,
			LeaseOwnerID:        forced.LeaseOwnerID,
			LeaseFence:          forced.LeaseFence,
			ToPhase:             workerapp.BackendDeadlineSourceWriteFenced,
			WriteFence:          &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !includeCleanup {
		return started, current, deadlineAt
	}
	confirmedAt := absentAt.Add(time.Second)
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		forceCommand.ForceStop.ExpectedContainerID,
		"switchback-runtime-secret-"+started.RuntimeInstanceID,
		confirmedAt,
		confirmedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return confirmedAt }
	current, err = service.AdvanceBackendDeadline(
		ctx,
		workerapp.AdvanceBackendDeadlineSourceCommand{
			SettlementID:        current.SettlementID,
			ExpectedRevision:    current.Revision,
			ExpectedStateDigest: current.Source.StateDigest,
			LeaseOwnerID:        current.LeaseOwnerID,
			LeaseFence:          current.LeaseFence,
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return started, current, deadlineAt
}

func seedBackendDeadlineTaskSettled(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	settlement workerapp.TurnSettlement,
	deadlineAt time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	return seedBackendDeadlineTaskSettledWithMode(
		t,
		pool,
		started,
		settlement,
		deadlineAt,
		false,
	)
}

func seedBackendDeadlineCancelledTaskSettled(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	settlement workerapp.TurnSettlement,
	deadlineAt time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	return seedBackendDeadlineTaskSettledWithMode(
		t,
		pool,
		started,
		settlement,
		deadlineAt,
		true,
	)
}

func seedBackendDeadlineTaskSettledWithMode(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	settlement workerapp.TurnSettlement,
	deadlineAt time.Time,
	settleCancelled bool,
) workerapp.TurnSettlement {
	t.Helper()
	ctx := context.Background()
	// startActiveRecoveryFixture stops at the durable delivery boundary. This
	// source-apply fixture starts after the Runtime has consumed StartAgentTurn,
	// so advance only that test-owned Turn to the real state-machine predecessor
	// of timed_out. Production transition validation remains unchanged.
	tag, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='consumed',revision=revision+1
WHERE agent_turn_id=$1 AND status='delivered'`,
		started.AgentTurnID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() == 0 {
		var turnStatus string
		if err := pool.QueryRow(ctx, `
SELECT status
FROM agent_turns
WHERE agent_turn_id=$1`,
			started.AgentTurnID,
		).Scan(&turnStatus); err != nil {
			t.Fatal(err)
		}
		if turnStatus != "consumed" {
			t.Fatalf(
				"consume Backend deadline fixture Turn rows=0 status=%s",
				turnStatus,
			)
		}
	}
	var cancelled taskapp.CancelledTask
	if settleCancelled {
		var taskRevision uint64
		if err := pool.QueryRow(ctx, `
SELECT revision
FROM tasks
WHERE task_id=$1`,
			started.TaskID,
		).Scan(&taskRevision); err != nil {
			t.Fatal(err)
		}
		cancelled, err = (taskapp.TaskActionService{
			Store: taskpostgres.NewStore(pool),
			IDs:   &concurrentAllocationIDs{},
			Now: func() time.Time {
				return settlement.UpdatedAt.Add(2 * time.Microsecond)
			},
		}).Cancel(
			ctx,
			taskapp.CancelTaskCommand{
				Actor: taskapp.Actor{
					TenantID: "tenant-local",
					UserID:   "user-local",
				},
				TaskID:               started.TaskID,
				ExpectedTaskRevision: taskRevision,
				IdempotencyKey: "cancel-before-deadline-settlement-" +
					started.AgentTurnID,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	var runtimeStatus string
	if err := pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,
       runtime.revision,runtime.status
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.agent_turn_id=$3
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.runtime_instance_id=$4
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
		&runtimeStatus,
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
		SessionTarget:           domain.SessionWaitingUser,
		RunTarget:               domain.RunTimedOut,
		TurnTarget:              domain.TurnTimedOut,
		TerminalReason:          "agent_turn_deadline",
	}
	currentTime := settlement.UpdatedAt.Add(time.Second)
	service := workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}
	checkpoint := validBackendDeadlineSettlementCheckpoint(
		t,
		started,
		stage,
		"backend-deadline-report-"+started.AgentTurnID,
		"backend-deadline-patch-"+started.AgentTurnID,
		deadlineAt,
		workerapp.TurnSettlementAccepted,
		taskIDForBackendDeadlineCheckpoint(started, settleCancelled),
		nil,
		nil,
	)
	current, err := service.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     settlement.SettlementID,
			ExpectedRevision: settlement.Revision,
			LeaseOwnerID:     settlement.LeaseOwnerID,
			LeaseFence:       settlement.LeaseFence,
			Checkpoint: typedTurnSettlementCheckpoint(
				t,
				checkpoint,
			),
		},
	)
	if err != nil {
		t.Fatalf("freeze Backend deadline settlement checkpoint: %v", err)
	}
	checkpoint = validBackendDeadlineSettlementCheckpoint(
		t,
		started,
		stage,
		"backend-deadline-report-"+started.AgentTurnID,
		"backend-deadline-patch-"+started.AgentTurnID,
		deadlineAt,
		workerapp.TurnSettlementEvidencePrepared,
		taskIDForBackendDeadlineCheckpoint(started, settleCancelled),
		nil,
		nil,
	)
	currentTime = currentTime.Add(time.Second)
	current, err = service.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			FromPhase:        current.Phase,
			ToPhase: workerapp.
				TurnSettlementEvidencePrepared,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatalf("prepare Backend deadline settlement evidence: %v", err)
	}
	currentTime = currentTime.Add(time.Second)
	committed, err := (workerapp.TurnSettlementDomainService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return currentTime },
	}).Commit(
		ctx,
		workerapp.CommitTurnSettlementDomainCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			Resident:         stage,
		},
	)
	if err != nil {
		t.Fatalf("commit Backend deadline settlement domain: %v", err)
	}
	current = committed.Settlement
	if committed.Resident.SessionStatus != domain.SessionWaitingUser ||
		committed.Resident.RunStatus != domain.RunTimedOut ||
		committed.Resident.TurnStatus != domain.TurnTimedOut ||
		committed.Resident.RuntimeStatus != runtimeStatus ||
		committed.Resident.SessionRevision != sessionRevision+1 ||
		committed.Resident.RuntimeRevision != runtimeRevision {
		t.Fatalf("Backend deadline domain commit=%+v", committed)
	}
	checkpoint = validBackendDeadlineSettlementCheckpoint(
		t,
		started,
		stage,
		"backend-deadline-report-"+started.AgentTurnID,
		"backend-deadline-patch-"+started.AgentTurnID,
		deadlineAt,
		workerapp.TurnSettlementEvidencePublished,
		taskIDForBackendDeadlineCheckpoint(started, settleCancelled),
		nil,
		nil,
	)
	currentTime = currentTime.Add(time.Second)
	current, err = service.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			FromPhase:        current.Phase,
			ToPhase: workerapp.
				TurnSettlementEvidencePublished,
			Checkpoint: checkpoint,
		},
	)
	if err != nil {
		t.Fatalf("advance Backend deadline settlement to evidence: %v", err)
	}
	var taskPlan, taskResult map[string]any
	if settleCancelled {
		currentTime = currentTime.Add(time.Second)
		settled, err := (taskapp.TaskActionService{
			Store: taskpostgres.NewStore(pool),
			IDs:   &concurrentAllocationIDs{},
			Now:   func() time.Time { return currentTime },
		}).SettleCancelled(
			ctx,
			taskapp.SettleCancelledTaskCommand{
				Actor: taskapp.Actor{
					TenantID: "tenant-local",
					UserID:   "user-local",
				},
				TaskID:                  started.TaskID,
				WorkerSessionID:         started.WorkerSessionID,
				WorkerRunID:             started.WorkerRunID,
				ExpectedTaskRevision:    cancelled.Revision,
				ExpectedSessionRevision: committed.Proof.SessionRevision,
				IdempotencyKey: "settle-cancelled-deadline-" +
					started.AgentTurnID,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		taskPlan = map[string]any{
			"action":                 "settle_cancelled",
			"task_id":                started.TaskID,
			"expected_task_revision": cancelled.Revision,
			"idempotency_key": "settle-cancelled-deadline-" +
				started.AgentTurnID,
		}
		taskResult = map[string]any{
			"status":   "applied",
			"revision": settled.SessionRevision,
			"event_id": settled.EventID,
		}
	}
	checkpoint = validBackendDeadlineSettlementCheckpoint(
		t,
		started,
		stage,
		"backend-deadline-report-"+started.AgentTurnID,
		"backend-deadline-patch-"+started.AgentTurnID,
		deadlineAt,
		workerapp.TurnSettlementTaskSettled,
		taskIDForBackendDeadlineCheckpoint(started, settleCancelled),
		taskPlan,
		taskResult,
	)
	currentTime = currentTime.Add(time.Second)
	current, err = service.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			FromPhase:        current.Phase,
			ToPhase:          workerapp.TurnSettlementTaskSettled,
			Checkpoint:       checkpoint,
		},
	)
	if err != nil {
		t.Fatalf("advance Backend deadline settlement to task: %v", err)
	}
	return current
}

func taskIDForBackendDeadlineCheckpoint(
	started workerapp.StartedWorkerRun,
	include bool,
) string {
	if include {
		return started.TaskID
	}
	return ""
}

func validBackendDeadlineSettlementCheckpoint(
	t *testing.T,
	started workerapp.StartedWorkerRun,
	stage workerapp.CompleteResidentStageCommand,
	reportArtifactID string,
	patchArtifactID string,
	recordedAt time.Time,
	phase workerapp.TurnSettlementPhase,
	taskID string,
	taskPlan map[string]any,
	taskResult map[string]any,
) json.RawMessage {
	t.Helper()
	checkpoint := validTurnSettlementCheckpoint(
		t,
		started,
		stage,
		reportArtifactID,
		patchArtifactID,
		recordedAt,
		phase,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	source, ok := fields["source"].(map[string]any)
	if !ok {
		t.Fatalf("checkpoint source=%T", fields["source"])
	}
	source["kind"] = string(workerapp.TurnSettlementSourceBackendDeadline)
	source["reason"] = "agent_turn_deadline"
	source["occurred_at"] = recordedAt
	source["timed_out"] = true
	source["forced_stop"] = true
	if taskID != "" {
		evidenceInput, ok := fields["evidence_input"].(map[string]any)
		if !ok {
			t.Fatalf(
				"checkpoint evidence input=%T",
				fields["evidence_input"],
			)
		}
		evidenceInput["task_id"] = taskID
		if turnSettlementTestPhaseAtLeast(
			phase,
			workerapp.TurnSettlementEvidencePrepared,
		) {
			evidence, ok := fields["evidence"].(map[string]any)
			if !ok {
				t.Fatalf("checkpoint evidence=%T", fields["evidence"])
			}
			finalized, ok := evidence["finalized"].(map[string]any)
			if !ok {
				t.Fatalf(
					"checkpoint finalized evidence=%T",
					evidence["finalized"],
				)
			}
			artifactRefs, ok := finalized["ArtifactRefs"].([]any)
			if !ok {
				t.Fatalf(
					"checkpoint Artifact refs=%T",
					finalized["ArtifactRefs"],
				)
			}
			for _, artifactRef := range artifactRefs {
				artifact, ok := artifactRef.(map[string]any)
				if !ok {
					t.Fatalf(
						"checkpoint Artifact ref=%T",
						artifactRef,
					)
				}
				artifact["task_id"] = taskID
			}
		}
	}
	if taskPlan != nil {
		fields["task_plan"] = taskPlan
	}
	if taskResult != nil {
		fields["task_result"] = taskResult
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err :=
		workerapp.CanonicalizeTurnSettlementCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		phase,
		canonical,
	); err != nil {
		t.Fatal(err)
	}
	return canonical
}

func assertBackendDeadlineSourceApplyUnchanged(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	wantSettlement workerapp.TurnSettlement,
	wantDomain backendDeadlineDomainSnapshot,
) {
	t.Helper()
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(context.Background(), wantSettlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != wantSettlement.Revision ||
		current.Phase != wantSettlement.Phase ||
		current.Source.Digest != wantSettlement.Source.Digest ||
		current.Source.StateDigest != wantSettlement.Source.StateDigest ||
		current.PhaseStateDigest != wantSettlement.PhaseStateDigest ||
		current.TerminalEventDisposition !=
			wantSettlement.TerminalEventDisposition ||
		current.TerminalApplyOutcomeDigest !=
			wantSettlement.TerminalApplyOutcomeDigest ||
		!current.UpdatedAt.Equal(wantSettlement.UpdatedAt) {
		t.Fatalf(
			"rejected source apply mutated settlement: before=%+v after=%+v",
			wantSettlement,
			current,
		)
	}
	if currentDomain := loadBackendDeadlineDomainSnapshot(
		t,
		pool,
		started,
	); currentDomain != wantDomain {
		t.Fatalf(
			"rejected source apply mutated domain: before=%+v after=%+v",
			wantDomain,
			currentDomain,
		)
	}
}

func replaceBackendDeadlineSourceStateFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	settlement workerapp.TurnSettlement,
	sourceState json.RawMessage,
	sourceStateDigest string,
) workerapp.TurnSettlement {
	t.Helper()
	tag, err := pool.Exec(
		context.Background(),
		`
UPDATE worker_turn_settlements
SET source_state=$2,source_state_digest=$3
WHERE settlement_id=$1
  AND revision=$4
  AND source_state_digest=$5`,
		settlement.SettlementID,
		sourceState,
		sourceStateDigest,
		settlement.Revision,
		settlement.Source.StateDigest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf(
			"replace Backend deadline source state rows=%d",
			tag.RowsAffected(),
		)
	}
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(context.Background(), settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func setupLeasedBackendDeadlineSource(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
	time.Time,
) {
	return setupLeasedBackendDeadlineSourceWithRuntimePredecessor(
		t,
		pool,
		base,
		"active",
	)
}

func setupLeasedBackendDeadlineSourceWithRuntimePredecessor(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
	runtimePredecessor string,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
	time.Time,
) {
	t.Helper()
	ctx := context.Background()
	started, active := startActiveRecoveryFixture(t, pool, base)
	deadlineAt, sourceRef := loadAgentTurnDeadlineSource(
		t,
		pool,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	)
	_, _ = bindAndClaimRuntimeControl(t, pool, started, deadlineAt)
	switch runtimePredecessor {
	case "active":
	case "suspect":
		heartbeatAt := base.Add(6 * time.Minute)
		heartbeatIDs := &concurrentAllocationIDs{}
		heartbeat, err := (workerapp.RuntimeHeartbeatService{
			Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
			IDs:   heartbeatIDs,
		}).Observe(
			ctx,
			workerapp.ObserveRuntimeHeartbeatCommand{
				WorkerSessionID:   started.WorkerSessionID,
				RuntimeInstanceID: started.RuntimeInstanceID,
				RuntimeGeneration: started.RuntimeGeneration,
				ChannelEpoch:      started.ControlChannelEpoch,
				HeartbeatSequence: 1,
				ControlSequence:   1,
				ReceivedAt:        heartbeatAt,
			},
		)
		if err != nil {
			t.Fatalf("observe suspect predecessor heartbeat: %v", err)
		}
		suspectAt := heartbeatAt.Add(workerapp.RuntimeSuspectAfter)
		evaluated, err := (workerapp.RuntimeLivenessService{
			Store: workerpostgres.NewRuntimeHeartbeatStore(pool),
			IDs:   heartbeatIDs,
			Now:   func() time.Time { return suspectAt },
		}).Evaluate(
			ctx,
			workerapp.EvaluateRuntimeLivenessCommand{
				TenantID:                    "tenant-local",
				RuntimeInstanceID:           started.RuntimeInstanceID,
				RuntimeGeneration:           started.RuntimeGeneration,
				ChannelEpoch:                started.ControlChannelEpoch,
				HeartbeatSequence:           1,
				Stage:                       workerapp.RuntimeLivenessStageSuspect,
				ExpectedObservationRevision: heartbeat.ObservationRevision,
			},
		)
		if err != nil {
			t.Fatalf("mark source predecessor suspect: %v", err)
		}
		if !evaluated.Applied ||
			evaluated.LivenessStatus != workerapp.RuntimeLivenessSuspect {
			t.Fatalf("suspect predecessor evaluation=%+v", evaluated)
		}
	case "unhealthy_preempted_recovery":
		opened, err := (workerapp.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool),
			IDs:   &concurrentAllocationIDs{},
			Now:   func() time.Time { return base.Add(7 * time.Minute) },
		}).Open(
			ctx,
			runtimeRecoveryOpenCommand(
				started,
				active,
				base.Add(6*time.Minute+time.Second),
				domain.RecoveryTriggerContainerExit,
			),
		)
		if err != nil {
			t.Fatalf("open source predecessor recovery: %v", err)
		}
		if opened.Phase != domain.RecoveryOpen {
			t.Fatalf("source predecessor recovery=%+v", opened)
		}
	default:
		t.Fatalf(
			"unknown Backend deadline Runtime predecessor %q",
			runtimePredecessor,
		)
	}
	repository := workerpostgres.NewTurnSettlementRepository(pool)
	claimed, err := (workerapp.TurnSettlementSourceService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		ctx,
		backendDeadlineClaimCommand(started, deadlineAt, sourceRef),
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceState, err := workerapp.DecodeBackendDeadlineSourceState(
		claimed.Source,
	)
	if err != nil {
		t.Fatal(err)
	}
	switch runtimePredecessor {
	case "active", "suspect":
		if sourceState.RecoveryIncidentID != "" ||
			sourceState.RecoveryIncidentRevision != 0 ||
			!sourceState.RecoveryPreemptedAt.IsZero() {
			t.Fatalf(
				"%s source unexpectedly bound recovery: %+v",
				runtimePredecessor,
				sourceState,
			)
		}
	case "unhealthy_preempted_recovery":
		if sourceState.RecoveryIncidentID == "" ||
			sourceState.RecoveryIncidentRevision == 0 ||
			sourceState.RecoveryPreemptedAt.IsZero() {
			t.Fatalf(
				"unhealthy source lacks exact recovery binding: %+v",
				sourceState,
			)
		}
	}
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			OwnerID:          "deadline-source-worker-1",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return started, leased, deadlineAt
}

func renewBackendDeadlineSourceLeaseAt(
	t *testing.T,
	pool *pgxpool.Pool,
	settlement workerapp.TurnSettlement,
	at time.Time,
) workerapp.TurnSettlement {
	t.Helper()
	renewed, err := (workerapp.TurnSettlementLeaseService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
		Now:   func() time.Time { return at },
	}).Renew(
		context.Background(),
		workerapp.RenewTurnSettlementLeaseCommand{
			SettlementID:     settlement.SettlementID,
			ExpectedRevision: settlement.Revision,
			LeaseOwnerID:     settlement.LeaseOwnerID,
			LeaseFence:       settlement.LeaseFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return renewed
}

type backendDeadlineDomainSnapshot struct {
	SessionStatus           string
	SessionRevision         uint64
	ActiveRuntimeInstanceID string
	RunStatus               string
	RunRevision             uint64
	TurnStatus              string
	TurnRevision            uint64
	RuntimeStatus           string
	RuntimeRevision         uint64
	RuntimeCleanupConfirmed bool
	TaskStatus              string
	TaskRevision            uint64
}

func loadBackendDeadlineDomainSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
) backendDeadlineDomainSnapshot {
	t.Helper()
	var snapshot backendDeadlineDomainSnapshot
	if err := pool.QueryRow(context.Background(), `
SELECT session.status,session.session_revision,
       COALESCE(session.active_runtime_instance_id,''),
       run.status,run.revision,
       turn.status,turn.revision,
       runtime.status,runtime.revision,runtime.cleanup_confirmed,
       task.status,task.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id=$3
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.runtime_instance_id=$4
JOIN tasks task ON task.task_id=$5
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.WorkerRunID,
		started.AgentTurnID,
		started.RuntimeInstanceID,
		started.TaskID,
	).Scan(
		&snapshot.SessionStatus,
		&snapshot.SessionRevision,
		&snapshot.ActiveRuntimeInstanceID,
		&snapshot.RunStatus,
		&snapshot.RunRevision,
		&snapshot.TurnStatus,
		&snapshot.TurnRevision,
		&snapshot.RuntimeStatus,
		&snapshot.RuntimeRevision,
		&snapshot.RuntimeCleanupConfirmed,
		&snapshot.TaskStatus,
		&snapshot.TaskRevision,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func backendDeadlineInterruptAdvanceCommand(
	started workerapp.StartedWorkerRun,
	leased workerapp.TurnSettlement,
) workerapp.AdvanceBackendDeadlineSourceCommand {
	messageID := "deadline-interrupt-" + started.AgentTurnID
	return workerapp.AdvanceBackendDeadlineSourceCommand{
		SettlementID:            leased.SettlementID,
		ExpectedRevision:        leased.Revision,
		ExpectedStateDigest:     leased.Source.StateDigest,
		LeaseOwnerID:            leased.LeaseOwnerID,
		LeaseFence:              leased.LeaseFence,
		ToPhase:                 workerapp.BackendDeadlineSourceInterruptQueued,
		InterruptMessageID:      messageID,
		InterruptIdempotencyKey: messageID,
		InterruptEnvelope: runtimeControlEnvelope(
			started,
			"command",
			0,
			messageID,
			messageID,
			"agent_turn.interrupt",
		),
	}
}

func backendDeadlineForceStopAdvanceCommand(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapp.StartedWorkerRun,
	leased workerapp.TurnSettlement,
) workerapp.AdvanceBackendDeadlineSourceCommand {
	t.Helper()
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(leased.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	var sessionRevision, runtimeRevision uint64
	var containerID string
	if err := pool.QueryRow(context.Background(), `
SELECT session.session_revision,runtime.revision,
       COALESCE(runtime.container_id,'')
FROM worker_sessions session
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.runtime_instance_id=$2
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID,
		started.RuntimeInstanceID,
	).Scan(
		&sessionRevision,
		&runtimeRevision,
		&containerID,
	); err != nil {
		t.Fatal(err)
	}
	return workerapp.AdvanceBackendDeadlineSourceCommand{
		SettlementID:        leased.SettlementID,
		ExpectedRevision:    leased.Revision,
		ExpectedStateDigest: leased.Source.StateDigest,
		LeaseOwnerID:        leased.LeaseOwnerID,
		LeaseFence:          leased.LeaseFence,
		ToPhase:             workerapp.BackendDeadlineSourceForceStopIntent,
		ForceStop: &workerapp.BackendDeadlineForceStopProof{
			IntentID:                  "deadline-force-stop-" + started.AgentTurnID,
			DecidedAt:                 state.ForceAfter,
			RuntimeInstanceID:         state.RuntimeInstanceID,
			RuntimeGeneration:         state.RuntimeGeneration,
			ChannelEpoch:              state.ChannelEpoch,
			ExpectedSessionRevision:   sessionRevision,
			ExpectedRuntimeRevision:   runtimeRevision,
			ExpectedContainerID:       containerID,
			CleanupScheduledCommandID: "deadline-cleanup-command-" + started.AgentTurnID,
			CleanupIdempotencyKey:     "deadline-cleanup-" + started.AgentTurnID,
		},
	}
}
