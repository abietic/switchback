package application

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type TaskContinuationInteraction string

const (
	TaskContinuationChat   TaskContinuationInteraction = "chat"
	TaskContinuationRework TaskContinuationInteraction = "rework"
)

type ContinueTaskWithWorkerCommand struct {
	Actor                   Actor
	TaskID                  string
	ExpectedTaskRevision    uint64
	WorkerSessionID         string
	ExpectedSessionRevision uint64
	PreviousWorkerRunID     string
	ExpectedRunRevision     uint64
	PreviousAgentTurnID     string
	ExpectedTurnRevision    uint64
	InteractionKind         TaskContinuationInteraction
	Message                 string
	IdempotencyKey          string
}

type ContinueTaskWithWorkerRecord struct {
	Command            ContinueTaskWithWorkerCommand
	WorkerRunID        string
	AgentTurnID        string
	ScheduledCommandID string
	TransitionID       string
	EventID            string
	RequestDigest      string
	Now                time.Time
}

type ContinuedTask struct {
	TaskID          string                      `json:"task_id"`
	Status          string                      `json:"status"`
	Revision        uint64                      `json:"revision"`
	WorkerSessionID string                      `json:"worker_session_id"`
	SessionRevision uint64                      `json:"session_revision"`
	WorkerRunID     string                      `json:"worker_run_id"`
	AgentTurnID     string                      `json:"agent_turn_id"`
	InteractionKind TaskContinuationInteraction `json:"interaction_kind"`
	EventID         string                      `json:"event_id"`
}

type ReworkTaskCommand struct {
	Actor                Actor
	TaskID               string
	ExpectedTaskRevision uint64
	Message              string
	IdempotencyKey       string
}

type ReworkTaskRecord struct {
	Command            ReworkTaskCommand
	WorkerRunID        string
	AgentTurnID        string
	ScheduledCommandID string
	TransitionID       string
	EventID            string
	RequestDigest      string
	Now                time.Time
}

type ReworkedTask struct {
	TaskID          string
	Status          string
	Revision        uint64
	WorkerSessionID string
	WorkerRunID     string
	AgentTurnID     string
	EventID         string
}

type CancelTaskCommand struct {
	Actor                Actor
	TaskID               string
	ExpectedTaskRevision uint64
	IdempotencyKey       string
}

type CancelTaskRecord struct {
	Command          CancelTaskCommand
	TransitionID     string
	EventID          string
	ControlCommandID string
	Now              time.Time
}

type CancelledTask struct {
	TaskID   string
	Status   string
	Revision uint64
	EventID  string
}

type SettleCancelledTaskCommand struct {
	Actor                   Actor
	TaskID                  string
	WorkerSessionID         string
	WorkerRunID             string
	ExpectedTaskRevision    uint64
	ExpectedSessionRevision uint64
	IdempotencyKey          string
}

type SettleCancelledTaskRecord struct {
	Command SettleCancelledTaskCommand
	EventID string
	Now     time.Time
}

type SettledCancelledTask struct {
	TaskID          string
	WorkerSessionID string
	SessionRevision uint64
	EventID         string
}

type TaskActionStore interface {
	ContinueTaskWithWorker(context.Context, ContinueTaskWithWorkerRecord) (ContinuedTask, error)
	ReworkTask(context.Context, ReworkTaskRecord) (ReworkedTask, error)
	CancelTask(context.Context, CancelTaskRecord) (CancelledTask, error)
	SettleCancelledTask(context.Context, SettleCancelledTaskRecord) (SettledCancelledTask, error)
}

type TaskActionService struct {
	Store TaskActionStore
	IDs   IDGenerator
	Now   func() time.Time
}

func (service TaskActionService) ContinueTaskWithWorker(
	ctx context.Context,
	command ContinueTaskWithWorkerCommand,
) (ContinuedTask, error) {
	if err := service.validate(); err != nil {
		return ContinuedTask{}, err
	}
	for name, value := range map[string]string{
		"tenant_id": command.Actor.TenantID, "user_id": command.Actor.UserID,
		"task_id": command.TaskID, "worker_session_id": command.WorkerSessionID,
		"previous_worker_run_id": command.PreviousWorkerRunID,
		"previous_agent_turn_id": command.PreviousAgentTurnID,
		"idempotency_key":        command.IdempotencyKey,
	} {
		if !canonical(value) {
			return ContinuedTask{}, fmt.Errorf("%s must be canonical", name)
		}
	}
	if command.ExpectedTaskRevision == 0 || command.ExpectedSessionRevision == 0 ||
		command.ExpectedRunRevision == 0 || command.ExpectedTurnRevision == 0 {
		return ContinuedTask{}, fmt.Errorf("Task continuation requires exact revisions")
	}
	if command.InteractionKind != TaskContinuationChat &&
		command.InteractionKind != TaskContinuationRework {
		return ContinuedTask{}, fmt.Errorf("Task continuation interaction kind is invalid")
	}
	if strings.TrimSpace(command.Message) == "" || strings.ContainsRune(command.Message, '\x00') {
		return ContinuedTask{}, fmt.Errorf("Task continuation message is required")
	}
	requestDigest, err := canonicalDigest(struct {
		Actor                   Actor                       `json:"actor"`
		TaskID                  string                      `json:"task_id"`
		ExpectedTaskRevision    uint64                      `json:"expected_task_revision"`
		WorkerSessionID         string                      `json:"worker_session_id"`
		ExpectedSessionRevision uint64                      `json:"expected_session_revision"`
		PreviousWorkerRunID     string                      `json:"previous_worker_run_id"`
		ExpectedRunRevision     uint64                      `json:"expected_run_revision"`
		PreviousAgentTurnID     string                      `json:"previous_agent_turn_id"`
		ExpectedTurnRevision    uint64                      `json:"expected_turn_revision"`
		InteractionKind         TaskContinuationInteraction `json:"interaction_kind"`
		Message                 string                      `json:"message"`
	}{
		Actor: command.Actor, TaskID: command.TaskID,
		ExpectedTaskRevision:    command.ExpectedTaskRevision,
		WorkerSessionID:         command.WorkerSessionID,
		ExpectedSessionRevision: command.ExpectedSessionRevision,
		PreviousWorkerRunID:     command.PreviousWorkerRunID,
		ExpectedRunRevision:     command.ExpectedRunRevision,
		PreviousAgentTurnID:     command.PreviousAgentTurnID,
		ExpectedTurnRevision:    command.ExpectedTurnRevision,
		InteractionKind:         command.InteractionKind, Message: command.Message,
	})
	if err != nil {
		return ContinuedTask{}, err
	}
	values, err := service.actionIDs()
	if err != nil {
		return ContinuedTask{}, err
	}
	return service.Store.ContinueTaskWithWorker(ctx, ContinueTaskWithWorkerRecord{
		Command: command, WorkerRunID: values[0], AgentTurnID: values[1],
		ScheduledCommandID: values[2], TransitionID: values[3], EventID: values[4],
		RequestDigest: requestDigest, Now: service.Now().UTC(),
	})
}

