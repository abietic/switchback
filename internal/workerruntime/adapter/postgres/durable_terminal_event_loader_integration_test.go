//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestDurableTerminalEventLoaderAppliesTerminalObservedWithoutLiveHub(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, settlement := prepareDurableTerminalObservedSettlement(
		t,
		pool,
		time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC),
		"durable-terminal-loader",
	)
	applyAt := settlement.UpdatedAt.Add(time.Second)
	loader := workerapp.DurableTerminalEventLoaderService{
		Store: workerpostgres.NewDurableTerminalEventLoader(
			pool,
			func() time.Time { return applyAt },
		),
	}

	// No controlgrpc Hub, live Runtime, or runtime map participates: the
	// accepted terminal envelope and delivery authority come only from
	// PostgreSQL.
	event, err := loader.Load(ctx, settlement)
	if err != nil {
		t.Fatal(err)
	}
	if string(event.Envelope.AgentTurnID) != settlement.AgentTurnID ||
		event.Envelope.Sequence != settlement.TerminalEventSequence ||
		event.Envelope.MessageID != settlement.TerminalMessageID ||
		event.Envelope.IdempotencyKey !=
			settlement.TerminalIdempotencyKey ||
		event.Envelope.MessageType != "agent_turn.terminal" ||
		string(event.Payload) != string(event.Envelope.Payload) {
		t.Fatalf("durable terminal event=%+v", event)
	}
	delivery, ok := event.Delivery.(workerapp.TurnSettlementWorkerControlEventDelivery)
	if !ok {
		t.Fatalf(
			"durable terminal delivery=%T lacks Turn settlement Apply",
			event.Delivery,
		)
	}
	outcome := json.RawMessage(
		`{"status":"terminal_applied","effect_disposition":"commit"}`,
	)
	authority := workerapp.TurnSettlementApplyAuthority{
		SettlementID:     settlement.SettlementID,
		ExpectedRevision: settlement.Revision,
		LeaseOwnerID:     settlement.LeaseOwnerID,
		LeaseFence:       settlement.LeaseFence,
	}
	if err := delivery.ApplyTurnSettlement(
		ctx,
		outcome,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	applied, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase !=
		workerapp.TurnSettlementTerminalEventApplied ||
		applied.TerminalEventDisposition !=
			workerapp.RuntimeControlEffectDispositionCommit {
		t.Fatalf("durable terminal settlement Apply=%+v", applied)
	}
	var effectStatus string
	var eventAppliedSequence uint64
	if err := pool.QueryRow(ctx, `
SELECT message.effect_status,channel.event_applied_sequence
FROM runtime_control_messages message
JOIN runtime_control_channels channel
  ON channel.runtime_instance_id=message.runtime_instance_id
WHERE message.runtime_instance_id=$1
  AND message.direction='event'
  AND message.sequence=$2`,
		started.RuntimeInstanceID,
		settlement.TerminalEventSequence,
	).Scan(
		&effectStatus,
		&eventAppliedSequence,
	); err != nil {
		t.Fatal(err)
	}
	if effectStatus != "applied" ||
		eventAppliedSequence != settlement.TerminalEventSequence {
		t.Fatalf(
			"durable terminal inbox=%s/%d",
			effectStatus,
			eventAppliedSequence,
		)
	}

	replay, err := loader.Load(ctx, applied)
	if err != nil {
		t.Fatalf("load already-applied terminal: %v", err)
	}
	replayDelivery, ok := replay.Delivery.(workerapp.TurnSettlementWorkerControlEventDelivery)
	if !ok {
		t.Fatalf("replay delivery=%T", replay.Delivery)
	}
	beforeRevision := applied.Revision
	if err := replayDelivery.ApplyTurnSettlement(
		ctx,
		outcome,
		authority,
	); err != nil {
		t.Fatalf("replay already-applied terminal: %v", err)
	}
	replayed, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != beforeRevision {
		t.Fatalf(
			"already-applied replay changed settlement revision: before=%d after=%d",
			beforeRevision,
			replayed.Revision,
		)
	}
}

func TestDurableTerminalEventLoaderAllowsCommittedHeartbeatAfterTerminal(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, settlement := prepareDurableTerminalObservedSettlement(
		t,
		pool,
		time.Date(2026, 7, 27, 8, 5, 0, 0, time.UTC),
		"durable-terminal-followed-by-heartbeat",
	)
	var processNonce string
	var streamAttempt, ownerFence, commandAppliedSequence uint64
	if err := pool.QueryRow(ctx, `
SELECT process_nonce,stream_attempt,owner_fence,command_applied_sequence
FROM runtime_control_channels
WHERE runtime_instance_id=$1`,
		started.RuntimeInstanceID,
	).Scan(
		&processNonce,
		&streamAttempt,
		&ownerFence,
		&commandAppliedSequence,
	); err != nil {
		t.Fatal(err)
	}
	heartbeatEnvelope, err := contractsv1.DecodeRuntimeControlEnvelope(
		runtimeControlEnvelopeWithAck(
			t,
			runtimeControlEnvelope(
				started,
				"heartbeat",
				settlement.TerminalEventSequence+1,
				"heartbeat-after-terminal",
				"heartbeat-after-terminal",
				"runtime.heartbeat",
			),
			commandAppliedSequence,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	heartbeatEnvelope.Payload = json.RawMessage(`{"heartbeat_sequence":1}`)
	encodedHeartbeat, err := json.Marshal(heartbeatEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	journal := workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return settlement.UpdatedAt.Add(time.Second) },
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
			Sequence:       settlement.TerminalEventSequence + 1,
			MessageID:      "heartbeat-after-terminal",
			IdempotencyKey: "heartbeat-after-terminal",
			Envelope:       encodedHeartbeat,
		},
	); err != nil {
		t.Fatal(err)
	}
	loader := workerapp.DurableTerminalEventLoaderService{
		Store: workerpostgres.NewDurableTerminalEventLoader(
			pool,
			func() time.Time { return settlement.UpdatedAt.Add(2 * time.Second) },
		),
	}
	event, err := loader.Load(ctx, settlement)
	if err != nil {
		t.Fatal(err)
	}
	if event.Envelope.Sequence != settlement.TerminalEventSequence ||
		event.Envelope.MessageID != settlement.TerminalMessageID {
		t.Fatalf("durable terminal after later heartbeat=%+v", event.Envelope)
	}
}

