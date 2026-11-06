package capacityprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPoolNativeEveryOperationFault(t *testing.T) {
	base := newPoolFixture(true)
	if _, e := collectPool(context.Background(), base.plan, base.get, base, base.now); e != nil {
		t.Fatal(e)
	}
	base.released(t)
	for index, name := range base.events {
		if name == "close" {
			continue
		}
		t.Run(fmt.Sprintf("%03d_%s", index, name), func(t *testing.T) {
			f := newPoolFixture(true)
			hit := false
			cause := errors.New("secret-canary/path")
			f.hook = func(_ string, n int) error {
				if n == index+1 {
					hit = true
					return cause
				}
				return nil
			}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if !hit || e == nil || errors.Is(e, cause) || strings.Contains(e.Error(), "secret") || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("fault escaped: hit=%v %v", hit, e)
			}
			if !errors.Is(e, ErrPoolUnavailable) {
				t.Fatalf("wrong class %v", e)
			}
			f.released(t)
		})
	}
}
func TestPoolCancellation(t *testing.T) {
	base := newPoolFixture(true)
	if _, e := collectPool(context.Background(), base.plan, base.get, base, base.now); e != nil {
		t.Fatal(e)
	}
	for index, name := range base.events {
		if name == "close" {
			continue
		}
		t.Run(fmt.Sprintf("%03d", index), func(t *testing.T) {
			f := newPoolFixture(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hit := false
			f.hook = func(_ string, n int) error {
				if n == index+1 {
					hit = true
					cancel()
				}
				return nil
			}
			o, e := collectPool(ctx, f.plan, f.get, f, f.now)
			if !hit || !errors.Is(e, context.Canceled) || !errors.Is(e, ErrPoolUnavailable) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("cancel missed %v hit=%v", e, hit)
			}
			f.released(t)
			for _, event := range f.events[index+1:] {
				if event != "close" {
					t.Fatal("operation after cancellation", event)
				}
			}
		})
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newPoolFixture(false)
			ctx := &poolLateContext{Context: context.Background(), cause: cause, at: 1}
			o, e := collectPool(ctx, f.plan, f.get, f, f.now)
			if !errors.Is(e, cause) || !reflect.DeepEqual(o, PoolObservation{}) || len(f.events) != 0 {
				t.Fatal("pre-cancel IO")
			}
		})
	}
}

type poolLateContext struct {
	context.Context
	count, at int
	cause     error
}

