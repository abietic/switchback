package capacityprobe

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
)

const poolFixtureUUID = "01020304-0506-0708-090a-0b0c0d0e0f10"

var poolFixture16 = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
var poolFixtureCID = strings.Repeat("a", 64)
var poolFixtureWorker = strings.Repeat("c", 64)

const poolFixtureGroup = poolCoreRoot + "/core.scope"
const poolFixtureWGroup = poolWorkerRoot + "/worker.scope"

type poolFixtureDir struct {
	meta     createdLayerMetadata
	geometry [256]byte
	attrs    [28]byte
}
type poolFixture struct {
	plan        PoolPlan
	raw         map[string][]byte
	dirs        map[string]poolFixtureDir
	processes   map[string]map[string][]byte
	controls    map[string]PoolCgroupFiles
	fds         map[int]string
	closed      map[int]int
	next        int
	events      []string
	hook        func(string, int) error
	tick        int64
	statfsValue PoolStatFSReadback
}

func poolFixtureStat(pid uint64) []byte {
	return []byte(fmt.Sprintf("%d (comm has (spaces) and ) parens) S %s123 0\n", pid, strings.Repeat("1 ", 18)))
}
func poolFixtureQuota(id uint32) [112]byte {
	var q [112]byte
	q[0], q[1] = 1, 2
	binary.LittleEndian.PutUint32(q[4:8], id)
	if id > 0 {
		binary.LittleEndian.PutUint64(q[8:16], 20971520)
		binary.LittleEndian.PutUint64(q[16:24], 20971520)
		binary.LittleEndian.PutUint64(q[40:48], 8)
		binary.LittleEndian.PutUint64(q[48:56], 9)
	}
	return q
}
func newPoolFixture(worker bool) *poolFixture {
	f := &poolFixture{plan: PoolPlan{AnchorPath: Root + "/anchor", Core: []PoolCorePlan{{"core", poolCoreRoot, []string{poolFixtureCID}}}, Storage: []PoolStoragePlan{{"core", Root + "/core"}, {"product", Root + "/product"}}}, raw: map[string][]byte{}, dirs: map[string]poolFixtureDir{}, processes: map[string]map[string][]byte{}, controls: map[string]PoolCgroupFiles{}, fds: map[int]string{}, closed: map[int]int{}, next: 10, statfsValue: PoolStatFSReadback{0x58465342, 4096, 4096, 1 << 24, 1 << 23, 1 << 22}}
	f.raw["/v1.56/version"] = []byte(`{"ApiVersion":"1.56","Version":"29.9.0"}`)
	f.raw["/v1.56/info"] = []byte(`{"ID":"` + poolFixtureUUID + `","ServerVersion":"29.9.0","DockerRootDir":"` + Root + `/docker","Driver":"overlay2","NCPU":8,"MemTotal":34359738368}`)
	ids := []any{map[string]any{"Id": poolFixtureCID}}
	makeInspect := func(cid string, pid uint64, cg string, labels map[string]string, layer string, cpu, mem, pids uint64) {
		v := map[string]any{"Id": cid, "Image": "sha256:" + strings.Repeat("b", 64), "State": map[string]any{"Status": "running", "Pid": pid, "Running": true, "Paused": false, "Restarting": false, "Dead": false, "StartedAt": "2026-10-20T01:00:00Z"}, "Config": map[string]any{"Labels": labels, "Env": []string{"secret-canary-not-copied"}}, "HostConfig": map[string]any{"NanoCpus": cpu, "Memory": mem, "MemorySwap": mem, "PidsLimit": pids, "CgroupParent": cg, "Privileged": false, "NetworkMode": "bridge"}, "GraphDriver": map[string]any{"Name": "overlay2", "Data": map[string]any{"UpperDir": layer}}}
		f.raw["/v1.56/containers/"+cid+"/json"], _ = json.Marshal(v)
	}
	makeInspect(poolFixtureCID, 101, "switchback-core.slice", map[string]string{}, "", 0, 0, 0)
	for i, p := range []string{Root, Root + "/anchor", Root + "/core", Root + "/product", poolCoreRoot, poolFixtureGroup} {
		d := poolFixtureDir{meta: createdLayerMetadata{Inode: uint64(i + 1), Major: 8, Minor: 7, Mode: 0040755, MountID: 73, Nlink: 2, FSType: 0x58465342}}
		binary.LittleEndian.PutUint32(d.geometry[0:4], 4096)
		binary.LittleEndian.PutUint32(d.geometry[12:16], 4)
		binary.LittleEndian.PutUint64(d.geometry[32:40], 1<<24)
		copy(d.geometry[64:80], poolFixture16[:])
		binary.LittleEndian.PutUint32(d.geometry[88:92], 5)
		if i == 2 || i == 3 {
			binary.LittleEndian.PutUint32(d.attrs[0:4], 0x200)
			binary.LittleEndian.PutUint32(d.attrs[12:16], uint32(98+i))
		}
		if strings.HasPrefix(p, "/sys/") {
			d.meta.FSType = 0x63677270
			d.meta.Major = 0
			d.meta.Minor = 28
			d.meta.MountID = 74
		}
		f.dirs[p] = d
	}
	f.controls[poolCoreRoot] = PoolCgroupFiles{"120000 100000\n", "3221225472\n", "0\n", "1024\n", "domain\n"}
	f.processes["0"] = map[string][]byte{"stat": poolFixtureStat(99), "cgroup": []byte("0::" + strings.TrimPrefix(poolFixtureGroup, "/sys/fs/cgroup") + "\n")}
	f.processes["101"] = map[string][]byte{"stat": poolFixtureStat(101), "cgroup": []byte("0::" + strings.TrimPrefix(poolFixtureGroup, "/sys/fs/cgroup") + "\n")}
	if worker {
		ids = append(ids, map[string]any{"Id": poolFixtureWorker})
		r, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
		n, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
		labels := map[string]string{"com.switchback.managed": "worker-runtime", "com.switchback.worker-session-id": "session-one", "com.switchback.worker-run-id": "run-one", "com.switchback.runtime-instance-id": "runtime-one", "com.switchback.runtime-generation": "1", "com.switchback.resource-profile": r.ID + "@1:" + r.Digest, "com.switchback.agent-profile": "agent@1:sha256:" + strings.Repeat("f", 64), "com.switchback.network-policy-id": n.ID, "com.switchback.network-policy-revision": "1", "com.switchback.network-policy-digest": n.Digest, "com.switchback.network-enforcement-status": "active"}
		layer := Root + "/docker/overlay2/worker-layer/diff"
		makeInspect(poolFixtureWorker, 202, "switchback-workers.slice", labels, layer, 2000000000, 4<<30, 512)
		f.processes["202"] = map[string][]byte{"stat": poolFixtureStat(202), "cgroup": []byte("0::" + strings.TrimPrefix(poolFixtureWGroup, "/sys/fs/cgroup") + "\n")}
		f.controls[poolFixtureWGroup] = PoolCgroupFiles{"200000 100000\n", "4294967296\n", "0\n", "512\n", "domain\n"}
		for i, p := range []string{Root + "/docker/overlay2/worker-layer", layer, poolFixtureWGroup} {
			d := f.dirs[Root+"/core"]
			d.meta.Inode = uint64(10 + i)
			binary.LittleEndian.PutUint32(d.attrs[12:16], 200)
			if i == 2 {
				d.meta.FSType = 0x63677270
				d.meta.Major = 0
				d.meta.Minor = 28
				d.meta.MountID = 74
			}
			f.dirs[p] = d
		}
	}
	f.raw["/v1.56/containers/json?all=1"], _ = json.Marshal(ids)
	return f
}
func (f *poolFixture) event(s string) error {
	f.events = append(f.events, s)
	if f.hook != nil {
		return f.hook(s, len(f.events))
	}
	return nil
}
func (f *poolFixture) get(ctx context.Context, p string) ([]byte, error) {
	if e := f.event("get:" + p); e != nil {
		return nil, e
	}
	b, ok := f.raw[p]
	if !ok {
		return nil, errors.New("secret-canary")
	}
	return bytes.Clone(b), nil
}
func (f *poolFixture) now() time.Time {
	f.tick++
	return time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC).Add(time.Duration(f.tick) * time.Microsecond)
}
func (f *poolFixture) boot(context.Context) ([16]byte, error) { return poolFixture16, f.event("boot") }
func (f *poolFixture) namespace(_ context.Context, one bool, kind string) (uint64, error) {
	return 17, f.event(fmt.Sprintf("ns:%v:%s", one, kind))
}
func (f *poolFixture) mountinfo(context.Context) ([]byte, error) {
	return []byte("1 1 0:1 / / rw - ext4 /dev/root rw\n73 1 8:7 / " + Root + " rw - xfs /dev/synthetic rw,prjquota\n74 1 0:28 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n"), f.event("mountinfo")
}
func (f *poolFixture) open(_ context.Context, p string) (int, error) {
	if e := f.event("open:" + p); e != nil {
		return -1, e
	}
	if _, ok := f.dirs[p]; !ok {
		return -1, errors.New("fixture missing directory")
	}
	f.next++
	f.fds[f.next] = p
	return f.next, nil
}
func (f *poolFixture) procOpen(_ context.Context, pid uint64) (int, error) {
	s := poolPIDText(pid)
	if e := f.event("proc_open:" + s); e != nil {
		return -1, e
	}
	if f.processes[s] == nil {
		return -1, ErrPoolUnstable
	}
	f.next++
	f.fds[f.next] = "proc:" + s
	return f.next, nil
}
func (f *poolFixture) close(fd int) {
	f.closed[fd]++
	delete(f.fds, fd)
	f.events = append(f.events, "close")
}
func (f *poolFixture) stat(_ context.Context, fd int) (createdLayerMetadata, error) {
	p := f.fds[fd]
	return f.dirs[p].meta, f.event("stat:" + p)
}
func (f *poolFixture) geometry(_ context.Context, fd int, out *[256]byte) error {
	p := f.fds[fd]
	*out = f.dirs[p].geometry
	return f.event("geometry:" + p)
}
func (f *poolFixture) attrs(_ context.Context, fd int, out *[28]byte) error {
	p := f.fds[fd]
	*out = f.dirs[p].attrs
	return f.event("attrs:" + p)
}
func (f *poolFixture) quota(_ context.Context, fd int, id uint32, state bool, out []byte) error {
	p := f.fds[fd]
	if state {
		var q [160]byte
		q[0] = 1
		binary.LittleEndian.PutUint16(q[2:4], 0x30)
		copy(out, q[:])
	} else {
		q := poolFixtureQuota(id)
		copy(out, q[:])
	}
	return f.event(fmt.Sprintf("quota:%s:%d:%v", p, id, state))
}
func (f *poolFixture) statfs(_ context.Context, fd int) (PoolStatFSReadback, error) {
	return f.statfsValue, f.event("statfs:" + f.fds[fd])
}
func (f *poolFixture) control(_ context.Context, fd int, leaf string) ([]byte, error) {
	p := f.fds[fd]
	v := f.controls[p]
	m := map[string]string{"cpu.max": v.CPU, "memory.max": v.Memory, "memory.swap.max": v.Swap, "pids.max": v.PIDs, "cgroup.type": v.Type}
	s, ok := m[leaf]
	if !ok || s == "" {
		return nil, ErrPoolInvalid
	}
	return []byte(s), f.event("control:" + p + ":" + leaf)
}
func (f *poolFixture) process(_ context.Context, fd int, leaf string) ([]byte, error) {
	p := f.fds[fd]
	v := f.processes[strings.TrimPrefix(p, "proc:")][leaf]
	return bytes.Clone(v), f.event("process:" + p + ":" + leaf)
}
func (f *poolFixture) released(t *testing.T) {
	t.Helper()
	if len(f.fds) != 0 {
		t.Fatalf("FD leak: %v", f.fds)
	}
	for fd, n := range f.closed {
		if n != 1 {
			t.Fatalf("close(%d)=%d", fd, n)
		}
	}
}

