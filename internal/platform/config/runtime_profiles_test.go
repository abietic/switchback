package config_test

import (
	"math"
	"strings"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
)

func TestProductionProfileRegistryResolvesFixedServerOwnedProfiles(t *testing.T) {
	registry := config.ProductionProfileRegistry()

	resources, err := registry.ResolveRuntimeResource(config.RuntimeResourceProfileRef{
		ID:       config.LocalStandardV1,
		Revision: 1,
		Digest:   "sha256:26a711346a64896a5d56448065fb2ce89284d9f86fa4a991407a4b56ca8a76d4",
	})
	if err != nil {
		actual, _ := registry.RuntimeResource(config.LocalStandardV1)
		t.Fatalf("resolve runtime resource profile: %v (actual digest %s)", err, actual.Digest)
	}
	if resources.CPUUnits != 2 || resources.MemoryBytes != 4<<30 || resources.PIDsLimit != 512 || resources.WritableLayerBytes != 10<<30 {
		t.Fatalf("unexpected resource limits: %+v", resources)
	}

	deepseek, err := registry.ResolveAgent(config.AgentProfileRef{
		ID:       config.DeepSeekCodexOpenAIV1,
		Revision: 1,
		Digest:   "sha256:d4437eefaf16e95d363b1c55fefd4d1fe73d3238b32ab3afa10b85f1acb134fb",
	})
	if err != nil {
		actual, _ := registry.Agent(config.DeepSeekCodexOpenAIV1)
		t.Fatalf("resolve deepseek profile: %v (actual digest %s)", err, actual.Digest)
	}
	if deepseek.LiteLLMModel != "deepseek/deepseek-v4-pro" || deepseek.APIBase != "https://api.deepseek.com" || deepseek.UpstreamProtocol != config.UpstreamProtocolOpenAI {
		t.Fatalf("unexpected deepseek route: %+v", deepseek)
	}

	kimi, err := registry.ResolveAgent(config.AgentProfileRef{
		ID:       config.KimiCodexAnthropicV1,
		Revision: 1,
		Digest:   "sha256:2ba4c6870eb74c52515b54de6ef89dece1955893a4c7b0efb3d632c977e6eaf2",
	})
	if err != nil {
		actual, _ := registry.Agent(config.KimiCodexAnthropicV1)
		t.Fatalf("resolve kimi profile: %v (actual digest %s)", err, actual.Digest)
	}
	if kimi.LiteLLMModel != "anthropic/kimi-for-coding" || kimi.APIBase != "https://api.kimi.com/coding" || kimi.UpstreamProtocol != config.UpstreamProtocolAnthropic {
		t.Fatalf("unexpected kimi route: %+v", kimi)
	}
	if deepseek.AutomaticFallbackProfileID != "" || kimi.AutomaticFallbackProfileID != "" {
		t.Fatal("M1 profiles must not configure an in-turn provider fallback")
	}

	for id, model := range map[string]string{
		config.DeepSeekAssistantOpenAIV1: "switchback-assistant-deepseek",
	} {
		assistant, ok := registry.Assistant(id)
		if !ok {
			t.Fatalf("Assistant model profile %q missing", id)
		}
		resolvedAssistant, err := registry.ResolveAssistant(assistant.Ref())
		if err != nil {
			t.Fatalf("resolve Assistant profile %q: %v", id, err)
		}
		if resolvedAssistant.LiteLLMModel != model ||
			resolvedAssistant.ResponseSchema != "assistant.decision.v1" ||
			resolvedAssistant.AutomaticFallbackProfileID != "" {
			t.Fatalf("unexpected Assistant model route: %+v", resolvedAssistant)
		}
	}
	if _, ok := registry.Assistant(config.KimiAssistantOpenAIV1); ok {
		t.Fatal("old Kimi Assistant profile must not remain selectable")
	}
}

