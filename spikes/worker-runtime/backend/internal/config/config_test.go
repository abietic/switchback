package config

import (
	"path/filepath"
	"testing"

	"switchback.local/spikes/worker-runtime/internal/agentprofile"
)

func TestLoadUsesLoopbackAndLocalStorageByDefault(t *testing.T) {
	setDeploymentEnvironment(t)
	t.Setenv("SWITCHBACK_SPIKE_DATA_ROOT", "")
	t.Setenv("SWITCHBACK_SPIKE_HOST_DATA_ROOT", "")
	t.Setenv("SWITCHBACK_SPIKE_MODEL_KEY_FILE", "")
	t.Setenv("SWITCHBACK_SPIKE_LISTEN_ADDR", "")
	t.Setenv("SWITCHBACK_SPIKE_GRPC_LISTEN_ADDR", "")
	t.Setenv("SWITCHBACK_SPIKE_FIXTURE_PATH", "")
	t.Setenv("SWITCHBACK_SPIKE_WORKER_NETWORK", "")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("ListenAddr = %q, want loopback default", got.ListenAddr)
	}
	if !filepath.IsAbs(got.ContainerDataRoot) || !filepath.IsAbs(got.HostDataRoot) || !filepath.IsAbs(got.ObjectStoreRoot) || !filepath.IsAbs(got.FixturePath) {
		t.Fatalf("Load() returned non-absolute local paths: %+v", got)
	}
	if got.WorkerImage != "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("WorkerImage = %q, want configured immutable server-owned image", got.WorkerImage)
	}
	if got.WorkerNetwork != "none" {
		t.Fatalf("WorkerNetwork = %q, want fail-closed local default", got.WorkerNetwork)
	}
	if got.AgentProfile != agentprofile.Codex {
		t.Fatalf("AgentProfile = %q, want Codex default", got.AgentProfile)
	}
}

func TestLoadAgentProfileRequiresServerOwnedIntegrationGate(t *testing.T) {
	setDeploymentEnvironment(t)
	t.Setenv("SWITCHBACK_SPIKE_AGENT_PROFILE", "integration-fake")
	t.Setenv("SWITCHBACK_SPIKE_INTEGRATION", "")
	if got, err := Load(); err == nil {
		t.Fatalf("Load() = %+v, want integration profile rejection without gate", got)
	}

	t.Setenv("SWITCHBACK_SPIKE_INTEGRATION", "1")
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.AgentProfile != agentprofile.IntegrationFake {
		t.Fatalf("AgentProfile = %q, want integration-fake", got.AgentProfile)
	}
}

func TestLoadRejectsInvalidExplicitAgentProfile(t *testing.T) {
	setDeploymentEnvironment(t)
	t.Setenv("SWITCHBACK_SPIKE_INTEGRATION", "1")
	for _, value := range []string{"", " ", " codex", "codex ", "unknown", "/opt/custom-agent"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SWITCHBACK_SPIKE_AGENT_PROFILE", value)
			if got, err := Load(); err == nil {
				t.Fatalf("Load() = %+v, want explicit profile rejection", got)
			}
		})
	}
}

func TestLoadReadsServerOwnedWorkerImageAndNetwork(t *testing.T) {
	setDeploymentEnvironment(t)
	t.Setenv("SWITCHBACK_SPIKE_WORKER_IMAGE", "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	t.Setenv("SWITCHBACK_SPIKE_WORKER_NETWORK", "switchback-worker-net")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.WorkerImage != "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("WorkerImage = %q", got.WorkerImage)
	}
	if got.WorkerNetwork != "switchback-worker-net" {
		t.Fatalf("WorkerNetwork = %q", got.WorkerNetwork)
	}
}

func TestLoadAcceptsTheNoneWorkerNetwork(t *testing.T) {
	setDeploymentEnvironment(t)
	t.Setenv("SWITCHBACK_SPIKE_WORKER_NETWORK", "none")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.WorkerNetwork != "none" {
		t.Fatalf("WorkerNetwork = %q, want none", got.WorkerNetwork)
	}
}

