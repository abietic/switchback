package localproduct

import (
	"context"
	"path/filepath"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const initialActivationWindow = 750 * time.Millisecond
const initialActivationPoll = 25 * time.Millisecond

// Process-local continuation of one already-provisioned initializer. Losing this
// map on restart leaves the durable interrupted-owner hold; it is not recovery.
// Every field except busy/blocked is immutable once published under runner.mu.
type pendingInitialActivation struct {
	command workerapp.ActivateResidentRuntimeCommand
	ready   workerapp.WorkerControlEvent
	live    *liveRuntime
	busy    bool
	blocked error
}

func initialActivationWait(err error) bool {
	switch singleRuntimeCause(err) {
	case workerapp.CapacityUninitialized, workerapp.CapacityEvidenceInvalid,
		workerapp.CapacityEvidenceStale, workerapp.CapacityIdentityDrift,
		workerapp.CapacityInventoryUnknown, workerapp.CapacityExhausted,
		workerapp.CapacityContainerProofPending:
		return true
	}
	return false
}

func (r *runtimeRunner) beginInitialActivation(ctx context.Context, started workerapp.StartedRuntime, prepared preparedRuntime, bound workerapp.BoundAgentSession, ready runtimeReadyPayload, event workerapp.WorkerControlEvent, events <-chan workerapp.WorkerControlEvent) error {
	offset, _ := fileSize(filepath.Join(prepared.Storage.TraceSpool.BackendPath, "acp.raw.jsonl"))
	op := &pendingInitialActivation{
		command: workerapp.ActivateResidentRuntimeCommand{
			TenantID: r.configuration.Profile.TenantID, WorkerSessionID: started.Ownership.WorkerSessionID,
			WorkerRunID: started.Ownership.WorkerRunID, AgentTurnID: started.Ownership.AgentTurnID,
			RuntimeInstanceID: started.Ownership.RuntimeInstanceID, ContainerID: started.Runtime.ContainerID,
			RuntimeGeneration: started.Ownership.RuntimeGeneration, ControlChannelEpoch: started.Ownership.ControlChannelEpoch,
			ExpectedSessionRevision: bound.SessionRevision, ExpectedRunRevision: bound.RunRevision,
			ExpectedTurnRevision: bound.TurnRevision, ExpectedRuntimeRevision: bound.RuntimeRevision,
		}, ready: event, busy: true,
		live: &liveRuntime{identity: prepared.Credential.Identity, agentSessionID: ready.AgentSessionID,
			storage: prepared.Storage, sourceDigest: prepared.SourceDigest, events: events, traceOffset: offset,
			containerID: started.Runtime.ContainerID, initialRunID: started.Ownership.WorkerRunID, initialTurnID: started.Ownership.AgentTurnID},
	}
	r.mu.Lock()
	if r.pendingInitialActivations == nil {
		r.pendingInitialActivations = make(map[string]*pendingInitialActivation)
	}
	if r.pendingInitialActivations[op.command.RuntimeInstanceID] != nil {
		r.mu.Unlock()
		return workerapp.CapacityBindingMismatch
	}
	r.pendingInitialActivations[op.command.RuntimeInstanceID] = op
	r.mu.Unlock()
	return r.completeInitialActivation(ctx, op)
}

// IO never holds runner.mu. busy grants exactly one local handoff owner; the
// original Activate transaction still checks the complete consumed graph.
func (r *runtimeRunner) completeInitialActivation(ctx context.Context, op *pendingInitialActivation) (result error) {
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.pendingInitialActivations[op.command.RuntimeInstanceID] != op {
			return
		}
		op.busy = false
		if result != nil && !initialActivationWait(result) {
			op.blocked = result
		}
	}()
	bounded, cancel := context.WithTimeout(ctx, initialActivationWindow)
	defer cancel()
	var lastWait error
	for {
		err := r.checkInitialCapacity(bounded)
		if err == nil {
			_, err = r.residency.Activate(bounded, op.command)
		}
		if err == nil {
			if err = processWorkerControlEvent(bounded, op.ready, "runtime_ready_committed", func() error { return nil }); err != nil {
				return err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.pendingInitialActivations[op.command.RuntimeInstanceID] != op || !op.busy || r.runtimes[op.command.RuntimeInstanceID] != nil {
				return workerapp.CapacityBindingMismatch
			}
			r.runtimes[op.command.RuntimeInstanceID] = op.live
			delete(r.pendingInitialActivations, op.command.RuntimeInstanceID)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if bounded.Err() != nil && lastWait != nil && singleRuntimeCause(err) == context.DeadlineExceeded {
			return lastWait
		}
		if !initialActivationWait(err) {
			return err
		}
		lastWait = err
		timer := time.NewTimer(initialActivationPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-bounded.Done():
			timer.Stop()
			return lastWait
		case <-timer.C:
		}
	}
}

func (r *runtimeRunner) reconcileInitialActivation(ctx context.Context) error {
	r.mu.Lock()
	var selected *pendingInitialActivation
	for _, op := range r.pendingInitialActivations {
		if op.busy {
			continue
		}
		if selected == nil || op.command.RuntimeInstanceID < selected.command.RuntimeInstanceID {
			selected = op
		}
	}
	if selected == nil {
		r.mu.Unlock()
		return nil
	}
	if selected.blocked != nil {
		err := selected.blocked
		r.mu.Unlock()
		return err
	}
	selected.busy = true
	r.mu.Unlock()
	return r.completeInitialActivation(ctx, selected)
}

// This only selects the narrow source preflight. A matching live identity is
// insufficient authorization: Hub.Send must reserve the original start envelope
// in the production journal, which validates the full initial consumed graph.
func (r *runtimeRunner) checkDeliveredCapacity(ctx context.Context, live *liveRuntime, turn pendingTurn) error {
	if live != nil && live.initialRunID == turn.WorkerRunID && live.initialTurnID == turn.AgentTurnID && turn.RuntimeGeneration == 1 && live.identity.RuntimeInstanceID == turn.RuntimeInstanceID && live.identity.WorkerSessionID == turn.WorkerSessionID {
		return r.checkInitialCapacity(ctx)
	}
	return checkRuntimeStart(ctx, r.startPolicy)
}
