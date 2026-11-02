package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type SessionFollowUpStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewSessionFollowUpStore(pool *pgxpool.Pool) *SessionFollowUpStore {
	return &SessionFollowUpStore{pool: pool}
}

func (store *SessionFollowUpStore) StartSessionFollowUp(ctx context.Context, record application.StartSessionFollowUpRecord) (application.StartedSessionFollowUp, error) {
	var result application.StartedSessionFollowUp
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		existing, found, err := readFollowUpResult(ctx, tx, record)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		var sessionTenant, sessionUser, sessionStatus, activeRuntimeID string
		var sessionRevision, runtimeGeneration uint64
		var boundTaskID, activeRunID *string
		err = tx.QueryRow(ctx, `
SELECT tenant_id,user_id,status,session_revision,runtime_generation,bound_task_id,
       active_worker_run_id,active_runtime_instance_id
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, record.Command.WorkerSessionID).Scan(
			&sessionTenant, &sessionUser, &sessionStatus, &sessionRevision, &runtimeGeneration,
			&boundTaskID, &activeRunID, &activeRuntimeID)
		if errors.Is(err, pgx.ErrNoRows) || sessionTenant != record.Command.TenantID || sessionUser != record.Command.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if sessionRevision != record.Command.ExpectedSessionRevision || runtimeGeneration != 1 ||
			(sessionStatus != "interactive" && sessionStatus != "idle") || boundTaskID != nil || activeRunID != nil {
			return platformpostgres.ErrRevisionConflict
		}
		var originTaskID, runStatus, profileID, profileRevision, profileDigest string
		var runRevision uint64
		err = tx.QueryRow(ctx, `
SELECT COALESCE(task_id,origin_task_id),status,revision,agent_profile_id,agent_profile_revision,agent_profile_digest
FROM worker_runs WHERE worker_run_id=$1 AND worker_session_id=$2 FOR UPDATE`,
			record.Command.PreviousWorkerRunID, record.Command.WorkerSessionID).Scan(
			&originTaskID, &runStatus, &runRevision, &profileID, &profileRevision, &profileDigest)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil || runRevision != record.Command.ExpectedRunRevision || !terminalRunStatus(runStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		var agentSessionID, turnStatus string
		var turnRevision uint64
		err = tx.QueryRow(ctx, `
SELECT agent_session_id,status,revision FROM agent_turns
WHERE agent_turn_id=$1 AND worker_run_id=$2 AND worker_session_id=$3 FOR UPDATE`,
			record.Command.PreviousAgentTurnID, record.Command.PreviousWorkerRunID,
			record.Command.WorkerSessionID).Scan(&agentSessionID, &turnStatus, &turnRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil || turnRevision != record.Command.ExpectedTurnRevision || !terminalTurnStatus(turnStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		var initiatorID, taskStatus string
		if err := tx.QueryRow(ctx, `SELECT initiator_user_id,status FROM tasks WHERE task_id=$1`, originTaskID).Scan(&initiatorID, &taskStatus); err != nil {
			return platformpostgres.ErrNotFound
		}
		if initiatorID != record.Command.UserID || !terminalTaskStatus(taskStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		var runtimeStatus string
		var runtimeRevision uint64
		err = tx.QueryRow(ctx, `
SELECT status,revision FROM runtime_instances
WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND generation=1 FOR UPDATE`,
			activeRuntimeID, record.Command.WorkerSessionID).Scan(&runtimeStatus, &runtimeRevision)
		if err != nil || runtimeStatus != "active" {
			return platformpostgres.ErrRevisionConflict
		}
		if err := application.RejectUnintegratedExecution(); err != nil {
			return err
		}
		startSnapshot, _ := json.Marshal(map[string]any{
			"continued_from_worker_run_id": record.Command.PreviousWorkerRunID,
			"resident_runtime_instance_id": activeRuntimeID,
		})
		if _, err := tx.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,origin_task_id,trigger_type,previous_worker_run_id,start_snapshot,
   agent_profile_id,agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at)
VALUES ($1,$2,$3,'direct_agent_turn',$4,$5,$6,$7,$8,'running',1,$9,$9)`,
			record.WorkerRunID, record.Command.WorkerSessionID, originTaskID,
			record.Command.PreviousWorkerRunID, startSnapshot, profileID, profileRevision,
			profileDigest, record.Now); err != nil {
			return fmt.Errorf("insert standalone follow-up WorkerRun: %w", err)
		}
		content, _ := json.Marshal(map[string]string{"text": record.Command.Content})
		if _, err := tx.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
   idempotency_key,created_at,started_at,deadline_at)
