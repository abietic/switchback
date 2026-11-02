package domain

import (
	"errors"
	"fmt"
)

var ErrIllegalTaskTransition = errors.New("illegal Task transition")

type TaskStatus string

const (
	TaskQueued      TaskStatus = "queued"
	TaskRunning     TaskStatus = "running"
	TaskWaitingUser TaskStatus = "waiting_user"
	TaskDelivered   TaskStatus = "delivered"
	TaskCompleted   TaskStatus = "completed"
	TaskFailed      TaskStatus = "failed"
	TaskCancelled   TaskStatus = "cancelled"
)

var taskTransitions = map[TaskStatus]map[TaskStatus]struct{}{
	TaskQueued: {
		TaskRunning: {}, TaskWaitingUser: {}, TaskFailed: {}, TaskCancelled: {},
	},
	TaskRunning: {
		TaskWaitingUser: {}, TaskDelivered: {}, TaskCompleted: {}, TaskFailed: {}, TaskCancelled: {},
	},
	TaskWaitingUser: {
		TaskRunning: {}, TaskDelivered: {}, TaskFailed: {}, TaskCancelled: {},
	},
	TaskDelivered: {
		TaskRunning: {}, TaskCompleted: {}, TaskCancelled: {},
	},
}

func (status TaskStatus) Transition(target TaskStatus) (TaskStatus, error) {
	if _, ok := taskTransitions[status][target]; !ok {
		return status, fmt.Errorf("%w: %q -> %q", ErrIllegalTaskTransition, status, target)
	}
	return target, nil
}

func (status TaskStatus) IsTerminal() bool {
	return status == TaskCompleted || status == TaskFailed || status == TaskCancelled
}
