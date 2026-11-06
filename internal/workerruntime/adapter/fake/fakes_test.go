package fake_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/adapter/fake"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestFakesImplementRuntimeApplicationPorts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	provisioner := &fake.RuntimeProvisioner{ProvisionResult: application.ProvisionedRuntime{RuntimeInstanceID: "runtime-1"}}
	if _, err := provisioner.Provision(ctx, application.RuntimeProvisionRequest{WorkerSessionID: "session-1"}, func(context.Context, application.CreatedRuntimeIdentity) error { return nil }); err != nil {
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

func TestRuntimeProvisionerPreStartExactIdentityAndContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "original-context")
	request := application.RuntimeProvisionRequest{WorkerSessionID: "session-request", RuntimeInstanceID: "request-runtime", Generation: 99}
	for _, result := range []application.ProvisionedRuntime{
		{ContainerID: "created-cid", RuntimeInstanceID: "created-runtime", Generation: 7, StartedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)},
		{},
	} {
		provisioner := &fake.RuntimeProvisioner{ProvisionResult: result}
		calls := 0
		got, err := provisioner.Provision(ctx, request, func(checkCtx context.Context, identity application.CreatedRuntimeIdentity) error {
			calls++
			if checkCtx != ctx {
				t.Errorf("callback context differs from original context")
			}
			want := application.CreatedRuntimeIdentity{ContainerID: result.ContainerID, RuntimeInstanceID: result.RuntimeInstanceID, WorkerSessionID: "session-request", Generation: result.Generation}
			if identity != want {
				t.Errorf("identity=%+v, want %+v", identity, want)
			}
			if !reflect.DeepEqual(provisioner.ProvisionRequests, []application.RuntimeProvisionRequest{request}) {
				t.Errorf("request not recorded before callback: %+v", provisioner.ProvisionRequests)
			}
			return nil
		})
		if err != nil || got != result || calls != 1 {
			t.Fatalf("got=%+v err=%v callback calls=%d, want result=%+v and one callback", got, err, calls, result)
		}
	}
}

func TestRuntimeProvisionerPreStartRejectionPreservesPartialIdentity(t *testing.T) {
	rejected := errors.New("created layer rejected")
	result := application.ProvisionedRuntime{ContainerID: "created-cid", RuntimeInstanceID: "created-runtime", Generation: 7, StartedAt: time.Now(), PhysicalEffectUnknown: true}
	provisioner := &fake.RuntimeProvisioner{ProvisionResult: result}
	calls := 0
	got, err := provisioner.Provision(context.Background(), application.RuntimeProvisionRequest{}, func(context.Context, application.CreatedRuntimeIdentity) error { calls++; return rejected })
	want := result
	want.StartedAt = time.Time{}
	if !errors.Is(err, rejected) || got != want || calls != 1 {
		t.Fatalf("got=%+v err=%v calls=%d, want partial=%+v and rejection", got, err, calls, want)
	}
	if provisioner.ProvisionResult != result {
		t.Fatal("rejection mutated configured result")
	}
}

func TestRuntimeProvisionerPreStartNilCheckRejects(t *testing.T) {
	result := application.ProvisionedRuntime{ContainerID: "created-cid", RuntimeInstanceID: "created-runtime", Generation: 7, StartedAt: time.Now()}
	got, err := (&fake.RuntimeProvisioner{ProvisionResult: result}).Provision(context.Background(), application.RuntimeProvisionRequest{}, nil)
	want := result
	want.StartedAt = time.Time{}
	if err == nil || got != want {
		t.Fatalf("got=%+v err=%v, want partial=%+v and error", got, err, want)
	}
}

