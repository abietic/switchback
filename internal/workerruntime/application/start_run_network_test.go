package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestStartWorkerRunPersistsResolvedCurrentNetworkEvidenceInSnapshot(t *testing.T) {
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	command := validStartWorkerRunCommand()
	command.NetworkPolicyID = profile.ID
	command.NetworkPolicyRevision = profile.Revision
	command.NetworkPolicyDigest = profile.Digest
	command.NetworkEnforcementStatus = config.NetworkEnforcementActive
	store := &networkStartStore{}
	service := application.StartWorkerRunService{
		Store: store,
		IDs: &sequenceIDs{values: []string{
			"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1",
			"deadline-1", "transition-1", "event-1",
		}},
		Now: func() time.Time { return time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC) },
	}

	if _, err := service.Execute(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		RuntimeNetworkProfile config.RuntimeNetworkEvidence `json:"runtime_network_profile"`
	}
	if err := json.Unmarshal(store.record.Command.StartSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.RuntimeNetworkProfile != profile.Evidence() {
		t.Fatalf("runtime network snapshot = %+v", snapshot.RuntimeNetworkProfile)
	}
}

func TestStartWorkerRunRejectsInvalidNetworkEvidence(t *testing.T) {
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]config.RuntimeNetworkEvidence{
		"missing revision": {
			ID: profile.ID, Digest: profile.Digest, EnforcementStatus: config.NetworkEnforcementActive,
		},
		"missing digest": {
			ID: profile.ID, Revision: profile.Revision, EnforcementStatus: config.NetworkEnforcementActive,
		},
		"missing status": {
			ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
		},
		"legacy marked active": {
			ID: config.LegacyPublicWebRestrictedV1, EnforcementStatus: config.NetworkEnforcementActive,
		},
	}
	for name, evidence := range tests {
		t.Run(name, func(t *testing.T) {
			command := validStartWorkerRunCommand()
			command.NetworkPolicyID = evidence.ID
			command.NetworkPolicyRevision = evidence.Revision
			command.NetworkPolicyDigest = evidence.Digest
			command.NetworkEnforcementStatus = evidence.EnforcementStatus
			store := &networkStartStore{}
			service := application.StartWorkerRunService{
				Store: store,
				IDs: &sequenceIDs{values: []string{
					"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1",
					"deadline-1", "transition-1", "event-1",
				}},
				Now: time.Now,
			}
			if _, err := service.Execute(context.Background(), command); err == nil {
				t.Fatalf("invalid network evidence was accepted: %+v", evidence)
			}
			if store.called {
				t.Fatal("invalid network evidence reached persistence")
			}
		})
	}
}

func TestStartWorkerRunAcceptsLegacyCompatibilityEvidence(t *testing.T) {
	command := validStartWorkerRunCommand()
	command.NetworkPolicyID = config.LegacyPublicWebRestrictedV1
	command.NetworkPolicyRevision = 0
	command.NetworkPolicyDigest = ""
	command.NetworkEnforcementStatus = config.NetworkEnforcementLegacy
	store := &networkStartStore{}
	service := application.StartWorkerRunService{
		Store: store,
		IDs: &sequenceIDs{values: []string{
			"worker-run-1", "runtime-1", "agent-session-1", "agent-turn-1",
			"deadline-1", "transition-1", "event-1",
		}},
		Now: time.Now,
	}
	if _, err := service.Execute(context.Background(), command); err != nil {
		t.Fatal(err)
	}
}

type networkStartStore struct {
	called bool
	record application.StartWorkerRunRecord
}

func (store *networkStartStore) StartWorkerRun(
	_ context.Context,
	record application.StartWorkerRunRecord,
) (application.StartedWorkerRun, error) {
	store.called = true
	store.record = record
	return application.StartedWorkerRun{
		WorkerRunID: record.WorkerRunID, WorkerSessionID: record.Command.WorkerSessionID,
		TaskID: record.Command.TaskID, RuntimeInstanceID: record.RuntimeInstanceID,
		AgentSessionID: record.AgentSessionID, AgentTurnID: record.AgentTurnID,
		RuntimeGeneration:   record.Command.RuntimeGeneration,
		ControlChannelEpoch: record.Command.ControlChannelEpoch,
		Status:              "starting", RunRevision: 1, SessionRevision: 3,
	}, nil
}
