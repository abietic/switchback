//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestDisabledHardExitRecordsFailureWithoutReplacement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	store := workerpostgres.NewRuntimeRecoveryStore(pool)
	observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapplication.RuntimeRecoveryService{
		Store: store, IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return base.Add(7 * time.Minute) },
	}, false)
	state, recorded, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID, EvidenceAt: base.Add(6 * time.Minute),
		Inspection: &workerapplication.RuntimeInspection{Exists: true, ContainerID: "container-recovery-1", Generation: 1, ObservedAt: base.Add(6 * time.Minute)},
	})
	if err != nil || !recorded || state.Phase != domain.RecoveryExhausted || state.AttemptCount != 0 || state.ReplacementRuntimeInstanceID != "" {
		t.Fatalf("disabled hard exit remained unrecorded: state=%+v recorded=%t err=%v", state, recorded, err)
	}
}

func TestDisabledFirstHardFailureObserversConverge(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	readTaskSnapshot := func() string {
		t.Helper()
		var snapshot string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
  'task',(SELECT to_jsonb(task) FROM tasks task WHERE task_id='task-01'),
  'transitions',(SELECT jsonb_agg(to_jsonb(transition) ORDER BY transition_id) FROM task_transitions transition WHERE task_id='task-01'),
  'events',(SELECT jsonb_agg(to_jsonb(event) ORDER BY event_id) FROM outbox_events event WHERE stream_id='task/task-01')
)::text`).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	taskBefore := readTaskSnapshot()
	release := make(chan struct{})
	store := &firstFailureBarrierStore{
		RuntimeRecoveryStore: workerpostgres.NewRuntimeRecoveryStore(pool),
		arrived:              make(chan workerapplication.RuntimeFailureWithoutReplacementRecord, 2),
		release:              release,
	}
	observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapplication.RuntimeRecoveryService{
		Store: store, IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return base.Add(7 * time.Minute) },
	}, false)
	signal := workerpostgres.DetectedRuntimeFailure{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID, EvidenceAt: base.Add(6 * time.Minute),
		Inspection: &workerapplication.RuntimeInspection{Exists: true, ContainerID: "container-recovery-1", Generation: 1, ObservedAt: base.Add(6 * time.Minute)},
	}
	type observationResult struct {
		state    workerapplication.RuntimeRecoveryState
		recorded bool
		err      error
	}
	results := make(chan observationResult, 2)
	var callers sync.WaitGroup
	defer func() { cancel(); callers.Wait() }()
	for i := 0; i < 2; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			state, recorded, err := observer.OpenDetectedRuntimeFailure(ctx, signal)
			results <- observationResult{state: state, recorded: recorded, err: err}
		}()
	}
	var admitted [2]workerapplication.RuntimeFailureWithoutReplacementRecord
	for i := range admitted {
		select {
		case admitted[i] = <-store.arrived:
		case <-ctx.Done():
			t.Fatalf("both first observers did not reach the public store port: %v", ctx.Err())
		}
	}
	// Both observers have independently resolved the original revisions and
	// prepared distinct first-write identities. No store invocation has run yet.
	if admitted[0].IncidentID == admitted[1].IncidentID || admitted[0].EventID == admitted[1].EventID || admitted[0].Command.ExpectedSessionRevision != admitted[1].Command.ExpectedSessionRevision {
		t.Fatal("fixture did not admit two distinct first observations of the same revision")
	}
	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_recovery_incidents`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("first-write barrier already has an incident: count=%d err=%v", before, err)
	}
	close(release)
	var first workerapplication.RuntimeRecoveryState
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.err != nil || !result.recorded || result.state.Phase != domain.RecoveryExhausted || result.state.AttemptCount != 0 || result.state.ReplacementRuntimeInstanceID != "" {
				t.Fatalf("first observation failed: %+v", result)
			}
			if i == 0 {
				first = result.state
			} else if result.state.IncidentID != first.IncidentID || result.state.EventID != first.EventID {
				t.Fatalf("competing first observations diverged: first=%+v second=%+v", first, result.state)
			}
		case <-ctx.Done():
			t.Fatalf("competing first observations did not settle: %v", ctx.Err())
		}
	}
	if first.IncidentID == "" || first.EventID == "" {
		t.Fatal("first result omitted its durable identities")
	}
	var incidents, failures, replacements, runtimes int
	var generation, sessionGeneration uint64
	var incidentID, eventID, runtimeID string
	if err := pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM runtime_recovery_incidents),
 (SELECT count(*) FROM outbox_events WHERE event_type='RuntimeRecoveryFailed.v1'),
 (SELECT count(*) FROM runtime_replacement_intents),
 (SELECT count(*) FROM runtime_instances),
 incident.incident_id,event.event_id,runtime.runtime_instance_id,runtime.generation,session.runtime_generation
