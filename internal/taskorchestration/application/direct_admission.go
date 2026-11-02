package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

var (
	ErrTaskNotReady           = errors.New("direct Task input is not ready")
	ErrAdmissionConfiguration = errors.New("direct admission configuration is invalid")
)

type IDGenerator interface {
	NewID(kind string) (string, error)
}

type Actor struct {
	TenantID string
	UserID   string
}

type WorkspaceRevisionEvidence struct {
	TenantID            string
	WorkspaceID         string
	WorkspaceRevisionID string
	ResolutionID        string
	ResolutionRevision  uint64
	MembershipRevision  uint64
	Readable            bool
}

type AgentProfileEvidence struct {
	ProfileID string
	Revision  string
	Enabled   bool
}

type DirectTaskRequest struct {
	ProjectID          string
	Title              string
	Goal               string
	TargetKind         string
	TargetRef          string
	Constraints        []string
	AcceptanceCriteria []string
	AcceptanceMode     string
	UserVisibleTypes   []string
}

type DirectAdmissionCommand struct {
	Actor        Actor
	Workspace    WorkspaceRevisionEvidence
	AgentProfile AgentProfileEvidence
	Request      DirectTaskRequest
}

type DirectAdmissionPolicy struct {
	ReadinessPolicyRevision string
	TriggerPolicyRevision   string
	NetworkProfile          config.RuntimeNetworkProfile
	IntentTTL               time.Duration
}

type DirectAdmissionResult struct {
	Assessment domain.TaskReadinessAssessment
	Intent     domain.ExplicitTaskCreationIntent
	TaskSpec   contractsv1.TaskSpec
}

type DirectAdmission struct {
	IDs    IDGenerator
	Now    func() time.Time
	Policy DirectAdmissionPolicy
}

func (service DirectAdmission) Admit(command DirectAdmissionCommand) (DirectAdmissionResult, error) {
	if err := service.validateConfiguration(); err != nil {
		return DirectAdmissionResult{}, err
	}
	now := service.Now().UTC()
	assessmentID, err := service.IDs.NewID("task-readiness-assessment")
	if err != nil {
		return DirectAdmissionResult{}, fmt.Errorf("generate readiness assessment id: %w", err)
	}
	assessment := assessDirectCommand(assessmentID, service.Policy.ReadinessPolicyRevision, command, now)
	result := DirectAdmissionResult{Assessment: assessment}
	if len(assessment.BlockingUnknowns) != 0 {
		return result, ErrTaskNotReady
	}

	assessmentDigest, err := canonicalDigest(assessment)
	if err != nil {
		return DirectAdmissionResult{}, fmt.Errorf("digest readiness assessment: %w", err)
	}
	spec := buildTaskSpec(service.Policy, command, assessment, assessmentDigest)
	taskSpecDigest, err := CanonicalTaskSpecDigest(spec)
	if err != nil {
		return DirectAdmissionResult{}, err
	}
	intentID, err := service.IDs.NewID("explicit-task-creation-intent")
	if err != nil {
		return DirectAdmissionResult{}, fmt.Errorf("generate creation intent id: %w", err)
	}
	ttl := service.Policy.IntentTTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	intent := domain.ExplicitTaskCreationIntent{
		IntentID: intentID, ActorUserID: command.Actor.UserID, TenantID: command.Actor.TenantID,
		TaskSpecDigest: taskSpecDigest, WorkspaceID: command.Workspace.WorkspaceID,
		WorkspaceRevisionID:   command.Workspace.WorkspaceRevisionID,
		ReadinessAssessmentID: assessment.AssessmentID,
		TriggerPolicyRevision: service.Policy.TriggerPolicyRevision,
		CreatedAt:             now, ExpiresAt: now.Add(ttl),
	}
	spec.CreationIntentRef = contractsv1.CreationIntentRef{
		IntentID: intent.IntentID, TriggerPolicyRevision: intent.TriggerPolicyRevision,
		TaskSpecDigest: intent.TaskSpecDigest,
	}
	if err := spec.Validate(); err != nil {
		return DirectAdmissionResult{}, fmt.Errorf("validate admitted TaskSpec: %w", err)
	}
	result.Intent = intent
	result.TaskSpec = spec
	return result, nil
}

func (service DirectAdmission) validateConfiguration() error {
	if service.IDs == nil || service.Now == nil {
		return fmt.Errorf("%w: ids and clock are required", ErrAdmissionConfiguration)
	}
	currentNetwork, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if service.Policy.ReadinessPolicyRevision == "" ||
		service.Policy.TriggerPolicyRevision == "" ||
		err != nil ||
		service.Policy.NetworkProfile != currentNetwork ||
		service.Policy.IntentTTL < 0 {
		return ErrAdmissionConfiguration
	}
	return nil
}

