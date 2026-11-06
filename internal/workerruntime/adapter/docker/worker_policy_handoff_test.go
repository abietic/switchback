package docker_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/abietic/switchback/internal/workerruntime/application"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type handoffEngine struct {
	*prestartEngine
	onStart       func(string)
	cleanupMode   bool
	workerRemoved bool
	removalFails  bool
}

func (e *handoffEngine) ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if id == "container-2" && e.onStart != nil {
		e.onStart(id)
	}
	return e.prestartEngine.ContainerStart(ctx, id, opts)
}
func (e *handoffEngine) ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if e.cleanupMode {
		if e.workerRemoved {
			return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
		}
		return e.recordingEngine.ContainerInspect(ctx, "container-2", opts)
	}
	return e.prestartEngine.ContainerInspect(ctx, id, opts)
}
func (e *handoffEngine) ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if id == "container-2" {
		if e.removalFails {
			return client.ContainerRemoveResult{}, errors.New("unconfirmed remove")
		}
		e.workerRemoved = true
	}
	return e.recordingEngine.ContainerRemove(ctx, id, opts)
}
func (e *handoffEngine) VolumeInspect(ctx context.Context, id string, opts client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	if e.cleanupMode {
		return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
	}
	return e.recordingEngine.VolumeInspect(ctx, id, opts)
}
func newHandoffEngine() *handoffEngine {
	return &handoffEngine{prestartEngine: &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound})}}
}
func handoffTarget(request application.RuntimeProvisionRequest) application.CreatedRuntimeIdentity {
	return application.CreatedRuntimeIdentity{WorkerSessionID: request.WorkerSessionID, RuntimeInstanceID: request.RuntimeInstanceID, Generation: request.Generation}
}
func requireHandoff(t *testing.T, p *dockeradapter.Provisioner, target application.CreatedRuntimeIdentity, want string) {
	t.Helper()
	got, found, err := p.LookupWorkerPolicyHandoff(target)
	if err != nil || found != (want != "") || got.ContainerID != want {
		t.Fatalf("handoff=%+v found=%v err=%v want=%q", got, found, err, want)
	}
	if found && (got.WorkerSessionID != target.WorkerSessionID || got.RuntimeInstanceID != target.RuntimeInstanceID || got.Generation != target.Generation) {
		t.Fatalf("handoff owner changed: %+v", got)
	}
}

func TestWorkerPolicyHandoffPublishedAfterFinalCheckBeforeSynchronousStart(t *testing.T) {
	e := newHandoffEngine()
	p := newProvisioner(t, e, &recordingVerifier{})
	request := validRequest(t)
	target := handoffTarget(request)
	starts := 0
	e.onStart = func(cid string) {
		starts++
		if e.inspectCount != 2 {
			t.Errorf("published before final inspection: %d", e.inspectCount)
		}
		requireHandoff(t, p, target, cid)
		// Caller can modify its copy, never the retained identity.
		got, _, _ := p.LookupWorkerPolicyHandoff(target)
		got.ContainerID = "caller-change"
		requireHandoff(t, p, target, cid)
	}
	result := make(chan error, 1)
	go func() {
		_, err := p.Provision(context.Background(), request, func(_ context.Context, id application.CreatedRuntimeIdentity) error {
			if e.inspectCount != 1 {
				t.Errorf("preStart inspection count=%d", e.inspectCount)
			}
			requireHandoff(t, p, target, "")
			return nil
		})
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous Start handoff lookup deadlocked on Provisioner lock")
	}
	if starts != 1 {
		t.Fatalf("Worker starts=%d", starts)
	}
	requireHandoff(t, p, target, "container-2")
	// A fresh Backend/Provisioner cannot recreate the handoff from Engine labels.
	fresh := newProvisioner(t, e, &recordingVerifier{})
	requireHandoff(t, fresh, target, "")
	for _, change := range []func(*application.CreatedRuntimeIdentity){
		func(id *application.CreatedRuntimeIdentity) { id.WorkerSessionID = "wrong" },
		func(id *application.CreatedRuntimeIdentity) { id.Generation++ },
		func(id *application.CreatedRuntimeIdentity) { id.ContainerID = "wrong" },
	} {
		bad := target
		change(&bad)
		if _, _, err := p.LookupWorkerPolicyHandoff(bad); err == nil {
			t.Fatal("conflicting handoff lookup accepted")
		}
	}
	wrongRuntime := target
	wrongRuntime.RuntimeInstanceID = "other-runtime"
	requireHandoff(t, p, wrongRuntime, "")
}

