package domain

import (
	"errors"
	"fmt"
)

var (
	ErrIllegalWorkerSessionTransition = errors.New("illegal WorkerSession transition")
	ErrIllegalWorkerRunTransition     = errors.New("illegal WorkerRun transition")
	ErrIllegalAgentTurnTransition     = errors.New("illegal AgentTurn transition")
)

type WorkerSessionStatus string

const (
	SessionAllocating  WorkerSessionStatus = "allocating"
	SessionInteractive WorkerSessionStatus = "interactive"
	SessionRunning     WorkerSessionStatus = "running"
	SessionRecovering  WorkerSessionStatus = "recovering"
	SessionWaitingUser WorkerSessionStatus = "waiting_user"
	SessionIdle        WorkerSessionStatus = "idle"
	SessionLost        WorkerSessionStatus = "lost"
	SessionExpired     WorkerSessionStatus = "expired"
	SessionClosed      WorkerSessionStatus = "closed"
)

var workerSessionTransitions = transitions(map[WorkerSessionStatus][]WorkerSessionStatus{
	SessionAllocating:  {SessionInteractive, SessionLost, SessionClosed},
	SessionInteractive: {SessionRunning, SessionRecovering, SessionIdle, SessionLost, SessionExpired, SessionClosed},
	SessionRunning:     {SessionInteractive, SessionRecovering, SessionWaitingUser, SessionIdle, SessionLost, SessionExpired, SessionClosed},
	SessionRecovering:  {SessionInteractive, SessionRunning, SessionWaitingUser, SessionIdle, SessionLost, SessionExpired, SessionClosed},
	SessionWaitingUser: {SessionInteractive, SessionRunning, SessionRecovering, SessionIdle, SessionLost, SessionExpired, SessionClosed},
	SessionIdle:        {SessionInteractive, SessionRunning, SessionRecovering, SessionLost, SessionExpired, SessionClosed},
	SessionLost:        {SessionExpired, SessionClosed},
})

func (status WorkerSessionStatus) Transition(target WorkerSessionStatus) (WorkerSessionStatus, error) {
	return applyTransition(status, target, workerSessionTransitions, ErrIllegalWorkerSessionTransition)
}

func (status WorkerSessionStatus) IsTerminal() bool {
	return status == SessionExpired || status == SessionClosed
}

type WorkerRunStatus string

const (
	RunStarting    WorkerRunStatus = "starting"
	RunRunning     WorkerRunStatus = "running"
	RunWaitingUser WorkerRunStatus = "waiting_user"
	RunCompleted   WorkerRunStatus = "completed"
	RunFailed      WorkerRunStatus = "failed"
	RunCancelled   WorkerRunStatus = "cancelled"
	RunTimedOut    WorkerRunStatus = "timed_out"
)

var workerRunTransitions = transitions(map[WorkerRunStatus][]WorkerRunStatus{
	RunStarting:    {RunRunning, RunFailed, RunCancelled, RunTimedOut},
	RunRunning:     {RunWaitingUser, RunCompleted, RunFailed, RunCancelled, RunTimedOut},
	RunWaitingUser: {RunRunning, RunCompleted, RunFailed, RunCancelled, RunTimedOut},
})

func (status WorkerRunStatus) Transition(target WorkerRunStatus) (WorkerRunStatus, error) {
	return applyTransition(status, target, workerRunTransitions, ErrIllegalWorkerRunTransition)
}

func (status WorkerRunStatus) IsTerminal() bool {
	return status == RunCompleted || status == RunFailed || status == RunCancelled || status == RunTimedOut
}

type AgentTurnStatus string

const (
	TurnAccepted  AgentTurnStatus = "accepted"
	TurnQueued    AgentTurnStatus = "queued"
	TurnDelivered AgentTurnStatus = "delivered"
	TurnConsumed  AgentTurnStatus = "consumed"
	TurnCompleted AgentTurnStatus = "completed"
	TurnCancelled AgentTurnStatus = "cancelled"
	TurnTimedOut  AgentTurnStatus = "timed_out"
	TurnFailed    AgentTurnStatus = "failed"
)

var agentTurnTransitions = transitions(map[AgentTurnStatus][]AgentTurnStatus{
	TurnAccepted:  {TurnQueued, TurnDelivered, TurnCancelled, TurnFailed},
	TurnQueued:    {TurnDelivered, TurnCancelled, TurnFailed},
	TurnDelivered: {TurnConsumed, TurnCompleted, TurnCancelled, TurnFailed},
	TurnConsumed:  {TurnCompleted, TurnCancelled, TurnTimedOut, TurnFailed},
})

func (status AgentTurnStatus) Transition(target AgentTurnStatus) (AgentTurnStatus, error) {
	return applyTransition(status, target, agentTurnTransitions, ErrIllegalAgentTurnTransition)
}

func (status AgentTurnStatus) IsTerminal() bool {
	return status == TurnCompleted || status == TurnCancelled || status == TurnTimedOut || status == TurnFailed
}

func transitions[S ~string](source map[S][]S) map[S]map[S]struct{} {
	result := make(map[S]map[S]struct{}, len(source))
	for from, targets := range source {
		result[from] = make(map[S]struct{}, len(targets))
		for _, target := range targets {
			result[from][target] = struct{}{}
		}
	}
	return result
}

func applyTransition[S ~string](status, target S, allowed map[S]map[S]struct{}, sentinel error) (S, error) {
	if _, ok := allowed[status][target]; !ok {
		return status, fmt.Errorf("%w: %q -> %q", sentinel, status, target)
	}
	return target, nil
}
