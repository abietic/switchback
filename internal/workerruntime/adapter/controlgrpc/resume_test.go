package controlgrpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestWorkerControlCodecRejectsResumeFrameWithMissingRequiredField(t *testing.T) {
	const missingActiveTurn = `{
		"schema_version":"runtime.control.frame.v1",
		"frame_type":"resume_hello",
		"resume_hello":{
			"worker_session_id":"worker-session-1",
			"runtime_instance_id":"runtime-1",
			"runtime_generation":1,
			"channel_epoch":7,
			"shim_process_nonce":"process-1",
			"stream_attempt":1,
			"worker_tx_last_sequence":0,
			"worker_tx_backend_acked_sequence":0,
			"worker_tx_cursor_digest":"sha256:4f4de1293c07ebd69a11800878dd689311974c66f87a0582375b21c72bb787af",
			"worker_rx_last_sequence":0,
			"worker_rx_applied_sequence":0,
			"worker_rx_cursor_digest":"sha256:4f4de1293c07ebd69a11800878dd689311974c66f87a0582375b21c72bb787af"
		},
		"resume_ack":null,
		"resume_ready":null,
		"resume_complete":null,
		"envelope":null
	}`
	var frame contractsv1.RuntimeControlFrame
	if err := (strictJSONCodec{}).Unmarshal([]byte(missingActiveTurn), &frame); err == nil {
		t.Fatal("codec accepted a ResumeHello frame with a missing active_turn field")
	}
}

func TestWorkerControlResumeAckRequestsReplayAfterDurableEventCursor(t *testing.T) {
	plan := application.RuntimeControlReplayPlan{
		Channel: application.RuntimeControlChannelState{
			OwnerID: "server-connection-1", ResumeClaimRevision: 9,
			CommandCommittedDigest:        testDigest("commands"),
			EventCommittedSequence:        2,
			EventCommittedDigest:          testDigest("event-2"),
			EventAppliedSequence:          2,
			EventReplayCheckpointSequence: 5,
			EventReplayCheckpointDigest:   testDigest("event-5"),
			EventReplayTargetSequence:     5,
			EventReplayTargetDigest:       testDigest("event-5"),
		},
		EventReplayFrom:    3,
		EventReplayThrough: 5,
	}
	ack, err := acceptedResumeAck(plan)
	if err != nil {
		t.Fatal(err)
	}
	if ack.BackendRXCommittedWorkerSequence != 2 ||
		ack.ReplayWorkerFromSequence != 3 ||
		ack.BackendRXCursorDigest != testDigest("event-2") {
		t.Fatalf("ResumeAck replay cursor=%+v", ack)
	}
}

func TestWorkerControlServerRequiresResumeHelloAsFirstFrame(t *testing.T) {
	hub, rpc, cleanup := startTestControl(t)
	defer cleanup()
	credential := testCredential()
	if err := hub.RegisterCredential(credential); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(
		ctx,
		metadata.Pairs("authorization", "Bearer "+credential.Token),
	)
	stream, err := rpc.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	envelope := testEnvelope(
		credential.Identity,
		"event-before-hello",
		"runtime.ready",
		[]byte(`{"ready":true}`),
	)
	envelope.Sequence = 1
	envelope.SentAt = time.Now().UTC()
	if err := stream.Send(&envelope); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	if _, err := stream.RecvFrame(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("first non-Hello frame error=%v, want InvalidArgument", err)
	}
}

func TestWorkerControlCredentialConflictReturnsUnauthenticatedWithoutResumeAck(t *testing.T) {
	journal := &scriptedControlJournal{
		claim: func(
			_ context.Context,
			command application.ClaimRuntimeControlStreamCommand,
		) (application.RuntimeControlReplayPlan, error) {
			if command.Credential != "wrong-token" {
				t.Fatalf("claim credential=%q", command.Credential)
			}
			return application.RuntimeControlReplayPlan{},
				application.ErrRuntimeControlCredentialConflict
		},
	}
	hub := newHub(journal)
	rpc, cleanup := startDurableTestControl(t, hub)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(
		ctx,
		metadata.Pairs("authorization", "Bearer wrong-token"),
	)
	stream, err := rpc.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hello := testResumeHello(testCredential().Identity, 1)
	if err := stream.SendFrame(resumeHelloFrame(hello)); err != nil {
		t.Fatal(err)
	}
	if frame, err := stream.RecvFrame(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("received frame=%+v error=%v, want unauthenticated with no ResumeAck", frame, err)
	}
}

func TestWorkerControlProductionClientCompletesDurableServerHandshake(t *testing.T) {
	var (
		mu        sync.Mutex
		claimPlan application.RuntimeControlReplayPlan
	)
	journal := &scriptedControlJournal{}
	journal.claim = func(
		_ context.Context,
		command application.ClaimRuntimeControlStreamCommand,
	) (application.RuntimeControlReplayPlan, error) {
		if command.Credential != testCredential().Token {
			t.Fatalf("claim credential=%q", command.Credential)
		}
		mu.Lock()
		defer mu.Unlock()
		claimPlan = testReplayPlan(command)
		return claimPlan, nil
	}
	journal.complete = func(
		_ context.Context,
		command application.CompleteRuntimeControlResumeCommand,
	) (application.RuntimeControlChannelState, error) {
		mu.Lock()
		defer mu.Unlock()
		if command.Ready.ServerConnectionID != claimPlan.Channel.OwnerID ||
			command.Ready.AcknowledgedJournalRevision != claimPlan.Channel.ResumeClaimRevision {
			t.Fatalf("ResumeReady=%+v claim=%+v", command.Ready, claimPlan.Channel)
		}
		state := claimPlan.Channel
		state.Status = "connected"
		state.Revision = 6
		state.ResumeCompletedRevision = 6
		state.ResumeReadyDigest = testDigest("production-client-ready")
		claimPlan.Channel = state
		return state, nil
	}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		return claimPlan, nil
	}
	hub := newHub(journal)
	rpc, cleanup := startDurableTestControl(t, hub)
	defer cleanup()
	client, err := NewClient(rpc, testCredential())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- client.Run(ctx) }()
	select {
	case transition := <-client.ReadyTransitions():
		if !transition.Ready || transition.StreamAttempt != 1 {
			t.Fatalf("ready transition=%+v", transition)
		}
	case <-time.After(time.Second):
		t.Fatal("production Client.Run did not complete durable server handshake")
	}
	cancel()
	select {
	case err := <-runResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Client.Run result=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("production Client.Run did not stop")
	}
}

func TestWorkerControlSendCommitsJournalBeforeWakingStream(t *testing.T) {
	var (
		mu              sync.Mutex
		claimPlan       application.RuntimeControlReplayPlan
		pendingCommands []application.RuntimeControlMessage
		reserveReturned bool
	)
	journal := &scriptedControlJournal{}
	journal.claim = func(
		_ context.Context,
		command application.ClaimRuntimeControlStreamCommand,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		claimPlan = testReplayPlan(command)
		return claimPlan, nil
	}
	journal.complete = func(
		_ context.Context,
		command application.CompleteRuntimeControlResumeCommand,
	) (application.RuntimeControlChannelState, error) {
		mu.Lock()
		defer mu.Unlock()
		state := claimPlan.Channel
		state.Status = "connected"
		state.Revision = 6
		state.ResumeCompletedRevision = 6
		state.ResumeReadyDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		return state, nil
	}
	journal.reserve = func(
		_ context.Context,
		command application.ReserveRuntimeControlCommandCommand,
	) (application.RuntimeControlMessage, error) {
		envelope := contractsv1.RuntimeControlEnvelope{}
		if err := json.Unmarshal(command.Envelope, &envelope); err != nil {
			return application.RuntimeControlMessage{}, err
		}
		mu.Lock()
		defer mu.Unlock()
		envelope.Sequence = uint64(len(pendingCommands) + 1)
		envelope.AckSequence = 0
		envelope.SentAt = time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return application.RuntimeControlMessage{}, err
		}
		message := application.RuntimeControlMessage{
			Direction:      application.RuntimeControlDirectionCommand,
			Sequence:       envelope.Sequence,
			MessageID:      envelope.MessageID,
			IdempotencyKey: envelope.IdempotencyKey,
			Envelope:       encoded,
		}
		pendingCommands = append(pendingCommands, message)
		reserveReturned = true
		return message, nil
	}
	journal.replay = func(
		_ context.Context,
		_ application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		plan := claimPlan
		plan.PendingCommands = append(
			[]application.RuntimeControlMessage(nil),
			pendingCommands...,
		)
		return plan, nil
	}

	hub := newHub(journal)
	rpc, cleanup := startDurableTestControl(t, hub)
	defer cleanup()
	stream, _ := openResumedTestStream(t, rpc, testCredential().Token, testResumeHello(testCredential().Identity, 1))

	command := testEnvelope(
		testCredential().Identity,
		"command-after-resume",
		"runtime.inspect",
		json.RawMessage(`{"inspect":true}`),
	)
	var sendErr error
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		sendErr = hub.Send(context.Background(), application.WorkerControlCommand{Envelope: command})
		if !errors.Is(sendErr, ErrResumeInProgress) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if sendErr != nil {
		t.Fatalf("Hub.Send: %v", sendErr)
	}
	frame, err := stream.RecvFrame()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Envelope == nil || frame.Envelope.Sequence != 1 ||
		frame.Envelope.MessageID != "command-after-resume" {
		t.Fatalf("command frame=%+v", frame)
	}
	mu.Lock()
	committedBeforeSend := reserveReturned
	mu.Unlock()
	if !committedBeforeSend {
		t.Fatal("command reached stream before journal reservation returned")
	}
	_ = stream.CloseSend()
}

