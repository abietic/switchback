package postgres

import (
	"context"
	"encoding/json"
	"errors"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// releaseTerminalTurn derives authority from the original ledger and immutable
// start snapshot. It deliberately does not require current admission freshness,
// Task ownership/status, or a live Session: those can change during settlement.
// The caller owns the pool prefix and original business locks in this same TX.
func (h *CapacityTx) releaseTerminalTurn(ctx context.Context, turnID string) error {
	if turnID == "" {
		return nil
	}
	if err := h.valid(ctx); err != nil {
		return err
	}
	var run, status, terminalReason, runTerminalReason string
	var raw []byte
	err := h.tx.QueryRow(ctx, `SELECT t.worker_run_id,t.status,r.start_snapshot,COALESCE(t.terminal_reason,''),COALESCE(r.terminal_reason,'') FROM agent_turns t JOIN worker_runs r ON r.worker_run_id=t.worker_run_id WHERE t.agent_turn_id=$1`, turnID).Scan(&run, &status, &raw, &terminalReason, &runTerminalReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isTurnTerminal(status) {
		return nil
	}
	var snapshot struct {
		Capacity *struct {
			Target      app.CapacityTarget
			Reservation app.CapacityReservation
		} `json:"runtime_capacity"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return app.CapacityBindingMismatch
	}
	token := ""
	if snapshot.Capacity != nil {
		token = snapshot.Capacity.Reservation.Token
	}
	rows, err := h.tx.Query(ctx, `SELECT token::text,permit_id FROM runtime_capacity_executions WHERE pool_id='local' AND (run_id=$1 OR turn_id=$2 OR token::text=$3)`, run, turnID, token)
	if err != nil {
		return err
	}
	var tokens, permits []string
	for rows.Next() {
		var t, p string
		if err = rows.Scan(&t, &p); err != nil {
			rows.Close()
			return err
		}
		tokens = append(tokens, t)
		permits = append(permits, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(tokens) == 0 && snapshot.Capacity == nil {
		return nil
	} // legacy terminal, no new right
	if len(tokens) != 1 || snapshot.Capacity == nil {
		return app.CapacityBindingMismatch
	}
	// Historical DB-only absolute expiry is not proof of lost write access.
	// The old live owner terminalized the Run but left the Turn nonterminal;
	// a late deadline must not disguise that Run history with its own Turn reason.
	// Replays must not bypass the same gate as a new expiry transaction.
	if terminalReason == "worker_session_absolute_expired" || runTerminalReason == "worker_session_absolute_expired" {
		return app.CapacityPathNotIntegrated
	}
	target, err := h.BoundTaskTarget(ctx, tokens[0], permits[0])
	if err != nil {
		return err
	}
	original, _, err := h.findSettlementReservation(ctx, `token::text=$1`, tokens[0])
	if err != nil {
		return err
	}
	saved := snapshot.Capacity.Reservation
	if snapshot.Capacity.Target != target || saved.Token != original.Token || saved.PermitID != original.PermitID || saved.CommitmentID != original.CommitmentID || saved.SnapshotSequence != original.SnapshotSequence || !saved.ExpiresAt.Equal(original.ExpiresAt) || saved.RunID != run || saved.TurnID != turnID || saved.RuntimeID != original.RuntimeID || saved.RunID != original.RunID || saved.TurnID != original.TurnID || saved.Released || !equalCapacityGuarantee(saved.Guarantee, original.Guarantee) {
		return app.CapacityBindingMismatch
	}
	var valid bool
	err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_executions e
 JOIN runtime_capacity_residents c ON c.commitment_id=e.commitment_id
 JOIN worker_sessions s ON s.worker_session_id=e.worker_session_id
 JOIN worker_runs r ON r.worker_run_id=e.run_id
 JOIN agent_turns t ON t.agent_turn_id=e.turn_id
 JOIN runtime_instances i ON i.runtime_instance_id=e.runtime_id
 JOIN tasks task ON task.task_id=r.task_id
 JOIN session_dispatch_permits p ON p.permit_id=e.permit_id
 JOIN session_task_queue_entries q ON q.worker_session_id=s.worker_session_id AND q.task_id=task.task_id
 WHERE e.token::text=$1 AND e.consumed_at IS NOT NULL AND e.tenant_id=$2
 AND s.worker_session_id=$3 AND s.tenant_id=$2 AND s.user_id=$4
 AND r.worker_session_id=$3 AND r.task_id=$5 AND r.trigger_type='task_initial'
 AND t.worker_session_id=$3 AND t.worker_run_id=r.worker_run_id AND t.status IN ('completed','failed','cancelled','timed_out')
 AND i.worker_session_id=$3 AND i.worker_run_id=r.worker_run_id AND i.generation=$6 AND i.resource_profile_id=$7
 AND task.tenant_id=$2 AND task.initiator_user_id=$4
 AND p.worker_session_id=$3 AND p.target_kind='task' AND p.target_id=$5 AND p.status='consumed' AND p.consumed_by_worker_run_id=r.worker_run_id AND p.capacity_admission_ref=e.token::text
 AND p.runtime_generation=$6 AND p.expected_session_revision=$8 AND p.session_queue_sequence=$9 AND p.queue_revision=$10 AND p.expires_at=e.expires_at
 AND q.status='dispatched' AND q.session_queue_sequence=$9 AND q.queue_revision=$10
 AND c.pool_id='local' AND c.worker_session_id=$3 AND c.runtime_instance_id=i.runtime_instance_id AND c.generation=$6 AND c.origin='reserved'
 AND convert_from(c.profile_bytes,'UTF8')::jsonb=convert_from(e.target_bytes,'UTF8')::jsonb->'Profile')`, original.Token, target.TenantID, target.SessionID, target.ActorID, target.TaskID, target.Generation, target.Profile.ID, target.SessionRevision, target.QueueSequence, target.QueueRevision).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return app.CapacityBindingMismatch
	}
	return h.ReleaseExecution(ctx, app.CapacityConsumption{Token: original.Token, PermitID: original.PermitID, RunID: run, TurnID: turnID, RuntimeID: original.RuntimeID, Target: target})
}

// RevokeCancelledTaskExecution only revokes an unconsumed original Task right.
// CancelTask holds the pool and Task lock and has already persisted cancellation.
// Inspect active permits before the caller revokes them; historical revocations
// must not be mistaken for the current cancellation target.
// Consumed execution remains owned until the actual Turn terminal transaction.
func (h *CapacityTx) RevokeCancelledTaskExecution(ctx context.Context, taskID string, expectedRevision uint64) error {
	if err := h.valid(ctx); err != nil {
		return err
	}
	rows, err := h.tx.Query(ctx, `SELECT e.token::text,e.permit_id FROM runtime_capacity_executions e LEFT JOIN session_dispatch_permits p ON p.permit_id=e.permit_id WHERE e.pool_id='local' AND e.consumed_at IS NULL AND (p.status='active' OR p.permit_id IS NULL) AND ((p.target_kind='task' AND p.target_id=$1) OR convert_from(e.target_bytes,'UTF8')::jsonb->>'TaskID'=$1)`, taskID)
	if err != nil {
		return err
	}
	type binding struct{ token, permit string }
	var bindings []binding
	for rows.Next() {
		var b binding
		if err = rows.Scan(&b.token, &b.permit); err != nil {
			rows.Close()
			return err
		}
		bindings = append(bindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, b := range bindings {
		target, err := h.BoundTaskTarget(ctx, b.token, b.permit)
		if err != nil {
			return err
		}
		if target.TaskID != taskID || target.TaskRevision != expectedRevision {
			return app.CapacityBindingMismatch
		}
		var valid bool
		err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_executions e JOIN session_dispatch_permits p ON p.permit_id=e.permit_id JOIN runtime_capacity_residents c ON c.commitment_id=e.commitment_id JOIN tasks t ON t.task_id=p.target_id JOIN session_task_queue_entries q ON q.worker_session_id=p.worker_session_id AND q.task_id=t.task_id
 WHERE e.token::text=$1 AND e.pool_id='local' AND e.tenant_id=$2 AND e.worker_session_id=$3 AND e.consumed_at IS NULL AND e.run_id IS NULL AND e.turn_id IS NULL AND e.runtime_id IS NULL
 AND t.task_id=$4 AND t.tenant_id=$2 AND t.initiator_user_id=$5 AND t.status='cancelled' AND t.revision=$6 AND t.current_worker_run_id IS NULL AND t.worker_session_id=$3
 AND p.status='active' AND p.consumed_by_worker_run_id IS NULL AND p.capacity_admission_ref=e.token::text AND p.worker_session_id=$3 AND p.runtime_generation=$7 AND p.expected_session_revision=$8 AND p.session_queue_sequence=$9 AND p.queue_revision=$10 AND p.expires_at=e.expires_at
 AND q.status='cancelled' AND q.session_queue_sequence=$9 AND q.queue_revision=$10
 AND c.pool_id='local' AND c.worker_session_id=$3 AND c.generation=$7 AND c.origin='reserved' AND convert_from(c.profile_bytes,'UTF8')::jsonb=convert_from(e.target_bytes,'UTF8')::jsonb->'Profile'
 AND NOT EXISTS(SELECT 1 FROM worker_runs r WHERE r.task_id=t.task_id AND r.trigger_type='task_initial'))`, b.token, target.TenantID, target.SessionID, taskID, target.ActorID, expectedRevision+1, target.Generation, target.SessionRevision, target.QueueSequence, target.QueueRevision).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return app.CapacityBindingMismatch
		}
		if _, err = h.tx.Exec(ctx, `UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='task_cancelled' WHERE token::text=$1 AND released_at IS NULL AND consumed_at IS NULL`, b.token); err != nil {
			return err
		}
	}
	return nil
}

// expireUnconsumedExecutions uses the database clock and never changes identity,
// original TTL, permit results, consumed execution or resident commitments.
func (h *CapacityTx) expireUnconsumedExecutions(ctx context.Context) error {
	if err := h.valid(ctx); err != nil {
		return err
	}
	_, err := h.tx.Exec(ctx, `UPDATE runtime_capacity_executions SET released_at=clock_timestamp(),release_reason='permit_expired' WHERE pool_id='local' AND consumed_at IS NULL AND released_at IS NULL AND expires_at<=clock_timestamp()`)
	return err
}

// The outer pool lock precedes the owner's existing business lock sequence.
func inTerminalCapacityTransaction(ctx context.Context, pool *pgxpool.Pool, turnID func() string, action func(platformpostgres.Tx) error) error {
	return InCapacityTransaction(ctx, pool, func(tx pgx.Tx, h *CapacityTx) error {
		if err := action(tx); err != nil {
			return err
		}
		return h.releaseTerminalTurn(ctx, turnID())
	})
}

// Initial absolute expiry has no independent durable proof that the old
// Runtime lost WorkingCopy write access. Control fencing and cleanup JSON are
// insufficient. Keep all ownership and charges until that seam is implemented;
// preserve pre-ledger legacy expiry without claiming physical safety for it.
func rejectUnprovenInitialAbsoluteExpiry(ctx context.Context, tx platformpostgres.Tx, sessionID string) error {
	var initial bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_executions WHERE pool_id='local' AND worker_session_id=$1)`, sessionID).Scan(&initial); err != nil {
		return err
	}
	if initial {
		return app.CapacityPathNotIntegrated
	}
	return nil
}
