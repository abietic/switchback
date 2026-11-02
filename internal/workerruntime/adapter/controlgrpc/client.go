package controlgrpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	ErrResumeRejected         = errors.New("Runtime control resume was rejected")
	ErrClientAlreadyRunning   = errors.New("Runtime control client is already running")
	ErrTurnConflict           = errors.New("Runtime control active Turn conflicts")
	ErrApplicationSequence    = errors.New("Runtime control command application sequence is invalid")
	ErrTurnContinuityUnproven = errors.New("Runtime control active Turn continuity is unproven")
	ErrLogicalHistoryCapacity = errors.New(
		"Runtime control logical identity history capacity is exhausted",
	)
)

const (
	defaultClientSpoolMessages = 64
	defaultClientSpoolBytes    = 8 << 20
	// One protocol-sized slot is held for the first active-effect proof and
	// one for the terminal proof. Ordinary events and transport ACKs cannot
	// consume either slot.
	defaultReservedMessages = 2
	defaultReservedBytes    = 2 * maxEnvelopeBytes
	defaultReconnectBackoff = 100 * time.Millisecond
	maxReconnectBackoff     = 5 * time.Second

	// Logical identity history is a process-level safety bound, not a
	// throughput quota. ACKed identities remain here so an old MessageID can
	// never be silently reused with conflicting content. MessageID itself is
	// contract-bounded to 160 bytes. In addition to 64K ordinary identities,
	// two entries remain unavailable for the current Turn's active and terminal
	// proofs.
	defaultLogicalOrdinaryHistoryMessages = 64 << 10
	defaultLogicalProofReserveMessages    = 2
	defaultLogicalHistoryMessages         = defaultLogicalOrdinaryHistoryMessages +
		defaultLogicalProofReserveMessages
)

// Client binds a Shim connection to one scoped Runtime credential.
type Client struct {
	rpc        WorkerControlRPCClient
	credential Credential

	now          func() time.Time
	processNonce string
	backoffMin   time.Duration
	backoffMax   time.Duration

	txMaxMessages               int
	txMaxBytes                  int
	txReservedMessages          int
	txReservedBytes             int
	rxMaxMessages               int
	rxMaxBytes                  int
	logicalMaxMessages          int
	logicalProofReserveMessages int

	mu sync.Mutex

	running       bool
	ready         bool
	streamAttempt uint64
	terminalErr   error
	// turnContinuationErr is independent of terminalErr: a transport failure
	// after Prompt intent may still need local effect recording, while an
	// explicit continuity_unproven disposition forbids further Turn effects.
	turnContinuationErr error

	txBaseSequence uint64
	txBaseDigest   string
	txLastSequence uint64
	txLastDigest   string
	txBackendAcked uint64
	txSpool        map[uint64]*clientOutbound
	txBytes        int
	sentLogical    map[string][32]byte
	pendingACK     *contractsv1.RuntimeControlEnvelope

	rxBaseSequence uint64
	rxBaseDigest   string
	rxLastSequence uint64
	rxLastDigest   string
	rxApplied      uint64
	rxConfirmed    uint64
	rxSpool        map[uint64]*clientInbound
	rxBytes        int

	activeTurn          *contractsv1.RuntimeControlActiveTurn
	activeProofSequence uint64

	wake             chan struct{}
	stateChanged     chan struct{}
	commands         chan application.WorkerControlCommand
	readyTransitions chan ClientReadyTransition
}

func NewClient(rpc WorkerControlRPCClient, credential Credential) (*Client, error) {
	if rpc == nil {
		return nil, fmt.Errorf("WorkerControl RPC client is required")
	}
	if err := credential.Identity.validate(); err != nil || !validToken(credential.Token) {
		return nil, ErrUnauthenticated
	}
	nonce, err := newShimProcessNonce()
	if err != nil {
		return nil, fmt.Errorf("create Runtime control process nonce: %w", err)
	}
	return &Client{
		rpc:                         rpc,
		credential:                  credential,
		now:                         time.Now,
		processNonce:                nonce,
		backoffMin:                  defaultReconnectBackoff,
		backoffMax:                  maxReconnectBackoff,
		txMaxMessages:               defaultClientSpoolMessages,
		txMaxBytes:                  defaultClientSpoolBytes,
		txReservedMessages:          defaultReservedMessages,
		txReservedBytes:             defaultReservedBytes,
		rxMaxMessages:               defaultClientSpoolMessages,
		rxMaxBytes:                  defaultClientSpoolBytes,
		logicalMaxMessages:          defaultLogicalHistoryMessages,
		logicalProofReserveMessages: defaultLogicalProofReserveMessages,
		txBaseDigest:                contractsv1.RuntimeControlCursorGenesisDigest,
		txLastDigest:                contractsv1.RuntimeControlCursorGenesisDigest,
		txSpool:                     make(map[uint64]*clientOutbound),
		sentLogical:                 make(map[string][32]byte),
		rxBaseDigest:                contractsv1.RuntimeControlCursorGenesisDigest,
		rxLastDigest:                contractsv1.RuntimeControlCursorGenesisDigest,
		rxSpool:                     make(map[uint64]*clientInbound),
		wake:                        make(chan struct{}, 1),
		stateChanged:                make(chan struct{}),
		commands:                    make(chan application.WorkerControlCommand, controlQueueCapacity),
		readyTransitions:            make(chan ClientReadyTransition, 8),
	}, nil
}

// ClientReadyTransition reports only transport readiness. A false transition
// does not imply Runtime or Shim process loss.
type ClientReadyTransition struct {
	Ready                 bool
	StreamAttempt         uint64
	ActiveTurnDisposition string
	AuthoritativeStart    *contractsv1.RuntimeControlActiveTurn
	OccurredAt            time.Time
	Cause                 error
}

type clientOutbound struct {
	envelope     contractsv1.RuntimeControlEnvelope
	fingerprint  [32]byte
	cursorDigest string
	size         int
	terminal     bool
}

type clientInbound struct {
	envelope      contractsv1.RuntimeControlEnvelope
	fingerprint   [32]byte
	cursorDigest  string
	size          int
	delivered     bool
	ackTXSequence uint64
}

type clientResumeSnapshot struct {
	attempt        uint64
	txLast         uint64
	txBackendAcked uint64
	txDigest       string
	rxLast         uint64
	rxApplied      uint64
	rxDigest       string
	activeTurn     *contractsv1.RuntimeControlActiveTurn
}

type clientIncomingFrame struct {
	frame *contractsv1.RuntimeControlFrame
	err   error
}

