package control_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
	. "switchback.local/spikes/worker-runtime/backend/internal/control"
	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	"switchback.local/spikes/worker-runtime/backend/internal/runtime"
	"switchback.local/spikes/worker-runtime/backend/internal/service"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
)

func TestConnectRejectsAuthenticationBeforeRegistration(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := &recordingEventHandler{}
	client, cleanup := startBufconnServer(t, registry, handler)
	defer cleanup()

	tests := []struct {
		name          string
		authorization string
	}{
		{name: "missing"},
		{name: "wrong", authorization: "Bearer wrong-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.authorization != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, "authorization", test.authorization)
			}
			stream, err := client.Connect(ctx)
			if err != nil {
				t.Fatalf("Connect() setup error = %v", err)
			}
			_ = stream.Send(runtimeReadyEnvelope(identity, 1, 0))
			if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("Recv() status = %v, want Unauthenticated", err)
			}
		})
	}
	if got := handler.count(); got != 0 {
		t.Fatalf("persisted events = %d, want 0", got)
	}
}

func TestConnectRequiresExactRuntimeIdentityAndRejectsDuplicateStream(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := &recordingEventHandler{}
	client, cleanup := startBufconnServer(t, registry, handler)
	defer cleanup()

	mismatch := identity
	mismatch.RuntimeInstanceID = "runtime-other"
	mismatchStream := authenticatedStream(t, client, "worker-token")
	_ = mismatchStream.Send(runtimeReadyEnvelope(mismatch, 1, 0))
	if _, err := mismatchStream.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mismatch Recv() status = %v, want PermissionDenied", err)
	}
	first := authenticatedStream(t, client, "worker-token")
	_ = first.Send(runtimeReadyEnvelope(identity, 1, 0))
	readyContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := registry.WaitReady(readyContext, identity); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}

	second := authenticatedStream(t, client, "worker-token")
	_ = second.Send(runtimeReadyEnvelope(identity, 1, 0))
	if _, err := second.Recv(); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("second Recv() status = %v, want AlreadyExists", err)
	}
	if got := handler.count(); got != 1 {
		t.Fatalf("persisted events = %d, want only first RuntimeReady", got)
	}
	_ = first.CloseSend()
}

func TestConnectMaintainsIndependentSequencesAndReadiness(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := &recordingEventHandler{}
	client, cleanup := startBufconnServer(t, registry, handler)
	defer cleanup()
	streamContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := authenticatedStreamContext(t, client, streamContext, "worker-token")

	diagnostic := workerEnvelope(identity, "", 1, 0, &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
		RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{Level: "info", Code: "booting"},
	})
	if err := stream.Send(diagnostic); err != nil {
		t.Fatalf("diagnostic Send() error = %v", err)
	}
	notReadyContext, stopWait := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stopWait()
	if err := registry.WaitReady(notReadyContext, identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() before RuntimeReady error = %v, want deadline", err)
	}
	if err := stream.Send(runtimeReadyEnvelope(identity, 2, 0)); err != nil {
		t.Fatalf("RuntimeReady Send() error = %v", err)
	}
	readyContext, stopReady := context.WithTimeout(context.Background(), time.Second)
	defer stopReady()
	if err := registry.WaitReady(readyContext, identity); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}

	firstCommand := backendCommand("turn-1", "first")
	if err := registry.Send(context.Background(), identity, firstCommand); err != nil {
		t.Fatalf("first command Send() error = %v", err)
	}
	firstReceived, err := stream.Recv()
	if err != nil {
		t.Fatalf("first command Recv() error = %v", err)
	}
	assertEnvelopeCursor(t, firstReceived.GetMeta(), 1, 2)

	update := workerEnvelope(identity, "turn-1", 3, 1, &workercontrolv1.WorkerEnvelope_AcpUpdate{
		AcpUpdate: &workercontrolv1.ACPUpdate{Kind: "tool_call"},
	})
	if err := stream.Send(update); err != nil {
		t.Fatalf("ACP update Send() error = %v", err)
	}
	handler.waitCount(t, 3)
	secondCommand := backendCommand("turn-1", "second")
	if err := registry.Send(context.Background(), identity, secondCommand); err != nil {
		t.Fatalf("second command Send() error = %v", err)
	}
	secondReceived, err := stream.Recv()
	if err != nil {
		t.Fatalf("second command Recv() error = %v", err)
	}
	assertEnvelopeCursor(t, secondReceived.GetMeta(), 2, 3)

	duplicate := workerEnvelope(identity, "turn-1", 3, 2, &workercontrolv1.WorkerEnvelope_AcpUpdate{
		AcpUpdate: &workercontrolv1.ACPUpdate{Kind: "duplicate"},
	})
	if err := stream.Send(duplicate); err != nil {
		t.Fatalf("duplicate Send() error = %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("duplicate Recv() status = %v, want InvalidArgument", err)
	}
	if got := handler.count(); got != 3 {
		t.Fatalf("persisted events = %d, want diagnostic, ready, and one ACP update", got)
	}
}

