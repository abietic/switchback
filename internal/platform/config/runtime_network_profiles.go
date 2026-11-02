package config

import (
	"fmt"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

const (
	TrustedFullNetworkV1        = contractsv1.RuntimeNetworkPolicyTrustedFullV1
	LegacyPublicWebRestrictedV1 = contractsv1.RuntimeNetworkPolicyLegacyRestrictedV1

	NetworkOutboundUnrestricted = "unrestricted"
	NetworkEnforcementActive    = "active"
	NetworkEnforcementLegacy    = "legacy_unenforced"
)

// RuntimeNetworkProfile is a server-owned deployment capability definition.
type RuntimeNetworkProfile struct {
	ID                string `json:"id"`
	Revision          uint64 `json:"revision"`
	DeploymentProfile string `json:"deployment_profile"`
	OutboundMode      string `json:"outbound_mode"`
	Digest            string `json:"digest"`
}

// RuntimeNetworkEvidence is the immutable identity recorded by execution facts.
type RuntimeNetworkEvidence struct {
	ID                string `json:"id"`
	Revision          uint64 `json:"revision"`
	Digest            string `json:"digest"`
	EnforcementStatus string `json:"enforcement_status"`
}

func (profile RuntimeNetworkProfile) Evidence() RuntimeNetworkEvidence {
	return RuntimeNetworkEvidence{
		ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
		EnforcementStatus: NetworkEnforcementActive,
	}
}

func (profile RuntimeNetworkProfile) ValidateDigest() error {
	return validateProfileDigest(profile.Digest, runtimeNetworkProfileDigest(profile))
}

type RuntimeNetworkProfileRegistry struct {
	currentByDeployment map[string]RuntimeNetworkProfile
}

func ProductionRuntimeNetworkProfiles() RuntimeNetworkProfileRegistry {
	profile := RuntimeNetworkProfile{
		ID:                TrustedFullNetworkV1,
		Revision:          1,
		DeploymentProfile: SingleUserLocalProfileName,
		OutboundMode:      NetworkOutboundUnrestricted,
	}
	profile.Digest = runtimeNetworkProfileDigest(profile)
	return RuntimeNetworkProfileRegistry{
		currentByDeployment: map[string]RuntimeNetworkProfile{
			profile.DeploymentProfile: profile,
		},
	}
}

func (registry RuntimeNetworkProfileRegistry) ResolveForDeployment(
	deploymentProfile string,
) (RuntimeNetworkProfile, error) {
	profile, ok := registry.currentByDeployment[deploymentProfile]
	if !ok {
		return RuntimeNetworkProfile{}, fmt.Errorf(
			"runtime network profile is unavailable for deployment %q",
			deploymentProfile,
		)
	}
	if err := profile.ValidateDigest(); err != nil {
		return RuntimeNetworkProfile{}, fmt.Errorf(
			"server runtime network profile %q is invalid: %w",
			profile.ID,
			err,
		)
	}
	return profile, nil
}

func (registry RuntimeNetworkProfileRegistry) ResolveStored(
	evidence RuntimeNetworkEvidence,
) (RuntimeNetworkEvidence, error) {
	switch evidence.ID {
	case TrustedFullNetworkV1:
		profile, err := registry.ResolveForDeployment(SingleUserLocalProfileName)
		if err != nil {
			return RuntimeNetworkEvidence{}, err
		}
		if evidence.Revision != profile.Revision ||
			evidence.Digest != profile.Digest ||
			evidence.EnforcementStatus != "" &&
				evidence.EnforcementStatus != NetworkEnforcementActive {
			return RuntimeNetworkEvidence{}, fmt.Errorf(
				"runtime network profile %q reference is stale or invalid",
				evidence.ID,
			)
		}
		return profile.Evidence(), nil
	case LegacyPublicWebRestrictedV1:
		if evidence.Revision != 0 ||
			evidence.Digest != "" ||
			evidence.EnforcementStatus != "" &&
				evidence.EnforcementStatus != NetworkEnforcementLegacy {
			return RuntimeNetworkEvidence{}, fmt.Errorf(
				"legacy runtime network evidence is invalid",
			)
		}
		return RuntimeNetworkEvidence{
			ID:                LegacyPublicWebRestrictedV1,
			EnforcementStatus: NetworkEnforcementLegacy,
		}, nil
	default:
		return RuntimeNetworkEvidence{}, fmt.Errorf(
			"runtime network profile %q is unsupported",
			evidence.ID,
		)
	}
}

func runtimeNetworkProfileDigest(profile RuntimeNetworkProfile) string {
	profile.Digest = ""
	return digestJSON(profile)
}
