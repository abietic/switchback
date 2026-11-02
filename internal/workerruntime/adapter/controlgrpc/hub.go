package controlgrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
)

var (
	ErrUnauthenticated      = errors.New("worker authentication failed")
	ErrInvalidIdentity      = errors.New("invalid Runtime identity")
	ErrCredentialConflict   = errors.New("Runtime credential conflict")
	ErrAlreadyConnected     = errors.New("Runtime already has an active WorkerControl stream")
	ErrNotConnected         = errors.New("Runtime WorkerControl stream is not connected")
	ErrResumeInProgress     = errors.New("Runtime WorkerControl resume is in progress")
	ErrJournalUnavailable   = errors.New("Runtime WorkerControl journal is not configured")
	ErrBackpressure         = errors.New("WorkerControl queue is full")
	ErrSequenceGap          = errors.New("WorkerControl sequence gap")
	ErrConflictingDuplicate = errors.New("WorkerControl duplicate has conflicting content")
	ErrStaleRuntime         = errors.New("WorkerControl Runtime generation or channel epoch is stale")
)

const controlQueueCapacity = 64

type runtimeKey struct {
	runtimeInstanceID string
	generation        uint32
}

func keyFor(identity RuntimeIdentity) runtimeKey {
	return runtimeKey{runtimeInstanceID: identity.RuntimeInstanceID, generation: identity.RuntimeGeneration}
}

// Hub owns only ephemeral stream routing. PostgreSQL, reached through journal,
// owns credentials, stream authority, sequences, ACKs, and replay bytes.
type Hub struct {
	mu sync.RWMutex

	credentials   map[runtimeKey]Credential
	connections   map[runtimeKey]*connection
	events        map[runtimeKey]chan application.WorkerControlEvent
	deliveries    map[runtimeKey]map[uint64]*journalEventDelivery
	fenced        map[RuntimeIdentity]struct{}
	stateChange   map[runtimeKey]chan struct{}
	observer      application.WorkerControlEventObserver
	modelAcceptor application.ModelObservationAcceptor
	journal       durableRuntimeControlJournal
	now           func() time.Time
}

var _ application.WorkerControl = (*Hub)(nil)

func NewHub() *Hub {
	return newHub(nil)
}

func NewHubWithJournal(service application.RuntimeControlJournalService) *Hub {
	return newHub(service)
}

func newHub(journal durableRuntimeControlJournal) *Hub {
	return &Hub{
		credentials: make(map[runtimeKey]Credential),
		connections: make(map[runtimeKey]*connection),
		events:      make(map[runtimeKey]chan application.WorkerControlEvent),
		deliveries:  make(map[runtimeKey]map[uint64]*journalEventDelivery),
		fenced:      make(map[RuntimeIdentity]struct{}),
		stateChange: make(map[runtimeKey]chan struct{}),
		journal:     journal,
		now:         time.Now,
	}
}

