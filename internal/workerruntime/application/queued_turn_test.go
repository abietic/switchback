package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestQueuedTurnRequiresExactRuntimeGeneration(t *testing.T) {
	store := &recordingQueuedTurnStore{}
	service := application.QueuedTurnService{
		Store: store,
		IDs:   fixedAllocationIDs{id: "generated-id"},
		Now:   time.Now,
	}
	command := validQueuedTurnCommand()
	command.RuntimeGeneration = 0

	if _, err := service.Execute(context.Background(), command); err == nil {
		t.Fatal("zero Runtime generation unexpectedly accepted")
	}
	if store.calls != 0 {
		t.Fatal("invalid Runtime generation reached persistence")
	}
}

func TestQueuedTurnPassesExactRuntimeGenerationToPersistence(t *testing.T) {
	store := &recordingQueuedTurnStore{}
	service := application.QueuedTurnService{
		Store: store,
		IDs:   fixedAllocationIDs{id: "generated-id"},
		Now:   time.Now,
	}
	command := validQueuedTurnCommand()
	command.RuntimeGeneration = 3

	if _, err := service.Execute(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || store.record.Command.RuntimeGeneration != 3 {
		t.Fatalf("calls=%d record=%+v", store.calls, store.record)
	}
}

func validQueuedTurnCommand() application.ContinueQueuedTurnCommand {
	return application.ContinueQueuedTurnCommand{
		TenantID: "tenant-local", UserID: "user-local", WorkerSessionID: "session-1",
		PreviousWorkerRunID: "run-1", PreviousAgentTurnID: "turn-1",
		RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, QueueCommandID: "command-1",
		ExpectedSessionRevision: 2, ExpectedRunRevision: 2,
		ExpectedTurnRevision: 2, ExpectedRuntimeRevision: 2,
	}
}

type recordingQueuedTurnStore struct {
	calls  int
	record application.ContinueQueuedTurnRecord
}

func (store *recordingQueuedTurnStore) ContinueQueuedTurn(
	_ context.Context,
	record application.ContinueQueuedTurnRecord,
) (application.ContinuedQueuedTurn, error) {
	store.calls++
	store.record = record
	return application.ContinuedQueuedTurn{}, nil
}
