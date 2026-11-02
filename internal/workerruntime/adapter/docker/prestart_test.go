package docker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestProvisionChecksActualCreatedContainerBeforeOnlyWorkerStart(t *testing.T) {
	e := &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound})}
	request := validRequest(t)
	got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), request, func(_ context.Context, id application.CreatedRuntimeIdentity) error {
		if id.ContainerID != "container-2" || id.RuntimeInstanceID != request.RuntimeInstanceID || id.WorkerSessionID != request.WorkerSessionID || id.Generation != request.Generation {
			t.Fatalf("proof identity: %+v", id)
		}
		e.events = append(e.events, "quota", "authority")
		return nil
	})
	want := []string{"create", "inspect:container-2", "quota", "authority", "inspect:container-2", "start:container-2"}
	if err != nil || got.StartedAt.IsZero() || !reflect.DeepEqual(e.events, want) {
		t.Fatalf("result=%+v err=%v order=%v want=%v", got, err, e.events, want)
	}
}

type prestartEngine struct {
	*infoRecordingEngine
	events       []string
	mutate       func(int, *container.InspectResponse)
	inspectCount int
	createErr    error
	emptyID      bool
	startErr     error
	popStartErr  error
	popRemoveErr error
}

func (e *prestartEngine) ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	result, err := e.recordingEngine.ContainerCreate(ctx, opts)
	if opts.Name == "switchback-runtime-runtime-1" {
		e.events = append(e.events, "create")
		if e.emptyID {
			result.ID = ""
		}
		if e.createErr != nil {
			return result, e.createErr
		}
	}
	return result, err
}
func (e *prestartEngine) ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if id == "container-2" {
		e.events = append(e.events, "inspect:"+id)
		e.inspectCount++
		result, err := e.recordingEngine.ContainerInspect(ctx, id, opts)
		if e.mutate != nil {
			e.mutate(e.inspectCount, &result.Container)
		}
		return result, err
	}
	return e.recordingEngine.ContainerInspect(ctx, id, opts)
}
func (e *prestartEngine) ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if id == "container-1" && e.popStartErr != nil {
		return client.ContainerStartResult{}, e.popStartErr
	}
	if id == "container-2" {
		e.events = append(e.events, "start:"+id)
		if e.startErr != nil {
			return client.ContainerStartResult{}, e.startErr
		}
	}
	return e.recordingEngine.ContainerStart(ctx, id, opts)
}

func TestProvisionPreStartDenialsNeverStartWorkerAndKeepIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(int, *container.InspectResponse)
		checkErr error
		cancel   bool
	}{
		{name: "CID-A inspect CID-B", mutate: func(_ int, c *container.InspectResponse) { c.ID = "container-B" }},
		{name: "resource mismatch", mutate: func(_ int, c *container.InspectResponse) { h := *c.HostConfig; h.Memory = 1; c.HostConfig = &h }},
		{name: "capability mismatch", mutate: func(_ int, c *container.InspectResponse) {
			h := *c.HostConfig
			h.CapAdd = []string{"SYS_ADMIN"}
			c.HostConfig = &h
		}},
		{name: "label mismatch", mutate: func(_ int, c *container.InspectResponse) {
			cfg := *c.Config
			cfg.Labels = map[string]string{}
			c.Config = &cfg
		}},
		{name: "already running", mutate: func(_ int, c *container.InspectResponse) {
			c.State = &container.State{Status: "running", Running: true}
		}},
		{name: "previously started", mutate: func(_ int, c *container.InspectResponse) {
			c.State = &container.State{Status: "created", StartedAt: "2026-10-14T00:00:00Z"}
		}},
		{name: "state changed after proof", mutate: func(n int, c *container.InspectResponse) {
			if n == 2 {
				c.State = &container.State{Status: "exited"}
			}
		}},
		{name: "CID changed after proof", mutate: func(n int, c *container.InspectResponse) {
			if n == 2 {
				c.ID = "container-B"
			}
		}},
		{name: "ENOENT quota is unknown", checkErr: errors.New("quota ENOENT")},
		{name: "stale proof", checkErr: errors.New("stale created proof")},
		{name: "proof CID-B", checkErr: errors.New("created proof identity mismatch")},
		{name: "quota mismatch", checkErr: errors.New("quota limit mismatch")},
		{name: "unavailable proof", checkErr: errors.New("physical proof unavailable")},
		{name: "authority revoked", checkErr: errors.New("consumed authority revoked")},
		{name: "canceled after check", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound}), mutate: tc.mutate}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(ctx, validRequest(t), func(context.Context, application.CreatedRuntimeIdentity) error {
				if tc.cancel {
					cancel()
				}
				return tc.checkErr
			})
			if err == nil || got.ContainerID != "container-2" || got.RuntimeInstanceID != "runtime-1" || got.Generation != 1 || !got.StartedAt.IsZero() {
				t.Fatalf("denial lost partial runtime: %+v err=%v", got, err)
			}
			if !reflect.DeepEqual(e.started, []string{"container-1"}) || !reflect.DeepEqual(e.removed, []string{"container-1"}) || len(e.volumeRemoved) != 0 {
				t.Fatalf("Worker started/eagerly cleaned: starts=%v removes=%v volumes=%v", e.started, e.removed, e.volumeRemoved)
			}
		})
	}
}