// This test checks complete primitive data before a rejecting collector is called.
// Synthetic metadata and linux_syscall labels are not native Linux evidence.
func TestPoolFixtureComplete(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint(worker), func(t *testing.T) {
			f := newPoolFixture(worker)
			if _, e := poolPlan(f.plan); e != nil {
				t.Fatal(e)
			}
			api, ver, e := poolDecodeVersion(f.raw["/v1.56/version"])
			if e != nil || api != "1.56" || ver != "29.9.0" {
				t.Fatal(e)
			}
			info, e := poolDecodeInfo(f.raw["/v1.56/info"])
			if e != nil || info.CPUCount != 8 || info.MemoryBytes != 32<<30 {
				t.Fatal(e)
			}
			ids, e := poolDecodeList(f.raw["/v1.56/containers/json?all=1"])
			want := 1
			if worker {
				want = 2
			}
			if e != nil || len(ids) != want {
				t.Fatal(e)
			}
			for _, id := range ids {
				i, e := poolDecodeInspect(f.raw["/v1.56/containers/"+id+"/json"])
				if e != nil || i.ID != id || !i.Running || i.StartedAt.IsZero() {
					t.Fatal(e)
				}
			}
			mount, e := f.mountinfo(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			m, opts, e := poolMount(mount)
			if e != nil || m.id != 73 || !reflect.DeepEqual(opts, []string{"rw", "prjquota"}) {
				t.Fatalf("mount %v %v", opts, e)
			}
			for p, d := range f.dirs {
				if strings.HasPrefix(p, Root) {
					if !createdLayerMetadataValid(d.meta, m) {
						t.Fatal("metadata", p)
					}
					if u, e := createdLayerGeometry(&d.geometry); e != nil || u != poolFixture16 {
						t.Fatal("geometry", p, e)
					}
					for _, b := range d.attrs[20:] {
						if b != 0 {
							t.Fatal("attrs tail")
						}
					}
				}
			}
			for p, raw := range f.processes {
				pid := uint64(99)
				if p != "0" {
					var e error
					pid, e = createdLayerDecimal(p, 64)
					if e != nil {
						t.Fatal(e)
					}
				}
				ticks, g, e := poolProcess(raw["stat"], raw["cgroup"], pid)
				if e != nil || ticks != 123 || !strings.HasPrefix(g, "/sys/fs/cgroup/") {
					t.Fatalf("process %s ticks=%d group=%s %v", p, ticks, g, e)
				}
			}
			for p, c := range f.controls {
				for _, s := range []string{c.CPU, c.Memory, c.Swap, c.PIDs, c.Type} {
					if s == "" || len(s) > 128 || !strings.HasSuffix(s, "\n") {
						t.Fatal("cgroup", p)
					}
				}
			}
			for _, id := range []uint32{0, 100, 101, 200} {
				q := poolFixtureQuota(id)
				v, e := parseQuota(q[:], id)
				if e != nil || v.RawHex == "" || q[0] != 1 || q[1] != 2 || binary.LittleEndian.Uint32(q[4:8]) != id {
					t.Fatal(e)
				}
				if id > 0 && *v.HardBlocks != 20971520 {
					t.Fatal("hard blocks")
				}
				for _, b := range q[72:104] {
					if b != 0 {
						t.Fatal("quota reserved")
					}
				}
			}
			fd, e := f.open(context.Background(), Root+"/core")
			if e != nil {
				t.Fatal(e)
			}
			var qs [160]byte
			qs[0] = 1
			if e = f.quota(context.Background(), fd, 0, true, qs[:]); e != nil || qs[0] != 1 || binary.LittleEndian.Uint16(qs[2:4]) != 0x30 {
				t.Fatal(e)
			}
			f.close(fd)
			f.released(t)
		})
	}
}
func TestPoolCollectExecutable(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint(worker), func(t *testing.T) {
			f := newPoolFixture(worker)
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if e != nil {
				t.Fatal(e)
			}
			f.released(t)
			want := 1
			if worker {
				want = 2
			}
			if len(o.EngineBefore.Containers) != want || !poolSameCensus(o.EngineBefore, o.EngineAfter) || o.Core[0].Members[0].StartTimeTicksBefore != 123 || o.Core[0].Cgroup.Before.FSType != 0x63677270 || o.Anchor.Directory.Before.MountID != 73 || !o.Ended.After(o.Started) || len(o.Storage) != 2 {
				t.Fatalf("incomplete readbacks: %#v", o)
			}
			for n, q := range o.Storage {
				if *q.Quota.Target.HardBlocks != 20971520 || binary.LittleEndian.Uint32(q.Quota.Default0[4:8]) != 0 || len(q.Quota.StateSamples) != 3 || q.Directory.Before.CanonicalPath != f.plan.Storage[n].Path {
					t.Fatal("quota/raw path")
				}
				for i, s := range q.Quota.StateSamples {
					if s.Sequence != i+1 || s.Operation != []string{"qstatv", "target_quota", "default0_quota"}[i] || s.Source != "linux_syscall" || s.Outcome != "observed" || !s.Ended.After(s.Started) {
						t.Fatal("actual samples")
					}
				}
			}
			if worker && (len(o.Workers) != 1 || o.Workers[0].Process.PIDBefore != 202 || o.Workers[0].Layer.Before.CanonicalPath != Root+"/docker/overlay2/worker-layer/diff" || *o.Workers[0].Quota.Target.HardBlocks != 20971520) {
				t.Fatal("worker")
			}
			first, last := -1, -1
			for i, s := range f.events {
				if s == "get:/v1.56/version" {
					if first < 0 {
						first = i
					}
					last = i
				}
			}
			if first < 0 || last <= first || f.events[0] != "boot" {
				t.Fatalf("order: %v", f.events)
			}
			bad := newPoolFixture(worker)
			bad.raw["/v1.56/info"] = []byte(`{"ID":null}`)
			rejectPool(t, bad, ErrPoolInvalid)
		})
	}
}
func rejectPool(t *testing.T, f *poolFixture, want error) {
	t.Helper()
	o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
	if !errors.Is(e, want) || !reflect.DeepEqual(o, PoolObservation{}) {
		t.Fatalf("zero failure %v %#v", e, o)
	}
	f.released(t)
}

