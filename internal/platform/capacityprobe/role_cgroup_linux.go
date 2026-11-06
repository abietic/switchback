//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"bytes"
	"context"
	"strings"
)

// ObserveRoleCgroup reuses the native collector's finite read-only cgroup
// primitives without an Engine census, quota read, sample or sequence.
func ObserveRoleCgroup(ctx context.Context, path string) (PoolCgroupReadback, error) {
	var zero PoolCgroupReadback
	if ctx == nil || ctx.Err() != nil {
		return zero, ErrDeploymentCancelled
	}
	root := "/sys/fs/cgroup/switchback.slice/switchback-core.slice"
	if !poolCanonical(path) || (path != root && !strings.HasPrefix(path, root+"/")) {
		return zero, ErrDeploymentInvalid
	}
	k := poolLinux{}
	raw, e := k.mountinfo(ctx)
	if e != nil {
		return zero, e
	}
	r := poolRead{ctx: ctx, k: k}
	defer r.close()
	r.mounts, e = poolMountRecords(raw)
	if e != nil {
		return zero, e
	}
	r.cgroupMount, e = poolCgroupMount(raw)
	if e != nil {
		return zero, e
	}
	g, e := r.group(path)
	if e != nil {
		return zero, e
	}
	if e = r.groupAfter(&g); e != nil {
		return zero, e
	}
	after, e := k.mountinfo(ctx)
	if e != nil || !bytes.Equal(raw, after) {
		return zero, ErrDeploymentInvalid
	}
	return g.value, nil
}