func TestProductionProfileRegistryIncludesKimiCodeAssistantRequestPolicy(t *testing.T) {
	const wantDigest = "sha256:04e2bd0fb1ab379aaaffaf0584da50c1997b6807bde537e606954ef4f0f34b2c"

	profile, ok := config.ProductionProfileRegistry().Assistant(
		config.KimiCodeAssistantOpenAIV1,
	)
	if !ok ||
		profile.Revision != 2 ||
		profile.LiteLLMModel != "switchback-assistant-kimi-code" ||
		profile.DeploymentScope != config.SingleUserLocalProfileName ||
		profile.RequestPolicy == nil ||
		profile.RequestPolicy.Temperature == nil ||
		*profile.RequestPolicy.Temperature != 1 ||
		profile.RequestPolicy.StructuredOutput == nil {
		t.Fatalf("unexpected Kimi Code Assistant profile: %#v", profile)
	}
	structured := profile.RequestPolicy.StructuredOutput
	if structured.Mode != "strict_json_schema" ||
		structured.DecisionSchema != contractsv1.AssistantDecisionSchemaVersion ||
		structured.ProviderSchemaID != contractsv1.AssistantDecisionProviderSchemaID ||
		structured.ProviderSchemaDigest != contractsv1.AssistantDecisionProviderSchemaDigest {
		t.Fatalf("unexpected Kimi Code structured-output policy: %#v", structured)
	}
	if profile.Digest != wantDigest {
		t.Fatalf("Kimi Code Assistant digest = %q, want %q", profile.Digest, wantDigest)
	}
	if err := profile.ValidateDigest(); err != nil {
		t.Fatalf("Kimi Code Assistant profile is not self-verifying: %v", err)
	}

	deepseek, ok := config.ProductionProfileRegistry().Assistant(config.DeepSeekAssistantOpenAIV1)
	if !ok {
		t.Fatal("DeepSeek Assistant profile missing")
	}
	if deepseek.RequestPolicy != nil {
		t.Fatalf("DeepSeek Assistant unexpectedly declares request policy: %#v", deepseek.RequestPolicy)
	}
	if deepseek.Digest != "sha256:00305a3d4da93017a6c43b530e63f381098225bc966b88c80be2873ffa0ba730" {
		t.Fatalf("DeepSeek Assistant digest changed: %s", deepseek.Digest)
	}
}

func TestProfileRegistryAssistantReturnsAnIsolatedRequestPolicy(t *testing.T) {
	registry := config.ProductionProfileRegistry()
	profile, ok := registry.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Kimi Code Assistant profile missing")
	}
	ref := profile.Ref()
	*profile.RequestPolicy.Temperature = 2
	profile.RequestPolicy.StructuredOutput.Mode = "caller_override"
	profile.RequestPolicy = nil

	current, ok := registry.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok || current.RequestPolicy == nil || current.RequestPolicy.Temperature == nil ||
		*current.RequestPolicy.Temperature != 1 ||
		current.RequestPolicy.StructuredOutput == nil ||
		current.RequestPolicy.StructuredOutput.Mode != "strict_json_schema" {
		t.Fatalf("returned Assistant profile mutated registry state: %#v", current)
	}
	if err := current.ValidateDigest(); err != nil {
		t.Fatalf("current Assistant profile digest is invalid: %v", err)
	}

	resolved, err := registry.ResolveAssistant(ref)
	if err != nil {
		t.Fatalf("resolve Assistant profile after caller mutation: %v", err)
	}
	if resolved.RequestPolicy == nil || resolved.RequestPolicy.Temperature == nil ||
		*resolved.RequestPolicy.Temperature != 1 ||
		resolved.RequestPolicy.StructuredOutput == nil ||
		resolved.RequestPolicy.StructuredOutput.Mode != "strict_json_schema" {
		t.Fatalf("resolved Assistant profile mutated registry state: %#v", resolved)
	}
}

