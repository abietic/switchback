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
	provisioner := newProvisioner(t, classicOverlay2Engine(engine), verifier)
	request := validRequest(t)

	got, err := provisioner.Provision(context.Background(), request, allowSyntheticPreStart)
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
	if create.HostConfig.Resources.CgroupParent != "switchback-workers.slice" ||
		create.HostConfig.Resources.NanoCPUs != 2_000_000_000 || create.HostConfig.Resources.Memory != 4<<30 ||
		create.HostConfig.Resources.MemorySwap != 4<<30 ||
		create.HostConfig.Resources.PidsLimit == nil || *create.HostConfig.Resources.PidsLimit != 512 {
		t.Fatalf("Worker resources = %+v", create.HostConfig.Resources)
	}
	if !reflect.DeepEqual(create.HostConfig.StorageOpt, map[string]string{"size": "10737418240"}) {
		t.Fatalf("Worker writable-layer size = %v, want fixed 10 GiB", create.HostConfig.StorageOpt)
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
		name    string
		wantErr string
		mutate  func(*application.RuntimeProvisionRequest)
	}{
		{name: "docker socket", wantErr: "working copy mount is not server-derived", mutate: func(request *application.RuntimeProvisionRequest) {
			request.Storage.WorkingCopy.EngineSource = "/var/run/docker.sock"
		}},
		{name: "outside root", wantErr: "agent state mount is not server-derived", mutate: func(request *application.RuntimeProvisionRequest) {
			request.Storage.AgentState.EngineSource = "/tmp/caller"
		}},
		{name: "wrong Worker target", wantErr: "trace spool mount is not server-derived", mutate: func(request *application.RuntimeProvisionRequest) { request.Storage.TraceSpool.WorkerPath = "/caller" }},
		{name: "resource revision", wantErr: "runtime resource profile \"local_standard_v1\" reference is stale or invalid", mutate: func(request *application.RuntimeProvisionRequest) { request.ResourceProfile.Revision++ }},
		{name: "resource profile", wantErr: "runtime resource profile \"caller-profile\" is unsupported", mutate: func(request *application.RuntimeProvisionRequest) { request.ResourceProfile.ID = "caller-profile" }},
		{name: "resource digest", wantErr: "runtime resource profile \"local_standard_v1\" reference is stale or invalid", mutate: func(request *application.RuntimeProvisionRequest) { request.ResourceProfile.Digest = "sha256:caller" }},
		{name: "agent profile", wantErr: "agent profile \"caller-profile\" is unsupported", mutate: func(request *application.RuntimeProvisionRequest) { request.AgentProfile.ID = "caller-profile" }},
		{name: "network profile", wantErr: "Runtime network profile is unavailable or stale", mutate: func(request *application.RuntimeProvisionRequest) { request.NetworkProfile.ID = "caller-profile" }},
		{name: "network digest", wantErr: "Runtime network profile is unavailable or stale", mutate: func(request *application.RuntimeProvisionRequest) { request.NetworkProfile.Digest = "sha256:caller" }},
		{name: "network status", wantErr: "Runtime network profile is unavailable or stale", mutate: func(request *application.RuntimeProvisionRequest) { request.NetworkEnforcementStatus = "caller-status" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
			engine := classicOverlay2Engine(recording)
			request := validRequest(t)
			tt.mutate(&request)
			verifier := &recordingVerifier{}
			if _, err := newProvisioner(t, engine, verifier).Provision(context.Background(), request, allowSyntheticPreStart); err == nil || err.Error() != tt.wantErr {
				t.Fatalf("unsafe Runtime provision error = %v, want %q", err, tt.wantErr)
			}
			if recording.inspections != 0 || len(recording.creates) != 0 || len(recording.started) != 0 || len(recording.volumeCreates) != 0 || len(verifier.paths) != 0 {
				t.Fatalf("request validation reached provision operations: inspect=%d creates=%d starts=%v volumes=%d paths=%v", recording.inspections, len(recording.creates), recording.started, len(recording.volumeCreates), verifier.paths)
			}
		})
	}
}

func TestProvisionRejectsEvenExactExistingNetworkConfiguration(t *testing.T) {
	request := validRequest(t)
	createdEngine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	if _, err := newProvisioner(t, classicOverlay2Engine(createdEngine), &recordingVerifier{}).Provision(
		context.Background(),
		request, allowSyntheticPreStart); err != nil {
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
	reused, err := newProvisioner(t, classicOverlay2Engine(exactEngine), &recordingVerifier{}).Provision(
		context.Background(),
		request, allowSyntheticPreStart)
	if err == nil || reused.ContainerID != "container-existing" || !reused.StartedAt.IsZero() || len(exactEngine.creates) != 0 || len(exactEngine.started) != 0 {
		t.Fatalf("existing Runtime must remain pending without adoption: result=%+v err=%v creates=%d starts=%v", reused, err, len(exactEngine.creates), exactEngine.started)
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
	if _, err := newProvisioner(t, classicOverlay2Engine(driftedEngine), &recordingVerifier{}).Provision(
		context.Background(),
		request, allowSyntheticPreStart); err == nil {
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
	creates           []client.ContainerCreateOptions
	started           []string
	removed           []string
	volumeCreates     []client.VolumeCreateOptions
	volumeInspections int
	volumeRemoved     []string
	inspect           client.ContainerInspectResult
	inspectErr        error
	inspections       int
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

func (e *recordingEngine) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	e.inspections++
	for i, created := range e.creates {
		if id == fmt.Sprintf("container-%d", i+1) {
			cfg := *created.Config
			cfg.Image = created.Image
			return client.ContainerInspectResult{Container: container.InspectResponse{
				ID: id, Config: &cfg, HostConfig: created.HostConfig,
				State: &container.State{Status: "created"},
			}}, nil
		}
	}
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
	e.volumeInspections++
	return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
}

func (e *recordingEngine) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	e.volumeRemoved = append(e.volumeRemoved, name)
	return client.VolumeRemoveResult{}, nil
}

var (
	_ dockeradapter.Engine           = (*recordingEngine)(nil)
	_ dockeradapter.HostPathVerifier = (*recordingVerifier)(nil)
	_ application.RuntimeProvisioner = (*dockeradapter.Provisioner)(nil)
)

// Synthetic only: production always requires trusted created-layer evidence.
func allowSyntheticPreStart(context.Context, application.CreatedRuntimeIdentity) error { return nil }

func TestProvisionRequiresPreStartCheckBeforeAnyEffects(t *testing.T) {
	recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	engine := classicOverlay2Engine(recording)
	verifier := &recordingVerifier{}
	request := validRequest(t)
	got, err := newProvisioner(t, engine, verifier).Provision(context.Background(), request, nil)
	if err == nil {
		t.Fatal("missing per-call pre-start check accepted")
	}
	if got.RuntimeInstanceID != request.RuntimeInstanceID || got.Generation != request.Generation || got.PhysicalEffectUnknown || !got.StartedAt.IsZero() {
		t.Fatalf("unissued effect identity = %+v", got)
	}
	if engine.infoCalls != 0 {
		t.Fatalf("missing check reached Info: %d", engine.infoCalls)
	}
	assertNoProvisionSideEffects(t, recording, verifier, request.Secrets.Bootstrap.BackendPath)
}
