package capacityprobe

import (
	"context"
	"time"
)

// CreatedLayerTarget selects exactly one existing container; it carries no path or authority.
type CreatedLayerTarget struct{ ContainerID string }

// CreatedLayerSources is a deployment/test seam, never a Task-controlled endpoint.
// Physical must read only the supplied, validated actual inspect UpperDir. A nonnil
// partial observation is retained on error. Implementations must honor ctx.
type CreatedLayerSources struct {
	Get      func(context.Context, string) ([]byte, error)
	Physical func(context.Context, string) (*CreatedLayerPhysical, error)
	Now      func() time.Time
}

// CreatedLayerIdentity is the metadata returned by directory FD reads.
// Device numbers and XFSUUID are distinct: statfs.Fsid is not a filesystem UUID.
type CreatedLayerIdentity struct {
	CanonicalPath string   `json:"canonical_path"`
	Inode         uint64   `json:"inode"`
	DeviceMajor   uint32   `json:"device_major"`
	DeviceMinor   uint32   `json:"device_minor"`
	Mode          uint32   `json:"mode"`
	UID           uint32   `json:"uid"`
	GID           uint32   `json:"gid"`
	MountID       uint64   `json:"mount_id"`
	XFSUUID       [16]byte `json:"xfs_uuid"`
}

// Raw buffers retain the Linux little-endian arm64/amd64 ABI without interpreting
// limits or enforcement. Zero/off values must stay visible to a future checker.
type CreatedLayerDirectory struct {
	Before               CreatedLayerIdentity `json:"before"`
	After                CreatedLayerIdentity `json:"after"`
	Geometry             [256]byte            `json:"geometry"`
	GeometryAfter        [256]byte            `json:"geometry_after"`
	FSXAttrAfter         [28]byte             `json:"fsxattr_after"`
	FSXAttr              [28]byte             `json:"fsxattr"`
	NoFollowWalk         bool                 `json:"nofollow_walk"`
	ReopenedNoFollow     bool                 `json:"reopened_nofollow"`
	IdentityRechecked    bool                 `json:"identity_rechecked"`
	FilesystemUUIDSameFD bool                 `json:"filesystem_uuid_same_fd"`
}

// CreatedLayerPhysical is a finite observation, not an authorization or admission
// object. The Linux adapter owns syscall completion and actual identity rechecks.
// GET quota ioctls can have internal kernel effects; these are not mutation ioctls.
type CreatedLayerPhysical struct {
	Root                    CreatedLayerDirectory `json:"root"`
	Parent                  CreatedLayerDirectory `json:"parent"`
	Diff                    CreatedLayerDirectory `json:"diff"`
	QStatV                  [160]byte             `json:"qstatv"`
	TargetQuota             [112]byte             `json:"target_quota"`
	DefaultQuota            [112]byte             `json:"default0_quota"`
	ProjectID               uint32                `json:"project_id"`
	BootIDBefore            [16]byte              `json:"boot_id_before"`
	BootIDAfter             [16]byte              `json:"boot_id_after"`
	MountNamespaceBefore    uint64                `json:"mount_namespace_before"`
	MountNamespaceAfter     uint64                `json:"mount_namespace_after"`
	MountInfoSHA256Before   [32]byte              `json:"mountinfo_sha256_before"`
	MountInfoSHA256After    [32]byte              `json:"mountinfo_sha256_after"`
	MountInfoRechecked      bool                  `json:"mountinfo_rechecked"`
	RootXFSVerified         bool                  `json:"root_xfs_verified"`
	SameMountVerified       bool                  `json:"same_mount_verified"`
	BootIdentityRechecked   bool                  `json:"boot_identity_rechecked"`
	MountNamespaceRechecked bool                  `json:"mount_namespace_rechecked"`
	ObservationComplete     bool                  `json:"observation_complete"`
	Samples                 []CreatedLayerSample  `json:"samples"`
	SamplesTruncated        bool                  `json:"samples_truncated"`
}

type CreatedLayerSample struct {
	Sequence  int       `json:"sequence"`
	Operation string    `json:"operation"`
	Source    string    `json:"source"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	Outcome   string    `json:"outcome"`
	Errno     *uint64   `json:"errno,omitempty"`
}

type CreatedLayerEngine struct {
	ID         string `json:"id"`
	APIVersion string `json:"api_version"`
	Version    string `json:"version"`
	DockerRoot string `json:"docker_root"`
	Driver     string `json:"driver"`
	Source     string `json:"source"`
}

type CreatedLayerInspect struct {
	ID         string `json:"id"`
	Image      string `json:"image,omitempty"`
	Status     string `json:"status"`
	PID        int64  `json:"pid"`
	Running    bool   `json:"running"`
	Paused     bool   `json:"paused"`
	Restarting bool   `json:"restarting"`
	Dead       bool   `json:"dead"`
	StartedAt  string `json:"started_at"`
	Driver     string `json:"driver"`
	UpperDir   string `json:"upper_dir"`
	ParentDir  string `json:"parent_dir"`
	Source     string `json:"source"`
}

// ObservationComplete reports only finite requested reads and rechecks. It never
// proves quota compliance, absence of ABA/external writers, or all-time capacity.
type CreatedLayerCollection struct {
	Schema              string                `json:"schema"`
	Status              string                `json:"status"`
	Trust               string                `json:"trust"`
	ExecutionEnabled    bool                  `json:"execution_enabled"`
	CapacityReady       bool                  `json:"capacity_ready"`
	ObservationComplete bool                  `json:"observation_complete"`
	Started             time.Time             `json:"started"`
	Ended               time.Time             `json:"ended"`
	Engine              *CreatedLayerEngine   `json:"engine,omitempty"`
	Inspect             *CreatedLayerInspect  `json:"inspect,omitempty"`
	Physical            *CreatedLayerPhysical `json:"physical,omitempty"`
	Samples             []CreatedLayerSample  `json:"samples"`
	Blockers            []string              `json:"blockers"`
}
