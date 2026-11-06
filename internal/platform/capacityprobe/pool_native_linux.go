//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type poolLinux struct{}

func newPoolNative() poolKernel { return poolLinux{} }
func (poolLinux) close(fd int)  { _ = unix.Close(fd) }
func poolNativeContext(ctx context.Context) error {
	if ctx == nil {
		return ErrPoolInvalid
	}
	return ctx.Err()
}
func poolOwnedDirectory(s unix.Stat_t) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFDIR && s.Uid == 0 && s.Gid == 0 && s.Mode&07022 == 0
}

// Unlike the created-only walk, check every ancestor's ownership and mode.
// This is a held nofollow walk, not EvalSymlinks followed by a data-open.
func poolNativeWalk(ctx context.Context, p string, proc bool) (fd int, err error) {
	fd = -1
	if !poolCanonical(p) {
		return fd, ErrPoolInvalid
	}
	if err = poolNativeContext(ctx); err != nil {
		return
	}
	fd, err = unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			unix.Close(fd)
			fd = -1
		}
	}()
	var s unix.Stat_t
	if err = unix.Fstat(fd, &s); err != nil {
		return
	}
	if !poolOwnedDirectory(s) {
		return fd, ErrPoolInvalid
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, part := range parts {
		if err = poolNativeContext(ctx); err != nil {
			return
		}
		var next int
		next, err = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return
		}
		unix.Close(fd)
		fd = next
		if err = unix.Fstat(fd, &s); err != nil {
			return
		}
		if !(proc && i == len(parts)-1) && !poolOwnedDirectory(s) {
			return fd, ErrPoolInvalid
		}
		if s.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fd, ErrPoolInvalid
		}
	}
	err = poolNativeContext(ctx)
	return
}
func (poolLinux) open(ctx context.Context, p string) (int, error) {
	if !(p == Root || strings.HasPrefix(p, Root+"/") || poolInside(p, poolCoreRoot) || poolInside(p, poolWorkerRoot)) {
		return -1, ErrPoolInvalid
	}
	return poolNativeWalk(ctx, p, false)
}
func poolProcError(e error) error {
	if errors.Is(e, unix.ENOENT) || errors.Is(e, unix.ESRCH) {
		return ErrPoolUnstable
	}
	return e
}
func (poolLinux) procOpen(ctx context.Context, pid uint64) (int, error) {
	if pid == 0 {
		pid = uint64(os.Getpid())
	}
	if pid > uint64(^uint32(0)>>1) {
		return -1, ErrPoolInvalid
	}
	fd, e := poolNativeWalk(ctx, "/proc/"+poolPIDText(pid), true)
	return fd, poolProcError(e)
}
func poolNativeRead(ctx context.Context, p string, limit int) ([]byte, error) {
	if e := poolNativeContext(ctx); e != nil {
		return nil, e
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if ce := poolNativeContext(ctx); ce != nil {
		return nil, ce
	}
	if e != nil {
		return nil, e
	}
	if len(b) > limit {
		return nil, ErrPoolInvalid
	}
	return b, nil
}
func (poolLinux) boot(ctx context.Context) ([16]byte, error) {
	b, e := poolNativeRead(ctx, "/proc/sys/kernel/random/boot_id", 64)
	if e != nil {
		return [16]byte{}, e
	}
	u, e := createdLayerUUID(b)
	if e != nil {
		return u, ErrPoolInvalid
	}
	return u, nil
}
func (poolLinux) namespace(ctx context.Context, one bool, kind string) (uint64, error) {
	if kind != "mnt" && kind != "cgroup" {
		return 0, ErrPoolInvalid
	}
	if e := poolNativeContext(ctx); e != nil {
		return 0, e
	}
	pid := "self"
	if one {
		pid = "1"
	}
	var s unix.Stat_t
	if e := unix.Stat("/proc/"+pid+"/ns/"+kind, &s); e != nil {
		return 0, e
	}
	if e := poolNativeContext(ctx); e != nil {
		return 0, e
	}
	if s.Ino == 0 {
		return 0, ErrPoolInvalid
	}
	return s.Ino, nil
}
func (poolLinux) mountinfo(ctx context.Context) ([]byte, error) {
	return poolNativeRead(ctx, "/proc/self/mountinfo", MaxResponse)
}
func (poolLinux) stat(ctx context.Context, fd int) (m createdLayerMetadata, err error) {
	if err = poolNativeContext(ctx); err != nil {
		return
	}
	var s unix.Stat_t
	if err = unix.Fstat(fd, &s); err != nil {
		return
	}
	if err = poolNativeContext(ctx); err != nil {
		return
	}
	var fs unix.Statfs_t
	if err = unix.Fstatfs(fd, &fs); err != nil {
		return
	}
	if err = poolNativeContext(ctx); err != nil {
		return
	}
	b, e := poolNativeRead(ctx, "/proc/self/fdinfo/"+strconv.Itoa(fd), 4096)
	if e != nil {
		return m, e
	}
	id, e := createdLayerFDInfo(b)
	if e != nil {
		return m, ErrPoolInvalid
	}
	m = createdLayerMetadata{Inode: s.Ino, Major: unix.Major(uint64(s.Dev)), Minor: unix.Minor(uint64(s.Dev)), Mode: s.Mode, UID: s.Uid, GID: s.Gid, MountID: id, Nlink: uint64(s.Nlink), CtimeSec: s.Ctim.Sec, CtimeNsec: s.Ctim.Nsec, FSType: int64(fs.Type)}
	return m, poolNativeContext(ctx)
}

// Only these three existing private ABI operations are reused. Running-state
// identity, statfs, namespace, cgroup and process reads are independent.
func (poolLinux) geometry(ctx context.Context, fd int, out *[256]byte) error {
	return (createdLayerLinux{}).geometry(ctx, fd, out)
}
func (poolLinux) attrs(ctx context.Context, fd int, out *[28]byte) error {
	return (createdLayerLinux{}).attrs(ctx, fd, out)
}
func (poolLinux) quota(ctx context.Context, fd int, id uint32, state bool, out []byte) error {
	return (createdLayerLinux{}).quota(ctx, fd, id, state, out)
}
func (poolLinux) statfs(ctx context.Context, fd int) (o PoolStatFSReadback, e error) {
	if e = poolNativeContext(ctx); e != nil {
		return
	}
	var s unix.Statfs_t
	if e = unix.Fstatfs(fd, &s); e != nil {
		return
	}
	if e = poolNativeContext(ctx); e != nil {
		return
	}
	if s.Type < 0 || s.Bsize <= 0 || s.Frsize <= 0 {
		return o, ErrPoolInvalid
	}
	o = PoolStatFSReadback{uint64(s.Type), uint64(s.Bsize), uint64(s.Frsize), s.Blocks, s.Bfree, s.Bavail}
	if !poolStatFSValid(o) {
		return PoolStatFSReadback{}, ErrPoolInvalid
	}
	return
}
func poolReadLeaf(ctx context.Context, dir int, leaf string, limit int, owned bool) (raw []byte, err error) {
	if err = poolNativeContext(ctx); err != nil {
		return
	}
	// Pin type first. FIFO/device is never data-opened, even with O_NONBLOCK.
	pin, e := unix.Openat(dir, leaf, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	defer unix.Close(pin)
	var before unix.Stat_t
	if err = unix.Fstat(pin, &before); err != nil {
		return
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || (owned && (before.Uid != 0 || before.Gid != 0 || before.Mode&07022 != 0)) {
		return nil, ErrPoolInvalid
	}
	if err = poolNativeContext(ctx); err != nil {
		return
	}
	fd, e := unix.Open("/proc/self/fd/"+strconv.Itoa(pin), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), "pool-control")
	if f == nil {
		unix.Close(fd)
		return nil, ErrPoolUnavailable
	}
	defer f.Close()
	var held unix.Stat_t
	if err = unix.Fstat(fd, &held); err != nil {
		return
	}
	if held.Dev != before.Dev || held.Ino != before.Ino || held.Mode != before.Mode || held.Uid != before.Uid || held.Gid != before.Gid {
		return nil, ErrPoolInvalid
	}
	raw, err = io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if ce := poolNativeContext(ctx); ce != nil {
		return nil, ce
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, ErrPoolInvalid
	}
	var after unix.Stat_t
	if err = unix.Fstatat(dir, leaf, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if after.Dev != held.Dev || after.Ino != held.Ino || after.Mode != held.Mode || after.Uid != held.Uid || after.Gid != held.Gid {
		return nil, ErrPoolInvalid
	}
	return raw, poolNativeContext(ctx)
}
func (poolLinux) control(ctx context.Context, fd int, leaf string) ([]byte, error) {
	switch leaf {
	case "cpu.max", "memory.max", "memory.swap.max", "pids.max", "cgroup.type":
		return poolReadLeaf(ctx, fd, leaf, 128, true)
	default:
		return nil, ErrPoolInvalid
	}
}
func (poolLinux) process(ctx context.Context, fd int, leaf string) ([]byte, error) {
	if leaf != "stat" && leaf != "cgroup" {
		return nil, ErrPoolInvalid
	}
	b, e := poolReadLeaf(ctx, fd, leaf, 4096, false)
	return b, poolProcError(e)
}
