package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeFailureCancellationPublisher struct {
	Pool    *pgxpool.Pool
	Actions taskapp.TaskActionService
	Next    platformpostgres.EventPublisher
}

func (publisher runtimeFailureCancellationPublisher) Publish(ctx context.Context, event contractsv1.IntegrationEvent) error {
	if event.EventType != "TaskCancelled.v1" {
		return publisher.Next.Publish(ctx, event)
	}
	// Source identity is verified before projection or command creation. Actor
	// authority comes from the Task record, never from event payload/user text.
	var durable contractsv1.IntegrationEvent
	if err := publisher.Pool.QueryRow(ctx, `SELECT schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,COALESCE(client_command_id,''),visibility,occurred_at,payload FROM outbox_events WHERE event_id=$1`, event.EventID).Scan(&durable.SchemaVersion, &durable.EventID, &durable.EventType, &durable.ProducerContext, &durable.StreamID, &durable.Sequence, &durable.TenantID, &durable.AggregateType, &durable.AggregateID, &durable.TraceID, &durable.CorrelationID, &durable.CausationID, &durable.IdempotencyKey, &durable.ClientCommandID, &durable.Visibility, &durable.OccurredAt, &durable.Payload); err != nil {
		return err
	}
	event.OccurredAt, durable.OccurredAt = event.OccurredAt.UTC(), durable.OccurredAt.UTC()
	digest, err := platformpostgres.IntegrationEventDigest(event)
	if err != nil {
		return err
	}
	stored, err := platformpostgres.IntegrationEventDigest(durable)
	if err != nil {
		return err
	}
	if digest != stored || event.ProducerContext != "task-orchestration" || event.AggregateType != "Task" || event.StreamID != "task/"+event.AggregateID {
		return taskapp.ErrIdempotencyConflict
	}
	var command taskapp.SettleCancelledTaskCommand
	var status string
	if err := publisher.Pool.QueryRow(ctx, `SELECT task.tenant_id,task.initiator_user_id,task.task_id,COALESCE(task.worker_session_id,''),COALESCE(task.current_worker_run_id,''),task.revision,COALESCE(session.session_revision,0),task.status FROM tasks task LEFT JOIN worker_sessions session ON session.worker_session_id=task.worker_session_id WHERE task.task_id=$1`, event.AggregateID).Scan(&command.Actor.TenantID, &command.Actor.UserID, &command.TaskID, &command.WorkerSessionID, &command.WorkerRunID, &command.ExpectedTaskRevision, &command.ExpectedSessionRevision, &status); err != nil {
		return err
	}
	var source struct {
		TaskID       string `json:"task_id"`
		TaskRevision uint64 `json:"task_revision"`
	}
	if err := json.Unmarshal(event.Payload, &source); err != nil {
		return err
	}
	if command.Actor.TenantID != event.TenantID || source.TaskID != command.TaskID || source.TaskRevision != command.ExpectedTaskRevision || status != "cancelled" {
		return taskapp.ErrIdempotencyConflict
	}
	// The cancellation itself is committed and visible even while this source
	// remains unacknowledged for cleanup. Projection deduplicates repeated source.
	if err := publisher.Next.Publish(ctx, event); err != nil {
		return err
	}
	if command.WorkerSessionID == "" || command.WorkerRunID == "" {
		return nil
	}
	_, found, err := workerpostgres.ReadRuntimeFailureCancellation(ctx, publisher.Pool, command.Actor.TenantID, command.WorkerSessionID, command.WorkerRunID)
	if err != nil || !found {
		return err
	}
	command.IdempotencyKey = "runtime-failure-cancel/" + event.EventID
	// Recover the original request after a lost Settle response. The receipt's
	// digest is rechecked by the Task owner before returning its frozen result.
	var expected uint64
	err = publisher.Pool.QueryRow(ctx, `SELECT (payload->>'expected_session_revision')::bigint FROM outbox_events WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='TaskCancellationSettled.v1' AND producer_context='task-orchestration' AND idempotency_key=$3`, command.Actor.TenantID, command.TaskID, "TaskCancellationSettled.v1/"+command.IdempotencyKey).Scan(&expected)
	if err == nil {
		command.ExpectedSessionRevision = expected
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read cancellation receipt: %w", err)
	}
	_, err = publisher.Actions.SettleCancelled(ctx, command)
	return err
}

func (publisher runtimeFailureCancellationPublisher) SupportedIntegrationEventSchemaVersions() []string {
	return publisher.Next.SupportedIntegrationEventSchemaVersions()
}
