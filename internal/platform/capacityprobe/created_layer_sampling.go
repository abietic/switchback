package capacityprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Linux include/uapi/linux/fs.h: FS_XFLAG_REALTIME, FS_XFLAG_RTINHERIT
// and FS_XFLAG_PROJINHERIT (see linux-abi-primary-sources.md).
const (
	createdLayerXFlagRealtime    uint32 = 0x00000001
	createdLayerXFlagRTInherit   uint32 = 0x00000100
	createdLayerXFlagProjInherit uint32 = 0x00000200
)

// This private interface is only the finite directory sampling sequence. The sole
// production implementation is Linux; recording backends are not Linux evidence.
type createdLayerKernel interface {
	boot(context.Context) ([16]byte, error)
	namespace(context.Context) (uint64, error)
	mountinfo(context.Context) ([]byte, error)
	open(context.Context, string) (int, error)
	close(int)
	stat(context.Context, int) (createdLayerMetadata, error)
	geometry(context.Context, int, *[256]byte) error
	attrs(context.Context, int, *[28]byte) error
	quota(context.Context, int, uint32, bool, []byte) error
}
type createdLayerMetadata struct {
	Inode                        uint64
	Major, Minor, Mode, UID, GID uint32
	MountID, Nlink               uint64
	CtimeSec, CtimeNsec          int64
	FSType                       int64
}

// Only a fixed operation and an errno/context cause cross the diagnostic boundary.
type createdLayerReadFailure struct {
	operation string
	cause     error
}

func (e *createdLayerReadFailure) Error() string {
	return "created layer " + e.operation + " unavailable"
}
func (e *createdLayerReadFailure) Unwrap() error { return e.cause }
func createdLayerFailure(op string, err error) error {
	var n syscall.Errno
	var cause error
	switch {
	case errors.As(err, &n):
		cause = n
	case errors.Is(err, context.Canceled):
		cause = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		cause = context.DeadlineExceeded
	}
	return &createdLayerReadFailure{op, cause}
}

var errCreatedLayerPhysical = errors.New("physical observation invalid")

