package docker_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestProvisionResourceBoundaryWorkerCreate(t *testing.T) {
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	result, err := newProvisioner(t, classicOverlay2Engine(engine), &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContainerID != "container-2" || len(engine.creates) != 2 {
		t.Fatalf("Worker creation: result=%+v creates=%d", result, len(engine.creates))
	}
	host := engine.creates[1].HostConfig
	if host == nil {
		t.Fatal("Worker HostConfig is missing")
	}
	if host.CgroupParent != "switchback-workers.slice" || host.NanoCPUs != 2_000_000_000 ||
		host.Memory != 4_294_967_296 || host.MemorySwap != 4_294_967_296 ||
		host.PidsLimit == nil || *host.PidsLimit != 512 ||
		!reflect.DeepEqual(host.StorageOpt, map[string]string{"size": "10737418240"}) {
		t.Fatalf("Worker resource boundary: %+v", host.Resources)
	}
}

func TestProvisionResourceBoundarySecretPopulatorCreate(t *testing.T) {
	engine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	if _, err := newProvisioner(t, classicOverlay2Engine(engine), &recordingVerifier{}).Provision(context.Background(), validRequest(t), allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	if len(engine.creates) != 2 {
		t.Fatalf("creates=%d, want secret populator and Worker", len(engine.creates))
	}
	populator := engine.creates[0]
	host := populator.HostConfig
	if host == nil || populator.Config == nil {
		t.Fatal("secret populator configuration is missing")
	}
	if host.CgroupParent != "switchback-core.slice" || host.NanoCPUs != 100_000_000 ||
		host.Memory != 67_108_864 || host.MemorySwap != 67_108_864 ||
		host.PidsLimit == nil || *host.PidsLimit != 32 {
		t.Errorf("secret populator resource boundary: %+v", host.Resources)
	}
	if !host.ReadonlyRootfs || host.NetworkMode != "none" || host.Privileged ||
		host.PublishAllPorts || len(host.PortBindings) != 0 || len(populator.Config.Env) != 0 {
		t.Errorf("secret populator isolation: %+v", host)
	}
	for _, mounted := range host.Mounts {
		if mounted.Source == "/var/run/docker.sock" || mounted.Target == "/var/run/docker.sock" {
			t.Error("secret populator has a Docker socket mount")
		}
	}
	for _, argument := range append(populator.Config.Entrypoint, populator.Config.Cmd...) {
		if strings.Contains(argument, "provider-key") {
			t.Error("secret populator argv contains the provider credential")
		}
	}
}

func TestProvisionResourceBoundaryExistingContainers(t *testing.T) {
	// Build the existing inspect fixture through the production creation path,
	// then set the independently specified immutable resource values.
	request := validRequest(t)
	createdEngine := &recordingEngine{inspectErr: cerrdefs.ErrNotFound}
	if _, err := newProvisioner(t, classicOverlay2Engine(createdEngine), &recordingVerifier{}).Provision(context.Background(), request, allowSyntheticPreStart); err != nil {
		t.Fatal(err)
	}
	created := createdEngine.creates[1]
	inspectedConfig := *created.Config
	inspectedConfig.Image = created.Image
	baseHost := *created.HostConfig
	baseHost.CgroupParent = "switchback-workers.slice"
	baseHost.MemorySwap = 4_294_967_296

	tests := []struct {
		name         string
		mutate       func(*container.HostConfig)
		wantConflict bool
	}{
		{name: "exact"},
		{name: "empty_parent", mutate: func(host *container.HostConfig) { host.CgroupParent = "" }, wantConflict: true},
		{name: "other_parent", mutate: func(host *container.HostConfig) { host.CgroupParent = "switchback-core.slice" }, wantConflict: true},
		{name: "default_swap", mutate: func(host *container.HostConfig) { host.MemorySwap = 0 }, wantConflict: true},
		{name: "unlimited_swap", mutate: func(host *container.HostConfig) { host.MemorySwap = -1 }, wantConflict: true},
		{name: "extra_swap", mutate: func(host *container.HostConfig) { host.MemorySwap = 8_589_934_592 }, wantConflict: true},
	}
	for _, state := range []struct {
		name    string
		running bool
	}{{"running", true}, {"stopped", false}} {
		t.Run(state.name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					host := baseHost
					if tt.mutate != nil {
						tt.mutate(&host)
					}
					engine := &recordingEngine{inspect: client.ContainerInspectResult{Container: container.InspectResponse{
						ID: "container-existing", Config: &inspectedConfig, HostConfig: &host,
						State: &container.State{Running: state.running},
					}}}
					result, err := newProvisioner(t, classicOverlay2Engine(engine), &recordingVerifier{}).Provision(context.Background(), request, allowSyntheticPreStart)
					if tt.wantConflict {
						if err == nil || err.Error() != "runtime container immutable configuration conflict" {
							t.Errorf("mismatched existing container: result=%+v err=%v", result, err)
						}
						if len(engine.started) != 0 {
							t.Errorf("mismatched existing container was started: %v", engine.started)
						}
					} else {
						if err == nil || result.ContainerID != "container-existing" || !result.StartedAt.IsZero() || len(engine.started) != 0 {
							t.Errorf("exact existing container must remain pending: result=%+v err=%v starts=%v", result, err, engine.started)
						}
					}
					if len(engine.creates) != 0 || len(engine.removed) != 0 || len(engine.volumeCreates) != 0 || len(engine.volumeRemoved) != 0 {
						t.Errorf("existing container was recreated or removed: creates=%d removes=%v volumeCreates=%d volumeRemoves=%v", len(engine.creates), engine.removed, len(engine.volumeCreates), engine.volumeRemoved)
					}
				})
			}
		})
	}
}