func TestProvisionUnknownPhysicalEffectsRetainPartialRuntime(t *testing.T) {
	failure := errors.New("Engine response lost")
	for _, tc := range []struct {
		name      string
		createErr error
		empty     bool
		startErr  error
		wantCID   string
	}{
		{name: "Create failed with CID", createErr: failure, wantCID: "container-2"},
		{name: "Create empty CID", empty: true},
		{name: "Create failed without CID", createErr: failure, empty: true},
		{name: "Start reply lost", startErr: failure, wantCID: "container-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound}), createErr: tc.createErr, emptyID: tc.empty, startErr: tc.startErr}
			got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
			if err == nil || !got.PhysicalEffectUnknown || got.ContainerID != tc.wantCID || got.RuntimeInstanceID != "runtime-1" || got.Generation != 1 || !got.StartedAt.IsZero() {
				t.Fatalf("unknown effect lost: %+v err=%v", got, err)
			}
			if !reflect.DeepEqual(e.removed, []string{"container-1"}) || len(e.volumeRemoved) != 0 {
				t.Fatalf("unknown effects eagerly cleaned: %v %v", e.removed, e.volumeRemoved)
			}
		})
	}
}

func (e *prestartEngine) ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if id == "container-1" && e.popRemoveErr != nil {
		return client.ContainerRemoveResult{}, e.popRemoveErr
	}
	return e.recordingEngine.ContainerRemove(ctx, id, opts)
}
func TestProvisionStagingUnknownCleanupKeepsOwnerBeforeWorkerCreate(t *testing.T) {
	failure := errors.New("secret populator Start reply lost")
	cleanup := errors.New("populator removal reply lost")
	e := &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound}), popStartErr: failure, popRemoveErr: cleanup}
	got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
	if !errors.Is(err, failure) || !errors.Is(err, cleanup) || !got.PhysicalEffectUnknown || got.ContainerID != "" || got.RuntimeInstanceID != "runtime-1" || !got.StartedAt.IsZero() || len(e.creates) != 1 {
		t.Fatalf("staging ambiguity lost: result=%+v err=%v creates=%d", got, err, len(e.creates))
	}
}

func TestProvisionStagingFailureRemovesExactValidatedVolume(t *testing.T) {
	for _, phase := range []string{"create", "empty ID", "start", "wait", "cancel", "nonzero exit", "remove", "transient input", "transient root"} {
		t.Run(phase, func(t *testing.T) {
			primary := errors.New("injected populator failure")
			popCleanup := errors.New("injected populator cleanup failure")
			volumeCleanup := errors.New("injected volume cleanup failure")
			request := validRequest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := &stagingFailureEngine{
				prestartEngine: &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound})},
				phase:          phase, primary: primary, volumeCleanup: volumeCleanup, cancel: cancel, request: request,
			}
			if phase == "start" || phase == "wait" || phase == "cancel" || phase == "nonzero exit" {
				e.popRemoveErr = popCleanup
			}
			got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(ctx, request, allowSyntheticPreStart)
			if !errors.Is(err, volumeCleanup) || !got.PhysicalEffectUnknown || got.ContainerID != "" || got.RuntimeInstanceID != request.RuntimeInstanceID || got.Generation != request.Generation || !got.StartedAt.IsZero() || len(e.creates) != 1 {
				t.Errorf("staging failure lost error/owner or created Worker: result=%+v err=%v creates=%d", got, err, len(e.creates))
			}
			if want := []string{"switchback-runtime-secrets-runtime-1-g1"}; !reflect.DeepEqual(e.volumeRemoved, want) {
				t.Errorf("VolumeRemove targets=%q, want exact validated volume %q", e.volumeRemoved, want)
			}
			if e.cleanupContextErr != nil || !e.cleanupHasDeadline || e.cleanupRemaining <= 0 || e.cleanupRemaining > 10*time.Second {
				t.Errorf("cleanup context must be independently live and bounded by 10 seconds: err=%v deadline=%v remaining=%v", e.cleanupContextErr, e.cleanupHasDeadline, e.cleanupRemaining)
			}
			if e.popRemoveErr != nil && !errors.Is(err, popCleanup) {
				t.Errorf("populator cleanup error lost: %v", err)
			}
			switch phase {
			case "create", "start", "wait", "remove":
				if !errors.Is(err, primary) {
					t.Errorf("original populator error lost: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Errorf("original cancellation lost: %v", err)
				}
			case "empty ID", "nonzero exit", "transient input", "transient root":
				want := map[string]string{"empty ID": "empty id", "nonzero exit": "rejected its inputs", "transient input": "remove transient Runtime secret input", "transient root": "remove transient Runtime secret input root"}[phase]
				if !strings.Contains(err.Error(), want) {
					t.Errorf("original staging diagnostic lost: %v, want %q", err, want)
				}
			}
			wantRemoves := []string{"container-1"}
			if phase == "create" || phase == "empty ID" {
				wantRemoves = nil
			}
			if !reflect.DeepEqual(e.removed, wantRemoves) {
				t.Errorf("populator removal targets=%q, want=%q", e.removed, wantRemoves)
			}
		})
	}
}

