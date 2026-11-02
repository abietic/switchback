// Package localproduct composes the single-user M1 product from production ports and adapters.
package localproduct

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/abietic/switchback/internal/platform/config"
)

const (
	defaultProjectID           = "project-local"
	defaultWorkspaceID         = "workspace-local"
	defaultWorkspaceRevisionID = "workspace-revision-local-v1"
	defaultGRPCAddress         = "0.0.0.0:9090"
)

type Config struct {
	RuntimeCapacityAdmissionManifestFile string
	RuntimeCapacityTrustFile             string
	RuntimeCapacitySnapshotDirectory     string
	Profile                              config.SingleUserLocalProfile
	NetworkProfile                       config.RuntimeNetworkProfile
	Assistant                            config.AssistantSettings
	DatabaseURL                          string
	DataRoot                             string
	EngineDataRoot                       string
	WorkspaceSourceRoot                  string
	ProjectID                            string
	WorkspaceID                          string
	WorkspaceRevisionID                  string
	WorkerImage                          string
	WorkerNetwork                        string
	BackendGRPCTarget                    string
	BackendHTTPBaseURL                   string
	GRPCAddress                          string
	KimiCredentialFile                   string
	DeepSeekCredentialFile               string
	LangfuseBaseURL                      string
	LangfusePublicKey                    string
	LangfuseSecretKey                    string
	TelemetryAddress                     string
	AlloyPushURL                         string
	GrafanaBrowserBaseURL                string
	LangfuseBrowserBaseURL               string
	LangfuseProjectID                    string
	AssistantLiteLLMBaseURL              string
	AssistantLiteLLMAPIKey               string
	ExecutionEnabled                     bool
	WorkerDisplayEnabled                 bool
	RuntimeRecoveryEnabled               bool
}

