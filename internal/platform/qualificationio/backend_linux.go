//go:build linux && (amd64 || arm64 || 386 || arm)

package qualificationio

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

type backendObjects struct {
	root, parent, target, child *os.File
	c                           Control
	targetName                  string
	path                        string
	rootIdentity                FDIdentity
	targetIdentity              FDIdentity
	childIdentity               FDIdentity
	pathOnly                    bool
}

func (h *backendObjects) close() {
	if h.child != nil {
		h.child.Close()
	}
	if h.target != nil {
		h.target.Close()
	}
	if h.parent != nil {
		h.parent.Close()
	}
	if h.root != nil {
		h.root.Close()
	}
}

func openBackendObjects(ctx context.Context, c Control) (h *backendObjects, err error) {
	h = &backendObjects{c: c, path: targetPath(c), targetName: positiveCase(c.BackendCase)}
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
	h.rootIdentity, err = observeFD(ctx, fd, DataPath)
	if err != nil || !matchesObject(h.rootIdentity, c.Root) {
		return h, fmt.Errorf("qualification backend root mismatch")
	}
	if c.BackendCase == "worker-handoff" {
		h.targetName = "target"
		fd, err = unix.Dup(fd)
	} else {
		fd, err = unix.Openat(fd, "handoff", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return h, err
	}
	h.parent = os.NewFile(uintptr(fd), "qualification target parent")
	if c.BackendCase != "worker-handoff" {
		var s unix.Stat_t
		if unix.Fstat(fd, &s) != nil || s.Uid != 0 || s.Mode&unix.S_IFMT != unix.S_IFDIR || s.Mode&0022 != 0 {
			return h, fmt.Errorf("qualification handoff parent unsafe")
		}
	}
	fd, err = unix.Openat(int(h.parent.Fd()), h.targetName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil && c.BackendCase == "without-dac-override" && (err == unix.EACCES || err == unix.EPERM) {
		h.pathOnly = true
		fd, err = unix.Openat(int(h.parent.Fd()), h.targetName, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return h, err
	}
	h.target = os.NewFile(uintptr(fd), h.path)
	if h.pathOnly {
		h.targetIdentity, err = observePathFD(ctx, fd, h.path, h.rootIdentity)
	} else {
		h.targetIdentity, err = observeFD(ctx, fd, h.path)
	}
	if err != nil || !backendObjectMatches(h.targetIdentity, c.Target, h.pathOnly) {
		return h, fmt.Errorf("qualification backend target mismatch")
	}
	if c.BackendCase != "empty" && !negativeCase(c.BackendCase) {
		fd, err = unix.Openat(int(h.target.Fd()), "io", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return h, err
		}
		h.child = os.NewFile(uintptr(fd), h.path+"/io")
		h.childIdentity, err = observeFD(ctx, fd, h.path+"/io")
		if err != nil || !matchesObject(h.childIdentity, c.Child) {
			return h, fmt.Errorf("qualification backend child mismatch")
		}
		if binary.LittleEndian.Uint32(h.childIdentity.Statx[16:20]) != 1 {
			return h, fmt.Errorf("qualification backend child hardlink refused")
		}
	}
	if !negativeCase(c.BackendCase) {
		_, _, o, e := h.observe(ctx, 0)
		if e != nil || !o.EntriesAvailable {
			return h, fmt.Errorf("qualification initial entries unavailable")
		}
		want := uint64(1)
		if c.BackendCase == "empty" {
			want = 0
		}
		if o.Entries != want {
			return h, fmt.Errorf("qualification initial entries mismatch")
		}
	}
	return h, nil
}

func backendObjectMatches(v FDIdentity, o Object, pathOnly bool) bool {
	return v.Device == o.Device && v.FSUUID == o.FSUUID && v.Inode == o.Inode && v.Owner == o.Owner && v.Mode == o.Mode && (pathOnly || v.ProjectID == o.ProjectID)
}

// O_PATH cannot issue ioctl. Keep actual statx and mark attribute availability
// explicitly; filesystem UUID comes from the actual held same-device root FD.
func observePathFD(ctx context.Context, fd int, p string, root FDIdentity) (FDIdentity, error) {
	var st unix.Statx_t
	if err := checkedContext(ctx); err != nil {
		return FDIdentity{}, err
	}
	actualPath, err := actualFDPath(fd, p)
	if err != nil {
		return FDIdentity{}, err
	}
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st); err != nil {
		return FDIdentity{}, err
	}
	if unsafe.Sizeof(st) != 256 || st.Mask&(unix.STATX_INO|unix.STATX_UID|unix.STATX_MODE|unix.STATX_MNT_ID) != (unix.STATX_INO|unix.STATX_UID|unix.STATX_MODE|unix.STATX_MNT_ID) {
		return FDIdentity{}, fmt.Errorf("qualification O_PATH statx unavailable")
	}
	dev := fmt.Sprintf("%d:%d", st.Dev_major, st.Dev_minor)
	if dev != root.Device {
		return FDIdentity{}, fmt.Errorf("qualification O_PATH device changed")
	}
	return FDIdentity{FD: uint64(fd), Path: actualPath, Device: dev, FSUUID: root.FSUUID, Inode: st.Ino, MountID: st.Mnt_id, Owner: uint64(st.Uid), Mode: uint64(st.Mode), Statx: append([]byte{}, (*[256]byte)(unsafe.Pointer(&st))[:]...)}, nil
}

func (h *backendObjects) prepare(ctx context.Context) error {
	// This exact finite transition only handles producer-established root-owned
	// objects. Reduced-cap helpers never run it or restore capabilities.
	if negativeCase(h.c.BackendCase) {
		return fmt.Errorf("qualification negative prepare refused")
	}
	if err := checkedContext(ctx); err != nil {
		return err
	}
	if h.child != nil {
		if err := unix.Fchown(int(h.child.Fd()), 10002, 10002); err != nil {
			return err
		}
		if err := checkedContext(ctx); err != nil {
			return err
		}
		if err := unix.Fchmod(int(h.child.Fd()), 0600); err != nil {
			return err
		}
	}
	if err := checkedContext(ctx); err != nil {
		return err
	}
	if err := unix.Fchown(int(h.target.Fd()), 10002, 10002); err != nil {
		return err
	}
	if err := checkedContext(ctx); err != nil {
		return err
	}
	if err := unix.Fchmod(int(h.target.Fd()), backendMode(h.c.BackendCase)); err != nil {
		return err
	}
	return checkedContext(ctx)
}

func (h *backendObjects) observe(ctx context.Context, operationErrno uint64) (FDIdentity, *FDIdentity, BackendObservation, error) {
	o := BackendObservation{Operation: backendOperation(h.c.BackendCase), Errno: operationErrno, AttributesAvailable: !h.pathOnly}
	var target FDIdentity
	var err error
	if h.pathOnly {
		target, err = observePathFD(ctx, int(h.target.Fd()), h.path, h.rootIdentity)
		attrs := make([]byte, 28)
		o.AttributesErrno = rawIoctl(int(h.target.Fd()), 0x801c581f, attrs)
		target.FSXAttr = attrs
		if o.AttributesErrno == 0 {
			return target, nil, o, fmt.Errorf("qualification O_PATH attribute semantics unknown")
		}
	} else {
		target, err = observeFD(ctx, int(h.target.Fd()), h.path)
	}
	if err != nil {
		return target, nil, o, err
	}
	if target.Device != h.targetIdentity.Device || target.Inode != h.targetIdentity.Inode || target.FSUUID != h.targetIdentity.FSUUID {
		return target, nil, o, fmt.Errorf("qualification backend target replaced")
	}
	readFD, e := unix.Openat(int(h.target.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		o.EntriesErrno = errnoOf(e)
	} else {
		f := os.NewFile(uintptr(readFD), h.path)
		names, e := f.Readdirnames(2)
		f.Close()
		if e != nil && e != io.EOF {
			return target, nil, o, e
		}
		if len(names) > 1 || (len(names) == 1 && names[0] != "io") {
			return target, nil, o, fmt.Errorf("qualification unknown entries")
		}
		o.Entries = uint64(len(names))
		o.EntriesAvailable = true
	}
	var child *FDIdentity
	var cs unix.Stat_t
	e = unix.Fstatat(int(h.target.Fd()), "io", &cs, unix.AT_SYMLINK_NOFOLLOW)
	if e != nil {
		o.ChildErrno = errnoOf(e)
	} else {
		if cs.Mode&unix.S_IFMT != unix.S_IFREG || uint64(cs.Dev) != deviceNumber(h.c.Child.Device) || cs.Ino != h.c.Child.Inode {
			return target, nil, o, fmt.Errorf("qualification backend child replaced")
		}
		o.ChildAvailable = true
		o.ChildInode = cs.Ino
		o.ChildOwner = uint64(cs.Uid)
	}
	if h.child != nil {
		v, e := observeFD(ctx, int(h.child.Fd()), h.path+"/io")
		if e != nil {
			return target, nil, o, e
		}
		if v.Inode != h.childIdentity.Inode || v.Device != h.childIdentity.Device {
			return target, nil, o, fmt.Errorf("qualification held child changed")
		}
		child = &v
	}
	return target, child, o, nil
}

func deviceNumber(s string) uint64 {
	var major, minor uint32
	if _, err := fmt.Sscanf(s, "%d:%d", &major, &minor); err != nil {
		return ^uint64(0)
	}
	return unix.Mkdev(major, minor)
}

func (h *backendObjects) operate(ctx context.Context) (uint64, error) {
	if err := checkedContext(ctx); err != nil {
		return 0, err
	}
	var err error
	switch positiveCase(h.c.BackendCase) {
	case "empty", "nonempty":
		err = unix.Fchown(int(h.target.Fd()), 0, -1)
	case "private", "sticky":
		err = unix.Unlinkat(int(h.target.Fd()), "io", 0)
	default:
		return 0, fmt.Errorf("qualification operation invalid")
	}
	if err != nil {
		return errnoOf(err), checkedContext(ctx)
	}
	return 0, checkedContext(ctx)
}

func (h *backendObjects) cleanup(ctx context.Context) error {
	if err := checkedContext(ctx); err != nil {
		return err
	}
	var s unix.Stat_t
	if err := unix.Fstatat(int(h.parent.Fd()), h.targetName, &s, unix.AT_SYMLINK_NOFOLLOW); err != nil || s.Ino != h.targetIdentity.Inode || uint64(s.Dev) != deviceNumber(h.targetIdentity.Device) || s.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("qualification cleanup target unknown")
	}
	owner := uint32(10002)
	if h.c.BackendCase == "empty" || h.c.BackendCase == "nonempty" {
		owner = 0
	}
	if s.Uid != owner || s.Mode&07777 != backendMode(h.c.BackendCase) {
		return fmt.Errorf("qualification cleanup ownership changed")
	}
	_, _, o, err := h.observe(ctx, 0)
	if err != nil || !o.EntriesAvailable {
		return fmt.Errorf("qualification cleanup entries unknown")
	}
	if o.Entries != backendCleanupEntries(h.c.BackendCase) {
		return fmt.Errorf("qualification cleanup phase entries changed")
	}
	if o.Entries == 1 {
		if !o.ChildAvailable || o.ChildInode != h.childIdentity.Inode || o.ChildOwner != 10002 {
			return fmt.Errorf("qualification cleanup child unknown")
		}
		var child unix.Stat_t
		if unix.Fstatat(int(h.target.Fd()), "io", &child, unix.AT_SYMLINK_NOFOLLOW) != nil || child.Mode&07777 != 0600 || child.Nlink != 1 {
			return fmt.Errorf("qualification cleanup child ownership unknown")
		}
		if err := checkedContext(ctx); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(h.target.Fd()), "io", 0); err != nil {
			return err
		}
	}
	if err := checkedContext(ctx); err != nil {
		return err
	}
	return unix.Unlinkat(int(h.parent.Fd()), h.targetName, unix.AT_REMOVEDIR)
}

func emitBackendFrame(ctx context.Context, out io.Writer, c Control, h *backendObjects, seq uint64, phase string, errno uint64) error {
	p, err := observeLocal(ctx)
	if err != nil {
		return err
	}
	if err = verifyProcess(c, p, backendMask(c.BackendCase), c.BinaryDigest, false); err != nil {
		return err
	}
	t, ch, o, err := h.observe(ctx, errno)
	if err != nil {
		return err
	}
	f := NewCommand(c, seq, phase, c.BackendCase)
	f.Process = &p
	f.Target = &t
	f.Child = ch
	f.Backend = &o
	return WriteFrame(out, f)
}

// RunBackend is the final Backend ELF's finite file-capability branch. Full-cap
// holders retain established FDs across reduced-cap container observations;
// negative helpers neither prepare nor clean up, and cannot gain capabilities.
func RunBackend(ctx context.Context, output io.Writer) error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("qualification backend architecture unsupported")
	}
	ctx, cancel, err := finiteContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	c, err := readControl(ctx)
	if err != nil {
		return err
	}
	if c.Role != "backend" || c.Architecture != runtime.GOARCH {
		return fmt.Errorf("qualification backend role invalid")
	}
	p, err := observeLocal(ctx)
	if err != nil {
		return err
	}
	if err = verifyProcess(c, p, backendMask(c.BackendCase), c.BinaryDigest, false); err != nil {
		return err
	}
	h, err := openBackendObjects(ctx, c)
	if err != nil {
		return err
	}
	defer h.close()
	in, out, err := finiteChannels(ctx, output)
	if err != nil {
		return err
	}
	if !negativeCase(c.BackendCase) {
		if err = h.prepare(ctx); err != nil {
			return err
		}
	}
	if err = emitBackendFrame(ctx, out, c, h, 0, "backend-before", 0); err != nil {
		return err
	}
	if negativeCase(c.BackendCase) {
		if err = expectCommand(in, c, 1, "backend-operate", c.BackendCase); err != nil {
			return err
		}
		errno, e := h.operate(ctx)
		if e != nil {
			return e
		}
		if err = emitBackendFrame(ctx, out, c, h, 1, "backend-after", errno); err != nil {
			return err
		}
		if err = expectCommand(in, c, 2, "backend-release", c.BackendCase); err != nil {
			return err
		}
		if errno != uint64(unix.EPERM) && errno != uint64(unix.EACCES) {
			return fmt.Errorf("qualification negative operation not denied")
		}
		return nil
	}
	if err = expectCommand(in, c, 1, "backend-observe", c.BackendCase); err != nil {
		return err
	}
	if err = emitBackendFrame(ctx, out, c, h, 1, "backend-observed", 0); err != nil {
		return err
	}
	cleanupSequence := uint64(2)
	if c.BackendCase != "worker-handoff" {
		if err = expectCommand(in, c, 2, "backend-operate", c.BackendCase); err != nil {
			return err
		}
		errno, e := h.operate(ctx)
		if e != nil {
			return e
		}
		if err = emitBackendFrame(ctx, out, c, h, 2, "backend-after", errno); err != nil {
			return err
		}
		if errno != 0 {
			return fmt.Errorf("qualification backend operation failed")
		}
		cleanupSequence = 3
	}
	if err = expectCommand(in, c, cleanupSequence, "backend-cleanup", c.BackendCase); err != nil {
		return err
	}
	if err = h.cleanup(ctx); err != nil {
		return err
	}
	// A removed directory may no longer permit opening '.'. Record only the
	// actual held FD statx at the cleanup boundary; no synthetic entries count.
	p, err = observeLocal(ctx)
	if err != nil {
		return err
	}
	t, err := observeFD(ctx, int(h.target.Fd()), h.path)
	if err != nil {
		return err
	}
	f := NewCommand(c, cleanupSequence, "backend-cleaned", c.BackendCase)
	f.Process = &p
	f.Target = &t
	if err = WriteFrame(out, f); err != nil {
		return err
	}
	return expectCommand(in, c, cleanupSequence+1, "backend-release", c.BackendCase)
}
