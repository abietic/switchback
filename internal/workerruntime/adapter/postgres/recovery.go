package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

var ErrRuntimeRecoveryConflict = errors.New("Runtime recovery idempotency conflict")

type RuntimeRecoveryStore struct {
	pool    *pgxpool.Pool
	streams platformpostgres.OwnerStreamRepository
}

func NewRuntimeRecoveryStore(pool *pgxpool.Pool) *RuntimeRecoveryStore {
	return &RuntimeRecoveryStore{pool: pool}
}

func (store *RuntimeRecoveryStore) FindActiveRuntimeRecovery(
	ctx context.Context,
	tenantID string,
) (application.RuntimeRecoveryState, bool, error) {
	state, err := scanRuntimeRecoveryState(store.pool.QueryRow(ctx, runtimeRecoverySelect+`
WHERE incident.tenant_id=$1 AND incident.phase NOT IN ('recovered','exhausted')
ORDER BY incident.updated_at,incident.incident_id
LIMIT 1`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	if err != nil {
		return application.RuntimeRecoveryState{}, false, fmt.Errorf("find active Runtime recovery: %w", err)
	}
	return state, true, nil
}

func (store *RuntimeRecoveryStore) OpenRuntimeRecovery(
	ctx context.Context,
	record application.OpenRuntimeRecoveryRecord,
) (application.RuntimeRecoveryState, error) {
	var result application.RuntimeRecoveryState
	commandDigest, err := runtimeRecoveryCommandDigest(record.Command)
	if err != nil {
		return result, err
	}
	err = platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		if record.Command.AgentTurnID != "" {
			if err := lockAgentTurnOutcome(
				ctx,
				tx,
				record.Command.AgentTurnID,
			); err != nil {
				return err
			}
		}
		replayed, found, err := loadRuntimeRecoveryIdempotency(
			ctx,
			tx,
			record.Command.WorkerSessionID,
			"open",
			record.Command.IdempotencyKey,
			commandDigest,
		)
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}

		existing, found, err := loadRuntimeRecoveryByFailedGeneration(
			ctx,
			tx,
			record.Command.WorkerSessionID,
			record.Command.FailedGeneration,
		)
		if err != nil {
			return err
		}
		if found {
			if existing.TenantID != record.Command.TenantID ||
				existing.FailedRuntimeInstanceID != record.Command.FailedRuntimeInstanceID {
				return ErrRuntimeRecoveryConflict
			}
			if err := insertRuntimeRecoveryIdempotency(
				ctx,
				tx,
				existing,
				record.Command.WorkerSessionID,
				"open",
				record.Command.IdempotencyKey,
				commandDigest,
				record.Now,
			); err != nil {
				return err
			}
			result = existing
			return nil
		}

		if record.Command.AgentTurnID != "" {
			if err := assertNoCompetingTurnOutcomeAuthority(
				ctx,
				tx,
				record.Command.AgentTurnID,
			); err != nil {
				return err
			}
		}

		task, err := lockRuntimeTaskBeforeGraph(ctx, tx, record.Command.WorkerSessionID)
		if err != nil {
			return err
		}
		var row residentRuntimeRow
		var activeRuntimeID string
		if record.Command.WorkerRunID == "" {
			row, activeRuntimeID, err = lockIdleRecoveryRuntime(
				ctx,
				tx,
				record.Command.WorkerSessionID,
				record.Command.FailedRuntimeInstanceID,
			)
		} else {
			row, err = lockResidentRuntime(
				ctx,
				tx,
				record.Command.WorkerSessionID,
				record.Command.WorkerRunID,
				record.Command.AgentTurnID,
				record.Command.FailedRuntimeInstanceID,
			)
			activeRuntimeID = record.Command.FailedRuntimeInstanceID
		}
		if err != nil {
			return err
		}
		if err := task.revalidate(ctx, tx, record.Command.WorkerSessionID); err != nil {
			return err
		}
		if activeRuntimeID != record.Command.FailedRuntimeInstanceID ||
			row.TenantID != record.Command.TenantID ||
			row.RuntimeGeneration != record.Command.FailedGeneration ||
			row.ControlChannelEpoch != record.Command.FailedChannelEpoch ||
			row.SessionRevision != record.Command.ExpectedSessionRevision ||
			row.RunRevision != record.Command.ExpectedRunRevision ||
			row.TurnRevision != record.Command.ExpectedTurnRevision ||
			row.RuntimeRevision != record.Command.ExpectedRuntimeRevision ||
			!runtimeStatusCanRecover(row.RuntimeStatus) {
			return platformpostgres.ErrRevisionConflict
		}
		if _, err := row.SessionStatus.Transition(domain.SessionRecovering); err != nil {
			return err
		}
		incident, err := domain.OpenRuntimeRecoveryIncident(domain.OpenRuntimeRecoveryIncidentCommand{
			IncidentID: record.IncidentID, TenantID: record.Command.TenantID,
			WorkerSessionID: record.Command.WorkerSessionID, WorkerRunID: record.Command.WorkerRunID,
			AgentTurnID:             record.Command.AgentTurnID,
			FailedRuntimeInstanceID: record.Command.FailedRuntimeInstanceID,
			FailedGeneration:        record.Command.FailedGeneration, FailedChannelEpoch: record.Command.FailedChannelEpoch,
			Trigger: record.Command.Trigger, PreviousSessionStatus: row.SessionStatus,
			EvidenceRef: record.Command.EvidenceRef, EvidenceAt: record.Command.EvidenceAt, OpenedAt: record.Now,
		})
		if err != nil {
			return err
		}
		taskStatus, taskRevision := task.Status, task.Revision
		if _, err := tx.Exec(ctx, `
UPDATE session_dispatch_permits
SET status='revoked'
WHERE worker_session_id=$1 AND status='active'`, record.Command.WorkerSessionID); err != nil {
			return fmt.Errorf("revoke dispatch permits for Runtime recovery: %w", err)
		}
		var runtimeRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE runtime_instances
SET status='unhealthy',revision=revision+1
WHERE runtime_instance_id=$1 AND revision=$2 AND status=$3
RETURNING revision`, record.Command.FailedRuntimeInstanceID,
			record.Command.ExpectedRuntimeRevision, row.RuntimeStatus).Scan(&runtimeRevision); err != nil {
			return fmt.Errorf("mark Runtime unhealthy for recovery: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE runtime_heartbeat_observations
SET liveness_status='unhealthy',revision=revision+1
WHERE runtime_instance_id=$1 AND runtime_generation=$2
  AND liveness_status IN ('healthy','suspect')`,
			record.Command.FailedRuntimeInstanceID,
			record.Command.FailedGeneration,
		); err != nil {
			return fmt.Errorf("mark Runtime heartbeat unhealthy for recovery: %w", err)
		}
		var sessionRevision uint64
		if err := tx.QueryRow(ctx, `
UPDATE worker_sessions
SET status='recovering',session_revision=session_revision+1,updated_at=$2
WHERE worker_session_id=$1 AND session_revision=$3 AND status=$4
  AND active_runtime_instance_id=$5
RETURNING session_revision`, record.Command.WorkerSessionID, record.Now,
			record.Command.ExpectedSessionRevision, row.SessionStatus,
			record.Command.FailedRuntimeInstanceID).Scan(&sessionRevision); err != nil {
			return fmt.Errorf("mark WorkerSession recovering: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO runtime_recovery_incidents
  (incident_id,tenant_id,worker_session_id,worker_run_id,agent_turn_id,
   failed_runtime_instance_id,failed_generation,failed_channel_epoch,trigger,phase,
   previous_session_status,attempt_count,fence_status,continuity_status,evidence_ref,evidence_at,
   revision,opened_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,$12,$13,$14,$15,1,$16,$16)`,
			incident.IncidentID, incident.TenantID, incident.WorkerSessionID,
			nullableText(incident.WorkerRunID), nullableText(incident.AgentTurnID),
			incident.FailedRuntimeInstanceID, incident.FailedGeneration, incident.FailedChannelEpoch,
			incident.Trigger, incident.Phase, incident.PreviousSessionStatus, incident.FenceStatus,
			incident.ContinuityStatus, incident.EvidenceRef, incident.EvidenceAt, incident.OpenedAt,
		); err != nil {
			return fmt.Errorf("insert Runtime recovery incident: %w", err)
		}
		result = runtimeRecoveryState(incident)
		result.SessionRevision = sessionRevision
		result.RunRevision = row.RunRevision
		result.TurnRevision = row.TurnRevision
		result.FailedRuntimeRevision = runtimeRevision
		result.TaskStatus = taskStatus
		result.TaskRevision = taskRevision
		result.EventID = record.EventID
		if err := store.appendRuntimeRecoveryStartedEvent(ctx, tx, record, result); err != nil {
			return err
		}
		if err := insertRuntimeRecoveryIdempotency(
			ctx,
			tx,
			result,
			record.Command.WorkerSessionID,
			"open",
			record.Command.IdempotencyKey,
			commandDigest,
			record.Now,
		); err != nil {
			return err
		}
		return nil
	})
	return result, err
}

// FindTerminalRuntimeFailureForTurn is an exact durable handoff, including after
// Backend restart. Historical failures for another Turn or epoch cannot settle
// the caller's current execution.
func (store *RuntimeRecoveryStore) FindTerminalRuntimeFailureForTurn(ctx context.Context, tenantID, sessionID, runID, turnID, runtimeID string, generation, epoch uint64) (application.RuntimeRecoveryState, bool, error) {
	state, err := scanRuntimeRecoveryState(store.pool.QueryRow(ctx, runtimeRecoverySelect+`
WHERE incident.tenant_id=$1 AND incident.worker_session_id=$2
 AND incident.worker_run_id=$3 AND incident.agent_turn_id=$4
 AND incident.failed_runtime_instance_id=$5 AND incident.failed_generation=$6
 AND incident.failed_channel_epoch=$7 AND incident.phase='exhausted'
 AND turn.status='failed' AND turn.terminal_reason='runtime_recovery_failed'`, tenantID, sessionID, runID, turnID, runtimeID, generation, epoch))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	if err != nil {
		return application.RuntimeRecoveryState{}, false, fmt.Errorf("read terminal Runtime failure: %w", err)
	}
	return state, true, nil
}

func (store *RuntimeRecoveryStore) appendRuntimeRecoveryStartedEvent(
	ctx context.Context,
	tx platformpostgres.Tx,
	record application.OpenRuntimeRecoveryRecord,
	state application.RuntimeRecoveryState,
) error {
	stream := platformpostgres.OwnerStream{
		StreamID: "worker-session/" + record.Command.WorkerSessionID, OwnerContext: "worker-runtime",
		AggregateType: "WorkerSession", AggregateID: record.Command.WorkerSessionID,
	}
	sequence, err := store.streams.Next(ctx, tx, stream)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		IncidentID              string                        `json:"incident_id"`
		Phase                   domain.RuntimeRecoveryPhase   `json:"phase"`
		Trigger                 domain.RuntimeRecoveryTrigger `json:"trigger"`
		FailedRuntimeInstanceID string                        `json:"failed_runtime_instance_id"`
		FailedGeneration        uint64                        `json:"failed_generation"`
		AttemptCount            uint32                        `json:"attempt_count"`
		EvidenceAt              string                        `json:"evidence_at"`
	}{
		IncidentID: state.IncidentID, Phase: state.Phase, Trigger: record.Command.Trigger,
		FailedRuntimeInstanceID: state.FailedRuntimeInstanceID,
		FailedGeneration:        state.FailedGeneration, AttemptCount: state.AttemptCount,
		EvidenceAt: state.EvidenceAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
	})
	if err != nil {
		return err
	}
	causationID := record.Command.WorkerRunID
	if causationID == "" {
		causationID = record.Command.FailedRuntimeInstanceID
	}
	event := contractsv1.IntegrationEvent{
		SchemaVersion: contractsv1.IntegrationEventSchemaVersion,
		EventID:       record.EventID, EventType: "RuntimeRecoveryStarted.v1",
		ProducerContext: "worker-runtime", StreamID: stream.StreamID, Sequence: sequence,
		TenantID: record.Command.TenantID, AggregateType: "WorkerSession",
		AggregateID:   record.Command.WorkerSessionID,
		TraceID:       "worker-session/" + record.Command.WorkerSessionID,
		CorrelationID: state.IncidentID, CausationID: causationID,
		IdempotencyKey: record.Command.IdempotencyKey,
		Visibility:     "internal", OccurredAt: record.Now, Payload: payload,
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
		event.Visibility, event.Payload, event.OccurredAt,
	); err != nil {
		return fmt.Errorf("insert Runtime recovery Outbox event: %w", err)
	}
	return nil
}

func loadRuntimeRecoveryIdempotency(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	commandKind string,
	idempotencyKey string,
	commandDigest string,
) (application.RuntimeRecoveryState, bool, error) {
	var storedDigest string
	var resultJSON []byte
	err := tx.QueryRow(ctx, `
SELECT command_digest,result
FROM runtime_recovery_idempotency_results
WHERE worker_session_id=$1 AND command_kind=$2 AND idempotency_key=$3`,
		sessionID, commandKind, idempotencyKey).Scan(&storedDigest, &resultJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	if err != nil {
		return application.RuntimeRecoveryState{}, false, fmt.Errorf("read Runtime recovery idempotency: %w", err)
	}
	if storedDigest != commandDigest {
		return application.RuntimeRecoveryState{}, false, ErrRuntimeRecoveryConflict
	}
	var state application.RuntimeRecoveryState
	if err := json.Unmarshal(resultJSON, &state); err != nil {
		return application.RuntimeRecoveryState{}, false, fmt.Errorf("decode Runtime recovery idempotency result: %w", err)
	}
	return state, true, nil
}

func insertRuntimeRecoveryIdempotency(
	ctx context.Context,
	tx platformpostgres.Tx,
	state application.RuntimeRecoveryState,
	sessionID string,
	commandKind string,
	idempotencyKey string,
	commandDigest string,
	createdAt time.Time,
) error {
	resultJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO runtime_recovery_idempotency_results
  (incident_id,worker_session_id,command_kind,idempotency_key,command_digest,result,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		state.IncidentID, sessionID, commandKind, idempotencyKey, commandDigest, resultJSON, createdAt)
	if err != nil {
		return fmt.Errorf("insert Runtime recovery idempotency result: %w", err)
	}
	return nil
}

func loadRuntimeRecoveryByFailedGeneration(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	failedGeneration uint64,
) (application.RuntimeRecoveryState, bool, error) {
	state, err := scanRuntimeRecoveryState(tx.QueryRow(ctx, runtimeRecoverySelect+`
WHERE incident.worker_session_id=$1 AND incident.failed_generation=$2`,
		sessionID, failedGeneration))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuntimeRecoveryState{}, false, nil
	}
	if err != nil {
		return application.RuntimeRecoveryState{}, false, err
	}
	return state, true, nil
}

const runtimeRecoverySelect = `
SELECT incident.incident_id,incident.tenant_id,incident.worker_session_id,
       COALESCE(incident.worker_run_id,''),COALESCE(incident.agent_turn_id,''),
       incident.failed_runtime_instance_id,incident.failed_generation,incident.failed_channel_epoch,
       incident.trigger,incident.phase,incident.attempt_count,
       COALESCE(incident.replacement_runtime_instance_id,''),
       COALESCE(incident.replacement_generation,0),COALESCE(incident.replacement_channel_epoch,0),
       incident.fence_status,COALESCE(incident.mount_fence_token,0),incident.continuity_status,
       incident.previous_session_status,incident.evidence_ref,incident.evidence_at,
       COALESCE(incident.outcome,''),COALESCE(incident.outcome_reason,''),incident.revision,
       session.session_revision,
       COALESCE(run.revision,0),COALESCE(turn.revision,0),failed_runtime.revision,
       COALESCE(replacement_runtime.revision,0),
       COALESCE(replacement_runtime.container_id,''),
       COALESCE(task.status,''),COALESCE(task.revision,0),
       COALESCE(failed_runtime.worker_run_id,''),
       COALESCE(turn.agent_session_id,(
         SELECT previous_turn.agent_session_id
         FROM agent_turns previous_turn
         WHERE previous_turn.worker_run_id=failed_runtime.worker_run_id
         ORDER BY previous_turn.created_at DESC,previous_turn.agent_turn_id DESC
         LIMIT 1
       ),''),
       failed_runtime.image_ref,failed_runtime.resource_profile_id,
       COALESCE(failed_run.agent_profile_id,''),
       COALESCE(failed_run.agent_profile_revision,''),
       COALESCE(failed_run.agent_profile_digest,''),
       failed_runtime.network_policy_id,failed_runtime.network_policy_revision,
       failed_runtime.network_policy_digest,failed_runtime.network_enforcement_status,
       COALESCE(failed_run.start_snapshot->>'working_copy_checkpoint_digest',''),
       turn.deadline_at,
       COALESCE((
         SELECT command.scheduled_command_id
         FROM scheduled_commands command
         WHERE command.tenant_id=incident.tenant_id
           AND command.command_type='agent_turn_deadline'
           AND command.target_type='AgentTurn'
           AND command.target_id=turn.agent_turn_id
           AND command.due_at=turn.deadline_at
           AND command.idempotency_key='agent-turn-deadline/' || turn.agent_turn_id
         ORDER BY command.created_at,command.scheduled_command_id
         LIMIT 1
       ),''),
       session.absolute_expires_at,
       incident.opened_at,incident.updated_at,incident.closed_at,
       COALESCE((
         SELECT event.event_id
         FROM outbox_events event
         WHERE event.event_type IN ('RuntimeRecoveryStarted.v1','RuntimeRecoveryFailed.v1')
           AND event.correlation_id=incident.incident_id
         ORDER BY event.sequence
         LIMIT 1
       ),'')
FROM runtime_recovery_incidents incident
JOIN worker_sessions session ON session.worker_session_id=incident.worker_session_id
LEFT JOIN worker_runs run ON run.worker_run_id=incident.worker_run_id
LEFT JOIN agent_turns turn ON turn.agent_turn_id=incident.agent_turn_id
JOIN runtime_instances failed_runtime
  ON failed_runtime.runtime_instance_id=incident.failed_runtime_instance_id
LEFT JOIN worker_runs failed_run ON failed_run.worker_run_id=failed_runtime.worker_run_id
LEFT JOIN runtime_instances replacement_runtime
  ON replacement_runtime.runtime_instance_id=incident.replacement_runtime_instance_id
LEFT JOIN tasks task ON task.task_id=session.bound_task_id
`

type rowScanner interface {
	Scan(...any) error
}

func scanRuntimeRecoveryState(row rowScanner) (application.RuntimeRecoveryState, error) {
	var state application.RuntimeRecoveryState
	var agentTurnDeadlineAt *time.Time
	err := row.Scan(
		&state.IncidentID, &state.TenantID, &state.WorkerSessionID,
		&state.WorkerRunID, &state.AgentTurnID, &state.FailedRuntimeInstanceID,
		&state.FailedGeneration, &state.FailedChannelEpoch, &state.Trigger,
		&state.Phase, &state.AttemptCount,
		&state.ReplacementRuntimeInstanceID, &state.ReplacementGeneration,
		&state.ReplacementChannelEpoch, &state.FenceStatus, &state.MountFenceToken,
		&state.ContinuityStatus, &state.PreviousSessionStatus, &state.EvidenceRef,
		&state.EvidenceAt, &state.Outcome, &state.OutcomeReason, &state.IncidentRevision,
		&state.SessionRevision, &state.RunRevision, &state.TurnRevision,
		&state.FailedRuntimeRevision, &state.ReplacementRuntimeRevision,
		&state.ReplacementContainerID,
		&state.TaskStatus, &state.TaskRevision,
		&state.RuntimeWorkerRunID, &state.AgentSessionID, &state.RuntimeImageRef,
		&state.ResourceProfileID, &state.AgentProfileID, &state.AgentProfileRevision,
		&state.AgentProfileDigest, &state.NetworkPolicyID, &state.NetworkPolicyRevision,
		&state.NetworkPolicyDigest, &state.NetworkEnforcementStatus,
		&state.WorkingCopyCheckpointDigest,
		&agentTurnDeadlineAt, &state.AgentTurnDeadlineSourceRef,
		&state.SessionAbsoluteExpiresAt,
		&state.OpenedAt, &state.UpdatedAt, &state.ClosedAt, &state.EventID,
	)
	if err != nil {
		return application.RuntimeRecoveryState{}, err
	}
	if agentTurnDeadlineAt != nil {
		state.AgentTurnDeadlineAt = agentTurnDeadlineAt.UTC()
	}
	return state, nil
}

func runtimeRecoveryState(incident domain.RuntimeRecoveryIncident) application.RuntimeRecoveryState {
	return application.RuntimeRecoveryState{
		IncidentID: incident.IncidentID, TenantID: incident.TenantID,
		WorkerSessionID: incident.WorkerSessionID, WorkerRunID: incident.WorkerRunID,
		AgentTurnID: incident.AgentTurnID, FailedRuntimeInstanceID: incident.FailedRuntimeInstanceID,
		FailedGeneration: incident.FailedGeneration, FailedChannelEpoch: incident.FailedChannelEpoch,
		Trigger: incident.Trigger, Phase: incident.Phase, AttemptCount: incident.AttemptCount,
		ReplacementRuntimeInstanceID: incident.ReplacementRuntimeInstanceID,
		ReplacementGeneration:        incident.ReplacementGeneration,
		ReplacementChannelEpoch:      incident.ReplacementChannelEpoch,
		FenceStatus:                  incident.FenceStatus, MountFenceToken: incident.MountFenceToken,
		ContinuityStatus:      incident.ContinuityStatus,
		PreviousSessionStatus: incident.PreviousSessionStatus,
		EvidenceRef:           incident.EvidenceRef, EvidenceAt: incident.EvidenceAt,
		Outcome: incident.Outcome, OutcomeReason: incident.OutcomeReason,
		IncidentRevision: incident.Revision, OpenedAt: incident.OpenedAt,
		UpdatedAt: incident.UpdatedAt, ClosedAt: incident.ClosedAt,
	}
}

func runtimeRecoveryCommandDigest(command application.OpenRuntimeRecoveryCommand) (string, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func runtimeStatusCanRecover(status string) bool {
	return status == "starting" || status == "active" || status == "suspect" || status == "unhealthy"
}

func lockIdleRecoveryRuntime(
	ctx context.Context,
	tx platformpostgres.Tx,
	sessionID string,
	runtimeID string,
) (residentRuntimeRow, string, error) {
	var row residentRuntimeRow
	var activeRuntimeID *string
	var activeRunID *string
	var containerID *string
	err := tx.QueryRow(ctx, `
SELECT session.tenant_id,session.worker_session_id,session.active_runtime_instance_id,
       session.active_worker_run_id,session.status,session.session_revision,
       runtime.runtime_instance_id,runtime.container_id,runtime.generation,
       runtime.control_channel_epoch,runtime.status,runtime.revision
FROM worker_sessions session
JOIN runtime_instances runtime ON runtime.worker_session_id=session.worker_session_id
  AND runtime.runtime_instance_id=$2
WHERE session.worker_session_id=$1
FOR UPDATE OF session,runtime`, sessionID, runtimeID).Scan(
		&row.TenantID, &row.WorkerSessionID, &activeRuntimeID, &activeRunID,
		&row.SessionStatus, &row.SessionRevision, &row.RuntimeInstanceID,
		&containerID, &row.RuntimeGeneration, &row.ControlChannelEpoch,
		&row.RuntimeStatus, &row.RuntimeRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return residentRuntimeRow{}, "", platformpostgres.ErrNotFound
	}
	if err != nil {
		return residentRuntimeRow{}, "", fmt.Errorf("lock idle Runtime recovery identity: %w", err)
	}
	if activeRunID != nil {
		return residentRuntimeRow{}, "", platformpostgres.ErrRevisionConflict
	}
	if containerID != nil {
		row.ContainerID = *containerID
	}
	if activeRuntimeID == nil {
		return row, "", nil
	}
	return row, *activeRuntimeID, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

var _ application.RuntimeRecoveryStore = (*RuntimeRecoveryStore)(nil)
