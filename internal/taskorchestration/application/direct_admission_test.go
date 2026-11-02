package application_test

import (
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/taskorchestration/application"
	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

func TestDirectAdmissionCreatesReadyAssessmentAndBoundIntent(t *testing.T) {
	now := time.Date(2026, 7, 23, 1, 2, 3, 0, time.UTC)
	service := application.DirectAdmission{
		IDs: &fixedIDs{values: []string{"assessment-1", "intent-1"}},
		Now: func() time.Time { return now },
		Policy: application.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1",
			TriggerPolicyRevision:   "direct-create-v1",
			NetworkProfile:          currentNetworkProfile(),
		},
	}

	result, err := service.Admit(directAdmissionCommand())
	if err != nil {
		t.Fatalf("admit direct Task: %v", err)
	}
	if result.Assessment.Status != domain.ReadinessReadyConfirmed || len(result.Assessment.BlockingUnknowns) != 0 {
		t.Fatalf("assessment is not ready: %+v", result.Assessment)
	}
	if result.Assessment.ClarificationQuestions != 0 {
		t.Fatalf("direct admission asked questions: %+v", result.Assessment)
	}
	for _, dimension := range domain.RequiredReadinessDimensions() {
		if result.Assessment.Dimensions[dimension] != domain.DimensionKnown {
			t.Fatalf("dimension %s = %s", dimension, result.Assessment.Dimensions[dimension])
		}
	}
	if result.Intent.ActorUserID != "user-local" || result.Intent.WorkspaceID != "workspace-demo" || result.Intent.WorkspaceRevisionID != "workspace-revision-1" {
		t.Fatalf("intent binding is incomplete: %+v", result.Intent)
	}
	if result.Intent.TriggerPolicyRevision != "direct-create-v1" || result.Intent.TaskSpecDigest == "" {
		t.Fatalf("intent policy/digest is incomplete: %+v", result.Intent)
	}
	if result.TaskSpec.CreationIntentRef.TaskSpecDigest != result.Intent.TaskSpecDigest {
		t.Fatalf("TaskSpec digest %q != intent digest %q", result.TaskSpec.CreationIntentRef.TaskSpecDigest, result.Intent.TaskSpecDigest)
	}
	digest, err := application.CanonicalTaskSpecDigest(result.TaskSpec)
	if err != nil {
		t.Fatal(err)
	}
	if digest != result.Intent.TaskSpecDigest {
		t.Fatalf("recomputed digest %q != %q", digest, result.Intent.TaskSpecDigest)
	}
	if !result.Intent.ExpiresAt.After(now) {
		t.Fatalf("intent expiry = %s", result.Intent.ExpiresAt)
	}
}

func TestDirectAdmissionDigestBindsActorWorkspaceAndContent(t *testing.T) {
	base := directAdmissionCommand()
	digest := admitDigest(t, base)

	changedActor := base
	changedActor.Actor.UserID = "another-user"
	changedWorkspace := base
	changedWorkspace.Workspace.WorkspaceRevisionID = "workspace-revision-2"
	changedGoal := base
	changedGoal.Request.Goal = "A different exact goal."

	for name, command := range map[string]application.DirectAdmissionCommand{
		"actor":     changedActor,
		"workspace": changedWorkspace,
		"content":   changedGoal,
	} {
		if other := admitDigest(t, command); other == digest {
			t.Errorf("%s change did not change TaskSpec digest", name)
		}
	}
}

func TestDirectAdmissionDoesNotCreateIntentForBlockingInput(t *testing.T) {
	command := directAdmissionCommand()
	command.Request.AcceptanceCriteria = nil
	service := newAdmissionService()

	result, err := service.Admit(command)
	if !errors.Is(err, application.ErrTaskNotReady) {
		t.Fatalf("error = %v, want ErrTaskNotReady", err)
	}
	if result.Intent.IntentID != "" || len(result.Assessment.BlockingUnknowns) == 0 {
		t.Fatalf("blocking admission created intent or omitted blockers: %+v", result)
	}
}

func TestDirectAdmissionRejectsUnreadableWorkspace(t *testing.T) {
	command := directAdmissionCommand()
	command.Workspace.Readable = false
	result, err := newAdmissionService().Admit(command)
	if !errors.Is(err, application.ErrTaskNotReady) {
		t.Fatalf("error = %v, want ErrTaskNotReady", err)
	}
	if result.Intent.IntentID != "" {
		t.Fatalf("unreadable Workspace produced intent: %+v", result.Intent)
	}
}

func admitDigest(t *testing.T, command application.DirectAdmissionCommand) string {
	t.Helper()
	result, err := newAdmissionService().Admit(command)
	if err != nil {
		t.Fatal(err)
	}
	return result.Intent.TaskSpecDigest
}

func newAdmissionService() application.DirectAdmission {
	return application.DirectAdmission{
		IDs: &fixedIDs{values: []string{"assessment-1", "intent-1"}},
		Now: func() time.Time { return time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC) },
		Policy: application.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1",
			TriggerPolicyRevision:   "direct-create-v1",
			NetworkProfile:          currentNetworkProfile(),
		},
	}
}

func currentNetworkProfile() config.RuntimeNetworkProfile {
	profile, _ := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	return profile
}

func directAdmissionCommand() application.DirectAdmissionCommand {
	return application.DirectAdmissionCommand{
		Actor: application.Actor{TenantID: "tenant-local", UserID: "user-local"},
		Workspace: application.WorkspaceRevisionEvidence{
			TenantID: "tenant-local", WorkspaceID: "workspace-demo",
			WorkspaceRevisionID: "workspace-revision-1", ResolutionID: "resolution-1",
			ResolutionRevision: 1, MembershipRevision: 1, Readable: true,
		},
		AgentProfile: application.AgentProfileEvidence{
			ProfileID: "deepseek-codex-openai-v1", Revision: "1", Enabled: true,
		},
		Request: application.DirectTaskRequest{
			ProjectID: "project-switchback", Title: "Repair counter",
			Goal:       "Repair the counter and prove it with tests.",
			TargetKind: "workspace", TargetRef: "workspace-revision-1",
			Constraints:        []string{"Do not publish remote changes."},
			AcceptanceCriteria: []string{"All repository tests pass."},
			AcceptanceMode:     "manual", UserVisibleTypes: []string{"report", "patch"},
		},
	}
}

type fixedIDs struct {
	values []string
	index  int
}

func (ids *fixedIDs) NewID(string) (string, error) {
	value := ids.values[ids.index%len(ids.values)]
	ids.index++
	return value, nil
}
