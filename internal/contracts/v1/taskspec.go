package contractsv1

import "fmt"

const (
	TaskSpecSchemaVersion                  = "task.spec.v1"
	RuntimeNetworkPolicyTrustedFullV1      = "trusted_full_network_v1"
	RuntimeNetworkPolicyLegacyRestrictedV1 = "m1_public_web_restricted_v1"
)

type WorkspaceResolutionRef struct {
	ResolutionID        string `json:"resolution_id"`
	Revision            uint64 `json:"revision"`
	WorkspaceRevisionID string `json:"workspace_revision_id"`
	MembershipRevision  uint64 `json:"membership_revision"`
}

type ReadinessAssessmentRef struct {
	AssessmentID   string `json:"assessment_id"`
	PolicyRevision string `json:"policy_revision"`
	ContentDigest  string `json:"content_digest"`
}

type CreationIntentRef struct {
	IntentID              string `json:"intent_id"`
	TriggerPolicyRevision string `json:"trigger_policy_revision"`
	TaskSpecDigest        string `json:"task_spec_digest"`
}

type Assumption struct {
	RuleID      string `json:"rule_id"`
	Description string `json:"description"`
}

type ContextRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type TaskTarget struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type RuntimeConfig struct {
	NetworkPolicy         string `json:"network_policy"`
	NetworkPolicyRevision uint64 `json:"network_policy_revision,omitempty"`
	NetworkPolicyDigest   string `json:"network_policy_digest,omitempty"`
}

type AgentConfig struct {
	ProfileID       string `json:"profile_id"`
	ProfileRevision string `json:"profile_revision"`
	Adapter         string `json:"adapter"`
}

type CapabilityRefs struct {
	Skills     []string `json:"skills"`
	MCPServers []string `json:"mcp_servers"`
	Tools      []string `json:"tools"`
}

type ApprovalPolicy struct {
	SandboxOperations string   `json:"sandbox_operations"`
	ProtectedEffects  []string `json:"protected_effects"`
}

type OutputPolicy struct {
	UserVisibleTypes []string `json:"user_visible_types"`
	RemoteWrites     string   `json:"remote_writes"`
}

type TaskSpec struct {
	SchemaVersion          string                 `json:"schema_version"`
	Source                 string                 `json:"source"`
	TenantID               string                 `json:"tenant_id"`
	UserID                 string                 `json:"user_id"`
	ProjectID              string                 `json:"project_id"`
	WorkspaceID            string                 `json:"workspace_id"`
	WorkspaceResolutionRef WorkspaceResolutionRef `json:"workspace_resolution_ref"`
	Title                  string                 `json:"title"`
	Goal                   string                 `json:"goal"`
	ReadinessAssessmentRef ReadinessAssessmentRef `json:"readiness_assessment_ref"`
	CreationIntentRef      CreationIntentRef      `json:"creation_intent_ref"`
	Assumptions            []Assumption           `json:"assumptions"`
	ContextRefs            []ContextRef           `json:"context_refs"`
	Target                 TaskTarget             `json:"target"`
	Constraints            []string               `json:"constraints"`
	AcceptanceCriteria     []string               `json:"acceptance_criteria"`
	RuntimeConfig          RuntimeConfig          `json:"runtime_config"`
	AgentConfig            AgentConfig            `json:"agent_config"`
	CapabilityRefs         CapabilityRefs         `json:"capability_refs"`
	ApprovalPolicy         ApprovalPolicy         `json:"approval_policy"`
	OutputPolicy           OutputPolicy           `json:"output_policy"`
}

func DecodeTaskSpec(data []byte) (TaskSpec, error) {
	return decodeStrict(data, TaskSpec.Validate)
}

