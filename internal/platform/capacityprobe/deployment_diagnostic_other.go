//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

// ObserveDeploymentDiagnostic reports unsupported after validating the context.
func ObserveDeploymentDiagnostic(ctx context.Context) (DeploymentObservation, *DeploymentFailureObservation) {
	if ctx == nil {
		return DeploymentObservation{}, deploymentDiagnosticFailure("entry", "invalid")
	}
	if ctx.Err() != nil {
		return DeploymentObservation{}, deploymentDiagnosticFailure("observe", "cancelled")
	}
	return DeploymentObservation{}, deploymentDiagnosticFailure("observe", "unsupported")
}
