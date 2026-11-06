//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"crypto/sha256"
	"fmt"
	"golang.org/x/sys/unix"
	"runtime"
)

// ObserveRole reads only the invoking thread and its loaded executable. The
// proc magic link is intentional; the referent is checked and reopened.
func ObserveRole(ctx context.Context) (RoleObservation, error) {
	var zero RoleObservation
	if ctx == nil || ctx.Err() != nil {
		return zero, ErrDeploymentCancelled
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before, e := observeRoleProcess(ctx, deploymentLinux{})
	if e != nil {
		return zero, e
	}
	digest, e := roleExecutable(ctx)
	if e != nil {
		return zero, e
	}
	after, e := observeRoleProcess(ctx, deploymentLinux{})
	if e != nil || before != after {
		return zero, ErrDeploymentInvalid
	}
	before.BinaryDigest = digest
	return before, nil
}
func roleExecutable(ctx context.Context) (string, error) {
	fd := -1
	e := deploymentNativeCall(ctx, func() error {
		var err error
		fd, err = unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		return err
	})
	if fd >= 0 {
		defer unix.Close(fd)
	}
	if e != nil {
		return "", e
	}
	var before, after, reopened unix.Stat_t
	if e = deploymentNativeCall(ctx, func() error { return unix.Fstat(fd, &before) }); e != nil {
		return "", e
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 || before.Size > 256<<20 || before.Uid != 0 || before.Mode&0022 != 0 {
		return "", ErrDeploymentInvalid
	}
	h := sha256.New()
	buf := make([]byte, 65536)
	var total int64
	for {
		n := 0
		e = deploymentNativeCall(ctx, func() error { var err error; n, err = unix.Read(fd, buf); return err })
		if e != nil {
			return "", e
		}
		if n == 0 {
			break
		}
		total += int64(n)
		if total > before.Size {
			return "", ErrDeploymentInvalid
		}
		h.Write(buf[:n])
	}
	if e = deploymentNativeCall(ctx, func() error { return unix.Fstat(fd, &after) }); e != nil {
		return "", e
	}
	next := -1
	e = deploymentNativeCall(ctx, func() error {
		var err error
		next, err = unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		return err
	})
	if next >= 0 {
		defer unix.Close(next)
	}
	if e != nil {
		return "", e
	}
	if e = deploymentNativeCall(ctx, func() error { return unix.Fstat(next, &reopened) }); e != nil {
		return "", e
	}
	// atime can change due to our read; every identity/write timestamp must agree.
	before.Atim = after.Atim
	reopened.Atim = after.Atim
	if total != before.Size || before != after || reopened != after {
		return "", ErrDeploymentInvalid
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}
