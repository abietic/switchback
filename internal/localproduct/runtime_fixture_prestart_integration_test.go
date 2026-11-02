//go:build integration

package localproduct

import (
	"context"
	"errors"
	"sync"
	"testing"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func fixtureCreatedRuntime(request workerapp.RuntimeProvisionRequest) workerapp.ProvisionedRuntime {
	return workerapp.ProvisionedRuntime{RuntimeInstanceID: request.RuntimeInstanceID, ContainerID: "fixture-" + request.RuntimeInstanceID, Generation: request.Generation}
}

func fixtureRuntimePreStart(ctx context.Context, request workerapp.RuntimeProvisionRequest, partial workerapp.ProvisionedRuntime, check workerapp.RuntimePreStartCheck) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check == nil {
		return errors.New("fixture runtime pre-start check is required")
	}
	if err := check(ctx, workerapp.CreatedRuntimeIdentity{ContainerID: partial.ContainerID, RuntimeInstanceID: request.RuntimeInstanceID, WorkerSessionID: request.WorkerSessionID, Generation: request.Generation}); err != nil {
		return err
	}
	return ctx.Err()
}

// Exercise the actual successful fixtures. The unprepared request is deliberate:
// a client incorrectly started before the check returns an owned failure that
// the regression joins, instead of leaving background work behind on RED.
func TestRuntimeFixturePreStartRejectsBeforeClientStartup(t *testing.T) {
	for _, name := range []string{"supplement", "observation"} {
		t.Run(name, func(t *testing.T) {
			f := newSupplementFixture(t, func(f *supplementFixture) { f.manualInitial = true })
			request := workerapp.RuntimeProvisionRequest{WorkerSessionID: "session-prestart", WorkerRunID: "run-prestart", RuntimeInstanceID: "runtime-prestart", Generation: 7}
			type contextKey struct{}
			ctx := context.WithValue(f.ctx, contextKey{}, "exact-call-context")
			denied := errors.New("controlled fixture pre-start rejection")
			calls := 0
			published := false
			f.provisioner.publishInitial = func(got workerapp.RuntimeProvisionRequest) {
				if got != request {
					t.Errorf("published request=%+v, want %+v", got, request)
				}
				published = true
			}
			check := func(gotCtx context.Context, identity workerapp.CreatedRuntimeIdentity) error {
				calls++
				if gotCtx != ctx || gotCtx.Value(contextKey{}) != "exact-call-context" {
					t.Error("pre-start context changed")
				}
				want := workerapp.CreatedRuntimeIdentity{ContainerID: "fixture-runtime-prestart", RuntimeInstanceID: "runtime-prestart", WorkerSessionID: "session-prestart", Generation: 7}
				if identity != want {
					t.Errorf("created identity=%+v, want %+v", identity, want)
				}
				f.runtime.inventory.mu.Lock()
				created, ok := f.runtime.inventory.requests[request.RuntimeInstanceID]
				f.runtime.inventory.mu.Unlock()
				if !ok || created != request {
					t.Error("check ran before created inventory was recorded")
				}
				if name == "supplement" {
					if !published {
						t.Error("check ran before initial observation publication")
					}
					if !f.provisioner.mu.TryLock() {
						t.Error("check ran under provisioner mutex")
					} else {
						f.provisioner.mu.Unlock()
					}
				}
				return denied
			}
			var provisioner workerapp.RuntimeProvisioner = f.runtime
			if name == "supplement" {
				provisioner = f.provisioner
			}
			partial, err := provisioner.Provision(ctx, request, check)
			if !partial.StartedAt.IsZero() {
				if failure := supplementReceive(t, f.ctx, f.runtime.failures); failure.Error() != "missing prepared runtime" {
					t.Errorf("unexpected incorrectly started client failure: %v", failure)
				}
			}
			f.stop()
			want := workerapp.ProvisionedRuntime{RuntimeInstanceID: "runtime-prestart", ContainerID: "fixture-runtime-prestart", Generation: 7}
			if !errors.Is(err, denied) || partial != want || calls != 1 {
				t.Fatalf("rejected fixture partial=%+v err=%v calls=%d; want exact unstarted partial, denial and one check", partial, err, calls)
			}
			select {
			case failure := <-f.runtime.failures:
				t.Fatalf("rejected fixture started client: %v", failure)
			default:
			}
			select {
			case start := <-f.runtime.starts:
				t.Fatalf("rejected fixture dispatched client: %+v", start)
			default:
			}
		})
	}
}