func TestWorkerControlQueuesResumeReadyUntilBackendReplayFinishes(t *testing.T) {
	identity := testCredential().Identity
	commandEnvelope := testEnvelope(
		identity,
		"replay-command-1",
		"runtime.inspect",
		json.RawMessage(`{"inspect":true}`),
	)
	commandEnvelope.Kind = "command"
	commandEnvelope.Sequence = 1
	commandEnvelope.SentAt = time.Date(2026, 7, 26, 13, 10, 0, 0, time.UTC)
	commandBytes, err := json.Marshal(commandEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	var claimPlan application.RuntimeControlReplayPlan
	completeCalled := make(chan struct{}, 1)
	journal := &scriptedControlJournal{}
	journal.claim = func(
		_ context.Context,
		command application.ClaimRuntimeControlStreamCommand,
	) (application.RuntimeControlReplayPlan, error) {
		claimPlan = testReplayPlan(command)
		claimPlan.Channel.CommandCommittedSequence = 1
		claimPlan.Channel.CommandCommittedDigest =
			"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		claimPlan.PendingCommands = []application.RuntimeControlMessage{{
			Direction: application.RuntimeControlDirectionCommand,
			Sequence:  1, MessageID: commandEnvelope.MessageID,
			IdempotencyKey: commandEnvelope.IdempotencyKey,
			Envelope:       commandBytes,
		}}
		return claimPlan, nil
	}
	journal.complete = func(
		_ context.Context,
		_ application.CompleteRuntimeControlResumeCommand,
	) (application.RuntimeControlChannelState, error) {
		completeCalled <- struct{}{}
		state := claimPlan.Channel
		state.CommandAcknowledgedSequence = 1
		state.Status = "connected"
		state.Revision = 6
		state.ResumeCompletedRevision = 6
		state.ResumeReadyDigest = testDigest("ready")
		return state, nil
	}
	hub := newHub(journal)
	stream := newBlockingConnectServer(testCredential().Token)
	stream.blockEnvelope = make(chan struct{})
	stream.envelopeStarted = make(chan struct{})
	hello := testResumeHello(identity, 1)
	stream.receive(resumeHelloFrame(hello))
	result := make(chan error, 1)
	go func() { result <- NewServer(hub).Connect(stream) }()

	ackFrame := stream.nextSent(t)
	if ackFrame.ResumeAck == nil {
		t.Fatalf("first frame=%+v, want ResumeAck", ackFrame)
	}
	select {
	case <-stream.envelopeStarted:
	case <-time.After(time.Second):
		t.Fatal("backend replay did not start")
	}
	ack := ackFrame.ResumeAck
	stream.receive(&contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeReady,
		ResumeReady: &contractsv1.RuntimeControlResumeReady{
			ServerConnectionID:              ack.ServerConnectionID,
			AcknowledgedJournalRevision:     ack.JournalRevision,
			WorkerTXReplayedThroughSequence: 0,
			WorkerTXCursorDigest:            hello.WorkerTXCursorDigest,
			WorkerRXReceivedThroughSequence: 1,
			WorkerRXAppliedSequence:         0,
			WorkerRXCursorDigest:            ack.BackendTXCursorDigest,
		},
	})
	select {
	case <-completeCalled:
		t.Fatal("ResumeReady crossed the backend replay send barrier")
	default:
	}
	close(stream.blockEnvelope)
	if replay := stream.nextSent(t); replay.Envelope == nil ||
		!reflect.DeepEqual(*replay.Envelope, commandEnvelope) {
		t.Fatalf("second frame=%+v, want exact journal replay %+v", replay, commandEnvelope)
	}
	if complete := stream.nextSent(t); complete.ResumeComplete == nil {
		t.Fatalf("third frame=%+v, want ResumeComplete", complete)
	}
	select {
	case <-completeCalled:
	case <-time.After(time.Second):
		t.Fatal("ResumeReady was not committed after replay completed")
	}
	close(stream.recv)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("server result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestWorkerControlDoesNotAcceptSendUntilResumeCompleteIsWritten(t *testing.T) {
	var (
		plan         application.RuntimeControlReplayPlan
		reserveCalls int
	)
	journal := &scriptedControlJournal{}
	journal.claim = func(
		_ context.Context,
		command application.ClaimRuntimeControlStreamCommand,
	) (application.RuntimeControlReplayPlan, error) {
		plan = testReplayPlan(command)
		return plan, nil
	}
	journal.complete = func(
		_ context.Context,
		_ application.CompleteRuntimeControlResumeCommand,
	) (application.RuntimeControlChannelState, error) {
		state := plan.Channel
		state.Status = "connected"
		state.Revision = 6
		state.ResumeCompletedRevision = 6
		state.ResumeReadyDigest =
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		return state, nil
	}
	journal.reserve = func(
		_ context.Context,
		command application.ReserveRuntimeControlCommandCommand,
	) (application.RuntimeControlMessage, error) {
		reserveCalls++
		return application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionCommand,
			Sequence:  uint64(reserveCalls), MessageID: command.MessageID,
			IdempotencyKey: command.IdempotencyKey,
			Envelope:       command.Envelope,
		}, nil
	}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		return plan, nil
	}
	hub := newHub(journal)
	stream := newBlockingConnectServer(testCredential().Token)
	stream.blockComplete = make(chan struct{})
	stream.completeStarted = make(chan struct{})
	hello := testResumeHello(testCredential().Identity, 1)
	stream.receive(resumeHelloFrame(hello))
	result := make(chan error, 1)
	go func() { result <- NewServer(hub).Connect(stream) }()
	ackFrame := stream.nextSent(t)
	ack := ackFrame.ResumeAck
	if ack == nil {
		t.Fatalf("first frame=%+v, want ResumeAck", ackFrame)
	}
	stream.receive(&contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeReady,
		ResumeReady: &contractsv1.RuntimeControlResumeReady{
			ServerConnectionID:              ack.ServerConnectionID,
			AcknowledgedJournalRevision:     ack.JournalRevision,
			WorkerTXReplayedThroughSequence: 0,
			WorkerTXCursorDigest:            hello.WorkerTXCursorDigest,
			WorkerRXReceivedThroughSequence: 0,
			WorkerRXAppliedSequence:         0,
			WorkerRXCursorDigest:            hello.WorkerRXCursorDigest,
		},
	})
	select {
	case <-stream.completeStarted:
	case <-time.After(time.Second):
		t.Fatal("ResumeComplete write did not start")
	}
	command := testEnvelope(
		testCredential().Identity,
		"command-complete-barrier",
		"runtime.inspect",
		json.RawMessage(`{"inspect":true}`),
	)
	if err := hub.Send(
		context.Background(),
		application.WorkerControlCommand{Envelope: command},
	); !errors.Is(err, ErrResumeInProgress) {
		t.Fatalf("Send while ResumeComplete is blocked=%v, want ErrResumeInProgress", err)
	}
	if reserveCalls != 0 {
		t.Fatalf("journal reserve calls before ResumeComplete=%d", reserveCalls)
	}
	close(stream.blockComplete)
	if complete := stream.nextSent(t); complete.ResumeComplete == nil {
		t.Fatalf("frame=%+v, want ResumeComplete", complete)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		err := hub.Send(
			context.Background(),
			application.WorkerControlCommand{Envelope: command},
		)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrResumeInProgress) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if reserveCalls != 1 {
		t.Fatalf("journal reserve calls after ResumeComplete=%d, want 1", reserveCalls)
	}
	close(stream.recv)
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("server did not stop after stream close")
	}
}

