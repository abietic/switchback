package capacityprobe

import (
	"errors"
	"github.com/abietic/switchback/internal/platform/capacityprobe/procstatus"
	"time"
)

// DeploymentObservation contains unsigned, per-invocation diagnostic facts only.
// It cannot authorize capacity admission or verify a different principal.
type DeploymentObservation struct {
	Schema        string                            `json:"schema"`
	Kind          string                            `json:"kind"`
	Started       time.Time                         `json:"started"`
	Ended         time.Time                         `json:"ended"`
	BootBefore    string                            `json:"boot_before"`
	BootAfter     string                            `json:"boot_after"`
	KernelRelease string                            `json:"kernel_release"`
	CapLastCap    uint32                            `json:"cap_last_cap"`
	CallerBefore  DeploymentProcessObservation      `json:"caller_before"`
	CallerAfter   DeploymentProcessObservation      `json:"caller_after"`
	PID1Before    DeploymentProcessObservation      `json:"pid1_before"`
	PID1After     DeploymentProcessObservation      `json:"pid1_after"`
	Namespaces    [8]DeploymentNamespaceObservation `json:"namespaces"`
}

type DeploymentProcessObservation = procstatus.Status

type DeploymentNamespaceObservation struct {
	Owner  string                   `json:"owner"`
	Kind   string                   `json:"kind"`
	Before DeploymentNamespaceValue `json:"before"`
	After  DeploymentNamespaceValue `json:"after"`
}

type DeploymentNamespaceValue struct {
	Device string `json:"device"`
	Inode  string `json:"inode"`
	Errno  uint32 `json:"errno"`
	Class  string `json:"class"`
}

var (
	ErrDeploymentUnavailable = errors.New("deployment diagnostic unavailable")
	ErrDeploymentInvalid     = errors.New("deployment diagnostic invalid")
	ErrDeploymentCancelled   = errors.New("deployment diagnostic cancelled")
	ErrDeploymentUnsupported = errors.New("deployment diagnostic unsupported")
)
