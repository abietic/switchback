package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

const (
	LocalStandardV1           = "local_standard_v1"
	DeepSeekCodexOpenAIV1     = "deepseek-codex-openai-v1"
	KimiCodexAnthropicV1      = "kimi-codex-anthropic-v1"
	KimiAssistantOpenAIV1     = "kimi-assistant-openai-v1"
	KimiCodeAssistantOpenAIV1 = "kimi-code-assistant-openai-v1"
	DeepSeekAssistantOpenAIV1 = "deepseek-assistant-openai-v1"
	UpstreamProtocolOpenAI    = "openai"
	UpstreamProtocolAnthropic = "anthropic"
)

type RuntimeResourceProfileRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}

type RuntimeResourceProfile struct {
	ID                 string `json:"id"`
	Revision           uint64 `json:"revision"`
	CPUUnits           int64  `json:"cpu_units"`
	MemoryBytes        int64  `json:"memory_bytes"`
	PIDsLimit          int64  `json:"pids_limit"`
	WritableLayerBytes int64  `json:"writable_layer_bytes"`
	Digest             string `json:"digest"`
}

func (p RuntimeResourceProfile) Ref() RuntimeResourceProfileRef {
	return RuntimeResourceProfileRef{ID: p.ID, Revision: p.Revision, Digest: p.Digest}
}

func (p RuntimeResourceProfile) ValidateDigest() error {
	return validateProfileDigest(p.Digest, runtimeResourceProfileDigest(p))
}

type AgentProfileRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}

type AgentProfile struct {
	ID                         string `json:"id"`
	Revision                   uint64 `json:"revision"`
	AgentAdapter               string `json:"agent_adapter"`
	LiteLLMModel               string `json:"litellm_model"`
	APIBase                    string `json:"api_base"`
	UpstreamProtocol           string `json:"upstream_protocol"`
	CredentialRef              string `json:"credential_ref"`
	AutomaticFallbackProfileID string `json:"automatic_fallback_profile_id,omitempty"`
	Digest                     string `json:"digest"`
}

func (p AgentProfile) Ref() AgentProfileRef {
	return AgentProfileRef{ID: p.ID, Revision: p.Revision, Digest: p.Digest}
}

func (p AgentProfile) ValidateDigest() error {
	return validateProfileDigest(p.Digest, agentProfileDigest(p))
}

type AssistantModelProfileRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Digest   string `json:"digest"`
}

type AssistantModelProfile struct {
	ID                         string                       `json:"id"`
	Revision                   uint64                       `json:"revision"`
	LiteLLMModel               string                       `json:"litellm_model"`
	ResponseSchema             string                       `json:"response_schema"`
	DeploymentScope            string                       `json:"deployment_scope,omitempty"`
	RequestPolicy              *AssistantModelRequestPolicy `json:"request_policy,omitempty"`
	AutomaticFallbackProfileID string                       `json:"automatic_fallback_profile_id,omitempty"`
	Digest                     string                       `json:"digest"`
}

type AssistantModelRequestPolicy struct {
	Temperature      *float64                         `json:"temperature,omitempty"`
	StructuredOutput *AssistantStructuredOutputPolicy `json:"structured_output,omitempty"`
}

type AssistantStructuredOutputPolicy struct {
	Mode                 string `json:"mode"`
	DecisionSchema       string `json:"decision_schema"`
	ProviderSchemaID     string `json:"provider_schema_id"`
	ProviderSchemaDigest string `json:"provider_schema_digest"`
}

func (profile AssistantModelProfile) Ref() AssistantModelProfileRef {
	return AssistantModelProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}
}

func (profile AssistantModelProfile) ValidateDigest() error {
	if err := validateAssistantRequestPolicy(profile.ResponseSchema, profile.RequestPolicy); err != nil {
		return err
	}
	if profile.AutomaticFallbackProfileID != "" {
		return fmt.Errorf("Assistant model profile must not configure an automatic fallback")
	}
	return validateProfileDigest(profile.Digest, assistantProfileDigest(profile))
}

