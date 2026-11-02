package application

import (
	"context"
	"fmt"
	"time"
)

type PauseTaskCommand struct {
	Actor                Actor
	TaskID               string
	WorkerSessionID      string
	WorkerRunID          string
	ExpectedTaskRevision uint64
	Reason               string
	IdempotencyKey       string
}

type PauseTaskRecord struct {
	Command      PauseTaskCommand
	TransitionID string
	EventID      string
	Now          time.Time
}

type PausedTask struct {
	TaskID   string
	Status   string
	Revision uint64
	EventID  string
}

type TaskPauseStore interface {
	PauseTask(context.Context, PauseTaskRecord) (PausedTask, error)
}

type TaskPauseService struct {
	Store TaskPauseStore
	IDs   IDGenerator
	Now   func() time.Time
}

func (service TaskPauseService) Execute(ctx context.Context, command PauseTaskCommand) (PausedTask, error) {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return PausedTask{}, fmt.Errorf("Task pause dependencies are required")
	}
	if err := validateDeliveryCommand(command.Actor, command.TaskID, command.IdempotencyKey, command.ExpectedTaskRevision); err != nil {
		return PausedTask{}, err
	}
	if !canonical(command.WorkerSessionID) || !canonical(command.WorkerRunID) || !canonical(command.Reason) {
		return PausedTask{}, fmt.Errorf("Task pause requires exact Worker identity and reason")
	}
	transitionID, err := service.IDs.NewID("task-transition")
	if err != nil {
		return PausedTask{}, err
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return PausedTask{}, err
	}
	return service.Store.PauseTask(ctx, PauseTaskRecord{
		Command: command, TransitionID: transitionID, EventID: eventID, Now: service.Now().UTC(),
	})
}
