package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

func (store *Store) ReworkTask(ctx context.Context, record application.ReworkTaskRecord) (application.ReworkedTask, error) {
	continued, err := store.continueTaskWithWorker(ctx, application.ContinueTaskWithWorkerRecord{
		Command: application.ContinueTaskWithWorkerCommand{
			Actor: record.Command.Actor, TaskID: record.Command.TaskID,
			ExpectedTaskRevision: record.Command.ExpectedTaskRevision,
			InteractionKind:      application.TaskContinuationRework,
			Message:              record.Command.Message, IdempotencyKey: record.Command.IdempotencyKey,
		},
		WorkerRunID: record.WorkerRunID, AgentTurnID: record.AgentTurnID,
		ScheduledCommandID: record.ScheduledCommandID, TransitionID: record.TransitionID,
		EventID: record.EventID, RequestDigest: record.RequestDigest, Now: record.Now,
	}, false)
	if err != nil {
		return application.ReworkedTask{}, err
	}
	return application.ReworkedTask{
		TaskID: continued.TaskID, Status: continued.Status, Revision: continued.Revision,
		WorkerSessionID: continued.WorkerSessionID, WorkerRunID: continued.WorkerRunID,
		AgentTurnID: continued.AgentTurnID, EventID: continued.EventID,
	}, nil
}

func (store *Store) ContinueTaskWithWorker(
	ctx context.Context,
	record application.ContinueTaskWithWorkerRecord,
) (application.ContinuedTask, error) {
	return store.continueTaskWithWorker(ctx, record, true)
}

func (store *Store) continueTaskWithWorker(
	ctx context.Context,
	record application.ContinueTaskWithWorkerRecord,
	requireExactTarget bool,
) (application.ContinuedTask, error) {
	var result application.ContinuedTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		if existing, found, err := readContinuationIdempotencyResult(
			ctx, tx, record.Command, record.RequestDigest,
		); err != nil {
			return err
		} else if found {
			result = existing
			return nil
		}
		var tenantID, initiatorID, status, sessionID, previousRunID string
		var taskRevision uint64
		err := tx.QueryRow(ctx, `SELECT tenant_id,initiator_user_id,status,
COALESCE(worker_session_id,''),COALESCE(current_worker_run_id,''),revision
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&tenantID, &initiatorID, &status, &sessionID, &previousRunID, &taskRevision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.Actor.TenantID ||
			initiatorID != record.Command.Actor.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != string(domain.TaskDelivered) ||
			taskRevision != record.Command.ExpectedTaskRevision ||
			sessionID == "" || previousRunID == "" {
			return platformpostgres.ErrRevisionConflict
		}
		if requireExactTarget && (sessionID != record.Command.WorkerSessionID ||
			previousRunID != record.Command.PreviousWorkerRunID) {
			return platformpostgres.ErrRevisionConflict
		}
		var sessionStatus, boundTaskID, runtimeID string
		var activeRunID *string
		var sessionRevision uint64
		if err := tx.QueryRow(ctx, `SELECT status,COALESCE(bound_task_id,''),active_worker_run_id,
active_runtime_instance_id,session_revision FROM worker_sessions
WHERE worker_session_id=$1 AND tenant_id=$2 AND user_id=$3 FOR UPDATE`, sessionID, tenantID,
			initiatorID).Scan(&sessionStatus, &boundTaskID, &activeRunID, &runtimeID, &sessionRevision); err != nil ||
			boundTaskID != record.Command.TaskID || activeRunID != nil ||
			(sessionStatus != "interactive" && sessionStatus != "waiting_user" && sessionStatus != "idle") {
			return platformpostgres.ErrRevisionConflict
		}
		if requireExactTarget && sessionRevision != record.Command.ExpectedSessionRevision {
			return platformpostgres.ErrRevisionConflict
		}
		plan, err := domain.PlanSessionFollowUp(
			record.Command.TaskID, domain.TaskStatus(status), initiatorID == record.Command.Actor.UserID, true,
		)
		if err != nil || plan.Kind != domain.FollowUpTaskRun || !plan.MutateTask ||
			!plan.RequiresNewWorkerRun || plan.TargetTaskStatus != domain.TaskRunning {
			return platformpostgres.ErrRevisionConflict
		}
		var runStatus, profileID, profileRevision, profileDigest string
		var runRevision uint64
		if err := tx.QueryRow(ctx, `SELECT status,revision,agent_profile_id,agent_profile_revision,
agent_profile_digest FROM worker_runs WHERE worker_run_id=$1 AND worker_session_id=$2 FOR UPDATE`,
			previousRunID, sessionID).Scan(&runStatus, &runRevision, &profileID, &profileRevision, &profileDigest); err != nil ||
			!terminalRun(runStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		if requireExactTarget && runRevision != record.Command.ExpectedRunRevision {
			return platformpostgres.ErrRevisionConflict
		}
		var previousTurnID, agentSessionID, turnStatus string
		var turnRevision uint64
		if err := tx.QueryRow(ctx, `SELECT agent_turn_id,agent_session_id,status,revision FROM agent_turns
WHERE worker_run_id=$1 ORDER BY created_at DESC,agent_turn_id DESC LIMIT 1 FOR UPDATE`, previousRunID).Scan(
			&previousTurnID, &agentSessionID, &turnStatus, &turnRevision); err != nil || !terminalTurn(turnStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		if requireExactTarget && (previousTurnID != record.Command.PreviousAgentTurnID ||
			turnRevision != record.Command.ExpectedTurnRevision) {
			return platformpostgres.ErrRevisionConflict
		}
		var runtimeStatus string
		var runtimeRevision uint64
		if err := tx.QueryRow(ctx, `SELECT status,revision FROM runtime_instances
WHERE runtime_instance_id=$1 AND worker_session_id=$2 FOR UPDATE`, runtimeID, sessionID).Scan(
			&runtimeStatus, &runtimeRevision); err != nil || runtimeStatus != "active" {
			return platformpostgres.ErrRevisionConflict
		}
		startSnapshot, _ := json.Marshal(map[string]any{
			"continued_from_worker_run_id": previousRunID,
			"resident_runtime_instance_id": runtimeID,
			"interaction_kind":             record.Command.InteractionKind,
		})
		if _, err := tx.Exec(ctx, `INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,previous_worker_run_id,start_snapshot,
   agent_profile_id,agent_profile_revision,agent_profile_digest,status,revision,created_at,started_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'running',1,$10,$10)`, record.WorkerRunID,
			sessionID, record.Command.TaskID, plan.TriggerType, previousRunID, startSnapshot,
			profileID, profileRevision, profileDigest, record.Now); err != nil {
			return err
		}
		content, _ := json.Marshal(map[string]any{
			"text": record.Command.Message, "interaction_kind": record.Command.InteractionKind,
		})
		if _, err := tx.Exec(ctx, `INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,revision,
   idempotency_key,created_at,started_at,deadline_at)
