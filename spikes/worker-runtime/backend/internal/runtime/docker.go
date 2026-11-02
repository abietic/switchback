package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
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
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

var ErrRuntimeIdentityConflict = errors.New("runtime container identity conflict")

type sanitizedDockerError struct {
	operation string
	cause     error
}

func (e *sanitizedDockerError) Error() string {
	return e.operation + " failed"
}

func (e *sanitizedDockerError) Unwrap() error {
	return e.cause
}

func (e sanitizedDockerError) Format(state fmt.State, verb rune) {
	message := e.operation + " failed"
	if verb == 'q' {
		_, _ = fmt.Fprintf(state, "%q", message)
		return
	}
	_, _ = fmt.Fprint(state, message)
}

func dockerOperationError(operation string, cause error) error {
	return &sanitizedDockerError{operation: operation, cause: cause}
}

const (
	LabelManaged           = "com.switchback.managed"
	LabelWorkerSessionID   = "com.switchback.worker-session-id"
	LabelWorkerRunID       = "com.switchback.worker-run-id"
	LabelRuntimeInstanceID = "com.switchback.runtime-instance-id"
	LabelRuntimeGeneration = "com.switchback.runtime-generation"

	ManagedWorkerRuntime       = "worker-runtime"
	ManagedRuntimeSecretVolume = "runtime-secret-volume"
	ManagedSecretPopulator     = "runtime-secret-populator"

	WorkerWorkingCopyPath = "/workspace"
	WorkerAgentStatePath  = "/agent-state"
	WorkerTraceSpoolPath  = "/trace-spool"
	WorkerSecretsPath     = "/run/switchback-secrets"
	WorkerBootstrapPath   = WorkerSecretsPath + "/bootstrap.json"
	WorkerModelSecretPath = WorkerSecretsPath + "/model"

	secretInputBootstrapPath = "/opt/switchback/secret-input/bootstrap.json"
	secretInputModelPath     = "/opt/switchback/secret-input/model"
	secretOutputPath         = "/opt/switchback/secret-output"

	// WritableLayerLimitStatus is an explicit Task 7 acceptance fact. The
	// adapter sends no driver-specific StorageOpt for the 10 GiB ADR target.
	WritableLayerLimitStatus = "not_verified"
)

const (
	workerNanoCPUs      int64 = 2_000_000_000
	workerMemoryBytes   int64 = 4 << 30
	workerPIDs          int64 = 512
	workerPath                = "/usr/local/go/bin:/opt/switchback/node_modules/.bin:/usr/local/bin:/usr/bin:/bin"
	workerImageGPGKey         = "7169605F62C751356D054A26A821E680E5FA6305"
	workerPythonVersion       = "3.13.14"
	workerPythonSHA256        = "639e43243c620a308f968213df9e00f2f8f62332f7adbaa7a7eeb9783057c690"
	workerUser                = "0:0"
	workerTiniPath            = "/usr/bin/tini"
	workerShimPath            = "/usr/local/bin/worker-shim"
	workerRuntime             = "runc"
	workerLogDriver           = "json-file"
	workerShmSize       int64 = 64 << 20

	secretPopulatorReconcileTimeout = 5 * time.Second
)

// DockerEngine is the recording seam used by the adapter tests.
type DockerEngine interface {
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

// HostPathKind is the required type of one Docker-host bind source.
type HostPathKind string

const (
	HostPathDirectory   HostPathKind = "directory"
	HostPathRegularFile HostPathKind = "regular-file"
)

// HostPathRequirement describes one final bind source passed to Docker.
type HostPathRequirement struct {
	Path string
	Kind HostPathKind
}

// HostPathVerifier checks Docker-host paths before a Worker mount is acquired.
// This string-path check is not atomic with the later Docker create/start.
type HostPathVerifier interface {
	VerifyHostPaths(context.Context, string, []HostPathRequirement) error
}

// HostPathVerifierFunc adapts the pre-agreed construction seam for tests.
type HostPathVerifierFunc func(context.Context, string, []HostPathRequirement) error

func (f HostPathVerifierFunc) VerifyHostPaths(ctx context.Context, root string, requirements []HostPathRequirement) error {
	return f(ctx, root, requirements)
}

// LocalHostPathVerifier verifies HostDataRoot through the local filesystem.
// It is valid only when the caller can see the same paths as the Docker host.
type LocalHostPathVerifier struct{}

func (LocalHostPathVerifier) VerifyHostPaths(
	ctx context.Context,
	root string,
	requirements []HostPathRequirement,
) error {
	if err := validateRoot("host data", root); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect host data root %q: %w", root, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("host data root %q must not be a symlink", root)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("host data root %q must be a directory", root)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve canonical host data root %q: %w", root, err)
	}

	for _, requirement := range requirements {
		if err := ctx.Err(); err != nil {
			return err
		}
		if requirement.Path == "" ||
			!filepath.IsAbs(requirement.Path) ||
			filepath.Clean(requirement.Path) != requirement.Path {
			return fmt.Errorf("host path must be a nonempty clean absolute path")
		}
		relativePath, err := filepath.Rel(root, requirement.Path)
		if err != nil ||
			relativePath == "." ||
			relativePath == ".." ||
			strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("host path %q escapes configured host data root", requirement.Path)
		}

		current := root
		var info os.FileInfo
		for _, segment := range strings.Split(relativePath, string(filepath.Separator)) {
			current = filepath.Join(current, segment)
			info, err = os.Lstat(current)
			if err != nil {
				return fmt.Errorf("inspect host path %q: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("host path ancestor %q must not be a symlink", current)
			}
		}

		canonicalPath, err := filepath.EvalSymlinks(requirement.Path)
		if err != nil {
			return fmt.Errorf("resolve canonical host path %q: %w", requirement.Path, err)
		}
		if !isBelow(canonicalRoot, canonicalPath) || canonicalPath == canonicalRoot {
			return fmt.Errorf("canonical host path %q escapes canonical root", canonicalPath)
		}
		switch requirement.Kind {
		case HostPathDirectory:
			if !info.IsDir() {
				return fmt.Errorf("host path %q must be a directory", requirement.Path)
			}
		case HostPathRegularFile:
			if !info.Mode().IsRegular() {
				return fmt.Errorf("host path %q must be a regular file", requirement.Path)
			}
		default:
			return fmt.Errorf("host path %q has unsupported expected type %q", requirement.Path, requirement.Kind)
		}
	}
	return nil
}

const (
	dockerHostPathAttestationRoot   = "/opt/switchback/attest/root"
	dockerHostPathCleanupTimeout    = 10 * time.Second
	dockerHostPathAttestationScript = `set -eu
root=/opt/switchback/attest/root
[ -d "$root" ]
while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || exit 64
  kind=$1
  rest=$2
  shift 2
  current=$root
  while [ -n "$rest" ]; do
    case "$rest" in
      */*) segment=${rest%%/*}; rest=${rest#*/} ;;
      *) segment=$rest; rest= ;;
    esac
    [ -n "$segment" ] && [ "$segment" != "." ] && [ "$segment" != ".." ] || exit 64
    current="${current}/${segment}"
    [ ! -L "$current" ]
  done
  case "$kind" in
    directory) [ -d "$current" ] ;;
    regular-file) [ -f "$current" ] ;;
    *) exit 64 ;;
  esac
done`
)

// DockerHostPathVerifier checks host bind paths through a short-lived,
// networkless Engine probe. HostDataRoot is the server-owned, non-symlink M1
// trust anchor; the probe checks descendants only. A trusted concurrent host
// writer can still race this check and Docker's later string-path resolution.
type DockerHostPathVerifier struct {
	lifecycleCtx context.Context
	engine       DockerEngine
	image        string
}

