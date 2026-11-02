package contractsv1_test

import (
	"bytes"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
)

func TestTaskSpecAcceptsCurrentNetworkEvidence(t *testing.T) {
	spec := legacyTaskSpec(t)
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	spec.RuntimeConfig = contractsv1.RuntimeConfig{
		NetworkPolicy:         profile.ID,
		NetworkPolicyRevision: profile.Revision,
		NetworkPolicyDigest:   profile.Digest,
	}

	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskSpecReadsLegacyNetworkEvidenceWithoutInventingEnforcement(t *testing.T) {
	data := readTestdata(t, "task-spec.json")
	if !bytes.Contains(data, []byte(`"network_policy":"m1_public_web_restricted_v1"`)) {
		t.Fatal("historical fixture no longer contains the stored legacy profile")
	}
	for _, forbidden := range [][]byte{
		[]byte(`"network_policy_revision"`),
		[]byte(`"network_policy_digest"`),
		[]byte(`"legacy_unenforced"`),
	} {
		if bytes.Contains(data, forbidden) {
			t.Fatalf("historical fixture was rewritten with %s", forbidden)
		}
	}

	spec, err := contractsv1.DecodeTaskSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	if spec.RuntimeConfig.NetworkPolicy != config.LegacyPublicWebRestrictedV1 ||
		spec.RuntimeConfig.NetworkPolicyRevision != 0 ||
		spec.RuntimeConfig.NetworkPolicyDigest != "" {
		t.Fatalf("legacy runtime config = %+v", spec.RuntimeConfig)
	}
}

func TestTaskSpecRejectsInvalidNetworkEvidenceShapes(t *testing.T) {
	profile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]contractsv1.RuntimeConfig{
		"unknown": {
			NetworkPolicy: "caller_selected_network",
		},
		"current without revision": {
			NetworkPolicy:       profile.ID,
			NetworkPolicyDigest: profile.Digest,
		},
		"current without digest": {
			NetworkPolicy:         profile.ID,
			NetworkPolicyRevision: profile.Revision,
		},
		"legacy with revision": {
			NetworkPolicy:         config.LegacyPublicWebRestrictedV1,
			NetworkPolicyRevision: 1,
		},
		"legacy with digest": {
			NetworkPolicy:       config.LegacyPublicWebRestrictedV1,
			NetworkPolicyDigest: profile.Digest,
		},
	}
	for name, runtimeConfig := range tests {
		t.Run(name, func(t *testing.T) {
			spec := legacyTaskSpec(t)
			spec.RuntimeConfig = runtimeConfig
			if err := spec.Validate(); err == nil {
				t.Fatalf("invalid runtime config was accepted: %+v", runtimeConfig)
			}
		})
	}
}

func legacyTaskSpec(t *testing.T) contractsv1.TaskSpec {
	t.Helper()
	spec, err := contractsv1.DecodeTaskSpec(readTestdata(t, "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
