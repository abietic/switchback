// Package shimruntime owns the trusted root Worker Shim process lifecycle.
package shimruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/adapter/codexacp"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/modelobs"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	"github.com/abietic/switchback/internal/workerruntime/application"
	sdk "github.com/coder/acp-go-sdk"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	liteLLMBinary       = "/usr/local/bin/litellm"
	liteLLMConfigPath   = "/opt/switchback/litellm/config.yaml"
	liteLLMReadyURL     = "http://127.0.0.1:4000/health/liveliness"
	workingCopyPath     = "/workspace"
	traceSpoolPath      = "/var/lib/switchback/trace-spool"
	readinessTimeout    = 60 * time.Second
	heartbeatInterval   = 15 * time.Second
	controlDrainTimeout = 5 * time.Second
)

type controlStream interface {
	SendEvent(application.WorkerControlEvent) error
	ReceiveCommand() (application.WorkerControlCommand, error)
	CloseSend() error
}

type runtimeControl interface {
	Run(context.Context) error
	Publish(context.Context, application.WorkerControlEvent) error
	TryPublishHeartbeat(application.WorkerControlEvent) error
	Commands() <-chan application.WorkerControlCommand
	MarkApplied(uint64) error
	BeginTurn(application.WorkerControlCommand) error
	MarkPromptIntent(string) error
	MarkPromptActive(string) error
	PublishTurnUpdate(context.Context, application.WorkerControlEvent, bool) error
	ReadyTransitions() <-chan controlgrpc.ClientReadyTransition
	Drain(context.Context) error
}

type connectedControl struct {
	client runtimeControl
	close  func() error
}

type runtimeLoopOptions struct {
	heartbeatInterval time.Duration
	drainTimeout      time.Duration
}

type runtimeControlEventStream struct {
	ctx     context.Context
	control runtimeControl
}

func (stream runtimeControlEventStream) SendEvent(event application.WorkerControlEvent) error {
	return stream.control.Publish(stream.ctx, event)
}

func (runtimeControlEventStream) ReceiveCommand() (application.WorkerControlCommand, error) {
	return application.WorkerControlCommand{}, errors.New("reconnecting Runtime control uses Commands")
}

func (runtimeControlEventStream) CloseSend() error { return nil }

// Run starts the production co-process runtime and blocks until the Backend
// closes it, the context is cancelled, or a required child/transport fails.
func Run(ctx context.Context, stdout, stderr io.Writer) error {
	_ = stderr
	inputs, err := runtimesecret.LoadRuntimeInputs()
	if err != nil {
		return err
	}
	defer zero(inputs.Credential)
	if err := validateBackendEndpoints(os.Getenv("SWITCHBACK_BACKEND_GRPC_TARGET"), os.Getenv("SWITCHBACK_BACKEND_HTTP_BASE_URL")); err != nil {
		return err
	}
	if err := os.MkdirAll(traceSpoolPath, 0o700); err != nil {
		return fmt.Errorf("prepare Runtime trace spool")
	}
	if err := prepareAgentDirectories([]string{
		"/var/lib/switchback/agent-state/codex",
	}, prepareAgentDirectory); err != nil {
		return err
	}
	observations := make(chan modelobs.Observation, 128)
	binder := modelobs.NewRequestBinder()
	observationServer, err := modelobs.NewServer(inputs.Bootstrap.ModelSinkToken, binder, func(observation modelobs.Observation) error {
		select {
		case observations <- observation:
			return nil
		default:
			return fmt.Errorf("model observation buffer is full")
		}
	})
	if err != nil {
		return err
	}
	if err := observationServer.Start(); err != nil {
		return err
	}
	defer observationServer.Shutdown(context.Background())
	liteLLMLog, err := openRuntimeLog(filepath.Join(traceSpoolPath, "litellm.log"))
	if err != nil {
		return err
	}
	defer liteLLMLog.Close()
	proxy, err := startModelProxy(ctx, inputs.Bootstrap, inputs.Credential, liteLLMLog)
	if err != nil {
		return err
	}
	zero(inputs.Credential)
	inputs.Credential = nil
	defer proxy.Close(context.Background())

	trace, err := openRuntimeLog(filepath.Join(traceSpoolPath, "acp.raw.jsonl"))
	if err != nil {
		return err
	}
	defer trace.Close()
	agent, err := codexacp.New(trace)
	if err != nil {
		return err
	}
	defer agent.Close(context.Background())
	capabilities, agentSessionID, err := startAgent(ctx, agent, inputs.Bootstrap)
	if err != nil {
		return err
	}

	control, err := connectControl(ctx, inputs.Bootstrap, os.Getenv("SWITCHBACK_BACKEND_GRPC_TARGET"))
	if err != nil {
		return err
	}
	defer control.close()
	if stdout != nil {
		_, _ = fmt.Fprintf(stdout, "worker runtime ready profile=%s generation=%d\n", inputs.Bootstrap.AgentProfileID, inputs.Bootstrap.RuntimeGeneration)
	}
	return serveReconnectableRuntime(
		ctx,
		control.client,
		proxy.Done(),
		agent,
		inputs.Bootstrap,
		agentSessionID,
		capabilities,
		binder,
		observations,
		observationServer.Errors(),
		runtimeLoopOptions{
			heartbeatInterval: heartbeatInterval,
			drainTimeout:      controlDrainTimeout,
		},
	)
}

