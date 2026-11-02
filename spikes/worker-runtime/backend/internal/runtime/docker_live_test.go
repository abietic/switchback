//go:build docker_integration

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const dockerLiveImage = "docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"

func TestDockerLiveConcreteHostPathVerifierAcceptsServerOwnedTree(t *testing.T) {
	ctx, engine := newDockerLiveEngine(t)
	attestorsBefore := dockerLiveAttestorIDs(t, ctx, engine)

	switch value := os.Getenv("SWITCHBACK_SPIKE_DOCKER_TEST_INJECT_FAILURE"); value {
	case "", "after-start":
	default:
		t.Fatalf("unsupported SWITCHBACK_SPIKE_DOCKER_TEST_INJECT_FAILURE value %q", value)
	}

	dataRoot := t.TempDir()
	sessionRoot := filepath.Join(dataRoot, "sessions", "live-session")
	workingCopy := filepath.Join(sessionRoot, "working-copy")
	agentState := filepath.Join(sessionRoot, "agent-state")
	traceSpool := filepath.Join(sessionRoot, "trace-spool")
	bootstrap := filepath.Join(sessionRoot, "bootstrap.json")
	modelSecret := filepath.Join(sessionRoot, "model-secret")
	fixturePath := filepath.Join(dataRoot, "fixture")
	for _, directory := range []string{workingCopy, agentState, traceSpool, fixturePath, filepath.Dir(modelSecret)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create live directory: %v", err)
		}
	}
	bootstrapContent := []byte(`{
  "upstream_model": "openai/switchback-live-fake",
  "upstream_api_base": "http://127.0.0.1:1/v1",
  "proxy_token": "live-proxy-token",
  "model_sink_token": "live-model-sink-token",
  "agent_profile": "integration-fake"
}`)
	modelSecretContent := []byte("live-upstream-key")
	if err := os.WriteFile(bootstrap, bootstrapContent, 0o600); err != nil {
		t.Fatalf("create live bootstrap: %v", err)
	}
	if err := os.WriteFile(modelSecret, modelSecretContent, 0o600); err != nil {
		t.Fatalf("create live model secret: %v", err)
	}

	runtimeInstanceID := "live-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	request := ProvisionRequest{
		Identity: RuntimeIdentity{
			WorkerSessionID:   "live-session",
			WorkerRunID:       "live-run",
			RuntimeInstanceID: runtimeInstanceID,
			RuntimeGeneration: 7,
		},
		WorkerToken: "live-worker-token",
		WorkingCopy: PathPair{ContainerPath: workingCopy, HostPath: workingCopy},
		AgentState:  PathPair{ContainerPath: agentState, HostPath: agentState},
		TraceSpool:  PathPair{ContainerPath: traceSpool, HostPath: traceSpool},
		Bootstrap:   PathPair{ContainerPath: bootstrap, HostPath: bootstrap},
		ModelSecret: PathPair{ContainerPath: modelSecret, HostPath: modelSecret},
	}
	hostPathVerifier, err := NewDockerHostPathVerifier(ctx, engine, dockerLiveImage)
	if err != nil {
		t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
	}
	workerImage := dockerLiveWorkerImage(t, ctx, engine)
	primaryNetworkName := "switchback-task14-primary-" + runtimeInstanceID
	primaryNetwork, err := engine.NetworkCreate(ctx, primaryNetworkName, client.NetworkCreateOptions{})
	if err != nil {
		t.Fatalf("create primary live Worker network: %v", err)
	}
	primaryNetworkPresent := true
	t.Cleanup(func() {
		if !primaryNetworkPresent {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := engine.NetworkRemove(cleanupCtx, primaryNetwork.ID, client.NetworkRemoveOptions{}); err != nil &&
			!cerrdefs.IsNotFound(err) {
			t.Errorf("force-remove primary live Worker network %q: %v", primaryNetworkName, err)
		}
	})
	options := DockerOptions{
		Image:   workerImage,
		Network: primaryNetworkName,
		Mapper: PathMapper{
			ContainerDataRoot: dataRoot,
			HostDataRoot:      dataRoot,
		},
		FixturePath:        fixturePath,
		BackendGRPCTarget:  "spike-backend:9090",
		BackendHTTPBaseURL: "http://spike-backend:8080",
		HostPathVerifier:   hostPathVerifier,
	}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}
	name := "switchback-runtime-" + runtimeInstanceID
	volumeName := "switchback-runtime-secrets-" + runtimeInstanceID + "-g7"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 30*time.Second)
		defer cleanupCancel()
		_, cleanupErr := engine.ContainerRemove(cleanupCtx, name, client.ContainerRemoveOptions{Force: true})
		if cleanupErr != nil && !cerrdefs.IsNotFound(cleanupErr) {
			t.Errorf("force-remove live container %q: %v", name, cleanupErr)
		}
		_, cleanupErr = engine.VolumeRemove(cleanupCtx, volumeName, client.VolumeRemoveOptions{})
		if cleanupErr != nil && !cerrdefs.IsNotFound(cleanupErr) {
			t.Errorf("force-remove live volume %q: %v", volumeName, cleanupErr)
		}
	})

	instance, err := provisioner.Start(ctx, request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	assertDockerLiveAttestorsUnchanged(t, ctx, engine, attestorsBefore)
	if os.Getenv("SWITCHBACK_SPIKE_DOCKER_TEST_INJECT_FAILURE") == "after-start" {
		t.Fatal("injected live failure after container start")
	}
	inspected, err := engine.ContainerInspect(ctx, instance.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect() error = %v", err)
	}
	containerInfo := inspected.Container
	if containerInfo.Config == nil {
		t.Fatal("Config = nil")
	}
	if containerInfo.State == nil {
		t.Fatal("State = nil")
	}
	firstStartedAt := containerInfo.State.StartedAt
	wantLabels := map[string]string{
		"com.switchback.managed":             "worker-runtime",
		"com.switchback.worker-session-id":   "live-session",
		"com.switchback.worker-run-id":       "live-run",
		"com.switchback.runtime-instance-id": runtimeInstanceID,
		"com.switchback.runtime-generation":  "7",
	}
	if !reflect.DeepEqual(containerInfo.Config.Labels, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", containerInfo.Config.Labels, wantLabels)
	}
	if len(containerInfo.Config.ExposedPorts) != 0 {
		t.Fatalf("ExposedPorts = %#v, want none", containerInfo.Config.ExposedPorts)
	}
	if !exactEnvironment(containerInfo.Config.Env, workerEnvironment(request, options)) {
		t.Fatalf(
			"Worker environment = %#v, want exact server-owned allowlist %#v",
			containerInfo.Config.Env,
			workerEnvironment(request, options),
		)
	}
	host := containerInfo.HostConfig
	if host == nil {
		t.Fatal("HostConfig = nil")
	}
	if host.Privileged || host.ReadonlyRootfs {
		t.Fatalf("unsafe host config: privileged=%v readonly=%v", host.Privileged, host.ReadonlyRootfs)
	}
	if host.NetworkMode != container.NetworkMode(options.Network) {
		t.Fatalf("NetworkMode = %q, want %q", host.NetworkMode, options.Network)
	}
	if host.PublishAllPorts || len(host.PortBindings) != 0 {
		t.Fatalf("host ports enabled: PublishAllPorts=%v PortBindings=%#v", host.PublishAllPorts, host.PortBindings)
	}
	if len(host.Binds) != 0 {
		t.Fatalf("legacy Binds = %#v, want typed mounts only", host.Binds)
	}
	if host.StorageOpt != nil || WritableLayerLimitStatus != "not_verified" {
		t.Fatalf("writable layer inspection: StorageOpt=%#v status=%q", host.StorageOpt, WritableLayerLimitStatus)
	}
	t.Log("writable_layer_limit=not_verified StorageOpt=nil")
	if host.NanoCPUs != 2_000_000_000 || host.Memory != 4_294_967_296 || host.PidsLimit == nil || *host.PidsLimit != 512 {
		t.Fatalf("resource limits: NanoCPUs=%d Memory=%d PidsLimit=%v", host.NanoCPUs, host.Memory, host.PidsLimit)
	}
	if got, want := sortedLiveStrings(host.CapDrop), []string{"ALL"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CapDrop = %v, want %v", got, want)
	}
	if got, want := sortedLiveStrings(host.CapAdd), []string{"CAP_KILL", "CAP_SETGID", "CAP_SETUID"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CapAdd = %v, want %v", got, want)
	}
	if got, want := sortedLiveStrings(host.SecurityOpt), []string{"no-new-privileges"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SecurityOpt = %v, want %v", got, want)
	}

	gotMounts := normalizeLiveMounts(containerInfo.Mounts)
	wantMounts := []liveMount{
		{Type: "bind", Source: agentState, Target: "/agent-state", RW: true},
		{Type: "volume", Name: volumeName, Target: "/run/switchback-secrets", RW: false},
		{Type: "bind", Source: traceSpool, Target: "/trace-spool", RW: true},
		{Type: "bind", Source: workingCopy, Target: "/workspace", RW: true},
	}
	if !reflect.DeepEqual(gotMounts, wantMounts) {
		t.Fatalf("daemon-normalized mounts = %#v, want %#v", gotMounts, wantMounts)
	}
	for _, mounted := range gotMounts {
		for _, forbidden := range []string{"/", "/var/run/docker.sock", dataRoot, fixturePath} {
			if mounted.Source == forbidden {
				t.Fatalf("forbidden live mount source = %q", mounted.Source)
			}
		}
	}
	for _, transient := range []string{bootstrap, modelSecret} {
		if _, err := os.Lstat(transient); !os.IsNotExist(err) {
			t.Fatalf("transient source %q still exists before Worker prompt: %v", transient, err)
		}
	}
	secretVolume, err := engine.VolumeInspect(ctx, volumeName, client.VolumeInspectOptions{})
	if err != nil {
		t.Fatalf("VolumeInspect() error = %v", err)
	}
	if err := verifySecretVolumeIdentity(volumeName, secretVolume.Volume, request.Identity); err != nil {
		t.Fatalf("runtime secret volume identity: %v", err)
	}
	if _, err := engine.ContainerStop(ctx, instance.ContainerID, client.ContainerStopOptions{}); err != nil {
		t.Fatalf("stop exact Worker before restart proof: %v", err)
	}
	for _, file := range []string{bootstrap, modelSecret} {
		var content []byte
		switch file {
		case bootstrap:
			content = bootstrapContent
		case modelSecret:
			content = modelSecretContent
		}
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatalf("recreate transient source for existing Worker reconciliation: %v", err)
		}
	}
	stopped, err := engine.ContainerInspect(ctx, instance.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect stopped exact Worker: %v", err)
	}
	if stopped.Container.State == nil || stopped.Container.State.Running {
		t.Fatalf("exact Worker did not stop before restart proof: state=%#v", stopped.Container.State)
	}
	reused, err := provisioner.Start(ctx, request)
	if err != nil {
		t.Fatalf("Start() existing exact Worker error = %v", err)
	}
	if reused.ContainerID != instance.ContainerID {
		t.Fatalf("Start() existing ContainerID = %q, want %q", reused.ContainerID, instance.ContainerID)
	}
	restarted, err := engine.ContainerInspect(ctx, reused.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect restarted exact Worker: %v", err)
	}
	if restarted.Container.State == nil ||
		restarted.Container.State.StartedAt == "" ||
		restarted.Container.State.StartedAt == firstStartedAt {
		t.Fatalf("exact Worker start timestamp did not advance after restart: state=%#v", restarted.Container.State)
	}
	if err := provisioner.verifyWorkerImmutableSpec(name, restarted, request); err != nil {
		t.Fatalf("restarted Worker immutable spec: %v", err)
	}
	for _, transient := range []string{bootstrap, modelSecret} {
		if _, err := os.Lstat(transient); !os.IsNotExist(err) {
			t.Fatalf("unused transient source %q remains after stopped restart: %v", transient, err)
		}
	}

	extraNetworkName := "switchback-task14-extra-" + runtimeInstanceID
	extraNetwork, err := engine.NetworkCreate(ctx, extraNetworkName, client.NetworkCreateOptions{})
	if err != nil {
		t.Fatalf("create second live Worker network: %v", err)
	}
	extraNetworkPresent := true
	t.Cleanup(func() {
		if !extraNetworkPresent {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := engine.NetworkRemove(cleanupCtx, extraNetwork.ID, client.NetworkRemoveOptions{}); err != nil &&
			!cerrdefs.IsNotFound(err) {
			t.Errorf("force-remove second live Worker network %q: %v", extraNetworkName, err)
		}
	})
	if _, err := engine.NetworkConnect(ctx, extraNetwork.ID, client.NetworkConnectOptions{
		Container: instance.ContainerID,
	}); err != nil {
		t.Fatalf("connect exact Worker to second live network: %v", err)
	}
	if _, err := engine.ContainerStop(ctx, instance.ContainerID, client.ContainerStopOptions{}); err != nil {
		t.Fatalf("stop exact Worker before extra-network rejection: %v", err)
	}
	for _, file := range []string{bootstrap, modelSecret} {
		var content []byte
		switch file {
		case bootstrap:
			content = bootstrapContent
		case modelSecret:
			content = modelSecretContent
		}
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatalf("recreate transient source for extra-network rejection: %v", err)
		}
	}
	beforeRejectedStart, err := engine.ContainerInspect(ctx, instance.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect stopped extra-network Worker: %v", err)
	}
	rejected, err := provisioner.Start(ctx, request)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() extra-network Worker error = %v, want identity conflict", err)
	}
	if rejected != (Instance{}) {
		t.Fatalf("Start() extra-network Worker = %#v, want no returned instance", rejected)
	}
	afterRejectedStart, err := engine.ContainerInspect(ctx, instance.ContainerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect rejected extra-network Worker: %v", err)
	}
	if afterRejectedStart.Container.State == nil ||
		afterRejectedStart.Container.State.Running ||
		afterRejectedStart.Container.State.StartedAt != beforeRejectedStart.Container.State.StartedAt {
		t.Fatalf(
			"extra-network Worker was restarted: before=%#v after=%#v",
			beforeRejectedStart.Container.State,
			afterRejectedStart.Container.State,
		)
	}

	if err := provisioner.Cleanup(ctx, request.Identity); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := engine.NetworkRemove(ctx, extraNetwork.ID, client.NetworkRemoveOptions{}); err != nil {
		t.Fatalf("remove second live Worker network: %v", err)
	}
	extraNetworkPresent = false
	if _, err := engine.NetworkInspect(ctx, extraNetwork.ID, client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("second live Worker network remains after cleanup: %v", err)
	}
	if _, err := engine.NetworkRemove(ctx, primaryNetwork.ID, client.NetworkRemoveOptions{}); err != nil {
		t.Fatalf("remove primary live Worker network: %v", err)
	}
	primaryNetworkPresent = false
	if _, err := engine.NetworkInspect(ctx, primaryNetwork.ID, client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("primary live Worker network remains after cleanup: %v", err)
	}
	if _, err := engine.ContainerInspect(ctx, instance.ContainerID, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("container still inspectable after Cleanup: %v", err)
	}
	if _, err := engine.VolumeInspect(ctx, volumeName, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("runtime secret volume still inspectable after Cleanup: %v", err)
	}
	assertDockerLiveAttestorsUnchanged(t, ctx, engine, attestorsBefore)
}

