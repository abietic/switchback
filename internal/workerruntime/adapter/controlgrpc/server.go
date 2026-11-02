package controlgrpc

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server terminates authenticated WorkerControl streams. The journal is the
// authority; the stream becomes live only after ResumeComplete is durably
// committed and successfully written to the peer.
type Server struct{ hub *Hub }

func NewServer(hub *Hub) *Server { return &Server{hub: hub} }

func (server *Server) Connect(stream WorkerControlRPC_ConnectServer) (result error) {
	if server == nil || server.hub == nil {
		return status.Error(codes.FailedPrecondition, "WorkerControl is not configured")
	}
	token, err := bearerFromContext(stream.Context())
	if err != nil {
		return controlStatus(err)
	}
	first, err := stream.RecvFrame()
	if err != nil {
		return controlStatus(err)
	}
	if first.FrameType != contractsv1.RuntimeControlFrameTypeResumeHello || first.ResumeHello == nil {
		return status.Error(codes.InvalidArgument, "WorkerControl first frame must be ResumeHello")
	}
	if server.hub.journal == nil {
		return status.Error(codes.FailedPrecondition, "WorkerControl durable journal is not configured")
	}
	hello := *first.ResumeHello
	connectionID, err := newConnectionID()
	if err != nil {
		return status.Error(codes.Internal, "create WorkerControl connection authority")
	}
	plan, err := server.hub.journal.Claim(stream.Context(), application.ClaimRuntimeControlStreamCommand{
		WorkerSessionID:                        string(hello.WorkerSessionID),
		RuntimeInstanceID:                      string(hello.RuntimeInstanceID),
		RuntimeGeneration:                      uint64(hello.RuntimeGeneration),
		ChannelEpoch:                           hello.ChannelEpoch,
		Credential:                             token,
		ProcessNonce:                           hello.ShimProcessNonce,
		StreamAttempt:                          hello.StreamAttempt,
		OwnerID:                                connectionID,
		WorkerEventLastSequence:                hello.WorkerTXLastSequence,
		WorkerEventBackendAcknowledgedSequence: hello.WorkerTXBackendAckedSequence,
		WorkerEventCursorDigest:                hello.WorkerTXCursorDigest,
		WorkerCommandLastSequence:              hello.WorkerRXLastSequence,
		WorkerCommandAppliedSequence:           hello.WorkerRXAppliedSequence,
		WorkerCommandCursorDigest:              hello.WorkerRXCursorDigest,
		ActiveTurn:                             hello.ActiveTurn,
	})
	if err != nil {
		if errors.Is(err, application.ErrRuntimeControlCredentialConflict) {
			return controlStatus(err)
		}
		rejectionCode, ok := resumeRejectionCode(err)
		if !ok {
			return controlStatus(err)
		}
		rejected := contractsv1.RuntimeControlFrame{
			SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
			FrameType:     contractsv1.RuntimeControlFrameTypeResumeAck,
			ResumeAck: &contractsv1.RuntimeControlResumeAck{
				Accepted:      false,
				RejectionCode: rejectionCode,
			},
		}
		if sendErr := stream.SendFrame(&rejected); sendErr != nil {
			return controlStatus(errors.Join(err, sendErr))
		}
		return controlStatus(err)
	}
	if plan.Channel.OwnerID != connectionID {
		return status.Error(codes.Internal, "Runtime control journal returned the wrong connection authority")
	}
	identity := RuntimeIdentity{
		WorkerSessionID:   string(hello.WorkerSessionID),
		RuntimeInstanceID: string(hello.RuntimeInstanceID),
		RuntimeGeneration: hello.RuntimeGeneration,
		ChannelEpoch:      hello.ChannelEpoch,
	}
	authority := application.RuntimeControlAuthority{
		WorkerSessionID:   identity.WorkerSessionID,
		RuntimeInstanceID: identity.RuntimeInstanceID,
		RuntimeGeneration: uint64(identity.RuntimeGeneration),
		ChannelEpoch:      identity.ChannelEpoch,
		ProcessNonce:      plan.Channel.ProcessNonce,
		StreamAttempt:     plan.Channel.StreamAttempt,
		OwnerFence:        plan.Channel.OwnerFence,
	}
	active, err := server.hub.attachClaimed(identity, authority, connectionID)
	if err != nil {
		return controlStatus(err)
	}
	defer func() {
		result = errors.Join(result, server.hub.detach(stream.Context(), active, result))
		result = controlStatus(result)
	}()

	ack, err := acceptedResumeAck(plan)
	if err != nil {
		return err
	}
	if err := stream.SendFrame(&contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeResumeAck,
		ResumeAck:     &ack,
	}); err != nil {
		return err
	}

	incoming := startRuntimeControlReceivePump(stream, active)
	sentThrough, err := server.sendJournalMessages(stream, active, plan.PendingCommands)
	if err != nil {
		return err
	}

	for {
		received, ok := <-incoming
		if !ok {
			return io.EOF
		}
		if received.err != nil {
			return received.err
		}
		frame := received.frame
		switch frame.FrameType {
		case contractsv1.RuntimeControlFrameTypeEnvelope:
			if err := server.hub.acceptEvent(stream.Context(), active, *frame.Envelope, false); err != nil {
				return err
			}
		case contractsv1.RuntimeControlFrameTypeResumeReady:
			state, err := server.hub.journal.CompleteResume(
				stream.Context(),
				application.CompleteRuntimeControlResumeCommand{
					Authority: active.authority,
					Ready:     *frame.ResumeReady,
				},
			)
			if err != nil {
				return err
			}
			complete, err := completedResume(active.connectionID, state)
			if err != nil {
				return err
			}
			if err := stream.SendFrame(&contractsv1.RuntimeControlFrame{
				SchemaVersion:  contractsv1.RuntimeControlFrameSchemaVersion,
				FrameType:      contractsv1.RuntimeControlFrameTypeResumeComplete,
				ResumeComplete: &complete,
			}); err != nil {
				return err
			}
			active.markLive(maxUint64(sentThrough, state.CommandCommittedSequence))
			server.hub.startPendingEventPump(active)
			return server.serveLive(stream, active, incoming)
		default:
			return status.Errorf(
				codes.InvalidArgument,
				"RuntimeControl frame %q is invalid during resume",
				frame.FrameType,
			)
		}
	}
}