FROM runtime_recovery_incidents incident
JOIN outbox_events event ON event.correlation_id=incident.incident_id AND event.event_type='RuntimeRecoveryFailed.v1'
JOIN runtime_instances runtime ON runtime.runtime_instance_id=incident.failed_runtime_instance_id
JOIN worker_sessions session ON session.worker_session_id=incident.worker_session_id`).Scan(&incidents, &failures, &replacements, &runtimes, &incidentID, &eventID, &runtimeID, &generation, &sessionGeneration); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 || failures != 1 || replacements != 0 || runtimes != 1 || generation != 1 || sessionGeneration != 1 || runtimeID != started.RuntimeInstanceID || incidentID != first.IncidentID || eventID != first.EventID {
		t.Fatalf("first-write race created extra authority: incidents=%d failures=%d replacements=%d runtimes=%d runtime=%s generation=%d sessionGeneration=%d incident=%s event=%s", incidents, failures, replacements, runtimes, runtimeID, generation, sessionGeneration, incidentID, eventID)
	}
	if taskAfter := readTaskSnapshot(); taskAfter != taskBefore {
		t.Fatalf("Worker first-write race mutated Task-owned state: before=%s after=%s", taskBefore, taskAfter)
	}
}

// The barrier only coordinates entry to the public store port. Each admitted
// call executes the unchanged real PostgreSQL transaction after release.
type firstFailureBarrierStore struct {
	*workerpostgres.RuntimeRecoveryStore
	arrived chan workerapplication.RuntimeFailureWithoutReplacementRecord
	release <-chan struct{}
}

func (store *firstFailureBarrierStore) RecordRuntimeFailureWithoutReplacement(ctx context.Context, record workerapplication.RuntimeFailureWithoutReplacementRecord) (workerapplication.RuntimeRecoveryState, error) {
	select {
	case store.arrived <- record:
	case <-ctx.Done():
		return workerapplication.RuntimeRecoveryState{}, ctx.Err()
	}
	select {
	case <-store.release:
		return store.RuntimeRecoveryStore.RecordRuntimeFailureWithoutReplacement(ctx, record)
	case <-ctx.Done():
		return workerapplication.RuntimeRecoveryState{}, ctx.Err()
	}
}

func TestDisabledHardFailureAtomicOwnership(t *testing.T) {
	for _, scenario := range []string{"exit", "missing", "duplicate-concurrent", "outbox-rollback", "terminal-first", "deadline-first", "absolute-expired", "cancelled-task", "resource-exhausted", "old-epoch", "old-generation", "wrong-container", "alive", "older-evidence", "delivered-task", "active-incident"} {
		t.Run(scenario, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			started, active := startActiveRecoveryFixture(t, pool, base)
			journal, authority := bindAndClaimRuntimeControl(t, pool, started, base.Add(6*time.Minute))
			now := base.Add(7 * time.Minute)
			store := workerpostgres.NewRuntimeRecoveryStore(pool)
			recovery := workerapplication.RuntimeRecoveryService{Store: store, IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return now }}
			observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, recovery, false)
			exitCode := 1
			signal := workerpostgres.DetectedRuntimeFailure{WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit, EvidenceRef: "runtime-inspection/" + started.RuntimeInstanceID, EvidenceAt: now.Add(-time.Second), Inspection: &workerapplication.RuntimeInspection{Exists: true, ContainerID: "container-recovery-1", Generation: 1, ExitCode: &exitCode, ObservedAt: now.Add(-time.Second)}}
			wantFailure := scenario == "exit" || scenario == "missing" || scenario == "duplicate-concurrent" || scenario == "delivered-task"
			switch scenario {
			case "missing":
				signal.Trigger = domain.RecoveryTriggerContainerMissing
				signal.Inspection.Exists = false
				signal.Inspection.ContainerID = ""
				signal.Inspection.Generation = 0
			case "outbox-rollback":
				rejectRuntimeRecoveryOutbox(t, pool)
			case "terminal-first":
				envelope := runtimeControlEnvelope(started, "event", 1, "terminal-before-hard-exit", "terminal-before-hard-exit", "agent_turn.terminal")
				if _, err := journal.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{Authority: authority, Sequence: 1, MessageID: "terminal-before-hard-exit", IdempotencyKey: "terminal-before-hard-exit", Envelope: envelope}); err != nil {
					t.Fatal(err)
				}
			case "deadline-first":
				now = base.Add(25 * time.Hour)
			case "absolute-expired":
				now = base.Add(15 * 24 * time.Hour)
			case "cancelled-task":
				if _, err := pool.Exec(ctx, `UPDATE tasks SET status='cancelled' WHERE task_id='task-01'`); err != nil {
					t.Fatal(err)
				}
			case "resource-exhausted":
				if _, err := pool.Exec(ctx, `UPDATE agent_turns SET status='failed',terminal_at=$2,terminal_reason='resource_exhausted' WHERE agent_turn_id=$1`, started.AgentTurnID, base.Add(6*time.Minute)); err != nil {
					t.Fatal(err)
				}
			case "old-epoch":
				signal.ChannelEpoch = 2
			case "old-generation":
				signal.RuntimeGeneration = 2
			case "wrong-container":
				signal.Inspection.ContainerID = "other-container"
			case "alive":
				signal.Inspection.Running = true
			case "older-evidence":
				signal.EvidenceAt = base
				signal.Inspection.ObservedAt = base
			case "delivered-task":
				if _, err := pool.Exec(ctx, `UPDATE tasks SET status='delivered' WHERE task_id='task-01'`); err != nil {
					t.Fatal(err)
				}
			case "active-incident":
				if err := pool.QueryRow(ctx, `SELECT revision FROM runtime_instances WHERE runtime_instance_id=$1`, started.RuntimeInstanceID).Scan(&active.RuntimeRevision); err != nil {
					t.Fatal(err)
				}
				if _, err := recovery.Open(ctx, runtimeRecoveryOpenCommand(started, active, signal.EvidenceAt, domain.RecoveryTriggerContainerExit)); err != nil {
					t.Fatal(err)
				}
			}
			state, recorded, err := observer.OpenDetectedRuntimeFailure(ctx, signal)
			if wantFailure && (err != nil || !recorded || state.Phase != domain.RecoveryExhausted || state.AttemptCount != 0 || state.FenceStatus != domain.RecoveryFenceUnproven || state.ReplacementRuntimeInstanceID != "") {
				t.Fatalf("hard failure state=%+v recorded=%t err=%v", state, recorded, err)
			}
			if !wantFailure && recorded {
				t.Fatalf("ineligible observation recorded %+v", state)
			}
			if scenario == "outbox-rollback" && err == nil {
				t.Fatal("Outbox failure did not abort")
			}
			if scenario == "duplicate-concurrent" {
				var wg sync.WaitGroup
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						replay, recorded, err := observer.OpenDetectedRuntimeFailure(ctx, signal)
						if err != nil || !recorded || replay.IncidentID != state.IncidentID || replay.EventID != state.EventID {
							t.Errorf("duplicate failure=%+v recorded=%t err=%v", replay, recorded, err)
						}
					}()
				}
				wg.Wait()
			}
			terminal, found, err := workerpostgres.NewRuntimeRecoveryStore(pool).FindTerminalRuntimeFailureForTurn(ctx, "tenant-local", started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID, started.RuntimeInstanceID, 1, 1)
			if err != nil || found != wantFailure {
				t.Fatalf("durable exact reader found=%t err=%v", found, err)
			}
			if wantFailure && terminal.EventID != state.EventID {
				t.Fatal("restart lost failure event identity")
			}
			if _, found, err := store.FindTerminalRuntimeFailureForTurn(ctx, "tenant-local", started.WorkerSessionID, started.WorkerRunID, started.AgentTurnID, started.RuntimeInstanceID, 1, 2); err != nil || found {
				t.Fatalf("wrong epoch matched: %t %v", found, err)
			}
			var incidents, replacements, failures, settlements int
			var sessionStatus, runStatus, turnStatus, runtimeStatus, taskStatus, channelStatus string
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime_recovery_incidents),(SELECT count(*) FROM runtime_replacement_intents),(SELECT count(*) FROM outbox_events WHERE event_type='RuntimeRecoveryFailed.v1'),(SELECT count(*) FROM worker_turn_settlements),s.status,r.status,t.status,i.status,task.status,c.status FROM worker_sessions s JOIN worker_runs r ON r.worker_run_id=s.active_worker_run_id JOIN agent_turns t ON t.worker_run_id=r.worker_run_id JOIN runtime_instances i ON i.runtime_instance_id=s.active_runtime_instance_id JOIN tasks task ON task.task_id=s.bound_task_id JOIN runtime_control_channels c ON c.runtime_instance_id=i.runtime_instance_id WHERE s.worker_session_id=$1`, started.WorkerSessionID).Scan(&incidents, &replacements, &failures, &settlements, &sessionStatus, &runStatus, &turnStatus, &runtimeStatus, &taskStatus, &channelStatus); err != nil {
				t.Fatal(err)
			}
			if replacements != 0 {
				t.Fatal("replacement authority created")
			}
			if wantFailure {
				if incidents != 1 || failures != 1 || settlements != 0 || sessionStatus != "waiting_user" || runStatus != "waiting_user" || turnStatus != "failed" || runtimeStatus != "lost" || channelStatus != "fenced" {
					t.Fatalf("partial failure: incidents=%d failures=%d settlements=%d graph=%s/%s/%s/%s/%s", incidents, failures, settlements, sessionStatus, runStatus, turnStatus, runtimeStatus, channelStatus)
				}
				wantTask := "running"
				if scenario == "delivered-task" {
					wantTask = "delivered"
				}
				if taskStatus != wantTask {
					t.Fatalf("Worker changed Task: %s", taskStatus)
				}
				if _, err := journal.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{Authority: authority, Sequence: 1, MessageID: "late-after-hard-exit", IdempotencyKey: "late-after-hard-exit", Envelope: runtimeControlEnvelope(started, "event", 1, "late-after-hard-exit", "late-after-hard-exit", "agent_turn.terminal")}); err == nil {
					t.Fatal("fenced Runtime accepted late terminal authority")
				}
			} else if scenario == "active-incident" {
				if current, found, err := store.FindActiveRuntimeRecovery(ctx, "tenant-local"); err != nil || !found || current.Phase != domain.RecoveryOpen {
					t.Fatalf("disabled flag changed existing incident: %+v %t %v", current, found, err)
				}
			} else if incidents != 0 || failures != 0 || sessionStatus != "running" || runtimeStatus != "active" || channelStatus != "connected" {
				t.Fatalf("ineligible/rollback changed graph: incidents=%d failures=%d graph=%s/%s/%s", incidents, failures, sessionStatus, runtimeStatus, channelStatus)
			}
		})
	}
}

