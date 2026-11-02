package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
)

const (
	AssistantFailureContextCompile = "context_compile_failed"
	AssistantFailureModelTimeout   = "model_timeout"
	AssistantFailureModelMalformed = "model_malformed"
	AssistantFailureModelPolicy    = "model_policy_conflict"
	AssistantFailureModelUpstream  = "model_upstream"
)

// AssistantRunProcessor owns the one-way transition from a claimed run to a
// committed decision or a fenced terminal failure. It never creates a Task:
// exact user confirmation remains the only handoff to Task Orchestration.
type AssistantRunProcessor struct {
	Runs           AssistantRunRepository
	Context        ContextCompiler
	CurrentDrafts  CurrentTaskDraftReader
	Model          AssistantModel
	Validator      DecisionValidator
	DraftBuilder   TaskDraftBuilder
	Clarifications ClarificationPlanner
	Effects        PublishAssistantDerivedEffects
	IDs            IDGenerator
	Clock          Clock
}

func (processor AssistantRunProcessor) ProcessAssistantRun(ctx context.Context, claim RunClaim) error {
	if err := processor.validate(claim); err != nil {
		return err
	}

	pack, err := processor.Context.Compile(ctx, claim)
	if err != nil {
		return processor.commitFailure(ctx, claim, claim.Run.Revision, AssistantFailureContextCompile, true, nil)
	}
	if !processor.Clock.Now().UTC().Before(claim.Run.TimeoutAt) {
		return processor.commitFailure(ctx, claim, claim.Run.Revision, AssistantFailureModelTimeout, true, nil)
	}
	invocationRevision, err := processor.Runs.MarkInvocationStarted(
		ctx, claim.Run.ID, claim.Run.Revision, claim.Run.FencingToken, processor.Clock.Now().UTC(),
	)
	if err != nil {
		return err
	}

	modelContext, cancel := context.WithDeadline(ctx, claim.Run.TimeoutAt)
	result, modelErr := processor.Model.Complete(modelContext, ModelRequest{
		RunID: claim.Run.ID, ConversationID: claim.Conversation.ID,
		ProfileID: claim.Run.ModelProfile, ProfileRevision: claim.ModelProfileRevision,
		ProfileDigest: claim.ModelProfileDigest, PolicyRevision: claim.Run.PolicyRevision, Context: pack,
	})
	cancel()
	if modelErr != nil {
		return processor.commitFailure(ctx, claim, invocationRevision, failureCode(modelErr), true, nil)
	}

	validated, err := processor.Validator.Validate(claim, pack, result.Decision)
	if err != nil {
		return processor.commitFailure(ctx, claim, invocationRevision, failureCode(err), true, &result)
	}
	decision := validated.Decision
	decision.Checkpoint = nil // Checkpoints are application-generated, never model authority.
	var draft *TaskDraftRecord
	var checkpoint *domain.ClarificationCheckpoint
	switch decision.DecisionType {
	case "ready_draft":
		current, loadErr := processor.CurrentDrafts.LoadCurrentTaskDraft(ctx, claim.Conversation.ID)
		if loadErr != nil {
			return processor.commitFailure(ctx, claim, invocationRevision, AssistantFailureContextCompile, true, &result)
		}
		built, buildErr := processor.DraftBuilder.Build(claim, validated, taskDraft(current))
		if buildErr != nil {
			return processor.commitFailure(ctx, claim, invocationRevision, AssistantFailureModelPolicy, true, &result)
		}
		decision.TaskDraft = &built.Contract
		draft = &TaskDraftRecord{Draft: built.Draft, TaskSpec: built.TaskSpec}
	case "clarification":
		current, loadErr := processor.CurrentDrafts.LoadCurrentTaskDraft(ctx, claim.Conversation.ID)
		if loadErr != nil {
			return processor.commitFailure(ctx, claim, invocationRevision, AssistantFailureContextCompile, true, &result)
		}
		workingDraft := initialWorkingDraft(claim, nil)
		if current != nil {
			workingDraft = append(json.RawMessage(nil), current.Draft.TaskSpecSnapshot...)
		}
		plan, planErr := processor.Clarifications.Plan(validated.Readiness, workingDraft, currentDraftID(current), currentDraftRevision(current))
		if planErr != nil {
			return processor.commitFailure(ctx, claim, invocationRevision, AssistantFailureModelPolicy, true, &result)
		}
		decision.UserVisibleContent = plan.Question
		decision.ClarificationQuestion = plan.Question
		if plan.Checkpoint != nil {
			checkpoint = plan.Checkpoint
			decision.Checkpoint = checkpointContract(*checkpoint)
		}
	case "informational":
		// Informational decisions still persist a readiness snapshot and visible Turn.
	default:
		return processor.commitFailure(ctx, claim, invocationRevision, AssistantFailureModelMalformed, true, &result)
	}

	turnID, err := processor.IDs.NewID("assistant-turn")
	if err != nil {
		return err
	}
	event, err := processor.decisionEvent(claim, turnID, decision, processor.Clock.Now().UTC())
	if err != nil {
		return err
	}
	occurredAt := processor.Clock.Now().UTC()
	commit := RunDecisionCommit{
		ConversationID: claim.Conversation.ID, ExpectedConversationRevision: claim.Conversation.Revision,
		RunID: claim.Run.ID, ExpectedRunRevision: invocationRevision, FencingToken: claim.Run.FencingToken,
		AssistantTurn: domain.Turn{ID: turnID, CorrelationID: claim.Run.ID}, Decision: decision,
		Readiness: ReadinessRecord{Assessment: validated.Readiness, Workspace: claim.Conversation.Workspace, Digest: validated.Digest},
		TaskDraft: draft, Checkpoint: checkpoint, Event: event, OccurredAt: occurredAt,
	}
	if err := processor.Runs.CommitDecision(ctx, commit); err != nil {
		return err
	}
	return processor.publishEffects(ctx, claim, decision, checkpoint, result, occurredAt)
}

