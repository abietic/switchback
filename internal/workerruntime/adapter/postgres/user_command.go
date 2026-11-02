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

type WorkerUserCommandStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewWorkerUserCommandStore(pool *pgxpool.Pool) *WorkerUserCommandStore {
	return &WorkerUserCommandStore{pool: pool}
}

func (store *WorkerUserCommandStore) SubmitWorkerUserCommand(
	ctx context.Context,
	record application.WorkerUserCommandRecord,
) (application.AcceptedWorkerUserCommand, error) {
	var result application.AcceptedWorkerUserCommand
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var existingDigest string
		var existingSequence *uint64
		err := tx.QueryRow(ctx, `
SELECT command_id,status,queue_sequence,outcome->>'request_digest'
FROM worker_user_commands
WHERE tenant_id=$1 AND user_id=$2 AND idempotency_key=$3`, record.Command.TenantID,
			record.Command.UserID, record.Command.IdempotencyKey).Scan(
			&result.CommandID, &result.Status, &existingSequence, &existingDigest)
		if err == nil {
			if existingDigest != record.RequestDigest {
				return application.ErrWorkerUserCommandIdempotencyConflict
			}
			result.QueueSequence = existingSequence
			result.WorkerSessionID = record.Command.WorkerSessionID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var tenantID, userID, sessionStatus, runStatus, turnStatus string
		var sessionRevision, runRevision, turnRevision uint64
		err = tx.QueryRow(ctx, `
SELECT session.tenant_id,session.user_id,session.status,run.status,turn.status,
       session.session_revision,run.revision,turn.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN agent_turns turn ON turn.worker_session_id=session.worker_session_id
  AND turn.worker_run_id=run.worker_run_id AND turn.agent_turn_id=$3
WHERE session.worker_session_id=$1
FOR UPDATE OF session,run,turn`, record.Command.WorkerSessionID,
			record.Command.WorkerRunID, record.Command.AgentTurnID).Scan(
			&tenantID, &userID, &sessionStatus, &runStatus, &turnStatus,
			&sessionRevision, &runRevision, &turnRevision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.TenantID || userID != record.Command.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if sessionRevision != record.Command.ExpectedSessionRevision ||
			runRevision != record.Command.ExpectedRunRevision ||
			turnRevision != record.Command.ExpectedTurnRevision || sessionStatus != "running" ||
			runStatus != "running" || (turnStatus != "delivered" && turnStatus != "consumed") {
			return platformpostgres.ErrRevisionConflict
		}
		var queueSequence *uint64
		if record.Command.CommandType == "queue" {
			sequence, err := nextWorkerMessageSequence(ctx, tx, record)
			if err != nil {
				return err
			}
			queueSequence = &sequence
		}
		outcome, _ := json.Marshal(map[string]any{"request_digest": record.RequestDigest})
		if _, err := tx.Exec(ctx, `
INSERT INTO worker_user_commands
  (command_id,tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,command_type,
   content,expected_session_revision,expected_run_revision,expected_turn_revision,queue_sequence,
   status,outcome,idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'accepted',$13,$14,$15,$15)`,
			record.CommandID, record.Command.TenantID, record.Command.UserID,
			record.Command.WorkerSessionID, record.Command.WorkerRunID, record.Command.AgentTurnID,
			record.Command.CommandType, record.Command.Content,
			record.Command.ExpectedSessionRevision, record.Command.ExpectedRunRevision,
			record.Command.ExpectedTurnRevision, queueSequence, outcome,
			record.Command.IdempotencyKey, record.Now); err != nil {
			return err
		}
		if err := refreshSessionIdleDeadline(ctx, tx, record.Command.WorkerSessionID, sessionRevision, record.Now); err != nil {
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
			"command_id": record.CommandID, "command_type": record.Command.CommandType,
			"worker_run_id": record.Command.WorkerRunID, "agent_turn_id": record.Command.AgentTurnID,
			"queue_sequence": queueSequence,
		})
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
			EventID:       record.EventID, EventType: "WorkerUserCommandAccepted.v1",
			ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
			TenantID: record.Command.TenantID, AggregateType: "WorkerSession",
			AggregateID:   record.Command.WorkerSessionID,
			TraceID:       "worker-session/" + record.Command.WorkerSessionID,
			CorrelationID: record.CommandID, CausationID: record.Command.AgentTurnID,
			ClientCommandID: record.Command.IdempotencyKey,
			IdempotencyKey:  "worker-user-command/" + record.Command.IdempotencyKey,
			Visibility:      "user", OccurredAt: record.Now, Payload: payload,
		}
		if err := event.Validate(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,client_command_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
			event.StreamID, event.Sequence, event.TenantID, event.AggregateType, event.AggregateID,
			event.TraceID, event.CorrelationID, event.CausationID, workerNullIfEmpty(event.ClientCommandID),
			event.IdempotencyKey,
			event.Visibility, event.Payload, event.OccurredAt); err != nil {
			return err
		}
		result = application.AcceptedWorkerUserCommand{
			CommandID: record.CommandID, Status: "accepted", QueueSequence: queueSequence,
			WorkerSessionID: record.Command.WorkerSessionID,
		}
		return nil
	})
	return result, err
}

func nextWorkerMessageSequence(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.WorkerUserCommandRecord,
) (uint64, error) {
	if _, err := tx.Exec(ctx, `
INSERT INTO worker_message_queue_positions (worker_session_id,next_sequence,revision,updated_at)
VALUES ($1,1,1,$2) ON CONFLICT DO NOTHING`, record.Command.WorkerSessionID, record.Now); err != nil {
		return 0, err
	}
	var sequence uint64
	if err := tx.QueryRow(ctx, `
UPDATE worker_message_queue_positions
SET next_sequence=next_sequence+1,revision=revision+1,updated_at=$2
WHERE worker_session_id=$1 RETURNING next_sequence-1`, record.Command.WorkerSessionID, record.Now).Scan(&sequence); err != nil {
		return 0, err
	}
	return sequence, nil
}

var _ application.WorkerUserCommandStore = (*WorkerUserCommandStore)(nil)

func workerNullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
