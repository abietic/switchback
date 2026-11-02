package localproduct

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeTurnSettlementCoordinatorResumesFromEveryDurablePhase(t *testing.T) {
	tests := []struct {
		name       string
		phase      workerapp.TurnSettlementPhase
		checkpoint runtimeTurnSettlementCheckpoint
		wantCalls  []string
	}{
		{
			name:       "accepted",
			phase:      workerapp.TurnSettlementAccepted,
			checkpoint: runtimeTurnSettlementCheckpoint{},
			wantCalls: []string{
				"prepare_evidence",
				"checkpoint:accepted",
				"finalize_evidence",
				"advance:evidence_prepared",
				"commit_domain",
				"publish_evidence",
				"advance:evidence_published",
				"plan_task",
				"checkpoint:evidence_published",
				"settle_task",
				"advance:task_settled",
				"apply_source",
				"plan_continuation",
				"checkpoint:terminal_event_applied",
				"settle_continuation",
				"advance:continuation_settled",
				"advance:completed",
			},
		},
		{
			name:  "domain committed",
			phase: workerapp.TurnSettlementDomainCommitted,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementDomainCommitted,
			),
			wantCalls: []string{
				"publish_evidence",
				"advance:evidence_published",
				"plan_task",
				"checkpoint:evidence_published",
				"settle_task",
				"advance:task_settled",
				"apply_source",
				"plan_continuation",
				"checkpoint:terminal_event_applied",
				"settle_continuation",
				"advance:continuation_settled",
				"advance:completed",
			},
		},
		{
			name:  "terminal event applied",
			phase: workerapp.TurnSettlementTerminalEventApplied,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementTerminalEventApplied,
			),
			wantCalls: []string{
				"plan_continuation",
				"checkpoint:terminal_event_applied",
				"settle_continuation",
				"advance:continuation_settled",
				"advance:completed",
			},
		},
		{
			name:  "continuation settled",
			phase: workerapp.TurnSettlementContinuationSettled,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementContinuationSettled,
			),
			wantCalls: []string{"advance:completed"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &recordingRuntimeSettlementController{
				current: workerapp.TurnSettlement{
					SettlementID: "turn-settlement/turn-1",
					AgentTurnID:  "turn-1",
					Phase:        test.phase,
					PhaseState:   mustRuntimeSettlementCheckpoint(t, test.checkpoint),
					Revision:     7,
					LeaseOwnerID: "settler-1",
					LeaseFence:   3,
				},
			}
			effects := recordingRuntimeSettlementEffects{
				calls: &controller.calls,
			}
			coordinator := runtimeTurnSettlementCoordinator{
				controller: controller,
				effects:    effects.steps(),
			}

			settled, err := coordinator.reconcileClaimed(
				context.Background(),
				controller.current,
			)
			if err != nil {
				t.Fatal(err)
			}
			if settled.Phase != workerapp.TurnSettlementCompleted {
				t.Fatalf("settled phase=%s", settled.Phase)
			}
			if !reflect.DeepEqual(controller.calls, test.wantCalls) {
				t.Fatalf(
					"calls=%v want=%v",
					controller.calls,
					test.wantCalls,
				)
			}
		})
	}
}

func TestRuntimeTurnSettlementCoordinatorDoesNotRepeatCommittedEffectAfterResponseLoss(t *testing.T) {
	checkpoint := completeRuntimeSettlementCheckpointThrough(
		workerapp.TurnSettlementDomainCommitted,
	)
	controller := &recordingRuntimeSettlementController{
		current: workerapp.TurnSettlement{
			SettlementID: "turn-settlement/turn-1",
			AgentTurnID:  "turn-1",
			Phase:        workerapp.TurnSettlementDomainCommitted,
			PhaseState:   mustRuntimeSettlementCheckpoint(t, checkpoint),
			Revision:     9,
			LeaseOwnerID: "settler-1",
			LeaseFence:   4,
		},
		failAdvanceTo: workerapp.TurnSettlementEvidencePublished,
	}
	effects := recordingRuntimeSettlementEffects{calls: &controller.calls}
	coordinator := runtimeTurnSettlementCoordinator{
		controller: controller,
		effects:    effects.steps(),
	}

	if _, err := coordinator.reconcileClaimed(
		context.Background(),
		controller.current,
	); !errors.Is(err, errInjectedSettlementResponseLoss) {
		t.Fatalf("first reconcile error=%v", err)
	}
	if got := countRuntimeSettlementCall(controller.calls, "publish_evidence"); got != 1 {
		t.Fatalf("publication calls after response loss=%d", got)
	}

	// Simulate the PostgreSQL commit succeeding while its response was lost.
	controller.failAdvanceTo = ""
	controller.current.Phase = workerapp.TurnSettlementEvidencePublished
	checkpoint.Publication = &runtimeSettlementPublicationCheckpoint{
		TraceStreamID: "trace-turn-1",
		ArtifactIDs:   []string{"report-1", "patch-1"},
	}
	controller.current.PhaseState = mustRuntimeSettlementCheckpoint(t, checkpoint)
	if _, err := coordinator.reconcileClaimed(
		context.Background(),
		controller.current,
	); err != nil {
		t.Fatal(err)
	}
	if got := countRuntimeSettlementCall(controller.calls, "publish_evidence"); got != 1 {
		t.Fatalf("publication repeated after durable phase recovery: %d", got)
	}
}