func (c *poolLateContext) Err() error {
	c.count++
	if c.count >= c.at {
		return c.cause
	}
	return nil
}
func TestPoolNativeStableAndDrift(t *testing.T) {
	cases := []struct {
		name   string
		change func(*poolFixture)
	}{
		{"ctime_nlink", func(f *poolFixture) {
			for p, d := range f.dirs {
				d.meta.CtimeSec++
				d.meta.CtimeNsec++
				d.meta.Nlink++
				f.dirs[p] = d
			}
		}},
		{"directory_replaced", func(f *poolFixture) { d := f.dirs[Root+"/product"]; d.meta.Inode++; f.dirs[Root+"/product"] = d }},
		{"project", func(f *poolFixture) { d := f.dirs[Root+"/product"]; d.attrs[12]++; f.dirs[Root+"/product"] = d }},
		{"geometry", func(f *poolFixture) { d := f.dirs[Root+"/product"]; d.geometry[32]++; f.dirs[Root+"/product"] = d }},
		{"mode", func(f *poolFixture) { d := f.dirs[Root+"/product"]; d.meta.Mode |= 2; f.dirs[Root+"/product"] = d }},
		{"pid_reused", func(f *poolFixture) {
			f.processes["202"]["stat"] = []byte(strings.Replace(string(poolFixtureStat(202)), "123", "124", 1))
		}},
		{"process_group", func(f *poolFixture) {
			f.processes["202"]["cgroup"] = []byte("0::/switchback.slice/switchback-workers.slice/other.scope\n")
		}},
		{"cgroup_files", func(f *poolFixture) {
			v := f.controls[poolFixtureWGroup]
			v.PIDs = "511\n"
			f.controls[poolFixtureWGroup] = v
		}},
		{"cgroup_inode", func(f *poolFixture) { d := f.dirs[poolFixtureWGroup]; d.meta.Inode++; f.dirs[poolFixtureWGroup] = d }},
		{"statfs", func(f *poolFixture) { f.statfsValue.BlockSize *= 2; f.statfsValue.FragmentSize *= 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPoolFixture(true)
			changed := false
			f.hook = func(s string, _ int) error {
				if !changed && strings.HasPrefix(s, "quota:") {
					tc.change(f)
					changed = true
				}
				return nil
			}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			f.released(t)
			if !changed {
				t.Fatal("cut missed")
			}
			if tc.name == "ctime_nlink" {
				if e != nil || o.Workers[0].Layer.Before != o.Workers[0].Layer.After {
					t.Fatalf("ordinary changes rejected %v", e)
				}
			} else if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("drift accepted %v", e)
			}
		})
	}
}
func TestPoolNativePrimitiveValidation(t *testing.T) {
	for _, name := range []string{"attrs_reserved", "attrs_realtime", "project_zero", "project_shared", "parent_project", "inherit", "geometry_tail", "statfs_signed", "control_oversize", "control_nul", "proc_bad_pid", "proc_bad_ticks", "proc_missing", "namespace"} {
		t.Run(name, func(t *testing.T) {
			f := newPoolFixture(true)
			want := ErrPoolInvalid
			switch name {
			case "attrs_reserved", "attrs_realtime", "project_zero", "project_shared", "parent_project", "inherit", "geometry_tail":
				p := Root + "/docker/overlay2/worker-layer/diff"
				if name == "parent_project" {
					p = Root + "/docker/overlay2/worker-layer"
				}
				d := f.dirs[p]
				switch name {
				case "attrs_reserved":
					d.attrs[27] = 1
				case "attrs_realtime":
					d.attrs[0] |= 1
				case "project_zero":
					binary.LittleEndian.PutUint32(d.attrs[12:16], 0)
				case "project_shared":
					binary.LittleEndian.PutUint32(d.attrs[12:16], 100)
				case "parent_project":
					binary.LittleEndian.PutUint32(d.attrs[12:16], 201)
				case "inherit":
					d.attrs[1] = 0
				case "geometry_tail":
					d.geometry[255] = 1
				}
				f.dirs[p] = d
			case "statfs_signed":
				f.statfsValue.BlockSize = ^uint64(0)
			case "control_oversize":
				v := f.controls[poolCoreRoot]
				v.Type = strings.Repeat("x", 129)
				f.controls[poolCoreRoot] = v
			case "control_nul":
				v := f.controls[poolCoreRoot]
				v.Type = "domain\x00\n"
				f.controls[poolCoreRoot] = v
			case "proc_bad_pid":
				f.processes["202"]["stat"] = poolFixtureStat(303)
			case "proc_bad_ticks":
				f.processes["202"]["stat"] = []byte(strings.Replace(string(poolFixtureStat(202)), "123", "0", 1))
			case "proc_missing":
				delete(f.processes, "202")
				want = ErrPoolUnstable
			case "namespace":
				f.hook = func(s string, _ int) error {
					if s == "ns:true:cgroup" {
						return ErrPoolInvalid
					}
					return nil
				}
			}
			rejectPool(t, f, want)
		})
	}
}
func TestPoolProcessParser(t *testing.T) {
	ticks, g, e := poolProcess(poolFixtureStat(202), []byte("0::/switchback.slice/switchback-workers.slice/worker.scope\n"), 202)
	if e != nil || ticks != 123 || g != poolFixtureWGroup {
		t.Fatal(e)
	}
	for _, s := range []string{"", "1:cpu:/x\n", "0::/x\n0::/y\n", "0::relative\n", "0::/a/../b\n", "0::/bad space\n", "0::/x", "0:://x\n", "0::/x\x00\n"} {
		t.Run(fmt.Sprintf("group_%x", s), func(t *testing.T) {
			if _, _, e := poolProcess(poolFixtureStat(202), []byte(s), 202); e == nil {
				t.Fatal("accepted cgroup")
			}
		})
	}
	for _, s := range []string{"", "202 (bad) S", strings.Replace(string(poolFixtureStat(202)), "123", "18446744073709551616", 1), strings.Replace(string(poolFixtureStat(202)), "123", "1e2", 1)} {
		t.Run(fmt.Sprintf("stat_%x", s), func(t *testing.T) {
			if _, _, e := poolProcess([]byte(s), []byte("0::/x\n"), 202); e == nil {
				t.Fatal("accepted stat")
			}
		})
	}
}
func TestPoolPlanValidation(t *testing.T) {
	for _, name := range []string{"anchor_root", "anchor_relative", "anchor_escape", "core_empty", "storage_empty", "core_33", "storage_33", "duplicate_id", "duplicate_path", "overlap", "core_path", "duplicate_cid", "bad_cid", "core_empty_members"} {
		t.Run(name, func(t *testing.T) {
			f := newPoolFixture(false)
			switch name {
			case "anchor_root":
				f.plan.AnchorPath = Root
			case "anchor_relative":
				f.plan.AnchorPath = "relative"
			case "anchor_escape":
				f.plan.AnchorPath = Root + "/x/../anchor"
			case "core_empty":
				f.plan.Core = nil
			case "storage_empty":
				f.plan.Storage = nil
			case "core_33":
				f.plan.Core = make([]PoolCorePlan, 33)
			case "storage_33":
				f.plan.Storage = make([]PoolStoragePlan, 33)
			case "duplicate_id":
				f.plan.Storage[1].ID = f.plan.Storage[0].ID
			case "duplicate_path":
				f.plan.Storage[1].Path = f.plan.Storage[0].Path
			case "overlap":
				f.plan.Storage[1].Path = f.plan.Storage[0].Path + "/child"
			case "core_path":
				f.plan.Core[0].CgroupPath = poolWorkerRoot
			case "duplicate_cid":
				f.plan.Core[0].ContainerIDs = append(f.plan.Core[0].ContainerIDs, poolFixtureCID)
			case "bad_cid":
				f.plan.Core[0].ContainerIDs[0] = "bad"
			case "core_empty_members":
				f.plan.Core[0].ContainerIDs = nil
			}
			rejectPool(t, f, ErrPoolInvalid)
			if len(f.events) != 0 {
				t.Fatal("IO before validation")
			}
		})
	}
}
func TestPoolPlanRejectsContainmentBeforeIO(t *testing.T) {
	for _, name := range []string{"core_ancestor_first", "core_descendant_first", "anchor_contains_storage", "storage_contains_anchor"} {
		t.Run(name, func(t *testing.T) {
			f := newPoolFixture(false)
			switch name {
			case "core_ancestor_first", "core_descendant_first":
				f.plan.Core = []PoolCorePlan{
					{"parent", poolCoreRoot + "/a", []string{poolFixtureCID}},
					{"child", poolCoreRoot + "/a/b", []string{strings.Repeat("b", 64)}},
				}
				if name == "core_descendant_first" {
					f.plan.Core[0], f.plan.Core[1] = f.plan.Core[1], f.plan.Core[0]
				}
			case "anchor_contains_storage":
				f.plan.AnchorPath = Root + "/core"
				f.plan.Storage[0].Path = Root + "/core/storage"
			case "storage_contains_anchor":
				f.plan.AnchorPath = Root + "/core/anchor"
			}
			if _, e := poolPlan(f.plan); !errors.Is(e, ErrPoolInvalid) {
				t.Errorf("overlapping plan accepted: %v", e)
			}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Errorf("overlapping collection did not fail with zero observation: %v", e)
			}
			if len(f.events) != 0 || f.tick != 0 {
				t.Errorf("IO or clock before overlap validation: events=%v clock=%d", f.events, f.tick)
			}
			f.released(t)
		})
	}
}

