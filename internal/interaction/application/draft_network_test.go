package application

import (
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
)

func TestTaskDraftBuilderEmitsCurrentServerNetworkProfile(t *testing.T) {
	now := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	ids := contextIDs(0)
	builder := TaskDraftBuilder{
		IDs: &ids, Clock: fixedClock(now), Policy: TaskDraftPolicy{
			ProjectID: "project-local", TriggerPolicyRevision: "assistant-trigger-v1",
			NetworkProfile: network, AgentProfile: agent,
			UserVisibleTypes: []string{"report"}, ContextRefs: []contractsv1.ContextRef{},
			SkillRefs: []string{}, MCPRefs: []string{}, ToolRefs: []string{},
		},
	}
	claim := readyClaim(now)
	claim.Conversation.TenantID = "tenant-local"
	claim.Conversation.ActorUserID = "user-local"

	result, err := builder.Build(claim, validatedReadyDecision(now), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskSpec.RuntimeConfig.NetworkPolicy != network.ID ||
		result.TaskSpec.RuntimeConfig.NetworkPolicyRevision != network.Revision ||
		result.TaskSpec.RuntimeConfig.NetworkPolicyDigest != network.Digest {
		t.Fatalf("runtime config = %+v", result.TaskSpec.RuntimeConfig)
	}
}

func TestTaskDraftBuilderRejectsLegacyNetworkProfileForNewDraft(t *testing.T) {
	now := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	agent, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	ids := contextIDs(0)
	builder := TaskDraftBuilder{
		IDs: &ids, Clock: fixedClock(now), Policy: TaskDraftPolicy{
			ProjectID: "project-local", TriggerPolicyRevision: "assistant-trigger-v1",
			NetworkProfile: config.RuntimeNetworkProfile{
				ID: config.LegacyPublicWebRestrictedV1,
			},
			AgentProfile: agent, UserVisibleTypes: []string{"report"},
			ContextRefs: []contractsv1.ContextRef{}, SkillRefs: []string{},
			MCPRefs: []string{}, ToolRefs: []string{},
		},
	}

	if _, err := builder.Build(readyClaim(now), validatedReadyDecision(now), nil); err == nil {
		t.Fatal("TaskDraft accepted a legacy network profile for new admission")
	}
}

func currentNetworkProfile() config.RuntimeNetworkProfile {
	profile, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	return profile
}