// Run owns the reconnect loop for the lifetime of one live Shim process.
// Transient transport loss creates a new stream attempt without resetting
// either cursor or redelivering already-delivered commands.
func (client *Client) Run(ctx context.Context) error {
	if client == nil || client.rpc == nil {
		return ErrNotConnected
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client.mu.Lock()
	if client.running {
		client.mu.Unlock()
		return ErrClientAlreadyRunning
	}
	client.running = true
	client.terminalErr = nil
	client.mu.Unlock()
	defer func() {
		client.mu.Lock()
		client.running = false
		client.ready = false
		client.signalStateChangedLocked()
		client.mu.Unlock()
	}()

	backoff := client.backoffMin
	for {
		completed, err := client.runAttempt(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = io.EOF
		}
		client.markNotReady(err)
		if !transientClientError(err) {
			client.mu.Lock()
			client.terminalErr = err
			client.signalStateChangedLocked()
			client.mu.Unlock()
			return err
		}
		if completed {
			backoff = client.backoffMin
		}
		if backoff <= 0 {
			backoff = defaultReconnectBackoff
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < client.backoffMax {
			backoff *= 2
			if backoff > client.backoffMax {
				backoff = client.backoffMax
			}
		}
	}
}

func (client *Client) runAttempt(ctx context.Context) (completed bool, result error) {
	snapshot, hello, err := client.freezeResumeHello()
	if err != nil {
		return false, err
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	attemptCtx = metadata.NewOutgoingContext(attemptCtx, metadata.Pairs(
		"authorization", "Bearer "+client.credential.Token,
	))
	stream, err := client.rpc.Connect(attemptCtx)
	if err != nil {
		cancel()
		return false, fmt.Errorf("connect WorkerControl stream: %w", err)
	}
	incoming, pumpDone := startClientReceivePump(attemptCtx, stream)
	defer func() {
		cancel()
		_ = stream.CloseSend()
		<-pumpDone
	}()

	if err := sendClientFrame(stream, &contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeHello,
		ResumeHello:   &hello,
	}); err != nil {
		return false, err
	}
	received, err := receiveClientFrame(attemptCtx, incoming)
	if err != nil {
		return false, err
	}
	if received.FrameType != contractsv1.RuntimeControlFrameTypeResumeAck ||
		received.ResumeAck == nil {
		return false, fmt.Errorf("WorkerControl expected ResumeAck, got %q", received.FrameType)
	}
	ack := *received.ResumeAck
	if err := client.acceptResumeAck(snapshot, ack); err != nil {
		return false, err
	}
	replay, err := client.workerReplay(snapshot, ack)
	if err != nil {
		return false, err
	}
	for index := range replay {
		envelope := replay[index]
		if err := sendClientFrame(stream, envelopeFrame(&envelope)); err != nil {
			return false, err
		}
	}
	for sequence := ack.ReplayBackendFromSequence; sequence <= ack.BackendTXLastCommandSequence && sequence != 0; sequence++ {
		frame, err := receiveClientFrame(attemptCtx, incoming)
		if err != nil {
			return false, err
		}
		if frame.FrameType != contractsv1.RuntimeControlFrameTypeEnvelope ||
			frame.Envelope == nil {
			return false, fmt.Errorf(
				"WorkerControl expected replay envelope at sequence %d, got %q",
				sequence,
				frame.FrameType,
			)
		}
		if frame.Envelope.Sequence != sequence {
			return false, ErrSequenceGap
		}
		if _, _, err := client.acceptBackendEnvelope(*frame.Envelope, false); err != nil {
			return false, err
		}
	}
	ready, err := client.resumeReady(snapshot, ack)
	if err != nil {
		return false, err
	}
	if err := sendClientFrame(stream, &contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeReady,
		ResumeReady:   &ready,
	}); err != nil {
		return false, err
	}
	received, err = receiveClientFrame(attemptCtx, incoming)
	if err != nil {
		return false, err
	}
	if received.FrameType != contractsv1.RuntimeControlFrameTypeResumeComplete ||
		received.ResumeComplete == nil {
		return false, fmt.Errorf(
			"WorkerControl expected ResumeComplete, got %q",
			received.FrameType,
		)
	}
	if err := client.acceptResumeComplete(
		snapshot,
		ack,
		ready,
		*received.ResumeComplete,
	); err != nil {
		return false, err
	}
	completed = true
	if err := client.markReady(
		attemptCtx,
		snapshot.attempt,
		*received.ResumeComplete,
	); err != nil {
		return completed, err
	}
	if err := client.deliverPendingCommands(); err != nil {
		return completed, err
	}
	sentThrough := snapshot.txLast

	for {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		envelopes, last, err := client.unsentWorkerEvents(sentThrough)
		if err != nil {
			return completed, err
		}
		for index := range envelopes {
			envelope := envelopes[index]
			if err := sendClientFrame(stream, envelopeFrame(&envelope)); err != nil {
				return completed, err
			}
			sentThrough = envelope.Sequence
		}
		if last > sentThrough {
			continue
		}
		select {
		case <-ctx.Done():
			return completed, ctx.Err()
		case <-client.wake:
			continue
		case next, ok := <-incoming:
			if !ok {
				return completed, io.EOF
			}
			if next.err != nil {
				return completed, next.err
			}
			if next.frame == nil ||
				next.frame.FrameType != contractsv1.RuntimeControlFrameTypeEnvelope ||
				next.frame.Envelope == nil {
				return completed, fmt.Errorf(
					"WorkerControl frame is invalid after ResumeComplete",
				)
			}
			duplicate, needsACK, err := client.acceptBackendEnvelope(
				*next.frame.Envelope,
				true,
			)
			if err != nil {
				return completed, err
			}
			if needsACK {
				if err := client.enqueueTransportACK(*next.frame.Envelope); err != nil {
					return completed, err
				}
			}
			if !duplicate {
				if err := client.deliverPendingCommands(); err != nil {
					return completed, err
				}
			}
		}
	}
}

func startClientReceivePump(
	ctx context.Context,
	stream WorkerControlRPC_ConnectClient,
) (<-chan clientIncomingFrame, <-chan struct{}) {
	incoming := make(chan clientIncomingFrame, controlQueueCapacity)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(incoming)
		for {
			frame, err := stream.RecvFrame()
			select {
			case <-ctx.Done():
				return
			case incoming <- clientIncomingFrame{frame: frame, err: err}:
			}
			if err != nil {
				return
			}
		}
	}()
	return incoming, done
}

func sendClientFrame(
	stream WorkerControlRPC_ConnectClient,
	frame *contractsv1.RuntimeControlFrame,
) error {
	if frame == nil {
		return fmt.Errorf("Runtime control frame is required")
	}
	if err := frame.Validate(); err != nil {
		return fmt.Errorf("invalid Runtime control frame: %w", err)
	}
	if err := stream.SendFrame(frame); err != nil {
		return fmt.Errorf("send Runtime control frame: %w", err)
	}
	return nil
}

func receiveClientFrame(
	ctx context.Context,
	incoming <-chan clientIncomingFrame,
) (*contractsv1.RuntimeControlFrame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case received, ok := <-incoming:
		if !ok {
			return nil, io.EOF
		}
		if received.err != nil {
			return nil, received.err
		}
		if received.frame == nil {
			return nil, fmt.Errorf("Runtime control frame is missing")
		}
		if err := received.frame.Validate(); err != nil {
			return nil, fmt.Errorf("invalid Runtime control frame: %w", err)
		}
		return received.frame, nil
	}
}

