//go:build linux && (amd64 || arm64)

package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"golang.org/x/sys/unix"
)

const (
	qualificationQGet       = 0x580302
	qualificationQSet       = 0x580402
	qualificationQStatV     = 0x580802
	qualificationFSGetXAttr = 0x801c581f
	qualificationFSSetXAttr = 0x401c5820
	qualificationFSGeometry = 0x8100587e
)

func qualificationIoctl(ctx context.Context, fd int, request uintptr, raw []byte) error {
	if fd < 0 || len(raw) == 0 || qualificationContext(ctx) != nil {
		return errQualification
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(unsafe.Pointer(&raw[0])))
	if errno != 0 {
		return errno
	}
	return qualificationContext(ctx)
}

func qualificationQuotaRead(ctx context.Context, fd int, id uint32) ([]byte, error) {
	if fd < 0 || id == 0 || qualificationContext(ctx) != nil {
		return nil, errQualification
	}
	raw := make([]byte, 112)
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), qualificationQGet, uintptr(id), uintptr(unsafe.Pointer(&raw[0])), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	if !createdQuotaRecord(raw, id) {
		return nil, errQualification
	}
	return raw, qualificationContext(ctx)
}

func qualificationQuotaState(ctx context.Context, fd int) ([]byte, error) {
	raw := make([]byte, 160)
	raw[0] = 1
	if fd < 0 || qualificationContext(ctx) != nil {
		return nil, errQualification
	}
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), qualificationQStatV, 0, uintptr(unsafe.Pointer(&raw[0])), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	if raw[0] != 1 || binary.LittleEndian.Uint16(raw[2:4])&0x30 != 0x30 {
		return nil, errQualification
	}
	return raw, qualificationContext(ctx)
}

// Q_XSETQLIM with FS_DQ_BHARD is the single bounded limit mutation. The
// struct layout and mask follow Linux v6.8 dqblk_xfs.h. A call error is an
// unknown effect and is never retried or compensated automatically.
func qualificationQuotaSetHard(ctx context.Context, fd int, id uint32, hard512 uint64) error {
	if fd < 0 || id == 0 || hard512 > 32768 || qualificationContext(ctx) != nil {
		return errQualification
	}
	raw := make([]byte, 112)
	raw[0], raw[1] = 1, 2
	binary.LittleEndian.PutUint16(raw[2:4], 1<<3)
	binary.LittleEndian.PutUint32(raw[4:8], id)
	binary.LittleEndian.PutUint64(raw[8:16], hard512)
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), qualificationQSet, uintptr(id), uintptr(unsafe.Pointer(&raw[0])), 0, 0)
	if errno != 0 {
		return errno
	}
	return qualificationContext(ctx)
}

func qualificationNativeFD(ctx context.Context, fd int, expectedPath string) (qualificationio.FDIdentity, error) {
	out, err := qualificationNativeFDAny(ctx, fd)
	if err != nil || out.Path != expectedPath {
		return qualificationio.FDIdentity{}, errQualification
	}
	return out, nil
}

func qualificationNativeFDAny(ctx context.Context, fd int) (qualificationio.FDIdentity, error) {
	var out qualificationio.FDIdentity
	if fd < 3 || qualificationContext(ctx) != nil {
		return out, errQualification
	}
	link := "/proc/self/fd/" + strconv.Itoa(fd)
	path, err := os.Readlink(link)
	if err != nil || path == "" {
		return out, errQualification
	}
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat); err != nil {
		return out, err
	}
	need := uint32(unix.STATX_INO | unix.STATX_UID | unix.STATX_MODE | unix.STATX_MNT_ID)
	if unsafe.Sizeof(stat) != 256 || stat.Mask&need != need || stat.Ino == 0 || stat.Mnt_id == 0 {
		return out, errQualification
	}
	attrs := make([]byte, 28)
	if err := qualificationIoctl(ctx, fd, qualificationFSGetXAttr, attrs); err != nil {
		return out, err
	}
	geometry := make([]byte, 256)
	if err := qualificationIoctl(ctx, fd, qualificationFSGeometry, geometry); err != nil {
		return out, err
	}
	if binary.LittleEndian.Uint32(geometry[88:92]) != 5 || bytes.Equal(geometry[64:80], make([]byte, 16)) {
		return out, errQualification
	}
	uuid := geometry[64:80]
	out = qualificationio.FDIdentity{
		FD: uint64(fd), Path: path, Device: fmt.Sprintf("%d:%d", stat.Dev_major, stat.Dev_minor),
		FSUUID: fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]),
		Inode:  stat.Ino, MountID: stat.Mnt_id, Owner: uint64(stat.Uid), Mode: uint64(stat.Mode),
		ProjectID: uint64(binary.LittleEndian.Uint32(attrs[12:16])),
		Statx:     append([]byte(nil), (*[256]byte)(unsafe.Pointer(&stat))[:]...), FSXAttr: attrs,
	}
	return out, qualificationContext(ctx)
}

