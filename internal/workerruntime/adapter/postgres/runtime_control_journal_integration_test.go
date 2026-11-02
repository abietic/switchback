//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapplication "github.com/abietic/switchback/internal/workerruntime/application"
)

func TestRuntimeControlJournalFencesOwnersAndReservesCommandsBeforeSend(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	current := now.Add(6 * time.Minute)
	service := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return current },
	}
	bind := runtimeControlBind(started)
	firstBinding, err := service.Bind(ctx, bind)
	if err != nil {
		t.Fatal(err)
	}
	duplicateBinding, err := service.Bind(ctx, bind)
	if err != nil || duplicateBinding.Revision != firstBinding.Revision {
		t.Fatalf("duplicate binding=%+v err=%v", duplicateBinding, err)
	}
	conflictingBind := bind
	conflictingBind.Credential = "other-secret"
	if _, err := service.Bind(ctx, conflictingBind); !errors.Is(err, workerapplication.ErrRuntimeControlCredentialConflict) {
		t.Fatalf("conflicting credential err=%v", err)
	}

	firstPlan, err := service.Claim(ctx, runtimeControlClaim(started, "process-1", 1, "backend-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authorityFrom(firstPlan.Channel), MessageID: "before-resume-complete",
		IdempotencyKey: "before-resume-complete",
		Envelope: runtimeControlEnvelope(
			started, "command", 0, "before-resume-complete", "before-resume-complete", "runtime.inspect",
		),
	}); !errors.Is(err, workerapplication.ErrRuntimeControlStaleOwner) {
		t.Fatalf("reserve before ResumeComplete err=%v", err)
	}
	firstCompleted := completeRuntimeControlClaim(t, service, firstPlan)
	firstAuthority := authorityFrom(firstCompleted)
	held := runtimeControlClaim(started, "process-1", 1, "backend-other")
	if _, err := service.Claim(ctx, held); !errors.Is(err, workerapplication.ErrRuntimeControlLeaseHeld) {
		t.Fatalf("same-attempt lease err=%v", err)
	}
	firstEnvelope := runtimeControlEnvelope(started, "command", 1, "command-1", "turn-start-1", "agent_turn.start")
	var firstTemplate contractsv1.RuntimeControlEnvelope
	if err := json.Unmarshal(firstEnvelope, &firstTemplate); err != nil {
		t.Fatal(err)
	}
	firstTemplate.Payload = json.RawMessage(`{"b":2,"a":1}`)
	firstEnvelope, _ = json.Marshal(firstTemplate)
	first, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "command-1",
		IdempotencyKey: "turn-start-1", Envelope: firstEnvelope,
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicateTemplate := firstTemplate
	duplicateTemplate.Payload = json.RawMessage(` { "a": 1, "b": 2 } `)
	duplicateEnvelope, _ := json.Marshal(duplicateTemplate)
	duplicate, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "command-1",
		IdempotencyKey: "turn-start-1", Envelope: duplicateEnvelope,
	})
	if err != nil || duplicate.Sequence != first.Sequence || !duplicate.Duplicate {
		t.Fatalf("duplicate command=%+v err=%v", duplicate, err)
	}
	if !bytes.Equal(duplicate.Envelope, first.Envelope) ||
		!duplicate.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("duplicate did not replay exact reservation: first=%+v duplicate=%+v", first, duplicate)
	}
	reservedEnvelope := decodeRuntimeControlEnvelope(t, first.Envelope)
	if reservedEnvelope.Sequence != 1 || reservedEnvelope.AckSequence != 0 ||
		!reservedEnvelope.SentAt.Equal(current) {
		t.Fatalf("PostgreSQL did not own command transport fields: %+v", reservedEnvelope)
	}
	prefilled := reservedEnvelope
	prefilled.MessageID = "prefilled-command"
	prefilled.IdempotencyKey = "prefilled-command"
	prefilled.Sequence = 99
	prefilled.AckSequence = 99
	prefilled.SentAt = current.Add(time.Hour)
	prefilledBytes, _ := json.Marshal(prefilled)
	if _, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: prefilled.MessageID,
		IdempotencyKey: prefilled.IdempotencyKey, Envelope: prefilledBytes,
	}); err == nil {
		t.Fatal("caller-prefilled command transport fields were accepted")
	}
	if _, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "command-conflict",
		IdempotencyKey: "turn-start-1",
		Envelope: runtimeControlEnvelope(
			started, "command", 2, "command-conflict", "turn-start-1", "agent_turn.start",
		),
	}); !errors.Is(err, workerapplication.ErrRuntimeControlMessageConflict) {
		t.Fatalf("conflicting command err=%v", err)
	}
	second, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "command-2",
		IdempotencyKey: "turn-cancel-1",
		Envelope: runtimeControlEnvelope(
			started, "command", 2, "command-2", "turn-cancel-1", "agent_turn.cancel",
		),
	})
	if err != nil || second.Sequence != first.Sequence+1 {
		t.Fatalf("second command=%+v err=%v", second, err)
	}
	ackOnly, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "backend-ack-1",
		IdempotencyKey: "backend-ack-1",
		Envelope: runtimeControlEnvelope(
			started, "ack", 0, "backend-ack-1", "backend-ack-1", "runtime.ack",
		),
	})
	if err != nil || decodeRuntimeControlEnvelope(t, ackOnly.Envelope).Kind != "ack" {
		t.Fatalf("reserve ACK-only envelope=%+v err=%v", ackOnly, err)
	}
	wrongRuntimeEnvelope := runtimeControlEnvelope(
		started, "command", 3, "wrong-runtime-command", "wrong-runtime-command", "runtime.inspect",
	)
	var wrongRuntime contractsv1.RuntimeControlEnvelope
	if err := json.Unmarshal(wrongRuntimeEnvelope, &wrongRuntime); err != nil {
		t.Fatal(err)
	}
	wrongRuntime.RuntimeInstanceID = contractsv1.RuntimeInstanceID("runtime-other")
	wrongRuntimeEnvelope, _ = json.Marshal(wrongRuntime)
	if _, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "wrong-runtime-command",
		IdempotencyKey: "wrong-runtime-command", Envelope: wrongRuntimeEnvelope,
	}); !errors.Is(err, workerapplication.ErrRuntimeControlIdentityConflict) {
		t.Fatalf("cross-Runtime command err=%v", err)
	}

	current = current.Add(time.Second)
	secondClaim := runtimeControlClaim(started, "process-1", 2, "backend-2")
	secondPlan, err := service.Claim(ctx, secondClaim)
	if err != nil {
		t.Fatal(err)
	}
	if secondPlan.Channel.OwnerFence != firstPlan.Channel.OwnerFence+1 ||
		len(secondPlan.PendingCommands) != 3 ||
		secondPlan.PendingCommands[0].Sequence != first.Sequence {
		t.Fatalf("second replay plan=%+v", secondPlan)
	}
	if _, err := service.Claim(ctx, runtimeControlClaim(started, "process-1", 1, "backend-1")); !errors.Is(
		err, workerapplication.ErrRuntimeControlStreamAttemptStale,
	) {
		t.Fatalf("stale stream attempt err=%v", err)
	}
	conflictingCursor := runtimeControlClaim(started, "process-1", 3, "backend-3")
	conflictingCursor.WorkerCommandLastSequence = 2
	if _, err := service.Claim(ctx, conflictingCursor); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlCursorConflict,
	) {
		t.Fatalf("conflicting resume cursor err=%v", err)
	}
	if _, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: firstAuthority, MessageID: "stale-command",
		IdempotencyKey: "stale-command",
		Envelope: runtimeControlEnvelope(
			started, "command", 3, "stale-command", "stale-command", "agent_turn.cancel",
		),
	}); !errors.Is(err, workerapplication.ErrRuntimeControlStaleOwner) {
		t.Fatalf("stale reserve err=%v", err)
	}
	if _, err := service.AcknowledgeCommands(ctx, workerapplication.AcknowledgeRuntimeControlCommandsCommand{
		Authority: firstAuthority, ThroughSequence: first.Sequence,
	}); !errors.Is(err, workerapplication.ErrRuntimeControlStaleOwner) {
		t.Fatalf("stale acknowledge err=%v", err)
	}
	secondAuthority := authorityFrom(secondPlan.Channel)
	acknowledged, err := service.AcknowledgeCommands(ctx, workerapplication.AcknowledgeRuntimeControlCommandsCommand{
		Authority: secondAuthority, ThroughSequence: first.Sequence,
	})
	if err != nil || acknowledged.CommandAcknowledgedSequence != first.Sequence {
		t.Fatalf("acknowledged=%+v err=%v", acknowledged, err)
	}
	secondReady := contractsv1.RuntimeControlResumeReady{
		ServerConnectionID:              secondPlan.Channel.OwnerID,
		AcknowledgedJournalRevision:     secondPlan.Channel.ResumeClaimRevision,
		WorkerTXReplayedThroughSequence: secondPlan.Channel.EventCommittedSequence,
		WorkerTXCursorDigest:            secondPlan.Channel.EventCommittedDigest,
		WorkerRXReceivedThroughSequence: secondPlan.Channel.CommandCommittedSequence,
		WorkerRXAppliedSequence:         secondPlan.Channel.CommandAppliedSequence,
		WorkerRXCursorDigest:            secondPlan.Channel.CommandCommittedDigest,
		ActiveTurn:                      secondPlan.Channel.ActiveEffectSnapshot,
	}
	secondCompleted, err := service.CompleteResume(ctx, workerapplication.CompleteRuntimeControlResumeCommand{
		Authority: secondAuthority, Ready: secondReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondAuthority = authorityFrom(secondCompleted)
	results := make(chan workerapplication.RuntimeControlMessage, 2)
	errs := make(chan error, 2)
	for _, id := range []string{"concurrent-command-a", "concurrent-command-b"} {
		id := id
		go func() {
			message, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
				Authority: secondAuthority, MessageID: id, IdempotencyKey: id,
				Envelope: runtimeControlEnvelope(started, "command", 0, id, id, "runtime.inspect"),
			})
			results <- message
			errs <- err
		}()
	}
	sequences := map[uint64]bool{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		sequences[(<-results).Sequence] = true
	}
	if !sequences[4] || !sequences[5] {
		t.Fatalf("concurrent PostgreSQL-assigned sequences=%v", sequences)
	}
}

