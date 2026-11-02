package controlgrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

const (
	codecName        = "switchback-control-json"
	serviceName      = "switchback.workercontrol.v1.WorkerControl"
	connectMethod    = "/" + serviceName + "/Connect"
	maxEnvelopeBytes = 256 << 10
)

type strictJSONCodec struct{}

func (strictJSONCodec) Name() string { return codecName }

func (strictJSONCodec) Marshal(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode WorkerControl frame: %w", err)
	}
	if len(encoded) > maxEnvelopeBytes {
		return nil, fmt.Errorf("WorkerControl frame exceeds %d bytes", maxEnvelopeBytes)
	}
	return encoded, nil
}

func (strictJSONCodec) Unmarshal(data []byte, value any) error {
	if len(data) == 0 || len(data) > maxEnvelopeBytes {
		return fmt.Errorf("WorkerControl frame size is invalid")
	}
	if frame, ok := value.(*contractsv1.RuntimeControlFrame); ok {
		decoded, err := contractsv1.DecodeRuntimeControlFrame(data)
		if err != nil {
			return fmt.Errorf("decode WorkerControl frame: %w", err)
		}
		*frame = decoded
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode WorkerControl frame")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("decode WorkerControl frame")
	}
	return nil
}

func init() {
	encoding.RegisterCodec(strictJSONCodec{})
}

type WorkerControlRPCClient interface {
	Connect(context.Context, ...grpc.CallOption) (WorkerControlRPC_ConnectClient, error)
}

type workerControlRPCClient struct{ connection grpc.ClientConnInterface }

func NewWorkerControlRPCClient(connection grpc.ClientConnInterface) WorkerControlRPCClient {
	return &workerControlRPCClient{connection: connection}
}

func (c *workerControlRPCClient) Connect(ctx context.Context, options ...grpc.CallOption) (WorkerControlRPC_ConnectClient, error) {
	if c == nil || c.connection == nil {
		return nil, fmt.Errorf("WorkerControl gRPC connection is required")
	}
	options = append([]grpc.CallOption{grpc.CallContentSubtype(codecName)}, options...)
	stream, err := c.connection.NewStream(ctx, &workerControlRPCServiceDesc.Streams[0], connectMethod, options...)
	if err != nil {
		return nil, err
	}
	return &workerControlRPCConnectClient{ClientStream: stream}, nil
}

type WorkerControlRPC_ConnectClient interface {
	Send(*contractsv1.RuntimeControlEnvelope) error
	Recv() (*contractsv1.RuntimeControlEnvelope, error)
	SendFrame(*contractsv1.RuntimeControlFrame) error
	RecvFrame() (*contractsv1.RuntimeControlFrame, error)
	CloseSend() error
	grpc.ClientStream
}

type workerControlRPCConnectClient struct{ grpc.ClientStream }

func (c *workerControlRPCConnectClient) Send(envelope *contractsv1.RuntimeControlEnvelope) error {
	return c.SendFrame(envelopeFrame(envelope))
}

func (c *workerControlRPCConnectClient) Recv() (*contractsv1.RuntimeControlEnvelope, error) {
	frame, err := c.RecvFrame()
	if err != nil {
		return nil, err
	}
	if frame.Envelope == nil {
		return nil, fmt.Errorf("WorkerControl frame is not an envelope")
	}
	return frame.Envelope, nil
}

func (c *workerControlRPCConnectClient) SendFrame(frame *contractsv1.RuntimeControlFrame) error {
	return c.ClientStream.SendMsg(frame)
}

func (c *workerControlRPCConnectClient) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	frame := new(contractsv1.RuntimeControlFrame)
	if err := c.ClientStream.RecvMsg(frame); err != nil {
		return nil, err
	}
	return frame, nil
}

type WorkerControlRPCServer interface {
	Connect(WorkerControlRPC_ConnectServer) error
}

type WorkerControlRPC_ConnectServer interface {
	Send(*contractsv1.RuntimeControlEnvelope) error
	Recv() (*contractsv1.RuntimeControlEnvelope, error)
	SendFrame(*contractsv1.RuntimeControlFrame) error
	RecvFrame() (*contractsv1.RuntimeControlFrame, error)
	grpc.ServerStream
}

type workerControlRPCConnectServer struct{ grpc.ServerStream }

func (s *workerControlRPCConnectServer) Send(envelope *contractsv1.RuntimeControlEnvelope) error {
	return s.SendFrame(envelopeFrame(envelope))
}

func (s *workerControlRPCConnectServer) Recv() (*contractsv1.RuntimeControlEnvelope, error) {
	frame, err := s.RecvFrame()
	if err != nil {
		return nil, err
	}
	if frame.Envelope == nil {
		return nil, fmt.Errorf("WorkerControl frame is not an envelope")
	}
	return frame.Envelope, nil
}

func (s *workerControlRPCConnectServer) SendFrame(frame *contractsv1.RuntimeControlFrame) error {
	return s.ServerStream.SendMsg(frame)
}

func (s *workerControlRPCConnectServer) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	frame := new(contractsv1.RuntimeControlFrame)
	if err := s.ServerStream.RecvMsg(frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func RegisterWorkerControlRPCServer(registrar grpc.ServiceRegistrar, server WorkerControlRPCServer) {
	registrar.RegisterService(&workerControlRPCServiceDesc, server)
}

func workerControlConnectHandler(server any, stream grpc.ServerStream) error {
	return server.(WorkerControlRPCServer).Connect(&workerControlRPCConnectServer{ServerStream: stream})
}

var workerControlRPCServiceDesc = grpc.ServiceDesc{
	ServiceName: serviceName,
	HandlerType: (*WorkerControlRPCServer)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName: "Connect", Handler: workerControlConnectHandler,
		ServerStreams: true, ClientStreams: true,
	}},
	Metadata: "api/schema/v1/runtime-control-frame.schema.json",
}

func envelopeFrame(envelope *contractsv1.RuntimeControlEnvelope) *contractsv1.RuntimeControlFrame {
	return &contractsv1.RuntimeControlFrame{
		SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion,
		FrameType:     contractsv1.RuntimeControlFrameTypeEnvelope,
		Envelope:      envelope,
	}
}
