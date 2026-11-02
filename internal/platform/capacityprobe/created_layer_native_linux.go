//go:build linux && (amd64 || arm64)

package capacityprobe

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux LP64 little-endian ABI sources: selected Ubuntu 6.8.0-142.142
// fs/xfs/libxfs/xfs_fs.h:165-190,837; include/uapi/linux/fs.h:113-122,214;
// include/uapi/linux/dqblk_xfs.h:46-81,180-226; include/uapi/linux/quota.h:61-63.
// _IOR encoding: upstream Linux v6.8 include/uapi/asm-generic/ioctl.h:23-84.
// Full source reasoning: .scratch/runtime-producer-1016/linux-abi-primary-sources.md.
// Arrays use native alignment, and exact-size bidirectional compile assertions.
const createdLayerFSGEOMETRY = 0x8100587e
const createdLayerFSGETXATTR = 0x801c581f
const createdLayerQSTATV = 0x580802
const createdLayerQGET = 0x580302

type createdLayerGeometryABI [32]uint64
type createdLayerAttrsABI [7]uint32
type createdLayerQStatABI [20]uint64
type createdLayerQuotaABI [14]uint64

var (
	_ [256 - unsafe.Sizeof(createdLayerGeometryABI{})]byte
	_ [unsafe.Sizeof(createdLayerGeometryABI{}) - 256]byte
	_ [28 - unsafe.Sizeof(createdLayerAttrsABI{})]byte
	_ [unsafe.Sizeof(createdLayerAttrsABI{}) - 28]byte
	_ [160 - unsafe.Sizeof(createdLayerQStatABI{})]byte
	_ [unsafe.Sizeof(createdLayerQStatABI{}) - 160]byte
	_ [112 - unsafe.Sizeof(createdLayerQuotaABI{})]byte
	_ [unsafe.Sizeof(createdLayerQuotaABI{}) - 112]byte
	_ [unsafe.Alignof(createdLayerGeometryABI{}) - 8]byte
	_ [8 - unsafe.Alignof(createdLayerGeometryABI{})]byte
	_ [unsafe.Alignof(createdLayerAttrsABI{}) - 4]byte
	_ [4 - unsafe.Alignof(createdLayerAttrsABI{})]byte
	_ [unsafe.Alignof(createdLayerQStatABI{}) - 8]byte
	_ [8 - unsafe.Alignof(createdLayerQStatABI{})]byte
	_ [unsafe.Alignof(createdLayerQuotaABI{}) - 8]byte
	_ [8 - unsafe.Alignof(createdLayerQuotaABI{})]byte
)

type createdLayerLinux struct{}

