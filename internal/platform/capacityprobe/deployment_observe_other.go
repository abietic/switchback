//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

func ObserveDeployment(ctx context.Context) (DeploymentObservation, error) {
	if ctx == nil {
		return DeploymentObservation{}, ErrDeploymentInvalid
	}
	if ctx.Err() != nil {
		return DeploymentObservation{}, ErrDeploymentCancelled
	}
	return DeploymentObservation{}, ErrDeploymentUnsupported
}
