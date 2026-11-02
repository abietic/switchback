package shimruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/modelobs"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReconnectableRuntimeKeepsActivePromptAcrossUnavailableAndSpoolsTerminalOnce(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	binder := modelobs.NewRequestBinder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			binder,
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted

	start := testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"keep working"}`),
	)
	control.commands <- start
	agent.waitPrompt(t)

	control.readyTransitions <- controlgrpc.ClientReadyTransition{
		Ready:         false,
		StreamAttempt: 1,
		Cause:         status.Error(codes.Unavailable, "connection lost"),
	}
	agent.updates <- application.AgentUpdate{
		Kind:    "progress",
		Payload: json.RawMessage(`{"text":"still running"}`),
	}
	agent.updates <- application.AgentUpdate{
		Kind:    "terminal",
		Payload: json.RawMessage(`{"status":"completed"}`),
	}
	close(agent.updates)

	control.waitPublishedType(t, "agent_turn.terminal")
	control.commands <- start // defensive exact replay must never invoke ACP twice.
	time.Sleep(20 * time.Millisecond)
	if got := agent.promptCalls(); got != 1 {
		t.Fatalf("ACP Prompt calls = %d, want exactly one", got)
	}
	if got := control.countPublishedType("agent_turn.effect"); got != 1 {
		t.Fatalf("active effect events = %d, want one", got)
	}
	effect := control.firstPublishedType(t, "agent_turn.effect")
	var effectPayload struct {
		ActiveTurn contractsv1.RuntimeControlActiveTurn `json:"active_turn"`
	}
	if err := json.Unmarshal(effect.Envelope.Payload, &effectPayload); err != nil {
		t.Fatalf("decode active effect payload: %v", err)
	}
	startDigest, err := contractsv1.RuntimeControlEnvelopeDigest(start.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if effectPayload.ActiveTurn.StartEnvelopeDigest != startDigest ||
		effectPayload.ActiveTurn.StartCommandSequence != start.Envelope.Sequence ||
		effectPayload.ActiveTurn.StartMessageID != start.Envelope.MessageID ||
		effectPayload.ActiveTurn.Phase != contractsv1.RuntimeControlTurnEffectActive {
		t.Fatalf("active effect proof = %+v", effectPayload.ActiveTurn)
	}
	if got := control.countPublishedType("agent.update"); got != 1 {
		t.Fatalf("progress events = %d, want one", got)
	}
	if got := control.countPublishedType("agent_turn.terminal"); got != 1 {
		t.Fatalf("terminal events = %d, want one", got)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve reconnectable Runtime: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnectable Runtime did not stop after context cancellation")
	}
}

func TestReconnectableRuntimeKeepsPromptContextAliveUntilTerminalUpdate(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	agent.promptContextDone = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted

	control.commands <- testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"keep the ACP request context alive"}`),
	)
	agent.waitPrompt(t)
	control.waitPublishedType(t, "agent_turn.effect")

	select {
	case <-agent.promptContextDone:
		t.Fatal("ACP Prompt context was cancelled immediately after Prompt returned its update stream")
	case <-time.After(50 * time.Millisecond):
	}

	agent.updates <- application.AgentUpdate{
		Kind:    "terminal",
		Payload: json.RawMessage(`{"status":"completed"}`),
	}
	close(agent.updates)
	control.waitPublishedType(t, "agent_turn.terminal")

	select {
	case <-agent.promptContextDone:
	case <-time.After(time.Second):
		t.Fatal("ACP Prompt context was not released after the terminal update")
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve reconnectable Runtime: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnectable Runtime did not stop after context cancellation")
	}
}

