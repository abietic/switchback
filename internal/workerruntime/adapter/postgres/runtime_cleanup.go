package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

type RuntimeCleanupStore struct {
	pool *pgxpool.Pool
}

// ConfirmedRuntimeCleanupIdentity binds the immutable failure snapshot to the
// current Session revision. No field represents caller-asserted confirmation.
type ConfirmedRuntimeCleanupIdentity struct {
	TenantID, WorkerSessionID, RuntimeInstanceID                         string
	Generation, ChannelEpoch                                             uint64
	FailedRuntimeRevision, FailedSessionRevision, CurrentSessionRevision uint64
}

// ReadConfirmedRuntimeCleanup performs only SELECT in the caller's transaction;
// it neither locks nor starts transactions. False means proof is absent; ordinary
// database errors remain errors. Both Task consumption and Worker release use
// this exact existing cleanup lease/diagnostic proof.
func ReadConfirmedRuntimeCleanup(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, identity ConfirmedRuntimeCleanupIdentity) (bool, error) {
	var proven bool
	err := query.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM runtime_instances cleaned
 JOIN scheduled_commands cleanup ON cleanup.scheduled_command_id=cleaned.cleanup_diagnostic->>'scheduled_command_id'
 WHERE cleaned.runtime_instance_id=$3 AND cleaned.worker_session_id=$2
   AND cleaned.generation=$4 AND cleaned.control_channel_epoch=$5
   AND cleaned.status='terminated' AND cleaned.cleanup_confirmed
   AND cleaned.cleanup_diagnostic @> '{"status":"confirmed","fence_status":"confirmed","observation":{"Confirmed":true,"ContainerExists":false,"SecretVolumeExists":false}}'::jsonb
   AND COALESCE(cleaned.cleanup_diagnostic->>'error_code','')=''
   AND cleanup.tenant_id=$1 AND cleanup.command_type='runtime_cleanup_reconcile'
   AND cleanup.target_type='RuntimeInstance' AND cleanup.target_id=$3
   AND cleanup.status IN ('cancelled','leased','applied')
   AND cleanup.idempotency_key=cleaned.cleanup_diagnostic->>'idempotency_key'
   AND cleanup.expected_revision>$6 AND cleaned.revision>cleanup.expected_revision
   AND (cleanup.payload->>'expected_session_revision')::bigint >= $7
   AND (cleanup.payload->>'expected_session_revision')::bigint < $8
   AND cleanup.payload @> jsonb_build_object('tenant_id',$1::text,'worker_session_id',$2::text,
     'runtime_instance_id',$3::text,'runtime_generation',$4::bigint,
     'expected_container_id',cleaned.container_id,'scheduled_command_id',cleanup.scheduled_command_id,
     'idempotency_key',cleanup.idempotency_key,'expected_runtime_revision',cleanup.expected_revision)
)`, identity.TenantID, identity.WorkerSessionID, identity.RuntimeInstanceID, identity.Generation, identity.ChannelEpoch, identity.FailedRuntimeRevision, identity.FailedSessionRevision, identity.CurrentSessionRevision).Scan(&proven)
	return proven, err
}

func NewRuntimeCleanupStore(pool *pgxpool.Pool) *RuntimeCleanupStore {
	return &RuntimeCleanupStore{pool: pool}
}

func (store *RuntimeCleanupStore) BeginRuntimeCleanup(
	ctx context.Context,
	record application.BeginRuntimeCleanupRecord,
) (application.RuntimeCleanupLease, error) {
	var lease application.RuntimeCleanupLease
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		row, err := lockCleanupRuntime(ctx, tx, record.Command.WorkerSessionID, record.Command.RuntimeInstanceID)
		if err != nil {
			return err
		}
		if row.RuntimeStatus == "draining" {
			var diagnostic cleanupDiagnostic
			if err := json.Unmarshal(row.CleanupDiagnostic, &diagnostic); err != nil {
				return fmt.Errorf("decode pending Runtime cleanup: %w", err)
			}
			if diagnostic.IdempotencyKey != record.Command.IdempotencyKey {
				return ErrRuntimeLossConflict
			}
			lease = cleanupLease(row, diagnostic.ScheduledCommandID, record.Command.IdempotencyKey)
			return nil
		}
		recoveryCleanupAllowed := false
		if row.SessionStatus == "waiting_user" {
			if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM runtime_recovery_incidents incident
  WHERE incident.worker_session_id=$1
    AND incident.phase='exhausted'
    AND (incident.failed_runtime_instance_id=$2
      OR incident.replacement_runtime_instance_id=$2)
)`,
				record.Command.WorkerSessionID,
				record.Command.RuntimeInstanceID,
			).Scan(&recoveryCleanupAllowed); err != nil {
				return fmt.Errorf("verify exhausted recovery cleanup authority: %w", err)
			}
		}
		if row.TenantID != record.Command.TenantID ||
			row.SessionRevision != record.Command.ExpectedSessionRevision ||
			row.RuntimeRevision != record.Command.ExpectedRuntimeRevision ||
			row.RuntimeGeneration != record.Command.RuntimeGeneration ||
			row.ContainerID != record.Command.ExpectedContainerID ||
			row.ActiveRuntimeInstanceID != record.Command.RuntimeInstanceID ||
			(row.SessionStatus != "lost" && row.SessionStatus != "closed" &&
				row.SessionStatus != "expired" && !recoveryCleanupAllowed) ||
			(row.RuntimeStatus != "active" && row.RuntimeStatus != "lost" && row.RuntimeStatus != "starting") {
			return platformpostgres.ErrRevisionConflict
		}
		diagnostic := cleanupDiagnostic{
			Status: "pending", IdempotencyKey: record.Command.IdempotencyKey,
			ScheduledCommandID: record.ScheduledCommandID,
		}
		diagnosticJSON, _ := json.Marshal(diagnostic)
		var runtimeRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='draining',cleanup_confirmed=false,cleanup_diagnostic=$2,
    revision=revision+1,ended_at=NULL
