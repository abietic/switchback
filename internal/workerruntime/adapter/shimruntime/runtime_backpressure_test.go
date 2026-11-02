package shimruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/modelobs"
	"github.com/abietic/switchback/internal/workerruntime/application"
	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The only fakes here are the two external peers. Frames are consumed directly
// by SendFrame; there is no small fake output channel to manufacture saturation.
func TestReconnectableRuntimeRealACPControlBackpressure(t *testing.T) {
	for _, count := range []int{256, 2048} {
		t.Run(strconv.Itoa(count), func(t *testing.T) { testRealACPControlBurst(t, count, "") })
	}
	for _, mode := range []string{"interrupt", "context_cancel", "close", "terminal_replay", "model_prefix_and_late_supplemental"} {
		t.Run(mode, func(t *testing.T) { testRealACPControlBurst(t, 2048, mode) })
	}
}

func testRealACPControlBurst(t *testing.T, count int, mode string) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	agent, err := acpclient.New(acpclient.Options{
		Command: func() *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBackpressureACPProcess$")
			cmd.Env = append(os.Environ(), "SWITCHBACK_BACKPRESSURE_PEER=1", "SWITCHBACK_BACKPRESSURE_COUNT="+strconv.Itoa(count), "SWITCHBACK_BACKPRESSURE_ROOT="+root)
			if mode == "model_prefix_and_late_supplemental" {
				cmd.Env = append(cmd.Env, "SWITCHBACK_BACKPRESSURE_WAIT_CANCEL=1")
			}
			return cmd
		},
		ProcessName: "backpressure-acp", WorkspaceRoot: root, Trace: io.Discard,
		Authentication: acpclient.Authentication{MethodID: "fixture-auth", Request: func(string) (sdk.AuthenticateRequest, error) {
			return sdk.AuthenticateRequest{MethodId: "fixture-auth"}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		if err := agent.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	caps, err := agent.Initialize(ctx, application.AgentInitializeRequest{WorkerSessionID: bootstrap.WorkerSessionID, RuntimeID: bootstrap.RuntimeInstanceID, Generation: 1, ProtocolVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Authenticate(ctx, "fixture-token"); err != nil {
		t.Fatal(err)
	}
	sessionID, err := agent.OpenSession(ctx, application.AgentSessionRequest{WorkerSessionID: bootstrap.WorkerSessionID, WorkingCopyPath: root})
	if err != nil {
		t.Fatal(err)
	}
	start := testStartCommand(bootstrap, sessionID, json.RawMessage(`{"prompt":"burst"}`))
	peer := &backpressureControlPeer{start: start.Envelope, changed: make(chan struct{}), workerDigest: contractsv1.RuntimeControlCursorGenesisDigest, backendDigest: contractsv1.RuntimeControlCursorGenesisDigest, committedDigest: contractsv1.RuntimeControlCursorGenesisDigest, workerFrames: make(map[uint64]contractsv1.RuntimeControlEnvelope), backendFrames: make(map[uint64]contractsv1.RuntimeControlEnvelope), withholdTerminal: mode == "terminal_replay"}
	client, err := controlgrpc.NewClient(peer, controlgrpc.Credential{
		Identity: controlgrpc.RuntimeIdentity{WorkerSessionID: bootstrap.WorkerSessionID, RuntimeInstanceID: bootstrap.RuntimeInstanceID, RuntimeGeneration: 1, ChannelEpoch: bootstrap.ChannelEpoch}, Token: bootstrap.WorkerControlToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	finished := make(chan struct{})
	var observations chan modelobs.Observation
	if mode == "model_prefix_and_late_supplemental" {
		observations = make(chan modelobs.Observation, 128)
		peer.observations = observations
		peer.modelGateAt = count
	}
	go func() {
		defer close(finished)
		result <- serveReconnectableRuntime(ctx, client, nil, agent, bootstrap, sessionID, caps, modelobs.NewRequestBinder(), observations, nil, runtimeLoopOptions{heartbeatInterval: time.Hour, drainTimeout: 50 * time.Millisecond})
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("runtime failed to stop")
		}
	}()
	for {
		peer.mu.Lock()
		count, changed := len(peer.updates), peer.changed
		peer.mu.Unlock()
		if count >= 58 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("burst must wait for ACK instead of terminating: %v", err)
		case <-changed:
		case <-ctx.Done():
			t.Fatal("burst did not reach real control saturation")
		}
	}
	select {
	case err := <-result:
		t.Fatalf("burst must wait for ACK instead of terminating: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if mode == "interrupt" {
		peer.interrupt(t)
		waitPeerEvidence(t, ctx, root, "session/cancel")
		peer.mu.Lock()
		if peer.ackEnabled {
			t.Fatal("Cancel was only observed after ACK recovery")
		}
		peer.mu.Unlock()
	}
	if mode == "close" || mode == "context_cancel" {
		if mode == "context_cancel" {
			cancel()
			// Prove the Runtime's own context wakes the pending publication.
			// Close remains deferred cleanup and cannot make this assertion pass.
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("ctx alone did not wake pending control output")
			}
			return
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		if err := agent.Close(closeCtx); err != nil {
			t.Fatalf("Close blocked behind saturated stdout: %v", err)
		}
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("ctx did not wake pending control output")
		}
		return
	}
	peer.releaseACKs(t)
	if observations != nil {
		// The external control peer freezes ACKs again at the last ACP chunk,
		// after accepting a full finite model prefix but before terminal send.
		for {
			peer.mu.Lock()
			pending, changed := peer.modelGated && len(peer.models) >= 58, peer.changed
			peer.mu.Unlock()
			if pending {
				break
			}
			select {
			case <-changed:
			case err := <-result:
				t.Fatalf("runtime ended during model prefix: %v", err)
			case <-ctx.Done():
				t.Fatal("model prefix did not reach ACK backpressure")
			}
		}
		peer.interrupt(t)
		waitPeerEvidence(t, ctx, root, "session/cancel")
		peer.mu.Lock()
		if peer.ackEnabled || peer.terminal != nil {
			t.Fatal("model-prefix interrupt waited for ACK/terminal")
		}
		peer.mu.Unlock()
		peer.releaseACKs(t)
	}
	for {
		peer.mu.Lock()
		terminal, changed := peer.terminal, peer.changed
		peer.mu.Unlock()
		if terminal != nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("runtime ended before terminal: %v", err)
		case <-changed:
		case <-ctx.Done():
			t.Fatal("terminal did not arrive after ACK release")
		}
	}
	if observations != nil {
		// Late model work carries its immutable original scope, even after the
		// active Turn has gone. Keep the channel open: silence is not closure.
		select {
		case observations <- backpressureObservation(peer.start, "late-original-scope"):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		for {
			peer.mu.Lock()
			late, changed := len(peer.models) == 129, peer.changed
			peer.mu.Unlock()
			if late {
				break
			}
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("late supplemental observation was swallowed")
			}
		}
	}
	if mode == "terminal_replay" {
		peer.mu.Lock()
		stream := peer.stream
		peer.mu.Unlock()
		select {
		case stream.incoming <- backpressureIncoming{err: status.Error(codes.Unavailable, "terminal ACK lost")}:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		for {
			peer.mu.Lock()
			resumed, changed := peer.resumed, peer.changed
			peer.mu.Unlock()
			if resumed {
				break
			}
			select {
			case <-changed:
			case err := <-result:
				t.Fatalf("terminal resume failed: %v", err)
			case <-ctx.Done():
				t.Fatal("terminal resume timed out")
			}
		}
		if err := client.Drain(ctx); err != nil {
			t.Fatalf("replayed terminal did not cross ResumeComplete ACK barrier: %v", err)
		}
	}
	evidence, err := os.ReadFile(filepath.Join(root, "peer-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(evidence), "session/prompt\n") != 1 {
		t.Fatalf("Prompt replayed: %s", evidence)
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if len(peer.updates) != count {
		t.Fatalf("accepted updates = %d, want %d", len(peer.updates), count)
	}
	for i, envelope := range peer.updates {
		var payload struct {
			Update struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Update.Content.Text != strconv.Itoa(i) {
			t.Fatalf("ordinal %d arrived as %q", i, payload.Update.Content.Text)
		}
	}
	stopReason := "end_turn"
	if mode == "interrupt" || mode == "model_prefix_and_late_supplemental" {
		stopReason = "cancelled"
	}
	if string(peer.terminal.Payload) != fmt.Sprintf(`{"update":{"stop_reason":"%s"},"update_kind":"terminal"}`, stopReason) {
		t.Fatalf("terminal = %s", peer.terminal.Payload)
	}
	if mode == "terminal_replay" {
		digest, err := contractsv1.RuntimeControlEnvelopeDigest(*peer.terminal)
		if err != nil {
			t.Fatal(err)
		}
		if peer.resumeHello == nil || peer.resumeHello.ActiveTurn == nil || peer.resumeHello.ActiveTurn.TerminalEnvelopeDigest != digest {
			t.Fatalf("Hello did not retain exact terminal proof: %+v", peer.resumeHello)
		}
		if peer.terminalReplays != 1 || peer.terminalCount != 1 {
			t.Fatalf("terminal originals/replays = %d/%d, want 1/1", peer.terminalCount, peer.terminalReplays)
		}
	}
	if observations != nil {
		for i, envelope := range peer.models {
			if envelope.AgentTurnID != peer.start.AgentTurnID || envelope.WorkerRunID != peer.start.WorkerRunID || envelope.WorkerSessionID != peer.start.WorkerSessionID {
				t.Fatalf("model %d lost original scope: %+v", i, envelope)
			}
			if i < 128 && envelope.Sequence >= peer.terminal.Sequence {
				t.Fatalf("accepted prefix %d overtaken by terminal", i)
			}
			if i == 128 && envelope.Sequence <= peer.terminal.Sequence {
				t.Fatal("late supplemental preceded terminal")
			}
			want := fmt.Sprintf("model-observation-prefix-%03d", i)
			if i == 128 {
				want = "model-observation-late-original-scope"
			}
			if !strings.Contains(envelope.MessageID, want) {
				t.Fatalf("model FIFO %d = %s", i, envelope.MessageID)
			}
		}
	}
}

func backpressureObservation(start contractsv1.RuntimeControlEnvelope, id string) modelobs.Observation {
	return modelobs.Observation{Model: "fixture", Route: "fixture", Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), Correlation: modelobs.Correlation{RequestID: id, WorkerSessionID: string(start.WorkerSessionID), WorkerRunID: string(start.WorkerRunID), AgentTurnID: string(start.AgentTurnID)}}
}

func waitPeerEvidence(t *testing.T, ctx context.Context, root, event string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		data, _ := os.ReadFile(filepath.Join(root, "peer-evidence"))
		if strings.Contains(string(data), event+"\n") {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("peer did not observe %s", event)
		}
	}
}

type backpressureIncoming struct {
	frame *contractsv1.RuntimeControlFrame
	err   error
}
type backpressureControlPeer struct {
	mu                sync.Mutex
	start             contractsv1.RuntimeControlEnvelope
	stream            *backpressureControlStream
	workerSequence    uint64
	workerDigest      string
	backendSequence   uint64
	backendDigest     string
	ackEnabled        bool
	updates           []contractsv1.RuntimeControlEnvelope
	terminal          *contractsv1.RuntimeControlEnvelope
	changed           chan struct{}
	committedSequence uint64
	committedDigest   string
	workerFrames      map[uint64]contractsv1.RuntimeControlEnvelope
	backendFrames     map[uint64]contractsv1.RuntimeControlEnvelope
	attempts          int
	withholdTerminal  bool
	terminalCount     int
	terminalReplays   int
	resumeHello       *contractsv1.RuntimeControlResumeHello
	resumed           bool
	observations      chan modelobs.Observation
	modelGateAt       int
	modelGated        bool
	models            []contractsv1.RuntimeControlEnvelope
}

func (p *backpressureControlPeer) Connect(ctx context.Context, _ ...grpc.CallOption) (controlgrpc.WorkerControlRPC_ConnectClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stream = &backpressureControlStream{ctx: ctx, peer: p, incoming: make(chan backpressureIncoming, 64)}
	p.attempts++
	return p.stream, nil
}
func bpFrame(kind string) *contractsv1.RuntimeControlFrame {
	return &contractsv1.RuntimeControlFrame{SchemaVersion: contractsv1.RuntimeControlFrameSchemaVersion, FrameType: kind}
}
func (p *backpressureControlPeer) changedLocked() { close(p.changed); p.changed = make(chan struct{}) }
func (p *backpressureControlPeer) releaseACKs(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ackEnabled = true
	if err := p.ackLocked(); err != nil {
		t.Fatal(err)
	}
}
func (p *backpressureControlPeer) ackLocked() error {
	p.backendSequence++
	ack := p.start
	ack.Sequence = p.backendSequence
	ack.Kind, ack.MessageType = "ack", "runtime.ack"
	ack.MessageID = fmt.Sprintf("backend-ack-%d", ack.Sequence)
	ack.IdempotencyKey = ack.MessageID
	ack.AckSequence = p.workerSequence
	ack.Payload = json.RawMessage(`{}`)
	digest, err := contractsv1.AdvanceRuntimeControlCursor(p.backendDigest, contractsv1.RuntimeControlDirectionBackendToRuntime, ack)
	if err != nil {
		return err
	}
	p.backendDigest = digest
	p.backendFrames[ack.Sequence] = ack
	p.committedSequence, p.committedDigest = p.workerSequence, p.workerDigest
	f := bpFrame(contractsv1.RuntimeControlFrameTypeEnvelope)
	f.Envelope = &ack
	return p.stream.deliver(f)
}

func (p *backpressureControlPeer) interrupt(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	command := p.start
	p.backendSequence++
	command.Sequence = p.backendSequence
	command.AckSequence = p.committedSequence
	command.MessageID = "interrupt-1"
	command.IdempotencyKey = command.MessageID
	command.MessageType = "agent_turn.interrupt"
	command.Payload = json.RawMessage(`{}`)
	digest, err := contractsv1.AdvanceRuntimeControlCursor(p.backendDigest, contractsv1.RuntimeControlDirectionBackendToRuntime, command)
	if err != nil {
		t.Fatal(err)
	}
	p.backendDigest = digest
	p.backendFrames[command.Sequence] = command
	f := bpFrame(contractsv1.RuntimeControlFrameTypeEnvelope)
	f.Envelope = &command
	if err := p.stream.deliver(f); err != nil {
		t.Fatal(err)
	}
}

type backpressureControlStream struct {
	ctx      context.Context
	peer     *backpressureControlPeer
	incoming chan backpressureIncoming
	ready    bool
}

func (s *backpressureControlStream) deliver(f *contractsv1.RuntimeControlFrame) error {
	select {
	case s.incoming <- backpressureIncoming{frame: f}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *backpressureControlStream) SendFrame(f *contractsv1.RuntimeControlFrame) error {
	p := s.peer
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case f.ResumeHello != nil:
		hello := f.ResumeHello
		disposition := contractsv1.RuntimeControlResumeNoActiveTurn
		if p.attempts > 1 {
			p.resumeHello = hello
			disposition = contractsv1.RuntimeControlResumeAlreadyTerminal
		}
		ack := bpFrame(contractsv1.RuntimeControlFrameTypeResumeAck)
		ack.ResumeAck = &contractsv1.RuntimeControlResumeAck{Accepted: true, ServerConnectionID: "bp-connection", JournalRevision: 1, BackendRXCommittedWorkerSequence: p.committedSequence, BackendRXCursorDigest: p.committedDigest, BackendTXLastCommandSequence: p.backendSequence, BackendTXWorkerAckedSequence: hello.WorkerRXLastSequence, BackendTXWorkerAppliedSequence: hello.WorkerRXAppliedSequence, BackendTXCursorDigest: p.backendDigest, ReplayWorkerFromSequence: p.committedSequence + 1, ReplayBackendFromSequence: hello.WorkerRXLastSequence + 1, ActiveTurnDisposition: disposition, AuthoritativeStart: hello.ActiveTurn}
		if err := s.deliver(ack); err != nil {
			return err
		}
		for seq := hello.WorkerRXLastSequence + 1; seq <= p.backendSequence; seq++ {
			envelope := p.backendFrames[seq]
			frame := bpFrame(contractsv1.RuntimeControlFrameTypeEnvelope)
			frame.Envelope = &envelope
			if err := s.deliver(frame); err != nil {
				return err
			}
		}
		return nil
	case f.ResumeReady != nil:
		complete := bpFrame(contractsv1.RuntimeControlFrameTypeResumeComplete)
		disposition := contractsv1.RuntimeControlResumeNoActiveTurn
		if p.attempts > 1 {
			disposition = contractsv1.RuntimeControlResumeAlreadyTerminal
		}
		complete.ResumeComplete = &contractsv1.RuntimeControlResumeComplete{ServerConnectionID: "bp-connection", JournalRevision: 2, BackendRXCommittedWorkerSequence: p.workerSequence, BackendRXCursorDigest: p.workerDigest, BackendTXLastCommandSequence: p.backendSequence, BackendTXWorkerAckedSequence: p.backendSequence, BackendTXWorkerAppliedSequence: f.ResumeReady.WorkerRXAppliedSequence, BackendTXCursorDigest: p.backendDigest, ActiveTurnDisposition: disposition, AuthoritativeStart: f.ResumeReady.ActiveTurn}
		if err := s.deliver(complete); err != nil {
			return err
		}
		s.ready = true
		p.committedSequence, p.committedDigest = p.workerSequence, p.workerDigest
		if p.attempts > 1 {
			p.resumed = true
			p.changedLocked()
			return nil
		}
		p.backendSequence = 1
		p.start.AckSequence = p.workerSequence
		var err error
		p.backendDigest, err = contractsv1.AdvanceRuntimeControlCursor(p.backendDigest, contractsv1.RuntimeControlDirectionBackendToRuntime, p.start)
		if err != nil {
			return err
		}
		p.backendFrames[1] = p.start
		command := bpFrame(contractsv1.RuntimeControlFrameTypeEnvelope)
		command.Envelope = &p.start
		return s.deliver(command)
	case f.Envelope != nil:
		envelope := *f.Envelope
		if envelope.Sequence <= p.workerSequence {
			original := p.workerFrames[envelope.Sequence]
			left, _ := contractsv1.RuntimeControlEnvelopeDigest(original)
			right, _ := contractsv1.RuntimeControlEnvelopeDigest(envelope)
			if left != right || original.MessageID != envelope.MessageID {
				return fmt.Errorf("replay changed sequence %d identity/digest", envelope.Sequence)
			}
			if envelope.MessageType == "agent_turn.terminal" {
				p.terminalReplays++
			}
			p.changedLocked()
			return nil
		}
		if envelope.Sequence != p.workerSequence+1 {
			return fmt.Errorf("worker sequence = %d after %d", envelope.Sequence, p.workerSequence)
		}
		var err error
		p.workerDigest, err = contractsv1.AdvanceRuntimeControlCursor(p.workerDigest, contractsv1.RuntimeControlDirectionRuntimeToBackend, envelope)
		if err != nil {
			return err
		}
		p.workerSequence = envelope.Sequence
		p.workerFrames[envelope.Sequence] = envelope
		if envelope.MessageType == "agent.update" {
			p.updates = append(p.updates, envelope)
			if p.observations != nil && len(p.updates) == p.modelGateAt {
				for i := 0; i < cap(p.observations); i++ {
					select {
					case p.observations <- backpressureObservation(p.start, fmt.Sprintf("prefix-%03d", i)):
					case <-s.ctx.Done():
						return s.ctx.Err()
					}
				}
				p.modelGated, p.ackEnabled = true, false
			}
		}
		if envelope.MessageType == "model.observation" {
			p.models = append(p.models, envelope)
		}
		if envelope.MessageType == "agent_turn.terminal" {
			p.terminal = &envelope
			p.terminalCount++
		}
		p.changedLocked()
		if s.ready && p.ackEnabled && envelope.Kind != "ack" && !(p.withholdTerminal && envelope.MessageType == "agent_turn.terminal") {
			return p.ackLocked()
		}
	}
	return nil
}
func (s *backpressureControlStream) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	select {
	case x := <-s.incoming:
		return x.frame, x.err
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
func (s *backpressureControlStream) Send(e *contractsv1.RuntimeControlEnvelope) error {
	f := bpFrame(contractsv1.RuntimeControlFrameTypeEnvelope)
	f.Envelope = e
	return s.SendFrame(f)
}
func (s *backpressureControlStream) Recv() (*contractsv1.RuntimeControlEnvelope, error) {
	f, e := s.RecvFrame()
	if e != nil {
		return nil, e
	}
	return f.Envelope, nil
}
func (s *backpressureControlStream) Header() (metadata.MD, error) { return nil, nil }
func (s *backpressureControlStream) Trailer() metadata.MD         { return nil }
func (s *backpressureControlStream) CloseSend() error             { return nil }
func (s *backpressureControlStream) Context() context.Context     { return s.ctx }
func (s *backpressureControlStream) SendMsg(v any) error {
	return s.SendFrame(v.(*contractsv1.RuntimeControlFrame))
}
func (s *backpressureControlStream) RecvMsg(v any) error {
	f, e := s.RecvFrame()
	if e == nil {
		*v.(*contractsv1.RuntimeControlFrame) = *f
	}
	return e
}

func TestBackpressureACPProcess(t *testing.T) {
	if os.Getenv("SWITCHBACK_BACKPRESSURE_PEER") != "1" {
		return
	}
	defer os.Exit(0)
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var outputMu sync.Mutex
	write := func(v any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		if encoder.Encode(v) != nil {
			os.Exit(3)
		}
	}
	count, _ := strconv.Atoi(os.Getenv("SWITCHBACK_BACKPRESSURE_COUNT"))
	evidence, err := os.OpenFile(filepath.Join(os.Getenv("SWITCHBACK_BACKPRESSURE_ROOT"), "peer-evidence"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer evidence.Close()
	var cancelled atomic.Bool
	cancelSignal := make(chan struct{})
	var cancelOnce sync.Once
	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if decoder.Decode(&m) != nil {
			return
		}
		if _, err := evidence.WriteString(m.Method + "\n"); err != nil {
			os.Exit(2)
		}
		result := any(map[string]any{})
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{map[string]any{"id": "fixture-auth", "name": "Fixture"}}}
		case "session/new":
			result = map[string]any{"sessionId": "agent-session-1"}
		case "session/prompt":
			go func(id json.RawMessage) {
				for i := 0; i < count; i++ {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "agent-session-1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": strconv.Itoa(i)}}}})
				}
				if os.Getenv("SWITCHBACK_BACKPRESSURE_WAIT_CANCEL") == "1" {
					select {
					case <-cancelSignal:
					case <-time.After(5 * time.Second):
						os.Exit(4)
					}
				}
				stopReason := "end_turn"
				if cancelled.Load() {
					stopReason = "cancelled"
				}
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": stopReason}})
			}(m.ID)
			continue
		case "session/cancel":
			cancelled.Store(true)
			cancelOnce.Do(func() { close(cancelSignal) })
			continue
		}
		if len(m.ID) == 0 {
			continue
		}
		write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}
}