func NewDockerHostPathVerifier(
	lifecycleCtx context.Context,
	engine DockerEngine,
	image string,
) (*DockerHostPathVerifier, error) {
	if lifecycleCtx == nil {
		return nil, fmt.Errorf("Docker HostPath lifecycle context is required")
	}
	if engine == nil {
		return nil, fmt.Errorf("docker engine is required")
	}
	if err := validateDockerImage(image); err != nil {
		return nil, err
	}
	return &DockerHostPathVerifier{
		lifecycleCtx: lifecycleCtx,
		engine:       engine,
		image:        image,
	}, nil
}

func (v *DockerHostPathVerifier) VerifyHostPaths(
	ctx context.Context,
	root string,
	requirements []HostPathRequirement,
) error {
	if v == nil || v.engine == nil {
		return fmt.Errorf("Docker HostPath verification engine is required")
	}
	if err := validateHostPathRequirements(root, requirements); err != nil {
		return err
	}
	if len(requirements) == 0 || len(requirements) > 5 {
		return fmt.Errorf("Docker HostPath verification requires one to five paths")
	}

	command := []string{"-eu", "-c", dockerHostPathAttestationScript, "hostpath-attestor"}
	for _, requirement := range requirements {
		relativePath, err := filepath.Rel(root, requirement.Path)
		if err != nil {
			return fmt.Errorf("derive Docker HostPath requirement relative path")
		}
		command = append(command, string(requirement.Kind), filepath.ToSlash(relativePath))
	}

	name := "switchback-hostpath-attest-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	created, err := v.engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:  name,
		Image: v.image,
		Config: &container.Config{
			Entrypoint: []string{"/bin/sh"},
			Cmd:        command,
			Labels: map[string]string{
				LabelManaged: "hostpath-attestation",
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("none"),
			Privileged:     false,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   root,
				Target:   dockerHostPathAttestationRoot,
				ReadOnly: true,
				BindOptions: &mount.BindOptions{
					Propagation:            mount.PropagationRPrivate,
					ReadOnlyForceRecursive: true,
				},
			}},
		},
	})
	if err != nil {
		return dockerOperationError("create Docker HostPath attestation", err)
	}
	if created.ID == "" {
		return fmt.Errorf("Docker HostPath attestation returned an empty container id")
	}
	if _, err := v.engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return errors.Join(dockerOperationError("start Docker HostPath attestation", err), v.removeAttestor(created.ID))
	}
	wait := v.engine.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-ctx.Done():
		return errors.Join(ctx.Err(), v.removeAttestor(created.ID))
	case err := <-wait.Error:
		if err != nil {
			return errors.Join(dockerOperationError("wait for Docker HostPath attestation", err), v.removeAttestor(created.ID))
		}
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			return errors.Join(fmt.Errorf("Docker HostPath attestation rejected a bind source"), v.removeAttestor(created.ID))
		}
	}
	return v.removeAttestor(created.ID)
}

func (v *DockerHostPathVerifier) removeAttestor(containerID string) error {
	// Shutdown cancellation must not prevent bounded removal of an attestor
	// that already has a container ID.
	cleanupBase := context.WithoutCancel(v.lifecycleCtx)
	cleanupCtx, cancel := context.WithTimeout(cleanupBase, dockerHostPathCleanupTimeout)
	defer cancel()
	_, err := v.engine.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return dockerOperationError("remove Docker HostPath attestation", err)
	}
	return nil
}

// DockerOptions contains only server-owned runtime configuration.
type DockerOptions struct {
	Image              string
	Network            string
	Mapper             PathMapper
	FixturePath        string
	BackendGRPCTarget  string
	BackendHTTPBaseURL string
	HostPathVerifier   HostPathVerifier
}

// DockerProvisioner creates and removes M1 Worker containers through Moby.
type DockerProvisioner struct {
	engine         DockerEngine
	options        DockerOptions
	operationLocks *runtimeOperationLocks
}

type runtimeOperationLocks struct {
	mu      sync.Mutex
	entries map[string]*runtimeOperationLock
}

type runtimeOperationLock struct {
	semaphore chan struct{}
	refs      int
}

func newRuntimeOperationLocks() *runtimeOperationLocks {
	return &runtimeOperationLocks{entries: make(map[string]*runtimeOperationLock)}
}

func (l *runtimeOperationLocks) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	entry := l.entries[key]
	if entry == nil {
		entry = &runtimeOperationLock{semaphore: make(chan struct{}, 1)}
		entry.semaphore <- struct{}{}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	releaseRef := func() {
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		releaseRef()
		return nil, err
	}
	select {
	case <-entry.semaphore:
		if err := ctx.Err(); err != nil {
			entry.semaphore <- struct{}{}
			releaseRef()
			return nil, err
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				entry.semaphore <- struct{}{}
				releaseRef()
			})
		}, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