func (client *Client) freezeResumeHello() (
	clientResumeSnapshot,
	contractsv1.RuntimeControlResumeHello,
	error,
) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.streamAttempt++
	snapshot := clientResumeSnapshot{
		attempt:        client.streamAttempt,
		txLast:         client.txLastSequence,
		txBackendAcked: client.txBackendAcked,
		txDigest:       client.txLastDigest,
		rxLast:         client.rxLastSequence,
		rxApplied:      client.rxApplied,
		rxDigest:       client.rxLastDigest,
		activeTurn:     cloneActiveTurn(client.activeTurn),
	}
	identity := client.credential.Identity
	hello := contractsv1.RuntimeControlResumeHello{
		WorkerSessionID:              contractsv1.WorkerSessionID(identity.WorkerSessionID),
		RuntimeInstanceID:            contractsv1.RuntimeInstanceID(identity.RuntimeInstanceID),
		RuntimeGeneration:            identity.RuntimeGeneration,
		ChannelEpoch:                 identity.ChannelEpoch,
		ShimProcessNonce:             client.processNonce,
		StreamAttempt:                snapshot.attempt,
		WorkerTXLastSequence:         snapshot.txLast,
		WorkerTXBackendAckedSequence: snapshot.txBackendAcked,
		WorkerTXCursorDigest:         snapshot.txDigest,
		WorkerRXLastSequence:         snapshot.rxLast,
		WorkerRXAppliedSequence:      snapshot.rxApplied,
		WorkerRXCursorDigest:         snapshot.rxDigest,
		ActiveTurn:                   cloneActiveTurn(snapshot.activeTurn),
	}
	if err := hello.Validate(); err != nil {
		return clientResumeSnapshot{}, contractsv1.RuntimeControlResumeHello{}, err
	}
	return snapshot, hello, nil
}

