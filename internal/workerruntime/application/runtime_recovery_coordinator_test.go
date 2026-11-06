package application_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeRecoveryCoordinatorStopsAfterFencingUntilCapacityIntegrated(t *testing.T) {
	var journal []string
	ids := &coordinatorIDs{}
	coordinator := application.RuntimeRecoveryCoordinator{
		Recovery:        application.RuntimeRecoveryService{Store: &coordinatorRecoveryStore{journal: &journal}, IDs: ids, Now: time.Now},
		DeadlineSources: &coordinatorDeadlineSources{journal: &journal},
		Effects:         &coordinatorEffects{journal: &journal, stable: true},
		Provisioner:     &coordinatorProvisioner{journal: &journal},
	}
	result, err := coordinator.Reconcile(context.Background(), coordinatorRecoveryState())
	if err != application.CapacityPathNotIntegrated || result.Phase != domain.RecoveryProvisioning || result.AttemptCount != 1 || result.ReplacementRuntimeInstanceID == "" || result.ReplacementGeneration != 2 || result.ReplacementChannelEpoch != 3 {
		t.Fatalf("capacity gate lost reserved original intent: result=%+v err=%v", result, err)
	}
	want := []string{"persist:fencing", "effect:fence", "persist:provisioning"}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
	}
	reservedIDs := ids.sequence
	for i := 0; i < 2; i++ {
		replayed, err := coordinator.Reconcile(context.Background(), result)
		if err != application.CapacityPathNotIntegrated || replayed != result || ids.sequence != reservedIDs || !reflect.DeepEqual(journal, want) {
			t.Fatalf("capacity wait replay changed owner or issued effects: result=%+v err=%v ids=%d journal=%v", replayed, err, ids.sequence, journal)
		}
	}
}

func TestRuntimeRecoveryCoordinatorExhaustsBeforeProvisionWhenFenceIsUnproven(t *testing.T) {
	var journal []string
	store := &coordinatorRecoveryStore{journal: &journal}
	effects := &coordinatorEffects{journal: &journal}
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: store, IDs: &coordinatorIDs{}, Now: time.Now,
		},
		DeadlineSources: &coordinatorDeadlineSources{journal: &journal},
		Effects:         effects,
		Provisioner:     &coordinatorProvisioner{journal: &journal},
	}).Reconcile(context.Background(), coordinatorRecoveryState())
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != domain.RecoveryExhausted ||
		result.OutcomeReason != "runtime_fencing_failed" {
		t.Fatalf("result=%+v", result)
	}
	want := []string{
		"persist:fencing", "effect:fence", "persist:exhausted", "effect:fence_replacement",
	}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
	}
}

func TestRuntimeRecoveryCoordinatorLeavesVerifyingOpenUntilStableEvidence(t *testing.T) {
	var journal []string
	state := coordinatorRecoveryState()
	state.Phase = domain.RecoveryVerifying
	state.IncidentRevision = 5
	state.AttemptCount = 1
	state.ReplacementRuntimeInstanceID = "runtime-2"
	state.ReplacementGeneration = 2
	state.ReplacementChannelEpoch = 3
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: &coordinatorRecoveryStore{journal: &journal},
			IDs:   &coordinatorIDs{}, Now: time.Now,
		},
		DeadlineSources: &coordinatorDeadlineSources{journal: &journal},
		Effects:         &coordinatorEffects{journal: &journal, stable: false},
		Provisioner:     &coordinatorProvisioner{journal: &journal},
	}).Reconcile(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != domain.RecoveryVerifying ||
		!reflect.DeepEqual(journal, []string{"effect:verify"}) {
		t.Fatalf("result=%+v journal=%v", result, journal)
	}
}