type receivedRuntimeControlFrame struct {
	frame *contractsv1.RuntimeControlFrame
	err   error
}

func startRuntimeControlReceivePump(
	stream WorkerControlRPC_ConnectServer,
	active *connection,
) <-chan receivedRuntimeControlFrame {
	incoming := make(chan receivedRuntimeControlFrame, controlQueueCapacity)
	go func() {
		defer close(incoming)
		for {
			frame, err := stream.RecvFrame()
			received := receivedRuntimeControlFrame{frame: frame, err: err}
			select {
			case <-active.done:
				return
			case incoming <- received:
			}
			if err != nil {
				return
			}
		}
	}()
	return incoming
}

func (server *Server) serveLive(
	stream WorkerControlRPC_ConnectServer,
	active *connection,
	incoming <-chan receivedRuntimeControlFrame,
) error {
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-active.done:
			return active.terminalError()
		case received, ok := <-incoming:
			if !ok {
				return io.EOF
			}
			if received.err != nil {
				return received.err
			}
			if received.frame.FrameType != contractsv1.RuntimeControlFrameTypeEnvelope ||
				received.frame.Envelope == nil {
				return status.Errorf(
					codes.InvalidArgument,
					"RuntimeControl frame %q is invalid after resume",
					received.frame.FrameType,
				)
			}
			if err := server.hub.acceptEvent(
				stream.Context(),
				active,
				*received.frame.Envelope,
				true,
			); err != nil {
				return err
			}
		case <-active.sendWake:
			plan, err := server.hub.journal.ReplayPlan(stream.Context(), active.authority)
			if err != nil {
				return err
			}
			if _, err := server.sendJournalMessages(stream, active, plan.PendingCommands); err != nil {
				return err
			}
		}
	}
}

