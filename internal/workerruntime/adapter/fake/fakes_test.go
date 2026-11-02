package fake_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestFakesImplementRuntimeApplicationPorts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	provisioner := &fake.RuntimeProvisioner{ProvisionResult: application.ProvisionedRuntime{RuntimeInstanceID: "runtime-1"}}
	if _, err := provisioner.Provision(ctx, application.RuntimeProvisionRequest{WorkerSessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	control := &fake.WorkerControl{}
	if err := control.Fence(ctx, "session-1", 1, "test"); err != nil {
		t.Fatal(err)
	}
	store := &fake.ObjectStore{}
	temporary, err := store.PutTemporary(ctx, application.ObjectIdentity{WorkerSessionID: "session-1", Kind: "trace"}, bytes.NewBufferString("trace"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Finalize(ctx, application.ObjectIdentity{}, temporary)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(ctx, stored.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if data, _ := io.ReadAll(reader); string(data) != "trace" {
		t.Fatalf("stored data=%q", data)
	}
	sink := &fake.ModelObservationSink{}
	if err := sink.Record(ctx, application.ModelObservation{ObservationID: "observation-1"}); err != nil {
		t.Fatal(err)
	}
	if got := (fake.Clock{Value: now}).Now(); !got.Equal(now) {
		t.Fatalf("clock=%s", got)
	}
	if len(provisioner.ProvisionRequests) != 1 || len(control.Fences) != 1 || len(sink.Observations) != 1 {
		t.Fatal("fake adapters did not record port calls")
	}
}

var (
	_ application.RuntimeProvisioner   = (*fake.RuntimeProvisioner)(nil)
	_ application.RuntimeCleaner       = (*fake.RuntimeProvisioner)(nil)
	_ application.WorkerControl        = (*fake.WorkerControl)(nil)
	_ application.AgentAdapter         = (*fake.AgentAdapter)(nil)
	_ application.ObjectStore          = (*fake.ObjectStore)(nil)
	_ application.ModelObservationSink = (*fake.ModelObservationSink)(nil)
	_ application.Clock                = fake.Clock{}
)
