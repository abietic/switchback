package localproduct

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const runtimeSettlementEventReplayTimeout = 5 * time.Second

var errRuntimeSettlementOwnerBusy = errors.New(
	"Runtime Turn settlement is already owned by the live Runtime supervisor",
)

func (runner *runtimeRunner) runtimeHasPendingTurnSettlement(
	ctx context.Context,
	workerSessionID string,
) (bool, error) {
	var pending bool
	err := runner.pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM worker_turn_settlements
  WHERE tenant_id=$1
    AND worker_session_id=$2
    AND phase <> 'completed'
)`,
		runner.configuration.Profile.TenantID,
		workerSessionID,
	).Scan(&pending)
	return pending, err
}

func runtimeSettlementNeedsTerminalDelivery(
	phase workerapp.TurnSettlementPhase,
) bool {
	switch phase {
	case workerapp.TurnSettlementAccepted,
		workerapp.TurnSettlementEvidencePrepared,
		workerapp.TurnSettlementDomainCommitted,
		workerapp.TurnSettlementEvidencePublished,
		workerapp.TurnSettlementTaskSettled:
		return true
	default:
		return false
	}
}

func (runner *runtimeRunner) readRuntimeSettlementTurn(
	ctx context.Context,
	settlement workerapp.TurnSettlement,
) (pendingTurn, error) {
	var turn pendingTurn
	var channelEpoch uint64
	runtimeInstanceID, runtimeGeneration, expectedChannelEpoch, err :=
		runtimeSettlementRuntimeAuthority(settlement)
	if err != nil {
		return pendingTurn{}, err
	}
	err = runner.pool.QueryRow(ctx, `
SELECT session.tenant_id,session.user_id,session.workspace_id,
       session.session_revision,
       runtime_run.revision,COALESCE(runtime_run.task_id,''),
       COALESCE(task.revision,0),
       turn.revision,turn.agent_session_id,
       COALESCE(turn.started_at,turn.created_at),
       COALESCE(turn.deadline_at,turn.created_at + interval '24 hours'),
       turn.content,
       runtime.revision,runtime.generation,runtime.control_channel_epoch,
       runtime.network_policy_id,runtime.network_policy_revision,
       runtime.network_policy_digest,runtime.network_enforcement_status
