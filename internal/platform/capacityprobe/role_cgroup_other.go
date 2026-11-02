//go:build !linux || (!amd64 && !arm64)

package capacityprobe

import "context"

func ObserveRoleCgroup(context.Context, string) (PoolCgroupReadback, error) {
	return PoolCgroupReadback{}, ErrDeploymentUnsupported
}
