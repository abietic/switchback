//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// ObserveDeployment observes the invoking OS thread and fixed procfs PID1 view.
// It never verifies a deployment or grants admission authority.
func ObserveDeployment(ctx context.Context) (DeploymentObservation, error) {
	if ctx == nil {
		return DeploymentObservation{}, ErrDeploymentInvalid
	}
	if ctx.Err() != nil {
		return DeploymentObservation{}, ErrDeploymentCancelled
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// observeDeployment cleans every FD before this thread can be unlocked.
	return observeDeployment(ctx, deploymentLinux{})
}

type deploymentLinux struct{}

func deploymentNativeCall(ctx context.Context, f func() error) error {
	if ctx == nil {
		return ErrDeploymentInvalid
	}
	if ctx.Err() != nil {
		return ErrDeploymentCancelled
	}
	e := f()
	if ctx.Err() != nil {
		return ErrDeploymentCancelled
	}
	return e
}
func (deploymentLinux) now(ctx context.Context) (v time.Time, e error) {
	e = deploymentNativeCall(ctx, func() error { v = time.Now(); return nil })
	return
}
func (deploymentLinux) ids(ctx context.Context) (pid, tid uint64, e error) {
	e = deploymentNativeCall(ctx, func() error { pid = uint64(unix.Getpid()); return nil })
	if e != nil {
		return
	}
	e = deploymentNativeCall(ctx, func() error { tid = uint64(unix.Gettid()); return nil })
	return
}
func (deploymentLinux) kernel(ctx context.Context) (v string, e error) {
	var u unix.Utsname
	e = deploymentNativeCall(ctx, func() error { return unix.Uname(&u) })
	if e == nil {
		v = unix.ByteSliceToString(u.Release[:])
	}
	return
}
func (deploymentLinux) openGlobal(ctx context.Context, g deploymentGlobal) (fd int, e error) {
	fd = -1
	var path string
	switch g {
	case deploymentBoot:
		path = "/proc/sys/kernel/random/boot_id"
	case deploymentLastCap:
		path = "/proc/sys/kernel/cap_last_cap"
	default:
		return fd, ErrDeploymentInvalid
	}
	e = deploymentNativeCall(ctx, func() error {
		var err error
		fd, err = unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		return err
	})
	return
}
func (deploymentLinux) openProc(ctx context.Context, o deploymentOwner) (fd int, e error) {
	fd = -1
	var path string
	switch o {
	case deploymentCaller:
		path = "/proc/thread-self"
	case deploymentPID1:
		path = "/proc/1"
	default:
		return fd, ErrDeploymentInvalid
	}
	e = deploymentNativeCall(ctx, func() error {
		var err error
		fd, err = unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		return err
	})
	return
}
func (deploymentLinux) openLeaf(ctx context.Context, parent int, l deploymentLeaf) (fd int, e error) {
	fd = -1
	var path string
	switch l {
	case deploymentStatus:
		path = "status"
	case deploymentStat:
		path = "stat"
	case deploymentCgroup:
		path = "cgroup"
	default:
		return fd, ErrDeploymentInvalid
	}
	e = deploymentNativeCall(ctx, func() error {
		var err error
		fd, err = unix.Openat(parent, path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		return err
	})
	return
}
func deploymentNamespaceLeaf(n deploymentNS) (string, error) {
	switch n {
	case deploymentUserNS:
		return "ns/user", nil
	case deploymentPIDNS:
		return "ns/pid", nil
	case deploymentMountNS:
		return "ns/mnt", nil
	case deploymentCgroupNS:
		return "ns/cgroup", nil
	}
	return "", ErrDeploymentInvalid
}
func (deploymentLinux) openNamespace(ctx context.Context, parent int, n deploymentNS) (fd int, e error) {
	fd = -1
	path, e := deploymentNamespaceLeaf(n)
	if e != nil {
		return
	}
	e = deploymentNativeCall(ctx, func() error {
		var err error
		fd, err = unix.Openat(parent, path, unix.O_PATH|unix.O_CLOEXEC, 0)
		return err
	})
	return
}
func (deploymentLinux) namespaceStat(ctx context.Context, parent int, n deploymentNS) (v deploymentMetadata, e error) {
	path, e := deploymentNamespaceLeaf(n)
	if e != nil {
		return
	}
	var s unix.Stat_t
	// Flags=0 follows the proc magic link to the actual namespace referent.
	e = deploymentNativeCall(ctx, func() error { return unix.Fstatat(parent, path, &s, 0) })
	if e == nil {
		v = deploymentMetadata{device: uint64(s.Dev), inode: s.Ino, mode: s.Mode}
	}
	return
}
func (deploymentLinux) stat(ctx context.Context, fd int) (v deploymentMetadata, e error) {
	var s unix.Stat_t
	e = deploymentNativeCall(ctx, func() error { return unix.Fstat(fd, &s) })
	if e == nil {
		v = deploymentMetadata{device: uint64(s.Dev), inode: s.Ino, mode: s.Mode}
	}
	return
}
func (deploymentLinux) statfs(ctx context.Context, fd int) (v uint64, e error) {
	var s unix.Statfs_t
	e = deploymentNativeCall(ctx, func() error { return unix.Fstatfs(fd, &s) })
	if e == nil {
		v = uint64(s.Type)
	}
	return
}
func (deploymentLinux) read(ctx context.Context, fd, limit int) (raw []byte, e error) {
	switch limit {
	case 32, 64, 4096, 65536:
	default:
		return nil, ErrDeploymentInvalid
	}
	// The buffer includes the first forbidden byte; do not silently truncate.
	buffer := make([]byte, limit+1)
	used := 0
	for used < len(buffer) {
		n := 0
		e = deploymentNativeCall(ctx, func() error { var err error; n, err = unix.Read(fd, buffer[used:]); return err })
		if e != nil {
			return nil, e
		}
		if n == 0 {
			break
		}
		used += n
	}
	if used > limit {
		return nil, ErrDeploymentInvalid
	}
	return buffer[:used], nil
}
func (deploymentLinux) close(fd int) { _ = unix.Close(fd) }
