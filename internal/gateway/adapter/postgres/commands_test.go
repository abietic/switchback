package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abietic/switchback/internal/platform/config"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestNewCommandServiceRequiresExactCurrentAssistantProfile(t *testing.T) {
	profiles := config.ProductionProfileRegistry()
	current, ok := profiles.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("current Kimi Code Assistant profile missing")
	}

	tests := []struct {
		name string
		ref  config.AssistantModelProfileRef
	}{
		{
			name: "unknown ID",
			ref: config.AssistantModelProfileRef{
				ID:       "unknown-assistant-profile",
				Revision: current.Revision,
				Digest:   current.Digest,
			},
		},
		{
			name: "stale revision one",
			ref: config.AssistantModelProfileRef{
				ID:       current.ID,
				Revision: 1,
				Digest:   "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c",
			},
		},
		{
			name: "digest mismatch",
			ref: config.AssistantModelProfileRef{
				ID:       current.ID,
				Revision: current.Revision,
				Digest:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validCommandServiceOptions(t)
			options.Assistant.ModelProfile = test.ref
			if _, err := NewCommandService(options); err == nil {
				t.Fatal("NewCommandService accepted non-current Assistant profile")
			}
		})
	}

	options := validCommandServiceOptions(t)
	options.Assistant.ModelProfile = current.Ref()
	if service, err := NewCommandService(options); err != nil || service == nil {
		t.Fatalf("NewCommandService rejected current registry-owned Assistant profile: service=%+v error=%v",
			service, err)
	}
}

func TestNewCommandServiceKeepsAssistantDisabledConfigurationValid(t *testing.T) {
	options := validCommandServiceOptions(t)
	options.Profiles = config.ProfileRegistry{}
	options.Assistant = config.AssistantSettings{}
	if service, err := NewCommandService(options); err != nil || service == nil {
		t.Fatalf("NewCommandService rejected Assistant-disabled configuration: service=%+v error=%v",
			service, err)
	}
}

func validCommandServiceOptions(t *testing.T) CommandServiceOptions {
	t.Helper()
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	profiles := config.ProductionProfileRegistry()
	current, ok := profiles.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("current Kimi Code Assistant profile missing")
	}
	return CommandServiceOptions{
		Pool:           &pgxpool.Pool{},
		IDs:            commandServiceIDs{},
		Now:            func() time.Time { return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC) },
		ProjectID:      "project-local",
		Profiles:       profiles,
		NetworkProfile: network,
		Workspace: taskapp.WorkspaceRevisionEvidence{
			TenantID:            "tenant-local",
			WorkspaceID:         "workspace-local",
			WorkspaceRevisionID: "workspace-revision-local-v1",
			ResolutionID:        "resolution-local",
			ResolutionRevision:  1,
			MembershipRevision:  1,
			Readable:            true,
		},
		Assistant: config.AssistantSettings{
			Enabled:                 true,
			ModelProfile:            current.Ref(),
			ReadinessPolicyRevision: config.DefaultAssistantReadinessPolicy,
			TriggerPolicyRevision:   config.DefaultAssistantTriggerPolicy,
			ContextBudgetTokens:     8192,
			RequestTimeout:          time.Minute,
			MaxRecentTurns:          24,
		},
	}
}

type commandServiceIDs struct{}

func (commandServiceIDs) NewID(prefix string) (string, error) {
	return prefix + "-1", nil
}
