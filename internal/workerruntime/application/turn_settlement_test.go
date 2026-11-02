package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestTurnSettlementServiceAllowsOnlyDurablePhaseOwnerTransitions(t *testing.T) {
	now := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	store := &recordingTurnSettlementStore{
		state: workerapp.TurnSettlement{
			SettlementID: "turn-settlement/turn-1",
			AgentTurnID:  "turn-1",
			Phase:        workerapp.TurnSettlementAccepted,
			Revision:     1,
		},
	}
	service := workerapp.TurnSettlementService{
		Store: store,
		Now:   func() time.Time { return now },
	}

	claimed, err := service.Claim(context.Background(), workerapp.ClaimTurnSettlementCommand{
		SettlementID:     store.state.SettlementID,
		ExpectedRevision: 1,
		OwnerID:          "settlement-worker-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.LeaseOwnerID != "settlement-worker-1" ||
		claimed.LeaseFence != 1 ||
		!claimed.LeaseExpiresAt.Equal(now.Add(workerapp.TurnSettlementLeaseDuration)) {
		t.Fatalf("claimed settlement=%+v", claimed)
	}
	frozen, err := service.FreezeCheckpoint(
		context.Background(),
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID: claimed.SettlementID, ExpectedRevision: claimed.Revision,
			LeaseOwnerID: claimed.LeaseOwnerID, LeaseFence: claimed.LeaseFence,
			Checkpoint: workerapp.TurnSettlementCheckpoint{
				SchemaVersion: workerapp.TurnSettlementCheckpointSchemaVersion,
				FrozenArtifacts: &workerapp.FrozenArtifactMetadata{
					ReportArtifactID: "artifact-report-1",
					PatchArtifactID:  "artifact-patch-1",
					CreatedAt:        now,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	advanced, err := service.Advance(context.Background(), workerapp.AdvanceTurnSettlementCommand{
		SettlementID:     frozen.SettlementID,
		ExpectedRevision: frozen.Revision,
		LeaseOwnerID:     frozen.LeaseOwnerID,
		LeaseFence:       frozen.LeaseFence,
		FromPhase:        workerapp.TurnSettlementAccepted,
		ToPhase:          workerapp.TurnSettlementEvidencePrepared,
		Checkpoint: validTurnSettlementCheckpointForPhase(
			t,
			workerapp.TurnSettlementEvidencePrepared,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Phase != workerapp.TurnSettlementEvidencePrepared ||
		advanced.PhaseStateDigest == "" {
		t.Fatalf("advanced settlement=%+v", advanced)
	}

	for name, command := range map[string]workerapp.AdvanceTurnSettlementCommand{
		"skip phase": {
			SettlementID: advanced.SettlementID, ExpectedRevision: advanced.Revision,
			LeaseOwnerID: advanced.LeaseOwnerID, LeaseFence: advanced.LeaseFence,
			FromPhase:  workerapp.TurnSettlementEvidencePrepared,
			ToPhase:    workerapp.TurnSettlementEvidencePublished,
			Checkpoint: json.RawMessage(`{}`),
		},
		"apply outside journal": {
			SettlementID: advanced.SettlementID, ExpectedRevision: advanced.Revision,
			LeaseOwnerID: advanced.LeaseOwnerID, LeaseFence: advanced.LeaseFence,
			FromPhase:  workerapp.TurnSettlementTaskSettled,
			ToPhase:    workerapp.TurnSettlementTerminalEventApplied,
			Checkpoint: json.RawMessage(`{}`),
		},
		"domain outside atomic store": {
			SettlementID: advanced.SettlementID, ExpectedRevision: advanced.Revision,
			LeaseOwnerID: advanced.LeaseOwnerID, LeaseFence: advanced.LeaseFence,
			FromPhase:  workerapp.TurnSettlementEvidencePrepared,
			ToPhase:    workerapp.TurnSettlementDomainCommitted,
			Checkpoint: advanced.PhaseState,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Advance(context.Background(), command); !errors.Is(
				err,
				workerapp.ErrTurnSettlementPhaseConflict,
			) {
				t.Fatalf("Advance error=%v", err)
			}
		})
	}
}

func TestCanonicalizeTurnSettlementCheckpointNormalizesWhitespaceAndKeyOrder(t *testing.T) {
	first, firstDigest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(
		json.RawMessage(` { "b": 2, "a": { "z": true, "x": "value" } } `),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, secondDigest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(
		json.RawMessage(`{"a":{"x":"value","z":true},"b":2}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := json.RawMessage(`{"a":{"x":"value","z":true},"b":2}`)
	if !bytes.Equal(first, want) || !bytes.Equal(second, want) ||
		firstDigest != secondDigest {
		t.Fatalf(
			"canonical checkpoints first=%s/%s second=%s/%s",
			first,
			firstDigest,
			second,
			secondDigest,
		)
	}
}

func TestTurnSettlementServiceClaimsNextAndFreezesTypedCheckpointAtPostgresPrecision(t *testing.T) {
	now := time.Date(2026, 7, 26, 22, 0, 0, 123456789, time.FixedZone("test", 8*60*60))
	store := &recordingTurnSettlementStore{
		state: workerapp.TurnSettlement{
			SettlementID: "turn-settlement/turn-next",
			AgentTurnID:  "turn-next",
			Phase:        workerapp.TurnSettlementAccepted,
			PhaseState:   json.RawMessage(`{}`),
			Revision:     1,
			LeaseOwnerID: "settlement-owner",
			LeaseFence:   1,
			LeaseExpiresAt: now.UTC().Truncate(time.Microsecond).
				Add(workerapp.TurnSettlementLeaseDuration),
		},
	}
	service := workerapp.TurnSettlementService{
		Store: store,
		Now:   func() time.Time { return now },
	}
	claimed, err := service.ClaimNextPending(
		context.Background(),
		workerapp.ClaimNextPendingTurnSettlementCommand{
			OwnerID: "settlement-owner",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantNow := now.UTC().Truncate(time.Microsecond)
	if !store.claimNext.ClaimedAt.Equal(wantNow) ||
		!store.claimNext.LeaseExpiresAt.Equal(
			wantNow.Add(workerapp.TurnSettlementLeaseDuration),
		) ||
		!claimed.UpdatedAt.Equal(wantNow) {
		t.Fatalf("claim record=%+v settlement=%+v", store.claimNext, claimed)
	}

	frozen, err := service.FreezeCheckpoint(
		context.Background(),
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     claimed.SettlementID,
			ExpectedRevision: claimed.Revision,
			LeaseOwnerID:     claimed.LeaseOwnerID,
			LeaseFence:       claimed.LeaseFence,
			Checkpoint: workerapp.TurnSettlementCheckpoint{
				SchemaVersion: workerapp.TurnSettlementCheckpointSchemaVersion,
				FrozenArtifacts: &workerapp.FrozenArtifactMetadata{
					ReportArtifactID: "artifact-report-1",
					PatchArtifactID:  "artifact-patch-1",
					CreatedAt:        now,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint struct {
		SchemaVersion   uint32                           `json:"schema_version"`
		FrozenArtifacts workerapp.FrozenArtifactMetadata `json:"frozen_artifacts"`
	}
	if err := json.Unmarshal(store.freeze.CanonicalCheckpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.SchemaVersion != workerapp.TurnSettlementCheckpointSchemaVersion ||
		!checkpoint.FrozenArtifacts.CreatedAt.Equal(wantNow) ||
		!store.freeze.FrozenAt.Equal(wantNow) ||
		frozen.PhaseStateDigest == "" {
		t.Fatalf(
			"checkpoint=%+v record=%+v settlement=%+v",
			checkpoint,
			store.freeze,
			frozen,
		)
	}
}

func TestTurnSettlementServiceRejectsUnknownCheckpointSchema(t *testing.T) {
	service := workerapp.TurnSettlementService{
		Store: &recordingTurnSettlementStore{},
		Now:   time.Now,
	}
	_, err := service.FreezeCheckpoint(
		context.Background(),
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:     "turn-settlement/turn-1",
			ExpectedRevision: 1,
			LeaseOwnerID:     "settlement-owner",
			LeaseFence:       1,
			Checkpoint: workerapp.TurnSettlementCheckpoint{
				SchemaVersion: 2,
				FrozenArtifacts: &workerapp.FrozenArtifactMetadata{
					ReportArtifactID: "artifact-report-1",
					PatchArtifactID:  "artifact-patch-1",
					CreatedAt:        time.Now(),
				},
			},
		},
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("unknown checkpoint schema error=%v", err)
	}
}

func TestTurnSettlementAdvanceRejectsWeakPhaseCheckpointBeforePersistence(t *testing.T) {
	store := &recordingTurnSettlementStore{}
	service := workerapp.TurnSettlementService{
		Store: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC)
		},
	}

	_, err := service.Advance(
		context.Background(),
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     "turn-settlement/turn-weak",
			ExpectedRevision: 2,
			LeaseOwnerID:     "settlement-owner",
			LeaseFence:       1,
			FromPhase:        workerapp.TurnSettlementAccepted,
			ToPhase:          workerapp.TurnSettlementEvidencePrepared,
			Checkpoint:       json.RawMessage(`{"schema_version":1}`),
		},
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("weak phase checkpoint error=%v", err)
	}
	if store.advanceCalls != 0 {
		t.Fatalf("weak phase checkpoint reached persistence calls=%d", store.advanceCalls)
	}
}

func TestTurnSettlementCheckpointProofRequiresStoredDomainPostRevisions(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementDomainCommitted,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "domain")
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementDomainCommitted,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("missing domain post revisions error=%v", err)
	}
}

func TestRuntimeEventSettlementProofAllowsDispatchAndRuntimeControlRevisionAdvance(
	t *testing.T,
) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementDomainCommitted,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	fields["domain"].(map[string]any)["turn_revision"] = float64(7)
	fields["domain"].(map[string]any)["runtime_revision"] = float64(9)
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementDomainCommitted,
		checkpoint,
	); err != nil {
		t.Fatalf("Runtime-control revision advance proof error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRejectsRevisionOverflow(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementDomainCommitted,
	)
	checkpoint = bytes.Replace(
		checkpoint,
		[]byte(`"expected_session_revision":3`),
		[]byte(`"expected_session_revision":18446744073709551615`),
		1,
	)
	checkpoint = bytes.Replace(
		checkpoint,
		[]byte(`"session_revision":4`),
		[]byte(`"session_revision":0`),
		1,
	)

	err := workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementDomainCommitted,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("overflowed domain revision error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRejectsEmptyFinalizedEvidence(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementEvidencePrepared,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	evidence := fields["evidence"].(map[string]any)
	evidence["finalized"] = map[string]any{}
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementEvidencePrepared,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("empty finalized evidence error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofBindsDomainPlanToFrozenSource(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementEvidencePrepared,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	domainPlan := fields["domain_plan"].(map[string]any)
	domainPlan["terminal_reason"] = "different_reason"
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementEvidencePrepared,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("source/domain-plan mismatch error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRequiresExactEvidencePublication(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementEvidencePublished,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	fields["publication"] = map[string]any{
		"trace_stream_id": "trace-turn-1",
		"artifact_ids":    []string{"artifact-report-1"},
	}
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementEvidencePublished,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("incomplete publication proof error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofBindsPublicationToFinalizedTrace(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementEvidencePublished,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	publication := fields["publication"].(map[string]any)
	publication["trace_stream_id"] = "different-trace"
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementEvidencePublished,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("publication Trace mismatch error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRejectsNoOpTaskWithEffectAuthority(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementTaskSettled,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	fields["task_plan"] = map[string]any{
		"action":                 "none",
		"reason":                 "no_task",
		"task_id":                "task-1",
		"expected_task_revision": 7,
		"idempotency_key":        "deliver-turn/turn-1",
	}
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementTaskSettled,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("no-op Task authority error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRequiresTerminalSourceApplied(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementTerminalEventApplied,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "terminal_source_applied")
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementTerminalEventApplied,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("missing terminal source proof error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRejectsNoOpContinuationWithEffectAuthority(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementContinuationSettled,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	fields["continuation_plan"] = map[string]any{
		"action":                    "none",
		"reason":                    "no_queued_turn",
		"queue_command_id":          "queue-command-1",
		"expected_session_revision": 4,
		"expected_run_revision":     5,
		"expected_turn_revision":    6,
		"expected_runtime_revision": 6,
	}
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementContinuationSettled,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("no-op continuation authority error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofAcceptsExactProofForEveryPhase(t *testing.T) {
	for _, phase := range []workerapp.TurnSettlementPhase{
		workerapp.TurnSettlementAccepted,
		workerapp.TurnSettlementEvidencePrepared,
		workerapp.TurnSettlementDomainCommitted,
		workerapp.TurnSettlementEvidencePublished,
		workerapp.TurnSettlementTaskSettled,
		workerapp.TurnSettlementTerminalEventApplied,
		workerapp.TurnSettlementContinuationSettled,
		workerapp.TurnSettlementCompleted,
	} {
		t.Run(string(phase), func(t *testing.T) {
			if err := workerapp.ValidateTurnSettlementCheckpointProof(
				phase,
				validTurnSettlementCheckpointForPhase(t, phase),
			); err != nil {
				t.Fatalf("valid %s proof error=%v", phase, err)
			}
		})
	}
}

func TestTurnSettlementCheckpointProofValidatesEffectfulTaskActions(t *testing.T) {
	tests := []struct {
		name         string
		action       string
		sourceReason string
		timedOut     bool
		reason       string
	}{
		{
			name:         "record stage result",
			action:       "record_stage_result",
			sourceReason: "end_turn",
		},
		{
			name:         "settle cancelled",
			action:       "settle_cancelled",
			sourceReason: "end_turn",
		},
		{
			name:         "pause agent error",
			action:       "pause",
			sourceReason: "agent_error",
			reason:       "agent_error",
		},
		{
			name:         "pause deadline",
			action:       "pause",
			sourceReason: "agent_turn_deadline",
			timedOut:     true,
			reason:       "agent_turn_deadline",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkpoint := validTurnSettlementCheckpointForPhase(
				t,
				workerapp.TurnSettlementTaskSettled,
			)
			var fields map[string]any
			if err := json.Unmarshal(checkpoint, &fields); err != nil {
				t.Fatal(err)
			}
			source := fields["source"].(map[string]any)
			source["reason"] = test.sourceReason
			source["timed_out"] = test.timedOut
			if test.timedOut {
				source["kind"] = "backend_deadline"
			}
			domainPlan := fields["domain_plan"].(map[string]any)
			domainPlan["terminal_reason"] = test.sourceReason
			input := fields["evidence_input"].(map[string]any)
			input["task_id"] = "task-1"
			evidence := fields["evidence"].(map[string]any)
			finalized := evidence["finalized"].(map[string]any)
			for _, rawArtifact := range finalized["ArtifactRefs"].([]any) {
				rawArtifact.(map[string]any)["task_id"] = "task-1"
			}
			fields["task_plan"] = map[string]any{
				"action":                 test.action,
				"task_id":                "task-1",
				"expected_task_revision": 7,
				"idempotency_key":        "settle-turn/turn-1",
				"reason":                 test.reason,
			}
			fields["task_result"] = map[string]any{
				"status":   "applied",
				"revision": 8,
				"event_id": "event-task-1",
			}
			checkpoint, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := workerapp.ValidateTurnSettlementCheckpointProof(
				workerapp.TurnSettlementTaskSettled,
				checkpoint,
			); err != nil {
				t.Fatalf("valid Task action proof error=%v", err)
			}
		})
	}
}

func TestTurnSettlementCheckpointProofRejectsInconsistentTerminalSourceAuthority(
	t *testing.T,
) {
	tests := []struct {
		name     string
		kind     string
		reason   string
		timedOut bool
	}{
		{
			name:     "runtime event claims deadline",
			kind:     "runtime_event",
			reason:   "agent_turn_deadline",
			timedOut: true,
		},
		{
			name:     "backend deadline lacks timeout",
			kind:     "backend_deadline",
			reason:   "end_turn",
			timedOut: false,
		},
		{
			name:     "runtime recovery cannot settle normal terminal",
			kind:     "runtime_recovery",
			reason:   "acp_eof",
			timedOut: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checkpoint := validTurnSettlementCheckpointForPhase(
				t,
				workerapp.TurnSettlementAccepted,
			)
			var fields map[string]any
			if err := json.Unmarshal(checkpoint, &fields); err != nil {
				t.Fatal(err)
			}
			source := fields["source"].(map[string]any)
			source["kind"] = test.kind
			source["reason"] = test.reason
			source["timed_out"] = test.timedOut
			fields["domain_plan"].(map[string]any)["terminal_reason"] =
				test.reason
			checkpoint, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := workerapp.ValidateTurnSettlementCheckpointProof(
				workerapp.TurnSettlementAccepted,
				checkpoint,
			); !errors.Is(
				err,
				workerapp.ErrTurnSettlementCheckpointConflict,
			) {
				t.Fatalf("inconsistent source proof error=%v", err)
			}
		})
	}
}

func TestTurnSettlementCheckpointProofValidatesCreatedContinuationAuthority(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementContinuationSettled,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	fields["continuation_plan"] = map[string]any{
		"action":                    "create_queued_turn",
		"queue_command_id":          "queue-command-1",
		"expected_session_revision": 4,
		"expected_run_revision":     5,
		"expected_turn_revision":    6,
		"expected_runtime_revision": 6,
	}
	fields["continuation_result"] = map[string]any{
		"status":        "created",
		"worker_run_id": "run-2",
		"agent_turn_id": "turn-2",
	}
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementContinuationSettled,
		checkpoint,
	); err != nil {
		t.Fatalf("valid created continuation proof error=%v", err)
	}

	fields["continuation_plan"].(map[string]any)["expected_runtime_revision"] = 7
	checkpoint, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementContinuationSettled,
		checkpoint,
	); !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("stale continuation authority error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRejectsUnknownTopLevelField(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementEvidencePrepared,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	fields["weak_marker"] = map[string]any{"status": "pretend"}
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementEvidencePrepared,
		checkpoint,
	); !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("unknown proof field error=%v", err)
	}
}

func TestTurnSettlementCheckpointProofRequiresStableFrozenTimestamp(t *testing.T) {
	checkpoint := validTurnSettlementCheckpointForPhase(
		t,
		workerapp.TurnSettlementEvidencePrepared,
	)
	var fields map[string]any
	if err := json.Unmarshal(checkpoint, &fields); err != nil {
		t.Fatal(err)
	}
	frozen := fields["frozen_artifacts"].(map[string]any)
	frozen["created_at"] = "2026-07-27T01:00:00.123456789Z"
	input := fields["evidence_input"].(map[string]any)
	input["artifact_created_at"] = "2026-07-27T01:00:00.123456789Z"
	checkpoint, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}

	err = workerapp.ValidateTurnSettlementCheckpointProof(
		workerapp.TurnSettlementEvidencePrepared,
		checkpoint,
	)
	if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
		t.Fatalf("unstable frozen timestamp error=%v", err)
	}
}

func TestTurnSettlementCheckpointExtensionPreservesCumulativeTopLevelState(t *testing.T) {
	current := json.RawMessage(
		`{"schema_version":1,"frozen_artifacts":{"report_artifact_id":"report-1"},"evidence":{"digest":"sha256:a"}}`,
	)
	valid := json.RawMessage(
		`{"task":{"action":"deliver"},"evidence":{"digest":"sha256:a"},"schema_version":1,"frozen_artifacts":{"report_artifact_id":"report-1"}}`,
	)
	if err := workerapp.ValidateTurnSettlementCheckpointExtension(current, valid); err != nil {
		t.Fatalf("valid cumulative checkpoint error=%v", err)
	}
	for name, checkpoint := range map[string]json.RawMessage{
		"removed field": json.RawMessage(
			`{"schema_version":1,"frozen_artifacts":{"report_artifact_id":"report-1"}}`,
		),
		"changed field": json.RawMessage(
			`{"schema_version":1,"frozen_artifacts":{"report_artifact_id":"report-2"},"evidence":{"digest":"sha256:a"}}`,
		),
		"changed schema": json.RawMessage(
			`{"schema_version":2,"frozen_artifacts":{"report_artifact_id":"report-1"},"evidence":{"digest":"sha256:a"}}`,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if err := workerapp.ValidateTurnSettlementCheckpointExtension(
				current,
				checkpoint,
			); !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
				t.Fatalf("checkpoint extension error=%v", err)
			}
		})
	}
}

func validTurnSettlementCheckpointForPhase(
	t *testing.T,
	phase workerapp.TurnSettlementPhase,
) json.RawMessage {
	t.Helper()
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recordedAt := "2026-07-27T01:00:00Z"
	checkpoint := map[string]any{
		"schema_version": 1,
		"frozen_artifacts": map[string]any{
			"report_artifact_id": "artifact-report-1",
			"patch_artifact_id":  "artifact-patch-1",
			"created_at":         recordedAt,
		},
		"source": map[string]any{
			"kind":        "runtime_event",
			"reason":      "end_turn",
			"occurred_at": recordedAt,
		},
		"evidence_input": map[string]any{
			"tenant_id":                  "tenant-local",
			"user_id":                    "user-local",
			"workspace_id":               "workspace-local",
			"worker_session_id":          "session-1",
			"worker_run_id":              "run-1",
			"agent_session_id":           "agent-session-1",
			"agent_turn_id":              "turn-1",
			"runtime_instance_id":        "runtime-1",
			"runtime_generation":         1,
			"network_policy_id":          "trusted_full_network_v1",
			"network_policy_revision":    1,
			"network_policy_digest":      digest,
			"network_enforcement_status": "enforced",
			"trace_start_offset":         0,
			"trace_end_offset":           10,
			"trace_digest":               digest,
			"patch_snapshot_file":        "turn-settlement-patch.patch",
			"patch_size":                 0,
			"no_files_changed":           true,
			"patch_digest":               digest,
			"server_recorded_at":         recordedAt,
			"worker_recorded_at":         recordedAt,
			"report_artifact_id":         "artifact-report-1",
			"patch_artifact_id":          "artifact-patch-1",
			"artifact_created_at":        recordedAt,
			"source_checkpoint_id":       "working-copy-start-run-1",
			"source_checkpoint_digest":   digest,
		},
		"domain_plan": map[string]any{
			"session_target":            "interactive",
			"run_target":                "completed",
			"turn_target":               "completed",
			"terminal_reason":           "end_turn",
			"expected_session_revision": 3,
			"expected_run_revision":     4,
			"expected_turn_revision":    5,
			"expected_runtime_revision": 6,
		},
		"evidence": map[string]any{
			"manifest_digest": digest,
			"finalized":       validFinalizedTurnEvidenceProof(recordedAt, digest),
		},
		"domain": map[string]any{
			"session_revision": 4,
			"run_revision":     5,
			"turn_revision":    6,
			"runtime_revision": 6,
		},
		"publication": map[string]any{
			"trace_stream_id": "trace-turn-1",
			"artifact_ids": []string{
				"artifact-report-1",
				"artifact-patch-1",
			},
		},
		"task_plan": map[string]any{
			"action": "none",
			"reason": "no_task",
		},
		"task_result": map[string]any{
			"status": "not_applicable",
		},
		"terminal_source_applied": true,
		"continuation_plan": map[string]any{
			"action": "none",
			"reason": "no_queued_turn",
		},
		"continuation_result": map[string]any{
			"status": "not_applicable",
		},
	}
	switch phase {
	case workerapp.TurnSettlementAccepted:
		delete(checkpoint, "evidence")
		fallthrough
	case workerapp.TurnSettlementEvidencePrepared:
		delete(checkpoint, "domain")
		fallthrough
	case workerapp.TurnSettlementDomainCommitted:
		delete(checkpoint, "publication")
		fallthrough
	case workerapp.TurnSettlementEvidencePublished:
		delete(checkpoint, "task_plan")
		delete(checkpoint, "task_result")
		fallthrough
	case workerapp.TurnSettlementTaskSettled:
		delete(checkpoint, "terminal_source_applied")
		fallthrough
	case workerapp.TurnSettlementTerminalEventApplied:
		delete(checkpoint, "continuation_plan")
		delete(checkpoint, "continuation_result")
	case workerapp.TurnSettlementContinuationSettled,
		workerapp.TurnSettlementCompleted:
	default:
		t.Fatalf("unsupported fixture phase %q", phase)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func validFinalizedTurnEvidenceProof(
	recordedAt string,
	digest string,
) map[string]any {
	sha256 := digest[len("sha256:"):]
	stored := func(key string, size int64) map[string]any {
		return map[string]any{
			"Key":      key,
			"Size":     size,
			"Checksum": digest,
		}
	}
	artifact := func(
		artifactID string,
		artifactType string,
		key string,
		size int64,
	) map[string]any {
		return map[string]any{
			"schema_version":      "artifact.v1",
			"artifact_id":         artifactID,
			"tenant_id":           "tenant-local",
			"user_id":             "user-local",
			"workspace_id":        "workspace-local",
			"worker_session_id":   "session-1",
			"worker_run_id":       "run-1",
			"agent_turn_id":       "turn-1",
			"runtime_instance_id": "runtime-1",
			"runtime_generation":  1,
			"type":                artifactType,
			"uri":                 "object://" + key,
			"sha256":              sha256,
			"size_bytes":          size,
			"permission": map[string]any{
				"visibility":      "workspace",
				"policy_revision": "artifact-policy-v1",
			},
			"retention_policy_ref": "retention-v1",
			"storage_tier":         "hot",
			"availability":         "online",
			"completeness":         "complete",
			"completeness_reason":  "",
			"source_checkpoint_ref": map[string]any{
				"checkpoint_id": "working-copy-start-run-1",
				"digest":        digest,
			},
			"available_actions": []string{"preview", "download"},
			"created_at":        recordedAt,
		}
	}
	return map[string]any{
		"Manifest": map[string]any{
			"schema_version":      "trace.manifest.v1",
			"trace_stream_id":     "trace-turn-1",
			"tenant_id":           "tenant-local",
			"user_id":             "user-local",
			"workspace_id":        "workspace-local",
			"worker_session_id":   "session-1",
			"worker_run_id":       "run-1",
			"agent_turn_id":       "turn-1",
			"runtime_instance_id": "runtime-1",
			"runtime_generation":  1,
			"segments": []map[string]any{
				{
					"kind":           "trace-raw-acp",
					"uri":            "object://objects/raw",
					"first_sequence": 1,
					"last_sequence":  1,
					"sha256":         sha256,
					"size_bytes":     10,
					"record_count":   1,
				},
				{
					"kind":           "trace-normalized",
					"uri":            "object://objects/normalized",
					"first_sequence": 1,
					"last_sequence":  1,
					"sha256":         sha256,
					"size_bytes":     12,
					"record_count":   1,
				},
			},
			"first_recorded_at":    recordedAt,
			"last_recorded_at":     recordedAt,
			"schema_versions":      []string{"trace.record.v1"},
			"lifecycle_policy_ref": "trace-policy-v1",
			"storage_tier":         "hot",
			"availability":         "online",
			"analytics_cursor":     "1",
			"langfuse_export": map[string]any{
				"status":   "pending",
				"trace_id": "langfuse-turn-1",
			},
		},
		"ManifestObject": stored("objects/manifest", 20),
		"ArtifactRefs": []map[string]any{
			artifact(
				"artifact-report-1",
				"report",
				"objects/report",
				11,
			),
			artifact(
				"artifact-patch-1",
				"patch",
				"objects/patch",
				0,
			),
		},
		"Objects": []map[string]any{
			{"Kind": "trace-raw-acp", "Stored": stored("objects/raw", 10)},
			{
				"Kind":   "trace-normalized",
				"Stored": stored("objects/normalized", 12),
			},
			{"Kind": "report", "Stored": stored("objects/report", 11)},
			{"Kind": "patch", "Stored": stored("objects/patch", 0)},
		},
	}
}

type recordingTurnSettlementStore struct {
	state        workerapp.TurnSettlement
	claimNext    workerapp.ClaimNextPendingTurnSettlementRecord
	freeze       workerapp.FreezeTurnSettlementCheckpointRecord
	advanceCalls int
}

func (store *recordingTurnSettlementStore) LoadTurnSettlement(
	context.Context,
	string,
) (workerapp.TurnSettlement, error) {
	return store.state, nil
}

func (store *recordingTurnSettlementStore) ClaimTurnSettlement(
	_ context.Context,
	record workerapp.ClaimTurnSettlementRecord,
) (workerapp.TurnSettlement, error) {
	store.state.LeaseOwnerID = record.Command.OwnerID
	store.state.LeaseFence++
	store.state.LeaseExpiresAt = record.LeaseExpiresAt
	store.state.Revision++
	return store.state, nil
}

func (store *recordingTurnSettlementStore) ClaimNextPendingTurnSettlement(
	_ context.Context,
	record workerapp.ClaimNextPendingTurnSettlementRecord,
) (workerapp.TurnSettlement, error) {
	store.claimNext = record
	store.state.LeaseOwnerID = record.Command.OwnerID
	store.state.LeaseFence++
	store.state.LeaseExpiresAt = record.LeaseExpiresAt
	store.state.UpdatedAt = record.ClaimedAt
	store.state.Revision++
	return store.state, nil
}

func (store *recordingTurnSettlementStore) FreezeTurnSettlementCheckpoint(
	_ context.Context,
	record workerapp.FreezeTurnSettlementCheckpointRecord,
) (workerapp.TurnSettlement, error) {
	store.freeze = record
	store.state.PhaseState = append([]byte(nil), record.CanonicalCheckpoint...)
	store.state.PhaseStateDigest = record.CheckpointDigest
	store.state.UpdatedAt = record.FrozenAt
	store.state.Revision++
	return store.state, nil
}

func (store *recordingTurnSettlementStore) AdvanceTurnSettlement(
	_ context.Context,
	record workerapp.AdvanceTurnSettlementRecord,
) (workerapp.TurnSettlement, error) {
	store.advanceCalls++
	store.state.Phase = record.Command.ToPhase
	store.state.PhaseState = append([]byte(nil), record.Command.Checkpoint...)
	store.state.PhaseStateDigest = record.CheckpointDigest
	store.state.Revision++
	return store.state, nil
}
