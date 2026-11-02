// Package docker implements the Backend-owned Docker RuntimeProvisioner.
package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

const (
	labelManaged           = "com.switchback.managed"
	labelWorkerSessionID   = "com.switchback.worker-session-id"
	labelWorkerRunID       = "com.switchback.worker-run-id"
	labelRuntimeInstanceID = "com.switchback.runtime-instance-id"
	labelRuntimeGeneration = "com.switchback.runtime-generation"
	labelResourceProfile   = "com.switchback.resource-profile"
	labelAgentProfile      = "com.switchback.agent-profile"
	labelNetworkPolicyID   = "com.switchback.network-policy-id"
	labelNetworkRevision   = "com.switchback.network-policy-revision"
	labelNetworkDigest     = "com.switchback.network-policy-digest"
	labelNetworkStatus     = "com.switchback.network-enforcement-status"

	managedWorkerRuntime = "worker-runtime"
	workerUser           = "0:0"
	workerTiniPath       = "/usr/bin/tini"
	workerShimPath       = "/usr/local/bin/worker-shim"
)

type Engine interface {
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

type HostPathVerifier interface {
	VerifyHostPaths(context.Context, string, []HostPathRequirement) error
}

type HostPathKind string

const (
	HostPathDirectory   HostPathKind = "directory"
	HostPathRegularFile HostPathKind = "regular-file"
)

type HostPathRequirement struct {
	Path string
	Kind HostPathKind
}

type Options struct {
	Image              string
	Network            string
	EngineDataRoot     string
	BackendGRPCTarget  string
	BackendHTTPBaseURL string
	HostPathVerifier   HostPathVerifier
}

type Provisioner struct {
	engine          Engine
	options         Options
	profiles        config.ProfileRegistry
	networkProfiles config.RuntimeNetworkProfileRegistry
	mu              sync.Mutex
}

func NewClient() (*client.Client, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

func NewProvisioner(engine Engine, options Options) (*Provisioner, error) {
	if engine == nil {
		return nil, fmt.Errorf("Docker Engine is required")
	}
	if isNilInterface(options.HostPathVerifier) {
		return nil, fmt.Errorf("Docker HostPath verifier is required")
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	return &Provisioner{
		engine: engine, options: options, profiles: config.ProductionProfileRegistry(),
		networkProfiles: config.ProductionRuntimeNetworkProfiles(),
	}, nil
}

func (p *Provisioner) Provision(ctx context.Context, request application.RuntimeProvisionRequest) (application.ProvisionedRuntime, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	resources, err := p.validateRequest(request)
	if err != nil {
		return application.ProvisionedRuntime{}, err
	}
	name := containerName(request.RuntimeInstanceID)
	inspected, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		if err := p.verifyExisting(inspected, request, resources); err != nil {
			return application.ProvisionedRuntime{}, err
		}
		if inspected.Container.State == nil {
			return application.ProvisionedRuntime{}, fmt.Errorf("runtime container identity conflict")
		}
		if !inspected.Container.State.Running {
			if _, err := p.engine.ContainerStart(ctx, inspected.Container.ID, client.ContainerStartOptions{}); err != nil {
				return application.ProvisionedRuntime{}, dockerError("start existing runtime container", err)
			}
		}
		return provisioned(request, inspected.Container.ID, parseCreated(inspected.Container.Created)), nil
	}
	if !cerrdefs.IsNotFound(err) {
		return application.ProvisionedRuntime{}, dockerError("inspect runtime container", err)
	}

	paths := hostPathRequirements(request)
	if err := p.options.HostPathVerifier.VerifyHostPaths(ctx, p.options.EngineDataRoot, paths); err != nil {
		return application.ProvisionedRuntime{}, fmt.Errorf("verify Docker HostPaths: %w", err)
	}
	secretVolume, err := p.stageSecretVolume(ctx, request)
	if err != nil {
		return application.ProvisionedRuntime{}, err
	}
	removeVolume := true
	defer func() {
		if removeVolume {
			_, _ = p.engine.VolumeRemove(context.WithoutCancel(ctx), secretVolume, client.VolumeRemoveOptions{Force: true})
		}
	}()
	created, err := p.engine.ContainerCreate(ctx, p.createOptions(request, resources, secretVolume))
	if err != nil {
		return application.ProvisionedRuntime{}, dockerError("create runtime container", err)
	}
	if created.ID == "" {
		return application.ProvisionedRuntime{}, fmt.Errorf("create runtime container returned an empty id")
	}
	if _, err := p.engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, cleanupErr := p.engine.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
		return application.ProvisionedRuntime{}, errors.Join(dockerError("start runtime container", err), cleanupErr)
	}
	removeVolume = false
	return provisioned(request, created.ID, time.Now().UTC()), nil
}

func (p *Provisioner) Inspect(ctx context.Context, runtimeInstanceID string) (application.RuntimeInspection, error) {
	if err := validateIdentity("runtime instance id", runtimeInstanceID); err != nil {
		return application.RuntimeInspection{}, err
	}
	inspected, err := p.engine.ContainerInspect(ctx, containerName(runtimeInstanceID), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return application.RuntimeInspection{ObservedAt: time.Now().UTC()}, nil
	}
	if err != nil {
		return application.RuntimeInspection{}, dockerError("inspect runtime container", err)
	}
	if inspected.Container.Config == nil || inspected.Container.Config.Labels[labelRuntimeInstanceID] != runtimeInstanceID || inspected.Container.State == nil {
		return application.RuntimeInspection{}, fmt.Errorf("runtime container identity conflict")
	}
	generation, err := strconv.ParseUint(inspected.Container.Config.Labels[labelRuntimeGeneration], 10, 64)
	if err != nil || generation == 0 {
		return application.RuntimeInspection{}, fmt.Errorf("runtime container identity conflict")
	}
	var exitCode *int
	if !inspected.Container.State.Running {
		value := inspected.Container.State.ExitCode
		exitCode = &value
	}
	return application.RuntimeInspection{
		Exists:      true,
		Running:     inspected.Container.State.Running,
		ContainerID: inspected.Container.ID,
		Generation:  generation,
		ExitCode:    exitCode,
		ObservedAt:  time.Now().UTC(),
	}, nil
}

func (p *Provisioner) Cleanup(
	ctx context.Context,
	request application.RuntimeCleanupRequest,
) (application.RuntimeCleanupObservation, error) {
	observation := application.RuntimeCleanupObservation{
		ContainerRef:    containerName(request.RuntimeInstanceID),
		SecretVolumeRef: secretVolumeNameFor(request.RuntimeInstanceID, request.Generation),
	}
	if err := validateIdentity("runtime instance id", request.RuntimeInstanceID); err != nil {
		return observation, err
	}
	if request.ExpectedContainerID != "" {
		if err := validateIdentity(
			"expected container id",
			request.ExpectedContainerID,
		); err != nil {
			return observation, err
		}
	}
	if request.Generation == 0 {
		return observation, fmt.Errorf("runtime generation must be positive")
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	containerID, containerExists, err := p.inspectCleanupContainer(ctx, request)
	if err != nil {
		return observation, err
	}
	if containerExists {
		_, _ = p.engine.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	}
	volumeExists, err := p.inspectCleanupVolume(ctx, request)
	if err != nil {
		return observation, err
	}
	if volumeExists {
		_, _ = p.engine.VolumeRemove(ctx, observation.SecretVolumeRef, client.VolumeRemoveOptions{Force: true})
	}

	_, observation.ContainerExists, err = p.inspectCleanupContainer(ctx, request)
	if err != nil {
		return observation, err
	}
	observation.SecretVolumeExists, err = p.inspectCleanupVolume(ctx, request)
	if err != nil {
		return observation, err
	}
	observation.ObservedAt = time.Now().UTC()
	observation.Confirmed = !observation.ContainerExists && !observation.SecretVolumeExists
	if !observation.Confirmed {
		return observation, fmt.Errorf("Runtime cleanup residue remains")
	}
	return observation, nil
}

func (p *Provisioner) inspectCleanupContainer(
	ctx context.Context,
	request application.RuntimeCleanupRequest,
) (string, bool, error) {
	inspected, err := p.engine.ContainerInspect(ctx, containerName(request.RuntimeInstanceID), client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, dockerError("inspect Runtime container for cleanup", err)
	}
	labels := map[string]string(nil)
	if inspected.Container.Config != nil {
		labels = inspected.Container.Config.Labels
	}
	if inspected.Container.ID == "" || labels[labelManaged] != managedWorkerRuntime ||
		labels[labelRuntimeInstanceID] != request.RuntimeInstanceID ||
		labels[labelRuntimeGeneration] != strconv.FormatUint(request.Generation, 10) ||
		(request.ExpectedContainerID != "" &&
			inspected.Container.ID != request.ExpectedContainerID) {
		return "", false, fmt.Errorf("Runtime container identity conflict")
	}
	return inspected.Container.ID, true, nil
}

func (p *Provisioner) inspectCleanupVolume(
	ctx context.Context,
	request application.RuntimeCleanupRequest,
) (bool, error) {
	name := secretVolumeNameFor(request.RuntimeInstanceID, request.Generation)
	inspected, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, dockerError("inspect Runtime secret volume for cleanup", err)
	}
	labels := inspected.Volume.Labels
	if inspected.Volume.Name != name || labels[labelManaged] != "runtime-secret-volume" ||
		labels[labelRuntimeInstanceID] != request.RuntimeInstanceID ||
		labels[labelRuntimeGeneration] != strconv.FormatUint(request.Generation, 10) {
		return false, fmt.Errorf("Runtime secret volume identity conflict")
	}
	return true, nil
}

func (p *Provisioner) validateRequest(request application.RuntimeProvisionRequest) (config.RuntimeResourceProfile, error) {
	for name, value := range map[string]string{
		"worker session id":   request.WorkerSessionID,
		"worker run id":       request.WorkerRunID,
		"runtime instance id": request.RuntimeInstanceID,
	} {
		if err := validateIdentity(name, value); err != nil {
			return config.RuntimeResourceProfile{}, err
		}
	}
	if request.Generation == 0 {
		return config.RuntimeResourceProfile{}, fmt.Errorf("runtime generation must be positive")
	}
	resources, err := p.profiles.ResolveRuntimeResource(config.RuntimeResourceProfileRef{
		ID: request.ResourceProfile.ID, Revision: request.ResourceProfile.Revision, Digest: request.ResourceProfile.Digest,
	})
	if err != nil {
		return config.RuntimeResourceProfile{}, err
	}
	if _, err := p.profiles.ResolveAgent(config.AgentProfileRef{
		ID: request.AgentProfile.ID, Revision: request.AgentProfile.Revision, Digest: request.AgentProfile.Digest,
	}); err != nil {
		return config.RuntimeResourceProfile{}, err
	}
	network, err := p.networkProfiles.ResolveStored(config.RuntimeNetworkEvidence{
		ID: request.NetworkProfile.ID, Revision: request.NetworkProfile.Revision,
		Digest: request.NetworkProfile.Digest, EnforcementStatus: request.NetworkEnforcementStatus,
	})
	if err != nil || network.EnforcementStatus != request.NetworkEnforcementStatus {
		return config.RuntimeResourceProfile{}, fmt.Errorf("Runtime network profile is unavailable or stale")
	}
	if err := validateStorage(p.options.EngineDataRoot, request.WorkerSessionID, request.Storage); err != nil {
		return config.RuntimeResourceProfile{}, err
	}
	if err := validateRuntimeSecrets(request); err != nil {
		return config.RuntimeResourceProfile{}, err
	}
	return resources, nil
}

func (p *Provisioner) createOptions(request application.RuntimeProvisionRequest, resources config.RuntimeResourceProfile, secretVolume string) client.ContainerCreateOptions {
	pids := resources.PIDsLimit
	return client.ContainerCreateOptions{
		Name:  containerName(request.RuntimeInstanceID),
		Image: p.options.Image,
		Config: &container.Config{
			User:       workerUser,
			Labels:     runtimeLabels(request),
			WorkingDir: request.Storage.WorkingCopy.WorkerPath,
			Env: []string{
				"SWITCHBACK_WORKER_SESSION_ID=" + request.WorkerSessionID,
				"SWITCHBACK_WORKER_RUN_ID=" + request.WorkerRunID,
				"SWITCHBACK_RUNTIME_INSTANCE_ID=" + request.RuntimeInstanceID,
				"SWITCHBACK_RUNTIME_GENERATION=" + strconv.FormatUint(request.Generation, 10),
				"SWITCHBACK_BACKEND_GRPC_TARGET=" + p.options.BackendGRPCTarget,
				"SWITCHBACK_BACKEND_HTTP_BASE_URL=" + p.options.BackendHTTPBaseURL,
			},
			Entrypoint: []string{workerTiniPath, "--"},
			Cmd:        []string{workerShimPath},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:     container.NetworkMode(p.options.Network),
			ExtraHosts:      []string{"host.docker.internal:host-gateway"},
			Privileged:      false,
			PublishAllPorts: false,
			ReadonlyRootfs:  false,
			CapDrop:         []string{"ALL"},
			CapAdd:          []string{"KILL", "SETUID", "SETGID"},
			SecurityOpt:     []string{"no-new-privileges"},
			CgroupnsMode:    container.CgroupnsModePrivate,
			IpcMode:         container.IPCModePrivate,
			RestartPolicy:   container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Resources: container.Resources{
				NanoCPUs:  resources.CPUUnits * 1_000_000_000,
				Memory:    resources.MemoryBytes,
				PidsLimit: &pids,
			},
			Mounts: storageMounts(request.Storage, secretVolume),
		},
	}
}

func (p *Provisioner) verifyExisting(inspected client.ContainerInspectResult, request application.RuntimeProvisionRequest, resources config.RuntimeResourceProfile) error {
	if inspected.Container.ID == "" || inspected.Container.Config == nil || inspected.Container.HostConfig == nil ||
		!reflect.DeepEqual(inspected.Container.Config.Labels, runtimeLabels(request)) {
		return fmt.Errorf("runtime container identity conflict")
	}
	want := p.createOptions(request, resources, secretVolumeName(request))
	gotHost := inspected.Container.HostConfig
	if inspected.Container.Config.Image != p.options.Image || inspected.Container.Config.User != workerUser ||
		gotHost.Privileged || gotHost.PublishAllPorts || len(gotHost.PortBindings) != 0 ||
		gotHost.NetworkMode != want.HostConfig.NetworkMode ||
		!reflect.DeepEqual(gotHost.ExtraHosts, want.HostConfig.ExtraHosts) ||
		!reflect.DeepEqual(gotHost.Mounts, want.HostConfig.Mounts) ||
		gotHost.Resources.NanoCPUs != want.HostConfig.Resources.NanoCPUs || gotHost.Resources.Memory != want.HostConfig.Resources.Memory ||
		gotHost.Resources.PidsLimit == nil || *gotHost.Resources.PidsLimit != resources.PIDsLimit {
		return fmt.Errorf("runtime container immutable configuration conflict")
	}
	return nil
}

func validateOptions(options Options) error {
	if err := validateImage(options.Image); err != nil {
		return err
	}
	if err := validateIdentity("Docker network", options.Network); err != nil {
		return err
	}
	networkMode := container.NetworkMode(options.Network)
	if networkMode.IsHost() || networkMode.IsContainer() || networkMode.IsDefault() || networkMode.IsNone() || options.Network == "bridge" {
		return fmt.Errorf("Docker network must be an isolated named network")
	}
	if options.EngineDataRoot == "" || !filepath.IsAbs(options.EngineDataRoot) || filepath.Clean(options.EngineDataRoot) != options.EngineDataRoot || options.EngineDataRoot == string(filepath.Separator) {
		return fmt.Errorf("Docker Engine data root must be a non-root clean absolute path")
	}
	if err := validateGRPCTarget(options.BackendGRPCTarget); err != nil {
		return err
	}
	if err := validateHTTPOrigin(options.BackendHTTPBaseURL); err != nil {
		return err
	}
	return nil
}

func validateImage(value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t ") {
		return fmt.Errorf("Docker image must be an immutable digest reference")
	}
	if digest, ok := strings.CutPrefix(value, "sha256:"); ok {
		if len(digest) != 64 {
			return fmt.Errorf("Docker image must be an immutable digest reference")
		}
		for _, character := range digest {
			if !strings.ContainsRune("0123456789abcdef", character) {
				return fmt.Errorf("Docker image must be an immutable digest reference")
			}
		}
		return nil
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return fmt.Errorf("Docker image must be an immutable digest reference")
	}
	canonical, ok := named.(reference.Canonical)
	if !ok || canonical.Digest().Algorithm() != "sha256" || len(canonical.Digest().Encoded()) != 64 {
		return fmt.Errorf("Docker image must be an immutable digest reference")
	}
	return nil
}

