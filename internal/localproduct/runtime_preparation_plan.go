package localproduct

import (
	"context"
	"fmt"
	"sync"

	"github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type runtimePreparationProfiles struct {
	agent    config.AgentProfile
	resource config.RuntimeResourceProfile
	network  config.RuntimeNetworkEvidence
}

func (environment *runtimeEnvironment) preparationProfiles(request workerapp.RuntimePreparationRequest) (runtimePreparationProfiles, error) {
	if environment == nil || environment.hub == nil {
		return runtimePreparationProfiles{}, fmt.Errorf("Runtime environment is unavailable")
	}
	profile, ok := environment.profiles.Agent(request.Command.AgentProfileID)
	if !ok || profile.Digest != request.Command.AgentProfileDigest {
		return runtimePreparationProfiles{}, fmt.Errorf("AgentProfile is unavailable or stale")
	}
	resource, ok := environment.profiles.RuntimeResource(request.Command.ResourceProfileID)
	if !ok {
		return runtimePreparationProfiles{}, fmt.Errorf("Runtime resource profile is unavailable")
	}
	network, err := environment.networkProfiles.ResolveStored(config.RuntimeNetworkEvidence{
		ID: request.Command.NetworkPolicyID, Revision: request.Command.NetworkPolicyRevision,
		Digest:            request.Command.NetworkPolicyDigest,
		EnforcementStatus: request.Command.NetworkEnforcementStatus,
	})
	if err != nil || network.EnforcementStatus != request.Command.NetworkEnforcementStatus {
		return runtimePreparationProfiles{}, fmt.Errorf("Runtime network profile is unavailable or stale")
	}

	return runtimePreparationProfiles{agent: profile, resource: resource, network: network}, nil
}

// A plan owns its rooted descriptor and immutable source binding. There is no
// plan registry or reconstruction after the execution owner authorizes it.
type runtimePreparationPlan struct {
	mu                sync.Mutex
	environment       *runtimeEnvironment
	request           workerapp.RuntimePreparationRequest
	source            *workspaceSourcePlan
	binding           workerapp.RuntimeSourcePlanBinding
	profiles          runtimePreparationProfiles
	attempted, closed bool
	closeErr          error
}

func (environment *runtimeEnvironment) PlanRuntime(ctx context.Context, request workerapp.RuntimePreparationRequest) (workerapp.RuntimePreparationPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	profiles, err := environment.preparationProfiles(request)
	if err != nil {
		return nil, err
	}
	source, err := planWorkspaceSource(ctx, environment.configuration.WorkspaceSourceRoot)
	if err != nil {
		return nil, err
	}
	binding, err := source.Binding()
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	if request.SourcePlan != (workerapp.RuntimeSourcePlanBinding{}) && request.SourcePlan != binding {
		_ = source.Close()
		return nil, fmt.Errorf("Runtime preparation source binding mismatch")
	}
	request.SourcePlan = binding
	return &runtimePreparationPlan{
		environment: environment, request: request, source: source,
		binding: binding, profiles: profiles,
	}, nil
}
func (plan *runtimePreparationPlan) SourceBinding() workerapp.RuntimeSourcePlanBinding {
	return plan.binding
}
func (plan *runtimePreparationPlan) Prepare(ctx context.Context) (workerapp.RuntimeProvisionRequest, error) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if plan.closed || plan.attempted {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("Runtime preparation plan is closed or already attempted")
	}
	plan.attempted = true
	if err := ctx.Err(); err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	if err := plan.source.validate(ctx); err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	return plan.environment.preparePlannedRuntime(ctx, plan.request, plan.source, plan.binding, plan.profiles)
}
func (plan *runtimePreparationPlan) Close() error {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	if !plan.closed {
		plan.closed = true
		plan.closeErr = plan.source.Close()
	}
	return plan.closeErr
}
