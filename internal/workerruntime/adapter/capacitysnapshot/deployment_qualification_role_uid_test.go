package capacitysnapshot

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
)

// Synthetic frozen receipt replay only: no PostgreSQL, Engine or native probe runs.
type roleUIDFixture struct {
	trust     Trust
	files     map[string][]byte
	source    qualificationSource
	inventory DeploymentInventory
	binding   qualificationBinding
	native    qualificationNativeReceipt
}

func newRoleUIDFixture(t *testing.T) *roleUIDFixture {
	t.Helper()
	tr, files := qualificationFixture(t)
	f := &roleUIDFixture{trust: tr, files: files}
	for _, v := range []struct {
		name   string
		target any
	}{
		{"source-manifest.json", &f.source}, {"deployment-inventory.json", &f.inventory},
		{"publisher-bindings.json", &f.binding}, {"native-enforcement-receipt.json", &f.native},
	} {
		if e := json.Unmarshal(files[v.name], v.target); e != nil {
			t.Fatal(e)
		}
	}
	return f
}

func roleUIDStatus(uid uint64, caps uint64) []byte {
	return []byte(strings.Replace(string(qualificationStatus(caps)), "Uid:\t0 0 0 0", fmt.Sprintf("Uid:\t%d %d %d %d", uid, uid, uid, uid), 1))
}

func (f *roleUIDFixture) check(id string, mutate func([]byte) []byte) {
	for j := range f.native.Checks {
		if f.native.Checks[j].ID == id {
			c := &f.native.Checks[j]
			c.Observation.Data = mutate(c.Observation.Data)
			c.Observation.Digest = Digest(c.Observation.Data)
			return
		}
	}
	panic(id)
}

func (f *roleUIDFixture) addContainer(name string, uid uint64, cid string) {
	build := qualificationBuild{Role: name, Toolchain: "go1.test", InputRoot: qualificationDigest(f.source.Files), BinaryDigest: Digest([]byte(name + "-binary")), ImageDigest: Digest([]byte(name + "-image"))}
	f.source.Builds = append(f.source.Builds, build)
	f.inventory.Binaries = append(f.inventory.Binaries, AdmissionBinary{Name: name, Digest: build.BinaryDigest})
	r := qualificationRole{Role: name, Kind: "container", UID: uid, ContainerID: cid, ImageDigest: build.ImageDigest, BinaryDigest: build.BinaryDigest, CoreID: "core", CgroupPath: f.trust.Core[0].CgroupPath + "/" + name, NoNewPrivileges: 1, Seccomp: 2, Mounts: []qualificationMount{}, Environment: []qualificationEnv{}}
	f.source.Inputs.Roles = append(f.source.Inputs.Roles, r)
	f.binding.Core = append(f.binding.Core, DeploymentCoreBinding{ContainerID: cid, ImageDigest: r.ImageDigest, CoreID: r.CoreID})
	f.check("namespaces-and-roles", func(raw []byte) []byte {
		var ns qualificationNamespaces
		_ = json.Unmarshal(raw, &ns)
		ns.Roles = append(ns.Roles, qualificationNativeRole{Role: name, StatusBefore: roleUIDStatus(uid, 0), StatusAfter: roleUIDStatus(uid, 0), CgroupBefore: strings.TrimPrefix(r.CgroupPath, "/sys/fs/cgroup"), CgroupAfter: strings.TrimPrefix(r.CgroupPath, "/sys/fs/cgroup"), BinaryDigest: r.BinaryDigest, Boot: f.trust.Identity.GuestBootID})
		return qualificationJSON(ns)
	})
	f.check("mounts-and-writers", func(raw []byte) []byte {
		var reach qualificationReachability
		_ = json.Unmarshal(raw, &reach)
		reach.Observed = append(reach.Observed, qualificationMountObservation{Role: name, Raw: []byte("99 0 0:1 / / rw - overlay overlay rw\n")})
		return qualificationJSON(reach)
	})
}

