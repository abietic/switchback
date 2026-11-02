package capacitypublisher

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const syntheticUUID = "01020304-0506-0708-090a-0b0c0d0e0f10"
const syntheticCore = "/sys/fs/cgroup/switchback.slice/switchback-core.slice"
const syntheticWorker = "/sys/fs/cgroup/switchback.slice/switchback-workers.slice/worker.scope"

var syntheticCID = strings.Repeat("a", 64)
var syntheticImage = "sha256:" + strings.Repeat("b", 64)
var syntheticWorkerCID = strings.Repeat("c", 64)
var syntheticWorkerImage = "sha256:" + strings.Repeat("d", 64)
var synthetic16 = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

// Every byte here is SYNTHETIC. This builder performs no filesystem or native reads.
func fixture(worker bool) (capacitysnapshot.Trust, []CoreContainerBinding, capacityprobe.PoolObservation) {
	started := time.Now().Add(-time.Second)
	g := config.M1AccountedCapacityGuarantee()
	trust := capacitysnapshot.Trust{KeyID: "synthetic", PublicKey: make([]byte, 32), ManifestDigest: "sha256:" + strings.Repeat("e", 64), Identity: capacitysnapshot.Identity{EngineID: syntheticUUID, DockerRoot: "/pool/docker", FSUUID: syntheticUUID, GuestBootID: syntheticUUID, MountNamespace: "17", MountID: 73, Device: "8:7"}, AnchorPath: "/pool/anchor", Core: []capacitysnapshot.CoreDomain{{ID: "core", CgroupPath: syntheticCore, CPUQuotaMicros: 120000, CPUPeriodMicros: 100000, MemoryMax: 3 << 30, PIDsMax: 1024}}, AdmissionPolicy: &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}}
	for n, id := range []string{"core-data", "product"} {
		trust.Storage = append(trust.Storage, capacitysnapshot.StorageDomain{ID: id, Path: "/pool/" + id, FSUUID: syntheticUUID, Device: "8:7", MountID: 73, FSType: "xfs", XFlags: 0x200, ProjectID: uint64(100 + n), HardBytes: 10 << 30, Quota: capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: uint64(100 + n), HardBlocks512: 20971520}})
	}
	for _, role := range []string{"core", "images", "logs", "build"} {
		trust.Coverage = append(trust.Coverage, capacitysnapshot.StorageCoverage{Role: role, DomainID: "core-data"})
	}
	for _, role := range []string{"product", "working_copy", "cache"} {
		trust.Coverage = append(trust.Coverage, capacitysnapshot.StorageCoverage{Role: role, DomainID: "product"})
	}
	mount := []byte("1 1 0:1 / / rw - ext4 /dev/root rw\n73 1 8:7 / /pool rw,prjquota - xfs /dev/synthetic rw,prjquota\n")
	inspect := capacityprobe.PoolContainerInspect{ID: syntheticCID, ImageDigest: syntheticImage, Status: "running", PID: 101, Running: true, StartedAt: started.Add(-time.Minute), Labels: map[string]string{}, CgroupParent: "switchback-core.slice", NetworkMode: "bridge"}
	engine := capacityprobe.PoolEngineReadback{ID: syntheticUUID, APIVersion: "1.56", Version: "29.9.0", DockerRoot: "/pool/docker", Driver: "overlay2", CPUCount: 8, MemoryBytes: 32 << 30, Containers: []capacityprobe.PoolContainerInspect{inspect}}
	stat := capacityprobe.PoolStatFSReadback{Type: 0x58465342, BlockSize: 4096, FragmentSize: 4096, Blocks: 1 << 24, Free: 1 << 23, Available: 1 << 22}
	after := stat
	after.Available--
	o := capacityprobe.PoolObservation{Started: started, Ended: started.Add(100 * time.Millisecond), BootBefore: synthetic16, BootAfter: synthetic16, NamespaceBefore: 17, NamespaceAfter: 17, MountInfoBefore: mount, MountInfoAfter: append([]byte(nil), mount...), EngineBefore: engine, EngineAfter: engine, Anchor: capacityprobe.PoolAnchorReadback{Directory: syntheticDir("/pool/anchor", 1, 0), Before: stat, After: after, MountOptions: []string{"rw", "prjquota"}}, Core: []capacityprobe.PoolCoreReadback{{CoreID: "core", Cgroup: syntheticCG(syntheticCore, 1, "120000 100000", "3221225472", "1024"), Members: []capacityprobe.PoolProcessReadback{syntheticProcess(syntheticCID, 101, syntheticCore+"/core.slice/core.scope")}}}}
	for n, id := range []string{"core-data", "product"} {
		o.Storage = append(o.Storage, capacityprobe.PoolStorageReadback{DomainID: id, Directory: syntheticDir("/pool/"+id, uint64(2+n), uint32(100+n)), Quota: syntheticQuota(uint32(100+n), started)})
	}
	if worker {
		addWorker(&o)
	}
	return trust, []CoreContainerBinding{{ContainerID: syntheticCID, ImageDigest: syntheticImage, CoreID: "core"}}, o
}
func syntheticDir(path string, inode uint64, project uint32) capacityprobe.CreatedLayerDirectory {
	i := capacityprobe.CreatedLayerIdentity{CanonicalPath: path, Inode: inode, DeviceMajor: 8, DeviceMinor: 7, Mode: 0x41ed, MountID: 73, XFSUUID: synthetic16}
	d := capacityprobe.CreatedLayerDirectory{Before: i, After: i, NoFollowWalk: true, ReopenedNoFollow: true, IdentityRechecked: true, FilesystemUUIDSameFD: true}
	binary.LittleEndian.PutUint32(d.Geometry[0:4], 4096)
	binary.LittleEndian.PutUint32(d.Geometry[12:16], 4)
	binary.LittleEndian.PutUint64(d.Geometry[32:40], 1<<20)
	copy(d.Geometry[64:80], synthetic16[:])
	binary.LittleEndian.PutUint32(d.Geometry[88:92], 5)
	d.GeometryAfter = d.Geometry
	if project > 0 {
		binary.LittleEndian.PutUint32(d.FSXAttr[0:4], 0x200)
	}
	binary.LittleEndian.PutUint32(d.FSXAttr[12:16], project)
	d.FSXAttrAfter = d.FSXAttr
	return d
}
func syntheticQuota(project uint32, started time.Time) capacityprobe.PoolQuotaReadback {
	var raw [112]byte
	raw[0], raw[1] = 1, 2
	binary.LittleEndian.PutUint32(raw[4:8], project)
	binary.LittleEndian.PutUint64(raw[8:16], 20971520)
	binary.LittleEndian.PutUint64(raw[16:24], 20971520)
	binary.LittleEndian.PutUint64(raw[40:48], 8)
	binary.LittleEndian.PutUint64(raw[48:56], math.MaxUint64)
	h, u, ih, iu := uint64(20971520), uint64(8), uint64(0), uint64(math.MaxUint64)
	q := capacityprobe.PoolQuotaReadback{Target: capacityprobe.Quota{RawHex: hex.EncodeToString(raw[:]), Status: "observed", ABI: "linux_v6.8_fs_disk_quota_112_le", UnitBytes: 512, HardBlocks: &h, UsedBlocks: &u, HardInodes: &ih, UsedInodes: &iu}, StateSamples: []capacityprobe.CreatedLayerSample{{Sequence: 1, Operation: "qstatv", Source: "linux_syscall", Started: started.Add(time.Millisecond), Ended: started.Add(2 * time.Millisecond), Outcome: "observed"}}}
	q.QStatV[0] = 1
	binary.LittleEndian.PutUint16(q.QStatV[2:4], 0x30)
	q.Default0[0], q.Default0[1] = 1, 2
	return q
}
func syntheticCG(path string, inode uint64, cpu, memory, pids string) capacityprobe.PoolCgroupReadback {
	id := capacityprobe.PoolCgroupIdentity{FSType: 0x63677270, Device: 9, Inode: inode}
	f := capacityprobe.PoolCgroupFiles{CPU: cpu + "\n", Memory: memory + "\n", Swap: "0\n", PIDs: pids + "\n", Type: "domain\n"}
	return capacityprobe.PoolCgroupReadback{Path: path, Before: id, After: id, FilesBefore: f, FilesAfter: f}
}
func syntheticProcess(cid string, pid uint64, path string) capacityprobe.PoolProcessReadback {
	return capacityprobe.PoolProcessReadback{ContainerID: cid, PIDBefore: pid, PIDAfter: pid, StartTimeTicksBefore: 123, StartTimeTicksAfter: 123, CgroupPathBefore: path, CgroupPathAfter: path}
}
func addWorker(o *capacityprobe.PoolObservation) {
	resource, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	labels := map[string]string{"com.switchback.managed": "worker-runtime", "com.switchback.worker-session-id": "session-one", "com.switchback.worker-run-id": "run-one", "com.switchback.runtime-instance-id": "runtime-one", "com.switchback.runtime-generation": "1", "com.switchback.resource-profile": resource.ID + "@1:" + resource.Digest, "com.switchback.agent-profile": "agent@1:sha256:" + strings.Repeat("f", 64), "com.switchback.network-policy-id": network.ID, "com.switchback.network-policy-revision": "1", "com.switchback.network-policy-digest": network.Digest, "com.switchback.network-enforcement-status": "active"}
	i := capacityprobe.PoolContainerInspect{ID: syntheticWorkerCID, ImageDigest: syntheticWorkerImage, Status: "running", PID: 202, Running: true, StartedAt: o.Started.Add(-time.Minute), Labels: labels, CgroupParent: "switchback-workers.slice", NanoCPUs: 2e9, MemoryBytes: 4 << 30, MemorySwapBytes: 4 << 30, PIDsMax: 512, NetworkMode: "bridge", GraphDriver: "overlay2", UpperDir: "/pool/docker/overlay2/worker-layer/diff"}
	o.EngineBefore.Containers = append(o.EngineBefore.Containers, i)
	o.EngineAfter.Containers = append([]capacityprobe.PoolContainerInspect(nil), o.EngineBefore.Containers...)
	o.Workers = append(o.Workers, capacityprobe.PoolWorkerReadback{ContainerID: i.ID, Process: syntheticProcess(i.ID, i.PID, syntheticWorker), Cgroup: syntheticCG(syntheticWorker, 2, "200000 100000", "4294967296", "512"), Layer: syntheticDir(i.UpperDir, 4, 200), Quota: syntheticQuota(200, o.Started)})
}
func newFixtureMapper(t *testing.T, worker bool) (*Mapper, capacityprobe.PoolObservation) {
	t.Helper()
	tr, c, o := fixture(worker)
	m, e := NewMapper(tr, c, syntheticWorkerImage)
	if e != nil {
		t.Fatal(e)
	}
	return m, o
}
func TestMapperExecutableZeroWorker(t *testing.T) {
	tr, c, o := fixture(false)
	m, e := NewMapper(tr, c, syntheticWorkerImage)
	if e != nil {
		t.Fatal(e)
	}
	before := deepClone(o)
	r, e := m.Map(context.Background(), o, 7)
	if e != nil {
		t.Fatal(e)
	}
	expectedStorage := append([]capacitysnapshot.StorageDomain(nil), tr.Storage...)
	for n := range expectedStorage {
		expectedStorage[n].UsedBytes = 4096
		expectedStorage[n].Quota.UsedBlocks512 = 8
	}
	expected := capacitysnapshot.Facts{Sequence: 7, ObservedAt: o.Started, Identity: tr.Identity, ManifestDigest: tr.ManifestDigest, EngineCPUMilli: 8000, EngineMemoryBytes: 34359738368, Anchor: capacitysnapshot.Anchor{Path: "/pool/anchor", FSUUID: syntheticUUID, MountID: 73, Device: "8:7", FSType: "xfs", MountOptions: []string{"rw", "prjquota"}, AvailableBytes: 17179865088}, Core: tr.Core, Storage: expectedStorage, Containers: []capacitysnapshot.Container{}, Removals: []capacitysnapshot.Removal{}}
	if !reflect.DeepEqual(r.Facts, expected) {
		t.Fatalf("facts mismatch: %#v", r.Facts)
	}
	for _, id := range []string{"core-data", "product"} {
		if r.StorageInodes[id] != (capacitysnapshot.CreatedLayerInodeObservation{HardInodes: 0, UsedInodes: math.MaxUint64, Assessment: capacitysnapshot.InodeBoundUnproven}) {
			t.Fatalf("inode %s: %#v", id, r.StorageInodes[id])
		}
	}
	if len(r.LayerInodes) != 0 || !reflect.DeepEqual(o, before) {
		t.Fatal("source changed or layers invented")
	}
}
func TestMapperExecutableWorkerUint64(t *testing.T) {
	m, o := newFixtureMapper(t, true)
	before := deepClone(o)
	r, e := m.Map(context.Background(), o, 9)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Facts.Containers) != 1 || len(r.Facts.Removals) != 0 {
		t.Fatal("census")
	}
	c := r.Facts.Containers[0]
	if c.ContainerID != syntheticWorkerCID || c.Layer.HardBytes != 10737418240 || c.Layer.UsedBytes != 4096 || c.Layer.ID != "layer-runtime-one" || c.CPUQuotaMicros != 200000 || c.CPUPeriodMicros != 100000 || c.NanoCPUs != 2000000000 || c.MemoryMax != 4294967296 || c.PIDsMax != 512 {
		t.Fatalf("worker %#v", c)
	}
	if r.LayerInodes[syntheticWorkerCID] != (capacitysnapshot.CreatedLayerInodeObservation{HardInodes: 0, UsedInodes: math.MaxUint64, Assessment: capacitysnapshot.InodeBoundUnproven}) {
		t.Fatal("uint64 narrowed")
	}
	if !reflect.DeepEqual(o, before) {
		t.Fatal("source changed")
	}
}

