package docker_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"github.com/abietic/switchback/internal/platform/config"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestProvisionCreatesOneFixedIsolatedWorkerContainer(t *testing.T) {
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	verifier := &recordingVerifier{}
	provisioner := newProvisioner(t, engine, verifier)
	request := validRequest(t)

	got, err := provisioner.Provision(context.Background(), request)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got.ContainerID != "container-2" || got.RuntimeInstanceID != "runtime-1" || got.Generation != 1 {
		t.Fatalf("provisioned runtime = %+v", got)
	}
	if len(engine.creates) != 2 || !reflect.DeepEqual(engine.started, []string{"container-1", "container-2"}) {
		t.Fatalf("Docker calls creates=%d starts=%v", len(engine.creates), engine.started)
	}
	populator := engine.creates[0]
	if populator.Config == nil || populator.HostConfig == nil || populator.Config.User != "0:0" ||
		populator.HostConfig.NetworkMode != "none" || populator.HostConfig.Privileged || !populator.HostConfig.ReadonlyRootfs ||
		len(populator.HostConfig.Mounts) != 3 || len(populator.Config.Env) != 0 {
		t.Fatalf("secret populator is not isolated: %+v", populator)
	}
	if len(populator.Config.Cmd) != 3 || !strings.Contains(populator.Config.Cmd[2], "0:0:400") {
		t.Fatalf("secret populator does not prove root-only output: %v", populator.Config.Cmd)
	}
	for _, argument := range append(slices.Clone(populator.Config.Entrypoint), populator.Config.Cmd...) {
		if strings.Contains(argument, "provider-key") {
			t.Fatalf("secret populator argv leaked provider key: %q", argument)
		}
	}
	if len(engine.volumeCreates) != 1 || engine.volumeCreates[0].Name != "switchback-runtime-secrets-runtime-1-g1" {
		t.Fatalf("secret volume creates = %+v", engine.volumeCreates)
	}
	if !reflect.DeepEqual(engine.removed, []string{"container-1"}) {
		t.Fatalf("secret populator removals = %v", engine.removed)
	}
	for _, path := range []string{request.Secrets.Bootstrap.BackendPath, request.Secrets.ModelCredential.BackendPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("transient Runtime secret input %q remains: %v", path, err)
		}
	}
	create := engine.creates[1]
	if create.Image != pinnedWorkerImage || create.Config == nil || create.HostConfig == nil {
		t.Fatalf("create options are incomplete: %+v", create)
	}
	if create.Name != "switchback-runtime-runtime-1" || create.Config.User != "0:0" || create.Config.WorkingDir != "/workspace" {
		t.Fatalf("portable config = %+v", create.Config)
	}
	if len(create.Config.ExposedPorts) != 0 || len(create.HostConfig.PortBindings) != 0 || create.HostConfig.PublishAllPorts {
		t.Fatalf("Worker exposes a host port: %+v", create.HostConfig)
	}
	if create.HostConfig.Privileged || create.HostConfig.NetworkMode != "switchback-worker-control" || create.HostConfig.PidMode == "host" {
		t.Fatalf("Worker isolation = %+v", create.HostConfig)
	}
	if !slices.Contains(create.HostConfig.ExtraHosts, "host.docker.internal:host-gateway") {
		t.Fatalf("Worker cannot resolve the server-owned host gateway: %+v", create.HostConfig.ExtraHosts)
	}
	network := request.NetworkProfile
	wantNetworkLabels := map[string]string{
		"com.switchback.network-policy-id":          network.ID,
		"com.switchback.network-policy-revision":    "1",
		"com.switchback.network-policy-digest":      network.Digest,
		"com.switchback.network-enforcement-status": config.NetworkEnforcementActive,
	}
	for label, want := range wantNetworkLabels {
		if create.Config.Labels[label] != want {
			t.Fatalf("network label %q=%q, want %q", label, create.Config.Labels[label], want)
		}
	}
	if !slices.Equal(create.HostConfig.CapDrop, []string{"ALL"}) || !slices.Equal(create.HostConfig.CapAdd, []string{"KILL", "SETUID", "SETGID"}) {
		t.Fatalf("Worker capabilities drop=%v add=%v", create.HostConfig.CapDrop, create.HostConfig.CapAdd)
	}
	if !slices.Contains(create.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Fatalf("Worker security options = %v", create.HostConfig.SecurityOpt)
	}
	if create.HostConfig.Resources.NanoCPUs != 2_000_000_000 || create.HostConfig.Resources.Memory != 4<<30 ||
		create.HostConfig.Resources.PidsLimit == nil || *create.HostConfig.Resources.PidsLimit != 512 {
		t.Fatalf("Worker resources = %+v", create.HostConfig.Resources)
	}
	if len(create.HostConfig.Mounts) != 4 {
		t.Fatalf("Worker mounts = %+v", create.HostConfig.Mounts)
	}
	wantMounts := map[string]string{
		"/workspace":                      "/srv/switchback/sessions/session-1/working-copy",
		"/var/lib/switchback/agent-state": "/srv/switchback/sessions/session-1/agent-state",
		"/var/lib/switchback/trace-spool": "/srv/switchback/sessions/session-1/trace-spool",
		"/run/switchback-secrets":         "switchback-runtime-secrets-runtime-1-g1",
	}
	for _, mounted := range create.HostConfig.Mounts {
		wantReadOnly := mounted.Target == "/run/switchback-secrets"
		if wantMounts[mounted.Target] != mounted.Source || mounted.ReadOnly != wantReadOnly || mounted.Source == "/var/run/docker.sock" {
			t.Fatalf("unexpected Worker mount: %+v", mounted)
		}
		delete(wantMounts, mounted.Target)
	}
	if len(wantMounts) != 0 {
		t.Fatalf("missing Worker mounts: %v", wantMounts)
	}
	if verifier.root != "/srv/switchback" || !reflect.DeepEqual(verifier.paths, []dockeradapter.HostPathRequirement{
		{Path: "/srv/switchback/sessions/session-1/working-copy", Kind: dockeradapter.HostPathDirectory},
		{Path: "/srv/switchback/sessions/session-1/agent-state", Kind: dockeradapter.HostPathDirectory},
		{Path: "/srv/switchback/sessions/session-1/trace-spool", Kind: dockeradapter.HostPathDirectory},
		{Path: "/srv/switchback/sessions/session-1/runtimes/runtime-1-g1/secret-input/bootstrap.json", Kind: dockeradapter.HostPathRegularFile},
		{Path: "/srv/switchback/sessions/session-1/runtimes/runtime-1-g1/secret-input/model", Kind: dockeradapter.HostPathRegularFile},
	}) {
		t.Fatalf("host path verification root=%q paths=%v", verifier.root, verifier.paths)
	}
	for _, environment := range create.Config.Env {
		if strings.HasPrefix(environment, "DOCKER_HOST=") || environment == "SWITCHBACK_BOOTSTRAP_SECRET_REF=caller-secret" {
			t.Fatalf("forbidden Worker environment: %q", environment)
		}
	}
}