func validateStorage(engineRoot, workerSessionID string, storage application.SessionStorage) error {
	wantRoot := filepath.Join(engineRoot, "sessions", workerSessionID)
	if storage.EngineRoot != wantRoot || storage.EngineRoot == engineRoot || !isBelow(engineRoot, storage.EngineRoot) {
		return fmt.Errorf("Session storage root is not server-derived")
	}
	if storage.BackendRoot == "" || !filepath.IsAbs(storage.BackendRoot) || filepath.Clean(storage.BackendRoot) != storage.BackendRoot {
		return fmt.Errorf("Backend Session storage root must be a clean absolute path")
	}
	for _, expected := range []struct {
		name       string
		mount      application.StorageMount
		leaf       string
		workerPath string
	}{
		{name: "working copy", mount: storage.WorkingCopy, leaf: "working-copy", workerPath: "/workspace"},
		{name: "agent state", mount: storage.AgentState, leaf: "agent-state", workerPath: "/var/lib/switchback/agent-state"},
		{name: "trace spool", mount: storage.TraceSpool, leaf: "trace-spool", workerPath: "/var/lib/switchback/trace-spool"},
	} {
		if expected.mount.EngineSource != filepath.Join(wantRoot, expected.leaf) ||
			expected.mount.BackendPath != filepath.Join(storage.BackendRoot, expected.leaf) ||
			expected.mount.WorkerPath != expected.workerPath {
			return fmt.Errorf("%s mount is not server-derived", expected.name)
		}
	}
	return nil
}

