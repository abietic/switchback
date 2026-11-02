package localproduct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// runBackendDeadlineSourceWorker is independent from the main delivery loop:
// waitTurnTerminal may block for the entire AgentTurn, while the Backend
// deadline source must still journal an interrupt and, after the frozen grace,
// fence and clean the Runtime.
func (runner *runtimeRunner) runBackendDeadlineSourceWorker(
	ctx context.Context,
) error {
	ticker := time.NewTicker(runtimePollInterval)
	defer ticker.Stop()

	for {
		progressed, err := runner.reconcileBackendDeadlineSource(ctx, "")
		if err == nil && !progressed {
			_, err = runner.reconcileBackendDeadlineTurnSettlement(ctx)
		}
		if err != nil &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, workerapp.ErrTurnSettlementLeaseHeld) &&
			!errors.Is(err, workerapp.ErrTurnSettlementRevisionConflict) &&
			!errors.Is(err, workerapp.ErrTurnSettlementLeaseStale) {
			slog.Error(
				"reconcile Backend deadline source",
				"error",
				err,
			)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// reconcileBackendDeadlineSource advances at most one durable source phase.
// An empty settlementID scans the next runnable source; a non-empty ID is used
// by the exact reconciliation ScheduledCommand written with force_stop_intent.
func (runner *runtimeRunner) reconcileBackendDeadlineSource(
	ctx context.Context,
	settlementID string,
) (bool, error) {
	current, found, err := runner.claimBackendDeadlineSource(
		ctx,
		settlementID,
	)
	if err != nil || !found {
		return found, err
	}
	sourceStore, ok := runner.settlements.Store.(workerapp.TurnSettlementSourceStore)
	if !ok {
		return true, fmt.Errorf(
			"Turn settlement store does not support Backend deadline sources",
		)
	}
	effects := runner.deadlineSourceEffects
	if effects == nil {
		effects = localBackendDeadlineSourceEffects{runner: runner}
	}
	result, err := (workerapp.BackendDeadlineSourceWorker{
		Source: workerapp.TurnSettlementSourceService{
			Store: sourceStore,
			Now:   runner.now,
		},
		Effects: effects,
		Now:     runner.now,
	}).Reconcile(ctx, current)
	if err != nil {
		return true, err
	}
	if err := runner.applyBackendDeadlinePostCommit(result); err != nil {
		return true, err
	}
	return true, nil
}

type localBackendDeadlineReconcilePayload struct {
	SchemaVersion             uint32 `json:"schema_version"`
	SettlementID              string `json:"settlement_id"`
	ExpectedSourceStateDigest string `json:"expected_source_state_digest"`
}

func (runner *runtimeRunner) handleBackendDeadlineReconcileCommand(
	ctx context.Context,
	command platformpostgres.ScheduledCommand,
) error {
	if command.ID == "" ||
		command.CommandType !=
			workerpostgres.CommandBackendDeadlineSourceReconcile ||
		command.TargetType != "TurnSettlement" ||
		command.TargetID == "" ||
		command.TenantID == "" ||
		command.ExpectedRevision == 0 ||
		command.DueAt.IsZero() ||
		command.FencingToken == 0 {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	payload, err := decodeLocalBackendDeadlineReconcilePayload(
		command.Payload,
	)
	if err != nil ||
		payload.SchemaVersion != 1 ||
		payload.SettlementID != command.TargetID {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	current, err := runner.settlements.Load(ctx, command.TargetID)
	if err != nil {
		return err
	}
	if current.TenantID != command.TenantID ||
		current.Source.Kind !=
			workerapp.TurnSettlementSourceBackendDeadline ||
		workerapp.ValidateTurnSettlementSource(current.Source) != nil ||
		current.Revision < command.ExpectedRevision {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(current.Source.State, &state); err != nil ||
		state.ForceStop == nil ||
		state.ForceStop.CleanupScheduledCommandID != command.ID ||
		!state.ForceStop.DecidedAt.Equal(command.DueAt) {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	forceState := state
	forceState.Phase = workerapp.BackendDeadlineSourceForceStopIntent
	forceState.WriteFence = nil
	forceState.Cleanup = nil
	forceStateBytes, err := json.Marshal(forceState)
	if err != nil {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	_, forceStateDigest, err :=
		workerapp.CanonicalizeTurnSettlementSourceState(forceStateBytes)
	if err != nil ||
		forceStateDigest != payload.ExpectedSourceStateDigest {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	if state.Phase ==
		workerapp.BackendDeadlineSourceCleanupConfirmed {
		return nil
	}
	found, err := runner.reconcileBackendDeadlineSource(
		ctx,
		command.TargetID,
	)
	if err != nil {
		return err
	}
	if !found {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	return nil
}

func decodeLocalBackendDeadlineReconcilePayload(
	raw json.RawMessage,
) (localBackendDeadlineReconcilePayload, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload localBackendDeadlineReconcilePayload
	if err := decoder.Decode(&payload); err != nil {
		return payload, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return payload, workerapp.ErrTurnSettlementSourceConflict
		}
		return payload, err
	}
	return payload, nil
}

func (runner *runtimeRunner) claimBackendDeadlineSource(
	ctx context.Context,
	settlementID string,
) (workerapp.TurnSettlement, bool, error) {
	if runner == nil || runner.pool == nil || runner.settlements.Store == nil ||
		runner.now == nil || runner.settlerID == "" {
		return workerapp.TurnSettlement{}, false,
			fmt.Errorf("Backend deadline source runner dependencies are required")
	}
	now := runner.now().UTC()
	var candidateID string
	var err error
	if settlementID == "" {
		err = runner.pool.QueryRow(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE tenant_id=$1
  AND source_kind='backend_deadline'
  AND phase <> 'completed'
  AND (
    source_state->>'phase' IN ('claimed','force_stop_intent','write_fenced')
    OR (
      source_state->>'phase'='interrupt_enqueued'
      AND (source_state->>'force_after')::timestamptz <= $2
    )
  )
  AND (
    lease_owner_id IS NULL
    OR lease_expires_at <= $2
    OR lease_owner_id=$3
  )
ORDER BY source_occurred_at,settlement_id
LIMIT 1`,
			runner.configuration.Profile.TenantID,
			now,
			runner.settlerID,
		).Scan(&candidateID)
	} else {
		err = runner.pool.QueryRow(ctx, `
SELECT settlement_id
FROM worker_turn_settlements
WHERE settlement_id=$1
  AND tenant_id=$2
  AND source_kind='backend_deadline'
  AND phase <> 'completed'
  AND (
    source_state->>'phase' IN ('claimed','force_stop_intent','write_fenced')
    OR (
      source_state->>'phase'='interrupt_enqueued'
      AND (source_state->>'force_after')::timestamptz <= $3
    )
  )
  AND (
    lease_owner_id IS NULL
    OR lease_expires_at <= $3
    OR lease_owner_id=$4
  )`,
			settlementID,
			runner.configuration.Profile.TenantID,
			now,
			runner.settlerID,
		).Scan(&candidateID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return workerapp.TurnSettlement{}, false, nil
	}
	if err != nil {
		return workerapp.TurnSettlement{}, false,
			fmt.Errorf("select Backend deadline source: %w", err)
	}
	current, err := runner.settlements.Load(ctx, candidateID)
	if err != nil {
		return workerapp.TurnSettlement{}, true, err
	}
	state, err := backendDeadlineRunnableState(current, now)
	if err != nil {
		return workerapp.TurnSettlement{}, true, err
	}
	if state.Phase == workerapp.BackendDeadlineSourceInterruptQueued &&
		now.Before(state.ForceAfter) {
		return workerapp.TurnSettlement{}, false, nil
	}
	if current.LeaseOwnerID == runner.settlerID &&
		current.LeaseFence > 0 &&
		current.LeaseExpiresAt.After(now) {
		return current, true, nil
	}
	if current.LeaseOwnerID != "" &&
		current.LeaseOwnerID != runner.settlerID &&
		current.LeaseExpiresAt.After(now) {
		return workerapp.TurnSettlement{}, true,
			workerapp.ErrTurnSettlementLeaseHeld
	}
	current, err = runner.settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			OwnerID:          runner.settlerID,
		},
	)
	return current, true, err
}

func backendDeadlineRunnableState(
	settlement workerapp.TurnSettlement,
	now time.Time,
) (workerapp.BackendDeadlineSourceState, error) {
	if settlement.Source.Kind !=
		workerapp.TurnSettlementSourceBackendDeadline ||
		workerapp.ValidateTurnSettlementSource(settlement.Source) != nil {
		return workerapp.BackendDeadlineSourceState{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(settlement.Source.State, &state); err != nil {
		return workerapp.BackendDeadlineSourceState{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	switch state.Phase {
	case workerapp.BackendDeadlineSourceClaimed,
		workerapp.BackendDeadlineSourceForceStopIntent,
		workerapp.BackendDeadlineSourceWriteFenced:
		return state, nil
	case workerapp.BackendDeadlineSourceInterruptQueued:
		if !now.Before(state.ForceAfter) {
			return state, nil
		}
	}
	return workerapp.BackendDeadlineSourceState{},
		workerapp.ErrTurnSettlementSourceConflict
}

func (runner *runtimeRunner) applyBackendDeadlinePostCommit(
	result workerapp.BackendDeadlineSourceWorkerResult,
) error {
	if result.Outcome !=
		workerapp.BackendDeadlineSourceWorkerInterruptQueued &&
		result.Outcome !=
			workerapp.BackendDeadlineSourceWorkerForceStopIntent {
		return nil
	}
	var state workerapp.BackendDeadlineSourceState
	if err := json.Unmarshal(
		result.Settlement.Source.State,
		&state,
	); err != nil {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	if state.RuntimeGeneration == 0 ||
		state.RuntimeGeneration > uint64(^uint32(0)) {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	identity := controlgrpc.RuntimeIdentity{
		WorkerSessionID:   result.Settlement.WorkerSessionID,
		RuntimeInstanceID: state.RuntimeInstanceID,
		RuntimeGeneration: uint32(state.RuntimeGeneration),
		ChannelEpoch:      state.ChannelEpoch,
	}
	switch result.Outcome {
	case workerapp.BackendDeadlineSourceWorkerInterruptQueued:
		runner.hub.WakePendingCommands(identity)
	case workerapp.BackendDeadlineSourceWorkerForceStopIntent:
		runner.hub.RevokeDurablyFenced(identity)
	}
	return nil
}

type localBackendDeadlineSourceEffects struct {
	runner       *runtimeRunner
	cleaner      workerapp.RuntimeCleaner
	cleanupLocal func(
		workerapp.BackendDeadlineSourceAuthority,
	) (workerapp.SessionStorage, error)
}

func (effects localBackendDeadlineSourceEffects) LoadForceStopTarget(
	ctx context.Context,
	authority workerapp.BackendDeadlineSourceAuthority,
) (workerapp.BackendDeadlineForceStopTarget, error) {
	if effects.runner == nil || effects.runner.pool == nil ||
		effects.runner.now == nil {
		return workerapp.BackendDeadlineForceStopTarget{},
			fmt.Errorf("Backend deadline force-stop reader is unavailable")
	}
	var target workerapp.BackendDeadlineForceStopTarget
	err := effects.runner.pool.QueryRow(ctx, `
SELECT session.session_revision,runtime.revision,
       COALESCE(runtime.container_id,'')
FROM worker_turn_settlements settlement
JOIN worker_sessions session
  ON session.worker_session_id=settlement.worker_session_id
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=settlement.worker_run_id
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_session_id=settlement.agent_session_id
 AND turn.agent_turn_id=settlement.agent_turn_id
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$7
WHERE settlement.settlement_id=$1
  AND settlement.tenant_id=$2
  AND settlement.worker_session_id=$3
  AND settlement.worker_run_id=$4
  AND settlement.agent_session_id=$5
  AND settlement.agent_turn_id=$6
  AND settlement.source_kind='backend_deadline'
  AND settlement.revision=$8
  AND settlement.source_state_digest=$9
  AND settlement.lease_owner_id=$10
  AND settlement.lease_fence=$11
  AND settlement.lease_expires_at > $12
  AND runtime.generation=$13
  AND runtime.control_channel_epoch=$14`,
		authority.SettlementID,
		authority.TenantID,
		authority.WorkerSessionID,
		authority.WorkerRunID,
		authority.AgentSessionID,
		authority.AgentTurnID,
		authority.RuntimeInstanceID,
		authority.ExpectedRevision,
		authority.ExpectedStateDigest,
		authority.LeaseOwnerID,
		authority.LeaseFence,
		effects.runner.now().UTC(),
		authority.RuntimeGeneration,
		authority.ChannelEpoch,
	).Scan(
		&target.ExpectedSessionRevision,
		&target.ExpectedRuntimeRevision,
		&target.ExpectedContainerID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerapp.BackendDeadlineForceStopTarget{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return workerapp.BackendDeadlineForceStopTarget{},
			fmt.Errorf("load Backend deadline force-stop target: %w", err)
	}
	return target, nil
}

func (effects localBackendDeadlineSourceEffects) ObserveWriteFence(
	ctx context.Context,
	request workerapp.BackendDeadlineWriteFenceObservationRequest,
) (workerapp.BackendDeadlineWriteFenceProof, error) {
	cleaner := effects.runtimeCleaner()
	if effects.runner == nil || cleaner == nil {
		return workerapp.BackendDeadlineWriteFenceProof{},
			fmt.Errorf("Backend deadline cleanup dependencies are unavailable")
	}
	var proof workerapp.BackendDeadlineWriteFenceProof
	_, err := effects.runner.backendDeadlineSettlementCoordinator().
		runWithLease(
			ctx,
			backendDeadlineAuthoritySettlement(request.Authority),
			func(
				effectCtx context.Context,
				_ workerapp.TurnSettlement,
			) error {
				_, localErr := effects.cleanupLocalRuntime(
					effectCtx,
					request.Authority,
				)
				observation, cleanupErr :=
					cleaner.Cleanup(
						effectCtx,
						workerapp.RuntimeCleanupRequest{
							RuntimeInstanceID: request.Authority.
								RuntimeInstanceID,
							ExpectedContainerID: request.ForceStop.
								ExpectedContainerID,
							Generation: request.Authority.
								RuntimeGeneration,
						},
					)
				if observation.ObservedAt.IsZero() ||
					observation.ContainerExists {
					return errors.Join(
						localErr,
						cleanupErr,
						fmt.Errorf(
							"Backend deadline container absence is not proven",
						),
					)
				}
				if localErr != nil || cleanupErr != nil {
					slog.Warn(
						"Backend deadline write fence has cleanup residue",
						"runtime_instance_id",
						request.Authority.RuntimeInstanceID,
						"error",
						errors.Join(localErr, cleanupErr),
					)
				}
				if err := effects.runner.
					forgetBackendDeadlineRuntime(
						request.Authority,
						request.ForceStop.ExpectedContainerID,
					); err != nil {
					return err
				}
				channelOwnerFence, channelRevision, channelFencedAt,
					err := effects.readFencedChannel(
					effectCtx,
					request.Authority,
				)
				if err != nil {
					return err
				}
				proof, err =
					workerapp.NewBackendDeadlineWriteFenceProof(
						channelOwnerFence,
						channelRevision,
						channelFencedAt,
						request.ForceStop.ExpectedContainerID,
						observation.ObservedAt,
					)
				return err
			},
		)
	return proof, err
}

func (effects localBackendDeadlineSourceEffects) ObserveCleanup(
	ctx context.Context,
	request workerapp.BackendDeadlineCleanupObservationRequest,
) (workerapp.BackendDeadlineCleanupProof, error) {
	cleaner := effects.runtimeCleaner()
	if effects.runner == nil || cleaner == nil ||
		effects.runner.now == nil {
		return workerapp.BackendDeadlineCleanupProof{},
			fmt.Errorf("Backend deadline cleanup dependencies are unavailable")
	}
	var proof workerapp.BackendDeadlineCleanupProof
	_, err := effects.runner.backendDeadlineSettlementCoordinator().
		runWithLease(
			ctx,
			backendDeadlineAuthoritySettlement(request.Authority),
			func(
				effectCtx context.Context,
				_ workerapp.TurnSettlement,
			) error {
				_, localErr := effects.cleanupLocalRuntime(
					effectCtx,
					request.Authority,
				)
				observation, cleanupErr :=
					cleaner.Cleanup(
						effectCtx,
						workerapp.RuntimeCleanupRequest{
							RuntimeInstanceID: request.Authority.
								RuntimeInstanceID,
							ExpectedContainerID: request.ForceStop.
								ExpectedContainerID,
							Generation: request.Authority.
								RuntimeGeneration,
						},
					)
				if err := errors.Join(localErr, cleanupErr); err != nil {
					return err
				}
				if !observation.Confirmed ||
					observation.ContainerExists ||
					observation.SecretVolumeExists ||
					observation.ObservedAt.IsZero() {
					return fmt.Errorf(
						"Backend deadline cleanup absence is not confirmed",
					)
				}
				if err := effects.runner.
					forgetBackendDeadlineRuntime(
						request.Authority,
						request.ForceStop.ExpectedContainerID,
					); err != nil {
					return err
				}
				confirmedAt := effects.runner.now().UTC()
				if confirmedAt.Before(observation.ObservedAt) {
					return workerapp.ErrTurnSettlementSourceConflict
				}
				proof, cleanupErr =
					workerapp.NewBackendDeadlineCleanupProof(
						request.ForceStop.ExpectedContainerID,
						observation.SecretVolumeRef,
						observation.ObservedAt,
						confirmedAt,
					)
				return cleanupErr
			},
		)
	return proof, err
}

func (effects localBackendDeadlineSourceEffects) runtimeCleaner() workerapp.RuntimeCleaner {
	if effects.cleaner != nil {
		return effects.cleaner
	}
	if effects.runner == nil {
		return nil
	}
	return effects.runner.provisioner
}

func (effects localBackendDeadlineSourceEffects) cleanupLocalRuntime(
	ctx context.Context,
	authority workerapp.BackendDeadlineSourceAuthority,
) (workerapp.SessionStorage, error) {
	if effects.cleanupLocal != nil {
		return effects.cleanupLocal(authority)
	}
	if effects.runner == nil || effects.runner.environment == nil {
		return workerapp.SessionStorage{},
			fmt.Errorf("Backend deadline Runtime environment is unavailable")
	}
	if authority.RuntimeGeneration == 0 || authority.RuntimeGeneration > uint64(^uint32(0)) {
		return workerapp.SessionStorage{}, errObservationCleanupFenceUnproven
	}
	state, err := effects.runner.readObservationCleanupFence(ctx, controlgrpc.RuntimeIdentity{
		WorkerSessionID: authority.WorkerSessionID, RuntimeInstanceID: authority.RuntimeInstanceID,
		RuntimeGeneration: uint32(authority.RuntimeGeneration), ChannelEpoch: authority.ChannelEpoch,
	})
	if err != nil {
		return workerapp.SessionStorage{}, err
	}
	return effects.runner.environment.cleanupDurablyFencedRuntime(authority, state)
}

func (effects localBackendDeadlineSourceEffects) readFencedChannel(
	ctx context.Context,
	authority workerapp.BackendDeadlineSourceAuthority,
) (uint64, uint64, time.Time, error) {
	var status string
	var ownerFence, fenceRevision uint64
	var fencedAt time.Time
	err := effects.runner.pool.QueryRow(ctx, `
SELECT status,fence_owner_fence,fence_revision,fenced_at
FROM runtime_control_channels
WHERE runtime_instance_id=$1
  AND worker_session_id=$2
  AND runtime_generation=$3
  AND channel_epoch=$4`,
		authority.RuntimeInstanceID,
		authority.WorkerSessionID,
		authority.RuntimeGeneration,
		authority.ChannelEpoch,
	).Scan(&status, &ownerFence, &fenceRevision, &fencedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, time.Time{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return 0, 0, time.Time{},
			fmt.Errorf("read Backend deadline fenced channel: %w", err)
	}
	if status != "fenced" ||
		ownerFence == 0 ||
		fenceRevision == 0 ||
		fencedAt.IsZero() {
		return 0, 0, time.Time{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	return ownerFence, fenceRevision, fencedAt.UTC(), nil
}

func (runner *runtimeRunner) backendDeadlineSettlementCoordinator() runtimeTurnSettlementCoordinator {
	return runtimeTurnSettlementCoordinator{
		controller: runtimeSettlementPersistence{
			service: runner.settlements,
		},
	}
}

func backendDeadlineAuthoritySettlement(
	authority workerapp.BackendDeadlineSourceAuthority,
) workerapp.TurnSettlement {
	return workerapp.TurnSettlement{
		SettlementID:   authority.SettlementID,
		Revision:       authority.ExpectedRevision,
		LeaseOwnerID:   authority.LeaseOwnerID,
		LeaseFence:     authority.LeaseFence,
		LeaseExpiresAt: authority.ForceAfter,
	}
}

func (runner *runtimeRunner) forgetBackendDeadlineRuntime(
	authority workerapp.BackendDeadlineSourceAuthority,
	expectedContainerID string,
) error {
	if authority.RuntimeGeneration == 0 ||
		authority.RuntimeGeneration > uint64(^uint32(0)) {
		return workerapp.ErrTurnSettlementSourceConflict
	}
	expected := controlgrpc.RuntimeIdentity{
		WorkerSessionID:   authority.WorkerSessionID,
		RuntimeInstanceID: authority.RuntimeInstanceID,
		RuntimeGeneration: uint32(authority.RuntimeGeneration),
		ChannelEpoch:      authority.ChannelEpoch,
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	live := runner.runtimes[authority.RuntimeInstanceID]
	if live == nil {
		return nil
	}
	if live.identity != expected ||
		live.containerID != expectedContainerID {
		return fmt.Errorf(
			"Backend deadline live Runtime identity conflicts",
		)
	}
	delete(runner.runtimes, authority.RuntimeInstanceID)
	return nil
}