func TestReconnectableRuntimeFailsClosedOnContinuityUnprovenReady(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	control.readyTransitions <- controlgrpc.ClientReadyTransition{
		Ready:                 true,
		StreamAttempt:         2,
		ActiveTurnDisposition: contractsv1.RuntimeControlResumeContinuityUnproven,
	}
	select {
	case err := <-result:
		if !errors.Is(err, controlgrpc.ErrTurnContinuityUnproven) {
			t.Fatalf("continuity-unproven result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("continuity-unproven ResumeComplete did not stop the Runtime")
	}
	if got := agent.promptCalls(); got != 0 {
		t.Fatalf("ACP Prompt calls=%d, want none after unproven resume", got)
	}
}

func TestReconnectableRuntimeStopsExactActiveTurnAlreadyTerminalAtBackend(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	binder := modelobs.NewRequestBinder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			binder,
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	start := testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"must stop after authoritative terminal"}`),
	)
	control.commands <- start
	agent.waitPrompt(t)
	startDigest, err := runtimeCommandDigest(start)
	if err != nil {
		t.Fatal(err)
	}
	control.readyTransitions <- controlgrpc.ClientReadyTransition{
		Ready:                 true,
		StreamAttempt:         2,
		ActiveTurnDisposition: contractsv1.RuntimeControlResumeAlreadyTerminal,
		AuthoritativeStart: &contractsv1.RuntimeControlActiveTurn{
			WorkerRunID:            start.Envelope.WorkerRunID,
			AgentSessionID:         start.Envelope.AgentSessionID,
			AgentTurnID:            start.Envelope.AgentTurnID,
			StartCommandSequence:   start.Envelope.Sequence,
			StartMessageID:         start.Envelope.MessageID,
			StartIdempotencyKey:    start.Envelope.IdempotencyKey,
			StartEnvelopeDigest:    startDigest,
			Phase:                  contractsv1.RuntimeControlTurnEffectTerminal,
			LastUpdateSequence:     3,
			TerminalEnvelopeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "already terminal") {
			t.Fatalf("already-terminal result=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("authoritative terminal did not stop the same local active Turn")
	}
	if got := control.countPublishedType("agent.update"); got != 0 {
		t.Fatalf("updates after authoritative terminal=%d, want none", got)
	}
}

func TestReconnectableRuntimeIgnoresDelayedAlreadyTerminalForDifferentTurn(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	control.readyTransitions = make(chan controlgrpc.ClientReadyTransition)
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	start := testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"current turn must survive stale terminal"}`),
	)
	control.commands <- start
	agent.waitPrompt(t)
	oldStart := start
	oldStart.Envelope.WorkerRunID = "run-old"
	oldStart.Envelope.AgentTurnID = "turn-old"
	oldStart.Envelope.MessageID = "start-old"
	oldStart.Envelope.IdempotencyKey = "start-old"
	oldStart.Payload = json.RawMessage(`{"prompt":"old turn"}`)
	oldStart.Envelope.Payload = append(json.RawMessage(nil), oldStart.Payload...)
	oldDigest, err := runtimeCommandDigest(oldStart)
	if err != nil {
		t.Fatal(err)
	}
	transitionConsumed := make(chan struct{})
	go func() {
		control.readyTransitions <- controlgrpc.ClientReadyTransition{
			Ready:                 true,
			StreamAttempt:         2,
			ActiveTurnDisposition: contractsv1.RuntimeControlResumeAlreadyTerminal,
			AuthoritativeStart: &contractsv1.RuntimeControlActiveTurn{
				WorkerRunID:            oldStart.Envelope.WorkerRunID,
				AgentSessionID:         oldStart.Envelope.AgentSessionID,
				AgentTurnID:            oldStart.Envelope.AgentTurnID,
				StartCommandSequence:   oldStart.Envelope.Sequence,
				StartMessageID:         oldStart.Envelope.MessageID,
				StartIdempotencyKey:    oldStart.Envelope.IdempotencyKey,
				StartEnvelopeDigest:    oldDigest,
				Phase:                  contractsv1.RuntimeControlTurnEffectTerminal,
				LastUpdateSequence:     3,
				TerminalEnvelopeDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		}
		close(transitionConsumed)
	}()
	select {
	case <-transitionConsumed:
	case <-time.After(time.Second):
		t.Fatal("Shim did not consume delayed already-terminal transition")
	}
	select {
	case err := <-result:
		t.Fatalf("delayed terminal stopped a different active Turn: %v", err)
	default:
	}
	agent.updates <- application.AgentUpdate{
		Kind:    "progress",
		Payload: json.RawMessage(`{"text":"still current"}`),
	}
	agent.updates <- application.AgentUpdate{
		Kind:    "terminal",
		Payload: json.RawMessage(`{"status":"completed"}`),
	}
	control.waitPublishedType(t, "agent_turn.terminal")
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("serve reconnectable Runtime: %v", err)
	}
}