// Reflection clone preserves nils, times and complete raw arrays without a wire codec.
func deepClone(o capacityprobe.PoolObservation) capacityprobe.PoolObservation {
	return cloneValue(reflect.ValueOf(o)).Interface().(capacityprobe.PoolObservation)
}
func cloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.New(v.Type().Elem())
		r.Elem().Set(cloneValue(v.Elem()))
		return r
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			r.Index(i).Set(cloneValue(v.Index(i)))
		}
		return r
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.MakeMap(v.Type())
		it := v.MapRange()
		for it.Next() {
			r.SetMapIndex(it.Key(), cloneValue(it.Value()))
		}
		return r
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return v
		}
		r := reflect.New(v.Type()).Elem()
		for i := 0; i < v.NumField(); i++ {
			r.Field(i).Set(cloneValue(v.Field(i)))
		}
		return r
	default:
		return v
	}
}

func TestMapperConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*capacitysnapshot.Trust, *[]CoreContainerBinding, *string)
	}{
		{"nil_policy", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) { tr.AdmissionPolicy = nil }}, {"legacy_policy", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) {
			tr.AdmissionPolicy.ID = "legacy"
		}}, {"policy_revision", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) {
			tr.AdmissionPolicy.Revision = 2
		}}, {"policy_digest", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) {
			tr.AdmissionPolicy.Digest = "sha256:" + strings.Repeat("0", 64)
		}}, {"duplicate_binding", func(_ *capacitysnapshot.Trust, c *[]CoreContainerBinding, _ *string) { *c = append(*c, (*c)[0]) }}, {"unknown_core", func(_ *capacitysnapshot.Trust, c *[]CoreContainerBinding, _ *string) { (*c)[0].CoreID = "unknown" }}, {"bad_core_image", func(_ *capacitysnapshot.Trust, c *[]CoreContainerBinding, _ *string) { (*c)[0].ImageDigest = "bad" }}, {"bad_cid", func(_ *capacitysnapshot.Trust, c *[]CoreContainerBinding, _ *string) { (*c)[0].ContainerID = "bad" }}, {"empty_core_bindings", func(_ *capacitysnapshot.Trust, c *[]CoreContainerBinding, _ *string) { *c = nil }}, {"binding_limit", func(_ *capacitysnapshot.Trust, c *[]CoreContainerBinding, _ *string) {
			*c = make([]CoreContainerBinding, 33)
		}}, {"worker_image", func(_ *capacitysnapshot.Trust, _ *[]CoreContainerBinding, s *string) { *s = "bad" }}, {"trust_key", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) { tr.PublicKey = nil }}, {"trust_core_limit", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) {
			tr.Core = make([]capacitysnapshot.CoreDomain, 33)
		}}, {"trust_storage_limit", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) {
			tr.Storage = make([]capacitysnapshot.StorageDomain, 33)
		}}, {"missing_coverage", func(tr *capacitysnapshot.Trust, _ *[]CoreContainerBinding, _ *string) { tr.Coverage = nil }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, c, _ := fixture(false)
			image := syntheticWorkerImage
			tc.mutate(&tr, &c, &image)
			m, e := NewMapper(tr, c, image)
			if e == nil || m != nil {
				t.Fatalf("accepted: %v", e)
			}
		})
	}
}
func TestMapperDetachedConfigurationAndResults(t *testing.T) {
	tr, c, o := fixture(true)
	m, e := NewMapper(tr, c, syntheticWorkerImage)
	if e != nil {
		t.Fatal(e)
	}
	tr.KeyID = "changed"
	tr.PublicKey[0] = 99
	tr.ManifestDigest = "bad"
	tr.Identity.EngineID = "bad"
	tr.AnchorPath = "/bad"
	tr.Core[0].MemoryMax = 1
	tr.Storage[0].HardBytes = 1
	tr.Coverage[0].Role = "bad"
	tr.AdmissionPolicy.ID = "bad"
	c[0].ContainerID = "bad"
	before := deepClone(o)
	r, e := m.Map(context.Background(), o, 1)
	if e != nil {
		t.Fatal(e)
	}
	r.Facts.Anchor.MountOptions[0] = "bad"
	r.Facts.Core[0].MemoryMax = 1
	r.Facts.Storage[0].HardBytes = 1
	r.Facts.Containers[0].Layer.HardBytes = 1
	r.StorageInodes["bad"] = capacitysnapshot.CreatedLayerInodeObservation{}
	delete(r.LayerInodes, syntheticWorkerCID)
	again, e := m.Map(context.Background(), o, 1)
	if e != nil {
		t.Fatal(e)
	}
	if again.Facts.Anchor.MountOptions[0] != "rw" || again.Facts.Core[0].MemoryMax != 3221225472 || again.Facts.Storage[0].HardBytes != 10737418240 || again.Facts.Containers[0].Layer.HardBytes != 10737418240 || len(again.StorageInodes) != 2 || len(again.LayerInodes) != 1 || !reflect.DeepEqual(o, before) {
		t.Fatal("alias shared")
	}
}
func mutateInspect(o *capacityprobe.PoolObservation, n int, fn func(*capacityprobe.PoolContainerInspect)) {
	fn(&o.EngineBefore.Containers[n])
	o.EngineAfter = deepClone(*o).EngineBefore
}
func TestMapperCensus(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*capacityprobe.PoolObservation)
	}{
		{"unknown", func(o *capacityprobe.PoolObservation) {
			i := o.EngineBefore.Containers[1]
			i.ID = strings.Repeat("f", 64)
			o.EngineBefore.Containers = append(o.EngineBefore.Containers, i)
			o.EngineAfter.Containers = append([]capacityprobe.PoolContainerInspect(nil), o.EngineBefore.Containers...)
		}}, {"duplicate", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.Containers = append(o.EngineBefore.Containers, o.EngineBefore.Containers[0])
			o.EngineAfter.Containers = o.EngineBefore.Containers
		}}, {"missing", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.Containers = o.EngineBefore.Containers[1:]
			o.EngineAfter.Containers = o.EngineBefore.Containers
		}}, {"missing_worker", func(o *capacityprobe.PoolObservation) { o.Workers = nil }}, {"extra_worker", func(o *capacityprobe.PoolObservation) { o.Workers = append(o.Workers, o.Workers[0]) }}, {"core_worker_overlap", func(o *capacityprobe.PoolObservation) { o.Workers[0].ContainerID = syntheticCID }}, {"labels_as_core", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 0, func(i *capacityprobe.PoolContainerInspect) {
				i.Labels = map[string]string{"com.switchback.worker-session-id": "session"}
			})
		}}, {"managed_core", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 0, func(i *capacityprobe.PoolContainerInspect) {
				i.Labels = map[string]string{"com.switchback.managed": "worker-runtime"}
			})
		}}, {"core_image", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 0, func(i *capacityprobe.PoolContainerInspect) { i.ImageDigest = syntheticWorkerImage })
		}}, {"worker_image", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.ImageDigest = syntheticImage })
		}}, {"worker_exited", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Status = "exited"; i.Running = false })
		}}, {"worker_created", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Status = "created"; i.Running = false; i.PID = 0 })
		}}, {"paused", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Paused = true })
		}}, {"restarting", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Restarting = true })
		}}, {"dead", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Dead = true })
		}}, {"no_pid", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.PID = 0 })
		}}, {"started_late", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.StartedAt = o.Started.Add(time.Nanosecond) })
		}}, {"started_zero", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.StartedAt = time.Time{} })
		}}, {"privileged", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Privileged = true })
		}}, {"host_network", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.NetworkMode = "host" })
		}}, {"cpu_hostconfig", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.NanoCPUs-- })
		}}, {"memory_hostconfig", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.MemoryBytes-- })
		}}, {"swap_hostconfig", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.MemorySwapBytes-- })
		}}, {"pids_hostconfig", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.PIDsMax-- })
		}}, {"census_drift", func(o *capacityprobe.PoolObservation) {
			o.EngineAfter = deepClone(*o).EngineAfter
			o.EngineAfter.Containers[1].PID++
		}}, {"label_drift", func(o *capacityprobe.PoolObservation) {
			o.EngineAfter = deepClone(*o).EngineAfter
			o.EngineAfter.Containers[1].Labels["com.switchback.worker-run-id"] = "different"
		}}, {"inventory_limit", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.Containers = make([]capacityprobe.PoolContainerInspect, 33)
		}}, {"oversize_label", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Labels["oversize"] = strings.Repeat("a", 513) })
		}}, {"label_count", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) {
				for n := 0; n < 33; n++ {
					i.Labels[string(rune('a'+n))] = "v"
				}
			})
		}}, {"label_nul", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Labels["com.switchback.worker-run-id"] = "bad\x00label" })
		}}, {"core_id", func(o *capacityprobe.PoolObservation) { o.Core[0].CoreID = "unknown" }}, {"storage_missing", func(o *capacityprobe.PoolObservation) { o.Storage = o.Storage[:1] }}, {"storage_duplicate", func(o *capacityprobe.PoolObservation) { o.Storage[1] = o.Storage[0] }}, {"storage_unknown", func(o *capacityprobe.PoolObservation) { o.Storage[0].DomainID = "unknown" }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { m, o := newFixtureMapper(t, true); tc.mutate(&o); rejectObservation(t, m, o) })
	}
	for _, key := range []string{"managed", "worker-session-id", "worker-run-id", "runtime-instance-id", "runtime-generation", "resource-profile", "agent-profile", "network-policy-id", "network-policy-revision", "network-policy-digest", "network-enforcement-status"} {
		t.Run("label_"+key, func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			mutateInspect(&o, 1, func(i *capacityprobe.PoolContainerInspect) {
				i.Labels["com.switchback."+key] = "wrong"
				if key == "worker-session-id" || key == "worker-run-id" || key == "runtime-instance-id" {
					i.Labels["com.switchback."+key] = ""
				}
			})
			rejectObservation(t, m, o)
		})
	}
	for _, generation := range []string{"0", "01", "-1", "0x1", "9223372036854775808"} {
		t.Run("generation_"+generation, func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			mutateInspect(&o, 1, func(i *capacityprobe.PoolContainerInspect) {
				i.Labels["com.switchback.runtime-generation"] = generation
			})
			rejectObservation(t, m, o)
		})
	}
}
func TestMapperCensusOrderAndTwoWorkers(t *testing.T) {
	m, o := newFixtureMapper(t, true)
	w := deepClone(o).Workers[0]
	w.ContainerID = strings.Repeat("f", 64)
	w.Process.ContainerID = w.ContainerID
	w.Process.PIDBefore = 303
	w.Process.PIDAfter = 303
	w.Process.CgroupPathBefore = syntheticWorker + "-two"
	w.Process.CgroupPathAfter = w.Process.CgroupPathBefore
	w.Cgroup.Path = w.Process.CgroupPathBefore
	w.Cgroup.Before.Inode = 3
	w.Cgroup.After = w.Cgroup.Before
	w.Layer = syntheticDir("/pool/docker/overlay2/worker-two/diff", 5, 201)
	w.Quota = syntheticQuota(201, o.Started)
	o.Workers = append(o.Workers, w)
	i := deepClone(o).EngineBefore.Containers[1]
	i.ID = w.ContainerID
	i.PID = 303
	i.UpperDir = w.Layer.Before.CanonicalPath
	i.Labels["com.switchback.runtime-instance-id"] = "runtime-two"
	i.Labels["com.switchback.worker-session-id"] = "session-two"
	o.EngineBefore.Containers = append(o.EngineBefore.Containers, i)
	o.EngineAfter = deepClone(o).EngineBefore
	o.EngineAfter.Containers[0], o.EngineAfter.Containers[2] = o.EngineAfter.Containers[2], o.EngineAfter.Containers[0]
	o.Storage[0], o.Storage[1] = o.Storage[1], o.Storage[0]
	o.Workers[0], o.Workers[1] = o.Workers[1], o.Workers[0]
	before := deepClone(o)
	r, e := m.Map(context.Background(), o, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Facts.Containers) != 2 || r.Facts.Containers[0].ContainerID != syntheticWorkerCID || r.Facts.Containers[1].ContainerID != strings.Repeat("f", 64) || r.Facts.Storage[0].ID != "core-data" || !reflect.DeepEqual(o, before) {
		t.Fatal("not stable detached ordering")
	}
}

func TestMapperIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*capacityprobe.PoolObservation)
	}{
		{"boot_zero", func(o *capacityprobe.PoolObservation) { o.BootBefore = [16]byte{}; o.BootAfter = o.BootBefore }}, {"boot_drift", func(o *capacityprobe.PoolObservation) { o.BootAfter[0]++ }}, {"boot_wrong", func(o *capacityprobe.PoolObservation) { o.BootBefore[0]++; o.BootAfter = o.BootBefore }}, {"namespace_zero", func(o *capacityprobe.PoolObservation) { o.NamespaceBefore = 0; o.NamespaceAfter = 0 }}, {"namespace_drift", func(o *capacityprobe.PoolObservation) { o.NamespaceAfter++ }}, {"namespace_wrong", func(o *capacityprobe.PoolObservation) { o.NamespaceBefore++; o.NamespaceAfter = o.NamespaceBefore }}, {"mount_drift", func(o *capacityprobe.PoolObservation) { o.MountInfoAfter = append(o.MountInfoAfter, 'x') }}, {"engine_cpu_zero", func(o *capacityprobe.PoolObservation) { o.EngineBefore.CPUCount = 0; o.EngineAfter.CPUCount = 0 }}, {"engine_cpu_overflow", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.CPUCount = math.MaxInt64/1000 + 1
			o.EngineAfter.CPUCount = o.EngineBefore.CPUCount
		}}, {"engine_memory_overflow", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.MemoryBytes = math.MaxInt64 + 1
			o.EngineAfter.MemoryBytes = o.EngineBefore.MemoryBytes
		}}, {"engine_uuid", func(o *capacityprobe.PoolObservation) { o.EngineBefore.ID = "wrong"; o.EngineAfter.ID = "wrong" }}, {"engine_api", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.APIVersion = "1.55"
			o.EngineAfter.APIVersion = "1.55"
		}}, {"engine_version", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.Version = "29.9"
			o.EngineAfter.Version = "29.9"
		}}, {"engine_root", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.DockerRoot = "/other"
			o.EngineAfter.DockerRoot = "/other"
		}}, {"engine_driver", func(o *capacityprobe.PoolObservation) {
			o.EngineBefore.Driver = "other"
			o.EngineAfter.Driver = "other"
		}}, {"engine_units_drift", func(o *capacityprobe.PoolObservation) { o.EngineAfter.CPUCount++ }},
		{"directory_path", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.CanonicalPath = "/other"
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_inode_zero", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.Inode = 0
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.After.Inode++ }}, {"directory_alias", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.Inode = o.Anchor.Directory.Before.Inode
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_mode", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.Mode = 0x81ed
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_device", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.DeviceMinor++
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_mount", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.MountID++
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_uuid", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Before.XFSUUID[0]++
			o.Workers[0].Layer.After = o.Workers[0].Layer.Before
		}}, {"directory_walk", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.NoFollowWalk = false }}, {"directory_reopen", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.ReopenedNoFollow = false }}, {"directory_recheck", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.IdentityRechecked = false }}, {"directory_same_fd", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.FilesystemUUIDSameFD = false }}, {"geometry_version", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Geometry[88] = 4
			o.Workers[0].Layer.GeometryAfter = o.Workers[0].Layer.Geometry
		}}, {"geometry_uuid", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Geometry[64]++
			o.Workers[0].Layer.GeometryAfter = o.Workers[0].Layer.Geometry
		}}, {"geometry_tail", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.Geometry[255] = 1
			o.Workers[0].Layer.GeometryAfter = o.Workers[0].Layer.Geometry
		}}, {"geometry_blocksize", func(o *capacityprobe.PoolObservation) {
			binary.LittleEndian.PutUint32(o.Workers[0].Layer.Geometry[0:4], 1025)
			o.Workers[0].Layer.GeometryAfter = o.Workers[0].Layer.Geometry
		}}, {"geometry_ag", func(o *capacityprobe.PoolObservation) {
			binary.LittleEndian.PutUint32(o.Workers[0].Layer.Geometry[12:16], 0)
			o.Workers[0].Layer.GeometryAfter = o.Workers[0].Layer.Geometry
		}}, {"geometry_data", func(o *capacityprobe.PoolObservation) {
			binary.LittleEndian.PutUint64(o.Workers[0].Layer.Geometry[32:40], 0)
			o.Workers[0].Layer.GeometryAfter = o.Workers[0].Layer.Geometry
		}}, {"geometry_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.GeometryAfter[32]++ }}, {"fsx_padding", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.FSXAttr[27] = 1
			o.Workers[0].Layer.FSXAttrAfter = o.Workers[0].Layer.FSXAttr
		}}, {"fsx_realtime", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.FSXAttr[0] = 1
			o.Workers[0].Layer.FSXAttrAfter = o.Workers[0].Layer.FSXAttr
		}}, {"fsx_no_inherit", func(o *capacityprobe.PoolObservation) {
			o.Workers[0].Layer.FSXAttr[1] = 0
			o.Workers[0].Layer.FSXAttrAfter = o.Workers[0].Layer.FSXAttr
		}}, {"fsx_project_drift", func(o *capacityprobe.PoolObservation) { o.Workers[0].Layer.FSXAttrAfter[12]++ }}, {"project_shared", func(o *capacityprobe.PoolObservation) {
			binary.LittleEndian.PutUint32(o.Workers[0].Layer.FSXAttr[12:16], 100)
			o.Workers[0].Layer.FSXAttrAfter = o.Workers[0].Layer.FSXAttr
		}}, {"anchor_project", func(o *capacityprobe.PoolObservation) {
			o.Anchor.Directory.FSXAttr[12] = 1
			o.Anchor.Directory.FSXAttrAfter = o.Anchor.Directory.FSXAttr
		}}, {"anchor_inherit", func(o *capacityprobe.PoolObservation) {
			o.Anchor.Directory.FSXAttr[1] = 2
			o.Anchor.Directory.FSXAttrAfter = o.Anchor.Directory.FSXAttr
		}},
		{"anchor_type", func(o *capacityprobe.PoolObservation) { o.Anchor.Before.Type = 1 }}, {"anchor_units", func(o *capacityprobe.PoolObservation) { o.Anchor.Before.FragmentSize = 1024 }}, {"anchor_zero_units", func(o *capacityprobe.PoolObservation) { o.Anchor.Before.BlockSize = 0 }}, {"anchor_free", func(o *capacityprobe.PoolObservation) { o.Anchor.Before.Free = o.Anchor.Before.Blocks + 1 }}, {"anchor_available", func(o *capacityprobe.PoolObservation) { o.Anchor.Before.Available = o.Anchor.Before.Free + 1 }}, {"anchor_overflow", func(o *capacityprobe.PoolObservation) {
			o.Anchor.Before.Blocks = math.MaxUint64
			o.Anchor.After.Blocks = math.MaxUint64
		}}, {"anchor_options", func(o *capacityprobe.PoolObservation) { o.Anchor.MountOptions = []string{"rw"} }}, {"anchor_options_invented", func(o *capacityprobe.PoolObservation) { o.Anchor.MountOptions = []string{"ro", "prjquota"} }}, {"layer_layout", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.UpperDir = "/pool/docker/overlay2/l/diff" })
		}}, {"layer_inspect_mismatch", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.UpperDir = "/pool/docker/overlay2/other/diff" })
		}}, {"layer_driver", func(o *capacityprobe.PoolObservation) {
			mutateInspect(o, 1, func(i *capacityprobe.PoolContainerInspect) { i.GraphDriver = "other" })
		}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { m, o := newFixtureMapper(t, true); tc.mutate(&o); rejectObservation(t, m, o) })
	}
	for _, tc := range []struct{ name, raw string }{{"no_newline", "1 1 8:7 / /pool rw - xfs dev rw"}, {"nul", "1 1 8:7 / /pool rw - xfs dev rw\x00\n"}, {"double_separator", "1 1 8:7 / /pool rw - xfs dev rw - x\n"}, {"empty_token", "1 1 8:7 / /pool  rw - xfs dev rw\n"}, {"bad_id", "-1 1 8:7 / /pool rw - xfs dev rw\n"}, {"bad_device", "1 1 0x8:7 / /pool rw - xfs dev rw\n"}, {"bad_escape", "1 1 8:7 / /pool\\999 rw - xfs dev rw\n"}, {"unsafe_escape", "1 1 8:7 / /pool\\011 rw - xfs dev rw\n"}, {"duplicate_id", "73 1 8:7 / /pool rw,prjquota - xfs dev rw\n73 1 8:7 / /pool rw,prjquota - xfs dev rw\n"}, {"ambiguous", "73 1 8:7 / /pool rw,prjquota - xfs dev rw\n74 1 8:7 / /pool rw,prjquota - xfs dev rw\n"}, {"overmount", "73 1 8:7 / /pool rw,prjquota - xfs dev rw\n74 73 8:8 / /pool/docker rw,prjquota - xfs dev rw\n"}, {"wrong_root", "73 1 8:7 /sub /pool rw,prjquota - xfs dev rw\n"}, {"wrong_fs", "73 1 8:7 / /pool rw,prjquota - ext4 dev rw\n"}, {"quota_off", "73 1 8:7 / /pool rw - xfs dev rw\n"}, {"oversize", strings.Repeat("a", capacityprobe.MaxResponse+1)}} {
		t.Run("mount_"+tc.name, func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			o.MountInfoBefore = []byte(tc.raw)
			o.MountInfoAfter = []byte(tc.raw)
			rejectObservation(t, m, o)
		})
	}
}
func TestMapperAnchorZeroAvailable(t *testing.T) {
	m, o := newFixtureMapper(t, false)
	o.Anchor.After.Available = 0
	r, e := m.Map(context.Background(), o, 1)
	if e != nil || r.Facts.Anchor.AvailableBytes != 0 {
		t.Fatalf("zero available: %#v %v", r, e)
	}
}
func moveWindow(o *capacityprobe.PoolObservation, start, end time.Time) {
	delta := start.Sub(o.Started)
	o.Started, o.Ended = start, end
	for n := range o.Storage {
		for j := range o.Storage[n].Quota.StateSamples {
			o.Storage[n].Quota.StateSamples[j].Started = o.Storage[n].Quota.StateSamples[j].Started.Add(delta)
			o.Storage[n].Quota.StateSamples[j].Ended = o.Storage[n].Quota.StateSamples[j].Ended.Add(delta)
		}
	}
}
func TestMapperClock(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		start, end time.Time
		ok         bool
	}{{"age_equal", now.Add(-30 * time.Second), now.Add(-29 * time.Second), true}, {"age_excess", now.Add(-30*time.Second - time.Nanosecond), now.Add(-29 * time.Second), false}, {"duration_equal", now.Add(-30 * time.Second), now, true}, {"duration_excess", now.Add(-30 * time.Second), now.Add(time.Nanosecond), false}, {"future_end_equal", now, now.Add(2 * time.Second), true}, {"future_end_excess", now, now.Add(2*time.Second + time.Nanosecond), false}, {"future_start_equal", now.Add(2 * time.Second), now.Add(2*time.Second + time.Nanosecond), false}, {"future_start_excess", now.Add(2*time.Second + time.Nanosecond), now.Add(2*time.Second + 2*time.Nanosecond), false}, {"zero_interval", now, now, false}, {"reversed", now, now.Add(-time.Second), false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, o := newFixtureMapper(t, false)
			moveWindow(&o, tc.start, tc.end)
			r, e := m.mapAt(context.Background(), o, 1, now)
			if tc.ok {
				if e != nil || r.Facts.ObservedAt != tc.start {
					t.Fatalf("%v %#v", e, r)
				}
			} else if e == nil || !reflect.DeepEqual(r, Result{}) {
				t.Fatalf("nonzero rejection %v %#v", e, r)
			}
		})
	}
}