func TestConnectRejectsMissingOrInvalidSentAtBeforeHandling(t *testing.T) {
	tests := []struct {
		name   string
		sentAt *timestamppb.Timestamp
	}{
		{name: "missing"},
		{name: "invalid", sentAt: &timestamppb.Timestamp{Seconds: 253402300800}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			identity := testIdentity()
			if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
				t.Fatalf("RegisterCredential() error = %v", err)
			}
			handler := &recordingEventHandler{}
			client, cleanup := startBufconnServer(t, registry, handler)
			defer cleanup()

			invalidContext, stopInvalid := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer stopInvalid()
			stream := authenticatedStreamContext(t, client, invalidContext, "worker-token")
			invalid := runtimeReadyEnvelope(identity, 1, 0)
			invalid.Meta.SentAt = test.sentAt
			if err := stream.Send(invalid); err != nil {
				t.Fatalf("invalid RuntimeReady Send() setup error = %v", err)
			}
			if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid RuntimeReady status = %v, want InvalidArgument", err)
			}
			if got := handler.count(); got != 0 {
				t.Fatalf("handled events after invalid sent_at = %d, want 0", got)
			}

			reconnected := authenticatedStream(t, client, "worker-token")
			if err := reconnected.Send(runtimeReadyEnvelope(identity, 1, 0)); err != nil {
				t.Fatalf("reconnected RuntimeReady Send() error = %v", err)
			}
			readyContext, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := registry.WaitReady(readyContext, identity); err != nil {
				t.Fatalf("WaitReady() after explicit reconnect error = %v", err)
			}
			if got := handler.count(); got != 1 {
				t.Fatalf("handled events after valid reconnect = %d, want 1", got)
			}
			_ = reconnected.CloseSend()
		})
	}
}

