package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func TestDockerStagesRuntimeSecretsInAnIsolatedNamedVolumeBeforeCreatingWorker(t *testing.T) {
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	request, options := dockerTestRequest(t)
	bootstrapSecret := "bootstrap-private-value"
	modelSecret := "model-private-value"
	if err := os.WriteFile(request.Bootstrap.ContainerPath, []byte(bootstrapSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Bootstrap.HostPath, []byte(bootstrapSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.ModelSecret.ContainerPath, []byte(modelSecret), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.ModelSecret.HostPath, []byte(modelSecret), 0o400); err != nil {
		t.Fatal(err)
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	instance, err := provisioner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := instance.ContainerID, "container-2"; got != want {
		t.Fatalf("ContainerID = %q, want %q", got, want)
	}
	if got, want := engine.started, []string{"container-1", "container-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("started containers = %v, want %v", got, want)
	}

	if got, want := engine.volumeCreates, []client.VolumeCreateOptions{{
		Name:   "switchback-runtime-secrets-runtime-1-g1",
		Driver: "local",
		Labels: map[string]string{
			LabelManaged:           "runtime-secret-volume",
			LabelWorkerSessionID:   "session-1",
			LabelWorkerRunID:       "run-1",
			LabelRuntimeInstanceID: "runtime-1",
			LabelRuntimeGeneration: "1",
		},
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("volume creates = %#v, want %#v", got, want)
	}

	if got, want := len(engine.creates), 2; got != want {
		t.Fatalf("container creates = %d, want %d", got, want)
	}
	populator := engine.creates[0]
	if got, want := populator.Name, "switchback-runtime-secret-populator-runtime-1-g1"; got != want {
		t.Fatalf("populator name = %q, want %q", got, want)
	}
	if populator.Image != options.Image {
		t.Fatalf("populator image = %q, want pinned Worker image %q", populator.Image, options.Image)
	}
	if populator.Config == nil || populator.HostConfig == nil {
		t.Fatal("populator config is incomplete")
	}
	wantPopulatorLabels := map[string]string{
		LabelManaged:           "runtime-secret-populator",
		LabelWorkerSessionID:   "session-1",
		LabelWorkerRunID:       "run-1",
		LabelRuntimeInstanceID: "runtime-1",
		LabelRuntimeGeneration: "1",
	}
	if !reflect.DeepEqual(populator.Config.Labels, wantPopulatorLabels) {
		t.Fatalf("populator labels = %#v, want %#v", populator.Config.Labels, wantPopulatorLabels)
	}
	if got, want := populator.Config.Env, []string{
		"PATH=/usr/local/go/bin:/opt/switchback/node_modules/.bin:/usr/local/bin:/usr/bin:/bin",
		"GPG_KEY=7169605F62C751356D054A26A821E680E5FA6305",
		"PYTHON_VERSION=3.13.14",
		"PYTHON_SHA256=639e43243c620a308f968213df9e00f2f8f62332f7adbaa7a7eeb9783057c690",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("populator environment = %#v, want exact non-secret image allowlist %#v", got, want)
	}
	if populator.Config.User != "0:0" || populator.Config.WorkingDir != "/" {
		t.Fatalf(
			"populator root identity = user %q working dir %q, want 0:0 and /",
			populator.Config.User,
			populator.Config.WorkingDir,
		)
	}
	for _, argument := range append(append([]string{}, populator.Config.Entrypoint...), populator.Config.Cmd...) {
		for _, secret := range []string{bootstrapSecret, modelSecret} {
			if strings.Contains(argument, secret) {
				t.Fatalf("populator argv exposed secret %q", secret)
			}
		}
	}
	populatorHost := populator.HostConfig
	if populatorHost.NetworkMode != container.NetworkMode("none") ||
		populatorHost.Privileged ||
		!populatorHost.ReadonlyRootfs ||
		populatorHost.PublishAllPorts ||
		len(populatorHost.PortBindings) != 0 ||
		len(populatorHost.Binds) != 0 {
		t.Fatalf("populator isolation = %#v", populatorHost)
	}
	if got, want := populatorHost.CapDrop, []string{"ALL"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("populator CapDrop = %#v, want %#v", got, want)
	}
	if len(populatorHost.CapAdd) != 0 {
		t.Fatalf("populator CapAdd = %#v, want empty", populatorHost.CapAdd)
	}
	if got, want := populatorHost.SecurityOpt, []string{"no-new-privileges"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("populator SecurityOpt = %#v, want %#v", got, want)
	}
	wantPopulatorMounts := []mount.Mount{
		{Type: mount.TypeBind, Source: request.Bootstrap.HostPath, Target: "/opt/switchback/secret-input/bootstrap.json", ReadOnly: true},
		{Type: mount.TypeBind, Source: request.ModelSecret.HostPath, Target: "/opt/switchback/secret-input/model", ReadOnly: true},
		{Type: mount.TypeVolume, Source: "switchback-runtime-secrets-runtime-1-g1", Target: "/opt/switchback/secret-output"},
	}
	if got := populatorHost.Mounts; !reflect.DeepEqual(got, wantPopulatorMounts) {
		t.Fatalf("populator mounts = %#v, want %#v", got, wantPopulatorMounts)
	}
	if got, want := engine.removed, []string{"container-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed containers before Worker start = %#v, want %#v", got, want)
	}
	if got, want := engine.removeOptions, []client.ContainerRemoveOptions{{Force: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("populator remove options = %#v, want %#v", got, want)
	}

	create := engine.creates[1]
	if create.Name != "switchback-runtime-runtime-1" {
		t.Fatalf("Worker container name = %q", create.Name)
	}
	if create.Image != options.Image {
		t.Fatalf("Worker image = %q, want server image %q", create.Image, options.Image)
	}
	if create.Config.User != "0:0" ||
		!reflect.DeepEqual(create.Config.Entrypoint, []string{"/usr/bin/tini", "--"}) ||
		!reflect.DeepEqual(create.Config.Cmd, []string{"/usr/local/bin/worker-shim"}) {
		t.Fatalf(
			"Worker execution identity = user %q entrypoint %#v cmd %#v, want trusted root shim",
			create.Config.User,
			create.Config.Entrypoint,
			create.Config.Cmd,
		)
	}
	wantLabels := map[string]string{
		LabelManaged:           ManagedWorkerRuntime,
		LabelWorkerSessionID:   "session-1",
		LabelWorkerRunID:       "run-1",
		LabelRuntimeInstanceID: "runtime-1",
		LabelRuntimeGeneration: "1",
	}
	if !reflect.DeepEqual(create.Config.Labels, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", create.Config.Labels, wantLabels)
	}
	if got := create.Config.ExposedPorts; len(got) != 0 {
		t.Fatalf("exposed ports = %#v, want none", got)
	}
	if create.HostConfig == nil {
		t.Fatal("HostConfig = nil")
	}
	host := create.HostConfig
	if host.Privileged {
		t.Fatal("Privileged = true")
	}
	if host.ReadonlyRootfs {
		t.Fatal("ReadonlyRootfs = true, want writable layer for Coding Agent tools")
	}
	if host.NetworkMode != container.NetworkMode(options.Network) || host.NetworkMode.IsHost() {
		t.Fatalf("NetworkMode = %q, want named server network %q", host.NetworkMode, options.Network)
	}
	if host.PublishAllPorts || len(host.PortBindings) != 0 {
		t.Fatalf("host ports enabled: PublishAllPorts=%v PortBindings=%#v", host.PublishAllPorts, host.PortBindings)
	}
	if len(host.Binds) != 0 {
		t.Fatalf("legacy Binds = %#v, want typed mounts only", host.Binds)
	}
	if host.StorageOpt != nil {
		t.Fatalf("StorageOpt = %#v, want nil because writable layer limit is not verified", host.StorageOpt)
	}
	if got, want := host.NanoCPUs, int64(2_000_000_000); got != want {
		t.Fatalf("NanoCPUs = %d, want %d", got, want)
	}
	if got, want := host.Memory, int64(4<<30); got != want {
		t.Fatalf("Memory = %d, want %d", got, want)
	}
	if host.PidsLimit == nil || *host.PidsLimit != 512 {
		t.Fatalf("PidsLimit = %v, want 512", host.PidsLimit)
	}
	if got, want := host.CapDrop, []string{"ALL"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CapDrop = %v, want %v", got, want)
	}
	if got, want := host.CapAdd, []string{"KILL", "SETUID", "SETGID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CapAdd = %v, want %v", got, want)
	}
	if got, want := host.SecurityOpt, []string{"no-new-privileges"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SecurityOpt = %v, want %v", got, want)
	}

	wantMounts := []mount.Mount{
		{Type: mount.TypeBind, Source: request.WorkingCopy.HostPath, Target: WorkerWorkingCopyPath},
		{Type: mount.TypeBind, Source: request.AgentState.HostPath, Target: WorkerAgentStatePath},
		{Type: mount.TypeBind, Source: request.TraceSpool.HostPath, Target: WorkerTraceSpoolPath},
		{Type: mount.TypeVolume, Source: "switchback-runtime-secrets-runtime-1-g1", Target: "/run/switchback-secrets", ReadOnly: true},
	}
	if !reflect.DeepEqual(host.Mounts, wantMounts) {
		t.Fatalf("mounts = %#v, want %#v", host.Mounts, wantMounts)
	}
	for _, mounted := range host.Mounts {
		if mounted.Source == "/" || mounted.Source == "/var/run/docker.sock" || mounted.Source == options.FixturePath {
			t.Fatalf("forbidden mount source = %q", mounted.Source)
		}
	}
	for _, transient := range []string{request.Bootstrap.ContainerPath, request.ModelSecret.ContainerPath} {
		if _, err := os.Lstat(transient); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transient source %q still exists after staging: %v", transient, err)
		}
	}
}

func TestDockerInjectsOnlyValidatedBackendWorkerControlAndUploadEndpoints(t *testing.T) {
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	request, options := dockerTestRequest(t)
	options.BackendGRPCTarget = "spike-backend:9090"
	options.BackendHTTPBaseURL = "http://spike-backend:8080"
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}
	if _, err := provisioner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for _, want := range []string{
		"SWITCHBACK_BACKEND_GRPC_TARGET=spike-backend:9090",
		"SWITCHBACK_BACKEND_HTTP_BASE_URL=http://spike-backend:8080",
	} {
		if !containsString(engine.created.Config.Env, want) {
			t.Fatalf("Worker environment = %#v, want %q", engine.created.Config.Env, want)
		}
	}
}

func TestDockerHostPathVerifierMountsRootAndPassesRelativeRequirements(t *testing.T) {
	engine := &recordingEngine{}
	verifier, err := NewDockerHostPathVerifier(context.Background(), engine, "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
	}
	requirements := []HostPathRequirement{
		{Path: "/host/switchback-spike/sessions/session-1/working-copy", Kind: HostPathDirectory},
		{Path: "/host/switchback-spike/env/upstream-model.key", Kind: HostPathRegularFile},
	}
	if err := verifier.VerifyHostPaths(context.Background(), "/host/switchback-spike", requirements); err != nil {
		t.Fatalf("VerifyHostPaths() error = %v", err)
	}
	if engine.createCalls != 1 || len(engine.started) != 1 || len(engine.removed) != 1 {
		t.Fatalf("engine calls create=%d start=%d remove=%d, want one trusted attestation", engine.createCalls, len(engine.started), len(engine.removed))
	}
	if engine.created.HostConfig.NetworkMode != "none" || engine.created.HostConfig.Privileged {
		t.Fatalf("attestation host config = %#v, want isolated unprivileged probe", engine.created.HostConfig)
	}
	if !engine.created.HostConfig.ReadonlyRootfs {
		t.Fatal("attestation ReadonlyRootfs = false, want true")
	}
	if got, want := engine.created.HostConfig.CapDrop, []string{"ALL"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attestation CapDrop = %#v, want %#v", got, want)
	}
	if got, want := engine.created.HostConfig.SecurityOpt, []string{"no-new-privileges"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attestation SecurityOpt = %#v, want %#v", got, want)
	}
	wantMounts := []mount.Mount{{
		Type:     mount.TypeBind,
		Source:   "/host/switchback-spike",
		Target:   "/opt/switchback/attest/root",
		ReadOnly: true,
		BindOptions: &mount.BindOptions{
			Propagation:            mount.PropagationRPrivate,
			ReadOnlyForceRecursive: true,
		},
	}}
	if got := engine.created.HostConfig.Mounts; !reflect.DeepEqual(got, wantMounts) {
		t.Fatalf("attestation mounts = %#v, want one server-owned root mount %#v", got, wantMounts)
	}
	wantArguments := []string{
		"directory", "sessions/session-1/working-copy",
		"regular-file", "env/upstream-model.key",
	}
	if got := engine.created.Config.Cmd; len(got) < len(wantArguments) ||
		!reflect.DeepEqual(got[len(got)-len(wantArguments):], wantArguments) {
		t.Fatalf("attestation command = %#v, want trailing relative requirement arguments %#v", got, wantArguments)
	}
	for _, value := range engine.created.Config.Cmd {
		if strings.Contains(value, "/host/switchback-spike") {
			t.Fatalf("attestation command contains absolute host path %q", value)
		}
	}
}

func TestDockerHostPathVerifierBuildsNoFollowSegmentChecks(t *testing.T) {
	engine := &recordingEngine{}
	verifier, err := NewDockerHostPathVerifier(context.Background(), engine, "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
	}
	if err := verifier.VerifyHostPaths(context.Background(), "/host/switchback-spike", []HostPathRequirement{{
		Path: "/host/switchback-spike/sessions/session-1/bootstrap.json",
		Kind: HostPathRegularFile,
	}}); err != nil {
		t.Fatalf("VerifyHostPaths() error = %v", err)
	}

	command := strings.Join(engine.created.Config.Cmd, "\n")
	for _, required := range []string{
		`current="${current}/${segment}"`,
		`[ ! -L "$current" ]`,
		`[ "$segment" != "." ]`,
		`[ "$segment" != ".." ]`,
		`directory) [ -d "$current" ]`,
		`regular-file) [ -f "$current" ]`,
	} {
		if !strings.Contains(command, required) {
			t.Errorf("attestation command = %q, want fixed no-follow check %q", command, required)
		}
	}
	if strings.Contains(command, "eval") {
		t.Fatalf("attestation command uses forbidden eval: %q", command)
	}
}

func TestDockerHostPathVerifierUsesBoundedLifecycleCleanupAfterRequestCancellation(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cleanupErr := errors.New("injected attestor cleanup failure")
	engine := &recordingEngine{
		blockWaitUntilCancel: true,
		waitStarted:          make(chan struct{}),
		waitCanceled:         make(chan struct{}),
		cancelRequestOnWait:  cancelRequest,
		removeErr:            cleanupErr,
	}
	verifier, err := NewDockerHostPathVerifier(context.Background(), engine, "switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
	}

	err = verifier.VerifyHostPaths(requestCtx, "/host/switchback-spike", []HostPathRequirement{{
		Path: "/host/switchback-spike/sessions/session-1/working-copy",
		Kind: HostPathDirectory,
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifyHostPaths() error = %v, want request cancellation", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("VerifyHostPaths() error = %v, want joined cleanup failure", err)
	}
	if got := len(engine.removeContexts); got != 1 {
		t.Fatalf("attestor remove contexts = %d, want 1", got)
	}
	observation := engine.removeContexts[0]
	if observation.err != nil {
		t.Fatalf("attestor remove context error = %v, want lifecycle context independent of request cancellation", observation.err)
	}
	if !observation.hasDeadline {
		t.Fatal("attestor remove context has no deadline, want bounded lifecycle cleanup")
	}
	remaining := time.Until(observation.deadline)
	if remaining <= 0 || remaining > 10*time.Second {
		t.Fatalf("attestor remove deadline remaining = %v, want within (0, 10s]", remaining)
	}
}

func TestDockerHostPathVerifierUsesBoundedCleanupAfterLifecycleCancellation(t *testing.T) {
	type lifecycleValueKey struct{}
	valueKey := lifecycleValueKey{}
	startErr := errors.New("injected attestor start failure")
	waitErr := errors.New("injected attestor wait failure")
	tests := []struct {
		name      string
		configure func(*recordingEngine, context.CancelFunc)
		wantErr   error
	}{
		{name: "success"},
		{
			name: "start error",
			configure: func(engine *recordingEngine, _ context.CancelFunc) {
				engine.startErr = startErr
			},
			wantErr: startErr,
		},
		{
			name: "wait error",
			configure: func(engine *recordingEngine, _ context.CancelFunc) {
				engine.waitErr = waitErr
			},
			wantErr: waitErr,
		},
		{
			name: "request cancel",
			configure: func(engine *recordingEngine, cancelRequest context.CancelFunc) {
				engine.blockWaitUntilCancel = true
				engine.waitStarted = make(chan struct{})
				engine.waitCanceled = make(chan struct{})
				engine.cancelRequestOnWait = cancelRequest
			},
			wantErr: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycleParent := context.WithValue(context.Background(), valueKey, "lifecycle-value")
			lifecycleCtx, cancelLifecycle := context.WithCancel(lifecycleParent)
			cancelLifecycle()
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			engine := &recordingEngine{removeContextValueKey: valueKey}
			if test.configure != nil {
				test.configure(engine, cancelRequest)
			}
			verifier, err := NewDockerHostPathVerifier(
				lifecycleCtx,
				engine,
				"switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			)
			if err != nil {
				t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
			}

			err = verifier.VerifyHostPaths(requestCtx, "/host/switchback-spike", []HostPathRequirement{{
				Path: "/host/switchback-spike/sessions/session-1/working-copy",
				Kind: HostPathDirectory,
			}})
			if test.wantErr == nil && err != nil {
				t.Fatalf("VerifyHostPaths() error = %v, want success", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("VerifyHostPaths() error = %v, want errors.Is(%v)", err, test.wantErr)
			}
			if got := len(engine.removeContexts); got != 1 {
				t.Fatalf("attestor remove contexts = %d, want 1", got)
			}
			observation := engine.removeContexts[0]
			if observation.err != nil {
				t.Fatalf("attestor remove context error = %v, want cleanup detached from lifecycle cancellation", observation.err)
			}
			if !observation.hasDeadline {
				t.Fatal("attestor remove context has no deadline, want bounded cleanup")
			}
			remaining := time.Until(observation.deadline)
			if remaining <= 0 || remaining > dockerHostPathCleanupTimeout {
				t.Fatalf("attestor remove deadline remaining = %v, want within (0, %v]", remaining, dockerHostPathCleanupTimeout)
			}
			if got, want := observation.value, "lifecycle-value"; got != want {
				t.Fatalf("attestor remove context value = %#v, want preserved lifecycle value %#v", got, want)
			}
		})
	}
}

func TestDockerHostPathVerifierCleansUpEveryPostCreateExit(t *testing.T) {
	startErr := errors.New("injected attestor start failure")
	waitErr := errors.New("injected attestor wait failure")
	tests := []struct {
		name           string
		configure      func(*recordingEngine)
		wantUnderlying error
		wantError      bool
	}{
		{name: "success"},
		{
			name: "start error",
			configure: func(engine *recordingEngine) {
				engine.startErr = startErr
			},
			wantUnderlying: startErr,
			wantError:      true,
		},
		{
			name: "wait error",
			configure: func(engine *recordingEngine) {
				engine.waitErr = waitErr
			},
			wantUnderlying: waitErr,
			wantError:      true,
		},
		{
			name: "rejected status",
			configure: func(engine *recordingEngine) {
				engine.waitStatusCode = 23
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := &recordingEngine{}
			if test.configure != nil {
				test.configure(engine)
			}
			verifier, err := NewDockerHostPathVerifier(
				context.Background(),
				engine,
				"switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			)
			if err != nil {
				t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
			}
			err = verifier.VerifyHostPaths(context.Background(), "/host/switchback-spike", []HostPathRequirement{{
				Path: "/host/switchback-spike/sessions/session-1/working-copy",
				Kind: HostPathDirectory,
			}})
			if test.wantError && err == nil {
				t.Fatal("VerifyHostPaths() error = nil, want failure")
			}
			if !test.wantError && err != nil {
				t.Fatalf("VerifyHostPaths() error = %v", err)
			}
			if test.wantUnderlying != nil && !errors.Is(err, test.wantUnderlying) {
				t.Fatalf("VerifyHostPaths() error = %v, want errors.Is(%v)", err, test.wantUnderlying)
			}
			if got, want := engine.removed, []string{"container-1"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("attestor removals = %v, want %v", got, want)
			}
			if got := len(engine.removeContexts); got != 1 {
				t.Fatalf("attestor remove contexts = %d, want 1", got)
			}
			observation := engine.removeContexts[0]
			if observation.err != nil || !observation.hasDeadline {
				t.Fatalf("attestor remove context = %#v, want active bounded lifecycle context", observation)
			}
		})
	}
}

func TestDockerHostPathVerifierRequiresLifecycleContext(t *testing.T) {
	if _, err := NewDockerHostPathVerifier(
		nil,
		&recordingEngine{},
		"switchback-spike-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111",
	); err == nil {
		t.Fatal("NewDockerHostPathVerifier() error = nil, want missing lifecycle context rejection")
	}
}

func TestDockerRejectsUnsafeServerOwnedConfiguration(t *testing.T) {
	_, valid := dockerTestRequest(t)
	tests := []struct {
		name   string
		mutate func(*DockerOptions)
	}{
		{name: "empty image", mutate: func(options *DockerOptions) { options.Image = "" }},
		{name: "image whitespace", mutate: func(options *DockerOptions) { options.Image += "\nforeign" }},
		{name: "host network", mutate: func(options *DockerOptions) { options.Network = "host" }},
		{name: "container network", mutate: func(options *DockerOptions) { options.Network = "container:foreign" }},
		{name: "host filesystem root", mutate: func(options *DockerOptions) { options.Mapper.HostDataRoot = "/" }},
		{name: "backend filesystem root", mutate: func(options *DockerOptions) { options.Mapper.ContainerDataRoot = "/" }},
		{name: "relative fixture", mutate: func(options *DockerOptions) { options.FixturePath = "fixture" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if _, err := NewDockerProvisioner(&recordingEngine{}, options); err == nil {
				t.Fatal("NewDockerProvisioner() error = nil, want fail-closed configuration rejection")
			}
		})
	}
}

func TestDockerRequiresHostPathVerifier(t *testing.T) {
	tests := []struct {
		name     string
		verifier HostPathVerifier
	}{
		{name: "nil interface"},
		{name: "typed nil", verifier: (*recordingHostPathVerifier)(nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, options := dockerTestRequest(t)
			options.HostPathVerifier = test.verifier
			if _, err := NewDockerProvisioner(&recordingEngine{}, options); err == nil {
				t.Fatal("NewDockerProvisioner() error = nil, want missing HostPathVerifier rejection")
			}
		})
	}
}

func TestDockerAcceptsServerDerivedSessionModelSecret(t *testing.T) {
	request, options := dockerTestRequest(t)
	options.HostPathVerifier = &recordingHostPathVerifier{}
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if _, err := provisioner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	var mountedSecrets *mount.Mount
	for index := range engine.created.HostConfig.Mounts {
		candidate := &engine.created.HostConfig.Mounts[index]
		if candidate.Target == WorkerSecretsPath {
			mountedSecrets = candidate
			break
		}
	}
	if mountedSecrets == nil {
		t.Fatal("runtime secret volume mount is missing")
	}
	if mountedSecrets.Type != mount.TypeVolume || mountedSecrets.Source != "switchback-runtime-secrets-runtime-1-g1" || !mountedSecrets.ReadOnly {
		t.Fatalf("runtime secret mount = %#v, want read-only named volume", *mountedSecrets)
	}
}

func TestDockerVerifiesExactDualRootHostPathsBeforeEngineCreate(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	verifier := &recordingHostPathVerifier{
		beforeVerify: func() {
			engine.calls = append(engine.calls, "verify-host-paths")
		},
	}
	options.HostPathVerifier = verifier
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if _, err := provisioner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if verifier.calls != 2 {
		t.Fatalf("HostPathVerifier calls = %d, want source staging and Worker bind verification", verifier.calls)
	}
	if verifier.root != options.Mapper.HostDataRoot {
		t.Fatalf("HostPathVerifier root = %q, want %q", verifier.root, options.Mapper.HostDataRoot)
	}
	wantStaging := []HostPathRequirement{
		{Path: request.WorkingCopy.HostPath, Kind: HostPathDirectory},
		{Path: request.AgentState.HostPath, Kind: HostPathDirectory},
		{Path: request.TraceSpool.HostPath, Kind: HostPathDirectory},
		{Path: request.Bootstrap.HostPath, Kind: HostPathRegularFile},
		{Path: request.ModelSecret.HostPath, Kind: HostPathRegularFile},
	}
	wantWorker := wantStaging[:3]
	if got := verifier.requirementSets; len(got) != 2 ||
		!reflect.DeepEqual(got[0], wantStaging) ||
		!reflect.DeepEqual(got[1], wantWorker) {
		t.Fatalf("HostPathVerifier requirements = %#v, want staging %#v then Worker %#v", got, wantStaging, wantWorker)
	}
	for index, requirement := range verifier.requirements {
		if requirement.Path == request.WorkingCopy.ContainerPath {
			t.Fatalf("HostPathVerifier requirement %d received Backend path %q", index, requirement.Path)
		}
	}
	wantCalls := []string{
		"inspect:switchback-runtime-runtime-1",
		"verify-host-paths",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
		"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
		"volume-create:switchback-runtime-secrets-runtime-1-g1",
		"create:runtime-secret-populator",
		"start:container-1",
		"wait:container-1",
		"remove:container-1",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
		"verify-host-paths",
		"create:worker-runtime",
		"start:container-2",
	}
	if !reflect.DeepEqual(engine.calls, wantCalls) {
		t.Fatalf("Engine call order = %#v, want probe immediately before create %#v", engine.calls, wantCalls)
	}
}

func TestLocalHostPathVerifierAcceptsCanonicalPathsBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "sessions", "session-1", "working-copy")
	file := filepath.Join(root, "sessions", "session-1", "bootstrap.json")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("bootstrap"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := (LocalHostPathVerifier{}).VerifyHostPaths(context.Background(), root, []HostPathRequirement{
		{Path: directory, Kind: HostPathDirectory},
		{Path: file, Kind: HostPathRegularFile},
	})
	if err != nil {
		t.Fatalf("VerifyHostPaths() error = %v", err)
	}
}

func TestLocalHostPathVerifierRejectsUnsafePathsAndTypes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) HostPathRequirement
	}{
		{
			name: "nonexistent final path",
			setup: func(_ *testing.T, root string) HostPathRequirement {
				return HostPathRequirement{
					Path: filepath.Join(root, "sessions", "missing", "working-copy"),
					Kind: HostPathDirectory,
				}
			},
		},
		{
			name: "path outside root",
			setup: func(t *testing.T, _ string) HostPathRequirement {
				outside := t.TempDir()
				return HostPathRequirement{Path: outside, Kind: HostPathDirectory}
			},
		},
		{
			name: "lexical traversal",
			setup: func(t *testing.T, root string) HostPathRequirement {
				path := root + string(filepath.Separator) + "sessions" + string(filepath.Separator) + ".." + string(filepath.Separator) + "outside"
				if err := os.MkdirAll(filepath.Clean(path), 0o700); err != nil {
					t.Fatal(err)
				}
				return HostPathRequirement{Path: path, Kind: HostPathDirectory}
			},
		},
		{
			name: "symlink ancestor below root",
			setup: func(t *testing.T, root string) HostPathRequirement {
				target := t.TempDir()
				if err := os.WriteFile(filepath.Join(target, "bootstrap.json"), []byte("bootstrap"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "sessions")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return HostPathRequirement{
					Path: filepath.Join(link, "bootstrap.json"),
					Kind: HostPathRegularFile,
				}
			},
		},
		{
			name: "symlink root",
			setup: func(t *testing.T, root string) HostPathRequirement {
				target := filepath.Join(root, "target")
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-link")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(link) })
				return HostPathRequirement{Path: filepath.Join(link, "target"), Kind: HostPathDirectory}
			},
		},
		{
			name: "file where directory required",
			setup: func(t *testing.T, root string) HostPathRequirement {
				path := filepath.Join(root, "sessions", "session-1", "working-copy")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				return HostPathRequirement{Path: path, Kind: HostPathDirectory}
			},
		},
		{
			name: "directory where regular file required",
			setup: func(t *testing.T, root string) HostPathRequirement {
				path := filepath.Join(root, "sessions", "session-1", "bootstrap.json")
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return HostPathRequirement{Path: path, Kind: HostPathRegularFile}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			requirement := test.setup(t, root)
			verifyRoot := root
			if test.name == "symlink root" {
				verifyRoot = filepath.Dir(requirement.Path)
			}
			if err := (LocalHostPathVerifier{}).VerifyHostPaths(
				context.Background(),
				verifyRoot,
				[]HostPathRequirement{requirement},
			); err == nil {
				t.Fatal("VerifyHostPaths() error = nil, want fail-closed rejection")
			}
		})
	}
}

func TestDockerRejectsAnArbitraryPathOverrideBeforeCallingTheEngine(t *testing.T) {
	request, options := dockerTestRequest(t)
	request.WorkingCopy.HostPath = t.TempDir()
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if _, err := provisioner.Start(context.Background(), request); err == nil {
		t.Fatal("Start() error = nil, want arbitrary path rejection")
	}
	if engine.createCalls != 0 {
		t.Fatalf("engine create calls = %d, want 0", engine.createCalls)
	}
}

func TestProvisionRequestDoesNotStringifyWorkerTokenOrPaths(t *testing.T) {
	request, _ := dockerTestRequest(t)
	for _, formatted := range []string{
		request.String(),
		request.GoString(),
	} {
		for _, protected := range []string{
			request.WorkerToken,
			request.WorkingCopy.ContainerPath,
			request.WorkingCopy.HostPath,
			request.Bootstrap.ContainerPath,
			request.ModelSecret.HostPath,
		} {
			if strings.Contains(formatted, protected) {
				t.Fatalf("ProvisionRequest formatting exposed %q", protected)
			}
		}
	}
}

func TestDockerStartReturnsTheExistingExactContainer(t *testing.T) {
	request, options := dockerTestRequest(t)
	verifier := &recordingHostPathVerifier{}
	options.HostPathVerifier = verifier
	engine := &recordingEngine{
		inspectResult:       inspectedSecureWorker("existing-container", request, options, true),
		volumeInspectResult: inspectedSecretVolume(request.Identity),
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	instance, err := provisioner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := instance, (Instance{ContainerID: "existing-container", ContainerName: "switchback-runtime-runtime-1"}); got != want {
		t.Fatalf("Start() = %#v, want %#v", got, want)
	}
	if engine.createCalls != 0 || len(engine.started) != 0 {
		t.Fatalf("idempotent Start created/started container: creates=%d starts=%v", engine.createCalls, engine.started)
	}
	if verifier.calls != 0 {
		t.Fatalf("HostPathVerifier calls = %d, want no new mount probe for running exact identity", verifier.calls)
	}
	if got, want := engine.calls, []string{
		"inspect:switchback-runtime-runtime-1",
		"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Engine calls = %#v, want exact Worker and volume identity checks %#v", got, want)
	}
	for _, transient := range []string{request.Bootstrap.ContainerPath, request.ModelSecret.ContainerPath} {
		if _, err := os.Lstat(transient); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transient source %q still exists after idempotent Start: %v", transient, err)
		}
	}
}

func TestDockerStartRestartsStoppedExactContainerWithoutRepopulatingItsAuthoritativeVolume(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectResult:       inspectedSecureWorker("existing-container", request, options, false),
		volumeInspectResult: inspectedSecretVolume(request.Identity),
	}
	verifier := &recordingHostPathVerifier{beforeVerify: func() {
		engine.calls = append(engine.calls, "verify-host-paths")
	}}
	options.HostPathVerifier = verifier
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if _, err := provisioner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	wantCalls := []string{
		"inspect:switchback-runtime-runtime-1",
		"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
		"verify-host-paths",
		"start:existing-container",
	}
	if !reflect.DeepEqual(engine.calls, wantCalls) {
		t.Fatalf("Engine call order = %#v, want authoritative volume preserved and Worker binds re-probed %#v", engine.calls, wantCalls)
	}
	wantWorkerBinds := []HostPathRequirement{
		{Path: request.WorkingCopy.HostPath, Kind: HostPathDirectory},
		{Path: request.AgentState.HostPath, Kind: HostPathDirectory},
		{Path: request.TraceSpool.HostPath, Kind: HostPathDirectory},
	}
	if got := verifier.requirementSets; len(got) != 1 ||
		!reflect.DeepEqual(got[0], wantWorkerBinds) {
		t.Fatalf("HostPathVerifier requirements = %#v, want only Worker binds", got)
	}
	if countManagedCreates(engine.creates, ManagedSecretPopulator) != 0 {
		t.Fatalf("stopped existing Worker repopulated its authoritative volume: creates=%#v", engine.creates)
	}
	for _, transient := range []string{request.Bootstrap.ContainerPath, request.ModelSecret.ContainerPath} {
		if _, err := os.Lstat(transient); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unused transient source %q still exists after restart: %v", transient, err)
		}
	}
}

