package localproduct

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAwaitStartupRuntimeRestoreCancelsOnDeadlineWorkerFailure(
	t *testing.T,
) {
	restoreStarted := make(chan struct{})
	restoreCancelled := make(chan struct{})
	workerExited := make(chan error, 1)
	workerErr := errors.New("deadline database unavailable")
	result := make(chan error, 1)

	go func() {
		_, err := awaitStartupRuntimeRestore(
			context.Background(),
			workerExited,
			func(ctx context.Context) (
				[]restoredStartupTurn,
				error,
			) {
				close(restoreStarted)
				<-ctx.Done()
				close(restoreCancelled)
				return nil, ctx.Err()
			},
		)
		result <- err
	}()
	select {
	case <-restoreStarted:
	case <-time.After(time.Second):
		t.Fatal("startup Runtime restore did not start")
	}
	workerExited <- workerErr
	select {
	case <-restoreCancelled:
	case <-time.After(time.Second):
		t.Fatal("deadline worker failure did not cancel Runtime restore")
	}
	select {
	case err := <-result:
		if !errors.Is(err, workerErr) ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("startup Runtime restore error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled startup Runtime restore did not converge")
	}
}

func TestAwaitStartupRuntimeRestoreRejectsUnexpectedDeadlineWorkerStop(
	t *testing.T,
) {
	workerExited := make(chan error, 1)
	restoreBlocked := make(chan struct{})
	workerExited <- nil

	_, err := awaitStartupRuntimeRestore(
		context.Background(),
		workerExited,
		func(ctx context.Context) ([]restoredStartupTurn, error) {
			close(restoreBlocked)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "stopped unexpectedly") ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected deadline worker stop error=%v", err)
	}
	select {
	case <-restoreBlocked:
	default:
		t.Fatal("startup Runtime restore was not joined after cancellation")
	}
}
