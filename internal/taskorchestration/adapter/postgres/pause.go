package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func (store *Store) PauseTask(ctx context.Context, record application.PauseTaskRecord) (application.PausedTask, error) {
	var result application.PausedTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var existing application.PausedTask
		err := tx.QueryRow(ctx, `SELECT task_id,to_state,result_revision,transition_id
FROM task_transitions WHERE task_id=$1 AND idempotency_key=$2`, record.Command.TaskID,
			record.Command.IdempotencyKey).Scan(&existing.TaskID, &existing.Status, &existing.Revision, &existing.EventID)
		if err == nil {
			result = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var tenantID, initiatorID, status, sessionID, runID string
		var revision uint64
		err = tx.QueryRow(ctx, `
SELECT tenant_id,initiator_user_id,status,COALESCE(worker_session_id,''),
       COALESCE(current_worker_run_id,''),revision
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&tenantID, &initiatorID, &status, &sessionID, &runID, &revision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.Actor.TenantID ||
			initiatorID != record.Command.Actor.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if revision != record.Command.ExpectedTaskRevision || status != "running" ||
			sessionID != record.Command.WorkerSessionID || runID != record.Command.WorkerRunID {
			return platformpostgres.ErrRevisionConflict
		}
		var nextRevision uint64
		waitingReason, _ := json.Marshal(map[string]string{"code": record.Command.Reason})
		if err := tx.QueryRow(ctx, `UPDATE tasks
SET status='waiting_user',waiting_reason=$2,revision=revision+1,updated_at=$3
WHERE task_id=$1 AND revision=$4 AND status='running' RETURNING revision`, record.Command.TaskID,
			waitingReason, record.Now, revision).Scan(&nextRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,
   from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,'running','waiting_user','worker_turn_paused',$6,$7)`,
			record.TransitionID, record.Command.TaskID, record.Command.IdempotencyKey,
			revision, nextRevision, waitingReason, record.Now); err != nil {
			return err
		}
		stream := platformpostgres.OwnerStream{StreamID: "task/" + record.Command.TaskID,
			OwnerContext: "task-orchestration", AggregateType: "Task", AggregateID: record.Command.TaskID}
		sequence, err := store.streams.Next(ctx, tx, stream)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"task_id": record.Command.TaskID,
			"task_revision": nextRevision, "reason": record.Command.Reason})
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: record.EventID,
			EventType: "TaskWaitingForUser.v1", ProducerContext: "task-orchestration",
			StreamID: stream.StreamID, Sequence: sequence, TenantID: tenantID,
			AggregateType: "Task", AggregateID: record.Command.TaskID,
			TraceID: "task/" + record.Command.TaskID, CorrelationID: record.EventID,
			CausationID:    record.Command.WorkerRunID,
			IdempotencyKey: "task-waiting/" + record.Command.IdempotencyKey,
			Visibility:     "user", OccurredAt: record.Now, Payload: payload,
		}
		if err := event.Validate(); err != nil {
			return err
		}
		if err := insertOutboxEvent(ctx, tx, event); err != nil {
			return err
		}
		result = application.PausedTask{TaskID: record.Command.TaskID, Status: "waiting_user",
			Revision: nextRevision, EventID: record.EventID}
		return nil
	})
	return result, err
}

var _ application.TaskPauseStore = (*Store)(nil)
