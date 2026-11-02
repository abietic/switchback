package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type QueuedTurnStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewQueuedTurnStore(pool *pgxpool.Pool) *QueuedTurnStore {
	return &QueuedTurnStore{pool: pool}
}

func (store *QueuedTurnStore) ContinueQueuedTurn(
	ctx context.Context,
	record application.ContinueQueuedTurnRecord,
) (application.ContinuedQueuedTurn, error) {
	var result application.ContinuedQueuedTurn
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var err error
		if err = lockQueuedTurnTask(ctx, tx, record.Command); err != nil {
			return err
		}
		result, err = store.continueQueuedTurnTx(ctx, tx, record)
		return err
	})
	return result, err
}

func lockQueuedTurnTask(ctx context.Context, tx platformpostgres.Tx, command application.ContinueQueuedTurnCommand) error {
	var taskID string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(task_id,'') FROM worker_runs WHERE worker_run_id=$1 AND worker_session_id=$2`, command.PreviousWorkerRunID, command.WorkerSessionID).Scan(&taskID); err != nil {
		return err
	}
	if taskID != "" {
		if err := tx.QueryRow(ctx, `SELECT task_id FROM tasks WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&taskID); err != nil {
			return err
		}
	}
	return nil
}

func (store *QueuedTurnStore) continueQueuedTurnTx(ctx context.Context, tx platformpostgres.Tx, record application.ContinueQueuedTurnRecord) (result application.ContinuedQueuedTurn, err error) {
	err = func() error {
		var commandTenant, commandUser, sessionID, previousRunID, previousTurnID, content, commandStatus string
		var queueSequence uint64
		err := tx.QueryRow(ctx, `
SELECT tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,content,status,queue_sequence
FROM worker_user_commands WHERE command_id=$1 AND command_type='queue' FOR UPDATE`,
			record.Command.QueueCommandID).Scan(&commandTenant, &commandUser, &sessionID,
			&previousRunID, &previousTurnID, &content, &commandStatus, &queueSequence)
		if errors.Is(err, pgx.ErrNoRows) || commandTenant != record.Command.TenantID ||
			commandUser != record.Command.UserID || sessionID != record.Command.WorkerSessionID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if commandStatus == "applied" {
			var runID, turnID, runtimeID, eventID string
			var runtimeGeneration, sessionRevision, runRevision, turnRevision, runtimeRevision uint64
			err := tx.QueryRow(ctx, `
SELECT outcome->>'worker_run_id',outcome->>'agent_turn_id',outcome->>'runtime_instance_id',
       COALESCE((outcome->>'runtime_generation')::bigint,runtime.generation),
       (outcome->>'session_revision')::bigint,(outcome->>'run_revision')::bigint,
       (outcome->>'turn_revision')::bigint,(outcome->>'runtime_revision')::bigint,
       COALESCE(outcome->>'event_id','')
FROM worker_user_commands user_command
JOIN runtime_instances runtime
  ON runtime.runtime_instance_id=user_command.outcome->>'runtime_instance_id'
WHERE user_command.command_id=$1`, record.Command.QueueCommandID).Scan(
				&runID, &turnID, &runtimeID, &runtimeGeneration, &sessionRevision,
				&runRevision, &turnRevision, &runtimeRevision, &eventID)
			if err != nil {
				return err
			}
			result = application.ContinuedQueuedTurn{
				WorkerSessionID: sessionID, WorkerRunID: runID, AgentTurnID: turnID,
				RuntimeInstanceID: runtimeID, RuntimeGeneration: runtimeGeneration,
				SessionRevision: sessionRevision,
				RunRevision:     runRevision, TurnRevision: turnRevision,
				RuntimeRevision: runtimeRevision, EventID: eventID,
			}
			return nil
		}
		if commandStatus != "accepted" || previousRunID != record.Command.PreviousWorkerRunID ||
			previousTurnID != record.Command.PreviousAgentTurnID {
			return platformpostgres.ErrRevisionConflict
		}
		var tenantID, userID, sessionStatus, boundTaskID, activeRuntimeID string
		var activeRunID *string
		var sessionRevision, sessionRuntimeGeneration uint64
		err = tx.QueryRow(ctx, `
SELECT tenant_id,user_id,status,COALESCE(bound_task_id,''),active_worker_run_id,
       active_runtime_instance_id,session_revision,runtime_generation
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, sessionID).Scan(
			&tenantID, &userID, &sessionStatus, &boundTaskID, &activeRunID,
			&activeRuntimeID, &sessionRevision, &sessionRuntimeGeneration)
		if err != nil || tenantID != record.Command.TenantID || userID != record.Command.UserID ||
			sessionRevision != record.Command.ExpectedSessionRevision || activeRunID != nil ||
			(sessionStatus != "interactive" && sessionStatus != "waiting_user") ||
			activeRuntimeID != record.Command.RuntimeInstanceID ||
			sessionRuntimeGeneration != record.Command.RuntimeGeneration {
			return platformpostgres.ErrRevisionConflict
		}
		var taskID, originTaskID, runStatus, profileID, profileRevision, profileDigest string
		var runRevision uint64
		if err := tx.QueryRow(ctx, `
SELECT COALESCE(task_id,''),COALESCE(origin_task_id,''),status,revision,
       agent_profile_id,agent_profile_revision,agent_profile_digest
FROM worker_runs WHERE worker_run_id=$1 AND worker_session_id=$2 FOR UPDATE`, previousRunID, sessionID).Scan(
			&taskID, &originTaskID, &runStatus, &runRevision, &profileID, &profileRevision, &profileDigest); err != nil {
			return platformpostgres.ErrNotFound
		}
		if runRevision != record.Command.ExpectedRunRevision || !terminalRunStatus(runStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		var agentSessionID, turnStatus string
		var turnRevision uint64
		if err := tx.QueryRow(ctx, `
SELECT agent_session_id,status,revision FROM agent_turns
WHERE agent_turn_id=$1 AND worker_run_id=$2 FOR UPDATE`, previousTurnID, previousRunID).Scan(
			&agentSessionID, &turnStatus, &turnRevision); err != nil {
			return platformpostgres.ErrNotFound
		}
		if turnRevision != record.Command.ExpectedTurnRevision || !terminalTurnStatus(turnStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		var runtimeStatus string
		var runtimeRevision uint64
		if err := tx.QueryRow(ctx, `
SELECT status,revision FROM runtime_instances
WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND generation=$3 AND worker_run_id=$4
FOR UPDATE`, activeRuntimeID, sessionID, record.Command.RuntimeGeneration, previousRunID).Scan(
			&runtimeStatus, &runtimeRevision,
		); err != nil ||
			runtimeStatus != "active" || runtimeRevision != record.Command.ExpectedRuntimeRevision {
			return platformpostgres.ErrRevisionConflict
		}
		triggerType := "direct_agent_turn"
		var newTaskID any
		var newOriginTaskID any
		if taskID != "" {
			triggerType = "task_rework"
			newTaskID = taskID
			if boundTaskID != taskID {
				return platformpostgres.ErrRevisionConflict
			}
			var taskStatus, initiatorID, currentRunID string
			if err := tx.QueryRow(ctx, `SELECT status,initiator_user_id,COALESCE(current_worker_run_id,'')
FROM tasks WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&taskStatus, &initiatorID, &currentRunID); err != nil ||
				taskStatus != "running" || initiatorID != record.Command.UserID || currentRunID != previousRunID {
				return platformpostgres.ErrRevisionConflict
			}
		} else {
			newOriginTaskID = originTaskID
			if boundTaskID != "" {
				return platformpostgres.ErrRevisionConflict
			}
		}
		startSnapshot, _ := json.Marshal(map[string]any{
			"queued_after_worker_run_id":   previousRunID,
			"queue_command_id":             record.Command.QueueCommandID,
			"queue_sequence":               queueSequence,
			"resident_runtime_instance_id": activeRuntimeID,
			"runtime_generation":           record.Command.RuntimeGeneration,
		})
		if _, err := tx.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,origin_task_id,trigger_type,previous_worker_run_id,
   start_snapshot,agent_profile_id,agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'running',1,$11,$11)`, record.WorkerRunID,
			sessionID, newTaskID, newOriginTaskID, triggerType, previousRunID, startSnapshot,
			profileID, profileRevision, profileDigest, record.Now); err != nil {
			return err
		}
		turnContent, _ := json.Marshal(map[string]string{"text": content})
		if _, err := tx.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
   idempotency_key,created_at,started_at,deadline_at)
VALUES ($1,$2,$3,$4,'queue',$5,'delivered',1,$6,$7,$7,$7::timestamptz + interval '24 hours')`,
			record.AgentTurnID, sessionID, record.WorkerRunID, agentSessionID, turnContent,
			"queued-turn/"+record.Command.QueueCommandID, record.Now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'agent_turn_deadline','AgentTurn',$3,1,'{}'::jsonb,
  $4::timestamptz + interval '24 hours','pending',$5,$4,$4)`, record.ScheduledCommandID,
			record.Command.TenantID, record.AgentTurnID, record.Now,
			"agent-turn-deadline/"+record.AgentTurnID); err != nil {
			return err
		}
		if taskID != "" {
			if _, err := tx.Exec(ctx, `UPDATE tasks SET current_worker_run_id=$2,revision=revision+1,updated_at=$3
