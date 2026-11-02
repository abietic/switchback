package control

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	"switchback.local/spikes/worker-runtime/worker/internal/acp"
	"switchback.local/spikes/worker-runtime/worker/internal/modelobs"
	"switchback.local/spikes/worker-runtime/worker/internal/upload"
)

func TestClientShutdownCancelsBeginCaptureAndJoinsTurn(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	captureStarted := make(chan struct{})
	captureStopped := make(chan struct{})
	var snapshotCalls atomic.Int32
	snapshot := func(ctx context.Context, _ string) (string, error) {
		if snapshotCalls.Add(1) != 1 {
			return "", errors.New("unexpected snapshot retry")
		}
		close(captureStarted)
		<-ctx.Done()
		close(captureStopped)
		return "", ctx.Err()
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", validInitialize(
			workingCopy,
			agentState,
			filepath.Dir(traceFile.Name()),
			"trace-1",
		))
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "cancel capture"},
		})
		<-captureStarted
		sendBackendCommand(t, stream, identity, 3, 2, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "cancel begin capture"},
		})
		_, err := stream.Recv()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	})
	defer cleanup()

	client, err := NewClient(Options{
		RPC:          rpc,
		Identity:     identity,
		WorkerToken:  "worker-token",
		ShimVersion:  "test",
		Agent:        &fakeAgent{trace: traceFile, workingCopy: workingCopy},
		AgentProfile: agentprofile.Codex,
		Capabilities: acp.CapabilitySnapshot{ProtocolVersion: sdk.ProtocolVersionNumber},
		WorkingCopy:  workingCopy,
		AgentState:   agentState,
		TraceSpool:   filepath.Dir(traceFile.Name()),
		Trace:        traceFile,
		Uploader:     uploader,
		Snapshot:     snapshot,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-captureStopped:
	default:
		t.Fatal("Run returned before the begin-capture Turn goroutine stopped")
	}
}

func TestClientShutdownCancelsEvidenceUploadAndJoinsTurn(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}

	uploadStarted := make(chan struct{})
	uploadCancelled := make(chan struct{})
	var startedOnce sync.Once
	var cancelledOnce sync.Once
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		startedOnce.Do(func() { close(uploadStarted) })
		<-request.Context().Done()
		cancelledOnce.Do(func() { close(uploadCancelled) })
		return nil, request.Context().Err()
	})}
	uploader, err := upload.NewClient("http://backend.test", "worker-token", httpClient)
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", validInitialize(
			workingCopy,
			agentState,
			filepath.Dir(traceFile.Name()),
			"trace-1",
		))
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "block evidence upload"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		<-uploadStarted
		sendBackendCommand(t, stream, identity, 3, 3, "", &workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "cancel evidence upload"},
		})
		terminal := receiveWorkerEvent(t, stream)
		assertWorkerEvent(t, terminal, 4, 3, "turn-1", "turn_failed")
		return nil
	})
	defer cleanup()

	client := newConfiguredClient(
		t,
		rpc,
		identity,
		agent,
		workingCopy,
		agentState,
		traceFile,
		uploader,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	select {
	case <-uploadCancelled:
	default:
		t.Fatal("Run returned before the evidence upload request was cancelled")
	}
}

