package docker_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func firstReportRequest(t *testing.T) application.RuntimeProvisionRequest {
	t.Helper()
	request := validRequest(t)
	profile, ok := config.ProductionProfileRegistry().Agent(config.DeepSeekFirstReportV1)
	if !ok {
		t.Fatal("first-report profile missing")
	}
	request.AgentProfile = application.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest}
	return request
}

func TestFirstReportRejectsIncompatibleEngineBeforeCreate(t *testing.T) {
	engine := &quotaEngine{info: system.Info{OSType: "linux", Driver: "overlayfs", DriverStatus: [][2]string{
		{"driver-type", "io.containerd.snapshotter.v1"},
	}}}
	_, err := newProvisioner(t, engine, &recordingVerifier{}).Provision(context.Background(), firstReportRequest(t))
	if err == nil || !strings.Contains(err.Error(), "classic Linux overlay2 on XFS") {
		t.Fatalf("expected storage compatibility rejection: %v", err)
	}
	if len(engine.creates) != 0 || len(engine.started) != 0 || len(engine.volumeCreates) != 0 {
		t.Fatal("Docker resources were changed before proving Engine compatibility")
	}
}

func TestFirstReportInspectsExactCreatedWorkerBeforeStart(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(*container.InspectResponse)
		start  bool
	}{
		{name: "quota reported", start: true},
		{name: "image labels inherited", start: true, mutate: func(got *container.InspectResponse) {
			got.Config.Labels["com.docker.compose.project"] = "switchback-m1"
		}},
		{name: "runtime identity differs", mutate: func(got *container.InspectResponse) {
			got.Config.Labels["com.switchback.runtime-instance-id"] = "different-runtime"
		}},
		{name: "unknown Switchback label", mutate: func(got *container.InspectResponse) {
			got.Config.Labels["com.switchback.unexpected"] = "value"
		}},
		{name: "quota missing", mutate: func(got *container.InspectResponse) { got.HostConfig.StorageOpt = nil }},
		{name: "different container", mutate: func(got *container.InspectResponse) { got.ID = "another-container" }},
		{name: "already running", mutate: func(got *container.InspectResponse) { got.State.Status = "running" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			engine := &quotaEngine{info: compatibleEngineInfo(), mutate: scenario.mutate}
			result, err := newProvisioner(t, engine, &recordingVerifier{}).Provision(context.Background(), firstReportRequest(t))
			if len(engine.creates) != 2 || engine.creates[1].HostConfig.StorageOpt["size"] != "10737418240" {
				t.Fatalf("Worker create did not request the profile's 10 GiB quota: %v", err)
			}
			if scenario.start {
				if err != nil || result.ContainerID != "container-2" || !reflect.DeepEqual(engine.workerCalls, []string{"inspect:container-2", "start:container-2"}) {
					t.Fatalf("expected exact inspect then Start: %v (%v)", err, engine.workerCalls)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(engine.workerCalls, []string{"inspect:container-2"}) || !slices.Contains(engine.removed, "container-2") {
				t.Fatalf("invalid evidence was not rejected and cleaned up: %v (calls=%v removed=%v)", err, engine.workerCalls, engine.removed)
			}
		})
	}
}

func TestFirstReportDoesNotStartExistingWorkerWithoutQuota(t *testing.T) {
	request := firstReportRequest(t)
	engine := &quotaEngine{info: compatibleEngineInfo()}
	if _, err := newProvisioner(t, engine, &recordingVerifier{}).Provision(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	options := engine.creates[1]
	configuration := *options.Config
	configuration.Image = options.Image
	configuration.Labels["com.docker.compose.project"] = "switchback-m1"
	options.HostConfig.StorageOpt = nil
	existing := &quotaEngine{info: compatibleEngineInfo(), existing: &container.InspectResponse{
		ID: "existing-worker", Config: &configuration, HostConfig: options.HostConfig, State: &container.State{Status: "created"},
	}}
	if _, err := newProvisioner(t, existing, &recordingVerifier{}).Provision(context.Background(), request); err == nil || !strings.Contains(err.Error(), "quota differs") {
		t.Fatalf("existing Worker without its quota was not rejected: %v", err)
	}
	if len(existing.started) != 0 || len(existing.creates) != 0 {
		t.Fatal("Docker mutated an existing Worker without the required quota")
	}
}

func compatibleEngineInfo() system.Info {
	return system.Info{OSType: "linux", Driver: "overlay2", DriverStatus: [][2]string{{"Backing Filesystem", "xfs"}}}
}

type quotaEngine struct {
	recordingEngine
	info        system.Info
	existing    *container.InspectResponse
	mutate      func(*container.InspectResponse)
	workerCalls []string
}

func (e *quotaEngine) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return client.SystemInfoResult{Info: e.info}, nil
}

func (e *quotaEngine) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if e.existing != nil {
		return client.ContainerInspectResult{Container: *e.existing}, nil
	}
	if id != "container-2" || len(e.creates) != 2 {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	e.workerCalls = append(e.workerCalls, "inspect:"+id)
	options := e.creates[1]
	host := *options.HostConfig
	result := container.InspectResponse{ID: id, Config: options.Config, HostConfig: &host, State: &container.State{Status: "created"}}
	if e.mutate != nil {
		e.mutate(&result)
	}
	return client.ContainerInspectResult{Container: result}, nil
}

func (e *quotaEngine) ContainerStart(ctx context.Context, id string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if id == "container-2" {
		e.workerCalls = append(e.workerCalls, "start:"+id)
	}
	return e.recordingEngine.ContainerStart(ctx, id, options)
}