func TestWorkerPolicyHandoffAbsentOnPreStartFailures(t *testing.T) {
	for _, boundary := range []string{"create", "first inspect", "authority", "second inspect", "context"} {
		t.Run(boundary, func(t *testing.T) {
			e := newHandoffEngine()
			p := newProvisioner(t, e, &recordingVerifier{})
			request := validRequest(t)
			target := handoffTarget(request)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if boundary == "create" {
				e.createErr = errors.New("Create response unknown")
			}
			e.mutate = func(n int, c *container.InspectResponse) {
				if (boundary == "first inspect" && n == 1) || (boundary == "second inspect" && n == 2) {
					host := *c.HostConfig
					host.SecurityOpt = []string{"seccomp=unconfined"}
					c.HostConfig = &host
				}
				if boundary == "context" && n == 2 {
					cancel()
				}
			}
			got, err := p.Provision(ctx, request, func(context.Context, application.CreatedRuntimeIdentity) error {
				if boundary == "authority" {
					return errors.New("authority denied")
				}
				return nil
			})
			if err == nil || got.ContainerID != "container-2" || !got.PhysicalEffectUnknown {
				t.Fatalf("lost failed owner: %+v %v", got, err)
			}
			requireHandoff(t, p, target, "")
			if !reflect.DeepEqual(e.started, []string{"container-1"}) || !reflect.DeepEqual(e.removed, []string{"container-1"}) {
				t.Fatalf("failed preStart started/removed Worker: %v %v", e.started, e.removed)
			}
		})
	}
}

func TestWorkerPolicyHandoffRetainedOnUnknownStartAndUnconfirmedCleanup(t *testing.T) {
	e := newHandoffEngine()
	e.startErr = errors.New("Start response lost")
	p := newProvisioner(t, e, &recordingVerifier{})
	request := validRequest(t)
	target := handoffTarget(request)
	got, err := p.Provision(context.Background(), request, allowSyntheticPreStart)
	if err == nil || got.ContainerID != "container-2" || !got.PhysicalEffectUnknown {
		t.Fatalf("unknown Start lost owner: %+v %v", got, err)
	}
	for i := 0; i < 2; i++ {
		requireHandoff(t, p, target, "container-2")
	}
	if !reflect.DeepEqual(e.removed, []string{"container-1"}) {
		t.Fatal("unknown Start eagerly cleaned Worker")
	}
	e.cleanupMode = true
	e.removalFails = true
	cleanup := application.RuntimeCleanupRequest{RuntimeInstanceID: target.RuntimeInstanceID, Generation: target.Generation, ExpectedContainerID: "container-2"}
	observation, err := p.Cleanup(context.Background(), cleanup)
	if err == nil || observation.Confirmed {
		t.Fatalf("unconfirmed cleanup=%+v %v", observation, err)
	}
	requireHandoff(t, p, target, "container-2")
	e.removalFails = false
	observation, err = p.Cleanup(context.Background(), cleanup)
	if err != nil || !observation.Confirmed {
		t.Fatalf("confirmed cleanup=%+v %v", observation, err)
	}
	requireHandoff(t, p, target, "")
}

func TestWorkerPolicyHandoffConflictDoesNotOverwriteOrStart(t *testing.T) {
	e := newHandoffEngine()
	p := newProvisioner(t, e, &recordingVerifier{})
	request := validRequest(t)
	target := handoffTarget(request)
	if _, err := p.Provision(context.Background(), request, allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	// The fake name lookup remains NotFound. A second actual Create returns a
	// different CID, but cannot overwrite the first process-local owner.
	got, err := p.Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
	if err == nil || got.ContainerID != "container-4" || !got.PhysicalEffectUnknown {
		t.Fatalf("handoff conflict lost partial owner: %+v %v", got, err)
	}
	requireHandoff(t, p, target, "container-2")
	if !reflect.DeepEqual(e.started, []string{"container-1", "container-2", "container-3"}) || !reflect.DeepEqual(e.removed, []string{"container-1", "container-3"}) {
		t.Fatalf("conflict started/cleaned Worker: %v %v", e.started, e.removed)
	}
}

func TestWorkerPolicyHandoffOnlyMatchingConfirmedCleanupClears(t *testing.T) {
	e := newHandoffEngine()
	p := newProvisioner(t, e, &recordingVerifier{})
	request := validRequest(t)
	target := handoffTarget(request)
	if _, err := p.Provision(context.Background(), request, allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	e.cleanupMode = true
	e.workerRemoved = true
	for _, cleanup := range []application.RuntimeCleanupRequest{
		{RuntimeInstanceID: target.RuntimeInstanceID, Generation: target.Generation},
		{RuntimeInstanceID: target.RuntimeInstanceID, Generation: target.Generation, ExpectedContainerID: "other"},
		{RuntimeInstanceID: target.RuntimeInstanceID, Generation: target.Generation + 1, ExpectedContainerID: "container-2"},
	} {
		observation, err := p.Cleanup(context.Background(), cleanup)
		if err != nil || !observation.Confirmed {
			t.Fatalf("fake absence cleanup=%+v %v", observation, err)
		}
		requireHandoff(t, p, target, "container-2")
	}
	observation, err := p.Cleanup(context.Background(), application.RuntimeCleanupRequest{RuntimeInstanceID: target.RuntimeInstanceID, Generation: target.Generation, ExpectedContainerID: "container-2"})
	if err != nil || !observation.Confirmed {
		t.Fatalf("exact cleanup=%+v %v", observation, err)
	}
	requireHandoff(t, p, target, "")
}
