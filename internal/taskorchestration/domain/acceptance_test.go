package domain_test

import (
	"errors"
	"testing"

	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

func TestStageResultAcceptanceMatrix(t *testing.T) {
	testCases := []struct {
		name      string
		result    domain.StageResult
		want      domain.TaskStatus
		retain    bool
		wantError bool
	}{
		{
			name: "auto deterministic report completes",
			result: domain.StageResult{CurrentStatus: domain.TaskRunning, AcceptanceMode: domain.AcceptanceAuto,
				Artifacts: []domain.ArtifactKind{domain.ArtifactReport}, DeterministicEvidence: true},
			want: domain.TaskCompleted,
		},
		{
			name: "auto uncertain report is delivered",
			result: domain.StageResult{CurrentStatus: domain.TaskRunning, AcceptanceMode: domain.AcceptanceAuto,
				Artifacts: []domain.ArtifactKind{domain.ArtifactReport}},
			want: domain.TaskDelivered, retain: true,
		},
		{
			name: "manual report is delivered",
			result: domain.StageResult{CurrentStatus: domain.TaskWaitingUser, AcceptanceMode: domain.AcceptanceManual,
				Artifacts: []domain.ArtifactKind{domain.ArtifactReport}, DeterministicEvidence: true},
			want: domain.TaskDelivered, retain: true,
		},
		{
			name: "manual patch is delivered",
			result: domain.StageResult{CurrentStatus: domain.TaskRunning, AcceptanceMode: domain.AcceptanceManual,
				Artifacts: []domain.ArtifactKind{domain.ArtifactReport, domain.ArtifactPatch}, DeterministicEvidence: true},
			want: domain.TaskDelivered, retain: true,
		},
		{
			name: "auto patch is forbidden",
			result: domain.StageResult{CurrentStatus: domain.TaskRunning, AcceptanceMode: domain.AcceptanceAuto,
				Artifacts: []domain.ArtifactKind{domain.ArtifactPatch}, DeterministicEvidence: true},
			wantError: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			decision, err := domain.DecideStageResult(testCase.result)
			if testCase.wantError {
				if !errors.Is(err, domain.ErrInvalidAcceptancePolicy) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil || decision.TargetStatus != testCase.want || decision.RetainSessionBinding != testCase.retain {
				t.Fatalf("decision=%+v error=%v", decision, err)
			}
			if testCase.retain && len(decision.AllowedUserActions) != 2 {
				t.Fatalf("manual actions=%v", decision.AllowedUserActions)
			}
		})
	}
}

func TestAcceptAndReworkRequireInitiatorExactRevision(t *testing.T) {
	evidence := domain.TaskActionEvidence{
		ActorUserID: "user-local", InitiatorUserID: "user-local",
		ExpectedRevision: 7, CurrentRevision: 7, CurrentStatus: domain.TaskDelivered, SessionLive: true,
	}
	status, err := domain.AcceptDelivered(evidence)
	if err != nil || status != domain.TaskCompleted {
		t.Fatalf("accept status=%s error=%v", status, err)
	}
	rework, err := domain.RequestRework(evidence)
	if err != nil || rework.TargetTaskStatus != domain.TaskRunning || !rework.RequiresNewWorkerRun || !rework.MutateTask {
		t.Fatalf("rework=%+v error=%v", rework, err)
	}
	for name, mutate := range map[string]func(*domain.TaskActionEvidence){
		"wrong actor":    func(value *domain.TaskActionEvidence) { value.ActorUserID = "other" },
		"stale revision": func(value *domain.TaskActionEvidence) { value.ExpectedRevision-- },
		"dead Session":   func(value *domain.TaskActionEvidence) { value.SessionLive = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := evidence
			mutate(&changed)
			if _, err := domain.RequestRework(changed); !errors.Is(err, domain.ErrTaskActionForbidden) {
				t.Fatalf("rework error=%v", err)
			}
		})
	}
}

func TestPostTerminalFollowUpCreatesStandaloneRunWithoutTaskMutation(t *testing.T) {
	for _, status := range []domain.TaskStatus{domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled} {
		plan, err := domain.PlanSessionFollowUp("task-1", status, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Kind != domain.FollowUpStandalone || plan.TriggerType != "direct_agent_turn" ||
			plan.OriginTaskID != "task-1" || plan.MutateTask || !plan.RequiresNewWorkerRun {
			t.Fatalf("terminal %s plan=%+v", status, plan)
		}
	}
	for _, status := range []domain.TaskStatus{domain.TaskRunning, domain.TaskWaitingUser, domain.TaskDelivered} {
		plan, err := domain.PlanSessionFollowUp("task-1", status, true, true)
		if err != nil || plan.Kind != domain.FollowUpTaskRun || !plan.MutateTask || plan.OriginTaskID != "" {
			t.Fatalf("nonterminal %s plan=%+v error=%v", status, plan, err)
		}
	}
}
