package capacitysnapshot

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (Trust, ed25519.PrivateKey, Facts) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	i := Identity{EngineID: "engine", DockerRoot: "/pool/docker", FSUUID: "uuid", GuestBootID: "boot", MountNamespace: "mnt:[1]", MountID: 1, Device: "8:1"}
	tr := Trust{KeyID: "key", PublicKey: pub, ManifestDigest: Digest([]byte("manifest")), Identity: i, AnchorPath: "/pool/anchor", Core: []CoreDomain{{ID: "core", CgroupPath: "/core", CPUQuotaMicros: 100000, CPUPeriodMicros: 100000, MemoryMax: 1 << 30, PIDsMax: 512}}}
	for n, id := range []string{"core", "product", "images", "logs", "build", "working_copy", "cache"} {
		tr.Storage = append(tr.Storage, StorageDomain{ID: id, Path: "/pool/" + id, ProjectID: uint64(n + 1), HardBytes: 1 << 30, FSUUID: i.FSUUID, Device: i.Device, MountID: i.MountID, FSType: "xfs", XFlags: 0x200, Quota: StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: uint64(n + 1), HardBlocks512: (1 << 30) / 512}})
		tr.Coverage = append(tr.Coverage, StorageCoverage{Role: id, DomainID: id})
	}
	return tr, key, Facts{Sequence: 1, ObservedAt: time.Now().UTC(), Identity: i, ManifestDigest: tr.ManifestDigest, EngineCPUMilli: 8000, EngineMemoryBytes: 16 << 30, Anchor: Anchor{Path: tr.AnchorPath, FSUUID: i.FSUUID, MountID: 1, Device: i.Device, FSType: "xfs", MountOptions: []string{"prjquota"}, AvailableBytes: 100 << 30}, Core: tr.Core, Storage: append([]StorageDomain(nil), tr.Storage...), Containers: []Container{}, Removals: []Removal{}}
}
func signed(t *testing.T, key ed25519.PrivateKey, payload []byte) []byte {
	t.Helper()
	b, e := json.Marshal(Envelope{Version: Version, KeyID: "key", Payload: payload, Signature: ed25519.Sign(key, append([]byte(SignatureDomain), payload...))})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestVerifierRejectsAmbiguousRawFacts(t *testing.T) {
	tr, key, f := fixture(t)
	b, _ := json.Marshal(f)
	cases := map[string]string{
		"missing_anchor_project": strings.Replace(string(b), `"project_id":0,`, "", 1),
		"duplicate":              strings.Replace(string(b), `"sequence":1`, `"sequence":1,"sequence":2`, 1),
		"escaped_duplicate":      strings.Replace(string(b), `"sequence":1`, `"sequence":1,"\u0073equence":2`, 1),
		"unknown":                strings.Replace(string(b), `"sequence":1`, `"sequence":1,"allow":true`, 1),
		"negative":               strings.Replace(string(b), `"sequence":1`, `"sequence":-1`, 1),
		"fraction":               strings.Replace(string(b), `"sequence":1`, `"sequence":1.5`, 1),
		"exponent":               strings.Replace(string(b), `"sequence":1`, `"sequence":1e0`, 1),
		"overflow":               strings.Replace(string(b), `"sequence":1`, `"sequence":9223372036854775808`, 1),
		"null_inventory":         strings.Replace(string(b), `"containers":[]`, `"containers":null`, 1),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, e := Verify(signed(t, key, []byte(p)), tr); e == nil {
				t.Fatal("ambiguous schema accepted")
			}
		})
	}
}
func TestVerifierRawBytesAndPinnedTrust(t *testing.T) {
	tr, key, f := fixture(t)
	payload, _ := json.MarshalIndent(f, "", " ")
	raw := signed(t, key, payload)
	v, err := Verify(raw, tr)
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Payload()) != string(payload) {
		t.Fatal("raw payload altered")
	}
	if err = CheckFacts(v.Facts(), tr); err != nil {
		t.Fatal(err)
	}
	changed := v.Payload()
	changed[0] = 'x'
	if _, err = Verify(v.Raw(), tr); err != nil {
		t.Fatal("caller mutated verified bytes")
	}
	var e Envelope
	json.Unmarshal(raw, &e)
	compact, _ := json.Marshal(f)
	e.Payload = compact
	bad, _ := json.Marshal(e)
	if _, err = Verify(bad, tr); err == nil {
		t.Fatal("reencoded raw payload retained signature authority")
	}
	e.Payload = payload
	e.Version = "v2"
	bad, _ = json.Marshal(e)
	if _, err = Verify(bad, tr); err == nil {
		t.Fatal("version accepted")
	}
	e.Version = Version
	e.KeyID = "other"
	bad, _ = json.Marshal(e)
	if _, err = Verify(bad, tr); err == nil {
		t.Fatal("key accepted")
	}
	if _, err = Verify(make([]byte, MaxBytes+1), tr); err == nil {
		t.Fatal("oversize accepted")
	}
}
func TestVerifierCoverageAndAnchor(t *testing.T) {
	for _, name := range []string{"project", "inherit", "mount", "filesystem", "core", "storage", "identity", "overlap", "units"} {
		t.Run(name, func(t *testing.T) {
			tr, _, f := fixture(t)
			switch name {
			case "project":
				f.Anchor.ProjectID = 1
			case "inherit":
				f.Anchor.XFlags = 0x200
			case "mount":
				f.Anchor.MountID++
			case "filesystem":
				f.Anchor.FSType = "ext4"
			case "core":
				f.Core = nil
			case "storage":
				f.Storage = f.Storage[:6]
			case "identity":
				f.Identity.GuestBootID = "changed"
			case "overlap":
				f.Storage[1].Path = f.Storage[0].Path
			case "units":
				f.EngineMemoryBytes = ^uint64(0)
			}
			if CheckFacts(f, tr) == nil {
				t.Fatal("unknown coverage accepted")
			}
		})
	}
}

