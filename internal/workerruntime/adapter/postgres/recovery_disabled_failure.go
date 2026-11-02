package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

// RecordRuntimeFailureWithoutReplacement never publishes a recoverable phase.
// The terminal incident is also the durable cleanup discovery intent; external
// cleanup still starts through RuntimeCleanupStore, after this transaction.
func (store *RuntimeRecoveryStore) RecordRuntimeFailureWithoutReplacement(ctx context.Context, record application.RuntimeFailureWithoutReplacementRecord) (application.RuntimeRecoveryState, error) {
	command := record.Command
	var result application.RuntimeRecoveryState
	err := inTerminalCapacityTransaction(ctx, store.pool, func() string { return command.AgentTurnID }, func(tx platformpostgres.Tx) error {
		if command.AgentTurnID != "" {
			if err := lockAgentTurnOutcome(ctx, tx, command.AgentTurnID); err != nil {
				return err
			}
		}
		task, err := lockRuntimeTaskBeforeGraph(ctx, tx, command.WorkerSessionID)
		if err != nil {
			return err
		}
		var row residentRuntimeRow
		if command.WorkerRunID == "" {
			row, _, err = lockIdleRecoveryRuntime(ctx, tx, command.WorkerSessionID, command.FailedRuntimeInstanceID)
		} else {
			row, err = lockResidentRuntime(ctx, tx, command.WorkerSessionID, command.WorkerRunID, command.AgentTurnID, command.FailedRuntimeInstanceID)
		}
		if err != nil {
			return err
		}
		if err := task.revalidate(ctx, tx, command.WorkerSessionID); err != nil {
			return err
		}
		var activeRuntimeID, activeRunID string
		var expiresAt, idleExpiresAt, updatedAt time.Time
		var sessionGeneration uint64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(active_runtime_instance_id,''),COALESCE(active_worker_run_id,''),absolute_expires_at,idle_expires_at,updated_at,runtime_generation FROM worker_sessions WHERE worker_session_id=$1`, command.WorkerSessionID).Scan(&activeRuntimeID, &activeRunID, &expiresAt, &idleExpiresAt, &updatedAt, &sessionGeneration); err != nil {
			return err
		}
		if row.TenantID != command.TenantID || activeRuntimeID != command.FailedRuntimeInstanceID || activeRunID != command.WorkerRunID || sessionGeneration != command.FailedGeneration || row.RuntimeGeneration != command.FailedGeneration || row.ControlChannelEpoch != command.FailedChannelEpoch || row.ContainerID != command.ExpectedContainerID {
			return platformpostgres.ErrRevisionConflict
		}
		if err := validateRuntimeTurnOutcomeAfterGraphLock(ctx, tx, command.FailedRuntimeInstanceID, runtimeTurnOutcomeLock{WorkerSessionID: command.WorkerSessionID, WorkerRunID: command.WorkerRunID, AgentTurnID: command.AgentTurnID}); err != nil {
			return err
		}
		// Session graph locking serializes idle observations too. Fresh observation
		// timestamps do not authorize another incident or change the first result.
		existing, found, err := loadRuntimeRecoveryByFailedGeneration(ctx, tx, command.WorkerSessionID, command.FailedGeneration)
		if err != nil {
			return err
		}
		if found {
			if existing.TenantID != command.TenantID || existing.FailedRuntimeInstanceID != command.FailedRuntimeInstanceID || existing.FailedChannelEpoch != command.FailedChannelEpoch || existing.WorkerRunID != command.WorkerRunID || existing.AgentTurnID != command.AgentTurnID {
				return ErrRuntimeRecoveryConflict
			}
			result = existing
			if existing.OutcomeReason == "runtime_recovery_disabled" {
				var encoded []byte
				if err := tx.QueryRow(ctx, `SELECT result FROM runtime_recovery_idempotency_results WHERE incident_id=$1 AND command_kind='exhaust' ORDER BY created_at LIMIT 1`, existing.IncidentID).Scan(&encoded); err != nil {
					return err
				}
				if err := json.Unmarshal(encoded, &result); err != nil {
					return err
				}
			}
			return nil
		}
		if command.AgentTurnID != "" {
			if err := assertNoCompetingTurnOutcomeAuthority(ctx, tx, command.AgentTurnID); err != nil {
				return err
			}
			var deadline *time.Time
			if err := tx.QueryRow(ctx, `SELECT deadline_at FROM agent_turns WHERE agent_turn_id=$1`, command.AgentTurnID).Scan(&deadline); err != nil {
				return err
			}
			if deadline != nil && !record.Now.Before(*deadline) {
				return application.ErrTurnOutcomeAuthorityClaimed
			}
			if row.RunStatus != domain.RunRunning && row.RunStatus != domain.RunStarting {
				return platformpostgres.ErrRevisionConflict
			}
			if row.TurnStatus != domain.TurnAccepted && row.TurnStatus != domain.TurnDelivered && row.TurnStatus != domain.TurnConsumed {
				return platformpostgres.ErrRevisionConflict
			}
		}
		idleExpired := activeRunID == "" && row.SessionStatus != domain.SessionWaitingUser && !record.Now.Before(idleExpiresAt)
		if task.Status == "cancelled" || idleExpired || !record.Now.Before(expiresAt) || command.EvidenceAt.Before(updatedAt) || !detectedFailureSessionStatusAllowed(string(row.SessionStatus)) || !runtimeStatusCanRecover(row.RuntimeStatus) || row.SessionRevision != command.ExpectedSessionRevision || row.RunRevision != command.ExpectedRunRevision || row.TurnRevision != command.ExpectedTurnRevision || row.RuntimeRevision != command.ExpectedRuntimeRevision {
			return platformpostgres.ErrRevisionConflict
		}
		incident, err := domain.OpenRuntimeRecoveryIncident(domain.OpenRuntimeRecoveryIncidentCommand{
			IncidentID: record.IncidentID, TenantID: command.TenantID, WorkerSessionID: command.WorkerSessionID, WorkerRunID: command.WorkerRunID, AgentTurnID: command.AgentTurnID,
			FailedRuntimeInstanceID: command.FailedRuntimeInstanceID, FailedGeneration: command.FailedGeneration, FailedChannelEpoch: command.FailedChannelEpoch,
			Trigger: command.Trigger, PreviousSessionStatus: row.SessionStatus, EvidenceRef: command.EvidenceRef, EvidenceAt: command.EvidenceAt, OpenedAt: record.Now,
		})
		if err != nil {
			return err
		}
		incident, err = incident.Transition(domain.RecoveryExhausted, record.Now, domain.RuntimeRecoveryTransition{FenceStatus: domain.RecoveryFenceUnproven, ContinuityStatus: domain.RecoveryContinuityUnproven, OutcomeReason: "runtime_recovery_disabled"})
		if err != nil {
			return err
		}
		channel, err := lockFenceRuntimeControlChannel(ctx, tx, runtimeControlIdentity{workerSessionID: command.WorkerSessionID, runtimeID: command.FailedRuntimeInstanceID, generation: command.FailedGeneration, channelEpoch: command.FailedChannelEpoch})
		if err != nil && !errors.Is(err, platformpostgres.ErrNotFound) {
			return err
		}
		if err == nil {
			if _, err := fenceRuntimeControlChannelLocked(ctx, tx, channel, application.FenceRuntimeControlChannelCommand{}, record.Now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE session_dispatch_permits SET status='revoked' WHERE worker_session_id=$1 AND status='active'`, command.WorkerSessionID); err != nil {
			return err
		}
		// Persist the actual observation, without asserting external write fencing,
		// cleanup completion, terminal Runtime envelopes or delivery artifacts.
		diagnostic, err := json.Marshal(struct {
			IncidentID          string                        `json:"incident_id"`
			ExpectedContainerID string                        `json:"expected_container_id"`
			Inspection          application.RuntimeInspection `json:"inspection"`
		}{incident.IncidentID, command.ExpectedContainerID, command.Inspection})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime_instances SET status='lost',revision=revision+1,ended_at=$2,loss_diagnostic=$3 WHERE runtime_instance_id=$1`, command.FailedRuntimeInstanceID, record.Now, diagnostic); err != nil {
			return err
		}
		if command.AgentTurnID != "" {
			if _, err := tx.Exec(ctx, `UPDATE agent_turns SET status='failed',revision=revision+1,terminal_at=$2,terminal_reason='runtime_recovery_failed' WHERE agent_turn_id=$1`, command.AgentTurnID, record.Now); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE worker_runs SET status='waiting_user',revision=revision+1 WHERE worker_run_id=$1`, command.WorkerRunID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE worker_sessions SET status='waiting_user',session_revision=session_revision+1,updated_at=$2 WHERE worker_session_id=$1`, command.WorkerSessionID, record.Now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime_recovery_incidents
 (incident_id,tenant_id,worker_session_id,worker_run_id,agent_turn_id,failed_runtime_instance_id,failed_generation,failed_channel_epoch,trigger,phase,previous_session_status,attempt_count,fence_status,continuity_status,evidence_ref,evidence_at,revision,opened_at,updated_at,closed_at,outcome,outcome_reason)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'exhausted',$10,0,'unproven','unproven',$11,$12,$13,$14,$14,$14,'exhausted','runtime_recovery_disabled')`,
			incident.IncidentID, incident.TenantID, incident.WorkerSessionID, nullableText(incident.WorkerRunID), nullableText(incident.AgentTurnID), incident.FailedRuntimeInstanceID, incident.FailedGeneration, incident.FailedChannelEpoch, incident.Trigger, incident.PreviousSessionStatus, incident.EvidenceRef, incident.EvidenceAt, incident.Revision, record.Now); err != nil {
			return fmt.Errorf("record terminal Runtime failure: %w", err)
		}
		result, err = scanRuntimeRecoveryState(tx.QueryRow(ctx, runtimeRecoverySelect+` WHERE incident.incident_id=$1`, incident.IncidentID))
		if err != nil {
			return err
		}
		result.EventID = record.EventID
		if err := store.appendRuntimeRecoveryPhaseEvent(ctx, tx, application.AdvanceRuntimeRecoveryRecord{Command: application.AdvanceRuntimeRecoveryCommand{IdempotencyKey: command.IdempotencyKey}, EventID: record.EventID, Now: record.Now}, result); err != nil {
			return err
		}
		digest, err := runtimeRecoveryCommandDigest(command.OpenRuntimeRecoveryCommand)
		if err != nil {
			return err
		}
		return insertRuntimeRecoveryIdempotency(ctx, tx, result, command.WorkerSessionID, "exhaust", command.IdempotencyKey, digest, record.Now)
	})
	return result, err
}