func createdLayerUpperValid(p string) bool {
	const prefix = Root + "/docker/overlay2/"
	if !strings.HasPrefix(p, prefix) || !strings.HasSuffix(p, "/diff") || path.Clean(p) != p {
		return false
	}
	return createdLayerSegment.MatchString(strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/diff"))
}
func createdLayerContext(ctx context.Context) error {
	if ctx == nil {
		return errCreatedLayerPhysical
	}
	return ctx.Err()
}
func createdLayerUUID(raw []byte) (out [16]byte, err error) {
	s := strings.TrimSuffix(string(raw), "\n")
	if !engineID.MatchString(s) {
		return out, errCreatedLayerPhysical
	}
	b, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if e != nil {
		return out, errCreatedLayerPhysical
	}
	copy(out[:], b)
	if out == [16]byte{} {
		return out, errCreatedLayerPhysical
	}
	return out, nil
}

type createdLayerMount struct {
	id           uint64
	major, minor uint32
}

// Parse the entire bounded table, not a longest-prefix guess. Reject ambiguous
// IDs, malformed lines/escapes, non-root XFS mounts and any descendant mount.
func createdLayerMountTable(raw []byte) (out createdLayerMount, err error) {
	if len(raw) == 0 || len(raw) > MaxResponse || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return out, errCreatedLayerPhysical
	}
	ids := map[uint64]bool{}
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Split(line, " ")
		if len(f) < 10 {
			return out, errCreatedLayerPhysical
		}
		sep := -1
		for i, s := range f {
			if s == "" {
				return out, errCreatedLayerPhysical
			}
			if s == "-" {
				if sep != -1 {
					return out, errCreatedLayerPhysical
				}
				sep = i
			}
		}
		if sep < 6 || len(f) != sep+4 {
			return out, errCreatedLayerPhysical
		}
		id, e := createdLayerDecimal(f[0], 64)
		if e != nil || id == 0 || ids[id] {
			return out, errCreatedLayerPhysical
		}
		ids[id] = true
		if _, e = createdLayerDecimal(f[1], 64); e != nil {
			return out, errCreatedLayerPhysical
		}
		dev := strings.Split(f[2], ":")
		if len(dev) != 2 {
			return out, errCreatedLayerPhysical
		}
		ma, e := createdLayerDecimal(dev[0], 32)
		if e != nil {
			return out, e
		}
		mi, e := createdLayerDecimal(dev[1], 32)
		if e != nil {
			return out, e
		}
		root, e := createdLayerMountPath(f[3])
		if e != nil {
			return out, e
		}
		point, e := createdLayerMountPath(f[4])
		if e != nil {
			return out, e
		}
		if _, e = createdLayerUnescape(f[sep+2]); e != nil {
			return out, e
		}
		if strings.HasPrefix(point, Root+"/") {
			return out, errCreatedLayerPhysical
		}
		if point == Root {
			if found || root != "/" || f[sep+1] != "xfs" {
				return out, errCreatedLayerPhysical
			}
			found = true
			out = createdLayerMount{id, uint32(ma), uint32(mi)}
		}
	}
	if !found {
		return out, errCreatedLayerPhysical
	}
	return out, nil
}
func createdLayerDecimal(s string, bits int) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errCreatedLayerPhysical
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errCreatedLayerPhysical
		}
	}
	n, e := strconv.ParseUint(s, 10, bits)
	if e != nil {
		return 0, errCreatedLayerPhysical
	}
	return n, nil
}
func createdLayerUnescape(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+3 >= len(s) {
			return "", errCreatedLayerPhysical
		}
		switch s[i+1 : i+4] {
		case "040":
			b.WriteByte(' ')
		case "011":
			b.WriteByte('\t')
		case "012":
			b.WriteByte('\n')
		case "134":
			b.WriteByte('\\')
		default:
			return "", errCreatedLayerPhysical
		}
		i += 3
	}
	return b.String(), nil
}
func createdLayerMountPath(s string) (string, error) {
	p, e := createdLayerUnescape(s)
	if e != nil || !path.IsAbs(p) || path.Clean(p) != p {
		return "", errCreatedLayerPhysical
	}
	return p, nil
}
func createdLayerFDInfo(raw []byte) (uint64, error) {
	if len(raw) == 0 || len(raw) > 4096 || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return 0, errCreatedLayerPhysical
	}
	var id uint64
	found := false
	for _, l := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		k, v, ok := strings.Cut(l, ":\t")
		if !ok || k == "" || v == "" {
			return 0, errCreatedLayerPhysical
		}
		if k == "mnt_id" {
			if found {
				return 0, errCreatedLayerPhysical
			}
			found = true
			var e error
			id, e = createdLayerDecimal(v, 64)
			if e != nil || id == 0 {
				return 0, errCreatedLayerPhysical
			}
		}
	}
	if !found {
		return 0, errCreatedLayerPhysical
	}
	return id, nil
}
func createdLayerGeometry(raw *[256]byte) (uuid [16]byte, err error) {
	copy(uuid[:], raw[64:80])
	b := binary.LittleEndian.Uint32(raw[0:4])
	if binary.LittleEndian.Uint32(raw[88:92]) != 5 || uuid == [16]byte{} || b < 512 || b > 65536 || b&(b-1) != 0 || binary.LittleEndian.Uint32(raw[12:16]) == 0 || binary.LittleEndian.Uint64(raw[32:40]) == 0 {
		return uuid, errCreatedLayerPhysical
	}
	for _, v := range raw[120:] {
		if v != 0 {
			return uuid, errCreatedLayerPhysical
		}
	}
	return uuid, nil
}
func createdLayerDirectoryIdentity(p string, m createdLayerMetadata, u [16]byte) CreatedLayerIdentity {
	return CreatedLayerIdentity{CanonicalPath: p, Inode: m.Inode, DeviceMajor: m.Major, DeviceMinor: m.Minor, Mode: m.Mode, UID: m.UID, GID: m.GID, MountID: m.MountID, XFSUUID: u}
}
func createdLayerMetadataValid(m createdLayerMetadata, mount createdLayerMount) bool {
	return m.Inode != 0 && m.Nlink != 0 && m.MountID == mount.id && m.Major == mount.major && m.Minor == mount.minor && m.Mode&0170000 == 0040000 && m.Mode&07022 == 0 && m.UID == 0 && m.GID == 0 && m.FSType == 0x58465342
}

