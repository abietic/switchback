package capacitysnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func qualificationEngineFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../../deploy/experience/engine/testdata/receipt-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestQualificationEngineDeploymentReceiptSharedContract(t *testing.T) {
	raw := qualificationEngineFixture(t)
	receipt, err := qualificationEngineReceiptDecode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "engine_ready" || receipt.ElapsedMS != 1200 {
		t.Fatalf("unexpected shared fixture: %+v", receipt)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(map[string]any){
		"unknown":             func(v map[string]any) { v["unexpected"] = false },
		"missing flag":        func(v map[string]any) { delete(v, "capacity_ready") },
		"null flag":           func(v map[string]any) { v["paid_consumed"] = nil },
		"true authority":      func(v map[string]any) { v["execution_enabled"] = true },
		"null zero uid":       func(v map[string]any) { v["runtime_parent"].(map[string]any)["uid"] = nil },
		"null required array": func(v map[string]any) { v["unknown_effects"] = nil },
		"null options":        func(v map[string]any) { v["networks"].([]any)[0].(map[string]any)["options"] = nil },
		"schema":              func(v map[string]any) { v["schema"] = "switchback.engine.deployment_receipt.v2" },
		"partial":             func(v map[string]any) { v["status"] = "unknown" },
		"excess elapsed":      func(v map[string]any) { v["elapsed_ms"] = 120001 },
		"daemon boot": func(v map[string]any) {
			v["daemons"].([]any)[0].(map[string]any)["boot_id"] = "00000000-0000-4000-8000-000000000009"
		},
		"socket parent": func(v map[string]any) { v["sockets"].([]any)[1].(map[string]any)["parent_inode"] = 999 },
		"network labels": func(v map[string]any) {
			v["networks"].([]any)[0].(map[string]any)["labels"].(map[string]any)["foreign"] = "yes"
		},
		"network overlap": func(v map[string]any) {
			v["networks"].([]any)[1].(map[string]any)["ipam"] = v["networks"].([]any)[0].(map[string]any)["ipam"]
		},
		"step exit": func(v map[string]any) { v["steps"].([]any)[0].(map[string]any)["exit_code"] = nil },
		"effects":   func(v map[string]any) { v["known_effects"] = []any{} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			var v map[string]any
			json.Unmarshal(raw, &v)
			change(v)
			b, _ := json.Marshal(v)
			if _, err := qualificationEngineReceiptDecode(b); err == nil {
				t.Fatal("accepted invalid shared DTO")
			}
		})
	}
	for _, bad := range [][]byte{bytes.Replace(raw, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1), bytes.Replace(raw, []byte(`"elapsed_ms": 1200`), []byte(`"elapsed_ms": NaN`), 1), bytes.Replace(raw, []byte(`"uid": 0`), []byte(`"uid":0,"uid":0`), 1)} {
		if _, err := qualificationEngineReceiptDecode(bad); err == nil {
			t.Fatal("accepted duplicate/nonfinite JSON")
		}
	}
}

func TestQualificationEngineDeploymentPreconnectionAndDrift(t *testing.T) {
	for _, fault := range []string{"receipt missing", "receipt malformed", "source", "installed", "boot", "filesystem", "pid", "start", "exe", "cgroup", "socket", "fd owner", "listener", "network", "final identity"} {
		t.Run(fault, func(t *testing.T) {
			fixture := newQualificationEngineReadbackFixture(t)
			fixture.fault = fault
			err := qualificationVerifyEngineDeploymentWith(context.Background(), fixture.kit, fixture.readers())
			if err == nil {
				t.Fatal("accepted drift: " + fault)
			}
			if fault != "network" && fault != "final identity" && fixture.apiCalls != 0 {
				t.Fatalf("API before native proof: %d calls", fixture.apiCalls)
			}
		})
	}
	f := newQualificationEngineReadbackFixture(t)
	if err := qualificationVerifyEngineDeploymentWith(context.Background(), f.kit, f.readers()); err != nil {
		t.Fatal(err)
	}
	if f.apiCalls != 3 || !reflect.DeepEqual(f.events, []string{"native", "info", "native", "network", "native", "network", "native"}) {
		t.Fatalf("wrong preconnection ordering: %v calls=%d", f.events, f.apiCalls)
	}
}
func TestQualificationEnginePythonSourceRecordEncoding(t *testing.T) {
	records := []qualificationSourceFile{{Path: "deploy/experience/engine/a<\u2028&.json", Type: "regular", Mode: 420, Bytes: 1, Digest: "sha256:" + strings.Repeat("a", 64)}}
	expected := `[{"path":"deploy/experience/engine/a<` + "\u2028" + `&.json","type":"regular","mode":420,"bytes":1,"digest":"sha256:` + strings.Repeat("a", 64) + `"}]`
	if string(qualificationEngineRecordBytes(records)) != expected {
		t.Fatalf("Python compact ordered source encoding differs: %s", qualificationEngineRecordBytes(records))
	}
}

type qualificationEngineReadbackFixture struct {
	kit                   qualificationKit
	receipt               qualificationEngineReceiptV2
	files                 map[string][]byte
	fault                 string
	apiCalls, nativeCalls int
	events                []string
}

