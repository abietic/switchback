package capacitysnapshot

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strconv"
	"strings"
)

// All seams are private and read-only. Native must establish complete process,
// FD and listener facts; no API factory or SDK call precedes that proof.
type qualificationEngineDeploymentReaders struct {
	read    func(context.Context, string, int64, uint64) ([]byte, error)
	native  func(context.Context, qualificationEngineReceiptV2) (qualificationEngineNative, error)
	info    func(context.Context) (qualificationEngineLiveInfo, error)
	network func(context.Context, int, string) (qualificationEngineNetworkIdentity, error)
}
type qualificationEngineLiveInfo struct {
	ID, Version, Root, Driver, CgroupDriver, CgroupVersion string
	Snapshotter                                            bool
	CPUs, Memory                                           int64
}
type qualificationEngineOwner struct {
	PID, Start, FD uint64
	Exe, Cgroup    string
}
type qualificationEngineListener struct {
	Family, Path string
	Address      string
	Port         uint64
	Inode        uint64
	Listener     bool
	Owners       []qualificationEngineOwner
}
type qualificationEngineNative struct {
	Boot              string
	Filesystem        qualificationEngineFilesystem
	RuntimeParent     qualificationEngineRuntimeParent
	Daemons           []qualificationEngineDaemon
	Sockets           []qualificationEngineSocketIdentity
	SocketParents     []qualificationEngineSocketParent
	Listeners         []qualificationEngineListener
	Complete          bool
	Attempts          []qualificationEngineAttempt `json:"-"`
	socketParentLinks []uint64
}
type qualificationEngineKitManifest struct {
	Schema               string                     `json:"schema"`
	RuntimeQualification bool                       `json:"runtime_qualification"`
	Toolchain            string                     `json:"toolchain"`
	SourceRoot           string                     `json:"source_input_root"`
	TemplatesRoot        string                     `json:"qualification_templates_root"`
	Files                []json.RawMessage          `json:"files"`
	Unresolved           map[string]json.RawMessage `json:"unresolved_runtime"`
}
type qualificationEngineBundleManifest struct {
	Schema               string                    `json:"schema"`
	KitDigest            string                    `json:"kit_manifest_sha256"`
	SourceRoot           string                    `json:"source_root"`
	TemplatesRoot        string                    `json:"templates_root"`
	Source               []qualificationSourceFile `json:"source_records"`
	Files                []qualificationSourceFile `json:"files"`
	RuntimeQualification bool                      `json:"runtime_qualification"`
	CapacityReady        bool                      `json:"capacity_ready"`
	ExecutionEnabled     bool                      `json:"execution_enabled"`
	PaidConsumed         bool                      `json:"paid_consumed"`
}

