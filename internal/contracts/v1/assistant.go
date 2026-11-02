package contractsv1

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	ConversationCommandSchemaVersion     = "assistant.conversation_command.v1"
	AssistantDecisionSchemaVersion       = "assistant.decision.v1"
	AssistantProjectionSchemaVersion     = "assistant.projection_event.v1"
	TaskDraftSchemaVersion               = "assistant.task_draft.v1"
	ReadinessAssessmentSchemaVersion     = "assistant.readiness_assessment.v1"
	ClarificationCheckpointSchemaVersion = "assistant.clarification_checkpoint.v1"
	CreationIntentSchemaVersion          = "assistant.creation_intent.v1"
)

var readinessDimensions = []string{
	"goal",
	"deliverable_acceptance",
	"scope_target",
	"constraints",
	"context_access",
	"risk_approval",
	"execution_policy",
}

type ConversationCommand struct {
	SchemaVersion               string `json:"schema_version"`
	CommandType                 string `json:"command_type"`
	ConversationID              string `json:"conversation_id"`
	ExpectedRevision            uint64 `json:"expected_revision"`
	IdempotencyKey              string `json:"idempotency_key"`
	Content                     string `json:"content"`
	AssistantRunID              string `json:"assistant_run_id"`
	AssistantRunRevision        uint64 `json:"assistant_run_revision"`
	WorkspaceID                 string `json:"workspace_id"`
	WorkspaceRevisionID         string `json:"workspace_revision_id"`
	WorkspaceResolutionID       string `json:"workspace_resolution_id"`
	WorkspaceResolutionRevision uint64 `json:"workspace_resolution_revision"`
	MembershipRevision          uint64 `json:"membership_revision"`
	CheckpointID                string `json:"checkpoint_id"`
	CheckpointRevision          uint64 `json:"checkpoint_revision"`
	CheckpointAction            string `json:"checkpoint_action"`
	TaskDraftID                 string `json:"task_draft_id"`
	TaskDraftRevision           uint64 `json:"task_draft_revision"`
	ReadinessAssessmentID       string `json:"readiness_assessment_id"`
	ReadinessAssessmentRevision uint64 `json:"readiness_assessment_revision"`
	TaskSpecDigest              string `json:"task_spec_digest"`
	TriggerPolicyRevision       string `json:"trigger_policy_revision"`
}

func DecodeConversationCommand(data []byte) (ConversationCommand, error) {
	return decodeStrict(data, ConversationCommand.Validate)
}

func (command ConversationCommand) Validate() error {
	if command.SchemaVersion != ConversationCommandSchemaVersion {
		return fmt.Errorf("unsupported Conversation command schema version")
	}
	if err := requireID("idempotency_key", command.IdempotencyKey); err != nil {
		return err
	}
	switch command.CommandType {
	case "create_conversation":
		if command.ConversationID != "" || command.ExpectedRevision != 0 {
			return fmt.Errorf("create Conversation cannot target an existing revision")
		}
		return nil
	case "open_conversation":
		return command.validateConversationTarget()
	case "send_message":
		if err := command.validateConversationTarget(); err != nil {
			return err
		}
		if err := requireString("content", command.Content); err != nil {
			return err
		}
	case "select_workspace":
		if err := command.validateConversationTarget(); err != nil {
			return err
		}
		if err := requireID("workspace_id", command.WorkspaceID); err != nil {
			return err
		}
		if err := requireID("workspace_revision_id", command.WorkspaceRevisionID); err != nil {
			return err
		}
		if err := requireID("workspace_resolution_id", command.WorkspaceResolutionID); err != nil {
			return err
		}
		if command.WorkspaceResolutionRevision == 0 || command.MembershipRevision == 0 {
			return fmt.Errorf("Workspace resolution evidence is incomplete")
		}
	case "checkpoint_action":
		if err := command.validateConversationTarget(); err != nil {
			return err
		}
		if err := requireID("checkpoint_id", command.CheckpointID); err != nil {
			return err
		}
		if command.CheckpointRevision == 0 ||
			(command.CheckpointAction != "continue" && command.CheckpointAction != "narrow_scope" &&
				command.CheckpointAction != "cancel") {
			return fmt.Errorf("checkpoint target is invalid")
		}
	case "confirm_task":
		if err := command.validateConversationTarget(); err != nil {
			return err
		}
		if err := command.validateConfirmation(); err != nil {
			return err
		}
	case "cancel_run", "retry_run":
		if err := command.validateConversationTarget(); err != nil {
			return err
		}
		if err := requireID("assistant_run_id", command.AssistantRunID); err != nil {
			return err
		}
		if command.AssistantRunRevision == 0 {
			return fmt.Errorf("assistant_run_revision must be positive")
		}
	default:
		return fmt.Errorf("unsupported Conversation command type")
	}
	return nil
}