func TestRuntimeTurnSettlementCoordinatorRejectsIncompleteEffectProofBeforeAdvance(t *testing.T) {
	tests := []struct {
		name          string
		phase         workerapp.TurnSettlementPhase
		checkpoint    runtimeTurnSettlementCheckpoint
		breakEffect   func(*runtimeSettlementEffects)
		forbiddenCall string
	}{
		{
			name:  "finalized evidence",
			phase: workerapp.TurnSettlementAccepted,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementAccepted,
			),
			breakEffect: func(effects *runtimeSettlementEffects) {
				effects.finalizeEvidence = func(
					_ context.Context,
					checkpoint runtimeTurnSettlementCheckpoint,
				) (runtimeTurnSettlementCheckpoint, error) {
					return checkpoint, nil
				}
			},
			forbiddenCall: "advance:evidence_prepared",
		},
		{
			name:  "publication",
			phase: workerapp.TurnSettlementDomainCommitted,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementDomainCommitted,
			),
			breakEffect: func(effects *runtimeSettlementEffects) {
				effects.publishEvidence = func(
					_ context.Context,
					_ workerapp.TurnSettlement,
					checkpoint runtimeTurnSettlementCheckpoint,
				) (runtimeTurnSettlementCheckpoint, error) {
					return checkpoint, nil
				}
			},
			forbiddenCall: "advance:evidence_published",
		},
		{
			name:  "Task plan",
			phase: workerapp.TurnSettlementEvidencePublished,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementEvidencePublished,
			),
			breakEffect: func(effects *runtimeSettlementEffects) {
				effects.planTask = func(
					_ context.Context,
					checkpoint runtimeTurnSettlementCheckpoint,
				) (runtimeTurnSettlementCheckpoint, error) {
					return checkpoint, nil
				}
			},
			forbiddenCall: "checkpoint:evidence_published",
		},
		{
			name:  "Task result",
			phase: workerapp.TurnSettlementEvidencePublished,
			checkpoint: func() runtimeTurnSettlementCheckpoint {
				checkpoint := completeRuntimeSettlementCheckpointThrough(
					workerapp.TurnSettlementEvidencePublished,
				)
				checkpoint.TaskPlan = &runtimeSettlementTaskPlan{
					Action: "none",
					Reason: "no_task",
				}
				return checkpoint
			}(),
			breakEffect: func(effects *runtimeSettlementEffects) {
				effects.settleTask = func(
					_ context.Context,
					checkpoint runtimeTurnSettlementCheckpoint,
				) (runtimeTurnSettlementCheckpoint, error) {
					return checkpoint, nil
				}
			},
			forbiddenCall: "advance:task_settled",
		},
		{
			name:  "continuation plan",
			phase: workerapp.TurnSettlementTerminalEventApplied,
			checkpoint: completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementTerminalEventApplied,
			),
			breakEffect: func(effects *runtimeSettlementEffects) {
				effects.planContinuation = func(
					_ context.Context,
					checkpoint runtimeTurnSettlementCheckpoint,
				) (runtimeTurnSettlementCheckpoint, error) {
					return checkpoint, nil
				}
			},
			forbiddenCall: "checkpoint:terminal_event_applied",
		},
		{
			name:  "continuation result",
			phase: workerapp.TurnSettlementTerminalEventApplied,
			checkpoint: func() runtimeTurnSettlementCheckpoint {
				checkpoint := completeRuntimeSettlementCheckpointThrough(
					workerapp.TurnSettlementTerminalEventApplied,
				)
				checkpoint.ContinuationPlan =
					&runtimeSettlementContinuationPlan{
						Action: "none",
						Reason: "no_queued_turn",
					}
				return checkpoint
			}(),
			breakEffect: func(effects *runtimeSettlementEffects) {
				effects.settleContinuation = func(
					_ context.Context,
					_ workerapp.TurnSettlement,
					checkpoint runtimeTurnSettlementCheckpoint,
				) (runtimeTurnSettlementCheckpoint, error) {
					return checkpoint, nil
				}
			},
			forbiddenCall: "advance:continuation_settled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := &recordingRuntimeSettlementController{
				current: workerapp.TurnSettlement{
					SettlementID: "turn-settlement/turn-1",
					AgentTurnID:  "turn-1",
					Phase:        test.phase,
					PhaseState: mustRuntimeSettlementCheckpoint(
						t,
						test.checkpoint,
					),
					Revision:     7,
					LeaseOwnerID: "settler-1",
					LeaseFence:   3,
				},
			}
			recordingEffects := recordingRuntimeSettlementEffects{
				calls: &controller.calls,
			}
			effects := recordingEffects.steps()
			test.breakEffect(&effects)
			coordinator := runtimeTurnSettlementCoordinator{
				controller: controller,
				effects:    effects,
			}

			if _, err := coordinator.reconcileClaimed(
				context.Background(),
				controller.current,
			); err == nil {
				t.Fatal("incomplete effect proof unexpectedly advanced")
			}
			if got := countRuntimeSettlementCall(
				controller.calls,
				test.forbiddenCall,
			); got != 0 {
				t.Fatalf(
					"forbidden persistence call %q count=%d calls=%v",
					test.forbiddenCall,
					got,
					controller.calls,
				)
			}
		})
	}
}