func TestDockerStartRejectsExistingWorkerWithoutExactSecretVolumeMount(t *testing.T) {
	request, options := dockerTestRequest(t)
	legacy := inspectedSecureWorker("existing-container", request, options, true)
	legacy.Container.Mounts = []container.MountPoint{{
		Type:        mount.TypeBind,
		Source:      request.ModelSecret.HostPath,
		Destination: WorkerModelSecretPath,
		RW:          false,
	}}
	engine := &recordingEngine{
		inspectResult:       legacy,
		volumeInspectResult: inspectedSecretVolume(request.Identity),
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want legacy Worker mount rejection", err)
	}
	if len(engine.started) != 0 || engine.createCalls != 0 {
		t.Fatalf("legacy Worker was changed: starts=%#v creates=%d", engine.started, engine.createCalls)
	}
}

func TestDockerStartRejectsExistingWorkerWithoutExactImmutableSpec(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*client.ContainerInspectResult, ProvisionRequest, DockerOptions)
	}{
		{
			name: "missing working copy bind",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts = inspected.Container.Mounts[1:]
			},
		},
		{
			name: "wrong bind source",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts[0].Source = "/tmp/foreign-working-copy"
			},
		},
		{
			name: "shared bind propagation",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Mounts[0].BindOptions = &mount.BindOptions{
					Propagation: mount.PropagationRShared,
				}
			},
		},
		{
			name: "wrong bind target",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts[0].Destination = "/foreign-workspace"
			},
		},
		{
			name: "read only working copy bind",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts[0].RW = false
			},
		},
		{
			name: "extra Docker socket",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts = append(inspected.Container.Mounts, container.MountPoint{
					Type:        mount.TypeBind,
					Source:      "/var/run/docker.sock",
					Destination: "/var/run/docker.sock",
					RW:          true,
				})
			},
		},
		{
			name: "extra host credential",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts = append(inspected.Container.Mounts, container.MountPoint{
					Type:        mount.TypeBind,
					Source:      "/home/backend/.config/gh/hosts.yml",
					Destination: "/root/.config/gh/hosts.yml",
					RW:          false,
				})
			},
		},
		{
			name: "second writable volume",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Mounts = append(inspected.Container.Mounts, container.MountPoint{
					Type:        mount.TypeVolume,
					Name:        "foreign-secrets",
					Source:      "/var/lib/docker/volumes/foreign-secrets/_data",
					Destination: "/run/foreign-secrets",
					RW:          true,
				})
			},
		},
		{
			name: "wrong image",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.Image = "foreign-worker@sha256:" + strings.Repeat("2", 64)
			},
		},
		{
			name: "wrong user",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.User = "10002:10002"
			},
		},
		{
			name: "wrong entrypoint",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.Entrypoint = []string{"/bin/sh"}
			},
		},
		{
			name: "wrong command",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.Cmd = []string{"-c", "cat /run/switchback-secrets/model"}
			},
		},
		{
			name: "wrong working directory",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.WorkingDir = "/tmp"
			},
		},
		{
			name: "extra environment",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.Env = append(inspected.Container.Config.Env, "HOST_CREDENTIAL=foreign")
			},
		},
		{
			name: "wrong server owned environment value",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.Env[1] = "SWITCHBACK_WORKER_TOKEN=foreign"
			},
		},
		{
			name: "wrong network",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.NetworkMode = container.NetworkMode("host")
			},
		},
		{
			name: "missing network settings",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.NetworkSettings = nil
			},
		},
		{
			name: "missing expected network attachment",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				delete(inspected.Container.NetworkSettings.Networks, options.Network)
			},
		},
		{
			name: "incomplete expected network attachment",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				inspected.Container.NetworkSettings.Networks[options.Network] = nil
			},
		},
		{
			name: "extra network attachment",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.NetworkSettings.Networks["foreign-private-network"] = &dockernetwork.EndpointSettings{}
			},
		},
		{
			name: "user configured endpoint IPAM",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				inspected.Container.NetworkSettings.Networks[options.Network].IPAMConfig = &dockernetwork.EndpointIPAMConfig{
					IPv4Address: netip.MustParseAddr("10.0.0.8"),
				}
			},
		},
		{
			name: "user configured endpoint alias",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				inspected.Container.NetworkSettings.Networks[options.Network].Aliases = []string{"database"}
			},
		},
		{
			name: "user configured endpoint link",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				inspected.Container.NetworkSettings.Networks[options.Network].Links = []string{"foreign:database"}
			},
		},
		{
			name: "user configured endpoint driver option",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				inspected.Container.NetworkSettings.Networks[options.Network].DriverOpts = map[string]string{
					"com.docker.network.endpoint.exposedports": "8080/tcp",
				}
			},
		},
		{
			name: "user configured endpoint gateway priority",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, options DockerOptions) {
				inspected.Container.NetworkSettings.Networks[options.Network].GwPriority = 1
			},
		},
		{
			name: "network published port",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				port := dockernetwork.MustParsePort("8080/tcp")
				inspected.Container.NetworkSettings.Ports = dockernetwork.PortMap{
					port: []dockernetwork.PortBinding{{HostPort: "18080"}},
				}
			},
		},
		{
			name: "host PID namespace",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.PidMode = container.PidMode("host")
			},
		},
		{
			name: "host IPC namespace",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.IpcMode = container.IPCModeHost
			},
		},
		{
			name: "host UTS namespace",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.UTSMode = container.UTSMode("host")
			},
		},
		{
			name: "host user namespace",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.UsernsMode = container.UsernsMode("host")
			},
		},
		{
			name: "host cgroup namespace",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.CgroupnsMode = container.CgroupnsModeHost
			},
		},
		{
			name: "extra hosts",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.ExtraHosts = []string{"metadata.internal:169.254.169.254"}
			},
		},
		{
			name: "volumes from",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.VolumesFrom = []string{"foreign-container:rw"}
			},
		},
		{
			name: "links",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Links = []string{"foreign-container:database"}
			},
		},
		{
			name: "custom DNS",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.DNS = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
			},
		},
		{
			name: "custom DNS options",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.DNSOptions = []string{"use-vc"}
			},
		},
		{
			name: "custom DNS search",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.DNSSearch = []string{"foreign.internal"}
			},
		},
		{
			name: "restart policy",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.RestartPolicy = container.RestartPolicy{
					Name: container.RestartPolicyAlways,
				}
			},
		},
		{
			name: "sysctl",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Sysctls = map[string]string{"net.ipv4.ip_forward": "1"}
			},
		},
		{
			name: "device",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Devices = []container.DeviceMapping{{
					PathOnHost:        "/dev/null",
					PathInContainer:   "/dev/foreign",
					CgroupPermissions: "rwm",
				}}
			},
		},
		{
			name: "device request",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.DeviceRequests = []container.DeviceRequest{{
					Driver:       "foreign",
					Count:        1,
					Capabilities: [][]string{{"gpu"}},
				}}
			},
		},
		{
			name: "tmpfs",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Tmpfs = map[string]string{"/run/foreign": "rw"}
			},
		},
		{
			name: "additional group",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.GroupAdd = []string{"docker"}
			},
		},
		{
			name: "foreign runtime",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Runtime = "foreign-runtime"
			},
		},
		{
			name: "auto remove",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.AutoRemove = true
			},
		},
		{
			name: "daemon init",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				enabled := true
				inspected.Container.HostConfig.Init = &enabled
			},
		},
		{
			name: "foreign logging",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.LogConfig = container.LogConfig{Type: "syslog"}
			},
		},
		{
			name: "privileged",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Privileged = true
			},
		},
		{
			name: "publish all ports",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.PublishAllPorts = true
			},
		},
		{
			name: "host port binding",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				port := dockernetwork.MustParsePort("8080/tcp")
				inspected.Container.HostConfig.PortBindings = dockernetwork.PortMap{
					port: []dockernetwork.PortBinding{{HostPort: "18080"}},
				}
			},
		},
		{
			name: "wrong capabilities",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.CapAdd = append(inspected.Container.HostConfig.CapAdd, "SYS_ADMIN")
			},
		},
		{
			name: "wrong security options",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.SecurityOpt = nil
			},
		},
		{
			name: "wrong resources",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.HostConfig.Memory = 8 << 30
			},
		},
		{
			name: "extra identity label",
			mutate: func(inspected *client.ContainerInspectResult, _ ProvisionRequest, _ DockerOptions) {
				inspected.Container.Config.Labels["foreign-owner"] = "true"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			inspected := inspectedSecureWorker("existing-container", request, options, true)
			test.mutate(&inspected, request, options)
			engine := &recordingEngine{
				inspectResult:       inspected,
				volumeInspectResult: inspectedSecretVolume(request.Identity),
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Start() error = %v, want immutable Worker spec rejection", err)
			}
			if len(engine.started) != 0 || engine.createCalls != 0 {
				t.Fatalf("mismatched Worker was changed: starts=%#v creates=%d", engine.started, engine.createCalls)
			}
		})
	}
}