func TestWorkerControlStaleDetachDoesNotDisconnectReplacementOwner(t *testing.T) {
	var (
		mu                 sync.Mutex
		disconnectedFences []uint64
	)
	journal := &scriptedControlJournal{
		disconnect: func(
			_ context.Context,
			authority application.RuntimeControlAuthority,
		) (application.RuntimeControlChannelState, error) {
			mu.Lock()
			disconnectedFences = append(disconnectedFences, authority.OwnerFence)
			mu.Unlock()
			return application.RuntimeControlChannelState{}, nil
		},
	}
	hub := newHub(journal)
	identity := testCredential().Identity
	oldAuthority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	oldConnection := mustAttachClaimed(
		t, hub, identity, oldAuthority, "server-connection-old",
	)
	newAuthority := oldAuthority
	newAuthority.StreamAttempt = 2
	newAuthority.OwnerFence = 2
	newConnection := mustAttachClaimed(
		t, hub, identity, newAuthority, "server-connection-new",
	)

	if err := hub.detach(context.Background(), oldConnection, io.EOF); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotDisconnects := append([]uint64(nil), disconnectedFences...)
	mu.Unlock()
	if len(gotDisconnects) != 0 {
		t.Fatalf("stale detach disconnected fences=%v", gotDisconnects)
	}
	hub.mu.RLock()
	current := hub.connections[keyFor(identity)]
	hub.mu.RUnlock()
	if current != newConnection {
		t.Fatalf("stale detach removed replacement connection")
	}
	if err := hub.detach(context.Background(), newConnection, io.EOF); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotDisconnects = append([]uint64(nil), disconnectedFences...)
	mu.Unlock()
	if len(gotDisconnects) != 1 || gotDisconnects[0] != 2 {
		t.Fatalf("replacement disconnect fences=%v, want [2]", gotDisconnects)
	}
}

func TestWorkerControlCommittedUnappliedEventIsRedeliveredAfterHubRestart(t *testing.T) {
	identity := testCredential().Identity
	envelope := testEnvelope(
		identity,
		"durable-event-1",
		"runtime.progress",
		json.RawMessage(`{"step":"committed"}`),
	)
	envelope.Kind = "event"
	envelope.Sequence = 1
	envelope.SentAt = time.Date(2026, 7, 26, 13, 20, 0, 0, time.UTC)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	message := application.RuntimeControlMessage{
		Direction: application.RuntimeControlDirectionEvent,
		Sequence:  1, MessageID: envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded,
	}
	var (
		mu      sync.Mutex
		applied bool
	)
	journal := &scriptedControlJournal{}
	journal.replay = func(
		_ context.Context,
		_ application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		plan := application.RuntimeControlReplayPlan{}
		if !applied {
			plan.PendingEvents = []application.RuntimeControlMessage{message}
		}
		return plan, nil
	}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		if command.Sequence != 1 || command.MessageID != envelope.MessageID {
			t.Fatalf("apply command=%+v", command)
		}
		mu.Lock()
		applied = true
		mu.Unlock()
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}

	firstHub := newHub(journal)
	firstConnection := mustAttachClaimed(
		t, firstHub, identity, authority, "server-connection-first",
	)
	firstEvents, err := firstHub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	firstConnection.markLive(0)
	firstHub.startPendingEventPump(firstConnection)
	firstDelivery := receiveEvent(t, firstEvents)
	if firstDelivery.Delivery == nil || firstDelivery.Envelope.MessageID != envelope.MessageID {
		t.Fatalf("first delivery=%+v", firstDelivery)
	}
	firstConnection.close(io.EOF)

	secondHub := newHub(journal)
	authority.StreamAttempt = 2
	authority.OwnerFence = 2
	secondConnection := mustAttachClaimed(
		t, secondHub, identity, authority, "server-connection-second",
	)
	secondEvents, err := secondHub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	secondConnection.markLive(0)
	secondHub.startPendingEventPump(secondConnection)
	secondDelivery := receiveEvent(t, secondEvents)
	if secondDelivery.Delivery == nil ||
		secondDelivery.Envelope.MessageID != firstDelivery.Envelope.MessageID {
		t.Fatalf("redelivered event=%+v", secondDelivery)
	}
	if err := secondDelivery.Delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"business_committed"}`),
	); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	wasApplied := applied
	mu.Unlock()
	if !wasApplied {
		t.Fatal("delivery Apply did not advance durable inbox")
	}
	secondConnection.close(io.EOF)
}

func TestWorkerControlSameHubReconnectReusesPendingDeliveryWithCurrentAuthority(t *testing.T) {
	identity := testCredential().Identity
	envelope := testEnvelope(
		identity,
		"same-hub-event-1",
		"runtime.progress",
		json.RawMessage(`{"step":"pending"}`),
	)
	envelope.Kind = "event"
	envelope.Sequence = 1
	envelope.SentAt = time.Date(2026, 7, 26, 13, 25, 0, 0, time.UTC)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	message := application.RuntimeControlMessage{
		Direction: application.RuntimeControlDirectionEvent,
		Sequence:  1, MessageID: envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded,
	}
	var (
		mu      sync.Mutex
		applied bool
	)
	journal := &scriptedControlJournal{}
	journal.replay = func(
		_ context.Context,
		_ application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		plan := application.RuntimeControlReplayPlan{}
		if !applied {
			plan.PendingEvents = []application.RuntimeControlMessage{message}
		}
		return plan, nil
	}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		if command.Authority.OwnerFence != 2 {
			return application.RuntimeControlEventAcceptance{},
				application.ErrRuntimeControlStaleOwner
		}
		mu.Lock()
		applied = true
		mu.Unlock()
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	hub := newHub(journal)
	var observeCalls atomic.Int64
	if err := hub.SetEventObserver(controlEventObserverFunc(func(
		context.Context,
		application.WorkerControlEvent,
		time.Time,
	) error {
		observeCalls.Add(1)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	first := mustAttachClaimed(t, hub, identity, authority, "server-connection-first")
	first.markLive(0)
	events, err := hub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	hub.startPendingEventPump(first)
	for deadline := time.Now().Add(time.Second); len(events) == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if len(events) != 1 {
		t.Fatalf("first pending delivery count=%d, want 1", len(events))
	}
	first.close(io.EOF)

	authority.StreamAttempt = 2
	authority.OwnerFence = 2
	second := mustAttachClaimed(t, hub, identity, authority, "server-connection-second")
	second.markLive(0)
	hub.startPendingEventPump(second)
	time.Sleep(20 * time.Millisecond)
	delivery := receiveEvent(t, events)
	if err := delivery.Delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"business_committed"}`),
	); err != nil {
		t.Fatalf("buffered delivery did not adopt current authority: %v", err)
	}
	select {
	case duplicate := <-events:
		t.Fatalf("same-Hub reconnect published duplicate pending event: %+v", duplicate.Envelope)
	case <-time.After(50 * time.Millisecond):
	}
	if observeCalls.Load() != 1 {
		t.Fatalf("pending event observed %d times across reconnect, want 1", observeCalls.Load())
	}
	second.close(io.EOF)
}

