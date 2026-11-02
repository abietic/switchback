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

func TestRuntimeRecoveryCoordinatorPersistsEveryIntentBeforeExternalEffect(t *testing.T) {
	var journal []string
	store := &coordinatorRecoveryStore{journal: &journal}
	effects := &coordinatorEffects{journal: &journal, stable: true}
	provisioner := &coordinatorProvisioner{journal: &journal}
	state := coordinatorRecoveryState()
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: store, IDs: &coordinatorIDs{}, Now: time.Now,
		},
		DeadlineSources: &coordinatorDeadlineSources{journal: &journal},
		Effects:         effects,
		Provisioner:     provisioner,
	}).Reconcile(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != domain.RecoveryRecovered || result.AttemptCount != 1 {
		t.Fatalf("result=%+v", result)
	}
	want := []string{
		"persist:fencing",
		"effect:fence",
		"persist:provisioning",
		"effect:prepare",
		"effect:provision",
		"persist:reattaching",
		"effect:reattach",
		"persist:verifying",
		"effect:verify",
		"persist:recovered",
	}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
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

func TestRuntimeRecoveryCoordinatorFencesLateProvisionWithExactContainerAfterDeadline(t *testing.T) {
	var journal []string
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	state := coordinatorProvisioningState()
	state.AgentTurnDeadlineAt = deadline
	effects := &coordinatorEffects{journal: &journal, stable: true}
	deadlineSources := &coordinatorDeadlineSources{
		journal:          &journal,
		acceptedAt:       deadline,
		incidentID:       state.IncidentID,
		incidentRevision: state.IncidentRevision + 1,
	}
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: &coordinatorRecoveryStore{journal: &journal},
			IDs:   &coordinatorIDs{}, Now: func() time.Time { return now },
		},
		DeadlineSources: deadlineSources,
		Effects:         effects,
		Provisioner: &coordinatorProvisioner{
			journal: &journal,
			afterProvision: func() {
				now = deadline
			},
		},
	}).Reconcile(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != domain.RecoveryExhausted ||
		result.OutcomeReason != "preempted_by_backend_deadline" {
		t.Fatalf("result=%+v", result)
	}
	if effects.fencedReplacement.ContainerID != "container-2" ||
		effects.fencedReplacement.RuntimeInstanceID != state.ReplacementRuntimeInstanceID ||
		effects.fencedReplacement.Generation != state.ReplacementGeneration {
		t.Fatalf("fenced replacement=%+v", effects.fencedReplacement)
	}
	if deadlineSources.command.RuntimeInstanceID !=
		state.ReplacementRuntimeInstanceID ||
		deadlineSources.command.RuntimeGeneration !=
			state.ReplacementGeneration ||
		deadlineSources.command.ChannelEpoch !=
			state.ReplacementChannelEpoch {
		t.Fatalf(
			"deadline targeted stale failed Runtime: %+v",
			deadlineSources.command,
		)
	}
	want := []string{
		"effect:prepare",
		"effect:provision",
		"source:backend_deadline",
		"effect:fence_late_provision",
	}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
	}
}

