//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeHeartbeatPersistenceIsMonotonicAndReschedulesLiveness(t *testing.T) {
	pool := postgrestest.Open(t)
	now := time.Date(2026, 7, 26, 4, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, now)
	ids := &concurrentAllocationIDs{}
	service := workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids,
	}
	command := workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		ControlSequence: 7, ReceivedAt: now.Add(6 * time.Minute),
	}
	first, err := service.Observe(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.LivenessStatus != workerapplication.RuntimeLivenessHealthy ||
		first.ObservationRevision != 1 || first.RuntimeRevision != active.RuntimeRevision {
		t.Fatalf("first heartbeat=%+v", first)
	}

	duplicateCommand := command
	duplicateCommand.ControlSequence = 8
	duplicateCommand.ReceivedAt = command.ReceivedAt.Add(time.Second)
	duplicate, err := service.Observe(context.Background(), duplicateCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.Stale ||
		!duplicate.LastReceivedAt.Equal(command.ReceivedAt) ||
		duplicate.ObservationRevision != first.ObservationRevision {
		t.Fatalf("duplicate heartbeat=%+v", duplicate)
	}

	nextCommand := command
	nextCommand.HeartbeatSequence = 3
	nextCommand.ControlSequence = 9
	nextCommand.ReceivedAt = command.ReceivedAt.Add(15 * time.Second)
	next, err := service.Observe(context.Background(), nextCommand)
	if err != nil {
		t.Fatal(err)
	}
	if next.Duplicate || next.Stale || next.ObservationRevision != 2 ||
		!next.LastReceivedAt.Equal(nextCommand.ReceivedAt) {
		t.Fatalf("next heartbeat=%+v", next)
	}

	staleCommand := command
	staleCommand.HeartbeatSequence = 2
	staleCommand.ControlSequence = 10
	staleCommand.ReceivedAt = nextCommand.ReceivedAt.Add(time.Second)
	stale, err := service.Observe(context.Background(), staleCommand)
	if err != nil {
		t.Fatal(err)
	}
	if !stale.Stale || stale.Duplicate || stale.ObservationRevision != next.ObservationRevision ||
		!stale.LastReceivedAt.Equal(nextCommand.ReceivedAt) {
		t.Fatalf("stale heartbeat=%+v", stale)
	}

	var pending, cancelled int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*) FILTER (WHERE status='pending'),count(*) FILTER (WHERE status='cancelled')
FROM scheduled_commands
WHERE command_type='runtime_liveness_evaluate' AND target_id=$1`,
		started.RuntimeInstanceID).Scan(&pending, &cancelled); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || cancelled != 1 {
		t.Fatalf("liveness schedules pending=%d cancelled=%d", pending, cancelled)
	}
}

func TestRuntimeLivenessBoundariesFreshHeartbeatAndRecoveryOpen(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 5, 0, 0, 0, time.UTC)
	started, active := startActiveRecoveryFixture(t, pool, base)
	ids := &concurrentAllocationIDs{}
	heartbeatAt := base.Add(6 * time.Minute)
	heartbeat := workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids,
	}
	first, err := heartbeat.Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		ControlSequence: 1, ReceivedAt: heartbeatAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	evaluatedAt := heartbeatAt.Add(44*time.Second + 999*time.Millisecond)
	liveness := workerapplication.RuntimeLivenessService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids,
		Now: func() time.Time { return evaluatedAt },
	}
	early, err := liveness.Evaluate(ctx, workerapplication.EvaluateRuntimeLivenessCommand{
		TenantID: "tenant-local", RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		Stage:                       workerapplication.RuntimeLivenessStageSuspect,
		ExpectedObservationRevision: first.ObservationRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if early.Applied {
		t.Fatalf("44.999 second evaluation applied: %+v", early)
	}

	evaluatedAt = heartbeatAt.Add(45 * time.Second)
	suspect, err := liveness.Evaluate(ctx, workerapplication.EvaluateRuntimeLivenessCommand{
		TenantID: "tenant-local", RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		Stage:                       workerapplication.RuntimeLivenessStageSuspect,
		ExpectedObservationRevision: first.ObservationRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !suspect.Applied || suspect.LivenessStatus != workerapplication.RuntimeLivenessSuspect ||
		suspect.ObservationRevision != first.ObservationRevision+1 ||
		suspect.RuntimeRevision != active.RuntimeRevision+1 {
		t.Fatalf("45 second evaluation=%+v", suspect)
	}

	var unhealthyCommand platformpostgres.ScheduledCommand
	if err := pool.QueryRow(ctx, `
SELECT scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,
       payload,due_at,fencing_token
FROM scheduled_commands
WHERE command_type=$1 AND target_id=$2 AND status='pending'
ORDER BY due_at DESC LIMIT 1`,
		workerpostgres.CommandRuntimeLivenessEvaluate,
		started.RuntimeInstanceID,
	).Scan(
		&unhealthyCommand.ID, &unhealthyCommand.TenantID, &unhealthyCommand.CommandType,
		&unhealthyCommand.TargetType, &unhealthyCommand.TargetID,
		&unhealthyCommand.ExpectedRevision, &unhealthyCommand.Payload,
		&unhealthyCommand.DueAt, &unhealthyCommand.FencingToken,
	); err != nil {
		t.Fatal(err)
	}
	if !unhealthyCommand.DueAt.Equal(heartbeatAt.Add(90 * time.Second)) {
		t.Fatalf("unhealthy due_at=%s", unhealthyCommand.DueAt)
	}

	fresh, err := heartbeat.Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 2,
		ControlSequence: 2, ReceivedAt: heartbeatAt.Add(60 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluatedAt = heartbeatAt.Add(90 * time.Second)
	stale, err := liveness.Evaluate(ctx, workerapplication.EvaluateRuntimeLivenessCommand{
		TenantID: unhealthyCommand.TenantID, RuntimeInstanceID: unhealthyCommand.TargetID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		Stage:                       workerapplication.RuntimeLivenessStageUnhealthy,
		ExpectedObservationRevision: unhealthyCommand.ExpectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Applied || fresh.LivenessStatus != workerapplication.RuntimeLivenessHealthy {
		t.Fatalf("fresh heartbeat did not fence stale unhealthy check: fresh=%+v stale=%+v", fresh, stale)
	}
}

func TestRuntimeLivenessAtNinetySecondsOpensRecoveryWithoutEndingTurn(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	base := time.Date(2026, 7, 26, 6, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, base)
	ids := &concurrentAllocationIDs{}
	heartbeatAt := base.Add(6 * time.Minute)
	heartbeat := workerapplication.RuntimeHeartbeatService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids,
	}
	first, err := heartbeat.Observe(ctx, workerapplication.ObserveRuntimeHeartbeatCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		ControlSequence: 1, ReceivedAt: heartbeatAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluatedAt := heartbeatAt.Add(45 * time.Second)
	liveness := workerapplication.RuntimeLivenessService{
		Store: workerpostgres.NewRuntimeHeartbeatStore(pool), IDs: ids,
		Now: func() time.Time { return evaluatedAt },
	}
	if _, err := liveness.Evaluate(ctx, workerapplication.EvaluateRuntimeLivenessCommand{
		TenantID: "tenant-local", RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		Stage:                       workerapplication.RuntimeLivenessStageSuspect,
		ExpectedObservationRevision: first.ObservationRevision,
	}); err != nil {
		t.Fatal(err)
	}
	var command platformpostgres.ScheduledCommand
	if err := pool.QueryRow(ctx, `
SELECT scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,
       payload,due_at,fencing_token
FROM scheduled_commands
WHERE command_type=$1 AND target_id=$2 AND status='pending'
ORDER BY due_at DESC LIMIT 1`,
		workerpostgres.CommandRuntimeLivenessEvaluate,
		started.RuntimeInstanceID,
	).Scan(
		&command.ID, &command.TenantID, &command.CommandType, &command.TargetType,
		&command.TargetID, &command.ExpectedRevision, &command.Payload,
		&command.DueAt, &command.FencingToken,
	); err != nil {
		t.Fatal(err)
	}
	evaluatedAt = heartbeatAt.Add(90 * time.Second)
	handler := workerpostgres.RuntimeLivenessCommandHandler{
		Liveness: liveness,
		Recovery: workerapplication.RuntimeRecoveryService{
			Store: workerpostgres.NewRuntimeRecoveryStore(pool), IDs: ids,
			Now: func() time.Time { return evaluatedAt },
		},
	}
	if err := handler.HandleScheduledCommand(ctx, command); err != nil {
		t.Fatal(err)
	}
	var sessionStatus, runtimeStatus, runStatus, turnStatus, heartbeatStatus string
	if err := pool.QueryRow(ctx, `
SELECT session.status,runtime.status,run.status,turn.status,heartbeat.liveness_status
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.runtime_instance_id=$2
JOIN worker_runs run ON run.worker_run_id=$3
JOIN agent_turns turn ON turn.agent_turn_id=$4
JOIN runtime_heartbeat_observations heartbeat ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
WHERE session.worker_session_id=$1`,
		started.WorkerSessionID, started.RuntimeInstanceID,
		started.WorkerRunID, started.AgentTurnID,
	).Scan(&sessionStatus, &runtimeStatus, &runStatus, &turnStatus, &heartbeatStatus); err != nil {
		t.Fatal(err)
	}
	if sessionStatus != "recovering" || runtimeStatus != "unhealthy" ||
		runStatus != "running" || turnStatus != "delivered" || heartbeatStatus != "unhealthy" {
		t.Fatalf("session=%s runtime=%s run=%s turn=%s heartbeat=%s",
			sessionStatus, runtimeStatus, runStatus, turnStatus, heartbeatStatus)
	}
}
