// Package control owns the Worker side of the WorkerControl stream and Turn loop.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/coder/acp-go-sdk"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
	"switchback.local/spikes/worker-runtime/internal/agentprofile"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	"switchback.local/spikes/worker-runtime/internal/ids"
	"switchback.local/spikes/worker-runtime/internal/redact"
	"switchback.local/spikes/worker-runtime/worker/internal/acp"
	workerevidence "switchback.local/spikes/worker-runtime/worker/internal/evidence"
	"switchback.local/spikes/worker-runtime/worker/internal/gitpatch"
	"switchback.local/spikes/worker-runtime/worker/internal/modelobs"
	"switchback.local/spikes/worker-runtime/worker/internal/upload"
)

const (
	maxEnvelopeBytes                   = 256 * 1024
	defaultEvidenceFinalizationTimeout = 15 * time.Second

	failureCodePrompt       = "agent_prompt_failed"
	failureCodeCancelled    = "agent_turn_cancelled"
	failureCodeFinalization = "evidence_finalization_failed"
)

// RuntimeIdentity is the immutable server-owned identity for one live runtime.
type RuntimeIdentity struct {
	WorkerSessionID   string
	WorkerRunID       string
	RuntimeInstanceID string
	RuntimeGeneration uint32
}

// Agent is the already-started ACP lifecycle used by the command loop.
type Agent interface {
	NewSession(context.Context, string) (string, error)
	Prompt(context.Context, string, string) (acp.PromptResult, error)
}

// Options are trusted Shim dependencies and fixed mounted paths.
type Options struct {
	RPC           workercontrolv1.WorkerControlClient
	Identity      RuntimeIdentity
	WorkerToken   string
	ShimVersion   string
	Agent         Agent
	AgentProfile  agentprofile.Profile
	Capabilities  acp.CapabilitySnapshot
	WorkingCopy   string
	AgentState    string
	TraceSpool    string
	Trace         *os.File
	Uploader      *upload.Client
	Secrets       redact.Secrets
	Observations  <-chan modelobs.Observation
	RequestBinder *modelobs.RequestBinder
	Now           func() time.Time
	Snapshot      func(context.Context, string) (string, error)

	FinalizationTimeout time.Duration
}

// Client owns one authenticated stream, one ACP session, and at most one Turn.
type Client struct {
	options Options

	mu              sync.Mutex
	sender          *streamSender
	runContext      context.Context
	cancelStream    context.CancelCauseFunc
	initialized     bool
	agentSessionID  string
	traceID         string
	recvSequence    uint64
	lastBackendAck  uint64
	pendingTerminal uint64
	active          *activeTurn
	turnEvents      chan turnEvent
	terminalTurns   map[string]struct{}
	shutdownPending bool
}

type activeTurn struct {
	identity          sharedevidence.TurnIdentity
	serverTimestamp   time.Time
	prompt            string
	capture           turnCapture
	lifecycleMu       sync.Mutex
	cancel            context.CancelFunc
	aborted           bool
	terminalAttempted atomic.Bool
	terminalSent      atomic.Bool
}

type turnCapture struct {
	beforeTree string
	traceStart int64
}

type turnResult struct {
	turnID string
	fatal  error
}

type turnEvent struct {
	ctx      context.Context
	identity sharedevidence.TurnIdentity
	payload  any
	response chan error
}

type receivedCommand struct {
	command *workercontrolv1.BackendEnvelope
	err     error
}

