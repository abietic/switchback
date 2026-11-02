package application

import (
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

var ErrConfirmationStale = errors.New("TaskDraft confirmation target is stale")

type ConfirmTaskDraftCommand struct {
	Actor                        taskapp.Actor
	ConversationID               string
	ExpectedConversationRevision uint64
	TaskDraftID                  string
	TaskDraftRevision            uint64
	ReadinessAssessmentID        string
	ReadinessAssessmentRevision  uint64
	WorkspaceID                  string
	WorkspaceRevisionID          string
	WorkspaceResolutionID        string
	WorkspaceResolutionRevision  uint64
	MembershipRevision           uint64
	TaskSpecDigest               string
	TriggerPolicyRevision        string
	IdempotencyKey               string
}

type ConfirmationState struct {
	Conversation domain.Conversation
	Draft        domain.TaskDraft
	Readiness    domain.TaskReadinessAssessment
	TaskSpec     contractsv1.TaskSpec
}

type PreparedConfirmation struct {
	Intent         contractsv1.AssistantCreationIntent
	TaskSpec       contractsv1.TaskSpec
	RequestDigest  string
	IdempotencyKey string
}

type TaskConfirmationPreparer struct {
	IDs                   IDGenerator
	Clock                 Clock
	TriggerPolicyRevision string
	IntentTTL             time.Duration
}

func (preparer TaskConfirmationPreparer) Prepare(
	command ConfirmTaskDraftCommand,
	state ConfirmationState,
) (PreparedConfirmation, error) {
	if preparer.IDs == nil || preparer.Clock == nil || preparer.TriggerPolicyRevision == "" ||
		preparer.IntentTTL <= 0 {
		return PreparedConfirmation{}, fmt.Errorf("Task confirmation dependencies are required")
	}
	if command.IdempotencyKey == "" || command.TriggerPolicyRevision != preparer.TriggerPolicyRevision {
		return PreparedConfirmation{}, ErrConfirmationStale
	}
	if !confirmationMatches(command, state) {
		return PreparedConfirmation{}, ErrConfirmationStale
	}
	intentID, err := preparer.IDs.NewID("explicit-task-creation-intent")
	if err != nil {
		return PreparedConfirmation{}, fmt.Errorf("generate creation intent id: %w", err)
	}
	now := preparer.Clock.Now().UTC()
	intent := contractsv1.AssistantCreationIntent{
		SchemaVersion: contractsv1.CreationIntentSchemaVersion, IntentID: intentID,
		ActorUserID: command.Actor.UserID, TenantID: command.Actor.TenantID,
		ConversationID: state.Conversation.ID, ConversationRevision: state.Conversation.Revision,
		TaskDraftID: state.Draft.ID, TaskDraftRevision: state.Draft.Revision,
		ReadinessAssessmentID:       state.Readiness.ID,
		WorkspaceID:                 state.Conversation.Workspace.WorkspaceID,
		WorkspaceRevisionID:         state.Conversation.Workspace.WorkspaceRevisionID,
		WorkspaceResolutionID:       state.Conversation.Workspace.ResolutionID,
		WorkspaceResolutionRevision: state.Conversation.Workspace.ResolutionRevision,
		MembershipRevision:          state.Conversation.Workspace.MembershipRevision,
		TaskSpecDigest:              state.Draft.TaskSpecDigest,
		TriggerPolicyRevision:       preparer.TriggerPolicyRevision,
		CreatedAt:                   now, ExpiresAt: now.Add(preparer.IntentTTL),
	}
	if err := intent.Validate(); err != nil {
		return PreparedConfirmation{}, err
	}
	taskSpec := state.TaskSpec
	taskSpec.CreationIntentRef = contractsv1.CreationIntentRef{
		IntentID: intent.IntentID, TriggerPolicyRevision: intent.TriggerPolicyRevision,
		TaskSpecDigest: intent.TaskSpecDigest,
	}
	if err := taskSpec.Validate(); err != nil {
		return PreparedConfirmation{}, fmt.Errorf("confirmed TaskSpec is invalid: %w", err)
	}
	requestDigest, err := digestJSON(struct {
		Command ConfirmTaskDraftCommand `json:"command"`
		Intent  string                  `json:"intent_id"`
	}{Command: command, Intent: intent.IntentID})
	if err != nil {
		return PreparedConfirmation{}, err
	}
	return PreparedConfirmation{
		Intent: intent, TaskSpec: taskSpec, RequestDigest: requestDigest,
		IdempotencyKey: command.IdempotencyKey,
	}, nil
}

func confirmationMatches(command ConfirmTaskDraftCommand, state ConfirmationState) bool {
	if state.Conversation.Workspace == nil || state.Conversation.Status != domain.ConversationActive ||
		state.Draft.Status != domain.TaskDraftCurrent || !state.Readiness.Ready() {
		return false
	}
	workspace := state.Conversation.Workspace
	return command.Actor.TenantID == state.Conversation.TenantID &&
		command.Actor.UserID == state.Conversation.ActorUserID &&
		command.Actor.UserID == state.Draft.InitiatorUserID &&
		command.ConversationID == state.Conversation.ID &&
		command.ExpectedConversationRevision == state.Conversation.Revision &&
		command.TaskDraftID == state.Draft.ID && command.TaskDraftRevision == state.Draft.Revision &&
		command.ReadinessAssessmentID == state.Readiness.ID &&
		command.ReadinessAssessmentRevision == state.Readiness.Revision &&
		command.WorkspaceID == workspace.WorkspaceID &&
		command.WorkspaceRevisionID == workspace.WorkspaceRevisionID &&
		command.WorkspaceResolutionID == workspace.ResolutionID &&
		command.WorkspaceResolutionRevision == workspace.ResolutionRevision &&
		command.MembershipRevision == workspace.MembershipRevision &&
		command.TaskSpecDigest == state.Draft.TaskSpecDigest &&
		command.TaskSpecDigest == state.TaskSpec.CreationIntentRef.TaskSpecDigest &&
		command.TriggerPolicyRevision == state.TaskSpec.CreationIntentRef.TriggerPolicyRevision &&
		state.Draft.ReadinessAssessmentID == state.Readiness.ID &&
		state.Draft.WorkspaceID == workspace.WorkspaceID &&
		state.Draft.WorkspaceRevisionID == workspace.WorkspaceRevisionID
}
