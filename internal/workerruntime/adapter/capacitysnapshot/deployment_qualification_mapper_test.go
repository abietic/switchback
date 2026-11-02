package capacitysnapshot_test

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitypublisher"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQualifiedDecimalNamespaceComposesWithOriginalMapper(t *testing.T) {
	tr, binding := capacitysnapshot.QualificationFixtureForMapperTest(t)
	ns, e := strconv.ParseUint(tr.Identity.MountNamespace, 10, 64)
	if e != nil {
		t.Fatal("qualified namespace must retain decimal contract", e)
	}
	core := []capacitypublisher.CoreContainerBinding{}
	for _, b := range binding.Core {
		core = append(core, capacitypublisher.CoreContainerBinding{ContainerID: b.ContainerID, ImageDigest: b.ImageDigest, CoreID: b.CoreID})
	}
	mapper, e := capacitypublisher.NewMapper(tr, core, binding.WorkerImageDigest)
	if e != nil {
		t.Fatal(e)
	}
	o := qualifiedPoolObservation(t, tr, binding, ns)
	result, e := mapper.Map(context.Background(), o, 7)
	if e != nil {
		t.Fatal("qualified candidate cannot map", e)
	}
	if result.Facts.Sequence != 7 || result.Facts.Identity != tr.Identity || result.Facts.ManifestDigest != tr.ManifestDigest {
		t.Fatal("mapping changed qualified identity/actual sequence")
	}
	o.NamespaceBefore++
	o.NamespaceAfter = o.NamespaceBefore
	if _, e = mapper.Map(context.Background(), o, 8); e == nil {
		t.Fatal("different namespace mapped")
	}
}

