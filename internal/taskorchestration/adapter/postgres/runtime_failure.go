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
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
)

type runtimeFailureQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (store *Store) ReadRuntimeFailureTask(ctx context.Context, event contractsv1.IntegrationEvent) (application.PauseTaskCommand, bool, error) {
	return readRuntimeFailureTask(ctx, store.pool, event, false)
}

// Every delivery, including duplicates, is compared with the actual durable
// owner event. Failure-only consumption must not impose contiguous sequences.
func verifyRuntimeFailureEvent(ctx context.Context, query runtimeFailureQuery, event contractsv1.IntegrationEvent) (string, error) {
	if err := event.Validate(); err != nil {
		return "", err
	}
	var durable contractsv1.IntegrationEvent
	if err := query.QueryRow(ctx, `SELECT schema_version,event_id,event_type,producer_context,stream_id,sequence,tenant_id,aggregate_type,aggregate_id,trace_id,correlation_id,causation_id,idempotency_key,COALESCE(client_command_id,''),visibility,occurred_at,payload FROM outbox_events WHERE event_id=$1`, event.EventID).Scan(
		&durable.SchemaVersion, &durable.EventID, &durable.EventType, &durable.ProducerContext,
		&durable.StreamID, &durable.Sequence, &durable.TenantID, &durable.AggregateType,
		&durable.AggregateID, &durable.TraceID, &durable.CorrelationID, &durable.CausationID,
		&durable.IdempotencyKey, &durable.ClientCommandID, &durable.Visibility, &durable.OccurredAt, &durable.Payload); err != nil {
		return "", fmt.Errorf("read durable Runtime failure event: %w", err)
	}
	event.OccurredAt, durable.OccurredAt = event.OccurredAt.UTC(), durable.OccurredAt.UTC()
	digest, err := platformpostgres.IntegrationEventDigest(event)
	if err != nil {
		return "", err
	}
	storedDigest, err := platformpostgres.IntegrationEventDigest(durable)
	if err != nil {
		return "", err
	}
	if digest != storedDigest || event.EventType != "RuntimeRecoveryFailed.v1" || event.ProducerContext != "worker-runtime" || event.AggregateType != "WorkerSession" || event.StreamID != "worker-session/"+event.AggregateID {
		return "", application.ErrIdempotencyConflict
	}
	return digest, nil
}

