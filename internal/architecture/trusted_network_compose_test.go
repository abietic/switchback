package architecture_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
)

func TestTrustedNetworkComposeKeepsOneNamedEgressBridgeAndInternalInfrastructure(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "local", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	compose := string(data)
	const workerNetworkBinding = "${SWITCHBACK_WORKER_NETWORK:-switchback-m1-worker-control}"
	if count := strings.Count(compose, workerNetworkBinding); count != 2 {
		t.Fatalf("server-owned Worker network binding occurs %d times, want exactly Backend and network name", count)
	}
	if !strings.Contains(compose, "SWITCHBACK_WORKER_NETWORK: "+workerNetworkBinding) {
		t.Fatal("Backend Worker network must use the server-owned Compose network binding")
	}
	networksIndex := strings.LastIndex(compose, "\nnetworks:\n")
	if networksIndex < 0 {
		t.Fatal("Compose networks section is missing")
	}
	networks := compose[networksIndex:]
	workerControl := composeNetworkBlock(t, networks, "worker-control")
	if !strings.Contains(workerControl, "name: "+workerNetworkBinding) ||
		strings.Contains(workerControl, "internal: true") {
		t.Fatalf("worker-control must be one stable, non-internal named bridge:\n%s", workerControl)
	}
	for _, name := range []string{"switchback-data", "observability", "assistant-model", "langfuse-data"} {
		block := composeNetworkBlock(t, networks, name)
		if !strings.Contains(block, "internal: true") {
			t.Fatalf("%s must remain internal:\n%s", name, block)
		}
	}
	if !strings.Contains(compose, "networks: [worker-control, switchback-data, observability, assistant-model, local-access]") {
		t.Fatal("Backend must remain the only Compose service attached to the Worker egress bridge")
	}
}

func TestTrustedNetworkComposeRendersDefaultAndExplicitIsolatedWorkerNetwork(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{name: "default remains the M1 network", want: "switchback-m1-worker-control"},
		{
			name:     "explicit isolated network owns both consumers",
			override: "switchback-task24-isolated-worker-control",
			want:     "switchback-task24-isolated-worker-control",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered := renderLocalCompose(t, test.override)
			backend, ok := rendered.Services["switchback-backend"]
			if !ok {
				t.Fatal("rendered Compose lacks switchback-backend")
			}
			if actual := backend.Environment["SWITCHBACK_WORKER_NETWORK"]; actual != test.want {
				t.Fatalf("Backend Worker network = %q, want %q", actual, test.want)
			}
			network, ok := rendered.Networks["worker-control"]
			if !ok {
				t.Fatal("rendered Compose lacks worker-control network")
			}
			if network.Name != test.want {
				t.Fatalf("worker-control network name = %q, want %q", network.Name, test.want)
			}
		})
	}
}