func TestDockerStartRejectsExistingWorkerWithUnsafeSecretVolumeBackingIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*volume.Volume)
	}{
		{
			name: "local driver bind options",
			mutate: func(inspected *volume.Volume) {
				inspected.Options = map[string]string{
					"type":   "none",
					"o":      "bind",
					"device": "/test-owned/foreign-secret-source",
				}
			},
		},
		{
			name: "global scope",
			mutate: func(inspected *volume.Volume) {
				inspected.Scope = "global"
			},
		},
		{
			name: "cluster volume",
			mutate: func(inspected *volume.Volume) {
				inspected.ClusterVolume = &volume.ClusterVolume{ID: "foreign-cluster-volume"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			inspectedVolume := inspectedSecretVolume(request.Identity)
			test.mutate(&inspectedVolume.Volume)
			engine := &recordingEngine{
				inspectResult:       inspectedSecureWorker("existing-container", request, options, false),
				volumeInspectResult: inspectedVolume,
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Start() error = %v, want unsafe secret volume backing rejection", err)
			}
			if len(engine.started) != 0 || engine.createCalls != 0 || len(engine.volumeRemoves) != 0 {
				t.Fatalf(
					"unsafe existing volume was adopted or changed: starts=%#v creates=%d volume-removes=%#v",
					engine.started,
					engine.createCalls,
					engine.volumeRemoves,
				)
			}
		})
	}
}

