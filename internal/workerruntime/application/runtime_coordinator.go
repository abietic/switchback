package application

import (
	"context"
	"fmt"
)

type RuntimePreparationRequest struct {
	Ownership StartedWorkerRun
	Command   StartWorkerRunCommand
}

type RuntimeEnvironmentPreparer interface {
	PrepareRuntime(context.Context, RuntimePreparationRequest) (RuntimeProvisionRequest, error)
}

type StartRuntimeCommand struct {
	Run StartWorkerRunCommand
}

type StartedRuntime struct {
	Ownership StartedWorkerRun
	Runtime   ProvisionedRuntime
}

// RuntimeCoordinator keeps irreversible environment and Docker work behind the
// transaction that establishes the Runtime, Run, AgentSession, and AgentTurn owners.
type RuntimeCoordinator struct {
	Starts      StartWorkerRunService
	Environment RuntimeEnvironmentPreparer
	Provisioner RuntimeProvisioner
}

func (coordinator RuntimeCoordinator) Start(
	ctx context.Context,
	command StartRuntimeCommand,
) (StartedRuntime, error) {
	if coordinator.Environment == nil || coordinator.Provisioner == nil {
		return StartedRuntime{}, fmt.Errorf("RuntimeCoordinator dependencies are required")
	}
	ownership, err := coordinator.Starts.Execute(ctx, command.Run)
	if err != nil {
		return StartedRuntime{}, err
	}
	request, err := coordinator.Environment.PrepareRuntime(ctx, RuntimePreparationRequest{
		Ownership: ownership,
		Command:   command.Run,
	})
	if err != nil {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("prepare committed Runtime environment: %w", err)
	}
	if err := validateProvisionIdentity(ownership, command.Run, request); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	runtime, err := coordinator.Provisioner.Provision(ctx, request)
	if err != nil {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("provision committed Runtime: %w", err)
	}
	if runtime.RuntimeInstanceID != ownership.RuntimeInstanceID ||
		runtime.Generation != ownership.RuntimeGeneration {
		return StartedRuntime{Ownership: ownership, Runtime: runtime},
			fmt.Errorf("provisioned Runtime identity does not match committed ownership")
	}
	return StartedRuntime{Ownership: ownership, Runtime: runtime}, nil
}

func validateProvisionIdentity(
	ownership StartedWorkerRun,
	command StartWorkerRunCommand,
	request RuntimeProvisionRequest,
) error {
	if request.WorkerSessionID != ownership.WorkerSessionID ||
		request.WorkerRunID != ownership.WorkerRunID ||
		request.RuntimeInstanceID != ownership.RuntimeInstanceID ||
		request.Generation != ownership.RuntimeGeneration {
		return fmt.Errorf("prepared Runtime identity does not match committed ownership")
	}
	if request.NetworkProfile.ID != command.NetworkPolicyID ||
		request.NetworkProfile.Revision != command.NetworkPolicyRevision ||
		request.NetworkProfile.Digest != command.NetworkPolicyDigest ||
		request.NetworkEnforcementStatus != command.NetworkEnforcementStatus {
		return fmt.Errorf("prepared Runtime network evidence does not match committed ownership")
	}
	return nil
}