func TestDockerLiveRejectsExistingWorkerBackedByLocalBindVolume(t *testing.T) {
	ctx, engine := newDockerLiveEngine(t)
	request, options := dockerTestRequest(t)
	options.Image = dockerLiveImage
	options.Network = "none"
	options.HostPathVerifier = LocalHostPathVerifier{}
	provisioner, err := NewDockerProvisioner(engine, options)
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}

	volumeName := secretVolumeName(request.Identity)
	hostileBacking := t.TempDir()
	_, err = engine.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   volumeName,
		Driver: "local",
		DriverOpts: map[string]string{
			"type":   "none",
			"o":      "bind",
			"device": hostileBacking,
		},
		Labels: resourceLabels(request.Identity, ManagedRuntimeSecretVolume),
	})
	if err != nil {
		t.Fatalf("create local bind-options volume: %v", err)
	}
	workerID := ""
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if workerID != "" {
			if _, err := engine.ContainerRemove(cleanupCtx, workerID, client.ContainerRemoveOptions{Force: true}); err != nil &&
				!cerrdefs.IsNotFound(err) {
				t.Errorf("force-remove bind-volume Worker %q: %v", workerID, err)
			}
		}
		if _, err := engine.VolumeRemove(cleanupCtx, volumeName, client.VolumeRemoveOptions{}); err != nil &&
			!cerrdefs.IsNotFound(err) {
			t.Errorf("force-remove bind-options volume %q: %v", volumeName, err)
		}
	})
	createdWorker, err := engine.ContainerCreate(ctx, provisioner.workerContainerCreateOptions(request, volumeName))
	if err != nil {
		t.Fatalf("create exact stopped Worker with bind-options volume: %v", err)
	}
	workerID = createdWorker.ID

	instance, err := provisioner.Start(ctx, request)
	if !errors.Is(err, ErrRuntimeIdentityConflict) {
		t.Fatalf("Start() bind-options Worker error = %v, want identity conflict", err)
	}
	if instance != (Instance{}) {
		t.Fatalf("Start() bind-options Worker = %#v, want no adopted instance", instance)
	}
	inspectedWorker, err := engine.ContainerInspect(ctx, workerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect rejected bind-options Worker: %v", err)
	}
	if inspectedWorker.Container.State == nil || inspectedWorker.Container.State.Running {
		t.Fatalf("bind-options Worker was started: state=%#v", inspectedWorker.Container.State)
	}
	inspectedVolume, err := engine.VolumeInspect(ctx, volumeName, client.VolumeInspectOptions{})
	if err != nil {
		t.Fatalf("bind-options volume was deleted: %v", err)
	}
	if inspectedVolume.Volume.Options["type"] != "none" ||
		inspectedVolume.Volume.Options["o"] != "bind" ||
		inspectedVolume.Volume.Options["device"] == "" {
		t.Fatalf("bind-options volume identity was changed: %#v", inspectedVolume.Volume.Options)
	}
	if _, err := os.Stat(filepath.Join(hostileBacking, "model")); !os.IsNotExist(err) {
		t.Fatalf("rejected bind-options volume exposed or populated a host secret: %v", err)
	}
}

