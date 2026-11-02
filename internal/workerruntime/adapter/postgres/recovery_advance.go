package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func (store *RuntimeRecoveryStore) AdvanceRuntimeRecovery(
	ctx context.Context,
	record application.AdvanceRuntimeRecoveryRecord,
) (application.RuntimeRecoveryState, error) {
	var result application.RuntimeRecoveryState
	commandKind, err := recoveryCommandKind(record.Command.TargetPhase)
	if err != nil {
		return result, err
	}
	digest, err := advanceRecoveryCommandDigest(record.Command)
	if err != nil {
		return result, err
	}
	err = platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		replayed, found, err := loadRuntimeRecoveryIdempotency(
			ctx, tx, record.Command.WorkerSessionID, commandKind,
			record.Command.IdempotencyKey, digest,
		)
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}
		if record.Command.TargetPhase == domain.RecoveryExhausted &&
			record.Command.OutcomeReason == "agent_turn_deadline" {
			return application.ErrTurnOutcomeAuthorityClaimed
		}
		var agentTurnID *string
		err = tx.QueryRow(ctx, `
SELECT agent_turn_id
FROM runtime_recovery_incidents
WHERE incident_id=$1 AND worker_session_id=$2 AND tenant_id=$3`,
			record.Command.IncidentID,
			record.Command.WorkerSessionID,
			record.Command.TenantID,
		).Scan(&agentTurnID)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf(
				"read Runtime recovery AgentTurn authority: %w",
				err,
			)
		}
		if agentTurnID != nil {
			if err := lockAgentTurnOutcome(
				ctx,
				tx,
				*agentTurnID,
			); err != nil {
				return err
			}
		}
		var lockedRevision uint64
		err = tx.QueryRow(ctx, `
SELECT revision
FROM runtime_recovery_incidents
WHERE incident_id=$1 AND worker_session_id=$2 AND tenant_id=$3
FOR UPDATE`,
			record.Command.IncidentID,
			record.Command.WorkerSessionID,
			record.Command.TenantID,
		).Scan(&lockedRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return platformpostgres.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Runtime recovery incident: %w", err)
		}
		if lockedRevision != record.Command.ExpectedIncidentRevision {
			return platformpostgres.ErrRevisionConflict
		}
		current, err := scanRuntimeRecoveryState(tx.QueryRow(ctx, runtimeRecoverySelect+`
WHERE incident.incident_id=$1`, record.Command.IncidentID))
		if err != nil {
			return fmt.Errorf("read Runtime recovery incident: %w", err)
		}
		deadlineOwnsOutcome := !current.AgentTurnDeadlineAt.IsZero() &&
			!record.Now.Before(current.AgentTurnDeadlineAt) &&
			(current.SessionAbsoluteExpiresAt.IsZero() ||
				current.AgentTurnDeadlineAt.Before(
					current.SessionAbsoluteExpiresAt,
				))
		if deadlineOwnsOutcome {
			return application.ErrTurnOutcomeAuthorityClaimed
		}
		incident := recoveryIncidentFromState(current)
		transition := domain.RuntimeRecoveryTransition{
			FenceStatus:      record.Command.FenceStatus,
			MountFenceToken:  record.Command.MountFenceToken,
			ContinuityStatus: record.Command.ContinuityStatus,
			OutcomeReason:    record.Command.OutcomeReason,
		}
		if record.Command.TargetPhase == domain.RecoveryProvisioning {
			transition.ReplacementRuntimeInstanceID = record.ReplacementRuntimeInstanceID
			transition.ReplacementGeneration = current.FailedGeneration + 1
			transition.ReplacementChannelEpoch = record.ReplacementChannelEpoch
		}
		next, err := incident.Transition(record.Command.TargetPhase, record.Now, transition)
		if err != nil {
			return err
		}
		switch next.Phase {
		case domain.RecoveryFencing:
			err = store.persistRecoveryFencing(ctx, tx, current, next, record)
		case domain.RecoveryProvisioning:
			err = store.persistRecoveryReplacement(ctx, tx, current, next, record)
		case domain.RecoveryReattaching:
			err = store.persistRecoveryProvisioned(ctx, tx, current, next, record)
		case domain.RecoveryVerifying:
			err = store.persistRecoveryVerifying(ctx, tx, current, next, record)
		case domain.RecoveryRecovered:
			err = store.persistRecoverySucceeded(ctx, tx, current, next, record)
		case domain.RecoveryExhausted:
			err = store.persistRecoveryExhausted(ctx, tx, current, next, record)
		default:
			err = fmt.Errorf("unsupported Runtime recovery phase %q", next.Phase)
		}
		if err != nil {
			return err
		}
		result, err = scanRuntimeRecoveryState(tx.QueryRow(ctx, runtimeRecoverySelect+`
WHERE incident.incident_id=$1`, record.Command.IncidentID))
		if err != nil {
			return fmt.Errorf("read advanced Runtime recovery incident: %w", err)
		}
		result.EventID = record.EventID
		if err := store.appendRuntimeRecoveryPhaseEvent(ctx, tx, record, result); err != nil {
			return err
		}
		if err := insertRuntimeRecoveryIdempotency(
			ctx, tx, result, record.Command.WorkerSessionID, commandKind,
			record.Command.IdempotencyKey, digest, record.Now,
		); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

