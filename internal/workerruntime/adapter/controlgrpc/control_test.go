package controlgrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
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

func TestServerRejectsMissingAuthenticationBeforeReadingResumeHello(t *testing.T) {
	_, rpc, cleanup := startTestControl(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := rpc.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := stream.RecvFrame(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("received frame=%+v error=%v, want Unauthenticated", frame, err)
	}
}

func TestServerMapsUnknownJournalFailureToRetryableUnavailable(t *testing.T) {
	journal := &scriptedControlJournal{
		claim: func(
			context.Context,
			application.ClaimRuntimeControlStreamCommand,
		) (application.RuntimeControlReplayPlan, error) {
			return application.RuntimeControlReplayPlan{},
				errors.New("postgres connection was reset")
		},
	}
	rpc, cleanup := startDurableTestControl(t, newHub(journal))
	defer cleanup()
	credential := testCredential()
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
	hello := testResumeHello(credential.Identity, 1)
	if err := stream.SendFrame(resumeHelloFrame(hello)); err != nil {
		t.Fatal(err)
	}
	if frame, err := stream.RecvFrame(); status.Code(err) != codes.Unavailable {
		t.Fatalf("received frame=%+v error=%v, want retryable Unavailable", frame, err)
	}
}

func TestServerMapsSettlementResumeRacesToRetryableUnavailable(t *testing.T) {
	for _, err := range []error{
		application.ErrRuntimeControlPendingTurnSettlement,
		application.ErrRuntimeControlResumeSnapshotChanged,
	} {
		if code := status.Code(controlStatus(err)); code != codes.Unavailable {
			t.Fatalf("resume race %v mapped to %s", err, code)
		}
	}
}

func TestGeneratedCredentialIsScopedAndRedacted(t *testing.T) {
	identity := testCredential().Identity
	credential, err := NewCredential(identity)
	if err != nil {
		t.Fatalf("generate credential: %v", err)
	}
	if credential.Identity != identity || !validToken(credential.Token) || len(credential.Token) < 40 {
		t.Fatalf("generated credential = %v", credential)
	}
	formatted := fmt.Sprintf("%v %#v", credential, credential)
	if strings.Contains(formatted, credential.Token) {
		t.Fatalf("credential formatting leaked token: %s", formatted)
	}
}

func startTestControl(t *testing.T) (*Hub, WorkerControlRPCClient, func()) {
	t.Helper()
	hub := NewHub()
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
	cleanup := func() {
		_ = connection.Close()
		server.Stop()
		_ = listener.Close()
	}
	return hub, NewWorkerControlRPCClient(connection), cleanup
}

func testCredential() Credential {
	return Credential{
		Identity: RuntimeIdentity{
			WorkerSessionID: "worker-session-1", RuntimeInstanceID: "runtime-1",
			RuntimeGeneration: 1, ChannelEpoch: 7,
		},
		Token: "worker-control-token",
	}
}

func testEnvelope(
	identity RuntimeIdentity,
	messageID string,
	messageType string,
	payload json.RawMessage,
) contractsv1.RuntimeControlEnvelope {
	return contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion,
		MessageID:     messageID, Kind: "event",
		WorkerSessionID:   contractsv1.WorkerSessionID(identity.WorkerSessionID),
		WorkerRunID:       contractsv1.WorkerRunID("worker-run-1"),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(identity.RuntimeInstanceID),
		RuntimeGeneration: identity.RuntimeGeneration,
		AgentSessionID:    contractsv1.AgentSessionID("agent-session-1"),
		AgentTurnID:       contractsv1.AgentTurnID("agent-turn-1"),
		ChannelEpoch:      identity.ChannelEpoch,
		IdempotencyKey:    messageID, MessageType: messageType,
		Payload: payload,
	}
}

func receiveEvent(
	t *testing.T,
	events <-chan application.WorkerControlEvent,
) application.WorkerControlEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for WorkerControl event")
		return application.WorkerControlEvent{}
	}
}