// This context becomes cancelled only at the second check, after local parsing.
type terminalCancelContext struct {
	context.Context
	checks int
}

func (c *terminalCancelContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}
func TestMapperContextAndZeroValues(t *testing.T) {
	m, o := newFixtureMapper(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name      string
		m         *Mapper
		ctx       context.Context
		seq       int64
		cancelled bool
	}{{"nil_context", m, nil, 1, false}, {"cancelled", m, ctx, 1, true}, {"nil_mapper", nil, context.Background(), 1, false}, {"zero_mapper", &Mapper{}, context.Background(), 1, false}, {"zero_sequence", m, context.Background(), 0, false}, {"negative_sequence", m, context.Background(), -1, false}, {"late_cancellation", m, &terminalCancelContext{Context: context.Background()}, 1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			r, e := tc.m.Map(tc.ctx, o, tc.seq)
			if e == nil || !reflect.DeepEqual(r, Result{}) {
				t.Fatalf("%v %#v", e, r)
			}
			if tc.cancelled && !errors.Is(e, context.Canceled) {
				t.Fatalf("cancellation lost %v", e)
			}
		})
	}
}

func TestMapperRuntimeIdentityGrammar(t *testing.T) {
	for _, value := range []string{"_bad", "/path", "with space", "a@b", strings.Repeat("a", 161)} {
		t.Run(value[:min(len(value), 20)], func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			mutateInspect(&o, 1, func(i *capacityprobe.PoolContainerInspect) { i.Labels["com.switchback.runtime-instance-id"] = value })
			rejectObservation(t, m, o)
		})
	}
}
func TestMapperFinalNoOverlap(t *testing.T) {
	m, o := newFixtureMapper(t, true)
	w := &o.Workers[0]
	w.Cgroup.Path = syntheticCore + "/worker.scope"
	w.Process.CgroupPathBefore = w.Cgroup.Path
	w.Process.CgroupPathAfter = w.Cgroup.Path
	rejectObservation(t, m, o)
}
func TestMapperSuperblockQuotaAndKernelEscapes(t *testing.T) {
	m, o := newFixtureMapper(t, false)
	o.MountInfoBefore = []byte("1 1 0:1 / / rw - ext4 /dev/root rw\n73 1 8:7 / /pool rw - xfs /dev/synthetic rw,prjquota\n74 1 0:2 / /unrelated\\040space\\134path rw - tmpfs tmpfs rw\n")
	o.MountInfoAfter = append([]byte(nil), o.MountInfoBefore...)
	r, e := m.Map(context.Background(), o, 1)
	if e != nil || !reflect.DeepEqual(r.Facts.Anchor.MountOptions, []string{"rw", "prjquota"}) {
		t.Fatalf("kernel mount options/escaping %v %#v", e, r)
	}
}

func TestMapperLateCancellationOnRejectedObservation(t *testing.T) {
	m, o := newFixtureMapper(t, true)
	o.Workers[0].Quota.Target.RawHex = "invalid"
	ctx := &terminalCancelContext{Context: context.Background()}
	r, e := m.Map(ctx, o, 1)
	if !errors.Is(e, context.Canceled) || !reflect.DeepEqual(r, Result{}) || ctx.checks != 2 {
		t.Fatalf("after-check did not discard rejection: %v %#v checks=%d", e, r, ctx.checks)
	}
}
func TestMapperFinalStorageNoOverlap(t *testing.T) {
	tr, c, o := fixture(true)
	tr.Storage[0].Path = "/pool/docker/overlay2"
	o.Storage[0].Directory = syntheticDir("/pool/docker/overlay2", 2, 100)
	m, e := NewMapper(tr, c, syntheticWorkerImage)
	if e != nil {
		t.Fatal(e)
	}
	rejectObservation(t, m, o)
}