func qualificationEngineSourceRecords(records []qualificationSourceFile) error {
	if len(records) == 0 || len(records) > 4096 {
		return errQualification
	}
	previous := ""
	for _, r := range records {
		if r.Path <= previous || len(r.Path) > 512 || path.Clean(r.Path) != r.Path || strings.HasPrefix(r.Path, "/") || r.Path == ".." || strings.HasPrefix(r.Path, "../") || strings.Contains(r.Path, "/../") || strings.ContainsRune(r.Path, 0) || r.Type != "regular" || r.Mode > 0777 || r.Mode&0022 != 0 || r.Bytes == 0 || !digestPattern.MatchString(r.Digest) {
			return errQualification
		}
		previous = r.Path
	}
	return nil
}
func qualificationEngineBind(ctx context.Context, k qualificationKit, r qualificationEngineReceiptV2, read func(context.Context, string, int64, uint64) ([]byte, error)) error {
	if k.Schema != qualificationMaterialSchema || k.Toolchain != "go1.26.4" || (k.Architecture != "amd64" && k.Architecture != "arm64") || qualificationEngineSourceRecords(k.Source) != nil || qualificationEngineSourceRecords(k.Templates) != nil {
		return errQualification
	}
	if Digest(qualificationEngineRecordBytes(k.Source)) != r.SourceRoot || Digest(qualificationEngineRecordBytes(k.Templates)) != r.TemplatesRoot {
		return fmt.Errorf("Engine SourceRoot/TemplatesRoot differs")
	}
	raw, e := read(ctx, qualificationKitRoot+"/manifest.json", 1<<20, 0)
	if e != nil || Digest(raw) != r.KitDigest {
		return fmt.Errorf("Engine kit manifest differs")
	}
	var m qualificationEngineKitManifest
	if qualificationEngineDecode(raw, &m) != nil || m.Schema != "switchback.qualification.offline_kit.v1" || m.RuntimeQualification || m.Toolchain != k.Toolchain || m.SourceRoot != r.SourceRoot || m.TemplatesRoot != r.TemplatesRoot || len(m.Files) == 0 || len(m.Files) > 8192 || len(m.Unresolved) != 13 {
		return errQualification
	}
	for _, name := range []string{"image_ids", "container_ids", "guest_boot", "host_boot", "public_identity", "host_volume_readback", "engine_identity", "native_receipt", "host_receipt", "trust", "manifest", "journal", "pg_preparation"} {
		if string(m.Unresolved[name]) != "null" {
			return errQualification
		}
	}
	indexed := map[string]qualificationSourceFile{}
	for _, record := range m.Files {
		var object map[string]json.RawMessage
		if json.Unmarshal(record, &object) != nil {
			return errQualification
		}
		for name := range object {
			switch name {
			case "path", "type", "mode", "bytes", "digest", "role", "goos", "goarch", "elf_class", "elf_machine":
			default:
				return errQualification
			}
		}
		base := map[string]json.RawMessage{}
		for _, name := range []string{"path", "type", "mode", "bytes", "digest"} {
			base[name] = object[name]
		}
		encoded, e := json.Marshal(base)
		var f qualificationSourceFile
		if e != nil || qualificationEngineDecode(encoded, &f) != nil || f.Type != "regular" || f.Bytes == 0 || f.Mode > 0777 || f.Mode&0022 != 0 || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "..") || !digestPattern.MatchString(f.Digest) {
			return errQualification
		}
		if _, ok := indexed[f.Path]; ok {
			return errQualification
		}
		indexed[f.Path] = f
	}
	sources := map[string]qualificationSourceFile{}
	for _, records := range [][]qualificationSourceFile{k.Source, k.Templates} {
		for _, f := range records {
			if _, ok := sources[f.Path]; ok {
				return errQualification
			}
			sources[f.Path] = f
			copied := indexed["source/"+f.Path]
			copied.Path = f.Path
			if copied != f {
				return fmt.Errorf("kit copied Source record differs: %s", f.Path)
			}
		}
	}
	raw, e = read(ctx, qualificationKitRoot+"/kit-inputs-"+k.Architecture+".json", 1<<20, 0)
	if e != nil {
		return e
	}
	var canonical qualificationKit
	if qualificationEngineDecode(raw, &canonical) != nil || !reflect.DeepEqual(canonical, k) {
		return fmt.Errorf("selected kit inputs differ from canonical architecture")
	}
	selected := indexed["kit-inputs-"+k.Architecture+".json"]
	if Digest(raw) != selected.Digest || uint64(len(raw)) != selected.Bytes {
		return errQualification
	}
	raw, e = read(ctx, qualificationEngineBundleRoot+"/manifest.json", 1<<20, 0600)
	if e != nil || Digest(raw) != r.BundleDigest {
		return fmt.Errorf("Engine bundle manifest differs")
	}
	var b qualificationEngineBundleManifest
	if qualificationEngineDecode(raw, &b) != nil || b.Schema != "switchback.engine.offline_bundle.v1" || b.KitDigest != r.KitDigest || b.SourceRoot != r.SourceRoot || b.TemplatesRoot != r.TemplatesRoot || b.RuntimeQualification || b.CapacityReady || b.ExecutionEnabled || b.PaidConsumed || len(b.Source) != len(b.Files) || qualificationEngineSourceRecords(b.Source) != nil {
		return errQualification
	}
	expected := []qualificationSourceFile{}
	allowedEngine := map[string]bool{"switchback-m1-containerd.service": true, "switchback-m1-engine.service": true, "containerd.toml": true, "daemon.json": true, "fd_lifecycle.py": true, "test_fd_lifecycle.py": true, "testdata/fd-lifecycle-corpus.json": true, "contract.py": true, "bundle.py": true, "guest.py": true, "prepare.py": true, "store.py": true, "binaries.py": true, "README.md": true, "test_contract.py": true, "test_prepare.py": true, "test_guest.py": true, "test_store.py": true, "testdata/receipt-v1.json": true, "testdata/receipt-v2.json": true, "test_listener_v2.py": true, "testdata/listener-v2-corpus.json": true, "testdata/listeners-kit21-v1.json": true, "testdata/listener-v2-provenance.json": true, "test_default_bridge_store.py": true, "testdata/default-bridge-records.json": true, "test_bridge_ipam.py": true, "testdata/default-bridge-ipam-records.json": true}
	for _, s := range k.Source {
		if strings.HasPrefix(s.Path, qualificationEngineSourcePrefix) && !allowedEngine[strings.TrimPrefix(s.Path, qualificationEngineSourcePrefix)] {
			return fmt.Errorf("unknown Engine source leaf")
		}
		if strings.HasPrefix(s.Path, qualificationEngineSourcePrefix) || s.Path == "deploy/experience/primary-pins.json" {
			expected = append(expected, s)
		}
	}
	for _, name := range []string{"fd_lifecycle.py", "test_fd_lifecycle.py", "testdata/fd-lifecycle-corpus.json", "testdata/receipt-v2.json", "test_listener_v2.py", "testdata/listener-v2-corpus.json", "testdata/listeners-kit21-v1.json", "testdata/listener-v2-provenance.json"} {
		if _, ok := sources[qualificationEngineSourcePrefix+name]; !ok {
			return fmt.Errorf("required Engine source leaf missing: %s", name)
		}
	}
	if !reflect.DeepEqual(expected, b.Source) {
		return fmt.Errorf("Engine bundle source membership differs")
	}
	for i, s := range b.Source {
		copied := b.Files[i]
		copied.Path = strings.TrimPrefix(copied.Path, "source/")
		if b.Files[i].Path != "source/"+s.Path || copied != s {
			return errQualification
		}
		for _, base := range []string{qualificationKitRoot, qualificationEngineBundleRoot} {
			data, e := read(ctx, base+"/source/"+s.Path, int64(s.Bytes), s.Mode)
			if e != nil || uint64(len(data)) != s.Bytes || Digest(data) != s.Digest {
				return fmt.Errorf("current Engine source bytes differ: %s", s.Path)
			}
		}
	}
	for _, f := range r.Installed {
		source, ok := sources[f.Source]
		if !ok || source.Digest != f.Digest || source.Bytes != f.Bytes {
			return fmt.Errorf("installed Engine file source binding differs: %s", f.Source)
		}
		data, e := read(ctx, f.Target, 16384, f.Mode)
		if e != nil || uint64(len(data)) != source.Bytes || Digest(data) != source.Digest {
			return fmt.Errorf("installed Engine bytes differ: %s", f.Target)
		}
	}
	return qualificationContext(ctx)
}
func qualificationEngineCurrent(r qualificationEngineReceiptV2, n qualificationEngineNative) error {
	if _, err := qualificationEngineReceiptCheck(r.qualificationEngineReceipt, 2, r.SocketParents); err != nil {
		return err
	}
	if !n.Complete || n.Boot != r.Boot || n.Filesystem != r.Filesystem || n.RuntimeParent != r.RuntimeParent || !reflect.DeepEqual(n.Daemons, r.Daemons) || !reflect.DeepEqual(n.Sockets, r.Sockets) || !reflect.DeepEqual(n.SocketParents, r.SocketParents) {
		return fmt.Errorf("Engine native identity differs")
	}
	return qualificationEngineListenerOwnership(r.Daemons, r.Sockets, n.Listeners)
}

