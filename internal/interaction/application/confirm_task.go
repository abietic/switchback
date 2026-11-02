package application

import (
	"context"
	"fmt"
)

type ConfirmTaskDraftResult struct {
	CommandID    string `json:"command_id"`
	TaskID       string `json:"task_id"`
	TaskRevision uint64 `json:"task_revision"`
	Status       string `json:"status"`
	TaskViewRef  string `json:"task_view_ref"`
}

type ConfirmTaskDraft struct {
	States    ConfirmationStateRepository
	Preparer  TaskConfirmationPreparer
	Admission TaskAdmission
}

func (service ConfirmTaskDraft) Execute(
	ctx context.Context,
	command ConfirmTaskDraftCommand,
) (ConfirmTaskDraftResult, error) {
	if service.States == nil || service.Admission == nil {
		return ConfirmTaskDraftResult{}, fmt.Errorf("ConfirmTaskDraft dependencies are required")
	}
	if previous, found, err := service.States.FindConfirmationResult(ctx, command); err != nil {
		return ConfirmTaskDraftResult{}, err
	} else if found {
		return previous, nil
	}
	state, err := service.States.LoadConfirmationState(ctx, command)
	if err != nil {
		return ConfirmTaskDraftResult{}, err
	}
	prepared, err := service.Preparer.Prepare(command, state)
	if err != nil {
		return ConfirmTaskDraftResult{}, err
	}
	acceptanceMode := "auto"
	for _, artifactType := range prepared.TaskSpec.OutputPolicy.UserVisibleTypes {
		if artifactType == "patch" {
			acceptanceMode = "manual"
		}
	}
	created, err := service.Admission.CreateAssistantTask(ctx, AssistantTaskRequest{
		Actor: command.Actor, TaskSpec: prepared.TaskSpec, AcceptanceMode: acceptanceMode,
		IdempotencyKey: command.IdempotencyKey, AssistantIntent: prepared.Intent,
		Readiness: state.Readiness, AssistantReadinessRevision: command.ReadinessAssessmentRevision,
	})
	if err != nil {
		return ConfirmTaskDraftResult{}, err
	}
	return ConfirmTaskDraftResult{
		CommandID: created.EventID, TaskID: created.TaskID, TaskRevision: created.Revision,
		Status: created.Status, TaskViewRef: "/api/v1/views/task_summary/" + created.TaskID,
	}, nil
}