// NewClient validates all trusted dependencies before any stream is opened.
func NewClient(options Options) (*Client, error) {
	if options.RPC == nil {
		return nil, fmt.Errorf("WorkerControl RPC client is required")
	}
	if err := validateRuntimeIdentity(options.Identity); err != nil {
		return nil, err
	}
	if !validToken(options.WorkerToken) {
		return nil, fmt.Errorf("Worker token is invalid")
	}
	if strings.TrimSpace(options.ShimVersion) == "" {
		return nil, fmt.Errorf("Shim version is required")
	}
	if options.Agent == nil {
		return nil, fmt.Errorf("ACP agent is required")
	}
	if _, err := agentprofile.Parse(string(options.AgentProfile)); err != nil {
		return nil, fmt.Errorf("Worker agent profile: %w", err)
	}
	for label, path := range map[string]string{
		"WorkingCopy": options.WorkingCopy,
		"AgentState":  options.AgentState,
		"TraceSpool":  options.TraceSpool,
	} {
		if !cleanAbsolutePath(path) {
			return nil, fmt.Errorf("%s path must be a clean absolute path", label)
		}
	}
	if options.Trace == nil {
		return nil, fmt.Errorf("ACP trace file is required")
	}
	if options.Uploader == nil {
		return nil, fmt.Errorf("evidence uploader is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Snapshot == nil {
		options.Snapshot = gitpatch.Snapshot
	}
	if options.FinalizationTimeout < 0 {
		return nil, fmt.Errorf("evidence finalization timeout must not be negative")
	}
	if options.FinalizationTimeout == 0 {
		options.FinalizationTimeout = defaultEvidenceFinalizationTimeout
	}
	if options.RequestBinder == nil {
		options.RequestBinder = modelobs.NewRequestBinder()
	}
	return &Client{options: options}, nil
}

// Run connects, announces RuntimeReady, and processes commands until shutdown.
func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("WorkerControl context is required")
	}
	c.mu.Lock()
	if c.runContext != nil {
		c.mu.Unlock()
		return fmt.Errorf("WorkerControl client can run only once")
	}
	streamContext, cancelStream := context.WithCancelCause(ctx)
	defer cancelStream(nil)
	c.runContext = streamContext
	c.cancelStream = cancelStream
	c.mu.Unlock()
	authContext := metadata.NewOutgoingContext(
		streamContext,
		metadata.Pairs("authorization", "Bearer "+c.options.WorkerToken),
	)
	stream, err := c.options.RPC.Connect(authContext)
	if err != nil {
		return fmt.Errorf("connect WorkerControl: %w", err)
	}
	c.sender = &streamSender{
		stream:   stream,
		identity: c.options.Identity,
		now:      c.options.Now,
	}
	if _, err := c.sender.send("", &workercontrolv1.WorkerEnvelope_RuntimeReady{
		RuntimeReady: &workercontrolv1.RuntimeReady{ShimVersion: c.options.ShimVersion},
	}); err != nil {
		return fmt.Errorf("send RuntimeReady: %w", err)
	}

	commands := make(chan receivedCommand, 1)
	receiveDone := make(chan struct{})
	go func() {
		defer close(receiveDone)
		receiveCommands(streamContext, stream, commands)
	}()
	turns := make(chan turnResult, 1)
	c.turnEvents = make(chan turnEvent, 1)
	var turnGroup sync.WaitGroup
	defer func() {
		cancelStream(nil)
		c.abortActive()
		_ = stream.CloseSend()
		turnGroup.Wait()
		<-receiveDone
	}()
	for {
		select {
		case <-streamContext.Done():
			c.abortActive()
			return fmt.Errorf("WorkerControl stream stopped: %w", context.Cause(streamContext))
		case received := <-commands:
			if received.err != nil {
				c.abortActive()
				if streamContext.Err() != nil {
					return fmt.Errorf(
						"WorkerControl stream stopped: %w",
						context.Cause(streamContext),
					)
				}
				return fmt.Errorf("receive WorkerControl command: %w", received.err)
			}
			if err := c.acceptCommand(received.command); err != nil {
				c.abortActive()
				return err
			}
			shutdown, err := c.handleCommand(received.command, turns, &turnGroup)
			if err != nil {
				c.abortActive()
				return err
			}
			if shutdown {
				return nil
			}
		case result := <-turns:
			if c.active != nil && c.active.identity.AgentTurnID == result.turnID {
				c.active = nil
			}
			if result.fatal != nil {
				return result.fatal
			}
			if c.shutdownPending {
				return nil
			}
		case event := <-c.turnEvents:
			event.response <- c.handleTurnEvent(event)
		case observation, open := <-c.options.Observations:
			if !open {
				c.options.Observations = nil
				continue
			}
			if err := c.forwardObservation(observation); err != nil {
				c.abortActive()
				return err
			}
		}
	}
}

