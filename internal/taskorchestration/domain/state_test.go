package domain_test

import (
	"errors"
	"testing"

	"github.com/abietic/switchback/internal/taskorchestration/domain"
)

func TestTaskStatusTransitionsAreExhaustive(t *testing.T) {
	states := []domain.TaskStatus{
		domain.TaskQueued,
		domain.TaskRunning,
		domain.TaskWaitingUser,
		domain.TaskDelivered,
		domain.TaskCompleted,
		domain.TaskFailed,
		domain.TaskCancelled,
	}
	legal := transitionSet[domain.TaskStatus](map[domain.TaskStatus][]domain.TaskStatus{
		domain.TaskQueued:      {domain.TaskRunning, domain.TaskWaitingUser, domain.TaskFailed, domain.TaskCancelled},
		domain.TaskRunning:     {domain.TaskWaitingUser, domain.TaskDelivered, domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled},
		domain.TaskWaitingUser: {domain.TaskRunning, domain.TaskDelivered, domain.TaskFailed, domain.TaskCancelled},
		domain.TaskDelivered:   {domain.TaskRunning, domain.TaskCompleted, domain.TaskCancelled},
	})

	for _, from := range states {
		for _, to := range states {
			result, err := from.Transition(to)
			_, wantLegal := legal[[2]domain.TaskStatus{from, to}]
			if wantLegal {
				if err != nil || result != to {
					t.Errorf("%s -> %s: result=%s err=%v", from, to, result, err)
				}
			} else if !errors.Is(err, domain.ErrIllegalTaskTransition) {
				t.Errorf("%s -> %s: want illegal transition, got %v", from, to, err)
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