func validateRuntimeSecrets(request application.RuntimeProvisionRequest) error {
	runtimeLeaf := request.RuntimeInstanceID + "-g" + strconv.FormatUint(request.Generation, 10)
	wantBackendRoot := filepath.Join(request.Storage.BackendRoot, "runtimes", runtimeLeaf, "secret-input")
	wantEngineRoot := filepath.Join(request.Storage.EngineRoot, "runtimes", runtimeLeaf, "secret-input")
	if request.Secrets.BackendRoot != wantBackendRoot || request.Secrets.EngineRoot != wantEngineRoot {
		return fmt.Errorf("Runtime secret roots are not server-derived")
	}
	for _, expected := range []struct {
		name       string
		mount      application.StorageMount
		leaf       string
		workerPath string
	}{
		{name: "bootstrap", mount: request.Secrets.Bootstrap, leaf: "bootstrap.json", workerPath: "/run/switchback-secrets/bootstrap.json"},
		{name: "model credential", mount: request.Secrets.ModelCredential, leaf: "model", workerPath: "/run/switchback-secrets/model"},
	} {
		if expected.mount.BackendPath != filepath.Join(wantBackendRoot, expected.leaf) ||
			expected.mount.EngineSource != filepath.Join(wantEngineRoot, expected.leaf) || expected.mount.WorkerPath != expected.workerPath {
			return fmt.Errorf("Runtime %s path is not server-derived", expected.name)
		}
	}
	return nil
}