WHERE runtime_instance_id=$1 AND revision=$3
RETURNING revision`, record.Command.RuntimeInstanceID, diagnosticJSON,
			record.Command.ExpectedRuntimeRevision).Scan(&runtimeRevision); err != nil {
			return fmt.Errorf("begin Runtime cleanup: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE session_dispatch_permits
SET status='revoked' WHERE worker_session_id=$1 AND status='active'`,
			record.Command.WorkerSessionID); err != nil {
			return fmt.Errorf("revoke cleanup dispatch permits: %w", err)
		}
		lease = application.RuntimeCleanupLease{
			TenantID: record.Command.TenantID, WorkerSessionID: record.Command.WorkerSessionID,
			RuntimeInstanceID:       record.Command.RuntimeInstanceID,
			ExpectedContainerID:     record.Command.ExpectedContainerID,
			RuntimeGeneration:       record.Command.RuntimeGeneration,
			ExpectedSessionRevision: record.Command.ExpectedSessionRevision,
			ExpectedRuntimeRevision: runtimeRevision,
			ScheduledCommandID:      record.ScheduledCommandID,
			IdempotencyKey:          record.Command.IdempotencyKey,
		}
		payload, _ := json.Marshal(lease)
		if _, err := tx.Exec(ctx, `
INSERT INTO scheduled_commands
  (scheduled_command_id,tenant_id,command_type,target_type,target_id,expected_revision,payload,
   due_at,status,idempotency_key,created_at,updated_at)
VALUES ($1,$2,'runtime_cleanup_reconcile','RuntimeInstance',$3,$4,$5,$6,'pending',$7,$8,$8)`,
			record.ScheduledCommandID, record.Command.TenantID, record.Command.RuntimeInstanceID,
			runtimeRevision, payload, record.RetryAt, record.Command.IdempotencyKey, record.Now); err != nil {
			return fmt.Errorf("schedule Runtime cleanup reconciliation: %w", err)
		}
		return nil
	})
	return lease, err
}