// NewDockerClient constructs the pinned Moby client with API negotiation.
func NewDockerClient() (*client.Client, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

// NewDockerProvisioner validates server-owned configuration before retaining it.
func NewDockerProvisioner(engine DockerEngine, options DockerOptions) (*DockerProvisioner, error) {
	if engine == nil {
		return nil, fmt.Errorf("docker engine is required")
	}
	if isNilHostPathVerifier(options.HostPathVerifier) {
		return nil, fmt.Errorf("Docker HostPath verifier is required")
	}
	if err := validateDockerOptions(options); err != nil {
		return nil, err
	}
	return &DockerProvisioner{
		engine:         engine,
		options:        options,
		operationLocks: newRuntimeOperationLocks(),
	}, nil
}

func isNilHostPathVerifier(verifier HostPathVerifier) bool {
	if verifier == nil {
		return true
	}
	value := reflect.ValueOf(verifier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Start creates and starts the exact container for a runtime identity.
func (p *DockerProvisioner) Start(ctx context.Context, request ProvisionRequest) (instance Instance, resultErr error) {
	if err := validateRuntimeIdentity(request.Identity); err != nil {
		return Instance{}, err
	}
	unlock, err := p.operationLocks.lock(ctx, containerName(request.Identity))
	if err != nil {
		return Instance{}, err
	}
	defer unlock()
	return p.startLocked(ctx, request)
}

func (p *DockerProvisioner) startLocked(
	ctx context.Context,
	request ProvisionRequest,
) (instance Instance, resultErr error) {
	hostPaths, err := p.validateRequest(request)
	if err != nil {
		return Instance{}, err
	}
	name := containerName(request.Identity)
	if inspected, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); err == nil {
		return p.startExisting(ctx, name, inspected, request, hostPaths)
	} else if !cerrdefs.IsNotFound(err) {
		return Instance{}, dockerOperationError("inspect runtime container", err)
	}

	// This is deliberately the final Engine step before create, not an atomic
	// path handle. M1 depends on a server-owned root with no untrusted writer.
	if err := p.verifyHostPaths(ctx, hostPaths); err != nil {
		return Instance{}, err
	}
	if err := p.reconcileStaleSecretPopulator(ctx, request); err != nil {
		return Instance{}, err
	}
	volumeName, volumeOwned, err := p.ensureSecretVolume(ctx, request.Identity)
	if err != nil {
		return Instance{}, err
	}
	rollbackVolume := volumeOwned
	var workerContainerID string
	defer func() {
		if rollbackVolume {
			var rollbackErr error
			if workerContainerID != "" {
				rollbackErr = p.removeContainerForRollback(ctx, workerContainerID)
			}
			if rollbackErr == nil {
				rollbackErr = p.removeSecretVolumeForRollback(ctx, request.Identity)
			}
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	if err := p.populateSecretVolume(ctx, request, volumeName); err != nil {
		return Instance{}, err
	}
	if err := removeTransientSecretSources(request); err != nil {
		return Instance{}, err
	}
	if err := p.verifyHostPaths(ctx, hostPaths[:3]); err != nil {
		return Instance{}, err
	}
	create := p.workerContainerCreateOptions(request, volumeName)
	created, err := p.engine.ContainerCreate(ctx, create)
	if err != nil {
		if cerrdefs.IsConflict(err) {
			inspected, inspectErr := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
			if inspectErr != nil {
				return Instance{}, dockerOperationError("inspect conflicting runtime container", inspectErr)
			}
			if err := p.verifyWorkerImmutableSpec(name, inspected, request); err != nil {
				return Instance{}, err
			}
			if err := p.inspectExactSecretVolume(ctx, request.Identity); err != nil {
				return Instance{}, err
			}
			if !inspected.Container.State.Running {
				if _, err := p.engine.ContainerStart(ctx, inspected.Container.ID, client.ContainerStartOptions{}); err != nil {
					return Instance{}, dockerOperationError("start conflicting runtime container", err)
				}
			}
			rollbackVolume = false
			return Instance{ContainerID: inspected.Container.ID, ContainerName: name}, nil
		}
		return Instance{}, dockerOperationError("create runtime container", err)
	}
	if created.ID == "" {
		return Instance{}, fmt.Errorf("create container %q returned an empty id", name)
	}
	workerContainerID = created.ID
	if _, err := p.engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return Instance{}, dockerOperationError("start runtime container", err)
	}
	rollbackVolume = false
	return Instance{ContainerID: created.ID, ContainerName: name}, nil
}

func (p *DockerProvisioner) workerContainerCreateOptions(
	request ProvisionRequest,
	volumeName string,
) client.ContainerCreateOptions {
	return client.ContainerCreateOptions{
		Name:  containerName(request.Identity),
		Image: p.options.Image,
		Config: &container.Config{
			User:       workerUser,
			Labels:     identityLabels(request.Identity),
			WorkingDir: WorkerWorkingCopyPath,
			Env:        workerEnvironment(request, p.options),
			Entrypoint: []string{workerTiniPath, "--"},
			Cmd:        []string{workerShimPath},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(p.options.Network),
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
			Resources:       workerResources(),
			Mounts:          workerMounts(request, volumeName),
			MaskedPaths:     workerMaskedPaths(),
			ReadonlyPaths:   workerReadonlyPaths(),
		},
	}
}

const populateSecretVolumeScript = `set -eu
output=/opt/switchback/secret-output
bootstrap_input=/opt/switchback/secret-input/bootstrap.json
model_input=/opt/switchback/secret-input/model
[ -f "$bootstrap_input" ] && [ ! -L "$bootstrap_input" ]
[ -f "$model_input" ] && [ ! -L "$model_input" ]
unexpected=$(find "$output" -mindepth 1 -maxdepth 1 ! -name bootstrap.json ! -name model -print -quit)
[ -z "$unexpected" ]
rm -f "$output/bootstrap.json" "$output/model"
umask 077
cat "$bootstrap_input" > "$output/bootstrap.json"
cat "$model_input" > "$output/model"
chmod 0400 "$output/bootstrap.json" "$output/model"
[ "$(stat -c '%u:%g:%a:%F' "$output/bootstrap.json")" = "0:0:400:regular file" ]
[ "$(stat -c '%u:%g:%a:%F' "$output/model")" = "0:0:400:regular file" ]`

func (p *DockerProvisioner) ensureSecretVolume(
	ctx context.Context,
	identity RuntimeIdentity,
) (name string, owned bool, resultErr error) {
	name = secretVolumeName(identity)
	inspected, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err == nil {
		if err := verifySecretVolumeIdentity(name, inspected.Volume, identity); err != nil {
			return "", false, err
		}
		if err := p.removeOrphanSecretVolume(ctx, identity); err != nil {
			return "", false, err
		}
	}
	if err != nil && !cerrdefs.IsNotFound(err) {
		return "", false, dockerOperationError("inspect runtime secret volume", err)
	}
	created, err := p.engine.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   name,
		Driver: "local",
		Labels: resourceLabels(identity, ManagedRuntimeSecretVolume),
	})
	if err != nil {
		if cerrdefs.IsConflict(err) {
			racing, inspectErr := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
			if inspectErr != nil {
				return "", false, dockerOperationError("inspect conflicting runtime secret volume", inspectErr)
			}
			if err := verifySecretVolumeIdentity(name, racing.Volume, identity); err != nil {
				return "", false, err
			}
			return "", false, fmt.Errorf(
				"%w: runtime secret volume has an active concurrent owner",
				ErrRuntimeIdentityConflict,
			)
		}
		return "", false, dockerOperationError("create runtime secret volume", err)
	}
	if err := verifySecretVolumeIdentity(name, created.Volume, identity); err != nil {
		return "", false, err
	}
	return name, true, nil
}

func (p *DockerProvisioner) removeOrphanSecretVolume(ctx context.Context, identity RuntimeIdentity) error {
	name := secretVolumeName(identity)
	inspected, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("reinspect orphan runtime secret volume", err)
	}
	if err := verifySecretVolumeIdentity(name, inspected.Volume, identity); err != nil {
		return err
	}
	if _, err := p.engine.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		return dockerOperationError("remove orphan runtime secret volume", err)
	}
	residue, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("verify orphan runtime secret volume removal", err)
	}
	if verifySecretVolumeIdentity(name, residue.Volume, identity) == nil {
		return fmt.Errorf("%w: orphan runtime secret volume remains after removal", ErrRuntimeIdentityConflict)
	}
	return fmt.Errorf("%w: conflicting runtime secret volume replaced orphan during removal", ErrRuntimeIdentityConflict)
}

func verifySecretVolumeIdentity(name string, inspected volume.Volume, identity RuntimeIdentity) error {
	if inspected.Name != name || inspected.Driver != "local" ||
		len(inspected.Options) != 0 ||
		inspected.Scope != "local" ||
		inspected.ClusterVolume != nil ||
		!exactLabels(inspected.Labels, resourceLabels(identity, ManagedRuntimeSecretVolume)) {
		return fmt.Errorf("%w: runtime secret volume identity does not match", ErrRuntimeIdentityConflict)
	}
	return nil
}

func exactLabels(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func (p *DockerProvisioner) populateSecretVolume(
	ctx context.Context,
	request ProvisionRequest,
	volumeName string,
) (resultErr error) {
	create := p.secretPopulatorCreateOptions(request, volumeName)
	created, err := p.engine.ContainerCreate(ctx, create)
	if err != nil {
		if !cerrdefs.IsConflict(err) {
			return dockerOperationError("create runtime secret populator", err)
		}
		inspected, found, inspectErr := p.inspectExactSecretPopulator(ctx, request)
		if inspectErr != nil {
			return inspectErr
		}
		if !found {
			return fmt.Errorf("%w: conflicting runtime secret populator is missing", ErrRuntimeIdentityConflict)
		}
		if inspected.Container.State.Running {
			return fmt.Errorf("%w: runtime secret populator has an active concurrent owner", ErrRuntimeIdentityConflict)
		}
		if err := p.removeInspectedSecretPopulator(ctx, request, inspected); err != nil {
			return err
		}
		created, err = p.engine.ContainerCreate(ctx, create)
		if err != nil {
			if cerrdefs.IsConflict(err) {
				return fmt.Errorf("%w: runtime secret populator create conflict repeated", ErrRuntimeIdentityConflict)
			}
			return dockerOperationError("create runtime secret populator after stale cleanup", err)
		}
	}
	if created.ID == "" {
		return fmt.Errorf("create runtime secret populator returned an empty container id")
	}
	defer func() {
		resultErr = errors.Join(
			resultErr,
			p.removeOwnedSecretPopulator(ctx, request, created.ID),
		)
	}()
	if _, err := p.engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return dockerOperationError("start runtime secret populator", err)
	}
	wait := p.engine.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-wait.Error:
		if err != nil {
			return dockerOperationError("wait for runtime secret populator", err)
		}
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			return fmt.Errorf("runtime secret populator rejected secret staging")
		}
	}
	return nil
}

