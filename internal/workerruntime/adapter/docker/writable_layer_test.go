package docker_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

func TestProvisionRejectsUnsupportedWritableLayerCompatibilityBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name string
		info system.Info
	}{
		{"containerd overlayfs", system.Info{OSType: "linux", Driver: "overlayfs", DriverStatus: [][2]string{{"driver-type", "io.containerd.snapshotter.v1"}}}},
		{"containerd claiming overlay2", system.Info{OSType: "linux", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}, {"driver-type", "io.containerd.snapshotter.v1"}}}},
		{"unknown driver type", system.Info{OSType: "linux", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}, {"driver-type", "unknown"}}}},
		{"unknown driver", system.Info{OSType: "linux", Driver: "unknown"}},
		{"unknown OS", system.Info{Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}}}},
		{"Windows", system.Info{OSType: "windows", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}}}},
		{"ext4", system.Info{OSType: "linux", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "extfs"}}}},
		{"unproven backing filesystem", system.Info{OSType: "linux", Driver: "overlay2"}},
		{"conflicting backing filesystem", system.Info{OSType: "linux", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}, {"Backing Filesystem", "extfs"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
			engine := &infoRecordingEngine{recordingEngine: recording, info: client.SystemInfoResult{Info: tt.info}}
			verifier := &recordingVerifier{}
			request := validRequest(t)
			if _, err := newProvisioner(t, engine, verifier).Provision(context.Background(), request, allowSyntheticPreStart); err == nil {
				t.Fatal("unsupported writable-layer compatibility accepted")
			}
			assertNoProvisionSideEffects(t, recording, verifier, request.Secrets.Bootstrap.BackendPath)
		})
	}
}

type infoRecordingEngine struct {
	*recordingEngine
	info      client.SystemInfoResult
	infoErr   error
	infoCalls int
}

func (e *infoRecordingEngine) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	e.infoCalls++
	return e.info, e.infoErr
}

func classicOverlay2Engine(recording *recordingEngine) *infoRecordingEngine {
	return &infoRecordingEngine{recordingEngine: recording, info: client.SystemInfoResult{Info: system.Info{
		OSType: "linux", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}},
		Architecture: "aarch64", SecurityOptions: []string{"name=seccomp,profile=builtin"},
	}}}
}

func TestProvisionRejectsCanceledOrFailedWritableLayerEvidenceBeforeSideEffects(t *testing.T) {
	failure := errors.New("Engine Info failed")
	for _, tt := range []struct {
		name    string
		cancel  bool
		infoErr error
		want    error
	}{
		{"canceled context", true, nil, context.Canceled},
		{"failed Info", false, failure, failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
			engine := classicOverlay2Engine(recording)
			engine.infoErr = tt.infoErr
			verifier := &recordingVerifier{}
			request := validRequest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			_, err := newProvisioner(t, engine, verifier).Provision(ctx, request, allowSyntheticPreStart)
			if !errors.Is(err, tt.want) {
				t.Fatalf("provision error = %v, want %v", err, tt.want)
			}
			assertNoProvisionSideEffects(t, recording, verifier, request.Secrets.Bootstrap.BackendPath)
		})
	}
}

func TestProvisionRejectsMissingWritableLayerEvidenceBeforeSideEffects(t *testing.T) {
	recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	// Expose only the existing Engine contract, as cleanup-only adapters do.
	engine := struct{ dockeradapter.Engine }{recording}
	verifier := &recordingVerifier{}
	request := validRequest(t)
	_, err := newProvisioner(t, engine, verifier).Provision(context.Background(), request, allowSyntheticPreStart)
	if err == nil {
		t.Fatal("provision accepted an Engine without writable-layer capability evidence")
	}
	assertNoProvisionSideEffects(t, recording, verifier, request.Secrets.Bootstrap.BackendPath)
}

