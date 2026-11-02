package capacityprobe

import "context"

// RoleObservation is a finite current-process readback. It grants no authority.
// Backend deliberately observes only itself; it never opens PID1 namespaces.
type RoleObservation struct {
	BinaryDigest string
	Boot         string
	Process      DeploymentProcessObservation
}

func observeRoleProcess(ctx context.Context, k deploymentBackend) (RoleObservation, error) {
	var out RoleObservation
	if ctx == nil || ctx.Err() != nil {
		return out, ErrDeploymentCancelled
	}
	r := deploymentRead{ctx: ctx, k: k}
	defer r.cleanup()
	raw, e := r.global(deploymentBoot, 64)
	if e != nil {
		return out, e
	}
	out.Boot, e = parseDeploymentBoot(raw)
	if e != nil {
		return out, e
	}
	pid, tid, e := k.ids(ctx)
	if e != nil {
		return out, e
	}
	fd, e := r.acquire(func() (int, error) { return k.openProc(ctx, deploymentCaller) })
	if e != nil {
		return out, e
	}
	meta, e := r.metadata(fd, deploymentProcFS, deploymentDirectory)
	if e != nil {
		return out, e
	}
	out.Process, e = r.process(fd, pid, tid)
	if e != nil {
		return out, e
	}
	fresh, e := r.acquire(func() (int, error) { return k.openProc(ctx, deploymentCaller) })
	if e != nil {
		return out, e
	}
	after, e := r.metadata(fresh, deploymentProcFS, deploymentDirectory)
	if e != nil || after != meta {
		return out, ErrDeploymentInvalid
	}
	p, e := r.process(fresh, pid, tid)
	if e != nil || p != out.Process {
		return out, ErrDeploymentInvalid
	}
	raw, e = r.global(deploymentBoot, 64)
	if e != nil {
		return out, e
	}
	boot, e := parseDeploymentBoot(raw)
	if e != nil || boot != out.Boot {
		return out, ErrDeploymentInvalid
	}
	return out, nil
}
