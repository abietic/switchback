//go:build integration

package localproduct

import (
	"context"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// Only explicit legacy fake-runtime fixtures use these signed synthetic facts.
// Real PG permit/consume/activation owners still validate the exact identity.
func publishSyntheticFixtureContainer(ctx context.Context, runner *runtimeRunner, request workerapp.RuntimeProvisionRequest, container string) error {
	if runner == nil || request.SourcePlanDigest == "" {
		return nil
	}
	policy, ok := runner.startPolicy.(*syntheticRuntimeStartPolicy)
	if !ok || policy.inventory == nil {
		return nil
	}
	policy.inventory.Observe(request.WorkerSessionID, request.RuntimeInstanceID, container, request.Generation)
	// Publish physical facts before the pre-start callback. Reconciliation is
	// owned by activation; doing it here would bind the resident container before
	// the still-pre-Start authorizer has completed its final check.
	return ctx.Err()
}