func TestRuntimeControlJournalAcceptsEventsOnceBeforeAcknowledgement(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	firstEnvelope := runtimeControlEnvelope(started, "event", 1, "event-1", "event-1", "runtime.update")
	secondEnvelope := runtimeControlEnvelope(started, "event", 2, "event-2", "event-2", "runtime.update")
	eventCursor := runtimeControlCursor(t, contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionRuntimeToBackend, firstEnvelope)
	eventCursor = runtimeControlCursor(t, eventCursor,
		contractsv1.RuntimeControlDirectionRuntimeToBackend, secondEnvelope)
	reconnect := runtimeControlClaim(started, "process-1", 2, "backend-2")
	reconnect.WorkerEventLastSequence = 2
	reconnect.WorkerEventCursorDigest = eventCursor
	replay, err := service.Claim(ctx, reconnect)
	if err != nil {
		t.Fatal(err)
	}
	authority = authorityFrom(replay.Channel)
	conflictingHello := reconnect
	conflictingHello.StreamAttempt = 3
	conflictingHello.OwnerID = "backend-conflicting"
	conflictingHello.WorkerEventCursorDigest = contractsv1.RuntimeControlCursorGenesisDigest
	if _, err := service.Claim(ctx, conflictingHello); !errors.Is(
		err, workerapplication.ErrRuntimeControlCursorConflict,
	) {
		t.Fatalf("same replay target with conflicting digest err=%v", err)
	}
	if replay.EventReplayFrom != 1 || replay.EventReplayThrough != 2 {
		t.Fatalf("event replay plan=%+v", replay)
	}

	firstCommand := workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "event-1", IdempotencyKey: "event-1",
		Envelope: firstEnvelope,
	}
	firstOutcome := json.RawMessage(`{"applied":true,"projection_revision":7}`)
	first, err := service.AcceptEvent(ctx, firstCommand)
	if err != nil {
		t.Fatal(err)
	}
	earlyReady := contractsv1.RuntimeControlResumeReady{
		ServerConnectionID:              "backend-2",
		AcknowledgedJournalRevision:     replay.Channel.Revision,
		WorkerTXReplayedThroughSequence: 2,
		WorkerTXCursorDigest:            eventCursor,
		WorkerRXReceivedThroughSequence: 0,
		WorkerRXAppliedSequence:         0,
		WorkerRXCursorDigest:            contractsv1.RuntimeControlCursorGenesisDigest,
	}
	if _, err := service.CompleteResume(ctx, workerapplication.CompleteRuntimeControlResumeCommand{
		Authority: authority, Ready: earlyReady,
	}); !errors.Is(err, workerapplication.ErrRuntimeControlCursorConflict) {
		t.Fatalf("early ResumeReady err=%v", err)
	}
	afterEarly, err := service.ReplayPlan(ctx, authority)
	if err != nil || afterEarly.Channel.Revision != first.Channel.Revision {
		t.Fatalf("early ResumeReady mutated barrier: plan=%+v err=%v", afterEarly, err)
	}
	duplicate, err := service.AcceptEvent(ctx, firstCommand)
	if err != nil || !duplicate.Message.Duplicate ||
		len(duplicate.Message.Outcome) != 0 || len(first.Message.Outcome) != 0 {
		t.Fatalf("duplicate event=%+v err=%v", duplicate, err)
	}
	gap := firstCommand
	gap.Sequence = 3
	gap.MessageID = "event-3"
	gap.IdempotencyKey = "event-3"
	gap.Envelope = runtimeControlEnvelope(started, "event", 3, "event-3", "event-3", "runtime.update")
	if _, err := service.AcceptEvent(ctx, gap); !errors.Is(err, workerapplication.ErrRuntimeControlSequenceGap) {
		t.Fatalf("event gap err=%v", err)
	}
	conflict := firstCommand
	conflict.Envelope = runtimeControlEnvelope(started, "event", 1, "event-1", "event-1", "runtime.conflict")
	if _, err := service.AcceptEvent(ctx, conflict); !errors.Is(err, workerapplication.ErrRuntimeControlMessageConflict) {
		t.Fatalf("conflicting duplicate err=%v", err)
	}
	second := firstCommand
	second.Sequence = 2
	second.MessageID = "event-2"
	second.IdempotencyKey = "event-2"
	second.Envelope = secondEnvelope
	secondOutcome := json.RawMessage(`{"applied":true,"projection_revision":8}`)
	conflictingReplay := second
	conflictingReplay.Envelope = runtimeControlEnvelope(
		started, "event", 2, "event-2", "event-2", "runtime.conflict",
	)
	if _, err := service.AcceptEvent(ctx, conflictingReplay); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlCursorConflict,
	) {
		t.Fatalf("conflicting replay target err=%v", err)
	}
	accepted, err := service.AcceptEvent(ctx, second)
	if err != nil || accepted.Channel.EventCommittedSequence != 2 ||
		accepted.Channel.EventAcknowledgedSequence != 2 ||
		accepted.Channel.EventAppliedSequence != 0 ||
		accepted.Channel.EventReplayTargetSequence != 2 {
		t.Fatalf("second event=%+v err=%v", accepted, err)
	}
	conflictingReady := earlyReady
	conflictingReady.WorkerTXCursorDigest = contractsv1.RuntimeControlCursorGenesisDigest
	if _, err := service.CompleteResume(ctx, workerapplication.CompleteRuntimeControlResumeCommand{
		Authority: authority, Ready: conflictingReady,
	}); !errors.Is(err, workerapplication.ErrRuntimeControlCursorConflict) {
		t.Fatalf("conflicting ResumeReady digest err=%v", err)
	}
	afterConflict, err := service.ReplayPlan(ctx, authority)
	if err != nil || afterConflict.Channel.Revision != accepted.Channel.Revision {
		t.Fatalf("conflicting ResumeReady mutated barrier: plan=%+v err=%v", afterConflict, err)
	}
	for name, revision := range map[string]uint64{
		"old":    replay.Channel.Revision - 1,
		"future": replay.Channel.Revision + 1,
	} {
		ready := earlyReady
		ready.AcknowledgedJournalRevision = revision
		if _, err := service.CompleteResume(ctx, workerapplication.CompleteRuntimeControlResumeCommand{
			Authority: authority, Ready: ready,
		}); !errors.Is(err, workerapplication.ErrRuntimeControlCursorConflict) {
			t.Fatalf("%s Claim revision err=%v", name, err)
		}
		afterRevisionConflict, err := service.ReplayPlan(ctx, authority)
		if err != nil || afterRevisionConflict.Channel.Revision != accepted.Channel.Revision {
			t.Fatalf("%s Claim revision mutated barrier: plan=%+v err=%v",
				name, afterRevisionConflict, err)
		}
	}
	completeCommand := workerapplication.CompleteRuntimeControlResumeCommand{
		Authority: authority,
		Ready: contractsv1.RuntimeControlResumeReady{
			ServerConnectionID:              "backend-2",
			AcknowledgedJournalRevision:     replay.Channel.Revision,
			WorkerTXReplayedThroughSequence: 2,
			WorkerTXCursorDigest:            eventCursor,
			WorkerRXReceivedThroughSequence: 0,
			WorkerRXAppliedSequence:         0,
			WorkerRXCursorDigest:            contractsv1.RuntimeControlCursorGenesisDigest,
		},
	}
	completed, err := service.CompleteResume(ctx, completeCommand)
	if err != nil || completed.EventReplayTargetSequence != 0 ||
		completed.EventReplayCheckpointSequence != 0 {
		t.Fatalf("complete resume=%+v err=%v", completed, err)
	}
	duplicateComplete, err := service.CompleteResume(ctx, completeCommand)
	if err != nil || duplicateComplete.Revision != completed.Revision {
		t.Fatalf("duplicate ResumeReady=%+v err=%v", duplicateComplete, err)
	}
	pool = postgrestest.Restart(t, pool)
	service = workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(7 * time.Minute) },
	}
	pending, err := service.ReplayPlan(ctx, authority)
	if err != nil || len(pending.PendingEvents) != 2 {
		t.Fatalf("pending events after restart=%+v err=%v", pending.PendingEvents, err)
	}
	if _, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 2, MessageID: "event-2",
		IdempotencyKey: "apply-event-2", Outcome: secondOutcome,
	}); !errors.Is(err, workerapplication.ErrRuntimeControlSequenceGap) {
		t.Fatalf("out-of-order event apply err=%v", err)
	}
	appliedFirst, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "event-1",
		IdempotencyKey: "apply-event-1", Outcome: firstOutcome,
	})
	if err != nil || appliedFirst.Channel.EventAppliedSequence != 1 {
		t.Fatalf("apply first=%+v err=%v", appliedFirst, err)
	}
	duplicateApply, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "event-1",
		IdempotencyKey: "apply-event-1", Outcome: firstOutcome,
	})
	if err != nil || !duplicateApply.Message.Duplicate {
		t.Fatalf("duplicate event apply=%+v err=%v", duplicateApply, err)
	}
	if _, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "event-1",
		IdempotencyKey: "apply-event-conflict", Outcome: json.RawMessage(`{"applied":false}`),
	}); !errors.Is(err, workerapplication.ErrRuntimeControlMessageConflict) {
		t.Fatalf("conflicting event apply err=%v", err)
	}
	if _, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 2, MessageID: "event-2",
		IdempotencyKey: "apply-event-2", Outcome: secondOutcome,
	}); err != nil {
		t.Fatal(err)
	}
	complete, err := service.ReplayPlan(ctx, authority)
	if err != nil || len(complete.PendingEvents) != 0 ||
		complete.Channel.EventAppliedSequence != 2 {
		t.Fatalf("complete event replay=%+v err=%v", complete, err)
	}
}

func TestRuntimeControlJournalPreservesReplayCheckpointWhenTargetGrows(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 13, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, _ := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	cursor := contractsv1.RuntimeControlCursorGenesisDigest
	for sequence := uint64(1); sequence <= 2; sequence++ {
		envelope := runtimeControlEnvelope(
			started, "event", sequence, fmt.Sprintf("grow-event-%d", sequence),
			fmt.Sprintf("grow-event-%d", sequence), "runtime.update",
		)
		cursor = runtimeControlCursor(
			t, cursor, contractsv1.RuntimeControlDirectionRuntimeToBackend, envelope,
		)
	}
	first := runtimeControlClaim(started, "process-1", 2, "backend-2")
	first.WorkerEventLastSequence = 2
	first.WorkerEventCursorDigest = cursor
	firstPlan, err := service.Claim(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	oldCheckpointDigest := firstPlan.Channel.EventReplayCheckpointDigest
	thirdEnvelope := runtimeControlEnvelope(
		started, "event", 3, "grow-event-3", "grow-event-3", "runtime.update",
	)
	cursor = runtimeControlCursor(
		t, cursor, contractsv1.RuntimeControlDirectionRuntimeToBackend, thirdEnvelope,
	)
	grown := first
	grown.StreamAttempt = 3
	grown.OwnerID = "backend-3"
	grown.WorkerEventLastSequence = 3
	grown.WorkerEventCursorDigest = cursor
	grownPlan, err := service.Claim(ctx, grown)
	if err != nil ||
		grownPlan.Channel.EventReplayCheckpointSequence != 2 ||
		grownPlan.Channel.EventReplayCheckpointDigest != oldCheckpointDigest ||
		grownPlan.Channel.EventReplayTargetSequence != 3 ||
		grownPlan.Channel.EventReplayTargetDigest != cursor {
		t.Fatalf("grown replay target=%+v err=%v", grownPlan.Channel, err)
	}
}

func TestRuntimeControlJournalAdvancesSatisfiedReplayCheckpointWhenTargetGrows(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 13, 35, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, _ := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))

	cursor := contractsv1.RuntimeControlCursorGenesisDigest
	var replayed []json.RawMessage
	for sequence := uint64(1); sequence <= 2; sequence++ {
		envelope := runtimeControlEnvelope(
			started, "event", sequence, fmt.Sprintf("satisfied-event-%d", sequence),
			fmt.Sprintf("satisfied-event-%d", sequence), "runtime.update",
		)
		cursor = runtimeControlCursor(
			t, cursor, contractsv1.RuntimeControlDirectionRuntimeToBackend, envelope,
		)
		replayed = append(replayed, envelope)
	}
	first := runtimeControlClaim(started, "process-1", 2, "backend-2")
	first.WorkerEventLastSequence = 2
	first.WorkerEventCursorDigest = cursor
	firstPlan, err := service.Claim(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	authority := authorityFrom(firstPlan.Channel)
	for index, envelope := range replayed {
		sequence := uint64(index + 1)
		if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
			Authority: authority, Sequence: sequence,
			MessageID:      fmt.Sprintf("satisfied-event-%d", sequence),
			IdempotencyKey: fmt.Sprintf("satisfied-event-%d", sequence),
			Envelope:       envelope,
		}); err != nil {
			t.Fatalf("accept replay checkpoint event %d: %v", sequence, err)
		}
	}

	thirdEnvelope := runtimeControlEnvelope(
		started, "event", 3, "satisfied-event-3", "satisfied-event-3", "runtime.update",
	)
	cursor = runtimeControlCursor(
		t, cursor, contractsv1.RuntimeControlDirectionRuntimeToBackend, thirdEnvelope,
	)
	grown := first
	grown.StreamAttempt = 3
	grown.OwnerID = "backend-3"
	grown.WorkerEventLastSequence = 3
	grown.WorkerEventCursorDigest = cursor
	grownPlan, err := service.Claim(ctx, grown)
	if err != nil {
		t.Fatal(err)
	}
	if grownPlan.Channel.EventReplayCheckpointSequence != 3 ||
		grownPlan.Channel.EventReplayCheckpointDigest != cursor {
		t.Fatalf("satisfied replay checkpoint did not advance: %+v", grownPlan.Channel)
	}
	if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authorityFrom(grownPlan.Channel), Sequence: 3,
		MessageID: "satisfied-event-3", IdempotencyKey: "satisfied-event-3",
		Envelope: thirdEnvelope,
	}); err != nil {
		t.Fatalf("accept event after satisfied target grew: %v", err)
	}
}