func TestCleanupRemovesExactContainerAndSecretVolumeAndProvesAbsence(t *testing.T) {
	engine := &cleanupEngine{containerExists: true, volumeExists: true}
	cleaner := newProvisioner(t, engine, &recordingVerifier{})
	result, err := cleaner.Cleanup(context.Background(), application.RuntimeCleanupRequest{
		RuntimeInstanceID:   "runtime-1",
		ExpectedContainerID: "container-runtime-1",
		Generation:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Confirmed || result.ContainerExists || result.SecretVolumeExists ||
		result.ContainerRef != "switchback-runtime-runtime-1" ||
		result.SecretVolumeRef != "switchback-runtime-secrets-runtime-1-g1" {
		t.Fatalf("cleanup result=%+v", result)
	}
	if !reflect.DeepEqual(engine.removed, []string{"container-runtime-1"}) ||
		!reflect.DeepEqual(engine.volumeRemoved, []string{"switchback-runtime-secrets-runtime-1-g1"}) {
		t.Fatalf("removed containers=%v volumes=%v", engine.removed, engine.volumeRemoved)
	}
}

func TestCleanupReportsSecretResidueAndReconciliationRemovesIt(t *testing.T) {
	engine := &cleanupEngine{containerExists: true, volumeExists: true, volumeRemoveFailures: 1}
	cleaner := newProvisioner(t, engine, &recordingVerifier{})
	request := application.RuntimeCleanupRequest{
		RuntimeInstanceID:   "runtime-1",
		ExpectedContainerID: "container-runtime-1",
		Generation:          1,
	}
	failed, err := cleaner.Cleanup(context.Background(), request)
	if err == nil || failed.Confirmed || failed.ContainerExists || !failed.SecretVolumeExists {
		t.Fatalf("failed cleanup result=%+v error=%v", failed, err)
	}
	reconciled, err := cleaner.Cleanup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reconciled.Confirmed || reconciled.ContainerExists || reconciled.SecretVolumeExists {
		t.Fatalf("reconciled cleanup result=%+v", reconciled)
	}
}

func TestCleanupRejectsDifferentEngineContainerIDBeforeRemoval(t *testing.T) {
	engine := &cleanupEngine{containerExists: true, volumeExists: true}
	cleaner := newProvisioner(t, engine, &recordingVerifier{})
	observation, err := cleaner.Cleanup(
		context.Background(),
		application.RuntimeCleanupRequest{
			RuntimeInstanceID:   "runtime-1",
			ExpectedContainerID: "different-container-id",
			Generation:          1,
		},
	)
	if err == nil || observation.Confirmed {
		t.Fatalf(
			"mismatched container cleanup observation=%+v error=%v",
			observation,
			err,
		)
	}
	if len(engine.removed) != 0 || len(engine.volumeRemoved) != 0 {
		t.Fatalf(
			"mismatched cleanup removed containers=%v volumes=%v",
			engine.removed,
			engine.volumeRemoved,
		)
	}
}

func TestProvisionRejectsArbitraryMountAndProfileOverridesBeforeDocker(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*application.RuntimeProvisionRequest)
	}{
		{name: "docker socket", mutate: func(request *application.RuntimeProvisionRequest) {
			request.Storage.WorkingCopy.EngineSource = "/var/run/docker.sock"
		}},
		{name: "outside root", mutate: func(request *application.RuntimeProvisionRequest) {
			request.Storage.AgentState.EngineSource = "/tmp/caller"
		}},
		{name: "wrong Worker target", mutate: func(request *application.RuntimeProvisionRequest) { request.Storage.TraceSpool.WorkerPath = "/caller" }},
		{name: "resource revision", mutate: func(request *application.RuntimeProvisionRequest) { request.ResourceProfile.Revision++ }},
		{name: "resource digest", mutate: func(request *application.RuntimeProvisionRequest) { request.ResourceProfile.Digest = "sha256:caller" }},
		{name: "agent profile", mutate: func(request *application.RuntimeProvisionRequest) { request.AgentProfile.ID = "caller-profile" }},
		{name: "network profile", mutate: func(request *application.RuntimeProvisionRequest) { request.NetworkProfile.ID = "caller-profile" }},
		{name: "network digest", mutate: func(request *application.RuntimeProvisionRequest) { request.NetworkProfile.Digest = "sha256:caller" }},
		{name: "network status", mutate: func(request *application.RuntimeProvisionRequest) { request.NetworkEnforcementStatus = "caller-status" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
			request := validRequest(t)
			tt.mutate(&request)
			if _, err := newProvisioner(t, engine, &recordingVerifier{}).Provision(context.Background(), request); err == nil {
				t.Fatal("unsafe Runtime provision request was accepted")
			}
			if len(engine.creates) != 0 {
				t.Fatal("Docker was called before request rejection")
			}
		})
	}
}