func (command ConversationCommand) validateConversationTarget() error {
	if err := requireID("conversation_id", command.ConversationID); err != nil {
		return err
	}
	if command.ExpectedRevision == 0 {
		return fmt.Errorf("expected_revision must be positive")
	}
	return nil
}

func (command ConversationCommand) validateConfirmation() error {
	for name, value := range map[string]string{
		"task_draft_id": command.TaskDraftID, "readiness_assessment_id": command.ReadinessAssessmentID,
		"workspace_id": command.WorkspaceID, "workspace_revision_id": command.WorkspaceRevisionID,
		"workspace_resolution_id": command.WorkspaceResolutionID,
		"trigger_policy_revision": command.TriggerPolicyRevision,
	} {
		if err := requireID(name, value); err != nil {
			return err
		}
	}
	if command.TaskDraftRevision == 0 || command.ReadinessAssessmentRevision == 0 ||
		command.WorkspaceResolutionRevision == 0 || command.MembershipRevision == 0 {
		return fmt.Errorf("confirmation revisions must be positive")
	}
	return requireSHA256("task_spec_digest", command.TaskSpecDigest, true)
}

type ReadinessDimensionState struct {
	Dimension    string   `json:"dimension"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type AssistantReadinessAssessment struct {
	SchemaVersion          string                    `json:"schema_version"`
	AssessmentID           string                    `json:"assessment_id"`
	Revision               uint64                    `json:"revision"`
	PolicyRevision         string                    `json:"policy_revision"`
	Dimensions             []ReadinessDimensionState `json:"dimensions"`
	BlockingUnknowns       []string                  `json:"blocking_unknowns"`
	Assumptions            []string                  `json:"assumptions"`
	ClarificationQuestions uint64                    `json:"clarification_questions"`
	Status                 string                    `json:"status"`
	EvaluatedAt            time.Time                 `json:"evaluated_at"`
}

func (assessment AssistantReadinessAssessment) Validate() error {
	if assessment.SchemaVersion != ReadinessAssessmentSchemaVersion {
		return fmt.Errorf("unsupported readiness assessment schema version")
	}
	if err := requireID("assessment_id", assessment.AssessmentID); err != nil {
		return err
	}
	if assessment.Revision == 0 || assessment.EvaluatedAt.IsZero() {
		return fmt.Errorf("readiness assessment revision and evaluation time are required")
	}
	if err := requireID("policy_revision", assessment.PolicyRevision); err != nil {
		return err
	}
	if len(assessment.Dimensions) != len(readinessDimensions) {
		return fmt.Errorf("readiness assessment must contain seven dimensions")
	}
	seen := make(map[string]struct{}, len(assessment.Dimensions))
	for _, dimension := range assessment.Dimensions {
		if !contains(readinessDimensions, dimension.Dimension) ||
			!contains([]string{"known", "assumed", "blocking_unknown", "not_applicable"}, dimension.Status) {
			return fmt.Errorf("readiness dimension is invalid")
		}
		if _, duplicate := seen[dimension.Dimension]; duplicate {
			return fmt.Errorf("readiness dimension is duplicated")
		}
		if err := requireString("dimension summary", dimension.Summary); err != nil {
			return err
		}
		if err := requireStrings("dimension evidence_refs", dimension.EvidenceRefs, false); err != nil {
			return err
		}
		seen[dimension.Dimension] = struct{}{}
	}
	if err := requireStrings("blocking_unknowns", assessment.BlockingUnknowns, false); err != nil {
		return err
	}
	if err := requireStrings("assumptions", assessment.Assumptions, false); err != nil {
		return err
	}
	if !contains([]string{"clarifying", "ready_confirmed", "ready_with_assumptions"}, assessment.Status) {
		return fmt.Errorf("readiness status is invalid")
	}
	return nil
}

type AssistantTaskDraft struct {
	SchemaVersion         string          `json:"schema_version"`
	TaskDraftID           string          `json:"task_draft_id"`
	Revision              uint64          `json:"revision"`
	InitiatorUserID       string          `json:"initiator_user_id"`
	ConversationID        string          `json:"conversation_id"`
	WorkspaceID           string          `json:"workspace_id"`
	WorkspaceRevisionID   string          `json:"workspace_revision_id"`
	ReadinessAssessmentID string          `json:"readiness_assessment_id"`
	TaskSpecDigest        string          `json:"task_spec_digest"`
	Title                 string          `json:"title"`
	Goal                  string          `json:"goal"`
	Target                json.RawMessage `json:"target"`
	Constraints           []string        `json:"constraints"`
	AcceptanceCriteria    []string        `json:"acceptance_criteria"`
	Assumptions           []string        `json:"assumptions"`
	TaskSpec              json.RawMessage `json:"task_spec"`
	CreatedAt             time.Time       `json:"created_at"`
}

func (draft AssistantTaskDraft) Validate() error {
	if draft.SchemaVersion != TaskDraftSchemaVersion || draft.Revision == 0 || draft.CreatedAt.IsZero() {
		return fmt.Errorf("TaskDraft schema, revision, and creation time are required")
	}
	for name, value := range map[string]string{
		"task_draft_id": draft.TaskDraftID, "initiator_user_id": draft.InitiatorUserID,
		"conversation_id": draft.ConversationID, "workspace_id": draft.WorkspaceID,
		"workspace_revision_id":   draft.WorkspaceRevisionID,
		"readiness_assessment_id": draft.ReadinessAssessmentID,
	} {
		if err := requireID(name, value); err != nil {
			return err
		}
	}
	if err := requireSHA256("task_spec_digest", draft.TaskSpecDigest, true); err != nil {
		return err
	}
	if err := requireString("title", draft.Title); err != nil {
		return err
	}
	if err := requireString("goal", draft.Goal); err != nil {
		return err
	}
	if err := requireJSONObject("target", draft.Target); err != nil {
		return err
	}
	if err := requireJSONObject("task_spec", draft.TaskSpec); err != nil {
		return err
	}
	if err := requireStrings("constraints", draft.Constraints, false); err != nil {
		return err
	}
	if err := requireStrings("acceptance_criteria", draft.AcceptanceCriteria, true); err != nil {
		return err
	}
	return requireStrings("assumptions", draft.Assumptions, false)
}

type AssistantClarificationCheckpoint struct {
	SchemaVersion         string          `json:"schema_version"`
	CheckpointID          string          `json:"checkpoint_id"`
	Revision              uint64          `json:"revision"`
	ConversationID        string          `json:"conversation_id"`
	TaskDraftID           string          `json:"task_draft_id"`
	TaskDraftRevision     uint64          `json:"task_draft_revision"`
	WorkingDraft          json.RawMessage `json:"working_draft"`
	ReadinessAssessmentID string          `json:"readiness_assessment_id"`
	Assumptions           []string        `json:"assumptions"`
	Blockers              []string        `json:"blockers"`
	EvidenceRefs          []string        `json:"evidence_refs"`
	AllowedActions        []string        `json:"allowed_actions"`
	CreatedAt             time.Time       `json:"created_at"`
}

func (checkpoint AssistantClarificationCheckpoint) Validate() error {
	if checkpoint.SchemaVersion != ClarificationCheckpointSchemaVersion ||
		checkpoint.Revision == 0 || checkpoint.CreatedAt.IsZero() {
		return fmt.Errorf("checkpoint schema, revision, and creation time are required")
	}
	for name, value := range map[string]string{
		"checkpoint_id": checkpoint.CheckpointID, "conversation_id": checkpoint.ConversationID,
		"readiness_assessment_id": checkpoint.ReadinessAssessmentID,
	} {
		if err := requireID(name, value); err != nil {
			return err
		}
	}
	if err := requireJSONObject("working_draft", checkpoint.WorkingDraft); err != nil {
		return err
	}
	if checkpoint.TaskDraftID == "" && checkpoint.TaskDraftRevision != 0 {
		return fmt.Errorf("partial checkpoint cannot carry a TaskDraft revision")
	}
	if checkpoint.TaskDraftID != "" {
		if err := requireID("task_draft_id", checkpoint.TaskDraftID); err != nil {
			return err
		}
		if checkpoint.TaskDraftRevision == 0 {
			return fmt.Errorf("task_draft_revision must be positive")
		}
	}
	if err := requireStrings("assumptions", checkpoint.Assumptions, false); err != nil {
		return err
	}
	if err := requireStrings("blockers", checkpoint.Blockers, true); err != nil {
		return err
	}
	if err := requireStrings("evidence_refs", checkpoint.EvidenceRefs, false); err != nil {
		return err
	}
	if err := requireStrings("allowed_actions", checkpoint.AllowedActions, true); err != nil {
		return err
	}
	for _, action := range checkpoint.AllowedActions {
		if !contains([]string{"continue", "narrow_scope", "cancel"}, action) {
			return fmt.Errorf("checkpoint action is invalid")
		}
	}
	return nil
}

type AssistantCreationIntent struct {
	SchemaVersion               string    `json:"schema_version"`
	IntentID                    string    `json:"intent_id"`
	ActorUserID                 string    `json:"actor_user_id"`
	TenantID                    string    `json:"tenant_id"`
	ConversationID              string    `json:"conversation_id"`
	ConversationRevision        uint64    `json:"conversation_revision"`
	TaskDraftID                 string    `json:"task_draft_id"`
	TaskDraftRevision           uint64    `json:"task_draft_revision"`
	ReadinessAssessmentID       string    `json:"readiness_assessment_id"`
	WorkspaceID                 string    `json:"workspace_id"`
	WorkspaceRevisionID         string    `json:"workspace_revision_id"`
	WorkspaceResolutionID       string    `json:"workspace_resolution_id"`
	WorkspaceResolutionRevision uint64    `json:"workspace_resolution_revision"`
	MembershipRevision          uint64    `json:"membership_revision"`
	TaskSpecDigest              string    `json:"task_spec_digest"`
	TriggerPolicyRevision       string    `json:"trigger_policy_revision"`
	CreatedAt                   time.Time `json:"created_at"`
	ExpiresAt                   time.Time `json:"expires_at"`
}

func (intent AssistantCreationIntent) Validate() error {
	if intent.SchemaVersion != CreationIntentSchemaVersion ||
		intent.ConversationRevision == 0 || intent.TaskDraftRevision == 0 ||
		intent.WorkspaceResolutionRevision == 0 || intent.MembershipRevision == 0 ||
		intent.CreatedAt.IsZero() || !intent.ExpiresAt.After(intent.CreatedAt) {
		return fmt.Errorf("creation intent schema, revisions, and lifetime are invalid")
	}
	for name, value := range map[string]string{
		"intent_id": intent.IntentID, "actor_user_id": intent.ActorUserID, "tenant_id": intent.TenantID,
		"conversation_id": intent.ConversationID, "task_draft_id": intent.TaskDraftID,
		"readiness_assessment_id": intent.ReadinessAssessmentID, "workspace_id": intent.WorkspaceID,
		"workspace_revision_id":   intent.WorkspaceRevisionID,
		"workspace_resolution_id": intent.WorkspaceResolutionID,
		"trigger_policy_revision": intent.TriggerPolicyRevision,
	} {
		if err := requireID(name, value); err != nil {
			return err
		}
	}
	return requireSHA256("task_spec_digest", intent.TaskSpecDigest, true)
}

type AssistantDecision struct {
	SchemaVersion         string                            `json:"schema_version"`
	DecisionType          string                            `json:"decision_type"`
	UserVisibleContent    string                            `json:"user_visible_content"`
	ClarificationQuestion string                            `json:"clarification_question"`
	Readiness             AssistantReadinessAssessment      `json:"readiness"`
	TaskDraft             *AssistantTaskDraft               `json:"task_draft"`
	Checkpoint            *AssistantClarificationCheckpoint `json:"checkpoint"`
}

func DecodeAssistantDecision(data []byte) (AssistantDecision, error) {
	return decodeStrict(data, AssistantDecision.Validate)
}

func (decision AssistantDecision) Validate() error {
	if decision.SchemaVersion != AssistantDecisionSchemaVersion {
		return fmt.Errorf("unsupported AssistantDecision schema version")
	}
	if err := requireString("user_visible_content", decision.UserVisibleContent); err != nil {
		return err
	}
	if err := decision.Readiness.Validate(); err != nil {
		return err
	}
	switch decision.DecisionType {
	case "clarification":
		if err := requireString("clarification_question", decision.ClarificationQuestion); err != nil {
			return err
		}
		if decision.TaskDraft != nil {
			return fmt.Errorf("clarification cannot include a TaskDraft")
		}
	case "ready_draft":
		if decision.TaskDraft == nil || decision.ClarificationQuestion != "" {
			return fmt.Errorf("ready draft decision must contain one TaskDraft")
		}
		if err := decision.TaskDraft.Validate(); err != nil {
			return err
		}
	case "informational":
		if decision.ClarificationQuestion != "" || decision.TaskDraft != nil {
			return fmt.Errorf("informational decision cannot propose work")
		}
	default:
		return fmt.Errorf("AssistantDecision type is invalid")
	}
	if decision.Checkpoint != nil {
		return decision.Checkpoint.Validate()
	}
	return nil
}

type AssistantProjectionEvent struct {
	SchemaVersion        string          `json:"schema_version"`
	EventType            string          `json:"event_type"`
	ConversationID       string          `json:"conversation_id"`
	ConversationRevision uint64          `json:"conversation_revision"`
	AssistantRunID       string          `json:"assistant_run_id"`
	CommandID            string          `json:"command_id"`
	ProjectionRef        string          `json:"projection_ref"`
	Payload              json.RawMessage `json:"payload"`
	OccurredAt           time.Time       `json:"occurred_at"`
}

func DecodeAssistantProjectionEvent(data []byte) (AssistantProjectionEvent, error) {
	return decodeStrict(data, AssistantProjectionEvent.Validate)
}

func (event AssistantProjectionEvent) Validate() error {
	if event.SchemaVersion != AssistantProjectionSchemaVersion || event.ConversationRevision == 0 ||
		event.OccurredAt.IsZero() {
		return fmt.Errorf("Assistant projection schema, revision, and time are required")
	}
	for name, value := range map[string]string{
		"conversation_id": event.ConversationID, "assistant_run_id": event.AssistantRunID,
		"command_id": event.CommandID,
	} {
		if err := requireID(name, value); err != nil {
			return err
		}
	}
	if !contains([]string{"message_accepted", "run_started", "clarification_ready", "draft_ready",
		"checkpoint_ready", "task_requested", "task_linked", "run_failed", "run_cancelled"}, event.EventType) {
		return fmt.Errorf("Assistant projection event type is invalid")
	}
	if err := requireString("projection_ref", event.ProjectionRef); err != nil {
		return err
	}
	return requireJSONObject("payload", event.Payload)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