func TestReconnectableRuntimeConsumesCriticalReadyWithFullBufferWhilePromptBlocks(t *testing.T) {
	for _, test := range []struct {
		name        string
		disposition string
		checkError  func(error) bool
	}{
		{
			name:        "continuity unproven",
			disposition: contractsv1.RuntimeControlResumeContinuityUnproven,
			checkError: func(err error) bool {
				return errors.Is(err, controlgrpc.ErrTurnContinuityUnproven)
			},
		},
		{
			name:        "already terminal",
			disposition: contractsv1.RuntimeControlResumeAlreadyTerminal,
			checkError: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "already terminal")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
			bootstrap := testBootstrap(profile)
			control := newFakeRuntimeControl()
			promptBlock := make(chan struct{})
			promptDone := make(chan struct{}, 1)
			var unblockPrompt sync.Once
			defer unblockPrompt.Do(func() { close(promptBlock) })
			agent := newFakeRuntimeAgent()
			agent.promptBlock = promptBlock
			agent.promptDone = promptDone
			binder := modelobs.NewRequestBinder()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- serveReconnectableRuntime(
					ctx,
					control,
					nil,
					agent,
					bootstrap,
					"agent-session-1",
					application.AgentCapabilities{},
					binder,
					nil,
					nil,
					runtimeLoopOptions{
						heartbeatInterval: time.Hour,
						drainTimeout:      50 * time.Millisecond,
					},
				)
			}()
			<-control.runStarted
			start := testStartCommand(
				bootstrap,
				"agent-session-1",
				json.RawMessage(`{"prompt":"blocked during reconnect"}`),
			)
			control.commands <- start
			agent.waitPrompt(t)
			for index := 0; index < cap(control.readyTransitions); index++ {
				control.readyTransitions <- controlgrpc.ClientReadyTransition{
					Ready:         false,
					StreamAttempt: uint64(index + 10),
				}
			}
			transition := controlgrpc.ClientReadyTransition{
				Ready:                 true,
				StreamAttempt:         20,
				ActiveTurnDisposition: test.disposition,
			}
			if test.disposition ==
				contractsv1.RuntimeControlResumeAlreadyTerminal {
				startDigest, err := runtimeCommandDigest(start)
				if err != nil {
					t.Fatal(err)
				}
				transition.AuthoritativeStart =
					&contractsv1.RuntimeControlActiveTurn{
						WorkerRunID:            start.Envelope.WorkerRunID,
						AgentSessionID:         start.Envelope.AgentSessionID,
						AgentTurnID:            start.Envelope.AgentTurnID,
						StartCommandSequence:   start.Envelope.Sequence,
						StartMessageID:         start.Envelope.MessageID,
						StartIdempotencyKey:    start.Envelope.IdempotencyKey,
						StartEnvelopeDigest:    startDigest,
						Phase:                  contractsv1.RuntimeControlTurnEffectTerminal,
						LastUpdateSequence:     3,
						TerminalEnvelopeDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
					}
			}
			transitionSent := make(chan struct{})
			go func() {
				control.readyTransitions <- transition
				close(transitionSent)
			}()
			select {
			case err := <-result:
				if !test.checkError(err) {
					t.Fatalf("critical transition result=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("critical transition did not stop blocked Prompt")
			}
			if err := binder.Bind("request-after-safety-stop"); err == nil {
				t.Fatal("safety stop left model correlation active")
			}
			select {
			case <-transitionSent:
			case <-time.After(time.Second):
				t.Fatal("critical transition sender remained blocked")
			}
			select {
			case <-promptDone:
				t.Fatal("Prompt returned before the ignored context block was released")
			default:
			}
			unblockPrompt.Do(func() { close(promptBlock) })
			select {
			case <-promptDone:
			case <-time.After(time.Second):
				t.Fatal("blocked Prompt goroutine did not finish after release")
			}
		})
	}
}

