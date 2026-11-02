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
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

var (
	ErrAllocationNotHead     = errors.New("allocation request is not the FIFO head")
	ErrAllocationUnavailable = errors.New("allocation request is not pending or allocated")
)

type AllocatorStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewAllocatorStore(pool *pgxpool.Pool) *AllocatorStore {
	return &AllocatorStore{pool: pool}
}

func (store *AllocatorStore) AllocateWorkerSession(
	ctx context.Context,
	record application.AllocationRecord,
) (application.AllocationResult, error) {
	if store == nil || store.pool == nil || record.Now.IsZero() {
		return application.AllocationResult{}, fmt.Errorf("PostgreSQL allocator and server time are required")
	}
	var result application.AllocationResult
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		quotaLimit, reservedCount, quotaRevision, err := lockContinuityQuota(ctx, tx, record)
		if err != nil {
			return err
		}
		request, err := lockAllocationRequest(ctx, tx, record.Command)
		if err != nil {
			return err
		}
		if request.Status == "allocated" {
			var reservationRevision uint64
			if err := tx.QueryRow(ctx, `
SELECT quota_reservation_revision FROM worker_sessions
WHERE worker_session_id=$1`, request.AllocatedSessionID).Scan(&reservationRevision); err != nil {
				return fmt.Errorf("read allocated WorkerSession result: %w", err)
			}
			result = application.AllocationResult{
				AllocationRequestID:       record.Command.AllocationRequestID,
				SessionAllocationSequence: request.Sequence,
				WorkerSessionID:           request.AllocatedSessionID,
				QuotaRevision:             reservationRevision,
			}
			return nil
		}
		if request.Status != "pending" {
			return ErrAllocationUnavailable
		}
		if err := requireAllocationHead(ctx, tx, record.Command, request.Sequence); err != nil {
			return err
		}
		if err := validateAllocationOwner(ctx, tx, request); err != nil {
			return err
		}
		if reservedCount >= quotaLimit {
			if _, err := tx.Exec(ctx, `
UPDATE session_allocation_requests
SET allocation_wait_reason='session_capacity',updated_at=$2
WHERE allocation_request_id=$1`, record.Command.AllocationRequestID, record.Now); err != nil {
				return fmt.Errorf("record Session capacity wait: %w", err)
			}
			result = application.AllocationResult{
				AllocationRequestID:       record.Command.AllocationRequestID,
				SessionAllocationSequence: request.Sequence,
				QuotaRevision:             quotaRevision, CapacityFull: true,
			}
			return nil
		}
		newQuotaRevision, err := reserveContinuitySlot(ctx, tx, record, quotaRevision)
		if err != nil {
			return err
		}
		if err := insertWorkerSession(ctx, tx, record, request, newQuotaRevision); err != nil {
			return err
		}
		if err := insertSessionLifecycleCommands(ctx, tx, record); err != nil {
			return err
		}
		commandTag, err := tx.Exec(ctx, `
UPDATE session_allocation_requests
SET status='allocated',allocation_wait_reason=NULL,allocated_worker_session_id=$2,updated_at=$3
WHERE allocation_request_id=$1 AND status='pending'`,
			record.Command.AllocationRequestID, record.IDs.WorkerSessionID, record.Now)
		if err != nil || commandTag.RowsAffected() != 1 {
			return fmt.Errorf("allocate Session request: %w", platformpostgres.ErrRevisionConflict)
		}
		if err := store.appendAllocatedEvent(ctx, tx, record, request, newQuotaRevision); err != nil {
			return err
		}
		result = application.AllocationResult{
			AllocationRequestID:       record.Command.AllocationRequestID,
			SessionAllocationSequence: request.Sequence,
			WorkerSessionID:           record.IDs.WorkerSessionID, QuotaRevision: newQuotaRevision,
		}
		return nil
	})
	return result, err
}

type allocationRequest struct {
	TenantID            string
	UserID              string
	WorkspaceID         string
	WorkspaceRevisionID string
	RequestKind         string
	OwnerID             string
	OwnerRevision       uint64
	ContextPolicy       string
	Sequence            uint64
	Status              string
	AllocatedSessionID  string
}

