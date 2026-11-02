package postgres

import (
	"context"
	"encoding/json"
	"time"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

// SettlementEffectProof is the frozen projection shared by PostgreSQL effect
// owners. It grants no authority without LockSettlementEffect in the same TX.
type SettlementEffectProof struct {
	EvidenceInput struct {
		TenantID          string `json:"tenant_id"`
		UserID            string `json:"user_id"`
		WorkspaceID       string `json:"workspace_id"`
		WorkerSessionID   string `json:"worker_session_id"`
		WorkerRunID       string `json:"worker_run_id"`
		AgentTurnID       string `json:"agent_turn_id"`
		AgentSessionID    string `json:"agent_session_id"`
		RuntimeInstanceID string `json:"runtime_instance_id"`
		RuntimeGeneration uint64 `json:"runtime_generation"`
	} `json:"evidence_input"`
	Evidence struct {
		Finalized json.RawMessage `json:"finalized"`
	} `json:"evidence"`
	Domain     application.TurnSettlementDomainProof `json:"domain"`
	DomainPlan struct {
		SessionTarget string `json:"session_target"`
		RunTarget     string `json:"run_target"`
		TurnTarget    string `json:"turn_target"`
	} `json:"domain_plan"`
	ContinuationPlan struct {
		Action                  string `json:"action"`
		QueueCommandID          string `json:"queue_command_id"`
		ExpectedSessionRevision uint64 `json:"expected_session_revision"`
		ExpectedRunRevision     uint64 `json:"expected_run_revision"`
		ExpectedTurnRevision    uint64 `json:"expected_turn_revision"`
		ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision"`
	} `json:"continuation_plan"`
	ForcedStop bool `json:"-"`
}

// LockSettlementEffect precedes Task/queue and graph locks. Reusing the normal
// reader also validates durable source state and the exact terminal witness.
func LockSettlementEffect(ctx context.Context, tx platformpostgres.Tx, expected application.TurnSettlement, phase application.TurnSettlementPhase) (SettlementEffectProof, error) {
	var proof SettlementEffectProof
	if err := lockAgentTurnOutcome(ctx, tx, expected.AgentTurnID); err != nil {
		return proof, err
	}
	current, err := readTurnSettlement(ctx, tx, expected.SettlementID, true)
	if err != nil {
		return proof, err
	}
	if current.Phase != phase || expected.Phase != phase {
		return proof, application.ErrTurnSettlementPhaseConflict
	}
	if current.Revision != expected.Revision {
		return proof, application.ErrTurnSettlementRevisionConflict
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return proof, err
	}
	if current.LeaseOwnerID == "" || current.LeaseOwnerID != expected.LeaseOwnerID || current.LeaseFence != expected.LeaseFence || !current.LeaseExpiresAt.After(now) {
		return proof, application.ErrTurnSettlementLeaseStale
	}
	_, digest, err := application.CanonicalizeTurnSettlementCheckpoint(current.PhaseState)
	if err != nil || digest != current.PhaseStateDigest || digest != expected.PhaseStateDigest {
		return proof, application.ErrTurnSettlementCheckpointConflict
	}
	if current.TenantID != expected.TenantID || current.WorkerSessionID != expected.WorkerSessionID || current.WorkerRunID != expected.WorkerRunID || current.AgentTurnID != expected.AgentTurnID || current.AgentSessionID != expected.AgentSessionID || current.RuntimeInstanceID != expected.RuntimeInstanceID || current.RuntimeGeneration != expected.RuntimeGeneration || current.ChannelEpoch != expected.ChannelEpoch {
		return proof, application.ErrTurnSettlementConflict
	}
	if err := application.ValidateTurnSettlementCheckpointProof(phase, current.PhaseState); err != nil {
		return proof, err
	}
	if err := json.Unmarshal(current.PhaseState, &proof); err != nil {
		return proof, err
	}
	i := proof.EvidenceInput
	runtimeID, forced, err := turnSettlementDomainRuntimeAuthority(current)
	if err != nil {
		return proof, err
	}
	proof.ForcedStop = forced
	if i.TenantID != current.TenantID || i.WorkerSessionID != current.WorkerSessionID || i.WorkerRunID != current.WorkerRunID || i.AgentTurnID != current.AgentTurnID || i.AgentSessionID != current.AgentSessionID || i.RuntimeInstanceID != runtimeID || i.RuntimeGeneration == 0 || i.UserID == "" || i.WorkspaceID == "" {
		return proof, application.ErrTurnSettlementConflict
	}
	return proof, nil
}

// LockSettlementEffectRuntime checks exact business state and current execution
// identity. Only its transaction-local Runtime revision may replace a frozen CAS.
func LockSettlementEffectRuntime(ctx context.Context, tx platformpostgres.Tx, settlement application.TurnSettlement, proof SettlementEffectProof, revisions application.TurnSettlementDomainProof) (uint64, error) {
	i := proof.EvidenceInput
	var sessionID string
	if err := tx.QueryRow(ctx, `SELECT worker_session_id FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, i.WorkerSessionID).Scan(&sessionID); err != nil {
		return 0, err
	}
	row, err := lockResidentRuntime(ctx, tx, i.WorkerSessionID, i.WorkerRunID, i.AgentTurnID, i.RuntimeInstanceID)
	if err != nil {
		return 0, err
	}
	if row.SessionRevision != revisions.SessionRevision || row.RunRevision != revisions.RunRevision || row.TurnRevision != revisions.TurnRevision || row.RuntimeRevision < revisions.RuntimeRevision || string(row.SessionStatus) != proof.DomainPlan.SessionTarget || string(row.RunStatus) != proof.DomainPlan.RunTarget || string(row.TurnStatus) != proof.DomainPlan.TurnTarget || row.RuntimeStatus != "active" || row.RuntimeGeneration != i.RuntimeGeneration || row.RuntimeGeneration != settlement.RuntimeGeneration || row.ControlChannelEpoch != settlement.ChannelEpoch {
		return 0, platformpostgres.ErrRevisionConflict
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT session.tenant_id=$2 AND session.user_id=$3 AND session.workspace_id=$4
 AND session.active_runtime_instance_id=$5 AND session.runtime_generation=$6
 AND session.active_worker_run_id IS NULL AND turn.agent_session_id=$7
 FROM worker_sessions session JOIN agent_turns turn ON turn.agent_turn_id=$8
 WHERE session.worker_session_id=$1`, i.WorkerSessionID, i.TenantID, i.UserID, i.WorkspaceID, i.RuntimeInstanceID, i.RuntimeGeneration, i.AgentSessionID, i.AgentTurnID).Scan(&valid); err != nil {
		return 0, err
	}
	if !valid {
		return 0, platformpostgres.ErrRevisionConflict
	}
	channel, err := readRuntimeControlChannel(ctx, tx, i.RuntimeInstanceID, true)
	if err != nil {
		return 0, err
	}
	if channel.WorkerSessionID != i.WorkerSessionID || channel.RuntimeGeneration != i.RuntimeGeneration || channel.ChannelEpoch != settlement.ChannelEpoch || channel.Status == "fenced" {
		return 0, platformpostgres.ErrRevisionConflict
	}
	return row.RuntimeRevision, nil
}

// ValidateSettlementEffectLease is the final transaction check, after all
// business writes (which may themselves wait on owner-stream or outbox locks).
// Expiry rolls back the whole effect; the authority row is still locked.
func ValidateSettlementEffectLease(ctx context.Context, tx platformpostgres.Tx, settlement application.TurnSettlement) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT lease_expires_at > clock_timestamp()
 AND lease_owner_id=$2 AND lease_fence=$3 AND revision=$4
 FROM worker_turn_settlements WHERE settlement_id=$1`, settlement.SettlementID, settlement.LeaseOwnerID, settlement.LeaseFence, settlement.Revision).Scan(&live); err != nil {
		return err
	}
	if !live {
		return application.ErrTurnSettlementLeaseStale
	}
	return nil
}
