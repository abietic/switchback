package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeResidencyAcceptsMonotonicGenerationBeforePersistence(t *testing.T) {
	store := &recordingResidencyStore{}
	service := application.RuntimeResidencyService{Store: store, Now: time.Now}
	_, err := service.Activate(context.Background(), application.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1", ContainerID: "container-1",
		RuntimeGeneration: 2, ControlChannelEpoch: 1,
		ExpectedSessionRevision: 3, ExpectedRunRevision: 1,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.activateCalls != 1 {
		t.Fatal("generation 2 did not reach persistence")
	}
}

func TestRuntimeResidencyRejectsZeroGenerationBeforePersistence(t *testing.T) {
	store := &recordingResidencyStore{}
	service := application.RuntimeResidencyService{Store: store, Now: time.Now}
	_, err := service.Activate(context.Background(), application.ActivateResidentRuntimeCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1", ContainerID: "container-1",
		RuntimeGeneration: 0, ControlChannelEpoch: 1,
		ExpectedSessionRevision: 3, ExpectedRunRevision: 1,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 1,
	})
	if err == nil || store.activateCalls != 0 {
		t.Fatalf("zero generation err=%v calls=%d", err, store.activateCalls)
	}
}

func TestRuntimeResidencyRejectsInconsistentStageOutcome(t *testing.T) {
	store := &recordingResidencyStore{}
	service := application.RuntimeResidencyService{Store: store, Now: time.Now}
	_, err := service.CompleteStage(context.Background(), application.CompleteResidentStageCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1,
		ExpectedSessionRevision: 3, ExpectedRunRevision: 2,
		ExpectedTurnRevision: 1, ExpectedRuntimeRevision: 2,
		SessionTarget: domain.SessionIdle, RunTarget: domain.RunWaitingUser,
		TurnTarget: domain.TurnCompleted,
	})
	if err == nil {
		t.Fatal("waiting Run with idle Session unexpectedly accepted")
	}
	if store.completeCalls != 0 {
		t.Fatal("inconsistent outcome reached persistence")
	}
}

type recordingResidencyStore struct {
	activateCalls int
	completeCalls int
}

func (store *recordingResidencyStore) ActivateResidentRuntime(
	context.Context,
	application.ActivateResidentRuntimeRecord,
) (application.ResidentRuntimeState, error) {
	store.activateCalls++
	return application.ResidentRuntimeState{}, nil
}

func (store *recordingResidencyStore) CompleteResidentStage(
	context.Context,
	application.CompleteResidentStageRecord,
) (application.ResidentRuntimeState, error) {
	store.completeCalls++
	return application.ResidentRuntimeState{}, nil
}
