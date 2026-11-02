package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"switchback.local/spikes/worker-runtime/backend/internal/config"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
)

func TestNewApplicationUsesConcreteDockerHostPathVerifier(t *testing.T) {
	cfg, request := newApplicationTestFixture(t)
	engine := &applicationDockerEngine{}

	application, err := newApplication(context.Background(), cfg, engine, func(string) ([]byte, error) {
		return []byte("upstream-model-key\n"), nil
	})
	if err != nil {
		t.Fatalf("newApplication() error = %v", err)
	}
	if _, ok := application.provisioner.(*runtime.DockerProvisioner); !ok {
		t.Fatalf("provisioner = %T, want *runtime.DockerProvisioner", application.provisioner)
	}
	if application.projector == nil {
		t.Fatal("Langfuse projector = nil")
	}

	if _, err := application.provisioner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	wantCalls := []string{
		"inspect:switchback-runtime-runtime-main-test",
		"create:hostpath-attestation",
		"start:attestor-1",
		"wait:attestor-1",
		"remove:attestor-1",
		"inspect:switchback-runtime-secret-populator-runtime-main-test-g1",
		"volume-inspect:switchback-runtime-secrets-runtime-main-test-g1",
		"volume-create:switchback-runtime-secrets-runtime-main-test-g1",
		"create:runtime-secret-populator",
		"start:populator-1",
		"wait:populator-1",
		"remove:populator-1",
		"inspect:switchback-runtime-secret-populator-runtime-main-test-g1",
		"create:hostpath-attestation",
		"start:attestor-2",
		"wait:attestor-2",
		"remove:attestor-2",
		"create:worker-runtime",
		"start:worker-1",
	}
	if !reflect.DeepEqual(engine.calls, wantCalls) {
		t.Fatalf("Engine calls = %#v, want concrete attestor cleanup before Worker create %#v", engine.calls, wantCalls)
	}
	if got := len(engine.creates); got != 4 {
		t.Fatalf("ContainerCreate calls = %d, want two attestors, populator and Worker", got)
	}
	attestor := engine.creates[0]
	if attestor.HostConfig == nil ||
		attestor.HostConfig.NetworkMode != container.NetworkMode("none") ||
		len(attestor.HostConfig.Mounts) != 1 ||
		attestor.HostConfig.Mounts[0].Source != cfg.HostDataRoot ||
		!attestor.HostConfig.Mounts[0].ReadOnly {
		t.Fatalf("attestor create = %#v, want one read-only HostDataRoot mount on network none", attestor)
	}
	worker := engine.creates[3]
	if got := len(worker.HostConfig.Mounts); got != 4 {
		t.Fatalf("Worker mounts = %d, want three host binds plus one secret volume", got)
	}
	for _, mounted := range worker.HostConfig.Mounts {
		if mounted.Source == cfg.HostDataRoot {
			t.Fatalf("Worker received forbidden HostDataRoot mount: %#v", mounted)
		}
	}
}

func TestNewApplicationStopsBeforeWorkerCreateWhenConcreteProbeRejects(t *testing.T) {
	cfg, request := newApplicationTestFixture(t)
	engine := &applicationDockerEngine{attestorStatusCode: 23}
	application, err := newApplication(context.Background(), cfg, engine, func(string) ([]byte, error) {
		return []byte("upstream-model-key\n"), nil
	})
	if err != nil {
		t.Fatalf("newApplication() error = %v", err)
	}

	if _, err := application.provisioner.Start(context.Background(), request); err == nil {
		t.Fatal("Start() error = nil, want concrete HostPath rejection")
	}
	wantCalls := []string{
		"inspect:switchback-runtime-runtime-main-test",
		"create:hostpath-attestation",
		"start:attestor-1",
		"wait:attestor-1",
		"remove:attestor-1",
	}
	if !reflect.DeepEqual(engine.calls, wantCalls) {
		t.Fatalf("Engine calls = %#v, want rejected attestor cleanup without Worker create %#v", engine.calls, wantCalls)
	}
	for _, create := range engine.creates {
		if create.Config != nil && create.Config.Labels[runtime.LabelManaged] == runtime.ManagedWorkerRuntime {
			t.Fatalf("Worker create occurred after rejected concrete probe: %#v", create)
		}
	}
}

type applicationDockerEngine struct {
	calls              []string
	creates            []client.ContainerCreateOptions
	attestorStatusCode int64
	attestorCount      int
}

func (e *applicationDockerEngine) ContainerCreate(
	_ context.Context,
	options client.ContainerCreateOptions,
) (client.ContainerCreateResult, error) {
	managed := ""
	if options.Config != nil {
		managed = options.Config.Labels[runtime.LabelManaged]
	}
	e.calls = append(e.calls, "create:"+managed)
	e.creates = append(e.creates, options)
	switch managed {
	case "hostpath-attestation":
		e.attestorCount++
		return client.ContainerCreateResult{ID: fmt.Sprintf("attestor-%d", e.attestorCount)}, nil
	case runtime.ManagedSecretPopulator:
		return client.ContainerCreateResult{ID: "populator-1"}, nil
	case runtime.ManagedWorkerRuntime:
		return client.ContainerCreateResult{ID: "worker-1"}, nil
	default:
		return client.ContainerCreateResult{}, nil
	}
}

