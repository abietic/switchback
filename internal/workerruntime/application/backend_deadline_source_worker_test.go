package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestBackendDeadlineSourceWorkerQueuesInterruptBeforeFrozenForceAfter(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	now := deadlineAt.Add(5 * time.Second)
	current := backendDeadlineWorkerSettlement(t, deadlineAt)
	store := &backendDeadlineWorkerSourceStore{current: current}
	effects := &recordingBackendDeadlineSourceEffects{}
	worker := BackendDeadlineSourceWorker{
		Source:  TurnSettlementSourceService{Store: store},
		Effects: effects,
		Now:     func() time.Time { return now },
	}

	result, err := worker.Reconcile(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BackendDeadlineSourceWorkerInterruptQueued ||
		!result.NextAttemptAt.Equal(
			deadlineAt.Add(BackendDeadlineForceGrace),
		) {
		t.Fatalf("result=%+v", result)
	}
	if store.advanceCalls != 1 ||
		store.lastRecord.Command.ToPhase !=
			BackendDeadlineSourceInterruptQueued {
		t.Fatalf(
			"advance calls=%d record=%+v",
			store.advanceCalls,
			store.lastRecord,
		)
	}
	envelope := store.lastRecord.InterruptLogicalEnvelope
	if envelope == nil ||
		envelope.MessageID != "deadline-interrupt-turn-1" ||
		envelope.IdempotencyKey != "deadline-interrupt-turn-1" ||
		envelope.MessageType != "agent_turn.interrupt" ||
		envelope.WorkerSessionID != "session-1" ||
		envelope.WorkerRunID != "run-1" ||
		envelope.AgentSessionID != "agent-session-1" ||
		envelope.AgentTurnID != "turn-1" ||
		envelope.RuntimeInstanceID != "runtime-1" ||
		envelope.RuntimeGeneration != 1 ||
		envelope.ChannelEpoch != 1 ||
		string(envelope.Payload) != "{}" {
		t.Fatalf("interrupt envelope=%+v", envelope)
	}
	if effects.totalCalls() != 0 {
		t.Fatalf("unexpected external effect calls=%d", effects.totalCalls())
	}
}

func TestBackendDeadlineSourceWorkerUsesFrozenForceAfterAcrossRestart(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	restartedAt := deadlineAt.Add(31 * time.Second)
	current := backendDeadlineWorkerSettlement(t, deadlineAt)
	store := &backendDeadlineWorkerSourceStore{current: current}
	effects := &recordingBackendDeadlineSourceEffects{
		target: BackendDeadlineForceStopTarget{
			ExpectedSessionRevision: 4,
			ExpectedRuntimeRevision: 5,
			ExpectedContainerID:     "container-1",
		},
	}
	worker := BackendDeadlineSourceWorker{
		Source:  TurnSettlementSourceService{Store: store},
		Effects: effects,
		Now:     func() time.Time { return restartedAt },
	}

	result, err := worker.Reconcile(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != BackendDeadlineSourceWorkerForceStopIntent ||
		!result.NextAttemptAt.IsZero() {
		t.Fatalf("result=%+v", result)
	}
	if effects.targetCalls != 1 || effects.lastAuthority.ForceAfter !=
		deadlineAt.Add(BackendDeadlineForceGrace) {
		t.Fatalf(
			"target calls=%d authority=%+v",
			effects.targetCalls,
			effects.lastAuthority,
		)
	}
	proof := store.lastRecord.Transition.ForceStop
	if proof == nil ||
		!proof.DecidedAt.Equal(
			deadlineAt.Add(BackendDeadlineForceGrace),
		) ||
		proof.DecidedAt.Equal(restartedAt) ||
		proof.IntentID != "deadline-force-stop-turn-1" ||
		proof.RuntimeInstanceID != "runtime-1" ||
		proof.RuntimeGeneration != 1 ||
		proof.ChannelEpoch != 1 ||
		proof.ExpectedSessionRevision != 4 ||
		proof.ExpectedRuntimeRevision != 5 ||
		proof.ExpectedContainerID != "container-1" ||
		proof.CleanupScheduledCommandID !=
			"deadline-cleanup-command-turn-1" ||
		proof.CleanupIdempotencyKey != "deadline-cleanup-turn-1" {
		t.Fatalf("force proof=%+v", proof)
	}
}

func TestBackendDeadlineSourceWorkerWaitsThenForcesInterruptPhase(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	forceAfter := deadlineAt.Add(BackendDeadlineForceGrace)
	interrupt := backendDeadlineWorkerInterruptProof(deadlineAt)
	current := backendDeadlineWorkerSettlementAtPhase(
		t,
		deadlineAt,
		BackendDeadlineSourceTransition{
			ToPhase:   BackendDeadlineSourceInterruptQueued,
			Interrupt: &interrupt,
		},
	)

	t.Run("wait before boundary", func(t *testing.T) {
		store := &backendDeadlineWorkerSourceStore{current: current}
		effects := &recordingBackendDeadlineSourceEffects{}
		result, err := (BackendDeadlineSourceWorker{
			Source:  TurnSettlementSourceService{Store: store},
			Effects: effects,
			Now:     func() time.Time { return forceAfter.Add(-time.Microsecond) },
		}).Reconcile(context.Background(), current)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != BackendDeadlineSourceWorkerWaitForceAfter ||
			!result.NextAttemptAt.Equal(forceAfter) ||
			store.advanceCalls != 0 || effects.totalCalls() != 0 {
			t.Fatalf(
				"result=%+v advances=%d effects=%d",
				result,
				store.advanceCalls,
				effects.totalCalls(),
			)
		}
	})

	t.Run("force exactly at boundary", func(t *testing.T) {
		store := &backendDeadlineWorkerSourceStore{current: current}
		effects := &recordingBackendDeadlineSourceEffects{
			target: BackendDeadlineForceStopTarget{
				ExpectedSessionRevision: 4,
				ExpectedRuntimeRevision: 5,
			},
		}
		result, err := (BackendDeadlineSourceWorker{
			Source:  TurnSettlementSourceService{Store: store},
			Effects: effects,
			Now:     func() time.Time { return forceAfter },
		}).Reconcile(context.Background(), current)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != BackendDeadlineSourceWorkerForceStopIntent ||
			store.lastRecord.Command.ToPhase !=
				BackendDeadlineSourceForceStopIntent ||
			effects.targetCalls != 1 {
			t.Fatalf(
				"result=%+v record=%+v effects=%+v",
				result,
				store.lastRecord,
				effects,
			)
		}
	})
}

func TestBackendDeadlineSourceWorkerCollectsCleanupProofsOnePhaseAtATime(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	forceAfter := deadlineAt.Add(BackendDeadlineForceGrace)
	force := backendDeadlineWorkerForceProof(deadlineAt)
	writeFence := BackendDeadlineWriteFenceProof{
		ChannelOwnerFence:         7,
		ChannelRevision:           8,
		ChannelFencedAt:           forceAfter,
		ContainerRef:              "container-1",
		ContainerAbsentObservedAt: forceAfter.Add(time.Second),
	}
	writeFence.ProofDigest =
		backendDeadlineWriteFenceProofDigest(writeFence)
	cleanup := BackendDeadlineCleanupProof{
		ContainerRef:       "container-1",
		SecretVolumeRef:    "secret-1",
		ContainerExists:    false,
		SecretVolumeExists: false,
		ObservedAt:         forceAfter.Add(2 * time.Second),
		ConfirmedAt:        forceAfter.Add(2 * time.Second),
	}
	cleanup.ObservationDigest =
		backendDeadlineCleanupObservationDigest(cleanup)

	t.Run("force intent observes write fence", func(t *testing.T) {
		current := backendDeadlineWorkerSettlementAtPhase(
			t,
			deadlineAt,
			BackendDeadlineSourceTransition{
				ToPhase:   BackendDeadlineSourceForceStopIntent,
				ForceStop: &force,
			},
		)
		store := &backendDeadlineWorkerSourceStore{current: current}
		effects := &recordingBackendDeadlineSourceEffects{
			writeFence: writeFence,
		}
		result, err := (BackendDeadlineSourceWorker{
			Source:  TurnSettlementSourceService{Store: store},
			Effects: effects,
			Now:     func() time.Time { return forceAfter.Add(time.Second) },
		}).Reconcile(context.Background(), current)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != BackendDeadlineSourceWorkerWriteFenced ||
			effects.writeFenceCalls != 1 ||
			effects.cleanupCalls != 0 ||
			effects.lastWriteFenceRequest.ForceStop.IntentID !=
				force.IntentID ||
			store.lastRecord.Transition.WriteFence == nil ||
			store.lastRecord.Transition.WriteFence.ProofDigest !=
				writeFence.ProofDigest {
			t.Fatalf(
				"result=%+v effects=%+v record=%+v",
				result,
				effects,
				store.lastRecord,
			)
		}
	})

	t.Run("write fence observes complete cleanup", func(t *testing.T) {
		current := backendDeadlineWorkerSettlementAtPhase(
			t,
			deadlineAt,
			BackendDeadlineSourceTransition{
				ToPhase:   BackendDeadlineSourceForceStopIntent,
				ForceStop: &force,
			},
			BackendDeadlineSourceTransition{
				ToPhase:    BackendDeadlineSourceWriteFenced,
				WriteFence: &writeFence,
			},
		)
		store := &backendDeadlineWorkerSourceStore{current: current}
		effects := &recordingBackendDeadlineSourceEffects{
			cleanup: cleanup,
		}
		result, err := (BackendDeadlineSourceWorker{
			Source:  TurnSettlementSourceService{Store: store},
			Effects: effects,
			Now:     func() time.Time { return forceAfter.Add(2 * time.Second) },
		}).Reconcile(context.Background(), current)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != BackendDeadlineSourceWorkerCleanupConfirmed ||
			effects.writeFenceCalls != 0 ||
			effects.cleanupCalls != 1 ||
			effects.lastCleanupRequest.WriteFence.ProofDigest !=
				writeFence.ProofDigest ||
			store.lastRecord.Transition.Cleanup == nil ||
			store.lastRecord.Transition.Cleanup.ObservationDigest !=
				cleanup.ObservationDigest {
			t.Fatalf(
				"result=%+v effects=%+v record=%+v",
				result,
				effects,
				store.lastRecord,
			)
		}
	})
}

func TestBackendDeadlineSourceWorkerTreatsTerminalPhasesAsNoOp(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	force := backendDeadlineWorkerForceProof(deadlineAt)
	writeFence := BackendDeadlineWriteFenceProof{
		ChannelOwnerFence:         7,
		ChannelRevision:           8,
		ChannelFencedAt:           force.DecidedAt,
		ContainerAbsentObservedAt: force.DecidedAt.Add(time.Second),
	}
	writeFence.ProofDigest =
		backendDeadlineWriteFenceProofDigest(writeFence)
	cleanup := BackendDeadlineCleanupProof{
		ContainerExists:    false,
		SecretVolumeExists: false,
		ObservedAt:         force.DecidedAt.Add(2 * time.Second),
		ConfirmedAt:        force.DecidedAt.Add(2 * time.Second),
	}
	cleanup.ObservationDigest =
		backendDeadlineCleanupObservationDigest(cleanup)
	tests := []struct {
		name        string
		transitions []BackendDeadlineSourceTransition
		want        BackendDeadlineSourceWorkerOutcome
	}{
		{
			name: "terminal observed",
			transitions: []BackendDeadlineSourceTransition{{
				ToPhase:            BackendDeadlineSourceTerminalObserved,
				TerminalObservedAt: deadlineAt.Add(time.Second),
			}},
			want: BackendDeadlineSourceWorkerTerminalObserved,
		},
		{
			name: "cleanup confirmed",
			transitions: []BackendDeadlineSourceTransition{
				{
					ToPhase:   BackendDeadlineSourceForceStopIntent,
					ForceStop: &force,
				},
				{
					ToPhase:    BackendDeadlineSourceWriteFenced,
					WriteFence: &writeFence,
				},
				{
					ToPhase: BackendDeadlineSourceCleanupConfirmed,
					Cleanup: &cleanup,
				},
			},
			want: BackendDeadlineSourceWorkerCleanupConfirmed,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			current := backendDeadlineWorkerSettlementAtPhase(
				t,
				deadlineAt,
				test.transitions...,
			)
			store := &backendDeadlineWorkerSourceStore{current: current}
			effects := &recordingBackendDeadlineSourceEffects{}
			result, err := (BackendDeadlineSourceWorker{
				Source:  TurnSettlementSourceService{Store: store},
				Effects: effects,
				Now: func() time.Time {
					return deadlineAt.Add(time.Hour - time.Microsecond)
				},
			}).Reconcile(context.Background(), current)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != test.want ||
				store.advanceCalls != 0 || effects.totalCalls() != 0 {
				t.Fatalf(
					"result=%+v advances=%d effects=%d",
					result,
					store.advanceCalls,
					effects.totalCalls(),
				)
			}
		})
	}
}

func TestBackendDeadlineSourceWorkerPropagatesLeaseAndTransitionConflicts(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	t.Run("locally expired lease prevents effects", func(t *testing.T) {
		current := backendDeadlineWorkerSettlement(t, deadlineAt)
		current.LeaseExpiresAt = deadlineAt.Add(time.Second)
		effects := &recordingBackendDeadlineSourceEffects{}
		store := &backendDeadlineWorkerSourceStore{current: current}
		_, err := (BackendDeadlineSourceWorker{
			Source:  TurnSettlementSourceService{Store: store},
			Effects: effects,
			Now:     func() time.Time { return deadlineAt.Add(2 * time.Second) },
		}).Reconcile(context.Background(), current)
		if !errors.Is(err, ErrTurnSettlementLeaseStale) ||
			store.advanceCalls != 0 || effects.totalCalls() != 0 {
			t.Fatalf(
				"error=%v advances=%d effects=%d",
				err,
				store.advanceCalls,
				effects.totalCalls(),
			)
		}
	})

	t.Run("backend source accepts an exact late runtime witness", func(t *testing.T) {
		current := backendDeadlineWorkerSettlement(t, deadlineAt)
		current.RuntimeInstanceID = "runtime-1"
		current.RuntimeGeneration = 1
		current.ChannelEpoch = 1
		store := &backendDeadlineWorkerSourceStore{current: current}
		_, err := (BackendDeadlineSourceWorker{
			Source: TurnSettlementSourceService{Store: store},
			Now:    func() time.Time { return deadlineAt.Add(time.Second) },
		}).Reconcile(context.Background(), current)
		if err != nil || store.advanceCalls != 1 {
			t.Fatalf(
				"error=%v advances=%d",
				err,
				store.advanceCalls,
			)
		}
	})

	t.Run("backend source rejects a mismatched runtime witness", func(t *testing.T) {
		current := backendDeadlineWorkerSettlement(t, deadlineAt)
		current.RuntimeInstanceID = "runtime-other"
		current.RuntimeGeneration = 1
		current.ChannelEpoch = 1
		store := &backendDeadlineWorkerSourceStore{current: current}
		_, err := (BackendDeadlineSourceWorker{
			Source: TurnSettlementSourceService{Store: store},
			Now:    func() time.Time { return deadlineAt.Add(time.Second) },
		}).Reconcile(context.Background(), current)
		if !errors.Is(err, ErrTurnSettlementSourceConflict) ||
			store.advanceCalls != 0 {
			t.Fatalf(
				"error=%v advances=%d",
				err,
				store.advanceCalls,
			)
		}
	})

	t.Run("store stale lease is returned unchanged", func(t *testing.T) {
		current := backendDeadlineWorkerSettlement(t, deadlineAt)
		store := &backendDeadlineWorkerSourceStore{
			current: current,
			err:     ErrTurnSettlementLeaseStale,
		}
		_, err := (BackendDeadlineSourceWorker{
			Source: TurnSettlementSourceService{Store: store},
			Now:    func() time.Time { return deadlineAt.Add(time.Second) },
		}).Reconcile(context.Background(), current)
		if !errors.Is(err, ErrTurnSettlementLeaseStale) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("transition race is returned unchanged", func(t *testing.T) {
		current := backendDeadlineWorkerSettlement(t, deadlineAt)
		store := &backendDeadlineWorkerSourceStore{
			current: current,
			err:     ErrTurnSettlementSourceConflict,
		}
		effects := &recordingBackendDeadlineSourceEffects{
			target: BackendDeadlineForceStopTarget{
				ExpectedSessionRevision: 4,
				ExpectedRuntimeRevision: 5,
			},
		}
		_, err := (BackendDeadlineSourceWorker{
			Source:  TurnSettlementSourceService{Store: store},
			Effects: effects,
			Now: func() time.Time {
				return deadlineAt.Add(BackendDeadlineForceGrace)
			},
		}).Reconcile(context.Background(), current)
		if !errors.Is(err, ErrTurnSettlementSourceConflict) {
			t.Fatalf("error=%v", err)
		}
	})
}

type recordingBackendDeadlineSourceEffects struct {
	target      BackendDeadlineForceStopTarget
	targetErr   error
	targetCalls int

	writeFence      BackendDeadlineWriteFenceProof
	writeFenceErr   error
	writeFenceCalls int

	cleanup      BackendDeadlineCleanupProof
	cleanupErr   error
	cleanupCalls int

	lastAuthority         BackendDeadlineSourceAuthority
	lastWriteFenceRequest BackendDeadlineWriteFenceObservationRequest
	lastCleanupRequest    BackendDeadlineCleanupObservationRequest
}

func (effects *recordingBackendDeadlineSourceEffects) LoadForceStopTarget(
	_ context.Context,
	authority BackendDeadlineSourceAuthority,
) (BackendDeadlineForceStopTarget, error) {
	effects.targetCalls++
	effects.lastAuthority = authority
	return effects.target, effects.targetErr
}

func (effects *recordingBackendDeadlineSourceEffects) ObserveWriteFence(
	_ context.Context,
	request BackendDeadlineWriteFenceObservationRequest,
) (BackendDeadlineWriteFenceProof, error) {
	effects.writeFenceCalls++
	effects.lastWriteFenceRequest = request
	return effects.writeFence, effects.writeFenceErr
}

func (effects *recordingBackendDeadlineSourceEffects) ObserveCleanup(
	_ context.Context,
	request BackendDeadlineCleanupObservationRequest,
) (BackendDeadlineCleanupProof, error) {
	effects.cleanupCalls++
	effects.lastCleanupRequest = request
	return effects.cleanup, effects.cleanupErr
}

func (effects *recordingBackendDeadlineSourceEffects) totalCalls() int {
	return effects.targetCalls + effects.writeFenceCalls +
		effects.cleanupCalls
}

type backendDeadlineWorkerSourceStore struct {
	current      TurnSettlement
	err          error
	advanceCalls int
	lastRecord   AdvanceBackendDeadlineSourceRecord
}

func (store *backendDeadlineWorkerSourceStore) ClaimBackendDeadlineTurnSettlement(
	context.Context,
	ClaimBackendDeadlineRecord,
) (TurnSettlement, error) {
	panic("unexpected Backend deadline claim")
}

func (store *backendDeadlineWorkerSourceStore) AdvanceBackendDeadlineSource(
	_ context.Context,
	record AdvanceBackendDeadlineSourceRecord,
) (TurnSettlement, error) {
	store.advanceCalls++
	store.lastRecord = record
	if store.err != nil {
		return TurnSettlement{}, store.err
	}
	transition := record.Transition
	if transition.ToPhase == BackendDeadlineSourceInterruptQueued {
		transition.Interrupt = &BackendDeadlineInterruptProof{
			MessageID:      record.Command.InterruptMessageID,
			IdempotencyKey: record.Command.InterruptIdempotencyKey,
			Sequence:       1,
			EnvelopeDigest: testSourceDigest("worker-interrupt-envelope"),
			CursorDigest:   testSourceDigest("worker-interrupt-cursor"),
			JournaledAt:    record.AdvancedAt,
		}
	}
	nextSource, err := ApplyBackendDeadlineSourceTransition(
		store.current.Source,
		transition,
	)
	if err != nil {
		return TurnSettlement{}, err
	}
	store.current.Source = nextSource
	store.current.Revision++
	store.current.UpdatedAt = record.AdvancedAt
	return store.current, nil
}

func (store *backendDeadlineWorkerSourceStore) ApplyBackendDeadlineTurnSettlement(
	context.Context,
	ApplyBackendDeadlineSourceRecord,
) (TurnSettlement, error) {
	panic("unexpected Backend deadline source apply")
}

func backendDeadlineWorkerSettlement(
	t *testing.T,
	deadlineAt time.Time,
) TurnSettlement {
	t.Helper()
	record, err := NewBackendDeadlineClaimRecord(
		ClaimBackendDeadlineCommand{
			TenantID:          "tenant-1",
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			AgentSessionID:    "agent-session-1",
			AgentTurnID:       "turn-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1,
			ChannelEpoch:      1,
			DeadlineAt:        deadlineAt,
			SourceRef:         "deadline-command-1",
		},
		deadlineAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return TurnSettlement{
		SettlementID:     record.SettlementID,
		TenantID:         record.Command.TenantID,
		WorkerSessionID:  record.Command.WorkerSessionID,
		WorkerRunID:      record.Command.WorkerRunID,
		AgentSessionID:   record.Command.AgentSessionID,
		AgentTurnID:      record.Command.AgentTurnID,
		Source:           record.Source,
		Phase:            TurnSettlementAccepted,
		PhaseState:       json.RawMessage(`{"schema_version":1}`),
		PhaseStateDigest: testSourceDigest("worker-phase-state"),
		Revision:         2,
		LeaseOwnerID:     "backend-deadline-worker-1",
		LeaseFence:       3,
		LeaseExpiresAt:   deadlineAt.Add(time.Hour),
		AcceptedAt:       deadlineAt,
		UpdatedAt:        deadlineAt,
	}
}

func backendDeadlineWorkerSettlementAtPhase(
	t *testing.T,
	deadlineAt time.Time,
	transitions ...BackendDeadlineSourceTransition,
) TurnSettlement {
	t.Helper()
	current := backendDeadlineWorkerSettlement(t, deadlineAt)
	for _, transition := range transitions {
		next, err := ApplyBackendDeadlineSourceTransition(
			current.Source,
			transition,
		)
		if err != nil {
			t.Fatal(err)
		}
		current.Source = next
		current.Revision++
	}
	return current
}

func backendDeadlineWorkerInterruptProof(
	deadlineAt time.Time,
) BackendDeadlineInterruptProof {
	return BackendDeadlineInterruptProof{
		MessageID:      "deadline-interrupt-turn-1",
		IdempotencyKey: "deadline-interrupt-turn-1",
		Sequence:       1,
		EnvelopeDigest: testSourceDigest("worker-existing-envelope"),
		CursorDigest:   testSourceDigest("worker-existing-cursor"),
		JournaledAt:    deadlineAt.Add(time.Second),
	}
}

func backendDeadlineWorkerForceProof(
	deadlineAt time.Time,
) BackendDeadlineForceStopProof {
	return BackendDeadlineForceStopProof{
		IntentID:                  "deadline-force-stop-turn-1",
		DecidedAt:                 deadlineAt.Add(BackendDeadlineForceGrace),
		RuntimeInstanceID:         "runtime-1",
		RuntimeGeneration:         1,
		ChannelEpoch:              1,
		ExpectedSessionRevision:   4,
		ExpectedRuntimeRevision:   5,
		ExpectedContainerID:       "container-1",
		CleanupScheduledCommandID: "deadline-cleanup-command-turn-1",
		CleanupIdempotencyKey:     "deadline-cleanup-turn-1",
	}
}

var _ BackendDeadlineSourceEffects = (*recordingBackendDeadlineSourceEffects)(nil)
var _ TurnSettlementSourceStore = (*backendDeadlineWorkerSourceStore)(nil)
