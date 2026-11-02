package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const runtimeRecoveryStabilityWindow = 30 * time.Second

type localRuntimeRecoveryEffects struct {
	runner *runtimeRunner
}

type localRuntimeControlObserver struct {
	heartbeat workerapp.RuntimeHeartbeatObserver
}

func (observer localRuntimeControlObserver) ObserveWorkerControlEvent(
	ctx context.Context,
	event workerapp.WorkerControlEvent,
	receivedAt time.Time,
) error {
	return observer.heartbeat.ObserveWorkerControlEvent(ctx, event, receivedAt)
}

func (effects localRuntimeRecoveryEffects) FenceFailedRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
) (workerapp.RuntimeRecoveryFenceEvidence, error) {
	if effects.runner == nil {
		return workerapp.RuntimeRecoveryFenceEvidence{}, fmt.Errorf("Runtime recovery runner is unavailable")
	}
	if _, err := effects.runner.environment.fenceFailedRuntime(state); err != nil {
		return workerapp.RuntimeRecoveryFenceEvidence{}, err
	}
	observation, err := effects.runner.provisioner.Cleanup(ctx, workerapp.RuntimeCleanupRequest{
		RuntimeInstanceID: state.FailedRuntimeInstanceID,
		Generation:        state.FailedGeneration,
	})
	if err != nil || !observation.Confirmed {
		return workerapp.RuntimeRecoveryFenceEvidence{}, errors.Join(
			err,
			fmt.Errorf("failed Runtime container or secret volume cleanup is unproven"),
		)
	}
	effects.runner.mu.Lock()
	delete(effects.runner.runtimes, state.FailedRuntimeInstanceID)
	effects.runner.mu.Unlock()
	return workerapp.RuntimeRecoveryFenceEvidence{
		ExclusiveWriteAuthority: true,
		MountFenceToken:         state.FailedChannelEpoch + 1,
	}, nil
}

func (effects localRuntimeRecoveryEffects) FenceReplacementRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
) error {
	if effects.runner == nil {
		return nil
	}
	target := state
	runtimeRevision := state.FailedRuntimeRevision
	containerID := ""
	if state.ReplacementRuntimeInstanceID != "" {
		target.FailedRuntimeInstanceID = state.ReplacementRuntimeInstanceID
		target.FailedGeneration = state.ReplacementGeneration
		target.FailedChannelEpoch = state.ReplacementChannelEpoch
		runtimeRevision = state.ReplacementRuntimeRevision
		containerID = state.ReplacementContainerID
	}
	// Cleanup persists its retry lease before attempting any fence. Exhaustion
	// has already committed, so a failed external effect must retain an owner.
	result, cleanupErr := effects.runner.cleanup.Cleanup(ctx, workerapp.BeginRuntimeCleanupCommand{
		TenantID: state.TenantID, WorkerSessionID: state.WorkerSessionID,
		RuntimeInstanceID:   target.FailedRuntimeInstanceID,
		ExpectedContainerID: containerID, RuntimeGeneration: target.FailedGeneration,
		ExpectedSessionRevision: state.SessionRevision,
		ExpectedRuntimeRevision: runtimeRevision,
		IdempotencyKey:          "recovery-cleanup/" + state.IncidentID,
	})
	if cleanupErr != nil {
		return cleanupErr
	}
	effects.runner.mu.Lock()
	delete(effects.runner.runtimes, target.FailedRuntimeInstanceID)
	effects.runner.mu.Unlock()
	if !result.Confirmed && result.Reconciliation.ScheduledCommandID == "" {
		return fmt.Errorf("Runtime recovery cleanup is neither confirmed nor scheduled")
	}
	return nil
}

