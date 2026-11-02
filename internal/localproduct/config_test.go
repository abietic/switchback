package localproduct

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/platform/config"
	"gopkg.in/yaml.v3"
)

func TestOptionalObservabilityConfigurationNeverRejectsCore(t *testing.T) {
	for _, tt := range []struct {
		name, url, public, secret, code string
		enabled                         bool
	}{
		{name: "empty"},
		{name: "partial", url: "http://langfuse:3000", code: "observability_config_invalid"},
		{name: "malformed", url: "http://user:password@langfuse:3000", public: "public", secret: "secret", code: "observability_config_invalid"},
		{name: "enabled", url: "http://langfuse:3000", public: "public", secret: "secret", enabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			configuration, err := LoadConfig(mapLookup(map[string]string{"SWITCHBACK_DATABASE_URL": "postgres://localhost/switchback", "SWITCHBACK_DATA_ROOT": t.TempDir(), "SWITCHBACK_M2_ASSISTANT_ENABLED": "1", "SWITCHBACK_ASSISTANT_LITELLM_BASE_URL": "http://litellm:4000", "SWITCHBACK_ASSISTANT_LITELLM_API_KEY": "model-key", "LANGFUSE_BASE_URL": tt.url, "LANGFUSE_PUBLIC_KEY": tt.public, "LANGFUSE_SECRET_KEY": tt.secret}))
			if err != nil {
				t.Fatal(err)
			}
			enabled, code := configuration.observabilityConfiguration()
			if enabled != tt.enabled || code != tt.code {
				t.Fatalf("enabled=%v code=%q", enabled, code)
			}
		})
	}
}

func TestOptionalDiagnosticConfigurationNeverRejectsCore(t *testing.T) {
	for _, base := range []string{"", "http://localhost:3001", "http://user:secret@grafana:3000"} {
		configuration, err := LoadConfig(mapLookup(map[string]string{
			"SWITCHBACK_DATABASE_URL": "postgres://localhost/switchback", "SWITCHBACK_DATA_ROOT": t.TempDir(),
			"SWITCHBACK_GRAFANA_BROWSER_BASE_URL":  base,
			"SWITCHBACK_LANGFUSE_BROWSER_BASE_URL": base,
			"SWITCHBACK_LANGFUSE_PROJECT_ID":       "invalid/path-is-optional",
		}))
		if err != nil || configuration.GrafanaBrowserBaseURL != base || configuration.LangfuseBrowserBaseURL != base || configuration.LangfuseProjectID != "invalid/path-is-optional" {
			t.Fatalf("optional diagnostic config rejected: %v", err)
		}
	}
}

func TestComposeLangfuseBrowserProjectSharesInitValue(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/local/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct{ Environment map[string]string }
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	backend := document.Services["switchback-backend"].Environment
	web := document.Services["langfuse-web"].Environment
	if backend["SWITCHBACK_LANGFUSE_PROJECT_ID"] != "${LANGFUSE_INIT_PROJECT_ID:-switchback-m1}" || backend["SWITCHBACK_LANGFUSE_PROJECT_ID"] != web["LANGFUSE_INIT_PROJECT_ID"] {
		t.Fatal("Langfuse public project does not share the init value")
	}
	if backend["SWITCHBACK_LANGFUSE_BROWSER_BASE_URL"] != "${SWITCHBACK_LANGFUSE_BROWSER_BASE_URL-http://localhost:${SWITCHBACK_LANGFUSE_PORT:-3000}}" {
		t.Fatal("Langfuse browser origin is not the public loopback config")
	}
	if !strings.Contains(string(raw), "SWITCHBACK_LANGFUSE_PROJECT_ID: *langfuse-project-id") || !strings.Contains(string(raw), "LANGFUSE_INIT_PROJECT_ID: *langfuse-project-id") {
		t.Fatal("Langfuse project defaults must share one anchor")
	}
}