FROM worker_sessions session
JOIN worker_runs runtime_run
  ON runtime_run.worker_session_id=session.worker_session_id
 AND runtime_run.worker_run_id=$3
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=runtime_run.worker_run_id
 AND turn.agent_turn_id=$4
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.runtime_instance_id=$5
LEFT JOIN tasks task ON task.task_id=runtime_run.task_id
WHERE session.tenant_id=$1
  AND session.worker_session_id=$2`,
		settlement.TenantID,
		settlement.WorkerSessionID,
		settlement.WorkerRunID,
		settlement.AgentTurnID,
		runtimeInstanceID,
	).Scan(
		&turn.TenantID,
		&turn.UserID,
		&turn.WorkspaceID,
		&turn.SessionRevision,
		&turn.RunRevision,
		&turn.TaskID,
		&turn.TaskRevision,
		&turn.TurnRevision,
		&turn.AgentSessionID,
		&turn.StartedAt,
		&turn.DeadlineAt,
		&turn.Content,
		&turn.RuntimeRevision,
		&turn.RuntimeGeneration,
		&channelEpoch,
		&turn.NetworkPolicyID,
		&turn.NetworkPolicyRevision,
		&turn.NetworkPolicyDigest,
		&turn.NetworkEnforcementStatus,
	)
	if err != nil {
		return pendingTurn{}, fmt.Errorf(
			"read Runtime Turn settlement domain identity: %w",
			err,
		)
	}
	turn.WorkerSessionID = settlement.WorkerSessionID
	turn.WorkerRunID = settlement.WorkerRunID
	turn.AgentTurnID = settlement.AgentTurnID
	turn.RuntimeInstanceID = runtimeInstanceID
	if turn.AgentSessionID != settlement.AgentSessionID ||
		turn.RuntimeGeneration != runtimeGeneration ||
		channelEpoch != expectedChannelEpoch {
		return pendingTurn{}, fmt.Errorf(
			"Runtime Turn settlement domain authority changed",
		)
	}
	return turn, nil
}

func (runner *runtimeRunner) waitRuntimeSettlementTerminalDelivery(
	ctx context.Context,
	live *liveRuntime,
	settlement workerapp.TurnSettlement,
	turn pendingTurn,
) (*workerapp.WorkerControlEvent, error) {
	if live == nil || live.events == nil {
		return nil, fmt.Errorf(
			"pending Runtime Turn settlement has no live event stream",
		)
	}
	waitCtx, cancel := context.WithTimeout(
		ctx,
		runtimeSettlementEventReplayTimeout,
	)
	defer cancel()
	for {
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf(
				"wait for durable terminal event replay: %w",
				waitCtx.Err(),
			)
		case event, open := <-live.events:
			if !open {
				return nil, fmt.Errorf(
					"pending Runtime Turn settlement event stream closed",
				)
			}
			if event.Envelope.Sequence <
				settlement.TerminalEventSequence {
				if err := runner.applyRuntimeSettlementPredecessorEvent(
					ctx,
					live,
					turn,
					event,
				); err != nil {
					return nil, err
				}
				continue
			}
			if err := validateRuntimeSettlementEventHeader(
				event.Envelope,
				settlement,
			); err != nil {
				if event.Delivery != nil {
					event.Delivery.ReleaseForRetry()
				}
				return nil, err
			}
			if event.Delivery == nil {
				return nil, fmt.Errorf(
					"pending Runtime Turn settlement event lacks delivery",
				)
			}
			return &event, nil
		}
	}
}

func (runner *runtimeRunner) applyRuntimeSettlementPredecessorEvent(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	event workerapp.WorkerControlEvent,
) error {
	if event.Delivery == nil {
		return fmt.Errorf(
			"Runtime Turn settlement predecessor event lacks delivery",
		)
	}
	if event.Envelope.AgentTurnID !=
		contractsv1.AgentTurnID(turn.AgentTurnID) {
		return ignoreWorkerControlEvent(
			ctx,
			event,
			"ignored_settlement_predecessor_turn",
		)
	}
	switch event.Envelope.MessageType {
	case "agent_turn.effect":
		return processWorkerControlEffectEvent(
			ctx,
			event,
			"agent_turn_effect_committed",
			"commit",
			func() error { return nil },
		)
	case "agent.update":
		return processWorkerControlEvent(
			ctx,
			event,
			"agent_update_committed",
			func() error { return nil },
		)
	default:
		event.Delivery.ReleaseForRetry()
		return fmt.Errorf(
			"unsupported Runtime Turn settlement predecessor event %q",
			event.Envelope.MessageType,
		)
	}
}

func (runner *runtimeRunner) reconcilePendingTurnSettlement(
	ctx context.Context,
	current workerapp.TurnSettlement,
	live *liveRuntime,
) (runtimeTurnSettlementCheckpoint, error) {
	turn, err := runner.readRuntimeSettlementTurn(ctx, current)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	checkpoint, err := decodeRuntimeTurnSettlementCheckpoint(
		current.Phase,
		current.PhaseState,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	var terminal turnTerminalResult
	var sourceApplyAttempted bool
	needsTerminalDelivery, err :=
		runtimeSettlementRequiresTerminalDelivery(current)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	if needsTerminalDelivery {
		event, err := runner.waitRuntimeSettlementTerminalDelivery(
			ctx,
			live,
			current,
			turn,
		)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
		defer func() {
			if !sourceApplyAttempted {
				event.Delivery.ReleaseForRetry()
			}
		}()
		classified, recoveryRequired, err :=
			terminalResultFromRuntimeSettlementEvent(
				event,
				current,
				turn,
			)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
		if recoveryRequired {
			// Apply(ignore) must durably close the ordered terminal inbox
			// before RuntimeRecovery may claim the same AgentTurn. Mark the
			// delivery handled before the Apply attempt because the concrete
			// journal delivery owns any retry from that point onward.
			sourceApplyAttempted = true
			opened, recoveryErr := runner.applyACPEOFRecoverySignal(
				ctx,
				live,
				turn,
				*event,
				current.AcceptedAt,
			)
			if recoveryErr != nil {
				return runtimeTurnSettlementCheckpoint{}, recoveryErr
			}
			if !opened {
				return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
					"pending Runtime recovery signal could not open recovery",
				)
			}
			return checkpoint, errRuntimeRecoverySettled
		}
		terminal = classified
		if checkpoint.Source != nil {
			durableTerminal, err :=
				terminalResultFromRuntimeSettlementCheckpoint(
					checkpoint,
				)
			if err != nil {
				return runtimeTurnSettlementCheckpoint{}, err
			}
			if durableTerminal.reason != terminal.reason ||
				durableTerminal.timedOut != terminal.timedOut ||
				durableTerminal.forcedStop != terminal.forcedStop ||
				durableTerminal.sourceKind != terminal.sourceKind ||
				!durableTerminal.occurredAt.Equal(
					terminal.occurredAt,
				) {
				return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
					"durable Runtime Turn settlement source changed",
				)
			}
		}
		if err := runner.finishInterruptCommands(
			ctx,
			turn,
			terminal.reason,
		); err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
	} else {
		if checkpoint.Source == nil &&
			current.Source.Kind ==
				workerapp.TurnSettlementSourceBackendDeadline {
			terminal, err =
				terminalResultFromBackendDeadlineCleanup(current)
			if err == nil {
				err = runner.finishInterruptCommands(
					ctx,
					turn,
					terminal.reason,
				)
			}
		} else {
			terminal, err =
				terminalResultFromRuntimeSettlementCheckpoint(checkpoint)
		}
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
	}
	current, err = runner.settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			OwnerID:          runner.settlerID,
		},
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	settled, applyAttempted, err :=
		runner.reconcileClaimedRuntimeEventTurnSettlement(
			ctx,
			current,
			live,
			turn,
			terminal,
		)
	sourceApplyAttempted = applyAttempted
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	if settled.EvidenceInput == nil ||
		settled.Evidence == nil ||
		settled.DomainPlan == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"completed Runtime Turn settlement lacks durable outcome",
		)
	}
	if live.traceOffset < settled.EvidenceInput.TraceEndOffset {
		live.traceOffset = settled.EvidenceInput.TraceEndOffset
	}
	return settled, nil
}

func runtimeSettlementRequiresTerminalDelivery(
	settlement workerapp.TurnSettlement,
) (bool, error) {
	needs := runtimeSettlementNeedsTerminalDelivery(settlement.Phase)
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline {
		return needs, nil
	}
	state, err := workerapp.DecodeBackendDeadlineSourceState(
		settlement.Source,
	)
	if err != nil {
		return false, err
	}
	switch state.Phase {
	case workerapp.BackendDeadlineSourceTerminalObserved:
		return needs, nil
	case workerapp.BackendDeadlineSourceCleanupConfirmed:
		return false, nil
	default:
		return false, workerapp.ErrTurnSettlementSourceConflict
	}
}

func terminalResultFromRuntimeSettlementCheckpoint(
	checkpoint runtimeTurnSettlementCheckpoint,
) (turnTerminalResult, error) {
	if checkpoint.Source == nil ||
		checkpoint.Source.OccurredAt.IsZero() {
		return turnTerminalResult{}, fmt.Errorf(
			"pending Runtime Turn settlement source proof is missing",
		)
	}
	sourceKind := workerapp.TurnOutcomeSource(
		checkpoint.Source.Kind,
	)
	switch sourceKind {
	case workerapp.TurnOutcomeSourceRuntimeEvent:
		if checkpoint.Source.TimedOut {
			return turnTerminalResult{}, fmt.Errorf(
				"Runtime event settlement cannot carry deadline authority",
			)
		}
	case workerapp.TurnOutcomeSourceBackendDeadline:
		if !checkpoint.Source.TimedOut ||
			checkpoint.Source.Reason != "agent_turn_deadline" {
			return turnTerminalResult{}, fmt.Errorf(
				"backend deadline settlement source is inconsistent",
			)
		}
	default:
		return turnTerminalResult{}, fmt.Errorf(
			"pending Runtime Turn settlement source %q is unsupported",
			checkpoint.Source.Kind,
		)
	}
	return turnTerminalResult{
		reason:     checkpoint.Source.Reason,
		timedOut:   checkpoint.Source.TimedOut,
		forcedStop: checkpoint.Source.ForcedStop,
		sourceKind: sourceKind,
		occurredAt: checkpoint.Source.OccurredAt,
	}, nil
}

func (runner *runtimeRunner) claimNextPendingTurnSettlement(
	ctx context.Context,
) (workerapp.TurnSettlement, *liveRuntime, func(), bool, error) {
	var settlementID, runtimeInstanceID string
	err := runner.pool.QueryRow(ctx, `
