package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/postgres/settlementbarrier"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

var (
	ErrSessionNotDispatchable = errors.New("WorkerSession is not dispatchable")
	ErrDispatchPermitExists   = errors.New("WorkerSession already has an active dispatch permit")
	ErrSessionQueueEmpty      = errors.New("WorkerSession Task queue is empty")
)

func (store *Store) EnqueueTask(
	ctx context.Context,
	record application.EnqueueTaskRecord,
) (application.EnqueuedTask, error) {
	var result application.EnqueuedTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		// Match Task actions: acquire the exact Task before its Session. Keep
		// replay ahead of revision/binding validation for response-loss retries.
		var taskTenant, taskUser, taskWorkspace string
		var taskRevision uint64
		var assignedSession *string
		err := tx.QueryRow(ctx, `
SELECT tenant_id,initiator_user_id,workspace_id,revision,worker_session_id
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&taskTenant, &taskUser, &taskWorkspace, &taskRevision, &assignedSession)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Task for Session enqueue: %w", err)
		}
		session, err := lockDispatchSession(ctx, tx, record.Command.WorkerSessionID)
		if err != nil {
			return err
		}
		if session.TenantID != record.Command.TenantID || session.UserID != record.Command.UserID {
			return platformpostgres.ErrNotFound
		}
		existing, found, err := readExistingEnqueue(ctx, tx, record.Command)
		if err != nil {
			return err
		}
		if found {
			result = existing
			return nil
		}
		if session.Revision != record.Command.ExpectedSessionRevision || session.Terminal || session.Lost {
			return ErrSessionNotDispatchable
		}
		if taskTenant != session.TenantID || taskUser != session.UserID || taskWorkspace != session.WorkspaceID ||
			taskRevision != record.Command.ExpectedTaskRevision || assignedSession != nil {
			return platformpostgres.ErrRevisionConflict
		}
		sequence, queueRevision, err := nextSessionQueuePosition(ctx, tx, record)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO session_task_queue_entries
  (queue_entry_id,worker_session_id,task_id,session_queue_sequence,queue_revision,
   expected_session_revision,status,idempotency_key,enqueued_at)
VALUES ($1,$2,$3,$4,$5,$6,'pending',$7,$8)`,
			record.QueueEntryID, record.Command.WorkerSessionID, record.Command.TaskID,
			sequence, queueRevision, session.Revision, record.Command.IdempotencyKey, record.Now); err != nil {
			return fmt.Errorf("insert SessionTaskQueue entry: %w", err)
		}
		var nextTaskRevision uint64
		err = tx.QueryRow(ctx, `
UPDATE tasks
SET worker_session_id=$3,session_allocation_sequence=NULL,allocation_wait_reason=NULL,
    session_queue_sequence=$4,revision=revision+1,updated_at=$5
WHERE task_id=$1 AND revision=$2 AND status='queued' AND worker_session_id IS NULL
RETURNING revision`, record.Command.TaskID, record.Command.ExpectedTaskRevision,
			record.Command.WorkerSessionID, sequence, record.Now).Scan(&nextTaskRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrRevisionConflict
		}
		if err != nil {
			return fmt.Errorf("assign Task to WorkerSession: %w", err)
		}
		if err := store.appendTaskAssignedEvent(ctx, tx, record, sequence, nextTaskRevision); err != nil {
			return err
		}
		result = application.EnqueuedTask{
			TaskID: record.Command.TaskID, WorkerSessionID: record.Command.WorkerSessionID,
			SessionQueueSequence: sequence, QueueRevision: queueRevision,
			TaskRevision: nextTaskRevision,
		}
		return nil
	})
	return result, err
}

