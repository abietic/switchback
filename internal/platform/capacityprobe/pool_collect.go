package capacityprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"path"
	"runtime"
	"time"
)

// CollectPool reads fixed sources. Its output is unsigned and cannot enable
// readiness or execution. Unsupported platforms fail before constructing IO.
func CollectPool(ctx context.Context, plan PoolPlan) (PoolObservation, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return PoolObservation{}, poolError(ctx, ErrPoolUnavailable)
	}
	p, e := poolPlan(plan)
	if ctx == nil || e != nil {
		return PoolObservation{}, poolError(ctx, ErrPoolInvalid)
	}
	if e = ctx.Err(); e != nil {
		return PoolObservation{}, poolError(ctx, e)
	}
	return collectPool(ctx, p, newPoolEngineReader(), newPoolNative(), time.Now)
}
func collectPool(ctx context.Context, plan PoolPlan, get poolGet, k poolKernel, now func() time.Time) (out PoolObservation, err error) {
	p, e := poolPlan(plan)
	if ctx == nil || e != nil || get == nil || k == nil || now == nil {
		return out, poolError(ctx, ErrPoolInvalid)
	}
	if e = ctx.Err(); e != nil {
		return out, poolError(ctx, e)
	}
	ctx, cancel := context.WithTimeout(ctx, poolWindow)
	defer cancel()
	r := poolRead{ctx: ctx, k: k, now: now, inodes: map[uint64]bool{}}
	defer r.close()
	// Partial values remain local and are discarded on every error.
	var o PoolObservation
	defer func() {
		if err != nil {
			out = PoolObservation{}
			err = poolError(ctx, err)
		}
	}()
	if o.Started, e = r.time(); e != nil {
		return out, e
	}
	if o.BootBefore, o.NamespaceBefore, o.MountInfoBefore, e = r.global(); e != nil {
		return out, e
	}
	if r.mount, o.Anchor.MountOptions, e = poolMount(o.MountInfoBefore); e != nil {
		return out, e
	}
	if r.mounts, e = poolMountRecords(o.MountInfoBefore); e != nil {
		return out, e
	}
	if r.cgroupMount, e = poolCgroupMount(o.MountInfoBefore); e != nil {
		return out, e
	}
	if o.EngineBefore, e = poolCensus(ctx, get); e != nil {
		return out, e
	}
	members := map[string]int{}
	for n, c := range p.Core {
		for _, id := range c.ContainerIDs {
			members[id] = n
		}
	}
	seen := map[string]bool{}
	workers := []PoolContainerInspect{}
	for _, i := range o.EngineBefore.Containers {
		if i.Privileged || i.NetworkMode == "host" {
			return out, ErrPoolInvalid
		}
		if _, ok := members[i.ID]; ok {
			if i.CgroupParent != "switchback-core.slice" {
				return out, ErrPoolInvalid
			}
			seen[i.ID] = true
		} else {
			if i.CgroupParent != "switchback-workers.slice" || i.Labels["com.switchback.managed"] != "worker-runtime" || i.GraphDriver != "overlay2" || !createdLayerUpperValid(i.UpperDir) {
				return out, ErrPoolInvalid
			}
			workers = append(workers, i)
		}
	}
	if len(seen) != len(members) {
		return out, ErrPoolUnstable
	}
	root, e := r.directory(Root)
	if e != nil {
		return out, e
	}
	anchor, e := r.directory(p.AnchorPath)
	if e != nil {
		return out, e
	}
	if binary.LittleEndian.Uint32(anchor.value.FSXAttr[12:16]) != 0 || binary.LittleEndian.Uint32(anchor.value.FSXAttr[:4])&createdLayerXFlagProjInherit != 0 {
		return out, ErrPoolInvalid
	}
	if e = r.call(func() error { var er error; o.Anchor.Before, er = k.statfs(ctx, anchor.fd); return er }); e != nil {
		return out, e
	}
	if !poolStatFSValid(o.Anchor.Before) {
		return out, ErrPoolInvalid
	}
	storage := make([]poolDirectory, len(p.Storage))
	core := make([]poolGroup, len(p.Core))
	procs := make([][]poolProc, len(p.Core))
	projects := map[uint32]bool{}
	for n, s := range p.Storage {
		if storage[n], e = r.directory(s.Path); e != nil {
			return out, e
		}
		id := binary.LittleEndian.Uint32(storage[n].value.FSXAttr[12:16])
		if id == 0 || projects[id] {
			return out, ErrPoolInvalid
		}
		projects[id] = true
	}
	for n, c := range p.Core {
		if core[n], e = r.group(c.CgroupPath); e != nil {
			return out, e
		}
	}
	self, e := r.proc("", 0)
	if e != nil {
		return out, e
	}
	selfOK := false
	for _, c := range p.Core {
		selfOK = selfOK || poolInside(self.group, c.CgroupPath)
	}
	if !selfOK {
		return out, ErrPoolInvalid
	}
	for _, i := range o.EngineBefore.Containers {
		if n, ok := members[i.ID]; ok {
			proc, er := r.proc(i.ID, i.PID)
			if er != nil {
				return out, er
			}
			if !poolInside(proc.group, p.Core[n].CgroupPath) {
				return out, ErrPoolInvalid
			}
			procs[n] = append(procs[n], proc)
		}
	}
	type workerRead struct {
		inspect       PoolContainerInspect
		proc          poolProc
		group         poolGroup
		parent, layer poolDirectory
		quota         PoolQuotaReadback
	}
	wr := make([]workerRead, len(workers))
	for n, i := range workers {
		w := &wr[n]
		w.inspect = i
		if w.proc, e = r.proc(i.ID, i.PID); e != nil {
			return out, e
		}
		if !poolInside(w.proc.group, poolWorkerRoot) || w.proc.group == poolWorkerRoot {
			return out, ErrPoolInvalid
		}
		if w.group, e = r.group(w.proc.group); e != nil {
			return out, e
		}
		if w.parent, e = r.directory(path.Dir(i.UpperDir)); e != nil {
			return out, e
		}
		if w.layer, e = r.directory(i.UpperDir); e != nil {
			return out, e
		}
		id := binary.LittleEndian.Uint32(w.layer.value.FSXAttr[12:16])
		if id == 0 || projects[id] || id != binary.LittleEndian.Uint32(w.parent.value.FSXAttr[12:16]) || binary.LittleEndian.Uint32(w.parent.value.FSXAttr[:4])&createdLayerXFlagProjInherit == 0 {
			return out, ErrPoolInvalid
		}
		projects[id] = true
	}
	o.Storage = make([]PoolStorageReadback, len(storage))
	for n, d := range storage {
		q, er := r.quota(d)
		if er != nil {
			return out, er
		}
		o.Storage[n] = PoolStorageReadback{DomainID: p.Storage[n].ID, Quota: q}
	}
	for n := range wr {
		if wr[n].quota, e = r.quota(wr[n].layer); e != nil {
			return out, e
		}
	}
	// Reverse-side process/cgroup and filesystem reads precede the second census.
	if _, e = r.procAfter(self); e != nil {
		return out, e
	}
	o.Core = make([]PoolCoreReadback, len(core))
	for n := range core {
		v := &o.Core[n]
		v.CoreID = p.Core[n].ID
		v.Members = []PoolProcessReadback{}
		for _, p := range procs[n] {
			proc, er := r.procAfter(p)
			if er != nil {
				return out, er
			}
			v.Members = append(v.Members, proc)
		}
		if e = r.groupAfter(&core[n]); e != nil {
			return out, e
		}
		v.Cgroup = core[n].value
	}
	o.Workers = make([]PoolWorkerReadback, len(wr))
	for n := range wr {
		w := &wr[n]
		v := &o.Workers[n]
		v.ContainerID = w.inspect.ID
		if v.Process, e = r.procAfter(w.proc); e != nil {
			return out, e
		}
		if e = r.groupAfter(&w.group); e != nil {
			return out, e
		}
		v.Cgroup = w.group.value
		v.Quota = w.quota
	}
	if e = r.call(func() error { var er error; o.Anchor.After, er = k.statfs(ctx, anchor.fd); return er }); e != nil {
		return out, e
	}
	a, b := o.Anchor.Before, o.Anchor.After
	if !poolStatFSValid(b) || a.Type != b.Type || a.BlockSize != b.BlockSize || a.FragmentSize != b.FragmentSize || a.Blocks != b.Blocks {
		return out, ErrPoolInvalid
	}
	if e = r.directoryAfter(&root); e != nil {
		return out, e
	}
	if e = r.directoryAfter(&anchor); e != nil {
		return out, e
	}
	o.Anchor.Directory = anchor.value
	for n := range storage {
		if e = r.directoryAfter(&storage[n]); e != nil {
			return out, e
		}
		o.Storage[n].Directory = storage[n].value
	}
	for n := range wr {
		if e = r.directoryAfter(&wr[n].parent); e != nil {
			return out, e
		}
		if e = r.directoryAfter(&wr[n].layer); e != nil {
			return out, e
		}
		o.Workers[n].Layer = wr[n].layer.value
	}
	if o.EngineAfter, e = poolCensus(ctx, get); e != nil {
		return out, e
	}
	if !poolSameCensus(o.EngineBefore, o.EngineAfter) {
		a, b := o.EngineBefore.Containers, o.EngineAfter.Containers
		if len(a) != len(b) {
			return out, ErrPoolUnstable
		}
		for n := range a {
			if a[n].ID != b[n].ID {
				return out, ErrPoolUnstable
			}
		}
		return out, ErrPoolInvalid
	}
	if o.BootAfter, o.NamespaceAfter, o.MountInfoAfter, e = r.global(); e != nil {
		return out, e
	}
	if o.BootBefore != o.BootAfter || o.NamespaceBefore != o.NamespaceAfter || !bytes.Equal(o.MountInfoBefore, o.MountInfoAfter) {
		return out, ErrPoolInvalid
	}
	if o.Ended, e = r.time(); e != nil {
		return out, e
	}
	if o.Ended.Sub(o.Started) > poolWindow {
		return out, ErrPoolInvalid
	}
	if e = ctx.Err(); e != nil {
		return out, e
	}
	return o, nil
}
