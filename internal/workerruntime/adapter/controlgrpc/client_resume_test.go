package controlgrpc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestReconnectingClientDoesNotDeliverCommandsBeforeResumeComplete(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	runCtx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()

	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	if hello == nil || hello.StreamAttempt != 1 {
		t.Fatalf("first frame = %+v, want attempt-1 ResumeHello", hello)
	}

	command := testClientCommand(client.credential.Identity, 1, 0, "command-1")
	commandDigest := advanceClientCursor(t, contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime, command)
	attempt.receive(resumeAckFrame(contractsv1.RuntimeControlResumeAck{
		Accepted:                     true,
		ServerConnectionID:           "connection-1",
		JournalRevision:              1,
		BackendRXCursorDigest:        contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXLastCommandSequence: 1,
		BackendTXCursorDigest:        commandDigest,
		ReplayWorkerFromSequence:     1,
		ReplayBackendFromSequence:    1,
		ActiveTurnDisposition:        contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	attempt.receive(envelopeClientFrame(command))

	ready := receiveSentFrame(t, attempt).ResumeReady
	if ready == nil || ready.WorkerRXReceivedThroughSequence != 1 {
		t.Fatalf("frame = %+v, want ResumeReady through command 1", ready)
	}
	select {
	case delivered := <-client.Commands():
		t.Fatalf("command crossed ResumeComplete barrier: %+v", delivered)
	default:
	}

	attempt.receive(resumeCompleteFrame(contractsv1.RuntimeControlResumeComplete{
		ServerConnectionID:           "connection-1",
		JournalRevision:              2,
		BackendRXCursorDigest:        contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXLastCommandSequence: 1,
		BackendTXWorkerAckedSequence: 1,
		BackendTXCursorDigest:        commandDigest,
		ActiveTurnDisposition:        contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	select {
	case delivered := <-client.Commands():
		if delivered.Envelope.MessageID != "command-1" {
			t.Fatalf("delivered command = %+v", delivered.Envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("command was not delivered after ResumeComplete")
	}
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready ||
			transition.StreamAttempt != 1 ||
			transition.ActiveTurnDisposition != contractsv1.RuntimeControlResumeNoActiveTurn {
			t.Fatalf("ready transition = %+v", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("missing ready transition")
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestReconnectingClientEmitsReadyTransitionForCompletedUnprovenResume(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	runCtx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()

	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	if hello == nil || hello.ActiveTurn != nil {
		t.Fatalf("first frame=%+v, want idle ResumeHello", hello)
	}
	ack := emptyClientResumeAck("connection-1", hello.WorkerTXCursorDigest)
	ack.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	attempt.receive(resumeAckFrame(ack))
	if ready := receiveSentFrame(t, attempt).ResumeReady; ready == nil {
		t.Fatal("missing ResumeReady")
	}
	complete := emptyClientResumeComplete(
		"connection-1",
		hello.WorkerTXCursorDigest,
	)
	complete.ActiveTurnDisposition =
		contractsv1.RuntimeControlResumeContinuityUnproven
	attempt.receive(resumeCompleteFrame(complete))
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready ||
			transition.StreamAttempt != 1 ||
			transition.ActiveTurnDisposition !=
				contractsv1.RuntimeControlResumeContinuityUnproven {
			t.Fatalf("ready transition=%+v", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("missing continuity-unproven ReadyTransition")
	}

	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v, want context.Canceled", err)
	}
}

func TestReconnectingClientNeverDropsCriticalReadyTransitionWhenBufferFull(t *testing.T) {
	for _, test := range []struct {
		name          string
		disposition   string
		authoritative *contractsv1.RuntimeControlActiveTurn
	}{
		{
			name:        "continuity unproven",
			disposition: contractsv1.RuntimeControlResumeContinuityUnproven,
		},
		{
			name:        "already terminal",
			disposition: contractsv1.RuntimeControlResumeAlreadyTerminal,
			authoritative: testClientActiveTurn(
				contractsv1.RuntimeControlTurnEffectTerminal,
				testClientDigest("critical-terminal"),
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestReconnectingClient(t, newScriptedClientRPC(1))
			for index := 0; index < cap(client.readyTransitions); index++ {
				client.readyTransitions <- ClientReadyTransition{
					Ready:         false,
					StreamAttempt: uint64(index + 10),
				}
			}
			published := make(chan error, 1)
			go func() {
				published <- client.markReady(
					context.Background(),
					1,
					contractsv1.RuntimeControlResumeComplete{
						ActiveTurnDisposition: test.disposition,
						AuthoritativeStart:    cloneActiveTurn(test.authoritative),
					},
				)
			}()
			select {
			case err := <-published:
				t.Fatalf(
					"critical %q transition returned %v while the buffer was full",
					test.disposition,
					err,
				)
			case <-time.After(20 * time.Millisecond):
			}
			<-client.ReadyTransitions()
			select {
			case err := <-published:
				if err != nil {
					t.Fatalf("publish critical transition: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatalf(
					"critical %q transition did not enqueue after capacity freed",
					test.disposition,
				)
			}
			found := false
			for index := 0; index < cap(client.readyTransitions); index++ {
				transition := <-client.ReadyTransitions()
				found = found ||
					transition.Ready &&
						transition.ActiveTurnDisposition == test.disposition
			}
			if !found {
				t.Fatalf(
					"critical %q transition was dropped behind stale pulses",
					test.disposition,
				)
			}
		})
	}
}

func TestReconnectingClientCriticalReadyEnqueueIsReleasedByRunContext(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	for index := 0; index < cap(client.readyTransitions); index++ {
		client.readyTransitions <- ClientReadyTransition{
			Ready:         false,
			StreamAttempt: uint64(index + 10),
		}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	published := make(chan error, 1)
	go func() {
		published <- client.markReady(
			runCtx,
			1,
			contractsv1.RuntimeControlResumeComplete{
				ActiveTurnDisposition: contractsv1.RuntimeControlResumeContinuityUnproven,
			},
		)
	}()
	select {
	case err := <-published:
		t.Fatalf("critical transition did not wait for capacity: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-published:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("critical enqueue cancellation err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run context did not release critical transition enqueue")
	}
}

func TestReconnectingClientAppliesAndDeliversCommandsStrictlyInOrder(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()

	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	first := testClientCommand(client.credential.Identity, 1, 0, "command-1")
	second := testClientCommand(client.credential.Identity, 2, 0, "command-2")
	firstDigest := advanceClientCursor(
		t,
		contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		first,
	)
	secondDigest := advanceClientCursor(
		t,
		firstDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		second,
	)
	attempt.receive(resumeAckFrame(contractsv1.RuntimeControlResumeAck{
		Accepted:                     true,
		ServerConnectionID:           "connection-1",
		JournalRevision:              1,
		BackendRXCursorDigest:        contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXLastCommandSequence: 2,
		BackendTXCursorDigest:        secondDigest,
		ReplayWorkerFromSequence:     1,
		ReplayBackendFromSequence:    1,
		ActiveTurnDisposition:        contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	attempt.receive(envelopeClientFrame(first))
	attempt.receive(envelopeClientFrame(second))
	if frame := receiveSentFrame(t, attempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want Ready", frame)
	}
	attempt.receive(resumeCompleteFrame(contractsv1.RuntimeControlResumeComplete{
		ServerConnectionID:           "connection-1",
		JournalRevision:              2,
		BackendRXCursorDigest:        contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXLastCommandSequence: 2,
		BackendTXWorkerAckedSequence: 2,
		BackendTXCursorDigest:        secondDigest,
		ActiveTurnDisposition:        contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	firstDelivered := receiveClientCommand(t, client.Commands())
	if firstDelivered.Envelope.MessageID != "command-1" {
		t.Fatalf("first delivered = %+v", firstDelivered.Envelope)
	}
	select {
	case unexpected := <-client.Commands():
		t.Fatalf("second command delivered before first application: %+v", unexpected)
	default:
	}
	if err := client.MarkApplied(2); !errors.Is(err, ErrApplicationSequence) {
		t.Fatalf("out-of-order MarkApplied = %v", err)
	}
	if err := client.MarkApplied(1); err != nil {
		t.Fatalf("MarkApplied(1): %v", err)
	}
	secondDelivered := receiveClientCommand(t, client.Commands())
	if secondDelivered.Envelope.MessageID != "command-2" {
		t.Fatalf("second delivered = %+v", secondDelivered.Envelope)
	}
	if err := client.MarkApplied(2); err != nil {
		t.Fatalf("MarkApplied(2): %v", err)
	}
	if err := client.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	_ = hello
}

func TestReconnectingClientReplaysExactEventAfterUnavailableWithoutResettingCursor(t *testing.T) {
	rpc := newScriptedClientRPC(2)
	client := newTestReconnectingClient(t, rpc)
	event := testClientEvent(client.credential.Identity, "event-1", "runtime.ready")
	if err := client.Publish(context.Background(), application.WorkerControlEvent{Envelope: event}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()

	firstAttempt := rpc.nextAttempt(t)
	firstHello := receiveSentFrame(t, firstAttempt).ResumeHello
	if firstHello == nil || firstHello.WorkerTXLastSequence != 1 {
		t.Fatalf("first Hello = %+v", firstHello)
	}
	firstAttempt.receive(resumeAckFrame(emptyClientResumeAck(
		"connection-1",
		firstHello.WorkerTXCursorDigest,
	)))
	firstReplay := receiveSentFrame(t, firstAttempt).Envelope
	if firstReplay == nil {
		t.Fatal("first replay was not an envelope")
	}
	if frame := receiveSentFrame(t, firstAttempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want ResumeReady", frame)
	}
	firstAttempt.fail(status.Error(codes.Unavailable, "backend restart"))

	secondAttempt := rpc.nextAttempt(t)
	secondHello := receiveSentFrame(t, secondAttempt).ResumeHello
	if secondHello == nil ||
		secondHello.StreamAttempt != 2 ||
		secondHello.WorkerTXLastSequence != firstHello.WorkerTXLastSequence ||
		secondHello.WorkerTXCursorDigest != firstHello.WorkerTXCursorDigest {
		t.Fatalf("second Hello = %+v; first = %+v", secondHello, firstHello)
	}
	secondAttempt.receive(resumeAckFrame(emptyClientResumeAck(
		"connection-2",
		secondHello.WorkerTXCursorDigest,
	)))
	secondReplay := receiveSentFrame(t, secondAttempt).Envelope
	if !reflect.DeepEqual(firstReplay, secondReplay) {
		t.Fatalf("replayed envelope changed:\nfirst=%+v\nsecond=%+v", firstReplay, secondReplay)
	}
	if frame := receiveSentFrame(t, secondAttempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want ResumeReady", frame)
	}
	secondAttempt.receive(resumeCompleteFrame(emptyClientResumeComplete(
		"connection-2",
		secondHello.WorkerTXCursorDigest,
	)))
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready || transition.StreamAttempt != 2 {
			t.Fatalf("ready transition = %+v", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("second attempt did not become ready")
	}
	if rpc.connectCount() != 2 {
		t.Fatalf("connect count = %d, want 2", rpc.connectCount())
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientAcceptsExactCommandReplayWithOlderPiggybackACK(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	command := testClientCommand(client.credential.Identity, 1, 0, "command-1")
	if _, _, err := client.acceptBackendEnvelope(command, false); err != nil {
		t.Fatal(err)
	}
	event := testClientEvent(client.credential.Identity, "event-1", "runtime.progress")
	if err := client.Publish(
		context.Background(),
		application.WorkerControlEvent{Envelope: event},
	); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	commandDigest := advanceClientCursor(
		t,
		contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		command,
	)
	attempt.receive(resumeAckFrame(contractsv1.RuntimeControlResumeAck{
		Accepted:                         true,
		ServerConnectionID:               "connection-1",
		JournalRevision:                  1,
		BackendRXCommittedWorkerSequence: 1,
		BackendRXCursorDigest:            hello.WorkerTXCursorDigest,
		BackendTXLastCommandSequence:     1,
		BackendTXCursorDigest:            commandDigest,
		ReplayWorkerFromSequence:         2,
		ReplayBackendFromSequence:        1,
		ActiveTurnDisposition:            contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	attempt.receive(envelopeClientFrame(command))
	if frame := receiveSentFrame(t, attempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want Ready after exact replay", frame)
	}
	attempt.receive(resumeCompleteFrame(contractsv1.RuntimeControlResumeComplete{
		ServerConnectionID:               "connection-1",
		JournalRevision:                  2,
		BackendRXCommittedWorkerSequence: 1,
		BackendRXCursorDigest:            hello.WorkerTXCursorDigest,
		BackendTXLastCommandSequence:     1,
		BackendTXWorkerAckedSequence:     1,
		BackendTXCursorDigest:            commandDigest,
		ActiveTurnDisposition:            contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	if delivered := receiveClientCommand(t, client.Commands()); delivered.Envelope.MessageID != "command-1" {
		t.Fatalf("delivered command = %+v", delivered.Envelope)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientFencesFramesFromAnOldAttempt(t *testing.T) {
	rpc := newScriptedClientRPC(2)
	client := newTestReconnectingClient(t, rpc)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()

	firstAttempt := rpc.nextAttempt(t)
	firstHello := receiveSentFrame(t, firstAttempt).ResumeHello
	firstAttempt.receive(resumeAckFrame(emptyClientResumeAck(
		"connection-1",
		firstHello.WorkerTXCursorDigest,
	)))
	if frame := receiveSentFrame(t, firstAttempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want Ready", frame)
	}
	firstAttempt.receive(resumeCompleteFrame(emptyClientResumeComplete(
		"connection-1",
		firstHello.WorkerTXCursorDigest,
	)))
	select {
	case <-client.ReadyTransitions():
	case <-time.After(time.Second):
		t.Fatal("first attempt did not become ready")
	}
	firstAttempt.fail(status.Error(codes.Unavailable, "stream replaced"))

	secondAttempt := rpc.nextAttempt(t)
	secondHello := receiveSentFrame(t, secondAttempt).ResumeHello
	late := testClientCommand(client.credential.Identity, 1, 0, "late-old-command")
	firstAttempt.receive(envelopeClientFrame(late))
	secondAttempt.receive(resumeAckFrame(emptyClientResumeAck(
		"connection-2",
		secondHello.WorkerTXCursorDigest,
	)))
	if frame := receiveSentFrame(t, secondAttempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want Ready", frame)
	}
	secondAttempt.receive(resumeCompleteFrame(emptyClientResumeComplete(
		"connection-2",
		secondHello.WorkerTXCursorDigest,
	)))
	select {
	case transition := <-client.ReadyTransitions():
		if transition.Ready {
			t.Fatalf("transition = %+v, want disconnect transition first", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("missing disconnect transition")
	}
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready || transition.StreamAttempt != 2 {
			t.Fatalf("second ready transition = %+v", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("missing second ready transition")
	}
	select {
	case command := <-client.Commands():
		t.Fatalf("old-attempt command crossed owner fence: %+v", command)
	default:
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientDelaysEventsPublishedDuringHandshake(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	first := testClientEvent(client.credential.Identity, "event-1", "runtime.ready")
	if err := client.Publish(context.Background(), application.WorkerControlEvent{Envelope: first}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	if hello == nil || hello.WorkerTXLastSequence != 1 {
		t.Fatalf("Hello = %+v", hello)
	}
	second := testClientEvent(client.credential.Identity, "event-2", "runtime.progress")
	if err := client.Publish(context.Background(), application.WorkerControlEvent{Envelope: second}); err != nil {
		t.Fatal(err)
	}
	attempt.receive(resumeAckFrame(emptyClientResumeAck(
		"connection-1",
		hello.WorkerTXCursorDigest,
	)))
	if got := receiveSentFrame(t, attempt).Envelope; got == nil || got.MessageID != "event-1" {
		t.Fatalf("first replay = %+v", got)
	}
	if frame := receiveSentFrame(t, attempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want ResumeReady", frame)
	}
	select {
	case frame := <-attempt.sent:
		t.Fatalf("post-Hello event sent before Complete: %+v", frame)
	default:
	}
	attempt.receive(resumeCompleteFrame(emptyClientResumeComplete(
		"connection-1",
		hello.WorkerTXCursorDigest,
	)))
	if got := receiveSentFrame(t, attempt).Envelope; got == nil || got.MessageID != "event-2" {
		t.Fatalf("post-barrier event = %+v", got)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientFailsClosedOnCursorConflictWithoutRetry(t *testing.T) {
	rpc := newScriptedClientRPC(2)
	client := newTestReconnectingClient(t, rpc)
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(context.Background()) }()
	attempt := rpc.nextAttempt(t)
	if hello := receiveSentFrame(t, attempt).ResumeHello; hello == nil {
		t.Fatal("missing ResumeHello")
	}
	attempt.receive(resumeAckFrame(contractsv1.RuntimeControlResumeAck{
		Accepted:      false,
		RejectionCode: contractsv1.RuntimeControlResumeRejectedCursorConflict,
	}))
	select {
	case err := <-runErr:
		if !errors.Is(err, ErrResumeRejected) {
			t.Fatalf("Run error = %v, want ErrResumeRejected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not fail closed")
	}
	if rpc.connectCount() != 1 {
		t.Fatalf("connect count = %d, want no retry", rpc.connectCount())
	}
}

func TestReconnectingClientFailsClosedOnAuthenticationErrorWithoutRetry(t *testing.T) {
	rpc := newScriptedClientRPC(2)
	client := newTestReconnectingClient(t, rpc)
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(context.Background()) }()
	attempt := rpc.nextAttempt(t)
	if hello := receiveSentFrame(t, attempt).ResumeHello; hello == nil {
		t.Fatal("missing ResumeHello")
	}
	attempt.fail(status.Error(codes.Unauthenticated, "bad credential"))
	select {
	case err := <-runErr:
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("Run error = %v, want Unauthenticated", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not fail closed")
	}
	if rpc.connectCount() != 1 {
		t.Fatalf("connect count = %d, want no retry", rpc.connectCount())
	}
}

func TestReconnectingClientDoesNotAckAnAckEnvelope(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	attempt.receive(resumeAckFrame(emptyClientResumeAck(
		"connection-1",
		hello.WorkerTXCursorDigest,
	)))
	if frame := receiveSentFrame(t, attempt); frame.ResumeReady == nil {
		t.Fatalf("frame = %+v, want Ready", frame)
	}
	attempt.receive(resumeCompleteFrame(emptyClientResumeComplete(
		"connection-1",
		hello.WorkerTXCursorDigest,
	)))
	select {
	case <-client.ReadyTransitions():
	case <-time.After(time.Second):
		t.Fatal("client did not become ready")
	}

	ackEnvelope := testClientCommand(client.credential.Identity, 1, 0, "backend-ack-1")
	ackEnvelope.Kind = "ack"
	ackEnvelope.MessageType = "runtime.control.ack"
	attempt.receive(envelopeClientFrame(ackEnvelope))
	select {
	case frame := <-attempt.sent:
		t.Fatalf("ACK envelope caused ping-pong response: %+v", frame)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientResumeReadyIncludesAutoAppliedReplayACK(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	if hello.WorkerRXAppliedSequence != 0 {
		t.Fatalf("Hello applied cursor = %d, want 0", hello.WorkerRXAppliedSequence)
	}
	ackEnvelope := testClientCommand(client.credential.Identity, 1, 0, "backend-ack-1")
	ackEnvelope.Kind = "ack"
	ackEnvelope.MessageType = "runtime.control.ack"
	ackDigest := advanceClientCursor(
		t,
		contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		ackEnvelope,
	)
	attempt.receive(resumeAckFrame(contractsv1.RuntimeControlResumeAck{
		Accepted:                     true,
		ServerConnectionID:           "connection-1",
		JournalRevision:              1,
		BackendRXCursorDigest:        contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXLastCommandSequence: 1,
		BackendTXCursorDigest:        ackDigest,
		ReplayWorkerFromSequence:     1,
		ReplayBackendFromSequence:    1,
		ActiveTurnDisposition:        contractsv1.RuntimeControlResumeNoActiveTurn,
	}))
	attempt.receive(envelopeClientFrame(ackEnvelope))
	ready := receiveSentFrame(t, attempt).ResumeReady
	if ready == nil || ready.WorkerRXAppliedSequence != 1 {
		t.Fatalf("Ready = %+v, want replay ACK applied through 1", ready)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientCollapsesDisconnectedHeartbeats(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	first := testClientEvent(client.credential.Identity, "heartbeat-1", "runtime.heartbeat")
	second := testClientEvent(client.credential.Identity, "heartbeat-2", "runtime.heartbeat")
	if err := client.TryPublishHeartbeat(application.WorkerControlEvent{Envelope: first}); err != nil {
		t.Fatal(err)
	}
	if err := client.TryPublishHeartbeat(application.WorkerControlEvent{Envelope: second}); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	if hello == nil || hello.WorkerTXLastSequence != 1 {
		t.Fatalf("Hello worker TX cursor = %+v, want one collapsed heartbeat", hello)
	}
	cancel()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientPreservesTerminalCapacityAndFreezesHelloTurnSnapshot(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	client.txMaxMessages = 4
	client.txReservedMessages = 2

	start := testClientCommand(client.credential.Identity, 1, 0, "start-1")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	start.IdempotencyKey = "start-key-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), application.WorkerControlEvent{
		Envelope: testClientEvent(client.credential.Identity, "event-1", "runtime.progress"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), application.WorkerControlEvent{
		Envelope: testClientEvent(client.credential.Identity, "event-2", "runtime.progress"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(context.Background(), application.WorkerControlEvent{
		Envelope: testClientEvent(client.credential.Identity, "event-3", "runtime.progress"),
	}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("ordinary Publish = %v, want ErrBackpressure", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	hello := receiveSentFrame(t, attempt).ResumeHello
	if hello == nil || hello.ActiveTurn == nil ||
		hello.ActiveTurn.Phase != contractsv1.RuntimeControlTurnEffectReceived {
		t.Fatalf("Hello active Turn = %+v", hello)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatal(err)
	}
	ordinaryTurnUpdate := testClientEvent(
		client.credential.Identity,
		"turn-update-1",
		"agent_turn.update",
	)
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: ordinaryTurnUpdate,
	}, false); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("ordinary Turn update = %v, want ErrBackpressure", err)
	}
	activeEffect := testClientEvent(client.credential.Identity, "active-1", "agent_turn.effect")
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: activeEffect,
	}, false); err != nil {
		t.Fatalf("active PublishTurnUpdate used reserved capacity: %v", err)
	}
	if err := client.Publish(context.Background(), application.WorkerControlEvent{
		Envelope: testClientEvent(client.credential.Identity, "event-4", "runtime.progress"),
	}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("ordinary Publish after active proof = %v, want ErrBackpressure", err)
	}
	terminal := testClientEvent(client.credential.Identity, "terminal-1", "agent_turn.terminal")
	terminal.WorkerRunID = "run-1"
	terminal.AgentSessionID = "agent-session-1"
	terminal.AgentTurnID = "turn-1"
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: terminal,
	}, true); err != nil {
		t.Fatalf("terminal PublishTurnUpdate used reserved capacity: %v", err)
	}
	activeAck := emptyClientResumeAck("connection-1", hello.WorkerTXCursorDigest)
	activeAck.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	activeAck.AuthoritativeStart = cloneActiveTurn(hello.ActiveTurn)
	activeAck.BackendTXLastCommandSequence = 1
	activeAck.BackendTXCursorDigest = advanceClientCursor(
		t,
		contractsv1.RuntimeControlCursorGenesisDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		start,
	)
	attempt.receive(resumeAckFrame(activeAck))
	for sequence := uint64(1); sequence <= hello.WorkerTXLastSequence; sequence++ {
		if frame := receiveSentFrame(t, attempt); frame.Envelope == nil ||
			frame.Envelope.Sequence != sequence {
			t.Fatalf("replay frame %d = %+v", sequence, frame)
		}
	}
	attempt.receive(envelopeClientFrame(start))
	ready := receiveSentFrame(t, attempt).ResumeReady
	if ready == nil || ready.ActiveTurn == nil ||
		ready.ActiveTurn.Phase != contractsv1.RuntimeControlTurnEffectReceived {
		t.Fatalf("Ready did not retain frozen Hello proof: %+v", ready)
	}
	cancel()
	attempt.fail(io.EOF)
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestReconnectingClientTransportACKsCannotConsumeEffectAndTerminalReserve(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	client.txMaxMessages = 4
	client.txReservedMessages = 2

	start := testClientCommand(client.credential.Identity, 1, 0, "start-1")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	start.IdempotencyKey = "start-key-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 2; sequence++ {
		if err := client.Publish(context.Background(), application.WorkerControlEvent{
			Envelope: testClientEvent(
				client.credential.Identity,
				fmt.Sprintf("ordinary-%d", sequence),
				"runtime.progress",
			),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for sequence := uint64(2); sequence <= 3; sequence++ {
		command := testClientCommand(
			client.credential.Identity,
			sequence,
			0,
			fmt.Sprintf("command-%d", sequence),
		)
		if _, _, err := client.acceptBackendEnvelope(command, false); err != nil {
			t.Fatal(err)
		}
		if err := client.enqueueTransportACK(command); err != nil {
			t.Fatalf("enqueue transport ACK %d: %v", sequence, err)
		}
	}

	activeEffect := testClientEvent(
		client.credential.Identity,
		"active-effect",
		"agent_turn.effect",
	)
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: activeEffect,
	}, false); err != nil {
		t.Fatalf("active effect lost its reserved capacity: %v", err)
	}
	terminal := testClientEvent(
		client.credential.Identity,
		"terminal-effect",
		"agent_turn.terminal",
	)
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: terminal,
	}, true); err != nil {
		t.Fatalf("terminal effect lost its reserved capacity: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.txSpool) != 4 {
		t.Fatalf("outbound spool size=%d, want two ordinary plus effect and terminal", len(client.txSpool))
	}
	for sequence, wantType := range map[uint64]string{
		1: "runtime.progress",
		2: "runtime.progress",
		3: "agent_turn.effect",
		4: "agent_turn.terminal",
	} {
		if got := client.txSpool[sequence]; got == nil || got.envelope.MessageType != wantType {
			t.Fatalf("outbound sequence %d=%+v, want %q", sequence, got, wantType)
		}
	}
}

func TestReconnectingClientBoundsAcknowledgedLogicalIdentityHistory(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	client.logicalMaxMessages = 4
	client.logicalProofReserveMessages = 2

	first := testClientEvent(
		client.credential.Identity,
		"logical-history-1",
		"runtime.progress",
	)
	second := testClientEvent(
		client.credential.Identity,
		"logical-history-2",
		"runtime.progress",
	)
	for _, event := range []contractsv1.RuntimeControlEnvelope{first, second} {
		if err := client.Publish(
			context.Background(),
			application.WorkerControlEvent{Envelope: event},
		); err != nil {
			t.Fatalf("fill logical identity history: %v", err)
		}
	}
	client.mu.Lock()
	if err := client.applyPeerEventACKLocked(2); err != nil {
		client.mu.Unlock()
		t.Fatalf("ack logical identity history: %v", err)
	}
	if len(client.txSpool) != 0 {
		client.mu.Unlock()
		t.Fatalf("ACKed resend spool size=%d, want zero", len(client.txSpool))
	}
	client.mu.Unlock()

	if err := client.Publish(
		context.Background(),
		application.WorkerControlEvent{Envelope: first},
	); err != nil {
		t.Fatalf("exact ACKed logical duplicate: %v", err)
	}
	conflicting := first
	conflicting.Payload = json.RawMessage(`{"conflict":true}`)
	if err := client.Publish(
		context.Background(),
		application.WorkerControlEvent{Envelope: conflicting},
	); !errors.Is(err, ErrConflictingDuplicate) {
		t.Fatalf("conflicting ACKed logical duplicate err=%v", err)
	}
	third := testClientEvent(
		client.credential.Identity,
		"logical-history-3",
		"runtime.progress",
	)
	if err := client.Publish(
		context.Background(),
		application.WorkerControlEvent{Envelope: third},
	); !errors.Is(err, ErrLogicalHistoryCapacity) {
		t.Fatalf("new identity beyond bounded history err=%v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sentLogical) != 2 || client.txLastSequence != 2 ||
		len(client.txSpool) != 0 {
		t.Fatalf(
			"bounded logical history=%d tx last/spool=%d/%d",
			len(client.sentLogical),
			client.txLastSequence,
			len(client.txSpool),
		)
	}
}

func TestReconnectingClientDefaultLogicalHistorySafetyBound(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	if client.logicalProofReserveMessages != 2 {
		t.Fatalf(
			"logical proof reserve=%d, want active and terminal slots",
			client.logicalProofReserveMessages,
		)
	}
	if ordinary := client.logicalMaxMessages -
		client.logicalProofReserveMessages; ordinary != 64<<10 {
		t.Fatalf("ordinary logical history bound=%d, want 64K", ordinary)
	}
}

func TestReconnectingClientReservesLogicalHistoryBeforePromptIntent(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	client.logicalMaxMessages = 4
	client.logicalProofReserveMessages = 2

	for sequence := 1; sequence <= 2; sequence++ {
		event := testClientEvent(
			client.credential.Identity,
			fmt.Sprintf("logical-ordinary-%d", sequence),
			"runtime.progress",
		)
		if err := client.Publish(
			context.Background(),
			application.WorkerControlEvent{Envelope: event},
		); err != nil {
			t.Fatalf("fill ordinary logical history: %v", err)
		}
	}
	heartbeat := testClientEvent(
		client.credential.Identity,
		"logical-heartbeat",
		"runtime.heartbeat",
	)
	if err := client.TryPublishHeartbeat(
		application.WorkerControlEvent{Envelope: heartbeat},
	); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("heartbeat consumed logical proof reserve err=%v", err)
	}
	if got := len(client.sentLogical); got != 2 {
		t.Fatalf("heartbeat changed logical history size=%d, want 2", got)
	}
	start := testClientCommand(client.credential.Identity, 1, 0, "logical-start-1")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatalf("two proof slots did not admit prompt intent: %v", err)
	}
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatal(err)
	}
	active := testClientEvent(
		client.credential.Identity,
		"logical-active-1",
		"agent_turn.effect",
	)
	if err := client.PublishTurnUpdate(
		context.Background(),
		application.WorkerControlEvent{Envelope: active},
		false,
	); err != nil {
		t.Fatalf("active proof lost reserved logical slot: %v", err)
	}
	terminal := testClientEvent(
		client.credential.Identity,
		"logical-terminal-1",
		"agent_turn.terminal",
	)
	if err := client.PublishTurnUpdate(
		context.Background(),
		application.WorkerControlEvent{Envelope: terminal},
		true,
	); err != nil {
		t.Fatalf("terminal proof lost reserved logical slot: %v", err)
	}

	nextStart := testClientCommand(
		client.credential.Identity,
		2,
		4,
		"logical-start-2",
	)
	nextStart.MessageType = "agent_turn.start"
	nextStart.WorkerRunID = "run-2"
	nextStart.AgentSessionID = "agent-session-1"
	nextStart.AgentTurnID = "turn-2"
	if _, _, err := client.acceptBackendEnvelope(nextStart, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(
		application.WorkerControlCommand{Envelope: nextStart},
	); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-2"); !errors.Is(
		err,
		ErrLogicalHistoryCapacity,
	) {
		t.Fatalf("prompt intent without two proof slots err=%v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.activeTurn == nil ||
		client.activeTurn.AgentTurnID != "turn-2" ||
		client.activeTurn.Phase != contractsv1.RuntimeControlTurnEffectReceived ||
		len(client.sentLogical) != 4 {
		t.Fatalf(
			"capacity failure advanced prompt boundary: turn=%+v history=%d",
			client.activeTurn,
			len(client.sentLogical),
		)
	}
}

func TestLogicalHistoryCapacityIsPermanentClientError(t *testing.T) {
	err := fmt.Errorf("enqueue Runtime control event: %w", ErrLogicalHistoryCapacity)
	if transientClientError(err) {
		t.Fatalf("logical history capacity must fail closed without reconnect")
	}
}

func TestReconnectingClientCoalescesDeferredTransportACKWhenOrdinaryCapacityFrees(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	client.txMaxMessages = 3
	client.txReservedMessages = 2

	if err := client.Publish(context.Background(), application.WorkerControlEvent{
		Envelope: testClientEvent(
			client.credential.Identity,
			"ordinary-1",
			"runtime.progress",
		),
	}); err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		command := testClientCommand(
			client.credential.Identity,
			sequence,
			0,
			fmt.Sprintf("command-%d", sequence),
		)
		if _, _, err := client.acceptBackendEnvelope(command, false); err != nil {
			t.Fatal(err)
		}
		if err := client.enqueueTransportACK(command); err != nil {
			t.Fatal(err)
		}
	}
	client.mu.Lock()
	if client.pendingACK == nil || client.pendingACK.Sequence != 2 {
		t.Fatalf("pending transport ACK=%+v, want cumulative command sequence 2", client.pendingACK)
	}
	client.mu.Unlock()

	peerACK := testClientCommand(client.credential.Identity, 3, 1, "backend-ack-3")
	peerACK.Kind = "ack"
	peerACK.MessageType = "runtime.control.ack"
	if _, _, err := client.acceptBackendEnvelope(peerACK, false); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.pendingACK != nil {
		t.Fatalf("pending transport ACK was not materialized: %+v", client.pendingACK)
	}
	if client.txBaseSequence != 1 || client.txLastSequence != 2 {
		t.Fatalf(
			"worker cursor base/last=%d/%d, want immutable ordinary sequence 1 followed by ACK sequence 2",
			client.txBaseSequence,
			client.txLastSequence,
		)
	}
	ack := client.txSpool[2]
	if ack == nil || ack.envelope.Kind != "ack" ||
		ack.envelope.MessageID != "worker-ack-2" ||
		ack.envelope.AckSequence != 3 {
		t.Fatalf("materialized cumulative ACK=%+v", ack)
	}
}

func TestReconnectingClientBoundsActiveEffectSoTerminalKeepsByteReserve(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "start-1")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	start.IdempotencyKey = "start-key-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatal(err)
	}

	oversizedPayload, err := json.Marshal(map[string]string{
		"data": strings.Repeat("x", maxEnvelopeBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	oversized := testClientEvent(
		client.credential.Identity,
		"oversized-active",
		"agent_turn.effect",
	)
	oversized.Payload = oversizedPayload
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: oversized,
	}, false); err == nil {
		t.Fatal("oversized active effect entered the resend spool")
	}
	client.mu.Lock()
	if client.txLastSequence != 0 || client.activeProofSequence != 0 {
		t.Fatalf(
			"oversized effect mutated worker cursor/proof=%d/%d",
			client.txLastSequence,
			client.activeProofSequence,
		)
	}
	client.mu.Unlock()

	ordinary := testClientEvent(
		client.credential.Identity,
		"ordinary-byte-budget",
		"runtime.progress",
	)
	preparedOrdinary, _, err := client.prepareEvent(
		application.WorkerControlEvent{Envelope: ordinary},
	)
	if err != nil {
		t.Fatal(err)
	}
	ordinarySize, err := outboundFrameSize(preparedOrdinary)
	if err != nil {
		t.Fatal(err)
	}
	client.txMaxBytes = ordinarySize + 1024 + defaultReservedBytes
	client.txReservedBytes = defaultReservedBytes
	if err := client.Publish(context.Background(), application.WorkerControlEvent{
		Envelope: ordinary,
	}); err != nil {
		t.Fatal(err)
	}

	boundedPayload, err := json.Marshal(map[string]string{
		"data": strings.Repeat("y", maxEnvelopeBytes-2048),
	})
	if err != nil {
		t.Fatal(err)
	}
	active := testClientEvent(
		client.credential.Identity,
		"bounded-active",
		"agent_turn.effect",
	)
	active.Payload = boundedPayload
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: active,
	}, false); err != nil {
		t.Fatalf("bounded active effect lost its byte reserve: %v", err)
	}
	terminal := testClientEvent(
		client.credential.Identity,
		"bounded-terminal",
		"agent_turn.terminal",
	)
	terminal.Payload = boundedPayload
	if err := client.PublishTurnUpdate(context.Background(), application.WorkerControlEvent{
		Envelope: terminal,
	}, true); err != nil {
		t.Fatalf("bounded terminal effect lost its byte reserve: %v", err)
	}
}

func TestReconnectingClientAllowsHigherStartAfterTerminalProofIsAcknowledged(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	firstStart := testClientCommand(client.credential.Identity, 1, 0, "start-1")
	firstStart.MessageType = "agent_turn.start"
	firstStart.WorkerRunID = "run-1"
	firstStart.AgentSessionID = "agent-session-1"
	firstStart.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(firstStart, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: firstStart}); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatal(err)
	}
	terminal := testClientEvent(client.credential.Identity, "terminal-1", "agent_turn.terminal")
	if err := client.PublishTurnUpdate(
		context.Background(),
		application.WorkerControlEvent{Envelope: terminal},
		true,
	); err != nil {
		t.Fatal(err)
	}

	secondStart := testClientCommand(client.credential.Identity, 2, 1, "start-2")
	secondStart.MessageType = "agent_turn.start"
	secondStart.WorkerRunID = "run-2"
	secondStart.AgentSessionID = "agent-session-1"
	secondStart.AgentTurnID = "turn-2"
	if _, _, err := client.acceptBackendEnvelope(secondStart, false); err != nil {
		t.Fatal(err)
	}
	_, retainedHello, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	if retainedHello.ActiveTurn == nil ||
		retainedHello.ActiveTurn.Phase != contractsv1.RuntimeControlTurnEffectTerminal ||
		retainedHello.ActiveTurn.AgentTurnID != "turn-1" {
		t.Fatalf("terminal proof was cleared by receipt ACK: %+v", retainedHello.ActiveTurn)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: secondStart}); err != nil {
		t.Fatalf("BeginTurn for higher acknowledged start: %v", err)
	}
	_, hello, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	if hello.ActiveTurn == nil ||
		hello.ActiveTurn.AgentTurnID != "turn-2" ||
		hello.ActiveTurn.StartCommandSequence != 2 {
		t.Fatalf("active Turn after rollover = %+v", hello.ActiveTurn)
	}
}

func TestReconnectingClientFatalStateFencesPromptIntent(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "fatal-race-start")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	fatalErr := status.Error(codes.Unauthenticated, "credential revoked")
	client.mu.Lock()
	client.terminalErr = fatalErr
	client.mu.Unlock()
	if err := client.MarkPromptIntent("turn-1"); !errors.Is(err, fatalErr) {
		t.Fatalf("prompt intent after fatal WorkerControl state err=%v, want %v", err, fatalErr)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.activeTurn == nil ||
		client.activeTurn.Phase != contractsv1.RuntimeControlTurnEffectReceived {
		t.Fatalf("fatal state advanced prompt intent: %+v", client.activeTurn)
	}
}

func TestReconnectingClientUnprovenReceivedResumeFencesPromptIntent(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "unproven-start")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(
		application.WorkerControlCommand{Envelope: start},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	ack := emptyClientResumeAck("connection-1", snapshot.txDigest)
	ack.BackendTXLastCommandSequence = start.Sequence
	ack.BackendTXCursorDigest = snapshot.rxDigest
	ack.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	ack.AuthoritativeStart = cloneActiveTurn(snapshot.activeTurn)
	if err := client.acceptResumeAck(snapshot, ack); err != nil {
		t.Fatalf("accept continuity-unproven ResumeAck: %v", err)
	}
	if err := client.MarkPromptIntent("turn-1"); !errors.Is(
		err,
		ErrTurnContinuityUnproven,
	) {
		t.Fatalf("prompt intent after unproven resume err=%v", err)
	}
	if err := client.BeginTurn(
		application.WorkerControlCommand{Envelope: start},
	); !errors.Is(err, ErrTurnContinuityUnproven) {
		t.Fatalf("redelivered start after unproven resume err=%v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.activeTurn == nil ||
		client.activeTurn.Phase != contractsv1.RuntimeControlTurnEffectReceived {
		t.Fatalf("unproven resume advanced prompt intent: %+v", client.activeTurn)
	}
}

func TestReconnectingClientUnprovenResumeFencesPromptActivationAfterIntent(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "unproven-race-start")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(
		application.WorkerControlCommand{Envelope: start},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}
	ack := emptyClientResumeAck("connection-1", snapshot.txDigest)
	ack.BackendTXLastCommandSequence = start.Sequence
	ack.BackendTXCursorDigest = snapshot.rxDigest
	ack.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	ack.AuthoritativeStart = cloneActiveTurn(snapshot.activeTurn)
	if err := client.acceptResumeAck(snapshot, ack); err != nil {
		t.Fatalf("accept continuity-unproven ResumeAck: %v", err)
	}
	if err := client.MarkPromptActive("turn-1"); !errors.Is(
		err,
		ErrTurnContinuityUnproven,
	) {
		t.Fatalf("prompt activation after unproven resume err=%v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.rxApplied != 0 ||
		client.activeTurn == nil ||
		client.activeTurn.Phase != contractsv1.RuntimeControlTurnEffectPromptIntent {
		t.Fatalf(
			"unproven resume applied start=%d activeTurn=%+v",
			client.rxApplied,
			client.activeTurn,
		)
	}
}

func TestReconnectingClientFatalStateFencesBeginTurn(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "fatal-before-begin")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	fatalErr := status.Error(codes.InvalidArgument, "schema conflict")
	client.mu.Lock()
	client.terminalErr = fatalErr
	client.mu.Unlock()
	if err := client.BeginTurn(
		application.WorkerControlCommand{Envelope: start},
	); !errors.Is(err, fatalErr) {
		t.Fatalf("BeginTurn after fatal WorkerControl state err=%v, want %v", err, fatalErr)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.activeTurn != nil {
		t.Fatalf("fatal state created active Turn: %+v", client.activeTurn)
	}
}

func TestReconnectingClientPromptIntentWinningFatalRaceCanRecordActiveProof(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "intent-before-fatal")
	start.MessageType = "agent_turn.start"
	start.WorkerRunID = "run-1"
	start.AgentSessionID = "agent-session-1"
	start.AgentTurnID = "turn-1"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.terminalErr = status.Error(codes.Unauthenticated, "credential revoked")
	client.mu.Unlock()
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatalf("Prompt side effect lost its active proof after fatal race: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.rxApplied != start.Sequence ||
		client.activeTurn == nil ||
		client.activeTurn.Phase != contractsv1.RuntimeControlTurnEffectActive {
		t.Fatalf(
			"post-intent fatal state rxApplied=%d activeTurn=%+v",
			client.rxApplied,
			client.activeTurn,
		)
	}
}

func TestReconnectingClientMarkPromptActiveAtomicallyAdvancesStartApplication(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	start := testClientCommand(client.credential.Identity, 1, 0, "start-1")
	start.MessageType = "agent_turn.start"
	if _, _, err := client.acceptBackendEnvelope(start, false); err != nil {
		t.Fatal(err)
	}
	if err := client.deliverPendingCommands(); err != nil {
		t.Fatal(err)
	}
	if err := client.BeginTurn(application.WorkerControlCommand{Envelope: start}); err != nil {
		t.Fatal(err)
	}
	if err := client.MarkPromptIntent("turn-1"); err != nil {
		t.Fatal(err)
	}

	const snapshotCount = 256
	snapshots := make(chan contractsv1.RuntimeControlResumeHello, snapshotCount)
	snapshotErrors := make(chan error, 1)
	var snapshotsDone sync.WaitGroup
	snapshotsDone.Add(1)
	go func() {
		defer snapshotsDone.Done()
		for index := 0; index < snapshotCount; index++ {
			_, hello, err := client.freezeResumeHello()
			if err != nil {
				snapshotErrors <- err
				return
			}
			snapshots <- hello
		}
	}()
	if err := client.MarkPromptActive("turn-1"); err != nil {
		t.Fatalf("MarkPromptActive: %v", err)
	}
	snapshotsDone.Wait()
	close(snapshots)
	select {
	case err := <-snapshotErrors:
		t.Fatal(err)
	default:
	}
	for hello := range snapshots {
		if hello.ActiveTurn == nil {
			t.Fatal("snapshot lost active Turn identity")
		}
		switch hello.ActiveTurn.Phase {
		case contractsv1.RuntimeControlTurnEffectPromptIntent:
			if hello.WorkerRXAppliedSequence != 0 {
				t.Fatalf("prompt-intent snapshot applied=%d", hello.WorkerRXAppliedSequence)
			}
		case contractsv1.RuntimeControlTurnEffectActive:
			if hello.WorkerRXAppliedSequence != hello.ActiveTurn.StartCommandSequence {
				t.Fatalf("active snapshot = %+v", hello)
			}
		default:
			t.Fatalf("snapshot phase = %q", hello.ActiveTurn.Phase)
		}
	}
	_, final, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	if final.ActiveTurn == nil ||
		final.ActiveTurn.Phase != contractsv1.RuntimeControlTurnEffectActive ||
		final.WorkerRXAppliedSequence != 1 {
		t.Fatalf("final active/application snapshot = %+v", final)
	}
}

func TestValidateResumeTurnEnforcesDispositionMatrix(t *testing.T) {
	reportedTerminal := testClientActiveTurn(
		contractsv1.RuntimeControlTurnEffectTerminal,
		testClientDigest("terminal-worker"),
	)
	reportedActive := testClientActiveTurn(
		contractsv1.RuntimeControlTurnEffectActive,
		"",
	)
	authoritativeTerminal := cloneActiveTurn(reportedTerminal)
	authoritativeActive := cloneActiveTurn(reportedActive)
	authoritativePrompt := cloneActiveTurn(reportedActive)
	authoritativePrompt.Phase = contractsv1.RuntimeControlTurnEffectPromptIntent
	tests := []struct {
		name          string
		reported      *contractsv1.RuntimeControlActiveTurn
		disposition   string
		authoritative *contractsv1.RuntimeControlActiveTurn
		wantError     bool
	}{
		{
			name:          "continue active rejects terminal authority",
			reported:      reportedTerminal,
			disposition:   contractsv1.RuntimeControlResumeContinueActive,
			authoritative: authoritativeTerminal,
			wantError:     true,
		},
		{
			name:          "continue active rejects terminal worker proof",
			reported:      reportedTerminal,
			disposition:   contractsv1.RuntimeControlResumeContinueActive,
			authoritative: authoritativeActive,
			wantError:     true,
		},
		{
			name:          "already terminal rejects active authority",
			reported:      reportedTerminal,
			disposition:   contractsv1.RuntimeControlResumeAlreadyTerminal,
			authoritative: authoritativeActive,
			wantError:     true,
		},
		{
			name:        "already terminal requires exact terminal digest",
			reported:    reportedTerminal,
			disposition: contractsv1.RuntimeControlResumeAlreadyTerminal,
			authoritative: func() *contractsv1.RuntimeControlActiveTurn {
				conflicting := cloneActiveTurn(authoritativeTerminal)
				conflicting.TerminalEnvelopeDigest = testClientDigest("terminal-backend")
				return conflicting
			}(),
			wantError: true,
		},
		{
			name:          "continuity unproven permits explicit prompt-intent downgrade",
			reported:      reportedTerminal,
			disposition:   contractsv1.RuntimeControlResumeContinuityUnproven,
			authoritative: authoritativePrompt,
		},
		{
			name:          "continuity unproven accepts pending terminal replay authority",
			reported:      reportedTerminal,
			disposition:   contractsv1.RuntimeControlResumeContinuityUnproven,
			authoritative: authoritativeActive,
		},
		{
			name:          "continuity unproven rejects active authority for active worker",
			reported:      reportedActive,
			disposition:   contractsv1.RuntimeControlResumeContinuityUnproven,
			authoritative: authoritativeActive,
			wantError:     true,
		},
		{
			name:          "already terminal rejects worker that only reports active",
			reported:      reportedActive,
			disposition:   contractsv1.RuntimeControlResumeAlreadyTerminal,
			authoritative: authoritativeTerminal,
			wantError:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateResumeTurn(
				test.reported,
				test.disposition,
				test.authoritative,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("validateResumeTurn error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestReconnectingClientAcceptsPendingTerminalReplayThenExactTerminalComplete(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	reportedTerminal := testClientActiveTurn(
		contractsv1.RuntimeControlTurnEffectTerminal,
		testClientDigest("terminal-envelope"),
	)
	client.mu.Lock()
	client.activeTurn = cloneActiveTurn(reportedTerminal)
	client.mu.Unlock()
	snapshot, _, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	authoritativeActive := cloneActiveTurn(reportedTerminal)
	authoritativeActive.Phase = contractsv1.RuntimeControlTurnEffectActive
	authoritativeActive.TerminalEnvelopeDigest = ""
	ack := contractsv1.RuntimeControlResumeAck{
		Accepted:                  true,
		ServerConnectionID:        "connection-1",
		JournalRevision:           1,
		BackendRXCursorDigest:     contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXCursorDigest:     contractsv1.RuntimeControlCursorGenesisDigest,
		ReplayWorkerFromSequence:  1,
		ReplayBackendFromSequence: 1,
		ActiveTurnDisposition:     contractsv1.RuntimeControlResumeContinuityUnproven,
		AuthoritativeStart:        authoritativeActive,
	}
	if err := client.acceptResumeAck(snapshot, ack); err != nil {
		t.Fatalf("pending terminal ResumeAck: %v", err)
	}
	ready, err := client.resumeReady(snapshot, ack)
	if err != nil {
		t.Fatal(err)
	}
	complete := contractsv1.RuntimeControlResumeComplete{
		ServerConnectionID:    "connection-1",
		JournalRevision:       2,
		BackendRXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest,
		ActiveTurnDisposition: contractsv1.RuntimeControlResumeAlreadyTerminal,
		AuthoritativeStart:    cloneActiveTurn(reportedTerminal),
	}
	if err := client.acceptResumeComplete(snapshot, ack, ready, complete); err != nil {
		t.Fatalf("terminal ResumeComplete: %v", err)
	}
}

func TestReconnectingClientCompleteUnprovenFencesActiveTurnUpdates(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	reportedActive := testClientActiveTurn(
		contractsv1.RuntimeControlTurnEffectActive,
		"",
	)
	client.mu.Lock()
	client.activeTurn = cloneActiveTurn(reportedActive)
	client.mu.Unlock()
	snapshot, _, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	authoritativeIntent := cloneActiveTurn(reportedActive)
	authoritativeIntent.Phase = contractsv1.RuntimeControlTurnEffectPromptIntent
	ack := emptyClientResumeAck("connection-1", snapshot.txDigest)
	ack.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	ack.AuthoritativeStart = authoritativeIntent
	if err := client.acceptResumeAck(snapshot, ack); err != nil {
		t.Fatalf("accept pending active-proof ResumeAck: %v", err)
	}
	ready, err := client.resumeReady(snapshot, ack)
	if err != nil {
		t.Fatal(err)
	}
	complete := emptyClientResumeComplete("connection-1", snapshot.txDigest)
	complete.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	complete.AuthoritativeStart = cloneActiveTurn(authoritativeIntent)
	if err := client.acceptResumeComplete(snapshot, ack, ready, complete); err != nil {
		t.Fatalf("accept continuity-unproven ResumeComplete: %v", err)
	}
	update := testClientEvent(
		client.credential.Identity,
		"unproven-active-update",
		"agent.update",
	)
	if err := client.PublishTurnUpdate(
		context.Background(),
		application.WorkerControlEvent{Envelope: update},
		false,
	); !errors.Is(err, ErrTurnContinuityUnproven) {
		t.Fatalf("active update after unproven Complete err=%v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.txLastSequence != 0 {
		t.Fatalf("unproven active update advanced TX sequence=%d", client.txLastSequence)
	}
}

func TestReconnectingClientAllowsActiveProofReplayToResolveAtComplete(t *testing.T) {
	client := newTestReconnectingClient(t, newScriptedClientRPC(1))
	reportedActive := testClientActiveTurn(
		contractsv1.RuntimeControlTurnEffectActive,
		"",
	)
	client.mu.Lock()
	client.activeTurn = cloneActiveTurn(reportedActive)
	client.mu.Unlock()
	snapshot, _, err := client.freezeResumeHello()
	if err != nil {
		t.Fatal(err)
	}
	authoritativeIntent := cloneActiveTurn(reportedActive)
	authoritativeIntent.Phase = contractsv1.RuntimeControlTurnEffectPromptIntent
	ack := emptyClientResumeAck("connection-1", snapshot.txDigest)
	ack.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinuityUnproven
	ack.AuthoritativeStart = authoritativeIntent
	if err := client.acceptResumeAck(snapshot, ack); err != nil {
		t.Fatalf("accept pending active-proof ResumeAck: %v", err)
	}
	ready, err := client.resumeReady(snapshot, ack)
	if err != nil {
		t.Fatal(err)
	}
	complete := emptyClientResumeComplete("connection-1", snapshot.txDigest)
	complete.ActiveTurnDisposition = contractsv1.RuntimeControlResumeContinueActive
	complete.AuthoritativeStart = cloneActiveTurn(reportedActive)
	if err := client.acceptResumeComplete(snapshot, ack, ready, complete); err != nil {
		t.Fatalf("accept proven-active ResumeComplete: %v", err)
	}
	update := testClientEvent(
		client.credential.Identity,
		"proven-active-update",
		"agent.update",
	)
	if err := client.PublishTurnUpdate(
		context.Background(),
		application.WorkerControlEvent{Envelope: update},
		false,
	); err != nil {
		t.Fatalf("active update after proven Complete: %v", err)
	}
}

func TestReconnectingClientCancellationPreemptsBackoff(t *testing.T) {
	rpc := newScriptedClientRPC(1)
	client := newTestReconnectingClient(t, rpc)
	client.backoffMin = time.Hour
	client.backoffMax = time.Hour
	runCtx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- client.Run(runCtx) }()
	attempt := rpc.nextAttempt(t)
	if hello := receiveSentFrame(t, attempt).ResumeHello; hello == nil {
		t.Fatal("missing Hello")
	}
	attempt.fail(status.Error(codes.Unavailable, "restart"))
	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not preempt reconnect backoff")
	}
}

func testClientActiveTurn(
	phase string,
	terminalDigest string,
) *contractsv1.RuntimeControlActiveTurn {
	return &contractsv1.RuntimeControlActiveTurn{
		WorkerRunID:            "run-1",
		AgentSessionID:         "agent-session-1",
		AgentTurnID:            "turn-1",
		StartCommandSequence:   1,
		StartMessageID:         "start-1",
		StartIdempotencyKey:    "start-key-1",
		StartEnvelopeDigest:    testClientDigest("start-envelope"),
		Phase:                  phase,
		LastUpdateSequence:     2,
		TerminalEnvelopeDigest: terminalDigest,
	}
}

func testClientDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + fmt.Sprintf("%x", sum[:])
}

func newTestReconnectingClient(t *testing.T, rpc WorkerControlRPCClient) *Client {
	t.Helper()
	client, err := NewClient(rpc, testCredential())
	if err != nil {
		t.Fatal(err)
	}
	client.backoffMin = time.Millisecond
	client.backoffMax = 2 * time.Millisecond
	return client
}

func testClientEvent(identity RuntimeIdentity, messageID, messageType string) contractsv1.RuntimeControlEnvelope {
	return contractsv1.RuntimeControlEnvelope{
		MessageID:         messageID,
		WorkerSessionID:   contractsv1.WorkerSessionID(identity.WorkerSessionID),
		WorkerRunID:       "run-1",
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(identity.RuntimeInstanceID),
		RuntimeGeneration: identity.RuntimeGeneration,
		AgentSessionID:    "agent-session-1",
		AgentTurnID:       "turn-1",
		ChannelEpoch:      identity.ChannelEpoch,
		IdempotencyKey:    messageID + "-key",
		MessageType:       messageType,
		Payload:           json.RawMessage(`{}`),
	}
}

func testClientCommand(
	identity RuntimeIdentity,
	sequence uint64,
	ackSequence uint64,
	messageID string,
) contractsv1.RuntimeControlEnvelope {
	envelope := testClientEvent(identity, messageID, "runtime.inspect")
	envelope.SchemaVersion = contractsv1.RuntimeControlSchemaVersion
	envelope.Kind = "command"
	envelope.ChannelEpoch = identity.ChannelEpoch
	envelope.Sequence = sequence
	envelope.AckSequence = ackSequence
	envelope.SentAt = time.Date(2026, 7, 26, 8, 0, int(sequence), 0, time.UTC)
	return envelope
}

func emptyClientResumeAck(connectionID, workerTXDigest string) contractsv1.RuntimeControlResumeAck {
	return contractsv1.RuntimeControlResumeAck{
		Accepted:                  true,
		ServerConnectionID:        connectionID,
		JournalRevision:           1,
		BackendRXCursorDigest:     contractsv1.RuntimeControlCursorGenesisDigest,
		BackendTXCursorDigest:     contractsv1.RuntimeControlCursorGenesisDigest,
		ReplayWorkerFromSequence:  1,
		ReplayBackendFromSequence: 1,
		ActiveTurnDisposition:     contractsv1.RuntimeControlResumeNoActiveTurn,
	}
}

func emptyClientResumeComplete(connectionID, workerTXDigest string) contractsv1.RuntimeControlResumeComplete {
	sequence := uint64(0)
	digest := contractsv1.RuntimeControlCursorGenesisDigest
	if workerTXDigest != contractsv1.RuntimeControlCursorGenesisDigest {
		sequence = 1
		digest = workerTXDigest
	}
	return contractsv1.RuntimeControlResumeComplete{
		ServerConnectionID:               connectionID,
		JournalRevision:                  2,
		BackendRXCommittedWorkerSequence: sequence,
		BackendRXCursorDigest:            digest,
		BackendTXCursorDigest:            contractsv1.RuntimeControlCursorGenesisDigest,
		ActiveTurnDisposition:            contractsv1.RuntimeControlResumeNoActiveTurn,
	}
}

func resumeAckFrame(ack contractsv1.RuntimeControlResumeAck) *contractsv1.RuntimeControlFrame {
	return &contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeAck,
		ResumeAck:     &ack,
	}
}

func resumeCompleteFrame(complete contractsv1.RuntimeControlResumeComplete) *contractsv1.RuntimeControlFrame {
	return &contractsv1.RuntimeControlFrame{
		SchemaVersion:  contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:      contractsv1.RuntimeControlFrameTypeResumeComplete,
		ResumeComplete: &complete,
	}
}

func envelopeClientFrame(envelope contractsv1.RuntimeControlEnvelope) *contractsv1.RuntimeControlFrame {
	return &contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeEnvelope,
		Envelope:      &envelope,
	}
}

func advanceClientCursor(
	t *testing.T,
	previous string,
	direction string,
	envelope contractsv1.RuntimeControlEnvelope,
) string {
	t.Helper()
	digest, err := contractsv1.AdvanceRuntimeControlCursor(previous, direction, envelope)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

type scriptedClientRPC struct {
	mu       sync.Mutex
	attempts chan *scriptedClientAttempt
	count    int
}

func newScriptedClientRPC(capacity int) *scriptedClientRPC {
	return &scriptedClientRPC{attempts: make(chan *scriptedClientAttempt, capacity)}
}

func (rpc *scriptedClientRPC) Connect(ctx context.Context, _ ...grpc.CallOption) (WorkerControlRPC_ConnectClient, error) {
	rpc.mu.Lock()
	rpc.count++
	rpc.mu.Unlock()
	attempt := &scriptedClientAttempt{
		ctx:      ctx,
		sent:     make(chan *contractsv1.RuntimeControlFrame, 32),
		incoming: make(chan scriptedClientIncoming, 32),
	}
	rpc.attempts <- attempt
	return attempt, nil
}

func (rpc *scriptedClientRPC) nextAttempt(t *testing.T) *scriptedClientAttempt {
	t.Helper()
	select {
	case attempt := <-rpc.attempts:
		return attempt
	case <-time.After(time.Second):
		t.Fatal("client did not connect")
		return nil
	}
}

func (rpc *scriptedClientRPC) connectCount() int {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	return rpc.count
}

type scriptedClientIncoming struct {
	frame *contractsv1.RuntimeControlFrame
	err   error
}

type scriptedClientAttempt struct {
	ctx      context.Context
	sent     chan *contractsv1.RuntimeControlFrame
	incoming chan scriptedClientIncoming
}

func (attempt *scriptedClientAttempt) Send(envelope *contractsv1.RuntimeControlEnvelope) error {
	return attempt.SendFrame(envelopeClientFrame(*envelope))
}

func (attempt *scriptedClientAttempt) Recv() (*contractsv1.RuntimeControlEnvelope, error) {
	frame, err := attempt.RecvFrame()
	if err != nil {
		return nil, err
	}
	if frame.Envelope == nil {
		return nil, errors.New("not an envelope")
	}
	return frame.Envelope, nil
}

func (attempt *scriptedClientAttempt) SendFrame(frame *contractsv1.RuntimeControlFrame) error {
	select {
	case <-attempt.ctx.Done():
		return attempt.ctx.Err()
	case attempt.sent <- cloneClientFrame(frame):
		return nil
	}
}

func (attempt *scriptedClientAttempt) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	select {
	case <-attempt.ctx.Done():
		return nil, attempt.ctx.Err()
	case incoming := <-attempt.incoming:
		return cloneClientFrame(incoming.frame), incoming.err
	}
}

func (attempt *scriptedClientAttempt) receive(frame *contractsv1.RuntimeControlFrame) {
	attempt.incoming <- scriptedClientIncoming{frame: cloneClientFrame(frame)}
}

func (attempt *scriptedClientAttempt) fail(err error) {
	attempt.incoming <- scriptedClientIncoming{err: err}
}

func (attempt *scriptedClientAttempt) Header() (metadata.MD, error) { return nil, nil }
func (attempt *scriptedClientAttempt) Trailer() metadata.MD         { return nil }
func (attempt *scriptedClientAttempt) CloseSend() error             { return nil }
func (attempt *scriptedClientAttempt) Context() context.Context     { return attempt.ctx }
func (attempt *scriptedClientAttempt) SendMsg(value any) error {
	frame, ok := value.(*contractsv1.RuntimeControlFrame)
	if !ok {
		return errors.New("unexpected message")
	}
	return attempt.SendFrame(frame)
}
func (attempt *scriptedClientAttempt) RecvMsg(value any) error {
	frame, err := attempt.RecvFrame()
	if err != nil {
		return err
	}
	target, ok := value.(*contractsv1.RuntimeControlFrame)
	if !ok {
		return errors.New("unexpected message")
	}
	*target = *frame
	return nil
}

func receiveSentFrame(t *testing.T, attempt *scriptedClientAttempt) *contractsv1.RuntimeControlFrame {
	t.Helper()
	select {
	case frame := <-attempt.sent:
		return frame
	case <-time.After(time.Second):
		t.Fatal("client did not send expected frame")
		return nil
	}
}

func receiveClientCommand(
	t *testing.T,
	commands <-chan application.WorkerControlCommand,
) application.WorkerControlCommand {
	t.Helper()
	select {
	case command := <-commands:
		return command
	case <-time.After(time.Second):
		t.Fatal("client did not deliver expected command")
		return application.WorkerControlCommand{}
	}
}

func cloneClientFrame(frame *contractsv1.RuntimeControlFrame) *contractsv1.RuntimeControlFrame {
	if frame == nil {
		return nil
	}
	encoded, _ := json.Marshal(frame)
	var clone contractsv1.RuntimeControlFrame
	_ = json.Unmarshal(encoded, &clone)
	return &clone
}