func (store *RuntimeCleanupStore) FinishRuntimeCleanup(
	ctx context.Context,
	record application.FinishRuntimeCleanupRecord,
) (application.RuntimeCleanupResult, error) {
	var result application.RuntimeCleanupResult
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		row, err := lockCleanupRuntime(ctx, tx, record.Lease.WorkerSessionID, record.Lease.RuntimeInstanceID)
		if err != nil {
			return err
		}
		if row.TenantID != record.Lease.TenantID || row.RuntimeStatus != "draining" ||
			row.RuntimeRevision != record.Lease.ExpectedRuntimeRevision ||
			row.SessionRevision != record.Lease.ExpectedSessionRevision ||
			row.RuntimeGeneration != record.Lease.RuntimeGeneration ||
			row.ContainerID != record.Lease.ExpectedContainerID {
			return platformpostgres.ErrRevisionConflict
		}
		var pending cleanupDiagnostic
		if err := json.Unmarshal(row.CleanupDiagnostic, &pending); err != nil ||
			pending.IdempotencyKey != record.Lease.IdempotencyKey ||
			pending.ScheduledCommandID != record.Lease.ScheduledCommandID {
			return platformpostgres.ErrRevisionConflict
		}
		if err := validateCleanupObservation(record.Lease, record.Observation); err != nil {
			return err
		}
		confirmed := record.FenceStatus == "confirmed" && record.Observation.Confirmed && record.ErrorCode == "" &&
			!record.Observation.ContainerExists && !record.Observation.SecretVolumeExists
		diagnostic := cleanupDiagnostic{
			Status: "unconfirmed", IdempotencyKey: record.Lease.IdempotencyKey,
			ScheduledCommandID: record.Lease.ScheduledCommandID,
			FenceStatus:        record.FenceStatus, ErrorCode: record.ErrorCode,
			Observation: record.Observation,
		}
		if confirmed {
			diagnostic.Status = "confirmed"
		}
		diagnosticJSON, _ := json.Marshal(diagnostic)
		var runtimeRevision uint64
		if confirmed {
			if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='terminated',cleanup_confirmed=true,cleanup_diagnostic=$2,
    revision=revision+1,ended_at=$3
WHERE runtime_instance_id=$1 AND revision=$4 AND status='draining'
RETURNING revision`, record.Lease.RuntimeInstanceID, diagnosticJSON, record.Now,
				record.Lease.ExpectedRuntimeRevision).Scan(&runtimeRevision); err != nil {
				return fmt.Errorf("confirm Runtime cleanup: %w", err)
			}
			var sessionRevision uint64
			if err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id=NULL,session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1 AND session_revision=$3 AND active_runtime_instance_id=$4
RETURNING session_revision`, record.Lease.WorkerSessionID, record.Now,
				record.Lease.ExpectedSessionRevision, record.Lease.RuntimeInstanceID).Scan(&sessionRevision); err != nil {
				return fmt.Errorf("release cleaned Runtime from WorkerSession: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE scheduled_commands
SET status=CASE WHEN status='pending' THEN 'cancelled' ELSE status END,updated_at=$2
WHERE scheduled_command_id=$1 AND status IN ('pending','leased')`,
				record.Lease.ScheduledCommandID, record.Now); err != nil {
				return fmt.Errorf("cancel Runtime cleanup reconciliation: %w", err)
			}
			result = application.RuntimeCleanupResult{
				Confirmed: true, Observation: record.Observation,
			}
			return nil
		}
		if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET cleanup_confirmed=false,cleanup_diagnostic=$2,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$3 AND status='draining'
RETURNING revision`, record.Lease.RuntimeInstanceID, diagnosticJSON,
			record.Lease.ExpectedRuntimeRevision).Scan(&runtimeRevision); err != nil {
			return fmt.Errorf("record unconfirmed Runtime cleanup: %w", err)
		}
		next := record.Lease
		next.ExpectedRuntimeRevision = runtimeRevision
		payload, _ := json.Marshal(next)
		if _, err := tx.Exec(ctx, `
UPDATE scheduled_commands
SET expected_revision=$2,payload=$3,due_at=$4,updated_at=$5
WHERE scheduled_command_id=$1 AND status IN ('pending','leased')`, record.Lease.ScheduledCommandID,
			runtimeRevision, payload, record.RetryAt, record.Now); err != nil {
			return fmt.Errorf("reschedule Runtime cleanup reconciliation: %w", err)
		}
		result = application.RuntimeCleanupResult{
			Confirmed: false, ErrorCode: record.ErrorCode,
			Observation: record.Observation, Reconciliation: next,
		}
		return nil
	})
	return result, err
}

