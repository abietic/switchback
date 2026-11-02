package gatewayhttp_test

import (
	"encoding/json"
	"testing"

	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
)

func TestDirectTaskInputAcceptsOnlyExecutionIntent(t *testing.T) {
	data := directTaskInput(t, nil)

	input, err := gatewayhttp.DecodeDirectTaskInput(data)
	if err != nil {
		t.Fatalf("decode direct Task input: %v", err)
	}
	if input.WorkspaceID != "workspace-demo" || input.AgentProfileID != "deepseek-codex-openai-v1" {
		t.Fatalf("decoded wrong input: %+v", input)
	}
}

func TestDirectTaskInputRejectsInfrastructureAndIdentityOverrides(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "tenant", field: "tenant_id", value: "tenant-attacker"},
		{name: "user", field: "user_id", value: "user-attacker"},
		{name: "Docker options", field: "docker", value: map[string]any{"privileged": true}},
		{name: "host path", field: "host_path", value: "/Users/example"},
		{name: "resource numbers", field: "resource_limits", value: map[string]any{"cpu": 8, "memory_mb": 32768}},
		{name: "provider credential", field: "provider_api_key", value: "known-secret"},
		{name: "provider fallback", field: "provider_fallback", value: []string{"kimi-codex-anthropic-v1"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := gatewayhttp.DecodeDirectTaskInput(directTaskInput(t, map[string]any{test.field: test.value})); err == nil {
				t.Fatalf("client override %q was accepted", test.field)
			}
		})
	}
}

func directTaskInput(t *testing.T, override map[string]any) []byte {
	t.Helper()
	input := map[string]any{
		"schema_version":        "gateway.direct_task_input.v1",
		"idempotency_key":       "create-task-1",
		"project_id":            "project-switchback",
		"workspace_id":          "workspace-demo",
		"workspace_revision_id": "workspace-revision-1",
		"title":                 "Repair counter",
		"goal":                  "Repair the counter and prove it with tests.",
		"target":                map[string]any{"kind": "workspace", "ref": "workspace-revision-1"},
		"constraints":           []string{"Do not publish remote changes."},
		"acceptance_criteria":   []string{"All repository tests pass."},
		"agent_profile_id":      "deepseek-codex-openai-v1",
		"acceptance_mode":       "manual",
		"user_visible_types":    []string{"report", "patch"},
	}
	for key, value := range override {
		input[key] = value
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal direct Task input: %v", err)
	}
	return data
}