func TestDisabledIdleFailurePreservesHistoricalTurn(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "idle-expired"}[expired], func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			started, active := startActiveRecoveryFixture(t, pool, base)
			_, err := (workerapplication.RuntimeResidencyService{Store: workerpostgres.NewRuntimeResidencyStore(pool), Now: func() time.Time { return base.Add(6 * time.Minute) }}).CompleteStage(ctx, workerapplication.CompleteResidentStageCommand{TenantID: "tenant-local", WorkerSessionID: started.WorkerSessionID, WorkerRunID: started.WorkerRunID, AgentTurnID: started.AgentTurnID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ExpectedSessionRevision: active.SessionRevision, ExpectedRunRevision: active.RunRevision, ExpectedTurnRevision: active.TurnRevision, ExpectedRuntimeRevision: active.RuntimeRevision, SessionTarget: domain.SessionIdle, RunTarget: domain.RunCompleted, TurnTarget: domain.TurnCompleted, TerminalReason: "completed"})
			if err != nil {
				t.Fatal(err)
			}
			now := base.Add(7 * time.Minute)
			if expired {
				now = base.Add(49 * time.Hour)
			}
			observer := workerpostgres.NewGatedRuntimeFailureObserver(pool, workerapplication.RuntimeRecoveryService{Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: &concurrentAllocationIDs{}, Now: func() time.Time { return now }}, false)
			state, found, err := observer.OpenDetectedRuntimeFailure(ctx, workerpostgres.DetectedRuntimeFailure{WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerMissing, EvidenceRef: "runtime-inspection/idle", EvidenceAt: now, Inspection: &workerapplication.RuntimeInspection{ObservedAt: now}})
			if err != nil || found == expired {
				t.Fatalf("idle failure state=%+v found=%t err=%v", state, found, err)
			}
			if found && (state.WorkerRunID != "" || state.AgentTurnID != "" || state.Phase != domain.RecoveryExhausted) {
				t.Fatalf("idle failure invented active execution: %+v", state)
			}
			var turns, channels int
			var status string
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_turns),(SELECT count(*) FROM runtime_control_channels),status FROM agent_turns WHERE agent_turn_id=$1`, started.AgentTurnID).Scan(&turns, &channels, &status); err != nil || turns != 1 || channels != 0 || status != "completed" {
				t.Fatalf("idle failure changed history/fabricated channel: turns=%d channels=%d status=%s err=%v", turns, channels, status, err)
			}
		})
	}
}