func qualificationEngineListenerOwnership(daemons []qualificationEngineDaemon, sockets []qualificationEngineSocketIdentity, listeners []qualificationEngineListener) error {
	return qualificationEngineListenerPolicy(daemons, sockets, listeners, false)
}
func qualificationEngineListenerPolicy(daemons []qualificationEngineDaemon, sockets []qualificationEngineSocketIdentity, listeners []qualificationEngineListener, partial bool) error {
	byInode := map[uint64]qualificationEngineSocketIdentity{}
	byPID := map[uint64]qualificationEngineDaemon{}
	found := map[uint64]bool{}
	all := map[uint64]bool{}
	fds := map[[3]uint64]uint64{}
	for _, s := range sockets {
		byInode[s.KernelInode] = s
	}
	for _, d := range daemons {
		byPID[d.PID] = d
	}
	if len(listeners) > 24576 {
		return errQualification
	}
	for _, l := range listeners {
		if l.Inode == 0 || l.Inode > 1<<63-1 || all[l.Inode] || !partial && len(l.Owners) == 0 || len(l.Owners) > 64 {
			return errQualification
		}
		all[l.Inode] = true
		related := false
		seen := map[[2]uint64]bool{}
		for _, o := range l.Owners {
			if o.PID == 0 || o.PID > 1<<31-1 || o.Start > 1<<63-1 || o.Start == 0 && o.PID != 1 || o.FD > 1<<20-1 || !qualificationEngineOwnerPath(o.Exe) || !qualificationEngineOwnerPath(o.Cgroup) {
				return errQualification
			}
			key := [3]uint64{o.PID, o.Start, o.FD}
			if old, ok := fds[key]; ok && old != l.Inode {
				return errQualification
			}
			fds[key] = l.Inode
			fd := [2]uint64{o.PID, o.FD}
			if seen[fd] {
				return errQualification
			}
			seen[fd] = true
			d, known := byPID[o.PID]
			base := path.Base(o.Exe)
			daemonish := base == "dockerd" || base == "containerd" || strings.HasPrefix(base, "containerd-shim")
			if known || daemonish {
				if !known || d.Start != o.Start || d.Exe != o.Exe || d.Cgroup != o.Cgroup {
					return errQualification
				}
				related = true
			}
		}
		if l.Family != "unix" {
			if l.Family != "tcp" && l.Family != "tcp6" || related || !qualificationEngineText(l.Address, 128, false) || l.Port == 0 || l.Port > 65535 {
				return errQualification
			}
			continue
		}
		if !qualificationEngineText(l.Path, 512, true) {
			return errQualification
		}
		for _, protected := range sockets {
			if l.Listener && l.Path == protected.Path && l.Inode != protected.KernelInode {
				return errQualification
			}
		}
		sensitive := strings.Contains(l.Path, "docker") || strings.Contains(l.Path, "containerd") || strings.Contains(l.Path, "switchback-engine")
		s, required := byInode[l.Inode]
		if related && !l.Listener {
			allowed := l.Path == ""
			for _, s := range sockets {
				allowed = allowed || l.Path == s.Path
			}
			if !allowed {
				return errQualification
			}
			for _, o := range l.Owners {
				if _, ok := byPID[o.PID]; !ok {
					return errQualification
				}
			}
			continue
		}
		if related || sensitive || required {
			if !l.Listener || !required || s.Path != l.Path {
				return errQualification
			}
			for _, o := range l.Owners {
				if o.PID != s.OwnerPID || o.Start != s.OwnerStart {
					return errQualification
				}
			}
			found[l.Inode] = true
		}
	}
	if !partial && len(found) != len(sockets) {
		return fmt.Errorf("required Engine listener ownership missing")
	}
	return nil
}
func qualificationVerifyEngineDeploymentWith(ctx context.Context, k qualificationKit, io qualificationEngineDeploymentReaders) error {
	if qualificationContext(ctx) != nil || io.read == nil || io.native == nil || io.info == nil || io.network == nil {
		return errQualification
	}
	raw, e := io.read(ctx, qualificationEngineReceiptPath, 1<<20, 0600)
	if e != nil {
		return fmt.Errorf("Engine deployment receipt unavailable: %w", e)
	}
	r, e := qualificationEngineReceiptV2Decode(raw)
	if e != nil {
		return e
	}
	if e = qualificationEngineBind(ctx, k, r, io.read); e != nil {
		return e
	}
	gate := func() error {
		if e := qualificationContext(ctx); e != nil {
			return e
		}
		n, e := io.native(ctx, r)
		if e != nil {
			return e
		}
		return qualificationEngineCurrent(r, n)
	}
	if e = gate(); e != nil {
		return e
	}
	info, e := io.info(ctx)
	if e != nil {
		return e
	}
	if info.ID != r.Engine.ID || info.Version != r.Engine.Version || info.Root != r.Engine.DataRoot || info.Driver != "overlay2" || info.Snapshotter || info.CgroupDriver != "systemd" || info.CgroupVersion != "2" || info.CPUs != 4 || info.Memory <= 0 {
		return fmt.Errorf("current Engine API identity differs")
	}
	for i, want := range r.Networks {
		if e = gate(); e != nil {
			return e
		}
		actual, e := io.network(ctx, i, want.ID)
		if e != nil {
			return e
		}
		if qualificationEngineNetworkCheck(actual, i) != nil || !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("current Engine network differs")
		}
	}
	if e = gate(); e != nil {
		return e
	}
	if e = qualificationEngineBind(ctx, k, r, io.read); e != nil {
		return e
	}
	after, e := io.read(ctx, qualificationEngineReceiptPath, 1<<20, 0600)
	if e != nil || !reflect.DeepEqual(after, raw) {
		return fmt.Errorf("Engine deployment receipt changed")
	}
	return qualificationContext(ctx)
}

