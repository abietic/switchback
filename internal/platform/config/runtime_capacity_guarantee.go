package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const M1AccountedCapacityV1 = "m1_accounted_capacity_v1"
const m1AccountedCapacityDefinition = `{"id":"m1_accounted_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":true,"authority":"original_pg_capacity_owner"}`
const M1SharedVolumeCapacityV1 = "m1_shared_volume_capacity_v1"
const m1SharedVolumeCapacityDefinition = `{"id":"m1_shared_volume_capacity_v1","revision":1,"deployment_profile":"single_user_local_v1","resource_profile_id":"local_standard_v1","aggregate_percent":80,"storage_accounting":"remaining_byte_commitment","guest_future_metadata_upper_bound":false,"target_inode_zero":"no_proven_inode_bound","host_backing_safety_required":false,"host_storage_boundary":"apfs_volume_quota_and_reserve_v1","host_future_container_metadata_upper_bound":false,"authority":"original_pg_capacity_owner"}`

// RuntimeCapacityGuarantee is contract metadata, not a capacity observation or grant.
type RuntimeCapacityGuarantee struct {
	ID       string
	Revision uint64
	Digest   string
}

func M1AccountedCapacityGuarantee() RuntimeCapacityGuarantee {
	digest := sha256.Sum256([]byte(m1AccountedCapacityDefinition))
	return RuntimeCapacityGuarantee{ID: M1AccountedCapacityV1, Revision: 1, Digest: "sha256:" + hex.EncodeToString(digest[:])}
}

func M1SharedVolumeCapacityGuarantee() RuntimeCapacityGuarantee {
	digest := sha256.Sum256([]byte(m1SharedVolumeCapacityDefinition))
	return RuntimeCapacityGuarantee{ID: M1SharedVolumeCapacityV1, Revision: 1, Digest: "sha256:" + hex.EncodeToString(digest[:])}
}

func RuntimeCapacityGuaranteeDefinition() []byte {
	return []byte(m1AccountedCapacityDefinition)
}

func RuntimeCapacityGuaranteeDefinitionFor(ref RuntimeCapacityGuarantee) ([]byte, error) {
	switch ref {
	case M1AccountedCapacityGuarantee():
		return []byte(m1AccountedCapacityDefinition), nil
	case M1SharedVolumeCapacityGuarantee():
		return []byte(m1SharedVolumeCapacityDefinition), nil
	default:
		return nil, fmt.Errorf("unsupported runtime capacity guarantee")
	}
}

func (g RuntimeCapacityGuarantee) Validate() error {
	if g != M1AccountedCapacityGuarantee() && g != M1SharedVolumeCapacityGuarantee() {
		return fmt.Errorf("unsupported runtime capacity guarantee")
	}
	return nil
}