VALUES ($1,$2,$3,$4,'prompt',$5,'delivered',1,$6,$7,$7,$7::timestamptz + interval '24 hours')`,
			record.AgentTurnID, sessionID, record.WorkerRunID, agentSessionID, content,
			record.Command.IdempotencyKey, record.Now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'agent_turn_deadline','AgentTurn',$3,1,'{}'::jsonb,
  $4::timestamptz + interval '24 hours','pending',$5,$4,$4)`, record.ScheduledCommandID,
			tenantID, record.AgentTurnID, record.Now, "agent-turn-deadline/"+record.AgentTurnID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE runtime_instances SET worker_run_id=$2,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$3 AND status='active' RETURNING revision`, runtimeID,
			record.WorkerRunID, runtimeRevision).Scan(&runtimeRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if err := tx.QueryRow(ctx, `UPDATE worker_sessions SET status='running',active_worker_run_id=$2,
session_revision=session_revision+1,updated_at=$3 WHERE worker_session_id=$1 AND session_revision=$4
AND active_worker_run_id IS NULL RETURNING session_revision`, sessionID, record.WorkerRunID,
			record.Now, sessionRevision).Scan(&sessionRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if err := refreshSessionIdleDeadline(ctx, tx, sessionID, sessionRevision, record.Now); err != nil {
			return err
		}
		var nextTaskRevision uint64
		if err := tx.QueryRow(ctx, `UPDATE tasks SET status=$2,current_worker_run_id=$3,
