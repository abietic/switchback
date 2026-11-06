package capacitysnapshot

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/qualificationio"
)

func qualificationV2Stat(pid, parent, ticks uint64) []byte {
	f := make([]string, 40)
	for j := range f {
		f[j] = "0"
	}
	f[0], f[1], f[19] = "S", strconv.FormatUint(parent, 10), strconv.FormatUint(ticks, 10)
	return []byte(fmt.Sprintf("%d (qualification helper) %s\n", pid, strings.Join(f, " ")))
}

func qualificationV2Status(host, ns uint64, local bool) []byte {
	pid := host
	mapping := fmt.Sprintf("%d %d", host, ns)
	if local {
		pid, mapping = ns, strconv.FormatUint(ns, 10)
	}
	raw := string(qualificationStatus(0))
	raw = strings.ReplaceAll(raw, "42", strconv.FormatUint(pid, 10))
	raw = strings.Replace(raw, "Uid:\t0 0 0 0", "Uid:\t10002 10002 10002 10002", 1)
	raw = strings.Replace(raw, "Gid:\t0 0 0 0", "Gid:\t10002 10002 10002 10002", 1)
	return []byte(raw + "NSpid:\t" + mapping + "\nNStgid:\t" + mapping + "\n")
}

func qualificationV2Process(w qualificationWorker, pid, ns, parent, localParent, ticks uint64, digest string, mounts []byte) qualificationio.ProcessEvidence {
	p := qualificationio.ProcessEvidence{PID: pid, TID: pid, TGID: pid, NamespacePID: ns, NamespaceTID: ns, NamespaceTGID: ns, StartTicks: ticks, ThreadStartTicks: ticks, Boot: w.Boot}
	h := qualificationio.ProcSnapshot{Stat: qualificationV2Stat(pid, parent, ticks), ThreadStat: qualificationV2Stat(pid, parent, ticks), Status: qualificationV2Status(pid, ns, false), Cgroup: []byte("0::" + w.Cgroup + "\n"), MountInfo: mounts, ExeDigest: digest}
	n := h
	n.Stat, n.ThreadStat, n.Status = qualificationV2Stat(ns, localParent, ticks), qualificationV2Stat(ns, localParent, ticks), qualificationV2Status(pid, ns, true)
	p.HostBefore, p.HostAfter, p.NamespaceBefore, p.NamespaceAfter = h, h, n, n
	return p
}

func qualificationV2ELF(abi, arch string) qualificationio.ELF {
	class, machine, size, name := uint64(2), uint64(62), 64, "worker-native-helper"
	if arch == "arm64" {
		machine = 183
	}
	if abi == "compat32" {
		class, machine, size, name = 1, 3, 52, "worker-compat-helper"
		if arch == "arm64" {
			machine = 40
		}
	}
	h := make([]byte, size)
	copy(h, []byte{0x7f, 'E', 'L', 'F', byte(class), 1, 1})
	binary.LittleEndian.PutUint16(h[16:18], 2)
	binary.LittleEndian.PutUint16(h[18:20], uint16(machine))
	binary.LittleEndian.PutUint32(h[20:24], 1)
	offset := 52
	if class == 1 {
		offset = 40
	}
	binary.LittleEndian.PutUint16(h[offset:offset+2], uint16(size))
	return qualificationio.ELF{Artifact: name, Digest: Digest([]byte(name + "-binary")), Header: h, Class: class, Machine: machine}
}

func qualificationV2FD(o qualificationTestObject, fd, mount uint64, pathname string, attrs []byte) qualificationio.FDIdentity {
	s := make([]byte, 256)
	binary.LittleEndian.PutUint32(s[:4], 0x111f)
	binary.LittleEndian.PutUint32(s[16:20], 1)
	binary.LittleEndian.PutUint32(s[20:24], uint32(o.Owner))
	binary.LittleEndian.PutUint16(s[28:30], uint16(o.Mode))
	binary.LittleEndian.PutUint64(s[32:40], o.Inode)
	parts := strings.Split(o.Device, ":")
	major, _ := strconv.ParseUint(parts[0], 10, 32)
	minor, _ := strconv.ParseUint(parts[1], 10, 32)
	binary.LittleEndian.PutUint32(s[136:140], uint32(major))
	binary.LittleEndian.PutUint32(s[140:144], uint32(minor))
	binary.LittleEndian.PutUint64(s[144:152], mount)
	return qualificationio.FDIdentity{FD: fd, Path: pathname, Device: o.Device, FSUUID: o.FSUUID, Inode: o.Inode, MountID: mount, Owner: o.Owner, Mode: o.Mode, ProjectID: o.ProjectID, Statx: s, FSXAttr: append([]byte(nil), attrs...)}
}