func TestReconnectableRuntimeFailsClosedOnFatalControlErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		code codes.Code
	}{
		{name: "authentication", code: codes.Unauthenticated},
		{name: "schema", code: codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
			bootstrap := testBootstrap(profile)
			control := newFakeRuntimeControl()
			agent := newFakeRuntimeAgent()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- serveReconnectableRuntime(
					ctx,
					control,
					nil,
					agent,
					bootstrap,
					"agent-session-1",
					application.AgentCapabilities{},
					modelobs.NewRequestBinder(),
					nil,
					nil,
					runtimeLoopOptions{
						heartbeatInterval: time.Hour,
						drainTimeout:      50 * time.Millisecond,
					},
				)
			}()
			<-control.runStarted
			control.runFailure <- status.Error(test.code, "control stream rejected")
			select {
			case err := <-result:
				if status.Code(err) != test.code {
					t.Fatalf("serve error = %v, want %s", err, test.code)
				}
			case <-time.After(time.Second):
				t.Fatalf("fatal WorkerControl %s failure did not stop the Runtime", test.name)
			}
			if got := control.countPublishedType("runtime.ready"); got != 1 {
				t.Fatalf("runtime.ready events = %d, want exactly one", got)
			}
			if got := agent.promptCalls(); got != 0 {
				t.Fatalf("ACP Prompt calls = %d, want none", got)
			}
		})
	}
}

func TestReconnectableRuntimeProcessesFatalSignalsWhilePromptStartBlocks(t *testing.T) {
	for _, test := range []struct {
		name         string
		proxyDone    chan struct{}
		observation  chan error
		trigger      func(*fakeRuntimeControl, chan struct{}, chan error)
		checkError   func(error) bool
		errorSummary string
	}{
		{
			name: "control authentication",
			trigger: func(
				control *fakeRuntimeControl,
				_ chan struct{},
				_ chan error,
			) {
				control.runFailure <- status.Error(
					codes.Unauthenticated,
					"control stream rejected",
				)
			},
			checkError: func(err error) bool {
				return status.Code(err) == codes.Unauthenticated
			},
			errorSummary: "fatal WorkerControl failure",
		},
		{
			name:      "LiteLLM proxy exit",
			proxyDone: make(chan struct{}),
			trigger: func(
				_ *fakeRuntimeControl,
				proxyDone chan struct{},
				_ chan error,
			) {
				close(proxyDone)
			},
			checkError: func(err error) bool {
				return err != nil &&
					strings.Contains(err.Error(), "LiteLLM child exited")
			},
			errorSummary: "LiteLLM proxy exit",
		},
		{
			name:        "observation sink failure",
			observation: make(chan error, 1),
			trigger: func(
				_ *fakeRuntimeControl,
				_ chan struct{},
				observation chan error,
			) {
				observation <- errors.New("observation sink failed")
			},
			checkError: func(err error) bool {
				return err != nil &&
					strings.Contains(err.Error(), "observation sink")
			},
			errorSummary: "model observation sink failure",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
			bootstrap := testBootstrap(profile)
			control := newFakeRuntimeControl()
			promptBlock := make(chan struct{})
			promptDone := make(chan struct{}, 1)
			var unblockPrompt sync.Once
			defer unblockPrompt.Do(func() { close(promptBlock) })
			agent := newFakeRuntimeAgent()
			agent.promptBlock = promptBlock
			agent.promptDone = promptDone
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- serveReconnectableRuntime(
					ctx,
					control,
					test.proxyDone,
					agent,
					bootstrap,
					"agent-session-1",
					application.AgentCapabilities{},
					modelobs.NewRequestBinder(),
					nil,
					test.observation,
					runtimeLoopOptions{
						heartbeatInterval: time.Hour,
						drainTimeout:      50 * time.Millisecond,
					},
				)
			}()
			<-control.runStarted
			control.commands <- testStartCommand(
				bootstrap,
				"agent-session-1",
				json.RawMessage(`{"prompt":"block while a fatal signal arrives"}`),
			)
			agent.waitPrompt(t)
			test.trigger(control, test.proxyDone, test.observation)

			select {
			case err := <-result:
				if !test.checkError(err) {
					t.Fatalf("%s result = %v", test.errorSummary, err)
				}
			case <-time.After(time.Second):
				t.Fatalf(
					"%s did not stop a blocked Prompt start",
					test.errorSummary,
				)
			}

			unblockPrompt.Do(func() { close(promptBlock) })
			select {
			case <-promptDone:
			case <-time.After(time.Second):
				t.Fatal("blocked Prompt goroutine did not finish after release")
			}
		})
	}
}