func TestRuntimeRecoveryCoordinatorExhaustsReplacementOnFirstHeartbeatTimeout(
	t *testing.T,
) {
	var journal []string
	state := coordinatorRecoveryState()
	state.Phase = domain.RecoveryVerifying
	state.IncidentRevision = 5
	state.AttemptCount = 1
	state.ReplacementRuntimeInstanceID = "runtime-2"
	state.ReplacementGeneration = 2
	state.ReplacementChannelEpoch = 3
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: &coordinatorRecoveryStore{journal: &journal},
			IDs:   &coordinatorIDs{},
			Now:   time.Now,
		},
		DeadlineSources: &coordinatorDeadlineSources{
			journal: &journal,
		},
		Effects: &coordinatorEffects{
			journal:   &journal,
			verifyErr: application.ErrReplacementFirstHeartbeatTimeout,
		},
		Provisioner: &coordinatorProvisioner{journal: &journal},
	}).Reconcile(context.Background(), state)
	if err != nil {
		t.Fatalf("timeout convergence error=%v", err)
	}
	if result.Phase != domain.RecoveryExhausted ||
		result.OutcomeReason !=
			"replacement_first_heartbeat_timeout" {
		t.Fatalf("timeout result=%+v", result)
	}
	want := []string{
		"effect:verify",
		"persist:exhausted",
		"effect:fence_replacement",
	}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("timeout journal=%v want=%v", journal, want)
	}
}

func TestRuntimeRecoveryCoordinatorDeadlinePreemptsBeforeReplacement(t *testing.T) {
	var journal []string
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	state := coordinatorRecoveryState()
	state.AgentTurnDeadlineAt = now
	state.SessionAbsoluteExpiresAt = now.Add(time.Hour)
	deadlineSources := &coordinatorDeadlineSources{
		journal:          &journal,
		acceptedAt:       now,
		incidentID:       state.IncidentID,
		incidentRevision: state.IncidentRevision + 1,
	}
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: &coordinatorRecoveryStore{journal: &journal},
			IDs:   &coordinatorIDs{}, Now: func() time.Time { return now },
		},
		DeadlineSources: deadlineSources,
		Effects:         &coordinatorEffects{journal: &journal, stable: true},
		Provisioner:     &coordinatorProvisioner{journal: &journal},
	}).Reconcile(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != domain.RecoveryExhausted ||
		result.Outcome != domain.RecoveryOutcomeExhausted ||
		result.OutcomeReason != "preempted_by_backend_deadline" ||
		result.IncidentRevision != state.IncidentRevision+1 ||
		result.ClosedAt == nil || !result.ClosedAt.Equal(now) {
		t.Fatalf("result=%+v", result)
	}
	want := []string{"source:backend_deadline"}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
	}
	if deadlineSources.command.SourceRef != state.AgentTurnDeadlineSourceRef {
		t.Fatalf("deadline command=%+v", deadlineSources.command)
	}
}

func TestRuntimeRecoveryCoordinatorAbsoluteExpiryPreemptsDeadline(t *testing.T) {
	var journal []string
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	state := coordinatorRecoveryState()
	state.AgentTurnDeadlineAt = now
	state.SessionAbsoluteExpiresAt = now
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: &coordinatorRecoveryStore{journal: &journal},
			IDs:   &coordinatorIDs{}, Now: func() time.Time { return now },
		},
		DeadlineSources: &coordinatorDeadlineSources{journal: &journal},
		Effects:         &coordinatorEffects{journal: &journal, stable: true},
		Provisioner:     &coordinatorProvisioner{journal: &journal},
	}).Reconcile(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != domain.RecoveryExhausted ||
		result.OutcomeReason != "worker_session_absolute_expired" {
		t.Fatalf("result=%+v", result)
	}
	want := []string{"persist:exhausted", "effect:fence_replacement"}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
	}
}

