package capacitysnapshot

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/qualificationio"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func qualificationJSON(v any) []byte {
	if raw, ok := v.(qualificationSettersV2); ok {
		wire, err := qualificationio.EncodeSetterPayload(raw)
		if err != nil {
			panic(err)
		}
		v = wire
	}

	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func qualificationStatus(caps uint64) []byte {
	return []byte(fmt.Sprintf("Pid:\t42\nTgid:\t42\nUid:\t0 0 0 0\nGid:\t0 0 0 0\nCapInh:\t0000000000000000\nCapPrm:\t%016x\nCapEff:\t%016x\nCapBnd:\t%016x\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n", caps, caps, caps))
}
func qualificationCheckValue(id string, v any) qualificationCheck {
	raw := qualificationJSON(v)
	return qualificationCheck{ID: id, Observation: qualificationRaw{WindowID: "qualification-once", Started: "2026-10-22T01:00:01Z", Ended: "2026-10-22T01:00:02Z", Source: id, Data: raw, Digest: Digest(raw)}}
}
func qualificationCheckBytes(id string, raw []byte) qualificationCheck {
	return qualificationCheck{ID: id, Observation: qualificationRaw{WindowID: "qualification-once", Started: "2026-10-22T01:00:01Z", Ended: "2026-10-22T01:00:02Z", Source: id, Data: raw, Digest: Digest(raw)}}
}
func qualificationFixture(t *testing.T) (Trust, map[string][]byte) {
	t.Helper()
	tr, files := qualificationLegacyFixture(t)
	return qualificationUpgradeV2(t, tr, files)
}