func (spec TaskSpec) Validate() error {
	if spec.SchemaVersion != TaskSpecSchemaVersion {
		return fmt.Errorf("unsupported TaskSpec schema %q", spec.SchemaVersion)
	}
	if spec.Source != "assistant" && spec.Source != "delivery" && spec.Source != "api" {
		return fmt.Errorf("unsupported TaskSpec source %q", spec.Source)
	}
	for name, value := range map[string]string{
		"tenant_id": spec.TenantID, "user_id": spec.UserID, "project_id": spec.ProjectID,
		"workspace_id": spec.WorkspaceID, "title": spec.Title, "goal": spec.Goal,
		"resolution_id":             spec.WorkspaceResolutionRef.ResolutionID,
		"workspace_revision_id":     spec.WorkspaceResolutionRef.WorkspaceRevisionID,
		"assessment_id":             spec.ReadinessAssessmentRef.AssessmentID,
		"readiness_policy_revision": spec.ReadinessAssessmentRef.PolicyRevision,
		"intent_id":                 spec.CreationIntentRef.IntentID,
		"trigger_policy_revision":   spec.CreationIntentRef.TriggerPolicyRevision,
		"target.kind":               spec.Target.Kind, "target.ref": spec.Target.Ref,
		"agent.profile_id":       spec.AgentConfig.ProfileID,
		"agent.profile_revision": spec.AgentConfig.ProfileRevision,
		"agent.adapter":          spec.AgentConfig.Adapter,
	} {
		if err := requireString(name, value); err != nil {
			return err
		}
	}
	if spec.WorkspaceResolutionRef.Revision == 0 || spec.WorkspaceResolutionRef.MembershipRevision == 0 {
		return fmt.Errorf("Workspace resolution and membership revisions must be positive")
	}
	if err := requireSHA256("readiness content digest", spec.ReadinessAssessmentRef.ContentDigest, true); err != nil {
		return err
	}
	if err := requireSHA256("TaskSpec digest", spec.CreationIntentRef.TaskSpecDigest, true); err != nil {
		return err
	}
	if spec.Assumptions == nil || spec.ContextRefs == nil {
		return fmt.Errorf("assumptions and context_refs must be explicit arrays")
	}
	if err := requireStrings("constraints", spec.Constraints, false); err != nil {
		return err
	}
	if err := requireStrings("acceptance_criteria", spec.AcceptanceCriteria, true); err != nil {
		return err
	}
	switch spec.RuntimeConfig.NetworkPolicy {
	case RuntimeNetworkPolicyTrustedFullV1:
		if spec.RuntimeConfig.NetworkPolicyRevision == 0 {
			return fmt.Errorf("runtime network policy revision must be positive")
		}
		if err := requireSHA256(
			"runtime network policy digest",
			spec.RuntimeConfig.NetworkPolicyDigest,
			true,
		); err != nil {
			return err
		}
	case RuntimeNetworkPolicyLegacyRestrictedV1:
		if spec.RuntimeConfig.NetworkPolicyRevision != 0 ||
			spec.RuntimeConfig.NetworkPolicyDigest != "" {
			return fmt.Errorf("legacy runtime network policy must not invent enforcement evidence")
		}
	default:
		return fmt.Errorf("unsupported runtime network policy %q", spec.RuntimeConfig.NetworkPolicy)
	}
	if spec.AgentConfig.Adapter != "codex-acp" {
		return fmt.Errorf("unsupported M1 agent adapter %q", spec.AgentConfig.Adapter)
	}
	if spec.CapabilityRefs.Skills == nil || spec.CapabilityRefs.MCPServers == nil || spec.CapabilityRefs.Tools == nil {
		return fmt.Errorf("capability refs must be explicit arrays")
	}
	if spec.ApprovalPolicy.SandboxOperations != "allow" {
		return fmt.Errorf("ordinary sandbox operations must be allowed")
	}
	if err := requireStrings("approval_policy.protected_effects", spec.ApprovalPolicy.ProtectedEffects, true); err != nil {
		return err
	}
	if spec.OutputPolicy.RemoteWrites != "deny" {
		return fmt.Errorf("M1 remote writes must be denied")
	}
	if err := requireStrings("output_policy.user_visible_types", spec.OutputPolicy.UserVisibleTypes, true); err != nil {
		return err
	}
	for _, artifactType := range spec.OutputPolicy.UserVisibleTypes {
		if artifactType != "report" && artifactType != "patch" {
			return fmt.Errorf("unsupported M1 user-visible Artifact type %q", artifactType)
		}
	}
	return nil
}