func validateAssistantRequestPolicy(
	responseSchema string,
	policy *AssistantModelRequestPolicy,
) error {
	if policy == nil {
		return nil
	}
	if policy.Temperature != nil {
		temperature := *policy.Temperature
		if math.IsNaN(temperature) || math.IsInf(temperature, 0) || temperature < 0 || temperature > 2 {
			return fmt.Errorf("Assistant model request policy temperature must be finite and between 0 and 2")
		}
	}
	if policy.StructuredOutput == nil {
		return nil
	}
	structured := policy.StructuredOutput
	if structured.Mode != contractsv1.AssistantStructuredOutputModeStrictJSONSchema {
		return fmt.Errorf("Assistant model structured output mode is unsupported")
	}
	if structured.DecisionSchema != responseSchema ||
		structured.DecisionSchema != contractsv1.AssistantDecisionSchemaVersion {
		return fmt.Errorf("Assistant model structured output decision schema is unsupported")
	}
	if structured.ProviderSchemaID != contractsv1.AssistantDecisionProviderSchemaID {
		return fmt.Errorf("Assistant model structured output provider schema id is unsupported")
	}
	if structured.ProviderSchemaDigest != contractsv1.AssistantDecisionProviderSchemaDigest {
		return fmt.Errorf("Assistant model structured output provider schema digest is unsupported")
	}
	return nil
}

type ProfileRegistry struct {
	runtimeResources map[string]RuntimeResourceProfile
	agents           map[string]AgentProfile
	assistants       map[string]AssistantModelProfile
}

func ProductionProfileRegistry() ProfileRegistry {
	resources := RuntimeResourceProfile{
		ID:                 LocalStandardV1,
		Revision:           1,
		CPUUnits:           2,
		MemoryBytes:        4 << 30,
		PIDsLimit:          512,
		WritableLayerBytes: 10 << 30,
	}
	resources.Digest = runtimeResourceProfileDigest(resources)

	deepseek := AgentProfile{
		ID:               DeepSeekCodexOpenAIV1,
		Revision:         1,
		AgentAdapter:     "codex-acp",
		LiteLLMModel:     "deepseek/deepseek-v4-pro",
		APIBase:          "https://api.deepseek.com",
		UpstreamProtocol: UpstreamProtocolOpenAI,
		CredentialRef:    "model-profile/deepseek-codex-openai-v1",
	}
	deepseek.Digest = agentProfileDigest(deepseek)

	kimi := AgentProfile{
		ID:               KimiCodexAnthropicV1,
		Revision:         1,
		AgentAdapter:     "codex-acp",
		LiteLLMModel:     "anthropic/kimi-for-coding",
		APIBase:          "https://api.kimi.com/coding",
		UpstreamProtocol: UpstreamProtocolAnthropic,
		CredentialRef:    "model-profile/kimi-codex-anthropic-v1",
	}
	kimi.Digest = agentProfileDigest(kimi)

	kimiCodeAssistantTemperature := 1.0
	kimiCodeAssistant := AssistantModelProfile{
		ID:              KimiCodeAssistantOpenAIV1,
		Revision:        2,
		LiteLLMModel:    "switchback-assistant-kimi-code",
		ResponseSchema:  contractsv1.AssistantDecisionSchemaVersion,
		DeploymentScope: SingleUserLocalProfileName,
		RequestPolicy: &AssistantModelRequestPolicy{
			Temperature: &kimiCodeAssistantTemperature,
			StructuredOutput: &AssistantStructuredOutputPolicy{
				Mode:                 contractsv1.AssistantStructuredOutputModeStrictJSONSchema,
				DecisionSchema:       contractsv1.AssistantDecisionSchemaVersion,
				ProviderSchemaID:     contractsv1.AssistantDecisionProviderSchemaID,
				ProviderSchemaDigest: contractsv1.AssistantDecisionProviderSchemaDigest,
			},
		},
	}
	kimiCodeAssistant.Digest = assistantProfileDigest(kimiCodeAssistant)
	deepSeekAssistant := AssistantModelProfile{
		ID: DeepSeekAssistantOpenAIV1, Revision: 1, LiteLLMModel: "switchback-assistant-deepseek",
		ResponseSchema: "assistant.decision.v1",
	}
	deepSeekAssistant.Digest = assistantProfileDigest(deepSeekAssistant)

	return ProfileRegistry{
		runtimeResources: map[string]RuntimeResourceProfile{resources.ID: resources},
		agents: map[string]AgentProfile{
			deepseek.ID: deepseek,
			kimi.ID:     kimi,
		},
		assistants: map[string]AssistantModelProfile{
			kimiCodeAssistant.ID: kimiCodeAssistant,
			deepSeekAssistant.ID: deepSeekAssistant,
		},
	}
}

