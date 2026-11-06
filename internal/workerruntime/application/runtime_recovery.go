package application

import (
	"context"
	"fmt"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type OpenRuntimeRecoveryCommand struct {
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	FailedRuntimeInstanceID string
	FailedGeneration        uint64
	FailedChannelEpoch      uint64
	Trigger                 domain.RuntimeRecoveryTrigger
	EvidenceRef             string
	EvidenceAt              time.Time
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
	IdempotencyKey          string
}

type OpenRuntimeRecoveryRecord struct {
	Command    OpenRuntimeRecoveryCommand
	IncidentID string
	EventID    string
	Now        time.Time
}

type RuntimeRecoveryState struct {
	IncidentID                   string
	TenantID                     string
	WorkerSessionID              string
	WorkerRunID                  string
	AgentTurnID                  string
	FailedRuntimeInstanceID      string
	FailedGeneration             uint64
	FailedChannelEpoch           uint64
	Trigger                      domain.RuntimeRecoveryTrigger
	Phase                        domain.RuntimeRecoveryPhase
	AttemptCount                 uint32
	ReplacementRuntimeInstanceID string
	ReplacementGeneration        uint64
	ReplacementChannelEpoch      uint64
	FenceStatus                  domain.RuntimeRecoveryFenceStatus
	MountFenceToken              uint64
	ContinuityStatus             domain.RuntimeRecoveryContinuityStatus
	PreviousSessionStatus        domain.WorkerSessionStatus
	EvidenceRef                  string
	EvidenceAt                   time.Time
	Outcome                      domain.RuntimeRecoveryOutcome
	OutcomeReason                string
	IncidentRevision             uint64
	SessionRevision              uint64
	RunRevision                  uint64
	TurnRevision                 uint64
	FailedRuntimeRevision        uint64
	ReplacementRuntimeRevision   uint64
	ReplacementContainerID       string
	TaskStatus                   string
	TaskRevision                 uint64
	RuntimeWorkerRunID           string
	AgentSessionID               string
	RuntimeImageRef              string
	ResourceProfileID            string
	AgentProfileID               string
	AgentProfileRevision         string
	AgentProfileDigest           string
	NetworkPolicyID              string
	NetworkPolicyRevision        uint64
	NetworkPolicyDigest          string
	NetworkEnforcementStatus     string
	WorkingCopyCheckpointDigest  string
	AgentTurnDeadlineAt          time.Time
	AgentTurnDeadlineSourceRef   string
	SessionAbsoluteExpiresAt     time.Time
	OpenedAt                     time.Time
	UpdatedAt                    time.Time
	ClosedAt                     *time.Time
	EventID                      string
}

type RuntimeRecoveryStore interface {
	OpenRuntimeRecovery(context.Context, OpenRuntimeRecoveryRecord) (RuntimeRecoveryState, error)
	AdvanceRuntimeRecovery(context.Context, AdvanceRuntimeRecoveryRecord) (RuntimeRecoveryState, error)
}

// RuntimeFailureWithoutReplacementStore commits a hard failure directly to its
// terminal incident. It is separate from automatic recovery admission.
type RuntimeFailureWithoutReplacementStore interface {
	RecordRuntimeFailureWithoutReplacement(context.Context, RuntimeFailureWithoutReplacementRecord) (RuntimeRecoveryState, error)
}

type RuntimeFailureWithoutReplacementCommand struct {
	OpenRuntimeRecoveryCommand
	ExpectedContainerID string
	Inspection          RuntimeInspection
}

type RuntimeFailureWithoutReplacementRecord struct {
	Command    RuntimeFailureWithoutReplacementCommand
	IncidentID string
	EventID    string
	Now        time.Time
}

type RuntimeFailureWithoutReplacementService struct {
	Store RuntimeFailureWithoutReplacementStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service RuntimeFailureWithoutReplacementService) RecordFailureWithoutReplacement(ctx context.Context, command RuntimeFailureWithoutReplacementCommand) (RuntimeRecoveryState, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime failure dependencies are required")
	}
	if err := validateOpenRuntimeRecoveryCommand(command.OpenRuntimeRecoveryCommand); err != nil {
		return RuntimeRecoveryState{}, err
	}
	now := service.Now().UTC()
	inspection := command.Inspection
	if inspection.Running || inspection.ObservedAt.IsZero() || !inspection.ObservedAt.Equal(command.EvidenceAt) || inspection.ObservedAt.After(now) {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime failure requires a current hard inspection")
	}
	if inspection.Exists {
		if command.Trigger != domain.RecoveryTriggerContainerExit || command.ExpectedContainerID == "" || inspection.ContainerID != command.ExpectedContainerID || inspection.Generation != command.FailedGeneration {
			return RuntimeRecoveryState{}, fmt.Errorf("Runtime exit inspection identity conflict")
		}
	} else if command.Trigger != domain.RecoveryTriggerContainerMissing {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime missing inspection identity conflict")
	}
	incidentID, err := service.IDs.NewID("runtime-recovery-incident")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-runtime-recovery-failed")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	return service.Store.RecordRuntimeFailureWithoutReplacement(ctx, RuntimeFailureWithoutReplacementRecord{Command: command, IncidentID: incidentID, EventID: eventID, Now: now})
}

