package docker

import (
	"fmt"
	"sync"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

// A handoff is only the exact Create identity whose final policy inspection and
// pre-Start authority check succeeded in this process. It is not durable owner,
// capacity or credential authority. This lock is independent of Provisioner's
// lifecycle lock: ContainerStart may synchronously cause the first control Claim.
type workerPolicyHandoffs struct {
	mu         sync.RWMutex
	identities map[string]application.CreatedRuntimeIdentity
}

func (h *workerPolicyHandoffs) publish(identity application.CreatedRuntimeIdentity) error {
	if err := validatePolicyHandoffIdentity(identity); err != nil {
		return err
	}
	if identity.ContainerID == "" {
		return fmt.Errorf("Worker policy handoff requires exact Create CID")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.identities[identity.RuntimeInstanceID]; ok && existing != identity {
		return fmt.Errorf("Worker policy Create handoff identity conflict")
	}
	if h.identities == nil {
		h.identities = make(map[string]application.CreatedRuntimeIdentity)
	}
	h.identities[identity.RuntimeInstanceID] = identity
	return nil
}

// LookupWorkerPolicyHandoff returns a value copy, never consumes the handoff,
// and never inspects a container name to manufacture one after Backend restart.
// An absent handoff may be supplemented by independent durable CID evidence.
func (p *Provisioner) LookupWorkerPolicyHandoff(target application.CreatedRuntimeIdentity) (application.CreatedRuntimeIdentity, bool, error) {
	if p == nil {
		return application.CreatedRuntimeIdentity{}, false, fmt.Errorf("Worker policy handoff source is unavailable")
	}
	if err := validatePolicyHandoffIdentity(target); err != nil {
		return application.CreatedRuntimeIdentity{}, false, err
	}
	p.policyHandoffs.mu.RLock()
	defer p.policyHandoffs.mu.RUnlock()
	identity, ok := p.policyHandoffs.identities[target.RuntimeInstanceID]
	if !ok {
		return application.CreatedRuntimeIdentity{}, false, nil
	}
	if identity.WorkerSessionID != target.WorkerSessionID || identity.Generation != target.Generation || (target.ContainerID != "" && target.ContainerID != identity.ContainerID) {
		return application.CreatedRuntimeIdentity{}, false, fmt.Errorf("Worker policy Create handoff identity conflict")
	}
	return identity, true, nil
}

// Only the trusted Cleanup caller invokes this after confirming removal. An
// unknown/failed Start never clears identity. Without an exact requested or
// observed CID, even a name-based absence observation cannot clear a handoff.
func (h *workerPolicyHandoffs) clearConfirmed(request application.RuntimeCleanupRequest, observedCID string) {
	cid := request.ExpectedContainerID
	if cid == "" {
		cid = observedCID
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	identity, ok := h.identities[request.RuntimeInstanceID]
	if ok && cid != "" && identity.ContainerID == cid && identity.Generation == request.Generation {
		delete(h.identities, request.RuntimeInstanceID)
	}
}

func validatePolicyHandoffIdentity(identity application.CreatedRuntimeIdentity) error {
	for name, value := range map[string]string{"runtime instance id": identity.RuntimeInstanceID, "worker session id": identity.WorkerSessionID} {
		if err := validateIdentity(name, value); err != nil {
			return err
		}
	}
	if identity.Generation == 0 {
		return fmt.Errorf("Worker policy handoff generation is required")
	}
	if identity.ContainerID != "" {
		return validateIdentity("container id", identity.ContainerID)
	}
	return nil
}
