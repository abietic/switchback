package config_test

import (
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestTrustedFullNetworkResolvesOnlyForSingleUserLocalProfile(t *testing.T) {
	registry := config.ProductionRuntimeNetworkProfiles()

	profile, err := registry.ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != config.TrustedFullNetworkV1 ||
		profile.Revision != 1 ||
		profile.DeploymentProfile != config.SingleUserLocalProfileName ||
		profile.OutboundMode != config.NetworkOutboundUnrestricted ||
		profile.Digest == "" {
		t.Fatalf("profile = %+v", profile)
	}
	if _, err := registry.ResolveForDeployment("multi_user_remote_v1"); err == nil {
		t.Fatal("trusted full network escaped SingleUserLocalProfile")
	}
}

func TestStoredCurrentNetworkEvidenceRequiresExactServerProfile(t *testing.T) {
	registry := config.ProductionRuntimeNetworkProfiles()
	profile, err := registry.ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}

	evidence, err := registry.ResolveStored(config.RuntimeNetworkEvidence{
		ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.EnforcementStatus != config.NetworkEnforcementActive {
		t.Fatalf("evidence = %+v", evidence)
	}

	for name, candidate := range map[string]config.RuntimeNetworkEvidence{
		"revision": {ID: profile.ID, Revision: profile.Revision + 1, Digest: profile.Digest},
		"digest":   {ID: profile.ID, Revision: profile.Revision, Digest: "sha256:caller"},
		"status": {
			ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
			EnforcementStatus: config.NetworkEnforcementLegacy,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.ResolveStored(candidate); err == nil {
				t.Fatalf("tampered evidence was accepted: %+v", candidate)
			}
		})
	}
}

func TestStoredLegacyNetworkEvidenceIsReadableButUnenforced(t *testing.T) {
	registry := config.ProductionRuntimeNetworkProfiles()

	got, err := registry.ResolveStored(config.RuntimeNetworkEvidence{
		ID: config.LegacyPublicWebRestrictedV1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != config.LegacyPublicWebRestrictedV1 ||
		got.Revision != 0 ||
		got.Digest != "" ||
		got.EnforcementStatus != config.NetworkEnforcementLegacy {
		t.Fatalf("legacy evidence = %+v", got)
	}

	if _, err := registry.ResolveStored(config.RuntimeNetworkEvidence{
		ID: config.LegacyPublicWebRestrictedV1, Revision: 1, Digest: "sha256:invented",
	}); err == nil {
		t.Fatal("legacy evidence accepted invented enforcement identity")
	}
}