func startAgent(ctx context.Context, agent application.AgentAdapter, bootstrap runtimesecret.Bootstrap) (application.AgentCapabilities, string, error) {
	capabilities, err := agent.Initialize(ctx, application.AgentInitializeRequest{
		WorkerSessionID: bootstrap.WorkerSessionID,
		RuntimeID:       bootstrap.RuntimeInstanceID,
		Generation:      bootstrap.RuntimeGeneration,
		ProtocolVersion: strconv.Itoa(sdk.ProtocolVersionNumber),
	})
	if err != nil {
		return application.AgentCapabilities{}, "", fmt.Errorf("initialize production ACP Agent: %w", err)
	}
	if err := agent.Authenticate(ctx, bootstrap.ProxyToken); err != nil {
		return application.AgentCapabilities{}, "", fmt.Errorf("authenticate production ACP Agent: %w", err)
	}
	agentSessionID, err := agent.OpenSession(ctx, application.AgentSessionRequest{
		WorkerSessionID: bootstrap.WorkerSessionID,
		AgentSessionID:  bootstrap.AgentSessionID,
		WorkingCopyPath: workingCopyPath,
		LoadExisting:    bootstrap.LoadExistingSession,
	})
	if err != nil {
		return application.AgentCapabilities{}, "", fmt.Errorf("open production ACP AgentSession: %w", err)
	}
	return capabilities, agentSessionID, nil
}

func connectControl(ctx context.Context, bootstrap runtimesecret.Bootstrap, target string) (connectedControl, error) {
	connection, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return connectedControl{}, fmt.Errorf("create WorkerControl connection")
	}
	credential := controlgrpc.Credential{
		Identity: controlgrpc.RuntimeIdentity{
			WorkerSessionID: bootstrap.WorkerSessionID, RuntimeInstanceID: bootstrap.RuntimeInstanceID,
			RuntimeGeneration: uint32(bootstrap.RuntimeGeneration), ChannelEpoch: bootstrap.ChannelEpoch,
		},
		Token: bootstrap.WorkerControlToken,
	}
	client, err := controlgrpc.NewClient(controlgrpc.NewWorkerControlRPCClient(connection), credential)
	if err != nil {
		_ = connection.Close()
		return connectedControl{}, fmt.Errorf("configure WorkerControl client")
	}
	return connectedControl{client: client, close: connection.Close}, nil
}

func serveReconnectableRuntime(
	ctx context.Context,
	control runtimeControl,
	proxyDone <-chan struct{},
	agent application.AgentAdapter,
	bootstrap runtimesecret.Bootstrap,
	agentSessionID string,
	capabilities application.AgentCapabilities,
	binder *modelobs.RequestBinder,
	observations <-chan modelobs.Observation,
	observationErrors <-chan error,
	options runtimeLoopOptions,
) error {
	if control == nil {
		return fmt.Errorf("Runtime control client is required")
	}
	if options.heartbeatInterval <= 0 {
		options.heartbeatInterval = heartbeatInterval
	}
	if options.drainTimeout <= 0 {
		options.drainTimeout = controlDrainTimeout
	}
	publisher := runtimeControlEventStream{ctx: ctx, control: control}
	if err := sendRuntimeReady(publisher, bootstrap, agentSessionID, capabilities); err != nil {
		return fmt.Errorf("spool Runtime ready: %w", err)
	}
	controlCtx, cancelControl := context.WithCancel(ctx)
	controlResult := make(chan error, 1)
	go func() {
		defer close(controlResult)
		controlResult <- control.Run(controlCtx)
	}()
	result := serveReconnectableCommands(
		ctx,
		control,
		proxyDone,
		agent,
		bootstrap,
		agentSessionID,
		binder,
		observations,
		observationErrors,
		controlResult,
		options,
	)
	cancelControl()
	runErr, controlResultOpen := <-controlResult
	if result != nil {
		if ctx.Err() != nil && errors.Is(result, context.Canceled) {
			return nil
		}
		return result
	}
	if !controlResultOpen {
		return nil
	}
	if ctx.Err() != nil || errors.Is(runErr, context.Canceled) {
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("WorkerControl client stopped: %w", runErr)
	}
	return nil
}

