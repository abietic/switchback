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

type AgentSessionBindingStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewAgentSessionBindingStore(pool *pgxpool.Pool) *AgentSessionBindingStore {
	return &AgentSessionBindingStore{pool: pool}
}

func (store *AgentSessionBindingStore) BindAgentSession(
	ctx context.Context,
	record application.BindAgentSessionRecord,
) (application.BoundAgentSession, error) {
	var result application.BoundAgentSession
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var tenantID, currentAgentSessionID, sessionStatus, runStatus, turnStatus, runtimeStatus string
		var generation, sessionRevision, runRevision, turnRevision, runtimeRevision uint64
		err := tx.QueryRow(ctx, `
SELECT session.tenant_id,turn.agent_session_id,session.status,run.status,turn.status,runtime.status,
       runtime.generation,session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN agent_turns turn ON turn.worker_session_id=session.worker_session_id
  AND turn.worker_run_id=run.worker_run_id AND turn.agent_turn_id=$3
JOIN runtime_instances runtime ON runtime.worker_session_id=session.worker_session_id
  AND runtime.worker_run_id=run.worker_run_id AND runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1
FOR UPDATE OF session,run,turn,runtime`, record.Command.WorkerSessionID,
			record.Command.WorkerRunID, record.Command.AgentTurnID, record.Command.RuntimeInstanceID).Scan(
			&tenantID, &currentAgentSessionID, &sessionStatus, &runStatus, &turnStatus, &runtimeStatus,
			&generation, &sessionRevision, &runRevision, &turnRevision, &runtimeRevision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.TenantID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentAgentSessionID == record.Command.ActualAgentSessionID {
			var eventID string
			err := tx.QueryRow(ctx, `SELECT event_id FROM outbox_events
WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID, record.Command.IdempotencyKey).Scan(&eventID)
			if err != nil {
				return platformpostgres.ErrRevisionConflict
			}
			result = application.BoundAgentSession{
				AgentSessionID: currentAgentSessionID, SessionRevision: sessionRevision,
				RunRevision: runRevision, TurnRevision: turnRevision,
				RuntimeRevision: runtimeRevision, EventID: eventID,
			}
			return nil
		}
		if currentAgentSessionID != record.Command.ReservedAgentSessionID ||
			generation != record.Command.RuntimeGeneration ||
			sessionRevision != record.Command.ExpectedSessionRevision ||
			runRevision != record.Command.ExpectedRunRevision ||
			turnRevision != record.Command.ExpectedTurnRevision ||
			runtimeRevision != record.Command.ExpectedRuntimeRevision ||
			sessionStatus != "running" || runStatus != "starting" ||
			turnStatus != "delivered" || runtimeStatus != "starting" {
			return platformpostgres.ErrRevisionConflict
		}
		if err := tx.QueryRow(ctx, `
UPDATE agent_turns SET agent_session_id=$2,revision=revision+1
WHERE agent_turn_id=$1 AND revision=$3 AND agent_session_id=$4 AND status='delivered'
RETURNING revision`, record.Command.AgentTurnID, record.Command.ActualAgentSessionID,
			record.Command.ExpectedTurnRevision, record.Command.ReservedAgentSessionID).Scan(&turnRevision); err != nil {
			return platformpostgres.ErrRevisionConflict
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
			"worker_run_id":       record.Command.WorkerRunID,
			"agent_turn_id":       record.Command.AgentTurnID,
			"runtime_instance_id": record.Command.RuntimeInstanceID,
			"agent_session_id":    record.Command.ActualAgentSessionID,
			"turn_revision":       turnRevision,
		})
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
			EventID:       record.EventID, EventType: "AgentSessionBound.v1",
			ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
			TenantID: tenantID, AggregateType: "WorkerSession", AggregateID: record.Command.WorkerSessionID,
			TraceID:       "worker-session/" + record.Command.WorkerSessionID,
			CorrelationID: record.Command.IdempotencyKey, CausationID: record.Command.RuntimeInstanceID,
			IdempotencyKey: record.Command.IdempotencyKey, Visibility: "internal",
			OccurredAt: record.Now, Payload: payload,
		}
		if err := event.Validate(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events
  (event_id,schema_version,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,
   aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,visibility,payload,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext,
			event.StreamID, event.Sequence, event.TenantID, event.AggregateType, event.AggregateID,
			event.TraceID, event.CorrelationID, event.CausationID, event.IdempotencyKey,
			event.Visibility, event.Payload, event.OccurredAt); err != nil {
			return fmt.Errorf("insert AgentSession bound Outbox event: %w", err)
		}
		result = application.BoundAgentSession{
			AgentSessionID: record.Command.ActualAgentSessionID, SessionRevision: sessionRevision,
			RunRevision: runRevision, TurnRevision: turnRevision,
			RuntimeRevision: runtimeRevision, EventID: record.EventID,
		}
		return nil
	})
	return result, err
}

var _ application.AgentSessionBindingStore = (*AgentSessionBindingStore)(nil)
