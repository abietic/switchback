package capacityprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"path"
	"strconv"
	"strings"
	"time"
)

// Finite synchronous operations only. No method accepts observed success DTOs.
// All descriptors and arrays belong to one collectPool invocation.
type poolKernel interface {
	boot(context.Context) ([16]byte, error)
	namespace(context.Context, bool, string) (uint64, error)
	mountinfo(context.Context) ([]byte, error)
	open(context.Context, string) (int, error)
	procOpen(context.Context, uint64) (int, error) // 0 means self, never caller-selected text
	close(int)
	stat(context.Context, int) (createdLayerMetadata, error)
	geometry(context.Context, int, *[256]byte) error
	attrs(context.Context, int, *[28]byte) error
	quota(context.Context, int, uint32, bool, []byte) error
	statfs(context.Context, int) (PoolStatFSReadback, error)
	control(context.Context, int, string) ([]byte, error)
	process(context.Context, int, string) ([]byte, error)
}

func poolInside(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }
func poolCanonical(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\r\n\t\\ ") && len(p) <= 4096
}
func poolPlan(plan PoolPlan) (PoolPlan, error) {
	out := PoolPlan{AnchorPath: plan.AnchorPath}
	if !poolCanonical(plan.AnchorPath) || !strings.HasPrefix(plan.AnchorPath, Root+"/") || len(plan.Core) == 0 || len(plan.Core) > 32 || len(plan.Storage) == 0 || len(plan.Storage) > 32 {
		return out, ErrPoolInvalid
	}
	ids, paths, cids := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range plan.Core {
		if c.ID == "" || len(c.ID) > 160 || ids[c.ID] || !poolCanonical(c.CgroupPath) || !poolInside(c.CgroupPath, poolCoreRoot) || paths[c.CgroupPath] || len(c.ContainerIDs) == 0 || len(c.ContainerIDs) > 32 {
			return out, ErrPoolInvalid
		}
		for p := range paths {
			if poolInside(p, c.CgroupPath) || poolInside(c.CgroupPath, p) {
				return out, ErrPoolInvalid
			}
		}
		ids[c.ID] = true
		paths[c.CgroupPath] = true
		for _, id := range c.ContainerIDs {
			if !hexID.MatchString(id) || cids[id] {
				return out, ErrPoolInvalid
			}
			cids[id] = true
		}
		out.Core = append(out.Core, PoolCorePlan{c.ID, c.CgroupPath, append([]string(nil), c.ContainerIDs...)})
	}
	if len(cids) > 32 {
		return out, ErrPoolInvalid
	}
	ids = map[string]bool{}
	paths = map[string]bool{}
	for _, s := range plan.Storage {
		if s.ID == "" || len(s.ID) > 160 || ids[s.ID] || !poolCanonical(s.Path) || !strings.HasPrefix(s.Path, Root+"/") || poolInside(s.Path, plan.AnchorPath) || poolInside(plan.AnchorPath, s.Path) {
			return out, ErrPoolInvalid
		}
		for p := range paths {
			if poolInside(p, s.Path) || poolInside(s.Path, p) {
				return out, ErrPoolInvalid
			}
		}
		ids[s.ID] = true
		paths[s.Path] = true
		out.Storage = append(out.Storage, s)
	}
	return out, nil
}
func poolProcess(rawStat, rawGroup []byte, pid uint64) (uint64, string, error) {
	if len(rawStat) == 0 || len(rawStat) > 4096 || bytes.IndexByte(rawStat, 0) >= 0 {
		return 0, "", ErrPoolInvalid
	}
	s := strings.TrimSuffix(string(rawStat), "\n")
	a := strings.Index(s, " (")
	b := strings.LastIndex(s, ") ")
	if a < 1 || b < a+2 {
		return 0, "", ErrPoolInvalid
	}
	n, e := createdLayerDecimal(s[:a], 64)
	if e != nil || (pid != 0 && n != pid) || n == 0 {
		return 0, "", ErrPoolInvalid
	}
	fields := strings.Fields(s[b+2:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, "", ErrPoolInvalid
	}
	ticks, e := createdLayerDecimal(fields[19], 64)
	// PID 1 may start in tick zero; retain it only for an explicit PID 1 binding.
	if e != nil || (ticks == 0 && pid != 1) {
		return 0, "", ErrPoolInvalid
	}
	g, e := poolUnified(rawGroup)
	if e != nil {
		return 0, "", e
	}
	return ticks, g, nil
}
func poolUnified(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return "", ErrPoolInvalid
	}
	s := strings.TrimSuffix(string(raw), "\n")
	if !strings.HasPrefix(s, "0::") || strings.Contains(s, "\n") {
		return "", ErrPoolInvalid
	}
	p := strings.TrimPrefix(s, "0::")
	if !poolCanonical(p) {
		return "", ErrPoolInvalid
	}
	return "/sys/fs/cgroup" + strings.TrimSuffix(p, "/"), nil
}