func TestProvisionRejectsExistingWritableLayerWithoutAdoption(t *testing.T) {
	request := validRequest(t)
	createdEngine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	if _, err := newProvisioner(t, classicOverlay2Engine(createdEngine), &recordingVerifier{}).Provision(context.Background(), request, allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	created := createdEngine.creates[1]
	config := *created.Config
	config.Image = created.Image
	for _, tt := range []struct {
		name    string
		options map[string]string
		accept  bool
	}{
		{"matching cap", map[string]string{"size": "10737418240"}, true},
		{"missing cap", nil, false},
		{"smaller cap", map[string]string{"size": "5368709120"}, false},
		{"larger cap", map[string]string{"size": "21474836480"}, false},
		{"noncanonical cap", map[string]string{"size": "10G"}, false},
		{"extra storage option", map[string]string{"size": "10737418240", "caller": "override"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host := *created.HostConfig
			host.StorageOpt = tt.options
			engine := &recordingEngine{inspect: client.ContainerInspectResult{Container: container.InspectResponse{
				ID: "existing-runtime", Created: time.Now().UTC().Format(time.RFC3339Nano), Config: &config, HostConfig: &host,
				State: &container.State{Running: false},
			}}}
			result, err := newProvisioner(t, classicOverlay2Engine(engine), &recordingVerifier{}).Provision(context.Background(), request, allowSyntheticPreStart)
			if err == nil || len(engine.started) != 0 || result.ContainerID != "existing-runtime" || !result.StartedAt.IsZero() {
				t.Fatalf("existing Runtime adopted: result=%+v error=%v starts=%v", result, err, engine.started)
			}
			if !tt.accept && err.Error() != "runtime container immutable configuration conflict" {
				t.Fatalf("immutable drift was not validated: %v", err)
			}
			if len(engine.creates) != 0 || len(engine.volumeCreates) != 0 {
				t.Fatal("reuse unexpectedly created container or secrets")
			}
		})
	}
}

func TestProvisionPropagatesQuotaCreateRejectionWithoutUnboundedFallback(t *testing.T) {
	quotaFailure := errors.New("overlay2: size requires XFS project quota mounted with pquota")
	recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	engine := &quotaRejectingEngine{infoRecordingEngine: classicOverlay2Engine(recording), rejection: quotaFailure}
	_, err := newProvisioner(t, engine, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
	if !errors.Is(err, quotaFailure) {
		t.Fatalf("quota rejection = %v, want original Engine error", err)
	}
	if len(recording.creates) != 2 || !reflect.DeepEqual(recording.creates[1].HostConfig.StorageOpt, map[string]string{"size": "10737418240"}) || !reflect.DeepEqual(recording.started, []string{"container-1"}) {
		t.Fatalf("quota rejection retried or started Worker: creates=%d starts=%v", len(recording.creates), recording.started)
	}
}

type quotaRejectingEngine struct {
	*infoRecordingEngine
	rejection error
}

func (e *quotaRejectingEngine) ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if options.Name == "switchback-runtime-runtime-1" {
		e.creates = append(e.creates, options)
		return client.ContainerCreateResult{}, e.rejection
	}
	return e.recordingEngine.ContainerCreate(ctx, options)
}

func TestCheckWritableLayerReturnsFreshCompatibilityEvidenceWithoutProvisionOperations(t *testing.T) {
	recording := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	engine := classicOverlay2Engine(recording)
	verifier := &recordingVerifier{}
	provisioner := newProvisioner(t, engine, verifier)
	if err := provisioner.CheckWritableLayer(context.Background()); err != nil {
		t.Fatalf("classic Linux overlay2/XFS prerequisite: %v", err)
	}
	engine.info.Info.DriverStatus = append(engine.info.Info.DriverStatus, [2]string{"driver-type", "io.containerd.snapshotter.v1"})
	if err := provisioner.CheckWritableLayer(context.Background()); err == nil {
		t.Fatal("preflight reused stale compatible evidence")
	}
	if recording.inspections != 0 || len(recording.creates) != 0 || len(recording.started) != 0 || len(recording.volumeCreates) != 0 || recording.volumeInspections != 0 || len(recording.volumeRemoved) != 0 || len(verifier.paths) != 0 {
		t.Fatal("resource preflight performed provision operations")
	}
}

var _ dockeradapter.InfoEngine = (*infoRecordingEngine)(nil)

func assertNoProvisionSideEffects(t *testing.T, engine *recordingEngine, verifier *recordingVerifier, secretPath string) {
	t.Helper()
	if engine.inspections != 0 || len(engine.creates) != 0 || len(engine.started) != 0 || len(engine.volumeCreates) != 0 || engine.volumeInspections != 0 || len(engine.volumeRemoved) != 0 || len(verifier.paths) != 0 {
		t.Fatalf("unsupported Engine reached provision operations: inspect=%d create=%d start=%v volumes=%d paths=%v", engine.inspections, len(engine.creates), engine.started, len(engine.volumeCreates), verifier.paths)
	}
	if _, err := os.Lstat(secretPath); err != nil {
		t.Fatalf("preflight changed transient secret input: %v", err)
	}
}