func TestRuntimeControlJournalAllowsOldDuplicateAckButRejectsNewAckRegression(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 13, 40, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	if _, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "ack-command", IdempotencyKey: "ack-command",
		Envelope: runtimeControlEnvelope(started, "command", 0, "ack-command", "ack-command", "runtime.inspect"),
	}); err != nil {
		t.Fatal(err)
	}
	firstEnvelope := runtimeControlEnvelope(
		started, "event", 1, "old-ack-event", "old-ack-event", "runtime.update",
	)
	firstCommand := workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "old-ack-event",
		IdempotencyKey: "old-ack-event", Envelope: firstEnvelope,
	}
	if _, err := service.AcceptEvent(ctx, firstCommand); err != nil {
		t.Fatal(err)
	}
	secondEnvelope := runtimeControlEnvelopeWithAck(t, runtimeControlEnvelope(
		started, "event", 2, "new-ack-event", "new-ack-event", "runtime.update",
	), 1)
	if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 2, MessageID: "new-ack-event",
		IdempotencyKey: "new-ack-event", Envelope: secondEnvelope,
	}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.AcceptEvent(ctx, firstCommand)
	if err != nil || !duplicate.Message.Duplicate {
		t.Fatalf("old duplicate ACK=%+v err=%v", duplicate, err)
	}
	if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 3, MessageID: "regressed-ack-event",
		IdempotencyKey: "regressed-ack-event",
		Envelope: runtimeControlEnvelope(
			started, "event", 3, "regressed-ack-event", "regressed-ack-event", "runtime.update",
		),
	}); !errors.Is(err, workerapplication.ErrRuntimeControlCursorConflict) {
		t.Fatalf("new ACK regression err=%v", err)
	}
}

func TestRuntimeControlJournalReconstructsReplayAndConsumesActiveTurnAfterRestart(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	start, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "agent-turn-start-1",
		IdempotencyKey: "agent-turn-" + started.AgentTurnID + "-start",
		Envelope: runtimeControlEnvelope(
			started, "command", 1, "agent-turn-start-1",
			"agent-turn-"+started.AgentTurnID+"-start", "agent_turn.start",
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
	resume := runtimeControlClaim(started, "process-1", 2, "backend-2")
	resume.WorkerCommandLastSequence = start.Sequence
	resume.WorkerCommandAppliedSequence = start.Sequence
	resume.WorkerCommandCursorDigest = start.CursorDigest
	resume.ActiveTurn = &active
	effect, err := service.Claim(ctx, resume)
	if err != nil || !effect.TurnConsumed ||
		effect.Channel.CommandAppliedSequence != start.Sequence {
		t.Fatalf("active effect resume=%+v err=%v", effect, err)
	}
	authority = authorityFrom(completeRuntimeControlClaim(t, service, effect))
	duplicateEffect, err := service.Claim(ctx, resume)
	if err != nil || duplicateEffect.TurnConsumed {
		t.Fatalf("duplicate active resume=%+v err=%v", duplicateEffect, err)
	}
	authority = authorityFrom(completeRuntimeControlClaim(t, service, duplicateEffect))
	conflicting := resume
	conflicting.StreamAttempt = 3
	conflicting.OwnerID = "backend-3"
	conflictingActive := active
	conflictingActive.WorkerRunID = contractsv1.WorkerRunID("worker-run-other")
	conflicting.ActiveTurn = &conflictingActive
	if _, err := service.Claim(ctx, conflicting); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlIdentityConflict,
	) {
		t.Fatalf("conflicting active resume err=%v", err)
	}
	pending, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "command-after-active-1",
		IdempotencyKey: "command-after-active-1",
		Envelope: runtimeControlEnvelope(
			started, "command", 2, "command-after-active-1",
			"command-after-active-1", "runtime.inspect",
		),
	})
	if err != nil {
		t.Fatal(err)
	}

	pool = postgrestest.Restart(t, pool)
	restarted := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(7 * time.Minute) },
	}
	plan, err := restarted.ReplayPlan(ctx, authority)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PendingCommands) != 1 ||
		plan.PendingCommands[0].Sequence != pending.Sequence ||
		plan.Channel.ActiveEffectAgentTurnID != started.AgentTurnID ||
		plan.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectActive {
		t.Fatalf("restart replay plan=%+v", plan)
	}
	duplicate, err := restarted.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: start.Sequence,
		MessageID: "agent-turn-start-1", AgentTurnID: started.AgentTurnID,
		Phase:      workerapplication.RuntimeControlEffectActive,
		ActiveTurn: &active,
	})
	if err != nil || !duplicate.Duplicate || duplicate.TurnConsumed {
		t.Fatalf("duplicate active effect=%+v err=%v", duplicate, err)
	}
	var turnStatus string
	var turnRevision uint64
	if err := pool.QueryRow(ctx, `
SELECT status,revision FROM agent_turns WHERE agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(&turnStatus, &turnRevision); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "consumed" || turnRevision != 2 {
		t.Fatalf("turn status=%s revision=%d", turnStatus, turnRevision)
	}
}

func TestRuntimeControlJournalRejectsEffectAckForNonStartCommand(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	message, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "runtime-inspect-1",
		IdempotencyKey: "runtime-inspect-1",
		Envelope: runtimeControlEnvelope(
			started, "command", 1, "runtime-inspect-1", "runtime-inspect-1", "runtime.inspect",
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: message.Sequence,
		MessageID: "runtime-inspect-1", AgentTurnID: started.AgentTurnID,
		Phase: workerapplication.RuntimeControlEffectActive,
		ActiveTurn: func() *contractsv1.RuntimeControlActiveTurn {
			proof := runtimeControlActiveTurn(
				t, started, message, contractsv1.RuntimeControlTurnEffectActive,
			)
			return &proof
		}(),
	}); !errors.Is(err, workerapplication.ErrRuntimeControlMessageConflict) {
		t.Fatalf("non-start effect acknowledgement err=%v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `
SELECT status FROM agent_turns WHERE agent_turn_id=$1`,
		started.AgentTurnID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("non-start effect changed AgentTurn to %s", status)
	}
}

func TestRuntimeControlJournalDisconnectsAndFencesExactAuthority(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	disconnected, err := service.Disconnect(ctx, authority)
	if err != nil || disconnected.Status != "disconnected" ||
		disconnected.OwnerFence != authority.OwnerFence+1 {
		t.Fatalf("disconnected=%+v err=%v", disconnected, err)
	}
	if _, err := service.Claim(ctx, runtimeControlClaim(started, "process-1", 1, "backend-1")); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlStreamAttemptStale,
	) {
		t.Fatalf("same stream attempt reclaimed disconnected channel: %v", err)
	}
	resumed, err := service.Claim(ctx, runtimeControlClaim(started, "process-1", 2, "backend-2"))
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := service.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: started.RuntimeGeneration, ChannelEpoch: started.ControlChannelEpoch,
	})
	if err != nil || fenced.Status != "fenced" ||
		fenced.OwnerFence != resumed.Channel.OwnerFence+1 {
		t.Fatalf("fenced=%+v err=%v", fenced, err)
	}
	if _, err := service.Claim(ctx, runtimeControlClaim(started, "process-1", 3, "backend-3")); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlStaleOwner,
	) {
		t.Fatalf("fenced channel reclaimed: %v", err)
	}
}

func TestRuntimeControlJournalConditionallyFencesResumeCompleteRace(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 16, 20, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(6 * time.Minute) },
	}
	if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(
		ctx,
		runtimeControlClaim(started, "process-1", 1, "backend-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	channel := plan.Channel
	ready := contractsv1.RuntimeControlResumeReady{
		ServerConnectionID:              channel.OwnerID,
		AcknowledgedJournalRevision:     channel.ResumeClaimRevision,
		WorkerTXReplayedThroughSequence: channel.EventCommittedSequence,
		WorkerTXCursorDigest:            channel.EventCommittedDigest,
		WorkerRXReceivedThroughSequence: channel.CommandCommittedSequence,
		WorkerRXAppliedSequence:         channel.CommandAppliedSequence,
		WorkerRXCursorDigest:            channel.CommandCommittedDigest,
		ActiveTurn:                      channel.ActiveEffectSnapshot,
	}
	start := make(chan struct{})
	completeResult := make(chan error, 1)
	fenceResult := make(
		chan struct {
			state workerapplication.RuntimeControlChannelState
			err   error
		},
		1,
	)
	go func() {
		<-start
		_, completeErr := service.CompleteResume(
			ctx,
			workerapplication.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(channel),
				Ready:     ready,
			},
		)
		completeResult <- completeErr
	}()
	go func() {
		<-start
		state, fenceErr := service.Fence(
			ctx,
			workerapplication.FenceRuntimeControlChannelCommand{
				WorkerSessionID:    started.WorkerSessionID,
				RuntimeInstanceID:  started.RuntimeInstanceID,
				RuntimeGeneration:  started.RuntimeGeneration,
				ChannelEpoch:       started.ControlChannelEpoch,
				OnlyIfNotConnected: true,
				ConnectedBy:        now.Add(6 * time.Minute),
			},
		)
		fenceResult <- struct {
			state workerapplication.RuntimeControlChannelState
			err   error
		}{state: state, err: fenceErr}
	}()
	close(start)
	completeErr := <-completeResult
	fenced := <-fenceResult
	if fenced.err != nil {
		t.Fatal(fenced.err)
	}
	switch fenced.state.Status {
	case "connected":
		if completeErr != nil {
			t.Fatalf(
				"ResumeComplete won but failed: state=%+v err=%v",
				fenced.state,
				completeErr,
			)
		}
	case "fenced":
		if !errors.Is(
			completeErr,
			workerapplication.ErrRuntimeControlStaleOwner,
		) {
			t.Fatalf(
				"conditional fence won but ResumeComplete err=%v",
				completeErr,
			)
		}
	default:
		t.Fatalf(
			"ResumeComplete/fence race had no unique winner: state=%+v complete_err=%v",
			fenced.state,
			completeErr,
		)
	}
}

func TestRuntimeControlJournalConditionalFenceUsesResumeCompletionTime(t *testing.T) {
	t.Run("on-time completion survives later journal activity", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		base := time.Date(2026, 7, 26, 16, 25, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, base)
		completedAt := base.Add(6 * time.Minute)
		service, _ := bindAndClaimRuntimeControl(t, pool, started, completedAt)
		if _, err := pool.Exec(ctx, `
UPDATE runtime_control_channels
SET updated_at=$2
WHERE runtime_instance_id=$1`,
			started.RuntimeInstanceID,
			completedAt.Add(time.Minute),
		); err != nil {
			t.Fatal(err)
		}
		state, err := service.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{
			WorkerSessionID:    started.WorkerSessionID,
			RuntimeInstanceID:  started.RuntimeInstanceID,
			RuntimeGeneration:  started.RuntimeGeneration,
			ChannelEpoch:       started.ControlChannelEpoch,
			OnlyIfNotConnected: true,
			ConnectedBy:        completedAt.Add(time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		if state.Status != "connected" ||
			!state.ResumeCompletedAt.Equal(completedAt) {
			t.Fatalf("on-time completed channel was fenced: %+v", state)
		}
	})

	t.Run("late completion is fenced", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		base := time.Date(2026, 7, 26, 16, 35, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, base)
		deadline := base.Add(6 * time.Minute)
		service, _ := bindAndClaimRuntimeControl(
			t,
			pool,
			started,
			// PostgreSQL timestamptz has microsecond precision; stay
			// deliberately above its truncation boundary.
			deadline.Add(time.Millisecond),
		)
		state, err := service.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{
			WorkerSessionID:    started.WorkerSessionID,
			RuntimeInstanceID:  started.RuntimeInstanceID,
			RuntimeGeneration:  started.RuntimeGeneration,
			ChannelEpoch:       started.ControlChannelEpoch,
			OnlyIfNotConnected: true,
			ConnectedBy:        deadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		if state.Status != "fenced" {
			t.Fatalf("late completed channel escaped fence: %+v", state)
		}
	})
}

func TestRuntimeControlJournalFencesBoundAndHistoricalIdentity(t *testing.T) {
	t.Run("bound before claim", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 16, 30, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service := workerapplication.RuntimeControlJournalService{
			Store: workerpostgres.NewRuntimeControlJournal(pool),
			Now:   func() time.Time { return now.Add(6 * time.Minute) },
		}
		if _, err := service.Bind(ctx, runtimeControlBind(started)); err != nil {
			t.Fatal(err)
		}
		fenced, err := service.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{
			WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration, ChannelEpoch: started.ControlChannelEpoch,
		})
		if err != nil || fenced.Status != "fenced" || fenced.OwnerFence == 0 {
			t.Fatalf("fence bound channel=%+v err=%v", fenced, err)
		}
	})

	t.Run("historical after active binding cleared", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 16, 40, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service, _ := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
		if _, err := pool.Exec(ctx,
			`UPDATE runtime_instances SET status='unhealthy' WHERE runtime_instance_id=$1`,
			started.RuntimeInstanceID,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE worker_sessions SET active_runtime_instance_id=NULL WHERE worker_session_id=$1`,
			started.WorkerSessionID,
		); err != nil {
			t.Fatal(err)
		}
		fenced, err := service.Fence(ctx, workerapplication.FenceRuntimeControlChannelCommand{
			WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
			RuntimeGeneration: started.RuntimeGeneration, ChannelEpoch: started.ControlChannelEpoch,
		})
		if err != nil || fenced.Status != "fenced" {
			t.Fatalf("fence historical channel=%+v err=%v", fenced, err)
		}
	})
}