func qualificationFDObject(v qualificationio.FDIdentity) qualificationTestObject {
	return qualificationTestObject{Device: v.Device, FSUUID: v.FSUUID, MountID: v.MountID, Inode: v.Inode, Owner: v.Owner, Mode: v.Mode, ProjectID: v.ProjectID}
}

func qualificationControlObject(v qualificationio.FDIdentity) qualificationio.Object {
	return qualificationio.Object{Device: v.Device, FSUUID: v.FSUUID, Inode: v.Inode, Owner: v.Owner, Mode: v.Mode, ProjectID: v.ProjectID}
}

func qualificationOpenPoolRoot(ctx context.Context, path string) (int, error) {
	if !absolute(path) {
		return -1, errQualification
	}
	dir, err := qualificationOpenArtifactParent(ctx, path)
	if err != nil {
		return -1, err
	}
	fd := int(dir.Fd())
	var s unix.Stat_t
	if err := unix.Fstat(fd, &s); err != nil || s.Uid != 0 || s.Mode&unix.S_IFMT != unix.S_IFDIR || s.Mode&0022 != 0 {
		dir.Close()
		return -1, errQualification
	}
	newfd, err := unix.Dup(fd)
	dir.Close()
	return newfd, err
}

type qualificationTestDirectory struct {
	Parent   int
	Root     int
	Path     string
	Identity qualificationio.FDIdentity
}

func qualificationCreateTestDirectory(ctx context.Context, trust Trust, window string) (qualificationTestDirectory, error) {
	var out qualificationTestDirectory
	rootPath, err := qualificationTestRoot(trust, window)
	if err != nil {
		return out, err
	}
	parentPath := filepath.Dir(rootPath)
	parent, err := qualificationOpenPoolRoot(ctx, parentPath)
	if err != nil {
		return out, err
	}
	var s unix.Stat_t
	if err := unix.Fstat(parent, &s); err != nil || s.Uid != 0 || s.Gid != 0 || s.Mode&0777 != 0700 {
		unix.Close(parent)
		return out, errQualification
	}
	if err := unix.Mkdirat(parent, window, 0755); err != nil {
		unix.Close(parent)
		return out, err
	}
	fd, err := unix.Openat(parent, window, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Close(parent)
		return out, err
	}
	identity, err := qualificationNativeFD(ctx, fd, rootPath)
	if err != nil || identity.Owner != 0 || identity.Mode != 0040755 || identity.Device != trust.Identity.Device || identity.FSUUID != trust.Identity.FSUUID {
		unix.Close(fd)
		unix.Close(parent)
		return out, errQualification
	}
	product := uint64(0)
	for _, coverage := range trust.Coverage {
		if coverage.Role != "product" {
			continue
		}
		for _, storage := range trust.Storage {
			if storage.ID == coverage.DomainID && storage.Path == filepath.Dir(parentPath) {
				product = storage.ProjectID
			}
		}
	}
	if product == 0 || identity.ProjectID != product {
		unix.Close(fd)
		unix.Close(parent)
		return out, errQualification
	}
	out = qualificationTestDirectory{Parent: parent, Root: fd, Path: rootPath, Identity: identity}
	return out, qualificationContext(ctx)
}

func (d *qualificationTestDirectory) close() {
	if d.Root >= 0 {
		_ = unix.Close(d.Root)
		d.Root = -1
	}
	if d.Parent >= 0 {
		_ = unix.Close(d.Parent)
		d.Parent = -1
	}
}

func qualificationCreateDirectoryAt(ctx context.Context, parent int, parentPath, name string, mode uint32) (int, qualificationio.FDIdentity, error) {
	if !qualificationLeaf(name) || mode != 0755 || qualificationContext(ctx) != nil {
		return -1, qualificationio.FDIdentity{}, errQualification
	}
	if err := unix.Mkdirat(parent, name, mode); err != nil {
		return -1, qualificationio.FDIdentity{}, err
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, qualificationio.FDIdentity{}, err
	}
	id, err := qualificationNativeFD(ctx, fd, filepath.Join(parentPath, name))
	if err != nil || id.Owner != 0 || id.Mode != 0040755 {
		unix.Close(fd)
		return -1, qualificationio.FDIdentity{}, errQualification
	}
	return fd, id, nil
}

