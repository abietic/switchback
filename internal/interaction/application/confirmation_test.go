package application

import (
	"errors"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestTaskConfirmationBindsExactReadyStateAndReplacesPendingIntent(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	state, command := confirmationFixture(t, now)
	ids := contextIDs(0)
	prepared, err := (TaskConfirmationPreparer{
		IDs: &ids, Clock: fixedClock(now), TriggerPolicyRevision: "assistant-trigger-v1",
		IntentTTL: 15 * time.Minute,
	}).Prepare(command, state)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Intent.IntentID != "context-pack-1" ||
		prepared.Intent.ConversationRevision != state.Conversation.Revision ||
		prepared.Intent.TaskDraftRevision != state.Draft.Revision ||
		prepared.TaskSpec.CreationIntentRef.IntentID != prepared.Intent.IntentID ||
		prepared.TaskSpec.CreationIntentRef.TaskSpecDigest != state.Draft.TaskSpecDigest ||
		prepared.RequestDigest == "" {
		t.Fatalf("confirmation binding is incomplete: %+v", prepared)
	}
}

func TestTaskConfirmationRejectsEveryStaleBinding(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	state, base := confirmationFixture(t, now)
	mutations := map[string]func(*ConfirmTaskDraftCommand){
		"actor":        func(command *ConfirmTaskDraftCommand) { command.Actor.UserID = "other" },
		"conversation": func(command *ConfirmTaskDraftCommand) { command.ExpectedConversationRevision++ },
		"draft":        func(command *ConfirmTaskDraftCommand) { command.TaskDraftRevision++ },
		"readiness":    func(command *ConfirmTaskDraftCommand) { command.ReadinessAssessmentRevision++ },
		"workspace":    func(command *ConfirmTaskDraftCommand) { command.WorkspaceRevisionID = "other" },
		"resolution":   func(command *ConfirmTaskDraftCommand) { command.WorkspaceResolutionRevision++ },
		"membership":   func(command *ConfirmTaskDraftCommand) { command.MembershipRevision++ },
		"digest": func(command *ConfirmTaskDraftCommand) {
			command.TaskSpecDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		},
		"policy": func(command *ConfirmTaskDraftCommand) { command.TriggerPolicyRevision = "other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			command := base
			mutate(&command)
			ids := contextIDs(0)
			_, err := (TaskConfirmationPreparer{
				IDs: &ids, Clock: fixedClock(now), TriggerPolicyRevision: "assistant-trigger-v1",
				IntentTTL: 15 * time.Minute,
			}).Prepare(command, state)
			if !errors.Is(err, ErrConfirmationStale) {
				t.Fatalf("stale %s binding = %v", name, err)
			}
		})
	}
}

func TestTaskConfirmationRejectsInvalidatedDraft(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	state, command := confirmationFixture(t, now)
	state.Draft.Status = domain.TaskDraftInvalidated
	state.Draft.InvalidatedAt = now
	ids := contextIDs(0)
	if _, err := (TaskConfirmationPreparer{
		IDs: &ids, Clock: fixedClock(now), TriggerPolicyRevision: "assistant-trigger-v1",
		IntentTTL: 15 * time.Minute,
	}).Prepare(command, state); !errors.Is(err, ErrConfirmationStale) {
		t.Fatalf("invalidated draft confirmation = %v", err)
	}
}

func confirmationFixture(t *testing.T, now time.Time) (ConfirmationState, ConfirmTaskDraftCommand) {
	t.Helper()
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	ids := contextIDs(0)
	builder := TaskDraftBuilder{
		IDs: &ids, Clock: fixedClock(now), Policy: TaskDraftPolicy{
			ProjectID: "project-local", TriggerPolicyRevision: "assistant-trigger-v1",
			NetworkProfile: currentNetworkProfile(), AgentProfile: profile,
			UserVisibleTypes: []string{"report"}, ContextRefs: []contractsv1.ContextRef{},
			SkillRefs: []string{}, MCPRefs: []string{}, ToolRefs: []string{},
		},
	}
	claim := readyClaim(now)
	claim.Conversation.TenantID = "tenant-local"
	claim.Conversation.ActorUserID = "user-local"
	claim.Conversation.Status = domain.ConversationActive
	validated := validatedReadyDecision(now)
	built, err := builder.Build(claim, validated, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := ConfirmationState{
		Conversation: claim.Conversation, Draft: built.Draft,
		Readiness: validated.Readiness, TaskSpec: built.TaskSpec,
	}
	command := ConfirmTaskDraftCommand{
		Actor:          taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"},
		ConversationID: state.Conversation.ID, ExpectedConversationRevision: state.Conversation.Revision,
		TaskDraftID: state.Draft.ID, TaskDraftRevision: state.Draft.Revision,
		ReadinessAssessmentID: state.Readiness.ID, ReadinessAssessmentRevision: state.Readiness.Revision,
		WorkspaceID:                 state.Conversation.Workspace.WorkspaceID,
		WorkspaceRevisionID:         state.Conversation.Workspace.WorkspaceRevisionID,
		WorkspaceResolutionID:       state.Conversation.Workspace.ResolutionID,
		WorkspaceResolutionRevision: state.Conversation.Workspace.ResolutionRevision,
		MembershipRevision:          state.Conversation.Workspace.MembershipRevision,
		TaskSpecDigest:              state.Draft.TaskSpecDigest, TriggerPolicyRevision: "assistant-trigger-v1",
		IdempotencyKey: "confirm-1",
	}
	if reflect.ValueOf(command).IsZero() {
		t.Fatal("confirmation fixture is empty")
	}
	return state, command
}