func TestLoadRejectsInvalidResolvedWorkerImageAndNetwork(t *testing.T) {
	setDeploymentEnvironment(t)
	tests := []struct {
		name    string
		image   string
		network string
	}{
		{name: "whitespace image", image: " \t ", network: "none"},
		{name: "image with embedded whitespace", image: "switchback worker:latest", network: "none"},
		{name: "malformed image digest", image: "switchback-worker@@sha256:not-a-digest", network: "none"},
		{name: "tag plus digest image", image: "switchback-worker:latest@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "none"},
		{name: "uppercase image repository", image: "example.com/SWITCHBACK/worker:latest", network: "none"},
		{name: "whitespace network", image: "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "  "},
		{name: "host network", image: "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "host"},
		{name: "container network", image: "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "container:foreign"},
		{name: "network with slash", image: "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "switchback/worker"},
		{name: "network with colon", image: "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "switchback:worker"},
		{name: "network with invalid prefix", image: "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111", network: "-switchback-worker"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SWITCHBACK_SPIKE_WORKER_IMAGE", test.image)
			t.Setenv("SWITCHBACK_SPIKE_WORKER_NETWORK", test.network)
			if got, err := Load(); err == nil {
				t.Fatalf("Load() = %+v, want fail-closed runtime config rejection", got)
			}
		})
	}
}

func TestLoadRejectsRelativeConfiguredPaths(t *testing.T) {
	setDeploymentEnvironment(t)
	t.Setenv("SWITCHBACK_SPIKE_DATA_ROOT", "relative-data")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want relative data root rejection")
	}
}

func TestLoadAcceptsOnlyIPLiteralLoopbackListenAddresses(t *testing.T) {
	setDeploymentEnvironment(t)
	tests := []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "IPv4 localhost", address: "127.0.0.1:8080"},
		{name: "IPv4 loopback range", address: "127.42.0.9:9090"},
		{name: "IPv6 loopback", address: "[::1]:8080"},
		{name: "empty host", address: ":8080", wantErr: true},
		{name: "IPv4 unspecified", address: "0.0.0.0:8080"},
		{name: "IPv6 unspecified", address: "[::]:8080"},
		{name: "public IP", address: "203.0.113.8:8080", wantErr: true},
		{name: "private non-loopback IP", address: "192.168.1.8:8080", wantErr: true},
		{name: "hostname", address: "localhost:8080", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SWITCHBACK_SPIKE_LISTEN_ADDR", tt.address)
			got, err := Load()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() = %+v, want listen address rejection", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got.ListenAddr != tt.address {
				t.Fatalf("ListenAddr = %q, want %q", got.ListenAddr, tt.address)
			}
		})
	}
}

