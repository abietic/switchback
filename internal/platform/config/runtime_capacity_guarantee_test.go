package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const capacityGuaranteeGolden = `{"id":"m1_accounted_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":true,"authority":"original_pg_capacity_owner"}`
const capacityGuaranteeDigestGolden = "sha256:badb64addbd9fbeff2f74978887f4d3c592d587f0cd47d72efeb40dd48b5f5cd"
const sharedVolumeCapacityGuaranteeGolden = `{"id":"m1_shared_volume_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":false,"host_storage_boundary":"apfs_volume_quota_and_reserve_v1","host_future_container_metadata_upper_bound":false,"authority":"original_pg_capacity_owner"}`
const sharedVolumeCapacityGuaranteeDigestGolden = "sha256:31b0caa11d3ff6cf95631815a0879771ffce565c735f6fd5f6ecab58638dcb01"

func TestRuntimeCapacityGuaranteeAcceptsSharedVolumeContract(t *testing.T) {
	want := RuntimeCapacityGuarantee{ID: "m1_shared_volume_capacity_v1", Revision: 1, Digest: sharedVolumeCapacityGuaranteeDigestGolden}
	if err := want.Validate(); err != nil {
		t.Fatalf("known shared-volume contract rejected: %v", err)
	}
}

func TestRuntimeCapacityGuaranteeDefinition(t *testing.T) {
	body := RuntimeCapacityGuaranteeDefinition()
	if len(body) != 367 || !bytes.Equal(body, []byte(capacityGuaranteeGolden)) {
		t.Fatalf("canonical contract bytes differ: length=%d body=%q", len(body), body)
	}
	hash := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(hash[:]); got != capacityGuaranteeDigestGolden {
		t.Fatalf("independent digest=%s", got)
	}
	want := RuntimeCapacityGuarantee{ID: "m1_accounted_capacity_v1", Revision: 1, Digest: capacityGuaranteeDigestGolden}
	if got := M1AccountedCapacityGuarantee(); got != want {
		t.Fatalf("contract=%+v want=%+v", got, want)
	}
	if err := want.Validate(); err != nil {
		t.Fatalf("known contract rejected: %v", err)
	}
}

func TestRuntimeCapacityGuaranteeSharedVolumeDefinition(t *testing.T) {
	want := RuntimeCapacityGuarantee{ID: "m1_shared_volume_capacity_v1", Revision: 1, Digest: sharedVolumeCapacityGuaranteeDigestGolden}
	if got := M1SharedVolumeCapacityGuarantee(); got != want {
		t.Fatalf("contract=%+v want=%+v", got, want)
	}
	if err := want.Validate(); err != nil {
		t.Fatalf("known shared-volume contract rejected: %v", err)
	}
	body, err := RuntimeCapacityGuaranteeDefinitionFor(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 482 || !bytes.Equal(body, []byte(sharedVolumeCapacityGuaranteeGolden)) {
		t.Fatalf("canonical shared-volume contract bytes differ: length=%d body=%q", len(body), body)
	}
	hash := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(hash[:]); got != sharedVolumeCapacityGuaranteeDigestGolden {
		t.Fatalf("independent shared-volume digest=%s", got)
	}
	legacy, err := RuntimeCapacityGuaranteeDefinitionFor(M1AccountedCapacityGuarantee())
	if err != nil || !bytes.Equal(legacy, []byte(capacityGuaranteeGolden)) {
		t.Fatalf("legacy definition changed: error=%v body=%q", err, legacy)
	}
}

func TestRuntimeCapacityGuaranteeDefinitionForDetachedValues(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  RuntimeCapacityGuarantee
		want string
	}{
		{name: "accounted", ref: M1AccountedCapacityGuarantee(), want: capacityGuaranteeGolden},
		{name: "shared_volume", ref: M1SharedVolumeCapacityGuarantee(), want: sharedVolumeCapacityGuaranteeGolden},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := RuntimeCapacityGuaranteeDefinitionFor(test.ref)
			if err != nil {
				t.Fatal(err)
			}
			body[0] = 'x'
			got, err := RuntimeCapacityGuaranteeDefinitionFor(test.ref)
			if err != nil || string(got) != test.want {
				t.Fatalf("definition shared mutable bytes: error=%v body=%q", err, got)
			}
		})
	}
}