func TestReconnectableRuntimeKeepsHeartbeatAliveWhilePromptStartBlocks(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	promptBlock := make(chan struct{})
	defer close(promptBlock)
	agent := newFakeRuntimeAgent()
	agent.promptBlock = promptBlock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: 5 * time.Millisecond,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	control.commands <- testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"block without starving the heartbeat"}`),
	)
	agent.waitPrompt(t)
	control.waitPublishedType(t, "runtime.heartbeat")

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve reconnectable Runtime: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Prompt start did not stop after context cancellation")
	}
}

func TestReconnectableRuntimeAppliesReplayedInterruptAndCancelsPromptOnce(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	start := testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"long task"}`),
	)
	control.commands <- start
	agent.waitPrompt(t)
	interrupt := testRuntimeCommand(start, 2, "interrupt-2", "agent_turn.interrupt")
	control.commands <- interrupt
	control.waitApplied(t, 2)
	control.commands <- interrupt
	time.Sleep(20 * time.Millisecond)
	if got := agent.cancelCalls(); got != 1 {
		t.Fatalf("ACP Cancel calls = %d, want exactly one", got)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("serve reconnectable Runtime: %v", err)
	}
}

func TestReconnectableRuntimeHeartbeatContinuesWhileControlIsReconnecting(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: 5 * time.Millisecond,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	control.readyTransitions <- controlgrpc.ClientReadyTransition{
		Ready:         false,
		StreamAttempt: 1,
		Cause:         status.Error(codes.Unavailable, "connection lost"),
	}
	deadline := time.Now().Add(time.Second)
	for control.countPublishedType("runtime.heartbeat") < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := control.countPublishedType("runtime.heartbeat"); got < 2 {
		t.Fatalf("heartbeats while reconnecting = %d, want at least two", got)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("serve reconnectable Runtime: %v", err)
	}
}

func TestReconnectableRuntimeStopMarksAppliedAndUsesBoundedDrain(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	control.drain = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      20 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	stop := testRuntimeCommand(
		testStartCommand(bootstrap, "agent-session-1", json.RawMessage(`{"prompt":"unused"}`)),
		1,
		"runtime-stop-1",
		"runtime.stop",
	)
	startedAt := time.Now()
	control.commands <- stop
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Runtime stop: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Runtime stop did not honor bounded drain")
	}
	if elapsed := time.Since(startedAt); elapsed < 15*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("Runtime drain elapsed = %s, want bounded wait", elapsed)
	}
	control.mu.Lock()
	applied := append([]uint64(nil), control.applied...)
	drainCalls := control.drainCalls
	control.mu.Unlock()
	if len(applied) != 1 || applied[0] != 1 || drainCalls != 1 {
		t.Fatalf("applied=%v drain calls=%d", applied, drainCalls)
	}
}

func TestReconnectableRuntimeDoesNotRetryPromptAfterIntentBoundaryFailure(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	agent.promptErr = errors.New("ACP prompt outcome unknown")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	start := testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"unsafe to retry"}`),
	)
	control.commands <- start
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "start production ACP AgentTurn") {
			t.Fatalf("Prompt failure = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unknown ACP Prompt outcome did not fail closed")
	}
	if got := agent.promptCalls(); got != 1 {
		t.Fatalf("ACP Prompt calls = %d, want exactly one", got)
	}
	if got := control.countPublishedType("agent_turn.effect"); got != 0 {
		t.Fatalf("active effect events = %d, want none without activation proof", got)
	}
}

func TestReconnectableRuntimeFailsClosedWhenActivePromptEndsWithoutTerminal(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	control.commands <- testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"must have a terminal update"}`),
	)
	agent.waitPrompt(t)
	close(agent.updates)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "without a terminal update") {
			t.Fatalf("update stream close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active ACP update stream closure did not fail closed")
	}
	if got := control.countPublishedType("agent_turn.terminal"); got != 0 {
		t.Fatalf("terminal events = %d, want no fabricated terminal proof", got)
	}
}

