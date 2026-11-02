package application_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/abietic/switchback/internal/workerruntime/domain"
)

func TestFirstHeartbeatClassificationUsesOriginalAnchorBoundaries(t *testing.T) {
	t.Parallel()

	anchorAt := time.Date(
		2026,
		7,
		27,
		8,
		0,
		0,
		123456000,
		time.UTC,
	)
	tests := []struct {
		name        string
		evaluatedAt time.Time
		want        application.RuntimeFirstHeartbeatStatus
		wantError   bool
	}{
		{
			name:        "missing immutable anchor",
			evaluatedAt: time.Time{},
			wantError:   true,
		},
		{
			name:        "before immutable anchor",
			evaluatedAt: anchorAt.Add(-time.Microsecond),
			wantError:   true,
		},
		{
			name:        "before suspect boundary",
			evaluatedAt: anchorAt.Add(45*time.Second - time.Microsecond),
			want:        application.RuntimeFirstHeartbeatAwaiting,
		},
		{
			name:        "at suspect boundary",
			evaluatedAt: anchorAt.Add(45 * time.Second),
			want:        application.RuntimeFirstHeartbeatSuspect,
		},
		{
			name:        "before unhealthy boundary",
			evaluatedAt: anchorAt.Add(90*time.Second - time.Microsecond),
			want:        application.RuntimeFirstHeartbeatSuspect,
		},
		{
			name:        "at unhealthy boundary",
			evaluatedAt: anchorAt.Add(90 * time.Second),
			want:        application.RuntimeFirstHeartbeatUnhealthy,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := application.ClassifyRuntimeFirstHeartbeat(
				anchorAt,
				test.evaluatedAt,
			)
			if test.wantError {
				if err == nil {
					t.Fatalf("classification=%q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf(
					"classification=%q error=%v want=%q",
					got,
					err,
					test.want,
				)
			}
		})
	}
}

func TestFirstHeartbeatServiceUsesServerClockAndSchedulesOneUnhealthyEvaluation(t *testing.T) {
	t.Parallel()

	now := time.Date(
		2026,
		7,
		27,
		8,
		0,
		45,
		123456789,
		time.FixedZone("caller-independent", 8*60*60),
	)
	store := &recordingFirstHeartbeatStore{
		result: application.RuntimeFirstHeartbeatEvaluation{
			Applied:          true,
			Status:           application.RuntimeFirstHeartbeatSuspect,
			WatchdogRevision: 6,
			RuntimeRevision:  8,
		},
	}
	ids := &recordingFirstHeartbeatIDs{id: "scheduled-first-heartbeat-unhealthy-1"}
	service := application.RuntimeFirstHeartbeatService{
		Store: store,
		IDs:   ids,
		Now:   func() time.Time { return now },
	}
	command := validFirstHeartbeatCommand()
	command.EvaluatedAt = now.Add(24 * time.Hour)

	got, err := service.Evaluate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != application.RuntimeFirstHeartbeatSuspect {
		t.Fatalf("status=%q want=%q", got.Status, application.RuntimeFirstHeartbeatSuspect)
	}
	if store.calls != 1 {
		t.Fatalf("store calls=%d want=1", store.calls)
	}
	wantNow := now.UTC().Truncate(time.Microsecond)
	record := store.records[0]
	if !record.Command.EvaluatedAt.Equal(wantNow) {
		t.Fatalf(
			"evaluated_at=%s want server time %s",
			record.Command.EvaluatedAt,
			wantNow,
		)
	}
	if record.UnhealthyCommandID != ids.id {
		t.Fatalf("unhealthy command id=%q want=%q", record.UnhealthyCommandID, ids.id)
	}
	const wantKey = "first-heartbeat/2/3/unhealthy"
	if record.UnhealthyIdempotencyKey != wantKey {
		t.Fatalf(
			"unhealthy idempotency key=%q want=%q",
			record.UnhealthyIdempotencyKey,
			wantKey,
		)
	}
	if len(ids.kinds) != 1 || ids.kinds[0] != "scheduled-runtime-first-heartbeat-watchdog" {
		t.Fatalf("generated ID kinds=%v", ids.kinds)
	}
}