func TestPoolPlanSiblingPathsAccepted(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			f := newPoolFixture(false)
			// Shared parent and textual prefixes are legal without path containment.
			f.plan.Core = []PoolCorePlan{
				{"a", poolCoreRoot + "/a", []string{poolFixtureCID}},
				{"ab", poolCoreRoot + "/ab", []string{strings.Repeat("b", 64)}},
			}
			f.plan.AnchorPath = Root + "/layout/storage-anchor"
			f.plan.Storage = []PoolStoragePlan{{"a", Root + "/layout/storage"}, {"b", Root + "/layout/storage2"}}
			if reverse {
				f.plan.Core[0], f.plan.Core[1] = f.plan.Core[1], f.plan.Core[0]
				f.plan.Storage[0], f.plan.Storage[1] = f.plan.Storage[1], f.plan.Storage[0]
			}
			got, e := poolPlan(f.plan)
			if e != nil || !reflect.DeepEqual(got, f.plan) {
				t.Fatalf("legal siblings rejected or changed: %v %#v", e, got)
			}
		})
	}
}

func TestPoolClock(t *testing.T) {
	for _, name := range []string{"zero", "equal", "backward", "late", "sample_equal"} {
		t.Run(name, func(t *testing.T) {
			f := newPoolFixture(false)
			calls := 0
			now := func() time.Time {
				calls++
				v := f.now()
				switch name {
				case "zero":
					return time.Time{}
				case "equal":
					return time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
				case "backward":
					return v.Add(-time.Duration(calls) * time.Second)
				case "late":
					if calls > 1 {
						return v.Add(26 * time.Second)
					}
				case "sample_equal":
					if calls == 3 {
						return v.Add(-time.Microsecond)
					}
				}
				return v
			}
			o, e := collectPool(context.Background(), f.plan, f.get, f, now)
			if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("clock accepted %v", e)
			}
			f.released(t)
		})
	}
}
func TestPoolConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := newPoolFixture(i%2 == 0)
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if e != nil || len(o.Storage) != 2 {
				t.Errorf("independent collect %v", e)
			}
			f.released(t)
		}(i)
	}
	wg.Wait()
}