func TestClientStreamFailureCancelsActivePromptAndJoinsTurn(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &joinBlockingAgent{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
		release:   make(chan struct{}),
		done:      make(chan struct{}),
	}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	identity := testRuntimeIdentity()
	rpc, cleanup := startControlServer(t, func(stream workercontrolv1.WorkerControl_ConnectServer) error {
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 1, 0, "", "runtime_ready")
		sendBackendCommand(t, stream, identity, 1, 1, "", validInitialize(
			workingCopy,
			agentState,
			filepath.Dir(traceFile.Name()),
			"trace-1",
		))
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 2, 1, "", "agent_session_ready")
		sendBackendCommand(t, stream, identity, 2, 2, "turn-1", &workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "wait for stream failure"},
		})
		assertWorkerEvent(t, receiveWorkerEvent(t, stream), 3, 2, "turn-1", "turn_started")
		<-agent.started
		return errors.New("injected WorkerControl stream failure")
	})
	defer cleanup()

	client := newConfiguredClient(
		t,
		rpc,
		identity,
		agent,
		workingCopy,
		agentState,
		traceFile,
		uploader,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()

	select {
	case <-agent.cancelled:
	case <-ctx.Done():
		t.Fatal("stream failure did not cancel the active Prompt")
	}
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before the cancelled Prompt exited: %v", err)
	default:
	}
	close(agent.release)
	select {
	case err := <-runDone:
		if err == nil || !strings.Contains(err.Error(), "receive WorkerControl command") {
			t.Fatalf("Run() error = %v, want stream receive failure", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not join the active Prompt after stream failure")
	}
	select {
	case <-agent.done:
	default:
		t.Fatal("Run returned before the active Prompt goroutine stopped")
	}
}

func TestClientTerminalSendDeadlineCancelsStreamAndJoins(t *testing.T) {
	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	stream := newTerminalBarrierClientStream(terminalSendWaitsForContext)
	defer stream.releaseTerminalSend()
	identity := testRuntimeIdentity()
	client := newConfiguredClient(
		t,
		terminalBarrierRPC{stream: stream},
		identity,
		agent,
		workingCopy,
		agentState,
		traceFile,
		uploader,
		nil,
	)
	client.options.FinalizationTimeout = 500 * time.Millisecond

	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	guardContext, cancelGuard := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelGuard()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runContext) }()

	assertWorkerEvent(t, receiveTerminalBarrierEvent(t, guardContext, stream, runDone), 1, 0, "", "runtime_ready")
	stream.sendCommand(t, guardContext, makeBackendCommand(
		identity,
		1,
		1,
		"",
		validInitialize(workingCopy, agentState, filepath.Dir(traceFile.Name()), "trace-1"),
	))
	assertWorkerEvent(t, receiveTerminalBarrierEvent(t, guardContext, stream, runDone), 2, 1, "", "agent_session_ready")
	stream.sendCommand(t, guardContext, makeBackendCommand(
		identity,
		2,
		2,
		"turn-1",
		&workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "deadline-bound terminal send"},
		},
	))
	assertWorkerEvent(t, receiveTerminalBarrierEvent(t, guardContext, stream, runDone), 3, 2, "turn-1", "turn_started")

	select {
	case <-stream.terminalSendStarted:
	case <-guardContext.Done():
		t.Fatal("terminal Send did not start")
	}
	select {
	case <-stream.terminalSendContextDone:
	case <-guardContext.Done():
		t.Fatal("finalization deadline did not cancel the blocked terminal stream Send")
	}
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before the blocked terminal Send returned: %v", err)
	default:
	}

	stream.releaseTerminalSend()
	select {
	case err := <-runDone:
		if err == nil || !strings.Contains(err.Error(), "terminality is unknown") {
			t.Fatalf("Run() error = %v, want ambiguous terminal send failure", err)
		}
	case <-guardContext.Done():
		t.Fatal("Run did not exit after the blocked terminal Send returned")
	}
	assertChannelClosed(t, stream.terminalSendReturned, "Run returned before terminal Send stopped")
	assertChannelClosed(t, stream.recvReturned, "Run returned before the receiver stopped")
}

func TestClientShutdownPropagatesAmbiguousTerminalSend(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	stream := newTerminalBarrierClientStream(terminalSendFailsWhenReleased)
	defer stream.releaseTerminalSend()
	identity := testRuntimeIdentity()
	client := newConfiguredClient(
		t,
		terminalBarrierRPC{stream: stream},
		identity,
		agent,
		workingCopy,
		agentState,
		traceFile,
		uploader,
		nil,
	)

	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	guardContext, cancelGuard := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelGuard()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(runContext) }()

	assertWorkerEvent(t, receiveTerminalBarrierEvent(t, guardContext, stream, runDone), 1, 0, "", "runtime_ready")
	stream.sendCommand(t, guardContext, makeBackendCommand(
		identity,
		1,
		1,
		"",
		validInitialize(workingCopy, agentState, filepath.Dir(traceFile.Name()), "trace-1"),
	))
	assertWorkerEvent(t, receiveTerminalBarrierEvent(t, guardContext, stream, runDone), 2, 1, "", "agent_session_ready")
	stream.sendCommand(t, guardContext, makeBackendCommand(
		identity,
		2,
		2,
		"turn-1",
		&workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "shutdown during terminal send"},
		},
	))
	assertWorkerEvent(t, receiveTerminalBarrierEvent(t, guardContext, stream, runDone), 3, 2, "turn-1", "turn_started")
	select {
	case <-stream.terminalSendStarted:
	case <-guardContext.Done():
		t.Fatal("terminal Send did not start")
	}

	stream.sendCommand(t, guardContext, makeBackendCommand(
		identity,
		3,
		3,
		"",
		&workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "concurrent terminal ambiguity"},
		},
	))
	select {
	case <-stream.afterThirdCommandDelivered:
	case <-guardContext.Done():
		t.Fatal("shutdown command was not queued behind the blocked terminal Send")
	}
	stream.releaseTerminalSend()

	select {
	case err := <-runDone:
		if err == nil || !strings.Contains(err.Error(), "terminality is unknown") {
			t.Fatalf("Run() error = %v, want ambiguous terminal send failure", err)
		}
	case <-guardContext.Done():
		t.Fatal("Run did not exit after shutdown and ambiguous terminal Send")
	}
	assertChannelClosed(t, stream.terminalSendReturned, "Run returned before terminal Send stopped")
	assertChannelClosed(t, stream.recvReturned, "Run returned before the receiver stopped")
}

