package control

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/ids"
)

type EventDisposition uint8

const (
	EventDispositionInvalid EventDisposition = iota
	EventDispositionAuthoritative
	EventDispositionDiagnosticOnly
)

// EventHandler persists a validated worker event and tells the transport
// whether it was accepted as authoritative state or retained only as a
// diagnostic.
type EventHandler interface {
	HandleWorkerEvent(context.Context, Identity, *workercontrolv1.WorkerEnvelope) (EventDisposition, error)
}

// Server owns the authenticated WorkerControl stream protocol.
type Server struct {
	workercontrolv1.UnimplementedWorkerControlServer
	registry *Registry
	handler  EventHandler
}

func NewServer(registry *Registry, handler EventHandler) *Server {
	return &Server{registry: registry, handler: handler}
}

func (s *Server) Connect(stream workercontrolv1.WorkerControl_ConnectServer) error {
	if s.registry == nil {
		return status.Error(codes.FailedPrecondition, "worker control registry is not configured")
	}
	expected, err := s.registry.Authenticate(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, "worker authentication failed")
	}
	first, err := stream.Recv()
	if err != nil {
		return streamStatus(err)
	}
	if err := ValidateWorkerEnvelope(first, Cursor{}); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if !identityMatches(expected, first.GetMeta().GetIdentity()) {
		return status.Error(codes.PermissionDenied, "worker identity does not match credential")
	}
	connection, err := s.registry.attach(expected)
	if err != nil {
		if errors.Is(err, ErrAlreadyConnected) {
			return status.Error(codes.AlreadyExists, err.Error())
		}
		return status.Error(codes.InvalidArgument, err.Error())
	}

	if err := s.persistEvent(stream.Context(), connection, first); err != nil {
		s.registry.fenceAndWait(expected, connection, err)
		return err
	}
	results := make(chan streamLoopResult, 3)
	go func() { results <- streamLoopResult{err: s.receiveLoop(stream, connection)} }()
	go func() { results <- streamLoopResult{err: s.sendLoop(stream, connection)} }()
	go func() {
		<-connection.done
		results <- streamLoopResult{err: connection.terminalError(), externallyFenced: true}
	}()
	result := <-results
	if !result.externallyFenced {
		s.registry.fenceForStreamExit(expected, connection, result.err)
	}
	return connectionStatus(result.err)
}

type streamLoopResult struct {
	err              error
	externallyFenced bool
}

func (s *Server) receiveLoop(stream workercontrolv1.WorkerControl_ConnectServer, connection *Connection) error {
	for {
		event, err := stream.Recv()
		if err != nil {
			return streamStatus(err)
		}
		if err := s.persistEvent(stream.Context(), connection, event); err != nil {
			return err
		}
	}
}

func (s *Server) sendLoop(stream workercontrolv1.WorkerControl_ConnectServer, connection *Connection) error {
	for {
		select {
		case <-connection.done:
			return connection.terminalError()
		default:
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-connection.done:
			return connection.terminalError()
		case command := <-connection.Commands:
			operation, err := s.registry.admit(connection.Identity, connection)
			if err != nil {
				return err
			}
			connection.mu.Lock()
			connection.SendSequence++
			sequence := connection.SendSequence
			ack := connection.RecvSequence
			connection.mu.Unlock()

			turnID := command.GetMeta().GetIdentity().GetAgentTurnId()
			command.Meta = &workercontrolv1.EnvelopeMeta{
				MessageId:      ids.New(),
				StreamSequence: sequence,
				AckSequence:    ack,
				Identity: &workercontrolv1.Identity{
					WorkerSessionId:   connection.Identity.WorkerSessionID,
					WorkerRunId:       connection.Identity.WorkerRunID,
					RuntimeInstanceId: connection.Identity.RuntimeInstanceID,
					RuntimeGeneration: connection.Identity.RuntimeGeneration,
					AgentTurnId:       turnID,
				},
				SentAt: timestamppb.Now(),
			}
			err = stream.Send(command)
			operation.release()
			if err != nil {
				return streamStatus(err)
			}
		}
	}
}

func (s *Server) persistEvent(ctx context.Context, connection *Connection, event *workercontrolv1.WorkerEnvelope) error {
	operation, err := s.registry.admit(connection.Identity, connection)
	if err != nil {
		return connectionStatus(err)
	}
	defer operation.release()

	connection.mu.Lock()
	cursor := Cursor{
		LastInboundSequence: connection.RecvSequence,
		LastPeerSequence:    connection.SendSequence,
	}
	peerAck := connection.PeerAck
	connection.mu.Unlock()

	if err = ValidateWorkerEnvelope(event, cursor); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if !identityMatches(connection.Identity, event.GetMeta().GetIdentity()) {
		return status.Error(codes.PermissionDenied, "worker identity does not match credential")
	}
	if event.GetMeta().GetAckSequence() < peerAck {
		return status.Error(codes.InvalidArgument, "ack sequence must not move backwards")
	}
	if s.handler == nil {
		return status.Error(codes.FailedPrecondition, "worker event handler is not configured")
	}
	disposition, err := s.handler.HandleWorkerEvent(ctx, connection.Identity, event)
	if err != nil {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if disposition != EventDispositionAuthoritative && disposition != EventDispositionDiagnosticOnly {
		return status.Error(codes.FailedPrecondition, "worker event handler returned an invalid disposition")
	}

	connection.mu.Lock()
	connection.RecvSequence = event.GetMeta().GetStreamSequence()
	connection.PeerAck = event.GetMeta().GetAckSequence()
	connection.mu.Unlock()
	if event.GetRuntimeReady() != nil && disposition == EventDispositionAuthoritative {
		connection.markReady()
	}
	return nil
}

func identityMatches(expected Identity, actual *workercontrolv1.Identity) bool {
	return actual != nil &&
		expected.WorkerSessionID == actual.GetWorkerSessionId() &&
		expected.WorkerRunID == actual.GetWorkerRunId() &&
		expected.RuntimeInstanceID == actual.GetRuntimeInstanceId() &&
		expected.RuntimeGeneration == actual.GetRuntimeGeneration()
}

func streamStatus(err error) error {
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case status.Code(err) != codes.Unknown:
		return err
	default:
		return status.Error(codes.Unavailable, err.Error())
	}
}

func connectionStatus(err error) error {
	switch {
	case err == nil || errors.Is(err, io.EOF):
		return nil
	case errors.Is(err, ErrBackpressure):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, "worker authentication failed")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	default:
		return err
	}
}