func lockContinuityQuota(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AllocationRecord,
) (int, int, uint64, error) {
	if _, err := tx.Exec(ctx, `
INSERT INTO worker_session_continuity_quotas
  (tenant_id,user_id,quota_limit,reserved_count,revision,policy_revision,updated_at)
VALUES ($1,$2,10,0,1,'m1-continuity-v1',$3)
ON CONFLICT DO NOTHING`, record.Command.TenantID, record.Command.UserID, record.Now); err != nil {
		return 0, 0, 0, fmt.Errorf("ensure WorkerSession quota: %w", err)
	}
	var limit, reserved int
	var revision uint64
	if err := tx.QueryRow(ctx, `
SELECT quota_limit,reserved_count,revision
FROM worker_session_continuity_quotas
WHERE tenant_id=$1 AND user_id=$2
FOR UPDATE`, record.Command.TenantID, record.Command.UserID).Scan(&limit, &reserved, &revision); err != nil {
		return 0, 0, 0, fmt.Errorf("lock WorkerSession quota: %w", err)
	}
	return limit, reserved, revision, nil
}

func lockAllocationRequest(
	ctx context.Context,
	tx platformpostgres.Tx,
	command application.AllocateWorkerSessionCommand,
) (allocationRequest, error) {
	var request allocationRequest
	var allocatedSessionID *string
	err := tx.QueryRow(ctx, `
SELECT tenant_id,user_id,workspace_id,workspace_revision_id,request_kind,owner_id,owner_revision,
  context_policy,session_allocation_sequence,status,allocated_worker_session_id
FROM session_allocation_requests
WHERE allocation_request_id=$1
FOR UPDATE`, command.AllocationRequestID).Scan(
		&request.TenantID, &request.UserID, &request.WorkspaceID, &request.WorkspaceRevisionID,
		&request.RequestKind, &request.OwnerID, &request.OwnerRevision, &request.ContextPolicy,
		&request.Sequence, &request.Status, &allocatedSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return allocationRequest{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return allocationRequest{}, fmt.Errorf("lock allocation request: %w", err)
	}
	if request.TenantID != command.TenantID || request.UserID != command.UserID {
		return allocationRequest{}, platformpostgres.ErrNotFound
	}
	if allocatedSessionID != nil {
		request.AllocatedSessionID = *allocatedSessionID
	}
	return request, nil
}

func requireAllocationHead(
	ctx context.Context,
	tx platformpostgres.Tx,
	command application.AllocateWorkerSessionCommand,
	sequence uint64,
) error {
	var headID string
	var headSequence uint64
	err := tx.QueryRow(ctx, `
SELECT allocation_request_id,session_allocation_sequence
FROM session_allocation_requests
WHERE tenant_id=$1 AND user_id=$2 AND status='pending'
ORDER BY session_allocation_sequence
LIMIT 1`, command.TenantID, command.UserID).Scan(&headID, &headSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAllocationUnavailable
	}
	if err != nil {
		return fmt.Errorf("read SessionAllocationQueue head: %w", err)
	}
	if headID != command.AllocationRequestID || headSequence != sequence {
		return fmt.Errorf("%w: head=%s sequence=%d", ErrAllocationNotHead, headID, headSequence)
	}
	return nil
}

func validateAllocationOwner(ctx context.Context, tx platformpostgres.Tx, request allocationRequest) error {
	if request.RequestKind != "task" {
		return nil
	}
	var valid bool
	err := tx.QueryRow(ctx, `
SELECT true FROM tasks
WHERE task_id=$1 AND tenant_id=$2 AND workspace_id=$3 AND workspace_revision_id=$4
  AND revision=$5 AND status='queued' AND worker_session_id IS NULL`,
		request.OwnerID, request.TenantID, request.WorkspaceID,
		request.WorkspaceRevisionID, request.OwnerRevision).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAllocationUnavailable
	}
	if err != nil {
		return fmt.Errorf("validate allocation owner: %w", err)
	}
	return nil
}