func TestDockerStartRejectsFreshSecretVolumeWithUnsafeBackingIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*volume.Volume)
	}{
		{
			name: "local driver bind options",
			mutate: func(inspected *volume.Volume) {
				inspected.Options = map[string]string{
					"type":   "none",
					"o":      "bind",
					"device": "/test-owned/foreign-secret-source",
				}
			},
		},
		{
			name: "global scope",
			mutate: func(inspected *volume.Volume) {
				inspected.Scope = "global"
			},
		},
		{
			name: "cluster volume",
			mutate: func(inspected *volume.Volume) {
				inspected.ClusterVolume = &volume.ClusterVolume{ID: "foreign-cluster-volume"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			createdVolume := inspectedSecretVolume(request.Identity)
			test.mutate(&createdVolume.Volume)
			engine := &recordingEngine{
				inspectErr:         cerrdefs.ErrNotFound,
				volumeCreateResult: client.VolumeCreateResult{Volume: createdVolume.Volume},
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Start() error = %v, want unsafe fresh volume rejection", err)
			}
			if engine.createCalls != 0 || len(engine.started) != 0 {
				t.Fatalf(
					"unsafe fresh volume reached container staging: creates=%d starts=%#v",
					engine.createCalls,
					engine.started,
				)
			}
		})
	}
}

func TestDockerStartRejectsUnsafeSecretVolumeBackingOnRollbackReinspection(t *testing.T) {
	request, options := dockerTestRequest(t)
	unsafe := inspectedSecretVolume(request.Identity)
	unsafe.Volume.Options = map[string]string{
		"type":   "none",
		"o":      "bind",
		"device": "/test-owned/replaced-secret-source",
	}
	workerCreateFailure := errors.New("injected Worker create failure")
	engine := &recordingEngine{
		inspectErr: cerrdefs.ErrNotFound,
		volumeInspectSequence: []volumeInspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: unsafe},
		},
		createErrSequence: []error{nil, workerCreateFailure},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, workerCreateFailure) || !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want create failure joined with unsafe rollback identity", err)
	}
	if len(engine.volumeRemoves) != 0 {
		t.Fatalf("rollback removed replaced unsafe volume: %#v", engine.volumeRemoves)
	}
}

func TestDockerStartAllowsSecretVolumeOperationalMetadata(t *testing.T) {
	request, options := dockerTestRequest(t)
	inspectedVolume := inspectedSecretVolume(request.Identity)
	inspectedVolume.Volume.Mountpoint = "/var/lib/docker/volumes/test-owned/_data"
	inspectedVolume.Volume.CreatedAt = "2026-07-20T00:00:00Z"
	inspectedVolume.Volume.Status = map[string]any{"daemon": "operational"}
	inspectedVolume.Volume.UsageData = &volume.UsageData{RefCount: 1, Size: 128}
	engine := &recordingEngine{
		inspectResult:       inspectedSecureWorker("existing-container", request, options, true),
		volumeInspectResult: inspectedVolume,
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	instance, err := provisioner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if instance.ContainerID != "existing-container" {
		t.Fatalf("Start() ContainerID = %q, want existing-container", instance.ContainerID)
	}
}

func TestDockerStartRejectsConflictingRuntimeSecretVolumeLabels(t *testing.T) {
	request, options := dockerTestRequest(t)
	conflicting := request.Identity
	conflicting.WorkerRunID = "foreign-run"
	engine := &recordingEngine{
		inspectErr:          cerrdefs.ErrNotFound,
		volumeInspectResult: inspectedSecretVolume(conflicting),
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want volume identity conflict", err)
	}
	if engine.createCalls != 0 || len(engine.volumeCreates) != 0 ||
		len(engine.volumeRemoves) != 0 || len(engine.started) != 0 {
		t.Fatalf(
			"conflicting volume changed resources: container creates=%d volume creates=%d volume removes=%d starts=%d",
			engine.createCalls,
			len(engine.volumeCreates),
			len(engine.volumeRemoves),
			len(engine.started),
		)
	}
}

func TestDockerStartReplacesExactOrphanSecretVolumeBeforeFreshStaging(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectErr: cerrdefs.ErrNotFound,
		volumeInspectSequence: []volumeInspectCallResult{
			{result: inspectedSecretVolume(request.Identity)},
			{result: inspectedSecretVolume(request.Identity)},
			{err: cerrdefs.ErrNotFound},
		},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if _, err := provisioner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	volumeName := secretVolumeName(request.Identity)
	if got, want := engine.volumeRemoves, []string{volumeName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("orphan volume removals = %#v, want %#v", got, want)
	}
	if got, want := len(engine.volumeCreates), 1; got != want {
		t.Fatalf("fresh volume creates = %d, want %d", got, want)
	}
	removeIndex := indexString(engine.calls, "volume-remove:"+volumeName)
	createIndex := indexString(engine.calls, "volume-create:"+volumeName)
	if removeIndex < 0 || createIndex < 0 || removeIndex > createIndex {
		t.Fatalf("orphan volume was not removed before fresh create: calls=%#v", engine.calls)
	}
}

func TestDockerStartFailsClosedWhileReconcilingExactOrphanSecretVolume(t *testing.T) {
	injected := errors.New("injected orphan volume removal failure")
	conflictingIdentity := RuntimeIdentity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "replacement-run",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
	tests := []struct {
		name                  string
		volumeInspectSequence func(ProvisionRequest) []volumeInspectCallResult
		volumeRemoveSequence  []error
		wantCause             error
	}{
		{
			name: "remove failure",
			volumeInspectSequence: func(request ProvisionRequest) []volumeInspectCallResult {
				return []volumeInspectCallResult{
					{result: inspectedSecretVolume(request.Identity)},
					{result: inspectedSecretVolume(request.Identity)},
				}
			},
			volumeRemoveSequence: []error{injected},
			wantCause:            injected,
		},
		{
			name: "remove residue",
			volumeInspectSequence: func(request ProvisionRequest) []volumeInspectCallResult {
				return []volumeInspectCallResult{
					{result: inspectedSecretVolume(request.Identity)},
					{result: inspectedSecretVolume(request.Identity)},
					{result: inspectedSecretVolume(request.Identity)},
				}
			},
			volumeRemoveSequence: []error{nil},
			wantCause:            ErrRuntimeIdentityConflict,
		},
		{
			name: "label replacement before remove",
			volumeInspectSequence: func(request ProvisionRequest) []volumeInspectCallResult {
				return []volumeInspectCallResult{
					{result: inspectedSecretVolume(request.Identity)},
					{result: inspectedSecretVolume(conflictingIdentity)},
				}
			},
			wantCause: ErrRuntimeIdentityConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			engine := &recordingEngine{
				inspectErr:               cerrdefs.ErrNotFound,
				volumeInspectSequence:    test.volumeInspectSequence(request),
				volumeRemoveSequence:     test.volumeRemoveSequence,
				volumeInspectResult:      inspectedSecretVolume(request.Identity),
				populatorInspectSequence: []inspectCallResult{{err: cerrdefs.ErrNotFound}},
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if !errors.Is(err, test.wantCause) {
				t.Fatalf("Start() error = %v, want errors.Is(%v)", err, test.wantCause)
			}
			if engine.createCalls != 0 || len(engine.volumeCreates) != 0 || len(engine.started) != 0 {
				t.Fatalf(
					"orphan reconciliation failure created runtime resources: creates=%d volumes=%d starts=%#v",
					engine.createCalls,
					len(engine.volumeCreates),
					engine.started,
				)
			}
			if test.name == "label replacement before remove" && len(engine.volumeRemoves) != 0 {
				t.Fatalf("label-replaced orphan volume was removed: %#v", engine.volumeRemoves)
			}
		})
	}
}

func TestDockerStartStoppedExistingWorkerFailurePreservesOrConvergesThreeResources(t *testing.T) {
	injected := errors.New("injected existing Worker failure")
	tests := []struct {
		name              string
		configure         func(*testing.T, ProvisionRequest, *DockerOptions, *recordingEngine)
		wantWorkerRemoved bool
		wantVolumeRemoved bool
	}{
		{
			name: "source removal",
			configure: func(t *testing.T, request ProvisionRequest, _ *DockerOptions, _ *recordingEngine) {
				if err := os.Remove(request.Bootstrap.ContainerPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(request.Bootstrap.ContainerPath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "bind reprobe",
			configure: func(_ *testing.T, _ ProvisionRequest, options *DockerOptions, _ *recordingEngine) {
				options.HostPathVerifier = &recordingHostPathVerifier{err: injected}
			},
		},
		{
			name: "Worker restart",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.startErr = injected
			},
			wantWorkerRemoved: true,
			wantVolumeRemoved: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			engine := &recordingEngine{
				inspectResult:       inspectedSecureWorker("existing-container", request, options, false),
				volumeInspectResult: inspectedSecretVolume(request.Identity),
			}
			test.configure(t, request, &options, engine)
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if err == nil {
				t.Fatal("Start() error = nil, want stopped existing Worker failure")
			}
			if countManagedCreates(engine.creates, ManagedSecretPopulator) != 0 {
				t.Fatalf("existing Worker failure created a populator: creates=%#v", engine.creates)
			}
			if got := containsString(engine.removed, "existing-container"); got != test.wantWorkerRemoved {
				t.Fatalf("existing Worker removed = %v, want %v; removals=%#v", got, test.wantWorkerRemoved, engine.removed)
			}
			if got := len(engine.volumeRemoves) != 0; got != test.wantVolumeRemoved {
				t.Fatalf("authoritative volume removed = %v, want %v; removals=%#v", got, test.wantVolumeRemoved, engine.volumeRemoves)
			}
			if !test.wantWorkerRemoved {
				if engine.inspectResult.Container.ID != "existing-container" {
					t.Fatalf("existing Worker state changed after pre-start failure: %#v", engine.inspectResult.Container)
				}
				if engine.volumeInspectResult.Volume.Name != secretVolumeName(request.Identity) {
					t.Fatalf("authoritative volume state changed after pre-start failure: %#v", engine.volumeInspectResult.Volume)
				}
			}
		})
	}
}

func TestDockerStartFailsClosedOnVolumeCreateConflict(t *testing.T) {
	for _, exact := range []bool{true, false} {
		t.Run(fmt.Sprintf("exact=%v", exact), func(t *testing.T) {
			request, options := dockerTestRequest(t)
			conflicting := request.Identity
			if !exact {
				conflicting.WorkerRunID = "foreign-run"
			}
			engine := &recordingEngine{
				inspectErr:      cerrdefs.ErrNotFound,
				volumeCreateErr: cerrdefs.ErrConflict,
				volumeInspectSequence: []volumeInspectCallResult{
					{err: cerrdefs.ErrNotFound},
					{result: inspectedSecretVolume(conflicting)},
				},
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Start() error = %v, want fail-closed volume create conflict", err)
			}
			if engine.createCalls != 0 || len(engine.started) != 0 || len(engine.volumeRemoves) != 0 {
				t.Fatalf(
					"volume conflict changed resources: creates=%d starts=%#v volume-removes=%#v",
					engine.createCalls,
					engine.started,
					engine.volumeRemoves,
				)
			}
			if got := countString(engine.calls, "volume-inspect:switchback-runtime-secrets-runtime-1-g1"); got != 2 {
				t.Fatalf("volume inspect calls = %d, want initial miss plus conflict identity inspection", got)
			}
		})
	}
}

func TestDockerStartReconcilesStaleExactSecretPopulatorBeforeStaging(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			request, options := dockerTestRequest(t)
			engine := &recordingEngine{
				inspectErr: cerrdefs.ErrNotFound,
				populatorInspectSequence: []inspectCallResult{
					{result: inspectedSecretPopulator("stale-populator", request, options, running)},
					{err: cerrdefs.ErrNotFound},
					{err: cerrdefs.ErrNotFound},
				},
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			if _, err := provisioner.Start(context.Background(), request); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if !containsString(engine.removed, "stale-populator") {
				t.Fatalf("stale populator was not removed before staging: removals=%#v", engine.removed)
			}
			if got := indexString(engine.calls, "remove:stale-populator"); got < 0 {
				t.Fatalf("stale populator removal missing from calls %#v", engine.calls)
			} else if create := indexString(engine.calls, "create:"+ManagedSecretPopulator); create < 0 || got > create {
				t.Fatalf("stale populator was not removed before create: calls=%#v", engine.calls)
			}
		})
	}
}

