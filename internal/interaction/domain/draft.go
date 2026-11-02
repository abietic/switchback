package domain

import (
	"fmt"
	"time"
)

type TaskDraftStatus string

const (
	TaskDraftCurrent     TaskDraftStatus = "current"
	TaskDraftInvalidated TaskDraftStatus = "invalidated"
	TaskDraftConfirmed   TaskDraftStatus = "confirmed"
)

type TaskDraft struct {
	ID                    string
	ConversationID        string
	InitiatorUserID       string
	Revision              uint64
	ParentTaskDraftID     string
	ReadinessAssessmentID string
	WorkspaceID           string
	WorkspaceRevisionID   string
	TaskSpecSnapshot      []byte
	TaskSpecDigest        string
	Assumptions           []string
	Status                TaskDraftStatus
	CreatedAt             time.Time
	InvalidatedAt         time.Time
}

func NewTaskDraft(
	id, conversationID, initiatorUserID, parentID, readinessID, workspaceID, workspaceRevisionID string,
	revision uint64,
	taskSpec []byte,
	digest string,
	assumptions []string,
	now time.Time,
) (TaskDraft, error) {
	draft := TaskDraft{
		ID: id, ConversationID: conversationID, InitiatorUserID: initiatorUserID,
		ParentTaskDraftID: parentID, ReadinessAssessmentID: readinessID,
		WorkspaceID: workspaceID, WorkspaceRevisionID: workspaceRevisionID,
		Revision: revision, TaskSpecSnapshot: append([]byte(nil), taskSpec...),
		TaskSpecDigest: digest, Assumptions: append([]string(nil), assumptions...),
		Status: TaskDraftCurrent, CreatedAt: now.UTC(),
	}
	if err := draft.Validate(); err != nil {
		return TaskDraft{}, err
	}
	return draft, nil
}

func (draft TaskDraft) Validate() error {
	for name, value := range map[string]string{
		"TaskDraft id": draft.ID, "Conversation id": draft.ConversationID,
		"initiator user id": draft.InitiatorUserID, "readiness assessment id": draft.ReadinessAssessmentID,
		"Workspace id": draft.WorkspaceID, "Workspace revision id": draft.WorkspaceRevisionID,
		"TaskSpec digest": draft.TaskSpecDigest,
	} {
		if !canonical(value) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if draft.Revision == 0 || len(draft.TaskSpecSnapshot) == 0 || draft.CreatedAt.IsZero() {
		return fmt.Errorf("TaskDraft revision, snapshot, and creation time are required")
	}
	if draft.ParentTaskDraftID != "" && !canonical(draft.ParentTaskDraftID) {
		return fmt.Errorf("parent TaskDraft id is invalid")
	}
	for _, assumption := range draft.Assumptions {
		if !canonical(assumption) {
			return fmt.Errorf("TaskDraft assumption is invalid")
		}
	}
	if draft.Status != TaskDraftCurrent && draft.Status != TaskDraftInvalidated && draft.Status != TaskDraftConfirmed {
		return fmt.Errorf("TaskDraft status is invalid")
	}
	if (draft.Status == TaskDraftInvalidated) != !draft.InvalidatedAt.IsZero() {
		return fmt.Errorf("TaskDraft invalidation state is inconsistent")
	}
	return nil
}

func (draft *TaskDraft) Invalidate(now time.Time) error {
	if draft.Status != TaskDraftCurrent || now.IsZero() || now.Before(draft.CreatedAt) {
		return ErrInvalidState
	}
	draft.Status = TaskDraftInvalidated
	draft.InvalidatedAt = now.UTC()
	return nil
}

func (draft *TaskDraft) Confirm() error {
	if draft.Status != TaskDraftCurrent {
		return ErrInvalidState
	}
	draft.Status = TaskDraftConfirmed
	return nil
}
