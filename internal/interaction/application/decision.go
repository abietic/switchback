package application

import (
	"fmt"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/interaction/domain"
)

type ReadinessPolicy struct {
	Revision            string
	AssumableDimensions map[domain.ReadinessDimension]bool
}

type ValidatedDecision struct {
	Decision  contractsv1.AssistantDecision
	Readiness domain.TaskReadinessAssessment
	Digest    string
}

type DecisionValidator struct {
	IDs    IDGenerator
	Clock  Clock
	Policy ReadinessPolicy
}

func (validator DecisionValidator) Validate(
	claim RunClaim,
	contextPack ContextPack,
	proposal contractsv1.AssistantDecision,
) (ValidatedDecision, error) {
	if validator.IDs == nil || validator.Clock == nil || validator.Policy.Revision == "" ||
		claim.Run.ID == "" || claim.Conversation.ID == "" {
		return ValidatedDecision{}, fmt.Errorf("DecisionValidator dependencies and claim are required")
	}
	if err := proposal.Validate(); err != nil {
		return ValidatedDecision{}, ErrModelMalformed
	}
	if proposal.Readiness.PolicyRevision != validator.Policy.Revision ||
		proposal.Readiness.PolicyRevision != claim.Run.PolicyRevision {
		return ValidatedDecision{}, ErrModelPolicyConflict
	}
	allowedEvidence := make(map[string]struct{}, len(contextPack.Selected))
	for _, item := range contextPack.Selected {
		allowedEvidence[item.Ref] = struct{}{}
	}
	dimensions := make(map[domain.ReadinessDimension]domain.DimensionState, 7)
	for _, proposed := range proposal.Readiness.Dimensions {
		dimension := domain.ReadinessDimension(proposed.Dimension)
		status := domain.DimensionStatus(proposed.Status)
		if status == domain.DimensionAssumed && !validator.Policy.AssumableDimensions[dimension] {
			return ValidatedDecision{}, ErrModelPolicyConflict
		}
		for _, ref := range proposed.EvidenceRefs {
			if _, ok := allowedEvidence[ref]; !ok {
				return ValidatedDecision{}, ErrModelPolicyConflict
			}
		}
		dimensions[dimension] = domain.DimensionState{
			Status: status, Summary: proposed.Summary,
			EvidenceRefs: append([]string(nil), proposed.EvidenceRefs...),
		}
	}
	if claim.Conversation.Workspace == nil {
		contextState := dimensions[domain.DimensionContextAccess]
		if contextState.Status != domain.DimensionBlockingUnknown {
			return ValidatedDecision{}, ErrModelPolicyConflict
		}
		if proposal.DecisionType == "ready_draft" {
			return ValidatedDecision{}, ErrModelPolicyConflict
		}
	}
	assessmentID, err := validator.IDs.NewID("assistant-readiness-assessment")
	if err != nil {
		return ValidatedDecision{}, fmt.Errorf("generate readiness assessment id: %w", err)
	}
	assessment := domain.TaskReadinessAssessment{
		ID: assessmentID, ConversationID: claim.Conversation.ID, AssistantRunID: claim.Run.ID,
		Revision: claim.Conversation.Revision, PolicyRevision: validator.Policy.Revision,
		Dimensions:             dimensions,
		BlockingUnknowns:       append([]string{}, proposal.Readiness.BlockingUnknowns...),
		Assumptions:            append([]string{}, proposal.Readiness.Assumptions...),
		ClarificationQuestions: proposal.Readiness.ClarificationQuestions,
		Status:                 domain.ReadinessStatus(proposal.Readiness.Status), EvaluatedAt: validator.Clock.Now().UTC(),
	}
	if err := assessment.Validate(); err != nil {
		return ValidatedDecision{}, ErrModelPolicyConflict
	}
	if proposal.DecisionType == "ready_draft" && !assessment.Ready() {
		return ValidatedDecision{}, ErrModelPolicyConflict
	}
	if proposal.DecisionType == "clarification" && assessment.Ready() {
		return ValidatedDecision{}, ErrModelPolicyConflict
	}
	proposal.Readiness.AssessmentID = assessment.ID
	proposal.Readiness.Revision = assessment.Revision
	proposal.Readiness.EvaluatedAt = assessment.EvaluatedAt
	proposal.Readiness.PolicyRevision = assessment.PolicyRevision
	proposal.Readiness.BlockingUnknowns = append([]string{}, assessment.BlockingUnknowns...)
	proposal.Readiness.Assumptions = append([]string{}, assessment.Assumptions...)
	proposal.Readiness.ClarificationQuestions = assessment.ClarificationQuestions
	proposal.Readiness.Status = string(assessment.Status)
	digest, err := digestJSON(proposal.Readiness)
	if err != nil {
		return ValidatedDecision{}, err
	}
	return ValidatedDecision{Decision: proposal, Readiness: assessment, Digest: digest}, nil
}