func TestLoadConfigRequiresOnlyPersistenceWhenExecutionIsDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL": "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ExecutionEnabled || configuration.WorkspaceID != defaultWorkspaceID ||
		configuration.Profile.TenantID != "tenant-local" || configuration.Profile.ActorUserID != "user-local" ||
		!configuration.WorkerDisplayEnabled || configuration.RuntimeRecoveryEnabled {
		t.Fatalf("configuration=%+v", configuration)
	}
}

func TestLoadConfigRejectsRuntimeRecoveryWithoutWorkerDelivery(t *testing.T) {
	_, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL":                    "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":                       t.TempDir(),
		"SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED": "1",
	}))
	if err == nil {
		t.Fatal("Runtime recovery was enabled without Worker delivery")
	}
}

func TestLoadConfigCanDisableWorkerDisplayProjection(t *testing.T) {
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL":                      "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":                         t.TempDir(),
		"SWITCHBACK_WORKER_DISPLAY_PROJECTION_ENABLED": "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.WorkerDisplayEnabled {
		t.Fatalf("configuration=%+v", configuration)
	}
}

func TestLoadConfigRequiresServerOwnedRuntimeInputsWhenExecutionIsEnabled(t *testing.T) {
	_, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL": "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":    t.TempDir(), "SWITCHBACK_M1_WORKER_DELIVERY_ENABLED": "1",
	}))
	if err == nil {
		t.Fatal("execution configuration accepted without server-owned Runtime inputs")
	}
}

func TestLoadConfigRequiresAssistantProxyOnlyWhenAssistantIsEnabled(t *testing.T) {
	base := map[string]string{
		"SWITCHBACK_DATABASE_URL": "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":    t.TempDir(),
	}
	base["SWITCHBACK_M2_ASSISTANT_ENABLED"] = "1"
	if _, err := LoadConfig(mapLookup(base)); err == nil {
		t.Fatal("Assistant configuration accepted without its server-owned LiteLLM route")
	}

	base["SWITCHBACK_ASSISTANT_LITELLM_BASE_URL"] = "http://assistant-litellm:4000"
	base["SWITCHBACK_ASSISTANT_LITELLM_API_KEY"] = "assistant-proxy-key"
	configuration, err := LoadConfig(mapLookup(base))
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.Assistant.Enabled ||
		configuration.Assistant.ModelProfile.ID != config.KimiCodeAssistantOpenAIV1 ||
		configuration.Assistant.ModelProfile.Revision != 2 ||
		configuration.AssistantLiteLLMBaseURL != "http://assistant-litellm:4000" ||
		configuration.AssistantLiteLLMAPIKey != "assistant-proxy-key" {
		t.Fatalf("configuration=%+v", configuration)
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("valid Kimi revision-2 configuration was rejected: %v", err)
	}
}