func TestConnectRejectsRuntimeGenerationTwo(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := &recordingEventHandler{}
	client, cleanup := startBufconnServer(t, registry, handler)
	defer cleanup()

	generationTwo := identity
	generationTwo.RuntimeGeneration = 2
	invalidContext, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	stream := authenticatedStreamContext(t, client, invalidContext, "worker-token")
	if err := stream.Send(runtimeReadyEnvelope(generationTwo, 1, 0)); err != nil {
		t.Fatalf("generation 2 RuntimeReady Send() setup error = %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("generation 2 RuntimeReady status = %v, want InvalidArgument", err)
	}
	if got := handler.count(); got != 0 {
		t.Fatalf("handled generation 2 events = %d, want 0", got)
	}
}

func TestConnectCompletionUsesAuthoritativeEvidenceAndPreservesTwoTurnLifecycle(t *testing.T) {
	registry := NewRegistry()
	provisioner := newCapturingProvisioner()
	coordinator := service.NewCoordinator(service.Options{
		RootContext:  context.Background(),
		Materializer: controlTestMaterializer{},
		ObjectStore: objectstore.LocalFileObjectStore{
			Root: t.TempDir(),
		},
		Scheduler: newControlRuntimeScheduler(t, registry, provisioner),
	})
	created, err := coordinator.CreateRun(context.Background(), service.CreateRunInput{
		Goal:               "fix the fixture",
		AcceptanceCriteria: []string{"tests pass"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	provisioning := provisioner.waitProvision(t)
	if provisioning.Identity.WorkerSessionID != created.WorkerSessionID {
		t.Fatalf("provisioned session ID = %q, want %q", provisioning.Identity.WorkerSessionID, created.WorkerSessionID)
	}
	if provisioning.Identity.WorkerRunID != created.WorkerRunID {
		t.Fatalf("provisioned run ID = %q, want %q", provisioning.Identity.WorkerRunID, created.WorkerRunID)
	}
	if provisioning.WorkerToken == "" {
		t.Fatal("provisioned worker token is empty")
	}

	client, cleanup := startBufconnServer(t, registry, coordinator)
	defer cleanup()
	streamContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := authenticatedStreamContext(t, client, streamContext, provisioning.WorkerToken)
	identity := provisioning.Identity
	if err := stream.Send(runtimeReadyEnvelope(identity, 1, 0)); err != nil {
		t.Fatalf("RuntimeReady Send() error = %v", err)
	}
	initialize, err := stream.Recv()
	if err != nil {
		t.Fatalf("InitializeSession Recv() error = %v", err)
	}
	if initialize.GetInitializeSession() == nil {
		t.Fatalf("first backend command = %T, want InitializeSession", initialize.GetPayload())
	}

	agentReady := workerEnvelope(identity, "", 2, 1, &workercontrolv1.WorkerEnvelope_AgentSessionReady{
		AgentSessionReady: &workercontrolv1.AgentSessionReady{AgentSessionId: "agent-session-1", ProtocolVersion: "1"},
	})
	if err := stream.Send(agentReady); err != nil {
		t.Fatalf("AgentSessionReady Send() error = %v", err)
	}
	firstStart, err := stream.Recv()
	if err != nil {
		t.Fatalf("first StartAgentTurn Recv() error = %v", err)
	}
	if firstStart.GetStartAgentTurn() == nil || firstStart.GetMeta().GetIdentity().GetAgentTurnId() != created.TurnID {
		t.Fatalf("first turn command = %+v, want turn %q", firstStart, created.TurnID)
	}
	if err := stream.Send(workerEnvelope(identity, created.TurnID, 3, 2, &workercontrolv1.WorkerEnvelope_TurnStarted{
		TurnStarted: &workercontrolv1.TurnStarted{},
	})); err != nil {
		t.Fatalf("first TurnStarted Send() error = %v", err)
	}
	beforeIncomplete := waitRunState(t, coordinator, created.WorkerRunID, "turn_running")

	firstEvidence := uploadTurnEvidence(t, coordinator, provisioning, created.TurnID)
	incomplete := firstEvidence[:3]
	incompleteEvent := workerEnvelope(identity, created.TurnID, 4, 2, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{AgentSessionId: "agent-session-1", Evidence: incomplete},
	})
	if err := stream.Send(incompleteEvent); err != nil {
		t.Fatalf("incomplete TurnCompleted Send() setup error = %v", err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("incomplete TurnCompleted status = %v, want FailedPrecondition", err)
	}
	afterIncomplete, err := coordinator.Snapshot(created.WorkerRunID)
	if err != nil {
		t.Fatalf("Snapshot() after incomplete completion error = %v", err)
	}
	if afterIncomplete.State != "turn_running" ||
		afterIncomplete.ActiveTurnID != created.TurnID ||
		afterIncomplete.Revision != beforeIncomplete.Revision {
		t.Fatalf(
			"run after incomplete completion = state %q active turn %q revision %d, want unchanged running revision %d",
			afterIncomplete.State,
			afterIncomplete.ActiveTurnID,
			afterIncomplete.Revision,
			beforeIncomplete.Revision,
		)
	}

	stream = authenticatedStreamContext(t, client, streamContext, provisioning.WorkerToken)
	firstComplete := workerEnvelope(identity, created.TurnID, 1, 0, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{AgentSessionId: "agent-session-1", Evidence: firstEvidence},
	})
	if err := stream.Send(firstComplete); err != nil {
		t.Fatalf("first TurnCompleted Send() error = %v", err)
	}
	waitRunState(t, coordinator, created.WorkerRunID, "idle")

	second, err := coordinator.CreateTurn(context.Background(), created.WorkerSessionID, "add a boundary test")
	if err != nil {
		t.Fatalf("CreateTurn() error = %v", err)
	}
	secondStart, err := stream.Recv()
	if err != nil {
		t.Fatalf("second StartAgentTurn Recv() error = %v", err)
	}
	if secondStart.GetMeta().GetIdentity().GetAgentTurnId() != second.TurnID {
		t.Fatalf("second turn command turn = %q, want %q", secondStart.GetMeta().GetIdentity().GetAgentTurnId(), second.TurnID)
	}
	assertEnvelopeCursor(t, secondStart.GetMeta(), 1, 1)
	if err := stream.Send(workerEnvelope(identity, second.TurnID, 2, 1, &workercontrolv1.WorkerEnvelope_TurnStarted{
		TurnStarted: &workercontrolv1.TurnStarted{},
	})); err != nil {
		t.Fatalf("second TurnStarted Send() error = %v", err)
	}
	waitRunState(t, coordinator, created.WorkerRunID, "turn_running")
	secondEvidence := uploadTurnEvidence(t, coordinator, provisioning, second.TurnID)
	if err := stream.Send(workerEnvelope(identity, second.TurnID, 3, 1, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{AgentSessionId: "agent-session-1", Evidence: secondEvidence},
	})); err != nil {
		t.Fatalf("second TurnCompleted Send() error = %v", err)
	}
	snapshot := waitRunState(t, coordinator, created.WorkerRunID, "completed")
	if snapshot.ExecutionOutcome != "completed" {
		t.Fatalf("execution outcome = %q, want completed", snapshot.ExecutionOutcome)
	}
}

func TestConnectTurnFailedPreservesPartialEvidenceAndBoundsLateDiagnostics(t *testing.T) {
	harness := startRunningTurn(t)
	partial := uploadEvidenceKinds(
		t,
		harness.coordinator,
		harness.provisioning,
		harness.created.TurnID,
		[]objectstore.EvidenceKind{objectstore.EvidenceTrace},
	)
	failed := workerEnvelope(harness.identity, harness.created.TurnID, 4, 2, &workercontrolv1.WorkerEnvelope_TurnFailed{
		TurnFailed: &workercontrolv1.TurnFailed{
			Code:            "agent_failed",
			Detail:          "test failure",
			PartialEvidence: partial,
		},
	})
	if err := harness.stream.Send(failed); err != nil {
		t.Fatalf("TurnFailed Send() error = %v", err)
	}
	snapshot := waitRunState(t, harness.coordinator, harness.created.WorkerRunID, "failed")
	if snapshot.ExecutionOutcome != "failed" {
		t.Fatalf("execution outcome = %q, want failed", snapshot.ExecutionOutcome)
	}

	for index := 0; index < service.MaxRuntimeDiagnostics+25; index++ {
		event := workerEnvelope(harness.identity, "", uint64(5+index), 2, &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
			RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{
				Level:  "warning",
				Code:   fmt.Sprintf("late-%03d", index),
				Detail: "after terminal failure",
			},
		})
		if err := harness.stream.Send(event); err != nil {
			t.Fatalf("late diagnostic #%d Send() error = %v", index, err)
		}
	}
	wantLatest := fmt.Sprintf("late-%03d", service.MaxRuntimeDiagnostics+24)
	waitLatestDiagnostic(t, harness.coordinator, harness.created.WorkerRunID, wantLatest)
	diagnostics, err := harness.coordinator.Diagnostics(harness.created.WorkerRunID)
	if err != nil {
		t.Fatalf("Diagnostics() error = %v", err)
	}
	if len(diagnostics) != service.MaxRuntimeDiagnostics {
		t.Fatalf("diagnostics = %d, want bounded %d", len(diagnostics), service.MaxRuntimeDiagnostics)
	}
	if diagnostics[len(diagnostics)-1].Code != "late_worker_event" || diagnostics[len(diagnostics)-1].Detail != wantLatest {
		t.Fatalf("last diagnostic = %+v, want newest late event", diagnostics[len(diagnostics)-1])
	}
	afterLate, err := harness.coordinator.Snapshot(harness.created.WorkerRunID)
	if err != nil {
		t.Fatalf("Snapshot() after late events error = %v", err)
	}
	if afterLate.State != "failed" || afterLate.ExecutionOutcome != "failed" || afterLate.Revision != snapshot.Revision {
		t.Fatalf("run after late events = %+v, want unchanged failed terminal state", afterLate)
	}

	harness.provisioner.blockCleanup()
	t.Cleanup(harness.provisioner.releaseCleanup)
	if err := harness.coordinator.Cleanup(context.Background(), harness.created.WorkerSessionID); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	harness.provisioner.waitCleanup(t)
	if _, err := harness.stream.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream after cleanup fence status = %v, want Unauthenticated", err)
	}
	harness.provisioner.releaseCleanup()
}