func TestProvisionReusesOnlyExactInspectedNetworkConfiguration(t *testing.T) {
	request := validRequest(t)
	createdEngine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	if _, err := newProvisioner(t, createdEngine, &recordingVerifier{}).Provision(
		context.Background(),
		request,
	); err != nil {
		t.Fatal(err)
	}
	created := createdEngine.creates[1]
	inspectedConfig := *created.Config
	inspectedConfig.Image = created.Image
	inspection := client.ContainerInspectResult{Container: container.InspectResponse{
		ID: "container-existing", Created: time.Now().UTC().Format(time.RFC3339Nano),
		Config: &inspectedConfig, HostConfig: created.HostConfig,
		State: &container.State{Running: true},
	}}
	exactEngine := &recordingEngine{inspect: inspection}
	reused, err := newProvisioner(t, exactEngine, &recordingVerifier{}).Provision(
		context.Background(),
		request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reused.ContainerID != "container-existing" || len(exactEngine.creates) != 0 {
		t.Fatalf("exact Runtime was not reused: result=%+v creates=%d", reused, len(exactEngine.creates))
	}

	driftedLabels := make(map[string]string, len(inspectedConfig.Labels))
	for key, value := range inspectedConfig.Labels {
		driftedLabels[key] = value
	}
	driftedLabels["com.switchback.network-enforcement-status"] = "legacy_unenforced"
	driftedConfig := inspectedConfig
	driftedConfig.Labels = driftedLabels
	driftedEngine := &recordingEngine{inspect: client.ContainerInspectResult{Container: container.InspectResponse{
		ID: "container-existing", Created: time.Now().UTC().Format(time.RFC3339Nano),
		Config: &driftedConfig, HostConfig: created.HostConfig,
		State: &container.State{Running: true},
	}}}
	if _, err := newProvisioner(t, driftedEngine, &recordingVerifier{}).Provision(
		context.Background(),
		request,
	); err == nil {
		t.Fatal("existing Runtime with drifted network evidence was reused")
	}
}

func TestLocalHostPathVerifierRejectsEscapeAndSymlink(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "sessions", "session-1")
	workingCopy := filepath.Join(session, "working-copy")
	if err := os.MkdirAll(workingCopy, 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := dockeradapter.LocalHostPathVerifier{}
	if err := verifier.VerifyHostPaths(context.Background(), root, []dockeradapter.HostPathRequirement{{Path: workingCopy, Kind: dockeradapter.HostPathDirectory}}); err != nil {
		t.Fatalf("verify server-derived HostPath: %v", err)
	}
	if err := verifier.VerifyHostPaths(context.Background(), root, []dockeradapter.HostPathRequirement{{Path: t.TempDir(), Kind: dockeradapter.HostPathDirectory}}); err == nil {
		t.Fatal("HostPath outside the configured root was accepted")
	}
	outside := t.TempDir()
	link := filepath.Join(session, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyHostPaths(context.Background(), root, []dockeradapter.HostPathRequirement{{Path: link, Kind: dockeradapter.HostPathDirectory}}); err == nil {
		t.Fatal("symlink HostPath was accepted")
	}
}

func TestNewProvisionerRejectsHostNetworkFloatingImageAndMissingVerifier(t *testing.T) {
	base := dockeradapter.Options{
		Image:              pinnedWorkerImage,
		Network:            "switchback-worker-control",
		EngineDataRoot:     "/srv/switchback",
		BackendGRPCTarget:  "switchback-backend:9090",
		BackendHTTPBaseURL: "http://switchback-backend:8080",
		HostPathVerifier:   &recordingVerifier{},
	}
	tests := []struct {
		name   string
		mutate func(*dockeradapter.Options)
	}{
		{name: "host network", mutate: func(options *dockeradapter.Options) { options.Network = "host" }},
		{name: "container network", mutate: func(options *dockeradapter.Options) { options.Network = "container:other" }},
		{name: "floating image", mutate: func(options *dockeradapter.Options) { options.Image = "switchback-worker:latest" }},
		{name: "filesystem root", mutate: func(options *dockeradapter.Options) { options.EngineDataRoot = "/" }},
		{name: "missing verifier", mutate: func(options *dockeradapter.Options) { options.HostPathVerifier = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := base
			tt.mutate(&options)
			if _, err := dockeradapter.NewProvisioner(&recordingEngine{}, options); err == nil {
				t.Fatal("unsafe Docker options were accepted")
			}
		})
	}
}

func TestNewProvisionerAcceptsImmutableLocalImageID(t *testing.T) {
	options := dockeradapter.Options{
		Image:              "sha256:" + strings.Repeat("a", 64),
		Network:            "switchback-worker-control",
		EngineDataRoot:     "/srv/switchback",
		BackendGRPCTarget:  "switchback-backend:9090",
		BackendHTTPBaseURL: "http://switchback-backend:8080",
		HostPathVerifier:   &recordingVerifier{},
	}
	if _, err := dockeradapter.NewProvisioner(&recordingEngine{}, options); err != nil {
		t.Fatalf("accept immutable local image id: %v", err)
	}
}

const pinnedWorkerImage = "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111"

func newProvisioner(t *testing.T, engine dockeradapter.Engine, verifier dockeradapter.HostPathVerifier) *dockeradapter.Provisioner {
	t.Helper()
	provisioner, err := dockeradapter.NewProvisioner(engine, dockeradapter.Options{
		Image:              pinnedWorkerImage,
		Network:            "switchback-worker-control",
		EngineDataRoot:     "/srv/switchback",
		BackendGRPCTarget:  "switchback-backend:9090",
		BackendHTTPBaseURL: "http://switchback-backend:8080",
		HostPathVerifier:   verifier,
	})
	if err != nil {
		t.Fatalf("new provisioner: %v", err)
	}
	return provisioner
}

func validRequest(t *testing.T) application.RuntimeProvisionRequest {
	t.Helper()
	network, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if err != nil {
		t.Fatal(err)
	}
	backendRoot := filepath.Join(t.TempDir(), "sessions", "session-1")
	secretRoot := filepath.Join(backendRoot, "runtimes", "runtime-1-g1", "secret-input")
	for _, directory := range []string{
		filepath.Join(backendRoot, "working-copy"),
		filepath.Join(backendRoot, "agent-state"),
		filepath.Join(backendRoot, "trace-spool"),
		secretRoot,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]string{
		filepath.Join(secretRoot, "bootstrap.json"): "{}\n",
		filepath.Join(secretRoot, "model"):          "provider-key",
	} {
		if err := os.WriteFile(path, []byte(value), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	return application.RuntimeProvisionRequest{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		Generation:        1,
		ResourceProfile: application.ProfileRef{
			ID:       "local_standard_v1",
			Revision: 1,
			Digest:   "sha256:26a711346a64896a5d56448065fb2ce89284d9f86fa4a991407a4b56ca8a76d4",
		},
		AgentProfile: application.ProfileRef{
			ID:       "deepseek-codex-openai-v1",
			Revision: 1,
			Digest:   "sha256:d4437eefaf16e95d363b1c55fefd4d1fe73d3238b32ab3afa10b85f1acb134fb",
		},
		NetworkProfile: application.ProfileRef{
			ID: network.ID, Revision: network.Revision, Digest: network.Digest,
		},
		NetworkEnforcementStatus: config.NetworkEnforcementActive,
		Storage: application.SessionStorage{
			BackendRoot: backendRoot,
			EngineRoot:  "/srv/switchback/sessions/session-1",
			WorkingCopy: application.StorageMount{BackendPath: filepath.Join(backendRoot, "working-copy"), EngineSource: "/srv/switchback/sessions/session-1/working-copy", WorkerPath: "/workspace"},
			AgentState:  application.StorageMount{BackendPath: filepath.Join(backendRoot, "agent-state"), EngineSource: "/srv/switchback/sessions/session-1/agent-state", WorkerPath: "/var/lib/switchback/agent-state"},
			TraceSpool:  application.StorageMount{BackendPath: filepath.Join(backendRoot, "trace-spool"), EngineSource: "/srv/switchback/sessions/session-1/trace-spool", WorkerPath: "/var/lib/switchback/trace-spool"},
		},
		Secrets: application.RuntimeSecretResources{
			BackendRoot: secretRoot,
			EngineRoot:  "/srv/switchback/sessions/session-1/runtimes/runtime-1-g1/secret-input",
			Bootstrap: application.StorageMount{
				BackendPath:  filepath.Join(secretRoot, "bootstrap.json"),
				EngineSource: "/srv/switchback/sessions/session-1/runtimes/runtime-1-g1/secret-input/bootstrap.json",
				WorkerPath:   "/run/switchback-secrets/bootstrap.json",
			},
			ModelCredential: application.StorageMount{
				BackendPath:  filepath.Join(secretRoot, "model"),
				EngineSource: "/srv/switchback/sessions/session-1/runtimes/runtime-1-g1/secret-input/model",
				WorkerPath:   "/run/switchback-secrets/model",
			},
		},
	}
}

type recordingVerifier struct {
	root  string
	paths []dockeradapter.HostPathRequirement
	err   error
}

func (v *recordingVerifier) VerifyHostPaths(_ context.Context, root string, paths []dockeradapter.HostPathRequirement) error {
	v.root = root
	v.paths = slices.Clone(paths)
	return v.err
}

type recordingEngine struct {
	creates       []client.ContainerCreateOptions
	started       []string
	removed       []string
	volumeCreates []client.VolumeCreateOptions
	inspect       client.ContainerInspectResult
	inspectErr    error
}

type cleanupEngine struct {
	recordingEngine
	containerExists      bool
	volumeExists         bool
	volumeRemoveFailures int
	volumeRemoved        []string
}

func (e *cleanupEngine) ContainerInspect(_ context.Context, _ string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if !e.containerExists {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID: "container-runtime-1",
		Config: &container.Config{Labels: map[string]string{
			"com.switchback.managed":             "worker-runtime",
			"com.switchback.runtime-instance-id": "runtime-1",
			"com.switchback.runtime-generation":  "1",
		}},
		State: &container.State{Running: true},
	}}, nil
}

func (e *cleanupEngine) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	e.removed = append(e.removed, id)
	e.containerExists = false
	return client.ContainerRemoveResult{}, nil
}

func (e *cleanupEngine) VolumeInspect(_ context.Context, name string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	if !e.volumeExists {
		return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.VolumeInspectResult{Volume: volume.Volume{
		Name: name, Driver: "local", Labels: map[string]string{
			"com.switchback.managed":             "runtime-secret-volume",
			"com.switchback.runtime-instance-id": "runtime-1",
			"com.switchback.runtime-generation":  "1",
		},
	}}, nil
}

func (e *cleanupEngine) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	e.volumeRemoved = append(e.volumeRemoved, name)
	if e.volumeRemoveFailures > 0 {
		e.volumeRemoveFailures--
		return client.VolumeRemoveResult{}, fmt.Errorf("injected volume delete failure")
	}
	e.volumeExists = false
	return client.VolumeRemoveResult{}, nil
}

func (e *recordingEngine) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	e.creates = append(e.creates, options)
	return client.ContainerCreateResult{ID: fmt.Sprintf("container-%d", len(e.creates))}, nil
}

func (e *recordingEngine) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	e.started = append(e.started, id)
	return client.ContainerStartResult{}, nil
}

