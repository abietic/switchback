package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

func TestClaimBackendDeadlineFreezesCanonicalSourceBeforeStore(t *testing.T) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 123456000, time.UTC)
	now := deadlineAt.Add(2 * time.Second)
	store := &recordingTurnSettlementSourceStore{
		result: TurnSettlement{SettlementID: "turn-settlement/turn-1"},
	}
	service := TurnSettlementSourceService{
		Store: store,
		Now:   func() time.Time { return now },
	}
	result, err := service.ClaimBackendDeadline(
		context.Background(),
		validBackendDeadlineClaim(deadlineAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.SettlementID != "turn-settlement/turn-1" || store.calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, store.calls)
	}
	record := store.record
	if record.SettlementID != "turn-settlement/turn-1" ||
		record.Source.Kind != TurnSettlementSourceBackendDeadline ||
		record.Source.Reason != "agent_turn_deadline" ||
		!record.Source.OccurredAt.Equal(deadlineAt) ||
		record.Source.Ref != "deadline-command-1" ||
		record.Source.Digest == "" ||
		record.Source.StateDigest == "" ||
		!record.ClaimedAt.Equal(now) {
		t.Fatalf("record=%+v", record)
	}
	var state BackendDeadlineSourceState
	if err := json.Unmarshal(record.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != BackendDeadlineSourceStateSchemaVersion ||
		state.Phase != BackendDeadlineSourceClaimed ||
		!state.DeadlineAt.Equal(deadlineAt) ||
		!state.ForceAfter.Equal(deadlineAt.Add(BackendDeadlineForceGrace)) ||
		state.RuntimeInstanceID != "runtime-1" ||
		state.RuntimeGeneration != 1 ||
		state.ChannelEpoch != 1 {
		t.Fatalf("state=%+v", state)
	}
	canonical, digest, err := CanonicalizeTurnSettlementSourceState(
		record.Source.State,
	)
	if err != nil || string(canonical) != string(record.Source.State) ||
		digest != record.Source.StateDigest {
		t.Fatalf(
			"canonical=%s digest=%s want=%s error=%v",
			canonical,
			digest,
			record.Source.StateDigest,
			err,
		)
	}
}

func TestClaimBackendDeadlineRejectsInvalidOrEarlyObservation(t *testing.T) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		now     time.Time
		mutate  func(*ClaimBackendDeadlineCommand)
		wantErr error
	}{
		{
			name:    "before deadline",
			now:     deadlineAt.Add(-time.Microsecond),
			wantErr: ErrTurnSettlementSourceConflict,
		},
		{
			name: "missing tenant",
			now:  deadlineAt,
			mutate: func(command *ClaimBackendDeadlineCommand) {
				command.TenantID = ""
			},
		},
		{
			name: "zero generation",
			now:  deadlineAt,
			mutate: func(command *ClaimBackendDeadlineCommand) {
				command.RuntimeGeneration = 0
			},
			wantErr: ErrTurnSettlementSourceConflict,
		},
		{
			name: "zero epoch",
			now:  deadlineAt,
			mutate: func(command *ClaimBackendDeadlineCommand) {
				command.ChannelEpoch = 0
			},
			wantErr: ErrTurnSettlementSourceConflict,
		},
		{
			name: "non PostgreSQL timestamp",
			now:  deadlineAt,
			mutate: func(command *ClaimBackendDeadlineCommand) {
				command.DeadlineAt = command.DeadlineAt.Add(time.Nanosecond)
			},
			wantErr: ErrTurnSettlementSourceConflict,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := &recordingTurnSettlementSourceStore{}
			command := validBackendDeadlineClaim(deadlineAt)
			if test.mutate != nil {
				test.mutate(&command)
			}
			_, err := (TurnSettlementSourceService{
				Store: store,
				Now:   func() time.Time { return test.now },
			}).ClaimBackendDeadline(context.Background(), command)
			if err == nil {
				t.Fatal("invalid deadline source was accepted")
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
			if store.calls != 0 {
				t.Fatalf("store calls=%d", store.calls)
			}
		})
	}
}