func TestConnectCleanupLateRuntimeReadyDoesNotUnblockOrInitialize(t *testing.T) {
	registry := NewRegistry()
	controlPlane := newBlockingRevokeControlPlane(registry)
	provisioner := newCapturingProvisioner()
	coordinator := service.NewCoordinator(service.Options{
		RootContext:  context.Background(),
		Materializer: controlTestMaterializer{},
		ObjectStore: objectstore.LocalFileObjectStore{
			Root: t.TempDir(),
		},
		Scheduler: newControlRuntimeScheduler(t, controlPlane, provisioner),
	})
	created, err := coordinator.CreateRun(context.Background(), service.CreateRunInput{
		Goal:               "fix the fixture",
		AcceptanceCriteria: []string{"tests pass"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	provisioning := provisioner.waitProvision(t)
	client, cleanup := startBufconnServer(t, registry, coordinator)
	defer cleanup()
	streamContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := authenticatedStreamContext(t, client, streamContext, provisioning.WorkerToken)
	if err := stream.Send(workerEnvelope(provisioning.Identity, "", 1, 0, &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
		RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{Level: "info", Code: "connected"},
	})); err != nil {
		t.Fatalf("connected diagnostic Send() error = %v", err)
	}
	waitDiagnostics(t, coordinator, created.WorkerRunID, 1)

	if err := coordinator.Cleanup(context.Background(), created.WorkerSessionID); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	controlPlane.waitRevoke(t)
	if err := stream.Send(runtimeReadyEnvelope(provisioning.Identity, 2, 0)); err != nil {
		t.Fatalf("cleanup-late RuntimeReady Send() error = %v", err)
	}
	waitDiagnostics(t, coordinator, created.WorkerRunID, 2)

	notReadyContext, stopWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopWait()
	if err := registry.WaitReady(notReadyContext, provisioning.Identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() after cleanup-late RuntimeReady error = %v, want deadline", err)
	}

	controlPlane.releaseRevoke()
	for {
		command, err := stream.Recv()
		if err != nil {
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("stream after cleanup revoke status = %v, want Unauthenticated", err)
			}
			break
		}
		if command.GetInitializeSession() != nil {
			t.Fatal("cleanup-late RuntimeReady emitted InitializeSession")
		}
	}
}

func TestConnectBackpressureClosesRuntimeWithBoundedQueue(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := newBlockingMutationHandler()
	t.Cleanup(handler.release)
	client, cleanup := startBufconnServer(t, registry, handler)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := authenticatedStreamContext(t, client, ctx, "worker-token")
	if err := stream.Send(runtimeReadyEnvelope(identity, 1, 0)); err != nil {
		t.Fatalf("RuntimeReady Send() error = %v", err)
	}
	handler.waitStarted(t)

	waitContext, waitObserved, stopWait := newObservedDoneContext(time.Second)
	defer stopWait()
	waitReadyDone := make(chan error, 1)
	go func() { waitReadyDone <- registry.WaitReady(waitContext, identity) }()
	select {
	case <-waitObserved:
	case <-time.After(time.Second):
		t.Fatal("WaitReady() did not begin observing the active connection")
	}

	command := backendCommand("turn-1", "queued")
	sendDone := make(chan error, 1)
	go func() {
		for index := 0; index < 32; index++ {
			if err := registry.Send(context.Background(), identity, command); err != nil {
				sendDone <- fmt.Errorf("fill command queue at item %d: %w", index+1, err)
				return
			}
		}
		sendDone <- registry.Send(context.Background(), identity, command)
	}()
	if err := <-waitReadyDone; !errors.Is(err, ErrBackpressure) {
		t.Fatalf("WaitReady() after backpressure error = %v, want ErrBackpressure", err)
	}
	select {
	case err := <-sendDone:
		t.Fatalf("backpressure Send() returned before admitted event drained: %v", err)
	default:
	}
	if err := stream.Send(workerEnvelope(identity, "", 2, 0, &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
		RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{Level: "warning", Code: "post-backpressure"},
	})); err != nil {
		t.Fatalf("post-backpressure event Send() setup error = %v", err)
	}

	handler.release()
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrBackpressure) {
			t.Fatalf("full queue Send() error = %v, want ErrBackpressure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backpressure Send() did not return after admitted event drained")
	}
	if _, err := stream.Recv(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("stream close status = %v, want ResourceExhausted", err)
	}
	if got := handler.count(); got != 1 {
		t.Fatalf("handled events after backpressure fence = %d, want only RuntimeReady", got)
	}
}

func TestConnectLocalReceiveFailureReturnsBeforeBlockedSendDrains(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := &recordingEventHandler{}
	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer worker-token"),
	))
	defer cancel()
	stream := newBlockedSendRejectedReceiveStream(ctx, identity)
	connectDone := make(chan error, 1)
	go func() {
		connectDone <- NewServer(registry, handler).Connect(stream)
	}()

	readyContext, stopReady := context.WithTimeout(context.Background(), time.Second)
	defer stopReady()
	if err := registry.WaitReady(readyContext, identity); err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if err := registry.Send(context.Background(), identity, backendCommand("turn-1", "blocked")); err != nil {
		t.Fatalf("command Send() error = %v", err)
	}
	select {
	case <-stream.sendStarted:
	case <-time.After(time.Second):
		t.Fatal("server stream Send() did not block")
	}
	close(stream.rejectReceive)

	select {
	case err := <-connectDone:
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("Connect() status = %v, want InvalidArgument", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Connect() did not return after local receive rejection while stream.Send was blocked")
	}
	select {
	case <-stream.sendReturned:
		t.Fatal("blocked stream.Send returned before transport cancellation")
	default:
	}
	if got := handler.count(); got != 1 {
		t.Fatalf("handled events = %d, want only RuntimeReady", got)
	}

	cancel()
	select {
	case <-stream.sendReturned:
	case <-time.After(time.Second):
		t.Fatal("blocked stream.Send did not return after transport cancellation")
	}
}

