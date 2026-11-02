package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrIllegalRuntimeRecoveryTransition = errors.New("illegal RuntimeRecoveryIncident transition")
	ErrInvalidRuntimeRecoveryEvidence   = errors.New("invalid RuntimeRecoveryIncident evidence")
)

type RuntimeRecoveryTrigger string

const (
	RecoveryTriggerContainerExit    RuntimeRecoveryTrigger = "container_exit"
	RecoveryTriggerContainerMissing RuntimeRecoveryTrigger = "container_missing"
	// RecoveryTriggerWorkerControlClosed is retained only to decode and
	// reconcile incidents persisted before transport close became reconnect
	// evidence. It is intentionally ineligible for new incidents.
	RecoveryTriggerWorkerControlClosed   RuntimeRecoveryTrigger = "worker_control_closed"
	RecoveryTriggerACPEOF                RuntimeRecoveryTrigger = "acp_eof"
	RecoveryTriggerHeartbeatUnhealthy    RuntimeRecoveryTrigger = "heartbeat_unhealthy"
	RecoveryTriggerUserCancellation      RuntimeRecoveryTrigger = "user_cancellation"
	RecoveryTriggerSessionExpiry         RuntimeRecoveryTrigger = "session_expiry"
	RecoveryTriggerAuthorization         RuntimeRecoveryTrigger = "authorization"
	RecoveryTriggerPolicy                RuntimeRecoveryTrigger = "policy"
	RecoveryTriggerProtocol              RuntimeRecoveryTrigger = "protocol"
	RecoveryTriggerResourceExhausted     RuntimeRecoveryTrigger = "resource_exhausted"
	RecoveryTriggerUnknownExternalEffect RuntimeRecoveryTrigger = "unknown_external_effect"
)

func (trigger RuntimeRecoveryTrigger) Eligible() bool {
	switch trigger {
	case RecoveryTriggerContainerExit,
		RecoveryTriggerContainerMissing,
		RecoveryTriggerACPEOF,
		RecoveryTriggerHeartbeatUnhealthy:
		return true
	default:
		return false
	}
}

type RuntimeRecoveryPhase string

const (
	RecoveryOpen         RuntimeRecoveryPhase = "open"
	RecoveryFencing      RuntimeRecoveryPhase = "fencing"
	RecoveryProvisioning RuntimeRecoveryPhase = "provisioning"
	RecoveryReattaching  RuntimeRecoveryPhase = "reattaching"
	RecoveryVerifying    RuntimeRecoveryPhase = "verifying"
	RecoveryRecovered    RuntimeRecoveryPhase = "recovered"
	RecoveryExhausted    RuntimeRecoveryPhase = "exhausted"
)

type RuntimeRecoveryOutcome string

const (
	RecoveryOutcomeNone      RuntimeRecoveryOutcome = ""
	RecoveryOutcomeRecovered RuntimeRecoveryOutcome = "recovered"
	RecoveryOutcomeExhausted RuntimeRecoveryOutcome = "exhausted"
)

type RuntimeRecoveryFenceStatus string

const (
	RecoveryFencePending   RuntimeRecoveryFenceStatus = "pending"
	RecoveryFenceConfirmed RuntimeRecoveryFenceStatus = "confirmed"
	RecoveryFenceUnproven  RuntimeRecoveryFenceStatus = "unproven"
)

type RuntimeRecoveryContinuityStatus string

const (
	RecoveryContinuityPending  RuntimeRecoveryContinuityStatus = "pending"
	RecoveryContinuityProven   RuntimeRecoveryContinuityStatus = "proven"
	RecoveryContinuityUnproven RuntimeRecoveryContinuityStatus = "unproven"
)