func TestAssistantModelProfileRejectsInvalidRequestPoliciesBeforeDigest(t *testing.T) {
	registry := config.ProductionProfileRegistry()
	_, ok := registry.Assistant(config.KimiCodeAssistantOpenAIV1)
	if !ok {
		t.Fatal("Kimi Code Assistant profile missing")
	}

	tests := []struct {
		name      string
		wantError string
		mutate    func(*config.AssistantModelProfile)
	}{
		{name: "NaN", wantError: "temperature", mutate: func(profile *config.AssistantModelProfile) { *profile.RequestPolicy.Temperature = math.NaN() }},
		{name: "positive infinity", wantError: "temperature", mutate: func(profile *config.AssistantModelProfile) { *profile.RequestPolicy.Temperature = math.Inf(1) }},
		{name: "negative temperature", wantError: "temperature", mutate: func(profile *config.AssistantModelProfile) { *profile.RequestPolicy.Temperature = -0.1 }},
		{name: "temperature above two", wantError: "temperature", mutate: func(profile *config.AssistantModelProfile) { *profile.RequestPolicy.Temperature = 2.1 }},
		{name: "wrong structured-output mode", wantError: "structured output mode", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.Mode = "json_object"
		}},
		{name: "wrong decision schema", wantError: "structured output decision schema", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.DecisionSchema = "assistant.decision.v2"
		}},
		{name: "wrong provider schema id", wantError: "provider schema id", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.ProviderSchemaID = "caller-schema"
		}},
		{name: "wrong provider schema digest", wantError: "provider schema digest", mutate: func(profile *config.AssistantModelProfile) {
			profile.RequestPolicy.StructuredOutput.ProviderSchemaDigest = "sha256:caller"
		}},
		{name: "non-empty fallback", wantError: "automatic fallback", mutate: func(profile *config.AssistantModelProfile) { profile.AutomaticFallbackProfileID = "fallback" }},
		{name: "stale digest", wantError: "profile digest mismatch", mutate: func(profile *config.AssistantModelProfile) { profile.Digest = "sha256:stale" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, ok := registry.Assistant(config.KimiCodeAssistantOpenAIV1)
			if !ok {
				t.Fatal("Kimi Code Assistant profile missing")
			}
			tt.mutate(&profile)
			if err := profile.ValidateDigest(); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("unexpected validation result: %v", err)
			}
		})
	}
}

func TestProductionProfileRegistryRejectsTamperedAssistantReference(t *testing.T) {
	registry := config.ProductionProfileRegistry()
	for name, ref := range map[string]config.AssistantModelProfileRef{
		"historical revision one": {
			ID:       config.KimiCodeAssistantOpenAIV1,
			Revision: 1,
			Digest:   "sha256:395f6949fc8ad3464f61e2dd6c90778ef22e190738d00bf13ed32789d0c6456c",
		},
		"tampered digest": {
			ID:       config.KimiCodeAssistantOpenAIV1,
			Revision: 2,
			Digest:   "sha256:caller",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.ResolveAssistant(ref); err == nil {
				t.Fatalf("stale or tampered Assistant profile was accepted: %+v", ref)
			}
		})
	}
}

func TestProductionProfileRegistryRejectsUnknownOrTamperedReferences(t *testing.T) {
	registry := config.ProductionProfileRegistry()

	tests := []struct {
		name string
		ref  config.AgentProfileRef
	}{
		{name: "unknown", ref: config.AgentProfileRef{ID: "caller-profile", Revision: 1, Digest: "sha256:caller"}},
		{name: "stale revision", ref: config.AgentProfileRef{ID: config.DeepSeekCodexOpenAIV1, Revision: 2, Digest: "sha256:d4437eefaf16e95d363b1c55fefd4d1fe73d3238b32ab3afa10b85f1acb134fb"}},
		{name: "wrong digest", ref: config.AgentProfileRef{ID: config.KimiCodexAnthropicV1, Revision: 1, Digest: "sha256:caller"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := registry.ResolveAgent(tt.ref); err == nil {
				t.Fatalf("profile reference %+v was accepted", tt.ref)
			}
		})
	}

	if _, err := registry.ResolveRuntimeResource(config.RuntimeResourceProfileRef{ID: config.LocalStandardV1, Revision: 1, Digest: "sha256:caller"}); err == nil {
		t.Fatal("tampered resource profile digest was accepted")
	}
}

func TestProfileDefinitionsAreSelfVerifyingAndContainNoCredentialMaterial(t *testing.T) {
	registry := config.ProductionProfileRegistry()
	profile, ok := registry.Agent(config.DeepSeekCodexOpenAIV1)
	if !ok {
		t.Fatal("deepseek profile missing")
	}

	profile.APIBase = "https://caller.example.invalid"
	if err := profile.ValidateDigest(); err == nil {
		t.Fatal("mutated server-owned profile retained a valid digest")
	}

	for _, id := range []string{config.DeepSeekCodexOpenAIV1, config.KimiCodexAnthropicV1} {
		profile, ok := registry.Agent(id)
		if !ok {
			t.Fatalf("agent profile %q missing", id)
		}
		serialized := strings.ToLower(profile.ID + profile.APIBase + profile.LiteLLMModel + profile.CredentialRef)
		for _, forbidden := range []string{"api_key", "authorization", "bearer ", "sk-"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("profile %q contains credential material marker %q", id, forbidden)
			}
		}
	}
}