// Full table parsers retain unnamed/nonlistener Unix sockets. TCP rows are all
// parsed; only listening rows carry listener-owner policy. No socket is opened.
func qualificationEngineParseListeners(raw []byte, family string) ([]qualificationEngineListener, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || raw[len(raw)-1] != '\n' {
		return nil, errQualification
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) > 8193 {
		return nil, errQualification
	}
	if family == "unix" {
		if lines[0] != "Num       RefCount Protocol Flags    Type St Inode Path" {
			return nil, errQualification
		}
	} else if family == "tcp" || family == "tcp6" {
		if !strings.Contains(lines[0], "local_address") || !strings.Contains(lines[0], "inode") {
			return nil, errQualification
		}
	} else {
		return nil, errQualification
	}
	out := []qualificationEngineListener{}
	seen := map[uint64]bool{}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if family == "unix" {
			if len(f) < 7 || len(f) > 8 {
				return nil, errQualification
			}
			inode, e := strconv.ParseUint(f[6], 10, 63)
			flags, fe := strconv.ParseUint(f[3], 16, 32)
			if e != nil || fe != nil || inode == 0 || seen[inode] {
				return nil, errQualification
			}
			seen[inode] = true
			p := ""
			if len(f) == 8 {
				p = f[7]
			}
			out = append(out, qualificationEngineListener{Family: family, Path: p, Inode: inode, Listener: flags&0x10000 != 0, Owners: []qualificationEngineOwner{}})
		} else {
			if len(f) < 10 {
				return nil, errQualification
			}
			inode, e := strconv.ParseUint(f[9], 10, 63)
			local := strings.Split(f[1], ":")
			remote := strings.Split(f[2], ":")
			width := 8
			if family == "tcp6" {
				width = 32
			}
			if e != nil || len(local) != 2 || len(remote) != 2 || len(local[0]) != width || len(remote[0]) != width {
				return nil, errQualification
			}
			for _, endpoint := range [][]string{local, remote} {
				if _, e := hex.DecodeString(endpoint[0]); e != nil {
					return nil, e
				}
				if len(endpoint[1]) != 4 {
					return nil, errQualification
				}
				if _, e := strconv.ParseUint(endpoint[1], 16, 16); e != nil {
					return nil, e
				}
			}
			state, e := strconv.ParseUint(f[3], 16, 8)
			if e != nil {
				return nil, e
			}
			if state != 0x0a {
				continue
			}
			port, e := strconv.ParseUint(local[1], 16, 16)
			if e != nil || inode == 0 || port == 0 || seen[inode] {
				return nil, errQualification
			}
			seen[inode] = true
			out = append(out, qualificationEngineListener{Family: family, Address: local[0], Port: port, Inode: inode, Listener: true, Owners: []qualificationEngineOwner{}})
		}
	}
	return out, nil
}

