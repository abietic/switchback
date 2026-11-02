package application

import (
	"context"
	"fmt"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

type RecordStageResultCommand struct {
	Actor                 Actor
	TaskID                string
	WorkerSessionID       string
	WorkerRunID           string
	ExpectedTaskRevision  uint64
	ArtifactRefs          []contractsv1.ArtifactRef
	DeterministicEvidence bool
	IdempotencyKey        string
}

type AcceptTaskCommand struct {
	Actor                Actor
	TaskID               string
	ExpectedTaskRevision uint64
	IdempotencyKey       string
}

type TaskDeliveryRecord struct {
	Command      RecordStageResultCommand
	TransitionID string
	EventID      string
	Now          time.Time
}

type TaskAcceptanceRecord struct {
	Command      AcceptTaskCommand
	TransitionID string
	EventID      string
	Now          time.Time
}

type TaskDeliveryResult struct {
	TaskID   string
	Status   string
	Revision uint64
	EventID  string
}

type TaskDeliveryStore interface {
	RecordStageResult(context.Context, TaskDeliveryRecord) (TaskDeliveryResult, error)
	AcceptTask(context.Context, TaskAcceptanceRecord) (TaskDeliveryResult, error)
}

type TaskDeliveryService struct {
	Store TaskDeliveryStore
	IDs   IDGenerator
	Now   func() time.Time
}

func (service TaskDeliveryService) RecordStageResult(ctx context.Context, command RecordStageResultCommand) (TaskDeliveryResult, error) {
	if err := service.validate(); err != nil {
		return TaskDeliveryResult{}, err
	}
	if err := validateDeliveryCommand(command.Actor, command.TaskID, command.IdempotencyKey, command.ExpectedTaskRevision); err != nil {
		return TaskDeliveryResult{}, err
	}
	if !canonical(command.WorkerSessionID) || !canonical(command.WorkerRunID) || len(command.ArtifactRefs) == 0 {
		return TaskDeliveryResult{}, fmt.Errorf("stage result requires exact Session, Run, and ArtifactRefs")
	}
	transitionID, err := service.IDs.NewID("task-transition")
	if err != nil {
		return TaskDeliveryResult{}, err
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return TaskDeliveryResult{}, err
	}
	return service.Store.RecordStageResult(ctx, TaskDeliveryRecord{
		Command: command, TransitionID: transitionID, EventID: eventID, Now: service.Now().UTC(),
	})
}

func (service TaskDeliveryService) Accept(ctx context.Context, command AcceptTaskCommand) (TaskDeliveryResult, error) {
	if err := service.validate(); err != nil {
		return TaskDeliveryResult{}, err
	}
	if err := validateDeliveryCommand(command.Actor, command.TaskID, command.IdempotencyKey, command.ExpectedTaskRevision); err != nil {
		return TaskDeliveryResult{}, err
	}
	transitionID, err := service.IDs.NewID("task-transition")
	if err != nil {
		return TaskDeliveryResult{}, err
	}
	eventID, err := service.IDs.NewID("integration-event")
	if err != nil {
		return TaskDeliveryResult{}, err
	}
	return service.Store.AcceptTask(ctx, TaskAcceptanceRecord{
		Command: command, TransitionID: transitionID, EventID: eventID, Now: service.Now().UTC(),
	})
}

func (service TaskDeliveryService) validate() error {
	if service.Store == nil || service.IDs == nil || service.Now == nil {
		return fmt.Errorf("Task delivery dependencies are required")
	}
	return nil
}

func validateDeliveryCommand(actor Actor, taskID, idempotencyKey string, revision uint64) error {
	if !canonical(actor.TenantID) || !canonical(actor.UserID) || !canonical(taskID) ||
		!canonical(idempotencyKey) || revision == 0 {
		return fmt.Errorf("Task delivery command requires canonical identity and exact revision")
	}
	return nil
}