func TestRuntimeCapacityGuaranteeDefinitionForRejectsUnsupportedRef(t *testing.T) {
	for name, change := range map[string]func(*RuntimeCapacityGuarantee){
		"wrong_revision": func(g *RuntimeCapacityGuarantee) { g.Revision++ },
		"wrong_digest":   func(g *RuntimeCapacityGuarantee) { g.Digest += "x" },
		"unknown_id":     func(g *RuntimeCapacityGuarantee) { g.ID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			ref := M1SharedVolumeCapacityGuarantee()
			change(&ref)
			if err := ref.Validate(); err == nil {
				t.Fatalf("unsupported contract accepted: %+v", ref)
			}
			if _, err := RuntimeCapacityGuaranteeDefinitionFor(ref); err == nil || err.Error() != "unsupported runtime capacity guarantee" {
				t.Fatalf("unsupported definition error=%v", err)
			}
		})
	}
}

func TestRuntimeCapacityGuaranteeDecodedContract(t *testing.T) {
	var got struct {
		ID                            string `json:"id"`
		Revision                      uint64 `json:"revision"`
		DeploymentProfile             string `json:"deployment_profile"`
		ResourceProfileID             string `json:"resource_profile_id"`
		AggregatePercent              int    `json:"aggregate_percent"`
		StorageAccounting             string `json:"storage_accounting"`
		GuestFutureMetadataUpperBound bool   `json:"guest_future_metadata_upper_bound"`
		TargetInodeZero               string `json:"target_inode_zero"`
		HostBackingSafetyRequired     bool   `json:"host_backing_safety_required"`
		Authority                     string `json:"authority"`
	}
	if err := json.Unmarshal(RuntimeCapacityGuaranteeDefinition(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "m1_accounted_capacity_v1" || got.Revision != 1 || got.DeploymentProfile != "single_user_local_v1" || got.ResourceProfileID != "local_standard_v1" || got.AggregatePercent != 80 || got.StorageAccounting != "remaining_byte_commitment" || got.GuestFutureMetadataUpperBound || got.TargetInodeZero != "no_proven_inode_bound" || !got.HostBackingSafetyRequired || got.Authority != "original_pg_capacity_owner" {
		t.Fatalf("decoded deployment contract changed: %+v", got)
	}
}

func TestRuntimeCapacityGuaranteeDetachedValues(t *testing.T) {
	body := RuntimeCapacityGuaranteeDefinition()
	body[0] = 'x'
	if string(RuntimeCapacityGuaranteeDefinition()) != capacityGuaranteeGolden {
		t.Fatal("definition shared mutable bytes")
	}
	guarantee := M1AccountedCapacityGuarantee()
	guarantee.ID, guarantee.Revision, guarantee.Digest = "changed", 99, "changed"
	if got := M1AccountedCapacityGuarantee(); got.ID != "m1_accounted_capacity_v1" || got.Revision != 1 || got.Digest != capacityGuaranteeDigestGolden || got.Validate() != nil {
		t.Fatalf("getter shared mutated contract: %+v", got)
	}
}

func TestRuntimeCapacityGuaranteeRejectsUnknownContract(t *testing.T) {
	for name, change := range map[string]func(*RuntimeCapacityGuarantee){
		"empty_id":         func(g *RuntimeCapacityGuarantee) { g.ID = "" },
		"unknown_id":       func(g *RuntimeCapacityGuarantee) { g.ID = "other" },
		"zero_revision":    func(g *RuntimeCapacityGuarantee) { g.Revision = 0 },
		"wrong_revision":   func(g *RuntimeCapacityGuarantee) { g.Revision = 2 },
		"max_revision":     func(g *RuntimeCapacityGuarantee) { g.Revision = ^uint64(0) },
		"empty_digest":     func(g *RuntimeCapacityGuarantee) { g.Digest = "" },
		"zero_digest":      func(g *RuntimeCapacityGuarantee) { g.Digest = "sha256:" + strings.Repeat("0", 64) },
		"tampered_digest":  func(g *RuntimeCapacityGuarantee) { g.Digest = g.Digest[:len(g.Digest)-1] + "e" },
		"wrongcase_digest": func(g *RuntimeCapacityGuarantee) { g.Digest = strings.ToUpper(g.Digest) },
		"newline_digest":   func(g *RuntimeCapacityGuarantee) { g.Digest += "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			g := M1AccountedCapacityGuarantee()
			change(&g)
			if err := g.Validate(); err == nil {
				t.Fatalf("unsupported contract accepted: %+v", g)
			}
		})
	}
	if err := (RuntimeCapacityGuarantee{}).Validate(); err == nil {
		t.Fatal("zero value became a known contract")
	}
}
