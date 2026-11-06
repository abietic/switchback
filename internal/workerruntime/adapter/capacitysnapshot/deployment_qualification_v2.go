package capacitysnapshot

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"github.com/abietic/switchback/internal/platform/qualificationio"
)

const qualificationNativeV2 = "switchback.capacity.native_qualification_receipt.v2"
const qualificationTestBind = "/qualification-data"

// Worker is the frozen UID10002 parent shim in the finite qualification
// container. Each actual ioctl executor is a distinct explicit helper ELF.
// This does not qualify the normal production root-shim UID-switch startup.
type qualificationSetterV2 = qualificationio.SetterCase
type qualificationSettersV2 = qualificationio.SetterPayload

func decodeQualificationSettersV2(raw []byte) (qualificationSettersV2, error) {
	var encoded qualificationio.EncodedSetterPayload
	if err := strictJSON(raw, &encoded); err != nil {
		return qualificationSettersV2{}, err
	}
	return qualificationio.DecodeSetterPayload(encoded)
}

func validateQualificationV2Source(s qualificationSource, i DeploymentInventory) error {
	seen := map[string]string{}
	for _, b := range s.Builds {
		if b.Role == "worker" || b.Role == "worker-native-helper" || b.Role == "worker-compat-helper" {
			if b.ImageDigest != i.WorkerImageDigest {
				return errQualification
			}
			seen[b.Role] = b.BinaryDigest
		}
	}
	if len(seen) != 3 || seen["worker"] == seen["worker-native-helper"] || seen["worker"] == seen["worker-compat-helper"] || seen["worker-native-helper"] == seen["worker-compat-helper"] {
		return errQualification
	}
	return nil
}

func validateQualificationNativeV2(r qualificationNativeReceipt, out qualificationResult) error {
	checks, loaded, quota, err := validateQualificationNativeBase(r, out)
	start, se := time.Parse(time.RFC3339Nano, r.Window.Started)
	end, ee := time.Parse(time.RFC3339Nano, r.Window.Ended)
	if err != nil || se != nil || ee != nil || !end.After(start) || end.Sub(start) > 240*time.Second {
		return errQualification
	}
	if loaded.Worker.Cgroup != "/switchback.slice/switchback-workers.slice/docker-"+loaded.Worker.ContainerID+".scope" {
		return errQualification
	}
	setters, se := decodeQualificationSettersV2(checks["worker-setters"])
	if se != nil || len(setters.ABIs) != 4 {
		return errQualification
	}
	root, err := qualificationTestRoot(out.trust, r.Window.ID)
	if err != nil || setters.HostTestRoot != root {
		return errQualification
	}
	seen := map[string]bool{}
	var parent qualificationio.ProcessEvidence
	var parentMount qualificationMount
	executed, unsupported := map[string]bool{}, map[string]bool{}
	for _, s := range setters.ABIs {
		key := s.ABI + ":" + s.Command
		if seen[key] || s.Architecture != out.inputs.Architecture || !reflect.DeepEqual(qualificationWorker(s.Worker), loaded.Worker) || (s.Command != "FS_IOC_FSSETXATTR" && s.Command != "FS_IOC_SETFLAGS") || (s.ABI != "native" && s.ABI != "compat32") {
			return errQualification
		}
		seen[key] = true
		if validateQualificationProcess(s.ParentProcess, loaded.Worker, loaded.Worker.BinaryDigest, 0) != nil || s.ParentProcess.PID != loaded.Worker.PID || s.ParentProcess.StartTicks != loaded.Worker.StartTicks {
			return errQualification
		}
		if parent.PID != 0 && !sameQualificationProcess(parent, s.ParentProcess) {
			return errQualification
		}
		pm, pe := qualificationProcessMount(s.ParentProcess)
		if pe != nil || (parent.PID != 0 && parentMount != pm) {
			return errQualification
		}
		parentMount = pm
		parent = s.ParentProcess
		for j, raw := range [][]byte{loaded.Worker.StatusBefore, loaded.Worker.StatusAfter} {
			ws, we := procstatus.Parse(raw)
			ps := s.ParentProcess.HostBefore.Status
			if j == 1 {
				ps = s.ParentProcess.HostAfter.Status
			}
			observed, pe := procstatus.Parse(ps)
			leaderPID, lpe := qualificationNamespaceIDs(raw, "NSpid")
			leaderTGID, lte := qualificationNamespaceIDs(raw, "NStgid")
			if we != nil || pe != nil || lpe != nil || lte != nil || len(leaderPID) < 2 || len(leaderTGID) < 2 ||
				leaderPID[0] != loaded.Worker.PID || leaderTGID[0] != loaded.Worker.PID ||
				leaderPID[len(leaderPID)-1] != s.ParentProcess.NamespacePID ||
				leaderTGID[len(leaderTGID)-1] != s.ParentProcess.NamespaceTGID ||
				!qualificationSamePermissionStatus(ws, observed) {
				return errQualification
			}
		}
		if validateQualificationELF(s.Execution.ELF, s.ABI, s.Architecture, out.metadata.Inventory) != nil {
			return errQualification
		}
		x := s.Execution
		if x.ExecErrno != 0 {
			if s.ABI != "compat32" || x.ExecErrno != 8 || x.UnsupportedCause != "execve_ENOEXEC" || len(x.ExecOutput) > 4096 || len(x.Executors) != 0 || len(x.Facts) != 0 {
				return errQualification
			}
			unsupported[s.ABI] = true
		} else {
			executed[s.ABI] = true
			if x.UnsupportedCause != "" || len(x.ExecOutput) > 4096 || len(x.Executors) != 1 || len(x.Facts) != 1 || x.Executors[0].PID == s.ParentProcess.PID || validateQualificationProcess(x.Executors[0], loaded.Worker, x.ELF.Digest, s.ParentProcess.PID) != nil {
				return errQualification
			}
			executor := x.Executors[0]
			em, me := qualificationProcessMount(executor)
			if me != nil || em != parentMount {
				return errQualification
			}
			facts := x.Facts[0]
			for _, raw := range [][]byte{executor.NamespaceBefore.Stat, executor.NamespaceAfter.Stat} {
				_, ppid, _, pe := qualificationProcStat(raw)
				if pe != nil || ppid != s.ParentProcess.NamespacePID {
					return errQualification
				}
			}
			if validateQualificationFD(facts.TargetFD, executor, quota.Denied.ObjectBefore, root, "target") != nil || validateQualificationFD(facts.IOFD, executor, quota.Denied.ChildBefore, root, "target/io") != nil || facts.TargetFD.NamespaceBefore.FD == facts.IOFD.NamespaceBefore.FD || facts.TargetFD.HostBefore.FD == facts.IOFD.HostBefore.FD {
				return errQualification
			}
			if validateQualificationIoctls(facts.IO, s.ABI, s.Command, quota.Denied.ObjectBefore) != nil || !bytes.Equal(facts.IO.BeforeFSXAttr.Buffer, facts.TargetFD.NamespaceBefore.FSXAttr) || !bytes.Equal(facts.IO.AfterFSXAttr.Buffer, facts.TargetFD.NamespaceAfter.FSXAttr) {
				return errQualification
			}
		}
	}
	if executed["compat32"] && unsupported["compat32"] {
		return errQualification
	}
	return nil
}