func (p *DockerProvisioner) secretPopulatorCreateOptions(
	request ProvisionRequest,
	volumeName string,
) client.ContainerCreateOptions {
	return client.ContainerCreateOptions{
		Name:  secretPopulatorName(request.Identity),
		Image: p.options.Image,
		Config: &container.Config{
			User:       "0:0",
			WorkingDir: "/",
			Env:        workerImageEnvironment(),
			Entrypoint: []string{"/bin/sh"},
			Cmd:        []string{"-eu", "-c", populateSecretVolumeScript},
			Labels:     resourceLabels(request.Identity, ManagedSecretPopulator),
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("none"),
			Privileged:     false,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			Mounts: []mount.Mount{
				bindMount(request.Bootstrap, secretInputBootstrapPath, true),
				bindMount(request.ModelSecret, secretInputModelPath, true),
				{Type: mount.TypeVolume, Source: volumeName, Target: secretOutputPath},
			},
		},
	}
}

func (p *DockerProvisioner) reconcileStaleSecretPopulator(
	ctx context.Context,
	request ProvisionRequest,
) error {
	inspected, found, err := p.inspectExactSecretPopulator(ctx, request)
	if err != nil || !found {
		return err
	}
	if inspected.Container.State.Running {
		waitCtx, cancelWait := context.WithTimeout(ctx, secretPopulatorReconcileTimeout)
		wait := p.engine.ContainerWait(waitCtx, inspected.Container.ID, client.ContainerWaitOptions{
			Condition: container.WaitConditionNotRunning,
		})
		select {
		case <-waitCtx.Done():
			cancelAndDrainContainerWait(cancelWait, wait)
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf(
				"%w: runtime secret populator has an active concurrent owner",
				ErrRuntimeIdentityConflict,
			)
		case err := <-wait.Error:
			cancelWait()
			if err != nil {
				return dockerOperationError("wait for stale runtime secret populator", err)
			}
		case <-wait.Result:
			cancelWait()
		}
	}
	return p.removeInspectedSecretPopulator(ctx, request, inspected)
}

func (p *DockerProvisioner) inspectExactSecretPopulator(
	ctx context.Context,
	request ProvisionRequest,
) (client.ContainerInspectResult, bool, error) {
	name := secretPopulatorName(request.Identity)
	inspected, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return client.ContainerInspectResult{}, false, nil
	}
	if err != nil {
		return client.ContainerInspectResult{}, false, dockerOperationError(
			"inspect runtime secret populator",
			err,
		)
	}
	if err := p.verifySecretPopulatorImmutableSpec(name, inspected, request); err != nil {
		return client.ContainerInspectResult{}, false, err
	}
	return inspected, true, nil
}

func (p *DockerProvisioner) verifySecretPopulatorImmutableSpec(
	name string,
	inspected client.ContainerInspectResult,
	request ProvisionRequest,
) error {
	config := inspected.Container.Config
	host := inspected.Container.HostConfig
	if inspected.Container.ID == "" || inspected.Container.State == nil || config == nil || host == nil {
		return fmt.Errorf("%w: runtime secret populator %q inspect is incomplete", ErrRuntimeIdentityConflict, name)
	}
	if !exactLabels(config.Labels, resourceLabels(request.Identity, ManagedSecretPopulator)) ||
		config.Image != p.options.Image ||
		config.User != "0:0" ||
		config.WorkingDir != "/" ||
		!exactEnvironment(config.Env, workerImageEnvironment()) ||
		!reflect.DeepEqual(config.Entrypoint, []string{"/bin/sh"}) ||
		!reflect.DeepEqual(config.Cmd, []string{"-eu", "-c", populateSecretVolumeScript}) ||
		len(config.ExposedPorts) != 0 {
		return fmt.Errorf("%w: runtime secret populator %q portable config does not match", ErrRuntimeIdentityConflict, name)
	}
	if host.NetworkMode != container.NetworkMode("none") ||
		host.Privileged ||
		!host.ReadonlyRootfs ||
		host.PublishAllPorts ||
		len(host.PortBindings) != 0 ||
		len(host.Binds) != 0 ||
		!exactCapabilities(host.CapDrop, []string{"ALL"}) ||
		len(host.CapAdd) != 0 ||
		!exactStringSet(host.SecurityOpt, []string{"no-new-privileges"}) ||
		!exactUnconstrainedResources(host.Resources) {
		return fmt.Errorf("%w: runtime secret populator %q host config does not match", ErrRuntimeIdentityConflict, name)
	}
	if !exactSecretPopulatorMounts(inspected.Container.Mounts, request) {
		return fmt.Errorf("%w: runtime secret populator %q mounts do not match", ErrRuntimeIdentityConflict, name)
	}
	return nil
}

func exactSecretPopulatorMounts(got []container.MountPoint, request ProvisionRequest) bool {
	if len(got) != 3 {
		return false
	}
	want := map[string]container.MountPoint{
		secretInputBootstrapPath: {
			Type:        mount.TypeBind,
			Source:      request.Bootstrap.HostPath,
			Destination: secretInputBootstrapPath,
			RW:          false,
		},
		secretInputModelPath: {
			Type:        mount.TypeBind,
			Source:      request.ModelSecret.HostPath,
			Destination: secretInputModelPath,
			RW:          false,
		},
		secretOutputPath: {
			Type:        mount.TypeVolume,
			Name:        secretVolumeName(request.Identity),
			Destination: secretOutputPath,
			RW:          true,
		},
	}
	for _, mounted := range got {
		expected, ok := want[mounted.Destination]
		if !ok || mounted.Type != expected.Type || mounted.RW != expected.RW {
			return false
		}
		switch mounted.Type {
		case mount.TypeBind:
			if mounted.Source != expected.Source || mounted.Name != "" {
				return false
			}
		case mount.TypeVolume:
			if mounted.Name != expected.Name {
				return false
			}
		default:
			return false
		}
		delete(want, mounted.Destination)
	}
	return len(want) == 0
}

func (p *DockerProvisioner) removeOwnedSecretPopulator(
	ctx context.Context,
	request ProvisionRequest,
	containerID string,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerHostPathCleanupTimeout)
	defer cancel()
	_, err := p.engine.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return dockerOperationError("remove runtime secret populator", err)
	}
	return p.verifySecretPopulatorAbsent(cleanupCtx, request)
}

func (p *DockerProvisioner) removeInspectedSecretPopulator(
	ctx context.Context,
	request ProvisionRequest,
	inspected client.ContainerInspectResult,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerHostPathCleanupTimeout)
	defer cancel()
	_, err := p.engine.ContainerRemove(
		cleanupCtx,
		inspected.Container.ID,
		client.ContainerRemoveOptions{Force: true},
	)
	if err != nil && !cerrdefs.IsNotFound(err) {
		return dockerOperationError("remove runtime secret populator", err)
	}
	return p.verifySecretPopulatorAbsent(cleanupCtx, request)
}

func (p *DockerProvisioner) verifySecretPopulatorAbsent(
	ctx context.Context,
	request ProvisionRequest,
) error {
	name := secretPopulatorName(request.Identity)
	residue, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("verify runtime secret populator removal", err)
	}
	if verifyErr := p.verifySecretPopulatorImmutableSpec(name, residue, request); verifyErr != nil {
		return fmt.Errorf("%w: conflicting runtime secret populator remains after removal", ErrRuntimeIdentityConflict)
	}
	return fmt.Errorf("%w: runtime secret populator remains after removal", ErrRuntimeIdentityConflict)
}