func serveReconnectableCommands(
	ctx context.Context,
	control runtimeControl,
	proxyDone <-chan struct{},
	agent application.AgentAdapter,
	bootstrap runtimesecret.Bootstrap,
	agentSessionID string,
	binder *modelobs.RequestBinder,
	observations <-chan modelobs.Observation,
	observationErrors <-chan error,
	controlResult <-chan error,
	options runtimeLoopOptions,
) error {
	heartbeats := time.NewTicker(options.heartbeatInterval)
	defer heartbeats.Stop()
	readyTransitions := control.ReadyTransitions()
	publisher := runtimeControlEventStream{ctx: ctx, control: control}
	var active *application.WorkerControlCommand
	var updates <-chan application.AgentUpdate
	var cancelActivePrompt context.CancelFunc
	defer func() {
		if cancelActivePrompt != nil {
			cancelActivePrompt()
		}
	}()
	updateSequence := uint64(0)
	// Keep one immutable pending event; retries never regenerate its identity.
	var pending *application.WorkerControlEvent
	var pendingTurn, pendingTerminal bool
	var terminalUpdate *application.AgentUpdate
	terminalObservations := 0
	heartbeatSequence := uint64(0)
	processedCommands := make(map[uint64]string)
	publishHeartbeat := func() error {
		heartbeatSequence++
		event := heartbeatEvent(bootstrap, agentSessionID, heartbeatSequence)
		if err := control.TryPublishHeartbeat(event); err != nil &&
			!errors.Is(err, controlgrpc.ErrBackpressure) {
			return fmt.Errorf("spool Runtime heartbeat: %w", err)
		}
		return nil
	}
	for {
		var capacityChanged <-chan struct{}
		if terminalUpdate != nil && pending == nil {
			if terminalObservations > 0 {
				select {
				case observation, open := <-observations:
					if !open {
						terminalObservations = 0
						observations = nil
						continue
					}
					event, err := modelObservationEvent(bootstrap, agentSessionID, observation)
					if err != nil {
						return err
					}
					pending = &event
					pendingTurn, pendingTerminal = false, false
					terminalObservations--
				default:
					terminalObservations = 0
					continue
				}
			} else {
				updateSequence++
				event, terminal := agentUpdateEvent(*active, *terminalUpdate, updateSequence)
				pending, pendingTurn, pendingTerminal = &event, true, terminal
				terminalUpdate = nil
			}
		}
		if pending != nil {
			if source, ok := control.(interface{ CapacityChanged() <-chan struct{} }); ok {
				// Snapshot before Publish, never after observing ErrBackpressure.
				capacityChanged = source.CapacityChanged()
			}
			var err error
			if pendingTurn {
				err = control.PublishTurnUpdate(ctx, *pending, pendingTerminal)
			} else {
				err = control.Publish(ctx, *pending)
			}
			if err == nil {
				pending = nil
				if pendingTerminal {
					cancelActivePrompt()
					cancelActivePrompt = nil
					_ = binder.Deactivate(turnCorrelation(*active))
					active, updates = nil, nil
				}
				pendingTerminal = false
				continue
			}
			if !errors.Is(err, controlgrpc.ErrBackpressure) || capacityChanged == nil {
				return fmt.Errorf("spool AgentTurn update: %w", err)
			}
		}
		consumeUpdates, consumeObservations := updates, observations
		if pending != nil || terminalUpdate != nil {
			consumeUpdates, consumeObservations = nil, nil
		}
		select {
		case <-capacityChanged:
		case <-ctx.Done():
			return nil
		case <-proxyDone:
			return fmt.Errorf("LiteLLM child exited unexpectedly")
		case err := <-controlResult:
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				return fmt.Errorf("WorkerControl client stopped unexpectedly")
			}
			return fmt.Errorf("WorkerControl client stopped: %w", err)
		case transition, open := <-readyTransitions:
			if !open {
				readyTransitions = nil
				continue
			}
			if err := criticalReadyTransitionError(
				transition,
				active,
			); err != nil {
				if active != nil {
					_ = binder.Deactivate(turnCorrelation(*active))
				}
				return err
			}
		case err, open := <-observationErrors:
			if open && err != nil {
				return fmt.Errorf("model observation sink stopped unexpectedly")
			}
			observationErrors = nil
		case command, open := <-control.Commands():
			if !open {
				return fmt.Errorf("WorkerControl command delivery stopped unexpectedly")
			}
			commandDigest, err := runtimeCommandDigest(command)
			if err != nil {
				return fmt.Errorf("digest WorkerControl command: %w", err)
			}
			if previous, exists := processedCommands[command.Envelope.Sequence]; exists {
				if previous != commandDigest {
					return fmt.Errorf("WorkerControl command sequence %d conflicts with an applied command", command.Envelope.Sequence)
				}
				continue
			}
			if command.Envelope.MessageType == "runtime.stop" {
				if err := control.MarkApplied(command.Envelope.Sequence); err != nil {
					return fmt.Errorf("apply Runtime stop command: %w", err)
				}
				processedCommands[command.Envelope.Sequence] = commandDigest
				drainCtx, cancel := context.WithTimeout(ctx, options.drainTimeout)
				err := control.Drain(drainCtx)
				cancel()
				if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
					return fmt.Errorf("drain Runtime control journal: %w", err)
				}
				return nil
			}
			if command.Envelope.MessageType == "agent_turn.interrupt" {
				if active == nil || command.Envelope.AgentSessionID != contractsv1.AgentSessionID(agentSessionID) ||
					command.Envelope.AgentTurnID != active.Envelope.AgentTurnID {
					if err := sendCommandRejected(publisher, command, "stale_or_inactive_turn"); err != nil {
						return err
					}
				} else {
					if err := agent.Cancel(ctx, agentSessionID, string(active.Envelope.AgentTurnID)); err != nil {
						return fmt.Errorf("cancel production ACP AgentTurn: %w", err)
					}
				}
				if err := control.MarkApplied(command.Envelope.Sequence); err != nil {
					return fmt.Errorf("apply AgentTurn interrupt command: %w", err)
				}
				processedCommands[command.Envelope.Sequence] = commandDigest
				continue
			}
			if command.Envelope.MessageType != "agent_turn.start" {
				if err := sendCommandRejected(publisher, command, "unsupported_command"); err != nil {
					return err
				}
				if err := control.MarkApplied(command.Envelope.Sequence); err != nil {
					return fmt.Errorf("apply unsupported WorkerControl command: %w", err)
				}
				processedCommands[command.Envelope.Sequence] = commandDigest
				continue
			}
			if active != nil {
				if err := sendCommandRejected(publisher, command, "agent_turn_busy"); err != nil {
					return err
				}
				return fmt.Errorf("reject AgentTurn start while another Turn is active")
			}
			prompt, err := decodeStartCommand(command, bootstrap, agentSessionID)
			if err != nil {
				if sendErr := sendCommandRejected(publisher, command, "invalid_start_command"); sendErr != nil {
					return sendErr
				}
				return fmt.Errorf("reject invalid AgentTurn start command")
			}
			correlation := turnCorrelation(command)
			if err := control.BeginTurn(command); err != nil {
				return fmt.Errorf("record AgentTurn start effect: %w", err)
			}
			if err := binder.Activate(correlation); err != nil {
				if sendErr := sendCommandRejected(publisher, command, "model_correlation_unavailable"); sendErr != nil {
					return sendErr
				}
				return fmt.Errorf("reject AgentTurn start without model correlation")
			}
			if err := control.MarkPromptIntent(string(command.Envelope.AgentTurnID)); err != nil {
				_ = binder.Deactivate(correlation)
				return fmt.Errorf("record ACP prompt intent: %w", err)
			}
			turnUpdates, cancelPrompt, err := awaitAgentPrompt(
				ctx,
				agent,
				application.AgentPromptRequest{
					AgentSessionID: agentSessionID,
					AgentTurnID:    string(command.Envelope.AgentTurnID),
					Content:        prompt,
				},
				command,
				agentPromptStartMonitors{
					readyTransitions:  readyTransitions,
					proxyDone:         proxyDone,
					controlResult:     controlResult,
					observationErrors: observationErrors,
					heartbeats:        heartbeats.C,
					publishHeartbeat:  publishHeartbeat,
				},
			)
			if err != nil {
				_ = binder.Deactivate(correlation)
				return fmt.Errorf("start production ACP AgentTurn: %w", err)
			}
			cancelActivePrompt = cancelPrompt
			if err := control.MarkPromptActive(string(command.Envelope.AgentTurnID)); err != nil {
				_ = binder.Deactivate(correlation)
				return fmt.Errorf("record active ACP prompt: %w", err)
			}
			effect, err := activeTurnEffectEvent(command)
			if err != nil {
				_ = binder.Deactivate(correlation)
				return err
			}
			if err := control.PublishTurnUpdate(ctx, effect, false); err != nil {
				_ = binder.Deactivate(correlation)
				return fmt.Errorf("spool active AgentTurn effect: %w", err)
			}
			processedCommands[command.Envelope.Sequence] = commandDigest
			active = &command
			updates = turnUpdates
			updateSequence = 0
		case update, open := <-consumeUpdates:
			if active == nil {
				continue
			}
			if !open {
				_ = binder.Deactivate(turnCorrelation(*active))
				return fmt.Errorf("production ACP AgentTurn ended without a terminal update")
			}
			if update.Kind == "terminal" || update.Kind == "error" {
				// Drain the finite accepted observation prefix through the same
				// cancellable control select before publishing the terminal proof.
				terminalUpdate = &update
				terminalObservations = len(observations)
				continue
			}
			updateSequence++
			event, terminal := agentUpdateEvent(*active, update, updateSequence)
			pending, pendingTurn, pendingTerminal = &event, true, terminal
		case observation, open := <-consumeObservations:
			if !open {
				observations = nil
				continue
			}
			event, err := modelObservationEvent(bootstrap, agentSessionID, observation)
			if err != nil {
				return err
			}
			pending, pendingTurn, pendingTerminal = &event, false, false
		case <-heartbeats.C:
			if err := publishHeartbeat(); err != nil {
				return err
			}
		}
	}
}