WHERE task_id=$1 AND status='running' AND current_worker_run_id=$4`, taskID, record.WorkerRunID,
				record.Now, previousRunID); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `UPDATE runtime_instances SET worker_run_id=$2,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$3 AND status='active' AND generation=$4
  AND worker_run_id=$5
RETURNING revision`, activeRuntimeID, record.WorkerRunID, runtimeRevision,
			record.Command.RuntimeGeneration, previousRunID).Scan(&runtimeRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if err := tx.QueryRow(ctx, `
UPDATE worker_sessions SET status='running',active_worker_run_id=$2,
  session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND session_revision=$4 AND active_worker_run_id IS NULL
RETURNING session_revision`, sessionID, record.WorkerRunID, record.Now, sessionRevision).Scan(&sessionRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		outcome, _ := json.Marshal(map[string]any{
			"worker_run_id": record.WorkerRunID, "agent_turn_id": record.AgentTurnID,
			"runtime_instance_id": activeRuntimeID,
			"runtime_generation":  record.Command.RuntimeGeneration,
			"session_revision":    sessionRevision,
			"run_revision":        1, "turn_revision": 1, "runtime_revision": runtimeRevision,
			"event_id": record.EventID,
		})
		if _, err := tx.Exec(ctx, `UPDATE worker_user_commands
