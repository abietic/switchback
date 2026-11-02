package runstate

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrRevisionMismatch  = errors.New("run revision does not match")
	ErrInvalidTransition = errors.New("invalid run state transition")
)

type Store struct {
	mu  sync.RWMutex
	run Run
}

func NewStore(run Run) (*Store, error) {
	if run.State != StateProvisioning {
		return nil, fmt.Errorf("initial run state must be %q", StateProvisioning)
	}
	if run.RuntimeGeneration != 1 {
		return nil, fmt.Errorf("runtime generation must be 1")
	}
	if run.AgentSessionID != "" {
		return nil, fmt.Errorf("initial agent session ID must be empty")
	}
	if run.ActiveTurnID != "" {
		return nil, fmt.Errorf("initial active turn ID must be empty")
	}
	if len(run.Turns) != 0 {
		return nil, fmt.Errorf("initial turns must be empty")
	}
	return &Store{run: cloneRun(run)}, nil
}

func (s *Store) Snapshot() Run {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return cloneRun(s.run)
}

func (s *Store) Advance(expectedRevision uint64, target State) (Run, error) {
	return s.mutate(expectedRevision, func(run *Run) error {
		if !canAdvance(run.State, target) {
			return fmt.Errorf("%w: %q to %q", ErrInvalidTransition, run.State, target)
		}
		if target == StateReady && run.AgentSessionID == "" {
			return fmt.Errorf("%w: agent session ID is required before ready", ErrInvalidTransition)
		}
		run.State = target
		if target == StateFailed {
			run.ActiveTurnID = ""
		}
		return nil
	})
}

func (s *Store) BindAgentSession(expectedRevision uint64, agentSessionID string) (Run, error) {
	return s.mutate(expectedRevision, func(run *Run) error {
		if run.State != StateConnecting {
			return fmt.Errorf("%w: cannot bind agent session from %q", ErrInvalidTransition, run.State)
		}
		if agentSessionID == "" {
			return fmt.Errorf("agent session ID is required")
		}
		if run.AgentSessionID != "" {
			return fmt.Errorf("%w: agent session is already bound", ErrInvalidTransition)
		}

		run.AgentSessionID = agentSessionID
		return nil
	})
}

func (s *Store) StartTurn(expectedRevision uint64, turnID string) (Run, error) {
	return s.mutate(expectedRevision, func(run *Run) error {
		if run.State != StateReady && run.State != StateIdle {
			return fmt.Errorf("%w: cannot start a turn from %q", ErrInvalidTransition, run.State)
		}
		if run.AgentSessionID == "" {
			return fmt.Errorf("%w: agent session ID is required before starting a turn", ErrInvalidTransition)
		}
		if run.ActiveTurnID != "" {
			return fmt.Errorf("%w: turn %q is already active", ErrInvalidTransition, run.ActiveTurnID)
		}
		if turnID == "" {
			return fmt.Errorf("turn ID is required")
		}

		run.State = StateTurnRunning
		run.ActiveTurnID = turnID
		run.Turns = append(run.Turns, Turn{ID: turnID})
		return nil
	})
}

func (s *Store) CompleteTurn(expectedRevision uint64, turnID string) (Run, error) {
	return s.mutate(expectedRevision, func(run *Run) error {
		if run.State != StateTurnRunning || run.ActiveTurnID != turnID {
			return fmt.Errorf("%w: cannot complete turn %q from %q", ErrInvalidTransition, turnID, run.State)
		}

		if len(run.Turns) == 1 {
			run.State = StateIdle
		} else {
			run.State = StateCompleted
		}
		run.ActiveTurnID = ""
		return nil
	})
}

func (s *Store) SetExecutionOutcome(expectedRevision uint64, outcome ExecutionOutcome) (Run, error) {
	return s.mutate(expectedRevision, func(run *Run) error {
		switch outcome {
		case ExecutionPending, ExecutionCompleted, ExecutionFailed:
			run.ExecutionOutcome = outcome
			return nil
		default:
			return fmt.Errorf("unknown execution outcome %q", outcome)
		}
	})
}

func (s *Store) SetSpikeAcceptance(expectedRevision uint64, acceptance SpikeAcceptance) (Run, error) {
	return s.mutate(expectedRevision, func(run *Run) error {
		switch acceptance {
		case AcceptancePending, AcceptancePassed, AcceptanceFailed:
			run.SpikeAcceptance = acceptance
			return nil
		default:
			return fmt.Errorf("unknown spike acceptance %q", acceptance)
		}
	})
}

func (s *Store) mutate(expectedRevision uint64, apply func(*Run) error) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if expectedRevision != s.run.Revision {
		return cloneRun(s.run), fmt.Errorf("%w: got %d, want %d", ErrRevisionMismatch, expectedRevision, s.run.Revision)
	}
	if err := apply(&s.run); err != nil {
		return cloneRun(s.run), err
	}

	s.run.Revision++
	return cloneRun(s.run), nil
}

func canAdvance(current, target State) bool {
	switch current {
	case StateProvisioning:
		return target == StateConnecting || target == StateFailed
	case StateConnecting:
		return target == StateReady || target == StateFailed
	case StateReady, StateTurnRunning, StateIdle:
		return target == StateFailed
	case StateFailed:
		return target == StateDiagnosticHold
	case StateDiagnosticHold, StateCompleted:
		return target == StateShuttingDown
	default:
		return false
	}
}