func reserveContinuitySlot(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AllocationRecord,
	expectedRevision uint64,
) (uint64, error) {
	var revision uint64
	err := tx.QueryRow(ctx, `
UPDATE worker_session_continuity_quotas
SET reserved_count=reserved_count+1,revision=revision+1,updated_at=$4
WHERE tenant_id=$1 AND user_id=$2 AND revision=$3 AND reserved_count < quota_limit
RETURNING revision`, record.Command.TenantID, record.Command.UserID, expectedRevision, record.Now).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, platformpostgres.ErrRevisionConflict
	}
	if err != nil {
		return 0, fmt.Errorf("reserve WorkerSession continuity slot: %w", err)
	}
	return revision, nil
}

func insertWorkerSession(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AllocationRecord,
	request allocationRequest,
	quotaRevision uint64,
) error {
	_, err := tx.Exec(ctx, `
INSERT INTO worker_sessions
  (worker_session_id,tenant_id,user_id,workspace_id,workspace_revision_id,working_copy_id,status,
   session_revision,runtime_generation,quota_reservation_revision,idle_expires_at,absolute_expires_at,
   created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,'allocating',1,0,$7,$8::timestamptz + interval '48 hours',
  $8::timestamptz + interval '14 days',$8,$8)`,
		record.IDs.WorkerSessionID, request.TenantID, request.UserID, request.WorkspaceID,
		request.WorkspaceRevisionID, record.IDs.WorkingCopyID, quotaRevision, record.Now)
	if err != nil {
		return fmt.Errorf("insert allocating WorkerSession: %w", err)
	}
	return nil
}

func insertSessionLifecycleCommands(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AllocationRecord,
) error {
	commands := []struct {
		id          string
		commandType string
		dueAt       time.Time
	}{
		{record.IDs.IdleCommandID, CommandWorkerSessionIdle, record.Now.Add(48 * time.Hour)},
		{record.IDs.AbsoluteCommandID, CommandWorkerSessionAbsolute, record.Now.Add(14 * 24 * time.Hour)},
	}
	for _, command := range commands {
		if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,fencing_token,attempt_count,idempotency_key,created_at,updated_at)
VALUES ($1,$2,$3,'WorkerSession',$4,1,'{}'::jsonb,$5,'pending',0,0,$6,$7,$7)`,
			command.id, record.Command.TenantID, command.commandType, record.IDs.WorkerSessionID,
			command.dueAt, command.commandType+"/"+record.IDs.WorkerSessionID, record.Now); err != nil {
			return fmt.Errorf("insert %s ScheduledCommand: %w", command.commandType, err)
		}
	}
	return nil
}

func (store *AllocatorStore) appendAllocatedEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AllocationRecord,
	request allocationRequest,
	quotaRevision uint64,
) error {
	stream := platformpostgres.OwnerStream{
		StreamID:     "worker-session/" + record.IDs.WorkerSessionID,
		OwnerContext: "worker-runtime", AggregateType: "WorkerSession",
		AggregateID: record.IDs.WorkerSessionID,
	}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		WorkerSessionID           string `json:"worker_session_id"`
		AllocationRequestID       string `json:"allocation_request_id"`
		SessionAllocationSequence uint64 `json:"session_allocation_sequence"`
		QuotaRevision             uint64 `json:"quota_revision"`
	}{record.IDs.WorkerSessionID, record.Command.AllocationRequestID, request.Sequence, quotaRevision})
	if err != nil {
		return err
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.IDs.EventID, EventType: "WorkerSessionAllocated.v1",
		ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: request.TenantID, AggregateType: "WorkerSession",
		AggregateID: record.IDs.WorkerSessionID, TraceID: "worker-session/" + record.IDs.WorkerSessionID,
		CorrelationID: record.Command.AllocationRequestID, CausationID: request.OwnerID,
		IdempotencyKey: "worker-session-allocated/" + record.Command.AllocationRequestID,
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
		event.EventID, event.SchemaVersion, event.EventType, event.ProducerContext, event.StreamID,
		event.Sequence, event.TenantID, event.AggregateType, event.AggregateID, event.TraceID,
		event.CorrelationID, event.CausationID, event.IdempotencyKey, event.Visibility,
		event.Payload, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("insert WorkerSession allocated Outbox event: %w", err)
	}
	return nil
}
