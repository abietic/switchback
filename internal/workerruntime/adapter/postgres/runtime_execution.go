package postgres

import (
	"context"
	"encoding/json"
	"errors"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"

	"github.com/abietic/switchback/internal/platform/config"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InitialRuntimeExecutionAuthorizer struct{ pool *pgxpool.Pool }

func NewInitialRuntimeExecutionAuthorizer(pool *pgxpool.Pool) *InitialRuntimeExecutionAuthorizer {
	return &InitialRuntimeExecutionAuthorizer{pool: pool}
}

func (a *InitialRuntimeExecutionAuthorizer) AuthorizeInitialPreparation(ctx context.Context, r app.RuntimePreparationRequest) error {
	return a.authorize(ctx, r, nil)
}
func (a *InitialRuntimeExecutionAuthorizer) AuthorizeInitialProvision(ctx context.Context, r app.RuntimePreparationRequest, p app.RuntimeProvisionRequest) error {
	return a.authorize(ctx, r, &p)
}
func (a *InitialRuntimeExecutionAuthorizer) authorize(ctx context.Context, r app.RuntimePreparationRequest, p *app.RuntimeProvisionRequest) error {
	if a == nil || a.pool == nil {
		return app.CapacityBindingMismatch
	}
	sourceInput, err := validateInitialSourcePlanInput(r, p)
	if err != nil {
		return err
	}
	return InCapacityTransaction(ctx, a.pool, func(tx pgx.Tx, h *CapacityTx) error {
		o, c := r.Ownership, r.Command
		row, target, err := h.initialExecution(ctx, o.WorkerSessionID, o.WorkerRunID, o.AgentTurnID, o.RuntimeInstanceID, o.RuntimeGeneration)
		if err != nil {
			return err
		}
		if row.RuntimeStatus != "starting" || row.ContainerID != "" || row.RunStatus != "starting" || row.TurnStatus != "delivered" || c.TenantID != target.TenantID || c.UserID != target.ActorID || c.TaskID != target.TaskID || c.WorkerSessionID != target.SessionID || o.TaskID != target.TaskID || c.RuntimeGeneration != target.Generation || c.ResourceProfileID != target.Profile.ID || c.ControlChannelEpoch != row.ControlChannelEpoch || o.ControlChannelEpoch != row.ControlChannelEpoch {
			return app.CapacityBindingMismatch
		}
		var permit, agentSession, container string
		if err = tx.QueryRow(ctx, `SELECT e.permit_id,t.agent_session_id,COALESCE(r.container_id,'') FROM runtime_capacity_executions e JOIN runtime_capacity_residents r ON r.commitment_id=e.commitment_id JOIN agent_turns t ON t.agent_turn_id=e.turn_id WHERE e.run_id=$1 AND e.turn_id=$2 AND e.runtime_id=$3`, o.WorkerRunID, o.AgentTurnID, o.RuntimeInstanceID).Scan(&permit, &agentSession, &container); err != nil {
			return err
		}
		if permit != c.PermitID || agentSession != o.AgentSessionID || container != "" {
			return app.CapacityBindingMismatch
		}
		network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
		if c.NetworkPolicyID != network.ID || c.NetworkPolicyRevision != network.Revision || c.NetworkPolicyDigest != network.Digest || c.NetworkEnforcementStatus != config.NetworkEnforcementActive {
			return app.CapacityBindingMismatch
		}
		if p != nil && (p.WorkerSessionID != o.WorkerSessionID || p.WorkerRunID != o.WorkerRunID || p.RuntimeInstanceID != o.RuntimeInstanceID || p.Generation != o.RuntimeGeneration || p.ResourceProfile != (app.ProfileRef{ID: target.Profile.ID, Revision: target.Profile.Revision, Digest: target.Profile.Digest}) || p.NetworkProfile != (app.ProfileRef{ID: network.ID, Revision: network.Revision, Digest: network.Digest}) || p.NetworkEnforcementStatus != config.NetworkEnforcementActive) {
			return app.CapacityBindingMismatch
		}
		var stored persistedRuntimeSourcePlan
		var snapshot []byte
		// Reuse the consumed original row/key, under the already-held pool and
		// business locks. No source filesystem or Engine work enters this transaction.
		if err = tx.QueryRow(ctx, `SELECT e.source_plan_bytes,e.source_plan_digest,e.source_plan_bound_at,w.start_snapshot
 FROM runtime_capacity_executions e JOIN worker_runs w ON w.worker_run_id=e.run_id
 WHERE e.pool_id='local' AND e.worker_session_id=$1 AND e.run_id=$2 AND e.turn_id=$3 AND e.runtime_id=$4
 AND e.consumed_at IS NOT NULL AND e.released_at IS NULL FOR UPDATE OF e`, o.WorkerSessionID, o.WorkerRunID, o.AgentTurnID, o.RuntimeInstanceID).Scan(&stored.bytes, &stored.digest, &stored.boundAt, &snapshot); err != nil {
			return err
		}
		if err = sourceInput.validate(snapshot, stored); err != nil {
			return err
		}
		if _, _, err = h.sample(ctx); err != nil {
			return err
		}
		if p == nil && stored.boundAt == nil {
			updated, err := tx.Exec(ctx, `UPDATE runtime_capacity_executions SET source_plan_bytes=$5,source_plan_digest=$6,source_plan_bound_at=clock_timestamp()
 WHERE pool_id='local' AND worker_session_id=$1 AND run_id=$2 AND turn_id=$3 AND runtime_id=$4
 AND consumed_at IS NOT NULL AND released_at IS NULL AND source_plan_bound_at IS NULL`, o.WorkerSessionID, o.WorkerRunID, o.AgentTurnID, o.RuntimeInstanceID, []byte(r.SourcePlan.Manifest), r.SourcePlan.Digest)
			if err != nil {
				return err
			}
			if updated.RowsAffected() != 1 {
				return app.CapacityBindingMismatch
			}
		}
		return nil
	})
}

// initialExecution requires an original consumed reservation and current exact
// business ownership. Pool is already locked; Task precedes Session/Run/Turn.
// Consumed execution does not expire with the original unconsumed permit TTL.
func (h *CapacityTx) initialExecution(ctx context.Context, session, run, turn, runtime string, generation uint64) (residentRuntimeRow, app.CapacityTarget, error) {
	var row residentRuntimeRow
	var target app.CapacityTarget
	fail := func(err error) (residentRuntimeRow, app.CapacityTarget, error) { return row, target, err }
	if err := h.valid(ctx); err != nil {
		return fail(err)
	}
	var token, permit string
	err := h.tx.QueryRow(ctx, `SELECT token::text,permit_id FROM runtime_capacity_executions WHERE pool_id='local' AND worker_session_id=$1 AND run_id=$2 AND turn_id=$3 AND runtime_id=$4 AND consumed_at IS NOT NULL AND released_at IS NULL`, session, run, turn, runtime).Scan(&token, &permit)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(app.CapacityBindingMismatch)
	}
	if err != nil {
		return fail(err)
	}
	target, err = h.BoundTaskTarget(ctx, token, permit)
	if err != nil {
		return fail(err)
	}
	if target.SessionID != session || target.Generation != generation {
		return fail(app.CapacityBindingMismatch)
	}
	if err = lockAgentTurnOutcome(ctx, h.tx, turn); err != nil {
		return fail(err)
	}
	var valid bool
	// This lock uses the original target Task, never a caller-supplied kind label.
	var taskTenant, taskActor, workspace, taskSession, taskRun, status string
	var revision uint64
	err = h.tx.QueryRow(ctx, `SELECT tenant_id,initiator_user_id,workspace_id,COALESCE(worker_session_id,''),COALESCE(current_worker_run_id,''),status,revision FROM tasks WHERE task_id=$1 FOR UPDATE`, target.TaskID).Scan(&taskTenant, &taskActor, &workspace, &taskSession, &taskRun, &status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(app.CapacityBindingMismatch)
	}
	if err != nil {
		return fail(err)
	}
	if taskTenant != target.TenantID || taskActor != target.ActorID || taskSession != session || taskRun != run || status != "running" || revision != target.TaskRevision+1 {
		return fail(app.CapacityBindingMismatch)
	}
	row, err = lockResidentRuntime(ctx, h.tx, session, run, turn, runtime)
	if err != nil {
		return fail(err)
	}
	var currentRuntime, currentRun string
	var currentGeneration uint64
	if err = h.tx.QueryRow(ctx, `SELECT COALESCE(active_runtime_instance_id,''),COALESCE(active_worker_run_id,''),runtime_generation FROM worker_sessions WHERE worker_session_id=$1`, session).Scan(&currentRuntime, &currentRun, &currentGeneration); err != nil {
		return fail(err)
	}
	if currentRuntime != runtime || currentRun != run || currentGeneration != generation {
		return fail(platformpostgres.ErrRevisionConflict)
	}
	network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_sessions s JOIN worker_runs r ON r.worker_run_id=$2 JOIN agent_turns t ON t.agent_turn_id=$3 JOIN runtime_instances i ON i.runtime_instance_id=$4 JOIN session_dispatch_permits p ON p.permit_id=$5 JOIN session_task_queue_entries q ON q.worker_session_id=s.worker_session_id AND q.task_id=$6 JOIN runtime_capacity_executions e ON e.token::text=$7 JOIN runtime_capacity_residents c ON c.commitment_id=e.commitment_id
 WHERE s.worker_session_id=$1 AND s.tenant_id=$8 AND s.user_id=$9 AND s.workspace_id=$10 AND s.status='running' AND s.bound_task_id=$6 AND s.active_worker_run_id=$2 AND s.active_runtime_instance_id=$4 AND s.runtime_generation=$11
 AND r.worker_session_id=$1 AND r.task_id=$6 AND r.trigger_type='task_initial' AND r.status IN ('starting','running') AND t.worker_session_id=$1 AND t.worker_run_id=$2 AND t.status IN ('delivered','consumed') AND i.worker_session_id=$1 AND i.worker_run_id=$2 AND i.generation=$11 AND i.status IN ('starting','active') AND i.resource_profile_id=$12 AND i.network_policy_id=$13 AND i.network_policy_revision=$14 AND i.network_policy_digest=$15 AND i.network_enforcement_status='active'
 AND p.worker_session_id=$1 AND p.target_kind='task' AND p.target_id=$6 AND p.status='consumed' AND p.consumed_by_worker_run_id=$2 AND p.capacity_admission_ref=e.token::text AND p.runtime_generation=$11 AND p.expected_session_revision=$16 AND p.session_queue_sequence=$17 AND p.queue_revision=$18 AND p.expires_at=e.expires_at
 AND q.status='dispatched' AND q.session_queue_sequence=$17 AND q.queue_revision=$18
 AND c.origin='reserved' AND c.removal_sequence IS NULL AND c.worker_session_id=$1 AND c.runtime_instance_id=$4 AND c.generation=$11 AND convert_from(c.profile_bytes,'UTF8')::jsonb=convert_from(e.target_bytes,'UTF8')::jsonb->'Profile')`, session, run, turn, runtime, permit, target.TaskID, token, target.TenantID, target.ActorID, workspace, generation, target.Profile.ID, network.ID, network.Revision, network.Digest, target.SessionRevision, target.QueueSequence, target.QueueRevision).Scan(&valid)
	if err != nil {
		return fail(err)
	}
	if !valid {
		return fail(app.CapacityBindingMismatch)
	}
	// The immutable start snapshot must bind the same exact reservation and graph.
	var raw []byte
	if err = h.tx.QueryRow(ctx, `SELECT start_snapshot FROM worker_runs WHERE worker_run_id=$1`, run).Scan(&raw); err != nil {
		return fail(err)
	}
	var snapshot struct {
		Capacity struct {
			Target      app.CapacityTarget
			Reservation app.CapacityReservation
		} `json:"runtime_capacity"`
	}
	original, _, err := h.findReservation(ctx, `token::text=$1`, token)
	if err != nil {
		return fail(err)
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Capacity.Target != target || snapshot.Capacity.Reservation.Token != token || snapshot.Capacity.Reservation.PermitID != permit || snapshot.Capacity.Reservation.RunID != run || snapshot.Capacity.Reservation.TurnID != turn || snapshot.Capacity.Reservation.RuntimeID != runtime || snapshot.Capacity.Reservation.CommitmentID != original.CommitmentID || snapshot.Capacity.Reservation.SnapshotSequence != original.SnapshotSequence || !snapshot.Capacity.Reservation.ExpiresAt.Equal(original.ExpiresAt) || snapshot.Capacity.Reservation.Released || !equalCapacityGuarantee(snapshot.Capacity.Reservation.Guarantee, original.Guarantee) {
		return fail(app.CapacityBindingMismatch)
	}
	return row, target, nil
}

// actualInitialContainer uses signed inventory plus its original resident pin.
// Caller container text is only a comparison, never enforcement evidence.
func (h *CapacityTx) actualInitialContainer(ctx context.Context, row residentRuntimeRow, target app.CapacityTarget) (string, error) {
	f, trust, err := h.sample(ctx)
	if err != nil {
		return "", err
	}
	if err = h.validateResidentIdentities(ctx, f); err != nil {
		return "", err
	}
	p, err := capacityProfile(target.Profile)
	if err != nil {
		return "", err
	}
	for _, c := range f.Containers {
		if c.SessionID != row.WorkerSessionID || c.RuntimeID != row.RuntimeInstanceID || c.Generation != row.RuntimeGeneration {
			continue
		}
		if c.ProfileID != p.ID || c.ProfileRevision != p.Revision || c.ProfileDigest != p.Digest || c.NanoCPUs != uint64(p.CPUUnits)*1_000_000_000 || c.CPUQuotaMicros/c.CPUPeriodMicros != uint64(p.CPUUnits) || c.CPUQuotaMicros%c.CPUPeriodMicros != 0 || c.MemoryMax != uint64(p.MemoryBytes) || c.PIDsMax != uint64(p.PIDsLimit) || c.Layer.HardBytes != uint64(p.WritableLayerBytes) {
			return "", app.CapacityInventoryUnknown
		}
		var pinned bool
		err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_capacity_residents WHERE pool_id='local' AND worker_session_id=$1 AND runtime_instance_id=$2 AND generation=$3 AND engine_id=$4 AND container_id=$5 AND layer_project_id=$6 AND layer_path=$7 AND removal_sequence IS NULL AND origin='reserved')`, row.WorkerSessionID, row.RuntimeInstanceID, row.RuntimeGeneration, trust.Identity.EngineID, c.ContainerID, c.Layer.ProjectID, c.Layer.Path).Scan(&pinned)
		if err != nil {
			return "", err
		}
		if !pinned {
			return "", app.CapacityBindingMismatch
		}
		return c.ContainerID, nil
	}
	return "", app.CapacityContainerProofPending
}