func TestConnectRevokeWaitsForAdmittedStateMutationAndFencesPostFenceEvent(t *testing.T) {
	registry := NewRegistry()
	identity := testIdentity()
	if err := registry.RegisterCredential(Credential{Identity: identity, Token: "worker-token"}); err != nil {
		t.Fatalf("RegisterCredential() error = %v", err)
	}
	handler := newBlockingMutationHandler()
	client, cleanup := startBufconnServer(t, registry, handler)
	defer cleanup()
	streamContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := authenticatedStreamContext(t, client, streamContext, "worker-token")

	if err := stream.Send(workerEnvelope(identity, "", 1, 0, &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
		RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{Level: "info", Code: "half-commit"},
	})); err != nil {
		t.Fatalf("first diagnostic Send() error = %v", err)
	}
	handler.waitStarted(t)

	waitReadyDone := make(chan error, 1)
	go func() { waitReadyDone <- registry.WaitReady(context.Background(), identity) }()
	revokeDone := make(chan struct{})
	go func() {
		registry.Revoke(identity)
		close(revokeDone)
	}()
	if err := <-waitReadyDone; !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("WaitReady() after revoke error = %v, want ErrUnauthenticated", err)
	}
	select {
	case <-revokeDone:
		t.Fatal("Revoke() returned while the event mutation was half-committed")
	default:
	}
	if err := stream.Send(workerEnvelope(identity, "", 2, 0, &workercontrolv1.WorkerEnvelope_RuntimeDiagnostic{
		RuntimeDiagnostic: &workercontrolv1.RuntimeDiagnostic{Level: "warning", Code: "post-fence"},
	})); err != nil {
		t.Fatalf("post-fence diagnostic Send() setup error = %v", err)
	}

	handler.release()
	select {
	case <-revokeDone:
	case <-time.After(time.Second):
		t.Fatal("Revoke() did not return after the admitted mutation committed")
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream after revoke status = %v, want Unauthenticated", err)
	}
	if got := handler.count(); got != 1 {
		t.Fatalf("handled state mutations = %d, want only the pre-fence event", got)
	}
}

func TestConnectRevokeDrainsAdmittedTerminalMutationAndRejectsStaleTerminalEvent(t *testing.T) {
	var blocker *blockingDelegatingHandler
	harness := startRunningTurnWithHandler(t, func(next EventHandler) EventHandler {
		blocker = newBlockingDelegatingHandler(next)
		return blocker
	})
	t.Cleanup(blocker.release)
	evidence := uploadTurnEvidence(t, harness.coordinator, harness.provisioning, harness.created.TurnID)
	completed := workerEnvelope(harness.identity, harness.created.TurnID, 4, 2, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{
			AgentSessionId: "agent-session-1",
			Evidence:       evidence,
		},
	})
	if err := harness.stream.Send(completed); err != nil {
		t.Fatalf("TurnCompleted Send() error = %v", err)
	}
	blocker.waitStarted(t)

	revokeDone := make(chan struct{})
	go func() {
		harness.registry.Revoke(harness.identity)
		close(revokeDone)
	}()
	waitCredentialRevoked(t, harness.registry, harness.provisioning.WorkerToken)
	select {
	case <-revokeDone:
		t.Fatal("Revoke() returned while an admitted terminal mutation was half-committed")
	default:
	}
	_ = harness.stream.Send(workerEnvelope(harness.identity, harness.created.TurnID, 5, 2, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{
			AgentSessionId: "agent-session-1",
			Evidence:       evidence,
		},
	}))

	blocker.release()
	select {
	case <-revokeDone:
	case <-time.After(time.Second):
		t.Fatal("Revoke() did not return after the admitted terminal mutation committed")
	}
	if _, err := harness.stream.Recv(); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream after terminal revoke status = %v, want Unauthenticated", err)
	}
	snapshot := waitRunState(t, harness.coordinator, harness.created.WorkerRunID, "idle")
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("active turn after admitted completion = %q, want empty", snapshot.ActiveTurnID)
	}
	if got := blocker.terminalCalls(); got != 1 {
		t.Fatalf("delegated terminal mutations = %d, want exactly 1", got)
	}
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

type blockedSendRejectedReceiveStream struct {
	grpc.ServerStream
	ctx           context.Context
	identity      Identity
	rejectReceive chan struct{}
	sendStarted   chan struct{}
	sendReturned  chan struct{}
	sendStartOnce sync.Once
	sendDoneOnce  sync.Once
	recvMu        sync.Mutex
	recvCount     int
}

func newBlockedSendRejectedReceiveStream(
	ctx context.Context,
	identity Identity,
) *blockedSendRejectedReceiveStream {
	return &blockedSendRejectedReceiveStream{
		ctx:           ctx,
		identity:      identity,
		rejectReceive: make(chan struct{}),
		sendStarted:   make(chan struct{}),
		sendReturned:  make(chan struct{}),
	}
}

func (s *blockedSendRejectedReceiveStream) Context() context.Context {
	return s.ctx
}

