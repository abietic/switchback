package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

type TaskDraftPolicy struct {
	ProjectID             string
	TriggerPolicyRevision string
	NetworkProfile        config.RuntimeNetworkProfile
	AgentProfile          config.AgentProfile
	UserVisibleTypes      []string
	ContextRefs           []contractsv1.ContextRef
	SkillRefs             []string
	MCPRefs               []string
	ToolRefs              []string
}

type TaskDraftBuildResult struct {
	Draft          domain.TaskDraft
	Contract       contractsv1.AssistantTaskDraft
	TaskSpec       contractsv1.TaskSpec
	AcceptanceMode string
}

type TaskDraftBuilder struct {
	IDs    IDGenerator
	Clock  Clock
	Policy TaskDraftPolicy
}

func (builder TaskDraftBuilder) Build(
	claim RunClaim,
	validated ValidatedDecision,
	current *domain.TaskDraft,
) (TaskDraftBuildResult, error) {
	if builder.IDs == nil || builder.Clock == nil || validated.Decision.TaskDraft == nil ||
		!validated.Readiness.Ready() || claim.Conversation.Workspace == nil {
		return TaskDraftBuildResult{}, fmt.Errorf("ready TaskDraft inputs are incomplete")
	}
	currentNetwork, networkErr := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err := builder.Policy.AgentProfile.ValidateDigest(); err != nil ||
		builder.Policy.ProjectID == "" || builder.Policy.TriggerPolicyRevision == "" ||
		networkErr != nil || builder.Policy.NetworkProfile != currentNetwork ||
		len(builder.Policy.UserVisibleTypes) == 0 {
		return TaskDraftBuildResult{}, fmt.Errorf("TaskDraft server policy is invalid")
	}
	proposal := validated.Decision.TaskDraft
	target, err := decodeTaskTarget(proposal.Target)
	if err != nil {
		return TaskDraftBuildResult{}, ErrModelPolicyConflict
	}
	assumptions := make([]contractsv1.Assumption, len(validated.Readiness.Assumptions))
	for index, assumption := range validated.Readiness.Assumptions {
		assumptions[index] = contractsv1.Assumption{
			RuleID: "assistant-default/" + strconv.Itoa(index+1), Description: assumption,
		}
	}
	spec := contractsv1.TaskSpec{
		SchemaVersion: contractsv1.TaskSpecSchemaVersion, Source: "assistant",
		TenantID: claim.Conversation.TenantID, UserID: claim.Conversation.ActorUserID,
		ProjectID: builder.Policy.ProjectID, WorkspaceID: claim.Conversation.Workspace.WorkspaceID,
		WorkspaceResolutionRef: contractsv1.WorkspaceResolutionRef{
			ResolutionID:        claim.Conversation.Workspace.ResolutionID,
			Revision:            claim.Conversation.Workspace.ResolutionRevision,
			WorkspaceRevisionID: claim.Conversation.Workspace.WorkspaceRevisionID,
			MembershipRevision:  claim.Conversation.Workspace.MembershipRevision,
		},
		Title: proposal.Title, Goal: proposal.Goal,
		ReadinessAssessmentRef: contractsv1.ReadinessAssessmentRef{
			AssessmentID: validated.Readiness.ID, PolicyRevision: validated.Readiness.PolicyRevision,
			ContentDigest: validated.Digest,
		},
		CreationIntentRef: contractsv1.CreationIntentRef{
			IntentID: "pending-confirmation", TriggerPolicyRevision: builder.Policy.TriggerPolicyRevision,
			TaskSpecDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		},
		Assumptions: assumptions, ContextRefs: append([]contractsv1.ContextRef{}, builder.Policy.ContextRefs...),
		Target: target, Constraints: append([]string{}, proposal.Constraints...),
		AcceptanceCriteria: append([]string{}, proposal.AcceptanceCriteria...),
		RuntimeConfig: contractsv1.RuntimeConfig{
			NetworkPolicy:         builder.Policy.NetworkProfile.ID,
			NetworkPolicyRevision: builder.Policy.NetworkProfile.Revision,
			NetworkPolicyDigest:   builder.Policy.NetworkProfile.Digest,
		},
		AgentConfig: contractsv1.AgentConfig{
			ProfileID:       builder.Policy.AgentProfile.ID,
			ProfileRevision: strconv.FormatUint(builder.Policy.AgentProfile.Revision, 10),
			Adapter:         builder.Policy.AgentProfile.AgentAdapter,
		},
		CapabilityRefs: contractsv1.CapabilityRefs{
			Skills:     append([]string{}, builder.Policy.SkillRefs...),
			MCPServers: append([]string{}, builder.Policy.MCPRefs...),
			Tools:      append([]string{}, builder.Policy.ToolRefs...),
		},
		ApprovalPolicy: contractsv1.ApprovalPolicy{
			SandboxOperations: "allow",
			ProtectedEffects: []string{
				"external_authentication", "external_write", "production_write", "capability_install",
			},
		},
		OutputPolicy: contractsv1.OutputPolicy{
			UserVisibleTypes: append([]string{}, builder.Policy.UserVisibleTypes...), RemoteWrites: "deny",
		},
	}
	digest, err := taskapp.CanonicalTaskSpecDigest(spec)
	if err != nil {
		return TaskDraftBuildResult{}, err
	}
	spec.CreationIntentRef.TaskSpecDigest = digest
	if err := spec.Validate(); err != nil {
		return TaskDraftBuildResult{}, fmt.Errorf("validate server-built TaskSpec: %w", err)
	}
	draftID, err := builder.IDs.NewID("assistant-task-draft")
	if err != nil {
		return TaskDraftBuildResult{}, fmt.Errorf("generate TaskDraft id: %w", err)
	}
	revision := uint64(1)
	parentID := ""
	if current != nil {
		if current.ConversationID != claim.Conversation.ID || current.Status != domain.TaskDraftCurrent {
			return TaskDraftBuildResult{}, domain.ErrInvalidState
		}
		revision = current.Revision + 1
		parentID = current.ID
	}
	taskSpecJSON, err := json.Marshal(spec)
	if err != nil {
		return TaskDraftBuildResult{}, err
	}
	now := builder.Clock.Now().UTC()
	draft, err := domain.NewTaskDraft(
		draftID, claim.Conversation.ID, claim.Conversation.ActorUserID, parentID,
		validated.Readiness.ID, claim.Conversation.Workspace.WorkspaceID,
		claim.Conversation.Workspace.WorkspaceRevisionID, revision, taskSpecJSON, digest,
		validated.Readiness.Assumptions, now,
	)
	if err != nil {
		return TaskDraftBuildResult{}, err
	}
	contract := *proposal
	contract.TaskDraftID = draft.ID
	contract.Revision = draft.Revision
	contract.InitiatorUserID = draft.InitiatorUserID
	contract.ConversationID = draft.ConversationID
	contract.WorkspaceID = draft.WorkspaceID
	contract.WorkspaceRevisionID = draft.WorkspaceRevisionID
	contract.ReadinessAssessmentID = draft.ReadinessAssessmentID
	contract.TaskSpecDigest = draft.TaskSpecDigest
	contract.Assumptions = append([]string{}, draft.Assumptions...)
	contract.TaskSpec = taskSpecJSON
	contract.CreatedAt = now
	acceptanceMode := "auto"
	for _, output := range builder.Policy.UserVisibleTypes {
		if output == "patch" {
			acceptanceMode = "manual"
		}
	}
	return TaskDraftBuildResult{
		Draft: draft, Contract: contract, TaskSpec: spec, AcceptanceMode: acceptanceMode,
	}, nil
}

func decodeTaskTarget(data json.RawMessage) (contractsv1.TaskTarget, error) {
	var target contractsv1.TaskTarget
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil || target.Kind == "" || target.Ref == "" {
		return contractsv1.TaskTarget{}, fmt.Errorf("Task target is invalid")
	}
	return target, nil
}