func TestRuntimeControlJournalPersistsAppliedCommandRange(t *testing.T) {
	t.Run("active start then non-start", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
		start, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "range-start", IdempotencyKey: "range-start",
			Envelope: runtimeControlEnvelope(started, "command", 0, "range-start", "range-start", "agent_turn.start"),
		})
		if err != nil {
			t.Fatal(err)
		}
		interrupt, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "range-interrupt", IdempotencyKey: "range-interrupt",
			Envelope: runtimeControlEnvelope(started, "command", 0, "range-interrupt", "range-interrupt", "agent_turn.interrupt"),
		})
		if err != nil {
			t.Fatal(err)
		}
		active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
		claim := runtimeControlClaim(started, "process-1", 2, "backend-2")
		claim.WorkerCommandLastSequence = interrupt.Sequence
		claim.WorkerCommandAppliedSequence = interrupt.Sequence
		claim.WorkerCommandCursorDigest = interrupt.CursorDigest
		claim.ActiveTurn = &active
		plan, err := service.Claim(ctx, claim)
		if err != nil || plan.Channel.CommandAppliedSequence != 2 {
			t.Fatalf("apply command range=%+v err=%v", plan, err)
		}
		var startStatus, interruptStatus string
		var startAppliedAt, interruptAppliedAt *time.Time
		if err := pool.QueryRow(ctx, `
SELECT first.effect_status,first.applied_at,second.effect_status,second.applied_at
FROM runtime_control_messages first
JOIN runtime_control_messages second ON second.runtime_instance_id=first.runtime_instance_id
WHERE first.runtime_instance_id=$1 AND first.direction='command' AND first.sequence=1
  AND second.direction='command' AND second.sequence=2`,
			started.RuntimeInstanceID,
		).Scan(&startStatus, &startAppliedAt, &interruptStatus, &interruptAppliedAt); err != nil {
			t.Fatal(err)
		}
		if startStatus != "active" || startAppliedAt == nil ||
			interruptStatus != "applied" || interruptAppliedAt == nil {
			t.Fatalf("command effects start=%s/%v interrupt=%s/%v",
				startStatus, startAppliedAt, interruptStatus, interruptAppliedAt)
		}
	})

	t.Run("non-start without active Turn", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 18, 10, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
		command, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "inspect-applied", IdempotencyKey: "inspect-applied",
			Envelope: runtimeControlEnvelope(started, "command", 0, "inspect-applied", "inspect-applied", "runtime.inspect"),
		})
		if err != nil {
			t.Fatal(err)
		}
		claim := runtimeControlClaim(started, "process-1", 2, "backend-2")
		claim.WorkerCommandLastSequence = command.Sequence
		claim.WorkerCommandAppliedSequence = command.Sequence
		claim.WorkerCommandCursorDigest = command.CursorDigest
		plan, err := service.Claim(ctx, claim)
		if err != nil || plan.Channel.CommandAppliedSequence != 1 {
			t.Fatalf("non-start applied=%+v err=%v", plan, err)
		}
	})

	t.Run("replayed non-start applied at ResumeReady", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 18, 15, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
		command, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "replayed-inspect", IdempotencyKey: "replayed-inspect",
			Envelope: runtimeControlEnvelope(
				started, "command", 0, "replayed-inspect", "replayed-inspect", "runtime.inspect",
			),
		})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := service.Claim(
			ctx,
			runtimeControlClaim(started, "process-1", 2, "backend-2"),
		)
		if err != nil || len(plan.PendingCommands) != 1 {
			t.Fatalf("replay plan=%+v err=%v", plan, err)
		}
		completed, err := service.CompleteResume(
			ctx,
			workerapplication.CompleteRuntimeControlResumeCommand{
				Authority: authorityFrom(plan.Channel),
				Ready: contractsv1.RuntimeControlResumeReady{
					ServerConnectionID:              plan.Channel.OwnerID,
					AcknowledgedJournalRevision:     plan.Channel.ResumeClaimRevision,
					WorkerTXReplayedThroughSequence: 0,
					WorkerTXCursorDigest:            contractsv1.RuntimeControlCursorGenesisDigest,
					WorkerRXReceivedThroughSequence: command.Sequence,
					WorkerRXAppliedSequence:         command.Sequence,
					WorkerRXCursorDigest:            command.CursorDigest,
				},
			},
		)
		if err != nil || completed.CommandAcknowledgedSequence != command.Sequence ||
			completed.CommandAppliedSequence != command.Sequence {
			t.Fatalf("complete replayed command=%+v err=%v", completed, err)
		}
	})

	t.Run("unproven start cannot be crossed", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 18, 20, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
		_, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "unproved-start", IdempotencyKey: "unproved-start",
			Envelope: runtimeControlEnvelope(started, "command", 0, "unproved-start", "unproved-start", "agent_turn.start"),
		})
		if err != nil {
			t.Fatal(err)
		}
		command, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "after-unproved", IdempotencyKey: "after-unproved",
			Envelope: runtimeControlEnvelope(started, "command", 0, "after-unproved", "after-unproved", "runtime.inspect"),
		})
		if err != nil {
			t.Fatal(err)
		}
		claim := runtimeControlClaim(started, "process-1", 2, "backend-2")
		claim.WorkerCommandLastSequence = 2
		claim.WorkerCommandAppliedSequence = 2
		claim.WorkerCommandCursorDigest = command.CursorDigest
		if _, err := service.Claim(ctx, claim); !errors.Is(err, workerapplication.ErrRuntimeControlCursorConflict) {
			t.Fatalf("unproved start cursor err=%v", err)
		}
	})

	t.Run("non-start before proven start", func(t *testing.T) {
		pool := postgrestest.Open(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 26, 18, 30, 0, 0, time.UTC)
		started, _ := startActiveRecoveryFixture(t, pool, now)
		service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
		first, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "before-start", IdempotencyKey: "before-start",
			Envelope: runtimeControlEnvelope(started, "command", 0, "before-start", "before-start", "runtime.inspect"),
		})
		if err != nil {
			t.Fatal(err)
		}
		start, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "second-start", IdempotencyKey: "second-start",
			Envelope: runtimeControlEnvelope(started, "command", 0, "second-start", "second-start", "agent_turn.start"),
		})
		if err != nil {
			t.Fatal(err)
		}
		firstClaim := runtimeControlClaim(started, "process-1", 2, "backend-2")
		firstClaim.WorkerCommandLastSequence = 2
		firstClaim.WorkerCommandAppliedSequence = 1
		firstClaim.WorkerCommandCursorDigest = start.CursorDigest
		firstPlan, err := service.Claim(ctx, firstClaim)
		if err != nil || firstPlan.Channel.CommandAppliedSequence != first.Sequence {
			t.Fatalf("first non-start cursor=%+v err=%v", firstPlan, err)
		}
		active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
		startClaim := firstClaim
		startClaim.StreamAttempt = 3
		startClaim.OwnerID = "backend-3"
		startClaim.WorkerCommandAppliedSequence = 2
		startClaim.ActiveTurn = &active
		startPlan, err := service.Claim(ctx, startClaim)
		if err != nil || !startPlan.TurnConsumed ||
			startPlan.Channel.CommandAppliedSequence != 2 {
			t.Fatalf("second start cursor=%+v err=%v", startPlan, err)
		}
	})
}