func TestReconnectableRuntimeDoesNotApplyRejectedStartWithoutActiveProof(t *testing.T) {
	profile, _ := config.ProductionProfileRegistry().Agent(config.DeepSeekCodexOpenAIV1)
	bootstrap := testBootstrap(profile)
	control := newFakeRuntimeControl()
	agent := newFakeRuntimeAgent()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serveReconnectableRuntime(
			ctx,
			control,
			nil,
			agent,
			bootstrap,
			"agent-session-1",
			application.AgentCapabilities{},
			modelobs.NewRequestBinder(),
			nil,
			nil,
			runtimeLoopOptions{
				heartbeatInterval: time.Hour,
				drainTimeout:      50 * time.Millisecond,
			},
		)
	}()
	<-control.runStarted
	control.commands <- testStartCommand(
		bootstrap,
		"agent-session-1",
		json.RawMessage(`{"prompt":"","unknown":"field"}`),
	)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "invalid AgentTurn start") {
			t.Fatalf("invalid start error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid start did not fail closed")
	}
	control.mu.Lock()
	applied := append([]uint64(nil), control.applied...)
	control.mu.Unlock()
	if len(applied) != 0 {
		t.Fatalf("invalid start applied without active proof: %v", applied)
	}
	if got := control.countPublishedType("command.rejected"); got != 1 {
		t.Fatalf("command.rejected events = %d, want one", got)
	}
}

type fakeRuntimeControl struct {
	commands         chan application.WorkerControlCommand
	readyTransitions chan controlgrpc.ClientReadyTransition
	runStarted       chan struct{}
	runFailure       chan error

	mu         sync.Mutex
	published  []application.WorkerControlEvent
	applied    []uint64
	drainCalls int
	drain      func(context.Context) error
}

func newFakeRuntimeControl() *fakeRuntimeControl {
	return &fakeRuntimeControl{
		commands:         make(chan application.WorkerControlCommand, 8),
		readyTransitions: make(chan controlgrpc.ClientReadyTransition, 8),
		runStarted:       make(chan struct{}),
		runFailure:       make(chan error, 1),
	}
}

func (control *fakeRuntimeControl) Run(ctx context.Context) error {
	close(control.runStarted)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-control.runFailure:
		return err
	}
}

func (control *fakeRuntimeControl) Publish(_ context.Context, event application.WorkerControlEvent) error {
	control.mu.Lock()
	control.published = append(control.published, cloneWorkerEvent(event))
	control.mu.Unlock()
	return nil
}

func (control *fakeRuntimeControl) TryPublishHeartbeat(event application.WorkerControlEvent) error {
	return control.Publish(context.Background(), event)
}

func (control *fakeRuntimeControl) Commands() <-chan application.WorkerControlCommand {
	return control.commands
}

func (control *fakeRuntimeControl) MarkApplied(sequence uint64) error {
	control.mu.Lock()
	control.applied = append(control.applied, sequence)
	control.mu.Unlock()
	return nil
}

func (*fakeRuntimeControl) BeginTurn(application.WorkerControlCommand) error { return nil }

func (*fakeRuntimeControl) MarkPromptIntent(string) error { return nil }

func (*fakeRuntimeControl) MarkPromptActive(string) error { return nil }

func (control *fakeRuntimeControl) PublishTurnUpdate(
	ctx context.Context,
	event application.WorkerControlEvent,
	_ bool,
) error {
	return control.Publish(ctx, event)
}

func (control *fakeRuntimeControl) ReadyTransitions() <-chan controlgrpc.ClientReadyTransition {
	return control.readyTransitions
}

func (control *fakeRuntimeControl) Drain(ctx context.Context) error {
	control.mu.Lock()
	control.drainCalls++
	drain := control.drain
	control.mu.Unlock()
	if drain != nil {
		return drain(ctx)
	}
	return nil
}

func (control *fakeRuntimeControl) waitApplied(t *testing.T, sequence uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		control.mu.Lock()
		found := false
		for _, applied := range control.applied {
			found = found || applied == sequence
		}
		control.mu.Unlock()
		if found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("command sequence %d was not applied", sequence)
}