VALUES ($1,$2,$3,$4,'prompt',$5,'delivered',1,$6,$7,$7,$7::timestamptz + interval '24 hours')`,
			record.AgentTurnID, record.Command.WorkerSessionID, record.WorkerRunID,
			agentSessionID, content, record.Command.IdempotencyKey, record.Now); err != nil {
			return fmt.Errorf("insert standalone follow-up AgentTurn: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'agent_turn_deadline','AgentTurn',$3,1,'{}'::jsonb,
  $4::timestamptz + interval '24 hours','pending',$5,$4,$4)`, record.ScheduledCommandID,
			record.Command.TenantID, record.AgentTurnID, record.Now, "agent-turn-deadline/"+record.AgentTurnID); err != nil {
			return err
		}
		var nextRuntimeRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE runtime_instances SET worker_run_id=$2,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$3 AND status='active'
RETURNING revision`, activeRuntimeID, record.WorkerRunID, runtimeRevision).Scan(&nextRuntimeRevision)
		if err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		var nextSessionRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status='running',active_worker_run_id=$2,session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND session_revision=$4 AND active_worker_run_id IS NULL AND bound_task_id IS NULL
RETURNING session_revision`, record.Command.WorkerSessionID, record.WorkerRunID,
			record.Now, sessionRevision).Scan(&nextSessionRevision)
		if err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if err := refreshSessionIdleDeadline(ctx, tx, record.Command.WorkerSessionID, nextSessionRevision, record.Now); err != nil {
			return err
		}
		stream := platformpostgres.OwnerStream{
			StreamID:     "worker-session/" + record.Command.WorkerSessionID,
			OwnerContext: "worker-runtime", AggregateType: "WorkerSession",
			AggregateID: record.Command.WorkerSessionID,
		}
		sequence, err := store.streams.Next(ctx, tx, stream)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"worker_run_id": record.WorkerRunID, "agent_turn_id": record.AgentTurnID,
			"runtime_instance_id": activeRuntimeID, "session_revision": nextSessionRevision,
		})
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: record.EventID,
			EventType: "WorkerSessionFollowUpStarted.v1", ProducerContext: "worker-runtime",
			StreamID: stream.StreamID, Sequence: sequence, TenantID: record.Command.TenantID,
			AggregateType: "WorkerSession", AggregateID: record.Command.WorkerSessionID,
			TraceID:       "worker-session/" + record.Command.WorkerSessionID,
			CorrelationID: record.EventID, CausationID: record.Command.PreviousWorkerRunID,
			ClientCommandID: record.Command.IdempotencyKey,
			IdempotencyKey:  "session-follow-up/" + record.Command.IdempotencyKey,
			Visibility:      "internal", OccurredAt: record.Now, Payload: payload,
		}
		if err := event.Validate(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,client_command_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext, event.StreamID,
			event.Sequence, event.TenantID, event.AggregateType, event.AggregateID, event.TraceID,
			event.CorrelationID, event.CausationID, workerNullIfEmpty(event.ClientCommandID),
			event.IdempotencyKey, event.Visibility,
			event.Payload, event.OccurredAt); err != nil {
			return err
		}
		result = application.StartedSessionFollowUp{
			WorkerSessionID: record.Command.WorkerSessionID, WorkerRunID: record.WorkerRunID,
			AgentTurnID: record.AgentTurnID, RuntimeInstanceID: activeRuntimeID,
			SessionRevision: nextSessionRevision, RunRevision: 1, TurnRevision: 1,
			RuntimeRevision: nextRuntimeRevision, EventID: record.EventID,
		}
		response, _ := json.Marshal(result)
		if _, err := tx.Exec(ctx, `
INSERT INTO idempotency_results
  (tenant_id,actor_user_id,operation,idempotency_key,request_digest,resource_type,resource_id,
   response_status,response_body,created_at)
VALUES ($1,$2,'session_follow_up',$3,$4,'WorkerRun',$5,202,$6,$7)`, record.Command.TenantID,
			record.Command.UserID, record.Command.IdempotencyKey, record.RequestDigest,
			record.WorkerRunID, response, record.Now); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func readFollowUpResult(ctx context.Context, tx platformpostgres.Tx, record application.StartSessionFollowUpRecord) (application.StartedSessionFollowUp, bool, error) {
	var digest string
	var response []byte
	err := tx.QueryRow(ctx, `
SELECT request_digest,response_body FROM idempotency_results
WHERE tenant_id=$1 AND actor_user_id=$2 AND operation='session_follow_up' AND idempotency_key=$3`,
		record.Command.TenantID, record.Command.UserID, record.Command.IdempotencyKey).Scan(&digest, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StartedSessionFollowUp{}, false, nil
	}
	if err != nil {
		return application.StartedSessionFollowUp{}, false, err
	}
	if digest != record.RequestDigest {
		return application.StartedSessionFollowUp{}, false, fmt.Errorf("follow-up idempotency content conflict")
	}
	var result application.StartedSessionFollowUp
	if err := json.Unmarshal(response, &result); err != nil {
		return application.StartedSessionFollowUp{}, false, err
	}
	return result, true, nil
}

func terminalRunStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timed_out"
}

func terminalTurnStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timed_out"
}

func terminalTaskStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

var _ application.SessionFollowUpStore = (*SessionFollowUpStore)(nil)