// Rebind every changed input/receipt/candidate, leaving validation to production.
func (f *roleUIDFixture) seal(t *testing.T) {
	t.Helper()
	sort.Slice(f.source.Builds, func(i, j int) bool { return f.source.Builds[i].Role < f.source.Builds[j].Role })
	sort.Slice(f.source.Inputs.Roles, func(i, j int) bool { return f.source.Inputs.Roles[i].Role < f.source.Inputs.Roles[j].Role })
	f.inventory.DeploymentInputsDigest = qualificationDigest(f.source.Inputs)
	f.files["source-manifest.json"] = qualificationJSON(f.source)
	f.inventory.SourceManifestDigest = Digest(f.files["source-manifest.json"])
	f.files["deployment-inventory.json"] = qualificationJSON(f.inventory)
	var m AdmissionManifest
	_ = json.Unmarshal(f.files["admission-manifest.json"], &m)
	m.SourceManifestDigest = f.inventory.SourceManifestDigest
	m.DeploymentInventoryDigest = Digest(f.files["deployment-inventory.json"])
	candidate, e := qualificationCandidate(f.trust, m, DeploymentPublisherBinding{Core: f.binding.Core, WorkerImageDigest: f.inventory.WorkerImageDigest})
	if e != nil {
		t.Fatal(e)
	}
	var host qualificationHostReceipt
	_ = json.Unmarshal(f.files["host-boundary-receipt.json"], &host)
	host.Candidate = candidate
	f.files["host-boundary-receipt.json"] = qualificationJSON(host)
	f.native.Candidate = candidate
	f.check("loaded-identity", func(raw []byte) []byte {
		var v qualificationLoaded
		_ = json.Unmarshal(raw, &v)
		v.Roles = f.source.Inputs.Roles
		return qualificationJSON(v)
	})
	f.check("core-limits", func(raw []byte) []byte {
		var v qualificationCore
		_ = json.Unmarshal(raw, &v)
		v.Members = f.binding.Core
		return qualificationJSON(v)
	})
	f.check("mounts-and-writers", func(raw []byte) []byte {
		var v qualificationReachability
		_ = json.Unmarshal(raw, &v)
		v.Roles = f.source.Inputs.Roles
		v.Writers = f.binding.Core
		return qualificationJSON(v)
	})
	f.files["native-enforcement-receipt.json"] = qualificationJSON(f.native)
	f.files["publisher-bindings.json"] = qualificationJSON(f.binding)
	f.files["admission-manifest.json"] = qualificationJSON(m)
	f.trust = qualificationRebind(f.trust, f.files)
}

func TestDeploymentQualificationDeclaredRoleUID(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint64
	}{{"root-baseline", 0}, {"postgres", 999}, {"assistant", 10001}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoleUIDFixture(t)
			role := tc.name
			if role == "root-baseline" {
				role = "postgres"
			}
			f.addContainer(role, tc.uid, strings.Repeat("b", 64))
			f.seal(t)
			// Isolate the source and native hypotheses before the full real validator.
			if e := validateQualificationSource(f.source, f.inventory, f.trust, f.binding.Core); e != nil {
				t.Errorf("source semantics: %v", e)
			}
			out := qualificationResult{trust: f.trust, inputs: f.source.Inputs, metadata: AdmissionManifestMetadata{Inventory: f.inventory}, binding: DeploymentPublisherBinding{Core: f.binding.Core, WorkerImageDigest: f.inventory.WorkerImageDigest}}
			if e := validateQualificationNative(f.native, out); e != nil {
				t.Errorf("native semantics: %v", e)
			}
			if _, e := validateDeploymentQualification(f.trust, f.files); e != nil {
				t.Fatalf("full frozen qualification: %v", e)
			}
		})
	}
}