func TestDockerStartReconcilesSecretPopulatorCreateConflictFailClosed(t *testing.T) {
	tests := []struct {
		name        string
		inspected   func(ProvisionRequest, DockerOptions) client.ContainerInspectResult
		wantSuccess bool
	}{
		{
			name: "exact stopped stale",
			inspected: func(request ProvisionRequest, options DockerOptions) client.ContainerInspectResult {
				return inspectedSecretPopulator("stale-populator", request, options, false)
			},
			wantSuccess: true,
		},
		{
			name: "exact active concurrent owner",
			inspected: func(request ProvisionRequest, options DockerOptions) client.ContainerInspectResult {
				return inspectedSecretPopulator("active-populator", request, options, true)
			},
		},
		{
			name: "mismatched immutable spec",
			inspected: func(request ProvisionRequest, options DockerOptions) client.ContainerInspectResult {
				inspected := inspectedSecretPopulator("foreign-populator", request, options, false)
				inspected.Container.HostConfig.NetworkMode = container.NetworkMode(options.Network)
				return inspected
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			conflicting := test.inspected(request, options)
			inspectSequence := []inspectCallResult{
				{err: cerrdefs.ErrNotFound},
				{result: conflicting},
			}
			if test.wantSuccess {
				inspectSequence = append(
					inspectSequence,
					inspectCallResult{err: cerrdefs.ErrNotFound},
					inspectCallResult{err: cerrdefs.ErrNotFound},
				)
			} else {
				inspectSequence = append(inspectSequence, inspectCallResult{result: conflicting})
			}
			engine := &recordingEngine{
				inspectErr:               cerrdefs.ErrNotFound,
				createErrSequence:        []error{cerrdefs.ErrConflict, nil},
				populatorInspectSequence: inspectSequence,
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			instance, err := provisioner.Start(context.Background(), request)
			if test.wantSuccess {
				if err != nil {
					t.Fatalf("Start() error = %v", err)
				}
				if instance.ContainerID == "" || !containsString(engine.removed, "stale-populator") {
					t.Fatalf("exact stale conflict did not reconcile: instance=%#v removals=%#v", instance, engine.removed)
				}
				return
			}
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Start() error = %v, want fail-closed populator conflict", err)
			}
			for _, protected := range []string{"active-populator", "foreign-populator"} {
				if containsString(engine.removed, protected) {
					t.Fatalf("Start() removed concurrent/mismatched populator %q", protected)
				}
			}
			if len(engine.volumeRemoves) != 0 {
				t.Fatalf("Start() removed volume owned by another attempt: %#v", engine.volumeRemoves)
			}
		})
	}
}

func TestDockerStartRejectsMismatchedStaleSecretPopulatorSpecs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*client.ContainerInspectResult)
	}{
		{
			name: "labels",
			mutate: func(inspected *client.ContainerInspectResult) {
				inspected.Container.Config.Labels[LabelWorkerRunID] = "foreign-run"
			},
		},
		{
			name: "image",
			mutate: func(inspected *client.ContainerInspectResult) {
				inspected.Container.Config.Image = "foreign@sha256:" + strings.Repeat("2", 64)
			},
		},
		{
			name: "environment",
			mutate: func(inspected *client.ContainerInspectResult) {
				inspected.Container.Config.Env = append(inspected.Container.Config.Env, "SECRET=foreign")
			},
		},
		{
			name: "argv",
			mutate: func(inspected *client.ContainerInspectResult) {
				inspected.Container.Config.Cmd = append(inspected.Container.Config.Cmd, "secret-value")
			},
		},
		{
			name: "network",
			mutate: func(inspected *client.ContainerInspectResult) {
				inspected.Container.HostConfig.NetworkMode = container.NetworkMode("bridge")
			},
		},
		{
			name: "mount",
			mutate: func(inspected *client.ContainerInspectResult) {
				inspected.Container.Mounts[0].Source = "/tmp/foreign-bootstrap"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			inspected := inspectedSecretPopulator("stale-populator", request, options, false)
			test.mutate(&inspected)
			engine := &recordingEngine{
				inspectErr: cerrdefs.ErrNotFound,
				populatorInspectSequence: []inspectCallResult{
					{result: inspected},
				},
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			_, err = provisioner.Start(context.Background(), request)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Start() error = %v, want populator immutable spec rejection", err)
			}
			if len(engine.removed) != 0 || engine.createCalls != 0 {
				t.Fatalf("mismatched populator was changed: removed=%#v creates=%d", engine.removed, engine.createCalls)
			}
		})
	}
}

func TestDockerStartSurfacesSecretPopulatorRemovalResidue(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectErr: cerrdefs.ErrNotFound,
		populatorInspectSequence: []inspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: inspectedSecretPopulator("container-1", request, options, false)},
		},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want populator removal residue", err)
	}
	if countManagedCreates(engine.creates, ManagedWorkerRuntime) != 0 {
		t.Fatalf("Worker created after populator residue: creates=%#v", engine.creates)
	}
}

func TestDockerStartRevalidatesVolumeLabelsBeforeRollbackRemoval(t *testing.T) {
	request, options := dockerTestRequest(t)
	conflicting := request.Identity
	conflicting.WorkerRunID = "replacement-run"
	createFailure := errors.New("injected populator create failure")
	engine := &recordingEngine{
		inspectErr: cerrdefs.ErrNotFound,
		populatorInspectSequence: []inspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{err: cerrdefs.ErrNotFound},
		},
		volumeInspectSequence: []volumeInspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: inspectedSecretVolume(conflicting)},
		},
		createErrSequence: []error{createFailure},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, createFailure) || !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want create failure plus replacement conflict", err)
	}
	if len(engine.volumeRemoves) != 0 {
		t.Fatalf("rollback removed label-replaced volume: %#v", engine.volumeRemoves)
	}
}

func TestDockerStartRollsBackEveryPostVolumeFailureBeforeReturning(t *testing.T) {
	injected := errors.New("injected provisioning failure")
	tests := []struct {
		name                 string
		configure            func(*testing.T, ProvisionRequest, *DockerOptions, *recordingEngine)
		wantVolumeRemove     bool
		wantWorkerCreate     bool
		wantWorkerStartCall  bool
		wantWorkerRemoveCall bool
	}{
		{
			name: "volume inspect",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.volumeInspectErr = injected
			},
		},
		{
			name: "volume create",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.volumeCreateErr = injected
			},
		},
		{
			name: "populator create",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.createErrSequence = []error{injected}
			},
			wantVolumeRemove: true,
		},
		{
			name: "populator empty id",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.createIDSequence = []string{""}
			},
			wantVolumeRemove: true,
		},
		{
			name: "populator start",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.startErrSequence = []error{injected}
			},
			wantVolumeRemove: true,
		},
		{
			name: "populator wait",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.waitErrSequence = []error{injected}
			},
			wantVolumeRemove: true,
		},
		{
			name: "populator verify",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.waitStatusSequence = []int64{23}
			},
			wantVolumeRemove: true,
		},
		{
			name: "populator forced removal",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.removeErrSequence = []error{injected}
			},
		},
		{
			name: "transient source removal",
			configure: func(t *testing.T, request ProvisionRequest, options *DockerOptions, _ *recordingEngine) {
				calls := 0
				options.HostPathVerifier = HostPathVerifierFunc(func(
					_ context.Context,
					_ string,
					_ []HostPathRequirement,
				) error {
					calls++
					if calls == 1 {
						if err := os.Remove(request.Bootstrap.ContainerPath); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(request.Bootstrap.ContainerPath, 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(
							filepath.Join(request.Bootstrap.ContainerPath, "residue"),
							[]byte("test-owned"),
							0o600,
						); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				})
			},
			wantVolumeRemove: true,
		},
		{
			name: "Worker host path reverify",
			configure: func(_ *testing.T, _ ProvisionRequest, options *DockerOptions, _ *recordingEngine) {
				options.HostPathVerifier = &recordingHostPathVerifier{
					errSequence: []error{nil, injected},
				}
			},
			wantVolumeRemove: true,
		},
		{
			name: "Worker create",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.createErrSequence = []error{nil, injected}
			},
			wantVolumeRemove: true,
			wantWorkerCreate: true,
		},
		{
			name: "Worker empty id",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.createIDSequence = []string{"populator-1", ""}
			},
			wantVolumeRemove: true,
			wantWorkerCreate: true,
		},
		{
			name: "Worker start",
			configure: func(_ *testing.T, _ ProvisionRequest, _ *DockerOptions, engine *recordingEngine) {
				engine.startErrSequence = []error{nil, injected}
			},
			wantVolumeRemove:     true,
			wantWorkerCreate:     true,
			wantWorkerStartCall:  true,
			wantWorkerRemoveCall: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
			test.configure(t, request, &options, engine)
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			instance, err := provisioner.Start(context.Background(), request)
			if err == nil {
				t.Fatalf("Start() = %#v, nil error; want failure", instance)
			}
			if got := len(engine.volumeRemoves) > 0; got != test.wantVolumeRemove {
				t.Fatalf("volume rollback = %v, want %v; calls=%#v", got, test.wantVolumeRemove, engine.calls)
			}
			if got := countManagedCreates(engine.creates, ManagedWorkerRuntime) > 0; got != test.wantWorkerCreate {
				t.Fatalf("Worker create = %v, want %v; creates=%#v", got, test.wantWorkerCreate, engine.creates)
			}
			if got := containsString(engine.started, "container-2"); got != test.wantWorkerStartCall {
				t.Fatalf("Worker start call = %v, want %v; starts=%#v", got, test.wantWorkerStartCall, engine.started)
			}
			if got := containsString(engine.removed, "container-2"); got != test.wantWorkerRemoveCall {
				t.Fatalf("Worker rollback = %v, want %v; removals=%#v", got, test.wantWorkerRemoveCall, engine.removed)
			}
		})
	}
}

func TestDockerStartSurfacesRollbackFailuresWithoutDeletingInUseVolume(t *testing.T) {
	t.Run("Worker removal failure fences volume removal", func(t *testing.T) {
		request, options := dockerTestRequest(t)
		startFailure := errors.New("injected Worker start failure")
		workerRollbackFailure := errors.New("injected Worker rollback failure")
		engine := &recordingEngine{
			inspectErr:        cerrdefs.ErrNotFound,
			startErrSequence:  []error{nil, startFailure},
			removeErrSequence: []error{nil, workerRollbackFailure},
		}
		provisioner, err := NewDockerProvisioner(engine, options)
		if err != nil {
			t.Fatalf("NewDockerProvisioner() error = %v", err)
		}

		_, err = provisioner.Start(context.Background(), request)
		for _, want := range []error{startFailure, workerRollbackFailure} {
			if !errors.Is(err, want) {
				t.Errorf("Start() error = %v, want errors.Is(%v)", err, want)
			}
		}
		if len(engine.volumeRemoves) != 0 {
			t.Fatalf("rollback removed volume while Worker removal was unconfirmed: %#v", engine.volumeRemoves)
		}
	})

	t.Run("volume removal failure remains nonzero", func(t *testing.T) {
		request, options := dockerTestRequest(t)
		startFailure := errors.New("injected Worker start failure")
		volumeRollbackFailure := errors.New("injected volume rollback failure")
		engine := &recordingEngine{
			inspectErr:           cerrdefs.ErrNotFound,
			startErrSequence:     []error{nil, startFailure},
			volumeRemoveSequence: []error{volumeRollbackFailure},
		}
		provisioner, err := NewDockerProvisioner(engine, options)
		if err != nil {
			t.Fatalf("NewDockerProvisioner() error = %v", err)
		}

		_, err = provisioner.Start(context.Background(), request)
		for _, want := range []error{startFailure, volumeRollbackFailure} {
			if !errors.Is(err, want) {
				t.Errorf("Start() error = %v, want errors.Is(%v)", err, want)
			}
		}
		if got, want := engine.calls[len(engine.calls)-2:], []string{
			"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
			"volume-remove:switchback-runtime-secrets-runtime-1-g1",
		}; !reflect.DeepEqual(got, want) {
			t.Fatalf("rollback tail = %#v, want exact re-inspect then volume removal %#v", got, want)
		}
	})
}

func TestDockerStartRejectsRuntimeSecretVolumeResidueAfterRollback(t *testing.T) {
	request, options := dockerTestRequest(t)
	createFailure := errors.New("injected Worker create failure")
	engine := &recordingEngine{
		inspectErr: cerrdefs.ErrNotFound,
		volumeInspectSequence: []volumeInspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: inspectedSecretVolume(request.Identity)},
			{result: inspectedSecretVolume(request.Identity)},
		},
		createErrSequence: []error{nil, createFailure},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, createFailure) {
		t.Fatalf("Start() error = %v, want create failure", err)
	}
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want rollback residue failure", err)
	}
	if got, want := engine.volumeRemoves, []string{"switchback-runtime-secrets-runtime-1-g1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("volume removals = %#v, want %#v", got, want)
	}
	if got := countString(engine.calls, "volume-inspect:switchback-runtime-secrets-runtime-1-g1"); got != 3 {
		t.Fatalf("volume inspect calls = %d, want create precheck, pre-remove revalidation and residue check", got)
	}
}

func TestDockerStartRejectsAnExistingContainerWithConflictingIdentity(t *testing.T) {
	request, options := dockerTestRequest(t)
	conflicting := request.Identity
	conflicting.WorkerRunID = "other-run"
	engine := &recordingEngine{
		inspectResult: inspectedContainer("foreign-container", conflicting, true),
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() error = %v, want ErrRuntimeIdentityConflict", err)
	}
	if engine.createCalls != 0 || len(engine.started) != 0 {
		t.Fatalf("conflicting Start changed container: creates=%d starts=%v", engine.createCalls, engine.started)
	}
}

func TestDockerStartReconcilesACreateConflictToTheExactContainer(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectSequence: []inspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: inspectedSecureWorker("racing-container", request, options, true)},
		},
		createErrSequence: []error{nil, cerrdefs.ErrConflict},
		volumeInspectSequence: []volumeInspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: inspectedSecretVolume(request.Identity)},
		},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	instance, err := provisioner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got, want := instance.ContainerID, "racing-container"; got != want {
		t.Fatalf("ContainerID = %q, want %q", got, want)
	}
	if engine.createCalls != 2 || !reflect.DeepEqual(engine.started, []string{"container-1"}) {
		t.Fatalf("conflict reconciliation calls: creates=%d starts=%v", engine.createCalls, engine.started)
	}
}

