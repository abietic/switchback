package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

type RuntimePreparationRequest struct {
	Ownership  StartedWorkerRun
	Command    StartWorkerRunCommand
	SourcePlan RuntimeSourcePlanBinding
}

// RuntimePreparationPlan owns readonly input facts and the source handle used
// for one preparation attempt. Close releases that handle on every outcome.
type RuntimePreparationPlan interface {
	SourceBinding() RuntimeSourcePlanBinding
	Prepare(context.Context) (RuntimeProvisionRequest, error)
	Close() error
}

type RuntimeEnvironmentPreparer interface {
	PlanRuntime(context.Context, RuntimePreparationRequest) (RuntimePreparationPlan, error)
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
	Authorizer          InitialRuntimeExecutionAuthorizer
	CreatedLayerChecker InitialCreatedLayerChecker
	Starts              StartWorkerRunService
	Environment         RuntimeEnvironmentPreparer
	Provisioner         RuntimeProvisioner
}

func (coordinator RuntimeCoordinator) Start(
	ctx context.Context,
	command StartRuntimeCommand,
) (result StartedRuntime, resultErr error) {
	if coordinator.Authorizer == nil || coordinator.Environment == nil || coordinator.Provisioner == nil {
		return StartedRuntime{}, fmt.Errorf("RuntimeCoordinator dependencies are required")
	}
	ownership, err := coordinator.Starts.Execute(ctx, command.Run)
	if err != nil {
		return StartedRuntime{}, err
	}
	if !ownership.Created {
		return StartedRuntime{Ownership: ownership}, InitialRuntimeStartReplayPending
	}
	// Historical ownership is a pending replay, never another physical attempt.
	// A missing (including typed-nil) physical reader denies before preparation.
	if missingCreatedLayerChecker(coordinator.CreatedLayerChecker) {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("initial created-layer checker is required")
	}
	preparation := RuntimePreparationRequest{Ownership: ownership, Command: command.Run}
	plan, err := coordinator.Environment.PlanRuntime(ctx, preparation)
	if !nilRuntimeDependency(plan) {
		defer func() {
			if closeErr := plan.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, closeErr)
			}
		}()
	}
	if err != nil {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("plan committed Runtime environment: %w", err)
	}
	if nilRuntimeDependency(plan) {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("Runtime preparation plan is required")
	}
	preparation.SourcePlan = plan.SourceBinding()
	legacy, err := preparation.SourcePlan.LegacyFileDigest()
	if err != nil {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("invalid Runtime source plan: %w", err)
	}
	var snapshot struct {
		Digest string `json:"working_copy_checkpoint_digest"`
	}
	if json.Unmarshal(command.Run.StartSnapshot, &snapshot) != nil || snapshot.Digest != "sha256:"+legacy {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("Runtime source plan does not match committed start snapshot")
	}
	if err := ctx.Err(); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	if err := coordinator.Authorizer.AuthorizeInitialPreparation(ctx, preparation); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	if err := ctx.Err(); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	request, err := plan.Prepare(ctx)
	if err != nil {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("prepare committed Runtime environment: %w", err)
	}
	if request.SourcePlanDigest != preparation.SourcePlan.Digest {
		return StartedRuntime{Ownership: ownership}, fmt.Errorf("prepared Runtime source plan digest does not match authorized plan")
	}
	if err := validateProvisionIdentity(ownership, command.Run, request); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	if err := ctx.Err(); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	if err := coordinator.Authorizer.AuthorizeInitialProvision(ctx, preparation, request); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	if err := ctx.Err(); err != nil {
		return StartedRuntime{Ownership: ownership}, err
	}
	runtime, err := coordinator.Provisioner.Provision(ctx, request, func(checkCtx context.Context, identity CreatedRuntimeIdentity) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if identity.ContainerID == "" || identity.RuntimeInstanceID != request.RuntimeInstanceID ||
			identity.WorkerSessionID != request.WorkerSessionID || identity.Generation != request.Generation {
			return fmt.Errorf("created Runtime identity does not match committed ownership")
		}
		// Physical I/O finishes before the existing authorizer opens its transaction.
		if err := coordinator.CreatedLayerChecker.CheckCreatedLayer(checkCtx, request, identity); err != nil {
			return err
		}
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if err := coordinator.Authorizer.AuthorizeInitialProvision(checkCtx, preparation, request); err != nil {
			return err
		}
		return checkCtx.Err()
	})
	if err != nil {
		return StartedRuntime{Ownership: ownership, Runtime: runtime}, fmt.Errorf("provision committed Runtime: %w", err)
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

func missingCreatedLayerChecker(checker InitialCreatedLayerChecker) bool {
	return nilRuntimeDependency(checker)
}

func nilRuntimeDependency(checker any) bool {
	if checker == nil {
		return true
	}
	value := reflect.ValueOf(checker)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
