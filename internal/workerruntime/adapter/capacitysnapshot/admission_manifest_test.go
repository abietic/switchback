package capacitysnapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func admissionFixture(t *testing.T) (Trust, AdmissionManifest, [][]byte) {
	t.Helper()
	trust, _, _ := fixture(t)
	trust.AdmissionPolicy = knownAdmissionPolicy()
	resource, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	network, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	source := []byte(`{"candidate":"frozen-source"}`)
	inventory, _ := json.Marshal(DeploymentInventory{Schema: DeploymentInventoryVersion, SourceManifestDigest: Digest(source), DeploymentInputsDigest: Digest([]byte("inputs")), WorkerImage: "worker@sha256:" + strings.Repeat("a", 64), WorkerImageDigest: Digest([]byte("image")), Binaries: []AdmissionBinary{{Name: "backend", Digest: Digest([]byte("binary"))}}})
	evidence := [][]byte{source, inventory, []byte(`{"receipt":"host"}`), []byte(`{"receipt":"native"}`)}
	m := AdmissionManifest{Schema: AdmissionManifestVersion, Guarantee: *trust.AdmissionPolicy, Identity: trust.Identity, ResourceProfile: resource.Ref(), NetworkProfile: config.RuntimeResourceProfileRef{ID: network.ID, Revision: network.Revision, Digest: network.Digest}, KeyID: trust.KeyID, PublicKey: trust.PublicKey, SourceManifestDigest: Digest(source), DeploymentInventoryDigest: Digest(inventory), HostBoundaryReceiptDigest: Digest(evidence[2]), NativeEnforcementReceiptDigest: Digest(evidence[3])}
	raw, _ := json.Marshal(m)
	trust.ManifestDigest = Digest(raw)
	return trust, m, evidence
}
func parseAdmissionTest(t *testing.T, trust Trust, raw []byte, want bool) {
	t.Helper()
	trust.ManifestDigest = Digest(raw)
	_, err := ParseAdmissionManifest(raw, trust)
	if (err == nil) != want {
		t.Fatalf("parse success=%v want %v: %v", err == nil, want, err)
	}
}
func TestAdmissionManifestExactSchemaAndTrustBinding(t *testing.T) {
	trust, m, _ := admissionFixture(t)
	raw, _ := json.Marshal(m)
	if _, err := ParseAdmissionManifest(raw, trust); err != nil {
		t.Fatal(err)
	}
	// These literals document the complete fixed wire names; no ready/secrets.
	want := []string{"schema", "guarantee", "identity", "resource_profile", "network_profile", "key_id", "public_key", "source_manifest_digest", "deployment_inventory_digest", "host_boundary_receipt_digest", "native_enforcement_receipt_digest"}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if len(fields) != len(want) {
		t.Fatal("wire field inventory changed")
	}
	for _, k := range want {
		if _, ok := fields[k]; !ok {
			t.Fatal("missing wire key", k)
		}
	}
	cases := map[string]func(*AdmissionManifest){
		"schema":             func(m *AdmissionManifest) { m.Schema = "unsupported" },
		"guarantee_id":       func(m *AdmissionManifest) { m.Guarantee.ID = "other" },
		"guarantee_revision": func(m *AdmissionManifest) { m.Guarantee.Revision++ },
		"guarantee_digest":   func(m *AdmissionManifest) { m.Guarantee.Digest = Digest([]byte("other")) },
		"engine":             func(m *AdmissionManifest) { m.Identity.EngineID = "other" },
		"docker_root":        func(m *AdmissionManifest) { m.Identity.DockerRoot = "/other" },
		"fs_uuid":            func(m *AdmissionManifest) { m.Identity.FSUUID = "other" },
		"boot":               func(m *AdmissionManifest) { m.Identity.GuestBootID = "other" },
		"namespace":          func(m *AdmissionManifest) { m.Identity.MountNamespace = "other" },
		"mount":              func(m *AdmissionManifest) { m.Identity.MountID++ },
		"device":             func(m *AdmissionManifest) { m.Identity.Device = "other" },
		"resource_id":        func(m *AdmissionManifest) { m.ResourceProfile.ID = "other" },
		"resource_revision":  func(m *AdmissionManifest) { m.ResourceProfile.Revision++ },
		"resource_digest":    func(m *AdmissionManifest) { m.ResourceProfile.Digest = Digest([]byte("other")) },
		"network_id":         func(m *AdmissionManifest) { m.NetworkProfile.ID = "other" },
		"network_revision":   func(m *AdmissionManifest) { m.NetworkProfile.Revision++ },
		"network_digest":     func(m *AdmissionManifest) { m.NetworkProfile.Digest = Digest([]byte("other")) },
		"publisher_id":       func(m *AdmissionManifest) { m.KeyID = "other" },
		"publisher_key":      func(m *AdmissionManifest) { m.PublicKey = make([]byte, 32) },
		"source_digest":      func(m *AdmissionManifest) { m.SourceManifestDigest = "bad" },
		"inventory_digest":   func(m *AdmissionManifest) { m.DeploymentInventoryDigest = "bad" },
		"host_digest":        func(m *AdmissionManifest) { m.HostBoundaryReceiptDigest = "bad" },
		"native_digest":      func(m *AdmissionManifest) { m.NativeEnforcementReceiptDigest = "bad" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := m
			mutate(&bad)
			raw, _ := json.Marshal(bad)
			parseAdmissionTest(t, trust, raw, false)
		})
	}
	for _, name := range []string{"legacy_policy", "unsupported_policy", "manifest_bytes"} {
		t.Run(name, func(t *testing.T) {
			tr := trust
			switch name {
			case "legacy_policy":
				tr.AdmissionPolicy = nil
			case "unsupported_policy":
				ref := *tr.AdmissionPolicy
				ref.ID = "unknown"
				tr.AdmissionPolicy = &ref
			case "manifest_bytes":
				tr.ManifestDigest = Digest(append(raw, ' '))
			}
			if _, err := ParseAdmissionManifest(raw, tr); err == nil {
				t.Fatal("Trust binding mutation accepted")
			}
		})
	}
}
func TestAdmissionManifestStrictJSON(t *testing.T) {
	trust, m, _ := admissionFixture(t)
	raw, _ := json.Marshal(m)
	for _, bad := range []string{"", "null", "{}", string(raw) + "{}", `{"ready":true}`, strings.Replace(string(raw), `"schema":`, `"schema":"other","schema":`, 1), strings.Replace(string(raw), `"schema":`, `"sche\u006da":"other","schema":`, 1)} {
		parseAdmissionTest(t, trust, []byte(bad), false)
	}
	// Exercise every nested required field, null, unknown and escaped duplicate.
	var object map[string]any
	json.Unmarshal(raw, &object)
	var walk func(map[string]any, string)
	walk = func(obj map[string]any, prefix string) {
		for key, value := range obj {
			t.Run(prefix+key, func(t *testing.T) {
				delete(obj, key)
				b, _ := json.Marshal(object)
				parseAdmissionTest(t, trust, b, false)
				obj[key] = nil
				b, _ = json.Marshal(object)
				parseAdmissionTest(t, trust, b, false)
				obj[key] = value
			})
			if nested, ok := value.(map[string]any); ok {
				nested["unknown"] = true
				b, _ := json.Marshal(object)
				parseAdmissionTest(t, trust, b, false)
				delete(nested, "unknown")
				walk(nested, prefix+key+"/")
			}
		}
	}
	walk(object, "")
}
func TestAdmissionManifestEvidenceByteBindingAndInventory(t *testing.T) {
	_, m, e := admissionFixture(t)
	if _, err := bindAdmissionEvidence(m, e[0], e[1], e[2], e[3]); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"source", "inventory", "host", "native"} {
		t.Run(name, func(t *testing.T) {
			bad := append([][]byte(nil), e...)
			bad[i] = append(append([]byte(nil), bad[i]...), ' ')
			if _, err := bindAdmissionEvidence(m, bad[0], bad[1], bad[2], bad[3]); err == nil {
				t.Fatal("changed receipt/source bytes accepted")
			}
		})
	}
	var original DeploymentInventory
	json.Unmarshal(e[1], &original)
	for _, name := range []string{"schema", "source", "input", "image", "image_digest", "binaries", "binary_digest", "duplicate_binary", "unknown", "null"} {
		t.Run(name, func(t *testing.T) {
			v := original
			v.Binaries = append([]AdmissionBinary(nil), original.Binaries...)
			switch name {
			case "schema":
				v.Schema = "bad"
			case "source":
				v.SourceManifestDigest = Digest([]byte("wrong"))
			case "input":
				v.DeploymentInputsDigest = "bad"
			case "image":
				v.WorkerImage = ""
			case "image_digest":
				v.WorkerImageDigest = "bad"
			case "binaries":
				v.Binaries = []AdmissionBinary{}
			case "binary_digest":
				v.Binaries[0].Digest = "bad"
			case "duplicate_binary":
				v.Binaries = append(v.Binaries, v.Binaries[0])
			}
			data, _ := json.Marshal(v)
			if name == "unknown" {
				data = append([]byte(`{"unknown":true,`), data[1:]...)
			}
			if name == "null" {
				data = []byte("null")
			}
			changed := m
			changed.DeploymentInventoryDigest = Digest(data)
			if _, err := bindAdmissionEvidence(changed, e[0], data, e[2], e[3]); err == nil {
				t.Fatal("invalid inventory accepted after matching byte digest")
			}
		})
	}
}
func TestAdmissionManifestUnsafeFileReads(t *testing.T) {
	tr, _, _ := admissionFixture(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "admission.json")
	os.WriteFile(file, []byte("{}"), 0600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", "/", filepath.Join(dir, "missing"), file, link, dir, dir + "/../admission.json"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := ReadAdmissionManifest(path, tr); err == nil {
				t.Fatal("unsafe/root-unowned file accepted")
			}
		})
	}
	// Non-root macOS cannot build safe root-owned deployment ancestors. Positive
	// root/native file acceptance is pending; no chown/sudo or test factory seam.
}

func TestAdmissionManifestRootAncestor(t *testing.T) {
	dir, err := openAdmissionDirectory("/")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	info, err := dir.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("opened root ancestor is not a directory")
	}
}