func assessDirectCommand(
	assessmentID string,
	policyRevision string,
	command DirectAdmissionCommand,
	now time.Time,
) domain.TaskReadinessAssessment {
	dimensions := make(map[domain.ReadinessDimension]domain.DimensionStatus)
	for _, dimension := range domain.RequiredReadinessDimensions() {
		dimensions[dimension] = domain.DimensionKnown
	}
	blockers := make([]string, 0)
	block := func(dimension domain.ReadinessDimension, reason string) {
		dimensions[dimension] = domain.DimensionBlockingUnknown
		blockers = append(blockers, reason)
	}
	if !canonical(command.Actor.TenantID) || !canonical(command.Actor.UserID) {
		block(domain.DimensionContextAccess, "actor_identity_missing")
	}
	if !canonical(command.Request.Goal) || !canonical(command.Request.Title) {
		block(domain.DimensionGoal, "goal_or_title_missing")
	}
	if len(command.Request.AcceptanceCriteria) == 0 || len(command.Request.UserVisibleTypes) == 0 {
		block(domain.DimensionDeliverableAcceptance, "deliverable_or_acceptance_missing")
	}
	if !canonical(command.Request.TargetKind) || !canonical(command.Request.TargetRef) ||
		command.Workspace.WorkspaceID == "" || command.Workspace.WorkspaceRevisionID == "" {
		block(domain.DimensionScopeTarget, "scope_or_target_missing")
	}
	if command.Request.Constraints == nil {
		block(domain.DimensionConstraints, "constraints_not_explicit")
	}
	if !command.Workspace.Readable || command.Workspace.TenantID != command.Actor.TenantID ||
		command.Workspace.ResolutionRevision == 0 || command.Workspace.MembershipRevision == 0 {
		block(domain.DimensionContextAccess, "workspace_not_readable_or_stale")
	}
	if command.Request.AcceptanceMode != "auto" && command.Request.AcceptanceMode != "manual" {
		block(domain.DimensionRiskApproval, "acceptance_mode_invalid")
	}
	for _, artifactType := range command.Request.UserVisibleTypes {
		if artifactType != "report" && artifactType != "patch" {
			block(domain.DimensionDeliverableAcceptance, "unsupported_output_type")
		}
		if artifactType == "patch" && command.Request.AcceptanceMode != "manual" {
			block(domain.DimensionRiskApproval, "patch_requires_manual_acceptance")
		}
	}
	if !command.AgentProfile.Enabled || !canonical(command.AgentProfile.ProfileID) || !canonical(command.AgentProfile.Revision) {
		block(domain.DimensionExecutionPolicy, "agent_profile_not_enabled")
	}
	status := domain.ReadinessReadyConfirmed
	if len(blockers) != 0 {
		status = domain.ReadinessClarifying
	}
	return domain.TaskReadinessAssessment{
		AssessmentID: assessmentID, Source: "api", PolicyRevision: policyRevision,
		Dimensions: dimensions, BlockingUnknowns: blockers, Assumptions: []string{},
		ClarificationQuestions: 0, Status: status, EvaluatedAt: now,
	}
}

func buildTaskSpec(
	policy DirectAdmissionPolicy,
	command DirectAdmissionCommand,
	assessment domain.TaskReadinessAssessment,
	assessmentDigest string,
) contractsv1.TaskSpec {
	return contractsv1.TaskSpec{
		SchemaVersion: contractsv1.TaskSpecSchemaVersion,
		Source:        "api", TenantID: command.Actor.TenantID, UserID: command.Actor.UserID,
		ProjectID: command.Request.ProjectID, WorkspaceID: command.Workspace.WorkspaceID,
		WorkspaceResolutionRef: contractsv1.WorkspaceResolutionRef{
			ResolutionID: command.Workspace.ResolutionID, Revision: command.Workspace.ResolutionRevision,
			WorkspaceRevisionID: command.Workspace.WorkspaceRevisionID,
			MembershipRevision:  command.Workspace.MembershipRevision,
		},
		Title: command.Request.Title, Goal: command.Request.Goal,
		ReadinessAssessmentRef: contractsv1.ReadinessAssessmentRef{
			AssessmentID: assessment.AssessmentID, PolicyRevision: assessment.PolicyRevision,
			ContentDigest: assessmentDigest,
		},
		CreationIntentRef: contractsv1.CreationIntentRef{},
		Assumptions:       []contractsv1.Assumption{}, ContextRefs: []contractsv1.ContextRef{},
		Target:             contractsv1.TaskTarget{Kind: command.Request.TargetKind, Ref: command.Request.TargetRef},
		Constraints:        append([]string{}, command.Request.Constraints...),
		AcceptanceCriteria: append([]string{}, command.Request.AcceptanceCriteria...),
		RuntimeConfig: contractsv1.RuntimeConfig{
			NetworkPolicy:         policy.NetworkProfile.ID,
			NetworkPolicyRevision: policy.NetworkProfile.Revision,
			NetworkPolicyDigest:   policy.NetworkProfile.Digest,
		},
		AgentConfig: contractsv1.AgentConfig{
			ProfileID: command.AgentProfile.ProfileID, ProfileRevision: command.AgentProfile.Revision,
			Adapter: "codex-acp",
		},
		CapabilityRefs: contractsv1.CapabilityRefs{Skills: []string{}, MCPServers: []string{}, Tools: []string{}},
		ApprovalPolicy: contractsv1.ApprovalPolicy{
			SandboxOperations: "allow",
			ProtectedEffects:  []string{"external_authentication", "external_write", "production_write", "capability_install"},
		},
		OutputPolicy: contractsv1.OutputPolicy{
			UserVisibleTypes: append([]string{}, command.Request.UserVisibleTypes...), RemoteWrites: "deny",
		},
	}
}

func CanonicalTaskSpecDigest(spec contractsv1.TaskSpec) (string, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal TaskSpec creation payload: %w", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("decode TaskSpec creation payload: %w", err)
	}
	delete(payload, "creation_intent_ref")
	return canonicalDigest(payload)
}

func canonicalDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonical(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
