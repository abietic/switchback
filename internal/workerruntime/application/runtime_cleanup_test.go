package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeCleanupPersistsPendingIntentBeforeDeleteAndReconciles(t *testing.T) {
	now := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	store := &recordingRuntimeCleanupStore{}
	cleaner := &fake.RuntimeProvisioner{
		CleanupResult: application.RuntimeCleanupObservation{
			ContainerExists: false, SecretVolumeExists: true,
			ContainerRef: "switchback-runtime-runtime-1", SecretVolumeRef: "switchback-runtime-secrets-runtime-1-g1",
		},
		Err: errors.New("raw Docker failure with provider-key"),
	}
	service := application.RuntimeCleanupService{
		Store: store, Cleaner: cleaner, Control: &fake.WorkerControl{},
		IDs: &sequenceIDs{values: []string{"cleanup-command-1"}},
		Now: func() time.Time { return now }, RetryDelay: time.Minute,
	}
	failed, err := service.Cleanup(context.Background(), validRuntimeCleanupCommand())
	if err != nil {
		t.Fatal(err)
	}
	if failed.Confirmed || failed.ErrorCode != "runtime_cleanup_residue" ||
		store.beginCalls != 1 || store.finishCalls != 1 || len(cleaner.CleanupRequests) != 1 {
		t.Fatalf("failed cleanup=%+v begin=%d finish=%d requests=%v",
			failed, store.beginCalls, store.finishCalls, cleaner.CleanupRequests)
	}
	cleaner.Err = nil
	cleaner.CleanupResult = application.RuntimeCleanupObservation{Confirmed: true}
	reconciled, err := service.Reconcile(context.Background(), failed.Reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled.Confirmed || store.beginCalls != 1 || store.finishCalls != 2 ||
		len(cleaner.CleanupRequests) != 2 {
		t.Fatalf("reconciled cleanup=%+v begin=%d finish=%d requests=%v",
			reconciled, store.beginCalls, store.finishCalls, cleaner.CleanupRequests)
	}
}

func validRuntimeCleanupCommand() application.BeginRuntimeCleanupCommand {
	return application.BeginRuntimeCleanupCommand{
		TenantID: "tenant-local", WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
		ExpectedContainerID: "container-1", RuntimeGeneration: 1,
		ExpectedSessionRevision: 5, ExpectedRuntimeRevision: 3,
		IdempotencyKey: "cleanup/runtime-1/g1",
	}
}

func TestRuntimeCleanupFenceFailureRemainsRetryableWithZeroResidue(t *testing.T) {
	store := &recordingRuntimeCleanupStore{}
	control := &fake.WorkerControl{Err: errors.New("controlled journal failure with secret")}
	service := application.RuntimeCleanupService{
		Store: store, Control: control,
		Cleaner: &fake.RuntimeProvisioner{CleanupResult: application.RuntimeCleanupObservation{Confirmed: true}},
		IDs:     &sequenceIDs{values: []string{"cleanup-command-1"}}, Now: time.Now, RetryDelay: time.Minute,
	}
	result, err := service.Cleanup(context.Background(), validRuntimeCleanupCommand())
	if err != nil {
		t.Fatal(err)
	}
	if result.Confirmed || result.ErrorCode != "runtime_cleanup_fence_unconfirmed" || result.Reconciliation.ScheduledCommandID == "" {
		t.Fatalf("fence failure incorrectly completed cleanup: %+v", result)
	}
	control.Err = nil
	result, err = service.Reconcile(context.Background(), result.Reconciliation)
	if err != nil || !result.Confirmed {
		t.Fatalf("fence retry: %+v %v", result, err)
	}
}

type recordingRuntimeCleanupStore struct {
	beginCalls  int
	finishCalls int
}

func (store *recordingRuntimeCleanupStore) BeginRuntimeCleanup(
	_ context.Context,
	record application.BeginRuntimeCleanupRecord,
) (application.RuntimeCleanupLease, error) {
	store.beginCalls++
	return application.RuntimeCleanupLease{
		TenantID: record.Command.TenantID, WorkerSessionID: record.Command.WorkerSessionID,
		RuntimeInstanceID:       record.Command.RuntimeInstanceID,
		ExpectedContainerID:     record.Command.ExpectedContainerID,
		RuntimeGeneration:       record.Command.RuntimeGeneration,
		ExpectedSessionRevision: record.Command.ExpectedSessionRevision,
		ExpectedRuntimeRevision: record.Command.ExpectedRuntimeRevision + 1,
		ScheduledCommandID:      record.ScheduledCommandID,
		IdempotencyKey:          record.Command.IdempotencyKey,
	}, nil
}

func (store *recordingRuntimeCleanupStore) FinishRuntimeCleanup(
	_ context.Context,
	record application.FinishRuntimeCleanupRecord,
) (application.RuntimeCleanupResult, error) {
	store.finishCalls++
	result := application.RuntimeCleanupResult{
		Confirmed:   record.Observation.Confirmed && record.ErrorCode == "",
		ErrorCode:   record.ErrorCode,
		Observation: record.Observation,
	}
	if !result.Confirmed {
		next := record.Lease
		next.ExpectedRuntimeRevision++
		result.Reconciliation = next
	}
	return result, nil
}
