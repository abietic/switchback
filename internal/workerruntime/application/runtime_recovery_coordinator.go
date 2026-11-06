package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

var ErrReplacementFirstHeartbeatTimeout = errors.New(
	"replacement Runtime did not persist its first heartbeat before the immutable deadline",
)

type RuntimeRecoveryFenceEvidence struct {
	ExclusiveWriteAuthority bool
	MountFenceToken         uint64
}

type RuntimeRecoveryAttachmentEvidence struct {
	ContinuityProven bool
}

type RuntimeRecoveryHealthEvidence struct {
	Stable bool
}

type RuntimeRecoveryEffects interface {
	FenceFailedRuntime(context.Context, RuntimeRecoveryState) (RuntimeRecoveryFenceEvidence, error)
	FenceReplacementRuntime(context.Context, RuntimeRecoveryState) error
	FenceLateProvisionedRuntime(context.Context, RuntimeRecoveryState, ProvisionedRuntime) error
	PrepareReplacementRuntime(context.Context, RuntimeRecoveryState) (RuntimeProvisionRequest, error)
	ReattachReplacementRuntime(context.Context, RuntimeRecoveryState) (RuntimeRecoveryAttachmentEvidence, error)
	VerifyReplacementRuntime(context.Context, RuntimeRecoveryState) (RuntimeRecoveryHealthEvidence, error)
}

// ErrRuntimePreparationRetryable means the reserved replacement has not reached
// Provision and its exact preparation can be retried by the existing phase owner.
var ErrRuntimePreparationRetryable = errors.New("Runtime preparation temporarily unavailable")

type BackendDeadlineSettlementClaimer interface {
	ClaimBackendDeadline(
		context.Context,
		ClaimBackendDeadlineCommand,
	) (TurnSettlement, error)
}

// RuntimeRecoveryCoordinator reconciles one persisted phase at a time. Every
// external effect is preceded by the phase that owns that effect, so a Backend
// restart can resume from the durable incident without inventing authority.
type RuntimeRecoveryCoordinator struct {
	Recovery        RuntimeRecoveryService
	DeadlineSources BackendDeadlineSettlementClaimer
	Effects         RuntimeRecoveryEffects
	Provisioner     RuntimeProvisioner
}