func qualificationTestRoot(tr Trust, window string) (string, error) {
	if !qualificationWindowLabel(window) {
		return "", errQualification
	}
	domain := ""
	for _, c := range tr.Coverage {
		if c.Role == "product" {
			if domain != "" {
				return "", errQualification
			}
			domain = c.DomainID
		}
	}
	for _, s := range tr.Storage {
		if s.ID == domain && domain != "" && absolute(s.Path) {
			return path.Join(s.Path, ".qualification", window), nil
		}
	}
	return "", errQualification
}

func validateQualificationELF(e qualificationio.ELF, abi, arch string, inventory DeploymentInventory) error {
	class, machine, headerSize, artifact := uint64(2), uint64(62), 64, "worker-native-helper"
	if arch == "arm64" {
		machine = 183
	} else if arch != "amd64" {
		return errQualification
	}
	if abi == "compat32" {
		class, headerSize, artifact = 1, 52, "worker-compat-helper"
		machine = 3
		if arch == "arm64" {
			machine = 40
		}
	} else if abi != "native" {
		return errQualification
	}
	found := false
	for _, b := range inventory.Binaries {
		if b.Name == artifact && b.Digest == e.Digest {
			found = true
		}
	}
	if !found || !digestPattern.MatchString(e.Digest) || e.Artifact != artifact || e.Class != class || e.Machine != machine || len(e.Header) != headerSize {
		return errQualification
	}
	h := e.Header
	if !bytes.Equal(h[:4], []byte{0x7f, 'E', 'L', 'F'}) || uint64(h[4]) != class || h[5] != 1 || h[6] != 1 || (binary.LittleEndian.Uint16(h[16:18]) != 2 && binary.LittleEndian.Uint16(h[16:18]) != 3) || uint64(binary.LittleEndian.Uint16(h[18:20])) != machine || binary.LittleEndian.Uint32(h[20:24]) != 1 {
		return errQualification
	}
	offset := 52
	if class == 1 {
		offset = 40
	}
	if int(binary.LittleEndian.Uint16(h[offset:offset+2])) != headerSize {
		return errQualification
	}
	return nil
}

