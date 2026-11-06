package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

var ErrRuntimeLossConflict = errors.New("Runtime loss idempotency conflict")

type RuntimeLossStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewRuntimeLossStore(pool *pgxpool.Pool) *RuntimeLossStore {
	return &RuntimeLossStore{pool: pool}
}

func (store *RuntimeLossStore) RecordRuntimeLoss(
	ctx context.Context,
	record application.RuntimeLossRecord,
) (application.RuntimeLossState, error) {
	var result application.RuntimeLossState
	err := inTerminalCapacityTransaction(ctx, store.pool, func() string { return record.Command.AgentTurnID }, func(tx platformpostgres.Tx) error {
		if err := lockAgentTurnOutcome(
			ctx,
			tx,
			record.Command.AgentTurnID,
		); err != nil {
			return err
		}
		task, err := lockRuntimeTaskBeforeGraph(ctx, tx, record.Command.WorkerSessionID)
		if err != nil {
			return err
		}
		row, err := lockResidentRuntime(ctx, tx, record.Command.WorkerSessionID,
			record.Command.WorkerRunID, record.Command.AgentTurnID, record.Command.RuntimeInstanceID)
		if err != nil {
			return err
		}
		if err := task.revalidate(ctx, tx, record.Command.WorkerSessionID); err != nil {
			return err
		}
		taskID, taskStatus, taskRevision := task.TaskID, task.Status, task.Revision
		if row.RuntimeStatus == "lost" {
			var diagnostic runtimeLossDiagnostic
			if err := tx.QueryRow(ctx, `SELECT loss_diagnostic FROM runtime_instances
WHERE runtime_instance_id=$1`, record.Command.RuntimeInstanceID).Scan(&diagnostic); err != nil {
				return err
			}
			if diagnostic.IdempotencyKey != record.Command.IdempotencyKey {
				return ErrRuntimeLossConflict
			}
			result = runtimeLossState(row, taskStatus, taskRevision)
			return nil
		}
		if err := assertNoCompetingTurnOutcomeAuthority(
			ctx,
			tx,
			record.Command.AgentTurnID,
		); err != nil {
			return err
		}
		expectedStatus := record.ExpectedStatus
		if expectedStatus == "" {
			expectedStatus = "active"
		}
		if row.TenantID != record.Command.TenantID ||
			row.RuntimeGeneration != record.Command.RuntimeGeneration ||
			row.ContainerID != record.Command.ExpectedContainerID ||
			row.SessionRevision != record.Command.ExpectedSessionRevision ||
			row.RunRevision != record.Command.ExpectedRunRevision ||
			row.TurnRevision != record.Command.ExpectedTurnRevision ||
			row.RuntimeRevision != record.Command.ExpectedRuntimeRevision ||
			row.RuntimeStatus != expectedStatus {
			return platformpostgres.ErrRevisionConflict
		}
		if _, err := row.SessionStatus.Transition(domain.SessionLost); err != nil {
			return err
		}
		if !row.RunStatus.IsTerminal() {
			if _, err := row.RunStatus.Transition(domain.RunFailed); err != nil {
				return err
			}
		}
		if !row.TurnStatus.IsTerminal() {
			if _, err := row.TurnStatus.Transition(domain.TurnFailed); err != nil {
				return err
			}
		}
		diagnostic := runtimeLossDiagnostic{
			IdempotencyKey:     record.Command.IdempotencyKey,
			FenceStatus:        record.FenceStatus,
			Inspection:         record.Inspection,
			ProvenTraceRefs:    append([]string(nil), record.Command.ProvenTraceRefs...),
			ProvenArtifactRefs: append([]string(nil), record.Command.ProvenArtifactRefs...),
		}
		diagnosticJSON, err := json.Marshal(diagnostic)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
UPDATE session_dispatch_permits
SET status='revoked'
WHERE worker_session_id=$1 AND status='active'`, record.Command.WorkerSessionID); err != nil {
			return fmt.Errorf("revoke Runtime loss dispatch permits: %w", err)
		}
		var runtimeRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='lost',loss_diagnostic=$2,revision=revision+1,ended_at=$3
WHERE runtime_instance_id=$1 AND revision=$4 AND status=$5
RETURNING revision`, record.Command.RuntimeInstanceID, diagnosticJSON, record.Now,
			record.Command.ExpectedRuntimeRevision, expectedStatus).Scan(&runtimeRevision); err != nil {
			return fmt.Errorf("mark Runtime lost: %w", err)
		}
		turnRevision := row.TurnRevision
		if !row.TurnStatus.IsTerminal() {
			if err := tx.QueryRow(ctx, `
UPDATE agent_turns
SET status='failed',revision=revision+1,terminal_at=$2,terminal_reason='runtime_lost'
WHERE agent_turn_id=$1 AND revision=$3
RETURNING revision`, record.Command.AgentTurnID, record.Now, row.TurnRevision).Scan(&turnRevision); err != nil {
				return fmt.Errorf("fail Runtime loss AgentTurn: %w", err)
			}
			row.TurnStatus = domain.TurnFailed
		}
		runRevision := row.RunRevision
		if !row.RunStatus.IsTerminal() {
			if err := tx.QueryRow(ctx, `
UPDATE worker_runs
SET status='failed',revision=revision+1,terminal_at=$2,terminal_reason='runtime_lost'
WHERE worker_run_id=$1 AND revision=$3
RETURNING revision`, record.Command.WorkerRunID, record.Now, row.RunRevision).Scan(&runRevision); err != nil {
				return fmt.Errorf("fail Runtime loss WorkerRun: %w", err)
			}
			row.RunStatus = domain.RunFailed
		}
		var sessionRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status='lost',session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1 AND session_revision=$3
RETURNING session_revision`, record.Command.WorkerSessionID, record.Now,
			record.Command.ExpectedSessionRevision).Scan(&sessionRevision); err != nil {
			return fmt.Errorf("fence lost WorkerSession: %w", err)
		}
		if taskID != "" && taskStatus != "delivered" &&
			taskStatus != "completed" && taskStatus != "failed" && taskStatus != "cancelled" {
			var nextTaskRevision uint64
			if err := tx.QueryRow(ctx, `
UPDATE tasks
SET status='waiting_user',waiting_reason=$2,revision=revision+1,updated_at=$3
WHERE task_id=$1 AND revision=$4
RETURNING revision`, taskID, json.RawMessage(`{"code":"runtime_lost"}`),
				record.Now, taskRevision).Scan(&nextTaskRevision); err != nil {
				return fmt.Errorf("surface Runtime loss on Task: %w", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,
   from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,'waiting_user','runtime_lost',$7,$8)`,
				record.TaskTransitionID, taskID, record.Command.IdempotencyKey,
				taskRevision, nextTaskRevision, taskStatus,
				json.RawMessage(`{"code":"runtime_lost"}`), record.Now); err != nil {
				return fmt.Errorf("insert Runtime loss TaskTransition: %w", err)
			}
			taskStatus = "waiting_user"
			taskRevision = nextTaskRevision
		}
		if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands SET status='cancelled',updated_at=$2