func (control *fakeRuntimeControl) waitPublishedType(t *testing.T, messageType string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if control.countPublishedType(messageType) != 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("event %q was not published", messageType)
}

func (control *fakeRuntimeControl) countPublishedType(messageType string) int {
	control.mu.Lock()
	defer control.mu.Unlock()
	count := 0
	for _, event := range control.published {
		if event.Envelope.MessageType == messageType {
			count++
		}
	}
	return count
}

func (control *fakeRuntimeControl) firstPublishedType(
	t *testing.T,
	messageType string,
) application.WorkerControlEvent {
	t.Helper()
	control.mu.Lock()
	defer control.mu.Unlock()
	for _, event := range control.published {
		if event.Envelope.MessageType == messageType {
			return cloneWorkerEvent(event)
		}
	}
	t.Fatalf("event %q was not published", messageType)
	return application.WorkerControlEvent{}
}

func cloneWorkerEvent(event application.WorkerControlEvent) application.WorkerControlEvent {
	event.Envelope.Payload = append(json.RawMessage(nil), event.Envelope.Payload...)
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	return event
}

type fakeRuntimeAgent struct {
	mu                sync.Mutex
	promptCount       int
	cancelCount       int
	promptErr         error
	promptBlock       <-chan struct{}
	promptDone        chan<- struct{}
	promptContextDone chan struct{}
	prompted          chan struct{}
	updates           chan application.AgentUpdate
}

func newFakeRuntimeAgent() *fakeRuntimeAgent {
	return &fakeRuntimeAgent{
		prompted: make(chan struct{}, 1),
		updates:  make(chan application.AgentUpdate, 8),
	}
}

func (*fakeRuntimeAgent) Initialize(context.Context, application.AgentInitializeRequest) (application.AgentCapabilities, error) {
	return application.AgentCapabilities{}, errors.New("unexpected Initialize")
}

func (*fakeRuntimeAgent) Authenticate(context.Context, string) error {
	return errors.New("unexpected Authenticate")
}

func (*fakeRuntimeAgent) OpenSession(context.Context, application.AgentSessionRequest) (string, error) {
	return "", errors.New("unexpected OpenSession")
}

func (agent *fakeRuntimeAgent) Prompt(ctx context.Context, _ application.AgentPromptRequest) (<-chan application.AgentUpdate, error) {
	agent.mu.Lock()
	agent.promptCount++
	promptBlock := agent.promptBlock
	promptDone := agent.promptDone
	promptContextDone := agent.promptContextDone
	agent.mu.Unlock()
	select {
	case agent.prompted <- struct{}{}:
	default:
	}
	if promptContextDone != nil {
		go func() {
			<-ctx.Done()
			promptContextDone <- struct{}{}
		}()
	}
	if promptBlock != nil {
		<-promptBlock
	}
	if promptDone != nil {
		promptDone <- struct{}{}
	}
	return agent.updates, agent.promptErr
}

func (agent *fakeRuntimeAgent) Cancel(context.Context, string, string) error {
	agent.mu.Lock()
	agent.cancelCount++
	agent.mu.Unlock()
	return nil
}

func (*fakeRuntimeAgent) Close(context.Context) error { return nil }

func (agent *fakeRuntimeAgent) waitPrompt(t *testing.T) {
	t.Helper()
	select {
	case <-agent.prompted:
	case <-time.After(time.Second):
		t.Fatal("ACP Prompt was not invoked")
	}
}

func (agent *fakeRuntimeAgent) promptCalls() int {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.promptCount
}

func (agent *fakeRuntimeAgent) cancelCalls() int {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.cancelCount
}

func testRuntimeCommand(
	base application.WorkerControlCommand,
	sequence uint64,
	messageID string,
	messageType string,
) application.WorkerControlCommand {
	payload := json.RawMessage(`{}`)
	base.Envelope.Sequence = sequence
	base.Envelope.MessageID = messageID
	base.Envelope.IdempotencyKey = messageID
	base.Envelope.MessageType = messageType
	base.Envelope.Payload = payload
	base.Envelope.SentAt = time.Now().UTC()
	base.Payload = payload
	return base
}