func readCreatedLayerPhysical(ctx context.Context, upper string, k createdLayerKernel) (p *CreatedLayerPhysical, err error) {
	p = &CreatedLayerPhysical{}
	if !createdLayerUpperValid(upper) || createdLayerContext(ctx) != nil || k == nil {
		return p, createdLayerFailure("input", createdLayerContext(ctx))
	}
	ctx, cancel := context.WithTimeout(ctx, createdLayerWindow)
	defer cancel()
	// Every operation is synchronous: cancellation is cooperative, never a detached
	// goroutine racing a syscall or closing/reusing its FD and buffer.
	sample := func(op string, call func() error) error {
		started := time.Now().UTC()
		e := createdLayerContext(ctx)
		if e == nil {
			e = call()
		}
		if ce := createdLayerContext(ctx); ce != nil && e == nil {
			e = ce
		}
		s := CreatedLayerSample{Sequence: len(p.Samples) + 1, Operation: op, Source: "linux_syscall", Started: started, Ended: time.Now().UTC(), Outcome: "observed"}
		if e == nil && (!s.Ended.After(s.Started) || (len(p.Samples) > 0 && s.Started.Before(p.Samples[len(p.Samples)-1].Ended))) {
			e = errCreatedLayerPhysical
		}
		if e != nil {
			s.Outcome = "unavailable"
			var n syscall.Errno
			if errors.As(e, &n) {
				v := uint64(n)
				s.Errno = &v
			}
		}
		if len(p.Samples) < 64 {
			p.Samples = append(p.Samples, s)
		} else {
			p.SamplesTruncated = true
			e = errCreatedLayerPhysical
		}
		if e != nil {
			return createdLayerFailure(op, e)
		}
		return nil
	}
	if err = sample("boot_id_before", func() error {
		var e error
		p.BootIDBefore, e = k.boot(ctx)
		if e == nil && p.BootIDBefore == [16]byte{} {
			e = errCreatedLayerPhysical
		}
		return e
	}); err != nil {
		return
	}
	if err = sample("mount_namespace_before", func() error {
		var e error
		p.MountNamespaceBefore, e = k.namespace(ctx)
		if e == nil && p.MountNamespaceBefore == 0 {
			e = errCreatedLayerPhysical
		}
		return e
	}); err != nil {
		return
	}
	var mountRaw []byte
	var mount createdLayerMount
	if err = sample("mountinfo_before", func() error {
		var e error
		mountRaw, e = k.mountinfo(ctx)
		if e != nil {
			return e
		}
		p.MountInfoSHA256Before = sha256.Sum256(mountRaw)
		mount, e = createdLayerMountTable(mountRaw)
		return e
	}); err != nil {
		return
	}
	dirs := []*CreatedLayerDirectory{&p.Root, &p.Parent, &p.Diff}
	names := []string{"root", "parent", "diff"}
	paths := []string{Root, path.Dir(upper), upper}
	fds := [3]int{-1, -1, -1}
	var meta [3]createdLayerMetadata
	defer func() {
		for _, fd := range fds {
			if fd >= 0 {
				k.close(fd)
			}
		}
	}()
	for i, d := range dirs {
		if err = sample(names[i]+"_open", func() error {
			var e error
			fds[i], e = k.open(ctx, paths[i])
			if e == nil && fds[i] < 0 {
				e = errCreatedLayerPhysical
			}
			return e
		}); err != nil {
			return
		}
		d.NoFollowWalk = true
		if err = sample(names[i]+"_stat", func() error {
			var e error
			meta[i], e = k.stat(ctx, fds[i])
			d.Before = createdLayerDirectoryIdentity(paths[i], meta[i], [16]byte{})
			if e == nil && !createdLayerMetadataValid(meta[i], mount) {
				e = errCreatedLayerPhysical
			}
			return e
		}); err != nil {
			return
		}
		if err = sample(names[i]+"_geometry", func() error {
			if e := k.geometry(ctx, fds[i], &d.Geometry); e != nil {
				return e
			}
			u, e := createdLayerGeometry(&d.Geometry)
			d.Before.XFSUUID = u
			if e == nil && i > 0 && dirs[0].Geometry != d.Geometry {
				e = errCreatedLayerPhysical
			}
			return e
		}); err != nil {
			return
		}
		d.FilesystemUUIDSameFD = true
		if err = sample(names[i]+"_fsxattr", func() error {
			if e := k.attrs(ctx, fds[i], &d.FSXAttr); e != nil {
				return e
			}
			if i == 2 {
				// Dynamic target comes from actual parent/diff, never config or CID.
				p.ProjectID = binary.LittleEndian.Uint32(p.Diff.FSXAttr[12:16])
				id := p.ProjectID
				if id == 0 || id == 100 || id == 101 || id == 999 || id != binary.LittleEndian.Uint32(p.Parent.FSXAttr[12:16]) {
					return errCreatedLayerPhysical
				}
				for _, flags := range []uint32{binary.LittleEndian.Uint32(p.Parent.FSXAttr[0:4]), binary.LittleEndian.Uint32(p.Diff.FSXAttr[0:4])} {
					if flags&createdLayerXFlagProjInherit == 0 || flags&(createdLayerXFlagRealtime|createdLayerXFlagRTInherit) != 0 {
						return errCreatedLayerPhysical
					}
				}
			}
			return nil
		}); err != nil {
			return
		}
	}
	p.RootXFSVerified = true
	p.SameMountVerified = true
	p.QStatV[0] = 1
	if err = sample("qstatv", func() error {
		if e := k.quota(ctx, fds[1], 0, true, p.QStatV[:]); e != nil {
			return e
		}
		if p.QStatV[0] != 1 {
			return errCreatedLayerPhysical
		}
		return nil
	}); err != nil {
		return
	}
	for i, q := range []*[112]byte{&p.TargetQuota, &p.DefaultQuota} {
		id := p.ProjectID
		op := "target_quota"
		if i == 1 {
			id = 0
			op = "default0_quota"
		}
		if err = sample(op, func() error {
			if e := k.quota(ctx, fds[1], id, false, q[:]); e != nil {
				return e
			}
			if q[0] != 1 || q[1] != 2 || binary.LittleEndian.Uint32(q[4:8]) != id {
				return errCreatedLayerPhysical
			}
			for _, b := range q[72:104] {
				if b != 0 {
					return errCreatedLayerPhysical
				}
			}
			return nil
		}); err != nil {
			return
		}
	}
	if err = sample("boot_id_after", func() error {
		var e error
		p.BootIDAfter, e = k.boot(ctx)
		if e == nil && p.BootIDAfter != p.BootIDBefore {
			e = errCreatedLayerPhysical
		}
		return e
	}); err != nil {
		return
	}
	p.BootIdentityRechecked = true
	if err = sample("mount_namespace_after", func() error {
		var e error
		p.MountNamespaceAfter, e = k.namespace(ctx)
		if e == nil && p.MountNamespaceAfter != p.MountNamespaceBefore {
			e = errCreatedLayerPhysical
		}
		return e
	}); err != nil {
		return
	}
	p.MountNamespaceRechecked = true
	if err = sample("mountinfo_after", func() error {
		raw, e := k.mountinfo(ctx)
		if e != nil {
			return e
		}
		p.MountInfoSHA256After = sha256.Sum256(raw)
		if _, e = createdLayerMountTable(raw); e != nil {
			return e
		}
		if !bytes.Equal(raw, mountRaw) {
			return errCreatedLayerPhysical
		}
		return nil
	}); err != nil {
		return
	}
	p.MountInfoRechecked = true
	for i, d := range dirs {
		// Held FD is checked again, then canonical path is independently walked and
		// compared. An unlinked held inode alone cannot hide a path replacement.
		if err = sample(names[i]+"_stat", func() error {
			m, e := k.stat(ctx, fds[i])
			d.After = createdLayerDirectoryIdentity(paths[i], m, [16]byte{})
			if e == nil && m != meta[i] {
				e = errCreatedLayerPhysical
			}
			return e
		}); err != nil {
			return
		}
		if err = sample(names[i]+"_geometry", func() error {
			if e := k.geometry(ctx, fds[i], &d.GeometryAfter); e != nil {
				return e
			}
			u, e := createdLayerGeometry(&d.GeometryAfter)
			d.After.XFSUUID = u
			if e == nil && d.GeometryAfter != d.Geometry {
				e = errCreatedLayerPhysical
			}
			return e
		}); err != nil {
			return
		}
		if err = sample(names[i]+"_fsxattr", func() error {
			if e := k.attrs(ctx, fds[i], &d.FSXAttrAfter); e != nil {
				return e
			}
			if d.FSXAttrAfter != d.FSXAttr {
				return errCreatedLayerPhysical
			}
			return nil
		}); err != nil {
			return
		}
		if err = sample(names[i]+"_reopen", func() error {
			fd, e := k.open(ctx, paths[i])
			if fd >= 0 {
				defer k.close(fd)
			}
			if e != nil {
				return e
			}
			if fd < 0 {
				return errCreatedLayerPhysical
			}
			d.ReopenedNoFollow = true
			if e = createdLayerContext(ctx); e != nil {
				return e
			}
			m, e := k.stat(ctx, fd)
			if e != nil {
				return e
			}
			if m != meta[i] {
				return errCreatedLayerPhysical
			}
			if e = createdLayerContext(ctx); e != nil {
				return e
			}
			var g [256]byte
			if e = k.geometry(ctx, fd, &g); e != nil {
				return e
			}
			if e = createdLayerContext(ctx); e != nil {
				return e
			}
			if g != d.Geometry {
				return errCreatedLayerPhysical
			}
			var a [28]byte
			if e = k.attrs(ctx, fd, &a); e != nil {
				return e
			}
			if e = createdLayerContext(ctx); e != nil {
				return e
			}
			if a != d.FSXAttr {
				return errCreatedLayerPhysical
			}
			return nil
		}); err != nil {
			return
		}
		d.IdentityRechecked = true
	}
	p.ObservationComplete = !p.SamplesTruncated
	return
}