func TestRuntimeProvisionerPreStartCanceledContextPreventsCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := application.ProvisionedRuntime{ContainerID: "created-cid", RuntimeInstanceID: "created-runtime", Generation: 7, StartedAt: time.Now()}
	calls := 0
	provisioner := &fake.RuntimeProvisioner{ProvisionResult: result}
	got, err := provisioner.Provision(ctx, application.RuntimeProvisionRequest{WorkerSessionID: "session-canceled"}, func(context.Context, application.CreatedRuntimeIdentity) error { calls++; return nil })
	want := result
	want.StartedAt = time.Time{}
	if !errors.Is(err, context.Canceled) || got != want || calls != 0 {
		t.Fatalf("got=%+v err=%v calls=%d, want partial=%+v and canceled without callback", got, err, calls, want)
	}
	if len(provisioner.ProvisionRequests) != 1 {
		t.Fatal("canceled request not recorded")
	}
}

func TestRuntimeProvisionerPreStartCallbackCancellationPreventsStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := application.ProvisionedRuntime{ContainerID: "created-cid", RuntimeInstanceID: "created-runtime", Generation: 7, StartedAt: time.Now()}
	calls := 0
	got, err := (&fake.RuntimeProvisioner{ProvisionResult: result}).Provision(ctx, application.RuntimeProvisionRequest{}, func(context.Context, application.CreatedRuntimeIdentity) error { calls++; cancel(); return nil })
	want := result
	want.StartedAt = time.Time{}
	if !errors.Is(err, context.Canceled) || got != want || calls != 1 {
		t.Fatalf("got=%+v err=%v calls=%d, want partial=%+v and canceled after callback", got, err, calls, want)
	}
}

func TestRuntimeProvisionerConfiguredErrorPrecedesPreStartCheck(t *testing.T) {
	early := errors.New("create failed")
	rejected := errors.New("check rejected")
	result := application.ProvisionedRuntime{ContainerID: "configured-cid", RuntimeInstanceID: "configured-runtime", Generation: 7, StartedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"rejecting-check", "nil-check", "canceled-context"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			check := application.RuntimePreStartCheck(func(context.Context, application.CreatedRuntimeIdentity) error { calls++; return rejected })
			callCtx := context.Background()
			if name == "nil-check" {
				check = nil
			}
			if name == "canceled-context" {
				callCtx = ctx
			}
			provisioner := &fake.RuntimeProvisioner{ProvisionResult: result, Err: early}
			got, err := provisioner.Provision(callCtx, application.RuntimeProvisionRequest{}, check)
			if !errors.Is(err, early) || got != result || calls != 0 {
				t.Fatalf("got=%+v err=%v calls=%d, want original=%+v and early error", got, err, calls, result)
			}
			if len(provisioner.ProvisionRequests) != 1 {
				t.Fatal("early failure request not recorded")
			}
		})
	}
}

func TestRuntimeProvisionerPreStartCheckCanReenterInspect(t *testing.T) {
	inspection := application.RuntimeInspection{Exists: true, ContainerID: "created-cid", Generation: 7}
	result := application.ProvisionedRuntime{ContainerID: "created-cid", RuntimeInstanceID: "created-runtime", Generation: 7, StartedAt: time.Now()}
	provisioner := &fake.RuntimeProvisioner{ProvisionResult: result, Inspection: inspection}
	done := make(chan struct{})
	var got application.ProvisionedRuntime
	var err error
	calls := 0
	go func() {
		defer close(done)
		got, err = provisioner.Provision(context.Background(), application.RuntimeProvisionRequest{}, func(ctx context.Context, identity application.CreatedRuntimeIdentity) error {
			calls++
			inspected, inspectErr := provisioner.Inspect(ctx, identity.ContainerID)
			if inspectErr != nil {
				return inspectErr
			}
			if inspected != inspection {
				return errors.New("unexpected inspection")
			}
			return nil
		})
	}()
	select {
	case <-done:
		if err != nil || got != result || calls != 1 {
			t.Fatalf("got=%+v err=%v calls=%d, want original result and one reentrant check", got, err, calls)
		}
		if !reflect.DeepEqual(provisioner.InspectionIDs, []string{"created-cid"}) {
			t.Fatalf("inspection IDs=%v", provisioner.InspectionIDs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preStart check could not reenter Inspect; callback may hold fake mutex")
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