func (store *RuntimeRecoveryStore) persistRecoveryFencing(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
	record application.AdvanceRuntimeRecoveryRecord,
) error {
	evidence := json.RawMessage(`{"intent":"revoke_control_secret_and_mount_authority"}`)
	if _, err := tx.Exec(ctx, `
INSERT INTO runtime_mount_fences
  (incident_id,failed_runtime_instance_id,failed_generation,fencing_token,status,evidence,
   revision,created_at,updated_at)
VALUES ($1,$2,$3,$4,'pending',$5,1,$6,$6)`,
		current.IncidentID, current.FailedRuntimeInstanceID, current.FailedGeneration,
		current.FailedChannelEpoch+1, evidence, record.Now,
	); err != nil {
		return fmt.Errorf("persist Runtime mount-fence intent: %w", err)
	}
	return updateRecoveryIncident(ctx, tx, next)
}

func (store *RuntimeRecoveryStore) persistRecoveryReplacement(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
	record application.AdvanceRuntimeRecoveryRecord,
) error {
	fenceEvidence, err := json.Marshal(struct {
		ContainerRemoved   bool   `json:"container_removed"`
		SecretLeaseRevoked bool   `json:"secret_lease_revoked"`
		ControlRevoked     bool   `json:"control_revoked"`
		EvidenceRef        string `json:"evidence_ref"`
	}{
		ContainerRemoved: true, SecretLeaseRevoked: true, ControlRevoked: true,
		EvidenceRef: current.EvidenceRef,
	})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE runtime_mount_fences
SET fencing_token=$2,status='confirmed',evidence=$3,revision=revision+1,updated_at=$4
WHERE incident_id=$1 AND status='pending'`,
		current.IncidentID, next.MountFenceToken, fenceEvidence, record.Now,
	)
	if err != nil {
		return fmt.Errorf("confirm Runtime mount fence: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	var failedRuntimeRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='lost',cleanup_confirmed=true,revision=revision+1,ended_at=$2
WHERE runtime_instance_id=$1 AND revision=$3 AND status='unhealthy'
RETURNING revision`,
		current.FailedRuntimeInstanceID, record.Now, current.FailedRuntimeRevision,
	).Scan(&failedRuntimeRevision); err != nil {
		return fmt.Errorf("retire fenced Runtime generation: %w", err)
	}
	provisionDigest, err := replacementProvisionDigest(current, next)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO runtime_instances
  (runtime_instance_id,worker_session_id,worker_run_id,generation,runtime_type,status,
   image_ref,resource_profile_id,network_policy_id,network_policy_revision,
   network_policy_digest,network_enforcement_status,control_channel_epoch,revision,created_at)
SELECT $1,worker_session_id,worker_run_id,$2,runtime_type,'starting',
       image_ref,resource_profile_id,network_policy_id,network_policy_revision,
       network_policy_digest,network_enforcement_status,$3,1,$4