func storageMounts(storage application.SessionStorage, secretVolume string) []mount.Mount {
	return []mount.Mount{
		{Type: mount.TypeBind, Source: storage.WorkingCopy.EngineSource, Target: storage.WorkingCopy.WorkerPath},
		{Type: mount.TypeBind, Source: storage.AgentState.EngineSource, Target: storage.AgentState.WorkerPath},
		{Type: mount.TypeBind, Source: storage.TraceSpool.EngineSource, Target: storage.TraceSpool.WorkerPath},
		{Type: mount.TypeVolume, Source: secretVolume, Target: "/run/switchback-secrets", ReadOnly: true, VolumeOptions: &mount.VolumeOptions{NoCopy: true}},
	}
}

func hostPathRequirements(request application.RuntimeProvisionRequest) []HostPathRequirement {
	return []HostPathRequirement{
		{Path: request.Storage.WorkingCopy.EngineSource, Kind: HostPathDirectory},
		{Path: request.Storage.AgentState.EngineSource, Kind: HostPathDirectory},
		{Path: request.Storage.TraceSpool.EngineSource, Kind: HostPathDirectory},
		{Path: request.Secrets.Bootstrap.EngineSource, Kind: HostPathRegularFile},
		{Path: request.Secrets.ModelCredential.EngineSource, Kind: HostPathRegularFile},
	}
}

