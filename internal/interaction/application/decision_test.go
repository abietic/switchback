package application

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
)

func TestDecisionValidatorReplacesModelAuthorityAndAcceptsAllowedDefault(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	ids := contextIDs(0)
	validator := DecisionValidator{
		IDs: &ids, Clock: fixedClock(now), Policy: ReadinessPolicy{
			Revision:            "assistant-readiness-v1",
			AssumableDimensions: map[domain.ReadinessDimension]bool{domain.DimensionConstraints: true},
		},
	}
	claim := readyClaim(now)
	contextPack := decisionContextPack()
	proposal := readyDecision(now)
	proposal.Readiness.AssessmentID = "model-controlled"
	proposal.Readiness.Revision = 999
	proposal.Readiness.EvaluatedAt = now.Add(24 * time.Hour)
	for index := range proposal.Readiness.Dimensions {
		if proposal.Readiness.Dimensions[index].Dimension == "constraints" {
			proposal.Readiness.Dimensions[index].Status = "assumed"
			proposal.Readiness.Dimensions[index].Summary = "Use repository conventions"
		}
	}
	proposal.Readiness.Status = "ready_with_assumptions"
	proposal.Readiness.Assumptions = []string{"Use repository conventions"}
	validated, err := validator.Validate(claim, contextPack, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Readiness.ID != "context-pack-1" || validated.Readiness.Revision != claim.Conversation.Revision ||
		!validated.Readiness.EvaluatedAt.Equal(now) || validated.Decision.Readiness.AssessmentID == "model-controlled" {
		t.Fatalf("model authority survived validation: %+v", validated)
	}
}

func TestDecisionValidatorKeepsHardDimensionsBlocking(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	tests := []string{"goal", "deliverable_acceptance", "scope_target", "context_access", "risk_approval"}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			ids := contextIDs(0)
			validator := DecisionValidator{
				IDs: &ids, Clock: fixedClock(now), Policy: ReadinessPolicy{
					Revision:            "assistant-readiness-v1",
					AssumableDimensions: map[domain.ReadinessDimension]bool{domain.DimensionConstraints: true},
				},
			}
			proposal := readyDecision(now)
			for index := range proposal.Readiness.Dimensions {
				if proposal.Readiness.Dimensions[index].Dimension == target {
					proposal.Readiness.Dimensions[index].Status = "assumed"
				}
			}
			proposal.Readiness.Status = "ready_with_assumptions"
			proposal.Readiness.Assumptions = []string{"unsafe default"}
			if _, err := validator.Validate(readyClaim(now), decisionContextPack(), proposal); !errors.Is(err, ErrModelPolicyConflict) {
				t.Fatalf("hard dimension became assumption: %v", err)
			}
		})
	}
}

func TestDecisionValidatorRequiresExplicitWorkspaceAndAuthorizedEvidence(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	ids := contextIDs(0)
	validator := DecisionValidator{
		IDs: &ids, Clock: fixedClock(now),
		Policy: ReadinessPolicy{Revision: "assistant-readiness-v1", AssumableDimensions: map[domain.ReadinessDimension]bool{}},
	}
	claim := readyClaim(now)
	claim.Conversation.Workspace = nil
	if _, err := validator.Validate(claim, decisionContextPack(), readyDecision(now)); !errors.Is(err, ErrModelPolicyConflict) {
		t.Fatalf("ready decision without Workspace = %v", err)
	}
	claim = readyClaim(now)
	proposal := readyDecision(now)
	proposal.Readiness.Dimensions[0].EvidenceRefs = []string{"not-selected"}
	if _, err := validator.Validate(claim, decisionContextPack(), proposal); !errors.Is(err, ErrModelPolicyConflict) {
		t.Fatalf("unauthorized evidence ref = %v", err)
	}
}