func (s *blockedSendRejectedReceiveStream) Recv() (*workercontrolv1.WorkerEnvelope, error) {
	s.recvMu.Lock()
	index := s.recvCount
	s.recvCount++
	s.recvMu.Unlock()
	if index == 0 {
		return runtimeReadyEnvelope(s.identity, 1, 0), nil
	}
	if index == 1 {
		select {
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-s.rejectReceive:
			return runtimeReadyEnvelope(s.identity, 1, 0), nil
		}
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *blockedSendRejectedReceiveStream) Send(*workercontrolv1.BackendEnvelope) error {
	s.sendStartOnce.Do(func() { close(s.sendStarted) })
	<-s.ctx.Done()
	s.sendDoneOnce.Do(func() { close(s.sendReturned) })
	return s.ctx.Err()
}

func newObservedDoneContext(timeout time.Duration) (*observedDoneContext, <-chan struct{}, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	observed := make(chan struct{})
	return &observedDoneContext{Context: ctx, observed: observed}, observed, cancel
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func startBufconnServer(t *testing.T, registry *Registry, handler EventHandler) (workercontrolv1.WorkerControlClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	workercontrolv1.RegisterWorkerControlServer(server, NewServer(registry, handler))
	go func() {
		if err := server.Serve(listener); err != nil {
			return
		}
	}()
	dialer := func(context.Context, string) (net.Conn, error) { return listener.Dial() }
	connection, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		server.Stop()
		listener.Close()
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	return workercontrolv1.NewWorkerControlClient(connection), func() {
		connection.Close()
		server.Stop()
		listener.Close()
	}
}

func authenticatedStream(t *testing.T, client workercontrolv1.WorkerControlClient, token string) workercontrolv1.WorkerControl_ConnectClient {
	t.Helper()
	return authenticatedStreamContext(t, client, context.Background(), token)
}

func authenticatedStreamContext(t *testing.T, client workercontrolv1.WorkerControlClient, ctx context.Context, token string) workercontrolv1.WorkerControl_ConnectClient {
	t.Helper()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return stream
}

func backendCommand(turnID, prompt string) *workercontrolv1.BackendEnvelope {
	return &workercontrolv1.BackendEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			Identity: &workercontrolv1.Identity{AgentTurnId: turnID},
		},
		Payload: &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: prompt},
		},
	}
}

func workerEnvelope(identity Identity, turnID string, sequence, ack uint64, payload any) *workercontrolv1.WorkerEnvelope {
	envelope := &workercontrolv1.WorkerEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      "worker-message",
			StreamSequence: sequence,
			AckSequence:    ack,
			SentAt:         timestamppb.Now(),
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   identity.WorkerSessionID,
				WorkerRunId:       identity.WorkerRunID,
				RuntimeInstanceId: identity.RuntimeInstanceID,
				RuntimeGeneration: identity.RuntimeGeneration,
				AgentTurnId:       turnID,
			},
		},
	}
	switch value := payload.(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeDiagnostic:
		envelope.Payload = value
	case *workercontrolv1.WorkerEnvelope_AcpUpdate:
		envelope.Payload = value
	case *workercontrolv1.WorkerEnvelope_AgentSessionReady:
		envelope.Payload = value
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		envelope.Payload = value
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		envelope.Payload = value
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		envelope.Payload = value
	default:
		panic("unsupported test worker payload")
	}
	return envelope
}

func assertEnvelopeCursor(t *testing.T, meta *workercontrolv1.EnvelopeMeta, sequence, ack uint64) {
	t.Helper()
	if meta.GetMessageId() == "" || meta.GetSentAt() == nil {
		t.Fatalf("backend meta = %+v, want assigned message ID and timestamp", meta)
	}
	if meta.GetStreamSequence() != sequence || meta.GetAckSequence() != ack {
		t.Fatalf("backend cursor = sequence %d ack %d, want sequence %d ack %d", meta.GetStreamSequence(), meta.GetAckSequence(), sequence, ack)
	}
}

func testIdentity() Identity {
	return Identity{
		WorkerSessionID:   "session-1",
		WorkerRunID:       "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeGeneration: 1,
	}
}

func runtimeReadyEnvelope(identity Identity, sequence, ack uint64) *workercontrolv1.WorkerEnvelope {
	return &workercontrolv1.WorkerEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      "message-ready",
			StreamSequence: sequence,
			AckSequence:    ack,
			SentAt:         timestamppb.Now(),
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   identity.WorkerSessionID,
				WorkerRunId:       identity.WorkerRunID,
				RuntimeInstanceId: identity.RuntimeInstanceID,
				RuntimeGeneration: identity.RuntimeGeneration,
			},
		},
		Payload: &workercontrolv1.WorkerEnvelope_RuntimeReady{
			RuntimeReady: &workercontrolv1.RuntimeReady{ShimVersion: "test"},
		},
	}
}

type recordingEventHandler struct {
	mu     sync.Mutex
	events []*workercontrolv1.WorkerEnvelope
}

type blockingMutationHandler struct {
	started     chan struct{}
	releaseGate chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	handled     int
}

type blockingDelegatingHandler struct {
	next        EventHandler
	started     chan struct{}
	releaseGate chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	terminals   int
}

func newBlockingDelegatingHandler(next EventHandler) *blockingDelegatingHandler {
	return &blockingDelegatingHandler{
		next:        next,
		started:     make(chan struct{}),
		releaseGate: make(chan struct{}),
	}
}

func (h *blockingDelegatingHandler) HandleWorkerEvent(
	ctx context.Context,
	identity Identity,
	event *workercontrolv1.WorkerEnvelope,
) (EventDisposition, error) {
	if event.GetTurnCompleted() != nil {
		h.mu.Lock()
		h.terminals++
		h.mu.Unlock()
		h.startOnce.Do(func() { close(h.started) })
		<-h.releaseGate
	}
	return h.next.HandleWorkerEvent(ctx, identity, event)
}

func (h *blockingDelegatingHandler) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-h.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for admitted terminal mutation")
	}
}

func (h *blockingDelegatingHandler) release() {
	h.releaseOnce.Do(func() { close(h.releaseGate) })
}

func (h *blockingDelegatingHandler) terminalCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminals
}

func newBlockingMutationHandler() *blockingMutationHandler {
	return &blockingMutationHandler{
		started:     make(chan struct{}),
		releaseGate: make(chan struct{}),
	}
}