func runtimeLabels(request application.RuntimeProvisionRequest) map[string]string {
	return map[string]string{
		labelManaged:           managedWorkerRuntime,
		labelWorkerSessionID:   request.WorkerSessionID,
		labelWorkerRunID:       request.WorkerRunID,
		labelRuntimeInstanceID: request.RuntimeInstanceID,
		labelRuntimeGeneration: strconv.FormatUint(request.Generation, 10),
		labelResourceProfile:   request.ResourceProfile.ID + "@" + strconv.FormatUint(request.ResourceProfile.Revision, 10) + ":" + request.ResourceProfile.Digest,
		labelAgentProfile:      request.AgentProfile.ID + "@" + strconv.FormatUint(request.AgentProfile.Revision, 10) + ":" + request.AgentProfile.Digest,
		labelNetworkPolicyID:   request.NetworkProfile.ID,
		labelNetworkRevision:   strconv.FormatUint(request.NetworkProfile.Revision, 10),
		labelNetworkDigest:     request.NetworkProfile.Digest,
		labelNetworkStatus:     request.NetworkEnforcementStatus,
	}
}

func secretVolumeName(request application.RuntimeProvisionRequest) string {
	return secretVolumeNameFor(request.RuntimeInstanceID, request.Generation)
}

func secretVolumeNameFor(runtimeInstanceID string, generation uint64) string {
	return "switchback-runtime-secrets-" + runtimeInstanceID + "-g" + strconv.FormatUint(generation, 10)
}