func TestRuntimeRecoveryCoordinatorBlockedPhasesHonorOriginalDeadline(t *testing.T) {
	for _, phase := range []domain.RuntimeRecoveryPhase{domain.RecoveryProvisioning, domain.RecoveryReattaching} {
		t.Run(string(phase), func(t *testing.T) {
			var journal []string
			state := coordinatorProvisioningState()
			state.Phase = phase
			deadline := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
			state.AgentTurnDeadlineAt = deadline
			now := deadline.Add(-time.Nanosecond)
			source := &coordinatorDeadlineSources{journal: &journal, acceptedAt: deadline, incidentID: state.IncidentID, incidentRevision: state.IncidentRevision + 1}
			coordinator := application.RuntimeRecoveryCoordinator{
				Recovery:        application.RuntimeRecoveryService{Store: &coordinatorRecoveryStore{journal: &journal}, IDs: &coordinatorIDs{}, Now: func() time.Time { return now }},
				DeadlineSources: source, Effects: &coordinatorEffects{journal: &journal, stable: true}, Provisioner: &coordinatorProvisioner{journal: &journal},
			}
			result, err := coordinator.Reconcile(context.Background(), state)
			if err != application.CapacityPathNotIntegrated || result != state || len(journal) != 0 {
				t.Fatalf("before deadline changed original owner: result=%+v err=%v journal=%v", result, err, journal)
			}
			now = deadline
			result, err = coordinator.Reconcile(context.Background(), state)
			expected := state
			expected.Phase, expected.Outcome, expected.OutcomeReason = domain.RecoveryExhausted, domain.RecoveryOutcomeExhausted, "preempted_by_backend_deadline"
			expected.IncidentRevision++
			expected.UpdatedAt, expected.ClosedAt = deadline, &deadline
			if err != nil || !reflect.DeepEqual(result, expected) {
				t.Fatalf("deadline did not preempt capacity wait: result=%+v err=%v", result, err)
			}
			if source.command.RuntimeInstanceID != state.ReplacementRuntimeInstanceID || source.command.RuntimeGeneration != state.ReplacementGeneration || source.command.ChannelEpoch != state.ReplacementChannelEpoch || !source.command.DeadlineAt.Equal(deadline) || source.command.SourceRef != state.AgentTurnDeadlineSourceRef {
				t.Fatalf("deadline lost original exact target: %+v", source.command)
			}
			if !reflect.DeepEqual(journal, []string{"source:backend_deadline"}) {
				t.Fatalf("deadline acquired new execution effects: %v", journal)
			}
		})
	}
}

func TestRuntimeRecoveryCoordinatorBlockedDeadlineClaimFailureRetainsOwner(t *testing.T) {
	for _, phase := range []domain.RuntimeRecoveryPhase{domain.RecoveryProvisioning, domain.RecoveryReattaching} {
		t.Run(string(phase), func(t *testing.T) {
			var journal []string
			state := coordinatorProvisioningState()
			state.Phase = phase
			now := state.AgentTurnDeadlineAt
			claimErr := errors.New("deadline source owner already advanced incident")
			result, err := (application.RuntimeRecoveryCoordinator{
				Recovery:        application.RuntimeRecoveryService{Store: &coordinatorRecoveryStore{journal: &journal}, IDs: &coordinatorIDs{}, Now: func() time.Time { return now }},
				DeadlineSources: &coordinatorDeadlineSources{journal: &journal, err: claimErr},
				Effects:         &coordinatorEffects{journal: &journal, stable: true}, Provisioner: &coordinatorProvisioner{journal: &journal},
			}).Reconcile(context.Background(), state)
			if !errors.Is(err, claimErr) || result != state || !reflect.DeepEqual(journal, []string{"source:backend_deadline"}) {
				t.Fatalf("failed deadline claim changed owner or issued effects: result=%+v err=%v journal=%v", result, err, journal)
			}
		})
	}
}

func coordinatorProvisioningState() application.RuntimeRecoveryState {
	state := coordinatorRecoveryState()
	state.Phase = domain.RecoveryProvisioning
	state.IncidentRevision = 3
	state.AttemptCount = 1
	state.ReplacementRuntimeInstanceID = "runtime-2"
	state.ReplacementGeneration = 2
	state.ReplacementChannelEpoch = 3
	return state
}

