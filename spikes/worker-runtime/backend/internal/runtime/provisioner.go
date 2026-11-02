package runtime

import (
	"context"
	"fmt"
)

// RuntimeIdentity is the server-issued identity carried by one container.
type RuntimeIdentity struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	RuntimeGeneration uint32
}

// ProvisionRequest contains only trusted server-side identities, credentials,
// and paths. It must never be used as an HTTP input.
type ProvisionRequest struct {
	Identity    RuntimeIdentity
	WorkerToken string
	WorkingCopy PathPair
	AgentState  PathPair
	TraceSpool  PathPair
	Bootstrap   PathPair
	ModelSecret PathPair
}

func (r ProvisionRequest) String() string {
	return fmt.Sprintf(
		"ProvisionRequest{WorkerSessionID:%q WorkerRunID:%q RuntimeInstanceID:%q RuntimeGeneration:%d WorkerToken:[REDACTED] Paths:[REDACTED]}",
		r.Identity.WorkerSessionID,
		r.Identity.WorkerRunID,
		r.Identity.RuntimeInstanceID,
		r.Identity.RuntimeGeneration,
	)
}

func (r ProvisionRequest) GoString() string {
	return r.String()
}

// Instance identifies the exact container selected for a runtime generation.
type Instance struct {
	ContainerID   string
	ContainerName string
}

// Provisioner owns isolated runtime container lifecycle side effects.
type Provisioner interface {
	Start(context.Context, ProvisionRequest) (Instance, error)
	Cleanup(context.Context, RuntimeIdentity) error
}