type poolMountRecord struct {
	id                uint64
	major, minor      uint32
	root, point, kind string
	options           []string
}

// Parse the entire bounded table, then bind each sampled path AND its actual FD
// mount id/device. An overlay merged sibling does not replace its XFS diff.
func poolMountRecords(raw []byte) ([]poolMountRecord, error) {
	if len(raw) == 0 || len(raw) > MaxResponse || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return nil, ErrPoolInvalid
	}
	out := []poolMountRecord{}
	ids := map[uint64]bool{}
	points := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Split(line, " ")
		sep := -1
		for n, v := range f {
			if v == "" {
				return nil, ErrPoolInvalid
			}
			if v == "-" {
				if sep != -1 {
					return nil, ErrPoolInvalid
				}
				sep = n
			}
		}
		if sep < 6 || len(f) != sep+4 {
			return nil, ErrPoolInvalid
		}
		id, e := createdLayerDecimal(f[0], 64)
		if e != nil || id == 0 || ids[id] {
			return nil, ErrPoolInvalid
		}
		ids[id] = true
		parent, e := createdLayerDecimal(f[1], 64)
		if e != nil || parent == 0 {
			return nil, ErrPoolInvalid
		}
		dev := strings.Split(f[2], ":")
		if len(dev) != 2 {
			return nil, ErrPoolInvalid
		}
		ma, e := createdLayerDecimal(dev[0], 32)
		if e != nil {
			return nil, ErrPoolInvalid
		}
		mi, e := createdLayerDecimal(dev[1], 32)
		if e != nil {
			return nil, ErrPoolInvalid
		}
		root, e := createdLayerMountPath(f[3])
		if e != nil {
			return nil, ErrPoolInvalid
		}
		point, e := createdLayerMountPath(f[4])
		if e != nil || points[point] {
			return nil, ErrPoolInvalid
		}
		points[point] = true
		if _, e = createdLayerUnescape(f[sep+2]); e != nil {
			return nil, ErrPoolInvalid
		}
		r := poolMountRecord{id: id, major: uint32(ma), minor: uint32(mi), root: root, point: point, kind: f[sep+1]}
		seen := map[string]bool{}
		for _, opts := range []string{f[5], f[sep+3]} {
			for _, v := range strings.Split(opts, ",") {
				if v == "" || len(v) > 512 || strings.ContainsAny(v, "\x00\r\n\t") {
					return nil, ErrPoolInvalid
				}
				if !seen[v] {
					r.options = append(r.options, v)
					seen[v] = true
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func poolMountAt(records []poolMountRecord, p string) (poolMountRecord, error) {
	var out poolMountRecord
	found := false
	for _, r := range records {
		if (r.point == "/" || poolInside(p, r.point)) && (!found || len(r.point) > len(out.point)) {
			out = r
			found = true
		}
	}
	if !found {
		return out, ErrPoolInvalid
	}
	return out, nil
}
func poolMount(raw []byte) (createdLayerMount, []string, error) {
	records, e := poolMountRecords(raw)
	if e != nil {
		return createdLayerMount{}, nil, e
	}
	r, e := poolMountAt(records, Root)
	if e != nil || r.point != Root || r.root != "/" || r.kind != "xfs" {
		return createdLayerMount{}, nil, ErrPoolInvalid
	}
	cg, e := poolMountAt(records, "/sys/fs/cgroup")
	if e != nil || cg.point != "/sys/fs/cgroup" || cg.root != "/" || cg.kind != "cgroup2" {
		return createdLayerMount{}, nil, ErrPoolInvalid
	}
	return createdLayerMount{r.id, r.major, r.minor}, append([]string(nil), r.options...), nil
}
func poolPIDText(pid uint64) string { return strconv.FormatUint(pid, 10) }

// Held resources are closed synchronously on every exit, including late context
// failures. Reopens get independent descriptors and never replace held ones.
type poolRead struct {
	ctx             context.Context
	k               poolKernel
	now             func() time.Time
	clock           time.Time
	fds             []int
	mount           createdLayerMount
	geometry        [256]byte
	inodes          map[uint64]bool
	cgroupNamespace uint64
	cgroupMount     createdLayerMount
	mounts          []poolMountRecord
}
type poolDirectory struct {
	fd    int
	path  string
	meta  createdLayerMetadata
	value CreatedLayerDirectory
}
type poolGroup struct {
	fd    int
	meta  createdLayerMetadata
	value PoolCgroupReadback
}
type poolProc struct {
	fd    int
	cid   string
	pid   uint64
	ticks uint64
	group string
}

func (r *poolRead) call(f func() error) error { return poolCall(r.ctx, f) }
func (r *poolRead) time() (time.Time, error) {
	t := r.now().UTC()
	if t.IsZero() || (!r.clock.IsZero() && !t.After(r.clock)) {
		return t, ErrPoolInvalid
	}
	r.clock = t
	return t, nil
}
func (r *poolRead) hold(open func() (int, error)) (fd int, e error) {
	fd = -1
	e = r.call(func() error {
		var er error
		fd, er = open()
		if fd >= 0 {
			r.fds = append(r.fds, fd)
		}
		if er == nil && fd < 0 {
			return ErrPoolInvalid
		}
		return er
	})
	return
}
func (r *poolRead) close() {
	for i := len(r.fds) - 1; i >= 0; i-- {
		r.k.close(r.fds[i])
	}
}
func poolStable(a, b createdLayerMetadata) bool {
	a.CtimeSec, b.CtimeSec = 0, 0
	a.CtimeNsec, b.CtimeNsec = 0, 0
	a.Nlink, b.Nlink = 0, 0
	return a == b
}
func poolAttrs(a [28]byte) bool {
	for _, b := range a[20:] {
		if b != 0 {
			return false
		}
	}
	flags := binary.LittleEndian.Uint32(a[:4])
	return flags&(createdLayerXFlagRealtime|createdLayerXFlagRTInherit) == 0
}

// fsx_nextents [8:12] is a read-only extent count that can change during
// ordinary directory writes. Compare every other attribute, including configured
// extent sizes, project, xflags and reserved bytes; keep both raw arrays intact.
func poolStableAttrs(a, b [28]byte) bool {
	return bytes.Equal(a[:8], b[:8]) && bytes.Equal(a[12:], b[12:])
}

func (r *poolRead) directory(p string) (d poolDirectory, e error) {
	d.path = p
	selected, er := poolMountAt(r.mounts, p)
	if er != nil || selected.id != r.mount.id || selected.major != r.mount.major || selected.minor != r.mount.minor || selected.kind != "xfs" {
		return d, ErrPoolInvalid
	}
	d.fd, e = r.hold(func() (int, error) { return r.k.open(r.ctx, p) })
	if e != nil {
		return
	}
	if e = r.call(func() error { var er error; d.meta, er = r.k.stat(r.ctx, d.fd); return er }); e != nil {
		return
	}
	if !createdLayerMetadataValid(d.meta, r.mount) || r.inodes[d.meta.Inode] {
		return d, ErrPoolInvalid
	}
	r.inodes[d.meta.Inode] = true
	if e = r.call(func() error { return r.k.geometry(r.ctx, d.fd, &d.value.Geometry) }); e != nil {
		return
	}
	u, e := createdLayerGeometry(&d.value.Geometry)
	if e != nil {
		return d, ErrPoolInvalid
	}
	if r.geometry == [256]byte{} {
		r.geometry = d.value.Geometry
	} else if r.geometry != d.value.Geometry {
		return d, ErrPoolInvalid
	}
	if e = r.call(func() error { return r.k.attrs(r.ctx, d.fd, &d.value.FSXAttr) }); e != nil {
		return
	}
	if !poolAttrs(d.value.FSXAttr) {
		return d, ErrPoolInvalid
	}
	d.value.Before = createdLayerDirectoryIdentity(p, d.meta, u)
	d.value.NoFollowWalk = true
	d.value.FilesystemUUIDSameFD = true
	return
}
func (r *poolRead) directoryAfter(d *poolDirectory) error {
	var m createdLayerMetadata
	if e := r.call(func() error { var er error; m, er = r.k.stat(r.ctx, d.fd); return er }); e != nil {
		return e
	}
	if !createdLayerMetadataValid(m, r.mount) || !poolStable(m, d.meta) {
		return ErrPoolInvalid
	}
	if e := r.call(func() error { return r.k.geometry(r.ctx, d.fd, &d.value.GeometryAfter) }); e != nil {
		return e
	}
	if d.value.GeometryAfter != d.value.Geometry {
		return ErrPoolInvalid
	}
	if e := r.call(func() error { return r.k.attrs(r.ctx, d.fd, &d.value.FSXAttrAfter) }); e != nil {
		return e
	}
	if !poolStableAttrs(d.value.FSXAttrAfter, d.value.FSXAttr) {
		return ErrPoolInvalid
	}
	d.value.After = createdLayerDirectoryIdentity(d.path, m, d.value.Before.XFSUUID)
	fd := -1
	e := r.call(func() error { var er error; fd, er = r.k.open(r.ctx, d.path); return er })
	if fd >= 0 {
		defer r.k.close(fd)
	}
	if ce := r.ctx.Err(); ce != nil {
		return ce
	}
	if e != nil {
		return e
	}
	if fd < 0 {
		return ErrPoolInvalid
	}
	if e = r.call(func() error { var er error; m, er = r.k.stat(r.ctx, fd); return er }); e != nil {
		return e
	}
	if !createdLayerMetadataValid(m, r.mount) || !poolStable(m, d.meta) {
		return ErrPoolInvalid
	}
	var g [256]byte
	var a [28]byte
	if e = r.call(func() error { return r.k.geometry(r.ctx, fd, &g) }); e != nil {
		return e
	}
	if g != d.value.Geometry {
		return ErrPoolInvalid
	}
	if e = r.call(func() error { return r.k.attrs(r.ctx, fd, &a) }); e != nil {
		return e
	}
	if !poolStableAttrs(a, d.value.FSXAttr) {
		return ErrPoolInvalid
	}
	d.value.ReopenedNoFollow = true
	d.value.IdentityRechecked = true
	return nil
}
func poolGroupID(m createdLayerMetadata) PoolCgroupIdentity {
	return PoolCgroupIdentity{uint64(m.FSType), uint64(m.Major)<<32 | uint64(m.Minor), m.Inode}
}
func poolGroupValid(m createdLayerMetadata) bool {
	return m.FSType == 0x63677270 && m.Inode != 0 && (m.Major != 0 || m.Minor != 0) && m.Mode&0170000 == 0040000 && m.Mode&07022 == 0 && m.UID == 0 && m.GID == 0 && m.Nlink > 0
}
func (r *poolRead) control(fd int) (f PoolCgroupFiles, e error) {
	for i, n := range []string{"cpu.max", "memory.max", "memory.swap.max", "pids.max", "cgroup.type"} {
		var raw []byte
		e = r.call(func() error { var er error; raw, er = r.k.control(r.ctx, fd, n); return er })
		if e != nil {
			return
		}
		if len(raw) == 0 || len(raw) > 128 || raw[len(raw)-1] != '\n' || bytes.ContainsAny(raw, "\x00\r") {
			return f, ErrPoolInvalid
		}
		s := string(raw)
		switch i {
		case 0:
			f.CPU = s
		case 1:
			f.Memory = s
		case 2:
			f.Swap = s
		case 3:
			f.PIDs = s
		case 4:
			f.Type = s
		}
	}
	return
}
func (r *poolRead) group(p string) (g poolGroup, e error) {
	g.value.Path = p
	selected, er := poolMountAt(r.mounts, p)
	if er != nil || selected.id != r.cgroupMount.id || selected.major != r.cgroupMount.major || selected.minor != r.cgroupMount.minor || selected.kind != "cgroup2" {
		return g, ErrPoolInvalid
	}
	g.fd, e = r.hold(func() (int, error) { return r.k.open(r.ctx, p) })
	if e != nil {
		return
	}
	e = r.call(func() error { var er error; g.meta, er = r.k.stat(r.ctx, g.fd); return er })
	if e != nil {
		return
	}
	if !r.groupValid(g.meta) {
		return g, ErrPoolInvalid
	}
	g.value.Before = poolGroupID(g.meta)
	g.value.FilesBefore, e = r.control(g.fd)
	return
}
func (r *poolRead) groupAfter(g *poolGroup) error {
	var m createdLayerMetadata
	if e := r.call(func() error { var er error; m, er = r.k.stat(r.ctx, g.fd); return er }); e != nil {
		return e
	}
	if !r.groupValid(m) || !poolStable(m, g.meta) {
		return ErrPoolInvalid
	}
	g.value.After = poolGroupID(m)
	f, e := r.control(g.fd)
	if e != nil {
		return e
	}
	if f != g.value.FilesBefore {
		return ErrPoolInvalid
	}
	g.value.FilesAfter = f
	fd := -1
	e = r.call(func() error { var er error; fd, er = r.k.open(r.ctx, g.value.Path); return er })
	if fd >= 0 {
		defer r.k.close(fd)
	}
	if ce := r.ctx.Err(); ce != nil {
		return ce
	}
	if e != nil {
		return e
	}
	if fd < 0 {
		return ErrPoolInvalid
	}
	if e = r.call(func() error { var er error; m, er = r.k.stat(r.ctx, fd); return er }); e != nil {
		return e
	}
	if !poolStable(m, g.meta) || !r.groupValid(m) {
		return ErrPoolInvalid
	}
	return nil
}
func (r *poolRead) processRead(fd int, pid uint64) (ticks uint64, g string, e error) {
	var stat, group []byte
	if e = r.call(func() error { var er error; stat, er = r.k.process(r.ctx, fd, "stat"); return er }); e != nil {
		return
	}
	if e = r.call(func() error { var er error; group, er = r.k.process(r.ctx, fd, "cgroup"); return er }); e != nil {
		return
	}
	ticks, g, e = poolProcess(stat, group, pid)
	if e != nil {
		return
	}
	selected, er := poolMountAt(r.mounts, g)
	if er != nil || selected.id != r.cgroupMount.id || selected.kind != "cgroup2" {
		return 0, "", ErrPoolInvalid
	}
	return
}
func (r *poolRead) proc(cid string, pid uint64) (p poolProc, e error) {
	p.cid, p.pid = cid, pid
	p.fd, e = r.hold(func() (int, error) { return r.k.procOpen(r.ctx, pid) })
	if e != nil {
		return
	}
	p.ticks, p.group, e = r.processRead(p.fd, pid)
	return
}
func (r *poolRead) procAfter(p poolProc) (v PoolProcessReadback, e error) {
	t, g, e := r.processRead(p.fd, p.pid)
	if e != nil {
		return v, e
	}
	if t != p.ticks || g != p.group {
		return v, ErrPoolInvalid
	}
	// /proc held FDs alone do not prove the canonical PID was not reused.
	fd := -1
	e = r.call(func() error { var er error; fd, er = r.k.procOpen(r.ctx, p.pid); return er })
	if fd >= 0 {
		defer r.k.close(fd)
	}
	if ce := r.ctx.Err(); ce != nil {
		return v, ce
	}
	if e != nil {
		return v, e
	}
	if fd < 0 {
		return v, ErrPoolInvalid
	}
	t, g, e = r.processRead(fd, p.pid)
	if e != nil {
		return v, e
	}
	if t != p.ticks || g != p.group {
		return v, ErrPoolInvalid
	}
	return PoolProcessReadback{p.cid, p.pid, p.pid, p.ticks, t, p.group, g}, nil
}
func (r *poolRead) quota(d poolDirectory) (q PoolQuotaReadback, e error) {
	id := binary.LittleEndian.Uint32(d.value.FSXAttr[12:16])
	if id == 0 || binary.LittleEndian.Uint32(d.value.FSXAttr[:4])&createdLayerXFlagProjInherit == 0 {
		return q, ErrPoolInvalid
	}
	var raw [112]byte
	q.QStatV[0] = 1
	for i, op := range []string{"qstatv", "target_quota", "default0_quota"} {
		start, er := r.time()
		if er != nil {
			return q, er
		}
		switch i {
		case 0:
			e = r.call(func() error { return r.k.quota(r.ctx, d.fd, 0, true, q.QStatV[:]) })
		case 1:
			e = r.call(func() error { return r.k.quota(r.ctx, d.fd, id, false, raw[:]) })
		case 2:
			e = r.call(func() error { return r.k.quota(r.ctx, d.fd, 0, false, q.Default0[:]) })
		}
		if e != nil {
			return q, e
		}
		end, er := r.time()
		if er != nil {
			return q, er
		}
		q.StateSamples = append(q.StateSamples, CreatedLayerSample{Sequence: i + 1, Operation: op, Source: "linux_syscall", Started: start, Ended: end, Outcome: "observed"})
	}
	flags := binary.LittleEndian.Uint16(q.QStatV[2:4])
	if q.QStatV[0] != 1 || flags&0x30 != 0x30 || flags & ^uint16(0x3f) != 0 {
		return q, ErrPoolInvalid
	}
	for i, b := range q.QStatV {
		if (i == 1 || (i >= 28 && i < 32) || (i >= 52 && i < 56) || (i >= 76 && i < 80) || i >= 98) && b != 0 {
			return q, ErrPoolInvalid
		}
	}
	// dqblk_xfs.h:71,102:112 are padding. Timers, warning counts and
	// realtime quantities are semantic fields; retain them byte-for-byte.
	for _, b := range [][112]byte{raw, q.Default0} {
		if b[71] != 0 {
			return q, ErrPoolInvalid
		}
		for _, v := range b[102:] {
			if v != 0 {
				return q, ErrPoolInvalid
			}
		}
	}
	q.Target, e = parseQuota(raw[:], id)
	if e != nil {
		return q, ErrPoolInvalid
	}
	if _, e = parseQuota(q.Default0[:], 0); e != nil {
		return q, ErrPoolInvalid
	}
	return q, nil
}
func (r *poolRead) global() (boot [16]byte, ns uint64, mount []byte, e error) {
	if e = r.call(func() error { var er error; boot, er = r.k.boot(r.ctx); return er }); e != nil {
		return
	}
	if boot == [16]byte{} {
		return boot, 0, nil, ErrPoolInvalid
	}
	for _, kind := range []string{"mnt", "cgroup"} {
		var self, one uint64
		if e = r.call(func() error { var er error; self, er = r.k.namespace(r.ctx, false, kind); return er }); e != nil {
			return
		}
		if e = r.call(func() error { var er error; one, er = r.k.namespace(r.ctx, true, kind); return er }); e != nil {
			return
		}
		if self == 0 || self != one {
			return boot, 0, nil, ErrPoolInvalid
		}
		if kind == "mnt" {
			ns = self
		} else {
			if r.cgroupNamespace != 0 && r.cgroupNamespace != self {
				return boot, 0, nil, ErrPoolInvalid
			}
			r.cgroupNamespace = self
		}
	}
	e = r.call(func() error { var er error; mount, er = r.k.mountinfo(r.ctx); return er })
	return
}
func poolStatFSValid(s PoolStatFSReadback) bool {
	if s.Type != 0x58465342 || s.BlockSize < 512 || s.BlockSize > 65536 || s.BlockSize&(s.BlockSize-1) != 0 || s.FragmentSize != s.BlockSize || s.Blocks == 0 || s.Free > s.Blocks || s.Available > s.Free {
		return false
	}
	_, e := Scale(s.Blocks, s.FragmentSize)
	return e == nil
}

func poolCgroupMount(raw []byte) (createdLayerMount, error) {
	records, e := poolMountRecords(raw)
	if e != nil {
		return createdLayerMount{}, e
	}
	cg, e := poolMountAt(records, "/sys/fs/cgroup")
	if e != nil || cg.point != "/sys/fs/cgroup" || cg.root != "/" || cg.kind != "cgroup2" {
		return createdLayerMount{}, ErrPoolInvalid
	}
	return createdLayerMount{cg.id, cg.major, cg.minor}, nil
}
func (r *poolRead) groupValid(m createdLayerMetadata) bool {
	return poolGroupValid(m) && m.MountID == r.cgroupMount.id && m.Major == r.cgroupMount.major && m.Minor == r.cgroupMount.minor
}