func TestPoolCollectOperationOrder(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(fmt.Sprint(worker), func(t *testing.T) {
			f := newPoolFixture(worker)
			o, e := collectPool(context.Background(), f.plan, f.get, f, f.now)
			if e != nil {
				t.Fatal(e)
			}
			position := func(s string, last bool) int {
				result := -1
				for n, ev := range f.events {
					if ev == s {
						result = n
						if !last {
							return n
						}
					}
				}
				if result < 0 {
					t.Fatal("operation absent", s)
				}
				return result
			}
			before := func(a string, alast bool, b string, blast bool) {
				t.Helper()
				if position(a, alast) >= position(b, blast) {
					t.Fatalf("wrong order %s before %s", a, b)
				}
			}
			before("mountinfo", false, "get:/v1.56/version", false)
			before("get:/v1.56/containers/"+poolFixtureCID+"/json", false, "open:"+Root, false)
			before("open:"+poolCoreRoot, false, "quota:"+Root+"/core:0:true", false)
			before("process:proc:101:cgroup", false, "quota:"+Root+"/core:0:true", false)
			expected := []string{"quota:" + Root + "/core:0:true", "quota:" + Root + "/core:100:false", "quota:" + Root + "/core:0:false", "quota:" + Root + "/product:0:true", "quota:" + Root + "/product:101:false", "quota:" + Root + "/product:0:false"}
			if worker {
				layer := Root + "/docker/overlay2/worker-layer/diff"
				before("attrs:"+layer, false, expected[0], false)
				expected = append(expected, "quota:"+layer+":0:true", "quota:"+layer+":200:false", "quota:"+layer+":0:false")
			}
			for n := 1; n < len(expected); n++ {
				before(expected[n-1], false, expected[n], false)
			}
			before(expected[len(expected)-1], false, "process:proc:101:stat", true)
			before("process:proc:101:stat", true, "statfs:"+Root+"/anchor", true)
			before("attrs:"+Root+"/product", true, "get:/v1.56/version", true)
			before("get:/v1.56/containers/"+poolFixtureCID+"/json", true, "boot", true)
			before("boot", true, "mountinfo", true)
			ticks := int64(14)
			if worker {
				ticks = 20
			}
			if f.tick != ticks || o.Ended.Sub(o.Started) != time.Duration(ticks-1)*time.Microsecond {
				t.Fatal("actual clock samples missing")
			}
			f.released(t)
		})
	}
}
