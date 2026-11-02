package domain

import "time"

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

var requiredReadinessDimensions = []ReadinessDimension{
	DimensionGoal,
	DimensionDeliverableAcceptance,
	DimensionScopeTarget,
	DimensionConstraints,
	DimensionContextAccess,
	DimensionRiskApproval,
	DimensionExecutionPolicy,
}

func RequiredReadinessDimensions() []ReadinessDimension {
	return append([]ReadinessDimension(nil), requiredReadinessDimensions...)
}

type DimensionStatus string

const (
	DimensionKnown           DimensionStatus = "known"
	DimensionAssumed         DimensionStatus = "assumed"
	DimensionBlockingUnknown DimensionStatus = "blocking_unknown"
	DimensionNotApplicable   DimensionStatus = "not_applicable"
)

type ReadinessStatus string

const (
	ReadinessClarifying           ReadinessStatus = "clarifying"
	ReadinessReadyConfirmed       ReadinessStatus = "ready_confirmed"
	ReadinessReadyWithAssumptions ReadinessStatus = "ready_with_assumptions"
)

type TaskReadinessAssessment struct {
	AssessmentID           string
	Source                 string
	PolicyRevision         string
	Dimensions             map[ReadinessDimension]DimensionStatus
	BlockingUnknowns       []string
	Assumptions            []string
	ClarificationQuestions int
	Status                 ReadinessStatus
	EvaluatedAt            time.Time
}

type ExplicitTaskCreationIntent struct {
	IntentID              string
	ActorUserID           string
	TenantID              string
	TaskSpecDigest        string
	WorkspaceID           string
	WorkspaceRevisionID   string
	ReadinessAssessmentID string
	TriggerPolicyRevision string
	CreatedAt             time.Time
	ExpiresAt             time.Time
}