func TestClientDropsLateObservationFromTerminalTurnAfterNextTurnStarts(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	workingCopy := newGitWorkingCopy(t)
	traceFile := newTraceFile(t)
	agentState := t.TempDir()
	agent := &fakeAgent{trace: traceFile, workingCopy: workingCopy}
	uploadServer, _ := newUploadServer(t, "")
	defer uploadServer.Close()
	uploader, err := upload.NewClient(uploadServer.URL, "worker-token", uploadServer.Client())
	if err != nil {
		t.Fatalf("upload.NewClient() error = %v", err)
	}

	stream := newOrderingClientStream()
	identity := testRuntimeIdentity()
	observations := make(chan modelobs.Observation, 1)
	client := newConfiguredClient(
		t,
		orderingRPC{stream: stream},
		identity,
		agent,
		workingCopy,
		agentState,
		traceFile,
		uploader,
		observations,
	)
	stream.client = client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()

	assertWorkerEvent(t, receiveOrderingEvent(t, ctx, stream, runDone), 1, 0, "", "runtime_ready")
	stream.sendCommand(t, ctx, makeBackendCommand(
		identity,
		1,
		1,
		"",
		validInitialize(workingCopy, agentState, filepath.Dir(traceFile.Name()), "trace-1"),
	))
	assertWorkerEvent(t, receiveOrderingEvent(t, ctx, stream, runDone), 2, 1, "", "agent_session_ready")
	stream.sendCommand(t, ctx, makeBackendCommand(
		identity,
		2,
		2,
		"turn-1",
		&workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "first prompt"},
		},
	))
	assertWorkerEvent(t, receiveOrderingEvent(t, ctx, stream, runDone), 3, 2, "turn-1", "turn_started")
	assertWorkerEvent(t, receiveOrderingEvent(t, ctx, stream, runDone), 4, 2, "turn-1", "turn_completed")
	stream.sendCommand(t, ctx, makeBackendCommand(
		identity,
		3,
		4,
		"turn-2",
		&workercontrolv1.BackendEnvelope_StartAgentTurn{
			StartAgentTurn: &workercontrolv1.StartAgentTurn{Prompt: "second prompt"},
		},
	))
	assertWorkerEvent(t, receiveOrderingEvent(t, ctx, stream, runDone), 5, 3, "turn-2", "turn_started")
	observations <- modelobs.Observation{
		Model:     "codex",
		Route:     "litellm",
		StartedAt: time.Unix(10, 0).UTC(),
		EndedAt:   time.Unix(11, 0).UTC(),
		Correlation: modelobs.Correlation{
			RequestID:       "late-observation",
			TraceID:         "trace-1",
			WorkerSessionID: identity.WorkerSessionID,
			WorkerRunID:     identity.WorkerRunID,
			AgentTurnID:     "turn-1",
		},
	}
	assertWorkerEvent(t, receiveOrderingEvent(t, ctx, stream, runDone), 6, 3, "turn-2", "turn_completed")
	stream.sendCommand(t, ctx, makeBackendCommand(
		identity,
		4,
		6,
		"",
		&workercontrolv1.BackendEnvelope_ShutdownRuntime{
			ShutdownRuntime: &workercontrolv1.ShutdownRuntime{Reason: "two turns complete"},
		},
	))
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not finish after two sequential Turns")
	}
}

func TestReceiveCommandsStopsWhenDeliveryIsBlockedAndContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &blockedDeliveryStream{received: make(chan struct{})}
	commands := make(chan receivedCommand)
	done := make(chan struct{})
	go func() {
		defer close(done)
		receiveCommands(ctx, stream, commands)
	}()
	<-stream.received
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receiveCommands did not stop after context cancellation")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type joinBlockingAgent struct {
	started   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	done      chan struct{}
}

func (*joinBlockingAgent) NewSession(context.Context, string) (string, error) {
	return "agent-session-1", nil
}