type RuntimeRecoveryService struct {
	Store RuntimeRecoveryStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

type AdvanceRuntimeRecoveryCommand struct {
	TenantID                 string
	WorkerSessionID          string
	IncidentID               string
	TargetPhase              domain.RuntimeRecoveryPhase
	ExpectedIncidentRevision uint64
	FenceStatus              domain.RuntimeRecoveryFenceStatus
	MountFenceToken          uint64
	ContainerID              string
	ContinuityStatus         domain.RuntimeRecoveryContinuityStatus
	OutcomeReason            string
	IdempotencyKey           string
}

type AdvanceRuntimeRecoveryRecord struct {
	Command                      AdvanceRuntimeRecoveryCommand
	ReplacementRuntimeInstanceID string
	ReplacementChannelEpoch      uint64
	EventID                      string
	TaskTransitionID             string
	Now                          time.Time
}

func (service RuntimeRecoveryService) Advance(
	ctx context.Context,
	command AdvanceRuntimeRecoveryCommand,
) (RuntimeRecoveryState, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime recovery dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "worker_session_id": command.WorkerSessionID,
		"incident_id": command.IncidentID, "idempotency_key": command.IdempotencyKey,
	} {
		if err := validateCanonical(name, value); err != nil {
			return RuntimeRecoveryState{}, err
		}
	}
	if command.ExpectedIncidentRevision == 0 {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime recovery incident revision is required")
	}
	switch command.TargetPhase {
	case domain.RecoveryFencing, domain.RecoveryProvisioning, domain.RecoveryReattaching,
		domain.RecoveryVerifying, domain.RecoveryRecovered, domain.RecoveryExhausted:
	default:
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime recovery target phase %q is invalid", command.TargetPhase)
	}
	replacementID, err := service.IDs.NewID("runtime")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-runtime-recovery")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	taskTransitionID, err := service.IDs.NewID("task-transition-runtime-recovery")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	return service.Store.AdvanceRuntimeRecovery(ctx, AdvanceRuntimeRecoveryRecord{
		Command: command, ReplacementRuntimeInstanceID: replacementID,
		ReplacementChannelEpoch: command.MountFenceToken + 1,
		EventID:                 eventID, TaskTransitionID: taskTransitionID,
		Now: service.Now().UTC(),
	})
}

func (service RuntimeRecoveryService) Open(
	ctx context.Context,
	command OpenRuntimeRecoveryCommand,
) (RuntimeRecoveryState, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime recovery dependencies are required")
	}
	if err := validateOpenRuntimeRecoveryCommand(command); err != nil {
		return RuntimeRecoveryState{}, err
	}
	now := service.Now().UTC()
	if command.EvidenceAt.After(now) {
		return RuntimeRecoveryState{}, fmt.Errorf("Runtime recovery evidence cannot be in the future")
	}
	incidentID, err := service.IDs.NewID("runtime-recovery-incident")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-runtime-recovery-started")
	if err != nil {
		return RuntimeRecoveryState{}, err
	}
	return service.Store.OpenRuntimeRecovery(ctx, OpenRuntimeRecoveryRecord{
		Command: command, IncidentID: incidentID, EventID: eventID, Now: now,
	})
}

func validateOpenRuntimeRecoveryCommand(command OpenRuntimeRecoveryCommand) error {
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "worker_session_id": command.WorkerSessionID,
		"failed_runtime_instance_id": command.FailedRuntimeInstanceID,
		"evidence_ref":               command.EvidenceRef, "idempotency_key": command.IdempotencyKey,
	} {
		if err := validateCanonical(name, value); err != nil {
			return err
		}
	}
	if (command.WorkerRunID == "") != (command.AgentTurnID == "") {
		return fmt.Errorf("WorkerRun and AgentTurn recovery identity must both be present or absent")
	}
	if command.WorkerRunID != "" {
		if err := validateCanonical("worker_run_id", command.WorkerRunID); err != nil {
			return err
		}
		if err := validateCanonical("agent_turn_id", command.AgentTurnID); err != nil {
			return err
		}
		if !allRevisions(command.ExpectedRunRevision, command.ExpectedTurnRevision) {
			return fmt.Errorf("active Runtime recovery requires exact Run and Turn revisions")
		}
	} else if command.ExpectedRunRevision != 0 || command.ExpectedTurnRevision != 0 {
		return fmt.Errorf("idle Runtime recovery cannot carry Run or Turn revisions")
	}
	if !command.Trigger.Eligible() {
		return fmt.Errorf("Runtime recovery trigger %q is ineligible", command.Trigger)
	}
	if command.FailedGeneration == 0 || command.FailedChannelEpoch == 0 ||
		command.EvidenceAt.IsZero() ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRuntimeRevision) {
		return fmt.Errorf("Runtime recovery requires exact generation, epoch, evidence time, and revisions")
	}
	return nil
}
