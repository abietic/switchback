package localproduct

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

var errObservationCleanupFenceUnproven = errors.New("observation_cleanup_fence_unproven")

// No path or credential from the caller is used to locate storage. The state
// must come from the committed journal Fence result (or the deadline proof read).
func (environment *runtimeEnvironment) cleanupFencedObservationRegistry(ctx context.Context, identity controlgrpc.RuntimeIdentity, state workerapp.RuntimeControlChannelState) error {
	if environment == nil || !observationCleanupFenceProven(identity, state) {
		return errObservationCleanupFenceUnproven
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if prepared, ok := environment.prepared[identity.RuntimeInstanceID]; ok && prepared.Credential.Identity != identity {
		return errObservationCleanupFenceUnproven
	}
	storage, err := environment.storage.OpenExisting(identity.WorkerSessionID)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(storage.BackendRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	// Root confines even a concurrent path substitution to this server-owned
	// Session tree. Only this Runtime generation's secret/control-plane tree goes.
	relative := filepath.Join("runtimes", identity.RuntimeInstanceID+"-g"+strconv.FormatUint(uint64(identity.RuntimeGeneration), 10))
	if err := root.RemoveAll(relative); err != nil {
		return fmt.Errorf("remove fenced Runtime secret resources: %w", err)
	}
	if _, err := root.Lstat(relative); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("fenced Runtime secret resources remain")
	}
	delete(environment.prepared, identity.RuntimeInstanceID)
	return nil
}

func observationCleanupFenceProven(identity controlgrpc.RuntimeIdentity, state workerapp.RuntimeControlChannelState) bool {
	return observationCleanupIdentityValid(identity) &&
		state.WorkerSessionID == identity.WorkerSessionID && state.RuntimeInstanceID == identity.RuntimeInstanceID &&
		state.RuntimeGeneration == uint64(identity.RuntimeGeneration) && state.ChannelEpoch == identity.ChannelEpoch &&
		state.Status == "fenced" && !state.FencedAt.IsZero() && state.FenceRevision > 0 &&
		state.FenceRevision <= state.Revision && state.FenceOwnerFence > 0 && state.FenceOwnerFence == state.OwnerFence
}

func observationCleanupIdentityValid(identity controlgrpc.RuntimeIdentity) bool {
	for _, id := range []string{identity.WorkerSessionID, identity.RuntimeInstanceID} {
		if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, "\\\x00") || strings.TrimSpace(id) != id {
			return false
		}
	}
	return identity.RuntimeGeneration > 0 && identity.ChannelEpoch > 0
}

func (environment *runtimeEnvironment) fenceObservationRegistry(ctx context.Context, identity controlgrpc.RuntimeIdentity) error {
	state, err := environment.hub.FenceIdentity(ctx, identity)
	if err != nil {
		return errObservationCleanupFenceUnproven
	}
	return environment.cleanupFencedObservationRegistry(ctx, identity, state)
}

// This adapter is used by both the initial ordinary terminal cleanup and its
// ScheduledCommand retries. Generic WorkerControl behavior is unchanged.
type runtimeObservationCleanupControl struct {
	workerapp.WorkerControl
	runner *runtimeRunner
}

func (control runtimeObservationCleanupControl) Fence(ctx context.Context, runtimeID string, generation uint64, _ string) error {
	if generation == 0 || generation > uint64(^uint32(0)) {
		return errObservationCleanupFenceUnproven
	}
	identity := controlgrpc.RuntimeIdentity{RuntimeInstanceID: runtimeID, RuntimeGeneration: uint32(generation)}
	err := control.runner.pool.QueryRow(ctx, `SELECT worker_session_id,control_channel_epoch FROM runtime_instances WHERE runtime_instance_id=$1 AND generation=$2`, runtimeID, generation).Scan(&identity.WorkerSessionID, &identity.ChannelEpoch)
	if err != nil {
		return errObservationCleanupFenceUnproven
	}
	return control.runner.environment.fenceObservationRegistry(ctx, identity)
}

func (runner *runtimeRunner) readObservationCleanupFence(ctx context.Context, identity controlgrpc.RuntimeIdentity) (workerapp.RuntimeControlChannelState, error) {
	var state workerapp.RuntimeControlChannelState
	err := runner.pool.QueryRow(ctx, `SELECT worker_session_id,runtime_instance_id,runtime_generation,channel_epoch,status,owner_fence,revision,fenced_at,fence_revision,fence_owner_fence FROM runtime_control_channels WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND runtime_generation=$3 AND channel_epoch=$4`, identity.RuntimeInstanceID, identity.WorkerSessionID, identity.RuntimeGeneration, identity.ChannelEpoch).Scan(
		&state.WorkerSessionID, &state.RuntimeInstanceID, &state.RuntimeGeneration, &state.ChannelEpoch, &state.Status, &state.OwnerFence, &state.Revision, &state.FencedAt, &state.FenceRevision, &state.FenceOwnerFence)
	if err != nil || !observationCleanupFenceProven(identity, state) {
		return workerapp.RuntimeControlChannelState{}, errObservationCleanupFenceUnproven
	}
	return state, nil
}