func TestFirstHeartbeatServiceRejectsInvalidCommandBeforeStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*application.EvaluateRuntimeFirstHeartbeatCommand)
	}{
		{
			name: "missing tenant",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.TenantID = ""
			},
		},
		{
			name: "missing runtime",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.RuntimeInstanceID = ""
			},
		},
		{
			name: "zero generation",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.RuntimeGeneration = 0
			},
		},
		{
			name: "zero channel epoch",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.ChannelEpoch = 0
			},
		},
		{
			name: "unknown anchor kind",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.AnchorKind = "reconnect_v1"
			},
		},
		{
			name: "zero anchor revision",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.AnchorRevision = 0
			},
		},
		{
			name: "zero watchdog revision",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.ExpectedWatchdogRevision = 0
			},
		},
		{
			name: "unknown stage",
			mutate: func(command *application.EvaluateRuntimeFirstHeartbeatCommand) {
				command.Stage = "quiet"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := &recordingFirstHeartbeatStore{}
			ids := &recordingFirstHeartbeatIDs{id: "must-not-be-generated"}
			service := application.RuntimeFirstHeartbeatService{
				Store: store,
				IDs:   ids,
				Now:   func() time.Time { return time.Now().UTC() },
			}
			command := validFirstHeartbeatCommand()
			test.mutate(&command)

			if _, err := service.Evaluate(context.Background(), command); err == nil {
				t.Fatal("invalid first-heartbeat command was accepted")
			}
			if store.calls != 0 || len(ids.kinds) != 0 {
				t.Fatalf("store calls=%d generated ID kinds=%v", store.calls, ids.kinds)
			}
		})
	}
}

func TestFirstHeartbeatServiceRejectsZeroServerTime(t *testing.T) {
	t.Parallel()

	store := &recordingFirstHeartbeatStore{}
	service := application.RuntimeFirstHeartbeatService{
		Store: store,
		Now:   func() time.Time { return time.Time{} },
	}
	command := validFirstHeartbeatCommand()
	command.Stage = application.RuntimeFirstHeartbeatStageUnhealthy

	if _, err := service.Evaluate(context.Background(), command); err == nil {
		t.Fatal("zero server time was accepted")
	}
	if store.calls != 0 {
		t.Fatalf("store calls=%d want=0", store.calls)
	}
}

func TestFirstHeartbeatServiceAllowsZeroValueNoopForStaleCommand(t *testing.T) {
	t.Parallel()

	store := &recordingFirstHeartbeatStore{}
	service := application.RuntimeFirstHeartbeatService{
		Store: store,
		Now:   func() time.Time { return time.Now().UTC() },
	}
	command := validFirstHeartbeatCommand()
	command.Stage = application.RuntimeFirstHeartbeatStageUnhealthy

	got, err := service.Evaluate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got != (application.RuntimeFirstHeartbeatEvaluation{}) {
		t.Fatalf("stale evaluation=%+v want zero-value no-op", got)
	}
}