func qualificationLegacyFixture(t *testing.T) (Trust, map[string][]byte) {
	t.Helper()
	tr, m, _ := admissionFixture(t)
	g := config.M1SharedVolumeCapacityGuarantee()
	tr.AdmissionPolicy = &workerapp.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}
	m.Guarantee = *tr.AdmissionPolicy
	tr.Identity.GuestBootID = "11111111-1111-1111-1111-111111111111"
	tr.Identity.MountNamespace = "102"
	tr.Identity.EngineID = "01020304-0506-0708-090a-0b0c0d0e0f10"
	tr.Identity.FSUUID = tr.Identity.EngineID
	for j := range tr.Storage {
		tr.Storage[j].FSUUID = tr.Identity.FSUUID
	}
	m.Identity = tr.Identity
	tr.Core[0].CgroupPath = "/sys/fs/cgroup/switchback.slice/switchback-core.slice"
	s := qualificationSource{Schema: "switchback.capacity.source_manifest.v1", Files: []qualificationSourceFile{{Path: "cmd/main.go", Type: "regular", Mode: 0644, Bytes: 7, Digest: Digest([]byte("source!"))}}, Inputs: qualificationInputs{Schema: "switchback.capacity.deployment_inputs.v1", Architecture: "arm64", TrustManifest: qualificationDerived{"final_manifest_digest"}, PublisherManifest: qualificationDerived{"final_manifest_digest"}, ObserverManifest: qualificationDerived{"final_manifest_digest"}, Maintenance: "stop_admission_and_fence_before_change_v1"}}
	input := qualificationDigest(s.Files)
	i := DeploymentInventory{Schema: DeploymentInventoryVersion, WorkerImage: "worker:fixture", WorkerImageDigest: Digest([]byte("worker-image")), Binaries: []AdmissionBinary{}}
	for _, name := range []string{"backend", "engine", "host-producer", "kernel", "native-producer", "observer", "publisher", "worker"} {
		b := qualificationBuild{Role: name, Toolchain: "go1.test", InputRoot: input, BinaryDigest: Digest([]byte(name + "-binary"))}
		if name == "backend" || name == "worker" {
			b.ImageDigest = Digest([]byte(name + "-image"))
		}
		s.Builds = append(s.Builds, b)
		i.Binaries = append(i.Binaries, AdmissionBinary{Name: name, Digest: b.BinaryDigest})
	}
	binding := qualificationBinding{Schema: "switchback.capacity.publisher_bindings.v1", Core: []DeploymentCoreBinding{{ContainerID: strings.Repeat("a", 64), ImageDigest: Digest([]byte("backend-image")), CoreID: "core"}}, WorkerImageDigest: i.WorkerImageDigest}
	for _, name := range []string{"backend", "observer", "publisher"} {
		role := qualificationRole{Role: name, Kind: "host_process", BinaryDigest: Digest([]byte(name + "-binary")), CoreID: "core", CgroupPath: tr.Core[0].CgroupPath + "/" + name, NoNewPrivileges: 1, Seccomp: 2, Mounts: []qualificationMount{}, Environment: []qualificationEnv{}}
		if name == "backend" {
			role.Kind = "container"
			role.ContainerID = binding.Core[0].ContainerID
			role.ImageDigest = binding.Core[0].ImageDigest
			role.Capabilities = 0xb
		}
		if name == "publisher" {
			role.Capabilities = 0x280000
		}
		if name == "observer" {
			role.Capabilities = 0x200000
		}
		if name == "backend" || name == "publisher" {
			role.Mounts = append(role.Mounts, qualificationMount{Source: "/etc/switchback/created-layer-observer", Target: "/etc/switchback/created-layer-observer", Device: "8:2", Root: "/etc/switchback/created-layer-observer", MountID: 2, ReadOnly: true})
		}
		s.Inputs.Roles = append(s.Inputs.Roles, role)
	}
	h := qualificationHostTarget{VolumeUUID: "22222222-2222-2222-2222-222222222222", ContainerUUID: "33333333-3333-3333-3333-333333333333", PhysicalStoreUUID: "44444444-4444-4444-4444-444444444444", PhysicalStore: "disk0s2", Mount: "/Volumes/SwitchbackCore", QuotaBytes: 120259084288, ReserveBytes: 120259084288, Writers: []qualificationWriter{}}
	paths := qualificationHostPaths{Mount: h.Mount, VolumeUUID: h.VolumeUUID, StatDevice: 7, StatInode: 99, Writers: []qualificationWriterReadback{}}
	for _, id := range qualificationWriterIDs {
		w := qualificationWriter{ID: id, Path: h.Mount + "/" + id, ArtifactDigest: Digest([]byte(id)), Policy: "volume_mapped"}
		if id == "snapshot" || id == "clone" || id == "conversion" {
			w.Policy = "forbidden_during_frozen_deployment"
			w.Path = ""
		}
		h.Writers = append(h.Writers, w)
		r := qualificationWriterReadback{ID: id, Path: w.Path, ArtifactDigest: w.ArtifactDigest, Policy: w.Policy}
		if w.Policy == "volume_mapped" {
			r.VolumeUUID = h.VolumeUUID
			r.StatDevice = 7
			r.StatInode = uint64(len(paths.Writers) + 1)
			r.AllocatedBlocks512 = 123
		}
		paths.Writers = append(paths.Writers, r)
	}
	s.Inputs.Host = h
	i.DeploymentInputsDigest = qualificationDigest(s.Inputs)
	files := map[string][]byte{"source-manifest.json": qualificationJSON(s)}
	i.SourceManifestDigest = Digest(files["source-manifest.json"])
	files["deployment-inventory.json"] = qualificationJSON(i)
	m.SourceManifestDigest = i.SourceManifestDigest
	m.DeploymentInventoryDigest = Digest(files["deployment-inventory.json"])
	candidate, e := qualificationCandidate(tr, m, DeploymentPublisherBinding{Core: binding.Core, WorkerImageDigest: i.WorkerImageDigest})
	if e != nil {
		t.Fatal(e)
	}
	window := qualificationWindow{ID: "qualification-once", Started: "2026-10-22T01:00:00Z", Ended: "2026-10-22T01:01:00Z", HostBootBefore: "55555555-5555-5555-5555-555555555555", HostBootAfter: "55555555-5555-5555-5555-555555555555", GuestBootBefore: tr.Identity.GuestBootID, GuestBootAfter: tr.Identity.GuestBootID}
	producer := func(role string) qualificationProducer {
		for _, b := range s.Builds {
			if b.Role == role {
				return qualificationProducer{Role: role, ArtifactDigest: b.BinaryDigest, BuildDigest: qualificationDigest(b), InputRoot: b.InputRoot}
			}
		}
		panic(role)
	}
	host := qualificationHostReceipt{Schema: "switchback.capacity.host_volume_receipt.v1", Kind: "apfs_volume_quota_and_reserve_v1", Lifecycle: qualificationLifecycle, Candidate: candidate, Window: window, Producer: producer("host-producer")}
	info := []byte(`<plist version="1.0"><dict><key>DiskUUID</key><string>22222222-2222-2222-2222-222222222222</string><key>MountPoint</key><string>/Volumes/SwitchbackCore</string><key>FilesystemType</key><string>apfs</string><key>DeviceIdentifier</key><string>disk3s7</string><key>APFSContainerReference</key><string>disk3</string><key>FreeSpace</key><integer>0</integer><key>CapacityInUse</key><integer>120259084288</integer><key>APFSPhysicalStores</key><array><dict><key>APFSPhysicalStore</key><string>disk0s2</string></dict></array></dict></plist>`)
	apfs := []byte(`<plist version="1.0"><dict><key>Containers</key><array><dict><key>APFSContainerUUID</key><string>33333333-3333-3333-3333-333333333333</string><key>ContainerReference</key><string>disk3</string><key>PhysicalStores</key><array><dict><key>DeviceIdentifier</key><string>disk0s2</string><key>DiskUUID</key><string>44444444-4444-4444-4444-444444444444</string></dict></array><key>Volumes</key><array><dict><key>APFSVolumeUUID</key><string>22222222-2222-2222-2222-222222222222</string><key>DeviceIdentifier</key><string>disk3s7</string><key>CapacityQuota</key><integer>120259084288</integer><key>CapacityReserve</key><integer>120259084288</integer><key>CapacityInUse</key><integer>120259084288</integer></dict></array></dict></array></dict></plist>`)
	for _, phase := range []string{"before", "after"} {
		host.Checks = append(host.Checks, qualificationCheckBytes("diskutil-info-"+phase, info), qualificationCheckBytes("apfs-list-"+phase, apfs))
	}
	host.Checks = append(host.Checks, qualificationCheckValue("writer-paths", paths))
	native := qualificationNativeReceipt{Schema: "switchback.capacity.native_enforcement_receipt.v1", Kind: "linux_xfs_enforcement_v1", Lifecycle: qualificationLifecycle, Candidate: candidate, Window: window, Producer: producer("native-producer")}
	profile, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	workerStatus := []byte(strings.Replace(string(qualificationStatus(0)), "Uid:\t0 0 0 0", "Uid:\t10002 10002 10002 10002", 1))
	worker := qualificationWorker{ContainerID: strings.Repeat("c", 64), ImageDigest: i.WorkerImageDigest, BinaryDigest: Digest([]byte("worker-binary")), Profile: profile.Ref(), Boot: tr.Identity.GuestBootID, PID: 42, StartTicks: 123, Cgroup: "/switchback.slice/switchback-workers.slice/test.scope", StatusBefore: workerStatus, StatusAfter: workerStatus}
	loaded := qualificationLoaded{Worker: worker, Identity: tr.Identity, EngineArtifact: Digest([]byte("engine-binary")), KernelArtifact: Digest([]byte("kernel-binary")), Filesystem: "xfs", Roles: s.Inputs.Roles}
	ns := qualificationNamespaces{Namespaces: []qualificationNamespace{}, Roles: []qualificationNativeRole{}}
	for _, owner := range []string{"publisher_thread", "pid1"} {
		for j, kind := range []string{"user", "pid", "mnt", "cgroup"} {
			ns.Namespaces = append(ns.Namespaces, qualificationNamespace{Owner: owner, Kind: kind, Device: 4, Inode: uint64(j + 100), AfterDevice: 4, AfterInode: uint64(j + 100)})
		}
	}
	for _, role := range s.Inputs.Roles {
		ns.Roles = append(ns.Roles, qualificationNativeRole{Role: role.Role, StatusBefore: qualificationStatus(role.Capabilities), StatusAfter: qualificationStatus(role.Capabilities), CgroupBefore: strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup"), CgroupAfter: strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup"), BinaryDigest: role.BinaryDigest, Boot: tr.Identity.GuestBootID})
	}
	core := qualificationCore{Domains: tr.Core, Members: binding.Core, CPU: []string{"100000 100000\n"}, Memory: []string{"1073741824\n"}, PIDs: []string{"512\n"}, Swap: []string{"0\n"}}
	trial := func(hard uint64) qualificationQuotaTrial {
		q := StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: 1234, HardBlocks512: hard}
		raw := make([]byte, 112)
		raw[0] = 1
		raw[1] = 2
		binary.LittleEndian.PutUint32(raw[4:8], 1234)
		binary.LittleEndian.PutUint64(raw[8:16], hard)
		state := make([]byte, 160)
		state[0] = 1
		binary.LittleEndian.PutUint16(state[2:4], 0x30)
		attrs := make([]byte, 28)
		binary.LittleEndian.PutUint32(attrs[:4], 0x200)
		binary.LittleEndian.PutUint32(attrs[12:16], 1234)
		parent := qualificationTestObject{Device: tr.Identity.Device, FSUUID: tr.Identity.FSUUID, MountID: tr.Identity.MountID, Inode: 1001, Owner: 10002, Mode: 0040700, ProjectID: 1234}
		child := parent
		child.Inode = 1002
		child.Mode = 0100600
		return qualificationQuotaTrial{ObjectBefore: parent, ObjectAfter: parent, ChildBefore: child, ChildAfter: child, Inode: 1001, ChildInode: 1002, ChildFSXAttr: append([]byte(nil), attrs...), Device: tr.Identity.Device, FSUUID: tr.Identity.FSUUID, ProjectID: 1234, Before: q, After: q, QStatV: state, QuotaBefore: raw, QuotaAfter: raw, FSXAttr: attrs, XFlags: 0x200, ChildProjectID: 1234, RequestedBytes: 8192, PoolFreeBytes: 1 << 20}
	}
	quota := qualificationQuota{Denied: trial(8), Succeeded: trial(32)}
	quota.Denied.Errno = 122
	quota.Succeeded.WrittenBytes = 8192
	quota.Succeeded.After.UsedBlocks512 = 16
	quota.Succeeded.QuotaAfter = append([]byte(nil), quota.Succeeded.QuotaBefore...)
	binary.LittleEndian.PutUint64(quota.Succeeded.QuotaAfter[40:48], 16)
	setters := struct {
		ABIs []qualificationSetter `json:"abis"`
	}{ABIs: []qualificationSetter{}}
	for _, abi := range []string{"native", "compat32"} {
		for _, cmd := range []string{"FS_IOC_FSSETXATTR", "FS_IOC_SETFLAGS"} {
			flags := uint64(0x200)
			if cmd == "FS_IOC_SETFLAGS" {
				flags = 0x20000000
			}
			getFlags := make([]byte, 4)
			binary.LittleEndian.PutUint32(getFlags, 0x20000000)
			attempt := append([]byte(nil), quota.Denied.FSXAttr...)
			if cmd == "FS_IOC_FSSETXATTR" {
				binary.LittleEndian.PutUint32(attempt[12:16], 1235)
			} else {
				size := 8
				if abi == "compat32" {
					size = 4
				}
				attempt = make([]byte, size)
			}
			setters.ABIs = append(setters.ABIs, qualificationSetter{Worker: worker, TargetBefore: quota.Denied.ObjectBefore, TargetAfter: quota.Denied.ObjectBefore, IOInode: quota.Denied.ChildInode, BeforeFSXAttr: quota.Denied.FSXAttr, AfterFSXAttr: quota.Denied.FSXAttr, BeforeGetFlags: getFlags, AfterGetFlags: getFlags, Attempt: attempt, Architecture: "arm64", ABI: abi, Command: cmd, ExecOutput: []byte{}, SetErrno: 1, BeforeProject: 1234, AfterProject: 1234, BeforeFlags: flags, AfterFlags: flags, ReadBytes: 1, WriteBytes: 1})
		}
	}
	handoff := qualificationHandoff{Cases: []qualificationHandoffCase{}}
	positives := map[string]qualificationHandoffCase{}
	for j, id := range []string{"empty", "nonempty", "sticky", "private"} {
		v := qualificationHandoffCase{ID: id, BinaryDigest: Digest([]byte("backend-binary")), Device: tr.Identity.Device, TargetInode: uint64(2000 + j), ChildInode: uint64(3000 + j), ChildOwner: 10002, Operation: "chown", OwnerBefore: 10002, OwnerAfter: 0, EntriesBefore: 1, EntriesAfter: 1, Mode: 0755, Status: qualificationStatus(11)}
		if id == "empty" {
			v.EntriesBefore = 0
			v.EntriesAfter = 0
			v.ChildInode = 0
			v.ChildOwner = 0
		}
		if id == "private" || id == "sticky" {
			v.Mode = 0700
			v.Operation = "unlink_private_child"
			if id == "sticky" {
				v.Mode = 01777
				v.Operation = "unlink_sticky_child"
			}
			v.OwnerAfter = 10002
			v.EntriesAfter = 0
		}
		positives[id] = v
		handoff.Cases = append(handoff.Cases, v)
	}
	for _, pair := range []struct {
		negative, positive string
		caps               uint64
	}{{"without-chown", "nonempty", 10}, {"without-dac-override", "private", 9}, {"without-fowner", "sticky", 3}} {
		v := positives[pair.positive]
		v.ID = pair.negative
		v.Status = qualificationStatus(pair.caps)
		v.Errno = 1
		v.OwnerAfter = v.OwnerBefore
		v.EntriesAfter = v.EntriesBefore
		handoff.Cases = append(handoff.Cases, v)
	}
	reach := qualificationReachability{Roles: s.Inputs.Roles, Writers: binding.Core, PrivateSources: []string{"/mnt/lima-sbquota/core/capacity-publication-private", "/mnt/lima-sbquota/core/capacity-publisher-keys"}, Observed: []qualificationMountObservation{}, Processes: []qualificationProcessWriter{}, Private: []qualificationPrivateMount{}}
	for _, p := range reach.PrivateSources {
		reach.Private = append(reach.Private, qualificationPrivateMount{Path: p, Device: tr.Identity.Device, Root: p, Inode: uint64(123 + len(reach.Private)), MountID: tr.Identity.MountID, Mode: 0040700})
	}
	for _, role := range s.Inputs.Roles {
		reach.Observed = append(reach.Observed, qualificationMountObservation{Role: role.Role, Raw: []byte("99 0 0:1 / / rw - overlay overlay rw\n2 99 8:2 /etc/switchback/created-layer-observer /etc/switchback/created-layer-observer ro - ext4 /dev/test ro\n")})
		if role.Role == "publisher" {
			reach.Observed[len(reach.Observed)-1].Raw = append(reach.Observed[len(reach.Observed)-1].Raw, []byte("1 99 8:1 /mnt/lima-sbquota /mnt/lima-sbquota rw - xfs /dev/test rw\n")...)
		}
		if role.Kind == "host_process" {
			reach.Processes = append(reach.Processes, qualificationProcessWriter{Role: role.Role, PID: 42, StartTime: 1, Cgroup: strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup"), BinaryDigest: role.BinaryDigest})
		}
	}
	for _, check := range []qualificationCheck{qualificationCheckValue("loaded-identity", loaded), qualificationCheckValue("namespaces-and-roles", ns), qualificationCheckValue("core-limits", core), qualificationCheckValue("byte-quota", quota), qualificationCheckValue("worker-setters", setters), qualificationCheckValue("backend-handoff", handoff), qualificationCheckValue("mounts-and-writers", reach)} {
		native.Checks = append(native.Checks, check)
	}
	files["host-boundary-receipt.json"] = qualificationJSON(host)
	files["native-enforcement-receipt.json"] = qualificationJSON(native)
	m.HostBoundaryReceiptDigest = Digest(files["host-boundary-receipt.json"])
	m.NativeEnforcementReceiptDigest = Digest(files["native-enforcement-receipt.json"])
	files["admission-manifest.json"] = qualificationJSON(m)
	tr.ManifestDigest = Digest(files["admission-manifest.json"])
	binding.ManifestDigest = tr.ManifestDigest
	files["trust.json"] = qualificationJSON(tr)
	files["publisher-bindings.json"] = qualificationJSON(binding)
	files["observer-deployment.env"] = []byte("SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=" + tr.ManifestDigest + "\n")
	return tr, files
}
func TestDeploymentQualificationCompletePureReplay(t *testing.T) {
	tr, files := qualificationFixture(t)
	var source qualificationSource
	if e := strictJSON(files["source-manifest.json"], &source); e != nil {
		t.Fatal("source parse", e)
	}
	var inventory DeploymentInventory
	_ = json.Unmarshal(files["deployment-inventory.json"], &inventory)
	var binding qualificationBinding
	_ = json.Unmarshal(files["publisher-bindings.json"], &binding)
	if e := validateQualificationSource(source, inventory, tr, binding.Core); e != nil {
		t.Fatal("source semantics", e)
	}
	var host qualificationHostReceipt
	if e := strictJSON(files["host-boundary-receipt.json"], &host); e != nil {
		t.Fatal("host parse", e)
	}
	if e := validateQualificationHost(host, source.Inputs.Host); e != nil {
		t.Fatal("host semantics", e)
	}
	var native qualificationNativeReceipt
	if e := strictJSON(files["native-enforcement-receipt.json"], &native); e != nil {
		t.Fatal("native parse", e)
	}
	if e := validateQualificationNative(native, qualificationResult{trust: tr, inputs: source.Inputs, metadata: AdmissionManifestMetadata{Inventory: inventory}, binding: DeploymentPublisherBinding{Core: binding.Core, WorkerImageDigest: inventory.WorkerImageDigest}}); e != nil {
		t.Fatal("native semantics", e)
	}
	result, e := validateDeploymentQualification(tr, files)
	if e != nil {
		t.Fatal(e)
	}
	if result.metadata.Manifest.Guarantee.ID != "m1_shared_volume_capacity_v1" {
		t.Fatal("wrong guarantee")
	}
}

// Rebind only outer digests: malformed native/host semantics must fail even when
// the deployment owner supplies internally consistent byte hashes.
func qualificationRebind(tr Trust, f map[string][]byte) Trust {
	var m AdmissionManifest
	json.Unmarshal(f["admission-manifest.json"], &m)
	m.HostBoundaryReceiptDigest = Digest(f["host-boundary-receipt.json"])
	m.NativeEnforcementReceiptDigest = Digest(f["native-enforcement-receipt.json"])
	f["admission-manifest.json"] = qualificationJSON(m)
	tr.ManifestDigest = Digest(f["admission-manifest.json"])
	f["trust.json"] = qualificationJSON(tr)
	var b qualificationBinding
	json.Unmarshal(f["publisher-bindings.json"], &b)
	b.ManifestDigest = tr.ManifestDigest
	f["publisher-bindings.json"] = qualificationJSON(b)
	f["observer-deployment.env"] = []byte("SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=" + tr.ManifestDigest + "\n")
	return tr
}
func TestDeploymentQualificationEveryProofRequired(t *testing.T) {
	for _, leaf := range []string{"host-boundary-receipt.json", "native-enforcement-receipt.json"} {
		t.Run(leaf, func(t *testing.T) {
			tr, files := qualificationFixture(t)
			var receipt qualificationHostReceipt
			json.Unmarshal(files[leaf], &receipt)
			for i := range receipt.Checks {
				t.Run(receipt.Checks[i].ID, func(t *testing.T) {
					bad := qualificationClone(receipt)
					bad.Checks[i].Observation.Data = []byte(`{"passed":true}`)
					bad.Checks[i].Observation.Digest = Digest(bad.Checks[i].Observation.Data)
					copy := qualificationClone(files)
					copy[leaf] = qualificationJSON(bad)
					badTrust := qualificationRebind(tr, copy)
					if _, e := validateDeploymentQualification(badTrust, copy); e == nil {
						t.Fatal("placeholder proof accepted")
					}
				})
			}
		})
	}
}
func TestDeploymentQualificationRejectsOldAndPlaceholder(t *testing.T) {
	tr, m, evidence := admissionFixture(t)
	files := map[string][]byte{"admission-manifest.json": qualificationJSON(m), "source-manifest.json": evidence[0], "deployment-inventory.json": evidence[1], "host-boundary-receipt.json": evidence[2], "native-enforcement-receipt.json": evidence[3]}
	if _, e := validateDeploymentQualification(tr, files); e == nil {
		t.Fatal("placeholder qualified")
	}
	tr, files = qualificationFixture(t)
	tr.AdmissionPolicy = knownAdmissionPolicy()
	if _, e := validateDeploymentQualification(tr, files); e == nil {
		t.Fatal("weaker receipt qualified old guarantee")
	}
}
func TestDeploymentQualificationCandidateOrderingAndCycle(t *testing.T) {
	tr, f := qualificationFixture(t)
	var m AdmissionManifest
	json.Unmarshal(f["admission-manifest.json"], &m)
	var b qualificationBinding
	json.Unmarshal(f["publisher-bindings.json"], &b)
	binding := DeploymentPublisherBinding{Core: b.Core, WorkerImageDigest: b.WorkerImageDigest}
	before, e := qualificationCandidate(tr, m, binding)
	if e != nil {
		t.Fatal(e)
	}
	sort.Slice(tr.Storage, func(i, j int) bool { return tr.Storage[i].ID > tr.Storage[j].ID })
	sort.Slice(tr.Coverage, func(i, j int) bool { return tr.Coverage[i].Role > tr.Coverage[j].Role })
	tr.ManifestDigest = Digest([]byte("different-final"))
	after, e := qualificationCandidate(tr, m, binding)
	if e != nil || before != after {
		t.Fatal("candidate order/hash cycle")
	}
	binding.Core = append(binding.Core, binding.Core[0])
	if _, e = qualificationCandidate(tr, m, binding); e == nil {
		t.Fatal("duplicate binding")
	}
}
func TestDeploymentQualificationHandleCopiesAndZero(t *testing.T) {
	tr, f := qualificationFixture(t)
	r, e := validateDeploymentQualification(tr, f)
	if e != nil {
		t.Fatal(e)
	}
	q := &DeploymentQualification{result: r}
	binding := q.PublisherBinding()
	binding.Core[0].CoreID = "changed"
	m := q.Metadata()
	m.Manifest.PublicKey[0]++
	if q.PublisherBinding().Core[0].CoreID == "changed" || reflect.DeepEqual(m, q.Metadata()) {
		t.Fatal("alias")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = q.Recheck(ctx); e == nil {
		t.Fatal("cancelled accepted")
	}
	if e = (&DeploymentQualification{}).Recheck(context.Background()); e == nil {
		t.Fatal("zero accepted")
	}
	if _, e = q.RoleBinding("backend"); e == nil {
		t.Fatal("pure metadata made authority")
	}
}
func TestDeploymentQualificationLeafSyscalls(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "leaf")
	if e := os.WriteFile(p, []byte("data"), 0400); e != nil {
		t.Fatal(e)
	}
	d, e := os.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	stat := func(ctx context.Context, fd int) (unix.Stat_t, error) {
		s, e := qualificationStat(ctx, fd)
		s.Uid = 0
		return s, e
	}
	before, e := readQualificationLeafWithStat(context.Background(), d, "leaf", stat)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Link(p, filepath.Join(dir, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e = readQualificationLeafWithStat(context.Background(), d, "leaf", stat); e == nil {
		t.Fatal("hardlink accepted")
	}
	os.Remove(filepath.Join(dir, "link"))
	os.Chmod(p, 0600)
	os.WriteFile(p, []byte("data"), 0600)
	os.Chmod(p, 0400)
	after, e := readQualificationLeafWithStat(context.Background(), d, "leaf", stat)
	if e != nil {
		t.Fatal(e)
	}
	if before.identity == after.identity {
		t.Fatal("same-byte rewrite invisible")
	}
	calls := 0
	drift := func(ctx context.Context, fd int) (unix.Stat_t, error) {
		s, e := stat(ctx, fd)
		calls++
		if calls > 1 {
			s.Ino++
		}
		return s, e
	}
	if _, e = readQualificationLeafWithStat(context.Background(), d, "leaf", drift); e == nil {
		t.Fatal("replacement accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = readQualificationLeafWithStat(ctx, d, "leaf", stat); e == nil {
		t.Fatal("cancel accepted")
	}
	os.Chmod(p, 0600)
	os.WriteFile(p, make([]byte, MaxBytes+1), 0400)
	os.Chmod(p, 0400)
	if _, e = readQualificationLeafWithStat(context.Background(), d, "leaf", stat); e == nil {
		t.Fatal("oversize accepted")
	}
}
func qualificationMutateCheck(t *testing.T, leaf, id string, mutate func([]byte) []byte) {
	t.Helper()
	tr, f := qualificationFixture(t)
	var r qualificationHostReceipt
	json.Unmarshal(f[leaf], &r)
	found := false
	for j := range r.Checks {
		if r.Checks[j].ID == id {
			found = true
			r.Checks[j].Observation.Data = mutate(r.Checks[j].Observation.Data)
			r.Checks[j].Observation.Digest = Digest(r.Checks[j].Observation.Data)
		}
	}
	if !found {
		t.Fatal(id)
	}
	f[leaf] = qualificationJSON(r)
	tr = qualificationRebind(tr, f)
	if _, e := validateDeploymentQualification(tr, f); e == nil {
		t.Fatal("semantic mutation accepted", id)
	}
}
func TestDeploymentQualificationHostIdentityAndLimits(t *testing.T) {
	for _, c := range []struct{ name, id, old, new string }{{"quota-zero", "apfs-list-before", "<key>CapacityQuota</key><integer>120259084288</integer>", "<key>CapacityQuota</key><integer>0</integer>"}, {"reserve-drift", "apfs-list-after", "<key>CapacityReserve</key><integer>120259084288</integer>", "<key>CapacityReserve</key><integer>120259084287</integer>"}, {"uuid", "diskutil-info-before", "22222222-2222-2222-2222-222222222222", "other-uuid"}, {"container", "apfs-list-after", "33333333-3333-3333-3333-333333333333", "other-container"}, {"physical", "diskutil-info-after", "disk0s2", "disk0s3"}, {"mount", "diskutil-info-before", "/Volumes/SwitchbackCore", "/Volumes/Other"}} {
		t.Run(c.name, func(t *testing.T) {
			qualificationMutateCheck(t, "host-boundary-receipt.json", c.id, func(raw []byte) []byte { return []byte(strings.Replace(string(raw), c.old, c.new, 1)) })
		})
	}
	for _, name := range []string{"wrong-device", "missing-writer", "normal-forbidden", "wrong-volume"} {
		t.Run(name, func(t *testing.T) {
			qualificationMutateCheck(t, "host-boundary-receipt.json", "writer-paths", func(raw []byte) []byte {
				var v qualificationHostPaths
				json.Unmarshal(raw, &v)
				switch name {
				case "wrong-device":
					v.Writers[0].StatDevice++
				case "missing-writer":
					v.Writers = v.Writers[1:]
				case "normal-forbidden":
					v.Writers[0].Policy = "forbidden_during_frozen_deployment"
				case "wrong-volume":
					v.Writers[0].VolumeUUID = "other"
				}
				return qualificationJSON(v)
			})
		})
	}
}
func TestDeploymentQualificationNativeSemanticNegatives(t *testing.T) {
	for _, name := range []string{"caps", "boot", "binary", "out-of-scope-host", "missing-host", "namespace-drift"} {
		t.Run(name, func(t *testing.T) {
			qualificationMutateCheck(t, "native-enforcement-receipt.json", "namespaces-and-roles", func(raw []byte) []byte {
				var v qualificationNamespaces
				json.Unmarshal(raw, &v)
				switch name {
				case "caps":
					v.Roles[0].StatusBefore = qualificationStatus(0)
					v.Roles[0].StatusAfter = v.Roles[0].StatusBefore
				case "boot":
					v.Roles[0].Boot = "other"
				case "binary":
					v.Roles[0].BinaryDigest = Digest([]byte("other"))
				case "out-of-scope-host":
					v.Roles[1].CgroupBefore = "/other"
					v.Roles[1].CgroupAfter = "/other"
				case "missing-host":
					v.Roles = v.Roles[:2]
				case "namespace-drift":
					v.Namespaces[0].AfterInode++
				}
				return qualificationJSON(v)
			})
		})
	}
	for _, name := range []string{"wrong-private", "backend-alias", "missing-process", "wrong-process"} {
		t.Run(name, func(t *testing.T) {
			qualificationMutateCheck(t, "native-enforcement-receipt.json", "mounts-and-writers", func(raw []byte) []byte {
				var v qualificationReachability
				json.Unmarshal(raw, &v)
				switch name {
				case "wrong-private":
					v.PrivateSources[0] = "/irrelevant"
				case "backend-alias":
					v.Observed[0].Raw = append(v.Observed[0].Raw, []byte("3 1 8:1 /mnt/lima-sbquota /alias rw - xfs /dev/test rw\n")...)
				case "missing-process":
					v.Processes = v.Processes[:1]
				case "wrong-process":
					v.Processes[0].Cgroup = "/other"
				}
				return qualificationJSON(v)
			})
		})
	}
	qualificationMutateCheck(t, "native-enforcement-receipt.json", "core-limits", func(raw []byte) []byte {
		var v qualificationCore
		json.Unmarshal(raw, &v)
		v.Memory[0] = "max\n"
		return qualificationJSON(v)
	})
	qualificationMutateCheck(t, "native-enforcement-receipt.json", "byte-quota", func(raw []byte) []byte {
		var v qualificationQuota
		json.Unmarshal(raw, &v)
		v.Denied.Errno = 0
		return qualificationJSON(v)
	})
	qualificationMutateCheck(t, "native-enforcement-receipt.json", "worker-setters", func(raw []byte) []byte {
		v, err := decodeQualificationSettersV2(raw)
		if err != nil {
			t.Fatal(err)
		}
		v.ABIs = v.ABIs[:2]
		return qualificationJSON(v)
	})
	qualificationMutateCheck(t, "native-enforcement-receipt.json", "backend-handoff", func(raw []byte) []byte {
		var v qualificationHandoff
		json.Unmarshal(raw, &v)
		v.Cases[4].Errno = 0
		return qualificationJSON(v)
	})
}
func TestDeploymentQualificationObserverAndWindow(t *testing.T) {
	for _, bad := range []string{"SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=wrong\n", "MANIFEST_DIGEST=wrong\n", "SWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=wrong\nSWITCHBACK_DEPLOYMENT_MANIFEST_DIGEST=wrong\n"} {
		tr, f := qualificationFixture(t)
		f["observer-deployment.env"] = []byte(bad)
		if _, e := validateDeploymentQualification(tr, f); e == nil {
			t.Fatal("wrong actual observer config accepted")
		}
	}
	for _, name := range []string{"merged-window", "duplicate-check", "unknown-check", "producer"} {
		t.Run(name, func(t *testing.T) {
			tr, f := qualificationFixture(t)
			var r qualificationHostReceipt
			json.Unmarshal(f["host-boundary-receipt.json"], &r)
			switch name {
			case "merged-window":
				r.Checks[0].Observation.WindowID = "older-window"
			case "duplicate-check":
				r.Checks[0] = r.Checks[1]
			case "unknown-check":
				r.Checks[0].ID = "unknown"
			case "producer":
				r.Producer.ArtifactDigest = Digest([]byte("operator-approved"))
			}
			f["host-boundary-receipt.json"] = qualificationJSON(r)
			tr = qualificationRebind(tr, f)
			if _, e := validateDeploymentQualification(tr, f); e == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}
func TestDeploymentQualificationStatusVolatileBytes(t *testing.T) {
	tr, f := qualificationFixture(t)
	var r qualificationNativeReceipt
	json.Unmarshal(f["native-enforcement-receipt.json"], &r)
	for j := range r.Checks {
		if r.Checks[j].ID == "namespaces-and-roles" {
			var ns qualificationNamespaces
			json.Unmarshal(r.Checks[j].Observation.Data, &ns)
			ns.Roles[0].StatusBefore = append(ns.Roles[0].StatusBefore, []byte("VmRSS:\t100 kB\n")...)
			ns.Roles[0].StatusAfter = append(ns.Roles[0].StatusAfter, []byte("VmRSS:\t104 kB\n")...)
			r.Checks[j].Observation.Data = qualificationJSON(ns)
			r.Checks[j].Observation.Digest = Digest(r.Checks[j].Observation.Data)
		}
	}
	f["native-enforcement-receipt.json"] = qualificationJSON(r)
	tr = qualificationRebind(tr, f)
	if _, e := validateDeploymentQualification(tr, f); e != nil {
		t.Fatal("volatile status bytes rejected", e)
	}
}

func TestDeploymentQualificationReviewMutations(t *testing.T) {
	for _, name := range []string{"different-success-inodes", "changed-before-after-object"} {
		t.Run(name, func(t *testing.T) {
			qualificationMutateCheck(t, "native-enforcement-receipt.json", "byte-quota", func(raw []byte) []byte {
				var q qualificationQuota
				json.Unmarshal(raw, &q)
				if name == "different-success-inodes" {
					q.Succeeded.Inode += 100
					q.Succeeded.ChildInode += 100
					q.Succeeded.ObjectBefore.Inode += 100
					q.Succeeded.ObjectAfter.Inode += 100
					q.Succeeded.ChildBefore.Inode += 100
					q.Succeeded.ChildAfter.Inode += 100
				} else {
					q.Succeeded.ObjectAfter.Inode++
				}
				return qualificationJSON(q)
			})
		})
	}
	for _, name := range []string{"private-0777", "negative-other-target", "negative-other-operation", "private-not-uid10002"} {
		t.Run(name, func(t *testing.T) {
			qualificationMutateCheck(t, "native-enforcement-receipt.json", "backend-handoff", func(raw []byte) []byte {
				var h qualificationHandoff
				json.Unmarshal(raw, &h)
				for j := range h.Cases {
					c := &h.Cases[j]
					if name == "private-0777" && c.ID == "private" {
						c.Mode = 0777
					}
					if c.ID == "without-dac-override" {
						switch name {
						case "negative-other-target":
							c.TargetInode++
						case "negative-other-operation":
							c.Operation = "chown"
						}
					}
					if name == "private-not-uid10002" && c.ID == "private" {
						c.OwnerBefore = 0
					}
				}
				return qualificationJSON(h)
			})
		})
	}
	for _, name := range []string{"worker-image", "worker-profile", "worker-root", "worker-binary", "different-inode", "nonmutating-attempt", "wrong-get-project", "get-changed", "target-not-owned"} {
		t.Run(name, func(t *testing.T) {
			qualificationMutateCheck(t, "native-enforcement-receipt.json", "worker-setters", func(raw []byte) []byte {
				v, err := decodeQualificationSettersV2(raw)
				if err != nil {
					t.Fatal(err)
				}
				s := &v.ABIs[0]
				switch name {
				case "worker-image":
					s.Worker.ImageDigest = Digest([]byte("other"))
				case "worker-profile":
					s.Worker.Profile.Revision++
				case "worker-root":
					s.Worker.StatusBefore = qualificationStatus(0)
					s.Worker.StatusAfter = s.Worker.StatusBefore
				case "worker-binary":
					s.Worker.BinaryDigest = Digest([]byte("other"))
				case "different-inode":
					s.Execution.Facts[0].TargetFD.HostBefore.Inode++
				case "nonmutating-attempt":
					s.Execution.Facts[0].IO.Set.Buffer = append([]byte(nil), s.Execution.Facts[0].IO.BeforeFSXAttr.Buffer...)
				case "wrong-get-project":
					binary.LittleEndian.PutUint32(s.Execution.Facts[0].IO.BeforeFSXAttr.Buffer[12:16], 1235)
					s.Execution.Facts[0].IO.AfterFSXAttr.Buffer = append([]byte(nil), s.Execution.Facts[0].IO.BeforeFSXAttr.Buffer...)
				case "get-changed":
					s.Execution.Facts[0].IO.AfterFSXAttr.Buffer = append([]byte(nil), s.Execution.Facts[0].IO.BeforeFSXAttr.Buffer...)
					s.Execution.Facts[0].IO.AfterFSXAttr.Buffer[0]++
				case "target-not-owned":
					s.Execution.Facts[0].TargetFD.HostBefore.Owner = 0
				}
				return qualificationJSON(v)
			})
		})
	}
	// Required decimal encoding is preserved; a bracketed diagnostic rendering is
	// not an interchangeable Trust representation.
	tr, f := qualificationFixture(t)
	var ns qualificationNamespaces
	var r qualificationNativeReceipt
	json.Unmarshal(f["native-enforcement-receipt.json"], &r)
	for _, c := range r.Checks {
		if c.ID == "namespaces-and-roles" {
			json.Unmarshal(c.Observation.Data, &ns)
		}
	}
	var src qualificationSource
	json.Unmarshal(f["source-manifest.json"], &src)
	tr.Identity.MountNamespace = "mnt:[102]"
	if e := validateQualificationNamespaces(ns, qualificationResult{trust: tr, inputs: src.Inputs}); e == nil {
		t.Fatal("bracketed namespace accepted")
	}
}
