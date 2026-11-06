package localproduct

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRunOnceCapacityWaitStillReconcilesOldWork(t *testing.T) {
	runner := &runtimeRunner{}
	runner.startupReady.Store(true)
	steps := 0
	dependencies := runtimeRunOnceDependencies{
		reconcileSettlement: func(context.Context) (string, bool, error) {
			return "settlement-guarded", true, errRuntimeStartCapacityUnavailable
		},
		startAdoption:  func(context.Context) (bool, error) { steps++; return false, nil },
		detectFailures: func(context.Context) error { steps++; return nil },
		reconcileRecovery: func(context.Context) (workerapp.RuntimeRecoveryState, bool, error) {
			steps++
			return workerapp.RuntimeRecoveryState{}, false, nil
		},
		cleanupTerminal: func(context.Context) error { steps++; return nil },
		startQueuedTask: func(context.Context) error { t.Fatal("capacity wait started a queued Task"); return nil },
		executeTurn:     func(context.Context) error { t.Fatal("capacity wait dispatched a new prompt"); return nil },
	}
	err := runner.runOnceWith(context.Background(), dependencies)
	if !errors.Is(err, errRuntimeStartCapacityUnavailable) || steps != 4 {
		t.Fatalf("guarded settlement err=%v reconciliation steps=%d", err, steps)
	}
	ordinary := errors.New("ordinary settlement failure")
	dependencies.reconcileSettlement = func(context.Context) (string, bool, error) { return "", true, ordinary }
	steps = 0
	if err := runner.runOnceWith(context.Background(), dependencies); !errors.Is(err, ordinary) || steps != 0 {
		t.Fatalf("ordinary error reinterpreted: err=%v steps=%d", err, steps)
	}
}

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

func TestRunOnceTypedWaitDrainsOwnedWork(t *testing.T) {
	for _, stage := range []string{"settlement", "adoption", "failure", "recovery", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			runner := &runtimeRunner{}
			runner.startupReady.Store(true)
			cause := fmt.Errorf("pending initial owner: %w", workerapp.InitialRuntimeStartReplayPending)
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if stage == name {
					return cause
				}
				return nil
			}
			err := runner.runOnceWith(context.Background(), runtimeRunOnceDependencies{
				reconcileSettlement: func(context.Context) (string, bool, error) { return "", false, step("settlement") },
				startAdoption:       func(context.Context) (bool, error) { return false, step("adoption") },
				detectFailures:      func(context.Context) error { return step("failure") },
				reconcileRecovery: func(context.Context) (workerapp.RuntimeRecoveryState, bool, error) {
					return workerapp.RuntimeRecoveryState{}, false, step("recovery")
				},
				cleanupTerminal: func(context.Context) error { return step("cleanup") },
				startQueuedTask: func(context.Context) error { t.Fatal("pending owner dispatched Task"); return nil },
				executeTurn:     func(context.Context) error { t.Fatal("pending owner dispatched Prompt"); return nil },
			})
			if err != cause || !reflect.DeepEqual(calls, []string{"settlement", "adoption", "failure", "recovery", "cleanup"}) {
				t.Fatalf("cause identity=%t calls=%v error=%v", err == cause, calls, err)
			}
		})
	}
}

func TestRuntimeStagingWaitClassifierPreservesHardCauses(t *testing.T) {
	waits := []error{errRuntimeStartCapacityUnavailable, workerapp.CapacityUninitialized, workerapp.CapacityEvidenceInvalid, workerapp.CapacityEvidenceStale, workerapp.CapacityIdentityDrift, workerapp.CapacityInventoryUnknown, workerapp.CapacityExhausted, workerapp.CapacityPathNotIntegrated, workerapp.CapacityContainerProofPending, workerapp.InitialRuntimeStartReplayPending}
	hard := []error{context.Canceled, context.DeadlineExceeded, workerapp.CapacityBindingMismatch, workerapp.CapacityRequestExpired, workerapp.CapacitySecretFenceUnproven, workerapp.CapacityWaitReason("future_unknown"), errors.New("authorization/database/revision")}
	for _, wait := range waits {
		if !runtimeStagingWait(wait) || !runtimeStagingWait(fmt.Errorf("wrapped: %w", wait)) {
			t.Fatalf("known wait rejected: %v", wait)
		}
		for _, failure := range hard {
			if runtimeStagingWait(errors.Join(wait, failure)) || runtimeStagingWait(fmt.Errorf("wrapped join: %w", errors.Join(wait, failure))) {
				t.Fatalf("hard cause hidden: %v", failure)
			}
		}
	}
	for _, failure := range hard {
		if runtimeStagingWait(failure) || runtimeStagingWait(fmt.Errorf("wrapped: %w", failure)) {
			t.Fatalf("hard cause classified: %v", failure)
		}
	}
	if runtimeStagingWait(errors.Join(waits[0], waits[1])) {
		t.Fatal("ambiguous multi-cause wait accepted")
	}
}

func TestRunOnceTypedWaitDoesNotHideLaterHardFailure(t *testing.T) {
	for _, hard := range []error{context.Canceled, context.DeadlineExceeded, workerapp.CapacityBindingMismatch, workerapp.CapacityRequestExpired, workerapp.CapacitySecretFenceUnproven, errors.Join(workerapp.CapacityExhausted, errors.New("security"))} {
		runner := &runtimeRunner{}
		runner.startupReady.Store(true)
		called := false
		err := runner.runOnceWith(context.Background(), runtimeRunOnceDependencies{
			reconcileSettlement: func(context.Context) (string, bool, error) { return "", true, workerapp.CapacityEvidenceStale },
			startAdoption:       func(context.Context) (bool, error) { return false, nil },
			detectFailures:      func(context.Context) error { return hard },
			reconcileRecovery: func(context.Context) (workerapp.RuntimeRecoveryState, bool, error) {
				called = true
				return workerapp.RuntimeRecoveryState{}, false, nil
			},
			cleanupTerminal: func(context.Context) error { called = true; return nil },
			startQueuedTask: func(context.Context) error { called = true; return nil }, executeTurn: func(context.Context) error { called = true; return nil },
		})
		if err != hard || called {
			t.Fatalf("hard failure hidden: err=%v calls=%t", err, called)
		}
	}
}

func TestPendingInitialStartRefusalHasNoCleanupEffects(t *testing.T) {
	// Nil effect dependencies make accidental cleanup observable as a panic.
	runner := &runtimeRunner{}
	for _, cause := range []error{workerapp.InitialRuntimeStartReplayPending, fmt.Errorf("actual CID unavailable: %w", workerapp.CapacityContainerProofPending)} {
		if err := runner.reconcileFailedRuntimeStart(context.Background(), workerapp.StartedWorkerRun{RuntimeInstanceID: "original"}, cause); err != cause {
			t.Fatalf("pending cause replaced: %v", err)
		}
	}
}
