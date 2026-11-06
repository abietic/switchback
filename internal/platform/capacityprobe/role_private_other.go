//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

func ObserveRolePrivateDirectories(context.Context) ([]RolePrivateDirectory, error) {
	return nil, ErrDeploymentUnsupported
}
