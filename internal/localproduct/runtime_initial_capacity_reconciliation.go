package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
)

// interruptedInitialRuntime is read-only evidence for withholding effects. It
// does not authorize adoption, replay, removal or capacity release. A snapshot,
// execution or reserved resident identifying this owner prevents legacy fallback
// when part of the binding has been lost/corrupted. Fresh inventory is irrelevant
// here: absence of an initial CID is not proof of physical container loss.
// Active/suspect owners requiring a new restore use the same read-only graph
// checks across dispatch, consumption and terminal settlement. Execution release
// and current Session binding are lifecycle facts, not immutable owner identity.
// Even a committed terminal/accept event grants no restart/handoff authority.
// UUID-shaped consumed permit refs are conservative deny evidence only; an old
// opaque ref with that shape may also be held, never granted ledger authority.
func (runner *runtimeRunner) interruptedInitialRuntime(ctx context.Context, runtimeID string) error {
	var status, runID, sessionID string
	var generation uint64
	var raw []byte
	var ledger bool
	err := runner.pool.QueryRow(ctx, `SELECT i.status,i.worker_run_id,i.worker_session_id,i.generation,r.start_snapshot,
 EXISTS(SELECT 1 FROM runtime_capacity_executions e WHERE e.runtime_id=i.runtime_instance_id OR e.run_id=i.worker_run_id OR e.worker_session_id=i.worker_session_id)
 OR EXISTS(SELECT 1 FROM runtime_capacity_residents c WHERE c.origin='reserved' AND (c.runtime_instance_id=i.runtime_instance_id OR (c.worker_session_id=i.worker_session_id AND c.generation=i.generation)))
 OR EXISTS(SELECT 1 FROM session_dispatch_permits p WHERE p.consumed_by_worker_run_id=i.worker_run_id AND p.capacity_admission_ref ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')
 FROM runtime_instances i JOIN worker_runs r ON r.worker_run_id=i.worker_run_id WHERE i.runtime_instance_id=$1`, runtimeID).Scan(&status, &runID, &sessionID, &generation, &raw, &ledger)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerapp.CapacityBindingMismatch
	}
	if err != nil {
		return err
	}
	// Activation may have committed before the ready-event acknowledgement or
	// live handoff failed. A process-local owner still retains the actual CID;
	// never fall through to loss/cleanup just because no live entry exists yet.
	runner.mu.Lock()
	pending := runner.pendingInitialActivations[runtimeID]
	live := runner.runtimes[runtimeID]
	runner.mu.Unlock()
	if pending != nil && status == "active" {
		if pending.command.WorkerRunID != runID || pending.command.WorkerSessionID != sessionID || pending.command.RuntimeGeneration != generation {
			return workerapp.CapacityBindingMismatch
		}
	}
	if status != "starting" && (live != nil || (status != "active" && status != "suspect")) {
		return nil
	}
	var snapshot struct {
		Capacity *struct {
			Target      workerapp.CapacityTarget
			Reservation workerapp.CapacityReservation
		} `json:"runtime_capacity"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return workerapp.CapacityBindingMismatch
	}
	if snapshot.Capacity == nil && !ledger {
		return nil
	}
	if snapshot.Capacity == nil {
		return workerapp.CapacityBindingMismatch
	}
	target, saved := snapshot.Capacity.Target, snapshot.Capacity.Reservation
	if target.Kind != "task_initial" || target.SessionID != sessionID || target.Generation != generation || saved.RunID != runID || saved.RuntimeID != runtimeID || saved.Token == "" || saved.TurnID == "" || saved.Released {
		return workerapp.CapacityBindingMismatch
	}
	// CompleteResidentStage releases execution and clears the active Run in one
	// transaction, before Task publication/pause/cancellation settlement. Thus
	// Task may still be running (or already cancelled) at that boundary. Public
	// accept/cancel later clears bound_task_id; report-only auto delivery may
	// complete Task while retaining it. Every case below still binds the original
	// immutable snapshot, consumed permit, Run/Turn and resident CID/generation.
	// ObservePool commits a trusted resident CID pin before Activate writes the
	// Runtime CID in a separate transaction. Starting may therefore have either
	// an unpinned or pinned resident; both grant only the same read-only hold.
	// Once active/suspect, the two persisted CIDs must still match.
	canonical, _ := json.Marshal(target)
	var valid bool
	err = runner.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_executions e
 JOIN runtime_capacity_residents c ON c.commitment_id=e.commitment_id
 JOIN runtime_instances i ON i.runtime_instance_id=e.runtime_id
 JOIN worker_runs r ON r.worker_run_id=e.run_id
 JOIN agent_turns t ON t.agent_turn_id=e.turn_id
 JOIN worker_sessions s ON s.worker_session_id=e.worker_session_id
 JOIN tasks task ON task.task_id=r.task_id
 JOIN session_dispatch_permits p ON p.permit_id=e.permit_id
 JOIN session_task_queue_entries q ON q.worker_session_id=s.worker_session_id AND q.task_id=task.task_id
 WHERE e.pool_id='local' AND e.token::text=$1 AND e.permit_id=$2 AND e.commitment_id::text=$3
 AND e.snapshot_sequence=$4 AND e.expires_at=$5 AND e.run_id=$6 AND e.turn_id=$7 AND e.runtime_id=$8
 AND e.consumed_at IS NOT NULL AND e.target_digest=$9
 AND convert_from(e.target_bytes,'UTF8')::jsonb=$10::jsonb
 AND e.tenant_id=$11 AND e.worker_session_id=$12
 AND c.pool_id='local' AND c.origin='reserved' AND c.worker_session_id=$12 AND c.runtime_instance_id=$8 AND c.generation=$13 AND c.removal_sequence IS NULL
 AND convert_from(c.profile_bytes,'UTF8')::jsonb=$10::jsonb->'Profile'
 AND i.worker_session_id=$12 AND i.worker_run_id=$6 AND i.generation=$13 AND i.resource_profile_id=$14
 AND ((i.status='starting' AND i.container_id IS NULL) OR (i.status IN ('active','suspect') AND i.container_id IS NOT NULL AND i.container_id=c.container_id))
 AND r.worker_session_id=$12 AND r.task_id=$15 AND r.trigger_type='task_initial'
 AND t.worker_session_id=$12 AND t.worker_run_id=$6
 AND s.tenant_id=$11 AND s.user_id=$16 AND s.active_runtime_instance_id=$8 AND s.runtime_generation=$13
 AND task.tenant_id=$11 AND task.initiator_user_id=$16 AND task.worker_session_id=$12 AND task.current_worker_run_id=$6
 AND p.worker_session_id=$12 AND p.target_kind='task' AND p.target_id=$15 AND p.status='consumed' AND p.consumed_by_worker_run_id=$6 AND p.capacity_admission_ref=$1 AND p.runtime_generation=$13 AND p.expected_session_revision=$17 AND p.session_queue_sequence=$18 AND p.queue_revision=$19 AND p.expires_at=e.expires_at
 AND q.status='dispatched' AND q.session_queue_sequence=$18 AND q.queue_revision=$19
 AND (
   (e.released_at IS NULL AND e.release_reason IS NULL
    AND t.terminal_at IS NULL AND r.terminal_at IS NULL
    AND s.status='running' AND s.active_worker_run_id=$6 AND s.bound_task_id=$15
    AND task.status IN ('running','cancelled')
    AND ((i.status='starting' AND r.status='starting' AND t.status='delivered')
      OR (i.status IN ('active','suspect') AND r.status='running' AND t.status IN ('delivered','consumed'))))
   OR
   (i.status IN ('active','suspect') AND e.released_at IS NOT NULL AND e.release_reason='turn_terminal'
    AND t.terminal_at IS NOT NULL AND r.terminal_at=t.terminal_at
    AND r.status=t.status AND r.terminal_reason IS NOT DISTINCT FROM t.terminal_reason
    AND ((t.status='completed' AND t.terminal_reason IS NULL)
      OR (t.status='cancelled' AND t.terminal_reason IN ('cancelled','user_interrupt','cancel'))
      OR (t.status='failed' AND t.terminal_reason='agent_error')
      OR (t.status='timed_out' AND t.terminal_reason='agent_turn_deadline'))
    AND s.active_worker_run_id IS NULL AND s.status IN ('interactive','waiting_user','idle')
    AND ((r.status='completed' AND task.status IN ('running','delivered','completed','cancelled'))
      OR (r.status IN ('failed','cancelled','timed_out') AND task.status IN ('running','waiting_user','cancelled')))
    AND ((s.bound_task_id=$15 AND (task.status IN ('running','waiting_user','delivered','cancelled')
          OR (task.status='completed' AND task.acceptance_mode='auto')))
      OR (s.bound_task_id IS NULL AND task.status IN ('completed','cancelled'))))
 ))`, saved.Token, saved.PermitID, saved.CommitmentID, saved.SnapshotSequence, saved.ExpiresAt, runID, saved.TurnID, runtimeID, capacitysnapshot.Digest(canonical), canonical, target.TenantID, sessionID, generation, target.Profile.ID, target.TaskID, target.ActorID, target.SessionRevision, target.QueueSequence, target.QueueRevision).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid || target.TenantID != runner.configuration.Profile.TenantID || target.ActorID != runner.configuration.Profile.ActorUserID {
		return workerapp.CapacityBindingMismatch
	}
	return fmt.Errorf("initial Runtime %s retains interrupted ownership: %w", runtimeID, workerapp.InitialRuntimeStartReplayPending)
}
