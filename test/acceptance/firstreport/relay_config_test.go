package firstreport_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/localproduct"
	"github.com/abietic/switchback/internal/platform/config"
)

func TestFirstReportRelayConfigurationUsesOnlyItsProtectedToken(t *testing.T) {
	root := t.TempDir()
	token := filepath.Join(root, "relay-token")
	input := map[string]string{
		"SWITCHBACK_DATABASE_URL":                  "postgres://local@localhost/switchback",
		"SWITCHBACK_DATA_ROOT":                     root,
		"SWITCHBACK_ENGINE_DATA_ROOT":              root,
		"SWITCHBACK_WORKSPACE_SOURCE_ROOT":         root,
		"SWITCHBACK_M1_WORKER_DELIVERY_ENABLED":    "1",
		"SWITCHBACK_FIRST_REPORT_RELAY_TOKEN_FILE": token,
		"SWITCHBACK_FIRST_REPORT_ENGINE_ID":        "qualified-engine-1",
		"SWITCHBACK_WORKER_IMAGE":                  "switchback-worker:local",
		"SWITCHBACK_WORKER_NETWORK":                "worker-control",
		"SWITCHBACK_BACKEND_GRPC_TARGET":           "backend:9090",
		"SWITCHBACK_BACKEND_HTTP_BASE_URL":         "http://backend:8080",
	}
	lookup := func(name string) (string, bool) {
		value, found := input[name]
		return value, found
	}
	configured, err := localproduct.LoadConfig(lookup)
	if err != nil {
		t.Fatal(err)
	}
	if configured.RelayTokenFile != token || configured.DeepSeekCredentialFile != "" || configured.KimiCredentialFile != "" {
		t.Fatal("first-report configuration did not isolate the relay token")
	}
	input["SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE"] = filepath.Join(root, "direct-key")
	if _, err := localproduct.LoadConfig(lookup); err == nil || !strings.Contains(err.Error(), "direct provider credentials") {
		t.Fatalf("expected first-report configuration to reject a direct credential, got %v", err)
	}
	delete(input, "SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE")
	delete(input, "SWITCHBACK_FIRST_REPORT_ENGINE_ID")
	if _, err := localproduct.LoadConfig(lookup); err == nil || !strings.Contains(err.Error(), "qualified Docker Engine ID") {
		t.Fatalf("expected first-report configuration to require the Engine binding, got %v", err)
	}
	input["SWITCHBACK_FIRST_REPORT_ENGINE_ID"] = "qualified-engine-1"
	input["SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED"] = "1"
	if _, err := localproduct.LoadConfig(lookup); err == nil || !strings.Contains(err.Error(), "first-report relay requires") {
		t.Fatalf("expected first-report configuration to reject Runtime recovery, got %v", err)
	}
}

func TestFirstReportProfileBindsItsOwnRouteAndHasNoFallback(t *testing.T) {
	registry := config.ProductionProfileRegistry()
	profile, found := registry.Agent(config.DeepSeekFirstReportV1)
	if !found {
		t.Fatal("first-report profile is missing")
	}
	resolved, err := registry.ResolveAgent(profile.Ref())
	if err != nil {
		t.Fatal(err)
	}
	direct, _ := registry.Agent(config.DeepSeekCodexOpenAIV1)
	if resolved.APIBase != "http://first-report-model-relay:8080" || resolved.CredentialRef == direct.CredentialRef || resolved.AutomaticFallbackProfileID != "" {
		t.Fatal("first-report profile can reach a direct provider or fallback")
	}
}
