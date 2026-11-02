package capacityprobe

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type deploymentOwner uint8

const (
	deploymentCaller deploymentOwner = iota
	deploymentPID1
)

type deploymentLeaf uint8

const (
	deploymentStatus deploymentLeaf = iota
	deploymentStat
	deploymentCgroup
)

type deploymentGlobal uint8

const (
	deploymentBoot deploymentGlobal = iota
	deploymentLastCap
)

type deploymentNS uint8

const (
	deploymentUserNS deploymentNS = iota
	deploymentPIDNS
	deploymentMountNS
	deploymentCgroupNS
)

const deploymentProcFS uint64 = 0x9fa0
const deploymentNSFS uint64 = 0x6e736673
const deploymentDirectory uint32 = 0040000
const deploymentRegular uint32 = 0100000

type deploymentMetadata struct {
	device, inode uint64
	mode          uint32
}

// Every port operation is a finite raw primitive. Descriptors belong to this
// invocation; open may return an owned descriptor together with an error.
type deploymentBackend interface {
	now(context.Context) (time.Time, error)
	ids(context.Context) (uint64, uint64, error)
	kernel(context.Context) (string, error)
	openGlobal(context.Context, deploymentGlobal) (int, error)
	openProc(context.Context, deploymentOwner) (int, error)
	openLeaf(context.Context, int, deploymentLeaf) (int, error)
	openNamespace(context.Context, int, deploymentNS) (int, error)
	namespaceStat(context.Context, int, deploymentNS) (deploymentMetadata, error)
	stat(context.Context, int) (deploymentMetadata, error)
	statfs(context.Context, int) (uint64, error)
	read(context.Context, int, int) ([]byte, error)
	close(int)
}

func parseDeploymentStatus(raw []byte) (DeploymentProcessObservation, error) {
	v, e := procstatus.Parse(raw)
	if e != nil {
		return v, ErrDeploymentInvalid
	}
	return v, nil
}

// deploymentRead owns every successfully returned descriptor, including one
// returned with a late error or cancellation. Cleanup never needs a live context.
type deploymentRead struct {
	ctx   context.Context
	k     deploymentBackend
	owned []int
}