func (a *joinBlockingAgent) Prompt(ctx context.Context, _, _ string) (acp.PromptResult, error) {
	defer close(a.done)
	close(a.started)
	<-ctx.Done()
	close(a.cancelled)
	<-a.release
	return acp.PromptResult{}, ctx.Err()
}

type blockedDeliveryStream struct {
	grpc.ClientStream
	received chan struct{}
	once     sync.Once
}

func (s *blockedDeliveryStream) Recv() (*workercontrolv1.BackendEnvelope, error) {
	s.once.Do(func() { close(s.received) })
	return &workercontrolv1.BackendEnvelope{}, nil
}

func (*blockedDeliveryStream) Send(*workercontrolv1.WorkerEnvelope) error {
	return nil
}

type terminalSendBehavior uint8

const (
	terminalSendWaitsForContext terminalSendBehavior = iota
	terminalSendFailsWhenReleased
)

type terminalBarrierRPC struct {
	stream *terminalBarrierClientStream
}

func (r terminalBarrierRPC) Connect(
	ctx context.Context,
	_ ...grpc.CallOption,
) (grpc.BidiStreamingClient[workercontrolv1.WorkerEnvelope, workercontrolv1.BackendEnvelope], error) {
	r.stream.ctx = ctx
	return r.stream, nil
}

type terminalBarrierClientStream struct {
	grpc.ClientStream
	ctx                        context.Context
	behavior                   terminalSendBehavior
	inbound                    chan *workercontrolv1.BackendEnvelope
	outbound                   chan *workercontrolv1.WorkerEnvelope
	terminalSendStarted        chan struct{}
	terminalSendContextDone    chan struct{}
	terminalSendRelease        chan struct{}
	terminalSendReturned       chan struct{}
	recvReturned               chan struct{}
	afterThirdCommandDelivered chan struct{}
	terminalStartOnce          sync.Once
	terminalContextOnce        sync.Once
	terminalReleaseOnce        sync.Once
	terminalReturnOnce         sync.Once
	recvReturnOnce             sync.Once
	thirdCommandOnce           sync.Once
	recvCalls                  atomic.Int32
}

func newTerminalBarrierClientStream(behavior terminalSendBehavior) *terminalBarrierClientStream {
	return &terminalBarrierClientStream{
		behavior:                   behavior,
		inbound:                    make(chan *workercontrolv1.BackendEnvelope, 4),
		outbound:                   make(chan *workercontrolv1.WorkerEnvelope, 4),
		terminalSendStarted:        make(chan struct{}),
		terminalSendContextDone:    make(chan struct{}),
		terminalSendRelease:        make(chan struct{}),
		terminalSendReturned:       make(chan struct{}),
		recvReturned:               make(chan struct{}),
		afterThirdCommandDelivered: make(chan struct{}),
	}
}

func (s *terminalBarrierClientStream) Send(event *workercontrolv1.WorkerEnvelope) error {
	if event.GetTurnCompleted() == nil && event.GetTurnFailed() == nil {
		select {
		case s.outbound <- event:
			return nil
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}

	s.terminalStartOnce.Do(func() { close(s.terminalSendStarted) })
	switch s.behavior {
	case terminalSendWaitsForContext:
		<-s.ctx.Done()
		s.terminalContextOnce.Do(func() { close(s.terminalSendContextDone) })
		<-s.terminalSendRelease
		s.terminalReturnOnce.Do(func() { close(s.terminalSendReturned) })
		return s.ctx.Err()
	case terminalSendFailsWhenReleased:
		select {
		case <-s.terminalSendRelease:
			s.terminalReturnOnce.Do(func() { close(s.terminalSendReturned) })
			return errors.New("injected ambiguous terminal send failure")
		case <-s.ctx.Done():
			s.terminalReturnOnce.Do(func() { close(s.terminalSendReturned) })
			return s.ctx.Err()
		}
	default:
		return errors.New("unsupported terminal send behavior")
	}
}

func (s *terminalBarrierClientStream) Recv() (*workercontrolv1.BackendEnvelope, error) {
	if s.recvCalls.Add(1) == 4 {
		s.thirdCommandOnce.Do(func() { close(s.afterThirdCommandDelivered) })
	}
	select {
	case command := <-s.inbound:
		return command, nil
	case <-s.ctx.Done():
		s.recvReturnOnce.Do(func() { close(s.recvReturned) })
		return nil, s.ctx.Err()
	}
}

func (*terminalBarrierClientStream) CloseSend() error {
	return nil
}

func (s *terminalBarrierClientStream) releaseTerminalSend() {
	s.terminalReleaseOnce.Do(func() { close(s.terminalSendRelease) })
}

func (s *terminalBarrierClientStream) sendCommand(
	t *testing.T,
	ctx context.Context,
	command *workercontrolv1.BackendEnvelope,
) {
	t.Helper()
	select {
	case s.inbound <- command:
	case <-ctx.Done():
		t.Fatal("timed out sending Backend command")
	}
}

func receiveTerminalBarrierEvent(
	t *testing.T,
	ctx context.Context,
	stream *terminalBarrierClientStream,
	runDone <-chan error,
) *workercontrolv1.WorkerEnvelope {
	t.Helper()
	select {
	case event := <-stream.outbound:
		return event
	case err := <-runDone:
		t.Fatalf("Run returned before the expected Worker event: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for Worker event")
	}
	return nil
}

func assertChannelClosed(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-channel:
	default:
		t.Fatal(message)
	}
}