func TestValidateTurnSettlementSourceRejectsRecoveryOrMutableDeadlineProof(
	t *testing.T,
) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	recording := &recordingTurnSettlementSourceStore{}
	_, err := (TurnSettlementSourceService{
		Store: recording,
		Now:   func() time.Time { return deadlineAt },
	}).ClaimBackendDeadline(
		context.Background(),
		validBackendDeadlineClaim(deadlineAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	source := recording.record.Source
	for _, mutate := range []func(*TurnSettlementSource){
		func(source *TurnSettlementSource) {
			source.Kind = TurnSettlementSourceKind("runtime_recovery")
		},
		func(source *TurnSettlementSource) {
			source.Reason = "container_exit"
		},
		func(source *TurnSettlementSource) {
			var state map[string]any
			_ = json.Unmarshal(source.State, &state)
			state["force_after"] = deadlineAt.Add(time.Minute)
			source.State, _ = json.Marshal(state)
			_, source.StateDigest, _ =
				CanonicalizeTurnSettlementSourceState(source.State)
		},
		func(source *TurnSettlementSource) {
			var state map[string]any
			_ = json.Unmarshal(source.State, &state)
			state["runtime_instance_id"] = ""
			source.State, _ = json.Marshal(state)
			source.State, source.StateDigest, _ =
				CanonicalizeTurnSettlementSourceState(source.State)
		},
	} {
		candidate := source
		mutate(&candidate)
		if err := ValidateTurnSettlementSource(candidate); !errors.Is(
			err,
			ErrTurnSettlementSourceConflict,
		) {
			t.Fatalf("mutated source error=%v source=%+v", err, candidate)
		}
	}
}

func TestValidateTurnSettlementSourceRejectsUnknownStateFields(t *testing.T) {
	t.Parallel()

	deadlineAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	record, err := NewBackendDeadlineClaimRecord(
		validBackendDeadlineClaim(deadlineAt),
		deadlineAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(record.Source.State, &state); err != nil {
		t.Fatal(err)
	}
	state["future_unreviewed_authority"] = true
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	record.Source.State, record.Source.StateDigest, err =
		CanonicalizeTurnSettlementSourceState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTurnSettlementSource(record.Source); !errors.Is(
		err,
		ErrTurnSettlementSourceConflict,
	) {
		t.Fatalf("unknown source state field error=%v", err)
	}

	runtimeSource, err := NewRuntimeEventTurnSettlementSource(
		deadlineAt,
		"terminal-message-1",
		digestRuntimeControlBytes([]byte("terminal-message-1")),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeSource.State, runtimeSource.StateDigest, err =
		CanonicalizeTurnSettlementSourceState(
			json.RawMessage(
				`{"future_unreviewed_authority":true,"phase":"terminal_observed","schema_version":1}`,
			),
		)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTurnSettlementSource(runtimeSource); !errors.Is(
		err,
		ErrTurnSettlementSourceConflict,
	) {
		t.Fatalf("unknown Runtime source state field error=%v", err)
	}
}

func TestAdvanceBackendDeadlineQueuesStrictInterruptIntent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 27, 12, 0, 1, 0, time.UTC)
	store := &recordingTurnSettlementSourceStore{
		result: TurnSettlement{SettlementID: "turn-settlement/turn-1"},
	}
	envelope, err := json.Marshal(contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		MessageID:         "deadline-interrupt-turn-1",
		Kind:              "command",
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		AgentSessionID:    "agent-session-1",
		AgentTurnID:       "turn-1",
		ChannelEpoch:      1,
		IdempotencyKey:    "deadline-interrupt-turn-1",
		MessageType:       "agent_turn.interrupt",
		Payload:           json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (TurnSettlementSourceService{
		Store: store,
		Now:   func() time.Time { return now },
	}).AdvanceBackendDeadline(
		context.Background(),
		AdvanceBackendDeadlineSourceCommand{
			SettlementID:            "turn-settlement/turn-1",
			ExpectedRevision:        2,
			ExpectedStateDigest:     testSourceDigest("claimed"),
			LeaseOwnerID:            "settlement-worker-1",
			LeaseFence:              1,
			ToPhase:                 BackendDeadlineSourceInterruptQueued,
			InterruptMessageID:      "deadline-interrupt-turn-1",
			InterruptIdempotencyKey: "deadline-interrupt-turn-1",
			InterruptEnvelope:       envelope,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.SettlementID != "turn-settlement/turn-1" ||
		store.advanceCalls != 1 ||
		store.advanceRecord.AdvancedAt != now ||
		store.advanceRecord.InterruptLogicalEnvelope == nil ||
		store.advanceRecord.InterruptLogicalEnvelope.MessageType !=
			"agent_turn.interrupt" {
		t.Fatalf(
			"result=%+v calls=%d record=%+v",
			result,
			store.advanceCalls,
			store.advanceRecord,
		)
	}

	invalid := envelope[:len(envelope)-1]
	invalid = append(
		append([]byte(nil), invalid...),
		[]byte(`,"future_authority":true}`)...,
	)
	if _, err := (TurnSettlementSourceService{
		Store: store,
		Now:   func() time.Time { return now },
	}).AdvanceBackendDeadline(
		context.Background(),
		AdvanceBackendDeadlineSourceCommand{
			SettlementID:            "turn-settlement/turn-1",
			ExpectedRevision:        2,
			ExpectedStateDigest:     testSourceDigest("claimed"),
			LeaseOwnerID:            "settlement-worker-1",
			LeaseFence:              1,
			ToPhase:                 BackendDeadlineSourceInterruptQueued,
			InterruptMessageID:      "deadline-interrupt-turn-1",
			InterruptIdempotencyKey: "deadline-interrupt-turn-1",
			InterruptEnvelope:       invalid,
		},
	); err == nil {
		t.Fatal("interrupt template with unknown authority was accepted")
	}
	if store.advanceCalls != 1 {
		t.Fatalf("invalid intent reached store calls=%d", store.advanceCalls)
	}
}

func TestApplyBackendDeadlineSourceBuildsStrictAtomicRecord(t *testing.T) {
	t.Parallel()

	appliedAt := time.Date(2026, 7, 27, 12, 1, 0, 0, time.UTC)
	store := &recordingTurnSettlementSourceStore{
		result: TurnSettlement{
			SettlementID: "turn-settlement/turn-1",
			Phase:        TurnSettlementTerminalEventApplied,
		},
	}
	command := ApplyBackendDeadlineSourceCommand{
		SettlementID:              "turn-settlement/turn-1",
		ExpectedRevision:          8,
		ExpectedSourceDigest:      testSourceDigest("backend-deadline-source"),
		ExpectedSourceStateDigest: testSourceDigest("cleanup-confirmed"),
		LeaseOwnerID:              "settlement-worker-1",
		LeaseFence:                3,
	}
	result, err := (TurnSettlementSourceService{
		Store: store,
		Now:   func() time.Time { return appliedAt },
	}).ApplyBackendDeadline(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != TurnSettlementTerminalEventApplied ||
		store.applyCalls != 1 ||
		store.applyRecord.Command != command ||
		!store.applyRecord.AppliedAt.Equal(appliedAt) {
		t.Fatalf(
			"result=%+v calls=%d record=%+v",
			result,
			store.applyCalls,
			store.applyRecord,
		)
	}
}

func TestApplyBackendDeadlineSourceRejectsIncompleteAuthorityBeforeStore(
	t *testing.T,
) {
	t.Parallel()

	appliedAt := time.Date(2026, 7, 27, 12, 1, 0, 0, time.UTC)
	valid := ApplyBackendDeadlineSourceCommand{
		SettlementID:              "turn-settlement/turn-1",
		ExpectedRevision:          8,
		ExpectedSourceDigest:      testSourceDigest("backend-deadline-source"),
		ExpectedSourceStateDigest: testSourceDigest("cleanup-confirmed"),
		LeaseOwnerID:              "settlement-worker-1",
		LeaseFence:                3,
	}
	tests := map[string]func(*ApplyBackendDeadlineSourceCommand){
		"settlement id": func(command *ApplyBackendDeadlineSourceCommand) {
			command.SettlementID = ""
		},
		"revision": func(command *ApplyBackendDeadlineSourceCommand) {
			command.ExpectedRevision = 0
		},
		"source digest": func(command *ApplyBackendDeadlineSourceCommand) {
			command.ExpectedSourceDigest = ""
		},
		"source state digest": func(command *ApplyBackendDeadlineSourceCommand) {
			command.ExpectedSourceStateDigest = "not-a-digest"
		},
		"lease owner": func(command *ApplyBackendDeadlineSourceCommand) {
			command.LeaseOwnerID = ""
		},
		"lease fence": func(command *ApplyBackendDeadlineSourceCommand) {
			command.LeaseFence = 0
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &recordingTurnSettlementSourceStore{}
			command := valid
			mutate(&command)
			_, err := (TurnSettlementSourceService{
				Store: store,
				Now:   func() time.Time { return appliedAt },
			}).ApplyBackendDeadline(context.Background(), command)
			if err == nil {
				t.Fatal("incomplete Backend deadline apply authority was accepted")
			}
			if store.applyCalls != 0 {
				t.Fatalf("store calls=%d", store.applyCalls)
			}
		})
	}
}

func validBackendDeadlineClaim(deadlineAt time.Time) ClaimBackendDeadlineCommand {
	return ClaimBackendDeadlineCommand{
		TenantID:          "tenant-local",
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		AgentSessionID:    "agent-session-1",
		AgentTurnID:       "turn-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
		ChannelEpoch:      1,
		DeadlineAt:        deadlineAt,
		SourceRef:         "deadline-command-1",
	}
}

type recordingTurnSettlementSourceStore struct {
	calls         int
	record        ClaimBackendDeadlineRecord
	advanceCalls  int
	advanceRecord AdvanceBackendDeadlineSourceRecord
	applyCalls    int
	applyRecord   ApplyBackendDeadlineSourceRecord
	result        TurnSettlement
	err           error
}

func (store *recordingTurnSettlementSourceStore) ClaimBackendDeadlineTurnSettlement(
	_ context.Context,
	record ClaimBackendDeadlineRecord,
) (TurnSettlement, error) {
	store.calls++
	store.record = record
	return store.result, store.err
}

func (store *recordingTurnSettlementSourceStore) AdvanceBackendDeadlineSource(
	_ context.Context,
	record AdvanceBackendDeadlineSourceRecord,
) (TurnSettlement, error) {
	store.advanceCalls++
	store.advanceRecord = record
	return store.result, store.err
}

func (store *recordingTurnSettlementSourceStore) ApplyBackendDeadlineTurnSettlement(
	_ context.Context,
	record ApplyBackendDeadlineSourceRecord,
) (TurnSettlement, error) {
	store.applyCalls++
	store.applyRecord = record
	return store.result, store.err
}
