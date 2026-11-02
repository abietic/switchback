package application

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBackendDeadlineSourceTransitionsPreserveExactProofPrefixes(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	initial := backendDeadlineTransitionSource(t, deadlineAt)
	interrupt := backendDeadlineInterruptTestProof(deadlineAt)
	queued, err := ApplyBackendDeadlineSourceTransition(
		initial,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &interrupt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ApplyBackendDeadlineSourceTransition(
		queued,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &interrupt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.StateDigest != queued.StateDigest ||
		string(replayed.State) != string(queued.State) {
		t.Fatalf("exact interrupt replay changed source: %+v", replayed)
	}
	sameInstant := interrupt
	sameInstant.JournaledAt = sameInstant.JournaledAt.In(
		time.FixedZone("UTC-equivalent", 0),
	)
	replayed, err = ApplyBackendDeadlineSourceTransition(
		queued,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &sameInstant,
		},
	)
	if err != nil || replayed.StateDigest != queued.StateDigest {
		t.Fatalf(
			"same-instant interrupt replay=%+v error=%v",
			replayed,
			err,
		)
	}
	conflictingInterrupt := interrupt
	conflictingInterrupt.Sequence++
	if _, err := ApplyBackendDeadlineSourceTransition(
		queued,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &conflictingInterrupt,
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("conflicting interrupt proof error=%v", err)
	}

	observedAt := deadlineAt.Add(2 * time.Second)
	terminal, err := ApplyBackendDeadlineSourceTransition(
		queued,
		BackendDeadlineSourceTransition{
			ToPhase:            BackendDeadlineSourceTerminalObserved,
			TerminalObservedAt: observedAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var terminalState BackendDeadlineSourceState
	if err := json.Unmarshal(terminal.State, &terminalState); err != nil {
		t.Fatal(err)
	}
	if terminalState.Phase != BackendDeadlineSourceTerminalObserved ||
		terminalState.Interrupt == nil ||
		!terminalState.TerminalObservedAt.Equal(observedAt) {
		t.Fatalf("terminal state=%+v", terminalState)
	}
	if _, err := ApplyBackendDeadlineSourceTransition(
		terminal,
		BackendDeadlineSourceTransition{
			ToPhase: BackendDeadlineSourceForceStopIntent,
			ForceStop: backendDeadlineForceStopTestProof(
				deadlineAt,
			),
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("force stop replaced terminal witness: %v", err)
	}
}

func TestBackendDeadlineSourceForceCleanupBranchRequiresDurableOrder(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	initial := backendDeadlineTransitionSource(t, deadlineAt)
	interrupt := backendDeadlineInterruptTestProof(deadlineAt)
	queued, err := ApplyBackendDeadlineSourceTransition(
		initial,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &interrupt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	force := backendDeadlineForceStopTestProof(deadlineAt)
	forced, err := ApplyBackendDeadlineSourceTransition(
		queued,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceForceStopIntent,
			ForceStop: force,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	interruptReplay, err := ApplyBackendDeadlineSourceTransition(
		forced,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &interrupt,
		},
	)
	if err != nil || interruptReplay.StateDigest != forced.StateDigest {
		t.Fatalf(
			"interrupt prefix replay=%+v error=%v",
			interruptReplay,
			err,
		)
	}
	writeFence, err := NewBackendDeadlineWriteFenceProof(
		4,
		8,
		// A Runtime may already have a durable channel fence before the
		// deadline force decision. Container absence, not fence time, must be
		// observed after the immutable force boundary.
		force.DecidedAt.Add(-time.Minute),
		"container-1",
		force.DecidedAt.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := ApplyBackendDeadlineSourceTransition(
		forced,
		BackendDeadlineSourceTransition{
			ToPhase:    BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := NewBackendDeadlineCleanupProof(
		"container-1",
		"secret-volume-1",
		writeFence.ContainerAbsentObservedAt.Add(time.Second),
		writeFence.ContainerAbsentObservedAt.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	cleaned, err := ApplyBackendDeadlineSourceTransition(
		fenced,
		BackendDeadlineSourceTransition{
			ToPhase: BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var state BackendDeadlineSourceState
	if err := json.Unmarshal(cleaned.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != BackendDeadlineSourceCleanupConfirmed ||
		state.ForceStop == nil || state.WriteFence == nil ||
		state.Cleanup == nil {
		t.Fatalf("cleanup state=%+v", state)
	}
	replayed, err := ApplyBackendDeadlineSourceTransition(
		cleaned,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceForceStopIntent,
			ForceStop: force,
		},
	)
	if err != nil || replayed.StateDigest != cleaned.StateDigest {
		t.Fatalf("earlier exact proof replay=%+v error=%v", replayed, err)
	}

	tampered := writeFence
	tampered.ContainerRef = "container-2"
	if _, err := ApplyBackendDeadlineSourceTransition(
		forced,
		BackendDeadlineSourceTransition{
			ToPhase:    BackendDeadlineSourceWriteFenced,
			WriteFence: &tampered,
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("tampered write-fence proof error=%v", err)
	}
	tamperedCleanup := cleanup
	tamperedCleanup.SecretVolumeRef = "secret-volume-2"
	if _, err := ApplyBackendDeadlineSourceTransition(
		fenced,
		BackendDeadlineSourceTransition{
			ToPhase: BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &tamperedCleanup,
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("tampered cleanup observation error=%v", err)
	}
}

func TestReconcileBackendDeadlineTerminalWitnessPreservesForcedBranch(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 13, 30, 0, 0, time.UTC)
	initial := backendDeadlineTransitionSource(t, deadlineAt)
	forced, err := ApplyBackendDeadlineSourceTransition(
		initial,
		BackendDeadlineSourceTransition{
			ToPhase: BackendDeadlineSourceForceStopIntent,
			ForceStop: backendDeadlineForceStopTestProof(
				deadlineAt,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, advanced, err := ReconcileBackendDeadlineTerminalWitness(
		forced,
		deadlineAt.Add(BackendDeadlineForceGrace+time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if advanced || reconciled.StateDigest != forced.StateDigest ||
		string(reconciled.State) != string(forced.State) {
		t.Fatalf(
			"late terminal replaced forced branch: advanced=%t source=%+v",
			advanced,
			reconciled,
		)
	}
}

func TestBackendDeadlineSourceRejectsEarlyOrIncompleteForceProof(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	initial := backendDeadlineTransitionSource(t, deadlineAt)
	early := backendDeadlineForceStopTestProof(deadlineAt)
	early.DecidedAt = deadlineAt.Add(BackendDeadlineForceGrace - time.Microsecond)
	if _, err := ApplyBackendDeadlineSourceTransition(
		initial,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceForceStopIntent,
			ForceStop: early,
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("early force proof error=%v", err)
	}
	late := backendDeadlineForceStopTestProof(deadlineAt)
	late.DecidedAt = deadlineAt.Add(BackendDeadlineForceGrace + time.Microsecond)
	if _, err := ApplyBackendDeadlineSourceTransition(
		initial,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceForceStopIntent,
			ForceStop: late,
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("mutable late force proof error=%v", err)
	}
	lateInterrupt := backendDeadlineInterruptTestProof(deadlineAt)
	lateInterrupt.JournaledAt = deadlineAt.Add(BackendDeadlineForceGrace)
	if _, err := ApplyBackendDeadlineSourceTransition(
		initial,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &lateInterrupt,
		},
	); !errors.Is(err, ErrTurnSettlementSourceConflict) {
		t.Fatalf("interrupt at force boundary error=%v", err)
	}

	var invalid BackendDeadlineSourceState
	if err := json.Unmarshal(initial.State, &invalid); err != nil {
		t.Fatal(err)
	}
	invalid.Phase = BackendDeadlineSourceWriteFenced
	encoded, _ := json.Marshal(invalid)
	candidate := initial
	candidate.State, candidate.StateDigest, _ =
		CanonicalizeTurnSettlementSourceState(encoded)
	if err := ValidateTurnSettlementSource(candidate); !errors.Is(
		err,
		ErrTurnSettlementSourceConflict,
	) {
		t.Fatalf("incomplete future source state error=%v", err)
	}
}

func backendDeadlineTransitionSource(
	t *testing.T,
	deadlineAt time.Time,
) TurnSettlementSource {
	t.Helper()
	record, err := NewBackendDeadlineClaimRecord(
		validBackendDeadlineClaim(deadlineAt),
		deadlineAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return record.Source
}

func backendDeadlineInterruptTestProof(
	deadlineAt time.Time,
) BackendDeadlineInterruptProof {
	return BackendDeadlineInterruptProof{
		MessageID:      "deadline-interrupt-turn-1",
		IdempotencyKey: "deadline-interrupt-turn-1",
		Sequence:       7,
		EnvelopeDigest: testSourceDigest("envelope"),
		CursorDigest:   testSourceDigest("cursor"),
		JournaledAt:    deadlineAt,
	}
}

func backendDeadlineForceStopTestProof(
	deadlineAt time.Time,
) *BackendDeadlineForceStopProof {
	return &BackendDeadlineForceStopProof{
		IntentID:                  "deadline-force-stop-turn-settlement-turn-1",
		DecidedAt:                 deadlineAt.Add(BackendDeadlineForceGrace),
		RuntimeInstanceID:         "runtime-1",
		RuntimeGeneration:         1,
		ChannelEpoch:              1,
		ExpectedSessionRevision:   4,
		ExpectedRuntimeRevision:   5,
		ExpectedContainerID:       "container-1",
		CleanupScheduledCommandID: "deadline-cleanup-command-turn-1",
		CleanupIdempotencyKey:     "deadline-cleanup-runtime-1-g1",
	}
}

func testSourceDigest(seed string) string {
	return digestRuntimeControlBytes([]byte(seed))
}