func coordinatorRecoveryState() application.RuntimeRecoveryState {
	return application.RuntimeRecoveryState{
		IncidentID: "incident-1", TenantID: "tenant-local",
		WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", FailedRuntimeInstanceID: "runtime-1",
		FailedGeneration: 1, FailedChannelEpoch: 1,
		Phase: domain.RecoveryOpen, IncidentRevision: 1,
		RuntimeWorkerRunID: "run-1", AgentSessionID: "agent-session-1",
		AgentTurnDeadlineAt: time.Date(
			2100, 1, 1, 0, 0, 0, 0, time.UTC,
		),
		AgentTurnDeadlineSourceRef: "deadline-command-1",
		RuntimeImageRef:            "worker:test", ResourceProfileID: "local_standard_v1",
		AgentProfileID: "deepseek_codex_v1", AgentProfileRevision: "1",
		AgentProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NetworkPolicyID:    "trusted_full_network_v1", NetworkPolicyRevision: 1,
		NetworkPolicyDigest:      "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		NetworkEnforcementStatus: "active",
	}
}

type coordinatorRecoveryStore struct {
	journal    *[]string
	advanceErr error
	current    *application.RuntimeRecoveryState
}

type coordinatorDeadlineSources struct {
	journal          *[]string
	command          application.ClaimBackendDeadlineCommand
	acceptedAt       time.Time
	incidentID       string
	incidentRevision uint64
	err              error
}