func TestDockerStartDoesNotRollbackAnExternalWorkerAfterCreateConflict(t *testing.T) {
	request, options := dockerTestRequest(t)
	startFailure := errors.New("injected racing Worker start failure")
	racing := inspectedSecureWorker("racing-container", request, options, false)
	engine := &recordingEngine{
		inspectSequence: []inspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: racing},
			{result: racing},
		},
		createErrSequence: []error{nil, cerrdefs.ErrConflict},
		startErrSequence:  []error{nil, startFailure},
		volumeInspectSequence: []volumeInspectCallResult{
			{err: cerrdefs.ErrNotFound},
			{result: inspectedSecretVolume(request.Identity)},
		},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	_, err = provisioner.Start(context.Background(), request)
	if !errors.Is(err, startFailure) {
		t.Fatalf("Start() error = %v, want racing Worker start failure", err)
	}
	if slices.Contains(engine.removed, racing.Container.ID) {
		t.Fatalf("rollback removed external racing Worker: %#v", engine.removed)
	}
	if len(engine.volumeRemoves) != 0 {
		t.Fatalf("rollback removed a volume adopted by an external racing Worker: %#v", engine.volumeRemoves)
	}
}

func TestDockerCleanupRemovesOnlyTheExactManagedContainer(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectResult: inspectedSecureWorker("existing-container", request, options, true),
		populatorInspectSequence: []inspectCallResult{
			{result: inspectedSecretPopulator("stale-populator", request, options, false)},
			{err: cerrdefs.ErrNotFound},
		},
		volumeInspectSequence: []volumeInspectCallResult{
			{result: inspectedSecretVolume(request.Identity)},
			{err: cerrdefs.ErrNotFound},
		},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if err := provisioner.Cleanup(context.Background(), request.Identity); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if got, want := engine.waited, []string{"existing-container"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("waited containers = %v, want %v", got, want)
	}
	if got, want := engine.waitOptions, []client.ContainerWaitOptions{{Condition: container.WaitConditionRemoved}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wait options = %#v, want %#v", got, want)
	}
	if got, want := engine.removed, []string{"existing-container", "stale-populator"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed containers = %v, want %v", got, want)
	}
	if got, want := engine.removeOptions, []client.ContainerRemoveOptions{{Force: true}, {Force: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remove options = %#v, want %#v", got, want)
	}
	if got, want := engine.volumeRemoves, []string{"switchback-runtime-secrets-runtime-1-g1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removed volumes = %#v, want %#v", got, want)
	}
	if got, want := engine.volumeRemoveOptions, []client.VolumeRemoveOptions{{}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("volume remove options = %#v, want %#v", got, want)
	}
	wantOrder := []string{
		"inspect:switchback-runtime-runtime-1",
		"wait:existing-container",
		"remove:existing-container",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
		"remove:stale-populator",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
		"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
		"volume-remove:switchback-runtime-secrets-runtime-1-g1",
		"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
	}
	if !reflect.DeepEqual(engine.calls, wantOrder) {
		t.Fatalf("cleanup call order = %#v, want Worker before volume %#v", engine.calls, wantOrder)
	}
}

func TestDockerCleanupRejectsMismatchedOrResidualSecretPopulatorBeforeVolume(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(*client.ContainerInspectResult, DockerOptions)
		residue         bool
		wantRemoveCalls int
	}{
		{
			name: "mismatched config",
			mutate: func(inspected *client.ContainerInspectResult, options DockerOptions) {
				inspected.Container.HostConfig.NetworkMode = container.NetworkMode(options.Network)
			},
		},
		{
			name:            "residue after remove",
			residue:         true,
			wantRemoveCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			inspected := inspectedSecretPopulator("stale-populator", request, options, false)
			if test.mutate != nil {
				test.mutate(&inspected, options)
			}
			sequence := []inspectCallResult{{result: inspected}}
			if test.residue {
				sequence = append(sequence, inspectCallResult{result: inspected})
			}
			engine := &recordingEngine{
				inspectErr:               cerrdefs.ErrNotFound,
				populatorInspectSequence: sequence,
				volumeInspectResult:      inspectedSecretVolume(request.Identity),
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			err = provisioner.Cleanup(context.Background(), request.Identity)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Cleanup() error = %v, want populator conflict", err)
			}
			if got := len(engine.removed); got != test.wantRemoveCalls {
				t.Fatalf("populator remove calls = %d, want %d", got, test.wantRemoveCalls)
			}
			if len(engine.volumeRemoves) != 0 {
				t.Fatalf("Cleanup() removed volume after populator conflict: %#v", engine.volumeRemoves)
			}
		})
	}
}

func TestDockerCleanupRejectsAContainerWithoutAllExpectedLabels(t *testing.T) {
	request, options := dockerTestRequest(t)
	inspected := inspectedContainer("foreign-container", request.Identity, true)
	delete(inspected.Container.Config.Labels, LabelManaged)
	engine := &recordingEngine{inspectResult: inspected}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	err = provisioner.Cleanup(context.Background(), request.Identity)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Cleanup() error = %v, want ErrRuntimeIdentityConflict", err)
	}
	if len(engine.removed) != 0 {
		t.Fatalf("Cleanup() removed foreign container %v", engine.removed)
	}
}

func TestDockerCleanupIsIdempotentWhenContainerDoesNotExist(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectErr:       cerrdefs.ErrNotFound,
		volumeInspectErr: cerrdefs.ErrNotFound,
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if err := provisioner.Cleanup(context.Background(), request.Identity); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if len(engine.removed) != 0 {
		t.Fatalf("Cleanup() removals = %v, want none", engine.removed)
	}
	if got, want := engine.calls, []string{
		"inspect:switchback-runtime-runtime-1",
		"inspect:switchback-runtime-secret-populator-runtime-1-g1",
		"volume-inspect:switchback-runtime-secrets-runtime-1-g1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Cleanup() calls = %#v, want idempotent checks %#v", got, want)
	}
}

func TestDockerCleanupRemovesExactVolumeWhenWorkerIsAlreadyMissing(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectErr: cerrdefs.ErrNotFound,
		volumeInspectSequence: []volumeInspectCallResult{
			{result: inspectedSecretVolume(request.Identity)},
			{err: cerrdefs.ErrNotFound},
		},
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if err := provisioner.Cleanup(context.Background(), request.Identity); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if got, want := engine.volumeRemoves, []string{"switchback-runtime-secrets-runtime-1-g1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Cleanup() volume removals = %#v, want %#v", got, want)
	}
}

func TestDockerCleanupRejectsConflictingOrResidualSecretVolume(t *testing.T) {
	request, options := dockerTestRequest(t)
	conflicting := request.Identity
	conflicting.WorkerRunID = "foreign-run"
	tests := []struct {
		name                  string
		volumeInspectSequence []volumeInspectCallResult
		wantRemoveCalls       int
	}{
		{
			name: "conflicting labels",
			volumeInspectSequence: []volumeInspectCallResult{
				{result: inspectedSecretVolume(conflicting)},
			},
		},
		{
			name: "residue after remove",
			volumeInspectSequence: []volumeInspectCallResult{
				{result: inspectedSecretVolume(request.Identity)},
				{result: inspectedSecretVolume(request.Identity)},
			},
			wantRemoveCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := &recordingEngine{
				inspectErr:            cerrdefs.ErrNotFound,
				volumeInspectSequence: test.volumeInspectSequence,
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			err = provisioner.Cleanup(context.Background(), request.Identity)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Cleanup() error = %v, want ErrRuntimeIdentityConflict", err)
			}
			if got := len(engine.volumeRemoves); got != test.wantRemoveCalls {
				t.Fatalf("volume remove calls = %d, want %d", got, test.wantRemoveCalls)
			}
		})
	}
}

func TestDockerCleanupRejectsUnsafeSecretVolumeBackingIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*volume.Volume)
	}{
		{
			name: "local driver bind options",
			mutate: func(inspected *volume.Volume) {
				inspected.Options = map[string]string{
					"type":   "none",
					"o":      "bind",
					"device": "/test-owned/foreign-secret-source",
				}
			},
		},
		{
			name: "global scope",
			mutate: func(inspected *volume.Volume) {
				inspected.Scope = "global"
			},
		},
		{
			name: "cluster volume",
			mutate: func(inspected *volume.Volume) {
				inspected.ClusterVolume = &volume.ClusterVolume{ID: "foreign-cluster-volume"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			inspectedVolume := inspectedSecretVolume(request.Identity)
			test.mutate(&inspectedVolume.Volume)
			engine := &recordingEngine{
				inspectErr:          cerrdefs.ErrNotFound,
				volumeInspectResult: inspectedVolume,
			}
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			err = provisioner.Cleanup(context.Background(), request.Identity)
			if !errors.Is(err, ErrRuntimeIdentityConflict) {
				t.Fatalf("Cleanup() error = %v, want unsafe volume identity conflict", err)
			}
			if len(engine.volumeRemoves) != 0 {
				t.Fatalf("Cleanup() removed unsafe volume: %#v", engine.volumeRemoves)
			}
		})
	}
}

func TestDockerCleanupCancelsBlockedWaitWhenRemoveFails(t *testing.T) {
	request, options := dockerTestRequest(t)
	removeErr := errors.New("injected remove failure")
	engine := &recordingEngine{
		inspectResult:        inspectedContainer("existing-container", request.Identity, true),
		removeErr:            removeErr,
		blockWaitUntilCancel: true,
		waitStarted:          make(chan struct{}),
		waitCanceled:         make(chan struct{}),
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	err = provisioner.Cleanup(context.Background(), request.Identity)
	if !errors.Is(err, removeErr) {
		t.Fatalf("Cleanup() error = %v, want injected remove error", err)
	}
	select {
	case <-engine.waitStarted:
	default:
		t.Fatal("ContainerWait did not start before remove")
	}
	select {
	case <-engine.waitCanceled:
	default:
		t.Fatal("Cleanup() returned before the blocked ContainerWait was canceled and drained")
	}
}

func TestDockerCleanupTreatsRemoveNotFoundAsSuccessAndCancelsWait(t *testing.T) {
	request, options := dockerTestRequest(t)
	engine := &recordingEngine{
		inspectResult:        inspectedContainer("existing-container", request.Identity, true),
		removeErr:            cerrdefs.ErrNotFound,
		blockWaitUntilCancel: true,
		waitStarted:          make(chan struct{}),
		waitCanceled:         make(chan struct{}),
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	if err := provisioner.Cleanup(context.Background(), request.Identity); err != nil {
		t.Fatalf("Cleanup() error = %v, want remove-time NotFound success", err)
	}
	select {
	case <-engine.waitStarted:
	default:
		t.Fatal("ContainerWait did not start before remove")
	}
	select {
	case <-engine.waitCanceled:
	default:
		t.Fatal("Cleanup() returned before the NotFound-race wait was canceled and drained")
	}
}

func TestDockerSerializesOperationsForTheSameRuntime(t *testing.T) {
	tests := []struct {
		name   string
		second func(*DockerProvisioner, ProvisionRequest) error
	}{
		{
			name: "Start Start",
			second: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "Start Cleanup",
			second: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(context.Background(), request.Identity)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			engine := newSerializationEngine()
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			firstDone := make(chan error, 1)
			go func() {
				_, err := provisioner.Start(context.Background(), request)
				firstDone <- err
			}()
			<-engine.firstEntered

			secondStarted := make(chan struct{})
			secondDone := make(chan error, 1)
			go func() {
				close(secondStarted)
				secondDone <- test.second(provisioner, request)
			}()
			<-secondStarted
			time.Sleep(25 * time.Millisecond)
			if got := engine.inspectCalls.Load(); got != 1 {
				t.Fatalf("same-Runtime operations entered Engine concurrently: inspect calls=%d", got)
			}

			close(engine.releaseFirst)
			if err := <-firstDone; !errors.Is(err, engine.inspectFailure) {
				t.Fatalf("first Start error = %v, want injected inspect failure", err)
			}
			if err := <-secondDone; !errors.Is(err, engine.inspectFailure) {
				t.Fatalf("second operation error = %v, want injected inspect failure", err)
			}
			if got := engine.inspectCalls.Load(); got != 2 {
				t.Fatalf("serialized inspect calls = %d, want 2", got)
			}
		})
	}
}

func TestDockerCanceledOperationWaitersReturnAndReleaseRuntimeLockEntries(t *testing.T) {
	tests := []struct {
		name   string
		second func(context.Context, *DockerProvisioner, ProvisionRequest) error
	}{
		{
			name: "Start Start",
			second: func(ctx context.Context, provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(ctx, request)
				return err
			},
		},
		{
			name: "Start Cleanup",
			second: func(ctx context.Context, provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(ctx, request.Identity)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			engine := newSerializationEngine()
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			firstDone := make(chan error, 1)
			go func() {
				_, err := provisioner.Start(context.Background(), request)
				firstDone <- err
			}()
			<-engine.firstEntered

			secondCtx, cancelSecond := context.WithCancel(context.Background())
			secondDone := make(chan error, 1)
			go func() {
				secondDone <- test.second(secondCtx, provisioner, request)
			}()
			cancelSecond()

			select {
			case err := <-secondDone:
				if !errors.Is(err, context.Canceled) {
					close(engine.releaseFirst)
					<-firstDone
					t.Fatalf("second operation error = %v, want context.Canceled", err)
				}
			case <-time.After(250 * time.Millisecond):
				close(engine.releaseFirst)
				<-firstDone
				t.Fatal("canceled second operation remained blocked on the per-Runtime lock")
			}

			if got := engine.inspectCalls.Load(); got != 1 {
				close(engine.releaseFirst)
				<-firstDone
				t.Fatalf("canceled second operation entered Engine: inspect calls=%d", got)
			}
			close(engine.releaseFirst)
			if err := <-firstDone; !errors.Is(err, engine.inspectFailure) {
				t.Fatalf("first Start error = %v, want injected inspect failure", err)
			}

			provisioner.operationLocks.mu.Lock()
			remainingEntries := len(provisioner.operationLocks.entries)
			provisioner.operationLocks.mu.Unlock()
			if remainingEntries != 0 {
				t.Fatalf("per-Runtime lock entries = %d, want 0", remainingEntries)
			}
		})
	}
}

func TestDockerEngineErrorsAreSanitizedAndPreserveTheirCause(t *testing.T) {
	tests := []struct {
		name      string
		want      string
		setup     func(*recordingEngine, *DockerOptions, ProvisionRequest, error)
		invokeErr func(*DockerProvisioner, ProvisionRequest) error
	}{
		{
			name: "host path verifier",
			want: "verify Docker HostPaths failed",
			setup: func(engine *recordingEngine, options *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				options.HostPathVerifier = &recordingHostPathVerifier{err: cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "initial inspect",
			want: "inspect runtime container failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "volume inspect",
			want: "inspect runtime secret volume failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.volumeInspectErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "volume create",
			want: "create runtime secret volume failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.volumeInspectErr = cerrdefs.ErrNotFound
				engine.volumeCreateErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "populator create",
			want: "create runtime secret populator failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.createErrSequence = []error{cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "populator start",
			want: "start runtime secret populator failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.startErrSequence = []error{cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "populator wait",
			want: "wait for runtime secret populator failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.waitErrSequence = []error{cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "conflict inspect",
			want: "inspect conflicting runtime container failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectSequence = []inspectCallResult{
					{err: cerrdefs.ErrNotFound},
					{err: cause},
				}
				engine.createErrSequence = []error{nil, cerrdefs.ErrConflict}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "create",
			want: "create runtime container failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.createErrSequence = []error{nil, cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "start",
			want: "start runtime container failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.startErrSequence = []error{nil, cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "existing start",
			want: "start existing runtime container failed",
			setup: func(engine *recordingEngine, options *DockerOptions, request ProvisionRequest, cause error) {
				engine.inspectResult = inspectedSecureWorker("existing-container", request, *options, false)
				engine.volumeInspectResult = inspectedSecretVolume(request.Identity)
				engine.startErrSequence = []error{cause}
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				_, err := provisioner.Start(context.Background(), request)
				return err
			},
		},
		{
			name: "cleanup inspect",
			want: "inspect runtime container for cleanup failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(context.Background(), request.Identity)
			},
		},
		{
			name: "cleanup volume inspect",
			want: "inspect runtime secret volume for cleanup failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, _ ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.volumeInspectErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(context.Background(), request.Identity)
			},
		},
		{
			name: "cleanup volume remove",
			want: "remove runtime secret volume failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, request ProvisionRequest, cause error) {
				engine.inspectErr = cerrdefs.ErrNotFound
				engine.volumeInspectResult = inspectedSecretVolume(request.Identity)
				engine.volumeRemoveErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(context.Background(), request.Identity)
			},
		},
		{
			name: "wait",
			want: "wait for runtime container removal failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, request ProvisionRequest, cause error) {
				engine.inspectResult = inspectedContainer("existing-container", request.Identity, true)
				engine.waitErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(context.Background(), request.Identity)
			},
		},
		{
			name: "remove",
			want: "remove runtime container failed",
			setup: func(engine *recordingEngine, _ *DockerOptions, request ProvisionRequest, cause error) {
				engine.inspectResult = inspectedContainer("existing-container", request.Identity, true)
				engine.removeErr = cause
			},
			invokeErr: func(provisioner *DockerProvisioner, request ProvisionRequest) error {
				return provisioner.Cleanup(context.Background(), request.Identity)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, options := dockerTestRequest(t)
			protected := []string{
				request.WorkerToken,
				request.WorkingCopy.HostPath,
				request.Bootstrap.HostPath,
				request.ModelSecret.HostPath,
			}
			cause := &injectedDockerError{text: strings.Join(protected, " | ")}
			engine := &recordingEngine{}
			test.setup(engine, &options, request, cause)
			provisioner, err := NewDockerProvisioner(engine, options)
			if err != nil {
				t.Fatalf("NewDockerProvisioner() error = %v", err)
			}

			gotErr := test.invokeErr(provisioner, request)
			if !errors.Is(gotErr, cause) {
				t.Fatalf("error = %v, want errors.Is(injected cause)", gotErr)
			}
			var typed *injectedDockerError
			if !errors.As(gotErr, &typed) || typed != cause {
				t.Fatalf("error = %v, want errors.As(*injectedDockerError)", gotErr)
			}
			var wrapped *sanitizedDockerError
			if !errors.As(gotErr, &wrapped) {
				t.Fatalf("error = %v, want errors.As(*sanitizedDockerError)", gotErr)
			}
			assertSanitizedDockerFormats(t, wrapped, test.want, protected)
		})
	}
}