type RuntimeRecoveryIncident struct {
	IncidentID                   string
	TenantID                     string
	WorkerSessionID              string
	WorkerRunID                  string
	AgentTurnID                  string
	FailedRuntimeInstanceID      string
	FailedGeneration             uint64
	FailedChannelEpoch           uint64
	Trigger                      RuntimeRecoveryTrigger
	Phase                        RuntimeRecoveryPhase
	AttemptCount                 uint32
	ReplacementRuntimeInstanceID string
	ReplacementGeneration        uint64
	ReplacementChannelEpoch      uint64
	FenceStatus                  RuntimeRecoveryFenceStatus
	MountFenceToken              uint64
	ContinuityStatus             RuntimeRecoveryContinuityStatus
	PreviousSessionStatus        WorkerSessionStatus
	EvidenceRef                  string
	EvidenceAt                   time.Time
	Outcome                      RuntimeRecoveryOutcome
	OutcomeReason                string
	Revision                     uint64
	OpenedAt                     time.Time
	UpdatedAt                    time.Time
	ClosedAt                     *time.Time
}

type OpenRuntimeRecoveryIncidentCommand struct {
	IncidentID              string
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	FailedRuntimeInstanceID string
	FailedGeneration        uint64
	FailedChannelEpoch      uint64
	Trigger                 RuntimeRecoveryTrigger
	PreviousSessionStatus   WorkerSessionStatus
	EvidenceRef             string
	EvidenceAt              time.Time
	OpenedAt                time.Time
}

func OpenRuntimeRecoveryIncident(command OpenRuntimeRecoveryIncidentCommand) (RuntimeRecoveryIncident, error) {
	for name, value := range map[string]string{
		"incident_id": command.IncidentID, "tenant_id": command.TenantID,
		"worker_session_id": command.WorkerSessionID, "failed_runtime_instance_id": command.FailedRuntimeInstanceID,
		"evidence_ref": command.EvidenceRef,
	} {
		if strings.TrimSpace(value) == "" {
			return RuntimeRecoveryIncident{}, fmt.Errorf("%w: %s is required", ErrInvalidRuntimeRecoveryEvidence, name)
		}
	}
	if !command.Trigger.Eligible() || command.FailedGeneration == 0 || command.FailedChannelEpoch == 0 ||
		command.EvidenceAt.IsZero() || command.OpenedAt.IsZero() || command.OpenedAt.Before(command.EvidenceAt) {
		return RuntimeRecoveryIncident{}, ErrInvalidRuntimeRecoveryEvidence
	}
	switch command.PreviousSessionStatus {
	case SessionInteractive, SessionRunning, SessionWaitingUser, SessionIdle:
	default:
		return RuntimeRecoveryIncident{}, ErrInvalidRuntimeRecoveryEvidence
	}
	return RuntimeRecoveryIncident{
		IncidentID: command.IncidentID, TenantID: command.TenantID,
		WorkerSessionID: command.WorkerSessionID, WorkerRunID: command.WorkerRunID,
		AgentTurnID: command.AgentTurnID, FailedRuntimeInstanceID: command.FailedRuntimeInstanceID,
		FailedGeneration: command.FailedGeneration, FailedChannelEpoch: command.FailedChannelEpoch,
		Trigger: command.Trigger, Phase: RecoveryOpen, FenceStatus: RecoveryFencePending,
		ContinuityStatus: RecoveryContinuityPending, PreviousSessionStatus: command.PreviousSessionStatus,
		EvidenceRef: command.EvidenceRef, EvidenceAt: command.EvidenceAt.UTC(),
		Revision: 1, OpenedAt: command.OpenedAt.UTC(), UpdatedAt: command.OpenedAt.UTC(),
	}, nil
}

type RuntimeRecoveryTransition struct {
	FenceStatus                  RuntimeRecoveryFenceStatus
	MountFenceToken              uint64
	ReplacementRuntimeInstanceID string
	ReplacementGeneration        uint64
	ReplacementChannelEpoch      uint64
	ContinuityStatus             RuntimeRecoveryContinuityStatus
	OutcomeReason                string
}