func (r ProfileRegistry) RuntimeResource(id string) (RuntimeResourceProfile, bool) {
	profile, ok := r.runtimeResources[id]
	return profile, ok
}

func (r ProfileRegistry) Agent(id string) (AgentProfile, bool) {
	profile, ok := r.agents[id]
	return profile, ok
}

func (r ProfileRegistry) Assistant(id string) (AssistantModelProfile, bool) {
	profile, ok := r.assistants[id]
	return cloneAssistantModelProfile(profile), ok
}

func cloneAssistantModelProfile(profile AssistantModelProfile) AssistantModelProfile {
	if profile.RequestPolicy == nil {
		return profile
	}
	requestPolicy := *profile.RequestPolicy
	if requestPolicy.Temperature != nil {
		temperature := *requestPolicy.Temperature
		requestPolicy.Temperature = &temperature
	}
	if requestPolicy.StructuredOutput != nil {
		structuredOutput := *requestPolicy.StructuredOutput
		requestPolicy.StructuredOutput = &structuredOutput
	}
	profile.RequestPolicy = &requestPolicy
	return profile
}

func (r ProfileRegistry) ResolveRuntimeResource(ref RuntimeResourceProfileRef) (RuntimeResourceProfile, error) {
	profile, ok := r.RuntimeResource(ref.ID)
	if !ok {
		return RuntimeResourceProfile{}, fmt.Errorf("runtime resource profile %q is unsupported", ref.ID)
	}
	if err := profile.ValidateDigest(); err != nil {
		return RuntimeResourceProfile{}, fmt.Errorf("server runtime resource profile %q is invalid: %w", ref.ID, err)
	}
	if ref.Revision != profile.Revision || ref.Digest != profile.Digest {
		return RuntimeResourceProfile{}, fmt.Errorf("runtime resource profile %q reference is stale or invalid", ref.ID)
	}
	return profile, nil
}

func (r ProfileRegistry) ResolveAgent(ref AgentProfileRef) (AgentProfile, error) {
	profile, ok := r.Agent(ref.ID)
	if !ok {
		return AgentProfile{}, fmt.Errorf("agent profile %q is unsupported", ref.ID)
	}
	if err := profile.ValidateDigest(); err != nil {
		return AgentProfile{}, fmt.Errorf("server agent profile %q is invalid: %w", ref.ID, err)
	}
	if ref.Revision != profile.Revision || ref.Digest != profile.Digest {
		return AgentProfile{}, fmt.Errorf("agent profile %q reference is stale or invalid", ref.ID)
	}
	return profile, nil
}

func (r ProfileRegistry) ResolveAssistant(ref AssistantModelProfileRef) (AssistantModelProfile, error) {
	profile, ok := r.Assistant(ref.ID)
	if !ok {
		return AssistantModelProfile{}, fmt.Errorf("Assistant model profile %q is unsupported", ref.ID)
	}
	if err := profile.ValidateDigest(); err != nil {
		return AssistantModelProfile{}, fmt.Errorf("server Assistant model profile %q is invalid: %w", ref.ID, err)
	}
	if ref.Revision != profile.Revision || ref.Digest != profile.Digest {
		return AssistantModelProfile{}, fmt.Errorf("Assistant model profile %q reference is stale or invalid", ref.ID)
	}
	return profile, nil
}

func runtimeResourceProfileDigest(profile RuntimeResourceProfile) string {
	profile.Digest = ""
	return digestJSON(profile)
}

func agentProfileDigest(profile AgentProfile) string {
	profile.Digest = ""
	return digestJSON(profile)
}

func assistantProfileDigest(profile AssistantModelProfile) string {
	profile.Digest = ""
	return digestJSON(profile)
}

func digestJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal fixed profile: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateProfileDigest(actual, expected string) error {
	if actual == "" || actual != expected {
		return fmt.Errorf("profile digest mismatch")
	}
	return nil
}