func TestWorkerDockerNetworkCannotBeSelectedByClientAPIOrTaskSpec(t *testing.T) {
	selectors := []string{"worker_network", "docker_network", "network_name"}
	for _, selector := range selectors {
		t.Run("direct API rejects "+selector, func(t *testing.T) {
			input := validDirectTaskInput()
			input[selector] = "caller-selected-network"
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gatewayhttp.DecodeDirectTaskInput(data); err == nil {
				t.Fatalf("direct Task API accepted client network selector %q", selector)
			}
		})
		t.Run("TaskSpec rejects "+selector, func(t *testing.T) {
			path := filepath.Join("..", "contracts", "v1", "testdata", "task-spec.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err := json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			runtimeConfig, ok := document["runtime_config"].(map[string]any)
			if !ok {
				t.Fatal("TaskSpec fixture lacks runtime_config object")
			}
			runtimeConfig[selector] = "caller-selected-network"
			data, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := contractsv1.DecodeTaskSpec(data); err == nil {
				t.Fatalf("TaskSpec accepted client network selector %q", selector)
			}
		})
	}
}

type renderedLocalCompose struct {
	Services map[string]struct {
		Environment map[string]string `json:"environment"`
	} `json:"services"`
	Networks map[string]struct {
		Name string `json:"name"`
	} `json:"networks"`
}

func renderLocalCompose(t *testing.T, workerNetwork string) renderedLocalCompose {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "compose.env")
	dataRoot := filepath.Join(t.TempDir(), "data")
	keyRoot := t.TempDir()
	env := strings.Join([]string{
		"SWITCHBACK_HOST_DATA_ROOT=" + dataRoot,
		"SWITCHBACK_DOCKER_SOCKET=/var/run/docker.sock",
		"SWITCHBACK_WORKER_IMAGE=switchback-worker:test",
		"SWITCHBACK_POSTGRES_PASSWORD=test-postgres",
		"SWITCHBACK_GRAFANA_PASSWORD=test-grafana",
		"SWITCHBACK_ASSISTANT_LITELLM_PROXY_KEY=test-litellm",
		"SWITCHBACK_KIMI_KEY_FILE=" + filepath.Join(keyRoot, "kimi.key"),
		"SWITCHBACK_DEEPSEEK_KEY_FILE=" + filepath.Join(keyRoot, "deepseek.key"),
		"LANGFUSE_SALT=test-salt",
		"LANGFUSE_ENCRYPTION_KEY=test-encryption-key",
		"LANGFUSE_NEXTAUTH_SECRET=test-nextauth",
		"LANGFUSE_CLICKHOUSE_PASSWORD=test-clickhouse",
		"LANGFUSE_MINIO_PASSWORD=test-minio",
		"LANGFUSE_REDIS_PASSWORD=test-redis",
		"LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-test",
		"LANGFUSE_INIT_PROJECT_SECRET_KEY=sk-test",
		"LANGFUSE_INIT_USER_PASSWORD=test-user",
		"SWITCHBACK_WEB_UI_PORT=24173",
		"SWITCHBACK_BACKEND_PORT=28080",
		"SWITCHBACK_LANGFUSE_PORT=23000",
		"SWITCHBACK_LANGFUSE_MINIO_PORT=29090",
	}, "\n") + "\n"
	if err := os.WriteFile(envFile, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	composePath, err := filepath.Abs(filepath.Join("..", "..", "deploy", "local", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		"docker", "compose",
		"--env-file", envFile,
		"-p", "switchback-task24-render",
		"-f", composePath,
		"config", "--format", "json",
	)
	command.Env = withoutEnvironmentVariable(os.Environ(), "SWITCHBACK_WORKER_NETWORK")
	if workerNetwork != "" {
		command.Env = append(command.Env, "SWITCHBACK_WORKER_NETWORK="+workerNetwork)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("render local Compose: %v\n%s", err, output)
	}
	var rendered renderedLocalCompose
	if err := json.Unmarshal(output, &rendered); err != nil {
		t.Fatalf("decode rendered Compose: %v", err)
	}
	return rendered
}

func withoutEnvironmentVariable(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func validDirectTaskInput() map[string]any {
	return map[string]any{
		"schema_version":        "gateway.direct_task_input.v1",
		"idempotency_key":       "task24-create",
		"project_id":            "project-local",
		"workspace_id":          "workspace-local",
		"workspace_revision_id": "workspace-revision-local-v1",
		"title":                 "Verify isolated Worker network",
		"goal":                  "Prove the Worker Docker network remains server-owned.",
		"target": map[string]any{
			"kind": "workspace",
			"ref":  "workspace-revision-local-v1",
		},
		"constraints":         []string{},
		"acceptance_criteria": []string{"Render the exact server-owned Worker network."},
		"agent_profile_id":    "codex-default",
		"acceptance_mode":     "manual",
		"user_visible_types":  []string{"report"},
	}
}

func composeNetworkBlock(t *testing.T, networks, name string) string {
	t.Helper()
	lines := strings.Split(networks, "\n")
	start := -1
	for index, line := range lines {
		if line == "  "+name+":" {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("Compose network %q is missing", name)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && line != "" {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}