// Override only a finite primitive. No completed observation is injected.
type poolNamespaceFixture struct {
	*poolFixture
	mode  string
	calls int
}

func (f *poolNamespaceFixture) namespace(ctx context.Context, one bool, kind string) (uint64, error) {
	n, e := f.poolFixture.namespace(ctx, one, kind)
	f.calls++
	switch f.mode {
	case "private_mnt":
		if one && kind == "mnt" {
			n++
		}
	case "private_cgroup":
		if one && kind == "cgroup" {
			n++
		}
	case "cgroup_drift":
		if f.calls > 4 && kind == "cgroup" {
			n++
		}
	case "mnt_drift":
		if f.calls > 4 && kind == "mnt" {
			n++
		}
	}
	return n, e
}
func TestPoolNamespaceIdentity(t *testing.T) {
	for _, mode := range []string{"private_mnt", "private_cgroup", "cgroup_drift", "mnt_drift"} {
		t.Run(mode, func(t *testing.T) {
			f := &poolNamespaceFixture{poolFixture: newPoolFixture(true), mode: mode}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("namespace accepted %v", e)
			}
			f.released(t)
		})
	}
}

type poolQuotaFixture struct {
	*poolFixture
	mode       string
	reads      []int
	operations []string
}

func (f *poolQuotaFixture) quota(ctx context.Context, fd int, id uint32, state bool, b []byte) error {
	f.reads = append(f.reads, fd)
	f.operations = append(f.operations, fmt.Sprintf("%d:%v", id, state))
	if e := f.poolFixture.quota(ctx, fd, id, state, b); e != nil {
		return e
	}
	if state {
		switch f.mode {
		case "valid_semantics":
			binary.LittleEndian.PutUint32(b[4:8], 7)
			binary.LittleEndian.PutUint64(b[56:64], 99)
			binary.LittleEndian.PutUint32(b[80:84], 86400)
		case "state_padding":
			b[98] = 1
		case "state_flags":
			b[3] = 1
		case "state_off":
			b[2] = 0
		}
	} else {
		switch f.mode {
		case "valid_semantics":
			binary.LittleEndian.PutUint32(b[56:60], 1234)
			binary.LittleEndian.PutUint64(b[72:80], 8)
			binary.LittleEndian.PutUint16(b[100:102], 5)
		case "target_id":
			if id > 0 {
				b[4]++
			}
		case "default_id":
			if id == 0 {
				b[4] = 1
			}
		case "quota_padding":
			b[102] = 1
		case "quota_pad71":
			b[71] = 1
		case "quota_tail":
			b[111] = 1
		}
	}
	return nil
}
func TestPoolQuotaFullRawAndSameFD(t *testing.T) {
	for _, mode := range []string{"valid_semantics", "state_padding", "state_flags", "state_off", "target_id", "default_id", "quota_padding", "quota_pad71", "quota_tail"} {
		t.Run(mode, func(t *testing.T) {
			f := &poolQuotaFixture{poolFixture: newPoolFixture(true), mode: mode}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			f.released(t)
			if mode != "valid_semantics" {
				if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
					t.Fatalf("raw malformed accepted %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < len(f.reads); i += 3 {
				if f.reads[i] != f.reads[i+1] || f.reads[i] != f.reads[i+2] || f.operations[i] != "0:true" || f.operations[i+2] != "0:false" {
					t.Fatal("quota did not use same held FD", f.operations)
				}
			}
			for _, q := range []PoolQuotaReadback{o.Storage[0].Quota, o.Storage[1].Quota, o.Workers[0].Quota} {
				if binary.LittleEndian.Uint32(q.QStatV[4:8]) != 7 || binary.LittleEndian.Uint16(q.Default0[100:102]) != 5 || !strings.Contains(q.Target.RawHex, "0500") {
					t.Fatal("semantic raw data discarded")
				}
			}
		})
	}
}

type poolMountFixture struct {
	*poolFixture
	mode  string
	calls int
}

func (f *poolMountFixture) mountinfo(ctx context.Context) ([]byte, error) {
	raw, e := f.poolFixture.mountinfo(ctx)
	f.calls++
	if e != nil {
		return nil, e
	}
	switch f.mode {
	case "merged_sibling":
		raw = append(raw, []byte("80 73 0:50 / "+Root+"/docker/overlay2/worker-layer/merged rw - overlay overlay rw,lowerdir=/layers\n")...)
	case "diff":
		raw = append(raw, []byte("80 73 0:50 / "+Root+"/docker/overlay2/worker-layer/diff rw - overlay overlay rw\n")...)
	case "parent":
		raw = append(raw, []byte("80 73 8:7 /other "+Root+"/docker/overlay2/worker-layer rw - xfs dev rw,prjquota\n")...)
	case "anchor":
		raw = append(raw, []byte("80 73 8:7 /other "+Root+"/anchor rw - xfs dev rw,prjquota\n")...)
	case "storage":
		raw = append(raw, []byte("80 73 8:7 /other "+Root+"/product rw - xfs dev rw,prjquota\n")...)
	case "core_cgroup":
		raw = append(raw, []byte("80 74 0:28 / "+poolCoreRoot+" rw - cgroup2 cgroup rw\n")...)
	case "worker_cgroup":
		raw = append(raw, []byte("80 74 0:28 / "+poolFixtureWGroup+" rw - cgroup2 cgroup rw\n")...)
	case "wrong_cgroup_device":
		raw = []byte(strings.Replace(string(raw), "74 1 0:28", "74 1 0:29", 1))
	case "wrong_cgroup_root":
		raw = []byte(strings.Replace(string(raw), "0:28 / /sys/fs/cgroup", "0:28 /private /sys/fs/cgroup", 1))
	case "mount_drift":
		if f.calls == 2 {
			raw = append(raw, []byte("80 1 0:50 / /unrelated rw - tmpfs tmpfs rw\n")...)
		}
	case "duplicate_id":
		raw = append(raw, []byte("73 1 0:50 / /unrelated rw - tmpfs tmpfs rw\n")...)
	case "escape":
		raw = append(raw, []byte("80 1 0:50 / /bad\\999 rw - tmpfs tmpfs rw\n")...)
	case "private_self_cgroup":
		raw = append(raw, []byte("80 74 0:28 / "+poolFixtureGroup+" rw - cgroup2 cgroup rw\n")...)
	}
	return raw, nil
}
func TestPoolMountPathAndFDIdentity(t *testing.T) {
	for _, mode := range []string{"merged_sibling", "diff", "parent", "anchor", "storage", "core_cgroup", "worker_cgroup", "wrong_cgroup_device", "wrong_cgroup_root", "mount_drift", "duplicate_id", "escape", "private_self_cgroup"} {
		t.Run(mode, func(t *testing.T) {
			f := &poolMountFixture{poolFixture: newPoolFixture(true), mode: mode}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			f.released(t)
			if mode == "merged_sibling" {
				if e != nil || len(o.Workers) != 1 || o.Workers[0].Layer.Before.MountID != 73 {
					t.Fatalf("legal merged sibling rejected %v", e)
				}
				return
			}
			if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("wrong sampled mount accepted %v", e)
			}
		})
	}
}

