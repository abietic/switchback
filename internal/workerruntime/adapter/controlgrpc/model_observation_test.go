package controlgrpc

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

func TestModelObservationIdleDoesNotBlockHeartbeat(t *testing.T) {
	identity := testCredential().Identity
	authority := application.RuntimeControlAuthority{WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID, RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch, ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1}
	model := testEnvelope(identity, "idle-model", "model.observation", json.RawMessage(`{}`))
	model.Kind, model.Sequence = "event", 1
	model.SentAt = time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	heartbeat := testEnvelope(identity, "after-model", "runtime.heartbeat", json.RawMessage(`{"heartbeat_sequence":2}`))
	heartbeat.Kind, heartbeat.Sequence = "heartbeat", 2
	heartbeat.SentAt = model.SentAt
	inbox := []application.RuntimeControlMessage{}
	for _, envelope := range []application.WorkerControlEvent{{Envelope: model}, {Envelope: heartbeat}} {
		raw, err := json.Marshal(envelope.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		inbox = append(inbox, application.RuntimeControlMessage{Direction: application.RuntimeControlDirectionEvent, Sequence: envelope.Envelope.Sequence, MessageID: envelope.Envelope.MessageID, IdempotencyKey: envelope.Envelope.IdempotencyKey, Envelope: raw})
	}
	var mu sync.Mutex
	var through uint64
	finished := make(chan struct{})
	journal := &scriptedControlJournal{}
	journal.replay = func(context.Context, application.RuntimeControlAuthority) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		return application.RuntimeControlReplayPlan{PendingEvents: inbox[through:]}, nil
	}
	journal.apply = func(_ context.Context, command application.ApplyRuntimeControlEventCommand) (application.RuntimeControlEventAcceptance, error) {
		mu.Lock()
		defer mu.Unlock()
		if command.Sequence != through+1 {
			return application.RuntimeControlEventAcceptance{}, application.ErrRuntimeControlSequenceGap
		}
		through = command.Sequence
		if through == 2 {
			close(finished)
		}
		return application.RuntimeControlEventAcceptance{}, nil
	}
	hub := newHub(journal)
	if err := hub.SetModelObservationAcceptor(application.ModelObservationAcceptor{Preparer: unavailableModelPreparer{}}); err != nil {
		t.Fatal(err)
	}
	active := mustAttachClaimed(t, hub, identity, authority, "idle-model-stream")
	active.markLive(0)
	t.Cleanup(func() { active.close(context.Canceled) })
	hub.startPendingEventPump(active)
	select {
	case <-finished:
	case event := <-active.events:
		t.Fatalf("model leaked to Turn waiter and blocked heartbeat: %s", event.Envelope.MessageType)
	case <-time.After(time.Second):
		t.Fatal("model blocked heartbeat without a Turn waiter")
	}
}

type reconnectModelPreparer struct {
	first chan struct{}
	calls atomic.Int32
}

func (p *reconnectModelPreparer) PrepareModelObservation(ctx context.Context, _ application.WorkerControlEvent) application.RuntimeObservationCapture {
	if p.calls.Add(1) == 1 {
		close(p.first)
		<-ctx.Done()
	}
	return application.RuntimeObservationCapture{SafeCode: application.ObservationCaptureUnavailable}
}

func TestModelObservationReconnectSingleOwner(t *testing.T) {
	identity := testCredential().Identity
	model := testEnvelope(identity, "reconnect-model", "model.observation", json.RawMessage(`{}`))
	model.Sequence = 1
	model.SentAt = time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	raw, _ := json.Marshal(model)
	message := application.RuntimeControlMessage{Direction: application.RuntimeControlDirectionEvent, Sequence: 1, MessageID: model.MessageID, IdempotencyKey: model.IdempotencyKey, Envelope: raw}
	var mu sync.Mutex
	plans := map[uint64]application.RuntimeControlReplayPlan{}
	committed := false
	finished := make(chan struct{})
	journal := &scriptedControlJournal{}
	journal.claim = func(_ context.Context, c application.ClaimRuntimeControlStreamCommand) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		p := testReplayPlan(c)
		plans[c.StreamAttempt] = p
		return p, nil
	}
	journal.complete = func(_ context.Context, c application.CompleteRuntimeControlResumeCommand) (application.RuntimeControlChannelState, error) {
		mu.Lock()
		defer mu.Unlock()
		p := plans[c.Authority.StreamAttempt]
		p.Channel.Status = "connected"
		p.Channel.Revision = 6
		p.Channel.ResumeCompletedRevision = 6
		p.Channel.ResumeReadyDigest = testDigest("model-ready")
		plans[c.Authority.StreamAttempt] = p
		return p.Channel, nil
	}
	journal.replay = func(_ context.Context, a application.RuntimeControlAuthority) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		p := plans[a.StreamAttempt]
		if !committed {
			p.PendingEvents = []application.RuntimeControlMessage{message}
		}
		return p, nil
	}
	journal.apply = func(ctx context.Context, c application.ApplyRuntimeControlEventCommand) (application.RuntimeControlEventAcceptance, error) {
		if ctx.Err() != nil {
			return application.RuntimeControlEventAcceptance{}, ctx.Err()
		}
		mu.Lock()
		defer mu.Unlock()
		if c.Authority.OwnerFence != 2 || c.ObservationCapture == nil {
			t.Errorf("missing current authority/capture: %+v", c.Authority)
		}
		if !committed {
			committed = true
			close(finished)
		}
		return application.RuntimeControlEventAcceptance{Message: message}, nil
	}
	hub := newHub(journal)
	preparer := &reconnectModelPreparer{first: make(chan struct{})}
	if err := hub.SetModelObservationAcceptor(application.ModelObservationAcceptor{Preparer: preparer}); err != nil {
		t.Fatal(err)
	}
	rpc, cleanup := startDurableTestControl(t, hub)
	defer cleanup()
	first, _ := openResumedTestStream(t, rpc, testCredential().Token, testResumeHello(identity, 1))
	select {
	case <-preparer.first:
	case <-time.After(time.Second):
		t.Fatal("first model preparation not reached")
	}
	second, _ := openResumedTestStream(t, rpc, testCredential().Token, testResumeHello(identity, 2))
	defer second.CloseSend()
	defer first.CloseSend()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("replacement did not apply pending model")
	}
	events, err := hub.Events(context.Background(), identity.RuntimeInstanceID, uint64(identity.RuntimeGeneration))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		t.Fatalf("model acquired a Turn waiter owner: %s", e.Envelope.MessageType)
	default:
	}
}