FROM runtime_instances
WHERE runtime_instance_id=$5`,
		next.ReplacementRuntimeInstanceID, next.ReplacementGeneration,
		next.ReplacementChannelEpoch, record.Now, current.FailedRuntimeInstanceID,
	)
	if err != nil {
		return fmt.Errorf("reserve replacement Runtime identity: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO runtime_replacement_intents
  (incident_id,replacement_runtime_instance_id,replacement_generation,
   replacement_channel_epoch,provision_request_digest,status,revision,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,'reserved',1,$6,$6)`,
		current.IncidentID, next.ReplacementRuntimeInstanceID, next.ReplacementGeneration,
		next.ReplacementChannelEpoch, provisionDigest, record.Now,
	); err != nil {
		return fmt.Errorf("persist replacement provision intent: %w", err)
	}
	var sessionRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET active_runtime_instance_id=$2,runtime_generation=$3,
    session_revision=session_revision+1,updated_at=$4
WHERE worker_session_id=$1 AND session_revision=$5 AND status='recovering'
  AND active_runtime_instance_id=$6
RETURNING session_revision`,
		current.WorkerSessionID, next.ReplacementRuntimeInstanceID, next.ReplacementGeneration,
		record.Now, current.SessionRevision, current.FailedRuntimeInstanceID,
	).Scan(&sessionRevision); err != nil {
		return fmt.Errorf("bind replacement Runtime identity: %w", err)
	}
	_ = failedRuntimeRevision
	_ = sessionRevision
	return updateRecoveryIncident(ctx, tx, next)
}

func (store *RuntimeRecoveryStore) persistRecoveryProvisioned(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
	record application.AdvanceRuntimeRecoveryRecord,
) error {
	if err := validateDatabaseIdentity("container_id", record.Command.ContainerID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE runtime_replacement_intents
SET status='provisioned',container_id=$2,revision=revision+1,updated_at=$3
WHERE incident_id=$1 AND status IN ('reserved','provisioning')
  AND replacement_runtime_instance_id=$4`,
		current.IncidentID, record.Command.ContainerID, record.Now,
		current.ReplacementRuntimeInstanceID,
	)
	if err != nil {
		return fmt.Errorf("record provisioned replacement intent: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	tag, err = tx.Exec(ctx, `
UPDATE runtime_instances
SET container_id=$2,revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$3 AND status='starting'
  AND container_id IS NULL`,
		current.ReplacementRuntimeInstanceID, record.Command.ContainerID,
		current.ReplacementRuntimeRevision,
	)
	if err != nil {
		return fmt.Errorf("record replacement container: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	return updateRecoveryIncident(ctx, tx, next)
}

func (store *RuntimeRecoveryStore) persistRecoveryVerifying(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
	record application.AdvanceRuntimeRecoveryRecord,
) error {
	tag, err := tx.Exec(ctx, `
UPDATE runtime_instances
SET status='active',revision=revision+1,started_at=COALESCE(started_at,$2)
WHERE runtime_instance_id=$1 AND revision=$3 AND status='starting'
  AND container_id IS NOT NULL`,
		current.ReplacementRuntimeInstanceID, record.Now,
		current.ReplacementRuntimeRevision,
	)
	if err != nil {
		return fmt.Errorf("activate reattached replacement Runtime: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	return updateRecoveryIncident(ctx, tx, next)
}

func (store *RuntimeRecoveryStore) persistRecoverySucceeded(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
	record application.AdvanceRuntimeRecoveryRecord,
) error {
	var heartbeatCount int
	err := tx.QueryRow(ctx, `
SELECT count(*)
FROM runtime_heartbeat_observations heartbeat
JOIN runtime_instances runtime ON runtime.runtime_instance_id=heartbeat.runtime_instance_id
WHERE heartbeat.runtime_instance_id=$1
  AND heartbeat.runtime_generation=$2
  AND heartbeat.channel_epoch=$3
  AND heartbeat.liveness_status='healthy'
  AND runtime.status='active'
  AND runtime.container_id IS NOT NULL
  AND heartbeat.last_received_at >= runtime.started_at`,
		current.ReplacementRuntimeInstanceID, current.ReplacementGeneration,
		current.ReplacementChannelEpoch,
	).Scan(&heartbeatCount)
	if err != nil {
		return fmt.Errorf("verify replacement heartbeat evidence: %w", err)
	}
	if heartbeatCount != 1 {
		return fmt.Errorf("replacement Runtime health evidence is incomplete")
	}
	tag, err := tx.Exec(ctx, `
UPDATE worker_sessions
SET status=$2,session_revision=session_revision+1,updated_at=$3
WHERE worker_session_id=$1 AND session_revision=$4 AND status='recovering'
  AND active_runtime_instance_id=$5`,
		current.WorkerSessionID, current.PreviousSessionStatus, record.Now,
		current.SessionRevision, current.ReplacementRuntimeInstanceID,
	)
	if err != nil {
		return fmt.Errorf("restore recovered WorkerSession: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	return updateRecoveryIncident(ctx, tx, next)
}

func (store *RuntimeRecoveryStore) persistRecoveryExhausted(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
	record application.AdvanceRuntimeRecoveryRecord,
) error {
	// Exhaustion writes both the Task and graph; take the Task before even the
	// first Runtime UPDATE, then prove the discovered binding under Session lock.
	task, err := lockRuntimeTaskBeforeGraph(ctx, tx, current.WorkerSessionID)
	if err != nil {
		return err
	}
	if _, err := lockLifecycleSession(ctx, tx, current.WorkerSessionID); err != nil {
		return err
	}
	if err := task.revalidate(ctx, tx, current.WorkerSessionID); err != nil {
		return err
	}
	if current.ReplacementRuntimeInstanceID != "" {
		if _, err := tx.Exec(ctx, `
UPDATE runtime_instances
SET status='lost',revision=revision+1,ended_at=COALESCE(ended_at,$2)
WHERE runtime_instance_id=$1
  AND status IN ('starting','active','suspect','unhealthy','draining')`,
			current.ReplacementRuntimeInstanceID, record.Now,
		); err != nil {
			return fmt.Errorf("fence failed replacement Runtime: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE runtime_instances
SET status='lost',revision=revision+1,ended_at=COALESCE(ended_at,$2)
WHERE runtime_instance_id=$1
  AND status IN ('starting','active','suspect','unhealthy','draining')`,
		current.FailedRuntimeInstanceID, record.Now,
	); err != nil {
		return fmt.Errorf("fence failed Runtime after recovery exhaustion: %w", err)
	}
	taskID, taskStatus, taskRevision := task.TaskID, task.Status, task.Revision
	switch next.OutcomeReason {
	case "worker_session_absolute_expired":
		if err := persistRecoveryAbsoluteExpiryPreemption(
			ctx, tx, current, record, taskID, taskStatus, taskRevision,
		); err != nil {
			return err
		}
		return updateRecoveryIncident(ctx, tx, next)
	}
	if current.AgentTurnID != "" {
		if _, err := tx.Exec(ctx, `
UPDATE agent_turns
SET status='failed',revision=revision+1,terminal_at=$2,
    terminal_reason='runtime_recovery_failed'
WHERE agent_turn_id=$1 AND status IN ('accepted','delivered','consumed')`,
			current.AgentTurnID, record.Now,
		); err != nil {
			return fmt.Errorf("fail AgentTurn after recovery exhaustion: %w", err)
		}
	}
	if current.WorkerRunID != "" {
		if _, err := tx.Exec(ctx, `
UPDATE worker_runs
SET status='waiting_user',revision=revision+1
WHERE worker_run_id=$1 AND status IN ('starting','running')`,
			current.WorkerRunID,
		); err != nil {
			return fmt.Errorf("pause WorkerRun after recovery exhaustion: %w", err)
		}
	}
	var sessionRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status='waiting_user',session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1 AND session_revision=$3 AND status='recovering'
RETURNING session_revision`,
		current.WorkerSessionID, record.Now, current.SessionRevision,
	).Scan(&sessionRevision); err != nil {
		return fmt.Errorf("pause WorkerSession after recovery exhaustion: %w", err)
	}
	if err := persistRecoveryWaitingTask(
		ctx, tx, record, taskID, taskStatus, taskRevision, "runtime_recovery_failed",
	); err != nil {
		return err
	}
	_ = sessionRevision
	return updateRecoveryIncident(ctx, tx, next)
}

func persistRecoveryAbsoluteExpiryPreemption(
	ctx context.Context,
	tx platformpostgres.Tx,
	current application.RuntimeRecoveryState,
	record application.AdvanceRuntimeRecoveryRecord,
	taskID string,
	taskStatus string,
	taskRevision uint64,
) error {
	if current.AgentTurnID != "" {
		if _, err := tx.Exec(ctx, `
UPDATE agent_turns
SET status='timed_out',revision=revision+1,terminal_at=$2,
    terminal_reason='worker_session_absolute_expired'
WHERE agent_turn_id=$1 AND status IN ('accepted','delivered','consumed')`,
			current.AgentTurnID, record.Now,
		); err != nil {
			return fmt.Errorf("time out AgentTurn after WorkerSession expiry: %w", err)
		}
	}
	if current.WorkerRunID != "" {
		if _, err := tx.Exec(ctx, `
UPDATE worker_runs
SET status='timed_out',revision=revision+1,terminal_at=$2,
    terminal_reason='worker_session_absolute_expired'
WHERE worker_run_id=$1 AND status IN ('starting','running','waiting_user')`,
			current.WorkerRunID, record.Now,
		); err != nil {
			return fmt.Errorf("time out WorkerRun after WorkerSession expiry: %w", err)
		}
	}
	if taskID != "" {
		if _, err := tx.Exec(ctx, `
UPDATE tasks
SET status=CASE WHEN status='delivered' THEN status
                WHEN status IN ('completed','failed','cancelled') THEN status
                ELSE 'waiting_user' END,
    waiting_reason=CASE WHEN status='delivered' OR status IN ('completed','failed','cancelled')
                        THEN waiting_reason
                        ELSE '{"code":"worker_session_expired"}'::jsonb END,
    current_worker_run_id=NULL,worker_session_id=NULL,revision=revision+1,updated_at=$2
WHERE task_id=$1 AND revision=$3`,
			taskID, record.Now, taskRevision,
		); err != nil {
			return fmt.Errorf("detach Task after recovery expiry: %w", err)
		}
		if taskStatus != "delivered" && taskStatus != "completed" &&
			taskStatus != "failed" && taskStatus != "cancelled" &&
			taskStatus != "waiting_user" {
			var resultRevision uint64
			if err := tx.QueryRow(ctx, `
SELECT revision FROM tasks WHERE task_id=$1`, taskID).Scan(&resultRevision); err != nil {
				return err
			}
			reason := json.RawMessage(`{"code":"worker_session_expired"}`)
			if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,
   from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,'waiting_user','worker_session_absolute_expired',$7,$8)`,
				record.TaskTransitionID, taskID, record.Command.IdempotencyKey,
				taskRevision, resultRevision, taskStatus, reason, record.Now,
			); err != nil {
				return fmt.Errorf("insert recovery expiry TaskTransition: %w", err)
			}
		}
	}
	tag, err := tx.Exec(ctx, `
UPDATE worker_sessions
SET status='expired',bound_task_id=NULL,active_worker_run_id=NULL,
    session_revision=session_revision+1,terminal_at=$2,
    terminal_reason='worker_session_absolute_expired',updated_at=$2
WHERE worker_session_id=$1 AND session_revision=$3 AND status='recovering'`,
		current.WorkerSessionID, record.Now, current.SessionRevision,
	)
	if err != nil {
		return fmt.Errorf("expire WorkerSession during Runtime recovery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	tag, err = tx.Exec(ctx, `
UPDATE worker_session_continuity_quotas quota
SET reserved_count=reserved_count-1,revision=revision+1,updated_at=$3
WHERE quota.tenant_id=$1
  AND quota.user_id=(SELECT user_id FROM worker_sessions WHERE worker_session_id=$2)
  AND reserved_count > 0`,
		current.TenantID, current.WorkerSessionID, record.Now,
	)
	if err != nil {
		return fmt.Errorf("release expired WorkerSession continuity slot: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf(
			"release expired WorkerSession continuity slot: %w",
			platformpostgres.ErrRevisionConflict,
		)
	}
	return nil
}

func persistRecoveryWaitingTask(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AdvanceRuntimeRecoveryRecord,
	taskID string,
	taskStatus string,
	taskRevision uint64,
	reasonCode string,
) error {
	if taskID == "" || taskStatus == "delivered" ||
		taskStatus == "completed" || taskStatus == "failed" || taskStatus == "cancelled" ||
		taskStatus == "waiting_user" {
		return nil
	}
	reason, err := json.Marshal(struct {
		Code string `json:"code"`
	}{Code: reasonCode})
	if err != nil {
		return err
	}
	var nextTaskRevision uint64
	if err := tx.QueryRow(ctx, `
UPDATE tasks
SET status='waiting_user',waiting_reason=$2,revision=revision+1,updated_at=$3
WHERE task_id=$1 AND revision=$4
RETURNING revision`,
		taskID, reason, record.Now, taskRevision,
	).Scan(&nextTaskRevision); err != nil {
		return fmt.Errorf("pause Task after Runtime recovery: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO task_transitions
  (transition_id,task_id,idempotency_key,expected_revision,result_revision,
   from_state,to_state,trigger,result,occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,'waiting_user',$7,$8,$9)`,
		record.TaskTransitionID, taskID, record.Command.IdempotencyKey,
		taskRevision, nextTaskRevision, taskStatus, reasonCode, reason, record.Now,
	); err != nil {
		return fmt.Errorf("insert recovery TaskTransition: %w", err)
	}
	return nil
}

