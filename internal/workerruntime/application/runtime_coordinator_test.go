package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeCoordinatorCommitsOwnershipBeforeEnvironmentOrDocker(t *testing.T) {
	store := &recordingStartStore{}
	ids := &sequenceIDs{values: []string{
		"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
	}}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		Starts: application.StartWorkerRunService{
			Store: store, IDs: ids,
			Now: func() time.Time { return time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC) },
		},
		Environment: preparer,
		Provisioner: provisioner,
	}

	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{
		Run: validStartWorkerRunCommand(),
	})
	if err != nil {
		t.Fatalf("start Runtime: %v", err)
	}
	if result.Ownership.WorkerRunID != "worker-run-1" ||
		result.Ownership.RuntimeInstanceID != "runtime-1" ||
		result.Ownership.AgentSessionID != "agent-session-1" ||
		result.Ownership.AgentTurnID != "agent-turn-1" ||
		result.Runtime.ContainerID != "container-1" {
		t.Fatalf("start result = %+v", result)
	}
	if !store.committed || !preparer.prepared || !provisioner.provisioned {
		t.Fatalf("ordering store=%v prepare=%v provision=%v", store.committed, preparer.prepared, provisioner.provisioned)
	}
}

func TestRuntimeCoordinatorDoesNotTouchEnvironmentWhenOwnershipCommitFails(t *testing.T) {
	sentinel := errors.New("commit failed")
	store := &recordingStartStore{err: sentinel}
	preparer := &orderingPreparer{committed: &store.committed}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: provisioner,
	}
	_, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if !errors.Is(err, sentinel) {
		t.Fatalf("start error = %v", err)
	}
	if preparer.prepared || provisioner.provisioned {
		t.Fatalf("external work occurred after failed commit: prepare=%v provision=%v", preparer.prepared, provisioner.provisioned)
	}
}

func TestRuntimeCoordinatorRejectsPreparedIdentityDriftBeforeDocker(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed, driftRuntimeID: true}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: provisioner,
	}
	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if err == nil {
		t.Fatal("drifted Runtime preparation unexpectedly reached Docker")
	}
	if result.Ownership.RuntimeInstanceID != "runtime-1" || result.Ownership.WorkerRunID != "worker-run-1" {
		t.Fatalf("committed ownership was lost on post-commit failure: %+v", result)
	}
	if provisioner.provisioned {
		t.Fatal("Docker was called with drifted Runtime identity")
	}
}

func TestRuntimeCoordinatorRejectsPreparedNetworkEvidenceDriftBeforeDocker(t *testing.T) {
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed, driftNetwork: true}
	provisioner := &orderingProvisioner{prepared: &preparer.prepared}
	coordinator := application.RuntimeCoordinator{
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1",
				"deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: provisioner,
	}
	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{
		Run: validStartWorkerRunCommand(),
	})
	if err == nil {
		t.Fatal("drifted Runtime network evidence unexpectedly reached Docker")
	}
	if result.Ownership.RuntimeInstanceID != "runtime-1" || result.Ownership.WorkerRunID != "worker-run-1" {
		t.Fatalf("committed ownership was lost on network drift: %+v", result)
	}
	if provisioner.provisioned {
		t.Fatal("Docker was called with drifted Runtime network evidence")
	}
}

func TestRuntimeCoordinatorReturnsCommittedOwnershipWhenEnvironmentPreparationFails(t *testing.T) {
	sentinel := errors.New("prepare failed")
	store := &recordingStartStore{}
	preparer := &orderingPreparer{committed: &store.committed, err: sentinel}
	coordinator := application.RuntimeCoordinator{
		Starts: application.StartWorkerRunService{
			Store: store,
			IDs: &sequenceIDs{values: []string{
				"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1", "deadline-1", "transition-1", "event-1",
			}},
			Now: time.Now,
		},
		Environment: preparer,
		Provisioner: &orderingProvisioner{prepared: &preparer.prepared},
	}
	result, err := coordinator.Start(context.Background(), application.StartRuntimeCommand{Run: validStartWorkerRunCommand()})
	if !errors.Is(err, sentinel) || result.Ownership.RuntimeInstanceID != "runtime-1" {
		t.Fatalf("post-commit failure result=%+v error=%v", result, err)
	}
}