func (server *Server) sendJournalMessages(
	stream WorkerControlRPC_ConnectServer,
	active *connection,
	messages []application.RuntimeControlMessage,
) (uint64, error) {
	var sentThrough uint64
	for _, message := range messages {
		if !active.nextUnsent(message.Sequence) {
			sentThrough = maxUint64(sentThrough, message.Sequence)
			continue
		}
		envelope, err := decodeJournalEnvelope(message)
		if err != nil {
			return sentThrough, err
		}
		if envelope.Kind != "command" && envelope.Kind != "ack" {
			return sentThrough, fmt.Errorf(
				"journaled backend envelope at sequence %d has kind %q",
				envelope.Sequence,
				envelope.Kind,
			)
		}
		if err := stream.SendFrame(envelopeFrame(&envelope)); err != nil {
			return sentThrough, err
		}
		active.markSent(message.Sequence)
		sentThrough = message.Sequence
	}
	return sentThrough, nil
}

func newConnectionID() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "server-connection-" + base64.RawURLEncoding.EncodeToString(random), nil
}

func resumeRejectionCode(err error) (string, bool) {
	switch {
	case errors.Is(err, application.ErrRuntimeControlStreamAttemptStale):
		return contractsv1.RuntimeControlResumeRejectedStaleAttempt, true
	case errors.Is(err, application.ErrRuntimeControlLeaseHeld):
		return contractsv1.RuntimeControlResumeRejectedLeaseHeld, true
	case errors.Is(err, application.ErrRuntimeControlIdentityConflict),
		errors.Is(err, application.ErrRuntimeControlStaleOwner):
		return contractsv1.RuntimeControlResumeRejectedStaleRuntime, true
	case errors.Is(err, application.ErrRuntimeControlCursorConflict),
		errors.Is(err, application.ErrRuntimeControlSequenceGap):
		return contractsv1.RuntimeControlResumeRejectedCursorConflict, true
	case errors.Is(err, application.ErrRuntimeControlMessageConflict):
		return contractsv1.RuntimeControlResumeRejectedTurnConflict, true
	default:
		return "", false
	}
}

func controlStatus(err error) error {
	switch {
	case err == nil || errors.Is(err, io.EOF):
		return nil
	case errors.Is(err, ErrUnauthenticated),
		errors.Is(err, application.ErrRuntimeControlCredentialConflict):
		return status.Error(codes.Unauthenticated, "worker authentication failed")
	case errors.Is(err, ErrAlreadyConnected),
		errors.Is(err, application.ErrRuntimeControlLeaseHeld):
		return status.Error(codes.AlreadyExists, "Runtime already has an active control stream")
	case errors.Is(err, ErrStaleRuntime),
		errors.Is(err, application.ErrRuntimeControlIdentityConflict),
		errors.Is(err, application.ErrRuntimeControlStaleOwner):
		return status.Error(codes.PermissionDenied, "Runtime generation, epoch, or owner is stale")
	case errors.Is(err, ErrResumeInProgress),
		errors.Is(err, ErrJournalUnavailable),
		errors.Is(err, application.ErrRuntimeControlStreamAttemptStale):
		return status.Error(codes.FailedPrecondition, "Runtime control resume precondition failed")
	case errors.Is(
		err,
		application.ErrRuntimeControlResumeSnapshotChanged,
	), errors.Is(
		err,
		application.ErrRuntimeControlPendingTurnSettlement,
	):
		return status.Error(
			codes.Unavailable,
			"Runtime control durable authority changed during resume",
		)
	case errors.Is(err, ErrBackpressure):
		return status.Error(codes.ResourceExhausted, "WorkerControl backpressure")
	case errors.Is(err, ErrSequenceGap), errors.Is(err, ErrConflictingDuplicate),
		errors.Is(err, application.ErrRuntimeControlSequenceGap),
		errors.Is(err, application.ErrRuntimeControlMessageConflict),
		errors.Is(err, application.ErrRuntimeControlCursorConflict):
		return status.Error(codes.InvalidArgument, "WorkerControl sequence or cursor is invalid")
	case status.Code(err) != codes.Unknown:
		return err
	default:
		return status.Error(codes.Unavailable, "WorkerControl internal dependency is unavailable")
	}
}

func maxUint64(left, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}