func TestFirstHeartbeatServiceReplaysStableUnhealthyRecoveryIntent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 27, 8, 1, 30, 0, time.UTC)
	recovery := application.OpenRuntimeRecoveryCommand{
		TenantID:                "tenant-local",
		WorkerSessionID:         "session-1",
		FailedRuntimeInstanceID: "runtime-1",
		FailedGeneration:        2,
		FailedChannelEpoch:      3,
		Trigger:                 domain.RecoveryTriggerHeartbeatUnhealthy,
		EvidenceRef:             "runtime-first-heartbeat/runtime-1/2/3",
		EvidenceAt:              now,
		ExpectedSessionRevision: 11,
		ExpectedRuntimeRevision: 8,
		IdempotencyKey:          "runtime-first-heartbeat/runtime-1/2/3/recovery",
	}
	replayedRecovery := recovery
	store := &recordingFirstHeartbeatStore{
		results: []application.RuntimeFirstHeartbeatEvaluation{
			{
				Applied:          true,
				Status:           application.RuntimeFirstHeartbeatUnhealthy,
				WatchdogRevision: 7,
				RuntimeRevision:  8,
				OpenRecovery:     &recovery,
			},
			{
				Applied:          false,
				Status:           application.RuntimeFirstHeartbeatUnhealthy,
				WatchdogRevision: 7,
				RuntimeRevision:  8,
				OpenRecovery:     &replayedRecovery,
			},
		},
	}
	service := application.RuntimeFirstHeartbeatService{
		Store: store,
		Now:   func() time.Time { return now },
	}
	command := validFirstHeartbeatCommand()
	command.Stage = application.RuntimeFirstHeartbeatStageUnhealthy

	first, err := service.Evaluate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Evaluate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 2 {
		t.Fatalf("store calls=%d want=2", store.calls)
	}
	if first.OpenRecovery == second.OpenRecovery ||
		!reflect.DeepEqual(first.OpenRecovery, second.OpenRecovery) {
		t.Fatalf("replayed recovery intents first=%+v second=%+v", first, second)
	}
	for index, record := range store.records {
		if record.UnhealthyCommandID != "" || record.UnhealthyIdempotencyKey != "" {
			t.Fatalf("record[%d] unexpectedly scheduled another stage: %+v", index, record)
		}
	}
}

func TestFirstHeartbeatServiceAllowsReplacementTerminalReason(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 27, 8, 1, 30, 0, time.UTC)
	store := &recordingFirstHeartbeatStore{
		result: application.RuntimeFirstHeartbeatEvaluation{
			Applied:          true,
			Status:           application.RuntimeFirstHeartbeatUnhealthy,
			TerminalReason:   application.RuntimeFirstHeartbeatReplacementTimeout,
			WatchdogRevision: 7,
			RuntimeRevision:  8,
		},
	}
	service := application.RuntimeFirstHeartbeatService{
		Store: store,
		Now:   func() time.Time { return now },
	}
	command := validFirstHeartbeatCommand()
	command.Stage = application.RuntimeFirstHeartbeatStageUnhealthy

	got, err := service.Evaluate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenRecovery != nil ||
		got.TerminalReason != application.RuntimeFirstHeartbeatReplacementTimeout {
		t.Fatalf("replacement evaluation=%+v", got)
	}
}

