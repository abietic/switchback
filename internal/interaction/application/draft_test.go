package application

import (
	"encoding/json"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestTaskDraftBuilderRebuildsTaskSpecAndIgnoresModelAuthority(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	ids := contextIDs(0)
	builder := TaskDraftBuilder{
		IDs: &ids, Clock: fixedClock(now), Policy: TaskDraftPolicy{
			ProjectID: "project-local", TriggerPolicyRevision: "assistant-trigger-v1",
			NetworkProfile: currentNetworkProfile(), AgentProfile: profile,
			UserVisibleTypes: []string{"report", "patch"},
			ContextRefs:      []contractsv1.ContextRef{}, SkillRefs: []string{}, MCPRefs: []string{}, ToolRefs: []string{},
		},
	}
	claim := readyClaim(now)
	claim.Conversation.TenantID = "tenant-local"
	claim.Conversation.ActorUserID = "user-local"
	validated := validatedReadyDecision(now)
	validated.Decision.TaskDraft.TaskSpecDigest =
		"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	validated.Decision.TaskDraft.TaskSpec = json.RawMessage(`{"model_owned":true}`)
	result, err := builder.Build(claim, validated, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Draft.ID != "context-pack-1" || result.Draft.Revision != 1 ||
		result.TaskSpec.Source != "assistant" || result.TaskSpec.TenantID != "tenant-local" ||
		result.TaskSpec.UserID != "user-local" ||
		result.TaskSpec.WorkspaceResolutionRef.ResolutionID != "resolution-local" ||
		result.TaskSpec.CreationIntentRef.IntentID != "pending-confirmation" ||
		result.AcceptanceMode != "manual" ||
		result.Contract.TaskSpecDigest ==
			"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" {
		t.Fatalf("unexpected server-built TaskDraft: %+v", result)
	}
	digest, err := taskapp.CanonicalTaskSpecDigest(result.TaskSpec)
	if err != nil || digest != result.Draft.TaskSpecDigest {
		t.Fatalf("TaskSpec digest mismatch: digest=%s draft=%s error=%v", digest, result.Draft.TaskSpecDigest, err)
	}
}

func TestTaskDraftBuilderCreatesRevisionLineageAndFencesWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
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
	first, err := builder.Build(claim, validatedReadyDecision(now), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(claim, validatedReadyDecision(now), &first.Draft)
	if err != nil {
		t.Fatal(err)
	}
	if second.Draft.Revision != 2 || second.Draft.ParentTaskDraftID != first.Draft.ID {
		t.Fatalf("TaskDraft lineage was not preserved: %+v", second.Draft)
	}
	claim.Conversation.Workspace = nil
	if _, err := builder.Build(claim, validatedReadyDecision(now), &second.Draft); err == nil {
		t.Fatal("TaskDraft was built without exact Workspace")
	}
}

func validatedReadyDecision(now time.Time) ValidatedDecision {
	proposal := readyDecision(now)
	proposal.TaskDraft.Target = json.RawMessage(`{"kind":"repository","ref":"repo-local"}`)
	assessment := blockingAssessment(0)
	for dimension, state := range assessment.Dimensions {
		state.Status = domain.DimensionKnown
		assessment.Dimensions[dimension] = state
	}
	assessment.ID = "assessment-ready"
	assessment.Status = domain.ReadinessReadyConfirmed
	assessment.BlockingUnknowns = []string{}
	assessment.ClarificationQuestions = 0
	return ValidatedDecision{
		Decision: proposal, Readiness: assessment,
		Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}