func (effects localRuntimeRecoveryEffects) FenceLateProvisionedRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
	provisioned workerapp.ProvisionedRuntime,
) error {
	if effects.runner == nil {
		return fmt.Errorf("Runtime recovery runner is unavailable")
	}
	target := state
	target.FailedRuntimeInstanceID = provisioned.RuntimeInstanceID
	target.FailedGeneration = provisioned.Generation
	target.FailedChannelEpoch = state.ReplacementChannelEpoch
	_, fenceErr := effects.runner.environment.fenceFailedRuntime(target)
	observation, cleanupErr := effects.runner.provisioner.Cleanup(
		ctx,
		workerapp.RuntimeCleanupRequest{
			RuntimeInstanceID:   provisioned.RuntimeInstanceID,
			ExpectedContainerID: provisioned.ContainerID,
			Generation:          provisioned.Generation,
		},
	)
	effects.runner.mu.Lock()
	delete(effects.runner.runtimes, provisioned.RuntimeInstanceID)
	effects.runner.mu.Unlock()
	if cleanupErr != nil || !observation.Confirmed {
		cleanupErr = errors.Join(
			cleanupErr,
			fmt.Errorf("late provisioned Runtime cleanup is unproven"),
		)
	}
	return errors.Join(fenceErr, cleanupErr)
}

func (effects localRuntimeRecoveryEffects) PrepareReplacementRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
) (workerapp.RuntimeProvisionRequest, error) {
	return effects.runner.environment.PrepareReplacementRuntime(ctx, state)
}

func (effects localRuntimeRecoveryEffects) ReattachReplacementRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
) (workerapp.RuntimeRecoveryAttachmentEvidence, error) {
	events, err := effects.runner.hub.Events(
		ctx,
		state.ReplacementRuntimeInstanceID,
		state.ReplacementGeneration,
	)
	if err != nil {
		return workerapp.RuntimeRecoveryAttachmentEvidence{}, err
	}
	readyEvent, err := waitWorkerEvent(ctx, events, runtimeReadyTimeout, func(event workerapp.WorkerControlEvent) bool {
		return event.Envelope.MessageType == "runtime.ready"
	})
	if err != nil {
		return workerapp.RuntimeRecoveryAttachmentEvidence{}, err
	}
	var ready runtimeReadyPayload
	if err := json.Unmarshal(readyEvent.Payload, &ready); err != nil {
		return workerapp.RuntimeRecoveryAttachmentEvidence{}, fmt.Errorf("replacement runtime.ready is invalid")
	}
	if ready.AgentSessionID != state.AgentSessionID ||
		ready.AgentProfileID != state.AgentProfileID ||
		fmt.Sprint(ready.AgentProfileRevision) != state.AgentProfileRevision ||
		ready.AgentProfileDigest != state.AgentProfileDigest ||
		ready.Capabilities.ProtocolVersion != "1" ||
		(!ready.Capabilities.ResumeSession && !ready.Capabilities.LoadSession) {
		return workerapp.RuntimeRecoveryAttachmentEvidence{}, fmt.Errorf(
			"replacement Runtime ACP identity or load capability is stale",
		)
	}
	prepared, ok := effects.runner.environment.get(state.ReplacementRuntimeInstanceID)
	if !ok {
		return workerapp.RuntimeRecoveryAttachmentEvidence{}, fmt.Errorf(
			"replacement Runtime environment is not owned",
		)
	}
	tracePath := filepath.Join(prepared.Storage.TraceSpool.BackendPath, "acp.raw.jsonl")
	offset, _ := fileSize(tracePath)
	live := &liveRuntime{
		identity: prepared.Credential.Identity, agentSessionID: ready.AgentSessionID,
		storage: prepared.Storage, sourceDigest: prepared.SourceDigest, events: events,
		traceOffset: offset, containerID: state.ReplacementContainerID,
	}
	attachment := workerapp.RuntimeRecoveryAttachmentEvidence{ContinuityProven: true}
	if state.AgentTurnID == "" {
		// No active Turn continuity needs to be classified.
	} else {
		var turnStatus string
		if err := effects.runner.pool.QueryRow(ctx, `
SELECT status
FROM agent_turns
WHERE agent_turn_id=$1 AND worker_session_id=$2 AND worker_run_id=$3`,
			state.AgentTurnID, state.WorkerSessionID, state.WorkerRunID,
		).Scan(&turnStatus); err != nil {
			return workerapp.RuntimeRecoveryAttachmentEvidence{}, err
		}
		switch turnStatus {
		case "accepted", "delivered", "consumed":
			// The pinned ACP stack can reattach Session state, but it does not prove
			// the lost prompt/Tool effect. Never resend the prompt automatically.
			attachment.ContinuityProven = false
		}
	}
	if err := processWorkerControlEvent(
		ctx,
		readyEvent,
		"runtime_ready_committed",
		func() error { return nil },
	); err != nil {
		return workerapp.RuntimeRecoveryAttachmentEvidence{}, fmt.Errorf(
			"apply replacement runtime.ready after reattach: %w",
			err,
		)
	}
	effects.runner.mu.Lock()
	effects.runner.runtimes[state.ReplacementRuntimeInstanceID] = live
	effects.runner.mu.Unlock()
	return attachment, nil
}