func (coordinator RuntimeRecoveryCoordinator) Reconcile(
	ctx context.Context,
	state RuntimeRecoveryState,
) (RuntimeRecoveryState, error) {
	if coordinator.Effects == nil || coordinator.Provisioner == nil {
		return state, fmt.Errorf("Runtime recovery coordinator dependencies are required")
	}
	if err := coordinator.validateDeadlineSourceState(state); err != nil {
		return state, err
	}
	for !state.PhaseTerminal() {
		if reason := coordinator.preemptionReason(state); reason != "" {
			return coordinator.exhaust(ctx, state, reason, nil)
		}
		// Persisted replacement intent is not integrated execution authority.
		// Check after deadline/expiry preemption so fencing and cleanup still converge.
		if state.Phase == domain.RecoveryProvisioning || state.Phase == domain.RecoveryReattaching {
			return state, CapacityPathNotIntegrated
		}
		switch state.Phase {
		case domain.RecoveryOpen:
			next, err := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
				TargetPhase: domain.RecoveryFencing,
			})
			if err != nil {
				return state, err
			}
			state = next
		case domain.RecoveryFencing:
			effectContext, cancel := coordinator.effectContext(ctx, state)
			evidence, err := coordinator.Effects.FenceFailedRuntime(effectContext, state)
			cancel()
			if reason := coordinator.preemptionReason(state); reason != "" {
				return coordinator.exhaust(ctx, state, reason, err)
			}
			if err != nil && ctx.Err() != nil {
				return state, err
			}
			if err != nil || !evidence.ExclusiveWriteAuthority ||
				evidence.MountFenceToken <= state.FailedChannelEpoch {
				return coordinator.exhaust(
					ctx, state, "runtime_fencing_failed", err,
				)
			}
			next, err := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
				TargetPhase:     domain.RecoveryProvisioning,
				FenceStatus:     domain.RecoveryFenceConfirmed,
				MountFenceToken: evidence.MountFenceToken,
			})
			if err != nil {
				return state, err
			}
			state = next
		case domain.RecoveryProvisioning:
			effectContext, cancel := coordinator.effectContext(ctx, state)
			request, err := coordinator.Effects.PrepareReplacementRuntime(effectContext, state)
			cancel()
			if reason := coordinator.preemptionReason(state); reason != "" {
				return coordinator.exhaust(ctx, state, reason, err)
			}
			if err != nil && ctx.Err() != nil {
				return state, err
			}
			if err != nil {
				if errors.Is(err, ErrRuntimePreparationRetryable) {
					return state, err
				}
				return coordinator.exhaust(ctx, state, "replacement_environment_failed", err)
			}
			if err := validateReplacementProvisionRequest(state, request); err != nil {
				return coordinator.exhaust(ctx, state, "replacement_identity_mismatch", err)
			}
			effectContext, cancel = coordinator.effectContext(ctx, state)
			provisioned, err := coordinator.Provisioner.Provision(effectContext, request, RejectRuntimePreStart)
			cancel()
			if reason := coordinator.preemptionReason(state); reason != "" {
				if provisionedReplacementMatches(state, provisioned) {
					return coordinator.exhaustProvisionedReplacement(
						ctx, state, provisioned, reason, err,
					)
				}
				return coordinator.exhaust(ctx, state, reason, err)
			}
			if err != nil && ctx.Err() != nil {
				return state, err
			}
			if err != nil {
				return coordinator.exhaust(ctx, state, "replacement_provision_failed", err)
			}
			if !provisionedReplacementMatches(state, provisioned) {
				return coordinator.exhaust(
					ctx, state, "replacement_identity_mismatch",
					fmt.Errorf("provisioned replacement identity differs from reserved identity"),
				)
			}
			next, err := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
				TargetPhase: domain.RecoveryReattaching, ContainerID: provisioned.ContainerID,
			})
			if err != nil {
				return state, err
			}
			state = next
		case domain.RecoveryReattaching:
			effectContext, cancel := coordinator.effectContext(ctx, state)
			attachment, err := coordinator.Effects.ReattachReplacementRuntime(effectContext, state)
			cancel()
			if reason := coordinator.preemptionReason(state); reason != "" {
				return coordinator.exhaust(ctx, state, reason, err)
			}
			if err != nil && ctx.Err() != nil {
				return state, err
			}
			if err != nil || !attachment.ContinuityProven {
				return coordinator.exhaust(ctx, state, "recovery_continuity_unproven", err)
			}
			next, err := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
				TargetPhase:      domain.RecoveryVerifying,
				ContinuityStatus: domain.RecoveryContinuityProven,
			})
			if err != nil {
				return state, err
			}
			state = next
		case domain.RecoveryVerifying:
			effectContext, cancel := coordinator.effectContext(ctx, state)
			health, err := coordinator.Effects.VerifyReplacementRuntime(effectContext, state)
			cancel()
			if reason := coordinator.preemptionReason(state); reason != "" {
				return coordinator.exhaust(ctx, state, reason, err)
			}
			if err != nil && ctx.Err() != nil {
				return state, err
			}
			if errors.Is(
				err,
				ErrReplacementFirstHeartbeatTimeout,
			) {
				return coordinator.exhaust(
					ctx,
					state,
					"replacement_first_heartbeat_timeout",
					err,
				)
			}
			if err != nil {
				return coordinator.exhaust(ctx, state, "replacement_health_failed", err)
			}
			if !health.Stable {
				return state, nil
			}
			next, err := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
				TargetPhase:      domain.RecoveryRecovered,
				ContinuityStatus: domain.RecoveryContinuityProven,
				OutcomeReason:    "replacement_runtime_stable",
			})
			if err != nil {
				return state, err
			}
			state = next
		default:
			return state, fmt.Errorf("unsupported Runtime recovery phase %q", state.Phase)
		}
	}
	return state, nil
}

func (coordinator RuntimeRecoveryCoordinator) advance(
	ctx context.Context,
	state RuntimeRecoveryState,
	command AdvanceRuntimeRecoveryCommand,
) (RuntimeRecoveryState, error) {
	command.TenantID = state.TenantID
	command.WorkerSessionID = state.WorkerSessionID
	command.IncidentID = state.IncidentID
	command.ExpectedIncidentRevision = state.IncidentRevision
	command.IdempotencyKey = fmt.Sprintf(
		"recovery/%s/%s/r%d",
		state.IncidentID,
		command.TargetPhase,
		state.IncidentRevision,
	)
	return coordinator.Recovery.Advance(ctx, command)
}