func qualificationUpgradeV2(t *testing.T, tr Trust, f map[string][]byte) (Trust, map[string][]byte) {
	t.Helper()
	var src qualificationSource
	var inv DeploymentInventory
	var n qualificationNativeReceipt
	json.Unmarshal(f["source-manifest.json"], &src)
	json.Unmarshal(f["deployment-inventory.json"], &inv)
	json.Unmarshal(f["native-enforcement-receipt.json"], &n)
	for _, name := range []string{"worker-native-helper", "worker-compat-helper"} {
		b := qualificationBuild{Role: name, Toolchain: "frozen-test-cc", InputRoot: qualificationDigest(src.Files), BinaryDigest: Digest([]byte(name + "-binary")), ImageDigest: inv.WorkerImageDigest}
		src.Builds = append(src.Builds, b)
		inv.Binaries = append(inv.Binaries, AdmissionBinary{Name: name, Digest: b.BinaryDigest})
	}
	sort.Slice(src.Builds, func(i, j int) bool { return src.Builds[i].Role < src.Builds[j].Role })
	var loaded qualificationLoaded
	var q qualificationQuota
	var old struct {
		ABIs []qualificationSetter `json:"abis"`
	}
	for _, c := range n.Checks {
		switch c.ID {
		case "loaded-identity":
			json.Unmarshal(c.Observation.Data, &loaded)
		case "byte-quota":
			json.Unmarshal(c.Observation.Data, &q)
		case "worker-setters":
			json.Unmarshal(c.Observation.Data, &old)
		}
	}
	root, err := qualificationTestRoot(tr, n.Window.ID)
	if err != nil {
		t.Fatal(err)
	}
	product := path.Dir(path.Dir(root))
	mounts := []byte(fmt.Sprintf("99 0 0:1 / / rw - overlay overlay rw\n701 99 %s /backing/product/.qualification/%s /qualification-data rw - xfs /dev/test rw\n", tr.Identity.Device, n.Window.ID))
	hostMounts := []byte(fmt.Sprintf("%d 0 %s /backing/product %s rw - xfs /dev/test rw\n", tr.Identity.MountID, tr.Identity.Device, product))
	loaded.Worker.Cgroup = "/switchback.slice/switchback-workers.slice/docker-" + loaded.Worker.ContainerID + ".scope"
	parent := qualificationV2Process(loaded.Worker, 42, 1, 2, 0, 123, loaded.Worker.BinaryDigest, mounts)
	loaded.Worker.StatusBefore, loaded.Worker.StatusAfter = parent.HostBefore.Status, parent.HostAfter.Status
	setters := qualificationSettersV2{HostTestRoot: root}
	for j, v := range old.ABIs {
		e := qualificationV2ELF(v.ABI, src.Inputs.Architecture)
		p := qualificationV2Process(loaded.Worker, uint64(60+j), uint64(10+j), parent.PID, parent.NamespacePID, uint64(200+j), e.Digest, mounts)
		size, get, set := 8, uint64(0x80086601), uint64(0x40086602)
		if v.ABI == "compat32" {
			size, get, set = 4, 0x80046601, 0x40046602
		}
		getbuf := make([]byte, size)
		copy(getbuf, v.BeforeGetFlags)
		if v.Command == "FS_IOC_FSSETXATTR" {
			set = 0x401c5820
		}
		io := qualificationio.SetterIO{BeforeFSXAttr: qualificationio.Ioctl{Request: 0x801c581f, Buffer: v.BeforeFSXAttr}, AfterFSXAttr: qualificationio.Ioctl{Request: 0x801c581f, Buffer: v.AfterFSXAttr}, BeforeGetFlags: qualificationio.Ioctl{Request: get, Buffer: getbuf}, AfterGetFlags: qualificationio.Ioctl{Request: get, Buffer: append([]byte(nil), getbuf...)}, Set: qualificationio.Ioctl{Request: set, Buffer: v.Attempt, Errno: 1}, ReadBytes: 1, WriteBytes: 1}
		fd := func(o qualificationTestObject, number uint64, suffix string, attrs []byte) qualificationio.FDProof {
			h := qualificationV2FD(o, number, o.MountID, path.Join(root, suffix), attrs)
			l := qualificationV2FD(o, number+10, 701, path.Join(qualificationTestBind, suffix), attrs)
			return qualificationio.FDProof{HostMountInfoBefore: hostMounts, HostMountInfoAfter: hostMounts, HostBefore: h, HostAfter: h, NamespaceBefore: l, NamespaceAfter: l}
		}
		x := qualificationio.SetterExecution{ELF: e, Executors: []qualificationio.ProcessEvidence{p}, Facts: []qualificationio.SetterFacts{{TargetFD: fd(q.Denied.ObjectBefore, 5, "target", q.Denied.FSXAttr), IOFD: fd(q.Denied.ChildBefore, 6, "target/io", q.Denied.ChildFSXAttr), IO: io}}, ExecOutput: []byte{}}
		setters.ABIs = append(setters.ABIs, qualificationSetterV2{Worker: qualificationio.WorkerParent(loaded.Worker), ParentProcess: parent, Architecture: src.Inputs.Architecture, ABI: v.ABI, Command: v.Command, Execution: x})
	}
	for j := range n.Checks {
		if n.Checks[j].ID == "loaded-identity" {
			n.Checks[j] = qualificationCheckValue("loaded-identity", loaded)
		}
		if n.Checks[j].ID == "worker-setters" {
			n.Checks[j] = qualificationCheckValue("worker-setters", setters)
		}
	}
	n.Schema = qualificationNativeV2
	f["native-enforcement-receipt.json"] = qualificationJSON(n)
	f["source-manifest.json"] = qualificationJSON(src)
	inv.SourceManifestDigest = Digest(f["source-manifest.json"])
	f["deployment-inventory.json"] = qualificationJSON(inv)
	var m AdmissionManifest
	var b qualificationBinding
	json.Unmarshal(f["admission-manifest.json"], &m)
	json.Unmarshal(f["publisher-bindings.json"], &b)
	m.SourceManifestDigest, m.DeploymentInventoryDigest = inv.SourceManifestDigest, Digest(f["deployment-inventory.json"])
	candidate, err := qualificationCandidate(tr, m, DeploymentPublisherBinding{Core: b.Core, WorkerImageDigest: b.WorkerImageDigest})
	if err != nil {
		t.Fatal(err)
	}
	var host qualificationHostReceipt
	json.Unmarshal(f["host-boundary-receipt.json"], &host)
	host.Candidate, n.Candidate = candidate, candidate
	f["host-boundary-receipt.json"], f["native-enforcement-receipt.json"] = qualificationJSON(host), qualificationJSON(n)
	f["admission-manifest.json"] = qualificationJSON(m)
	return qualificationRebind(tr, f), f
}

