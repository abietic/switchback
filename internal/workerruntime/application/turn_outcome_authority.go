package application

import (
	"errors"
	"fmt"
	"time"
)

var ErrTurnOutcomeAuthorityConflict = errors.New(
	"AgentTurn outcome authority conflicts",
)

var ErrTurnOutcomeAuthorityClaimed = errors.New(
	"AgentTurn outcome authority is already claimed",
)

// TurnOutcomeSource names the durable owner whose fact is allowed to settle or
// continue an AgentTurn. It is deliberately independent from transport and
// orchestration phases: callers arbitrate facts once, persist the result, then
// replay that exact authority after restart.
type TurnOutcomeSource string

const (
	TurnOutcomeSourceRuntimeEvent    TurnOutcomeSource = "runtime_event"
	TurnOutcomeSourceBackendDeadline TurnOutcomeSource = "backend_deadline"
	// Deprecated: Runtime recovery is a preemptible execution claim, not a
	// terminal AgentTurn outcome source. It remains named only so persisted
	// legacy values can be rejected explicitly.
	TurnOutcomeSourceRuntimeRecovery TurnOutcomeSource = "runtime_recovery"
)

type TurnOutcomeAuthority struct {
	Source     TurnOutcomeSource
	Reason     string
	OccurredAt time.Time
	Terminal   bool
}

type RuntimeTerminalOutcomeCandidate struct {
	AcceptedAt time.Time
	Reason     string
	// Recoverable means the Runtime event is a recovery signal, not a business
	// terminal result. P0 uses it only for acp_eof.
	Recoverable bool
}

type RuntimeRecoverySignal struct {
	Reason     string
	AcceptedAt time.Time
}

type RuntimeTerminalOutcomeClassification struct {
	Terminal       *TurnOutcomeAuthority
	RecoverySignal *RuntimeRecoverySignal
}

func ClassifyRuntimeTerminalOutcome(
	deadlineAt time.Time,
	candidate RuntimeTerminalOutcomeCandidate,
) (RuntimeTerminalOutcomeClassification, error) {
	if deadlineAt.IsZero() ||
		candidate.AcceptedAt.IsZero() ||
		validateCanonical("terminal_reason", candidate.Reason) != nil {
		return RuntimeTerminalOutcomeClassification{},
			ErrTurnOutcomeAuthorityConflict
	}
	deadlineAt = deadlineAt.UTC()
	acceptedAt := candidate.AcceptedAt.UTC()
	if !acceptedAt.Before(deadlineAt) {
		deadline := backendDeadlineAuthority(deadlineAt)
		return RuntimeTerminalOutcomeClassification{
			Terminal: &deadline,
		}, nil
	}
	if candidate.Recoverable {
		if candidate.Reason != "acp_eof" {
			return RuntimeTerminalOutcomeClassification{},
				ErrTurnOutcomeAuthorityConflict
		}
		return RuntimeTerminalOutcomeClassification{
			RecoverySignal: &RuntimeRecoverySignal{
				Reason:     candidate.Reason,
				AcceptedAt: acceptedAt,
			},
		}, nil
	}
	if candidate.Reason == "acp_eof" {
		return RuntimeTerminalOutcomeClassification{},
			ErrTurnOutcomeAuthorityConflict
	}
	runtimeTerminal := TurnOutcomeAuthority{
		Source:     TurnOutcomeSourceRuntimeEvent,
		Reason:     candidate.Reason,
		OccurredAt: acceptedAt,
		Terminal:   true,
	}
	return RuntimeTerminalOutcomeClassification{
		Terminal: &runtimeTerminal,
	}, nil
}

type ArbitrateTurnOutcomeAuthorityCommand struct {
	// Persisted terminal facts are immutable. Runtime recovery claims are
	// persisted by RuntimeRecoveryIncident and never enter this field.
	Persisted *TurnOutcomeAuthority

	DeadlineAt                time.Time
	BackendDeadlineObservedAt time.Time
	RuntimeTerminal           *RuntimeTerminalOutcomeCandidate
}

// ArbitrateTurnOutcomeAuthority selects the earliest durable fact. A deadline
// is effective at DeadlineAt (not at poll time), and wins an exact-time tie.
// Persisted terminal facts are never reelected. Runtime recovery is a
// preemptible execution claim and must be coordinated outside this selector.
func ArbitrateTurnOutcomeAuthority(
	command ArbitrateTurnOutcomeAuthorityCommand,
) (TurnOutcomeAuthority, error) {
	if command.Persisted != nil {
		persisted := normalizedTurnOutcomeAuthority(*command.Persisted)
		if err := validateTurnOutcomeAuthority(persisted); err != nil {
			return TurnOutcomeAuthority{}, err
		}
		return persisted, nil
	}

	var candidates []TurnOutcomeAuthority
	if !command.BackendDeadlineObservedAt.IsZero() {
		if command.DeadlineAt.IsZero() ||
			command.BackendDeadlineObservedAt.Before(command.DeadlineAt) {
			return TurnOutcomeAuthority{}, ErrTurnOutcomeAuthorityConflict
		}
		candidates = append(candidates, backendDeadlineAuthority(
			command.DeadlineAt,
		))
	}
	if command.RuntimeTerminal != nil {
		classification, err := ClassifyRuntimeTerminalOutcome(
			command.DeadlineAt,
			*command.RuntimeTerminal,
		)
		if err != nil {
			return TurnOutcomeAuthority{}, err
		}
		if classification.Terminal != nil {
			candidates = append(candidates, *classification.Terminal)
		}
	}
	if len(candidates) == 0 {
		return TurnOutcomeAuthority{}, ErrTurnOutcomeAuthorityConflict
	}

	winner := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.OccurredAt.Before(winner.OccurredAt) ||
			(candidate.OccurredAt.Equal(winner.OccurredAt) &&
				turnOutcomeSourceRank(candidate.Source) <
					turnOutcomeSourceRank(winner.Source)) {
			winner = candidate
		}
	}
	return winner, nil
}

func backendDeadlineAuthority(deadlineAt time.Time) TurnOutcomeAuthority {
	return TurnOutcomeAuthority{
		Source:     TurnOutcomeSourceBackendDeadline,
		Reason:     "agent_turn_deadline",
		OccurredAt: deadlineAt.UTC(),
		Terminal:   true,
	}
}

func normalizedTurnOutcomeAuthority(
	authority TurnOutcomeAuthority,
) TurnOutcomeAuthority {
	authority.OccurredAt = authority.OccurredAt.UTC()
	return authority
}

func validateTurnOutcomeAuthority(authority TurnOutcomeAuthority) error {
	if authority.OccurredAt.IsZero() ||
		validateCanonical("turn_outcome_reason", authority.Reason) != nil {
		return ErrTurnOutcomeAuthorityConflict
	}
	switch authority.Source {
	case TurnOutcomeSourceRuntimeEvent:
		if !authority.Terminal || authority.Reason == "acp_eof" {
			return ErrTurnOutcomeAuthorityConflict
		}
	case TurnOutcomeSourceBackendDeadline:
		if !authority.Terminal ||
			authority.Reason != "agent_turn_deadline" {
			return ErrTurnOutcomeAuthorityConflict
		}
	default:
		return fmt.Errorf(
			"%w: unsupported source %q",
			ErrTurnOutcomeAuthorityConflict,
			authority.Source,
		)
	}
	return nil
}

func turnOutcomeSourceRank(source TurnOutcomeSource) int {
	switch source {
	case TurnOutcomeSourceBackendDeadline:
		return 1
	case TurnOutcomeSourceRuntimeEvent:
		return 2
	default:
		return 3
	}
}