func removeTransientSecretSources(request ProvisionRequest) error {
	var result error
	for _, source := range []struct {
		operation string
		path      string
	}{
		{operation: "remove transient runtime bootstrap", path: request.Bootstrap.ContainerPath},
		{operation: "remove transient runtime model secret", path: request.ModelSecret.ContainerPath},
	} {
		if err := os.Remove(source.path); err != nil {
			result = errors.Join(result, dockerOperationError(source.operation, err))
		}
	}
	return result
}

func (p *DockerProvisioner) removeContainerForRollback(ctx context.Context, containerID string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerHostPathCleanupTimeout)
	defer cancel()
	_, err := p.engine.ContainerRemove(cleanupCtx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return dockerOperationError("remove runtime staging container", err)
	}
	return nil
}

func (p *DockerProvisioner) removeSecretVolumeForRollback(ctx context.Context, identity RuntimeIdentity) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerHostPathCleanupTimeout)
	defer cancel()
	for _, resource := range []struct {
		operation string
		name      string
	}{
		{operation: "runtime container", name: containerName(identity)},
		{operation: "runtime secret populator", name: secretPopulatorName(identity)},
	} {
		_, err := p.engine.ContainerInspect(cleanupCtx, resource.name, client.ContainerInspectOptions{})
		if err == nil {
			return fmt.Errorf(
				"%w: %s exists before runtime secret volume rollback",
				ErrRuntimeIdentityConflict,
				resource.operation,
			)
		}
		if !cerrdefs.IsNotFound(err) {
			return dockerOperationError(
				"inspect "+resource.operation+" before secret volume rollback",
				err,
			)
		}
	}
	name := secretVolumeName(identity)
	inspected, err := p.engine.VolumeInspect(cleanupCtx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("inspect runtime secret volume before rollback", err)
	}
	if err := verifySecretVolumeIdentity(name, inspected.Volume, identity); err != nil {
		return err
	}
	_, err = p.engine.VolumeRemove(cleanupCtx, name, client.VolumeRemoveOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("remove runtime secret volume", err)
	}
	residue, err := p.engine.VolumeInspect(cleanupCtx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("verify runtime secret volume rollback", err)
	}
	if verifySecretVolumeIdentity(name, residue.Volume, identity) == nil {
		return fmt.Errorf("%w: runtime secret volume remains after rollback", ErrRuntimeIdentityConflict)
	}
	return fmt.Errorf("%w: conflicting runtime secret volume remains after rollback", ErrRuntimeIdentityConflict)
}

func (p *DockerProvisioner) startExisting(
	ctx context.Context,
	name string,
	inspected client.ContainerInspectResult,
	request ProvisionRequest,
	hostPaths []HostPathRequirement,
) (Instance, error) {
	identity := request.Identity
	if err := p.verifyWorkerImmutableSpec(name, inspected, request); err != nil {
		return Instance{}, err
	}
	if err := p.inspectExactSecretVolume(ctx, identity); err != nil {
		return Instance{}, err
	}
	if err := p.reconcileStaleSecretPopulator(ctx, request); err != nil {
		return Instance{}, err
	}
	if inspected.Container.State.Running {
		if err := removeTransientSecretSources(request); err != nil {
			return Instance{}, err
		}
		return Instance{ContainerID: inspected.Container.ID, ContainerName: name}, nil
	}
	if err := removeTransientSecretSources(request); err != nil {
		return Instance{}, err
	}
	if err := p.verifyHostPaths(ctx, hostPaths[:3]); err != nil {
		return Instance{}, err
	}
	if _, err := p.engine.ContainerStart(ctx, inspected.Container.ID, client.ContainerStartOptions{}); err != nil {
		startErr := dockerOperationError("start existing runtime container", err)
		return Instance{}, errors.Join(startErr, p.cleanupAfterExistingStartFailure(ctx, identity))
	}
	return Instance{ContainerID: inspected.Container.ID, ContainerName: name}, nil
}

func (p *DockerProvisioner) cleanupAfterExistingStartFailure(
	ctx context.Context,
	identity RuntimeIdentity,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dockerHostPathCleanupTimeout)
	defer cancel()
	return p.cleanupLocked(cleanupCtx, identity)
}

func (p *DockerProvisioner) verifyWorkerImmutableSpec(
	name string,
	inspected client.ContainerInspectResult,
	request ProvisionRequest,
) error {
	if err := verifyContainerIdentity(name, inspected, request.Identity); err != nil {
		return err
	}
	config := inspected.Container.Config
	host := inspected.Container.HostConfig
	if host == nil {
		return fmt.Errorf("%w: container %q has no host config", ErrRuntimeIdentityConflict, name)
	}
	if !exactWorkerPortableConfig(inspected.Container.ID, config, request, p.options) {
		return fmt.Errorf("%w: container %q portable config does not match", ErrRuntimeIdentityConflict, name)
	}
	if !exactWorkerHostConfig(host, request, p.options) {
		return fmt.Errorf("%w: container %q host config does not match", ErrRuntimeIdentityConflict, name)
	}
	if !exactWorkerMounts(inspected.Container.Mounts, request) {
		return fmt.Errorf("%w: container %q mounts do not match", ErrRuntimeIdentityConflict, name)
	}
	if !exactWorkerNetworkSettings(inspected.Container.NetworkSettings, p.options.Network) {
		return fmt.Errorf("%w: container %q network settings do not match", ErrRuntimeIdentityConflict, name)
	}
	return nil
}

func exactWorkerNetworkSettings(settings *container.NetworkSettings, expectedNetwork string) bool {
	if settings == nil ||
		expectedNetwork == "" ||
		len(settings.Ports) != 0 ||
		len(settings.Networks) != 1 {
		return false
	}
	endpoint, ok := settings.Networks[expectedNetwork]
	return ok && exactWorkerEndpointSettings(endpoint)
}

func exactWorkerEndpointSettings(endpoint *dockernetwork.EndpointSettings) bool {
	return endpoint != nil &&
		endpoint.IPAMConfig == nil &&
		len(endpoint.Links) == 0 &&
		len(endpoint.Aliases) == 0 &&
		len(endpoint.DriverOpts) == 0 &&
		endpoint.GwPriority == 0
}

func exactWorkerPortableConfig(
	containerID string,
	config *container.Config,
	request ProvisionRequest,
	options DockerOptions,
) bool {
	if config == nil {
		return false
	}
	if config.Hostname != "" {
		if len(containerID) < 12 || config.Hostname != containerID[:12] {
			return false
		}
	}
	return config.Domainname == "" &&
		config.User == workerUser &&
		!config.AttachStdin &&
		!config.AttachStdout &&
		!config.AttachStderr &&
		!config.Tty &&
		!config.OpenStdin &&
		!config.StdinOnce &&
		len(config.ExposedPorts) == 0 &&
		exactEnvironment(config.Env, workerEnvironment(request, options)) &&
		reflect.DeepEqual(config.Cmd, []string{workerShimPath}) &&
		config.Healthcheck == nil &&
		!config.ArgsEscaped &&
		config.Image == options.Image &&
		len(config.Volumes) == 0 &&
		config.WorkingDir == WorkerWorkingCopyPath &&
		reflect.DeepEqual(config.Entrypoint, []string{workerTiniPath, "--"}) &&
		!config.NetworkDisabled &&
		len(config.OnBuild) == 0 &&
		config.StopSignal == "" &&
		config.StopTimeout == nil &&
		len(config.Shell) == 0
}