func (coordinator RuntimeRecoveryCoordinator) exhaust(
	ctx context.Context,
	state RuntimeRecoveryState,
	reason string,
	cause error,
) (RuntimeRecoveryState, error) {
	if reason == "agent_turn_deadline" {
		exhausted, err := coordinator.claimBackendDeadline(ctx, state)
		if err != nil {
			return state, errors.Join(cause, err)
		}
		return exhausted, cause
	}
	exhausted, err := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
		TargetPhase: domain.RecoveryExhausted, OutcomeReason: reason,
	})
	if err != nil {
		return state, errors.Join(cause, err)
	}
	cleanupErr := coordinator.Effects.FenceReplacementRuntime(ctx, exhausted)
	return exhausted, cleanupErr
}

func (coordinator RuntimeRecoveryCoordinator) exhaustProvisionedReplacement(
	ctx context.Context,
	state RuntimeRecoveryState,
	provisioned ProvisionedRuntime,
	reason string,
	cause error,
) (RuntimeRecoveryState, error) {
	if reason == "agent_turn_deadline" {
		exhausted, claimErr := coordinator.claimBackendDeadline(ctx, state)
		// Provision returned an external identity after the deadline owner was
		// selected, before that identity could be persisted on the incident.
		// Fence exactly what Provision returned even if another lifecycle owner
		// won the source claim.
		cleanupErr := coordinator.Effects.FenceLateProvisionedRuntime(
			ctx,
			state,
			provisioned,
		)
		if claimErr != nil {
			return state, errors.Join(cause, claimErr, cleanupErr)
		}
		return exhausted, errors.Join(cause, cleanupErr)
	}
	exhausted, advanceErr := coordinator.advance(ctx, state, AdvanceRuntimeRecoveryCommand{
		TargetPhase: domain.RecoveryExhausted, OutcomeReason: reason,
	})
	if advanceErr != nil {
		// A lifecycle owner already committed a newer incident revision. Bypass
		// the stale aggregate revisions and fence only the exact external
		// identity returned by this in-flight provision call.
		cleanupErr := coordinator.Effects.FenceLateProvisionedRuntime(
			ctx, state, provisioned,
		)
		return state, errors.Join(cause, advanceErr, cleanupErr)
	}
	// The exhausted transition happened before the replacement could be
	// attached, so its container id is not necessarily present in the stored
	// state. Carry the exact returned identity into durable cleanup.
	exhausted.ReplacementRuntimeInstanceID = provisioned.RuntimeInstanceID
	exhausted.ReplacementGeneration = provisioned.Generation
	exhausted.ReplacementContainerID = provisioned.ContainerID
	cleanupErr := coordinator.Effects.FenceReplacementRuntime(ctx, exhausted)
	return exhausted, cleanupErr
}

func (coordinator RuntimeRecoveryCoordinator) claimBackendDeadline(
	ctx context.Context,
	state RuntimeRecoveryState,
) (RuntimeRecoveryState, error) {
	runtimeInstanceID := state.FailedRuntimeInstanceID
	runtimeGeneration := state.FailedGeneration
	channelEpoch := state.FailedChannelEpoch
	if state.ReplacementRuntimeInstanceID != "" {
		if state.ReplacementGeneration == 0 ||
			state.ReplacementChannelEpoch == 0 {
			return state, ErrTurnSettlementSourceConflict
		}
		runtimeInstanceID = state.ReplacementRuntimeInstanceID
		runtimeGeneration = state.ReplacementGeneration
		channelEpoch = state.ReplacementChannelEpoch
	}
	command := ClaimBackendDeadlineCommand{
		TenantID:          state.TenantID,
		WorkerSessionID:   state.WorkerSessionID,
		WorkerRunID:       state.WorkerRunID,
		AgentSessionID:    state.AgentSessionID,
		AgentTurnID:       state.AgentTurnID,
		RuntimeInstanceID: runtimeInstanceID,
		RuntimeGeneration: runtimeGeneration,
		ChannelEpoch:      channelEpoch,
		DeadlineAt:        state.AgentTurnDeadlineAt,
		SourceRef:         state.AgentTurnDeadlineSourceRef,
	}
	settlement, err := coordinator.DeadlineSources.ClaimBackendDeadline(
		ctx,
		command,
	)
	if err != nil {
		return state, err
	}
	expected, err := NewBackendDeadlineClaimRecord(
		command,
		settlement.AcceptedAt,
	)
	if err != nil {
		return state, err
	}
	if settlement.SettlementID != expected.SettlementID ||
		settlement.TenantID != command.TenantID ||
		settlement.WorkerSessionID != command.WorkerSessionID ||
		settlement.WorkerRunID != command.WorkerRunID ||
		settlement.AgentSessionID != command.AgentSessionID ||
		settlement.AgentTurnID != command.AgentTurnID ||
		settlement.Source.Kind != expected.Source.Kind ||
		settlement.Source.Reason != expected.Source.Reason ||
		!settlement.Source.OccurredAt.Equal(expected.Source.OccurredAt) ||
		settlement.Source.Ref != expected.Source.Ref ||
		settlement.Source.Digest != expected.Source.Digest ||
		ValidateTurnSettlementSource(settlement.Source) != nil {
		return state, ErrTurnSettlementSourceConflict
	}
	sourceState, err := decodeTurnSettlementSourceState(settlement.Source.State)
	if err != nil ||
		sourceState.RecoveryIncidentID != state.IncidentID ||
		sourceState.RecoveryIncidentRevision <= state.IncidentRevision ||
		!sourceState.RecoveryPreemptedAt.Equal(settlement.AcceptedAt) {
		return state, ErrTurnSettlementSourceConflict
	}
	exhausted := state
	exhausted.Phase = domain.RecoveryExhausted
	exhausted.Outcome = domain.RecoveryOutcomeExhausted
	exhausted.OutcomeReason = "preempted_by_backend_deadline"
	exhausted.IncidentRevision = sourceState.RecoveryIncidentRevision
	exhausted.UpdatedAt = sourceState.RecoveryPreemptedAt
	closedAt := sourceState.RecoveryPreemptedAt
	exhausted.ClosedAt = &closedAt
	return exhausted, nil
}