waiting_reason=NULL,revision=revision+1,updated_at=$4 WHERE task_id=$1 AND revision=$5
AND status='delivered' RETURNING revision`, record.Command.TaskID, plan.TargetTaskStatus,
			record.WorkerRunID, record.Now, taskRevision).Scan(&nextTaskRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		transitionResult, _ := json.Marshal(map[string]any{"worker_session_id": sessionID,
			"worker_run_id": record.WorkerRunID, "agent_turn_id": record.AgentTurnID,
			"interaction_kind": record.Command.InteractionKind, "event_id": record.EventID})
		transitionTrigger := "initiator_chat"
		eventType := "TaskContinuationStarted.v1"
		if record.Command.InteractionKind == application.TaskContinuationRework {
			transitionTrigger = "initiator_rework"
			eventType = "TaskReworkStarted.v1"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,from_state,to_state,
   trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,'delivered','running',$6,$7,$8)`, record.TransitionID,
			record.Command.TaskID, record.Command.IdempotencyKey, taskRevision, nextTaskRevision,
			transitionTrigger, transitionResult, record.Now); err != nil {
			return err
		}
		if err := store.appendTaskActionEvent(ctx, tx, record.EventID, tenantID, record.Command.TaskID,
			eventType, record.Command.IdempotencyKey, record.Command.IdempotencyKey, nextTaskRevision,
			map[string]any{
				"worker_session_id": sessionID, "session_revision": sessionRevision,
				"worker_run_id": record.WorkerRunID, "agent_turn_id": record.AgentTurnID,
				"interaction_kind": record.Command.InteractionKind,
			}, record.Now); err != nil {
			return err
		}
		result = application.ContinuedTask{
			TaskID: record.Command.TaskID, Status: string(plan.TargetTaskStatus),
			Revision: nextTaskRevision, WorkerSessionID: sessionID, SessionRevision: sessionRevision,
			WorkerRunID: record.WorkerRunID, AgentTurnID: record.AgentTurnID,
			InteractionKind: record.Command.InteractionKind, EventID: record.EventID,
		}
		response, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO idempotency_results
  (tenant_id,actor_user_id,operation,idempotency_key,request_digest,resource_type,resource_id,
   response_status,response_body,created_at,expires_at)
VALUES ($1,$2,'continue_task_with_worker',$3,$4,'Task',$5,202,$6,$7,$7::timestamptz + interval '14 days')`,
			tenantID, initiatorID, record.Command.IdempotencyKey, record.RequestDigest,
			record.Command.TaskID, response, record.Now); err != nil {
			return fmt.Errorf("insert Task continuation idempotency result: %w", err)
		}
		return nil
	})
	return result, err
}

func readContinuationIdempotencyResult(
	ctx context.Context,
	tx platformpostgres.Tx,
	command application.ContinueTaskWithWorkerCommand,
	requestDigest string,
) (application.ContinuedTask, bool, error) {
	var storedDigest string
	var payload json.RawMessage
	err := tx.QueryRow(ctx, `SELECT request_digest,response_body FROM idempotency_results
WHERE tenant_id=$1 AND actor_user_id=$2 AND operation='continue_task_with_worker'
  AND idempotency_key=$3`,
		command.Actor.TenantID, command.Actor.UserID, command.IdempotencyKey,
	).Scan(&storedDigest, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ContinuedTask{}, false, nil
	}
	if err != nil {
		return application.ContinuedTask{}, false, err
	}
	if storedDigest != requestDigest {
		return application.ContinuedTask{}, false, application.ErrIdempotencyConflict
	}
	var result application.ContinuedTask
	if err := json.Unmarshal(payload, &result); err != nil || result.TaskID != command.TaskID {
		return application.ContinuedTask{}, false, platformpostgres.ErrRevisionConflict
	}
	return result, true, nil
}

func (store *Store) CancelTask(ctx context.Context, record application.CancelTaskRecord) (application.CancelledTask, error) {
	var result application.CancelledTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var existingStatus string
		err := tx.QueryRow(ctx, `SELECT to_state,result_revision,transition_id FROM task_transitions
WHERE task_id=$1 AND idempotency_key=$2`, record.Command.TaskID, record.Command.IdempotencyKey).Scan(
			&existingStatus, &result.Revision, &result.EventID)
		if err == nil {
			result.TaskID, result.Status = record.Command.TaskID, existingStatus
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var tenantID, initiatorID, status, sessionID, runID string
		var revision uint64
		err = tx.QueryRow(ctx, `SELECT tenant_id,initiator_user_id,status,COALESCE(worker_session_id,''),
