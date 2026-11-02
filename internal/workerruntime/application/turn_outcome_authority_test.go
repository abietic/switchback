package application

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyRuntimeTerminalOutcomeSeparatesRecoverySignal(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	classification, err := ClassifyRuntimeTerminalOutcome(
		deadline,
		RuntimeTerminalOutcomeCandidate{
			AcceptedAt:  deadline.Add(-time.Second),
			Reason:      "acp_eof",
			Recoverable: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if classification.Terminal != nil {
		t.Fatalf(
			"ACP EOF became terminal authority: %+v",
			*classification.Terminal,
		)
	}
	want := &RuntimeRecoverySignal{
		Reason:     "acp_eof",
		AcceptedAt: deadline.Add(-time.Second),
	}
	if classification.RecoverySignal == nil ||
		*classification.RecoverySignal != *want {
		t.Fatalf(
			"recovery signal=%+v want=%+v",
			classification.RecoverySignal,
			want,
		)
	}
}

func TestClassifyRuntimeTerminalOutcomeDeadlineWinsAtRecoveryBoundary(
	t *testing.T,
) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	classification, err := ClassifyRuntimeTerminalOutcome(
		deadline,
		RuntimeTerminalOutcomeCandidate{
			AcceptedAt:  deadline,
			Reason:      "acp_eof",
			Recoverable: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := backendDeadlineAuthority(deadline)
	if classification.Terminal == nil ||
		*classification.Terminal != want ||
		classification.RecoverySignal != nil {
		t.Fatalf(
			"classification=%+v want terminal=%+v",
			classification,
			want,
		)
	}
}

func TestArbitrateTurnOutcomeAuthorityUsesEarliestDurableFact(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		command ArbitrateTurnOutcomeAuthorityCommand
		want    TurnOutcomeAuthority
	}{
		{
			name: "normal Runtime terminal accepted before deadline",
			command: ArbitrateTurnOutcomeAuthorityCommand{
				DeadlineAt: deadline,
				RuntimeTerminal: &RuntimeTerminalOutcomeCandidate{
					AcceptedAt: deadline.Add(-time.Second),
					Reason:     "end_turn",
				},
			},
			want: TurnOutcomeAuthority{
				Source:     TurnOutcomeSourceRuntimeEvent,
				Reason:     "end_turn",
				OccurredAt: deadline.Add(-time.Second),
				Terminal:   true,
			},
		},
		{
			name: "deadline wins at exact acceptance boundary",
			command: ArbitrateTurnOutcomeAuthorityCommand{
				DeadlineAt: deadline,
				RuntimeTerminal: &RuntimeTerminalOutcomeCandidate{
					AcceptedAt: deadline,
					Reason:     "end_turn",
				},
			},
			want: TurnOutcomeAuthority{
				Source:     TurnOutcomeSourceBackendDeadline,
				Reason:     "agent_turn_deadline",
				OccurredAt: deadline,
				Terminal:   true,
			},
		},
		{
			name: "deadline still wins over ACP EOF accepted at the boundary",
			command: ArbitrateTurnOutcomeAuthorityCommand{
				DeadlineAt: deadline,
				RuntimeTerminal: &RuntimeTerminalOutcomeCandidate{
					AcceptedAt:  deadline,
					Reason:      "acp_eof",
					Recoverable: true,
				},
			},
			want: TurnOutcomeAuthority{
				Source:     TurnOutcomeSourceBackendDeadline,
				Reason:     "agent_turn_deadline",
				OccurredAt: deadline,
				Terminal:   true,
			},
		},
		{
			name: "observed backend deadline is a candidate without a Runtime event",
			command: ArbitrateTurnOutcomeAuthorityCommand{
				DeadlineAt:                deadline,
				BackendDeadlineObservedAt: deadline.Add(time.Second),
			},
			want: TurnOutcomeAuthority{
				Source:     TurnOutcomeSourceBackendDeadline,
				Reason:     "agent_turn_deadline",
				OccurredAt: deadline,
				Terminal:   true,
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ArbitrateTurnOutcomeAuthority(test.command)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("authority=%+v want=%+v", got, test.want)
			}
		})
	}
}

func TestArbitrateTurnOutcomeAuthorityDoesNotReelectPersistedAuthority(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	persisted := TurnOutcomeAuthority{
		Source:     TurnOutcomeSourceRuntimeEvent,
		Reason:     "end_turn",
		OccurredAt: deadline.Add(-time.Second),
		Terminal:   true,
	}
	got, err := ArbitrateTurnOutcomeAuthority(
		ArbitrateTurnOutcomeAuthorityCommand{
			Persisted:                 &persisted,
			DeadlineAt:                deadline,
			BackendDeadlineObservedAt: deadline.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != persisted {
		t.Fatalf("persisted authority was reelected: got=%+v want=%+v", got, persisted)
	}
}

func TestArbitrateTurnOutcomeAuthorityRejectsPersistedRecoveryClaim(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	_, err := ArbitrateTurnOutcomeAuthority(
		ArbitrateTurnOutcomeAuthorityCommand{
			Persisted: &TurnOutcomeAuthority{
				Source:     TurnOutcomeSourceRuntimeRecovery,
				Reason:     "container_exit",
				OccurredAt: deadline.Add(-time.Minute),
				Terminal:   false,
			},
			DeadlineAt:                deadline,
			BackendDeadlineObservedAt: deadline.Add(time.Second),
		},
	)
	if !errors.Is(err, ErrTurnOutcomeAuthorityConflict) {
		t.Fatalf("persisted recovery error=%v", err)
	}
}

func TestArbitrateTurnOutcomeAuthorityRejectsRecoverySignalAsTerminalFact(
	t *testing.T,
) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	_, err := ArbitrateTurnOutcomeAuthority(
		ArbitrateTurnOutcomeAuthorityCommand{
			DeadlineAt: deadline,
			RuntimeTerminal: &RuntimeTerminalOutcomeCandidate{
				AcceptedAt:  deadline.Add(-time.Second),
				Reason:      "acp_eof",
				Recoverable: true,
			},
		},
	)
	if !errors.Is(err, ErrTurnOutcomeAuthorityConflict) {
		t.Fatalf("recovery signal arbitration error=%v", err)
	}
}

func TestArbitrateTurnOutcomeAuthorityRejectsMissingOrInvalidEvidence(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []ArbitrateTurnOutcomeAuthorityCommand{
		{},
		{
			DeadlineAt: deadline,
			RuntimeTerminal: &RuntimeTerminalOutcomeCandidate{
				AcceptedAt: deadline.Add(-time.Second),
			},
		},
		{
			DeadlineAt:                deadline,
			BackendDeadlineObservedAt: deadline.Add(-time.Nanosecond),
		},
		{
			Persisted: &TurnOutcomeAuthority{
				Source:     TurnOutcomeSourceRuntimeRecovery,
				Reason:     "acp_eof",
				OccurredAt: deadline,
				Terminal:   true,
			},
		},
	}
	for index, command := range tests {
		if _, err := ArbitrateTurnOutcomeAuthority(command); !errors.Is(
			err,
			ErrTurnOutcomeAuthorityConflict,
		) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
}