func ReadCreatedLayerPhysical(ctx context.Context, upper string) (*CreatedLayerPhysical, error) {
	return readCreatedLayerPhysical(ctx, upper, createdLayerLinux{})
}
func (createdLayerLinux) close(fd int) { _ = unix.Close(fd) }
func (createdLayerLinux) open(ctx context.Context, p string) (fd int, err error) {
	if e := createdLayerContext(ctx); e != nil {
		return -1, e
	}
	fd, err = unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer func() {
		if err != nil {
			unix.Close(fd)
			fd = -1
		}
	}()
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if e := createdLayerContext(ctx); e != nil {
			return fd, e
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return fd, e
		}
		unix.Close(fd)
		fd = next
		if e = createdLayerContext(ctx); e != nil {
			return fd, e
		}
	}
	return fd, nil
}
func createdLayerProcRead(ctx context.Context, p string, limit int64) (raw []byte, err error) {
	if err = createdLayerContext(ctx); err != nil {
		return
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if err = createdLayerContext(ctx); err != nil {
		return
	}
	raw, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return
	}
	if int64(len(raw)) > limit {
		return nil, errCreatedLayerPhysical
	}
	err = createdLayerContext(ctx)
	return
}
func (createdLayerLinux) boot(ctx context.Context) ([16]byte, error) {
	b, e := createdLayerProcRead(ctx, "/proc/sys/kernel/random/boot_id", 64)
	if e != nil {
		return [16]byte{}, e
	}
	return createdLayerUUID(b)
}
func (createdLayerLinux) namespace(ctx context.Context) (uint64, error) {
	if e := createdLayerContext(ctx); e != nil {
		return 0, e
	}
	var s unix.Stat_t
	e := unix.Stat("/proc/self/ns/mnt", &s)
	if e != nil {
		return 0, e
	}
	if e = createdLayerContext(ctx); e != nil {
		return 0, e
	}
	if s.Ino == 0 {
		return 0, errCreatedLayerPhysical
	}
	return s.Ino, nil
}
func (createdLayerLinux) mountinfo(ctx context.Context) ([]byte, error) {
	return createdLayerProcRead(ctx, "/proc/self/mountinfo", MaxResponse)
}
func (createdLayerLinux) stat(ctx context.Context, fd int) (m createdLayerMetadata, err error) {
	if err = createdLayerContext(ctx); err != nil {
		return
	}
	var s unix.Stat_t
	if err = unix.Fstat(fd, &s); err != nil {
		return
	}
	m = createdLayerMetadata{Inode: s.Ino, Major: unix.Major(uint64(s.Dev)), Minor: unix.Minor(uint64(s.Dev)), Mode: s.Mode, UID: s.Uid, GID: s.Gid, Nlink: uint64(s.Nlink), CtimeSec: s.Ctim.Sec, CtimeNsec: s.Ctim.Nsec}
	if err = createdLayerContext(ctx); err != nil {
		return
	}
	var fs unix.Statfs_t
	if err = unix.Fstatfs(fd, &fs); err != nil {
		return
	}
	m.FSType = int64(fs.Type)
	if err = createdLayerContext(ctx); err != nil {
		return
	}
	var b []byte
	b, err = createdLayerProcRead(ctx, "/proc/self/fdinfo/"+strconv.Itoa(fd), 4096)
	if err != nil {
		return
	}
	m.MountID, err = createdLayerFDInfo(b)
	return
}
func (createdLayerLinux) geometry(ctx context.Context, fd int, out *[256]byte) error {
	if e := createdLayerContext(ctx); e != nil {
		return e
	}
	var b createdLayerGeometryABI
	_, _, n := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), createdLayerFSGEOMETRY, uintptr(unsafe.Pointer(&b[0])))
	copy(out[:], unsafe.Slice((*byte)(unsafe.Pointer(&b[0])), 256))
	if n != 0 {
		return n
	}
	return createdLayerContext(ctx)
}
func (createdLayerLinux) attrs(ctx context.Context, fd int, out *[28]byte) error {
	if e := createdLayerContext(ctx); e != nil {
		return e
	}
	var b createdLayerAttrsABI
	_, _, n := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), createdLayerFSGETXATTR, uintptr(unsafe.Pointer(&b[0])))
	copy(out[:], unsafe.Slice((*byte)(unsafe.Pointer(&b[0])), 28))
	if n != 0 {
		return n
	}
	return createdLayerContext(ctx)
}
func (createdLayerLinux) quota(ctx context.Context, fd int, id uint32, status bool, out []byte) error {
	if e := createdLayerContext(ctx); e != nil {
		return e
	}
	// Q_XGETQUOTA(0) may push XFS inodegc; GET is not kernel-zero-side-effect.
	var n unix.Errno
	if status {
		if len(out) != 160 || id != 0 || out[0] != 1 {
			return errCreatedLayerPhysical
		}
		var b createdLayerQStatABI
		b[0] = 1
		_, _, n = unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), createdLayerQSTATV, 0, uintptr(unsafe.Pointer(&b[0])), 0, 0)
		copy(out, unsafe.Slice((*byte)(unsafe.Pointer(&b[0])), 160))
	} else {
		if len(out) != 112 {
			return errCreatedLayerPhysical
		}
		var b createdLayerQuotaABI
		_, _, n = unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), createdLayerQGET, uintptr(id), uintptr(unsafe.Pointer(&b[0])), 0, 0)
		copy(out, unsafe.Slice((*byte)(unsafe.Pointer(&b[0])), 112))
	}
	if n != 0 {
		return n
	}
	return createdLayerContext(ctx)
}