COALESCE(current_worker_run_id,''),revision FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&tenantID, &initiatorID, &status, &sessionID, &runID, &revision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.Actor.TenantID ||
			initiatorID != record.Command.Actor.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if revision != record.Command.ExpectedTaskRevision || status == "completed" ||
			status == "failed" || status == "cancelled" {
			return platformpostgres.ErrRevisionConflict
		}
		var nextRevision uint64
		if err := tx.QueryRow(ctx, `UPDATE tasks SET status='cancelled',waiting_reason=NULL,
revision=revision+1,updated_at=$2 WHERE task_id=$1 AND revision=$3 RETURNING revision`,
			record.Command.TaskID, record.Now, revision).Scan(&nextRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE session_allocation_requests SET status='cancelled',updated_at=$2
WHERE request_kind='task' AND owner_id=$1 AND status='pending'`, record.Command.TaskID, record.Now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session_task_queue_entries SET status='cancelled'
WHERE task_id=$1 AND status IN ('pending','permit_issued')`, record.Command.TaskID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session_dispatch_permits SET status='revoked'
WHERE target_kind='task' AND target_id=$1 AND status='active'`, record.Command.TaskID); err != nil {
			return err
		}
		if sessionID != "" {
			var sessionRevision uint64
			var activeRunID *string
			var sessionStatus string
			if err := tx.QueryRow(ctx, `SELECT session_revision,active_worker_run_id,status
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, sessionID).Scan(
				&sessionRevision, &activeRunID, &sessionStatus); err != nil {
				return err
			}
			if activeRunID == nil {
				if _, err := tx.Exec(ctx, `UPDATE worker_sessions SET bound_task_id=NULL,
status=CASE WHEN status='waiting_user' THEN 'interactive' ELSE status END,
session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND bound_task_id=$2`, sessionID, record.Command.TaskID, record.Now); err != nil {
					return err
				}
			} else {
				var turnID string
				var runRevision, turnRevision uint64
				if err := tx.QueryRow(ctx, `SELECT turn.agent_turn_id,run.revision,turn.revision
FROM worker_runs run JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id
WHERE run.worker_run_id=$1 AND turn.status IN ('delivered','consumed')
ORDER BY turn.created_at DESC LIMIT 1`, *activeRunID).Scan(&turnID, &runRevision, &turnRevision); err != nil {
					return platformpostgres.ErrRevisionConflict
				}
				outcome, _ := json.Marshal(map[string]any{"source": "task_cancel"})
				if _, err := tx.Exec(ctx, `INSERT INTO worker_user_commands
  (command_id,tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,command_type,
   expected_session_revision,expected_run_revision,expected_turn_revision,status,outcome,
   idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,'interrupt',$7,$8,$9,'accepted',$10,$11,$12,$12)`,
					record.ControlCommandID, tenantID, initiatorID, sessionID, *activeRunID, turnID,
					sessionRevision, runRevision, turnRevision, outcome,
					"task-cancel-control/"+record.Command.IdempotencyKey, record.Now); err != nil {
					return err
				}
			}
		}
		transitionResult, _ := json.Marshal(map[string]any{"cancelled": true, "event_id": record.EventID})
		if _, err := tx.Exec(ctx, `INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,from_state,to_state,
   trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,'cancelled','initiator_cancel',$7,$8)`, record.TransitionID,
			record.Command.TaskID, record.Command.IdempotencyKey, revision, nextRevision,
			status, transitionResult, record.Now); err != nil {
			return err
		}
		if err := store.appendTaskActionEvent(ctx, tx, record.EventID, tenantID, record.Command.TaskID,
			"TaskCancelled.v1", record.Command.IdempotencyKey, record.Command.IdempotencyKey,
			nextRevision, nil, record.Now); err != nil {
			return err
		}
		result = application.CancelledTask{TaskID: record.Command.TaskID, Status: "cancelled",
			Revision: nextRevision, EventID: record.EventID}
		return nil
	})
	return result, err
}