func exactWorkerHostConfig(host *container.HostConfig, request ProvisionRequest, options DockerOptions) bool {
	if host == nil {
		return false
	}
	return len(host.Binds) == 0 &&
		host.ContainerIDFile == "" &&
		host.LogConfig.Type == workerLogDriver &&
		len(host.LogConfig.Config) == 0 &&
		host.NetworkMode == container.NetworkMode(options.Network) &&
		len(host.PortBindings) == 0 &&
		host.RestartPolicy.Name == container.RestartPolicyDisabled &&
		host.RestartPolicy.MaximumRetryCount == 0 &&
		!host.AutoRemove &&
		host.VolumeDriver == "" &&
		len(host.VolumesFrom) == 0 &&
		host.ConsoleSize == [2]uint{} &&
		len(host.Annotations) == 0 &&
		exactCapabilities(host.CapDrop, []string{"ALL"}) &&
		exactCapabilities(host.CapAdd, []string{"KILL", "SETUID", "SETGID"}) &&
		host.CgroupnsMode == container.CgroupnsModePrivate &&
		len(host.DNS) == 0 &&
		len(host.DNSOptions) == 0 &&
		len(host.DNSSearch) == 0 &&
		len(host.ExtraHosts) == 0 &&
		len(host.GroupAdd) == 0 &&
		host.IpcMode == container.IPCModePrivate &&
		host.Cgroup == "" &&
		len(host.Links) == 0 &&
		host.OomScoreAdj == 0 &&
		host.PidMode.IsPrivate() &&
		!host.Privileged &&
		!host.PublishAllPorts &&
		!host.ReadonlyRootfs &&
		exactStringSet(host.SecurityOpt, []string{"no-new-privileges"}) &&
		len(host.StorageOpt) == 0 &&
		len(host.Tmpfs) == 0 &&
		host.UTSMode.IsPrivate() &&
		host.UsernsMode.IsPrivate() &&
		host.ShmSize == workerShmSize &&
		len(host.Sysctls) == 0 &&
		host.Runtime == workerRuntime &&
		host.Isolation.IsDefault() &&
		exactWorkerResources(host.Resources) &&
		exactWorkerMountSpecs(host.Mounts, request) &&
		exactStringSet(host.MaskedPaths, workerMaskedPaths()) &&
		exactStringSet(host.ReadonlyPaths, workerReadonlyPaths()) &&
		host.Init != nil &&
		!*host.Init
}

func exactWorkerMountSpecs(got []mount.Mount, request ProvisionRequest) bool {
	want := workerMounts(request, secretVolumeName(request.Identity))
	if len(got) != len(want) {
		return false
	}
	byTarget := make(map[string]mount.Mount, len(got))
	for _, mounted := range got {
		if mounted.Target == "" {
			return false
		}
		if _, duplicate := byTarget[mounted.Target]; duplicate {
			return false
		}
		byTarget[mounted.Target] = mounted
	}
	for _, expected := range want {
		actual, ok := byTarget[expected.Target]
		if !ok ||
			actual.Type != expected.Type ||
			actual.Source != expected.Source ||
			actual.ReadOnly != expected.ReadOnly ||
			actual.Target != expected.Target ||
			actual.Consistency != "" ||
			actual.BindOptions != nil ||
			actual.VolumeOptions != nil ||
			actual.ImageOptions != nil ||
			actual.TmpfsOptions != nil ||
			actual.ClusterOptions != nil {
			return false
		}
	}
	return true
}

func workerMaskedPaths() []string {
	return []string{
		"/proc/acpi",
		"/proc/asound",
		"/proc/interrupts",
		"/proc/kcore",
		"/proc/keys",
		"/proc/latency_stats",
		"/proc/sched_debug",
		"/proc/scsi",
		"/proc/timer_list",
		"/proc/timer_stats",
		"/sys/devices/virtual/powercap",
		"/sys/firmware",
	}
}

func workerReadonlyPaths() []string {
	return []string{
		"/proc/bus",
		"/proc/fs",
		"/proc/irq",
		"/proc/sys",
		"/proc/sysrq-trigger",
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func workerEnvironment(request ProvisionRequest, options DockerOptions) []string {
	environment := workerImageEnvironment()
	return append(environment,
		"SWITCHBACK_WORKER_TOKEN="+request.WorkerToken,
		"SWITCHBACK_WORKER_SESSION_ID="+request.Identity.WorkerSessionID,
		"SWITCHBACK_WORKER_RUN_ID="+request.Identity.WorkerRunID,
		"SWITCHBACK_RUNTIME_INSTANCE_ID="+request.Identity.RuntimeInstanceID,
		"SWITCHBACK_RUNTIME_GENERATION="+strconv.FormatUint(uint64(request.Identity.RuntimeGeneration), 10),
		"SWITCHBACK_BACKEND_GRPC_TARGET="+options.BackendGRPCTarget,
		"SWITCHBACK_BACKEND_HTTP_BASE_URL="+options.BackendHTTPBaseURL,
	)
}

func workerImageEnvironment() []string {
	return []string{
		"PATH=" + workerPath,
		"GPG_KEY=" + workerImageGPGKey,
		"PYTHON_VERSION=" + workerPythonVersion,
		"PYTHON_SHA256=" + workerPythonSHA256,
	}
}

func workerResources() container.Resources {
	pids := workerPIDs
	return container.Resources{
		NanoCPUs:  workerNanoCPUs,
		Memory:    workerMemoryBytes,
		PidsLimit: &pids,
	}
}

func exactWorkerResources(resources container.Resources) bool {
	return exactContainerResources(
		resources,
		workerMemoryBytes,
		workerMemoryBytes*2,
		workerNanoCPUs,
		workerPIDs,
	)
}

func exactUnconstrainedResources(resources container.Resources) bool {
	return exactContainerResources(resources, 0, 0, 0, 0)
}

func exactContainerResources(
	resources container.Resources,
	memory int64,
	memorySwap int64,
	nanoCPUs int64,
	pids int64,
) bool {
	if resources.Memory != memory ||
		resources.MemorySwap != memorySwap ||
		resources.NanoCPUs != nanoCPUs ||
		resources.CPUShares != 0 ||
		resources.CgroupParent != "" ||
		resources.BlkioWeight != 0 ||
		len(resources.BlkioWeightDevice) != 0 ||
		len(resources.BlkioDeviceReadBps) != 0 ||
		len(resources.BlkioDeviceWriteBps) != 0 ||
		len(resources.BlkioDeviceReadIOps) != 0 ||
		len(resources.BlkioDeviceWriteIOps) != 0 ||
		resources.CPUPeriod != 0 ||
		resources.CPUQuota != 0 ||
		resources.CPURealtimePeriod != 0 ||
		resources.CPURealtimeRuntime != 0 ||
		resources.CpusetCpus != "" ||
		resources.CpusetMems != "" ||
		len(resources.Devices) != 0 ||
		len(resources.DeviceCgroupRules) != 0 ||
		len(resources.DeviceRequests) != 0 ||
		resources.MemoryReservation != 0 ||
		resources.MemorySwappiness != nil ||
		(resources.OomKillDisable != nil && *resources.OomKillDisable) ||
		len(resources.Ulimits) != 0 ||
		resources.CPUCount != 0 ||
		resources.CPUPercent != 0 ||
		resources.IOMaximumIOps != 0 ||
		resources.IOMaximumBandwidth != 0 {
		return false
	}
	if pids == 0 {
		return resources.PidsLimit == nil || *resources.PidsLimit == 0
	}
	return resources.PidsLimit != nil && *resources.PidsLimit == pids
}

func workerMounts(request ProvisionRequest, volumeName string) []mount.Mount {
	return []mount.Mount{
		bindMount(request.WorkingCopy, WorkerWorkingCopyPath, false),
		bindMount(request.AgentState, WorkerAgentStatePath, false),
		bindMount(request.TraceSpool, WorkerTraceSpoolPath, false),
		{
			Type:     mount.TypeVolume,
			Source:   volumeName,
			Target:   WorkerSecretsPath,
			ReadOnly: true,
		},
	}
}

func exactWorkerMounts(got []container.MountPoint, request ProvisionRequest) bool {
	if len(got) != 4 {
		return false
	}
	want := map[string]container.MountPoint{
		WorkerWorkingCopyPath: {
			Type:        mount.TypeBind,
			Source:      request.WorkingCopy.HostPath,
			Destination: WorkerWorkingCopyPath,
			RW:          true,
		},
		WorkerAgentStatePath: {
			Type:        mount.TypeBind,
			Source:      request.AgentState.HostPath,
			Destination: WorkerAgentStatePath,
			RW:          true,
		},
		WorkerTraceSpoolPath: {
			Type:        mount.TypeBind,
			Source:      request.TraceSpool.HostPath,
			Destination: WorkerTraceSpoolPath,
			RW:          true,
		},
		WorkerSecretsPath: {
			Type:        mount.TypeVolume,
			Name:        secretVolumeName(request.Identity),
			Destination: WorkerSecretsPath,
			RW:          false,
		},
	}
	for _, mounted := range got {
		expected, ok := want[mounted.Destination]
		if !ok ||
			mounted.Type != expected.Type ||
			mounted.RW != expected.RW {
			return false
		}
		switch mounted.Type {
		case mount.TypeBind:
			if mounted.Source != expected.Source || mounted.Name != "" {
				return false
			}
		case mount.TypeVolume:
			if mounted.Name != expected.Name {
				return false
			}
		default:
			return false
		}
		delete(want, mounted.Destination)
	}
	return len(want) == 0
}

func exactEnvironment(got, want []string) bool {
	gotValues, ok := environmentValues(got)
	if !ok {
		return false
	}
	wantValues, ok := environmentValues(want)
	return ok && reflect.DeepEqual(gotValues, wantValues)
}

func environmentValues(values []string) (map[string]string, bool) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, _, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, false
		}
		if _, duplicate := result[key]; duplicate {
			return nil, false
		}
		result[key] = value
	}
	return result, true
}