SELECT settlement_id,runtime_instance_id
FROM worker_turn_settlements
WHERE tenant_id=$1
  AND (
    source_kind='runtime_event'
    OR (
      source_kind='backend_deadline'
      AND source_state->>'phase'='terminal_observed'
    )
  )
  AND phase <> 'completed'
  AND (
      lease_owner_id IS NULL
      OR lease_expires_at <= $2
      OR lease_owner_id=$3
  )
ORDER BY accepted_at,settlement_id
LIMIT 1`,
		runner.configuration.Profile.TenantID,
		runner.now().UTC(),
		runner.settlerID,
	).Scan(&settlementID, &runtimeInstanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerapp.TurnSettlement{}, nil, nil, false, nil
	}
	if err != nil {
		return workerapp.TurnSettlement{}, nil, nil, false, err
	}
	runner.mu.Lock()
	live := runner.runtimes[runtimeInstanceID]
	runner.mu.Unlock()
	if live == nil {
		current, err := runner.settlements.Load(ctx, settlementID)
		if err != nil {
			return workerapp.TurnSettlement{},
				nil,
				nil,
				true,
				err
		}
		if current.Phase == workerapp.TurnSettlementCompleted ||
			(current.LeaseOwnerID != "" &&
				current.LeaseOwnerID != runner.settlerID &&
				current.LeaseExpiresAt.After(runner.now().UTC())) {
			return workerapp.TurnSettlement{},
				nil,
				nil,
				true,
				errRuntimeSettlementOwnerBusy
		}
		return current, nil, func() {}, true, nil
	}
	if !live.mu.TryLock() {
		return workerapp.TurnSettlement{}, nil, nil, true,
			errRuntimeSettlementOwnerBusy
	}
	unlock := live.mu.Unlock
	current, err := runner.settlements.Load(ctx, settlementID)
	if err != nil {
		unlock()
		return workerapp.TurnSettlement{}, nil, nil, true, err
	}
	if current.Phase == workerapp.TurnSettlementCompleted ||
		(current.LeaseOwnerID != "" &&
			current.LeaseOwnerID != runner.settlerID &&
			current.LeaseExpiresAt.After(runner.now().UTC())) {
		unlock()
		return workerapp.TurnSettlement{},
			nil,
			nil,
			true,
			errRuntimeSettlementOwnerBusy
	}
	return current, live, unlock, true, nil
}

func (runner *runtimeRunner) reconcileOnePendingTurnSettlement(
	ctx context.Context,
) (string, bool, error) {
	current, live, unlock, found, err :=
		runner.claimNextPendingTurnSettlement(ctx)
	if err != nil || !found {
		return "", found, err
	}
	defer unlock()
	if live == nil {
		runtimeInstanceID, _, _, authorityErr :=
			runtimeSettlementRuntimeAuthority(current)
		if authorityErr != nil {
			return current.AgentTurnID, true, authorityErr
		}
		reconcileDurable := runner.reconcileDurableTerminalSettlementByID
		if runner.durableTerminalReconciler != nil {
			reconcileDurable = runner.durableTerminalReconciler
		}
		_, settleErr := reconcileDurable(ctx, current.SettlementID)
		if settleErr != nil {
			if errors.Is(
				settleErr,
				workerapp.ErrTurnSettlementLeaseHeld,
			) ||
				errors.Is(
					settleErr,
					workerapp.ErrTurnSettlementLeaseStale,
				) ||
				errors.Is(
					settleErr,
					workerapp.ErrTurnSettlementRevisionConflict,
				) {
				return current.AgentTurnID,
					true,
					errRuntimeSettlementOwnerBusy
			}
			return current.AgentTurnID, true, settleErr
		}
		restoreUnowned := runner.restoreUnownedRuntimeByID
		if runner.unownedRuntimeRestorer != nil {
			restoreUnowned = runner.unownedRuntimeRestorer
		}
		restored, restoreErr := restoreUnowned(ctx, runtimeInstanceID)
		if restoreErr != nil {
			return current.AgentTurnID, true, restoreErr
		}
		if restored != nil {
			runner.superviseRestoredTurn(ctx, *restored)
		}
		return current.AgentTurnID, true, nil
	}
	_, err = runner.reconcilePendingTurnSettlement(
		ctx,
		current,
		live,
	)
	if errors.Is(err, errRuntimeRecoverySettled) {
		return current.AgentTurnID, true, nil
	}
	return current.AgentTurnID, true, err
}

func (runner *runtimeRunner) reconcileStartupRuntimeTurnSettlements(
	ctx context.Context,
	runtimeInstanceID string,
	live *liveRuntime,
) (map[string]struct{}, error) {
	settledTurns := make(map[string]struct{})
	for {
		var settlementID string
		err := runner.pool.QueryRow(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE tenant_id=$1
  AND runtime_instance_id=$2
  AND (
    source_kind='runtime_event'
    OR (
      source_kind='backend_deadline'
      AND source_state->>'phase'='terminal_observed'
    )
  )
  AND phase <> 'completed'
ORDER BY accepted_at,settlement_id
LIMIT 1`,
			runner.configuration.Profile.TenantID,
			runtimeInstanceID,
		).Scan(&settlementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return settledTurns, nil
		}
		if err != nil {
			return nil, err
		}
		current, err := runner.settlements.Load(ctx, settlementID)
		if err != nil {
			return nil, err
		}
		now := runner.now().UTC()
		if current.LeaseOwnerID != "" &&
			current.LeaseOwnerID != runner.settlerID &&
			current.LeaseExpiresAt.After(now) {
			wait := current.LeaseExpiresAt.Sub(now)
			if wait > runtimePollInterval {
				wait = runtimePollInterval
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		live.mu.Lock()
		_, err = runner.reconcilePendingTurnSettlement(
			ctx,
			current,
			live,
		)
		live.mu.Unlock()
		if err != nil {
			return nil, err
		}
		settledTurns[current.AgentTurnID] = struct{}{}
	}
}
