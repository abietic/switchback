package capacitysnapshot

import "github.com/abietic/switchback/internal/platform/config"

// These types deliberately remain private: receipts are replayable claims, not
// capability tokens. Only fixed safe files can construct a production handle.

type DeploymentCoreBinding struct {
	ContainerID string `json:"container_id"`
	ImageDigest string `json:"image_digest"`
	CoreID      string `json:"core_id"`
}
type DeploymentPublisherBinding struct {
	Core              []DeploymentCoreBinding
	WorkerImageDigest string
}
type qualificationBinding struct {
	Schema            string                  `json:"schema"`
	ManifestDigest    string                  `json:"manifest_digest"`
	Core              []DeploymentCoreBinding `json:"core"`
	WorkerImageDigest string                  `json:"worker_image_digest"`
}
type qualificationSource struct {
	Schema string                    `json:"schema"`
	Files  []qualificationSourceFile `json:"files"`
	Builds []qualificationBuild      `json:"builds"`
	Inputs qualificationInputs       `json:"inputs"`
}
type qualificationSourceFile struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   uint64 `json:"mode"`
	Bytes  uint64 `json:"bytes"`
	Digest string `json:"digest"`
}
type qualificationBuild struct {
	Role         string `json:"role"`
	Toolchain    string `json:"toolchain"`
	InputRoot    string `json:"input_root"`
	BinaryDigest string `json:"binary_digest"`
	ImageDigest  string `json:"image_digest"`
}
type qualificationDerived struct {
	Reference string `json:"reference"`
}
type qualificationInputs struct {
	Architecture      string                  `json:"architecture"`
	Schema            string                  `json:"schema"`
	TrustManifest     qualificationDerived    `json:"trust_manifest"`
	PublisherManifest qualificationDerived    `json:"publisher_manifest"`
	ObserverManifest  qualificationDerived    `json:"observer_manifest"`
	Roles             []qualificationRole     `json:"roles"`
	Host              qualificationHostTarget `json:"host"`
	Maintenance       string                  `json:"maintenance"`
}
type qualificationRole struct {
	Kind            string               `json:"kind"`
	Role            string               `json:"role"`
	ContainerID     string               `json:"container_id"`
	ImageDigest     string               `json:"image_digest"`
	BinaryDigest    string               `json:"binary_digest"`
	CoreID          string               `json:"core_id"`
	CgroupPath      string               `json:"cgroup_path"`
	UID             uint64               `json:"uid"`
	Capabilities    uint64               `json:"capabilities"`
	NoNewPrivileges uint64               `json:"no_new_privileges"`
	Seccomp         uint64               `json:"seccomp"`
	Mounts          []qualificationMount `json:"mounts"`
	Environment     []qualificationEnv   `json:"environment"`
}
type qualificationMount struct {
	Device   string `json:"device"`
	Root     string `json:"root"`
	MountID  uint64 `json:"mount_id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}
type qualificationEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type qualificationHostTarget struct {
	VolumeUUID        string                `json:"volume_uuid"`
	ContainerUUID     string                `json:"container_uuid"`
	PhysicalStoreUUID string                `json:"physical_store_uuid"`
	PhysicalStore     string                `json:"physical_store"`
	Mount             string                `json:"mount"`
	QuotaBytes        uint64                `json:"quota_bytes"`
	ReserveBytes      uint64                `json:"reserve_bytes"`
	Writers           []qualificationWriter `json:"writers"`
}
type qualificationWriter struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	ArtifactDigest string `json:"artifact_digest"`
	Policy         string `json:"policy"`
}
type qualificationWindow struct {
	ID              string `json:"id"`
	Started         string `json:"started"`
	Ended           string `json:"ended"`
	HostBootBefore  string `json:"host_boot_before"`
	HostBootAfter   string `json:"host_boot_after"`
	GuestBootBefore string `json:"guest_boot_before"`
	GuestBootAfter  string `json:"guest_boot_after"`
}
type qualificationProducer struct {
	Role           string `json:"role"`
	ArtifactDigest string `json:"artifact_digest"`
	BuildDigest    string `json:"build_digest"`
	InputRoot      string `json:"input_root"`
}
type qualificationRaw struct {
	WindowID string `json:"window_id"`
	Started  string `json:"started"`
	Ended    string `json:"ended"`
	Source   string `json:"source"`
	Data     []byte `json:"data"`
	Digest   string `json:"digest"`
	Exit     int64  `json:"exit"`
	Errno    uint64 `json:"errno"`
}
type qualificationCheck struct {
	ID          string           `json:"id"`
	Observation qualificationRaw `json:"observation"`
}
type qualificationHostReceipt struct {
	Schema    string                `json:"schema"`
	Kind      string                `json:"kind"`
	Lifecycle string                `json:"lifecycle"`
	Candidate string                `json:"candidate"`
	Window    qualificationWindow   `json:"window"`
	Producer  qualificationProducer `json:"producer"`
	Checks    []qualificationCheck  `json:"checks"`
}
type qualificationNativeReceipt qualificationHostReceipt

// Raw native payloads are separate strict formats; wrapper exit=0 cannot prove
// a check. Every field participates in a deterministic predicate below.
type qualificationLoaded struct {
	Worker         qualificationWorker `json:"worker"`
	Identity       Identity            `json:"identity"`
	EngineArtifact string              `json:"engine_artifact"`
	KernelArtifact string              `json:"kernel_artifact"`
	Filesystem     string              `json:"filesystem"`
	Roles          []qualificationRole `json:"roles"`
}
type qualificationNativeRole struct {
	Role         string `json:"role"`
	StatusBefore []byte `json:"status_before"`
	StatusAfter  []byte `json:"status_after"`
	CgroupBefore string `json:"cgroup_before"`
	CgroupAfter  string `json:"cgroup_after"`
	BinaryDigest string `json:"binary_digest"`
	Boot         string `json:"boot"`
}
type qualificationNamespaces struct {
	Namespaces []qualificationNamespace  `json:"namespaces"`
	Roles      []qualificationNativeRole `json:"roles"`
}
type qualificationNamespace struct {
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	AfterDevice uint64 `json:"after_device"`
	AfterInode  uint64 `json:"after_inode"`
	Errno       uint64 `json:"errno"`
}
type qualificationCore struct {
	Domains []CoreDomain            `json:"domains"`
	Members []DeploymentCoreBinding `json:"members"`
	CPU     []string                `json:"cpu"`
	Memory  []string                `json:"memory"`
	PIDs    []string                `json:"pids"`
	Swap    []string                `json:"swap"`
}
type qualificationQuotaTrial struct {
	ObjectBefore   qualificationTestObject `json:"object_before"`
	ObjectAfter    qualificationTestObject `json:"object_after"`
	ChildBefore    qualificationTestObject `json:"child_before"`
	ChildAfter     qualificationTestObject `json:"child_after"`
	Inode          uint64                  `json:"inode"`
	ChildInode     uint64                  `json:"child_inode"`
	ChildFSXAttr   []byte                  `json:"child_fsxattr"`
	Device         string                  `json:"device"`
	FSUUID         string                  `json:"fs_uuid"`
	ProjectID      uint64                  `json:"project_id"`
	Before         StorageQuota            `json:"before"`
	After          StorageQuota            `json:"after"`
	QStatV         []byte                  `json:"qstatv"`
	QuotaBefore    []byte                  `json:"quota_before"`
	QuotaAfter     []byte                  `json:"quota_after"`
	FSXAttr        []byte                  `json:"fsxattr"`
	XFlags         uint64                  `json:"xflags"`
	ChildProjectID uint64                  `json:"child_project_id"`
	RequestedBytes uint64                  `json:"requested_bytes"`
	WrittenBytes   uint64                  `json:"written_bytes"`
	Errno          uint64                  `json:"errno"`
	PoolFreeBytes  uint64                  `json:"pool_free_bytes"`
}
type qualificationQuota struct {
	Denied    qualificationQuotaTrial `json:"denied"`
	Succeeded qualificationQuotaTrial `json:"succeeded"`
}
type qualificationSetter struct {
	Worker           qualificationWorker     `json:"worker"`
	TargetBefore     qualificationTestObject `json:"target_before"`
	TargetAfter      qualificationTestObject `json:"target_after"`
	IOInode          uint64                  `json:"io_inode"`
	BeforeFSXAttr    []byte                  `json:"before_fsxattr"`
	AfterFSXAttr     []byte                  `json:"after_fsxattr"`
	BeforeGetFlags   []byte                  `json:"before_get_flags"`
	AfterGetFlags    []byte                  `json:"after_get_flags"`
	Attempt          []byte                  `json:"attempt"`
	AfterGetErrno    uint64                  `json:"after_get_errno"`
	Architecture     string                  `json:"architecture"`
	Command          string                  `json:"command"`
	ExecErrno        uint64                  `json:"exec_errno"`
	ExecOutput       []byte                  `json:"exec_output"`
	ABI              string                  `json:"abi"`
	GetErrno         uint64                  `json:"get_errno"`
	SetErrno         uint64                  `json:"set_errno"`
	BeforeProject    uint64                  `json:"before_project"`
	AfterProject     uint64                  `json:"after_project"`
	BeforeFlags      uint64                  `json:"before_flags"`
	AfterFlags       uint64                  `json:"after_flags"`
	ReadBytes        uint64                  `json:"read_bytes"`
	WriteBytes       uint64                  `json:"write_bytes"`
	UnsupportedCause string                  `json:"unsupported_cause"`
}
type qualificationHandoff struct {
	Cases []qualificationHandoffCase `json:"cases"`
}
type qualificationHandoffCase struct {
	BinaryDigest   string `json:"binary_digest"`
	Device         string `json:"device"`
	TargetInode    uint64 `json:"target_inode"`
	ChildInode     uint64 `json:"child_inode"`
	ChildOwner     uint64 `json:"child_owner"`
	Operation      string `json:"operation"`
	RequestedOwner uint64 `json:"requested_owner"`
	ID             string `json:"id"`
	Status         []byte `json:"status"`
	OwnerBefore    uint64 `json:"owner_before"`
	OwnerAfter     uint64 `json:"owner_after"`
	EntriesBefore  uint64 `json:"entries_before"`
	EntriesAfter   uint64 `json:"entries_after"`
	Mode           uint64 `json:"mode"`
	Errno          uint64 `json:"errno"`
}
type qualificationReachability struct {
	Observed       []qualificationMountObservation `json:"observed"`
	Processes      []qualificationProcessWriter    `json:"processes"`
	Private        []qualificationPrivateMount     `json:"private"`
	Roles          []qualificationRole             `json:"roles"`
	Writers        []DeploymentCoreBinding         `json:"writers"`
	PrivateSources []string                        `json:"private_sources"`
}
type qualificationHostPaths struct {
	Mount      string                        `json:"mount"`
	VolumeUUID string                        `json:"volume_uuid"`
	StatDevice uint64                        `json:"stat_device"`
	StatInode  uint64                        `json:"stat_inode"`
	Writers    []qualificationWriterReadback `json:"writers"`
}
type qualificationWriterReadback struct {
	ID                 string `json:"id"`
	Path               string `json:"path"`
	VolumeUUID         string `json:"volume_uuid"`
	ArtifactDigest     string `json:"artifact_digest"`
	StatDevice         uint64 `json:"stat_device"`
	StatInode          uint64 `json:"stat_inode"`
	AllocatedBlocks512 uint64 `json:"allocated_blocks_512"`
	Policy             string `json:"policy"`
}

type qualificationMountObservation struct {
	Role string `json:"role"`
	Raw  []byte `json:"raw"`
}
type qualificationProcessWriter struct {
	Role         string `json:"role"`
	PID          uint64 `json:"pid"`
	StartTime    uint64 `json:"start_time"`
	Cgroup       string `json:"cgroup"`
	BinaryDigest string `json:"binary_digest"`
}
type qualificationPrivateMount = DeploymentPrivateDirectoryBinding

// Expected read-only identity metadata copied from validated native receipts.
type DeploymentPrivateDirectoryBinding struct {
	MountID uint64 `json:"mount_id"`
	Mode    uint64 `json:"mode"`
	UID     uint64 `json:"uid"`
	GID     uint64 `json:"gid"`
	Path    string `json:"path"`
	Device  string `json:"device"`
	Root    string `json:"root"`
	Inode   uint64 `json:"inode"`
}

// Tested Worker identity is distinct from the receipt producer. PID/status and
// loaded executable belong to the process issuing the ioctls, not the observer.
type qualificationWorker struct {
	ContainerID  string                           `json:"container_id"`
	ImageDigest  string                           `json:"image_digest"`
	BinaryDigest string                           `json:"binary_digest"`
	Profile      config.RuntimeResourceProfileRef `json:"profile"`
	Boot         string                           `json:"boot"`
	PID          uint64                           `json:"pid"`
	StartTicks   uint64                           `json:"start_ticks"`
	Cgroup       string                           `json:"cgroup"`
	StatusBefore []byte                           `json:"status_before"`
	StatusAfter  []byte                           `json:"status_after"`
}
type qualificationTestObject struct {
	Device    string `json:"device"`
	FSUUID    string `json:"fs_uuid"`
	MountID   uint64 `json:"mount_id"`
	Inode     uint64 `json:"inode"`
	Owner     uint64 `json:"owner"`
	Mode      uint64 `json:"mode"`
	ProjectID uint64 `json:"project_id"`
}