func (e *recordingEngine) ContainerInspect(_ context.Context, _ string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	return e.inspect, e.inspectErr
}

func (e *recordingEngine) ContainerWait(_ context.Context, _ string, _ client.ContainerWaitOptions) client.ContainerWaitResult {
	result := make(chan container.WaitResponse, 1)
	errors := make(chan error, 1)
	result <- container.WaitResponse{StatusCode: 0}
	return client.ContainerWaitResult{Result: result, Error: errors}
}

func (e *recordingEngine) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	e.removed = append(e.removed, id)
	return client.ContainerRemoveResult{}, nil
}

func (e *recordingEngine) VolumeCreate(_ context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	e.volumeCreates = append(e.volumeCreates, options)
	return client.VolumeCreateResult{Volume: volume.Volume{Name: options.Name, Driver: options.Driver, Labels: options.Labels}}, nil
}

func (e *recordingEngine) VolumeInspect(_ context.Context, _ string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
}

func (e *recordingEngine) VolumeRemove(_ context.Context, _ string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	return client.VolumeRemoveResult{}, nil
}

var (
	_ dockeradapter.Engine           = (*recordingEngine)(nil)
	_ dockeradapter.HostPathVerifier = (*recordingVerifier)(nil)
	_ application.RuntimeProvisioner = (*dockeradapter.Provisioner)(nil)
)