func TestDurableTerminalEventLoaderRejectsWrongHeaderWithoutMutation(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	_, settlement := prepareDurableTerminalObservedSettlement(
		t,
		pool,
		time.Date(2026, 7, 27, 8, 10, 0, 0, time.UTC),
		"durable-terminal-wrong-header",
	)
	wrong := settlement
	wrong.TerminalMessageID = "different-terminal-message"
	loader := workerapp.DurableTerminalEventLoaderService{
		Store: workerpostgres.NewDurableTerminalEventLoader(
			pool,
			func() time.Time { return settlement.UpdatedAt.Add(time.Second) },
		),
	}
	if _, err := loader.Load(ctx, wrong); !errors.Is(
		err,
		workerapp.ErrTurnSettlementConflict,
	) {
		t.Fatalf("wrong durable terminal header error=%v", err)
	}
	current, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, settlement.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != settlement.Revision ||
		current.Phase != settlement.Phase {
		t.Fatalf(
			"wrong header mutated settlement: before=%+v after=%+v",
			settlement,
			current,
		)
	}
}

func TestDurableTerminalEventLoaderRejectsFencedForcedSource(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	fixture := prepareBackendDeadlineLateTerminalApplyFixture(
		t,
		pool,
		time.Date(2026, 7, 27, 8, 20, 0, 0, time.UTC),
		"durable-terminal-forced-source",
	)
	loader := workerapp.DurableTerminalEventLoaderService{
		Store: workerpostgres.NewDurableTerminalEventLoader(
			pool,
			func() time.Time {
				return fixture.Settlement.UpdatedAt.Add(time.Second)
			},
		),
	}
	if _, err := loader.Load(
		context.Background(),
		fixture.Settlement,
	); !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("forced source durable terminal load error=%v", err)
	}
}

func TestDurableTerminalEventLoaderRejectsFencedTerminalObservedChannel(
	t *testing.T,
) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	started, settlement := prepareDurableTerminalObservedSettlement(
		t,
		pool,
		time.Date(2026, 7, 27, 8, 30, 0, 0, time.UTC),
		"durable-terminal-fenced-channel",
	)
	fenceAt := settlement.UpdatedAt.Add(time.Second)
	if _, err := (workerapp.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return fenceAt },
	}).Fence(
		ctx,
		workerapp.FenceRuntimeControlChannelCommand{
			WorkerSessionID:   started.WorkerSessionID,
			RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration,
			ChannelEpoch:      started.ControlChannelEpoch,
		},
	); err != nil {
		t.Fatal(err)
	}
	loader := workerapp.DurableTerminalEventLoaderService{
		Store: workerpostgres.NewDurableTerminalEventLoader(
			pool,
			func() time.Time { return fenceAt.Add(time.Second) },
		),
	}
	if _, err := loader.Load(ctx, settlement); !errors.Is(
		err,
		workerapp.ErrRuntimeControlStaleOwner,
	) {
		t.Fatalf("fenced terminal-observed channel load error=%v", err)
	}
}

func prepareDurableTerminalObservedSettlement(
	t *testing.T,
	pool *pgxpool.Pool,
	base time.Time,
	messageID string,
) (
	workerapp.StartedWorkerRun,
	workerapp.TurnSettlement,
) {
	t.Helper()
	ctx := context.Background()
	started, _ := startActiveRecoveryFixture(
		t,
		pool,
		base,
	)
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
		deadlineAt.Add(-2*time.Second),
	)
	journal.Now = func() time.Time { return deadlineAt.Add(-time.Second) }
	startMessageID := messageID + "-start"
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
	leased, err := (workerapp.TurnSettlementService{
		Store: repository,
		Now:   func() time.Time { return deadlineAt },
	}).Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			OwnerID:          "durable-terminal-loader",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptedAt := deadlineAt.Add(time.Second)
	journal.Now = func() time.Time { return acceptedAt }
	if _, err := journal.AcceptEvent(
		ctx,
		workerapp.AcceptRuntimeControlEventCommand{
			Authority:      authority,
			Sequence:       1,
			MessageID:      messageID,
			IdempotencyKey: messageID,
			Envelope: runtimeControlEnvelopeWithAck(
				t,
				runtimeControlEnvelope(
					started,
					"event",
					1,
					messageID,
					messageID,
					"agent_turn.terminal",
				),
				start.Sequence,
			),
		},
	); err != nil {
		t.Fatal(err)
	}
	attached, err := (workerapp.TurnSettlementService{
		Store: workerpostgres.NewTurnSettlementRepository(pool),
	}).Load(ctx, leased.SettlementID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := workerapp.DecodeBackendDeadlineSourceState(
		attached.Source,
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase !=
		workerapp.BackendDeadlineSourceTerminalObserved {
		t.Fatalf("durable terminal source=%+v", state)
	}
	return started, seedBackendDeadlineTaskSettled(
		t,
		pool,
		started,
		attached,
		deadlineAt,
	)
}