func turnCorrelation(command application.WorkerControlCommand) modelobs.Correlation {
	return modelobs.Correlation{
		TraceID:         "langfuse-" + string(command.Envelope.AgentTurnID),
		WorkerSessionID: string(command.Envelope.WorkerSessionID),
		WorkerRunID:     string(command.Envelope.WorkerRunID),
		AgentTurnID:     string(command.Envelope.AgentTurnID),
	}
}

func runtimeCommandDigest(command application.WorkerControlCommand) (string, error) {
	envelope := command.Envelope
	if len(command.Payload) != 0 {
		envelope.Payload = append(json.RawMessage(nil), command.Payload...)
	}
	return contractsv1.RuntimeControlEnvelopeDigest(envelope)
}

type agentPromptStartResult struct {
	updates <-chan application.AgentUpdate
	err     error
}

type agentPromptStartMonitors struct {
	readyTransitions  <-chan controlgrpc.ClientReadyTransition
	proxyDone         <-chan struct{}
	controlResult     <-chan error
	observationErrors <-chan error
	heartbeats        <-chan time.Time
	publishHeartbeat  func() error
}

func awaitAgentPrompt(
	ctx context.Context,
	agent application.AgentAdapter,
	request application.AgentPromptRequest,
	command application.WorkerControlCommand,
	monitors agentPromptStartMonitors,
) (<-chan application.AgentUpdate, context.CancelFunc, error) {
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	result := make(chan agentPromptStartResult, 1)
	go func() {
		updates, err := agent.Prompt(promptCtx, request)
		result <- agentPromptStartResult{updates: updates, err: err}
	}()
	readyTransitions := monitors.readyTransitions
	observationErrors := monitors.observationErrors
	for {
		select {
		case <-ctx.Done():
			cancelPrompt()
			return nil, nil, ctx.Err()
		case <-monitors.proxyDone:
			cancelPrompt()
			return nil, nil, fmt.Errorf("LiteLLM child exited unexpectedly")
		case err, open := <-monitors.controlResult:
			cancelPrompt()
			if !open || err == nil {
				return nil, nil, fmt.Errorf("WorkerControl client stopped unexpectedly")
			}
			return nil, nil, fmt.Errorf("WorkerControl client stopped: %w", err)
		case err, open := <-observationErrors:
			if open && err != nil {
				cancelPrompt()
				return nil, nil, fmt.Errorf(
					"model observation sink stopped unexpectedly",
				)
			}
			observationErrors = nil
		case <-monitors.heartbeats:
			if err := monitors.publishHeartbeat(); err != nil {
				cancelPrompt()
				return nil, nil, err
			}
		case started := <-result:
			if started.err != nil {
				cancelPrompt()
				return nil, nil, started.err
			}
			return started.updates, cancelPrompt, nil
		case transition, open := <-readyTransitions:
			if !open {
				readyTransitions = nil
				continue
			}
			if err := criticalReadyTransitionError(
				transition,
				&command,
			); err != nil {
				cancelPrompt()
				return nil, nil, err
			}
		}
	}
}