func TestConfigValidateRejectsStaleOrTamperedKimiProfileBeforeProviderStartup(t *testing.T) {
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL":               "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":                  t.TempDir(),
		"SWITCHBACK_M2_ASSISTANT_ENABLED":       "1",
		"SWITCHBACK_ASSISTANT_LITELLM_BASE_URL": "http://assistant-litellm:4000",
		"SWITCHBACK_ASSISTANT_LITELLM_API_KEY":  "assistant-proxy-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for name, ref := range map[string]config.AssistantModelProfileRef{
		"historical revision one": {
			ID:       config.KimiCodeAssistantOpenAIV1,
			Revision: 1,
			Digest:   "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c",
		},
		"tampered policy digest": {
			ID:       config.KimiCodeAssistantOpenAIV1,
			Revision: 2,
			Digest:   "sha256:caller",
		},
	} {
		t.Run(name, func(t *testing.T) {
			current := configuration
			current.Assistant.ModelProfile = ref
			if err := current.Validate(); err == nil ||
				!strings.Contains(err.Error(), "Assistant model profile is invalid") {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}

func TestConfigValidateRejectsAssistantModelOutsideDeploymentScopeBeforeProviderValidation(t *testing.T) {
	registry := config.ProductionProfileRegistry()
	profile, ok := registry.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Kimi Code Assistant profile missing")
	}
	configuration := Config{
		Profile:   config.SingleUserLocalProfile{Name: "another_deployment_profile"},
		Assistant: config.AssistantSettings{Enabled: true, ModelProfile: profile.Ref()},
	}
	if err := configuration.Validate(); err == nil ||
		!strings.Contains(err.Error(), "Assistant model profile is not allowed for the deployment profile") {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestLoadConfigRejectsNonCanonicalAssistantProxyInputs(t *testing.T) {
	for name, value := range map[string]string{
		"base URL credentials": "http://user:pass@assistant-litellm:4000",
		"base URL path":        "http://assistant-litellm:4000/v1",
		"proxy key newline":    "assistant-proxy-key\n",
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{
				"SWITCHBACK_DATABASE_URL":               "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
				"SWITCHBACK_DATA_ROOT":                  t.TempDir(),
				"SWITCHBACK_M2_ASSISTANT_ENABLED":       "1",
				"SWITCHBACK_ASSISTANT_LITELLM_BASE_URL": "http://assistant-litellm:4000",
				"SWITCHBACK_ASSISTANT_LITELLM_API_KEY":  "assistant-proxy-key",
			}
			if name == "proxy key newline" {
				values["SWITCHBACK_ASSISTANT_LITELLM_API_KEY"] = value
			} else {
				values["SWITCHBACK_ASSISTANT_LITELLM_BASE_URL"] = value
			}
			if _, err := LoadConfig(mapLookup(values)); err == nil {
				t.Fatal("invalid Assistant proxy configuration was accepted")
			}
		})
	}
}

func TestLoadConfigDisablesPartialOptionalAssistantLangfuseInputs(t *testing.T) {
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL":               "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":                  t.TempDir(),
		"SWITCHBACK_M2_ASSISTANT_ENABLED":       "1",
		"SWITCHBACK_ASSISTANT_LITELLM_BASE_URL": "http://assistant-litellm:4000",
		"SWITCHBACK_ASSISTANT_LITELLM_API_KEY":  "assistant-proxy-key",
		"LANGFUSE_BASE_URL":                     "http://langfuse-web:3000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if enabled, code := configuration.observabilityConfiguration(); enabled || code != "observability_config_invalid" {
		t.Fatal("partial optional export must be disabled")
	}
}

func TestLoadConfigAcceptsExactExecutionAndLangfuseInputs(t *testing.T) {
	root := t.TempDir()
	configuration, err := LoadConfig(mapLookup(map[string]string{
		"SWITCHBACK_DATABASE_URL":                    "postgres://switchback:local@127.0.0.1:5432/switchback?sslmode=disable",
		"SWITCHBACK_DATA_ROOT":                       filepath.Join(root, "data"),
		"SWITCHBACK_ENGINE_DATA_ROOT":                filepath.Join(root, "engine"),
		"SWITCHBACK_WORKSPACE_SOURCE_ROOT":           filepath.Join(root, "source"),
		"SWITCHBACK_WORKER_IMAGE":                    "switchback-worker:local",
		"SWITCHBACK_WORKER_NETWORK":                  "switchback-m1-worker-control",
		"SWITCHBACK_BACKEND_GRPC_TARGET":             "switchback-backend:9090",
		"SWITCHBACK_BACKEND_HTTP_BASE_URL":           "http://switchback-backend:8080",
		"SWITCHBACK_KIMI_CREDENTIAL_FILE":            filepath.Join(root, "kimi.key"),
		"SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE":        filepath.Join(root, "deepseek.key"),
		"SWITCHBACK_M1_WORKER_DELIVERY_ENABLED":      "1",
		"SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED": "1",
		"LANGFUSE_BASE_URL":                          "http://langfuse-web:3000",
		"LANGFUSE_PUBLIC_KEY":                        "pk-local",
		"LANGFUSE_SECRET_KEY":                        "sk-local",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.ExecutionEnabled || !configuration.RuntimeRecoveryEnabled ||
		configuration.LangfuseBaseURL != "http://langfuse-web:3000" {
		t.Fatalf("configuration=%+v", configuration)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
