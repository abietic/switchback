//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"runtime"
)

// ObserveDeploymentDiagnostic observes the invoking OS thread through the fixed
// native backend. Failure context is diagnostic only and grants no authority.
func ObserveDeploymentDiagnostic(ctx context.Context) (DeploymentObservation, *DeploymentFailureObservation) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return observeDeploymentDiagnostic(ctx, deploymentLinux{})
}