func (effects localRuntimeRecoveryEffects) VerifyReplacementRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
) (workerapp.RuntimeRecoveryHealthEvidence, error) {
	now := effects.runner.now().UTC()
	var watchdogStatus, watchdogTerminalReason string
	err := effects.runner.pool.QueryRow(ctx, `
SELECT status,COALESCE(terminal_reason,'')
FROM runtime_first_heartbeat_watchdogs
WHERE runtime_instance_id=$1
  AND worker_session_id=$2
  AND runtime_generation=$3
  AND channel_epoch=$4`,
		state.ReplacementRuntimeInstanceID,
		state.WorkerSessionID,
		state.ReplacementGeneration,
		state.ReplacementChannelEpoch,
	).Scan(&watchdogStatus, &watchdogTerminalReason)
	if err == nil && watchdogStatus == "unhealthy" {
		if watchdogTerminalReason ==
			string(workerapp.RuntimeFirstHeartbeatReplacementTimeout) {
			return workerapp.RuntimeRecoveryHealthEvidence{},
				workerapp.ErrReplacementFirstHeartbeatTimeout
		}
		return workerapp.RuntimeRecoveryHealthEvidence{},
			fmt.Errorf(
				"replacement Runtime first-heartbeat watchdog is unhealthy without its timeout proof",
			)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return workerapp.RuntimeRecoveryHealthEvidence{}, err
	}
	var runtimeStatus, heartbeatStatus string
	var startedAt, heartbeatAt time.Time
	err = effects.runner.pool.QueryRow(ctx, `
SELECT runtime.status,runtime.started_at,heartbeat.liveness_status,heartbeat.last_received_at
FROM runtime_instances runtime
JOIN runtime_heartbeat_observations heartbeat
  ON heartbeat.runtime_instance_id=runtime.runtime_instance_id
WHERE runtime.runtime_instance_id=$1 AND runtime.worker_session_id=$2
  AND runtime.generation=$3 AND runtime.control_channel_epoch=$4`,
		state.ReplacementRuntimeInstanceID, state.WorkerSessionID,
		state.ReplacementGeneration, state.ReplacementChannelEpoch,
	).Scan(&runtimeStatus, &startedAt, &heartbeatStatus, &heartbeatAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerapp.RuntimeRecoveryHealthEvidence{}, nil
	}
	if err != nil {
		return workerapp.RuntimeRecoveryHealthEvidence{}, err
	}
	if runtimeStatus != "active" || heartbeatStatus != "healthy" ||
		now.Sub(heartbeatAt) >= workerapp.RuntimeSuspectAfter {
		return workerapp.RuntimeRecoveryHealthEvidence{}, fmt.Errorf(
			"replacement Runtime liveness regressed during stability verification",
		)
	}
	return workerapp.RuntimeRecoveryHealthEvidence{
		Stable: !now.Before(startedAt.Add(runtimeRecoveryStabilityWindow)),
	}, nil
}

var _ workerapp.RuntimeRecoveryEffects = localRuntimeRecoveryEffects{}
var _ workerapp.WorkerControlEventObserver = localRuntimeControlObserver{}
