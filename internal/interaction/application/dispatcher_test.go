package application

import (
	"context"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/interaction/domain"
)

type dispatcherRuns struct {
	claim     RunClaim
	claimErr  error
	recovered int
	claims    int
}

func (runs *dispatcherRuns) ClaimNextRun(context.Context, string, time.Duration, time.Time) (RunClaim, error) {
	runs.claims++
	return runs.claim, runs.claimErr
}
func (*dispatcherRuns) LoadRun(context.Context, string) (domain.AssistantRun, error) {
	return domain.AssistantRun{}, nil
}
func (*dispatcherRuns) MarkInvocationStarted(context.Context, string, uint64, uint64, time.Time) (uint64, error) {
	return 0, nil
}
func (*dispatcherRuns) CommitDecision(context.Context, RunDecisionCommit) error { return nil }
func (*dispatcherRuns) CommitFailure(context.Context, RunFailureCommit) error   { return nil }
func (*dispatcherRuns) RequeueExpired(context.Context, string, uint64, uint64, time.Time) error {
	return nil
}
func (runs *dispatcherRuns) RecoverExpiredClaims(context.Context, time.Time) (RunRecoveryResult, error) {
	return RunRecoveryResult{Count: runs.recovered}, nil
}

type dispatcherProcessor struct {
	claims []RunClaim
}

func (processor *dispatcherProcessor) ProcessAssistantRun(_ context.Context, claim RunClaim) error {
	processor.claims = append(processor.claims, claim)
	return nil
}

type fixedClock time.Time

func (clock fixedClock) Now() time.Time { return time.Time(clock) }

func TestAssistantRunDispatcherProcessesOneDurableClaim(t *testing.T) {
	runs := &dispatcherRuns{claim: RunClaim{Run: domain.AssistantRun{ID: "run-1"}}}
	processor := &dispatcherProcessor{}
	dispatcher := AssistantRunDispatcher{
		Runs: runs, Processor: processor,
		Clock:      fixedClock(time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)),
		LeaseOwner: "dispatcher-1", LeaseDuration: 30 * time.Second,
	}
	processed, err := dispatcher.RunOnce(context.Background())
	if err != nil || !processed || runs.claims != 1 || len(processor.claims) != 1 {
		t.Fatalf("dispatch: processed=%v claims=%d processor=%d error=%v",
			processed, runs.claims, len(processor.claims), err)
	}
}

func TestAssistantRunDispatcherTreatsEmptyQueueAsIdle(t *testing.T) {
	runs := &dispatcherRuns{claimErr: ErrNoQueuedRun}
	dispatcher := AssistantRunDispatcher{
		Runs: runs, Processor: &dispatcherProcessor{},
		Clock:      fixedClock(time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)),
		LeaseOwner: "dispatcher-1", LeaseDuration: 30 * time.Second,
	}
	processed, err := dispatcher.RunOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("idle dispatch: processed=%v error=%v", processed, err)
	}
}