func TestRuntimeControlJournalAllowsNextTurnOnlyAfterExactTerminalProof(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	startOneEnvelope := runtimeControlEnvelope(
		started, "command", 1, "turn-one-start", "turn-one-start", "agent_turn.start",
	)
	startOne, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "turn-one-start",
		IdempotencyKey: "turn-one-start", Envelope: startOneEnvelope,
	})
	if err != nil {
		t.Fatal(err)
	}
	activeOne := runtimeControlActiveTurn(
		t, started, startOne, contractsv1.RuntimeControlTurnEffectActive,
	)
	activeClaim := runtimeControlClaim(started, "process-1", 2, "backend-2")
	activeClaim.WorkerCommandLastSequence = 1
	activeClaim.WorkerCommandAppliedSequence = 1
	activeClaim.WorkerCommandCursorDigest = startOne.CursorDigest
	activeClaim.ActiveTurn = &activeOne
	activePlan, err := service.Claim(ctx, activeClaim)
	if err != nil {
		t.Fatal(err)
	}
	authority = authorityFrom(activePlan.Channel)

	terminalEnvelope := runtimeControlEnvelopeWithAck(
		t, runtimeControlEnvelope(
			started, "event", 1, "turn-one-terminal", "turn-one-terminal", "agent_turn.terminal",
		), 1,
	)
	terminalDigest := runtimeControlEnvelopeFingerprint(t, terminalEnvelope)
	terminalCursor := runtimeControlCursor(
		t, contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionRuntimeToBackend, terminalEnvelope,
	)
	terminalProof := activeOne
	terminalProof.Phase = contractsv1.RuntimeControlTurnEffectTerminal
	terminalProof.LastUpdateSequence = 1
	terminalProof.TerminalEnvelopeDigest = terminalDigest
	terminalClaim := runtimeControlClaim(started, "process-1", 3, "backend-3")
	terminalClaim.WorkerCommandLastSequence = 1
	terminalClaim.WorkerCommandAppliedSequence = 1
	terminalClaim.WorkerCommandCursorDigest = startOne.CursorDigest
	terminalClaim.WorkerEventLastSequence = 1
	terminalClaim.WorkerEventCursorDigest = terminalCursor
	terminalClaim.ActiveTurn = &terminalProof
	terminalPlan, err := service.Claim(ctx, terminalClaim)
	if err != nil || !terminalPlan.TerminalProofPending ||
		terminalPlan.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectActive {
		t.Fatalf("terminal pending plan=%+v err=%v", terminalPlan, err)
	}
	authority = authorityFrom(terminalPlan.Channel)
	terminalAccepted, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "turn-one-terminal",
		IdempotencyKey: "turn-one-terminal", Envelope: terminalEnvelope,
	})
	if err != nil || terminalAccepted.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectTerminal ||
		terminalAccepted.Channel.TerminalProofPending {
		t.Fatalf("terminal accepted=%+v err=%v", terminalAccepted, err)
	}
	completeResumeCommand := workerapplication.CompleteRuntimeControlResumeCommand{
		Authority: authority,
		Ready: contractsv1.RuntimeControlResumeReady{
			ServerConnectionID:              "backend-3",
			AcknowledgedJournalRevision:     terminalPlan.Channel.Revision,
			WorkerTXReplayedThroughSequence: 1,
			WorkerTXCursorDigest:            terminalCursor,
			WorkerRXReceivedThroughSequence: 1,
			WorkerRXAppliedSequence:         1,
			WorkerRXCursorDigest:            startOne.CursorDigest,
			ActiveTurn:                      &terminalProof,
		},
	}
	if _, err := service.CompleteResume(
		ctx,
		completeResumeCommand,
	); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlPendingTurnSettlement,
	) {
		t.Fatalf("terminal settlement did not block ResumeComplete err=%v", err)
	}
	terminalSettlement := advanceTerminalSettlementToTaskSettled(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	service = workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(6*time.Minute + 5*time.Millisecond) },
	}
	if _, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "turn-one-terminal",
		IdempotencyKey: "turn-one-terminal/apply",
		Outcome: json.RawMessage(
			`{"status":"terminal_committed","effect_disposition":"commit"}`,
		),
		TurnSettlement: turnSettlementApplyAuthority(terminalSettlement),
	}); err != nil {
		t.Fatal(err)
	}
	completedSettlement := completeAppliedTerminalSettlement(
		t,
		pool,
		started,
		now.Add(6*time.Minute+6*time.Millisecond),
	)
	if completedSettlement.Phase != workerapplication.TurnSettlementCompleted {
		t.Fatalf("completed terminal settlement=%+v", completedSettlement)
	}
	completedResume, err := service.CompleteResume(
		ctx,
		completeResumeCommand,
	)
	if err != nil || completedResume.EventReplayTargetSequence != 0 {
		t.Fatalf("terminal resume complete=%+v err=%v", completedResume, err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='completed',revision=revision+1,terminal_at=$2
WHERE agent_turn_id=$1 AND status='consumed'`,
		started.AgentTurnID, now.Add(8*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at)
VALUES
  ('agent-turn-next',$1,$3,$4,'prompt','{}','delivered',1,'turn-next',
   $2::timestamptz,$2::timestamptz,$2::timestamptz+interval '24 hours')`,
		started.WorkerSessionID, now.Add(8*time.Minute), started.WorkerRunID, started.AgentSessionID,
	); err != nil {
		t.Fatal(err)
	}
	next := started
	next.AgentTurnID = "agent-turn-next"
	startTwoEnvelope := runtimeControlEnvelope(
		next, "command", 2, "turn-two-start", "turn-two-start", "agent_turn.start",
	)
	startTwo, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "turn-two-start",
		IdempotencyKey: "turn-two-start", Envelope: startTwoEnvelope,
	})
	if err != nil {
		t.Fatal(err)
	}
	activeTwo := runtimeControlActiveTurn(
		t, next, startTwo, contractsv1.RuntimeControlTurnEffectActive,
	)
	nextClaim := runtimeControlClaim(next, "process-1", 4, "backend-4")
	nextClaim.WorkerEventLastSequence = 1
	nextClaim.WorkerEventBackendAcknowledgedSequence = 1
	nextClaim.WorkerEventCursorDigest = terminalCursor
	nextClaim.WorkerCommandLastSequence = 2
	nextClaim.WorkerCommandAppliedSequence = 2
	nextClaim.WorkerCommandCursorDigest = startTwo.CursorDigest
	nextClaim.ActiveTurn = &activeTwo
	nextPlan, err := service.Claim(ctx, nextClaim)
	if err != nil || !nextPlan.TurnConsumed ||
		nextPlan.Channel.ActiveEffectAgentTurnID != next.AgentTurnID ||
		nextPlan.Channel.ActiveEffectCommandSequence != 2 {
		t.Fatalf("next Turn plan=%+v err=%v", nextPlan, err)
	}
}