func updateRecoveryIncident(
	ctx context.Context,
	tx platformpostgres.Tx,
	next domain.RuntimeRecoveryIncident,
) error {
	tag, err := tx.Exec(ctx, `
UPDATE runtime_recovery_incidents
SET phase=$2,attempt_count=$3,replacement_runtime_instance_id=$4,
    replacement_generation=$5,replacement_channel_epoch=$6,fence_status=$7,
    mount_fence_token=$8,continuity_status=$9,outcome=NULLIF($10,''),
    outcome_reason=NULLIF($11,''),revision=$12,updated_at=$13,closed_at=$14
WHERE incident_id=$1 AND revision=$15`,
		next.IncidentID, next.Phase, next.AttemptCount,
		nullableText(next.ReplacementRuntimeInstanceID), nullableUint64(next.ReplacementGeneration),
		nullableUint64(next.ReplacementChannelEpoch), next.FenceStatus,
		nullableUint64(next.MountFenceToken), next.ContinuityStatus, next.Outcome,
		next.OutcomeReason, next.Revision, next.UpdatedAt, next.ClosedAt, next.Revision-1,
	)
	if err != nil {
		return fmt.Errorf("advance Runtime recovery incident: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return platformpostgres.ErrRevisionConflict
	}
	return nil
}

func (store *RuntimeRecoveryStore) appendRuntimeRecoveryPhaseEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.AdvanceRuntimeRecoveryRecord,
	state application.RuntimeRecoveryState,
) error {
	eventType, err := recoveryEventType(state.Phase)
	if err != nil {
		return err
	}
	stream := platformpostgres.OwnerStream{
		StreamID: "worker-session/" + state.WorkerSessionID, OwnerContext: "worker-runtime",
		AggregateType: "WorkerSession", AggregateID: state.WorkerSessionID,
	}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		IncidentID              string                                 `json:"incident_id"`
		Phase                   domain.RuntimeRecoveryPhase            `json:"phase"`
		FailedRuntimeInstanceID string                                 `json:"failed_runtime_instance_id"`
		FailedGeneration        uint64                                 `json:"failed_generation"`
		ReplacementRuntimeID    string                                 `json:"replacement_runtime_instance_id,omitempty"`
		ReplacementGeneration   uint64                                 `json:"replacement_generation,omitempty"`
		AttemptCount            uint32                                 `json:"attempt_count"`
		FenceStatus             domain.RuntimeRecoveryFenceStatus      `json:"fence_status"`
		ContinuityStatus        domain.RuntimeRecoveryContinuityStatus `json:"continuity_status"`
		EvidenceAt              string                                 `json:"evidence_at"`
		OutcomeReason           string                                 `json:"outcome_reason,omitempty"`
	}{
		IncidentID: state.IncidentID, Phase: state.Phase,
		FailedRuntimeInstanceID: state.FailedRuntimeInstanceID,
		FailedGeneration:        state.FailedGeneration,
		ReplacementRuntimeID:    state.ReplacementRuntimeInstanceID,
		ReplacementGeneration:   state.ReplacementGeneration,
		AttemptCount:            state.AttemptCount, FenceStatus: state.FenceStatus,
		ContinuityStatus: state.ContinuityStatus,
		EvidenceAt:       state.EvidenceAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		OutcomeReason:    state.OutcomeReason,
	})
	if err != nil {
		return err
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.EventID, EventType: eventType,
		ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: state.TenantID, AggregateType: "WorkerSession",
		AggregateID: state.WorkerSessionID, TraceID: "worker-session/" + state.WorkerSessionID,
		CorrelationID: state.IncidentID, CausationID: state.FailedRuntimeInstanceID,
		IdempotencyKey: record.Command.IdempotencyKey, Visibility: "internal",
		OccurredAt: record.Now, Payload: payload,
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
		event.Visibility, event.Payload, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("insert Runtime recovery phase Outbox event: %w", err)
	}
	return nil
}

