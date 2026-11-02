package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeLossFailsClosedWithoutProvisioningReplacement(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	provisioner := &fake.RuntimeProvisioner{Inspection: application.RuntimeInspection{
		Exists: false, Running: false, Generation: 1, ObservedAt: now,
	}}
	control := &fake.WorkerControl{}
	store := &recordingRuntimeLossStore{}
	service := application.RuntimeLossService{
		Store: store, Provisioner: provisioner, Control: control,
		IDs: &sequenceIDs{values: []string{"transition-loss-1", "event-loss-1"}},
		Now: func() time.Time { return now },
	}
	result, err := service.Observe(context.Background(), validRuntimeLossCommand())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Lost || result.FenceStatus != "confirmed" || store.calls != 1 {
		t.Fatalf("loss result=%+v store calls=%d", result, store.calls)
	}
	if len(provisioner.InspectionIDs) != 1 || provisioner.InspectionIDs[0] != "runtime-1" ||
		len(provisioner.ProvisionRequests) != 0 || len(provisioner.CleanupRequests) != 0 {
		t.Fatalf("unexpected Runtime calls inspect=%v provision=%d cleanup=%v",
			provisioner.InspectionIDs, len(provisioner.ProvisionRequests), provisioner.CleanupRequests)
	}
	if len(control.Fences) != 1 || control.Fences[0] != "runtime-1/1/runtime_lost" {
		t.Fatalf("control fences=%v", control.Fences)
	}
}

func TestRuntimeLossLeavesHealthyResidentRuntimeUntouched(t *testing.T) {
	provisioner := &fake.RuntimeProvisioner{Inspection: application.RuntimeInspection{
		Exists: true, Running: true, ContainerID: "container-1", Generation: 1,
	}}
	control := &fake.WorkerControl{}
	store := &recordingRuntimeLossStore{}
	result, err := (application.RuntimeLossService{
		Store: store, Provisioner: provisioner, Control: control,
		IDs: &sequenceIDs{values: []string{"unused-1", "unused-2"}}, Now: time.Now,
	}).Observe(context.Background(), validRuntimeLossCommand())
	if err != nil {
		t.Fatal(err)
	}
	if result.Lost || store.calls != 0 || len(control.Fences) != 0 {
		t.Fatalf("healthy Runtime changed result=%+v store=%d fences=%v", result, store.calls, control.Fences)
	}
}

func TestRuntimeLossPersistsDatabaseFenceWhenControlAlreadyDisconnected(t *testing.T) {
	provisioner := &fake.RuntimeProvisioner{Inspection: application.RuntimeInspection{Exists: false}}
	control := &fake.WorkerControl{Err: errors.New("not connected")}
	store := &recordingRuntimeLossStore{}
	result, err := (application.RuntimeLossService{
		Store: store, Provisioner: provisioner, Control: control,
		IDs: &sequenceIDs{values: []string{"transition-loss-1", "event-loss-1"}}, Now: time.Now,
	}).Observe(context.Background(), validRuntimeLossCommand())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Lost || result.FenceStatus != "unconfirmed" || store.record.FenceStatus != "unconfirmed" {
		t.Fatalf("disconnected fence result=%+v record=%+v", result, store.record)
	}
}

func TestBackendRestartReconcilesAnUnownedRuntimeAfterExactCleanup(t *testing.T) {
	now := time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)
	cleaner := &fake.RuntimeProvisioner{CleanupResult: application.RuntimeCleanupObservation{
		Confirmed: true, ObservedAt: now, ContainerRef: "switchback-runtime-runtime-1",
	}}
	control := &fake.WorkerControl{Err: errors.New("old control stream is gone")}
	store := &recordingRuntimeLossStore{}
	result, err := (application.UnownedRuntimeReconciler{
		Store: store, Cleaner: cleaner, Control: control,
		IDs: &sequenceIDs{values: []string{"transition-loss-1", "event-loss-1"}},
		Now: func() time.Time { return now },
	}).Execute(context.Background(), validUnownedRuntimeCommand())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Lost || result.FenceStatus != "unconfirmed" || store.calls != 1 ||
		store.record.ExpectedStatus != "active" {
		t.Fatalf("restart reconciliation result=%+v record=%+v", result, store.record)
	}
	if len(cleaner.CleanupRequests) != 1 || len(control.Fences) != 1 ||
		control.Fences[0] != "runtime-1/1/backend_restarted" {
		t.Fatalf("cleanup=%v fences=%v", cleaner.CleanupRequests, control.Fences)
	}
}

func TestBackendRestartDoesNotChangeDatabaseWhenCleanupIsUnconfirmed(t *testing.T) {
	cleaner := &fake.RuntimeProvisioner{CleanupResult: application.RuntimeCleanupObservation{
		Confirmed: false, ContainerExists: true,
	}}
	store := &recordingRuntimeLossStore{}
	_, err := (application.UnownedRuntimeReconciler{
		Store: store, Cleaner: cleaner, Control: &fake.WorkerControl{},
		IDs: &sequenceIDs{values: []string{"unused-1", "unused-2"}}, Now: time.Now,
	}).Execute(context.Background(), validUnownedRuntimeCommand())
	if err == nil || store.calls != 0 {
		t.Fatalf("unconfirmed cleanup err=%v store calls=%d", err, store.calls)
	}
}

func validUnownedRuntimeCommand() application.ReconcileUnownedRuntimeCommand {
	return application.ReconcileUnownedRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1", ExpectedContainerID: "container-1",
		RuntimeGeneration: 1, RuntimeStatus: "active", ExpectedSessionRevision: 4,
		ExpectedRunRevision: 2, ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 2,
		IdempotencyKey: "backend-restart/runtime-1/1",
	}
}

func validRuntimeLossCommand() application.ObserveRuntimeLossCommand {
	return application.ObserveRuntimeLossCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1",
		ExpectedContainerID: "container-1", RuntimeGeneration: 1,
		ExpectedSessionRevision: 4, ExpectedRunRevision: 2,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 2,
		IdempotencyKey:  "runtime-loss/runtime-1/g1",
		ProvenTraceRefs: []string{"trace/segment-1"}, ProvenArtifactRefs: []string{"artifact/report-1"},
	}
}

type recordingRuntimeLossStore struct {
	calls  int
	record application.RuntimeLossRecord
}

func (store *recordingRuntimeLossStore) RecordRuntimeLoss(
	_ context.Context,
	record application.RuntimeLossRecord,
) (application.RuntimeLossState, error) {
	store.calls++
	store.record = record
	return application.RuntimeLossState{RuntimeStatus: "lost"}, nil
}