func TestRuntimeControlJournalAdvancesLiveTerminalAndNextTurn(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 19, 0, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	start, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "live-start", IdempotencyKey: "live-start",
		Envelope: runtimeControlEnvelope(started, "command", 0, "live-start", "live-start", "agent_turn.start"),
	})
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
	if _, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: 1, MessageID: "live-start",
		AgentTurnID: started.AgentTurnID, Phase: workerapplication.RuntimeControlEffectActive,
		ActiveTurn: &active,
	}); err != nil {
		t.Fatalf("reconcile live active: %v", err)
	}
	terminalEnvelope := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(started, "event", 1, "live-terminal", "live-terminal", "agent_turn.terminal"),
		1,
	)
	terminalDigest := runtimeControlEnvelopeFingerprint(t, terminalEnvelope)
	_, err = service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "live-terminal",
		IdempotencyKey: "live-terminal", Envelope: terminalEnvelope,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*contractsv1.RuntimeControlActiveTurn){
		"digest": func(proof *contractsv1.RuntimeControlActiveTurn) {
			proof.StartEnvelopeDigest = contractsv1.RuntimeControlCursorGenesisDigest
		},
		"run": func(proof *contractsv1.RuntimeControlActiveTurn) {
			proof.WorkerRunID = contractsv1.WorkerRunID("worker-run-other")
		},
		"session": func(proof *contractsv1.RuntimeControlActiveTurn) {
			proof.AgentSessionID = contractsv1.AgentSessionID("agent-session-other")
		},
	} {
		proof := active
		mutate(&proof)
		if _, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
			Authority: authority, CommandSequence: 1, MessageID: "live-start",
			AgentTurnID: started.AgentTurnID, Phase: workerapplication.RuntimeControlEffectActive,
			ActiveTurn: &proof,
		}); !errors.Is(err, workerapplication.ErrRuntimeControlIdentityConflict) {
			t.Fatalf("tampered live %s proof err=%v", name, err)
		}
	}
	terminal, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: 1, MessageID: "live-start",
		AgentTurnID: started.AgentTurnID, Phase: workerapplication.RuntimeControlEffectTerminal,
		TerminalEventSequence: 1, TerminalEnvelopeDigest: terminalDigest,
		ActiveTurn: func() *contractsv1.RuntimeControlActiveTurn {
			proof := active
			proof.Phase = contractsv1.RuntimeControlTurnEffectTerminal
			proof.LastUpdateSequence = 1
			proof.TerminalEnvelopeDigest = terminalDigest
			return &proof
		}(),
	})
	if err != nil || terminal.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectTerminal {
		t.Fatalf("live terminal=%+v err=%v", terminal, err)
	}
	terminalSettlement := advanceTerminalSettlementToTaskSettled(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	service = workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(6*time.Minute + 5*time.Millisecond) },
	}
	if _, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "live-terminal",
		IdempotencyKey: "live-terminal/apply",
		Outcome: json.RawMessage(
			`{"status":"terminal_committed","effect_disposition":"commit"}`,
		),
		TurnSettlement: turnSettlementApplyAuthority(terminalSettlement),
	}); err != nil {
		t.Fatal(err)
	}
	completeAppliedTerminalSettlement(
		t,
		pool,
		started,
		now.Add(6*time.Minute+6*time.Millisecond),
	)
	if _, err := pool.Exec(ctx, `
UPDATE agent_turns SET status='completed',revision=revision+1,terminal_at=$2
WHERE agent_turn_id=$1 AND status='consumed'`,
		started.AgentTurnID, now.Add(8*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at)
VALUES ('agent-turn-live-next',$1,$3,$4,'prompt','{}','delivered',1,'live-next',
 $2::timestamptz,$2::timestamptz,$2::timestamptz+interval '24 hours')`,
		started.WorkerSessionID, now.Add(8*time.Minute), started.WorkerRunID, started.AgentSessionID,
	); err != nil {
		t.Fatal(err)
	}
	next := started
	next.AgentTurnID = "agent-turn-live-next"
	transportACK, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "live-transport-ack", IdempotencyKey: "live-transport-ack",
		Envelope: runtimeControlEnvelope(
			started, "ack", 0, "live-transport-ack", "live-transport-ack", "runtime.control.ack",
		),
	})
	if err != nil || transportACK.Sequence != 2 {
		t.Fatalf("reserve live transport ACK=%+v err=%v", transportACK, err)
	}
	nextStart, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "live-next-start", IdempotencyKey: "live-next-start",
		Envelope: runtimeControlEnvelope(next, "command", 0, "live-next-start", "live-next-start", "agent_turn.start"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nextActive := runtimeControlActiveTurn(t, next, nextStart, contractsv1.RuntimeControlTurnEffectActive)
	nextEffect, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: 3, MessageID: "live-next-start",
		AgentTurnID: next.AgentTurnID, Phase: workerapplication.RuntimeControlEffectActive,
		ActiveTurn: &nextActive,
	})
	if err != nil || !nextEffect.TurnConsumed ||
		nextEffect.Channel.ActiveEffectAgentTurnID != next.AgentTurnID ||
		nextEffect.Channel.CommandAppliedSequence != 3 {
		t.Fatalf("live next Turn=%+v err=%v", nextEffect, err)
	}
	var transportACKStatus string
	var transportACKAppliedAt *time.Time
	if err := pool.QueryRow(ctx, `
SELECT effect_status,applied_at
FROM runtime_control_messages
WHERE runtime_instance_id=$1 AND direction='command' AND sequence=2`,
		started.RuntimeInstanceID,
	).Scan(&transportACKStatus, &transportACKAppliedAt); err != nil {
		t.Fatal(err)
	}
	if transportACKStatus != "applied" || transportACKAppliedAt == nil {
		t.Fatalf("intervening ACK command was not proven applied: %s/%v",
			transportACKStatus, transportACKAppliedAt)
	}
}

func TestRuntimeControlJournalAtomicallyAppliesTurnEffectsWithInboxOutcome(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 19, 15, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	start, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "atomic-start", IdempotencyKey: "atomic-start",
		Envelope: runtimeControlEnvelope(
			started,
			"command",
			0,
			"atomic-start",
			"atomic-start",
			"agent_turn.start",
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
	activePayload, err := json.Marshal(struct {
		ActiveTurn contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
	}{ActiveTurn: active})
	if err != nil {
		t.Fatal(err)
	}
	var activeEnvelope contractsv1.RuntimeControlEnvelope
	if err := json.Unmarshal(
		runtimeControlEnvelopeWithAck(
			t,
			runtimeControlEnvelope(
				started,
				"event",
				1,
				"atomic-active",
				"atomic-active",
				"agent_turn.effect",
			),
			start.Sequence,
		),
		&activeEnvelope,
	); err != nil {
		t.Fatal(err)
	}
	activeEnvelope.Payload = activePayload
	encodedActive, err := json.Marshal(activeEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "atomic-active",
		IdempotencyKey: "atomic-active", Envelope: encodedActive,
	}); err != nil {
		t.Fatal(err)
	}
	activeApplied, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "atomic-active",
		IdempotencyKey: "atomic-active",
		Outcome: json.RawMessage(
			`{"status":"effect_committed","effect_disposition":"commit"}`,
		),
	})
	if err != nil ||
		activeApplied.Channel.EventAppliedSequence != 1 ||
		activeApplied.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectActive ||
		activeApplied.Channel.ActiveEffectAgentTurnID != started.AgentTurnID {
		t.Fatalf("atomic active Apply=%+v err=%v", activeApplied, err)
	}
	var turnStatus, startEffect, activeEventEffect string
	if err := pool.QueryRow(ctx, `
SELECT turn.status,start.effect_status,event.effect_status
FROM agent_turns turn
JOIN runtime_control_messages start
  ON start.runtime_instance_id=$2 AND start.direction='command' AND start.sequence=1
JOIN runtime_control_messages event
  ON event.runtime_instance_id=$2 AND event.direction='event' AND event.sequence=1
WHERE turn.agent_turn_id=$1`,
		started.AgentTurnID,
		started.RuntimeInstanceID,
	).Scan(&turnStatus, &startEffect, &activeEventEffect); err != nil {
		t.Fatal(err)
	}
	if turnStatus != "consumed" || startEffect != "active" || activeEventEffect != "applied" {
		t.Fatalf(
			"atomic active state turn=%s start=%s event=%s",
			turnStatus,
			startEffect,
			activeEventEffect,
		)
	}

	terminalEnvelope := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(
			started,
			"event",
			2,
			"atomic-terminal",
			"atomic-terminal",
			"agent_turn.terminal",
		),
		start.Sequence,
	)
	if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 2, MessageID: "atomic-terminal",
		IdempotencyKey: "atomic-terminal", Envelope: terminalEnvelope,
	}); err != nil {
		t.Fatal(err)
	}
	terminalSettlement := advanceTerminalSettlementToTaskSettled(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	terminalApplied, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 2, MessageID: "atomic-terminal",
		IdempotencyKey: "atomic-terminal",
		Outcome: json.RawMessage(
			`{"status":"terminal_committed","effect_disposition":"commit"}`,
		),
		TurnSettlement: turnSettlementApplyAuthority(terminalSettlement),
	})
	if err != nil ||
		terminalApplied.Channel.EventAppliedSequence != 2 ||
		terminalApplied.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectTerminal ||
		terminalApplied.Channel.ActiveEffectAgentTurnID != started.AgentTurnID {
		t.Fatalf("atomic terminal Apply=%+v err=%v", terminalApplied, err)
	}
}

func TestRuntimeControlJournalAppliesHistoricalTerminalInboxWithoutRewindingHigherTurn(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 19, 30, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	start, err := service.ReserveCommand(ctx, workerapplication.ReserveRuntimeControlCommandCommand{
		Authority: authority, MessageID: "historical-start", IdempotencyKey: "historical-start",
		Envelope: runtimeControlEnvelope(
			started,
			"command",
			0,
			"historical-start",
			"historical-start",
			"agent_turn.start",
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	active := runtimeControlActiveTurn(t, started, start, contractsv1.RuntimeControlTurnEffectActive)
	if _, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: start.Sequence, MessageID: start.MessageID,
		AgentTurnID: started.AgentTurnID, Phase: workerapplication.RuntimeControlEffectActive,
		ActiveTurn: &active,
	}); err != nil {
		t.Fatal(err)
	}
	terminalEnvelope := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(
			started,
			"event",
			1,
			"historical-terminal",
			"historical-terminal",
			"agent_turn.terminal",
		),
		start.Sequence,
	)
	terminalDigest := runtimeControlEnvelopeFingerprint(t, terminalEnvelope)
	if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "historical-terminal",
		IdempotencyKey: "historical-terminal", Envelope: terminalEnvelope,
	}); err != nil {
		t.Fatal(err)
	}
	terminalSettlement := advanceTerminalSettlementToTaskSettled(
		t,
		pool,
		started,
		now.Add(6*time.Minute),
	)
	terminalProof := active
	terminalProof.Phase = contractsv1.RuntimeControlTurnEffectTerminal
	terminalProof.LastUpdateSequence = 1
	terminalProof.TerminalEnvelopeDigest = terminalDigest
	if _, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: start.Sequence, MessageID: start.MessageID,
		AgentTurnID: started.AgentTurnID, Phase: workerapplication.RuntimeControlEffectTerminal,
		TerminalEventSequence: 1, TerminalEnvelopeDigest: terminalDigest,
		ActiveTurn: &terminalProof,
	}); err != nil {
		t.Fatal(err)
	}
	service = workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now.Add(6*time.Minute + 5*time.Millisecond) },
	}
	if _, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "historical-terminal",
		IdempotencyKey: "historical-terminal/apply",
		Outcome: json.RawMessage(
			`{"status":"stage_committed","effect_disposition":"commit"}`,
		),
		TurnSettlement: turnSettlementApplyAuthority(terminalSettlement),
	}); err != nil {
		t.Fatalf("apply terminal before higher Turn: %v", err)
	}
	completeAppliedTerminalSettlement(
		t,
		pool,
		started,
		now.Add(6*time.Minute+6*time.Millisecond),
	)
	if _, err := pool.Exec(ctx, `
UPDATE agent_turns SET status='completed',revision=revision+1,terminal_at=$2
WHERE agent_turn_id=$1 AND status='consumed'`,
		started.AgentTurnID,
		now.Add(8*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_turns
  (agent_turn_id,worker_session_id,worker_run_id,agent_session_id,mode,content,status,
   revision,idempotency_key,created_at,started_at,deadline_at)
VALUES ('agent-turn-higher',$1,$3,$4,'prompt','{}','delivered',1,'turn-higher',
 $2::timestamptz,$2::timestamptz,$2::timestamptz+interval '24 hours')`,
		started.WorkerSessionID,
		now.Add(8*time.Minute),
		started.WorkerRunID,
		started.AgentSessionID,
	); err != nil {
		t.Fatal(err)
	}
	higher := started
	higher.AgentTurnID = "agent-turn-higher"
	higherStart, err := service.ReserveCommand(
		ctx,
		workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "higher-start", IdempotencyKey: "higher-start",
			Envelope: runtimeControlEnvelope(
				higher,
				"command",
				0,
				"higher-start",
				"higher-start",
				"agent_turn.start",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	higherActive := runtimeControlActiveTurn(
		t,
		higher,
		higherStart,
		contractsv1.RuntimeControlTurnEffectActive,
	)
	if _, err := service.ReconcileEffect(ctx, workerapplication.ReconcileRuntimeControlEffectCommand{
		Authority: authority, CommandSequence: higherStart.Sequence, MessageID: higherStart.MessageID,
		AgentTurnID: higher.AgentTurnID, Phase: workerapplication.RuntimeControlEffectActive,
		ActiveTurn: &higherActive,
	}); err != nil {
		t.Fatal(err)
	}

	applied, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "historical-terminal",
		IdempotencyKey: "historical-terminal/apply",
		Outcome: json.RawMessage(
			`{"status":"stage_committed","effect_disposition":"commit"}`,
		),
	})
	if err != nil {
		t.Fatalf("apply historical terminal inbox: %v", err)
	}
	if applied.Channel.EventAppliedSequence != 1 ||
		applied.Channel.ActiveEffectAgentTurnID != higher.AgentTurnID ||
		applied.Channel.ActiveEffectCommandSequence != higherStart.Sequence ||
		applied.Channel.ActiveEffectPhase != workerapplication.RuntimeControlEffectActive {
		t.Fatalf("historical Apply rewound higher Turn: %+v", applied.Channel)
	}
	duplicate, err := service.ApplyEvent(ctx, workerapplication.ApplyRuntimeControlEventCommand{
		Authority: authority, Sequence: 1, MessageID: "historical-terminal",
		IdempotencyKey: "historical-terminal/apply",
		Outcome: json.RawMessage(
			`{"status":"stage_committed","effect_disposition":"commit"}`,
		),
	})
	if err != nil || !duplicate.Message.Duplicate ||
		duplicate.Channel.ActiveEffectAgentTurnID != higher.AgentTurnID {
		t.Fatalf("historical duplicate=%+v err=%v", duplicate, err)
	}
}