func TestWorkerControlSameHubRedeliversAfterApplyDuringReplacementResume(t *testing.T) {
	identity := testCredential().Identity
	envelope := testEnvelope(
		identity,
		"same-hub-retry-event-1",
		"runtime.progress",
		json.RawMessage(`{"step":"pending"}`),
	)
	envelope.Kind = "event"
	envelope.Sequence = 1
	envelope.SentAt = time.Date(2026, 7, 26, 13, 30, 0, 0, time.UTC)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	message := application.RuntimeControlMessage{
		Direction: application.RuntimeControlDirectionEvent,
		Sequence:  1, MessageID: envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded,
	}
	var (
		mu      sync.Mutex
		applied bool
	)
	journal := &scriptedControlJournal{}
	journal.replay = func(
		_ context.Context,
		_ application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		if applied {
			return application.RuntimeControlReplayPlan{}, nil
		}
		return application.RuntimeControlReplayPlan{
			PendingEvents: []application.RuntimeControlMessage{message},
		}, nil
	}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		if command.Authority.OwnerFence != 2 {
			return application.RuntimeControlEventAcceptance{},
				application.ErrRuntimeControlStaleOwner
		}
		mu.Lock()
		applied = true
		mu.Unlock()
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	hub := newHub(journal)
	var observeCalls atomic.Int64
	if err := hub.SetEventObserver(controlEventObserverFunc(func(
		context.Context,
		application.WorkerControlEvent,
		time.Time,
	) error {
		observeCalls.Add(1)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	first := mustAttachClaimed(t, hub, identity, authority, "server-connection-first")
	first.markLive(0)
	events, err := hub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	hub.startPendingEventPump(first)
	firstDelivery := receiveEvent(t, events)
	first.close(io.EOF)

	authority.StreamAttempt = 2
	authority.OwnerFence = 2
	second := mustAttachClaimed(t, hub, identity, authority, "server-connection-second")
	if err := firstDelivery.Delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"business_committed"}`),
	); !errors.Is(err, ErrResumeInProgress) {
		t.Fatalf("Apply during replacement resume err=%v", err)
	}
	second.markLive(0)
	hub.startPendingEventPump(second)
	redelivered := receiveEvent(t, events)
	if redelivered.Envelope.MessageID != firstDelivery.Envelope.MessageID {
		t.Fatalf("redelivered event=%+v", redelivered.Envelope)
	}
	if err := redelivered.Delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"business_committed"}`),
	); err != nil {
		t.Fatal(err)
	}
	if observeCalls.Load() != 1 {
		t.Fatalf("pending event observed %d times across retry, want 1", observeCalls.Load())
	}
	second.close(io.EOF)
}

func TestWorkerControlReleaseForRetryRedeliversWithoutReconnect(t *testing.T) {
	identity := testCredential().Identity
	envelope := testEnvelope(
		identity,
		"same-hub-release-event-1",
		"runtime.progress",
		json.RawMessage(`{"step":"pending"}`),
	)
	envelope.Kind = "event"
	envelope.Sequence = 1
	envelope.SentAt = time.Date(2026, 7, 26, 13, 35, 0, 0, time.UTC)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	message := application.RuntimeControlMessage{
		Direction: application.RuntimeControlDirectionEvent,
		Sequence:  1, MessageID: envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded,
	}
	var applied atomic.Bool
	journal := &scriptedControlJournal{}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		if applied.Load() {
			return application.RuntimeControlReplayPlan{}, nil
		}
		return application.RuntimeControlReplayPlan{
			PendingEvents: []application.RuntimeControlMessage{message},
		}, nil
	}
	journal.apply = func(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		applied.Store(true)
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	hub := newHub(journal)
	var observeCalls atomic.Int64
	if err := hub.SetEventObserver(controlEventObserverFunc(func(
		context.Context,
		application.WorkerControlEvent,
		time.Time,
	) error {
		observeCalls.Add(1)
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	active := mustAttachClaimed(t, hub, identity, authority, "server-connection-first")
	active.markLive(0)
	events, err := hub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	hub.startPendingEventPump(active)
	first := receiveEvent(t, events)
	first.Delivery.ReleaseForRetry()
	second := receiveEvent(t, events)
	if second.Envelope.MessageID != first.Envelope.MessageID {
		t.Fatalf("redelivered event=%+v", second.Envelope)
	}
	if err := second.Delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"business_committed"}`),
	); err != nil {
		t.Fatal(err)
	}
	if observeCalls.Load() != 1 {
		t.Fatalf("pending event observed %d times across business retry, want 1", observeCalls.Load())
	}
	active.close(io.EOF)
}

func TestWorkerControlPendingEventPumpCancelsJournalOnDisconnect(t *testing.T) {
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	journal := &scriptedControlJournal{}
	journal.replay = func(
		ctx context.Context,
		_ application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return application.RuntimeControlReplayPlan{}, ctx.Err()
	}
	hub := newHub(journal)
	active := mustAttachClaimed(
		t, hub, identity, authority, "server-connection-cancellable-pump",
	)
	active.markLive(0)
	hub.startPendingEventPump(active)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("pending event pump did not enter journal ReplayPlan")
	}
	active.close(io.EOF)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel pending journal ReplayPlan")
	}
}

func TestWorkerControlLogicalFingerprintIncludesNormalizedKind(t *testing.T) {
	identity := testCredential().Identity
	event := testEnvelope(
		identity,
		"kind-sensitive-message",
		"runtime.progress",
		json.RawMessage(`{"step":1}`),
	)
	event.Kind = "event"
	heartbeat := event
	heartbeat.Kind = "heartbeat"
	if logicalFingerprint(event) == logicalFingerprint(heartbeat) {
		t.Fatal("logical fingerprint treated event and heartbeat as the same retry")
	}
}

func TestWorkerControlDeliveryCommitsActiveTurnEffectAndInboxThroughOneJournalCall(t *testing.T) {
	identity := testCredential().Identity
	proof := contractsv1.RuntimeControlActiveTurn{
		WorkerRunID:          "worker-run-1",
		AgentSessionID:       "agent-session-1",
		AgentTurnID:          "agent-turn-1",
		StartCommandSequence: 3,
		StartMessageID:       "start-message-1",
		StartIdempotencyKey:  "start-idempotency-1",
		StartEnvelopeDigest:  contractsv1.RuntimeControlCursorGenesisDigest,
		Phase:                contractsv1.RuntimeControlTurnEffectActive,
	}
	payload, err := json.Marshal(struct {
		ActiveTurn contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
	}{ActiveTurn: proof})
	if err != nil {
		t.Fatal(err)
	}
	envelope := testEnvelope(identity, "active-effect-1", "agent_turn.effect", payload)
	envelope.Sequence = 4
	envelope.SentAt = time.Now().UTC()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	var order []string
	journal := &scriptedControlJournal{}
	journal.reconcile = func(
		_ context.Context,
		command application.ReconcileRuntimeControlEffectCommand,
	) (application.RuntimeControlEffectResult, error) {
		t.Fatalf("transport must not split effect reconciliation from inbox Apply: %+v", command)
		return application.RuntimeControlEffectResult{}, nil
	}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		order = append(order, "apply")
		if command.Authority != authority ||
			command.Sequence != envelope.Sequence ||
			command.MessageID != envelope.MessageID ||
			command.IdempotencyKey != envelope.IdempotencyKey ||
			!bytes.Equal(
				command.Outcome,
				json.RawMessage(`{"status":"consumed","effect_disposition":"commit"}`),
			) {
			t.Fatalf("atomic effect inbox Apply=%+v", command)
		}
		return application.RuntimeControlEventAcceptance{}, nil
	}
	delivery := &journalEventDelivery{
		journal: journal, authority: authority,
		message: application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionEvent,
			Sequence:  envelope.Sequence, MessageID: envelope.MessageID,
			IdempotencyKey: envelope.IdempotencyKey, Envelope: encoded,
		},
		applied: make(chan struct{}),
	}
	if err := delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"consumed","effect_disposition":"commit"}`),
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"apply"}) {
		t.Fatalf("delivery calls=%v, want one atomic Apply", order)
	}

	var ignoredApplyCalls int
	ignoredJournal := &scriptedControlJournal{}
	ignoredJournal.reconcile = func(
		context.Context,
		application.ReconcileRuntimeControlEffectCommand,
	) (application.RuntimeControlEffectResult, error) {
		t.Fatal("ignored stale effect must not reconcile active Turn authority")
		return application.RuntimeControlEffectResult{}, nil
	}
	ignoredJournal.apply = func(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		ignoredApplyCalls++
		return application.RuntimeControlEventAcceptance{}, nil
	}
	ignoredDelivery := &journalEventDelivery{
		journal: ignoredJournal, authority: authority,
		message: application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionEvent,
			Sequence:  envelope.Sequence, MessageID: envelope.MessageID,
			IdempotencyKey: envelope.IdempotencyKey, Envelope: encoded,
		},
		applied: make(chan struct{}),
	}
	if err := ignoredDelivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"ignored_stale_runtime","effect_disposition":"ignore"}`),
	); err != nil {
		t.Fatal(err)
	}
	if ignoredApplyCalls != 1 {
		t.Fatalf("ignored effect inbox Apply calls=%d, want 1", ignoredApplyCalls)
	}
	if err := ignoredDelivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"missing_disposition"}`),
	); err == nil || !strings.Contains(err.Error(), "effect_disposition") {
		t.Fatalf("effect without explicit disposition err=%v", err)
	}
	if ignoredApplyCalls != 1 {
		t.Fatalf("invalid effect outcome advanced inbox; calls=%d", ignoredApplyCalls)
	}
}

