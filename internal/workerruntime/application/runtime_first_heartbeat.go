package application

import (
	"context"
	"fmt"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type RuntimeFirstHeartbeatStage string

const (
	RuntimeFirstHeartbeatStageSuspect   RuntimeFirstHeartbeatStage = "suspect"
	RuntimeFirstHeartbeatStageUnhealthy RuntimeFirstHeartbeatStage = "unhealthy"
)

type RuntimeFirstHeartbeatAnchorKind string

const (
	RuntimeFirstHeartbeatAnchorResumeComplete RuntimeFirstHeartbeatAnchorKind = "resume_complete_v1"
	RuntimeFirstHeartbeatAnchorLegacyRuntime  RuntimeFirstHeartbeatAnchorKind = "legacy_runtime_started_v1"
)

type RuntimeFirstHeartbeatStatus string

const (
	RuntimeFirstHeartbeatAwaiting  RuntimeFirstHeartbeatStatus = "awaiting"
	RuntimeFirstHeartbeatSuspect   RuntimeFirstHeartbeatStatus = "suspect"
	RuntimeFirstHeartbeatSatisfied RuntimeFirstHeartbeatStatus = "satisfied"
	RuntimeFirstHeartbeatUnhealthy RuntimeFirstHeartbeatStatus = "unhealthy"
	RuntimeFirstHeartbeatCancelled RuntimeFirstHeartbeatStatus = "cancelled"
)

type RuntimeFirstHeartbeatTerminalReason string

const (
	RuntimeFirstHeartbeatReplacementTimeout RuntimeFirstHeartbeatTerminalReason = "replacement_first_heartbeat_timeout"
	RuntimeFirstHeartbeatBackendDeadline    RuntimeFirstHeartbeatTerminalReason = "backend_deadline_owned"
	RuntimeFirstHeartbeatTurnOutcome        RuntimeFirstHeartbeatTerminalReason = "turn_outcome_owned"
	RuntimeFirstHeartbeatRuntimeNotLive     RuntimeFirstHeartbeatTerminalReason = "runtime_not_live"
)

type EvaluateRuntimeFirstHeartbeatCommand struct {
	TenantID                 string
	RuntimeInstanceID        string
	RuntimeGeneration        uint64
	ChannelEpoch             uint64
	AnchorKind               RuntimeFirstHeartbeatAnchorKind
	AnchorRevision           uint64
	Stage                    RuntimeFirstHeartbeatStage
	ExpectedWatchdogRevision uint64
	EvaluatedAt              time.Time
}

type EvaluateRuntimeFirstHeartbeatRecord struct {
	Command                 EvaluateRuntimeFirstHeartbeatCommand
	UnhealthyCommandID      string
	UnhealthyIdempotencyKey string
}

type RuntimeFirstHeartbeatEvaluation struct {
	Applied          bool
	Status           RuntimeFirstHeartbeatStatus
	TerminalReason   RuntimeFirstHeartbeatTerminalReason
	WatchdogRevision uint64
	RuntimeRevision  uint64
	OpenRecovery     *OpenRuntimeRecoveryCommand
}

type RuntimeFirstHeartbeatStore interface {
	EvaluateRuntimeFirstHeartbeat(
		context.Context,
		EvaluateRuntimeFirstHeartbeatRecord,
	) (RuntimeFirstHeartbeatEvaluation, error)
}

type RuntimeFirstHeartbeatService struct {
	Store RuntimeFirstHeartbeatStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service RuntimeFirstHeartbeatService) Evaluate(
	ctx context.Context,
	command EvaluateRuntimeFirstHeartbeatCommand,
) (RuntimeFirstHeartbeatEvaluation, error) {
	if service.Store == nil || service.Now == nil {
		return RuntimeFirstHeartbeatEvaluation{}, fmt.Errorf(
			"Runtime first-heartbeat dependencies are required",
		)
	}
	for name, value := range map[string]string{
		"tenant_id":           command.TenantID,
		"runtime_instance_id": command.RuntimeInstanceID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return RuntimeFirstHeartbeatEvaluation{}, err
		}
	}
	if command.RuntimeGeneration == 0 ||
		command.ChannelEpoch == 0 ||
		command.AnchorRevision == 0 ||
		command.ExpectedWatchdogRevision == 0 {
		return RuntimeFirstHeartbeatEvaluation{}, fmt.Errorf(
			"Runtime first-heartbeat identity, anchor, and revision are required",
		)
	}
	if !command.AnchorKind.valid() {
		return RuntimeFirstHeartbeatEvaluation{}, fmt.Errorf(
			"Runtime first-heartbeat anchor kind %q is invalid",
			command.AnchorKind,
		)
	}
	if !command.Stage.valid() {
		return RuntimeFirstHeartbeatEvaluation{}, fmt.Errorf(
			"Runtime first-heartbeat stage %q is invalid",
			command.Stage,
		)
	}

	evaluatedAt := normalizePostgresTimestamp(service.Now())
	if evaluatedAt.IsZero() {
		return RuntimeFirstHeartbeatEvaluation{}, fmt.Errorf(
			"Runtime first-heartbeat server evaluation time is required",
		)
	}
	command.EvaluatedAt = evaluatedAt
	record := EvaluateRuntimeFirstHeartbeatRecord{Command: command}
	if command.Stage == RuntimeFirstHeartbeatStageSuspect {
		if service.IDs == nil {
			return RuntimeFirstHeartbeatEvaluation{}, fmt.Errorf(
				"Runtime first-heartbeat suspect evaluation requires an ID generator",
			)
		}
		commandID, err := service.IDs.NewID(
			"scheduled-runtime-first-heartbeat-watchdog",
		)
		if err != nil {
			return RuntimeFirstHeartbeatEvaluation{}, err
		}
		if err := validateCanonical(
			"unhealthy_command_id",
			commandID,
		); err != nil {
			return RuntimeFirstHeartbeatEvaluation{}, err
		}
		record.UnhealthyCommandID = commandID
		record.UnhealthyIdempotencyKey = fmt.Sprintf(
			"first-heartbeat/%d/%d/unhealthy",
			command.RuntimeGeneration,
			command.ChannelEpoch,
		)
	}

	evaluation, err := service.Store.EvaluateRuntimeFirstHeartbeat(ctx, record)
	if err != nil {
		return RuntimeFirstHeartbeatEvaluation{}, err
	}
	if err := validateRuntimeFirstHeartbeatEvaluation(command, evaluation); err != nil {
		return RuntimeFirstHeartbeatEvaluation{}, err
	}
	return evaluation, nil
}