func TestProvisionStagingUnvalidatedVolumeNeverRemovesReplacement(t *testing.T) {
	for _, mismatch := range []string{"name", "labels"} {
		t.Run(mismatch, func(t *testing.T) {
			e := &stagingFailureEngine{
				prestartEngine: &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound})},
				volumeMismatch: mismatch,
			}
			got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
			if err == nil || !strings.Contains(err.Error(), "secret volume identity conflict") || !got.PhysicalEffectUnknown || len(e.volumeRemoved) != 0 || len(e.creates) != 0 {
				t.Fatalf("unvalidated volume cleaned or Worker created: result=%+v err=%v removes=%q creates=%d", got, err, e.volumeRemoved, len(e.creates))
			}
		})
	}
}

type stagingFailureEngine struct {
	*prestartEngine
	phase              string
	primary            error
	volumeCleanup      error
	volumeMismatch     string
	cancel             context.CancelFunc
	request            application.RuntimeProvisionRequest
	cleanupContextErr  error
	cleanupHasDeadline bool
	cleanupRemaining   time.Duration
}

func (e *stagingFailureEngine) ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	result, err := e.prestartEngine.ContainerCreate(ctx, opts)
	if len(e.creates) == 1 {
		if e.phase == "create" {
			return result, e.primary
		}
		if e.phase == "empty ID" {
			result.ID = ""
		}
	}
	return result, err
}

func (e *stagingFailureEngine) ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if e.phase == "start" {
		return client.ContainerStartResult{}, e.primary
	}
	return e.prestartEngine.ContainerStart(ctx, id, opts)
}

func (e *stagingFailureEngine) ContainerWait(ctx context.Context, id string, opts client.ContainerWaitOptions) client.ContainerWaitResult {
	result := make(chan container.WaitResponse, 1)
	waitErrors := make(chan error, 1)
	switch e.phase {
	case "wait":
		waitErrors <- e.primary
	case "cancel":
		e.cancel()
	case "nonzero exit":
		result <- container.WaitResponse{StatusCode: 1}
	default:
		return e.prestartEngine.ContainerWait(ctx, id, opts)
	}
	return client.ContainerWaitResult{Result: result, Error: waitErrors}
}

func (e *stagingFailureEngine) ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	result, err := e.recordingEngine.ContainerRemove(ctx, id, opts)
	if e.phase == "remove" {
		return result, e.primary
	}
	if e.phase == "transient input" {
		path := e.request.Secrets.Bootstrap.BackendPath
		if err := os.Remove(path); err != nil {
			return result, err
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return result, err
		}
		if err := os.WriteFile(filepath.Join(path, "still-present"), nil, 0o600); err != nil {
			return result, err
		}
	}
	if e.phase == "transient root" {
		if err := os.WriteFile(filepath.Join(e.request.Secrets.BackendRoot, "still-present"), nil, 0o600); err != nil {
			return result, err
		}
	}
	return result, errors.Join(err, e.popRemoveErr)
}

func (e *stagingFailureEngine) VolumeCreate(ctx context.Context, opts client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	result, err := e.recordingEngine.VolumeCreate(ctx, opts)
	switch e.volumeMismatch {
	case "name":
		result.Volume.Name = "replacement-volume"
	case "labels":
		result.Volume.Labels = map[string]string{}
	}
	return result, err
}

func (e *stagingFailureEngine) VolumeRemove(ctx context.Context, name string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	e.cleanupContextErr = ctx.Err()
	deadline, hasDeadline := ctx.Deadline()
	e.cleanupHasDeadline = hasDeadline
	e.cleanupRemaining = time.Until(deadline)
	result, err := e.recordingEngine.VolumeRemove(ctx, name, opts)
	return result, errors.Join(err, e.volumeCleanup)
}

type lostCleanupEngine struct {
	*cleanupEngine
	removalErr error
}

func (e *lostCleanupEngine) ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	result, _ := e.cleanupEngine.ContainerRemove(ctx, id, opts)
	return result, e.removalErr
}
func TestCleanupRetainsRemoveErrorEvenWhenReinspectionProvesAbsence(t *testing.T) {
	failure := errors.New("ContainerRemove reply lost")
	e := &lostCleanupEngine{cleanupEngine: &cleanupEngine{containerExists: true, volumeExists: true}, removalErr: failure}
	got, err := newProvisioner(t, e, &recordingVerifier{}).Cleanup(context.Background(), application.RuntimeCleanupRequest{RuntimeInstanceID: "runtime-1", Generation: 1, ExpectedContainerID: "container-runtime-1"})
	if !errors.Is(err, failure) || !got.Confirmed || got.ContainerExists || got.SecretVolumeExists {
		t.Fatalf("removal diagnostic/absence lost: %+v err=%v", got, err)
	}
}