func (processor AssistantRunProcessor) validate(claim RunClaim) error {
	if processor.Runs == nil || processor.Context.Repository == nil || processor.Context.IDs == nil || processor.Context.Budget <= 0 ||
		processor.CurrentDrafts == nil || processor.Model == nil || processor.IDs == nil || processor.Clock == nil ||
		processor.Validator.IDs == nil || processor.Validator.Clock == nil || processor.Validator.Policy.Revision == "" ||
		processor.DraftBuilder.IDs == nil || processor.DraftBuilder.Clock == nil ||
		processor.Clarifications.IDs == nil || processor.Clarifications.Clock == nil ||
		processor.Effects.Store == nil || processor.Effects.Clock == nil ||
		claim.Conversation.ID == "" || claim.Run.ID == "" || claim.Run.Revision == 0 || claim.Run.FencingToken == 0 {
		return fmt.Errorf("AssistantRun processor dependencies or claim are incomplete")
	}
	return nil
}

func (processor AssistantRunProcessor) commitFailure(ctx context.Context, claim RunClaim, runRevision uint64, code string, retryable bool, result *ModelResult) error {
	now := processor.Clock.Now().UTC()
	event, err := processor.failureEvent(claim, code, now)
	if err != nil {
		return err
	}
	commit := RunFailureCommit{
		ConversationID: claim.Conversation.ID, ExpectedConversationRevision: claim.Conversation.Revision,
		RunID: claim.Run.ID, ExpectedRunRevision: runRevision, FencingToken: claim.Run.FencingToken,
		ErrorCode: code, Retryable: retryable, Event: event, OccurredAt: now,
	}
	if err := processor.Runs.CommitFailure(ctx, commit); err != nil {
		return err
	}
	return processor.publishFailureEffects(ctx, claim, code, result, now)
}

