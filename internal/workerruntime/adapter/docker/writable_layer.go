package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/moby/client"
)

// InfoEngine is separate from Engine so cleanup does not require storage evidence.
type InfoEngine interface {
	Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error)
}

// CheckWritableLayer checks the Engine compatibility prerequisite for the fixed
// writable-layer limit and the Worker seccomp filter, using the same Info result.
// It proves neither quota nor BPF enforcement; container create must still accept
// both mandatory options without an unbounded or unfiltered fallback.
func (p *Provisioner) CheckWritableLayer(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Docker writable-layer enforcement unavailable: %w", err)
	}
	engine, ok := p.engine.(InfoEngine)
	if !ok || isNilInterface(engine) {
		return fmt.Errorf("Docker writable-layer enforcement unavailable: Engine Info capability is required")
	}
	result, err := engine.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("Docker writable-layer enforcement unavailable: inspect Engine Info: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("Docker writable-layer enforcement unavailable: %w", err)
	}
	info := result.Info
	if info.OSType != "linux" || info.Driver != "overlay2" {
		return fmt.Errorf("Docker writable-layer enforcement unavailable: requires classic Linux overlay2 on XFS (os=%q driver=%q)", info.OSType, info.Driver)
	}
	xfs := false
	for _, status := range info.DriverStatus {
		switch strings.ToLower(strings.TrimSpace(status[0])) {
		case "driver-type":
			return fmt.Errorf("Docker writable-layer enforcement unavailable: snapshotter driver-type %q is unproven", status[1])
		case "backing filesystem":
			if status[1] != "xfs" {
				return fmt.Errorf("Docker writable-layer enforcement unavailable: requires XFS backing filesystem (got %q)", status[1])
			}
			xfs = true
		}
	}
	if !xfs {
		return fmt.Errorf("Docker writable-layer enforcement unavailable: XFS backing filesystem evidence is required")
	}
	return checkWorkerSeccompPrerequisites(info)
}
