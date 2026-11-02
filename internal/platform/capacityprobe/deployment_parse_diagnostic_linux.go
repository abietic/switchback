//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"runtime"
)

// ObserveDeploymentParseDiagnostic observes the invoking thread with the fixed
// backend. Optional parse facts are diagnostic only and grant no authority.
func ObserveDeploymentParseDiagnostic(ctx context.Context) (DeploymentObservation, *DeploymentParseFailureObservation) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return observeDeploymentParseDiagnostic(ctx, deploymentLinux{})
}
