package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidAcceptancePolicy = errors.New("invalid Task acceptance policy")
	ErrTaskActionForbidden     = errors.New("Task action is not authorized for this actor or revision")
	ErrFollowUpUnavailable     = errors.New("WorkerSession follow-up is unavailable")
)

type AcceptanceMode string

const (
	AcceptanceAuto   AcceptanceMode = "auto"
	AcceptanceManual AcceptanceMode = "manual"
)

type ArtifactKind string

const (
	ArtifactReport ArtifactKind = "report"
	ArtifactPatch  ArtifactKind = "patch"
)

type StageResult struct {
	CurrentStatus         TaskStatus
	AcceptanceMode        AcceptanceMode
	Artifacts             []ArtifactKind
	DeterministicEvidence bool
}

type StageDecision struct {
	TargetStatus         TaskStatus
	RetainSessionBinding bool
	AllowedUserActions   []string
}

func DecideStageResult(result StageResult) (StageDecision, error) {
	if result.CurrentStatus != TaskRunning && result.CurrentStatus != TaskWaitingUser {
		return StageDecision{}, fmt.Errorf("%w: stage result from %s", ErrInvalidAcceptancePolicy, result.CurrentStatus)
	}
	if result.AcceptanceMode != AcceptanceAuto && result.AcceptanceMode != AcceptanceManual {
		return StageDecision{}, fmt.Errorf("%w: unsupported mode %s", ErrInvalidAcceptancePolicy, result.AcceptanceMode)
	}
	if len(result.Artifacts) == 0 {
		return StageDecision{}, fmt.Errorf("%w: Artifact set is empty", ErrInvalidAcceptancePolicy)
	}
	hasPatch := false
	for _, artifact := range result.Artifacts {
		switch artifact {
		case ArtifactReport:
		case ArtifactPatch:
			hasPatch = true
		default:
			return StageDecision{}, fmt.Errorf("%w: unsupported Artifact %s", ErrInvalidAcceptancePolicy, artifact)
		}
	}
	if hasPatch && result.AcceptanceMode != AcceptanceManual {
		return StageDecision{}, fmt.Errorf("%w: patch requires manual acceptance", ErrInvalidAcceptancePolicy)
	}
	if !hasPatch && result.AcceptanceMode == AcceptanceAuto && result.DeterministicEvidence {
		return StageDecision{TargetStatus: TaskCompleted, RetainSessionBinding: false}, nil
	}
	return StageDecision{
		TargetStatus: TaskDelivered, RetainSessionBinding: true,
		AllowedUserActions: []string{"accept", "rework"},
	}, nil
}

type TaskActionEvidence struct {
	ActorUserID      string
	InitiatorUserID  string
	ExpectedRevision uint64
	CurrentRevision  uint64
	CurrentStatus    TaskStatus
	SessionLive      bool
}

func AcceptDelivered(evidence TaskActionEvidence) (TaskStatus, error) {
	if err := validateTaskAction(evidence); err != nil {
		return evidence.CurrentStatus, err
	}
	if evidence.CurrentStatus != TaskDelivered {
		return evidence.CurrentStatus, fmt.Errorf("%w: accept requires delivered Task", ErrTaskActionForbidden)
	}
	return evidence.CurrentStatus.Transition(TaskCompleted)
}

func RequestRework(evidence TaskActionEvidence) (FollowUpPlan, error) {
	if err := validateTaskAction(evidence); err != nil {
		return FollowUpPlan{}, err
	}
	if evidence.CurrentStatus != TaskDelivered || !evidence.SessionLive {
		return FollowUpPlan{}, fmt.Errorf("%w: rework requires delivered Task and live Session", ErrTaskActionForbidden)
	}
	target, err := evidence.CurrentStatus.Transition(TaskRunning)
	if err != nil {
		return FollowUpPlan{}, err
	}
	return FollowUpPlan{
		Kind: FollowUpTaskRun, TriggerType: "task_rework", TargetTaskStatus: target,
		MutateTask: true, RequiresNewWorkerRun: true,
	}, nil
}

func validateTaskAction(evidence TaskActionEvidence) error {
	if evidence.ActorUserID == "" || evidence.ActorUserID != evidence.InitiatorUserID ||
		evidence.ExpectedRevision == 0 || evidence.ExpectedRevision != evidence.CurrentRevision {
		return ErrTaskActionForbidden
	}
	return nil
}

type FollowUpKind string

const (
	FollowUpTaskRun    FollowUpKind = "task_run"
	FollowUpStandalone FollowUpKind = "standalone_run"
)

type FollowUpPlan struct {
	Kind                 FollowUpKind
	TriggerType          string
	OriginTaskID         string
	TargetTaskStatus     TaskStatus
	MutateTask           bool
	RequiresNewWorkerRun bool
}

func PlanSessionFollowUp(
	taskID string,
	status TaskStatus,
	actorIsInitiator bool,
	sessionLive bool,
) (FollowUpPlan, error) {
	if taskID == "" || !actorIsInitiator || !sessionLive {
		return FollowUpPlan{}, ErrFollowUpUnavailable
	}
	if status.IsTerminal() {
		return FollowUpPlan{
			Kind: FollowUpStandalone, TriggerType: "direct_agent_turn", OriginTaskID: taskID,
			MutateTask: false, RequiresNewWorkerRun: true,
		}, nil
	}
	switch status {
	case TaskRunning, TaskWaitingUser:
		return FollowUpPlan{
			Kind: FollowUpTaskRun, TriggerType: "task_rework", TargetTaskStatus: TaskRunning,
			MutateTask: true, RequiresNewWorkerRun: true,
		}, nil
	case TaskDelivered:
		return FollowUpPlan{
			Kind: FollowUpTaskRun, TriggerType: "task_rework", TargetTaskStatus: TaskRunning,
			MutateTask: true, RequiresNewWorkerRun: true,
		}, nil
	default:
		return FollowUpPlan{}, ErrFollowUpUnavailable
	}
}