func (sources *coordinatorDeadlineSources) ClaimBackendDeadline(
	_ context.Context,
	command application.ClaimBackendDeadlineCommand,
) (application.TurnSettlement, error) {
	if sources.journal != nil {
		*sources.journal = append(
			*sources.journal,
			"source:backend_deadline",
		)
	}
	sources.command = command
	if sources.err != nil {
		return application.TurnSettlement{}, sources.err
	}
	acceptedAt := sources.acceptedAt
	if acceptedAt.IsZero() {
		acceptedAt = command.DeadlineAt
	}
	record, err := application.NewBackendDeadlineClaimRecord(
		command,
		acceptedAt,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	incidentID := sources.incidentID
	if incidentID == "" {
		incidentID = "incident-1"
	}
	incidentRevision := sources.incidentRevision
	if incidentRevision == 0 {
		incidentRevision = 2
	}
	source, err := application.BindBackendDeadlineRecoveryPreemption(
		record.Source,
		incidentID,
		incidentRevision,
		acceptedAt,
	)
	if err != nil {
		return application.TurnSettlement{}, err
	}
	return application.TurnSettlement{
		SettlementID:    record.SettlementID,
		TenantID:        command.TenantID,
		WorkerSessionID: command.WorkerSessionID,
		WorkerRunID:     command.WorkerRunID,
		AgentSessionID:  command.AgentSessionID,
		AgentTurnID:     command.AgentTurnID,
		Source:          source,
		Phase:           application.TurnSettlementAccepted,
		Revision:        1,
		AcceptedAt:      acceptedAt,
		UpdatedAt:       acceptedAt,
	}, nil
}

func (*coordinatorRecoveryStore) OpenRuntimeRecovery(
	context.Context,
	application.OpenRuntimeRecoveryRecord,
) (application.RuntimeRecoveryState, error) {
	return application.RuntimeRecoveryState{}, nil
}

func (store *coordinatorRecoveryStore) AdvanceRuntimeRecovery(
	_ context.Context,
	record application.AdvanceRuntimeRecoveryRecord,
) (application.RuntimeRecoveryState, error) {
	*store.journal = append(*store.journal, "persist:"+string(record.Command.TargetPhase))
	if store.advanceErr != nil {
		return application.RuntimeRecoveryState{}, store.advanceErr
	}
	state := coordinatorRecoveryState()
	if store.current != nil {
		state = *store.current
	}
	state.Phase = record.Command.TargetPhase
	state.IncidentRevision = record.Command.ExpectedIncidentRevision + 1
	if state.Phase == domain.RecoveryProvisioning ||
		state.Phase == domain.RecoveryReattaching ||
		state.Phase == domain.RecoveryVerifying ||
		state.Phase == domain.RecoveryRecovered {
		state.AttemptCount = 1
		state.ReplacementRuntimeInstanceID = record.ReplacementRuntimeInstanceID
		if record.Command.TargetPhase != domain.RecoveryProvisioning {
			state.ReplacementRuntimeInstanceID = "runtime-2"
		}
		state.ReplacementGeneration = 2
		state.ReplacementChannelEpoch = 3
	}
	if state.Phase == domain.RecoveryExhausted {
		state.OutcomeReason = record.Command.OutcomeReason
	}
	return state, nil
}

type coordinatorEffects struct {
	journal           *[]string
	stable            bool
	verifyErr         error
	fencedReplacement application.ProvisionedRuntime
}

func (effects *coordinatorEffects) FenceFailedRuntime(
	context.Context,
	application.RuntimeRecoveryState,
) (application.RuntimeRecoveryFenceEvidence, error) {
	*effects.journal = append(*effects.journal, "effect:fence")
	return application.RuntimeRecoveryFenceEvidence{
		ExclusiveWriteAuthority: effects.stable,
		MountFenceToken:         2,
	}, nil
}

func (effects *coordinatorEffects) PrepareReplacementRuntime(
	_ context.Context,
	state application.RuntimeRecoveryState,
) (application.RuntimeProvisionRequest, error) {
	*effects.journal = append(*effects.journal, "effect:prepare")
	return application.RuntimeProvisionRequest{
		WorkerSessionID: state.WorkerSessionID, WorkerRunID: state.RuntimeWorkerRunID,
		RuntimeInstanceID: state.ReplacementRuntimeInstanceID,
		Generation:        state.ReplacementGeneration,
		ResourceProfile:   application.ProfileRef{ID: state.ResourceProfileID},
		AgentProfile: application.ProfileRef{
			ID: state.AgentProfileID, Digest: state.AgentProfileDigest,
		},
		NetworkProfile: application.ProfileRef{
			ID: state.NetworkPolicyID, Revision: state.NetworkPolicyRevision,
			Digest: state.NetworkPolicyDigest,
		},
		NetworkEnforcementStatus: state.NetworkEnforcementStatus,
	}, nil
}

func TestRuntimeRecoveryCoordinatorBlockedPhasesAbsoluteExpiryPreemptsCapacityAndDeadline(t *testing.T) {
	for _, phase := range []domain.RuntimeRecoveryPhase{domain.RecoveryProvisioning, domain.RecoveryReattaching} {
		for _, simultaneous := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/simultaneous=%t", phase, simultaneous), func(t *testing.T) {
				var journal []string
				state := coordinatorProvisioningState()
				state.Phase = phase
				state.SessionAbsoluteExpiresAt = time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
				state.AgentTurnDeadlineAt = state.SessionAbsoluteExpiresAt.Add(time.Minute)
				if simultaneous {
					state.AgentTurnDeadlineAt = state.SessionAbsoluteExpiresAt
				}
				if phase == domain.RecoveryReattaching {
					state.ReplacementContainerID = "original-replacement-container"
				}
				now := state.SessionAbsoluteExpiresAt
				original := state
				effects := &coordinatorEffects{journal: &journal, stable: true}
				result, err := (application.RuntimeRecoveryCoordinator{
					Recovery:        application.RuntimeRecoveryService{Store: &coordinatorRecoveryStore{journal: &journal, current: &original}, IDs: &coordinatorIDs{}, Now: func() time.Time { return now }},
					DeadlineSources: &coordinatorDeadlineSources{journal: &journal}, Effects: effects, Provisioner: &coordinatorProvisioner{journal: &journal},
				}).Reconcile(context.Background(), state)
				expected := state
				expected.Phase, expected.OutcomeReason = domain.RecoveryExhausted, "worker_session_absolute_expired"
				expected.IncidentRevision++
				if err != nil || result != expected {
					t.Fatalf("absolute expiry did not retain original identity: result=%+v err=%v", result, err)
				}
				want := application.ProvisionedRuntime{RuntimeInstanceID: state.ReplacementRuntimeInstanceID, Generation: state.ReplacementGeneration, ContainerID: state.ReplacementContainerID}
				if effects.fencedReplacement != want || !reflect.DeepEqual(journal, []string{"persist:exhausted", "effect:fence_replacement"}) {
					t.Fatalf("expiry failed exact fencing or issued execution: fenced=%+v want=%+v journal=%v", effects.fencedReplacement, want, journal)
				}
			})
		}
	}
}