func (client *Client) acceptResumeAck(
	snapshot clientResumeSnapshot,
	ack contractsv1.RuntimeControlResumeAck,
) error {
	if err := ack.Validate(); err != nil {
		return fmt.Errorf("invalid Runtime control ResumeAck: %w", err)
	}
	if !ack.Accepted {
		return fmt.Errorf("%w: %s", ErrResumeRejected, ack.RejectionCode)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if snapshot.attempt != client.streamAttempt {
		return ErrStaleRuntime
	}
	if ack.BackendRXCommittedWorkerSequence < snapshot.txBackendAcked ||
		ack.BackendRXCommittedWorkerSequence > snapshot.txLast {
		return fmt.Errorf("%w: backend event receipt cursor", ErrConflictingDuplicate)
	}
	digest, ok := client.txCursorAtLocked(ack.BackendRXCommittedWorkerSequence)
	if !ok || digest != ack.BackendRXCursorDigest {
		return fmt.Errorf("%w: backend event receipt digest", ErrConflictingDuplicate)
	}
	if ack.BackendTXLastCommandSequence < snapshot.rxLast ||
		ack.BackendTXWorkerAckedSequence > snapshot.rxLast ||
		ack.BackendTXWorkerAppliedSequence > snapshot.rxApplied {
		return fmt.Errorf("%w: backend command cursor", ErrConflictingDuplicate)
	}
	if err := validateResumeTurn(snapshot.activeTurn, ack.ActiveTurnDisposition, ack.AuthoritativeStart); err != nil {
		return err
	}
	client.latchPreActiveContinuityFailureLocked(
		snapshot.activeTurn,
		ack.ActiveTurnDisposition,
	)
	client.clearPendingTransportACKLocked(ack.BackendTXWorkerAckedSequence)
	if err := client.applyPeerEventACKLocked(
		ack.BackendRXCommittedWorkerSequence,
	); err != nil {
		return err
	}
	client.rxConfirmed = maxClientUint64(client.rxConfirmed, ack.BackendTXWorkerAckedSequence)
	client.pruneRXLocked()
	return nil
}

func (client *Client) latchPreActiveContinuityFailureLocked(
	reported *contractsv1.RuntimeControlActiveTurn,
	disposition string,
) {
	if disposition != contractsv1.RuntimeControlResumeContinuityUnproven ||
		reported != nil &&
			reported.Phase != contractsv1.RuntimeControlTurnEffectReceived &&
			reported.Phase != contractsv1.RuntimeControlTurnEffectPromptIntent {
		return
	}
	client.turnContinuationErr = ErrTurnContinuityUnproven
	client.signalStateChangedLocked()
}

func (client *Client) workerReplay(
	snapshot clientResumeSnapshot,
	ack contractsv1.RuntimeControlResumeAck,
) ([]contractsv1.RuntimeControlEnvelope, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	var replay []contractsv1.RuntimeControlEnvelope
	for sequence := ack.ReplayWorkerFromSequence; sequence <= snapshot.txLast && sequence != 0; sequence++ {
		message := client.txSpool[sequence]
		if message == nil {
			return nil, fmt.Errorf(
				"%w: missing worker replay sequence %d",
				ErrConflictingDuplicate,
				sequence,
			)
		}
		replay = append(replay, cloneEnvelope(message.envelope))
	}
	return replay, nil
}

func (client *Client) resumeReady(
	snapshot clientResumeSnapshot,
	ack contractsv1.RuntimeControlResumeAck,
) (contractsv1.RuntimeControlResumeReady, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.rxLastSequence != ack.BackendTXLastCommandSequence {
		return contractsv1.RuntimeControlResumeReady{}, fmt.Errorf(
			"%w: backend replay ended at %d, want %d",
			ErrSequenceGap,
			client.rxLastSequence,
			ack.BackendTXLastCommandSequence,
		)
	}
	if client.rxLastDigest != ack.BackendTXCursorDigest {
		return contractsv1.RuntimeControlResumeReady{}, fmt.Errorf(
			"%w: backend command cursor digest",
			ErrConflictingDuplicate,
		)
	}
	ready := contractsv1.RuntimeControlResumeReady{
		ServerConnectionID:              ack.ServerConnectionID,
		AcknowledgedJournalRevision:     ack.JournalRevision,
		WorkerTXReplayedThroughSequence: snapshot.txLast,
		WorkerTXCursorDigest:            snapshot.txDigest,
		WorkerRXReceivedThroughSequence: ack.BackendTXLastCommandSequence,
		WorkerRXAppliedSequence:         client.rxApplied,
		WorkerRXCursorDigest:            ack.BackendTXCursorDigest,
		ActiveTurn:                      cloneActiveTurn(snapshot.activeTurn),
	}
	if err := ready.Validate(); err != nil {
		return contractsv1.RuntimeControlResumeReady{}, err
	}
	return ready, nil
}

func (client *Client) acceptResumeComplete(
	snapshot clientResumeSnapshot,
	ack contractsv1.RuntimeControlResumeAck,
	ready contractsv1.RuntimeControlResumeReady,
	complete contractsv1.RuntimeControlResumeComplete,
) error {
	if err := complete.Validate(); err != nil {
		return fmt.Errorf("invalid Runtime control ResumeComplete: %w", err)
	}
	if complete.ServerConnectionID != ack.ServerConnectionID ||
		complete.JournalRevision < ack.JournalRevision ||
		complete.BackendRXCommittedWorkerSequence != snapshot.txLast ||
		complete.BackendRXCursorDigest != snapshot.txDigest ||
		complete.BackendTXLastCommandSequence != ack.BackendTXLastCommandSequence ||
		complete.BackendTXWorkerAckedSequence != ack.BackendTXLastCommandSequence ||
		complete.BackendTXWorkerAppliedSequence != ready.WorkerRXAppliedSequence ||
		complete.BackendTXCursorDigest != ack.BackendTXCursorDigest {
		return fmt.Errorf("%w: ResumeComplete cursor barrier", ErrConflictingDuplicate)
	}
	if err := validateResumeTurn(
		snapshot.activeTurn,
		complete.ActiveTurnDisposition,
		complete.AuthoritativeStart,
	); err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if complete.ActiveTurnDisposition ==
		contractsv1.RuntimeControlResumeContinuityUnproven {
		client.turnContinuationErr = ErrTurnContinuityUnproven
		client.signalStateChangedLocked()
	}
	client.clearPendingTransportACKLocked(complete.BackendTXWorkerAckedSequence)
	if err := client.applyPeerEventACKLocked(
		complete.BackendRXCommittedWorkerSequence,
	); err != nil {
		return err
	}
	client.rxConfirmed = maxClientUint64(client.rxConfirmed, complete.BackendTXWorkerAckedSequence)
	client.pruneRXLocked()
	client.signalStateChangedLocked()
	return nil
}

func validateResumeTurn(
	reported *contractsv1.RuntimeControlActiveTurn,
	disposition string,
	authoritative *contractsv1.RuntimeControlActiveTurn,
) error {
	switch disposition {
	case contractsv1.RuntimeControlResumeNoActiveTurn:
		if reported != nil || authoritative != nil {
			return ErrTurnConflict
		}
		return nil
	case contractsv1.RuntimeControlResumeContinueActive:
		if !compatibleResumeTurn(reported, authoritative) ||
			authoritative.Phase != contractsv1.RuntimeControlTurnEffectActive ||
			reported.Phase != contractsv1.RuntimeControlTurnEffectActive {
			return ErrTurnConflict
		}
		return nil
	case contractsv1.RuntimeControlResumeAlreadyTerminal:
		if !compatibleResumeTurn(reported, authoritative) ||
			reported.Phase != contractsv1.RuntimeControlTurnEffectTerminal ||
			authoritative.Phase != contractsv1.RuntimeControlTurnEffectTerminal ||
			reported.TerminalEnvelopeDigest != authoritative.TerminalEnvelopeDigest {
			return ErrTurnConflict
		}
		return nil
	case contractsv1.RuntimeControlResumeContinuityUnproven:
		if authoritative == nil {
			if reported == nil ||
				reported.Phase == contractsv1.RuntimeControlTurnEffectReceived ||
				reported.Phase == contractsv1.RuntimeControlTurnEffectPromptIntent {
				return nil
			}
			return ErrTurnConflict
		}
		if !compatibleResumeTurn(reported, authoritative) {
			return ErrTurnConflict
		}
		switch authoritative.Phase {
		case contractsv1.RuntimeControlTurnEffectReceived,
			contractsv1.RuntimeControlTurnEffectPromptIntent:
			return nil
		case contractsv1.RuntimeControlTurnEffectActive:
			if reported.Phase == contractsv1.RuntimeControlTurnEffectTerminal &&
				authoritative.LastUpdateSequence == reported.LastUpdateSequence &&
				authoritative.TerminalEnvelopeDigest == "" {
				return nil
			}
		}
		return ErrTurnConflict
	default:
		return ErrTurnConflict
	}
}

func compatibleResumeTurn(
	reported *contractsv1.RuntimeControlActiveTurn,
	authoritative *contractsv1.RuntimeControlActiveTurn,
) bool {
	return reported != nil &&
		authoritative != nil &&
		sameTurnStart(reported, authoritative) &&
		turnPhaseRank(authoritative.Phase) <= turnPhaseRank(reported.Phase) &&
		authoritative.LastUpdateSequence <= reported.LastUpdateSequence
}

func sameTurnStart(left, right *contractsv1.RuntimeControlActiveTurn) bool {
	return left != nil && right != nil &&
		left.WorkerRunID == right.WorkerRunID &&
		left.AgentSessionID == right.AgentSessionID &&
		left.AgentTurnID == right.AgentTurnID &&
		left.StartCommandSequence == right.StartCommandSequence &&
		left.StartMessageID == right.StartMessageID &&
		left.StartIdempotencyKey == right.StartIdempotencyKey &&
		left.StartEnvelopeDigest == right.StartEnvelopeDigest
}

func (client *Client) unsentWorkerEvents(
	sentThrough uint64,
) ([]contractsv1.RuntimeControlEnvelope, uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	last := client.txLastSequence
	if sentThrough >= last {
		return nil, last, nil
	}
	envelopes := make([]contractsv1.RuntimeControlEnvelope, 0, last-sentThrough)
	for sequence := sentThrough + 1; sequence <= last; sequence++ {
		message := client.txSpool[sequence]
		if message == nil {
			if sequence <= client.txBackendAcked {
				continue
			}
			return nil, last, fmt.Errorf(
				"%w: missing outbound sequence %d",
				ErrConflictingDuplicate,
				sequence,
			)
		}
		envelopes = append(envelopes, cloneEnvelope(message.envelope))
	}
	return envelopes, last, nil
}

// Publish commits an ordinary worker event to the process-local resend spool.
// Success means it will survive gRPC attempt loss, not that Backend has ACKed it.
func (client *Client) Publish(
	ctx context.Context,
	event application.WorkerControlEvent,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := client.enqueueEvent(event, false, false)
	return err
}

// TryPublishHeartbeat collapses disconnected heartbeats to at most one
// unacknowledged entry. Heartbeats never consume the terminal reserve.
func (client *Client) TryPublishHeartbeat(event application.WorkerControlEvent) error {
	client.mu.Lock()
	if !client.ready {
		for _, message := range client.txSpool {
			if message.envelope.Kind == "heartbeat" {
				client.mu.Unlock()
				return nil
			}
		}
	}
	client.mu.Unlock()
	event.Envelope.Kind = "heartbeat"
	_, err := client.enqueueEvent(event, false, false)
	return err
}

// CapacityChanged snapshots a broadcast pulse. Callers must obtain it BEFORE
// attempting nonblocking Publish: an ACK between that snapshot and a failed
// Publish closes the captured channel, so waiting cannot lose the wakeup.
// Pulses include lifecycle/continuity changes; callers must retry only
// ErrBackpressure and propagate all other errors.
func (client *Client) CapacityChanged() <-chan struct{} {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.stateChanged
}

func (client *Client) enqueueEvent(
	event application.WorkerControlEvent,
	terminal bool,
	essential bool,
) (contractsv1.RuntimeControlEnvelope, error) {
	envelope, logical, err := client.prepareEvent(event)
	if err != nil {
		return contractsv1.RuntimeControlEnvelope{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.enqueuePreparedEventLocked(envelope, logical, terminal, essential)
}

func (client *Client) prepareEvent(
	event application.WorkerControlEvent,
) (contractsv1.RuntimeControlEnvelope, [32]byte, error) {
	envelope := cloneEnvelope(event.Envelope)
	if len(event.Payload) != 0 {
		envelope.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	if !matchesRuntime(client.credential.Identity, envelope) {
		return contractsv1.RuntimeControlEnvelope{}, [32]byte{}, ErrStaleRuntime
	}
	if envelope.Kind != "ack" && envelope.Kind != "heartbeat" {
		envelope.Kind = "event"
	}
	return envelope, logicalFingerprint(envelope), nil
}

func (client *Client) enqueuePreparedEventLocked(
	envelope contractsv1.RuntimeControlEnvelope,
	logical [32]byte,
	terminal bool,
	essential bool,
) (contractsv1.RuntimeControlEnvelope, error) {
	if client.terminalErr != nil {
		return contractsv1.RuntimeControlEnvelope{}, client.terminalErr
	}
	if previous, exists := client.sentLogical[envelope.MessageID]; exists {
		if previous == logical {
			for _, message := range client.txSpool {
				if message.envelope.MessageID == envelope.MessageID {
					return cloneEnvelope(message.envelope), nil
				}
			}
			return contractsv1.RuntimeControlEnvelope{}, nil
		}
		return contractsv1.RuntimeControlEnvelope{}, ErrConflictingDuplicate
	}
	envelope.SchemaVersion = contractsv1.RuntimeControlSchemaVersion
	envelope.ChannelEpoch = client.credential.Identity.ChannelEpoch
	envelope.Sequence = client.txLastSequence + 1
	envelope.AckSequence = client.rxLastSequence
	envelope.SentAt = client.now().UTC()
	if err := envelope.Validate(); err != nil {
		return contractsv1.RuntimeControlEnvelope{}, fmt.Errorf(
			"invalid WorkerControl event: %w",
			err,
		)
	}
	size, err := outboundFrameSize(envelope)
	if err != nil {
		return contractsv1.RuntimeControlEnvelope{}, err
	}
	if size > maxEnvelopeBytes {
		return contractsv1.RuntimeControlEnvelope{}, fmt.Errorf(
			"invalid WorkerControl event: frame exceeds %d bytes",
			maxEnvelopeBytes,
		)
	}
	if err := client.logicalHistoryCapacityErrorLocked(
		envelope,
		terminal || essential,
	); err != nil {
		return contractsv1.RuntimeControlEnvelope{}, err
	}
	if !client.hasTXCapacityLocked(size, terminal || essential) {
		return contractsv1.RuntimeControlEnvelope{}, ErrBackpressure
	}
	cursor, err := contractsv1.AdvanceRuntimeControlCursor(
		client.txLastDigest,
		contractsv1.RuntimeControlDirectionRuntimeToBackend,
		envelope,
	)
	if err != nil {
		return contractsv1.RuntimeControlEnvelope{}, err
	}
	message := &clientOutbound{
		envelope:     cloneEnvelope(envelope),
		fingerprint:  envelopeFingerprint(envelope),
		cursorDigest: cursor,
		size:         size,
		terminal:     terminal,
	}
	client.txSpool[envelope.Sequence] = message
	client.txLastSequence = envelope.Sequence
	client.txLastDigest = cursor
	client.txBytes += size
	client.sentLogical[envelope.MessageID] = logical
	client.associateCommandACKLocked(envelope.Sequence, envelope.AckSequence)
	client.clearPendingTransportACKLocked(envelope.AckSequence)
	client.signalWakeLocked()
	client.signalStateChangedLocked()
	return cloneEnvelope(envelope), nil
}

func (client *Client) logicalHistoryCapacityErrorLocked(
	envelope contractsv1.RuntimeControlEnvelope,
	privileged bool,
) error {
	limit := client.logicalMaxMessages
	if !privileged {
		limit -= client.logicalProofReserveMessages
	}
	if limit < 0 {
		limit = 0
	}
	if len(client.sentLogical) < limit {
		return nil
	}
	if !privileged && envelope.Kind == "heartbeat" {
		return ErrBackpressure
	}
	return ErrLogicalHistoryCapacity
}

func (client *Client) hasTXCapacityLocked(size int, privileged bool) bool {
	messageLimit := client.txMaxMessages
	byteLimit := client.txMaxBytes
	if !privileged {
		messageLimit -= client.txReservedMessages
		byteLimit -= client.txReservedBytes
	}
	return len(client.txSpool) < messageLimit && client.txBytes+size <= byteLimit
}

func (client *Client) acceptBackendEnvelope(
	envelope contractsv1.RuntimeControlEnvelope,
	live bool,
) (duplicate bool, needsACK bool, result error) {
	if err := envelope.Validate(); err != nil {
		return false, false, fmt.Errorf("invalid WorkerControl command: %w", err)
	}
	if (envelope.Kind != "command" && envelope.Kind != "ack") ||
		!matchesRuntime(client.credential.Identity, envelope) {
		return false, false, ErrStaleRuntime
	}
	fingerprint := envelopeFingerprint(envelope)
	size := envelopeSize(envelope)
	client.mu.Lock()
	defer client.mu.Unlock()
	if envelope.AckSequence > client.txLastSequence {
		return false, false, fmt.Errorf(
			"%w: backend ACK is outside worker cursor",
			ErrConflictingDuplicate,
		)
	}
	if envelope.Sequence <= client.rxBaseSequence {
		return false, false, ErrConflictingDuplicate
	}
	if envelope.Sequence <= client.rxLastSequence {
		previous := client.rxSpool[envelope.Sequence]
		if previous != nil && previous.fingerprint == fingerprint {
			if err := client.applyPeerEventACKLocked(envelope.AckSequence); err != nil {
				return false, false, err
			}
			return true, false, nil
		}
		return false, false, ErrConflictingDuplicate
	}
	if envelope.AckSequence < client.txBackendAcked {
		return false, false, fmt.Errorf(
			"%w: backend ACK regressed on a new envelope",
			ErrConflictingDuplicate,
		)
	}
	if envelope.Sequence != client.rxLastSequence+1 {
		return false, false, ErrSequenceGap
	}
	if len(client.rxSpool) >= client.rxMaxMessages ||
		client.rxBytes+size > client.rxMaxBytes {
		return false, false, ErrBackpressure
	}
	cursor, err := contractsv1.AdvanceRuntimeControlCursor(
		client.rxLastDigest,
		contractsv1.RuntimeControlDirectionBackendToRuntime,
		envelope,
	)
	if err != nil {
		return false, false, err
	}
	client.rxSpool[envelope.Sequence] = &clientInbound{
		envelope:     cloneEnvelope(envelope),
		fingerprint:  fingerprint,
		cursorDigest: cursor,
		size:         size,
	}
	client.rxLastSequence = envelope.Sequence
	client.rxLastDigest = cursor
	client.rxBytes += size
	if err := client.applyPeerEventACKLocked(envelope.AckSequence); err != nil {
		return false, false, err
	}
	if envelope.Kind == "ack" && client.rxApplied+1 == envelope.Sequence {
		client.rxApplied = envelope.Sequence
		client.advanceAutoAppliedACKsLocked()
	}
	client.signalStateChangedLocked()
	return false, live && envelope.Kind != "ack", nil
}

func (client *Client) enqueueTransportACK(
	command contractsv1.RuntimeControlEnvelope,
) error {
	event := transportACKEvent(command)
	envelope, logical, err := client.prepareEvent(event)
	if err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if command.Sequence <= client.rxConfirmed {
		return nil
	}
	received := client.rxSpool[command.Sequence]
	if received == nil || received.fingerprint != envelopeFingerprint(command) {
		return ErrConflictingDuplicate
	}
	if received.ackTXSequence != 0 {
		return nil
	}
	if _, err := client.enqueuePreparedEventLocked(
		envelope,
		logical,
		false,
		false,
	); err == nil {
		client.clearPendingTransportACKLocked(command.Sequence)
		return nil
	} else if !errors.Is(err, ErrBackpressure) {
		return err
	}
	if client.pendingACK == nil || command.Sequence > client.pendingACK.Sequence {
		pending := cloneEnvelope(command)
		client.pendingACK = &pending
	} else if command.Sequence == client.pendingACK.Sequence &&
		envelopeFingerprint(command) != envelopeFingerprint(*client.pendingACK) {
		return ErrConflictingDuplicate
	}
	return nil
}

func transportACKEvent(
	command contractsv1.RuntimeControlEnvelope,
) application.WorkerControlEvent {
	messageID := fmt.Sprintf("worker-ack-%d", command.Sequence)
	return application.WorkerControlEvent{Envelope: contractsv1.RuntimeControlEnvelope{
		MessageID:         messageID,
		Kind:              "ack",
		WorkerSessionID:   command.WorkerSessionID,
		WorkerRunID:       command.WorkerRunID,
		RuntimeInstanceID: command.RuntimeInstanceID,
		RuntimeGeneration: command.RuntimeGeneration,
		AgentSessionID:    command.AgentSessionID,
		AgentTurnID:       command.AgentTurnID,
		ChannelEpoch:      command.ChannelEpoch,
		IdempotencyKey:    messageID,
		MessageType:       "runtime.control.ack",
		Payload: json.RawMessage(fmt.Sprintf(
			`{"through_sequence":%d}`,
			command.Sequence,
		)),
	}}
}

func (client *Client) flushPendingTransportACKLocked() error {
	if client.pendingACK == nil {
		return nil
	}
	command := cloneEnvelope(*client.pendingACK)
	envelope, logical, err := client.prepareEvent(transportACKEvent(command))
	if err != nil {
		return err
	}
	if _, err := client.enqueuePreparedEventLocked(
		envelope,
		logical,
		false,
		false,
	); err != nil {
		if errors.Is(err, ErrBackpressure) {
			return nil
		}
		return err
	}
	client.clearPendingTransportACKLocked(command.Sequence)
	return nil
}

func (client *Client) clearPendingTransportACKLocked(through uint64) {
	if client.pendingACK != nil && client.pendingACK.Sequence <= through {
		client.pendingACK = nil
	}
}

func (client *Client) deliverPendingCommands() error {
	for {
		client.mu.Lock()
		var pending *clientInbound
		for sequence := client.rxApplied + 1; sequence <= client.rxLastSequence; sequence++ {
			message := client.rxSpool[sequence]
			if message == nil {
				client.mu.Unlock()
				return ErrSequenceGap
			}
			if message.envelope.Kind == "ack" {
				client.rxApplied = sequence
				continue
			}
			if !message.delivered {
				pending = message
			}
			break
		}
		if pending == nil {
			client.advanceAutoAppliedACKsLocked()
			client.signalStateChangedLocked()
			client.mu.Unlock()
			return nil
		}
		command := application.WorkerControlCommand{
			Envelope: cloneEnvelope(pending.envelope),
			Payload:  append(json.RawMessage(nil), pending.envelope.Payload...),
		}
		pending.delivered = true
		select {
		case client.commands <- command:
			client.mu.Unlock()
			return nil
		default:
			pending.delivered = false
			client.mu.Unlock()
			return ErrBackpressure
		}
	}
}

// Commands exposes application commands only after ResumeComplete. Exact
// duplicate envelopes never produce a second delivery.
func (client *Client) Commands() <-chan application.WorkerControlCommand {
	if client == nil {
		return nil
	}
	return client.commands
}

// MarkApplied advances an ordinary command's application cursor strictly in
// backend sequence order. agent_turn.start is applied atomically by
// MarkPromptActive instead. Transport ACK envelopes are applied automatically.
func (client *Client) MarkApplied(sequence uint64) error {
	client.mu.Lock()
	if sequence != client.rxApplied+1 {
		client.mu.Unlock()
		return ErrApplicationSequence
	}
	message := client.rxSpool[sequence]
	if message == nil || message.envelope.Kind != "command" || !message.delivered {
		client.mu.Unlock()
		return ErrApplicationSequence
	}
	client.rxApplied = sequence
	client.advanceAutoAppliedACKsLocked()
	client.pruneRXLocked()
	client.signalStateChangedLocked()
	client.mu.Unlock()
	return client.deliverPendingCommands()
}

func (client *Client) advanceAutoAppliedACKsLocked() {
	for client.rxApplied < client.rxLastSequence {
		next := client.rxSpool[client.rxApplied+1]
		if next == nil || next.envelope.Kind != "ack" {
			return
		}
		client.rxApplied++
	}
}

// BeginTurn records receipt of the exact journaled agent_turn.start envelope.
// Repeating the same start is idempotent; a different live start fails closed.
func (client *Client) BeginTurn(command application.WorkerControlCommand) error {
	envelope := cloneEnvelope(command.Envelope)
	if len(command.Payload) != 0 {
		envelope.Payload = append(json.RawMessage(nil), command.Payload...)
	}
	if envelope.MessageType != "agent_turn.start" ||
		envelope.Kind != "command" ||
		!matchesRuntime(client.credential.Identity, envelope) {
		return ErrTurnConflict
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		return err
	}
	effect := &contractsv1.RuntimeControlActiveTurn{
		WorkerRunID:          envelope.WorkerRunID,
		AgentSessionID:       envelope.AgentSessionID,
		AgentTurnID:          envelope.AgentTurnID,
		StartCommandSequence: envelope.Sequence,
		StartMessageID:       envelope.MessageID,
		StartIdempotencyKey:  envelope.IdempotencyKey,
		StartEnvelopeDigest:  digest,
		Phase:                contractsv1.RuntimeControlTurnEffectReceived,
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.terminalErr != nil {
		return client.terminalErr
	}
	if client.turnContinuationErr != nil {
		return client.turnContinuationErr
	}
	received := client.rxSpool[envelope.Sequence]
	if received == nil || received.fingerprint != envelopeFingerprint(envelope) {
		return ErrTurnConflict
	}
	if client.activeTurn != nil {
		if sameTurnStart(client.activeTurn, effect) {
			return nil
		}
		if client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectTerminal &&
			effect.StartCommandSequence > client.activeTurn.StartCommandSequence {
			client.activeTurn = effect
			client.activeProofSequence = 0
			client.signalStateChangedLocked()
			return nil
		}
		return ErrTurnConflict
	}
	client.activeTurn = effect
	client.activeProofSequence = 0
	client.signalStateChangedLocked()
	return nil
}

func (client *Client) MarkPromptIntent(agentTurnID string) error {
	return client.advanceTurnPhase(agentTurnID, contractsv1.RuntimeControlTurnEffectPromptIntent)
}

// MarkPromptActive atomically records that Prompt took effect and applies the
// exact agent_turn.start command. Callers must not separately MarkApplied that
// start sequence.
func (client *Client) MarkPromptActive(agentTurnID string) error {
	client.mu.Lock()
	if client.turnContinuationErr != nil {
		client.mu.Unlock()
		return client.turnContinuationErr
	}
	if client.activeTurn == nil ||
		string(client.activeTurn.AgentTurnID) != agentTurnID ||
		client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectTerminal {
		client.mu.Unlock()
		return ErrTurnConflict
	}
	if client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectActive {
		client.mu.Unlock()
		return nil
	}
	if client.activeTurn.Phase != contractsv1.RuntimeControlTurnEffectPromptIntent ||
		client.activeTurn.StartCommandSequence != client.rxApplied+1 {
		client.mu.Unlock()
		return ErrApplicationSequence
	}
	start := client.rxSpool[client.activeTurn.StartCommandSequence]
	if start == nil ||
		!start.delivered ||
		start.envelope.Kind != "command" ||
		start.envelope.MessageType != "agent_turn.start" ||
		start.envelope.MessageID != client.activeTurn.StartMessageID ||
		start.envelope.IdempotencyKey != client.activeTurn.StartIdempotencyKey {
		client.mu.Unlock()
		return ErrApplicationSequence
	}
	client.rxApplied = client.activeTurn.StartCommandSequence
	client.activeTurn.Phase = contractsv1.RuntimeControlTurnEffectActive
	client.advanceAutoAppliedACKsLocked()
	client.pruneRXLocked()
	client.signalStateChangedLocked()
	client.mu.Unlock()
	return client.deliverPendingCommands()
}

func (client *Client) advanceTurnPhase(agentTurnID, phase string) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.terminalErr != nil {
		return client.terminalErr
	}
	if client.turnContinuationErr != nil {
		return client.turnContinuationErr
	}
	if client.activeTurn == nil ||
		string(client.activeTurn.AgentTurnID) != agentTurnID ||
		client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectTerminal {
		return ErrTurnConflict
	}
	current := turnPhaseRank(client.activeTurn.Phase)
	next := turnPhaseRank(phase)
	if next < current || next > current+1 {
		return ErrTurnConflict
	}
	if next == current {
		return nil
	}
	if phase == contractsv1.RuntimeControlTurnEffectPromptIntent &&
		!client.hasPromptProofCapacityLocked() {
		return ErrLogicalHistoryCapacity
	}
	client.activeTurn.Phase = phase
	client.signalStateChangedLocked()
	return nil
}

func (client *Client) hasPromptProofCapacityLocked() bool {
	if client.logicalMaxMessages < len(client.sentLogical) ||
		client.logicalProofReserveMessages < 0 {
		return false
	}
	return client.logicalMaxMessages-len(client.sentLogical) >=
		client.logicalProofReserveMessages
}

// PublishTurnUpdate uses the terminal reserve when terminal is true and binds
// the exact resulting envelope digest to the active Turn proof.
func (client *Client) PublishTurnUpdate(
	ctx context.Context,
	event application.WorkerControlEvent,
	terminal bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	envelope, logical, err := client.prepareEvent(event)
	if err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.turnContinuationErr != nil {
		return client.turnContinuationErr
	}
	if client.activeTurn == nil ||
		envelope.WorkerRunID != client.activeTurn.WorkerRunID ||
		envelope.AgentSessionID != client.activeTurn.AgentSessionID ||
		envelope.AgentTurnID != client.activeTurn.AgentTurnID {
		return ErrTurnConflict
	}
	if client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectTerminal &&
		!terminal {
		return ErrTurnConflict
	}
	if terminal && envelope.MessageType != "agent_turn.terminal" {
		return ErrTurnConflict
	}
	if client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectTerminal {
		existing := client.txSpool[client.activeTurn.LastUpdateSequence]
		if existing == nil ||
			existing.envelope.MessageID != envelope.MessageID ||
			client.sentLogical[envelope.MessageID] != logical {
			return ErrTurnConflict
		}
	}
	useReservedCapacity := terminal ||
		(client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectActive &&
			client.activeProofSequence == 0 &&
			envelope.MessageType == "agent_turn.effect")
	envelope, err = client.enqueuePreparedEventLocked(
		envelope,
		logical,
		terminal,
		useReservedCapacity,
	)
	if err != nil {
		return err
	}
	digest, err := contractsv1.RuntimeControlEnvelopeDigest(envelope)
	if err != nil {
		return err
	}
	client.activeTurn.LastUpdateSequence = envelope.Sequence
	if !terminal &&
		client.activeTurn.Phase == contractsv1.RuntimeControlTurnEffectActive &&
		client.activeProofSequence == 0 &&
		envelope.MessageType == "agent_turn.effect" {
		client.activeProofSequence = envelope.Sequence
	}
	if terminal {
		client.activeTurn.Phase = contractsv1.RuntimeControlTurnEffectTerminal
		client.activeTurn.TerminalEnvelopeDigest = digest
	}
	client.signalStateChangedLocked()
	return nil
}

func turnPhaseRank(phase string) int {
	switch phase {
	case contractsv1.RuntimeControlTurnEffectReceived:
		return 1
	case contractsv1.RuntimeControlTurnEffectPromptIntent:
		return 2
	case contractsv1.RuntimeControlTurnEffectActive:
		return 3
	case contractsv1.RuntimeControlTurnEffectTerminal:
		return 4
	default:
		return -1
	}
}

// ReadyTransitions emits a pulse immediately after a ResumeComplete barrier
// and a false pulse when that ready attempt is lost.
func (client *Client) ReadyTransitions() <-chan ClientReadyTransition {
	if client == nil {
		return nil
	}
	return client.readyTransitions
}

// Drain waits until Backend has durably acknowledged every worker event and
// every received command has been applied.
func (client *Client) Drain(ctx context.Context) error {
	for {
		client.mu.Lock()
		if client.terminalErr != nil {
			err := client.terminalErr
			client.mu.Unlock()
			return err
		}
		if client.txBackendAcked == client.txLastSequence &&
			client.rxApplied == client.rxLastSequence {
			client.mu.Unlock()
			return nil
		}
		changed := client.stateChanged
		client.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (client *Client) markReady(
	ctx context.Context,
	attempt uint64,
	complete contractsv1.RuntimeControlResumeComplete,
) error {
	client.mu.Lock()
	client.ready = true
	client.signalStateChangedLocked()
	transition := ClientReadyTransition{
		Ready:                 true,
		StreamAttempt:         attempt,
		ActiveTurnDisposition: complete.ActiveTurnDisposition,
		AuthoritativeStart:    cloneActiveTurn(complete.AuthoritativeStart),
		OccurredAt:            client.now().UTC(),
	}
	client.mu.Unlock()
	return client.publishReadyTransition(ctx, transition)
}

func (client *Client) markNotReady(cause error) {
	client.mu.Lock()
	if !client.ready {
		client.mu.Unlock()
		return
	}
	client.ready = false
	client.signalStateChangedLocked()
	transition := ClientReadyTransition{
		Ready:         false,
		StreamAttempt: client.streamAttempt,
		OccurredAt:    client.now().UTC(),
		Cause:         cause,
	}
	client.mu.Unlock()
	_ = client.publishReadyTransition(context.Background(), transition)
}

func (client *Client) publishReadyTransition(
	ctx context.Context,
	transition ClientReadyTransition,
) error {
	if transition.Ready &&
		(transition.ActiveTurnDisposition ==
			contractsv1.RuntimeControlResumeContinuityUnproven ||
			transition.ActiveTurnDisposition ==
				contractsv1.RuntimeControlResumeAlreadyTerminal) {
		select {
		case client.readyTransitions <- transition:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case client.readyTransitions <- transition:
	default:
	}
	return nil
}

func (client *Client) applyPeerEventACKLocked(sequence uint64) error {
	if sequence <= client.txBackendAcked {
		return nil
	}
	client.txBackendAcked = sequence
	for current := client.txBaseSequence + 1; current <= sequence; current++ {
		message := client.txSpool[current]
		if message == nil {
			continue
		}
		client.txBaseSequence = current
		client.txBaseDigest = message.cursorDigest
		client.txBytes -= message.size
		delete(client.txSpool, current)
	}
	client.updateRXConfirmationLocked()
	client.signalStateChangedLocked()
	return client.flushPendingTransportACKLocked()
}

func (client *Client) txCursorAtLocked(sequence uint64) (string, bool) {
	if sequence == client.txBaseSequence {
		return client.txBaseDigest, true
	}
	message := client.txSpool[sequence]
	if message == nil {
		return "", false
	}
	return message.cursorDigest, true
}

func (client *Client) associateCommandACKLocked(
	eventSequence uint64,
	commandThrough uint64,
) {
	for sequence := client.rxBaseSequence + 1; sequence <= commandThrough; sequence++ {
		message := client.rxSpool[sequence]
		if message != nil && message.ackTXSequence == 0 {
			message.ackTXSequence = eventSequence
		}
	}
}

func (client *Client) updateRXConfirmationLocked() {
	for sequence := client.rxConfirmed + 1; sequence <= client.rxLastSequence; sequence++ {
		message := client.rxSpool[sequence]
		if message == nil || message.ackTXSequence == 0 ||
			message.ackTXSequence > client.txBackendAcked {
			break
		}
		client.rxConfirmed = sequence
	}
	client.pruneRXLocked()
}

func (client *Client) pruneRXLocked() {
	through := minClientUint64(client.rxApplied, client.rxConfirmed)
	for sequence := client.rxBaseSequence + 1; sequence <= through; sequence++ {
		message := client.rxSpool[sequence]
		if message == nil {
			continue
		}
		client.rxBaseSequence = sequence
		client.rxBaseDigest = message.cursorDigest
		client.rxBytes -= message.size
		delete(client.rxSpool, sequence)
	}
}

func (client *Client) signalWakeLocked() {
	select {
	case client.wake <- struct{}{}:
	default:
	}
}

func (client *Client) signalStateChangedLocked() {
	close(client.stateChanged)
	client.stateChanged = make(chan struct{})
}

func cloneActiveTurn(
	active *contractsv1.RuntimeControlActiveTurn,
) *contractsv1.RuntimeControlActiveTurn {
	if active == nil {
		return nil
	}
	cloned := *active
	return &cloned
}

func cloneEnvelope(
	envelope contractsv1.RuntimeControlEnvelope,
) contractsv1.RuntimeControlEnvelope {
	envelope.Payload = append(json.RawMessage(nil), envelope.Payload...)
	return envelope
}

func envelopeSize(envelope contractsv1.RuntimeControlEnvelope) int {
	encoded, _ := json.Marshal(envelope)
	return len(encoded)
}

func outboundFrameSize(
	envelope contractsv1.RuntimeControlEnvelope,
) (int, error) {
	encoded, err := json.Marshal(envelopeFrame(&envelope))
	if err != nil {
		return 0, fmt.Errorf("encode WorkerControl event frame: %w", err)
	}
	return len(encoded), nil
}

func newShimProcessNonce() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "shim-process-" + base64.RawURLEncoding.EncodeToString(random), nil
}

func minClientUint64(left, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func maxClientUint64(left, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}

func transientClientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	case codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument,
		codes.FailedPrecondition, codes.AlreadyExists, codes.DataLoss,
		codes.ResourceExhausted:
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

// Connect is the legacy one-shot envelope stream kept for source
// compatibility. New Runtime code must use Run; a server that requires the
// resume handshake will reject this legacy stream.
func (client *Client) Connect(ctx context.Context) (*ClientStream, error) {
	if client == nil || client.rpc == nil {
		return nil, fmt.Errorf("WorkerControl client is not configured")
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"authorization", "Bearer "+client.credential.Token,
	))
	rpcStream, err := client.rpc.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect WorkerControl stream: %w", err)
	}
	return &ClientStream{
		rpc:         rpcStream,
		identity:    client.credential.Identity,
		sentLogical: make(map[string][32]byte),
		received:    make(map[uint64][32]byte),
		now:         time.Now,
	}, nil
}

// ClientStream owns independent worker->backend and backend->worker cursors.
type ClientStream struct {
	rpc      WorkerControlRPC_ConnectClient
	identity RuntimeIdentity
	now      func() time.Time

	sendMu sync.Mutex
	recvMu sync.Mutex
	mu     sync.Mutex

	sendSequence uint64
	recvSequence uint64
	peerAck      uint64
	sentLogical  map[string][32]byte
	received     map[uint64][32]byte
}

func (stream *ClientStream) SendEvent(event application.WorkerControlEvent) error {
	if stream == nil || stream.rpc == nil {
		return ErrNotConnected
	}
	stream.sendMu.Lock()
	defer stream.sendMu.Unlock()
	envelope := event.Envelope
	if len(event.Payload) != 0 {
		envelope.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	if !matchesRuntime(stream.identity, envelope) {
		return ErrStaleRuntime
	}
	if envelope.Kind != "ack" && envelope.Kind != "heartbeat" {
		envelope.Kind = "event"
	}
	logical := logicalFingerprint(envelope)
	stream.mu.Lock()
	if previous, exists := stream.sentLogical[envelope.MessageID]; exists {
		stream.mu.Unlock()
		if previous == logical {
			return nil
		}
		return ErrConflictingDuplicate
	}
	envelope.SchemaVersion = contractsv1.RuntimeControlSchemaVersion
	envelope.ChannelEpoch = stream.identity.ChannelEpoch
	envelope.Sequence = stream.sendSequence + 1
	envelope.AckSequence = stream.recvSequence
	envelope.SentAt = stream.now().UTC()
	stream.mu.Unlock()
	if err := envelope.Validate(); err != nil {
		return fmt.Errorf("invalid WorkerControl event: %w", err)
	}
	if err := stream.rpc.Send(&envelope); err != nil {
		return fmt.Errorf("send WorkerControl event: %w", err)
	}
	stream.mu.Lock()
	stream.sendSequence = envelope.Sequence
	stream.sentLogical[envelope.MessageID] = logical
	stream.mu.Unlock()
	return nil
}

func (stream *ClientStream) ReceiveCommand() (application.WorkerControlCommand, error) {
	if stream == nil || stream.rpc == nil {
		return application.WorkerControlCommand{}, ErrNotConnected
	}
	stream.recvMu.Lock()
	defer stream.recvMu.Unlock()
	for {
		envelope, err := stream.rpc.Recv()
		if err != nil {
			return application.WorkerControlCommand{}, err
		}
		duplicate, err := stream.acceptCommand(*envelope)
		if err != nil {
			return application.WorkerControlCommand{}, err
		}
		if duplicate {
			continue
		}
		return application.WorkerControlCommand{
			Envelope: *envelope,
			Payload:  append(json.RawMessage(nil), envelope.Payload...),
		}, nil
	}
}

func (stream *ClientStream) acceptCommand(envelope contractsv1.RuntimeControlEnvelope) (bool, error) {
	if err := envelope.Validate(); err != nil {
		return false, fmt.Errorf("invalid WorkerControl command: %w", err)
	}
	if envelope.Kind != "command" || !matchesRuntime(stream.identity, envelope) {
		return false, ErrStaleRuntime
	}
	fingerprint := envelopeFingerprint(envelope)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if envelope.Sequence <= stream.recvSequence {
		if previous, ok := stream.received[envelope.Sequence]; ok && previous == fingerprint {
			return true, nil
		}
		return false, ErrConflictingDuplicate
	}
	if envelope.AckSequence < stream.peerAck || envelope.AckSequence > stream.sendSequence {
		return false, fmt.Errorf("WorkerControl ACK is outside the sent event cursor")
	}
	if envelope.Sequence != stream.recvSequence+1 {
		return false, ErrSequenceGap
	}
	stream.recvSequence = envelope.Sequence
	stream.peerAck = envelope.AckSequence
	stream.received[envelope.Sequence] = fingerprint
	return false, nil
}

func (stream *ClientStream) CloseSend() error {
	if stream == nil || stream.rpc == nil {
		return nil
	}
	return stream.rpc.CloseSend()
}