func qualificationProcStat(raw []byte) (pid, ppid, ticks uint64, err error) {
	fail := func() (uint64, uint64, uint64, error) { return 0, 0, 0, errQualification }
	if len(raw) == 0 || len(raw) > 65536 || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return fail()
	}
	s := string(raw)
	open, close := strings.Index(s, " ("), strings.LastIndex(s, ") ")
	if open <= 0 || close <= open {
		return fail()
	}
	pid, err = strconv.ParseUint(s[:open], 10, 32)
	f := strings.Fields(s[close+2:])
	if err != nil || pid == 0 || len(f) < 20 || len(f[0]) != 1 {
		return fail()
	}
	ppid, err = strconv.ParseUint(f[1], 10, 32)
	if err != nil {
		return fail()
	}
	ticks, err = strconv.ParseUint(f[19], 10, 64)
	if err != nil || ticks == 0 {
		return fail()
	}
	return
}

func qualificationNamespaceIDs(raw []byte, key string) ([]uint64, error) {
	var out []uint64
	for _, line := range strings.Split(string(raw), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || name != key {
			continue
		}
		if out != nil {
			return nil, errQualification
		}
		f := strings.Fields(value)
		if len(f) == 0 || len(f) > 32 {
			return nil, errQualification
		}
		for _, v := range f {
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil || n == 0 || strconv.FormatUint(n, 10) != v {
				return nil, errQualification
			}
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, errQualification
	}
	return out, nil
}

func qualificationMapping(host, local []byte, key string, first, last uint64) bool {
	h, he := qualificationNamespaceIDs(host, key)
	l, le := qualificationNamespaceIDs(local, key)
	if he != nil || le != nil || len(h) < 2 || h[0] != first || h[len(h)-1] != last || len(l) > len(h) || l[0] != last || l[len(l)-1] != last {
		return false
	}
	return reflect.DeepEqual(h[len(h)-len(l):], l)
}

func validateQualificationProcess(p qualificationio.ProcessEvidence, w qualificationWorker, digest string, parentPID uint64) error {
	if p.PID == 0 || p.PID != p.TGID || p.TID == 0 || p.NamespacePID == 0 || p.NamespacePID != p.NamespaceTGID || p.NamespaceTID == 0 || p.StartTicks == 0 || p.ThreadStartTicks == 0 || p.Boot != w.Boot {
		return errQualification
	}
	var selected []procstatus.Status
	var parents []uint64
	for j, s := range []qualificationio.ProcSnapshot{p.HostBefore, p.HostAfter, p.NamespaceBefore, p.NamespaceAfter} {
		host := j < 2
		pid, tid, tgid := p.NamespacePID, p.NamespaceTID, p.NamespaceTGID
		if host {
			pid, tid, tgid = p.PID, p.TID, p.TGID
		}
		ps, err := procstatus.Parse(s.Status)
		spid, ppid, st, se := qualificationProcStat(s.Stat)
		stid, tppid, tst, te := qualificationProcStat(s.ThreadStat)
		if err != nil || se != nil || te != nil || spid != pid || stid != tid || st != p.StartTicks || tst != p.ThreadStartTicks || tppid != ppid || ps.PID != tgid || ps.TID != tid || ps.TGID != tgid || ps.UIDs != [4]uint32{10002, 10002, 10002, 10002} || ps.GIDs != [4]uint32{10002, 10002, 10002, 10002} || ps.CapInh != "0000000000000000" || ps.CapPrm != "0000000000000000" || ps.CapEff != "0000000000000000" || ps.CapBnd != "0000000000000000" || ps.CapAmb != "0000000000000000" || ps.NoNewPrivs != 1 || ps.Seccomp != 2 || s.ExeDigest != digest || (host && !bytes.Equal(s.Cgroup, []byte("0::"+w.Cgroup+"\n"))) || (!host && !bytes.Equal(s.Cgroup, []byte("0::/\n")) && !bytes.Equal(s.Cgroup, []byte("0::"+w.Cgroup+"\n"))) {
			return errQualification
		}
		if _, err := qualificationMountInfo(s.MountInfo); err != nil {
			return errQualification
		}
		selected = append(selected, ps)
		parents = append(parents, ppid)
	}
	if selected[0] != selected[1] || selected[2] != selected[3] || parents[0] != parents[1] || parents[2] != parents[3] || (parentPID != 0 && parents[0] != parentPID) {
		return errQualification
	}
	for _, pair := range [][2]qualificationio.ProcSnapshot{{p.HostBefore, p.NamespaceBefore}, {p.HostAfter, p.NamespaceAfter}} {
		if !qualificationMapping(pair[0].Status, pair[1].Status, "NSpid", p.TID, p.NamespaceTID) || !qualificationMapping(pair[0].Status, pair[1].Status, "NStgid", p.TGID, p.NamespaceTGID) {
			return errQualification
		}
	}
	for _, pair := range [][2]qualificationio.ProcSnapshot{{p.HostBefore, p.HostAfter}, {p.NamespaceBefore, p.NamespaceAfter}} {
		for _, key := range []string{"NSpid", "NStgid"} {
			a, ae := qualificationNamespaceIDs(pair[0].Status, key)
			b, be := qualificationNamespaceIDs(pair[1].Status, key)
			if ae != nil || be != nil || !reflect.DeepEqual(a, b) {
				return errQualification
			}
		}
		if !bytes.Equal(pair[0].Cgroup, pair[1].Cgroup) {
			return errQualification
		}
	}
	return nil
}

func sameQualificationProcess(a, b qualificationio.ProcessEvidence) bool {
	return a.PID == b.PID && a.TID == b.TID && a.TGID == b.TGID && a.StartTicks == b.StartTicks && a.ThreadStartTicks == b.ThreadStartTicks && a.NamespacePID == b.NamespacePID && a.NamespaceTID == b.NamespaceTID && a.NamespaceTGID == b.NamespaceTGID && a.Boot == b.Boot && a.HostBefore.ExeDigest == b.HostBefore.ExeDigest
}

func validateQualificationFD(f qualificationio.FDProof, p qualificationio.ProcessEvidence, object qualificationTestObject, root, suffix string) error {
	if !sameQualificationFD(f.HostBefore, f.HostAfter) || !sameQualificationFD(f.NamespaceBefore, f.NamespaceAfter) {
		return errQualification
	}
	for _, v := range []qualificationio.FDIdentity{f.HostBefore, f.HostAfter, f.NamespaceBefore, f.NamespaceAfter} {
		if v.FD < 3 || v.Device != object.Device || v.FSUUID != object.FSUUID || v.Inode != object.Inode || v.Owner != object.Owner || v.Mode != object.Mode || v.ProjectID != object.ProjectID || v.MountID == 0 || len(v.Statx) != 256 || len(v.FSXAttr) != 28 {
			return errQualification
		}
		s := v.Statx
		if binary.LittleEndian.Uint32(s[:4])&0x111f != 0x111f || binary.LittleEndian.Uint32(s[16:20]) == 0 || uint64(binary.LittleEndian.Uint32(s[20:24])) != v.Owner || uint64(binary.LittleEndian.Uint16(s[28:30])) != v.Mode || binary.LittleEndian.Uint64(s[32:40]) != v.Inode || fmt.Sprintf("%d:%d", binary.LittleEndian.Uint32(s[136:140]), binary.LittleEndian.Uint32(s[140:144])) != v.Device || binary.LittleEndian.Uint64(s[144:152]) != v.MountID || uint64(binary.LittleEndian.Uint32(v.FSXAttr[12:16])) != v.ProjectID {
			return errQualification
		}
	}
	if f.HostBefore.Path != path.Join(root, suffix) || f.NamespaceBefore.Path != path.Join(qualificationTestBind, suffix) || f.HostBefore.MountID != object.MountID || !bytes.Equal(f.HostBefore.FSXAttr, f.NamespaceBefore.FSXAttr) {
		return errQualification
	}
	for j, hraw := range [][]byte{f.HostMountInfoBefore, f.HostMountInfoAfter} {
		host, err := qualificationMountInfo(hraw)
		if err != nil {
			return errQualification
		}
		nraw := p.NamespaceBefore.MountInfo
		if j == 1 {
			nraw = p.NamespaceAfter.MountInfo
		}
		ns, err := qualificationMountInfo(nraw)
		if err != nil {
			return errQualification
		}
		var hm qualificationMount
		for _, m := range host {
			if (f.HostBefore.Path == m.Target || strings.HasPrefix(f.HostBefore.Path, strings.TrimSuffix(m.Target, "/")+"/")) && len(m.Target) > len(hm.Target) {
				hm = m
			}
		}
		nm, ok := ns[qualificationTestBind]
		if !ok || !qualificationMountXFS(hraw, hm) || !qualificationMountXFS(nraw, nm) || hm.MountID != f.HostBefore.MountID || nm.MountID != f.NamespaceBefore.MountID || hm.Device != object.Device || nm.Device != object.Device || hm.ReadOnly || nm.ReadOnly {
			return errQualification
		}
		rel := strings.TrimPrefix(root, strings.TrimSuffix(hm.Target, "/"))
		if nm.Root != path.Join(hm.Root, rel) {
			return errQualification
		}
	}
	return nil
}

func validateQualificationIoctls(io qualificationio.SetterIO, abi, command string, target qualificationTestObject) error {
	size, get, set := 8, uint64(0x80086601), uint64(0x40086602)
	if abi == "compat32" {
		size, get, set = 4, 0x80046601, 0x40046602
	}
	if io.BeforeFSXAttr.Request != 0x801c581f || io.AfterFSXAttr.Request != 0x801c581f || io.BeforeFSXAttr.Errno != 0 || io.AfterFSXAttr.Errno != 0 || len(io.BeforeFSXAttr.Buffer) != 28 || !bytes.Equal(io.BeforeFSXAttr.Buffer, io.AfterFSXAttr.Buffer) || io.BeforeGetFlags.Request != get || io.AfterGetFlags.Request != get || io.BeforeGetFlags.Errno != 0 || io.AfterGetFlags.Errno != 0 || len(io.BeforeGetFlags.Buffer) != size || !bytes.Equal(io.BeforeGetFlags.Buffer, io.AfterGetFlags.Buffer) || io.Set.Errno != 1 || io.ReadBytes == 0 || io.ReadBytes > 16<<20 || io.WriteBytes == 0 || io.WriteBytes > 16<<20 {
		return errQualification
	}
	flags := uint64(binary.LittleEndian.Uint32(io.BeforeGetFlags.Buffer))
	if size == 8 {
		flags = binary.LittleEndian.Uint64(io.BeforeGetFlags.Buffer)
	}
	if flags > 0xffffffff || flags&0x20000000 == 0 || uint64(binary.LittleEndian.Uint32(io.BeforeFSXAttr.Buffer[12:16])) != target.ProjectID || binary.LittleEndian.Uint32(io.BeforeFSXAttr.Buffer[:4])&0x200 == 0 {
		return errQualification
	}
	var want []byte
	if command == "FS_IOC_FSSETXATTR" {
		set = 0x401c5820
		want = append([]byte(nil), io.BeforeFSXAttr.Buffer...)
		binary.LittleEndian.PutUint32(want[12:16], uint32(target.ProjectID+1))
	} else {
		want = make([]byte, size)
		binary.LittleEndian.PutUint32(want, uint32(flags&^0x20000000))
	}
	if io.Set.Request != set || !bytes.Equal(io.Set.Buffer, want) {
		return errQualification
	}
	return nil
}

// Preserve the full raw buffers, comparing only identity across finite I/O.
// statx size, timestamps and block usage may legitimately change on writes.
func sameQualificationFD(a, b qualificationio.FDIdentity) bool {
	a.Statx, b.Statx = nil, nil
	return reflect.DeepEqual(a, b)
}

func qualificationWindowLabel(id string) bool {
	if len(id) == 0 || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func qualificationMountXFS(raw []byte, m qualificationMount) bool {
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			return false
		}
		id, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return false
		}
		if id != m.MountID {
			continue
		}
		if found {
			return false
		}
		found = true
		for j := 6; j < len(f); j++ {
			if f[j] == "-" {
				if j+1 >= len(f) || f[j+1] != "xfs" {
					return false
				}
				break
			}
		}
	}
	return found
}

// Host /proc/<task>/mountinfo and the helper's own /proc view refer to the
// same task mount namespace. The producer's own host mounts are separate FD
// proof tables and may have different mount IDs and filesystem-relative roots.
func qualificationProcessMount(p qualificationio.ProcessEvidence) (qualificationMount, error) {
	var expected qualificationMount
	for _, s := range []qualificationio.ProcSnapshot{p.HostBefore, p.HostAfter, p.NamespaceBefore, p.NamespaceAfter} {
		mounts, err := qualificationMountInfo(s.MountInfo)
		if err != nil {
			return expected, err
		}
		m, ok := mounts[qualificationTestBind]
		if !ok || m.ReadOnly || !qualificationMountXFS(s.MountInfo, m) || (expected.MountID != 0 && m != expected) {
			return qualificationMount{}, errQualification
		}
		expected = m
	}
	return expected, nil
}