func TestDeploymentQualificationV2FullValidator(t *testing.T) {
	tr, f := qualificationFixture(t)
	if _, err := validateDeploymentQualification(tr, f); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentQualificationProductionRejectsLegacyV1(t *testing.T) {
	tr, f := qualificationLegacyFixture(t)
	if _, err := validateLegacyDeploymentQualificationV1(tr, f); err != nil {
		t.Fatal("historical pure replay", err)
	}
	if _, err := validateDeploymentQualification(tr, f); err == nil {
		t.Fatal("production validator accepted native v1 fallback")
	}
}

func qualificationMutateV2(t *testing.T, mutate func(*qualificationSettersV2)) {
	t.Helper()
	qualificationMutateCheck(t, "native-enforcement-receipt.json", "worker-setters", func(raw []byte) []byte {
		s, err := decodeQualificationSettersV2(raw)
		if err != nil {
			t.Fatal(err)
		}
		mutate(&s)
		return qualificationJSON(s)
	})
}

func TestDeploymentQualificationV2ExecutorSwaps(t *testing.T) {
	cases := map[string]func(*qualificationSetterV2){
		"parent-pid":         func(s *qualificationSetterV2) { s.ParentProcess.PID++ },
		"parent-start":       func(s *qualificationSetterV2) { s.ParentProcess.StartTicks++ },
		"executor-is-parent": func(s *qualificationSetterV2) { s.Execution.Executors[0] = s.ParentProcess },
		"executor-pid":       func(s *qualificationSetterV2) { s.Execution.Executors[0].PID++ },
		"executor-tid":       func(s *qualificationSetterV2) { s.Execution.Executors[0].TID++ },
		"executor-start":     func(s *qualificationSetterV2) { s.Execution.Executors[0].StartTicks++ },
		"executor-boot":      func(s *qualificationSetterV2) { s.Execution.Executors[0].Boot = "other" },
		"namespace-pid":      func(s *qualificationSetterV2) { s.Execution.Executors[0].NamespacePID++ },
		"elf-parent-alias":   func(s *qualificationSetterV2) { s.Execution.ELF.Digest = s.Worker.BinaryDigest },
		"elf-machine":        func(s *qualificationSetterV2) { s.Execution.ELF.Header[18]++ },
		"elf-class":          func(s *qualificationSetterV2) { s.Execution.ELF.Header[4] = 1 },
		"elf-bad-magic":      func(s *qualificationSetterV2) { s.Execution.ELF.Header[0] = 0 },
		"abi":                func(s *qualificationSetterV2) { s.ABI = "compat32" },
		"fd-inode":           func(s *qualificationSetterV2) { s.Execution.Facts[0].TargetFD.NamespaceBefore.Inode++ },
		"fd-raw-inode":       func(s *qualificationSetterV2) { s.Execution.Facts[0].TargetFD.NamespaceBefore.Statx[32]++ },
		"fd-device":          func(s *qualificationSetterV2) { s.Execution.Facts[0].TargetFD.NamespaceBefore.Device = "9:9" },
		"fd-project":         func(s *qualificationSetterV2) { s.Execution.Facts[0].TargetFD.NamespaceBefore.FSXAttr[12]++ },
		"fd-path":            func(s *qualificationSetterV2) { s.Execution.Facts[0].TargetFD.NamespaceBefore.Path = "/other" },
		"io-child":           func(s *qualificationSetterV2) { s.Execution.Facts[0].IOFD = s.Execution.Facts[0].TargetFD },
		"ioctl-request":      func(s *qualificationSetterV2) { s.Execution.Facts[0].IO.BeforeGetFlags.Request = 0x80046601 },
		"native-short-buffer": func(s *qualificationSetterV2) {
			s.Execution.Facts[0].IO.BeforeGetFlags.Buffer = s.Execution.Facts[0].IO.BeforeGetFlags.Buffer[:4]
		},
		"get-buffer-drift": func(s *qualificationSetterV2) { s.Execution.Facts[0].IO.AfterGetFlags.Buffer[0]++ },
		"set-buffer": func(s *qualificationSetterV2) {
			s.Execution.Facts[0].IO.Set.Buffer = s.Execution.Facts[0].IO.BeforeFSXAttr.Buffer
		},
		"syscall-success": func(s *qualificationSetterV2) { s.Execution.Facts[0].IO.Set.Errno = 0 },
		"exec-other":      func(s *qualificationSetterV2) { s.Execution.ExecErrno = 2 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) { qualificationMutateV2(t, func(v *qualificationSettersV2) { mutate(&v.ABIs[0]) }) })
	}
	for _, which := range []string{"parent", "executor"} {
		for _, field := range []string{"uid", "gid", "caps", "nnp", "seccomp", "boot", "cgroup", "exe", "mapping", "local-parent"} {
			t.Run(which+"-"+field, func(t *testing.T) {
				qualificationMutateV2(t, func(v *qualificationSettersV2) {
					s := &v.ABIs[0]
					p := &s.ParentProcess
					if which == "executor" {
						p = &s.Execution.Executors[0]
					}
					switch field {
					case "uid":
						p.HostBefore.Status = []byte(strings.Replace(string(p.HostBefore.Status), "Uid:\t10002 10002 10002 10002", "Uid:\t0 0 0 0", 1))
					case "gid":
						p.NamespaceAfter.Status = []byte(strings.Replace(string(p.NamespaceAfter.Status), "Gid:\t10002 10002 10002 10002", "Gid:\t0 0 0 0", 1))
					case "caps":
						p.HostAfter.Status = []byte(strings.Replace(string(p.HostAfter.Status), "CapBnd:\t0000000000000000", "CapBnd:\t0000000000200000", 1))
					case "nnp":
						p.NamespaceBefore.Status = []byte(strings.Replace(string(p.NamespaceBefore.Status), "NoNewPrivs:\t1", "NoNewPrivs:\t0", 1))
					case "seccomp":
						p.HostBefore.Status = []byte(strings.Replace(string(p.HostBefore.Status), "Seccomp:\t2", "Seccomp:\t0", 1))
					case "boot":
						p.Boot = "wrong"
					case "cgroup":
						p.HostAfter.Cgroup = []byte("0::/other\n")
					case "exe":
						p.NamespaceBefore.ExeDigest = Digest([]byte("other"))
					case "mapping":
						p.HostBefore.Status = []byte(strings.Replace(string(p.HostBefore.Status), "NSpid:", "Missing:", 1))
					case "local-parent":
						p.NamespaceBefore.Stat = qualificationV2Stat(p.NamespacePID, 999, p.StartTicks)
					}
				})
			})
		}
	}
}