func TestRuntimeRecoveryCoordinatorFencesLateProvisionWhenLifecycleOwnerWonRevision(t *testing.T) {
	var journal []string
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	state := coordinatorProvisioningState()
	state.AgentTurnDeadlineAt = deadline
	claimErr := errors.New("deadline source owner already advanced incident")
	effects := &coordinatorEffects{journal: &journal, stable: true}
	result, err := (application.RuntimeRecoveryCoordinator{
		Recovery: application.RuntimeRecoveryService{
			Store: &coordinatorRecoveryStore{journal: &journal},
			IDs:   &coordinatorIDs{}, Now: func() time.Time { return now },
		},
		DeadlineSources: &coordinatorDeadlineSources{
			journal: &journal,
			err:     claimErr,
		},
		Effects: effects,
		Provisioner: &coordinatorProvisioner{
			journal: &journal,
			afterProvision: func() {
				now = deadline
			},
		},
	}).Reconcile(context.Background(), state)
	if !errors.Is(err, claimErr) {
		t.Fatalf("error=%v", err)
	}
	if result.IncidentID != state.IncidentID || result.Phase != domain.RecoveryProvisioning {
		t.Fatalf("result=%+v", result)
	}
	if effects.fencedReplacement.ContainerID != "container-2" ||
		effects.fencedReplacement.RuntimeInstanceID != state.ReplacementRuntimeInstanceID ||
		effects.fencedReplacement.Generation != state.ReplacementGeneration {
		t.Fatalf("fenced replacement=%+v", effects.fencedReplacement)
	}
	want := []string{
		"effect:prepare",
		"effect:provision",
		"source:backend_deadline",
		"effect:fence_late_provision",
	}
	if !reflect.DeepEqual(journal, want) {
		t.Fatalf("journal=%v want=%v", journal, want)
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
	prepareErr        error
	afterPrepare      func()
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
	if effects.afterPrepare != nil {
		effects.afterPrepare()
	}
	if effects.prepareErr != nil {
		return application.RuntimeProvisionRequest{}, effects.prepareErr
	}
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

func TestRuntimeRecoveryCoordinatorPreparationRetryHonorsDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"temporary", "permanent", "cancelled", "deadline", "absolute-expiry"} {
		t.Run(mode, func(t *testing.T) {
			var journal []string
			now := time.Now().UTC()
			state := coordinatorProvisioningState()
			state.AgentTurnDeadlineAt = now.Add(time.Minute)
			state.SessionAbsoluteExpiresAt = now.Add(time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			effects := &coordinatorEffects{journal: &journal, prepareErr: application.ErrRuntimePreparationRetryable}
			source := &coordinatorDeadlineSources{journal: &journal, acceptedAt: state.AgentTurnDeadlineAt, incidentID: state.IncidentID, incidentRevision: state.IncidentRevision + 1}
			switch mode {
			case "permanent":
				effects.prepareErr = errors.New("identity rejected")
			case "cancelled":
				effects.afterPrepare = cancel
			case "deadline":
				effects.afterPrepare = func() { now = state.AgentTurnDeadlineAt }
			case "absolute-expiry":
				effects.afterPrepare = func() { now = state.SessionAbsoluteExpiresAt }
			}
			result, err := (application.RuntimeRecoveryCoordinator{Recovery: application.RuntimeRecoveryService{Store: &coordinatorRecoveryStore{journal: &journal}, IDs: &coordinatorIDs{}, Now: func() time.Time { return now }}, DeadlineSources: source, Effects: effects, Provisioner: &coordinatorProvisioner{journal: &journal}}).Reconcile(ctx, state)
			for _, event := range journal {
				if event == "effect:provision" {
					t.Fatal("preparation failure provisioned a Runtime")
				}
			}
			if mode == "temporary" || mode == "cancelled" {
				if !errors.Is(err, application.ErrRuntimePreparationRetryable) || !reflect.DeepEqual(result, state) {
					t.Fatalf("preparation retry changed original owner: result=%+v err=%v", result, err)
				}
			} else {
				if result.Phase != domain.RecoveryExhausted {
					t.Fatalf("failed to honor terminal decision: phase=%s err=%v", result.Phase, err)
				}
				if mode == "deadline" && (result.OutcomeReason != "preempted_by_backend_deadline" || !source.command.DeadlineAt.Equal(state.AgentTurnDeadlineAt)) {
					t.Fatal("retry changed deadline authority")
				}
				if mode == "absolute-expiry" && result.OutcomeReason != "worker_session_absolute_expired" {
					t.Fatal("retry bypassed absolute expiry")
				}
			}
		})
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
	journal        *[]string
	afterProvision func()
}

func (provisioner *coordinatorProvisioner) Provision(
	_ context.Context,
	request application.RuntimeProvisionRequest,
) (application.ProvisionedRuntime, error) {
	*provisioner.journal = append(*provisioner.journal, "effect:provision")
	if provisioner.afterProvision != nil {
		provisioner.afterProvision()
	}
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