func exactCapabilities(got, want []string) bool {
	normalize := func(value string) string {
		return strings.TrimPrefix(strings.ToUpper(value), "CAP_")
	}
	gotNormalized := make([]string, 0, len(got))
	for _, value := range got {
		gotNormalized = append(gotNormalized, normalize(value))
	}
	wantNormalized := make([]string, 0, len(want))
	for _, value := range want {
		wantNormalized = append(wantNormalized, normalize(value))
	}
	return exactStringSet(gotNormalized, wantNormalized)
}

func exactStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	values := make(map[string]struct{}, len(got))
	for _, value := range got {
		if _, duplicate := values[value]; duplicate {
			return false
		}
		values[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := values[value]; !ok {
			return false
		}
	}
	return true
}

func (p *DockerProvisioner) inspectExactSecretVolume(ctx context.Context, identity RuntimeIdentity) error {
	name := secretVolumeName(identity)
	inspected, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: runtime secret volume is missing", ErrRuntimeIdentityConflict)
	}
	if err != nil {
		return dockerOperationError("inspect runtime secret volume", err)
	}
	return verifySecretVolumeIdentity(name, inspected.Volume, identity)
}

// Cleanup removes the exact Worker before its exact runtime secret volume.
func (p *DockerProvisioner) Cleanup(ctx context.Context, identity RuntimeIdentity) error {
	if err := validateRuntimeIdentity(identity); err != nil {
		return err
	}
	unlock, err := p.operationLocks.lock(ctx, containerName(identity))
	if err != nil {
		return err
	}
	defer unlock()
	return p.cleanupLocked(ctx, identity)
}

func (p *DockerProvisioner) cleanupLocked(ctx context.Context, identity RuntimeIdentity) error {
	if err := p.cleanupWorker(ctx, identity); err != nil {
		return err
	}
	if err := p.cleanupSecretPopulator(ctx, identity); err != nil {
		return err
	}
	return p.cleanupSecretVolume(ctx, identity)
}

func (p *DockerProvisioner) cleanupSecretPopulator(
	ctx context.Context,
	identity RuntimeIdentity,
) error {
	request, err := p.secretPopulatorRequest(identity)
	if err != nil {
		return err
	}
	inspected, found, err := p.inspectExactSecretPopulator(ctx, request)
	if err != nil || !found {
		return err
	}
	return p.removeInspectedSecretPopulator(ctx, request, inspected)
}

func (p *DockerProvisioner) secretPopulatorRequest(identity RuntimeIdentity) (ProvisionRequest, error) {
	bootstrap, err := p.options.Mapper.SessionPath(identity.WorkerSessionID, "bootstrap.json")
	if err != nil {
		return ProvisionRequest{}, fmt.Errorf("derive runtime bootstrap path: %w", err)
	}
	modelSecret, err := p.options.Mapper.SessionPath(identity.WorkerSessionID, "model-secret")
	if err != nil {
		return ProvisionRequest{}, fmt.Errorf("derive runtime model secret path: %w", err)
	}
	return ProvisionRequest{
		Identity:    identity,
		Bootstrap:   bootstrap,
		ModelSecret: modelSecret,
	}, nil
}

func (p *DockerProvisioner) cleanupWorker(ctx context.Context, identity RuntimeIdentity) error {
	name := containerName(identity)
	inspected, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("inspect runtime container for cleanup", err)
	}
	if err := verifyContainerIdentity(name, inspected, identity); err != nil {
		return err
	}

	waitCtx, cancelWait := context.WithCancel(ctx)
	wait := p.engine.ContainerWait(waitCtx, inspected.Container.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionRemoved,
	})
	if _, err := p.engine.ContainerRemove(ctx, inspected.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		cancelAndDrainContainerWait(cancelWait, wait)
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return dockerOperationError("remove runtime container", err)
	}
	defer cancelWait()
	select {
	case <-ctx.Done():
		cancelAndDrainContainerWait(cancelWait, wait)
		return ctx.Err()
	case err := <-wait.Error:
		if err != nil {
			return dockerOperationError("wait for runtime container removal", err)
		}
		return nil
	case <-wait.Result:
		return nil
	}
}

func (p *DockerProvisioner) cleanupSecretVolume(ctx context.Context, identity RuntimeIdentity) error {
	name := secretVolumeName(identity)
	inspected, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("inspect runtime secret volume for cleanup", err)
	}
	if err := verifySecretVolumeIdentity(name, inspected.Volume, identity); err != nil {
		return err
	}
	if _, err := p.engine.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return dockerOperationError("remove runtime secret volume", err)
	}
	residue, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return dockerOperationError("verify runtime secret volume removal", err)
	}
	if verifySecretVolumeIdentity(name, residue.Volume, identity) == nil {
		return fmt.Errorf("%w: runtime secret volume remains after cleanup", ErrRuntimeIdentityConflict)
	}
	return fmt.Errorf("%w: conflicting runtime secret volume remains after cleanup", ErrRuntimeIdentityConflict)
}

func cancelAndDrainContainerWait(cancel context.CancelFunc, wait client.ContainerWaitResult) {
	cancel()
	select {
	case <-wait.Error:
	case <-wait.Result:
	}
}

func verifyContainerIdentity(
	name string,
	inspected client.ContainerInspectResult,
	identity RuntimeIdentity,
) error {
	if inspected.Container.ID == "" || inspected.Container.Config == nil {
		return fmt.Errorf("%w: container %q has incomplete inspect identity", ErrRuntimeIdentityConflict, name)
	}
	if !exactLabels(inspected.Container.Config.Labels, identityLabels(identity)) {
		return fmt.Errorf("%w: container %q labels do not match", ErrRuntimeIdentityConflict, name)
	}
	if inspected.Container.State == nil {
		return fmt.Errorf("%w: container %q has no state", ErrRuntimeIdentityConflict, name)
	}
	return nil
}

