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
	"github.com/abietic/switchback/internal/testsupport/runtimecapacity"
	pg "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func initialControlCapacityFixture(t *testing.T) (*pgxpool.Pool, app.StartedWorkerRun, app.RuntimeControlJournalService, app.RuntimeControlAuthority) {
	t.Helper()
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	runtimecapacity.ObserveContainer(t, pool, started.WorkerSessionID, started.RuntimeInstanceID, "initial-actual", started.RuntimeGeneration)
	_, err := pg.NewRuntimeResidencyStore(pool).ActivateResidentRuntime(ctx, app.ActivateResidentRuntimeRecord{Command: app.ActivateResidentRuntimeCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, ContainerID: "initial-actual", RuntimeGeneration: 1, ControlChannelEpoch: 1, ExpectedSessionRevision: started.SessionRevision, ExpectedRunRevision: 1, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1}, Now: now.Add(6 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	service := app.RuntimeControlJournalService{Store: pg.NewRuntimeControlJournal(pool), Now: func() time.Time { return now.Add(7 * time.Minute) }}
	if _, err = service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(ctx, runtimeControlClaim(started, "initial-process", 1, "initial-owner"))
	if err != nil {
		t.Fatal(err)
	}
	connected := completeRuntimeControlClaim(t, service, plan)
	return pool, started, service, authorityFrom(connected)
}
func initialStartCommand(started app.StartedWorkerRun, authority app.RuntimeControlAuthority) app.ReserveRuntimeControlCommandCommand {
	return app.ReserveRuntimeControlCommandCommand{Authority: authority, MessageID: "initial-start", IdempotencyKey: "initial-start", Envelope: runtimeControlEnvelope(started, "command", 0, "initial-start", "initial-start", "agent_turn.start")}
}

func TestInitialExecutionControlExactStartAndDuplicate(t *testing.T) {
	_, started, service, authority := initialControlCapacityFixture(t)
	ctx := context.Background()
	command := initialStartCommand(started, authority)
	first, err := service.ReserveCommand(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.ReserveCommand(ctx, command)
	if err != nil || !again.Duplicate || string(again.Envelope) != string(first.Envelope) {
		t.Fatalf("exact replay: %+v %v", again, err)
	}
}

func TestInitialExecutionControlUnbackedStartCannotReturnOrReplay(t *testing.T) {
	for _, boundary := range []string{"duplicate", "claim_without_active_turn", "replay", "resume"} {
		t.Run(boundary, func(t *testing.T) {
			pool, started, service, authority := initialControlCapacityFixture(t)
			ctx := context.Background()
			command := initialStartCommand(started, authority)
			if _, err := service.ReserveCommand(ctx, command); err != nil {
				t.Fatal(err)
			}
			var resumePlan app.RuntimeControlReplayPlan
			if boundary == "resume" {
				var err error
				resumePlan, err = service.Claim(ctx, runtimeControlClaim(started, "initial-process", 2, "new-owner"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := pool.Exec(ctx, `DELETE FROM runtime_capacity_executions`); err != nil {
				t.Fatal(err)
			}
			var err error
			switch boundary {
			case "duplicate":
				var m app.RuntimeControlMessage
				m, err = service.ReserveCommand(ctx, command)
				if len(m.Envelope) != 0 {
					t.Fatalf("denied command returned envelope: %+v", m)
				}
			case "claim_without_active_turn":
				var plan app.RuntimeControlReplayPlan
				plan, err = service.Claim(ctx, runtimeControlClaim(started, "initial-process", 2, "new-owner"))
				if len(plan.PendingCommands) > 0 {
					t.Fatal("unbacked start delivered")
				}
			case "replay":
				var plan app.RuntimeControlReplayPlan
				plan, err = service.ReplayPlan(ctx, authority)
				if len(plan.PendingCommands) > 0 {
					t.Fatal("unbacked start replayed")
				}
			case "resume":
				_, err = pg.NewRuntimeControlJournal(pool).CompleteRuntimeControlResume(ctx, app.CompleteRuntimeControlResumeRecord{Command: app.CompleteRuntimeControlResumeCommand{Authority: authorityFrom(resumePlan.Channel)}, CompletedAt: time.Now()})
			}
			if !errors.Is(err, app.CapacityBindingMismatch) {
				t.Fatalf("unbacked %s err=%v", boundary, err)
			}
		})
	}
}

func TestInitialExecutionControlRejectsWrongTurnOrCancelledGraph(t *testing.T) {
	for _, bad := range []string{"turn", "run", "generation", "cancelled", "released", "profile", "network"} {
		t.Run(bad, func(t *testing.T) {
			pool, started, service, authority := initialControlCapacityFixture(t)
			ctx := context.Background()
			command := initialStartCommand(started, authority)
			var e contractsv1.RuntimeControlEnvelope
			if err := json.Unmarshal(command.Envelope, &e); err != nil {
				t.Fatal(err)
			}
			var sql string
			switch bad {
			case "turn":
				e.AgentTurnID = "other-turn"
			case "run":
				e.WorkerRunID = "other-run"
			case "generation":
				e.RuntimeGeneration++
			case "cancelled":
				sql = `UPDATE tasks SET status='cancelled'`
			case "released":
				sql = `UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='test_release'`
			case "profile":
				sql = `UPDATE runtime_instances SET resource_profile_id='other-profile'`
			case "network":
				sql = `UPDATE runtime_instances SET network_policy_id='m1_public_web_restricted_v1',network_policy_revision=0,network_policy_digest='',network_enforcement_status='legacy_unenforced'`
			}
			if sql != "" {
				if _, err := pool.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			command.Envelope, _ = json.Marshal(e)
			result, err := service.ReserveCommand(ctx, command)
			if err == nil || len(result.Envelope) != 0 {
				t.Fatalf("%s delivered command: %+v %v", bad, result, err)
			}
		})
	}
}

func TestInitialExecutionActiveReconnectAndCancelSurviveStaleSample(t *testing.T) {
	pool, started, service, authority := initialControlCapacityFixture(t)
	ctx := context.Background()
	message, err := service.ReserveCommand(ctx, initialStartCommand(started, authority))
	if err != nil {
		t.Fatal(err)
	}
	runtimecapacity.ObserveStale(t, pool)
	proof := runtimeControlActiveTurn(t, started, message, contractsv1.RuntimeControlTurnEffectActive)
	claim := runtimeControlClaim(started, "initial-process", 2, "second-owner")
	claim.WorkerCommandLastSequence = message.Sequence
	claim.WorkerCommandAppliedSequence = message.Sequence
	claim.WorkerCommandCursorDigest = message.CursorDigest
	claim.ActiveTurn = &proof
	plan, err := service.Claim(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	authority = authorityFrom(completeRuntimeControlClaim(t, service, plan))
	if len(plan.PendingCommands) != 0 {
		t.Fatal("active reconnect redelivered Prompt grant")
	}
	if _, err = service.ReplayPlan(ctx, authority); err != nil {
		t.Fatalf("active replay blocked by stale sample: %v", err)
	}
	cancel := app.ReserveRuntimeControlCommandCommand{Authority: authority, MessageID: "cancel", IdempotencyKey: "cancel", Envelope: runtimeControlEnvelope(started, "command", 0, "cancel", "cancel", "agent_turn.cancel")}
	if _, err = service.ReserveCommand(ctx, cancel); err != nil {
		t.Fatalf("stale capacity blocked cancellation: %v", err)
	}
}

func TestInitialExecutionBootstrapClaimWithoutStartDoesNotRequireContainerSample(t *testing.T) {
	pool := postgrestest.Open(t)
	now := time.Now().UTC()
	ctx := context.Background()
	started := startInitialRuntimeBeforeActivation(t, pool, now)
	service := app.RuntimeControlJournalService{Store: pg.NewRuntimeControlJournal(pool), Now: func() time.Time { return now.Add(5 * time.Minute) }}
	if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(ctx, runtimeControlClaim(started, "bootstrap", 1, "bootstrap-owner"))
	if err != nil {
		t.Fatal(err)
	}
	connected := completeRuntimeControlClaim(t, service, plan)
	readyEnvelope := runtimeControlEnvelope(started, "event", 1, "bootstrap-ready", "bootstrap-ready", "runtime.ready")
	if _, err = service.AcceptEvent(ctx, app.AcceptRuntimeControlEventCommand{Authority: authorityFrom(connected), Sequence: 1, MessageID: "bootstrap-ready", IdempotencyKey: "bootstrap-ready", Envelope: readyEnvelope}); err != nil {
		t.Fatalf("bootstrap runtime.ready without container proof: %v", err)
	}
	if start, err := service.ReserveCommand(ctx, initialStartCommand(started, authorityFrom(connected))); err == nil || len(start.Envelope) > 0 {
		t.Fatalf("bootstrap delivered a premature start: %v", err)
	}
	if len(plan.PendingCommands) != 0 {
		t.Fatal("bootstrap acquired a start")
	}
}

func TestInitialExecutionTerminalProofReplaySurvivesReleasedCapacity(t *testing.T) {
	pool, started, service, authority := initialControlCapacityFixture(t)
	ctx := context.Background()
	message, err := service.ReserveCommand(ctx, initialStartCommand(started, authority))
	if err != nil {
		t.Fatal(err)
	}
	terminal := runtimeControlEnvelopeWithAck(t, runtimeControlEnvelope(started, "event", 1, "terminal", "terminal", "agent_turn.terminal"), 1)
	proof := runtimeControlActiveTurn(t, started, message, contractsv1.RuntimeControlTurnEffectTerminal)
	proof.LastUpdateSequence = 1
	proof.TerminalEnvelopeDigest = runtimeControlEnvelopeFingerprint(t, terminal)
	claim := runtimeControlClaim(started, "initial-process", 2, "terminal-owner")
	claim.WorkerCommandLastSequence = 1
	claim.WorkerCommandAppliedSequence = 1
	claim.WorkerCommandCursorDigest = message.CursorDigest
	claim.WorkerEventLastSequence = 1
	claim.WorkerEventCursorDigest = runtimeControlCursor(t, contractsv1.RuntimeControlCursorGenesisDigest, contractsv1.RuntimeControlDirectionRuntimeToBackend, terminal)
	claim.ActiveTurn = &proof
	runtimecapacity.ObserveStale(t, pool)
	if _, err = pool.Exec(ctx, `UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='terminal_test'`); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(ctx, claim)
	if err != nil || !plan.TerminalProofPending {
		t.Fatalf("terminal claim: %+v %v", plan, err)
	}
	authority = authorityFrom(plan.Channel)
	replay, err := service.ReplayPlan(ctx, authority)
	if err != nil || !replay.TerminalProofPending || len(replay.PendingCommands) != 0 {
		t.Fatalf("terminal report replay: %+v %v", replay, err)
	}
	if _, err = service.AcceptEvent(ctx, app.AcceptRuntimeControlEventCommand{Authority: authority, Sequence: 1, MessageID: "terminal", IdempotencyKey: "terminal", Envelope: terminal}); err != nil {
		t.Fatalf("terminal convergence blocked: %v", err)
	}
}

func TestInitialExecutionCannotIssueSecondStartGrantForSameTurn(t *testing.T) {
	_, started, service, authority := initialControlCapacityFixture(t)
	ctx := context.Background()
	if _, err := service.ReserveCommand(ctx, initialStartCommand(started, authority)); err != nil {
		t.Fatal(err)
	}
	command := app.ReserveRuntimeControlCommandCommand{Authority: authority, MessageID: "second-start", IdempotencyKey: "second-start", Envelope: runtimeControlEnvelope(started, "command", 0, "second-start", "second-start", "agent_turn.start")}
	if message, err := service.ReserveCommand(ctx, command); !errors.Is(err, app.CapacityBindingMismatch) || len(message.Envelope) != 0 {
		t.Fatalf("second Prompt grant: %+v err=%v", message, err)
	}
}