type poolCanonicalFixture struct {
	*poolFixture
	metas    map[int]createdLayerMetadata
	replaced bool
	target   string
}

func (f *poolCanonicalFixture) open(ctx context.Context, p string) (int, error) {
	fd, e := f.poolFixture.open(ctx, p)
	if fd >= 0 {
		f.metas[fd] = f.dirs[p].meta
	}
	return fd, e
}
func (f *poolCanonicalFixture) stat(ctx context.Context, fd int) (createdLayerMetadata, error) {
	_, e := f.poolFixture.stat(ctx, fd)
	return f.metas[fd], e
}
func TestPoolCanonicalReplacementHeldFD(t *testing.T) {
	for _, target := range []string{Root + "/product", poolFixtureWGroup} {
		t.Run(target, func(t *testing.T) {
			f := &poolCanonicalFixture{poolFixture: newPoolFixture(true), metas: map[int]createdLayerMetadata{}, target: target}
			f.hook = func(s string, _ int) error {
				if !f.replaced && strings.HasPrefix(s, "quota:") {
					d := f.dirs[target]
					d.meta.Inode += 100
					f.dirs[target] = d
					f.replaced = true
				}
				return nil
			}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if !f.replaced || !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
				t.Fatalf("canonical replacement accepted %v", e)
			}
			f.released(t)
		})
	}
}

