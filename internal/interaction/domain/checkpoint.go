package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

type CheckpointStatus string

const (
	CheckpointActive    CheckpointStatus = "active"
	CheckpointResolved  CheckpointStatus = "resolved"
	CheckpointCancelled CheckpointStatus = "cancelled"
)

type CheckpointAction string

const (
	CheckpointContinue    CheckpointAction = "continue"
	CheckpointNarrowScope CheckpointAction = "narrow_scope"
	CheckpointCancel      CheckpointAction = "cancel"
)

type ClarificationCheckpoint struct {
	ID                    string
	ConversationID        string
	Revision              uint64
	TaskDraftID           string
	TaskDraftRevision     uint64
	WorkingDraft          json.RawMessage
	ReadinessAssessmentID string
	Assumptions           []string
	Blockers              []string
	EvidenceRefs          []string
	AllowedActions        []CheckpointAction
	Status                CheckpointStatus
	ResolvedAction        CheckpointAction
	CreatedAt             time.Time
	ResolvedAt            time.Time
}

func (checkpoint ClarificationCheckpoint) Validate() error {
	if !canonical(checkpoint.ID) || !canonical(checkpoint.ConversationID) ||
		checkpoint.Revision == 0 || !canonical(checkpoint.ReadinessAssessmentID) ||
		len(checkpoint.WorkingDraft) == 0 || !json.Valid(checkpoint.WorkingDraft) ||
		len(checkpoint.Blockers) == 0 || len(checkpoint.AllowedActions) == 0 ||
		checkpoint.CreatedAt.IsZero() {
		return fmt.Errorf("ClarificationCheckpoint is incomplete")
	}
	if (checkpoint.TaskDraftID == "") != (checkpoint.TaskDraftRevision == 0) {
		return fmt.Errorf("ClarificationCheckpoint TaskDraft target is inconsistent")
	}
	if checkpoint.TaskDraftID != "" && !canonical(checkpoint.TaskDraftID) {
		return fmt.Errorf("ClarificationCheckpoint TaskDraft id is invalid")
	}
	if checkpoint.Status != CheckpointActive && checkpoint.Status != CheckpointResolved &&
		checkpoint.Status != CheckpointCancelled {
		return fmt.Errorf("ClarificationCheckpoint status is invalid")
	}
	if (checkpoint.Status == CheckpointActive) != checkpoint.ResolvedAt.IsZero() {
		return fmt.Errorf("ClarificationCheckpoint resolution state is inconsistent")
	}
	return nil
}

func (checkpoint *ClarificationCheckpoint) Apply(
	expectedRevision uint64,
	action CheckpointAction,
	now time.Time,
) error {
	if checkpoint.Revision != expectedRevision {
		return ErrStaleRevision
	}
	if checkpoint.Status != CheckpointActive || now.IsZero() || now.Before(checkpoint.CreatedAt) ||
		!checkpoint.allows(action) {
		return ErrInvalidState
	}
	checkpoint.ResolvedAction = action
	checkpoint.ResolvedAt = now.UTC()
	checkpoint.Revision++
	if action == CheckpointCancel {
		checkpoint.Status = CheckpointCancelled
	} else {
		checkpoint.Status = CheckpointResolved
	}
	return nil
}

func (checkpoint ClarificationCheckpoint) allows(action CheckpointAction) bool {
	for _, allowed := range checkpoint.AllowedActions {
		if allowed == action {
			return true
		}
	}
	return false
}