type orderingRPC struct {
	stream *orderingClientStream
}

func (r orderingRPC) Connect(
	ctx context.Context,
	_ ...grpc.CallOption,
) (grpc.BidiStreamingClient[workercontrolv1.WorkerEnvelope, workercontrolv1.BackendEnvelope], error) {
	r.stream.ctx = ctx
	return r.stream, nil
}

type orderingClientStream struct {
	grpc.ClientStream
	ctx                context.Context
	inbound            chan *workercontrolv1.BackendEnvelope
	outbound           chan *workercontrolv1.WorkerEnvelope
	recvCalls          atomic.Int32
	firstTerminal      atomic.Bool
	nextCommandWaiting chan struct{}
	nextWaitingOnce    sync.Once
	client             *Client
}

func newOrderingClientStream() *orderingClientStream {
	return &orderingClientStream{
		inbound:            make(chan *workercontrolv1.BackendEnvelope, 4),
		outbound:           make(chan *workercontrolv1.WorkerEnvelope, 8),
		nextCommandWaiting: make(chan struct{}),
	}
}

func (s *orderingClientStream) Send(event *workercontrolv1.WorkerEnvelope) error {
	select {
	case s.outbound <- event:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	if (event.GetTurnCompleted() != nil || event.GetTurnFailed() != nil) &&
		s.firstTerminal.CompareAndSwap(false, true) {
		select {
		case <-s.nextCommandWaiting:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
		for range 10_000 {
			if s.client != nil && !s.client.mu.TryLock() {
				break
			}
			if s.client != nil {
				s.client.mu.Unlock()
			}
			runtime.Gosched()
		}
	}
	return nil
}

func (s *orderingClientStream) Recv() (*workercontrolv1.BackendEnvelope, error) {
	if s.recvCalls.Add(1) == 4 {
		s.nextWaitingOnce.Do(func() { close(s.nextCommandWaiting) })
	}
	select {
	case command := <-s.inbound:
		return command, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (*orderingClientStream) CloseSend() error {
	return nil
}

func (s *orderingClientStream) sendCommand(
	t *testing.T,
	ctx context.Context,
	command *workercontrolv1.BackendEnvelope,
) {
	t.Helper()
	select {
	case s.inbound <- command:
	case <-ctx.Done():
		t.Fatal("timed out sending Backend command")
	}
}

func receiveOrderingEvent(
	t *testing.T,
	ctx context.Context,
	stream *orderingClientStream,
	runDone <-chan error,
) *workercontrolv1.WorkerEnvelope {
	t.Helper()
	select {
	case event := <-stream.outbound:
		return event
	case err := <-runDone:
		t.Fatalf("Run returned before the expected Worker event: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for Worker event")
	}
	return nil
}

func makeBackendCommand(
	identity RuntimeIdentity,
	sequence, ack uint64,
	turnID string,
	payload any,
) *workercontrolv1.BackendEnvelope {
	command := &workercontrolv1.BackendEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      "backend-ordering",
			StreamSequence: sequence,
			AckSequence:    ack,
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   identity.WorkerSessionID,
				WorkerRunId:       identity.WorkerRunID,
				RuntimeInstanceId: identity.RuntimeInstanceID,
				RuntimeGeneration: identity.RuntimeGeneration,
				AgentTurnId:       turnID,
			},
			SentAt: timestamppb.New(time.Unix(int64(sequence), 0).UTC()),
		},
	}
	switch typed := payload.(type) {
	case *workercontrolv1.BackendEnvelope_InitializeSession:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_StartAgentTurn:
		command.Payload = typed
	case *workercontrolv1.BackendEnvelope_ShutdownRuntime:
		command.Payload = typed
	default:
		panic("unsupported ordering test payload")
	}
	return command
}