func (h *blockingMutationHandler) HandleWorkerEvent(_ context.Context, _ Identity, _ *workercontrolv1.WorkerEnvelope) (EventDisposition, error) {
	h.mu.Lock()
	h.handled++
	h.mu.Unlock()
	h.startOnce.Do(func() { close(h.started) })
	<-h.releaseGate
	return EventDispositionAuthoritative, nil
}

func (h *blockingMutationHandler) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-h.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for half-committed event mutation")
	}
}

func (h *blockingMutationHandler) release() {
	h.releaseOnce.Do(func() { close(h.releaseGate) })
}

func (h *blockingMutationHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.handled
}

func (h *recordingEventHandler) HandleWorkerEvent(_ context.Context, _ Identity, event *workercontrolv1.WorkerEnvelope) (EventDisposition, error) {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
	return EventDispositionAuthoritative, nil
}

func (h *recordingEventHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *recordingEventHandler) waitCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.count() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("persisted events = %d, want at least %d", h.count(), want)
}

type controlTestMaterializer struct{}

func (controlTestMaterializer) Materialize(_ context.Context, sessionID string) (runtime.MaterializedSession, error) {
	pair := func(name string) runtime.PathPair {
		return runtime.PathPair{
			ContainerPath: fmt.Sprintf("/switchback/sessions/%s/%s", sessionID, name),
			HostPath:      fmt.Sprintf("/host/switchback/sessions/%s/%s", sessionID, name),
		}
	}
	return runtime.MaterializedSession{
		WorkingCopy: pair("working-copy"),
		AgentState:  pair("agent-state"),
		TraceSpool:  pair("trace-spool"),
		RuntimeLogs: pair("runtime-logs"),
	}, nil
}

func (controlTestMaterializer) Rollback(context.Context, string, runtime.MaterializedSession) error {
	return nil
}

type capturingProvisioner struct {
	provisioned    chan service.RuntimeProvisionRequest
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	cleanupOnce    sync.Once
	releaseOnce    sync.Once
}

type blockingRevokeControlPlane struct {
	registry      *Registry
	revokeStarted chan struct{}
	revokeRelease chan struct{}
	startOnce     sync.Once
	releaseOnce   sync.Once
}

func newBlockingRevokeControlPlane(registry *Registry) *blockingRevokeControlPlane {
	return &blockingRevokeControlPlane{
		registry:      registry,
		revokeStarted: make(chan struct{}),
		revokeRelease: make(chan struct{}),
	}
}

func (p *blockingRevokeControlPlane) RegisterCredential(credential Credential) error {
	return p.registry.RegisterCredential(credential)
}

func (p *blockingRevokeControlPlane) Revoke(identity Identity) {
	p.startOnce.Do(func() { close(p.revokeStarted) })
	<-p.revokeRelease
	p.registry.Revoke(identity)
}

func (p *blockingRevokeControlPlane) WaitReady(ctx context.Context, identity Identity) error {
	return p.registry.WaitReady(ctx, identity)
}

func (p *blockingRevokeControlPlane) Send(ctx context.Context, identity Identity, command *workercontrolv1.BackendEnvelope) error {
	return p.registry.Send(ctx, identity, command)
}

func (p *blockingRevokeControlPlane) waitRevoke(t *testing.T) {
	t.Helper()
	select {
	case <-p.revokeStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cleanup revoke")
	}
}

func (p *blockingRevokeControlPlane) releaseRevoke() {
	p.releaseOnce.Do(func() { close(p.revokeRelease) })
}

func newCapturingProvisioner() *capturingProvisioner {
	return &capturingProvisioner{
		provisioned:    make(chan service.RuntimeProvisionRequest, 1),
		cleanupStarted: make(chan struct{}),
	}
}

func (p *capturingProvisioner) Provision(_ context.Context, request service.RuntimeProvisionRequest) error {
	p.provisioned <- request
	return nil
}

func (p *capturingProvisioner) Cleanup(ctx context.Context, _ Identity) error {
	p.cleanupOnce.Do(func() { close(p.cleanupStarted) })
	if p.cleanupRelease != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.cleanupRelease:
		}
	}
	return nil
}

func (p *capturingProvisioner) blockCleanup() {
	p.cleanupRelease = make(chan struct{})
}

func (p *capturingProvisioner) releaseCleanup() {
	p.releaseOnce.Do(func() {
		if p.cleanupRelease != nil {
			close(p.cleanupRelease)
		}
	})
}

func (p *capturingProvisioner) waitCleanup(t *testing.T) {
	t.Helper()
	select {
	case <-p.cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime cleanup")
	}
}

func (p *capturingProvisioner) waitProvision(t *testing.T) service.RuntimeProvisionRequest {
	t.Helper()
	select {
	case request := <-p.provisioned:
		return request
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime provisioning")
		return service.RuntimeProvisionRequest{}
	}
}

func uploadTurnEvidence(
	t *testing.T,
	coordinator *service.Coordinator,
	provisioning service.RuntimeProvisionRequest,
	turnID string,
) []*workercontrolv1.EvidencePointer {
	return uploadEvidenceKinds(t, coordinator, provisioning, turnID, []objectstore.EvidenceKind{
		objectstore.EvidenceRawACP,
		objectstore.EvidenceTrace,
		objectstore.EvidenceReport,
		objectstore.EvidencePatch,
	})
}