func TestLoadAcceptsPinnedComposeDeploymentWiring(t *testing.T) {
	t.Setenv("SWITCHBACK_SPIKE_DATA_ROOT", "/var/lib/switchback-spike")
	t.Setenv("SWITCHBACK_SPIKE_HOST_DATA_ROOT", "/Users/example/.switchback-spike")
	t.Setenv("SWITCHBACK_SPIKE_FIXTURE_PATH", "/opt/switchback/fixture")
	t.Setenv("SWITCHBACK_SPIKE_LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv("SWITCHBACK_SPIKE_GRPC_LISTEN_ADDR", "0.0.0.0:9090")
	t.Setenv("SWITCHBACK_SPIKE_WORKER_IMAGE", "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	t.Setenv("SWITCHBACK_SPIKE_WORKER_NETWORK", "switchback-spike-control")
	t.Setenv("SWITCHBACK_SPIKE_BACKEND_GRPC_TARGET", "spike-backend:9090")
	t.Setenv("SWITCHBACK_SPIKE_BACKEND_HTTP_BASE_URL", "http://spike-backend:8080")
	t.Setenv("SWITCHBACK_SPIKE_MODEL_KEY_FILE", "/var/lib/switchback-spike/env/upstream-model.key")
	t.Setenv("SWITCHBACK_SPIKE_UPSTREAM_MODEL", "openai/codex")
	t.Setenv("SWITCHBACK_SPIKE_UPSTREAM_API_BASE", "https://models.example.invalid/v1")
	t.Setenv("LANGFUSE_BASE_URL", "http://langfuse-web:3000")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "lf_pk_test")
	t.Setenv("LANGFUSE_SECRET_KEY", "lf_sk_test")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.ContainerDataRoot != "/var/lib/switchback-spike" || got.HostDataRoot != "/Users/example/.switchback-spike" {
		t.Fatalf("data roots = %+v, want D-o-D container/host roots", got)
	}
	if got.HostModelKeyFile != "/Users/example/.switchback-spike/env/upstream-model.key" {
		t.Fatalf("HostModelKeyFile = %q", got.HostModelKeyFile)
	}
	if got.BackendGRPCTarget != "spike-backend:9090" || got.BackendHTTPBaseURL != "http://spike-backend:8080" {
		t.Fatalf("Worker endpoint wiring = %+v", got)
	}
}

func TestLoadRejectsDeploymentSecretsAndWorkerEndpointsOutsideTheTrustedShape(t *testing.T) {
	setDeploymentEnvironment(t)
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing Langfuse secret", key: "LANGFUSE_SECRET_KEY", value: ""},
		{name: "Langfuse path", key: "LANGFUSE_BASE_URL", value: "http://langfuse-web:3000/private"},
		{name: "Worker gRPC URL", key: "SWITCHBACK_SPIKE_BACKEND_GRPC_TARGET", value: "http://spike-backend:9090"},
		{name: "Worker upload userinfo", key: "SWITCHBACK_SPIKE_BACKEND_HTTP_BASE_URL", value: "http://token@spike-backend:8080"},
		{name: "model key outside container root", key: "SWITCHBACK_SPIKE_MODEL_KEY_FILE", value: "/tmp/model-key"},
		{name: "tag-only worker image", key: "SWITCHBACK_SPIKE_WORKER_IMAGE", value: "switchback-spike-worker:latest"},
		{name: "tag plus digest worker image", key: "SWITCHBACK_SPIKE_WORKER_IMAGE", value: "switchback-spike-worker:latest@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load() error = nil, want deployment configuration rejection")
			}
		})
	}
}

func setDeploymentEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("SWITCHBACK_SPIKE_DATA_ROOT", "/var/lib/switchback-spike")
	t.Setenv("SWITCHBACK_SPIKE_HOST_DATA_ROOT", "/host/switchback-spike")
	t.Setenv("SWITCHBACK_SPIKE_FIXTURE_PATH", "/opt/switchback/fixture")
	t.Setenv("SWITCHBACK_SPIKE_LISTEN_ADDR", "0.0.0.0:8080")
	t.Setenv("SWITCHBACK_SPIKE_GRPC_LISTEN_ADDR", "0.0.0.0:9090")
	t.Setenv("SWITCHBACK_SPIKE_WORKER_IMAGE", "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	t.Setenv("SWITCHBACK_SPIKE_WORKER_NETWORK", "switchback-spike-control")
	t.Setenv("SWITCHBACK_SPIKE_BACKEND_GRPC_TARGET", "spike-backend:9090")
	t.Setenv("SWITCHBACK_SPIKE_BACKEND_HTTP_BASE_URL", "http://spike-backend:8080")
	t.Setenv("SWITCHBACK_SPIKE_MODEL_KEY_FILE", "/var/lib/switchback-spike/env/upstream-model.key")
	t.Setenv("SWITCHBACK_SPIKE_UPSTREAM_MODEL", "openai/codex")
	t.Setenv("SWITCHBACK_SPIKE_UPSTREAM_API_BASE", "https://models.example.invalid/v1")
	t.Setenv("LANGFUSE_BASE_URL", "http://langfuse-web:3000")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "lf_pk_test")
	t.Setenv("LANGFUSE_SECRET_KEY", "lf_sk_test")
}