func criticalReadyTransitionError(
	transition controlgrpc.ClientReadyTransition,
	current *application.WorkerControlCommand,
) error {
	if !transition.Ready {
		return nil
	}
	switch transition.ActiveTurnDisposition {
	case contractsv1.RuntimeControlResumeContinuityUnproven:
		return fmt.Errorf(
			"%w after ResumeComplete attempt %d",
			controlgrpc.ErrTurnContinuityUnproven,
			transition.StreamAttempt,
		)
	case contractsv1.RuntimeControlResumeAlreadyTerminal:
		if current == nil {
			return nil
		}
		// ReadyTransition delivery can lag behind a newer command. Terminal
		// authority therefore applies only to its exact start.
		matches, err := authoritativeStartMatchesCommand(
			transition.AuthoritativeStart,
			*current,
		)
		if err != nil {
			return fmt.Errorf(
				"compare authoritative terminal AgentTurn: %w",
				err,
			)
		}
		if matches {
			return fmt.Errorf(
				"active AgentTurn %s is already terminal at Backend",
				current.Envelope.AgentTurnID,
			)
		}
	}
	return nil
}

func authoritativeStartMatchesCommand(
	start *contractsv1.RuntimeControlActiveTurn,
	command application.WorkerControlCommand,
) (bool, error) {
	if start == nil {
		return false, nil
	}
	digest, err := runtimeCommandDigest(command)
	if err != nil {
		return false, err
	}
	return start.WorkerRunID == command.Envelope.WorkerRunID &&
		start.AgentSessionID == command.Envelope.AgentSessionID &&
		start.AgentTurnID == command.Envelope.AgentTurnID &&
		start.StartCommandSequence == command.Envelope.Sequence &&
		start.StartMessageID == command.Envelope.MessageID &&
		start.StartIdempotencyKey == command.Envelope.IdempotencyKey &&
		start.StartEnvelopeDigest == digest, nil
}

