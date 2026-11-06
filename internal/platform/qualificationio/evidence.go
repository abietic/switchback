// Package qualificationio defines raw, untrusted qualification helper evidence.
// These values never grant capacity or stand in for host observations. A native
// producer must observe the live helper independently before encoding a receipt.
package qualificationio

import "github.com/abietic/switchback/internal/platform/config"

// ELF binds the actual attempted file to an explicit frozen build artifact.
// Header is the complete 52-byte ELF32 or 64-byte ELF64 header; Digest hashes
// the entire executable, not just Header. Class and Machine are decoded again.
type ELF struct {
	Artifact string `json:"artifact"`
	Digest   string `json:"digest"`
	Header   []byte `json:"header"`
	Class    uint64 `json:"class"`
	Machine  uint64 `json:"machine"`
}

// ProcSnapshot contains actual raw proc bytes, not normalized status summaries.
// Stat is the process stat; ThreadStat and Status belong to the exact task TID.
type ProcSnapshot struct {
	Stat       []byte `json:"stat"`
	ThreadStat []byte `json:"thread_stat"`
	Status     []byte `json:"status"`
	Cgroup     []byte `json:"cgroup"`
	MountInfo  []byte `json:"mountinfo"`
	ExeDigest  string `json:"exe_digest"`
}

// ProcessEvidence explicitly separates guest-host proc observations made by the
// trusted producer from namespace-local bytes made by the live helper. PID is
// the host process ID (equal to TGID); TID may differ. Namespace IDs are matched
// through NSpid/NStgid, never by assuming host and namespace PIDs are equal.
type ProcessEvidence struct {
	PID              uint64       `json:"pid"`
	TID              uint64       `json:"tid"`
	TGID             uint64       `json:"tgid"`
	StartTicks       uint64       `json:"start_ticks"`
	ThreadStartTicks uint64       `json:"thread_start_ticks"`
	NamespacePID     uint64       `json:"namespace_pid"`
	NamespaceTID     uint64       `json:"namespace_tid"`
	NamespaceTGID    uint64       `json:"namespace_tgid"`
	Boot             string       `json:"boot"`
	HostBefore       ProcSnapshot `json:"host_before"`
	HostAfter        ProcSnapshot `json:"host_after"`
	NamespaceBefore  ProcSnapshot `json:"namespace_before"`
	NamespaceAfter   ProcSnapshot `json:"namespace_after"`
}

// FDIdentity is read from an actual held FD. Statx is the full 256-byte Linux
// statx buffer, and FSXAttr is the actual 28-byte FS_IOC_FSGETXATTR buffer.
// MountID is local to the observation's mount namespace.
type FDIdentity struct {
	FD        uint64 `json:"fd"`
	Path      string `json:"path"`
	Device    string `json:"device"`
	FSUUID    string `json:"fs_uuid"`
	Inode     uint64 `json:"inode"`
	MountID   uint64 `json:"mount_id"`
	Owner     uint64 `json:"owner"`
	Mode      uint64 `json:"mode"`
	ProjectID uint64 `json:"project_id"`
	Statx     []byte `json:"statx"`
	FSXAttr   []byte `json:"fsxattr"`
}

type FDProof struct {
	HostMountInfoBefore []byte     `json:"host_mountinfo_before"`
	HostMountInfoAfter  []byte     `json:"host_mountinfo_after"`
	HostBefore          FDIdentity `json:"host_before"`
	HostAfter           FDIdentity `json:"host_after"`
	NamespaceBefore     FDIdentity `json:"namespace_before"`
	NamespaceAfter      FDIdentity `json:"namespace_after"`
}

// Ioctl preserves the actual request, complete syscall buffer and errno.
type Ioctl struct {
	Request uint64 `json:"request"`
	Buffer  []byte `json:"buffer"`
	Errno   uint64 `json:"errno"`
}

type SetterIO struct {
	BeforeFSXAttr  Ioctl  `json:"before_fsxattr"`
	AfterFSXAttr   Ioctl  `json:"after_fsxattr"`
	BeforeGetFlags Ioctl  `json:"before_get_flags"`
	AfterGetFlags  Ioctl  `json:"after_get_flags"`
	Set            Ioctl  `json:"set"`
	ReadBytes      uint64 `json:"read_bytes"`
	WriteBytes     uint64 `json:"write_bytes"`
}

// SetterFacts contains actual FD and syscall facts only after exec succeeds.
type SetterFacts struct {
	TargetFD FDProof  `json:"target_fd"`
	IOFD     FDProof  `json:"io_fd"`
	IO       SetterIO `json:"io"`
}

// SetterExecution has one executor and one Facts entry after successful exec.
// Actual ENOEXEC has empty Executors/Facts; ELF and the parent errno still bind
// the attempted real compat helper, and stdout can be empty.
type SetterExecution struct {
	ELF              ELF               `json:"elf"`
	Executors        []ProcessEvidence `json:"executors"`
	Facts            []SetterFacts     `json:"facts"`
	ExecErrno        uint64            `json:"exec_errno"`
	ExecOutput       []byte            `json:"exec_output"`
	UnsupportedCause string            `json:"unsupported_cause"`
}

// WorkerParent is untrusted, frozen parent identity data; it grants no authority.
type WorkerParent struct {
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

type SetterCase struct {
	Worker        WorkerParent    `json:"worker"`
	ParentProcess ProcessEvidence `json:"parent_process"`
	Architecture  string          `json:"architecture"`
	ABI           string          `json:"abi"`
	Command       string          `json:"command"`
	Execution     SetterExecution `json:"execution"`
}

// SetterPayload is raw helper/producer material before receipt deduplication.
type SetterPayload struct {
	HostTestRoot string       `json:"host_test_root"`
	ABIs         []SetterCase `json:"abis"`
}
