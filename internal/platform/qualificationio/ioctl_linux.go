//go:build linux && (amd64 || arm64 || 386 || arm)

package qualificationio

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

func rawIoctl(fd int, request uint64, buf []byte) uint64 {
	if len(buf) == 0 {
		return uint64(unix.EINVAL)
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(request), uintptr(unsafe.Pointer(&buf[0])))
	return uint64(errno)
}

func observeFD(ctx context.Context, fd int, p string) (FDIdentity, error) {
	var out FDIdentity
	if err := checkedContext(ctx); err != nil {
		return out, err
	}
	actualPath, err := actualFDPath(fd, p)
	if err != nil {
		return out, err
	}
	var st unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st); err != nil {
		return out, err
	}
	if unsafe.Sizeof(st) != 256 || st.Mask&(unix.STATX_INO|unix.STATX_UID|unix.STATX_MODE|unix.STATX_MNT_ID) != (unix.STATX_INO|unix.STATX_UID|unix.STATX_MODE|unix.STATX_MNT_ID) {
		return out, fmt.Errorf("qualification statx fields unavailable")
	}
	raw := append([]byte{}, (*[256]byte)(unsafe.Pointer(&st))[:]...)
	attrs := make([]byte, 28)
	if rawIoctl(fd, 0x801c581f, attrs) != 0 {
		return out, fmt.Errorf("qualification fsxattr unavailable")
	}
	geometry := make([]byte, 256)
	if rawIoctl(fd, 0x8100587e, geometry) != 0 || binary.LittleEndian.Uint32(geometry[88:92]) != 5 {
		return out, fmt.Errorf("qualification XFS geometry unavailable")
	}
	uuid := geometry[64:80]
	if bytes.Equal(uuid, make([]byte, 16)) {
		return out, fmt.Errorf("qualification filesystem UUID unavailable")
	}
	out = FDIdentity{FD: uint64(fd), Path: actualPath, Device: fmt.Sprintf("%d:%d", st.Dev_major, st.Dev_minor), FSUUID: fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), Inode: st.Ino, MountID: st.Mnt_id, Owner: uint64(st.Uid), Mode: uint64(st.Mode), ProjectID: uint64(binary.LittleEndian.Uint32(attrs[12:16])), Statx: raw, FSXAttr: attrs}
	return out, checkedContext(ctx)
}

func actualFDPath(fd int, expected string) (string, error) {
	if fd < 3 {
		return "", fmt.Errorf("qualification object FD invalid")
	}
	buf := make([]byte, 4096)
	n, err := unix.Readlink("/proc/self/fd/"+strconv.Itoa(fd), buf)
	if err != nil {
		return "", err
	}
	if n < 1 || n >= len(buf) {
		return "", fmt.Errorf("qualification object FD path oversized")
	}
	actual := string(buf[:n])
	if actual != expected && actual != expected+" (deleted)" {
		return "", fmt.Errorf("qualification object FD path changed")
	}
	return actual, nil
}

func matchesObject(v FDIdentity, o Object) bool {
	return v.Device == o.Device && v.FSUUID == o.FSUUID && v.Inode == o.Inode && v.Owner == o.Owner && v.Mode == o.Mode && v.ProjectID == o.ProjectID
}

type heldObjects struct {
	root, target, child *os.File
	c                   Control
}

func (h *heldObjects) close() {
	if h == nil {
		return
	}
	if h.child != nil {
		h.child.Close()
	}
	if h.target != nil {
		h.target.Close()
	}
	if h.root != nil {
		h.root.Close()
	}
}

func openWorkerObjects(ctx context.Context, c Control) (h *heldObjects, err error) {
	h = &heldObjects{c: c}
	defer func() {
		if err != nil {
			h.close()
		}
	}()
	fd, err := unix.Open(DataPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return h, err
	}
	h.root = os.NewFile(uintptr(fd), DataPath)
	v, err := observeFD(ctx, fd, DataPath)
	if err != nil || !matchesObject(v, c.Root) {
		return h, fmt.Errorf("qualification root identity mismatch")
	}
	fd, err = unix.Openat(int(h.root.Fd()), "target", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return h, err
	}
	h.target = os.NewFile(uintptr(fd), DataPath+"/target")
	fd, err = unix.Openat(int(h.target.Fd()), "io", unix.O_RDWR|unix.O_DSYNC|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return h, err
	}
	h.child = os.NewFile(uintptr(fd), DataPath+"/target/io")
	_, _, err = h.observe(ctx)
	if err == nil {
		var s unix.Stat_t
		if unix.Fstat(int(h.child.Fd()), &s) != nil || s.Nlink != 1 || s.Size != 0 {
			return h, fmt.Errorf("qualification initial child is not fresh")
		}
		for _, v := range []struct {
			f    *os.File
			name string
		}{{h.root, "target"}, {h.target, "io"}} {
			names, e := v.f.Readdirnames(2)
			if (e != nil && e != io.EOF) || len(names) != 1 || names[0] != v.name {
				return h, fmt.Errorf("qualification initial directory not exclusive")
			}
		}
	}
	return h, err
}