func readRuntimeFailureTask(ctx context.Context, query runtimeFailureQuery, event contractsv1.IntegrationEvent, lock bool) (application.PauseTaskCommand, bool, error) {
	var command application.PauseTaskCommand
	if _, err := verifyRuntimeFailureEvent(ctx, query, event); err != nil {
		return command, false, err
	}
	var payload struct {
		IncidentID       string `json:"incident_id"`
		Phase            string `json:"phase"`
		FailedRuntimeID  string `json:"failed_runtime_instance_id"`
		FailedGeneration uint64 `json:"failed_generation"`
		AttemptCount     uint32 `json:"attempt_count"`
		OutcomeReason    string `json:"outcome_reason"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return command, false, err
	}
	var tenant, session, run, turn, runtime, reason, phase, taskID string
	var generation, epoch, incidentRevision uint64
	var attempts uint32
	if err := query.QueryRow(ctx, `SELECT tenant_id,worker_session_id,COALESCE(worker_run_id,''),COALESCE(agent_turn_id,''),failed_runtime_instance_id,failed_generation,failed_channel_epoch,phase,attempt_count,COALESCE(outcome_reason,''),revision FROM runtime_recovery_incidents WHERE incident_id=$1`, payload.IncidentID).Scan(
		&tenant, &session, &run, &turn, &runtime, &generation, &epoch, &phase, &attempts, &reason, &incidentRevision); err != nil {
		return command, false, fmt.Errorf("read durable Runtime failure incident: %w", err)
	}
	if tenant != event.TenantID || session != event.AggregateID || payload.IncidentID != event.CorrelationID || runtime != event.CausationID || payload.FailedRuntimeID != runtime || payload.FailedGeneration != generation || payload.Phase != phase || phase != "exhausted" || payload.AttemptCount != attempts || payload.OutcomeReason != reason || epoch == 0 || incidentRevision == 0 {
		return command, false, application.ErrIdempotencyConflict
	}
	// Enabled recovery retains its existing owner path and compatibility.
	if reason != "runtime_recovery_disabled" {
		return command, false, nil
	}
	if attempts != 0 {
		return command, false, application.ErrIdempotencyConflict
	}
	// The exhausted idempotency result is the immutable Worker-owned version
	// snapshot. Live Session fields cannot redefine the failed Run or Turn.
	var encoded []byte
	if err := query.QueryRow(ctx, `SELECT result FROM runtime_recovery_idempotency_results WHERE incident_id=$1 AND command_kind='exhaust' AND result->>'EventID'=$2`, payload.IncidentID, event.EventID).Scan(&encoded); err != nil {
		return command, false, fmt.Errorf("read immutable Runtime failure result: %w", err)
	}
	var original struct {
		IncidentID, TenantID, WorkerSessionID, WorkerRunID, AgentTurnID, FailedRuntimeInstanceID                                  string
		FailedGeneration, FailedChannelEpoch, IncidentRevision, SessionRevision, RunRevision, TurnRevision, FailedRuntimeRevision uint64
	}
	if err := json.Unmarshal(encoded, &original); err != nil {
		return command, false, err
	}
	if original.IncidentID != payload.IncidentID || original.TenantID != tenant || original.WorkerSessionID != session || original.WorkerRunID != run || original.AgentTurnID != turn || original.FailedRuntimeInstanceID != runtime || original.FailedGeneration != generation || original.FailedChannelEpoch != epoch || original.IncidentRevision != incidentRevision || original.SessionRevision == 0 || original.FailedRuntimeRevision == 0 || run != "" && (original.RunRevision == 0 || original.TurnRevision == 0) {
		return command, false, application.ErrIdempotencyConflict
	}
	var runtimeSession, runtimeRun string
	var runtimeGeneration, runtimeEpoch uint64
	if err := query.QueryRow(ctx, `SELECT worker_session_id,COALESCE(worker_run_id,''),generation,control_channel_epoch FROM runtime_instances WHERE runtime_instance_id=$1`, runtime).Scan(&runtimeSession, &runtimeRun, &runtimeGeneration, &runtimeEpoch); err != nil {
		return command, false, err
	}
	if runtimeSession != session || runtimeGeneration != generation || runtimeEpoch != epoch || (run != "" && runtimeRun != run) {
		return command, false, application.ErrIdempotencyConflict
	}
	if run != "" {
		var runSession string
		if err := query.QueryRow(ctx, `SELECT worker_session_id,COALESCE(task_id,'') FROM worker_runs WHERE worker_run_id=$1`, run).Scan(&runSession, &taskID); err != nil {
			return command, false, err
		}
		if runSession != session {
			return command, false, application.ErrIdempotencyConflict
		}
	}
	if turn != "" {
		var turnSession, turnRun string
		if err := query.QueryRow(ctx, `SELECT worker_session_id,worker_run_id FROM agent_turns WHERE agent_turn_id=$1`, turn).Scan(&turnSession, &turnRun); err != nil {
			return command, false, err
		}
		if turnSession != session || turnRun != run {
			return command, false, application.ErrIdempotencyConflict
		}
	} else if run != "" {
		return command, false, application.ErrIdempotencyConflict
	}
	var taskTenant, initiator, status, taskSession, taskRun string
	var revision uint64
	if taskID != "" {
		statement := `SELECT tenant_id,initiator_user_id,status,COALESCE(worker_session_id,''),COALESCE(current_worker_run_id,''),revision FROM tasks WHERE task_id=$1`
		if lock {
			statement += ` FOR UPDATE`
		}
		if err := query.QueryRow(ctx, statement, taskID).Scan(&taskTenant, &initiator, &status, &taskSession, &taskRun, &revision); err != nil {
			return command, false, err
		}
		if taskTenant != tenant {
			return command, false, application.ErrIdempotencyConflict
		}
	}
	var sessionTenant, boundTask, activeRun, activeRuntime string
	var currentGeneration, sessionRevision uint64
	statement := `SELECT tenant_id,COALESCE(bound_task_id,''),COALESCE(active_worker_run_id,''),COALESCE(active_runtime_instance_id,''),runtime_generation,session_revision FROM worker_sessions WHERE worker_session_id=$1`
	if lock {
		statement += ` FOR UPDATE`
	}
	if err := query.QueryRow(ctx, statement, session).Scan(&sessionTenant, &boundTask, &activeRun, &activeRuntime, &currentGeneration, &sessionRevision); err != nil {
		return command, false, err
	}
	if sessionTenant != tenant {
		return command, false, application.ErrIdempotencyConflict
	}
	if taskID == "" {
		return command, false, nil
	}
	if status != "running" && status != "waiting_user" || taskSession != session || taskRun != run || boundTask != taskID || activeRun != run {
		return command, false, nil
	}
	if activeRuntime != runtime || currentGeneration != generation {
		var proven bool
		if activeRuntime == "" && currentGeneration == generation {
			// Cleanup atomically clears only the Runtime pointer, not Task/Run
			// ownership. Require the exact failed Runtime's committed cleanup and
			// its original scheduled lease, after this immutable failure snapshot.
			var err error
			proven, err = workerpostgres.ReadConfirmedRuntimeCleanup(ctx, query, workerpostgres.ConfirmedRuntimeCleanupIdentity{TenantID: tenant, WorkerSessionID: session, RuntimeInstanceID: runtime, Generation: generation, ChannelEpoch: epoch, FailedRuntimeRevision: original.FailedRuntimeRevision, FailedSessionRevision: original.SessionRevision, CurrentSessionRevision: sessionRevision})
			if err != nil {
				return command, false, fmt.Errorf("verify failed Runtime cleanup: %w", err)
			}
			if !proven {
				return command, false, fmt.Errorf("failed Runtime cleanup is unproven: %w", application.ErrRuntimeFailureRevisionConflict)
			}
		} else if activeRuntime != "" && currentGeneration > generation {
			if err := query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_instances WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND generation=$3 AND control_channel_epoch>$4 AND worker_run_id=$5)`, activeRuntime, session, currentGeneration, epoch, run).Scan(&proven); err != nil {
				return command, false, err
			}
			if !proven {
				return command, false, fmt.Errorf("replacement Runtime identity is unproven: %w", application.ErrRuntimeFailureRevisionConflict)
			}
			return command, false, nil
		} else {
			return command, false, fmt.Errorf("failed Runtime binding is unproven: %w", application.ErrRuntimeFailureRevisionConflict)
		}
	}
	key := "runtime-failure/" + event.EventID
	if status == "waiting_user" {
		var exists bool
		if err := query.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_transitions WHERE task_id=$1 AND idempotency_key=$2)`, taskID, key).Scan(&exists); err != nil {
			return command, false, err
		}
		if !exists {
			return command, false, nil
		}
	}
	command = application.PauseTaskCommand{Actor: application.Actor{TenantID: tenant, UserID: initiator}, TaskID: taskID,
		WorkerSessionID: session, WorkerRunID: run, ExpectedTaskRevision: revision, Reason: reason, IdempotencyKey: key, RuntimeFailureEvent: &event}
	return command, true, nil
}

func runtimeFailureConflict() error {
	return errors.Join(platformpostgres.ErrRevisionConflict, application.ErrRuntimeFailureRevisionConflict)
}

var _ application.RuntimeFailureTaskReader = (*Store)(nil)