func (coordinator RuntimeRecoveryCoordinator) validateDeadlineSourceState(
	state RuntimeRecoveryState,
) error {
	if state.AgentTurnID == "" {
		return nil
	}
	if coordinator.DeadlineSources == nil ||
		state.WorkerRunID == "" ||
		state.AgentSessionID == "" ||
		state.AgentTurnDeadlineAt.IsZero() ||
		state.AgentTurnDeadlineSourceRef == "" {
		return fmt.Errorf(
			"Runtime recovery deadline source dependency and identity are required",
		)
	}
	return nil
}

func (coordinator RuntimeRecoveryCoordinator) preemptionReason(
	state RuntimeRecoveryState,
) string {
	if coordinator.Recovery.Now == nil {
		return ""
	}
	now := coordinator.Recovery.Now().UTC()
	if !state.SessionAbsoluteExpiresAt.IsZero() &&
		!now.Before(state.SessionAbsoluteExpiresAt) {
		return "worker_session_absolute_expired"
	}
	if state.AgentTurnID != "" && !state.AgentTurnDeadlineAt.IsZero() &&
		!now.Before(state.AgentTurnDeadlineAt) {
		return "agent_turn_deadline"
	}
	return ""
}

func (coordinator RuntimeRecoveryCoordinator) effectContext(
	ctx context.Context,
	state RuntimeRecoveryState,
) (context.Context, context.CancelFunc) {
	deadline := state.SessionAbsoluteExpiresAt
	if !state.AgentTurnDeadlineAt.IsZero() &&
		(deadline.IsZero() || state.AgentTurnDeadlineAt.Before(deadline)) {
		deadline = state.AgentTurnDeadlineAt
	}
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

func provisionedReplacementMatches(
	state RuntimeRecoveryState,
	provisioned ProvisionedRuntime,
) bool {
	return provisioned.RuntimeInstanceID == state.ReplacementRuntimeInstanceID &&
		provisioned.Generation == state.ReplacementGeneration &&
		provisioned.ContainerID != ""
}

func validateReplacementProvisionRequest(
	state RuntimeRecoveryState,
	request RuntimeProvisionRequest,
) error {
	if request.WorkerSessionID != state.WorkerSessionID ||
		request.WorkerRunID != state.RuntimeWorkerRunID ||
		request.RuntimeInstanceID != state.ReplacementRuntimeInstanceID ||
		request.Generation != state.ReplacementGeneration ||
		request.ResourceProfile.ID != state.ResourceProfileID ||
		request.AgentProfile.ID != state.AgentProfileID ||
		request.AgentProfile.Digest != state.AgentProfileDigest ||
		request.NetworkProfile.ID != state.NetworkPolicyID ||
		request.NetworkProfile.Revision != state.NetworkPolicyRevision ||
		request.NetworkProfile.Digest != state.NetworkPolicyDigest ||
		request.NetworkEnforcementStatus != state.NetworkEnforcementStatus {
		return fmt.Errorf("replacement Runtime provision request does not match persisted ownership")
	}
	return nil
}

func (state RuntimeRecoveryState) PhaseTerminal() bool {
	return state.Phase == domain.RecoveryRecovered || state.Phase == domain.RecoveryExhausted
}