func newQualificationEngineReadbackFixture(t *testing.T) *qualificationEngineReadbackFixture {
	t.Helper()
	raw := qualificationEngineV2Fixture(t)
	var receipt qualificationEngineReceiptV2
	if e := json.Unmarshal(raw, &receipt); e != nil {
		t.Fatal(e)
	}
	f := &qualificationEngineReadbackFixture{receipt: receipt, files: map[string][]byte{}}
	names, e := os.ReadDir("../../../../deploy/experience/engine")
	if e != nil {
		t.Fatal(e)
	}
	paths := []string{"deploy/experience/primary-pins.json", "deploy/experience/engine/testdata/fd-lifecycle-corpus.json", "deploy/experience/engine/testdata/receipt-v1.json", "deploy/experience/engine/testdata/receipt-v2.json", "deploy/experience/engine/testdata/listener-v2-corpus.json", "deploy/experience/engine/testdata/listeners-kit21-v1.json", "deploy/experience/engine/testdata/listener-v2-provenance.json", "deploy/experience/engine/testdata/default-bridge-ipam-records.json"}
	for _, n := range names {
		if !n.IsDir() && n.Name() != "__pycache__" {
			paths = append(paths, qualificationEngineSourcePrefix+n.Name())
		}
	}
	sort.Strings(paths)
	f.kit = qualificationKit{Schema: qualificationMaterialSchema, Architecture: "arm64", Toolchain: "go1.26.4", Artifacts: []qualificationKitArtifact{{Role: "backend", Path: qualificationKitRoot + "/bin/backend", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	for _, name := range paths {
		b, e := os.ReadFile("../../../../" + name)
		if e != nil {
			t.Fatal(e)
		}
		s := qualificationSourceFile{Path: name, Type: "regular", Mode: 0644, Bytes: uint64(len(b)), Digest: Digest(b)}
		f.kit.Source = append(f.kit.Source, s)
		for _, base := range []string{qualificationKitRoot, qualificationEngineBundleRoot} {
			f.files[base+"/source/"+name] = b
		}
	}
	b, e := os.ReadFile("../../../../deploy/experience/qualification/README.md")
	if e != nil {
		t.Fatal(e)
	}
	f.kit.Templates = []qualificationSourceFile{{Path: "deploy/experience/qualification/README.md", Type: "regular", Mode: 0644, Bytes: uint64(len(b)), Digest: Digest(b)}}
	f.receipt.SourceRoot = Digest(qualificationEngineRecordBytes(f.kit.Source))
	f.receipt.TemplatesRoot = Digest(qualificationEngineRecordBytes(f.kit.Templates))
	records := []qualificationSourceFile{}
	for _, r := range append(append([]qualificationSourceFile{}, f.kit.Source...), f.kit.Templates...) {
		r.Path = "source/" + r.Path
		records = append(records, r)
	}
	inputs, _ := json.Marshal(f.kit)
	f.files[qualificationKitRoot+"/kit-inputs-arm64.json"] = inputs
	records = append(records, qualificationSourceFile{Path: "kit-inputs-arm64.json", Type: "regular", Mode: 0644, Bytes: uint64(len(inputs)), Digest: Digest(inputs)})
	unresolved := map[string]json.RawMessage{}
	for _, name := range []string{"image_ids", "container_ids", "guest_boot", "host_boot", "public_identity", "host_volume_readback", "engine_identity", "native_receipt", "host_receipt", "trust", "manifest", "journal", "pg_preparation"} {
		unresolved[name] = json.RawMessage("null")
	}
	recordRaw := []json.RawMessage{}
	for _, r := range records {
		b, _ := json.Marshal(r)
		recordRaw = append(recordRaw, b)
	}
	manifest := qualificationEngineKitManifest{Schema: "switchback.qualification.offline_kit.v1", Toolchain: "go1.26.4", SourceRoot: f.receipt.SourceRoot, TemplatesRoot: f.receipt.TemplatesRoot, Files: recordRaw, Unresolved: unresolved}
	raw, _ = json.Marshal(manifest)
	f.files[qualificationKitRoot+"/manifest.json"] = raw
	f.receipt.KitDigest = Digest(raw)
	bundle := qualificationEngineBundleManifest{Schema: "switchback.engine.offline_bundle.v1", KitDigest: f.receipt.KitDigest, SourceRoot: f.receipt.SourceRoot, TemplatesRoot: f.receipt.TemplatesRoot, Source: f.kit.Source, Files: []qualificationSourceFile{}}
	for _, r := range f.kit.Source {
		r.Path = "source/" + r.Path
		bundle.Files = append(bundle.Files, r)
	}
	raw, _ = json.Marshal(bundle)
	f.files[qualificationEngineBundleRoot+"/manifest.json"] = raw
	f.receipt.BundleDigest = Digest(raw)
	for _, r := range f.receipt.Installed {
		b, e := os.ReadFile("../../../../" + r.Source)
		if e != nil {
			t.Fatal(e)
		}
		f.files[r.Target] = b
	}
	raw, _ = json.Marshal(f.receipt)
	f.files[qualificationEngineReceiptPath] = raw
	return f
}
func (f *qualificationEngineReadbackFixture) readers() qualificationEngineDeploymentReaders {
	return qualificationEngineDeploymentReaders{
		read: func(ctx context.Context, p string, limit int64, mode uint64) ([]byte, error) {
			if f.fault == "receipt missing" && p == qualificationEngineReceiptPath {
				return nil, os.ErrNotExist
			}
			if f.fault == "receipt malformed" && p == qualificationEngineReceiptPath {
				return []byte(`{}`), nil
			}
			b, ok := f.files[p]
			if !ok {
				return nil, fmt.Errorf("fixture missing path %s", p)
			}
			if f.fault == "source" && strings.Contains(p, "/source/") {
				return []byte("changed source"), nil
			}
			if f.fault == "installed" && strings.HasPrefix(p, "/etc/") {
				return []byte("changed installed"), nil
			}
			return b, nil
		},
		native: func(ctx context.Context, r qualificationEngineReceiptV2) (qualificationEngineNative, error) {
			f.nativeCalls++
			f.events = append(f.events, "native")
			n := qualificationEngineNative{Boot: r.Boot, Filesystem: r.Filesystem, RuntimeParent: r.RuntimeParent, Daemons: append([]qualificationEngineDaemon{}, r.Daemons...), Sockets: append([]qualificationEngineSocketIdentity{}, r.Sockets...), SocketParents: append([]qualificationEngineSocketParent{}, r.SocketParents...), Complete: true}
			for i, s := range r.Sockets {
				d := r.Daemons[0]
				if i >= 2 {
					d = r.Daemons[1]
				}
				n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "unix", Path: s.Path, Inode: s.KernelInode, Listener: true, Owners: []qualificationEngineOwner{{PID: d.PID, Start: d.Start, FD: uint64(i + 3), Exe: d.Exe, Cgroup: d.Cgroup}}})
			}
			switch f.fault {
			case "boot":
				n.Boot = "changed"
			case "filesystem":
				n.Filesystem.RootInode++
			case "pid":
				n.Daemons[0].PID++
			case "start":
				n.Daemons[0].Start++
			case "exe":
				n.Daemons[0].Digest = "changed"
			case "cgroup":
				n.Daemons[0].Cgroup += "/foreign"
			case "socket":
				n.Sockets[0].Inode++
			case "fd owner":
				n.Listeners[0].Owners[0].PID++
			case "listener":
				n.Listeners = append(n.Listeners, qualificationEngineListener{Family: "tcp6", Inode: 999, Listener: true, Owners: n.Listeners[0].Owners})
			case "final identity":
				if f.nativeCalls == 4 {
					n.Boot = "changed"
				}
			}
			return n, nil
		},
		info: func(ctx context.Context) (qualificationEngineLiveInfo, error) {
			f.apiCalls++
			f.events = append(f.events, "info")
			e := f.receipt.Engine
			return qualificationEngineLiveInfo{ID: e.ID, Version: e.Version, Root: e.DataRoot, Driver: e.StorageDriver, CgroupDriver: "systemd", CgroupVersion: "2", CPUs: 4, Memory: 10 << 30}, nil
		},
		network: func(ctx context.Context, index int, id string) (qualificationEngineNetworkIdentity, error) {
			f.apiCalls++
			f.events = append(f.events, "network")
			n := f.receipt.Networks[index]
			if f.fault == "network" {
				n.ID = strings.Repeat("9", 64)
			}
			return n, nil
		},
	}
}

func TestQualificationEngineDeploymentLeafMetadata(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		mode, links, uid, gid uint64
		size                  int64
		valid                 bool
	}{
		{"regular", 0100600, 1, 0, 0, 10, true}, {"symlink", 0120600, 1, 0, 0, 10, false}, {"hardlink", 0100600, 2, 0, 0, 10, false}, {"owner", 0100600, 1, 501, 0, 10, false}, {"group", 0100600, 1, 0, 501, 10, false}, {"mode", 0100644, 1, 0, 0, 10, false}, {"FIFO", 0010600, 1, 0, 0, 10, false}, {"oversize", 0100600, 1, 0, 0, 1001, false}, {"empty", 0100600, 1, 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := qualificationEngineCheckLeaf(tc.mode, tc.links, tc.uid, tc.gid, tc.size, 0600, 1000)
			if (e == nil) != tc.valid {
				t.Fatalf("metadata accepted=%v error=%v", e == nil, e)
			}
		})
	}
	// Source config is0644; installed config must be0600. Comparing their modes
	// directly would reject the legitimate deployment even when bytes match.
	if qualificationEngineCheckLeaf(0100644, 1, 0, 0, 10, 0644, 1000) != nil {
		t.Fatal("source mode rejected")
	}
}
func TestQualificationEngineDeploymentEffectiveUnit(t *testing.T) {
	// Systemd responses are synthetic; no service is queried.
	m := map[string]string{"Id": "switchback-m1-containerd.service", "MainPID": "101", "ActiveState": "active", "SubState": "running", "Result": "success", "ControlGroup": "/switchback.slice/switchback-core.slice/switchback-m1-containerd.service", "FragmentPath": "/etc/systemd/system/switchback-m1-containerd.service", "DropInPaths": "", "NeedDaemonReload": "no", "Type": "notify", "Restart": "no", "KillMode": "control-group", "TimeoutStartUSec": "30s", "TimeoutStopUSec": "10s", "Slice": "switchback-core.slice", "User": "root", "Group": "root", "UMask": "0077", "Delegate": "yes", "Environment": "", "EnvironmentFiles": ""}
	if e := qualificationEngineUnitCheck(m, 0); e != nil {
		t.Fatal(e)
	}
	for key, value := range map[string]string{"MainPID": "0", "ControlGroup": "/foreign", "FragmentPath": "/run/systemd/transient/foreign.service", "DropInPaths": "/etc/systemd/system/switchback-m1-containerd.service.d/override.conf", "NeedDaemonReload": "yes", "Restart": "always", "Environment": "CONTAINERD_ADDRESS=/run/containerd/containerd.sock", "TimeoutStartUSec": "infinity"} {
		t.Run(key, func(t *testing.T) {
			c := map[string]string{}
			for k, v := range m {
				c[k] = v
			}
			c[key] = value
			if qualificationEngineUnitCheck(c, 0) == nil {
				t.Fatal("accepted effective unit drift")
			}
		})
	}
	for i, unit := range qualificationEngineUnits {
		unitRaw, e := os.ReadFile("../../../../deploy/experience/engine/" + unit)
		if e != nil {
			t.Fatal(e)
		}
		args := strings.Join(qualificationEngineArgv(i), " ")
		if !strings.Contains(string(unitRaw), "ExecStart="+args+"\n") {
			t.Fatalf("current unit args differ: %s", unit)
		}
	}
}
func TestQualificationEngineDeploymentListenerTables(t *testing.T) {
	unix := []byte("Num       RefCount Protocol Flags    Type St Inode Path\n0000000000000000: 00000002 00000000 00010000 0001 01 301 /run/switchback-engine/m1-resident/containerd.sock\n0000000000000000: 00000002 00000000 00000000 0001 03 399\n")
	rows, e := qualificationEngineParseListeners(unix, "unix")
	if e != nil || len(rows) != 2 || !rows[0].Listener || rows[1].Listener || rows[1].Path != "" {
		t.Fatalf("Unix parse: %+v %v", rows, e)
	}
	tcp := []byte("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 999 1\n")
	if rows, e = qualificationEngineParseListeners(tcp, "tcp"); e != nil || len(rows) != 1 || rows[0].Inode != 999 {
		t.Fatalf("TCP parse: %+v %v", rows, e)
	}
	for _, bad := range [][]byte{unix[:len(unix)-1], bytes.Replace(unix, []byte(" 399"), []byte(" 301"), 1), bytes.Replace(unix, []byte("00010000"), []byte("not-hex"), 1)} {
		if _, e = qualificationEngineParseListeners(bad, "unix"); e == nil {
			t.Fatal("accepted malformed full Unix table")
		}
	}
}
func TestQualificationEngineDeploymentSourceRootActualBuilder(t *testing.T) {
	command := exec.Command("python3", "-c", `import json,sys;sys.path.insert(0,'../../../../deploy/experience/qualification');import build_candidate as b;s,t=b.inputs();print(json.dumps({'source':s,'templates':t,'source_root':b.digest(b.encode(s)),'templates_root':b.digest(b.encode(t))},separators=(',',':'),ensure_ascii=False))`)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	raw, e := command.Output()
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Source, Templates []qualificationSourceFile
		SourceRoot        string `json:"source_root"`
		TemplatesRoot     string `json:"templates_root"`
	}
	if json.Unmarshal(raw, &v) != nil || len(v.Source) == 0 || len(v.Templates) == 0 {
		t.Fatal("missing actual builder records")
	}
	if Digest(qualificationEngineRecordBytes(v.Source)) != v.SourceRoot || Digest(qualificationEngineRecordBytes(v.Templates)) != v.TemplatesRoot {
		t.Fatal("Go encoding differs from actual Python builder SourceRoot/TemplatesRoot")
	}
}

