package application

import (
	"context"
	"fmt"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

type ObserveRuntimeLossCommand struct {
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	RuntimeInstanceID       string
	ExpectedContainerID     string
	RuntimeGeneration       uint64
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
	IdempotencyKey          string
	ProvenTraceRefs         []string
	ProvenArtifactRefs      []string
}

type RuntimeLossRecord struct {
	Command          ObserveRuntimeLossCommand
	Inspection       RuntimeInspection
	FenceStatus      string
	ExpectedStatus   string
	TaskTransitionID string
	EventID          string
	Now              time.Time
}

type ReconcileUnownedRuntimeCommand struct {
	TenantID                string
	WorkerSessionID         string
	WorkerRunID             string
	AgentTurnID             string
	RuntimeInstanceID       string
	ExpectedContainerID     string
	RuntimeGeneration       uint64
	RuntimeStatus           string
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
	IdempotencyKey          string
}

type UnownedRuntimeReconciler struct {
	Store   RuntimeLossStore
	Cleaner RuntimeCleaner
	Control WorkerControl
	IDs     AllocationIDGenerator
	Now     func() time.Time
}

func (service UnownedRuntimeReconciler) Execute(
	ctx context.Context,
	command ReconcileUnownedRuntimeCommand,
) (RuntimeLossResult, error) {
	if service.Store == nil || service.Cleaner == nil || service.Control == nil ||
		service.IDs == nil || service.Now == nil {
		return RuntimeLossResult{}, fmt.Errorf("unowned Runtime reconciliation dependencies are required")
	}
	if err := validateUnownedRuntimeCommand(command); err != nil {
		return RuntimeLossResult{}, err
	}
	cleanup, err := service.Cleaner.Cleanup(ctx, RuntimeCleanupRequest{
		RuntimeInstanceID:   command.RuntimeInstanceID,
		ExpectedContainerID: command.ExpectedContainerID,
		Generation:          command.RuntimeGeneration,
	})
	if err != nil {
		return RuntimeLossResult{}, fmt.Errorf("cleanup unowned Runtime: %w", err)
	}
	if !cleanup.Confirmed {
		return RuntimeLossResult{}, fmt.Errorf("cleanup unowned Runtime is unconfirmed")
	}
	fenceStatus := "confirmed"
	if err := service.Control.Fence(ctx, command.RuntimeInstanceID, command.RuntimeGeneration, "backend_restarted"); err != nil {
		fenceStatus = "unconfirmed"
	}
	transitionID, err := service.IDs.NewID("task-transition-runtime-loss")
	if err != nil {
		return RuntimeLossResult{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-runtime-loss")
	if err != nil {
		return RuntimeLossResult{}, err
	}
	inspection := RuntimeInspection{
		Exists: cleanup.ContainerExists, ContainerID: cleanup.ContainerRef,
		Generation: command.RuntimeGeneration, ObservedAt: cleanup.ObservedAt,
	}
	state, err := service.Store.RecordRuntimeLoss(ctx, RuntimeLossRecord{
		Command: ObserveRuntimeLossCommand{
			TenantID: command.TenantID, WorkerSessionID: command.WorkerSessionID,
			WorkerRunID: command.WorkerRunID, AgentTurnID: command.AgentTurnID,
			RuntimeInstanceID: command.RuntimeInstanceID, ExpectedContainerID: command.ExpectedContainerID,
			RuntimeGeneration:       command.RuntimeGeneration,
			ExpectedSessionRevision: command.ExpectedSessionRevision,
			ExpectedRunRevision:     command.ExpectedRunRevision,
			ExpectedTurnRevision:    command.ExpectedTurnRevision,
			ExpectedRuntimeRevision: command.ExpectedRuntimeRevision,
			IdempotencyKey:          command.IdempotencyKey,
		},
		Inspection: inspection, FenceStatus: fenceStatus, ExpectedStatus: command.RuntimeStatus,
		TaskTransitionID: transitionID, EventID: eventID, Now: service.Now().UTC(),
	})
	if err != nil {
		return RuntimeLossResult{}, err
	}
	return RuntimeLossResult{Lost: true, FenceStatus: fenceStatus, Inspection: inspection, State: state}, nil
}

func validateUnownedRuntimeCommand(command ReconcileUnownedRuntimeCommand) error {
	if err := validateResidentIdentity(command.TenantID, command.WorkerSessionID, command.WorkerRunID,
		command.AgentTurnID, command.RuntimeInstanceID); err != nil {
		return err
	}
	if command.ExpectedContainerID != "" {
		if err := validateCanonical("expected_container_id", command.ExpectedContainerID); err != nil {
			return err
		}
	}
	if err := validateCanonical("idempotency_key", command.IdempotencyKey); err != nil {
		return err
	}
	if command.RuntimeStatus != "starting" && command.RuntimeStatus != "active" &&
		command.RuntimeStatus != "suspect" && command.RuntimeStatus != "unhealthy" && command.RuntimeStatus != "draining" {
		return fmt.Errorf("Runtime status is not reconcilable")
	}
	if command.RuntimeGeneration == 0 || !allRevisions(command.ExpectedSessionRevision,
		command.ExpectedRunRevision, command.ExpectedTurnRevision, command.ExpectedRuntimeRevision) {
		return fmt.Errorf("unowned Runtime reconciliation requires a positive generation and exact revisions")
	}
	return nil
}

type RuntimeLossState struct {
	WorkerSessionID   string
	WorkerRunID       string
	AgentTurnID       string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	SessionStatus     domain.WorkerSessionStatus
	RunStatus         domain.WorkerRunStatus
	TurnStatus        domain.AgentTurnStatus
	RuntimeStatus     string
	TaskStatus        string
	SessionRevision   uint64
	RunRevision       uint64
	TurnRevision      uint64
	RuntimeRevision   uint64
	TaskRevision      uint64
}

type RuntimeLossResult struct {
	Lost        bool
	FenceStatus string
	Inspection  RuntimeInspection
	State       RuntimeLossState
}

type RuntimeLossStore interface {
	RecordRuntimeLoss(context.Context, RuntimeLossRecord) (RuntimeLossState, error)
}

type RuntimeLossService struct {
	Store       RuntimeLossStore
	Provisioner RuntimeProvisioner
	Control     WorkerControl
	IDs         AllocationIDGenerator
	Now         func() time.Time
}

func (service RuntimeLossService) Observe(
	ctx context.Context,
	command ObserveRuntimeLossCommand,
) (RuntimeLossResult, error) {
	if service.Store == nil || service.Provisioner == nil || service.Control == nil ||
		service.IDs == nil || service.Now == nil {
		return RuntimeLossResult{}, fmt.Errorf("RuntimeLoss dependencies are required")
	}
	if err := validateRuntimeLossCommand(command); err != nil {
		return RuntimeLossResult{}, err
	}
	inspection, err := service.Provisioner.Inspect(ctx, command.RuntimeInstanceID)
	if err != nil {
		return RuntimeLossResult{}, fmt.Errorf("inspect Runtime loss evidence: %w", err)
	}
	if inspection.Exists && inspection.Running &&
		inspection.ContainerID == command.ExpectedContainerID &&
		inspection.Generation == command.RuntimeGeneration {
		return RuntimeLossResult{Lost: false, Inspection: inspection}, nil
	}
	fenceStatus := "confirmed"
	if err := service.Control.Fence(ctx, command.RuntimeInstanceID, command.RuntimeGeneration, "runtime_lost"); err != nil {
		fenceStatus = "unconfirmed"
	}
	transitionID, err := service.IDs.NewID("task-transition-runtime-loss")
	if err != nil {
		return RuntimeLossResult{}, err
	}
	eventID, err := service.IDs.NewID("integration-event-runtime-loss")
	if err != nil {
		return RuntimeLossResult{}, err
	}
	state, err := service.Store.RecordRuntimeLoss(ctx, RuntimeLossRecord{
		Command: command, Inspection: inspection, FenceStatus: fenceStatus,
		TaskTransitionID: transitionID, EventID: eventID, Now: service.Now().UTC(),
	})
	if err != nil {
		return RuntimeLossResult{}, err
	}
	return RuntimeLossResult{
		Lost: true, FenceStatus: fenceStatus, Inspection: inspection, State: state,
	}, nil
}

func validateRuntimeLossCommand(command ObserveRuntimeLossCommand) error {
	if err := validateResidentIdentity(
		command.TenantID, command.WorkerSessionID, command.WorkerRunID,
		command.AgentTurnID, command.RuntimeInstanceID,
	); err != nil {
		return err
	}
	if err := validateCanonical("expected_container_id", command.ExpectedContainerID); err != nil {
		return err
	}
	if err := validateCanonical("idempotency_key", command.IdempotencyKey); err != nil {
		return err
	}
	for _, ref := range append(append([]string(nil), command.ProvenTraceRefs...), command.ProvenArtifactRefs...) {
		if err := validateCanonical("evidence_ref", ref); err != nil {
			return err
		}
	}
	if command.RuntimeGeneration == 0 ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRunRevision,
			command.ExpectedTurnRevision, command.ExpectedRuntimeRevision) {
		return fmt.Errorf("Runtime loss requires a positive generation and exact revisions")
	}
	return nil
}