func recoveryIncidentFromState(state application.RuntimeRecoveryState) domain.RuntimeRecoveryIncident {
	return domain.RuntimeRecoveryIncident{
		IncidentID: state.IncidentID, TenantID: state.TenantID,
		WorkerSessionID: state.WorkerSessionID, WorkerRunID: state.WorkerRunID,
		AgentTurnID: state.AgentTurnID, FailedRuntimeInstanceID: state.FailedRuntimeInstanceID,
		FailedGeneration: state.FailedGeneration, FailedChannelEpoch: state.FailedChannelEpoch,
		Trigger: state.Trigger, Phase: state.Phase, AttemptCount: state.AttemptCount,
		ReplacementRuntimeInstanceID: state.ReplacementRuntimeInstanceID,
		ReplacementGeneration:        state.ReplacementGeneration,
		ReplacementChannelEpoch:      state.ReplacementChannelEpoch,
		FenceStatus:                  state.FenceStatus, MountFenceToken: state.MountFenceToken,
		ContinuityStatus: state.ContinuityStatus, PreviousSessionStatus: state.PreviousSessionStatus,
		EvidenceRef: state.EvidenceRef, EvidenceAt: state.EvidenceAt,
		Outcome: state.Outcome, OutcomeReason: state.OutcomeReason, Revision: state.IncidentRevision,
		OpenedAt: state.OpenedAt, UpdatedAt: state.UpdatedAt, ClosedAt: state.ClosedAt,
	}
}