func TestRuntimeFixturePreStartCancellationAndMissingCheck(t *testing.T) {
	for _, name := range []string{"supplement", "observation"} {
		for _, mode := range []string{"nil-check", "pre-canceled", "callback-canceled", "lifetime-canceled"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				f := newSupplementFixture(t, func(f *supplementFixture) { f.manualInitial = true })
				request := workerapp.RuntimeProvisionRequest{WorkerSessionID: "session-canceled", RuntimeInstanceID: "runtime-canceled", Generation: 9}
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				calls := 0
				check := workerapp.RuntimePreStartCheck(func(context.Context, workerapp.CreatedRuntimeIdentity) error {
					calls++
					switch mode {
					case "callback-canceled":
						cancel()
					case "lifetime-canceled":
						f.provisioner.cancel()
					}
					return nil
				})
				wantCalls := 1
				switch mode {
				case "nil-check":
					check, wantCalls = nil, 0
				case "pre-canceled":
					cancel()
					wantCalls = 0
				}
				var provisioner workerapp.RuntimeProvisioner = f.runtime
				if name == "supplement" {
					provisioner = f.provisioner
				}
				partial, err := provisioner.Provision(ctx, request, check)
				if !partial.StartedAt.IsZero() {
					supplementReceive(t, f.ctx, f.runtime.failures)
				}
				f.stop()
				want := workerapp.ProvisionedRuntime{RuntimeInstanceID: "runtime-canceled", ContainerID: "fixture-runtime-canceled", Generation: 9}
				if partial != want || err == nil || calls != wantCalls {
					t.Fatalf("partial=%+v err=%v calls=%d; want unstarted identity, rejection and %d checks", partial, err, calls, wantCalls)
				}
				if mode != "nil-check" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
				assertFixtureClientNotStarted(t, f)
			})
		}
	}
}

func TestRuntimeFixturePreStartStopWhileCheckIsInFlight(t *testing.T) {
	f := newSupplementFixture(t, func(f *supplementFixture) { f.manualInitial = true })
	request := workerapp.RuntimeProvisionRequest{WorkerSessionID: "session-stop", RuntimeInstanceID: "runtime-stop", Generation: 11}
	entered, release := make(chan struct{}), make(chan struct{})
	type result struct {
		partial workerapp.ProvisionedRuntime
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		partial, err := f.provisioner.Provision(f.ctx, request, func(context.Context, workerapp.CreatedRuntimeIdentity) error {
			close(entered)
			<-release
			return nil
		})
		resultCh <- result{partial, err}
	}()
	supplementReceive(t, f.ctx, entered)
	stopped := f.provisioner.stop()
	select {
	case <-stopped:
		t.Error("stop did not retain ownership of in-flight check")
	default:
	}
	close(release)
	got := supplementReceive(t, f.ctx, resultCh)
	supplementReceive(t, f.ctx, stopped)
	want := workerapp.ProvisionedRuntime{RuntimeInstanceID: "runtime-stop", ContainerID: "fixture-runtime-stop", Generation: 11}
	if got.partial != want || !errors.Is(got.err, context.Canceled) {
		t.Fatalf("stopped in-flight check partial=%+v err=%v", got.partial, got.err)
	}
	calls := 0
	afterStop, err := f.provisioner.Provision(f.ctx, request, func(context.Context, workerapp.CreatedRuntimeIdentity) error { calls++; return nil })
	if afterStop != want || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("already stopped Provision partial=%+v err=%v calls=%d", afterStop, err, calls)
	}
	f.stop()
	assertFixtureClientNotStarted(t, f)
}

func TestRuntimeFixturePreStartSuccessStartsClientAfterCheck(t *testing.T) {
	for _, name := range []string{"supplement", "observation"} {
		t.Run(name, func(t *testing.T) {
			f := newSupplementFixture(t, func(f *supplementFixture) { f.manualInitial = true })
			request := workerapp.RuntimeProvisionRequest{WorkerSessionID: "session-allow", RuntimeInstanceID: "runtime-allow", Generation: 13}
			calls := 0
			var provisioner workerapp.RuntimeProvisioner = f.runtime
			if name == "supplement" {
				provisioner = f.provisioner
			}
			partial, err := provisioner.Provision(f.ctx, request, func(ctx context.Context, identity workerapp.CreatedRuntimeIdentity) error {
				calls++
				if ctx != f.ctx || identity != (workerapp.CreatedRuntimeIdentity{ContainerID: "fixture-runtime-allow", RuntimeInstanceID: "runtime-allow", WorkerSessionID: "session-allow", Generation: 13}) {
					t.Error("successful check lost exact context/identity")
				}
				select {
				case failure := <-f.runtime.failures:
					t.Errorf("client started before check: %v", failure)
				default:
				}
				return nil
			})
			// The owned client's deliberate missing-prepared failure proves the accepted
			// check actually releases startup. It is drained before teardown, with no sleep.
			failure := supplementReceive(t, f.ctx, f.runtime.failures)
			f.stop()
			if err != nil || partial.StartedAt.IsZero() || calls != 1 || failure.Error() != "missing prepared runtime" {
				t.Fatalf("accepted fixture partial=%+v err=%v checks=%d client=%v", partial, err, calls, failure)
			}
		})
	}
}

func assertFixtureClientNotStarted(t *testing.T, f *supplementFixture) {
	t.Helper()
	select {
	case failure := <-f.runtime.failures:
		t.Fatalf("rejected fixture started client: %v", failure)
	default:
	}
	select {
	case start := <-f.runtime.starts:
		t.Fatalf("rejected fixture dispatched client: %+v", start)
	default:
	}
}

type countedFixtureCreatedLayerCheck struct {
	delegate   workerapp.InitialCreatedLayerChecker
	mu         sync.Mutex
	requests   []workerapp.RuntimeProvisionRequest
	identities []workerapp.CreatedRuntimeIdentity
}

func (c *countedFixtureCreatedLayerCheck) CheckCreatedLayer(ctx context.Context, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) error {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.identities = append(c.identities, identity)
	c.mu.Unlock()
	return c.delegate.CheckCreatedLayer(ctx, request, identity)
}