// Every readback is synthetic and contained in this test. No collector, signer,
// Store, Engine, filesystem or capacity publication is invoked.
func qualifiedPoolObservation(t *testing.T, tr capacitysnapshot.Trust, b capacitysnapshot.DeploymentPublisherBinding, ns uint64) capacityprobe.PoolObservation {
	t.Helper()
	uuid := func(s string) (v [16]byte) {
		raw, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
		if e != nil || len(raw) != 16 {
			t.Fatal("uuid", s)
		}
		copy(v[:], raw)
		return
	}
	fsuuid, boot := uuid(tr.Identity.FSUUID), uuid(tr.Identity.GuestBootID)
	var major, minor uint32
	if _, e := fmt.Sscanf(tr.Identity.Device, "%d:%d", &major, &minor); e != nil {
		t.Fatal(e)
	}
	started := time.Now().Add(-time.Second)
	directory := func(p string, inode, project uint64) capacityprobe.CreatedLayerDirectory {
		i := capacityprobe.CreatedLayerIdentity{CanonicalPath: p, Inode: inode, DeviceMajor: major, DeviceMinor: minor, Mode: 0040755, MountID: tr.Identity.MountID, XFSUUID: fsuuid}
		d := capacityprobe.CreatedLayerDirectory{Before: i, After: i, NoFollowWalk: true, ReopenedNoFollow: true, IdentityRechecked: true, FilesystemUUIDSameFD: true}
		binary.LittleEndian.PutUint32(d.Geometry[:4], 4096)
		binary.LittleEndian.PutUint32(d.Geometry[12:16], 4)
		binary.LittleEndian.PutUint64(d.Geometry[32:40], 1<<20)
		copy(d.Geometry[64:80], fsuuid[:])
		binary.LittleEndian.PutUint32(d.Geometry[88:92], 5)
		d.GeometryAfter = d.Geometry
		if project > 0 {
			binary.LittleEndian.PutUint32(d.FSXAttr[:4], 0x200)
		}
		binary.LittleEndian.PutUint32(d.FSXAttr[12:16], uint32(project))
		d.FSXAttrAfter = d.FSXAttr
		return d
	}
	quota := func(project, hard uint64) capacityprobe.PoolQuotaReadback {
		var raw [112]byte
		raw[0] = 1
		raw[1] = 2
		binary.LittleEndian.PutUint32(raw[4:8], uint32(project))
		binary.LittleEndian.PutUint64(raw[8:16], hard)
		binary.LittleEndian.PutUint64(raw[16:24], hard)
		binary.LittleEndian.PutUint64(raw[40:48], 8)
		used, inodeHard, inodeUsed := uint64(8), uint64(0), uint64(0)
		q := capacityprobe.PoolQuotaReadback{Target: capacityprobe.Quota{RawHex: hex.EncodeToString(raw[:]), Status: "observed", ABI: "linux_v6.8_fs_disk_quota_112_le", UnitBytes: 512, HardBlocks: &hard, UsedBlocks: &used, HardInodes: &inodeHard, UsedInodes: &inodeUsed}, StateSamples: []capacityprobe.CreatedLayerSample{{Sequence: 1, Operation: "qstatv", Source: "linux_syscall", Started: started.Add(time.Millisecond), Ended: started.Add(2 * time.Millisecond), Outcome: "observed"}}}
		q.QStatV[0] = 1
		binary.LittleEndian.PutUint16(q.QStatV[2:4], 0x30)
		q.Default0[0] = 1
		q.Default0[1] = 2
		return q
	}
	engine := capacityprobe.PoolEngineReadback{ID: tr.Identity.EngineID, APIVersion: "1.56", Version: "29.9.0", DockerRoot: tr.Identity.DockerRoot, Driver: "overlay2", CPUCount: 8, MemoryBytes: 32 << 30, Containers: []capacityprobe.PoolContainerInspect{}}
	for j, v := range b.Core {
		engine.Containers = append(engine.Containers, capacityprobe.PoolContainerInspect{ID: v.ContainerID, ImageDigest: v.ImageDigest, Status: "running", PID: uint64(101 + j), Running: true, StartedAt: started.Add(-time.Minute), Labels: map[string]string{}, CgroupParent: "switchback-core.slice", NetworkMode: "bridge"})
	}
	rawMount := []byte(fmt.Sprintf("99 99 0:1 / / rw - ext4 /dev/root rw\n%d 99 %s / /pool rw,prjquota - xfs /dev/test rw,prjquota\n", tr.Identity.MountID, tr.Identity.Device))
	stat := capacityprobe.PoolStatFSReadback{Type: 0x58465342, BlockSize: 4096, FragmentSize: 4096, Blocks: 1 << 24, Free: 1 << 23, Available: 1 << 22}
	o := capacityprobe.PoolObservation{Started: started, Ended: started.Add(100 * time.Millisecond), BootBefore: boot, BootAfter: boot, NamespaceBefore: ns, NamespaceAfter: ns, MountInfoBefore: rawMount, MountInfoAfter: rawMount, EngineBefore: engine, EngineAfter: engine, Anchor: capacityprobe.PoolAnchorReadback{Directory: directory(tr.AnchorPath, 1, 0), Before: stat, After: stat, MountOptions: []string{"rw", "prjquota"}}}
	for j, d := range tr.Core {
		id := capacityprobe.PoolCgroupIdentity{FSType: 0x63677270, Device: 9, Inode: uint64(j + 1)}
		f := capacityprobe.PoolCgroupFiles{CPU: fmt.Sprintf("%d %d\n", d.CPUQuotaMicros, d.CPUPeriodMicros), Memory: fmt.Sprintf("%d\n", d.MemoryMax), PIDs: fmt.Sprintf("%d\n", d.PIDsMax), Swap: "0\n", Type: "domain\n"}
		c := capacityprobe.PoolCoreReadback{CoreID: d.ID, Cgroup: capacityprobe.PoolCgroupReadback{Path: d.CgroupPath, Before: id, After: id, FilesBefore: f, FilesAfter: f}, Members: []capacityprobe.PoolProcessReadback{}}
		for k, v := range b.Core {
			if v.CoreID == d.ID {
				p := d.CgroupPath + "/test.scope"
				c.Members = append(c.Members, capacityprobe.PoolProcessReadback{ContainerID: v.ContainerID, PIDBefore: uint64(101 + k), PIDAfter: uint64(101 + k), StartTimeTicksBefore: 123, StartTimeTicksAfter: 123, CgroupPathBefore: p, CgroupPathAfter: p})
			}
		}
		o.Core = append(o.Core, c)
	}
	for j, d := range tr.Storage {
		o.Storage = append(o.Storage, capacityprobe.PoolStorageReadback{DomainID: d.ID, Directory: directory(d.Path, uint64(j+2), d.ProjectID), Quota: quota(d.ProjectID, d.Quota.HardBlocks512)})
	}
	return o
}