func receiveCommands(
	ctx context.Context,
	stream workercontrolv1.WorkerControl_ConnectClient,
	commands chan<- receivedCommand,
) {
	for {
		command, err := stream.Recv()
		select {
		case commands <- receivedCommand{command: command, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) acceptCommand(command *workercontrolv1.BackendEnvelope) error {
	if err := validateBackendEnvelope(command, c.options.Identity); err != nil {
		return fmt.Errorf("reject Backend command: %w", err)
	}
	meta := command.GetMeta()
	if meta.GetStreamSequence() != c.recvSequence+1 {
		return fmt.Errorf(
			"reject Backend command: stream sequence = %d, want %d",
			meta.GetStreamSequence(),
			c.recvSequence+1,
		)
	}
	sent := c.sender.sentSequence()
	if meta.GetAckSequence() < c.lastBackendAck {
		return fmt.Errorf("reject Backend command: ACK sequence moved backwards")
	}
	if meta.GetAckSequence() > sent {
		return fmt.Errorf("reject Backend command: ACK sequence %d exceeds sent sequence %d", meta.GetAckSequence(), sent)
	}
	c.recvSequence = meta.GetStreamSequence()
	c.lastBackendAck = meta.GetAckSequence()
	if c.pendingTerminal != 0 && meta.GetAckSequence() >= c.pendingTerminal {
		c.pendingTerminal = 0
	}
	c.sender.acknowledge(meta.GetStreamSequence())
	return nil
}

func (c *Client) handleCommand(
	command *workercontrolv1.BackendEnvelope,
	turns chan<- turnResult,
	turnGroup *sync.WaitGroup,
) (bool, error) {
	switch payload := command.GetPayload().(type) {
	case *workercontrolv1.BackendEnvelope_InitializeSession:
		return false, c.initializeSession(command, payload.InitializeSession)
	case *workercontrolv1.BackendEnvelope_StartAgentTurn:
		if err := c.requireNoPendingTerminal(); err != nil {
			return false, err
		}
		return false, c.startTurn(command, payload.StartAgentTurn, turns, turnGroup)
	case *workercontrolv1.BackendEnvelope_CancelAgentTurn:
		return false, c.cancelTurn(command, payload.CancelAgentTurn)
	case *workercontrolv1.BackendEnvelope_ShutdownRuntime:
		active := c.active
		if active != nil {
			c.shutdownPending = true
		}
		if active != nil {
			active.cancelCurrent()
			return false, nil
		}
		if err := c.requireNoPendingTerminal(); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("reject Backend command: payload is unsupported")
	}
}

func (c *Client) initializeSession(
	command *workercontrolv1.BackendEnvelope,
	initialize *workercontrolv1.InitializeSession,
) error {
	if initialize == nil ||
		initialize.GetWorkingCopy() != c.options.WorkingCopy ||
		initialize.GetAgentState() != c.options.AgentState ||
		initialize.GetTraceSpool() != c.options.TraceSpool ||
		initialize.GetAgentProfile() != c.options.AgentProfile.String() ||
		!validIdentityPart(initialize.GetTraceId()) {
		return fmt.Errorf("reject InitializeSession: trusted paths, profile, or trace identity do not match")
	}
	if c.initialized {
		return fmt.Errorf("reject InitializeSession: ACP session already initialized")
	}

	sessionID, err := c.options.Agent.NewSession(c.runContext, c.options.WorkingCopy)
	if err != nil {
		return fmt.Errorf("initialize ACP session: %w", err)
	}
	if !validIdentityPart(sessionID) {
		return fmt.Errorf("initialize ACP session: Agent session ID is invalid")
	}
	capabilityJSON, err := json.Marshal(c.options.Capabilities)
	if err != nil {
		return fmt.Errorf("encode ACP capability snapshot: %w", err)
	}
	if _, err := c.sender.send("", &workercontrolv1.WorkerEnvelope_AgentSessionReady{
		AgentSessionReady: &workercontrolv1.AgentSessionReady{
			AgentSessionId:         sessionID,
			ProtocolVersion:        strconv.FormatUint(uint64(c.options.Capabilities.ProtocolVersion), 10),
			CapabilitySnapshotJson: capabilityJSON,
		},
	}); err != nil {
		return fmt.Errorf("send AgentSessionReady: %w", err)
	}
	c.initialized = true
	c.agentSessionID = sessionID
	c.traceID = initialize.GetTraceId()
	_ = command
	return nil
}

func (c *Client) startTurn(
	command *workercontrolv1.BackendEnvelope,
	start *workercontrolv1.StartAgentTurn,
	turns chan<- turnResult,
	turnGroup *sync.WaitGroup,
) error {
	turnID := command.GetMeta().GetIdentity().GetAgentTurnId()
	if start == nil || strings.TrimSpace(start.GetPrompt()) == "" {
		return fmt.Errorf("reject StartAgentTurn: prompt is required")
	}
	if !c.initialized || c.agentSessionID == "" || c.traceID == "" {
		return fmt.Errorf("reject StartAgentTurn: ACP session is not initialized")
	}
	if c.active != nil && c.active.terminalSent.Load() && c.pendingTerminal == 0 {
		c.active = nil
	}
	if c.active != nil {
		activeID := c.active.identity.AgentTurnID
		return fmt.Errorf("reject StartAgentTurn: AgentTurn %q is already active", activeID)
	}
	identity := sharedevidence.TurnIdentity{
		WorkerSessionID:   c.options.Identity.WorkerSessionID,
		WorkerRunID:       c.options.Identity.WorkerRunID,
		RuntimeInstanceID: c.options.Identity.RuntimeInstanceID,
		RuntimeGeneration: c.options.Identity.RuntimeGeneration,
		AgentSessionID:    c.agentSessionID,
		AgentTurnID:       turnID,
		TraceID:           c.traceID,
	}

	turnContext, cancelTurn := context.WithCancel(c.runContext)
	active := &activeTurn{
		identity:        identity,
		serverTimestamp: command.GetMeta().GetSentAt().AsTime(),
		prompt:          start.GetPrompt(),
		cancel:          cancelTurn,
	}
	if c.active != nil {
		cancelTurn()
		return fmt.Errorf("reject StartAgentTurn: another AgentTurn became active")
	}
	c.active = active
	turnGroup.Add(1)
	go func() {
		defer turnGroup.Done()
		result := c.prepareAndExecuteTurn(turnContext, active)
		select {
		case turns <- result:
		case <-c.runContext.Done():
		}
	}()
	return nil
}

func (c *Client) prepareAndExecuteTurn(turnContext context.Context, active *activeTurn) turnResult {
	capture, err := c.beginTurn(turnContext)
	if err != nil {
		if errors.Is(turnContext.Err(), context.Canceled) {
			return turnResult{turnID: active.identity.AgentTurnID}
		}
		return turnResult{
			turnID: active.identity.AgentTurnID,
			fatal:  fmt.Errorf("capture AgentTurn start: %w", err),
		}
	}
	active.capture = capture
	if err := c.requestTurnEvent(turnContext, active.identity, &workercontrolv1.WorkerEnvelope_TurnStarted{
		TurnStarted: &workercontrolv1.TurnStarted{StartedAt: timestamppb.New(c.options.Now().UTC())},
	}); err != nil {
		return turnResult{
			turnID: active.identity.AgentTurnID,
			fatal:  fmt.Errorf("send TurnStarted: %w", err),
		}
	}
	return c.executeTurn(turnContext, active)
}

func (c *Client) executeTurn(promptContext context.Context, active *activeTurn) turnResult {
	defer active.cancelCurrent()
	requestBinder := c.options.RequestBinder
	if requestBinder == nil {
		requestBinder = modelobs.NewRequestBinder()
		c.options.RequestBinder = requestBinder
	}
	modelCorrelation := modelobs.Correlation{
		TraceID:         active.identity.TraceID,
		WorkerSessionID: active.identity.WorkerSessionID,
		WorkerRunID:     active.identity.WorkerRunID,
		AgentTurnID:     active.identity.AgentTurnID,
	}
	if err := requestBinder.Activate(modelCorrelation); err != nil {
		return turnResult{
			turnID: active.identity.AgentTurnID,
			fatal:  fmt.Errorf("activate model observation correlation: %w", err),
		}
	}
	defer func() { _ = requestBinder.Deactivate(modelCorrelation) }()
	promptContext = acp.WithPromptMetadata(promptContext, acp.PromptMetadata{
		RequestID:       ids.New(),
		TraceID:         active.identity.TraceID,
		WorkerSessionID: active.identity.WorkerSessionID,
		WorkerRunID:     active.identity.WorkerRunID,
		AgentTurnID:     active.identity.AgentTurnID,
	})
	result, promptErr := c.options.Agent.Prompt(
		promptContext,
		active.identity.AgentSessionID,
		active.prompt,
	)
	failureCode := ""
	failureDetail := ""
	switch {
	case promptErr != nil && errors.Is(promptContext.Err(), context.Canceled):
		failureCode = failureCodeCancelled
		failureDetail = "AgentTurn prompt was cancelled"
	case promptErr != nil:
		failureCode = failureCodePrompt
		failureDetail = "ACP prompt failed or its terminality is unknown"
	case result.StopReason == sdk.StopReasonCancelled:
		failureCode = failureCodeCancelled
		failureDetail = "ACP prompt reported cancellation"
	}
	finalizationContext, cancelFinalization := context.WithTimeout(
		context.WithoutCancel(c.runContext),
		c.options.FinalizationTimeout,
	)
	if !active.beginFinalization(cancelFinalization) {
		return turnResult{
			turnID: active.identity.AgentTurnID,
			fatal:  fmt.Errorf("AgentTurn was aborted before evidence finalization"),
		}
	}
	defer cancelFinalization()
	if err := c.finalizeTurn(finalizationContext, active, failureCode, failureDetail); err != nil {
		terminalAttempted := active.terminalAttempted.Load()
		terminalSent := active.terminalSent.Load()
		if terminalAttempted && !terminalSent {
			return turnResult{
				turnID: active.identity.AgentTurnID,
				fatal:  err,
			}
		}
		if !terminalAttempted {
			if terminalErr := c.failActive(
				c.runContext,
				active.identity,
				failureCodeFinalization,
				"AgentTurn evidence finalization failed",
				nil,
			); terminalErr != nil {
				return turnResult{
					turnID: active.identity.AgentTurnID,
					fatal:  errors.Join(err, terminalErr),
				}
			}
		}
	}
	return turnResult{turnID: active.identity.AgentTurnID}
}

func (c *Client) beginTurn(ctx context.Context) (turnCapture, error) {
	if err := c.options.Trace.Sync(); err != nil {
		return turnCapture{}, fmt.Errorf("sync ACP trace: %w", err)
	}
	traceStart, err := c.options.Trace.Seek(0, io.SeekCurrent)
	if err != nil {
		return turnCapture{}, fmt.Errorf("record ACP trace offset: %w", err)
	}
	snapshot := c.options.Snapshot
	if snapshot == nil {
		snapshot = gitpatch.Snapshot
	}
	beforeTree, err := snapshot(ctx, c.options.WorkingCopy)
	if err != nil {
		return turnCapture{}, fmt.Errorf("snapshot WorkingCopy: %w", err)
	}
	return turnCapture{beforeTree: beforeTree, traceStart: traceStart}, nil
}

func (c *Client) finalizeTurn(
	ctx context.Context,
	active *activeTurn,
	failureCode, failureDetail string,
) error {
	afterTree, err := gitpatch.Snapshot(ctx, c.options.WorkingCopy)
	if err != nil {
		return fmt.Errorf("snapshot WorkingCopy after turn: %w", err)
	}
	patch, err := gitpatch.Diff(ctx, c.options.WorkingCopy, active.capture.beforeTree, afterTree)
	if err != nil {
		return fmt.Errorf("diff WorkingCopy turn trees: %w", err)
	}
	rawACP, err := readTurnTrace(c.options.Trace, active.capture.traceStart)
	if err != nil {
		return err
	}
	bundle, err := workerevidence.Build(workerevidence.Input{
		Identity:        active.identity,
		ServerTimestamp: active.serverTimestamp,
		WorkerTimestamp: c.options.Now().UTC(),
		BeforeTree:      active.capture.beforeTree,
		AfterTree:       afterTree,
		RawACPJSONL:     rawACP,
		Patch:           patch,
		NoFilesChanged:  len(patch) == 0,
		Secrets:         c.options.Secrets,
	})
	if err != nil {
		return fmt.Errorf("build turn evidence: %w", err)
	}
	sink := upload.TerminalSink(c)
	if failureCode != "" {
		sink = failedOutcomeSink{
			client: c,
			code:   failureCode,
			detail: failureDetail,
		}
	}
	return c.options.Uploader.Finalize(ctx, bundle, sink)
}

func readTurnTrace(traceFile *os.File, start int64) ([]byte, error) {
	if traceFile == nil || start < 0 {
		return nil, fmt.Errorf("valid ACP trace file and offset are required")
	}
	if err := traceFile.Sync(); err != nil {
		return nil, fmt.Errorf("sync ACP trace after turn: %w", err)
	}
	end, err := traceFile.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("record ACP trace offset after turn: %w", err)
	}
	if end <= start {
		return nil, fmt.Errorf("AgentTurn produced no raw ACP trace")
	}
	content := make([]byte, end-start)
	read, err := traceFile.ReadAt(content, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read AgentTurn ACP trace: %w", err)
	}
	if read != len(content) {
		return nil, fmt.Errorf("read AgentTurn ACP trace: got %d bytes, want %d", read, len(content))
	}
	return content, nil
}

// TurnCompleted implements upload.TerminalSink with active identity fencing.
func (c *Client) TurnCompleted(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	refs []sharedevidence.ObjectRef,
) error {
	return c.sendTerminal(ctx, identity, &workercontrolv1.WorkerEnvelope_TurnCompleted{
		TurnCompleted: &workercontrolv1.TurnCompleted{
			AgentSessionId: identity.AgentSessionID,
			Evidence:       evidencePointers(refs),
		},
	})
}

// TurnFailed implements upload.TerminalSink with active identity fencing.
func (c *Client) TurnFailed(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	code, detail string,
	refs []sharedevidence.ObjectRef,
) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(detail) == "" {
		return fmt.Errorf("TurnFailed code and detail are required")
	}
	return c.sendTerminal(ctx, identity, &workercontrolv1.WorkerEnvelope_TurnFailed{
		TurnFailed: &workercontrolv1.TurnFailed{
			Code:            code,
			Detail:          detail,
			PartialEvidence: evidencePointers(refs),
		},
	})
}

func (c *Client) sendTerminal(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	payload any,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.requestTurnEvent(ctx, identity, payload)
}

type failedOutcomeSink struct {
	client *Client
	code   string
	detail string
}

func (s failedOutcomeSink) TurnCompleted(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	refs []sharedevidence.ObjectRef,
) error {
	return s.client.TurnFailed(ctx, identity, s.code, s.detail, refs)
}

func (s failedOutcomeSink) TurnFailed(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	code, detail string,
	refs []sharedevidence.ObjectRef,
) error {
	return s.client.TurnFailed(ctx, identity, code, detail, refs)
}

func (c *Client) failActive(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	code, detail string,
	refs []sharedevidence.ObjectRef,
) error {
	if err := c.TurnFailed(ctx, identity, code, detail, refs); err != nil {
		return fmt.Errorf("emit TurnFailed: %w", err)
	}
	return nil
}

func (c *Client) cancelTurn(
	command *workercontrolv1.BackendEnvelope,
	cancelCommand *workercontrolv1.CancelAgentTurn,
) error {
	if cancelCommand == nil || strings.TrimSpace(cancelCommand.GetReason()) == "" {
		return fmt.Errorf("reject CancelAgentTurn: reason is required")
	}
	turnID := command.GetMeta().GetIdentity().GetAgentTurnId()
	active := c.active
	terminalAttempted := active != nil && active.terminalAttempted.Load()
	if active == nil || active.identity.AgentTurnID != turnID {
		return fmt.Errorf("reject CancelAgentTurn: AgentTurn identity does not match active turn")
	}
	if terminalAttempted {
		return nil
	}
	active.cancelCurrent()
	return nil
}

func (c *Client) forwardObservation(observation modelobs.Observation) error {
	active := c.active
	terminalAttempted := active != nil && active.terminalAttempted.Load()
	if active == nil || terminalAttempted {
		return nil
	}
	correlation := observation.Correlation
	if _, terminal := c.terminalTurns[correlation.AgentTurnID]; terminal &&
		correlation.TraceID == active.identity.TraceID &&
		correlation.WorkerSessionID == active.identity.WorkerSessionID &&
		correlation.WorkerRunID == active.identity.WorkerRunID {
		return nil
	}
	if correlation.RequestID == "" ||
		correlation.TraceID != active.identity.TraceID ||
		correlation.WorkerSessionID != active.identity.WorkerSessionID ||
		correlation.WorkerRunID != active.identity.WorkerRunID ||
		correlation.AgentTurnID != active.identity.AgentTurnID {
		return fmt.Errorf("model observation identity does not match the active AgentTurn")
	}
	metadataJSON, err := json.Marshal(correlation)
	if err != nil {
		return fmt.Errorf("encode model observation metadata: %w", err)
	}
	status := "ok"
	errorKind := ""
	if observation.Error != "" {
		status = "error"
		errorKind = "model_error"
	}
	_, err = c.sender.send(active.identity.AgentTurnID, &workercontrolv1.WorkerEnvelope_ModelObservation{
		ModelObservation: &workercontrolv1.ModelObservation{
			ModelObservationId: correlation.RequestID,
			Model:              observation.Model,
			Route:              observation.Route,
			StartedAt:          timestamppb.New(observation.StartedAt),
			EndedAt:            timestamppb.New(observation.EndedAt),
			LatencyMs:          int64(observation.LatencyMS),
			InputTokens:        observation.Usage.InputTokens,
			OutputTokens:       observation.Usage.OutputTokens,
			TotalTokens:        observation.Usage.TotalTokens,
			CostUsd:            observation.CostUSD,
			Status:             status,
			ErrorKind:          errorKind,
			MetadataJson:       metadataJSON,
		},
	})
	if err != nil {
		return fmt.Errorf("send model observation: %w", err)
	}
	return nil
}

func (c *Client) abortActive() {
	active := c.active
	if active != nil {
		active.abort()
	}
}

func (a *activeTurn) cancelCurrent() {
	a.lifecycleMu.Lock()
	cancel := a.cancel
	a.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *activeTurn) abort() {
	a.lifecycleMu.Lock()
	a.aborted = true
	cancel := a.cancel
	a.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *activeTurn) beginFinalization(cancel context.CancelFunc) bool {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.aborted {
		cancel()
		return false
	}
	a.cancel = cancel
	return true
}

func (c *Client) requireNoPendingTerminal() error {
	if c.pendingTerminal != 0 {
		return fmt.Errorf("Backend has not acknowledged AgentTurn terminal sequence %d", c.pendingTerminal)
	}
	return nil
}

func (c *Client) requestTurnEvent(
	ctx context.Context,
	identity sharedevidence.TurnIdentity,
	payload any,
) error {
	if ctx == nil {
		return fmt.Errorf("Turn event context is required")
	}
	response := make(chan error, 1)
	request := turnEvent{ctx: ctx, identity: identity, payload: payload, response: response}
	select {
	case c.turnEvents <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.runContext.Done():
		return c.runContext.Err()
	}
	select {
	case err := <-response:
		return err
	default:
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		select {
		case err := <-response:
			return err
		default:
		}
		return c.cancelStreamForTurnEvent(payload, ctx.Err())
	case <-c.runContext.Done():
		return context.Cause(c.runContext)
	}
}

func (c *Client) cancelStreamForTurnEvent(payload any, err error) error {
	eventErr := turnEventContextError(payload, err)
	if c.cancelStream != nil {
		c.cancelStream(eventErr)
	}
	return eventErr
}

func turnEventContextError(payload any, err error) error {
	switch payload.(type) {
	case *workercontrolv1.WorkerEnvelope_TurnCompleted,
		*workercontrolv1.WorkerEnvelope_TurnFailed:
		return fmt.Errorf(
			"AgentTurn terminal event context ended after enqueue: terminality is unknown: %w",
			err,
		)
	default:
		return fmt.Errorf("Turn event context ended after enqueue: %w", err)
	}
}

func (c *Client) handleTurnEvent(event turnEvent) error {
	active := c.active
	if active == nil || active.identity != event.identity {
		return fmt.Errorf("Turn event identity does not match the active AgentTurn")
	}
	if err := event.ctx.Err(); err != nil {
		return c.cancelStreamForTurnEvent(event.payload, err)
	}
	switch event.payload.(type) {
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		if active.terminalAttempted.Load() {
			return fmt.Errorf("AgentTurn terminal event was already attempted")
		}
		_, err := c.sender.send(active.identity.AgentTurnID, event.payload)
		if err != nil {
			return fmt.Errorf("send TurnStarted: %w", err)
		}
		if err := event.ctx.Err(); err != nil {
			return c.cancelStreamForTurnEvent(event.payload, err)
		}
		return nil
	case *workercontrolv1.WorkerEnvelope_TurnCompleted,
		*workercontrolv1.WorkerEnvelope_TurnFailed:
		if !active.terminalAttempted.CompareAndSwap(false, true) {
			return fmt.Errorf("AgentTurn terminal event was already sent")
		}
		sequence, err := c.sender.send(active.identity.AgentTurnID, event.payload)
		if err != nil {
			return fmt.Errorf("send AgentTurn terminal event: terminality is unknown: %w", err)
		}
		if err := event.ctx.Err(); err != nil {
			return c.cancelStreamForTurnEvent(event.payload, err)
		}
		active.terminalSent.Store(true)
		if c.terminalTurns == nil {
			c.terminalTurns = make(map[string]struct{})
		}
		c.terminalTurns[active.identity.AgentTurnID] = struct{}{}
		if c.lastBackendAck < sequence {
			c.pendingTerminal = sequence
		}
		return nil
	default:
		return fmt.Errorf("Turn event payload %T is unsupported", event.payload)
	}
}

type streamSender struct {
	mu       sync.Mutex
	stream   workercontrolv1.WorkerControl_ConnectClient
	identity RuntimeIdentity
	now      func() time.Time
	sequence uint64
	ack      uint64
}

func (s *streamSender) send(
	turnID string,
	payload any,
) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	envelope := &workercontrolv1.WorkerEnvelope{
		Meta: &workercontrolv1.EnvelopeMeta{
			MessageId:      ids.New(),
			StreamSequence: s.sequence,
			AckSequence:    s.ack,
			Identity: &workercontrolv1.Identity{
				WorkerSessionId:   s.identity.WorkerSessionID,
				WorkerRunId:       s.identity.WorkerRunID,
				RuntimeInstanceId: s.identity.RuntimeInstanceID,
				RuntimeGeneration: s.identity.RuntimeGeneration,
				AgentTurnId:       turnID,
			},
			SentAt: timestamppb.New(s.now().UTC()),
		},
	}
	switch typed := payload.(type) {
	case *workercontrolv1.WorkerEnvelope_RuntimeReady:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_AgentSessionReady:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_TurnStarted:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_AcpUpdate:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_ModelObservation:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_TurnCompleted:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_TurnFailed:
		envelope.Payload = typed
	case *workercontrolv1.WorkerEnvelope_RuntimeDiagnostic:
		envelope.Payload = typed
	default:
		return s.sequence, fmt.Errorf("Worker envelope payload %T is unsupported", payload)
	}
	if proto.Size(envelope) > maxEnvelopeBytes {
		return s.sequence, fmt.Errorf("Worker envelope exceeds %d byte limit", maxEnvelopeBytes)
	}
	if err := s.stream.Send(envelope); err != nil {
		return s.sequence, err
	}
	return s.sequence, nil
}