func TestDeploymentQualificationRoleUIDRejects(t *testing.T) {
	cases := []string{"declared-mismatch", "uid-real", "uid-effective", "uid-saved", "uid-fs", "overflow", "caps", "nnp", "seccomp", "ambient", "inheritable", "backend", "publisher", "observer", "host-process", "missing-core", "handoff"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRoleUIDFixture(t)
			f.addContainer("postgres", 999, strings.Repeat("b", 64))
			switch name {
			case "declared-mismatch", "overflow":
				for j := range f.source.Inputs.Roles {
					if f.source.Inputs.Roles[j].Role == "postgres" {
						f.source.Inputs.Roles[j].UID = 10001
						if name == "overflow" {
							f.source.Inputs.Roles[j].UID = 1 << 32
						}
					}
				}
			case "backend", "publisher", "observer":
				for j := range f.source.Inputs.Roles {
					if f.source.Inputs.Roles[j].Role == name {
						f.source.Inputs.Roles[j].UID = 999
					}
				}
			case "host-process":
				for j := range f.source.Inputs.Roles {
					if f.source.Inputs.Roles[j].Role == "postgres" {
						f.source.Inputs.Roles[j].Kind = "host_process"
						f.source.Inputs.Roles[j].ContainerID = ""
						f.source.Inputs.Roles[j].ImageDigest = ""
					}
				}
				for j := range f.source.Builds {
					if f.source.Builds[j].Role == "postgres" {
						f.source.Builds[j].ImageDigest = ""
					}
				}
				f.binding.Core = f.binding.Core[:1]
			case "missing-core":
				f.source.Inputs.Roles = f.source.Inputs.Roles[:len(f.source.Inputs.Roles)-1]
			case "handoff":
				f.check("backend-handoff", func(raw []byte) []byte {
					var h qualificationHandoff
					_ = json.Unmarshal(raw, &h)
					for j := range h.Cases {
						h.Cases[j].Status = []byte(strings.Replace(string(h.Cases[j].Status), "Uid:\t0 0 0 0", "Uid:\t999 999 999 999", 1))
					}
					return qualificationJSON(h)
				})
			default:
				f.check("namespaces-and-roles", func(raw []byte) []byte {
					var ns qualificationNamespaces
					_ = json.Unmarshal(raw, &ns)
					for j := range ns.Roles {
						r := &ns.Roles[j]
						if r.Role != "postgres" {
							continue
						}
						s := string(r.StatusBefore)
						switch name {
						case "uid-real":
							s = strings.Replace(s, "Uid:\t999 999 999 999", "Uid:\t0 999 999 999", 1)
						case "uid-effective":
							s = strings.Replace(s, "Uid:\t999 999 999 999", "Uid:\t999 0 999 999", 1)
						case "uid-saved":
							s = strings.Replace(s, "Uid:\t999 999 999 999", "Uid:\t999 999 0 999", 1)
						case "uid-fs":
							s = strings.Replace(s, "Uid:\t999 999 999 999", "Uid:\t999 999 999 0", 1)
						case "caps":
							s = strings.Replace(s, "CapEff:\t0000000000000000", "CapEff:\t0000000000000001", 1)
						case "nnp":
							s = strings.Replace(s, "NoNewPrivs:\t1", "NoNewPrivs:\t0", 1)
						case "seccomp":
							s = strings.Replace(s, "Seccomp:\t2", "Seccomp:\t0", 1)
						case "ambient":
							s = strings.Replace(s, "CapAmb:\t0000000000000000", "CapAmb:\t0000000000000001", 1)
						case "inheritable":
							s = strings.Replace(s, "CapInh:\t0000000000000000", "CapInh:\t0000000000000001", 1)
						}
						r.StatusBefore = []byte(s)
						r.StatusAfter = []byte(s)
					}
					return qualificationJSON(ns)
				})
			}
			f.seal(t)
			switch name {
			case "overflow", "backend", "publisher", "observer", "host-process", "missing-core":
				if e := validateQualificationSource(f.source, f.inventory, f.trust, f.binding.Core); e == nil {
					t.Fatal("source accepted forbidden UID or missing core role")
				}
			default:
				if e := validateQualificationSource(f.source, f.inventory, f.trust, f.binding.Core); e != nil {
					t.Fatalf("native negative has invalid source fixture: %v", e)
				}
				out := qualificationResult{trust: f.trust, inputs: f.source.Inputs, metadata: AdmissionManifestMetadata{Inventory: f.inventory}, binding: DeploymentPublisherBinding{Core: f.binding.Core, WorkerImageDigest: f.inventory.WorkerImageDigest}}
				if e := validateQualificationNative(f.native, out); e == nil {
					t.Fatal("native accepted invalid role or non-root handoff")
				}
			}
			if _, e := validateDeploymentQualification(f.trust, f.files); e == nil {
				t.Fatal("invalid frozen role accepted")
			}
		})
	}
}

func TestRoleMatchesRootOnly(t *testing.T) {
	for _, uid := range []uint64{0, 999} {
		p, e := procstatus.Parse(roleUIDStatus(uid, 11))
		if e != nil {
			t.Fatal(e)
		}
		if roleMatches(p, 11) != (uid == 0) {
			t.Fatalf("root handoff predicate accepted UID %d", uid)
		}
	}
}

func TestRoleMatchesDeclaredUIDRange(t *testing.T) {
	for _, tc := range []struct {
		name               string
		declared, observed uint64
		want               bool
	}{
		{"uint32-max", 1<<32 - 1, 1<<32 - 1, true},
		{"overflow-to-root", 1 << 32, 0, false},
		{"overflow-to-postgres", 1<<32 + 999, 999, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := procstatus.Parse(roleUIDStatus(tc.observed, 0))
			if e != nil {
				t.Fatal(e)
			}
			if roleMatchesUID(p, tc.declared, 0) != tc.want {
				t.Fatal("declared UID range mismatch")
			}
		})
	}
}