func ClassifyRuntimeFirstHeartbeat(
	anchorAt time.Time,
	evaluatedAt time.Time,
) (RuntimeFirstHeartbeatStatus, error) {
	if anchorAt.IsZero() || evaluatedAt.IsZero() {
		return "", fmt.Errorf("first heartbeat anchor and evaluation time are required")
	}
	if evaluatedAt.Before(anchorAt) {
		return "", fmt.Errorf("first heartbeat evaluation precedes its immutable anchor")
	}

	elapsed := evaluatedAt.Sub(anchorAt)
	switch {
	case elapsed >= RuntimeUnhealthyAfter:
		return RuntimeFirstHeartbeatUnhealthy, nil
	case elapsed >= RuntimeSuspectAfter:
		return RuntimeFirstHeartbeatSuspect, nil
	default:
		return RuntimeFirstHeartbeatAwaiting, nil
	}
}

func validateRuntimeFirstHeartbeatEvaluation(
	command EvaluateRuntimeFirstHeartbeatCommand,
	evaluation RuntimeFirstHeartbeatEvaluation,
) error {
	if !evaluation.Applied &&
		evaluation == (RuntimeFirstHeartbeatEvaluation{}) {
		return nil
	}
	if !evaluation.Status.valid() {
		return fmt.Errorf(
			"Runtime first-heartbeat status %q is invalid",
			evaluation.Status,
		)
	}
	if !allRevisions(evaluation.WatchdogRevision, evaluation.RuntimeRevision) {
		return fmt.Errorf(
			"Runtime first-heartbeat evaluation revisions are required",
		)
	}

	if evaluation.OpenRecovery == nil {
		switch evaluation.Status {
		case RuntimeFirstHeartbeatUnhealthy:
			if evaluation.TerminalReason != RuntimeFirstHeartbeatReplacementTimeout {
				return fmt.Errorf(
					"unhealthy first-heartbeat watchdog without recovery requires replacement timeout reason",
				)
			}
		case RuntimeFirstHeartbeatCancelled:
			switch evaluation.TerminalReason {
			case RuntimeFirstHeartbeatBackendDeadline,
				RuntimeFirstHeartbeatTurnOutcome,
				RuntimeFirstHeartbeatRuntimeNotLive:
			default:
				return fmt.Errorf(
					"cancelled first-heartbeat watchdog requires a cancellation reason",
				)
			}
		default:
			if evaluation.TerminalReason != "" {
				return fmt.Errorf(
					"Runtime first-heartbeat status %q cannot carry a terminal reason",
					evaluation.Status,
				)
			}
		}
		return nil
	}

	if evaluation.Status != RuntimeFirstHeartbeatUnhealthy {
		return fmt.Errorf(
			"Runtime first-heartbeat recovery requires unhealthy status",
		)
	}
	if evaluation.TerminalReason != "" {
		return fmt.Errorf(
			"Runtime first-heartbeat recovery and terminal reason are mutually exclusive",
		)
	}
	recovery := *evaluation.OpenRecovery
	if err := validateOpenRuntimeRecoveryCommand(recovery); err != nil {
		return fmt.Errorf("invalid first-heartbeat recovery intent: %w", err)
	}
	if recovery.Trigger != domain.RecoveryTriggerHeartbeatUnhealthy {
		return fmt.Errorf(
			"first-heartbeat recovery trigger %q is invalid",
			recovery.Trigger,
		)
	}
	if recovery.TenantID != command.TenantID ||
		recovery.FailedRuntimeInstanceID != command.RuntimeInstanceID ||
		recovery.FailedGeneration != command.RuntimeGeneration ||
		recovery.FailedChannelEpoch != command.ChannelEpoch {
		return fmt.Errorf(
			"first-heartbeat recovery identity does not match evaluated Runtime",
		)
	}
	if recovery.ExpectedRuntimeRevision != evaluation.RuntimeRevision {
		return fmt.Errorf(
			"first-heartbeat recovery Runtime revision does not match evaluation",
		)
	}
	if recovery.EvidenceAt.After(command.EvaluatedAt) {
		return fmt.Errorf(
			"first-heartbeat recovery evidence cannot be in the future",
		)
	}
	return nil
}

func (kind RuntimeFirstHeartbeatAnchorKind) valid() bool {
	switch kind {
	case RuntimeFirstHeartbeatAnchorResumeComplete,
		RuntimeFirstHeartbeatAnchorLegacyRuntime:
		return true
	default:
		return false
	}
}

func (stage RuntimeFirstHeartbeatStage) valid() bool {
	switch stage {
	case RuntimeFirstHeartbeatStageSuspect,
		RuntimeFirstHeartbeatStageUnhealthy:
		return true
	default:
		return false
	}
}

func (status RuntimeFirstHeartbeatStatus) valid() bool {
	switch status {
	case RuntimeFirstHeartbeatAwaiting,
		RuntimeFirstHeartbeatSuspect,
		RuntimeFirstHeartbeatSatisfied,
		RuntimeFirstHeartbeatUnhealthy,
		RuntimeFirstHeartbeatCancelled:
		return true
	default:
		return false
	}
}