func secretResourceLabels(request application.RuntimeProvisionRequest) map[string]string {
	return map[string]string{
		labelManaged:           "runtime-secret-volume",
		labelWorkerSessionID:   request.WorkerSessionID,
		labelWorkerRunID:       request.WorkerRunID,
		labelRuntimeInstanceID: request.RuntimeInstanceID,
		labelRuntimeGeneration: strconv.FormatUint(request.Generation, 10),
	}
}

func provisioned(request application.RuntimeProvisionRequest, containerID string, startedAt time.Time) application.ProvisionedRuntime {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return application.ProvisionedRuntime{
		RuntimeInstanceID: request.RuntimeInstanceID,
		ContainerID:       containerID,
		Generation:        request.Generation,
		StartedAt:         startedAt.UTC(),
	}
}

func parseCreated(value string) time.Time {
	created, _ := time.Parse(time.RFC3339Nano, value)
	return created
}

func containerName(runtimeInstanceID string) string {
	return "switchback-runtime-" + runtimeInstanceID
}

func validateIdentity(name, value string) error {
	if value == "" || len(value) > 160 || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be canonical", name)
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (index > 0 && (character == '-' || character == '_' || character == '.')) {
			continue
		}
		return fmt.Errorf("%s must be canonical", name)
	}
	return nil
}

func validateGRPCTarget(value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "://") {
		return fmt.Errorf("Backend gRPC target must be one host:port authority")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("Backend gRPC target must be one host:port authority")
	}
	return nil
}

func validateHTTPOrigin(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("Backend HTTP base URL must be an HTTP(S) origin")
	}
	return nil
}

func isBelow(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

type sanitizedDockerError struct {
	operation string
	cause     error
}

func (e *sanitizedDockerError) Error() string { return e.operation + " failed" }
func (e *sanitizedDockerError) Unwrap() error { return e.cause }

func dockerError(operation string, cause error) error {
	return &sanitizedDockerError{operation: operation, cause: cause}
}

var _ application.RuntimeProvisioner = (*Provisioner)(nil)
var _ application.RuntimeCleaner = (*Provisioner)(nil)