func (s *streamSender) acknowledge(sequence uint64) {
	s.mu.Lock()
	s.ack = sequence
	s.mu.Unlock()
}

func (s *streamSender) sentSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence
}

func validateBackendEnvelope(
	envelope *workercontrolv1.BackendEnvelope,
	identity RuntimeIdentity,
) error {
	if envelope == nil || envelope.GetMeta() == nil || envelope.GetPayload() == nil {
		return fmt.Errorf("Backend envelope metadata and payload are required")
	}
	if proto.Size(envelope) > maxEnvelopeBytes {
		return fmt.Errorf("Backend envelope exceeds %d byte limit", maxEnvelopeBytes)
	}
	meta := envelope.GetMeta()
	if meta.GetMessageId() == "" || meta.GetSentAt() == nil {
		return fmt.Errorf("Backend envelope message ID and sent_at are required")
	}
	if err := meta.GetSentAt().CheckValid(); err != nil {
		return fmt.Errorf("Backend envelope sent_at is invalid: %w", err)
	}
	actual := meta.GetIdentity()
	if actual == nil ||
		actual.GetWorkerSessionId() != identity.WorkerSessionID ||
		actual.GetWorkerRunId() != identity.WorkerRunID ||
		actual.GetRuntimeInstanceId() != identity.RuntimeInstanceID ||
		actual.GetRuntimeGeneration() != identity.RuntimeGeneration {
		return fmt.Errorf("Backend envelope runtime identity does not match")
	}
	turnRequired := false
	switch envelope.GetPayload().(type) {
	case *workercontrolv1.BackendEnvelope_InitializeSession,
		*workercontrolv1.BackendEnvelope_ShutdownRuntime:
	case *workercontrolv1.BackendEnvelope_StartAgentTurn,
		*workercontrolv1.BackendEnvelope_CancelAgentTurn:
		turnRequired = true
	default:
		return fmt.Errorf("Backend envelope payload is unsupported")
	}
	if turnRequired != (actual.GetAgentTurnId() != "") {
		return fmt.Errorf("Backend envelope AgentTurn identity is invalid for payload")
	}
	return nil
}

func validateRuntimeIdentity(identity RuntimeIdentity) error {
	if !validIdentityPart(identity.WorkerSessionID) ||
		!validIdentityPart(identity.WorkerRunID) ||
		!validIdentityPart(identity.RuntimeInstanceID) ||
		identity.RuntimeGeneration != 1 {
		return fmt.Errorf("runtime identity is invalid")
	}
	return nil
}

func validIdentityPart(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "/\\\r\n\t ")
}

func validToken(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, " \t\r\n\x00")
}

func cleanAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func evidencePointers(refs []sharedevidence.ObjectRef) []*workercontrolv1.EvidencePointer {
	pointers := make([]*workercontrolv1.EvidencePointer, 0, len(refs))
	for _, ref := range refs {
		pointers = append(pointers, &workercontrolv1.EvidencePointer{
			Kind:       ref.Kind,
			ObjectPath: ref.Path,
			Sha256:     ref.SHA256,
			SizeBytes:  ref.Bytes,
		})
	}
	return pointers
}

var _ upload.TerminalSink = (*Client)(nil)