func assertSanitizedDockerFormats(
	t *testing.T,
	wrapped *sanitizedDockerError,
	want string,
	protected []string,
) {
	t.Helper()
	formats := []struct {
		format string
		want   string
	}{
		{format: "%v", want: want},
		{format: "%+v", want: want},
		{format: "%#v", want: want},
		{format: "%q", want: fmt.Sprintf("%q", want)},
	}
	values := []struct {
		name  string
		value any
	}{
		{name: "pointer", value: wrapped},
		{name: "value", value: *wrapped},
	}
	for _, value := range values {
		for _, format := range formats {
			got := fmt.Sprintf(format.format, value.value)
			if got != format.want {
				t.Errorf("%s formatted with %s = %q, want %q", value.name, format.format, got, format.want)
			}
			for _, secret := range protected {
				if strings.Contains(got, secret) {
					t.Errorf("%s formatted with %s exposed protected value %q", value.name, format.format, secret)
				}
			}
		}
	}
}

type recordingEngine struct {
	inspectResult            client.ContainerInspectResult
	inspectErr               error
	inspectSequence          []inspectCallResult
	populatorInspectResult   client.ContainerInspectResult
	populatorInspectErr      error
	populatorInspectSequence []inspectCallResult
	created                  client.ContainerCreateOptions
	creates                  []client.ContainerCreateOptions
	createCalls              int
	createErr                error
	createErrSequence        []error
	createIDSequence         []string
	started                  []string
	startErr                 error
	startErrSequence         []error
	waited                   []string
	waitOptions              []client.ContainerWaitOptions
	waitErr                  error
	waitErrSequence          []error
	waitStatusCode           int64
	waitStatusSequence       []int64
	blockWaitUntilCancel     bool
	waitStarted              chan struct{}
	waitCanceled             chan struct{}
	removed                  []string
	removeOptions            []client.ContainerRemoveOptions
	removeErr                error
	removeErrSequence        []error
	calls                    []string
	cancelRequestOnWait      context.CancelFunc
	removeContexts           []removeContextObservation
	removeContextValueKey    any
	volumeInspectResult      client.VolumeInspectResult
	volumeInspectErr         error
	volumeInspectSequence    []volumeInspectCallResult
	volumeCreates            []client.VolumeCreateOptions
	volumeCreateResult       client.VolumeCreateResult
	volumeCreateErr          error
	volumeRemoves            []string
	volumeRemoveOptions      []client.VolumeRemoveOptions
	volumeRemoveErr          error
	volumeRemoveSequence     []error
	containers               map[string]client.ContainerInspectResult
	containerNamesByID       map[string]string
}

type serializationEngine struct {
	inspectCalls   atomic.Int64
	firstEntered   chan struct{}
	releaseFirst   chan struct{}
	inspectFailure error
}

func newSerializationEngine() *serializationEngine {
	return &serializationEngine{
		firstEntered:   make(chan struct{}),
		releaseFirst:   make(chan struct{}),
		inspectFailure: errors.New("injected serialized inspect failure"),
	}
}

func (e *serializationEngine) ContainerInspect(
	_ context.Context,
	_ string,
	_ client.ContainerInspectOptions,
) (client.ContainerInspectResult, error) {
	call := e.inspectCalls.Add(1)
	if call == 1 {
		close(e.firstEntered)
		<-e.releaseFirst
	}
	return client.ContainerInspectResult{}, e.inspectFailure
}

func (*serializationEngine) ContainerCreate(
	context.Context,
	client.ContainerCreateOptions,
) (client.ContainerCreateResult, error) {
	return client.ContainerCreateResult{}, errors.New("unexpected ContainerCreate")
}

func (*serializationEngine) ContainerStart(
	context.Context,
	string,
	client.ContainerStartOptions,
) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, errors.New("unexpected ContainerStart")
}

func (*serializationEngine) ContainerWait(
	context.Context,
	string,
	client.ContainerWaitOptions,
) client.ContainerWaitResult {
	return client.ContainerWaitResult{}
}

func (*serializationEngine) ContainerRemove(
	context.Context,
	string,
	client.ContainerRemoveOptions,
) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, errors.New("unexpected ContainerRemove")
}

func (*serializationEngine) VolumeCreate(
	context.Context,
	client.VolumeCreateOptions,
) (client.VolumeCreateResult, error) {
	return client.VolumeCreateResult{}, errors.New("unexpected VolumeCreate")
}

func (*serializationEngine) VolumeInspect(
	context.Context,
	string,
	client.VolumeInspectOptions,
) (client.VolumeInspectResult, error) {
	return client.VolumeInspectResult{}, errors.New("unexpected VolumeInspect")
}

func (*serializationEngine) VolumeRemove(
	context.Context,
	string,
	client.VolumeRemoveOptions,
) (client.VolumeRemoveResult, error) {
	return client.VolumeRemoveResult{}, errors.New("unexpected VolumeRemove")
}

type removeContextObservation struct {
	err         error
	deadline    time.Time
	hasDeadline bool
	value       any
}

type inspectCallResult struct {
	result client.ContainerInspectResult
	err    error
}

type volumeInspectCallResult struct {
	result client.VolumeInspectResult
	err    error
}

type injectedDockerError struct {
	text string
}

func (e *injectedDockerError) Error() string {
	return e.text
}

type recordingHostPathVerifier struct {
	calls           int
	root            string
	requirements    []HostPathRequirement
	requirementSets [][]HostPathRequirement
	beforeVerify    func()
	err             error
	errSequence     []error
}

func (v *recordingHostPathVerifier) VerifyHostPaths(
	_ context.Context,
	root string,
	requirements []HostPathRequirement,
) error {
	v.calls++
	v.root = root
	v.requirements = append([]HostPathRequirement(nil), requirements...)
	v.requirementSets = append(v.requirementSets, append([]HostPathRequirement(nil), requirements...))
	if v.beforeVerify != nil {
		v.beforeVerify()
	}
	if len(v.errSequence) > 0 {
		err := v.errSequence[0]
		v.errSequence = v.errSequence[1:]
		return err
	}
	return v.err
}

func (e *recordingEngine) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	e.created = options
	e.creates = append(e.creates, options)
	e.createCalls++
	managed := ""
	if options.Config != nil {
		managed = options.Config.Labels[LabelManaged]
	}
	e.calls = append(e.calls, "create:"+managed)
	if len(e.createErrSequence) > 0 {
		err := e.createErrSequence[0]
		e.createErrSequence = e.createErrSequence[1:]
		if err != nil {
			return client.ContainerCreateResult{}, err
		}
	}
	if e.createErr != nil {
		return client.ContainerCreateResult{}, e.createErr
	}
	if len(e.createIDSequence) > 0 {
		id := e.createIDSequence[0]
		e.createIDSequence = e.createIDSequence[1:]
		if id != "" {
			e.recordContainer(options, id)
		}
		return client.ContainerCreateResult{ID: id}, nil
	}
	id := fmt.Sprintf("container-%d", e.createCalls)
	e.recordContainer(options, id)
	return client.ContainerCreateResult{ID: id}, nil
}

func (e *recordingEngine) recordContainer(options client.ContainerCreateOptions, id string) {
	if e.containers == nil {
		e.containers = make(map[string]client.ContainerInspectResult)
		e.containerNamesByID = make(map[string]string)
	}
	var config *container.Config
	if options.Config != nil {
		copied := *options.Config
		copied.Image = options.Image
		config = &copied
	}
	var host *container.HostConfig
	var mounts []container.MountPoint
	if options.HostConfig != nil {
		copied := *options.HostConfig
		host = &copied
		for _, mounted := range options.HostConfig.Mounts {
			point := container.MountPoint{
				Type:        mounted.Type,
				Source:      mounted.Source,
				Destination: mounted.Target,
				RW:          !mounted.ReadOnly,
			}
			if mounted.Type == mount.TypeVolume {
				point.Name = mounted.Source
			}
			mounts = append(mounts, point)
		}
	}
	inspected := client.ContainerInspectResult{Container: container.InspectResponse{
		ID:         id,
		State:      &container.State{},
		Config:     config,
		HostConfig: host,
		Mounts:     mounts,
	}}
	e.containers[options.Name] = inspected
	e.containers[id] = inspected
	e.containerNamesByID[id] = options.Name
}

func inspectedContainer(id string, identity RuntimeIdentity, running bool) client.ContainerInspectResult {
	return client.ContainerInspectResult{
		Container: container.InspectResponse{
			ID:     id,
			State:  &container.State{Running: running},
			Config: &container.Config{Labels: identityLabels(identity)},
		},
	}
}

