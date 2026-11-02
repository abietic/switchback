package application

import (
	"context"
	"fmt"
	"time"
)

type ContinueQueuedTurnCommand struct {
	TenantID                string
	UserID                  string
	WorkerSessionID         string
	PreviousWorkerRunID     string
	PreviousAgentTurnID     string
	RuntimeInstanceID       string
	RuntimeGeneration       uint64
	QueueCommandID          string
	ExpectedSessionRevision uint64
	ExpectedRunRevision     uint64
	ExpectedTurnRevision    uint64
	ExpectedRuntimeRevision uint64
}

type ContinueQueuedTurnRecord struct {
	Command            ContinueQueuedTurnCommand
	WorkerRunID        string
	AgentTurnID        string
	ScheduledCommandID string
	EventID            string
	Now                time.Time
}

type ContinuedQueuedTurn struct {
	WorkerSessionID   string
	WorkerRunID       string
	AgentTurnID       string
	RuntimeInstanceID string
	RuntimeGeneration uint64
	SessionRevision   uint64
	RunRevision       uint64
	TurnRevision      uint64
	RuntimeRevision   uint64
	EventID           string
}

type QueuedTurnStore interface {
	ContinueQueuedTurn(context.Context, ContinueQueuedTurnRecord) (ContinuedQueuedTurn, error)
}

type SettlementQueuedTurnStore interface {
	ContinueSettlementQueuedTurn(context.Context, TurnSettlement, ContinueQueuedTurnRecord) (ContinuedQueuedTurn, error)
}

type QueuedTurnService struct {
	Store QueuedTurnStore
	IDs   AllocationIDGenerator
	Now   func() time.Time
}

func (service QueuedTurnService) Execute(
	ctx context.Context,
	command ContinueQueuedTurnCommand,
) (ContinuedQueuedTurn, error) {
	record, err := service.prepareContinuation(command)
	if err != nil {
		return ContinuedQueuedTurn{}, err
	}
	return service.Store.ContinueQueuedTurn(ctx, record)
}

func (service QueuedTurnService) ExecuteSettlement(ctx context.Context, settlement TurnSettlement, command ContinueQueuedTurnCommand) (ContinuedQueuedTurn, error) {
	store, ok := service.Store.(SettlementQueuedTurnStore)
	if !ok {
		return ContinuedQueuedTurn{}, fmt.Errorf("settlement queued Turn store is required")
	}
	record, err := service.prepareContinuation(command)
	if err != nil {
		return ContinuedQueuedTurn{}, err
	}
	return store.ContinueSettlementQueuedTurn(ctx, settlement, record)
}

func (service QueuedTurnService) prepareContinuation(command ContinueQueuedTurnCommand) (ContinueQueuedTurnRecord, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return ContinueQueuedTurnRecord{}, fmt.Errorf("queued Turn dependencies are required")
	}
	for name, value := range map[string]string{
		"tenant_id": command.TenantID, "user_id": command.UserID,
		"worker_session_id":      command.WorkerSessionID,
		"previous_worker_run_id": command.PreviousWorkerRunID,
		"previous_agent_turn_id": command.PreviousAgentTurnID,
		"runtime_instance_id":    command.RuntimeInstanceID,
		"queue_command_id":       command.QueueCommandID,
	} {
		if err := validateCanonical(name, value); err != nil {
			return ContinueQueuedTurnRecord{}, err
		}
	}
	if command.RuntimeGeneration == 0 ||
		!allRevisions(command.ExpectedSessionRevision, command.ExpectedRunRevision,
			command.ExpectedTurnRevision, command.ExpectedRuntimeRevision) {
		return ContinueQueuedTurnRecord{}, fmt.Errorf(
			"queued Turn requires a positive Runtime generation and exact revisions",
		)
	}
	values := make([]string, 4)
	var err error
	for index, kind := range []string{"worker-run", "agent-turn", "scheduled-command-turn", "integration-event"} {
		values[index], err = service.IDs.NewID(kind)
		if err != nil {
			return ContinueQueuedTurnRecord{}, err
		}
	}
	return ContinueQueuedTurnRecord{
		Command: command, WorkerRunID: values[0], AgentTurnID: values[1],
		ScheduledCommandID: values[2], EventID: values[3], Now: service.Now().UTC(),
	}, nil
}