func (r *deploymentRead) call(f func() error) error {
	if r.ctx.Err() != nil {
		return ErrDeploymentCancelled
	}
	e := f()
	if r.ctx.Err() != nil {
		return ErrDeploymentCancelled
	}
	return e
}
func (r *deploymentRead) acquire(f func() (int, error)) (int, error) {
	fd := -1
	e := r.call(func() error { var err error; fd, err = f(); return err })
	if fd >= 0 {
		r.owned = append(r.owned, fd)
	} else if e == nil {
		e = ErrDeploymentInvalid
	}
	return fd, e
}
func (r *deploymentRead) close(fd int) {
	for i, v := range r.owned {
		if v == fd {
			r.k.close(fd)
			r.owned = append(r.owned[:i], r.owned[i+1:]...)
			return
		}
	}
}
func (r *deploymentRead) cleanup() {
	for len(r.owned) > 0 {
		r.close(r.owned[len(r.owned)-1])
	}
}
func (r *deploymentRead) metadata(fd int, fs uint64, kind uint32) (m deploymentMetadata, e error) {
	e = r.call(func() error { var err error; m, err = r.k.stat(r.ctx, fd); return err })
	if e != nil {
		return
	}
	var actual uint64
	e = r.call(func() error { var err error; actual, err = r.k.statfs(r.ctx, fd); return err })
	if e != nil {
		return
	}
	if actual != fs || m.device == 0 || m.inode == 0 || m.mode&0170000 != kind {
		e = ErrDeploymentInvalid
	}
	return
}
func (r *deploymentRead) readFile(open func() (int, error), limit int) (raw []byte, e error) {
	fd, e := r.acquire(open)
	if fd >= 0 {
		defer r.close(fd)
	}
	if e != nil {
		return nil, e
	}
	if _, e = r.metadata(fd, deploymentProcFS, deploymentRegular); e != nil {
		return nil, e
	}
	e = r.call(func() error { var err error; raw, err = r.k.read(r.ctx, fd, limit); return err })
	if e == nil && (len(raw) == 0 || len(raw) > limit) {
		e = ErrDeploymentInvalid
	}
	return
}
func (r *deploymentRead) global(g deploymentGlobal, limit int) ([]byte, error) {
	return r.readFile(func() (int, error) { return r.k.openGlobal(r.ctx, g) }, limit)
}
func (r *deploymentRead) process(fd int, pid, tid uint64) (DeploymentProcessObservation, error) {
	var zero DeploymentProcessObservation
	status, e := r.readFile(func() (int, error) { return r.k.openLeaf(r.ctx, fd, deploymentStatus) }, 65536)
	if e != nil {
		return zero, e
	}
	p, e := parseDeploymentStatus(status)
	if e != nil || p.PID != pid || p.TGID != pid || p.TID != tid {
		return zero, ErrDeploymentInvalid
	}
	stat, e := r.readFile(func() (int, error) { return r.k.openLeaf(r.ctx, fd, deploymentStat) }, 4096)
	if e != nil {
		return zero, e
	}
	group, e := r.readFile(func() (int, error) { return r.k.openLeaf(r.ctx, fd, deploymentCgroup) }, 4096)
	if e != nil {
		return zero, e
	}
	p.StartTimeTicks, p.UnifiedCgroupPath, e = poolProcess(stat, group, tid)
	if e != nil {
		return zero, ErrDeploymentInvalid
	}
	return p, nil
}
func (r *deploymentRead) namespace(fd int, n deploymentNS) (DeploymentNamespaceValue, error) {
	var m deploymentMetadata
	e := r.call(func() error { var err error; m, err = r.k.namespaceStat(r.ctx, fd, n); return err })
	if errors.Is(e, syscall.EACCES) {
		return DeploymentNamespaceValue{Errno: 13, Class: "EACCES"}, nil
	}
	if errors.Is(e, syscall.EPERM) {
		return DeploymentNamespaceValue{Errno: 1, Class: "EPERM"}, nil
	}
	if e != nil {
		return DeploymentNamespaceValue{}, e
	}
	if m.device == 0 || m.inode == 0 || m.mode&0170000 != deploymentRegular {
		return DeploymentNamespaceValue{}, ErrDeploymentInvalid
	}
	nfd, e := r.acquire(func() (int, error) { return r.k.openNamespace(r.ctx, fd, n) })
	if nfd >= 0 {
		defer r.close(nfd)
	}
	if e != nil {
		return DeploymentNamespaceValue{}, e
	}
	actual, e := r.metadata(nfd, deploymentNSFS, deploymentRegular)
	if e != nil {
		return DeploymentNamespaceValue{}, e
	}
	if actual.device != m.device || actual.inode != m.inode {
		return DeploymentNamespaceValue{}, ErrDeploymentInvalid
	}
	return DeploymentNamespaceValue{Device: strconv.FormatUint(m.device, 10), Inode: strconv.FormatUint(m.inode, 10), Class: "observed"}, nil
}
func parseDeploymentBoot(raw []byte) (string, error) {
	if len(raw) != 37 || raw[36] != '\n' {
		return "", ErrDeploymentInvalid
	}
	s := string(raw[:36])
	for _, n := range []int{8, 13, 18, 23} {
		if s[n] != '-' {
			return "", ErrDeploymentInvalid
		}
	}
	compact := strings.ReplaceAll(s, "-", "")
	v, e := hex.DecodeString(compact)
	if e != nil || len(v) != 16 || bytes.Equal(v, make([]byte, 16)) {
		return "", ErrDeploymentInvalid
	}
	return strings.ToLower(s), nil
}
func deploymentKernelValid(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-+", c)) {
			return false
		}
	}
	return true
}
func deploymentFailure(e error) error {
	switch e {
	case ErrDeploymentInvalid, ErrDeploymentCancelled, ErrDeploymentUnsupported:
		return e
	}
	return ErrDeploymentUnavailable
}
func observeDeployment(ctx context.Context, k deploymentBackend) (out DeploymentObservation, err error) {
	if ctx == nil || k == nil {
		return out, ErrDeploymentInvalid
	}
	if ctx.Err() != nil {
		return out, ErrDeploymentCancelled
	}
	r := deploymentRead{ctx: ctx, k: k}
	defer r.cleanup()
	defer func() {
		if err != nil {
			out = DeploymentObservation{}
			err = deploymentFailure(err)
		}
	}()
	var v DeploymentObservation
	if err = r.call(func() error { var e error; v.Started, e = k.now(ctx); v.Started = v.Started.Round(0); return e }); err != nil {
		return
	}
	if v.Started.IsZero() {
		err = ErrDeploymentInvalid
		return
	}
	var raw []byte
	if raw, err = r.global(deploymentBoot, 64); err != nil {
		return
	}
	if v.BootBefore, err = parseDeploymentBoot(raw); err != nil {
		return
	}
	if raw, err = r.global(deploymentLastCap, 32); err != nil {
		return
	}
	if len(raw) < 2 || raw[len(raw)-1] != '\n' {
		err = ErrDeploymentInvalid
		return
	}
	var last uint64
	last, err = createdLayerDecimal(string(raw[:len(raw)-1]), 64)
	if err != nil {
		err = ErrDeploymentInvalid
		return
	}
	if last > 63 {
		err = ErrDeploymentUnsupported
		return
	}
	v.CapLastCap = uint32(last)
	if err = r.call(func() error { var e error; v.KernelRelease, e = k.kernel(ctx); return e }); err != nil {
		return
	}
	if !deploymentKernelValid(v.KernelRelease) {
		err = ErrDeploymentInvalid
		return
	}
	var pid, tid uint64
	if err = r.call(func() error { var e error; pid, tid, e = k.ids(ctx); return e }); err != nil {
		return
	}
	if pid == 0 || tid == 0 || pid > 1<<32-1 || tid > 1<<32-1 {
		err = ErrDeploymentInvalid
		return
	}
	held := [2]int{-1, -1}
	var meta [2]deploymentMetadata
	for o := deploymentCaller; o <= deploymentPID1; o++ {
		held[o], err = r.acquire(func() (int, error) { return k.openProc(ctx, o) })
		if err != nil {
			return
		}
		if meta[o], err = r.metadata(held[o], deploymentProcFS, deploymentDirectory); err != nil {
			return
		}
	}
	before := [2]*DeploymentProcessObservation{&v.CallerBefore, &v.PID1Before}
	after := [2]*DeploymentProcessObservation{&v.CallerAfter, &v.PID1After}
	for phase := 0; phase < 2; phase++ {
		for o := deploymentCaller; o <= deploymentPID1; o++ {
			p, t := pid, tid
			if o == deploymentPID1 {
				p = 1
				t = 1
			}
			target := before[o]
			if phase == 1 {
				target = after[o]
			}
			if *target, err = r.process(held[o], p, t); err != nil {
				return
			}
			for n := deploymentUserNS; n <= deploymentCgroupNS; n++ {
				i := int(o)*4 + int(n)
				entry := &v.Namespaces[i]
				entry.Owner = [2]string{"caller_thread", "pid1"}[o]
				entry.Kind = [4]string{"user", "pid", "mnt", "cgroup"}[n]
				value, e := r.namespace(held[o], n)
				if e != nil {
					err = e
					return
				}
				if phase == 0 {
					entry.Before = value
				} else {
					entry.After = value
				}
			}
		}
	}
	for o := deploymentCaller; o <= deploymentPID1; o++ {
		canonical, e := r.acquire(func() (int, error) { return k.openProc(ctx, o) })
		if e != nil {
			err = e
			return
		}
		m, e := r.metadata(canonical, deploymentProcFS, deploymentDirectory)
		if e != nil {
			err = e
			return
		}
		if m != meta[o] {
			err = ErrDeploymentInvalid
			return
		}
		// Release the original only after comparing the independent canonical FD.
		// Reading through the canonical FD then needs at most one transient leaf.
		r.close(held[o])
		held[o] = -1
		p, t := pid, tid
		if o == deploymentPID1 {
			p = 1
			t = 1
		}
		actual, e := r.process(canonical, p, t)
		if e != nil {
			err = e
			return
		}
		if actual != *before[o] {
			err = ErrDeploymentInvalid
			return
		}
		r.close(canonical)
	}
	if raw, err = r.global(deploymentBoot, 64); err != nil {
		return
	}
	if v.BootAfter, err = parseDeploymentBoot(raw); err != nil {
		return
	}
	if err = r.call(func() error { var e error; v.Ended, e = k.now(ctx); v.Ended = v.Ended.Round(0); return e }); err != nil {
		return
	}
	if v.Ended.IsZero() || !v.Ended.After(v.Started) || v.BootBefore != v.BootAfter || v.CallerBefore != v.CallerAfter || v.PID1Before != v.PID1After {
		err = ErrDeploymentInvalid
		return
	}
	for _, entry := range v.Namespaces {
		if entry.Before != entry.After {
			err = ErrDeploymentInvalid
			return
		}
	}
	v.Schema = "switchback.capacity.deployment_diagnostic.v1"
	v.Kind = "diagnostic_only"
	return v, nil
}
