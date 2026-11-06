package capacitysnapshot

import (
	"bytes"
	"encoding/json"
	"time"
)

const VersionV2 = "switchback.capacity.raw.v2"
const SignatureDomainV2 = "switchback/capacity/raw/v2\x00"

// V2 is an independent offline evidence schema; it has no admission authority.
// Inode counts are GET counters including reservations, not bytes or a physical bound.
type StorageQuotaV2 struct {
	HardInodes    uint64 `json:"hard_inodes"`
	UsedInodes    uint64 `json:"used_inodes"`
	Format        string `json:"format"`
	StateVersion  uint64 `json:"state_version"`
	StateFlags    uint64 `json:"state_flags"`
	Version       uint64 `json:"version"`
	Flags         uint64 `json:"flags"`
	ProjectID     uint64 `json:"project_id"`
	HardBlocks512 uint64 `json:"hard_blocks_512"`
	UsedBlocks512 uint64 `json:"used_blocks_512"`
}
type StorageDomainV2 struct {
	FSUUID  string `json:"fs_uuid"`
	Device  string `json:"device"`
	MountID uint64 `json:"mount_id"`
	FSType  string `json:"fs_type"`
	// FS_IOC_FSGETXATTR directory fsx_xflags and fsx_projid (ProjectID).
	XFlags    uint64         `json:"xflags"`
	Quota     StorageQuotaV2 `json:"quota"`
	ID        string         `json:"id"`
	Path      string         `json:"path"`
	ProjectID uint64         `json:"project_id"`
	HardBytes uint64         `json:"hard_bytes"`
	UsedBytes uint64         `json:"used_bytes"`
}

type ContainerV2 struct {
	ContainerID     string          `json:"container_id"`
	RuntimeID       string          `json:"runtime_id"`
	SessionID       string          `json:"session_id"`
	Generation      uint64          `json:"generation"`
	ProfileID       string          `json:"profile_id"`
	ProfileRevision uint64          `json:"profile_revision"`
	ProfileDigest   string          `json:"profile_digest"`
	NanoCPUs        uint64          `json:"nano_cpus"`
	MemoryMax       uint64          `json:"memory_max"`
	PIDsMax         uint64          `json:"pids_max"`
	Layer           StorageDomainV2 `json:"layer"`
	CgroupPath      string          `json:"cgroup_path"`
	CPUQuotaMicros  uint64          `json:"cpu_quota_micros"`
	CPUPeriodMicros uint64          `json:"cpu_period_micros"`
}

type FactsV2 struct {
	Sequence          int64             `json:"sequence"`
	ObservedAt        time.Time         `json:"observed_at"`
	Identity          Identity          `json:"identity"`
	ManifestDigest    string            `json:"manifest_digest"`
	EngineCPUMilli    uint64            `json:"engine_cpu_milli"`
	EngineMemoryBytes uint64            `json:"engine_memory_bytes"`
	Anchor            Anchor            `json:"anchor"`
	Core              []CoreDomain      `json:"core"`
	Storage           []StorageDomainV2 `json:"storage"`
	Containers        []ContainerV2     `json:"containers"`
	Removals          []Removal         `json:"removals"`
}

// VerifiedEvidenceV2 retains signed v2 bytes without a conversion to v1 Verified.
type VerifiedEvidenceV2 struct {
	envelope Envelope
	facts    FactsV2
	raw      []byte
	digest   string
}

func (v VerifiedEvidenceV2) Raw() []byte       { return bytes.Clone(v.raw) }
func (v VerifiedEvidenceV2) Payload() []byte   { return bytes.Clone(v.envelope.Payload) }
func (v VerifiedEvidenceV2) Signature() []byte { return bytes.Clone(v.envelope.Signature) }
func (v VerifiedEvidenceV2) Digest() string    { return v.digest }
func (v VerifiedEvidenceV2) Facts() FactsV2 {
	var f FactsV2
	_ = json.Unmarshal(v.envelope.Payload, &f)
	return f
}
