package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	interactionapp "github.com/abietic/switchback/internal/interaction/application"
)

func TestDeterministicFakes(t *testing.T) {
	clock := NewClock(time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC))
	clock.Advance(time.Second)
	if clock.Now().Second() != 1 {
		t.Fatalf("clock did not advance: %s", clock.Now())
	}
	ids := &IDs{}
	first, _ := ids.NewID("turn")
	second, _ := ids.NewID("turn")
	if first != "turn-1" || second != "turn-2" {
		t.Fatalf("IDs are not deterministic: %s %s", first, second)
	}
	model := &Model{Errors: []error{errors.New("upstream timeout")}}
	if _, err := model.Complete(context.Background(), interactionapp.ModelRequest{RunID: "run-1"}); err == nil {
		t.Fatal("scripted model error was not returned")
	}
}

func TestScriptedModelCoversDecisionAndFailureOutcomes(t *testing.T) {
	now := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	assessment := contractsv1.AssistantReadinessAssessment{
		SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion,
		AssessmentID:  "assessment-1", Revision: 1, PolicyRevision: "readiness-v1",
		Dimensions: []contractsv1.ReadinessDimensionState{
			{Dimension: "goal", Status: "known", Summary: "known", EvidenceRefs: []string{}},
			{Dimension: "deliverable_acceptance", Status: "blocking_unknown", Summary: "missing", EvidenceRefs: []string{}},
			{Dimension: "scope_target", Status: "known", Summary: "known", EvidenceRefs: []string{}},
			{Dimension: "constraints", Status: "known", Summary: "known", EvidenceRefs: []string{}},
			{Dimension: "context_access", Status: "known", Summary: "known", EvidenceRefs: []string{}},
			{Dimension: "risk_approval", Status: "known", Summary: "known", EvidenceRefs: []string{}},
			{Dimension: "execution_policy", Status: "known", Summary: "known", EvidenceRefs: []string{}},
		},
		BlockingUnknowns: []string{"deliverable_acceptance"}, Assumptions: []string{},
		ClarificationQuestions: 1, Status: "clarifying", EvaluatedAt: now,
	}
	clarification := ClarificationDecision(assessment, "What should be verified?")
	informational := interactionapp.ModelResult{Decision: contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "informational",
		UserVisibleContent: "No task is required.", Readiness: assessment,
	}}
	readyAssessment := assessment
	readyAssessment.BlockingUnknowns = []string{}
	readyAssessment.Status = "ready_confirmed"
	for index := range readyAssessment.Dimensions {
		if readyAssessment.Dimensions[index].Status == "blocking_unknown" {
			readyAssessment.Dimensions[index].Status = "known"
		}
	}
	ready := interactionapp.ModelResult{Decision: contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "ready_draft",
		UserVisibleContent: "The draft is ready.", Readiness: readyAssessment,
		TaskDraft: &contractsv1.AssistantTaskDraft{
			SchemaVersion: contractsv1.TaskDraftSchemaVersion, TaskDraftID: "draft-1", Revision: 1,
			InitiatorUserID: "user-local", ConversationID: "conversation-1",
			WorkspaceID: "workspace-local", WorkspaceRevisionID: "workspace-revision-local-v1",
			ReadinessAssessmentID: "assessment-1",
			TaskSpecDigest:        "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Title:                 "Work", Goal: "Do work", Target: json.RawMessage(`{"kind":"repository"}`),
			Constraints: []string{}, AcceptanceCriteria: []string{"tests pass"}, Assumptions: []string{},
			TaskSpec: json.RawMessage(`{"schema_version":"task.spec.v1"}`), CreatedAt: now,
		},
	}}
	model := &Model{
		Results: []interactionapp.ModelResult{
			clarification, ready, informational, {}, {}, {},
		},
		Errors: []error{
			nil, nil, nil,
			interactionapp.ErrModelMalformed,
			interactionapp.ErrModelTimeout,
			interactionapp.ErrModelPolicyConflict,
		},
	}
	for index := range 6 {
		result, err := model.Complete(context.Background(), interactionapp.ModelRequest{RunID: "run"})
		if index < 3 && (err != nil || result.Decision.DecisionType == "") {
			t.Fatalf("scripted decision %d failed: result=%+v error=%v", index, result, err)
		}
		if index >= 3 && err == nil {
			t.Fatalf("scripted failure %d was not returned", index)
		}
	}
	if len(model.Requests) != 6 {
		t.Fatalf("scripted model requests = %d", len(model.Requests))
	}
}
