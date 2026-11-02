package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeRecoveryIncidentFollowsSingleAttemptLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	incident, err := domain.OpenRuntimeRecoveryIncident(domain.OpenRuntimeRecoveryIncidentCommand{
		IncidentID: "incident-1", TenantID: "tenant-local", WorkerSessionID: "session-1",
		WorkerRunID: "run-1", AgentTurnID: "turn-1", FailedRuntimeInstanceID: "runtime-2",
		FailedGeneration: 2, FailedChannelEpoch: 7, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/runtime-2", EvidenceAt: now, OpenedAt: now,
		PreviousSessionStatus: domain.SessionRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incident.Phase != domain.RecoveryOpen || incident.AttemptCount != 0 || incident.Revision != 1 {
		t.Fatalf("opened incident=%+v", incident)
	}

	incident = advanceRecovery(t, incident, domain.RecoveryFencing, now.Add(time.Second), domain.RuntimeRecoveryTransition{})
	incident = advanceRecovery(t, incident, domain.RecoveryProvisioning, now.Add(2*time.Second), domain.RuntimeRecoveryTransition{
		FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 11,
		ReplacementRuntimeInstanceID: "runtime-3", ReplacementGeneration: 3,
		ReplacementChannelEpoch: 8,
	})
	if incident.AttemptCount != 1 || incident.ReplacementGeneration != 3 || incident.MountFenceToken != 11 {
		t.Fatalf("replacement reservation=%+v", incident)
	}
	incident = advanceRecovery(t, incident, domain.RecoveryReattaching, now.Add(3*time.Second), domain.RuntimeRecoveryTransition{})
	incident = advanceRecovery(t, incident, domain.RecoveryVerifying, now.Add(4*time.Second), domain.RuntimeRecoveryTransition{
		ContinuityStatus: domain.RecoveryContinuityProven,
	})
	incident = advanceRecovery(t, incident, domain.RecoveryRecovered, now.Add(5*time.Second), domain.RuntimeRecoveryTransition{
		ContinuityStatus: domain.RecoveryContinuityProven,
		OutcomeReason:    "stable_progress",
	})
	if !incident.IsTerminal() || incident.Outcome != domain.RecoveryOutcomeRecovered ||
		incident.ClosedAt == nil || incident.Revision != 6 {
		t.Fatalf("recovered incident=%+v", incident)
	}
}

func TestRuntimeRecoveryIncidentExhaustsWhenFenceIsUnproven(t *testing.T) {
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	incident := openRecovery(t, now)
	incident = advanceRecovery(t, incident, domain.RecoveryFencing, now.Add(time.Second), domain.RuntimeRecoveryTransition{})
	incident = advanceRecovery(t, incident, domain.RecoveryExhausted, now.Add(2*time.Second), domain.RuntimeRecoveryTransition{
		FenceStatus: domain.RecoveryFenceUnproven, OutcomeReason: "mount_fence_unproven",
	})
	if incident.Outcome != domain.RecoveryOutcomeExhausted || incident.AttemptCount != 0 {
		t.Fatalf("exhausted incident=%+v", incident)
	}
}

func TestRuntimeRecoveryIncidentRejectsSplitBrainAndSecondAttempt(t *testing.T) {
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	incident := openRecovery(t, now)
	incident = advanceRecovery(t, incident, domain.RecoveryFencing, now.Add(time.Second), domain.RuntimeRecoveryTransition{})

	if _, err := incident.Transition(domain.RecoveryProvisioning, now.Add(2*time.Second), domain.RuntimeRecoveryTransition{
		FenceStatus: domain.RecoveryFenceUnproven, MountFenceToken: 1,
		ReplacementRuntimeInstanceID: "runtime-2", ReplacementGeneration: 2, ReplacementChannelEpoch: 2,
	}); !errors.Is(err, domain.ErrInvalidRuntimeRecoveryEvidence) {
		t.Fatalf("unproven fence error=%v", err)
	}
	if _, err := incident.Transition(domain.RecoveryProvisioning, now.Add(2*time.Second), domain.RuntimeRecoveryTransition{
		FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 1,
		ReplacementRuntimeInstanceID: "runtime-3", ReplacementGeneration: 3, ReplacementChannelEpoch: 2,
	}); !errors.Is(err, domain.ErrInvalidRuntimeRecoveryEvidence) {
		t.Fatalf("skipped generation error=%v", err)
	}

	incident = advanceRecovery(t, incident, domain.RecoveryProvisioning, now.Add(2*time.Second), domain.RuntimeRecoveryTransition{
		FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 1,
		ReplacementRuntimeInstanceID: "runtime-2", ReplacementGeneration: 2, ReplacementChannelEpoch: 2,
	})
	incident = advanceRecovery(t, incident, domain.RecoveryExhausted, now.Add(3*time.Second), domain.RuntimeRecoveryTransition{
		OutcomeReason: "replacement_failed",
	})
	if _, err := incident.Transition(domain.RecoveryProvisioning, now.Add(4*time.Second), domain.RuntimeRecoveryTransition{
		FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 2,
		ReplacementRuntimeInstanceID: "runtime-3", ReplacementGeneration: 3, ReplacementChannelEpoch: 3,
	}); !errors.Is(err, domain.ErrIllegalRuntimeRecoveryTransition) {
		t.Fatalf("second attempt error=%v", err)
	}
}

func TestRuntimeRecoveryEligibilityExcludesLegacyTransportCloseAndPolicyOutcomes(t *testing.T) {
	for _, trigger := range []domain.RuntimeRecoveryTrigger{
		domain.RecoveryTriggerContainerExit,
		domain.RecoveryTriggerContainerMissing,
		domain.RecoveryTriggerACPEOF,
		domain.RecoveryTriggerHeartbeatUnhealthy,
	} {
		if !trigger.Eligible() {
			t.Fatalf("eligible trigger %q rejected", trigger)
		}
	}
	for _, trigger := range []domain.RuntimeRecoveryTrigger{
		domain.RecoveryTriggerWorkerControlClosed,
		domain.RecoveryTriggerUserCancellation,
		domain.RecoveryTriggerSessionExpiry,
		domain.RecoveryTriggerAuthorization,
		domain.RecoveryTriggerPolicy,
		domain.RecoveryTriggerProtocol,
		domain.RecoveryTriggerResourceExhausted,
		domain.RecoveryTriggerUnknownExternalEffect,
	} {
		if trigger.Eligible() {
			t.Fatalf("ineligible trigger %q accepted", trigger)
		}
	}
}

func TestRuntimeRecoveryIncidentAllowsIdleRuntimeWithoutActiveRunOrTurn(t *testing.T) {
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	incident, err := domain.OpenRuntimeRecoveryIncident(domain.OpenRuntimeRecoveryIncidentCommand{
		IncidentID: "incident-idle", TenantID: "tenant-local", WorkerSessionID: "session-idle",
		FailedRuntimeInstanceID: "runtime-idle", FailedGeneration: 3, FailedChannelEpoch: 5,
		Trigger: domain.RecoveryTriggerHeartbeatUnhealthy, PreviousSessionStatus: domain.SessionIdle,
		EvidenceRef: "heartbeat/runtime-idle/9", EvidenceAt: now, OpenedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incident.WorkerRunID != "" || incident.AgentTurnID != "" || incident.Phase != domain.RecoveryOpen {
		t.Fatalf("idle incident=%+v", incident)
	}
}

func openRecovery(t *testing.T, now time.Time) domain.RuntimeRecoveryIncident {
	t.Helper()
	incident, err := domain.OpenRuntimeRecoveryIncident(domain.OpenRuntimeRecoveryIncidentCommand{
		IncidentID: "incident-1", TenantID: "tenant-local", WorkerSessionID: "session-1",
		WorkerRunID: "run-1", AgentTurnID: "turn-1", FailedRuntimeInstanceID: "runtime-1",
		FailedGeneration: 1, FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/runtime-1", EvidenceAt: now, OpenedAt: now,
		PreviousSessionStatus: domain.SessionRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	return incident
}

func advanceRecovery(
	t *testing.T,
	incident domain.RuntimeRecoveryIncident,
	target domain.RuntimeRecoveryPhase,
	at time.Time,
	transition domain.RuntimeRecoveryTransition,
) domain.RuntimeRecoveryIncident {
	t.Helper()
	next, err := incident.Transition(target, at, transition)
	if err != nil {
		t.Fatal(err)
	}
	return next
}