func (effects *coordinatorEffects) FenceReplacementRuntime(
	_ context.Context,
	state application.RuntimeRecoveryState,
) error {
	*effects.journal = append(*effects.journal, "effect:fence_replacement")
	effects.fencedReplacement = application.ProvisionedRuntime{
		RuntimeInstanceID: state.ReplacementRuntimeInstanceID,
		ContainerID:       state.ReplacementContainerID,
		Generation:        state.ReplacementGeneration,
	}
	return nil
}

func (effects *coordinatorEffects) FenceLateProvisionedRuntime(
	_ context.Context,
	_ application.RuntimeRecoveryState,
	provisioned application.ProvisionedRuntime,
) error {
	*effects.journal = append(*effects.journal, "effect:fence_late_provision")
	effects.fencedReplacement = provisioned
	return nil
}

func (effects *coordinatorEffects) ReattachReplacementRuntime(
	context.Context,
	application.RuntimeRecoveryState,
) (application.RuntimeRecoveryAttachmentEvidence, error) {
	*effects.journal = append(*effects.journal, "effect:reattach")
	return application.RuntimeRecoveryAttachmentEvidence{ContinuityProven: true}, nil
}

func (effects *coordinatorEffects) VerifyReplacementRuntime(
	context.Context,
	application.RuntimeRecoveryState,
) (application.RuntimeRecoveryHealthEvidence, error) {
	*effects.journal = append(*effects.journal, "effect:verify")
	return application.RuntimeRecoveryHealthEvidence{
		Stable: effects.stable,
	}, effects.verifyErr
}

type coordinatorProvisioner struct {
	journal *[]string
}

func (provisioner *coordinatorProvisioner) Provision(
	_ context.Context,
	request application.RuntimeProvisionRequest,
	_ application.RuntimePreStartCheck,
) (application.ProvisionedRuntime, error) {
	*provisioner.journal = append(*provisioner.journal, "effect:provision")
	return application.ProvisionedRuntime{
		RuntimeInstanceID: request.RuntimeInstanceID,
		ContainerID:       "container-2",
		Generation:        request.Generation,
	}, nil
}

func (*coordinatorProvisioner) Inspect(
	context.Context,
	string,
) (application.RuntimeInspection, error) {
	return application.RuntimeInspection{}, nil
}

type coordinatorIDs struct {
	sequence int
}

func (ids *coordinatorIDs) NewID(kind string) (string, error) {
	ids.sequence++
	return fmt.Sprintf("%s-%d", kind, ids.sequence), nil
}

func TestInitialExecutionRecoveryPersistedPhasesDenyNewEffects(t *testing.T) {
	for _, phase := range []domain.RuntimeRecoveryPhase{domain.RecoveryProvisioning, domain.RecoveryReattaching} {
		t.Run(string(phase), func(t *testing.T) {
			var journal []string
			state := coordinatorProvisioningState()
			state.Phase = phase
			got, err := (application.RuntimeRecoveryCoordinator{Recovery: application.RuntimeRecoveryService{Store: &coordinatorRecoveryStore{journal: &journal}, IDs: &coordinatorIDs{}, Now: time.Now}, DeadlineSources: &coordinatorDeadlineSources{journal: &journal}, Effects: &coordinatorEffects{journal: &journal, stable: true}, Provisioner: &coordinatorProvisioner{journal: &journal}}).Reconcile(context.Background(), state)
			if !errors.Is(err, application.CapacityPathNotIntegrated) || len(journal) != 0 || got != state {
				t.Fatalf("persisted replacement acquired effects: phase=%s err=%v journal=%v", phase, err, journal)
			}
		})
	}
}