func inheritedObjects(ctx context.Context, c Control) (*heldObjects, error) {
	h := &heldObjects{c: c, target: os.NewFile(3, DataPath+"/target"), child: os.NewFile(4, DataPath+"/target/io")}
	_, _, err := h.observe(ctx)
	if err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

func (h *heldObjects) observe(ctx context.Context) (FDIdentity, FDIdentity, error) {
	dir, err := observeFD(ctx, int(h.target.Fd()), DataPath+"/target")
	if err != nil {
		return dir, FDIdentity{}, err
	}
	child, err := observeFD(ctx, int(h.child.Fd()), DataPath+"/target/io")
	if err != nil {
		return dir, child, err
	}
	if !matchesObject(dir, h.c.Target) || !matchesObject(child, h.c.Child) || dir.FD == child.FD || dir.Inode == child.Inode {
		return dir, child, fmt.Errorf("qualification held objects changed")
	}
	if dir.Path != DataPath+"/target" || child.Path != DataPath+"/target/io" {
		return dir, child, fmt.Errorf("qualification held paths changed")
	}
	if binary.LittleEndian.Uint32(child.Statx[16:20]) != 1 {
		return dir, child, fmt.Errorf("qualification child link identity invalid")
	}
	return dir, child, nil
}

func writeQuota(ctx context.Context, fd int) (WriteResult, error) {
	return quotaIO(ctx, quotaIOOps{write: func(b []byte, offset int64) (int, error) { return unix.Pwrite(fd, b, offset) }, sync: func() error { return unix.Fsync(fd) }})
}

func errnoOf(err error) uint64 {
	if e, ok := err.(unix.Errno); ok {
		return uint64(e)
	}
	return ^uint64(0)
}

func executeSetter(ctx context.Context, h *heldObjects, command, abi string) (SetterIO, error) {
	var out SetterIO
	if err := checkedContext(ctx); err != nil {
		return out, err
	}
	r, err := setterRequests(abi)
	if err != nil {
		return out, err
	}
	fd := int(h.target.Fd())
	out.BeforeFSXAttr = Ioctl{Request: r.getAttr, Buffer: make([]byte, 28)}
	out.BeforeFSXAttr.Errno = rawIoctl(fd, r.getAttr, out.BeforeFSXAttr.Buffer)
	out.BeforeGetFlags = Ioctl{Request: r.getFlags, Buffer: make([]byte, r.flagsBytes)}
	out.BeforeGetFlags.Errno = rawIoctl(fd, r.getFlags, out.BeforeGetFlags.Buffer)
	if out.BeforeFSXAttr.Errno != 0 || out.BeforeGetFlags.Errno != 0 {
		return out, fmt.Errorf("qualification getter failed")
	}
	buf, err := setterAttempt(command, out.BeforeFSXAttr.Buffer, out.BeforeGetFlags.Buffer)
	if err != nil {
		return out, err
	}
	request := r.setAttr
	if command == "FS_IOC_SETFLAGS" {
		request = r.setFlags
	}
	out.Set = Ioctl{Request: request, Buffer: buf}
	if err := checkedContext(ctx); err != nil {
		return out, err
	}
	out.Set.Errno = rawIoctl(fd, request, buf)
	out.AfterFSXAttr = Ioctl{Request: r.getAttr, Buffer: make([]byte, 28)}
	out.AfterFSXAttr.Errno = rawIoctl(fd, r.getAttr, out.AfterFSXAttr.Buffer)
	out.AfterGetFlags = Ioctl{Request: r.getFlags, Buffer: make([]byte, r.flagsBytes)}
	out.AfterGetFlags.Errno = rawIoctl(fd, r.getFlags, out.AfterGetFlags.Buffer)
	if out.Set.Errno != uint64(unix.EPERM) || out.AfterFSXAttr.Errno != 0 || out.AfterGetFlags.Errno != 0 || !bytes.Equal(out.BeforeFSXAttr.Buffer, out.AfterFSXAttr.Buffer) || !bytes.Equal(out.BeforeGetFlags.Buffer, out.AfterGetFlags.Buffer) {
		return out, fmt.Errorf("qualification setter changed object or returned unexpected errno")
	}
	if err := checkedContext(ctx); err != nil {
		return out, err
	}
	// Keep finite ordinary I/O within the already charged quota-success extent.
	data := []byte("switchback-finite-qualification\n")
	n, err := unix.Pwrite(int(h.child.Fd()), data, 0)
	out.WriteBytes = uint64(n)
	if err != nil || n != len(data) {
		return out, fmt.Errorf("qualification ordinary write failed")
	}
	if err := unix.Fsync(int(h.child.Fd())); err != nil {
		return out, err
	}
	got := make([]byte, len(data))
	n, err = unix.Pread(int(h.child.Fd()), got, 0)
	out.ReadBytes = uint64(n)
	if err != nil || n != len(data) || !bytes.Equal(data, got) {
		return out, fmt.Errorf("qualification ordinary read failed")
	}
	return out, checkedContext(ctx)
}