func TestTrustRequiresMeasuredStorageEnforcement(t *testing.T) {
	for _, name := range []string{"filesystem", "submount", "inheritance", "accounting_only", "unknown_flags", "unsupported_format", "quota_project", "block_units", "readback_overflow"} {
		t.Run(name, func(t *testing.T) {
			tr, _, _ := fixture(t)
			d := &tr.Storage[0]
			switch name {
			case "filesystem":
				d.FSUUID = "other"
			case "submount":
				d.MountID++
			case "inheritance":
				d.XFlags = 0
			case "accounting_only":
				d.Quota.StateFlags = 0x10
			case "unknown_flags":
				d.Quota.StateFlags = 0x70
			case "unsupported_format":
				d.Quota.Format = "configured"
			case "quota_project":
				d.Quota.ProjectID++
			case "block_units":
				d.Quota.HardBlocks512 = d.HardBytes
			case "readback_overflow":
				d.Quota.HardBlocks512 = uint64(1) << 63
			}
			if ValidateTrust(tr) == nil {
				t.Fatal("deployment accepted incomplete quota identity/enforcement")
			}
		})
	}
}
func TestVerifierRejectsLayoutAndCaseAliases(t *testing.T) {
	tr, key, f := fixture(t)
	raw, _ := json.Marshal(f)
	for _, payload := range []string{strings.Replace(string(raw), `"sequence":1`, `"sequence":1,"Sequence":2`, 1), strings.Replace(string(raw), `"sequence":1`, `"Sequence":1`, 1), strings.Replace(string(raw), `"sequence":1`, `"sequence":1,"coverage":[]`, 1)} {
		if _, err := Verify(signed(t, key, []byte(payload)), tr); err == nil {
			t.Fatal("sample changed pinned layout or exact schema keys")
		}
	}
}
