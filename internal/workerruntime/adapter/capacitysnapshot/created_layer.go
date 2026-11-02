package capacitysnapshot

import "time"

// Created-layer facts are a finite observation, separate from running capacity.
// They cannot establish a capacity pool, residency, or execution permission.
const CreatedLayerVersion = "switchback.capacity.created_layer.raw.v1"
const CreatedLayerSignatureDomain = "switchback/capacity/created_layer/raw/v1\x00"
const CreatedLayerQuotaABI = "linux_v6.8_fs_disk_quota_112_le"

type CreatedLayerEnvelope struct {
	Version   string `json:"version"`
	KeyID     string `json:"key_id"`
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

type CreatedLayerFacts struct {
	SampleStartedAt time.Time         `json:"sample_started_at"`
	ObservedAt      time.Time         `json:"observed_at"`
	Identity        Identity          `json:"identity"`
	ManifestDigest  string            `json:"manifest_digest"`
	ContainerID     string            `json:"container_id"`
	RuntimeID       string            `json:"runtime_id"`
	SessionID       string            `json:"session_id"`
	WorkerRunID     string            `json:"worker_run_id"`
	Generation      uint64            `json:"generation"`
	ProfileID       string            `json:"profile_id"`
	ProfileRevision uint64            `json:"profile_revision"`
	ProfileDigest   string            `json:"profile_digest"`
	InspectSource   string            `json:"inspect_source"`
	GraphDriver     string            `json:"graph_driver"`
	State           CreatedLayerState `json:"state"`
	UpperDir        string            `json:"upper_dir"`
	Parent          CreatedLayerNode  `json:"parent"`
	Diff            CreatedLayerNode  `json:"diff"`
	Quota           CreatedLayerQuota `json:"quota"`
}

type CreatedLayerState struct {
	Status     string    `json:"status"`
	PID        uint64    `json:"pid"`
	Running    bool      `json:"running"`
	Paused     bool      `json:"paused"`
	Restarting bool      `json:"restarting"`
	Dead       bool      `json:"dead"`
	StartedAt  time.Time `json:"started_at"`
}

// Both parent quota domain and inherited diff must have observed physical identity.
// RawFSXAttr is the 28-byte little-endian FS_IOC_FSGETXATTR response, not configuration.
type CreatedLayerNode struct {
	Path               string `json:"path"`
	FSUUID             string `json:"fs_uuid"`
	MountID            uint64 `json:"mount_id"`
	Device             string `json:"device"`
	FSType             string `json:"fs_type"`
	Inode              uint64 `json:"inode"`
	DeviceMajor        uint64 `json:"device_major"`
	DeviceMinor        uint64 `json:"device_minor"`
	Mode               uint64 `json:"mode"`
	UID                uint64 `json:"uid"`
	GID                uint64 `json:"gid"`
	NoFollow           bool   `json:"no_follow"`
	NoSymlinkAncestors bool   `json:"no_symlink_ancestors"`
	MountStable        bool   `json:"mount_stable"`
	RawFSXAttr         []byte `json:"raw_fsxattr"`
}

// State fields preserve only queried Q_XGETQSTATV version/flags, using the
// existing enforcement format. No qfs_nblks or statfs quantity is inferred.
// Target and Default0 preserve the entire Q_XGETQUOTA ABI, including count +
// inflight reservations per exact142. Reads across records are not atomic.
type CreatedLayerQuota struct {
	Format       string `json:"format"`
	StateSource  string `json:"state_source"`
	StateVersion uint64 `json:"state_version"`
	StateFlags   uint64 `json:"state_flags"`
	QuotaSource  string `json:"quota_source"`
	ABI          string `json:"abi"`
	FSUUID       string `json:"fs_uuid"`
	MountID      uint64 `json:"mount_id"`
	Device       string `json:"device"`
	Target       []byte `json:"target"`
	Default0     []byte `json:"default0"`
}