var runtimeRecoveryTransitions = map[RuntimeRecoveryPhase]map[RuntimeRecoveryPhase]struct{}{
	RecoveryOpen:         {RecoveryFencing: {}, RecoveryExhausted: {}},
	RecoveryFencing:      {RecoveryProvisioning: {}, RecoveryExhausted: {}},
	RecoveryProvisioning: {RecoveryReattaching: {}, RecoveryExhausted: {}},
	RecoveryReattaching:  {RecoveryVerifying: {}, RecoveryExhausted: {}},
	RecoveryVerifying:    {RecoveryRecovered: {}, RecoveryExhausted: {}},
}

func (incident RuntimeRecoveryIncident) Transition(
	target RuntimeRecoveryPhase,
	at time.Time,
	transition RuntimeRecoveryTransition,
) (RuntimeRecoveryIncident, error) {
	if _, ok := runtimeRecoveryTransitions[incident.Phase][target]; !ok || incident.IsTerminal() {
		return incident, fmt.Errorf("%w: %q -> %q", ErrIllegalRuntimeRecoveryTransition, incident.Phase, target)
	}
	if at.IsZero() || at.Before(incident.UpdatedAt) {
		return incident, ErrInvalidRuntimeRecoveryEvidence
	}
	if err := incident.validateTransition(target, transition); err != nil {
		return incident, err
	}
	next := incident
	next.Phase = target
	next.Revision++
	next.UpdatedAt = at.UTC()
	if transition.FenceStatus != "" {
		next.FenceStatus = transition.FenceStatus
	}
	if transition.ContinuityStatus != "" {
		next.ContinuityStatus = transition.ContinuityStatus
	}
	if target == RecoveryProvisioning {
		next.AttemptCount = 1
		next.MountFenceToken = transition.MountFenceToken
		next.ReplacementRuntimeInstanceID = transition.ReplacementRuntimeInstanceID
		next.ReplacementGeneration = transition.ReplacementGeneration
		next.ReplacementChannelEpoch = transition.ReplacementChannelEpoch
	}
	if target == RecoveryRecovered || target == RecoveryExhausted {
		next.OutcomeReason = transition.OutcomeReason
		next.Outcome = RecoveryOutcomeRecovered
		if target == RecoveryExhausted {
			next.Outcome = RecoveryOutcomeExhausted
		}
		closedAt := at.UTC()
		next.ClosedAt = &closedAt
	}
	return next, nil
}

func (incident RuntimeRecoveryIncident) validateTransition(
	target RuntimeRecoveryPhase,
	transition RuntimeRecoveryTransition,
) error {
	if target == RecoveryProvisioning {
		if incident.AttemptCount != 0 || transition.FenceStatus != RecoveryFenceConfirmed ||
			transition.MountFenceToken == 0 || strings.TrimSpace(transition.ReplacementRuntimeInstanceID) == "" ||
			transition.ReplacementGeneration != incident.FailedGeneration+1 ||
			transition.ReplacementChannelEpoch <= incident.FailedChannelEpoch {
			return ErrInvalidRuntimeRecoveryEvidence
		}
	}
	if target == RecoveryVerifying && transition.ContinuityStatus != RecoveryContinuityProven {
		return ErrInvalidRuntimeRecoveryEvidence
	}
	if target == RecoveryRecovered &&
		(incident.AttemptCount != 1 || transition.ContinuityStatus != RecoveryContinuityProven ||
			strings.TrimSpace(transition.OutcomeReason) == "") {
		return ErrInvalidRuntimeRecoveryEvidence
	}
	if target == RecoveryExhausted && strings.TrimSpace(transition.OutcomeReason) == "" {
		return ErrInvalidRuntimeRecoveryEvidence
	}
	return nil
}

func (incident RuntimeRecoveryIncident) IsTerminal() bool {
	return incident.Phase == RecoveryRecovered || incident.Phase == RecoveryExhausted
}
