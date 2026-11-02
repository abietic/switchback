package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func (store *Store) PauseTask(ctx context.Context, record application.PauseTaskRecord) (application.PausedTask, error) {
	var result application.PausedTask
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		sourceDigest := ""
		if record.Command.RuntimeFailureEvent != nil {
			trusted, found, err := readRuntimeFailureTask(ctx, tx, *record.Command.RuntimeFailureEvent, true)
			if err != nil {
				return err
			}
			if !found {
				return runtimeFailureConflict()
			}
			if trusted.Actor != record.Command.Actor || trusted.TaskID != record.Command.TaskID || trusted.WorkerSessionID != record.Command.WorkerSessionID || trusted.WorkerRunID != record.Command.WorkerRunID || trusted.Reason != record.Command.Reason || trusted.IdempotencyKey != record.Command.IdempotencyKey {
				return application.ErrIdempotencyConflict
			}
			sourceDigest, err = verifyRuntimeFailureEvent(ctx, tx, *record.Command.RuntimeFailureEvent)
			if err != nil {
				return err
			}
		}
		var tenantID, initiatorID, status, sessionID, runID string
		var revision uint64
		err := tx.QueryRow(ctx, `
SELECT tenant_id,initiator_user_id,status,COALESCE(worker_session_id,''),
       COALESCE(current_worker_run_id,''),revision
FROM tasks WHERE task_id=$1 FOR UPDATE`, record.Command.TaskID).Scan(
			&tenantID, &initiatorID, &status, &sessionID, &runID, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if tenantID != record.Command.Actor.TenantID || initiatorID != record.Command.Actor.UserID {
			return platformpostgres.ErrNotFound
		}
		if sessionID != record.Command.WorkerSessionID || runID != record.Command.WorkerRunID {
			return runtimeFailureConflict()
		}
		var existing application.PausedTask
		var transitionResult []byte
		var expectedRevision uint64
		err = tx.QueryRow(ctx, `SELECT task_id,to_state,result_revision,expected_revision,result
FROM task_transitions WHERE task_id=$1 AND idempotency_key=$2`, record.Command.TaskID,
			record.Command.IdempotencyKey).Scan(&existing.TaskID, &existing.Status, &existing.Revision, &expectedRevision, &transitionResult)
		if err == nil {
			var stored struct {
				Code         string `json:"code"`
				EventID      string `json:"event_id"`
				SessionID    string `json:"worker_session_id"`
				RunID        string `json:"worker_run_id"`
				SourceDigest string `json:"source_event_digest"`
			}
			if err := json.Unmarshal(transitionResult, &stored); err != nil {
				return err
			}
			if stored.Code != record.Command.Reason || stored.SessionID != "" && stored.SessionID != sessionID || stored.RunID != "" && stored.RunID != runID || stored.SourceDigest != sourceDigest {
				return application.ErrIdempotencyConflict
			}
			if record.Command.RuntimeFailureEvent == nil && expectedRevision != record.Command.ExpectedTaskRevision {
				return application.ErrIdempotencyConflict
			}
			// Resolve actual owner identity for legacy results too; a TransitionID
			// is never an IntegrationEventID.
			if err := tx.QueryRow(ctx, `SELECT event_id FROM outbox_events WHERE producer_context='task-orchestration' AND aggregate_id=$1 AND event_type='TaskWaitingForUser.v1' AND idempotency_key=$2 AND tenant_id=$3 AND causation_id=$4`, record.Command.TaskID, "task-waiting/"+record.Command.IdempotencyKey, tenantID, runID).Scan(&existing.EventID); err != nil {
				return err
			}
			if stored.EventID != "" && stored.EventID != existing.EventID {
				return application.ErrIdempotencyConflict
			}
			result = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != record.Command.ExpectedTaskRevision || status != "running" ||
			sessionID != record.Command.WorkerSessionID || runID != record.Command.WorkerRunID {
			return runtimeFailureConflict()
		}
		var nextRevision uint64
		waitingReason, _ := json.Marshal(map[string]string{"code": record.Command.Reason})
		if err := tx.QueryRow(ctx, `UPDATE tasks
SET status='waiting_user',waiting_reason=$2,revision=revision+1,updated_at=$3
WHERE task_id=$1 AND revision=$4 AND status='running' RETURNING revision`, record.Command.TaskID,
			waitingReason, record.Now, revision).Scan(&nextRevision); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return runtimeFailureConflict()
			}
			return fmt.Errorf("pause Task: %w", err)
		}
		transitionResult, err = json.Marshal(map[string]string{"code": record.Command.Reason, "event_id": record.EventID,
			"worker_session_id": sessionID, "worker_run_id": runID, "source_event_digest": sourceDigest})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,
   from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,'running','waiting_user','worker_turn_paused',$6,$7)`,
			record.TransitionID, record.Command.TaskID, record.Command.IdempotencyKey,
			revision, nextRevision, transitionResult, record.Now); err != nil {
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