func activeTurnEffectEvent(command application.WorkerControlCommand) (application.WorkerControlEvent, error) {
	digest, err := runtimeCommandDigest(command)
	if err != nil {
		return application.WorkerControlEvent{}, fmt.Errorf("digest AgentTurn start command: %w", err)
	}
	proof := contractsv1.RuntimeControlActiveTurn{
		WorkerRunID:          command.Envelope.WorkerRunID,
		AgentSessionID:       command.Envelope.AgentSessionID,
		AgentTurnID:          command.Envelope.AgentTurnID,
		StartCommandSequence: command.Envelope.Sequence,
		StartMessageID:       command.Envelope.MessageID,
		StartIdempotencyKey:  command.Envelope.IdempotencyKey,
		StartEnvelopeDigest:  digest,
		Phase:                contractsv1.RuntimeControlTurnEffectActive,
	}
	payload, err := json.Marshal(struct {
		ActiveTurn contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
	}{ActiveTurn: proof})
	if err != nil {
		return application.WorkerControlEvent{}, fmt.Errorf("encode active AgentTurn effect")
	}
	return commandEvent(
		command,
		"active-effect",
		0,
		"agent_turn.effect",
		payload,
	), nil
}

func agentUpdateEvent(
	command application.WorkerControlCommand,
	update application.AgentUpdate,
	sequence uint64,
) (application.WorkerControlEvent, bool) {
	terminal := update.Kind == "terminal" || update.Kind == "error"
	messageType := "agent.update"
	if terminal {
		messageType = "agent_turn.terminal"
	}
	payload, _ := json.Marshal(map[string]any{
		"update_kind": update.Kind,
		"update":      update.Payload,
	})
	return commandEvent(command, "update", sequence, messageType, payload), terminal
}

func commandEvent(
	command application.WorkerControlCommand,
	purpose string,
	ordinal uint64,
	messageType string,
	payload json.RawMessage,
) application.WorkerControlEvent {
	messageID := deterministicRuntimeMessageID(
		purpose,
		command.Envelope.MessageID,
		ordinal,
	)
	return application.WorkerControlEvent{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion:     contractsv1.RuntimeControlSchemaVersion,
		MessageID:         messageID,
		Kind:              "event",
		WorkerSessionID:   command.Envelope.WorkerSessionID,
		WorkerRunID:       command.Envelope.WorkerRunID,
		RuntimeInstanceID: command.Envelope.RuntimeInstanceID,
		RuntimeGeneration: command.Envelope.RuntimeGeneration,
		AgentSessionID:    command.Envelope.AgentSessionID,
		AgentTurnID:       command.Envelope.AgentTurnID,
		ChannelEpoch:      command.Envelope.ChannelEpoch,
		IdempotencyKey:    messageID,
		MessageType:       messageType,
		Payload:           payload,
	}}
}

func deterministicRuntimeMessageID(purpose, source string, ordinal uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", purpose, source, ordinal)))
	return fmt.Sprintf("%s-%x", purpose, sum[:16])
}