func TestDeploymentQualificationV2CompatUnsupported(t *testing.T) {
	tr, f := qualificationFixture(t)
	var n qualificationNativeReceipt
	json.Unmarshal(f["native-enforcement-receipt.json"], &n)
	for j := range n.Checks {
		if n.Checks[j].ID == "worker-setters" {
			v, err := decodeQualificationSettersV2(n.Checks[j].Observation.Data)
			if err != nil {
				t.Fatal(err)
			}
			for i := range v.ABIs {
				if v.ABIs[i].ABI == "compat32" {
					e := v.ABIs[i].Execution.ELF
					v.ABIs[i].Execution = qualificationio.SetterExecution{ELF: e, Executors: []qualificationio.ProcessEvidence{}, Facts: []qualificationio.SetterFacts{}, ExecErrno: 8, ExecOutput: []byte{}, UnsupportedCause: "execve_ENOEXEC"}
				}
			}
			n.Checks[j] = qualificationCheckValue("worker-setters", v)
		}
	}
	f["native-enforcement-receipt.json"] = qualificationJSON(n)
	tr = qualificationRebind(tr, f)
	if _, err := validateDeploymentQualification(tr, f); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"general-error", "fabricated-executor", "missing-helper", "wrong-elf", "mixed-execution"} {
		t.Run(bad, func(t *testing.T) {
			copy := qualificationClone(f)
			var r qualificationNativeReceipt
			json.Unmarshal(copy["native-enforcement-receipt.json"], &r)
			for j := range r.Checks {
				if r.Checks[j].ID == "worker-setters" {
					v, err := decodeQualificationSettersV2(r.Checks[j].Observation.Data)
					if err != nil {
						t.Fatal(err)
					}
					s := &v.ABIs[2]
					switch bad {
					case "general-error":
						s.Execution.ExecErrno = 13
					case "fabricated-executor":
						s.Execution.Executors = append(s.Execution.Executors, s.ParentProcess)
					case "missing-helper":
						s.Execution.ELF.Digest = ""
					case "wrong-elf":
						s.Execution.ELF.Header[4] = 2
					case "mixed-execution":
						s.Execution.ExecErrno = 0
					}
					r.Checks[j] = qualificationCheckValue("worker-setters", v)
				}
			}
			copy["native-enforcement-receipt.json"] = qualificationJSON(r)
			bt := qualificationRebind(tr, copy)
			if _, err := validateDeploymentQualification(bt, copy); err == nil {
				t.Fatal("unsupported failure accepted")
			}
		})
	}
}

