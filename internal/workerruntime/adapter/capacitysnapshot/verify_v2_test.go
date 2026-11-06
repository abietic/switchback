package capacitysnapshot

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// SYNTHETIC: these signed fixtures establish the offline contract, not native provenance.
func inodeV2Fixture(t *testing.T) (Trust, ed25519.PrivateKey, []byte) {
	t.Helper()
	tr, key, f := fixture(t)
	layer := f.Storage[0]
	layer.ID, layer.Path, layer.ProjectID, layer.Quota.ProjectID = "layer", "/pool/worker-layer", 41, 41
	f.Containers = []Container{{ContainerID: "container", RuntimeID: "runtime", SessionID: "session", Generation: 1, ProfileID: "local_standard_v1", ProfileRevision: 1, ProfileDigest: Digest([]byte("profile")), NanoCPUs: 2000000000, MemoryMax: 4 << 30, PIDsMax: 512, Layer: layer, CgroupPath: "/worker", CPUQuotaMicros: 200000, CPUPeriodMicros: 100000}}
	b, _ := json.Marshal(f)
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	domains := fields["storage"].([]any)
	domains = append(domains, fields["containers"].([]any)[0].(map[string]any)["layer"])
	for _, d := range domains {
		q := d.(map[string]any)["quota"].(map[string]any)
		q["hard_inodes"] = uint64(100)
		q["used_inodes"] = uint64(7)
	}
	b, _ = json.Marshal(fields)
	return tr, key, b
}
func inodeV2Signed(t *testing.T, key ed25519.PrivateKey, payload []byte, version, domain string) []byte {
	t.Helper()
	b, err := json.Marshal(Envelope{Version: version, KeyID: "key", Payload: payload, Signature: ed25519.Sign(key, append([]byte(domain), payload...))})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestEvidenceV2SignedAndDetached(t *testing.T) {
	tr, key, payload := inodeV2Fixture(t)
	// Independent protocol literals pin the exact version and the actual NUL byte.
	raw := inodeV2Signed(t, key, payload, "switchback.capacity.raw.v2", "switchback/capacity/raw/v2\x00")
	original := bytes.Clone(raw)
	v, err := VerifyEvidenceV2(raw, tr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.Payload(), payload) || !bytes.Equal(v.Raw(), original) || v.Digest() != Digest(original) {
		t.Fatal("original signed bytes lost")
	}
	if v.Facts().Storage[0].Quota.HardInodes != 100 || v.Facts().Containers[0].Layer.Quota.UsedInodes != 7 {
		t.Fatal("inode counters lost")
	}
	raw[0] = 'x'
	payload[0] = 'x'
	tr.PublicKey[0] ^= 255
	for _, b := range [][]byte{v.Raw(), v.Payload(), v.Signature()} {
		b[0] ^= 255
	}
	f := v.Facts()
	f.Storage[0].Quota.HardInodes = 999
	f.Containers[0].Layer.Quota.UsedInodes = 999
	f.Anchor.MountOptions[0] = "changed"
	f.Core[0].ID = "changed"
	if !bytes.Equal(v.Raw(), original) || v.Facts().Storage[0].Quota.HardInodes != 100 || v.Facts().Containers[0].Layer.Quota.UsedInodes != 7 || v.Facts().Anchor.MountOptions[0] != "prjquota" || v.Facts().Core[0].ID != "core" {
		t.Fatal("verified evidence aliased mutable input/getter")
	}
	var e Envelope
	_ = json.Unmarshal(original, &e)
	if !bytes.Equal(v.Signature(), e.Signature) {
		t.Fatal("signature getter aliased")
	}
	if reflect.TypeOf(v).AssignableTo(reflect.TypeOf(Verified{})) || reflect.TypeOf(v).ConvertibleTo(reflect.TypeOf(Verified{})) {
		t.Fatal("v2 evidence can downgrade to admission Verified")
	}
	for i := 0; i < reflect.TypeOf(v).NumField(); i++ {
		if reflect.TypeOf(v).Field(i).IsExported() {
			t.Fatal("constructible verified fields")
		}
	}
}
func TestEvidenceV2AuthenticityAndVersionIsolation(t *testing.T) {
	tr, key, p := inodeV2Fixture(t)
	for name, change := range map[string]func(*Envelope){
		"version": func(e *Envelope) { e.Version = Version }, "key_id": func(e *Envelope) { e.KeyID = "other" }, "signature": func(e *Envelope) { e.Signature[0] ^= 1 }, "raw_payload": func(e *Envelope) { e.Payload = append(e.Payload, ' ') }, "short_signature": func(e *Envelope) { e.Signature = e.Signature[:63] },
	} {
		t.Run(name, func(t *testing.T) {
			var e Envelope
			_ = json.Unmarshal(inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2), &e)
			change(&e)
			b, _ := json.Marshal(e)
			if _, err := VerifyEvidenceV2(b, tr); err == nil {
				t.Fatal("unauthentic envelope accepted")
			}
		})
	}
	for name, domain := range map[string]string{"v1_domain": SignatureDomain, "literal_escape": `switchback/capacity/raw/v2\x00`, "missing_nul": "switchback/capacity/raw/v2"} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyEvidenceV2(inodeV2Signed(t, key, p, VersionV2, domain), tr); err == nil {
				t.Fatal("wrong domain accepted")
			}
		})
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, 32))
	if _, err := VerifyEvidenceV2(inodeV2Signed(t, other, p, VersionV2, SignatureDomainV2), tr); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := Verify(inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2), tr); err == nil {
		t.Fatal("v1 accepted v2")
	}
	_, _, f := fixture(t)
	f.Identity = tr.Identity
	f.ManifestDigest = tr.ManifestDigest
	f.Core = tr.Core
	f.Storage = tr.Storage
	p1, _ := json.Marshal(f)
	if _, err := Verify(signed(t, key, p1), tr); err != nil {
		t.Fatal("v1 compatibility", err)
	}
	if _, err := VerifyEvidenceV2(signed(t, key, p1), tr); err == nil {
		t.Fatal("v2 accepted v1")
	}
	if _, err := VerifyEvidenceV2(inodeV2Signed(t, key, p1, VersionV2, SignatureDomainV2), tr); err == nil {
		t.Fatal("v1 payload cross-wrapped as v2")
	}
	if _, err := Verify(inodeV2Signed(t, key, p, Version, SignatureDomain), tr); err == nil {
		t.Fatal("v2 payload cross-wrapped as v1")
	}
}
func TestEvidenceV2StrictRequiredInodes(t *testing.T) {
	tr, key, p := inodeV2Fixture(t)
	// Every mandatory inode field in all seven static domains and the layer is exercised.
	for domain := 0; domain < 8; domain++ {
		for _, field := range []string{"hard_inodes", "used_inodes"} {
			for _, value := range []string{"missing", "null", "-1", "1.5", "1e0", "18446744073709551616", `"7"`} {
				name := strings.Join([]string{[]string{"core", "product", "images", "logs", "build", "working_copy", "cache", "layer"}[domain], field, value}, "/")
				t.Run(name, func(t *testing.T) {
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(p, &fields)
					var ds []map[string]json.RawMessage
					_ = json.Unmarshal(fields["storage"], &ds)
					var cs []map[string]json.RawMessage
					_ = json.Unmarshal(fields["containers"], &cs)
					var layer map[string]json.RawMessage
					_ = json.Unmarshal(cs[0]["layer"], &layer)
					d := layer
					if domain < 7 {
						d = ds[domain]
					}
					var q map[string]json.RawMessage
					_ = json.Unmarshal(d["quota"], &q)
					if value == "missing" {
						delete(q, field)
					} else {
						q[field] = json.RawMessage(value)
					}
					d["quota"], _ = json.Marshal(q)
					fields["storage"], _ = json.Marshal(ds)
					cs[0]["layer"], _ = json.Marshal(layer)
					fields["containers"], _ = json.Marshal(cs)
					bad, _ := json.Marshal(fields)
					if bytes.Equal(bad, p) {
						t.Fatal("mutation ineffective")
					}
					if _, err := VerifyEvidenceV2(inodeV2Signed(t, key, bad, VersionV2, SignatureDomainV2), tr); err == nil {
						t.Fatal("malformed signed quota accepted")
					}
				})
			}
		}
	}
	t.Run("null_storage", func(t *testing.T) {
		// A valid signed control and exactly one changed value isolate null
		// rejection from unknown-field or deployment-metadata rejection.
		if _, err := VerifyEvidenceV2(inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2), tr); err != nil {
			t.Fatal("valid control rejected", err)
		}
		var original map[string]json.RawMessage
		if err := json.Unmarshal(p, &original); err != nil {
			t.Fatal(err)
		}
		var changed map[string]json.RawMessage
		if err := json.Unmarshal(p, &changed); err != nil {
			t.Fatal(err)
		}
		changed["storage"] = json.RawMessage("null")
		bad, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]json.RawMessage
		if err = json.Unmarshal(bad, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded) != len(original) || string(decoded["storage"]) != "null" {
			t.Fatal("null variant changed field set or failed to set storage=null")
		}
		for name, value := range original {
			if name != "storage" && !bytes.Equal(decoded[name], value) {
				t.Fatalf("null variant also changed %s", name)
			}
		}
		if _, err = VerifyEvidenceV2(inodeV2Signed(t, key, bad, VersionV2, SignatureDomainV2), tr); err == nil {
			t.Fatal("signed storage=null accepted")
		}
	})
	for name, bad := range map[string]string{
		"duplicate":         strings.Replace(string(p), `"hard_inodes":100`, `"hard_inodes":100,"hard_inodes":100`, 1),
		"escaped_duplicate": strings.Replace(string(p), `"hard_inodes":100`, `"hard_inodes":100,"\u0068ard_inodes":100`, 1),
		"alias":             strings.Replace(string(p), `"hard_inodes":100`, `"Hard_Inodes":100`, 1),
		"extra_alias":       strings.Replace(string(p), `"hard_inodes":100`, `"hard_inodes":100,"Hard_Inodes":100`, 1),
		"unknown":           strings.Replace(string(p), `"hard_inodes":100`, `"hard_inodes":100,"remaining_inodes":99`, 1),
		"trailing":          string(p) + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyEvidenceV2(inodeV2Signed(t, key, []byte(bad), VersionV2, SignatureDomainV2), tr); err == nil {
				t.Fatal("ambiguous signed JSON accepted")
			}
		})
	}
}
func TestEvidenceV2QuotaTypeFlagsRemainDiagnostic(t *testing.T) {
	// Selected XFS QSTATV bits: user accounting/enforcement=0x01/0x02,
	// group=0x04/0x08, project=0x10/0x20. Other active types do not
	// manufacture their quota-inode sets from these project records.
	for _, tc := range []struct {
		name  string
		flags uint64
	}{
		{"project_only", 0x30},
		{"user_accounting", 0x31},
		{"user_enforced", 0x33},
		{"group_accounting", 0x34},
		{"user_group_accounting", 0x35},
		{"user_enforced_group_accounting", 0x37},
		{"group_enforced", 0x3c},
		{"user_accounting_group_enforced", 0x3d},
		{"user_group_enforced", 0x3f},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, key, p := inodeV2Fixture(t)
			var f FactsV2
			if err := json.Unmarshal(p, &f); err != nil {
				t.Fatal(err)
			}
			for i := range f.Storage {
				tr.Storage[i].Quota.StateFlags = tc.flags
				f.Storage[i].Quota.StateFlags = tc.flags
				f.Storage[i].Quota.HardInodes = 0
			}
			f.Containers[0].Layer.Quota.StateFlags = tc.flags
			f.Containers[0].Layer.Quota.HardInodes = 0
			p, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			v, err := VerifyEvidenceV2(inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2), tr)
			if err != nil {
				t.Fatal("combined quota-type flags rejected", err)
			}
			observed := v.Facts()
			quotas := make([]StorageQuotaV2, 0, len(observed.Storage)+1)
			for _, d := range observed.Storage {
				quotas = append(quotas, d.Quota)
			}
			quotas = append(quotas, observed.Containers[0].Layer.Quota)
			for _, q := range quotas {
				if q.StateFlags != tc.flags || q.Flags != 2 || q.HardInodes != 0 || q.UsedInodes != 7 {
					t.Fatal("project counters or combined flags were rewritten")
				}
				if AssessInodeEvidence(q) != InodeBoundUnproven {
					t.Fatal("other active quota types manufactured a project inode bound")
				}
			}
			if _, err = Verify(v.Raw(), tr); err == nil {
				t.Fatal("diagnostic v2 envelope entered v1 admission evidence")
			}
			for _, claim := range []string{"all_quota_types_complete", "ready", "capacity_ready", "grant"} {
				t.Run(claim, func(t *testing.T) {
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(p, &fields); err != nil {
						t.Fatal(err)
					}
					fields[claim] = json.RawMessage("true")
					bad, err := json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					// Correctly re-sign so strict schema validation rejects the
					// attempted authority claim after signature verification.
					if _, err = VerifyEvidenceV2(inodeV2Signed(t, key, bad, VersionV2, SignatureDomainV2), tr); err == nil {
						t.Fatalf("quota-type flags authorized unsupported %s claim", claim)
					}
				})
			}
		})
	}
}
func TestEvidenceV2DeploymentMetadata(t *testing.T) {
	for _, name := range []string{"zero_sequence", "zero_clock", "identity", "manifest", "anchor", "core", "storage", "layer", "block_units", "block_overflow", "invalid_trust"} {
		t.Run(name, func(t *testing.T) {
			tr, key, p := inodeV2Fixture(t)
			var f FactsV2
			if err := json.Unmarshal(p, &f); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "zero_sequence":
				f.Sequence = 0
			case "zero_clock":
				f.ObservedAt = time.Time{}
			case "identity":
				f.Identity.EngineID = "other"
			case "manifest":
				f.ManifestDigest = Digest([]byte("other"))
			case "anchor":
				f.Anchor.MountID++
			case "core":
				f.Core[0].MemoryMax++
			case "storage":
				f.Storage[0].Path = "/wrong"
			case "layer":
				f.Containers[0].Layer.FSUUID = "wrong"
			case "block_units":
				f.Storage[0].Quota.HardBlocks512++
			case "block_overflow":
				f.Storage[0].Quota.HardBlocks512 = math.MaxUint64
			case "invalid_trust":
				tr.PublicKey = nil
			}
			p, _ = json.Marshal(f)
			if _, err := VerifyEvidenceV2(inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2), tr); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}
func TestEvidenceV2StrictEnvelope(t *testing.T) {
	tr, key, p := inodeV2Fixture(t)
	good := inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2)
	for name, bad := range map[string][]byte{
		"duplicate": bytes.Replace(good, []byte(`"key_id":"key"`), []byte(`"key_id":"key","key_id":"key"`), 1),
		"alias":     bytes.Replace(good, []byte(`"key_id"`), []byte(`"Key_ID"`), 1),
		"unknown":   bytes.Replace(good, []byte(`"key_id":"key"`), []byte(`"key_id":"key","allow":true`), 1),
		"missing":   bytes.Replace(good, []byte(`"key_id":"key",`), nil, 1),
		"null":      bytes.Replace(good, []byte(`"key_id":"key"`), []byte(`"key_id":null`), 1),
		"trailing":  append(bytes.Clone(good), []byte(` {}`)...),
		"oversize":  make([]byte, MaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(good, bad) {
				t.Fatal("ineffective mutation")
			}
			if _, err := VerifyEvidenceV2(bad, tr); err == nil {
				t.Fatal("malformed envelope accepted")
			}
		})
	}
}
func TestEvidenceV2InodeAssessment(t *testing.T) {
	cases := []struct {
		name       string
		hard, used uint64
		status     InodeEvidenceAssessment
	}{{"zero", 0, 0, InodeBoundUnproven}, {"zero_used", 0, 17, InodeBoundUnproven}, {"observed_zero", 100, 0, InodesObserved}, {"equal", 100, 100, InodesObserved}, {"over", 100, 101, InodesOverLimit}, {"max", math.MaxUint64, math.MaxUint64, InodesObserved}, {"max_used", 1, math.MaxUint64, InodesOverLimit}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, key, p := inodeV2Fixture(t)
			var f FactsV2
			_ = json.Unmarshal(p, &f)
			for j := range f.Storage {
				f.Storage[j].Quota.HardInodes = tc.hard
				f.Storage[j].Quota.UsedInodes = tc.used
			}
			f.Containers[0].Layer.Quota.HardInodes = tc.hard
			f.Containers[0].Layer.Quota.UsedInodes = tc.used
			p, _ = json.Marshal(f)
			v, err := VerifyEvidenceV2(inodeV2Signed(t, key, p, VersionV2, SignatureDomainV2), tr)
			if err != nil {
				t.Fatal(err)
			}
			q := v.Facts().Storage[0].Quota
			if q.HardInodes != tc.hard || q.UsedInodes != tc.used || AssessInodeEvidence(q) != tc.status {
				t.Fatal("counter altered or assessment wrong")
			}
			if AssessInodeEvidence(v.Facts().Containers[0].Layer.Quota) != tc.status {
				t.Fatal("layer assessment wrong")
			}
		})
	}
}
