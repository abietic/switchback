package localproduct

import (
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestLoadConfigResolvesTrustedFullNetworkFromDeploymentProfile(t *testing.T) {
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL": "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":    t.TempDir(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.NetworkProfile.ID != config.TrustedFullNetworkV1 ||
		configuration.NetworkProfile.DeploymentProfile != configuration.Profile.Name ||
		configuration.NetworkProfile.Revision != 1 ||
		configuration.NetworkProfile.Digest == "" {
		t.Fatalf("network profile = %+v", configuration.NetworkProfile)
	}
}

func TestConfigRejectsNetworkProfileOutsideCurrentDeployment(t *testing.T) {
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL": "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":    t.TempDir(),
	}))
	if err != nil {
		t.Fatal(err)
	}

	configuration.NetworkProfile.DeploymentProfile = "multi_user_remote_v1"
	if err := configuration.Validate(); err == nil {
		t.Fatal("configuration accepted network authority from another deployment profile")
	}
}