func (processor AssistantRunProcessor) decisionEvent(claim RunClaim, turnID string, decision contractsv1.AssistantDecision, now time.Time) (contractsv1.IntegrationEvent, error) {
	eventID, err := processor.IDs.NewID("integration-event")
	if err != nil {
		return contractsv1.IntegrationEvent{}, err
	}
	payload, err := json.Marshal(map[string]string{"assistant_run_id": claim.Run.ID, "turn_id": turnID, "decision_type": decision.DecisionType})
	if err != nil {
		return contractsv1.IntegrationEvent{}, err
	}
	return contractsv1.IntegrationEvent{SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: eventID,
		EventType: "AssistantDecisionCommitted.v1", ProducerContext: "interaction", StreamID: "conversation/" + claim.Conversation.ID,
		Sequence: 1, TenantID: claim.Conversation.TenantID, AggregateType: "Conversation", AggregateID: claim.Conversation.ID,
		TraceID: claim.Run.ID, CorrelationID: claim.Run.ID, CausationID: claim.Run.ID,
		IdempotencyKey: "assistant-decision/" + claim.Run.ID, Visibility: "user", Payload: payload, OccurredAt: now}, nil
}

func (processor AssistantRunProcessor) failureEvent(claim RunClaim, code string, now time.Time) (contractsv1.IntegrationEvent, error) {
	eventID, err := processor.IDs.NewID("integration-event")
	if err != nil {
		return contractsv1.IntegrationEvent{}, err
	}
	payload, err := json.Marshal(map[string]string{"assistant_run_id": claim.Run.ID, "error_code": code})
	if err != nil {
		return contractsv1.IntegrationEvent{}, err
	}
	return contractsv1.IntegrationEvent{SchemaVersion: contractsv1.IntegrationEventSchemaVersion, EventID: eventID,
		EventType: "AssistantRunFailed.v1", ProducerContext: "interaction", StreamID: "conversation/" + claim.Conversation.ID,
		Sequence: 1, TenantID: claim.Conversation.TenantID, AggregateType: "Conversation", AggregateID: claim.Conversation.ID,
		TraceID: claim.Run.ID, CorrelationID: claim.Run.ID, CausationID: claim.Run.ID,
		IdempotencyKey: "assistant-failure/" + claim.Run.ID, Visibility: "user", Payload: payload, OccurredAt: now}, nil
}

func (processor AssistantRunProcessor) publishEffects(ctx context.Context, claim RunClaim, decision contractsv1.AssistantDecision, checkpoint *domain.ClarificationCheckpoint, result ModelResult, now time.Time) error {
	var evidence *EvidenceBoundary
	boundary := ""
	if checkpoint != nil {
		boundary = "checkpoint"
	}
	if decision.DecisionType == "ready_draft" {
		boundary = "ready_draft"
	}
	if boundary != "" {
		record, _ := json.Marshal(map[string]any{"decision_type": decision.DecisionType, "assistant_run_id": claim.Run.ID})
		evidence = &EvidenceBoundary{TenantID: claim.Conversation.TenantID, UserID: claim.Conversation.ActorUserID,
			WorkspaceID: workspaceID(claim.Conversation.Workspace), ConversationID: claim.Conversation.ID,
			ConversationGeneration: claim.Conversation.Generation, AssistantRunID: claim.Run.ID, BoundaryType: boundary,
			Records: []json.RawMessage{record}, Metadata: map[string]string{"policy_revision": claim.Run.PolicyRevision}}
	}
	_, err := processor.Effects.Execute(ctx, PublishAssistantDerivedEffectsCommand{
		Evidence: evidence, Observation: modelObservation(claim, result, "ok", ""),
	})
	return err
}

