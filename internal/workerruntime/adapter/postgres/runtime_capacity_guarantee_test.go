package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"testing"
)

func TestCapacityGuaranteePersistedProjectionRejectsBadDigest(t *testing.T) {
	g := config.M1AccountedCapacityGuarantee()
	raw, _ := json.Marshal(capacitysnapshot.Trust{AdmissionPolicy: &app.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}})
	if _, err := persistedCapacityTrust(raw, capacitysnapshot.Digest([]byte("other"))); err == nil {
		t.Fatal("corrupt original Trust digest accepted")
	}
}
func TestCapacityGuaranteeComparisonPreservesNil(t *testing.T) {
	g := &app.CapacityGuaranteeRef{ID: "m1_accounted_capacity_v1", Revision: 1, Digest: "digest"}
	if equalCapacityGuarantee(nil, g) || equalCapacityGuarantee(g, nil) {
		t.Fatal("legacy nil matched present guarantee")
	}
}

func guaranteeTrustFixture() capacitysnapshot.Trust {
	g := config.M1AccountedCapacityGuarantee()
	tr := capacitysnapshot.Trust{KeyID: "publisher", PublicKey: make([]byte, 32), ManifestDigest: capacitysnapshot.Digest([]byte("manifest")), Identity: capacitysnapshot.Identity{EngineID: "engine", DockerRoot: "/pool/docker", FSUUID: "fs", GuestBootID: "boot", MountNamespace: "mnt:1", MountID: 1, Device: "8:1"}, AnchorPath: "/pool/anchor", Core: []capacitysnapshot.CoreDomain{{ID: "core", CgroupPath: "/core", CPUQuotaMicros: 100000, CPUPeriodMicros: 100000, MemoryMax: 1 << 30, PIDsMax: 512}}, AdmissionPolicy: &app.CapacityGuaranteeRef{ID: g.ID, Revision: g.Revision, Digest: g.Digest}}
	for i, role := range []string{"core", "product", "images", "logs", "build", "working_copy", "cache"} {
		n := uint64(i + 1)
		tr.Storage = append(tr.Storage, capacitysnapshot.StorageDomain{ID: role, Path: "/pool/" + role, ProjectID: n, HardBytes: 1 << 30, FSUUID: "fs", Device: "8:1", MountID: 1, FSType: "xfs", XFlags: 0x200, Quota: capacitysnapshot.StorageQuota{Format: "linux_xfs_quotactl_v1", StateVersion: 1, StateFlags: 0x30, Version: 1, Flags: 2, ProjectID: n, HardBlocks512: 1 << 21}})
		tr.Coverage = append(tr.Coverage, capacitysnapshot.StorageCoverage{Role: role, DomainID: role})
	}
	return tr
}
func TestCapacityGuaranteeProjectionDetachedAndLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			tr := guaranteeTrustFixture()
			if legacy {
				tr.AdmissionPolicy = nil
			}
			raw, _ := json.Marshal(tr)
			got, err := persistedCapacityTrust(raw, capacitysnapshot.Digest(raw))
			if err != nil {
				t.Fatal(err)
			}
			first, second := copyCapacityGuarantee(got.AdmissionPolicy), copyCapacityGuarantee(got.AdmissionPolicy)
			if !equalCapacityGuarantee(first, second) || !equalCapacityGuarantee(first, tr.AdmissionPolicy) {
				t.Fatal("value projection changed")
			}
			if legacy {
				if first != nil || second != nil {
					t.Fatal("legacy was upgraded")
				}
				return
			}
			if first == second || first == got.AdmissionPolicy {
				t.Fatal("projection shared mutable pointer")
			}
			first.ID = "mutated"
			second.Revision++
			third, err := persistedCapacityTrust(raw, capacitysnapshot.Digest(raw))
			if err != nil || !equalCapacityGuarantee(third.AdmissionPolicy, tr.AdmissionPolicy) {
				t.Fatal("caller mutation changed original bytes")
			}
		})
	}
}
func TestCapacityGuaranteeValueComparisonAllFields(t *testing.T) {
	original := guaranteeTrustFixture().AdmissionPolicy
	if !equalCapacityGuarantee(nil, nil) || !equalCapacityGuarantee(original, copyCapacityGuarantee(original)) {
		t.Fatal("equal values rejected")
	}
	for _, field := range []string{"id", "revision", "digest"} {
		t.Run(field, func(t *testing.T) {
			other := *original
			switch field {
			case "id":
				other.ID = "wrong"
			case "revision":
				other.Revision++
			case "digest":
				other.Digest = "wrong"
			}
			if equalCapacityGuarantee(original, &other) {
				t.Fatal("mutated ref matched")
			}
		})
	}
}
func TestCapacityGuaranteePersistedTrustRejectsTampering(t *testing.T) {
	for _, name := range []string{"unknown_policy", "revision", "digest", "malformed", "null", "duplicate", "trailing", "missing_trust"} {
		t.Run(name, func(t *testing.T) {
			tr := guaranteeTrustFixture()
			switch name {
			case "unknown_policy":
				tr.AdmissionPolicy.ID = "unknown"
			case "revision":
				tr.AdmissionPolicy.Revision++
			case "digest":
				tr.AdmissionPolicy.Digest = capacitysnapshot.Digest([]byte("wrong"))
			}
			raw, _ := json.Marshal(tr)
			switch name {
			case "malformed":
				raw = []byte("{")
			case "null":
				raw = []byte("null")
			case "duplicate":
				raw = append([]byte(`{"key_id":"ignored",`), raw[1:]...)
			case "trailing":
				raw = append(raw, ' ')
			case "missing_trust":
				raw = nil
			}
			if _, err := persistedCapacityTrust(raw, capacitysnapshot.Digest(raw)); err == nil {
				t.Fatal("invalid persisted original trust accepted")
			}
		})
	}
}
func TestCapacityGuaranteeReservationJSONCompatibility(t *testing.T) {
	raw, err := json.Marshal(app.CapacityReservation{})
	const legacy = `{"Token":"","CommitmentID":"","PermitID":"","SnapshotSequence":0,"ExpiresAt":"0001-01-01T00:00:00Z","RunID":"","TurnID":"","RuntimeID":"","Released":false}`
	if err != nil || string(raw) != legacy {
		t.Fatalf("legacy JSON shape changed: %s %v", raw, err)
	}
	present := app.CapacityReservation{Guarantee: guaranteeTrustFixture().AdmissionPolicy}
	raw, _ = json.Marshal(present)
	var decoded app.CapacityReservation
	if json.Unmarshal(raw, &decoded) != nil || !equalCapacityGuarantee(present.Guarantee, decoded.Guarantee) || !bytes.Contains(raw, []byte(`"Guarantee":{"id":`)) {
		t.Fatal("server reference not serialized with exact capitalized wrapper")
	}
}