SET status='applied',outcome=outcome || $2::jsonb,updated_at=$3
WHERE command_id=$1 AND status='accepted'`, record.Command.QueueCommandID, outcome, record.Now); err != nil {
			return err
		}
		stream := platformpostgres.OwnerStream{StreamID: "worker-session/" + sessionID,
			OwnerContext: "worker-runtime", AggregateType: "WorkerSession", AggregateID: sessionID}
		sequence, err := store.streams.Next(ctx, tx, stream)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"worker_run_id": record.WorkerRunID,
			"agent_turn_id": record.AgentTurnID, "queue_command_id": record.Command.QueueCommandID,
			"queue_sequence": queueSequence, "runtime_generation": record.Command.RuntimeGeneration})
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: record.EventID,
			EventType: "WorkerQueuedTurnStarted.v1", ProducerContext: "worker-runtime",
			StreamID: stream.StreamID, Sequence: sequence, TenantID: tenantID,
			AggregateType: "WorkerSession", AggregateID: sessionID,
			TraceID: "worker-session/" + sessionID, CorrelationID: record.Command.QueueCommandID,
			CausationID: previousTurnID, IdempotencyKey: "queued-turn/" + record.Command.QueueCommandID,
			Visibility: "user", OccurredAt: record.Now, Payload: payload,
		}
		if err := event.Validate(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, event.EventID,
			event.SchemaVersion, event.EventType, event.ProducerContext, event.StreamID, event.Sequence,
			event.TenantID, event.AggregateType, event.AggregateID, event.TraceID, event.CorrelationID,
			event.CausationID, event.IdempotencyKey, event.Visibility, event.Payload, event.OccurredAt); err != nil {
			return err
		}
		result = application.ContinuedQueuedTurn{
			WorkerSessionID: sessionID, WorkerRunID: record.WorkerRunID,
			AgentTurnID: record.AgentTurnID, RuntimeInstanceID: activeRuntimeID,
			RuntimeGeneration: record.Command.RuntimeGeneration,
			SessionRevision:   sessionRevision, RunRevision: 1, TurnRevision: 1,
			RuntimeRevision: runtimeRevision, EventID: record.EventID,
		}
		return nil
	}()
	return result, err
}

var _ application.QueuedTurnStore = (*QueuedTurnStore)(nil)
