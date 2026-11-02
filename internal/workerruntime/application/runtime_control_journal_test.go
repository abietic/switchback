package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeControlJournalBindsCredentialDigestWithoutPersistingBearer(t *testing.T) {
	now := time.Date(
		2026,
		7,
		26,
		19,
		0,
		0,
		987654321,
		time.FixedZone("test", 8*60*60),
	)
	wantNow := now.UTC().Truncate(time.Microsecond)
	store := &recordingRuntimeControlJournal{}
	service := application.RuntimeControlJournalService{
		Store: store,
		Now:   func() time.Time { return now },
	}

	state, err := service.Bind(context.Background(), application.BindRuntimeControlChannelCommand{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 2,
		ChannelEpoch:      9,
		Credential:        "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.bindCalls != 1 {
		t.Fatalf("bind calls=%d", store.bindCalls)
	}
	if store.bindRecord.CredentialDigest != "sha256:2bb80d537b1da3e38bd30361aa855686bde0eacd7162fef6a25fe97bf527a25b" {
		t.Fatalf("credential digest=%q", store.bindRecord.CredentialDigest)
	}
	if store.bindRecord.Command.Credential != "" {
		t.Fatalf("bearer credential reached persistence record")
	}
	if store.bindRecord.BoundAt != wantNow || state.RuntimeInstanceID != "runtime-1" {
		t.Fatalf("record=%+v state=%+v", store.bindRecord, state)
	}
}

func TestRuntimeControlJournalRejectsInvalidIdentityBeforePersistence(t *testing.T) {
	store := &recordingRuntimeControlJournal{}
	_, err := (application.RuntimeControlJournalService{
		Store: store,
		Now:   time.Now,
	}).Bind(context.Background(), application.BindRuntimeControlChannelCommand{
		WorkerSessionID:   "session-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 0,
		ChannelEpoch:      1,
		Credential:        "secret",
	})
	if err == nil || store.bindCalls != 0 {
		t.Fatalf("err=%v bind calls=%d", err, store.bindCalls)
	}
}

func TestRuntimeControlJournalCarriesEffectDispositionIntoAtomicInboxApply(t *testing.T) {
	now := time.Date(2026, 7, 26, 14, 0, 0, 123456789, time.UTC)
	wantNow := now.Truncate(time.Microsecond)
	store := &recordingRuntimeControlJournal{}
	service := application.RuntimeControlJournalService{
		Store: store,
		Now:   func() time.Time { return now },
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 2, ChannelEpoch: 9,
		ProcessNonce: "process-1", StreamAttempt: 3, OwnerFence: 4,
	}
	if _, err := service.ApplyEvent(context.Background(), application.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 7,
		MessageID: "event-7", IdempotencyKey: "event-7",
		Outcome: json.RawMessage(`{"status":"committed","effect_disposition":"commit"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if store.applyCalls != 1 ||
		store.applyRecord.EffectDisposition != application.RuntimeControlEffectDispositionCommit ||
		store.applyRecord.AppliedAt != wantNow {
		t.Fatalf("atomic Apply record=%+v calls=%d", store.applyRecord, store.applyCalls)
	}
	if _, err := service.ApplyEvent(context.Background(), application.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 8,
		MessageID: "event-8", IdempotencyKey: "event-8",
		Outcome: json.RawMessage(`{"status":"committed","effect_disposition":"maybe"}`),
	}); err == nil || store.applyCalls != 1 {
		t.Fatalf("invalid disposition err=%v apply calls=%d", err, store.applyCalls)
	}
}

type recordingRuntimeControlJournal struct {
	bindCalls   int
	bindRecord  application.BindRuntimeControlChannelRecord
	applyCalls  int
	applyRecord application.ApplyRuntimeControlEventRecord
}

func (store *recordingRuntimeControlJournal) BindRuntimeControlChannel(
	_ context.Context,
	record application.BindRuntimeControlChannelRecord,
) (application.RuntimeControlChannelState, error) {
	store.bindCalls++
	store.bindRecord = record
	return application.RuntimeControlChannelState{
		WorkerSessionID:   record.Command.WorkerSessionID,
		RuntimeInstanceID: record.Command.RuntimeInstanceID,
		RuntimeGeneration: record.Command.RuntimeGeneration,
		ChannelEpoch:      record.Command.ChannelEpoch,
		CredentialDigest:  record.CredentialDigest,
	}, nil
}

func (store *recordingRuntimeControlJournal) ClaimRuntimeControlStream(
	context.Context,
	application.ClaimRuntimeControlStreamRecord,
) (application.RuntimeControlReplayPlan, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) ReserveRuntimeControlCommand(
	context.Context,
	application.ReserveRuntimeControlCommandRecord,
) (application.RuntimeControlMessage, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) AcknowledgeRuntimeControlCommands(
	context.Context,
	application.AcknowledgeRuntimeControlCommandsRecord,
) (application.RuntimeControlChannelState, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) AcceptRuntimeControlEvent(
	context.Context,
	application.AcceptRuntimeControlEventRecord,
) (application.RuntimeControlEventAcceptance, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) ApplyRuntimeControlEvent(
	_ context.Context,
	record application.ApplyRuntimeControlEventRecord,
) (application.RuntimeControlEventAcceptance, error) {
	store.applyCalls++
	store.applyRecord = record
	return application.RuntimeControlEventAcceptance{}, nil
}

func (store *recordingRuntimeControlJournal) ReconcileRuntimeControlEffect(
	context.Context,
	application.ReconcileRuntimeControlEffectRecord,
) (application.RuntimeControlEffectResult, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) LoadRuntimeControlReplayPlan(
	context.Context,
	application.RuntimeControlAuthority,
) (application.RuntimeControlReplayPlan, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) DisconnectRuntimeControl(
	context.Context,
	application.RuntimeControlAuthority,
	time.Time,
) (application.RuntimeControlChannelState, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) FenceRuntimeControl(
	context.Context,
	application.FenceRuntimeControlChannelCommand,
	time.Time,
) (application.RuntimeControlChannelState, error) {
	panic("unexpected call")
}

func (store *recordingRuntimeControlJournal) CompleteRuntimeControlResume(
	context.Context,
	application.CompleteRuntimeControlResumeRecord,
) (application.RuntimeControlChannelState, error) {
	panic("unexpected CompleteRuntimeControlResume")
}