type cleanupRuntimeRow struct {
	TenantID                string
	WorkerSessionID         string
	RuntimeInstanceID       string
	SessionStatus           string
	SessionRevision         uint64
	ActiveRuntimeInstanceID string
	RuntimeStatus           string
	RuntimeRevision         uint64
	RuntimeGeneration       uint64
	ContainerID             string
	CleanupDiagnostic       json.RawMessage
}

func lockCleanupRuntime(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	runtimeID string,
) (cleanupRuntimeRow, error) {
	var row cleanupRuntimeRow
	var activeRuntimeID, containerID *string
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.worker_session_id,runtime.runtime_instance_id,
  session.status,session.session_revision,session.active_runtime_instance_id,
  runtime.status,runtime.revision,runtime.generation,runtime.container_id,
  COALESCE(runtime.cleanup_diagnostic,'{}'::jsonb)
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.worker_session_id=session.worker_session_id
  AND runtime.runtime_instance_id=$2
WHERE session.worker_session_id=$1
FOR UPDATE OF session,runtime`, sessionID, runtimeID).Scan(
		&row.TenantID, &row.WorkerSessionID, &row.RuntimeInstanceID,
		&row.SessionStatus, &row.SessionRevision, &activeRuntimeID,
		&row.RuntimeStatus, &row.RuntimeRevision, &row.RuntimeGeneration, &containerID,
		&row.CleanupDiagnostic)
	if errors.Is(err, pgx.ErrNoRows) {
		return cleanupRuntimeRow{}, platformpostgres.ErrNotFound
	}
	if err != nil {
		return cleanupRuntimeRow{}, fmt.Errorf("lock Runtime cleanup ownership: %w", err)
	}
	if activeRuntimeID != nil {
		row.ActiveRuntimeInstanceID = *activeRuntimeID
	}
	if containerID != nil {
		row.ContainerID = *containerID
	}
	return row, nil
}

type cleanupDiagnostic struct {
	Status             string                                `json:"status"`
	IdempotencyKey     string                                `json:"idempotency_key"`
	ScheduledCommandID string                                `json:"scheduled_command_id"`
	FenceStatus        string                                `json:"fence_status,omitempty"`
	ErrorCode          string                                `json:"error_code,omitempty"`
	Observation        application.RuntimeCleanupObservation `json:"observation,omitempty"`
}

func cleanupLease(row cleanupRuntimeRow, commandID, idempotencyKey string) application.RuntimeCleanupLease {
	return application.RuntimeCleanupLease{
		TenantID: row.TenantID, WorkerSessionID: row.WorkerSessionID,
		RuntimeInstanceID:   row.RuntimeInstanceID,
		ExpectedContainerID: row.ContainerID, RuntimeGeneration: row.RuntimeGeneration,
		ExpectedSessionRevision: row.SessionRevision, ExpectedRuntimeRevision: row.RuntimeRevision,
		ScheduledCommandID: commandID, IdempotencyKey: idempotencyKey,
	}
}

func validateCleanupObservation(
	lease application.RuntimeCleanupLease,
	observation application.RuntimeCleanupObservation,
) error {
	wantContainer := "switchback-runtime-" + lease.RuntimeInstanceID
	wantVolume := "switchback-runtime-secrets-" + lease.RuntimeInstanceID + "-g" + strconv.FormatUint(lease.RuntimeGeneration, 10)
	if observation.ContainerRef != "" && observation.ContainerRef != wantContainer {
		return fmt.Errorf("Runtime cleanup container identity conflict")
	}
	if observation.SecretVolumeRef != "" && observation.SecretVolumeRef != wantVolume {
		return fmt.Errorf("Runtime cleanup secret volume identity conflict")
	}
	return nil
}

var _ application.RuntimeCleanupStore = (*RuntimeCleanupStore)(nil)
