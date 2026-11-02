package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type ApprovalDecisionStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewApprovalDecisionStore(pool *pgxpool.Pool) *ApprovalDecisionStore {
	return &ApprovalDecisionStore{pool: pool}
}

func (store *ApprovalDecisionStore) DecideApproval(
	ctx context.Context,
	record application.ApprovalDecisionRecord,
) (application.ApprovalDecisionResult, error) {
	var result application.ApprovalDecisionResult
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		var existingDigest string
		var grantID *string
		err := tx.QueryRow(ctx, `SELECT decision.command_id,decision.request_digest,grant_row.approval_grant_id
FROM worker_approval_decisions decision
LEFT JOIN worker_approval_grants grant_row ON grant_row.approval_request_id=decision.approval_request_id
WHERE decision.tenant_id=$1 AND decision.user_id=$2 AND decision.idempotency_key=$3`,
			record.Command.TenantID, record.Command.UserID, record.Command.IdempotencyKey).Scan(
			&result.CommandID, &existingDigest, &grantID)
		if err == nil {
			if existingDigest != record.RequestDigest {
				return application.ErrApprovalDecisionIdempotencyConflict
			}
			result.Status = "accepted"
			if grantID != nil {
				result.GrantID = *grantID
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		var tenantID, userID, sessionStatus, runStatus, turnStatus string
		var sessionRevision, runRevision, turnRevision uint64
		err = tx.QueryRow(ctx, `SELECT session.tenant_id,session.user_id,session.status,
       run.status,turn.status,session.session_revision,run.revision,turn.revision
FROM worker_sessions session
JOIN worker_runs run ON run.worker_session_id=session.worker_session_id AND run.worker_run_id=$2
JOIN agent_turns turn ON turn.worker_run_id=run.worker_run_id AND turn.agent_turn_id=$3
WHERE session.worker_session_id=$1 FOR UPDATE OF session,run,turn`,
			record.Command.WorkerSessionID, record.Command.WorkerRunID, record.Command.AgentTurnID).Scan(
			&tenantID, &userID, &sessionStatus, &runStatus, &turnStatus,
			&sessionRevision, &runRevision, &turnRevision)
		if errors.Is(err, pgx.ErrNoRows) || tenantID != record.Command.TenantID || userID != record.Command.UserID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if sessionRevision != record.Command.ExpectedSessionRevision || runRevision != record.Command.ExpectedRunRevision ||
			turnRevision != record.Command.ExpectedTurnRevision ||
			(sessionStatus != "running" && sessionStatus != "waiting_user") ||
			(runStatus != "running" && runStatus != "waiting_user") ||
			(turnStatus != "delivered" && turnStatus != "consumed") {
			return platformpostgres.ErrRevisionConflict
		}

		var requestRevision uint64
		var requestTenant, requestUser, workspaceID, sessionID, runID, turnID string
		var capability, target, effect, status string
		var scopeAllowed bool
		var expiresAt time.Time
		err = tx.QueryRow(ctx, `SELECT tenant_id,user_id,workspace_id,worker_session_id,worker_run_id,
       agent_turn_id,capability,target,effect,status,revision,$2=ANY(scope_options),expires_at
FROM worker_approval_requests WHERE approval_request_id=$1 FOR UPDATE`,
			record.Command.ApprovalRequestID, record.Command.Scope).Scan(
			&requestTenant, &requestUser, &workspaceID, &sessionID, &runID, &turnID,
			&capability, &target, &effect, &status, &requestRevision, &scopeAllowed, &expiresAt)
		_ = workspaceID
		if errors.Is(err, pgx.ErrNoRows) || requestTenant != tenantID || requestUser != userID {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return err
		}
		if sessionID != record.Command.WorkerSessionID || runID != record.Command.WorkerRunID ||
			turnID != record.Command.AgentTurnID || capability != record.Command.Capability ||
			target != record.Command.Target || effect != record.Command.Effect || status != "pending" ||
			requestRevision != record.Command.ExpectedApprovalRevision || !scopeAllowed ||
			!expiresAt.After(record.Now) {
			return platformpostgres.ErrRevisionConflict
		}

		requestStatus := "denied"
		if record.Command.Decision == "approve" {
			requestStatus = "approved"
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_approval_requests
SET status=$2,revision=revision+1,resolved_at=$3
WHERE approval_request_id=$1 AND revision=$4 AND status='pending'`,
			record.Command.ApprovalRequestID, requestStatus, record.Now,
			record.Command.ExpectedApprovalRevision); err != nil {
			return err
		}
		if record.Command.Decision == "approve" {
			if _, err := tx.Exec(ctx, `INSERT INTO worker_approval_grants
  (approval_grant_id,approval_request_id,tenant_id,user_id,worker_session_id,scope,
   capability,target,effect,status,expires_at,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',$10,$11)`, record.GrantID,
				record.Command.ApprovalRequestID, tenantID, userID, record.Command.WorkerSessionID,
				record.Command.Scope, capability, target, effect, expiresAt, record.Now); err != nil {
				return err
			}
			result.GrantID = record.GrantID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO worker_approval_decisions
  (command_id,tenant_id,user_id,approval_request_id,decision,scope,request_digest,idempotency_key,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, record.CommandID, tenantID, userID,
			record.Command.ApprovalRequestID, record.Command.Decision, record.Command.Scope,
			record.RequestDigest, record.Command.IdempotencyKey, record.Now); err != nil {
			return err
		}
		if err := refreshSessionIdleDeadline(ctx, tx, record.Command.WorkerSessionID, sessionRevision, record.Now); err != nil {
			return err
		}
		stream := platformpostgres.OwnerStream{StreamID: "worker-session/" + record.Command.WorkerSessionID,
			OwnerContext: "worker-runtime", AggregateType: "WorkerSession", AggregateID: record.Command.WorkerSessionID}
		sequence, err := store.streams.Next(ctx, tx, stream)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"approval_request_id": record.Command.ApprovalRequestID, "decision": record.Command.Decision,
			"scope": record.Command.Scope, "command_id": record.CommandID,
		})
		event := contractsv1.IntegrationEvent{
			SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: record.EventID,
			EventType: "WorkerApprovalDecided.v1", ProducerContext: "worker-runtime",
			StreamID: stream.StreamID, Sequence: sequence, TenantID: tenantID,
			AggregateType: "WorkerSession", AggregateID: record.Command.WorkerSessionID,
			TraceID: stream.StreamID, CorrelationID: record.CommandID,
			CausationID:     record.Command.ApprovalRequestID,
			ClientCommandID: record.Command.IdempotencyKey,
			IdempotencyKey:  "worker-approval/" + record.Command.IdempotencyKey,
			Visibility:      "user", OccurredAt: record.Now, Payload: payload,
		}
		if err := event.Validate(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO outbox_events
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
		result.CommandID = record.CommandID
		result.Status = "accepted"
		return nil
	})
	return result, err
}

var _ application.ApprovalDecisionStore = (*ApprovalDecisionStore)(nil)
