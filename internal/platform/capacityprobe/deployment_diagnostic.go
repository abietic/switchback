package capacityprobe

import (
	"context"
	"errors"
	"syscall"
	"time"
)

func deploymentDiagnosticFailure(stage, class string) *DeploymentFailureObservation {
	return &DeploymentFailureObservation{Schema: DeploymentFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class}
}

func observeDeploymentDiagnostic(ctx context.Context, backend deploymentBackend) (DeploymentObservation, *DeploymentFailureObservation) {
	if ctx == nil || backend == nil {
		return DeploymentObservation{}, deploymentDiagnosticFailure("entry", "invalid")
	}
	if ctx.Err() != nil {
		return DeploymentObservation{}, deploymentDiagnosticFailure("observe", "cancelled")
	}
	traced := &deploymentDiagnosticBackend{backend: backend, targets: make(map[int]string)}
	out, err := observeDeployment(ctx, traced)
	if err == nil {
		return out, nil
	}
	class := "unavailable"
	switch err {
	case ErrDeploymentInvalid:
		class = "invalid"
	case ErrDeploymentCancelled:
		class = "cancelled"
	case ErrDeploymentUnsupported:
		class = "unsupported"
	}
	failure := deploymentDiagnosticFailure("observe", class)
	// Own the final context independently of the decorator, including its errno.
	if traced.last != nil {
		last := *traced.last
		if last.Errno != nil {
			errno := *last.Errno
			last.Errno = &errno
		}
		failure.LastPrimitive = &last
	}
	return DeploymentObservation{}, failure
}

type deploymentDiagnosticBackend struct {
	backend deploymentBackend
	targets map[int]string
	last    *DeploymentPrimitiveObservation
}

func (b *deploymentDiagnosticBackend) record(operation, target string, err error) {
	p := &DeploymentPrimitiveObservation{Operation: operation, Target: target, Outcome: "returned"}
	if err != nil {
		p.Outcome = "failed"
		var errno syscall.Errno
		if errors.As(err, &errno) && errno >= 1 && errno <= 4095 {
			n := uint32(errno)
			p.Errno = &n
		}
	}
	b.last = p
}
func (b *deploymentDiagnosticBackend) target(fd int) string {
	if target, ok := b.targets[fd]; ok {
		return target
	}
	return "unknown"
}
func (b *deploymentDiagnosticBackend) acquired(fd int, target string) {
	if fd >= 0 {
		if b.targets == nil {
			b.targets = make(map[int]string)
		}
		b.targets[fd] = target
	}
}
func deploymentDiagnosticOwner(o deploymentOwner) string {
	switch o {
	case deploymentCaller:
		return "caller"
	case deploymentPID1:
		return "pid1"
	}
	return "unknown"
}
func deploymentDiagnosticGlobal(g deploymentGlobal) string {
	switch g {
	case deploymentBoot:
		return "boot"
	case deploymentLastCap:
		return "cap_last_cap"
	}
	return "unknown"
}
func deploymentDiagnosticLeaf(parent string, l deploymentLeaf) string {
	if parent != "caller" && parent != "pid1" {
		return "unknown"
	}
	switch l {
	case deploymentStatus:
		return parent + ".status"
	case deploymentStat:
		return parent + ".stat"
	case deploymentCgroup:
		return parent + ".cgroup"
	}
	return "unknown"
}
func deploymentDiagnosticNamespace(parent string, n deploymentNS) string {
	if parent != "caller" && parent != "pid1" {
		return "unknown"
	}
	switch n {
	case deploymentUserNS:
		return parent + ".ns.user"
	case deploymentPIDNS:
		return parent + ".ns.pid"
	case deploymentMountNS:
		return parent + ".ns.mnt"
	case deploymentCgroupNS:
		return parent + ".ns.cgroup"
	}
	return "unknown"
}
func (b *deploymentDiagnosticBackend) now(ctx context.Context) (time.Time, error) {
	v, e := b.backend.now(ctx)
	b.record("now", "clock", e)
	return v, e
}
func (b *deploymentDiagnosticBackend) ids(ctx context.Context) (uint64, uint64, error) {
	p, t, e := b.backend.ids(ctx)
	b.record("ids", "identity", e)
	return p, t, e
}
func (b *deploymentDiagnosticBackend) kernel(ctx context.Context) (string, error) {
	v, e := b.backend.kernel(ctx)
	b.record("kernel", "kernel", e)
	return v, e
}
func (b *deploymentDiagnosticBackend) openGlobal(ctx context.Context, g deploymentGlobal) (int, error) {
	fd, e := b.backend.openGlobal(ctx, g)
	target := deploymentDiagnosticGlobal(g)
	b.acquired(fd, target)
	b.record("open_global", target, e)
	return fd, e
}
func (b *deploymentDiagnosticBackend) openProc(ctx context.Context, o deploymentOwner) (int, error) {
	fd, e := b.backend.openProc(ctx, o)
	target := deploymentDiagnosticOwner(o)
	b.acquired(fd, target)
	b.record("open_proc", target, e)
	return fd, e
}
func (b *deploymentDiagnosticBackend) openLeaf(ctx context.Context, parent int, l deploymentLeaf) (int, error) {
	fd, e := b.backend.openLeaf(ctx, parent, l)
	target := deploymentDiagnosticLeaf(b.target(parent), l)
	b.acquired(fd, target)
	b.record("open_leaf", target, e)
	return fd, e
}
func (b *deploymentDiagnosticBackend) openNamespace(ctx context.Context, parent int, n deploymentNS) (int, error) {
	fd, e := b.backend.openNamespace(ctx, parent, n)
	target := deploymentDiagnosticNamespace(b.target(parent), n)
	b.acquired(fd, target)
	b.record("open_namespace", target, e)
	return fd, e
}
func (b *deploymentDiagnosticBackend) namespaceStat(ctx context.Context, parent int, n deploymentNS) (deploymentMetadata, error) {
	v, e := b.backend.namespaceStat(ctx, parent, n)
	b.record("namespace_stat", deploymentDiagnosticNamespace(b.target(parent), n), e)
	return v, e
}
func (b *deploymentDiagnosticBackend) stat(ctx context.Context, fd int) (deploymentMetadata, error) {
	v, e := b.backend.stat(ctx, fd)
	b.record("stat", b.target(fd), e)
	return v, e
}
func (b *deploymentDiagnosticBackend) statfs(ctx context.Context, fd int) (uint64, error) {
	v, e := b.backend.statfs(ctx, fd)
	b.record("statfs", b.target(fd), e)
	return v, e
}
func (b *deploymentDiagnosticBackend) read(ctx context.Context, fd, limit int) ([]byte, error) {
	v, e := b.backend.read(ctx, fd, limit)
	b.record("read", b.target(fd), e)
	return v, e
}
func (b *deploymentDiagnosticBackend) close(fd int) {
	b.backend.close(fd)
	delete(b.targets, fd)
}