func TestQualificationEngineDeploymentEveryRequiredProperty(t *testing.T) {
	var original map[string]any
	json.Unmarshal(qualificationEngineFixture(t), &original)
	var paths [][]any
	var walk func(any, []any)
	walk = func(value any, path []any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				p := append(append([]any{}, path...), key)
				paths = append(paths, p)
				walk(child, p)
			}
		case []any:
			for i, child := range v {
				walk(child, append(append([]any{}, path...), i))
			}
		}
	}
	walk(original, nil)
	for _, p := range paths {
		for _, kind := range []string{"missing", "null"} {
			t.Run(fmt.Sprint(p)+kind, func(t *testing.T) {
				var v map[string]any
				json.Unmarshal(qualificationEngineFixture(t), &v)
				var parent any = v
				for _, part := range p[:len(p)-1] {
					switch key := part.(type) {
					case string:
						parent = parent.(map[string]any)[key]
					case int:
						parent = parent.([]any)[key]
					}
				}
				key := p[len(p)-1].(string)
				if kind == "missing" {
					delete(parent.(map[string]any), key)
				} else {
					parent.(map[string]any)[key] = nil
				}
				b, _ := json.Marshal(v)
				if _, e := qualificationEngineReceiptDecode(b); e == nil {
					t.Fatalf("accepted %s required property %v", kind, p)
				}
			})
		}
	}
}

