package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/postgres/settlementbarrier"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

var (
	ErrDispatchPermitInvalid  = errors.New("SessionDispatchPermit is invalid or stale")
	ErrDispatchPermitConsumed = errors.New("SessionDispatchPermit was consumed by another Run")
	ErrSessionBusy            = errors.New("WorkerSession already has an active top-level Run")
)

type RunStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewRunStore(pool *pgxpool.Pool) *RunStore {
	return &RunStore{pool: pool}
}

func (store *RunStore) StartWorkerRun(
	ctx context.Context,
	record application.StartWorkerRunRecord,
) (application.StartedWorkerRun, error) {
	var result application.StartedWorkerRun
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		session, err := lockRunSession(ctx, tx, record.Command.WorkerSessionID)
		if err != nil {
			return err
		}
		if session.TenantID != record.Command.TenantID || session.UserID != record.Command.UserID {
			return platformpostgres.ErrNotFound
		}
		permit, err := lockRunPermit(ctx, tx, record.Command.PermitID)
		if err != nil {
			return err
		}
		if permit.WorkerSessionID != record.Command.WorkerSessionID || permit.TargetKind != "task" ||
			permit.TargetID != record.Command.TaskID {
			return ErrDispatchPermitInvalid
		}
		if permit.Status == "consumed" {
			if permit.ConsumedRunID == "" {
				return ErrDispatchPermitInvalid
			}
			result, err = readStartedRun(ctx, tx, permit.ConsumedRunID)
			return err
		}
		if err := settlementbarrier.EnsureSessionClear(
			ctx,
			tx,
			record.Command.WorkerSessionID,
		); err != nil {
			return err
		}
		if permit.Status != "active" || !permit.ExpiresAt.After(record.Now) ||
			permit.ExpectedSessionRevision != record.Command.ExpectedSessionRevision ||
			permit.RuntimeGeneration != record.Command.RuntimeGeneration {
			return ErrDispatchPermitInvalid
		}
		if session.Revision != permit.ExpectedSessionRevision ||
			session.RuntimeGeneration != permit.RuntimeGeneration {
			return platformpostgres.ErrRevisionConflict
		}
		if session.ActiveRunID != "" || session.BoundTaskID != "" ||
			session.ActiveRuntimeID != "" {
			return ErrSessionBusy
		}
		if session.Status != "interactive" && session.Status != "idle" {
			return ErrSessionBusy
		}
		if err := validatePermitQueueEntry(ctx, tx, permit, record.Command.TaskID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO worker_runs
  (worker_run_id,worker_session_id,task_id,trigger_type,start_snapshot,agent_profile_id,
   agent_profile_revision,agent_profile_digest,status,revision,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'starting',1,$9)`,
			record.WorkerRunID, record.Command.WorkerSessionID, record.Command.TaskID,
			record.Command.TriggerType, record.Command.StartSnapshot, record.Command.AgentProfileID,
			record.Command.AgentProfileRevision, record.Command.AgentProfileDigest, record.Now); err != nil {
			return fmt.Errorf("insert WorkerRun: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at)
VALUES ($1,$2,$3,$4,'prompt',$5,'delivered',1,$6,$7,$7,$7::timestamptz + interval '24 hours')`,
			record.AgentTurnID, record.Command.WorkerSessionID, record.WorkerRunID,
			record.AgentSessionID, record.Command.AgentTurnContent,
			record.Command.TurnIdempotencyKey, record.Now); err != nil {
			return fmt.Errorf("insert initial AgentTurn: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'agent_turn_deadline','AgentTurn',$3,1,'{}'::jsonb,
  $4::timestamptz + interval '24 hours','pending',$5,$4,$4)`,
			record.ScheduledCommandID, record.Command.TenantID, record.AgentTurnID,
			record.Now, "agent-turn-deadline/"+record.AgentTurnID); err != nil {
			return fmt.Errorf("schedule initial AgentTurn deadline: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,status,
   image_ref,resource_profile_id,network_policy_id,network_policy_revision,network_policy_digest,
   network_enforcement_status,control_channel_epoch,revision,created_at)
VALUES ($1,$2,$3,$4,'docker_container','starting',$5,$6,$7,$8,$9,$10,$11,1,$12)`,
			record.RuntimeInstanceID, record.Command.WorkerSessionID, record.WorkerRunID,
			record.Command.RuntimeGeneration, record.Command.RuntimeImageRef,
			record.Command.ResourceProfileID, record.Command.NetworkPolicyID,
			record.Command.NetworkPolicyRevision, record.Command.NetworkPolicyDigest,
			record.Command.NetworkEnforcementStatus, record.Command.ControlChannelEpoch,
			record.Now); err != nil {
			return fmt.Errorf("insert RuntimeInstance: %w", err)
		}
		var taskRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE tasks
SET status='running',current_worker_run_id=$3,revision=revision+1,updated_at=$4
WHERE task_id=$1 AND worker_session_id=$2 AND status='queued'
RETURNING revision`, record.Command.TaskID, record.Command.WorkerSessionID,
			record.WorkerRunID, record.Now).Scan(&taskRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("start Task with WorkerRun: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,
   from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,'queued','running','worker_run_started',$6,$7)`,
			record.TaskTransitionID, record.Command.TaskID,
			"worker-run-start/"+record.Command.PermitID, taskRevision-1, taskRevision,
			json.RawMessage(`{"worker_run_started":true}`), record.Now); err != nil {
			return fmt.Errorf("insert Task WorkerRun transition: %w", err)
		}
		var sessionRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE worker_sessions