func (store *Store) SettleCancelledTask(
	ctx context.Context,
	record application.SettleCancelledTaskRecord,
) (application.SettledCancelledTask, error) {
	var result application.SettledCancelledTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var existingEventID string
		err := tx.QueryRow(ctx, `SELECT event_id FROM outbox_events
WHERE tenant_id=$1 AND idempotency_key=$2`, record.Command.Actor.TenantID,
			"TaskCancellationSettled.v1/"+record.Command.IdempotencyKey).Scan(&existingEventID)
		if err == nil {
			var revision uint64
			if err := tx.QueryRow(ctx, `SELECT session_revision FROM worker_sessions
WHERE worker_session_id=$1`, record.Command.WorkerSessionID).Scan(&revision); err != nil {
				return err
			}
			result = application.SettledCancelledTask{TaskID: record.Command.TaskID,
				WorkerSessionID: record.Command.WorkerSessionID, SessionRevision: revision,
				EventID: existingEventID}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var tenantID, initiatorID, status, sessionID, runID string
		var taskRevision uint64
		if err := tx.QueryRow(ctx, `SELECT tenant_id,initiator_user_id,status,
COALESCE(worker_session_id,''),COALESCE(current_worker_run_id,''),revision
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(&tenantID, &initiatorID,
			&status, &sessionID, &runID, &taskRevision); err != nil {
			return platformpostgres.ErrNotFound
		}
		if tenantID != record.Command.Actor.TenantID || initiatorID != record.Command.Actor.UserID ||
			status != "cancelled" || sessionID != record.Command.WorkerSessionID ||
			runID != record.Command.WorkerRunID || taskRevision != record.Command.ExpectedTaskRevision {
			return platformpostgres.ErrRevisionConflict
		}
		var sessionRevision uint64
		if err := tx.QueryRow(ctx, `UPDATE worker_sessions SET bound_task_id=NULL,
status=CASE WHEN status='waiting_user' THEN 'interactive' ELSE status END,
session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND bound_task_id=$2 AND active_worker_run_id IS NULL
  AND session_revision=$4 AND status IN ('interactive','waiting_user','idle')
RETURNING session_revision`, record.Command.WorkerSessionID, record.Command.TaskID, record.Now,
			record.Command.ExpectedSessionRevision).Scan(&sessionRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_user_commands SET status='rejected',
outcome=outcome || '{"code":"task_cancelled"}'::jsonb,updated_at=$2
WHERE worker_session_id=$1 AND command_type='queue' AND status='accepted'`,
			record.Command.WorkerSessionID, record.Now); err != nil {
			return err
		}
		if err := store.appendTaskActionEvent(ctx, tx, record.EventID, tenantID,
			record.Command.TaskID, "TaskCancellationSettled.v1", record.Command.IdempotencyKey,
			"", taskRevision, map[string]any{"session_revision": sessionRevision}, record.Now); err != nil {
			return err
		}
		result = application.SettledCancelledTask{TaskID: record.Command.TaskID,
			WorkerSessionID: record.Command.WorkerSessionID, SessionRevision: sessionRevision,
			EventID: record.EventID}
		return nil
	})
	return result, err
}

func readReworkResult(ctx context.Context, tx platformpostgres.Tx, taskID, idempotencyKey string) (application.ReworkedTask, bool, error) {
	var result application.ReworkedTask
	var payload json.RawMessage
	err := tx.QueryRow(ctx, `SELECT to_state,result_revision,result FROM task_transitions
WHERE task_id=$1 AND idempotency_key=$2`, taskID, idempotencyKey).Scan(&result.Status, &result.Revision, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReworkedTask{}, false, nil
	}
	if err != nil {
		return application.ReworkedTask{}, false, err
	}
	var values struct {
		WorkerSessionID string `json:"worker_session_id"`
		WorkerRunID     string `json:"worker_run_id"`
		AgentTurnID     string `json:"agent_turn_id"`
		EventID         string `json:"event_id"`
	}
	if json.Unmarshal(payload, &values) != nil {
		return application.ReworkedTask{}, false, platformpostgres.ErrRevisionConflict
	}
	result.TaskID = taskID
	result.WorkerSessionID, result.WorkerRunID = values.WorkerSessionID, values.WorkerRunID
	result.AgentTurnID, result.EventID = values.AgentTurnID, values.EventID
	return result, true, nil
}

func (store *Store) appendTaskActionEvent(ctx context.Context, tx platformpostgres.Tx, eventID,
	tenantID, taskID, eventType, commandID, clientCommandID string, revision uint64,
	extra map[string]any, now time.Time) error {
	stream := platformpostgres.OwnerStream{StreamID: "task/" + taskID,
		OwnerContext: "task-orchestration", AggregateType: "Task", AggregateID: taskID}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload := map[string]any{"task_id": taskID, "task_revision": revision}
	for key, value := range extra {
		payload[key] = value
	}
	encoded, _ := json.Marshal(payload)
	event := contractsv1.IntegrationEvent{SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID: eventID, EventType: eventType, ProducerContext: "task-orchestration",
		StreamID: stream.StreamID, Sequence: sequence, TenantID: tenantID, AggregateType: "Task",
		AggregateID: taskID, TraceID: "task/" + taskID, CorrelationID: eventID,
		CausationID: commandID, ClientCommandID: clientCommandID,
		IdempotencyKey: eventType + "/" + commandID,
		Visibility:     "user", OccurredAt: now, Payload: encoded}
	if err := event.Validate(); err != nil {
		return err
	}
	return insertOutboxEvent(ctx, tx, event)
}

func terminalRun(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timed_out"
}

func terminalTurn(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timed_out"
}

var _ application.TaskActionStore = (*Store)(nil)
