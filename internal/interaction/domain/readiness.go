package domain

import (
	"fmt"
	"time"
)

type ReadinessDimension string

const (
	DimensionGoal                  ReadinessDimension = "goal"
	DimensionDeliverableAcceptance ReadinessDimension = "deliverable_acceptance"
	DimensionScopeTarget           ReadinessDimension = "scope_target"
	DimensionConstraints           ReadinessDimension = "constraints"
	DimensionContextAccess         ReadinessDimension = "context_access"
	DimensionRiskApproval          ReadinessDimension = "risk_approval"
	DimensionExecutionPolicy       ReadinessDimension = "execution_policy"
)

var requiredDimensions = []ReadinessDimension{
	DimensionGoal,
	DimensionDeliverableAcceptance,
	DimensionScopeTarget,
	DimensionConstraints,
	DimensionContextAccess,
	DimensionRiskApproval,
	DimensionExecutionPolicy,
}

type DimensionStatus string

const (
	DimensionKnown           DimensionStatus = "known"
	DimensionAssumed         DimensionStatus = "assumed"
	DimensionBlockingUnknown DimensionStatus = "blocking_unknown"
	DimensionNotApplicable   DimensionStatus = "not_applicable"
)

type DimensionState struct {
	Status       DimensionStatus
	Summary      string
	EvidenceRefs []string
}

type ReadinessStatus string

const (
	ReadinessClarifying           ReadinessStatus = "clarifying"
	ReadinessReadyConfirmed       ReadinessStatus = "ready_confirmed"
	ReadinessReadyWithAssumptions ReadinessStatus = "ready_with_assumptions"
)

type TaskReadinessAssessment struct {
	ID                     string
	ConversationID         string
	AssistantRunID         string
	Revision               uint64
	PolicyRevision         string
	Dimensions             map[ReadinessDimension]DimensionState
	BlockingUnknowns       []string
	Assumptions            []string
	ClarificationQuestions uint64
	Status                 ReadinessStatus
	EvaluatedAt            time.Time
}

func (assessment TaskReadinessAssessment) Validate() error {
	if !canonical(assessment.ID) || !canonical(assessment.ConversationID) ||
		!canonical(assessment.AssistantRunID) || assessment.Revision == 0 ||
		!canonical(assessment.PolicyRevision) || assessment.EvaluatedAt.IsZero() {
		return fmt.Errorf("readiness assessment identity is invalid")
	}
	if len(assessment.Dimensions) != len(requiredDimensions) {
		return fmt.Errorf("readiness must contain seven dimensions")
	}
	blocking := false
	assumed := false
	for _, dimension := range requiredDimensions {
		state, ok := assessment.Dimensions[dimension]
		if !ok || !canonical(state.Summary) {
			return fmt.Errorf("readiness dimension %s is missing", dimension)
		}
		switch state.Status {
		case DimensionKnown, DimensionNotApplicable:
		case DimensionAssumed:
			assumed = true
		case DimensionBlockingUnknown:
			blocking = true
		default:
			return fmt.Errorf("readiness dimension %s has invalid status", dimension)
		}
		for _, ref := range state.EvidenceRefs {
			if !canonical(ref) {
				return fmt.Errorf("readiness evidence ref is invalid")
			}
		}
	}
	if blocking && (assessment.Status != ReadinessClarifying || len(assessment.BlockingUnknowns) == 0) {
		return fmt.Errorf("blocking readiness must remain clarifying")
	}
	if !blocking && assessment.Status == ReadinessClarifying {
		return fmt.Errorf("ready assessment cannot remain clarifying")
	}
	if !blocking && assumed && assessment.Status != ReadinessReadyWithAssumptions {
		return fmt.Errorf("assumed readiness must expose assumptions")
	}
	if !blocking && !assumed && assessment.Status != ReadinessReadyConfirmed {
		return fmt.Errorf("fully known readiness must be confirmed")
	}
	return nil
}

func (assessment TaskReadinessAssessment) Ready() bool {
	return assessment.Validate() == nil && assessment.Status != ReadinessClarifying
}

func RequiredReadinessDimensions() []ReadinessDimension {
	return append([]ReadinessDimension(nil), requiredDimensions...)
}