func (processor AssistantRunProcessor) publishFailureEffects(ctx context.Context, claim RunClaim, code string, result *ModelResult, now time.Time) error {
	record, _ := json.Marshal(map[string]string{"assistant_run_id": claim.Run.ID, "status": "failed", "error_code": code})
	var observation *ModelObservation
	if result != nil {
		observation = modelObservation(claim, *result, "failed", code)
	}
	_, err := processor.Effects.Execute(ctx, PublishAssistantDerivedEffectsCommand{Evidence: &EvidenceBoundary{
		TenantID: claim.Conversation.TenantID, UserID: claim.Conversation.ActorUserID, WorkspaceID: workspaceID(claim.Conversation.Workspace),
		ConversationID: claim.Conversation.ID, ConversationGeneration: claim.Conversation.Generation, AssistantRunID: claim.Run.ID,
		BoundaryType: "run_failed", Records: []json.RawMessage{record}, Metadata: map[string]string{"error_code": code},
	}, Observation: observation})
	return err
}

func modelObservation(claim RunClaim, result ModelResult, status, code string) *ModelObservation {
	return &ModelObservation{TenantID: claim.Conversation.TenantID, UserID: claim.Conversation.ActorUserID, AssistantRunID: claim.Run.ID,
		ConversationID: claim.Conversation.ID, ProfileID: claim.Run.ModelProfile, ProfileRevision: claim.ModelProfileRevision,
		ProfileDigest: claim.ModelProfileDigest, ProviderRequestID: result.ProviderRequestID, Model: result.Model,
		InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt,
		Status: status, ErrorCode: code}
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrModelTimeout):
		return AssistantFailureModelTimeout
	case errors.Is(err, ErrModelMalformed):
		return AssistantFailureModelMalformed
	case errors.Is(err, ErrModelPolicyConflict):
		return AssistantFailureModelPolicy
	default:
		return AssistantFailureModelUpstream
	}
}

func taskDraft(record *TaskDraftRecord) *domain.TaskDraft {
	if record == nil {
		return nil
	}
	return &record.Draft
}
func currentDraftID(record *TaskDraftRecord) string {
	if record == nil {
		return ""
	}
	return record.Draft.ID
}
func currentDraftRevision(record *TaskDraftRecord) uint64 {
	if record == nil {
		return 0
	}
	return record.Draft.Revision
}
func initialWorkingDraft(claim RunClaim, snapshot json.RawMessage) json.RawMessage {
	if len(snapshot) != 0 {
		return snapshot
	}
	value, _ := json.Marshal(map[string]string{"conversation_id": claim.Conversation.ID, "schema_version": "assistant.working_draft.v1"})
	return value
}
func workspaceID(workspace *domain.WorkspaceResolution) string {
	if workspace == nil {
		return ""
	}
	return workspace.WorkspaceID
}

func checkpointContract(checkpoint domain.ClarificationCheckpoint) *contractsv1.AssistantClarificationCheckpoint {
	actions := make([]string, len(checkpoint.AllowedActions))
	for index, action := range checkpoint.AllowedActions {
		actions[index] = string(action)
	}
	return &contractsv1.AssistantClarificationCheckpoint{SchemaVersion: contractsv1.ClarificationCheckpointSchemaVersion,
		CheckpointID: checkpoint.ID, Revision: checkpoint.Revision, ConversationID: checkpoint.ConversationID,
		TaskDraftID: checkpoint.TaskDraftID, TaskDraftRevision: checkpoint.TaskDraftRevision, WorkingDraft: checkpoint.WorkingDraft,
		ReadinessAssessmentID: checkpoint.ReadinessAssessmentID,
		Assumptions:           append([]string{}, checkpoint.Assumptions...),
		Blockers:              append([]string{}, checkpoint.Blockers...),
		EvidenceRefs:          append([]string{}, checkpoint.EvidenceRefs...),
		AllowedActions:        actions, CreatedAt: checkpoint.CreatedAt}
}

var _ RunProcessor = AssistantRunProcessor{}