func TestDeploymentQualificationV2CheckCoverageAndWindow(t *testing.T) {
	for _, bad := range []string{"duplicate", "missing", "unknown", "long-window"} {
		t.Run(bad, func(t *testing.T) {
			tr, f := qualificationFixture(t)
			var n qualificationNativeReceipt
			json.Unmarshal(f["native-enforcement-receipt.json"], &n)
			switch bad {
			case "duplicate":
				n.Checks[0] = n.Checks[1]
			case "missing":
				n.Checks = n.Checks[:6]
			case "unknown":
				n.Checks[0].ID = "other"
			case "long-window":
				n.Window.Ended = "2026-10-22T01:04:01Z"
				var h qualificationHostReceipt
				json.Unmarshal(f["host-boundary-receipt.json"], &h)
				h.Window = n.Window
				f["host-boundary-receipt.json"] = qualificationJSON(h)
			}
			f["native-enforcement-receipt.json"] = qualificationJSON(n)
			tr = qualificationRebind(tr, f)
			if _, err := validateDeploymentQualification(tr, f); err == nil {
				t.Fatal("invalid exact coverage/window accepted")
			}
		})
	}
}

func TestDeploymentQualificationV2RealBufferAndNamespaceViews(t *testing.T) {
	for _, name := range []string{"amd64", "private-cgroup-root", "volatile-statx", "separate-task-tid"} {
		t.Run(name, func(t *testing.T) {
			tr, f := qualificationFixture(t)
			if name == "amd64" {
				tr, f = qualificationLegacyFixture(t)
				var src qualificationSource
				var inv DeploymentInventory
				json.Unmarshal(f["source-manifest.json"], &src)
				json.Unmarshal(f["deployment-inventory.json"], &inv)
				src.Inputs.Architecture = "amd64"
				inv.DeploymentInputsDigest = qualificationDigest(src.Inputs)
				f["source-manifest.json"], f["deployment-inventory.json"] = qualificationJSON(src), qualificationJSON(inv)
				tr, f = qualificationUpgradeV2(t, tr, f)
			} else {
				var n qualificationNativeReceipt
				json.Unmarshal(f["native-enforcement-receipt.json"], &n)
				for j := range n.Checks {
					if n.Checks[j].ID == "worker-setters" {
						v, err := decodeQualificationSettersV2(n.Checks[j].Observation.Data)
						if err != nil {
							t.Fatal(err)
						}
						for i := range v.ABIs {
							s := &v.ABIs[i]
							switch name {
							case "private-cgroup-root":
								s.ParentProcess.NamespaceBefore.Cgroup = []byte("0::/\n")
								s.ParentProcess.NamespaceAfter.Cgroup = []byte("0::/\n")
								s.Execution.Executors[0].NamespaceBefore.Cgroup = []byte("0::/\n")
								s.Execution.Executors[0].NamespaceAfter.Cgroup = []byte("0::/\n")
							case "volatile-statx":
								for _, proof := range []*qualificationio.FDProof{&s.Execution.Facts[0].TargetFD, &s.Execution.Facts[0].IOFD} {
									for _, fd := range []*qualificationio.FDIdentity{&proof.HostAfter, &proof.NamespaceAfter} {
										fd.Statx = append([]byte(nil), fd.Statx...)
										binary.LittleEndian.PutUint64(fd.Statx[40:48], 4096)
										binary.LittleEndian.PutUint64(fd.Statx[96:104], 1234)
									}
								}
							case "separate-task-tid":
								p := &s.Execution.Executors[0]
								p.TID += 100
								p.NamespaceTID += 100
								p.ThreadStartTicks += 5
								for k, snap := range []*qualificationio.ProcSnapshot{&p.HostBefore, &p.HostAfter, &p.NamespaceBefore, &p.NamespaceAfter} {
									tid, parent := p.TID, s.ParentProcess.PID
									if k >= 2 {
										tid, parent = p.NamespaceTID, s.ParentProcess.NamespacePID
									}
									snap.ThreadStat = qualificationV2Stat(tid, parent, p.ThreadStartTicks)
									old, new := fmt.Sprintf("Pid:\t%d\n", p.PID), fmt.Sprintf("Pid:\t%d\n", p.TID)
									mapping := fmt.Sprintf("NSpid:\t%d %d", p.PID, p.NamespacePID)
									newmap := fmt.Sprintf("NSpid:\t%d %d", p.TID, p.NamespaceTID)
									if k >= 2 {
										old, new = fmt.Sprintf("Pid:\t%d\n", p.NamespacePID), fmt.Sprintf("Pid:\t%d\n", p.NamespaceTID)
										mapping, newmap = fmt.Sprintf("NSpid:\t%d", p.NamespacePID), fmt.Sprintf("NSpid:\t%d", p.NamespaceTID)
									}
									snap.Status = []byte(strings.Replace(strings.Replace(string(snap.Status), old, new, 1), mapping, newmap, 1))
								}
							}
						}
						n.Checks[j] = qualificationCheckValue("worker-setters", v)
					}
				}
				f["native-enforcement-receipt.json"] = qualificationJSON(n)
				tr = qualificationRebind(tr, f)
			}
			if _, err := validateDeploymentQualification(tr, f); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeploymentQualificationV2HelpersAndSchemaRequired(t *testing.T) {
	for _, name := range []string{"missing-native-helper", "missing-compat-helper", "aliased-helper", "v1-schema-with-v2-proof"} {
		t.Run(name, func(t *testing.T) {
			f := newRoleUIDFixture(t)
			if name == "v1-schema-with-v2-proof" {
				f.native.Schema = "switchback.capacity.native_enforcement_receipt.v1"
			} else {
				role := "worker-native-helper"
				if name == "missing-compat-helper" {
					role = "worker-compat-helper"
				}
				for j, b := range f.source.Builds {
					if b.Role == role {
						if name == "aliased-helper" {
							f.source.Builds[j].BinaryDigest = Digest([]byte("worker-binary"))
						} else {
							f.source.Builds = append(f.source.Builds[:j], f.source.Builds[j+1:]...)
						}
						break
					}
				}
				for j, b := range f.inventory.Binaries {
					if b.Name == role {
						if name == "aliased-helper" {
							f.inventory.Binaries[j].Digest = Digest([]byte("worker-binary"))
						} else {
							f.inventory.Binaries = append(f.inventory.Binaries[:j], f.inventory.Binaries[j+1:]...)
						}
						break
					}
				}
			}
			f.seal(t)
			if _, err := validateDeploymentQualification(f.trust, f.files); err == nil {
				t.Fatal("new production helper/schema requirement bypassed")
			}
		})
	}
}

func TestDeploymentQualificationV2MountFSType(t *testing.T) {
	qualificationMutateV2(t, func(v *qualificationSettersV2) {
		f := &v.ABIs[0].Execution.Facts[0].TargetFD
		f.HostMountInfoBefore = []byte(strings.Replace(string(f.HostMountInfoBefore), " - xfs ", " - ext4 ", 1))
		f.HostMountInfoAfter = append([]byte(nil), f.HostMountInfoBefore...)
	})
}

func TestDeploymentQualificationV2DeduplicatesLargeMountTables(t *testing.T) {
	tr, f := qualificationFixture(t)
	var n qualificationNativeReceipt
	json.Unmarshal(f["native-enforcement-receipt.json"], &n)
	for j := range n.Checks {
		if n.Checks[j].ID == "worker-setters" {
			v, err := decodeQualificationSettersV2(n.Checks[j].Observation.Data)
			if err != nil {
				t.Fatal(err)
			}
			grow := func(base []byte) []byte {
				out := append([]byte(nil), base...)
				for k := 0; k < 512; k++ {
					out = append(out, []byte(fmt.Sprintf("%d 99 0:1 / /unrelated/mount-%04d rw - tmpfs tmpfs rw\n", 1000+k, k))...)
				}
				return out
			}
			ns, host := grow(v.ABIs[0].ParentProcess.NamespaceBefore.MountInfo), grow(v.ABIs[0].Execution.Facts[0].TargetFD.HostMountInfoBefore)
			for i := range v.ABIs {
				s := &v.ABIs[i]
				for _, p := range []*qualificationio.ProcessEvidence{&s.ParentProcess, &s.Execution.Executors[0]} {
					p.HostBefore.MountInfo, p.HostAfter.MountInfo, p.NamespaceBefore.MountInfo, p.NamespaceAfter.MountInfo = ns, ns, ns, ns
				}
				for _, p := range []*qualificationio.FDProof{&s.Execution.Facts[0].TargetFD, &s.Execution.Facts[0].IOFD} {
					p.HostMountInfoBefore, p.HostMountInfoAfter = host, host
				}
			}
			logical, _ := json.Marshal(v)
			wire, err := qualificationio.EncodeSetterPayload(v)
			if err != nil {
				t.Fatal(err)
			}
			encoded := qualificationJSON(wire)
			if len(logical) <= 256<<10 || len(encoded) > 256<<10 || len(wire.MountTables) != 2 {
				t.Fatalf("logical=%d encoded=%d tables=%d", len(logical), len(encoded), len(wire.MountTables))
			}
			decoded, err := decodeQualificationSettersV2(encoded)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range decoded.ABIs {
				for _, p := range []qualificationio.ProcessEvidence{s.ParentProcess, s.Execution.Executors[0]} {
					for _, snapshot := range []qualificationio.ProcSnapshot{p.HostBefore, p.HostAfter, p.NamespaceBefore, p.NamespaceAfter} {
						if !bytes.Equal(snapshot.MountInfo, ns) {
							t.Fatal("mount bytes lost")
						}
					}
				}
				for _, p := range []qualificationio.FDProof{s.Execution.Facts[0].TargetFD, s.Execution.Facts[0].IOFD} {
					if !bytes.Equal(p.HostMountInfoBefore, host) || !bytes.Equal(p.HostMountInfoAfter, host) {
						t.Fatal("host raw lost")
					}
				}
			}
			n.Checks[j] = qualificationCheckBytes("worker-setters", encoded)
			t.Logf("logical raw payload=%d bytes; encoded complete check=%d bytes; unique raw tables=%d; ns=%d host=%d bytes", len(logical), len(encoded), len(wire.MountTables), len(ns), len(host))
		}
	}
	f["native-enforcement-receipt.json"] = qualificationJSON(n)
	tr = qualificationRebind(tr, f)
	if _, err := validateDeploymentQualification(tr, f); err != nil {
		t.Fatal("full validator rejected complete deduplicated evidence", err)
	}
}

func TestDeploymentQualificationV2MountTableReferences(t *testing.T) {
	for _, bad := range []string{"wrong-ref", "wrong-valid-ref", "missing-ref", "bad-digest", "duplicate", "unused", "raw-plus-ref", "raw-only", "oversized"} {
		t.Run(bad, func(t *testing.T) {
			qualificationMutateCheck(t, "native-enforcement-receipt.json", "worker-setters", func(raw []byte) []byte {
				var v qualificationio.EncodedSetterPayload
				if err := strictJSON(raw, &v); err != nil {
					t.Fatal(err)
				}
				switch bad {
				case "wrong-ref":
					v.ABIs[0].ParentProcess.HostBefore.MountInfoRef = Digest([]byte("missing"))
				case "wrong-valid-ref":
					v.ABIs[0].ParentProcess.HostBefore.MountInfoRef = v.ABIs[0].Execution.Facts[0].TargetFD.HostMountInfoBeforeRef
				case "missing-ref":
					v.ABIs[0].ParentProcess.HostBefore.MountInfoRef = ""
				case "bad-digest":
					v.MountTables[0].Digest = Digest([]byte("different"))
				case "duplicate":
					v.MountTables = append(v.MountTables, v.MountTables[0])
					sort.Slice(v.MountTables, func(i, j int) bool { return v.MountTables[i].Digest < v.MountTables[j].Digest })
				case "unused":
					b := []byte("unused mount table\n")
					v.MountTables = append(v.MountTables, qualificationio.MountTable{Digest: Digest(b), Raw: b})
					sort.Slice(v.MountTables, func(i, j int) bool { return v.MountTables[i].Digest < v.MountTables[j].Digest })
				case "oversized":
					v.MountTables[0].Raw = make([]byte, (256<<10)+1)
					v.MountTables[0].Digest = Digest(v.MountTables[0].Raw)
				case "raw-plus-ref", "raw-only":
					var obj map[string]any
					json.Unmarshal(raw, &obj)
					snap := obj["abis"].([]any)[0].(map[string]any)["parent_process"].(map[string]any)["host_before"].(map[string]any)
					snap["mountinfo"] = []byte("raw mount table\n")
					if bad == "raw-only" {
						delete(snap, "mountinfo_ref")
					}
					return qualificationJSON(obj)
				}
				return qualificationJSON(v)
			})
		})
	}
}

func TestDeploymentQualificationV2RecordedWindowBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, end    string
		legacy, want bool
	}{
		{"exact-240-seconds", "2026-10-22T01:04:00Z", false, true},
		{"one-nanosecond-over", "2026-10-22T01:04:00.000000001Z", false, false},
		{"duration-overflow", "9999-12-31T23:59:59Z", false, false},
		{"legacy-hour-preserved", "2026-10-22T02:00:00Z", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, f := qualificationFixture(t)
			if tc.legacy {
				tr, f = qualificationLegacyFixture(t)
			}
			for _, leaf := range []string{"host-boundary-receipt.json", "native-enforcement-receipt.json"} {
				var r qualificationHostReceipt
				json.Unmarshal(f[leaf], &r)
				r.Window.Ended = tc.end
				f[leaf] = qualificationJSON(r)
			}
			tr = qualificationRebind(tr, f)
			var err error
			if tc.legacy {
				_, err = validateLegacyDeploymentQualificationV1(tr, f)
			} else {
				_, err = validateDeploymentQualification(tr, f)
			}
			if (err == nil) != tc.want {
				t.Fatalf("accepted=%v want=%v err=%v", err == nil, tc.want, err)
			}
		})
	}
}

func TestDeploymentQualificationV2RejectsCoherentCIDOnlySwap(t *testing.T) {
	tr, f := qualificationFixture(t)
	var n qualificationNativeReceipt
	json.Unmarshal(f["native-enforcement-receipt.json"], &n)
	cid := strings.Repeat("d", 64)
	for j := range n.Checks {
		switch n.Checks[j].ID {
		case "loaded-identity":
			var v qualificationLoaded
			json.Unmarshal(n.Checks[j].Observation.Data, &v)
			v.Worker.ContainerID = cid
			n.Checks[j] = qualificationCheckValue("loaded-identity", v)
		case "worker-setters":
			v, err := decodeQualificationSettersV2(n.Checks[j].Observation.Data)
			if err != nil {
				t.Fatal(err)
			}
			for i := range v.ABIs {
				v.ABIs[i].Worker.ContainerID = cid
			}
			n.Checks[j] = qualificationCheckValue("worker-setters", v)
		}
	}
	f["native-enforcement-receipt.json"] = qualificationJSON(n)
	tr = qualificationRebind(tr, f)
	if _, err := validateDeploymentQualification(tr, f); err == nil {
		t.Fatal("CID-only swap accepted with old actual host cgroup")
	}
}