func (h *Hub) SetEventObserver(observer application.WorkerControlEventObserver) error {
	if h == nil || observer == nil {
		return fmt.Errorf("WorkerControl event observer is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.connections) != 0 {
		return fmt.Errorf("WorkerControl event observer cannot change while streams are connected")
	}
	h.observer = observer
	return nil
}

func (h *Hub) SetModelObservationAcceptor(acceptor application.ModelObservationAcceptor) error {
	if h == nil || acceptor.Preparer == nil {
		return fmt.Errorf("model observation acceptor is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.connections) != 0 {
		return fmt.Errorf("model observation acceptor cannot change while streams are connected")
	}
	h.modelAcceptor = acceptor
	return nil
}

func (h *Hub) RegisterCredential(credential Credential) error {
	if h == nil {
		return fmt.Errorf("WorkerControl Hub is required")
	}
	if err := credential.Identity.validate(); err != nil {
		return err
	}
	if !validToken(credential.Token) {
		return ErrUnauthenticated
	}
	if h.journal != nil {
		if _, err := h.journal.Bind(context.Background(), application.BindRuntimeControlChannelCommand{
			WorkerSessionID:   credential.Identity.WorkerSessionID,
			RuntimeInstanceID: credential.Identity.RuntimeInstanceID,
			RuntimeGeneration: uint64(credential.Identity.RuntimeGeneration),
			ChannelEpoch:      credential.Identity.ChannelEpoch,
			Credential:        credential.Token,
		}); err != nil {
			return err
		}
	}
	key := keyFor(credential.Identity)
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.credentials[key]; ok {
		if existing.Identity == credential.Identity && secureEqual(existing.Token, credential.Token) {
			return nil
		}
		return ErrCredentialConflict
	}
	for _, existing := range h.credentials {
		if secureEqual(existing.Token, credential.Token) {
			return ErrCredentialConflict
		}
	}
	h.credentials[key] = credential
	if h.events[key] == nil {
		h.events[key] = make(chan application.WorkerControlEvent, controlQueueCapacity)
	}
	h.signalStateChangeLocked(key)
	return nil
}

func (h *Hub) Revoke(identity RuntimeIdentity) {
	if h == nil {
		return
	}
	if h.journal != nil {
		if _, err := h.journal.Fence(context.Background(), application.FenceRuntimeControlChannelCommand{
			WorkerSessionID:   identity.WorkerSessionID,
			RuntimeInstanceID: identity.RuntimeInstanceID,
			RuntimeGeneration: uint64(identity.RuntimeGeneration),
			ChannelEpoch:      identity.ChannelEpoch,
		}); err != nil {
			return
		}
	}
	h.revokeDurablyFenced(identity, ErrUnauthenticated, true)
}

// FenceIdentity uses the caller's persistent exact identity, including after a
// Backend restart with no in-memory credential. The returned state is the
// journal's committed result; callers must check its fence proof before cleanup.
func (h *Hub) FenceIdentity(ctx context.Context, identity RuntimeIdentity) (application.RuntimeControlChannelState, error) {
	if h == nil || h.journal == nil {
		return application.RuntimeControlChannelState{}, ErrJournalUnavailable
	}
	if err := identity.validate(); err != nil {
		return application.RuntimeControlChannelState{}, err
	}
	state, err := h.journal.Fence(ctx, application.FenceRuntimeControlChannelCommand{
		WorkerSessionID: identity.WorkerSessionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration), ChannelEpoch: identity.ChannelEpoch,
	})
	if err == nil && state.Status == "fenced" && state.WorkerSessionID == identity.WorkerSessionID &&
		state.RuntimeInstanceID == identity.RuntimeInstanceID && state.RuntimeGeneration == uint64(identity.RuntimeGeneration) && state.ChannelEpoch == identity.ChannelEpoch &&
		!state.FencedAt.IsZero() && state.FenceRevision > 0 && state.FenceRevision <= state.Revision && state.FenceOwnerFence > 0 && state.FenceOwnerFence == state.OwnerFence {
		h.revokeDurablyFenced(identity, ErrNotConnected, true)
	}
	return state, err
}

// WakePendingCommands is a best-effort post-commit latency hint for commands
// already stored in the durable Runtime-control journal. It never reads or
// mutates that journal. Resume replay remains the delivery authority when no
// exact local connection is currently attached.
func (h *Hub) WakePendingCommands(identity RuntimeIdentity) bool {
	if h == nil || identity.validate() != nil {
		return false
	}
	key := keyFor(identity)
	h.mu.RLock()
	active := h.connections[key]
	h.mu.RUnlock()
	if active == nil || active.identity != identity {
		return false
	}
	select {
	case <-active.done:
		return false
	default:
	}
	active.signalSend()
	return true
}

// RevokeDurablyFenced applies an already-committed durable channel fence to
// ephemeral routing. The complete Runtime identity is required because the Hub
// routing key intentionally omits WorkerSession and ChannelEpoch.
func (h *Hub) RevokeDurablyFenced(identity RuntimeIdentity) bool {
	return h.revokeDurablyFenced(identity, ErrNotConnected, false)
}

func (h *Hub) revokeDurablyFenced(
	identity RuntimeIdentity,
	cause error,
	revokeCredential bool,
) bool {
	if h == nil || identity.validate() != nil {
		return false
	}
	key := keyFor(identity)
	h.mu.Lock()
	h.fenced[identity] = struct{}{}
	credential, registered := h.credentials[key]
	exactCredential := registered && credential.Identity == identity
	if revokeCredential && exactCredential {
		delete(h.credentials, key)
	}
	active := h.connections[key]
	exact := active != nil && active.identity == identity
	if exact {
		delete(h.connections, key)
		delete(h.deliveries, key)
		h.signalStateChangeLocked(key)
	} else if exactCredential {
		h.signalStateChangeLocked(key)
	}
	h.mu.Unlock()
	if exact {
		active.close(cause)
	}
	return exact
}

func (h *Hub) Send(ctx context.Context, command application.WorkerControlCommand) error {
	if h == nil || h.journal == nil {
		return ErrJournalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	envelope := command.Envelope
	key := runtimeKey{
		runtimeInstanceID: string(envelope.RuntimeInstanceID),
		generation:        envelope.RuntimeGeneration,
	}
	h.mu.RLock()
	active := h.connections[key]
	h.mu.RUnlock()
	if active == nil {
		return ErrNotConnected
	}
	if !active.isLive() {
		return ErrResumeInProgress
	}
	if len(command.Payload) != 0 {
		envelope.Payload = append(json.RawMessage(nil), command.Payload...)
	}
	if !matchesRuntime(active.identity, envelope) {
		return ErrStaleRuntime
	}
	envelope.SchemaVersion = contractsv1.RuntimeControlSchemaVersion
	envelope.Kind = "command"
	envelope.ChannelEpoch = active.identity.ChannelEpoch
	envelope.Sequence = 0
	envelope.AckSequence = 0
	envelope.SentAt = time.Time{}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode Runtime control command template: %w", err)
	}
	if _, err := h.journal.ReserveCommand(ctx, application.ReserveRuntimeControlCommandCommand{
		Authority:      active.authority,
		MessageID:      envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded,
	}); err != nil {
		return err
	}
	h.WakePendingCommands(active.identity)
	return nil
}

func (h *Hub) Events(
	_ context.Context,
	runtimeInstanceID string,
	generation uint64,
) (<-chan application.WorkerControlEvent, error) {
	if !validIdentity(runtimeInstanceID) || generation == 0 || generation > uint64(^uint32(0)) {
		return nil, ErrInvalidIdentity
	}
	key := runtimeKey{runtimeInstanceID: runtimeInstanceID, generation: uint32(generation)}
	h.mu.RLock()
	events := h.events[key]
	h.mu.RUnlock()
	if events == nil {
		return nil, ErrUnauthenticated
	}
	return events, nil
}

// WaitEvents waits until the exact registered Runtime identity has crossed the
// ResumeComplete barrier. It never treats a credential or a shared event queue
// as proof that the current WorkerControl stream is live.
func (h *Hub) WaitEvents(
	ctx context.Context,
	identity RuntimeIdentity,
) (<-chan application.WorkerControlEvent, error) {
	if h == nil {
		return nil, ErrNotConnected
	}
	if err := identity.validate(); err != nil {
		return nil, err
	}
	key := keyFor(identity)
	for {
		h.mu.Lock()
		credential, registered := h.credentials[key]
		if !registered {
			h.mu.Unlock()
			return nil, ErrUnauthenticated
		}
		if credential.Identity != identity {
			h.mu.Unlock()
			return nil, ErrStaleRuntime
		}
		if _, fenced := h.fenced[identity]; fenced {
			h.mu.Unlock()
			return nil, ErrStaleRuntime
		}
		active := h.connections[key]
		events := h.events[key]
		changed := h.stateChange[key]
		if changed == nil {
			changed = make(chan struct{})
			h.stateChange[key] = changed
		}
		h.mu.Unlock()
		if active != nil {
			if active.identity != identity {
				return nil, ErrStaleRuntime
			}
			if active.isLive() {
				if events == nil {
					return nil, ErrNotConnected
				}
				return events, nil
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (h *Hub) Fence(
	ctx context.Context,
	runtimeInstanceID string,
	generation uint64,
	reason string,
) error {
	_, err := h.fence(
		ctx,
		runtimeInstanceID,
		generation,
		reason,
		false,
		time.Time{},
	)
	return err
}

// FenceIfNotLive durably serializes a liveness timeout against
// CompleteResume. If CompleteResume has already committed, it returns false
// without fencing so the exact resumed stream wins the deadline race.
func (h *Hub) FenceIfNotLive(
	ctx context.Context,
	runtimeInstanceID string,
	generation uint64,
	reason string,
	connectedBy time.Time,
) (bool, error) {
	if connectedBy.IsZero() {
		return false, ErrInvalidIdentity
	}
	return h.fence(
		ctx,
		runtimeInstanceID,
		generation,
		reason,
		true,
		connectedBy.UTC(),
	)
}

func (h *Hub) fence(
	ctx context.Context,
	runtimeInstanceID string,
	generation uint64,
	reason string,
	onlyIfNotConnected bool,
	connectedBy time.Time,
) (bool, error) {
	if h == nil || h.journal == nil {
		return false, ErrJournalUnavailable
	}
	if !validIdentity(runtimeInstanceID) || generation == 0 ||
		generation > uint64(^uint32(0)) || reason == "" {
		return false, ErrInvalidIdentity
	}
	key := runtimeKey{runtimeInstanceID: runtimeInstanceID, generation: uint32(generation)}
	h.mu.RLock()
	active := h.connections[key]
	credential, registered := h.credentials[key]
	h.mu.RUnlock()
	var identity RuntimeIdentity
	switch {
	case active != nil:
		identity = active.identity
	case registered:
		identity = credential.Identity
	default:
		return false, ErrNotConnected
	}
	state, err := h.journal.Fence(ctx, application.FenceRuntimeControlChannelCommand{
		WorkerSessionID:    identity.WorkerSessionID,
		RuntimeInstanceID:  identity.RuntimeInstanceID,
		RuntimeGeneration:  uint64(identity.RuntimeGeneration),
		ChannelEpoch:       identity.ChannelEpoch,
		OnlyIfNotConnected: onlyIfNotConnected,
		ConnectedBy:        connectedBy,
	})
	if err != nil {
		return false, err
	}
	if onlyIfNotConnected && state.Status == "connected" {
		return false, nil
	}
	h.revokeDurablyFenced(identity, ErrNotConnected, false)
	return true, nil
}

func (h *Hub) attachClaimed(
	identity RuntimeIdentity,
	authority application.RuntimeControlAuthority,
	connectionID string,
) (*connection, error) {
	key := keyFor(identity)
	h.mu.Lock()
	if _, fenced := h.fenced[identity]; fenced {
		h.mu.Unlock()
		return nil, ErrStaleRuntime
	}
	events := h.events[key]
	if events == nil {
		events = make(chan application.WorkerControlEvent, controlQueueCapacity)
		h.events[key] = events
	}
	active := newConnection(identity, authority, connectionID, events, func() {
		h.signalStateChange(key)
	})
	replaced := h.connections[key]
	h.connections[key] = active
	h.signalStateChangeLocked(key)
	h.mu.Unlock()
	if replaced != nil {
		replaced.close(ErrStaleRuntime)
	}
	return active, nil
}

func (h *Hub) detach(ctx context.Context, active *connection, cause error) error {
	if active == nil {
		return nil
	}
	key := keyFor(active.identity)
	h.mu.Lock()
	current := h.connections[key]
	if current == active {
		delete(h.connections, key)
		h.signalStateChangeLocked(key)
	}
	h.mu.Unlock()
	active.close(cause)
	if current != active || h.journal == nil {
		return nil
	}
	_, err := h.journal.Disconnect(context.WithoutCancel(ctx), active.authority)
	if errors.Is(err, application.ErrRuntimeControlStaleOwner) {
		return nil
	}
	return err
}

func (h *Hub) signalStateChange(key runtimeKey) {
	h.mu.Lock()
	h.signalStateChangeLocked(key)
	h.mu.Unlock()
}

func (h *Hub) signalStateChangeLocked(key runtimeKey) {
	if changed := h.stateChange[key]; changed != nil {
		close(changed)
	}
	h.stateChange[key] = make(chan struct{})
}

func (h *Hub) acceptEvent(
	ctx context.Context,
	active *connection,
	envelope contractsv1.RuntimeControlEnvelope,
	emitTransportACK bool,
) error {
	if h.journal == nil {
		return ErrJournalUnavailable
	}
	if !matchesRuntime(active.identity, envelope) {
		return ErrStaleRuntime
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode Runtime control event: %w", err)
	}
	accepted, err := h.journal.AcceptEvent(ctx, application.AcceptRuntimeControlEventCommand{
		Authority:      active.authority,
		Sequence:       envelope.Sequence,
		MessageID:      envelope.MessageID,
		IdempotencyKey: envelope.IdempotencyKey,
		Envelope:       encoded,
	})
	if err != nil {
		return err
	}
	if accepted.Message.Duplicate {
		if emitTransportACK && envelope.Kind != "ack" {
			return h.reserveTransportACK(ctx, active, envelope)
		}
		return nil
	}

	active.signalEvent()
	if emitTransportACK && envelope.Kind != "ack" {
		if err := h.reserveTransportACK(ctx, active, envelope); err != nil {
			return err
		}
	}
	return nil
}

func (h *Hub) applyAdapterEvent(
	ctx context.Context,
	active *connection,
	message application.RuntimeControlMessage,
	status string,
) error {
	outcome, _ := json.Marshal(map[string]string{"status": status})
	_, err := h.journal.ApplyEvent(ctx, application.ApplyRuntimeControlEventCommand{
		Authority:      active.authority,
		Sequence:       message.Sequence,
		MessageID:      message.MessageID,
		IdempotencyKey: message.IdempotencyKey,
		Outcome:        outcome,
	})
	return err
}

func (h *Hub) deliveryAuthority(
	key runtimeKey,
) (application.RuntimeControlAuthority, error) {
	h.mu.RLock()
	active := h.connections[key]
	h.mu.RUnlock()
	if active == nil {
		return application.RuntimeControlAuthority{}, ErrNotConnected
	}
	if !active.isLive() {
		return application.RuntimeControlAuthority{}, ErrResumeInProgress
	}
	return active.authority, nil
}

func (h *Hub) pendingDelivery(
	key runtimeKey,
	message application.RuntimeControlMessage,
) (*journalEventDelivery, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bySequence := h.deliveries[key]
	if bySequence == nil {
		bySequence = make(map[uint64]*journalEventDelivery)
		h.deliveries[key] = bySequence
	}
	if existing := bySequence[message.Sequence]; existing != nil {
		if existing.message.MessageID != message.MessageID ||
			existing.message.IdempotencyKey != message.IdempotencyKey ||
			!bytes.Equal(existing.message.Envelope, message.Envelope) {
			return nil, ErrConflictingDuplicate
		}
		return existing, nil
	}
	delivery := &journalEventDelivery{
		hub:     h,
		key:     key,
		journal: h.journal,
		message: message,
		applied: make(chan struct{}),
		retry:   make(chan struct{}, 1),
	}
	bySequence[message.Sequence] = delivery
	return delivery, nil
}

func (h *Hub) forgetDelivery(
	key runtimeKey,
	sequence uint64,
	delivery *journalEventDelivery,
) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bySequence := h.deliveries[key]
	if bySequence == nil || bySequence[sequence] != delivery {
		return
	}
	delete(bySequence, sequence)
	if len(bySequence) == 0 {
		delete(h.deliveries, key)
	}
}

func (h *Hub) reserveTransportACK(
	ctx context.Context,
	active *connection,
	event contractsv1.RuntimeControlEnvelope,
) error {
	messageID := fmt.Sprintf("backend-ack-%d", event.Sequence)
	envelope := contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		MessageID:         messageID,
		Kind:              "ack",
		WorkerSessionID:   event.WorkerSessionID,
		WorkerRunID:       event.WorkerRunID,
		RuntimeInstanceID: event.RuntimeInstanceID,
		RuntimeGeneration: event.RuntimeGeneration,
		AgentSessionID:    event.AgentSessionID,
		AgentTurnID:       event.AgentTurnID,
		ChannelEpoch:      event.ChannelEpoch,
		IdempotencyKey:    messageID,
		MessageType:       "runtime.control.ack",
		Payload:           json.RawMessage(fmt.Sprintf(`{"through_sequence":%d}`, event.Sequence)),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := h.journal.ReserveCommand(ctx, application.ReserveRuntimeControlCommandCommand{
		Authority:      active.authority,
		MessageID:      messageID,
		IdempotencyKey: messageID,
		Envelope:       encoded,
	}); err != nil {
		return err
	}
	h.WakePendingCommands(active.identity)
	return nil
}

func (h *Hub) startPendingEventPump(active *connection) {
	go func() {
		for {
			plan, err := h.journal.ReplayPlan(active.streamContext, active.authority)
			if err != nil {
				active.close(err)
				return
			}
			if len(plan.PendingEvents) == 0 {
				select {
				case <-active.done:
					return
				case <-active.eventWake:
					continue
				}
			}
			message := plan.PendingEvents[0]
			envelope, err := decodeJournalEnvelope(message)
			if err != nil {
				active.close(err)
				return
			}
			event := application.WorkerControlEvent{
				Envelope: envelope,
				Payload:  append(json.RawMessage(nil), envelope.Payload...),
			}
			h.mu.RLock()
			observer := h.observer
			observedAt := message.CreatedAt
			if observedAt.IsZero() {
				observedAt = h.now().UTC()
			}
			h.mu.RUnlock()
			if envelope.Kind == "ack" || envelope.Kind == "heartbeat" {
				if observer != nil {
					if err := observer.ObserveWorkerControlEvent(
						active.streamContext,
						event,
						observedAt,
					); err != nil {
						active.close(fmt.Errorf("observe WorkerControl event: %w", err))
						return
					}
				}
				if err := h.applyAdapterEvent(
					active.streamContext,
					active,
					message,
					"adapter_replay_applied",
				); err != nil {
					active.close(err)
					return
				}
				continue
			}
			delivery, err := h.pendingDelivery(keyFor(active.identity), message)
			if err != nil {
				active.close(err)
				return
			}
			event.Delivery = delivery
			if envelope.MessageType == "model.observation" {
				h.mu.RLock()
				acceptor := h.modelAcceptor
				h.mu.RUnlock()
				if err := acceptor.Accept(active.streamContext, event); err != nil {
					if acceptor.Preparer == nil {
						active.close(err)
						return
					}
					select {
					case <-active.done:
						return
					case <-delivery.retry:
					}
				}
				continue
			}
			published, err := delivery.publish(
				active.streamContext,
				active,
				event,
				observer,
				observedAt,
			)
			if err != nil {
				active.close(err)
				return
			}
			if !published && !delivery.isPublished() {
				return
			}
			select {
			case <-active.done:
				return
			case <-delivery.applied:
			case <-delivery.retry:
			}
		}
	}()
}

type connection struct {
	identity      RuntimeIdentity
	authority     application.RuntimeControlAuthority
	connectionID  string
	events        chan application.WorkerControlEvent
	sendWake      chan struct{}
	eventWake     chan struct{}
	done          chan struct{}
	streamContext context.Context
	cancelStream  context.CancelFunc
	stateChanged  func()

	mu sync.Mutex

	closed      bool
	closeErr    error
	live        bool
	sentThrough uint64
}

func newConnection(
	identity RuntimeIdentity,
	authority application.RuntimeControlAuthority,
	connectionID string,
	events chan application.WorkerControlEvent,
	stateChanged func(),
) *connection {
	streamContext, cancelStream := context.WithCancel(context.Background())
	return &connection{
		identity: identity, authority: authority, connectionID: connectionID,
		events: events, sendWake: make(chan struct{}, 1), eventWake: make(chan struct{}, 1),
		done: make(chan struct{}), streamContext: streamContext, cancelStream: cancelStream,
		stateChanged: stateChanged,
	}
}

func (connection *connection) close(cause error) {
	connection.mu.Lock()
	if connection.closed {
		connection.mu.Unlock()
		return
	}
	connection.closed = true
	connection.closeErr = cause
	close(connection.done)
	connection.cancelStream()
	stateChanged := connection.stateChanged
	connection.mu.Unlock()
	if stateChanged != nil {
		stateChanged()
	}
}

func (connection *connection) terminalError() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.closeErr
}

func (connection *connection) markLive(sentThrough uint64) {
	connection.mu.Lock()
	connection.live = true
	if sentThrough > connection.sentThrough {
		connection.sentThrough = sentThrough
	}
	stateChanged := connection.stateChanged
	connection.mu.Unlock()
	if stateChanged != nil {
		stateChanged()
	}
}

func (connection *connection) isLive() bool {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return connection.live && !connection.closed
}

func (connection *connection) nextUnsent(sequence uint64) bool {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return sequence > connection.sentThrough
}

func (connection *connection) markSent(sequence uint64) {
	connection.mu.Lock()
	if sequence > connection.sentThrough {
		connection.sentThrough = sequence
	}
	connection.mu.Unlock()
}

func (connection *connection) signalSend() {
	select {
	case connection.sendWake <- struct{}{}:
	default:
	}
}

func (connection *connection) signalEvent() {
	select {
	case connection.eventWake <- struct{}{}:
	default:
	}
}

func matchesRuntime(identity RuntimeIdentity, envelope contractsv1.RuntimeControlEnvelope) bool {
	return identity.WorkerSessionID == string(envelope.WorkerSessionID) &&
		identity.RuntimeInstanceID == string(envelope.RuntimeInstanceID) &&
		identity.RuntimeGeneration == envelope.RuntimeGeneration &&
		identity.ChannelEpoch == envelope.ChannelEpoch
}
