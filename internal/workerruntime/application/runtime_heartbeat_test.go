package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeHeartbeatObserverUsesServerReceiveTimeAndIndependentSequence(t *testing.T) {
	receivedAt := time.Date(2026, 7, 26, 4, 0, 0, 0, time.UTC)
	store := &recordingHeartbeatStore{}
	observer := application.RuntimeHeartbeatObserver{
		Service: application.RuntimeHeartbeatService{
			Store: store,
			IDs:   &sequenceIDs{values: []string{"scheduled-heartbeat-1"}},
		},
	}
	event := application.WorkerControlEvent{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion,
		MessageID:     "heartbeat-9", Kind: "heartbeat",
		WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-2",
		RuntimeGeneration: 2, ChannelEpoch: 7, Sequence: 42,
		MessageType: "runtime.heartbeat",
		Payload:     json.RawMessage(`{"heartbeat_sequence":9}`),
	}}
	if err := observer.ObserveWorkerControlEvent(context.Background(), event, receivedAt); err != nil {
		t.Fatal(err)
	}
	if store.record.Command.HeartbeatSequence != 9 ||
		store.record.Command.ControlSequence != 42 ||
		!store.record.Command.ReceivedAt.Equal(receivedAt) ||
		store.record.ScheduledCommandID != "scheduled-heartbeat-1" {
		t.Fatalf("heartbeat record=%+v", store.record)
	}
	if !store.record.SuspectDueAt.Equal(receivedAt.Add(45*time.Second)) ||
		!store.record.UnhealthyDueAt.Equal(receivedAt.Add(90*time.Second)) {
		t.Fatalf("heartbeat due times=%+v", store.record)
	}
}

func TestRuntimeHeartbeatObserverRejectsMissingSequenceBeforePersistence(t *testing.T) {
	store := &recordingHeartbeatStore{}
	observer := application.RuntimeHeartbeatObserver{
		Service: application.RuntimeHeartbeatService{
			Store: store, IDs: &sequenceIDs{values: []string{"unused"}},
		},
	}
	event := application.WorkerControlEvent{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion,
		MessageID:     "heartbeat", Kind: "heartbeat",
		WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, ChannelEpoch: 1, Sequence: 1,
		MessageType: "runtime.heartbeat", Payload: json.RawMessage(`{}`),
	}}
	if err := observer.ObserveWorkerControlEvent(context.Background(), event, time.Now()); err == nil {
		t.Fatal("heartbeat without independent sequence was accepted")
	}
	if store.calls != 0 {
		t.Fatal("invalid heartbeat reached persistence")
	}
}

func TestRuntimeLivenessClassificationUsesFixedServerThresholds(t *testing.T) {
	last := time.Date(2026, 7, 26, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		now  time.Time
		want application.RuntimeLivenessStatus
	}{
		{name: "healthy", now: last.Add(44*time.Second + 999*time.Millisecond), want: application.RuntimeLivenessHealthy},
		{name: "suspect boundary", now: last.Add(45 * time.Second), want: application.RuntimeLivenessSuspect},
		{name: "suspect", now: last.Add(89*time.Second + 999*time.Millisecond), want: application.RuntimeLivenessSuspect},
		{name: "unhealthy boundary", now: last.Add(90 * time.Second), want: application.RuntimeLivenessUnhealthy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := application.ClassifyRuntimeLiveness(last, test.now); got != test.want {
				t.Fatalf("status=%q want=%q", got, test.want)
			}
		})
	}
}

func TestRuntimeLivenessServiceUsesServerClockAndFixedUnhealthyIdentity(t *testing.T) {
	now := time.Date(2026, 7, 26, 5, 0, 0, 0, time.UTC)
	store := &recordingLivenessStore{}
	service := application.RuntimeLivenessService{
		Store: store,
		IDs:   fixedAllocationIDs{id: "scheduled-liveness-unhealthy-1"},
		Now:   func() time.Time { return now },
	}
	_, err := service.Evaluate(context.Background(), application.EvaluateRuntimeLivenessCommand{
		TenantID: "tenant-local", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 2, ChannelEpoch: 3, HeartbeatSequence: 9,
		Stage: application.RuntimeLivenessStageSuspect, ExpectedObservationRevision: 4,
		EvaluatedAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !store.record.Command.EvaluatedAt.Equal(now) ||
		store.record.UnhealthyCommandID != "scheduled-liveness-unhealthy-1" ||
		store.record.UnhealthyIdempotencyKey != "heartbeat/2/9/unhealthy" {
		t.Fatalf("liveness record=%+v", store.record)
	}
}

func TestRuntimeLivenessServiceRejectsUnknownStage(t *testing.T) {
	service := application.RuntimeLivenessService{
		Store: &recordingLivenessStore{}, IDs: fixedAllocationIDs{id: "unused"},
		Now: time.Now,
	}
	_, err := service.Evaluate(context.Background(), application.EvaluateRuntimeLivenessCommand{
		TenantID: "tenant-local", RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1, ChannelEpoch: 1, HeartbeatSequence: 1,
		Stage: "quiet", ExpectedObservationRevision: 1,
	})
	if err == nil {
		t.Fatal("unknown liveness stage was accepted")
	}
}

type recordingHeartbeatStore struct {
	calls  int
	record application.RecordRuntimeHeartbeat
}

type recordingLivenessStore struct {
	record application.EvaluateRuntimeLivenessRecord
}

func (store *recordingLivenessStore) EvaluateRuntimeLiveness(
	_ context.Context,
	record application.EvaluateRuntimeLivenessRecord,
) (application.RuntimeLivenessEvaluation, error) {
	store.record = record
	return application.RuntimeLivenessEvaluation{}, nil
}

type fixedAllocationIDs struct {
	id string
}

func (ids fixedAllocationIDs) NewID(string) (string, error) {
	return ids.id, nil
}

func (store *recordingHeartbeatStore) RecordRuntimeHeartbeat(
	_ context.Context,
	record application.RecordRuntimeHeartbeat,
) (application.RuntimeHeartbeatState, error) {
	store.calls++
	store.record = record
	return application.RuntimeHeartbeatState{HeartbeatSequence: record.Command.HeartbeatSequence}, nil
}