type poolBootFixture struct {
	*poolFixture
	calls int
}

func (f *poolBootFixture) boot(ctx context.Context) ([16]byte, error) {
	b, e := f.poolFixture.boot(ctx)
	f.calls++
	if f.calls > 1 {
		b[0]++
	}
	return b, e
}
func TestPoolBootDrift(t *testing.T) {
	f := &poolBootFixture{poolFixture: newPoolFixture(false)}
	o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
	if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
		t.Fatal("boot drift accepted", e)
	}
	f.released(t)
}
func TestPoolProcessDisappearanceAndLateClockCancel(t *testing.T) {
	f := newPoolFixture(true)
	seen := 0
	f.hook = func(s string, _ int) error {
		if s == "process:proc:202:stat" {
			seen++
			if seen == 2 {
				return ErrPoolUnstable
			}
		}
		return nil
	}
	rejectPool(t, f, ErrPoolUnstable)
	f = newPoolFixture(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	o, e := collectPool(ctx, f.plan, f.get, f, func() time.Time {
		calls++
		if calls == 20 {
			cancel()
		}
		return f.now()
	})
	if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(o, PoolObservation{}) {
		t.Fatalf("late clock cancellation %d %v", calls, e)
	}
	f.released(t)
}

// Each primitive is independently faulted before and after the recorded backend
// operation. Allocated descriptors returned with a late error must still close.
type poolEdgeFixture struct {
	*poolFixture
	edges  []string
	at     int
	cancel context.CancelFunc
	cause  error
}

func (f *poolEdgeFixture) edge(op string) error {
	f.edges = append(f.edges, op)
	if f.at == len(f.edges) {
		if f.cancel != nil {
			f.cancel()
			return nil
		}
		return f.cause
	}
	return nil
}
func (f *poolEdgeFixture) step(op string, call func() error) error {
	if e := f.edge("before:" + op); e != nil {
		return e
	}
	if e := call(); e != nil {
		return e
	}
	return f.edge("after:" + op)
}
func (f *poolEdgeFixture) boot(ctx context.Context) (v [16]byte, e error) {
	e = f.step("boot", func() error { var er error; v, er = f.poolFixture.boot(ctx); return er })
	return
}
func (f *poolEdgeFixture) namespace(ctx context.Context, one bool, kind string) (v uint64, e error) {
	e = f.step("namespace", func() error { var er error; v, er = f.poolFixture.namespace(ctx, one, kind); return er })
	return
}
func (f *poolEdgeFixture) mountinfo(ctx context.Context) (v []byte, e error) {
	e = f.step("mountinfo", func() error { var er error; v, er = f.poolFixture.mountinfo(ctx); return er })
	return
}
func (f *poolEdgeFixture) open(ctx context.Context, p string) (v int, e error) {
	v = -1
	e = f.step("open", func() error { var er error; v, er = f.poolFixture.open(ctx, p); return er })
	return
}
func (f *poolEdgeFixture) procOpen(ctx context.Context, pid uint64) (v int, e error) {
	v = -1
	e = f.step("proc_open", func() error { var er error; v, er = f.poolFixture.procOpen(ctx, pid); return er })
	return
}
func (f *poolEdgeFixture) stat(ctx context.Context, fd int) (v createdLayerMetadata, e error) {
	e = f.step("stat", func() error { var er error; v, er = f.poolFixture.stat(ctx, fd); return er })
	return
}
func (f *poolEdgeFixture) geometry(ctx context.Context, fd int, b *[256]byte) error {
	return f.step("geometry", func() error { return f.poolFixture.geometry(ctx, fd, b) })
}
func (f *poolEdgeFixture) attrs(ctx context.Context, fd int, b *[28]byte) error {
	return f.step("attrs", func() error { return f.poolFixture.attrs(ctx, fd, b) })
}
func (f *poolEdgeFixture) quota(ctx context.Context, fd int, id uint32, state bool, b []byte) error {
	return f.step("quota", func() error { return f.poolFixture.quota(ctx, fd, id, state, b) })
}
func (f *poolEdgeFixture) statfs(ctx context.Context, fd int) (v PoolStatFSReadback, e error) {
	e = f.step("statfs", func() error { var er error; v, er = f.poolFixture.statfs(ctx, fd); return er })
	return
}
func (f *poolEdgeFixture) control(ctx context.Context, fd int, leaf string) (v []byte, e error) {
	e = f.step("control", func() error { var er error; v, er = f.poolFixture.control(ctx, fd, leaf); return er })
	return
}
func (f *poolEdgeFixture) process(ctx context.Context, fd int, leaf string) (v []byte, e error) {
	e = f.step("process", func() error { var er error; v, er = f.poolFixture.process(ctx, fd, leaf); return er })
	return
}
func TestPoolNativeBeforeAfterBoundaries(t *testing.T) {
	base := &poolEdgeFixture{poolFixture: newPoolFixture(true)}
	if _, e := collectPool(context.Background(), base.plan, base.get, base, base.now); e != nil {
		t.Fatal(e)
	}
	base.released(t)
	for index, edge := range base.edges {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%03d_%s_cancel_%v", index, edge, cancelled), func(t *testing.T) {
				f := &poolEdgeFixture{poolFixture: newPoolFixture(true), at: index + 1, cause: errors.New("secret-canary")}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelled {
					f.cancel = cancel
				}
				o, e := collectPool(ctx, f.plan, f.get, f, f.now)
				if e == nil || !errors.Is(e, ErrPoolUnavailable) || !reflect.DeepEqual(o, PoolObservation{}) || errors.Is(e, f.cause) || strings.Contains(e.Error(), "secret") {
					t.Fatal("unsafe finite boundary", e)
				}
				if cancelled && !errors.Is(e, context.Canceled) {
					t.Fatal("cancel identity lost", e)
				}
				f.released(t)
			})
		}
	}
}

