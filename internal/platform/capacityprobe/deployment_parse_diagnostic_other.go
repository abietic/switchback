//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

// ObserveDeploymentParseDiagnostic reports unsupported after context validation.
func ObserveDeploymentParseDiagnostic(ctx context.Context) (DeploymentObservation, *DeploymentParseFailureObservation) {
	if ctx == nil {
		return DeploymentObservation{}, deploymentParseFailure(deploymentDiagnosticFailure("entry", "invalid"))
	}
	if ctx.Err() != nil {
		return DeploymentObservation{}, deploymentParseFailure(deploymentDiagnosticFailure("observe", "cancelled"))
	}
	return DeploymentObservation{}, deploymentParseFailure(deploymentDiagnosticFailure("observe", "unsupported"))
}