func qualificationCreateFileAt(ctx context.Context, parent int, parentPath string) (int, qualificationio.FDIdentity, error) {
	if qualificationContext(ctx) != nil {
		return -1, qualificationio.FDIdentity{}, errQualification
	}
	fd, err := unix.Openat(parent, "io", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_DSYNC, 0600)
	if err != nil {
		return -1, qualificationio.FDIdentity{}, err
	}
	id, err := qualificationNativeFD(ctx, fd, filepath.Join(parentPath, "io"))
	if err != nil || id.Owner != 0 || id.Mode != 0100600 || id.Inode == 0 {
		unix.Close(fd)
		return -1, qualificationio.FDIdentity{}, errQualification
	}
	return fd, id, nil
}

func qualificationSetProject(ctx context.Context, fd int, path string, inheritedProject, project uint32) (qualificationio.FDIdentity, error) {
	if project == 0 || project >= 0xfffffffe {
		return qualificationio.FDIdentity{}, errQualification
	}
	before, err := qualificationNativeFD(ctx, fd, path)
	if err != nil || before.Owner != 0 || before.ProjectID != uint64(inheritedProject) || inheritedProject == project || binary.LittleEndian.Uint32(before.FSXAttr[0:4])&0x200 == 0 {
		return qualificationio.FDIdentity{}, errQualification
	}
	request := append([]byte(nil), before.FSXAttr...)
	binary.LittleEndian.PutUint32(request[0:4], binary.LittleEndian.Uint32(request[0:4])|0x200)
	binary.LittleEndian.PutUint32(request[12:16], project)
	if err := qualificationIoctl(ctx, fd, qualificationFSSetXAttr, request); err != nil {
		return qualificationio.FDIdentity{}, err
	}
	after, err := qualificationNativeFD(ctx, fd, path)
	if err != nil || after.ProjectID != uint64(project) || binary.LittleEndian.Uint32(after.FSXAttr[:4])&0x200 == 0 || after.Inode != before.Inode || after.Device != before.Device || after.FSUUID != before.FSUUID {
		return qualificationio.FDIdentity{}, errQualification
	}
	return after, nil
}

func qualificationFreeBytes(ctx context.Context, fd int) (uint64, error) {
	var s unix.Statfs_t
	if err := unix.Fstatfs(fd, &s); err != nil || s.Bavail == 0 || s.Frsize <= 0 {
		return 0, errQualification
	}
	if s.Bavail > math.MaxUint64/uint64(s.Frsize) {
		return 0, errQualification
	}
	return s.Bavail * uint64(s.Frsize), qualificationContext(ctx)
}

func qualificationDuplicateHeldFD(ctx context.Context, hostPID uint64, target qualificationio.FDIdentity) (int, qualificationio.FDIdentity, error) {
	if hostPID == 0 || hostPID > math.MaxInt32 || target.FD < 3 || target.FD > math.MaxInt32 {
		return -1, qualificationio.FDIdentity{}, errQualification
	}
	pidfd, err := unix.PidfdOpen(int(hostPID), 0)
	if err != nil {
		return -1, qualificationio.FDIdentity{}, err
	}
	defer unix.Close(pidfd)
	fd, err := unix.PidfdGetfd(pidfd, int(target.FD), 0)
	if err != nil {
		return -1, qualificationio.FDIdentity{}, err
	}
	actual, err := qualificationNativeFDAny(ctx, fd)
	if err != nil || actual.Device != target.Device || actual.FSUUID != target.FSUUID || actual.Inode != target.Inode || actual.Owner != target.Owner || actual.Mode != target.Mode || actual.ProjectID != target.ProjectID {
		unix.Close(fd)
		return -1, qualificationio.FDIdentity{}, errQualification
	}
	return fd, actual, nil
}

func qualificationQuotaValue(raw, state []byte, id uint32) (StorageQuota, error) {
	if !createdQuotaRecord(raw, id) || len(state) != 160 || state[0] != 1 {
		return StorageQuota{}, errQualification
	}
	return StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: uint64(state[0]), StateFlags: uint64(binary.LittleEndian.Uint16(state[2:4])),
		Version: uint64(raw[0]), Flags: uint64(raw[1]), ProjectID: uint64(id),
		HardBlocks512: binary.LittleEndian.Uint64(raw[8:16]), UsedBlocks512: binary.LittleEndian.Uint64(raw[40:48])}, nil
}

func qualificationGuestCgroupPath(raw []byte) string {
	return strings.TrimSuffix(strings.TrimPrefix(string(raw), "0::"), "\n")
}