func recoveryCommandKind(phase domain.RuntimeRecoveryPhase) (string, error) {
	switch phase {
	case domain.RecoveryFencing:
		return "begin_fencing", nil
	case domain.RecoveryProvisioning:
		return "reserve_replacement", nil
	case domain.RecoveryReattaching:
		return "record_provisioned", nil
	case domain.RecoveryVerifying:
		return "record_reattached", nil
	case domain.RecoveryRecovered:
		return "recover", nil
	case domain.RecoveryExhausted:
		return "exhaust", nil
	default:
		return "", fmt.Errorf("unsupported Runtime recovery phase %q", phase)
	}
}

func recoveryEventType(phase domain.RuntimeRecoveryPhase) (string, error) {
	switch phase {
	case domain.RecoveryFencing:
		return "RuntimeRecoveryFencingStarted.v1", nil
	case domain.RecoveryProvisioning:
		return "RuntimeRecoveryReplacementReserved.v1", nil
	case domain.RecoveryReattaching:
		return "RuntimeRecoveryReplacementProvisioned.v1", nil
	case domain.RecoveryVerifying:
		return "RuntimeRecoveryVerifying.v1", nil
	case domain.RecoveryRecovered:
		return "RuntimeRecoverySucceeded.v1", nil
	case domain.RecoveryExhausted:
		return "RuntimeRecoveryFailed.v1", nil
	default:
		return "", fmt.Errorf("unsupported Runtime recovery phase %q", phase)
	}
}

func replacementProvisionDigest(
	current application.RuntimeRecoveryState,
	next domain.RuntimeRecoveryIncident,
) (string, error) {
	payload, err := json.Marshal(struct {
		IncidentID         string `json:"incident_id"`
		WorkerSessionID    string `json:"worker_session_id"`
		FailedRuntimeID    string `json:"failed_runtime_instance_id"`
		ReplacementRuntime string `json:"replacement_runtime_instance_id"`
		Generation         uint64 `json:"generation"`
		ChannelEpoch       uint64 `json:"channel_epoch"`
	}{
		IncidentID: current.IncidentID, WorkerSessionID: current.WorkerSessionID,
		FailedRuntimeID:    current.FailedRuntimeInstanceID,
		ReplacementRuntime: next.ReplacementRuntimeInstanceID,
		Generation:         next.ReplacementGeneration, ChannelEpoch: next.ReplacementChannelEpoch,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func advanceRecoveryCommandDigest(command application.AdvanceRuntimeRecoveryCommand) (string, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func nullableUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func validateDatabaseIdentity(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be canonical", name)
	}
	return nil
}