func (e *applicationDockerEngine) ContainerStart(
	_ context.Context,
	containerID string,
	_ client.ContainerStartOptions,
) (client.ContainerStartResult, error) {
	e.calls = append(e.calls, "start:"+containerID)
	return client.ContainerStartResult{}, nil
}

func (e *applicationDockerEngine) ContainerInspect(
	_ context.Context,
	containerID string,
	_ client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	e.calls = append(e.calls, "inspect:"+containerID)
	return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
}

func (e *applicationDockerEngine) ContainerWait(
	_ context.Context,
	containerID string,
	_ client.ContainerWaitOptions,
) client.ContainerWaitResult {
	e.calls = append(e.calls, "wait:"+containerID)
	result := make(chan container.WaitResponse, 1)
	waitErr := make(chan error, 1)
	result <- container.WaitResponse{StatusCode: e.attestorStatusCode}
	return client.ContainerWaitResult{Result: result, Error: waitErr}
}

func (e *applicationDockerEngine) ContainerRemove(
	_ context.Context,
	containerID string,
	_ client.ContainerRemoveOptions,
) (client.ContainerRemoveResult, error) {
	e.calls = append(e.calls, "remove:"+containerID)
	return client.ContainerRemoveResult{}, nil
}

func (e *applicationDockerEngine) VolumeCreate(
	_ context.Context,
	options client.VolumeCreateOptions,
) (client.VolumeCreateResult, error) {
	e.calls = append(e.calls, "volume-create:"+options.Name)
	return client.VolumeCreateResult{Volume: volume.Volume{
		Name:   options.Name,
		Driver: "local",
		Labels: options.Labels,
		Scope:  "local",
	}}, nil
}

func (e *applicationDockerEngine) VolumeInspect(
	_ context.Context,
	name string,
	_ client.VolumeInspectOptions,
) (client.VolumeInspectResult, error) {
	e.calls = append(e.calls, "volume-inspect:"+name)
	return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
}

func (e *applicationDockerEngine) VolumeRemove(
	_ context.Context,
	name string,
	_ client.VolumeRemoveOptions,
) (client.VolumeRemoveResult, error) {
	e.calls = append(e.calls, "volume-remove:"+name)
	return client.VolumeRemoveResult{}, nil
}

func newApplicationTestFixture(t *testing.T) (config.Config, runtime.ProvisionRequest) {
	t.Helper()
	root := t.TempDir()
	containerRoot := filepath.Join(root, "backend-data")
	hostRoot := filepath.Join(root, "host-data")
	for _, path := range []string{containerRoot, hostRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		ListenAddr:         "0.0.0.0:8080",
		GRPCListenAddr:     "0.0.0.0:9090",
		ContainerDataRoot:  containerRoot,
		HostDataRoot:       hostRoot,
		ObjectStoreRoot:    filepath.Join(containerRoot, "objects"),
		FixturePath:        filepath.Join(root, "fixture"),
		WorkerImage:        "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		WorkerNetwork:      "switchback-spike-control",
		BackendGRPCTarget:  "spike-backend:9090",
		BackendHTTPBaseURL: "http://spike-backend:8080",
		ModelKeyFile:       filepath.Join(containerRoot, "env", "upstream-model.key"),
		HostModelKeyFile:   filepath.Join(hostRoot, "env", "upstream-model.key"),
		UpstreamModel:      "openai/codex",
		UpstreamAPIBase:    "https://models.example.invalid/v1",
		AgentProfile:       agentprofile.Codex,
		LangfuseBaseURL:    "http://langfuse-web:3000",
		LangfusePublicKey:  "lf_pk_test",
		LangfuseSecretKey:  "lf_sk_test",
	}
	mapper := runtime.PathMapper{
		ContainerDataRoot: containerRoot,
		HostDataRoot:      hostRoot,
	}
	pair := func(name string, directory bool) runtime.PathPair {
		t.Helper()
		path, err := mapper.SessionPath("session-main-test", name)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{path.ContainerPath, path.HostPath} {
			if directory {
				err = os.MkdirAll(value, 0o700)
			} else {
				err = os.MkdirAll(filepath.Dir(value), 0o700)
				if err == nil {
					err = os.WriteFile(value, []byte("server-owned"), 0o600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	return cfg, runtime.ProvisionRequest{
		Identity: runtime.RuntimeIdentity{
			WorkerSessionID:   "session-main-test",
			WorkerRunID:       "run-main-test",
			RuntimeInstanceID: "runtime-main-test",
			RuntimeGeneration: 1,
		},
		WorkerToken: "worker-token",
		WorkingCopy: pair("working-copy", true),
		AgentState:  pair("agent-state", true),
		TraceSpool:  pair("trace-spool", true),
		Bootstrap:   pair("bootstrap.json", false),
		ModelSecret: pair("model-secret", false),
	}
}

var _ runtime.DockerEngine = (*applicationDockerEngine)(nil)
