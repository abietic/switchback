package application_test

import (
	"errors"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestDirectAdmissionEmitsCurrentServerNetworkProfile(t *testing.T) {
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := application.DirectAdmission{
		IDs: &fixedIDs{values: []string{"assessment-network", "intent-network"}},
		Now: func() time.Time { return time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC) },
		Policy: application.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1",
			TriggerPolicyRevision:   "direct-create-v1",
			NetworkProfile:          profile,
		},
	}

	result, err := service.Admit(directAdmissionCommand())
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskSpec.RuntimeConfig.NetworkPolicy != profile.ID ||
		result.TaskSpec.RuntimeConfig.NetworkPolicyRevision != profile.Revision ||
		result.TaskSpec.RuntimeConfig.NetworkPolicyDigest != profile.Digest {
		t.Fatalf("runtime config = %+v", result.TaskSpec.RuntimeConfig)
	}
}

func TestDirectAdmissionRejectsLegacyNetworkProfileForNewTask(t *testing.T) {
	service := application.DirectAdmission{
		IDs: &fixedIDs{values: []string{"assessment-network", "intent-network"}},
		Now: func() time.Time { return time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC) },
		Policy: application.DirectAdmissionPolicy{
			ReadinessPolicyRevision: "direct-ready-v1",
			TriggerPolicyRevision:   "direct-create-v1",
			NetworkProfile: config.RuntimeNetworkProfile{
				ID: config.LegacyPublicWebRestrictedV1,
			},
		},
	}

	if _, err := service.Admit(directAdmissionCommand()); !errors.Is(
		err,
		application.ErrAdmissionConfiguration,
	) {
		t.Fatalf("error = %v, want ErrAdmissionConfiguration", err)
	}
}