func LoadConfig(lookup config.LookupEnv) (Config, error) {
	profile, err := config.LoadSingleUserLocalProfile(lookup)
	if err != nil {
		return Config{}, err
	}
	profiles := config.ProductionProfileRegistry()
	assistant, err := config.LoadAssistantSettings(lookup, profiles)
	if err != nil {
		return Config{}, err
	}
	networkProfile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(profile.Name)
	if err != nil {
		return Config{}, err
	}
	result := Config{
		RuntimeCapacityAdmissionManifestFile: value(lookup, "SWITCHBACK_RUNTIME_CAPACITY_ADMISSION_MANIFEST_FILE", ""),
		RuntimeCapacityTrustFile:             value(lookup, "SWITCHBACK_RUNTIME_CAPACITY_TRUST_FILE", ""),
		RuntimeCapacitySnapshotDirectory:     value(lookup, "SWITCHBACK_RUNTIME_CAPACITY_SNAPSHOT_DIRECTORY", ""),
		Profile:                              profile,
		NetworkProfile:                       networkProfile,
		Assistant:                            assistant,
		DatabaseURL:                          value(lookup, "SWITCHBACK_DATABASE_URL", ""),
		DataRoot:                             value(lookup, "SWITCHBACK_DATA_ROOT", ""),
		EngineDataRoot:                       value(lookup, "SWITCHBACK_ENGINE_DATA_ROOT", ""),
		WorkspaceSourceRoot:                  value(lookup, "SWITCHBACK_WORKSPACE_SOURCE_ROOT", ""),
		ProjectID:                            value(lookup, "SWITCHBACK_PROJECT_ID", defaultProjectID),
		WorkspaceID:                          value(lookup, "SWITCHBACK_WORKSPACE_ID", defaultWorkspaceID),
		WorkspaceRevisionID:                  value(lookup, "SWITCHBACK_WORKSPACE_REVISION_ID", defaultWorkspaceRevisionID),
		WorkerImage:                          value(lookup, "SWITCHBACK_WORKER_IMAGE", ""),
		WorkerNetwork:                        value(lookup, "SWITCHBACK_WORKER_NETWORK", ""),
		BackendGRPCTarget:                    value(lookup, "SWITCHBACK_BACKEND_GRPC_TARGET", ""),
		BackendHTTPBaseURL:                   value(lookup, "SWITCHBACK_BACKEND_HTTP_BASE_URL", ""),
		GRPCAddress:                          value(lookup, "SWITCHBACK_GRPC_ADDRESS", defaultGRPCAddress),
		KimiCredentialFile:                   value(lookup, "SWITCHBACK_KIMI_CREDENTIAL_FILE", ""),
		DeepSeekCredentialFile:               value(lookup, "SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE", ""),
		LangfuseBaseURL:                      value(lookup, "LANGFUSE_BASE_URL", ""),
		LangfusePublicKey:                    value(lookup, "LANGFUSE_PUBLIC_KEY", ""),
		LangfuseSecretKey:                    value(lookup, "LANGFUSE_SECRET_KEY", ""),
		TelemetryAddress:                     value(lookup, "SWITCHBACK_TELEMETRY_ADDRESS", ""),
		AlloyPushURL:                         value(lookup, "SWITCHBACK_ALLOY_PUSH_URL", ""),
		GrafanaBrowserBaseURL:                value(lookup, "SWITCHBACK_GRAFANA_BROWSER_BASE_URL", ""),
		LangfuseBrowserBaseURL:               value(lookup, "SWITCHBACK_LANGFUSE_BROWSER_BASE_URL", ""),
		LangfuseProjectID:                    value(lookup, "SWITCHBACK_LANGFUSE_PROJECT_ID", ""),
		AssistantLiteLLMBaseURL:              value(lookup, "SWITCHBACK_ASSISTANT_LITELLM_BASE_URL", ""),
		AssistantLiteLLMAPIKey:               value(lookup, "SWITCHBACK_ASSISTANT_LITELLM_API_KEY", ""),
	}
	result.ExecutionEnabled, err = boolValue(lookup, "SWITCHBACK_M1_WORKER_DELIVERY_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	result.WorkerDisplayEnabled, err = boolValue(lookup, "SWITCHBACK_WORKER_DISPLAY_PROJECTION_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	result.RuntimeRecoveryEnabled, err = boolValue(lookup, "SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (configuration Config) Validate() error {
	if err := configuration.validateCapacityPaths(); err != nil {
		return err
	}
	if configuration.Assistant.Enabled {
		profile, err := config.ProductionProfileRegistry().ResolveAssistant(
			configuration.Assistant.ModelProfile,
		)
		if err != nil {
			return fmt.Errorf("Assistant model profile is invalid: %w", err)
		}
		if profile.DeploymentScope != "" &&
			profile.DeploymentScope != configuration.Profile.Name {
			return fmt.Errorf("Assistant model profile is not allowed for the deployment profile")
		}
	}
	networkProfile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		configuration.Profile.Name,
	)
	if err != nil || configuration.NetworkProfile != networkProfile {
		return fmt.Errorf("runtime network profile does not match the deployment profile")
	}
	for name, current := range map[string]string{
		"database URL":          configuration.DatabaseURL,
		"project id":            configuration.ProjectID,
		"workspace id":          configuration.WorkspaceID,
		"workspace revision id": configuration.WorkspaceRevisionID,
	} {
		if !canonical(current) {
			return fmt.Errorf("%s is required", name)
		}
	}
	databaseURL, err := url.Parse(configuration.DatabaseURL)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Host == "" {
		return fmt.Errorf("database URL must use PostgreSQL")
	}
	if err := absolutePath("data root", configuration.DataRoot); err != nil {
		return err
	}
	if configuration.ExecutionEnabled {
		for name, current := range map[string]string{
			"Worker image":          configuration.WorkerImage,
			"Worker network":        configuration.WorkerNetwork,
			"Backend gRPC target":   configuration.BackendGRPCTarget,
			"Backend HTTP base URL": configuration.BackendHTTPBaseURL,
		} {
			if !canonical(current) {
				return fmt.Errorf("%s is required when Worker delivery is enabled", name)
			}
		}
		for name, current := range map[string]string{
			"engine data root":         configuration.EngineDataRoot,
			"Workspace source root":    configuration.WorkspaceSourceRoot,
			"Kimi credential file":     configuration.KimiCredentialFile,
			"DeepSeek credential file": configuration.DeepSeekCredentialFile,
		} {
			if err := absolutePath(name, current); err != nil {
				return err
			}
		}
		if err := hostPort("Backend gRPC target", configuration.BackendGRPCTarget); err != nil {
			return err
		}
		backendURL, err := url.Parse(configuration.BackendHTTPBaseURL)
		if err != nil || backendURL.Scheme != "http" || backendURL.Host == "" || backendURL.User != nil ||
			backendURL.Path != "" || backendURL.RawQuery != "" || backendURL.Fragment != "" {
			return fmt.Errorf("Backend HTTP base URL must be one internal HTTP origin")
		}
	}
	if configuration.RuntimeRecoveryEnabled && !configuration.ExecutionEnabled {
		return fmt.Errorf("single Runtime recovery requires Worker delivery")
	}
	if configuration.Assistant.Enabled {
		if !canonical(configuration.AssistantLiteLLMAPIKey) {
			return fmt.Errorf("Assistant LiteLLM API key is required when Assistant is enabled")
		}
		assistantURL, err := url.Parse(configuration.AssistantLiteLLMBaseURL)
		if err != nil || (assistantURL.Scheme != "http" && assistantURL.Scheme != "https") ||
			assistantURL.Host == "" || assistantURL.User != nil ||
			assistantURL.Path != "" && assistantURL.Path != "/" ||
			assistantURL.RawQuery != "" || assistantURL.Fragment != "" {
			return fmt.Errorf("Assistant LiteLLM base URL must be one HTTP(S) origin")
		}
	}
	return hostPort("gRPC address", configuration.GRPCAddress)
}

func (configuration Config) observabilityConfiguration() (bool, string) {
	if configuration.LangfuseBaseURL == "" && configuration.LangfusePublicKey == "" && configuration.LangfuseSecretKey == "" {
		return false, ""
	}
	for _, value := range []string{configuration.LangfuseBaseURL, configuration.LangfusePublicKey, configuration.LangfuseSecretKey} {
		if !canonical(value) {
			return false, "observability_config_invalid"
		}
	}
	endpoint, err := url.Parse(configuration.LangfuseBaseURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" || endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return false, "observability_config_invalid"
	}
	return true, ""
}

func value(lookup config.LookupEnv, name, fallback string) string {
	if current, ok := lookup(name); ok {
		return current
	}
	return fallback
}

func boolValue(lookup config.LookupEnv, name string, fallback bool) (bool, error) {
	current, ok := lookup(name)
	if !ok {
		return fallback, nil
	}
	switch current {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 0 or 1", name)
	}
}

func absolutePath(name, current string) error {
	if current == "" || !filepath.IsAbs(current) || filepath.Clean(current) != current {
		return fmt.Errorf("%s must be a clean absolute path", name)
	}
	return nil
}

func hostPort(name, current string) error {
	host, port, err := net.SplitHostPort(current)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%s must be host:port", name)
	}
	return nil
}

func canonical(current string) bool {
	return current != "" && strings.TrimSpace(current) == current && !strings.ContainsAny(current, "\r\n\x00")
}

func (c Config) validateCapacityPaths() error {
	if c.RuntimeCapacityTrustFile == "" && c.RuntimeCapacitySnapshotDirectory == "" && c.RuntimeCapacityAdmissionManifestFile == "" {
		return nil
	}
	for _, p := range []string{c.RuntimeCapacityTrustFile, c.RuntimeCapacitySnapshotDirectory, c.RuntimeCapacityAdmissionManifestFile} {
		if !canonical(p) || !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return fmt.Errorf("runtime capacity trust file, snapshot directory and admission manifest must be grouped canonical absolute paths")
		}
	}
	return nil
}
