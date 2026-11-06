//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

func ObserveRole(ctx context.Context) (RoleObservation, error) {
	return RoleObservation{}, ErrDeploymentUnsupported
}