func TestRuntimeSettlementTerminalClassificationUsesDurableAcceptanceTime(
	t *testing.T,
) {
	deadline := time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
	turn := pendingTurn{DeadlineAt: deadline}
	tests := []struct {
		name         string
		acceptedAt   time.Time
		updateKind   string
		update       json.RawMessage
		wantReason   string
		wantTimedOut bool
		wantRecovery bool
	}{
		{
			name:       "terminal before deadline",
			acceptedAt: deadline.Add(-time.Microsecond),
			updateKind: "terminal",
			update:     json.RawMessage(`{"stop_reason":"end_turn"}`),
			wantReason: "end_turn",
		},
		{
			name:         "terminal at deadline",
			acceptedAt:   deadline,
			updateKind:   "terminal",
			update:       json.RawMessage(`{"stop_reason":"end_turn"}`),
			wantReason:   "agent_turn_deadline",
			wantTimedOut: true,
		},
		{
			name:       "agent error",
			acceptedAt: deadline.Add(-time.Second),
			updateKind: "error",
			update:     json.RawMessage(`{"code":"tool_failed"}`),
			wantReason: "agent_error",
		},
		{
			name:         "ACP EOF opens recovery",
			acceptedAt:   deadline.Add(-time.Second),
			updateKind:   "error",
			update:       json.RawMessage(`{"code":"acp_eof"}`),
			wantRecovery: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(struct {
				UpdateKind string          `json:"update_kind"`
				Update     json.RawMessage `json:"update"`
			}{
				UpdateKind: test.updateKind,
				Update:     test.update,
			})
			if err != nil {
				t.Fatal(err)
			}
			event := &workerapp.WorkerControlEvent{
				Envelope: contractsv1.RuntimeControlEnvelope{
					MessageType: "agent_turn.terminal",
				},
				Payload: payload,
			}
			got, recovery, err :=
				terminalResultFromRuntimeSettlementEvent(
					event,
					workerapp.TurnSettlement{
						AcceptedAt: test.acceptedAt,
					},
					turn,
				)
			if err != nil {
				t.Fatal(err)
			}
			if recovery != test.wantRecovery ||
				got.reason != test.wantReason ||
				got.timedOut != test.wantTimedOut ||
				got.sourceEvent != event {
				t.Fatalf(
					"terminal=%+v recovery=%t",
					got,
					recovery,
				)
			}
			if !recovery {
				wantSource := workerapp.TurnOutcomeSourceRuntimeEvent
				wantOccurredAt := test.acceptedAt
				if test.wantTimedOut {
					wantSource =
						workerapp.TurnOutcomeSourceBackendDeadline
					wantOccurredAt = deadline
				}
				if got.sourceKind != wantSource ||
					!got.occurredAt.Equal(wantOccurredAt) {
					t.Fatalf(
						"terminal authority=%s@%s want=%s@%s",
						got.sourceKind,
						got.occurredAt,
						wantSource,
						wantOccurredAt,
					)
				}
			}
		})
	}
}