func TestDecisionValidatorPreservesExplicitEmptyArraysInAuthoritativeDecision(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	ids := contextIDs(0)
	validator := DecisionValidator{
		IDs: &ids, Clock: fixedClock(now),
		Policy: ReadinessPolicy{
			Revision:            "assistant-readiness-v1",
			AssumableDimensions: map[domain.ReadinessDimension]bool{},
		},
	}
	proposal := readyDecision(now)
	proposal.Readiness.Assumptions = []string{}
	proposal.Readiness.BlockingUnknowns = []string{}
	validated, err := validator.Validate(readyClaim(now), decisionContextPack(), proposal)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Decision.Readiness.Assumptions == nil ||
		validated.Decision.Readiness.BlockingUnknowns == nil {
		t.Fatal("authoritative readiness changed explicit empty arrays into null")
	}
	if err := validated.Decision.Validate(); err != nil {
		t.Fatalf("authoritative decision no longer satisfies its wire contract: %v", err)
	}
}

func readyClaim(now time.Time) RunClaim {
	return RunClaim{
		Conversation: domain.Conversation{
			ID: "conversation-1", Revision: 4,
			Workspace: &domain.WorkspaceResolution{
				WorkspaceID: "workspace-local", WorkspaceRevisionID: "workspace-revision-local-v1",
				ResolutionID: "resolution-local", ResolutionRevision: 1, MembershipRevision: 1,
			},
		},
		Run: domain.AssistantRun{
			ID: "run-1", PolicyRevision: "assistant-readiness-v1", CreatedAt: now,
		},
	}
}

func decisionContextPack() ContextPack {
	return ContextPack{
		SchemaVersion:  ContextPackSchemaVersion,
		PolicyRevision: "assistant-readiness-v1",
		Selected: []ContextItem{
			{Ref: "turn-1", Kind: "conversation_turn", Content: json.RawMessage(`{"content":"work"}`)},
			{Ref: "workspace-1", Kind: "workspace", Content: json.RawMessage(`{"id":"workspace-local"}`)},
			{Ref: "policy-v1", Kind: "policy", Content: json.RawMessage(`{"revision":"v1"}`)},
		},
		Dropped: []DroppedContextItem{},
	}
}

func readyDecision(now time.Time) contractsv1.AssistantDecision {
	dimensions := make([]contractsv1.ReadinessDimensionState, 0, 7)
	for _, dimension := range []string{
		"goal", "deliverable_acceptance", "scope_target", "constraints",
		"context_access", "risk_approval", "execution_policy",
	} {
		ref := "turn-1"
		if dimension == "context_access" {
			ref = "workspace-1"
		}
		if dimension == "execution_policy" {
			ref = "policy-v1"
		}
		dimensions = append(dimensions, contractsv1.ReadinessDimensionState{
			Dimension: dimension, Status: "known", Summary: "known", EvidenceRefs: []string{ref},
		})
	}
	return contractsv1.AssistantDecision{
		SchemaVersion: contractsv1.AssistantDecisionSchemaVersion, DecisionType: "ready_draft",
		UserVisibleContent: "Ready.",
		Readiness: contractsv1.AssistantReadinessAssessment{
			SchemaVersion: contractsv1.ReadinessAssessmentSchemaVersion, AssessmentID: "model-assessment",
			Revision: 1, PolicyRevision: "assistant-readiness-v1", Dimensions: dimensions,
			BlockingUnknowns: []string{}, Assumptions: []string{}, Status: "ready_confirmed", EvaluatedAt: now,
		},
		TaskDraft: &contractsv1.AssistantTaskDraft{
			SchemaVersion: contractsv1.TaskDraftSchemaVersion, TaskDraftID: "model-draft", Revision: 1,
			InitiatorUserID: "user-local", ConversationID: "conversation-1",
			WorkspaceID: "workspace-local", WorkspaceRevisionID: "workspace-revision-local-v1",
			ReadinessAssessmentID: "model-assessment",
			TaskSpecDigest:        "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Title:                 "Work", Goal: "Do work", Target: json.RawMessage(`{"kind":"repository"}`),
			Constraints: []string{}, AcceptanceCriteria: []string{"tests pass"}, Assumptions: []string{},
			TaskSpec: json.RawMessage(`{"schema_version":"task.spec.v1"}`), CreatedAt: now,
		},
	}
}
