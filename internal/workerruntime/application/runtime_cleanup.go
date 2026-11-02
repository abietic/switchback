package application

import (
	"context"
	"fmt"
	"time"
)

type BeginRuntimeCleanupCommand struct {
	TenantID                string
	WorkerSessionID         string
	RuntimeInstanceID       string
	ExpectedContainerID     string
	RuntimeGeneration       uint64
	ExpectedSessionRevision uint64
	ExpectedRuntimeRevision uint64
	IdempotencyKey          string
}

type BeginRuntimeCleanupRecord struct {
	Command            BeginRuntimeCleanupCommand
	ScheduledCommandID string
	Now                time.Time
	RetryAt            time.Time
}

type RuntimeCleanupLease struct {
	TenantID                string `json:"tenant_id"`
	WorkerSessionID         string `json:"worker_session_id"`
	RuntimeInstanceID       string `json:"runtime_instance_id"`
	ExpectedContainerID     string `json:"expected_container_id"`
	RuntimeGeneration       uint64 `json:"runtime_generation"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision"`
	ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision"`
	ScheduledCommandID      string `json:"scheduled_command_id"`
	IdempotencyKey          string `json:"idempotency_key"`
}

type FinishRuntimeCleanupRecord struct {
	Lease       RuntimeCleanupLease
	Observation RuntimeCleanupObservation
	FenceStatus string
	ErrorCode   string
	Now         time.Time
	RetryAt     time.Time
}

type RuntimeCleanupResult struct {
	Confirmed      bool
	ErrorCode      string
	Observation    RuntimeCleanupObservation
	Reconciliation RuntimeCleanupLease
}

type RuntimeCleanupStore interface {
	BeginRuntimeCleanup(context.Context, BeginRuntimeCleanupRecord) (RuntimeCleanupLease, error)
	FinishRuntimeCleanup(context.Context, FinishRuntimeCleanupRecord) (RuntimeCleanupResult, error)
}

type RuntimeCleanupService struct {
	Store      RuntimeCleanupStore
	Cleaner    RuntimeCleaner
	Control    WorkerControl
	IDs        AllocationIDGenerator
	Now        func() time.Time
	RetryDelay time.Duration
}

func (service RuntimeCleanupService) Cleanup(
	ctx context.Context,
	command BeginRuntimeCleanupCommand,
) (RuntimeCleanupResult, error) {
	if err := service.validateDependencies(); err != nil {
		return RuntimeCleanupResult{}, err
	}
	if err := validateBeginCleanup(command); err != nil {
		return RuntimeCleanupResult{}, err
	}
	commandID, err := service.IDs.NewID("scheduled-command-runtime-cleanup")
	if err != nil {
		return RuntimeCleanupResult{}, err
	}
	now := service.Now().UTC()
	lease, err := service.Store.BeginRuntimeCleanup(ctx, BeginRuntimeCleanupRecord{
		Command: command, ScheduledCommandID: commandID,
		Now: now, RetryAt: now.Add(service.RetryDelay),
	})
	if err != nil {
		return RuntimeCleanupResult{}, err
	}
	return service.reconcile(ctx, lease)
}

func (service RuntimeCleanupService) Reconcile(
	ctx context.Context,
	lease RuntimeCleanupLease,
) (RuntimeCleanupResult, error) {
	if err := service.validateDependencies(); err != nil {
		return RuntimeCleanupResult{}, err
	}
	if err := validateCleanupLease(lease); err != nil {
		return RuntimeCleanupResult{}, err
	}
	return service.reconcile(ctx, lease)
}

func (service RuntimeCleanupService) reconcile(
	ctx context.Context,
	lease RuntimeCleanupLease,
) (RuntimeCleanupResult, error) {
	fenceStatus := "confirmed"
	if err := service.Control.Fence(ctx, lease.RuntimeInstanceID, lease.RuntimeGeneration, "runtime_cleanup"); err != nil {
		fenceStatus = "unconfirmed"
	}
	observation, cleanupErr := service.Cleaner.Cleanup(ctx, RuntimeCleanupRequest{
		RuntimeInstanceID:   lease.RuntimeInstanceID,
		ExpectedContainerID: lease.ExpectedContainerID,
		Generation:          lease.RuntimeGeneration,
	})
	errorCode := ""
	if fenceStatus != "confirmed" {
		errorCode = "runtime_cleanup_fence_unconfirmed"
	}
	if !observation.Confirmed || cleanupErr != nil {
		errorCode = "runtime_cleanup_unconfirmed"
		if observation.ContainerExists || observation.SecretVolumeExists {
			errorCode = "runtime_cleanup_residue"
		}
	}
	now := service.Now().UTC()
	return service.Store.FinishRuntimeCleanup(ctx, FinishRuntimeCleanupRecord{
		Lease: lease, Observation: observation, FenceStatus: fenceStatus,
		ErrorCode: errorCode, Now: now, RetryAt: now.Add(service.RetryDelay),
	})
}

func (service RuntimeCleanupService) validateDependencies() error {
	if service.Store == nil || service.Cleaner == nil || service.Control == nil ||
		service.IDs == nil || service.Now == nil || service.RetryDelay <= 0 {
		return fmt.Errorf("RuntimeCleanup dependencies are required")
	}
	return nil
}

func validateBeginCleanup(command BeginRuntimeCleanupCommand) error {
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "worker_session_id": command.WorkerSessionID,
		"runtime_instance_id": command.RuntimeInstanceID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return err
		}
	}
	if command.ExpectedContainerID != "" {
		if err := validateCanonical("expected_container_id", command.ExpectedContainerID); err != nil {
			return err
		}
	}
	if err := validateCanonical("idempotency_key", command.IdempotencyKey); err != nil {
		return err
	}
	if command.RuntimeGeneration == 0 ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRuntimeRevision) {
		return fmt.Errorf("Runtime cleanup requires a positive generation and exact revisions")
	}
	return nil
}

func validateCleanupLease(lease RuntimeCleanupLease) error {
	return validateBeginCleanup(BeginRuntimeCleanupCommand{
		TenantID: lease.TenantID, WorkerSessionID: lease.WorkerSessionID,
		RuntimeInstanceID:       lease.RuntimeInstanceID,
		ExpectedContainerID:     lease.ExpectedContainerID,
		RuntimeGeneration:       lease.RuntimeGeneration,
		ExpectedSessionRevision: lease.ExpectedSessionRevision,
		ExpectedRuntimeRevision: lease.ExpectedRuntimeRevision,
		IdempotencyKey:          lease.IdempotencyKey,
	})
}