func (service TaskActionService) Rework(ctx context.Context, command ReworkTaskCommand) (ReworkedTask, error) {
	if err := service.validate(); err != nil {
		return ReworkedTask{}, err
	}
	if err := validateDeliveryCommand(command.Actor, command.TaskID, command.IdempotencyKey, command.ExpectedTaskRevision); err != nil {
		return ReworkedTask{}, err
	}
	if strings.TrimSpace(command.Message) == "" || strings.ContainsRune(command.Message, '\x00') {
		return ReworkedTask{}, fmt.Errorf("Task rework message is required")
	}
	requestDigest, err := canonicalDigest(struct {
		Actor                Actor  `json:"actor"`
		TaskID               string `json:"task_id"`
		ExpectedTaskRevision uint64 `json:"expected_task_revision"`
		Message              string `json:"message"`
	}{
		Actor: command.Actor, TaskID: command.TaskID,
		ExpectedTaskRevision: command.ExpectedTaskRevision, Message: command.Message,
	})
	if err != nil {
		return ReworkedTask{}, err
	}
	values, err := service.actionIDs()
	if err != nil {
		return ReworkedTask{}, err
	}
	return service.Store.ReworkTask(ctx, ReworkTaskRecord{
		Command: command, WorkerRunID: values[0], AgentTurnID: values[1],
		ScheduledCommandID: values[2], TransitionID: values[3], EventID: values[4],
		RequestDigest: requestDigest, Now: service.Now().UTC(),
	})
}

func (service TaskActionService) actionIDs() ([]string, error) {
	values := make([]string, 5)
	for index, kind := range []string{
		"worker-run", "agent-turn", "scheduled-command-turn", "task-transition", "integration-event",
	} {
		value, err := service.IDs.NewID(kind)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

func (service TaskActionService) Cancel(ctx context.Context, command CancelTaskCommand) (CancelledTask, error) {
	if err := service.validate(); err != nil {
		return CancelledTask{}, err
	}
	if err := validateDeliveryCommand(command.Actor, command.TaskID, command.IdempotencyKey, command.ExpectedTaskRevision); err != nil {
		return CancelledTask{}, err
	}
	values := make([]string, 3)
	var err error
	for index, kind := range []string{"task-transition", "integration-event", "worker-user-command"} {
		values[index], err = service.IDs.NewID(kind)
		if err != nil {
			return CancelledTask{}, err
		}
	}
	return service.Store.CancelTask(ctx, CancelTaskRecord{
		Command: command, TransitionID: values[0], EventID: values[1],
		ControlCommandID: values[2], Now: service.Now().UTC(),
	})
}

func (service TaskActionService) SettleCancelled(
	ctx context.Context,
	command SettleCancelledTaskCommand,
) (SettledCancelledTask, error) {
	if err := service.validate(); err != nil {
		return SettledCancelledTask{}, err
	}
	for name, value := range map[string]string{
		"tenant_id": command.Actor.TenantID, "user_id": command.Actor.UserID,
		"task_id": command.TaskID, "worker_session_id": command.WorkerSessionID,
		"worker_run_id": command.WorkerRunID, "idempotency_key": command.IdempotencyKey,
	} {
		if !canonical(value) {
			return SettledCancelledTask{}, fmt.Errorf("%s must be canonical", name)
		}
	}
	if command.ExpectedTaskRevision == 0 || command.ExpectedSessionRevision == 0 {
		return SettledCancelledTask{}, fmt.Errorf("cancel settlement requires exact revisions")
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return SettledCancelledTask{}, err
	}
	return service.Store.SettleCancelledTask(ctx, SettleCancelledTaskRecord{
		Command: command, EventID: eventID, Now: service.Now().UTC(),
	})
}

func (service TaskActionService) validate() error {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return fmt.Errorf("Task action dependencies are required")
	}
	return nil
}