func drainModelObservations(
	stream controlStream,
	bootstrap runtimesecret.Bootstrap,
	agentSessionID string,
	observations <-chan modelobs.Observation,
) error {
	for {
		select {
		case observation := <-observations:
			if err := sendModelObservation(stream, bootstrap, agentSessionID, observation); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func sendModelObservation(
	stream controlStream,
	bootstrap runtimesecret.Bootstrap,
	agentSessionID string,
	observation modelobs.Observation,
) error {
	event, err := modelObservationEvent(bootstrap, agentSessionID, observation)
	if err != nil {
		return err
	}
	return stream.SendEvent(event)
}

func modelObservationEvent(bootstrap runtimesecret.Bootstrap, agentSessionID string, observation modelobs.Observation) (application.WorkerControlEvent, error) {
	correlation := observation.Correlation
	status := "ok"
	if observation.Error != "" {
		status = "error"
	}
	payload, err := json.Marshal(application.ModelObservation{
		ObservationID:   correlation.RequestID,
		WorkerSessionID: correlation.WorkerSessionID,
		WorkerRunID:     correlation.WorkerRunID,
		AgentTurnID:     correlation.AgentTurnID,
		RuntimeID:       bootstrap.RuntimeInstanceID,
		Generation:      bootstrap.RuntimeGeneration,
		Model:           observation.Model,
		Route:           observation.Route,
		Input:           observation.Input,
		Output:          observation.Output,
		InputTokens:     observation.Usage.InputTokens,
		OutputTokens:    observation.Usage.OutputTokens,
		StartedAt:       observation.StartedAt,
		EndedAt:         observation.EndedAt,
		Status:          status,
	})
	if err != nil {
		return application.WorkerControlEvent{}, fmt.Errorf("encode model observation")
	}
	envelope := eventEnvelope(bootstrap, correlation.WorkerRunID, agentSessionID,
		correlation.AgentTurnID, "model-observation-"+correlation.RequestID, "model.observation", payload)
	return application.WorkerControlEvent{Envelope: envelope}, nil
}

func decodeStartCommand(command application.WorkerControlCommand, bootstrap runtimesecret.Bootstrap, agentSessionID string) (string, error) {
	envelope := command.Envelope
	if envelope.WorkerSessionID != contractsv1.WorkerSessionID(bootstrap.WorkerSessionID) ||
		envelope.RuntimeInstanceID != contractsv1.RuntimeInstanceID(bootstrap.RuntimeInstanceID) ||
		envelope.RuntimeGeneration != uint32(bootstrap.RuntimeGeneration) || envelope.ChannelEpoch != bootstrap.ChannelEpoch ||
		envelope.AgentSessionID != contractsv1.AgentSessionID(agentSessionID) {
		return "", fmt.Errorf("AgentTurn start target is stale")
	}
	var payload struct {
		Prompt string `json:"prompt"`
	}
	decoder := json.NewDecoder(bytes.NewReader(command.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		payload.Prompt == "" || strings.TrimSpace(payload.Prompt) != payload.Prompt || len(payload.Prompt) > 256<<10 {
		return "", fmt.Errorf("AgentTurn prompt is invalid")
	}
	return payload.Prompt, nil
}

func sendRuntimeReady(stream controlStream, bootstrap runtimesecret.Bootstrap, agentSessionID string, capabilities application.AgentCapabilities) error {
	payload, _ := json.Marshal(map[string]any{
		"agent_session_id":       agentSessionID,
		"agent_profile_id":       bootstrap.AgentProfileID,
		"agent_profile_revision": bootstrap.AgentProfileRevision,
		"agent_profile_digest":   bootstrap.AgentProfileDigest,
		"capabilities":           capabilities,
	})
	return stream.SendEvent(application.WorkerControlEvent{Envelope: eventEnvelope(
		bootstrap, bootstrap.WorkerRunID, agentSessionID, "runtime-startup", "runtime-ready", "runtime.ready", payload,
	)})
}

func sendAgentUpdate(stream controlStream, command application.WorkerControlCommand, update application.AgentUpdate, sequence uint64) error {
	event, _ := agentUpdateEvent(command, update, sequence)
	return stream.SendEvent(event)
}

func sendCommandRejected(stream controlStream, command application.WorkerControlCommand, code string) error {
	payload, _ := json.Marshal(map[string]string{"code": code})
	envelope := command.Envelope
	envelope.MessageID += "-rejected"
	envelope.Kind = "event"
	envelope.Sequence = 0
	envelope.AckSequence = 0
	envelope.MessageType = "command.rejected"
	envelope.Payload = payload
	envelope.SentAt = time.Time{}
	return stream.SendEvent(application.WorkerControlEvent{Envelope: envelope})
}

func sendHeartbeat(stream controlStream, bootstrap runtimesecret.Bootstrap, agentSessionID string, sequence uint64) error {
	return stream.SendEvent(heartbeatEvent(bootstrap, agentSessionID, sequence))
}

func heartbeatEvent(
	bootstrap runtimesecret.Bootstrap,
	agentSessionID string,
	sequence uint64,
) application.WorkerControlEvent {
	payload, _ := json.Marshal(struct {
		HeartbeatSequence uint64 `json:"heartbeat_sequence"`
	}{HeartbeatSequence: sequence})
	envelope := eventEnvelope(bootstrap, bootstrap.WorkerRunID, agentSessionID, "runtime-heartbeat", fmt.Sprintf("heartbeat-%d", sequence), "runtime.heartbeat", payload)
	envelope.Kind = "heartbeat"
	return application.WorkerControlEvent{Envelope: envelope}
}

func eventEnvelope(bootstrap runtimesecret.Bootstrap, workerRunID, agentSessionID, agentTurnID, messageID, messageType string, payload json.RawMessage) contractsv1.RuntimeControlEnvelope {
	return contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: messageID, Kind: "event",
		WorkerSessionID: contractsv1.WorkerSessionID(bootstrap.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(workerRunID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(bootstrap.RuntimeInstanceID), RuntimeGeneration: uint32(bootstrap.RuntimeGeneration),
		AgentSessionID: contractsv1.AgentSessionID(agentSessionID), AgentTurnID: contractsv1.AgentTurnID(agentTurnID),
		ChannelEpoch: bootstrap.ChannelEpoch, IdempotencyKey: messageID, MessageType: messageType, Payload: payload,
	}
}

func validateBackendEndpoints(grpcTarget, httpBase string) error {
	host, port, err := net.SplitHostPort(grpcTarget)
	if err != nil || host == "" || port == "" || strings.Contains(grpcTarget, "://") {
		return fmt.Errorf("Backend gRPC target is invalid")
	}
	parsed, err := url.Parse(httpBase)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Backend HTTP base URL is invalid")
	}
	return nil
}

func openRuntimeLog(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Runtime trace output")
	}
	return file, nil
}

func prepareAgentDirectories(paths []string, prepare func(string, int, int) error) error {
	if prepare == nil {
		return fmt.Errorf("Agent directory preparation operation is required")
	}
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("Agent persistent directory is invalid")
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Agent persistent directory is a symlink")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Agent persistent directory")
		}
		if err := prepare(path, runtimesecret.AgentUID, runtimesecret.AgentUID); err != nil {
			return fmt.Errorf("prepare Agent persistent directory")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("Agent persistent directory is not protected")
		}
	}
	return nil
}

func prepareAgentDirectory(path string, uid, gid int) error {
	command := exec.Command("/usr/bin/install", "-d", "-m", "0700", "--", path)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{},
	}}
	if err := command.Run(); err != nil {
		return fmt.Errorf("run Agent directory preparer")
	}
	return nil
}