func TestWorkerControlDeliveryCommitsTerminalWithoutMutableReplayPlan(t *testing.T) {
	identity := testCredential().Identity
	envelope := testEnvelope(
		identity,
		"terminal-effect-1",
		"agent_turn.terminal",
		json.RawMessage(`{"update_kind":"terminal","update":{"stop_reason":"end_turn"}}`),
	)
	envelope.Sequence = 5
	envelope.SentAt = time.Now().UTC()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		t.Fatal(err)
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	var order []string
	journal := &scriptedControlJournal{}
	journal.replay = func(
		_ context.Context,
		got application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		t.Fatalf("terminal delivery must not derive proof from mutable ReplayPlan: %+v", got)
		return application.RuntimeControlReplayPlan{}, nil
	}
	journal.reconcile = func(
		_ context.Context,
		command application.ReconcileRuntimeControlEffectCommand,
	) (application.RuntimeControlEffectResult, error) {
		t.Fatalf("transport must not split terminal reconciliation from inbox Apply: %+v", command)
		return application.RuntimeControlEffectResult{}, nil
	}
	journal.apply = func(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		order = append(order, "apply")
		return application.RuntimeControlEventAcceptance{}, nil
	}
	delivery := &journalEventDelivery{
		journal: journal, authority: authority,
		message: application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionEvent,
			Sequence:  envelope.Sequence, MessageID: envelope.MessageID,
			IdempotencyKey: envelope.IdempotencyKey, Fingerprint: digest, Envelope: encoded,
		},
		applied: make(chan struct{}),
	}
	if err := delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"stage_committed","effect_disposition":"commit"}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"stage_committed","effect_disposition":"commit"}`),
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"apply", "apply"}) {
		t.Fatalf("delivery calls=%v, want atomic Apply on each idempotent attempt", order)
	}
}

func TestWorkerControlDeliveryDoesNotHideConflictingDuplicateOutcome(t *testing.T) {
	var calls int
	journal := &scriptedControlJournal{}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		calls++
		if calls == 2 {
			return application.RuntimeControlEventAcceptance{},
				application.ErrRuntimeControlMessageConflict
		}
		return application.RuntimeControlEventAcceptance{
			Message: application.RuntimeControlMessage{
				Sequence:  command.Sequence,
				MessageID: command.MessageID,
			},
		}, nil
	}
	delivery := &journalEventDelivery{
		journal: journal,
		authority: application.RuntimeControlAuthority{
			WorkerSessionID: "worker-session-1", RuntimeInstanceID: "runtime-instance-1",
			RuntimeGeneration: 1, ChannelEpoch: 1,
			ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
		},
		message: application.RuntimeControlMessage{
			Sequence: 1, MessageID: "event-1", IdempotencyKey: "event-1",
		},
		applied: make(chan struct{}),
	}
	if err := delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"first"}`),
	); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"conflicting"}`),
	); !errors.Is(err, application.ErrRuntimeControlMessageConflict) {
		t.Fatalf("conflicting duplicate outcome err=%v", err)
	}
	if calls != 2 {
		t.Fatalf("journal Apply calls=%d, want 2", calls)
	}
}

func TestWorkerControlDurableDuplicateRepairsMissingTransportACK(t *testing.T) {
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	var reserveCalls int
	journal := &scriptedControlJournal{}
	journal.accept = func(
		_ context.Context,
		command application.AcceptRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		return application.RuntimeControlEventAcceptance{
			Message: application.RuntimeControlMessage{
				Direction: application.RuntimeControlDirectionEvent,
				Sequence:  command.Sequence, MessageID: command.MessageID,
				IdempotencyKey: command.IdempotencyKey, Duplicate: true,
			},
		}, nil
	}
	journal.reserve = func(
		_ context.Context,
		command application.ReserveRuntimeControlCommandCommand,
	) (application.RuntimeControlMessage, error) {
		reserveCalls++
		return application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionCommand,
			Sequence:  1, MessageID: command.MessageID,
			IdempotencyKey: command.IdempotencyKey, Duplicate: true,
		}, nil
	}
	hub := newHub(journal)
	active := mustAttachClaimed(t, hub, identity, authority, "server-connection-duplicate")
	active.markLive(0)
	event := testEnvelope(
		identity,
		"duplicate-event-1",
		"runtime.progress",
		json.RawMessage(`{"step":1}`),
	)
	event.Kind = "event"
	event.Sequence = 1
	event.SentAt = time.Now().UTC()
	if err := hub.acceptEvent(context.Background(), active, event, true); err != nil {
		t.Fatal(err)
	}
	if reserveCalls != 1 {
		t.Fatalf("transport ACK repair reservations=%d, want 1", reserveCalls)
	}
	active.close(io.EOF)
}

func TestWorkerControlACKEnvelopeDoesNotGenerateAnotherACK(t *testing.T) {
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	var (
		mu           sync.Mutex
		pending      *application.RuntimeControlMessage
		reserveCalls atomic.Int64
		applyCalls   atomic.Int64
	)
	journal := &scriptedControlJournal{}
	journal.accept = func(
		_ context.Context,
		command application.AcceptRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		message := application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionEvent,
			Sequence:  command.Sequence, MessageID: command.MessageID,
			IdempotencyKey: command.IdempotencyKey,
			Envelope:       command.Envelope,
		}
		mu.Lock()
		pending = &message
		mu.Unlock()
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	journal.apply = func(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		applyCalls.Add(1)
		mu.Lock()
		pending = nil
		mu.Unlock()
		return application.RuntimeControlEventAcceptance{}, nil
	}
	journal.reserve = func(
		context.Context,
		application.ReserveRuntimeControlCommandCommand,
	) (application.RuntimeControlMessage, error) {
		reserveCalls.Add(1)
		return application.RuntimeControlMessage{}, nil
	}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		if pending == nil {
			return application.RuntimeControlReplayPlan{}, nil
		}
		return application.RuntimeControlReplayPlan{
			PendingEvents: []application.RuntimeControlMessage{*pending},
		}, nil
	}
	hub := newHub(journal)
	active := mustAttachClaimed(
		t, hub, identity, authority, "server-connection-ack",
	)
	active.markLive(0)
	hub.startPendingEventPump(active)
	ack := testEnvelope(
		identity,
		"worker-ack-1",
		"runtime.control.ack",
		json.RawMessage(`{"through_sequence":1}`),
	)
	ack.Kind = "ack"
	ack.Sequence = 1
	ack.SentAt = time.Date(2026, 7, 26, 13, 30, 0, 0, time.UTC)
	if err := hub.acceptEvent(context.Background(), active, ack, true); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); applyCalls.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if applyCalls.Load() != 1 || reserveCalls.Load() != 0 {
		t.Fatalf(
			"apply calls=%d reserve ACK calls=%d, want 1/0",
			applyCalls.Load(),
			reserveCalls.Load(),
		)
	}
	active.close(io.EOF)
}

func TestWorkerControlAdapterACKWaitsBehindEarlierBusinessEventApplication(t *testing.T) {
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	var (
		mu             sync.Mutex
		inbox          []application.RuntimeControlMessage
		appliedThrough uint64
	)
	journal := &scriptedControlJournal{}
	journal.accept = func(
		_ context.Context,
		command application.AcceptRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		message := application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionEvent,
			Sequence:  command.Sequence,
			MessageID: command.MessageID, IdempotencyKey: command.IdempotencyKey,
			Envelope: command.Envelope, CreatedAt: time.Now().UTC(),
		}
		mu.Lock()
		inbox = append(inbox, message)
		mu.Unlock()
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		var pending []application.RuntimeControlMessage
		for _, message := range inbox {
			if message.Sequence > appliedThrough {
				pending = append(pending, message)
			}
		}
		return application.RuntimeControlReplayPlan{PendingEvents: pending}, nil
	}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		mu.Lock()
		defer mu.Unlock()
		if command.Sequence != appliedThrough+1 {
			return application.RuntimeControlEventAcceptance{},
				application.ErrRuntimeControlSequenceGap
		}
		appliedThrough = command.Sequence
		return application.RuntimeControlEventAcceptance{}, nil
	}
	hub := newHub(journal)
	active := mustAttachClaimed(
		t, hub, identity, authority, "server-connection-ordered-inbox",
	)
	active.markLive(0)
	events, err := hub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	hub.startPendingEventPump(active)

	business := testEnvelope(
		identity,
		"business-event-1",
		"runtime.progress",
		json.RawMessage(`{"step":1}`),
	)
	business.Kind = "event"
	business.Sequence = 1
	business.SentAt = time.Now().UTC()
	if err := hub.acceptEvent(context.Background(), active, business, false); err != nil {
		t.Fatal(err)
	}
	ack := testEnvelope(
		identity,
		"worker-ack-2",
		"runtime.control.ack",
		json.RawMessage(`{"through_sequence":1}`),
	)
	ack.Kind = "ack"
	ack.Sequence = 2
	ack.SentAt = time.Now().UTC()
	if err := hub.acceptEvent(context.Background(), active, ack, false); err != nil {
		t.Fatalf("ACK overtook pending business event: %v", err)
	}
	delivered := receiveEvent(t, events)
	mu.Lock()
	beforeBusinessApply := appliedThrough
	mu.Unlock()
	if beforeBusinessApply != 0 {
		t.Fatalf("adapter ACK advanced applied cursor to %d before business event", beforeBusinessApply)
	}
	if err := delivered.Delivery.Apply(
		context.Background(),
		json.RawMessage(`{"status":"business_committed"}`),
	); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		mu.Lock()
		done := appliedThrough == 2
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	finalApplied := appliedThrough
	mu.Unlock()
	if finalApplied != 2 {
		t.Fatalf("ordered event cursor=%d, want ACK auto-applied at 2", finalApplied)
	}
	active.close(io.EOF)
}

func TestPendingTerminalBlocksFollowingHeartbeatUntilApplied(t *testing.T) {
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	terminal := testEnvelope(
		identity,
		"pending-terminal-1",
		"agent_turn.terminal",
		json.RawMessage(
			`{"update_kind":"terminal","update":{"stop_reason":"end_turn"}}`,
		),
	)
	terminal.Kind = "event"
	terminal.Sequence = 1
	terminal.SentAt = time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	heartbeat := testEnvelope(
		identity,
		"heartbeat-behind-terminal-2",
		"runtime.heartbeat",
		json.RawMessage(`{"heartbeat_sequence":2}`),
	)
	heartbeat.Kind = "heartbeat"
	heartbeat.Sequence = 2
	heartbeat.SentAt = terminal.SentAt.Add(time.Second)

	toMessage := func(
		envelope contractsv1.RuntimeControlEnvelope,
	) application.RuntimeControlMessage {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return application.RuntimeControlMessage{
			Direction: application.RuntimeControlDirectionEvent,
			Sequence:  envelope.Sequence,
			MessageID: envelope.MessageID, IdempotencyKey: envelope.IdempotencyKey,
			Envelope: encoded, CreatedAt: envelope.SentAt,
		}
	}
	inbox := []application.RuntimeControlMessage{
		toMessage(terminal),
		toMessage(heartbeat),
	}
	var (
		mu             sync.Mutex
		appliedThrough uint64
		appliedOrder   []uint64
	)
	journal := &scriptedControlJournal{}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		var pending []application.RuntimeControlMessage
		for _, message := range inbox {
			if message.Sequence > appliedThrough {
				pending = append(pending, message)
			}
		}
		return application.RuntimeControlReplayPlan{
			PendingEvents: pending,
		}, nil
	}
	journal.apply = func(
		_ context.Context,
		command application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		mu.Lock()
		defer mu.Unlock()
		if command.Sequence != appliedThrough+1 {
			return application.RuntimeControlEventAcceptance{},
				application.ErrRuntimeControlSequenceGap
		}
		appliedThrough = command.Sequence
		appliedOrder = append(appliedOrder, command.Sequence)
		return application.RuntimeControlEventAcceptance{}, nil
	}

	heartbeatObserved := make(chan struct{}, 1)
	hub := newHub(journal)
	if err := hub.SetEventObserver(controlEventObserverFunc(func(
		_ context.Context,
		event application.WorkerControlEvent,
		_ time.Time,
	) error {
		if event.Envelope.MessageType == "runtime.heartbeat" {
			heartbeatObserved <- struct{}{}
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	active := mustAttachClaimed(
		t,
		hub,
		identity,
		authority,
		"server-connection-terminal-heartbeat-order",
	)
	active.markLive(0)
	events, err := hub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	hub.startPendingEventPump(active)

	delivered := receiveEvent(t, events)
	if delivered.Envelope.MessageID != terminal.MessageID ||
		delivered.Delivery == nil {
		t.Fatalf("first business delivery=%+v", delivered)
	}
	select {
	case <-heartbeatObserved:
		t.Fatal("heartbeat was observed before the earlier terminal event applied")
	case <-time.After(50 * time.Millisecond):
	}
	mu.Lock()
	beforeTerminalApply := appliedThrough
	mu.Unlock()
	if beforeTerminalApply != 0 {
		t.Fatalf(
			"heartbeat advanced ordered cursor to %d before terminal Apply",
			beforeTerminalApply,
		)
	}

	if err := delivered.Delivery.Apply(
		context.Background(),
		json.RawMessage(
			`{"status":"terminal_committed","effect_disposition":"commit"}`,
		),
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-heartbeatObserved:
	case <-time.After(time.Second):
		t.Fatal("heartbeat was not observed after terminal Apply")
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		mu.Lock()
		done := appliedThrough == 2
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	finalApplied := appliedThrough
	finalOrder := append([]uint64(nil), appliedOrder...)
	mu.Unlock()
	if finalApplied != 2 || !reflect.DeepEqual(finalOrder, []uint64{1, 2}) {
		t.Fatalf(
			"ordered terminal/heartbeat Apply=%d order=%v, want 2/[1 2]",
			finalApplied,
			finalOrder,
		)
	}
	select {
	case published := <-events:
		t.Fatalf("heartbeat entered business event queue: %+v", published)
	default:
	}
	active.close(io.EOF)
}

func TestWorkerControlFencePersistsBeforeClosingLiveConnection(t *testing.T) {
	fenceStarted := make(chan struct{})
	allowFenceCommit := make(chan struct{})
	journal := &scriptedControlJournal{
		fence: func(
			context.Context,
			application.FenceRuntimeControlChannelCommand,
		) (application.RuntimeControlChannelState, error) {
			close(fenceStarted)
			<-allowFenceCommit
			return application.RuntimeControlChannelState{Status: "fenced"}, nil
		},
	}
	hub := newHub(journal)
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	active := mustAttachClaimed(
		t, hub, identity, authority, "server-connection-fence",
	)
	result := make(chan error, 1)
	go func() {
		result <- hub.Fence(
			context.Background(),
			identity.RuntimeInstanceID,
			uint64(identity.RuntimeGeneration),
			"security termination",
		)
	}()
	select {
	case <-fenceStarted:
	case <-time.After(time.Second):
		t.Fatal("persistent fence did not start")
	}
	select {
	case <-active.done:
		t.Fatal("connection closed before persistent fence committed")
	default:
	}
	close(allowFenceCommit)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case <-active.done:
	case <-time.After(time.Second):
		t.Fatal("connection remained live after persistent fence committed")
	}
	authority.StreamAttempt = 2
	authority.OwnerFence = 2
	if resurrected, err := hub.attachClaimed(
		identity,
		authority,
		"server-connection-after-fence",
	); !errors.Is(err, ErrStaleRuntime) || resurrected != nil {
		t.Fatalf("attach after fence=(%v,%v), want nil/ErrStaleRuntime", resurrected, err)
	}
}

func TestWorkerControlDurableHeartbeatIsObservedAtJournalReceiveTimeWithoutPublication(t *testing.T) {
	identity := testCredential().Identity
	receivedAt := time.Date(2026, 7, 26, 13, 40, 0, 0, time.UTC)
	envelope := testEnvelope(
		identity,
		"heartbeat-durable-1",
		"runtime.heartbeat",
		json.RawMessage(`{"heartbeat_sequence":1}`),
	)
	envelope.Kind = "heartbeat"
	envelope.Sequence = 1
	envelope.SentAt = receivedAt.Add(-time.Second)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	message := application.RuntimeControlMessage{
		Direction: application.RuntimeControlDirectionEvent,
		Sequence:  1, MessageID: envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded, CreatedAt: receivedAt,
	}
	var applied atomic.Bool
	journal := &scriptedControlJournal{}
	journal.replay = func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error) {
		if applied.Load() {
			return application.RuntimeControlReplayPlan{}, nil
		}
		return application.RuntimeControlReplayPlan{
			PendingEvents: []application.RuntimeControlMessage{message},
		}, nil
	}
	journal.apply = func(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error) {
		applied.Store(true)
		return application.RuntimeControlEventAcceptance{}, nil
	}
	observed := make(chan time.Time, 1)
	hub := newHub(journal)
	if err := hub.SetEventObserver(controlEventObserverFunc(func(
		_ context.Context,
		event application.WorkerControlEvent,
		at time.Time,
	) error {
		if event.Envelope.MessageID != envelope.MessageID {
			t.Fatalf("observed event=%+v", event)
		}
		observed <- at
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}
	active := mustAttachClaimed(
		t, hub, identity, authority, "server-connection-heartbeat",
	)
	events, err := hub.Events(
		context.Background(),
		identity.RuntimeInstanceID,
		uint64(identity.RuntimeGeneration),
	)
	if err != nil {
		t.Fatal(err)
	}
	active.markLive(0)
	hub.startPendingEventPump(active)
	select {
	case at := <-observed:
		if !at.Equal(receivedAt) {
			t.Fatalf("heartbeat observed at %s, want %s", at, receivedAt)
		}
	case <-time.After(time.Second):
		t.Fatal("durable heartbeat was not observed")
	}
	for deadline := time.Now().Add(time.Second); !applied.Load() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if !applied.Load() {
		t.Fatal("heartbeat inbox entry was not applied")
	}
	select {
	case published := <-events:
		t.Fatalf("heartbeat entered business event queue: %+v", published)
	default:
	}
	active.close(io.EOF)
}

func TestWorkerControlDisconnectDoesNotPublishFailureEvent(t *testing.T) {
	observed := make(chan application.WorkerControlEvent, 1)
	journal := &scriptedControlJournal{}
	hub := newHub(journal)
	if err := hub.SetEventObserver(controlEventObserverFunc(
		func(
			_ context.Context,
			event application.WorkerControlEvent,
			_ time.Time,
		) error {
			observed <- event
			return nil
		},
	)); err != nil {
		t.Fatal(err)
	}
	identity := testCredential().Identity
	active := mustAttachClaimed(t, hub, identity, application.RuntimeControlAuthority{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
		ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1,
	}, "server-connection-disconnect")
	if err := hub.detach(context.Background(), active, io.EOF); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-observed:
		t.Fatalf("ordinary disconnect published a failure event: %+v", event)
	default:
	}
}

func openResumedTestStream(
	t *testing.T,
	rpc WorkerControlRPCClient,
	token string,
	hello contractsv1.RuntimeControlResumeHello,
) (WorkerControlRPC_ConnectClient, contractsv1.RuntimeControlResumeComplete) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	stream, err := rpc.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SendFrame(resumeHelloFrame(hello)); err != nil {
		t.Fatal(err)
	}
	ackFrame, err := stream.RecvFrame()
	if err != nil {
		t.Fatal(err)
	}
	if ackFrame.ResumeAck == nil || !ackFrame.ResumeAck.Accepted {
		t.Fatalf("ResumeAck=%+v", ackFrame)
	}
	ack := ackFrame.ResumeAck
	ready := contractsv1.RuntimeControlResumeReady{
		ServerConnectionID:              ack.ServerConnectionID,
		AcknowledgedJournalRevision:     ack.JournalRevision,
		WorkerTXReplayedThroughSequence: hello.WorkerTXLastSequence,
		WorkerTXCursorDigest:            hello.WorkerTXCursorDigest,
		WorkerRXReceivedThroughSequence: ack.BackendTXLastCommandSequence,
		WorkerRXAppliedSequence:         ack.BackendTXWorkerAppliedSequence,
		WorkerRXCursorDigest:            ack.BackendTXCursorDigest,
		ActiveTurn:                      ack.AuthoritativeStart,
	}
	if err := stream.SendFrame(&contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeReady,
		ResumeReady:   &ready,
	}); err != nil {
		t.Fatal(err)
	}
	completeFrame, err := stream.RecvFrame()
	if err != nil {
		t.Fatal(err)
	}
	if completeFrame.ResumeComplete == nil {
		t.Fatalf("ResumeComplete frame=%+v", completeFrame)
	}
	return stream, *completeFrame.ResumeComplete
}

func startDurableTestControl(
	t *testing.T,
	hub *Hub,
) (WorkerControlRPCClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	RegisterWorkerControlRPCServer(server, NewServer(hub))
	go func() { _ = server.Serve(listener) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	connection, err := grpc.DialContext(
		ctx,
		"passthrough:///worker-control",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	cancel()
	if err != nil {
		server.Stop()
		_ = listener.Close()
		t.Fatalf("dial bufconn: %v", err)
	}
	return NewWorkerControlRPCClient(connection), func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
}

func testResumeHello(identity RuntimeIdentity, attempt uint64) contractsv1.RuntimeControlResumeHello {
	return contractsv1.RuntimeControlResumeHello{
		WorkerSessionID:              contractsv1.WorkerSessionID(identity.WorkerSessionID),
		RuntimeInstanceID:            contractsv1.RuntimeInstanceID(identity.RuntimeInstanceID),
		RuntimeGeneration:            identity.RuntimeGeneration,
		ChannelEpoch:                 identity.ChannelEpoch,
		ShimProcessNonce:             "shim-process-1",
		StreamAttempt:                attempt,
		WorkerTXCursorDigest:         contractsv1.RuntimeControlCursorGenesisDigest,
		WorkerRXCursorDigest:         contractsv1.RuntimeControlCursorGenesisDigest,
		WorkerTXLastSequence:         0,
		WorkerTXBackendAckedSequence: 0,
		WorkerRXLastSequence:         0,
		WorkerRXAppliedSequence:      0,
	}
}

func testDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func resumeHelloFrame(
	hello contractsv1.RuntimeControlResumeHello,
) *contractsv1.RuntimeControlFrame {
	return &contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeHello,
		ResumeHello:   &hello,
	}
}

func testReplayPlan(
	command application.ClaimRuntimeControlStreamCommand,
) application.RuntimeControlReplayPlan {
	return application.RuntimeControlReplayPlan{
		Channel: application.RuntimeControlChannelState{
			WorkerSessionID:               command.WorkerSessionID,
			RuntimeInstanceID:             command.RuntimeInstanceID,
			RuntimeGeneration:             command.RuntimeGeneration,
			ChannelEpoch:                  command.ChannelEpoch,
			ProcessNonce:                  command.ProcessNonce,
			StreamAttempt:                 command.StreamAttempt,
			OwnerID:                       command.OwnerID,
			OwnerFence:                    command.StreamAttempt,
			CommandCommittedDigest:        contractsv1.RuntimeControlCursorGenesisDigest,
			EventCommittedDigest:          contractsv1.RuntimeControlCursorGenesisDigest,
			EventReplayCheckpointDigest:   contractsv1.RuntimeControlCursorGenesisDigest,
			EventReplayTargetDigest:       contractsv1.RuntimeControlCursorGenesisDigest,
			ResumeClaimRevision:           5,
			Revision:                      5,
			Status:                        "resuming",
			EventReplayCheckpointSequence: 0,
		},
		EventReplayFrom: 1,
	}
}

func mustAttachClaimed(
	t *testing.T,
	hub *Hub,
	identity RuntimeIdentity,
	authority application.RuntimeControlAuthority,
	connectionID string,
) *connection {
	t.Helper()
	active, err := hub.attachClaimed(identity, authority, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

type scriptedControlJournal struct {
	mu sync.Mutex

	bind func(
		context.Context,
		application.BindRuntimeControlChannelCommand,
	) (application.RuntimeControlChannelState, error)
	claim func(
		context.Context,
		application.ClaimRuntimeControlStreamCommand,
	) (application.RuntimeControlReplayPlan, error)
	reserve func(
		context.Context,
		application.ReserveRuntimeControlCommandCommand,
	) (application.RuntimeControlMessage, error)
	accept func(
		context.Context,
		application.AcceptRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error)
	apply func(
		context.Context,
		application.ApplyRuntimeControlEventCommand,
	) (application.RuntimeControlEventAcceptance, error)
	reconcile func(
		context.Context,
		application.ReconcileRuntimeControlEffectCommand,
	) (application.RuntimeControlEffectResult, error)
	disconnect func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlChannelState, error)
	fence func(
		context.Context,
		application.FenceRuntimeControlChannelCommand,
	) (application.RuntimeControlChannelState, error)
	complete func(
		context.Context,
		application.CompleteRuntimeControlResumeCommand,
	) (application.RuntimeControlChannelState, error)
	replay func(
		context.Context,
		application.RuntimeControlAuthority,
	) (application.RuntimeControlReplayPlan, error)
}

func (journal *scriptedControlJournal) Bind(
	ctx context.Context,
	command application.BindRuntimeControlChannelCommand,
) (application.RuntimeControlChannelState, error) {
	if journal.bind != nil {
		return journal.bind(ctx, command)
	}
	return application.RuntimeControlChannelState{}, nil
}

func (journal *scriptedControlJournal) Claim(
	ctx context.Context,
	command application.ClaimRuntimeControlStreamCommand,
) (application.RuntimeControlReplayPlan, error) {
	if journal.claim == nil {
		return application.RuntimeControlReplayPlan{}, errors.New("unexpected journal Claim")
	}
	return journal.claim(ctx, command)
}

func (journal *scriptedControlJournal) ReserveCommand(
	ctx context.Context,
	command application.ReserveRuntimeControlCommandCommand,
) (application.RuntimeControlMessage, error) {
	if journal.reserve == nil {
		return application.RuntimeControlMessage{}, errors.New("unexpected journal ReserveCommand")
	}
	return journal.reserve(ctx, command)
}

func (journal *scriptedControlJournal) AcceptEvent(
	ctx context.Context,
	command application.AcceptRuntimeControlEventCommand,
) (application.RuntimeControlEventAcceptance, error) {
	if journal.accept == nil {
		return application.RuntimeControlEventAcceptance{}, errors.New("unexpected journal AcceptEvent")
	}
	return journal.accept(ctx, command)
}

func (journal *scriptedControlJournal) ApplyEvent(
	ctx context.Context,
	command application.ApplyRuntimeControlEventCommand,
) (application.RuntimeControlEventAcceptance, error) {
	if journal.apply == nil {
		return application.RuntimeControlEventAcceptance{}, errors.New("unexpected journal ApplyEvent")
	}
	return journal.apply(ctx, command)
}

func (journal *scriptedControlJournal) ReconcileEffect(
	ctx context.Context,
	command application.ReconcileRuntimeControlEffectCommand,
) (application.RuntimeControlEffectResult, error) {
	if journal.reconcile == nil {
		return application.RuntimeControlEffectResult{}, errors.New("unexpected journal ReconcileEffect")
	}
	return journal.reconcile(ctx, command)
}

func (journal *scriptedControlJournal) Disconnect(
	ctx context.Context,
	authority application.RuntimeControlAuthority,
) (application.RuntimeControlChannelState, error) {
	if journal.disconnect != nil {
		return journal.disconnect(ctx, authority)
	}
	return application.RuntimeControlChannelState{}, nil
}

func (journal *scriptedControlJournal) Fence(
	ctx context.Context,
	command application.FenceRuntimeControlChannelCommand,
) (application.RuntimeControlChannelState, error) {
	if journal.fence != nil {
		return journal.fence(ctx, command)
	}
	return application.RuntimeControlChannelState{}, nil
}

func (journal *scriptedControlJournal) CompleteResume(
	ctx context.Context,
	command application.CompleteRuntimeControlResumeCommand,
) (application.RuntimeControlChannelState, error) {
	if journal.complete == nil {
		return application.RuntimeControlChannelState{}, errors.New("unexpected journal CompleteResume")
	}
	return journal.complete(ctx, command)
}

func (journal *scriptedControlJournal) ReplayPlan(
	ctx context.Context,
	authority application.RuntimeControlAuthority,
) (application.RuntimeControlReplayPlan, error) {
	if journal.replay != nil {
		return journal.replay(ctx, authority)
	}
	return application.RuntimeControlReplayPlan{}, nil
}

var _ durableRuntimeControlJournal = (*scriptedControlJournal)(nil)

type controlEventObserverFunc func(
	context.Context,
	application.WorkerControlEvent,
	time.Time,
) error

func (observe controlEventObserverFunc) ObserveWorkerControlEvent(
	ctx context.Context,
	event application.WorkerControlEvent,
	receivedAt time.Time,
) error {
	return observe(ctx, event, receivedAt)
}

type blockingConnectServer struct {
	ctx context.Context

	recv chan *contractsv1.RuntimeControlFrame
	sent chan *contractsv1.RuntimeControlFrame

	blockEnvelope   chan struct{}
	envelopeStarted chan struct{}
	envelopeOnce    sync.Once
	blockComplete   chan struct{}
	completeStarted chan struct{}
	completeOnce    sync.Once
}

func newBlockingConnectServer(token string) *blockingConnectServer {
	return &blockingConnectServer{
		ctx: metadata.NewIncomingContext(
			context.Background(),
			metadata.Pairs("authorization", "Bearer "+token),
		),
		recv: make(chan *contractsv1.RuntimeControlFrame, controlQueueCapacity),
		sent: make(chan *contractsv1.RuntimeControlFrame, controlQueueCapacity),
	}
}

func (stream *blockingConnectServer) receive(frame *contractsv1.RuntimeControlFrame) {
	stream.recv <- frame
}

func (stream *blockingConnectServer) nextSent(t *testing.T) *contractsv1.RuntimeControlFrame {
	t.Helper()
	select {
	case frame := <-stream.sent:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server frame")
		return nil
	}
}

func (stream *blockingConnectServer) SendFrame(frame *contractsv1.RuntimeControlFrame) error {
	if frame.Envelope != nil && stream.blockEnvelope != nil {
		stream.envelopeOnce.Do(func() {
			if stream.envelopeStarted != nil {
				close(stream.envelopeStarted)
			}
		})
		<-stream.blockEnvelope
	}
	if frame.ResumeComplete != nil && stream.blockComplete != nil {
		stream.completeOnce.Do(func() {
			if stream.completeStarted != nil {
				close(stream.completeStarted)
			}
		})
		<-stream.blockComplete
	}
	stream.sent <- frame
	return nil
}

func (stream *blockingConnectServer) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	frame, ok := <-stream.recv
	if !ok {
		return nil, io.EOF
	}
	return frame, nil
}

func (stream *blockingConnectServer) Send(envelope *contractsv1.RuntimeControlEnvelope) error {
	return stream.SendFrame(envelopeFrame(envelope))
}

func (stream *blockingConnectServer) Recv() (*contractsv1.RuntimeControlEnvelope, error) {
	frame, err := stream.RecvFrame()
	if err != nil {
		return nil, err
	}
	if frame.Envelope == nil {
		return nil, errors.New("received non-envelope frame")
	}
	return frame.Envelope, nil
}

func (stream *blockingConnectServer) SetHeader(metadata.MD) error  { return nil }
func (stream *blockingConnectServer) SendHeader(metadata.MD) error { return nil }
func (stream *blockingConnectServer) SetTrailer(metadata.MD)       {}
func (stream *blockingConnectServer) Context() context.Context     { return stream.ctx }
func (stream *blockingConnectServer) SendMsg(value any) error {
	frame, ok := value.(*contractsv1.RuntimeControlFrame)
	if !ok {
		return errors.New("unexpected SendMsg value")
	}
	return stream.SendFrame(frame)
}
func (stream *blockingConnectServer) RecvMsg(value any) error {
	frame, err := stream.RecvFrame()
	if err != nil {
		return err
	}
	target, ok := value.(*contractsv1.RuntimeControlFrame)
	if !ok {
		return errors.New("unexpected RecvMsg value")
	}
	*target = *frame
	return nil
}

var _ WorkerControlRPC_ConnectServer = (*blockingConnectServer)(nil)
