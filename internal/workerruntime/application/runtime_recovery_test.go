package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestRuntimeRecoveryOpenPersistsOneOwnerIntentBeforeEffects(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	store := &recordingRecoveryStore{}
	service := application.RuntimeRecoveryService{
		Store: store,
		IDs: &sequenceIDs{values: []string{
			"recovery-incident-1", "integration-event-recovery-started-1",
		}},
		Now: func() time.Time { return now },
	}
	result, err := service.Open(context.Background(), validOpenRecoveryCommand())
	if err != nil {
		t.Fatal(err)
	}
	if store.openCalls != 1 || store.openRecord.IncidentID != "recovery-incident-1" ||
		store.openRecord.EventID != "integration-event-recovery-started-1" ||
		!store.openRecord.Now.Equal(now) {
		t.Fatalf("open record=%+v calls=%d", store.openRecord, store.openCalls)
	}
	if result.IncidentID != "recovery-incident-1" {
		t.Fatalf("result=%+v", result)
	}
}

func TestRuntimeRecoveryOpenRejectsIneligibleTriggerBeforePersistence(t *testing.T) {
	store := &recordingRecoveryStore{}
	command := validOpenRecoveryCommand()
	command.Trigger = domain.RecoveryTriggerUserCancellation
	_, err := (application.RuntimeRecoveryService{
		Store: store, IDs: &sequenceIDs{}, Now: time.Now,
	}).Open(context.Background(), command)
	if err == nil || store.openCalls != 0 {
		t.Fatalf("ineligible recovery err=%v calls=%d", err, store.openCalls)
	}
}

func TestRuntimeRecoveryOpenRejectsLegacyTransportCloseTriggerBeforePersistence(t *testing.T) {
	store := &recordingRecoveryStore{}
	command := validOpenRecoveryCommand()
	command.Trigger = domain.RecoveryTriggerWorkerControlClosed
	_, err := (application.RuntimeRecoveryService{
		Store: store,
		IDs: &sequenceIDs{values: []string{
			"legacy-recovery-incident", "legacy-recovery-event",
		}},
		Now: time.Now,
	}).Open(context.Background(), command)
	if err == nil || store.openCalls != 0 {
		t.Fatalf("legacy transport-close recovery err=%v calls=%d", err, store.openCalls)
	}
}

func TestRuntimeRecoveryOpenAcceptsIdleRuntimeWithoutRunOrTurn(t *testing.T) {
	store := &recordingRecoveryStore{}
	command := validOpenRecoveryCommand()
	command.WorkerRunID = ""
	command.AgentTurnID = ""
	command.ExpectedRunRevision = 0
	command.ExpectedTurnRevision = 0
	_, err := (application.RuntimeRecoveryService{
		Store: store,
		IDs: &sequenceIDs{values: []string{
			"recovery-incident-idle", "integration-event-recovery-started-idle",
		}},
		Now: func() time.Time { return command.EvidenceAt.Add(time.Minute) },
	}).Open(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if store.openCalls != 1 {
		t.Fatalf("idle recovery calls=%d", store.openCalls)
	}
}

func TestRuntimeRecoveryAdvancePersistsPhaseIntentBeforeEffects(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 5, 0, 0, time.UTC)
	store := &recordingRecoveryStore{}
	service := application.RuntimeRecoveryService{
		Store: store,
		IDs: &sequenceIDs{values: []string{
			"runtime-replacement-2", "integration-event-recovery-2", "task-transition-recovery-2",
		}},
		Now: func() time.Time { return now },
	}
	result, err := service.Advance(context.Background(), application.AdvanceRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", IncidentID: "incident-1",
		TargetPhase: domain.RecoveryProvisioning, ExpectedIncidentRevision: 2,
		FenceStatus: domain.RecoveryFenceConfirmed, MountFenceToken: 8,
		IdempotencyKey: "recovery/incident-1/reserve",
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.advanceCalls != 1 ||
		store.advanceRecord.ReplacementRuntimeInstanceID != "runtime-replacement-2" ||
		store.advanceRecord.ReplacementChannelEpoch != 9 ||
		store.advanceRecord.EventID != "integration-event-recovery-2" ||
		!store.advanceRecord.Now.Equal(now) ||
		result.IncidentID != "incident-1" {
		t.Fatalf("advance record=%+v calls=%d result=%+v",
			store.advanceRecord, store.advanceCalls, result)
	}
}

func validOpenRecoveryCommand() application.OpenRuntimeRecoveryCommand {
	return application.OpenRuntimeRecoveryCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", WorkerRunID: "run-1",
		AgentTurnID: "turn-1", FailedRuntimeInstanceID: "runtime-1",
		FailedGeneration: 1, FailedChannelEpoch: 1, Trigger: domain.RecoveryTriggerContainerExit,
		EvidenceRef: "runtime-inspection/runtime-1", EvidenceAt: time.Date(2026, 7, 26, 2, 59, 0, 0, time.UTC),
		ExpectedSessionRevision: 4, ExpectedRunRevision: 2,
		ExpectedTurnRevision: 3, ExpectedRuntimeRevision: 5,
		IdempotencyKey: "runtime-recovery/session-1/g1",
	}
}

type recordingRecoveryStore struct {
	openCalls     int
	openRecord    application.OpenRuntimeRecoveryRecord
	advanceCalls  int
	advanceRecord application.AdvanceRuntimeRecoveryRecord
}

func (store *recordingRecoveryStore) OpenRuntimeRecovery(
	_ context.Context,
	record application.OpenRuntimeRecoveryRecord,
) (application.RuntimeRecoveryState, error) {
	store.openCalls++
	store.openRecord = record
	return application.RuntimeRecoveryState{IncidentID: record.IncidentID}, nil
}

func (store *recordingRecoveryStore) AdvanceRuntimeRecovery(
	_ context.Context,
	record application.AdvanceRuntimeRecoveryRecord,
) (application.RuntimeRecoveryState, error) {
	store.advanceCalls++
	store.advanceRecord = record
	return application.RuntimeRecoveryState{IncidentID: record.Command.IncidentID}, nil
}
