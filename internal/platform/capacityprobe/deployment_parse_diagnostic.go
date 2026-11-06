package capacityprobe

import "context"

func deploymentParseFailure(v *DeploymentFailureObservation) *DeploymentParseFailureObservation {
	if v == nil {
		return nil
	}
	out := &DeploymentParseFailureObservation{Schema: DeploymentParseFailureSchema, Kind: v.Kind, Stage: v.Stage, ErrorClass: v.ErrorClass}
	if v.LastPrimitive != nil {
		p := *v.LastPrimitive
		if p.Errno != nil {
			n := *p.Errno
			p.Errno = &n
		}
		out.LastPrimitive = &p
	}
	return out
}

func observeDeploymentParseDiagnostic(ctx context.Context, backend deploymentBackend) (DeploymentObservation, *DeploymentParseFailureObservation) {
	// Check the original interface before wrapping so a wrapper cannot hide nil.
	if ctx == nil || backend == nil {
		return DeploymentObservation{}, deploymentParseFailure(deploymentDiagnosticFailure("entry", "invalid"))
	}
	b := &deploymentParseBackend{deploymentBackend: backend, mappings: make(map[int]deploymentParseFD)}
	out, failure := observeDeploymentDiagnostic(ctx, b)
	v := deploymentParseFailure(failure)
	if v != nil {
		v.ParseChecks = b.checks(v)
	}
	return out, v
}

const deploymentParseMapLimit = 16
const deploymentParseReadLimit = 4096

type deploymentParseFD struct {
	owner   deploymentOwner
	proc    uint64
	attempt uint64
	leaf    deploymentLeaf
	isProc  bool
}
type deploymentParseSlot struct {
	attempt, proc uint64
	stat, group   []byte
	groupShape    string
	// Only the ordered successful stat -> cgroup pair is eligible.
	statRead, groupRead bool
}

// This decorator never owns an FD and never changes a backend return value.
// Two replaceable owner slots retain at most 4 * 4096 copied bytes.
type deploymentParseBackend struct {
	deploymentBackend
	mappings        map[int]deploymentParseFD
	slots           [2]deploymentParseSlot
	sequence        uint64
	tid             uint64
	idsOK, disabled bool
	last            deploymentParseFD
	lastGroup       bool
}

func (b *deploymentParseBackend) ids(ctx context.Context) (uint64, uint64, error) {
	p, t, e := b.deploymentBackend.ids(ctx)
	b.idsOK = e == nil && p > 0 && t > 0 && p <= 1<<32-1 && t <= 1<<32-1
	b.tid = t
	return p, t, e
}
func (b *deploymentParseBackend) track(fd int, m deploymentParseFD) {
	if fd < 0 || b.disabled {
		return
	}
	if b.mappings == nil {
		b.mappings = make(map[int]deploymentParseFD)
	}
	if _, ok := b.mappings[fd]; !ok && len(b.mappings) >= deploymentParseMapLimit {
		b.disabled = true
		b.mappings = nil
		b.slots = [2]deploymentParseSlot{}
		b.lastGroup = false
		return
	}
	b.mappings[fd] = m
}
func (b *deploymentParseBackend) openProc(ctx context.Context, o deploymentOwner) (int, error) {
	fd, e := b.deploymentBackend.openProc(ctx, o)
	if o <= deploymentPID1 {
		b.sequence++
		b.track(fd, deploymentParseFD{owner: o, proc: b.sequence, isProc: true})
	}
	return fd, e
}
func (b *deploymentParseBackend) openLeaf(ctx context.Context, parent int, l deploymentLeaf) (int, error) {
	m, ok := b.mappings[parent]
	ok = ok && m.isProc && m.owner <= deploymentPID1 && !b.disabled
	// Even a failed status open begins a new attempt and invalidates old bytes.
	if ok && l == deploymentStatus {
		b.sequence++
		b.slots[m.owner] = deploymentParseSlot{attempt: b.sequence, proc: m.proc}
	}
	fd, e := b.deploymentBackend.openLeaf(ctx, parent, l)
	if ok {
		s := b.slots[m.owner]
		b.track(fd, deploymentParseFD{owner: m.owner, proc: m.proc, attempt: s.attempt, leaf: l})
	}
	return fd, e
}
func (b *deploymentParseBackend) read(ctx context.Context, fd, limit int) ([]byte, error) {
	raw, e := b.deploymentBackend.read(ctx, fd, limit)
	b.lastGroup = false
	m, ok := b.mappings[fd]
	if !ok || m.isProc || b.disabled || m.owner > deploymentPID1 {
		return raw, e
	}
	s := &b.slots[m.owner]
	if m.attempt == 0 || s.attempt != m.attempt || s.proc != m.proc {
		return raw, e
	}
	switch m.leaf {
	case deploymentStat:
		s.stat = nil
		s.group = nil
		s.statRead = false
		s.groupRead = false
		s.groupShape = ""
		if e == nil && len(raw) > 0 && len(raw) <= deploymentParseReadLimit {
			s.stat = append([]byte(nil), raw...)
			s.statRead = true
		}
	case deploymentCgroup:
		s.group = nil
		s.groupRead = false
		s.groupShape = ""
		if e == nil && s.statRead {
			s.groupRead = true
			switch {
			case len(raw) == 0:
				s.groupShape = "empty"
			case len(raw) > deploymentParseReadLimit:
				s.groupShape = "oversize"
			default:
				s.groupShape = "bounded"
				s.group = append([]byte(nil), raw...)
			}
			b.last = m
			b.lastGroup = true
		}
	}
	return raw, e
}
func (b *deploymentParseBackend) close(fd int) {
	b.deploymentBackend.close(fd)
	delete(b.mappings, fd)
}
func (b *deploymentParseBackend) checks(v *DeploymentParseFailureObservation) *DeploymentProcessParseChecks {
	p := v.LastPrimitive
	if b.disabled || !b.idsOK || !b.lastGroup || p == nil || v.Stage != "observe" || v.ErrorClass != "invalid" || p.Operation != "read" || p.Outcome != "returned" || p.Errno != nil {
		return nil
	}
	m := b.last
	s := b.slots[m.owner]
	owner := deploymentDiagnosticOwner(m.owner)
	if p.Target != owner+".cgroup" || s.attempt != m.attempt || s.proc != m.proc || !s.statRead || !s.groupRead {
		return nil
	}
	c := &DeploymentProcessParseChecks{Owner: owner, StatRead: "bounded", CgroupRead: s.groupShape, CgroupCheck: "not_checked", ProcessCheck: "not_checked"}
	if s.groupShape == "bounded" {
		c.CgroupCheck = "accepted"
		if _, e := poolUnified(s.group); e != nil {
			c.CgroupCheck = "rejected"
		}
		tid := b.tid
		if m.owner == deploymentPID1 {
			tid = 1
		}
		c.ProcessCheck = "accepted"
		if _, _, e := poolProcess(s.stat, s.group, tid); e != nil {
			c.ProcessCheck = "rejected"
		}
	}
	return c
}