func validateDockerOptions(options DockerOptions) error {
	if err := validateDockerImage(options.Image); err != nil {
		return err
	}
	if err := validatePathSegment(options.Network); err != nil {
		return fmt.Errorf("docker network: %w", err)
	}
	mode := container.NetworkMode(options.Network)
	if mode.IsHost() || mode.IsContainer() {
		return fmt.Errorf("docker network must be an isolated named network")
	}
	if err := validateRoot("container data", options.Mapper.ContainerDataRoot); err != nil {
		return err
	}
	if err := validateRoot("host data", options.Mapper.HostDataRoot); err != nil {
		return err
	}
	if options.Mapper.ContainerDataRoot == string(filepath.Separator) || options.Mapper.HostDataRoot == string(filepath.Separator) {
		return fmt.Errorf("docker data roots must not be the filesystem root")
	}
	if options.FixturePath == "" || !filepath.IsAbs(options.FixturePath) || filepath.Clean(options.FixturePath) != options.FixturePath {
		return fmt.Errorf("fixture path must be a nonempty clean absolute path")
	}
	if err := validateDockerGRPCTarget(options.BackendGRPCTarget); err != nil {
		return fmt.Errorf("Backend gRPC target: %w", err)
	}
	if err := validateDockerHTTPBaseURL(options.BackendHTTPBaseURL); err != nil {
		return fmt.Errorf("Backend HTTP base URL: %w", err)
	}
	return nil
}

func validateDockerImage(value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t ") {
		return fmt.Errorf("docker image must be a nonempty immutable reference without whitespace")
	}
	if strings.HasPrefix(value, "sha256:") {
		if len(value) == 71 && allLowerHex(value[len("sha256:"):]) {
			return nil
		}
		return fmt.Errorf("docker image id must be a SHA-256 digest")
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return fmt.Errorf("docker image must be a valid Docker image reference")
	}
	if _, tagged := named.(reference.NamedTagged); tagged {
		return fmt.Errorf("docker image must not retain a floating tag")
	}
	canonicalRef, isCanonical := named.(reference.Canonical)
	if !isCanonical || canonicalRef.Digest().Algorithm() != "sha256" || !allLowerHex(canonicalRef.Digest().Encoded()) {
		return fmt.Errorf("docker image must be digest-pinned")
	}
	return nil
}

func allLowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validateDockerGRPCTarget(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, "://") {
		return fmt.Errorf("must be one host:port authority")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("must be one host:port authority")
	}
	return nil
}

func validateDockerHTTPBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("must be an HTTP(S) origin")
	}
	return nil
}

func validateHostPathRequirements(root string, requirements []HostPathRequirement) error {
	if err := validateRoot("host data", root); err != nil {
		return err
	}
	for _, requirement := range requirements {
		if requirement.Path == "" || !filepath.IsAbs(requirement.Path) || filepath.Clean(requirement.Path) != requirement.Path ||
			!isBelow(root, requirement.Path) || requirement.Path == root {
			return fmt.Errorf("host path must be a clean absolute child of configured host data root")
		}
		if requirement.Kind != HostPathDirectory && requirement.Kind != HostPathRegularFile {
			return fmt.Errorf("host path has unsupported expected type %q", requirement.Kind)
		}
	}
	return nil
}

func (p *DockerProvisioner) validateRequest(request ProvisionRequest) ([]HostPathRequirement, error) {
	identity := request.Identity
	if err := validateRuntimeIdentity(identity); err != nil {
		return nil, err
	}
	if request.WorkerToken == "" || strings.ContainsRune(request.WorkerToken, '\x00') {
		return nil, fmt.Errorf("worker token must be nonempty and contain no NUL")
	}
	hostPaths := make([]HostPathRequirement, 0, 5)
	for _, expected := range []struct {
		name      string
		leaf      string
		pair      PathPair
		directory bool
	}{
		{name: "working copy", leaf: "working-copy", pair: request.WorkingCopy, directory: true},
		{name: "agent state", leaf: "agent-state", pair: request.AgentState, directory: true},
		{name: "trace spool", leaf: "trace-spool", pair: request.TraceSpool, directory: true},
		{name: "bootstrap", leaf: "bootstrap.json", pair: request.Bootstrap},
		{name: "model secret", leaf: "model-secret", pair: request.ModelSecret},
	} {
		var want PathPair
		var err error
		want, err = p.options.Mapper.SessionPath(identity.WorkerSessionID, expected.leaf)
		if err != nil {
			return nil, fmt.Errorf("derive %s path: %w", expected.name, err)
		}
		if expected.pair != want {
			return nil, fmt.Errorf("%s path does not match its server-derived path", expected.name)
		}
		if err := validateMountSource(expected.name, expected.pair.ContainerPath, expected.directory); err != nil {
			return nil, err
		}
		if expected.pair.HostPath == "/" ||
			expected.pair.HostPath == "/var/run/docker.sock" ||
			expected.pair.HostPath == p.options.FixturePath {
			return nil, fmt.Errorf("%s uses a forbidden host mount source", expected.name)
		}
		kind := HostPathRegularFile
		if expected.directory {
			kind = HostPathDirectory
		}
		hostPaths = append(hostPaths, HostPathRequirement{
			Path: expected.pair.HostPath,
			Kind: kind,
		})
	}
	return hostPaths, nil
}

func (p *DockerProvisioner) verifyHostPaths(ctx context.Context, hostPaths []HostPathRequirement) error {
	if err := p.options.HostPathVerifier.VerifyHostPaths(
		ctx,
		p.options.Mapper.HostDataRoot,
		hostPaths,
	); err != nil {
		return dockerOperationError("verify Docker HostPaths", err)
	}
	return nil
}

func validateRuntimeIdentity(identity RuntimeIdentity) error {
	for name, value := range map[string]string{
		"worker session id":   identity.WorkerSessionID,
		"worker run id":       identity.WorkerRunID,
		"runtime instance id": identity.RuntimeInstanceID,
	} {
		if err := validatePathSegment(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if identity.RuntimeGeneration == 0 {
		return fmt.Errorf("runtime generation must be positive")
	}
	return nil
}

func validateMountSource(name, path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s source: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s source must not be a symlink", name)
	}
	if directory && !info.IsDir() {
		return fmt.Errorf("%s source must be a directory", name)
	}
	if !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("%s source must be a regular file", name)
	}
	return nil
}

func bindMount(pair PathPair, target string, readOnly bool) mount.Mount {
	return mount.Mount{
		Type:     mount.TypeBind,
		Source:   pair.HostPath,
		Target:   target,
		ReadOnly: readOnly,
	}
}

func identityLabels(identity RuntimeIdentity) map[string]string {
	return resourceLabels(identity, ManagedWorkerRuntime)
}

func resourceLabels(identity RuntimeIdentity, managed string) map[string]string {
	return map[string]string{
		LabelManaged:           managed,
		LabelWorkerSessionID:   identity.WorkerSessionID,
		LabelWorkerRunID:       identity.WorkerRunID,
		LabelRuntimeInstanceID: identity.RuntimeInstanceID,
		LabelRuntimeGeneration: strconv.FormatUint(uint64(identity.RuntimeGeneration), 10),
	}
}

func containerName(identity RuntimeIdentity) string {
	return "switchback-runtime-" + identity.RuntimeInstanceID
}

func secretVolumeName(identity RuntimeIdentity) string {
	return "switchback-runtime-secrets-" + identity.RuntimeInstanceID + "-g" +
		strconv.FormatUint(uint64(identity.RuntimeGeneration), 10)
}

func secretPopulatorName(identity RuntimeIdentity) string {
	return "switchback-runtime-secret-populator-" + identity.RuntimeInstanceID + "-g" +
		strconv.FormatUint(uint64(identity.RuntimeGeneration), 10)
}

var _ Provisioner = (*DockerProvisioner)(nil)