func uploadEvidenceKinds(
	t *testing.T,
	coordinator *service.Coordinator,
	provisioning service.RuntimeProvisionRequest,
	turnID string,
	kinds []objectstore.EvidenceKind,
) []*workercontrolv1.EvidencePointer {
	t.Helper()
	authorization, err := coordinator.AuthorizeEvidenceUpload(
		provisioning.Identity.WorkerSessionID,
		provisioning.Identity.WorkerRunID,
		turnID,
		provisioning.WorkerToken,
	)
	if err != nil {
		t.Fatalf("AuthorizeEvidenceUpload() error = %v", err)
	}
	pointers := make([]*workercontrolv1.EvidencePointer, 0, len(kinds))
	for _, kind := range kinds {
		stored, err := coordinator.StoreEvidence(context.Background(), authorization, kind, []byte("evidence "+kind))
		if err != nil {
			t.Fatalf("StoreEvidence(%q) error = %v", kind, err)
		}
		pointers = append(pointers, &workercontrolv1.EvidencePointer{
			Kind:       string(kind),
			ObjectPath: stored.Path,
			Sha256:     stored.SHA256,
			SizeBytes:  stored.Bytes,
		})
	}
	return pointers
}

type runningTurnHarness struct {
	coordinator  *service.Coordinator
	provisioner  *capturingProvisioner
	registry     *Registry
	created      service.CreatedRun
	provisioning service.RuntimeProvisionRequest
	identity     Identity
	stream       workercontrolv1.WorkerControl_ConnectClient
}

func startRunningTurn(t *testing.T) runningTurnHarness {
	return startRunningTurnWithHandler(t, nil)
}

func startRunningTurnWithHandler(t *testing.T, wrap func(EventHandler) EventHandler) runningTurnHarness {
	t.Helper()
	registry := NewRegistry()
	provisioner := newCapturingProvisioner()
	coordinator := service.NewCoordinator(service.Options{
		RootContext:  context.Background(),
		Materializer: controlTestMaterializer{},
		ObjectStore: objectstore.LocalFileObjectStore{
			Root: t.TempDir(),
		},
		Scheduler: newControlRuntimeScheduler(t, registry, provisioner),
	})
	created, err := coordinator.CreateRun(context.Background(), service.CreateRunInput{
		Goal:               "fix the fixture",
		AcceptanceCriteria: []string{"tests pass"},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	provisioning := provisioner.waitProvision(t)
	var handler EventHandler = coordinator
	if wrap != nil {
		handler = wrap(handler)
	}
	client, cleanup := startBufconnServer(t, registry, handler)
	t.Cleanup(cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	stream := authenticatedStreamContext(t, client, ctx, provisioning.WorkerToken)
	identity := provisioning.Identity
	if err := stream.Send(runtimeReadyEnvelope(identity, 1, 0)); err != nil {
		t.Fatalf("RuntimeReady Send() error = %v", err)
	}
	if command, err := stream.Recv(); err != nil || command.GetInitializeSession() == nil {
		t.Fatalf("InitializeSession Recv() command = %+v error = %v", command, err)
	}
	if err := stream.Send(workerEnvelope(identity, "", 2, 1, &workercontrolv1.WorkerEnvelope_AgentSessionReady{
		AgentSessionReady: &workercontrolv1.AgentSessionReady{AgentSessionId: "agent-session-1", ProtocolVersion: "1"},
	})); err != nil {
		t.Fatalf("AgentSessionReady Send() error = %v", err)
	}
	if command, err := stream.Recv(); err != nil || command.GetStartAgentTurn() == nil {
		t.Fatalf("StartAgentTurn Recv() command = %+v error = %v", command, err)
	}
	if err := stream.Send(workerEnvelope(identity, created.TurnID, 3, 2, &workercontrolv1.WorkerEnvelope_TurnStarted{
		TurnStarted: &workercontrolv1.TurnStarted{},
	})); err != nil {
		t.Fatalf("TurnStarted Send() error = %v", err)
	}
	waitRunState(t, coordinator, created.WorkerRunID, "turn_running")
	return runningTurnHarness{
		coordinator:  coordinator,
		provisioner:  provisioner,
		registry:     registry,
		created:      created,
		provisioning: provisioning,
		identity:     identity,
		stream:       stream,
	}
}

func waitCredentialRevoked(t *testing.T, registry *Registry, token string) {
	t.Helper()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := registry.Authenticate(ctx); errors.Is(err, ErrUnauthenticated) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for credential revocation")
}

func waitDiagnostics(t *testing.T, coordinator *service.Coordinator, runID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		diagnostics, err := coordinator.Diagnostics(runID)
		if err != nil {
			t.Fatalf("Diagnostics() error = %v", err)
		}
		if len(diagnostics) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	diagnostics, _ := coordinator.Diagnostics(runID)
	t.Fatalf("diagnostics = %d, want %d", len(diagnostics), want)
}

func waitLatestDiagnostic(t *testing.T, coordinator *service.Coordinator, runID, detail string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		diagnostics, err := coordinator.Diagnostics(runID)
		if err != nil {
			t.Fatalf("Diagnostics() error = %v", err)
		}
		if len(diagnostics) == service.MaxRuntimeDiagnostics && diagnostics[len(diagnostics)-1].Detail == detail {
			return
		}
		time.Sleep(time.Millisecond)
	}
	diagnostics, _ := coordinator.Diagnostics(runID)
	t.Fatalf("latest diagnostic = %+v, want detail %q", diagnostics[len(diagnostics)-1], detail)
}

func waitRunState(t *testing.T, coordinator *service.Coordinator, runID, state string) service.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := coordinator.Snapshot(runID)
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		if snapshot.State == state {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	snapshot, err := coordinator.Snapshot(runID)
	if err != nil {
		t.Fatalf("Snapshot() final error = %v", err)
	}
	t.Fatalf("run state = %q, want %q", snapshot.State, state)
	return service.RunSnapshot{}
}

func newControlRuntimeScheduler(
	t *testing.T,
	controlPlane service.ControlPlane,
	provisioner service.RuntimeProvisioner,
) service.Scheduler {
	t.Helper()
	scheduler, err := service.NewRuntimeScheduler(
		controlPlane,
		provisioner,
		agentprofile.Codex,
	)
	if err != nil {
		t.Fatalf("NewRuntimeScheduler() error = %v", err)
	}
	return scheduler
}
