package capacitysnapshot

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
)

var qualificationNativeChecks = []string{"loaded-identity", "namespaces-and-roles", "core-limits", "byte-quota", "worker-setters", "backend-handoff", "mounts-and-writers"}

func validateQualificationNative(r qualificationNativeReceipt, out qualificationResult) error {
	if r.Schema == qualificationNativeV2 {
		return validateQualificationNativeV2(r, out)
	}
	if r.Schema != "switchback.capacity.native_enforcement_receipt.v1" {
		return errQualification
	}
	return validateQualificationNativeLegacyV1(r, out)
}

func validateQualificationNativeLegacyV1(r qualificationNativeReceipt, out qualificationResult) error {
	c, loaded, quota, e := validateQualificationNativeBase(r, out)
	if e != nil {
		return e
	}
	var setters struct {
		ABIs []qualificationSetter `json:"abis"`
	}
	if strictJSON(c["worker-setters"], &setters) != nil {
		return errQualification
	}
	return validateQualificationSetters(setters.ABIs, loaded.Worker, quota, out)
}

// The six unchanged checks are shared predicates. Setter v1/v2 decoding stays
// separate; production v2 never turns missing executor facts into legacy proof.
func validateQualificationNativeBase(r qualificationNativeReceipt, out qualificationResult) (map[string][]byte, qualificationLoaded, qualificationQuota, error) {
	var loaded qualificationLoaded
	var quota qualificationQuota
	fail := func() (map[string][]byte, qualificationLoaded, qualificationQuota, error) {
		return nil, qualificationLoaded{}, qualificationQuota{}, errQualification
	}
	c, e := qualificationChecks(r.Checks, qualificationNativeChecks, r.Window)
	if e != nil {
		return fail()
	}
	if strictJSON(c["loaded-identity"], &loaded) != nil || loaded.Identity != out.trust.Identity || loaded.Filesystem != "xfs" || validateQualificationWorker(loaded.Worker, out, r.Schema == qualificationNativeV2) != nil || !reflect.DeepEqual(loaded.Roles, out.inputs.Roles) {
		return fail()
	}
	for _, b := range out.metadata.Inventory.Binaries {
		if b.Name == "engine" && b.Digest != loaded.EngineArtifact {
			return fail()
		}
		if b.Name == "kernel" && b.Digest != loaded.KernelArtifact {
			return fail()
		}
	}
	var ns qualificationNamespaces
	if strictJSON(c["namespaces-and-roles"], &ns) != nil || validateQualificationNamespaces(ns, out) != nil {
		return fail()
	}
	var core qualificationCore
	if strictJSON(c["core-limits"], &core) != nil || validateQualificationCore(core, out) != nil {
		return fail()
	}
	if strictJSON(c["byte-quota"], &quota) != nil || validateQualificationQuota(quota, out.trust.Identity) != nil {
		return fail()
	}
	var handoff qualificationHandoff
	if strictJSON(c["backend-handoff"], &handoff) != nil || validateQualificationHandoff(handoff, out) != nil {
		return fail()
	}
	var reach qualificationReachability
	if strictJSON(c["mounts-and-writers"], &reach) != nil || validateQualificationReachability(reach, out) != nil {
		return fail()
	}
	return c, loaded, quota, nil
}
func validateQualificationNamespaces(ns qualificationNamespaces, out qualificationResult) error {
	if len(ns.Namespaces) != 8 || len(ns.Roles) != len(out.inputs.Roles) {
		return errQualification
	}
	seen := map[string]bool{}
	byKind := map[string]qualificationNamespace{}
	for _, n := range ns.Namespaces {
		key := n.Owner + ":" + n.Kind
		if seen[key] || (n.Owner != "publisher_thread" && n.Owner != "pid1") || (n.Kind != "user" && n.Kind != "pid" && n.Kind != "mnt" && n.Kind != "cgroup") || n.Errno != 0 || n.Device == 0 || n.Inode == 0 || n.Device != n.AfterDevice || n.Inode != n.AfterInode {
			return errQualification
		}
		seen[key] = true
		if old, ok := byKind[n.Kind]; ok && (old.Device != n.Device || old.Inode != n.Inode) {
			return errQualification
		}
		byKind[n.Kind] = n
		if n.Kind == "mnt" && strconv.FormatUint(n.Inode, 10) != out.trust.Identity.MountNamespace {
			return errQualification
		}
	}
	seen = map[string]bool{}
	for _, observed := range ns.Roles {
		if seen[observed.Role] || observed.CgroupBefore != observed.CgroupAfter || observed.Boot != out.trust.Identity.GuestBootID {
			return errQualification
		}
		seen[observed.Role] = true
		p, e := procstatus.Parse(observed.StatusBefore)
		after, ae := procstatus.Parse(observed.StatusAfter)
		if e != nil || ae != nil || p != after {
			return errQualification
		}
		matched := false
		for _, r := range out.inputs.Roles {
			if r.Role != observed.Role {
				continue
			}
			matched = true
			if !roleMatchesUID(p, r.UID, r.Capabilities) || observed.BinaryDigest != r.BinaryDigest || observed.CgroupBefore != strings.TrimPrefix(r.CgroupPath, "/sys/fs/cgroup") {
				return errQualification
			}
		}
		if !matched {
			return errQualification
		}
	}
	return nil
}
func validateQualificationCore(c qualificationCore, out qualificationResult) error {
	expected := qualificationClone(out.trust.Core)
	if !reflect.DeepEqual(c.Domains, expected) || len(c.CPU) != len(expected) || len(c.Memory) != len(expected) || len(c.PIDs) != len(expected) || len(c.Swap) != len(expected) {
		return errQualification
	}
	bindings, e := normalizeQualificationCore(c.Members)
	if e != nil || !reflect.DeepEqual(bindings, out.binding.Core) {
		return errQualification
	}
	for i, d := range expected {
		if c.CPU[i] != fmt.Sprintf("%d %d\n", d.CPUQuotaMicros, d.CPUPeriodMicros) || c.Memory[i] != fmt.Sprintf("%d\n", d.MemoryMax) || c.PIDs[i] != fmt.Sprintf("%d\n", d.PIDsMax) || c.Swap[i] != "0\n" {
			return errQualification
		}
	}
	return nil
}
func validateQualificationQuota(q qualificationQuota, identity Identity) error {
	if q.Denied.ProjectID != q.Succeeded.ProjectID || q.Denied.RequestedBytes != q.Succeeded.RequestedBytes || q.Denied.Device != q.Succeeded.Device || q.Denied.FSUUID != q.Succeeded.FSUUID || q.Denied.Inode != q.Succeeded.Inode || q.Denied.ChildInode != q.Succeeded.ChildInode {
		return errQualification
	}
	for _, t := range []qualificationQuotaTrial{q.Denied, q.Succeeded} {
		if t.ObjectBefore != t.ObjectAfter || t.ChildBefore != t.ChildAfter || t.ObjectBefore != q.Denied.ObjectBefore || t.ChildBefore != q.Denied.ChildBefore || !qualificationObject(t.ObjectBefore, identity) || !qualificationObject(t.ChildBefore, identity) || t.ObjectBefore.Inode != t.Inode || t.ChildBefore.Inode != t.ChildInode || t.ObjectBefore.ProjectID != t.ProjectID || t.ChildBefore.ProjectID != t.ProjectID || t.ObjectBefore.Mode != 0040700 || t.ChildBefore.Mode != 0100600 {
			return errQualification
		}
		if t.Inode == 0 || t.ChildInode == 0 || t.Inode == t.ChildInode || len(t.ChildFSXAttr) != 28 || uint64(binary.LittleEndian.Uint32(t.ChildFSXAttr[12:16])) != t.ProjectID || t.Device != identity.Device || t.FSUUID != identity.FSUUID || t.ProjectID == 0 || t.ProjectID > 0xffffffff || t.ChildProjectID != t.ProjectID || t.XFlags&0x200 == 0 || t.RequestedBytes == 0 || t.RequestedBytes > 16<<20 || t.PoolFreeBytes < t.RequestedBytes*2 || len(t.QStatV) != 160 || t.QStatV[0] != 1 || binary.LittleEndian.Uint16(t.QStatV[2:4])&0x30 != 0x30 || len(t.FSXAttr) != 28 || uint64(binary.LittleEndian.Uint32(t.FSXAttr[:4])) != t.XFlags || uint64(binary.LittleEndian.Uint32(t.FSXAttr[12:16])) != t.ProjectID {
			return errQualification
		}
		for i, raw := range [][]byte{t.QuotaBefore, t.QuotaAfter} {
			if !createdQuotaRecord(raw, uint32(t.ProjectID)) {
				return errQualification
			}
			v := t.Before
			if i == 1 {
				v = t.After
			}
			if v.Format != "linux_xfs_quotactl_v1" || v.StateVersion != 1 || v.StateFlags != uint64(binary.LittleEndian.Uint16(t.QStatV[2:4])) || v.Version != 1 || v.Flags != 2 || v.ProjectID != t.ProjectID || v.HardBlocks512 != binary.LittleEndian.Uint64(raw[8:16]) || v.UsedBlocks512 != binary.LittleEndian.Uint64(raw[40:48]) || v.HardBlocks512 == 0 || v.HardBlocks512 > 32768 || v.UsedBlocks512 > v.HardBlocks512 {
				return errQualification
			}
		}
		if t.Before.HardBlocks512 != t.After.HardBlocks512 || t.Before.UsedBlocks512 > t.After.UsedBlocks512 {
			return errQualification
		}
	}
	d, s := q.Denied, q.Succeeded
	// Finite test I/O only. These bytes never become pool availability or a sample.
	if d.Errno != 122 || d.WrittenBytes >= d.RequestedBytes || d.RequestedBytes <= d.Before.HardBlocks512*512 || s.Errno != 0 || s.WrittenBytes != s.RequestedBytes || s.After.UsedBlocks512 <= s.Before.UsedBlocks512 || s.Before.HardBlocks512 <= d.Before.HardBlocks512 || s.RequestedBytes > (s.Before.HardBlocks512-s.Before.UsedBlocks512)*512 {
		return errQualification
	}
	return nil
}
func validateQualificationHandoff(h qualificationHandoff, out qualificationResult) error {
	ids := []string{"empty", "nonempty", "sticky", "private", "without-chown", "without-dac-override", "without-fowner"}
	if len(h.Cases) != len(ids) {
		return errQualification
	}
	binary := ""
	for _, b := range out.metadata.Inventory.Binaries {
		if b.Name == "backend" {
			binary = b.Digest
		}
	}
	cases := map[string]qualificationHandoffCase{}
	for _, c := range h.Cases {
		known := false
		for _, id := range ids {
			if c.ID == id {
				known = true
			}
		}
		if !known || cases[c.ID].ID != "" || c.OwnerBefore != 10002 || c.BinaryDigest != binary || c.Device != out.trust.Identity.Device || c.TargetInode == 0 || c.RequestedOwner != 0 {
			return errQualification
		}
		cases[c.ID] = c
		p, e := procstatus.Parse(c.Status)
		if e != nil {
			return errQualification
		}
		expected := uint64(0xb)
		switch c.ID {
		case "without-chown":
			expected &^= 1
		case "without-dac-override":
			expected &^= 2
		case "without-fowner":
			expected &^= 8
		}
		if !roleMatches(p, expected) {
			return errQualification
		}
	}
	for _, id := range []string{"empty", "nonempty", "private", "sticky"} {
		c := cases[id]
		if c.Errno != 0 {
			return errQualification
		}
		switch id {
		case "empty", "nonempty":
			if c.Mode != 0755 || c.Operation != "chown" || c.OwnerAfter != 0 || c.EntriesAfter != c.EntriesBefore {
				return errQualification
			}
			if id == "empty" {
				if c.EntriesBefore != 0 || c.ChildInode != 0 || c.ChildOwner != 0 {
					return errQualification
				}
			} else if c.EntriesBefore != 1 || c.ChildInode == 0 || c.ChildOwner != 10002 {
				return errQualification
			}
		case "private", "sticky":
			mode := uint64(0700)
			operation := "unlink_private_child"
			if id == "sticky" {
				mode = 01777
				operation = "unlink_sticky_child"
			}
			if c.Mode != mode || c.Operation != operation || c.OwnerAfter != 10002 || c.ChildOwner != 10002 || c.ChildInode == 0 || c.ChildInode == c.TargetInode || c.EntriesBefore != 1 || c.EntriesAfter != 0 {
				return errQualification
			}
		}
	}
	for negative, positive := range map[string]string{"without-chown": "nonempty", "without-dac-override": "private", "without-fowner": "sticky"} {
		n, p := cases[negative], cases[positive]
		if (n.Errno != 1 && n.Errno != 13) || n.OwnerAfter != n.OwnerBefore || n.EntriesAfter != n.EntriesBefore || n.Device != p.Device || n.TargetInode != p.TargetInode || n.ChildInode != p.ChildInode || n.ChildOwner != p.ChildOwner || n.Mode != p.Mode || n.Operation != p.Operation || n.OwnerBefore != p.OwnerBefore || n.EntriesBefore != p.EntriesBefore || n.RequestedOwner != p.RequestedOwner {
			return errQualification
		}
	}
	return nil
}
func qualificationObject(v qualificationTestObject, i Identity) bool {
	return v.Device == i.Device && v.FSUUID == i.FSUUID && v.MountID == i.MountID && v.Inode > 0 && v.Owner == 10002 && v.ProjectID > 0 && v.ProjectID < 0xffffffff
}
func validateQualificationWorker(w qualificationWorker, out qualificationResult, v2 bool) error {
	profile, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	binary := ""
	for _, b := range out.metadata.Inventory.Binaries {
		if b.Name == "worker" {
			binary = b.Digest
		}
	}
	if !containerIDPattern.MatchString(w.ContainerID) || w.ImageDigest != out.metadata.Inventory.WorkerImageDigest || w.BinaryDigest != binary || w.Profile != profile.Ref() || w.Boot != out.trust.Identity.GuestBootID || w.PID == 0 || w.StartTicks == 0 || !absolute(w.Cgroup) || !strings.HasPrefix(w.Cgroup, "/switchback.slice/switchback-workers.slice/") {
		return errQualification
	}
	p, e := procstatus.Parse(w.StatusBefore)
	after, ae := procstatus.Parse(w.StatusAfter)
	if e != nil || ae != nil || p != after || p.PID != w.PID || p.UIDs != [4]uint32{10002, 10002, 10002, 10002} || p.CapPrm != "0000000000000000" || p.CapEff != "0000000000000000" || p.CapBnd != "0000000000000000" || p.CapInh != "0000000000000000" || p.CapAmb != "0000000000000000" || p.NoNewPrivs != 1 || p.Seccomp != 2 || (v2 && (p.TID != w.PID || p.TGID != w.PID)) {
		return errQualification
	}
	return nil
}
func validateQualificationSetters(setters []qualificationSetter, worker qualificationWorker, q qualificationQuota, out qualificationResult) error {
	if len(setters) != 4 {
		return errQualification
	}
	seen := map[string]bool{}
	unsupported := map[string]bool{}
	execution := map[string]bool{}
	for _, s := range setters {
		key := s.ABI + ":" + s.Command
		if s.Architecture != out.inputs.Architecture || (s.ABI != "native" && s.ABI != "compat32") || (s.Command != "FS_IOC_FSSETXATTR" && s.Command != "FS_IOC_SETFLAGS") || seen[key] || !reflect.DeepEqual(s.Worker, worker) {
			return errQualification
		}
		seen[key] = true
		if s.TargetBefore != s.TargetAfter || s.TargetBefore != q.Denied.ObjectBefore || s.IOInode != q.Denied.ChildInode || !qualificationObject(s.TargetBefore, out.trust.Identity) {
			return errQualification
		}
		if s.ExecErrno != 0 {
			if s.ABI != "compat32" || s.ExecErrno != 8 || s.UnsupportedCause != "execve_ENOEXEC" || len(s.ExecOutput) == 0 || len(s.ExecOutput) > 4096 || s.GetErrno != 0 || s.AfterGetErrno != 0 || s.SetErrno != 0 || s.BeforeProject != 0 || s.AfterProject != 0 || s.BeforeFlags != 0 || s.AfterFlags != 0 || s.ReadBytes != 0 || s.WriteBytes != 0 || len(s.BeforeFSXAttr) != 0 || len(s.AfterFSXAttr) != 0 || len(s.BeforeGetFlags) != 0 || len(s.AfterGetFlags) != 0 || len(s.Attempt) != 0 {
				return errQualification
			}
			unsupported[s.ABI] = true
			continue
		}
		execution[s.ABI] = true
		if s.GetErrno != 0 || s.AfterGetErrno != 0 || s.ReadBytes == 0 || s.WriteBytes == 0 || s.UnsupportedCause != "" || s.SetErrno != 1 || len(s.BeforeFSXAttr) != 28 || !bytes.Equal(s.BeforeFSXAttr, s.AfterFSXAttr) || len(s.BeforeGetFlags) != 4 || !bytes.Equal(s.BeforeGetFlags, s.AfterGetFlags) {
			return errQualification
		}
		project := uint64(binary.LittleEndian.Uint32(s.BeforeFSXAttr[12:16]))
		xflags := uint64(binary.LittleEndian.Uint32(s.BeforeFSXAttr[:4]))
		flags := uint64(binary.LittleEndian.Uint32(s.BeforeGetFlags))
		if project != s.TargetBefore.ProjectID || s.BeforeProject != project || s.AfterProject != project || xflags&0x200 == 0 || flags&0x20000000 == 0 {
			return errQualification
		}
		if s.Command == "FS_IOC_FSSETXATTR" {
			if s.BeforeFlags != xflags || s.AfterFlags != xflags || len(s.Attempt) != 28 {
				return errQualification
			}
			want := append([]byte(nil), s.BeforeFSXAttr...)
			binary.LittleEndian.PutUint32(want[12:16], uint32(project+1))
			if !bytes.Equal(s.Attempt, want) {
				return errQualification
			}
		} else {
			if s.BeforeFlags != flags || s.AfterFlags != flags {
				return errQualification
			}
			size := 8
			if s.ABI == "compat32" {
				size = 4
			}
			want := make([]byte, size)
			binary.LittleEndian.PutUint32(want, uint32(flags&^0x20000000))
			if !bytes.Equal(s.Attempt, want) {
				return errQualification
			}
		}
	}
	if unsupported["compat32"] && execution["compat32"] {
		return errQualification
	}
	return nil
}
func validateQualificationReachability(r qualificationReachability, out qualificationResult) error {
	if !reflect.DeepEqual(r.Roles, out.inputs.Roles) || len(r.PrivateSources) != 2 {
		return errQualification
	}
	writers, e := normalizeQualificationCore(r.Writers)
	if e != nil || !reflect.DeepEqual(writers, out.binding.Core) {
		return errQualification
	}
	const key = "/mnt/lima-sbquota/core/capacity-publisher-keys"
	const journal = "/mnt/lima-sbquota/core/capacity-publication-private"
	if r.PrivateSources[0] != journal || r.PrivateSources[1] != key || len(r.Private) != 2 || len(r.Observed) != len(r.Roles) {
		return errQualification
	}
	for i, p := range r.Private {
		if p.Path != r.PrivateSources[i] || p.Device == "" || !absolute(p.Root) || p.Inode == 0 || p.Device != out.trust.Identity.Device || p.MountID != out.trust.Identity.MountID || p.Mode != 0040700 || p.UID != 0 || p.GID != 0 {
			return errQualification
		}
	}
	seen := map[string]bool{}
	for _, ob := range r.Observed {
		if seen[ob.Role] || len(ob.Raw) == 0 || len(ob.Raw) > 65536 {
			return errQualification
		}
		seen[ob.Role] = true
		var role *qualificationRole
		for i := range r.Roles {
			if r.Roles[i].Role == ob.Role {
				role = &r.Roles[i]
			}
		}
		if role == nil {
			return errQualification
		}
		mounts, e := qualificationMountInfo(ob.Raw)
		if e != nil {
			return e
		}
		for _, expected := range role.Mounts {
			actual, ok := mounts[expected.Target]
			if !ok || actual.Device != expected.Device || actual.Root != expected.Root || actual.MountID != expected.MountID || actual.ReadOnly != expected.ReadOnly {
				return errQualification
			}
		}
		if role.Role == "publisher" {
			for _, p := range r.Private {
				var selected qualificationMount
				found := false
				for _, m := range mounts {
					if m.Target == "/" || p.Path == m.Target || strings.HasPrefix(p.Path, m.Target+"/") {
						if !found || len(m.Target) > len(selected.Target) {
							selected = m
							found = true
						}
					}
				}
				if !found || selected.Device != p.Device || selected.MountID != p.MountID || path.Join(selected.Root, strings.TrimPrefix(strings.TrimPrefix(p.Path, selected.Target), "/")) != p.Root {
					return errQualification
				}
			}
		}
		if role.Role == "backend" {
			for _, m := range mounts {
				for _, p := range r.Private {
					if m.Device == p.Device && (m.Root == "/" || m.Root == p.Root || strings.HasPrefix(p.Root, m.Root+"/")) {
						return errQualification
					}
				}
			}
		}
	}
	host := map[string]qualificationRole{}
	for _, role := range r.Roles {
		if role.Kind == "host_process" {
			host[role.Role] = role
		}
	}
	if len(r.Processes) != len(host) {
		return errQualification
	}
	seen = map[string]bool{}
	for _, p := range r.Processes {
		role, ok := host[p.Role]
		if !ok || seen[p.Role] || p.PID == 0 || p.StartTime == 0 || p.Cgroup != strings.TrimPrefix(role.CgroupPath, "/sys/fs/cgroup") || p.BinaryDigest != role.BinaryDigest {
			return errQualification
		}
		seen[p.Role] = true
	}
	return nil
}
func qualificationMountInfo(raw []byte) (map[string]qualificationMount, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, errQualification
	}
	out := map[string]qualificationMount{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			return nil, errQualification
		}
		id, e := strconv.ParseUint(f[0], 10, 64)
		if e != nil || id == 0 || (f[3] != "/" && !absolute(f[3])) || (f[4] != "/" && !absolute(f[4])) || strings.Contains(f[3], "\\") || strings.Contains(f[4], "\\") {
			return nil, errQualification
		}
		if _, ok := out[f[4]]; ok {
			return nil, errQualification
		}
		sep := 0
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep == 0 || len(f) != sep+4 {
			return nil, errQualification
		}
		ro, rw := false, false
		for _, o := range strings.Split(f[5], ",") {
			ro = ro || o == "ro"
			rw = rw || o == "rw"
		}
		if ro == rw {
			return nil, errQualification
		}
		out[f[4]] = qualificationMount{Target: f[4], Root: f[3], Device: f[2], MountID: id, ReadOnly: ro}
	}
	return out, nil
}