func inspectedSecureWorker(
	id string,
	request ProvisionRequest,
	options DockerOptions,
	running bool,
) client.ContainerInspectResult {
	identity := request.Identity
	pids := workerPIDs
	oomKillDisable := false
	inspected := inspectedContainer(id, identity, running)
	inspected.Container.Config.Image = options.Image
	inspected.Container.Config.User = workerUser
	inspected.Container.Config.WorkingDir = WorkerWorkingCopyPath
	inspected.Container.Config.Env = expectedWorkerEnvironment(request, options)
	inspected.Container.Config.Entrypoint = []string{workerTiniPath, "--"}
	inspected.Container.Config.Cmd = []string{workerShimPath}
	inspected.Container.HostConfig = &container.HostConfig{
		NetworkMode: container.NetworkMode(options.Network),
		LogConfig:   container.LogConfig{Type: workerLogDriver},
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyDisabled,
		},
		CgroupnsMode:    container.CgroupnsModePrivate,
		IpcMode:         container.IPCModePrivate,
		Privileged:      false,
		PublishAllPorts: false,
		ReadonlyRootfs:  false,
		CapDrop:         []string{"ALL"},
		CapAdd:          []string{"KILL", "SETUID", "SETGID"},
		SecurityOpt:     []string{"no-new-privileges"},
		ShmSize:         workerShmSize,
		Runtime:         workerRuntime,
		Init:            boolPointer(false),
		Mounts:          workerMounts(request, secretVolumeName(identity)),
		MaskedPaths:     workerMaskedPaths(),
		ReadonlyPaths:   workerReadonlyPaths(),
		Resources: container.Resources{
			NanoCPUs:       workerNanoCPUs,
			Memory:         workerMemoryBytes,
			MemorySwap:     workerMemoryBytes * 2,
			OomKillDisable: &oomKillDisable,
			PidsLimit:      &pids,
		},
	}
	inspected.Container.Mounts = []container.MountPoint{
		{
			Type:        mount.TypeBind,
			Source:      request.WorkingCopy.HostPath,
			Destination: WorkerWorkingCopyPath,
			RW:          true,
		},
		{
			Type:        mount.TypeBind,
			Source:      request.AgentState.HostPath,
			Destination: WorkerAgentStatePath,
			RW:          true,
		},
		{
			Type:        mount.TypeBind,
			Source:      request.TraceSpool.HostPath,
			Destination: WorkerTraceSpoolPath,
			RW:          true,
		},
		{
			Type:        mount.TypeVolume,
			Name:        secretVolumeName(identity),
			Source:      secretVolumeName(identity),
			Destination: WorkerSecretsPath,
			RW:          false,
		},
	}
	inspected.Container.NetworkSettings = &container.NetworkSettings{
		Networks: map[string]*dockernetwork.EndpointSettings{
			options.Network: {},
		},
	}
	return inspected
}

func expectedWorkerEnvironment(request ProvisionRequest, options DockerOptions) []string {
	return []string{
		"PATH=/usr/local/go/bin:/opt/switchback/node_modules/.bin:/usr/local/bin:/usr/bin:/bin",
		"GPG_KEY=7169605F62C751356D054A26A821E680E5FA6305",
		"PYTHON_VERSION=3.13.14",
		"PYTHON_SHA256=639e43243c620a308f968213df9e00f2f8f62332f7adbaa7a7eeb9783057c690",
		"SWITCHBACK_WORKER_TOKEN=" + request.WorkerToken,
		"SWITCHBACK_WORKER_SESSION_ID=" + request.Identity.WorkerSessionID,
		"SWITCHBACK_WORKER_RUN_ID=" + request.Identity.WorkerRunID,
		"SWITCHBACK_RUNTIME_INSTANCE_ID=" + request.Identity.RuntimeInstanceID,
		"SWITCHBACK_RUNTIME_GENERATION=" + strconv.FormatUint(uint64(request.Identity.RuntimeGeneration), 10),
		"SWITCHBACK_BACKEND_GRPC_TARGET=" + options.BackendGRPCTarget,
		"SWITCHBACK_BACKEND_HTTP_BASE_URL=" + options.BackendHTTPBaseURL,
	}
}

func inspectedSecretVolume(identity RuntimeIdentity) client.VolumeInspectResult {
	return client.VolumeInspectResult{Volume: volume.Volume{
		Name:   secretVolumeName(identity),
		Driver: "local",
		Labels: resourceLabels(identity, ManagedRuntimeSecretVolume),
		Scope:  "local",
	}}
}

func inspectedSecretPopulator(
	id string,
	request ProvisionRequest,
	options DockerOptions,
	running bool,
) client.ContainerInspectResult {
	oomKillDisable := false
	inspected := inspectedContainer(id, request.Identity, running)
	inspected.Container.Config = &container.Config{
		Image:      options.Image,
		User:       "0:0",
		WorkingDir: "/",
		Env: []string{
			"PATH=/usr/local/go/bin:/opt/switchback/node_modules/.bin:/usr/local/bin:/usr/bin:/bin",
			"GPG_KEY=7169605F62C751356D054A26A821E680E5FA6305",
			"PYTHON_VERSION=3.13.14",
			"PYTHON_SHA256=639e43243c620a308f968213df9e00f2f8f62332f7adbaa7a7eeb9783057c690",
		},
		Entrypoint: []string{"/bin/sh"},
		Cmd:        []string{"-eu", "-c", populateSecretVolumeScript},
		Labels:     resourceLabels(request.Identity, ManagedSecretPopulator),
	}
	inspected.Container.HostConfig = &container.HostConfig{
		NetworkMode:    container.NetworkMode("none"),
		Privileged:     false,
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Resources: container.Resources{
			OomKillDisable: &oomKillDisable,
		},
	}
	inspected.Container.Mounts = []container.MountPoint{
		{
			Type:        mount.TypeBind,
			Source:      request.Bootstrap.HostPath,
			Destination: secretInputBootstrapPath,
			RW:          false,
		},
		{
			Type:        mount.TypeBind,
			Source:      request.ModelSecret.HostPath,
			Destination: secretInputModelPath,
			RW:          false,
		},
		{
			Type:        mount.TypeVolume,
			Name:        secretVolumeName(request.Identity),
			Destination: secretOutputPath,
			RW:          true,
		},
	}
	return inspected
}

func (e *recordingEngine) ContainerStart(_ context.Context, containerID string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	e.started = append(e.started, containerID)
	e.calls = append(e.calls, "start:"+containerID)
	if len(e.startErrSequence) > 0 {
		err := e.startErrSequence[0]
		e.startErrSequence = e.startErrSequence[1:]
		if err == nil {
			e.setContainerRunning(containerID, true)
		}
		return client.ContainerStartResult{}, err
	}
	if e.startErr == nil {
		e.setContainerRunning(containerID, true)
	}
	return client.ContainerStartResult{}, e.startErr
}

func (e *recordingEngine) setContainerRunning(containerID string, running bool) {
	inspected, ok := e.containers[containerID]
	if !ok || inspected.Container.State == nil {
		return
	}
	inspected.Container.State.Running = running
	e.containers[containerID] = inspected
	if name := e.containerNamesByID[containerID]; name != "" {
		e.containers[name] = inspected
	}
}

func (e *recordingEngine) ContainerInspect(_ context.Context, containerID string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	e.calls = append(e.calls, "inspect:"+containerID)
	if strings.HasPrefix(containerID, "switchback-runtime-secret-populator-") {
		if len(e.populatorInspectSequence) > 0 {
			next := e.populatorInspectSequence[0]
			e.populatorInspectSequence = e.populatorInspectSequence[1:]
			return next.result, next.err
		}
		if e.populatorInspectErr != nil {
			return e.populatorInspectResult, e.populatorInspectErr
		}
		if e.populatorInspectResult.Container.ID == "" {
			if inspected, ok := e.containers[containerID]; ok {
				return inspected, nil
			}
			return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
		}
		return e.populatorInspectResult, nil
	}
	if len(e.inspectSequence) > 0 {
		next := e.inspectSequence[0]
		e.inspectSequence = e.inspectSequence[1:]
		return next.result, next.err
	}
	if inspected, ok := e.containers[containerID]; ok {
		return inspected, nil
	}
	return e.inspectResult, e.inspectErr
}

func (e *recordingEngine) ContainerWait(ctx context.Context, containerID string, options client.ContainerWaitOptions) client.ContainerWaitResult {
	e.waited = append(e.waited, containerID)
	e.waitOptions = append(e.waitOptions, options)
	e.calls = append(e.calls, "wait:"+containerID)
	if e.cancelRequestOnWait != nil {
		e.cancelRequestOnWait()
	}
	result := make(chan container.WaitResponse, 1)
	waitErr := make(chan error, 1)
	if e.blockWaitUntilCancel {
		close(e.waitStarted)
		go func() {
			<-ctx.Done()
			close(e.waitCanceled)
			waitErr <- ctx.Err()
		}()
		return client.ContainerWaitResult{Result: result, Error: waitErr}
	}
	selectedWaitErr := e.waitErr
	if len(e.waitErrSequence) > 0 {
		selectedWaitErr = e.waitErrSequence[0]
		e.waitErrSequence = e.waitErrSequence[1:]
	}
	if selectedWaitErr != nil {
		waitErr <- selectedWaitErr
		return client.ContainerWaitResult{Result: result, Error: waitErr}
	}
	statusCode := e.waitStatusCode
	if len(e.waitStatusSequence) > 0 {
		statusCode = e.waitStatusSequence[0]
		e.waitStatusSequence = e.waitStatusSequence[1:]
	}
	e.setContainerRunning(containerID, false)
	result <- container.WaitResponse{StatusCode: statusCode}
	return client.ContainerWaitResult{Result: result, Error: waitErr}
}

func (e *recordingEngine) ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	e.removed = append(e.removed, containerID)
	e.removeOptions = append(e.removeOptions, options)
	e.calls = append(e.calls, "remove:"+containerID)
	deadline, hasDeadline := ctx.Deadline()
	var value any
	if e.removeContextValueKey != nil {
		value = ctx.Value(e.removeContextValueKey)
	}
	e.removeContexts = append(e.removeContexts, removeContextObservation{
		err:         ctx.Err(),
		deadline:    deadline,
		hasDeadline: hasDeadline,
		value:       value,
	})
	if len(e.removeErrSequence) > 0 {
		err := e.removeErrSequence[0]
		e.removeErrSequence = e.removeErrSequence[1:]
		if err == nil || cerrdefs.IsNotFound(err) {
			e.forgetContainer(containerID)
		}
		return client.ContainerRemoveResult{}, err
	}
	if e.removeErr == nil || cerrdefs.IsNotFound(e.removeErr) {
		e.forgetContainer(containerID)
	}
	return client.ContainerRemoveResult{}, e.removeErr
}

func (e *recordingEngine) forgetContainer(containerID string) {
	if e.inspectResult.Container.ID == containerID {
		e.inspectResult = client.ContainerInspectResult{}
		e.inspectErr = cerrdefs.ErrNotFound
	}
	if e.containers == nil {
		return
	}
	name := e.containerNamesByID[containerID]
	delete(e.containers, containerID)
	if name != "" {
		delete(e.containers, name)
		delete(e.containerNamesByID, containerID)
	}
}

func (e *recordingEngine) VolumeCreate(_ context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	e.volumeCreates = append(e.volumeCreates, options)
	e.calls = append(e.calls, "volume-create:"+options.Name)
	if e.volumeCreateErr != nil {
		return client.VolumeCreateResult{}, e.volumeCreateErr
	}
	if e.volumeCreateResult.Volume.Name != "" {
		e.volumeInspectResult = client.VolumeInspectResult{Volume: e.volumeCreateResult.Volume}
		return e.volumeCreateResult, nil
	}
	result := client.VolumeCreateResult{Volume: volume.Volume{
		Name:   options.Name,
		Driver: "local",
		Labels: options.Labels,
		Scope:  "local",
	}}
	e.volumeInspectResult = client.VolumeInspectResult{Volume: result.Volume}
	return result, nil
}

func (e *recordingEngine) VolumeInspect(_ context.Context, name string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	e.calls = append(e.calls, "volume-inspect:"+name)
	if len(e.volumeInspectSequence) > 0 {
		next := e.volumeInspectSequence[0]
		e.volumeInspectSequence = e.volumeInspectSequence[1:]
		return next.result, next.err
	}
	if e.volumeInspectErr != nil {
		return e.volumeInspectResult, e.volumeInspectErr
	}
	if e.volumeInspectResult.Volume.Name == "" {
		return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
	}
	return e.volumeInspectResult, nil
}

func (e *recordingEngine) VolumeRemove(_ context.Context, name string, options client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	e.volumeRemoves = append(e.volumeRemoves, name)
	e.volumeRemoveOptions = append(e.volumeRemoveOptions, options)
	e.calls = append(e.calls, "volume-remove:"+name)
	if len(e.volumeRemoveSequence) > 0 {
		err := e.volumeRemoveSequence[0]
		e.volumeRemoveSequence = e.volumeRemoveSequence[1:]
		if err == nil || cerrdefs.IsNotFound(err) {
			e.volumeInspectResult = client.VolumeInspectResult{}
		}
		return client.VolumeRemoveResult{}, err
	}
	if e.volumeRemoveErr == nil || cerrdefs.IsNotFound(e.volumeRemoveErr) {
		e.volumeInspectResult = client.VolumeInspectResult{}
	}
	return client.VolumeRemoveResult{}, e.volumeRemoveErr
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countManagedCreates(creates []client.ContainerCreateOptions, managed string) int {
	count := 0
	for _, created := range creates {
		if created.Config != nil && created.Config.Labels[LabelManaged] == managed {
			count++
		}
	}
	return count
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func indexString(values []string, want string) int {
	for index, value := range values {
		if value == want {
			return index
		}
	}
	return -1
}

func dockerTestRequest(t *testing.T) (ProvisionRequest, DockerOptions) {
	t.Helper()
	root := t.TempDir()
	mapper := PathMapper{
		ContainerDataRoot: filepath.Join(root, "backend-data"),
		HostDataRoot:      filepath.Join(root, "host-data"),
	}
	for _, path := range []string{mapper.ContainerDataRoot, mapper.HostDataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pair := func(name string, directory bool) PathPair {
		t.Helper()
		got, err := mapper.SessionPath("session-1", name)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{got.ContainerPath, got.HostPath} {
			if directory {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			} else if err := os.WriteFile(path, []byte("server-owned"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return got
	}
	return ProvisionRequest{
			Identity: RuntimeIdentity{
				WorkerSessionID:   "session-1",
				WorkerRunID:       "run-1",
				RuntimeInstanceID: "runtime-1",
				RuntimeGeneration: 1,
			},
			WorkerToken: "private-worker-token",
			WorkingCopy: pair("working-copy", true),
			AgentState:  pair("agent-state", true),
			TraceSpool:  pair("trace-spool", true),
			Bootstrap:   pair("bootstrap.json", false),
			ModelSecret: pair("model-secret", false),
		}, DockerOptions{
			Image:              "switchback-worker@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			Network:            "switchback-worker-net",
			Mapper:             mapper,
			FixturePath:        filepath.Join(root, "fixture"),
			BackendGRPCTarget:  "spike-backend:9090",
			BackendHTTPBaseURL: "http://spike-backend:8080",
			HostPathVerifier:   LocalHostPathVerifier{},
		}
}

var (
	_ DockerEngine = (*recordingEngine)(nil)
	_ DockerEngine = (*serializationEngine)(nil)
)