func TestDockerLiveConcreteHostPathVerifierRejectsLeafSymlink(t *testing.T) {
	testDockerLiveConcreteHostPathVerifierRejectsSymlink(t, "leaf")
}

func TestDockerLiveConcreteHostPathVerifierRejectsAncestorSymlink(t *testing.T) {
	testDockerLiveConcreteHostPathVerifierRejectsSymlink(t, "ancestor")
}

func testDockerLiveConcreteHostPathVerifierRejectsSymlink(t *testing.T, symlinkKind string) {
	t.Helper()
	ctx, engine := newDockerLiveEngine(t)
	attestorsBefore := dockerLiveAttestorIDs(t, ctx, engine)
	root := t.TempDir()
	containerRoot := filepath.Join(root, "backend-data")
	hostRoot := filepath.Join(root, "host-data")
	for _, directory := range []string{containerRoot, hostRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mapper := PathMapper{ContainerDataRoot: containerRoot, HostDataRoot: hostRoot}
	pair := func(name string, directory bool) PathPair {
		t.Helper()
		got, err := mapper.SessionPath("symlink-session", name)
		if err != nil {
			t.Fatal(err)
		}
		if directory {
			err = os.MkdirAll(got.ContainerPath, 0o700)
		} else {
			err = os.MkdirAll(filepath.Dir(got.ContainerPath), 0o700)
			if err == nil {
				err = os.WriteFile(got.ContainerPath, []byte("test-owned"), 0o600)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	workingCopy := pair("working-copy", true)
	agentState := pair("agent-state", true)
	traceSpool := pair("trace-spool", true)
	bootstrap := pair("bootstrap.json", false)
	modelSecret := pair("model-secret", false)
	pairs := []struct {
		pair      PathPair
		directory bool
	}{
		{pair: workingCopy, directory: true},
		{pair: agentState, directory: true},
		{pair: traceSpool, directory: true},
		{pair: bootstrap},
		{pair: modelSecret},
	}
	switch symlinkKind {
	case "leaf":
		for _, item := range pairs[1:] {
			if item.directory {
				if err := os.MkdirAll(item.pair.HostPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(item.pair.HostPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(item.pair.HostPath, []byte("test-owned"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		target := filepath.Join(hostRoot, "targets", "working-copy")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, workingCopy.HostPath); err != nil {
			t.Fatal(err)
		}
	case "ancestor":
		realSessions := filepath.Join(hostRoot, "real-sessions")
		for _, item := range pairs {
			relative, err := filepath.Rel(filepath.Join(hostRoot, "sessions"), item.pair.HostPath)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(realSessions, relative)
			if item.directory {
				err = os.MkdirAll(target, 0o700)
			} else {
				err = os.MkdirAll(filepath.Dir(target), 0o700)
				if err == nil {
					err = os.WriteFile(target, []byte("test-owned"), 0o600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(realSessions, filepath.Join(hostRoot, "sessions")); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported symlink kind %q", symlinkKind)
	}

	runtimeInstanceID := "symlink-" + symlinkKind + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	request := ProvisionRequest{
		Identity: RuntimeIdentity{
			WorkerSessionID:   "symlink-session",
			WorkerRunID:       "symlink-run",
			RuntimeInstanceID: runtimeInstanceID,
			RuntimeGeneration: 1,
		},
		WorkerToken: "live-worker-token",
		WorkingCopy: workingCopy,
		AgentState:  agentState,
		TraceSpool:  traceSpool,
		Bootstrap:   bootstrap,
		ModelSecret: modelSecret,
	}
	hostPathVerifier, err := NewDockerHostPathVerifier(ctx, engine, dockerLiveImage)
	if err != nil {
		t.Fatalf("NewDockerHostPathVerifier() error = %v", err)
	}
	provisioner, err := NewDockerProvisioner(engine, DockerOptions{
		Image:              dockerLiveImage,
		Network:            "none",
		Mapper:             mapper,
		FixturePath:        filepath.Join(root, "fixture"),
		BackendGRPCTarget:  "spike-backend:9090",
		BackendHTTPBaseURL: "http://spike-backend:8080",
		HostPathVerifier:   hostPathVerifier,
	})
	if err != nil {
		t.Fatalf("NewDockerProvisioner() error = %v", err)
	}
	if _, err := provisioner.Start(ctx, request); err == nil {
		t.Fatalf("Start() error = nil, want %s symlink rejection", symlinkKind)
	}
	name := "switchback-runtime-" + runtimeInstanceID
	if _, err := engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("Worker container %q exists after %s symlink rejection: %v", name, symlinkKind, err)
	}
	assertDockerLiveAttestorsUnchanged(t, ctx, engine, attestorsBefore)
}

type liveMount struct {
	Type   string
	Name   string
	Source string
	Target string
	RW     bool
}

func normalizeLiveMounts(points []container.MountPoint) []liveMount {
	got := make([]liveMount, 0, len(points))
	for _, point := range points {
		source := point.Source
		if point.Type == mount.TypeVolume {
			source = ""
		}
		got = append(got, liveMount{
			Type:   string(point.Type),
			Name:   point.Name,
			Source: source,
			Target: point.Destination,
			RW:     point.RW,
		})
	}
	sort.Slice(got, func(i, j int) bool {
		return got[i].Target < got[j].Target
	})
	return got
}

func sortedLiveStrings(values []string) []string {
	got := append([]string(nil), values...)
	sort.Strings(got)
	return got
}

func newDockerLiveEngine(t *testing.T) (context.Context, *client.Client) {
	t.Helper()
	switch value := os.Getenv("SWITCHBACK_SPIKE_DOCKER_TEST"); value {
	case "":
		t.Skip("SWITCHBACK_SPIKE_DOCKER_TEST=1 is absent")
	case "1":
	default:
		t.Fatalf("SWITCHBACK_SPIKE_DOCKER_TEST must equal 1 when set, got %q", value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	engine, err := NewDockerClient()
	if err != nil {
		t.Fatalf("NewDockerClient() error = %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("close Docker client: %v", err)
		}
	})
	ensureDockerLiveImage(t, ctx, engine)
	return ctx, engine
}

func dockerLiveAttestorIDs(t *testing.T, ctx context.Context, engine *client.Client) []string {
	t.Helper()
	filters := client.Filters{}
	filters = filters.Add("label", LabelManaged+"=hostpath-attestation")
	listed, err := engine.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		t.Fatalf("list Docker HostPath attestors: %v", err)
	}
	ids := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

func assertDockerLiveAttestorsUnchanged(
	t *testing.T,
	ctx context.Context,
	engine *client.Client,
	want []string,
) {
	t.Helper()
	if got := dockerLiveAttestorIDs(t, ctx, engine); !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker HostPath attestors after probe = %v, want baseline %v", got, want)
	}
}

func ensureDockerLiveImage(t *testing.T, ctx context.Context, engine *client.Client) {
	t.Helper()
	if _, err := engine.ImageInspect(ctx, dockerLiveImage); err == nil {
		return
	} else if !cerrdefs.IsNotFound(err) {
		t.Fatalf("inspect pinned live image: %v", err)
	}
	pull, err := engine.ImagePull(ctx, dockerLiveImage, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull pinned live image: %v", err)
	}
	defer pull.Close()
	if err := pull.Wait(ctx); err != nil {
		t.Fatalf("wait for pinned live image pull: %v", err)
	}
	if _, err := engine.ImageInspect(ctx, dockerLiveImage); err != nil {
		t.Fatalf("inspect pulled pinned live image: %v", err)
	}
}

func dockerLiveWorkerImage(t *testing.T, ctx context.Context, engine *client.Client) string {
	t.Helper()
	image := os.Getenv("SWITCHBACK_SPIKE_DOCKER_WORKER_IMAGE")
	if image == "" {
		inspected, err := engine.ImageInspect(ctx, "switchback-spike-worker:local")
		if cerrdefs.IsNotFound(err) {
			t.Skip("build switchback-spike-worker:local or set SWITCHBACK_SPIKE_DOCKER_WORKER_IMAGE for Worker lifecycle proof")
		}
		if err != nil {
			t.Fatalf("inspect local live Worker image: %v", err)
		}
		image = inspected.ID
	}
	if err := validateDockerImage(image); err != nil {
		t.Fatalf("live Worker image must be immutable: %v", err)
	}
	if _, err := engine.ImageInspect(ctx, image); err != nil {
		t.Fatalf("inspect immutable live Worker image: %v", err)
	}
	return image
}
