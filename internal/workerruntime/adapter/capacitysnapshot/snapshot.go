// Package capacitysnapshot verifies deployment-owned raw capacity observations.
// No type in this package grants execution permission; only the PG owner can.
package capacitysnapshot

import (
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const Version = "switchback.capacity.raw.v1"
const MaxBytes = 1 << 20
const SignatureDomain = "switchback/capacity/raw/v1\x00"

type Envelope struct {
	Version   string `json:"version"`
	KeyID     string `json:"key_id"`
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}
type Identity struct {
	EngineID       string `json:"engine_id"`
	DockerRoot     string `json:"docker_root"`
	FSUUID         string `json:"fs_uuid"`
	GuestBootID    string `json:"guest_boot_id"`
	MountNamespace string `json:"mount_namespace"`
	MountID        uint64 `json:"mount_id"`
	Device         string `json:"device"`
}

// Trust is server deployment configuration, never accepted from TaskSpec or the
// signed file itself. Every bounded storage/cgroup domain is pinned here.
type Trust struct {
	KeyID           string                          `json:"key_id"`
	PublicKey       []byte                          `json:"public_key"`
	ManifestDigest  string                          `json:"manifest_digest"`
	Identity        Identity                        `json:"identity"`
	AnchorPath      string                          `json:"anchor_path"`
	Core            []CoreDomain                    `json:"core"`
	Storage         []StorageDomain                 `json:"storage"`
	Coverage        []StorageCoverage               `json:"coverage"`
	AdmissionPolicy *workerapp.CapacityGuaranteeRef `json:"admission_policy,omitempty"`
}

// StorageCoverage maps a required coverage role to one physical quota domain.
// Only deployment trust owns this mapping; raw samples cannot select a layout.
type StorageCoverage struct {
	Role     string `json:"role"`
	DomainID string `json:"domain_id"`
}
type CoreDomain struct {
	ID              string `json:"id"`
	CgroupPath      string `json:"cgroup_path"`
	CPUQuotaMicros  uint64 `json:"cpu_quota_micros"`
	CPUPeriodMicros uint64 `json:"cpu_period_micros"`
	MemoryMax       uint64 `json:"memory_max"`
	PIDsMax         uint64 `json:"pids_max"`
}

// StorageQuota is the readback of Q_XGETQSTATV (StateVersion/StateFlags)
// and Q_XGETQUOTA (Version/Flags/ProjectID/HardBlocks512/UsedBlocks512),
// both issued for the domain device with XQM_PRJQUOTA. Linux dqblk_xfs.h
// defines these quota block quantities as 512-byte basic blocks, not statfs
// blocks. The probe must not substitute configured limits for these results.
type StorageQuota struct {
	Format        string `json:"format"`
	StateVersion  uint64 `json:"state_version"`
	StateFlags    uint64 `json:"state_flags"`
	Version       uint64 `json:"version"`
	Flags         uint64 `json:"flags"`
	ProjectID     uint64 `json:"project_id"`
	HardBlocks512 uint64 `json:"hard_blocks_512"`
	UsedBlocks512 uint64 `json:"used_blocks_512"`
}
type StorageDomain struct {
	FSUUID  string `json:"fs_uuid"`
	Device  string `json:"device"`
	MountID uint64 `json:"mount_id"`
	FSType  string `json:"fs_type"`
	// FS_IOC_FSGETXATTR directory fsx_xflags and fsx_projid (ProjectID).
	XFlags    uint64       `json:"xflags"`
	Quota     StorageQuota `json:"quota"`
	ID        string       `json:"id"`
	Path      string       `json:"path"`
	ProjectID uint64       `json:"project_id"`
	HardBytes uint64       `json:"hard_bytes"`
	UsedBytes uint64       `json:"used_bytes"`
}
type Anchor struct {
	Path           string   `json:"path"`
	FSUUID         string   `json:"fs_uuid"`
	MountID        uint64   `json:"mount_id"`
	Device         string   `json:"device"`
	FSType         string   `json:"fs_type"`
	MountOptions   []string `json:"mount_options"`
	ProjectID      uint64   `json:"project_id"`
	XFlags         uint64   `json:"xflags"`
	AvailableBytes uint64   `json:"available_bytes"`
}
type Container struct {
	ContainerID     string        `json:"container_id"`
	RuntimeID       string        `json:"runtime_id"`
	SessionID       string        `json:"session_id"`
	Generation      uint64        `json:"generation"`
	ProfileID       string        `json:"profile_id"`
	ProfileRevision uint64        `json:"profile_revision"`
	ProfileDigest   string        `json:"profile_digest"`
	NanoCPUs        uint64        `json:"nano_cpus"`
	MemoryMax       uint64        `json:"memory_max"`
	PIDsMax         uint64        `json:"pids_max"`
	Layer           StorageDomain `json:"layer"`
	CgroupPath      string        `json:"cgroup_path"`
	CPUQuotaMicros  uint64        `json:"cpu_quota_micros"`
	CPUPeriodMicros uint64        `json:"cpu_period_micros"`
}
type Removal struct {
	EngineID, ContainerID, RuntimeID, SessionID, SecretVolumeID string
	Generation, ChannelEpoch                                    uint64
	ContainerInspectHTTPStatus, SecretVolumeInspectHTTPStatus   uint64
}
type Facts struct {
	Sequence          int64           `json:"sequence"`
	ObservedAt        time.Time       `json:"observed_at"`
	Identity          Identity        `json:"identity"`
	ManifestDigest    string          `json:"manifest_digest"`
	EngineCPUMilli    uint64          `json:"engine_cpu_milli"`
	EngineMemoryBytes uint64          `json:"engine_memory_bytes"`
	Anchor            Anchor          `json:"anchor"`
	Core              []CoreDomain    `json:"core"`
	Storage           []StorageDomain `json:"storage"`
	Containers        []Container     `json:"containers"`
	Removals          []Removal       `json:"removals"`
}

// Verified fields remain private so consumers cannot construct a verified fact.
// Bytes and Facts return copies, retaining raw signature semantics.
type Verified struct {
	envelope Envelope
	facts    Facts
	raw      []byte
	digest   string
}