func TestFirstHeartbeatServiceAllowsCancelledTerminalResult(t *testing.T) {
	t.Parallel()

	store := &recordingFirstHeartbeatStore{
		result: application.RuntimeFirstHeartbeatEvaluation{
			Status:           application.RuntimeFirstHeartbeatCancelled,
			TerminalReason:   application.RuntimeFirstHeartbeatTurnOutcome,
			WatchdogRevision: 7,
			RuntimeRevision:  8,
		},
	}
	service := application.RuntimeFirstHeartbeatService{
		Store: store,
		Now:   func() time.Time { return time.Now().UTC() },
	}
	command := validFirstHeartbeatCommand()
	command.Stage = application.RuntimeFirstHeartbeatStageUnhealthy

	got, err := service.Evaluate(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != application.RuntimeFirstHeartbeatCancelled {
		t.Fatalf("status=%q want=%q", got.Status, application.RuntimeFirstHeartbeatCancelled)
	}
}

func TestFirstHeartbeatServiceRejectsInvalidStoreResult(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 27, 8, 1, 30, 0, time.UTC)
	tests := []struct {
		name   string
		result application.RuntimeFirstHeartbeatEvaluation
	}{
		{
			name: "unknown status",
			result: application.RuntimeFirstHeartbeatEvaluation{
				Status:           "quiet",
				WatchdogRevision: 7,
				RuntimeRevision:  8,
			},
		},
		{
			name: "zero watchdog revision",
			result: application.RuntimeFirstHeartbeatEvaluation{
				Status:          application.RuntimeFirstHeartbeatSatisfied,
				RuntimeRevision: 8,
			},
		},
		{
			name: "unhealthy without recovery or terminal reason",
			result: application.RuntimeFirstHeartbeatEvaluation{
				Status:           application.RuntimeFirstHeartbeatUnhealthy,
				WatchdogRevision: 7,
				RuntimeRevision:  8,
			},
		},
		{
			name: "cancelled without terminal reason",
			result: application.RuntimeFirstHeartbeatEvaluation{
				Status:           application.RuntimeFirstHeartbeatCancelled,
				WatchdogRevision: 7,
				RuntimeRevision:  8,
			},
		},
		{
			name: "recovery on non-unhealthy status",
			result: application.RuntimeFirstHeartbeatEvaluation{
				Status:           application.RuntimeFirstHeartbeatSatisfied,
				WatchdogRevision: 7,
				RuntimeRevision:  8,
				OpenRecovery: &application.OpenRuntimeRecoveryCommand{
					Trigger: domain.RecoveryTriggerHeartbeatUnhealthy,
				},
			},
		},
		{
			name: "wrong recovery trigger",
			result: application.RuntimeFirstHeartbeatEvaluation{
				Status:           application.RuntimeFirstHeartbeatUnhealthy,
				WatchdogRevision: 7,
				RuntimeRevision:  8,
				OpenRecovery: &application.OpenRuntimeRecoveryCommand{
					TenantID:                "tenant-local",
					WorkerSessionID:         "session-1",
					FailedRuntimeInstanceID: "runtime-1",
					FailedGeneration:        2,
					FailedChannelEpoch:      3,
					Trigger:                 domain.RecoveryTriggerContainerExit,
					EvidenceRef:             "runtime-first-heartbeat/runtime-1/2/3",
					EvidenceAt:              now,
					ExpectedSessionRevision: 11,
					ExpectedRuntimeRevision: 8,
					IdempotencyKey:          "runtime-first-heartbeat/runtime-1/2/3/recovery",
				},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := &recordingFirstHeartbeatStore{result: test.result}
			service := application.RuntimeFirstHeartbeatService{
				Store: store,
				Now:   func() time.Time { return now },
			}
			command := validFirstHeartbeatCommand()
			command.Stage = application.RuntimeFirstHeartbeatStageUnhealthy

			if _, err := service.Evaluate(context.Background(), command); err == nil {
				t.Fatal("invalid first-heartbeat store result was accepted")
			}
		})
	}
}

func validFirstHeartbeatCommand() application.EvaluateRuntimeFirstHeartbeatCommand {
	return application.EvaluateRuntimeFirstHeartbeatCommand{
		TenantID:                 "tenant-local",
		RuntimeInstanceID:        "runtime-1",
		RuntimeGeneration:        2,
		ChannelEpoch:             3,
		AnchorKind:               application.RuntimeFirstHeartbeatAnchorResumeComplete,
		AnchorRevision:           4,
		Stage:                    application.RuntimeFirstHeartbeatStageSuspect,
		ExpectedWatchdogRevision: 5,
	}
}

type recordingFirstHeartbeatStore struct {
	calls   int
	records []application.EvaluateRuntimeFirstHeartbeatRecord
	result  application.RuntimeFirstHeartbeatEvaluation
	results []application.RuntimeFirstHeartbeatEvaluation
}

func (store *recordingFirstHeartbeatStore) EvaluateRuntimeFirstHeartbeat(
	_ context.Context,
	record application.EvaluateRuntimeFirstHeartbeatRecord,
) (application.RuntimeFirstHeartbeatEvaluation, error) {
	store.calls++
	store.records = append(store.records, record)
	if len(store.results) != 0 {
		index := store.calls - 1
		if index >= len(store.results) {
			index = len(store.results) - 1
		}
		return store.results[index], nil
	}
	return store.result, nil
}

type recordingFirstHeartbeatIDs struct {
	id    string
	kinds []string
}

func (ids *recordingFirstHeartbeatIDs) NewID(kind string) (string, error) {
	ids.kinds = append(ids.kinds, kind)
	return ids.id, nil
}
