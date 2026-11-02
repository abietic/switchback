package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	DefaultAssistantReadinessPolicy = "assistant-readiness-v1"
	DefaultAssistantTriggerPolicy   = "assistant-trigger-v1"
)

type AssistantSettings struct {
	Enabled                 bool
	ModelProfile            AssistantModelProfileRef
	ReadinessPolicyRevision string
	TriggerPolicyRevision   string
	ContextBudgetTokens     int
	RequestTimeout          time.Duration
	MaxRecentTurns          int
}

func LoadAssistantSettings(lookup LookupEnv, registry ProfileRegistry) (AssistantSettings, error) {
	if lookup == nil {
		return AssistantSettings{}, fmt.Errorf("environment lookup is required")
	}
	enabled, err := serverBool(lookup, "SWITCHBACK_M2_ASSISTANT_ENABLED", false)
	if err != nil {
		return AssistantSettings{}, err
	}
	profileID := serverValue(lookup, "SWITCHBACK_ASSISTANT_MODEL_PROFILE", KimiCodeAssistantOpenAIV1)
	profile, ok := registry.Assistant(profileID)
	if !ok {
		return AssistantSettings{}, fmt.Errorf("Assistant model profile %q is unsupported", profileID)
	}
	budget, err := serverPositiveInt(lookup, "SWITCHBACK_ASSISTANT_CONTEXT_BUDGET_TOKENS", 8192)
	if err != nil {
		return AssistantSettings{}, err
	}
	maxTurns, err := serverPositiveInt(lookup, "SWITCHBACK_ASSISTANT_MAX_RECENT_TURNS", 24)
	if err != nil {
		return AssistantSettings{}, err
	}
	timeout, err := serverDuration(lookup, "SWITCHBACK_ASSISTANT_REQUEST_TIMEOUT", 90*time.Second)
	if err != nil {
		return AssistantSettings{}, err
	}
	settings := AssistantSettings{
		Enabled: enabled, ModelProfile: profile.Ref(),
		ReadinessPolicyRevision: serverValue(lookup, "SWITCHBACK_ASSISTANT_READINESS_POLICY_REVISION", DefaultAssistantReadinessPolicy),
		TriggerPolicyRevision:   serverValue(lookup, "SWITCHBACK_ASSISTANT_TRIGGER_POLICY_REVISION", DefaultAssistantTriggerPolicy),
		ContextBudgetTokens:     budget, RequestTimeout: timeout, MaxRecentTurns: maxTurns,
	}
	if err := validateIdentity("Assistant readiness policy revision", settings.ReadinessPolicyRevision); err != nil {
		return AssistantSettings{}, err
	}
	if err := validateIdentity("Assistant trigger policy revision", settings.TriggerPolicyRevision); err != nil {
		return AssistantSettings{}, err
	}
	return settings, nil
}

func serverPositiveInt(lookup LookupEnv, name string, fallback int) (int, error) {
	value, ok := lookup(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be one positive integer", name)
	}
	return parsed, nil
}

func serverDuration(lookup LookupEnv, name string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 || parsed > 10*time.Minute {
		return 0, fmt.Errorf("%s must be a positive duration no greater than 10m", name)
	}
	return parsed, nil
}