func qualificationEngineCheckLeaf(fileMode, nlink, uid, gid uint64, size int64, mode uint64, limit int64) error {
	if fileMode&0170000 != 0100000 || nlink != 1 || uid != 0 || gid != 0 || fileMode&0022 != 0 || size <= 0 || size > limit || mode != 0 && fileMode&07777 != mode {
		return errQualification
	}
	return nil
}
func qualificationEngineArgv(index int) []string {
	if index == 0 {
		return []string{"/usr/bin/containerd", "--config", "/etc/switchback/m1-engine/containerd.toml"}
	}
	return []string{"/usr/bin/dockerd", "-H", "unix:///run/switchback-engine/docker.sock", "--group=root", "--containerd=/run/switchback-engine/m1-resident/containerd.sock", "--pidfile=/run/switchback-engine/m1-resident/docker.pid", "--exec-root=/run/switchback-engine/m1-resident/docker", "--config-file=/etc/switchback/m1-engine/daemon.json"}
}
func qualificationEngineUnitCheck(m map[string]string, index int) error {
	expected := map[string]string{"Id": qualificationEngineUnits[index], "ActiveState": "active", "SubState": "running", "Result": "success", "ControlGroup": "/switchback.slice/switchback-core.slice/" + qualificationEngineUnits[index], "FragmentPath": "/etc/systemd/system/" + qualificationEngineUnits[index], "DropInPaths": "", "NeedDaemonReload": "no", "Type": "notify", "Restart": "no", "KillMode": "control-group", "TimeoutStartUSec": "30s", "TimeoutStopUSec": "10s", "Slice": "switchback-core.slice", "User": "root", "Group": "root", "UMask": "0077", "Delegate": "yes", "Environment": "", "EnvironmentFiles": ""}
	if len(m) != len(expected)+1 {
		return errQualification
	}
	for key, value := range expected {
		if m[key] != value {
			return fmt.Errorf("Engine effective unit property differs: %s", key)
		}
	}
	pid, e := strconv.ParseUint(m["MainPID"], 10, 31)
	if e != nil || pid == 0 {
		return errQualification
	}
	return nil
}