func TestCapacityGuaranteeSettlementLegacyMissingTrustIsNarrow(t *testing.T) {
	if _, err := persistedCapacityTrust(nil, ""); !errors.Is(err, app.CapacityUninitialized) {
		t.Fatal("missing original Trust diagnostic", err)
	}
	for _, test := range []struct {
		raw    []byte
		digest string
	}{{nil, "present"}, {[]byte{}, ""}, {[]byte{}, capacitysnapshot.Digest(nil)}, {[]byte("null"), capacitysnapshot.Digest([]byte("null"))}} {
		if _, err := persistedCapacityTrust(test.raw, test.digest); !errors.Is(err, app.CapacityEvidenceInvalid) {
			t.Fatal("partial/malformed original Trust accepted", err)
		}
	}
	legacy := []byte(`{"runtime_capacity":{"Reservation":{}}}`)
	if !settlementGuaranteeMatches(legacy, nil) {
		t.Fatal("legacy nil settlement rejected")
	}
	present, _ := json.Marshal(map[string]any{"runtime_capacity": map[string]any{"Reservation": app.CapacityReservation{Guarantee: guaranteeTrustFixture().AdmissionPolicy}}})
	if settlementGuaranteeMatches(present, nil) || settlementGuaranteeMatches(legacy, guaranteeTrustFixture().AdmissionPolicy) {
		t.Fatal("missing owner confused with guarantee")
	}
	for _, raw := range [][]byte{nil, []byte("null"), []byte("{}"), []byte(`{"runtime_capacity":null}`)} {
		if settlementGuaranteeMatches(raw, nil) {
			t.Fatal("missing/invalid snapshot accepted")
		}
	}
}