WHERE target_type='AgentTurn' AND target_id=$1 AND status='pending'`,
			record.Command.AgentTurnID, record.Now); err != nil {
			return fmt.Errorf("cancel lost AgentTurn deadline: %w", err)
		}
		row.SessionStatus = domain.SessionLost
		row.SessionRevision = sessionRevision
		row.RunRevision = runRevision
		row.TurnRevision = turnRevision
		row.RuntimeStatus = "lost"
		row.RuntimeRevision = runtimeRevision
		if err := store.appendRuntimeLossEvent(ctx, tx, record, row); err != nil {
			return err
		}
		result = runtimeLossState(row, taskStatus, taskRevision)
		return nil
	})
	return result, err
}

type runtimeLossDiagnostic struct {
	IdempotencyKey     string                        `json:"idempotency_key"`
	FenceStatus        string                        `json:"fence_status"`
	Inspection         application.RuntimeInspection `json:"inspection"`
	ProvenTraceRefs    []string                      `json:"proven_trace_refs"`
	ProvenArtifactRefs []string                      `json:"proven_artifact_refs"`
}

func (diagnostic *runtimeLossDiagnostic) Scan(source any) error {
	bytes, ok := source.([]byte)
	if !ok {
		return fmt.Errorf("Runtime loss diagnostic has type %T", source)
	}
	return json.Unmarshal(bytes, diagnostic)
}

func runtimeLossState(
	row residentRuntimeRow,
	taskStatus string,
	taskRevision uint64,
) application.RuntimeLossState {
	return application.RuntimeLossState{
		WorkerSessionID: row.WorkerSessionID, WorkerRunID: row.WorkerRunID,
		AgentTurnID: row.AgentTurnID, RuntimeInstanceID: row.RuntimeInstanceID,
		RuntimeGeneration: row.RuntimeGeneration, SessionStatus: row.SessionStatus,
		RunStatus: row.RunStatus, TurnStatus: row.TurnStatus, RuntimeStatus: row.RuntimeStatus,
		TaskStatus: taskStatus, SessionRevision: row.SessionRevision,
		RunRevision: row.RunRevision, TurnRevision: row.TurnRevision,
		RuntimeRevision: row.RuntimeRevision, TaskRevision: taskRevision,
	}
}

func (store *RuntimeLossStore) appendRuntimeLossEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.RuntimeLossRecord,
	row residentRuntimeRow,
) error {
	stream := platformpostgres.OwnerStream{
		StreamID:     "worker-session/" + record.Command.WorkerSessionID,
		OwnerContext: "worker-runtime", AggregateType: "WorkerSession",
		AggregateID: record.Command.WorkerSessionID,
	}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		RuntimeInstanceID string `json:"runtime_instance_id"`
		WorkerRunID       string `json:"worker_run_id"`
		AgentTurnID       string `json:"agent_turn_id"`
		FenceStatus       string `json:"fence_status"`
	}{record.Command.RuntimeInstanceID, record.Command.WorkerRunID,
		record.Command.AgentTurnID, record.FenceStatus})
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.EventID, EventType: "RuntimeLost.v1",
		ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: record.Command.TenantID, AggregateType: "WorkerSession",
		AggregateID:   record.Command.WorkerSessionID,
		TraceID:       "worker-session/" + record.Command.WorkerSessionID,
		CorrelationID: record.Command.RuntimeInstanceID, CausationID: record.Command.WorkerRunID,
		IdempotencyKey: record.Command.IdempotencyKey,
		Visibility:     "internal", OccurredAt: record.Now, Payload: payload,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
		event.StreamID, event.Sequence, event.TenantID, event.AggregateType, event.AggregateID,
		event.TraceID, event.CorrelationID, event.CausationID, event.IdempotencyKey,
		event.Visibility, event.Payload, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("insert Runtime loss Outbox event: %w", err)
	}
	return nil
}

var _ application.RuntimeLossStore = (*RuntimeLossStore)(nil)