SET bound_task_id=$3,active_worker_run_id=$4,active_runtime_instance_id=$5,
    status='running',session_revision=session_revision+1,updated_at=$6
WHERE worker_session_id=$1 AND session_revision=$2
  AND active_worker_run_id IS NULL AND bound_task_id IS NULL
  AND active_runtime_instance_id IS NULL
RETURNING session_revision`, record.Command.WorkerSessionID, session.Revision,
			record.Command.TaskID, record.WorkerRunID, record.RuntimeInstanceID,
			record.Now).Scan(&sessionRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionBusy
		}
		if err != nil {
			return fmt.Errorf("occupy WorkerSession execution slot: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE session_dispatch_permits
SET status='consumed',consumed_by_worker_run_id=$2,consumed_at=$3
WHERE permit_id=$1 AND status='active'`, record.Command.PermitID, record.WorkerRunID, record.Now); err != nil {
			return fmt.Errorf("consume SessionDispatchPermit: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE session_task_queue_entries
SET status='dispatched',dispatched_at=$3
WHERE worker_session_id=$1 AND task_id=$2 AND status='permit_issued'`,
			record.Command.WorkerSessionID, record.Command.TaskID, record.Now); err != nil {
			return fmt.Errorf("dispatch SessionTaskQueue entry: %w", err)
		}
		if err := store.appendRunStartedEvent(ctx, tx, record, sessionRevision); err != nil {
			return err
		}
		result = application.StartedWorkerRun{
			WorkerRunID: record.WorkerRunID, WorkerSessionID: record.Command.WorkerSessionID,
			TaskID: record.Command.TaskID, RuntimeInstanceID: record.RuntimeInstanceID,
			AgentSessionID: record.AgentSessionID, AgentTurnID: record.AgentTurnID,
			RuntimeGeneration:   record.Command.RuntimeGeneration,
			ControlChannelEpoch: record.Command.ControlChannelEpoch,
			Status:              "starting", RunRevision: 1, SessionRevision: sessionRevision,
		}
		return nil
	})
	return result, err
}

type runSession struct {
	TenantID          string
	UserID            string
	Status            string
	Revision          uint64
	RuntimeGeneration uint64
	BoundTaskID       string
	ActiveRunID       string
	ActiveRuntimeID   string
}

func lockRunSession(ctx context.Context, tx platformpostgres.Tx, sessionID string) (runSession, error) {
	var session runSession
	var boundTaskID, activeRunID, activeRuntimeID *string
	err := tx.QueryRow(ctx, `
SELECT tenant_id,user_id,status,session_revision,runtime_generation,bound_task_id,
       active_worker_run_id,active_runtime_instance_id
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, sessionID).Scan(
		&session.TenantID, &session.UserID, &session.Status, &session.Revision,
		&session.RuntimeGeneration, &boundTaskID, &activeRunID, &activeRuntimeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return runSession{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return runSession{}, fmt.Errorf("lock WorkerSession Run slot: %w", err)
	}
	if boundTaskID != nil {
		session.BoundTaskID = *boundTaskID
	}
	if activeRunID != nil {
		session.ActiveRunID = *activeRunID
	}
	if activeRuntimeID != nil {
		session.ActiveRuntimeID = *activeRuntimeID
	}
	return session, nil
}

type runPermit struct {
	WorkerSessionID         string
	TargetKind              string
	TargetID                string
	Sequence                uint64
	QueueRevision           uint64
	ExpectedSessionRevision uint64
	RuntimeGeneration       uint64
	Status                  string
	ConsumedRunID           string
	ExpiresAt               time.Time
}

func lockRunPermit(ctx context.Context, tx platformpostgres.Tx, permitID string) (runPermit, error) {
	var permit runPermit
	var sequence *uint64
	var consumedRunID *string
	err := tx.QueryRow(ctx, `
SELECT worker_session_id,target_kind,target_id,session_queue_sequence,queue_revision,
  expected_session_revision,runtime_generation,status,consumed_by_worker_run_id,expires_at
FROM session_dispatch_permits WHERE permit_id=$1 FOR UPDATE`, permitID).Scan(
		&permit.WorkerSessionID, &permit.TargetKind, &permit.TargetID, &sequence,
		&permit.QueueRevision, &permit.ExpectedSessionRevision, &permit.RuntimeGeneration,
		&permit.Status, &consumedRunID, &permit.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return runPermit{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return runPermit{}, fmt.Errorf("lock SessionDispatchPermit: %w", err)
	}
	if sequence != nil {
		permit.Sequence = *sequence
	}
	if consumedRunID != nil {
		permit.ConsumedRunID = *consumedRunID
	}
	return permit, nil
}

func validatePermitQueueEntry(
	ctx context.Context,
	tx platformpostgres.Tx,
	permit runPermit,
	taskID string,
) error {
	var valid bool
	err := tx.QueryRow(ctx, `
SELECT true FROM session_task_queue_entries
WHERE worker_session_id=$1 AND task_id=$2 AND session_queue_sequence=$3
  AND queue_revision=$4 AND status='permit_issued'`,
		permit.WorkerSessionID, taskID, permit.Sequence, permit.QueueRevision).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDispatchPermitInvalid
	}
	if err != nil {
		return fmt.Errorf("validate permit queue entry: %w", err)
	}
	return nil
}

func readStartedRun(ctx context.Context, tx platformpostgres.Tx, runID string) (application.StartedWorkerRun, error) {
	var result application.StartedWorkerRun
	err := tx.QueryRow(ctx, `
SELECT run.worker_run_id,run.worker_session_id,run.task_id,run.status,run.revision,
  runtime.runtime_instance_id,runtime.generation,runtime.control_channel_epoch,
  turn.agent_session_id,turn.agent_turn_id
FROM worker_runs run
JOIN runtime_instances runtime ON runtime.worker_run_id=run.worker_run_id
JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id AND turn.mode='prompt'
WHERE run.worker_run_id=$1`, runID).Scan(
		&result.WorkerRunID, &result.WorkerSessionID, &result.TaskID, &result.Status,
		&result.RunRevision, &result.RuntimeInstanceID, &result.RuntimeGeneration,
		&result.ControlChannelEpoch, &result.AgentSessionID, &result.AgentTurnID)
	if err != nil {
		return application.StartedWorkerRun{}, fmt.Errorf("read consumed permit Run: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT session_revision FROM worker_sessions
WHERE worker_session_id=$1`, result.WorkerSessionID).Scan(&result.SessionRevision); err != nil {
		return application.StartedWorkerRun{}, err
	}
	return result, nil
}

func (store *RunStore) appendRunStartedEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.StartWorkerRunRecord,
	sessionRevision uint64,
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
	payload, err := workerRunStartedPayload(record, sessionRevision)
	if err != nil {
		return err
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.EventID, EventType: "WorkerRunStarted.v1",
		ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: record.Command.TenantID, AggregateType: "WorkerSession",
		AggregateID:   record.Command.WorkerSessionID,
		TraceID:       "worker-session/" + record.Command.WorkerSessionID,
		CorrelationID: record.Command.PermitID, CausationID: record.Command.TaskID,
		IdempotencyKey: "worker-run-started/" + record.Command.PermitID,
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
		return fmt.Errorf("insert WorkerRun started Outbox event: %w", err)
	}
	return nil
}

func workerRunStartedPayload(
	record application.StartWorkerRunRecord,
	sessionRevision uint64,
) (json.RawMessage, error) {
	payload, err := json.Marshal(struct {
		WorkerRunID       string                        `json:"worker_run_id"`
		TaskID            string                        `json:"task_id"`
		RuntimeInstanceID string                        `json:"runtime_instance_id"`
		AgentSessionID    string                        `json:"agent_session_id"`
		AgentTurnID       string                        `json:"agent_turn_id"`
		SessionRevision   uint64                        `json:"session_revision"`
		NetworkProfile    config.RuntimeNetworkEvidence `json:"network_profile"`
	}{
		WorkerRunID: record.WorkerRunID, TaskID: record.Command.TaskID,
		RuntimeInstanceID: record.RuntimeInstanceID, AgentSessionID: record.AgentSessionID,
		AgentTurnID: record.AgentTurnID, SessionRevision: sessionRevision,
		NetworkProfile: config.RuntimeNetworkEvidence{
			ID: record.Command.NetworkPolicyID, Revision: record.Command.NetworkPolicyRevision,
			Digest:            record.Command.NetworkPolicyDigest,
			EnforcementStatus: record.Command.NetworkEnforcementStatus,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode WorkerRun started payload: %w", err)
	}
	return payload, nil
}
