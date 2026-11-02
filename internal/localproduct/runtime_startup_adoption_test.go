package localproduct

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRunOnceContinuesWhileUnownedRuntimeAdoptionBlocks(t *testing.T) {
	restoreStarted := make(chan struct{})
	releaseRestore := make(chan struct{})
	var continuationSteps atomic.Int32
	runner := &runtimeRunner{
		runtimes: make(map[string]*liveRuntime),
	}
	runner.startupReady.Store(true)
	adoptionDependencies := unownedRuntimeAdoptionDependencies{
		loadCandidates: func(context.Context) ([]startupRuntimeCandidate, error) {
			return []startupRuntimeCandidate{{
				Restore: liveRuntimeRestoreRequest{
					WorkerSessionID:   "session-1",
					RuntimeInstanceID: "runtime-1",
				},
			}}, nil
		},
		hasPendingSettlement: func(context.Context, string) (bool, error) {
			return false, nil
		},
		restore: func(
			context.Context,
			string,
		) (*restoredStartupTurn, error) {
			close(restoreStarted)
			<-releaseRestore
			return nil, errors.New("transient restore failure")
		},
		supervise: func(context.Context, restoredStartupTurn) {
			t.Error("failed adoption must not start Turn supervision")
		},
	}
	result := make(chan error, 1)
	go func() {
		result <- runner.runOnceWith(
			context.Background(),
			runtimeRunOnceDependencies{
				reconcileSettlement: func(
					context.Context,
				) (string, bool, error) {
					return "", false, nil
				},
				startAdoption: func(ctx context.Context) (bool, error) {
					return runner.restoreOneUnownedRuntimeWith(
						ctx,
						adoptionDependencies,
					)
				},
				detectFailures: func(context.Context) error {
					continuationSteps.Add(1)
					return nil
				},
				reconcileRecovery: func(
					context.Context,
				) (workerapp.RuntimeRecoveryState, bool, error) {
					continuationSteps.Add(1)
					return workerapp.RuntimeRecoveryState{}, false, nil
				},
				cleanupTerminal: func(context.Context) error {
					continuationSteps.Add(1)
					return nil
				},
				startQueuedTask: func(context.Context) error {
					continuationSteps.Add(1)
					return nil
				},
				executeTurn: func(context.Context) error {
					continuationSteps.Add(1)
					return nil
				},
			},
		)
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runOnce error=%v", err)
		}
	case <-time.After(time.Second):
		close(releaseRestore)
		t.Fatal("blocking adoption blocked runOnce")
	}
	select {
	case <-restoreStarted:
	case <-time.After(time.Second):
		close(releaseRestore)
		t.Fatal("runOnce did not start background adoption")
	}
	if steps := continuationSteps.Load(); steps != 5 {
		close(releaseRestore)
		t.Fatalf("runOnce continuation steps=%d, want 5", steps)
	}
	close(releaseRestore)
	expiry := time.Now().Add(time.Second)
	for runner.runtimeAdoptionInFlight("runtime-1") {
		if time.Now().After(expiry) {
			t.Fatal("background adoption did not clear its marker")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRestoreOneUnownedRuntimeStartsBlockingAdoptionOnce(t *testing.T) {
	restoreStarted := make(chan struct{})
	releaseRestore := make(chan struct{})
	restoreFinished := make(chan struct{})
	var restoreCalls atomic.Int32

	runner := &runtimeRunner{
		runtimes: make(map[string]*liveRuntime),
	}
	dependencies := unownedRuntimeAdoptionDependencies{
		loadCandidates: func(context.Context) ([]startupRuntimeCandidate, error) {
			return []startupRuntimeCandidate{{
				Restore: liveRuntimeRestoreRequest{
					WorkerSessionID:   "session-1",
					RuntimeInstanceID: "runtime-1",
				},
			}}, nil
		},
		hasPendingSettlement: func(context.Context, string) (bool, error) {
			return false, nil
		},
		restore: func(
			context.Context,
			string,
		) (*restoredStartupTurn, error) {
			if restoreCalls.Add(1) == 1 {
				close(restoreStarted)
			}
			<-releaseRestore
			close(restoreFinished)
			return nil, errors.New("transient restore failure")
		},
		supervise: func(context.Context, restoredStartupTurn) {
			t.Error("failed adoption must not start Turn supervision")
		},
	}

	firstResult := make(chan struct {
		started bool
		err     error
	}, 1)
	go func() {
		started, err := runner.restoreOneUnownedRuntimeWith(
			context.Background(),
			dependencies,
		)
		firstResult <- struct {
			started bool
			err     error
		}{started: started, err: err}
	}()

	select {
	case result := <-firstResult:
		if result.err != nil || !result.started {
			t.Fatalf(
				"first adoption started=%t error=%v",
				result.started,
				result.err,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("blocking restore blocked the runOnce adoption step")
	}
	select {
	case <-restoreStarted:
	case <-time.After(time.Second):
		t.Fatal("background adoption did not start")
	}

	started, err := runner.restoreOneUnownedRuntimeWith(
		context.Background(),
		dependencies,
	)
	if err != nil || started {
		t.Fatalf(
			"duplicate adoption started=%t error=%v",
			started,
			err,
		)
	}
	if calls := restoreCalls.Load(); calls != 1 {
		t.Fatalf("restore calls=%d, want 1", calls)
	}

	close(releaseRestore)
	select {
	case <-restoreFinished:
	case <-time.After(time.Second):
		t.Fatal("background adoption did not finish")
	}
	expiry := time.Now().Add(time.Second)
	for runner.runtimeAdoptionInFlight("runtime-1") {
		if time.Now().After(expiry) {
			t.Fatal("failed adoption did not clear its singleflight marker")
		}
		time.Sleep(time.Millisecond)
	}

	retryStarted := make(chan struct{})
	dependencies.restore = func(
		context.Context,
		string,
	) (*restoredStartupTurn, error) {
		restoreCalls.Add(1)
		close(retryStarted)
		return nil, errors.New("second transient restore failure")
	}
	started, err = runner.restoreOneUnownedRuntimeWith(
		context.Background(),
		dependencies,
	)
	if err != nil || !started {
		t.Fatalf(
			"retry adoption started=%t error=%v",
			started,
			err,
		)
	}
	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("cleared adoption marker was not retryable")
	}
	if calls := restoreCalls.Load(); calls != 2 {
		t.Fatalf("restore calls after retry=%d, want 2", calls)
	}
	expiry = time.Now().Add(time.Second)
	for runner.runtimeAdoptionInFlight("runtime-1") {
		if time.Now().After(expiry) {
			t.Fatal("retried adoption did not clear its singleflight marker")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSuccessfulUnownedRuntimeAdoptionStartsOneTurnSupervisor(t *testing.T) {
	restoreStarted := make(chan struct{})
	releaseRestore := make(chan struct{})
	supervisionStarted := make(chan struct{})
	releaseSupervision := make(chan struct{})
	var restoreCalls atomic.Int32
	var supervisionCalls atomic.Int32
	live := &liveRuntime{}
	restored := restoredStartupTurn{
		live: live,
		turn: pendingTurn{AgentTurnID: "turn-1"},
	}
	runner := &runtimeRunner{
		runtimes: make(map[string]*liveRuntime),
	}
	dependencies := unownedRuntimeAdoptionDependencies{
		loadCandidates: func(context.Context) ([]startupRuntimeCandidate, error) {
			return []startupRuntimeCandidate{{
				Restore: liveRuntimeRestoreRequest{
					WorkerSessionID:   "session-1",
					RuntimeInstanceID: "runtime-1",
				},
			}}, nil
		},
		hasPendingSettlement: func(context.Context, string) (bool, error) {
			return false, nil
		},
		restore: func(
			context.Context,
			string,
		) (*restoredStartupTurn, error) {
			restoreCalls.Add(1)
			close(restoreStarted)
			<-releaseRestore
			runner.mu.Lock()
			runner.runtimes["runtime-1"] = live
			runner.mu.Unlock()
			return &restored, nil
		},
		supervise: func(context.Context, restoredStartupTurn) {
			supervisionCalls.Add(1)
			close(supervisionStarted)
			<-releaseSupervision
		},
	}

	started, err := runner.restoreOneUnownedRuntimeWith(
		context.Background(),
		dependencies,
	)
	if err != nil || !started {
		t.Fatalf("adoption started=%t error=%v", started, err)
	}
	select {
	case <-restoreStarted:
	case <-time.After(time.Second):
		t.Fatal("background adoption did not start")
	}
	started, err = runner.restoreOneUnownedRuntimeWith(
		context.Background(),
		dependencies,
	)
	if err != nil || started {
		t.Fatalf(
			"duplicate adoption started=%t error=%v",
			started,
			err,
		)
	}
	close(releaseRestore)
	select {
	case <-supervisionStarted:
	case <-time.After(time.Second):
		t.Fatal("successful adoption did not start Turn supervision")
	}
	started, err = runner.restoreOneUnownedRuntimeWith(
		context.Background(),
		dependencies,
	)
	if err != nil || started {
		t.Fatalf(
			"owned Runtime restarted adoption=%t error=%v",
			started,
			err,
		)
	}
	if restoreCalls.Load() != 1 || supervisionCalls.Load() != 1 {
		t.Fatalf(
			"restore calls=%d supervision calls=%d, want 1/1",
			restoreCalls.Load(),
			supervisionCalls.Load(),
		)
	}
	close(releaseSupervision)
}

func TestUnownedRuntimeAdoptionContextIsCancelledWithRunner(t *testing.T) {
	restoreStarted := make(chan struct{})
	restoreCancelled := make(chan struct{})
	runner := &runtimeRunner{
		runtimes: make(map[string]*liveRuntime),
	}
	dependencies := unownedRuntimeAdoptionDependencies{
		loadCandidates: func(context.Context) ([]startupRuntimeCandidate, error) {
			return []startupRuntimeCandidate{{
				Restore: liveRuntimeRestoreRequest{
					WorkerSessionID:   "session-1",
					RuntimeInstanceID: "runtime-1",
				},
			}}, nil
		},
		hasPendingSettlement: func(context.Context, string) (bool, error) {
			return false, nil
		},
		restore: func(
			ctx context.Context,
			_ string,
		) (*restoredStartupTurn, error) {
			close(restoreStarted)
			<-ctx.Done()
			close(restoreCancelled)
			return nil, ctx.Err()
		},
		supervise: func(context.Context, restoredStartupTurn) {
			t.Error("cancelled adoption must not start Turn supervision")
		},
	}
	workerCtx, cancelWorkers := context.WithCancel(context.Background())

	started, err := runner.restoreOneUnownedRuntimeWith(
		workerCtx,
		dependencies,
	)
	if err != nil || !started {
		t.Fatalf("adoption started=%t error=%v", started, err)
	}
	select {
	case <-restoreStarted:
	case <-time.After(time.Second):
		t.Fatal("background adoption did not start")
	}
	cancelWorkers()
	select {
	case <-restoreCancelled:
	case <-time.After(time.Second):
		t.Fatal("Runner cancellation did not reach background adoption")
	}
	expiry := time.Now().Add(time.Second)
	for runner.runtimeAdoptionInFlight("runtime-1") {
		if time.Now().After(expiry) {
			t.Fatal("cancelled adoption did not clear its singleflight marker")
		}
		time.Sleep(time.Millisecond)
	}
}