func validStartWorkerRunCommand() application.StartWorkerRunCommand {
	return application.StartWorkerRunCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: "worker-session-1",
		TaskID: "task-1", PermitID: "permit-1", ExpectedSessionRevision: 2,
		RuntimeGeneration: 1, TriggerType: "task_initial",
		StartSnapshot:  json.RawMessage(`{"working_copy_checkpoint":"checkpoint-1"}`),
		AgentProfileID: "deepseek-codex-openai-v1", AgentProfileRevision: "1",
		AgentProfileDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AgentTurnContent:   json.RawMessage(`{"text":"fix it"}`),
		TurnIdempotencyKey: "turn/task-1/revision-1",
		RuntimeImageRef:    "switchback-worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ResourceProfileID:  "local_standard_v1", NetworkPolicyID: "m1_public_web_restricted_v1",
		NetworkEnforcementStatus: "legacy_unenforced", ControlChannelEpoch: 1,
	}
}

type sequenceIDs struct {
	values []string
	index  int
}

func (ids *sequenceIDs) NewID(string) (string, error) {
	if ids.index >= len(ids.values) {
		return "", errors.New("no test ID")
	}
	value := ids.values[ids.index]
	ids.index++
	return value, nil
}

type recordingStartStore struct {
	committed bool
	err       error
}

func (store *recordingStartStore) StartWorkerRun(_ context.Context, record application.StartWorkerRunRecord) (application.StartedWorkerRun, error) {
	if store.err != nil {
		return application.StartedWorkerRun{}, store.err
	}
	store.committed = true
	return application.StartedWorkerRun{
		WorkerRunID: record.WorkerRunID, WorkerSessionID: record.Command.WorkerSessionID,
		TaskID: record.Command.TaskID, RuntimeInstanceID: record.RuntimeInstanceID,
		AgentSessionID: record.AgentSessionID, AgentTurnID: record.AgentTurnID,
		RuntimeGeneration:   record.Command.RuntimeGeneration,
		ControlChannelEpoch: record.Command.ControlChannelEpoch,
		Status:              "starting", RunRevision: 1, SessionRevision: 3,
	}, nil
}

type orderingPreparer struct {
	committed      *bool
	prepared       bool
	driftRuntimeID bool
	driftNetwork   bool
	err            error
}

func (preparer *orderingPreparer) PrepareRuntime(_ context.Context, request application.RuntimePreparationRequest) (application.RuntimeProvisionRequest, error) {
	if preparer.committed == nil || !*preparer.committed {
		return application.RuntimeProvisionRequest{}, errors.New("environment prepared before ownership commit")
	}
	preparer.prepared = true
	if preparer.err != nil {
		return application.RuntimeProvisionRequest{}, preparer.err
	}
	runtimeID := request.Ownership.RuntimeInstanceID
	if preparer.driftRuntimeID {
		runtimeID = "runtime-drift"
	}
	networkID := request.Command.NetworkPolicyID
	if preparer.driftNetwork {
		networkID = "caller-network"
	}
	return application.RuntimeProvisionRequest{
		WorkerSessionID:   request.Ownership.WorkerSessionID,
		WorkerRunID:       request.Ownership.WorkerRunID,
		RuntimeInstanceID: runtimeID,
		Generation:        request.Ownership.RuntimeGeneration,
		NetworkProfile: application.ProfileRef{
			ID: networkID, Revision: request.Command.NetworkPolicyRevision,
			Digest: request.Command.NetworkPolicyDigest,
		},
		NetworkEnforcementStatus: request.Command.NetworkEnforcementStatus,
	}, nil
}

type orderingProvisioner struct {
	prepared    *bool
	provisioned bool
}

func (provisioner *orderingProvisioner) Provision(_ context.Context, request application.RuntimeProvisionRequest) (application.ProvisionedRuntime, error) {
	if provisioner.prepared == nil || !*provisioner.prepared {
		return application.ProvisionedRuntime{}, errors.New("Docker called before environment preparation")
	}
	provisioner.provisioned = true
	return application.ProvisionedRuntime{
		RuntimeInstanceID: request.RuntimeInstanceID,
		ContainerID:       "container-1", Generation: request.Generation,
	}, nil
}

func (*orderingProvisioner) Inspect(context.Context, string) (application.RuntimeInspection, error) {
	return application.RuntimeInspection{}, nil
}
