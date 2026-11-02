package postgres

import (
	"context"
	"encoding/json"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func (store *QueuedTurnStore) ContinueSettlementQueuedTurn(ctx context.Context, settlement application.TurnSettlement, record application.ContinueQueuedTurnRecord) (application.ContinuedQueuedTurn, error) {
	var result application.ContinuedQueuedTurn
	err := platformpostgres.InTransaction(ctx, store.pool, func(tx platformpostgres.Tx) error {
		proof, err := LockSettlementEffect(ctx, tx, settlement, application.TurnSettlementTerminalEventApplied)
		if err != nil {
			return err
		}
		i, p, c := proof.EvidenceInput, proof.ContinuationPlan, record.Command
		if proof.ForcedStop || p.Action != "create_queued_turn" || c.TenantID != i.TenantID || c.UserID != i.UserID || c.WorkerSessionID != i.WorkerSessionID || c.PreviousWorkerRunID != i.WorkerRunID || c.PreviousAgentTurnID != i.AgentTurnID || c.RuntimeInstanceID != i.RuntimeInstanceID || c.RuntimeGeneration != i.RuntimeGeneration || c.QueueCommandID != p.QueueCommandID || c.ExpectedSessionRevision != p.ExpectedSessionRevision || c.ExpectedRunRevision != p.ExpectedRunRevision || c.ExpectedTurnRevision != p.ExpectedTurnRevision || c.ExpectedRuntimeRevision != p.ExpectedRuntimeRevision {
			return application.ErrTurnSettlementCheckpointConflict
		}
		if err := lockQueuedTurnTask(ctx, tx, c); err != nil {
			return err
		}
		var status, tenant, user, session, run, turn string
		var outcome json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT status,tenant_id,user_id,worker_session_id,worker_run_id,agent_turn_id,outcome FROM worker_user_commands WHERE command_id=$1 AND command_type='queue' FOR UPDATE`, c.QueueCommandID).Scan(&status, &tenant, &user, &session, &run, &turn, &outcome); err != nil {
			return err
		}
		if tenant != i.TenantID || user != i.UserID || session != i.WorkerSessionID || run != i.WorkerRunID || turn != i.AgentTurnID {
			return application.ErrTurnSettlementConflict
		}
		if status == "applied" {
			if err := validateSettlementContinuationReplay(ctx, tx, settlement, proof, outcome); err != nil {
				return err
			}
		} else {
			revision, err := LockSettlementEffectRuntime(ctx, tx, settlement, proof, application.TurnSettlementDomainProof{SessionRevision: p.ExpectedSessionRevision, RunRevision: p.ExpectedRunRevision, TurnRevision: p.ExpectedTurnRevision, RuntimeRevision: p.ExpectedRuntimeRevision})
			if err != nil {
				return err
			}
			record.Command.ExpectedRuntimeRevision = revision
		}
		result, err = store.continueQueuedTurnTx(ctx, tx, record)
		if err != nil {
			return err
		}
		return ValidateSettlementEffectLease(ctx, tx, settlement)
	})
	return result, err
}

func validateSettlementContinuationReplay(ctx context.Context, tx platformpostgres.Tx, settlement application.TurnSettlement, proof SettlementEffectProof, encoded json.RawMessage) error {
	var outcome struct {
		WorkerRunID       string `json:"worker_run_id"`
		AgentTurnID       string `json:"agent_turn_id"`
		RuntimeInstanceID string `json:"runtime_instance_id"`
		RuntimeGeneration uint64 `json:"runtime_generation"`
		SessionRevision   uint64 `json:"session_revision"`
		RunRevision       uint64 `json:"run_revision"`
		TurnRevision      uint64 `json:"turn_revision"`
		RuntimeRevision   uint64 `json:"runtime_revision"`
	}
	if err := json.Unmarshal(encoded, &outcome); err != nil {
		return err
	}
	i, p := proof.EvidenceInput, proof.ContinuationPlan
	if outcome.WorkerRunID == "" || outcome.AgentTurnID == "" || outcome.RuntimeInstanceID != i.RuntimeInstanceID || outcome.RuntimeGeneration != i.RuntimeGeneration || outcome.SessionRevision != p.ExpectedSessionRevision+1 || outcome.RuntimeRevision <= p.ExpectedRuntimeRevision || outcome.RunRevision != 1 || outcome.TurnRevision != 1 {
		return application.ErrTurnSettlementConflict
	}
	var sessionID string
	if err := tx.QueryRow(ctx, `SELECT worker_session_id FROM worker_sessions WHERE worker_session_id=$1 FOR UPDATE`, i.WorkerSessionID).Scan(&sessionID); err != nil {
		return err
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT oldrun.revision=$3 AND oldturn.revision=$4 AND oldturn.agent_session_id=$5
 AND oldrun.worker_session_id=$6 AND oldturn.worker_run_id=oldrun.worker_run_id
 FROM worker_runs oldrun JOIN agent_turns oldturn ON oldturn.agent_turn_id=$2
 WHERE oldrun.worker_run_id=$1 FOR UPDATE OF oldrun,oldturn`, i.WorkerRunID, i.AgentTurnID, p.ExpectedRunRevision, p.ExpectedTurnRevision, i.AgentSessionID, i.WorkerSessionID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return platformpostgres.ErrRevisionConflict
	}
	row, err := lockResidentRuntime(ctx, tx, i.WorkerSessionID, outcome.WorkerRunID, outcome.AgentTurnID, i.RuntimeInstanceID)
	if err != nil {
		return err
	}
	if row.SessionRevision != outcome.SessionRevision || row.RunRevision != outcome.RunRevision || row.TurnRevision != outcome.TurnRevision || row.RuntimeRevision < outcome.RuntimeRevision || row.RuntimeGeneration != i.RuntimeGeneration || row.ControlChannelEpoch != settlement.ChannelEpoch || row.RuntimeStatus != "active" {
		return platformpostgres.ErrRevisionConflict
	}
	if err := tx.QueryRow(ctx, `SELECT s.tenant_id=$2 AND s.user_id=$3 AND s.workspace_id=$4 AND s.active_worker_run_id=$5 AND s.active_runtime_instance_id=$6 AND s.runtime_generation=$7 AND r.previous_worker_run_id=$8 AND t.agent_session_id=$9 AND t.idempotency_key=$10 FROM worker_sessions s JOIN worker_runs r ON r.worker_run_id=$5 JOIN agent_turns t ON t.agent_turn_id=$11 WHERE s.worker_session_id=$1`, i.WorkerSessionID, i.TenantID, i.UserID, i.WorkspaceID, outcome.WorkerRunID, i.RuntimeInstanceID, i.RuntimeGeneration, i.WorkerRunID, i.AgentSessionID, "queued-turn/"+p.QueueCommandID, outcome.AgentTurnID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return platformpostgres.ErrRevisionConflict
	}
	channel, err := readRuntimeControlChannel(ctx, tx, i.RuntimeInstanceID, true)
	if err != nil {
		return err
	}
	if channel.Status == "fenced" || channel.ChannelEpoch != settlement.ChannelEpoch || channel.RuntimeGeneration != i.RuntimeGeneration || channel.WorkerSessionID != i.WorkerSessionID {
		return platformpostgres.ErrRevisionConflict
	}
	return nil
}