func TestRuntimeSettlementRecoveryRestoresBackendDeadlineAuthority(
	t *testing.T,
) {
	deadline := time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)
	terminal, err := terminalResultFromRuntimeSettlementCheckpoint(
		runtimeTurnSettlementCheckpoint{
			Source: &runtimeSettlementSourceCheckpoint{
				Kind:       "backend_deadline",
				Reason:     "agent_turn_deadline",
				OccurredAt: deadline,
				TimedOut:   true,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.sourceKind !=
		workerapp.TurnOutcomeSourceBackendDeadline ||
		terminal.reason != "agent_turn_deadline" ||
		!terminal.timedOut ||
		!terminal.occurredAt.Equal(deadline) {
		t.Fatalf("restored terminal=%+v", terminal)
	}
}

func TestRuntimeSettlementCheckpointPreservesBackendDeadlineSourceProof(
	t *testing.T,
) {
	checkpoint := completeRuntimeSettlementCheckpointThrough(
		workerapp.TurnSettlementTerminalEventApplied,
	)
	checkpoint.Source = &runtimeSettlementSourceCheckpoint{
		Kind:       string(workerapp.TurnSettlementSourceBackendDeadline),
		Reason:     "agent_turn_deadline",
		OccurredAt: time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC),
		TimedOut:   true,
		ForcedStop: true,
	}
	checkpoint.BackendDeadlineSourceApplied = json.RawMessage(
		`{"kind":"backend_deadline","runtime_instance_id":"runtime-1"}`,
	)
	payload := mustRuntimeSettlementCheckpoint(t, checkpoint)
	decoded, err := decodeRuntimeTurnSettlementCheckpoint(
		workerapp.TurnSettlementTerminalEventApplied,
		payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	reencoded := mustRuntimeSettlementCheckpoint(t, decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(reencoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["backend_deadline_source_applied"]) !=
		string(checkpoint.BackendDeadlineSourceApplied) {
		t.Fatalf(
			"Backend deadline source proof changed: got=%s want=%s",
			fields["backend_deadline_source_applied"],
			checkpoint.BackendDeadlineSourceApplied,
		)
	}

	withoutProof := checkpoint
	withoutProof.BackendDeadlineSourceApplied = nil
	if err := validateRuntimeTurnSettlementCheckpoint(
		workerapp.TurnSettlementTerminalEventApplied,
		withoutProof,
	); err == nil {
		t.Fatal("forced Backend deadline checkpoint without source proof was accepted")
	}
	runtimeEvent := checkpoint
	runtimeEvent.Source = &runtimeSettlementSourceCheckpoint{
		Kind:   string(workerapp.TurnSettlementSourceRuntimeEvent),
		Reason: "end_turn",
	}
	if err := validateRuntimeTurnSettlementCheckpoint(
		workerapp.TurnSettlementTerminalEventApplied,
		runtimeEvent,
	); err == nil {
		t.Fatal("Runtime event checkpoint accepted Backend deadline source proof")
	}
}

func TestApplyRuntimeSettlementSourceDispatchesCleanupWithoutTerminalEvent(
	t *testing.T,
) {
	deadlineAt := time.Date(2026, 7, 27, 2, 30, 0, 0, time.UTC)
	claim, err := workerapp.NewBackendDeadlineClaimRecord(
		workerapp.ClaimBackendDeadlineCommand{
			TenantID:          "tenant-1",
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			AgentSessionID:    "agent-session-1",
			AgentTurnID:       "turn-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1,
			ChannelEpoch:      2,
			DeadlineAt:        deadlineAt,
			SourceRef:         "deadline-source-1",
		},
		deadlineAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	forceAt := deadlineAt.Add(workerapp.BackendDeadlineForceGrace)
	source, err := workerapp.ApplyBackendDeadlineSourceTransition(
		claim.Source,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase: workerapp.BackendDeadlineSourceForceStopIntent,
			ForceStop: &workerapp.BackendDeadlineForceStopProof{
				IntentID:                  "force-intent-1",
				DecidedAt:                 forceAt,
				RuntimeInstanceID:         "runtime-1",
				RuntimeGeneration:         1,
				ChannelEpoch:              2,
				ExpectedSessionRevision:   4,
				ExpectedRuntimeRevision:   5,
				ExpectedContainerID:       "container-1",
				CleanupScheduledCommandID: "cleanup-command-1",
				CleanupIdempotencyKey:     "cleanup-idempotency-1",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		3,
		6,
		forceAt,
		"container-1",
		forceAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err = workerapp.ApplyBackendDeadlineSourceTransition(
		source,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase:    workerapp.BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		"container-1",
		"secret-volume-1",
		forceAt.Add(2*time.Second),
		forceAt.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err = workerapp.ApplyBackendDeadlineSourceTransition(
		source,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase: workerapp.
				BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	current := workerapp.TurnSettlement{
		SettlementID:   claim.SettlementID,
		Source:         source,
		Phase:          workerapp.TurnSettlementTaskSettled,
		Revision:       9,
		LeaseOwnerID:   "settler-1",
		LeaseFence:     7,
		LeaseExpiresAt: forceAt.Add(time.Minute),
	}
	store := &recordingLocalTurnSettlementSourceStore{
		result: current,
	}
	store.result.Phase =
		workerapp.TurnSettlementTerminalEventApplied
	runner := &runtimeRunner{
		now: func() time.Time { return forceAt.Add(3 * time.Second) },
		settlements: workerapp.TurnSettlementService{
			Store: store,
		},
	}
	applied, err := runner.applyRuntimeSettlementSource(
		context.Background(),
		nil,
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase !=
		workerapp.TurnSettlementTerminalEventApplied ||
		store.applyCalls != 1 ||
		store.applied.Command.SettlementID != current.SettlementID ||
		store.applied.Command.ExpectedRevision != current.Revision ||
		store.applied.Command.ExpectedSourceDigest !=
			current.Source.Digest ||
		store.applied.Command.ExpectedSourceStateDigest !=
			current.Source.StateDigest ||
		store.applied.Command.LeaseOwnerID !=
			current.LeaseOwnerID ||
		store.applied.Command.LeaseFence != current.LeaseFence {
		t.Fatalf(
			"applied=%+v calls=%d record=%+v",
			applied,
			store.applyCalls,
			store.applied,
		)
	}
}

func TestApplyRuntimeSettlementSourceDispatchesCleanupWithDurableTerminalWitness(
	t *testing.T,
) {
	current, appliedAt := backendDeadlineCleanupSettlementForSourceRoutingTest(t)
	current.RuntimeInstanceID = "runtime-1"
	current.RuntimeGeneration = 1
	current.ChannelEpoch = 2
	current.TerminalEventSequence = 3
	current.TerminalMessageID = "late-terminal-1"
	current.TerminalIdempotencyKey = "late-terminal-idempotency-1"
	current.TerminalEnvelopeDigest =
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	var got workerapp.TurnSettlement
	want := current
	want.Phase = workerapp.TurnSettlementTerminalEventApplied
	runner := &runtimeRunner{
		now: func() time.Time { return appliedAt },
		applyDeadlineTerminalWitness: func(
			_ context.Context,
			settlement workerapp.TurnSettlement,
		) (workerapp.TurnSettlement, error) {
			got = settlement
			return want, nil
		},
		settlements: workerapp.TurnSettlementService{
			Store: &recordingLocalTurnSettlementSourceStore{},
		},
	}

	applied, err := runner.applyRuntimeSettlementSource(
		context.Background(),
		nil,
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase != workerapp.TurnSettlementTerminalEventApplied ||
		got.SettlementID != current.SettlementID ||
		got.TerminalMessageID != current.TerminalMessageID {
		t.Fatalf("applied=%+v routed=%+v", applied, got)
	}
}

func TestApplyRuntimeSettlementSourceRejectsPartialBackendDeadlineWitness(
	t *testing.T,
) {
	current, appliedAt := backendDeadlineCleanupSettlementForSourceRoutingTest(t)
	current.RuntimeInstanceID = "runtime-1"
	store := &recordingLocalTurnSettlementSourceStore{}
	runner := &runtimeRunner{
		now: func() time.Time { return appliedAt },
		settlements: workerapp.TurnSettlementService{
			Store: store,
		},
	}

	_, err := runner.applyRuntimeSettlementSource(
		context.Background(),
		nil,
		current,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementSourceConflict) {
		t.Fatalf("partial witness error=%v", err)
	}
	if store.applyCalls != 0 {
		t.Fatalf("partial witness reached source store calls=%d", store.applyCalls)
	}
}

func backendDeadlineCleanupSettlementForSourceRoutingTest(
	t *testing.T,
) (workerapp.TurnSettlement, time.Time) {
	t.Helper()
	deadlineAt := time.Date(2026, 7, 27, 2, 35, 0, 0, time.UTC)
	claim, err := workerapp.NewBackendDeadlineClaimRecord(
		workerapp.ClaimBackendDeadlineCommand{
			TenantID:          "tenant-1",
			WorkerSessionID:   "session-1",
			WorkerRunID:       "run-1",
			AgentSessionID:    "agent-session-1",
			AgentTurnID:       "turn-1",
			RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1,
			ChannelEpoch:      2,
			DeadlineAt:        deadlineAt,
			SourceRef:         "deadline-source-1",
		},
		deadlineAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	forceAt := deadlineAt.Add(workerapp.BackendDeadlineForceGrace)
	source, err := workerapp.ApplyBackendDeadlineSourceTransition(
		claim.Source,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase: workerapp.BackendDeadlineSourceForceStopIntent,
			ForceStop: &workerapp.BackendDeadlineForceStopProof{
				IntentID:                  "force-intent-1",
				DecidedAt:                 forceAt,
				RuntimeInstanceID:         "runtime-1",
				RuntimeGeneration:         1,
				ChannelEpoch:              2,
				ExpectedSessionRevision:   4,
				ExpectedRuntimeRevision:   5,
				ExpectedContainerID:       "container-1",
				CleanupScheduledCommandID: "cleanup-command-1",
				CleanupIdempotencyKey:     "cleanup-idempotency-1",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	writeFence, err := workerapp.NewBackendDeadlineWriteFenceProof(
		3,
		6,
		forceAt,
		"container-1",
		forceAt.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err = workerapp.ApplyBackendDeadlineSourceTransition(
		source,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase:    workerapp.BackendDeadlineSourceWriteFenced,
			WriteFence: &writeFence,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := workerapp.NewBackendDeadlineCleanupProof(
		"container-1",
		"secret-volume-1",
		forceAt.Add(2*time.Second),
		forceAt.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	source, err = workerapp.ApplyBackendDeadlineSourceTransition(
		source,
		workerapp.BackendDeadlineSourceTransition{
			ToPhase: workerapp.BackendDeadlineSourceCleanupConfirmed,
			Cleanup: &cleanup,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return workerapp.TurnSettlement{
		SettlementID:    claim.SettlementID,
		TenantID:        "tenant-1",
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentSessionID:  "agent-session-1",
		AgentTurnID:     "turn-1",
		Source:          source,
		Phase:           workerapp.TurnSettlementTaskSettled,
		Revision:        9,
		LeaseOwnerID:    "settler-1",
		LeaseFence:      7,
		LeaseExpiresAt:  forceAt.Add(time.Minute),
	}, forceAt.Add(3 * time.Second)
}

func TestStartupSettlementDrainsPredecessorBeforeExactTerminalDelivery(
	t *testing.T,
) {
	identity := contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		Kind:              "event",
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-1",
		AgentTurnID:       "turn-1",
		ChannelEpoch:      3,
		SentAt: time.Date(
			2026,
			7,
			27,
			2,
			0,
			0,
			0,
			time.UTC,
		),
	}
	predecessorEnvelope := identity
	predecessorEnvelope.Sequence = 1
	predecessorEnvelope.MessageID = "update-1"
	predecessorEnvelope.IdempotencyKey = "update-1"
	predecessorEnvelope.MessageType = "agent.update"
	predecessorEnvelope.Payload = json.RawMessage(
		`{"update_kind":"message","update":{}}`,
	)
	terminalEnvelope := identity
	terminalEnvelope.Sequence = 2
	terminalEnvelope.MessageID = "terminal-1"
	terminalEnvelope.IdempotencyKey = "terminal-1"
	terminalEnvelope.MessageType = "agent_turn.terminal"
	terminalEnvelope.Payload = json.RawMessage(
		`{"update_kind":"terminal","update":{"stop_reason":"end_turn"}}`,
	)
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(
		terminalEnvelope,
	)
	if err != nil {
		t.Fatal(err)
	}
	predecessorDelivery := &recordingWorkerControlDelivery{}
	terminalDelivery := &recordingWorkerControlDelivery{}
	events := make(chan workerapp.WorkerControlEvent, 2)
	events <- workerapp.WorkerControlEvent{
		Envelope: predecessorEnvelope,
		Payload:  predecessorEnvelope.Payload,
		Delivery: predecessorDelivery,
	}
	events <- workerapp.WorkerControlEvent{
		Envelope: terminalEnvelope,
		Payload:  terminalEnvelope.Payload,
		Delivery: terminalDelivery,
	}
	runner := &runtimeRunner{}
	event, err := runner.waitRuntimeSettlementTerminalDelivery(
		context.Background(),
		&liveRuntime{events: events},
		workerapp.TurnSettlement{
			WorkerSessionID:        "session-1",
			WorkerRunID:            "run-1",
			AgentSessionID:         "agent-session-1",
			AgentTurnID:            "turn-1",
			RuntimeInstanceID:      "runtime-1",
			RuntimeGeneration:      1,
			ChannelEpoch:           3,
			TerminalEventSequence:  2,
			TerminalMessageID:      "terminal-1",
			TerminalIdempotencyKey: "terminal-1",
			TerminalEnvelopeDigest: digest,
		},
		pendingTurn{AgentTurnID: "turn-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if event.Envelope.MessageID != "terminal-1" ||
		predecessorDelivery.calls != 1 ||
		predecessorDelivery.retryCalls != 0 ||
		terminalDelivery.calls != 0 ||
		terminalDelivery.retryCalls != 0 {
		t.Fatalf(
			"event=%+v predecessor=%+v terminal=%+v",
			event.Envelope,
			predecessorDelivery,
			terminalDelivery,
		)
	}
}

func TestRuntimeSettlementWaitsForDeliveryOnlyBeforeAtomicSourceApply(
	t *testing.T,
) {
	for _, test := range []struct {
		phase workerapp.TurnSettlementPhase
		want  bool
	}{
		{workerapp.TurnSettlementAccepted, true},
		{workerapp.TurnSettlementEvidencePrepared, true},
		{workerapp.TurnSettlementDomainCommitted, true},
		{workerapp.TurnSettlementEvidencePublished, true},
		{workerapp.TurnSettlementTaskSettled, true},
		{workerapp.TurnSettlementTerminalEventApplied, false},
		{workerapp.TurnSettlementContinuationSettled, false},
		{workerapp.TurnSettlementCompleted, false},
	} {
		if got := runtimeSettlementNeedsTerminalDelivery(
			test.phase,
		); got != test.want {
			t.Fatalf(
				"phase %s needs terminal delivery=%t want=%t",
				test.phase,
				got,
				test.want,
			)
		}
	}
}

func TestRuntimeSettlementPatchSnapshotIsDurableAndContentAddressed(t *testing.T) {
	directory := t.TempDir()
	first, err := freezeRuntimeSettlementPatchSnapshot(
		directory,
		"turn-1",
		[]byte("first patch"),
	)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := freezeRuntimeSettlementPatchSnapshot(
		directory,
		"turn-1",
		[]byte("first patch"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != first {
		t.Fatalf("repeated snapshot=%q want=%q", repeated, first)
	}
	content, err := readRuntimeSettlementPatchSnapshot(directory, first)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first patch" {
		t.Fatalf("snapshot content=%q", content)
	}
	changed, err := freezeRuntimeSettlementPatchSnapshot(
		directory,
		"turn-1",
		[]byte("second patch"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changed patch reused the frozen snapshot identity")
	}
	if _, err := readRuntimeSettlementPatchSnapshot(
		directory,
		"../outside.patch",
	); err == nil {
		t.Fatal("path traversal snapshot reference unexpectedly accepted")
	}
}

var errInjectedSettlementResponseLoss = errors.New("injected settlement response loss")

type recordingRuntimeSettlementController struct {
	current       workerapp.TurnSettlement
	calls         []string
	failAdvanceTo workerapp.TurnSettlementPhase
}

func (controller *recordingRuntimeSettlementController) checkpoint(
	_ context.Context,
	current workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (workerapp.TurnSettlement, error) {
	controller.calls = append(
		controller.calls,
		"checkpoint:"+string(current.Phase),
	)
	current.PhaseState, _ = json.Marshal(checkpoint)
	current.Revision++
	controller.current = current
	return current, nil
}

func (controller *recordingRuntimeSettlementController) advance(
	_ context.Context,
	current workerapp.TurnSettlement,
	to workerapp.TurnSettlementPhase,
	checkpoint runtimeTurnSettlementCheckpoint,
) (workerapp.TurnSettlement, error) {
	controller.calls = append(controller.calls, "advance:"+string(to))
	current.Phase = to
	current.PhaseState, _ = json.Marshal(checkpoint)
	current.Revision++
	controller.current = current
	if controller.failAdvanceTo == to {
		return workerapp.TurnSettlement{}, errInjectedSettlementResponseLoss
	}
	return current, nil
}

func (controller *recordingRuntimeSettlementController) commitDomain(
	_ context.Context,
	current workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (workerapp.TurnSettlement, runtimeTurnSettlementCheckpoint, error) {
	controller.calls = append(controller.calls, "commit_domain")
	checkpoint.Domain = &runtimeSettlementDomainCheckpoint{
		SessionRevision: 8,
		RunRevision:     4,
		TurnRevision:    3,
		RuntimeRevision: 5,
	}
	current.Phase = workerapp.TurnSettlementDomainCommitted
	current.PhaseState, _ = json.Marshal(checkpoint)
	current.Revision++
	controller.current = current
	return current, checkpoint, nil
}

func (controller *recordingRuntimeSettlementController) applySource(
	_ context.Context,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	controller.calls = append(controller.calls, "apply_source")
	current.Phase = workerapp.TurnSettlementTerminalEventApplied
	checkpoint, err := decodeRuntimeTurnSettlementCheckpoint(
		workerapp.TurnSettlementTaskSettled,
		current.PhaseState,
	)
	if err != nil {
		return workerapp.TurnSettlement{}, err
	}
	checkpoint.TerminalSourceApplied = true
	current.PhaseState, _ = json.Marshal(checkpoint)
	current.Revision++
	controller.current = current
	return current, nil
}

type recordingRuntimeSettlementEffects struct {
	calls *[]string
}

func (effects recordingRuntimeSettlementEffects) steps() runtimeSettlementEffects {
	call := func(name string) {
		*effects.calls = append(*effects.calls, name)
	}
	return runtimeSettlementEffects{
		prepareEvidence: func(
			context.Context,
			workerapp.TurnSettlement,
			runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("prepare_evidence")
			return completeRuntimeSettlementCheckpointThrough(
				workerapp.TurnSettlementAccepted,
			), nil
		},
		finalizeEvidence: func(
			_ context.Context,
			checkpoint runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("finalize_evidence")
			checkpoint.Evidence = &runtimeSettlementEvidenceCheckpoint{
				ManifestDigest: "sha256:evidence",
			}
			return checkpoint, nil
		},
		publishEvidence: func(
			_ context.Context,
			_ workerapp.TurnSettlement,
			checkpoint runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("publish_evidence")
			checkpoint.Publication = &runtimeSettlementPublicationCheckpoint{
				TraceStreamID: "trace-turn-1",
				ArtifactIDs:   []string{"report-1", "patch-1"},
			}
			return checkpoint, nil
		},
		planTask: func(
			_ context.Context,
			checkpoint runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("plan_task")
			checkpoint.TaskPlan = &runtimeSettlementTaskPlan{
				Action: "none",
				Reason: "no_task",
			}
			return checkpoint, nil
		},
		settleTask: func(
			_ context.Context,
			checkpoint runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("settle_task")
			checkpoint.TaskResult = &runtimeSettlementTaskResult{
				Status: "not_applicable",
			}
			return checkpoint, nil
		},
		planContinuation: func(
			_ context.Context,
			checkpoint runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("plan_continuation")
			checkpoint.ContinuationPlan = &runtimeSettlementContinuationPlan{
				Action: "none",
				Reason: "no_queued_turn",
			}
			return checkpoint, nil
		},
		settleContinuation: func(
			_ context.Context,
			_ workerapp.TurnSettlement,
			checkpoint runtimeTurnSettlementCheckpoint,
		) (runtimeTurnSettlementCheckpoint, error) {
			call("settle_continuation")
			checkpoint.ContinuationResult = &runtimeSettlementContinuationResult{
				Status: "not_applicable",
			}
			return checkpoint, nil
		},
	}
}

func completeRuntimeSettlementCheckpointThrough(
	phase workerapp.TurnSettlementPhase,
) runtimeTurnSettlementCheckpoint {
	createdAt := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	checkpoint := runtimeTurnSettlementCheckpoint{
		SchemaVersion: 1,
		FrozenArtifacts: &workerapp.FrozenArtifactMetadata{
			ReportArtifactID: "report-1",
			PatchArtifactID:  "patch-1",
			CreatedAt:        createdAt,
		},
		Source: &runtimeSettlementSourceCheckpoint{
			Kind:   "runtime_event",
			Reason: "end_turn",
		},
		EvidenceInput: &runtimeSettlementEvidenceInput{
			TraceStartOffset:  0,
			TraceEndOffset:    10,
			TraceDigest:       "sha256:trace",
			PatchSnapshotFile: "turn-settlement-patch.patch",
			PatchDigest:       "sha256:patch",
			ReportArtifactID:  "report-1",
			PatchArtifactID:   "patch-1",
			ArtifactCreatedAt: createdAt,
		},
		DomainPlan: &runtimeSettlementDomainPlan{
			SessionTarget: "interactive",
			RunTarget:     "completed",
			TurnTarget:    "completed",
		},
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementEvidencePrepared,
	) {
		checkpoint.Evidence = &runtimeSettlementEvidenceCheckpoint{
			ManifestDigest: "sha256:evidence",
		}
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementDomainCommitted,
	) {
		checkpoint.Domain = &runtimeSettlementDomainCheckpoint{
			SessionRevision: 8,
			RunRevision:     4,
			TurnRevision:    3,
			RuntimeRevision: 5,
		}
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementEvidencePublished,
	) {
		checkpoint.Publication = &runtimeSettlementPublicationCheckpoint{
			TraceStreamID: "trace-turn-1",
			ArtifactIDs:   []string{"report-1", "patch-1"},
		}
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementTaskSettled,
	) {
		checkpoint.TaskPlan = &runtimeSettlementTaskPlan{
			Action: "none",
			Reason: "no_task",
		}
		checkpoint.TaskResult = &runtimeSettlementTaskResult{
			Status: "not_applicable",
		}
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementTerminalEventApplied,
	) {
		checkpoint.TerminalSourceApplied = true
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementContinuationSettled,
	) {
		checkpoint.ContinuationPlan = &runtimeSettlementContinuationPlan{
			Action: "none",
			Reason: "no_queued_turn",
		}
		checkpoint.ContinuationResult = &runtimeSettlementContinuationResult{
			Status: "not_applicable",
		}
	}
	return checkpoint
}

func mustRuntimeSettlementCheckpoint(
	t *testing.T,
	checkpoint runtimeTurnSettlementCheckpoint,
) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func countRuntimeSettlementCall(calls []string, target string) int {
	count := 0
	for _, call := range calls {
		if call == target {
			count++
		}
	}
	return count
}

type recordingLocalTurnSettlementSourceStore struct {
	result     workerapp.TurnSettlement
	applied    workerapp.ApplyBackendDeadlineSourceRecord
	applyCalls int
}

func (store *recordingLocalTurnSettlementSourceStore) LoadTurnSettlement(
	context.Context,
	string,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected Turn settlement load")
}

func (store *recordingLocalTurnSettlementSourceStore) ClaimTurnSettlement(
	context.Context,
	workerapp.ClaimTurnSettlementRecord,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected Turn settlement claim")
}

func (store *recordingLocalTurnSettlementSourceStore) ClaimNextPendingTurnSettlement(
	context.Context,
	workerapp.ClaimNextPendingTurnSettlementRecord,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected next Turn settlement claim")
}

func (store *recordingLocalTurnSettlementSourceStore) FreezeTurnSettlementCheckpoint(
	context.Context,
	workerapp.FreezeTurnSettlementCheckpointRecord,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected Turn settlement checkpoint freeze")
}

func (store *recordingLocalTurnSettlementSourceStore) AdvanceTurnSettlement(
	context.Context,
	workerapp.AdvanceTurnSettlementRecord,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected Turn settlement advance")
}

func (store *recordingLocalTurnSettlementSourceStore) ClaimBackendDeadlineTurnSettlement(
	context.Context,
	workerapp.ClaimBackendDeadlineRecord,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected Backend deadline claim")
}

func (store *recordingLocalTurnSettlementSourceStore) AdvanceBackendDeadlineSource(
	context.Context,
	workerapp.AdvanceBackendDeadlineSourceRecord,
) (workerapp.TurnSettlement, error) {
	return workerapp.TurnSettlement{},
		errors.New("unexpected Backend deadline advance")
}

func (store *recordingLocalTurnSettlementSourceStore) ApplyBackendDeadlineTurnSettlement(
	_ context.Context,
	record workerapp.ApplyBackendDeadlineSourceRecord,
) (workerapp.TurnSettlement, error) {
	store.applyCalls++
	store.applied = record
	return store.result, nil
}
