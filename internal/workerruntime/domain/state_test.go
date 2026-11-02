package domain_test

import (
	"errors"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestWorkerSessionTransitionsAreExhaustive(t *testing.T) {
	states := []domain.WorkerSessionStatus{
		domain.SessionAllocating, domain.SessionInteractive, domain.SessionRunning,
		domain.SessionRecovering, domain.SessionWaitingUser, domain.SessionIdle,
		domain.SessionLost, domain.SessionExpired, domain.SessionClosed,
	}
	legal := transitionSet(map[domain.WorkerSessionStatus][]domain.WorkerSessionStatus{
		domain.SessionAllocating:  {domain.SessionInteractive, domain.SessionLost, domain.SessionClosed},
		domain.SessionInteractive: {domain.SessionRunning, domain.SessionRecovering, domain.SessionIdle, domain.SessionLost, domain.SessionExpired, domain.SessionClosed},
		domain.SessionRunning:     {domain.SessionInteractive, domain.SessionRecovering, domain.SessionWaitingUser, domain.SessionIdle, domain.SessionLost, domain.SessionExpired, domain.SessionClosed},
		domain.SessionRecovering:  {domain.SessionInteractive, domain.SessionRunning, domain.SessionWaitingUser, domain.SessionIdle, domain.SessionLost, domain.SessionExpired, domain.SessionClosed},
		domain.SessionWaitingUser: {domain.SessionInteractive, domain.SessionRunning, domain.SessionRecovering, domain.SessionIdle, domain.SessionLost, domain.SessionExpired, domain.SessionClosed},
		domain.SessionIdle:        {domain.SessionInteractive, domain.SessionRunning, domain.SessionRecovering, domain.SessionLost, domain.SessionExpired, domain.SessionClosed},
		domain.SessionLost:        {domain.SessionExpired, domain.SessionClosed},
	})
	assertTransitions(t, states, legal, func(from, to domain.WorkerSessionStatus) error {
		_, err := from.Transition(to)
		return err
	}, domain.ErrIllegalWorkerSessionTransition)
}

func TestWorkerRunTransitionsAreExhaustive(t *testing.T) {
	states := []domain.WorkerRunStatus{
		domain.RunStarting, domain.RunRunning, domain.RunWaitingUser,
		domain.RunCompleted, domain.RunFailed, domain.RunCancelled, domain.RunTimedOut,
	}
	legal := transitionSet(map[domain.WorkerRunStatus][]domain.WorkerRunStatus{
		domain.RunStarting:    {domain.RunRunning, domain.RunFailed, domain.RunCancelled, domain.RunTimedOut},
		domain.RunRunning:     {domain.RunWaitingUser, domain.RunCompleted, domain.RunFailed, domain.RunCancelled, domain.RunTimedOut},
		domain.RunWaitingUser: {domain.RunRunning, domain.RunCompleted, domain.RunFailed, domain.RunCancelled, domain.RunTimedOut},
	})
	assertTransitions(t, states, legal, func(from, to domain.WorkerRunStatus) error {
		_, err := from.Transition(to)
		return err
	}, domain.ErrIllegalWorkerRunTransition)
}

func TestAgentTurnTransitionsAreExhaustive(t *testing.T) {
	states := []domain.AgentTurnStatus{
		domain.TurnAccepted, domain.TurnQueued, domain.TurnDelivered, domain.TurnConsumed,
		domain.TurnCompleted, domain.TurnCancelled, domain.TurnTimedOut, domain.TurnFailed,
	}
	legal := transitionSet(map[domain.AgentTurnStatus][]domain.AgentTurnStatus{
		domain.TurnAccepted:  {domain.TurnQueued, domain.TurnDelivered, domain.TurnCancelled, domain.TurnFailed},
		domain.TurnQueued:    {domain.TurnDelivered, domain.TurnCancelled, domain.TurnFailed},
		domain.TurnDelivered: {domain.TurnConsumed, domain.TurnCompleted, domain.TurnCancelled, domain.TurnFailed},
		domain.TurnConsumed:  {domain.TurnCompleted, domain.TurnCancelled, domain.TurnTimedOut, domain.TurnFailed},
	})
	assertTransitions(t, states, legal, func(from, to domain.AgentTurnStatus) error {
		_, err := from.Transition(to)
		return err
	}, domain.ErrIllegalAgentTurnTransition)
}

func assertTransitions[S ~string](
	t *testing.T,
	states []S,
	legal map[[2]S]struct{},
	transition func(S, S) error,
	illegalError error,
) {
	t.Helper()
	for _, from := range states {
		for _, to := range states {
			err := transition(from, to)
			_, wantLegal := legal[[2]S{from, to}]
			if wantLegal && err != nil {
				t.Errorf("%s -> %s: %v", from, to, err)
			}
			if !wantLegal && !errors.Is(err, illegalError) {
				t.Errorf("%s -> %s: want %v, got %v", from, to, illegalError, err)
			}
		}
	}
}

func transitionSet[S ~string](legal map[S][]S) map[[2]S]struct{} {
	result := make(map[[2]S]struct{})
	for from, targets := range legal {
		for _, to := range targets {
			result[[2]S{from, to}] = struct{}{}
		}
	}
	return result
}