// After the original builder and downstream bundle finish, this test consumes
// their actual complete manifests through the private read seam. It never reads
// a runtime receipt, executes a built binary or reaches a native/Engine reader.
func TestQualificationEngineDeploymentFinalKitBinding(t *testing.T) {
	root := filepath.Clean("../../../..")
	kitCandidates, _ := filepath.Glob(filepath.Join(root, ".scratch/runtime-m1-qualification-delivery-1022/task-4/kit-*/manifest.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(kitCandidates)))
	var kitPath string
	var k qualificationKit
	var manifest qualificationEngineKitManifest
	current, e := os.ReadFile("qualification_engine_deployment_test.go")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range kitCandidates {
		raw, e := os.ReadFile(name)
		if e != nil || qualificationEngineDecode(raw, &manifest) != nil {
			continue
		}
		folder := filepath.Dir(name)
		raw, e = os.ReadFile(filepath.Join(folder, "kit-inputs-arm64.json"))
		if e != nil || qualificationEngineDecode(raw, &k) != nil {
			continue
		}
		for _, s := range k.Source {
			if s.Path == "internal/workerruntime/adapter/capacitysnapshot/qualification_engine_deployment_test.go" && s.Digest == Digest(current) {
				kitPath = folder
				break
			}
		}
		if kitPath != "" {
			break
		}
	}
	if kitPath == "" {
		t.Skip("final current complete kit not built yet")
	}
	kitRaw, e := os.ReadFile(filepath.Join(kitPath, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	kitDigest := Digest(kitRaw)
	var candidates []string
	for _, pattern := range []string{
		".scratch/runtime-m1-resident-engine-delivery-1022/task-3/bundle-*/manifest.json",
		".scratch/runtime-m1-darwin-group-convergence-1023/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-host-recorder-contract-1023/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-quota-backing-device-1023/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-volume-quota-backing-device-1023/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-empty-plugin-directory-1023/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-process-state-identity-1024/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-unit-catalog-output-budget-1024/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-unit-template-catalog-semantics-1024/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-unit-array-property-1025/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-containerd-dump-imports-1025/task-2/bundle-*/manifest.json",
		".scratch/runtime-m1-worker-execution-profile-1025/bundle-*/manifest.json",
	} {
		matches, _ := filepath.Glob(filepath.Join(root, pattern))
		candidates = append(candidates, matches...)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	var bundlePath string
	var bundleRaw []byte
	for _, name := range candidates {
		raw, e := os.ReadFile(name)
		var bundle qualificationEngineBundleManifest
		if e == nil && qualificationEngineDecode(raw, &bundle) == nil && bundle.KitDigest == kitDigest {
			bundlePath = filepath.Dir(name)
			bundleRaw = raw
			break
		}
	}
	if bundlePath == "" {
		t.Skip("exact downstream bundle not built yet")
	}
	r, e := qualificationEngineReceiptV2Decode(qualificationEngineV2Fixture(t))
	if e != nil {
		t.Fatal(e)
	}
	r.KitDigest = kitDigest
	r.BundleDigest = Digest(bundleRaw)
	r.SourceRoot = manifest.SourceRoot
	r.TemplatesRoot = manifest.TemplatesRoot
	installed := map[string]string{}
	for _, i := range r.Installed {
		installed[i.Target] = filepath.Join(kitPath, "source", i.Source)
	}
	read := func(ctx context.Context, name string, limit int64, mode uint64) ([]byte, error) {
		target := ""
		switch {
		case strings.HasPrefix(name, qualificationKitRoot+"/"):
			target = filepath.Join(kitPath, strings.TrimPrefix(name, qualificationKitRoot+"/"))
		case strings.HasPrefix(name, qualificationEngineBundleRoot+"/"):
			target = filepath.Join(bundlePath, strings.TrimPrefix(name, qualificationEngineBundleRoot+"/"))
		default:
			target = installed[name]
		}
		if target == "" {
			return nil, os.ErrNotExist
		}
		raw, e := os.ReadFile(target)
		if int64(len(raw)) > limit {
			return nil, fmt.Errorf("actual kit read bound")
		}
		return raw, e
	}
	if e := qualificationEngineBind(context.Background(), k, r, read); e != nil {
		t.Fatal(e)
	}
	t.Logf("actual full kit %s manifest %s SourceRoot %s TemplatesRoot %s downstream bundle %s digest %s; runtime reads zero", kitPath, kitDigest, r.SourceRoot, r.TemplatesRoot, bundlePath, r.BundleDigest)
}

// Match SharedReceiptParityTests against the exact shared receipt-v1 bytes.
// RawMessage deliberately retains .0; float64 re-marshalling could erase it.
func TestQualificationEngineSharedReceiptParity(t *testing.T) {
	raw := qualificationEngineFixture(t)
	fresh := func() map[string]any {
		var value map[string]any
		if e := json.Unmarshal(raw, &value); e != nil {
			t.Fatal(e)
		}
		return value
	}
	socket := func(v map[string]any) map[string]any { return v["sockets"].([]any)[0].(map[string]any) }
	allocation := func(v map[string]any) map[string]any {
		return v["networks"].([]any)[0].(map[string]any)["ipam"].(map[string]any)["config"].([]any)[0].(map[string]any)
	}
	for _, tc := range []struct{ name, field, number, lexical string }{
		{"socket_owner_pid_float", "owner_pid", "101.0", `"owner_pid":101.0`},
		{"socket_owner_start_tick_float", "owner_start_tick", "1001.0", `"owner_start_tick":1001.0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := fresh()
			socket(v)[tc.field] = json.RawMessage(tc.number)
			b, e := json.Marshal(v)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Contains(b, []byte(tc.lexical)) {
				t.Fatalf("lost lexical float vector: %s", b)
			}
			if _, e = qualificationEngineReceiptDecode(b); e == nil {
				t.Fatal("accepted lexical float owner")
			}
		})
	}
	for _, field := range []string{"owner_pid", "owner_start_tick"} {
		for _, invalid := range []any{json.RawMessage("101.5"), "101", nil, true, false} {
			t.Run(fmt.Sprintf("%s/%v", field, invalid), func(t *testing.T) {
				v := fresh()
				if _, ok := invalid.(bool); ok {
					daemon := v["daemons"].([]any)[0].(map[string]any)
					key := "main_pid"
					if field == "owner_start_tick" {
						key = "start_tick"
					}
					daemon[key] = 1
					for _, item := range v["sockets"].([]any)[:2] {
						item.(map[string]any)[field] = 1
					}
				}
				socket(v)[field] = invalid
				b, _ := json.Marshal(v)
				if _, e := qualificationEngineReceiptDecode(b); e == nil {
					t.Fatal("accepted non-integer socket owner")
				}
			})
		}
	}
	t.Run("ipam_gateway_integer", func(t *testing.T) {
		v := fresh()
		allocation(v)["gateway"] = json.RawMessage("2887647233")
		b, _ := json.Marshal(v)
		if _, e := qualificationEngineReceiptDecode(b); e == nil {
			t.Fatal("accepted numeric gateway")
		}
	})
	for _, field := range []string{"subnet", "gateway"} {
		for _, invalid := range []any{true, nil, []any{}, json.RawMessage("2887647233.0")} {
			t.Run(fmt.Sprintf("ipam/%s/%v", field, invalid), func(t *testing.T) {
				v := fresh()
				allocation(v)[field] = invalid
				b, _ := json.Marshal(v)
				if _, e := qualificationEngineReceiptDecode(b); e == nil {
					t.Fatal("accepted non-string IPAM field")
				}
			})
		}
	}
	for _, tc := range []struct{ field, text string }{
		{"subnet", "172.30.0.0/255.255.0.0"}, {"subnet", "172.30.0.0/0.0.255.255"},
		{"subnet", "172.30.0.0/016"}, {"subnet", "172.30.0.0/+16"},
		{"subnet", "172.030.0.0/16"}, {"gateway", "172.030.0.1"},
		{"gateway", "172.30.0.01"}, {"gateway", " 172.30.0.1"},
		{"gateway", "172.30.0.1 "}, {"gateway", "+172.30.0.1"},
	} {
		t.Run("ipam_canonical_text/"+tc.field+"/"+tc.text, func(t *testing.T) {
			v := fresh()
			allocation(v)[tc.field] = tc.text
			b, _ := json.Marshal(v)
			if _, e := qualificationEngineReceiptDecode(b); e == nil {
				t.Fatal("accepted noncanonical IPv4 allocation text")
			}
		})
	}
	for _, tc := range []struct {
		subnet, gateway string
		valid           bool
	}{
		{"0.0.0.0/24", "0.0.0.1", false}, {"127.0.0.0/24", "127.0.0.1", false}, {"169.254.1.0/24", "169.254.1.1", false}, {"192.0.0.0/29", "192.0.0.1", false},
		{"192.0.2.0/24", "192.0.2.1", false}, {"198.51.100.0/24", "198.51.100.1", false}, {"203.0.113.0/24", "203.0.113.1", false}, {"198.18.0.0/24", "198.18.0.1", false},
		{"240.0.0.0/24", "240.0.0.1", false}, {"255.255.255.0/24", "255.255.255.1", false}, {"224.0.0.0/24", "224.0.0.1", false}, {"100.64.0.0/24", "100.64.0.1", false},
		{"8.8.8.0/24", "8.8.8.1", false}, {"10.0.0.0/7", "10.0.0.1", false}, {"172.0.0.0/8", "172.16.0.1", false}, {"192.0.0.0/8", "192.168.0.1", false},
		{"::1/128", "::1", false}, {"::ffff:172.30.0.0/120", "::ffff:172.30.0.1", false},
		{"10.20.0.0/24", "10.20.0.1", true}, {"172.16.0.0/24", "172.16.0.1", true}, {"192.168.0.0/24", "192.168.0.1", true},
	} {
		t.Run("ipam_reserved_network/"+tc.subnet, func(t *testing.T) {
			v := fresh()
			a := allocation(v)
			a["subnet"], a["gateway"] = tc.subnet, tc.gateway
			b, _ := json.Marshal(v)
			_, e := qualificationEngineReceiptDecode(b)
			if (e == nil) != tc.valid {
				t.Fatalf("IPv4 allocation accepted=%v expected=%v", e == nil, tc.valid)
			}
		})
	}
	if _, e := qualificationEngineReceiptDecode(raw); e != nil {
		t.Fatalf("valid shared fixture rejected: %v", e)
	}
}

// This fixture joins only synthetic bytes in memory. It does not discover Source
// files, read receipts, or reach native/Engine readers.
func newQualificationEngineSourceLeafFixture(t *testing.T, extra ...string) *qualificationEngineReadbackFixture {
	return newQualificationEngineSourceLeafFixtureWith(t, nil, extra...)
}
func newQualificationEngineSourceLeafFixtureWith(t *testing.T, change func([]string) []string, extra ...string) *qualificationEngineReadbackFixture {
	t.Helper()
	marshal := func(value any) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	f := &qualificationEngineReadbackFixture{files: map[string][]byte{}}
	f.kit = qualificationKit{Schema: qualificationMaterialSchema, Architecture: "arm64", Toolchain: "go1.26.4", Artifacts: []qualificationKitArtifact{}}
	paths := []string{"deploy/experience/primary-pins.json"}
	for _, leaf := range append([]string{"fd_lifecycle.py", "test_fd_lifecycle.py", "testdata/fd-lifecycle-corpus.json", "daemon.json", "testdata/receipt-v2.json", "test_listener_v2.py", "testdata/listener-v2-corpus.json", "testdata/listeners-kit21-v1.json", "testdata/listener-v2-provenance.json"}, extra...) {
		paths = append(paths, qualificationEngineSourcePrefix+leaf)
	}
	if change != nil {
		paths = change(paths)
	}
	sort.Strings(paths)
	for _, name := range paths {
		data := []byte("synthetic Source bytes: " + name + "\n")
		record := qualificationSourceFile{Path: name, Type: "regular", Mode: 0644, Bytes: uint64(len(data)), Digest: Digest(data)}
		f.kit.Source = append(f.kit.Source, record)
		for _, base := range []string{qualificationKitRoot, qualificationEngineBundleRoot} {
			f.files[base+"/source/"+name] = data
		}
		if name == qualificationEngineSourcePrefix+"daemon.json" {
			installed := qualificationEngineInstalled{Source: name, Target: "/etc/switchback-source-leaf-test/daemon.json", Mode: 0600, Bytes: record.Bytes, Digest: record.Digest}
			f.receipt.Installed = []qualificationEngineInstalled{installed}
			f.files[installed.Target] = data
		}
	}
	templateData := []byte("synthetic qualification template\n")
	f.kit.Templates = []qualificationSourceFile{{Path: "deploy/experience/qualification/README.md", Type: "regular", Mode: 0644, Bytes: uint64(len(templateData)), Digest: Digest(templateData)}}
	f.files[qualificationKitRoot+"/source/"+f.kit.Templates[0].Path] = templateData
	f.receipt.SourceRoot = Digest(qualificationEngineRecordBytes(f.kit.Source))
	f.receipt.TemplatesRoot = Digest(qualificationEngineRecordBytes(f.kit.Templates))
	inputs := marshal(f.kit)
	f.files[qualificationKitRoot+"/kit-inputs-arm64.json"] = inputs
	manifest := qualificationEngineKitManifest{Schema: "switchback.qualification.offline_kit.v1", Toolchain: f.kit.Toolchain, SourceRoot: f.receipt.SourceRoot, TemplatesRoot: f.receipt.TemplatesRoot, Unresolved: map[string]json.RawMessage{}}
	for _, name := range []string{"image_ids", "container_ids", "guest_boot", "host_boot", "public_identity", "host_volume_readback", "engine_identity", "native_receipt", "host_receipt", "trust", "manifest", "journal", "pg_preparation"} {
		manifest.Unresolved[name] = json.RawMessage("null")
	}
	for _, record := range append(append([]qualificationSourceFile{}, f.kit.Source...), f.kit.Templates...) {
		record.Path = "source/" + record.Path
		manifest.Files = append(manifest.Files, marshal(record))
	}
	manifest.Files = append(manifest.Files, marshal(qualificationSourceFile{Path: "kit-inputs-arm64.json", Type: "regular", Mode: 0644, Bytes: uint64(len(inputs)), Digest: Digest(inputs)}))
	raw := marshal(manifest)
	f.files[qualificationKitRoot+"/manifest.json"] = raw
	f.receipt.KitDigest = Digest(raw)
	bundle := qualificationEngineBundleManifest{Schema: "switchback.engine.offline_bundle.v1", KitDigest: f.receipt.KitDigest, SourceRoot: f.receipt.SourceRoot, TemplatesRoot: f.receipt.TemplatesRoot, Source: append([]qualificationSourceFile{}, f.kit.Source...), Files: []qualificationSourceFile{}}
	for _, record := range f.kit.Source {
		record.Path = "source/" + record.Path
		bundle.Files = append(bundle.Files, record)
	}
	raw = marshal(bundle)
	f.files[qualificationEngineBundleRoot+"/manifest.json"] = raw
	f.receipt.BundleDigest = Digest(raw)
	return f
}

func TestQualificationEngineDefaultBridgeSourceLeaves(t *testing.T) {
	approved := []string{"test_default_bridge_store.py", "testdata/default-bridge-records.json"}
	bind := func(f *qualificationEngineReadbackFixture) error {
		return qualificationEngineBind(context.Background(), f.kit, f.receipt, f.readers().read)
	}
	for _, tc := range []struct {
		name   string
		leaves []string
	}{
		{"legacy", nil},
		{"test_only", approved[:1]},
		{"records_only", approved[1:]},
		{"both", approved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := bind(newQualificationEngineSourceLeafFixture(t, tc.leaves...)); err != nil {
				t.Fatalf("fully bound synthetic Source rejected: %v", err)
			}
		})
	}
	for _, leaf := range []string{"unknown.py", "test_default_bridge_store.py.bak", "nested/test_default_bridge_store.py", "test_default_bridge_store.PY", "testdata/default-bridge-records.json.bak", "testdata/default-bridge-records.JSON"} {
		t.Run("reject_leaf/"+leaf, func(t *testing.T) {
			if err := bind(newQualificationEngineSourceLeafFixture(t, approved...)); err != nil {
				t.Fatalf("valid baseline: %v", err)
			}
			// Rejoin all manifests/hashes with this extra leaf; the closed leaf
			// check, rather than an unrelated stale digest, must reject it.
			f := newQualificationEngineSourceLeafFixture(t, append(append([]string{}, approved...), leaf)...)
			if err := bind(f); err == nil || err.Error() != "unknown Engine source leaf" {
				t.Fatalf("expected exact closed-leaf rejection, got %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*qualificationEngineReadbackFixture)
		want   string
	}{
		{"source_root", func(f *qualificationEngineReadbackFixture) { f.receipt.SourceRoot = Digest([]byte("different Source")) }, "Engine SourceRoot/TemplatesRoot differs"},
		{"templates_root", func(f *qualificationEngineReadbackFixture) {
			f.receipt.TemplatesRoot = Digest([]byte("different Templates"))
		}, "Engine SourceRoot/TemplatesRoot differs"},
		{"kit_manifest_hash", func(f *qualificationEngineReadbackFixture) {
			f.files[qualificationKitRoot+"/manifest.json"] = []byte("changed manifest")
		}, "Engine kit manifest differs"},
		{"canonical_inputs", func(f *qualificationEngineReadbackFixture) {
			other := f.kit
			other.Architecture = "amd64"
			raw, _ := json.Marshal(other)
			f.files[qualificationKitRoot+"/kit-inputs-arm64.json"] = raw
		}, "selected kit inputs differ from canonical architecture"},
		{"bundle_manifest_hash", func(f *qualificationEngineReadbackFixture) {
			f.files[qualificationEngineBundleRoot+"/manifest.json"] = []byte("changed bundle")
		}, "Engine bundle manifest differs"},
		{"kit_source_bytes", func(f *qualificationEngineReadbackFixture) {
			f.files[qualificationKitRoot+"/source/"+qualificationEngineSourcePrefix+approved[0]] = []byte("changed bytes")
		}, "current Engine source bytes differ: " + qualificationEngineSourcePrefix + approved[0]},
		{"bundle_source_bytes", func(f *qualificationEngineReadbackFixture) {
			f.files[qualificationEngineBundleRoot+"/source/"+qualificationEngineSourcePrefix+approved[1]] = []byte("changed bytes")
		}, "current Engine source bytes differ: " + qualificationEngineSourcePrefix + approved[1]},
		{"installed_source_hash", func(f *qualificationEngineReadbackFixture) {
			f.receipt.Installed[0].Digest = Digest([]byte("different installed Source"))
		}, "installed Engine file source binding differs: " + qualificationEngineSourcePrefix + "daemon.json"},
		{"installed_bytes", func(f *qualificationEngineReadbackFixture) {
			f.files[f.receipt.Installed[0].Target] = []byte("changed installed bytes")
		}, "installed Engine bytes differ: /etc/switchback-source-leaf-test/daemon.json"},
	} {
		t.Run("reject_drift/"+tc.name, func(t *testing.T) {
			f := newQualificationEngineSourceLeafFixture(t, approved...)
			if err := bind(f); err != nil {
				t.Fatalf("valid baseline: %v", err)
			}
			tc.change(f)
			if err := bind(f); err == nil || err.Error() != tc.want {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestQualificationEngineBridgeIPAMSourceLeaves(t *testing.T) {
	approved := []string{"test_bridge_ipam.py", "testdata/default-bridge-ipam-records.json"}
	for _, leaves := range [][]string{nil, approved[:1], approved[1:], approved} {
		f := newQualificationEngineSourceLeafFixture(t, leaves...)
		if err := qualificationEngineBind(context.Background(), f.kit, f.receipt, f.readers().read); err != nil {
			t.Fatalf("published optional bridge IPAM Source leaves %v rejected: %v", leaves, err)
		}
	}
	for _, leaf := range []string{"unknown.py", "test_bridge_ipam.py.bak", "nested/test_bridge_ipam.py", "test_bridge_ipam.PY", "testdata/default-bridge-ipam-records.json.bak", "testdata/default-bridge-ipam-records.JSON"} {
		t.Run(leaf, func(t *testing.T) {
			f := newQualificationEngineSourceLeafFixture(t, append(append([]string{}, approved...), leaf)...)
			if err := qualificationEngineBind(context.Background(), f.kit, f.receipt, f.readers().read); err == nil || err.Error() != "unknown Engine source leaf" {
				t.Fatalf("expected exact closed-leaf rejection, got %v", err)
			}
		})
	}
}