func TestModelObservationOldTurnPrecedesNewTerminalReplay(t *testing.T) {
	identity := testCredential().Identity
	a := application.RuntimeControlAuthority{WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID, RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch, ProcessNonce: "shim-process-1", StreamAttempt: 1, OwnerFence: 1}
	model := testEnvelope(identity, "old-model", "model.observation", json.RawMessage(`{}`))
	model.Sequence = 1
	model.SentAt = time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	terminal := testEnvelope(identity, "new-terminal", "agent_turn.terminal", json.RawMessage(`{"update_kind":"terminal","update":{"stop_reason":"end_turn"}}`))
	terminal.AgentTurnID = "turn-B"
	terminal.Sequence = 2
	terminal.SentAt = model.SentAt
	var inbox []application.RuntimeControlMessage
	for _, e := range []contractsv1.RuntimeControlEnvelope{model, terminal} {
		raw, _ := json.Marshal(e)
		inbox = append(inbox, application.RuntimeControlMessage{Direction: application.RuntimeControlDirectionEvent, Sequence: e.Sequence, MessageID: e.MessageID, IdempotencyKey: e.IdempotencyKey, Envelope: raw})
	}
	var mu sync.Mutex
	var through uint64
	journal := &scriptedControlJournal{}
	journal.replay = func(context.Context, application.RuntimeControlAuthority) (application.RuntimeControlReplayPlan, error) {
		mu.Lock()
		defer mu.Unlock()
		return application.RuntimeControlReplayPlan{PendingEvents: inbox[through:]}, nil
	}
	journal.apply = func(_ context.Context, c application.ApplyRuntimeControlEventCommand) (application.RuntimeControlEventAcceptance, error) {
		mu.Lock()
		defer mu.Unlock()
		if c.Sequence != through+1 {
			return application.RuntimeControlEventAcceptance{}, application.ErrRuntimeControlSequenceGap
		}
		through = c.Sequence
		return application.RuntimeControlEventAcceptance{}, nil
	}
	hub := newHub(journal)
	if err := hub.SetModelObservationAcceptor(application.ModelObservationAcceptor{Preparer: unavailableModelPreparer{}}); err != nil {
		t.Fatal(err)
	}
	first := mustAttachClaimed(t, hub, identity, a, "old-A")
	first.markLive(0)
	hub.startPendingEventPump(first)
	event := receiveEvent(t, first.events)
	if event.Envelope.AgentTurnID != "turn-B" {
		t.Fatalf("old model leaked: %+v", event.Envelope)
	}
	mu.Lock()
	before := through
	mu.Unlock()
	if before != 1 {
		t.Fatalf("terminal did not retain its own Apply boundary: %d", before)
	}
	a.StreamAttempt = 2
	a.OwnerFence = 2
	second := mustAttachClaimed(t, hub, identity, a, "replay-B")
	second.markLive(0)
	defer second.close(context.Canceled)
	hub.startPendingEventPump(second)
	if err := event.Delivery.Apply(context.Background(), json.RawMessage(`{"status":"terminal_committed","effect_disposition":"commit"}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := through
	mu.Unlock()
	if after != 2 {
		t.Fatal("terminal failed after model/reconnect")
	}
}

type unavailableModelPreparer struct{}

func (unavailableModelPreparer) PrepareModelObservation(context.Context, application.WorkerControlEvent) application.RuntimeObservationCapture {
	return application.RuntimeObservationCapture{SafeCode: application.ObservationCaptureUnavailable}
}