var _ app.InitialRuntimeExecutionAuthorizer = (*InitialRuntimeExecutionAuthorizer)(nil)

func (h *CapacityTx) authorizeStartEnvelope(ctx context.Context, envelope contractsv1.RuntimeControlEnvelope, fresh bool) error {
	row, target, err := h.initialExecution(ctx, string(envelope.WorkerSessionID), string(envelope.WorkerRunID), string(envelope.AgentTurnID), string(envelope.RuntimeInstanceID), uint64(envelope.RuntimeGeneration))
	if err != nil {
		return err
	}
	var otherGrant, conflictingKey bool
	if err = h.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='command' AND envelope->>'message_type'='agent_turn.start' AND envelope->>'agent_turn_id'=$2 AND (message_id<>$3 OR idempotency_key<>$4)), EXISTS(SELECT 1 FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='command' AND ((message_id=$3 AND idempotency_key<>$4) OR (message_id<>$3 AND idempotency_key=$4)))`, string(envelope.RuntimeInstanceID), string(envelope.AgentTurnID), envelope.MessageID, envelope.IdempotencyKey).Scan(&otherGrant, &conflictingKey); err != nil {
		return err
	}
	if conflictingKey {
		return app.ErrRuntimeControlMessageConflict
	}
	if otherGrant {
		return app.CapacityBindingMismatch
	}
	var agentSession string
	if err = h.tx.QueryRow(ctx, `SELECT agent_session_id FROM agent_turns WHERE agent_turn_id=$1`, string(envelope.AgentTurnID)).Scan(&agentSession); err != nil {
		return err
	}
	if row.RuntimeStatus != "active" || row.RunStatus != "running" || row.ContainerID == "" || row.ControlChannelEpoch != envelope.ChannelEpoch || agentSession != string(envelope.AgentSessionID) {
		return app.CapacityBindingMismatch
	}
	if fresh {
		container, err := h.actualInitialContainer(ctx, row, target)
		if err != nil {
			return err
		}
		if container != row.ContainerID {
			return app.CapacityBindingMismatch
		}
	}
	return nil
}

// Validate starts before any ACK, duplicate return or replay can grant delivery.
// The worker may omit ActiveTurn while an unreceived command is still pending.
// Active/terminal proofs match the original envelope; they never grant another
// Prompt and do not depend on the freshness of an old capacity sample.
func (h *CapacityTx) authorizeControlStarts(ctx context.Context, runtime string, reported *contractsv1.RuntimeControlActiveTurn) error {
	rows, err := h.tx.Query(ctx, `SELECT m.envelope,m.fingerprint,m.effect_status FROM runtime_control_messages m JOIN runtime_control_channels c ON c.runtime_instance_id=m.runtime_instance_id WHERE m.runtime_instance_id=$1 AND m.direction='command' AND m.envelope->>'message_type'='agent_turn.start' AND (m.sequence>c.command_applied_sequence OR m.message_id=c.active_effect_message_id OR m.envelope->>'agent_turn_id'=$2) ORDER BY m.sequence`, runtime, func() string {
		if reported != nil {
			return string(reported.AgentTurnID)
		}
		return ""
	}())
	if err != nil {
		return err
	}
	type start struct {
		raw            []byte
		digest, status string
	}
	var starts []start
	for rows.Next() {
		var s start
		if err = rows.Scan(&s.raw, &s.digest, &s.status); err != nil {
			rows.Close()
			return err
		}
		starts = append(starts, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	matched := reported == nil
	for _, s := range starts {
		e, err := contractsv1.DecodeRuntimeControlEnvelope(s.raw)
		if err != nil {
			return err
		}
		exact := reported != nil && reported.StartMessageID == e.MessageID && reported.StartIdempotencyKey == e.IdempotencyKey && reported.StartCommandSequence == e.Sequence && reported.StartEnvelopeDigest == s.digest && reported.WorkerRunID == e.WorkerRunID && reported.AgentTurnID == e.AgentTurnID && reported.AgentSessionID == e.AgentSessionID
		if exact {
			matched = true
		}
		// Terminal proof remains replayable even after release. Existing journal
		// continuity checks still prove the exact immutable envelope and cursors.
		if exact && reported.Phase == contractsv1.RuntimeControlTurnEffectTerminal {
			continue
		}
		if s.status == "terminal" {
			continue
		}
		if reported != nil && reported.StartCommandSequence == e.Sequence && !exact {
			return app.ErrRuntimeControlIdentityConflict
		}
		if exact {
			var status string
			if err = h.tx.QueryRow(ctx, `SELECT status FROM agent_turns WHERE agent_turn_id=$1`, string(e.AgentTurnID)).Scan(&status); err != nil {
				return err
			}
			if terminalTurnStatus(status) {
				return app.ErrRuntimeControlIdentityConflict
			}
		}
		active := exact && reported.Phase == contractsv1.RuntimeControlTurnEffectActive
		if err = h.authorizeStartEnvelope(ctx, e, !active && s.status != "active"); err != nil {
			return err
		}
	}
	if !matched {
		return app.ErrRuntimeControlIdentityConflict
	}
	return nil
}
