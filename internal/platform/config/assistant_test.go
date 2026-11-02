package config_test

import (
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/config"
)

func TestAssistantSettingsAreServerOwnedAndHaveSafeDefaults(t *testing.T) {
	settings, err := config.LoadAssistantSettings(
		func(string) (string, bool) { return "", false },
		config.ProductionProfileRegistry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.ModelProfile.ID != config.KimiCodeAssistantOpenAIV1 ||
		settings.ReadinessPolicyRevision != config.DefaultAssistantReadinessPolicy ||
		settings.TriggerPolicyRevision != config.DefaultAssistantTriggerPolicy ||
		settings.ContextBudgetTokens != 8192 || settings.RequestTimeout != 90*time.Second ||
		settings.MaxRecentTurns != 24 {
		t.Fatalf("unexpected Assistant defaults: %+v", settings)
	}
}

func TestAssistantSettingsRejectInvalidServerConfiguration(t *testing.T) {
	tests := []map[string]string{
		{"SWITCHBACK_M2_ASSISTANT_ENABLED": "true"},
		{"SWITCHBACK_ASSISTANT_MODEL_PROFILE": "caller-model"},
		{"SWITCHBACK_ASSISTANT_CONTEXT_BUDGET_TOKENS": "0"},
		{"SWITCHBACK_ASSISTANT_MAX_RECENT_TURNS": "-1"},
		{"SWITCHBACK_ASSISTANT_REQUEST_TIMEOUT": "11m"},
		{"SWITCHBACK_ASSISTANT_READINESS_POLICY_REVISION": "bad policy"},
	}
	for _, values := range tests {
		_, err := config.LoadAssistantSettings(func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		}, config.ProductionProfileRegistry())
		if err == nil {
			t.Fatalf("invalid Assistant settings were accepted: %+v", values)
		}
	}
}