func zero(content []byte) {
	for index := range content {
		content[index] = 0
	}
}

type modelProxy struct {
	command *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	waitErr error
}

func startModelProxy(ctx context.Context, bootstrap runtimesecret.Bootstrap, credential runtimesecret.ProviderCredential, log io.Writer) (*modelProxy, error) {
	command, err := modelProxyCommand(bootstrap, credential, log)
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		command.Env = nil
		return nil, fmt.Errorf("start LiteLLM child")
	}
	command.Env = nil
	processGroup, err := unix.Getpgid(command.Process.Pid)
	if err != nil || processGroup != command.Process.Pid {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		return nil, fmt.Errorf("LiteLLM process group is unverified")
	}
	proxy := &modelProxy{command: command, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		proxy.mu.Lock()
		proxy.waitErr = err
		proxy.mu.Unlock()
		close(proxy.done)
	}()
	readyCtx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()
	if err := waitForModelProxy(readyCtx, proxy); err != nil {
		_ = proxy.Close(context.Background())
		return nil, err
	}
	return proxy, nil
}

func modelProxyCommand(bootstrap runtimesecret.Bootstrap, credential runtimesecret.ProviderCredential, log io.Writer) (*exec.Cmd, error) {
	specs, err := runtimesecret.ChildProcessSpecs(bootstrap, credential)
	if err != nil {
		return nil, err
	}
	command := exec.Command(liteLLMBinary, "--config", liteLLMConfigPath, "--host", "127.0.0.1", "--port", "4000")
	command.Env = append(append([]string(nil), specs.LiteLLM.Env...),
		"HOME=/var/lib/switchback-litellm", "PATH=/usr/local/bin:/usr/bin:/bin")
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Credential: &syscall.Credential{Uid: uint32(specs.LiteLLM.UID), Gid: uint32(specs.LiteLLM.GID), NoSetGroups: true},
	}
	return command, nil
}

func waitForModelProxy(ctx context.Context, proxy *modelProxy) error {
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, liteLLMReadyURL, nil)
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("LiteLLM readiness timed out")
		case <-proxy.done:
			return fmt.Errorf("LiteLLM exited before readiness")
		case <-ticker.C:
		}
	}
}

func (proxy *modelProxy) Done() <-chan struct{} { return proxy.done }

func (proxy *modelProxy) Close(ctx context.Context) error {
	if proxy == nil || proxy.command == nil || proxy.command.Process == nil {
		return nil
	}
	select {
	case <-proxy.done:
		return nil
	default:
	}
	_ = unix.Kill(-proxy.command.Process.Pid, syscall.SIGTERM)
	select {
	case <-proxy.done:
		return nil
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	_ = unix.Kill(-proxy.command.Process.Pid, syscall.SIGKILL)
	select {
	case <-proxy.done:
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("LiteLLM process group termination is unconfirmed")
	}
}