// Record actual finite attrs returns at the held-after and canonical-reopen
// boundaries; the collector must keep those raw arrays, never normalize them.
type poolExtentFixture struct {
	*poolFixture
	boundary      string
	changedOffset int
	readbacks     [][28]byte
}

func (f *poolExtentFixture) attrs(ctx context.Context, fd int, out *[28]byte) error {
	if e := f.poolFixture.attrs(ctx, fd, out); e != nil {
		return e
	}
	if f.fds[fd] != Root+"/product" {
		return nil
	}
	index := len(f.readbacks) + 1
	binary.LittleEndian.PutUint32(out[8:12], 0x01020304)
	changed := (f.boundary == "before_after" && index >= 2) || (f.boundary == "canonical_reopen" && index >= 3)
	if changed {
		binary.LittleEndian.PutUint32(out[8:12], 0x11223344)
		if f.changedOffset >= 0 {
			out[f.changedOffset] ^= 4
		}
	}
	f.readbacks = append(f.readbacks, *out)
	return nil
}
func TestPoolFSXAttrRunningExtentChanges(t *testing.T) {
	for _, boundary := range []string{"before_after", "canonical_reopen"} {
		t.Run(boundary, func(t *testing.T) {
			f := &poolExtentFixture{poolFixture: newPoolFixture(true), boundary: boundary, changedOffset: -1}
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			f.released(t)
			if e != nil {
				t.Fatalf("ordinary fsx_nextents change rejected at %s: %v", boundary, e)
			}
			if len(f.readbacks) != 3 {
				t.Fatalf("missing actual attrs reads: %d", len(f.readbacks))
			}
			d := o.Storage[1].Directory
			if d.Before.CanonicalPath != Root+"/product" || d.FSXAttr != f.readbacks[0] || d.FSXAttrAfter != f.readbacks[1] {
				t.Fatal("actual raw attrs were overwritten")
			}
			if binary.LittleEndian.Uint32(d.FSXAttr[8:12]) != 0x01020304 || binary.LittleEndian.Uint32(f.readbacks[2][8:12]) != 0x11223344 {
				t.Fatal("fixture did not cross real extent-count boundary")
			}
			want := uint32(0x01020304)
			if boundary == "before_after" {
				want = 0x11223344
			}
			if binary.LittleEndian.Uint32(d.FSXAttrAfter[8:12]) != want {
				t.Fatal("after raw was normalized or replaced by canonical raw")
			}
			if !d.IdentityRechecked || !d.ReopenedNoFollow || d.Before != d.After {
				t.Fatal("stable directory recheck missing")
			}
		})
	}
}
func TestPoolFSXAttrStableFieldDrift(t *testing.T) {
	for _, boundary := range []string{"before_after", "canonical_reopen"} {
		for _, field := range []struct {
			name   string
			offset int
		}{{"xflags", 0}, {"extsize", 4}, {"project", 12}, {"cowextsize", 16}, {"padding", 27}} {
			t.Run(boundary+"/"+field.name, func(t *testing.T) {
				f := &poolExtentFixture{poolFixture: newPoolFixture(true), boundary: boundary, changedOffset: field.offset}
				o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
				f.released(t)
				if !errors.Is(e, ErrPoolInvalid) || !reflect.DeepEqual(o, PoolObservation{}) {
					t.Fatalf("security/config/reserved drift accepted at %s/%s: %v", boundary, field.name, e)
				}
				wantReads := 2
				if boundary == "canonical_reopen" {
					wantReads = 3
				}
				if len(f.readbacks) != wantReads {
					t.Fatalf("mutation not reached: got %d reads want %d", len(f.readbacks), wantReads)
				}
			})
		}
	}
}
