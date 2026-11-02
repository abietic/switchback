package config_test

import (
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestSingleUserLocalProfileDefaultsAreFixedAndLoopback(t *testing.T) {
	profile, err := config.LoadSingleUserLocalProfile(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if profile.Name != "single_user_local_v1" || profile.TenantID != "tenant-local" || profile.ActorUserID != "user-local" {
		t.Fatalf("unexpected fixed identity: %+v", profile)
	}
	if profile.WebAddress != "127.0.0.1:8080" {
		t.Fatalf("web address = %q", profile.WebAddress)
	}
}

func TestSingleUserLocalProfileLoadsOnlyServerConfiguration(t *testing.T) {
	values := map[string]string{
		"SWITCHBACK_LOCAL_TENANT_ID": "tenant-dev",
		"SWITCHBACK_LOCAL_ACTOR_ID":  "user-dev",
		"SWITCHBACK_WEB_ADDRESS":     "[::1]:18080",
	}
	profile, err := config.LoadSingleUserLocalProfile(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.TenantID != "tenant-dev" || profile.ActorUserID != "user-dev" || profile.WebAddress != "[::1]:18080" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
}

func TestSingleUserLocalProfileRejectsNonLoopbackWebBind(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8080", "192.168.1.2:8080", "localhost:8080", "127.0.0.1:0"} {
		t.Run(address, func(t *testing.T) {
			_, err := config.LoadSingleUserLocalProfile(func(key string) (string, bool) {
				if key == "SWITCHBACK_WEB_ADDRESS" {
					return address, true
				}
				return "", false
			})
			if err == nil {
				t.Fatalf("non-canonical loopback address %q was accepted", address)
			}
		})
	}
}

func TestSingleUserLocalProfileAllowsExplicitContainerBindOnly(t *testing.T) {
	values := map[string]string{"SWITCHBACK_CONTAINERIZED": "1", "SWITCHBACK_WEB_ADDRESS": "0.0.0.0:8080"}
	profile, err := config.LoadSingleUserLocalProfile(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil || !profile.Containerized {
		t.Fatalf("load container profile: profile=%+v error=%v", profile, err)
	}
	values["SWITCHBACK_WEB_ADDRESS"] = "192.168.1.2:8080"
	if _, err := config.LoadSingleUserLocalProfile(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}); err == nil {
		t.Fatal("container profile accepted a non-wildcard bind")
	}
	values["SWITCHBACK_CONTAINERIZED"] = "true"
	if _, err := config.LoadSingleUserLocalProfile(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}); err == nil {
		t.Fatal("container profile accepted a non-canonical boolean")
	}
}
