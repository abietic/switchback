package localproduct

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func (runner *runtimeRunner) reconcileBackendDeadlineTurnSettlement(
	ctx context.Context,
) (bool, error) {
	if runner == nil || runner.pool == nil || runner.now == nil ||
		runner.settlerID == "" {
		return false, fmt.Errorf(
			"Backend deadline Turn settlement runner is unavailable",
		)
	}
	var settlementID string
	err := runner.pool.QueryRow(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE tenant_id=$1
  AND source_kind='backend_deadline'
  AND source_state->>'phase'='cleanup_confirmed'
  AND phase <> 'completed'
  AND (
    lease_owner_id IS NULL
    OR lease_expires_at <= $2
    OR lease_owner_id=$3
  )
ORDER BY source_occurred_at,settlement_id
LIMIT 1`,
		runner.configuration.Profile.TenantID,
		runner.now().UTC(),
		runner.settlerID,
	).Scan(&settlementID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"select Backend deadline Turn settlement: %w",
			err,
		)
	}
	return runner.reconcileBackendDeadlineTurnSettlementByID(
		ctx,
		settlementID,
	)
}

func (runner *runtimeRunner) reconcileBackendDeadlineTurnSettlementByID(
	ctx context.Context,
	settlementID string,
) (bool, error) {
	if runner == nil || runner.settlements.Store == nil ||
		runner.now == nil || runner.settlerID == "" ||
		settlementID == "" {
		return false, fmt.Errorf(
			"Backend deadline Turn settlement runner is unavailable",
		)
	}
	current, err := runner.settlements.Load(ctx, settlementID)
	if err != nil {
		return true, err
	}
	if current.Phase == workerapp.TurnSettlementCompleted {
		return false, nil
	}
	now := runner.now().UTC()
	if current.LeaseOwnerID != "" &&
		current.LeaseOwnerID != runner.settlerID &&
		current.LeaseExpiresAt.After(now) {
		return true, workerapp.ErrTurnSettlementLeaseHeld
	}
	if _, err := terminalResultFromBackendDeadlineCleanup(current); err != nil {
		return true, err
	}
	live, err := runner.backendDeadlineSettlementLive(ctx, current)
	if err != nil {
		return true, err
	}
	_, err = runner.reconcilePendingTurnSettlement(
		ctx,
		current,
		live,
	)
	return true, err
}

func runtimeSettlementRuntimeAuthority(
	settlement workerapp.TurnSettlement,
) (string, uint64, uint64, error) {
	switch settlement.Source.Kind {
	case workerapp.TurnSettlementSourceRuntimeEvent:
		if settlement.RuntimeInstanceID == "" ||
			settlement.RuntimeGeneration == 0 ||
			settlement.ChannelEpoch == 0 {
			return "", 0, 0,
				workerapp.ErrTurnSettlementConflict
		}
		return settlement.RuntimeInstanceID,
			settlement.RuntimeGeneration,
			settlement.ChannelEpoch,
			nil
	case workerapp.TurnSettlementSourceBackendDeadline:
		state, err := workerapp.DecodeBackendDeadlineSourceState(
			settlement.Source,
		)
		if err != nil {
			return "", 0, 0, err
		}
		hasNoWitness := settlement.RuntimeInstanceID == "" &&
			settlement.RuntimeGeneration == 0 &&
			settlement.ChannelEpoch == 0
		hasExactWitness := settlement.RuntimeInstanceID ==
			state.RuntimeInstanceID &&
			settlement.RuntimeGeneration ==
				state.RuntimeGeneration &&
			settlement.ChannelEpoch == state.ChannelEpoch
		if !hasNoWitness && !hasExactWitness {
			return "", 0, 0,
				workerapp.ErrTurnSettlementConflict
		}
		return state.RuntimeInstanceID,
			state.RuntimeGeneration,
			state.ChannelEpoch,
			nil
	default:
		return "", 0, 0,
			workerapp.ErrTurnSettlementConflict
	}
}

func terminalResultFromBackendDeadlineCleanup(
	settlement workerapp.TurnSettlement,
) (turnTerminalResult, error) {
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline ||
		settlement.Source.Reason != "agent_turn_deadline" ||
		settlement.Source.OccurredAt.IsZero() {
		return turnTerminalResult{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	state, err := workerapp.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil ||
		state.Phase !=
			workerapp.BackendDeadlineSourceCleanupConfirmed ||
		state.ForceStop == nil ||
		state.WriteFence == nil ||
		state.Cleanup == nil {
		return turnTerminalResult{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	return turnTerminalResult{
		reason:     "agent_turn_deadline",
		timedOut:   true,
		forcedStop: true,
		sourceKind: workerapp.TurnOutcomeSourceBackendDeadline,
		occurredAt: settlement.Source.OccurredAt,
	}, nil
}

func (runner *runtimeRunner) backendDeadlineSettlementLive(
	ctx context.Context,
	settlement workerapp.TurnSettlement,
) (*liveRuntime, error) {
	if runner == nil || runner.environment == nil || runner.pool == nil {
		return nil, fmt.Errorf(
			"Backend deadline settlement environment is unavailable",
		)
	}
	runtimeID, generation, epoch, err :=
		runtimeSettlementRuntimeAuthority(settlement)
	if err != nil || generation > uint64(^uint32(0)) {
		if err != nil {
			return nil, err
		}
		return nil, workerapp.ErrTurnSettlementConflict
	}
	storage, err := runner.environment.storage.OpenExisting(
		settlement.WorkerSessionID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"open Backend deadline settlement storage: %w",
			err,
		)
	}
	var checkpointDigest string
	err = runner.pool.QueryRow(ctx, `
WITH RECURSIVE lineage AS (
  SELECT seed.worker_run_id,seed.previous_worker_run_id,
         seed.start_snapshot,0 AS depth
  FROM worker_runs seed
  WHERE seed.worker_run_id=$1
  UNION ALL
  SELECT previous.worker_run_id,previous.previous_worker_run_id,
         previous.start_snapshot,lineage.depth+1
  FROM lineage
  JOIN worker_runs previous
    ON previous.worker_run_id=lineage.previous_worker_run_id
  WHERE lineage.depth < 64
)
SELECT lineage.start_snapshot->>'working_copy_checkpoint_digest'
FROM lineage
WHERE lineage.start_snapshot->>'working_copy_checkpoint_digest'
  ~ '^sha256:[0-9a-f]{64}$'
ORDER BY lineage.depth
LIMIT 1`,
		settlement.WorkerRunID,
	).Scan(&checkpointDigest)
	if err != nil {
		return nil, fmt.Errorf(
			"read Backend deadline WorkingCopy checkpoint: %w",
			err,
		)
	}
	sourceDigest := strings.TrimPrefix(checkpointDigest, "sha256:")
	if len(sourceDigest) != 64 ||
		checkpointDigest != "sha256:"+sourceDigest {
		return nil, workerapp.ErrTurnSettlementConflict
	}
	return &liveRuntime{
		identity: controlgrpc.RuntimeIdentity{
			WorkerSessionID:   settlement.WorkerSessionID,
			RuntimeInstanceID: runtimeID,
			RuntimeGeneration: uint32(generation),
			ChannelEpoch:      epoch,
		},
		agentSessionID: settlement.AgentSessionID,
		storage:        storage,
		sourceDigest:   sourceDigest,
	}, nil
}