func TestRuntimeControlJournalRejectsConflictingOrNonEventTerminalProof(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(
			*testing.T,
			context.Context,
			workerapplication.RuntimeControlJournalService,
			workerapplication.StartedWorkerRun,
			workerapplication.RuntimeControlAuthority,
			workerapplication.RuntimeControlMessage,
			contractsv1.RuntimeControlActiveTurn,
		)
	}{
		{
			name: "heartbeat cannot prove terminal",
			run: func(
				t *testing.T,
				ctx context.Context,
				service workerapplication.RuntimeControlJournalService,
				started workerapplication.StartedWorkerRun,
				authority workerapplication.RuntimeControlAuthority,
				start workerapplication.RuntimeControlMessage,
				active contractsv1.RuntimeControlActiveTurn,
			) {
				envelope := runtimeControlEnvelopeWithAck(
					t,
					runtimeControlEnvelope(
						started, "heartbeat", 1, "fake-terminal", "fake-terminal", "agent_turn.terminal",
					),
					start.Sequence,
				)
				digest := runtimeControlEnvelopeFingerprint(t, envelope)
				if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
					Authority: authority, Sequence: 1, MessageID: "fake-terminal",
					IdempotencyKey: "fake-terminal", Envelope: envelope,
				}); err != nil {
					t.Fatal(err)
				}
				proof := active
				proof.Phase = contractsv1.RuntimeControlTurnEffectTerminal
				proof.LastUpdateSequence = 1
				proof.TerminalEnvelopeDigest = digest
				if _, err := service.ReconcileEffect(
					ctx,
					workerapplication.ReconcileRuntimeControlEffectCommand{
						Authority: authority, CommandSequence: start.Sequence,
						MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
						Phase:                 workerapplication.RuntimeControlEffectTerminal,
						TerminalEventSequence: 1, TerminalEnvelopeDigest: digest,
						ActiveTurn: &proof,
					},
				); !errors.Is(err, workerapplication.ErrRuntimeControlMessageConflict) {
					t.Fatalf("heartbeat terminal proof err=%v", err)
				}
			},
		},
		{
			name: "different terminal is not an exact duplicate",
			run: func(
				t *testing.T,
				ctx context.Context,
				service workerapplication.RuntimeControlJournalService,
				started workerapplication.StartedWorkerRun,
				authority workerapplication.RuntimeControlAuthority,
				start workerapplication.RuntimeControlMessage,
				active contractsv1.RuntimeControlActiveTurn,
			) {
				firstEnvelope := runtimeControlEnvelopeWithAck(
					t,
					runtimeControlEnvelope(
						started, "event", 1, "terminal-1", "terminal-1", "agent_turn.terminal",
					),
					start.Sequence,
				)
				firstDigest := runtimeControlEnvelopeFingerprint(t, firstEnvelope)
				if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
					Authority: authority, Sequence: 1, MessageID: "terminal-1",
					IdempotencyKey: "terminal-1", Envelope: firstEnvelope,
				}); err != nil {
					t.Fatal(err)
				}
				firstProof := active
				firstProof.Phase = contractsv1.RuntimeControlTurnEffectTerminal
				firstProof.LastUpdateSequence = 1
				firstProof.TerminalEnvelopeDigest = firstDigest
				if _, err := service.ReconcileEffect(
					ctx,
					workerapplication.ReconcileRuntimeControlEffectCommand{
						Authority: authority, CommandSequence: start.Sequence,
						MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
						Phase:                 workerapplication.RuntimeControlEffectTerminal,
						TerminalEventSequence: 1, TerminalEnvelopeDigest: firstDigest,
						ActiveTurn: &firstProof,
					},
				); err != nil {
					t.Fatalf("first terminal proof: %v", err)
				}
				secondEnvelope := runtimeControlEnvelopeWithAck(
					t,
					runtimeControlEnvelope(
						started, "event", 2, "terminal-2", "terminal-2", "agent_turn.terminal",
					),
					start.Sequence,
				)
				if _, err := service.AcceptEvent(ctx, workerapplication.AcceptRuntimeControlEventCommand{
					Authority: authority, Sequence: 2, MessageID: "terminal-2",
					IdempotencyKey: "terminal-2", Envelope: secondEnvelope,
				}); !errors.Is(err, workerapplication.ErrTurnSettlementConflict) {
					t.Fatalf("conflicting terminal acceptance error=%v", err)
				}
			},
		},
		{
			name: "resume cannot rewrite a final terminal proof",
			run: func(
				t *testing.T,
				ctx context.Context,
				service workerapplication.RuntimeControlJournalService,
				started workerapplication.StartedWorkerRun,
				authority workerapplication.RuntimeControlAuthority,
				start workerapplication.RuntimeControlMessage,
				active contractsv1.RuntimeControlActiveTurn,
			) {
				envelope := runtimeControlEnvelopeWithAck(
					t,
					runtimeControlEnvelope(
						started,
						"event",
						1,
						"resume-terminal-1",
						"resume-terminal-1",
						"agent_turn.terminal",
					),
					start.Sequence,
				)
				digest := runtimeControlEnvelopeFingerprint(t, envelope)
				lastAccepted, err := service.AcceptEvent(
					ctx,
					workerapplication.AcceptRuntimeControlEventCommand{
						Authority: authority, Sequence: 1, MessageID: "resume-terminal-1",
						IdempotencyKey: "resume-terminal-1", Envelope: envelope,
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				proof := active
				proof.Phase = contractsv1.RuntimeControlTurnEffectTerminal
				proof.LastUpdateSequence = 1
				proof.TerminalEnvelopeDigest = digest
				if _, err := service.ReconcileEffect(
					ctx,
					workerapplication.ReconcileRuntimeControlEffectCommand{
						Authority: authority, CommandSequence: start.Sequence,
						MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
						Phase:                 workerapplication.RuntimeControlEffectTerminal,
						TerminalEventSequence: 1, TerminalEnvelopeDigest: digest,
						ActiveTurn: &proof,
					},
				); err != nil {
					t.Fatal(err)
				}
				replacement := proof
				replacement.LastUpdateSequence = 2
				replacement.TerminalEnvelopeDigest = contractsv1.RuntimeControlCursorGenesisDigest
				claim := runtimeControlClaim(started, "process-1", 2, "backend-resume")
				claim.WorkerEventLastSequence = 1
				claim.WorkerEventBackendAcknowledgedSequence = 1
				claim.WorkerEventCursorDigest = lastAccepted.Message.CursorDigest
				claim.WorkerCommandLastSequence = start.Sequence
				claim.WorkerCommandAppliedSequence = start.Sequence
				claim.WorkerCommandCursorDigest = start.CursorDigest
				claim.ActiveTurn = &replacement
				if _, err := service.Claim(ctx, claim); !errors.Is(
					err,
					workerapplication.ErrRuntimeControlPendingTurnSettlement,
				) {
					t.Fatalf("replacement terminal ResumeHello err=%v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, 7, 26, 19, 30, 0, 0, time.UTC)
			started, _ := startActiveRecoveryFixture(t, pool, now)
			service, authority := bindAndClaimRuntimeControl(
				t, pool, started, now.Add(6*time.Minute),
			)
			start, err := service.ReserveCommand(
				ctx,
				workerapplication.ReserveRuntimeControlCommandCommand{
					Authority: authority, MessageID: "terminal-start",
					IdempotencyKey: "terminal-start",
					Envelope: runtimeControlEnvelope(
						started, "command", 0, "terminal-start", "terminal-start", "agent_turn.start",
					),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			active := runtimeControlActiveTurn(
				t, started, start, contractsv1.RuntimeControlTurnEffectActive,
			)
			if _, err := service.ReconcileEffect(
				ctx,
				workerapplication.ReconcileRuntimeControlEffectCommand{
					Authority: authority, CommandSequence: start.Sequence,
					MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
					Phase:      workerapplication.RuntimeControlEffectActive,
					ActiveTurn: &active,
				},
			); err != nil {
				t.Fatal(err)
			}
			test.run(t, ctx, service, started, authority, start, active)
		})
	}
}

func TestRuntimeControlJournalRejectsNonterminalProofAfterDomainTurnTerminal(t *testing.T) {
	for _, phase := range []workerapplication.RuntimeControlEffectPhase{
		workerapplication.RuntimeControlEffectReceived,
		workerapplication.RuntimeControlEffectPromptInvocationIntent,
		workerapplication.RuntimeControlEffectActive,
	} {
		t.Run("live "+string(phase), func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, 7, 26, 19, 40, 0, 0, time.UTC)
			started, _ := startActiveRecoveryFixture(t, pool, now)
			service, authority := bindAndClaimRuntimeControl(
				t, pool, started, now.Add(6*time.Minute),
			)
			start, err := service.ReserveCommand(
				ctx,
				workerapplication.ReserveRuntimeControlCommandCommand{
					Authority: authority, MessageID: "late-live-start",
					IdempotencyKey: "late-live-start",
					Envelope: runtimeControlEnvelope(
						started, "command", 0, "late-live-start", "late-live-start", "agent_turn.start",
					),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='completed',terminal_at=$2,revision=revision+1
WHERE agent_turn_id=$1`,
				started.AgentTurnID, now.Add(7*time.Minute),
			); err != nil {
				t.Fatal(err)
			}
			proof := runtimeControlActiveTurn(
				t, started, start, contractsv1.RuntimeControlTurnEffectActive,
			)
			proof.Phase = string(phase)
			if _, err := service.ReconcileEffect(
				ctx,
				workerapplication.ReconcileRuntimeControlEffectCommand{
					Authority: authority, CommandSequence: start.Sequence,
					MessageID: start.MessageID, AgentTurnID: started.AgentTurnID,
					Phase: phase, ActiveTurn: &proof,
				},
			); !errors.Is(err, workerapplication.ErrRuntimeControlIdentityConflict) {
				t.Fatalf("late live %s proof err=%v", phase, err)
			}
		})

		t.Run("resume "+string(phase), func(t *testing.T) {
			pool := postgrestest.Open(t)
			ctx := context.Background()
			now := time.Date(2026, 7, 26, 19, 45, 0, 0, time.UTC)
			started, _ := startActiveRecoveryFixture(t, pool, now)
			service, authority := bindAndClaimRuntimeControl(
				t, pool, started, now.Add(6*time.Minute),
			)
			start, err := service.ReserveCommand(
				ctx,
				workerapplication.ReserveRuntimeControlCommandCommand{
					Authority: authority, MessageID: "late-resume-start",
					IdempotencyKey: "late-resume-start",
					Envelope: runtimeControlEnvelope(
						started, "command", 0, "late-resume-start", "late-resume-start", "agent_turn.start",
					),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
UPDATE agent_turns
SET status='completed',terminal_at=$2,revision=revision+1
WHERE agent_turn_id=$1`,
				started.AgentTurnID, now.Add(7*time.Minute),
			); err != nil {
				t.Fatal(err)
			}
			proof := runtimeControlActiveTurn(
				t, started, start, contractsv1.RuntimeControlTurnEffectActive,
			)
			proof.Phase = string(phase)
			claim := runtimeControlClaim(started, "process-1", 2, "backend-late-resume")
			claim.WorkerCommandLastSequence = start.Sequence
			claim.WorkerCommandCursorDigest = start.CursorDigest
			if phase == workerapplication.RuntimeControlEffectActive {
				claim.WorkerCommandAppliedSequence = start.Sequence
			}
			claim.ActiveTurn = &proof
			if _, err := service.Claim(ctx, claim); !errors.Is(
				err,
				workerapplication.ErrRuntimeControlIdentityConflict,
			) {
				t.Fatalf("late resume %s proof err=%v", phase, err)
			}
		})
	}
}

func TestRuntimeControlJournalKeepsPendingTerminalDeclarationExactAcrossClaims(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 19, 50, 0, 0, time.UTC)
	started, _ := startActiveRecoveryFixture(t, pool, now)
	service, authority := bindAndClaimRuntimeControl(t, pool, started, now.Add(6*time.Minute))
	start, err := service.ReserveCommand(
		ctx,
		workerapplication.ReserveRuntimeControlCommandCommand{
			Authority: authority, MessageID: "pending-terminal-start",
			IdempotencyKey: "pending-terminal-start",
			Envelope: runtimeControlEnvelope(
				started, "command", 0, "pending-terminal-start", "pending-terminal-start", "agent_turn.start",
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstTerminal := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(
			started, "event", 1, "pending-terminal-1", "pending-terminal-1", "agent_turn.terminal",
		),
		start.Sequence,
	)
	firstDigest := runtimeControlEnvelopeFingerprint(t, firstTerminal)
	firstCursor := runtimeControlCursor(
		t,
		contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionRuntimeToBackend,
		firstTerminal,
	)
	firstProof := runtimeControlActiveTurn(
		t, started, start, contractsv1.RuntimeControlTurnEffectTerminal,
	)
	firstProof.LastUpdateSequence = 1
	firstProof.TerminalEnvelopeDigest = firstDigest
	firstClaim := runtimeControlClaim(started, "process-1", 2, "backend-pending-1")
	firstClaim.WorkerEventLastSequence = 1
	firstClaim.WorkerEventCursorDigest = firstCursor
	firstClaim.WorkerCommandLastSequence = start.Sequence
	firstClaim.WorkerCommandAppliedSequence = start.Sequence
	firstClaim.WorkerCommandCursorDigest = start.CursorDigest
	firstClaim.ActiveTurn = &firstProof
	firstPlan, err := service.Claim(ctx, firstClaim)
	if err != nil || !firstPlan.TerminalProofPending ||
		!reflect.DeepEqual(firstPlan.ReportedActiveTurn, &firstProof) {
		t.Fatalf("first pending terminal plan=%+v err=%v", firstPlan, err)
	}

	activeRegression := firstClaim
	activeRegression.StreamAttempt = 3
	activeRegression.OwnerID = "backend-pending-active"
	activeProof := runtimeControlActiveTurn(
		t, started, start, contractsv1.RuntimeControlTurnEffectActive,
	)
	activeProof.LastUpdateSequence = firstProof.LastUpdateSequence
	activeRegression.ActiveTurn = &activeProof
	if _, err := service.Claim(ctx, activeRegression); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlMessageConflict,
	) {
		t.Fatalf("pending terminal regressed to active err=%v", err)
	}

	secondTerminal := runtimeControlEnvelopeWithAck(
		t,
		runtimeControlEnvelope(
			started, "event", 2, "pending-terminal-2", "pending-terminal-2", "agent_turn.terminal",
		),
		start.Sequence,
	)
	secondCursor := runtimeControlCursor(
		t,
		firstCursor,
		contractsv1.RuntimeControlDirectionRuntimeToBackend,
		secondTerminal,
	)
	secondProof := firstProof
	secondProof.LastUpdateSequence = 2
	secondProof.TerminalEnvelopeDigest = runtimeControlEnvelopeFingerprint(t, secondTerminal)
	differentTerminal := firstClaim
	differentTerminal.StreamAttempt = 3
	differentTerminal.OwnerID = "backend-pending-2"
	differentTerminal.WorkerEventLastSequence = 2
	differentTerminal.WorkerEventCursorDigest = secondCursor
	differentTerminal.ActiveTurn = &secondProof
	if _, err := service.Claim(ctx, differentTerminal); !errors.Is(
		err,
		workerapplication.ErrRuntimeControlMessageConflict,
	) {
		t.Fatalf("pending terminal declaration changed err=%v", err)
	}
}

func runtimeControlBind(started workerapplication.StartedWorkerRun) workerapplication.BindRuntimeControlChannelCommand {
	return workerapplication.BindRuntimeControlChannelCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, Credential: "runtime-control-secret",
	}
}

func runtimeControlClaim(
	started workerapplication.StartedWorkerRun,
	processNonce string,
	streamAttempt uint64,
	ownerID string,
) workerapplication.ClaimRuntimeControlStreamCommand {
	return workerapplication.ClaimRuntimeControlStreamCommand{
		WorkerSessionID: started.WorkerSessionID, RuntimeInstanceID: started.RuntimeInstanceID,
		RuntimeGeneration: 1, ChannelEpoch: 1, Credential: "runtime-control-secret",
		ProcessNonce: processNonce, StreamAttempt: streamAttempt, OwnerID: ownerID,
		WorkerEventCursorDigest:   contractsv1.RuntimeControlCursorGenesisDigest,
		WorkerCommandCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest,
	}
}

func bindAndClaimRuntimeControl(
	t *testing.T,
	pool *pgxpool.Pool,
	started workerapplication.StartedWorkerRun,
	now time.Time,
) (workerapplication.RuntimeControlJournalService, workerapplication.RuntimeControlAuthority) {
	t.Helper()
	service := workerapplication.RuntimeControlJournalService{
		Store: workerpostgres.NewRuntimeControlJournal(pool),
		Now:   func() time.Time { return now },
	}
	if _, err := service.Bind(context.Background(), runtimeControlBind(started)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Claim(context.Background(), runtimeControlClaim(started, "process-1", 1, "backend-1"))
	if err != nil {
		t.Fatal(err)
	}
	completed := completeRuntimeControlClaim(t, service, plan)
	return service, authorityFrom(completed)
}

func completeRuntimeControlClaim(
	t *testing.T,
	service workerapplication.RuntimeControlJournalService,
	plan workerapplication.RuntimeControlReplayPlan,
) workerapplication.RuntimeControlChannelState {
	t.Helper()
	channel := plan.Channel
	completed, err := service.CompleteResume(
		context.Background(),
		workerapplication.CompleteRuntimeControlResumeCommand{
			Authority: authorityFrom(channel),
			Ready: contractsv1.RuntimeControlResumeReady{
				ServerConnectionID:              channel.OwnerID,
				AcknowledgedJournalRevision:     channel.ResumeClaimRevision,
				WorkerTXReplayedThroughSequence: channel.EventCommittedSequence,
				WorkerTXCursorDigest:            channel.EventCommittedDigest,
				WorkerRXReceivedThroughSequence: channel.CommandCommittedSequence,
				WorkerRXAppliedSequence:         channel.CommandAppliedSequence,
				WorkerRXCursorDigest:            channel.CommandCommittedDigest,
				ActiveTurn:                      channel.ActiveEffectSnapshot,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func authorityFrom(channel workerapplication.RuntimeControlChannelState) workerapplication.RuntimeControlAuthority {
	return workerapplication.RuntimeControlAuthority{
		WorkerSessionID: channel.WorkerSessionID, RuntimeInstanceID: channel.RuntimeInstanceID,
		RuntimeGeneration: channel.RuntimeGeneration, ChannelEpoch: channel.ChannelEpoch,
		ProcessNonce: channel.ProcessNonce, StreamAttempt: channel.StreamAttempt,
		OwnerFence: channel.OwnerFence,
	}
}

func runtimeControlEnvelope(
	started workerapplication.StartedWorkerRun,
	kind string,
	sequence uint64,
	messageID string,
	idempotencyKey string,
	messageType string,
) json.RawMessage {
	envelope := contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		MessageID:         messageID,
		Kind:              kind,
		WorkerSessionID:   contractsv1.WorkerSessionID(started.WorkerSessionID),
		WorkerRunID:       contractsv1.WorkerRunID(started.WorkerRunID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(started.RuntimeInstanceID),
		RuntimeGeneration: uint32(started.RuntimeGeneration),
		AgentSessionID:    contractsv1.AgentSessionID(started.AgentSessionID),
		AgentTurnID:       contractsv1.AgentTurnID(started.AgentTurnID),
		ChannelEpoch:      started.ControlChannelEpoch,
		Sequence:          sequence,
		IdempotencyKey:    idempotencyKey,
		MessageType:       messageType,
		Payload:           json.RawMessage(`{}`),
		SentAt:            time.Date(2026, 7, 26, 12, 0, int(sequence), 0, time.UTC),
	}
	if kind == "command" || kind == "ack" {
		envelope.Sequence = 0
		envelope.AckSequence = 0
		envelope.SentAt = time.Time{}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return encoded
}

func runtimeControlCursor(
	t *testing.T,
	previous string,
	direction string,
	encoded json.RawMessage,
) string {
	t.Helper()
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(encoded)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := contractsv1.AdvanceRuntimeControlCursor(previous, direction, envelope)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func runtimeControlActiveTurn(
	t *testing.T,
	started workerapplication.StartedWorkerRun,
	message workerapplication.RuntimeControlMessage,
	phase string,
) contractsv1.RuntimeControlActiveTurn {
	t.Helper()
	return contractsv1.RuntimeControlActiveTurn{
		WorkerRunID:          contractsv1.WorkerRunID(started.WorkerRunID),
		AgentSessionID:       contractsv1.AgentSessionID(started.AgentSessionID),
		AgentTurnID:          contractsv1.AgentTurnID(started.AgentTurnID),
		StartCommandSequence: message.Sequence,
		StartMessageID:       message.MessageID,
		StartIdempotencyKey:  message.IdempotencyKey,
		StartEnvelopeDigest:  message.Fingerprint,
		Phase:                phase,
	}
}

func decodeRuntimeControlEnvelope(
	t *testing.T,
	encoded json.RawMessage,
) contractsv1.RuntimeControlEnvelope {
	t.Helper()
	envelope, err := contractsv1.DecodeRuntimeControlEnvelope(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func runtimeControlEnvelopeWithAck(
	t *testing.T,
	encoded json.RawMessage,
	ackSequence uint64,
) json.RawMessage {
	t.Helper()
	envelope := decodeRuntimeControlEnvelope(t, encoded)
	envelope.AckSequence = ackSequence
	updated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func runtimeControlEnvelopeFingerprint(t *testing.T, encoded json.RawMessage) string {
	t.Helper()
	envelope := decodeRuntimeControlEnvelope(t, encoded)
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