func (store *Store) IssueTaskPermit(
	ctx context.Context,
	record application.IssueTaskPermitRecord,
) (application.IssuedTaskPermit, error) {
	var result application.IssuedTaskPermit
	err := workerpostgres.InCapacityTransaction(ctx, store.pool, func(tx pgx.Tx, capacity *workerpostgres.CapacityTx) error {
		// Locate without locking, then use pool -> exact Task -> Session. An old
		// permit always selects its original Task, never the next queue head.
		existing, found, err := readExistingPermit(ctx, tx, record.Command)
		if err != nil {
			return err
		}
		taskID := existing.TaskID
		if !found {
			err = tx.QueryRow(ctx, `SELECT task_id FROM session_task_queue_entries WHERE worker_session_id=$1 AND status IN ('pending','permit_issued') ORDER BY session_queue_sequence LIMIT 1`, record.Command.WorkerSessionID).Scan(&taskID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrSessionQueueEmpty
			}
			if err != nil {
				return err
			}
		}
		var taskTenant, taskActor, taskWorkspace, taskStatus string
		var taskSession *string
		var taskRevision uint64
		err = tx.QueryRow(ctx, `SELECT tenant_id,initiator_user_id,workspace_id,worker_session_id,status,revision FROM tasks WHERE task_id=$1 FOR UPDATE`, taskID).Scan(&taskTenant, &taskActor, &taskWorkspace, &taskSession, &taskStatus, &taskRevision)
		if err != nil {
			return err
		}
		session, err := lockDispatchSession(ctx, tx, record.Command.WorkerSessionID)
		if err != nil {
			return err
		}
		if session.TenantID != record.Command.TenantID || session.UserID != record.Command.UserID || taskTenant != session.TenantID || taskActor != session.UserID || taskWorkspace != session.WorkspaceID {
			return platformpostgres.ErrNotFound
		}
		// Capacity idempotency has tenant scope; the business command has Session
		// scope. A canonical tuple preserves that scope without separator collisions.
		keyBytes, _ := json.Marshal([]string{record.Command.WorkerSessionID, record.Command.IdempotencyKey})
		capacityKey := "task-permit/" + string(keyBytes)
		if found {
			var token, status, queueStatus, consumedRunID string
			var expiry time.Time
			err = tx.QueryRow(ctx, `SELECT capacity_admission_ref,status,expires_at,COALESCE(consumed_by_worker_run_id,'') FROM session_dispatch_permits WHERE permit_id=$1 FOR UPDATE`, existing.PermitID).Scan(&token, &status, &expiry, &consumedRunID)
			if err != nil {
				return err
			}
			target, err := capacity.BoundTaskTarget(ctx, token, existing.PermitID)
			if err != nil {
				return err
			}
			if target.Kind != "task_initial" || target.TenantID != taskTenant || target.ActorID != taskActor || target.SessionID != existing.WorkerSessionID || target.TaskID != taskID || target.Generation != existing.RuntimeGeneration || target.SessionRevision != existing.ExpectedSessionRevision || target.QueueSequence != existing.SessionQueueSequence || target.QueueRevision != existing.QueueRevision {
				return workerapplication.CapacityBindingMismatch
			}
			err = tx.QueryRow(ctx, `SELECT status FROM session_task_queue_entries WHERE worker_session_id=$1 AND task_id=$2 AND session_queue_sequence=$3 AND queue_revision=$4`, existing.WorkerSessionID, taskID, existing.SessionQueueSequence, existing.QueueRevision).Scan(&queueStatus)
			if err != nil {
				return workerapplication.CapacityBindingMismatch
			}
			if status != "active" && status != "consumed" {
				return workerapplication.CapacityBindingMismatch
			}
			if status == "active" && (queueStatus != "permit_issued" || taskStatus != "queued" || taskRevision != target.TaskRevision || taskSession == nil || *taskSession != record.Command.WorkerSessionID || session.Revision != target.SessionRevision || session.RuntimeGeneration != target.Generation) {
				return workerapplication.CapacityBindingMismatch
			}
			if status == "consumed" && queueStatus != "dispatched" {
				return workerapplication.CapacityBindingMismatch
			}
			reserved, err := capacity.ReserveAndBind(ctx, target, capacityKey, existing.PermitID)
			if err != nil {
				return err
			}
			if reserved.Token != token || reserved.PermitID != existing.PermitID || !reserved.ExpiresAt.Equal(expiry) || (status == "consumed") != (reserved.RunID != "") || reserved.RunID != consumedRunID {
				return workerapplication.CapacityBindingMismatch
			}
			result = existing
			return nil
		}
		if err := settlementbarrier.EnsureSessionClear(ctx, tx, record.Command.WorkerSessionID); err != nil {
			return err
		}
		initialGeneration := session.Status == "allocating" && session.RuntimeGeneration == 0 && record.Command.RuntimeGeneration == 1
		if session.Terminal || session.Lost || session.ActiveRunID != "" || session.BoundTaskID != "" || (!initialGeneration && (session.RuntimeGeneration != record.Command.RuntimeGeneration || (session.Status != "interactive" && session.Status != "idle"))) {
			return ErrSessionNotDispatchable
		}
		var activePermitID string
		err = tx.QueryRow(ctx, `SELECT permit_id FROM session_dispatch_permits WHERE worker_session_id=$1 AND status='active'`, record.Command.WorkerSessionID).Scan(&activePermitID)
		if err == nil {
			return fmt.Errorf("%w: %s", ErrDispatchPermitExists, activePermitID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var queueEntryID, headTask string
		var queueSequence, queueRevision uint64
		err = tx.QueryRow(ctx, `SELECT queue_entry_id,task_id,session_queue_sequence,queue_revision FROM session_task_queue_entries WHERE worker_session_id=$1 AND status='pending' ORDER BY session_queue_sequence LIMIT 1`, record.Command.WorkerSessionID).Scan(&queueEntryID, &headTask, &queueSequence, &queueRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSessionQueueEmpty
		}
		if err != nil {
			return err
		}
		if headTask != taskID || taskStatus != "queued" || taskSession == nil || *taskSession != record.Command.WorkerSessionID {
			return platformpostgres.ErrRevisionConflict
		}
		// This slice authorizes only the first Runtime, not resident/recovery starts.
		var hasRuntime bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_instances WHERE worker_session_id=$1)`, record.Command.WorkerSessionID).Scan(&hasRuntime); err != nil {
			return err
		}
		if hasRuntime || record.Command.RuntimeGeneration != 1 {
			return workerapplication.CapacityPathNotIntegrated
		}
		baseline := session.Revision
		if initialGeneration {
			baseline++
		}
		profile, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
		if !ok {
			return workerapplication.CapacityBindingMismatch
		}
		target := workerapplication.CapacityTarget{Kind: "task_initial", TenantID: taskTenant, ActorID: taskActor, SessionID: record.Command.WorkerSessionID, TaskID: taskID, Generation: record.Command.RuntimeGeneration, SessionRevision: baseline, TaskRevision: taskRevision, QueueSequence: queueSequence, QueueRevision: queueRevision, Profile: workerapplication.CapacityProfile{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}}
		reserved, err := capacity.ReserveAndBind(ctx, target, capacityKey, record.PermitID)
		if err != nil {
			return err
		}
		if initialGeneration {
			err = tx.QueryRow(ctx, `UPDATE worker_sessions SET status='interactive',runtime_generation=1,session_revision=session_revision+1,updated_at=$2 WHERE worker_session_id=$1 AND status='allocating' AND runtime_generation=0 AND session_revision=$3 RETURNING session_revision`, record.Command.WorkerSessionID, record.Now, session.Revision).Scan(&session.Revision)
			if err != nil {
				return fmt.Errorf("establish initial Runtime generation: %w", err)
			}
			session.RuntimeGeneration = 1
		}
		if _, err = tx.Exec(ctx, `INSERT INTO session_dispatch_permits (permit_id,worker_session_id,target_kind,target_id,session_queue_sequence,queue_revision,expected_session_revision,runtime_generation,capacity_admission_ref,status,idempotency_key,expires_at,created_at) VALUES ($1,$2,'task',$3,$4,$5,$6,$7,$8,'active',$9,$10,clock_timestamp())`, reserved.PermitID, record.Command.WorkerSessionID, taskID, queueSequence, queueRevision, session.Revision, record.Command.RuntimeGeneration, reserved.Token, record.Command.IdempotencyKey, reserved.ExpiresAt); err != nil {
			return fmt.Errorf("insert SessionDispatchPermit: %w", err)
		}
		tag, err := tx.Exec(ctx, `UPDATE session_task_queue_entries SET status='permit_issued' WHERE queue_entry_id=$1 AND status='pending'`, queueEntryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return platformpostgres.ErrRevisionConflict
		}
		if err := store.clearRuntimeDispatchWait(ctx, tx, taskID, taskTenant, reserved.PermitID+"/dispatch-ready", taskRevision, record.Now); err != nil {
			return err
		}
		result = application.IssuedTaskPermit{PermitID: reserved.PermitID, WorkerSessionID: record.Command.WorkerSessionID, TaskID: taskID, SessionQueueSequence: queueSequence, QueueRevision: queueRevision, ExpectedSessionRevision: session.Revision, RuntimeGeneration: session.RuntimeGeneration}
		return nil
	})
	return result, err
}

type dispatchSession struct {
	TenantID          string
	UserID            string
	WorkspaceID       string
	Status            string
	Revision          uint64
	RuntimeGeneration uint64
	BoundTaskID       string
	ActiveRunID       string
	Terminal          bool
	Lost              bool
}

func lockDispatchSession(ctx context.Context, tx platformpostgres.Tx, sessionID string) (dispatchSession, error) {
	var session dispatchSession
	var boundTaskID, activeRunID *string
	err := tx.QueryRow(ctx, `
SELECT tenant_id,user_id,workspace_id,status,session_revision,runtime_generation,
  bound_task_id,active_worker_run_id
FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, sessionID).Scan(
		&session.TenantID, &session.UserID, &session.WorkspaceID, &session.Status,
		&session.Revision, &session.RuntimeGeneration, &boundTaskID, &activeRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return dispatchSession{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return dispatchSession{}, fmt.Errorf("lock dispatch WorkerSession: %w", err)
	}
	if boundTaskID != nil {
		session.BoundTaskID = *boundTaskID
	}
	if activeRunID != nil {
		session.ActiveRunID = *activeRunID
	}
	session.Terminal = session.Status == "closed" || session.Status == "expired"
	session.Lost = session.Status == "lost"
	return session, nil
}

func readExistingEnqueue(
	ctx context.Context,
	tx platformpostgres.Tx,
	command application.EnqueueTaskCommand,
) (application.EnqueuedTask, bool, error) {
	var result application.EnqueuedTask
	var existingTaskID string
	err := tx.QueryRow(ctx, `
SELECT task_id,session_queue_sequence,queue_revision
FROM session_task_queue_entries
WHERE worker_session_id=$1 AND idempotency_key=$2`,
		command.WorkerSessionID, command.IdempotencyKey).Scan(
		&existingTaskID, &result.SessionQueueSequence, &result.QueueRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EnqueuedTask{}, false, nil
	}
	if err != nil {
		return application.EnqueuedTask{}, false, fmt.Errorf("read idempotent Session enqueue: %w", err)
	}
	if existingTaskID != command.TaskID {
		return application.EnqueuedTask{}, false, application.ErrIdempotencyConflict
	}
	result.TaskID = existingTaskID
	result.WorkerSessionID = command.WorkerSessionID
	if err := tx.QueryRow(ctx, `SELECT revision FROM tasks WHERE task_id=$1`, existingTaskID).Scan(&result.TaskRevision); err != nil {
		return application.EnqueuedTask{}, false, err
	}
	return result, true, nil
}

func nextSessionQueuePosition(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.EnqueueTaskRecord,
) (uint64, uint64, error) {
	if _, err := tx.Exec(ctx, `
INSERT INTO session_task_queue_positions (worker_session_id,next_sequence,revision,updated_at)
VALUES ($1,1,1,$2) ON CONFLICT DO NOTHING`, record.Command.WorkerSessionID, record.Now); err != nil {
		return 0, 0, fmt.Errorf("ensure SessionTaskQueue position: %w", err)
	}
	var sequence, queueRevision uint64
	err := tx.QueryRow(ctx, `
UPDATE session_task_queue_positions
SET next_sequence=next_sequence+1,revision=revision+1,updated_at=$2
WHERE worker_session_id=$1
RETURNING next_sequence-1,revision`, record.Command.WorkerSessionID, record.Now).Scan(
		&sequence, &queueRevision)
	if err != nil {
		return 0, 0, fmt.Errorf("allocate SessionTaskQueue sequence: %w", err)
	}
	return sequence, queueRevision, nil
}

func readExistingPermit(
	ctx context.Context,
	tx platformpostgres.Tx,
	command application.IssueTaskPermitCommand,
) (application.IssuedTaskPermit, bool, error) {
	var result application.IssuedTaskPermit
	var targetKind string
	err := tx.QueryRow(ctx, `
SELECT permit_id,target_kind,target_id,session_queue_sequence,queue_revision,
  expected_session_revision,runtime_generation
FROM session_dispatch_permits
WHERE worker_session_id=$1 AND idempotency_key=$2`,
		command.WorkerSessionID, command.IdempotencyKey).Scan(
		&result.PermitID, &targetKind, &result.TaskID, &result.SessionQueueSequence,
		&result.QueueRevision, &result.ExpectedSessionRevision, &result.RuntimeGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.IssuedTaskPermit{}, false, nil
	}
	if err != nil {
		return application.IssuedTaskPermit{}, false, fmt.Errorf("read idempotent dispatch permit: %w", err)
	}
	if targetKind != "task" || result.RuntimeGeneration != command.RuntimeGeneration {
		return application.IssuedTaskPermit{}, false, application.ErrIdempotencyConflict
	}
	result.WorkerSessionID = command.WorkerSessionID
	return result, true, nil
}

func (store *Store) appendTaskAssignedEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.EnqueueTaskRecord,
	queueSequence uint64,
	taskRevision uint64,
) error {
	stream := platformpostgres.OwnerStream{
		StreamID: "task/" + record.Command.TaskID, OwnerContext: "task-orchestration",
		AggregateType: "Task", AggregateID: record.Command.TaskID,
	}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		WorkerSessionID      string `json:"worker_session_id"`
		SessionQueueSequence uint64 `json:"session_queue_sequence"`
		TaskRevision         uint64 `json:"task_revision"`
	}{record.Command.WorkerSessionID, queueSequence, taskRevision})
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.EventID, EventType: "TaskAssignedToWorkerSession.v1",
		ProducerContext: "task-orchestration", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: record.Command.TenantID, AggregateType: "Task", AggregateID: record.Command.TaskID,
		TraceID: "task/" + record.Command.TaskID, CorrelationID: record.Command.IdempotencyKey,
		CausationID:    record.Command.WorkerSessionID,
		IdempotencyKey: "task-session-assigned/" + record.Command.TaskID,
		Visibility:     "internal", OccurredAt: record.Now, Payload: payload,
	}
	if err := event.Validate(); err != nil {
		return err
	}
	return insertOutboxEvent(ctx, tx, event)
}
